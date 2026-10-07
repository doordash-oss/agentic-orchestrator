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
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// TestCodexSettingsChangeLive proves the experimental settings update on the
// installed CLI; the fake app-server cannot establish CLI compatibility.
func TestCodexSettingsChangeLive(t *testing.T) {
	if testing.Short() || os.Getenv("AGENTIC_CODEX_LIVE") != "1" {
		t.Skip("set AGENTIC_CODEX_LIVE=1 without -short to run live Codex settings changes")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("Codex version: %v: %s", err, version)
	}
	cliVersion := strings.TrimSpace(string(version))
	t.Logf("CLI: %s", cliVersion)
	realHome, err := codex.ResolveHome()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	copied := 0
	for _, name := range []string{"auth.json", ".credentials.json"} {
		data, readErr := os.ReadFile(filepath.Join(realHome, name))
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(filepath.Join(home, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		copied++
	}
	if copied == 0 {
		t.Skipf("no Codex credentials under %s; run `codex login` first", realHome)
	}
	t.Setenv("CODEX_HOME", home)
	provider := codex.NewProvider("")
	catalog, err := provider.DiscoverModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("%s discover catalog: %v", cliVersion, err)
	}
	provider.SetModelCatalog(catalog)
	registry := llm.NewRegistry()
	registry.Register(provider)
	eligible := registry.EligibleModelsForPhase(llm.PhaseChat)["codex"]
	var models []string
	for _, model := range eligible {
		for _, info := range catalog {
			if info.ID == model && slices.Contains(info.EffortCapabilities, llm.EffortHigh) {
				models = append(models, model)
			}
		}
	}
	if len(models) < 2 {
		t.Skipf("%s catalog has fewer than two chat models", cliVersion)
	}
	firstModel, secondModel := models[0], models[1]
	for _, model := range models {
		if strings.Contains(model, "mini") || strings.Contains(model, "luna") {
			firstModel = model
			break
		}
	}
	if secondModel == firstModel {
		secondModel = models[0]
	}
	t.Logf("models: %s -> %s", firstModel, secondModel)
	h := &supervisorHarness{t: t, runtimeDir: t.TempDir(), claudeConfigDir: t.TempDir(), codexHome: home, harness: "codex", registry: registry, model: firstModel}
	h.stateDir = filepath.Join(h.runtimeDir, "state")
	if err := os.MkdirAll(h.stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.sessions = session.NewManager(nil)
	h.store = feature.NewStore(h.stateDir)
	h.runner = agent.NewPhaseRunner(h.sessions, h.store, h.stateDir)
	h.runner.Registry = registry
	h.runner.Config = config.NewDefault()
	h.runner.SkillsDir = filepath.Join(h.runtimeDir, "skills")
	h.admission = workadmission.New(workadmission.Options{})
	h.start()
	t.Cleanup(func() { h.stopServer(); _ = h.coord.Close(); h.sessions.Shutdown() })
	h.chooseSettings()
	h.send("Reply with just OK.", "live-settings-first")
	first := h.waitLive("first live reply", func(st server.SupervisorState) bool { return st.Lifecycle == server.SupervisorLifecycleIdle })
	if first.LastTurnOutcome != server.SupervisorTurnOutcomeCompleted {
		t.Fatalf("%s first outcome = %s", cliVersion, first.LastTurnOutcome)
	}
	var response server.SupervisorStateResponse
	h.do(http.MethodPatch, "/api/v1/supervisor/settings", map[string]any{
		"model": firstModel, "effort": "high", "request_id": "live-effort-high", "expected_generation": first.Generation,
	}, http.StatusOK, &response)
	if response.State.Settings.Effort != "high" || response.State.Generation != first.Generation {
		t.Fatalf("%s effort update refused or relaunched: %+v", cliVersion, response.State)
	}
	h.do(http.MethodPatch, "/api/v1/supervisor/settings", map[string]any{
		"model": secondModel, "effort": "high", "request_id": "live-model-next", "expected_generation": first.Generation,
	}, http.StatusOK, &response)
	if response.State.Settings.Model != secondModel || response.State.Generation != first.Generation {
		t.Fatalf("%s model update refused or relaunched: %+v", cliVersion, response.State)
	}
	h.send("Reply with just READY.", "live-settings-second")
	second := h.waitLive("second live reply", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.LastTurnOutcome == server.SupervisorTurnOutcomeCompleted && st.HeadSeq > response.State.HeadSeq
	})
	if second.Generation != first.Generation || second.Settings.Model != secondModel || second.EffectiveModel == "" {
		t.Fatalf("%s next turn did not use the changed thread: %+v", cliVersion, second)
	}
	if len(markerRecords(h.transcript(""), server.SupervisorMarkerSettingsChanged)) < 2 {
		t.Fatalf("%s missing confirmed settings markers", cliVersion)
	}
}
