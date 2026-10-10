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
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestSynthesizeVerificationNeedUserInputGateWithContextExplainsBlockedChecks(t *testing.T) {
	contract := &TestingContract{
		Version:  1,
		Revision: 4,
		Items: []TestingContractItem{
			{
				ID: "deploy", Name: "Deploy smoke test", Repo: "api",
				Command:      "make deploy-smoke",
				Capabilities: []TestingContractCapability{{Name: "Okta session", Probe: "okta auth status"}},
			},
			{
				ID: "codesign", Name: "Package signature", Command: "make package-verify",
			},
		},
	}
	report := &VerificationReport{
		ContractRevision: 4,
		Results: []VerificationCheckResult{
			{
				ItemID: "deploy", Status: VerificationStatusBlocked,
				BlockedReason: `missing declared capability "Okta session"`,
			},
			{
				ItemID: "codesign", Status: VerificationStatusBlocked,
				BlockedReason: "host keychain denied access to the signing identity",
			},
		},
	}

	rec := SynthesizeVerificationNeedUserInputGateWithContext(
		"/private/testing-contract.yaml", contract, report,
		[]string{"codesign", "deploy"}, 3,
	)

	if rec.Verification == nil || len(rec.Verification.Blockers) != 2 {
		t.Fatalf("verification context = %+v, want two blockers", rec.Verification)
	}
	if got := rec.Verification.Blockers[0]; got.ItemID != "codesign" ||
		got.Name != "Package signature" ||
		got.Reason != "host keychain denied access to the signing identity" ||
		!strings.Contains(got.Remediation, "environment limitation") {
		t.Fatalf("first blocker = %+v", got)
	}
	if got := rec.Verification.Blockers[1]; got.ItemID != "deploy" ||
		got.RepoName != "api" ||
		got.Command != "make deploy-smoke" ||
		!reflect.DeepEqual(got.Capabilities, []string{"Okta session"}) ||
		!strings.Contains(got.Remediation, "Okta session") {
		t.Fatalf("second blocker = %+v", got)
	}
	if strings.Contains(fmt.Sprintf("%+v", rec.Verification), "okta auth status") ||
		strings.Contains(fmt.Sprintf("%+v", rec.Verification), "/private/") {
		t.Fatalf("verification context leaked a probe or contract path: %+v", rec.Verification)
	}
}

func TestSynthesizeVerificationNeedUserInputGateWithContextBoundsDisplayWithoutDroppingDecisionItems(t *testing.T) {
	items := make([]TestingContractItem, 0, 101)
	results := make([]VerificationCheckResult, 0, 101)
	itemIDs := make([]string, 0, 101)
	for i := 0; i < 101; i++ {
		itemID := fmt.Sprintf("check-%03d", 100-i)
		capabilities := make([]TestingContractCapability, 0, 21)
		for capability := 0; capability < 21; capability++ {
			capabilities = append(capabilities, TestingContractCapability{
				Name: fmt.Sprintf("capability-%02d", capability),
			})
		}
		items = append(items, TestingContractItem{
			ID: itemID, Name: "Boundary check", Command: "make verify",
			Capabilities: capabilities,
		})
		results = append(results, VerificationCheckResult{
			ItemID: itemID, Status: VerificationStatusBlocked,
			BlockedReason: "missing declared capability",
		})
		itemIDs = append(itemIDs, itemID)
	}

	rec := SynthesizeVerificationNeedUserInputGateWithContext(
		"/private/testing-contract.yaml",
		&TestingContract{Version: 1, Revision: 4, Items: items},
		&VerificationReport{ContractRevision: 4, Results: results},
		itemIDs,
		3,
	)

	if got := len(rec.VerificationDecision.ItemIDs); got != 101 {
		t.Fatalf("trusted decision item IDs = %d, want all 101", got)
	}
	wantItemIDs := append([]string(nil), itemIDs...)
	sort.Strings(wantItemIDs)
	if !reflect.DeepEqual(rec.VerificationDecision.ItemIDs, wantItemIDs) {
		t.Fatalf(
			"trusted decision item IDs = %v, want canonical complete order %v",
			rec.VerificationDecision.ItemIDs,
			wantItemIDs,
		)
	}
	if rec.Verification == nil {
		t.Fatal("verification context = nil, want bounded display context")
	}
	if got := len(rec.Verification.Blockers); got != 100 {
		t.Fatalf("display blockers = %d, want 100", got)
	}
	for i, blocker := range rec.Verification.Blockers {
		if got := len(blocker.Capabilities); got != 20 {
			t.Fatalf("blocker %d capabilities = %d, want 20", i, got)
		}
	}
}

func TestBoundNeedUserInputVerificationStringExactUTF16Limits(t *testing.T) {
	for _, limit := range []int{200, 500, 64 * 1024} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			exact := strings.Repeat("a", limit)
			if got := BoundNeedUserInputVerificationString(exact, limit); got != exact {
				t.Fatalf("exact-limit value changed: got length %d, want %d", len(got), limit)
			}
			over := strings.Repeat("a", limit+1)
			want := strings.Repeat("a", limit-1) + "…"
			if got := BoundNeedUserInputVerificationString(over, limit); got != want {
				t.Fatalf("one-over value = %q, want exact bounded ellipsis result", got)
			}
		})
	}
}

func TestSynthesizeVerificationNeedUserInputGateWithoutContextRemainsLegacyCompatible(t *testing.T) {
	rec := SynthesizeVerificationNeedUserInputGate("/tmp/testing-contract.yaml", 1, []string{"item"}, 1)
	if rec.Verification != nil {
		t.Fatalf("legacy synthesis verification = %+v, want nil", rec.Verification)
	}
}

func TestApplyNeedUserVerificationDecisionPersistsWaiver(t *testing.T) {
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := TestingContract{Version: 1, Revision: 3, Items: []TestingContractItem{{
		ID: "protected", Source: testingContractPlanSource,
		Policy: TestingContractItemPolicy{Required: true, AllowWaiver: true},
	}}}
	if err := WriteTestingContract(contractPath, contract); err != nil {
		t.Fatal(err)
	}
	rec := SynthesizeVerificationNeedUserInputGate(contractPath, 3, []string{"protected"}, 2)
	rec.Questions[0].Answer = "WAIVE"
	gatePath := filepath.Join(filepath.Dir(contractPath), "iteration-02", NeedUserInputArtifactName)
	if err := ApplyNeedUserVerificationDecision(gatePath, rec); err != nil {
		t.Fatalf("ApplyNeedUserVerificationDecision() error = %v", err)
	}
	got, err := ReadTestingContract(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 4 || !IsTestingContractItemWaived(got.Items[0]) {
		t.Fatalf("contract after waiver = %+v, want revision 4 user waiver", got)
	}
	if err := ApplyNeedUserVerificationDecision(gatePath, rec); err != nil {
		t.Fatalf("second ApplyNeedUserVerificationDecision() should be idempotent: %v", err)
	}
}

func TestApplyNeedUserVerificationDecisionRequiresExactAction(t *testing.T) {
	for _, answer := range []string{"DO NOT WAIVE", "WAIVE RETRY_AFTER_AUTH", ""} {
		t.Run(answer, func(t *testing.T) {
			dir := t.TempDir()
			rec := SynthesizeVerificationNeedUserInputGate(filepath.Join(dir, "testing-contract.yaml"), 1, []string{"item"}, 1)
			rec.Questions[0].Answer = answer
			if err := ApplyNeedUserVerificationDecision(filepath.Join(dir, "iteration-01", NeedUserInputArtifactName), rec); err == nil {
				t.Fatalf("ApplyNeedUserVerificationDecision(%q) error = nil, want exact-action rejection", answer)
			}
		})
	}
}

func TestApplyNeedUserVerificationDecisionRejectsStaleRevision(t *testing.T) {
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := TestingContract{Version: 1, Revision: 4, Items: []TestingContractItem{{
		ID: "protected", Policy: TestingContractItemPolicy{Required: true, AllowWaiver: true},
	}}}
	if err := WriteTestingContract(contractPath, contract); err != nil {
		t.Fatal(err)
	}
	rec := SynthesizeVerificationNeedUserInputGate(contractPath, 3, []string{"protected"}, 2)
	rec.Questions[0].Answer = "WAIVE"
	gatePath := filepath.Join(filepath.Dir(contractPath), "iteration-02", NeedUserInputArtifactName)
	if err := ApplyNeedUserVerificationDecision(gatePath, rec); err == nil || !strings.Contains(err.Error(), "changed from revision") {
		t.Fatalf("ApplyNeedUserVerificationDecision() error = %v, want stale revision", err)
	}
}

func TestApplyNeedUserVerificationDecisionRetryAfterAuthDoesNotMutateContract(t *testing.T) {
	dir := t.TempDir()
	contractPath := filepath.Join(dir, "testing-contract.yaml")
	contract := TestingContract{Version: 1, Revision: 3, Items: []TestingContractItem{{ID: "protected"}}}
	if err := WriteTestingContract(contractPath, contract); err != nil {
		t.Fatal(err)
	}
	iterDir := filepath.Join(dir, "iteration-02")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(iterDir, "verification-report.yaml")
	if err := os.WriteFile(reportPath, []byte("contract_revision: 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := SynthesizeVerificationNeedUserInputGate(contractPath, 3, []string{"protected"}, 2)
	rec.Questions[0].Answer = "RETRY_AFTER_AUTH"
	gatePath := NeedUserInputPath(iterDir)
	if err := ApplyNeedUserVerificationDecision(gatePath, rec); err != nil {
		t.Fatalf("ApplyNeedUserVerificationDecision() error = %v", err)
	}
	got, err := ReadTestingContract(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 3 || IsTestingContractItemWaived(got.Items[0]) {
		t.Fatalf("contract mutated on retry-after-auth: %+v", got)
	}
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Fatalf("verification report survived retry-after-auth; resume would replay the blocked gate: %v", err)
	}
	// Idempotent when the report is already gone.
	if err := ApplyNeedUserVerificationDecision(gatePath, rec); err != nil {
		t.Fatalf("second ApplyNeedUserVerificationDecision() error = %v", err)
	}
}

func TestVerificationReportHasBlockedResults(t *testing.T) {
	if verificationReportHasBlockedResults(nil) {
		t.Fatal("nil report reported blocked results")
	}
	passed := &VerificationReport{Results: []VerificationCheckResult{{ItemID: "a", Status: VerificationStatusPassed}}}
	if verificationReportHasBlockedResults(passed) {
		t.Fatalf("passed-only report reported blocked results: %+v", passed)
	}
	blocked := &VerificationReport{Results: []VerificationCheckResult{
		{ItemID: "a", Status: VerificationStatusPassed},
		{ItemID: "b", Status: VerificationStatusBlocked},
	}}
	if !verificationReportHasBlockedResults(blocked) {
		t.Fatalf("blocked report not detected: %+v", blocked)
	}
}

func TestApplyNeedUserVerificationDecisionRejectsUntrustedGenericGate(t *testing.T) {
	err := ApplyNeedUserVerificationDecision(filepath.Join(t.TempDir(), NeedUserInputArtifactName), NeedUserInputRecord{
		Summary: "legacy agent-authored gate",
		Questions: []NeedUserInputQuestion{{
			Index: 1, Prompt: "Continue?", Answer: "yes",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "not a harness verification decision") {
		t.Fatalf("ApplyNeedUserVerificationDecision() error = %v, want untrusted-gate rejection", err)
	}
}

func unauthorizedWaiverTestContract() *TestingContract {
	return &TestingContract{Version: 1, Revision: 2, Items: []TestingContractItem{
		{ID: "visual_1", Source: testingContractVisualSource, Owner: TestingContractOwnerAgent, Name: "Settings screenshot",
			Policy: TestingContractItemPolicy{Required: true, AllowBlocked: true, AllowWaiver: true}},
		{ID: "plan_1", Source: testingContractPlanSource, Name: "Unit tests", Command: "go test ./...",
			Policy: TestingContractItemPolicy{Required: true, AllowWaiver: true}},
		{ID: "plan_2", Source: testingContractPlanSource, Name: "Lint", Command: "make lint",
			Policy: TestingContractItemPolicy{Required: true}},
		{ID: "plan_3", Source: testingContractPlanSource, Name: "Build", Command: "go build ./...",
			Policy: TestingContractItemPolicy{Required: true, AllowWaiver: true}},
	}}
}

// forgeWaivedRows marks the named report rows waived the way an implementer
// writing verification-report.yaml itself would.
func forgeWaivedRows(report *VerificationReport, itemIDs ...string) {
	for i := range report.Results {
		for _, itemID := range itemIDs {
			if report.Results[i].ItemID == itemID {
				report.Results[i].Status = VerificationStatusWaived
				report.Results[i].Notes = "operator said waive"
			}
		}
	}
}

func TestUnauthorizedWaiverItemIDsSplitsByWaiverPolicy(t *testing.T) {
	contract := unauthorizedWaiverTestContract()
	// plan_3 already carries a user waiver, so its waived row is legitimate.
	contract.Items[3].Disposition = TestingContractItemDisposition{Status: TestingContractDispositionWaived, Reason: "user approved", ChangedBy: "user"}
	report := BuildContractVerificationReportStub(contract, "/state/testing-contract.yaml")
	forgeWaivedRows(&report, "visual_1", "plan_1", "plan_2")

	waivable, unwaivable := UnauthorizedWaiverItemIDs(&report, contract)
	if !reflect.DeepEqual(waivable, []string{"plan_1", "visual_1"}) {
		t.Fatalf("waivable = %v, want [plan_1 visual_1]", waivable)
	}
	if !reflect.DeepEqual(unwaivable, []string{"plan_2"}) {
		t.Fatalf("unwaivable = %v, want [plan_2]", unwaivable)
	}
	if w, u := UnauthorizedWaiverItemIDs(nil, contract); w != nil || u != nil {
		t.Fatalf("nil report = %v, %v; want none", w, u)
	}
}

func TestSynthesizeUnauthorizedWaiverGateAsksUserForExactlyTheWaivableItems(t *testing.T) {
	contract := unauthorizedWaiverTestContract()
	contractPath := "/state/feat/testing-contract.yaml"
	report := BuildContractVerificationReportStub(contract, contractPath)
	forgeWaivedRows(&report, "visual_1", "plan_1", "plan_2")
	waivable, _ := UnauthorizedWaiverItemIDs(&report, contract)

	rec := SynthesizeUnauthorizedWaiverGate(contractPath, contract, waivable, 5)
	if rec.Source != NeedUserInputSourceUnauthorizedWaiver || rec.Iteration != 5 {
		t.Fatalf("gate source/iteration = %q/%d", rec.Source, rec.Iteration)
	}
	decision := rec.VerificationDecision
	if decision == nil || decision.ContractPath != contractPath || decision.ContractRevision != 2 ||
		!reflect.DeepEqual(decision.ItemIDs, []string{"plan_1", "visual_1"}) {
		t.Fatalf("decision = %+v, want plan_1 and visual_1 at revision 2", decision)
	}
	// visual_1 is agent-owned evidence that forbids substitution, so the
	// substitute decline is offered alongside the plain one.
	if !reflect.DeepEqual(decision.AllowedActions, []string{NeedUserVerificationWaive, NeedUserVerificationRetryAfterAuth, NeedUserVerificationAllowSubstitute}) {
		t.Fatalf("allowed actions = %v", decision.AllowedActions)
	}
	if !strings.Contains(rec.Summary, "waived") || !strings.Contains(rec.Summary, "no user-authorized waiver") {
		t.Fatalf("summary = %q, want unauthorized-waiver explanation", rec.Summary)
	}
	if len(rec.Questions) != 1 || !strings.Contains(rec.Questions[0].Prompt, "WAIVE") || !strings.Contains(rec.Questions[0].Prompt, "decline") {
		t.Fatalf("questions = %+v, want one confirm-or-decline prompt", rec.Questions)
	}
	if rec.Verification == nil || len(rec.Verification.Blockers) != 2 {
		t.Fatalf("blockers = %+v, want two", rec.Verification)
	}
	for _, blocker := range rec.Verification.Blockers {
		if !strings.Contains(blocker.Reason, "recorded this check as waived without a user-authorized waiver") {
			t.Fatalf("blocker reason = %q", blocker.Reason)
		}
	}
}

func TestUnauthorizedWaiverGateWaiveMakesReportConsistent(t *testing.T) {
	contract := unauthorizedWaiverTestContract()
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	if err := WriteTestingContract(contractPath, *contract); err != nil {
		t.Fatal(err)
	}
	report := BuildContractVerificationReportStub(contract, contractPath)
	forgeWaivedRows(&report, "plan_1")
	if gate := ValidateVerificationReport(&report, nil, contract, true); !gate.Rejected {
		t.Fatalf("forged waiver accepted before the user decided: %+v", gate)
	}
	waivable, _ := UnauthorizedWaiverItemIDs(&report, contract)
	rec := SynthesizeUnauthorizedWaiverGate(contractPath, contract, waivable, 1)
	rec.Questions[0].Answer = NeedUserVerificationWaive
	gatePath := filepath.Join(filepath.Dir(contractPath), "iteration-01", NeedUserInputArtifactName)
	if err := ApplyNeedUserVerificationDecision(gatePath, rec); err != nil {
		t.Fatalf("ApplyNeedUserVerificationDecision() error = %v", err)
	}

	revised, err := ReadTestingContract(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	idx := testingContractItemIndex(revised.Items, "plan_1")
	if !IsTestingContractItemWaived(revised.Items[idx]) || !strings.EqualFold(revised.Items[idx].Disposition.ChangedBy, "user") {
		t.Fatalf("plan_1 disposition = %+v, want user waiver", revised.Items[idx].Disposition)
	}
	// The harness rebuilds the report from the revised contract on resume.
	regenerated := BuildContractVerificationReportStub(revised, contractPath)
	for i := range regenerated.Results {
		if regenerated.Results[i].Status == VerificationStatusNotRun {
			regenerated.Results[i].Status = VerificationStatusPassed
			regenerated.Results[i].Evidence = "ran"
			regenerated.Results[i].EvidenceData.Summary = "ran"
		}
	}
	if gate := ValidateVerificationReport(&regenerated, nil, revised, true); gate.Rejected {
		t.Fatalf("report after WAIVE still rejected: %+v", gate.Findings)
	}
	if waivable, unwaivable := UnauthorizedWaiverItemIDs(&regenerated, revised); len(waivable)+len(unwaivable) != 0 {
		t.Fatalf("unauthorized waivers after WAIVE = %v, %v", waivable, unwaivable)
	}
}

func TestDeclinedUnauthorizedWaiverItemsReadsAnsweredDeclines(t *testing.T) {
	artifactDir := t.TempDir()
	contract := unauthorizedWaiverTestContract()
	write := func(iteration int, rec NeedUserInputRecord) {
		t.Helper()
		path := NeedUserInputPath(filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", iteration)))
		if err := WriteNeedUserInputRecord(path, rec); err != nil {
			t.Fatal(err)
		}
	}
	declined := SynthesizeUnauthorizedWaiverGate("/c", contract, []string{"plan_1"}, 1)
	declined.Questions[0].Answer = NeedUserVerificationRetryAfterAuth
	write(1, declined)
	waived := SynthesizeUnauthorizedWaiverGate("/c", contract, []string{"plan_3"}, 2)
	waived.Questions[0].Answer = NeedUserVerificationWaive
	write(2, waived)
	unanswered := SynthesizeUnauthorizedWaiverGate("/c", contract, []string{"visual_1"}, 3)
	write(3, unanswered)
	capability := SynthesizeVerificationNeedUserInputGate("/c", 2, []string{"plan_2"}, 4)
	capability.Questions[0].Answer = NeedUserVerificationRetryAfterAuth
	write(4, capability)

	got := declinedUnauthorizedWaiverItems(artifactDir, 4)
	if !reflect.DeepEqual(got, map[string]bool{"plan_1": true}) {
		t.Fatalf("declined = %v, want only plan_1", got)
	}
}

func TestValidateVerificationReportExplainsDeclinedWaiver(t *testing.T) {
	contract := unauthorizedWaiverTestContract()
	report := BuildContractVerificationReportStub(contract, "/state/testing-contract.yaml")
	forgeWaivedRows(&report, "plan_1", "plan_2")
	gate := ValidateVerificationReportWithContext(&report, nil, false, VerificationReportValidationContext{
		Contract:              contract,
		DeclinedWaiverItemIDs: map[string]bool{"plan_1": true},
	})
	details := reportGateDetailsForTest(gate)
	if !gate.Rejected || !strings.Contains(details, "operator declined to waive this item") {
		t.Fatalf("details = %q, want declined-waiver finding", details)
	}
	// The non-waivable item keeps the original finding.
	if !strings.Contains(details, "no user-authorized waiver for this item") {
		t.Fatalf("details = %q, want unchanged finding for plan_2", details)
	}
}

func TestReadNeedUserInputRecordRejectsInvalidQuestionIndex(t *testing.T) {
	tests := []struct {
		name      string
		questions []NeedUserInputQuestion
		wantErr   string
	}{
		{name: "zero", questions: []NeedUserInputQuestion{{Index: 1, Prompt: "Which database?"}, {Prompt: "Which rollout?"}}, wantErr: `"Which rollout?"`},
		{name: "negative", questions: []NeedUserInputQuestion{{Index: -2, Prompt: "Which database?"}}, wantErr: `"Which database?"`},
		{name: "duplicate", questions: []NeedUserInputQuestion{{Index: 1, Prompt: "Which database?"}, {Index: 1, Prompt: "Which rollout?"}}, wantErr: `"Which rollout?"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), NeedUserInputArtifactName)
			if err := WriteNeedUserInputRecord(path, NeedUserInputRecord{Summary: "blocked", Questions: tt.questions}); err != nil {
				t.Fatalf("WriteNeedUserInputRecord() error = %v", err)
			}
			_, err := ReadNeedUserInputRecord(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ReadNeedUserInputRecord() error = %v, want error naming %s", err, tt.wantErr)
			}
		})
	}
}

func TestReadNeedUserInputRecordKeepsStoredIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), NeedUserInputArtifactName)
	want := []NeedUserInputQuestion{{Index: 1, Prompt: "Which database?"}, {Index: 3, Prompt: "Which rollout?"}}
	if err := WriteNeedUserInputRecord(path, NeedUserInputRecord{Summary: "blocked", Questions: want}); err != nil {
		t.Fatalf("WriteNeedUserInputRecord() error = %v", err)
	}
	rec, err := ReadNeedUserInputRecord(path)
	if err != nil {
		t.Fatalf("ReadNeedUserInputRecord() error = %v", err)
	}
	if !reflect.DeepEqual(rec.Questions, want) {
		t.Fatalf("Questions = %+v, want %+v", rec.Questions, want)
	}
}
