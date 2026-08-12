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
	"strings"
	"testing"

	gitadapter "github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
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
	// the draft was already consumed under the creation lock, so replay only
	// has to finish the roll-forward.
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
	// The completed launch consumed the draft inside the creation
	// transaction: nothing remains for the post-create sweep to remove.
	if kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID); err != nil || kept != nil {
		t.Fatalf("draft after launch = %+v (err %v), want consumed under the creation lock", kept, err)
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
