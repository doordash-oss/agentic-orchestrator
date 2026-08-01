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

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestRefactorChildDiscardRecoveryJourney exercises the integration-attention
// discard path and the crash-recovery journey for interrupted discards:
//
//   - Part A (candidate rollback): A child with a transaction journal in the
//     "applied" phase is discarded. The discard flow must CAS-rollback every
//     provably child-applied parent ref to its anchor SHA, sync the parent
//     worktree, and close the child with outcome "discarded" while leaving
//     parent repositories free of child candidates.
//
//   - Part B (externally-moved ref → attention preserved): A child with an
//     applied journal where one parent ref was externally moved after the
//     apply. The discard flow must roll back provable refs, preserve the
//     externally moved ref (never overwrite), record diagnostics naming
//     repository, ref, anchor, candidate, and observed SHA, and keep the
//     child active with its discard intent. After the externally moved ref
//     is restored to the anchor, a retry discard completes.
//
//   - Part C (crash recovery): A child with an applied journal has its
//     discard intent durably recorded at "attention_resolved" — simulating a
//     crash after attention resolution but before ref safety. A fresh
//     orchestrator calls ReconcileDiscardIntents and the discard completes:
//     refs rolled back, child closed, cleanup done.
func TestRefactorChildDiscardRecoveryJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}

	// ------------------------------------------------------------------
	// Part A: Discard from applied state — candidate CAS rollback.
	// ------------------------------------------------------------------
	t.Run("applied_candidate_rollback", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		anchorSHAs := make([]string, len(fx.repoDirs))
		for i := range fx.repoDirs {
			anchorSHAs[i] = fx.refSHA(i, "refs/heads/feature/parent")
		}

		// Apply both refs to their candidate SHAs, simulating a completed
		// apply phase that crashed before close.
		for i := range journal.Entries {
			fx.manualApplyRef(t, i, &journal.Entries[i])
			journal.Entries[i].ApplyState = feature.RepoApplyApplied
		}
		journal.Phase = feature.TransactionPhaseApplied
		fx.saveJournal(journal)

		// Verify the refs actually moved to candidates before discard.
		for i := range fx.repoDirs {
			got := fx.refSHA(i, "refs/heads/feature/parent")
			if got != journal.Entries[i].CandidateSHA {
				t.Fatalf("repo %d: ref = %s before discard, want candidate %s", i, got, journal.Entries[i].CandidateSHA)
			}
		}

		o := fx.orchestrator()
		if err := o.DiscardChild(fx.child.ID); err != nil {
			t.Fatalf("DiscardChild() error = %v", err)
		}

		_, child := fx.reload()

		// The child must be closed with outcome "discarded".
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
			t.Fatalf("close_outcome = %q, want discarded", child.Parent.CloseOutcome)
		}
		if child.Parent.ClosedAt == nil {
			t.Fatal("closed_at missing")
		}
		if child.DiscardIntent == nil || child.DiscardIntent.Step != feature.DiscardStepCleanupDone {
			t.Fatalf("discard step = %v, want cleanup_done", child.DiscardIntent)
		}

		// Every parent ref must be rolled back to its anchor SHA — no
		// child candidate remains.
		for i := range fx.repoDirs {
			got := fx.refSHA(i, "refs/heads/feature/parent")
			if got != anchorSHAs[i] {
				t.Fatalf("repo %d: ref = %s after discard, want anchor %s (CAS rollback)", i, got, anchorSHAs[i])
			}
		}

		// Parent worktrees must be synced back to the anchor.
		for i := range fx.repoDirs {
			wtHead := multiRepoGit(t, fx.repoDirs[i], "rev-parse", "HEAD")
			if wtHead != anchorSHAs[i] {
				t.Fatalf("repo %d: worktree HEAD = %s, want anchor %s", i, wtHead, anchorSHAs[i])
			}
		}

		// The transaction journal must show all entries rolled back.
		tx := child.Parent.Transaction
		if tx == nil {
			t.Fatal("transaction journal missing after discard")
		}
		if tx.Phase != feature.TransactionPhaseRolledBack {
			t.Fatalf("journal phase = %q, want rolled_back", tx.Phase)
		}
		for i := range tx.Entries {
			if tx.Entries[i].ApplyState != feature.RepoApplyRolledBack {
				t.Fatalf("entry %d apply_state = %q, want rolled_back", i, tx.Entries[i].ApplyState)
			}
		}

		// The parent must not be moved to CodeReady or record integration success.
		parent, _ := fx.reload()
		if parent.Status == feature.StatusCodeReady {
			t.Fatal("parent moved to CodeReady after discard; must not")
		}
	})

	// ------------------------------------------------------------------
	// Part B: Externally moved ref → attention preserved, then retry.
	// ------------------------------------------------------------------
	t.Run("externally_moved_ref_attention", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 3)
		journal := fx.manualPrepare(t)

		anchorSHAs := make([]string, len(fx.repoDirs))
		for i := range fx.repoDirs {
			anchorSHAs[i] = fx.refSHA(i, "refs/heads/feature/parent")
		}

		// Apply all three refs to candidates.
		for i := range journal.Entries {
			fx.manualApplyRef(t, i, &journal.Entries[i])
			journal.Entries[i].ApplyState = feature.RepoApplyApplied
		}
		journal.Phase = feature.TransactionPhaseApplied
		fx.saveJournal(journal)

		// Externally move repo 2's parent ref after the apply: create a
		// new commit on the parent branch that is neither the anchor nor
		// the candidate.
		multiRepoGit(t, fx.repoDirs[2], "checkout", "feature/parent")
		testutil.CommitFile(t, fx.repoDirs[2], "external.txt", "external\n", "external ref movement")
		externalSHA := fx.refSHA(2, "refs/heads/feature/parent")
		if externalSHA == anchorSHAs[2] || externalSHA == journal.Entries[2].CandidateSHA {
			t.Fatal("external SHA should differ from both anchor and candidate")
		}

		o := fx.orchestrator()
		err := o.DiscardChild(fx.child.ID)
		if err == nil {
			t.Fatal("DiscardChild() should fail when ref safety cannot be proven")
		}
		if !strings.Contains(err.Error(), "ref safety") {
			t.Fatalf("DiscardChild() error = %v, want ref safety error", err)
		}

		_, child := fx.reload()

		// The child must remain active (not closed) with a durable
		// discard intent.
		if child.Parent.CloseOutcome != "" {
			t.Fatalf("close_outcome = %q, want empty (child must remain active)", child.Parent.CloseOutcome)
		}
		if child.DiscardIntent == nil {
			t.Fatal("discard intent missing; must remain durable")
		}

		// Repos 0 and 1 must be rolled back to their anchor SHAs.
		for i := 0; i < 2; i++ {
			got := fx.refSHA(i, "refs/heads/feature/parent")
			if got != anchorSHAs[i] {
				t.Fatalf("repo %d: ref = %s, want anchor %s (CAS rollback)", i, got, anchorSHAs[i])
			}
		}

		// Repo 2's externally moved ref must be preserved (not overwritten).
		got := fx.refSHA(2, "refs/heads/feature/parent")
		if got != externalSHA {
			t.Fatalf("repo 2: ref = %s, want externally moved %s (must not overwrite)", got, externalSHA)
		}

		// The journal must be in attention phase with diagnostics.
		tx := child.Parent.Transaction
		if tx == nil {
			t.Fatal("transaction journal missing")
		}
		if tx.Phase != feature.TransactionPhaseAttention {
			t.Fatalf("journal phase = %q, want attention", tx.Phase)
		}
		if tx.Attention == "" {
			t.Fatal("journal attention summary is empty; want diagnostics")
		}

		// The externally moved entry must have diagnostics naming repo,
		// ref, anchor, candidate, and observed SHA.
		movedEntry := tx.EntryByRepo(child.Repos[2].Name)
		if movedEntry == nil {
			t.Fatal("moved entry missing from journal")
		}
		if movedEntry.ApplyState != feature.RepoApplyApplied {
			t.Fatalf("moved entry apply_state = %q, want applied (not rolled back)", movedEntry.ApplyState)
		}
		if movedEntry.ObservedSHA != externalSHA {
			t.Fatalf("moved entry observed_sha = %s, want external %s", movedEntry.ObservedSHA, externalSHA)
		}
		if movedEntry.Diagnostics == "" {
			t.Fatal("moved entry diagnostics empty; want external race diagnostics")
		}
		// Diagnostics must name the repository, ref, anchor, candidate,
		// and observed SHA.
		for _, needle := range []string{
			movedEntry.Repo,
			"refs/heads/" + movedEntry.ParentBranch,
			movedEntry.ParentAnchorSHA,
			movedEntry.CandidateSHA,
			movedEntry.ObservedSHA,
		} {
			if !strings.Contains(movedEntry.Diagnostics, needle) {
				t.Fatalf("diagnostics %q missing %q", movedEntry.Diagnostics, needle)
			}
		}

		// The rolled-back entries must be in rolled_back state.
		for i := 0; i < 2; i++ {
			entry := tx.EntryByRepo(child.Repos[i].Name)
			if entry == nil {
				t.Fatalf("repo %d entry missing", i)
			}
			if entry.ApplyState != feature.RepoApplyRolledBack {
				t.Fatalf("repo %d apply_state = %q, want rolled_back", i, entry.ApplyState)
			}
		}

		// Another child cannot launch while the relationship is active.
		if _, err := fx.mgr.CreateRefactorChild(fx.parent.ID, feature.RefactorChildSpec{
			Name:     "should fail",
			Pipeline: feature.PipelineMedium,
		}); err == nil {
			t.Fatal("CreateRefactorChild should fail while discard is in attention")
		}

		// Fix the externally moved ref: reset it back to the anchor.
		// The reset --hard moves both HEAD and the branch ref.
		multiRepoGit(t, fx.repoDirs[2], "checkout", "feature/parent")
		multiRepoGit(t, fx.repoDirs[2], "reset", "--hard", anchorSHAs[2])

		// Retry the discard — it should now complete.
		if err := o.DiscardChild(fx.child.ID); err != nil {
			t.Fatalf("retry DiscardChild() error = %v", err)
		}

		_, child = fx.reload()
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
			t.Fatalf("close_outcome after retry = %q, want discarded", child.Parent.CloseOutcome)
		}
		if child.DiscardIntent == nil || child.DiscardIntent.Step != feature.DiscardStepCleanupDone {
			t.Fatalf("discard step after retry = %v, want cleanup_done", child.DiscardIntent)
		}

		// All parent refs must now be at the anchor (no child candidate).
		for i := range fx.repoDirs {
			got := fx.refSHA(i, "refs/heads/feature/parent")
			if got != anchorSHAs[i] {
				t.Fatalf("repo %d: ref = %s after retry, want anchor %s", i, got, anchorSHAs[i])
			}
		}
	})

	// ------------------------------------------------------------------
	// Part C: Crash recovery from integration-attention discard.
	// ------------------------------------------------------------------
	t.Run("crash_recovery_from_attention_resolved", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		anchorSHAs := make([]string, len(fx.repoDirs))
		for i := range fx.repoDirs {
			anchorSHAs[i] = fx.refSHA(i, "refs/heads/feature/parent")
		}

		// Apply both refs to candidates.
		for i := range journal.Entries {
			fx.manualApplyRef(t, i, &journal.Entries[i])
			journal.Entries[i].ApplyState = feature.RepoApplyApplied
		}
		journal.Phase = feature.TransactionPhaseApplied
		fx.saveJournal(journal)

		// Simulate a crash: durably record the discard intent at
		// attention_resolved — as if the orchestrator crashed after
		// resolving attention but before the ref safety step.
		intentTime := time.Now().UTC()
		if err := fx.store.Modify(fx.child.ID, func(f *feature.Feature) error {
			f.DiscardIntent = &feature.DiscardIntent{
				RequestedAt: intentTime,
				Step:        feature.DiscardStepAttentionResolved,
			}
			return nil
		}); err != nil {
			t.Fatalf("record crash discard intent: %v", err)
		}

		// Create a fresh orchestrator pointing at the same state. This
		// simulates a restart that discovers the interrupted discard.
		recoveryOrch := fx.orchestrator()

		if err := recoveryOrch.ReconcileDiscardIntents(); err != nil {
			t.Fatalf("ReconcileDiscardIntents: %v", err)
		}

		_, child := fx.reload()

		// The discard must have completed through the ref safety step
		// (CAS rollback), closure, and cleanup tail.
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
			t.Fatalf("close_outcome = %q, want discarded", child.Parent.CloseOutcome)
		}
		if child.Parent.ClosedAt == nil {
			t.Fatal("closed_at missing after reconcile")
		}
		if child.DiscardIntent == nil || child.DiscardIntent.Step != feature.DiscardStepCleanupDone {
			t.Fatalf("discard step = %v, want cleanup_done", child.DiscardIntent)
		}

		// All parent refs must be rolled back to their anchor SHAs.
		for i := range fx.repoDirs {
			got := fx.refSHA(i, "refs/heads/feature/parent")
			if got != anchorSHAs[i] {
				t.Fatalf("repo %d: ref = %s after recovery, want anchor %s", i, got, anchorSHAs[i])
			}
		}

		// The journal must show all entries rolled back.
		tx := child.Parent.Transaction
		if tx == nil {
			t.Fatal("transaction journal missing after recovery")
		}
		if tx.Phase != feature.TransactionPhaseRolledBack {
			t.Fatalf("journal phase = %q, want rolled_back", tx.Phase)
		}
		for i := range tx.Entries {
			if tx.Entries[i].ApplyState != feature.RepoApplyRolledBack {
				t.Fatalf("entry %d apply_state = %q, want rolled_back", i, tx.Entries[i].ApplyState)
			}
		}

		// The parent must not have moved to CodeReady or recorded
		// integration success.
		parent, _ := fx.reload()
		if parent.Status == feature.StatusCodeReady {
			t.Fatal("parent moved to CodeReady after discard recovery; must not")
		}

		// Idempotent: a second reconcile is a no-op.
		if err := recoveryOrch.ReconcileDiscardIntents(); err != nil {
			t.Fatalf("second ReconcileDiscardIntents: %v", err)
		}
		_, child2 := fx.reload()
		if child2.DiscardIntent.Step != feature.DiscardStepCleanupDone {
			t.Fatalf("discard step after second reconcile = %v, want cleanup_done", child2.DiscardIntent.Step)
		}
	})

	// ------------------------------------------------------------------
	// Part D: Discard from applying crash — ref at candidate but entry
	// not persisted as applied.
	// ------------------------------------------------------------------
	t.Run("applying_crash_ref_at_candidate_not_persisted", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		anchorSHAs := make([]string, len(fx.repoDirs))
		for i := range fx.repoDirs {
			anchorSHAs[i] = fx.refSHA(i, "refs/heads/feature/parent")
		}

		// Simulate an applying crash: the CAS succeeds for repo 0 (ref
		// moves to candidate) but the entry is NOT persisted as applied
		// because the crash happened between the CAS and the
		// persistTransaction call. The journal stays in "applying"
		// phase and the entry's ApplyState is empty.
		fx.manualApplyRef(t, 0, &journal.Entries[0])
		journal.Phase = feature.TransactionPhaseApplying
		// Deliberately do NOT set journal.Entries[0].ApplyState to
		// RepoApplyApplied — the crash left the entry un-persisted.
		fx.saveJournal(journal)

		// Verify repo 0's ref is at the candidate.
		if got := fx.refSHA(0, "refs/heads/feature/parent"); got != journal.Entries[0].CandidateSHA {
			t.Fatalf("repo 0 ref = %s, want candidate %s", got, journal.Entries[0].CandidateSHA)
		}

		o := fx.orchestrator()
		if err := o.DiscardChild(fx.child.ID); err != nil {
			t.Fatalf("DiscardChild() error = %v", err)
		}

		_, child := fx.reload()

		// The child must be closed as discarded.
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
			t.Fatalf("close_outcome = %q, want discarded", child.Parent.CloseOutcome)
		}

		// Repo 0's ref must be rolled back to its anchor — the discard
		// flow inspected the actual ref (not just the ApplyState) and
		// found it at the candidate even though the entry was not
		// persisted as applied.
		if got := fx.refSHA(0, "refs/heads/feature/parent"); got != anchorSHAs[0] {
			t.Fatalf("repo 0 ref = %s after discard, want anchor %s (crash-entry rollback)", got, anchorSHAs[0])
		}

		// Repo 1's ref must still be at its anchor (never applied).
		if got := fx.refSHA(1, "refs/heads/feature/parent"); got != anchorSHAs[1] {
			t.Fatalf("repo 1 ref = %s, want anchor %s", got, anchorSHAs[1])
		}
	})

	// ------------------------------------------------------------------
	// Part E: ScanRecovery processes discard intents before integration
	// reconciliation. A child with a discard intent and an all-at-candidate
	// journal must be rolled back and closed as discarded, NOT completed
	// and published by integration reconciliation.
	// ------------------------------------------------------------------
	t.Run("scan_recovery_discard_before_integration", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		anchorSHAs := make([]string, len(fx.repoDirs))
		for i := range fx.repoDirs {
			anchorSHAs[i] = fx.refSHA(i, "refs/heads/feature/parent")
		}

		// Apply all refs to candidates (all-at-candidate journal).
		for i := range journal.Entries {
			fx.manualApplyRef(t, i, &journal.Entries[i])
			journal.Entries[i].ApplyState = feature.RepoApplyApplied
		}
		journal.Phase = feature.TransactionPhaseApplied
		fx.saveJournal(journal)

		// Record a durable discard intent at attention_resolved — as if
		// the orchestrator crashed after resolving attention but before
		// ref safety. The child has BOTH a discard intent AND an
		// all-at-candidate applied journal. ScanRecovery must process the
		// discard first, not close the child as completed.
		intentTime := time.Now().UTC()
		if err := fx.store.Modify(fx.child.ID, func(f *feature.Feature) error {
			f.DiscardIntent = &feature.DiscardIntent{
				RequestedAt: intentTime,
				Step:        feature.DiscardStepAttentionResolved,
			}
			return nil
		}); err != nil {
			t.Fatalf("record crash discard intent: %v", err)
		}

		recoveryOrch := fx.orchestrator()

		// ScanRecovery processes discard intents before integration
		// reconciliation. The discard will roll back the refs and close
		// the child as discarded. Integration reconciliation must NOT
		// close it as completed or publish the parent. We call the two
		// reconciliation passes in the same order as ScanRecovery
		// (discard first, then integration) to verify the ordering.
		if err := recoveryOrch.ReconcileDiscardIntents(); err != nil {
			t.Fatalf("ReconcileDiscardIntents: %v", err)
		}
		if err := recoveryOrch.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("ReconcileIntegrationTransactions: %v", err)
		}

		_, child := fx.reload()
		parent, _ := fx.reload()

		// The child must be closed as discarded, NOT completed.
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
			t.Fatalf("close_outcome = %q, want discarded (not completed)", child.Parent.CloseOutcome)
		}

		// The parent must NOT have been moved to CodeReady or published.
		if parent.Status == feature.StatusCodeReady {
			t.Fatal("parent moved to CodeReady; discard should not trigger integration closure")
		}

		// All parent refs must be rolled back to anchors.
		for i := range fx.repoDirs {
			if got := fx.refSHA(i, "refs/heads/feature/parent"); got != anchorSHAs[i] {
				t.Fatalf("repo %d ref = %s, want anchor %s", i, got, anchorSHAs[i])
			}
		}
	})
}
