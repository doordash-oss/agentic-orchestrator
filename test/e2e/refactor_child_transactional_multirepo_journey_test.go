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
	t              *testing.T
	repoDirs       []string
	parentSHAs     []string
	store          *feature.Store
	mgr            *feature.Manager
	wm             *git.WorktreeManager
	parent         *feature.Feature
	child          *feature.Feature
	childWTs       []string
	parentBranch   string
	appendedBranch string
}

func newMultiRepoE2EFixture(t *testing.T, numRepos int) *multiRepoE2EFixture {
	t.Helper()
	repoDirs := make([]string, numRepos)
	parentSHAs := make([]string, numRepos)
	childTips := make([]string, numRepos)
	childWTs := make([]string, numRepos)
	childBranch := "feature/child-tx"
	parentBranch := "feature/parent"

	parentRepos := make([]feature.FeatureRepo, 0, numRepos)
	childRepos := make([]feature.FeatureRepo, 0, numRepos)
	bases := make([]feature.ChildRepoBase, 0, numRepos)

	for i := 0; i < numRepos; i++ {
		repoDir := testutil.InitGitRepo(t)
		multiRepoGit(t, repoDir, "checkout", "-b", parentBranch)
		testutil.CommitFile(t, repoDir, "base.txt", "v1\n", "parent base")
		parentSHA := multiRepoGit(t, repoDir, "rev-parse", "HEAD")

		childWT := t.TempDir() + "/child-wt-" + string(rune('A'+i))
		multiRepoGit(t, repoDir, "worktree", "add", "-b", childBranch, childWT, parentSHA)
		testutil.CommitFile(t, childWT, "child.txt", "child work\n", "child change")

		repoName := "repo" + string(rune('A'+i))
		publishable := true
		repoDirs[i] = repoDir
		parentSHAs[i] = parentSHA
		childWTs[i] = childWT

		parentRepos = append(parentRepos, feature.FeatureRepo{
			Name: repoName, Path: repoDir, WorktreePath: repoDir,
			Branch: parentBranch, BaseBranch: "main", Publishable: &publishable,
		})
		childRepos = append(childRepos, feature.FeatureRepo{
			Name: repoName, Path: repoDir, WorktreePath: childWT,
			Branch: childBranch, BaseBranch: "main",
		})
		bases = append(bases, feature.ChildRepoBase{Repo: repoName, SHA: parentSHA, ParentBranch: parentBranch})
	}

	// The child's recorded layer tip per repository: the head after the
	// committed child change, before integration commits anything remaining.
	for i, wt := range childWTs {
		childTips[i] = multiRepoGit(t, wt, "rev-parse", "HEAD")
	}

	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	parent := &feature.Feature{
		ID: "parent-tx", Name: "Parent TX", Slug: "parent-tx",
		Status: feature.StatusPublished, CurrentPhase: feature.PhasePublish,
		Created: time.Now(), ActiveRun: 1, RunCount: 1,
		Checkpoints: feature.Checkpoints{ManualPublish: true},
		Repos:       parentRepos,
		// A one-layer stack on the checked-out parent branch, so refactor
		// children append their layers onto it as position 2.
		Stack: []feature.StackLayer{{
			Position: 1, Title: "Parent layer", Slug: "parent-layer", Phases: []int{1}, Branch: parentBranch,
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
		ID: "child-tx", Name: "Child TX", Slug: "child-tx",
		Status: feature.StatusReviewPassed, CurrentPhase: feature.PhaseFinalReview,
		Pipeline: feature.PipelineMedium, Created: time.Now(),
		ActiveRun: 1, RunCount: 1, Repos: childRepos,
		// A one-layer stack on the child branch namespace with the recorded
		// per-repository tips, so refactor integration appends it as parent
		// position 2 on the appended branch.
		Stack: []feature.StackLayer{{
			Position: 1, Title: "Child layer", Slug: "child-layer", Phases: []int{1}, Branch: childBranch,
			Repos: childStackRepos,
		}},
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
		t: t, repoDirs: repoDirs, parentSHAs: parentSHAs,
		store: store, mgr: mgr, wm: wm,
		parent: parent, child: child, childWTs: childWTs,
		parentBranch:   parentBranch,
		appendedBranch: git.LayerBranchName(parent.WorkspaceSlug(), 2, "child-layer"),
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

// manualPrepare builds the append-shaped journal a prepared refactor child
// carries — created refs for the appended layer at position 2, the previous
// top, and the appended layer definitions — and writes it to disk without
// going through the orchestrator. This simulates a crash after the append
// candidates are prepared but before any ref is created.
func (fx *multiRepoE2EFixture) manualPrepare(t *testing.T) *feature.TransactionJournal {
	t.Helper()
	child, _ := fx.store.Load(fx.child.ID)
	journal := &feature.TransactionJournal{
		Phase: feature.TransactionPhasePrepared,
		AppendedLayers: []feature.AppendedLayer{{
			Position: 2, Title: "Child layer", Slug: "child-layer", Branch: fx.appendedBranch,
			Origin: &feature.StackLayerOrigin{SourceFeatureID: child.ID, SourceLayerPosition: 1},
		}},
	}
	for i := range child.Repos {
		childHead := multiRepoGit(t, child.Repos[i].WorktreePath, "rev-parse", "HEAD")
		parentTip := multiRepoGit(t, fx.repoDirs[i], "rev-parse", "refs/heads/"+fx.parentBranch)
		journal.Entries = append(journal.Entries, feature.RepoTransactionEntry{
			Repo: child.Repos[i].Name,
			Refs: []feature.RepoTransactionRef{{
				Kind: feature.RepoRefKindCreate, Branch: fx.appendedBranch, Layer: 2,
				CandidateSHA: childHead,
			}},
			PreviousTop: &feature.RepoTransactionPreviousTop{
				Branch: fx.parentBranch, Layer: 1, TipSHA: parentTip,
			},
			ChildHeadSHA: childHead,
			PrepState:    feature.RepoPrepPrepared,
		})
	}
	fx.saveJournal(journal)
	return journal
}

// manualApplyRef creates a repo's appended branch at the candidate and
// switches the worktree onto it, simulating a completed apply step before
// a crash.
func (fx *multiRepoE2EFixture) manualApplyRef(t *testing.T, idx int, entry *feature.RepoTransactionEntry) {
	t.Helper()
	top := entry.TopRef()
	multiRepoGit(t, fx.repoDirs[idx], "branch", top.Branch, top.CandidateSHA)
	multiRepoGit(t, fx.repoDirs[idx], "checkout", top.Branch)
}

// manualApplyRefNoSync creates a repo's appended branch at the candidate
// without switching the worktree, simulating a crash between the
// apply-progress write and the worktree switch.
func (fx *multiRepoE2EFixture) manualApplyRefNoSync(t *testing.T, idx int, entry *feature.RepoTransactionEntry) {
	t.Helper()
	top := entry.TopRef()
	multiRepoGit(t, fx.repoDirs[idx], "branch", top.Branch, top.CandidateSHA)
}

// manualRollbackRef deletes a repo's appended branch without switching the
// worktree back, simulating a crash between the rollback's ref delete and
// the worktree switch.
func (fx *multiRepoE2EFixture) manualRollbackRef(t *testing.T, idx int, entry *feature.RepoTransactionEntry) {
	t.Helper()
	top := entry.TopRef()
	multiRepoGit(t, fx.repoDirs[idx], "update-ref", "-d", "refs/heads/"+top.Branch)
}

// assertCreatedRefAt checks the appended branch ref sits at the candidate.
func (fx *multiRepoE2EFixture) assertCreatedRefAt(t *testing.T, repoIdx int, candidate string) {
	t.Helper()
	if got := fx.refSHA(repoIdx, "refs/heads/"+fx.appendedBranch); got != candidate {
		t.Fatalf("repo %d: appended branch ref = %s, want candidate %s", repoIdx, got, candidate)
	}
}

// assertCreatedRefAbsent checks the appended branch does not exist.
func (fx *multiRepoE2EFixture) assertCreatedRefAbsent(t *testing.T, repoIdx int) {
	t.Helper()
	if branches := multiRepoGit(t, fx.repoDirs[repoIdx], "branch", "--list", fx.appendedBranch); branches != "" {
		t.Fatalf("repo %d: appended branch %s exists, want absent", repoIdx, fx.appendedBranch)
	}
}

// assertWorktreeOn checks the repository's worktree checked-out branch.
func (fx *multiRepoE2EFixture) assertWorktreeOn(t *testing.T, repoIdx int, branch string) {
	t.Helper()
	if got := multiRepoGit(t, fx.repoDirs[repoIdx], "branch", "--show-current"); got != branch {
		t.Fatalf("repo %d: worktree branch = %q, want %q", repoIdx, got, branch)
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
// ref movement when RefSHAOrAbsent is first called for the target repo —
// the read the append flow uses to inspect refs during preparation and
// apply. Moving the parent branch makes the append transaction's verify of
// the previous top fail, so the raced repository's refs are preserved as
// integration attention.
type raceInjectingWorktrees struct {
	*git.WorktreeManager
	t           *testing.T
	raceRepoDir string
	raceBranch  string
	injected    bool
}

func (w *raceInjectingWorktrees) RefSHAOrAbsent(repoPath, ref string) (string, bool, error) {
	if !w.injected && repoPath == w.raceRepoDir {
		w.injected = true
		multiRepoGit(w.t, w.raceRepoDir, "checkout", w.raceBranch)
		testutil.CommitFile(w.t, w.raceRepoDir, "race.txt", "external\n", "external race before apply")
	}
	return w.WorktreeManager.RefSHAOrAbsent(repoPath, ref)
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

// switchFailingWorktrees wraps *git.WorktreeManager and fails the first
// SwitchBranch call for the target repo, simulating a worktree-sync failure
// after a successful append ref transaction.
type switchFailingWorktrees struct {
	*git.WorktreeManager
	failRepoDir string
	failed      bool
}

func (w *switchFailingWorktrees) SwitchBranch(worktreePath, branch string) error {
	if !w.failed && worktreePath == w.failRepoDir {
		w.failed = true
		return fmt.Errorf("simulated worktree switch failure")
	}
	return w.WorktreeManager.SwitchBranch(worktreePath, branch)
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

	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %+v, want merged", tx)
	}
	if len(tx.Entries) != 2 {
		t.Fatalf("transaction entries = %d, want 2", len(tx.Entries))
	}

	// Every repository's appended layer branch was created at the child
	// head, the existing parent layer's ref is unchanged, and the worktree
	// switched onto the appended branch.
	for i := range fx.repoDirs {
		fx.assertCreatedRefAt(t, i, tx.Entries[i].ChildHeadSHA)
		if got := fx.refSHA(i, "refs/heads/"+fx.parentBranch); got != fx.parentSHAs[i] {
			t.Fatalf("repo %d: parent branch ref = %s, want unchanged %s", i, got, fx.parentSHAs[i])
		}
		fx.assertWorktreeOn(t, i, fx.appendedBranch)
	}

	// Closure persisted the appended layer onto the parent's stack with
	// per-repository tips equal to the candidates and the repository
	// records pointing at the new top branch.
	if len(parent.Stack) != 2 {
		t.Fatalf("parent stack layers = %d, want 2 after closure", len(parent.Stack))
	}
	appended := parent.Stack[1]
	if appended.Position != 2 || appended.Branch != fx.appendedBranch ||
		appended.Origin == nil || appended.Origin.SourceFeatureID != child.ID || appended.Origin.SourceLayerPosition != 1 {
		t.Fatalf("persisted appended layer = %+v, want position 2 on %s with the child origin", appended, fx.appendedBranch)
	}
	for i := range parent.Repos {
		if got := appended.Repos[parent.Repos[i].Name].TipSHA; got != tx.Entries[i].ChildHeadSHA {
			t.Fatalf("repo %d: appended layer tip = %s, want the created ref's candidate %s", i, got, tx.Entries[i].ChildHeadSHA)
		}
		if parent.Repos[i].Branch != fx.appendedBranch {
			t.Fatalf("repo %d: record branch = %q, want the appended branch %q", i, parent.Repos[i].Branch, fx.appendedBranch)
		}
	}

	for i, wt := range fx.childWTs {
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Fatalf("repo %d: child worktree still present", i)
		}
	}
}

// TestRefactorChildTransactionalMultiRepoStagedConflictRestartAndReviewRenewal
// proves a parent divergence — the append analog of a staged conflict, since
// append preparation requires the candidates to descend from the parent
// tip — leaves every parent ref unmoved and no appended branch created,
// then Restart renews final review after the divergent child code changes.
// The test exercises:
//   - one diverged repository among clean peers, observed first as
//     parent-ref drift and then, once acknowledged, as the ancestry
//     violation (no replay is attempted),
//   - a resolution that merges the parent-side commits into the child
//     history — the append world absorbs parent movement through the
//     child, never through a replayed candidate,
//   - newer parent commits arriving before restart,
//   - code-changing resolution followed by final-review rerun via
//     RestartPhase → StartFeature (the desktop app's dispatch boundary),
//   - created refs at the child head in every repository after re-prepare.
func TestRefactorChildTransactionalMultiRepoStagedConflictRestartAndReviewRenewal(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoE2EFixture(t, 2)
	o := fx.orchestrator()

	// Divergence: a parent-side commit on the second repo's parent branch
	// that the child's history does not descend from.
	testutil.CommitFile(t, fx.repoDirs[1], "child.txt", "parent-side conflict\n", "conflicting parent commit")

	preRefs := make([]string, len(fx.repoDirs))
	for i := range fx.repoDirs {
		preRefs[i] = fx.refSHA(i, "refs/heads/"+fx.parentBranch)
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
	// The drift block carries the previous top's branch and the moved tip
	// as the observed SHA.
	if block := attentionRepoBlock(driftRec, child.Repos[1].Name); block == nil ||
		block.Branch != fx.parentBranch ||
		block.ObservedSHA != preRefs[1] {
		t.Fatalf("repo 1: drift repositories block = %+v, want branch %s and observed %s",
			block, fx.parentBranch, preRefs[1])
	}

	// Acknowledged retry: the drift gate passes, but the append cannot
	// absorb a tip the child's history does not descend from — no replay
	// is attempted — so preparation parks candidate-failed with the
	// ancestry-chain diagnostics.
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration() retry error = %v, want nil with attention", err)
	}

	// All parent refs unchanged and no appended branch created.
	for i := range fx.repoDirs {
		if got := fx.refSHA(i, "refs/heads/"+fx.parentBranch); got != preRefs[i] {
			t.Fatalf("repo %d: parent ref moved from %s to %s on divergence", i, preRefs[i], got)
		}
		fx.assertCreatedRefAbsent(t, i)
	}

	_, child = fx.reload()
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention", tx)
	}
	divergenceRec := tx.AttentionRecord()
	if divergenceRec == nil || divergenceRec.Code != errcat.IntegrationCandidateFailed {
		t.Fatalf("attention record = %+v, want code %s", divergenceRec, errcat.IntegrationCandidateFailed)
	}
	if !strings.Contains(divergenceRec.Diagnostics, "ancestry") {
		t.Fatalf("attention diagnostics = %q, want the ancestry-chain violation named", divergenceRec.Diagnostics)
	}
	if block := attentionRepoBlock(divergenceRec, child.Repos[1].Name); block == nil {
		t.Fatalf("repo 1: divergence repositories block missing, record = %+v", divergenceRec)
	}

	// Resolve the divergence: merge the parent's conflicting commit into
	// the child's history — taking the parent's child.txt, the same
	// resolution this test performed for the merge candidate — and add a
	// new file so the child head unambiguously changes (requiring review
	// renewal).
	multiRepoGit(t, fx.childWTs[1], "merge", "-X", "theirs", fx.parentBranch)
	testutil.CommitFile(t, fx.childWTs[1], "resolve.txt", "resolved\n", "add resolution marker")

	// Newer parent commits arrive before restart: add a clean commit to
	// repo 0's parent branch (not the diverged repo). The append world
	// never replays the child onto a moved tip, so the newer commit is
	// merged into repo 0's child history too; the re-prepared journal then
	// records it as the previous top.
	testutil.CommitFile(t, fx.repoDirs[0], "newer-parent.txt", "newer\n", "newer parent commit before restart")
	newerRepo0Tip := fx.refSHA(0, "refs/heads/"+fx.parentBranch)
	multiRepoGit(t, fx.childWTs[0], "merge", fx.parentBranch)

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

	// Dispatch Final Review via StartFeature — the same entry point the
	// client uses for RestartDispatchPhase. invalidateFinalReview set
	// CurrentPhase=PhaseFinalReview, so StartFeature dispatches it.
	// The mock returns all_passed, then advanceAfterFinalReview calls
	// RunChildIntegration to re-prepare the append candidates against the
	// latest parent tips and complete.
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

	// The transaction completes: the child is Completed, the parent
	// CodeReady, and every repository's appended branch sits at the child
	// head while the parent branches never moved.
	parent, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed after restart and review renewal", child.Parent.CloseOutcome)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady after restart", parent.Status)
	}
	tx = child.Parent.Transaction
	if tx == nil {
		t.Fatal("transaction journal missing after completion")
	}
	for i := range fx.repoDirs {
		fx.assertCreatedRefAt(t, i, tx.Entries[i].ChildHeadSHA)
		fx.assertWorktreeOn(t, i, fx.appendedBranch)
	}
	if got := fx.refSHA(0, "refs/heads/"+fx.parentBranch); got != newerRepo0Tip {
		t.Fatalf("repo 0: parent branch ref = %s, want unchanged newer tip %s", got, newerRepo0Tip)
	}
	if got := fx.refSHA(1, "refs/heads/"+fx.parentBranch); got != preRefs[1] {
		t.Fatalf("repo 1: parent branch ref = %s, want unchanged diverged tip %s", got, preRefs[1])
	}

	// The re-prepared journal used the latest parent tips: repo 0's
	// previous top records the newer tip, not the original launch base.
	repo0Entry := tx.EntryByRepo(child.Repos[0].Name)
	if repo0Entry == nil {
		t.Fatal("repo 0 entry missing")
	}
	if prev := repo0Entry.PreviousTopRef(); prev == nil || prev.TipSHA != newerRepo0Tip {
		t.Fatalf("repo 0 previous top = %+v, want the newer parent tip %s", repo0Entry.PreviousTopRef(), newerRepo0Tip)
	}
}

// TestRefactorChildTransactionalMultiRepoExternalRaceRollbackAndAttention
// proves an external ref race triggers only provable rollback and preserves
// ambiguous movement as precise integration attention. The raced movement
// targets the parent branch — the ref the append transaction verifies as
// its previous top — so the third repository's ref transaction refuses the
// moved tip after the first two repositories applied.
func TestRefactorChildTransactionalMultiRepoExternalRaceRollbackAndAttention(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newMultiRepoE2EFixture(t, 3)

	oldSHAs := make([]string, len(fx.repoDirs))
	for i := range fx.repoDirs {
		oldSHAs[i] = fx.refSHA(i, "refs/heads/"+fx.parentBranch)
	}

	// Use a wrapping worktree manager that injects an external ref
	// movement on the third repo when its refs are first read: the
	// append preparation observes the created refs' absence, and the
	// apply-time transaction then refuses the moved previous top.
	raceWT := &raceInjectingWorktrees{
		WorktreeManager: fx.wm,
		t:               t,
		raceRepoDir:     fx.repoDirs[2],
		raceBranch:      fx.parentBranch,
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
	// Repos 0 and 1 are rolled back: their created refs deleted, their
	// parent branches never moved, and their worktrees back on the parent
	// branch.
	for i := 0; i < 2; i++ {
		fx.assertCreatedRefAbsent(t, i)
		if got := fx.refSHA(i, "refs/heads/"+fx.parentBranch); got != oldSHAs[i] {
			t.Fatalf("repo %d: parent branch ref = %s after rollback, want previous top tip %s", i, got, oldSHAs[i])
		}
		fx.assertWorktreeOn(t, i, fx.parentBranch)
	}
	// The third repo's externally moved parent branch is preserved, and
	// its appended branch was never created.
	externalSHA := fx.refSHA(2, "refs/heads/"+fx.parentBranch)
	if externalSHA == oldSHAs[2] {
		t.Fatal("repo 2: externally moved parent branch was rolled back; should be preserved")
	}
	fx.assertCreatedRefAbsent(t, 2)

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
	// repositories block must carry its created ref's branch and candidate
	// SHA, with raw diagnostics naming the repository, the verified ref,
	// the previous top tip it expected, and the externally observed SHA.
	racedEntry := tx.EntryByRepo(child.Repos[2].Name)
	if racedEntry == nil {
		t.Fatal("raced repo entry missing from journal")
	}
	if racedEntry.ApplyState != feature.RepoApplyAttention {
		t.Fatalf("raced entry apply state = %q, want attention", racedEntry.ApplyState)
	}
	racedTop := racedEntry.TopRef()
	if racedTop == nil {
		t.Fatal("raced entry records no refs")
	}
	racedPrev := racedEntry.PreviousTopRef()
	if racedPrev == nil {
		t.Fatal("raced entry records no previous top")
	}
	if block := attentionRepoBlock(raceRec, racedEntry.Repo); block == nil ||
		block.Branch != racedTop.Branch ||
		block.CandidateSHA != racedTop.CandidateSHA {
		t.Fatalf("raced repo repositories block = %+v, want branch %s candidate %s",
			block, racedTop.Branch, racedTop.CandidateSHA)
	}
	for _, needle := range []string{
		racedEntry.Repo,
		"refs/heads/" + fx.parentBranch,
		racedPrev.TipSHA,
		externalSHA,
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

	// Cut point: interrupted staging. Refactor append staging is one
	// computation and one persist, so an interrupted staging leaves a
	// preparing-phase journal whose entries carry the child heads and
	// previous tops but no created refs. Reconciliation leaves it
	// retryable; restart re-prepares from scratch and completes.
	t.Run("partial_staging", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		child, _ := fx.store.Load(fx.child.ID)

		journal := &feature.TransactionJournal{Phase: feature.TransactionPhasePreparing}
		for i := range child.Repos {
			childHead := multiRepoGit(t, child.Repos[i].WorktreePath, "rev-parse", "HEAD")
			parentTip := multiRepoGit(t, fx.repoDirs[i], "rev-parse", "refs/heads/"+fx.parentBranch)
			journal.Entries = append(journal.Entries, feature.RepoTransactionEntry{
				Repo: child.Repos[i].Name,
				PreviousTop: &feature.RepoTransactionPreviousTop{
					Branch: fx.parentBranch, Layer: 1, TipSHA: parentTip,
				},
				ChildHeadSHA: childHead,
				PrepState:    feature.RepoPrepPending,
			})
		}
		fx.saveJournal(journal)

		preRefs := make([]string, len(fx.repoDirs))
		for i := range fx.repoDirs {
			preRefs[i] = fx.refSHA(i, "refs/heads/"+fx.parentBranch)
		}

		// Reconciliation should leave it unchanged (preparing → retryable).
		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		for i := range fx.repoDirs {
			if got := fx.refSHA(i, "refs/heads/"+fx.parentBranch); got != preRefs[i] {
				t.Fatalf("repo %d: ref moved during reconciliation", i)
			}
			fx.assertCreatedRefAbsent(t, i)
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

	// Cut point: append candidates prepared but not applied → restart
	// applies and closes.
	t.Run("prepared_but_unapplied", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		fx.manualPrepare(t)

		preRefs := make([]string, len(fx.repoDirs))
		for i := range fx.repoDirs {
			preRefs[i] = fx.refSHA(i, "refs/heads/"+fx.parentBranch)
		}

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		for i := range fx.repoDirs {
			if got := fx.refSHA(i, "refs/heads/"+fx.parentBranch); got != preRefs[i] {
				t.Fatalf("repo %d: ref moved during reconciliation", i)
			}
			fx.assertCreatedRefAbsent(t, i)
		}
		// The journal stays retryable: a created ref that is absent
		// classifies as at-anchor, so the scan neither applies, rolls
		// back, nor parks.
		_, child := fx.reload()
		if stored := child.Parent.Transaction; stored == nil ||
			stored.Phase != feature.TransactionPhasePrepared || stored.Attention != nil {
			t.Fatalf("journal after scan = %+v, want the prepared phase without attention (left retryable)", stored)
		}

		if err := o.RunChildIntegration(fx.child.ID); err != nil {
			t.Fatalf("RunChildIntegration() after prepared: %v", err)
		}
		_, child = fx.reload()
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			t.Fatalf("close outcome = %q, want completed after restart", child.Parent.CloseOutcome)
		}
	})

	// Cut point: partial apply — repo 0's ref transaction ran and its
	// worktree switched, but no apply progress was persisted. The startup
	// scan rolls back the provable partial apply; restart re-prepares and
	// completes.
	t.Run("partial_apply", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		// Manually apply the first repo's created ref and switch its
		// worktree, without persisting apply progress.
		fx.manualApplyRef(t, 0, &journal.Entries[0])
		journal.Entries[0].ApplyState = feature.RepoApplyApplied
		journal.Entries[0].TopRef().ObservedSHA = journal.Entries[0].TopRef().CandidateSHA
		journal.Phase = feature.TransactionPhaseApplying
		fx.saveJournal(journal)

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		// The provable partial apply is rolled back: the created ref is
		// deleted, the parent branch never moved, and the worktree is
		// back on the parent branch.
		fx.assertCreatedRefAbsent(t, 0)
		if got := fx.refSHA(0, "refs/heads/"+fx.parentBranch); got != journal.Entries[0].PreviousTopRef().TipSHA {
			t.Fatalf("repo 0: parent branch ref = %s after reconciliation, want previous top tip %s",
				got, journal.Entries[0].PreviousTopRef().TipSHA)
		}
		fx.assertWorktreeOn(t, 0, fx.parentBranch)
		stored, _ := fx.store.Load(fx.child.ID)
		if stored.Parent.Transaction == nil ||
			stored.Parent.Transaction.Phase != feature.TransactionPhaseRolledBack {
			t.Fatalf("transaction phase = %+v, want rolled_back after the scan", stored.Parent.Transaction)
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

	// Cut point: all created refs applied but not closed → reconciliation
	// completes closure and persists the appended layers.
	t.Run("applied_not_closed", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		for i := range journal.Entries {
			fx.manualApplyRef(t, i, &journal.Entries[i])
			journal.Entries[i].ApplyState = feature.RepoApplyApplied
			journal.Entries[i].TopRef().ObservedSHA = journal.Entries[i].TopRef().CandidateSHA
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
		// The scan finished closure: every created ref confirmed at its
		// candidate, every worktree on the new top branch, and the
		// appended layer persisted onto the parent's stack.
		for i := range fx.repoDirs {
			fx.assertCreatedRefAt(t, i, child.Parent.Transaction.Entries[i].TopRef().CandidateSHA)
			fx.assertWorktreeOn(t, i, fx.appendedBranch)
		}
		parent, _ := fx.reload()
		if len(parent.Stack) != 2 || parent.Stack[1].Branch != fx.appendedBranch || parent.Stack[1].Origin == nil {
			t.Fatalf("parent stack = %+v, want the appended layer persisted with its origin", parent.Stack)
		}
		for i := range parent.Repos {
			if got := parent.Stack[1].Repos[parent.Repos[i].Name].TipSHA; got != child.Parent.Transaction.Entries[i].TopRef().CandidateSHA {
				t.Fatalf("repo %d: appended layer tip = %s, want the created ref's candidate %s",
					i, got, child.Parent.Transaction.Entries[i].TopRef().CandidateSHA)
			}
		}
	})

	// Cut point: all created refs applied but one worktree not switched —
	// crash between the apply-progress write and the worktree switch.
	// Reconciliation must sync the stale worktree before closing.
	t.Run("applied_not_closed_worktree_unsynced", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		// Apply repo 0 fully (created ref + worktree switch).
		fx.manualApplyRef(t, 0, &journal.Entries[0])
		journal.Entries[0].ApplyState = feature.RepoApplyApplied
		journal.Entries[0].TopRef().ObservedSHA = journal.Entries[0].TopRef().CandidateSHA
		// Create repo 1's ref only (without the worktree switch — crash
		// between the progress write and the switch).
		fx.manualApplyRefNoSync(t, 1, &journal.Entries[1])
		journal.Entries[1].ApplyState = feature.RepoApplyApplied
		journal.Entries[1].TopRef().ObservedSHA = journal.Entries[1].TopRef().CandidateSHA
		journal.Phase = feature.TransactionPhaseApplied
		fx.saveJournal(journal)

		// Verify repo 1's worktree is stale before reconciliation: still
		// on the parent branch while its appended ref sits at the
		// candidate.
		fx.assertCreatedRefAt(t, 1, journal.Entries[1].TopRef().CandidateSHA)
		fx.assertWorktreeOn(t, 1, fx.parentBranch)

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		_, child := fx.reload()
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			t.Fatalf("close outcome = %q, want completed after reconciliation", child.Parent.CloseOutcome)
		}
		// The worktree should now be synced to the appended branch (clean).
		fx.assertWorktreeOn(t, 1, fx.appendedBranch)
		if clean := multiRepoGit(t, fx.repoDirs[1], "status", "--porcelain"); clean != "" {
			t.Fatalf("repo 1: worktree dirty after reconciliation: %s", clean)
		}
	})

	// Cut point: rollback ref delete completed but worktree not switched
	// back — crash between the ref delete and the worktree switch during
	// rollback. Reconciliation detects the rolled-back created ref and
	// finishes the worktree switch.
	t.Run("rollback_after_cas_before_worktree", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		journal := fx.manualPrepare(t)

		// Apply first repo's created ref and switch its worktree.
		fx.manualApplyRef(t, 0, &journal.Entries[0])
		// Roll back the created ref (delete it) but leave the worktree on
		// the appended branch (simulating a crash before the switch back).
		fx.manualRollbackRef(t, 0, &journal.Entries[0])

		journal.Entries[0].ApplyState = feature.RepoApplyApplied
		journal.Phase = feature.TransactionPhaseRollingBack
		fx.saveJournal(journal)

		// The crash state: the created ref is deleted while the durable
		// journal still marks the entry applied.
		fx.assertCreatedRefAbsent(t, 0)

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		// The worktree should now be switched back to the parent branch
		// and clean.
		fx.assertWorktreeOn(t, 0, fx.parentBranch)
		if clean := multiRepoGit(t, fx.repoDirs[0], "status", "--porcelain"); clean != "" {
			t.Fatalf("repo 0: worktree dirty after reconciliation: %s", clean)
		}
		// The parent branch should be unchanged (its ref was only ever
		// verified, never moved).
		if got := fx.refSHA(0, "refs/heads/"+fx.parentBranch); got != journal.Entries[0].PreviousTopRef().TipSHA {
			t.Fatalf("repo 0: parent branch ref = %s, want previous top tip %s after rollback convergence",
				got, journal.Entries[0].PreviousTopRef().TipSHA)
		}
	})

	// Cut point: external movement → durable attention.
	t.Run("external_movement", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 2)
		fx.manualPrepare(t)

		// Externally create the first repo's appended branch at an
		// unrelated commit: a created ref observed anywhere other than
		// absent or its candidate is a race.
		multiRepoGit(t, fx.repoDirs[0], "branch", fx.appendedBranch, fx.parentSHAs[0])
		externalSHA := fx.parentSHAs[0]

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		if got := fx.refSHA(0, "refs/heads/"+fx.appendedBranch); got != externalSHA {
			t.Fatalf("repo 0: appended branch ref = %s, want preserved external %s", got, externalSHA)
		}

		_, child := fx.reload()
		tx := child.Parent.Transaction
		if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
			t.Fatalf("phase = %+v, want attention", tx)
		}
		// Startup reconciliation classifies the externally created ref as
		// a ref race; the record's repositories block carries the entry's
		// branch, candidate, and observed SHAs.
		if rec := tx.AttentionRecord(); rec == nil || rec.Code != errcat.IntegrationRefRace {
			t.Fatalf("attention record = %+v, want code %s", rec, errcat.IntegrationRefRace)
		}
		movedEntry := tx.EntryByRepo(child.Repos[0].Name)
		if movedEntry == nil {
			t.Fatal("moved entry missing from journal")
		}
		movedTop := movedEntry.TopRef()
		if block := attentionRepoBlock(tx.AttentionRecord(), movedEntry.Repo); block == nil ||
			movedTop == nil ||
			block.Branch != movedTop.Branch ||
			block.CandidateSHA != movedTop.CandidateSHA ||
			block.ObservedSHA != externalSHA {
			t.Fatalf("moved repo repositories block = %+v, want branch %s candidate %s observed %s",
				block, movedTop.Branch, movedTop.CandidateSHA, externalSHA)
		}
	})

	// Cut point: apply worktree-switch failure — the ref transaction
	// succeeds for repo 1 but its first branch switch fails. The ref
	// vector continues forward and idempotent closure retries the switch
	// before completing.
	t.Run("apply_sync_failure", func(t *testing.T) {
		fx := newMultiRepoE2EFixture(t, 3)

		switchWT := &switchFailingWorktrees{
			WorktreeManager: fx.wm,
			failRepoDir:     fx.repoDirs[1],
		}
		o := fx.orchestratorWithWorktrees(switchWT)

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

		// All created refs and parent worktrees converge to their
		// candidates; none are compensated merely because the post-
		// transaction switch was transient.
		for i := range fx.repoDirs {
			candidate := child.Parent.Transaction.Entries[i].TopRef().CandidateSHA
			fx.assertCreatedRefAt(t, i, candidate)
			fx.assertWorktreeOn(t, i, fx.appendedBranch)
			if head := multiRepoGit(t, fx.repoDirs[i], "rev-parse", "HEAD"); head != candidate {
				t.Fatalf("repo %d: worktree HEAD = %s, want candidate %s", i, head, candidate)
			}
		}
	})

	// Persistence-failure crash matrix: inject a Store.Modify failure at
	// each transaction boundary, then create a fresh store and orchestrator
	// and prove convergence. Refactor staging is one computation and one
	// persist, so the former per-candidate cut points are re-mapped onto
	// the journal persists that exist: the prepared journal, the applying
	// phase, the per-repo apply-progress writes, the parent branch-record
	// writes the apply loop now interleaves, the applied phase, and the
	// closure writes.
	//
	// The 2-repo clean transaction issues these Store.Modify / MarkCodeReady
	// calls in order (the child close write runs through the store's
	// CloseChild primitive, which bypasses the failing wrapper):
	//   1. persist prepared journal (the append candidates)
	//   2. persist applying phase
	//   3. persist apply progress (repo 0)
	//   4. parent branch record (repo 0)
	//   5. persist apply progress (repo 1)
	//   6. parent branch record (repo 1)
	//   7. persist applied phase
	//   8. parent remap + appended layers
	//   9. MarkCodeReady (parent → CodeReady)
	//  10. persist merged phase
	//  11. clear worktree path (repo 0) — error caught as cleanup warning
	//  12. clear worktree path (repo 1) — error caught as cleanup warning
	//  13. record cleanup warning (repo 0)
	//  14. record cleanup warning (repo 1)
	const (
		numCleanRepos             = 2
		writePreparedJournal      = 1
		writeApplyingPhase        = writePreparedJournal + 1
		writeFirstApplyProgress   = writeApplyingPhase + 1
		writeFirstParentRecord    = writeFirstApplyProgress + 1
		writeSecondApplyProgress  = writeFirstParentRecord + 1
		writeSecondParentRecord   = writeSecondApplyProgress + 1
		writeAppliedPhase         = writeSecondParentRecord + 1
		writeParentRemapAndLayers = writeAppliedPhase + 1
		writeParentCodeReady      = writeParentRemapAndLayers + 1
		writeMergedJournal        = writeParentCodeReady + 1
		// 11 and 12 are the clear-worktree-path calls whose errors are
		// caught as warnings; 13 and 14 are the warning persists.
		writeFirstCleanupWarning  = writeMergedJournal + 1 + numCleanRepos
		writeSecondCleanupWarning = writeFirstCleanupWarning + 1
	)
	persistenceCrashCases := []struct {
		name   string
		failAt int32
	}{
		// Crash before the append candidates' single staging persist is
		// durable: nothing on disk, restart re-prepares from scratch.
		{"fail_before_first_candidate_persist", writePreparedJournal},
		// The append candidates (one journal, both repositories) are
		// durable; crash at the applying write.
		{"fail_after_first_candidate_persist", writeApplyingPhase},
		// Both repositories' candidates are durable and apply started;
		// crash at repo 0's apply-progress write — its ref transaction
		// ran but is not durable.
		{"fail_after_both_candidates_persist", writeFirstApplyProgress},
		// Repo 0 is applied and recorded durably; crash at its parent
		// branch-record write, before repo 1's transaction.
		{"fail_after_prepared_persist", writeFirstParentRecord},
		// Repo 0 is fully applied; crash at repo 1's apply-progress write.
		{"fail_after_first_apply_progress", writeSecondApplyProgress},
		// Both apply-progress writes are durable; crash at repo 1's
		// parent branch-record write.
		{"fail_after_second_apply_progress", writeSecondParentRecord},
		// Both repositories are applied durably; crash at the closure
		// write that persists the appended layers onto the parent run.
		{"fail_after_applied_persist", writeParentRemapAndLayers},
		// The appended layers are durable on the parent; crash at the
		// MarkCodeReady write.
		{"fail_after_parent_codeready", writeParentCodeReady},
		// The child is closed (the CloseChild primitive bypasses the
		// wrapper); crash at the merged-journal persist.
		{"fail_after_child_closure", writeMergedJournal},
		// The merged journal is durable; crash at the first
		// clear-worktree-path write in the closure tail (caught as a
		// cleanup warning, so the run still returns nil).
		{"fail_after_merged_journal", writeMergedJournal + 1},
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

			// Verify convergence: the child is Completed, the parent is
			// CodeReady, and every repository's appended branch sits at
			// the child head while the parent branch never moved.
			parent, child := fx.reload()
			if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
				t.Fatalf("fail at %d: close outcome = %q, want completed", tc.failAt, child.Parent.CloseOutcome)
			}
			if parent.Status != feature.StatusCodeReady {
				t.Fatalf("fail at %d: parent status = %s, want CodeReady", tc.failAt, parent.Status)
			}
			tx := child.Parent.Transaction
			if tx == nil || tx.Phase != feature.TransactionPhaseMerged {
				t.Fatalf("fail at %d: transaction phase = %+v, want merged", tc.failAt, tx)
			}
			for i := range fx.repoDirs {
				fx.assertCreatedRefAt(t, i, tx.Entries[i].ChildHeadSHA)
				if got := fx.refSHA(i, "refs/heads/"+fx.parentBranch); got != fx.parentSHAs[i] {
					t.Fatalf("fail at %d: repo %d parent branch ref = %s, want unchanged %s",
						tc.failAt, i, got, fx.parentSHAs[i])
				}
				fx.assertWorktreeOn(t, i, fx.appendedBranch)
			}
		})
	}

	// Rollback persistence-failure crash matrix: inject a Store.Modify
	// failure at each rollback boundary in a 3-repo transaction where an
	// external movement of the third repo's parent branch — the ref the
	// append transaction verifies — triggers semantic rollback. This
	// proves convergence at every crash cut point during rollback,
	// including the aggregate attention-phase write after compensation.
	//
	// The 3-repo rollback scenario issues these Store.Modify calls:
	//   1. persist prepared journal
	//   2. persist applying phase
	//   3-6. apply progress and parent branch record (repos 0 and 1)
	// (repo 2's transaction refuses the moved previous top → rollback)
	//   7. persist rolling_back phase
	//   8. parent branch record restore (repo 0)
	//   9. persist rollback progress (repo 0)
	//  10. parent branch record restore (repo 1)
	//  11. persist rollback progress (repo 1)
	//  12. persist attention phase preserving repo 2's external ref
	const (
		numRollbackRepos = 3
		// Only the first two repositories apply before the raced
		// repository's transaction refuses the moved previous top; each
		// consumes an apply-progress write and a parent-record write.
		rbWriteFirstApplyProgress   = 3
		rbWriteRollingBackPhase     = rbWriteFirstApplyProgress + 2*(numRollbackRepos-1)
		rbWriteFirstRollbackRecord  = rbWriteRollingBackPhase + 1
		rbWriteFirstRollbackWrite   = rbWriteFirstRollbackRecord + 1
		rbWriteSecondRollbackRecord = rbWriteFirstRollbackWrite + 1
		rbWriteSecondRollbackWrite  = rbWriteSecondRollbackRecord + 1
		rbWriteAttentionPhase       = rbWriteSecondRollbackWrite + 1
	)
	rollbackCrashCases := []struct {
		name   string
		failAt int32
	}{
		{"fail_at_rolling_back_phase", rbWriteRollingBackPhase},
		{"fail_after_first_rollback_progress", rbWriteFirstRollbackWrite},
		{"fail_after_second_rollback_progress", rbWriteSecondRollbackWrite},
		{"fail_before_attention_phase", rbWriteAttentionPhase},
	}
	for _, tc := range rollbackCrashCases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newMultiRepoE2EFixture(t, 3)

			// Inject an external movement of the third repo's parent
			// branch — the ref the append transaction verifies — when it
			// is first read; the first two repositories apply before the
			// third's transaction refuses the moved tip.
			raceWT := &raceInjectingWorktrees{
				WorktreeManager: fx.wm,
				t:               t,
				raceRepoDir:     fx.repoDirs[2],
				raceBranch:      fx.parentBranch,
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

			// Remediate the raced repository before the retry: the append
			// transaction can never absorb a parent tip the child's
			// history does not descend from (no replay), so the moved
			// branch is reset to its creation-time base instead of being
			// acknowledged the way a merge candidate would have absorbed
			// it.
			multiRepoGit(t, fx.repoDirs[2], "reset", "--hard", fx.parentSHAs[2])

			// Re-enter the integration boundary to complete the remaining
			// work.
			if err := freshO.RunChildIntegration(fx.child.ID); err != nil {
				t.Fatalf("RunChildIntegration after rollback reconcile (fail at %d): %v", tc.failAt, err)
			}

			// Verify convergence: the child is Completed, the parent is
			// CodeReady, and every repository's appended branch sits at
			// the child head while the parent branches never moved.
			parent, child := fx.reload()
			if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
				t.Fatalf("rollback fail at %d: close outcome = %q, want completed", tc.failAt, child.Parent.CloseOutcome)
			}
			if parent.Status != feature.StatusCodeReady {
				t.Fatalf("rollback fail at %d: parent status = %s, want CodeReady", tc.failAt, parent.Status)
			}
			tx := child.Parent.Transaction
			if tx == nil || tx.Phase != feature.TransactionPhaseMerged {
				t.Fatalf("rollback fail at %d: transaction phase = %+v, want merged", tc.failAt, tx)
			}
			for i := range fx.repoDirs {
				fx.assertCreatedRefAt(t, i, tx.Entries[i].ChildHeadSHA)
				if got := fx.refSHA(i, "refs/heads/"+fx.parentBranch); got != fx.parentSHAs[i] {
					t.Fatalf("rollback fail at %d: repo %d parent branch ref = %s, want unchanged %s",
						tc.failAt, i, got, fx.parentSHAs[i])
				}
				fx.assertWorktreeOn(t, i, fx.appendedBranch)
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

		// Apply both repos' created refs and switch worktrees.
		for i := range journal.Entries {
			fx.manualApplyRef(t, i, &journal.Entries[i])
			journal.Entries[i].ApplyState = feature.RepoApplyApplied
			journal.Entries[i].TopRef().ObservedSHA = journal.Entries[i].TopRef().CandidateSHA
		}

		// Roll back both repos' created refs (delete them without the
		// worktree switch back).
		for i := range journal.Entries {
			fx.manualRollbackRef(t, i, &journal.Entries[i])
			journal.Entries[i].ApplyState = feature.RepoApplyRolledBack
			// A created ref's anchor is absence, so the rolled-back
			// observation is the empty SHA.
			journal.Entries[i].TopRef().ObservedSHA = journal.Entries[i].TopRef().AnchorSHA
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
			journal.Entries[i].TopRef().ObservedSHA = journal.Entries[i].TopRef().CandidateSHA
		}

		// Roll back only repo 0.
		fx.manualRollbackRef(t, 0, &journal.Entries[0])
		journal.Entries[0].ApplyState = feature.RepoApplyRolledBack
		journal.Entries[0].TopRef().ObservedSHA = journal.Entries[0].TopRef().AnchorSHA

		journal.Phase = feature.TransactionPhaseRollingBack
		fx.saveJournal(journal)

		o := fx.orchestrator()
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			t.Fatalf("reconcile: %v", err)
		}

		// Repo 1 should be rolled back by reconciliation: its created ref
		// deleted and its worktree back on the parent branch.
		fx.assertCreatedRefAbsent(t, 1)
		fx.assertWorktreeOn(t, 1, fx.parentBranch)

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

	// Enable auto-publish; the parent already carries its one-layer stack
	// from the fixture, and closure appends the child's layer as position
	// 2, so the publish walk has layer entries to record pull requests on.
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
	// The hook records a pull request for every layer of the (now
	// two-layer) stack — the parent's own layer and the appended layer —
	// exactly as the real walk does, so the all-published check settles
	// and the parent reaches Published.
	var publishCount int32
	o.SetPublishRepoFn(func(featureID, repoName string) error {
		atomic.AddInt32(&publishCount, 1)
		for _, position := range []int{1, 2} {
			if err := fx.mgr.RecordStackLayerPR(featureID, repoName, position,
				fmt.Sprintf("https://github.com/test/%s/pull/%d", repoName, position), ""); err != nil {
				return err
			}
		}
		return nil
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

	// The appended layer's pull request is recorded on the parent's stack.
	if len(parent.Stack) != 2 {
		t.Fatalf("parent stack layers = %d, want 2 after closure", len(parent.Stack))
	}
	for i := range parent.Repos {
		if got := parent.Stack[1].Repos[parent.Repos[i].Name].PRURL; got == "" {
			t.Fatalf("repo %d: appended layer pull request not recorded", i)
		}
	}

	// One cleanup warning should be recorded (the failed repo).
	warningCount := 0
	tx := child.Parent.Transaction
	if tx != nil {
		for i := range tx.Entries {
			if tx.Entries[i].Cleanup != nil {
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
			if tx.Entries[i].Cleanup != nil {
				warningCount++
			}
		}
	}
	if warningCount != 0 {
		t.Fatalf("cleanup warning count after retry = %d, want 0 (all cleaned up)", warningCount)
	}

	// Every repository's appended branch sits at the child head, the
	// parent branch never moved, and the worktree is on the appended
	// branch.
	for i := range fx.repoDirs {
		fx.assertCreatedRefAt(t, i, tx.Entries[i].ChildHeadSHA)
		if got := fx.refSHA(i, "refs/heads/"+fx.parentBranch); got != fx.parentSHAs[i] {
			t.Fatalf("repo %d: parent branch ref = %s, want unchanged %s", i, got, fx.parentSHAs[i])
		}
		fx.assertWorktreeOn(t, i, fx.appendedBranch)
	}

	// Transaction is merged.
	if tx == nil || tx.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %+v, want merged", tx)
	}
}
