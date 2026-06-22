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
