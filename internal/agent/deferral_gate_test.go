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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestValidateDeferralLedger_MissingReasonRejected: every new structured
// entry must carry a reason; the ledger is useless to downstream phases
// without cited justifications.
func TestValidateDeferralLedger_MissingReasonRejected(t *testing.T) {
	deferrals := []feature.IncomingDeferral{
		{Description: "Install Tailwind", DueByPhase: 3, Reason: ""},
	}
	result := ValidateDeferralLedger(deferrals, nil, nil, 0, "")
	if !result.Rejected {
		t.Fatalf("expected rejection for reason-less deferral")
	}
	found := false
	for _, f := range result.Findings {
		if f.Kind == KindDeferralMissingReason {
			found = true
		}
	}
	if !found {
		t.Errorf("expected KindDeferralMissingReason finding; got %+v", result.Findings)
	}
}

// TestValidateDeferralLedger_UnclosedDueThisPhaseRejected is the primary
// regression guard: a ledger entry owed this phase is still open and the
// iteration neither closed nor re-deferred it.
func TestValidateDeferralLedger_UnclosedDueThisPhaseRejected(t *testing.T) {
	desc := "Install Tailwind + shadcn/ui"
	id := feature.DeferralID(1, desc)
	ledger := []feature.Deferral{
		{
			ID:             id,
			Description:    desc,
			CreatedInPhase: 1,
			DueByPhase:     3,
			Status:         feature.DeferralOpen,
			Reason:         "Scope",
		},
	}
	// Phase-3 iteration emits no closure or re-defer.
	result := ValidateDeferralLedger(nil, nil, ledger, 3, "")
	if !result.Rejected {
		t.Fatalf("expected rejection for unclosed due-this-phase deferral")
	}
	foundCitation := false
	for _, f := range result.Findings {
		if f.Kind == KindDeferralUnclosed && strings.Contains(f.Detail, id) {
			foundCitation = true
		}
	}
	if !foundCitation {
		t.Errorf("expected unclosed finding citing %s; got %+v", id, result.Findings)
	}
}

// TestValidateDeferralLedger_ClosedDeferralSatisfiesGate: when the
// iteration cites the ID in closed_deferrals, the gate is satisfied.
func TestValidateDeferralLedger_ClosedDeferralSatisfiesGate(t *testing.T) {
	desc := "Install Tailwind"
	id := feature.DeferralID(1, desc)
	ledger := []feature.Deferral{
		{ID: id, Description: desc, CreatedInPhase: 1, DueByPhase: 3, Status: feature.DeferralOpen, Reason: "Scope"},
	}
	result := ValidateDeferralLedger(nil, []string{id}, ledger, 3, "")
	for _, f := range result.Findings {
		if f.Kind == KindDeferralUnclosed {
			t.Errorf("unclosed finding fired when closed_deferrals cites %s: %+v", id, f)
		}
	}
}

// TestValidateDeferralLedger_RedeferralSatisfiesGate: when the iteration
// re-defers (same description, new DueByPhase, with reason), the gate is
// satisfied — chronic slippage is surfaced elsewhere, not blocked here.
func TestValidateDeferralLedger_RedeferralSatisfiesGate(t *testing.T) {
	desc := "Install Tailwind"
	id := feature.DeferralID(1, desc)
	ledger := []feature.Deferral{
		{ID: id, Description: desc, CreatedInPhase: 1, DueByPhase: 3, Status: feature.DeferralOpen, Reason: "Scope"},
	}
	deferrals := []feature.IncomingDeferral{
		{Description: desc, DueByPhase: 5, Reason: "Requires coordinated migration; scope shift"},
	}
	result := ValidateDeferralLedger(deferrals, nil, ledger, 3, "")
	for _, f := range result.Findings {
		if f.Kind == KindDeferralUnclosed {
			t.Errorf("unclosed finding fired when re-defer was emitted: %+v", f)
		}
	}
}

// TestValidateDeferralLedger_IDCitedRedeferWithDriftedDescription is the
// regression guard for the Native-App phase-3 stall: a re-defer that cites
// the ledger ID must satisfy the gate even when the description drifts.
// Before the fix, the gate matched by description hash only and any
// paraphrase left the original entry "unclosed" despite clear re-defer
// intent.
func TestValidateDeferralLedger_IDCitedRedeferWithDriftedDescription(t *testing.T) {
	originalDesc := "Install Tailwind"
	id := feature.DeferralID(1, originalDesc)
	ledger := []feature.Deferral{
		{ID: id, Description: originalDesc, CreatedInPhase: 1, DueByPhase: 3, Status: feature.DeferralOpen, Reason: "Scope"},
	}
	deferrals := []feature.IncomingDeferral{
		{
			ID:          id,
			Description: "Install Tailwind v4 + shadcn/ui with OKLCH token system",
			DueByPhase:  5,
			Reason:      "Phase 5 lands the design system per roadmap",
		},
	}
	result := ValidateDeferralLedger(deferrals, nil, ledger, 3, "")
	for _, f := range result.Findings {
		if f.Kind == KindDeferralUnclosed {
			t.Errorf("ID-cited re-defer with drifted description should satisfy gate; unclosed fired: %+v", f)
		}
	}
}

// TestValidateDeferralLedger_ZeroPhaseDisablesLedgerCheck: during early
// pipelines that haven't reached phase 1 yet, we shouldn't enforce
// due-this-phase on phase 0.
func TestValidateDeferralLedger_ZeroPhaseDisablesLedgerCheck(t *testing.T) {
	ledger := []feature.Deferral{
		{ID: "D-1", CreatedInPhase: 1, DueByPhase: 3, Status: feature.DeferralOpen},
	}
	result := ValidateDeferralLedger(nil, nil, ledger, 0, "")
	for _, f := range result.Findings {
		if f.Kind == KindDeferralUnclosed {
			t.Errorf("ledger check fired on currentPhase=0: %+v", f)
		}
	}
}

// TestValidateDeferralLedger_RepoScopedEntryDoesNotBlockOutOfScopeRepo
// confirms that a deferral scoped to repo-a does NOT produce an unclosed
// finding when the gate is run for repo-b. Out-of-scope repos must not be
// blocked by debt that doesn't apply to them.
func TestValidateDeferralLedger_RepoScopedEntryDoesNotBlockOutOfScopeRepo(t *testing.T) {
	desc := "Repo-a-only refactor"
	id := feature.DeferralID(1, desc)
	ledger := []feature.Deferral{
		{ID: id, Description: desc, CreatedInPhase: 1, DueByPhase: 3, Status: feature.DeferralOpen, Reason: "Scope", RepoScope: []string{"repo-a"}},
	}
	result := ValidateDeferralLedger(nil, nil, ledger, 3, "repo-b")
	for _, f := range result.Findings {
		if f.Kind == KindDeferralUnclosed {
			t.Errorf("repo-b gate should not be blocked by repo-a-scoped deferral; got finding: %+v", f)
		}
	}
}

// TestValidateDeferralLedger_RepoScopedEntryBlocksScopedRepo confirms the
// scoped repo IS blocked by its own debt. Same ledger as above, gate run
// for repo-a.
func TestValidateDeferralLedger_RepoScopedEntryBlocksScopedRepo(t *testing.T) {
	desc := "Repo-a-only refactor"
	id := feature.DeferralID(1, desc)
	ledger := []feature.Deferral{
		{ID: id, Description: desc, CreatedInPhase: 1, DueByPhase: 3, Status: feature.DeferralOpen, Reason: "Scope", RepoScope: []string{"repo-a"}},
	}
	result := ValidateDeferralLedger(nil, nil, ledger, 3, "repo-a")
	foundCitation := false
	for _, f := range result.Findings {
		if f.Kind == KindDeferralUnclosed && strings.Contains(f.Detail, id) {
			foundCitation = true
		}
	}
	if !foundCitation {
		t.Errorf("repo-a gate should report unclosed finding for repo-a-scoped deferral; got %+v", result.Findings)
	}
}

// TestValidateDeferralLedger_FeatureWideEntryBlocksAllRepos confirms that
// an unscoped (feature-wide) entry blocks every per-repo gate.
func TestValidateDeferralLedger_FeatureWideEntryBlocksAllRepos(t *testing.T) {
	desc := "Cross-cutting work"
	id := feature.DeferralID(1, desc)
	ledger := []feature.Deferral{
		{ID: id, Description: desc, CreatedInPhase: 1, DueByPhase: 3, Status: feature.DeferralOpen, Reason: "Scope"},
	}
	for _, repoName := range []string{"repo-a", "repo-b"} {
		result := ValidateDeferralLedger(nil, nil, ledger, 3, repoName)
		foundCitation := false
		for _, f := range result.Findings {
			if f.Kind == KindDeferralUnclosed && strings.Contains(f.Detail, id) {
				foundCitation = true
			}
		}
		if !foundCitation {
			t.Errorf("repoName=%q: feature-wide entry should block; got %+v", repoName, result.Findings)
		}
	}
}

// TestValidateDeferralLedger_EmptyRepoNameMatchesEverything locks in the
// permissive empty-repoName contract: legacy callers (those that pre-date
// per-repo gate threading) see every entry, never miss a block.
func TestValidateDeferralLedger_EmptyRepoNameMatchesEverything(t *testing.T) {
	wideDesc := "Cross-cutting"
	wideID := feature.DeferralID(1, wideDesc)
	scopedDesc := "Repo-a-only"
	scopedID := feature.DeferralID(1, scopedDesc)
	ledger := []feature.Deferral{
		{ID: wideID, Description: wideDesc, CreatedInPhase: 1, DueByPhase: 3, Status: feature.DeferralOpen, Reason: "Scope"},
		{ID: scopedID, Description: scopedDesc, CreatedInPhase: 1, DueByPhase: 3, Status: feature.DeferralOpen, Reason: "Scope", RepoScope: []string{"repo-a"}},
	}
	result := ValidateDeferralLedger(nil, nil, ledger, 3, "")
	wideFound, scopedFound := false, false
	for _, f := range result.Findings {
		if f.Kind == KindDeferralUnclosed {
			if strings.Contains(f.Detail, wideID) {
				wideFound = true
			}
			if strings.Contains(f.Detail, scopedID) {
				scopedFound = true
			}
		}
	}
	if !wideFound || !scopedFound {
		t.Errorf("empty repoName should produce findings for every open entry; wide=%v scoped=%v findings=%+v", wideFound, scopedFound, result.Findings)
	}
}

// TestMergeGateResults_CombinesFindings ensures that schema and deferral
// results combine cleanly so the agent sees all failure modes at once.
func TestMergeGateResults_CombinesFindings(t *testing.T) {
	a := ReportGateResult{
		Findings: []ReportGateFinding{{Kind: KindEmptyEvidence, Detail: "a"}},
	}
	b := ReportGateResult{
		Findings: []ReportGateFinding{{Kind: KindDeferralUnclosed, Detail: "b"}},
	}
	merged := MergeGateResults(a, b)
	if !merged.Rejected {
		t.Errorf("merged result should be Rejected when any input has findings")
	}
	if len(merged.Findings) != 2 {
		t.Errorf("merged findings = %d, want 2", len(merged.Findings))
	}
}
