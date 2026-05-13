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
				Status:  VerificationStatusWaived,
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
	if len(got.Results) != 4 {
		t.Fatalf("ReadVerificationReport() len(Results) = %d, want 4", len(got.Results))
	}
	if got.Results[0].EvidenceData.Summary != report.Results[0].EvidenceData.Summary {
		t.Fatalf("ReadVerificationReport() first summary = %q, want %q", got.Results[0].EvidenceData.Summary, report.Results[0].EvidenceData.Summary)
	}
	if got.Results[1].BlockedReason != "codesigning is unavailable in the sandbox" {
		t.Fatalf("ReadVerificationReport() blocked reason = %q", got.Results[1].BlockedReason)
	}
	if len(got.Mismatches) != 1 || got.Mismatches[0].ItemID != "smoke_test" {
		t.Fatalf("ReadVerificationReport() mismatches = %+v", got.Mismatches)
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
