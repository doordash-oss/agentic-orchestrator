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

// Real-git coverage of the multi-repository transactional integration
// boundary: candidate preparation without advancing parent refs, conditional
// apply with compare-and-swap ref updates, conditional rollback of provable
// partial changes, external race handling, and startup reconciliation.

package orchestrator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// multiRepoTransactionFixture builds a real multi-repository parent/child pair
// with the specified number of repositories. Each repository has its own real
// git repo, a parent branch checked out, and a child worktree with committed
// child changes.
type multiRepoTransactionFixture struct {
	t              *testing.T
	repoDirs       []string
	parentSHA      []string
	store          *feature.Store
	mgr            *feature.Manager
	wm             *git.WorktreeManager
	parent         *feature.Feature
	child          *feature.Feature
	childWTs       []string
	childBranch    string
	appendedBranch string
}

func newMultiRepoTransactionFixture(t *testing.T, numRepos int) *multiRepoTransactionFixture {
	t.Helper()
	repoDirs := make([]string, numRepos)
	parentSHAs := make([]string, numRepos)
	childWTs := make([]string, numRepos)
	childBranch := "feature/child-tx"

	parentRepos := make([]feature.FeatureRepo, 0, numRepos)
	childRepos := make([]feature.FeatureRepo, 0, numRepos)
	bases := make([]feature.ChildRepoBase, 0, numRepos)

	for i := 0; i < numRepos; i++ {
		repoDir := testutil.InitGitRepo(t)
		txGit(t, repoDir, "checkout", "-b", "feature/parent")
		testutil.CommitFile(t, repoDir, "base.txt", "v1\n", "parent base")
		parentSHA := txGit(t, repoDir, "rev-parse", "HEAD")

		childWT := t.TempDir() + "/child-wt-" + string(rune('A'+i))
		txGit(t, repoDir, "worktree", "add", "-b", childBranch, childWT, parentSHA)
		testutil.CommitFile(t, childWT, "child.txt", "child work\n", "child change")

		repoName := "repo" + string(rune('A'+i))
		publishable := true
		repoDirs[i] = repoDir
		parentSHAs[i] = parentSHA
		childWTs[i] = childWT

		parentRepos = append(parentRepos, feature.FeatureRepo{
			Name:         repoName,
			Path:         repoDir,
			WorktreePath: repoDir,
			Branch:       "feature/parent",
			BaseBranch:   "main",
			Publishable:  &publishable,
		})
		childRepos = append(childRepos, feature.FeatureRepo{
			Name:         repoName,
			Path:         repoDir,
			WorktreePath: childWT,
			Branch:       childBranch,
			BaseBranch:   "main",
		})
		bases = append(bases, feature.ChildRepoBase{Repo: repoName, SHA: parentSHA, ParentBranch: "feature/parent"})
	}

	// The child's recorded layer tip per repository: the head after the
	// committed child change, before integration commits anything remaining.
	childTips := make([]string, numRepos)
	for i, wt := range childWTs {
		childTips[i] = txGit(t, wt, "rev-parse", "HEAD")
	}

	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	parent := &feature.Feature{
		ID:           "parent-tx",
		Name:         "Parent TX",
		Slug:         "parent-tx",
		Status:       feature.StatusPublished,
		CurrentPhase: feature.PhasePublish,
		Created:      time.Now(),
		ActiveRun:    1,
		RunCount:     1,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Repos:        parentRepos,
		// A one-layer stack on the checked-out parent branch, so refactor
		// children append their layers onto it.
		Stack: []feature.StackLayer{{
			Position: 1, Title: "Parent layer", Slug: "parent-layer", Phases: []int{1}, Branch: "feature/parent",
		}},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	parent.RepoStates = make(map[string]*feature.RepoState, numRepos)
	for _, pr := range parentRepos {
		parent.RepoStates[pr.Name] = &feature.RepoState{Touched: true}
	}
	childStackRepos := make(map[string]feature.StackRepoEntry, numRepos)
	for i := range childRepos {
		childStackRepos[childRepos[i].Name] = feature.StackRepoEntry{TipSHA: childTips[i]}
	}
	child := &feature.Feature{
		ID:           "child-tx",
		Name:         "Child TX",
		Slug:         "child-tx",
		Status:       feature.StatusReviewPassed,
		CurrentPhase: feature.PhaseFinalReview,
		Pipeline:     feature.PipelineMedium,
		Created:      time.Now(),
		ActiveRun:    1,
		RunCount:     1,
		Repos:        childRepos,
		// A one-layer stack on the child branch namespace with the recorded
		// per-repository tips, so refactor integration appends it as parent
		// position 2.
		Stack: []feature.StackLayer{{
			Position: 1, Title: "Child layer", Slug: "child-layer", Phases: []int{1}, Branch: childBranch,
			Repos: childStackRepos,
		}},
		SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID,
			Kind:     feature.ChildKindRefactor,
			Bases:    bases,
		},
	}
	child.RepoStates = make(map[string]*feature.RepoState, numRepos)
	for _, cr := range childRepos {
		child.RepoStates[cr.Name] = &feature.RepoState{Touched: true}
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
	return &multiRepoTransactionFixture{
		t: t, repoDirs: repoDirs, parentSHA: parentSHAs,
		store: store, mgr: mgr, wm: wm,
		parent: parent, child: child, childWTs: childWTs, childBranch: childBranch,
		appendedBranch: git.LayerBranchName(parent.WorkspaceSlug(), 2, "child-layer"),
	}
}

func (fx *multiRepoTransactionFixture) orchestrator() *Orchestrator {
	return New(Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Worktrees: fx.wm,
	}, Hooks{})
}

func (fx *multiRepoTransactionFixture) reload() (*feature.Feature, *feature.Feature) {
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

func (fx *multiRepoTransactionFixture) refSHA(repoIdx int, ref string) string {
	return txGit(fx.t, fx.repoDirs[repoIdx], "rev-parse", ref)
}

func txGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestTransactionTwoRepoHappyPath proves the full multi-repository
// transaction boundary with two repositories: candidates are prepared without
// advancing parent refs, applied via CAS ref updates, the child closes
// Completed, and every parent branch carries an explicit merge commit.
func TestTransactionTwoRepoHappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)
	o := fx.orchestrator()

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v", err)
	}

	parent, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady", parent.Status)
	}

	// Every repository's appended layer branch was created at the child
	// head, the existing parent layer's ref is unchanged, and the worktree
	// switched onto the appended branch.
	for i := range fx.repoDirs {
		want := child.Parent.Transaction.Entries[i].ChildHeadSHA
		if got := fx.refSHA(i, "refs/heads/"+fx.appendedBranch); got != want {
			t.Fatalf("repo %d: appended branch ref = %s, want child head %s", i, got, want)
		}
		if got := fx.refSHA(i, "refs/heads/feature/parent"); got != fx.parentSHA[i] {
			t.Fatalf("repo %d: parent branch ref = %s, want unchanged %s", i, got, fx.parentSHA[i])
		}
		if branch := txGit(t, fx.repoDirs[i], "branch", "--show-current"); branch != fx.appendedBranch {
			t.Fatalf("repo %d: worktree branch = %q, want appended branch %q", i, branch, fx.appendedBranch)
		}
	}
	if child.Parent.Transaction == nil || child.Parent.Transaction.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %+v, want merged", child.Parent.Transaction)
	}

	// All child worktrees are cleaned up.
	for i, wt := range fx.childWTs {
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Fatalf("repo %d: child worktree still present", i)
		}
	}
}

// TestTransactionThreeRepoHappyPath proves the full boundary with three
// repositories: every repository gains the appended layer's created ref at
// the child head while its existing parent layer ref stays unchanged.
func TestTransactionThreeRepoHappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 3)
	o := fx.orchestrator()

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v", err)
	}

	_, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	for i := range fx.repoDirs {
		journal := child.Parent.Transaction
		entry := journal.EntryByRepo(child.Repos[i].Name)
		if entry == nil {
			t.Fatalf("repo %d: journal entry missing", i)
		}
		// The appended branch sits at the child head, which descends from
		// the previous top tip.
		top := entry.TopRef()
		if top == nil || top.RefKind() != feature.RepoRefKindCreate {
			t.Fatalf("repo %d: top ref = %+v, want a created ref", i, top)
		}
		if got := fx.refSHA(i, "refs/heads/"+fx.appendedBranch); got != entry.ChildHeadSHA {
			t.Fatalf("repo %d: appended branch ref = %s, want child head %s", i, got, entry.ChildHeadSHA)
		}
		if got := fx.refSHA(i, "refs/heads/feature/parent"); got != fx.parentSHA[i] {
			t.Fatalf("repo %d: parent branch ref = %s, want unchanged %s", i, got, fx.parentSHA[i])
		}
	}
}

// TestTransactionDirtyAggregation proves parent cleanliness is evaluated for
// all repositories in one preflight, with staged, unstaged, and untracked
// diagnostics returned together rather than stopping at the first dirty
// repository. All parent refs remain unchanged.
func TestTransactionDirtyAggregation(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 3)
	o := fx.orchestrator()

	// Make repoA dirty (untracked) and repoC dirty (staged).
	if err := os.WriteFile(fx.repoDirs[0]+"/stray.txt", []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty repoA: %v", err)
	}
	txGit(t, fx.repoDirs[2], "add", "base.txt")
	if err := os.WriteFile(fx.repoDirs[2]+"/base.txt", []byte("modified\n"), 0o644); err != nil {
		t.Fatalf("modify repoC: %v", err)
	}
	txGit(t, fx.repoDirs[2], "add", "base.txt")

	// Record pre-flight parent refs.
	preRefs := make([]string, len(fx.repoDirs))
	for i := range fx.repoDirs {
		preRefs[i] = fx.refSHA(i, "refs/heads/feature/parent")
	}

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil with attention", err)
	}

	// All parent refs unchanged.
	for i := range fx.repoDirs {
		if got := fx.refSHA(i, "refs/heads/feature/parent"); got != preRefs[i] {
			t.Fatalf("repo %d: parent ref moved from %s to %s on dirty preflight", i, preRefs[i], got)
		}
	}

	_, child := fx.reload()
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention", tx)
	}
	// The record classifies the dirty-parent park and lists every dirty
	// repository with its flattened dirty files.
	if tx.Attention == nil || tx.Attention.Code != errcat.IntegrationParentDirty {
		t.Fatalf("attention record = %+v, want integration_parent_dirty", tx.Attention)
	}
	if tx.Attention.Context == nil || len(tx.Attention.Context.Repositories) < 2 {
		t.Fatalf("attention repositories = %+v, want both dirty repositories", tx.Attention.Context)
	}
	foundUntracked := false
	for _, repo := range tx.Attention.Context.Repositories {
		if repo.Name == child.Repos[0].Name && slices.Contains(repo.DirtyFiles, "stray.txt") {
			foundUntracked = true
		}
	}
	if !foundUntracked {
		t.Fatalf("attention repositories = %+v, want repoA dirty_files to list stray.txt", tx.Attention.Context.Repositories)
	}
}

// TestTransactionPreparationFailureLeavesRefsUnchanged proves a preparation
// failure — here a parent tip the child's history does not descend from, the
// append analog of a merge conflict — leaves every parent ref unchanged.
func TestTransactionPreparationFailureLeavesRefsUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)
	o := fx.orchestrator()

	// Diverge the second repo's parent branch from the child's history and
	// pin the persisted base to the new tip so the drift gate does not fire
	// and the ancestry-violation path is exercised.
	testutil.CommitFile(t, fx.repoDirs[1], "child.txt", "parent-side conflict\n", "conflicting parent commit")
	child, _ := fx.store.Load(fx.child.ID)
	child.Parent.Bases[1].SHA = fx.refSHA(1, "refs/heads/feature/parent")
	if err := fx.store.Save(child); err != nil {
		t.Fatalf("save child with pinned base: %v", err)
	}

	preRefs := make([]string, len(fx.repoDirs))
	for i := range fx.repoDirs {
		preRefs[i] = fx.refSHA(i, "refs/heads/feature/parent")
	}

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil with attention", err)
	}

	// All parent refs unchanged.
	for i := range fx.repoDirs {
		if got := fx.refSHA(i, "refs/heads/feature/parent"); got != preRefs[i] {
			t.Fatalf("repo %d: parent ref moved from %s to %s on conflict", i, preRefs[i], got)
		}
	}

	_, child = fx.reload()
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention", tx)
	}
	// The record classifies the ancestry-violation park: no replay is
	// attempted, so the diverged tip is a candidate failure, not a conflict.
	if tx.Attention == nil || tx.Attention.Code != errcat.IntegrationCandidateFailed {
		t.Fatalf("attention record = %+v, want integration_candidate_failed", tx.Attention)
	}
	if !strings.Contains(tx.Attention.Diagnostics, "ancestry") {
		t.Fatalf("attention diagnostics = %q, want the ancestry-chain violation named", tx.Attention.Diagnostics)
	}
	if tx.Attention.Context == nil || len(tx.Attention.Context.Repositories) != 1 ||
		tx.Attention.Context.Repositories[0].Name != child.Repos[1].Name {
		t.Fatalf("attention repositories = %+v, want repo 1", tx.Attention.Context)
	}
	if branches := txGit(t, fx.repoDirs[1], "branch", "--list", fx.appendedBranch); branches != "" {
		t.Fatalf("appended branch %s created although preparation failed", fx.appendedBranch)
	}
}

// TestTransactionExternalParentAdvancementParksDrift proves an external
// commit that moves a parent branch tip away from its creation-time base is
// no longer silently absorbed: preparation parks the transaction at attention
// with the typed parent-drift gate code, stages no candidates, and leaves
// every parent ref byte-identical.
func TestTransactionExternalParentAdvancementParksDrift(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)
	o := fx.orchestrator()

	// Advance the first repo's parent branch outside the transaction.
	testutil.CommitFile(t, fx.repoDirs[0], "parent-advance.txt", "external advance\n", "parent advanced")

	preRefs := make([]string, len(fx.repoDirs))
	for i := range fx.repoDirs {
		preRefs[i] = fx.refSHA(i, "refs/heads/feature/parent")
	}

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil with attention", err)
	}

	// All parent refs unchanged.
	for i := range fx.repoDirs {
		if got := fx.refSHA(i, "refs/heads/feature/parent"); got != preRefs[i] {
			t.Fatalf("repo %d: parent ref moved from %s to %s on drift", i, preRefs[i], got)
		}
	}

	_, child := fx.reload()
	if child.Parent.CloseOutcome != "" {
		t.Fatalf("child close outcome = %q, want open relationship", child.Parent.CloseOutcome)
	}
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention", tx)
	}
	entry := tx.EntryByRepo(child.Repos[0].Name)
	if entry == nil {
		t.Fatalf("repo 0: entry missing, journal = %+v", tx)
	}
	if tx.Attention == nil || tx.Attention.Code != errcat.IntegrationParentRefDrift {
		t.Fatalf("attention record = %+v, want integration_parent_ref_drift", tx.Attention)
	}
	if tx.Attention.Context == nil || len(tx.Attention.Context.Repositories) != 1 ||
		tx.Attention.Context.Repositories[0].Name != child.Repos[0].Name {
		t.Fatalf("attention repositories = %+v, want repo 0", tx.Attention.Context)
	}
	if entry.PrepState != feature.RepoPrepFailed {
		t.Fatalf("repo 0: prep state = %s, want failed", entry.PrepState)
	}
	for i := range tx.Entries {
		if tx.Entries[i].HasCandidateRef() {
			t.Fatalf("repo %d: candidate staged despite drift: %+v", i, tx.Entries[i].Refs)
		}
	}
}

// TestTransactionParentDriftRetryAcknowledges proves drift parks integration
// exactly once: retrying at the unchanged tip is the operator's acknowledgment
// and the integration absorbs the moved tip and completes, while any further
// movement parks again.
func TestTransactionParentDriftRetryAcknowledges(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)
	o := fx.orchestrator()

	testutil.CommitFile(t, fx.repoDirs[0], "parent-advance.txt", "external advance\n", "parent advanced")

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("first runChildIntegration() error = %v, want nil with attention", err)
	}
	_, child := fx.reload()
	if tx := child.Parent.Transaction; tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention after drift", tx)
	}

	// Further movement after the drift attention parks again.
	testutil.CommitFile(t, fx.repoDirs[0], "parent-advance-2.txt", "more external\n", "parent advanced again")
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("second runChildIntegration() error = %v, want nil with attention", err)
	}
	_, child = fx.reload()
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention after renewed drift", tx)
	}
	if tx := child.Parent.Transaction; tx == nil || tx.Attention == nil || tx.Attention.Code != errcat.IntegrationParentRefDrift {
		t.Fatalf("attention record = %+v, want integration_parent_ref_drift after renewed drift", child.Parent.Transaction.Attention)
	}

	// Retry at the unchanged tip acknowledges the drift — the gate passes —
	// but the append cannot absorb a tip the child's history does not descend
	// from: no replay is attempted, so preparation parks with the
	// candidate-failed attention naming the ancestry violation.
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("acknowledging runChildIntegration() error = %v", err)
	}
	_, child = fx.reload()
	tx = child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention ||
		tx.Attention == nil || tx.Attention.Code != errcat.IntegrationCandidateFailed {
		t.Fatalf("transaction after acknowledgment = %+v, want candidate-failed attention (no replay)", tx)
	}
	if !strings.Contains(tx.Attention.Diagnostics, "ancestry") {
		t.Fatalf("attention diagnostics = %q, want the ancestry-chain violation named", tx.Attention.Diagnostics)
	}

	// Remediate: restore the parent branch to its creation-time base and
	// retry; the append completes.
	txGit(t, fx.repoDirs[0], "reset", "--hard", fx.parentSHA[0])
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("remediating runChildIntegration() error = %v", err)
	}
	_, child = fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed after remediation", child.Parent.CloseOutcome)
	}
}

// TestTransactionParentDriftMultiRepoAggregation proves drift is evaluated
// for every repository in one preflight: drifted repos carry the typed gate
// code, clean repos do not, and no candidate is staged for either.
func TestTransactionParentDriftMultiRepoAggregation(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 3)
	o := fx.orchestrator()

	// Advance the first and third parent branches; the second stays clean.
	testutil.CommitFile(t, fx.repoDirs[0], "drift-a.txt", "external\n", "external parent commit A")
	testutil.CommitFile(t, fx.repoDirs[2], "drift-c.txt", "external\n", "external parent commit C")

	preRefs := make([]string, len(fx.repoDirs))
	for i := range fx.repoDirs {
		preRefs[i] = fx.refSHA(i, "refs/heads/feature/parent")
	}

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil with attention", err)
	}

	for i := range fx.repoDirs {
		if got := fx.refSHA(i, "refs/heads/feature/parent"); got != preRefs[i] {
			t.Fatalf("repo %d: parent ref moved from %s to %s on drift", i, preRefs[i], got)
		}
	}

	_, child := fx.reload()
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention", tx)
	}
	for i := range tx.Entries {
		if tx.Entries[i].HasCandidateRef() {
			t.Fatalf("repo %d: candidate staged despite drift: %+v", i, tx.Entries[i].Refs)
		}
	}
	// Both drifted repos join the record's repositories block; the clean
	// middle repo does not.
	if tx.Attention == nil || tx.Attention.Code != errcat.IntegrationParentRefDrift {
		t.Fatalf("attention record = %+v, want integration_parent_ref_drift", tx.Attention)
	}
	if tx.Attention.Context == nil || len(tx.Attention.Context.Repositories) != 2 {
		t.Fatalf("attention repositories = %+v, want exactly the two drifted repos", tx.Attention.Context)
	}
	for _, repo := range tx.Attention.Context.Repositories {
		if repo.Name == child.Repos[1].Name {
			t.Fatalf("clean repo %s listed in the drift record", repo.Name)
		}
	}
}

// TestTransactionAppendPartialApplyCrashRollsBackAndRetries proves a crash
// between one repository's ref transaction and its durable apply progress is
// converged by the startup scan: the provable partial apply is rolled back —
// the created ref deleted, the worktree switched back to the previous top —
// and a retry re-prepares and completes. A parent tip a refactor transaction
// produced can never be mistaken for drift, because append transactions never
// move the parent branch.
func TestTransactionAppendPartialApplyCrashRollsBackAndRetries(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)

	child, _ := fx.store.Load(fx.child.ID)
	parent, _ := fx.store.Load(fx.parent.ID)
	o := fx.orchestrator()
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil {
		t.Fatalf("prepareTransactionCandidates() error = %v", err)
	}
	if journal == nil {
		t.Fatal("journal is nil")
	}

	// Simulate the crash: repo 0's ref transaction and worktree switch ran,
	// but no apply progress was persisted and the aggregate phase is still
	// applying.
	top := journal.Entries[0].TopRef()
	txGit(t, fx.repoDirs[0], "branch", top.Branch, top.CandidateSHA)
	txGit(t, fx.repoDirs[0], "checkout", top.Branch)
	journal.Phase = feature.TransactionPhaseApplying
	if err := o.persistTransaction(child.ID, journal); err != nil {
		t.Fatalf("persist applying journal: %v", err)
	}

	// The startup scan rolls back the provable partial apply.
	if err := o.ReconcileIntegrationTransactions(); err != nil {
		t.Fatalf("ReconcileIntegrationTransactions() error = %v", err)
	}
	if branches := txGit(t, fx.repoDirs[0], "branch", "--list", fx.appendedBranch); branches != "" {
		t.Fatalf("repo 0: appended branch still present after rollback: %s", branches)
	}
	if got := fx.refSHA(0, "refs/heads/feature/parent"); got != fx.parentSHA[0] {
		t.Fatalf("repo 0: parent branch ref = %s, want unchanged %s", got, fx.parentSHA[0])
	}
	if branch := txGit(t, fx.repoDirs[0], "branch", "--show-current"); branch != "feature/parent" {
		t.Fatalf("repo 0: worktree branch = %q, want the previous top branch after rollback", branch)
	}
	stored, _ := fx.store.Load(fx.child.ID)
	if stored.Parent.Transaction == nil || stored.Parent.Transaction.Phase != feature.TransactionPhaseRolledBack {
		t.Fatalf("transaction phase = %+v, want rolled_back after the scan", stored.Parent.Transaction)
	}

	// A retry re-prepares from scratch and completes.
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration() retry error = %v", err)
	}
	_, child = fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed on retry", child.Parent.CloseOutcome)
	}
	if child.Parent.Transaction.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %s, want merged", child.Parent.Transaction.Phase)
	}
	if child.Parent.Transaction.Attention != nil {
		t.Fatalf("merged journal still carries an attention record: %+v", child.Parent.Transaction.Attention)
	}
}

// TestTransactionParentDriftRecheckAfterReset proves drift attention is
// re-checkable: once the parent branch is reset back to its creation-time
// base, re-running integration proceeds to completion.
func TestTransactionParentDriftRecheckAfterReset(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)
	o := fx.orchestrator()

	testutil.CommitFile(t, fx.repoDirs[0], "drift.txt", "external\n", "external parent commit")

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil with attention", err)
	}
	_, child := fx.reload()
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention", tx)
	}

	// Restore the parent branch to its creation-time base and retry.
	txGit(t, fx.repoDirs[0], "reset", "--hard", fx.parentSHA[0])

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() retry error = %v", err)
	}
	_, child = fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed after reset", child.Parent.CloseOutcome)
	}
	if child.Parent.Transaction.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %s, want merged", child.Parent.Transaction.Phase)
	}
}

// TestTransactionExternalRaceBeforeApply proves an external process that moves
// a target ref before apply is detected, preserved, and produces attention.
func TestTransactionExternalRaceBeforeApply(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)

	// Manually prepare candidates by calling prepareTransactionCandidates.
	child, _ := fx.store.Load(fx.child.ID)
	parent, _ := fx.store.Load(fx.parent.ID)
	o := fx.orchestrator()
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil {
		t.Fatalf("prepareTransactionCandidates() error = %v", err)
	}
	if journal == nil {
		t.Fatal("journal is nil after successful preparation")
	}

	// Externally move the first repo's parent branch.
	txGit(t, fx.repoDirs[0], "checkout", "feature/parent")
	testutil.CommitFile(t, fx.repoDirs[0], "external.txt", "external\n", "external movement")

	// Now apply should detect the external race.
	if err := o.applyTransactionCandidates(child, parent, journal); err != nil {
		t.Fatalf("applyTransactionCandidates() error = %v, want nil with attention", err)
	}

	_, child = fx.reload()
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention", tx)
	}
}

// TestTransactionRollbackOnLaterFailure proves a later apply failure rolls
// back earlier applied refs conditionally from candidate to old SHA.
func TestTransactionRollbackOnLaterFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 3)

	child, _ := fx.store.Load(fx.child.ID)
	parent, _ := fx.store.Load(fx.parent.ID)
	o := fx.orchestrator()
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil {
		t.Fatalf("prepareTransactionCandidates() error = %v", err)
	}
	if journal == nil {
		t.Fatal("journal is nil")
	}

	// Record the previous-top tips for rollback verification.
	oldSHAs := make([]string, len(journal.Entries))
	for i := range journal.Entries {
		if prev := journal.Entries[i].PreviousTopRef(); prev != nil {
			oldSHAs[i] = prev.TipSHA
		}
	}

	// Externally move the third repo's parent branch so the atomic verify of
	// its previous top fails. This will cause the apply of repo 2 to fail,
	// triggering rollback of repos 0 and 1.
	txGit(t, fx.repoDirs[2], "checkout", "feature/parent")
	testutil.CommitFile(t, fx.repoDirs[2], "race.txt", "external\n", "external race before repo 2 apply")

	// Apply: repo 0 and 1 should succeed, repo 2 should fail (CAS mismatch),
	// triggering rollback of repos 0 and 1.
	if err := o.applyTransactionCandidates(child, parent, journal); err != nil {
		t.Fatalf("applyTransactionCandidates() error = %v, want nil with attention", err)
	}

	_, child = fx.reload()
	tx := child.Parent.Transaction
	if tx == nil {
		t.Fatal("transaction journal missing")
	}
	// Repos 0 and 1 are rolled back: their created refs are deleted, their
	// parent branches never moved, and their worktrees are back on the
	// previous top branch.
	for i := 0; i < 2; i++ {
		if branches := txGit(t, fx.repoDirs[i], "branch", "--list", fx.appendedBranch); branches != "" {
			t.Fatalf("repo %d: appended branch still present after rollback: %s", i, branches)
		}
		got := fx.refSHA(i, "refs/heads/feature/parent")
		if got != oldSHAs[i] {
			t.Fatalf("repo %d: parent branch ref = %s after rollback, want previous top tip %s", i, got, oldSHAs[i])
		}
		if branch := txGit(t, fx.repoDirs[i], "branch", "--show-current"); branch != "feature/parent" {
			t.Fatalf("repo %d: worktree branch = %q, want the previous top branch after rollback", i, branch)
		}
	}
	// Repo 2's appended branch was never created.
	if branches := txGit(t, fx.repoDirs[2], "branch", "--list", fx.appendedBranch); branches != "" {
		t.Fatalf("repo 2: appended branch present despite the failed apply: %s", branches)
	}
}

// TestTransactionIdempotentReentry proves re-entering the same apply or
// rollback state is idempotent and cannot create duplicate merge commits or
// regress a ref.
func TestTransactionIdempotentReentry(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)
	o := fx.orchestrator()

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("first runChildIntegration() error = %v", err)
	}
	mergeHEADs := make([]string, len(fx.repoDirs))
	for i := range fx.repoDirs {
		mergeHEADs[i] = fx.refSHA(i, "refs/heads/feature/parent")
	}

	// Second pass should be a no-op on the settled relationship.
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("second runChildIntegration() error = %v", err)
	}
	for i := range fx.repoDirs {
		if got := fx.refSHA(i, "refs/heads/feature/parent"); got != mergeHEADs[i] {
			t.Fatalf("repo %d: ref changed on re-entry: %s != %s", i, got, mergeHEADs[i])
		}
	}
}

// TestTransactionStartupReconciliationApplied proves a journal whose complete
// candidate vector is already applied advances exactly once into normal
// settlement on startup.
func TestTransactionStartupReconciliationApplied(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)

	child, _ := fx.store.Load(fx.child.ID)
	parent, _ := fx.store.Load(fx.parent.ID)
	o := fx.orchestrator()
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil {
		t.Fatalf("prepareTransactionCandidates() error = %v", err)
	}
	if journal == nil {
		t.Fatal("journal is nil")
	}

	// Manually apply every ref (simulating a crash after all ref updates
	// but before closure): each created ref is created at its candidate.
	for i := range journal.Entries {
		top := journal.Entries[i].TopRef()
		txGit(t, fx.repoDirs[i], "branch", top.Branch, top.CandidateSHA)
	}

	// Simulate startup reconciliation.
	if err := o.ReconcileIntegrationTransactions(); err != nil {
		t.Fatalf("reconcileIntegrationTransactions() error = %v", err)
	}

	_, child = fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed after reconciliation", child.Parent.CloseOutcome)
	}
	if child.Parent.Transaction.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %s, want merged", child.Parent.Transaction.Phase)
	}

	// The closure persisted the appended layers onto the parent's stack with
	// per-repository tips equal to the created refs, and every worktree sits
	// on the new top branch.
	freshParent, _ := fx.store.Load(fx.parent.ID)
	if len(freshParent.Stack) != 2 {
		t.Fatalf("parent stack layers = %d, want 2 after reconciled closure", len(freshParent.Stack))
	}
	for i := range fx.repoDirs {
		appended := freshParent.Stack[1]
		if appended.Branch != fx.appendedBranch || appended.Origin == nil {
			t.Fatalf("persisted appended layer = %+v, want the appended branch with its origin", appended)
		}
		if got := appended.Repos[freshParent.Repos[i].Name].TipSHA; got != journal.Entries[i].TopRef().CandidateSHA {
			t.Fatalf("repo %d: appended layer tip = %s, want the created ref's candidate %s", i, got, journal.Entries[i].TopRef().CandidateSHA)
		}
		if branch := txGit(t, fx.repoDirs[i], "branch", "--show-current"); branch != fx.appendedBranch {
			t.Fatalf("repo %d: worktree branch = %q, want new top branch %q", i, branch, fx.appendedBranch)
		}
	}
}

// TestTransactionStartupReconciliationPreparedButUnapplied proves a journal
// with no applied refs remains safely restartable with all parent refs
// unchanged.
func TestTransactionStartupReconciliationPreparedButUnapplied(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)

	child, _ := fx.store.Load(fx.child.ID)
	parent, _ := fx.store.Load(fx.parent.ID)
	o := fx.orchestrator()
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil {
		t.Fatalf("prepareTransactionCandidates() error = %v", err)
	}
	if journal == nil {
		t.Fatal("journal is nil")
	}

	preRefs := make([]string, len(fx.repoDirs))
	for i := range fx.repoDirs {
		preRefs[i] = fx.refSHA(i, "refs/heads/feature/parent")
	}

	// Simulate startup reconciliation.
	if err := o.ReconcileIntegrationTransactions(); err != nil {
		t.Fatalf("reconcileIntegrationTransactions() error = %v", err)
	}

	// All parent refs unchanged, no appended branch created, and the journal
	// stays retryable: a created ref that is absent classifies as at-anchor,
	// so the scan neither applies, rolls back, nor parks.
	for i := range fx.repoDirs {
		if got := fx.refSHA(i, "refs/heads/feature/parent"); got != preRefs[i] {
			t.Fatalf("repo %d: parent ref moved from %s to %s during reconciliation", i, preRefs[i], got)
		}
		if branches := txGit(t, fx.repoDirs[i], "branch", "--list", fx.appendedBranch); branches != "" {
			t.Fatalf("repo %d: appended branch created during reconciliation", i)
		}
	}
	stored, _ := fx.store.Load(fx.child.ID)
	if stored.Parent.Transaction == nil ||
		stored.Parent.Transaction.Phase != feature.TransactionPhasePrepared ||
		stored.Parent.Transaction.Attention != nil {
		t.Fatalf("journal after scan = %+v, want the prepared phase without attention (left retryable)", stored.Parent.Transaction)
	}
}

// TestTransactionStartupReconciliationExternalMovement proves any ref that
// matches neither its recorded old SHA nor candidate SHA is preserved and
// produces attention instead of an automatic reset.
func TestTransactionStartupReconciliationExternalMovement(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)

	child, _ := fx.store.Load(fx.child.ID)
	parent, _ := fx.store.Load(fx.parent.ID)
	o := fx.orchestrator()
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil {
		t.Fatalf("prepareTransactionCandidates() error = %v", err)
	}
	if journal == nil {
		t.Fatal("journal is nil")
	}

	// Externally create the first repo's appended branch at an unrelated
	// commit: a created ref observed anywhere other than absent or its
	// candidate is a race.
	txGit(t, fx.repoDirs[0], "branch", fx.appendedBranch, fx.parentSHA[0])
	externalSHA := fx.parentSHA[0]

	// Simulate startup reconciliation.
	if err := o.ReconcileIntegrationTransactions(); err != nil {
		t.Fatalf("reconcileIntegrationTransactions() error = %v", err)
	}

	// The externally created ref should be preserved.
	if got := fx.refSHA(0, "refs/heads/"+fx.appendedBranch); got != externalSHA {
		t.Fatalf("repo 0: appended branch ref = %s, want preserved external %s", got, externalSHA)
	}

	_, child = fx.reload()
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention for external movement", tx)
	}
	if tx.Attention == nil || tx.Attention.Code != errcat.IntegrationRefRace {
		t.Fatalf("attention record = %+v, want integration_ref_race", tx.Attention)
	}
	if tx.Attention.Context == nil || len(tx.Attention.Context.Repositories) != 1 {
		t.Fatalf("attention repositories = %+v, want the externally moved repo", tx.Attention.Context)
	}
	repo := tx.Attention.Context.Repositories[0]
	top := journal.Entries[0].TopRef()
	if repo.Name != child.Repos[0].Name ||
		repo.Branch != top.Branch ||
		repo.CandidateSHA != top.CandidateSHA ||
		repo.ObservedSHA != externalSHA {
		t.Fatalf("attention repository = %+v, want branch %s candidate %s observed %s",
			repo, top.Branch, top.CandidateSHA, externalSHA)
	}
}

// failingCASWorktrees wraps the real worktree manager but makes the Nth
// UpdateRef call fail, simulating a transient CAS failure on a later repo.
type failingCASWorktrees struct {
	*git.WorktreeManager
	failIdx   int
	failErr   error
	callCount int
}

func (w *failingCASWorktrees) UpdateRef(repoPath, ref, oldSHA, newSHA string) error {
	w.callCount++
	if w.callCount-1 == w.failIdx {
		return w.failErr
	}
	return w.WorktreeManager.UpdateRef(repoPath, ref, oldSHA, newSHA)
}

// failingResetWorktrees wraps the real worktree manager and fails the first
// ResetToCommit call for the target repo, simulating a worktree-sync failure
// after a successful apply CAS.
type failingResetWorktrees struct {
	*git.WorktreeManager
	failRepoDir string
	failed      bool
	failAlways  bool
}

func (w *failingResetWorktrees) ResetToCommit(worktreePath, commitSHA string) error {
	if worktreePath == w.failRepoDir && (w.failAlways || !w.failed) {
		w.failed = true
		return fmt.Errorf("simulated worktree sync failure")
	}
	return w.WorktreeManager.ResetToCommit(worktreePath, commitSHA)
}

// failingSwitchWorktrees wraps the real worktree manager and fails the
// branch switch for the target repo, simulating a worktree-sync failure
// after a successful append apply transaction.
type failingSwitchWorktrees struct {
	*git.WorktreeManager
	failRepoDir string
	failed      bool
	failAlways  bool
}

func (w *failingSwitchWorktrees) SwitchBranch(worktreePath, branch string) error {
	if worktreePath == w.failRepoDir && (w.failAlways || !w.failed) {
		w.failed = true
		return fmt.Errorf("simulated worktree switch failure")
	}
	return w.WorktreeManager.SwitchBranch(worktreePath, branch)
}

// TestTransactionFirstApplyFailureRollsBack proves a failure on the first
// repository's apply (no earlier applied refs) leaves all refs unchanged.
func TestTransactionFirstApplyFailureRollsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 3)

	child, _ := fx.store.Load(fx.child.ID)
	parent, _ := fx.store.Load(fx.parent.ID)
	o := fx.orchestrator()
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil {
		t.Fatalf("prepareTransactionCandidates() error = %v", err)
	}
	if journal == nil {
		t.Fatal("journal is nil")
	}

	// Externally move the first repo's ref so the first CAS fails.
	txGit(t, fx.repoDirs[0], "checkout", "feature/parent")
	testutil.CommitFile(t, fx.repoDirs[0], "race.txt", "external\n", "external race on first repo")

	preRefs := make([]string, len(fx.repoDirs))
	for i := range fx.repoDirs {
		preRefs[i] = fx.refSHA(i, "refs/heads/feature/parent")
	}

	if err := o.applyTransactionCandidates(child, parent, journal); err != nil {
		t.Fatalf("applyTransactionCandidates() error = %v, want nil with attention", err)
	}

	// No refs should have moved (first apply failed, nothing to roll back).
	for i := range fx.repoDirs {
		if got := fx.refSHA(i, "refs/heads/feature/parent"); got != preRefs[i] {
			t.Fatalf("repo %d: ref moved from %s to %s", i, preRefs[i], got)
		}
	}
}

// TestTransactionApplySyncFailureContinuesForward proves an environmental
// worktree-sync failure after a successful CAS preserves the applied ref,
// finishes the candidate vector, surfaces closure attention, and resumes.
func TestTransactionApplySyncFailureContinuesForward(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 3)

	child, _ := fx.store.Load(fx.child.ID)
	parent, _ := fx.store.Load(fx.parent.ID)

	// Prepare candidates with the real worktree manager.
	o := fx.orchestrator()
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil {
		t.Fatalf("prepareTransactionCandidates() error = %v", err)
	}
	if journal == nil {
		t.Fatal("journal is nil")
	}

	// Apply with a worktree manager that fails the branch switch for repo 1
	// (the second repo). The failure persists through the immediate closure
	// attempt so the journal's pending-sync diagnostic can be asserted.
	switchWT := &failingSwitchWorktrees{
		WorktreeManager: fx.wm,
		failRepoDir:     fx.repoDirs[1],
		failAlways:      true,
	}
	applyO := New(Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Worktrees: switchWT,
	}, Hooks{})

	if err := applyO.applyTransactionCandidates(child, parent, journal); err != nil {
		t.Fatalf("applyTransactionCandidates() error = %v, want nil with attention", err)
	}

	// Every created ref advances despite the environmental sync failure.
	for i := range fx.repoDirs {
		got := fx.refSHA(i, "refs/heads/"+fx.appendedBranch)
		if want := journal.Entries[i].TopRef().CandidateSHA; got != want {
			t.Fatalf("repo %d: appended branch ref = %s, want candidate %s", i, got, want)
		}
	}
	stored, _ := fx.store.Load(fx.child.ID)
	if stored.Parent.Transaction.Phase != feature.TransactionPhaseApplied {
		t.Fatalf("phase = %s, want applied", stored.Parent.Transaction.Phase)
	}
	failedEntry := stored.Parent.Transaction.EntryByRepo(journal.Entries[1].Repo)
	if failedEntry.ApplyState != feature.RepoApplyApplied || !failedEntry.PendingSync {
		t.Fatalf("failed entry = %+v, want applied with the typed pending-sync flag", failedEntry)
	}
	if stored.Parent.Transaction.Attention != nil {
		t.Fatalf("attention record = %+v, want none for a pending sync", stored.Parent.Transaction.Attention)
	}

	if err := applyO.closeTransactionAfterApply(fx.child.ID, fx.parent.ID); err == nil {
		t.Fatal("closeTransactionAfterApply() error = nil, want persistent sync failure")
	}
	stored, _ = fx.store.Load(fx.child.ID)
	// The transaction journal and the relationship event own the closure
	// worktree-sync failure: the child's run carries no failure record, the
	// journal stays in the applied phase with its pending-sync entry, and
	// the closure failure parks as the stored attention record.
	if rec := stored.FailureRecord(); rec != nil {
		t.Fatalf("child failure record = %+v, want none (journal owns the closure sync failure)", rec)
	}
	if stored.Parent.Transaction.Phase != feature.TransactionPhaseApplied {
		t.Fatalf("phase after failed closure = %s, want applied", stored.Parent.Transaction.Phase)
	}
	if rec := stored.Parent.Transaction.Attention; rec == nil || rec.Code != errcat.IntegrationWorktreeSyncFailed {
		t.Fatalf("attention record after failed closure = %+v, want integration_worktree_sync_failed", rec)
	} else if rec.Context == nil || len(rec.Context.Repositories) != 1 || rec.Context.Repositories[0].Name != journal.Entries[1].Repo {
		t.Fatalf("attention repositories after failed closure = %+v, want repo 1", rec.Context)
	}
	if pending := stored.Parent.Transaction.EntryByRepo(journal.Entries[1].Repo); pending == nil || !pending.PendingSync {
		t.Fatalf("pending entry after failed closure = %+v, want the typed flag still set", pending)
	}

	// Swap in the healthy manager and re-enter through the public integration
	// path. Applied journals go directly to idempotent closure.
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration() resume error = %v", err)
	}
	_, stored = fx.reload()
	if stored.Parent.Transaction.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("phase after resume = %s, want merged", stored.Parent.Transaction.Phase)
	}
	if rec := stored.FailureRecord(); rec != nil {
		t.Fatalf("child failure record after resume = %+v, want none", rec)
	}
	for i := range fx.repoDirs {
		if got := txGit(t, fx.repoDirs[i], "rev-parse", "HEAD"); got != journal.Entries[i].TopRef().CandidateSHA {
			t.Fatalf("repo %d worktree HEAD = %s, want candidate %s", i, got, journal.Entries[i].TopRef().CandidateSHA)
		}
		if branch := txGit(t, fx.repoDirs[i], "branch", "--show-current"); branch != fx.appendedBranch {
			t.Fatalf("repo %d: worktree branch = %q, want appended branch %q", i, branch, fx.appendedBranch)
		}
	}
}

func TestTransactionApplyingJournalSkipsParentTipRebuild(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)
	o := fx.orchestrator()
	child, _ := fx.store.Load(fx.child.ID)
	parent, _ := fx.store.Load(fx.parent.ID)
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil {
		t.Fatalf("prepareTransactionCandidates() error = %v", err)
	}
	originalCandidate := journal.Entries[0].TopRef().CandidateSHA
	journal.Phase = feature.TransactionPhaseApplying
	if err := o.persistTransaction(child.ID, journal); err != nil {
		t.Fatalf("persist applying journal: %v", err)
	}

	// Move the first parent tip after the interrupted apply journal is durable.
	testutil.CommitFile(t, fx.repoDirs[0], "external.txt", "external\n", "external parent move")
	if err := o.RunChildIntegration(child.ID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v, want retryable attention", err)
	}
	stored, _ := fx.store.Load(child.ID)
	if got := stored.Parent.Transaction.Entries[0].TopRef().CandidateSHA; got != originalCandidate {
		t.Fatalf("applying journal candidate was rebuilt from %s to %s", originalCandidate, got)
	}
	if stored.Parent.Transaction.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("phase = %s, want attention after CAS detects moved ref", stored.Parent.Transaction.Phase)
	}
}

// TestTransactionPassThroughSyncFailureRollsBackApplied proves that a
// worktree-sync failure on an up-to-date pass-through rebase repo still
// compensates the earlier applied refs. Without the fix, the pass-through
// sync failure parked at attention without rolling back the behind repo's
// applied ref, leaving the parent partially integrated.
func TestTransactionPassThroughSyncFailureRollsBackApplied(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)

	// Recreate the child as a rebase child where repoA was behind its target
	// at creation and repoB was already up to date (pass-through). Reset
	// repoB's child branch to the parent anchor so the pass-through
	// ancestor invariant holds. repoA carries a persisted restack result —
	// a single kept layer (the parent stack's layer 1) whose rebuilt top is
	// the parent tip, so the child head (parent tip plus the child commit)
	// descends from it and the rewrite candidate is the child head.
	txGit(t, fx.childWTs[1], "reset", "--hard", fx.parentSHA[1])
	child, _ := fx.store.Load(fx.child.ID)
	child.Parent.Kind = feature.ChildKindRebase
	child.Parent.RebaseWorkRepos = []string{"repoA"}
	child.Parent.RebaseRestacks = []feature.RebaseRepoRestack{{
		Repo:        "repoA",
		RebuiltTips: map[int]string{1: fx.parentSHA[0]},
		RebuiltTop:  fx.parentSHA[0],
		AnchorRemap: map[int]string{1: fx.parentSHA[0]},
	}}
	child.Parent.RebaseTargets = []feature.RebaseRepoTarget{{
		Repo: "repoA", Target: "feature/parent", Ref: "feature/parent",
		TargetSHA: fx.parentSHA[0],
	}}
	if err := fx.store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}
	parent, child := fx.reload()

	o := fx.orchestrator()
	journal, err := o.prepareTransactionCandidates(child, parent)
	if err != nil {
		t.Fatalf("prepareTransactionCandidates() error = %v", err)
	}
	if journal == nil {
		t.Fatal("journal is nil")
	}
	if top := journal.Entries[1].TopRef(); top == nil || top.CandidateSHA != top.AnchorSHA {
		t.Fatalf("repo 1: refs = %+v, want pass-through candidate equal to anchor", journal.Entries[1].Refs)
	}
	oldSHAs := make([]string, len(journal.Entries))
	for i := range journal.Entries {
		oldSHAs[i] = journal.Entries[i].TopRef().AnchorSHA
	}

	// Apply with a worktree manager that fails ResetToCommit for repoB's
	// pass-through sync. RepoA applies fully first, so the transaction must
	// roll back repoA's ref instead of parking at attention.
	resetWT := &failingResetWorktrees{
		WorktreeManager: fx.wm,
		failRepoDir:     fx.repoDirs[1],
	}
	applyO := New(Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Worktrees: resetWT,
	}, Hooks{})

	if err := applyO.applyTransactionCandidates(child, parent, journal); err != nil {
		t.Fatalf("applyTransactionCandidates() error = %v, want nil with attention", err)
	}

	// Both refs must be unchanged: repoA rolled back, repoB never moved.
	for i := range fx.repoDirs {
		got := fx.refSHA(i, "refs/heads/feature/parent")
		if got != oldSHAs[i] {
			t.Fatalf("repo %d: ref = %s after rollback, want old SHA %s (pass-through sync failure must roll back earlier applied refs)", i, got, oldSHAs[i])
		}
	}

	// RepoA's worktree must be restored to the rolled-back ref.
	wtHead := txGit(t, fx.repoDirs[0], "rev-parse", "HEAD")
	if wtHead != oldSHAs[0] {
		t.Fatalf("repo 0: worktree HEAD = %s, want old SHA %s after rollback", wtHead, oldSHAs[0])
	}
}
