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

// Package utilskill maintains a registry of utility skills and the phases
// in which they should appear in the discovery preamble. Utility skills are
// secondary skills (like frontend-design) that complement a phase's primary
// skill when their domain is relevant to the task.
package utilskill

import (
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// PhaseAll is a sentinel indicating a skill should be included in every
// phase's preamble.
const PhaseAll feature.Phase = -1

// entry records the phases in which a utility skill should be advertised.
type entry struct {
	phases []feature.Phase
}

// registry maps utility skill names to their advertisement policy. Add new
// utility skills here.
var registry = map[string]entry{
	"frontend-design": {
		phases: []feature.Phase{
			feature.PhaseBrainstorm,
			feature.PhasePlan,
			feature.PhaseImplement,
			feature.PhaseReview,
		},
	},
	"knowledge-reader": {
		phases: []feature.Phase{
			feature.PhaseInquire,
			feature.PhaseResearch,
			feature.PhaseBrainstorm,
			feature.PhasePlan,
			feature.PhaseImplement,
			feature.PhaseReview,
		},
	},
	"guideline-reader": {
		// Excluded from Inquire and Research: those phases only ask
		// clarifying questions or explore the codebase to answer them,
		// and never write or review code, so language-level conventions
		// are noise there. Every other interactive phase either
		// produces or evaluates code and benefits from the guidelines
		// catalog.
		phases: []feature.Phase{
			feature.PhaseBrainstorm,
			feature.PhasePlan,
			feature.PhaseImplement,
			feature.PhaseReview,
			feature.PhaseKnowledgeBase,
		},
	},
}

// ForPhase returns the names of utility skills that should appear in the
// preamble table for the given phase. Skills mapped to PhaseAll are always
// included. Passing PhaseAll as the phase returns all registered utility
// skills.
func ForPhase(phase feature.Phase) []string {
	if phase == PhaseAll {
		return All()
	}
	var names []string
	for name, e := range registry {
		for _, p := range e.phases {
			if p == PhaseAll || p == phase {
				names = append(names, name)
				break
			}
		}
	}
	sort.Strings(names)
	return names
}

// All returns every registered utility skill name, sorted alphabetically.
func All() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Registry returns a snapshot of the registry in the skill -> []phases form.
func Registry() map[string][]feature.Phase {
	cp := make(map[string][]feature.Phase, len(registry))
	for k, e := range registry {
		phases := make([]feature.Phase, len(e.phases))
		copy(phases, e.phases)
		cp[k] = phases
	}
	return cp
}
