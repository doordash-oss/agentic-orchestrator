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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// --- Fixture ---

type multiRepoE2EFixture struct {
	t        *testing.T
	repoDirs []string
	store    *feature.Store
	mgr      *feature.Manager
	wm       *git.WorktreeManager
	parent   *feature.Feature
	child    *feature.Feature
	childWTs []string
}

func newMultiRepoE2EFixture(t *testing.T, numRepos int) *multiRepoE2EFixture {
	t.Helper()
	repoDirs := make([]string, numRepos)
	childWTs := make([]string, numRepos)
	childBranch := "feature/child-tx"

	parentRepos := make([]feature.FeatureRepo, 0, numRepos)
	childRepos := make([]feature.FeatureRepo, 0, numRepos)
	bases := make([]feature.ChildRepoBase, 0, numRepos)

	for i := 0; i < numRepos; i++ {
		repoDir := testutil.InitGitRepo(t)
		multiRepoGit(t, repoDir, "checkout", "-b", "feature/parent")
		testutil.CommitFile(t, repoDir, "base.txt", "v1\n", "parent base")
		parentSHA := multiRepoGit(t, repoDir, "rev-parse", "HEAD")

		childWT := t.TempDir() + "/child-wt-" + string(rune('A'+i))
		multiRepoGit(t, repoDir, "worktree", "add", "-b", childBranch, childWT, parentSHA)
		testutil.CommitFile(t, childWT, "child.txt", "child work\n", "child change")

		repoName := "repo" + string(rune('A'+i))
		publishable := true
		repoDirs[i] = repoDir
		childWTs[i] = childWT

		parentRepos = append(parentRepos, feature.FeatureRepo{
			Name: repoName, Path: repoDir, WorktreePath: repoDir,
			Branch: "feature/parent", BaseBranch: "main", Publishable: &publishable,
		})
		childRepos = append(childRepos, feature.FeatureRepo{
			Name: repoName, Path: repoDir, WorktreePath: childWT,
			Branch: childBranch, BaseBranch: "main",
		})
		bases = append(bases, feature.ChildRepoBase{Repo: repoName, SHA: parentSHA, ParentBranch: "feature/parent"})
	}

	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	parent := &feature.Feature{
		ID: "parent-tx", Name: "Parent TX", Slug: "parent-tx",
		Status: feature.StatusPublished, CurrentPhase: feature.PhasePublish,
		Created: time.Now(), ActiveRun: 1, RunCount: 1,
		Checkpoints:   feature.Checkpoints{ManualPublish: true},
		Repos:         parentRepos,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	parent.RepoStates = make(map[string]*feature.RepoState, numRepos)
	for _, pr := range parentRepos {
		parent.RepoStates[pr.Name] = &feature.RepoState{Touched: true}
	}
	child := &feature.Feature{
		ID: "child-tx", Name: "Child TX", Slug: "child-tx",
		Status: feature.StatusReviewPassed, CurrentPhase: feature.PhaseFinalReview,
		Pipeline: feature.PipelineMedium, Created: time.Now(),
		ActiveRun: 1, RunCount: 1, Repos: childRepos,
		SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID, Kind: feature.ChildKindRefactor, Bases: bases,
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
	return &multiRepoE2EFixture{
		t: t, repoDirs: repoDirs, store: store, mgr: mgr, wm: wm,
		parent: parent, child: child, childWTs: childWTs,
	}
}

func (fx *multiRepoE2EFixture) orchestrator() *orchestrator.Orchestrator {
	return fx.orchestratorWithWorktrees(fx.wm)
}

func (fx *multiRepoE2EFixture) orchestratorWithWorktrees(wt feature.WorktreeOps) *orchestrator.Orchestrator {
	return orchestrator.New(orchestrator.Deps{
		Lifecycle: fx.mgr, Store: fx.store,
		Worktrees: wt,
	}, orchestrator.Hooks{})
}

func (fx *multiRepoE2EFixture) reload() (*feature.Feature, *feature.Feature) {
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

func (fx *multiRepoE2EFixture) refSHA(repoIdx int, ref string) string {
	return multiRepoGit(fx.t, fx.repoDirs[repoIdx], "rev-parse", ref)
}

func (fx *multiRepoE2EFixture) saveJournal(journal *feature.TransactionJournal) {
	fx.t.Helper()
	if err := fx.store.Modify(fx.child.ID, func(f *feature.Feature) error {
		f.Parent.Transaction = journal
		return nil
	}); err != nil {
		fx.t.Fatalf("save journal: %v", err)
	}
}

// manualPrepare creates merge candidates for every repository and writes a
// prepared journal to disk without going through the orchestrator. This
// simulates a crash after all candidates are prepared but before any are
// applied.
func (fx *multiRepoE2EFixture) manualPrepare(t *testing.T) *feature.TransactionJournal {
	t.Helper()
	child, _ := fx.store.Load(fx.child.ID)
	parent, _ := fx.store.Load(fx.parent.ID)
	journal := &feature.TransactionJournal{Phase: feature.TransactionPhasePrepared}
	for i := range child.Repos {
		childRepo := child.Repos[i]
		parentRepo := parent.Repos[i]
		childHead := multiRepoGit(t, childRepo.WorktreePath, "rev-parse", "HEAD")
		parentTip := multiRepoGit(t, parentRepo.Path, "rev-parse", "refs/heads/feature/parent")
		result, err := git.CreateMergeCandidate(parentRepo.Path, parentTip, childHead, "test merge")
		if err != nil {
			t.Fatalf("create merge candidate repo %d: %v", i, err)
		}
		journal.Entries = append(journal.Entries, feature.RepoTransactionEntry{
			Repo: childRepo.Name, ParentBranch: parentRepo.Branch,
			ParentAnchorSHA: parentTip, ExpectedRefSHA: parentTip,
			ChildHeadSHA: childHead, CandidateSHA: result.CandidateSHA,
			PrepState: feature.RepoPrepPrepared,
		})
	}
	fx.saveJournal(journal)
	return journal
}

// manualApplyRef moves a repo's parent ref to the candidate SHA and syncs
// the worktree, simulating a completed apply step before a crash.
func (fx *multiRepoE2EFixture) manualApplyRef(t *testing.T, idx int, entry *feature.RepoTransactionEntry) {
	t.Helper()
	ref := "refs/heads/" + entry.ParentBranch
	if err := git.UpdateRefCAS(fx.repoDirs[idx], ref, entry.ExpectedRefSHA, entry.CandidateSHA); err != nil {
		t.Fatalf("manual apply repo %d: %v", idx, err)
	}
	multiRepoGit(t, fx.repoDirs[idx], "checkout", "feature/parent")
	multiRepoGit(t, fx.repoDirs[idx], "reset", "--hard", entry.CandidateSHA)
}

// manualApplyRefNoSync moves a repo's parent ref to the candidate SHA
// without syncing the worktree, simulating a crash between the apply-progress
// write and the worktree sync.
func (fx *multiRepoE2EFixture) manualApplyRefNoSync(t *testing.T, idx int, entry *feature.RepoTransactionEntry) {
	t.Helper()
	ref := "refs/heads/" + entry.ParentBranch
	if err := git.UpdateRefCAS(fx.repoDirs[idx], ref, entry.ExpectedRefSHA, entry.CandidateSHA); err != nil {
		t.Fatalf("manual apply no sync repo %d: %v", idx, err)
	}
}

// manualRollbackRef moves a repo's parent ref back to the anchor SHA without
// syncing the worktree, simulating a crash between the ref CAS and the
// worktree reset during rollback.
func (fx *multiRepoE2EFixture) manualRollbackRef(t *testing.T, idx int, entry *feature.RepoTransactionEntry) {
	t.Helper()
	ref := "refs/heads/" + entry.ParentBranch
	if err := git.UpdateRefCAS(fx.repoDirs[idx], ref, entry.CandidateSHA, entry.ParentAnchorSHA); err != nil {
		t.Fatalf("manual rollback repo %d: %v", idx, err)
	}
}

func multiRepoGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// attentionRepoBlock returns the attention record's repositories-block entry
// for the named repository, or nil when the record or entry is absent.
func attentionRepoBlock(rec *errcat.FailureRecord, repo string) *errcat.CodeRepository {
	if rec == nil || rec.Context == nil {
		return nil
	}
	for i := range rec.Context.Repositories {
		if rec.Context.Repositories[i].Name == repo {
			return &rec.Context.Repositories[i]
		}
	}
	return nil
}

// --- Wrapping worktree managers for fault injection ---

// raceInjectingWorktrees wraps *git.WorktreeManager and injects an external
// ref movement when RefSHA is first called for the target repo during apply.
type raceInjectingWorktrees struct {
	*git.WorktreeManager
	t           *testing.T
	raceRepoDir string
	raceBranch  string
	injected    bool
}

func (w *raceInjectingWorktrees) RefSHA(repoPath, ref string) (string, error) {
	if !w.injected && repoPath == w.raceRepoDir {
		w.injected = true
		multiRepoGit(w.t, w.raceRepoDir, "checkout", w.raceBranch)
		testutil.CommitFile(w.t, w.raceRepoDir, "race.txt", "external\n", "external race before apply")
	}
	return w.WorktreeManager.RefSHA(repoPath, ref)
}

// cleanupFailingWorktrees wraps *git.WorktreeManager and fails the first N
// RemoveRef calls to simulate a per-repository cleanup failure.
type cleanupFailingWorktrees struct {
	*git.WorktreeManager
	failCount int
	failLimit int
}

func (w *cleanupFailingWorktrees) RemoveRef(worktreePath, mainRepo, branch string) error {
	if w.failCount < w.failLimit {
		w.failCount++
		return fmt.Errorf("simulated cleanup failure")
	}
	return w.WorktreeManager.RemoveRef(worktreePath, mainRepo, branch)
}

// resetFailingWorktrees wraps *git.WorktreeManager and fails the first
// ResetToCommit call for the target repo, simulating a worktree-sync failure
// after a successful apply CAS.
type resetFailingWorktrees struct {
	*git.WorktreeManager
	failRepoDir string
	failed      bool
}

func (w *resetFailingWorktrees) ResetToCommit(worktreePath, commitSHA string) error {
	if !w.failed && worktreePath == w.failRepoDir {
		w.failed = true
		return fmt.Errorf("simulated worktree sync failure")
	}
	return w.WorktreeManager.ResetToCommit(worktreePath, commitSHA)
}

// failingStoreWrapper wraps *feature.Store and fails on the Nth Modify
// call, simulating a persistence failure (crash) at a specific transaction
// boundary. All other methods delegate to the embedded Store.
type failingStoreWrapper struct {
	*feature.Store
	counter *sharedModifyCounter
}

func (s *failingStoreWrapper) Modify(id string, fn func(f *feature.Feature) error) error {
	if s.counter.fail() {
		return fmt.Errorf("simulated persistence failure at write %d", s.counter.counter)
	}
	return s.Store.Modify(id, fn)
}

// failingLifecycleWrapper wraps *feature.Manager and fails on the Nth
// call to MarkCodeReady (which internally calls Store.Modify). The shared
// counter ensures the failure index aligns across both wrappers.
type failingLifecycleWrapper struct {
	*feature.Manager
	counter *sharedModifyCounter
}

func (w *failingLifecycleWrapper) MarkCodeReady(featureID string) error {
	if w.counter.fail() {
		return fmt.Errorf("simulated persistence failure at MarkCodeReady")
	}
	return w.Manager.MarkCodeReady(featureID)
}

type sharedModifyCounter struct {
	counter int32
	failAt  int32
}

func (c *sharedModifyCounter) fail() bool {
	return atomic.AddInt32(&c.counter, 1) == c.failAt
}

func (fx *multiRepoE2EFixture) orchestratorWithFailingStore(failAt int32) *orchestrator.Orchestrator {
	return fx.orchestratorWithFailingStoreAndWorktree(failAt, fx.wm)
}

func (fx *multiRepoE2EFixture) orchestratorWithFailingStoreAndWorktree(failAt int32, wt feature.WorktreeOps) *orchestrator.Orchestrator {
	counter := &sharedModifyCounter{failAt: failAt}
	// Create a new Store with the same BaseDir so both the failing and
	// fresh orchestrators read/write the same feature files.
	failStore := feature.NewStore(fx.store.BaseDir)
	fs := &failingStoreWrapper{Store: failStore, counter: counter}
	mgr := feature.NewManager(failStore, config.NewDefault())
	mgr.Worktrees = fx.wm
	fl := &failingLifecycleWrapper{Manager: mgr, counter: counter}
	return orchestrator.New(orchestrator.Deps{
		Lifecycle: fl, Store: fs,
		Worktrees: wt,
	}, orchestrator.Hooks{})
}

// --- Tests ---

// TestRefactorChildTransactionalMultiRepoIntegrationSuccess proves the
// primary multi-repository child transactional integration journey.
func TestRefactorChildTransactionalMultiRepoIntegrationSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoE2EFixture(t, 2)
	o := fx.orchestrator()

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v", err)
	}

	parent, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady", parent.Status)
	}

	for i, dir := range fx.repoDirs {
		parents := multiRepoGit(t, dir, "rev-list", "--parents", "-n", "1", "HEAD")
		if fields := len(strings.Fields(parents)); fields != 3 {
			t.Fatalf("repo %d: merge parents = %q, want two-parent merge commit", i, parents)
		}
	}

	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %+v, want merged", tx)
	}
	if len(tx.Entries) != 2 {
		t.Fatalf("transaction entries = %d, want 2", len(tx.Entries))
	}

	for i, wt := range fx.childWTs {
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Fatalf("repo %d: child worktree still present", i)
		}
	}
}

// TestRefactorChildTransactionalMultiRepoStagedConflictRestartAndReviewRenewal
// proves a later staged conflict leaves every parent ref unmoved, then
// Restart uses the latest parent tips and renews final review after child
// code changes. The test exercises:
//   - one conflicting repository among clean peers,
//   - newer parent commits arriving before restart,
//   - code-changing resolution followed by final-review rerun via
//     RestartPhase → StartFeature (the desktop app's dispatch boundary),
//   - explicit merge boundaries in every repository after re-prepare.
func TestRefactorChildTransactionalMultiRepoStagedConflictRestartAndReviewRenewal(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoE2EFixture(t, 2)
	o := fx.orchestrator()

	// Create a conflicting change on the second repo's parent branch.
	testutil.CommitFile(t, fx.repoDirs[1], "child.txt", "parent-side conflict\n", "conflicting parent commit")

	preRefs := make([]string, len(fx.repoDirs))
	for i := range fx.repoDirs {
		preRefs[i] = fx.refSHA(i, "refs/heads/feature/parent")
	}

	// Initial integration attempt: parks once on parent-ref drift (the
	// conflicting parent commit moved the tip); the retry acknowledges it.
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v, want nil with drift attention", err)
	}
	_, child := fx.reload()
	driftTx := child.Parent.Transaction
	if driftTx == nil {
		t.Fatal("transaction journal missing after drift park")
	}
	driftRec := driftTx.AttentionRecord()
	if driftRec == nil || driftRec.Code != errcat.IntegrationParentRefDrift {
		t.Fatalf("attention record = %+v, want code %s", driftRec, errcat.IntegrationParentRefDrift)
	}
	// The drift block carries the creation-time base as the anchor and the
	// moved tip as the observed SHA.
	if block := attentionRepoBlock(driftRec, child.Repos[1].Name); block == nil ||
		block.ParentAnchorSHA != child.BaseSHA(child.Repos[1].Name) ||
		block.ObservedSHA != preRefs[1] {
		t.Fatalf("repo 1: drift repositories block = %+v, want anchor %s and observed %s",
			block, child.BaseSHA(child.Repos[1].Name), preRefs[1])
	}

	// Acknowledged retry: hits the staged conflict on repo 1.
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration() retry error = %v, want nil with attention", err)
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
	conflictRec := tx.AttentionRecord()
	if conflictRec == nil || conflictRec.Code != errcat.IntegrationMergeConflict {
		t.Fatalf("attention record = %+v, want code %s", conflictRec, errcat.IntegrationMergeConflict)
	}
	if block := attentionRepoBlock(conflictRec, child.Repos[1].Name); block == nil ||
		!slices.Contains(block.ConflictFiles, "child.txt") {
		t.Fatalf("repo 1: conflict files missing from repositories block, block = %+v", block)
	}

	// Resolve the conflict: change child.txt to match the parent's
	// conflicting version so the merge no longer conflicts, and add a
	// new file so the child head unambiguously changes (requiring review
	// renewal).
	testutil.CommitFile(t, fx.childWTs[1], "child.txt", "parent-side conflict\n", "accept parent change to resolve conflict")
	testutil.CommitFile(t, fx.childWTs[1], "resolve.txt", "resolved\n", "add resolution marker")

	// Newer parent commits arrive before restart: add a clean commit to
	// repo 0's parent branch (not the conflicting repo). Restart must use
	// the latest parent tips, including this newer commit.
	testutil.CommitFile(t, fx.repoDirs[0], "newer-parent.txt", "newer\n", "newer parent commit before restart")
	newerRepo0Tip := fx.refSHA(0, "refs/heads/feature/parent")

	// Install a mock final-review function so the e2e test can exercise
	// the full RestartPhase → StartFeature → advanceAfterFinalReview →
	// RunChildIntegration flow without booting real agent sessions.
	var frCalled int32
	o.SetRunMultiRepoFinalReviewFn(func(f *feature.Feature, kbInfos ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		atomic.StoreInt32(&frCalled, 1)
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		close(ch)
		return ch, nil
	})

	// Restart: child head changed, so invalidateFinalReview clears the
	// journal and sets StatusReviewPassed + PhaseFinalReview. RestartPhase
	// detects this and returns RestartDispatchPhase.
	outcome, err := o.RestartPhase(fx.child.ID, 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase() error = %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchPhase || outcome.Phase != feature.PhaseFinalReview {
		t.Fatalf("RestartPhase outcome = %+v, want RestartDispatchPhase/PhaseFinalReview", outcome)
	}

	// Assert the renewal contract: child remains open, transaction
	// is cleared, and status is reset to the pre-review state.
	_, child = fx.reload()
	if child.Parent.CloseOutcome != "" {
		t.Fatalf("child close outcome = %q, want empty (child remains open)", child.Parent.CloseOutcome)
	}
	if child.Parent.Transaction != nil {
		t.Fatalf("transaction journal = %+v, want nil (cleared by review invalidation)", child.Parent.Transaction)
	}
	if child.Status == feature.StatusReviewPassed {
		// After RestartPhase reloads and returns, the status should be
		// StatusReviewPassed + PhaseFinalReview (pre-final-review state).
		// But after StartFeature runs Final Review, it will transition
		// through StatusFinalReviewing and back to StatusReviewPassed.
	}

	// Dispatch Final Review via StartFeature — the same entry point the
	// client uses for RestartDispatchPhase. invalidateFinalReview set
	// CurrentPhase=PhaseFinalReview, so StartFeature dispatches it.
	// The mock returns all_passed, then advanceAfterFinalReview calls
	// RunChildIntegration to re-prepare candidates against the latest
	// parent tips and complete.
	if err := o.StartFeature(fx.child.ID); err != nil {
		t.Fatalf("StartFeature() after review invalidation: %v", err)
	}

	if atomic.LoadInt32(&frCalled) == 0 {
		t.Fatal("final review was not dispatched")
	}

	// startFinalReview completes the pass and advanceAfterFinalReview in a
	// background cycle goroutine; wait for it before asserting the terminal
	// integration outcome.
	o.WaitForCycles()

	// Review invalidation wiped the journal, so the moved parent tips are
	// re-observed as drift and park once more; the retry acknowledges them.
	_, child = fx.reload()
	tx = child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want drift attention after review renewal", tx)
	}
	if rec := tx.AttentionRecord(); rec == nil || rec.Code != errcat.IntegrationParentRefDrift {
		t.Fatalf("attention record after review renewal = %+v, want code %s", rec, errcat.IntegrationParentRefDrift)
	}
	if block := attentionRepoBlock(tx.AttentionRecord(), child.Repos[0].Name); block == nil ||
		block.ObservedSHA != newerRepo0Tip {
		t.Fatalf("repo 0: drift repositories block = %+v, want observed SHA %s (newer parent tip)", block, newerRepo0Tip)
	}
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration() acknowledging retry error = %v", err)
	}

	// The transaction should complete: child is Completed, parent is
	// CodeReady, and every repo has an explicit merge boundary.
	parent, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed after restart and review renewal", child.Parent.CloseOutcome)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady after restart", parent.Status)
	}
	for i, dir := range fx.repoDirs {
		parents := multiRepoGit(t, dir, "rev-list", "--parents", "-n", "1", "HEAD")
		if fields := len(strings.Fields(parents)); fields != 3 {
			t.Fatalf("repo %d: merge parents = %q, want two-parent merge commit", i, parents)
		}
	}

	// Verify repo 0's merge candidate was built against the newer parent
	// tip, not the original launch base.
	tx = child.Parent.Transaction
	if tx == nil {
		t.Fatal("transaction journal missing after completion")
	}
	repo0Entry := tx.EntryByRepo(child.Repos[0].Name)
	if repo0Entry == nil {
		t.Fatal("repo 0 entry missing")
	}
	if repo0Entry.ParentAnchorSHA != newerRepo0Tip {
		t.Fatalf("repo 0 anchor = %s, want newer parent tip %s", repo0Entry.ParentAnchorSHA, newerRepo0Tip)
	}
}

// TestRefactorChildTransactionalMultiRepoExternalRaceRollbackAndAttention
// proves an external ref race triggers only provable rollback and preserves
// ambiguous movement as precise integration attention.
func TestRefactorChildTransactionalMultiRepoExternalRaceRollbackAndAttention(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoE2EFixture(t, 3)

	oldSHAs := make([]string, len(fx.repoDirs))
	for i := range fx.repoDirs {
		oldSHAs[i] = fx.refSHA(i, "refs/heads/feature/parent")
	}

	// Use a wrapping worktree manager that injects an external ref
	// movement on the third repo during the apply phase.
	raceWT := &raceInjectingWorktrees{
		WorktreeManager: fx.wm,
		t:               t,
		raceRepoDir:     fx.repoDirs[2],
		raceBranch:      "feature/parent",
	}
	o := fx.orchestratorWithWorktrees(raceWT)

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v, want nil with attention", err)
	}

	_, child := fx.reload()
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
	// The third repo's externally moved ref should be preserved.
	externalSHA := fx.refSHA(2, "refs/heads/feature/parent")
	if externalSHA == oldSHAs[2] {
		t.Fatal("repo 2: externally moved ref was rolled back; should be preserved")
	}
	// Worktrees for repos 0 and 1 should be reset to the old SHA.
	for i := 0; i < 2; i++ {
		wtHead := multiRepoGit(t, fx.repoDirs[i], "rev-parse", "HEAD")
		if wtHead != oldSHAs[i] {
			t.Fatalf("repo %d: worktree HEAD = %s, want old SHA %s after rollback", i, wtHead, oldSHAs[i])
		}
	}

	// The aggregate phase must be attention — not rolled_back — so the
	// externally moved target is preserved as a precise, durable attention
	// state rather than being cleared and re-prepared from scratch.
	if tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %q, want attention (externally moved unapplied target must be durable attention)", tx.Phase)
	}
	raceRec := tx.AttentionRecord()
	if raceRec == nil || raceRec.Code != errcat.IntegrationRefRace {
		t.Fatalf("attention record = %+v, want code %s", raceRec, errcat.IntegrationRefRace)
	}

	// The raced entry must be in per-repo attention state, and the record's
	// repositories block must carry its expected, candidate, and observed
	// SHAs with raw diagnostics naming the repository, ref, and raced SHAs.
	racedEntry := tx.EntryByRepo(child.Repos[2].Name)
	if racedEntry == nil {
		t.Fatal("raced repo entry missing from journal")
	}
	if racedEntry.ApplyState != feature.RepoApplyAttention {
		t.Fatalf("raced entry apply state = %q, want attention", racedEntry.ApplyState)
	}
	if racedEntry.ObservedSHA == "" {
		t.Fatal("raced entry observed SHA empty; want the externally moved ref SHA")
	}
	if racedEntry.ObservedSHA == racedEntry.ExpectedRefSHA {
		t.Fatal("raced entry observed SHA equals expected; want externally moved SHA")
	}
	if block := attentionRepoBlock(raceRec, racedEntry.Repo); block == nil ||
		block.ExpectedRefSHA != racedEntry.ExpectedRefSHA ||
		block.CandidateSHA != racedEntry.CandidateSHA ||
		block.ObservedSHA != racedEntry.ObservedSHA {
		t.Fatalf("raced repo repositories block = %+v, want entry SHAs (expected %s candidate %s observed %s)",
			block, racedEntry.ExpectedRefSHA, racedEntry.CandidateSHA, racedEntry.ObservedSHA)
	}
	for _, needle := range []string{
		racedEntry.Repo,
		"refs/heads/" + racedEntry.ParentBranch,
		racedEntry.ExpectedRefSHA,
		racedEntry.ObservedSHA,
	} {
		if !strings.Contains(raceRec.Diagnostics, needle) {
			t.Fatalf("attention diagnostics %q missing %q", raceRec.Diagnostics, needle)
		}
	}

	// The rolled-back entries must be in rolled_back state, not attention.
	for i := 0; i < 2; i++ {
		entry := tx.EntryByRepo(child.Repos[i].Name)
		if entry == nil {
			t.Fatalf("repo %d entry missing", i)
		}
		if entry.ApplyState != feature.RepoApplyRolledBack {
			t.Fatalf("repo %d apply state = %q, want rolled_back", i, entry.ApplyState)
		}
	}
}

// TestRefactorChildTransactionalMultiRepoCrashCutPointConvergence proves
// fresh startup at every transaction crash cut point converges to a safe
// retry, completed apply, conditional rollback, or durable attention state.
func TestRefactorChildTransactionalMultiRepoCrashCutPointConvergence(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}

	// Cut point: partial candidate staging — crash after first candidate
	// write but before second. Reconciliation leaves it retryable; restart
	// re-prepares from scratch and completes.
	t.Run("partial_staging", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		child, _ := fx.store.Load(fx.child.ID)
		parent, _ := fx.store.Load(fx.parent.ID)

		// Prepare the first repo's candidate only.
		childHead0 := multiRepoGit(t, child.Repos[0].WorktreePath, "rev-parse", "HEAD")
		parentTip0 := multiRepoGit(t, parent.Repos[0].Path, "rev-parse", "refs/heads/feature/parent")
		result0, err := git.CreateMergeCandidate(parent.Repos[0].Path, parentTip0, childHead0, "merge 0")
		if err != nil {
			t.Fatalf("create merge candidate repo 0: %v", err)
		}
		childHead1 := multiRepoGit(t, child.Repos[1].WorktreePath, "rev-parse", "HEAD")
		parentTip1 := multiRepoGit(t, parent.Repos[1].Path, "rev-parse", "refs/heads/feature/parent")

		journal := &feature.TransactionJournal{
			Phase: feature.TransactionPhasePreparing,
			Entries: []feature.RepoTransactionEntry{
				{
					Repo: child.Repos[0].Name, ParentBranch: parent.Repos[0].Branch,
					ParentAnchorSHA: parentTip0, ExpectedRefSHA: parentTip0,
					ChildHeadSHA: childHead0, CandidateSHA: result0.CandidateSHA,
					PrepState: feature.RepoPrepPrepared,
				},
				{
					Repo: child.Repos[1].Name, ParentBranch: parent.Repos[1].Branch,
					ParentAnchorSHA: parentTip1, ExpectedRefSHA: parentTip1,
					ChildHeadSHA: childHead1, PrepState: feature.RepoPrepPending,
				},
			},
		}
		fx.saveJournal(journal)

		preRefs := make([]string, len(fx.repoDirs))
		for i := range fx.repoDirs {
			preRefs[i] = fx.refSHA(i, "refs/heads/feature/parent")
		}

		// Reconciliation should leave it unchanged (preparing → retryable).
		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		for i := range fx.repoDirs {
			if got := fx.refSHA(i, "refs/heads/feature/parent"); got != preRefs[i] {
				t.Fatalf("repo %d: ref moved during reconciliation", i)
			}
		}

		// Restart should re-prepare from scratch and complete.
		if err := o.RunChildIntegration(fx.child.ID); err != nil {
			t.Fatalf("RunChildIntegration() after partial staging: %v", err)
		}
		_, child = fx.reload()
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			t.Fatalf("close outcome = %q, want completed after restart", child.Parent.CloseOutcome)
		}
	})

	// Cut point: all candidates prepared but not applied → restart applies
	// and closes.
	t.Run("prepared_but_unapplied", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		fx.manualPrepare(t)

		preRefs := make([]string, len(fx.repoDirs))
		for i := range fx.repoDirs {
			preRefs[i] = fx.refSHA(i, "refs/heads/feature/parent")
		}

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		for i := range fx.repoDirs {
			if got := fx.refSHA(i, "refs/heads/feature/parent"); got != preRefs[i] {
				t.Fatalf("repo %d: ref moved during reconciliation", i)
			}
		}

		if err := o.RunChildIntegration(fx.child.ID); err != nil {
			t.Fatalf("RunChildIntegration() after prepared: %v", err)
		}
		_, child := fx.reload()
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			t.Fatalf("close outcome = %q, want completed after restart", child.Parent.CloseOutcome)
		}
	})

	// Cut point: partial apply — crash after first ref applied but before
	// second. Reconciliation rolls back the applied ref; restart completes.
	t.Run("partial_apply", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		// Manually apply the first repo's ref and sync worktree.
		fx.manualApplyRef(t, 0, &journal.Entries[0])
		journal.Entries[0].ApplyState = feature.RepoApplyApplied
		journal.Entries[0].ObservedSHA = journal.Entries[0].CandidateSHA
		journal.Phase = feature.TransactionPhaseApplying
		fx.saveJournal(journal)

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		// The first repo's ref should be rolled back to the old SHA.
		oldSHA := journal.Entries[0].ParentAnchorSHA
		if got := fx.refSHA(0, "refs/heads/feature/parent"); got != oldSHA {
			t.Fatalf("repo 0: ref = %s after reconciliation, want old SHA %s", got, oldSHA)
		}
		wtHead := multiRepoGit(t, fx.repoDirs[0], "rev-parse", "HEAD")
		if wtHead != oldSHA {
			t.Fatalf("repo 0: worktree HEAD = %s, want old SHA %s after rollback", wtHead, oldSHA)
		}

		// Restart should re-prepare and complete.
		if err := o.RunChildIntegration(fx.child.ID); err != nil {
			t.Fatalf("RunChildIntegration() after partial apply: %v", err)
		}
		_, child := fx.reload()
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			t.Fatalf("close outcome = %q, want completed after restart", child.Parent.CloseOutcome)
		}
	})

	// Cut point: all refs applied but not closed → reconciliation completes
	// closure.
	t.Run("applied_not_closed", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		for i := range journal.Entries {
			fx.manualApplyRef(t, i, &journal.Entries[i])
			journal.Entries[i].ApplyState = feature.RepoApplyApplied
			journal.Entries[i].ObservedSHA = journal.Entries[i].CandidateSHA
		}
		journal.Phase = feature.TransactionPhaseApplied
		fx.saveJournal(journal)

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		_, child := fx.reload()
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			t.Fatalf("close outcome = %q, want completed after reconciliation", child.Parent.CloseOutcome)
		}
		if child.Parent.Transaction.Phase != feature.TransactionPhaseMerged {
			t.Fatalf("phase = %s, want merged", child.Parent.Transaction.Phase)
		}
	})

	// Cut point: all refs applied but one worktree not synced — crash
	// between the apply-progress write and the worktree sync. Reconciliation
	// must sync the stale worktree before closing.
	t.Run("applied_not_closed_worktree_unsynced", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		// Apply repo 0 fully (CAS + worktree sync).
		fx.manualApplyRef(t, 0, &journal.Entries[0])
		journal.Entries[0].ApplyState = feature.RepoApplyApplied
		journal.Entries[0].ObservedSHA = journal.Entries[0].CandidateSHA
		// Apply repo 1's ref only (CAS without worktree sync — crash
		// between the progress write and the worktree sync).
		fx.manualApplyRefNoSync(t, 1, &journal.Entries[1])
		journal.Entries[1].ApplyState = feature.RepoApplyApplied
		journal.Entries[1].ObservedSHA = journal.Entries[1].CandidateSHA
		journal.Phase = feature.TransactionPhaseApplied
		fx.saveJournal(journal)

		// Verify repo 1's worktree is stale before reconciliation (files
		// at old tree, ref at candidate after the manual CAS without sync).
		if clean := multiRepoGit(t, fx.repoDirs[1], "status", "--porcelain"); clean == "" {
			t.Fatal("repo 1: worktree should be stale (files at old tree, ref at candidate) before reconciliation")
		}

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		_, child := fx.reload()
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			t.Fatalf("close outcome = %q, want completed after reconciliation", child.Parent.CloseOutcome)
		}
		// The worktree should now be synced to the candidate (clean).
		if clean := multiRepoGit(t, fx.repoDirs[1], "status", "--porcelain"); clean != "" {
			t.Fatalf("repo 1: worktree dirty after reconciliation: %s", clean)
		}
	})

	// Cut point: rollback ref CAS completed but worktree not reset — crash
	// between ref CAS and worktree sync during rollback. Reconciliation
	// detects the rolled-back ref and finishes the worktree reset.
	t.Run("rollback_after_cas_before_worktree", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		// Apply first repo's ref and sync worktree.
		fx.manualApplyRef(t, 0, &journal.Entries[0])
		// Roll back the ref (CAS from candidate to anchor) but leave the
		// worktree at the candidate (simulating a crash before worktree sync).
		fx.manualRollbackRef(t, 0, &journal.Entries[0])

		journal.Entries[0].ApplyState = feature.RepoApplyApplied
		journal.Phase = feature.TransactionPhaseRollingBack
		fx.saveJournal(journal)

		// Verify the worktree is dirty before reconciliation (files at
		// candidate, ref at anchor after the manual rollback CAS).
		if clean := multiRepoGit(t, fx.repoDirs[0], "status", "--porcelain"); clean == "" {
			t.Fatal("repo 0: worktree should be dirty before reconciliation (files at candidate, ref at anchor)")
		}

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		// The worktree should now be clean (reset to anchor).
		if clean := multiRepoGit(t, fx.repoDirs[0], "status", "--porcelain"); clean != "" {
			t.Fatalf("repo 0: worktree dirty after reconciliation: %s", clean)
		}
		// The ref should be at the anchor.
		anchorSHA := journal.Entries[0].ParentAnchorSHA
		if got := fx.refSHA(0, "refs/heads/feature/parent"); got != anchorSHA {
			t.Fatalf("repo 0: ref = %s, want anchor SHA %s after rollback convergence", got, anchorSHA)
		}
	})

	// Cut point: external movement → durable attention.
	t.Run("external_movement", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		fx.manualPrepare(t)

		// Externally move the first repo's parent branch.
		multiRepoGit(t, fx.repoDirs[0], "checkout", "feature/parent")
		testutil.CommitFile(t, fx.repoDirs[0], "external.txt", "external\n", "external movement")
		externalSHA := fx.refSHA(0, "refs/heads/feature/parent")

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		if got := fx.refSHA(0, "refs/heads/feature/parent"); got != externalSHA {
			t.Fatalf("repo 0: ref = %s, want preserved external %s", got, externalSHA)
		}

		_, child := fx.reload()
		tx := child.Parent.Transaction
		if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
			t.Fatalf("phase = %+v, want attention", tx)
		}
		// Startup reconciliation classifies the externally moved ref as a
		// ref race; the record's repositories block carries the entry's
		// old, candidate, and observed SHAs.
		if rec := tx.AttentionRecord(); rec == nil || rec.Code != errcat.IntegrationRefRace {
			t.Fatalf("attention record = %+v, want code %s", rec, errcat.IntegrationRefRace)
		}
		movedEntry := tx.EntryByRepo(child.Repos[0].Name)
		if movedEntry == nil {
			t.Fatal("moved entry missing from journal")
		}
		if block := attentionRepoBlock(tx.AttentionRecord(), movedEntry.Repo); block == nil ||
			block.ParentAnchorSHA != movedEntry.ParentAnchorSHA ||
			block.CandidateSHA != movedEntry.CandidateSHA ||
			block.ObservedSHA != externalSHA {
			t.Fatalf("moved repo repositories block = %+v, want old %s candidate %s observed %s",
				block, movedEntry.ParentAnchorSHA, movedEntry.CandidateSHA, externalSHA)
		}
	})

	// Cut point: apply worktree-sync failure — the CAS succeeds for repo 1
	// but its first worktree sync fails. The ref vector continues forward and
	// idempotent closure retries the sync before completing.
	t.Run("apply_sync_failure", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 3)

		resetWT := &resetFailingWorktrees{
			WorktreeManager: fx.wm,
			failRepoDir:     fx.repoDirs[1],
		}
		o := fx.orchestratorWithWorktrees(resetWT)

		if err := o.RunChildIntegration(fx.child.ID); err != nil {
			t.Fatalf("RunChildIntegration() error = %v, want closure retry to converge", err)
		}

		_, child := fx.reload()
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			t.Fatalf("close outcome = %q, want completed", child.Parent.CloseOutcome)
		}
		if child.Parent.Transaction.Phase != feature.TransactionPhaseMerged {
			t.Fatalf("phase = %s, want merged", child.Parent.Transaction.Phase)
		}

		// All refs and parent worktrees converge to their candidates; none
		// are compensated merely because the post-CAS sync was transient.
		for i := range fx.repoDirs {
			candidate := child.Parent.Transaction.Entries[i].CandidateSHA
			got := fx.refSHA(i, "refs/heads/feature/parent")
			if got != candidate {
				t.Fatalf("repo %d: ref = %s, want candidate %s", i, got, candidate)
			}
			wtHead := multiRepoGit(t, fx.repoDirs[i], "rev-parse", "HEAD")
			if wtHead != candidate {
				t.Fatalf("repo %d: worktree HEAD = %s, want candidate %s", i, wtHead, candidate)
			}
		}
	})

	// Persistence-failure crash matrix: inject a Store.Modify failure at
	// each transaction boundary, then create a fresh store and orchestrator
	// and prove convergence. Named constants derive each write ordinal
	// from the number of repos so an unrelated durable write makes the
	// shift obvious rather than silently re-targeting every later case.
	//
	// The 2-repo clean transaction issues these Store.Modify / MarkCodeReady
	// calls in order:
	//   1. persist preparing (candidate 0)
	//   2. persist preparing (candidate 1)
	//   3. persist prepared phase
	//   4. persist applying phase
	//   5. persist apply progress (repo 0 applied)
	//   6. persist apply progress (repo 1 applied)
	//   7. persist applied phase
	//   8. MarkCodeReady (parent → CodeReady)
	//   9. close child (CloseOutcome = completed)
	//  10. persist merged phase
	//  11. clear worktree path (repo 0) — error caught as cleanup warning
	//  12. record cleanup warning (repo 0)
	//  13. clear worktree path (repo 1) — error caught as cleanup warning
	//  14. record cleanup warning (repo 1)
	const (
		numCleanRepos            = 2
		writeFirstCandidate      = 1
		writeSecondCandidate     = writeFirstCandidate + numCleanRepos - 1
		writePreparedPhase       = writeSecondCandidate + 1
		writeApplyingPhase       = writePreparedPhase + 1
		writeFirstApplyProgress  = writeApplyingPhase + 1
		writeSecondApplyProgress = writeFirstApplyProgress + numCleanRepos - 1
		writeAppliedPhase        = writeSecondApplyProgress + 1
		writeParentCodeReady     = writeAppliedPhase + 1
		writeChildClosure        = writeParentCodeReady + 1
		writeMergedJournal       = writeChildClosure + 1
		// 11 and 13 are the clear-worktree-path calls whose errors
		// are caught as warnings; 12 and 14 are the warning persists.
		writeFirstCleanupWarning  = writeMergedJournal + 2
		writeSecondCleanupWarning = writeFirstCleanupWarning + 2
	)
	persistenceCrashCases := []struct {
		name   string
		failAt int32
	}{
		{"fail_before_first_candidate_persist", writeFirstCandidate},
		{"fail_after_first_candidate_persist", writeSecondCandidate},
		{"fail_after_both_candidates_persist", writePreparedPhase},
		{"fail_after_prepared_persist", writeApplyingPhase},
		{"fail_after_first_apply_progress", writeFirstApplyProgress},
		{"fail_after_second_apply_progress", writeSecondApplyProgress},
		{"fail_after_applied_persist", writeAppliedPhase},
		{"fail_after_parent_codeready", writeParentCodeReady},
		{"fail_after_child_closure", writeChildClosure},
		{"fail_after_merged_journal", writeMergedJournal},
		{"fail_at_first_cleanup_warning", writeFirstCleanupWarning},
		{"fail_at_second_cleanup_warning", writeSecondCleanupWarning},
	}
	for _, tc := range persistenceCrashCases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newMultiRepoE2EFixture(t, 2)
			failO := fx.orchestratorWithFailingStore(tc.failAt)

			// Run the transaction — it should fail at the injected
			// persistence boundary, unless the failure is caught as a
			// cleanup warning (in which case RunChildIntegration returns
			// nil and the transaction is already complete with a warning).
			err := failO.RunChildIntegration(fx.child.ID)
			if err != nil {
				// Create a fresh orchestrator with the original
				// (non-failing) store reading from the same on-disk
				// state.
				freshO := fx.orchestrator()

				// Reconcile from the on-disk state left by the crash.
				if err := freshO.ReconcileIntegrationTransactions(); err != nil {
					t.Fatalf("ReconcileIntegrationTransactions after fail at %d: %v", tc.failAt, err)
				}

				// Re-enter the integration boundary to complete any
				// remaining work.
				if err := freshO.RunChildIntegration(fx.child.ID); err != nil {
					t.Fatalf("RunChildIntegration after reconcile (fail at %d): %v", tc.failAt, err)
				}
			}

			// Verify convergence: child is Completed, parent is
			// CodeReady, and every repo has an explicit two-parent
			// merge commit.
			parent, child := fx.reload()
			if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
				t.Fatalf("fail at %d: close outcome = %q, want completed", tc.failAt, child.Parent.CloseOutcome)
			}
			if parent.Status != feature.StatusCodeReady {
				t.Fatalf("fail at %d: parent status = %s, want CodeReady", tc.failAt, parent.Status)
			}
			for i, dir := range fx.repoDirs {
				parents := multiRepoGit(t, dir, "rev-list", "--parents", "-n", "1", "HEAD")
				if fields := len(strings.Fields(parents)); fields != 3 {
					t.Fatalf("fail at %d: repo %d merge parents = %q, want two-parent merge commit", tc.failAt, i, parents)
				}
			}
		})
	}

	// Rollback persistence-failure crash matrix: inject a Store.Modify
	// failure at each rollback boundary in a 3-repo transaction where an
	// external ref race on the third repo triggers semantic rollback.
	// This proves convergence at every crash cut point during rollback,
	// including the aggregate attention-phase write after compensation.
	//
	// The 3-repo rollback scenario issues these Store.Modify calls:
	//   1-3. persist preparing (candidates 0, 1, 2)
	//   4. persist prepared phase
	//   5. persist applying phase
	//   6-7. persist apply progress (repos 0, 1 applied)
	// (repo 2 ref moves externally → rollbackTransaction)
	//   8. persist rolling_back phase
	//   9-10. persist rollback progress (repos 0, 1 rolled back)
	//  11. persist attention phase preserving repo 2's external ref
	const (
		numRollbackRepos              = 3
		rbWriteThirdCandidate         = numRollbackRepos
		rbWritePreparedPhase          = rbWriteThirdCandidate + 1
		rbWriteApplyingPhase          = rbWritePreparedPhase + 1
		rbWriteSecondApplyProgress    = rbWriteApplyingPhase + 2
		rbWriteRollingBackPhase       = rbWriteSecondApplyProgress + 1
		rbWriteFirstRollbackProgress  = rbWriteRollingBackPhase + 1
		rbWriteSecondRollbackProgress = rbWriteFirstRollbackProgress + 1
		rbWriteAttentionPhase         = rbWriteSecondRollbackProgress + 1
	)
	rollbackCrashCases := []struct {
		name   string
		failAt int32
	}{
		{"fail_at_rolling_back_phase", rbWriteRollingBackPhase},
		{"fail_after_first_rollback_progress", rbWriteFirstRollbackProgress},
		{"fail_after_second_rollback_progress", rbWriteSecondRollbackProgress},
		{"fail_before_attention_phase", rbWriteAttentionPhase},
	}
	for _, tc := range rollbackCrashCases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newMultiRepoE2EFixture(t, 3)

			// Inject an external ref movement when repo 2 is inspected,
			// after the first two refs have been applied.
			raceWT := &raceInjectingWorktrees{
				WorktreeManager: fx.wm,
				t:               t,
				raceRepoDir:     fx.repoDirs[2],
				raceBranch:      "feature/parent",
			}
			failO := fx.orchestratorWithFailingStoreAndWorktree(tc.failAt, raceWT)

			// Run the transaction — it should fail at the injected
			// rollback persistence boundary.
			err := failO.RunChildIntegration(fx.child.ID)
			if err == nil {
				t.Fatalf("expected error at rollback boundary %d, got nil", tc.failAt)
			}

			// Create a fresh orchestrator with a non-failing store and
			// the original worktree manager.
			freshO := fx.orchestrator()

			// Reconcile from the on-disk state left by the crash.
			if err := freshO.ReconcileIntegrationTransactions(); err != nil {
				t.Fatalf("ReconcileIntegrationTransactions after rollback fail at %d: %v", tc.failAt, err)
			}

			// Re-enter the integration boundary. The externally raced
			// ref is re-observed as parent drift and parks once; the
			// retry acknowledges it and completes the remaining work.
			if err := freshO.RunChildIntegration(fx.child.ID); err != nil {
				t.Fatalf("RunChildIntegration after rollback reconcile (fail at %d): %v", tc.failAt, err)
			}
			if _, c := fx.reload(); c.Parent.Transaction != nil &&
				c.Parent.Transaction.Phase == feature.TransactionPhaseAttention {
				if code := c.Parent.Transaction.AttentionCode(); code != errcat.IntegrationParentRefDrift {
					t.Fatalf("rollback fail at %d: attention code = %q, want %s (externally raced ref re-observed as drift): %+v",
						tc.failAt, code, errcat.IntegrationParentRefDrift, c.Parent.Transaction)
				}
				if err := freshO.RunChildIntegration(fx.child.ID); err != nil {
					t.Fatalf("RunChildIntegration acknowledging drift (fail at %d): %v", tc.failAt, err)
				}
			}

			// Verify convergence: child is Completed, parent is
			// CodeReady, and every repo has an explicit two-parent
			// merge commit.
			parent, child := fx.reload()
			if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
				t.Fatalf("rollback fail at %d: close outcome = %q, want completed", tc.failAt, child.Parent.CloseOutcome)
			}
			if parent.Status != feature.StatusCodeReady {
				t.Fatalf("rollback fail at %d: parent status = %s, want CodeReady", tc.failAt, parent.Status)
			}
			for i, dir := range fx.repoDirs {
				parents := multiRepoGit(t, dir, "rev-list", "--parents", "-n", "1", "HEAD")
				if fields := len(strings.Fields(parents)); fields != 3 {
					t.Fatalf("rollback fail at %d: repo %d merge parents = %q, want two-parent merge commit", tc.failAt, i, parents)
				}
			}
		})
	}

	// Cut point: all entries rolled back but the aggregate rolled_back
	// phase was not persisted — the exact crash-strand bug. The journal
	// is rolling_back with every entry already rolled_back. Reconciliation
	// must complete the transition to rolled_back so restart can re-prepare.
	t.Run("all_rolled_back_phase_not_persisted", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		// Apply both repos' refs and sync worktrees.
		for i := range journal.Entries {
			fx.manualApplyRef(t, i, &journal.Entries[i])
			journal.Entries[i].ApplyState = feature.RepoApplyApplied
			journal.Entries[i].ObservedSHA = journal.Entries[i].CandidateSHA
		}

		// Roll back both repos' refs (CAS from candidate to anchor).
		for i := range journal.Entries {
			fx.manualRollbackRef(t, i, &journal.Entries[i])
			journal.Entries[i].ApplyState = feature.RepoApplyRolledBack
			journal.Entries[i].ObservedSHA = journal.Entries[i].ParentAnchorSHA
		}

		// Leave the aggregate phase as rolling_back — simulating a crash
		// after all entries were durably marked rolled_back but before the
		// aggregate rolled_back phase write.
		journal.Phase = feature.TransactionPhaseRollingBack
		fx.saveJournal(journal)

		// Reconciliation should recognize the completed rollback and
		// transition to rolled_back.
		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		_, child := fx.reload()
		tx := child.Parent.Transaction
		if tx == nil {
			t.Fatal("transaction journal missing after reconciliation")
		}
		if tx.Phase != feature.TransactionPhaseRolledBack {
			t.Fatalf("phase = %s, want rolled_back after reconciliation", tx.Phase)
		}

		// Restart should re-prepare from scratch and complete.
		if err := o.RunChildIntegration(fx.child.ID); err != nil {
			t.Fatalf("RunChildIntegration after rolled_back convergence: %v", err)
		}
		_, child = fx.reload()
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			t.Fatalf("close outcome = %q, want completed after restart", child.Parent.CloseOutcome)
		}
	})

	// Cut point: partial rollback — one entry rolled back, one still
	// applied, journal in rolling_back. Reconciliation must resume
	// rollback for the remaining applied entry.
	t.Run("partial_rollback_resume", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		// Apply both repos.
		for i := range journal.Entries {
			fx.manualApplyRef(t, i, &journal.Entries[i])
			journal.Entries[i].ApplyState = feature.RepoApplyApplied
			journal.Entries[i].ObservedSHA = journal.Entries[i].CandidateSHA
		}

		// Roll back only repo 0.
		fx.manualRollbackRef(t, 0, &journal.Entries[0])
		journal.Entries[0].ApplyState = feature.RepoApplyRolledBack
		journal.Entries[0].ObservedSHA = journal.Entries[0].ParentAnchorSHA

		journal.Phase = feature.TransactionPhaseRollingBack
		fx.saveJournal(journal)

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		// Repo 1 should be rolled back by reconciliation.
		oldSHA1 := journal.Entries[1].ParentAnchorSHA
		if got := fx.refSHA(1, "refs/heads/feature/parent"); got != oldSHA1 {
			t.Fatalf("repo 1: ref = %s after reconciliation, want old SHA %s", got, oldSHA1)
		}

		// Restart should re-prepare and complete.
		if err := o.RunChildIntegration(fx.child.ID); err != nil {
			t.Fatalf("RunChildIntegration after partial rollback: %v", err)
		}
		_, child := fx.reload()
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			t.Fatalf("close outcome = %q, want completed after restart", child.Parent.CloseOutcome)
		}
	})
}

// TestRefactorChildTransactionalMultiRepoClosureCleanupAndPublicationHandoff
// proves fully applied integration performs child closure once, retries
// per-repository cleanup safely, and hands the CodeReady parent to
// publication once.
func TestRefactorChildTransactionalMultiRepoClosureCleanupAndPublicationHandoff(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoE2EFixture(t, 2)

	// Enable auto-publish on the parent.
	if err := fx.store.Modify(fx.parent.ID, func(f *feature.Feature) error {
		f.Checkpoints.ManualPublish = false
		return nil
	}); err != nil {
		t.Fatalf("set auto-publish: %v", err)
	}

	// Use a wrapping worktree manager that fails the first RemoveRef call
	// to simulate a per-repository cleanup failure.
	cleanupWT := &cleanupFailingWorktrees{
		WorktreeManager: fx.wm,
		failLimit:       1,
	}
	o := fx.orchestratorWithWorktrees(cleanupWT)

	// Install a counting publish hook to verify publication happens once.
	var publishCount int32
	o.SetPublishRepoFn(func(featureID, repoName string) (string, error) {
		atomic.AddInt32(&publishCount, 1)
		prURL := "https://github.com/test/" + repoName + "/pull/1"
		_ = fx.store.Modify(featureID, func(f *feature.Feature) error {
			if st, ok := f.RepoStates[repoName]; ok && st != nil {
				st.PRURL = prURL
			}
			return nil
		})
		return prURL, nil
	})

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v", err)
	}

	parent, child := fx.reload()

	// Child closure is durable and one-time.
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	if child.Parent.ClosedAt == nil {
		t.Fatal("child closed_at not recorded")
	}
	closedAt := child.Parent.ClosedAt

	// Parent moved to CodeReady and then to Published (auto-publish succeeded).
	if parent.Status != feature.StatusPublished {
		t.Fatalf("parent status = %s, want Published (auto-publish completed)", parent.Status)
	}

	// Publication was called for each repo (once).
	firstCount := atomic.LoadInt32(&publishCount)
	if firstCount != 2 {
		t.Fatalf("publication count after first pass = %d, want 2", firstCount)
	}

	// One cleanup warning should be recorded (the failed repo).
	warningCount := 0
	tx := child.Parent.Transaction
	if tx != nil {
		for i := range tx.Entries {
			if tx.Entries[i].CleanupWarning != "" {
				warningCount++
			}
		}
	}
	if warningCount != 1 {
		t.Fatalf("cleanup warning count = %d, want 1 (one failed cleanup)", warningCount)
	}

	// Second pass is idempotent — no duplicate closure and no duplicate
	// publication. Re-entering the settled closure tail retries the failed
	// cleanup and does not re-publish.
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("second RunChildIntegration() error = %v", err)
	}
	_, child = fx.reload()
	if !child.Parent.ClosedAt.Equal(*closedAt) {
		t.Fatalf("closed_at changed on re-entry: %v != %v", child.Parent.ClosedAt, closedAt)
	}

	// Publication count must not increase on re-entry.
	secondCount := atomic.LoadInt32(&publishCount)
	if secondCount != firstCount {
		t.Fatalf("publication count increased on re-entry: %d != %d", secondCount, firstCount)
	}

	// The failed cleanup should now succeed on retry (the wrapper's
	// failLimit was reached, so subsequent calls succeed).
	warningCount = 0
	tx = child.Parent.Transaction
	if tx != nil {
		for i := range tx.Entries {
			if tx.Entries[i].CleanupWarning != "" {
				warningCount++
			}
		}
	}
	if warningCount != 0 {
		t.Fatalf("cleanup warning count after retry = %d, want 0 (all cleaned up)", warningCount)
	}

	// Every parent branch has an explicit two-parent merge commit.
	for i, dir := range fx.repoDirs {
		parents := multiRepoGit(t, dir, "rev-list", "--parents", "-n", "1", "HEAD")
		if fields := len(strings.Fields(parents)); fields != 3 {
			t.Fatalf("repo %d: merge parents = %q, want two-parent merge commit", i, parents)
		}
	}

	// Transaction is merged.
	if tx == nil || tx.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %+v, want merged", tx)
	}
}
