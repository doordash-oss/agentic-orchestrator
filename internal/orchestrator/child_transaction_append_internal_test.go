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

// Real-git coverage of refactor-child append preparation: the child's own
// stack layers are mapped onto the parent's stack as appended layers —
// created refs named with the parent's numbering and the child's slugs, the
// child's recorded tips as candidates, the previous top recorded per
// repository, and the appended layer definitions recorded on the journal —
// without creating any ref or moving any parent ref.

package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// stackedAppendFixture builds a real parent/child pair whose parent carries a
// two-layer stack (the worktree checked out on layer 2) and whose refactor
// child carries a two-layer stack built on the parent tip: layer 1 has a
// commit in the first repository only, layer 2 a commit in the first
// repository, so the second repository exercises the no-commits rule (its
// candidates fall back to the tip below).
type stackedAppendFixture struct {
	t            *testing.T
	repoDirs     []string
	parentTips   []string
	childLayer1  []string // per-repo child layer-1 tips (empty commits fall back)
	store        *feature.Store
	mgr          *feature.Manager
	wm           *git.WorktreeManager
	parent       *feature.Feature
	child        *feature.Feature
	appended     []string // expected appended branch names, positions 3 and 4
	parentLayer2 string
}

func newStackedAppendFixture(t *testing.T, numRepos int) *stackedAppendFixture {
	t.Helper()
	repoDirs := make([]string, numRepos)
	parentTips := make([]string, numRepos)
	childLayer1 := make([]string, numRepos)
	parentLayer1 := "feature/append-parent-one"
	parentLayer2 := "feature/append-parent-two"
	childLayer1Branch := "feature/append-child-one"
	childLayer2Branch := "feature/append-child-two"

	parentRepos := make([]feature.FeatureRepo, 0, numRepos)
	childRepos := make([]feature.FeatureRepo, 0, numRepos)
	bases := make([]feature.ChildRepoBase, 0, numRepos)
	childWTs := make([]string, numRepos)

	for i := 0; i < numRepos; i++ {
		repoDir := testutil.InitGitRepo(t)
		repoName := "repo" + string(rune('A'+i))
		txGit(t, repoDir, "checkout", "-b", parentLayer1)
		testutil.CommitFile(t, repoDir, "p1.txt", "parent layer 1\n", "parent layer 1")
		txGit(t, repoDir, "checkout", "-b", parentLayer2)
		testutil.CommitFile(t, repoDir, "p2.txt", "parent layer 2\n", "parent layer 2")
		parentTip := txGit(t, repoDir, "rev-parse", "HEAD")

		childWT := t.TempDir() + "/child-wt-" + string(rune('A'+i))
		txGit(t, repoDir, "worktree", "add", "-b", childLayer1Branch, childWT, parentTip)
		layer1Tip := ""
		if i == 0 {
			// The child's layer 1 has a commit only in the first repository.
			testutil.CommitFile(t, childWT, "c1.txt", "child layer 1\n", "child layer 1")
			layer1Tip = txGit(t, childWT, "rev-parse", "HEAD")
			txGit(t, childWT, "checkout", "-b", childLayer2Branch)
			testutil.CommitFile(t, childWT, "c2.txt", "child layer 2\n", "child layer 2")
		} else {
			txGit(t, childWT, "checkout", "-b", childLayer2Branch)
		}

		publishable := true
		repoDirs[i] = repoDir
		parentTips[i] = parentTip
		childLayer1[i] = layer1Tip
		childWTs[i] = childWT
		parentRepos = append(parentRepos, feature.FeatureRepo{
			Name: repoName, Path: repoDir, WorktreePath: repoDir,
			Branch: parentLayer2, BaseBranch: "main", Publishable: &publishable,
		})
		childRepos = append(childRepos, feature.FeatureRepo{
			Name: repoName, Path: repoDir, WorktreePath: childWT,
			Branch: childLayer2Branch, BaseBranch: "main",
		})
		bases = append(bases, feature.ChildRepoBase{Repo: repoName, SHA: parentTip, ParentBranch: parentLayer2})
	}

	store := feature.NewStore(t.TempDir())
	parent := &feature.Feature{
		ID: "parent-append", Name: "Parent Append", Slug: "parent-append",
		Status: feature.StatusPublished, CurrentPhase: feature.PhasePublish,
		Created: time.Now(), ActiveRun: 1, RunCount: 1,
		Checkpoints: feature.Checkpoints{ManualPublish: true},
		Repos:       parentRepos,
		RepoStates:  map[string]*feature.RepoState{"repoA": {Touched: true}, "repoB": {Touched: true}},
		Stack: []feature.StackLayer{
			{Position: 1, Title: "Parent one", Slug: "parent-one", Phases: []int{1}, Branch: parentLayer1},
			{Position: 2, Title: "Parent two", Slug: "parent-two", Phases: []int{2}, Branch: parentLayer2},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if numRepos < 2 {
		parent.RepoStates = map[string]*feature.RepoState{"repoA": {Touched: true}}
	}
	child := &feature.Feature{
		ID: "child-append", Name: "Child Append", Slug: "child-append",
		Status: feature.StatusReviewPassed, CurrentPhase: feature.PhaseFinalReview,
		Pipeline: feature.PipelineMedium,
		Created:  time.Now(), ActiveRun: 1, RunCount: 1,
		Repos:      childRepos,
		RepoStates: map[string]*feature.RepoState{"repoA": {Touched: true}, "repoB": {Touched: true}},
		Stack: []feature.StackLayer{
			{
				Position: 1, Title: "Child one", Slug: "child-one", Phases: []int{1}, Branch: childLayer1Branch,
				Repos: map[string]feature.StackRepoEntry{"repoA": {TipSHA: childLayer1[0]}},
			},
			{
				Position: 2, Title: "Child two", Slug: "child-two", Phases: []int{2}, Branch: childLayer2Branch,
			},
		},
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID, Kind: feature.ChildKindRefactor, Bases: bases,
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if numRepos < 2 {
		child.RepoStates = map[string]*feature.RepoState{"repoA": {Touched: true}}
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
	appended := []string{
		git.LayerBranchName(parent.WorkspaceSlug(), 3, "child-one"),
		git.LayerBranchName(parent.WorkspaceSlug(), 4, "child-two"),
	}
	return &stackedAppendFixture{
		t: t, repoDirs: repoDirs, parentTips: parentTips, childLayer1: childLayer1,
		store: store, mgr: mgr, wm: wm, parent: parent, child: child,
		appended: appended, parentLayer2: parentLayer2,
	}
}

func (fx *stackedAppendFixture) orchestrator() *Orchestrator {
	return New(Deps{Lifecycle: fx.mgr, Store: fx.store, Worktrees: fx.wm}, Hooks{})
}

// TestAppendPreparationRecordsCreatedRefsPerLayer proves preparation maps
// each child layer j onto parent position n+j: two created refs per
// repository named with the parent's numbering (positions 3 and 4) and the
// child's slugs, candidates equal to the child's recorded layer tips (the
// top layer's candidate is the child head), an empty anchor on each, the
// previous top naming layer 2's branch and tip, and two appended layer
// definitions with origins — while no ref is created yet and the parent's
// refs are unchanged. A repository whose child layer has no commits gets its
// candidate from the tip below it.
func TestAppendPreparationRecordsCreatedRefsPerLayer(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newStackedAppendFixture(t, 2)
	o := fx.orchestrator()

	child, err := fx.store.Load(fx.child.ID)
	if err != nil {
		t.Fatalf("load child: %v", err)
	}
	parent, err := fx.store.Load(fx.parent.ID)
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil {
		t.Fatalf("prepareTransactionCandidates() error = %v", err)
	}
	if journal == nil {
		t.Fatal("journal is nil after successful preparation")
	}
	if journal.Phase != feature.TransactionPhasePrepared {
		t.Fatalf("journal phase = %s, want prepared", journal.Phase)
	}

	// Two appended layer definitions with origins, named with the parent's
	// numbering and the child's slugs.
	if len(journal.AppendedLayers) != 2 {
		t.Fatalf("appended layer definitions = %+v, want two", journal.AppendedLayers)
	}
	for k, want := range []struct {
		position int
		branch   string
		origin   int
	}{
		{position: 3, branch: fx.appended[0], origin: 1},
		{position: 4, branch: fx.appended[1], origin: 2},
	} {
		def := journal.AppendedLayers[k]
		if def.Position != want.position || def.Branch != want.branch ||
			def.Origin == nil || def.Origin.SourceFeatureID != child.ID || def.Origin.SourceLayerPosition != want.origin {
			t.Fatalf("appended layer def %d = %+v, want position %d on %s with origin layer %d",
				k, def, want.position, want.branch, want.origin)
		}
	}

	for i := range journal.Entries {
		entry := &journal.Entries[i]
		if entry.PrepState != feature.RepoPrepPrepared {
			t.Fatalf("repo %d: prep state = %s, want prepared", i, entry.PrepState)
		}
		if prev := entry.PreviousTopRef(); prev == nil || prev.Branch != fx.parentLayer2 ||
			prev.Layer != 2 || prev.TipSHA != fx.parentTips[i] {
			t.Fatalf("repo %d: previous top = %+v, want layer 2 on %s at %s",
				i, prev, fx.parentLayer2, fx.parentTips[i])
		}
		if len(entry.Refs) != 2 {
			t.Fatalf("repo %d: refs = %+v, want two created refs", i, entry.Refs)
		}
		for k, ref := range entry.Refs {
			if ref.RefKind() != feature.RepoRefKindCreate || ref.AnchorSHA != "" {
				t.Fatalf("repo %d ref %d = %+v, want a created ref with an empty anchor", i, k, ref)
			}
			if ref.Branch != fx.appended[k] || ref.Layer != 3+k {
				t.Fatalf("repo %d ref %d = %+v, want branch %s at position %d", i, k, ref, fx.appended[k], 3+k)
			}
		}
		// The first repository's candidates are the child's recorded layer
		// tips: layer 1's recorded tip for position 3, the child head (which
		// equals layer 2's tip) for position 4.
		if i == 0 {
			if got := entry.Refs[0].CandidateSHA; got != fx.childLayer1[0] {
				t.Fatalf("repo 0: position 3 candidate = %s, want child layer 1 tip %s", got, fx.childLayer1[0])
			}
			if got := entry.Refs[1].CandidateSHA; got != entry.ChildHeadSHA {
				t.Fatalf("repo 0: position 4 candidate = %s, want child head %s", got, entry.ChildHeadSHA)
			}
		} else {
			// The second repository's child layer 1 has no commits: both
			// candidates fall back to the tip below — the parent tip.
			for k, ref := range entry.Refs {
				if ref.CandidateSHA != fx.parentTips[i] {
					t.Fatalf("repo 1: position %d candidate = %s, want the tip below (parent tip %s)", 3+k, ref.CandidateSHA, fx.parentTips[i])
				}
			}
		}
	}

	// No ref is created yet and the parent's refs are unchanged.
	for i := range fx.repoDirs {
		for _, branch := range fx.appended {
			if branches := txGit(t, fx.repoDirs[i], "branch", "--list", branch); branches != "" {
				t.Fatalf("repo %d: appended branch %s created during preparation", i, branch)
			}
		}
		if got := txGit(t, fx.repoDirs[i], "rev-parse", "refs/heads/"+fx.parentLayer2); got != fx.parentTips[i] {
			t.Fatalf("repo %d: parent top ref = %s, want unchanged %s", i, got, fx.parentTips[i])
		}
		if got := txGit(t, fx.repoDirs[i], "rev-parse", "refs/heads/feature/append-parent-one"); got == "" {
			t.Fatalf("repo %d: parent layer 1 ref missing", i)
		}
	}
}

// TestAppendPreparationParksBeforeAnyComputation proves the parking
// conditions of append preparation: a parent tip that moved since launch
// parks the drift attention before any append computation (no appended layer
// definitions, no created refs), a pre-existing local branch with an appended
// name parks the ref-race attention naming the branch, and a parent or child
// without a persisted stack parks the candidate-failed attention naming the
// missing stack.
func TestAppendPreparationParksBeforeAnyComputation(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}

	t.Run("parent tip drift parks before any append computation", func(t *testing.T) {
		fx := newStackedAppendFixture(t, 2)
		testutil.CommitFile(t, fx.repoDirs[0], "drift.txt", "external\n", "external parent move")
		if err := fx.orchestrator().RunChildIntegration(fx.child.ID); err != nil {
			t.Fatalf("RunChildIntegration() error = %v, want nil with attention", err)
		}
		_, child := fx.reloadChild()
		tx := child.Parent.Transaction
		if tx == nil || tx.Phase != feature.TransactionPhaseAttention ||
			tx.Attention == nil || tx.Attention.Code != errcat.IntegrationParentRefDrift {
			t.Fatalf("transaction = %+v, want integration_parent_ref_drift attention", tx)
		}
		if len(tx.AppendedLayers) != 0 {
			t.Fatalf("appended layers computed despite the drift park: %+v", tx.AppendedLayers)
		}
		for i := range tx.Entries {
			if len(tx.Entries[i].Refs) != 0 {
				t.Fatalf("repo %d: created refs recorded despite the drift park: %+v", i, tx.Entries[i].Refs)
			}
		}
		for i := range fx.repoDirs {
			if branches := txGit(t, fx.repoDirs[i], "branch", "--list", fx.appended[0]); branches != "" {
				t.Fatalf("repo %d: appended branch created despite the drift park", i)
			}
		}
	})

	t.Run("pre-existing appended branch parks ref-race naming it", func(t *testing.T) {
		fx := newStackedAppendFixture(t, 2)
		txGit(t, fx.repoDirs[0], "branch", fx.appended[0], fx.parentTips[0])
		if err := fx.orchestrator().RunChildIntegration(fx.child.ID); err != nil {
			t.Fatalf("RunChildIntegration() error = %v, want nil with attention", err)
		}
		_, child := fx.reloadChild()
		tx := child.Parent.Transaction
		if tx == nil || tx.Phase != feature.TransactionPhaseAttention ||
			tx.Attention == nil || tx.Attention.Code != errcat.IntegrationRefRace {
			t.Fatalf("transaction = %+v, want integration_ref_race attention", tx)
		}
		if !strings.Contains(tx.Attention.Diagnostics, fx.appended[0]) {
			t.Fatalf("attention diagnostics = %q, want the pre-existing branch %s named", tx.Attention.Diagnostics, fx.appended[0])
		}
		if got := txGit(t, fx.repoDirs[0], "rev-parse", "refs/heads/"+fx.appended[0]); got != fx.parentTips[0] {
			t.Fatalf("pre-existing branch moved: %s, want preserved %s", got, fx.parentTips[0])
		}
		if branches := txGit(t, fx.repoDirs[1], "branch", "--list", fx.appended[0]); branches != "" {
			t.Fatalf("repo 1: appended branch created despite the park")
		}
	})

	t.Run("child without a persisted stack parks candidate-failed", func(t *testing.T) {
		fx := newStackedAppendFixture(t, 2)
		if err := fx.store.Modify(fx.child.ID, func(f *feature.Feature) error {
			f.Stack = nil
			return nil
		}); err != nil {
			t.Fatalf("strip child stack: %v", err)
		}
		if err := fx.orchestrator().RunChildIntegration(fx.child.ID); err != nil {
			t.Fatalf("RunChildIntegration() error = %v, want nil with attention", err)
		}
		_, child := fx.reloadChild()
		tx := child.Parent.Transaction
		if tx == nil || tx.Phase != feature.TransactionPhaseAttention ||
			tx.Attention == nil || tx.Attention.Code != errcat.IntegrationCandidateFailed {
			t.Fatalf("transaction = %+v, want integration_candidate_failed attention", tx)
		}
		if !strings.Contains(tx.Attention.Diagnostics, "child") {
			t.Fatalf("attention diagnostics = %q, want the missing child stack named", tx.Attention.Diagnostics)
		}
	})

	t.Run("parent without a persisted stack parks candidate-failed", func(t *testing.T) {
		fx := newStackedAppendFixture(t, 2)
		if err := fx.store.Modify(fx.parent.ID, func(f *feature.Feature) error {
			f.Stack = nil
			return nil
		}); err != nil {
			t.Fatalf("strip parent stack: %v", err)
		}
		if err := fx.orchestrator().RunChildIntegration(fx.child.ID); err != nil {
			t.Fatalf("RunChildIntegration() error = %v, want nil with attention", err)
		}
		_, child := fx.reloadChild()
		tx := child.Parent.Transaction
		if tx == nil || tx.Phase != feature.TransactionPhaseAttention ||
			tx.Attention == nil || tx.Attention.Code != errcat.IntegrationCandidateFailed {
			t.Fatalf("transaction = %+v, want integration_candidate_failed attention", tx)
		}
		if !strings.Contains(tx.Attention.Diagnostics, "parent") {
			t.Fatalf("attention diagnostics = %q, want the missing parent stack named", tx.Attention.Diagnostics)
		}
	})
}

func (fx *stackedAppendFixture) reloadChild() (*feature.Feature, *feature.Feature) {
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

// recordingTransactionWorktrees wraps the real worktree manager and records
// every multi-ref transaction batch it executes.
type recordingTransactionWorktrees struct {
	*git.WorktreeManager
	calls []recordedRefTransaction
}

type recordedRefTransaction struct {
	repoPath string
	updates  []git.RefUpdate
}

func (w *recordingTransactionWorktrees) UpdateRefsTransaction(repoPath string, updates []git.RefUpdate) error {
	w.calls = append(w.calls, recordedRefTransaction{
		repoPath: repoPath,
		updates:  append([]git.RefUpdate(nil), updates...),
	})
	return w.WorktreeManager.UpdateRefsTransaction(repoPath, updates)
}

// TestAppendApplyIssuesVerifyAndCreatesPerRepo proves apply issues exactly
// one ref transaction per repository — an atomic verify of the previous top
// at its recorded tip followed by a create expecting absence for every
// appended layer — switches the worktree onto the new top layer's branch,
// and updates the repository record's branch.
func TestAppendApplyIssuesVerifyAndCreatesPerRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newStackedAppendFixture(t, 2)
	child, err := fx.store.Load(fx.child.ID)
	if err != nil {
		t.Fatalf("load child: %v", err)
	}
	parent, err := fx.store.Load(fx.parent.ID)
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	o := fx.orchestrator()
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil || journal == nil {
		t.Fatalf("prepareTransactionCandidates() = %+v, %v; want a prepared journal", journal, err)
	}

	recording := &recordingTransactionWorktrees{WorktreeManager: fx.wm}
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
		if len(call.updates) != 3 {
			t.Fatalf("repo %d: transaction updates = %+v, want the previous-top verify plus two creates", i, call.updates)
		}
		verify := call.updates[0]
		if verify.Ref != "refs/heads/"+fx.parentLayer2 || verify.OldSHA != fx.parentTips[i] || verify.NewSHA != fx.parentTips[i] {
			t.Fatalf("repo %d: verify line = %+v, want previous top %s at %s", i, verify, fx.parentLayer2, fx.parentTips[i])
		}
		for k := range fx.appended {
			create := call.updates[1+k]
			want := journal.Entries[i].Refs[k].CandidateSHA
			if create.Ref != "refs/heads/"+fx.appended[k] || create.OldSHA != "" || create.NewSHA != want {
				t.Fatalf("repo %d: create line %d = %+v, want %s created at %s expecting absence", i, k, create, fx.appended[k], want)
			}
		}
		// The worktree switched onto the new top layer's branch and the
		// repository record follows it.
		if branch := txGit(t, fx.repoDirs[i], "branch", "--show-current"); branch != fx.appended[1] {
			t.Fatalf("repo %d: worktree branch = %q, want new top branch %q", i, branch, fx.appended[1])
		}
	}
	freshParent, err := fx.store.Load(fx.parent.ID)
	if err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	for i := range freshParent.Repos {
		if freshParent.Repos[i].Branch != fx.appended[1] {
			t.Fatalf("repo %d: record branch = %q, want new top branch %q", i, freshParent.Repos[i].Branch, fx.appended[1])
		}
	}
}

// TestAppendDiscardInAppliedPhaseDeletesCreatedRefs proves discarding a
// refactor child whose transaction is applied deletes the created refs still
// at their candidates, leaves the parent's existing layer refs byte-identical
// to before launch, restores the worktree to the previous top branch and the
// record with it, and closes the child as discarded.
func TestAppendDiscardInAppliedPhaseDeletesCreatedRefs(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newStackedAppendFixture(t, 2)
	child, err := fx.store.Load(fx.child.ID)
	if err != nil {
		t.Fatalf("load child: %v", err)
	}
	parent, err := fx.store.Load(fx.parent.ID)
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	o := fx.orchestrator()
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil || journal == nil {
		t.Fatalf("prepareTransactionCandidates() = %+v, %v; want a prepared journal", journal, err)
	}

	// Manually apply every repository: created refs at their candidates, the
	// worktrees switched, the entries marked applied.
	for i := range journal.Entries {
		top := journal.Entries[i].TopRef()
		txGit(t, fx.repoDirs[i], "branch", top.Branch, top.CandidateSHA)
		txGit(t, fx.repoDirs[i], "checkout", top.Branch)
		journal.Entries[i].ApplyState = feature.RepoApplyApplied
	}
	journal.Phase = feature.TransactionPhaseApplied
	if err := o.persistTransaction(child.ID, journal); err != nil {
		t.Fatalf("persist applied journal: %v", err)
	}

	if err := o.DiscardChild(fx.child.ID); err != nil {
		t.Fatalf("DiscardChild() error = %v", err)
	}
	_, child = fx.reloadChild()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
		t.Fatalf("child close outcome = %q, want discarded", child.Parent.CloseOutcome)
	}
	for i := range fx.repoDirs {
		for _, branch := range fx.appended {
			if branches := txGit(t, fx.repoDirs[i], "branch", "--list", branch); branches != "" {
				t.Fatalf("repo %d: created ref %s still present after discard", i, branch)
			}
		}
		if got := txGit(t, fx.repoDirs[i], "rev-parse", "refs/heads/"+fx.parentLayer2); got != fx.parentTips[i] {
			t.Fatalf("repo %d: parent top ref = %s, want byte-identical %s", i, got, fx.parentTips[i])
		}
		if got := txGit(t, fx.repoDirs[i], "rev-parse", "refs/heads/feature/append-parent-one"); got == "" {
			t.Fatalf("repo %d: parent layer 1 ref missing after discard", i)
		}
		if branch := txGit(t, fx.repoDirs[i], "branch", "--show-current"); branch != fx.parentLayer2 {
			t.Fatalf("repo %d: worktree branch = %q, want previous top branch %q after discard", i, branch, fx.parentLayer2)
		}
	}
	freshParent, _ := fx.reloadChild()
	for i := range freshParent.Repos {
		if freshParent.Repos[i].Branch != fx.parentLayer2 {
			t.Fatalf("repo %d: record branch = %q, want previous top branch %q restored", i, freshParent.Repos[i].Branch, fx.parentLayer2)
		}
	}
}

// TestAppendDiscardParksWhenCreatedRefMovedExternally proves a discard whose
// created ref was moved externally cannot be proven safe: the discard parks
// with the ref-race attention naming the branch, the ref is preserved, and
// the child stays active with the discard intent.
func TestAppendDiscardParksWhenCreatedRefMovedExternally(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newStackedAppendFixture(t, 2)
	child, err := fx.store.Load(fx.child.ID)
	if err != nil {
		t.Fatalf("load child: %v", err)
	}
	parent, err := fx.store.Load(fx.parent.ID)
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	o := fx.orchestrator()
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil || journal == nil {
		t.Fatalf("prepareTransactionCandidates() = %+v, %v; want a prepared journal", journal, err)
	}
	for i := range journal.Entries {
		top := journal.Entries[i].TopRef()
		txGit(t, fx.repoDirs[i], "branch", top.Branch, top.CandidateSHA)
		txGit(t, fx.repoDirs[i], "checkout", top.Branch)
		journal.Entries[i].ApplyState = feature.RepoApplyApplied
	}
	journal.Phase = feature.TransactionPhaseApplied
	if err := o.persistTransaction(child.ID, journal); err != nil {
		t.Fatalf("persist applied journal: %v", err)
	}

	// Move the first repository's top created ref externally: a commit on
	// the checked-out appended branch.
	movedSHA := testutil.CommitFile(t, fx.repoDirs[0], "external.txt", "external\n", "external move of a created ref")

	if err := o.DiscardChild(fx.child.ID); err == nil {
		t.Fatal("DiscardChild() error = nil, want ref safety not established")
	}
	_, child = fx.reloadChild()
	if child.Parent.CloseOutcome != "" {
		t.Fatalf("child closed (%q) although the discard parked", child.Parent.CloseOutcome)
	}
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention ||
		tx.Attention == nil || tx.Attention.Code != errcat.IntegrationRefRace {
		t.Fatalf("transaction = %+v, want integration_ref_race attention", tx)
	}
	if !strings.Contains(tx.Attention.Diagnostics, fx.appended[1]) {
		t.Fatalf("attention diagnostics = %q, want the moved created ref's branch %s named", tx.Attention.Diagnostics, fx.appended[1])
	}
	// The moved ref is preserved. Ref safety is per repository: the other
	// repository's provably applied created ref is still rolled back.
	if got := txGit(t, fx.repoDirs[0], "rev-parse", "refs/heads/"+fx.appended[1]); got != movedSHA {
		t.Fatalf("repo 0: moved created ref = %s, want preserved %s", got, movedSHA)
	}
	if branches := txGit(t, fx.repoDirs[1], "branch", "--list", fx.appended[1]); branches != "" {
		t.Fatal("repo 1: provably applied created ref not rolled back")
	}
	if branch := txGit(t, fx.repoDirs[1], "branch", "--show-current"); branch != fx.parentLayer2 {
		t.Fatalf("repo 1: worktree branch = %q, want previous top branch %q after its rollback", branch, fx.parentLayer2)
	}
}
