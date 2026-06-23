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
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// phaseCatalogFields is the canonical ordered list of phase-role field names
// consumed by both the wizard's inline catalog block and BuildPhaseModelCatalog.
var phaseCatalogFields = []string{"Research", "Planning", "Implementation", "Review", "KB Build"}

// phaseCatalogRoleToField maps llm.PhaseRole to catalog field name. The ground
// truth for this mapping lives in AppModel.transitionToWizard (app.go); the
// helper mirrors it while the wizard and edit-config overlay share the catalog.
var phaseCatalogRoleToField = map[llm.PhaseRole]string{
	llm.PhaseResearch:       "Research",
	llm.PhasePlanning:       "Planning",
	llm.PhaseImplementation: "Implementation",
	llm.PhaseReview:         "Review",
	llm.PhaseKBBuild:        "KB Build",
}

// PhaseModelCatalog bundles the per-phase-role model discovery that both
// the wizard's Review step and the EditConfig overlay consume. Built once
// at modal-open time via BuildPhaseModelCatalog and treated as immutable.
type PhaseModelCatalog struct {
	// ProviderModels maps provider name → ordered model IDs (all categories).
	ProviderModels map[string][]string
	// ProviderOrder is the display order of provider names.
	ProviderOrder []string
	// PhaseDefaults maps field name ("Research", "Planning",
	// "Implementation", "Review", "KB Build") → recommended model ID.
	PhaseDefaults map[string]string
	// PhaseProviderModels maps field name → provider → eligible model IDs
	// filtered by role category.
	PhaseProviderModels map[string]map[string][]string
	// Fields is the canonical ordered list of phase-role field names. Always
	// {"Research", "Planning", "Implementation", "Review", "KB Build"}.
	Fields []string
}

// BuildPhaseModelCatalog builds a PhaseModelCatalog from the current
// registry state. Mirrors the block currently inlined in
// AppModel.transitionToWizard; both stay in shape-parity (guarded by
// phase_catalog_test.go).
//
// The `defaults` parameter is accepted for forward compatibility.
//
// Re-entrancy + crash recovery: pure function; no persisted state and no
// side effects, so reentering with identical registry state yields an equal
// value. Process crash is irrelevant because there is no mutation to recover.
func BuildPhaseModelCatalog(reg *llm.Registry, _ config.DefaultsConfig) PhaseModelCatalog {
	cat := PhaseModelCatalog{
		Fields:              append([]string(nil), phaseCatalogFields...),
		ProviderModels:      map[string][]string{},
		PhaseDefaults:       map[string]string{},
		PhaseProviderModels: map[string]map[string][]string{},
	}
	if reg == nil {
		return cat
	}
	for _, p := range reg.DetectedProviders() {
		name := p.Name()
		if models := p.AvailableModels(); len(models) > 0 {
			cat.ProviderModels[name] = models
			cat.ProviderOrder = append(cat.ProviderOrder, name)
		}
	}
	if len(cat.ProviderModels) == 0 {
		if all := reg.AvailableModels(); len(all) > 0 {
			cat.ProviderModels["default"] = all
			cat.ProviderOrder = []string{"default"}
		}
	}
	catalogDefaults := reg.CatalogDefaultModels()
	cat.PhaseDefaults["Research"] = catalogDefaults.Research
	cat.PhaseDefaults["Planning"] = catalogDefaults.Planning
	cat.PhaseDefaults["Implementation"] = catalogDefaults.Implementation
	cat.PhaseDefaults["Review"] = catalogDefaults.Review
	cat.PhaseDefaults["KB Build"] = catalogDefaults.KBBuild
	for role, field := range phaseCatalogRoleToField {
		phaseEligible := reg.EligibleModelsForPhase(role)
		if len(phaseEligible) > 0 {
			cat.PhaseProviderModels[field] = phaseEligible
		}
	}
	return cat
}

// ModelOptionsForField returns the provider-flattened model list for a phase
// field, falling back to all models when the phase-specific list is empty.
// Same semantics as WizardModel.modelOptionsForField.
func (c PhaseModelCatalog) ModelOptionsForField(field string) []string {
	if pm, ok := c.PhaseProviderModels[field]; ok {
		var opts []string
		for _, prov := range c.ProviderOrder {
			opts = append(opts, pm[prov]...)
		}
		if len(opts) > 0 {
			return opts
		}
	}
	return c.AllModels()
}

// ProviderGroupsForField returns provider-grouped model lists for a phase
// field; same shape as WizardModel.modelProviderGroups. Falls back to a
// single "Available" group when no phase-specific groups exist.
func (c PhaseModelCatalog) ProviderGroupsForField(field string) []modelProviderGroup {
	providerModels := c.providerModelsForField(field)
	var groups []modelProviderGroup
	for _, prov := range c.ProviderOrder {
		if models := providerModels[prov]; len(models) > 0 {
			groups = append(groups, modelProviderGroup{Name: prov, Models: models})
		}
	}
	if len(groups) > 0 {
		return groups
	}
	if opts := c.ModelOptionsForField(field); len(opts) > 0 {
		return []modelProviderGroup{{Name: "Available", Models: opts}}
	}
	return nil
}

// AllModels returns the concatenation of ProviderModels in ProviderOrder.
// Matches the flattening behavior of WizardModel.allModels.
func (c PhaseModelCatalog) AllModels() []string {
	var out []string
	for _, prov := range c.ProviderOrder {
		out = append(out, c.ProviderModels[prov]...)
	}
	return out
}

// MatchesModelValue reports whether a bare option opt within provider group
// `provider` corresponds to the (possibly provider-prefixed) model string
// value. Per-provider option lists carry bare backend ids, while a recommended
// default (CatalogDefaultModels) or a persisted selection may carry the routing
// prefix when multiple providers are detected. Matching both the bare id and
// the "<provider>:<opt>" form lets the picker mark the recommended default and
// the current selection regardless of which form the value takes — including
// OpenCode slash-form ids such as "opencode:anthropic/claude-sonnet-4-5[200K]".
func (c PhaseModelCatalog) MatchesModelValue(provider, opt, value string) bool {
	if value == "" || opt == "" {
		return false
	}
	return value == opt || value == provider+":"+opt
}

// FlatOptionsForField returns the flattened option list for field together with
// the provider group each option belongs to. The two slices are index-parallel
// and follow the same order as ModelOptionsForField / ProviderGroupsForField, so
// callers iterating a flat cursor can still apply provider-aware MatchesModelValue
// against a possibly provider-prefixed selection.
func (c PhaseModelCatalog) FlatOptionsForField(field string) (opts []string, providers []string) {
	for _, group := range c.ProviderGroupsForField(field) {
		for _, opt := range group.Models {
			opts = append(opts, opt)
			providers = append(providers, group.Name)
		}
	}
	return opts, providers
}

// ClampModelValue keeps value when it is still an eligible option for field —
// matching either a bare backend id or its "<provider>:<id>" routing form, so a
// multi-provider default persisted as "opencode:anthropic/claude-sonnet-4-5[200K]"
// is preserved rather than silently replaced. When value is not eligible it
// returns the first eligible option; an empty option set leaves value unchanged.
func (c PhaseModelCatalog) ClampModelValue(field, value string) string {
	var first string
	for _, group := range c.ProviderGroupsForField(field) {
		for _, opt := range group.Models {
			if first == "" {
				first = opt
			}
			if c.MatchesModelValue(group.Name, opt, value) {
				return value
			}
		}
	}
	if first == "" {
		return value
	}
	return first
}

func (c PhaseModelCatalog) providerModelsForField(field string) map[string][]string {
	if pm, ok := c.PhaseProviderModels[field]; ok && len(pm) > 0 {
		return pm
	}
	return c.ProviderModels
}
