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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// phaseCatalogFields is the canonical ordered list of phase-role field names
// consumed by both the wizard's inline catalog block and BuildPhaseModelCatalog.
var phaseCatalogFields = []string{"Clarify", "Research", "Planning", "Implementation", "Review", "KB Build"}

// globalModelFields is phaseCatalogFields plus Utilities: the model used for
// AMA chat and other workspace-wide utility calls. No single feature owns
// that role, so it is intentionally absent from phaseCatalogFields and only
// surfaced by BuildWorkspaceModelCatalog.
var globalModelFields = []string{"Clarify", "Research", "Planning", "Implementation", "Review", "Utilities", "KB Build"}

// globalCatalogRoleToField maps llm.PhaseRole to catalog field name,
// including the Utilities role. BuildPhaseModelCatalog loops over this
// expanded map so the catalog always carries eligible-model data for
// Utilities; a caller's Fields list (phaseCatalogFields vs
// globalModelFields) is what actually decides whether its UI shows that row.
var globalCatalogRoleToField = map[llm.PhaseRole]string{
	llm.PhaseInquiry:        "Clarify",
	llm.PhaseResearch:       "Research",
	llm.PhasePlanning:       "Planning",
	llm.PhaseImplementation: "Implementation",
	llm.PhaseReview:         "Review",
	llm.PhaseChat:           "Utilities",
	llm.PhaseKBBuild:        "KB Build",
}

type PhaseModelEntry struct {
	Agent         string
	ModelID       string
	DisplayName   string
	FullID        string
	ContextWindow int
	Category      string
	Recommended   bool
	Aliases       []string
}

// PhaseModelCatalog bundles the per-phase-role model discovery that both
// the wizard's Review step and the EditConfig overlay consume. Built once
// at modal-open time via BuildPhaseModelCatalog and treated as immutable.
type PhaseModelCatalog struct {
	// ProviderModels maps provider name → ordered model IDs (all categories).
	ProviderModels map[string][]string
	// ProviderModelInfos maps provider name → ordered model metadata entries
	// for all available models.
	ProviderModelInfos map[string][]llm.ModelInfo
	// ProviderOrder is the display order of provider names.
	ProviderOrder []string
	// PhaseDefaults maps field name → recommended model ID. For
	// BuildPhaseModelCatalog, includes all 7 roles (including Utilities); for
	// BuildWorkspaceModelCatalog, the Fields list is what decides visibility.
	PhaseDefaults map[string]string
	// PhaseProviderModels maps field name → provider → eligible model IDs
	// filtered by role category.
	PhaseProviderModels map[string]map[string][]string
	// PhaseProviderModelInfos maps field name → provider → eligible model
	// metadata entries filtered by role category.
	PhaseProviderModelInfos map[string]map[string][]llm.ModelInfo
	// Fields is the canonical ordered list of phase-role field names.
	// BuildPhaseModelCatalog sets phaseCatalogFields (6 entries, no
	// Utilities); BuildWorkspaceModelCatalog overrides it to
	// globalModelFields (7 entries, includes Utilities).
	Fields []string
}

// BuildPhaseModelCatalog builds a PhaseModelCatalog from the current
// registry state. The wizard and edit-config overlay share this immutable
// view of model availability and per-phase eligibility.
//
// The `defaults` parameter is accepted for forward compatibility.
//
// Re-entrancy + crash recovery: pure function; no persisted state and no
// side effects, so reentering with identical registry state yields an equal
// value. Process crash is irrelevant because there is no mutation to recover.
func BuildPhaseModelCatalog(reg *llm.Registry, _ config.DefaultsConfig) PhaseModelCatalog {
	cat := PhaseModelCatalog{
		Fields:                  append([]string(nil), phaseCatalogFields...),
		ProviderModels:          map[string][]string{},
		ProviderModelInfos:      map[string][]llm.ModelInfo{},
		PhaseDefaults:           map[string]string{},
		PhaseProviderModels:     map[string]map[string][]string{},
		PhaseProviderModelInfos: map[string]map[string][]llm.ModelInfo{},
	}
	if reg == nil {
		return cat
	}
	for _, p := range reg.DetectedProviders() {
		name := p.Name()
		models := p.AvailableModels()
		if len(models) > 0 {
			cat.ProviderModels[name] = models
			cat.ProviderOrder = append(cat.ProviderOrder, name)
		}
		var catalogInfos []llm.ModelInfo
		if cp, ok := p.(llm.CatalogProvider); ok {
			if infos := cp.ModelCatalog(); len(infos) > 0 {
				catalogInfos = cloneModelInfos(infos)
			}
		}
		for _, id := range models {
			info := modelInfoFromList(catalogInfos, id)
			cat.ProviderModelInfos[name] = append(cat.ProviderModelInfos[name], info)
		}
	}
	if len(cat.ProviderModels) == 0 {
		if all := reg.AvailableModels(); len(all) > 0 {
			cat.ProviderModels["default"] = all
			for _, id := range all {
				cat.ProviderModelInfos["default"] = append(cat.ProviderModelInfos["default"], cloneModelInfo(llm.ModelInfo{ID: id}))
			}
			cat.ProviderOrder = []string{"default"}
		}
	}
	catalogDefaults := reg.CatalogDefaultModels()
	cat.PhaseDefaults["Clarify"] = catalogDefaults.Inquiry
	cat.PhaseDefaults["Research"] = catalogDefaults.Research
	cat.PhaseDefaults["Planning"] = catalogDefaults.Planning
	cat.PhaseDefaults["Implementation"] = catalogDefaults.Implementation
	cat.PhaseDefaults["Review"] = catalogDefaults.Review
	cat.PhaseDefaults["Utilities"] = catalogDefaults.Utilities
	cat.PhaseDefaults["KB Build"] = catalogDefaults.KBBuild
	for role, field := range globalCatalogRoleToField {
		phaseEligible := reg.EligibleModelsForPhase(role)
		if len(phaseEligible) > 0 {
			cat.PhaseProviderModels[field] = phaseEligible
			cat.PhaseProviderModelInfos[field] = map[string][]llm.ModelInfo{}
			for provider, ids := range phaseEligible {
				for _, id := range ids {
					cat.PhaseProviderModelInfos[field][provider] = append(
						cat.PhaseProviderModelInfos[field][provider],
						cat.modelInfoForProvider(provider, id),
					)
				}
			}
		}
	}
	return cat
}

// BuildWorkspaceModelCatalog builds a PhaseModelCatalog scoped to the
// workspace defaults editor: identical provider/model data to
// BuildPhaseModelCatalog, but Fields includes "Utilities" since the
// workspace editor (unlike the per-feature editor) owns that model role.
func BuildWorkspaceModelCatalog(reg *llm.Registry, defaults config.DefaultsConfig) PhaseModelCatalog {
	cat := BuildPhaseModelCatalog(reg, defaults)
	cat.Fields = append([]string(nil), globalModelFields...)
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

func (c PhaseModelCatalog) ModelEntriesForField(field string) []PhaseModelEntry {
	var entries []PhaseModelEntry
	for _, group := range c.ProviderEntryGroupsForField(field) {
		for _, entry := range group.Models {
			entries = append(entries, entry)
		}
	}
	return entries
}

type modelEntryGroup struct {
	Name   string
	Models []PhaseModelEntry
}

func (c PhaseModelCatalog) ProviderEntryGroupsForField(field string) []modelEntryGroup {
	defaultValue := c.PhaseDefaults[field]
	var groups []modelEntryGroup
	for _, provider := range c.ProviderOrder {
		ids := c.orderedProviderModelIDsForField(field, provider)
		if len(ids) == 0 {
			continue
		}
		group := modelEntryGroup{Name: provider}
		for _, id := range ids {
			info := c.modelInfoForEntryGroup(field, provider, id)
			entry := c.entryFromInfo(provider, info)
			entry.Recommended = c.MatchesModelValue(entry.Agent, entry.ModelID, defaultValue)
			group.Models = append(group.Models, entry)
		}
		groups = append(groups, group)
	}
	if len(groups) > 0 {
		return groups
	}
	for _, stringGroup := range c.ProviderGroupsForField(field) {
		group := modelEntryGroup{Name: stringGroup.Name}
		for _, id := range stringGroup.Models {
			info := c.modelInfoForEntryGroup(field, stringGroup.Name, id)
			entry := c.entryFromInfo(stringGroup.Name, info)
			entry.Recommended = c.MatchesModelValue(entry.Agent, entry.ModelID, defaultValue)
			group.Models = append(group.Models, entry)
		}
		groups = append(groups, group)
	}
	return groups
}

func (c PhaseModelCatalog) orderedProviderModelIDsForField(field, provider string) []string {
	all := c.ProviderModels[provider]
	if len(all) == 0 {
		return nil
	}
	eligible := c.PhaseProviderModels[field][provider]
	if len(eligible) == 0 {
		return append([]string(nil), all...)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(all))
	for _, id := range eligible {
		if id == "" || seen[id] {
			continue
		}
		out = append(out, id)
		seen[id] = true
	}
	for _, id := range all {
		if id == "" || seen[id] {
			continue
		}
		out = append(out, id)
		seen[id] = true
	}
	return out
}

func (c PhaseModelCatalog) EntriesForFieldAndAgent(field, agent string) []PhaseModelEntry {
	for _, group := range c.ProviderEntryGroupsForField(field) {
		if group.Name == agent {
			return group.Models
		}
	}
	return nil
}

func (c PhaseModelCatalog) RecommendedEntryForAgent(field, agent string) (PhaseModelEntry, bool) {
	entries := c.EntriesForFieldAndAgent(field, agent)
	if len(entries) == 0 {
		return PhaseModelEntry{}, false
	}
	for _, entry := range entries {
		if c.MatchesModelValue(agent, entry.ModelID, c.PhaseDefaults[field]) {
			entry.Recommended = true
			return entry, true
		}
	}
	// ok=true means a usable entry exists for this agent; Recommended records
	// whether it matched the phase default.
	return entries[0], true
}

func (c PhaseModelCatalog) SelectionValue(entry PhaseModelEntry) string {
	if entry.ModelID == "" {
		return ""
	}
	if len(c.ProviderOrder) <= 1 || entry.Agent == "" || entry.Agent == "default" || entry.Agent == "Available" {
		return entry.ModelID
	}
	return entry.Agent + ":" + entry.ModelID
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
// the current selection regardless of which form the value takes, including
// slash-form backend ids such as "gateway:vendor/model[200K]".
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
// multi-provider default persisted as "gateway:vendor/model[200K]"
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

func (c PhaseModelCatalog) modelInfoForProvider(provider, id string) llm.ModelInfo {
	if info, ok := c.findModelInfo(c.ProviderModelInfos[provider], id); ok {
		return info
	}
	return cloneModelInfo(llm.ModelInfo{ID: id})
}

func (c PhaseModelCatalog) findModelInfo(infos []llm.ModelInfo, id string) (llm.ModelInfo, bool) {
	if info, ok := findModelInfo(infos, id); ok {
		return info, true
	}
	return llm.ModelInfo{}, false
}

func findModelInfo(infos []llm.ModelInfo, id string) (llm.ModelInfo, bool) {
	for _, info := range infos {
		if modelInfoMatches(info, id) {
			return cloneModelInfo(info), true
		}
	}
	return llm.ModelInfo{}, false
}

func (c PhaseModelCatalog) modelInfoMatches(info llm.ModelInfo, value string) bool {
	return modelInfoMatches(info, value)
}

func modelInfoMatches(info llm.ModelInfo, value string) bool {
	if strings.EqualFold(info.ID, value) {
		return true
	}
	for _, alias := range info.Aliases {
		if strings.EqualFold(alias, value) {
			return true
		}
	}
	return false
}

func modelInfoFromList(infos []llm.ModelInfo, id string) llm.ModelInfo {
	if info, ok := findModelInfo(infos, id); ok {
		return info
	}
	return cloneModelInfo(llm.ModelInfo{ID: id})
}

func (c PhaseModelCatalog) entryFromInfo(agent string, info llm.ModelInfo) PhaseModelEntry {
	info = normalizeCatalogModelInfo(info)
	display := info.DisplayName
	if display == "" {
		display = info.ID
	}
	return PhaseModelEntry{
		Agent:         agent,
		ModelID:       info.ID,
		DisplayName:   display,
		FullID:        info.ID,
		ContextWindow: info.ContextWindow,
		Category:      info.Category,
		Aliases:       append([]string(nil), info.Aliases...),
	}
}

func normalizeCatalogModelInfo(info llm.ModelInfo) llm.ModelInfo {
	text := strings.ToLower(strings.Join(append([]string{info.ID, info.DisplayName}, info.Aliases...), " "))
	if info.Category == "" {
		info.Category = inferCatalogCategory(text)
	}
	if info.ContextWindow <= 0 {
		info.ContextWindow = inferCatalogContextWindow(text)
	}
	return info
}

func inferCatalogCategory(text string) string {
	switch {
	case containsAnyCatalogToken(text, []string{"nano", "haiku", "flash", "lite", "tiny", "embed"}):
		return "cheap"
	case containsAnyCatalogToken(text, []string{"mini", "small"}):
		return "balanced"
	case containsAnyCatalogToken(text, []string{"opus", "gpt-5", "-pro", "ultra", "-max", "405b", "reasoner", "deepseek-r"}):
		return "capable"
	case containsAnyCatalogToken(text, []string{"sonnet", "gpt-4", "claude-3", "gemini", "llama", "mistral", "qwen", "gemma", "glm", "grok", "deepseek", "codex", "command"}):
		return "balanced"
	default:
		return ""
	}
}

func inferCatalogContextWindow(text string) int {
	switch {
	case strings.Contains(text, "glm-5p2"), strings.Contains(text, "glm-5.2"), strings.Contains(text, "glm 5.2"):
		return 1_000_000
	default:
		return 0
	}
}

func containsAnyCatalogToken(text string, tokens []string) bool {
	for _, token := range tokens {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}

func (c PhaseModelCatalog) modelInfoForEntryGroup(field, provider, id string) llm.ModelInfo {
	if provider != "Available" {
		if info, ok := c.findModelInfo(c.PhaseProviderModelInfos[field][provider], id); ok {
			return info
		}
		return c.modelInfoForProvider(provider, id)
	}
	if info, ok := c.findModelInfo(c.PhaseProviderModelInfos[field][provider], id); ok {
		return info
	}
	for _, agent := range c.ProviderOrder {
		if info, ok := c.findModelInfo(c.PhaseProviderModelInfos[field][agent], id); ok {
			return info
		}
		if info, ok := c.findModelInfo(c.ProviderModelInfos[agent], id); ok {
			return info
		}
	}
	return cloneModelInfo(llm.ModelInfo{ID: id})
}

func (c PhaseModelCatalog) providerModelsForField(field string) map[string][]string {
	if pm, ok := c.PhaseProviderModels[field]; ok && len(pm) > 0 {
		return pm
	}
	return c.ProviderModels
}

func cloneModelInfos(infos []llm.ModelInfo) []llm.ModelInfo {
	if len(infos) == 0 {
		return nil
	}
	out := make([]llm.ModelInfo, len(infos))
	for i, info := range infos {
		out[i] = cloneModelInfo(info)
	}
	return out
}

func cloneModelInfo(info llm.ModelInfo) llm.ModelInfo {
	info.Aliases = append([]string(nil), info.Aliases...)
	return info
}
