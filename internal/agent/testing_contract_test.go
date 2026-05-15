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
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileTestingContract(t *testing.T) {
	planPath := filepath.Join("/tmp", "runs", "run-001", "phase-01", "plan", "approved.md")
	plan := strings.Join([]string{
		"#### Automated Verification:",
		"- [ ] Agent package tests pass: `go test ./internal/agent/... -count=1`",
		"- [ ] Build all packages: `go build ./...`",
		"- [ ] Agent package tests pass: `go test ./internal/agent/... -count=1`",
	}, "\n")

	contract := CompileTestingContract(plan, planPath, "tdd-fill-in")

	if contract.Version != testingContractVersion {
		t.Fatalf("CompileTestingContract() Version = %d, want %d", contract.Version, testingContractVersion)
	}
	if contract.Revision != testingContractInitialRev {
		t.Fatalf("CompileTestingContract() Revision = %d, want %d", contract.Revision, testingContractInitialRev)
	}
	if contract.Scope != "phase-01" {
		t.Fatalf("CompileTestingContract() Scope = %q, want %q", contract.Scope, "phase-01")
	}
	if contract.GeneratedFrom.PlanPath != planPath {
		t.Fatalf("CompileTestingContract() PlanPath = %q, want %q", contract.GeneratedFrom.PlanPath, planPath)
	}
	if contract.GeneratedFrom.BaselineProfile != testingContractBaselineName {
		t.Fatalf("CompileTestingContract() BaselineProfile = %q, want %q", contract.GeneratedFrom.BaselineProfile, testingContractBaselineName)
	}
	baselineSteps := DefaultBaselineVerificationSteps()
	if len(contract.Items) != len(baselineSteps)+2 {
		t.Fatalf("CompileTestingContract() got %d items, want %d", len(contract.Items), len(baselineSteps)+2)
	}

	first := contract.Items[0]
	if first.Source != testingContractBaselineSource {
		t.Fatalf("CompileTestingContract() first item source = %q, want %q", first.Source, testingContractBaselineSource)
	}
	if first.Policy != defaultTestingContractPolicy(testingContractBaselineSource) {
		t.Fatalf("CompileTestingContract() first item policy = %+v", first.Policy)
	}
	if first.ExpectedEvidence.Kind != testingContractEvidenceKind {
		t.Fatalf("CompileTestingContract() first item evidence kind = %q, want %q", first.ExpectedEvidence.Kind, testingContractEvidenceKind)
	}
	if first.ExpectedEvidence.Matcher != testingContractEvidenceMatcher {
		t.Fatalf("CompileTestingContract() first item evidence matcher = %q, want %q", first.ExpectedEvidence.Matcher, testingContractEvidenceMatcher)
	}
	if got, want := first.ID, testingContractItemID(testingContractBaselineSource, baselineSteps[0].Command); got != want {
		t.Fatalf("CompileTestingContract() baseline item ID = %q, want %q", got, want)
	}

	var foundPlanItem bool
	for _, item := range contract.Items {
		if item.Command != "go test ./internal/agent/... -count=1" {
			continue
		}
		foundPlanItem = true
		if item.Source != testingContractPlanSource {
			t.Fatalf("CompileTestingContract() plan item source = %q, want %q", item.Source, testingContractPlanSource)
		}
		if item.Policy != defaultTestingContractPolicy(testingContractPlanSource) {
			t.Fatalf("CompileTestingContract() plan item policy = %+v", item.Policy)
		}
		if got, want := item.ID, testingContractItemID(testingContractPlanSource, "go test ./internal/agent/... -count=1"); got != want {
			t.Fatalf("CompileTestingContract() plan item ID = %q, want %q", got, want)
		}
	}
	if !foundPlanItem {
		t.Fatalf("CompileTestingContract() missing plan item for agent tests")
	}
}

func TestCompileTestingContractWithBaseline(t *testing.T) {
	planPath := filepath.Join("/tmp", "runs", "run-001", "phase-01", "plan", "approved.md")
	baseline := []VerificationStep{
		{Description: "Build agentic", Command: "go build -o bin/agentic ./cmd/agentic"},
		{Description: "Run targeted agent tests", Command: "go test ./internal/agent/... -race"},
	}
	plan := "#### Automated Verification:\n- [ ] Smoke passes: `bash test/e2e/smoke.sh`\n"

	contract := CompileTestingContractWithBaseline(plan, planPath, "tdd-fill-in", "agentic-go", baseline)

	if contract.GeneratedFrom.BaselineProfile != "agentic-go" {
		t.Fatalf("CompileTestingContractWithBaseline() BaselineProfile = %q, want %q", contract.GeneratedFrom.BaselineProfile, "agentic-go")
	}
	if len(contract.Items) != 3 {
		t.Fatalf("CompileTestingContractWithBaseline() got %d items, want 3", len(contract.Items))
	}
	for i, step := range baseline {
		if got := contract.Items[i].Command; got != step.Command {
			t.Fatalf("CompileTestingContractWithBaseline() baseline[%d] command = %q, want %q", i, got, step.Command)
		}
		if contract.Items[i].Source != testingContractBaselineSource {
			t.Fatalf("CompileTestingContractWithBaseline() baseline[%d] source = %q, want %q", i, contract.Items[i].Source, testingContractBaselineSource)
		}
	}
}

func TestCompileTestingContract_ManualVerificationItems(t *testing.T) {
	plan := strings.Join([]string{
		"### Automated Verification",
		"- [ ] Agent tests pass: `go test ./internal/agent/... -count=1`",
		"### Manual Verification",
		"- [ ] Create a feature from the TUI and observe it reaches PlanReady.",
		"- [ ] None required: this marker should be ignored.",
	}, "\n")

	contract := CompileTestingContract(plan, "/tmp/phase-01/plan.md", "collapsed")

	var manual *TestingContractItem
	for i := range contract.Items {
		if contract.Items[i].Source == testingContractManualSource {
			manual = &contract.Items[i]
			break
		}
	}
	if manual == nil {
		t.Fatalf("missing manual contract item in %+v", contract.Items)
	}
	if manual.Name != "Create a feature from the TUI and observe it reaches PlanReady." {
		t.Fatalf("manual name = %q", manual.Name)
	}
	if manual.Command != "manual: Create a feature from the TUI and observe it reaches PlanReady." {
		t.Fatalf("manual command = %q", manual.Command)
	}
	if manual.ExpectedEvidence.Kind != testingContractManualKind {
		t.Fatalf("manual evidence kind = %q", manual.ExpectedEvidence.Kind)
	}
	if manual.Policy != defaultTestingContractPolicy(testingContractManualSource) {
		t.Fatalf("manual policy = %+v", manual.Policy)
	}
}

func TestCompileTestingContract_EvidenceItems(t *testing.T) {
	plan := strings.Join([]string{
		"## Success Criteria",
		"### Visual Evidence",
		"- [ ] Capture the import confirmation screen.",
		"- [ ] Capture the import confirmation screen.",
		"### Behavioral Evidence",
		"- [ ] Save the CLI transcript for a successful import.",
	}, "\n")

	contract := CompileTestingContract(plan, "/tmp/phase-01/plan.md", "collapsed")

	var visual, behavioral *TestingContractItem
	for i := range contract.Items {
		switch contract.Items[i].Source {
		case testingContractVisualSource:
			visual = &contract.Items[i]
		case testingContractBehavioralSource:
			behavioral = &contract.Items[i]
		}
	}
	if visual == nil {
		t.Fatalf("missing visual contract item: %+v", contract.Items)
	}
	if behavioral == nil {
		t.Fatalf("missing behavioral contract item: %+v", contract.Items)
	}
	if visual.ExpectedEvidence.Kind != testingContractVisualKind {
		t.Fatalf("visual evidence kind = %q, want %q", visual.ExpectedEvidence.Kind, testingContractVisualKind)
	}
	if visual.ExpectedEvidence.Matcher != testingContractEvidenceFileExistsMatcher {
		t.Fatalf("visual evidence matcher = %q, want %q", visual.ExpectedEvidence.Matcher, testingContractEvidenceFileExistsMatcher)
	}
	if visual.Policy != defaultTestingContractPolicy(testingContractVisualSource) {
		t.Fatalf("visual policy = %+v", visual.Policy)
	}
	if behavioral.ExpectedEvidence.Kind != testingContractBehavioralKind {
		t.Fatalf("behavioral evidence kind = %q, want %q", behavioral.ExpectedEvidence.Kind, testingContractBehavioralKind)
	}
	if behavioral.ExpectedEvidence.Matcher != testingContractEvidenceFileExistsMatcher {
		t.Fatalf("behavioral evidence matcher = %q, want %q", behavioral.ExpectedEvidence.Matcher, testingContractEvidenceFileExistsMatcher)
	}
	if behavioral.Policy != defaultTestingContractPolicy(testingContractBehavioralSource) {
		t.Fatalf("behavioral policy = %+v", behavioral.Policy)
	}
	if got := countItems(contract.Items, testingContractVisualSource, ""); got != 1 {
		t.Fatalf("visual item count = %d, want 1", got)
	}
}

func TestBuildContractVerificationReportStub_ManualMode(t *testing.T) {
	contract := CompileTestingContract("### Manual Verification\n- [ ] Exercise the primary workflow.\n", "/tmp/phase-01/plan.md", "collapsed")
	report := BuildContractVerificationReportStub(&contract, "/tmp/phase-01/testing-contract.yaml")

	var manual *VerificationCheckResult
	for i := range report.Results {
		if report.Results[i].Mode == VerificationModeManual {
			manual = &report.Results[i]
			break
		}
	}
	if manual == nil {
		t.Fatalf("missing manual verification report row: %+v", report.Results)
	}
	if manual.Status != VerificationStatusNotRun {
		t.Fatalf("manual status = %q", manual.Status)
	}
}

func TestReviseTestingContract(t *testing.T) {
	contract := CompileTestingContract("#### Automated Verification:\n- [ ] Agent tests: `go test ./internal/agent/... -count=1`\n", "/tmp/phase-01/plan.md", "tdd-fill-in")
	original := contract

	revised, err := ReviseTestingContract(&contract, []TestingContractChange{
		{
			ItemID:       contract.Items[len(contract.Items)-1].ID,
			Supersedes:   contract.Items[0].ID,
			ChangeReason: "replace the sandbox-blocked smoke command with a narrower package test",
			ChangedBy:    "implementer",
		},
	})
	if err != nil {
		t.Fatalf("ReviseTestingContract() error = %v", err)
	}

	if revised.Revision != original.Revision+1 {
		t.Fatalf("ReviseTestingContract() Revision = %d, want %d", revised.Revision, original.Revision+1)
	}
	if len(revised.Changes) != 1 {
		t.Fatalf("ReviseTestingContract() len(Changes) = %d, want 1", len(revised.Changes))
	}
	if revised.Changes[0].Supersedes != contract.Items[0].ID {
		t.Fatalf("ReviseTestingContract() Supersedes = %q, want %q", revised.Changes[0].Supersedes, contract.Items[0].ID)
	}
	if contract.Revision != original.Revision {
		t.Fatalf("ReviseTestingContract() mutated input revision to %d", contract.Revision)
	}
}

func TestReviseTestingContractRejectsInvalidChange(t *testing.T) {
	contract := CompileTestingContract("", "/tmp/phase-01/plan.md", "tdd-fill-in")

	tests := []struct {
		name   string
		change TestingContractChange
	}{
		{
			name: "missing_item_id",
			change: TestingContractChange{
				ChangeReason: "missing target",
				ChangedBy:    "implementer",
			},
		},
		{
			name: "unknown_item_id",
			change: TestingContractChange{
				ItemID:       "missing",
				ChangeReason: "unknown item",
				ChangedBy:    "implementer",
			},
		},
		{
			name: "unknown_supersedes",
			change: TestingContractChange{
				ItemID:       contract.Items[0].ID,
				Supersedes:   "missing",
				ChangeReason: "unknown predecessor",
				ChangedBy:    "implementer",
			},
		},
		{
			name: "missing_reason",
			change: TestingContractChange{
				ItemID:    contract.Items[0].ID,
				ChangedBy: "implementer",
			},
		},
		{
			name: "missing_actor",
			change: TestingContractChange{
				ItemID:       contract.Items[0].ID,
				ChangeReason: "missing actor",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ReviseTestingContract(&contract, []TestingContractChange{tt.change}); err == nil {
				t.Fatalf("ReviseTestingContract() error = nil, want failure for %+v", tt.change)
			}
		})
	}
}

func TestTestingContractRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "testing-contract.yaml")
	want := CompileTestingContract("#### Automated Verification:\n- [ ] Build binary: `go build -o bin/agentic ./cmd/agentic`\n", path, "tdd-fill-in")
	revised, err := ReviseTestingContract(&want, []TestingContractChange{
		{
			ItemID:       want.Items[len(want.Items)-1].ID,
			ChangeReason: "track the phase-local binary build explicitly",
			ChangedBy:    "implementer",
		},
	})
	if err != nil {
		t.Fatalf("ReviseTestingContract() error = %v", err)
	}

	if err := WriteTestingContract(path, *revised); err != nil {
		t.Fatalf("WriteTestingContract() error = %v", err)
	}

	got, err := ReadTestingContract(path)
	if err != nil {
		t.Fatalf("ReadTestingContract() error = %v", err)
	}
	if got.Version != revised.Version || got.Revision != revised.Revision || got.Scope != revised.Scope {
		t.Fatalf("ReadTestingContract() metadata mismatch: got %+v want %+v", *got, *revised)
	}
	if len(got.Items) != len(revised.Items) {
		t.Fatalf("ReadTestingContract() len(Items) = %d, want %d", len(got.Items), len(revised.Items))
	}
	for i := range got.Items {
		if got.Items[i] != revised.Items[i] {
			t.Fatalf("ReadTestingContract() Items[%d] = %+v, want %+v", i, got.Items[i], revised.Items[i])
		}
	}
	if len(got.Changes) != 1 {
		t.Fatalf("ReadTestingContract() len(Changes) = %d, want 1", len(got.Changes))
	}
}
