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

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func gatewayWinningCatalog() PhaseModelCatalog {
	cat := PhaseModelCatalog{
		Fields:        append([]string(nil), phaseCatalogFields...),
		ProviderOrder: []string{"claude", "gateway"},
		ProviderModels: map[string][]string{
			"claude":  {"sonnet[200K]", "opus[200K]"},
			"gateway": {"vendor/sonnet[200K]", "vendor/gpt-5"},
		},
		ProviderModelInfos: map[string][]llm.ModelInfo{
			"claude": {
				{ID: "sonnet[200K]", ContextWindow: 200_000, Category: "balanced"},
				{ID: "opus[200K]", ContextWindow: 200_000, Category: "capable"},
			},
			"gateway": {
				{ID: "vendor/sonnet[200K]", Aliases: []string{"vendor/sonnet"}, ContextWindow: 400_000, Category: "balanced"},
				{ID: "vendor/gpt-5", Category: "capable"},
			},
		},
		PhaseDefaults:       map[string]string{},
		PhaseProviderModels: map[string]map[string][]string{},
	}
	for _, field := range globalModelFields {
		cat.PhaseDefaults[field] = "gateway:vendor/sonnet[200K]"
		cat.PhaseProviderModels[field] = map[string][]string{
			"claude":  {"sonnet[200K]", "opus[200K]"},
			"gateway": {"vendor/sonnet[200K]", "vendor/gpt-5"},
		}
	}
	return cat
}

func TestPhaseModelCatalog_ModelOptionsForField(t *testing.T) {
	t.Parallel()
	cat := PhaseModelCatalog{
		ProviderOrder: []string{"claude", "codex"},
		ProviderModels: map[string][]string{
			"claude": {"sonnet", "opus"},
			"codex":  {"gpt-5"},
		},
		PhaseProviderModels: map[string]map[string][]string{
			"Research": {"claude": {"opus"}, "codex": {"gpt-5"}},
		},
	}

	if got, want := cat.ModelOptionsForField("Research"), []string{"opus", "gpt-5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Research options = %v, want %v", got, want)
	}
	if got, want := cat.ModelOptionsForField("Planning"), []string{"sonnet", "opus", "gpt-5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Planning options = %v, want %v", got, want)
	}
}

func TestPhaseModelCatalog_ClampModelValue(t *testing.T) {
	t.Parallel()
	cat := gatewayWinningCatalog()
	const prefixed = "gateway:vendor/sonnet[200K]"

	if got := cat.ClampModelValue("Research", prefixed); got != prefixed {
		t.Errorf("prefixed value = %q, want %q", got, prefixed)
	}
	if got := cat.ClampModelValue("Research", "sonnet[200K]"); got != "sonnet[200K]" {
		t.Errorf("bare value = %q, want sonnet[200K]", got)
	}
	if got := cat.ClampModelValue("Research", "retired/model"); got != "sonnet[200K]" {
		t.Errorf("stale value = %q, want first eligible model", got)
	}
	if got := (PhaseModelCatalog{}).ClampModelValue("Research", prefixed); got != prefixed {
		t.Errorf("empty catalog value = %q, want unchanged", got)
	}
}

func TestPhaseModelCatalog_DisplayEntriesCarryMetadata(t *testing.T) {
	t.Parallel()
	const id = "portkey/@fireworks/accounts/fireworks/models/glm-5p2"
	cat := PhaseModelCatalog{
		ProviderOrder:  []string{"gateway"},
		ProviderModels: map[string][]string{"gateway": {id}},
		ProviderModelInfos: map[string][]llm.ModelInfo{
			"gateway": {{
				ID: id, DisplayName: "GLM 5.2", ContextWindow: 131_000,
				Category: "balanced", Aliases: []string{"glm-5p2", "fireworks/glm-5p2"},
			}},
		},
		PhaseProviderModels: map[string]map[string][]string{"Planning": {"gateway": {id}}},
	}

	entries := cat.ModelEntriesForField("Planning")
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one", entries)
	}
	got := entries[0]
	if got.Agent != "gateway" || got.ModelID != id || got.DisplayName != "GLM 5.2" || got.FullID != id {
		t.Fatalf("identity metadata = %+v", got)
	}
	if got.ContextWindow != 131_000 || got.Category != "balanced" {
		t.Fatalf("capability metadata = %+v", got)
	}
	if !slices.Equal(got.Aliases, []string{"glm-5p2", "fireworks/glm-5p2"}) {
		t.Fatalf("aliases = %v", got.Aliases)
	}
}

func TestPhaseModelCatalog_IncludesDiscoveredModelsWithUnknownCategory(t *testing.T) {
	t.Parallel()
	const id = "portkey/@fireworks/accounts/fireworks/models/glm-5p2"
	cat := PhaseModelCatalog{
		ProviderOrder:  []string{"gateway"},
		ProviderModels: map[string][]string{"gateway": {"ollama/gemma4", id}},
		ProviderModelInfos: map[string][]llm.ModelInfo{
			"gateway": {
				{ID: "ollama/gemma4", Category: "balanced"},
				{ID: id, DisplayName: "GLM 5.2"},
			},
		},
	}

	entries := cat.EntriesForFieldAndAgent("Research", "gateway")
	for _, entry := range entries {
		if entry.ModelID == id {
			if entry.Category != "balanced" || entry.ContextWindow != 1_000_000 {
				t.Fatalf("normalized GLM metadata = %+v", entry)
			}
			return
		}
	}
	t.Fatalf("entries = %+v, want GLM", entries)
}

func TestPhaseModelCatalog_StringOnlyCatalogFallback(t *testing.T) {
	t.Parallel()
	cat := PhaseModelCatalog{
		ProviderOrder:  []string{"gateway"},
		ProviderModels: map[string][]string{"gateway": {"vendor/model"}},
		PhaseDefaults:  map[string]string{"Research": "vendor/model"},
	}

	entries := cat.ModelEntriesForField("Research")
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one", entries)
	}
	got := entries[0]
	if got.Agent != "gateway" || got.ModelID != "vendor/model" || got.DisplayName != "vendor/model" || !got.Recommended {
		t.Fatalf("synthetic entry = %+v", got)
	}
}

func TestPhaseModelCatalog_SelectionValue(t *testing.T) {
	t.Parallel()
	multi := gatewayWinningCatalog()
	entry, ok := multi.RecommendedEntryForAgent("Research", "gateway")
	if !ok {
		t.Fatal("missing recommended gateway entry")
	}
	if got := multi.SelectionValue(entry); got != "gateway:"+entry.ModelID {
		t.Fatalf("multi-provider value = %q", got)
	}

	solo := PhaseModelCatalog{
		ProviderOrder:  []string{"gateway"},
		ProviderModels: map[string][]string{"gateway": {"vendor/model"}},
		PhaseDefaults:  map[string]string{"Planning": "vendor/model"},
	}
	soloEntry, ok := solo.RecommendedEntryForAgent("Planning", "gateway")
	if !ok || solo.SelectionValue(soloEntry) != "vendor/model" {
		t.Fatalf("solo entry = %+v, ok=%v", soloEntry, ok)
	}
}

func TestPhaseModelCatalog_AutomaticReviewSelectionQualifiesOnlyAcrossEligibleProviders(t *testing.T) {
	t.Parallel()

	cat := PhaseModelCatalog{
		ProviderOrder: []string{"claude", "opencode", "codex"},
		ProviderModels: map[string][]string{
			"claude":   {"haiku"},
			"opencode": {"anthropic/haiku"},
			"codex":    {"gpt-5.4"},
		},
		PhaseProviderModels: map[string]map[string][]string{
			automaticReviewField: {
				"claude": {"haiku"},
				"codex":  {"gpt-5.4"},
			},
		},
	}

	claude := cat.EntriesForFieldAndAgent(automaticReviewField, "claude")[0]
	if got := cat.SelectionValueForField(automaticReviewField, claude); got != "claude:haiku" {
		t.Fatalf("multi-provider automatic-review value = %q, want claude:haiku", got)
	}

	cat.PhaseProviderModels[automaticReviewField] = map[string][]string{"claude": {"haiku"}}
	if got := cat.SelectionValueForField(automaticReviewField, claude); got != "haiku" {
		t.Fatalf("single eligible provider value = %q, want haiku", got)
	}
}

func TestPhaseModelCatalog_MarksRecommendedEntry(t *testing.T) {
	t.Parallel()
	cat := gatewayWinningCatalog()
	entries := cat.EntriesForFieldAndAgent("Research", "gateway")
	for _, entry := range entries {
		if entry.ModelID == "vendor/sonnet[200K]" {
			if !entry.Recommended {
				t.Fatalf("winning entry = %+v, want Recommended", entry)
			}
			return
		}
	}
	t.Fatalf("entries = %+v, want winning gateway model", entries)
}
