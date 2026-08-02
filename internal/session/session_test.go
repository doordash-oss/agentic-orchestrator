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

package session

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestSessionStartAndCapture(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test session state.
	var messages []llm.SDKMessage
	s := NewSession("test-session", "feat-1", feature.PhaseImplement)
	runMockSession(t, s, []llm.SDKMessage{
		{
			Type:    "system",
			Subtype: "init",
			Init: &llm.SystemInitMessage{
				SessionID: "test-sess",
				Model:     "test",
			},
		},
		{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{
					Role:    "assistant",
					Content: []llm.ContentBlock{{Type: "text", Text: "hello from mock"}},
				},
			},
		},
		{
			Type: "result",
			Result: &llm.ResultMessage{
				Type:         "result",
				Subtype:      "success",
				SessionID:    "test-sess",
				TotalCostUSD: 0.01,
			},
		},
	}, func(msg llm.SDKMessage) {
		messages = append(messages, msg)
	})

	// Check message log contains the assistant message
	if s.messageLog.Len() < 3 {
		t.Errorf("expected at least 3 messages in log, got %d", s.messageLog.Len())
	}

	// Check session ID captured from init (via protocol delegation)
	if s.SessionID() != "test-sess" {
		t.Errorf("SessionID() = %q, want test-sess", s.SessionID())
	}

	// Check cost captured from result
	if s.cost == nil || s.cost.TotalCostUSD != 0.01 {
		t.Errorf("cost = %+v, want 0.01", s.cost)
	}

	// Check statusCh received SUCCESS
	select {
	case status := <-s.statusCh:
		if status != "SUCCESS" {
			t.Errorf("statusCh = %q, want SUCCESS", status)
		}
	default:
		t.Error("statusCh empty, expected SUCCESS")
	}

	// LastStdoutAt should advance to roughly now after stdout lines were
	// read. Allow a generous upper bound to tolerate CI scheduling jitter.
	stdoutAt := s.LastStdoutAt()
	if stdoutAt.IsZero() {
		t.Error("LastStdoutAt should be non-zero after reading CLI output")
	}
	if age := time.Since(stdoutAt); age > 10*time.Second {
		t.Errorf("LastStdoutAt age = %s, want < 10s", age)
	}
}

func TestSessionInitPersistsProtocolSessionIDToPIDFile(t *testing.T) {
	dir := t.TempDir()

	s := NewSession("test-session", "feat-1", feature.PhaseImplement)
	s.pidDir = dir
	s.repoName = "repo"
	s.process = exec.Command("true")
	if err := s.process.Start(); err != nil {
		t.Fatalf("start process: %v", err)
	}
	s.protocol = newScriptedProtocol(llm.SDKMessage{
		Type:    "system",
		Subtype: "init",
		Init: &llm.SystemInitMessage{
			SessionID: "provider-session",
			Model:     "test",
		},
	})

	if err := WritePIDFile(dir, PIDFile{
		PID: s.process.Process.Pid, RepoName: "repo", FeatureID: "feat-1", Phase: feature.PhaseImplement.String(),
	}); err != nil {
		t.Fatalf("write initial PID file: %v", err)
	}

	var captured string
	runSessionWithStdoutLines(t, s, []string{"MOCK_MSG"}, func(msg llm.SDKMessage) {
		if msg.Init == nil {
			return
		}
		if pf, err := ReadPIDFile(filepath.Join(dir, PIDFileName("repo"))); err == nil {
			captured = pf.SessionID
		}
	})

	if captured != "provider-session" {
		t.Fatalf("PID file SessionID after init = %q, want provider-session", captured)
	}
	if got := s.SessionID(); got != "provider-session" {
		t.Fatalf("session.SessionID() = %q, want provider-session", got)
	}
}

func TestPendingToolWatchdogFailsIdlePendingTool(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "pending-tool-stall.sh")
	script := `#!/usr/bin/env bash
printf '%s\n' '{"type":"tool_progress","tool_use_id":"chatcmpl-tool-empty-write","tool_name":"Write","data":"pending"}'
sleep 30
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	mgr := NewManager(nil)
	defer mgr.Shutdown()

	sess, err := mgr.StartSession(
		"pending-tool-watchdog-stall",
		"feat-1",
		feature.PhasePlan,
		[]string{"bash", scriptPath},
		tmpDir,
		nil,
		&SessionOpts{
			ProviderName: "test-provider",
			Watchdog: &ports.SessionWatchdogConfig{
				PendingToolIdleTimeout:    200 * time.Millisecond,
				TurnCompletionIdleTimeout: 100 * time.Millisecond,
				PollInterval:              5 * time.Millisecond,
			},
		},
	)
	if err != nil {
		t.Fatalf("StartSession() error: %v", err)
	}

	select {
	case status := <-sess.StatusCh():
		if status != "FAILED" {
			t.Fatalf("StatusCh = %q, want FAILED", status)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for watchdog failure status")
	}

	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for watchdog to stop session")
	}

	if got := sess.ErrorDetail(); !strings.Contains(got, "provider watchdog stalled with pending tool Write") {
		t.Fatalf("ErrorDetail() = %q, want pending Write watchdog detail", got)
	}
}

func TestPendingToolWatchdogFailsIdleInProgressTool(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "in-progress-tool-stall.sh")
	script := `#!/usr/bin/env bash
printf '%s\n' '{"type":"tool_progress","tool_use_id":"task-1","tool_name":"Task","data":"pending"}'
printf '%s\n' '{"type":"tool_progress","tool_use_id":"task-1","tool_name":"Task","data":"in_progress"}'
sleep 30
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	mgr := NewManager(nil)
	defer mgr.Shutdown()

	sess, err := mgr.StartSession(
		"in-progress-tool-watchdog-stall",
		"feat-1",
		feature.PhasePlan,
		[]string{"bash", scriptPath},
		tmpDir,
		nil,
		&SessionOpts{
			ProviderName: "test-provider",
			Watchdog: &ports.SessionWatchdogConfig{
				PendingToolIdleTimeout:    25 * time.Millisecond,
				TurnCompletionIdleTimeout: 25 * time.Millisecond,
				PollInterval:              5 * time.Millisecond,
			},
		},
	)
	if err != nil {
		t.Fatalf("StartSession() error: %v", err)
	}

	select {
	case status := <-sess.StatusCh():
		if status != "FAILED" {
			t.Fatalf("StatusCh = %q, want FAILED", status)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for watchdog failure status")
	}

	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for watchdog to stop session")
	}

	if got := sess.ErrorDetail(); !strings.Contains(got, "provider watchdog stalled with pending tool Task") {
		t.Fatalf("ErrorDetail() = %q, want in-progress Task watchdog detail", got)
	}
}

func TestPendingToolWatchdogIgnoresToolWaitingOnPermission(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "tool-waiting-permission.sh")
	script := `#!/usr/bin/env bash
printf '%s\n' '{"type":"tool_progress","tool_use_id":"chatcmpl-tool-bash","tool_name":"Bash","data":"in_progress"}'
printf '%s\n' '{"type":"control_request","request_id":"req-bash-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"npm run slow-check"}}}'
sleep 0.12
printf '%s\n' '{"type":"result","subtype":"success","result":"ok"}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	mgr := NewManager(nil)
	defer mgr.Shutdown()

	sess, err := mgr.StartSession(
		"pending-tool-watchdog-permission-wait",
		"feat-1",
		feature.PhasePlan,
		[]string{"bash", scriptPath},
		tmpDir,
		nil,
		&SessionOpts{
			ProviderName: "test-provider",
			Watchdog: &ports.SessionWatchdogConfig{
				PendingToolIdleTimeout:    25 * time.Millisecond,
				TurnCompletionIdleTimeout: 25 * time.Millisecond,
				PollInterval:              5 * time.Millisecond,
			},
			ResultShutdownGrace: 20 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatalf("StartSession() error: %v", err)
	}

	select {
	case status := <-sess.StatusCh():
		if status != "SUCCESS" {
			t.Fatalf("StatusCh = %q, want SUCCESS; error detail: %s", status, sess.ErrorDetail())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for success status")
	}
}

func TestPendingToolWatchdogAllowsPromptResultAfterCompletedTool(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "pending-tool-completed.sh")
	script := `#!/usr/bin/env bash
printf '%s\n' '{"type":"tool_progress","tool_use_id":"chatcmpl-tool-write","tool_name":"Write","data":"pending"}'
sleep 0.01
printf '%s\n' '{"type":"tool_progress","tool_use_id":"chatcmpl-tool-write","tool_name":"Write","data":"in_progress"}'
sleep 0.01
printf '%s\n' '{"type":"tool_progress","tool_use_id":"chatcmpl-tool-write","tool_name":"Write","data":"completed"}'
sleep 0.06
printf '%s\n' '{"type":"result","subtype":"success","result":"ok"}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	mgr := NewManager(nil)
	defer mgr.Shutdown()

	sess, err := mgr.StartSession(
		"pending-tool-watchdog-completed",
		"feat-1",
		feature.PhasePlan,
		[]string{"bash", scriptPath},
		tmpDir,
		nil,
		&SessionOpts{
			ProviderName: "test-provider",
			Watchdog: &ports.SessionWatchdogConfig{
				PendingToolIdleTimeout:    25 * time.Millisecond,
				TurnCompletionIdleTimeout: 100 * time.Millisecond,
				PollInterval:              5 * time.Millisecond,
			},
			ResultShutdownGrace: 20 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatalf("StartSession() error: %v", err)
	}

	select {
	case status := <-sess.StatusCh():
		if status != "SUCCESS" {
			t.Fatalf("StatusCh = %q, want SUCCESS", status)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for success status")
	}
}

func TestWatchdogToolProgressTransitions(t *testing.T) {
	t.Parallel()

	observe := func(l watchdogToolLifecycle, msgs ...llm.ToolProgressMessage) watchdogToolLifecycle {
		for _, m := range msgs {
			l = l.observe(m)
		}
		return l
	}
	tests := []struct {
		name      string
		progress  []llm.ToolProgressMessage
		wantPhase watchdogToolPhase
		wantName  string
	}{
		{
			name:      "pending arms running tool",
			progress:  []llm.ToolProgressMessage{{ToolUseID: "tool-1", ToolName: "Write", Data: "pending"}},
			wantPhase: watchdogToolRunning,
			wantName:  "Write",
		},
		{
			name: "completed awaits enclosing turn result",
			progress: []llm.ToolProgressMessage{
				{ToolUseID: "tool-1", ToolName: "Write", Data: "pending"},
				{ToolUseID: "tool-1", ToolName: "Write", Data: "Status: completed"},
			},
			wantPhase: watchdogToolAwaitingTurnResult,
			wantName:  "Write",
		},
		{
			name: "failed awaits enclosing turn result",
			progress: []llm.ToolProgressMessage{
				{ToolUseID: "tool-1", ToolName: "Write", Data: "pending"},
				{ToolUseID: "tool-1", ToolName: "Write", Data: "failed"},
			},
			wantPhase: watchdogToolAwaitingTurnResult,
			wantName:  "Write",
		},
		{
			name: "terminal update for another tool cannot clear running tool",
			progress: []llm.ToolProgressMessage{
				{ToolUseID: "tool-1", ToolName: "Write", Data: "pending"},
				{ToolUseID: "tool-2", ToolName: "Read", Data: "completed"},
			},
			wantPhase: watchdogToolRunning,
			wantName:  "Write",
		},
		{
			name: "unrecognized progress keeps current state",
			progress: []llm.ToolProgressMessage{
				{ToolUseID: "tool-1", ToolName: "Write", Data: "pending"},
				{ToolUseID: "tool-1", ToolName: "Write", Data: "Status: running"},
			},
			wantPhase: watchdogToolRunning,
			wantName:  "Write",
		},
		{
			name: "sibling completion keeps remaining tools running",
			progress: []llm.ToolProgressMessage{
				{ToolUseID: "tool-1", ToolName: "Write", Data: "pending"},
				{ToolUseID: "tool-2", ToolName: "Read", Data: "pending"},
				{ToolUseID: "tool-2", ToolName: "Read", Data: "completed"},
			},
			wantPhase: watchdogToolRunning,
			wantName:  "Write",
		},
		{
			name: "last sibling completion awaits enclosing turn result",
			progress: []llm.ToolProgressMessage{
				{ToolUseID: "tool-1", ToolName: "Write", Data: "pending"},
				{ToolUseID: "tool-2", ToolName: "Read", Data: "pending"},
				{ToolUseID: "tool-2", ToolName: "Read", Data: "completed"},
				{ToolUseID: "tool-1", ToolName: "Write", Data: "completed"},
			},
			wantPhase: watchdogToolAwaitingTurnResult,
			wantName:  "Write",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := observe(watchdogToolLifecycle{}, tt.progress...)
			if got.phase() != tt.wantPhase {
				t.Fatalf("phase() = %v, want %v", got.phase(), tt.wantPhase)
			}
			if name := got.displayTool().displayName(); name != tt.wantName {
				t.Fatalf("displayTool().displayName() = %q, want %q", name, tt.wantName)
			}
		})
	}
}

func TestWatchdogSubagentTaskTracking(t *testing.T) {
	t.Parallel()

	l := watchdogToolLifecycle{}
	l = l.observe(llm.ToolProgressMessage{ToolUseID: "task-1", ToolName: "task", Data: "pending"})
	if !l.anySubagentPending() {
		t.Fatal("task tool should be tracked as a pending subagent")
	}
	// OpenCode renames the tool to its display title on later updates; the
	// subagent marker must survive the rename.
	l = l.observe(llm.ToolProgressMessage{ToolUseID: "task-1", ToolName: "Verification research", Data: "in_progress"})
	if !l.anySubagentPending() {
		t.Fatal("subagent marker should survive a display-name rename")
	}
	if name := l.displayTool().displayName(); name != "Verification research" {
		t.Fatalf("displayName() = %q, want renamed title", name)
	}
	l = l.observe(llm.ToolProgressMessage{ToolUseID: "task-1", Data: "completed"})
	if l.anySubagentPending() {
		t.Fatal("completed subagent should not be pending")
	}
}

func TestWatchdogSubagentTimeoutOverridesPendingToolTimeout(t *testing.T) {
	sess := NewSession("watchdog-subagent-timeout", "feat-1", feature.PhaseImplement)
	watchdog := newSessionWatchdog(sess, &ports.SessionWatchdogConfig{
		PendingToolIdleTimeout:    20 * time.Millisecond,
		SubagentToolIdleTimeout:   500 * time.Millisecond,
		TurnCompletionIdleTimeout: 20 * time.Millisecond,
		PollInterval:              5 * time.Millisecond,
	})
	watchdog.Observe(llm.SDKMessage{ToolProgress: &llm.ToolProgressMessage{
		ToolUseID: "task-1", ToolName: "task", Data: "in_progress",
	}})
	watchdog.Observe(llm.SDKMessage{ToolProgress: &llm.ToolProgressMessage{
		ToolUseID: "task-1", ToolName: "Verification research", Data: "in_progress",
	}})

	watchdog.mu.Lock()
	watchdog.lastActivityAt = time.Now().Add(-100 * time.Millisecond)
	watchdog.mu.Unlock()
	if _, _, stalled := watchdog.toolStall(); stalled {
		t.Fatal("watchdog stalled a pending subagent before the subagent timeout")
	}

	watchdog.mu.Lock()
	watchdog.lastActivityAt = time.Now().Add(-time.Second)
	watchdog.mu.Unlock()
	snap, timeout, stalled := watchdog.toolStall()
	if !stalled || timeout != 500*time.Millisecond {
		t.Fatalf("toolStall() = (%+v, %s, stalled=%v), want subagent-timeout stall", snap, timeout, stalled)
	}
}

func TestWatchdogDeclaredToolTimeoutExtendsPendingLeash(t *testing.T) {
	sess := NewSession("watchdog-declared-timeout", "feat-1", feature.PhaseImplement)
	watchdog := newSessionWatchdog(sess, &ports.SessionWatchdogConfig{
		PendingToolIdleTimeout:    20 * time.Millisecond,
		TurnCompletionIdleTimeout: 20 * time.Millisecond,
		PollInterval:              5 * time.Millisecond,
	})
	watchdog.Observe(llm.SDKMessage{ToolProgress: &llm.ToolProgressMessage{
		ToolUseID: "bash-1", ToolName: "Bash", Data: "in_progress", TimeoutMS: 500,
	}})
	// A later update without a declared timeout must not shrink the leash.
	watchdog.Observe(llm.SDKMessage{ToolProgress: &llm.ToolProgressMessage{
		ToolUseID: "bash-1", ToolName: "Bash", Data: "in_progress",
	}})

	watchdog.mu.Lock()
	watchdog.lastActivityAt = time.Now().Add(-100 * time.Millisecond)
	watchdog.mu.Unlock()
	if _, _, stalled := watchdog.toolStall(); stalled {
		t.Fatal("watchdog stalled a tool inside its declared execution timeout")
	}

	watchdog.mu.Lock()
	watchdog.lastActivityAt = time.Now().Add(-time.Second)
	watchdog.mu.Unlock()
	snap, timeout, stalled := watchdog.toolStall()
	if !stalled || timeout != 520*time.Millisecond {
		t.Fatalf("toolStall() = (%+v, %s, stalled=%v), want stall at declared timeout plus idle grace", snap, timeout, stalled)
	}
}

func TestWatchdogParallelSubagentSiblingCompletionKeepsRunning(t *testing.T) {
	sess := NewSession("watchdog-parallel-subagents", "feat-1", feature.PhaseImplement)
	watchdog := newSessionWatchdog(sess, &ports.SessionWatchdogConfig{
		PendingToolIdleTimeout:    20 * time.Millisecond,
		SubagentToolIdleTimeout:   time.Minute,
		TurnCompletionIdleTimeout: 20 * time.Millisecond,
		PollInterval:              5 * time.Millisecond,
	})
	for _, p := range []llm.ToolProgressMessage{
		{ToolUseID: "task-1", ToolName: "task", Data: "in_progress"},
		{ToolUseID: "task-2", ToolName: "task", Data: "in_progress"},
		{ToolUseID: "task-2", Data: "completed"},
	} {
		p := p
		watchdog.Observe(llm.SDKMessage{ToolProgress: &p})
	}

	watchdog.mu.Lock()
	watchdog.lastActivityAt = time.Now().Add(-time.Second)
	watchdog.mu.Unlock()
	if snap, _, stalled := watchdog.toolStall(); stalled || snap.phase != watchdogToolRunning {
		t.Fatalf("toolStall() = (%+v, stalled=%v), want still-running while a sibling subagent is pending", snap, stalled)
	}
}

func TestWatchdogControlWaitPausesAndRefreshesTurnTimer(t *testing.T) {
	sess := NewSession("watchdog-control-wait", "feat-1", feature.PhaseImplement)
	watchdog := newSessionWatchdog(sess, &ports.SessionWatchdogConfig{
		PendingToolIdleTimeout:    20 * time.Millisecond,
		TurnCompletionIdleTimeout: 20 * time.Millisecond,
		PollInterval:              5 * time.Millisecond,
	})
	watchdog.Observe(llm.SDKMessage{ToolProgress: &llm.ToolProgressMessage{
		ToolUseID: "tool-1",
		ToolName:  "Write",
		Data:      "completed",
	}})

	sess.mu.Lock()
	sess.status = SessionWaitingPermission
	sess.recordPendingControlRequestLocked(&llm.ControlRequestMessage{RequestID: "permission-1"})
	sess.mu.Unlock()
	watchdog.mu.Lock()
	watchdog.lastActivityAt = time.Now().Add(-time.Second)
	watchdog.mu.Unlock()

	if _, _, stalled := watchdog.toolStall(); stalled {
		t.Fatal("watchdog reported a stall while a real control request was pending")
	}

	sess.mu.Lock()
	sess.removePendingControlRequestLocked("permission-1")
	sess.status = SessionRunning
	sess.mu.Unlock()
	if _, _, stalled := watchdog.toolStall(); stalled {
		t.Fatal("watchdog reused pre-permission idle time after the control wait ended")
	}

	watchdog.mu.Lock()
	watchdog.lastActivityAt = time.Now().Add(-time.Second)
	watchdog.mu.Unlock()
	if tool, _, stalled := watchdog.toolStall(); !stalled || tool.phase != watchdogToolAwaitingTurnResult {
		t.Fatalf("toolStall() = (%+v, stalled=%v), want awaiting-turn-result stall after refreshed timeout", tool, stalled)
	}
}

func TestWatchdogDoesNotPauseForWaitingPermissionWithoutPendingControl(t *testing.T) {
	sess := NewSession("watchdog-stale-permission-status", "feat-1", feature.PhaseImplement)
	sess.SetStatus(SessionWaitingPermission)
	watchdog := newSessionWatchdog(sess, &ports.SessionWatchdogConfig{
		PendingToolIdleTimeout:    20 * time.Millisecond,
		TurnCompletionIdleTimeout: 20 * time.Millisecond,
		PollInterval:              5 * time.Millisecond,
	})
	watchdog.Observe(llm.SDKMessage{ToolProgress: &llm.ToolProgressMessage{
		ToolUseID: "tool-1",
		ToolName:  "Write",
		Data:      "completed",
	}})
	watchdog.mu.Lock()
	watchdog.lastActivityAt = time.Now().Add(-time.Second)
	watchdog.mu.Unlock()

	if tool, _, stalled := watchdog.toolStall(); !stalled || tool.phase != watchdogToolAwaitingTurnResult {
		t.Fatalf("toolStall() = (%+v, stalled=%v), want stale WaitingPermission status not to mask the stall", tool, stalled)
	}
}

func TestWatchdogActivityRefreshesAwaitingTurnTimerAndResultDisarmsIt(t *testing.T) {
	sess := NewSession("watchdog-activity", "feat-1", feature.PhaseImplement)
	watchdog := newSessionWatchdog(sess, &ports.SessionWatchdogConfig{
		PendingToolIdleTimeout:    20 * time.Millisecond,
		TurnCompletionIdleTimeout: 20 * time.Millisecond,
		PollInterval:              5 * time.Millisecond,
	})
	watchdog.Observe(llm.SDKMessage{ToolProgress: &llm.ToolProgressMessage{
		ToolUseID: "tool-1",
		ToolName:  "Write",
		Data:      "completed",
	}})
	watchdog.mu.Lock()
	watchdog.lastActivityAt = time.Now().Add(-time.Second)
	watchdog.mu.Unlock()

	watchdog.Observe(llm.SDKMessage{Assistant: &llm.AssistantMessage{}})
	if tool, _, stalled := watchdog.toolStall(); stalled || tool.phase != watchdogToolAwaitingTurnResult {
		t.Fatalf("toolStall() after assistant activity = (%+v, stalled=%v), want refreshed awaiting-turn state", tool, stalled)
	}

	watchdog.Observe(llm.SDKMessage{Result: &llm.ResultMessage{Subtype: "success"}})
	if tool, _, stalled := watchdog.toolStall(); stalled || tool.phase != watchdogToolInactive {
		t.Fatalf("toolStall() after Result = (%+v, stalled=%v), want inactive watchdog", tool, stalled)
	}
}

func TestPendingToolWatchdogFailsWhenTurnStallsAfterCompletedTool(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "completed-tool-turn-stall.sh")
	script := `#!/usr/bin/env bash
printf '%s\n' '{"type":"tool_progress","tool_use_id":"chatcmpl-tool-write","tool_name":"Write","data":"pending"}'
printf '%s\n' '{"type":"tool_progress","tool_use_id":"chatcmpl-tool-write","tool_name":"Write","data":"in_progress"}'
printf '%s\n' '{"type":"tool_progress","tool_use_id":"chatcmpl-tool-write","tool_name":"Write","data":"completed"}'
sleep 30
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	mgr := NewManager(nil)
	defer mgr.Shutdown()

	sess, err := mgr.StartSession(
		"completed-tool-turn-watchdog-stall",
		"feat-1",
		feature.PhaseImplement,
		[]string{"bash", scriptPath},
		tmpDir,
		nil,
		&SessionOpts{
			ProviderName: "test-provider",
			Watchdog: &ports.SessionWatchdogConfig{
				PendingToolIdleTimeout:    25 * time.Millisecond,
				TurnCompletionIdleTimeout: 25 * time.Millisecond,
				PollInterval:              5 * time.Millisecond,
			},
		},
	)
	if err != nil {
		t.Fatalf("StartSession() error: %v", err)
	}

	select {
	case status := <-sess.StatusCh():
		if status != "FAILED" {
			t.Fatalf("StatusCh = %q, want FAILED", status)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for watchdog to fail a turn stalled after a completed tool")
	}

	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for watchdog to stop session")
	}

	if got := sess.ErrorDetail(); !strings.Contains(got, "provider watchdog stalled awaiting turn completion after tool Write") {
		t.Fatalf("ErrorDetail() = %q, want post-tool turn-completion watchdog detail", got)
	}
}

func TestWatchdogPendingToolDataMatchesStatusLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want bool
	}{
		{name: "bare pending status", data: "pending", want: true},
		{name: "multi-line pending status", data: "File: report.md\nStatus: pending", want: true},
		{name: "bare in progress status", data: "in_progress", want: true},
		{name: "multi-line in progress status", data: "File: report.md\nStatus: in_progress", want: true},
		{name: "completed command mentions pending", data: "grep pending report.md\nStatus: completed", want: false},
		{name: "path mentions pending", data: "File: pending-report.md\nStatus: completed", want: false},
		{name: "running is not pending", data: "Status: running", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isWatchdogPendingToolData(tt.data); got != tt.want {
				t.Fatalf("isWatchdogPendingToolData(%q) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestWatchdogTerminalToolDataMatchesStatusLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want bool
	}{
		{name: "bare completed status", data: "completed", want: true},
		{name: "multi-line completed status", data: "File: report.md\nStatus: completed", want: true},
		{name: "bare failed status", data: "failed", want: true},
		{name: "multi-line failed status", data: "command\nStatus: failed", want: true},
		{name: "command mentions completed", data: "echo completed\nStatus: in_progress", want: false},
		{name: "path mentions failed", data: "File: failed-report.md\nStatus: in_progress", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isWatchdogTerminalToolData(tt.data); got != tt.want {
				t.Fatalf("isWatchdogTerminalToolData(%q) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestSessionStop(t *testing.T) {
	// parallel-exempt: subprocess-shutdown representative for the fast suite.
	dir := t.TempDir()
	script := filepath.Join(dir, "long.sh")
	initMsg := `{"type":"system","subtype":"init","session_id":"s1","model":"test"}`
	os.WriteFile(script, []byte("#!/bin/bash\necho '"+initMsg+"'\ncat > /dev/null\n"), 0o755)

	s := NewSession("stop-test", "feat-1", feature.PhaseResearch)
	err := s.Start([]string{"bash", script}, dir, nil, func(msg llm.SDKMessage) {})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Stop should terminate
	err = s.Stop()
	if err != nil {
		t.Errorf("stop: %v", err)
	}

	// Session should be done
	select {
	case <-s.done:
		// Good
	case <-time.After(10 * time.Second):
		t.Fatal("session did not stop within timeout")
	}
}

// TestSession_CodexProviderStaysAliveAfterResult verifies the legacy
// Codex provider exception for one-shot post-Result wrapper cleanup. The
// Codex provider's `app-server` is multi-turn: a Result on turn/completed is
// not a process-exit signal, and the orchestrator's
// SessionWaitingHelp branch (agent.WaitForPhaseOutcome) needs the session
// alive to deliver a follow-up user message. Closing stdin would EOF
// the JSON-RPC channel and kill app-server.
//
// The test mimics that lifecycle: the script writes a Result and then
// blocks on stdin (like `codex app-server` waiting for the next
// `user/message` notification). With the fix, stdin must remain open
// past the resultShutdownGrace window so the process keeps running.
// We then call Stop() to drain cleanly.
func TestSession_CodexProviderStaysAliveAfterResult(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-backed multi-turn provider lifecycle extended regression")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "codex_like.sh")
	initMsg := `{"type":"system","subtype":"init","session_id":"s1","model":"test"}`
	resultMsg := `{"type":"result","subtype":"success","is_error":false,"session_id":"s1","total_cost_usd":0.01}`
	body := "#!/bin/bash\n" +
		"echo '" + initMsg + "'\n" +
		"echo '" + resultMsg + "'\n" +
		"cat > /dev/null\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	s := NewSession("codex-alive", "feat-1", feature.PhaseResearch)
	s.protocol = claude.NewProtocol(llm.ProtocolOpts{WorkDir: dir})
	s.providerName = "codex"
	s.setResultShutdownGraceForTest(100 * time.Millisecond)
	if err := s.Start([]string{"bash", script}, dir, nil, func(llm.SDKMessage) {}); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait long enough for the Claude-style escalation to have fired
	// (resultShutdownGrace * 2 + a healthy buffer for the SIGKILL stage).
	// If the gate regresses, the session would be torn down within this
	// window and Done() would close.
	select {
	case <-s.Done():
		t.Fatal("codex session terminated after Result; the one-shot provider gate regressed and would kill the multi-turn app-server")
	case <-time.After(500 * time.Millisecond):
		// Expected: session still alive past the watchdog window.
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session did not terminate after explicit Stop")
	}
}

// TestSession_ResultUnsticksHungWrapper simulates the production wrapper
// pathology: the script writes a result message and then blocks reading
// stdin (like `cat`). Without the post-result stdin close + signal
// escalation, process.Wait() in readMessages' cleanup blocks forever and
// s.done never closes. With the fix, closing stdin lets the script's
// `cat` drain and the session terminates well within the grace window.
func TestSession_ResultUnsticksHungWrapper(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-backed wrapper cleanup extended regression")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "hung_wrapper.sh")
	initMsg := `{"type":"system","subtype":"init","session_id":"s1","model":"test"}`
	resultMsg := `{"type":"result","subtype":"success","is_error":false,"session_id":"s1","total_cost_usd":0.01}`
	body := "#!/bin/bash\n" +
		"echo '" + initMsg + "'\n" +
		"echo '" + resultMsg + "'\n" +
		"cat > /dev/null\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	s := NewSession("hung-wrapper", "feat-1", feature.PhaseResearch)
	s.protocol = claude.NewProtocol(llm.ProtocolOpts{WorkDir: dir})
	s.setResultShutdownGraceForTest(200 * time.Millisecond)
	if err := s.Start([]string{"bash", script}, dir, nil, func(llm.SDKMessage) {}); err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not terminate after result; stdin close + escalation regressed")
	}

	select {
	case status := <-s.statusCh:
		if status != "SUCCESS" {
			t.Errorf("statusCh = %q, want SUCCESS", status)
		}
	default:
		t.Error("statusCh empty; status was not signaled before shutdown")
	}
}

func TestSession_TurnResultKeepsStdinForContinuationWhenOptedIn(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-backed truncated-turn continuation extended regression")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "truncated_turn.sh")
	continuedPath := filepath.Join(dir, "continued.txt")
	initMsg := `{"type":"system","subtype":"init","session_id":"s1","model":"test"}`
	resultMsg := `{"type":"result","subtype":"success","is_error":false,"session_id":"s1","total_cost_usd":0.01,"stop_reason":"tool_use"}`
	body := "#!/bin/bash\n" +
		"echo '" + initMsg + "'\n" +
		"echo '" + resultMsg + "'\n" +
		"IFS= read -r _\n" +
		"if IFS= read -r line; then\n" +
		"  printf '%s\\n' \"$line\" > \"$CONTINUED_FILE\"\n" +
		"fi\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	mgr := NewManager(nil)
	s, err := mgr.StartSession(
		"truncated-keepalive",
		"feat-1",
		feature.PhaseImplement,
		[]string{"bash", script},
		dir,
		[]string{"CONTINUED_FILE=" + continuedPath},
		&SessionOpts{KeepAliveOnTurnResult: true, ResultShutdownGrace: 100 * time.Millisecond},
	)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case status := <-s.StatusCh():
		if status != "SUCCESS" {
			t.Fatalf("statusCh = %q, want SUCCESS", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for truncated result status")
	}

	select {
	case <-s.Done():
		t.Fatal("session terminated after truncated Result; continuation would race closed stdin")
	case <-time.After(350 * time.Millisecond):
		// Expected: the auto-resume waiter still has a live stdin pipe.
	}

	if err := s.SendUserMessage("keep going"); err != nil {
		t.Fatalf("SendUserMessage after truncated Result: %v", err)
	}

	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session did not terminate after continuation was consumed")
	}

	data, err := os.ReadFile(continuedPath)
	if err != nil {
		t.Fatalf("read continuation: %v", err)
	}
	if got := string(data); !strings.Contains(got, "keep going") {
		t.Fatalf("continuation stdin = %q, want user message payload", got)
	}
}

// interruptTrackingProtocol is a llm.Protocol test double that records
// Interrupt() calls and optionally returns a configured error.
type interruptTrackingProtocol struct {
	calls  int
	retErr error
}

func (p *interruptTrackingProtocol) SetStdin(io.Writer)                         {}
func (p *interruptTrackingProtocol) Handshake(context.Context) error            { return nil }
func (p *interruptTrackingProtocol) ParseLine([]byte) ([]llm.SDKMessage, error) { return nil, nil }
func (p *interruptTrackingProtocol) SendUserMessage(string) error               { return nil }
func (p *interruptTrackingProtocol) RespondToControl(string, bool, json.RawMessage, string) error {
	return nil
}
func (p *interruptTrackingProtocol) RespondToHook(string) error { return nil }
func (p *interruptTrackingProtocol) Interrupt() error {
	p.calls++
	return p.retErr
}
func (p *interruptTrackingProtocol) RespondToAskUser(string, json.RawMessage, map[string]string, map[string]llm.AskUserAnnotation) error {
	return nil
}
func (p *interruptTrackingProtocol) SessionID() string      { return "" }
func (p *interruptTrackingProtocol) TranscriptPath() string { return "" }
func (p *interruptTrackingProtocol) Close() error           { return nil }

func TestSessionInterrupt_DelegatesToProtocol(t *testing.T) {
	s := NewSession("int-test", "feat-1", feature.PhaseResearch)
	proto := &interruptTrackingProtocol{}
	s.protocol = proto

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt(): %v", err)
	}
	if proto.calls != 1 {
		t.Errorf("protocol.Interrupt calls = %d, want 1", proto.calls)
	}
}

func TestSessionInterrupt_NoProcess_NoError(t *testing.T) {
	// Protocol returns ErrNotSupported; process is nil; Interrupt should be a no-op.
	s := NewSession("int-noproc", "feat-1", feature.PhaseResearch)
	s.protocol = &interruptTrackingProtocol{retErr: llm.ErrNotSupported}

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt() with no process: %v", err)
	}
}

func TestSessionInterrupt_FallsBackToSIGINT(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-backed SIGINT fallback extended regression")
	}

	// A long-running script that exits 130 (SIGINT by convention) when
	// it receives SIGINT. The test verifies the fallback path delivers
	// the signal to the process group.
	dir := t.TempDir()
	script := filepath.Join(dir, "sigint.sh")
	initMsg := `{"type":"system","subtype":"init","session_id":"s1","model":"test"}`
	os.WriteFile(script, []byte("#!/bin/bash\ntrap 'exit 130' INT\necho '"+initMsg+"'\nwhile true; do sleep 0.1; done\n"), 0o755)

	s := NewSession("int-sigint", "feat-1", feature.PhaseResearch)
	if err := s.Start([]string{"bash", script}, dir, nil, func(msg llm.SDKMessage) {}); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	// Protocol says it can't interrupt, so Session falls back to SIGINT.
	s.protocol = &interruptTrackingProtocol{retErr: llm.ErrNotSupported}

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt(): %v", err)
	}

	select {
	case <-s.done:
		// Process exited in response to SIGINT.
	case <-time.After(5 * time.Second):
		t.Fatal("session did not exit after SIGINT fallback")
	}
}

func TestIsActive(t *testing.T) {
	tests := []struct {
		name   string
		status SessionStatus
		want   bool
	}{
		{"Running", SessionRunning, true},
		{"WaitingPermission", SessionWaitingPermission, true},
		{"WaitingHelp", SessionWaitingHelp, true},
		{"Done", SessionDone, false},
		{"Failed", SessionFailed, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{status: tt.status, done: make(chan struct{})}
			if got := s.IsActive(); got != tt.want {
				t.Errorf("IsActive() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResetWaitingStatus(t *testing.T) {
	tests := []struct {
		name   string
		status SessionStatus
		want   SessionStatus
	}{
		{"from WaitingPermission", SessionWaitingPermission, SessionRunning},
		{"from WaitingHelp", SessionWaitingHelp, SessionRunning},
		{"from Running (no-op)", SessionRunning, SessionRunning},
		{"from Done (no-op)", SessionDone, SessionDone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{status: tt.status, done: make(chan struct{})}
			s.ResetWaitingStatus()
			if s.status != tt.want {
				t.Errorf("ResetWaitingStatus() status = %v, want %v", s.status, tt.want)
			}
		})
	}
}

func TestSessionSendInput(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process stdin pipe with per-test session state.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = stdinR.Close()
		_ = stdinW.Close()
	})
	s := NewSession("input-test", "feat-1", feature.PhaseResearch)
	s.SetStdinForTest(stdinW)

	// Send raw input
	err = s.SendInput([]byte("test-input\n"))
	if err != nil {
		t.Errorf("send input: %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	data, err := io.ReadAll(stdinR)
	if err != nil {
		t.Fatalf("ReadAll(stdin): %v", err)
	}
	if got := string(data); got != "test-input\n" {
		t.Errorf("stdin = %q, want %q", got, "test-input\n")
	}
}

func TestSessionControlRequest(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test session state.
	s := NewSession("control-test", "feat-1", feature.PhaseImplement)
	s.permHandler = &permission.AutoApproveHandler{}
	runMockSession(t, s, []llm.SDKMessage{
		{
			Type:    "system",
			Subtype: "init",
			Init:    &llm.SystemInitMessage{SessionID: "s1", Model: "test"},
		},
		{
			Type: "control_request",
			ControlRequest: &llm.ControlRequestMessage{
				Type:      "control_request",
				RequestID: "req_1",
				Request: llm.ControlRequest{
					Subtype:  "can_use_tool",
					ToolName: "Bash",
					Input:    json.RawMessage(`{"command":"ls"}`),
				},
			},
		},
		{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
				Role:    "assistant",
				Content: []llm.ContentBlock{{Type: "text", Text: "response received"}},
			}},
		},
		{
			Type:   "result",
			Result: &llm.ResultMessage{Type: "result", Subtype: "success", SessionID: "s1"},
		},
	}, nil)

	// The auto-approve handler should have sent an allow response
	output := s.messageLog.AssistantText()
	if !strings.Contains(output, "response received") {
		t.Errorf("expected 'response received' in output, got: %s", output)
	}
}

func TestSessionSendUserMessage(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol double with per-test session state.
	s := NewSession("user-test", "feat-1", feature.PhaseResearch)
	s.protocol = &interruptTrackingProtocol{}

	if err := s.SendUserMessage("Hello Claude"); err != nil {
		t.Errorf("SendUserMessage: %v", err)
	}
}

func TestSessionSendUserMessageRecordsChatTurn(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol double with per-test session state.
	s := NewSession("chat-user-test", "__chat__", feature.PhaseResearch)
	s.SetKind(ports.KindChat)
	s.protocol = &interruptTrackingProtocol{}
	s.transcriptPath = filepath.Join(t.TempDir(), "transcript.jsonl")

	if err := s.SendUserMessage("What changed?"); err != nil {
		t.Fatalf("SendUserMessage: %v", err)
	}
	messages := s.MessageLog().Messages()
	if len(messages) != 1 || messages[0].User == nil {
		t.Fatalf("messages = %+v, want one user message", messages)
	}
	if !messages[0].LocallyAppended || messages[0].User.Message.Role != "user" {
		t.Fatalf("user message = %+v, want locally appended user role", messages[0])
	}
	if got := messages[0].User.Message.Content; len(got) != 1 || got[0].Text != "What changed?" {
		t.Fatalf("user content = %+v, want follow-up text", got)
	}

	persisted, err := readPersistedTranscript(s.transcriptPath)
	if err != nil {
		t.Fatalf("readPersistedTranscript: %v", err)
	}
	if len(persisted) != 1 || persisted[0].User == nil || persisted[0].User.Message.Content[0].Text != "What changed?" {
		t.Fatalf("persisted messages = %+v, want follow-up user message", persisted)
	}
}

func TestSessionWriteJSON(t *testing.T) {
	resp := llm.NewAllowResponse("req_1")
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(data) {
		t.Error("invalid JSON")
	}
}

func TestSessionStderrCapturedToFile(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-backed stderr capture extended regression")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "stderr.sh")
	os.WriteFile(script, []byte(`#!/bin/bash
echo "stdout line" >&1
echo -e "\x1b[32m⠋ Thinking...\x1b[0m" >&2
echo -e "\x1b[1mProcessing request\x1b[0m" >&2
echo "" >&2
`), 0o755)

	s := NewSession("stderr-test", "feat-1", feature.PhaseReview)
	s.stderrPath = filepath.Join(dir, "stderr.log")

	var messages []llm.SDKMessage
	err := s.Start([]string{"bash", script}, dir, nil, func(msg llm.SDKMessage) {
		messages = append(messages, msg)
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	allText := s.messageLog.AssistantText()
	if !strings.Contains(allText, "stdout line") {
		t.Errorf("expected 'stdout line' in message log, got: %s", allText)
	}
	if strings.Contains(allText, "Thinking...") || strings.Contains(allText, "Processing request") {
		t.Fatalf("stderr leaked into assistant log: %s", allText)
	}
	stderrBytes, err := os.ReadFile(s.stderrPath)
	if err != nil {
		t.Fatalf("reading stderr log: %v", err)
	}
	stderrText := string(stderrBytes)
	if !strings.Contains(stderrText, "Thinking...") {
		t.Errorf("expected stderr log to contain Thinking..., got %q", stderrText)
	}
	if !strings.Contains(stderrText, "Processing request") {
		t.Errorf("expected stderr log to contain Processing request, got %q", stderrText)
	}
	if len(messages) == 0 {
		t.Fatal("expected stdout messages to be forwarded")
	}
}

func TestSessionStderrNotForwardedToAttachChannel(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-backed stderr attach-channel extended regression")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "stderr-attach.sh")
	os.WriteFile(script, []byte(`#!/bin/bash
echo "stderr content" >&2
`), 0o755)

	s := NewSession("stderr-attach-test", "feat-1", feature.PhaseReview)
	s.stderrPath = filepath.Join(dir, "stderr.log")

	err := s.Start([]string{"bash", script}, dir, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Collect attach channel messages
	var attachMsgs []llm.SDKMessage
	done := make(chan struct{})
	go func() {
		defer close(done)
		for msg := range s.AttachCh() {
			attachMsgs = append(attachMsgs, msg)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("attach channel not closed within timeout")
	}

	found := false
	for _, msg := range attachMsgs {
		if msg.Assistant != nil {
			for _, block := range msg.Assistant.Message.Content {
				if strings.Contains(block.Text, "stderr content") {
					found = true
				}
			}
		}
	}
	if found {
		t.Error("stderr content should not be forwarded to attach channel")
	}
}

// TestSessionResultDeliveredUnderBackpressure reproduces the AMA-chat hang:
// when the CLI streams many messages faster than the consumer drains them,
// the Result message — which is the only in-band signal the desktop app uses to
// clear its "Thinking…" state — must not be dropped. The session's
// forwarder uses a bounded blocking send for Result so the consumer
// eventually receives it even if partials are dropped under load.
func TestSessionResultDeliveredUnderBackpressure(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test attach buffer.
	s := NewSession("bp-test", "feat-1", feature.PhaseImplement)
	s.setCriticalAttachSendTimeoutForTest(time.Second)
	unregister := registerAttachConsumerForTest(t, s)
	defer unregister()
	for i := 0; i < cap(s.attachCh); i++ {
		s.attachCh <- llm.SDKMessage{Type: "assistant"}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockSession(t, s, []llm.SDKMessage{
			{
				Type:    "system",
				Subtype: "init",
				Init:    &llm.SystemInitMessage{SessionID: "s1", Model: "m"},
			},
			{
				Type: "result",
				Result: &llm.ResultMessage{
					Type:         "result",
					Subtype:      "success",
					SessionID:    "s1",
					TotalCostUSD: 0.01,
				},
			},
		}, func(llm.SDKMessage) {})
	}()

	for {
		select {
		case msg, ok := <-s.AttachCh():
			if !ok {
				t.Fatal("attach channel closed before Result was delivered")
			}
			if msg.Result != nil {
				<-done
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Result SDKMessage was not delivered via AttachCh under backpressure")
		}
	}
}

func registerAttachConsumerForTest(t *testing.T, s *Session) func() {
	t.Helper()
	registrar, ok := any(s).(interface {
		RegisterAttachConsumer() func()
	})
	if !ok {
		t.Fatal("Session does not expose RegisterAttachConsumer")
	}
	return registrar.RegisterAttachConsumer()
}

func TestSession_OnSubagentEventFires(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process stdout replay with per-test session state.
	// Verify that task_progress and task_notification SDK messages trigger
	// the onSubagentEvent callback registered via SetOnSubagentEvent, and
	// that other system subtypes (init, compact_boundary) do not.
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s1","model":"m"}`,
		`{"type":"system","subtype":"task_started","task_id":"t1","tool_use_id":"u1","description":"reading","task_type":"local_agent","prompt":"p","session_id":"s1"}`,
		`{"type":"system","subtype":"task_progress","task_id":"t1","tool_use_id":"u1","description":"reading","last_tool_name":"Read","usage":{"total_tokens":10,"tool_uses":2,"duration_ms":500},"session_id":"s1"}`,
		`{"type":"system","subtype":"compact_boundary"}`,
		`{"type":"system","subtype":"task_notification","task_id":"t1","tool_use_id":"u1","status":"completed","summary":"ok","usage":{"total_tokens":20,"tool_uses":4,"duration_ms":1500},"session_id":"s1"}`,
		`{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}`,
	}

	s := NewSession("sub-test", "feat-1", feature.PhaseImplement)

	var mu sync.Mutex
	var captured []llm.SDKMessage
	var startedCount, progressCount, notificationCount atomic.Int32
	s.SetOnSubagentEvent(func(msg llm.SDKMessage) {
		mu.Lock()
		captured = append(captured, msg)
		mu.Unlock()
		if msg.TaskStarted != nil {
			startedCount.Add(1)
		}
		if msg.TaskProgress != nil {
			progressCount.Add(1)
		}
		if msg.TaskNotification != nil {
			notificationCount.Add(1)
		}
	})

	runSessionWithStdoutLines(t, s, lines, func(llm.SDKMessage) {})
	if startedCount.Load() != 1 || progressCount.Load() != 1 || notificationCount.Load() != 1 {
		mu.Lock()
		got := len(captured)
		mu.Unlock()
		t.Fatalf("onSubagentEvent calls: started=%d progress=%d notification=%d total=%d; want 1/1/1/3",
			startedCount.Load(), progressCount.Load(), notificationCount.Load(), got)
	}

	mu.Lock()
	if len(captured) != 3 {
		mu.Unlock()
		t.Fatalf("expected exactly 3 callbacks (started + progress + notification), got %d", len(captured))
	}
	if ts := captured[0].TaskStarted; ts == nil || ts.TaskID != "t1" || ts.TaskType != "local_agent" {
		t.Errorf("started payload unexpected: %+v", captured[0].TaskStarted)
	}
	if tp := captured[1].TaskProgress; tp == nil || tp.TaskID != "t1" || tp.Usage == nil || tp.Usage.DurationMs != 500 {
		t.Errorf("progress payload unexpected: %+v", captured[1].TaskProgress)
	}
	if tn := captured[2].TaskNotification; tn == nil || tn.Status != "completed" || tn.Usage == nil || tn.Usage.DurationMs != 1500 {
		t.Errorf("notification payload unexpected: %+v", captured[2].TaskNotification)
	}
	mu.Unlock()
}

func TestSessionStderrDiscardedWithoutLogPath(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-backed stderr discard extended regression")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "no-stderr.sh")
	os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
echo "secret stderr" >&2
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755)

	s := NewSession("no-stderr-test", "feat-1", feature.PhaseImplement)
	// printMode is false by default

	err := s.Start([]string{"bash", script}, dir, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	// stderr content should NOT appear in message log
	allText := s.messageLog.AssistantText()
	if strings.Contains(allText, "secret stderr") {
		t.Error("session should not capture stderr without StderrPath")
	}
}

func TestSession_MalformedJSONWrappedAsAssistant(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process stdout replay with per-test session state.
	s := NewSession("malformed-test", "feat-1", feature.PhaseImplement)
	runSessionWithStdoutLines(t, s, []string{
		`{"type":"system","subtype":"init","session_id":"s1","model":"test"}`,
		`WARNING: rate limit approaching`,
		`{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01}`,
	}, nil)

	// Verify the plain text was wrapped as assistant text
	text := s.messageLog.AssistantText()
	if !strings.Contains(text, "WARNING: rate limit approaching") {
		t.Errorf("expected plain text in assistant log, got: %q", text)
	}

	// Verify session completed successfully
	select {
	case status := <-s.statusCh:
		if status != "SUCCESS" {
			t.Errorf("statusCh = %q, want SUCCESS", status)
		}
	default:
		t.Error("statusCh empty, expected SUCCESS")
	}
}

func TestSession_APIErrorThenSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-backed repeated status delivery extended regression")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "api-error.sh")
	// The statusCh has buffer size 1 so we need to consume the first status
	// before the second one is sent. Use a long sleep so the test has time.
	os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
echo '{"type":"result","subtype":"error","error":"rate limited"}'
sleep 1
echo '{"type":"system","subtype":"init","session_id":"s2","model":"test"}'
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"recovered"}]}}'
echo '{"type":"result","subtype":"success","session_id":"s2","total_cost_usd":0.01}'
`), 0o755)

	s := NewSession("apierror-test", "feat-1", feature.PhaseImplement)
	err := s.Start([]string{"bash", script}, dir, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Read the first status (API_ERROR) before the second is sent
	select {
	case status := <-s.statusCh:
		if status != "API_ERROR" {
			t.Errorf("first status = %q, want API_ERROR", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for API_ERROR status")
	}

	// Wait for session to complete
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	// Read the second status (SUCCESS)
	select {
	case status := <-s.statusCh:
		if status != "SUCCESS" {
			t.Errorf("second status = %q, want SUCCESS", status)
		}
	default:
		t.Error("statusCh empty, expected SUCCESS after recovery")
	}
}

// TestSession_StatusChCoalescesToLatestResult proves that when two results
// arrive before the single consumer reads statusCh, the stale status is
// evicted and the latest one is delivered — never silently dropped.
func TestSession_StatusChCoalescesToLatestResult(t *testing.T) {
	t.Parallel()
	s := NewSession("coalesce-test", "feat-1", feature.PhaseImplement)
	runSessionWithStdoutLines(t, s, []string{
		`{"type":"system","subtype":"init","session_id":"s1","model":"test"}`,
		`{"type":"result","subtype":"error","error":"rate limited"}`,
		`{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01}`,
	}, nil)

	select {
	case status := <-s.statusCh:
		if status != "SUCCESS" {
			t.Errorf("statusCh = %q, want latest status SUCCESS", status)
		}
	default:
		t.Error("statusCh empty, expected coalesced SUCCESS")
	}
}

func TestSession_SIGTERMEscalation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "trap.sh")
	// Script traps SIGTERM and INT, then blocks forever.
	// The session's Stop() sends SIGTERM, then after the grace period sends SIGKILL.
	os.WriteFile(script, []byte("#!/bin/bash\ntrap '' SIGTERM\ntrap '' INT\nwhile true; do sleep 1; done\n"), 0o755)

	s := NewSession("sigterm-test", "feat-1", feature.PhaseImplement)
	err := s.Start([]string{"bash", script}, dir, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Retained in extended gate: the child script has no readiness signal for
	// when its SIGTERM trap is installed.
	time.Sleep(200 * time.Millisecond)

	// Stop should eventually kill the process via SIGKILL after the grace period
	err = s.Stop()
	if err != nil {
		t.Logf("stop returned error (expected for killed process): %v", err)
	}

	select {
	case <-s.done:
		// Process was killed successfully
	case <-time.After(15 * time.Second):
		t.Fatal("session did not die within 15s — SIGKILL escalation may be broken")
	}
}

func TestSession_LargeLineWithinBuffer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "large.sh")
	// Emit a line near the 10MB buffer limit but within it (5MB)
	// to verify the session handles large lines correctly.
	os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
python3 -c "print('x'*5000000)"
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01}'
`), 0o755)

	s := NewSession("large-test", "feat-1", feature.PhaseImplement)
	err := s.Start([]string{"bash", script}, dir, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-s.done:
		// Good — session handled the large line
	case <-time.After(15 * time.Second):
		t.Fatal("session hung on large line")
	}

	// The large plain text line should be wrapped as an assistant message
	text := s.messageLog.AssistantText()
	if len(text) < 1000 {
		t.Errorf("expected large text in assistant log, got %d bytes", len(text))
	}
}

func TestSession_LargeLineHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "largeline.sh")
	// Emit a 5MB line (within the 10MB bufio.Scanner limit) to verify
	// the session handles large output lines without hanging.
	os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
python3 -c "print('x'*5000000)"
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01}'
`), 0o755)

	s := NewSession("largeline-test", "feat-1", feature.PhaseImplement)
	err := s.Start([]string{"bash", script}, dir, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-s.done:
		// Session reached terminal state and processed the large line.
	case <-time.After(15 * time.Second):
		t.Fatal("session hung on large line (~5MB)")
	}

	// The session should have completed successfully since the line
	// was within the scanner buffer limit.
	if s.status != SessionDone {
		t.Errorf("expected SessionDone, got %v", s.status)
	}
}

func TestSession_OversizedLineHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "oversized.sh")
	// Emit a line >10MB (exceeds the bufio.Scanner buffer limit).
	// The scanner should fail with a token-too-long error, causing the
	// read loop to exit and the session to reach a terminal state
	// (SessionFailed) without hanging.
	os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
python3 -c "print('x'*11000000)"
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01}'
`), 0o755)

	s := NewSession("oversized-test", "feat-1", feature.PhaseImplement)
	err := s.Start([]string{"bash", script}, dir, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-s.done:
		// Good — session reached a terminal state without hanging.
	case <-time.After(15 * time.Second):
		t.Fatal("session hung on oversized line (>10MB)")
	}

	// The session should reach SessionFailed because the scanner stops
	// reading after the oversized line, causing the process to exit
	// without a successful result.
	if s.status != SessionFailed {
		t.Errorf("expected SessionFailed for oversized line, got %v", s.status)
	}
}

func TestSession_RespondToAskUser_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "ask_user.sh")
	// Script emits init, then a control_request for AskUserQuestion,
	// then reads stdin for the response, then emits success.
	os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
# Emit AskUserQuestion control_request
echo '{"type":"control_request","request_id":"req-ask-1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which DB?"}]}}}'
# Read the control_response from stdin
read -t 10 response
# Emit the response as assistant text
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Got answer"}]}}'
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01}'
`), 0o755)

	s := NewSession("ask-user-test", "feat-1", feature.PhaseResearch)
	// No permHandler — AskUserQuestion is always surfaced to desktop app
	err := s.Start([]string{"bash", script}, dir, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for the control_request to arrive (hasUnansweredQuestion or pending request)
	deadline := time.After(5 * time.Second)
	for {
		s.mu.Lock()
		hasQ := s.hasUnansweredQuestion
		hasPending := len(s.pendingControlRequests) > 0
		s.mu.Unlock()
		if hasQ || hasPending {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for AskUserQuestion control_request")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Send response
	questions := json.RawMessage(`[{"question":"Which DB?"}]`)
	answers := map[string]string{"Which DB?": "PostgreSQL"}
	err = s.RespondToAskUser("req-ask-1", questions, answers, nil)
	if err != nil {
		t.Fatalf("RespondToAskUser error: %v", err)
	}

	// Wait for session to complete
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for session to complete")
	}

	// Verify hasUnansweredQuestion was cleared
	if s.HasUnansweredQuestion() {
		t.Error("expected hasUnansweredQuestion to be cleared after response")
	}

	// Verify the answer acknowledgment appears in the message log
	text := s.messageLog.AssistantText()
	if !strings.Contains(text, "Got answer") {
		t.Errorf("expected answer acknowledgment in message log, got: %s", text)
	}
}

func TestSession_DenyResponse_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "deny.sh")
	// Script emits init, then a control_request for Bash tool.
	// DenyAllHandler will auto-deny it. Script reads the response and continues.
	os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
# Emit Bash tool permission request
echo '{"type":"control_request","request_id":"req-bash-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}'
# Read the deny response from stdin
read -t 10 response
# Emit assistant text acknowledging the deny
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Tool was denied"}]}}'
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01}'
`), 0o755)

	s := NewSession("deny-test", "feat-1", feature.PhaseResearch)
	s.permHandler = &permission.DenyAllHandler{}
	err := s.Start([]string{"bash", script}, dir, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for session to complete
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for session to complete")
	}

	// Verify session completed successfully — the deny was processed
	text := s.messageLog.AssistantText()
	if !strings.Contains(text, "Tool was denied") {
		t.Errorf("expected deny acknowledgment in assistant text, got: %s", text)
	}
}

func TestContextPercentage(t *testing.T) {
	tests := []struct {
		name  string
		usage *llm.Usage
		want  int
	}{
		{"no usage data", nil, -1},
		{"20K input on 200K model", &llm.Usage{InputTokens: 20_000, ContextWindow: 200_000}, 10},
		{"all token types on 200K", &llm.Usage{
			InputTokens:              10_000,
			CacheReadInputTokens:     50_000,
			CacheCreationInputTokens: 40_000,
			ContextWindow:            200_000,
		}, 50},
		{"usage on 1M model", &llm.Usage{InputTokens: 200_000, ContextWindow: 1_000_000}, 20},
		{"zero tokens", &llm.Usage{InputTokens: 0, ContextWindow: 200_000}, 0},
		{"over 100 percent capped", &llm.Usage{InputTokens: 300_000, ContextWindow: 200_000}, 100},
		{"context window from usage is used directly", &llm.Usage{
			InputTokens:   80_000,
			ContextWindow: 128_000,
		}, 62},
		{"missing context window returns unavailable", &llm.Usage{
			InputTokens:   20_000,
			ContextWindow: 0,
		}, -1},

		// Codex-style inputs: ContextTotalTokens populated, 12K baseline
		// subtracted from both numerator and denominator. Numbers drawn
		// from a real gpt-5.4 session (modelContextWindow=258400).
		{"codex turn 1 (22881/258400 w/ 12K baseline)", &llm.Usage{
			ContextTotalTokens: 22_881,
			ContextBaseline:    12_000,
			ContextWindow:      258_400,
		}, 4}, // (22881-12000) / (258400-12000) = 10881/246400 = 4.4% → 4
		{"codex turn 7 (84456/258400 w/ 12K baseline)", &llm.Usage{
			ContextTotalTokens: 84_456,
			ContextBaseline:    12_000,
			ContextWindow:      258_400,
		}, 29}, // (84456-12000) / 246400 = 72456/246400 = 29.4% → 29
		{"codex turn 20 (134635/258400 w/ 12K baseline)", &llm.Usage{
			ContextTotalTokens: 134_635,
			ContextBaseline:    12_000,
			ContextWindow:      258_400,
		}, 49}, // (134635-12000) / 246400 = 122635/246400 = 49.8% → 49
		{"codex ContextTotalTokens preferred over Claude-style sum", &llm.Usage{
			ContextTotalTokens:   50_000,
			InputTokens:          999_999, // would dominate if used
			CacheReadInputTokens: 999_999,
			ContextBaseline:      12_000,
			ContextWindow:        200_000,
		}, 20}, // uses ContextTotalTokens: (50000-12000)/(200000-12000) = 20.2% → 20
		{"codex used below baseline clamps to zero", &llm.Usage{
			ContextTotalTokens: 5_000,
			ContextBaseline:    12_000,
			ContextWindow:      258_400,
		}, 0},
		{"codex over-100 still caps", &llm.Usage{
			ContextTotalTokens: 300_000,
			ContextBaseline:    12_000,
			ContextWindow:      258_400,
		}, 100},
		{"baseline equals window returns unavailable", &llm.Usage{
			ContextTotalTokens: 10_000,
			ContextBaseline:    200_000,
			ContextWindow:      200_000,
		}, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{
				latestUsage: tt.usage,
				done:        make(chan struct{}),
			}
			got := s.ContextPercentage()
			if got != tt.want {
				t.Errorf("ContextPercentage() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSessionCapturesModelAndUsage(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test session state.
	s := NewSession("usage-test", "feat-1", feature.PhaseImplement)
	runMockSession(t, s, []llm.SDKMessage{
		{
			Type:    "system",
			Subtype: "init",
			Init:    &llm.SystemInitMessage{SessionID: "s1", Model: "opus[1m]"},
		},
		{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
				Role:    "assistant",
				Content: []llm.ContentBlock{{Type: "text", Text: "hello"}},
				Usage: &llm.Usage{
					InputTokens:          50000,
					OutputTokens:         1000,
					CacheReadInputTokens: 10000,
					ContextWindow:        1_000_000,
				},
			}},
		},
		{
			Type:   "result",
			Result: &llm.ResultMessage{Type: "result", Subtype: "success", SessionID: "s1", TotalCostUSD: 0.01},
		},
	}, nil)

	if s.model != "opus[1m]" {
		t.Errorf("model = %q, want opus[1m]", s.model)
	}

	if s.latestUsage == nil {
		t.Fatal("latestUsage is nil, expected usage data")
	}
	if s.latestUsage.InputTokens != 50000 {
		t.Errorf("InputTokens = %d, want 50000", s.latestUsage.InputTokens)
	}
	if s.latestUsage.CacheReadInputTokens != 10000 {
		t.Errorf("CacheReadInputTokens = %d, want 10000", s.latestUsage.CacheReadInputTokens)
	}

	// The protocol should use the discovered context window immediately, without
	// waiting for result.modelUsage.
	pct := s.ContextPercentage()
	if pct != 6 {
		t.Errorf("ContextPercentage() = %d, want 6", pct)
	}
}

// TestSessionCapturesUsageFromPartialMessage verifies that usage carried on a
// partial assistant message (as emitted by --include-partial-messages) is
// captured even though its subtype is "partial".
func TestSessionCapturesUsageFromPartialMessage(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test session state.
	s := NewSession("partial-usage-test", "feat-2", feature.PhaseImplement)
	runMockSession(t, s, []llm.SDKMessage{
		{
			Type:    "system",
			Subtype: "init",
			Init:    &llm.SystemInitMessage{SessionID: "s2", Model: "sonnet"},
		},
		{
			Type:    "assistant",
			Subtype: "partial",
			Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
				Role:    "assistant",
				Content: []llm.ContentBlock{{Type: "text", Text: "hi"}},
				Usage: &llm.Usage{
					InputTokens:          30000,
					OutputTokens:         500,
					CacheReadInputTokens: 5000,
					ContextWindow:        200_000,
				},
			}},
		},
		{
			Type:   "result",
			Result: &llm.ResultMessage{Type: "result", Subtype: "success", SessionID: "s2", TotalCostUSD: 0.005},
		},
	}, nil)

	if s.latestUsage == nil {
		t.Fatal("latestUsage is nil; usage on partial messages must be captured")
	}
	if s.latestUsage.InputTokens != 30000 {
		t.Errorf("InputTokens = %d, want 30000", s.latestUsage.InputTokens)
	}
	// The protocol should use the discovered context window immediately, without
	// waiting for result.modelUsage.
	pct := s.ContextPercentage()
	if pct != 17 {
		t.Errorf("ContextPercentage() = %d, want 17", pct)
	}
}

// TestSessionCapturesContextWindowFromModelUsage verifies that the context
// window reported in result.modelUsage is captured and used as the denominator
// for ContextPercentage, overriding the hardcoded heuristic.
func TestSessionCapturesContextWindowFromModelUsage(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test session state.
	s := NewSession("model-usage-test", "feat-3", feature.PhaseImplement)
	runMockSession(t, s, []llm.SDKMessage{
		{
			Type:    "system",
			Subtype: "init",
			Init:    &llm.SystemInitMessage{SessionID: "s3", Model: "opus"},
		},
		{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
				Role:    "assistant",
				Content: []llm.ContentBlock{{Type: "text", Text: "hi"}},
				Usage: &llm.Usage{
					InputTokens:          60000,
					OutputTokens:         1000,
					CacheReadInputTokens: 10000,
				},
			}},
		},
		{
			Type: "result",
			Result: &llm.ResultMessage{
				Type:         "result",
				Subtype:      "success",
				SessionID:    "s3",
				TotalCostUSD: 0.01,
				ModelUsage: map[string]llm.ModelUsageEntry{
					"opus": {ContextWindow: 200000},
				},
			},
		},
	}, nil)

	if s.latestUsage == nil {
		t.Fatal("latestUsage is nil")
	}
	if s.latestUsage.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", s.latestUsage.ContextWindow)
	}
	// (60000+10000) / 200000 * 100 = 35
	pct := s.ContextPercentage()
	if pct != 35 {
		t.Errorf("ContextPercentage() = %d, want 35", pct)
	}
}

func TestResultSubtypeToStatus(t *testing.T) {
	tests := []struct {
		name    string
		subtype string
		want    string
	}{
		{"success", "success", "SUCCESS"},
		{"error", "error", "API_ERROR"},
		{"max_turns", "max_turns", "FAILED"},
		{"max_budget", "max_budget", "FAILED"},
		{"unknown", "something_else", "FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &llm.ResultMessage{Subtype: tt.subtype}
			got := resultSubtypeToStatus(r)
			if got != tt.want {
				t.Errorf("resultSubtypeToStatus(%q) = %q, want %q", tt.subtype, got, tt.want)
			}
		})
	}
}

func TestAccumulatedUsageZeroDefault(t *testing.T) {
	s := NewSession("", "", 0)
	got := s.AccumulatedUsage()
	want := llm.Usage{}
	if got != want {
		t.Errorf("AccumulatedUsage() on new session = %+v, want zero-value %+v", got, want)
	}
}

func TestSetAccumulatedUsage(t *testing.T) {
	tests := []struct {
		name string
		set  llm.Usage
	}{
		{
			name: "basic input and output tokens",
			set:  llm.Usage{InputTokens: 100, OutputTokens: 50},
		},
		{
			name: "all token fields populated",
			set: llm.Usage{
				InputTokens:              500,
				OutputTokens:             200,
				CacheReadInputTokens:     1000,
				CacheCreationInputTokens: 300,
			},
		},
		{
			name: "zero values",
			set:  llm.Usage{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSession("set-usage-test", "feat-1", feature.PhaseImplement)
			s.SetAccumulatedUsage(tt.set)
			got := s.AccumulatedUsage()
			if got != tt.set {
				t.Errorf("AccumulatedUsage() = %+v, want %+v", got, tt.set)
			}
		})
	}
}

func TestAccumulatedUsageFromResult(t *testing.T) {
	// Test the SetCost + SetAccumulatedUsage path that mirrors what
	// readMessages() does when it receives a result with usage data.
	s := NewSession("result-usage-test", "feat-1", feature.PhaseImplement)

	resultUsage := llm.Usage{InputTokens: 500, OutputTokens: 200}
	s.SetCost(&llm.ResultMessage{TotalCostUSD: 1.0, Usage: &resultUsage})
	s.SetAccumulatedUsage(resultUsage)

	// Verify cost was captured
	if s.Cost() == nil {
		t.Fatal("Cost() is nil after SetCost")
	}
	if s.Cost().TotalCostUSD != 1.0 {
		t.Errorf("Cost().TotalCostUSD = %f, want 1.0", s.Cost().TotalCostUSD)
	}

	// Verify accumulated usage was set
	got := s.AccumulatedUsage()
	if got.InputTokens != 500 {
		t.Errorf("AccumulatedUsage().InputTokens = %d, want 500", got.InputTokens)
	}
	if got.OutputTokens != 200 {
		t.Errorf("AccumulatedUsage().OutputTokens = %d, want 200", got.OutputTokens)
	}
}

func assistantUsageMessage(text string, usage llm.Usage) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "assistant",
		Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
			Role:    "assistant",
			Content: []llm.ContentBlock{{Type: "text", Text: text}},
			Usage:   &usage,
		}},
	}
}

func TestAccumulatedUsageClaudeMultipleMessages(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test session state.
	s := NewSession("multi-usage-test", "feat-1", feature.PhaseImplement)
	runMockSession(t, s, []llm.SDKMessage{
		{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{SessionID: "s1", Model: "opus"}},
		assistantUsageMessage("msg1", llm.Usage{InputTokens: 100, OutputTokens: 50}),
		assistantUsageMessage("msg2", llm.Usage{InputTokens: 200, OutputTokens: 100}),
		assistantUsageMessage("msg3", llm.Usage{InputTokens: 150, OutputTokens: 75}),
		{Type: "result", Result: &llm.ResultMessage{Type: "result", Subtype: "success", SessionID: "s1", TotalCostUSD: 0.02}},
	}, nil)

	// Accumulated usage should be the SUM of all three messages
	got := s.AccumulatedUsage()
	if got.InputTokens != 450 {
		t.Errorf("AccumulatedUsage().InputTokens = %d, want 450", got.InputTokens)
	}
	if got.OutputTokens != 225 {
		t.Errorf("AccumulatedUsage().OutputTokens = %d, want 225", got.OutputTokens)
	}

	// Latest usage should be the LAST message's usage
	latest := s.LatestUsage()
	if latest == nil {
		t.Fatal("LatestUsage() is nil, expected usage data")
	}
	if latest.InputTokens != 150 {
		t.Errorf("LatestUsage().InputTokens = %d, want 150", latest.InputTokens)
	}
	if latest.OutputTokens != 75 {
		t.Errorf("LatestUsage().OutputTokens = %d, want 75", latest.OutputTokens)
	}
}

func TestAccumulatedUsageCacheTokens(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test session state.
	s := NewSession("cache-usage-test", "feat-1", feature.PhaseImplement)
	runMockSession(t, s, []llm.SDKMessage{
		{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{SessionID: "s1", Model: "sonnet"}},
		assistantUsageMessage("msg1", llm.Usage{
			InputTokens:              100,
			OutputTokens:             50,
			CacheReadInputTokens:     500,
			CacheCreationInputTokens: 200,
		}),
		assistantUsageMessage("msg2", llm.Usage{
			InputTokens:              200,
			OutputTokens:             100,
			CacheReadInputTokens:     300,
			CacheCreationInputTokens: 100,
		}),
		{Type: "result", Result: &llm.ResultMessage{Type: "result", Subtype: "success", SessionID: "s1", TotalCostUSD: 0.03}},
	}, nil)

	got := s.AccumulatedUsage()
	if got.InputTokens != 300 {
		t.Errorf("AccumulatedUsage().InputTokens = %d, want 300", got.InputTokens)
	}
	if got.OutputTokens != 150 {
		t.Errorf("AccumulatedUsage().OutputTokens = %d, want 150", got.OutputTokens)
	}
	if got.CacheReadInputTokens != 800 {
		t.Errorf("AccumulatedUsage().CacheReadInputTokens = %d, want 800", got.CacheReadInputTokens)
	}
	if got.CacheCreationInputTokens != 300 {
		t.Errorf("AccumulatedUsage().CacheCreationInputTokens = %d, want 300", got.CacheCreationInputTokens)
	}
}

// usageUpdateProtocol is a test Protocol that parses lines into UsageUpdate
// messages (simulating the Codex path with cumulative SET semantics).
type usageUpdateProtocol struct {
	sid string
}

type usageUpdateLine struct {
	Type               string  `json:"type"`
	InputTokens        int     `json:"input_tokens"`
	OutputTokens       int     `json:"output_tokens"`
	ContextInputTokens int     `json:"context_input_tokens,omitempty"`
	ContextTotalTokens int     `json:"context_total_tokens,omitempty"`
	ContextBaseline    int     `json:"context_baseline,omitempty"`
	ContextWindow      int     `json:"context_window,omitempty"`
	CostUSD            float64 `json:"cost_usd,omitempty"`
	Subtype            string  `json:"subtype,omitempty"`
	SessionID          string  `json:"session_id,omitempty"`
	TotalCostUSD       float64 `json:"total_cost_usd,omitempty"`
}

func (p *usageUpdateProtocol) SetStdin(io.Writer)              {}
func (p *usageUpdateProtocol) Handshake(context.Context) error { return nil }
func (p *usageUpdateProtocol) SendUserMessage(string) error    { return nil }
func (p *usageUpdateProtocol) RespondToControl(string, bool, json.RawMessage, string) error {
	return nil
}
func (p *usageUpdateProtocol) RespondToHook(string) error { return nil }
func (p *usageUpdateProtocol) Interrupt() error           { return llm.ErrNotSupported }
func (p *usageUpdateProtocol) RespondToAskUser(string, json.RawMessage, map[string]string, map[string]llm.AskUserAnnotation) error {
	return nil
}
func (p *usageUpdateProtocol) SessionID() string      { return p.sid }
func (p *usageUpdateProtocol) TranscriptPath() string { return "" }
func (p *usageUpdateProtocol) Close() error           { return nil }

func (p *usageUpdateProtocol) ParseLine(line []byte) ([]llm.SDKMessage, error) {
	var raw usageUpdateLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}
	switch raw.Type {
	case "usage_update":
		return []llm.SDKMessage{{
			Type: "usage_update",
			UsageUpdate: &llm.Usage{
				InputTokens:        raw.InputTokens,
				OutputTokens:       raw.OutputTokens,
				ContextInputTokens: raw.ContextInputTokens,
				ContextTotalTokens: raw.ContextTotalTokens,
				ContextBaseline:    raw.ContextBaseline,
				ContextWindow:      raw.ContextWindow,
				CostUSD:            raw.CostUSD,
			},
		}}, nil
	case "result":
		return []llm.SDKMessage{{
			Type: "result",
			Result: &llm.ResultMessage{
				Subtype:      raw.Subtype,
				SessionID:    raw.SessionID,
				TotalCostUSD: raw.TotalCostUSD,
			},
		}}, nil
	}
	return nil, nil
}

func TestAccumulatedUsageCodexSETSemantics(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process stdout replay with per-test session state.
	// Three usage_update messages with INCREASING cumulative totals,
	// followed by a result message for clean session termination.
	s := NewSession("codex-usage-test", "feat-1", feature.PhaseImplement)
	s.protocol = &usageUpdateProtocol{sid: "codex-test"}
	runSessionWithStdoutLines(t, s, []string{
		`{"type":"usage_update","input_tokens":100,"output_tokens":50,"cost_usd":0.01}`,
		`{"type":"usage_update","input_tokens":250,"output_tokens":120,"cost_usd":0.03}`,
		`{"type":"usage_update","input_tokens":400,"output_tokens":200,"cost_usd":0.05}`,
		`{"type":"result","subtype":"success","session_id":"codex-test","total_cost_usd":0.05}`,
	}, nil)

	// With SET semantics, AccumulatedUsage should reflect the LAST values,
	// NOT the sum (which would be 750, 370).
	got := s.AccumulatedUsage()
	if got.InputTokens != 400 {
		t.Errorf("AccumulatedUsage().InputTokens = %d, want 400 (SET semantics, not sum 750)", got.InputTokens)
	}
	if got.OutputTokens != 200 {
		t.Errorf("AccumulatedUsage().OutputTokens = %d, want 200 (SET semantics, not sum 370)", got.OutputTokens)
	}
	if got.CostUSD != 0.05 {
		t.Errorf("AccumulatedUsage().CostUSD = %v, want 0.05 (latest cumulative snapshot)", got.CostUSD)
	}
}

// TestContextPercentage_CodexZeroFallbackRetainsPriorFill verifies that when
// Codex sends a usage_update with ContextTotalTokens=0 (e.g. thread resume
// before first turn, or the fill_to_context_window corruption from
// openai/codex#16068), the session KEEPS the previously reported fill rather
// than falling back to the lifetime-cumulative InputTokens — which would pin
// the display at 100% for mature sessions.
func TestContextPercentage_CodexZeroFallbackRetainsPriorFill(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process stdout replay with per-test session state.
	// Turn 1: valid fill (50000 of 258400, 12K baseline) → (50000-12000)/246400 ≈ 15%
	// Turn 2: cumulative input balloons to 500_000 but Last.TotalTokens=0.
	//         The fix keeps turn 1's fill; the old code would have shown 100%.
	s := NewSession("codex-zero-fallback", "feat-zf", feature.PhaseImplement)
	s.protocol = &usageUpdateProtocol{sid: "codex-zero"}
	runSessionWithStdoutLines(t, s, []string{
		`{"type":"usage_update","input_tokens":50000,"output_tokens":100,"context_input_tokens":49900,"context_total_tokens":50000,"context_baseline":12000,"context_window":258400}`,
		`{"type":"usage_update","input_tokens":500000,"output_tokens":500,"context_input_tokens":0,"context_total_tokens":0,"context_baseline":12000,"context_window":258400}`,
		`{"type":"result","subtype":"success","session_id":"codex-zero","total_cost_usd":0.1}`,
	}, nil)

	pct := s.ContextPercentage()
	// (50000-12000) / (258400-12000) = 38000/246400 = 15.4% → 15
	if pct != 15 {
		t.Errorf("ContextPercentage() = %d, want 15 (prior fill retained, not cumulative 500000)", pct)
	}
}

// recordingAttachDropReporter captures ReportAttachDrop calls for
// assertion. Used by TestSession_AttachDropReporterFiresOnCriticalDrop.
type recordingAttachDropReporter struct {
	mu    sync.Mutex
	calls []attachDropCall
}

type attachDropCall struct {
	sessionID string
	featureID string
	phase     string
	msgType   string
	timeout   time.Duration
}

func (r *recordingAttachDropReporter) ReportAttachDrop(sessionID, featureID, phase, msgType string, timeout time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, attachDropCall{
		sessionID: sessionID,
		featureID: featureID,
		phase:     phase,
		msgType:   msgType,
		timeout:   timeout,
	})
}

func (r *recordingAttachDropReporter) snapshot() []attachDropCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]attachDropCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func TestSession_NoAttachConsumerSuppressesCriticalDropReport(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test attach buffer.
	wantTimeout := 200 * time.Millisecond

	s := NewSession("headless-drop-test", "feat-headless", feature.PhaseImplement)
	s.setCriticalAttachSendTimeoutForTest(wantTimeout)
	for i := 0; i < cap(s.attachCh); i++ {
		s.attachCh <- llm.SDKMessage{Type: "assistant"}
	}

	reporter := &recordingAttachDropReporter{}
	s.SetAttachDropReporter(reporter)

	started := time.Now()
	runMockSession(t, s, []llm.SDKMessage{
		{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{SessionID: "s1", Model: "m"}},
		{Type: "result", Result: &llm.ResultMessage{Type: "result", Subtype: "success", SessionID: "s1", TotalCostUSD: 0.01}},
	}, func(llm.SDKMessage) {})
	elapsed := time.Since(started)

	if calls := reporter.snapshot(); len(calls) != 0 {
		t.Fatalf("expected no AttachDrop reports without an attach consumer; got %d", len(calls))
	}
	if elapsed >= wantTimeout/2 {
		t.Fatalf("headless critical drop waited %s; want it to skip the %s timeout", elapsed, wantTimeout)
	}
}

// TestSession_AttachDropReporterFiresOnCriticalDrop simulates a stuck
// attachCh consumer. The CLI emits a Result message while the attachCh
// is full — the bounded blocking send at the drop site must time out,
// log a line, AND notify the AttachDropReporter (F9-2). Uses a shrunk
// per-session critical attach timeout so the test completes quickly.
func TestSession_AttachDropReporterFiresOnCriticalDrop(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test attach buffer.
	wantTimeout := 50 * time.Millisecond

	s := NewSession("drop-test", "feat-9", feature.PhaseImplement)
	s.setCriticalAttachSendTimeoutForTest(wantTimeout)
	unregister := registerAttachConsumerForTest(t, s)
	defer unregister()
	for i := 0; i < cap(s.attachCh); i++ {
		s.attachCh <- llm.SDKMessage{Type: "assistant"}
	}

	reporter := &recordingAttachDropReporter{}
	s.SetAttachDropReporter(reporter)

	// Intentionally do NOT drain s.AttachCh(). The buffer saturates and
	// the critical Result send times out — firing the reporter exactly
	// once per timed-out critical message.
	runMockSession(t, s, []llm.SDKMessage{
		{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{SessionID: "s1", Model: "m"}},
		{Type: "result", Result: &llm.ResultMessage{Type: "result", Subtype: "success", SessionID: "s1", TotalCostUSD: 0.01}},
	}, func(llm.SDKMessage) {})

	calls := reporter.snapshot()
	if len(calls) == 0 {
		t.Fatalf("expected at least one AttachDrop report; got 0")
	}
	// Every call must carry identity — this is the F9 / F4 assertion:
	// no empty FeatureID on the metric.
	for i, c := range calls {
		if c.sessionID != "drop-test" {
			t.Errorf("call %d: sessionID = %q, want %q", i, c.sessionID, "drop-test")
		}
		if c.featureID != "feat-9" {
			t.Errorf("call %d: featureID = %q, want %q", i, c.featureID, "feat-9")
		}
		if c.phase != feature.PhaseImplement.String() {
			t.Errorf("call %d: phase = %q, want %q", i, c.phase, feature.PhaseImplement.String())
		}
		if c.msgType == "" {
			t.Errorf("call %d: msgType is empty", i)
		}
		if c.timeout != wantTimeout {
			t.Errorf("call %d: timeout = %v, want %v", i, c.timeout, wantTimeout)
		}
	}
}

// TestSession_AttachDropReporterSilentWithoutReporter verifies that
// sessions without an installed reporter don't crash on drops — the
// log.Printf path is still taken, just no metric.
func TestSession_AttachDropReporterSilentWithoutReporter(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test attach buffer.
	const timeout = 50 * time.Millisecond

	s := NewSession("quiet-test", "feat-9", feature.PhaseImplement)
	s.setCriticalAttachSendTimeoutForTest(timeout)
	unregister := registerAttachConsumerForTest(t, s)
	defer unregister()
	for i := 0; i < cap(s.attachCh); i++ {
		s.attachCh <- llm.SDKMessage{Type: "assistant"}
	}
	// No SetAttachDropReporter: nil reporter must be tolerated.

	runMockSession(t, s, []llm.SDKMessage{
		{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{SessionID: "s1", Model: "m"}},
		{Type: "result", Result: &llm.ResultMessage{Type: "result", Subtype: "success", SessionID: "s1", TotalCostUSD: 0.01}},
	}, func(llm.SDKMessage) {})
	// No assertions on state — we only need the absence of a panic.
}

// TestSession_DrainerExitsOnCloseWithFullAttachChAndRingItems reproduces
// the shutdown hang seen when a session crashes with the desktop app detached:
// attachCh stays full (no consumer), the streamRing still has buffered
// deltas, and the drainer must still exit so readMessages' cleanup
// <-drainerDone does not deadlock. Without this guarantee the feature
// stays pinned in "Stopping…" forever and blocks Rewind.
func TestSession_DrainerExitsOnCloseWithFullAttachChAndRingItems(t *testing.T) {
	s := NewSession("drain-exit", "feat-drain", feature.PhaseImplement)

	// Saturate attachCh beyond the drainer's reserve so the reserve
	// check would short-circuit the inner drain loop in normal
	// operation. No consumer is attached — this models a detached desktop app.
	for i := 0; i < cap(s.attachCh); i++ {
		s.attachCh <- llm.SDKMessage{Type: "filler"}
	}

	// Buffer stream deltas that would otherwise keep the drainer
	// polling until its isClosedAndEmpty check can return true.
	for i := 0; i < streamRingCap/2; i++ {
		s.streamRing.Push(llm.SDKMessage{Type: "stream_event", StreamDeltaType: "text"})
	}

	s.ensureStreamDrainer()
	s.streamRing.Close()

	select {
	case <-s.drainerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("drainer did not exit within 2s after Close — regression of the rewind-hang bug")
	}
}

// TestSession_StreamBackpressurePreservesCritical is the F9 follow-up
// stress test. A subprocess floods the session with 10,000 stream_event
// deltas interleaved with 100 Result messages while the attach consumer
// drains at roughly 1/10 the producer rate. The drop-oldest stream ring
// plus attachStreamReserve slot reservation must deliver every Result
// to the consumer and never fire AttachDropReporter for a critical
// message (Result / ControlRequest).
func TestSession_StreamBackpressurePreservesCritical(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test — runs without -short")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "flood.sh")

	const (
		streamCount = 10000
		resultCount = 100
	)

	var sb strings.Builder
	sb.WriteString("#!/bin/bash\n")
	sb.WriteString(`echo '{"type":"system","subtype":"init","session_id":"s1","model":"m"}'` + "\n")

	// Interleave stream events with Results so the critical send path
	// competes with a live flood, not just a pre-filled backlog.
	interval := streamCount / resultCount // 100
	resultsEmitted := 0
	for i := 1; i <= streamCount; i++ {
		sb.WriteString(`echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"x"}}}'` + "\n")
		if i%interval == 0 && resultsEmitted < resultCount {
			sb.WriteString(`echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01}'` + "\n")
			resultsEmitted++
		}
	}
	if resultsEmitted != resultCount {
		t.Fatalf("test setup: emitted %d results, want %d", resultsEmitted, resultCount)
	}
	if err := os.WriteFile(script, []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	reporter := &recordingAttachDropReporter{}
	s := NewSession("stress", "feat-stress", feature.PhaseImplement)
	s.protocol = claude.NewProtocol(llm.ProtocolOpts{WorkDir: dir})
	s.SetAttachDropReporter(reporter)

	// Slow consumer — targets ~1/10 the producer's line-read rate.
	// The exact ratio is not load-bearing: we only need enough
	// backpressure to keep attachCh near its cap throughout the run.
	var resultsDelivered atomic.Int64
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for msg := range s.AttachCh() {
			if msg.Result != nil {
				resultsDelivered.Add(1)
			}
			// Retained: this is the deliberate slow-consumer simulation that
			// keeps attachCh under backpressure.
			time.Sleep(500 * time.Microsecond)
		}
	}()

	if err := s.Start([]string{"bash", script}, dir, nil, func(llm.SDKMessage) {}); err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-consumerDone:
	case <-time.After(90 * time.Second):
		t.Fatal("consumer did not finish within 90s")
	}

	if got := resultsDelivered.Load(); got != int64(resultCount) {
		t.Errorf("delivered %d/%d Result messages", got, resultCount)
	}
	var critDrops int
	for _, c := range reporter.snapshot() {
		if c.msgType == "result" || c.msgType == "control_request" {
			critDrops++
		}
	}
	if critDrops != 0 {
		t.Errorf("AttachDropReporter fired %d times for critical messages; want 0", critDrops)
	}
}

// TestSession_StatusChSignalAfterLogAppend is a regression test that exercises
// the race between statusCh and MessageLog.Append for result messages.
// A receiver woken by statusCh must observe a MessageLog that already contains
// the result message. Bounded-helper runs (read-only reviewers and validators)
// parse the result out of MessageLog.LastResultMessage() immediately after
// receiving on statusCh, and a stale read returns an empty verdict.
func TestSession_StatusChSignalAfterLogAppend(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test sessions.
	iterations := 500
	parallel := 16
	if testing.Short() {
		iterations = 20
		parallel = 4
	}

	// Run sessions in parallel so scheduler pressure amplifies any race between
	// statusCh signal and MessageLog.Append. Serial execution misses the window.
	var wg sync.WaitGroup
	var failures atomic.Int64
	for p := 0; p < parallel; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				s := NewSession("race-test", "feat-1", feature.PhaseImplement)
				runMockSession(t, s, []llm.SDKMessage{
					{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{SessionID: "s1", Model: "test"}},
					{Type: "result", Result: &llm.ResultMessage{
						Type:         "result",
						Subtype:      "success",
						SessionID:    "s1",
						TotalCostUSD: 0.01,
						Result:       "verdict: APPROVED",
					}},
				}, nil)

				select {
				case status := <-s.statusCh:
					if status != "SUCCESS" {
						t.Errorf("iteration %d: statusCh = %q, want SUCCESS", i, status)
						failures.Add(1)
					}
				case <-time.After(10 * time.Second):
					t.Errorf("iteration %d: timed out waiting for status", i)
					failures.Add(1)
					return
				}

				rm := s.messageLog.LastResultMessage()
				if rm == nil {
					t.Errorf("iteration %d: MessageLog.LastResultMessage() is nil immediately after statusCh signal — race regressed", i)
					failures.Add(1)
				} else if rm.Result != "verdict: APPROVED" {
					t.Errorf("iteration %d: result text = %q, want APPROVED verdict", i, rm.Result)
					failures.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if failures.Load() > 0 {
		t.Fatalf("%d race failures across %d parallel workers × %d iterations", failures.Load(), parallel, iterations)
	}
}

func TestSession_StatusChSignalAfterResultCallback(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol replay with per-test attach buffer.
	s := NewSession("callback-order-test", "feat-1", feature.PhaseImplement)
	s.setCriticalAttachSendTimeoutForTest(100 * time.Millisecond)
	for i := 0; i < cap(s.attachCh); i++ {
		s.attachCh <- llm.SDKMessage{Type: "assistant"}
	}

	resultCallbackDone := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockSession(t, s, []llm.SDKMessage{
			{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{SessionID: "s1", Model: "test"}},
			{Type: "result", Result: &llm.ResultMessage{Type: "result", Subtype: "success", SessionID: "s1", TotalCostUSD: 0.01}},
		}, func(msg llm.SDKMessage) {
			if msg.Result != nil {
				close(resultCallbackDone)
			}
		})
	}()

	select {
	case status := <-s.statusCh:
		if status != "SUCCESS" {
			t.Fatalf("statusCh = %q, want SUCCESS", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for status")
	}

	select {
	case <-resultCallbackDone:
	default:
		t.Fatal("statusCh signaled before result callback completed")
	}

	<-done
}

func taskStartedMsg(taskID string) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "system", Subtype: "task_started",
		TaskStarted: &llm.TaskStartedMessage{TaskID: taskID},
	}
}

func taskProgressMsg(taskID string) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "system", Subtype: "task_progress",
		TaskProgress: &llm.TaskProgressMessage{TaskID: taskID},
	}
}

func taskNotificationMsg(taskID string) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "system", Subtype: "task_notification",
		TaskNotification: &llm.TaskNotificationMessage{TaskID: taskID, Status: "completed"},
	}
}

func TestObserveBackgroundTasks_TracksLifecycle(t *testing.T) {
	s := NewSession("bg-test", "feat-1", feature.PhaseImplement)

	if got := s.LiveBackgroundTaskCount(); got != 0 {
		t.Fatalf("initial LiveBackgroundTaskCount() = %d, want 0", got)
	}

	s.observeTaskActivity(taskStartedMsg("task-a"))
	s.observeTaskActivity(taskStartedMsg("task-b"))
	if got := s.LiveBackgroundTaskCount(); got != 2 {
		t.Fatalf("after two starts LiveBackgroundTaskCount() = %d, want 2", got)
	}

	// Duplicate progress for a known task must not double-count.
	s.observeTaskActivity(taskProgressMsg("task-a"))
	if got := s.LiveBackgroundTaskCount(); got != 2 {
		t.Fatalf("after progress LiveBackgroundTaskCount() = %d, want 2", got)
	}

	// Progress for a task whose start we never saw (session attach mid-task)
	// registers it as live.
	s.observeTaskActivity(taskProgressMsg("task-c"))
	if got := s.LiveBackgroundTaskCount(); got != 3 {
		t.Fatalf("after unseen-task progress LiveBackgroundTaskCount() = %d, want 3", got)
	}

	// task_notification is the terminal lifecycle event.
	s.observeTaskActivity(taskNotificationMsg("task-a"))
	s.observeTaskActivity(taskNotificationMsg("task-b"))
	s.observeTaskActivity(taskNotificationMsg("task-c"))
	if got := s.LiveBackgroundTaskCount(); got != 0 {
		t.Fatalf("after notifications LiveBackgroundTaskCount() = %d, want 0", got)
	}

	// Delayed progress after a terminal event must not resurrect a phantom task.
	s.observeTaskActivity(taskProgressMsg("task-a"))
	if got := s.LiveBackgroundTaskCount(); got != 0 {
		t.Fatalf("after delayed terminal-task progress LiveBackgroundTaskCount() = %d, want 0", got)
	}

	// Notification for an unknown task records a terminal snapshot without
	// producing a panic or negative count.
	s.observeTaskActivity(taskNotificationMsg("task-x"))
	if got := s.LiveBackgroundTaskCount(); got != 0 {
		t.Fatalf("after unknown notification LiveBackgroundTaskCount() = %d, want 0", got)
	}
}

func TestTaskActivities_PreserveProviderNeutralLifecycleDetails(t *testing.T) {
	s := NewSession("task-activity-test", "feat-1", feature.PhaseImplement)
	startedAt := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	progressAt := startedAt.Add(2 * time.Minute)
	finishedAt := progressAt.Add(time.Minute)

	s.observeTaskActivity(llm.SDKMessage{
		Type:       "system",
		Subtype:    "task_started",
		OccurredAt: startedAt,
		Origin: llm.EventOrigin{
			Kind:           llm.EventOriginTask,
			TaskID:         "task-a",
			ChildSessionID: "child-session-a",
		},
		TaskStarted: &llm.TaskStartedMessage{
			TaskID:      "task-a",
			ToolUseID:   "tool-a",
			Description: "Refactor the execution path",
		},
	})
	s.observeTaskActivity(llm.SDKMessage{
		Type:       "system",
		Subtype:    "task_progress",
		OccurredAt: progressAt,
		Origin: llm.EventOrigin{
			Kind:           llm.EventOriginTask,
			TaskID:         "task-a",
			ChildSessionID: "child-session-a",
		},
		TaskProgress: &llm.TaskProgressMessage{
			TaskID:       "task-a",
			LastToolName: "apply_patch",
			LastPath:     "internal/server/runtime.go",
			Usage:        &llm.TaskUsage{TotalTokens: 1200, ToolUses: 4},
		},
	})

	running := s.TaskActivities()
	if len(running) != 1 {
		t.Fatalf("len(TaskActivities()) = %d, want 1", len(running))
	}
	if got := running[0]; got.State != llm.TaskActivityRunning ||
		got.TaskID != "task-a" ||
		got.ChildSessionID != "child-session-a" ||
		got.Description != "Refactor the execution path" ||
		got.LastToolName != "apply_patch" ||
		got.LastPath != "internal/server/runtime.go" ||
		!got.StartedAt.Equal(startedAt) ||
		!got.UpdatedAt.Equal(progressAt) ||
		got.Usage == nil ||
		got.Usage.TotalTokens != 1200 {
		t.Fatalf("running TaskActivities()[0] = %+v", got)
	}
	if got := s.LiveBackgroundTaskCount(); got != 1 {
		t.Fatalf("LiveBackgroundTaskCount() = %d, want 1", got)
	}

	s.observeTaskActivity(llm.SDKMessage{
		Type:       "system",
		Subtype:    "task_notification",
		OccurredAt: finishedAt,
		Origin: llm.EventOrigin{
			Kind:           llm.EventOriginTask,
			TaskID:         "task-a",
			ChildSessionID: "child-session-a",
		},
		TaskNotification: &llm.TaskNotificationMessage{
			TaskID:  "task-a",
			Status:  "failed",
			Summary: "tests failed",
		},
	})

	finished := s.TaskActivities()
	if len(finished) != 1 {
		t.Fatalf("len(TaskActivities()) after terminal event = %d, want 1", len(finished))
	}
	if got := finished[0]; got.State != llm.TaskActivityFailed ||
		got.Summary != "tests failed" ||
		!got.FinishedAt.Equal(finishedAt) ||
		!got.UpdatedAt.Equal(finishedAt) {
		t.Fatalf("finished TaskActivities()[0] = %+v", got)
	}
	if got := s.LiveBackgroundTaskCount(); got != 0 {
		t.Fatalf("LiveBackgroundTaskCount() after terminal event = %d, want 0", got)
	}
}

func TestTaskActivities_CorrelatesToolUseFallbackWithLaterProviderTaskID(t *testing.T) {
	s := NewSession("task-identity-upgrade", "feat-1", feature.PhaseImplement)
	s.observeTaskActivity(llm.SDKMessage{
		OccurredAt: time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC),
		TaskStarted: &llm.TaskStartedMessage{
			ToolUseID:   "tool-a",
			Description: "Started before the provider assigned a task ID",
		},
	})
	s.observeTaskActivity(llm.SDKMessage{
		OccurredAt: time.Date(2026, 7, 28, 10, 1, 0, 0, time.UTC),
		TaskProgress: &llm.TaskProgressMessage{
			TaskID:      "task-a",
			ToolUseID:   "tool-a",
			Description: "Provider task ID assigned",
		},
	})
	s.observeTaskActivity(llm.SDKMessage{
		OccurredAt: time.Date(2026, 7, 28, 10, 2, 0, 0, time.UTC),
		TaskNotification: &llm.TaskNotificationMessage{
			TaskID:    "task-a",
			ToolUseID: "tool-a",
			Status:    "completed",
		},
	})

	activities := s.TaskActivities()
	if len(activities) != 1 {
		t.Fatalf("TaskActivities() = %+v, want one correlated task", activities)
	}
	if got := activities[0]; got.TaskID != "task-a" ||
		got.ToolUseID != "tool-a" ||
		got.State != llm.TaskActivityCompleted {
		t.Fatalf("TaskActivities()[0] = %+v, want upgraded terminal task identity", got)
	}
	if got := s.LiveBackgroundTaskCount(); got != 0 {
		t.Fatalf("LiveBackgroundTaskCount() = %d, want 0", got)
	}
}

func TestRootCompletionIntent_IgnoresDelegatedTaskOutput(t *testing.T) {
	s := NewSession("completion-origin-test", "feat-1", feature.PhaseImplement)
	outcomeText := `<agentico-outcome>{"status":"success"}</agentico-outcome>`

	s.observeCompletionIntent(llm.SDKMessage{
		Type:   "assistant",
		Origin: llm.EventOrigin{Kind: llm.EventOriginTask, TaskID: "task-a"},
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Role:    "assistant",
				Content: []llm.ContentBlock{{Type: "text", Text: outcomeText}},
			},
		},
	})
	if got := s.RootCompletionIntent(); got.Found {
		t.Fatalf("child RootCompletionIntent() = %+v, want no root intent", got)
	}

	s.observeCompletionIntent(llm.SDKMessage{
		Type:   "assistant",
		Origin: llm.EventOrigin{Kind: llm.EventOriginRoot},
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Role:    "assistant",
				Content: []llm.ContentBlock{{Type: "text", Text: outcomeText}},
			},
		},
	})
	if got := s.RootCompletionIntent(); !got.Valid() || got.Status != llm.CompletionIntentSuccess {
		t.Fatalf("root RootCompletionIntent() = %+v, want valid success", got)
	}
}

func TestShouldShutdownOnResult_LoopManagedSessionsStayAlive(t *testing.T) {
	endTurn := &llm.ResultMessage{Type: "result", Subtype: "success", StopReason: "end_turn"}

	t.Run("keep-alive session with live tasks stays up on end_turn", func(t *testing.T) {
		s := NewSession("bg-keepalive", "feat-1", feature.PhaseImplement)
		s.keepAliveOnTurnResult = true
		s.observeTaskActivity(taskStartedMsg("task-a"))
		if s.shouldShutdownOnResult(endTurn) {
			t.Error("shouldShutdownOnResult() = true, want false while background tasks run")
		}
	})

	t.Run("keep-alive session without live tasks stays up for a harness nudge", func(t *testing.T) {
		s := NewSession("bg-none", "feat-1", feature.PhaseImplement)
		s.keepAliveOnTurnResult = true
		if s.shouldShutdownOnResult(endTurn) {
			t.Error("shouldShutdownOnResult() = true, want false for loop-managed turn")
		}
	})

	t.Run("non-loop session with live tasks still shuts down", func(t *testing.T) {
		s := NewSession("bg-oneshot", "feat-1", feature.PhaseImplement)
		s.observeTaskActivity(taskStartedMsg("task-a"))
		if !s.shouldShutdownOnResult(endTurn) {
			t.Error("shouldShutdownOnResult() = false, want true for non-keep-alive session")
		}
	})
}
