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
				"Resolved NEED_USER_INPUT from previous iterations",
				"Answers to NEED_HELP questions",
				"Testing contract",
			},
		},
		{
			name: "optional_feedback_sections_emitted_when_inputs_populated",
			input: ImplementUserInput{
				PlanPath:              "/plan.md",
				ExitCriteria:          "Relevant tests pass.",
				Feedback:              "Fix the PKCE branch.",
				HelpAnswers:           "Q: Which library?\nA: oauth2.",
				PriorUserInputAnswers: "Q: Legacy or new auth?\nA: New.",
				Iteration:             3,
			},
			wantContains: []string{
				"This is iteration 3.",
				"Reviewer feedback from previous iteration",
				"Fix the PKCE branch.",
				"Resolved NEED_USER_INPUT from previous iterations",
				"Q: Legacy or new auth?",
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
				VerificationReportPath:            "/iter/verification-report.yaml",
				IncludeManualVerificationOutcomes: true,
				Publishable:                       true,
			},
			wantContains: []string{"## Manual Verification Outcomes", "verification report named by the RoleSpec Output Files section"},
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

/* obsolete monolithic final-review prompt tests; removed after the rebase
	tests := []struct {
		name         string
		input        FinalReviewUserInput
		wantContains []string
		wantOmit     []string
	}{
		{
			name: "final_review_phase_variant_focuses_product_acceptance",
			input: FinalReviewUserInput{
				Iteration:          1,
				IsCycleReview:      false,
				DiffBase:           "main",
				FeatureDescription: "Build a 2D retro game maker.",
				RoadmapPath:        "/roadmap.md",
				DesignArtifactPath: "/design.md",
				ExitCriteria:       "Relevant tests pass.",
				FeedbackPath:       "/review-feedback.md",
				Publishable:        true,
			},
			wantContains: []string{"# Product Acceptance Context", "**Approved design**: /design.md", "Build a 2D retro game maker.", "Relevant tests pass.", "**Review feedback output**: /review-feedback.md"},
			wantOmit:     []string{"## Verification Context", "**Testing contract**", "**Verification report**", "## Current Cycle Focus", "NOTE: Local-only repository"},
		},
		{
			name: "final_review_lists_last_phase_evidence_as_reusable_qa_context",
			input: FinalReviewUserInput{
				Iteration:                            1,
				DiffBase:                             "main",
				PriorImplementationReportPaths:       []string{"/run/phase-01/implement/iteration-02/verification-report.yaml"},
				PriorImplementationEvidenceRootDirs:  []string{"/run/phase-01/implement/iteration-02"},
				PriorImplementationEvidenceArtifacts: []string{"/run/phase-01/implement/iteration-02/screenshots/setup.png"},
				Publishable:                          true,
			},
			wantContains: []string{
				"## Reusable Last-Phase QA Context",
				"/run/phase-01/implement/iteration-02/verification-report.yaml",
				"/run/phase-01/implement/iteration-02",
				"Referenced evidence artifacts",
				"/run/phase-01/implement/iteration-02/screenshots/setup.png",
			},
			wantOmit: []string{
				"Phase plans:",
				"Implementation testing contracts:",
				"/run/phase-01/plan/phase-plan.md",
				"/run/phase-01/testing-contract.yaml",
				"Use these prior implementation artifacts as the coverage source",
				"final-review testing contract stays PlanLess",
				"MISSING_EVIDENCE_REQUIREMENT phase <number>",
			},
		},
		{
			name: "final_review_cycle_variant_includes_cycle_focus_and_legacy_verification_path",
			input: FinalReviewUserInput{
				Iteration:        2,
				IsCycleReview:    true,
				DiffBase:         "main",
				RoadmapPath:      "/roadmap.md",
				CycleFocus:       "Rebase: button color only.",
				FeedbackPath:     "/cycle/review-feedback.md",
				Publishable:      false,
				PreviousFeedback: "Use a darker hover state.",
			},
			wantContains: []string{"## Current Cycle Focus", "Rebase: button color only.", "**Review feedback output**: /cycle/review-feedback.md", "NOTE: Local-only repository"},
			wantOmit:     []string{"**Testing contract**", "**Verification report**"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FinalReviewUserPrompt(tt.input)
			for _, want := range tt.wantContains {
				requireContains(t, got, want)
			}
			for _, unwanted := range tt.wantOmit {
				requireNotContains(t, got, unwanted)
			}
		})
	}
}
*/

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
		"design_system_rolespec":                   true,
		"design_user_multi_repo":                   true,
		"final_fix_user_with_manual":               true,
		"implement_system_rolespec":                true,
		"implementation_review_axis_final_user":    true,
		"implementation_review_axis_live_run_user": true,
		"implementation_review_axis_user":          true,
		"inquire_user_high_with_kb":                true,
		"kb_build_full":                            true,
		"phase_plan_revision_user":                 true,
		"phase_plan_user_autonomous":               true,
		"pr_description_user_full":                 true,
		"refactor_plan_user":                       true,
		"research_from_questions_user":             true,
		"roadmap_revision_user":                    true,
		"roadmap_user_multi_repo":                  true,
		"scout_user":                               true,
		"summary_user":                             true,
		"validate_specialized_grounding":           true,
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
