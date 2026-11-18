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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestContractRegistryImplementerLookup(t *testing.T) {
	contract, ok := lookup(feature.PhaseImplement, roleImplementer)
	if !ok {
		t.Fatal("Lookup(PhaseImplement, RoleImplementer) ok = false, want true")
	}
	if contract.Role != roleImplementer {
		t.Fatalf("Lookup(PhaseImplement, RoleImplementer).Role = %q, want %q", contract.Role, roleImplementer)
	}
	if len(contract.Required) != 0 {
		t.Fatalf("Lookup(PhaseImplement, RoleImplementer) required artifacts = %d, want 0", len(contract.Required))
	}
}

func TestContractRegistryPlanRoadmapPlanner(t *testing.T) {
	attemptDir := writeRoadmapPlannerAttempt(t, validRoadmapText(), validPlanAttemptMetaYAML())

	contract, ok := lookup(feature.PhasePlan, RolePlanRoadmapPlanner)
	if !ok {
		t.Fatal("Lookup(PhasePlan, RolePlanRoadmapPlanner) ok = false, want true")
	}
	if contract.Role != RolePlanRoadmapPlanner {
		t.Fatalf("Lookup(PhasePlan, RolePlanRoadmapPlanner).Role = %q, want %q", contract.Role, RolePlanRoadmapPlanner)
	}

	out, violations, err := Validate(feature.PhasePlan, RolePlanRoadmapPlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("Validate() violations = %v, want none", violations)
	}
	if !out.OK || len(out.RoadmapPhases) != 1 || out.PlanAttemptMeta == nil {
		t.Fatalf("Validate() outcome = %+v, want parsed roadmap phase and plan attempt meta", out)
	}
}

func TestContractRegistryPlanRoadmapPlannerReportsMissingRoadmap(t *testing.T) {
	attemptDir := writeRoadmapPlannerAttempt(t, "", validPlanAttemptMetaYAML())

	out, violations, err := Validate(feature.PhasePlan, RolePlanRoadmapPlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	got := joinProtocolViolations(violations)
	if !strings.Contains(got, "roadmap markdown") || !strings.Contains(got, "missing") {
		t.Fatalf("JoinProtocolViolations() = %q, want missing roadmap markdown", got)
	}
}

func TestContractRegistryPlanRoadmapPlannerReportsMalformedRoadmap(t *testing.T) {
	attemptDir := writeRoadmapPlannerAttempt(t, "# Roadmap\n\n## Phase 1\nmissing colon\n", validPlanAttemptMetaYAML())

	out, violations, err := Validate(feature.PhasePlan, RolePlanRoadmapPlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	got := joinProtocolViolations(violations)
	if !strings.Contains(got, "roadmap markdown") || !strings.Contains(got, "unparseable") {
		t.Fatalf("JoinProtocolViolations() = %q, want malformed roadmap violation", got)
	}
}

func TestContractRegistryPlanRoadmapPlannerReportsMissingMeta(t *testing.T) {
	attemptDir := writeRoadmapPlannerAttempt(t, validRoadmapText(), "")

	out, violations, err := Validate(feature.PhasePlan, RolePlanRoadmapPlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	got := joinProtocolViolations(violations)
	if !strings.Contains(got, "meta.yaml") || !strings.Contains(got, "missing") {
		t.Fatalf("JoinProtocolViolations() = %q, want missing meta.yaml", got)
	}
}

func TestValidateArtifactsPreflightPlanRoadmapPlannerSkipsHarnessMeta(t *testing.T) {
	attemptDir := writeRoadmapPlannerAttempt(t, validRoadmapText(), "")

	out, violations, err := ValidateArtifactsPreflight(feature.PhasePlan, RolePlanRoadmapPlanner, attemptDir)
	if err != nil {
		t.Fatalf("ValidateArtifactsPreflight() error = %v", err)
	}
	got := joinProtocolViolations(violations)
	if strings.Contains(got, "meta.yaml") {
		t.Fatalf("JoinProtocolViolations() = %q, want no meta.yaml violation", got)
	}
	if !out.OK || len(violations) != 0 {
		t.Fatalf("ValidateArtifactsPreflight() = (%+v, %v), want OK without harness meta.yaml", out, violations)
	}
}

func TestContractRegistryPlanRoadmapPlannerReportsMalformedMeta(t *testing.T) {
	attemptDir := writeRoadmapPlannerAttempt(t, validRoadmapText(), ":\n  :")

	out, violations, err := Validate(feature.PhasePlan, RolePlanRoadmapPlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	got := joinProtocolViolations(violations)
	if !strings.Contains(got, "meta.yaml") || !strings.Contains(got, "unparseable") {
		t.Fatalf("JoinProtocolViolations() = %q, want malformed meta.yaml violation", got)
	}
}

func TestContractRegistryPlanPhasePlanner(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{
		PlanText: validPhasePlanText(),
	})

	contract, ok := lookup(feature.PhasePlan, RolePlanPhasePlanner)
	if !ok {
		t.Fatal("Lookup(PhasePlan, RolePlanPhasePlanner) ok = false, want true")
	}
	if contract.Role != RolePlanPhasePlanner {
		t.Fatalf("Lookup(PhasePlan, RolePlanPhasePlanner).Role = %q, want %q", contract.Role, RolePlanPhasePlanner)
	}

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("Validate() violations = %v, want none", violations)
	}
	if !out.OK {
		t.Fatalf("Validate() outcome = %+v, want OK", out)
	}
}

func TestContractRegistryPlanPhasePlannerAcceptsLegacyPlan(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{PlanText: legacyPhasePlanText()})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 || !out.OK {
		t.Fatalf("Validate() = (%+v, %v), want OK", out, violations)
	}
}

func TestContractRegistryPlanPhasePlannerReportsMissingPlanMarkdown(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := joinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "phase plan markdown") {
		t.Fatalf("JoinProtocolViolations() = %q, want missing phase plan markdown", got)
	}
}

func TestContractRegistryPlanPhasePlannerReportsEmptyPlanMarkdown(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{
		PlanText: "  \n\t\n",
	})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := joinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "phase plan markdown") || !strings.Contains(got, "empty") {
		t.Fatalf("JoinProtocolViolations() = %q, want empty phase plan markdown violation", got)
	}
}

func TestContractRegistryPlanPhasePlannerReportsMissingMeta(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{
		PlanText: validPhasePlanText(),
		SkipMeta: true,
	})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := joinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "meta.yaml") {
		t.Fatalf("JoinProtocolViolations() = %q, want missing meta.yaml violation", got)
	}
}

func TestContractRegistryHasNoLegacyRefactorPlanRole(t *testing.T) {
	// The legacy refactor cycle and its refactor-plan role were removed; the
	// role registry must not carry any refactor-scoped role anymore.
	for _, spec := range roleSpecs {
		if spec.SkillName == "refactor" || strings.Contains(string(spec.Role), "refactor") {
			t.Errorf("legacy refactor role still registered: %+v", spec)
		}
	}
}

func TestContractRegistryArtifactPhaseRolesRequireMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		phase    feature.Phase
		role     Role
		fileName string
		body     string
	}{
		{"inquire", feature.PhaseInquire, RoleInquirer, "2026-05-07-inquire.md", "# Research Questions\n\n1. Which components own repository initialization?\n"},
		{"research", feature.PhaseResearch, RoleResearcher, "2026-05-07-research.md", "# Research findings\n"},
		{"design", feature.PhaseDesign, RoleDesigner, "2026-05-07-design.md", testutil.DesignDocumentMarkdown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			artifactPath := filepath.Join(dir, tt.fileName)
			if err := os.WriteFile(artifactPath, []byte(tt.body), 0o644); err != nil {
				t.Fatalf("write artifact: %v", err)
			}

			contract, ok := lookup(tt.phase, tt.role)
			if !ok {
				t.Fatalf("Lookup(%s, %s) ok = false, want true", tt.phase, tt.role)
			}

			out, violations, err := Validate(tt.phase, tt.role, dir)
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if len(violations) != 0 {
				t.Fatalf("Validate() violations = %v, want none", violations)
			}
			if !out.OK {
				t.Fatal("Validate() OK = false, want true")
			}
			if out.PhaseArtifactPath != artifactPath {
				t.Fatalf("PhaseArtifactPath = %q, want %q", out.PhaseArtifactPath, artifactPath)
			}
			if contract.Role != tt.role {
				t.Fatalf("contract.Role = %q, want %q", contract.Role, tt.role)
			}
		})
	}
}

func TestContractRegistryArtifactPhaseRolesReportMissingMarkdown(t *testing.T) {
	tests := []struct {
		phase feature.Phase
		role  Role
	}{
		{feature.PhaseInquire, RoleInquirer},
		{feature.PhaseResearch, RoleResearcher},
		{feature.PhaseDesign, RoleDesigner},
	}

	for _, tt := range tests {
		t.Run(tt.phase.String(), func(t *testing.T) {
			out, violations, err := Validate(tt.phase, tt.role, t.TempDir())
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if out.OK {
				t.Fatal("Validate() OK = true, want false")
			}
			got := joinProtocolViolations(violations)
			if !strings.Contains(got, "markdown") || !strings.Contains(got, "missing") {
				t.Fatalf("JoinProtocolViolations() = %q, want missing markdown violation", got)
			}
		})
	}
}

func TestContractRegistryArtifactPhaseRolesIgnoreExcludedMarkdown(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "qa-answers.md"), []byte("# ignored\n"), 0o644); err != nil {
		t.Fatalf("write qa-answers.md: %v", err)
	}

	out, violations, err := Validate(feature.PhaseResearch, RoleResearcher, dir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	if got := joinProtocolViolations(violations); !strings.Contains(got, "markdown") {
		t.Fatalf("JoinProtocolViolations() = %q, want markdown violation", got)
	}
}

func TestContractRegistryArtifactPhaseRolesSelectNewestMarkdown(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.md")
	newPath := filepath.Join(dir, "new.md")
	if err := os.WriteFile(oldPath, []byte("# old\n"), 0o644); err != nil {
		t.Fatalf("write old artifact: %v", err)
	}
	if err := os.WriteFile(newPath, []byte(testutil.DesignDocumentMarkdown), 0o644); err != nil {
		t.Fatalf("write new artifact: %v", err)
	}
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old artifact: %v", err)
	}

	out, violations, err := Validate(feature.PhaseDesign, RoleDesigner, dir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 || !out.OK {
		t.Fatalf("Validate() = (%+v, %v), want OK", out, violations)
	}
	if out.PhaseArtifactPath != newPath {
		t.Fatalf("PhaseArtifactPath = %q, want %q", out.PhaseArtifactPath, newPath)
	}
}

func TestContractRegistryImplementerAcceptsAnyProgressNotes(t *testing.T) {
	tests := []struct {
		name  string
		notes string
	}{
		{name: "missing"},
		{name: "free-form", notes: "Wired the handler; the migration is still left.\n"},
		{name: "legacy template", notes: "# Iteration Progress\n\n## Iteration Handoff\n- done\n\n## Iteration State\n\nSUCCESS\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iterDir := t.TempDir()
			if tt.notes != "" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(iterDir), "progress.md"), []byte(tt.notes), 0o644); err != nil {
					t.Fatalf("write progress: %v", err)
				}
			}
			out, violations, err := Validate(feature.PhaseImplement, roleImplementer, iterDir)
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if len(violations) != 0 || !out.OK {
				t.Fatalf("Validate() = (%+v, %v), want OK", out, violations)
			}
		})
	}
}

func TestContractRegistryFinalReviewFixerRequiresNoReportArtifact(t *testing.T) {
	contract, ok := lookup(feature.PhaseReview, roleFinalReviewFixer)
	if !ok {
		t.Fatal("Lookup(PhaseReview, RoleFinalReviewFixer) ok = false, want true")
	}
	if contract.Role != roleFinalReviewFixer {
		t.Fatalf("Lookup(PhaseReview, RoleFinalReviewFixer).Role = %q, want %q", contract.Role, roleFinalReviewFixer)
	}

	// An empty fix iteration dir must validate cleanly.
	out, violations, err := Validate(feature.PhaseReview, roleFinalReviewFixer, t.TempDir())
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("Validate() violations = %v, want none", violations)
	}
	if !out.OK {
		t.Fatalf("Validate() outcome = %+v, want OK", out)
	}
}

func TestContractRegistryPlanValidatorRequiresAxisFeedback(t *testing.T) {
	helperDir := filepath.Join(t.TempDir(), "validate-scope")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeReviewFeedbackFile(t, filepath.Join(helperDir, "validation-scope-feedback.md"), testutil.StructuredReviewFeedback("", "", agentStatusApproved))

	contract, ok := lookup(feature.PhasePlan, roleValidateRoadmapScope)
	if !ok {
		t.Fatal("Lookup(PhasePlan, RoleValidateRoadmapScope) ok = false, want true")
	}
	if contract.Role != roleValidateRoadmapScope {
		t.Fatalf("Lookup(PhasePlan, RoleValidateRoadmapScope).Role = %q, want %q", contract.Role, roleValidateRoadmapScope)
	}

	out, violations, err := Validate(feature.PhasePlan, roleValidateRoadmapScope, helperDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 || !out.OK || out.ReviewFeedback == nil {
		t.Fatalf("Validate() = (%+v, %v), want parsed plan-validator feedback", out, violations)
	}
}

func TestContractRegistryPlanValidatorIgnoresHelperAxisApprovalArtifact(t *testing.T) {
	helperDir := filepath.Join(t.TempDir(), "validate-scope")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeReviewFeedbackFile(t, filepath.Join(helperDir, "validation-scope-feedback.md"), testutil.StructuredReviewFeedback("", "", agentStatusApproved))
	if err := os.WriteFile(filepath.Join(helperDir, "axis-approved-scope.md"), []byte(`axis: scope
verdict: APPROVED
frozen_sections:
- File Structure
`), 0o644); err != nil {
		t.Fatalf("write axis approval: %v", err)
	}

	out, violations, err := Validate(feature.PhasePlan, roleValidateRoadmapScope, helperDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 || !out.OK {
		t.Fatalf("Validate() = (%+v, %v), want helper-local axis approval ignored", out, violations)
	}
}

func TestContractRegistryPlanValidatorReportsMissingAxisFeedback(t *testing.T) {
	helperDir := filepath.Join(t.TempDir(), "validate-scope")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	out, violations, err := Validate(feature.PhasePlan, roleValidateRoadmapScope, helperDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := joinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "validation-scope-feedback.md") {
		t.Fatalf("Validate() = (%+v, %q), want missing validation-scope-feedback.md", out, got)
	}
}

func TestContractRegistryRejectsLegacyGenericPlanValidator(t *testing.T) {
	if _, ok := lookup(feature.PhasePlan, Role("plan_validator")); ok {
		t.Fatal("Lookup(PhasePlan, plan_validator) ok = true, want false; validators must use per-axis RoleSpecs")
	}
}

func TestContractRegistryRejectsLegacyIterationReviewer(t *testing.T) {
	if _, ok := lookup(feature.PhaseReview, Role("iteration_reviewer")); ok {
		t.Fatal("Lookup(PhaseReview, iteration_reviewer) ok = true, want false; implementation review must use per-axis RoleSpecs")
	}
}

func TestContractRegistryImplementationReviewAxisRequiresReviewFeedback(t *testing.T) {
	helperDir := filepath.Join(t.TempDir(), "review", "craft")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeReviewFeedbackFile(t, filepath.Join(helperDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", agentStatusApproved))

	contract, ok := lookup(feature.PhaseReview, RoleImplementationReviewCraft)
	if !ok {
		t.Fatal("Lookup(PhaseReview, RoleImplementationReviewCraft) ok = false, want true")
	}
	if contract.Role != RoleImplementationReviewCraft {
		t.Fatalf("Lookup(PhaseReview, RoleImplementationReviewCraft).Role = %q, want %q", contract.Role, RoleImplementationReviewCraft)
	}

	out, violations, err := Validate(feature.PhaseReview, RoleImplementationReviewCraft, helperDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 || !out.OK || out.ReviewFeedback == nil {
		t.Fatalf("Validate() = (%+v, %v), want parsed iteration-reviewer feedback", out, violations)
	}
}

func writeReviewFeedbackFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write review feedback: %v", err)
	}
}

func writeRoadmapPlannerAttempt(t *testing.T, roadmapText, metaText string) string {
	t.Helper()

	artifactDir := t.TempDir()
	attemptDir := filepath.Join(artifactDir, "attempt-01")
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(attemptDir) error = %v", err)
	}
	if roadmapText != "" {
		if err := os.WriteFile(filepath.Join(artifactDir, "roadmap.md"), []byte(roadmapText), 0o644); err != nil {
			t.Fatalf("write roadmap: %v", err)
		}
	}
	if metaText != "" {
		if err := os.WriteFile(filepath.Join(attemptDir, "meta.yaml"), []byte(metaText), 0o644); err != nil {
			t.Fatalf("write meta.yaml: %v", err)
		}
	}
	return attemptDir
}

func validRoadmapText() string {
	return "# Roadmap\n\n## Phase 1: Skeleton\n\n### Goal\nShip the skeleton.\n"
}

func validPlanAttemptMetaYAML() string {
	return "attempt: 1\nagent_status: SUCCESS\nreview_status: VALIDATION_PENDING\n"
}

type phasePlannerArtifacts struct {
	PlanText string
	SkipMeta bool
}

func writePhasePlannerAttempt(t *testing.T, artifacts phasePlannerArtifacts) string {
	t.Helper()

	phaseRoot := t.TempDir()
	artifactDir := filepath.Join(phaseRoot, "plan")
	attemptDir := filepath.Join(artifactDir, "attempt-01")
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(attemptDir) error = %v", err)
	}
	if artifacts.PlanText != "" {
		planPath := filepath.Join(artifactDir, "plan.md")
		if err := os.WriteFile(planPath, []byte(artifacts.PlanText), 0o644); err != nil {
			t.Fatalf("write plan.md: %v", err)
		}
	}
	if !artifacts.SkipMeta {
		if err := os.WriteFile(filepath.Join(attemptDir, "meta.yaml"), []byte(validPlanAttemptMetaYAML()), 0o644); err != nil {
			t.Fatalf("write meta.yaml: %v", err)
		}
	}
	_ = phaseRoot
	return attemptDir
}

func validPhasePlanText() string {
	return "# Phase 1 Plan\n\n" +
		"## Metadata\n\n**Frontend:** false\n\n" +
		"## Overview\nShip the phase.\n\n" +
		"## Tasks\n\n" +
		"### Task 1: Build the slice\n\n" +
		"#### What to build\nImplement the phase behavior.\n\n" +
		"#### Acceptance criteria\n- [ ] Behavior is complete.\n\n" +
		"#### Blocked by\nNone - can start immediately\n\n" +
		"## Verification\n\nRun `go test ./...` and check the new endpoint answers.\n"
}

func legacyPhasePlanText() string {
	return "# Phase 1 Plan\n\n" +
		"## Overview\nShip the phase.\n\n" +
		"## Tasks\n\n" +
		"### Task 1: Build the slice\n\n" +
		"#### Acceptance criteria\n- [ ] Behavior is complete.\n\n" +
		"## Success Criteria\n\n" +
		"### Automated Verification\n- [ ] Tests pass: `go test ./...` [repo: api]\n\n" +
		"### Visual Evidence\n- [ ] None required: no user-facing rendered surface.\n"
}
