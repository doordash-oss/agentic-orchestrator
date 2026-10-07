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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor/codexsession"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// codexLiveModelEnv overrides the cheap model the live resume test uses.
const codexLiveModelEnv = "AGENTIC_CODEX_RESUME_MODEL"

// TestCodexResumeLive proves the installed Codex CLI resumes a thread whose
// rollout was rebuilt from the durable transcript: a first generation is told
// a fact and ends; the second rebuilds the rollout under the adopted thread
// id, resumes it, and answers from that history. All Codex state lives under
// a temporary CODEX_HOME holding a copy of the user's credentials.
func TestCodexResumeLive(t *testing.T) {
	if testing.Short() || os.Getenv("AGENTIC_CODEX_LIVE") != "1" {
		t.Skip("set AGENTIC_CODEX_LIVE=1 without -short to resume a live Codex against a rebuilt rollout")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatal(err)
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

	// Which models an account may use varies, so the default comes from the
	// installed CLI's own catalog: the smallest window of a light model when
	// one is listed, else the first entry.
	provider := codex.NewProvider("")
	catalog, err := provider.DiscoverModelCatalog(context.Background())
	if err != nil || len(catalog) == 0 {
		t.Fatalf("discover the Codex model catalog: %v (%d models)", err, len(catalog))
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
	t.Logf("model: %s", model)
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
	t.Cleanup(func() {
		h.stopServer()
		_ = h.coord.Close()
		h.sessions.Shutdown()
	})
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		logs, _ := filepath.Glob(filepath.Join(h.stateDir, "supervisor", "conversations", "*", "generations", "*", "*"))
		for _, path := range logs {
			if strings.HasSuffix(path, "output.txt") || strings.HasSuffix(path, "stderr.log") {
				if data, err := os.ReadFile(path); err == nil {
					t.Logf("%s:\n%s", path, tail(data, 6000))
				}
			}
		}
	})

	var nonce [4]byte
	_, _ = rand.Read(nonce[:])
	codeword := "periwinkle-" + hex.EncodeToString(nonce[:])
	h.chooseSettings()
	h.send("Remember this for later: the secret codeword is "+codeword+". Reply with just OK.", "live-1")
	first := h.waitLive("first reply", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.LastTurnOutcome != server.SupervisorTurnOutcomeNone
	})
	if first.LastTurnOutcome != server.SupervisorTurnOutcomeCompleted {
		t.Fatalf("first turn outcome = %s", first.LastTurnOutcome)
	}
	adopted := h.coord.State().NativeSessionID
	if adopted == "" || initSessionID(t, h.generationDir(first)) != adopted {
		t.Fatalf("adopted native id %q, first generation thread %q", adopted, initSessionID(t, h.generationDir(first)))
	}
	h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
	h.waitLive("stopped", func(st server.SupervisorState) bool { return st.Lifecycle == server.SupervisorLifecycleStopped })

	h.send("What is the secret codeword I told you earlier? Reply with just the codeword.", "live-2")
	st := h.waitLive("resumed reply", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.Generation == 2
	})
	if st.LastTurnOutcome != server.SupervisorTurnOutcomeCompleted {
		t.Fatalf("resumed turn outcome = %s", st.LastTurnOutcome)
	}
	page := h.transcript("")
	reply := ""
	for _, rec := range page.Items {
		if rec.Kind == server.SupervisorRecordKindAssistant && rec.Generation == st.Generation {
			reply += recordText(rec)
		}
	}
	if !strings.Contains(reply, codeword) {
		t.Fatalf("resumed reply %q does not contain the remembered codeword %q", reply, codeword)
	}
	// A fresh thread/start on the resumed generation would report a new
	// thread id and the history_not_restored fallback marker.
	if got := initSessionID(t, h.generationDir(st)); got != adopted {
		t.Fatalf("resumed generation runs thread %q, want the adopted %q", got, adopted)
	}
	if got := h.coord.State().NativeSessionID; got != adopted {
		t.Fatalf("native id after resume = %q, want %q", got, adopted)
	}
	if markers := markerRecords(page, server.SupervisorMarkerHistoryNotRestored); len(markers) != 0 {
		t.Fatalf("the resume fell back to a fresh thread: %+v", markers[0].Marker)
	}
	if _, found, err := codexsession.FindRollout(home, adopted); err != nil || !found {
		t.Fatalf("rebuilt rollout under the temporary home: found=%v err=%v", found, err)
	}
	if _, found, _ := codexsession.FindRollout(realHome, adopted); found {
		t.Fatalf("a rollout for %s was written under the real Codex home %s", adopted, realHome)
	}
}

func tail(data []byte, n int) string {
	if len(data) > n {
		data = data[len(data)-n:]
	}
	return string(data)
}
