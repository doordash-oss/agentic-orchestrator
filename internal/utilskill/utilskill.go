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
//
// Two strengths of loading are supported:
//
//   - Discovery (ForPhase): the skill appears as a row in the preamble table
//     with soft instructions ("scan the table… read SKILL.md for matching
//     topics"). The agent self-triggers on topic match. Adequate for
//     generically useful skills like knowledge-reader and guideline-reader
//     that every feature benefits from.
//
//   - Mandatory (RequiredForPhase): the skill's SKILL.md is emitted as a
//     strong skill-read block — the same "read this file now" treatment given
//     to phase-primary skills (implement, plan-phase, etc.).
//     Triggered when the feature carries a tag in the skill's requiredTags
//     list. This is the enforcement spine that makes a no-brainer skill
//     actually get loaded.
package utilskill

import (
	"slices"
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// PhaseAll is a sentinel indicating a skill should be included in every
// phase's preamble.
const PhaseAll feature.Phase = -1

// entry records the phases in which a utility skill should be advertised and
// (optionally) the feature tags that promote it to a mandatory read.
type entry struct {
	phases       []feature.Phase
	requiredTags []string // when non-empty, skill is mandatory for features with any of these tags
}

// registry maps utility skill names to their advertisement + requirement
// policy. Add new utility skills here.
var registry = map[string]entry{
	"frontend-design": {
		phases: []feature.Phase{
			feature.PhaseBrainstorm,
			feature.PhasePlan,
			feature.PhaseImplement,
			feature.PhaseReview,
		},
		requiredTags: []string{feature.TagFrontend},
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
//
// This is the "discovery" surface — soft advertisement. Use RequiredForPhase
// to compute the mandatory subset for a specific feature's tags.
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

// RequiredForPhase returns the names of utility skills that MUST be read
// during the given phase because the feature carries a matching tag. A skill
// is mandatory when (1) it is registered for this phase and (2) its
// requiredTags list intersects the feature's tags. Returns nil when no skill
// applies.
//
// Callers should emit a mandatory skill-read instruction for each returned
// skill so the agent receives the same strong treatment as the phase's primary
// skill.
func RequiredForPhase(phase feature.Phase, featureTags []string) []string {
	if len(featureTags) == 0 {
		return nil
	}
	var names []string
	for name, e := range registry {
		if len(e.requiredTags) == 0 {
			continue
		}
		if !phaseMatches(e.phases, phase) {
			continue
		}
		if !anyTagMatches(e.requiredTags, featureTags) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func phaseMatches(phases []feature.Phase, phase feature.Phase) bool {
	for _, p := range phases {
		if p == PhaseAll || p == phase {
			return true
		}
	}
	return false
}

func anyTagMatches(required, featureTags []string) bool {
	for _, r := range required {
		if slices.Contains(featureTags, r) {
			return true
		}
	}
	return false
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

// Registry returns a snapshot of the registry in the legacy
// skill -> []phases form. Retained for existing tests and callers that only
// care about advertisement. For mandatory-read policy use RegistryEntries.
func Registry() map[string][]feature.Phase {
	cp := make(map[string][]feature.Phase, len(registry))
	for k, e := range registry {
		phases := make([]feature.Phase, len(e.phases))
		copy(phases, e.phases)
		cp[k] = phases
	}
	return cp
}

// RegistryEntry is a read-only view of a single registry row.
type RegistryEntry struct {
	Phases       []feature.Phase
	RequiredTags []string
}

// RegistryEntries returns a snapshot of the full registry including both
// phase membership and requiredTags.
func RegistryEntries() map[string]RegistryEntry {
	cp := make(map[string]RegistryEntry, len(registry))
	for k, e := range registry {
		phases := make([]feature.Phase, len(e.phases))
		copy(phases, e.phases)
		var tags []string
		if len(e.requiredTags) > 0 {
			tags = make([]string, len(e.requiredTags))
			copy(tags, e.requiredTags)
		}
		cp[k] = RegistryEntry{Phases: phases, RequiredTags: tags}
	}
	return cp
}
