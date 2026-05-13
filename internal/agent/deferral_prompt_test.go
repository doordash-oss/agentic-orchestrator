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

func TestDeferralsDueThisPhaseSection_EmptyLedgerNoBlock(t *testing.T) {
	if got := deferralsDueThisPhaseSection(nil, 3, deferralPromptKindImplement, ""); got != "" {
		t.Errorf("empty ledger emitted a block: %q", got)
	}
	if got := deferralsDueThisPhaseSection([]feature.Deferral{}, 3, deferralPromptKindImplement, ""); got != "" {
		t.Errorf("empty ledger slice emitted a block: %q", got)
	}
}

func TestDeferralsDueThisPhaseSection_WrongPhaseNoBlock(t *testing.T) {
	ledger := []feature.Deferral{
		{ID: "D-1", Description: "X", DueByPhase: 5, Status: feature.DeferralOpen},
	}
	if got := deferralsDueThisPhaseSection(ledger, 3, deferralPromptKindImplement, ""); got != "" {
		t.Errorf("phase 3 prompt picked up phase-5 deferral: %q", got)
	}
}

func TestDeferralsDueThisPhaseSection_DueThisPhaseEmitsBlock(t *testing.T) {
	ledger := []feature.Deferral{
		{
			ID:             "D-a3f1c0",
			Description:    "Install Tailwind + shadcn/ui",
			CreatedInPhase: 1,
			CreatedInKind:  "implement",
			DueByPhase:     3,
			Status:         feature.DeferralOpen,
			Reason:         "Scoped to design-system phase",
		},
	}
	for _, kind := range []deferralPromptKind{deferralPromptKindPlan, deferralPromptKindImplement, deferralPromptKindReview} {
		got := deferralsDueThisPhaseSection(ledger, 3, kind, "")
		if got == "" {
			t.Fatalf("kind %v: block absent", kind)
		}
		if !strings.Contains(got, "Deferrals Due This Phase") {
			t.Errorf("kind %v: missing header", kind)
		}
		if !strings.Contains(got, "D-a3f1c0") {
			t.Errorf("kind %v: missing ID", kind)
		}
		if !strings.Contains(got, "Install Tailwind + shadcn/ui") {
			t.Errorf("kind %v: missing description", kind)
		}
		if !strings.Contains(got, "Scoped to design-system phase") {
			t.Errorf("kind %v: missing reason", kind)
		}
	}
}

func TestDeferralsDueThisPhaseSection_ChronicSlippageFlagged(t *testing.T) {
	ledger := []feature.Deferral{
		{
			ID:          "D-slip",
			Description: "Migrate session store",
			DueByPhase:  5,
			Status:      feature.DeferralRedeferred,
			History: []feature.DeferralEvent{
				{Kind: feature.DeferralEventCreated},
				{Kind: feature.DeferralEventRedeferred},
				{Kind: feature.DeferralEventRedeferred},
			},
		},
	}
	got := deferralsDueThisPhaseSection(ledger, 5, deferralPromptKindReview, "")
	if !strings.Contains(got, "Re-deferred 2 time(s)") {
		t.Errorf("chronic slippage flag missing; block:\n%s", got)
	}
}

func TestDeferralsDueThisPhaseSection_KindWordingDiffers(t *testing.T) {
	ledger := []feature.Deferral{
		{ID: "D-x", Description: "Do X", DueByPhase: 3, Status: feature.DeferralOpen, Reason: "y"},
	}
	plan := deferralsDueThisPhaseSection(ledger, 3, deferralPromptKindPlan, "")
	impl := deferralsDueThisPhaseSection(ledger, 3, deferralPromptKindImplement, "")
	review := deferralsDueThisPhaseSection(ledger, 3, deferralPromptKindReview, "")

	if !strings.Contains(plan, "Your plan MUST address") {
		t.Errorf("plan wording missing the imperative to address")
	}
	if !strings.Contains(impl, "Report Integrity Gate refuses SUCCESS") {
		t.Errorf("implement wording missing the gate reference")
	}
	if !strings.Contains(review, "closed_deferrals") {
		t.Errorf("review wording missing closed_deferrals reference")
	}
}

// TestBuildPhasePlanPrompt_OmitsDueDeferrals confirms phase planning no longer
// carries deferral-ledger work into the plan prompt. Due deferrals are handled
// by implementation/review progress, not by creating plan-level deferrals.
func TestBuildPhasePlanPrompt_OmitsDueDeferrals(t *testing.T) {
	f := &feature.Feature{Name: "X"}
	run := &feature.Run{
		Deferrals: []feature.Deferral{
			{
				ID: "D-x", Description: "Install Tailwind", DueByPhase: 3,
				CreatedInPhase: 1, Status: feature.DeferralOpen, Reason: "Scope",
			},
		},
	}
	f.SetRun(run)
	prompt := BuildPhasePlanPrompt(f, "", "", "/tmp/roadmap.md", RoadmapPhase{Number: 3, Type: "tdd-fill-in", Name: "Wizard"}, nil)
	if strings.Contains(prompt, "Deferrals Due This Phase") {
		t.Errorf("plan prompt unexpectedly carries deferrals section")
	}
	if strings.Contains(prompt, "D-x") {
		t.Errorf("plan prompt unexpectedly carries deferral ID")
	}
	if strings.Contains(prompt, "Install Tailwind") {
		t.Errorf("plan prompt unexpectedly carries deferral description")
	}
}

// TestBuildPhasePlanPrompt_WrongPhaseNoBlock — the plan for phase 2 must
// not inherit entries destined for phase 3.
func TestBuildPhasePlanPrompt_WrongPhaseNoBlock(t *testing.T) {
	f := &feature.Feature{Name: "X"}
	run := &feature.Run{
		Deferrals: []feature.Deferral{
			{ID: "D-x", Description: "Z", DueByPhase: 3, Status: feature.DeferralOpen, Reason: "r"},
		},
	}
	f.SetRun(run)
	prompt := BuildPhasePlanPrompt(f, "", "", "/tmp/roadmap.md", RoadmapPhase{Number: 2, Name: "Detail"}, nil)
	if strings.Contains(prompt, "Deferrals Due This Phase") {
		t.Errorf("phase-2 prompt unexpectedly carries phase-3 deferrals block")
	}
}

func TestDeferralsDueThisPhaseSection_FeatureWideEntryAppearsInBothRepos(t *testing.T) {
	ledger := []feature.Deferral{
		{ID: "D-wide", Description: "Cross-cutting work", DueByPhase: 3, Status: feature.DeferralOpen, Reason: "r"},
	}
	for _, repoName := range []string{"repo-a", "repo-b"} {
		got := deferralsDueThisPhaseSection(ledger, 3, deferralPromptKindImplement, repoName)
		if got == "" {
			t.Errorf("repoName=%q: feature-wide entry should appear in block", repoName)
			continue
		}
		if !strings.Contains(got, "D-wide") {
			t.Errorf("repoName=%q: block missing entry ID", repoName)
		}
	}
}

func TestDeferralsDueThisPhaseSection_RepoScopedEntryFiltersOut(t *testing.T) {
	ledger := []feature.Deferral{
		{ID: "D-a", Description: "Repo-a-only work", DueByPhase: 3, Status: feature.DeferralOpen, Reason: "r", RepoScope: []string{"repo-a"}},
	}
	got := deferralsDueThisPhaseSection(ledger, 3, deferralPromptKindImplement, "repo-b")
	if got != "" {
		t.Errorf("repo-a-scoped entry should produce empty block for repo-b; got:\n%s", got)
	}
}

func TestDeferralsDueThisPhaseSection_RepoScopedEntryAppearsInScopedRepo(t *testing.T) {
	ledger := []feature.Deferral{
		{ID: "D-a", Description: "Repo-a-only work", DueByPhase: 3, Status: feature.DeferralOpen, Reason: "r", RepoScope: []string{"repo-a"}},
	}
	got := deferralsDueThisPhaseSection(ledger, 3, deferralPromptKindImplement, "repo-a")
	if got == "" || !strings.Contains(got, "D-a") {
		t.Errorf("scoped entry should appear in scoped repo's block; got:\n%s", got)
	}
}

func TestDeferralsDueThisPhaseSection_RepoScopeWordingPresentInImplementKind(t *testing.T) {
	ledger := []feature.Deferral{
		{ID: "D-x", Description: "X", DueByPhase: 3, Status: feature.DeferralOpen, Reason: "r"},
	}
	got := deferralsDueThisPhaseSection(ledger, 3, deferralPromptKindImplement, "repo-a")
	if !strings.Contains(got, "repo_scope:") {
		t.Errorf("implement-kind block should mention repo_scope: option so the agent learns it from the prompt; got:\n%s", got)
	}
}

func TestDeferralsDueThisPhaseSection_EmptyRepoNameUnchangedForLegacyCallers(t *testing.T) {
	ledger := []feature.Deferral{
		{ID: "D-wide", Description: "wide", DueByPhase: 3, Status: feature.DeferralOpen, Reason: "r"},
		{ID: "D-scoped", Description: "scoped", DueByPhase: 3, Status: feature.DeferralOpen, Reason: "r", RepoScope: []string{"repo-a"}},
	}
	got := deferralsDueThisPhaseSection(ledger, 3, deferralPromptKindPlan, "")
	if !strings.Contains(got, "D-wide") {
		t.Errorf("legacy empty-repoName caller should see feature-wide entry; got:\n%s", got)
	}
	if !strings.Contains(got, "D-scoped") {
		t.Errorf("legacy empty-repoName caller should see scoped entry too (permissive default); got:\n%s", got)
	}
}
