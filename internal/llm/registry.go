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
	recommendations      map[string][]string
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
	p, _, err := r.ResolveModel(model)
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
				if stripped := stripContextAnnotation(bareModel); stripped != bareModel {
					if canonical, ok := canonicalModelForProvider(p, stripped); ok {
						return p, canonical, nil
					}
				}
				return p, bareModel, nil
			}
		}
		// The prefix is not a registered provider, so this colon may instead be
		// part of a bare catalog id whose model segment carries a colon-form tag
		// (e.g. a backend id like "ollama/llama3.1:8b"). Try resolving
		// the whole string as a bare catalog id before rejecting it; only fall
		// back to the unknown-provider error when no catalog matches.
		if p, canonical, ok := r.resolveBareLocked(model); ok {
			return p, canonical, nil
		}
		return nil, "", fmt.Errorf("unknown provider %q in model %q", providerName, model)
	}

	// Bare model name — search providers in registration order via MatchesModel.
	if p, canonical, ok := r.resolveBareLocked(model); ok {
		return p, canonical, nil
	}
	if stripped := stripContextAnnotation(model); stripped != model {
		if p, canonical, ok := r.resolveBareLocked(stripped); ok {
			return p, canonical, nil
		}
	}
	return nil, "", fmt.Errorf("no provider found for model %q", model)
}

// resolveBareLocked resolves a model string with no explicit provider prefix
// against the active providers' catalogs (canonicalizing ids/aliases) and
// MatchesModel checks, in registration order. It returns the matching provider
// and the canonical model name, or ok=false when nothing matches. The caller
// must hold r.mu.
func (r *Registry) resolveBareLocked(model string) (LLMProvider, string, bool) {
	for _, p := range r.providers {
		if !r.providerActiveLocked(p) {
			continue
		}
		if canonical, ok := canonicalModelForProvider(p, model); ok {
			return p, canonical, true
		}
		if p.MatchesModel(model) {
			return p, model, true
		}
	}
	return nil, "", false
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

func stripContextAnnotation(model string) string {
	open := strings.LastIndexByte(model, '[')
	if open <= 0 || !strings.HasSuffix(model, "]") {
		return model
	}
	context := model[open+1 : len(model)-1]
	if context == "" {
		return model
	}
	for _, r := range context {
		if r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case 'k', 'K', 'm', 'M':
			continue
		default:
			return model
		}
	}
	return strings.TrimSpace(model[:open])
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

func formatCatalogDefault(provider string, model ModelInfo, multi bool) string {
	if multi {
		return provider + ":" + model.ID
	}
	return model.ID
}

// CatalogDefaultModels recommends technically compatible models using explicit
// role preferences and deterministic metadata ordering. Existing configured
// selections are preserved by the startup reconciliation layer.
func (r *Registry) CatalogDefaultModels() config.ModelConfig {
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
		candidates := r.roleCandidates(role)
		if len(candidates) == 0 {
			return ""
		}
		best := candidates[0]
		return formatCatalogDefault(best.provider, best.model, multi)
	}

	return config.ModelConfig{
		Inquiry:        selectModel(PhaseInquiry),
		Research:       selectModel(PhaseResearch),
		Planning:       selectModel(PhasePlanning),
		Implementation: selectModel(PhaseImplementation),
		Review:         selectModel(PhaseReview),
		Utilities:      selectModel(PhaseChat),
		KBBuild:        selectModel(PhaseKBBuild),
	}
}

// EligibleModelsForPhase lists all technically compatible models. Recommended
// entries come first; price labels and unknown metadata never hide a model.
func (r *Registry) EligibleModelsForPhase(role PhaseRole) map[string][]string {
	if role == PhaseAutomaticReview {
		return r.eligibleAutomaticReviewModels()
	}
	result := make(map[string][]string)
	for _, candidate := range r.roleCandidates(role) {
		result[candidate.provider] = append(result[candidate.provider], candidate.model.ID)
	}
	return result
}

func (r *Registry) eligibleAutomaticReviewModels() map[string][]string {
	result := make(map[string][]string)
	for _, p := range r.DetectedProviders() {
		reviewer, ok := p.(NativeToollessReviewer)
		if !ok || !reviewer.SupportsNativeToollessReview() {
			continue
		}
		catalogProvider, ok := p.(CatalogProvider)
		if !ok {
			continue
		}
		catalog := catalogProvider.ModelCatalog()
		if len(catalog) == 0 {
			continue
		}
		seen := make(map[string]bool, len(catalog))
		ids := make([]string, 0, len(catalog))
		for _, model := range catalog {
			if model.ID == "" || seen[model.ID] {
				continue
			}
			seen[model.ID] = true
			ids = append(ids, model.ID)
		}
		if len(ids) > 0 {
			result[p.Name()] = ids
		}
	}
	return result
}
