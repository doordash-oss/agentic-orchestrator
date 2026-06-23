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

package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestProviderName(t *testing.T) {
	if got := New().Name(); got != "opencode" {
		t.Fatalf("Name() = %q, want opencode", got)
	}
}

func TestMatchesModel_ExplicitPrefixOnly(t *testing.T) {
	p := New()
	matchCases := []string{
		"opencode:anthropic/claude-sonnet-4-5",
		"opencode:openai/gpt-5",
	}
	for _, m := range matchCases {
		if !p.MatchesModel(m) {
			t.Errorf("MatchesModel(%q) = false, want true", m)
		}
	}

	// Bare model strings must never resolve to OpenCode during Phase 1, so the
	// provider cannot be selected by automatic defaults or generic lists. The
	// bare routing prefix with no backend model is not a valid selection either,
	// so it must not match.
	noMatchCases := []string{
		"sonnet",
		"gpt-5.4",
		"anthropic/claude-sonnet-4-5",
		"openai:gpt-5",
		"",
		" opencode:foo", // leading space is not the routing prefix
		"opencode:",     // routing prefix with no backend model
		"opencode:   ",  // routing prefix with whitespace-only backend
	}
	for _, m := range noMatchCases {
		if p.MatchesModel(m) {
			t.Errorf("MatchesModel(%q) = true, want false", m)
		}
	}
}

func TestAvailableModels_EmptyForExplicitOnly(t *testing.T) {
	if got := New().AvailableModels(); got != nil {
		t.Fatalf("AvailableModels() = %v, want nil (explicit-only provider)", got)
	}
}

func TestBackendModel_StripsRoutingPrefixOnce(t *testing.T) {
	cases := map[string]string{
		"opencode:anthropic/claude-sonnet-4-5": "anthropic/claude-sonnet-4-5",
		"opencode:openai/gpt-5-codex":          "openai/gpt-5-codex",
		"anthropic/claude-sonnet-4-5":          "anthropic/claude-sonnet-4-5",
		// Only one prefix is stripped; a backend provider literally named
		// "opencode" in slash form is preserved.
		"opencode:opencode/local-model": "opencode/local-model",
		"  opencode:x/y  ":              "x/y",
	}
	for in, want := range cases {
		if got := BackendModel(in); got != want {
			t.Errorf("BackendModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildCommand_ACPStdioWithModelEnv(t *testing.T) {
	p := New()
	cmd, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: "anthropic/claude-sonnet-4-5"})
	if err != nil {
		t.Fatalf("BuildCommand() error: %v", err)
	}
	if !slices.Equal(cmd, []string{"opencode", "acp"}) {
		t.Fatalf("BuildCommand() cmd = %v, want [opencode acp]", cmd)
	}

	got := configContentValue(t, env)
	if model := configModel(t, got); model != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("config content model = %q, want anthropic/claude-sonnet-4-5", model)
	}
}

func TestBuildCommand_StripsRoutingPrefixBeforeModelEnv(t *testing.T) {
	// Defensive: even if the routing-prefixed form reaches BuildCommand, the
	// prefix is stripped exactly once before being handed to OpenCode.
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: "opencode:openai/gpt-5"})
	if err != nil {
		t.Fatalf("BuildCommand() error: %v", err)
	}
	if model := configModel(t, configContentValue(t, env)); model != "openai/gpt-5" {
		t.Fatalf("config content model = %q, want openai/gpt-5", model)
	}
}

// TestBuildCommand_RejectsInvalidBackendModels proves the provider boundary
// fails closed before command construction for selections that are empty, look
// like CLI flags, or carry shell/interpolation metacharacters. None of these
// may yield a launchable `opencode acp` command, and an empty backend must not
// silently fall back to OpenCode's default model.
func TestBuildCommand_RejectsInvalidBackendModels(t *testing.T) {
	p := New()
	cases := []struct {
		name  string
		model string
	}{
		{"empty selection", ""},
		{"routing prefix only", "opencode:"},
		{"routing prefix with whitespace backend", "opencode:   "},
		{"flag-shaped bare", "--dangerously-skip"},
		{"flag-shaped behind prefix", "opencode:--dangerously-skip"},
		{"command substitution", "anthropic/$(whoami)"},
		{"variable interpolation", "anthropic/${HOME}"},
		{"backtick", "anthropic/`id`"},
		{"semicolon chain", "anthropic/claude;rm -rf /"},
		{"pipe", "anthropic/claude|cat"},
		{"ampersand", "anthropic/claude&"},
		{"redirect", "anthropic/claude>out"},
		{"embedded space", "anthropic/claude sonnet"},
		{"embedded newline", "anthropic/cla\nude"},
		{"double quote", "anthropic/\"claude\""},
		{"bracket suffix", "anthropic/claude-sonnet-4-5[200K]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: tc.model})
			if err == nil {
				t.Fatalf("BuildCommand(%q) = (%v, %v, nil), want rejection before command construction", tc.model, cmd, env)
			}
			if cmd != nil || env != nil {
				t.Fatalf("BuildCommand(%q) returned cmd=%v env=%v on rejection, want nil/nil", tc.model, cmd, env)
			}
		})
	}
}

// TestBuildCommand_PreservesValidSlashFormModels proves valid backend
// "provider/model" ids — including the routing-prefixed form, surrounding
// whitespace, and OpenCode-meaningful suffixes — survive validation and reach
// OPENCODE_CONFIG_CONTENT unchanged after the prefix is stripped exactly once.
func TestBuildCommand_PreservesValidSlashFormModels(t *testing.T) {
	p := New()
	cases := map[string]string{
		"anthropic/claude-sonnet-4-5":          "anthropic/claude-sonnet-4-5",
		"openai/gpt-5-codex":                   "openai/gpt-5-codex",
		"opencode:openai/gpt-5":                "openai/gpt-5",
		"opencode:anthropic/claude-sonnet-4-5": "anthropic/claude-sonnet-4-5",
		"ollama/llama3.1:8b":                   "ollama/llama3.1:8b",
		"  opencode:x/y  ":                     "x/y",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			cmd, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: in})
			if err != nil {
				t.Fatalf("BuildCommand(%q) error: %v", in, err)
			}
			if !slices.Equal(cmd, []string{"opencode", "acp"}) {
				t.Fatalf("BuildCommand(%q) cmd = %v, want [opencode acp]", in, cmd)
			}
			if model := configModel(t, configContentValue(t, env)); model != want {
				t.Fatalf("BuildCommand(%q) backend model = %q, want %q", in, model, want)
			}
		})
	}
}

func configContentValue(t *testing.T, env []string) string {
	t.Helper()
	for _, e := range env {
		if after, ok := strings.CutPrefix(e, configContentEnvVar+"="); ok {
			return after
		}
	}
	t.Fatalf("env %v missing %s", env, configContentEnvVar)
	return ""
}

// parsedConfig is the subset of OPENCODE_CONFIG_CONTENT the tests assert on:
// the pinned backend model and the session-scoped permission decisions. The
// permission values are decoded raw because a value may be a plain string
// decision or a path-pattern object once writable roots are bounded.
type parsedConfig struct {
	Model      string                     `json:"model"`
	Permission map[string]json.RawMessage `json:"permission"`
}

// permString returns a string-valued permission decision for key, failing the
// test if the value is missing or not a plain string.
func permString(t *testing.T, perm map[string]json.RawMessage, key string) string {
	t.Helper()
	raw, ok := perm[key]
	if !ok {
		t.Fatalf("permission missing key %q", key)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("permission[%q] = %s, want a string decision: %v", key, raw, err)
	}
	return s
}

func parseConfigContent(t *testing.T, content string) parsedConfig {
	t.Helper()
	var cfg parsedConfig
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT is not valid JSON %q: %v", content, err)
	}
	return cfg
}

func configModel(t *testing.T, content string) string {
	t.Helper()
	return parseConfigContent(t, content).Model
}

// TestBuildCommand_NormalModeAsksForMediatedSurfaces proves a normal (non
// dangerous-skip) OpenCode session is configured to ask for every mediated
// permission surface — shell, web fetch/search, subagent, skill — and for user
// questions, delivered inline via OPENCODE_CONFIG_CONTENT so the user's global
// OpenCode configuration is not mutated (Task 3). With no mounted read roots
// supplied, reads also ask rather than being silently allowed, so an
// external-directory read still pauses through Agentico.
func TestBuildCommand_NormalModeAsksForMediatedSurfaces(t *testing.T) {
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: "anthropic/claude-sonnet-4-5", DangerouslySkipPerms: false})
	if err != nil {
		t.Fatalf("BuildCommand() error: %v", err)
	}
	perm := parseConfigContent(t, configContentValue(t, env)).Permission
	for _, key := range []string{"bash", "webfetch", "websearch", "task", "skill", "question"} {
		if got := permString(t, perm, key); got != "ask" {
			t.Errorf("normal-mode permission[%q] = %q, want ask", key, got)
		}
	}
	// With no mounted read roots, reads are not silently allowed in normal mode;
	// they ask so external reads pause through Agentico.
	if got := permString(t, perm, "read"); got != "ask" {
		t.Errorf("normal-mode permission[read] = %q, want ask when no read roots are mounted", got)
	}
	// With no writable roots supplied, file edits fall back to the bare mode
	// decision rather than a path-pattern object.
	if got := permString(t, perm, "edit"); got != "ask" {
		t.Errorf("normal-mode permission[edit] = %q, want ask", got)
	}
}

// TestBuildCommand_DangerousSkipAllowsToolsButAsksQuestions proves dangerous-skip
// mode configures OpenCode to allow the permissioned tool surfaces
// noninteractively, while user questions remain "ask" so AskUserQuestion-style
// prompts still route as user-input decisions rather than being silently
// auto-approved (Task 3).
func TestBuildCommand_DangerousSkipAllowsToolsButAsksQuestions(t *testing.T) {
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: "anthropic/claude-sonnet-4-5", DangerouslySkipPerms: true})
	if err != nil {
		t.Fatalf("BuildCommand() error: %v", err)
	}
	perm := parseConfigContent(t, configContentValue(t, env)).Permission
	for _, key := range []string{"bash", "edit", "write", "apply_patch", "webfetch", "websearch", "task", "skill"} {
		if got := permString(t, perm, key); got != "allow" {
			t.Errorf("dangerous-skip permission[%q] = %q, want allow", key, got)
		}
	}
	if got := permString(t, perm, "question"); got != "ask" {
		t.Errorf("dangerous-skip permission[question] = %q, want ask (questions must not be auto-approved)", got)
	}
}

func TestInstallHint_SupportedDistribution(t *testing.T) {
	if got := New().InstallHint(); !strings.Contains(got, "opencode.ai/install") {
		t.Fatalf("InstallHint() = %q, want the supported opencode.ai installer", got)
	}
}

func TestMinVersion_Is1_17_9(t *testing.T) {
	if got := New().MinVersion(); got != [3]int{1, 17, 9} {
		t.Fatalf("MinVersion() = %v, want [1 17 9]", got)
	}
}

// TestEnforcesMinVersion_True proves OpenCode opts into the startup version gate
// so a too-old CLI is filtered out of the ready provider set rather than left
// routable with only a warning.
func TestEnforcesMinVersion_True(t *testing.T) {
	if !New().EnforcesMinVersion() {
		t.Fatal("EnforcesMinVersion() = false, want true (too-old OpenCode must be filtered out)")
	}
}

func TestCostAndContext_TreatedAsZero(t *testing.T) {
	p := New()
	if got := p.ComputeCost("anthropic/claude-sonnet-4-5", 1000, 2000); got != 0 {
		t.Fatalf("ComputeCost() = %v, want 0 (unknown pricing)", got)
	}
	if got := p.ContextWindowForModel("anthropic/claude-sonnet-4-5"); got != 0 {
		t.Fatalf("ContextWindowForModel() = %d, want 0 (unknown window)", got)
	}
}

func TestEnvVarsToExclude_Nil(t *testing.T) {
	if got := New().EnvVarsToExclude(); got != nil {
		t.Fatalf("EnvVarsToExclude() = %v, want nil", got)
	}
}

func TestProviderImplementsExpectedInterfaces(t *testing.T) {
	var p any = New()
	if _, ok := p.(llm.LLMProvider); !ok {
		t.Error("Provider does not implement llm.LLMProvider")
	}
	if _, ok := p.(llm.ReadinessChecker); !ok {
		t.Error("Provider does not implement llm.ReadinessChecker")
	}
	if _, ok := p.(llm.VersionEnforcer); !ok {
		t.Error("Provider does not implement llm.VersionEnforcer")
	}
	if _, ok := p.(llm.PromptAdapter); !ok {
		t.Error("Provider does not implement llm.PromptAdapter")
	}
	if _, ok := p.(llm.CostCalculator); !ok {
		t.Error("Provider does not implement llm.CostCalculator")
	}
	// Phase 1 keeps OpenCode out of catalog-driven surfaces: it must NOT
	// advertise a discoverable catalog.
	if _, ok := p.(llm.CatalogProvider); ok {
		t.Error("Provider unexpectedly implements llm.CatalogProvider (must stay out of catalog defaults)")
	}
	if _, ok := p.(llm.CatalogDiscoverer); ok {
		t.Error("Provider unexpectedly implements llm.CatalogDiscoverer")
	}
}

func TestAskingQuestionsClause_EmbedsConfidenceContract(t *testing.T) {
	clause := New().AskingQuestionsClause()
	if clause == "" {
		t.Fatal("AskingQuestionsClause() is empty")
	}
	if !strings.Contains(clause, llm.RecommendationConfidenceClause) {
		t.Error("AskingQuestionsClause() does not embed the shared recommendation-confidence contract")
	}
}

func TestCheckReadiness_States(t *testing.T) {
	tests := []struct {
		name      string
		runner    func(t *testing.T) func(context.Context, string, []string, []string) ([]byte, error)
		wantReady bool
		detailHas string
		remedyHas string
	}{
		{
			name: "ready when models are listed",
			runner: func(t *testing.T) func(context.Context, string, []string, []string) ([]byte, error) {
				return func(_ context.Context, name string, args []string, _ []string) ([]byte, error) {
					if name != "opencode" || !slices.Equal(args, []string{"models"}) {
						t.Fatalf("readiness ran %q %v, want opencode [models]", name, args)
					}
					return []byte("anthropic/claude-sonnet-4-5\nopenai/gpt-5\n"), nil
				}
			},
			wantReady: true,
			detailHas: "configured",
		},
		{
			name: "unconfigured when no models are listed",
			runner: func(t *testing.T) func(context.Context, string, []string, []string) ([]byte, error) {
				return func(context.Context, string, []string, []string) ([]byte, error) {
					return []byte("\n  \n"), nil
				}
			},
			wantReady: false,
			detailHas: "no provider is configured",
			remedyHas: "opencode auth login",
		},
		{
			name: "command failed",
			runner: func(t *testing.T) func(context.Context, string, []string, []string) ([]byte, error) {
				return func(context.Context, string, []string, []string) ([]byte, error) {
					return []byte("boom"), errors.New("exit status 1")
				}
			},
			wantReady: false,
			detailHas: "could not list OpenCode models",
			remedyHas: "opencode auth login",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Provider{runner: tt.runner(t)}
			status := p.CheckReadiness(context.Background())
			if status.Ready != tt.wantReady {
				t.Fatalf("Ready = %v, want %v (detail=%q remedy=%q)", status.Ready, tt.wantReady, status.Detail, status.Remedy)
			}
			if tt.detailHas != "" && !strings.Contains(status.Detail, tt.detailHas) {
				t.Errorf("Detail = %q, want substring %q", status.Detail, tt.detailHas)
			}
			if tt.remedyHas != "" && !strings.Contains(status.Remedy, tt.remedyHas) {
				t.Errorf("Remedy = %q, want substring %q", status.Remedy, tt.remedyHas)
			}
		})
	}
}

// TestCheckReadiness_RedactsSecretsInDiagnostics proves the readiness probe
// never surfaces credential-like content even when `opencode models` fails and
// dumps an auth token, API key, or provider config into its output or error.
// The plan requires readiness details to redact or omit OpenCode auth tokens,
// API keys, provider config contents, and environment-derived secrets before
// they are surfaced or persisted.
func TestCheckReadiness_RedactsSecretsInDiagnostics(t *testing.T) {
	withEnv(t, []string{"PATH=/usr/bin"})

	cases := []struct {
		name string
		out  []byte
		err  error
		// gone lists every credential-like value AND provider-config structure
		// fragment that must not appear in the surfaced readiness Detail. The
		// config object is omitted wholesale, so its keys, nesting braces, and
		// provider names must all be gone — not just the embedded secret.
		gone []string
	}{
		{
			name: "credential and provider config in command output",
			out:  []byte(`error: provider auth failed; config {"provider":{"anthropic":{"options":{"apiKey":"sk-ant-READINESSLEAK1234567890"}}}}`),
			err:  errors.New("exit status 1"),
			gone: []string{"sk-ant-READINESSLEAK1234567890", `"provider"`, `"options"`, "anthropic", "{", "}"},
		},
		{
			name: "credential in command error only",
			out:  []byte(""),
			err:  errors.New("dial backend failed using token=tok_live_READINESSERR0987654321"),
			gone: []string{"tok_live_READINESSERR0987654321"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, cmdErr := tc.out, tc.err
			p := &Provider{
				runner: func(context.Context, string, []string, []string) ([]byte, error) {
					return out, cmdErr
				},
			}
			status := p.CheckReadiness(context.Background())
			if status.Ready {
				t.Fatalf("Ready = true, want false on command failure")
			}
			for _, leak := range tc.gone {
				if strings.Contains(status.Detail, leak) {
					t.Fatalf("readiness Detail surfaced %q (credential or provider config content): %q", leak, status.Detail)
				}
			}
			// The detail still names the failure so it stays actionable.
			if !strings.Contains(status.Detail, "could not list OpenCode models") {
				t.Fatalf("readiness Detail lost its diagnostic framing: %q", status.Detail)
			}
		})
	}
}

func TestCheckReadiness_TimeoutDistinguished(t *testing.T) {
	p := &Provider{
		runner: func(ctx context.Context, _ string, _ []string, _ []string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	status := p.CheckReadiness(ctx)
	if status.Ready {
		t.Fatal("Ready = true, want false on timeout")
	}
	if !strings.Contains(status.Detail, "timed out") {
		t.Fatalf("Detail = %q, want timeout detail", status.Detail)
	}
}

// TestExplicitOnlyRouting_RegistryLevel proves that with OpenCode registered
// alongside other providers, only an explicit "opencode:" selection resolves to
// it, bare names route elsewhere, the backend slash-form is passed through
// unchanged, and OpenCode contributes no entries to the registry's model lists.
func TestExplicitOnlyRouting_RegistryLevel(t *testing.T) {
	reg := llm.NewRegistry()
	reg.Register(&fakeBareProvider{name: "claude", models: []string{"sonnet", "opus"}})
	reg.Register(New())

	prov, bare, err := reg.ResolveModel("opencode:anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("ResolveModel(opencode:...) error: %v", err)
	}
	if prov.Name() != "opencode" {
		t.Fatalf("ResolveModel routed to %q, want opencode", prov.Name())
	}
	if bare != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("ResolveModel bare model = %q, want backend slash form unchanged", bare)
	}

	// A bare model resolves to the other provider, never OpenCode.
	prov2, _, err := reg.ResolveModel("sonnet")
	if err != nil {
		t.Fatalf("ResolveModel(sonnet) error: %v", err)
	}
	if prov2.Name() == "opencode" {
		t.Fatal("bare model 'sonnet' resolved to opencode; explicit-only routing violated")
	}

	// OpenCode contributes nothing to the registry's user-selectable models.
	for _, m := range reg.AvailableModels() {
		if strings.HasPrefix(m, "opencode") || strings.Contains(m, "anthropic/") {
			t.Errorf("registry AvailableModels() leaked an OpenCode model: %q", m)
		}
	}
}

// fakeBareProvider is a minimal provider that matches its bare model names. It
// stands in for a normal catalog provider in routing tests.
type fakeBareProvider struct {
	name   string
	models []string
}

func (f *fakeBareProvider) Name() string { return f.name }
func (f *fakeBareProvider) MatchesModel(m string) bool {
	return slices.Contains(f.models, m)
}
func (f *fakeBareProvider) DetectCLI() bool           { return true }
func (f *fakeBareProvider) AvailableModels() []string { return f.models }
func (f *fakeBareProvider) BuildCommand(llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}
func (f *fakeBareProvider) NewProtocol(llm.ProtocolOpts) llm.Protocol { return nil }
func (f *fakeBareProvider) InstallHint() string                       { return "" }
func (f *fakeBareProvider) VersionInfo() (string, error)              { return "1.0.0", nil }
func (f *fakeBareProvider) MinVersion() [3]int                        { return [3]int{0, 0, 0} }
func (f *fakeBareProvider) EnvVarsToExclude() []string                { return nil }

// TestLiveVersionMeetsMinimum verifies, against a real installed OpenCode CLI,
// that VersionInfo parses to at least MinVersion. Skipped in -short runs and
// when the binary is absent so CI stays deterministic.
func TestLiveVersionMeetsMinimum(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live OpenCode version probe in short mode")
	}
	p := New()
	if !p.DetectCLI() {
		t.Skip("opencode CLI not installed")
	}
	raw, err := p.VersionInfo()
	if err != nil {
		t.Fatalf("VersionInfo() error: %v", err)
	}
	major, minor, patch, perr := parseSemver(raw)
	if perr != nil {
		t.Fatalf("could not parse version %q: %v", raw, perr)
	}
	min := p.MinVersion()
	got := [3]int{major, minor, patch}
	if !atLeast(got, min) {
		t.Fatalf("installed OpenCode version %v is below MinVersion %v", got, min)
	}
}

func parseSemver(s string) (int, int, int, error) {
	fields := strings.FieldsFunc(strings.TrimSpace(s), func(r rune) bool { return r == '.' || r == ' ' })
	if len(fields) < 3 {
		return 0, 0, 0, errors.New("not enough version components")
	}
	var nums [3]int
	for i := 0; i < 3; i++ {
		n := 0
		for _, c := range fields[i] {
			if c < '0' || c > '9' {
				return 0, 0, 0, errors.New("non-numeric version component")
			}
			n = n*10 + int(c-'0')
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], nil
}

func atLeast(got, min [3]int) bool {
	for i := 0; i < 3; i++ {
		if got[i] != min[i] {
			return got[i] > min[i]
		}
	}
	return true
}
