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

func TestMatchesModel_ExplicitPrefixAndFallback(t *testing.T) {
	p := New()
	matchCases := []string{
		// An explicit "opencode:" prefix always matches, in or out of the catalog.
		"opencode:anthropic/claude-sonnet-4-5",
		"opencode:vendor/custom-model",
		// Before discovery, bare slash-form ids/aliases resolve through the curated
		// fallback catalog, so a ready OpenCode is reachable without the prefix.
		"anthropic/claude-sonnet-4-5[200K]", // fallback canonical id
		"anthropic/claude-sonnet-4-5",       // fallback unsuffixed alias
		"openai/gpt-5",                      // fallback id
	}
	for _, m := range matchCases {
		if !p.MatchesModel(m) {
			t.Errorf("MatchesModel(%q) = false, want true", m)
		}
	}

	// A bare name that is neither a fallback id/alias nor slash-form must not
	// resolve to OpenCode: it never captures a bare name (e.g. "sonnet",
	// "gpt-5.4") meant for another provider, nor an unknown slash-form id. The
	// bare routing prefix with no backend model is not a valid selection either.
	noMatchCases := []string{
		"sonnet",
		"gpt-5.4",
		"anthropic/claude-opus", // not a fallback id (fallback has claude-opus-4-1)
		"openai/gpt-4",
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

// TestMatchesModel_BareCatalogEntries proves that once a catalog is discovered,
// a bare slash-form id or one of its aliases resolves to OpenCode through normal
// catalog matching, while names absent from the catalog still do not.
func TestMatchesModel_BareCatalogEntries(t *testing.T) {
	p := New()
	p.SetModelCatalog([]llm.ModelInfo{
		{ID: "anthropic/claude-sonnet-4-5[200K]", Aliases: []string{"anthropic/claude-sonnet-4-5"}, ContextWindow: 200_000},
		{ID: "openai/gpt-5"},
	})
	for _, m := range []string{
		"anthropic/claude-sonnet-4-5[200K]", // canonical id
		"anthropic/claude-sonnet-4-5",       // unsuffixed alias
		"ANTHROPIC/CLAUDE-SONNET-4-5",       // case-insensitive
		"openai/gpt-5",
		"opencode:openai/gpt-5", // explicit prefix still matches
	} {
		if !p.MatchesModel(m) {
			t.Errorf("MatchesModel(%q) = false, want true after discovery", m)
		}
	}
	for _, m := range []string{"sonnet", "anthropic/claude-opus", "openai/gpt-4"} {
		if p.MatchesModel(m) {
			t.Errorf("MatchesModel(%q) = true, want false (not in catalog)", m)
		}
	}
}

// TestAvailableModels_FallbackThenDiscovered proves OpenCode advertises its
// curated offline fallback ids before discovery (so setup/config consumers never
// see an empty model list for a ready OpenCode), then replaces them with the
// discovered catalog ids once discovery populates a catalog.
func TestAvailableModels_FallbackThenDiscovered(t *testing.T) {
	p := New()
	fallback := p.AvailableModels()
	if len(fallback) == 0 {
		t.Fatal("AvailableModels() = empty before discovery, want the built-in fallback catalog")
	}
	if !slices.Contains(fallback, "anthropic/claude-sonnet-4-5[200K]") {
		t.Fatalf("fallback AvailableModels() = %v, want a curated slash-form id", fallback)
	}

	p.SetModelCatalog([]llm.ModelInfo{
		{ID: "anthropic/claude-sonnet-4-5[200K]"},
		{ID: "openai/gpt-5"},
	})
	want := []string{"anthropic/claude-sonnet-4-5[200K]", "openai/gpt-5"}
	if got := p.AvailableModels(); !slices.Equal(got, want) {
		t.Fatalf("AvailableModels() = %v, want %v after discovery replaces the fallback", got, want)
	}
}

// TestModelCatalog_FallbackWhenEmpty proves a ready OpenCode whose live discovery
// failed or returned nothing still exposes a non-empty curated catalog through
// CatalogProvider — the documented degrade-to-fallback path — and that the
// fallback spans the cheap/balanced/capable categories selection relies on. A
// discovered (or cached) catalog overrides the fallback entirely.
func TestModelCatalog_FallbackWhenEmpty(t *testing.T) {
	p := New()

	for _, empty := range [][]llm.ModelInfo{nil, {}} {
		p.SetModelCatalog(empty)
		cat := p.ModelCatalog()
		if len(cat) == 0 {
			t.Fatalf("ModelCatalog() = empty for catalog %v, want the built-in fallback", empty)
		}
		categories := map[string]bool{}
		var ids []string
		for _, m := range cat {
			categories[m.Category] = true
			ids = append(ids, m.ID)
			if m.ID == "" {
				t.Errorf("fallback entry has empty ID: %+v", m)
			}
		}
		for _, want := range []string{"cheap", "balanced", "capable"} {
			if !categories[want] {
				t.Errorf("fallback categories = %v, want a %q entry so role selection can choose one", ids, want)
			}
		}
	}

	// A discovered catalog overrides the fallback.
	p.SetModelCatalog([]llm.ModelInfo{{ID: "openai/gpt-5", Category: "capable"}})
	if got := p.ModelCatalog(); len(got) != 1 || got[0].ID != "openai/gpt-5" {
		t.Fatalf("ModelCatalog() = %+v, want the discovered catalog to override the fallback", got)
	}
}

// TestFallbackCatalog_SuffixesAndAliases proves fallback entries are normalized
// exactly like discovered ones: a known context window promotes the id to the
// "[<window>]" form, records the window, derives a deterministic category, and
// keeps the bare backend id reachable as an alias. Pricing is absent from the
// fallback, so ComputeCost stays zero (real cost flows over ACP).
func TestFallbackCatalog_SuffixesAndAliases(t *testing.T) {
	p := New()
	cat := p.ModelCatalog()
	sonnet, ok := findModel(cat, "anthropic/claude-sonnet-4-5[200K]")
	if !ok {
		t.Fatalf("fallback missing suffixed sonnet id; got %+v", cat)
	}
	if sonnet.ContextWindow != 200_000 {
		t.Errorf("fallback sonnet ContextWindow = %d, want 200000", sonnet.ContextWindow)
	}
	if sonnet.Category != "balanced" {
		t.Errorf("fallback sonnet Category = %q, want balanced", sonnet.Category)
	}
	if !slices.Contains(sonnet.Aliases, "anthropic/claude-sonnet-4-5") {
		t.Errorf("fallback sonnet aliases = %v, want unsuffixed backend id preserved", sonnet.Aliases)
	}
	if got := p.ComputeCost("anthropic/claude-sonnet-4-5[200K]", 1_000_000, 1_000_000); got != 0 {
		t.Errorf("ComputeCost(fallback) = %v, want 0 (fallback carries no pricing)", got)
	}
	if got := p.ContextWindowForModel("anthropic/claude-sonnet-4-5"); got != 200_000 {
		t.Errorf("ContextWindowForModel(fallback alias) = %d, want 200000", got)
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
		// The Agentico context-window suffix is stripped too, so OpenCode never
		// receives bracketed selection metadata as part of a backend model name.
		"opencode:anthropic/claude-sonnet-4-5[200K]": "anthropic/claude-sonnet-4-5",
		"anthropic/claude-sonnet-4-5[1M]":            "anthropic/claude-sonnet-4-5",
		// A colon-form tag (ollama "model:tag") survives; only the trailing
		// "[window]" is removed.
		"opencode:ollama/llama3.1:8b[256K]": "ollama/llama3.1:8b",
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
	const secret = "sk-ant-deadbeefdeadbeefdeadbeef0123"
	cases := []struct {
		name        string
		model       string
		mustNotLeak string
	}{
		{"empty selection", "", ""},
		{"routing prefix only", "opencode:", ""},
		{"routing prefix with whitespace backend", "opencode:   ", ""},
		{"flag-shaped bare", "--dangerously-skip", ""},
		{"flag-shaped behind prefix", "opencode:--dangerously-skip", ""},
		{"at sign without slash form", "opencode:foo@bar", ""},
		{"at sign with empty model", "opencode:@fireworks", ""},
		{"command substitution", "anthropic/$(whoami)", ""},
		{"variable interpolation", "anthropic/${HOME}", ""},
		{"backtick", "anthropic/`id`", ""},
		{"semicolon chain", "anthropic/claude;rm -rf /", ""},
		{"pipe", "anthropic/claude|cat", ""},
		{"ampersand", "anthropic/claude&", ""},
		{"redirect", "anthropic/claude>out", ""},
		{"embedded space", "anthropic/claude sonnet", ""},
		{"embedded newline", "anthropic/cla\nude", ""},
		{"double quote", "anthropic/\"claude\"", ""},
		{"credential-like backend", "opencode:anthropic/" + secret + "@example", ""},
		{"credential-like backend with unsafe rune", "opencode:anthropic/" + secret + "@example$bad", secret},
		// A space behind a context-window suffix is still rejected: the suffix is
		// stripped first, exposing the unsafe space underneath.
		{"unsafe value behind suffix", "anthropic/claude sonnet[200K]", ""},
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
			if tc.mustNotLeak != "" && strings.Contains(err.Error(), tc.mustNotLeak) {
				t.Fatalf("BuildCommand(%q) error leaked credential-like backend content: %q", tc.model, err)
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
		"anthropic/claude-sonnet-4-5":                                   "anthropic/claude-sonnet-4-5",
		"openai/gpt-5-codex":                                            "openai/gpt-5-codex",
		"opencode:openai/gpt-5":                                         "openai/gpt-5",
		"opencode:anthropic/claude-sonnet-4-5":                          "anthropic/claude-sonnet-4-5",
		"opencode:portkey/@fireworks/accounts/fireworks/models/glm-5p2": "portkey/@fireworks/accounts/fireworks/models/glm-5p2",
		"ollama/llama3.1:8b":                                            "ollama/llama3.1:8b",
		"  opencode:x/y  ":                                              "x/y",
		// The Agentico context-window suffix is selection metadata only: it is
		// stripped so OpenCode receives the native backend model, with or without
		// the routing prefix.
		"anthropic/claude-sonnet-4-5[200K]":        "anthropic/claude-sonnet-4-5",
		"opencode:anthropic/claude-sonnet-4-5[1M]": "anthropic/claude-sonnet-4-5",
		"opencode:ollama/llama3.1:8b[256K]":        "ollama/llama3.1:8b",
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

// TestVersionInfo_ParsesSemverAndNeverEchoesRawOutput proves VersionInfo returns
// the parsed semver — never the raw `opencode --version` output — so the startup
// catalog cache key, cache filename, and persisted cache metadata can never carry
// trailing credential-like or terminal-control content from a malformed or
// hostile version line. On unparseable output it returns a generic error that
// does not echo the raw output, closing the same leak on the version diagnostic.
func TestVersionInfo_ParsesSemverAndNeverEchoesRawOutput(t *testing.T) {
	const secret = "sk-ant-deadbeefdeadbeefdeadbeef0123"
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "clean semver", raw: "1.17.9\n", want: "1.17.9"},
		{name: "trailing credential stripped", raw: "1.17.9 " + secret + "\n", want: "1.17.9"},
		{name: "terminal control stripped", raw: "1.17.9\x1b]0;pwn\x07\n", want: "1.17.9"},
		{name: "name prefixed", raw: "opencode 1.17.9", want: "1.17.9"},
		{name: "no semver errors without echo", raw: secret + "\n", wantErr: true},
		{name: "empty errors", raw: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := func(_ context.Context, name string, args []string, _ []string) ([]byte, error) {
				if name != "opencode" || strings.Join(args, " ") != "--version" {
					t.Fatalf("ran %q %v, want `opencode --version`", name, args)
				}
				return []byte(tc.raw), nil
			}
			got, err := NewWithRunner(runner).VersionInfo()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("VersionInfo() = %q, want error", got)
				}
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("VersionInfo() error %q echoed credential-like raw output", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("VersionInfo() error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("VersionInfo() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCostAndContext_ZeroWithoutMetadata proves that for a model absent from any
// catalog (no pricing and no context window in either the discovered catalog or
// the fallback), cost and context window both report the "unknown" zero rather
// than a guessed value. The curated fallback carries context windows but never
// pricing, so even a fallback model's cost stays zero.
func TestCostAndContext_ZeroWithoutMetadata(t *testing.T) {
	p := New()
	// A fallback model has a known window but no pricing → zero cost.
	if got := p.ComputeCost("anthropic/claude-sonnet-4-5", 1000, 2000); got != 0 {
		t.Fatalf("ComputeCost(fallback) = %v, want 0 (fallback carries no pricing)", got)
	}
	// A model in no catalog at all → unknown (zero) window and zero cost.
	if got := p.ComputeCost("vendor/unknown-model", 1000, 2000); got != 0 {
		t.Fatalf("ComputeCost(unknown) = %v, want 0 (no pricing metadata)", got)
	}
	if got := p.ContextWindowForModel("vendor/unknown-model"); got != 0 {
		t.Fatalf("ContextWindowForModel(unknown) = %d, want 0 (no catalog metadata)", got)
	}
	// A catalog with no pricing (e.g. loaded from the version-keyed cache) still
	// yields zero cost, but its discovered context window is returned.
	p.SetModelCatalog([]llm.ModelInfo{
		{ID: "anthropic/claude-sonnet-4-5[200K]", Aliases: []string{"anthropic/claude-sonnet-4-5"}, ContextWindow: 200_000},
	})
	if got := p.ComputeCost("anthropic/claude-sonnet-4-5[200K]", 1_000_000, 1_000_000); got != 0 {
		t.Fatalf("ComputeCost() = %v, want 0 when catalog carries no pricing", got)
	}
	if got := p.ContextWindowForModel("anthropic/claude-sonnet-4-5[200K]"); got != 200_000 {
		t.Fatalf("ContextWindowForModel(suffixed) = %d, want 200000", got)
	}
	if got := p.ContextWindowForModel("anthropic/claude-sonnet-4-5"); got != 200_000 {
		t.Fatalf("ContextWindowForModel(alias) = %d, want 200000", got)
	}
	p.SetModelCatalog([]llm.ModelInfo{
		{ID: "portkey/@fireworks/accounts/fireworks/models/glm-5p2"},
	})
	if got := p.ContextWindowForModel("portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]"); got != 1_040_000 {
		t.Fatalf("ContextWindowForModel(suffix fallback) = %d, want 1040000", got)
	}
	if got := p.ContextWindowForModel("opencode:portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]"); got != 1_040_000 {
		t.Fatalf("ContextWindowForModel(prefixed suffix fallback) = %d, want 1040000", got)
	}
}

func TestEnvVarsToExclude_Nil(t *testing.T) {
	if got := New().EnvVarsToExclude(); got != nil {
		t.Fatalf("EnvVarsToExclude() = %v, want nil", got)
	}
}

// TestSupportsFinishOrViolateNudge_True proves OpenCode opts into the shared
// finish-or-violate auto-continuation retry: when a session ends its turn
// without producing the required completion artifacts, the harness may nudge the
// same live session to finish before declaring a protocol violation.
func TestSupportsFinishOrViolateNudge_True(t *testing.T) {
	if !New().SupportsFinishOrViolateNudge() {
		t.Fatal("SupportsFinishOrViolateNudge() = false, want true")
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
	// OpenCode now participates in the shared catalog surfaces: it advertises a
	// discoverable, refreshable, progress-streaming, enrichable catalog so it
	// flows through the same discovery, cache, registry, and model-list paths as
	// the other providers.
	if _, ok := p.(llm.CatalogProvider); !ok {
		t.Error("Provider does not implement llm.CatalogProvider")
	}
	if _, ok := p.(llm.CatalogDiscoverer); !ok {
		t.Error("Provider does not implement llm.CatalogDiscoverer")
	}
	if _, ok := p.(llm.CatalogProgressDiscoverer); !ok {
		t.Error("Provider does not implement llm.CatalogProgressDiscoverer")
	}
	if _, ok := p.(llm.CatalogEnricher); !ok {
		t.Error("Provider does not implement llm.CatalogEnricher")
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

// TestFallbackRouting_RegistryLevel proves that before live discovery, an
// OpenCode registered alongside other providers still degrades to its curated
// fallback catalog: an explicit "opencode:" selection routes to OpenCode and
// passes the backend slash-form through unchanged, a bare fallback id resolves
// to OpenCode (canonicalized to the suffixed catalog id), and a bare name
// belonging to another provider still routes there. (List-surface contribution
// is covered provider-level by TestAvailableModels_FallbackThenDiscovered, which
// does not depend on the opencode CLI being installed for DetectCLI.)
func TestFallbackRouting_RegistryLevel(t *testing.T) {
	reg := llm.NewRegistry()
	reg.Register(&fakeBareProvider{name: "claude", models: []string{"sonnet", "opus"}})
	reg.Register(New())

	// An explicit selection for a fallback model canonicalizes to the suffixed
	// fallback id (the same canonicalization a discovered catalog applies); the
	// "[200K]" is selection metadata that BackendModel later strips for launch.
	if prov, bare, err := reg.ResolveModel("opencode:anthropic/claude-sonnet-4-5"); err != nil ||
		prov.Name() != "opencode" || bare != "anthropic/claude-sonnet-4-5[200K]" {
		t.Fatalf("ResolveModel(opencode:anthropic/claude-sonnet-4-5) = (%v, %q, %v), want opencode canonicalized to the suffixed fallback id", prov, bare, err)
	}

	// An explicit selection for a model absent from the fallback still routes to
	// OpenCode and passes the backend slash-form through unchanged.
	if prov, bare, err := reg.ResolveModel("opencode:vendor/custom-model"); err != nil ||
		prov.Name() != "opencode" || bare != "vendor/custom-model" {
		t.Fatalf("ResolveModel(opencode:vendor/custom-model) = (%v, %q, %v), want opencode passthrough", prov, bare, err)
	}

	// A bare fallback slash-form id resolves to OpenCode and canonicalizes to the
	// suffixed fallback catalog id.
	if prov, bare, err := reg.ResolveModel("anthropic/claude-sonnet-4-5"); err != nil ||
		prov.Name() != "opencode" || bare != "anthropic/claude-sonnet-4-5[200K]" {
		t.Fatalf("ResolveModel(anthropic/claude-sonnet-4-5) = (%v, %q, %v), want opencode canonicalized to the suffixed fallback id", prov, bare, err)
	}

	// A bare name belonging to another provider still routes there, never to the
	// OpenCode fallback (the fallback ids are slash-form only).
	prov2, _, err := reg.ResolveModel("sonnet")
	if err != nil {
		t.Fatalf("ResolveModel(sonnet) error: %v", err)
	}
	if prov2.Name() != "claude" {
		t.Fatalf("bare model 'sonnet' resolved to %q, want claude", prov2.Name())
	}
}

// TestCatalogBackedRouting_RegistryLevel proves that once OpenCode advertises a
// discovered catalog, the registry resolves explicit "opencode:" selections,
// bare slash-form catalog ids, and unsuffixed aliases to OpenCode and
// canonicalizes them to the suffixed catalog id, while bare names belonging to
// another provider still route there (backward compatibility) and an unknown
// bare model is unresolved.
func TestCatalogBackedRouting_RegistryLevel(t *testing.T) {
	reg := llm.NewRegistry()
	reg.Register(&fakeBareProvider{name: "claude", models: []string{"sonnet", "opus"}})
	oc := New()
	oc.SetModelCatalog([]llm.ModelInfo{
		{ID: "anthropic/claude-sonnet-4-5[200K]", Aliases: []string{"anthropic/claude-sonnet-4-5"}, ContextWindow: 200_000, Category: "balanced"},
		{ID: "openai/gpt-5", Category: "capable"},
	})
	reg.Register(oc)

	const canonical = "anthropic/claude-sonnet-4-5[200K]"
	routesToOpenCode := map[string]string{
		"opencode:anthropic/claude-sonnet-4-5[200K]": canonical, // explicit, suffixed
		"opencode:anthropic/claude-sonnet-4-5":       canonical, // explicit, unsuffixed alias
		"anthropic/claude-sonnet-4-5[200K]":          canonical, // bare canonical id
		"anthropic/claude-sonnet-4-5":                canonical, // bare unsuffixed alias
	}
	for in, wantBare := range routesToOpenCode {
		prov, bare, err := reg.ResolveModel(in)
		if err != nil {
			t.Fatalf("ResolveModel(%q) error: %v", in, err)
		}
		if prov.Name() != "opencode" {
			t.Errorf("ResolveModel(%q) routed to %q, want opencode", in, prov.Name())
		}
		if bare != wantBare {
			t.Errorf("ResolveModel(%q) bare = %q, want %q", in, bare, wantBare)
		}
	}

	// A bare name belonging to another provider still routes there.
	if prov, _, err := reg.ResolveModel("sonnet"); err != nil || prov.Name() != "claude" {
		t.Fatalf("ResolveModel(sonnet) = (%v, %v), want claude provider", prov, err)
	}

	// An explicit opencode: selection for a model absent from the catalog still
	// routes to OpenCode and passes the backend through unchanged.
	if prov, bare, err := reg.ResolveModel("opencode:vendor/custom-model"); err != nil || prov.Name() != "opencode" || bare != "vendor/custom-model" {
		t.Fatalf("ResolveModel(opencode:vendor/custom-model) = (%v, %q, %v), want opencode passthrough", prov, bare, err)
	}

	// An unknown bare model resolves to no provider.
	if _, _, err := reg.ResolveModel("vendor/unknown-model"); err == nil {
		t.Fatal("ResolveModel(vendor/unknown-model) succeeded, want error for unknown bare model")
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
