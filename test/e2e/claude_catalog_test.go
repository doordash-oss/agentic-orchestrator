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
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"go.uber.org/fx"
)

func claudeCatalogProvider(t *testing.T, binary string) *claude.Provider {
	t.Helper()
	cfg := config.NewDefault()
	cfg.Providers = map[string]config.ProviderConfig{"claude": {CLI: binary}}
	registry := llm.NewRegistry()
	app := fx.New(fx.Supply(cfg, registry), claude.Module, fx.NopLogger)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	return registry.ByName("claude").(*claude.Provider)
}

func TestClaudeCatalogProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("launches CLI subprocesses")
	}
	for _, scenario := range []string{"success", "rejected", "partial", "exit", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CLAUDECODE", "nested-session")
			response := `{"type":"control_response","response":{"request_id":"agentico-model-catalog","subtype":"success","response":{"models":[{"value":"claude-fable-5-1[1m]","resolvedModel":"claude-fable-5-1","displayName":"Fable","supportsEffort":true,"supportedEffortLevels":["high","max"]},{"value":"haiku"}]}}}`
			switch scenario {
			case "rejected":
				response = `{"type":"control_response","response":{"request_id":"agentico-model-catalog","subtype":"error","error":"secret-value"}}`
			case "partial":
				response = `{"type":"control_response","response":{"request_id":"agentico-model-catalog","subtype":"success","response":{"models":[{"value":"sonnet"},{"displayName":"Fable"}]}}}`
			}
			body := fmt.Sprintf(`
[ -z "${CLAUDECODE+x}" ] || exit 10
printf '%%s\n' "$$" > '%s/pid'
printf '%%s\n' "$@" > '%s/args'
IFS= read -r request || exit 11
printf '%%s\n' "$request" > '%s/input'
`, dir, dir, dir)
			if scenario == "exit" {
				body += "exit 5\n"
			} else if scenario != "canceled" {
				body += "printf '%s\n' '" + response + "'\n"
			}
			// Wait for another message. Discovery must kill/reap this process instead
			// of sending a prompt, closing stdin before the response, or waiting for exit.
			body += fmt.Sprintf("while IFS= read -r extra; do printf '%%s\n' \"$extra\" >> '%s/input'; done\n", dir)
			binary := testutil.WriteFakeClaudeScript(t, body)
			provider := claudeCatalogProvider(t, binary)
			original := []llm.ModelInfo{{ID: "known-fable", Category: "capable"}}
			provider.SetModelCatalog(original)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if scenario == "canceled" {
				// Cancel only after the fake CLI receives initialization. Startup speed
				// varies under the all-package race sweep and is not the contract here.
				watched := make(chan struct{})
				go func() {
					defer close(watched)
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						if input, err := os.ReadFile(filepath.Join(dir, "input")); err == nil && json.Valid(input) {
							cancel()
							return
						}
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
						}
					}
				}()
				t.Cleanup(func() { cancel(); <-watched })
			}
			var reported []llm.ModelInfo
			models, err := provider.DiscoverModelCatalogWithProgress(ctx, func(model llm.ModelInfo) { reported = append(reported, model) })
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if len(models) != 2 || len(reported) != 2 || models[0].ID != "claude-fable-5-1[1M]" {
					t.Fatalf("models=%+v reported=%+v", models, reported)
				}
			} else {
				if err == nil || len(models) != 0 || len(reported) != 0 {
					t.Fatalf("models=%+v reported=%+v err=%v", models, reported, err)
				}
				if scenario == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error=%v", err)
				}
				if strings.Contains(err.Error(), "secret-value") {
					t.Fatal("provider text leaked")
				}
			}
			if provider.ModelCatalog()[0].ID != "known-fable" {
				t.Fatal("discovery mutated catalog before successful publication")
			}
			argsData, err := os.ReadFile(filepath.Join(dir, "args"))
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Fields(string(argsData))
			for _, required := range []string{"-p", "--input-format", "--output-format", "--safe-mode", "--no-session-persistence"} {
				if !slices.Contains(args, required) {
					t.Fatalf("missing %s: %v", required, args)
				}
			}
			if slices.Contains(args, "--model") || slices.Contains(args, "--max-budget-usd") {
				t.Fatalf("inference probe flags remain: %v", args)
			}
			input, err := os.ReadFile(filepath.Join(dir, "input"))
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				Type    string `json:"type"`
				Request struct {
					Subtype string         `json:"subtype"`
					Hooks   map[string]any `json:"hooks"`
				} `json:"request"`
			}
			if err := json.Unmarshal(input, &request); err != nil {
				t.Fatalf("expected one initialization request: %v", err)
			}
			if request.Type != "control_request" || request.Request.Subtype != "initialize" || len(request.Request.Hooks) != 0 {
				t.Fatalf("unexpected request: %+v", request)
			}
			pidData, err := os.ReadFile(filepath.Join(dir, "pid"))
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
			if err != nil {
				t.Fatal(err)
			}
			process, err := os.FindProcess(pid)
			if err == nil && process.Signal(syscall.Signal(0)) == nil {
				t.Fatal("catalog process was not reaped")
			}
		})
	}
}

// This handshake reads CLI metadata without submitting a user prompt.
func TestClaudeCatalogLive(t *testing.T) {
	if testing.Short() || os.Getenv("AGENTIC_CLAUDE_CATALOG_LIVE") != "1" {
		t.Skip("set AGENTIC_CLAUDE_CATALOG_LIVE=1 for installed CLI metadata compatibility")
	}
	provider := claudeCatalogProvider(t, "claude")
	for attempt := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		start := time.Now()
		models, err := provider.DiscoverModelCatalog(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if len(models) == 0 {
			t.Fatal("empty live catalog")
		}
		ids := make([]string, 0, len(models))
		for _, model := range models {
			ids = append(ids, model.ID)
		}
		t.Logf("attempt %d: %v in %s", attempt+1, ids, time.Since(start))
	}
}
