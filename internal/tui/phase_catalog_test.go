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

package tui

import (
	"reflect"
	"slices"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

type phaseCatalogStubProvider struct {
	name    string
	models  []string
	catalog []llm.ModelInfo
}

func (p *phaseCatalogStubProvider) Name() string { return p.name }
func (p *phaseCatalogStubProvider) MatchesModel(model string) bool {
	return slices.Contains(p.models, model)
}
func (p *phaseCatalogStubProvider) DetectCLI() bool           { return true }
func (p *phaseCatalogStubProvider) AvailableModels() []string { return p.models }
func (p *phaseCatalogStubProvider) BuildCommand(llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}
func (p *phaseCatalogStubProvider) NewProtocol(llm.ProtocolOpts) llm.Protocol { return nil }
func (p *phaseCatalogStubProvider) InstallHint() string                       { return "" }
func (p *phaseCatalogStubProvider) VersionInfo() (string, error)              { return "", nil }
func (p *phaseCatalogStubProvider) MinVersion() [3]int                        { return [3]int{} }
func (p *phaseCatalogStubProvider) EnvVarsToExclude() []string                { return nil }
func (p *phaseCatalogStubProvider) ModelCatalog() []llm.ModelInfo             { return p.catalog }

// openCodeWinningRegistry returns a registry where Claude and OpenCode are both
// ready and OpenCode's balanced model has the largest context window, so the
// provider-neutral ranking makes OpenCode the recommended default for every
// role. Used by the OpenCode catalog/picker tests in this package.
func openCodeWinningRegistry() *llm.Registry {
	reg := llm.NewRegistry()
	reg.Register(&phaseCatalogStubProvider{
		name:   "claude",
		models: []string{"sonnet[200K]", "opus[200K]"},
		catalog: []llm.ModelInfo{
			{ID: "sonnet[200K]", ContextWindow: 200_000, Category: "balanced"},
			{ID: "opus[200K]", ContextWindow: 200_000, Category: "capable"},
		},
	})
	reg.Register(&phaseCatalogStubProvider{
		name:   "opencode",
		models: []string{"anthropic/claude-sonnet-4-5[200K]", "openai/gpt-5"},
		catalog: []llm.ModelInfo{
			{ID: "anthropic/claude-sonnet-4-5[200K]", Aliases: []string{"anthropic/claude-sonnet-4-5"}, ContextWindow: 400_000, Category: "balanced"},
			{ID: "openai/gpt-5", Category: "capable"},
		},
	})
	return reg
}

// TestBuildPhaseModelCatalog_SurfacesOpenCodeGroup proves that once OpenCode is a
// ready, detected provider it appears as its own provider group in the shared
// phase-model catalog the wizard and config editor consume: its backend ids
// populate ProviderModels and the per-phase eligible lists, and when OpenCode
// wins the provider-neutral ranking the recommended default carries the
// opencode: routing prefix (multi-provider form).
func TestBuildPhaseModelCatalog_SurfacesOpenCodeGroup(t *testing.T) {
	t.Parallel()
	cat := BuildPhaseModelCatalog(openCodeWinningRegistry(), config.DefaultsConfig{})

	if !slices.Contains(cat.ProviderOrder, "opencode") {
		t.Fatalf("ProviderOrder = %v, want it to include opencode", cat.ProviderOrder)
	}
	if got := cat.ProviderModels["opencode"]; !slices.Contains(got, "anthropic/claude-sonnet-4-5[200K]") {
		t.Errorf("opencode ProviderModels = %v, want the slash-form backend id", got)
	}
	for _, field := range []string{"Research", "Planning", "Implementation", "Review", "KB Build"} {
		if _, ok := cat.PhaseProviderModels[field]["opencode"]; !ok {
			t.Errorf("PhaseProviderModels[%q] missing opencode group", field)
		}
		if got := cat.PhaseDefaults[field]; got != "opencode:anthropic/claude-sonnet-4-5[200K]" {
			t.Errorf("PhaseDefaults[%q] = %q, want opencode:anthropic/claude-sonnet-4-5[200K]", field, got)
		}
	}
}

// TestBuildPhaseModelCatalog_Shape builds a catalog from a real *llm.Registry
// and verifies the Fields list, PhaseDefaults, and PhaseProviderModels have
// the expected shape.
func TestBuildPhaseModelCatalog_Shape(t *testing.T) {
	t.Parallel()
	reg := llm.NewRegistry()
	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})

	wantFields := []string{"Research", "Planning", "Implementation", "Review", "KB Build"}
	if !reflect.DeepEqual(cat.Fields, wantFields) {
		t.Errorf("Fields = %v, want %v", cat.Fields, wantFields)
	}
	// PhaseDefaults must have one entry per field (value may be empty if the
	// registry returns no default for that role).
	for _, f := range wantFields {
		if _, ok := cat.PhaseDefaults[f]; !ok {
			t.Errorf("PhaseDefaults missing key %q", f)
		}
	}
	// ProviderOrder is a subset of detected provider names (may be empty in
	// environments with no CLI binaries on PATH).
	detected := map[string]bool{}
	for _, p := range reg.DetectedProviders() {
		detected[p.Name()] = true
	}
	for _, name := range cat.ProviderOrder {
		if name == "default" {
			continue // fallback entry when no provider is detected
		}
		if !detected[name] {
			t.Errorf("ProviderOrder entry %q not in DetectedProviders", name)
		}
	}
	// PhaseProviderModels keys (if populated) are a subset of Fields.
	for k := range cat.PhaseProviderModels {
		found := false
		for _, f := range wantFields {
			if k == f {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("PhaseProviderModels has non-field key %q", k)
		}
	}
}

// TestBuildPhaseModelCatalog_NilRegistry verifies that passing nil for the
// registry yields a safe, non-panicking catalog with Fields still set.
func TestBuildPhaseModelCatalog_NilRegistry(t *testing.T) {
	t.Parallel()
	cat := BuildPhaseModelCatalog(nil, config.DefaultsConfig{})
	if len(cat.Fields) != 5 {
		t.Errorf("Fields = %d entries, want 5", len(cat.Fields))
	}
	if len(cat.ProviderOrder) != 0 {
		t.Errorf("ProviderOrder should be empty for nil registry, got %v", cat.ProviderOrder)
	}
	// Accessor methods must not panic on a nil-registry catalog.
	if opts := cat.ModelOptionsForField("Research"); len(opts) != 0 {
		t.Errorf("ModelOptionsForField should be empty, got %v", opts)
	}
	if all := cat.AllModels(); len(all) != 0 {
		t.Errorf("AllModels should be empty, got %v", all)
	}
	if g := cat.ProviderGroupsForField("Research"); g != nil {
		t.Errorf("ProviderGroupsForField should be nil for empty catalog, got %v", g)
	}
}

// TestPhaseModelCatalog_ModelOptionsForField verifies that the field-scoped
// option list respects provider order and falls back to AllModels when the
// phase-specific entry is empty.
func TestPhaseModelCatalog_ModelOptionsForField(t *testing.T) {
	t.Parallel()
	cat := PhaseModelCatalog{
		Fields:        []string{"Research", "Planning", "Implementation", "Review", "KB Build"},
		ProviderOrder: []string{"claude", "codex"},
		ProviderModels: map[string][]string{
			"claude": {"claude/sonnet-4-6", "claude/opus-4-7"},
			"codex":  {"codex/gpt-5-codex"},
		},
		PhaseProviderModels: map[string]map[string][]string{
			"Research": {
				"claude": {"claude/opus-4-7"},
				"codex":  {"codex/gpt-5-codex"},
			},
		},
		PhaseDefaults: map[string]string{},
	}

	got := cat.ModelOptionsForField("Research")
	want := []string{"claude/opus-4-7", "codex/gpt-5-codex"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ModelOptionsForField(Research) = %v, want %v", got, want)
	}
	// Field with no PhaseProviderModels entry falls back to AllModels in
	// provider order.
	got = cat.ModelOptionsForField("Planning")
	want = []string{"claude/sonnet-4-6", "claude/opus-4-7", "codex/gpt-5-codex"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ModelOptionsForField(Planning fallback) = %v, want %v", got, want)
	}
}

// TestPhaseModelCatalog_ClampModelValue proves provider-aware default clamping:
// a valid multi-provider default in routing-prefix form is kept, a valid bare
// option is kept, an ineligible value is replaced with the first eligible
// option, and an empty option set leaves the value untouched.
func TestPhaseModelCatalog_ClampModelValue(t *testing.T) {
	t.Parallel()
	cat := BuildPhaseModelCatalog(openCodeWinningRegistry(), config.DefaultsConfig{})
	const prefixed = "opencode:anthropic/claude-sonnet-4-5[200K]"

	if got := cat.ClampModelValue("Research", prefixed); got != prefixed {
		t.Errorf("ClampModelValue(prefixed) = %q, want kept %q", got, prefixed)
	}
	if got := cat.ClampModelValue("Research", "sonnet[200K]"); got != "sonnet[200K]" {
		t.Errorf("ClampModelValue(bare) = %q, want kept sonnet[200K]", got)
	}

	opts := cat.ModelOptionsForField("Research")
	if got := cat.ClampModelValue("Research", "retired/model"); got != opts[0] {
		t.Errorf("ClampModelValue(stale) = %q, want first eligible %q", got, opts[0])
	}

	empty := PhaseModelCatalog{}
	if got := empty.ClampModelValue("Research", prefixed); got != prefixed {
		t.Errorf("ClampModelValue(no options) = %q, want unchanged %q", got, prefixed)
	}
}

func TestBuildPhaseModelCatalog_PlanningUsesBalancedDefaultAndOptions(t *testing.T) {
	t.Parallel()
	reg := llm.NewRegistry()
	reg.Register(&phaseCatalogStubProvider{
		name:   "claude",
		models: []string{"opus", "sonnet", "haiku"},
		catalog: []llm.ModelInfo{
			{ID: "opus", Category: "capable"},
			{ID: "sonnet", Category: "balanced"},
			{ID: "haiku", Category: "cheap"},
		},
	})

	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})
	if got := cat.PhaseDefaults["Planning"]; got != "sonnet" {
		t.Errorf("Planning default = %q, want sonnet", got)
	}
	opts := cat.ModelOptionsForField("Planning")
	if !slices.Contains(opts, "sonnet") || !slices.Contains(opts, "opus") {
		t.Errorf("Planning options = %v, want sonnet and opus", opts)
	}
	if slices.Contains(opts, "haiku") {
		t.Errorf("Planning options = %v, want cheap model excluded", opts)
	}
}
