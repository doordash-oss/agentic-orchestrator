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

// Real-git coverage of the rebase mechanical integration gate: the
// kind-specific pre-prepare step re-verifies the persisted creation-time
// targets before any candidate or ref is touched, parking attention with
// typed per-repo diagnostics on any violation while leaving parent refs
// byte-identical.

package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// rebaseGateRepo is one repository in a rebase gate fixture.
type rebaseGateRepo struct {
	name        string
	repoDir     string
	branch      string // parent (feature) branch checked out at repoDir
	childWT     string // disposable child worktree path
	childBranch string
	baseBranch  string
	parentBase  string // captured parent tip SHA (fork point)
	mainTip     string // the target branch tip to persist as TargetSHA
}

// rebaseGateFixture builds real-git parent/child pairs for rebase gate tests.
// Each repo has a feature branch (the parent) and a child worktree on a child
// branch pinned at the parent tip. The persisted RebaseTargets/RebaseWorkRepos
// are set explicitly by each test via setRebaseTargets.
type rebaseGateFixture struct {
	t        *testing.T
	repos    []rebaseGateRepo
	store    *feature.Store
	mgr      *feature.Manager
	wm       *git.WorktreeManager
	parent   *feature.Feature
	child    *feature.Feature
	parentID string
	childID  string
}

// newRebaseGateFixture creates a single-repo rebase gate fixture whose child
// branch is forked at the parent tip and optionally merged with the target so
// the gate's ancestor check holds. The target (main) is advanced one commit
// beyond the fork point so a non-merged child fails the ancestor check.
func newRebaseGateFixture(t *testing.T, mergeTarget bool) *rebaseGateFixture {
	t.Helper()
	return newRebaseGateFixtureN(t, mergeTarget, 1)
}

func newRebaseGateFixtureN(t *testing.T, mergeTarget bool, n int) *rebaseGateFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("real-git rebase gate test")
	}
	fx := &rebaseGateFixture{t: t, parentID: "rebase-gate-parent", childID: "rebase-gate-child"}

	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	wm := git.NewWorktreeManager(t.TempDir())
	mgr := feature.NewManager(store, config.NewDefault())
	mgr.Worktrees = wm
	fx.store = store
	fx.mgr = mgr
	fx.wm = wm

	parentRepos := make([]feature.FeatureRepo, 0, n)
	childRepos := make([]feature.FeatureRepo, 0, n)
	var targets []feature.RebaseRepoTarget
	var behind []string

	for i := 0; i < n; i++ {
		repoName := "repoA"
		if n > 1 {
			repoName = "repo" + string([]byte{byte('A' + i)})
		}
		repoDir := testutil.InitGitRepo(t)
		// Feature branch off main.
		childIntegrationGit(t, repoDir, "checkout", "-b", "feature/parent")
		testutil.CommitFile(t, repoDir, "base.txt", "v1\n", "parent base")
		parentBase := childIntegrationGit(t, repoDir, "rev-parse", "HEAD")

		// Advance main (the target) one commit so the feature branch is behind.
		childIntegrationGit(t, repoDir, "checkout", "main")
		testutil.CommitFile(t, repoDir, "upstream.txt", "u\n", "upstream advancement")
		mainTip := childIntegrationGit(t, repoDir, "rev-parse", "HEAD")
		childIntegrationGit(t, repoDir, "checkout", "feature/parent")

		// Child worktree pinned at the parent tip.
		childWT := t.TempDir() + "/" + repoName + "-child-wt"
		childBranch := "feature/rebase-gate-child-" + repoName
		childIntegrationGit(t, repoDir, "worktree", "add", "-b", childBranch, childWT, parentBase)
		// One committed child change.
		testutil.CommitFile(t, childWT, "child.txt", "child work\n", "child change")

		// Optionally merge the target into the child branch so the gate's
		// ancestor check holds (happy path). The merge is clean because the
		// two sides touch disjoint files.
		if mergeTarget {
			rgGateMerge(t, childWT, mainTip, "merge target into child")
		}

		repo := rebaseGateRepo{
			name: repoName, repoDir: repoDir, branch: "feature/parent",
			childWT: childWT, childBranch: childBranch, baseBranch: "main",
			parentBase: parentBase, mainTip: mainTip,
		}
		fx.repos = append(fx.repos, repo)

		publishable := false
		parentRepos = append(parentRepos, feature.FeatureRepo{
			Name: repoName, Path: repoDir, WorktreePath: repoDir,
			Branch: "feature/parent", BaseBranch: "main", Publishable: &publishable,
		})
		childRepos = append(childRepos, feature.FeatureRepo{
			Name: repoName, Path: repoDir, WorktreePath: childWT,
			Branch: childBranch, BaseBranch: "main",
		})
		targets = append(targets, feature.RebaseRepoTarget{
			Repo: repoName, Target: "main", Ref: "main",
			Publishable: false, TargetSHA: mainTip,
		})
		behind = append(behind, repoName)
	}

	parent := &feature.Feature{
		ID: fx.parentID, Name: "Rebase gate parent", Slug: fx.parentID,
		Status: feature.StatusPublished, CurrentPhase: feature.PhasePublish,
		Created: time.Now(), ActiveRun: 1, RunCount: 1,
		Repos: parentRepos, RepoStates: map[string]*feature.RepoState{},
		Checkpoints:   feature.Checkpoints{ManualPublish: true},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	child := &feature.Feature{
		ID: fx.childID, Name: "Rebase gate child", Slug: fx.childID,
		Status: feature.StatusReviewPassed, CurrentPhase: feature.PhaseFinalReview,
		Pipeline: feature.PipelineMedium, Created: time.Now(),
		ActiveRun: 1, RunCount: 1, Repos: childRepos,
		RepoStates:    map[string]*feature.RepoState{},
		SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID, Kind: feature.ChildKindRebase,
			Bases:           fx.bases(),
			RebaseTargets:   targets,
			RebaseWorkRepos: behind,
		},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}
	fx.parent = parent
	fx.child = child
	return fx
}

// bases returns the per-repo launch tips for the persisted relationship.
func (fx *rebaseGateFixture) bases() []feature.ChildRepoBase {
	var out []feature.ChildRepoBase
	for _, r := range fx.repos {
		out = append(out, feature.ChildRepoBase{Repo: r.name, SHA: r.parentBase, ParentBranch: r.branch})
	}
	return out
}

func (fx *rebaseGateFixture) orchestrator() *Orchestrator {
	return New(Deps{Lifecycle: fx.mgr, Store: fx.store, Worktrees: fx.wm}, Hooks{})
}

func (fx *rebaseGateFixture) reload() (*feature.Feature, *feature.Feature) {
	fx.t.Helper()
	parent, err := fx.store.Load(fx.parentID)
	if err != nil {
		fx.t.Fatalf("reload parent: %v", err)
	}
	child, err := fx.store.Load(fx.childID)
	if err != nil {
		fx.t.Fatalf("reload child: %v", err)
	}
	return parent, child
}

// setRebaseTargets rewrites the persisted rebase targets/behind set on the
// child, used by tests that need to alter the persisted creation-time decision
// (e.g. drop the TargetSHA to exercise the missing-SHA path).
func (fx *rebaseGateFixture) setRebaseTargets(targets []feature.RebaseRepoTarget, behind []string) {
	fx.t.Helper()
	if err := fx.store.Modify(fx.childID, func(f *feature.Feature) error {
		f.Parent.RebaseTargets = append([]feature.RebaseRepoTarget(nil), targets...)
		f.Parent.RebaseWorkRepos = append([]string(nil), behind...)
		return nil
	}); err != nil {
		fx.t.Fatalf("setRebaseTargets: %v", err)
	}
}

// parentRefSHA returns the current parent branch ref SHA for repo i.
func (fx *rebaseGateFixture) parentRefSHA(i int) string {
	fx.t.Helper()
	return childIntegrationGit(fx.t, fx.repos[i].repoDir, "rev-parse", fx.repos[i].branch)
}

// TestRebaseGate_AncestorFailure verifies a rebase child whose branch does not
// contain its persisted target commit fails integration with a typed
// not-ancestor attention record, and every parent ref is byte-identical
// before and after.
func TestRebaseGate_AncestorFailure(t *testing.T) {
	fx := newRebaseGateFixture(t, false) // do not merge target -> ancestor check fails
	before := fx.parentRefSHA(0)

	o := fx.orchestrator()
	if err := o.RunChildIntegration(fx.childID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v; want nil (attention parks, not a returned error)", err)
	}

	_, child := fx.reload()
	journal := child.Parent.Transaction
	if journal == nil || journal.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction = %+v, want attention phase", journal)
	}
	if len(journal.Entries) != 1 {
		t.Fatalf("journal entries = %d, want 1", len(journal.Entries))
	}
	e := journal.Entries[0]
	if journal.Attention == nil || journal.Attention.Code != errcat.RebaseGateNotAncestor {
		t.Errorf("attention record = %+v, want rebase_gate_not_ancestor", journal.Attention)
	} else {
		if journal.Attention.Context == nil || len(journal.Attention.Context.Repositories) != 1 ||
			journal.Attention.Context.Repositories[0].Name != e.Repo {
			t.Errorf("attention repositories = %+v, want the violated repo", journal.Attention.Context)
		}
		if !strings.Contains(journal.Attention.Diagnostics, "not an ancestor") {
			t.Errorf("diagnostics = %q, want 'not an ancestor'", journal.Attention.Diagnostics)
		}
	}
	if e.PrepState != feature.RepoPrepFailed {
		t.Errorf("prep state = %q, want %q", e.PrepState, feature.RepoPrepFailed)
	}
	// The transaction-attention mirror into the child's run failure state is
	// gone: the journal owns the attention diagnostics.
	if rec := child.FailureRecord(); rec != nil {
		t.Errorf("child failure record = %+v, want none (journal owns attention diagnostics)", rec)
	}

	// Parent ref byte-identical before and after.
	if after := fx.parentRefSHA(0); after != before {
		t.Errorf("parent ref changed: before=%s after=%s; gate must not touch parent refs", before, after)
	}
}

// TestRebaseGate_ConflictMarkersFailure verifies a child carrying literal
// conflict markers in a tracked file aborts with the typed conflict-markers
// attention record and refs untouched.
func TestRebaseGate_ConflictMarkersFailure(t *testing.T) {
	fx := newRebaseGateFixture(t, true) // merge target so the ancestor check passes
	before := fx.parentRefSHA(0)

	// Write literal conflict markers into a tracked file in the child worktree
	// and commit them so the marker scan (which searches tracked files) finds
	// them. Construct markers from split strings so this test source does not
	// carry the literal sequences.
	marked := strings.Join([]string{
		"line before",
		"<" + "<<<<" + "<< ours",
		"our change",
		"=" + "=====" + "=",
		"their change",
		">" + ">>>>" + ">> theirs",
		"line after",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(fx.repos[0].childWT, "conflicted.txt"), []byte(marked), 0o644); err != nil {
		t.Fatalf("write conflicted file: %v", err)
	}
	childIntegrationGit(t, fx.repos[0].childWT, "add", "conflicted.txt")
	childIntegrationGit(t, fx.repos[0].childWT, "commit", "-m", "add conflict markers")

	o := fx.orchestrator()
	if err := o.RunChildIntegration(fx.childID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v; want nil", err)
	}

	_, child := fx.reload()
	journal := child.Parent.Transaction
	if journal == nil || journal.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction = %+v, want attention", journal)
	}
	if journal.Attention == nil || journal.Attention.Code != errcat.RebaseGateConflictMarkers {
		t.Errorf("attention record = %+v, want rebase_gate_conflict_markers", journal.Attention)
	} else if journal.Attention.Context == nil || len(journal.Attention.Context.Repositories) != 1 ||
		len(journal.Attention.Context.Repositories[0].ConflictFiles) == 0 {
		t.Errorf("attention repositories = %+v, want the marked file listed", journal.Attention.Context)
	}
	if after := fx.parentRefSHA(0); after != before {
		t.Errorf("parent ref changed: before=%s after=%s", before, after)
	}
}

// TestRebaseGate_MissingTargetSHAFailsClosed verifies a behind-repo target
// without a persisted creation-time SHA fails closed with its own typed
// diagnostic.
func TestRebaseGate_MissingTargetSHAFailsClosed(t *testing.T) {
	fx := newRebaseGateFixture(t, true)
	before := fx.parentRefSHA(0)

	// Drop the persisted TargetSHA to simulate an in-flight child created
	// before this phase landed.
	targets := append([]feature.RebaseRepoTarget(nil), fx.child.Parent.RebaseTargets...)
	targets[0].TargetSHA = ""
	fx.setRebaseTargets(targets, fx.child.Parent.RebaseWorkRepos)

	o := fx.orchestrator()
	if err := o.RunChildIntegration(fx.childID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v; want nil", err)
	}

	_, child := fx.reload()
	journal := child.Parent.Transaction
	if journal == nil || journal.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction = %+v, want attention", journal)
	}
	if journal.Attention == nil || journal.Attention.Code != errcat.RebaseGateTargetMissing {
		t.Errorf("attention record = %+v, want rebase_gate_target_missing", journal.Attention)
	}
	if after := fx.parentRefSHA(0); after != before {
		t.Errorf("parent ref changed: before=%s after=%s", before, after)
	}
}

// TestRebaseGate_TargetMovedStillPasses verifies the gate reads persisted
// targets only: advancing the target branch (local ref) after creation does
// not change what the gate checks — a child worktree that contains the
// creation-time target SHA passes even after the target advances, and the
// integration lands through the persisted restack result.
func TestRebaseGate_TargetMovedStillPasses(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{})
	o := fx.orchestrator()

	// Run the harness restack and land the child at the approved-shaped
	// state a finished verification round leaves behind.
	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("StartFeature() error = %v", err)
	}
	if err := fx.store.Modify(fx.childID, func(f *feature.Feature) error {
		f.Status = feature.StatusReviewPassed
		return nil
	}); err != nil {
		t.Fatalf("mark the child review-passed: %v", err)
	}

	// Advance the local target branch (main) past the creation-time SHA so
	// the ref no longer matches the persisted TargetSHA. The gate must still
	// check the persisted SHA and pass.
	restackCheckout(t, fx.repoDir, "main")
	testutil.CommitFile(t, fx.repoDir, "more_upstream.txt", "u2\n", "further upstream")
	restackCheckout(t, fx.repoDir, "stack/3")
	if got := restackGit(t, fx.repoDir, "rev-parse", "main"); got == fx.targetSHA {
		t.Fatalf("setup invariant: main did not advance past creation-time tip")
	}

	if err := o.RunChildIntegration(fx.childID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v, want nil (gate should pass)", err)
	}

	_, child := fx.reload()
	if child.Parent.Transaction == nil || child.Parent.Transaction.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %+v, want merged (gate passed, integration landed)", child.Parent.Transaction)
	}
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Errorf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
}

// TestRebaseGate_OnlyBehindReposGated verifies up-to-date repos (not in the
// persisted work list) are neither gated nor rewritten: a two-repository
// child with one work repository lands with the pass-through repository's
// ref untouched and a pass-through transaction entry.
func TestRebaseGate_OnlyBehindReposGated(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{WithPassThrough: true})
	o := fx.orchestrator()

	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("StartFeature() error = %v", err)
	}
	if err := fx.store.Modify(fx.childID, func(f *feature.Feature) error {
		f.Status = feature.StatusReviewPassed
		return nil
	}); err != nil {
		t.Fatalf("mark the child review-passed: %v", err)
	}
	repoBHeadBefore := restackGit(t, fx.passRepoDir, "rev-parse", "feature/pass")

	if err := o.RunChildIntegration(fx.childID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v, want nil", err)
	}

	_, child := fx.reload()
	if child.Parent.Transaction == nil || child.Parent.Transaction.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %+v, want merged", child.Parent.Transaction)
	}
	// repoA's parent worktree now sits on the rebuilt top: a linear chain
	// from the target (no merge commit anywhere in the new history).
	if !git.IsAncestor(fx.repoDir, fx.targetSHA, "HEAD") {
		t.Fatal("repoA parent HEAD does not descend from the target after integration")
	}
	if repoBHeadAfter := restackGit(t, fx.passRepoDir, "rev-parse", "feature/pass"); repoBHeadAfter != repoBHeadBefore {
		t.Errorf("repoB ref changed: before=%s after=%s", repoBHeadBefore, repoBHeadAfter)
	}
	entry := child.Parent.Transaction.EntryByRepo("repoB")
	if entry == nil {
		t.Fatalf("repoB transaction entry missing: %+v", child.Parent.Transaction)
	}
	if top := entry.TopRef(); top == nil || top.CandidateSHA != repoBHeadBefore || top.ObservedSHA != repoBHeadBefore {
		t.Errorf("repoB transaction refs=%+v, want pass-through SHA %s", entry.Refs, repoBHeadBefore)
	}
}

// TestRebaseGate_NonRebaseKindNotGated verifies refactor and review-feedback
// integration journeys flow through unchanged (no gate applied).
func TestRebaseGate_NonRebaseKindNotGated(t *testing.T) {
	// Reuse the existing refactor integration fixture: a refactor child that
	// does not contain any "target" must still integrate, proving the gate is
	// not applied to non-rebase kinds.
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v; want nil for refactor child", err)
	}
	_, child := fx.reload()
	if child.Parent.Transaction == nil || child.Parent.Transaction.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("refactor transaction phase = %+v, want merged (gate must not apply)", child.Parent.Transaction)
	}
}

// rgGateMerge merges a commit into the current branch of the worktree with a
// no-ff merge commit, using the test git identity.
func rgGateMerge(t *testing.T, worktreePath, commit, message string) {
	t.Helper()
	cmd := exec.Command("git", "-C", worktreePath, "merge", "--no-ff", "-m", message, commit)
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git merge %s: %v\n%s", commit, err, out)
	}
}
