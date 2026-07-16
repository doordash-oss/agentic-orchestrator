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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestContractRegistryImplementerLookup(t *testing.T) {
	contract, ok := Lookup(feature.PhaseImplement, RoleImplementer)
	if !ok {
		t.Fatal("Lookup(PhaseImplement, RoleImplementer) ok = false, want true")
	}
	if contract.Role != RoleImplementer {
		t.Fatalf("Lookup(PhaseImplement, RoleImplementer).Role = %q, want %q", contract.Role, RoleImplementer)
	}
	if len(contract.Required) != 1 {
		t.Fatalf("Lookup(PhaseImplement, RoleImplementer) required artifacts = %d, want 1", len(contract.Required))
	}
}

func TestContractRegistryPlanRoadmapPlanner(t *testing.T) {
	attemptDir := writeRoadmapPlannerAttempt(t, validRoadmapText(), validPlanAttemptMetaYAML())

	contract, ok := Lookup(feature.PhasePlan, RolePlanRoadmapPlanner)
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
	got := JoinProtocolViolations(violations)
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
	got := JoinProtocolViolations(violations)
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
	got := JoinProtocolViolations(violations)
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
	got := JoinProtocolViolations(violations)
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
	got := JoinProtocolViolations(violations)
	if !strings.Contains(got, "meta.yaml") || !strings.Contains(got, "unparseable") {
		t.Fatalf("JoinProtocolViolations() = %q, want malformed meta.yaml violation", got)
	}
}

func TestContractRegistryPlanPhasePlanner(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{
		PlanText: validPhasePlanText(),
	})

	contract, ok := Lookup(feature.PhasePlan, RolePlanPhasePlanner)
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

func TestContractRegistryPlanPhasePlannerReportsMissingPlanMarkdown(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
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
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "phase plan markdown") || !strings.Contains(got, "empty") {
		t.Fatalf("JoinProtocolViolations() = %q, want empty phase plan markdown violation", got)
	}
}

func TestContractRegistryPlanPhasePlannerReportsMissingEvidenceSections(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{
		PlanText: "# Phase 1 Plan\n\n" +
			"## Overview\nShip the phase.\n\n" +
			"## Tasks\n\n" +
			"### Task 1: Build\n\n" +
			"#### What to build\nDo the work.\n\n" +
			"#### Acceptance criteria\n- [ ] Done.\n\n" +
			"#### Blocked by\nNone - can start immediately\n\n" +
			"## Success Criteria\n\n" +
			"### Automated Verification\n- [ ] Tests pass: `go test ./...`\n\n" +
			"### Manual Verification\n- [ ] None required: internal-only phase.\n",
	})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "### Visual Evidence") || !strings.Contains(got, "### Behavioral Evidence") {
		t.Fatalf("JoinProtocolViolations() = %q, want missing evidence section violations", got)
	}
}

func TestContractRegistryPlanPhasePlannerReportsMissingAutomatedVerification(t *testing.T) {
	plan := strings.Replace(validPhasePlanText(), "### Automated Verification\n- [ ] Tests pass: `go test ./...`\n\n", "", 1)
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{PlanText: plan})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "### Automated Verification") {
		t.Fatalf("Validate() = (%+v, %q), want missing automated verification violation", out, got)
	}
}

func TestContractRegistryPlanPhasePlannerAllowsJustifiedNoAutomatedVerification(t *testing.T) {
	plan := strings.Replace(validPhasePlanText(), "- [ ] Tests pass: `go test ./...`", "- [ ] None required: documentation-only change with no executable behavior.", 1)
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{PlanText: plan})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !out.OK || len(violations) != 0 {
		t.Fatalf("Validate() = (%+v, %v), want justified no-automation plan accepted", out, violations)
	}
}

func TestContractRegistryPlanPhasePlannerRejectsUnscopedMultiRepoCommand(t *testing.T) {
	plan := strings.Replace(validPhasePlanText(), "### Task 1: Build the slice\n", "### Task 1: Build the slice\n\n**Repo:** api\n", 1)
	plan = strings.Replace(plan, "## Success Criteria", "### Task 2: Build the client\n\n**Repo:** web\n\n#### What to build\n\nBuild it.\n\n#### Acceptance criteria\n\n- [ ] It works.\n\n#### Blocked by\n\nNone - can start immediately.\n\n## Success Criteria", 1)
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{PlanText: plan})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "[repo: <name>]") {
		t.Fatalf("Validate() = (%+v, %q), want explicit multi-repo verification scope violation", out, got)
	}
}

func TestContractRegistryPlanPhasePlannerAcceptsScopedMultiRepoCommands(t *testing.T) {
	plan := strings.Replace(validPhasePlanText(), "### Task 1: Build the slice\n", "### Task 1: Build the slice\n\n**Repo:** api\n", 1)
	plan = strings.Replace(plan, "## Success Criteria", "### Task 2: Build the client\n\n**Repo:** web\n\n#### What to build\n\nBuild it.\n\n#### Acceptance criteria\n\n- [ ] It works.\n\n#### Blocked by\n\nNone - can start immediately.\n\n## Success Criteria", 1)
	plan = strings.Replace(plan, "- [ ] Tests pass: `go test ./...`", "- [ ] [repo: api] API tests pass: `go test ./...`\n- [ ] [repo: web] Web tests pass: `npm test`", 1)
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{PlanText: plan})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !out.OK || len(violations) != 0 {
		t.Fatalf("Validate() = (%+v, %v), want scoped multi-repo verification accepted", out, violations)
	}
}

func TestContractRegistryPlanPhasePlannerRejectsEmptyNoneReasonAndManualCommand(t *testing.T) {
	plan := strings.Replace(validPhasePlanText(), "- [ ] Tests pass: `go test ./...`", "- [ ] None required:", 1)
	plan = strings.Replace(plan, "- [ ] None required: internal-only test fixture.", "- [ ] Inspect by running `make test`.", 1)
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{PlanText: plan})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "concrete reason") || !strings.Contains(got, "without executable backtick commands") {
		t.Fatalf("Validate() = (%+v, %q), want empty None reason and disguised manual command rejected", out, got)
	}
}

func TestContractRegistryPlanPhasePlannerRejectsMultipleManualArtifacts(t *testing.T) {
	plan := strings.Replace(validPhasePlanText(),
		"- [ ] None required: internal-only test fixture.",
		"- [ ] Inspect the primary journey.\n- [ ] Inspect the same journey again.", 1)
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{PlanText: plan})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "single consolidated") {
		t.Fatalf("Validate() = (%+v, %q), want consolidated manual evidence violation", out, got)
	}
}

func TestContractRegistryPlanPhasePlannerRejectsMultipleBehavioralArtifacts(t *testing.T) {
	plan := strings.Replace(validPhasePlanText(),
		"- [ ] None required: automated tests provide the artifact.",
		"- [ ] Capture the primary journey trace.\n- [ ] Capture a second log for the same journey.", 1)
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{PlanText: plan})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "one consolidated primary-journey artifact") {
		t.Fatalf("Validate() = (%+v, %q), want consolidated behavioral evidence violation", out, got)
	}
}

func TestContractRegistryPlanPhasePlannerReportsTaskScopedEvidenceSections(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{
		PlanText: "# Phase 1 Plan\n\n" +
			"## Overview\nShip the phase.\n\n" +
			"## Tasks\n\n" +
			"### Task 1: Build\n\n" +
			"#### What to build\nDo the work.\n\n" +
			"#### Acceptance criteria\n- [ ] Done.\n\n" +
			"#### Visual Evidence\n- [ ] Capture only inside the task.\n\n" +
			"#### Behavioral Evidence\n- [ ] Attach only inside the task.\n\n" +
			"#### Blocked by\nNone - can start immediately\n\n" +
			"## Success Criteria\n\n" +
			"### Automated Verification\n- [ ] Tests pass: `go test ./...`\n\n" +
			"### Manual Verification\n- [ ] None required: internal-only phase.\n\n" +
			"### Visual Evidence\n- [ ] None required: no UI surface.\n\n" +
			"### Behavioral Evidence\n- [ ] None required: no user journey artifact.\n",
	})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "Task 1") || !strings.Contains(got, "phase-level") {
		t.Fatalf("JoinProtocolViolations() = %q, want task-scoped evidence violation", got)
	}
}

func TestContractRegistryPlanPhasePlannerRejectsFrontendWithoutRealVisualEvidence(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{
		PlanText: "# Phase 1 Plan\n\n" +
			"## Metadata\n\n" +
			"**Frontend:** true\n\n" +
			"## Overview\nShip the phase.\n\n" +
			"## Tasks\n\n" +
			"### Task 1: Build\n\n" +
			"#### What to build\nDo the work.\n\n" +
			"#### Acceptance criteria\n- [ ] Done.\n\n" +
			"#### Blocked by\nNone - can start immediately\n\n" +
			"## Success Criteria\n\n" +
			"### Automated Verification\n- [ ] Tests pass: `go test ./...`\n\n" +
			"### Manual Verification\n- [ ] None required: internal-only phase.\n\n" +
			"### Visual Evidence\n- [ ] None required: no UI surface.\n\n" +
			"### Behavioral Evidence\n- [ ] None required: no user journey artifact.\n",
	})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "frontend/visual-evidence rule") || !strings.Contains(got, "real checklist visual evidence requirement") {
		t.Fatalf("JoinProtocolViolations() = %q, want frontend visual evidence rule violation", got)
	}
}

func TestContractRegistryPlanPhasePlannerAcceptsFrontendWithRealVisualEvidence(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{
		PlanText: "# Phase 1 Plan\n\n" +
			"## Metadata\n\n" +
			"**Frontend:** true\n\n" +
			"## Overview\nShip the phase.\n\n" +
			"## Tasks\n\n" +
			"### Task 1: Build\n\n" +
			"#### What to build\nDo the work.\n\n" +
			"#### Acceptance criteria\n- [ ] Done.\n\n" +
			"#### Blocked by\nNone - can start immediately\n\n" +
			"## Success Criteria\n\n" +
			"### Automated Verification\n- [ ] Tests pass: `go test ./...`\n\n" +
			"### Manual Verification\n- [ ] None required: internal-only phase.\n\n" +
			"### Visual Evidence\n- [ ] Capture the dashboard state after the UI update.\n\n" +
			"### Behavioral Evidence\n- [ ] None required: no user journey artifact.\n",
	})

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

func TestContractRegistryPlanPhasePlannerAllowsNonFrontendNoneRequiredVisualEvidence(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{
		PlanText: "# Phase 1 Plan\n\n" +
			"## Metadata\n\n" +
			"**Frontend:** false\n\n" +
			"## Overview\nShip the phase.\n\n" +
			"## Tasks\n\n" +
			"### Task 1: Build\n\n" +
			"#### What to build\nDo the work.\n\n" +
			"#### Acceptance criteria\n- [ ] Done.\n\n" +
			"#### Blocked by\nNone - can start immediately\n\n" +
			"## Success Criteria\n\n" +
			"### Automated Verification\n- [ ] Tests pass: `go test ./...`\n\n" +
			"### Manual Verification\n- [ ] None required: internal-only phase.\n\n" +
			"### Visual Evidence\n- [ ] None required: no rendered surface.\n\n" +
			"### Behavioral Evidence\n- [ ] None required: no user journey artifact.\n",
	})

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

func TestContractRegistryPlanPhasePlannerReportsMissingMeta(t *testing.T) {
	attemptDir := writePhasePlannerAttempt(t, phasePlannerArtifacts{
		PlanText: validPhasePlanText(),
		SkipMeta: true,
	})

	out, violations, err := Validate(feature.PhasePlan, RolePlanPhasePlanner, attemptDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "meta.yaml") {
		t.Fatalf("JoinProtocolViolations() = %q, want missing meta.yaml violation", got)
	}
}

func TestContractRegistryRefactorPlanStep(t *testing.T) {
	refactorDir := t.TempDir()
	planPath := filepath.Join(refactorDir, "refactor-plan.md")
	if err := os.WriteFile(planPath, []byte(validRefactorPlanText()), 0o644); err != nil {
		t.Fatalf("write refactor plan: %v", err)
	}

	contract, ok := Lookup(feature.PhasePlan, RoleRefactorPlanStep)
	if !ok {
		t.Fatal("Lookup(PhasePlan, RoleRefactorPlanStep) ok = false, want true")
	}
	if contract.Role != RoleRefactorPlanStep {
		t.Fatalf("Lookup(PhasePlan, RoleRefactorPlanStep).Role = %q, want %q", contract.Role, RoleRefactorPlanStep)
	}

	out, violations, err := Validate(feature.PhasePlan, RoleRefactorPlanStep, refactorDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 || !out.OK {
		t.Fatalf("Validate() = (%+v, %v), want OK", out, violations)
	}
	if out.PlanMarkdownPath != planPath {
		t.Fatalf("PlanMarkdownPath = %q, want %q", out.PlanMarkdownPath, planPath)
	}
}

func TestContractRegistryRefactorPlanStepReportsMissingPlan(t *testing.T) {
	out, violations, err := Validate(feature.PhasePlan, RoleRefactorPlanStep, t.TempDir())
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "refactor-plan.md") || !strings.Contains(got, "missing") {
		t.Fatalf("Validate() = (%+v, %q), want missing refactor-plan.md", out, got)
	}
}

func TestContractRegistryRefactorPlanStepReportsEmptyPlan(t *testing.T) {
	refactorDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(refactorDir, "refactor-plan.md"), []byte(" \n\t\n"), 0o644); err != nil {
		t.Fatalf("write empty refactor plan: %v", err)
	}

	out, violations, err := Validate(feature.PhasePlan, RoleRefactorPlanStep, refactorDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "refactor-plan.md") || !strings.Contains(got, "empty") {
		t.Fatalf("Validate() = (%+v, %q), want empty refactor-plan.md", out, got)
	}
}

func TestContractRegistryRefactorPlanStepRejectsUnsupportedCrossRepoVerification(t *testing.T) {
	refactorDir := t.TempDir()
	plan := validRefactorPlanText() + "\n## Cross-Repo Verification\n\n- [ ] Smoke: `scripts/e2e.sh`\n"
	if err := os.WriteFile(filepath.Join(refactorDir, "refactor-plan.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}

	out, violations, err := Validate(feature.PhasePlan, RoleRefactorPlanStep, refactorDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "unsupported Cross-Repo Verification") {
		t.Fatalf("Validate() = (%+v, %q), want unsupported cross-repo verification rejected", out, got)
	}
}

func TestContractRegistryRefactorPlanStepRequiresExactlyOneRepoPerTask(t *testing.T) {
	refactorDir := t.TempDir()
	tests := map[string]string{
		"missing":  "# Refactor: test\n\n## Tasks\n\n### Task 1: unowned\n\nTouch files.\n",
		"multiple": "# Refactor: test\n\n## Tasks\n\n### Task 1: ambiguous\n\n**Repo:** api\n**Repo:** web\n",
	}
	for name, plan := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(refactorDir, name, "refactor-plan.md")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(plan), 0o644); err != nil {
				t.Fatal(err)
			}
			out, violations, err := Validate(feature.PhasePlan, RoleRefactorPlanStep, filepath.Dir(path))
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			got := JoinProtocolViolations(violations)
			if out.OK || !strings.Contains(got, "exactly one `**Repo:** <name>` tag") {
				t.Fatalf("Validate() = (%+v, %q), want exactly-one-repo rejection", out, got)
			}
		})
	}
}

func TestContractKnowledgeBaseBuilderRequiresIndexPresence(t *testing.T) {
	kbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kbDir, "index.md"), []byte("# repo kb\n"), 0o644); err != nil {
		t.Fatalf("write index.md: %v", err)
	}

	contract, ok := Lookup(feature.PhaseKnowledgeBase, RoleKnowledgeBaseBuilder)
	if !ok {
		t.Fatal("Lookup(PhaseKnowledgeBase, RoleKnowledgeBaseBuilder) ok = false, want true")
	}
	if contract.Role != RoleKnowledgeBaseBuilder {
		t.Fatalf("Lookup(PhaseKnowledgeBase, RoleKnowledgeBaseBuilder).Role = %q, want %q", contract.Role, RoleKnowledgeBaseBuilder)
	}

	out, violations, err := Validate(feature.PhaseKnowledgeBase, RoleKnowledgeBaseBuilder, kbDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !out.OK {
		t.Fatalf("Validate() OK = false, violations = %#v", violations)
	}
	if len(violations) != 0 {
		t.Fatalf("Validate() violations = %#v, want none", violations)
	}
}

func TestContractKnowledgeBaseBuilderReportsMissingIndex(t *testing.T) {
	kbDir := t.TempDir()

	out, violations, err := Validate(feature.PhaseKnowledgeBase, RoleKnowledgeBaseBuilder, kbDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	if len(violations) != 1 {
		t.Fatalf("Validate() violations = %#v, want one", violations)
	}
	if violations[0].Artifact != "index.md" {
		t.Fatalf("ProtocolViolation.Artifact = %q, want index.md", violations[0].Artifact)
	}
	if !strings.Contains(violations[0].Reason, "missing") {
		t.Fatalf("ProtocolViolation.Reason = %q, want missing index reason", violations[0].Reason)
	}
}

func TestContractKnowledgeBaseBuilderIndexPresenceOnly(t *testing.T) {
	kbDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(kbDir, "index.md"), nil, 0o644); err != nil {
		t.Fatalf("write empty index.md: %v", err)
	}

	out, violations, err := Validate(feature.PhaseKnowledgeBase, RoleKnowledgeBaseBuilder, kbDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !out.OK || len(violations) != 0 {
		t.Fatalf("Validate() = outcome=%#v violations=%#v, want presence-only success", out, violations)
	}
}

func TestContractRegistryArtifactPhaseRolesRequireMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		phase    feature.Phase
		role     Role
		fileName string
	}{
		{"inquire", feature.PhaseInquire, RoleInquirer, "2026-05-07-inquire.md"},
		{"research", feature.PhaseResearch, RoleResearcher, "2026-05-07-research.md"},
		{"design", feature.PhaseDesign, RoleDesigner, "2026-05-07-design.md"},
		// Legacy Design role still resolves and validates the same
		// markdown artifact behavior so older runs continue to complete.
		{"design", feature.PhaseDesign, RoleDesigner, "2026-05-07-design.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			artifactPath := filepath.Join(dir, tt.fileName)
			if err := os.WriteFile(artifactPath, []byte("# artifact\n"), 0o644); err != nil {
				t.Fatalf("write artifact: %v", err)
			}

			contract, ok := Lookup(tt.phase, tt.role)
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
			got := JoinProtocolViolations(violations)
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
	if got := JoinProtocolViolations(violations); !strings.Contains(got, "markdown") {
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
	if err := os.WriteFile(newPath, []byte("# new\n"), 0o644); err != nil {
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

func TestContractRegistryImplementer(t *testing.T) {
	iterDir := t.TempDir()
	artifactDir := filepath.Dir(iterDir)
	progressPath := filepath.Join(artifactDir, "progress.md")
	writeValidProgress(t, progressPath, "", StateSuccess)

	out, violations, err := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("Validate() violations = %v, want none", violations)
	}
	if !out.OK || out.Progress == nil || out.VerificationReport != nil {
		t.Fatalf("Validate() outcome = %+v, want parsed progress only", out)
	}
}

func TestContractRegistryImplementerReportsMissingArtifacts(t *testing.T) {
	iterDir := t.TempDir()

	out, violations, err := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	got := JoinProtocolViolations(violations)
	if !strings.Contains(got, "progress.md") {
		t.Fatalf("JoinProtocolViolations() = %q, want progress.md", got)
	}
}

func TestContractRegistryImplementerReportsMalformedProgress(t *testing.T) {
	iterDir := t.TempDir()
	artifactDir := filepath.Dir(iterDir)
	progressPath := filepath.Join(artifactDir, "progress.md")
	reportPath := filepath.Join(iterDir, "verification-report.yaml")

	if err := os.WriteFile(progressPath, []byte("# Iteration Progress\n\n## Iteration Handoff\n- malformed\n"), 0o644); err != nil {
		t.Fatalf("write malformed progress: %v", err)
	}
	if err := WriteVerificationReport(reportPath, BuildVerificationReportStub(nil)); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	out, violations, err := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	got := JoinProtocolViolations(violations)
	if !strings.Contains(got, "progress.md") || !strings.Contains(got, "Iteration State") {
		t.Fatalf("JoinProtocolViolations() = %q, want progress.md Iteration State violation", got)
	}
}

func TestContractRegistryImplementerAllowsRetryWithNotRunVerification(t *testing.T) {
	iterDir := t.TempDir()
	artifactDir := filepath.Dir(iterDir)
	progressPath := filepath.Join(artifactDir, "progress.md")
	reportPath := filepath.Join(iterDir, "verification-report.yaml")

	writeValidProgress(t, progressPath, reportPath, StateRetry)
	report := BuildVerificationReportStub([]RequiredVerificationItem{
		{Name: "Relevant tests pass", Requirement: "go test ./..."},
	})
	if err := WriteVerificationReport(reportPath, report); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	out, violations, err := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("Validate() violations = %v, want none for RETRY with not_run verification", violations)
	}
	if !out.OK {
		t.Fatal("Validate() OK = false, want true for RETRY with not_run verification")
	}
}

func TestContractRegistryImplementerParsesNeedUserInputWhenProgressNeedsUserInput(t *testing.T) {
	iterDir := t.TempDir()
	artifactDir := filepath.Dir(iterDir)
	progressPath := filepath.Join(artifactDir, "progress.md")
	reportPath := filepath.Join(iterDir, "verification-report.yaml")
	gatePath := NeedUserInputPath(iterDir)

	writeNeedUserInputProgress(t, progressPath, reportPath, "Need a product decision.", []string{"Legacy path or new service?"})
	if err := WriteVerificationReport(reportPath, BuildVerificationReportStub(nil)); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}
	writeNeedUserInputGate(t, gatePath, "Gate summary from yaml", []string{"Legacy path or new service?"})

	out, violations, err := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 || !out.OK {
		t.Fatalf("Validate() = (%+v, %v), want OK", out, violations)
	}
	if out.NeedUserInput == nil || out.NeedUserInput.Summary != "Gate summary from yaml" {
		t.Fatalf("NeedUserInput = %+v, want parsed yaml payload", out.NeedUserInput)
	}
}

func TestContractRegistryImplementerReportsMissingNeedUserInput(t *testing.T) {
	iterDir := t.TempDir()
	artifactDir := filepath.Dir(iterDir)
	progressPath := filepath.Join(artifactDir, "progress.md")
	reportPath := filepath.Join(iterDir, "verification-report.yaml")

	writeNeedUserInputProgress(t, progressPath, reportPath, "Need a product decision.", []string{"Legacy path or new service?"})
	if err := WriteVerificationReport(reportPath, BuildVerificationReportStub(nil)); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	out, violations, err := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	got := JoinProtocolViolations(violations)
	if !strings.Contains(got, "need-user-input.yaml") || !strings.Contains(got, "missing") {
		t.Fatalf("JoinProtocolViolations() = %q, want missing need-user-input.yaml", got)
	}
}

func TestContractRegistryImplementerReportsMalformedNeedUserInput(t *testing.T) {
	iterDir := t.TempDir()
	artifactDir := filepath.Dir(iterDir)
	progressPath := filepath.Join(artifactDir, "progress.md")
	reportPath := filepath.Join(iterDir, "verification-report.yaml")

	writeNeedUserInputProgress(t, progressPath, reportPath, "Need a product decision.", []string{"Legacy path or new service?"})
	if err := WriteVerificationReport(reportPath, BuildVerificationReportStub(nil)); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}
	if err := os.WriteFile(NeedUserInputPath(iterDir), []byte("questions: [\n"), 0o644); err != nil {
		t.Fatalf("write malformed need-user-input.yaml: %v", err)
	}

	out, violations, err := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	got := JoinProtocolViolations(violations)
	if !strings.Contains(got, "need-user-input.yaml") || !strings.Contains(got, "unparseable") {
		t.Fatalf("JoinProtocolViolations() = %q, want unparseable need-user-input.yaml", got)
	}
}

func TestContractRegistryImplementerDoesNotRequireNeedUserInputForSuccess(t *testing.T) {
	iterDir := t.TempDir()
	artifactDir := filepath.Dir(iterDir)
	progressPath := filepath.Join(artifactDir, "progress.md")
	reportPath := filepath.Join(iterDir, "verification-report.yaml")

	writeValidProgress(t, progressPath, reportPath, StateSuccess)
	if err := WriteVerificationReport(reportPath, BuildVerificationReportStub(nil)); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	out, violations, err := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 || !out.OK || out.NeedUserInput != nil {
		t.Fatalf("Validate() = (%+v, %v), want OK without need-user-input payload", out, violations)
	}
}

func TestContractRegistryFinalReviewFixerRequiresNoReportArtifact(t *testing.T) {
	contract, ok := Lookup(feature.PhaseReview, RoleFinalReviewFixer)
	if !ok {
		t.Fatal("Lookup(PhaseReview, RoleFinalReviewFixer) ok = false, want true")
	}
	if contract.Role != RoleFinalReviewFixer {
		t.Fatalf("Lookup(PhaseReview, RoleFinalReviewFixer).Role = %q, want %q", contract.Role, RoleFinalReviewFixer)
	}

	// verification-report.yaml is harness-owned: an empty fix iteration dir
	// must validate cleanly instead of demanding an agent-authored report.
	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewFixer, t.TempDir())
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

	contract, ok := Lookup(feature.PhasePlan, RoleValidateRoadmapScope)
	if !ok {
		t.Fatal("Lookup(PhasePlan, RoleValidateRoadmapScope) ok = false, want true")
	}
	if contract.Role != RoleValidateRoadmapScope {
		t.Fatalf("Lookup(PhasePlan, RoleValidateRoadmapScope).Role = %q, want %q", contract.Role, RoleValidateRoadmapScope)
	}

	out, violations, err := Validate(feature.PhasePlan, RoleValidateRoadmapScope, helperDir)
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

	out, violations, err := Validate(feature.PhasePlan, RoleValidateRoadmapScope, helperDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 || !out.OK || out.AxisApproval != nil {
		t.Fatalf("Validate() = (%+v, %v), want helper-local axis approval ignored", out, violations)
	}
}

func TestContractRegistryPlanValidatorReportsMissingAxisFeedback(t *testing.T) {
	helperDir := filepath.Join(t.TempDir(), "validate-scope")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	out, violations, err := Validate(feature.PhasePlan, RoleValidateRoadmapScope, helperDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "validation-scope-feedback.md") {
		t.Fatalf("Validate() = (%+v, %q), want missing validation-scope-feedback.md", out, got)
	}
}

func TestContractRegistryRejectsLegacyGenericPlanValidator(t *testing.T) {
	if _, ok := Lookup(feature.PhasePlan, Role("plan_validator")); ok {
		t.Fatal("Lookup(PhasePlan, plan_validator) ok = true, want false; validators must use per-axis RoleSpecs")
	}
}

func TestContractRegistryRejectsLegacyIterationReviewer(t *testing.T) {
	if _, ok := Lookup(feature.PhaseReview, Role("iteration_reviewer")); ok {
		t.Fatal("Lookup(PhaseReview, iteration_reviewer) ok = true, want false; implementation review must use per-axis RoleSpecs")
	}
}

func TestContractRegistryImplementationReviewAxisRequiresReviewFeedback(t *testing.T) {
	helperDir := filepath.Join(t.TempDir(), "review", "craft")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeReviewFeedbackFile(t, filepath.Join(helperDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", agentStatusApproved))

	contract, ok := Lookup(feature.PhaseReview, RoleImplementationReviewCraft)
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

func writeValidProgress(t *testing.T, path string, reportPath string, state IterationState) {
	t.Helper()
	_ = reportPath
	body := fmt.Sprintf(`# Iteration Progress

## Iteration Handoff

### Completed this iteration
- registry test

### Remaining from the plan
- none

### Where I stopped
- complete

### Gotchas / blockers / in-flight decisions
- none

## Deferrals

`+"```"+`yaml
deferrals: []
closed_deferrals: []
`+"```"+`

## Iteration State

%s
`, state.String())
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write progress: %v", err)
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
		"## Overview\nShip the phase.\n\n" +
		"## Tasks\n\n" +
		"### Task 1: Build the slice\n\n" +
		"#### What to build\nImplement the phase behavior.\n\n" +
		"#### Acceptance criteria\n- [ ] Behavior is complete.\n\n" +
		"#### Blocked by\nNone - can start immediately\n\n" +
		"## Success Criteria\n\n" +
		"### Automated Verification\n- [ ] Tests pass: `go test ./...`\n\n" +
		"### Manual Verification\n- [ ] None required: internal-only test fixture.\n\n" +
		"### Visual Evidence\n- [ ] None required: no user-facing rendered surface.\n\n" +
		"### Behavioral Evidence\n- [ ] None required: automated tests provide the artifact.\n"
}

func validRefactorPlanText() string {
	return "# Refactor: test\n\n## Tasks\n\n### Task 1: touch api\n\n**Repo:** api\n\nTouch the api repo.\n"
}

func writeNeedUserInputProgress(t *testing.T, path string, reportPath string, note string, questions []string) {
	t.Helper()
	var q strings.Builder
	q.WriteString("## Questions for User\n\n")
	for i, question := range questions {
		fmt.Fprintf(&q, "%d. %s\n", i+1, question)
	}
	body := fmt.Sprintf(`# Iteration Progress

## Iteration Handoff

### Completed this iteration
- registry test

### Remaining from the plan
- none

### Where I stopped
- waiting on user

### Gotchas / blockers / in-flight decisions
- user decision required

## Deferrals

~~~yaml
deferrals: []
closed_deferrals: []
~~~

## Verification Report

- **Path**: %s
- **Summary**: registry test

%s
## Iteration State

NEED_USER_INPUT
%s
`, reportPath, q.String(), note)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write progress: %v", err)
	}
}

func writeNeedUserInputGate(t *testing.T, path string, summary string, questions []string) {
	t.Helper()
	rec := NeedUserInputRecord{Summary: summary, Iteration: 1}
	for i, question := range questions {
		rec.Questions = append(rec.Questions, NeedUserInputQuestion{Index: i + 1, Prompt: question})
	}
	if err := WriteNeedUserInputRecord(path, rec); err != nil {
		t.Fatalf("WriteNeedUserInputRecord() error = %v", err)
	}
}

func TestVerificationScopeViolations(t *testing.T) {
	multiRepoTasks := "## Tasks\n\n### Task 1: api work\n**Repo:** `api`\n\nBody.\n\n### Task 2: web work\n**Repo:** `web`\n\nBody.\n"
	tests := []struct {
		name    string
		plan    string
		wantIn  string
		wantLen int
	}{
		{
			name:    "multi-repo unscoped command in success criteria",
			plan:    multiRepoTasks + "\n## Success Criteria\n\n### Automated Verification\n- [ ] Tests pass: `go test ./...`\n",
			wantIn:  "must scope every command",
			wantLen: 1,
		},
		{
			name:    "multi-repo unscoped command in top-level section outside success criteria",
			plan:    "### Automated Verification\n- [ ] Tests pass: `go test ./...`\n\n" + multiRepoTasks,
			wantIn:  "must scope every command",
			wantLen: 1,
		},
		{
			name:    "case-drifted repo tag accepted",
			plan:    multiRepoTasks + "\n## Success Criteria\n\n### Automated Verification\n- [ ] [repo: API] Tests pass: `go test ./...`\n",
			wantLen: 0,
		},
		{
			name:    "unknown repo rejected",
			plan:    multiRepoTasks + "\n## Success Criteria\n\n### Automated Verification\n- [ ] [repo: ghost] Tests pass: `go test ./...`\n",
			wantIn:  "not assigned to any phase task",
			wantLen: 1,
		},
		{
			name:    "per-task step with case-drifted matching repo accepted",
			plan:    "## Tasks\n\n### Task 1: api work\n**Repo:** `api`\n\n#### Automated Verification:\n- [ ] [repo: API] Tests: `go test ./...`\n",
			wantLen: 0,
		},
		{
			name:    "single-repo unscoped accepted",
			plan:    "## Tasks\n\n### Task 1: work\n**Repo:** `solo`\n\nBody.\n\n## Success Criteria\n\n### Automated Verification\n- [ ] Tests pass: `go test ./...`\n",
			wantLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verificationScopeViolations(tt.plan)
			if len(got) != tt.wantLen {
				t.Fatalf("verificationScopeViolations() = %+v, want %d violations", got, tt.wantLen)
			}
			if tt.wantLen > 0 && !strings.Contains(got[0].Reason, tt.wantIn) {
				t.Fatalf("violation = %q, want to contain %q", got[0].Reason, tt.wantIn)
			}
		})
	}
}

func TestValidateAutomatedVerificationSectionParserAgreement(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		wantIn string
	}{
		{
			name: "tilde fence with checklist-looking content accepted",
			body: "### Automated Verification\n- [ ] Tests pass: `go test ./...`\n\n~~~\n- [ ] example: `not a real command`\n~~~\n",
		},
		{
			name: "level-4 sub-heading does not truncate the section",
			body: "### Automated Verification\n- [ ] Tests pass: `go test ./...`\n\n#### Notes\n- [ ] Lint passes: `go vet ./...`\n",
		},
		{
			name:   "command-less checklist item still rejected",
			body:   "### Automated Verification\n- [ ] Tests pass somehow\n",
			wantIn: "one complete executable command",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateAutomatedVerificationSection("## Success Criteria\n\n" + tt.body)
			if tt.wantIn == "" && got != "" {
				t.Fatalf("validateAutomatedVerificationSection() = %q, want no violation", got)
			}
			if tt.wantIn != "" && !strings.Contains(got, tt.wantIn) {
				t.Fatalf("validateAutomatedVerificationSection() = %q, want to contain %q", got, tt.wantIn)
			}
		})
	}
}

func TestValidateManualVerificationSectionAllowsInlineCodeReferences(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		wantIn string
	}{
		{
			name: "inline symbol reference accepted",
			body: "### Manual Verification\n- [ ] Confirm the `Submit` button label reads correctly\n",
		},
		{
			name: "fenced example ignored",
			body: "### Manual Verification\n- [ ] Confirm the error page renders the fallback copy\n\n```\n- [ ] not a check: `go test ./...`\n```\n",
		},
		{
			name:   "command-shaped backtick still rejected",
			body:   "### Manual Verification\n- [ ] Confirm tests pass by running `go test ./...`\n",
			wantIn: "without executable backtick commands",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateManualVerificationSection("## Success Criteria\n\n" + tt.body)
			if tt.wantIn == "" && got != "" {
				t.Fatalf("validateManualVerificationSection() = %q, want no violation", got)
			}
			if tt.wantIn != "" && !strings.Contains(got, tt.wantIn) {
				t.Fatalf("validateManualVerificationSection() = %q, want to contain %q", got, tt.wantIn)
			}
		})
	}
}

func TestValidateRefactorPlanMarkdownScopesTopLevelCommands(t *testing.T) {
	iterDir := t.TempDir()
	planPath := filepath.Join(iterDir, "refactor-plan.md")
	plan := "# Refactor\n\n### Automated Verification\n- [ ] Tests pass: `go test ./...`\n\n" +
		"## Tasks\n\n### Task 1: api work\n**Repo:** `api`\n\nBody.\n\n### Task 2: web work\n**Repo:** `web`\n\nBody.\n"
	if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	var out Outcome
	violations, err := validateRefactorPlanMarkdownArtifact(iterDir, planPath, &out)
	if err != nil {
		t.Fatalf("validateRefactorPlanMarkdownArtifact() error = %v", err)
	}
	found := false
	for _, violation := range violations {
		if strings.Contains(violation.Reason, "must scope every command") {
			found = true
		}
	}
	if !found {
		t.Fatalf("violations = %+v, want unscoped multi-repo top-level command rejected", violations)
	}
}
