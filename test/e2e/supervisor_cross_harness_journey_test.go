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
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

type crossHarness struct {
	*supervisorHarness
	scripts map[string]string
}

func newCrossHarness(t *testing.T, source string) *crossHarness {
	t.Helper()
	box := newOpenCodeSandbox(t, false)
	for _, kv := range box.env {
		key, value, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(key, "XDG_") {
			t.Setenv(key, value)
		}
	}
	h := newHarnessBase(t, source)
	scripts := map[string]string{
		"claude":   testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeInteractiveScriptBody()),
		"codex":    testutil.WriteFakeCodexScript(t, testutil.FakeCodexScript{}),
		"opencode": testutil.WriteFakeOpenCodeScript(t, testutil.FakeOpenCodeScript{}),
	}
	h.script = scripts[source]
	h.registry = llm.NewRegistry()
	h.registry.Register(testutil.FakeClaudeProvider{Script: scripts["claude"]})
	h.registry.Register(testutil.NewFakeCodexProvider(t, scripts["codex"]))
	h.registry.Register(testutil.NewFakeOpenCodeProvider(t, scripts["opencode"]))
	h.init()
	h.chooseSettings()
	return &crossHarness{supervisorHarness: h, scripts: scripts}
}

func (h *crossHarness) switchTo(id, destination string, model, effort *string) server.SupervisorState {
	h.t.Helper()
	request := map[string]any{
		"harness": destination, "request_id": id,
		"expected_generation": h.state().Generation,
	}
	if model != nil {
		request["model"] = *model
	}
	if effort != nil {
		request["effort"] = *effort
	}
	var response server.SupervisorStateResponse
	h.do(http.MethodPatch, "/api/v1/supervisor/settings", request, http.StatusOK, &response)
	return response.State
}

func (h *crossHarness) launches(harness string) int {
	h.t.Helper()
	script := h.scripts[harness]
	switch harness {
	case "opencode":
		return testutil.FakeOpenCodeInvocations(h.t, script)
	default:
		data, err := os.ReadFile(filepath.Join(filepath.Dir(script), testutil.FakeSupervisorInvocationsFile))
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			h.t.Fatal(err)
		}
		return strings.Count(string(data), "\n")
	}
}

func (h *crossHarness) establishHistory() server.SupervisorState {
	h.t.Helper()
	h.send("The codeword is cobalt.", "early-decision")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	h.send("The launch color is amber.", "later-fact")
	return h.waitState("two source turns", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.HeadSeq >= 4
	})
}

func (h *crossHarness) assertSwitchRecords(source, destination, model string) {
	h.t.Helper()
	page := h.transcript("?limit=500")
	var marker, note int
	for _, record := range page.Items {
		if record.Kind == server.SupervisorRecordKindMarker && record.Marker != nil {
			if string(record.Marker.Marker) == "harness_change" {
				marker++
				if want := "Switched to " + harnessName(destination) + " · " + model; record.Marker.Text != want {
					h.t.Fatalf("harness marker = %q, want %q", record.Marker.Text, want)
				}
			}
			if record.Marker.Marker == server.SupervisorMarkerSettingsChanged {
				h.t.Fatal("harness switch wrote a settings_changed marker")
			}
		}
		if record.Kind == server.SupervisorRecordKindNote && record.Visibility == server.SupervisorVisibilityModelOnly && strings.Contains(record.Note, "switched") {
			note++
			if !strings.Contains(record.Note, harnessName(source)) || !strings.Contains(record.Note, harnessName(destination)) || !strings.Contains(record.Note, "tool calls") {
				h.t.Fatalf("switch note = %q", record.Note)
			}
		}
	}
	if marker != 1 || note != 1 {
		h.t.Fatalf("switch records: markers=%d notes=%d; kinds=%s", marker, note, recordKinds(page.Items))
	}
}

func harnessName(id string) string {
	switch id {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	default:
		return "OpenCode"
	}
}

func (h *crossHarness) assertDestinationArtifact(destination, native string) {
	h.t.Helper()
	switch destination {
	case "claude":
		data, err := os.ReadFile(h.nativeSessionFile(native))
		if err != nil || !strings.Contains(string(data), "The codeword is cobalt.") {
			h.t.Fatalf("Claude rebuild %q: %v, %s", native, err, data)
		}
	case "codex":
		resumes := testutil.FakeCodexResumes(h.t, h.scripts["codex"])
		if len(resumes) != 1 || !resumes[0].Found || resumes[0].ThreadID != native {
			h.t.Fatalf("Codex resume = %+v, native %q", resumes, native)
		}
		if prompts := rolloutUserPrompts(h.t, h.rolloutFor(native)); len(prompts) < 2 || !strings.Contains(strings.Join(prompts, "\n"), "The codeword is cobalt.") {
			h.t.Fatalf("Codex rollout prompts = %q", prompts)
		}
	case "opencode":
		seeds := testutil.FakeOpenCodeSeeds(h.t, h.scripts["opencode"])
		if len(seeds) != 1 || !seeds[0].NoReply || !strings.Contains(seeds[0].Text, "The codeword is cobalt.") || !strings.Contains(seeds[0].Text, "The launch color is amber.") {
			h.t.Fatalf("OpenCode seed = %+v", seeds)
		}
	}
}

func TestSupervisorCrossHarnessSixWayRecall(t *testing.T) {
	harnesses := []string{"claude", "codex", "opencode"}
	for _, source := range harnesses {
		for _, destination := range harnesses {
			if source == destination {
				continue
			}
			t.Run(source+"_to_"+destination, func(t *testing.T) {
				h := newCrossHarness(t, source)
				before := h.establishHistory()
				previousNative := h.coord.State().NativeSessionID
				changed := h.switchTo("switch", destination, nil, nil)
				models := h.registry.EligibleModelsForPhase(llm.PhaseChat)[destination]
				if changed.Lifecycle != server.SupervisorLifecycleStopped || changed.Settings.Harness != destination || changed.Settings.Model != models[0] || changed.Settings.Effort != "" || changed.PendingChange != nil || changed.Generation != before.Generation {
					t.Fatalf("switch state = %+v; default %q", changed, models[0])
				}
				native := h.coord.State().NativeSessionID
				if native == "" || native == previousNative {
					t.Fatalf("native id %q after %q", native, previousNative)
				}
				h.assertSwitchRecords(source, destination, models[0])
				if got := h.launches(source); got != 1 {
					t.Fatalf("source launches = %d, want one", got)
				}
				h.send(testutil.FakeSupervisorRecallFirst, "recall-first")
				h.waitLifecycle(server.SupervisorLifecycleIdle)
				if got := h.launches(destination); got != 1 {
					t.Fatalf("destination launches = %d, want one", got)
				}
				if got := lastAssistantText(h.transcript("?limit=500").Items); got != "First user prompt: The codeword is cobalt." {
					t.Fatalf("first recall = %q", got)
				}
				h.assertDestinationArtifact(destination, native)
				h.send(testutil.FakeSupervisorRecallLast, "recall-last")
				h.waitState("last recall", func(st server.SupervisorState) bool {
					return st.Lifecycle == server.SupervisorLifecycleIdle && st.HeadSeq >= 10
				})
				if got := lastAssistantText(h.transcript("?limit=500").Items); got != "Last exchange: The launch color is amber. | Hello from turn 2" {
					t.Fatalf("last recall = %q", got)
				}
			})
		}
	}
}

func TestSupervisorCrossHarnessTouchedSettings(t *testing.T) {
	h := newCrossHarness(t, "claude")
	h.establishHistory()
	model, effort := testutil.FakeCodexSecondModel, "high"
	st := h.switchTo("touched", "codex", &model, &effort)
	if st.Settings.Harness != "codex" || st.Settings.Model != model || st.Settings.Effort != effort || st.Lifecycle != server.SupervisorLifecycleStopped {
		t.Fatalf("touched settings = %+v", st)
	}
}

func TestSupervisorCrossHarnessQueuedStopAndCancel(t *testing.T) {
	h := newCrossHarness(t, "claude")
	h.send("hold "+testutil.FakeSupervisorHold, "held")
	h.waitLifecycle(server.SupervisorLifecycleRunning)
	queued := h.switchTo("queued-switch", "codex", nil, nil)
	if queued.PendingChange == nil || string(queued.PendingChange.Kind) != "harness" || queued.PendingChange.Target.Harness != "codex" || queued.Settings.Harness != "claude" {
		t.Fatalf("queued switch = %+v", queued)
	}
	var cancelled server.SupervisorStateResponse
	h.do(http.MethodDelete, "/api/v1/supervisor/pending-change/queued-switch", nil, http.StatusOK, &cancelled)
	if cancelled.State.PendingChange != nil || cancelled.State.Settings.Harness != "claude" {
		t.Fatalf("cancelled = %+v", cancelled.State)
	}
	queued = h.switchTo("queued-again", "codex", nil, nil)
	h.do(http.MethodPost, "/api/v1/supervisor/interrupt", map[string]any{}, http.StatusOK, nil)
	st := h.waitState("queued switch after stop", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleStopped && st.PendingChange == nil
	})
	if st.Settings.Harness != "codex" || st.Generation != queued.Generation {
		t.Fatalf("applied queued switch = %+v", st)
	}
}

func TestSupervisorCrossHarnessFailedDestinationRestoresSource(t *testing.T) {
	h := newCrossHarness(t, "codex")
	h.establishHistory()
	previous := h.coord.State().NativeSessionID
	testutil.UpdateFakeOpenCodeScript(t, h.scripts["opencode"], testutil.FakeOpenCodeScript{NoHTTP: true})
	h.switchTo("failed-switch", "opencode", nil, nil)
	var failed server.ErrorResponse
	h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{"text": "try switch", "client_message_id": "failed-message"}, http.StatusBadGateway, &failed)
	st := h.waitLifecycle(server.SupervisorLifecycleFailed)
	if st.Settings.Harness != "codex" || h.coord.State().NativeSessionID != previous || st.Failure == nil {
		t.Fatalf("rollback = %+v, native %q want %q", st, h.coord.State().NativeSessionID, previous)
	}
	markers := markerRecords(h.transcript("?limit=500"), server.SupervisorMarkerSettingsReverted)
	if len(markers) != 1 || !strings.Contains(markers[0].Marker.Text, "Couldn't switch to OpenCode") || !strings.Contains(markers[0].Marker.Text, "still using Codex") {
		t.Fatalf("revert markers = %+v", markers)
	}
	encoded, _ := json.Marshal(st.Failure)
	if !strings.Contains(string(encoded), `"harness":"opencode"`) {
		t.Fatalf("failure does not expose attempted settings: %s", encoded)
	}
	h.send("continue on codex", "after-rollback")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	resumes := testutil.FakeCodexResumes(t, h.scripts["codex"])
	if len(resumes) != 1 || !resumes[0].Found || resumes[0].ThreadID != previous {
		t.Fatalf("restored Codex resume = %+v, want %q", resumes, previous)
	}
}

func TestSupervisorCrossHarnessPendingAppliesAtBoot(t *testing.T) {
	h := newCrossHarness(t, "claude")
	h.establishHistory()
	previous := h.coord.State().NativeSessionID
	h.send("hold "+testutil.FakeSupervisorHold, "boot-hold")
	h.waitLifecycle(server.SupervisorLifecycleRunning)
	h.switchTo("boot-switch", "opencode", nil, nil)
	// Reboot from an isolated crash image. The abandoned coordinator still
	// has goroutines in this test process; when boot kills its orphan, its
	// exit watcher can apply the pending switch to its old files. A real
	// crashed server cannot do that. Keep those writes out of the new boot's
	// transcript while retaining the live provider/PID for orphan recovery.
	crashState := t.TempDir()
	if err := os.CopyFS(crashState, os.DirFS(h.stateDir)); err != nil {
		t.Fatal(err)
	}
	h.stateDir = crashState
	h.crash()
	st := h.waitLifecycle(server.SupervisorLifecycleStopped)
	if st.PendingChange != nil || st.Settings.Harness != "opencode" || h.coord.State().NativeSessionID == previous {
		t.Fatalf("boot switch = %+v, previous native %q", st, previous)
	}
	page := h.transcript("?limit=500")
	var interrupted, changed int64
	for _, record := range page.Items {
		if record.Kind != server.SupervisorRecordKindMarker || record.Marker == nil {
			continue
		}
		if record.Marker.Marker == server.SupervisorMarkerInterrupted {
			interrupted = record.Seq
		}
		if string(record.Marker.Marker) == "harness_change" {
			changed = record.Seq
		}
	}
	if interrupted == 0 || changed <= interrupted {
		t.Fatalf("boot marker order interrupted=%d changed=%d: %s", interrupted, changed, recordKinds(page.Items))
	}
	h.send("after boot", fmt.Sprintf("after-boot-%d", changed))
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	h.assertDestinationArtifact("opencode", h.coord.State().NativeSessionID)
}
