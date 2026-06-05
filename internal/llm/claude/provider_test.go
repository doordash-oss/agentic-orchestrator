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

package claude

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestClaudeProvider_EnvVarsToExclude(t *testing.T) {
	p := &Provider{}
	got := p.EnvVarsToExclude()
	if len(got) != 1 || got[0] != "CLAUDECODE" {
		t.Errorf("EnvVarsToExclude() = %v, want [CLAUDECODE]", got)
	}
}

func TestClaudeProvider_CheckReadinessAuthenticated(t *testing.T) {
	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			assertCommand(t, name, args, "claude", []string{"auth", "status", "--json"})
			return []byte(`{"loggedIn":true,"authMethod":"claude.ai","email":"user@example.com"}`), nil
		},
	}

	status := p.CheckReadiness(context.Background())
	if !status.Ready {
		t.Fatalf("CheckReadiness().Ready = false, detail=%q remedy=%q", status.Detail, status.Remedy)
	}
	if !strings.Contains(status.Detail, "user@example.com") {
		t.Fatalf("CheckReadiness().Detail = %q, want authenticated email", status.Detail)
	}
}

func TestClaudeProvider_CheckReadinessNotAuthenticated(t *testing.T) {
	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			return []byte(`{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`), errors.New("exit status 1")
		},
	}

	status := p.CheckReadiness(context.Background())
	if status.Ready {
		t.Fatal("CheckReadiness().Ready = true, want false")
	}
	if !strings.Contains(status.Detail, "not authenticated") {
		t.Fatalf("CheckReadiness().Detail = %q, want not authenticated", status.Detail)
	}
	if !strings.Contains(status.Remedy, "claude auth login") {
		t.Fatalf("CheckReadiness().Remedy = %q, want claude auth login", status.Remedy)
	}
}

func TestClaudeProvider_CheckReadinessAPIKey(t *testing.T) {
	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			return []byte(`{"loggedIn":true,"authMethod":"api_key","apiKeySource":"ANTHROPIC_API_KEY"}`), nil
		},
	}

	status := p.CheckReadiness(context.Background())
	if !status.Ready {
		t.Fatalf("CheckReadiness().Ready = false, detail=%q remedy=%q", status.Detail, status.Remedy)
	}
	if !strings.Contains(status.Detail, "ANTHROPIC_API_KEY") {
		t.Fatalf("CheckReadiness().Detail = %q, want API key source", status.Detail)
	}
}

func assertCommand(t *testing.T, gotName string, gotArgs []string, wantName string, wantArgs []string) {
	t.Helper()
	if gotName != wantName {
		t.Fatalf("command name = %q, want %q", gotName, wantName)
	}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Fatalf("command args = %v, want %v", gotArgs, wantArgs)
	}
}

func TestParseClaudeModelProbe_ExtractsResolvedModelAndContextWindow(t *testing.T) {
	out := []byte(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-4-8[1m]"}`,
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8[1m]","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":12,"output_tokens":1}}}`,
		`{"type":"result","subtype":"success","session_id":"s1","modelUsage":{"claude-opus-4-8[1m]":{"contextWindow":1000000}}}`,
	}, "\n"))

	model, contextWindow, err := parseClaudeModelProbe("opus[1m]", out)
	if err != nil {
		t.Fatalf("parseClaudeModelProbe() error: %v", err)
	}
	if model != "claude-opus-4-8[1m]" {
		t.Fatalf("model = %q, want claude-opus-4-8[1m]", model)
	}
	if contextWindow != 1_000_000 {
		t.Fatalf("contextWindow = %d, want 1000000", contextWindow)
	}
}

func TestClaudeProviderDiscoverModelCatalog_PartialSuccessUpdatesAliases(t *testing.T) {
	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			if name != "claude" {
				t.Fatalf("command name = %q, want claude", name)
			}
			if len(args) < 2 || args[0] != "--model" {
				t.Fatalf("command args = %v, want --model first", args)
			}
			model := args[1]
			if !slices.Equal(args, claudeModelProbeArgs(model)) {
				t.Fatalf("command args for %s = %v, want %v", model, args, claudeModelProbeArgs(model))
			}
			switch model {
			case "opus":
				return []byte(`{"type":"system","subtype":"init","model":"claude-opus-4-8"}` + "\n" +
					`{"type":"result","subtype":"success","modelUsage":{"claude-opus-4-8":{"contextWindow":200000}}}`), nil
			case "sonnet":
				return []byte(`{"type":"system","subtype":"init","model":"claude-sonnet-4-6"}` + "\n" +
					`{"type":"result","subtype":"success","modelUsage":{"claude-sonnet-4-6":{"contextWindow":200000}}}`), nil
			default:
				return nil, errors.New("model not available")
			}
		},
	}

	models, err := p.DiscoverModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModelCatalog() error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("DiscoverModelCatalog() returned %d models, want 2: %+v", len(models), models)
	}
	if models[0].ID != "opus" || !slices.Equal(models[0].Aliases, []string{"claude-opus-4-8"}) {
		t.Fatalf("first model = %+v, want opus with claude-opus-4-8 alias", models[0])
	}
	if models[1].ID != "sonnet" || !slices.Equal(models[1].Aliases, []string{"claude-sonnet-4-6"}) {
		t.Fatalf("second model = %+v, want sonnet with claude-sonnet-4-6 alias", models[1])
	}
}

func TestClaudeProviderDiscoverModelCatalog_PreservesUnattemptedFallbacksOnTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			if name != "claude" {
				t.Fatalf("command name = %q, want claude", name)
			}
			model := args[1]
			if model != "opus" {
				t.Fatalf("runner should only be called for opus before cancellation, got %s", model)
			}
			cancel()
			return []byte(`{"type":"system","subtype":"init","model":"claude-opus-4-8"}` + "\n" +
				`{"type":"result","subtype":"success","modelUsage":{"claude-opus-4-8":{"contextWindow":1000000}}}`), nil
		},
	}

	models, err := p.DiscoverModelCatalog(ctx)
	if err != nil {
		t.Fatalf("DiscoverModelCatalog() error: %v", err)
	}
	gotIDs := make([]string, 0, len(models))
	for _, model := range models {
		gotIDs = append(gotIDs, model.ID)
	}
	wantIDs := []string{"opus", "opus[1m]", "sonnet", "sonnet[1m]", "haiku"}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("model IDs = %v, want %v", gotIDs, wantIDs)
	}
	if models[0].ContextWindow != 1_000_000 || !slices.Equal(models[0].Aliases, []string{"claude-opus-4-8"}) {
		t.Fatalf("opus entry = %+v, want discovered metadata", models[0])
	}
	if models[len(models)-1].ID != "haiku" || models[len(models)-1].ContextWindow != 200_000 {
		t.Fatalf("last entry = %+v, want fallback haiku", models[len(models)-1])
	}
}

// TestClaudeProvider_DefaultCatalog_Invariants locks in the hardcoded
// fallback catalog used when CLI discovery is unavailable.
func TestClaudeProvider_DefaultCatalog_Invariants(t *testing.T) {
	p := &Provider{}
	cat := p.defaultModelInfos()

	byID := make(map[string]llm.ModelInfo, len(cat))
	for _, m := range cat {
		byID[m.ID] = m
	}

	wantWindows := map[string]int{
		"opus":       200_000,
		"opus[1m]":   1_000_000,
		"sonnet":     200_000,
		"sonnet[1m]": 1_000_000,
		"haiku":      200_000,
	}

	for id, want := range wantWindows {
		m, ok := byID[id]
		if !ok {
			t.Errorf("missing catalog entry for %q", id)
			continue
		}
		if m.ContextWindow != want {
			t.Errorf("%s.ContextWindow = %d, want %d", id, m.ContextWindow, want)
		}
	}

	// The `[1m]` variants must be distinct catalog entries, not aliases of
	// the base ID. This is load-bearing: canonicalModelForProvider rewrites
	// alias matches to the canonical ID, which would silently downgrade
	// `--model opus[1m]` (1M) to `--model opus` (200K).
	for _, m := range cat {
		for _, alias := range m.Aliases {
			if alias == m.ID+"[1m]" {
				t.Errorf("entry %q must not carry %q as an alias; %q must be its own catalog ID", m.ID, alias, alias)
			}
		}
	}

	// The offline fallback must not pin a concrete model version: what an
	// alias resolves to is provider-dependent and drifts, so a hardcoded
	// claude-* version is a frequently-wrong guess. The probe sets it instead.
	for _, m := range cat {
		for _, alias := range m.Aliases {
			if strings.HasPrefix(alias, "claude-") {
				t.Errorf("entry %q pins concrete-version alias %q; offline fallback must use the stable alias only", m.ID, alias)
			}
		}
	}
}

// TestClaudeProvider_ContextWindowForModel_ReturnsHardcodedWithoutSeed
// guards against regressions where ContextWindowForModel stopped falling
// back to defaultModelInfos when SetModelCatalog had not been called.
// Before the fix that path silently returned 0, which made the
// implement-loop 60% handoff ticker dead on fresh sessions.
func TestClaudeProvider_ContextWindowForModel_ReturnsHardcodedWithoutSeed(t *testing.T) {
	p := &Provider{}

	tests := map[string]int{
		"opus":       200_000,
		"opus[1m]":   1_000_000,
		"sonnet[1m]": 1_000_000,
		"haiku":      200_000,
	}
	for model, want := range tests {
		t.Run(model, func(t *testing.T) {
			if got := p.ContextWindowForModel(model); got != want {
				t.Errorf("ContextWindowForModel(%q) = %d, want %d", model, got, want)
			}
		})
	}
}

// TestClaudeProvider_ModelCatalog_FallsBackToHardcoded verifies the same
// fallback on the catalog-exposing API that the TUI and registry depend on.
func TestClaudeProvider_ModelCatalog_FallsBackToHardcoded(t *testing.T) {
	p := &Provider{}
	got := p.ModelCatalog()
	want := p.defaultModelInfos()
	if len(got) != len(want) {
		t.Fatalf("ModelCatalog() len = %d, want %d", len(got), len(want))
	}
}

// TestBuildInteractiveCommand_PermissionMode pins the wiring that prevents
// Claude Code's user-level "auto" defaultMode from silently overriding
// grilling-phase prompts. When the caller passes PermissionMode, the CLI
// receives an explicit `--permission-mode <value>` so the session is not
// launched under auto mode (which injects a "work without stopping for
// clarifying questions" system reminder that suppresses [grill-me]).
func TestBuildInteractiveCommand_PermissionMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want bool
	}{
		{name: "default mode emitted", mode: "default", want: true},
		{name: "plan mode emitted", mode: "plan", want: true},
		{name: "empty mode not emitted", mode: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := buildInteractiveCommand(llm.CommandBuildOpts{
				Model:          "opus",
				PermissionMode: tc.mode,
			})
			hasFlag := false
			for i, a := range args {
				if a == "--permission-mode" {
					hasFlag = true
					if i+1 >= len(args) || args[i+1] != tc.mode {
						t.Errorf("--permission-mode value = %q, want %q", args[i+1], tc.mode)
					}
				}
			}
			if hasFlag != tc.want {
				t.Errorf("--permission-mode present = %v, want %v; args=%v", hasFlag, tc.want, args)
			}
		})
	}
}

// TestBuildInteractiveCommand_PermissionMode_CoexistsWithDSP verifies that
// --dangerously-skip-permissions and --permission-mode can both be emitted on
// the same invocation. Empirically, the Claude CLI accepts both: DSP wins for
// runtime permission handling, but --permission-mode still suppresses the
// auto-mode system-reminder.
func TestBuildInteractiveCommand_PermissionMode_CoexistsWithDSP(t *testing.T) {
	args := buildInteractiveCommand(llm.CommandBuildOpts{
		Model:                "opus",
		DangerouslySkipPerms: true,
		PermissionMode:       "default",
	})
	if !slices.Contains(args, "--dangerously-skip-permissions") {
		t.Errorf("missing --dangerously-skip-permissions:\n%v", args)
	}
	if !slices.Contains(args, "--permission-mode") {
		t.Errorf("missing --permission-mode:\n%v", args)
	}
}
