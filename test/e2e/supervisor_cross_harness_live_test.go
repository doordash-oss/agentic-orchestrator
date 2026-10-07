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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

// TestSupervisorCrossHarnessLive exercises foreign history with installed CLIs.
// Every pair is independent so a missing gate or credential skips only that pair.
func TestSupervisorCrossHarnessLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live inference is excluded from the short suite")
	}
	harnesses := []string{"claude", "codex", "opencode"}
	gates := map[string]string{
		"claude":   "AGENTIC_CLAUDE_LIVE",
		"codex":    "AGENTIC_CODEX_LIVE",
		"opencode": "AGENTIC_OPENCODE_LIVE",
	}
	anyGate := false
	for _, gate := range gates {
		anyGate = anyGate || os.Getenv(gate) == "1"
	}
	if !anyGate {
		t.Skip("set at least two live harness gates to run cross-harness pairs")
	}
	for _, source := range harnesses {
		for _, destination := range harnesses {
			if source == destination {
				continue
			}
			t.Run(source+"_to_"+destination, func(t *testing.T) {
				for _, name := range []string{source, destination} {
					if os.Getenv(gates[name]) != "1" {
						t.Skipf("%s gate %s is unset", name, gates[name])
					}
				}
				registry := llm.NewRegistry()
				versions := make([]string, 0, 2)
				models := make(map[string]string)
				codexHome := ""
				for _, name := range []string{source, destination} {
					bin, err := exec.LookPath(name)
					if err != nil {
						t.Skipf("%s CLI is unavailable: %v", name, err)
					}
					out, err := exec.Command(bin, "--version").CombinedOutput()
					if err != nil {
						t.Skipf("%s version probe failed: %v", name, err)
					}
					versions = append(versions, name+" "+strings.TrimSpace(string(out)))
					switch name {
					case "claude":
						token, ok := claudeLiveToken(t)
						if !ok {
							t.Skip("Claude OAuth token is unavailable")
						}
						provider := claudeCatalogProvider(t, bin)
						registry.Register(tokenClaudeProvider{Provider: provider, token: token})
					case "codex":
						realHome, err := codex.ResolveHome()
						if err != nil {
							t.Skipf("Codex home is unavailable: %v", err)
						}
						home := t.TempDir()
						copied := 0
						for _, filename := range []string{"auth.json", ".credentials.json"} {
							data, err := os.ReadFile(filepath.Join(realHome, filename))
							if errors.Is(err, os.ErrNotExist) {
								continue
							}
							if err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(filepath.Join(home, filename), data, 0o600); err != nil {
								t.Fatal(err)
							}
							copied++
						}
						if copied == 0 {
							t.Skip("Codex credentials are unavailable")
						}
						t.Setenv("CODEX_HOME", home)
						codexHome = home
						provider := codex.NewProvider(bin)
						catalog, err := provider.DiscoverModelCatalog(context.Background())
						if err != nil || len(catalog) == 0 {
							t.Skipf("Codex model discovery unavailable: %v", err)
						}
						provider.SetModelCatalog(catalog)
						registry.Register(provider)
					case "opencode":
						box := newOpenCodeSandbox(t, true)
						home, err := os.UserHomeDir()
						if err != nil {
							t.Skipf("OpenCode home unavailable: %v", err)
						}
						auth := filepath.Join(xdgDir("XDG_DATA_HOME", filepath.Join(home, ".local", "share")), "opencode", "auth.json")
						if _, err := os.Stat(auth); err != nil {
							t.Skip("OpenCode credentials are unavailable")
						}
						model := openCodeLiveModel(t, bin, box.env)
						for _, kv := range box.env {
							key, value, ok := strings.Cut(kv, "=")
							if ok && strings.HasPrefix(key, "XDG_") {
								t.Setenv(key, value)
							}
						}
						provider := opencode.NewWithBinary(bin)
						provider.SetModelCatalog([]llm.ModelInfo{{ID: model, DisplayName: model, ContextWindow: 200000}})
						registry.Register(provider)
					}
				}
				t.Cleanup(func() {
					if t.Failed() {
						t.Logf("cross-harness pair %s -> %s failed; CLI versions: %s", source, destination, strings.Join(versions, ", "))
					}
				})
				for _, name := range []string{source, destination} {
					eligible := registry.EligibleModelsForPhase(llm.PhaseChat)[name]
					if len(eligible) == 0 {
						t.Skipf("%s has no chat-eligible model", name)
					}
					models[name] = eligible[0]
					if name == source && name == "claude" {
						for _, model := range eligible {
							if strings.Contains(strings.ToLower(model), "haiku") {
								models[name] = model
								break
							}
						}
					}
				}
				h := newHarnessBase(t, source)
				if codexHome != "" {
					h.codexHome = codexHome
					t.Setenv("CODEX_HOME", codexHome)
				}
				h.registry = registry
				h.init(func(o *supervisor.Options) { o.HandshakeTimeout = 2 * time.Minute })
				h.model = models[source]
				h.chooseSettings()
				var nonce [4]byte
				_, _ = rand.Read(nonce[:])
				codeword := "agate-" + hex.EncodeToString(nonce[:])
				fact := "731" + hex.EncodeToString(nonce[:])
				h.send("Remember this codeword: "+codeword+". Reply OK.", "cross-live-1")
				h.waitLive("source first reply", func(st server.SupervisorState) bool { return st.Lifecycle == server.SupervisorLifecycleIdle })
				h.send("The later number is "+fact+". Reply OK.", "cross-live-2")
				h.waitLive("source second reply", func(st server.SupervisorState) bool { return st.Lifecycle == server.SupervisorLifecycleIdle })
				var response server.SupervisorStateResponse
				h.do(http.MethodPatch, "/api/v1/supervisor/settings", map[string]any{
					"harness": destination, "request_id": "cross-live-switch",
					"expected_generation": h.state().Generation,
				}, http.StatusOK, &response)
				if response.State.Settings.Harness != destination || response.State.Settings.Model != models[destination] || response.State.Settings.Effort != "" {
					t.Fatalf("destination defaults = %+v", response.State.Settings)
				}
				h.send("What codeword did I ask you to remember in my first message? Answer with that codeword only.", "cross-live-3")
				h.waitLive("destination codeword reply", func(st server.SupervisorState) bool { return st.Lifecycle == server.SupervisorLifecycleIdle })
				if reply := lastAssistantText(h.transcript("").Items); !strings.Contains(reply, codeword) {
					t.Fatalf("destination codeword reply = %q, want %s", reply, codeword)
				}
				h.send("What later number did I give in my second message before the harness switch? Answer with the number only.", "cross-live-4")
				h.waitLive("destination later-fact reply", func(st server.SupervisorState) bool { return st.Lifecycle == server.SupervisorLifecycleIdle })
				if reply := lastAssistantText(h.transcript("").Items); !strings.Contains(reply, fact) {
					t.Fatalf("destination later-fact reply = %q, want %s", reply, fact)
				}
				if got := markerRecords(h.transcript(""), server.SupervisorMarkerHistoryNotRestored); len(got) != 0 {
					t.Fatalf("destination launched without restored history: %+v", got)
				}
			})
		}
	}
}
