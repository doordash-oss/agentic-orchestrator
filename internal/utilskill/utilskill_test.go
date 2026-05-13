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

package utilskill

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/skilldef"
)

func TestRegistryEntriesHaveAtLeastOnePhase(t *testing.T) {
	for name, phases := range Registry() {
		if len(phases) == 0 {
			t.Errorf("utility skill %q has no phases mapped", name)
		}
	}
}

// TestRequiredForPhase_FrontendTagPromotesFrontendDesign encodes the core
// guarantee: a frontend-tagged feature receives frontend-design as a
// mandatory read on every phase where it is registered. This is the
// regression guard against the Native App feature scenario (zero loads of
// frontend-design across 227 tracked file-reads).
func TestRequiredForPhase_FrontendTagPromotesFrontendDesign(t *testing.T) {
	phases := []feature.Phase{
		feature.PhaseBrainstorm,
		feature.PhasePlan,
		feature.PhaseImplement,
		feature.PhaseReview,
	}
	for _, p := range phases {
		got := RequiredForPhase(p, []string{feature.TagFrontend})
		if len(got) == 0 || got[0] != "frontend-design" {
			t.Errorf("RequiredForPhase(%v, [frontend]) = %v, want [frontend-design, ...]", p, got)
		}
	}
}

// TestRequiredForPhase_NoTagsNoRequirements ensures features without tags
// never receive mandatory skill instructions (avoiding noise on backend-only
// features that happen to match a phase).
func TestRequiredForPhase_NoTagsNoRequirements(t *testing.T) {
	got := RequiredForPhase(feature.PhaseImplement, nil)
	if len(got) != 0 {
		t.Errorf("RequiredForPhase(Implement, nil) = %v, want empty", got)
	}
	got = RequiredForPhase(feature.PhaseImplement, []string{})
	if len(got) != 0 {
		t.Errorf("RequiredForPhase(Implement, []) = %v, want empty", got)
	}
}

// TestRequiredForPhase_UnrelatedTagSkipsFrontendDesign ensures a
// backend-only feature doesn't pull in frontend-design.
func TestRequiredForPhase_UnrelatedTagSkipsFrontendDesign(t *testing.T) {
	got := RequiredForPhase(feature.PhaseImplement, []string{feature.TagBackend})
	for _, name := range got {
		if name == "frontend-design" {
			t.Errorf("RequiredForPhase(Implement, [backend]) unexpectedly includes frontend-design")
		}
	}
}

// TestRequiredForPhase_OffPhaseNoRequirement ensures frontend-design is not
// forced on phases where it isn't registered (e.g. Inquire, KB).
func TestRequiredForPhase_OffPhaseNoRequirement(t *testing.T) {
	got := RequiredForPhase(feature.PhaseInquire, []string{feature.TagFrontend})
	for _, name := range got {
		if name == "frontend-design" {
			t.Errorf("RequiredForPhase(Inquire, [frontend]) unexpectedly includes frontend-design")
		}
	}
}

func TestRegistrySkillsExistInEmbeddedFS(t *testing.T) {
	embedded, err := skilldef.ParseEmbedded()
	if err != nil {
		t.Fatalf("parsing embedded skills: %v", err)
	}
	for name := range Registry() {
		if _, ok := embedded[name]; !ok {
			t.Errorf("utility skill %q is not found in embedded skills FS", name)
		}
	}
}

func TestForPhaseReturnsCorrectSkills(t *testing.T) {
	tests := []struct {
		name  string
		phase feature.Phase
		want  []string
	}{
		{
			name:  "brainstorm includes frontend-design, guideline-reader, and knowledge-reader",
			phase: feature.PhaseBrainstorm,
			want:  []string{"frontend-design", "guideline-reader", "knowledge-reader"},
		},
		{
			name:  "plan includes frontend-design, guideline-reader, and knowledge-reader",
			phase: feature.PhasePlan,
			want:  []string{"frontend-design", "guideline-reader", "knowledge-reader"},
		},
		{
			name:  "implement includes frontend-design, guideline-reader, and knowledge-reader",
			phase: feature.PhaseImplement,
			want:  []string{"frontend-design", "guideline-reader", "knowledge-reader"},
		},
		{
			name:  "review includes frontend-design, guideline-reader, and knowledge-reader",
			phase: feature.PhaseReview,
			want:  []string{"frontend-design", "guideline-reader", "knowledge-reader"},
		},
		{
			// Research intentionally does not surface guideline-reader:
			// the phase only explores the codebase to answer questions
			// and never writes or reviews code, so coding-convention
			// guidance is noise.
			name:  "research includes only knowledge-reader",
			phase: feature.PhaseResearch,
			want:  []string{"knowledge-reader"},
		},
		{
			// Inquire intentionally does not surface guideline-reader:
			// the phase only asks clarifying questions and never writes
			// or reviews code, so coding-convention guidance is noise.
			name:  "inquire includes only knowledge-reader",
			phase: feature.PhaseInquire,
			want:  []string{"knowledge-reader"},
		},
		{
			name:  "knowledge base includes guideline-reader",
			phase: feature.PhaseKnowledgeBase,
			want:  []string{"guideline-reader"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ForPhase(tt.phase)
			if len(got) != len(tt.want) {
				t.Fatalf("ForPhase(%v) = %v, want %v", tt.phase, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ForPhase(%v)[%d] = %q, want %q", tt.phase, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestPhaseAllIncludedEverywhere(t *testing.T) {
	// Add a temporary PhaseAll entry to verify the mechanism
	origRegistry := registry
	registry = map[string]entry{
		"test-everywhere": {phases: []feature.Phase{PhaseAll}},
		"test-specific":   {phases: []feature.Phase{feature.PhaseImplement}},
	}
	defer func() { registry = origRegistry }()

	phases := []feature.Phase{
		feature.PhaseResearch,
		feature.PhasePlan,
		feature.PhaseImplement,
		feature.PhaseReview,
		feature.PhaseKnowledgeBase,
		feature.PhaseInquire,
		feature.PhaseBrainstorm,
	}

	for _, phase := range phases {
		got := ForPhase(phase)
		found := false
		for _, name := range got {
			if name == "test-everywhere" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("PhaseAll skill 'test-everywhere' not found in ForPhase(%v) = %v", phase, got)
		}
	}

	// test-specific should only appear for Implement
	implSkills := ForPhase(feature.PhaseImplement)
	foundSpecific := false
	for _, name := range implSkills {
		if name == "test-specific" {
			foundSpecific = true
			break
		}
	}
	if !foundSpecific {
		t.Error("test-specific not found in Implement phase")
	}

	researchSkills := ForPhase(feature.PhaseResearch)
	for _, name := range researchSkills {
		if name == "test-specific" {
			t.Error("test-specific should not appear in Research phase")
		}
	}
}

func TestAllReturnsAllRegisteredSkills(t *testing.T) {
	all := All()
	reg := Registry()
	if len(all) != len(reg) {
		t.Fatalf("All() returned %d skills, registry has %d", len(all), len(reg))
	}
	for _, name := range all {
		if _, ok := reg[name]; !ok {
			t.Errorf("All() returned %q which is not in registry", name)
		}
	}
}
