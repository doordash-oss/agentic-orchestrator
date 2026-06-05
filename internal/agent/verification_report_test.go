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

func TestBuildRequiredVerification(t *testing.T) {
	plan := "#### Automated Verification:\n- [ ] Unit tests pass: `go test ./...`\n- [ ] Lint passes: `go vet ./...`\n"
	got := BuildRequiredVerification(plan)

	if len(got) != 2 {
		t.Fatalf("BuildRequiredVerification() got %d required items, want 2", len(got))
	}
	if got[0].Name != "Unit tests pass" || got[0].Requirement != "go test ./..." {
		t.Fatalf("BuildRequiredVerification() first item = %+v", got[0])
	}
	if got[1].Name != "Lint passes" || got[1].Requirement != "go vet ./..." {
		t.Fatalf("BuildRequiredVerification() second item = %+v", got[1])
	}
}

func TestWriteAndReadVerificationReportStub(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verification-report.yaml")
	required := []RequiredVerificationItem{
		{Name: "Unit tests pass", Requirement: "go test ./..."},
		{Name: "Configured verification", Requirement: "npm run lint"},
	}

	if err := WriteVerificationReportStub(path, required); err != nil {
		t.Fatalf("WriteVerificationReportStub() error = %v", err)
	}

	report, err := ReadVerificationReport(path)
	if err != nil {
		t.Fatalf("ReadVerificationReport() error = %v", err)
	}
	if report.Version != 1 {
		t.Fatalf("ReadVerificationReport() Version = %d, want 1", report.Version)
	}
	if len(report.RequiredChecks) != 2 {
		t.Fatalf("ReadVerificationReport() got %d required checks, want 2", len(report.RequiredChecks))
	}
	if report.RequiredChecks[0].Status != VerificationStatusNotRun {
		t.Fatalf("ReadVerificationReport() status = %q, want %q", report.RequiredChecks[0].Status, VerificationStatusNotRun)
	}
}

func TestWriteAndReadContractVerificationReportStub(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verification-report.yaml")
	contract := CompileTestingContract("#### Automated Verification:\n- [ ] Agent tests: `go test ./internal/agent/... -count=1`\n", "/tmp/phase-01/plan.md", "tdd-fill-in")

	if err := WriteVerificationReportStubFromContract(path, "/tmp/phase-01/testing-contract.yaml", &contract); err != nil {
		t.Fatalf("WriteVerificationReportStubFromContract() error = %v", err)
	}

	report, err := ReadVerificationReport(path)
	if err != nil {
		t.Fatalf("ReadVerificationReport() error = %v", err)
	}
	if report.Version != 2 {
		t.Fatalf("ReadVerificationReport() Version = %d, want 2", report.Version)
	}
	if report.ContractPath != "/tmp/phase-01/testing-contract.yaml" {
		t.Fatalf("ReadVerificationReport() ContractPath = %q", report.ContractPath)
	}
	if report.ContractRevision != testingContractInitialRev {
		t.Fatalf("ReadVerificationReport() ContractRevision = %d, want %d", report.ContractRevision, testingContractInitialRev)
	}
	if len(report.Results) != len(contract.Items) {
		t.Fatalf("ReadVerificationReport() got %d results, want %d", len(report.Results), len(contract.Items))
	}
	if report.Results[0].ItemID == "" {
		t.Fatal("ReadVerificationReport() expected result ItemID to be populated")
	}
	if report.Results[0].Command == "" {
		t.Fatal("ReadVerificationReport() expected result Command to be populated")
	}
}

func TestBuildContractVerificationReportStub_EvidenceModes(t *testing.T) {
	contract := CompileTestingContract(strings.Join([]string{
		"## Success Criteria",
		"### Visual Evidence",
		"- [ ] Capture the dashboard.",
		"### Behavioral Evidence",
		"- [ ] Attach the workflow transcript.",
	}, "\n"), "/tmp/phase-01/plan.md", "tdd-fill-in")

	report := BuildContractVerificationReportStub(&contract, "/tmp/phase-01/testing-contract.yaml")

	var sawVisual, sawBehavioral bool
	for _, result := range report.Results {
		switch result.Mode {
		case VerificationModeVisual:
			sawVisual = true
		case VerificationModeBehavioral:
			sawBehavioral = true
		}
	}
	if !sawVisual {
		t.Fatalf("BuildContractVerificationReportStub() missing mode visual: %+v", report.Results)
	}
	if !sawBehavioral {
		t.Fatalf("BuildContractVerificationReportStub() missing mode behavioral: %+v", report.Results)
	}
}

func TestVerificationReportRoundTripV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verification-report.yaml")
	exitCode := 0
	report := VerificationReport{
		Version:          2,
		ContractPath:     "/tmp/phase-02/testing-contract.yaml",
		ContractRevision: 3,
		Results: []VerificationCheckResult{
			{
				ItemID:   "build_all",
				Name:     "Build succeeds",
				Command:  "go build ./...",
				Status:   VerificationStatusPassed,
				Evidence: "ok github.com/doordash-oss/agentic-orchestrator/cmd/agentic",
				EvidenceData: VerificationEvidence{
					ExitCode: &exitCode,
					Summary:  "ok github.com/doordash-oss/agentic-orchestrator/cmd/agentic",
				},
			},
			{
				ItemID:           "smoke_test",
				Name:             "Smoke test",
				Command:          "bash test/e2e/smoke.sh",
				Status:           VerificationStatusBlocked,
				BlockedReason:    "codesigning is unavailable in the sandbox",
				SubstituteItemID: "fast_suite",
			},
			{
				ItemID:   "fast_suite",
				Name:     "Fast suite",
				Command:  "go test ./... -race -short",
				Status:   VerificationStatusPassed,
				Evidence: "ok github.com/doordash-oss/agentic-orchestrator/internal/agent",
				EvidenceData: VerificationEvidence{
					ExitCode: &exitCode,
					Summary:  "ok github.com/doordash-oss/agentic-orchestrator/internal/agent",
				},
			},
			{
				ItemID:  "manual_signoff",
				Name:    "Manual signoff",
				Command: "n/a",
				Mode:    VerificationModeManual,
				Status:  VerificationStatusWaived,
			},
			{
				ItemID:   "visual_capture",
				Name:     "Visual capture",
				Command:  "visual: capture the dashboard",
				Mode:     VerificationModeVisual,
				Status:   VerificationStatusPassed,
				Evidence: "dashboard.png exists",
			},
			{
				ItemID:   "behavioral_capture",
				Name:     "Behavioral capture",
				Command:  "behavioral: capture the workflow",
				Mode:     VerificationModeBehavioral,
				Status:   VerificationStatusPassed,
				Evidence: "workflow.txt exists",
			},
		},
		Mismatches: []VerificationMismatch{
			{
				ItemID:               "smoke_test",
				ImplementationStatus: "blocked",
				FinalReviewStatus:    "passed",
				Note:                 "reran on a publishable machine",
			},
		},
		Summary: "targeted verification complete",
	}

	if err := WriteVerificationReport(path, report); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	got, err := ReadVerificationReport(path)
	if err != nil {
		t.Fatalf("ReadVerificationReport() error = %v", err)
	}
	if got.ContractRevision != 3 {
		t.Fatalf("ReadVerificationReport() ContractRevision = %d, want 3", got.ContractRevision)
	}
	if len(got.Results) != 6 {
		t.Fatalf("ReadVerificationReport() len(Results) = %d, want 6", len(got.Results))
	}
	if got.Results[0].EvidenceData.Summary != report.Results[0].EvidenceData.Summary {
		t.Fatalf("ReadVerificationReport() first summary = %q, want %q", got.Results[0].EvidenceData.Summary, report.Results[0].EvidenceData.Summary)
	}
	if got.Results[4].Mode != VerificationModeVisual {
		t.Fatalf("ReadVerificationReport() visual mode = %q, want %q", got.Results[4].Mode, VerificationModeVisual)
	}
	if got.Results[5].Mode != VerificationModeBehavioral {
		t.Fatalf("ReadVerificationReport() behavioral mode = %q, want %q", got.Results[5].Mode, VerificationModeBehavioral)
	}
	if got.Results[1].BlockedReason != "codesigning is unavailable in the sandbox" {
		t.Fatalf("ReadVerificationReport() blocked reason = %q", got.Results[1].BlockedReason)
	}
	if len(got.Mismatches) != 1 || got.Mismatches[0].ItemID != "smoke_test" {
		t.Fatalf("ReadVerificationReport() mismatches = %+v", got.Mismatches)
	}
}

func TestVerificationReportRoundTripFileBackedEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verification-report.yaml")
	exitCode := 0
	report := VerificationReport{
		Version:          2,
		ContractPath:     "/tmp/phase-02/testing-contract.yaml",
		ContractRevision: 1,
		Results: []VerificationCheckResult{
			{
				ItemID:   "visual_capture",
				Name:     "Visual capture",
				Command:  "visual: capture dashboard",
				Mode:     VerificationModeVisual,
				Status:   VerificationStatusPassed,
				Evidence: "dashboard screenshot captured",
				EvidenceData: VerificationEvidence{
					Summary:     "dashboard screenshot captured",
					Primary:     "screenshots/dashboard.png",
					Attachments: []string{"screenshots/dashboard-detail.png"},
				},
			},
			{
				ItemID:   "behavioral_capture",
				Name:     "Behavioral capture",
				Command:  "behavioral: capture workflow",
				Mode:     VerificationModeBehavioral,
				Status:   VerificationStatusPassed,
				Evidence: "workflow transcript captured",
				EvidenceData: VerificationEvidence{
					ExitCode: &exitCode,
					Summary:  "workflow transcript captured",
					Primary:  "behaviors/create-feature.log",
				},
			},
			{
				ItemID:   "visual_noisy_empty_attachments",
				Name:     "Visual no attachments",
				Command:  "visual: capture empty state",
				Mode:     VerificationModeVisual,
				Status:   VerificationStatusPassed,
				Evidence: "empty state captured",
				EvidenceData: VerificationEvidence{
					Summary:     "empty state captured",
					Primary:     "screenshots/empty.png",
					Attachments: []string{},
				},
			},
		},
	}

	if err := WriteVerificationReport(path, report); err != nil {
		t.Fatalf("WriteVerificationReport() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	if strings.Contains(string(data), "attachments: []") {
		t.Fatalf("WriteVerificationReport() emitted noisy empty attachments:\n%s", data)
	}

	got, err := ReadVerificationReport(path)
	if err != nil {
		t.Fatalf("ReadVerificationReport() error = %v", err)
	}
	if got.Results[0].EvidenceData.Primary != "screenshots/dashboard.png" {
		t.Fatalf("ReadVerificationReport() visual primary = %q, want screenshots/dashboard.png", got.Results[0].EvidenceData.Primary)
	}
	if len(got.Results[0].EvidenceData.Attachments) != 1 || got.Results[0].EvidenceData.Attachments[0] != "screenshots/dashboard-detail.png" {
		t.Fatalf("ReadVerificationReport() visual attachments = %+v, want dashboard detail", got.Results[0].EvidenceData.Attachments)
	}
	if got.Results[1].EvidenceData.ExitCode == nil || *got.Results[1].EvidenceData.ExitCode != 0 {
		t.Fatalf("ReadVerificationReport() behavioral exit code = %+v, want 0", got.Results[1].EvidenceData.ExitCode)
	}
	if got.Results[1].EvidenceData.Primary != "behaviors/create-feature.log" {
		t.Fatalf("ReadVerificationReport() behavioral primary = %q, want behaviors/create-feature.log", got.Results[1].EvidenceData.Primary)
	}
	if got.Results[2].EvidenceData.Attachments == nil || len(got.Results[2].EvidenceData.Attachments) != 0 {
		t.Fatalf("ReadVerificationReport() empty attachments = %#v, want non-nil empty slice", got.Results[2].EvidenceData.Attachments)
	}
}

func TestReadVerificationReport_V1Compatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verification-report.yaml")
	content := `version: 1
required_checks:
  - name: Unit tests
    requirement: go test ./...
    status: pass
    evidence: ok github.com/doordash-oss/agentic-orchestrator/internal/agent
known_caveats:
  contract_migration: waiting for phase 2 contract rollout
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	got, err := ReadVerificationReport(path)
	if err != nil {
		t.Fatalf("ReadVerificationReport() error = %v", err)
	}
	if got.RequiredChecks[0].Status != VerificationStatusPassed {
		t.Fatalf("ReadVerificationReport() Status = %q, want %q", got.RequiredChecks[0].Status, VerificationStatusPassed)
	}
	if got.RequiredChecks[0].Evidence != "ok github.com/doordash-oss/agentic-orchestrator/internal/agent" {
		t.Fatalf("ReadVerificationReport() legacy evidence = %q", got.RequiredChecks[0].Evidence)
	}
	if got.RequiredChecks[0].EvidenceData.Summary != got.RequiredChecks[0].Evidence {
		t.Fatalf("ReadVerificationReport() legacy summary = %q, want %q", got.RequiredChecks[0].EvidenceData.Summary, got.RequiredChecks[0].Evidence)
	}
	if got.KnownCaveats["contract_migration"] == "" {
		t.Fatalf("ReadVerificationReport() KnownCaveats = %+v", got.KnownCaveats)
	}
}

func TestCarryForwardVerificationResults(t *testing.T) {
	exit0 := 0
	stub := VerificationReport{Version: 2, Results: []VerificationCheckResult{
		{ItemID: "build", Status: VerificationStatusNotRun},
		{ItemID: "test", Status: VerificationStatusNotRun},
		{ItemID: "smoke", Status: VerificationStatusNotRun},
		{ItemID: "new-item", Status: VerificationStatusNotRun},
	}}
	prior := VerificationReport{Version: 2, Results: []VerificationCheckResult{
		{ItemID: "build", Status: VerificationStatusPassed, Evidence: "ok", EvidenceData: VerificationEvidence{Summary: "build ok", ExitCode: &exit0}},
		{ItemID: "test", Status: VerificationStatusFailed, Evidence: "boom"},
		{ItemID: "smoke", Status: VerificationStatusNotRun},
	}}
	got := carryForwardVerificationResults(stub, prior)
	byID := map[string]VerificationCheckResult{}
	for _, r := range got.Results {
		byID[r.ItemID] = r
	}
	if byID["build"].Status != VerificationStatusPassed || byID["build"].EvidenceData.Summary != "build ok" {
		t.Errorf("build row not carried forward: %+v", byID["build"])
	}
	if byID["test"].Status != VerificationStatusFailed {
		t.Errorf("test row = %q, want failed (carried)", byID["test"].Status)
	}
	if byID["smoke"].Status != VerificationStatusNotRun {
		t.Errorf("smoke (prior not_run) = %q, want not_run", byID["smoke"].Status)
	}
	if byID["new-item"].Status != VerificationStatusNotRun {
		t.Errorf("new-item (no prior match) = %q, want not_run", byID["new-item"].Status)
	}

	// requirement-text fallback (no item ids, RequiredChecks path)
	stub2 := VerificationReport{Version: 1, RequiredChecks: []VerificationCheckResult{
		{Requirement: "go build ./...", Status: VerificationStatusNotRun},
		{Requirement: "go test ./...", Status: VerificationStatusNotRun},
	}}
	prior2 := VerificationReport{Version: 1, RequiredChecks: []VerificationCheckResult{
		{Requirement: "go build ./...", Status: VerificationStatusPassed, Evidence: "built"},
	}}
	got2 := carryForwardVerificationResults(stub2, prior2)
	if got2.RequiredChecks[0].Status != VerificationStatusPassed {
		t.Errorf("requirement-matched row should carry passed, got %q", got2.RequiredChecks[0].Status)
	}
	if got2.RequiredChecks[1].Status != VerificationStatusNotRun {
		t.Errorf("unmatched requirement row should stay not_run, got %q", got2.RequiredChecks[1].Status)
	}
}

func TestLoadPriorVerificationReport(t *testing.T) {
	dir := t.TempDir()
	if got := loadPriorVerificationReport(dir, 1); got != nil {
		t.Errorf("iter<=1 should return nil, got %+v", got)
	}
	if got := loadPriorVerificationReport(dir, 3); got != nil {
		t.Errorf("missing prior report should return nil, got %+v", got)
	}
	iterDir := filepath.Join(dir, "iteration-02")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteVerificationReport(filepath.Join(iterDir, "verification-report.yaml"),
		VerificationReport{Version: 2, Results: []VerificationCheckResult{{ItemID: "x", Status: VerificationStatusPassed}}}); err != nil {
		t.Fatal(err)
	}
	got := loadPriorVerificationReport(dir, 3)
	if got == nil || len(got.Results) != 1 || got.Results[0].ItemID != "x" {
		t.Fatalf("expected prior report with item x, got %+v", got)
	}
}
