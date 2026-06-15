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

package llm

import (
	"fmt"
	"strings"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
)

// Registry holds all registered LLM providers and routes model strings
// to the appropriate provider.
type Registry struct {
	mu                   sync.RWMutex
	providers            []LLMProvider
	activeProviderFilter map[string]bool
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a provider to the registry. Providers are checked in
// registration order when resolving a model string.
func (r *Registry) Register(p LLMProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = append(r.providers, p)
}

// RestrictToProviders limits active model routing and detected-provider lists
// to the supplied providers. Startup uses this after readiness checks so
// installed-but-unauthenticated CLIs do not appear in defaults or model
// selection.
func (r *Registry) RestrictToProviders(providers []LLMProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	filter := make(map[string]bool, len(providers))
	for _, p := range providers {
		if p != nil {
			filter[p.Name()] = true
		}
	}
	r.activeProviderFilter = filter
}

func (r *Registry) providerActiveLocked(p LLMProvider) bool {
	if r.activeProviderFilter == nil {
		return true
	}
	return r.activeProviderFilter[p.Name()]
}

// ForModel returns the provider that handles the given model string.
// Returns an error if no provider matches.
func (r *Registry) ForModel(model string) (LLMProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.providers {
		if !r.providerActiveLocked(p) {
			continue
		}
		if p.MatchesModel(model) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no provider registered for model %q", model)
}

// ByName returns the provider with the given name, or nil if not found.
func (r *Registry) ByName(name string) LLMProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.providers {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// All returns all registered providers.
func (r *Registry) All() []LLMProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]LLMProvider, len(r.providers))
	copy(out, r.providers)
	return out
}

// AvailableModels returns all models from all providers whose CLI is detected.
func (r *Registry) AvailableModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var models []string
	for _, p := range r.providers {
		if p.DetectCLI() && r.providerActiveLocked(p) {
			models = append(models, p.AvailableModels()...)
		}
	}
	return models
}

// DetectedProviders returns providers whose CLI binary is available in PATH.
func (r *Registry) DetectedProviders() []LLMProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var detected []LLMProvider
	for _, p := range r.providers {
		if p.DetectCLI() && r.providerActiveLocked(p) {
			detected = append(detected, p)
		}
	}
	return detected
}

// PromptAdapterForModel returns the PromptAdapter for the given model.
// Returns an error if no provider matches or the provider doesn't implement PromptAdapter.
func (r *Registry) PromptAdapterForModel(model string) (PromptAdapter, error) {
	p, err := r.ForModel(model)
	if err != nil {
		return nil, err
	}
	pa, ok := p.(PromptAdapter)
	if !ok {
		return nil, fmt.Errorf("provider %q does not implement PromptAdapter", p.Name())
	}
	return pa, nil
}

// CostCalculatorForModel returns the CostCalculator for the given model.
// Returns an error if no provider matches or the provider doesn't implement CostCalculator.
func (r *Registry) CostCalculatorForModel(model string) (CostCalculator, error) {
	p, err := r.ForModel(model)
	if err != nil {
		return nil, err
	}
	cc, ok := p.(CostCalculator)
	if !ok {
		return nil, fmt.Errorf("provider %q does not implement CostCalculator", p.Name())
	}
	return cc, nil
}

// ResolveModel resolves a model string to a provider and the bare model name.
// Accepts both "provider:model" (explicit prefix) and "model" (implicit lookup).
// Returns (provider, strippedModel, error).
func (r *Registry) ResolveModel(model string) (LLMProvider, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if model == "" {
		return nil, "", fmt.Errorf("no provider found for model %q", model)
	}

	// Split on first colon for explicit provider prefix
	if idx := strings.IndexByte(model, ':'); idx > 0 {
		providerName := model[:idx]
		bareModel := model[idx+1:]
		for _, p := range r.providers {
			if p.Name() == providerName {
				if !r.providerActiveLocked(p) {
					return nil, "", fmt.Errorf("provider %q is not available", providerName)
				}
				if bareModel == "" {
					return p, "", nil
				}
				if canonical, ok := canonicalModelForProvider(p, bareModel); ok {
					return p, canonical, nil
				}
				return p, bareModel, nil
			}
		}
		return nil, "", fmt.Errorf("unknown provider %q in model %q", providerName, model)
	}

	// Bare model name — search providers in registration order via MatchesModel
	for _, p := range r.providers {
		if !r.providerActiveLocked(p) {
			continue
		}
		if canonical, ok := canonicalModelForProvider(p, model); ok {
			return p, canonical, nil
		}
		if p.MatchesModel(model) {
			return p, model, nil
		}
	}
	return nil, "", fmt.Errorf("no provider found for model %q", model)
}

func canonicalModelForProvider(p LLMProvider, model string) (string, bool) {
	if model == "" {
		return "", false
	}
	if cp, ok := p.(CatalogProvider); ok {
		if cat := cp.ModelCatalog(); len(cat) > 0 {
			for _, entry := range cat {
				if strings.EqualFold(entry.ID, model) {
					return entry.ID, true
				}
				for _, alias := range entry.Aliases {
					if strings.EqualFold(alias, model) {
						return entry.ID, true
					}
				}
			}
			return "", false
		}
	}
	for _, candidate := range p.AvailableModels() {
		if strings.EqualFold(candidate, model) {
			return candidate, true
		}
	}
	return "", false
}

// DefaultModels computes the recommended model for each phase role from the
// discovered model catalogs. When multiple providers are detected, model names
// are returned in "provider:model" format; with a single detected provider,
// bare names are used.
func (r *Registry) DefaultModels() map[PhaseRole]string {
	mc := r.CatalogDefaultModels()
	result := make(map[PhaseRole]string, 6)
	if mc.Research != "" {
		result[PhaseResearch] = mc.Research
	}
	if mc.Planning != "" {
		result[PhasePlanning] = mc.Planning
	}
	if mc.Implementation != "" {
		result[PhaseImplementation] = mc.Implementation
	}
	if mc.Review != "" {
		result[PhaseReview] = mc.Review
	}
	if mc.Utilities != "" {
		result[PhaseChat] = mc.Utilities
	}
	if mc.KBBuild != "" {
		result[PhaseKBBuild] = mc.KBBuild
	}
	return result
}

// --- Catalog-based methods ---

// catalogForProvider returns the ModelInfo catalog for a provider.
// If the provider implements CatalogProvider, returns its catalog.
// Otherwise, creates synthetic ModelInfo entries from AvailableModels().
func catalogForProvider(p LLMProvider) []ModelInfo {
	if cp, ok := p.(CatalogProvider); ok {
		if cat := cp.ModelCatalog(); len(cat) > 0 {
			return cat
		}
	}
	// Synthetic fallback
	models := p.AvailableModels()
	infos := make([]ModelInfo, len(models))
	for i, m := range models {
		infos[i] = ModelInfo{ID: m}
	}
	return infos
}

// AllModels returns all ModelInfo from all detected providers' catalogs.
// Providers not implementing CatalogProvider or with empty catalogs contribute
// synthetic ModelInfo entries from AvailableModels() with zero metadata.
func (r *Registry) AllModels() []ModelInfo {
	detected := r.DetectedProviders()
	var all []ModelInfo
	for _, p := range detected {
		all = append(all, catalogForProvider(p)...)
	}
	return all
}

// ModelsForProvider returns the catalog models for a specific provider.
// Returns nil if the provider is not found or not detected.
func (r *Registry) ModelsForProvider(name string) []ModelInfo {
	p := r.ByName(name)
	r.mu.RLock()
	active := p != nil && r.providerActiveLocked(p)
	r.mu.RUnlock()
	if p == nil || !p.DetectCLI() || !active {
		return nil
	}
	return catalogForProvider(p)
}

// MostCapableModel returns the ID of the most capable model.
// If providerHint is non-empty, prefers that provider's catalog.
// Falls back to all providers if the hint doesn't match or has no catalog.
func (r *Registry) MostCapableModel(providerHint string) string {
	if providerHint != "" {
		if models := r.ModelsForProvider(providerHint); len(models) > 0 {
			if m, ok := MostCapableFrom(models); ok {
				return m.ID
			}
		}
	}
	if m, ok := MostCapableFrom(r.AllModels()); ok {
		return m.ID
	}
	// Final fallback: first available model
	if models := r.AvailableModels(); len(models) > 0 {
		return models[0]
	}
	return ""
}

// CheapestModel returns the ID of the cheapest model across all detected providers.
func (r *Registry) CheapestModel() string {
	if m, ok := CheapestFrom(r.AllModels()); ok {
		return m.ID
	}
	// Fallback: last available model
	if models := r.AvailableModels(); len(models) > 0 {
		return models[len(models)-1]
	}
	return ""
}

// BalancedModel returns the ID of a balanced model across all detected providers.
func (r *Registry) BalancedModel() string {
	if m, ok := BalancedFrom(r.AllModels()); ok {
		return m.ID
	}
	if models := r.AvailableModels(); len(models) > 0 {
		return models[0]
	}
	return ""
}

// LargestContextModel returns the ID of the model with the largest context window.
func (r *Registry) LargestContextModel() string {
	if m, ok := LargestContextFrom(r.AllModels()); ok {
		return m.ID
	}
	if models := r.AvailableModels(); len(models) > 0 {
		return models[0]
	}
	return ""
}

// providerPreference maps phase roles to preferred provider names.
// Research/Planning/Implementation/Chat prefer "claude"; Review prefers "codex".
var providerPreference = map[PhaseRole]string{
	PhaseResearch:       "claude",
	PhasePlanning:       "claude",
	PhaseImplementation: "claude",
	PhaseReview:         "codex",
	PhaseChat:           "claude",
	PhaseKBBuild:        "claude",
}

// categoryForRole maps phase roles to the default model-selection category.
// Research and KB build are intentionally cost-efficient defaults: they are
// token-heavy phases that fan out through sub-agents, so a balanced model is
// usually a better starting point than the largest context window.
var categoryForRole = map[PhaseRole]string{
	PhaseResearch:       "balanced",
	PhasePlanning:       "capable",
	PhaseImplementation: "capable",
	PhaseReview:         "capable",
	PhaseChat:           "balanced",
	PhaseKBBuild:        "balanced",
}

var preferredModelHintsByRoleProvider = map[PhaseRole]map[string][]string{
	PhaseResearch: {
		"claude": {"sonnet[200K]", "sonnet"},
		"codex":  {"gpt-5.4[272K]", "gpt-5.4"},
	},
	PhaseKBBuild: {
		"claude": {"sonnet[200K]", "sonnet"},
		"codex":  {"gpt-5.4[272K]", "gpt-5.4"},
	},
}

func selectHintedModel(models []ModelInfo, hints []string) (ModelInfo, bool) {
	for _, hint := range hints {
		for _, model := range models {
			if strings.EqualFold(model.ID, hint) {
				return model, true
			}
			for _, alias := range model.Aliases {
				if strings.EqualFold(alias, hint) {
					return model, true
				}
			}
		}
	}
	return ModelInfo{}, false
}

func selectRoleModel(models []ModelInfo, role PhaseRole, provider string) (ModelInfo, bool) {
	if providerHints, ok := preferredModelHintsByRoleProvider[role]; ok {
		if m, ok := selectHintedModel(models, providerHints[provider]); ok {
			return m, true
		}
	}
	category := categoryForRole[role]
	if category == "balanced" {
		return BalancedFrom(models)
	}
	return MostCapableFrom(models)
}

func formatCatalogDefault(provider string, model ModelInfo, multi bool) string {
	if multi {
		return provider + ":" + model.ID
	}
	return model.ID
}

func (r *Registry) catalogDefaultModelsWithProviderOverride(providerOverride string) config.ModelConfig {
	detected := r.DetectedProviders()
	if len(detected) == 0 {
		return config.ModelConfig{}
	}

	hasCatalog := false
	for _, p := range detected {
		if cp, ok := p.(CatalogProvider); ok {
			if cat := cp.ModelCatalog(); len(cat) > 0 {
				hasCatalog = true
				break
			}
		}
	}
	if !hasCatalog {
		return config.ModelConfig{}
	}

	multi := len(detected) > 1

	selectModel := func(role PhaseRole) string {
		preferred := providerPreference[role]
		if providerOverride != "" {
			preferred = providerOverride
		}

		if preferred != "" {
			if models := r.ModelsForProvider(preferred); len(models) > 0 {
				if m, ok := selectRoleModel(models, role, preferred); ok {
					return formatCatalogDefault(preferred, m, multi)
				}
			}
			if providerOverride != "" {
				return ""
			}
		}

		for _, p := range detected {
			cat := catalogForProvider(p)
			if m, ok := selectRoleModel(cat, role, p.Name()); ok {
				return formatCatalogDefault(p.Name(), m, multi)
			}
		}

		return ""
	}

	return config.ModelConfig{
		Research:       selectModel(PhaseResearch),
		Planning:       selectModel(PhasePlanning),
		Implementation: selectModel(PhaseImplementation),
		Review:         selectModel(PhaseReview),
		Utilities:      selectModel(PhaseChat),
		KBBuild:        selectModel(PhaseKBBuild),
	}
}

// CatalogDefaultModelsForProvider returns catalog-driven defaults for a single
// preferred provider only. Roles the provider cannot satisfy are left empty so
// callers can fill them from the general catalog defaults.
func (r *Registry) CatalogDefaultModelsForProvider(name string) config.ModelConfig {
	return r.catalogDefaultModelsWithProviderOverride(name)
}

// CatalogDefaultModels returns a ModelConfig with catalog-driven defaults per phase role.
// Provider preferences: Research/Planning/Implementation/Chat/KBBuild → "claude",
// Review → "codex". Falls back to any detected provider when the preferred
// provider has no eligible model for a role.
func (r *Registry) CatalogDefaultModels() config.ModelConfig {
	return r.catalogDefaultModelsWithProviderOverride("")
}

// eligibleCategoriesForRole maps phase roles to the set of model categories
// that are appropriate for that phase.
var eligibleCategoriesForRole = map[PhaseRole]map[string]bool{
	PhaseResearch:       {"capable": true, "balanced": true},
	PhasePlanning:       {"capable": true},
	PhaseImplementation: {"capable": true, "balanced": true},
	PhaseReview:         {"capable": true, "balanced": true},
	PhaseChat:           {"balanced": true, "cheap": true},
	PhaseKBBuild:        {"capable": true, "balanced": true},
}

// maxModelsPerProvider is the maximum number of models shown per provider
// in the wizard's phase-filtered model list.
const maxModelsPerProvider = 3

// EligibleModelsForPhase returns model IDs grouped by provider that are
// eligible for the given phase role, filtered by category and limited to
// the top N most capable models per provider. Only models with a known
// category are included — models with empty categories (undiscovered) are
// excluded to keep the list clean.
func (r *Registry) EligibleModelsForPhase(role PhaseRole) map[string][]string {
	eligible := eligibleCategoriesForRole[role]
	result := make(map[string][]string)

	for _, p := range r.DetectedProviders() {
		name := p.Name()
		catalog := catalogForProvider(p)

		// Filter to eligible models with known categories.
		var filtered []ModelInfo
		for _, m := range catalog {
			if m.Category != "" && eligible[m.Category] {
				filtered = append(filtered, m)
			}
		}

		// Limit to top N by capability rank, while preserving the role's
		// default recommendation even when that recommendation is balanced and
		// would otherwise be crowded out by capable variants.
		ids := TopModelIDs(filtered, maxModelsPerProvider)
		if recommended, ok := selectRoleModel(catalog, role, name); ok {
			ids = includeRecommendedModelID(ids, recommended.ID)
		}
		if len(ids) > 0 {
			result[name] = ids
		}
	}
	return result
}

func includeRecommendedModelID(ids []string, recommended string) []string {
	if recommended == "" {
		return ids
	}
	for i, id := range ids {
		if id != recommended {
			continue
		}
		if i == 0 {
			return ids
		}
		out := make([]string, 0, len(ids))
		out = append(out, recommended)
		out = append(out, ids[:i]...)
		out = append(out, ids[i+1:]...)
		return out
	}

	out := make([]string, 0, len(ids)+1)
	out = append(out, recommended)
	out = append(out, ids...)
	if len(out) > maxModelsPerProvider {
		return out[:maxModelsPerProvider]
	}
	return out
}
