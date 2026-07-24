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
	"time"

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

func TestClaudeProvider_CheckBareAuthOAuthRejected(t *testing.T) {
	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			return []byte(`{"loggedIn":true,"authMethod":"claude.ai","email":"user@example.com"}`), nil
		},
	}
	p.CheckReadiness(context.Background())
	if p.CheckBareAuth() {
		t.Fatal("CheckBareAuth() = true for OAuth, want false (--bare skips OAuth)")
	}
}

func TestClaudeProvider_CheckBareAuthAPIKeyAccepted(t *testing.T) {
	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			return []byte(`{"loggedIn":true,"authMethod":"api_key","apiKeySource":"ANTHROPIC_API_KEY"}`), nil
		},
	}
	p.CheckReadiness(context.Background())
	if !p.CheckBareAuth() {
		t.Fatal("CheckBareAuth() = false for API key (non-keychain), want true")
	}
}

func TestClaudeProvider_CheckBareAuthKeychainRejected(t *testing.T) {
	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			return []byte(`{"loggedIn":true,"authMethod":"api_key","apiKeySource":"macOS Keychain"}`), nil
		},
	}
	p.CheckReadiness(context.Background())
	if p.CheckBareAuth() {
		t.Fatal("CheckBareAuth() = true for keychain-stored API key, want false (--bare skips keychain)")
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

func TestParseClaudeModelProbe_UsesResolvedModelContextWindow(t *testing.T) {
	out := []byte(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"s1","model":"claude-fable-5"}`,
		`{"type":"assistant","message":{"role":"assistant","model":"claude-fable-5","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":12,"output_tokens":1}}}`,
		`{"type":"result","subtype":"success","session_id":"s1","modelUsage":{"claude-haiku-4-5-20251001":{"contextWindow":200000},"claude-fable-5":{"contextWindow":1000000}}}`,
	}, "\n"))

	model, contextWindow, err := parseClaudeModelProbe("fable", out)
	if err != nil {
		t.Fatalf("parseClaudeModelProbe() error: %v", err)
	}
	if model != "claude-fable-5" {
		t.Fatalf("model = %q, want claude-fable-5", model)
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
			case "fable":
				return []byte(`{"type":"system","subtype":"init","model":"claude-fable-5"}` + "\n" +
					`{"type":"result","subtype":"success","modelUsage":{"claude-fable-5":{"contextWindow":1000000}}}`), nil
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
	if len(models) != 3 {
		t.Fatalf("DiscoverModelCatalog() returned %d models, want 3: %+v", len(models), models)
	}
	if models[0].ID != "fable[1M]" || !slices.Equal(models[0].Aliases, []string{"fable", "claude-fable-5"}) {
		t.Fatalf("first model = %+v, want fable[1M] with stable and concrete aliases", models[0])
	}
	if models[1].ID != "opus[200K]" || !slices.Equal(models[1].Aliases, []string{"opus", "claude-opus-4-8"}) {
		t.Fatalf("second model = %+v, want opus[200K] with stable and concrete aliases", models[1])
	}
	if models[2].ID != "sonnet[200K]" || !slices.Equal(models[2].Aliases, []string{"sonnet", "claude-sonnet-4-6"}) {
		t.Fatalf("third model = %+v, want sonnet[200K] with stable and concrete aliases", models[2])
	}
}

func TestClaudeProviderDiscoverModelCatalog_RetriesTransientProbeFailure(t *testing.T) {
	var fableCalls int
	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			if name != "claude" {
				t.Fatalf("command name = %q, want claude", name)
			}
			switch model := args[1]; model {
			case "fable":
				fableCalls++
				if fableCalls == 1 {
					return nil, errors.New("transient unavailable")
				}
				return []byte(`{"type":"system","subtype":"init","model":"claude-fable-5"}` + "\n" +
					`{"type":"result","subtype":"success","modelUsage":{"claude-fable-5":{"contextWindow":1000000}}}`), nil
			default:
				return nil, errors.New("model not available")
			}
		},
	}

	models, err := p.DiscoverModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModelCatalog() error: %v", err)
	}
	if fableCalls != 2 {
		t.Fatalf("fable probe calls = %d, want 2", fableCalls)
	}
	if len(models) != 1 || models[0].ID != "fable[1M]" {
		t.Fatalf("models = %+v, want only fable[1M]", models)
	}
}

func TestClaudeProviderDiscoverModelCatalog_ProbesCandidatesConcurrently(t *testing.T) {
	candidates := claudeModelProbeCandidates()
	started := make(chan string, len(candidates)*claudeModelProbeAttempts)
	release := make(chan struct{})
	closeRelease := func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	defer closeRelease()

	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			if name != "claude" {
				t.Fatalf("command name = %q, want claude", name)
			}
			model := args[1]
			started <- model
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if model == "fable" {
				return []byte(`{"type":"system","subtype":"init","model":"claude-fable-5"}` + "\n" +
					`{"type":"result","subtype":"success","modelUsage":{"claude-fable-5":{"contextWindow":1000000}}}`), nil
			}
			return nil, errors.New("model not available")
		},
	}

	done := make(chan error, 1)
	go func() {
		models, err := p.DiscoverModelCatalog(context.Background())
		if err == nil && (len(models) != 1 || models[0].ID != "fable[1M]") {
			err = errors.New("expected only fable[1M] after releasing blocked probes")
		}
		done <- err
	}()

	seen := make(map[string]bool, len(candidates))
	for len(seen) < len(candidates) {
		select {
		case model := <-started:
			seen[model] = true
		case <-time.After(250 * time.Millisecond):
			closeRelease()
			<-done
			t.Fatalf("Claude probes did not start concurrently; started = %v", seen)
		}
	}

	closeRelease()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("DiscoverModelCatalog did not return after releasing probes")
	}
}

func TestClaudeProviderDiscoverModelCatalogWithProgress_ReportsBeforeReturn(t *testing.T) {
	release := make(chan struct{})
	closeRelease := func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	defer closeRelease()

	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			if name != "claude" {
				t.Fatalf("command name = %q, want claude", name)
			}
			model := args[1]
			if model == "fable" {
				return []byte(`{"type":"system","subtype":"init","model":"claude-fable-5"}` + "\n" +
					`{"type":"result","subtype":"success","modelUsage":{"claude-fable-5":{"contextWindow":1000000}}}`), nil
			}
			select {
			case <-release:
				return nil, errors.New("model not available")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	reported := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := p.DiscoverModelCatalogWithProgress(context.Background(), func(model llm.ModelInfo) {
			reported <- model.ID
		})
		done <- err
	}()

	select {
	case id := <-reported:
		if id != "fable[1M]" {
			t.Fatalf("reported model = %q, want fable[1M]", id)
		}
	case <-time.After(250 * time.Millisecond):
		closeRelease()
		<-done
		t.Fatal("expected progress report before blocked probes returned")
	}

	select {
	case err := <-done:
		closeRelease()
		if err != nil {
			t.Fatal(err)
		}
		t.Fatal("DiscoverModelCatalogWithProgress returned before blocked probes were released")
	default:
	}

	closeRelease()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("DiscoverModelCatalogWithProgress did not return after releasing probes")
	}
}

func TestClaudeProviderDiscoverModelCatalog_PreservesCanceledFallbacksOnTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			if name != "claude" {
				t.Fatalf("command name = %q, want claude", name)
			}
			model := args[1]
			if model != "fable" {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			cancel()
			return []byte(`{"type":"system","subtype":"init","model":"claude-fable-5"}` + "\n" +
				`{"type":"result","subtype":"success","modelUsage":{"claude-fable-5":{"contextWindow":1000000}}}`), nil
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
	wantIDs := []string{"fable[1M]", "opus[200K]", "opus[1M]", "sonnet[200K]", "sonnet[1M]", "haiku[200K]"}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("model IDs = %v, want %v", gotIDs, wantIDs)
	}
	if models[0].ContextWindow != 1_000_000 || !slices.Equal(models[0].Aliases, []string{"fable", "claude-fable-5"}) {
		t.Fatalf("fable entry = %+v, want discovered metadata", models[0])
	}
	if models[len(models)-1].ID != "haiku[200K]" || models[len(models)-1].ContextWindow != 200_000 {
		t.Fatalf("last entry = %+v, want fallback haiku", models[len(models)-1])
	}
}

func TestClaudeProviderDiscoverModelCatalog_DerivesIDFromDiscoveredContextWindow(t *testing.T) {
	p := &Provider{
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			if name != "claude" {
				t.Fatalf("command name = %q, want claude", name)
			}
			switch model := args[1]; model {
			case "opus":
				return []byte(`{"type":"system","subtype":"init","model":"claude-opus-4-8"}` + "\n" +
					`{"type":"result","subtype":"success","modelUsage":{"claude-opus-4-8":{"contextWindow":1000000}}}`), nil
			default:
				return nil, errors.New("model not available")
			}
		},
	}

	models, err := p.DiscoverModelCatalog(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModelCatalog() error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("DiscoverModelCatalog() returned %d models, want 1: %+v", len(models), models)
	}
	if models[0].ID != "opus[1M]" {
		t.Fatalf("model ID = %q, want opus[1M]", models[0].ID)
	}
	if !slices.Equal(models[0].Aliases, []string{"opus", "claude-opus-4-8"}) {
		t.Fatalf("aliases = %v, want [opus claude-opus-4-8]", models[0].Aliases)
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
		"fable[1M]":    1_000_000,
		"opus[200K]":   200_000,
		"opus[1M]":     1_000_000,
		"sonnet[200K]": 200_000,
		"sonnet[1M]":   1_000_000,
		"haiku[200K]":  200_000,
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

	for _, baseID := range []string{"opus", "sonnet"} {
		baseEntry := byID[baseID+"[200K]"]
		for _, alias := range baseEntry.Aliases {
			if strings.EqualFold(alias, baseID+"[1M]") || strings.EqualFold(alias, baseID+"[1m]") {
				t.Errorf("entry %q must not carry 1M alias %q; 1M must stay its own catalog ID", baseEntry.ID, alias)
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
		"fable":        1_000_000,
		"fable[1M]":    1_000_000,
		"opus":         200_000,
		"opus[200K]":   200_000,
		"opus[1m]":     1_000_000,
		"opus[1M]":     1_000_000,
		"sonnet":       200_000,
		"sonnet[1m]":   1_000_000,
		"sonnet[1M]":   1_000_000,
		"sonnet[200K]": 200_000,
		"haiku[200K]":  200_000,
		"haiku":        200_000,
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

func TestProviderBuildCommand_UsesCatalogAliasForClaudeCLIModel(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"fable[1M]", "fable"},
		{"opus[200K]", "opus"},
		{"opus[1M]", "opus[1m]"},
		{"sonnet[200K]", "sonnet"},
		{"sonnet[1M]", "sonnet[1m]"},
		{"haiku[200K]", "haiku"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			p := &Provider{}
			args, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: tt.model})
			if err != nil {
				t.Fatalf("BuildCommand() error: %v", err)
			}
			if env != nil {
				t.Fatalf("BuildCommand() env = %v, want nil", env)
			}
			assertModelArg(t, args, tt.want)
		})
	}
}

func TestProviderBuildCommand_UsesDiscoveredCatalogAliasForClaudeCLIModel(t *testing.T) {
	p := &Provider{}
	p.SetModelCatalog([]llm.ModelInfo{
		{
			ID:            "experimental[1M]",
			DisplayName:   "Experimental (1M)",
			ContextWindow: 1_000_000,
			Aliases:       []string{"experimental[1m]", "claude-experimental-1"},
			Category:      "capable",
		},
	})

	args, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: "experimental[1M]"})
	if err != nil {
		t.Fatalf("BuildCommand() error: %v", err)
	}
	if env != nil {
		t.Fatalf("BuildCommand() env = %v, want nil", env)
	}
	assertModelArg(t, args, "experimental[1m]")
}

func assertModelArg(t *testing.T, args []string, want string) {
	t.Helper()
	for i, arg := range args {
		if arg != "--model" {
			continue
		}
		if i+1 >= len(args) {
			t.Fatalf("command args = %v, want --model value", args)
		}
		if args[i+1] != want {
			t.Fatalf("--model = %q, want %q; args=%v", args[i+1], want, args)
		}
		return
	}
	t.Fatalf("command args = %v, want --model", args)
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
			args := buildInteractiveCommand(defaultBinary, llm.CommandBuildOpts{
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
	args := buildInteractiveCommand(defaultBinary, llm.CommandBuildOpts{
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

// TestBuildInteractiveCommand_IsolationFlags is a table-driven test covering
// the hidden reviewer's launch-option isolation contract: ZeroTools emits
// --tools "" (and suppresses --disallowedTools), NoSessionPersistence emits
// --no-session-persistence, NoCustomization emits --bare, and all three can
// coexist on the same invocation.
func TestBuildInteractiveCommand_IsolationFlags(t *testing.T) {
	cases := []struct {
		name    string
		opts    llm.CommandBuildOpts
		wantFlags []string
		wantEmptyTools bool
		wantNoDisallowed bool
	}{
		{
			name:             "zero_tools",
			opts:             llm.CommandBuildOpts{Model: "haiku", ZeroTools: true, DisallowedTools: []string{"Bash", "Read"}},
			wantEmptyTools:   true,
			wantNoDisallowed: true,
		},
		{
			name:      "no_session_persistence",
			opts:      llm.CommandBuildOpts{Model: "haiku", NoSessionPersistence: true},
			wantFlags: []string{"--no-session-persistence"},
		},
		{
			name:           "no_customization",
			opts:           llm.CommandBuildOpts{Model: "haiku", NoCustomization: true},
			wantFlags:      []string{"--bare"},
		},
		{
			name:           "zero_tools_and_no_session_persistence",
			opts:           llm.CommandBuildOpts{Model: "haiku", ZeroTools: true, NoSessionPersistence: true},
			wantFlags:      []string{"--no-session-persistence"},
			wantEmptyTools: true,
		},
		{
			name:           "full_isolation",
			opts:           llm.CommandBuildOpts{Model: "haiku", ZeroTools: true, NoSessionPersistence: true, NoCustomization: true},
			wantFlags:      []string{"--no-session-persistence", "--bare"},
			wantEmptyTools: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := buildInteractiveCommand(defaultBinary, tc.opts)
			for _, flag := range tc.wantFlags {
				if !slices.Contains(args, flag) {
					t.Errorf("missing %s: %v", flag, args)
				}
			}
			if tc.wantEmptyTools {
				hasEmptyTools := false
				for i, a := range args {
					if a == "--tools" && i+1 < len(args) && args[i+1] == "" {
						hasEmptyTools = true
					}
				}
				if !hasEmptyTools {
					t.Errorf("missing --tools \"\": %v", args)
				}
			}
			if tc.wantNoDisallowed {
				if slices.Contains(args, "--disallowedTools") {
					t.Errorf("must not emit --disallowedTools when ZeroTools=true: %v", args)
				}
			}
		})
	}
}

func TestClaudeProvider_CLIBinaryDefault(t *testing.T) {
	if got := (&Provider{}).cliBinary(); got != "claude" {
		t.Errorf("cliBinary() = %q, want claude", got)
	}
}

func TestClaudeProvider_BuildCommandUsesCustomBinary(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		args, _, err := (&Provider{}).BuildCommand(llm.CommandBuildOpts{Model: "opus"})
		if err != nil {
			t.Fatalf("BuildCommand() error: %v", err)
		}
		if len(args) == 0 || args[0] != "claude" {
			t.Errorf("args[0] = %q, want claude; args=%v", firstArg(args), args)
		}
	})
	t.Run("override", func(t *testing.T) {
		p := &Provider{binary: "fcc-claude"}
		args, _, err := p.BuildCommand(llm.CommandBuildOpts{Model: "opus"})
		if err != nil {
			t.Fatalf("BuildCommand() error: %v", err)
		}
		if len(args) == 0 || args[0] != "fcc-claude" {
			t.Errorf("args[0] = %q, want fcc-claude; args=%v", firstArg(args), args)
		}
	})
}

func TestClaudeProvider_CheckReadinessUsesCustomBinary(t *testing.T) {
	var gotName string
	p := &Provider{
		binary: "fcc-claude",
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			gotName = name
			return []byte(`{"loggedIn":true,"authMethod":"claude.ai","email":"user@example.com"}`), nil
		},
	}
	if status := p.CheckReadiness(context.Background()); !status.Ready {
		t.Fatalf("CheckReadiness().Ready = false, detail=%q", status.Detail)
	}
	if gotName != "fcc-claude" {
		t.Errorf("CheckReadiness probed %q, want fcc-claude", gotName)
	}
}

func TestClaudeProvider_CLIVersionUsesCustomBinary(t *testing.T) {
	var gotName string
	p := &Provider{
		binary: "fcc-claude",
		runner: func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
			gotName = name
			return []byte("2.1.81 (Claude Code)"), nil
		},
	}
	if _, err := p.CLIVersion(); err != nil {
		t.Fatalf("CLIVersion() error: %v", err)
	}
	if gotName != "fcc-claude" {
		t.Errorf("CLIVersion probed %q, want fcc-claude", gotName)
	}
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func TestProviderSupportsSessionResume(t *testing.T) {
	p := &Provider{}
	if !p.SupportsSessionResume() {
		t.Error("SupportsSessionResume() = false, want true")
	}
}
