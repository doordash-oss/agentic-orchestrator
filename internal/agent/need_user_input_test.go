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
	"path/filepath"
	"reflect"
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
	if !reflect.DeepEqual(rec.VerificationDecision.ItemIDs, itemIDs) {
		t.Fatalf(
			"trusted decision item IDs = %v, want exact input order %v",
			rec.VerificationDecision.ItemIDs,
			itemIDs,
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
	if err := ApplyNeedUserVerificationDecision(rec); err != nil {
		t.Fatalf("ApplyNeedUserVerificationDecision() error = %v", err)
	}
	got, err := ReadTestingContract(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 4 || !IsTestingContractItemWaived(got.Items[0]) {
		t.Fatalf("contract after waiver = %+v, want revision 4 user waiver", got)
	}
	if err := ApplyNeedUserVerificationDecision(rec); err != nil {
		t.Fatalf("second ApplyNeedUserVerificationDecision() should be idempotent: %v", err)
	}
}

func TestApplyNeedUserVerificationDecisionRequiresExactAction(t *testing.T) {
	for _, answer := range []string{"DO NOT WAIVE", "WAIVE RETRY_AFTER_AUTH", ""} {
		t.Run(answer, func(t *testing.T) {
			rec := SynthesizeVerificationNeedUserInputGate(filepath.Join(t.TempDir(), "testing-contract.yaml"), 1, []string{"item"}, 1)
			rec.Questions[0].Answer = answer
			if err := ApplyNeedUserVerificationDecision(rec); err == nil {
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
	if err := ApplyNeedUserVerificationDecision(rec); err == nil || !strings.Contains(err.Error(), "changed from revision") {
		t.Fatalf("ApplyNeedUserVerificationDecision() error = %v, want stale revision", err)
	}
}

func TestApplyNeedUserVerificationDecisionRetryAfterAuthDoesNotMutateContract(t *testing.T) {
	contractPath := filepath.Join(t.TempDir(), "testing-contract.yaml")
	contract := TestingContract{Version: 1, Revision: 3, Items: []TestingContractItem{{ID: "protected"}}}
	if err := WriteTestingContract(contractPath, contract); err != nil {
		t.Fatal(err)
	}
	rec := SynthesizeVerificationNeedUserInputGate(contractPath, 3, []string{"protected"}, 2)
	rec.Questions[0].Answer = "RETRY_AFTER_AUTH"
	if err := ApplyNeedUserVerificationDecision(rec); err != nil {
		t.Fatalf("ApplyNeedUserVerificationDecision() error = %v", err)
	}
	got, err := ReadTestingContract(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 3 || IsTestingContractItemWaived(got.Items[0]) {
		t.Fatalf("contract mutated on retry-after-auth: %+v", got)
	}
}

func TestApplyNeedUserVerificationDecisionRejectsUntrustedGenericGate(t *testing.T) {
	err := ApplyNeedUserVerificationDecision(NeedUserInputRecord{
		Summary: "legacy agent-authored gate",
		Questions: []NeedUserInputQuestion{{
			Index: 1, Prompt: "Continue?", Answer: "yes",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "not a harness verification decision") {
		t.Fatalf("ApplyNeedUserVerificationDecision() error = %v, want untrusted-gate rejection", err)
	}
}
