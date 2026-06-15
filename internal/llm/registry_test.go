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

package llm_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// stubProvider is a minimal LLMProvider for testing.
type stubProvider struct {
	name           string
	models         []string
	hasCLI         bool
	installHint    string
	versionInfo    string
	versionInfoErr error
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) MatchesModel(m string) bool {
	for _, model := range s.models {
		if model == m {
			return true
		}
	}
	return false
}
func (s *stubProvider) DetectCLI() bool           { return s.hasCLI }
func (s *stubProvider) AvailableModels() []string { return s.models }
func (s *stubProvider) BuildCommand(_ llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}
func (s *stubProvider) NewProtocol(_ llm.ProtocolOpts) llm.Protocol { return nil }
func (s *stubProvider) InstallHint() string                         { return s.installHint }
func (s *stubProvider) VersionInfo() (string, error)                { return s.versionInfo, s.versionInfoErr }
func (s *stubProvider) MinVersion() [3]int                          { return [3]int{0, 0, 0} }
func (s *stubProvider) EnvVarsToExclude() []string                  { return nil }

// stubFullProvider implements LLMProvider + PromptAdapter + CostCalculator.
type stubFullProvider struct {
	stubProvider
	askingClause  string
	cost          float64
	contextWindow int
}

// stubCatalogProvider implements LLMProvider + CatalogProvider.
type stubCatalogProvider struct {
	stubProvider
	catalog []llm.ModelInfo
}

func (p *stubCatalogProvider) ModelCatalog() []llm.ModelInfo { return p.catalog }

func (s *stubFullProvider) AskingQuestionsClause() string            { return s.askingClause }
func (s *stubFullProvider) ComputeCost(string, int64, int64) float64 { return s.cost }
func (s *stubFullProvider) ContextWindowForModel(string) int         { return s.contextWindow }

func TestRegistry_ForModel(t *testing.T) {
	r := llm.NewRegistry()
	claude := &stubProvider{name: "claude", models: []string{"opus", "sonnet"}, hasCLI: true}
	codex := &stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: true}
	r.Register(claude)
	r.Register(codex)

	tests := []struct {
		model    string
		wantName string
		wantErr  bool
	}{
		{"opus", "claude", false},
		{"sonnet", "claude", false},
		{"gpt-5.4", "codex", false},
		{"unknown", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			p, err := r.ForModel(tt.model)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Name() != tt.wantName {
				t.Errorf("got provider %q, want %q", p.Name(), tt.wantName)
			}
		})
	}
}

func TestRegistry_AvailableModels(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus", "sonnet"}, hasCLI: true})
	r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: false})

	models := r.AvailableModels()
	if len(models) != 2 {
		t.Fatalf("expected 2 models (codex CLI not detected), got %d: %v", len(models), models)
	}
}

func TestRegistry_RestrictToProvidersFiltersDetectedModelsAndRouting(t *testing.T) {
	r := llm.NewRegistry()
	claude := &stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true}
	codex := &stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: true}
	r.Register(claude)
	r.Register(codex)

	r.RestrictToProviders([]llm.LLMProvider{claude})

	detected := r.DetectedProviders()
	if len(detected) != 1 || detected[0].Name() != "claude" {
		t.Fatalf("DetectedProviders() = %v, want only claude", detected)
	}
	if got := r.AvailableModels(); !slices.Equal(got, []string{"opus"}) {
		t.Fatalf("AvailableModels() = %v, want [opus]", got)
	}
	if _, err := r.ForModel("gpt-5.4"); err == nil {
		t.Fatal("ForModel(gpt-5.4) succeeded after codex was filtered")
	}
	if _, _, err := r.ResolveModel("codex:gpt-5.4"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("ResolveModel(codex:gpt-5.4) error = %v, want not available", err)
	}
	if got := r.ModelsForProvider("codex"); got != nil {
		t.Fatalf("ModelsForProvider(codex) = %v, want nil", got)
	}
}

func TestRegistry_ByName(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}})

	if p := r.ByName("claude"); p == nil {
		t.Fatal("expected provider, got nil")
	}
	if p := r.ByName("nonexistent"); p != nil {
		t.Fatalf("expected nil, got %v", p)
	}
}

func TestRegistry_PromptAdapterForModel(t *testing.T) {
	r := llm.NewRegistry()

	// stubProvider does NOT implement PromptAdapter
	r.Register(&stubProvider{name: "bare", models: []string{"bare-model"}, hasCLI: true})

	// stubFullProvider implements PromptAdapter
	r.Register(&stubFullProvider{
		stubProvider: stubProvider{name: "full", models: []string{"full-model"}, hasCLI: true},
		askingClause: "ask questions this way",
	})

	t.Run("provider_implements_PromptAdapter", func(t *testing.T) {
		pa, err := r.PromptAdapterForModel("full-model")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pa.AskingQuestionsClause() != "ask questions this way" {
			t.Errorf("got %q, want %q", pa.AskingQuestionsClause(), "ask questions this way")
		}
	})

	t.Run("provider_does_not_implement_PromptAdapter", func(t *testing.T) {
		_, err := r.PromptAdapterForModel("bare-model")
		if err == nil {
			t.Fatal("expected error for provider not implementing PromptAdapter")
		}
	})

	t.Run("unknown_model", func(t *testing.T) {
		_, err := r.PromptAdapterForModel("unknown")
		if err == nil {
			t.Fatal("expected error for unknown model")
		}
	})
}

func TestRegistry_CostCalculatorForModel(t *testing.T) {
	r := llm.NewRegistry()

	r.Register(&stubProvider{name: "bare", models: []string{"bare-model"}, hasCLI: true})
	r.Register(&stubFullProvider{
		stubProvider:  stubProvider{name: "full", models: []string{"full-model"}, hasCLI: true},
		cost:          1.23,
		contextWindow: 100000,
	})

	t.Run("provider_implements_CostCalculator", func(t *testing.T) {
		cc, err := r.CostCalculatorForModel("full-model")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cc.ComputeCost("full-model", 1000, 2000) != 1.23 {
			t.Errorf("got %f, want 1.23", cc.ComputeCost("full-model", 1000, 2000))
		}
		if cc.ContextWindowForModel("full-model") != 100000 {
			t.Errorf("got %d, want 100000", cc.ContextWindowForModel("full-model"))
		}
	})

	t.Run("provider_does_not_implement_CostCalculator", func(t *testing.T) {
		_, err := r.CostCalculatorForModel("bare-model")
		if err == nil {
			t.Fatal("expected error for provider not implementing CostCalculator")
		}
	})

	t.Run("unknown_model", func(t *testing.T) {
		_, err := r.CostCalculatorForModel("unknown")
		if err == nil {
			t.Fatal("expected error for unknown model")
		}
	})
}

func TestRegistry_ResolveModel_ExplicitProvider(t *testing.T) {
	r := llm.NewRegistry()
	claude := &stubProvider{name: "claude", models: []string{"opus", "sonnet"}, hasCLI: true}
	codex := &stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: true}
	r.Register(claude)
	r.Register(codex)

	tests := []struct {
		model         string
		wantProvider  string
		wantBareModel string
		wantErr       bool
		errContains   string
	}{
		{"claude:opus", "claude", "opus", false, ""},
		{"claude:haiku", "claude", "haiku", false, ""},
		{"codex:codex", "codex", "codex", false, ""},
		{"codex:gpt-5.4", "codex", "gpt-5.4", false, ""},
		{"unknown:x", "", "", true, "unknown provider"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			p, bareModel, err := r.ResolveModel(tt.model)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Name() != tt.wantProvider {
				t.Errorf("got provider %q, want %q", p.Name(), tt.wantProvider)
			}
			if bareModel != tt.wantBareModel {
				t.Errorf("got bareModel %q, want %q", bareModel, tt.wantBareModel)
			}
		})
	}
}

func TestRegistry_ResolveModel_BareModel_BackwardCompat(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus", "opus[1m]", "sonnet", "haiku"}, hasCLI: true})
	r.Register(&stubProvider{name: "codex", models: []string{"codex"}, hasCLI: true})

	tests := []struct {
		model         string
		wantProvider  string
		wantBareModel string
		wantErr       bool
		errContains   string
	}{
		{"opus", "claude", "opus", false, ""},
		{"opus[1m]", "claude", "opus[1m]", false, ""},
		{"sonnet", "claude", "sonnet", false, ""},
		{"haiku", "claude", "haiku", false, ""},
		{"codex", "codex", "codex", false, ""},
		{"nonexist", "", "", true, "no provider found"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			p, bareModel, err := r.ResolveModel(tt.model)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Name() != tt.wantProvider {
				t.Errorf("got provider %q, want %q", p.Name(), tt.wantProvider)
			}
			if bareModel != tt.wantBareModel {
				t.Errorf("got bareModel %q, want %q", bareModel, tt.wantBareModel)
			}
		})
	}
}

func TestRegistry_ResolveModel_CanonicalizesCatalogAliases(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubCatalogProvider{
		stubProvider: stubProvider{name: "claude", models: []string{"opus", "sonnet"}, hasCLI: true},
		catalog: []llm.ModelInfo{
			{ID: "opus", Aliases: []string{"opus[1m]"}},
			{ID: "sonnet"},
		},
	})

	tests := []struct {
		model         string
		wantProvider  string
		wantBareModel string
	}{
		{"opus[1m]", "claude", "opus"},
		{"claude:opus[1m]", "claude", "opus"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			p, bareModel, err := r.ResolveModel(tt.model)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Name() != tt.wantProvider {
				t.Errorf("got provider %q, want %q", p.Name(), tt.wantProvider)
			}
			if bareModel != tt.wantBareModel {
				t.Errorf("got bareModel %q, want %q", bareModel, tt.wantBareModel)
			}
		})
	}
}

func TestRegistry_ResolveModel_ColonEdgeCases(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "a", models: []string{"model"}, hasCLI: true})

	tests := []struct {
		name        string
		model       string
		wantErr     bool
		errContains string
		wantBare    string
	}{
		{"colon_at_start", ":model", true, "no provider found", ""},
		{"model_with_empty_bare", "a:", false, "", ""},
		{"multi_colon", "a:b:c", false, "", "b:c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, bareModel, err := r.ResolveModel(tt.model)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p == nil {
				t.Fatal("expected provider, got nil")
			}
			if bareModel != tt.wantBare {
				t.Errorf("got bareModel %q, want %q", bareModel, tt.wantBare)
			}
		})
	}
}

func TestRegistry_DefaultModels_UsesCatalogDefaults(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubCatalogProvider{
		stubProvider: stubProvider{name: "claude", models: []string{"opus", "sonnet", "haiku"}, hasCLI: true},
		catalog:      claudeCatalog,
	})
	r.Register(&stubCatalogProvider{
		stubProvider: stubProvider{name: "codex", models: []string{"gpt-5.4", "gpt-5.4-mini"}, hasCLI: true},
		catalog:      codexCatalog,
	})

	defaults := r.DefaultModels()
	if defaults[llm.PhaseResearch] != "claude:sonnet" {
		t.Errorf("research: got %q, want %q", defaults[llm.PhaseResearch], "claude:sonnet")
	}
	if defaults[llm.PhaseChat] != "claude:sonnet" {
		t.Errorf("chat: got %q, want %q", defaults[llm.PhaseChat], "claude:sonnet")
	}
	if defaults[llm.PhaseReview] != "codex:codex" {
		t.Errorf("review: got %q, want %q", defaults[llm.PhaseReview], "codex:codex")
	}
	if defaults[llm.PhaseKBBuild] != "claude:sonnet" {
		t.Errorf("kb_build: got %q, want %q", defaults[llm.PhaseKBBuild], "claude:sonnet")
	}
}

// --- Catalog-based test helpers ---

var claudeCatalog = []llm.ModelInfo{
	{ID: "opus", DisplayName: "Claude Opus", ContextWindow: 200000, Category: "capable"},
	{ID: "sonnet", DisplayName: "Claude Sonnet", ContextWindow: 200000, Category: "balanced"},
	{ID: "haiku", DisplayName: "Claude Haiku", ContextWindow: 200000, Category: "cheap"},
}

var codexCatalog = []llm.ModelInfo{
	{ID: "codex", DisplayName: "Codex", ContextWindow: 200000, Category: "capable"},
	{ID: "gpt-5.4-mini", DisplayName: "GPT 5.4 Mini", ContextWindow: 200000, Category: "balanced"},
}

func TestRegistry_CatalogAllModels(t *testing.T) {
	t.Run("collects_from_multiple_detected_providers_with_catalogs", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus", "sonnet", "haiku"}, hasCLI: true},
			catalog:      claudeCatalog,
		})
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "codex", models: []string{"codex", "gpt-5.4-mini"}, hasCLI: true},
			catalog:      codexCatalog,
		})

		all := r.AllModels()
		if len(all) != 5 {
			t.Fatalf("expected 5 models, got %d: %v", len(all), all)
		}
		ids := make([]string, len(all))
		for i, m := range all {
			ids[i] = m.ID
		}
		for _, want := range []string{"opus", "sonnet", "haiku", "codex", "gpt-5.4-mini"} {
			if !slices.Contains(ids, want) {
				t.Errorf("expected %q in %v", want, ids)
			}
		}
	})

	t.Run("creates_synthetic_for_provider_without_CatalogProvider", func(t *testing.T) {
		r := llm.NewRegistry()
		// bare stubProvider does not implement CatalogProvider
		r.Register(&stubProvider{name: "bare", models: []string{"bare-model-a", "bare-model-b"}, hasCLI: true})

		all := r.AllModels()
		if len(all) != 2 {
			t.Fatalf("expected 2 synthetic models, got %d", len(all))
		}
		for _, m := range all {
			if m.Category != "" {
				t.Errorf("expected empty category for synthetic entry, got %q", m.Category)
			}
			if m.ContextWindow != 0 {
				t.Errorf("expected 0 context window for synthetic entry, got %d", m.ContextWindow)
			}
		}
	})

	t.Run("skips_undetected_providers", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true},
			catalog:      claudeCatalog,
		})
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "codex", models: []string{"codex"}, hasCLI: false},
			catalog:      codexCatalog,
		})

		all := r.AllModels()
		// Only claude is detected
		if len(all) != len(claudeCatalog) {
			t.Fatalf("expected %d models (only claude detected), got %d", len(claudeCatalog), len(all))
		}
	})

	t.Run("returns_empty_for_no_detected_providers", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus"}, hasCLI: false},
			catalog:      claudeCatalog,
		})

		all := r.AllModels()
		if len(all) != 0 {
			t.Fatalf("expected 0 models, got %d", len(all))
		}
	})
}

func TestRegistry_CatalogModelsForProvider(t *testing.T) {
	t.Run("returns_catalog_for_named_provider", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus", "sonnet", "haiku"}, hasCLI: true},
			catalog:      claudeCatalog,
		})

		models := r.ModelsForProvider("claude")
		if len(models) != len(claudeCatalog) {
			t.Fatalf("expected %d models, got %d", len(claudeCatalog), len(models))
		}
		if models[0].ID != "opus" {
			t.Errorf("expected first model ID %q, got %q", "opus", models[0].ID)
		}
	})

	t.Run("returns_synthetic_entries_for_provider_without_catalog_interface", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubProvider{name: "bare", models: []string{"bare-model"}, hasCLI: true})

		models := r.ModelsForProvider("bare")
		if len(models) != 1 {
			t.Fatalf("expected 1 synthetic model, got %d", len(models))
		}
		if models[0].ID != "bare-model" {
			t.Errorf("expected synthetic model ID %q, got %q", "bare-model", models[0].ID)
		}
		if models[0].Category != "" {
			t.Errorf("expected empty category for synthetic entry, got %q", models[0].Category)
		}
	})

	t.Run("returns_nil_for_unknown_provider_name", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true},
			catalog:      claudeCatalog,
		})

		models := r.ModelsForProvider("nonexistent")
		if models != nil {
			t.Fatalf("expected nil, got %v", models)
		}
	})

	t.Run("returns_nil_for_undetected_provider", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus"}, hasCLI: false},
			catalog:      claudeCatalog,
		})

		models := r.ModelsForProvider("claude")
		if models != nil {
			t.Fatalf("expected nil for undetected provider, got %v", models)
		}
	})
}

func TestRegistry_CatalogMostCapableModel(t *testing.T) {
	t.Run("returns_most_capable_from_preferred_provider_when_hint_matches", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus", "sonnet", "haiku"}, hasCLI: true},
			catalog:      claudeCatalog,
		})
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "codex", models: []string{"codex", "gpt-5.4-mini"}, hasCLI: true},
			catalog:      codexCatalog,
		})

		got := r.MostCapableModel("claude")
		if got != "opus" {
			t.Errorf("expected %q, got %q", "opus", got)
		}
	})

	t.Run("falls_back_to_all_providers_when_hint_doesnt_match", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus", "sonnet", "haiku"}, hasCLI: true},
			catalog:      claudeCatalog,
		})

		// Hint doesn't match any registered provider
		got := r.MostCapableModel("nonexistent")
		if got != "opus" {
			t.Errorf("expected %q from fallback to all providers, got %q", "opus", got)
		}
	})

	t.Run("returns_first_available_model_when_no_catalogs_populated", func(t *testing.T) {
		r := llm.NewRegistry()
		// stubProvider without CatalogProvider — synthetic entries with no category
		r.Register(&stubProvider{name: "bare", models: []string{"model-a", "model-b"}, hasCLI: true})

		got := r.MostCapableModel("")
		// Synthetic entries have empty category (rank 0), MostCapableFrom returns first
		if got != "model-a" {
			t.Errorf("expected %q, got %q", "model-a", got)
		}
	})

	t.Run("works_with_single_provider", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "codex", models: []string{"codex", "gpt-5.4-mini"}, hasCLI: true},
			catalog:      codexCatalog,
		})

		got := r.MostCapableModel("codex")
		if got != "codex" {
			t.Errorf("expected %q, got %q", "codex", got)
		}
	})
}

func TestRegistry_CatalogCheapestModel(t *testing.T) {
	t.Run("returns_cheapest_across_all_providers", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus", "sonnet", "haiku"}, hasCLI: true},
			catalog:      claudeCatalog,
		})
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "codex", models: []string{"codex", "gpt-5.4-mini"}, hasCLI: true},
			catalog:      codexCatalog,
		})

		got := r.CheapestModel()
		if got != "haiku" {
			t.Errorf("expected %q (only cheap-category model), got %q", "haiku", got)
		}
	})

	t.Run("fallback_when_no_category_data", func(t *testing.T) {
		r := llm.NewRegistry()
		// Synthetic entries have no category — CheapestFrom returns false
		r.Register(&stubProvider{name: "bare", models: []string{"model-a", "model-b"}, hasCLI: true})

		got := r.CheapestModel()
		// Fallback: last available model
		if got != "model-b" {
			t.Errorf("expected %q (last available model fallback), got %q", "model-b", got)
		}
	})
}

func TestRegistry_CatalogBalancedModel(t *testing.T) {
	t.Run("returns_balanced_model", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus", "sonnet", "haiku"}, hasCLI: true},
			catalog:      claudeCatalog,
		})
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "codex", models: []string{"codex", "gpt-5.4-mini"}, hasCLI: true},
			catalog:      codexCatalog,
		})

		got := r.BalancedModel()
		if got != "sonnet" {
			t.Errorf("expected %q (first balanced-category model), got %q", "sonnet", got)
		}
	})

	t.Run("fallback_when_no_balanced_category", func(t *testing.T) {
		r := llm.NewRegistry()
		// Only capable and cheap — no balanced category
		capableAndCheap := []llm.ModelInfo{
			{ID: "top", Category: "capable", ContextWindow: 200000},
			{ID: "bottom", Category: "cheap", ContextWindow: 100000},
		}
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "test", models: []string{"top", "bottom"}, hasCLI: true},
			catalog:      capableAndCheap,
		})

		got := r.BalancedModel()
		// BalancedFrom picks closest to rank 2: cheap is rank 1 (dist 1), capable is rank 3 (dist 1)
		// Ties go to first encountered, which is capable ("top")
		if got != "top" {
			t.Errorf("expected %q (closest to balanced rank), got %q", "top", got)
		}
	})
}

func TestRegistry_CatalogLargestContextModel(t *testing.T) {
	t.Run("returns_model_with_largest_context_window", func(t *testing.T) {
		r := llm.NewRegistry()
		mixedContext := []llm.ModelInfo{
			{ID: "small", Category: "cheap", ContextWindow: 50000},
			{ID: "large", Category: "balanced", ContextWindow: 500000},
			{ID: "medium", Category: "capable", ContextWindow: 200000},
		}
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "test", models: []string{"small", "large", "medium"}, hasCLI: true},
			catalog:      mixedContext,
		})

		got := r.LargestContextModel()
		if got != "large" {
			t.Errorf("expected %q (largest context 500000), got %q", "large", got)
		}
	})

	t.Run("handles_equal_context_windows_prefers_more_capable", func(t *testing.T) {
		r := llm.NewRegistry()
		sameContext := []llm.ModelInfo{
			{ID: "balanced-model", Category: "balanced", ContextWindow: 200000},
			{ID: "capable-model", Category: "capable", ContextWindow: 200000},
		}
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "test", models: []string{"balanced-model", "capable-model"}, hasCLI: true},
			catalog:      sameContext,
		})

		got := r.LargestContextModel()
		if got != "capable-model" {
			t.Errorf("expected %q (higher category rank breaks tie), got %q", "capable-model", got)
		}
	})
}

func TestRegistry_CatalogDefaultModels(t *testing.T) {
	t.Run("both_providers_with_catalogs", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus", "sonnet", "haiku"}, hasCLI: true},
			catalog:      claudeCatalog,
		})
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "codex", models: []string{"codex", "gpt-5.4-mini"}, hasCLI: true},
			catalog:      codexCatalog,
		})

		mc := r.CatalogDefaultModels()

		// Research and KB build prefer cost-efficient claude defaults → "claude:sonnet"
		if mc.Research != "claude:sonnet" {
			t.Errorf("research: got %q, want %q", mc.Research, "claude:sonnet")
		}
		// Planning/Implementation prefer claude capable → "claude:opus"
		if mc.Planning != "claude:opus" {
			t.Errorf("planning: got %q, want %q", mc.Planning, "claude:opus")
		}
		if mc.Implementation != "claude:opus" {
			t.Errorf("implementation: got %q, want %q", mc.Implementation, "claude:opus")
		}
		// Review prefers codex capable → "codex:codex"
		if mc.Review != "codex:codex" {
			t.Errorf("review: got %q, want %q", mc.Review, "codex:codex")
		}
		// Chat prefers claude balanced → "claude:sonnet"
		if mc.Utilities != "claude:sonnet" {
			t.Errorf("chat: got %q, want %q", mc.Utilities, "claude:sonnet")
		}
		if mc.KBBuild != "claude:sonnet" {
			t.Errorf("kb_build: got %q, want %q", mc.KBBuild, "claude:sonnet")
		}
	})

	t.Run("claude_only", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus", "sonnet", "haiku"}, hasCLI: true},
			catalog:      claudeCatalog,
		})

		mc := r.CatalogDefaultModels()

		// Single provider → bare names (no prefix)
		if mc.Research != "sonnet" {
			t.Errorf("research: got %q, want %q", mc.Research, "sonnet")
		}
		if mc.Planning != "opus" {
			t.Errorf("planning: got %q, want %q", mc.Planning, "opus")
		}
		if mc.Implementation != "opus" {
			t.Errorf("implementation: got %q, want %q", mc.Implementation, "opus")
		}
		// Review prefers codex, but codex not available; falls back to claude capable
		if mc.Review != "opus" {
			t.Errorf("review: got %q, want %q", mc.Review, "opus")
		}
		if mc.Utilities != "sonnet" {
			t.Errorf("chat: got %q, want %q", mc.Utilities, "sonnet")
		}
		if mc.KBBuild != "sonnet" {
			t.Errorf("kb_build: got %q, want %q", mc.KBBuild, "sonnet")
		}
	})

	t.Run("codex_only", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "codex", models: []string{"codex", "gpt-5.4-mini"}, hasCLI: true},
			catalog:      codexCatalog,
		})

		mc := r.CatalogDefaultModels()

		// Single provider → bare names (no prefix)
		// Research prefers claude, but claude not available; falls back to a balanced codex model
		if mc.Research != "gpt-5.4-mini" {
			t.Errorf("research: got %q, want %q", mc.Research, "gpt-5.4-mini")
		}
		if mc.Planning != "codex" {
			t.Errorf("planning: got %q, want %q", mc.Planning, "codex")
		}
		if mc.Implementation != "codex" {
			t.Errorf("implementation: got %q, want %q", mc.Implementation, "codex")
		}
		if mc.Review != "codex" {
			t.Errorf("review: got %q, want %q", mc.Review, "codex")
		}
		if mc.Utilities != "gpt-5.4-mini" {
			t.Errorf("chat: got %q, want %q", mc.Utilities, "gpt-5.4-mini")
		}
		if mc.KBBuild != "gpt-5.4-mini" {
			t.Errorf("kb_build: got %q, want %q", mc.KBBuild, "gpt-5.4-mini")
		}
	})

	t.Run("codex_cost_efficient_research_and_kb_hint", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "codex", models: []string{"gpt-5.5[272K]", "gpt-5.4[272K]", "gpt-5.4[1M]", "gpt-5.4-mini[400K]"}, hasCLI: true},
			catalog: []llm.ModelInfo{
				{ID: "gpt-5.5[272K]", Category: "capable", ContextWindow: 272000, Aliases: []string{"gpt-5.5"}},
				{ID: "gpt-5.4[272K]", Category: "capable", ContextWindow: 272000, Aliases: []string{"gpt-5.4"}},
				{ID: "gpt-5.4[1M]", Category: "capable", ContextWindow: 1000000},
				{ID: "gpt-5.4-mini[400K]", Category: "balanced", ContextWindow: 400000, Aliases: []string{"gpt-5.4-mini"}},
			},
		})

		mc := r.CatalogDefaultModels()
		if mc.Research != "gpt-5.4[272K]" {
			t.Errorf("research: got %q, want %q", mc.Research, "gpt-5.4[272K]")
		}
		if mc.KBBuild != "gpt-5.4[272K]" {
			t.Errorf("kb_build: got %q, want %q", mc.KBBuild, "gpt-5.4[272K]")
		}
		if mc.Planning != "gpt-5.4[1M]" {
			t.Errorf("planning: got %q, want most-capable %q", mc.Planning, "gpt-5.4[1M]")
		}
	})

	t.Run("no_providers", func(t *testing.T) {
		r := llm.NewRegistry()

		mc := r.CatalogDefaultModels()
		if mc != (config.ModelConfig{}) {
			t.Errorf("expected empty defaults, got %+v", mc)
		}
	})

	t.Run("no_catalogs_populated", func(t *testing.T) {
		r := llm.NewRegistry()
		// Detected providers but with empty catalogs (stubCatalogProvider with nil catalog)
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true},
			catalog:      nil,
		})
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "codex", models: []string{"codex"}, hasCLI: true},
			catalog:      nil,
		})

		mc := r.CatalogDefaultModels()
		if mc != (config.ModelConfig{}) {
			t.Errorf("expected empty defaults, got %+v", mc)
		}
	})

	t.Run("partial_catalog", func(t *testing.T) {
		r := llm.NewRegistry()
		// Claude has a real catalog
		r.Register(&stubCatalogProvider{
			stubProvider: stubProvider{name: "claude", models: []string{"opus", "sonnet", "haiku"}, hasCLI: true},
			catalog:      claudeCatalog,
		})
		// Codex is detected but has no catalog — bare stubProvider (no CatalogProvider)
		r.Register(&stubProvider{name: "codex", models: []string{"codex-model"}, hasCLI: true})

		mc := r.CatalogDefaultModels()

		// Multi-provider detected, so prefixes are used
		// Research prefers cost-efficient claude defaults → "claude:sonnet"
		if mc.Research != "claude:sonnet" {
			t.Errorf("research: got %q, want %q", mc.Research, "claude:sonnet")
		}
		// Review prefers codex; codex has no catalog, so synthetic entry for "codex-model"
		// has no category → MostCapableFrom returns it (rank 0, but still the first/only).
		if mc.Review != "codex:codex-model" {
			t.Errorf("review: got %q, want %q", mc.Review, "codex:codex-model")
		}
		// Chat prefers claude balanced → "claude:sonnet"
		if mc.Utilities != "claude:sonnet" {
			t.Errorf("chat: got %q, want %q", mc.Utilities, "claude:sonnet")
		}
	})
}

func TestRegistry_EligibleModelsForPhase(t *testing.T) {
	r := llm.NewRegistry()
	claude := &stubCatalogProvider{
		stubProvider: stubProvider{name: "claude", models: []string{"opus", "sonnet", "haiku"}, hasCLI: true},
		catalog: []llm.ModelInfo{
			{ID: "opus", Category: "capable", Aliases: []string{"opus[1m]"}},
			{ID: "sonnet", Category: "balanced"},
			{ID: "haiku", Category: "cheap"},
		},
	}
	codex := &stubCatalogProvider{
		stubProvider: stubProvider{name: "codex", models: []string{"codex", "gpt-5.4", "gpt-5.4-mini"}, hasCLI: true},
		catalog: []llm.ModelInfo{
			{ID: "codex", Category: "capable"},
			{ID: "gpt-5.4", Category: "capable"},
			{ID: "gpt-5.4-mini", Category: "balanced"},
		},
	}
	r.Register(claude)
	r.Register(codex)

	t.Run("research includes cost-efficient default and capable options", func(t *testing.T) {
		result := r.EligibleModelsForPhase(llm.PhaseResearch)
		// claude: sonnet is recommended; opus remains available; cheap models are excluded.
		if got := result["claude"]; !slices.Contains(got, "sonnet") || !slices.Contains(got, "opus") {
			t.Errorf("claude research: got %v, want sonnet and opus", got)
		}
		if slices.Contains(result["claude"], "haiku") {
			t.Error("claude research should not include haiku")
		}
		// Aliases are no longer included in filtered results
		if slices.Contains(result["claude"], "opus[1m]") {
			t.Error("claude research should not include aliases")
		}
		// codex: gpt-5.4 is the recommended default; balanced mini remains available.
		if got := result["codex"]; !slices.Contains(got, "gpt-5.4") || !slices.Contains(got, "gpt-5.4-mini") {
			t.Errorf("codex research: got %v, want gpt-5.4 and gpt-5.4-mini", got)
		}
	})

	t.Run("implementation capable+balanced", func(t *testing.T) {
		result := r.EligibleModelsForPhase(llm.PhaseImplementation)
		if got := result["claude"]; !slices.Contains(got, "opus") || !slices.Contains(got, "sonnet") {
			t.Errorf("claude impl: got %v, want opus and sonnet", got)
		}
		if slices.Contains(result["claude"], "haiku") {
			t.Error("claude impl should not include haiku")
		}
		if got := result["codex"]; !slices.Contains(got, "gpt-5.4-mini") {
			t.Errorf("codex impl: got %v, want gpt-5.4-mini included", got)
		}
	})

	t.Run("chat balanced+cheap", func(t *testing.T) {
		result := r.EligibleModelsForPhase(llm.PhaseChat)
		if got := result["claude"]; !slices.Contains(got, "sonnet") || !slices.Contains(got, "haiku") {
			t.Errorf("claude chat: got %v, want sonnet and haiku", got)
		}
		if slices.Contains(result["claude"], "opus") {
			t.Error("claude chat should not include opus")
		}
	})
}
