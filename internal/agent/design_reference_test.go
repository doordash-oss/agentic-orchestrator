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

package agent

import (
	"strings"
	"testing"
)

func TestBuildFinalReviewPrompt_DoesNotEmitDesignBlock(t *testing.T) {
	prompt := BuildFinalReviewPrompt(FinalReviewPromptOpts{
		FeatureDescription:     "Build a thing",
		ExitCriteria:           "tests pass",
		DiffBase:               "main",
		WorkDir:                "/tmp/work",
		VerificationPath:       "/tmp/v.yaml",
		Iteration:              1,
		RoadmapPath:            "/tmp/roadmap.md",
		DesignArtifactPath: "/tmp/design.md",
		FeedbackPath:           "/tmp/feedback.md",
	})
	if strings.Contains(prompt, "Design Reference") {
		t.Errorf("final review prompt unexpectedly includes design block")
	}
	if strings.Contains(prompt, "/tmp/design.md") {
		t.Errorf("final review prompt unexpectedly includes design path")
	}
}

// TestBuildFinalReviewPrompt_NoPathNoBlock ensures backward compatibility:
// when no design path is supplied, no block is emitted and the prompt
// starts at "# Review Context" as before.
func TestBuildFinalReviewPrompt_NoPathNoBlock(t *testing.T) {
	prompt := BuildFinalReviewPrompt(FinalReviewPromptOpts{
		FeatureDescription: "Build a thing",
		ExitCriteria:       "tests pass",
		Iteration:          1,
		FeedbackPath:       "/tmp/feedback.md",
	})
	if strings.Contains(prompt, "Design Reference") {
		t.Errorf("final review prompt unexpectedly includes design block when path is empty")
	}
	if !strings.HasPrefix(prompt, "# Review Context") {
		t.Errorf("final review prompt header changed; first 40 chars: %q", prompt[:min(40, len(prompt))])
	}
}

func TestBuildFinalReviewPromptLeavesOutputContractToRoleSpec(t *testing.T) {
	feedbackPath := "/tmp/review-feedback.md"
	prompt := BuildFinalReviewPrompt(FinalReviewPromptOpts{
		FeatureDescription:  "Build a thing",
		ExitCriteria:        "tests pass",
		TestingContractPath: "/tmp/testing-contract.yaml",
		VerificationPath:    "/tmp/verification-report.yaml",
		Iteration:           1,
		FeedbackPath:        feedbackPath,
	})

	if strings.Contains(prompt, feedbackPath) {
		t.Fatalf("BuildFinalReviewPrompt() rendered RoleSpec-owned feedback path %q:\n%s", feedbackPath, prompt)
	}
	if strings.Contains(prompt, "Feedback file") {
		t.Fatalf("BuildFinalReviewPrompt() rendered output handoff prose:\n%s", prompt)
	}
}

func TestBuildFinalFixPrompt_DoesNotEmitDesignBlock(t *testing.T) {
	prompt := BuildFinalFixPrompt(FinalFixPromptOpts{
		Feedback:               "fix this",
		ExitCriteria:           "tests pass",
		IterDir:                "/tmp/iter",
		Iteration:              1,
		DesignArtifactPath: "/tmp/design.md",
	})
	if strings.Contains(prompt, "Design Reference") {
		t.Errorf("final fix prompt unexpectedly includes design block")
	}
	if strings.Contains(prompt, "/tmp/design.md") {
		t.Errorf("final fix prompt unexpectedly includes design path")
	}
}

func TestBuildFinalFixPromptDoesNotInlineReviewerFeedback(t *testing.T) {
	feedback := "## Findings\n- **High**: tighten the verifier\n\n## Suggestions\n- (none)\n\n## Verdict\nCHANGES_REQUESTED"
	feedbackPath := "/tmp/review-feedback.md"
	prompt := BuildFinalFixPrompt(FinalFixPromptOpts{
		Feedback:     feedback,
		FeedbackPath: feedbackPath,
		ExitCriteria: "tests pass",
		Iteration:    1,
	})
	if strings.Contains(prompt, feedback) {
		t.Fatalf("BuildFinalFixPrompt() inlined reviewer feedback body:\n%s", prompt)
	}
	if strings.Contains(prompt, "```") {
		t.Fatalf("BuildFinalFixPrompt() rendered legacy fenced feedback block:\n%s", prompt)
	}
	if !strings.Contains(prompt, feedbackPath) {
		t.Fatalf("BuildFinalFixPrompt() missing reviewer feedback path %q:\n%s", feedbackPath, prompt)
	}
}

func TestBuildFinalFixPrompt_NoPathNoBlock(t *testing.T) {
	prompt := BuildFinalFixPrompt(FinalFixPromptOpts{
		Feedback:     "fix this",
		ExitCriteria: "tests pass",
		Iteration:    1,
	})
	if strings.Contains(prompt, "Design Reference") {
		t.Errorf("final fix prompt unexpectedly includes design block when path is empty")
	}
}
