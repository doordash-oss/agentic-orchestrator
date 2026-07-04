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

// gatewayWinningRegistry returns a registry where Claude and a slash-form model
// provider are both ready, and the slash-form provider's balanced model has the
// largest context window, so the provider-neutral ranking makes it the
// recommended default for every role.
func gatewayWinningRegistry() *llm.Registry {
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
		name:   "gateway",
		models: []string{"vendor/sonnet[200K]", "vendor/gpt-5"},
		catalog: []llm.ModelInfo{
			{ID: "vendor/sonnet[200K]", Aliases: []string{"vendor/sonnet"}, ContextWindow: 400_000, Category: "balanced"},
			{ID: "vendor/gpt-5", Category: "capable"},
		},
	})
	return reg
}

// TestBuildPhaseModelCatalog_SurfacesProviderGroup proves that once a provider is
// ready and detected it appears as its own provider group in the shared
// phase-model catalog the wizard and config editor consume: its backend ids
// populate ProviderModels and the per-phase eligible lists, and when it wins the
// provider-neutral ranking the recommended default carries the routing prefix
// in multi-provider form.
func TestBuildPhaseModelCatalog_SurfacesProviderGroup(t *testing.T) {
	t.Parallel()
	cat := BuildPhaseModelCatalog(gatewayWinningRegistry(), config.DefaultsConfig{})

	if !slices.Contains(cat.ProviderOrder, "gateway") {
		t.Fatalf("ProviderOrder = %v, want it to include gateway", cat.ProviderOrder)
	}
	if got := cat.ProviderModels["gateway"]; !slices.Contains(got, "vendor/sonnet[200K]") {
		t.Errorf("gateway ProviderModels = %v, want the slash-form backend id", got)
	}
	for _, field := range []string{"Clarify", "Research", "Planning", "Implementation", "Review", "KB Build"} {
		if _, ok := cat.PhaseProviderModels[field]["gateway"]; !ok {
			t.Errorf("PhaseProviderModels[%q] missing gateway group", field)
		}
		if got := cat.PhaseDefaults[field]; got != "gateway:vendor/sonnet[200K]" {
			t.Errorf("PhaseDefaults[%q] = %q, want gateway:vendor/sonnet[200K]", field, got)
		}
	}
}

// TestBuildPhaseModelCatalog_FieldsExcludeUtilities proves the feature-scoped
// catalog builder is unaffected by the new global role mapping: Fields must
// stay at the original 6 entries (no Utilities row in the per-feature Edit
// Config overlay), even though the catalog now always computes Utilities
// eligibility data internally for BuildWorkspaceModelCatalog to reuse.
func TestBuildPhaseModelCatalog_FieldsExcludeUtilities(t *testing.T) {
	t.Parallel()
	cat := BuildPhaseModelCatalog(gatewayWinningRegistry(), config.DefaultsConfig{})

	for _, f := range cat.Fields {
		if f == "Utilities" {
			t.Fatalf("Fields = %v, feature-scoped catalog must not include Utilities", cat.Fields)
		}
	}
	if _, ok := cat.PhaseDefaults["Utilities"]; !ok {
		t.Error("PhaseDefaults should still carry a Utilities entry for BuildWorkspaceModelCatalog to reuse")
	}
	if _, ok := cat.PhaseProviderModels["Utilities"]; !ok {
		t.Error("PhaseProviderModels should still carry a Utilities entry for BuildWorkspaceModelCatalog to reuse")
	}
}

// TestBuildWorkspaceModelCatalog_IncludesUtilitiesInFields proves the
// workspace-scoped wrapper exposes the 7th field and reuses the same
// provider-neutral ranking as the other roles (gateway's larger-context
// balanced model wins, same as TestBuildPhaseModelCatalog_SurfacesProviderGroup).
func TestBuildWorkspaceModelCatalog_IncludesUtilitiesInFields(t *testing.T) {
	t.Parallel()
	cat := BuildWorkspaceModelCatalog(gatewayWinningRegistry(), config.DefaultsConfig{})

	wantFields := []string{"Clarify", "Research", "Planning", "Implementation", "Review", "Utilities", "KB Build"}
	if !reflect.DeepEqual(cat.Fields, wantFields) {
		t.Errorf("Fields = %v, want %v", cat.Fields, wantFields)
	}
	if got := cat.PhaseDefaults["Utilities"]; got != "gateway:vendor/sonnet[200K]" {
		t.Errorf("PhaseDefaults[Utilities] = %q, want gateway:vendor/sonnet[200K]", got)
	}
	if got := cat.PhaseProviderModels["Utilities"]["gateway"]; !slices.Contains(got, "vendor/sonnet[200K]") {
		t.Errorf("PhaseProviderModels[Utilities][gateway] = %v, want it to include vendor/sonnet[200K]", got)
	}
}

// TestBuildPhaseModelCatalog_Shape builds a catalog from a real *llm.Registry
// and verifies the Fields list, PhaseDefaults, and PhaseProviderModels have
// the expected shape.
func TestBuildPhaseModelCatalog_Shape(t *testing.T) {
	t.Parallel()
	reg := llm.NewRegistry()
	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})

	wantFields := []string{"Clarify", "Research", "Planning", "Implementation", "Review", "KB Build"}
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
	if len(cat.Fields) != 6 {
		t.Errorf("Fields = %d entries, want 6", len(cat.Fields))
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
		Fields:        []string{"Clarify", "Research", "Planning", "Implementation", "Review", "KB Build"},
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
	cat := BuildPhaseModelCatalog(gatewayWinningRegistry(), config.DefaultsConfig{})
	const prefixed = "gateway:vendor/sonnet[200K]"

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

func TestPhaseModelCatalog_DisplayEntriesCarryAgentAndMetadata(t *testing.T) {
	t.Parallel()
	reg := llm.NewRegistry()
	reg.Register(&phaseCatalogStubProvider{
		name:   "gateway",
		models: []string{"portkey/@fireworks/accounts/fireworks/models/glm-5p2"},
		catalog: []llm.ModelInfo{
			{
				ID:            "portkey/@fireworks/accounts/fireworks/models/glm-5p2",
				DisplayName:   "GLM 5.2",
				ContextWindow: 131_000,
				Category:      "balanced",
				Aliases:       []string{"glm-5p2", "fireworks/glm-5p2"},
			},
		},
	})

	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})
	entries := cat.ModelEntriesForField("Planning")
	if len(entries) != 1 {
		t.Fatalf("ModelEntriesForField(Planning) = %+v, want one entry", entries)
	}
	got := entries[0]
	if got.Agent != "gateway" {
		t.Fatalf("Agent = %q, want gateway", got.Agent)
	}
	if got.ModelID != "portkey/@fireworks/accounts/fireworks/models/glm-5p2" {
		t.Fatalf("ModelID = %q", got.ModelID)
	}
	if got.DisplayName != "GLM 5.2" {
		t.Fatalf("DisplayName = %q, want GLM 5.2", got.DisplayName)
	}
	if got.FullID != got.ModelID {
		t.Fatalf("FullID = %q, want ModelID %q", got.FullID, got.ModelID)
	}
	if got.ContextWindow != 131_000 {
		t.Fatalf("ContextWindow = %d, want 131000", got.ContextWindow)
	}
	if got.Category != "balanced" {
		t.Fatalf("Category = %q, want balanced", got.Category)
	}
	if !slices.Equal(got.Aliases, []string{"glm-5p2", "fireworks/glm-5p2"}) {
		t.Fatalf("Aliases = %v, want [glm-5p2 fireworks/glm-5p2]", got.Aliases)
	}
}

func TestPhaseModelCatalog_EntriesForFieldAndAgentIncludeDiscoveredModelsWithUnknownCategory(t *testing.T) {
	t.Parallel()
	reg := llm.NewRegistry()
	reg.Register(&phaseCatalogStubProvider{
		name:   "gateway",
		models: []string{"ollama/gemma4:31b-256k[262K]", "portkey/@fireworks/accounts/fireworks/models/glm-5p2"},
		catalog: []llm.ModelInfo{
			{ID: "ollama/gemma4:31b-256k[262K]", DisplayName: "Gemma 4 31B Dense", ContextWindow: 262_144, Category: "balanced"},
			{ID: "portkey/@fireworks/accounts/fireworks/models/glm-5p2", DisplayName: "GLM 5.2", ContextWindow: 131_072},
		},
	})

	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})
	entries := cat.EntriesForFieldAndAgent("Research", "gateway")
	var foundGLM bool
	for _, entry := range entries {
		if entry.ModelID == "portkey/@fireworks/accounts/fireworks/models/glm-5p2" {
			foundGLM = true
			if entry.DisplayName != "GLM 5.2" {
				t.Fatalf("GLM DisplayName = %q, want GLM 5.2", entry.DisplayName)
			}
		}
	}
	if !foundGLM {
		t.Fatalf("EntriesForFieldAndAgent(Research, gateway) = %+v, want discovered GLM even without a phase category", entries)
	}
}

func TestPhaseModelCatalog_NormalizesCachedGLMMetadata(t *testing.T) {
	t.Parallel()
	reg := llm.NewRegistry()
	reg.Register(&phaseCatalogStubProvider{
		name:   "gateway",
		models: []string{"portkey/@fireworks/accounts/fireworks/models/glm-5p2"},
		catalog: []llm.ModelInfo{
			{ID: "portkey/@fireworks/accounts/fireworks/models/glm-5p2", DisplayName: "glm-5p2"},
		},
	})

	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})
	entries := cat.EntriesForFieldAndAgent("Research", "gateway")
	if len(entries) != 1 {
		t.Fatalf("EntriesForFieldAndAgent = %+v, want one GLM entry", entries)
	}
	got := entries[0]
	if got.Category != "balanced" {
		t.Fatalf("GLM Category = %q, want balanced", got.Category)
	}
	if got.ContextWindow != 1_000_000 {
		t.Fatalf("GLM ContextWindow = %d, want 1000000", got.ContextWindow)
	}
}

func TestPhaseModelCatalog_ModelEntriesForFieldSynthesizesStringOnlyCatalog(t *testing.T) {
	t.Parallel()
	cat := PhaseModelCatalog{
		ProviderOrder: []string{"claude", "gateway"},
		ProviderModels: map[string][]string{
			"claude":  {"sonnet[200K]"},
			"gateway": {"vendor/model"},
		},
		PhaseProviderModels: map[string]map[string][]string{
			"Research": {
				"gateway": {"vendor/model"},
			},
		},
		PhaseDefaults: map[string]string{
			"Research": "gateway:vendor/model",
		},
	}

	entries := cat.ModelEntriesForField("Research")
	if len(entries) != 2 {
		t.Fatalf("ModelEntriesForField(Research) = %+v, want provider catalog plus phase entry", entries)
	}
	got := entries[1]
	if got.Agent != "gateway" {
		t.Fatalf("Agent = %q, want gateway", got.Agent)
	}
	if got.ModelID != "vendor/model" {
		t.Fatalf("ModelID = %q, want vendor/model", got.ModelID)
	}
	if got.DisplayName != "vendor/model" {
		t.Fatalf("DisplayName = %q, want vendor/model", got.DisplayName)
	}
	if got.FullID != "vendor/model" {
		t.Fatalf("FullID = %q, want vendor/model", got.FullID)
	}
	if !got.Recommended {
		t.Fatalf("Recommended = false, want true for gateway:vendor/model default")
	}
}

func TestPhaseModelCatalog_ProviderEntryGroupsForFieldUsesStringFallback(t *testing.T) {
	t.Parallel()
	cat := PhaseModelCatalog{
		ProviderOrder: []string{"claude"},
		ProviderModels: map[string][]string{
			"claude": {"sonnet[200K]"},
		},
		PhaseProviderModels: map[string]map[string][]string{
			"Research": {
				"gateway": {"vendor/model"},
			},
		},
		PhaseDefaults: map[string]string{
			"Research": "sonnet[200K]",
		},
	}

	stringGroups := cat.ProviderGroupsForField("Research")
	entryGroups := cat.ProviderEntryGroupsForField("Research")
	if len(entryGroups) != 1 {
		t.Fatalf("ProviderEntryGroupsForField groups = %+v, want one fallback group", entryGroups)
	}
	if entryGroups[0].Name != "claude" {
		t.Fatalf("entry group name = %q, want claude", entryGroups[0].Name)
	}
	if len(entryGroups[0].Models) != len(cat.ProviderModels["claude"]) {
		t.Fatalf("entry group models = %+v, want same count as string models %+v", entryGroups[0].Models, stringGroups[0].Models)
	}
	for i, entry := range entryGroups[0].Models {
		if entry.ModelID != cat.ProviderModels["claude"][i] {
			t.Fatalf("entry model %d = %q, want provider option %q", i, entry.ModelID, cat.ProviderModels["claude"][i])
		}
	}
}

func TestPhaseModelCatalog_ProviderCatalogAliasesAreDeepCopied(t *testing.T) {
	t.Parallel()
	catalog := []llm.ModelInfo{
		{
			ID:            "vendor/glm",
			DisplayName:   "Vendor GLM",
			ContextWindow: 131_000,
			Category:      "balanced",
			Aliases:       []string{"glm-original"},
		},
	}
	reg := llm.NewRegistry()
	provider := &phaseCatalogStubProvider{
		name:    "gateway",
		models:  []string{"vendor/glm"},
		catalog: catalog,
	}
	reg.Register(provider)

	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})
	provider.catalog[0].Aliases[0] = "glm-mutated"

	entries := cat.ModelEntriesForField("Planning")
	if len(entries) != 1 {
		t.Fatalf("ModelEntriesForField(Planning) = %+v, want one entry", entries)
	}
	if !slices.Equal(entries[0].Aliases, []string{"glm-original"}) {
		t.Fatalf("Aliases after provider mutation = %v, want [glm-original]", entries[0].Aliases)
	}
}

func TestPhaseModelCatalog_PartialProviderCatalogSynthesizesMissingAvailableModels(t *testing.T) {
	t.Parallel()
	reg := llm.NewRegistry()
	reg.Register(&phaseCatalogStubProvider{
		name:   "gateway",
		models: []string{"vendor/rich", "vendor/missing"},
		catalog: []llm.ModelInfo{
			{ID: "vendor/rich", DisplayName: "Rich Model", Category: "balanced"},
		},
	})

	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})
	infos := cat.ProviderModelInfos["gateway"]
	if len(infos) != 2 {
		t.Fatalf("ProviderModelInfos[gateway] = %+v, want rich and synthetic missing entries", infos)
	}
	if infos[0].ID != "vendor/rich" || infos[0].DisplayName != "Rich Model" {
		t.Fatalf("ProviderModelInfos[gateway][0] = %+v, want rich catalog metadata", infos[0])
	}
	if infos[1].ID != "vendor/missing" {
		t.Fatalf("ProviderModelInfos[gateway][1] = %+v, want synthetic vendor/missing", infos[1])
	}

	entries := cat.ModelEntriesForField("Unknown")
	var foundMissing bool
	for _, entry := range entries {
		if entry.ModelID == "vendor/missing" {
			foundMissing = true
			if entry.DisplayName != "vendor/missing" {
				t.Fatalf("synthetic missing DisplayName = %q, want vendor/missing", entry.DisplayName)
			}
		}
	}
	if !foundMissing {
		t.Fatalf("ModelEntriesForField(Unknown) = %+v, want synthetic vendor/missing entry", entries)
	}
}

func TestPhaseModelCatalog_SelectionValuePrefixesWhenMultipleAgents(t *testing.T) {
	t.Parallel()
	multi := BuildPhaseModelCatalog(gatewayWinningRegistry(), config.DefaultsConfig{})
	entry, ok := multi.RecommendedEntryForAgent("Research", "gateway")
	if !ok {
		t.Fatal("missing recommended gateway Research entry")
	}
	if got := multi.SelectionValue(entry); got != "gateway:"+entry.ModelID {
		t.Fatalf("SelectionValue(multi) = %q, want gateway:%s", got, entry.ModelID)
	}

	soloReg := llm.NewRegistry()
	soloReg.Register(&phaseCatalogStubProvider{
		name:   "gateway",
		models: []string{"vendor/model"},
		catalog: []llm.ModelInfo{
			{ID: "vendor/model", DisplayName: "Vendor Model", Category: "balanced"},
		},
	})
	solo := BuildPhaseModelCatalog(soloReg, config.DefaultsConfig{})
	soloEntry, ok := solo.RecommendedEntryForAgent("Planning", "gateway")
	if !ok {
		t.Fatal("missing solo gateway Planning entry")
	}
	if got := solo.SelectionValue(soloEntry); got != "vendor/model" {
		t.Fatalf("SelectionValue(solo) = %q, want bare vendor/model", got)
	}
}

func TestPhaseModelCatalog_EntriesForFieldAndAgentMarkRecommended(t *testing.T) {
	t.Parallel()
	cat := BuildPhaseModelCatalog(gatewayWinningRegistry(), config.DefaultsConfig{})
	entries := cat.EntriesForFieldAndAgent("Research", "gateway")
	if len(entries) == 0 {
		t.Fatal("EntriesForFieldAndAgent(Research, gateway) returned no entries")
	}

	for _, entry := range entries {
		if entry.ModelID != "vendor/sonnet[200K]" {
			continue
		}
		if !entry.Recommended {
			t.Fatalf("gateway winning Research entry Recommended = false, want true: %+v", entry)
		}
		return
	}
	t.Fatalf("EntriesForFieldAndAgent(Research, gateway) = %+v, want winning gateway model", entries)
}

func TestPhaseModelCatalog_ProviderEntryGroupsForFieldMarkRecommended(t *testing.T) {
	t.Parallel()
	cat := BuildPhaseModelCatalog(gatewayWinningRegistry(), config.DefaultsConfig{})
	groups := cat.ProviderEntryGroupsForField("Research")

	for _, group := range groups {
		if group.Name != "gateway" {
			continue
		}
		for _, entry := range group.Models {
			if entry.ModelID != "vendor/sonnet[200K]" {
				continue
			}
			if !entry.Recommended {
				t.Fatalf("gateway winning Research group entry Recommended = false, want true: %+v", entry)
			}
			return
		}
		t.Fatalf("gateway Research group = %+v, want winning gateway model", group.Models)
	}
	t.Fatalf("ProviderEntryGroupsForField(Research) = %+v, want gateway group", groups)
}
