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

// Real-git coverage of delete-ref transaction entries: an entry whose
// previous top is a deleted ref switches the parent worktree onto the new
// top branch before the ref transaction and hard-resets it to the top
// candidate after; a later repository's failure rolls the first back in one
// transaction that recreates the deleted ref at its anchor and rewinds the
// rewritten refs; a failed pre-transaction switch parks before any ref
// moves, mirroring the branch-mismatch park.

package orchestrator

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// The three-layer stack shape every delete-ref fixture shares: layer 1 is
// dropped by the transaction — its branch is the parent worktree's
// checked-out previous top — layers 2 and 3 are rewritten to new candidates,
// and layer 3 becomes the new top.
var (
	deleteRefDroppedBranch = "stack/drop-dropped"
	deleteRefKeptBranch    = "stack/drop-kept"
	deleteRefTopBranch     = "stack/drop-top"
)

// deleteRefFixture builds real repositories with a three-layer parent stack
// and a refactor child whose hand-built journal deletes the dropped layer
// and rewrites the two layers above it. No production preparation path
// records delete refs yet, so the journal is constructed directly — the
// durable shape transaction_delete_ref_test.go pins.
type deleteRefFixture struct {
	t          *testing.T
	repoDirs   []string
	tipDropped []string
	tipKept    []string
	tipTop     []string
	candKept   []string
	candTop    []string
	store      *feature.Store
	mgr        *feature.Manager
	wm         *git.WorktreeManager
	parent     *feature.Feature
	child      *feature.Feature
}

func newDeleteRefFixture(t *testing.T, numRepos int) *deleteRefFixture {
	t.Helper()
	repoDirs := make([]string, numRepos)
	tipDropped := make([]string, numRepos)
	tipKept := make([]string, numRepos)
	tipTop := make([]string, numRepos)
	candKept := make([]string, numRepos)
	candTop := make([]string, numRepos)

	parentRepos := make([]feature.FeatureRepo, 0, numRepos)
	childRepos := make([]feature.FeatureRepo, 0, numRepos)
	bases := make([]feature.ChildRepoBase, 0, numRepos)

	for i := 0; i < numRepos; i++ {
		repoDir := testutil.InitGitRepo(t)
		txGit(t, repoDir, "checkout", "-b", deleteRefDroppedBranch)
		tipDropped[i] = testutil.CommitFile(t, repoDir, "dropped.txt", "dropped layer\n", "dropped layer")
		txGit(t, repoDir, "checkout", "-b", deleteRefKeptBranch)
		tipKept[i] = testutil.CommitFile(t, repoDir, "kept.txt", "kept layer\n", "kept layer")
		txGit(t, repoDir, "checkout", "-b", deleteRefTopBranch)
		tipTop[i] = testutil.CommitFile(t, repoDir, "top.txt", "top layer\n", "top layer")
		// The parent worktree sits on the dropped layer's branch: the
		// previous top the transaction records and deletes — deleting it in
		// place would strand the checkout, which is why apply must move the
		// worktree first.
		txGit(t, repoDir, "checkout", deleteRefDroppedBranch)
		// Candidates staged without moving any ref: new commits on each
		// kept layer's tree.
		candKept[i] = txGit(t, repoDir, "commit-tree", deleteRefKeptBranch+"^{tree}", "-p", tipKept[i], "-m", "drop candidate: kept layer")
		candTop[i] = txGit(t, repoDir, "commit-tree", deleteRefTopBranch+"^{tree}", "-p", tipTop[i], "-m", "drop candidate: top layer")

		repoName := "repo" + string(rune('A'+i))
		publishable := true
		repoDirs[i] = repoDir
		parentRepos = append(parentRepos, feature.FeatureRepo{
			Name:         repoName,
			Path:         repoDir,
			WorktreePath: repoDir,
			Branch:       deleteRefDroppedBranch,
			BaseBranch:   "main",
			Publishable:  &publishable,
		})
		childRepos = append(childRepos, feature.FeatureRepo{
			Name:       repoName,
			Path:       repoDir,
			Branch:     "feature/drop-child",
			BaseBranch: deleteRefDroppedBranch,
		})
		bases = append(bases, feature.ChildRepoBase{Repo: repoName, SHA: tipDropped[i], ParentBranch: deleteRefDroppedBranch})
	}

	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	parent := &feature.Feature{
		ID:           "parent-drop",
		Name:         "Parent Drop",
		Slug:         "parent-drop",
		Status:       feature.StatusPublished,
		CurrentPhase: feature.PhasePublish,
		Created:      time.Now(),
		ActiveRun:    1,
		RunCount:     1,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Repos:        parentRepos,
		Stack: []feature.StackLayer{
			{Position: 1, Title: "Dropped", Slug: "dropped", Phases: []int{1}, Branch: deleteRefDroppedBranch},
			{Position: 2, Title: "Kept", Slug: "kept", Phases: []int{2}, Branch: deleteRefKeptBranch},
			{Position: 3, Title: "Top", Slug: "top", Phases: []int{3}, Branch: deleteRefTopBranch},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	child := &feature.Feature{
		ID:           "child-drop",
		Name:         "Child Drop",
		Slug:         "child-drop",
		Status:       feature.StatusReviewPassed,
		CurrentPhase: feature.PhaseFinalReview,
		Pipeline:     feature.PipelineMedium,
		Created:      time.Now(),
		ActiveRun:    1,
		RunCount:     1,
		Repos:        childRepos,
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID,
			Kind:     feature.ChildKindRefactor,
			Bases:    bases,
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	parent.RepoStates = make(map[string]*feature.RepoState, numRepos)
	child.RepoStates = make(map[string]*feature.RepoState, numRepos)
	for _, pr := range parentRepos {
		parent.RepoStates[pr.Name] = &feature.RepoState{Touched: true}
		child.RepoStates[pr.Name] = &feature.RepoState{Touched: true}
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}

	wm := git.NewWorktreeManager(t.TempDir())
	mgr := feature.NewManager(store, config.NewDefault())
	mgr.Worktrees = wm
	return &deleteRefFixture{
		t: t, repoDirs: repoDirs,
		tipDropped: tipDropped, tipKept: tipKept, tipTop: tipTop,
		candKept: candKept, candTop: candTop,
		store: store, mgr: mgr, wm: wm, parent: parent, child: child,
	}
}

func (fx *deleteRefFixture) reload() (*feature.Feature, *feature.Feature) {
	fx.t.Helper()
	parent, err := fx.store.Load(fx.parent.ID)
	if err != nil {
		fx.t.Fatalf("reload parent: %v", err)
	}
	child, err := fx.store.Load(fx.child.ID)
	if err != nil {
		fx.t.Fatalf("reload child: %v", err)
	}
	return parent, child
}

func (fx *deleteRefFixture) refSHA(repoIdx int, branch string) string {
	return txGit(fx.t, fx.repoDirs[repoIdx], "rev-parse", "refs/heads/"+branch)
}

func (fx *deleteRefFixture) branchAbsent(repoIdx int, branch string) bool {
	return txGit(fx.t, fx.repoDirs[repoIdx], "branch", "--list", branch) == ""
}

func (fx *deleteRefFixture) worktreeBranch(repoIdx int) string {
	return txGit(fx.t, fx.repoDirs[repoIdx], "branch", "--show-current")
}

// journal builds the delete-ref transaction journal: one entry per
// repository deleting the dropped layer's ref at its anchor, rewriting the
// two layers above it, and recording the dropped branch as the previous top.
func (fx *deleteRefFixture) journal() *feature.TransactionJournal {
	entries := make([]feature.RepoTransactionEntry, len(fx.repoDirs))
	for i := range fx.repoDirs {
		entries[i] = feature.RepoTransactionEntry{
			Repo: fx.parent.Repos[i].Name,
			Refs: []feature.RepoTransactionRef{
				{Branch: deleteRefDroppedBranch, Layer: 1, Kind: feature.RepoRefKindDelete, AnchorSHA: fx.tipDropped[i]},
				{Branch: deleteRefKeptBranch, Layer: 2, AnchorSHA: fx.tipKept[i], CandidateSHA: fx.candKept[i]},
				{Branch: deleteRefTopBranch, Layer: 3, AnchorSHA: fx.tipTop[i], CandidateSHA: fx.candTop[i]},
			},
			ChildHeadSHA: fx.tipDropped[i],
			PrepState:    feature.RepoPrepPrepared,
			PreviousTop: &feature.RepoTransactionPreviousTop{
				Branch: deleteRefDroppedBranch,
				Layer:  1,
				TipSHA: fx.tipDropped[i],
			},
		}
	}
	return &feature.TransactionJournal{Phase: feature.TransactionPhasePrepared, Entries: entries}
}

// deleteOrderRecordingWorktrees wraps the real worktree manager and records
// the ordered sequence of branch switches, hard resets, and multi-ref
// transactions, so tests can pin the switch-before-transaction and
// reset-after-transaction ordering of delete-previous-top entries.
type deleteOrderRecordingWorktrees struct {
	*git.WorktreeManager
	events []string
	calls  []recordedRefTransaction
}

func (w *deleteOrderRecordingWorktrees) SwitchBranch(worktreePath, branch string) error {
	w.events = append(w.events, "switch "+worktreePath+" "+branch)
	return w.WorktreeManager.SwitchBranch(worktreePath, branch)
}

func (w *deleteOrderRecordingWorktrees) ResetToCommit(worktreePath, commitSHA string) error {
	w.events = append(w.events, "reset "+worktreePath+" "+commitSHA)
	return w.WorktreeManager.ResetToCommit(worktreePath, commitSHA)
}

func (w *deleteOrderRecordingWorktrees) UpdateRefsTransaction(repoPath string, updates []git.RefUpdate) error {
	w.events = append(w.events, "txn "+repoPath)
	w.calls = append(w.calls, recordedRefTransaction{
		repoPath: repoPath,
		updates:  append([]git.RefUpdate(nil), updates...),
	})
	return w.WorktreeManager.UpdateRefsTransaction(repoPath, updates)
}

// TestDeleteRefApplySwitchesBeforeTransactionAndResetsAfter proves apply
// issues exactly one ref transaction per repository — the delete line
// expecting the dropped layer's anchor plus the two rewrite moves, and no
// previous-top verify line, because the delete line on the same ref already
// compares against the anchor — switches the worktree onto the new top
// branch before the transaction, hard-resets it to the top candidate after,
// and updates the repository record's branch.
func TestDeleteRefApplySwitchesBeforeTransactionAndResetsAfter(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newDeleteRefFixture(t, 2)
	parent, child := fx.reload()
	journal := fx.journal()

	recording := &deleteOrderRecordingWorktrees{WorktreeManager: fx.wm}
	applyO := New(Deps{Lifecycle: fx.mgr, Store: fx.store, Worktrees: recording}, Hooks{})
	if err := applyO.applyTransactionCandidates(child, parent, journal); err != nil {
		t.Fatalf("applyTransactionCandidates() error = %v", err)
	}

	if len(recording.calls) != len(fx.repoDirs) {
		t.Fatalf("ref transactions = %d, want exactly one per repository (%d)", len(recording.calls), len(fx.repoDirs))
	}
	for i, call := range recording.calls {
		if call.repoPath != fx.repoDirs[i] {
			t.Fatalf("transaction %d ran against %s, want repo %s", i, call.repoPath, fx.repoDirs[i])
		}
		want := []git.RefUpdate{
			{Ref: "refs/heads/" + deleteRefDroppedBranch, OldSHA: fx.tipDropped[i]},
			{Ref: "refs/heads/" + deleteRefKeptBranch, OldSHA: fx.tipKept[i], NewSHA: fx.candKept[i]},
			{Ref: "refs/heads/" + deleteRefTopBranch, OldSHA: fx.tipTop[i], NewSHA: fx.candTop[i]},
		}
		if !slices.Equal(call.updates, want) {
			t.Fatalf("repo %d: transaction updates =\n%+v\nwant\n%+v", i, call.updates, want)
		}
	}

	// Per repository, in order: the switch onto the new top branch precedes
	// the transaction and the hard reset to the top candidate follows it.
	wantEvents := make([]string, 0, 3*len(fx.repoDirs))
	for i := range fx.repoDirs {
		wantEvents = append(wantEvents,
			"switch "+fx.repoDirs[i]+" "+deleteRefTopBranch,
			"txn "+fx.repoDirs[i],
			"reset "+fx.repoDirs[i]+" "+fx.candTop[i],
		)
	}
	if !slices.Equal(recording.events, wantEvents) {
		t.Fatalf("worktree event order =\n%v\nwant\n%v", recording.events, wantEvents)
	}

	for i := range fx.repoDirs {
		if !fx.branchAbsent(i, deleteRefDroppedBranch) {
			t.Fatalf("repo %d: dropped ref still present after apply", i)
		}
		if got := fx.refSHA(i, deleteRefKeptBranch); got != fx.candKept[i] {
			t.Fatalf("repo %d: kept ref = %s, want candidate %s", i, got, fx.candKept[i])
		}
		if got := fx.refSHA(i, deleteRefTopBranch); got != fx.candTop[i] {
			t.Fatalf("repo %d: top ref = %s, want candidate %s", i, got, fx.candTop[i])
		}
		if branch := fx.worktreeBranch(i); branch != deleteRefTopBranch {
			t.Fatalf("repo %d: worktree branch = %q, want new top %q", i, branch, deleteRefTopBranch)
		}
		if head := txGit(t, fx.repoDirs[i], "rev-parse", "HEAD"); head != fx.candTop[i] {
			t.Fatalf("repo %d: worktree HEAD = %s, want top candidate %s", i, head, fx.candTop[i])
		}
	}
	freshParent, stored := fx.reload()
	for i := range freshParent.Repos {
		if freshParent.Repos[i].Branch != deleteRefTopBranch {
			t.Fatalf("repo %d: record branch = %q, want new top %q", i, freshParent.Repos[i].Branch, deleteRefTopBranch)
		}
	}
	if stored.Parent.Transaction.Phase != feature.TransactionPhaseApplied {
		t.Fatalf("journal phase = %s, want applied", stored.Parent.Transaction.Phase)
	}
}

// TestDeleteRefSecondRepoFailureRollsBackFirstInOneTransaction proves a
// failure on the second repository compensates the first in a single ref
// transaction that recreates the deleted ref at its anchor — expecting
// absence — and rewinds the rewritten refs to their anchors, then switches
// the worktree back to the recreated dropped branch and restores the
// repository record.
func TestDeleteRefSecondRepoFailureRollsBackFirstInOneTransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newDeleteRefFixture(t, 2)
	parent, child := fx.reload()
	journal := fx.journal()

	// Move the second repository's kept ref externally so its pre-apply
	// anchor verification refuses the repository after the first applied.
	txGit(t, fx.repoDirs[1], "update-ref", "refs/heads/"+deleteRefKeptBranch, fx.tipTop[1])

	recording := &deleteOrderRecordingWorktrees{WorktreeManager: fx.wm}
	applyO := New(Deps{Lifecycle: fx.mgr, Store: fx.store, Worktrees: recording}, Hooks{})
	if err := applyO.applyTransactionCandidates(child, parent, journal); err != nil {
		t.Fatalf("applyTransactionCandidates() error = %v, want nil with attention", err)
	}

	// The first repository applied and was then rolled back in one
	// transaction; the second repository never reached a transaction.
	if len(recording.calls) != 2 {
		t.Fatalf("ref transactions = %d, want the first repo's apply and rollback only (2)", len(recording.calls))
	}
	for i, call := range recording.calls {
		if call.repoPath != fx.repoDirs[0] {
			t.Fatalf("transaction %d ran against %s, want the first repo %s", i, call.repoPath, fx.repoDirs[0])
		}
	}
	rollback := recording.calls[1]
	want := []git.RefUpdate{
		{Ref: "refs/heads/" + deleteRefDroppedBranch, NewSHA: fx.tipDropped[0]},
		{Ref: "refs/heads/" + deleteRefKeptBranch, OldSHA: fx.candKept[0], NewSHA: fx.tipKept[0]},
		{Ref: "refs/heads/" + deleteRefTopBranch, OldSHA: fx.candTop[0], NewSHA: fx.tipTop[0]},
	}
	if !slices.Equal(rollback.updates, want) {
		t.Fatalf("rollback updates =\n%+v\nwant\n%+v", rollback.updates, want)
	}

	// The first repository is byte-identical to before launch and its
	// worktree is back on the recreated dropped branch.
	if got := fx.refSHA(0, deleteRefDroppedBranch); got != fx.tipDropped[0] {
		t.Fatalf("repo 0: dropped ref = %s, want recreated at anchor %s", got, fx.tipDropped[0])
	}
	if got := fx.refSHA(0, deleteRefKeptBranch); got != fx.tipKept[0] {
		t.Fatalf("repo 0: kept ref = %s, want anchor %s", got, fx.tipKept[0])
	}
	if got := fx.refSHA(0, deleteRefTopBranch); got != fx.tipTop[0] {
		t.Fatalf("repo 0: top ref = %s, want anchor %s", got, fx.tipTop[0])
	}
	if branch := fx.worktreeBranch(0); branch != deleteRefDroppedBranch {
		t.Fatalf("repo 0: worktree branch = %q, want the recreated dropped branch %q", branch, deleteRefDroppedBranch)
	}
	if head := txGit(t, fx.repoDirs[0], "rev-parse", "HEAD"); head != fx.tipDropped[0] {
		t.Fatalf("repo 0: worktree HEAD = %s, want the dropped anchor %s", head, fx.tipDropped[0])
	}
	freshParent, stored := fx.reload()
	if freshParent.Repos[0].Branch != deleteRefDroppedBranch {
		t.Fatalf("repo 0: record branch = %q, want the dropped branch %q restored", freshParent.Repos[0].Branch, deleteRefDroppedBranch)
	}

	tx := stored.Parent.Transaction
	if tx.Phase != feature.TransactionPhaseAttention || tx.Attention == nil || tx.Attention.Code != errcat.IntegrationRefRace {
		t.Fatalf("transaction = %+v, want integration_ref_race attention", tx)
	}
	if got := tx.Entries[0].ApplyState; got != feature.RepoApplyRolledBack {
		t.Fatalf("repo 0: apply state = %q, want rolled_back", got)
	}
	if got := tx.Entries[1].ApplyState; got != feature.RepoApplyAttention {
		t.Fatalf("repo 1: apply state = %q, want attention", got)
	}
}

// TestDeleteRefPreTransactionSwitchFailureParksWithoutRefChanges pins the
// pre-transaction switch failure semantics: with no earlier applied
// repository, the switch failure parks at the worktree-sync attention —
// mirroring the branch-mismatch park — and no ref of the repository moves.
func TestDeleteRefPreTransactionSwitchFailureParksWithoutRefChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newDeleteRefFixture(t, 1)
	parent, child := fx.reload()
	journal := fx.journal()

	switchWT := &failingSwitchWorktrees{WorktreeManager: fx.wm, failRepoDir: fx.repoDirs[0]}
	applyO := New(Deps{Lifecycle: fx.mgr, Store: fx.store, Worktrees: switchWT}, Hooks{})
	if err := applyO.applyTransactionCandidates(child, parent, journal); err != nil {
		t.Fatalf("applyTransactionCandidates() error = %v, want nil with attention", err)
	}

	if got := fx.refSHA(0, deleteRefDroppedBranch); got != fx.tipDropped[0] {
		t.Fatalf("dropped ref = %s, want unchanged %s", got, fx.tipDropped[0])
	}
	if got := fx.refSHA(0, deleteRefKeptBranch); got != fx.tipKept[0] {
		t.Fatalf("kept ref = %s, want unchanged %s", got, fx.tipKept[0])
	}
	if got := fx.refSHA(0, deleteRefTopBranch); got != fx.tipTop[0] {
		t.Fatalf("top ref = %s, want unchanged %s", got, fx.tipTop[0])
	}
	if branch := fx.worktreeBranch(0); branch != deleteRefDroppedBranch {
		t.Fatalf("worktree branch = %q, want still on the dropped branch %q", branch, deleteRefDroppedBranch)
	}

	_, stored := fx.reload()
	tx := stored.Parent.Transaction
	if tx.Phase != feature.TransactionPhaseAttention || tx.Attention == nil ||
		tx.Attention.Code != errcat.IntegrationWorktreeSyncFailed {
		t.Fatalf("transaction = %+v, want integration_worktree_sync_failed attention", tx)
	}
	if got := tx.Entries[0].ApplyState; got != feature.RepoApplyAttention {
		t.Fatalf("apply state = %q, want attention", got)
	}
}
