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

type roleCandidate struct {
	provider   string
	model      ModelInfo
	preference int
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

func (r *Registry) roleCandidates(role PhaseRole) []roleCandidate {
	r.mu.RLock()
	preferences := slices.Clone(r.recommendations[string(role)])
	r.mu.RUnlock()
	var result []roleCandidate
	for _, p := range r.DetectedProviders() {
		for _, model := range catalogForProvider(p) {
			if model.ID == "" || !modelEligible(model, role) {
				continue
			}
			rank := len(preferences)
			for i, id := range preferences {
				provider, backend, ok := strings.Cut(id, ":")
				if !ok || provider != p.Name() {
					continue
				}
				if strings.EqualFold(backend, model.ID) || slices.ContainsFunc(model.Aliases, func(alias string) bool { return strings.EqualFold(alias, backend) }) {
					rank = i
					break
				}
			}
			result = append(result, roleCandidate{p.Name(), model, rank})
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return betterRoleCandidate(result[i], result[j], role) })
	return result
}

// Fallback ordering is deterministic, not a quality claim: explicit evaluated
// preferences first, then reported technical support, positive advertised cost
// for a reference 1M-input/1M-output workload, context, and canonical identity.
// Zero/absent prices are not treated as proof that a gateway model is free.
func betterRoleCandidate(a, b roleCandidate, role PhaseRole) bool {
	if a.preference != b.preference {
		return a.preference < b.preference
	}
	support := func(m ModelInfo) int {
		if m.Capabilities == nil {
			return 0
		}
		n := 0
		if m.Capabilities.TextOutput != nil && *m.Capabilities.TextOutput {
			n++
		}
		if role != PhaseChat && m.Capabilities.ToolCall != nil && *m.Capabilities.ToolCall {
			n++
		}
		return n
	}
	if x, y := support(a.model), support(b.model); x != y {
		return x > y
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
