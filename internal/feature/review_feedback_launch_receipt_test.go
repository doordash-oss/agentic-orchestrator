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

package feature_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	gitadapter "github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// launchReceiptManager builds the two-repo launch fixture: a published parent
// with both PR'd worktrees and a pending draft reconciled from the reviewed
// snapshot, all comments selected.
func launchReceiptManager(t *testing.T) (*feature.Manager, *feature.Feature, *feature.ReviewFeedbackDraft) {
	t.Helper()
	heads := map[string]string{"/wt/api": strings.Repeat("a", 40), "/wt/web": strings.Repeat("b", 40)}
	mgr := newChildTestManager(t, heads, cleanEverywhere())
	parent := launchTestParent()
	saveChildTestParent(t, mgr, parent)
	draft := feature.ReconcileReviewFeedbackDraft(parent, nil, reviewedComments())
	if err := mgr.Store.SaveReviewFeedbackDraft(parent.ID, draft, 0); err != nil {
		t.Fatal(err)
	}
	return mgr, parent, draft
}

// launchReceiptFetch yields one changed comment (11), omits web's deleted
// comment (21), and adds one deferred comment (30), giving 1/1/1 counts.
func launchReceiptFetch() map[string][]gitadapter.ReviewComment {
	return map[string][]gitadapter.ReviewComment{
		"api": {
			launchComment(11, feature.ReviewFeedbackCommentTypeReview, "alice", "edited after review"),
			launchComment(12, feature.ReviewFeedbackCommentTypeReview, "bob", "will be edited"),
			launchComment(30, feature.ReviewFeedbackCommentTypeIssue, "erin", "arrived later"),
		},
	}
}

// installCountingLaunchFetchStub installs the GitHub resolver and returns the
// number of resolver calls made.
func installCountingLaunchFetchStub(t *testing.T, byRepo map[string][]gitadapter.ReviewComment) *int {
	t.Helper()
	calls := new(int)
	restore := feature.SwapFetchPRCommentsForTest(func(_ string, prURL string) ([]gitadapter.ReviewComment, error) {
		*calls++
		if strings.Contains(prURL, "/api/") {
			return byRepo["api"], nil
		}
		return byRepo["web"], nil
	})
	t.Cleanup(restore)
	return calls
}

func assertReceiptCounts(t *testing.T, result *feature.ReviewFeedbackLaunchResult, changed, omitted, deferred int) {
	t.Helper()
	if result.Changed != changed || result.Omitted != omitted || result.Deferred != deferred {
		t.Fatalf("counts = changed:%d omitted:%d deferred:%d, want %d/%d/%d",
			result.Changed, result.Omitted, result.Deferred, changed, omitted, deferred)
	}
}

func TestLaunchRepeatReplaysDurableReceiptWithoutRefetchOrSecondChild(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)
	fetchCalls := installCountingLaunchFetchStub(t, launchReceiptFetch())

	gate := true
	first, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate)
	if err != nil {
		t.Fatalf("first launch error = %v", err)
	}
	assertReceiptCounts(t, first, 1, 1, 1)
	if first.Replayed {
		t.Fatal("first launch must not report replay")
	}
	resolutionCalls := *fetchCalls
	if resolutionCalls == 0 {
		t.Fatal("first launch must resolve GitHub content")
	}

	// The durable receipt rides the child relationship: revision, gate
	// choice, and counts survive a reload.
	reloadedChild, err := mgr.Store.Load(first.Child.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := reloadedChild.Parent.LaunchReceipt
	if receipt == nil {
		t.Fatal("reloaded child carries no launch receipt")
	}
	if receipt.DraftRevision != draft.Revision || receipt.Gate == nil || *receipt.Gate != true {
		t.Fatalf("receipt revision/gate = %d/%v, want %d/true", receipt.DraftRevision, receipt.Gate, draft.Revision)
	}
	if receipt.Changed != 1 || receipt.Omitted != 1 || receipt.Deferred != 1 {
		t.Fatalf("receipt counts = %d/%d/%d, want 1/1/1", receipt.Changed, receipt.Omitted, receipt.Deferred)
	}

	second, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate)
	if err != nil {
		t.Fatalf("repeated launch error = %v", err)
	}
	if !second.Replayed {
		t.Fatal("repeated launch must report replay")
	}
	if second.Child.ID != first.Child.ID {
		t.Fatalf("replayed child ID = %q, want original %q", second.Child.ID, first.Child.ID)
	}
	assertReceiptCounts(t, second, 1, 1, 1)
	if *fetchCalls != resolutionCalls {
		t.Fatalf("replay resolved GitHub again: fetch calls %d, want %d", *fetchCalls, resolutionCalls)
	}
	// Exactly one child exists for the parent.
	children, err := mgr.Store.RelationshipChildren(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if children.Active == nil || children.Active.ID != first.Child.ID || len(children.Closed) != 0 {
		t.Fatalf("relationship children = %+v, want exactly the original active child", children)
	}
}

func TestLaunchReplayCompletesInterruptedDraftCleanup(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)
	installLaunchFetchStub(t, launchReceiptFetch())

	gate := true
	first, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate)
	if err != nil {
		t.Fatalf("first launch error = %v", err)
	}
	// Simulate a crash between child creation and draft deletion: the draft
	// is still durably present at the receipt's revision.
	if err := mgr.Store.SaveReviewFeedbackDraft(parent.ID, draft, 0); err != nil {
		t.Fatal(err)
	}

	replayed, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate)
	if err != nil {
		t.Fatalf("repeated launch error = %v", err)
	}
	if !replayed.Replayed || replayed.Child.ID != first.Child.ID {
		t.Fatalf("replay = %+v, want original child %q", replayed, first.Child.ID)
	}
	assertReceiptCounts(t, replayed, 1, 1, 1)
	if kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID); err != nil || kept != nil {
		t.Fatalf("draft after replay = %+v (err %v), want cleanup completed", kept, err)
	}
}

func TestLaunchReplayRollsForwardPendingCreationIntent(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)
	fetchCalls := installCountingLaunchFetchStub(t, launchReceiptFetch())

	gate := true
	// Inject a crash at the child-save boundary: the durable intent (with
	// the launch receipt) is committed, the child is not materialized, and
	// the draft is still on disk pinned at the consumed revision, so replay
	// finishes both the roll-forward and the pinned draft cleanup.
	mgr.Store.SetSaveHook(&feature.StoreSaveHook{FailOnCall: 2})
	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate); err == nil || !strings.Contains(err.Error(), "saving child") {
		t.Fatalf("interrupted launch error = %v, want injected child-save failure", err)
	}
	mgr.Store.ResetSaveHook()
	resolutionCalls := *fetchCalls

	interrupted, err := mgr.Store.Load(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if interrupted.PendingChild == nil || interrupted.PendingChild.LaunchReceipt == nil {
		t.Fatalf("pending intent = %+v, want review-feedback intent with launch receipt", interrupted.PendingChild)
	}
	pendingChildID := interrupted.PendingChild.ChildID
	pendingReceipt := interrupted.PendingChild.LaunchReceipt
	if pendingReceipt.DraftRevision != draft.Revision {
		t.Fatalf("intent receipt revision = %d, want %d", pendingReceipt.DraftRevision, draft.Revision)
	}
	// The consumed draft is still durable at the pinned revision: deletion
	// happens only after the intent commits, never inside the pre-commit
	// callback.
	pinned, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pinned == nil || pinned.Revision != draft.Revision || pinned.SnapshotID != draft.SnapshotID {
		t.Fatalf("draft after interrupted launch = %+v, want pinned revision %d snapshot %q", pinned, draft.Revision, draft.SnapshotID)
	}

	replayed, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate)
	if err != nil {
		t.Fatalf("replay after interrupted creation error = %v", err)
	}
	if !replayed.Replayed || replayed.Child.ID != pendingChildID {
		t.Fatalf("replay = %+v, want pending child %q", replayed, pendingChildID)
	}
	assertReceiptCounts(t, replayed, pendingReceipt.Changed, pendingReceipt.Omitted, pendingReceipt.Deferred)
	if *fetchCalls != resolutionCalls {
		t.Fatalf("replay resolved GitHub again: fetch calls %d, want %d", *fetchCalls, resolutionCalls)
	}
	settled, err := mgr.Store.Load(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.PendingChild != nil {
		t.Fatalf("settled parent PendingChild = %+v, want nil", settled.PendingChild)
	}
	if kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID); err != nil || kept != nil {
		t.Fatalf("draft after replay = %+v (err %v), want cleanup completed", kept, err)
	}
	// A further reconcile pass is a no-op: no intent survives.
	if reconciled, err := mgr.Store.ReconcilePendingChildCreations(); err != nil || len(reconciled) != 0 {
		t.Fatalf("reconcile after replay = %v (err %v), want no intents", reconciled, err)
	}
}

func TestLaunchInterruptionBeforeFirstIntentSavePreservesAcknowledgedDraft(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)
	installLaunchFetchStub(t, launchReceiptFetch())

	gate := true
	// Fail the first saveUnlocked call — the durable parent intent never
	// commits. Every pre-commit exit (this storage fault or a process
	// interruption at the same boundary) must leave the acknowledged draft
	// fully intact on disk: the draft is never deleted before the intent is
	// durable, so nothing has to be rolled back.
	mgr.Store.SetSaveHook(&feature.StoreSaveHook{FailOnCall: 1})
	defer mgr.Store.ResetSaveHook()
	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate); err == nil || !strings.Contains(err.Error(), "saving parent with child intent") {
		t.Fatalf("interrupted launch error = %v, want injected intent-save failure", err)
	}

	restored, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored == nil || restored.Revision != draft.Revision || restored.SnapshotID != draft.SnapshotID {
		t.Fatalf("restored draft = %+v, want revision %d snapshot %q", restored, draft.Revision, draft.SnapshotID)
	}
	if len(restored.Items) != len(draft.Items) {
		t.Fatalf("restored draft items = %d, want %d", len(restored.Items), len(draft.Items))
	}
	for i, item := range draft.Items {
		if restored.Items[i] != item {
			t.Fatalf("restored item %d = %+v, want %+v", i, restored.Items[i], item)
		}
	}

	reloaded, err := mgr.Store.Load(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PendingChild != nil {
		t.Fatalf("parent after intent-save failure = pending %+v, want none", reloaded.PendingChild)
	}
	children, err := mgr.Store.RelationshipChildren(parent.ID)
	if err != nil || children.Active != nil || len(children.Closed) != 0 {
		t.Fatalf("relationship children = %+v (err %v), want no child after rollback", children, err)
	}

	// The preserved draft is launchable: a retry commits normally and
	// creates exactly one child.
	mgr.Store.ResetSaveHook()
	result, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate)
	if err != nil {
		t.Fatalf("retry launch error = %v", err)
	}
	assertReceiptCounts(t, result, 1, 1, 1)
}

func TestSelectionCommitRejectsDurableConsumedMarker(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)
	installLaunchFetchStub(t, launchReceiptFetch())

	gate := true
	// Interrupt after the durable intent commits: the pending intent is the
	// consumption marker pinned to the draft's revision.
	mgr.Store.SetSaveHook(&feature.StoreSaveHook{FailOnCall: 2})
	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate); err == nil {
		t.Fatal("want injected child-save failure")
	}
	mgr.Store.ResetSaveHook()

	// A selection commit against the consumed revision is rejected with the
	// typed consumed error instead of mutating the draft the launch owns.
	bumped := feature.ReconcileReviewFeedbackDraft(parent, draft, reviewedComments())
	bumped.Revision = draft.Revision + 1
	var consumed *feature.ReviewFeedbackDraftConsumedError
	if err := mgr.Store.SaveReviewFeedbackDraft(parent.ID, bumped, draft.Revision); !errors.As(err, &consumed) {
		t.Fatalf("selection commit error = %v, want draft-consumed rejection", err)
	}
	kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kept == nil || kept.Revision != draft.Revision || kept.SnapshotID != draft.SnapshotID {
		t.Fatalf("draft after rejected commit = %+v, want untouched pinned revision %d", kept, draft.Revision)
	}

	// Roll-forward consumes the pinned revision; a fresh editing session can
	// then commit normally against the empty draft.
	if _, err := mgr.Store.ReconcilePendingChildCreations(); err != nil {
		t.Fatal(err)
	}
	if kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID); err != nil || kept != nil {
		t.Fatalf("draft after reconcile = %+v (err %v), want consumed", kept, err)
	}
	fresh := feature.ReconcileReviewFeedbackDraft(parent, nil, reviewedComments())
	if err := mgr.Store.SaveReviewFeedbackDraft(parent.ID, fresh, 0); err != nil {
		t.Fatalf("fresh draft commit after roll-forward error = %v", err)
	}
}

// TestChildCreationConsumesPinnedDraftWithinLockedTransition covers the
// post-intent-clear window: the pinned draft must already be consumed when
// CreateChildLocked returns with the intent cleared, not in a later second
// lock acquisition. A selection commit submitted after child creation must
// fail instead of advancing a stranded draft behind the active child.
func TestChildCreationConsumesPinnedDraftWithinLockedTransition(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)
	installLaunchFetchStub(t, launchReceiptFetch())

	gate := true
	result, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate)
	if err != nil {
		t.Fatalf("launch error = %v", err)
	}

	// The intent is cleared on the parent...
	settled, err := mgr.Store.Load(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.PendingChild != nil {
		t.Fatalf("settled parent PendingChild = %+v, want nil", settled.PendingChild)
	}
	// ...and the pinned draft is already gone at the same transition: no
	// interval exists where neither the intent nor the draft marks the
	// revision as consumed.
	if kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID); err != nil || kept != nil {
		t.Fatalf("draft after committed creation = %+v (err %v), want consumed within the locked transition", kept, err)
	}

	// The deterministic save-vs-cleanup interleaving: a selection commit
	// that observed the pre-launch revision and lands after the child exists
	// is rejected and must not strand a newer draft behind the active child.
	bumped := feature.ReconcileReviewFeedbackDraft(parent, draft, reviewedComments())
	bumped.Revision = draft.Revision + 1
	var conflict *feature.ReviewFeedbackRevisionConflictError
	if err := mgr.Store.SaveReviewFeedbackDraft(parent.ID, bumped, draft.Revision); !errors.As(err, &conflict) {
		t.Fatalf("post-intent-clear selection commit error = %v, want revision-conflict rejection", err)
	}
	if kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID); err != nil || kept != nil {
		t.Fatalf("draft after rejected commit = %+v (err %v), want none — the active child owns revision %d", kept, err, draft.Revision)
	}

	// The created child carries the consumed revision on its durable
	// receipt, so a repeated launch replays without a second creation.
	reloaded, err := mgr.Store.Load(result.Child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Parent == nil || reloaded.Parent.LaunchReceipt == nil || reloaded.Parent.LaunchReceipt.DraftRevision != draft.Revision {
		t.Fatalf("child launch receipt = %+v, want draft revision %d", reloaded.Parent.LaunchReceipt, draft.Revision)
	}
}

func TestStartupReconciliationClearsDraftConsumedByLaunchReceipt(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)
	installLaunchFetchStub(t, launchReceiptFetch())

	gate := true
	mgr.Store.SetSaveHook(&feature.StoreSaveHook{FailOnCall: 2})
	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate); err == nil {
		t.Fatal("want injected child-save failure")
	}
	mgr.Store.ResetSaveHook()

	reconciled, err := mgr.Store.ReconcilePendingChildCreations()
	if err != nil {
		t.Fatalf("ReconcilePendingChildCreations() error = %v", err)
	}
	if len(reconciled) != 1 || reconciled[0] != parent.ID {
		t.Fatalf("reconciled = %v, want [%s]", reconciled, parent.ID)
	}
	if kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID); err != nil || kept != nil {
		t.Fatalf("draft after reconciliation = %+v (err %v), want consumed", kept, err)
	}
	// The rolled-forward child carries the receipt, so ordinary launch replay
	// still returns the original counts after restart.
	children, err := mgr.Store.RelationshipChildren(parent.ID)
	if err != nil || children.Active == nil {
		t.Fatalf("relationship children = %+v (err %v), want the rebuilt child active", children, err)
	}
	receipt := children.Active.Parent.LaunchReceipt
	if receipt == nil || receipt.Changed != 1 || receipt.Omitted != 1 || receipt.Deferred != 1 {
		t.Fatalf("rebuilt child receipt = %+v, want 1/1/1 counts", receipt)
	}
}

// TestStartupReconciliationRetainsIntentWhenDraftCleanupFails covers the
// durability boundary where draft cleanup fails (or the process crashes)
// after the child is rolled forward: reconciliation must keep the durable
// pending intent, which is the only remaining marker that the receipt-pinned
// revision is consumed, and retry cleanup on the next scan. Clearing the
// intent first (the pre-fix ordering) stranded the consumed draft on disk
// behind the active child, letting SaveReviewFeedbackDraft acknowledge edits
// to it. The cleanup failure is injected by making the draft directory
// read-only so the pinned os.Remove fails deterministically.
func TestStartupReconciliationRetainsIntentWhenDraftCleanupFails(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)
	installLaunchFetchStub(t, launchReceiptFetch())

	gate := true
	mgr.Store.SetSaveHook(&feature.StoreSaveHook{FailOnCall: 2})
	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate); err == nil {
		t.Fatal("want injected child-save failure")
	}
	mgr.Store.ResetSaveHook()

	// Make the pinned-revision deletion fail: the draft file still loads (so
	// the failure lands at the remove step), but the read-only directory
	// rejects its removal.
	draftDir := filepath.Join(mgr.Store.BaseDir, parent.ID, "review-feedback")
	if err := os.Chmod(draftDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(draftDir, 0o755) })

	if _, err := mgr.Store.ReconcilePendingChildCreations(); err == nil {
		t.Fatal("want reconciliation error from the failed draft cleanup")
	}
	// The durable intent must survive the failed cleanup: it is the only
	// marker that the receipt-pinned revision is consumed.
	settled, err := mgr.Store.Load(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.PendingChild == nil || settled.PendingChild.LaunchReceipt == nil ||
		settled.PendingChild.LaunchReceipt.DraftRevision != draft.Revision {
		t.Fatalf("PendingChild after failed cleanup = %+v, want retained intent pinned to revision %d",
			settled.PendingChild, draft.Revision)
	}
	// The child was still rolled forward and owns the consumed revision.
	children, err := mgr.Store.RelationshipChildren(parent.ID)
	if err != nil || children.Active == nil {
		t.Fatalf("relationship children = %+v (err %v), want the rebuilt child active", children, err)
	}
	// A selection commit against the consumed revision is rejected by the
	// retained intent, never acknowledged against the stranded draft.
	bumped := feature.ReconcileReviewFeedbackDraft(parent, draft, reviewedComments())
	bumped.Revision = draft.Revision + 1
	var consumed *feature.ReviewFeedbackDraftConsumedError
	if err := mgr.Store.SaveReviewFeedbackDraft(parent.ID, bumped, draft.Revision); !errors.As(err, &consumed) {
		t.Fatalf("selection commit after failed cleanup error = %v, want draft-consumed rejection", err)
	}

	// Once the deletion can succeed, the next reconciliation pass completes
	// the roll-forward: intent cleared, pinned draft gone.
	if err := os.Chmod(draftDir, 0o755); err != nil {
		t.Fatal(err)
	}
	reconciled, err := mgr.Store.ReconcilePendingChildCreations()
	if err != nil {
		t.Fatalf("retry ReconcilePendingChildCreations() error = %v", err)
	}
	if len(reconciled) != 1 || reconciled[0] != parent.ID {
		t.Fatalf("reconciled = %v, want [%s]", reconciled, parent.ID)
	}
	if settled, err := mgr.Store.Load(parent.ID); err != nil || settled.PendingChild != nil {
		t.Fatalf("PendingChild after retry = %+v (err %v), want cleared", settled.PendingChild, err)
	}
	if kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID); err != nil || kept != nil {
		t.Fatalf("draft after retry = %+v (err %v), want consumed", kept, err)
	}
}

func TestLaunchReplayRejectsStaleReceiptWithoutMutation(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)
	fetchCalls := installCountingLaunchFetchStub(t, launchReceiptFetch())

	gate := true
	first, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate)
	if err != nil {
		t.Fatalf("first launch error = %v", err)
	}
	resolutionCalls := *fetchCalls

	// A different committed revision cannot claim the receipt.
	var conflict *feature.ReviewFeedbackRevisionConflictError
	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision+1, &gate); !errors.As(err, &conflict) {
		t.Fatalf("stale revision launch error = %v, want revision conflict", err)
	}
	// A mismatched gate cannot claim the receipt either.
	otherGate := false
	var activeChild *feature.ActiveChildExistsError
	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &otherGate); !errors.As(err, &activeChild) {
		t.Fatalf("mismatched gate launch error = %v, want active-child conflict", err)
	}
	if *fetchCalls != resolutionCalls {
		t.Fatalf("rejected launches resolved GitHub: fetch calls %d, want %d", *fetchCalls, resolutionCalls)
	}
	// Neither attempt mutated the child relationship.
	children, err := mgr.Store.RelationshipChildren(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if children.Active == nil || children.Active.ID != first.Child.ID || len(children.Closed) != 0 {
		t.Fatalf("relationship children = %+v, want only the original child", children)
	}
}

func TestLaunchReplayCleanupNeverDeletesNewerDraftRevision(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)
	installLaunchFetchStub(t, launchReceiptFetch())

	gate := true
	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate); err != nil {
		t.Fatalf("first launch error = %v", err)
	}
	// The completed launch consumed the draft: nothing remains after the
	// post-commit pinned-revision sweep.
	if kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID); err != nil || kept != nil {
		t.Fatalf("draft after launch = %+v (err %v), want consumed after durable creation", kept, err)
	}

	// A new editing session commits its own acknowledged selection after the
	// launch (a revision the receipt never pinned). Replaying the launch
	// receipt must never delete it.
	newer := feature.ReconcileReviewFeedbackDraft(parent, nil, reviewedComments())
	if err := mgr.Store.SaveReviewFeedbackDraft(parent.ID, newer, 0); err != nil {
		t.Fatal(err)
	}
	selection := map[feature.StableReviewFeedbackRef]bool{newer.Items[0].StableRef: false}
	if err := feature.ApplyReviewFeedbackSelection(newer, selection); err != nil {
		t.Fatal(err)
	}
	newer.Revision++
	if err := mgr.Store.SaveReviewFeedbackDraft(parent.ID, newer, 1); err != nil {
		t.Fatal(err)
	}

	replayed, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate)
	if err != nil {
		t.Fatalf("replay launch error = %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("replay = %+v, want replayed original launch", replayed)
	}
	kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID)
	if err != nil || kept == nil || kept.Revision != newer.Revision {
		t.Fatalf("draft after replay = %+v (err %v), want newer revision %d preserved", kept, err, newer.Revision)
	}
}

func TestLaunchAgainstUnrelatedActiveChildConflictsAndPreservesDraft(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)
	installLaunchFetchStub(t, launchReceiptFetch())

	// Fabricate an unrelated active (refactor) child: it must never claim a
	// review-feedback launch receipt.
	unrelated := &feature.Feature{
		ID:            "child-refactor-active",
		Slug:          "child-refactor-active",
		Status:        feature.StatusImplementing,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Parent:        &feature.ChildRelationship{ParentID: parent.ID, Kind: feature.ChildKindRefactor},
	}
	if err := mgr.Store.Save(unrelated); err != nil {
		t.Fatal(err)
	}

	var activeChild *feature.ActiveChildExistsError
	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, nil); !errors.As(err, &activeChild) {
		t.Fatalf("launch error = %v, want active-child conflict", err)
	}
	kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID)
	if err != nil || kept == nil || kept.Revision != draft.Revision {
		t.Fatalf("draft after conflict = %+v, want preserved at revision %d", kept, draft.Revision)
	}
	reloaded, err := mgr.Store.Load(unrelated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Parent.LaunchReceipt != nil {
		t.Fatalf("unrelated child gained a launch receipt: %+v", reloaded.Parent.LaunchReceipt)
	}
}

func TestLaunchCreationRejectsConcurrentSelectionCommit(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)

	// The creation lock re-verifies the committed draft revision: a receipt
	// pinned to a stale revision conflicts instead of launching from a
	// consumed snapshot.
	receipt := &feature.ReviewFeedbackLaunchReceipt{DraftRevision: draft.Revision + 1, Changed: 1}
	comment := feature.ReviewFeedbackComment{Repo: "api", ID: 12, Type: "review", Author: "bob", Body: "will be edited"}
	var conflict *feature.ReviewFeedbackRevisionConflictError
	_, err := mgr.CreateReviewFeedbackChild(parent.ID, feature.ReviewFeedbackChildSpec{
		Comments: []feature.ReviewFeedbackComment{comment},
		Receipt:  receipt,
	})
	if !errors.As(err, &conflict) {
		t.Fatalf("creation error = %v, want revision conflict", err)
	}
	if children, rerr := mgr.Store.RelationshipChildren(parent.ID); rerr != nil || children.Active != nil {
		t.Fatalf("relationship children = %+v (err %v), want none after conflict", children, rerr)
	}
	if reloaded, lerr := mgr.Store.Load(parent.ID); lerr != nil || reloaded.PendingChild != nil {
		t.Fatalf("parent after conflict = pending %+v (err %v), want none", reloaded, lerr)
	}

	// The matching revision stamps the receipt durably on the child.
	receipt.DraftRevision = draft.Revision
	child, err := mgr.CreateReviewFeedbackChild(parent.ID, feature.ReviewFeedbackChildSpec{
		Comments: []feature.ReviewFeedbackComment{comment},
		Receipt:  receipt,
	})
	if err != nil {
		t.Fatalf("creation with matching receipt error = %v", err)
	}
	reloadedChild, err := mgr.Store.Load(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	stamped := reloadedChild.Parent.LaunchReceipt
	if stamped == nil || stamped.DraftRevision != draft.Revision || stamped.Changed != 1 {
		t.Fatalf("stamped receipt = %+v, want revision %d and counts", stamped, draft.Revision)
	}
}

func TestLaunchZeroLaunchableLeavesNoReceipt(t *testing.T) {
	mgr, parent, draft := launchReceiptManager(t)
	// Every selected comment is deleted before launch.
	installLaunchFetchStub(t, map[string][]gitadapter.ReviewComment{"api": {}, "web": {}})

	var zero *feature.ReviewFeedbackZeroLaunchableSelectionError
	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, nil); !errors.As(err, &zero) {
		t.Fatalf("launch error = %v, want zero-launchable-selection", err)
	}
	if kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID); err != nil || kept == nil || kept.Revision != draft.Revision {
		t.Fatalf("draft after zero launch = %+v, want preserved at revision %d", kept, draft.Revision)
	}
	children, err := mgr.Store.RelationshipChildren(parent.ID)
	if err != nil || children.Active != nil || len(children.Closed) != 0 {
		t.Fatalf("relationship children = %+v (err %v), want no child and no receipt", children, err)
	}
}
