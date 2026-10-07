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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func (h *supervisorHarness) changeSettings(id, model, effort string) server.SupervisorState {
	h.t.Helper()
	var response server.SupervisorStateResponse
	h.do(http.MethodPatch, "/api/v1/supervisor/settings", map[string]any{
		"model": model, "effort": effort, "request_id": id,
		"expected_generation": h.state().Generation,
	}, http.StatusOK, &response)
	return response.State
}

func alternateCodexModel(current string) string {
	if current == testutil.FakeCodexModel {
		return testutil.FakeCodexSecondModel
	}
	return testutil.FakeCodexModel
}

func alternateOpenCodeModel(current string) string {
	if current == testutil.FakeOpenCodeModel {
		return testutil.FakeOpenCodeSecondModel
	}
	return testutil.FakeOpenCodeModel
}

func TestSupervisorCodexSettingsChangeInPlace(t *testing.T) {
	h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
	h.chooseSettings()
	h.send("first", "settings-first")
	first := h.waitLifecycle(server.SupervisorLifecycleIdle)
	target := alternateCodexModel(first.Settings.Model)
	changed := h.changeSettings("model-next", target, "high")
	if changed.Generation != first.Generation || changed.Lifecycle != server.SupervisorLifecycleIdle || changed.Settings.Model != target || changed.Settings.Effort != "high" {
		t.Fatalf("in-place change = %+v", changed)
	}
	updates := 0
	for _, request := range testutil.FakeCodexRequests(t, h.script) {
		if request.Method != "thread/settings/update" {
			continue
		}
		updates++
		var params struct{ Model, Effort string }
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(target, params.Model) || params.Effort != "high" {
			t.Fatalf("settings update = %+v", params)
		}
	}
	if updates != 1 {
		t.Fatalf("settings updates = %d, want 1", updates)
	}
	h.send("second", "settings-second")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	if h.state().Generation != first.Generation || h.invocations() != 1 {
		t.Fatalf("change relaunched Codex: state=%+v invocations=%d", h.state(), h.invocations())
	}
	turns := h.codexTurns()
	if len(turns) != 2 || !strings.Contains(string(turns[1].Params), `"effort":"high"`) {
		t.Fatalf("second turn = %+v", turns)
	}
	if got := h.transcript(""); len(markerRecords(got, server.SupervisorMarkerSettingsChanged)) == 0 {
		t.Fatal("missing settings_changed marker")
	}
}

func TestSupervisorCodexQueuedSettingsCancelAndApply(t *testing.T) {
	h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
	h.chooseSettings()
	h.send("hold "+testutil.FakeCodexHold, "settings-hold")
	h.waitLifecycle(server.SupervisorLifecycleRunning)
	target := alternateCodexModel(h.model)
	queued := h.changeSettings("queued-model", target, "")
	if queued.PendingChange == nil || queued.PendingChange.Kind != server.SupervisorPendingChangeKindModel || queued.Settings.Model == target {
		t.Fatalf("queued = %+v", queued)
	}
	var conflict server.ErrorResponse
	h.do(http.MethodPatch, "/api/v1/supervisor/settings", map[string]any{
		"model": target, "request_id": "different-pending", "expected_generation": queued.Generation,
	}, http.StatusConflict, &conflict)
	if conflict.Error.Code != "change_pending" {
		t.Fatalf("concurrent change = %+v", conflict)
	}
	var cancelled server.SupervisorStateResponse
	h.do(http.MethodDelete, "/api/v1/supervisor/pending-change/queued-model", nil, http.StatusOK, &cancelled)
	if cancelled.State.PendingChange != nil {
		t.Fatalf("cancelled = %+v", cancelled.State)
	}
	queued = h.changeSettings("queued-effort", h.model, "high")
	if queued.PendingChange == nil || queued.PendingChange.Kind != server.SupervisorPendingChangeKindEffort {
		t.Fatalf("effort queued = %+v", queued)
	}
	h.do(http.MethodPost, "/api/v1/supervisor/interrupt", map[string]any{}, http.StatusOK, nil)
	applied := h.waitState("queued settings applied", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.PendingChange == nil
	})
	if applied.Settings.Effort != "high" || applied.Settings.Model != h.model {
		t.Fatalf("applied = %+v", applied)
	}
}

func TestSupervisorPendingSettingsApplyAfterServerRestart(t *testing.T) {
	h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
	h.chooseSettings()
	h.send("hold "+testutil.FakeCodexHold, "restart-hold")
	h.waitLifecycle(server.SupervisorLifecycleRunning)
	target := alternateCodexModel(h.model)
	queued := h.changeSettings("restart-target", target, "")
	if queued.PendingChange == nil {
		t.Fatalf("pending before restart = %+v", queued)
	}
	h.crash()
	booted := h.waitLifecycle(server.SupervisorLifecycleStopped)
	if booted.Settings.Model != target || booted.PendingChange != nil {
		t.Fatalf("boot application = %+v", booted)
	}
	if _, err := h.coord.Transcript(supervisor.PageQuery{Limit: 500}); err != nil {
		t.Fatalf("boot transcript: %v", err)
	}
	page := h.transcript("")
	if len(markerRecords(page, server.SupervisorMarkerInterrupted)) == 0 || len(markerRecords(page, server.SupervisorMarkerSettingsChanged)) == 0 {
		t.Fatalf("missing interruption/change markers: %s", recordKinds(page.Items))
	}
}

func TestSupervisorCodexSettingsRejectionAndStaleGeneration(t *testing.T) {
	h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{RejectSettingsUpdate: true})
	h.chooseSettings()
	h.send("first", "reject-first")
	first := h.waitLifecycle(server.SupervisorLifecycleIdle)
	target := alternateCodexModel(first.Settings.Model)
	changed := h.changeSettings("rejected-model", target, "")
	if changed.Settings.Model != first.Settings.Model || changed.Generation != first.Generation {
		t.Fatalf("rejected update changed settings: %+v", changed)
	}
	if got := h.transcript(""); len(markerRecords(got, server.SupervisorMarkerSettingsReverted)) == 0 {
		t.Fatal("missing settings_reverted marker")
	}
	var conflict server.ErrorResponse
	h.do(http.MethodPatch, "/api/v1/supervisor/settings", map[string]any{
		"model": target, "request_id": "stale-model", "expected_generation": first.Generation - 1,
	}, http.StatusConflict, &conflict)
	if conflict.Error.Code != "stale_generation" {
		t.Fatalf("stale response = %+v", conflict)
	}
}

func TestSupervisorOpenCodeModelChangeInPlace(t *testing.T) {
	h := newOpenCodeSupervisorHarness(t, testutil.FakeOpenCodeScript{})
	h.chooseSettings()
	h.send("first", "opencode-settings-first")
	first := h.waitLifecycle(server.SupervisorLifecycleIdle)
	target := alternateOpenCodeModel(first.Settings.Model)
	changed := h.changeSettings("opencode-model", target, "")
	if changed.Generation != first.Generation || changed.Settings.Model != target {
		t.Fatalf("model switch = %+v", changed)
	}
	h.send("second", "opencode-settings-second")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	if h.invocations() != 1 {
		t.Fatalf("OpenCode relaunched %d times", h.invocations())
	}
	lines := testutil.FakeOpenCodeACP(t, h.script)
	var switchAt, promptAt int = -1, -1
	for i, line := range lines {
		if line.Dir == "in" && line.Method == "session/set_model" {
			switchAt = i
		}
		if line.Dir == "in" && line.Method == "session/prompt" {
			promptAt = i
		}
	}
	if switchAt < 0 || promptAt < switchAt {
		t.Fatalf("set-model/prompt ordering: switch=%d prompt=%d", switchAt, promptAt)
	}
}

type twoModelClaudeProvider struct{ testutil.FakeClaudeProvider }

func (p twoModelClaudeProvider) ModelCatalog() []llm.ModelInfo {
	models := p.FakeClaudeProvider.ModelCatalog()
	second := models[0]
	second.ID, second.DisplayName, second.Aliases = "sonnet[200K]", "Claude Sonnet", []string{"sonnet"}
	return append(models, second)
}
func (p twoModelClaudeProvider) AvailableModels() []string {
	return []string{"haiku[200K]", "sonnet[200K]"}
}
func (p twoModelClaudeProvider) MatchesModel(model string) bool {
	return p.FakeClaudeProvider.MatchesModel(model) || model == "sonnet[200K]" || model == "sonnet"
}

func TestSupervisorClaudeModelChangeRelaunches(t *testing.T) {
	h := newHarnessBase(t, "claude")
	h.script = testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeInteractiveScriptBody())
	h.registry = llm.NewRegistry()
	h.registry.Register(twoModelClaudeProvider{testutil.FakeClaudeProvider{Script: h.script}})
	h.init()
	h.chooseSettings()
	h.send("first", "claude-settings-first")
	first := h.waitLifecycle(server.SupervisorLifecycleIdle)
	target := "sonnet[200K]"
	if first.Settings.Model == target {
		target = "haiku[200K]"
	}
	changed := h.changeSettings("claude-model", target, "")
	if changed.Lifecycle != server.SupervisorLifecycleStopped || changed.Settings.Model != target {
		t.Fatalf("Claude change = %+v", changed)
	}
	h.send("second", "claude-settings-second")
	second := h.waitLifecycle(server.SupervisorLifecycleIdle)
	if second.Generation <= first.Generation || h.invocations() != 2 {
		t.Fatalf("Claude relaunch = %+v, invocations=%d", second, h.invocations())
	}
}
