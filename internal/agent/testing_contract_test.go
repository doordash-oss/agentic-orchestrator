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
	"reflect"
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
	if len(contract.Items) != 2 {
		t.Fatalf("CompileTestingContract() got %d items, want 2", len(contract.Items))
	}

	first := contract.Items[0]
	if first.Source != testingContractPlanSource {
		t.Fatalf("CompileTestingContract() first item source = %q, want %q", first.Source, testingContractPlanSource)
	}
	if first.Policy != defaultTestingContractPolicy(testingContractPlanSource) {
		t.Fatalf("CompileTestingContract() first item policy = %+v", first.Policy)
	}
	if first.ExpectedEvidence.Kind != testingContractEvidenceKind {
		t.Fatalf("CompileTestingContract() first item evidence kind = %q, want %q", first.ExpectedEvidence.Kind, testingContractEvidenceKind)
	}
	if first.ExpectedEvidence.Matcher != testingContractEvidenceMatcher {
		t.Fatalf("CompileTestingContract() first item evidence matcher = %q, want %q", first.ExpectedEvidence.Matcher, testingContractEvidenceMatcher)
	}
	if got, want := first.ID, testingContractItemID(testingContractPlanSource, first.Command); got != want {
		t.Fatalf("CompileTestingContract() plan item ID = %q, want %q", got, want)
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

func TestCompileTestingContractMarksOnlyExplicitPlanCommandsRunnable(t *testing.T) {
	plan := "### Automated Verification\n- [ ] Protected [agentico capability: Okta session; probe: okta auth status]: `make integration`\n"
	contract := CompileTestingContract(plan, "/tmp/phase/plan.md", "collapsed")
	planItems := 0
	for _, item := range contract.Items {
		if item.Source != testingContractPlanSource {
			continue
		}
		planItems++
		if item.Run == nil || item.Run.Shell != "make integration" {
			t.Fatalf("plan item Run = %+v, want explicit command", item.Run)
		}
		if len(item.Capabilities) != 1 || item.Capabilities[0].Probe != "okta auth status" {
			t.Fatalf("plan item Capabilities = %+v, want Okta probe", item.Capabilities)
		}
	}
	if planItems != 1 {
		t.Fatalf("plan items = %d, want 1 (assertions above would be vacuous)", planItems)
	}
}

func TestReviseTestingContractRejectsNonUserWaiver(t *testing.T) {
	contract := CompileTestingContract("### Automated Verification\n- [ ] Tests: `make test`\n", "/tmp/phase/plan.md", "collapsed")
	itemID := ""
	for _, item := range contract.Items {
		if item.Source == testingContractPlanSource {
			itemID = item.ID
		}
	}
	if itemID == "" {
		t.Fatal("compiled contract has no plan item")
	}

	tests := []struct {
		name      string
		changedBy string
	}{
		{name: "agent", changedBy: "agent"},
		{name: "planner", changedBy: "planner"},
		{name: "empty", changedBy: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReviseTestingContract(&contract, []TestingContractChange{{
				ItemID: itemID, Action: TestingContractChangeWaive,
				ChangeReason: "self-waive attempt", ChangedBy: tt.changedBy,
			}})
			if err == nil || !strings.Contains(err.Error(), "changed_by") {
				t.Fatalf("ReviseTestingContract() error = %v, want changed_by rejection", err)
			}
		})
	}
}

func TestReconcileTestingContractPreservesWaiverAndBaseCommit(t *testing.T) {
	fresh := CompileTestingContract("### Automated Verification\n- [ ] Tests: `make test`\n", "/tmp/phase/plan.md", "collapsed")
	existing := fresh
	existing.Revision = 2
	existing.BaseCommits = map[string]string{"repo": "abc123"}
	for i := range existing.Items {
		if existing.Items[i].Source == testingContractPlanSource {
			existing.Items[i].Disposition = TestingContractItemDisposition{Status: TestingContractDispositionWaived, Reason: "approved", ChangedBy: "user"}
			existing.Changes = []TestingContractChange{{ItemID: existing.Items[i].ID, Action: TestingContractChangeWaive, ChangeReason: "approved", ChangedBy: "user"}}
		}
	}

	merged := ReconcileTestingContract(&existing, fresh)
	if merged.Revision != 2 || merged.BaseCommits["repo"] != "abc123" {
		t.Fatalf("ReconcileTestingContract() metadata = revision %d bases %v", merged.Revision, merged.BaseCommits)
	}
	for _, item := range merged.Items {
		if item.Source == testingContractPlanSource && !IsTestingContractItemWaived(item) {
			t.Fatalf("waiver not preserved: %+v", item)
		}
	}
}

func TestReconcileTestingContractInvalidatesWaiverWhenRequirementChanges(t *testing.T) {
	oldContract := CompileTestingContract("### Automated Verification\n- [ ] Protected [agentico capability: Okta; probe: okta status]: `make test`\n", "/tmp/phase/plan.md", "collapsed")
	for i := range oldContract.Items {
		if oldContract.Items[i].Source == testingContractPlanSource {
			oldContract.Items[i].Disposition = TestingContractItemDisposition{Status: TestingContractDispositionWaived, Reason: "old exception", ChangedBy: "user"}
			oldContract.Changes = []TestingContractChange{{ItemID: oldContract.Items[i].ID, Action: TestingContractChangeWaive, ChangeReason: "old exception", ChangedBy: "user"}}
		}
	}
	fresh := CompileTestingContract("### Automated Verification\n- [ ] Protected [agentico capability: VPN; probe: vpn status]: `make test`\n", "/tmp/phase/plan.md", "collapsed")
	merged := ReconcileTestingContract(&oldContract, fresh)
	for _, item := range merged.Items {
		if item.Source == testingContractPlanSource && IsTestingContractItemWaived(item) {
			t.Fatalf("changed requirement retained stale waiver: %+v", item)
		}
	}
	if len(merged.Changes) != 0 || merged.Revision != oldContract.Revision+1 {
		t.Fatalf("merged changes/revision = %v/%d, want cleared changes and revision %d", merged.Changes, merged.Revision, oldContract.Revision+1)
	}
}

func TestCompileTestingContract_ManualVerificationItems(t *testing.T) {
	plan := strings.Join([]string{
		"### Automated Verification",
		"- [ ] Agent tests pass: `go test ./internal/agent/... -count=1`",
		"### Manual Verification",
		"- [ ] Create a feature from the TUI and observe it reaches PlanReady.",
		"- [ ] Confirm the status copy is understandable to a user.",
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
	wantName := "Complete the phase manual verification checklist:\n- Create a feature from the TUI and observe it reaches PlanReady.\n- Confirm the status copy is understandable to a user."
	if manual.Name != wantName {
		t.Fatalf("manual name = %q", manual.Name)
	}
	if manual.Command != "manual: "+wantName {
		t.Fatalf("manual command = %q", manual.Command)
	}
	manualCount := 0
	for i := range contract.Items {
		if contract.Items[i].Source == testingContractManualSource {
			manualCount++
		}
	}
	if manualCount != 1 {
		t.Fatalf("manual item count = %d, want one consolidated artifact", manualCount)
	}
	if manual.ExpectedEvidence.Kind != testingContractManualKind {
		t.Fatalf("manual evidence kind = %q", manual.ExpectedEvidence.Kind)
	}
	if manual.Policy != defaultTestingContractPolicy(testingContractManualSource) {
		t.Fatalf("manual policy = %+v", manual.Policy)
	}
}

func TestCompileTestingContract_ConsolidatesBehavioralEvidence(t *testing.T) {
	plan := "### Behavioral Evidence\n- [ ] Capture the primary journey trace.\n- [ ] Record the resulting state transition.\n"
	contract := CompileTestingContract(plan, "/tmp/phase/plan.md", "collapsed")
	var behavioral []TestingContractItem
	for _, item := range contract.Items {
		if item.Source == testingContractBehavioralSource {
			behavioral = append(behavioral, item)
		}
	}
	if len(behavioral) != 1 {
		t.Fatalf("behavioral items = %+v, want one consolidated artifact", behavioral)
	}
	if !strings.Contains(behavioral[0].Name, "primary journey trace") || !strings.Contains(behavioral[0].Name, "state transition") {
		t.Fatalf("behavioral name = %q, want both checklist requirements", behavioral[0].Name)
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
	contract := CompileTestingContract("### Automated Verification\n- [ ] Test: `make test`\n", "/tmp/phase-01/plan.md", "tdd-fill-in")

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
		if !reflect.DeepEqual(got.Items[i], revised.Items[i]) {
			t.Fatalf("ReadTestingContract() Items[%d] = %+v, want %+v", i, got.Items[i], revised.Items[i])
		}
	}
	if len(got.Changes) != 1 {
		t.Fatalf("ReadTestingContract() len(Changes) = %d, want 1", len(got.Changes))
	}
}

func TestCompileTestingContractMultiRepoBehavioralCommandIsHarnessOwned(t *testing.T) {
	plan := "### Behavioral Evidence\n\n- [ ] Primary journey trace bundle: `npx playwright test e2e/journey.spec.ts`\n"
	contract := CompileTestingContractMultiRepo(MultiRepoContractInput{Repos: []string{"app"}, PlanText: plan, PlanPath: "phase-01/plan/phase-plan.md"})
	var item *TestingContractItem
	for i := range contract.Items {
		if contract.Items[i].Source == testingContractBehavioralSource {
			item = &contract.Items[i]
		}
	}
	if item == nil {
		t.Fatal("no behavioral item compiled")
	}
	if item.Owner != TestingContractOwnerHarness {
		t.Fatalf("owner = %q, want harness", item.Owner)
	}
	if item.Run == nil || item.Run.Shell != "npx playwright test e2e/journey.spec.ts" {
		t.Fatalf("run = %+v, want the packaged command", item.Run)
	}
	if item.ExpectedEvidence.Kind != testingContractEvidenceKind || item.ExpectedEvidence.Matcher != testingContractEvidenceMatcher {
		t.Fatalf("evidence = %+v, want command_result/exit_code_zero", item.ExpectedEvidence)
	}
	if item.ExpectedEvidence.Path != "" {
		t.Fatalf("path = %q, want empty for harness-owned evidence", item.ExpectedEvidence.Path)
	}
}

func TestCompileTestingContractMultiRepoBehavioralWithoutCommandStaysAgentOwned(t *testing.T) {
	plan := "### Behavioral Evidence\n\n- [ ] Capture the primary journey with a screen recording\n"
	contract := CompileTestingContractMultiRepo(MultiRepoContractInput{Repos: []string{"app"}, PlanText: plan, PlanPath: "phase-01/plan/phase-plan.md"})
	for _, item := range contract.Items {
		if item.Source != testingContractBehavioralSource {
			continue
		}
		if item.Owner != TestingContractOwnerAgent || item.Run != nil {
			t.Fatalf("item = %+v, want agent-owned with no run", item)
		}
		if item.ExpectedEvidence.Path == "" {
			t.Fatal("agent-owned behavioral item must keep its canonical evidence path")
		}
		return
	}
	t.Fatal("no behavioral item compiled")
}

func TestCompileTestingContractMultiRepoVisualSizeCells(t *testing.T) {
	plan := "### Visual Evidence\n\n- [ ] Home populated, dark theme [size: 1440x900]\n- [ ] Home populated, dark theme [size: 760x900]\n"
	contract := CompileTestingContractMultiRepo(MultiRepoContractInput{Repos: []string{"app"}, PlanText: plan, PlanPath: "phase-01/plan/phase-plan.md"})
	var visual []TestingContractItem
	for _, item := range contract.Items {
		if item.Source == testingContractVisualSource {
			visual = append(visual, item)
		}
	}
	if len(visual) != 2 {
		t.Fatalf("visual rows = %d, want 2 distinct cells", len(visual))
	}
	if visual[0].ID == visual[1].ID {
		t.Fatal("cells differing only by size must get distinct item IDs")
	}
	if visual[0].ExpectedEvidence.Width != 1440 || visual[0].ExpectedEvidence.Height != 900 {
		t.Fatalf("first cell size = %dx%d", visual[0].ExpectedEvidence.Width, visual[0].ExpectedEvidence.Height)
	}
	if visual[1].ExpectedEvidence.Width != 760 {
		t.Fatalf("second cell width = %d", visual[1].ExpectedEvidence.Width)
	}
}

func TestValidateEvidenceSectionAllowsMultipleCommandBackedBehavioralItems(t *testing.T) {
	body := "### Behavioral Evidence\n\n- [ ] Primary journey: `npx playwright test e2e/a.spec.ts`\n- [ ] Attention journey: `npx playwright test e2e/b.spec.ts`\n"
	if reason := validateEvidenceSectionBody(body, "### Behavioral Evidence"); reason != "" {
		t.Fatalf("unexpected violation: %s", reason)
	}
	proseBody := "### Behavioral Evidence\n\n- [ ] Journey one recording\n- [ ] Journey two recording\n"
	if reason := validateEvidenceSectionBody(proseBody, "### Behavioral Evidence"); reason == "" {
		t.Fatal("multiple prose-only behavioral items must still be rejected")
	}
}
