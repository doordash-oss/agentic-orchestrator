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
	"math"
	"slices"
	"sort"
	"strings"
)

// SetModelRecommendations installs an ordered, model-exact policy. This is
// separate from technical capability discovery and from explicit selections.
func (r *Registry) SetModelRecommendations(preferences map[string][]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recommendations = make(map[string][]string, len(preferences))
	for role, ids := range preferences {
		r.recommendations[role] = slices.Clone(ids)
	}
}

// roleCandidate ranks a model in three layers: configured recommendations,
// the provider's own nominations, then the metadata fallback in
// betterRoleCandidate.
type roleCandidate struct {
	provider     string
	model        ModelInfo
	preference   int
	providerRank int
}

func modelEligible(model ModelInfo, role PhaseRole) bool {
	c := model.Capabilities
	if c == nil {
		return true
	}
	if c.TextOutput != nil && !*c.TextOutput {
		return false
	}
	return role == PhaseChat || c.ToolCall == nil || *c.ToolCall
}

func modelMatchesSelector(model ModelInfo, selector string) bool {
	if strings.EqualFold(selector, model.ID) {
		return true
	}
	return slices.ContainsFunc(model.Aliases, func(alias string) bool { return strings.EqualFold(alias, selector) })
}

// nominationRank returns the position of the first nomination naming model.
// Unnominated models, including every model of a provider that nominates
// nothing, share the lowest rank so nomination list lengths stay comparable
// across providers.
func nominationRank(model ModelInfo, nominated []string) int {
	for i, selector := range nominated {
		if modelMatchesSelector(model, selector) {
			return i
		}
	}
	return math.MaxInt
}

func (r *Registry) roleCandidates(role PhaseRole) []roleCandidate {
	r.mu.RLock()
	preferences := slices.Clone(r.recommendations[string(role)])
	r.mu.RUnlock()
	var result []roleCandidate
	for _, p := range r.DetectedProviders() {
		var nominated []string
		if recommender, ok := p.(RoleModelRecommender); ok {
			nominated = recommender.RecommendedModels(role)
		}
		for _, model := range catalogForProvider(p) {
			if model.ID == "" || !modelEligible(model, role) {
				continue
			}
			result = append(result, roleCandidate{
				provider:     p.Name(),
				model:        model,
				preference:   configuredRank(preferences, p.Name(), model),
				providerRank: nominationRank(model, nominated),
			})
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return betterRoleCandidate(result[i], result[j]) })
	return result
}

// configuredRank ranks model against the full ordered recommendation list so
// that positions stay comparable across providers.
func configuredRank(preferences []string, providerName string, model ModelInfo) int {
	for i, id := range preferences {
		provider, backend, ok := strings.Cut(id, ":")
		if ok && provider == providerName && modelMatchesSelector(model, backend) {
			return i
		}
	}
	return len(preferences)
}

// betterRoleCandidate is deterministic, not a quality claim. Configured
// recommendations win, then provider nominations, then lower positive
// advertised cost for a reference 1M-input/1M-output workload, then context,
// then canonical identity. Missing capabilities never rank a model below one
// that merely reports them: incompatibility is handled by modelEligible.
// Zero/absent prices are not treated as proof that a gateway model is free.
func betterRoleCandidate(a, b roleCandidate) bool {
	if a.preference != b.preference {
		return a.preference < b.preference
	}
	if a.providerRank != b.providerRank {
		return a.providerRank < b.providerRank
	}
	cost := func(m ModelInfo) float64 {
		if m.Cost == nil || m.Cost.Input < 0 || m.Cost.Output < 0 {
			return math.Inf(1)
		}
		value := m.Cost.Input + m.Cost.Output
		if value <= 0 || math.IsNaN(value) {
			return math.Inf(1)
		}
		return value
	}
	if x, y := cost(a.model), cost(b.model); x != y {
		return x < y
	}
	if a.model.ContextWindow != b.model.ContextWindow {
		return a.model.ContextWindow > b.model.ContextWindow
	}
	if a.provider != b.provider {
		return a.provider < b.provider
	}
	return a.model.ID < b.model.ID
}
