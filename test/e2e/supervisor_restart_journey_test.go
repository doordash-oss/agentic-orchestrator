// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/claudeconfig"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// argv returns the latest fake harness launch's arguments.
func (h *supervisorHarness) argv() []string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(h.script), testutil.FakeSupervisorArgvFile))
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// resumeID returns the --resume value of the latest launch, or "".
func (h *supervisorHarness) resumeID() string {
	h.t.Helper()
	args := h.argv()
	for i, arg := range args {
		if arg == "--resume" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// nativeSessionFile is where the rebuilt Claude session for id lives.
func (h *supervisorHarness) nativeSessionFile(id string) string {
	return filepath.Join(claudeconfig.ProjectsDir(h.claudeConfigDir, h.stateDir), id+".jsonl")
}

// generationDir is the per-launch directory holding the PID file.
func (h *supervisorHarness) generationDir(st server.SupervisorState) string {
	return filepath.Join(h.stateDir, "supervisor", "conversations", st.ConversationID, "generations", strconv.FormatInt(st.Generation, 10))
}

// providerPID reads the PID file of the generation's provider process.
func (h *supervisorHarness) providerPID(st server.SupervisorState) int {
	h.t.Helper()
	matches, _ := filepath.Glob(filepath.Join(h.generationDir(st), "session*.pid"))
	if len(matches) != 1 {
		h.t.Fatalf("PID files in %s = %v", h.generationDir(st), matches)
	}
	pf, err := session.ReadPIDFile(matches[0])
	if err != nil {
		h.t.Fatal(err)
	}
	return pf.PID
}

func processGroupAlive(pid int) bool { return syscall.Kill(-pid, 0) == nil }

func (h *supervisorHarness) waitRecord(what string, cond func(server.SupervisorRecord) bool) {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		for _, rec := range h.transcript("").Items {
			if cond(rec) {
				return
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func recordText(rec server.SupervisorRecord) string {
	var b strings.Builder
	for _, m := range rec.Messages {
		b.WriteString(m.Text)
	}
	return b.String()
}

func markerRecords(page server.SupervisorTranscriptResponse, marker server.SupervisorMarkerRecordMarker) []server.SupervisorRecord {
	var out []server.SupervisorRecord
	for _, rec := range page.Items {
		if rec.Kind == server.SupervisorRecordKindMarker && rec.Marker != nil && rec.Marker.Marker == marker {
			out = append(out, rec)
		}
	}
	return out
}

// TestSupervisorRestartMidTurnTerminatesOrphanAndResumes walks the restart
// journey: a server dies while a turn holds after committing partial text and
// a tool call; the next boot terminates the orphaned harness without
// reattaching, marks the turn interrupted, and the next message rebuilds the
// native session and resumes it under one stable native id.
func TestSupervisorRestartMidTurnTerminatesOrphanAndResumes(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	h.send("remember the blue door", "c1")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	if id := h.resumeID(); id != "" {
		t.Fatalf("a launch with no history resumed %q", id)
	}
	h.send("now work on it "+testutil.FakeSupervisorPartial, "c2")
	h.waitRecord("partial text", func(rec server.SupervisorRecord) bool {
		return rec.Kind == server.SupervisorRecordKindAssistant && strings.Contains(recordText(rec), testutil.FakeSupervisorPartialText)
	})
	h.waitRecord("partial tool call", func(rec server.SupervisorRecord) bool { return rec.Kind == server.SupervisorRecordKindToolUse })
	before := h.state()
	pid := h.providerPID(before)
	if !processGroupAlive(pid) {
		t.Fatal("the held harness is not running")
	}
	launches := h.invocations()

	h.crash()
	st := h.state()
	if processGroupAlive(pid) {
		t.Fatalf("boot reported ready while the orphaned harness %d is alive", pid)
	}
	if st.Lifecycle != server.SupervisorLifecycleStopped || st.LastTurnOutcome != server.SupervisorTurnOutcomeInterrupted ||
		st.InterruptedBy != server.SupervisorInterruptedByShutdown || st.SessionID != "" || h.invocations() != launches {
		t.Fatalf("state after restart = %+v (launches %d -> %d)", st, launches, h.invocations())
	}
	page := h.transcript("")
	last := page.Items[len(page.Items)-1]
	if last.Kind != server.SupervisorRecordKindMarker || last.Marker.Marker != server.SupervisorMarkerInterrupted || last.TurnID != "g1.t2" {
		t.Fatalf("newest record after restart = %+v", last)
	}

	stream := h.openStream("")
	h.send("what did I ask you to remember?", "c3")
	evs := stream.until("idle after resume", isState(server.SupervisorLifecycleIdle))
	var steps []server.SupervisorStartingStep
	for _, ev := range evs {
		if ev.data.State != nil && ev.data.State.Lifecycle == server.SupervisorLifecycleStarting {
			if step := ev.data.State.StartingStep; len(steps) == 0 || steps[len(steps)-1] != step {
				steps = append(steps, step)
			}
		}
	}
	want := []server.SupervisorStartingStep{server.SupervisorStartingStepRebuilding, server.SupervisorStartingStepLaunching, server.SupervisorStartingStepHandshake}
	if strings.Join(stepNames(steps), ",") != strings.Join(stepNames(want), ",") {
		t.Fatalf("starting steps = %v, want %v", steps, want)
	}
	native := h.resumeID()
	if native == "" {
		t.Fatalf("the relaunch did not resume: argv %v", h.argv())
	}
	reply := h.transcript("").Items
	answer := recordText(reply[len(reply)-1])
	if answer != "Resumed with 2 prior messages: remember the blue door" {
		t.Fatalf("resumed reply = %q", answer)
	}
	rebuilt, err := os.ReadFile(h.nativeSessionFile(native))
	if err != nil {
		t.Fatalf("rebuilt session file: %v", err)
	}
	if !strings.Contains(string(rebuilt), "now work on it") || !strings.Contains(string(rebuilt), testutil.FakeSupervisorPartialText) {
		t.Fatalf("rebuilt file lost the cut turn's prompt or partial text:\n%s", rebuilt)
	}
	if strings.Contains(string(rebuilt), "toolu_partial") {
		t.Fatalf("rebuilt file kept the cut turn's tool records:\n%s", rebuilt)
	}

	// Two more generations reuse the native id and replace the file.
	for i, cmid := range []string{"c4", "c5"} {
		h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
		h.waitLifecycle(server.SupervisorLifecycleStopped)
		h.send("follow-up "+cmid, cmid)
		h.waitLifecycle(server.SupervisorLifecycleIdle)
		if got := h.resumeID(); got != native {
			t.Fatalf("generation %d resumed %q, want %q", i+3, got, native)
		}
		data, err := os.ReadFile(h.nativeSessionFile(native))
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(data), `"content":"remember the blue door"`); n != 1 {
			t.Fatalf("generation %d: first prompt appears %d times; the file was appended, not replaced", i+3, n)
		}
	}
}

func stepNames(steps []server.SupervisorStartingStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = string(s)
	}
	return out
}

// TestSupervisorRestartLeavesMismatchedProcessAlone proves boot only
// terminates the process its PID file identifies: a live process whose
// recorded start does not match survives.
func TestSupervisorRestartLeavesMismatchedProcessAlone(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	h.send("hello", "c1")
	st := h.waitLifecycle(server.SupervisorLifecycleIdle)
	h.restart()

	stranger := exec.Command("sleep", "60")
	stranger.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = stranger.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-stranger.Process.Pid, syscall.SIGKILL)
		<-done
	})
	if err := session.WritePIDFile(h.generationDir(st), session.PIDFile{
		PID:       stranger.Process.Pid,
		StartedAt: time.Now().Add(-time.Hour),
		ManagerID: supervisor.SessionID(st.ConversationID, st.Generation),
		Kind:      ports.KindSupervisor,
	}); err != nil {
		t.Fatal(err)
	}
	h.restart()
	if !processGroupAlive(stranger.Process.Pid) {
		t.Fatal("boot terminated a process whose identity does not match its PID file")
	}
	if got := h.state(); got.Lifecycle != server.SupervisorLifecycleStopped || got.LastTurnOutcome == server.SupervisorTurnOutcomeInterrupted {
		t.Fatalf("state after restart = %+v", got)
	}
}

// TestSupervisorLaunchFailuresKeepTranscriptAndRetry covers the other two
// failure causes: a harness that never answers its handshake and a rebuild
// that cannot write the native session. Each fails with the error marker
// and failure envelope, and the next send relaunches under a new generation.
func TestSupervisorLaunchFailuresKeepTranscriptAndRetry(t *testing.T) {
	t.Run("silent handshake", func(t *testing.T) {
		h := newSupervisorHarness(t, "while IFS= read -r line; do :; done\n", func(o *supervisor.Options) { o.HandshakeTimeout = 500 * time.Millisecond })
		h.chooseSettings()
		var failed server.ErrorResponse
		h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{"text": "hello", "client_message_id": "c1"}, http.StatusBadGateway, &failed)
		h.assertLaunchFailed(failed.Error)
		if err := os.WriteFile(h.script, []byte("#!/bin/sh\n"+testutil.FakeClaudeInteractiveScriptBody()), 0o755); err != nil {
			t.Fatal(err)
		}
		if resp := h.send("hello", "c1"); resp.Record.Generation != 2 {
			t.Fatalf("retry = %+v", resp)
		}
		if st := h.waitLifecycle(server.SupervisorLifecycleIdle); st.Failure != nil {
			t.Fatalf("failure survived the retry: %+v", st.Failure)
		}
	})
	t.Run("rebuild write failure", func(t *testing.T) {
		h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
		h.chooseSettings()
		h.send("hello", "c1")
		h.waitLifecycle(server.SupervisorLifecycleIdle)
		h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
		h.waitLifecycle(server.SupervisorLifecycleStopped)
		// A file where the projects directory belongs makes the write fail.
		projects := filepath.Join(h.claudeConfigDir, "projects")
		if err := os.WriteFile(projects, []byte("not a directory"), 0o644); err != nil {
			t.Fatal(err)
		}
		launches := h.invocations()
		var failed server.ErrorResponse
		h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{"text": "again", "client_message_id": "c2"}, http.StatusBadGateway, &failed)
		h.assertLaunchFailed(failed.Error)
		if h.invocations() != launches {
			t.Fatal("the harness launched after the rebuild failed")
		}
		if err := os.Remove(projects); err != nil {
			t.Fatal(err)
		}
		if resp := h.send("again", "c2"); resp.Record.Generation != 3 {
			t.Fatalf("retry = %+v", resp)
		}
		if st := h.waitLifecycle(server.SupervisorLifecycleIdle); st.Failure != nil {
			t.Fatalf("failure survived the retry: %+v", st.Failure)
		}
		h.restart()
		if st := h.state(); st.Failure != nil {
			t.Fatalf("failure survived a restart: %+v", st.Failure)
		}
		if got := markerRecords(h.transcript(""), server.SupervisorMarkerError); len(got) != 1 {
			t.Fatalf("error markers after restart = %d, want 1", len(got))
		}
	})
}

// TestSupervisorPermissionModeRestrictedByPolicy compares the requested and
// effective permission modes the harness reports at init.
func TestSupervisorPermissionModeRestrictedByPolicy(t *testing.T) {
	for _, tc := range []struct {
		mode       string
		restricted bool
	}{{"plan", true}, {"default", false}} {
		t.Run(tc.mode, func(t *testing.T) {
			h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBodyWithPermissionMode(tc.mode))
			h.chooseSettings()
			h.send("hello", "c1")
			st := h.waitLifecycle(server.SupervisorLifecycleIdle)
			if st.PermissionMode != (server.SupervisorPermissionMode{Requested: "default", Effective: tc.mode, RestrictedByPolicy: tc.restricted}) {
				t.Fatalf("permission_mode = %+v", st.PermissionMode)
			}
			markers := markerRecords(h.transcript(""), server.SupervisorMarkerPermissionRestricted)
			if tc.restricted && len(markers) != 1 || !tc.restricted && len(markers) != 0 {
				raw, _ := json.Marshal(markers)
				t.Fatalf("permission_restricted markers = %s", raw)
			}
		})
	}
}
