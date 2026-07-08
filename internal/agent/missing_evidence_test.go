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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingEvidenceRequirements_ParseStructuredMarkers(t *testing.T) {
	feedback := strings.Join([]string{
		"## Findings",
		"- **Critical**: MISSING_EVIDENCE_REQUIREMENT visual: Capture the updated setup wizard empty state.",
		"- **High**: unrelated implementation issue",
		"- **Critical**: MISSING_EVIDENCE_REQUIREMENT behavioral: Record the create-project CLI journey through persisted config.",
		"",
		"## Suggestions",
		"- (none)",
		"",
		"## Verdict",
		agentStatusChangesRequested,
	}, "\n")

	got := MissingEvidenceRequirements(feedback)
	if len(got) != 2 {
		t.Fatalf("MissingEvidenceRequirements() len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Kind != testingContractVisualSource || got[0].Requirement != "Capture the updated setup wizard empty state." {
		t.Errorf("MissingEvidenceRequirements()[0] = %+v, want visual setup wizard requirement", got[0])
	}
	if got[1].Kind != testingContractBehavioralSource || got[1].Requirement != "Record the create-project CLI journey through persisted config." {
		t.Errorf("MissingEvidenceRequirements()[1] = %+v, want behavioral CLI requirement", got[1])
	}
}

func TestMissingEvidenceRequirements_ParsePhaseQualifiedMarkers(t *testing.T) {
	feedback := "- **Critical**: MISSING_EVIDENCE_REQUIREMENT phase 1 behavioral: Record the onboarding CLI journey."

	got := MissingEvidenceRequirements(feedback)
	if len(got) != 1 {
		t.Fatalf("MissingEvidenceRequirements() len = %d, want 1: %+v", len(got), got)
	}
	if got[0].Phase != 1 {
		t.Errorf("MissingEvidenceRequirements()[0].Phase = %d, want 1", got[0].Phase)
	}
	if got[0].Kind != testingContractBehavioralSource {
		t.Errorf("MissingEvidenceRequirements()[0].Kind = %q, want behavioral", got[0].Kind)
	}
	if got[0].Requirement != "Record the onboarding CLI journey." {
		t.Errorf("MissingEvidenceRequirements()[0].Requirement = %q, want reviewer-authored requirement", got[0].Requirement)
	}
}

func TestMissingEvidenceReviewerSkillDocumentsPerPhaseRules(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	implementationReviewPath := filepath.Join(repoRoot, "skills", "review-implementation-functionality-evidence", "SKILL.md")

	implementationReview, err := os.ReadFile(implementationReviewPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", implementationReviewPath, err)
	}
	for _, want := range []string{
		"Missing Visual / Behavioral Evidence Safety Net",
		"rendered UI, TUI screens, web/mobile/native views, CLI output",
		"Existing",
		"Do not",
		"MISSING_EVIDENCE_REQUIREMENT visual: <reviewer-authored requirement>",
		"MISSING_EVIDENCE_REQUIREMENT behavioral: <reviewer-authored requirement>",
		"phase-plan revision",
		"per-phase implementation review gate",
		"At the per-phase gate",
	} {
		if !strings.Contains(string(implementationReview), want) {
			t.Errorf("%s missing %q", implementationReviewPath, want)
		}
	}
	if strings.Contains(string(implementationReview), "At the Final gate") {
		t.Errorf("%s still documents Final-gate Functionality/Evidence behavior after QA replaced it", implementationReviewPath)
	}
}

func TestLatestPlanRevisionFeedbackAttemptFindsInvalidatedMissingEvidenceAttempt(t *testing.T) {
	planDir := t.TempDir()
	feedback := MissingEvidencePlanRevisionFeedback([]MissingEvidenceRequirement{
		{Kind: testingContractVisualSource, Requirement: "Capture the setup wizard empty state."},
	})
	if err := WritePlanAttemptMeta(planDir, PlanAttemptMeta{
		Attempt:      1,
		ReviewStatus: agentStatusChangesRequested,
	}); err != nil {
		t.Fatalf("WritePlanAttemptMeta() error = %v", err)
	}
	attemptDir := filepath.Join(planDir, "attempt-01")
	if err := os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(feedback), 0o644); err != nil {
		t.Fatalf("WriteFile(validation-feedback.md) error = %v", err)
	}

	attempt, got := latestPlanRevisionFeedbackAttempt(planDir)
	if attempt != 1 {
		t.Fatalf("latestPlanRevisionFeedbackAttempt() attempt = %d, want 1", attempt)
	}
	if !strings.Contains(got, "MISSING_EVIDENCE_REQUIREMENT visual: Capture the setup wizard empty state.") {
		t.Fatalf("latestPlanRevisionFeedbackAttempt() feedback missing marker:\n%s", got)
	}
}

func TestMissingEvidencePlanRevisionFeedback_PreservesReviewerRequirement(t *testing.T) {
	requirements := []MissingEvidenceRequirement{
		{Kind: testingContractVisualSource, Requirement: "Capture the updated setup wizard empty state."},
		{Phase: 1, Kind: testingContractBehavioralSource, Requirement: "Record the phase-one create-project CLI journey."},
	}

	got := MissingEvidencePlanRevisionFeedback(requirements)
	for _, want := range []string{
		"MISSING_EVIDENCE_REQUIREMENT visual: Capture the updated setup wizard empty state.",
		"MISSING_EVIDENCE_REQUIREMENT phase 1 behavioral: Record the phase-one create-project CLI journey.",
		"### Visual Evidence",
		"None required: <reason>",
		"Do not add verification-report.yaml rows directly",
		"Do not use testing-contract.yaml Changes entries",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("MissingEvidencePlanRevisionFeedback() missing %q:\n%s", want, got)
		}
	}
}
