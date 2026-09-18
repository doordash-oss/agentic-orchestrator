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

// Real-git coverage of delete-ref transaction journals' crash windows: a
// crash between the multi-ref ref transaction (deleted ref absent, rewritten
// refs at their candidates) and the journal's apply persistence is finished
// by the startup scan with the dropped layer's stack entry marked merged and
// its tip cleared; a deleted ref observed at a foreign SHA parks the
// ref-race attention without touching any ref; and discarding an applied
// entry recreates the deleted ref at its anchor, leaving the parent's
// commits byte-identical to before launch.

package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// dropLayerBranch names the parent's layer branch for one stack position.
func dropLayerBranch(pos int) string {
	return fmt.Sprintf("stack/drop-%d", pos)
}

// dropLayerFixture builds one real repository with a three-layer parent
// stack — layer 1 dropped by the transaction, layers 2 and 3 rewritten to
// staged candidates — the parent worktree checked out on the dropped layer's
// branch (the transaction's recorded previous top), and a refactor child
// whose hand-built delete-ref journal can be persisted in any phase. No
// production preparation path records delete refs yet, so the journal is
// constructed directly — the durable shape the feature-package round-trip
// test pins.
type dropLayerFixture struct {
	t          *testing.T
	repoDir    string
	childWT    string
	childBr    string
	tipDropped string
	tipKept    string
	tipTop     string
	candKept   string
	candTop    string
	store      *feature.Store
	mgr        *feature.Manager
	wm         *git.WorktreeManager
	parent     *feature.Feature
	child      *feature.Feature
}

func newDropLayerFixture(t *testing.T) *dropLayerFixture {
	t.Helper()
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")

	repoDir := testutil.InitGitRepo(t)
	runGit(t, repoDir, "checkout", "-b", dropLayerBranch(1))
	tipDropped := testutil.CommitFile(t, repoDir, "dropped.txt", "dropped layer\n", "dropped layer")
	runGit(t, repoDir, "checkout", "-b", dropLayerBranch(2))
	tipKept := testutil.CommitFile(t, repoDir, "kept.txt", "kept layer\n", "kept layer")
	runGit(t, repoDir, "checkout", "-b", dropLayerBranch(3))
	tipTop := testutil.CommitFile(t, repoDir, "top.txt", "top layer\n", "top layer")
	// The parent worktree sits on the dropped layer's branch: the previous
	// top the transaction records and deletes.
	runGit(t, repoDir, "checkout", dropLayerBranch(1))
	// Candidates staged without moving any ref.
	candKept := runGit(t, repoDir, "commit-tree", dropLayerBranch(2)+"^{tree}", "-p", tipKept, "-m", "drop candidate: kept layer")
	candTop := runGit(t, repoDir, "commit-tree", dropLayerBranch(3)+"^{tree}", "-p", tipTop, "-m", "drop candidate: top layer")

	childBr := "feature/drop-recovery-child"
	childWT := filepath.Join(tmp, "child-wt")
	runGit(t, repoDir, "worktree", "add", "-b", childBr, childWT, tipDropped)

	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: repoDir}
	store := feature.NewStore(stateDir)
	wm := git.NewWorktreeManager(filepath.Join(tmp, "worktrees"))
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = wm

	publishable := true
	parent := &feature.Feature{
		ID:            "drop-parent",
		Name:          "Drop Parent",
		Slug:          "drop-parent",
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now(),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{{
			Name:         "repo-a",
			Path:         repoDir,
			WorktreePath: repoDir,
			Branch:       dropLayerBranch(1),
			BaseBranch:   "main",
			Publishable:  &publishable,
		}},
		RepoStates:  map[string]*feature.RepoState{"repo-a": {Touched: true}},
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	}
	parent.Stack = make([]feature.StackLayer, 0, 3)
	for pos := 1; pos <= 3; pos++ {
		tip := ""
		switch pos {
		case 1:
			tip = tipDropped
		case 2:
			tip = tipKept
		case 3:
			tip = tipTop
		}
		parent.Stack = append(parent.Stack, feature.StackLayer{
			Position: pos,
			Title:    fmt.Sprintf("Layer %d", pos),
			Slug:     fmt.Sprintf("layer-%d", pos),
			Phases:   []int{pos},
			Branch:   dropLayerBranch(pos),
			Repos: map[string]feature.StackRepoEntry{"repo-a": {
				TipSHA:        tip,
				LastPushedSHA: tip,
				PRURL:         fmt.Sprintf("https://github.com/example/repo-a/pull/%d", pos),
				PRState:       feature.StackPRStateOpen,
			}},
		})
	}
	child := &feature.Feature{
		ID:            "drop-child",
		Name:          "Drop Child",
		Slug:          "drop-child",
		Status:        feature.StatusReviewPassed,
		CurrentPhase:  feature.PhaseFinalReview,
		Pipeline:      feature.PipelineMedium,
		Created:       time.Now(),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{{
			Name:         "repo-a",
			Path:         repoDir,
			WorktreePath: childWT,
			Branch:       childBr,
			BaseBranch:   dropLayerBranch(1),
		}},
		RepoStates: map[string]*feature.RepoState{"repo-a": {Touched: true}},
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID,
			Kind:     feature.ChildKindRefactor,
			Bases:    []feature.ChildRepoBase{{Repo: "repo-a", SHA: tipDropped, ParentBranch: dropLayerBranch(1)}},
		},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}

	return &dropLayerFixture{
		t: t, repoDir: repoDir, childWT: childWT, childBr: childBr,
		tipDropped: tipDropped, tipKept: tipKept, tipTop: tipTop,
		candKept: candKept, candTop: candTop,
		store: store, mgr: mgr, wm: wm, parent: parent, child: child,
	}
}

// journal builds the delete-ref transaction journal: the dropped layer's ref
// deleted at its anchor, the two layers above it rewritten to their
// candidates, the dropped branch recorded as the previous top, and the
// closure remap for the rewritten layers' tips.
func (fx *dropLayerFixture) journal(phase feature.TransactionPhase, applyState feature.RepoApplyState) *feature.TransactionJournal {
	return &feature.TransactionJournal{
		Phase: phase,
		Entries: []feature.RepoTransactionEntry{{
			Repo: "repo-a",
			Refs: []feature.RepoTransactionRef{
				{Branch: dropLayerBranch(1), Layer: 1, Kind: feature.RepoRefKindDelete, AnchorSHA: fx.tipDropped},
				{Branch: dropLayerBranch(2), Layer: 2, AnchorSHA: fx.tipKept, CandidateSHA: fx.candKept},
				{Branch: dropLayerBranch(3), Layer: 3, AnchorSHA: fx.tipTop, CandidateSHA: fx.candTop},
			},
			ChildHeadSHA: fx.tipDropped,
			PrepState:    feature.RepoPrepPrepared,
			ApplyState:   applyState,
			PreviousTop: &feature.RepoTransactionPreviousTop{
				Branch: dropLayerBranch(1),
				Layer:  1,
				TipSHA: fx.tipDropped,
			},
			Remap: feature.RestackRemap{Tips: map[int]string{
				2: fx.candKept,
				3: fx.candTop,
			}},
		}},
	}
}

func (fx *dropLayerFixture) persistJournal(journal *feature.TransactionJournal) {
	fx.t.Helper()
	if err := fx.store.Modify(fx.child.ID, func(f *feature.Feature) error {
		f.Parent.Transaction = journal
		return nil
	}); err != nil {
		fx.t.Fatalf("persist journal: %v", err)
	}
}

// branchAbsent reports whether the named branch's ref no longer exists.
func (fx *dropLayerFixture) branchAbsent(branch string) bool {
	return runGit(fx.t, fx.repoDir, "branch", "--list", branch) == ""
}

// TestDropLayerJournalCrashRecovery drives a delete-ref transaction through
// the real integration path with a crash injected between the ref
// transaction and the journal's apply persistence, then proves the startup
// recovery scan finishes the closure: the dropped layer's stack entry is
// marked merged with its tip and last-pushed SHA cleared (keeping the
// pull-request URL), the rewritten layers' tips are remapped, and the
// worktree sits on the new top branch at its candidate.
func TestDropLayerJournalCrashRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real git repository through the transaction boundary")
	}
	fx := newDropLayerFixture(t)
	fx.persistJournal(fx.journal(feature.TransactionPhaseApplying, ""))

	crashing := &crashOnRefsUpdate{WorktreeManager: fx.wm}
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Worktrees: crashing,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})

	crashing.crash.Store(true)
	crashed := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				crashed = true
			}
		}()
		if err := orch.RunChildIntegration(fx.child.ID); err != nil {
			t.Fatalf("RunChildIntegration before the crash: %v", err)
		}
	}()
	if !crashed {
		t.Fatal("the injected crash did not fire between the ref transaction and journal persistence")
	}
	crashing.crash.Store(false)

	// The durable journal is still in the applying phase with a never-applied
	// entry, while every ref already sits where the transaction put it: the
	// deleted ref absent, the rewrites at their candidates, and the worktree
	// on the new top branch after the pre-transaction switch.
	childRec, err := fx.store.Load(fx.child.ID)
	if err != nil {
		t.Fatalf("load child after the crash: %v", err)
	}
	tx := childRec.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseApplying {
		t.Fatalf("journal phase after the crash = %+v, want applying", tx)
	}
	if got := tx.Entries[0].ApplyState; got != "" {
		t.Fatalf("entry apply state after the crash = %q, want empty (crash before apply persistence)", got)
	}
	if !fx.branchAbsent(dropLayerBranch(1)) {
		t.Fatal("dropped ref still present after the crash")
	}
	if got := refSHA(t, fx.repoDir, dropLayerBranch(2)); got != fx.candKept {
		t.Fatalf("kept ref after the crash = %s, want candidate %s", got, fx.candKept)
	}
	if got := refSHA(t, fx.repoDir, dropLayerBranch(3)); got != fx.candTop {
		t.Fatalf("top ref after the crash = %s, want candidate %s", got, fx.candTop)
	}

	// A fresh orchestrator runs the startup recovery scan over the same
	// store, manager, and worktrees.
	fresh := orchestrator.New(orchestrator.Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Worktrees: fx.wm,
		Recovery:  &fakeRecoveryNoop{},
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = fresh.Shutdown()
		fresh.WaitForCycles()
	})
	if _, err := fresh.ScanRecovery(context.Background()); err != nil {
		t.Fatalf("ScanRecovery after the crash: %v", err)
	}

	childRec, err = fx.store.Load(fx.child.ID)
	if err != nil {
		t.Fatalf("load child after recovery: %v", err)
	}
	if childRec.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child closure after recovery = %q, want Completed", childRec.Parent.CloseOutcome)
	}
	tx = childRec.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("journal phase after recovery = %+v, want merged", tx)
	}
	if tx.Attention != nil {
		t.Fatalf("journal attention after recovery = %+v, want none", tx.Attention)
	}

	parentRec, err := fx.store.Load(fx.parent.ID)
	if err != nil {
		t.Fatalf("load parent after recovery: %v", err)
	}
	// The dropped layer's stack entry is settled as merged: tip and
	// last-pushed SHA cleared, the pull-request URL kept as the durable
	// record, the pull-request state merged.
	dropped := parentRec.Stack[0].Repos["repo-a"]
	if dropped.PRState != feature.StackPRStateMerged {
		t.Fatalf("dropped layer PR state after recovery = %q, want merged", dropped.PRState)
	}
	if dropped.TipSHA != "" || dropped.LastPushedSHA != "" {
		t.Fatalf("dropped layer entry after recovery = %+v, want cleared tip and last-pushed SHA", dropped)
	}
	if dropped.PRURL != "https://github.com/example/repo-a/pull/1" {
		t.Fatalf("dropped layer pull-request URL after recovery = %q, want kept", dropped.PRURL)
	}
	// The rewritten layers' recorded tips carry the remap.
	if got := parentRec.Stack[1].Repos["repo-a"].TipSHA; got != fx.candKept {
		t.Fatalf("kept layer tip after recovery = %s, want remapped candidate %s", got, fx.candKept)
	}
	if got := parentRec.Stack[2].Repos["repo-a"].TipSHA; got != fx.candTop {
		t.Fatalf("top layer tip after recovery = %s, want remapped candidate %s", got, fx.candTop)
	}
	if got := parentRec.Repos[0].Branch; got != dropLayerBranch(3) {
		t.Fatalf("repository record branch after recovery = %q, want the new top %q", got, dropLayerBranch(3))
	}

	// Every ref stays where the transaction put it and the worktree is
	// synced to the new top at its candidate.
	if !fx.branchAbsent(dropLayerBranch(1)) {
		t.Fatal("dropped ref present after recovery")
	}
	if got := refSHA(t, fx.repoDir, dropLayerBranch(2)); got != fx.candKept {
		t.Fatalf("kept ref after recovery = %s, want candidate %s", got, fx.candKept)
	}
	if got := refSHA(t, fx.repoDir, dropLayerBranch(3)); got != fx.candTop {
		t.Fatalf("top ref after recovery = %s, want candidate %s", got, fx.candTop)
	}
	if branch := runGit(t, fx.repoDir, "branch", "--show-current"); branch != dropLayerBranch(3) {
		t.Fatalf("worktree branch after recovery = %q, want the new top %q", branch, dropLayerBranch(3))
	}
	if head, err := git.CurrentHeadSHA(fx.repoDir); err != nil {
		t.Fatalf("parent head after recovery: %v", err)
	} else if head != fx.candTop {
		t.Fatalf("parent HEAD after recovery = %s, want the top candidate %s", head, fx.candTop)
	}
	if status := runGit(t, fx.repoDir, "status", "--porcelain"); status != "" {
		t.Fatalf("parent worktree dirty after recovery: %q", status)
	}
}

// TestDropLayerRecoveryDeletedRefAtForeignSHAParksRefRace proves a deleted
// ref observed at a foreign SHA after the transaction parks the startup scan
// at the ref-race attention without touching any ref.
func TestDropLayerRecoveryDeletedRefAtForeignSHAParksRefRace(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real git repository through the recovery scan")
	}
	fx := newDropLayerFixture(t)

	// Simulate the crash-after-transaction state — the pre-transaction switch
	// landed, the deleted ref absent, the rewrites at their candidates — then
	// an external actor recreates the deleted ref at a foreign SHA.
	runGit(t, fx.repoDir, "checkout", dropLayerBranch(3))
	runGit(t, fx.repoDir, "update-ref", "-d", "refs/heads/"+dropLayerBranch(1))
	runGit(t, fx.repoDir, "update-ref", "refs/heads/"+dropLayerBranch(2), fx.candKept)
	runGit(t, fx.repoDir, "update-ref", "refs/heads/"+dropLayerBranch(3), fx.candTop)
	runGit(t, fx.repoDir, "update-ref", "refs/heads/"+dropLayerBranch(1), fx.tipTop)

	fx.persistJournal(fx.journal(feature.TransactionPhaseApplying, ""))

	fresh := orchestrator.New(orchestrator.Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Worktrees: fx.wm,
		Recovery:  &fakeRecoveryNoop{},
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = fresh.Shutdown()
		fresh.WaitForCycles()
	})
	if _, err := fresh.ScanRecovery(context.Background()); err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}

	childRec, err := fx.store.Load(fx.child.ID)
	if err != nil {
		t.Fatalf("load child after recovery: %v", err)
	}
	if childRec.Parent.CloseOutcome != "" {
		t.Fatalf("child closed (%q) although the scan parked at attention", childRec.Parent.CloseOutcome)
	}
	tx := childRec.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention ||
		tx.Attention == nil || tx.Attention.Code != errcat.IntegrationRefRace {
		t.Fatalf("transaction = %+v, want integration_ref_race attention", tx)
	}
	if !strings.Contains(tx.Attention.Diagnostics, dropLayerBranch(1)) {
		t.Fatalf("attention diagnostics = %q, want the deleted ref's branch %s named",
			tx.Attention.Diagnostics, dropLayerBranch(1))
	}

	// No ref was touched: the recreated deleted ref stays at its foreign SHA
	// and the rewrites stay at their candidates.
	if got := refSHA(t, fx.repoDir, dropLayerBranch(1)); got != fx.tipTop {
		t.Fatalf("deleted ref after recovery = %s, want preserved foreign %s", got, fx.tipTop)
	}
	if got := refSHA(t, fx.repoDir, dropLayerBranch(2)); got != fx.candKept {
		t.Fatalf("kept ref after recovery = %s, want candidate %s", got, fx.candKept)
	}
	if got := refSHA(t, fx.repoDir, dropLayerBranch(3)); got != fx.candTop {
		t.Fatalf("top ref after recovery = %s, want candidate %s", got, fx.candTop)
	}
}

// TestDropLayerDiscardRecreatesDeletedRefs proves discarding a child whose
// delete-ref transaction is applied recreates the deleted ref at its anchor
// and rewinds the rewritten refs, leaving the parent's commits
// byte-identical to before launch, the worktree back on the dropped branch,
// and the repository record restored.
func TestDropLayerDiscardRecreatesDeletedRefs(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real git repository through the discard path")
	}
	fx := newDropLayerFixture(t)

	// Simulate a fully applied delete transaction: the pre-transaction switch
	// landed and the worktree was reset to the top candidate, the deleted ref
	// is absent, the rewrites sit at their candidates, the record moved, the
	// journal is durably applied.
	runGit(t, fx.repoDir, "checkout", dropLayerBranch(3))
	runGit(t, fx.repoDir, "reset", "--hard", fx.candTop)
	runGit(t, fx.repoDir, "update-ref", "-d", "refs/heads/"+dropLayerBranch(1))
	runGit(t, fx.repoDir, "update-ref", "refs/heads/"+dropLayerBranch(2), fx.candKept)
	runGit(t, fx.repoDir, "update-ref", "refs/heads/"+dropLayerBranch(3), fx.candTop)
	if err := fx.store.Modify(fx.parent.ID, func(f *feature.Feature) error {
		f.Repos[0].Branch = dropLayerBranch(3)
		return nil
	}); err != nil {
		t.Fatalf("move parent branch record: %v", err)
	}
	fx.persistJournal(fx.journal(feature.TransactionPhaseApplied, feature.RepoApplyApplied))

	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Worktrees: fx.wm,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})
	if err := orch.DiscardChild(fx.child.ID); err != nil {
		t.Fatalf("DiscardChild() error = %v", err)
	}

	childRec, err := fx.store.Load(fx.child.ID)
	if err != nil {
		t.Fatalf("load child after discard: %v", err)
	}
	if childRec.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
		t.Fatalf("child close outcome = %q, want discarded", childRec.Parent.CloseOutcome)
	}
	if tx := childRec.Parent.Transaction; tx == nil || tx.Phase != feature.TransactionPhaseRolledBack {
		t.Fatalf("journal phase after discard = %+v, want rolled_back", tx)
	}

	// The deleted ref was recreated at its anchor and the rewrites rewound:
	// every parent commit is byte-identical to before launch.
	if got := refSHA(t, fx.repoDir, dropLayerBranch(1)); got != fx.tipDropped {
		t.Fatalf("dropped ref after discard = %s, want recreated at anchor %s", got, fx.tipDropped)
	}
	if got := refSHA(t, fx.repoDir, dropLayerBranch(2)); got != fx.tipKept {
		t.Fatalf("kept ref after discard = %s, want anchor %s", got, fx.tipKept)
	}
	if got := refSHA(t, fx.repoDir, dropLayerBranch(3)); got != fx.tipTop {
		t.Fatalf("top ref after discard = %s, want anchor %s", got, fx.tipTop)
	}
	if branch := runGit(t, fx.repoDir, "branch", "--show-current"); branch != dropLayerBranch(1) {
		t.Fatalf("worktree branch after discard = %q, want the recreated dropped branch %q", branch, dropLayerBranch(1))
	}
	if head, err := git.CurrentHeadSHA(fx.repoDir); err != nil {
		t.Fatalf("parent head after discard: %v", err)
	} else if head != fx.tipDropped {
		t.Fatalf("parent HEAD after discard = %s, want the dropped anchor %s", head, fx.tipDropped)
	}

	parentRec, err := fx.store.Load(fx.parent.ID)
	if err != nil {
		t.Fatalf("load parent after discard: %v", err)
	}
	if got := parentRec.Repos[0].Branch; got != dropLayerBranch(1) {
		t.Fatalf("repository record branch after discard = %q, want the dropped branch %q restored", got, dropLayerBranch(1))
	}
}
