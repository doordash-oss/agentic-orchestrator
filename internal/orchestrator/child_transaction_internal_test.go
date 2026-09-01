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
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// multiRepoTransactionFixture builds a real multi-repository parent/child pair
// with the specified number of repositories. Each repository has its own real
// git repo, a parent branch checked out, and a child worktree with committed
// child changes.
type multiRepoTransactionFixture struct {
	t           *testing.T
	repoDirs    []string
	parentSHA   []string
	store       *feature.Store
	mgr         *feature.Manager
	wm          *git.WorktreeManager
	parent      *feature.Feature
	child       *feature.Feature
	childWTs    []string
	childBranch string
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

	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	parent := &feature.Feature{
		ID:            "parent-tx",
		Name:          "Parent TX",
		Slug:          "parent-tx",
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now(),
		ActiveRun:     1,
		RunCount:      1,
		Checkpoints:   feature.Checkpoints{ManualPublish: true},
		Repos:         parentRepos,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	parent.RepoStates = make(map[string]*feature.RepoState, numRepos)
	for _, pr := range parentRepos {
		parent.RepoStates[pr.Name] = &feature.RepoState{Touched: true}
	}
	child := &feature.Feature{
		ID:            "child-tx",
		Name:          "Child TX",
		Slug:          "child-tx",
		Status:        feature.StatusReviewPassed,
		CurrentPhase:  feature.PhaseFinalReview,
		Pipeline:      feature.PipelineMedium,
		Created:       time.Now(),
		ActiveRun:     1,
		RunCount:      1,
		Repos:         childRepos,
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

	// Every parent branch has an explicit two-parent merge commit.
	for i, dir := range fx.repoDirs {
		parents := txGit(t, dir, "rev-list", "--parents", "-n", "1", "HEAD")
		if fields := len(strings.Fields(parents)); fields != 3 {
			t.Fatalf("repo %d: merge parents = %q, want two-parent merge commit", i, parents)
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
// repositories, verifying explicit merge-parent ordering.
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
	for i, dir := range fx.repoDirs {
		parents := txGit(t, dir, "rev-list", "--parents", "-n", "1", "HEAD")
		fields := strings.Fields(parents)
		if len(fields) != 3 {
			t.Fatalf("repo %d: merge parents = %q, want two-parent merge commit", i, parents)
		}
		// First parent is the parent tip, second is the child head.
		journal := child.Parent.Transaction
		entry := journal.EntryByRepo(child.Repos[i].Name)
		if entry == nil {
			t.Fatalf("repo %d: journal entry missing", i)
		}
		if fields[1] != entry.ParentAnchorSHA {
			t.Fatalf("repo %d: first parent = %s, want anchor %s", i, fields[1], entry.ParentAnchorSHA)
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
	// Both dirty repos should have diagnostics.
	dirtyCount := 0
	for i := range tx.Entries {
		if len(tx.Entries[i].Dirty) > 0 {
			dirtyCount++
		}
	}
	if dirtyCount < 2 {
		t.Fatalf("dirty diagnostics count = %d, want at least 2 (aggregated)", dirtyCount)
	}
}

// TestTransactionPreparationFailureLeavesRefsUnchanged proves a conflict or
// operational failure during preparation leaves every parent ref unchanged.
func TestTransactionPreparationFailureLeavesRefsUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoTransactionFixture(t, 2)
	o := fx.orchestrator()

	// Create a conflicting change on the second repo's parent branch and pin
	// the persisted base to the new tip so the drift gate does not fire and
	// the merge-conflict path is exercised.
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
	// The second repo should have conflict files.
	entry := tx.EntryByRepo(child.Repos[1].Name)
	if entry == nil || len(entry.ConflictFiles) == 0 {
		t.Fatalf("repo 1: conflict files missing, entry = %+v", entry)
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
	if entry == nil || entry.GateCode != feature.GateCodeParentDrift {
		t.Fatalf("repo 0: gate code = %+v, want %s", entry, feature.GateCodeParentDrift)
	}
	if entry.PrepState != feature.RepoPrepFailed {
		t.Fatalf("repo 0: prep state = %s, want failed", entry.PrepState)
	}
	for i := range tx.Entries {
		if tx.Entries[i].CandidateSHA != "" {
			t.Fatalf("repo %d: candidate %s staged despite drift", i, tx.Entries[i].CandidateSHA)
		}
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
		entry := &tx.Entries[i]
		drifted := i == 0 || i == 2
		if drifted && entry.GateCode != feature.GateCodeParentDrift {
			t.Fatalf("repo %d: gate code = %q, want %s", i, entry.GateCode, feature.GateCodeParentDrift)
		}
		if !drifted && entry.GateCode != "" {
			t.Fatalf("repo %d: gate code = %q, want empty for clean repo", i, entry.GateCode)
		}
		if entry.CandidateSHA != "" {
			t.Fatalf("repo %d: candidate %s staged despite drift", i, entry.CandidateSHA)
		}
	}
}

// TestTransactionParentDriftExemptsPriorCandidate proves a resumed rebuild
// after a partial apply — where a parent tip equals a candidate the
// transaction itself produced — is not mistaken for external drift and the
// integration completes.
func TestTransactionParentDriftExemptsPriorCandidate(t *testing.T) {
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

	// Manually apply only the first repo (simulating a crash after its ref
	// CAS and worktree sync but before durable apply progress).
	entry := &journal.Entries[0]
	ref := "refs/heads/" + entry.ParentBranch
	if err := git.UpdateRefCAS(fx.repoDirs[0], ref, entry.ExpectedRefSHA, entry.CandidateSHA); err != nil {
		t.Fatalf("manual apply repo 0: %v", err)
	}
	txGit(t, fx.repoDirs[0], "reset", "--hard", entry.CandidateSHA)

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration() resume error = %v", err)
	}

	_, child = fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed on resume", child.Parent.CloseOutcome)
	}
	if child.Parent.Transaction.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %s, want merged", child.Parent.Transaction.Phase)
	}
	for i := range child.Parent.Transaction.Entries {
		if got := child.Parent.Transaction.Entries[i].GateCode; got == feature.GateCodeParentDrift {
			t.Fatalf("repo %d: resume tripped the drift gate", i)
		}
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

	// Record old SHAs for rollback verification.
	oldSHAs := make([]string, len(journal.Entries))
	for i := range journal.Entries {
		oldSHAs[i] = journal.Entries[i].ParentAnchorSHA
	}

	// Externally move the third repo's ref to simulate a CAS failure.
	// This will cause the apply of repo 2 to fail, triggering rollback
	// of repos 0 and 1.
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
	// Repos 0 and 1 should be rolled back (ref back at old SHA).
	for i := 0; i < 2; i++ {
		got := fx.refSHA(i, "refs/heads/feature/parent")
		if got != oldSHAs[i] {
			t.Fatalf("repo %d: ref = %s after rollback, want old SHA %s", i, got, oldSHAs[i])
		}
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
	// but before closure).
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		ref := "refs/heads/" + entry.ParentBranch
		if err := git.UpdateRefCAS(fx.repoDirs[i], ref, entry.ExpectedRefSHA, entry.CandidateSHA); err != nil {
			t.Fatalf("manual apply repo %d: %v", i, err)
		}
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

	// All parent refs unchanged.
	for i := range fx.repoDirs {
		if got := fx.refSHA(i, "refs/heads/feature/parent"); got != preRefs[i] {
			t.Fatalf("repo %d: parent ref moved from %s to %s during reconciliation", i, preRefs[i], got)
		}
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

	// Externally move the first repo's parent branch to an unrelated commit.
	txGit(t, fx.repoDirs[0], "checkout", "feature/parent")
	testutil.CommitFile(t, fx.repoDirs[0], "external.txt", "external\n", "external movement")
	externalSHA := fx.refSHA(0, "refs/heads/feature/parent")

	// Simulate startup reconciliation.
	if err := o.ReconcileIntegrationTransactions(); err != nil {
		t.Fatalf("reconcileIntegrationTransactions() error = %v", err)
	}

	// The externally moved ref should be preserved.
	if got := fx.refSHA(0, "refs/heads/feature/parent"); got != externalSHA {
		t.Fatalf("repo 0: ref = %s, want preserved external %s", got, externalSHA)
	}

	_, child = fx.reload()
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention for external movement", tx)
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

	// Apply with a worktree manager that fails ResetToCommit for repo 1
	// (the second repo). The failure persists through the immediate closure
	// attempt so its durable LastError can be asserted.
	resetWT := &failingResetWorktrees{
		WorktreeManager: fx.wm,
		failRepoDir:     fx.repoDirs[1],
		failAlways:      true,
	}
	applyO := New(Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Worktrees: resetWT,
	}, Hooks{})

	if err := applyO.applyTransactionCandidates(child, parent, journal); err != nil {
		t.Fatalf("applyTransactionCandidates() error = %v, want nil with attention", err)
	}

	// Every ref advances despite the environmental sync failure.
	for i := range fx.repoDirs {
		got := fx.refSHA(i, "refs/heads/feature/parent")
		if got != journal.Entries[i].CandidateSHA {
			t.Fatalf("repo %d: ref = %s, want candidate %s", i, got, journal.Entries[i].CandidateSHA)
		}
	}
	stored, _ := fx.store.Load(fx.child.ID)
	if stored.Parent.Transaction.Phase != feature.TransactionPhaseApplied {
		t.Fatalf("phase = %s, want applied", stored.Parent.Transaction.Phase)
	}
	failedEntry := stored.Parent.Transaction.EntryByRepo(journal.Entries[1].Repo)
	if failedEntry.ApplyState != feature.RepoApplyApplied || !strings.Contains(failedEntry.Diagnostics, "worktree sync pending after apply") {
		t.Fatalf("failed entry = %+v, want applied with pending-sync diagnostics", failedEntry)
	}

	if err := applyO.closeTransactionAfterApply(fx.child.ID, fx.parent.ID); err == nil {
		t.Fatal("closeTransactionAfterApply() error = nil, want persistent sync failure")
	}
	stored, _ = fx.store.Load(fx.child.ID)
	if !strings.Contains(stored.LastError, "syncing parent worktree for repo") {
		t.Fatalf("LastError = %q, want closure sync diagnostic", stored.LastError)
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
	if stored.LastError != "" {
		t.Fatalf("LastError after resume = %q, want cleared", stored.LastError)
	}
	for i := range fx.repoDirs {
		if got := txGit(t, fx.repoDirs[i], "rev-parse", "HEAD"); got != journal.Entries[i].CandidateSHA {
			t.Fatalf("repo %d worktree HEAD = %s, want candidate %s", i, got, journal.Entries[i].CandidateSHA)
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
	originalCandidate := journal.Entries[0].CandidateSHA
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
	if stored.Parent.Transaction.Entries[0].CandidateSHA != originalCandidate {
		t.Fatalf("applying journal candidate was rebuilt from %s to %s", originalCandidate, stored.Parent.Transaction.Entries[0].CandidateSHA)
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
	// ancestor invariant holds.
	txGit(t, fx.childWTs[1], "reset", "--hard", fx.parentSHA[1])
	child, _ := fx.store.Load(fx.child.ID)
	child.Parent.Kind = feature.ChildKindRebase
	child.Parent.RebaseBehind = []string{"repoA"}
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
	if got := journal.Entries[1].CandidateSHA; got != journal.Entries[1].ParentAnchorSHA {
		t.Fatalf("repo 1: CandidateSHA = %s, want pass-through anchor %s", got, journal.Entries[1].ParentAnchorSHA)
	}
	oldSHAs := make([]string, len(journal.Entries))
	for i := range journal.Entries {
		oldSHAs[i] = journal.Entries[i].ParentAnchorSHA
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
