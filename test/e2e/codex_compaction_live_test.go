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
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

func TestCodexCompactionLive(t *testing.T) {
	if testing.Short() || os.Getenv("AGENTIC_CODEX_LIVE") != "1" {
		t.Skip("set AGENTIC_CODEX_LIVE=1 without -short to test native Codex compaction")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatal(err)
	}
	if version, err := exec.Command("codex", "--version").Output(); err == nil {
		t.Logf("Codex CLI: %s", strings.TrimSpace(string(version)))
	}
	realHome, err := codex.ResolveHome()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	copied := 0
	for _, name := range []string{"auth.json", ".credentials.json"} {
		data, err := os.ReadFile(filepath.Join(realHome, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
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
	if err != nil || len(catalog) == 0 {
		t.Fatalf("discover Codex catalog: %v (%d models)", err, len(catalog))
	}
	provider.SetModelCatalog(catalog)
	model := os.Getenv(codexLiveModelEnv)
	if model == "" {
		model = catalog[0].ID
		for _, m := range catalog {
			if strings.Contains(m.ID, "mini") || strings.Contains(m.ID, "luna") {
				model = m.ID
				break
			}
		}
	}
	registry := llm.NewRegistry()
	registry.Register(provider)
	h := &supervisorHarness{t: t, runtimeDir: t.TempDir(), claudeConfigDir: t.TempDir(), codexHome: home, harness: "codex", registry: registry, model: model}
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
	const fact = "For this test, the project nickname is velvet-orbit-731."
	h.send(fact+" Reply with OK.", "fact")
	h.waitLive("first reply", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.LastTurnOutcome == server.SupervisorTurnOutcomeCompleted
	})
	thread := h.coord.State().NativeSessionID
	view := h.sessions.GetSession(h.coord.State().SessionID)
	compactor, ok := view.(interface{ ForceCompactionForTest(context.Context) error })
	if !ok {
		t.Fatal("session lacks test compaction bridge")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := compactor.ForceCompactionForTest(ctx); err != nil {
		t.Fatalf("force native compaction: %v", err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	var checkpoint supervisor.CheckpointData
	for time.Now().Before(deadline) {
		records := h.durableRecords()
		for i, record := range records {
			if record.Kind != supervisor.KindCheckpoint || i+1 >= len(records) || records[i+1].Kind != supervisor.KindMarker {
				continue
			}
			if err := json.Unmarshal(record.Data, &checkpoint); err != nil {
				t.Fatal(err)
			}
			break
		}
		if checkpoint.NativeBaseline != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if checkpoint.NativeBaseline == nil || checkpoint.NativeBaseline.Harness != "codex" || len(checkpoint.NativeBaseline.Payload) == 0 {
		t.Fatalf("native checkpoint = %+v", checkpoint)
	}
	h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
	h.waitLive("stopped", func(st server.SupervisorState) bool { return st.Lifecycle == server.SupervisorLifecycleStopped })
	h.send("What is the project nickname I told you earlier? Reply with just the nickname.", "recall")
	h.waitLive("resumed reply", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.Generation == 2
	})
	page := h.transcript("?limit=500")
	if reply := lastAssistantText(page.Items); !strings.Contains(reply, "velvet-orbit-731") {
		t.Fatalf("resumed reply = %q", reply)
	}
	if h.coord.State().NativeSessionID != thread {
		t.Fatalf("resumed thread = %q, want %q", h.coord.State().NativeSessionID, thread)
	}
	if markers := markerRecords(page, server.SupervisorMarkerHistoryNotRestored); len(markers) != 0 {
		t.Fatalf("history not restored: %+v", markers)
	}
}
