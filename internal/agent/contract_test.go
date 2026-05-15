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
	if len(contract.Required) != 2 {
		t.Fatalf("Lookup(PhaseImplement, RoleImplementer) required artifacts = %d, want 2", len(contract.Required))
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
		{"brainstorm", feature.PhaseBrainstorm, RoleBrainstormer, "2026-05-07-brainstorm.md"},
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
		{feature.PhaseBrainstorm, RoleBrainstormer},
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

	out, violations, err := Validate(feature.PhaseBrainstorm, RoleBrainstormer, dir)
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
	reportPath := filepath.Join(iterDir, "verification-report.yaml")

	writeValidProgress(t, progressPath, reportPath, StateSuccess)
	if err := WriteVerificationReport(reportPath, BuildVerificationReportStub(nil)); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	out, violations, err := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("Validate() violations = %v, want none", violations)
	}
	if !out.OK || out.Progress == nil || out.VerificationReport == nil {
		t.Fatalf("Validate() outcome = %+v, want parsed progress and verification report", out)
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
	for _, want := range []string{"progress.md", "verification-report.yaml"} {
		if !strings.Contains(got, want) {
			t.Fatalf("JoinProtocolViolations() = %q, want %q", got, want)
		}
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

func TestContractRegistryImplementerReportsMalformedVerificationReport(t *testing.T) {
	iterDir := t.TempDir()
	artifactDir := filepath.Dir(iterDir)
	progressPath := filepath.Join(artifactDir, "progress.md")
	reportPath := filepath.Join(iterDir, "verification-report.yaml")

	writeValidProgress(t, progressPath, reportPath, StateSuccess)
	if err := os.WriteFile(reportPath, []byte(":\n  :"), 0o644); err != nil {
		t.Fatalf("write malformed verification report: %v", err)
	}

	out, violations, err := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	got := JoinProtocolViolations(violations)
	if !strings.Contains(got, "verification-report.yaml") {
		t.Fatalf("JoinProtocolViolations() = %q, want verification-report.yaml", got)
	}
}

func TestContractRegistryImplementerReportsRejectedVerificationReport(t *testing.T) {
	iterDir := t.TempDir()
	artifactDir := filepath.Dir(iterDir)
	progressPath := filepath.Join(artifactDir, "progress.md")
	reportPath := filepath.Join(iterDir, "verification-report.yaml")

	writeValidProgress(t, progressPath, reportPath, StateSuccess)
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
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	got := JoinProtocolViolations(violations)
	if !strings.Contains(got, "verification-report.yaml") || !strings.Contains(got, "not_run") {
		t.Fatalf("JoinProtocolViolations() = %q, want verification-report.yaml not_run violation", got)
	}
}

func TestContractRegistryImplementerRejectsMissingEvidenceFile(t *testing.T) {
	iterDir := t.TempDir()
	artifactDir := filepath.Dir(iterDir)
	progressPath := filepath.Join(artifactDir, "progress.md")
	reportPath := filepath.Join(iterDir, "verification-report.yaml")
	contractPath := filepath.Join(artifactDir, "testing-contract.yaml")
	contract := CompileTestingContract("## Success Criteria\n### Visual Evidence\n- [ ] Capture the dashboard screenshot.\n", filepath.Join(artifactDir, "plan.md"), "collapsed")
	if err := WriteTestingContract(contractPath, contract); err != nil {
		t.Fatalf("WriteTestingContract() error = %v", err)
	}

	writeValidProgress(t, progressPath, reportPath, StateSuccess)
	report := passedArtifactReportForTest(&contract, contractPath)
	setArtifactEvidenceForTest(&report, VerificationModeVisual, VerificationEvidence{Primary: "screenshots/missing.png"})
	if err := WriteVerificationReport(reportPath, report); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	out, violations, err := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "screenshots/missing.png") {
		t.Fatalf("Validate() = (%+v, %q), want missing evidence file violation", out, got)
	}
}

func TestContractRegistryImplementerLoadsSiblingContractWhenReportPathMissing(t *testing.T) {
	iterDir := t.TempDir()
	artifactDir := filepath.Dir(iterDir)
	progressPath := filepath.Join(artifactDir, "progress.md")
	reportPath := filepath.Join(iterDir, "verification-report.yaml")
	contractPath := filepath.Join(artifactDir, "testing-contract.yaml")
	contract := CompileTestingContract("## Success Criteria\n### Visual Evidence\n- [ ] Capture the dashboard screenshot.\n", filepath.Join(artifactDir, "plan.md"), "collapsed")
	if err := WriteTestingContract(contractPath, contract); err != nil {
		t.Fatalf("WriteTestingContract() error = %v", err)
	}

	writeValidProgress(t, progressPath, reportPath, StateSuccess)
	report := passedArtifactReportForTest(&contract, "")
	setArtifactEvidenceForTest(&report, VerificationModeVisual, VerificationEvidence{Primary: "screenshots/missing.png"})
	if err := WriteVerificationReport(reportPath, report); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	out, violations, err := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "screenshots/missing.png") {
		t.Fatalf("Validate() = (%+v, %q), want sibling contract evidence-file violation", out, got)
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

func TestContractRegistryFinalReviewFixer(t *testing.T) {
	iterDir := t.TempDir()
	reportPath := filepath.Join(iterDir, "verification-report.yaml")

	if err := WriteVerificationReport(reportPath, BuildVerificationReportStub(nil)); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	contract, ok := Lookup(feature.PhaseReview, RoleFinalReviewFixer)
	if !ok {
		t.Fatal("Lookup(PhaseReview, RoleFinalReviewFixer) ok = false, want true")
	}
	if contract.Role != RoleFinalReviewFixer {
		t.Fatalf("Lookup(PhaseReview, RoleFinalReviewFixer).Role = %q, want %q", contract.Role, RoleFinalReviewFixer)
	}

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewFixer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("Validate() violations = %v, want none", violations)
	}
	if !out.OK || out.VerificationReport == nil {
		t.Fatalf("Validate() outcome = %+v, want parsed verification report", out)
	}
}

func TestContractRegistryFinalReviewFixerReportsMissingVerificationReport(t *testing.T) {
	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewFixer, t.TempDir())
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	got := JoinProtocolViolations(violations)
	if !strings.Contains(got, "verification-report.yaml") || !strings.Contains(got, "missing") {
		t.Fatalf("JoinProtocolViolations() = %q, want missing verification-report.yaml", got)
	}
}

func TestContractRegistryFinalReviewFixerReportsMalformedVerificationReport(t *testing.T) {
	iterDir := t.TempDir()
	reportPath := filepath.Join(iterDir, "verification-report.yaml")

	if err := os.WriteFile(reportPath, []byte(":\n  :"), 0o644); err != nil {
		t.Fatalf("write malformed verification report: %v", err)
	}

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewFixer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if out.OK {
		t.Fatal("Validate() OK = true, want false")
	}
	got := JoinProtocolViolations(violations)
	if !strings.Contains(got, "verification-report.yaml") || !strings.Contains(got, "unparseable") {
		t.Fatalf("JoinProtocolViolations() = %q, want unparseable verification-report.yaml", got)
	}
}

func TestContractRegistryFinalReviewFixerRejectsNotRunVerificationReport(t *testing.T) {
	iterDir := t.TempDir()
	reportPath := filepath.Join(iterDir, "verification-report.yaml")
	report := BuildVerificationReportStub([]RequiredVerificationItem{
		{Name: "Relevant tests pass", Requirement: "go test ./..."},
	})
	if err := WriteVerificationReport(reportPath, report); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewFixer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "not_run") {
		t.Fatalf("Validate() = (%+v, %q), want not_run schema violation", out, got)
	}
}

func TestContractRegistryFinalReviewerRejectsApprovedWithIncompleteVerification(t *testing.T) {
	iterDir := t.TempDir()
	writeReviewFeedbackFile(t, filepath.Join(iterDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", "APPROVED"))
	writeNotRunVerificationReport(t, filepath.Join(iterDir, "verification-report.yaml"))

	contract, ok := Lookup(feature.PhaseReview, RoleFinalReviewer)
	if !ok {
		t.Fatal("Lookup(PhaseReview, RoleFinalReviewer) ok = false, want true")
	}
	if contract.Role != RoleFinalReviewer {
		t.Fatalf("Lookup(PhaseReview, RoleFinalReviewer).Role = %q, want %q", contract.Role, RoleFinalReviewer)
	}

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "not_run") {
		t.Fatalf("Validate() = (%+v, %q), want APPROVED final review blocked by not_run verification", out, got)
	}
	if out.ReviewFeedback.Verdict != ReviewApproved {
		t.Fatalf("ReviewFeedback.Verdict = %s, want APPROVED", out.ReviewFeedback.Verdict)
	}
}

func TestContractRegistryFinalReviewerRejectsApprovedWithoutExpectedContractBinding(t *testing.T) {
	reviewDir := t.TempDir()
	iterDir := filepath.Join(reviewDir, "iteration-01")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatalf("mkdir iteration dir: %v", err)
	}
	expectedContractPath := filepath.Join(reviewDir, "testing-contract.yaml")
	writeTestingContractForFinalReview(t, expectedContractPath, 2, "baseline-tests", "go test ./...")
	writeReviewFeedbackFile(t, filepath.Join(iterDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", "APPROVED"))
	if err := WriteVerificationReport(filepath.Join(iterDir, "verification-report.yaml"), VerificationReport{Version: 2}); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "contract_path") {
		t.Fatalf("Validate() = (%+v, %q), want APPROVED final review blocked by missing contract_path", out, got)
	}
}

func TestContractRegistryFinalReviewerRejectsApprovedWithMismatchedContractBinding(t *testing.T) {
	reviewDir := t.TempDir()
	iterDir := filepath.Join(reviewDir, "iteration-01")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatalf("mkdir iteration dir: %v", err)
	}
	expectedContractPath := filepath.Join(reviewDir, "testing-contract.yaml")
	staleContractPath := filepath.Join(reviewDir, "old-testing-contract.yaml")
	writeTestingContractForFinalReview(t, expectedContractPath, 2, "current-tests", "go test ./...")
	staleContract := writeTestingContractForFinalReview(t, staleContractPath, 2, "old-tests", "go test ./old")
	writeReviewFeedbackFile(t, filepath.Join(iterDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", "APPROVED"))
	report := BuildContractVerificationReportStub(staleContract, staleContractPath)
	for i := range report.Results {
		report.Results[i].Status = VerificationStatusPassed
		report.Results[i].Evidence = "old contract passed"
	}
	if err := WriteVerificationReport(filepath.Join(iterDir, "verification-report.yaml"), report); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, expectedContractPath) {
		t.Fatalf("Validate() = (%+v, %q), want APPROVED final review blocked by stale contract_path", out, got)
	}
}

func TestContractRegistryFinalReviewerRejectsApprovedWithMissingImplementationEvidenceFile(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run-001")
	reviewDir := filepath.Join(runDir, "review")
	iterDir := filepath.Join(reviewDir, "iteration-01")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatalf("mkdir iteration dir: %v", err)
	}

	finalReviewContractPath := filepath.Join(reviewDir, "testing-contract.yaml")
	finalReviewContract := writeTestingContractForFinalReview(t, finalReviewContractPath, 1, "baseline-tests", "go test ./...")
	writeReviewFeedbackFile(t, filepath.Join(iterDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", "APPROVED"))
	finalReviewReport := BuildContractVerificationReportStub(finalReviewContract, finalReviewContractPath)
	exitCode := 0
	for i := range finalReviewReport.Results {
		finalReviewReport.Results[i].Status = VerificationStatusPassed
		finalReviewReport.Results[i].EvidenceData = VerificationEvidence{ExitCode: &exitCode, Summary: "passed"}
	}
	if err := WriteVerificationReport(filepath.Join(iterDir, "verification-report.yaml"), finalReviewReport); err != nil {
		t.Fatalf("WriteVerificationReport() final review error = %v", err)
	}

	implRoot := filepath.Join(runDir, "phase-01", "implement")
	implIterDir := filepath.Join(implRoot, "iteration-01")
	if err := os.MkdirAll(implIterDir, 0o755); err != nil {
		t.Fatalf("mkdir implementation iter dir: %v", err)
	}
	if err := NewArtifactManager(implRoot).WriteMeta(implIterDir, IterationMeta{Iteration: 1, AgentStatus: "SUCCESS", ReviewStatus: "skipped"}); err != nil {
		t.Fatalf("WriteMeta() error = %v", err)
	}
	implContractPath := filepath.Join(runDir, "phase-01", "testing-contract.yaml")
	implContract := CompileTestingContract("## Success Criteria\n### Visual Evidence\n- [ ] Capture the dashboard screenshot.\n", filepath.Join(runDir, "phase-01", "plan.md"), "collapsed")
	if err := WriteTestingContract(implContractPath, implContract); err != nil {
		t.Fatalf("WriteTestingContract() impl error = %v", err)
	}
	implReport := passedArtifactReportForTest(&implContract, implContractPath)
	setArtifactEvidenceForTest(&implReport, VerificationModeVisual, VerificationEvidence{Primary: "screenshots/missing.png"})
	if err := WriteVerificationReport(filepath.Join(implIterDir, "verification-report.yaml"), implReport); err != nil {
		t.Fatalf("WriteVerificationReport() impl error = %v", err)
	}

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "screenshots/missing.png") || !strings.Contains(got, "phase-01") {
		t.Fatalf("Validate() = (%+v, %q), want final review blocked by implementation evidence file", out, got)
	}
}

func TestContractRegistryFinalReviewerAuditsLatestCompletedImplementationOnly(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "run-001")
	reviewDir := filepath.Join(runDir, "review")
	iterDir := filepath.Join(reviewDir, "iteration-01")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatalf("mkdir iteration dir: %v", err)
	}

	finalReviewContractPath := filepath.Join(reviewDir, "testing-contract.yaml")
	finalReviewContract := writeTestingContractForFinalReview(t, finalReviewContractPath, 1, "baseline-tests", "go test ./...")
	writeReviewFeedbackFile(t, filepath.Join(iterDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", "APPROVED"))
	finalReviewReport := passedArtifactReportForTest(finalReviewContract, finalReviewContractPath)
	if err := WriteVerificationReport(filepath.Join(iterDir, "verification-report.yaml"), finalReviewReport); err != nil {
		t.Fatalf("WriteVerificationReport() final review error = %v", err)
	}

	implRoot := filepath.Join(runDir, "phase-01", "implement")
	implContractPath := filepath.Join(runDir, "phase-01", "testing-contract.yaml")
	implContract := CompileTestingContract("## Success Criteria\n### Visual Evidence\n- [ ] Capture the dashboard screenshot.\n", filepath.Join(runDir, "phase-01", "plan.md"), "collapsed")
	if err := WriteTestingContract(implContractPath, implContract); err != nil {
		t.Fatalf("WriteTestingContract() impl error = %v", err)
	}

	approvedIterDir := filepath.Join(implRoot, "iteration-01")
	mustWriteEvidenceFile(t, approvedIterDir, "screenshots/dashboard.png")
	if err := NewArtifactManager(implRoot).WriteMeta(approvedIterDir, IterationMeta{Iteration: 1, AgentStatus: "SUCCESS", ReviewStatus: "skipped"}); err != nil {
		t.Fatalf("WriteMeta() approved error = %v", err)
	}
	approvedReport := passedArtifactReportForTest(&implContract, implContractPath)
	setArtifactEvidenceForTest(&approvedReport, VerificationModeVisual, VerificationEvidence{Primary: "screenshots/dashboard.png"})
	if err := WriteVerificationReport(filepath.Join(approvedIterDir, "verification-report.yaml"), approvedReport); err != nil {
		t.Fatalf("WriteVerificationReport() approved impl error = %v", err)
	}

	retryIterDir := filepath.Join(implRoot, "iteration-02")
	if err := os.MkdirAll(retryIterDir, 0o755); err != nil {
		t.Fatalf("mkdir retry iteration dir: %v", err)
	}
	if err := NewArtifactManager(implRoot).WriteMeta(retryIterDir, IterationMeta{Iteration: 2, AgentStatus: "RETRY", ReviewStatus: "skipped_retry"}); err != nil {
		t.Fatalf("WriteMeta() retry error = %v", err)
	}
	retryReport := passedArtifactReportForTest(&implContract, implContractPath)
	setArtifactEvidenceForTest(&retryReport, VerificationModeVisual, VerificationEvidence{Primary: "screenshots/missing-retry.png"})
	if err := WriteVerificationReport(filepath.Join(retryIterDir, "verification-report.yaml"), retryReport); err != nil {
		t.Fatalf("WriteVerificationReport() retry impl error = %v", err)
	}

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !out.OK || len(violations) != 0 {
		t.Fatalf("Validate() = (%+v, %v), want retry iteration ignored during final review evidence re-audit", out, violations)
	}
}

func TestContractRegistryFinalReviewerAllowsChangesRequestedWithIncompleteVerification(t *testing.T) {
	iterDir := t.TempDir()
	writeReviewFeedbackFile(t, filepath.Join(iterDir, "review-feedback.md"), testutil.StructuredReviewFeedback("- needs work", "", "CHANGES_REQUESTED"))
	writeNotRunVerificationReport(t, filepath.Join(iterDir, "verification-report.yaml"))

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 || !out.OK || out.ReviewFeedback == nil || out.VerificationReport == nil {
		t.Fatalf("Validate() = (%+v, %v), want parsed CHANGES_REQUESTED feedback and parse-only report", out, violations)
	}
	if out.ReviewFeedback.Verdict != ReviewChangesRequested {
		t.Fatalf("ReviewFeedback.Verdict = %s, want CHANGES_REQUESTED", out.ReviewFeedback.Verdict)
	}
}

func TestContractRegistryFinalReviewerReportsMissingReviewFeedback(t *testing.T) {
	iterDir := t.TempDir()
	writePassedVerificationReport(t, filepath.Join(iterDir, "verification-report.yaml"))

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "review-feedback.md") || !strings.Contains(got, "not found") {
		t.Fatalf("Validate() = (%+v, %q), want missing review-feedback.md", out, got)
	}
}

func TestContractRegistryFinalReviewerReportsMalformedVerdict(t *testing.T) {
	iterDir := t.TempDir()
	body := "## Findings\n- malformed verdict\n\n## Suggestions\n- (none)\n\n## Verdict\nLGTM\n"
	writeReviewFeedbackFile(t, filepath.Join(iterDir, "review-feedback.md"), body)
	writePassedVerificationReport(t, filepath.Join(iterDir, "verification-report.yaml"))

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "LGTM") {
		t.Fatalf("Validate() = (%+v, %q), want unrecognized verdict violation", out, got)
	}
}

func TestContractRegistryFinalReviewerReportsMissingVerificationReport(t *testing.T) {
	iterDir := t.TempDir()
	writeReviewFeedbackFile(t, filepath.Join(iterDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", "APPROVED"))

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "verification-report.yaml") || !strings.Contains(got, "missing") {
		t.Fatalf("Validate() = (%+v, %q), want missing verification-report.yaml", out, got)
	}
}

func TestContractRegistryFinalReviewerReportsMalformedVerificationReport(t *testing.T) {
	iterDir := t.TempDir()
	writeReviewFeedbackFile(t, filepath.Join(iterDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", "APPROVED"))
	if err := os.WriteFile(filepath.Join(iterDir, "verification-report.yaml"), []byte(":\n  :"), 0o644); err != nil {
		t.Fatalf("write malformed verification report: %v", err)
	}

	out, violations, err := Validate(feature.PhaseReview, RoleFinalReviewer, iterDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	got := JoinProtocolViolations(violations)
	if out.OK || !strings.Contains(got, "verification-report.yaml") || !strings.Contains(got, "unparseable") {
		t.Fatalf("Validate() = (%+v, %q), want malformed verification-report.yaml", out, got)
	}
}

func TestContractRegistryPlanValidatorRequiresAxisFeedback(t *testing.T) {
	helperDir := filepath.Join(t.TempDir(), "validate-scope")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeReviewFeedbackFile(t, filepath.Join(helperDir, "validation-scope-feedback.md"), testutil.StructuredReviewFeedback("", "", "APPROVED"))

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
	writeReviewFeedbackFile(t, filepath.Join(helperDir, "validation-scope-feedback.md"), testutil.StructuredReviewFeedback("", "", "APPROVED"))
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

func TestContractRegistryIterationReviewerRequiresReviewFeedback(t *testing.T) {
	helperDir := filepath.Join(t.TempDir(), "review")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeReviewFeedbackFile(t, filepath.Join(helperDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", "APPROVED"))

	contract, ok := Lookup(feature.PhaseReview, RoleIterationReviewer)
	if !ok {
		t.Fatal("Lookup(PhaseReview, RoleIterationReviewer) ok = false, want true")
	}
	if contract.Role != RoleIterationReviewer {
		t.Fatalf("Lookup(PhaseReview, RoleIterationReviewer).Role = %q, want %q", contract.Role, RoleIterationReviewer)
	}

	out, violations, err := Validate(feature.PhaseReview, RoleIterationReviewer, helperDir)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if len(violations) != 0 || !out.OK || out.ReviewFeedback == nil {
		t.Fatalf("Validate() = (%+v, %v), want parsed iteration-reviewer feedback", out, violations)
	}
}

func TestContractRegistryTweakCarveOut(t *testing.T) {
	out, violations, err := Validate(feature.PhaseImplement, RoleInteractivePTY, t.TempDir())
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !out.OK || len(violations) != 0 {
		t.Fatalf("Validate() = (%+v, %v), want OK empty contract", out, violations)
	}
}

func writeReviewFeedbackFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write review feedback: %v", err)
	}
}

func writePassedVerificationReport(t *testing.T, path string) {
	t.Helper()
	if err := WriteVerificationReport(path, BuildVerificationReportStub(nil)); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}
}

func writeNotRunVerificationReport(t *testing.T, path string) {
	t.Helper()
	report := BuildVerificationReportStub([]RequiredVerificationItem{
		{Name: "Relevant tests pass", Requirement: "go test ./..."},
	})
	if err := WriteVerificationReport(path, report); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}
}

func writeTestingContractForFinalReview(t *testing.T, path string, revision int, itemID string, command string) *TestingContract {
	t.Helper()
	contract := TestingContract{
		Version:  1,
		Revision: revision,
		Scope:    "review",
		Items: []TestingContractItem{
			{
				ID:      itemID,
				Source:  testingContractBaselineSource,
				Repo:    TestingContractCrossRepoTag,
				Name:    "Relevant tests pass",
				Command: command,
				ExpectedEvidence: TestingContractExpectedEvidence{
					Kind:    testingContractEvidenceKind,
					Matcher: testingContractEvidenceMatcher,
				},
				Policy: TestingContractItemPolicy{
					Required: true,
				},
			},
		},
	}
	if err := WriteTestingContract(path, contract); err != nil {
		t.Fatalf("WriteTestingContract() error = %v", err)
	}
	return &contract
}

func writeValidProgress(t *testing.T, path string, reportPath string, state IterationState) {
	t.Helper()
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

## Verification Report

- **Path**: %s
- **Summary**: registry test

## Iteration State

%s
`, reportPath, state.String())
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
