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

func TestValidateVerificationReport_SchemaChecks(t *testing.T) {
	required := []RequiredVerificationItem{
		{Name: "Unit tests", Requirement: "go test ./..."},
		{Name: "Lint", Requirement: "go vet ./..."},
	}

	tests := []struct {
		name           string
		report         *VerificationReport
		phaseComplete  bool
		wantRejected   bool
		wantCategories []ReportGateCategory
		wantDetailHas  []string
	}{
		{
			name: "missing_required_check",
			report: &VerificationReport{
				Version: 1,
				RequiredChecks: []VerificationCheckResult{
					{Name: "Unit tests", Requirement: "go test ./...", Status: VerificationStatusPassed, Evidence: "ok 100 packages"},
					// Lint check intentionally omitted.
				},
			},
			phaseComplete:  true,
			wantRejected:   true,
			wantCategories: []ReportGateCategory{GateCategorySchema},
			wantDetailHas:  []string{"missing from the report", "go vet ./..."},
		},
		{
			name: "passed_with_empty_evidence",
			report: &VerificationReport{
				Version: 1,
				RequiredChecks: []VerificationCheckResult{
					{Name: "Unit tests", Requirement: "go test ./...", Status: VerificationStatusPassed, Evidence: ""},
					{Name: "Lint", Requirement: "go vet ./...", Status: VerificationStatusPassed, Evidence: "clean"},
				},
			},
			phaseComplete:  true,
			wantRejected:   true,
			wantCategories: []ReportGateCategory{GateCategorySchema},
			wantDetailHas:  []string{"evidence is empty"},
		},
		{
			name: "not_run_when_phase_complete",
			report: &VerificationReport{
				Version: 1,
				RequiredChecks: []VerificationCheckResult{
					{Name: "Unit tests", Requirement: "go test ./...", Status: VerificationStatusPassed, Evidence: "ok"},
					{Name: "Lint", Requirement: "go vet ./...", Status: VerificationStatusNotRun},
				},
			},
			phaseComplete:  true,
			wantRejected:   true,
			wantCategories: []ReportGateCategory{GateCategorySchema},
			wantDetailHas:  []string{"not_run", "phase is marked complete"},
		},
		{
			name: "not_run_when_phase_incomplete_is_ok",
			report: &VerificationReport{
				Version: 1,
				RequiredChecks: []VerificationCheckResult{
					{Name: "Unit tests", Requirement: "go test ./...", Status: VerificationStatusNotRun},
					{Name: "Lint", Requirement: "go vet ./...", Status: VerificationStatusNotRun},
				},
			},
			phaseComplete: false,
			wantRejected:  false,
		},
		{
			name: "unknown_status_value",
			report: &VerificationReport{
				Version: 1,
				RequiredChecks: []VerificationCheckResult{
					{Name: "Unit tests", Requirement: "go test ./...", Status: VerificationRunStatus("maybe"), Evidence: "sort of"},
					{Name: "Lint", Requirement: "go vet ./...", Status: VerificationStatusPassed, Evidence: "clean"},
				},
			},
			phaseComplete:  true,
			wantRejected:   true,
			wantCategories: []ReportGateCategory{GateCategorySchema},
			wantDetailHas:  []string{"\"maybe\""},
		},
		{
			name: "raw_pass_short_form_is_normalized",
			report: &VerificationReport{
				Version: 1,
				RequiredChecks: []VerificationCheckResult{
					{Name: "Unit tests", Requirement: "go test ./...", Status: VerificationRunStatus("pass"), Evidence: "ok"},
					{Name: "Lint", Requirement: "go vet ./...", Status: VerificationRunStatus("pass"), Evidence: "clean"},
				},
			},
			phaseComplete: true,
			wantRejected:  false,
		},
		{
			name: "clean_report_passes",
			report: &VerificationReport{
				Version: 1,
				RequiredChecks: []VerificationCheckResult{
					{Name: "Unit tests", Requirement: "go test ./...", Status: VerificationStatusPassed, Evidence: "ok 100 packages, 0 failures"},
					{Name: "Lint", Requirement: "go vet ./...", Status: VerificationStatusPassed, Evidence: "no issues"},
				},
			},
			phaseComplete: true,
			wantRejected:  false,
		},
		{
			name: "failed_status_does_not_reject",
			report: &VerificationReport{
				Version: 1,
				RequiredChecks: []VerificationCheckResult{
					{Name: "Unit tests", Requirement: "go test ./...", Status: VerificationStatusPassed, Evidence: "ok"},
					{Name: "Lint", Requirement: "go vet ./...", Status: VerificationStatusFailed, Evidence: "3 issues found"},
				},
			},
			phaseComplete: true,
			wantRejected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Normalize statuses the way ReadVerificationReport would.
			for i := range tt.report.RequiredChecks {
				tt.report.RequiredChecks[i].Status = NormalizeStatus(tt.report.RequiredChecks[i].Status)
			}
			result := ValidateVerificationReport(tt.report, required, nil, tt.phaseComplete)
			if result.Rejected != tt.wantRejected {
				t.Fatalf("Rejected=%v, want %v; findings=%+v", result.Rejected, tt.wantRejected, result.Findings)
			}
			if tt.wantRejected {
				gotCats := make(map[ReportGateCategory]bool)
				for _, f := range result.Findings {
					gotCats[f.Category] = true
				}
				for _, cat := range tt.wantCategories {
					if !gotCats[cat] {
						t.Errorf("want finding of category %q, got categories %v", cat, gotCats)
					}
				}
				joined := ""
				for _, f := range result.Findings {
					joined += f.Detail + "\n"
				}
				for _, needle := range tt.wantDetailHas {
					if !strings.Contains(joined, needle) {
						t.Errorf("want detail substring %q in findings, got:\n%s", needle, joined)
					}
				}
			}
		})
	}
}

func TestValidateVerificationReport_HedgePhrasesBlockPassClaims(t *testing.T) {
	required := []RequiredVerificationItem{{Name: "Lint", Requirement: "task lint"}}
	phrases := []string{
		"CAVEAT",
		"pre-existing bug",
		"fails on macOS Darwin/BSD xargs",
		"does not yet include an error-mapping interceptor",
		"orthogonal to Phase 3",
		"not yet implemented",
	}

	for _, phrase := range phrases {
		t.Run(phrase, func(t *testing.T) {
			report := &VerificationReport{
				Version: 1,
				RequiredChecks: []VerificationCheckResult{
					{
						Name:        "Lint",
						Requirement: "task lint",
						Status:      VerificationStatusPassed,
						Evidence:    "linter exits 0 — " + phrase,
					},
				},
			}
			result := ValidateVerificationReport(report, required, nil, true)
			if !result.Rejected {
				t.Fatalf("expected rejection for hedge phrase %q, got clean; evidence=%q", phrase, report.RequiredChecks[0].Evidence)
			}
			found := false
			for _, f := range result.Findings {
				if f.Category == GateCategoryHedge {
					found = true
				}
			}
			if !found {
				t.Errorf("expected a GateCategoryHedge finding, got %+v", result.Findings)
			}
		})
	}
}

func TestValidateVerificationReport_ManualPendingHumanRequiresContext(t *testing.T) {
	contract := CompileTestingContract("### Manual Verification\n- [ ] Exercise the primary workflow.\n", "/tmp/phase-01/plan.md", "collapsed")
	report := BuildContractVerificationReportStub(&contract, "/tmp/phase-01/testing-contract.yaml")
	for i := range report.Results {
		if report.Results[i].Mode == VerificationModeManual {
			report.Results[i].Status = VerificationStatusPendingHuman
			continue
		}
		report.Results[i].Status = VerificationStatusPassed
		exitCode := 0
		report.Results[i].EvidenceData = VerificationEvidence{ExitCode: &exitCode, Summary: "passed"}
	}

	result := ValidateVerificationReport(&report, nil, &contract, true)
	if !result.Rejected {
		t.Fatalf("pending_human manual row without context should be rejected")
	}

	for i := range report.Results {
		if report.Results[i].Mode == VerificationModeManual {
			report.Results[i].BlockedReason = "Requires a signed production account owned by release QA."
		}
	}
	result = ValidateVerificationReport(&report, nil, &contract, true)
	if result.Rejected {
		t.Fatalf("pending_human manual row with owner should pass gate: %+v", result.Findings)
	}
}

func TestValidateVerificationReport_PendingHumanNonManualRejected(t *testing.T) {
	contract := CompileTestingContract("### Automated Verification\n- [ ] Agent tests: `go test ./internal/agent/... -count=1`\n", "/tmp/phase-01/plan.md", "collapsed")
	report := BuildContractVerificationReportStub(&contract, "/tmp/phase-01/testing-contract.yaml")
	for i := range report.Results {
		report.Results[i].Status = VerificationStatusPassed
		exitCode := 0
		report.Results[i].EvidenceData = VerificationEvidence{ExitCode: &exitCode, Summary: "passed"}
	}
	report.Results[len(report.Results)-1].Status = VerificationStatusPendingHuman
	report.Results[len(report.Results)-1].EvidenceData = VerificationEvidence{}

	result := ValidateVerificationReport(&report, nil, &contract, true)
	if !result.Rejected {
		t.Fatalf("pending_human command row should be rejected")
	}
}

func TestValidateVerificationReport_EvidenceModesSchemaOnly(t *testing.T) {
	contract := CompileTestingContract(strings.Join([]string{
		"## Success Criteria",
		"### Visual Evidence",
		"- [ ] Capture the dashboard screenshot.",
		"### Behavioral Evidence",
		"- [ ] Attach the workflow transcript.",
	}, "\n"), "/tmp/phase-01/plan.md", "collapsed")
	report := BuildContractVerificationReportStub(&contract, "/tmp/phase-01/testing-contract.yaml")
	for i := range report.Results {
		report.Results[i].Status = VerificationStatusPassed
		report.Results[i].EvidenceData = VerificationEvidence{Summary: "artifact path recorded in the implementation log"}
	}

	result := ValidateVerificationReport(&report, nil, &contract, true)
	if result.Rejected {
		t.Fatalf("visual/behavioral evidence modes should only receive schema validation in this phase: %+v", result.Findings)
	}
}

func TestValidateVerificationReportWithContext_EvidenceFiles(t *testing.T) {
	iterDir := t.TempDir()
	contract := CompileTestingContract(strings.Join([]string{
		"## Success Criteria",
		"### Visual Evidence",
		"- [ ] Capture the dashboard screenshot.",
		"### Behavioral Evidence",
		"- [ ] Attach the workflow transcript.",
	}, "\n"), filepath.Join(iterDir, "plan.md"), "collapsed")

	mustWriteEvidenceFile(t, iterDir, "screenshots/dashboard.png")
	mustWriteEvidenceFile(t, iterDir, "screenshots/dashboard-detail.png")
	mustWriteEvidenceFile(t, iterDir, "behaviors/workflow.log")

	report := passedArtifactReportForTest(&contract, filepath.Join(iterDir, "testing-contract.yaml"))
	setArtifactEvidenceForTest(&report, VerificationModeVisual, VerificationEvidence{
		Primary:     "screenshots/dashboard.png",
		Attachments: []string{"screenshots/dashboard-detail.png"},
	})
	setArtifactEvidenceForTest(&report, VerificationModeBehavioral, VerificationEvidence{
		Summary: "workflow completed",
		Primary: "behaviors/workflow.log",
	})

	result := ValidateVerificationReportWithContext(&report, nil, true, VerificationReportValidationContext{
		IterationDir: iterDir,
		Contract:     &contract,
	})
	if result.Rejected {
		t.Fatalf("ValidateVerificationReportWithContext() rejected valid evidence files: %+v", result.Findings)
	}

	tests := []struct {
		name       string
		mutate     func(*VerificationReport)
		wantDetail string
	}{
		{
			name: "missing_primary",
			mutate: func(report *VerificationReport) {
				setArtifactEvidenceForTest(report, VerificationModeVisual, VerificationEvidence{})
			},
			wantDetail: "evidence.primary",
		},
		{
			name: "wrong_root",
			mutate: func(report *VerificationReport) {
				setArtifactEvidenceForTest(report, VerificationModeVisual, VerificationEvidence{Primary: "behaviors/dashboard.png"})
			},
			wantDetail: "screenshots/",
		},
		{
			name: "traversal",
			mutate: func(report *VerificationReport) {
				setArtifactEvidenceForTest(report, VerificationModeBehavioral, VerificationEvidence{Primary: "behaviors/../workflow.log"})
			},
			wantDetail: "traversal",
		},
		{
			name: "absolute",
			mutate: func(report *VerificationReport) {
				setArtifactEvidenceForTest(report, VerificationModeBehavioral, VerificationEvidence{Primary: filepath.Join(iterDir, "behaviors/workflow.log")})
			},
			wantDetail: "absolute",
		},
		{
			name: "missing_attachment",
			mutate: func(report *VerificationReport) {
				setArtifactEvidenceForTest(report, VerificationModeVisual, VerificationEvidence{
					Primary:     "screenshots/dashboard.png",
					Attachments: []string{"screenshots/missing.png"},
				})
			},
			wantDetail: "screenshots/missing.png",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := report
			candidate.Results = append([]VerificationCheckResult(nil), report.Results...)
			tt.mutate(&candidate)
			result := ValidateVerificationReportWithContext(&candidate, nil, true, VerificationReportValidationContext{
				IterationDir: iterDir,
				Contract:     &contract,
			})
			if !result.Rejected {
				t.Fatalf("ValidateVerificationReportWithContext() Rejected = false, want true")
			}
			joined := reportGateDetailsForTest(result)
			if !strings.Contains(joined, tt.wantDetail) {
				t.Fatalf("ValidateVerificationReportWithContext() details = %q, want %q", joined, tt.wantDetail)
			}
		})
	}
}

func TestValidateVerificationReportWithContext_EvidenceFilesSkippedForBlockedRows(t *testing.T) {
	iterDir := t.TempDir()
	contract := CompileTestingContract("## Success Criteria\n### Visual Evidence\n- [ ] Capture the dashboard screenshot.\n", filepath.Join(iterDir, "plan.md"), "collapsed")
	report := passedArtifactReportForTest(&contract, filepath.Join(iterDir, "testing-contract.yaml"))
	for i := range report.Results {
		if report.Results[i].Mode == VerificationModeVisual {
			report.Results[i].Status = VerificationStatusBlocked
			report.Results[i].BlockedReason = "external screenshot runner unavailable"
			report.Results[i].EvidenceData = VerificationEvidence{}
		}
	}

	result := ValidateVerificationReportWithContext(&report, nil, true, VerificationReportValidationContext{
		IterationDir: iterDir,
		Contract:     &contract,
	})
	if result.Rejected {
		t.Fatalf("ValidateVerificationReportWithContext() rejected blocked row without files: %+v", result.Findings)
	}
}

// TestFormatGateFeedback_ConsolidatesRepetitiveFindings covers the main
// reason gate feedback used to be unreadable: an iteration that writes
// SUCCESS + phase_complete without running any verification produces N
// identical "status is not_run" findings. Feedback should collapse those
// into one actionable bullet that names the affected checks and points
// the agent at the progress.md `## Iteration State: RETRY` protocol.
func TestFormatGateFeedback_ConsolidatesRepetitiveFindings(t *testing.T) {
	report := &VerificationReport{
		Version: 1,
		RequiredChecks: []VerificationCheckResult{
			{Name: "**Headless build works on Linux**: `go build ./...` exits 0.", Requirement: "go build ./...", Status: VerificationStatusNotRun},
			{Name: "**GUI build compiles**: `make build BUILD_TAGS=gui` succeeds.", Requirement: "make build BUILD_TAGS=gui", Status: VerificationStatusNotRun},
			{Name: "**Backend tests green on no-tag build**: `make test` exits 0.", Requirement: "make test", Status: VerificationStatusNotRun},
			{Name: "**Smoke test passes on macOS**: `bash test/e2e/smoke.sh` exits 0.", Requirement: "bash test/e2e/smoke.sh", Status: VerificationStatusNotRun},
		},
	}
	result := ValidateVerificationReport(report, nil, nil, true)
	if !result.Rejected {
		t.Fatalf("expected rejection; findings=%+v", result.Findings)
	}
	feedback := FormatGateFeedback(result)

	// Consolidated: one bullet, not four.
	notRunLines := strings.Count(feedback, "status: not_run")
	if notRunLines > 1 {
		t.Errorf("expected consolidated not_run guidance, but saw %d occurrences of \"status: not_run\". Feedback:\n%s", notRunLines, feedback)
	}
	// Must cite the count and point at the RETRY protocol.
	wantSubstrings := []string{
		"4 verification checks",
		"phase_complete",
		"## Iteration State: RETRY",
		"### Remaining from the plan",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(feedback, s) {
			t.Errorf("feedback missing %q; got:\n%s", s, feedback)
		}
	}
	// Clean names (no leading `**Name**`) in the Affected list.
	if strings.Contains(feedback, "****Headless") || strings.Contains(feedback, "****GUI") {
		t.Errorf("feedback contains unstripped markdown-bold wrappers; got:\n%s", feedback)
	}
	// Actually lists the affected check names.
	for _, want := range []string{"Headless build works on Linux", "GUI build compiles", "Backend tests green on no-tag build"} {
		if !strings.Contains(feedback, want) {
			t.Errorf("feedback missing affected check %q; got:\n%s", want, feedback)
		}
	}
}

func TestFormatGateFeedback_ConsolidatesEmptyEvidence(t *testing.T) {
	report := &VerificationReport{
		Version: 1,
		RequiredChecks: []VerificationCheckResult{
			{Name: "Compile", Requirement: "go build ./...", Status: VerificationStatusPassed, Evidence: ""},
			{Name: "Test", Requirement: "go test ./...", Status: VerificationStatusPassed, Evidence: "  "},
			{Name: "Lint", Requirement: "go vet ./...", Status: VerificationStatusPassed, Evidence: "clean"},
		},
	}
	result := ValidateVerificationReport(report, nil, nil, true)
	feedback := FormatGateFeedback(result)
	if strings.Count(feedback, "evidence is empty") > 1 {
		t.Errorf("expected consolidated empty_evidence guidance; feedback:\n%s", feedback)
	}
	if !strings.Contains(feedback, "2 checks marked `passed` with empty `evidence`") {
		t.Errorf("feedback missing consolidated count/guidance; got:\n%s", feedback)
	}
}

func TestValidateVerificationReport_KnownCaveatsSurfacedNotRejected(t *testing.T) {
	required := []RequiredVerificationItem{{Name: "Lint", Requirement: "task lint"}}
	report := &VerificationReport{
		Version: 1,
		RequiredChecks: []VerificationCheckResult{
			{Name: "Lint", Requirement: "task lint", Status: VerificationStatusPassed, Evidence: "exit 0, 0 issues"},
		},
		KnownCaveats: map[string]string{
			"stream_error_mapping": "Adding a StreamErrorInterceptor is orthogonal to Phase 3 scope.",
		},
	}
	result := ValidateVerificationReport(report, required, nil, true)
	if result.Rejected {
		t.Fatalf("unexpected rejection; findings=%+v", result.Findings)
	}
	if len(result.KnownCaveats) != 1 {
		t.Fatalf("want 1 known caveat surfaced, got %d", len(result.KnownCaveats))
	}
	addendum := KnownCaveatsReviewAddendum(result)
	if !strings.Contains(addendum, "stream_error_mapping") {
		t.Errorf("addendum missing caveat key; got:\n%s", addendum)
	}
	if !strings.Contains(addendum, "phase plan") {
		t.Errorf("addendum missing plan cross-check guidance; got:\n%s", addendum)
	}
}

// TestValidateVerificationReport_RealWorldPaymentsIter01 feeds a trimmed
// regression fixture for an invalid passing verification report. The gate must
// reject it.
func TestValidateVerificationReport_RealWorldPaymentsIter01(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verification-report.yaml")
	// Trimmed verbatim excerpt: the "task lint" check shows `status: pass`
	// with evidence text that admits the lint command actually fails on
	// macOS ("CAVEAT", "fails on macOS", "pre-existing environmental bug").
	// Also carries a real known_caveats section declaring a scope deferral
	// that the phase-03 plan required in-scope.
	content := `version: 1
required_checks:
    - name: '**Compiles**: exits 0.'
      requirement: go build ./...
      mode: verified
      status: pass
      evidence: |
        go build ./... at repo root exit 0. Phase 3 signature expansions compile clean.
    - name: '**Lint clean**: passes.'
      requirement: task lint
      mode: partial
      status: pass
      evidence: |
        ./project-golangci-lint run at repo root reports "0 issues" (exit 0).
        task proto:lint passes.

        CAVEAT: task lint chains a final subtask lint-go-version that runs
        tools/scripts/check-go-versions.sh; that script uses xargs --exit (GNU-only)
        and therefore fails on macOS Darwin/BSD xargs with a usage error. This is a
        pre-existing environmental bug at the BASE commit.
known_caveats:
  stream_error_mapping: |
    The stream path does not yet run errors through the unary grpcErrorMapper.
    Adding a StreamErrorInterceptor is orthogonal to Phase 3 scope.
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	report, err := ReadVerificationReport(path)
	if err != nil {
		t.Fatalf("ReadVerificationReport: %v", err)
	}

	required := []RequiredVerificationItem{
		{Name: "Compiles", Requirement: "go build ./..."},
		{Name: "Lint clean", Requirement: "task lint"},
	}
	result := ValidateVerificationReport(report, required, nil, true)
	if !result.Rejected {
		t.Fatalf("expected gate to reject the real-world iter-1 report; findings=%+v", result.Findings)
	}

	// Must flag the lint check as a hedge finding.
	foundLintHedge := false
	for _, f := range result.Findings {
		if f.Category == GateCategoryHedge && strings.Contains(f.CheckName, "Lint") {
			foundLintHedge = true
		}
	}
	if !foundLintHedge {
		t.Errorf("expected a hedge finding on the Lint check; got findings:\n%+v", result.Findings)
	}

	// Must surface the stream_error_mapping caveat.
	if _, ok := result.KnownCaveats["stream_error_mapping"]; !ok {
		t.Errorf("expected stream_error_mapping caveat to be surfaced; got %+v", result.KnownCaveats)
	}

	feedback := FormatGateFeedback(result)
	if !strings.Contains(feedback, "CHANGES_REQUESTED") {
		t.Errorf("feedback missing CHANGES_REQUESTED marker; got:\n%s", feedback)
	}
	if !strings.Contains(feedback, "stream_error_mapping") {
		t.Errorf("feedback missing known_caveats; got:\n%s", feedback)
	}
}

func TestValidateVerificationReport_ContractBackedChecks(t *testing.T) {
	contract := CompileTestingContract("#### Automated Verification:\n- [ ] Agent tests: `go test ./internal/agent/... -count=1`\n", "/tmp/phase-01/plan.md", "tracer-bullet")
	report := BuildContractVerificationReportStub(&contract, "/tmp/phase-01/testing-contract.yaml")
	exitCode := 0
	for i := range report.Results {
		report.Results[i].Status = VerificationStatusPassed
		report.Results[i].Evidence = "ok"
		report.Results[i].EvidenceData = VerificationEvidence{ExitCode: &exitCode, Summary: "ok"}
	}

	result := ValidateVerificationReport(&report, nil, &contract, true)
	if result.Rejected {
		t.Fatalf("expected clean contract-backed report, got findings=%+v", result.Findings)
	}

	report.Results = report.Results[:len(report.Results)-1]
	result = ValidateVerificationReport(&report, nil, &contract, true)
	if !result.Rejected {
		t.Fatal("expected missing contract item to reject")
	}

	report = BuildContractVerificationReportStub(&contract, "/tmp/phase-01/testing-contract.yaml")
	report.Results[0].ItemID = "bogus"
	report.Results[0].Status = VerificationStatusPassed
	report.Results[0].Evidence = "ok"
	report.Results[0].EvidenceData = VerificationEvidence{ExitCode: &exitCode, Summary: "ok"}
	result = ValidateVerificationReport(&report, nil, &contract, true)
	if !result.Rejected {
		t.Fatal("expected unknown item_id to reject")
	}
}

func TestValidateVerificationReport_ContractContextDoesNotRequireReportContractPath(t *testing.T) {
	contract := CompileTestingContract("## Success Criteria\n### Visual Evidence\n- [ ] Capture the dashboard screenshot.\n", "/tmp/phase-01/plan.md", "collapsed")
	report := passedArtifactReportForTest(&contract, "")
	setArtifactEvidenceForTest(&report, VerificationModeVisual, VerificationEvidence{Summary: "screenshot exists"})

	result := ValidateVerificationReportWithContext(&report, nil, true, VerificationReportValidationContext{
		IterationDir: t.TempDir(),
		Contract:     &contract,
	})
	if !result.Rejected {
		t.Fatal("ValidateVerificationReportWithContext() Rejected = false, want true for contract-backed report with missing evidence.primary")
	}
	got := reportGateDetailsForTest(result)
	if !strings.Contains(got, "evidence.primary") {
		t.Fatalf("ValidateVerificationReportWithContext() details = %q, want evidence.primary violation", got)
	}
}

func TestValidateVerificationReport_ContractRevisionAndBlockedRules(t *testing.T) {
	contract := CompileTestingContract("#### Automated Verification:\n- [ ] Agent tests: `go test ./internal/agent/... -count=1`\n", "/tmp/phase-02/plan.md", "tdd-fill-in")
	report := BuildContractVerificationReportStub(&contract, "/tmp/phase-02/testing-contract.yaml")
	exitCode := 0
	for i := range report.Results {
		report.Results[i].Status = VerificationStatusPassed
		report.Results[i].Evidence = "ok"
		report.Results[i].EvidenceData = VerificationEvidence{ExitCode: &exitCode, Summary: "ok"}
	}

	report.ContractRevision = contract.Revision - 1
	result := ValidateVerificationReport(&report, nil, &contract, true)
	if !result.Rejected {
		t.Fatal("expected stale contract revision to reject")
	}

	report = BuildContractVerificationReportStub(&contract, "/tmp/phase-02/testing-contract.yaml")
	for i := range report.Results {
		report.Results[i].Status = VerificationStatusPassed
		report.Results[i].Evidence = "ok"
		report.Results[i].EvidenceData = VerificationEvidence{ExitCode: &exitCode, Summary: "ok"}
	}
	report.Results[0].Status = VerificationStatusBlocked
	report.Results[0].Evidence = ""
	report.Results[0].EvidenceData = VerificationEvidence{}
	result = ValidateVerificationReport(&report, nil, &contract, true)
	if !result.Rejected {
		t.Fatal("expected blocked item without blocked_reason to reject")
	}

	report = BuildContractVerificationReportStub(&contract, "/tmp/phase-02/testing-contract.yaml")
	for i := range report.Results {
		report.Results[i].Status = VerificationStatusPassed
		report.Results[i].Evidence = "ok"
		report.Results[i].EvidenceData = VerificationEvidence{ExitCode: &exitCode, Summary: "ok"}
	}
	report.Results[len(report.Results)-1].Status = VerificationStatusBlocked
	report.Results[len(report.Results)-1].BlockedReason = "sandbox lacks network access"
	report.Results[len(report.Results)-1].SubstituteItemID = "missing"
	report.Results[len(report.Results)-1].Evidence = ""
	report.Results[len(report.Results)-1].EvidenceData = VerificationEvidence{}
	result = ValidateVerificationReport(&report, nil, &contract, true)
	if !result.Rejected {
		t.Fatal("expected missing substitute item row to reject")
	}
}

func passedArtifactReportForTest(contract *TestingContract, contractPath string) VerificationReport {
	report := BuildContractVerificationReportStub(contract, contractPath)
	exitCode := 0
	for i := range report.Results {
		report.Results[i].Status = VerificationStatusPassed
		report.Results[i].EvidenceData = VerificationEvidence{ExitCode: &exitCode, Summary: "passed"}
	}
	return report
}

func setArtifactEvidenceForTest(report *VerificationReport, mode VerificationMode, evidence VerificationEvidence) {
	for i := range report.Results {
		if report.Results[i].Mode != mode {
			continue
		}
		report.Results[i].Status = VerificationStatusPassed
		report.Results[i].Evidence = evidence.Summary
		report.Results[i].EvidenceData = evidence
	}
}

func mustWriteEvidenceFile(t *testing.T, iterDir, rel string) {
	t.Helper()
	path := filepath.Join(iterDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(rel), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func reportGateDetailsForTest(result ReportGateResult) string {
	var b strings.Builder
	for _, finding := range result.Findings {
		b.WriteString(finding.Detail)
		b.WriteByte('\n')
	}
	return b.String()
}
