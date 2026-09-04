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

package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImplementPromptBranchBehavior(t *testing.T) {
	tests := []struct {
		name         string
		input        ImplementUserInput
		wantContains []string
		wantOmit     []string
	}{
		{
			name: "optional_feedback_sections_omitted_when_inputs_empty",
			input: ImplementUserInput{
				PlanPath:     "/plan.md",
				ExitCriteria: "Relevant tests pass.",
				Iteration:    1,
			},
			wantContains: []string{"# Implementation Context", "**Plan**: /plan.md", "**Exit criteria**: Relevant tests pass."},
			wantOmit: []string{
				"Required verification items for this iteration",
				"Reviewer feedback from previous iteration",
				"Answers to NEED_HELP questions",
				"Testing contract",
			},
		},
		{
			name: "optional_feedback_sections_emitted_when_inputs_populated",
			input: ImplementUserInput{
				PlanPath:     "/plan.md",
				ExitCriteria: "Relevant tests pass.",
				Feedback:     "Fix the PKCE branch.",
				HelpAnswers:  "Q: Which library?\nA: oauth2.",
				Iteration:    3,
			},
			wantContains: []string{
				"This is iteration 3.",
				"Reviewer feedback from previous iteration",
				"Fix the PKCE branch.",
				"Answers to NEED_HELP questions",
				"Q: Which library?",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ImplementUserPrompt(tt.input)
			for _, want := range tt.wantContains {
				requireContains(t, got, want)
			}
			for _, unwanted := range tt.wantOmit {
				requireNotContains(t, got, unwanted)
			}
		})
	}
}

func TestReviewPromptBranchBehavior(t *testing.T) {
	base := ReviewUserInput{
		Iteration:              2,
		IterDir:                "/iter",
		RoadmapPath:            "/roadmap.md",
		PlanPath:               "/plan.md",
		ExitCriteria:           "Relevant tests pass.",
		VerificationReportPath: "/iter/verification-report.yaml",
		ContractPath:           "/phase/testing-contract.yaml",
		ProgressPath:           "/iter/progress.md",
		PhaseType:              "tdd-fill-in",
	}

	tests := []struct {
		name         string
		input        ReviewUserInput
		wantContains []string
		wantOmit     []string
	}{
		{
			name:         "review_prompt_omits_removed_evidence_sections",
			input:        base,
			wantContains: []string{"## Progress", "## Phase Type"},
			wantOmit:     []string{"## Behavioral Evidence From This Iteration", "## Visual Evidence From This Iteration"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReviewUserPrompt(tt.input)
			for _, want := range tt.wantContains {
				requireContains(t, got, want)
			}
			for _, unwanted := range tt.wantOmit {
				requireNotContains(t, got, unwanted)
			}
		})
	}
}

func TestFinalFixPromptBranches(t *testing.T) {
	tests := []struct {
		name         string
		input        FinalFixUserInput
		wantContains []string
		wantOmit     []string
	}{
		{
			name: "manual_verification_and_publishable_sections_emitted_when_enabled",
			input: FinalFixUserInput{
				Iteration:                         2,
				Feedback:                          "Manual verification is missing.",
				IncludeManualVerificationOutcomes: true,
				Publishable:                       true,
			},
			wantContains: []string{"## Manual Verification Outcomes", "describe in your fix output what you actually observed"},
			wantOmit:     []string{"NOTE: Local-only repository"},
		},
		{
			name: "manual_verification_omitted_and_local_only_note_emitted_when_not_publishable",
			input: FinalFixUserInput{
				Iteration:   1,
				Feedback:    "Tighten wording.",
				Publishable: false,
			},
			wantContains: []string{"NOTE: Local-only repository"},
			wantOmit:     []string{"## Manual Verification Outcomes"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FinalFixUserPrompt(tt.input)
			for _, want := range tt.wantContains {
				requireContains(t, got, want)
			}
			for _, unwanted := range tt.wantOmit {
				requireNotContains(t, got, unwanted)
			}
		})
	}
}

func TestPRDescriptionPromptPopulationBranches(t *testing.T) {
	tests := []struct {
		name         string
		input        PRDescriptionUserInput
		wantContains []string
		wantOmit     []string
	}{
		{
			name: "pr_description_full_renders_roadmap_commits_and_diffstat",
			input: PRDescriptionUserInput{
				FeatureName:        "Add OAuth login",
				FeatureDescription: "Sign in with Google.",
				Roadmap:            "Phase 1: scaffolding.",
				CommitBodies:       "feat: add login route",
				DiffStat:           " 5 files changed",
			},
			wantContains: []string{"## Feature", "## Roadmap / Plan", "## Commit Messages", "## Changes (file stats)"},
		},
		{
			name: "pr_description_minimal_omits_unpopulated_sections",
			input: PRDescriptionUserInput{
				FeatureName: "Add OAuth login",
			},
			wantContains: []string{"## Feature", "Name: Add OAuth login", "## Instructions"},
			wantOmit:     []string{"## Roadmap / Plan", "## Commit Messages", "## Changes (file stats)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PRDescriptionUserPrompt(tt.input)
			for _, want := range tt.wantContains {
				requireContains(t, got, want)
			}
			for _, unwanted := range tt.wantOmit {
				requireNotContains(t, got, unwanted)
			}
		})
	}
}

func TestValidateSpecializedPromptBranches(t *testing.T) {
	tests := []struct {
		name         string
		input        ValidateSpecializedUserInput
		wantContains []string
		wantOmit     []string
	}{
		{
			name: "validate_specialized_grounding_includes_feature_prior_phase_and_research_context",
			input: ValidateSpecializedUserInput{
				Name:                     "OAuth login",
				Description:              "Sign in with Google.",
				ExitCriteria:             "Relevant tests pass.",
				RiskLevel:                "medium",
				DomainName:               "grounding",
				PlanPath:                 "/phase-plan.md",
				IncludePriorPhaseContext: true,
				PriorPhasePlanPaths:      []string{"/phase-1/plan.md"},
				ResearchPath:             "/research.md",
			},
			wantContains: []string{"## Feature Under Review", "## Prior Phase Context", "/phase-1/plan.md", "## Research Findings"},
		},
		{
			name: "validate_specialized_roadmap_scope_omits_phase_only_context",
			input: ValidateSpecializedUserInput{
				Name:          "OAuth login",
				Description:   "Sign in with Google.",
				DomainName:    "scope",
				PlanPath:      "/roadmap.md",
				IsRoadmapKind: true,
			},
			wantContains: []string{"## Plan to Evaluate", "/roadmap.md"},
			wantOmit:     []string{"## Feature Under Review", "## Prior Phase Context", "## Research Findings"},
		},
		{
			name: "automated_verification_only_adds_note_and_omits_it_by_default",
			input: ValidateSpecializedUserInput{
				Name:                      "OAuth login",
				DomainName:                "scope",
				PlanPath:                  "/phase-plan.md",
				AutomatedVerificationOnly: true,
			},
			wantContains: []string{"this feature verifies through automated tests only", "do not request evidence matrices, screenshots, or manual observation steps"},
		},
		{
			name: "automated_verification_only_note_omitted_when_false",
			input: ValidateSpecializedUserInput{
				Name:       "OAuth login",
				DomainName: "scope",
				PlanPath:   "/phase-plan.md",
			},
			wantOmit: []string{"this feature verifies through automated tests only"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateSpecializedUserPrompt(tt.input)
			for _, want := range tt.wantContains {
				requireContains(t, got, want)
			}
			for _, unwanted := range tt.wantOmit {
				requireNotContains(t, got, unwanted)
			}
		})
	}
}

func TestGoldenSnapshotsNoOrphanFiles(t *testing.T) {
	retained := map[string]bool{
		"codex_design_system_rolespec":                         true,
		"codex_implement_system_rolespec":                      true,
		"autoreview_user":                                      true,
		"design_system_rolespec":                               true,
		"design_user_multi_repo":                               true,
		"final_fix_user_with_manual":                           true,
		"implement_system_rolespec":                            true,
		"implementation_review_axis_final_user":                true,
		"implementation_review_axis_live_run_user":             true,
		"implementation_review_axis_user":                      true,
		"inquire_user_high_with_kb":                            true,
		"kb_build_full":                                        true,
		"phase_plan_revision_user":                             true,
		"phase_plan_revision_user_automated_verification_only": true,
		"phase_plan_user_autonomous":                           true,
		"phase_plan_user_automated_verification_only":          true,
		"pr_description_user_full":                             true,
		"research_from_questions_user":                         true,
		"roadmap_revision_user":                                true,
		"roadmap_user_multi_repo":                              true,
		"roadmap_user_no_design":                               true,
		"roadmap_user_refactor_pass":                           true,
		"scout_user":                                           true,
		"summary_user":                                         true,
		"validate_specialized_grounding":                       true,
		"validate_specialized_automated_verification_only":     true,
	}

	files, err := filepath.Glob(filepath.Join("testdata", "*.golden"))
	if err != nil {
		t.Fatalf("glob golden files: %v", err)
	}
	if len(files) != len(retained) {
		t.Errorf("golden file count = %d, want %d", len(files), len(retained))
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".golden")
		if !retained[name] {
			t.Errorf("orphan golden file: %s", file)
		}
	}
	for name := range retained {
		path := filepath.Join("testdata", name+".golden")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("stat retained golden %s: %v", path, err)
		}
	}
}
