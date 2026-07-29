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

// Real-git coverage of the child integration boundary: durable
// anchors, cleanliness preflight, the explicit no-fast-forward merge,
// conflict/dirty attention with retry, idempotent closure, cleanup, and the
// close-before-publish ordering with both publish outcomes.

package orchestrator

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

type childIntegrationFixture struct {
	t             *testing.T
	repoDir       string
	childWorktree string
	childBranch   string
	parentBranch  string
	parentBaseSHA string
	store         *feature.Store
	mgr           *feature.Manager
	wm            *git.WorktreeManager
	parent        *feature.Feature
	child         *feature.Feature
}

// newChildIntegrationFixture builds a real single-repository parent/child
// pair: the parent branch is checked out at repoDir, the child branch lives
// in a disposable worktree pinned at the captured parent base with one
// committed child change plus one uncommitted change (to prove integration
// commits remaining work before touching the parent).
func newChildIntegrationFixture(t *testing.T, parentStatus feature.Status, manualPublish bool) *childIntegrationFixture {
	t.Helper()
	repoDir := testutil.InitGitRepo(t)
	childIntegrationGit(t, repoDir, "checkout", "-b", "feature/parent")
	testutil.CommitFile(t, repoDir, "base.txt", "v1\n", "parent base")
	parentBaseSHA := childIntegrationGit(t, repoDir, "rev-parse", "HEAD")

	childWorktree := t.TempDir() + "/child-wt"
	childBranch := "feature/child-integ"
	childIntegrationGit(t, repoDir, "worktree", "add", "-b", childBranch, childWorktree, parentBaseSHA)
	testutil.CommitFile(t, childWorktree, "child.txt", "child work\n", "child change")
	if err := os.WriteFile(childWorktree+"/pending.txt", []byte("not yet committed\n"), 0o644); err != nil {
		t.Fatalf("write pending file: %v", err)
	}

	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	publishable := true
	parent := &feature.Feature{
		ID:           "parent-1",
		Name:         "Parent",
		Slug:         "parent-1",
		Status:       parentStatus,
		CurrentPhase: feature.PhasePublish,
		Created:      time.Now(),
		ActiveRun:    1,
		RunCount:     1,
		// ManualPublish=true → parent.Checkpoints.AutoPublish() == false.
		Checkpoints: feature.Checkpoints{ManualPublish: manualPublish},
		Repos: []feature.FeatureRepo{{
			Name:         "repoA",
			Path:         repoDir,
			WorktreePath: repoDir,
			Branch:       "feature/parent",
			BaseBranch:   "main",
			Publishable:  &publishable,
		}},
		RepoStates:    map[string]*feature.RepoState{"repoA": {Touched: true}},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	child := &feature.Feature{
		ID:           "child-1",
		Name:         "Child",
		Slug:         "child-1",
		Status:       feature.StatusReviewPassed,
		CurrentPhase: feature.PhaseFinalReview,
		Pipeline:     feature.PipelineMedium,
		Created:      time.Now(),
		ActiveRun:    1,
		RunCount:     1,
		Repos: []feature.FeatureRepo{{
			Name:         "repoA",
			Path:         repoDir,
			WorktreePath: childWorktree,
			Branch:       childBranch,
			BaseBranch:   "main",
		}},
		RepoStates: map[string]*feature.RepoState{"repoA": {Touched: true}},
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID,
			Kind:     feature.ChildKindRefactor,
			Bases:    []feature.ChildRepoBase{{Repo: "repoA", SHA: parentBaseSHA, ParentBranch: "feature/parent"}},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
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
	return &childIntegrationFixture{
		t: t, repoDir: repoDir, childWorktree: childWorktree, childBranch: childBranch,
		parentBranch: "feature/parent", parentBaseSHA: parentBaseSHA,
		store: store, mgr: mgr, wm: wm, parent: parent, child: child,
	}
}

func (fx *childIntegrationFixture) orchestrator() *Orchestrator {
	return New(Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Publisher: &git.PublishAdapter{},
		Worktrees: fx.wm,
		Cleanliness: feature.CleanlinessFunc(func(worktreePath string, maxPerCategory int) (*feature.RepoCleanliness, error) {
			report, err := fx.wm.InspectCleanliness(worktreePath, maxPerCategory)
			if report == nil || err != nil {
				return nil, err
			}
			return &feature.RepoCleanliness{
				Staged: report.Staged, Unstaged: report.Unstaged, Untracked: report.Untracked,
				StagedTotal: report.StagedTotal, UnstagedTotal: report.UnstagedTotal, UntrackedTotal: report.UntrackedTotal,
			}, nil
		}),
	}, Hooks{})
}

func (fx *childIntegrationFixture) reload() (*feature.Feature, *feature.Feature) {
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

func childIntegrationGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := execCommandGit(dir, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func execCommandGit(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	return cmd
}

// TestChildIntegrationHappyPath proves the full boundary: remaining child
// work is committed, the child head is recorded before the parent moves, a
// two-parent no-fast-forward merge lands on the recorded parent branch, the
// child closes Completed with the merge HEAD and timestamp, the parent moves
// to CodeReady (manual publish never blocked), and the disposable child
// worktree and branch are removed.
func TestChildIntegrationHappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()

	if err := o.runChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v", err)
	}

	parent, child := fx.reload()
	// Parent branch now carries an explicit merge commit with two parents.
	parents := childIntegrationGit(t, fx.repoDir, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("parent merge commit parents = %q, want two parents", parents)
	}
	if _, err := os.Stat(fx.repoDir + "/child.txt"); err != nil {
		t.Fatalf("merged child content missing: %v", err)
	}
	if _, err := os.Stat(fx.repoDir + "/pending.txt"); err != nil {
		t.Fatalf("previously-uncommitted child content missing from merge: %v", err)
	}

	// Child closure record: outcome, timestamp, integration anchors, merge
	// HEAD matching the parent tip, no cleanup warning.
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want %q", child.Parent.CloseOutcome, feature.ChildCloseOutcomeCompleted)
	}
	if child.Parent.ClosedAt == nil {
		t.Fatal("child closed_at not recorded")
	}
	integ := child.Parent.Integration
	if integ == nil {
		t.Fatal("child integration record missing")
	}
	if integ.ParentBranch != fx.parentBranch || integ.ParentAnchorSHA == "" || integ.ChildHeadSHA == "" {
		t.Fatalf("integration anchors incomplete: %+v", integ)
	}
	mergeHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
	if integ.MergeHEAD != mergeHEAD {
		t.Fatalf("integration merge head = %s, want parent tip %s", integ.MergeHEAD, mergeHEAD)
	}
	if integ.CleanupWarning != "" {
		t.Fatalf("unexpected cleanup warning: %q", integ.CleanupWarning)
	}

	// Manual-publish parent stays CodeReady; the child never published.
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady", parent.Status)
	}
	if child.Status == feature.StatusCodeReady || child.Status == feature.StatusPublished {
		t.Fatalf("child reached delivery status %s; children never deliver", child.Status)
	}

	// Disposable resources are gone.
	if _, err := os.Stat(fx.childWorktree); !os.IsNotExist(err) {
		t.Fatalf("child worktree still present: %v", err)
	}
	if branches := childIntegrationGit(t, fx.repoDir, "branch", "--list", fx.childBranch); branches != "" {
		t.Fatalf("child branch %s still present", fx.childBranch)
	}
	if child.Repos[0].WorktreePath != "" {
		t.Fatalf("child durable worktree path %q not cleared", child.Repos[0].WorktreePath)
	}
}

// TestChildIntegrationDirtyParentBlocksMerge proves the parent cleanliness
// recheck blocks the merge with categorized diagnostics and leaves the
// parent ref untouched, then a Restart-driven retry after remediation
// completes the integration without rerunning any pipeline phase.
func TestChildIntegrationDirtyParentBlocksMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()

	if err := os.WriteFile(fx.repoDir+"/stray.txt", []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty parent: %v", err)
	}
	preHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")

	if err := o.runChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil with recorded attention", err)
	}
	if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != preHEAD {
		t.Fatalf("parent ref = %s, want unchanged %s on dirty preflight", got, preHEAD)
	}

	_, child := fx.reload()
	integ := child.Parent.Integration
	if integ == nil || integ.Phase != feature.ChildIntegrationPhaseAttention {
		t.Fatalf("integration phase = %+v, want attention", integ)
	}
	if integ.ChildHeadSHA == "" || integ.ParentAnchorSHA == "" {
		t.Fatalf("anchors must be durable before parent mutation: %+v", integ)
	}
	if integ.MergeHEAD != "" {
		t.Fatalf("merge head recorded (%s) although the merge was blocked", integ.MergeHEAD)
	}
	if len(integ.Dirty) != 1 || integ.Dirty[0].UntrackedTotal != 1 {
		t.Fatalf("dirty diagnostics = %+v, want categorized untracked entry", integ.Dirty)
	}
	if !strings.Contains(integ.Attention, "uncommitted changes") {
		t.Fatalf("attention = %q, want dirty summary", integ.Attention)
	}
	if child.Parent.CloseOutcome != "" || !child.IsActiveChild() {
		t.Fatalf("child closed (%q) on blocked integration", child.Parent.CloseOutcome)
	}

	// Remediate the dirty worktree, then Restart replays integration.
	if err := os.Remove(fx.repoDir + "/stray.txt"); err != nil {
		t.Fatalf("clean parent: %v", err)
	}
	outcome, err := fx.orchestrator().RestartPhase(fx.child.ID, 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase() error = %v", err)
	}
	if outcome.Action != RestartNoOp {
		t.Fatalf("RestartPhase() action = %v, want RestartNoOp (no phase replay)", outcome.Action)
	}

	parent, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q after restart, want completed", child.Parent.CloseOutcome)
	}
	parents := childIntegrationGit(t, fx.repoDir, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("parent merge commit parents = %q, want two parents after retry", parents)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady", parent.Status)
	}
}

// TestChildIntegrationConflictAttentionAndRetry proves a conflicting merge
// aborts cleanly (parent ref at its anchor, child branch preserved, no
// leftover merge state), records structured attention, and a retry after
// the divergence is resolved integrates normally.
func TestChildIntegrationConflictAttentionAndRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()

	// Conflict on child.txt: the child added it, the parent adds a different
	// file with the same name.
	testutil.CommitFile(t, fx.repoDir, "child.txt", "parent-side conflict\n", "conflicting parent commit")
	preHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")

	if err := o.runChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil with recorded attention", err)
	}
	if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != preHEAD {
		t.Fatalf("parent ref = %s, want unchanged %s after conflict abort", got, preHEAD)
	}
	if status := childIntegrationGit(t, fx.repoDir, "status", "--porcelain"); status != "" {
		t.Fatalf("parent worktree not clean after abort: %q", status)
	}

	_, child := fx.reload()
	integ := child.Parent.Integration
	if integ == nil || integ.Phase != feature.ChildIntegrationPhaseAttention {
		t.Fatalf("integration phase = %+v, want attention", integ)
	}
	if !strings.Contains(integ.Attention, "merge") {
		t.Fatalf("attention = %q, want merge failure detail", integ.Attention)
	}
	if child.Parent.CloseOutcome != "" {
		t.Fatalf("child closed on conflicted integration")
	}
	// Child branch and worktree survive.
	if branches := childIntegrationGit(t, fx.repoDir, "branch", "--list", fx.childBranch); branches == "" {
		t.Fatal("child branch was deleted on conflicted integration")
	}

	// Resolve the divergence on the parent side and retry via Restart.
	childIntegrationGit(t, fx.repoDir, "reset", "--hard", preHEAD+"^")
	outcome, err := fx.orchestrator().RestartPhase(fx.child.ID, 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase() error = %v", err)
	}
	if outcome.Action != RestartNoOp {
		t.Fatalf("RestartPhase() action = %v, want RestartNoOp", outcome.Action)
	}
	_, child = fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q after conflict retry, want completed", child.Parent.CloseOutcome)
	}
	if child.Parent.Integration.MergeHEAD == "" {
		t.Fatal("merge head not recorded after retry")
	}
}

// TestChildIntegrationRefusals pins the durable refusal conditions: a closed
// relationship, a mismatched parent repository, a multi-repository child,
// and a not-yet-approved final review never mutate the parent.
func TestChildIntegrationRefusals(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}

	t.Run("closed relationship", func(t *testing.T) {
		fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
		preHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
		if err := fx.store.Modify(fx.child.ID, func(f *feature.Feature) error {
			f.Parent.CloseOutcome = feature.ChildCloseOutcomeCompleted
			return nil
		}); err != nil {
			t.Fatalf("close child: %v", err)
		}
		err := fx.orchestrator().runChildIntegration(fx.child.ID)
		if !isIntegrationRefused(err) {
			t.Fatalf("runChildIntegration() error = %v, want refusal", err)
		}
		if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != preHEAD {
			t.Fatalf("parent ref moved on a closed relationship: %s", got)
		}
	})

	t.Run("final review not approved", func(t *testing.T) {
		fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
		preHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
		if err := fx.store.Modify(fx.child.ID, func(f *feature.Feature) error {
			f.Status = feature.StatusImplementing
			return nil
		}); err != nil {
			t.Fatalf("set child status: %v", err)
		}
		err := fx.orchestrator().runChildIntegration(fx.child.ID)
		if !isIntegrationRefused(err) {
			t.Fatalf("runChildIntegration() error = %v, want refusal", err)
		}
		if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != preHEAD {
			t.Fatalf("parent ref moved without an approved final review: %s", got)
		}
	})

	t.Run("multi-repository child", func(t *testing.T) {
		fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
		preHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
		if err := fx.store.Modify(fx.child.ID, func(f *feature.Feature) error {
			f.Repos = append(f.Repos, feature.FeatureRepo{Name: "repoB", Path: t.TempDir(), Branch: "b"})
			return nil
		}); err != nil {
			t.Fatalf("add repo: %v", err)
		}
		err := fx.orchestrator().runChildIntegration(fx.child.ID)
		if !isIntegrationRefused(err) {
			t.Fatalf("runChildIntegration() error = %v, want refusal", err)
		}
		if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != preHEAD {
			t.Fatalf("parent ref moved for a multi-repository child: %s", got)
		}
	})

	t.Run("parent repository mismatch", func(t *testing.T) {
		fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
		preHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
		if err := fx.store.Modify(fx.parent.ID, func(f *feature.Feature) error {
			f.Repos[0].Name = "renamed"
			return nil
		}); err != nil {
			t.Fatalf("rename parent repo: %v", err)
		}
		err := fx.orchestrator().runChildIntegration(fx.child.ID)
		if !isIntegrationRefused(err) {
			t.Fatalf("runChildIntegration() error = %v, want refusal", err)
		}
		if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != preHEAD {
			t.Fatalf("parent ref moved on repository mismatch: %s", got)
		}
	})
}

// TestChildIntegrationRepeatedCompletionIsIdempotent proves a second
// integration pass over a fully settled relationship is an idempotent no-op:
// it never creates another merge, never closes twice, and never regresses
// either record.
func TestChildIntegrationRepeatedCompletionIsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()
	if err := o.runChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v", err)
	}
	mergeHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
	_, child := fx.reload()
	closedAt := child.Parent.ClosedAt

	if err := o.runChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("second runChildIntegration() error = %v, want idempotent no-op on the settled relationship", err)
	}
	if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != mergeHEAD {
		t.Fatalf("second completion created another merge: %s != %s", got, mergeHEAD)
	}
	parent, child := fx.reload()
	if child.Parent.ClosedAt == nil || !child.Parent.ClosedAt.Equal(*closedAt) {
		t.Fatalf("closed_at changed across repeated completion: %v != %v", child.Parent.ClosedAt, closedAt)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s after repeated completion, want CodeReady", parent.Status)
	}
}

// failingRemoveWorktrees wraps the real worktree manager and fails Remove,
// simulating a transient cleanup failure after the merge is durable.
type failingRemoveWorktrees struct {
	*git.WorktreeManager
	removeErr error
}

func (w failingRemoveWorktrees) Remove(worktreePath string, deleteBranch bool) error {
	return w.removeErr
}

// RemoveRef must fail too: the orchestrator prefers the identity-carrying
// entrypoint when it is available, and the embedded manager would otherwise
// silently bypass the injected failure.
func (w failingRemoveWorktrees) RemoveRef(worktreePath, mainRepo, branch string) error {
	return w.removeErr
}

// TestChildIntegrationCleanupWarningNonFatal proves cleanup failure after a
// durable merge records a warning, leaves the child Completed and the parent
// CodeReady, and is retried through the production Restart path: the closed
// child re-enters only its impermanent closure tail, the cleanup retry
// settles and clears the warning, and neither the parent branch nor the
// integrated commits are touched.
func TestChildIntegrationCleanupWarningNonFatal(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()
	o.deps.Worktrees = failingRemoveWorktrees{WorktreeManager: fx.wm, removeErr: errors.New("simulated worktree removal failure")}

	if err := o.runChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v", err)
	}
	mergeHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")

	parent, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed despite cleanup failure", child.Parent.CloseOutcome)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady", parent.Status)
	}
	if child.Parent.Integration == nil || child.Parent.Integration.CleanupWarning == "" {
		t.Fatalf("cleanup warning not recorded: %+v", child.Parent.Integration)
	}
	// The merge boundary is durable and complete.
	parents := childIntegrationGit(t, fx.repoDir, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("parent merge commit parents = %q, want two parents", parents)
	}

	// Retry through the production restart entrypoint: a completed child
	// with an unsettled closure tail re-enters only that tail.
	outcome, err := fx.orchestrator().RestartPhase(fx.child.ID, 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase() error = %v", err)
	}
	if outcome.Action != RestartNoOp {
		t.Fatalf("RestartPhase() action = %v, want RestartNoOp (closure tail resume)", outcome.Action)
	}

	parent, child = fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q after cleanup retry, want still completed", child.Parent.CloseOutcome)
	}
	if child.Parent.Integration.CleanupWarning != "" {
		t.Fatalf("cleanup warning not cleared by retry: %q", child.Parent.Integration.CleanupWarning)
	}
	if child.Repos[0].WorktreePath != "" {
		t.Fatalf("child durable worktree path %q not cleared by retry", child.Repos[0].WorktreePath)
	}
	if _, err := os.Stat(fx.childWorktree); !os.IsNotExist(err) {
		t.Fatalf("child worktree still present after retry: %v", err)
	}
	if branches := childIntegrationGit(t, fx.repoDir, "branch", "--list", fx.childBranch); branches != "" {
		t.Fatalf("child branch %s still present after retry", fx.childBranch)
	}
	if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != mergeHEAD {
		t.Fatalf("parent ref = %s after cleanup retry, want unchanged %s", got, mergeHEAD)
	}
	if branches := childIntegrationGit(t, fx.repoDir, "branch", "--list", fx.parentBranch); branches == "" {
		t.Fatal("parent branch was deleted during cleanup")
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s after cleanup retry, want CodeReady", parent.Status)
	}
}

func isIntegrationRefused(err error) bool {
	return err != nil && strings.Contains(err.Error(), ErrChildIntegrationRefused.Error())
}

// TestChildIntegrationAutoPublish proves the close-before-publish ordering:
// with auto-publish configured, closure and the parent CodeReady transition
// complete before publication starts, and successful publication moves the
// parent through the normal publication semantics.
func TestChildIntegrationAutoPublish(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusCodeReady, false)
	o := fx.orchestrator()

	var childClosedAtPublish bool
	o.publishRepoFn = func(featureID, repoName string) (string, error) {
		// Close-before-publish: the child must already be Completed and the
		// parent already CodeReady when the publication path runs.
		c, _ := o.deps.Lifecycle.Get(fx.child.ID)
		p, _ := o.deps.Lifecycle.Get(fx.parent.ID)
		childClosedAtPublish = c.Parent.CloseOutcome == feature.ChildCloseOutcomeCompleted &&
			p.Status == feature.StatusCodeReady
		if err := o.deps.Lifecycle.SetRepoPublished(featureID, repoName, "https://example/pr/1"); err != nil {
			return "", err
		}
		return "https://example/pr/1", nil
	}

	if err := o.runChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v", err)
	}
	if !childClosedAtPublish {
		t.Fatal("publication ran before the child was Completed and the parent CodeReady")
	}
	parent, child := fx.reload()
	if parent.Status != feature.StatusPublished {
		t.Fatalf("parent status = %s, want Published after successful auto-publish", parent.Status)
	}
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
}

// TestChildIntegrationAutoPublishFailureKeepsCodeReady proves a publication
// failure leaves the completed child untouched and the parent CodeReady so
// the ordinary manual publication flow can retry.
func TestChildIntegrationAutoPublishFailureKeepsCodeReady(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusCodeReady, false)
	o := fx.orchestrator()
	o.publishRepoFn = func(featureID, repoName string) (string, error) {
		return "", errors.New("simulated push failure")
	}

	if err := o.runChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil (publish failure stays parent-side)", err)
	}
	parent, child := fx.reload()
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady after failed auto-publish", parent.Status)
	}
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
}

// TestAdvanceAfterFinalReviewRoutesChildToIntegration pins the terminal
// handoff: a child with a successful final review enters integration instead
// of MarkCodeReady or any delivery path.
func TestAdvanceAfterFinalReviewRoutesChildToIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()

	if err := o.advanceAfterFinalReview(fx.child.ID); err != nil {
		t.Fatalf("advanceAfterFinalReview() error = %v", err)
	}
	parent, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed via integration handoff", child.Parent.CloseOutcome)
	}
	if child.Status == feature.StatusCodeReady || child.Status == feature.StatusPublished || child.Status == feature.StatusDone {
		t.Fatalf("child reached delivery status %s; children never deliver", child.Status)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady", parent.Status)
	}
	parents := childIntegrationGit(t, fx.repoDir, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("parent merge commit parents = %q, want two-parent boundary", parents)
	}
}

// TestChildIntegrationMergeAppliesRecordedChildHead proves the merge applies
// the durable ChildHeadSHA anchor, not the mutable child branch: child-branch
// movement after anchor capture changes nothing about what is integrated.
func TestChildIntegrationMergeAppliesRecordedChildHead(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()

	// Capture anchors, then block the merge on a dirty parent so the child
	// head anchor is durable while the child branch remains live.
	if err := os.WriteFile(fx.repoDir+"/stray.txt", []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty parent: %v", err)
	}
	if err := o.runChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil with recorded attention", err)
	}
	_, child := fx.reload()
	recordedHead := child.Parent.Integration.ChildHeadSHA
	if recordedHead == "" {
		t.Fatal("child head anchor not recorded")
	}

	// Move the child branch forward after anchor capture. The integration
	// must NOT pick this commit up.
	testutil.CommitFile(t, fx.childWorktree, "late.txt", "moved after anchors\n", "late child work")
	lateHead := childIntegrationGit(t, fx.childWorktree, "rev-parse", "HEAD")
	if lateHead == recordedHead {
		t.Fatal("child branch did not move")
	}

	if err := os.Remove(fx.repoDir + "/stray.txt"); err != nil {
		t.Fatalf("clean parent: %v", err)
	}
	outcome, err := fx.orchestrator().RestartPhase(fx.child.ID, 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase() error = %v", err)
	}
	if outcome.Action != RestartNoOp {
		t.Fatalf("RestartPhase() action = %v, want RestartNoOp", outcome.Action)
	}

	second := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD^2")
	if second != recordedHead {
		t.Fatalf("merge second parent = %s, want recorded child head %s (branch moved to %s)", second, recordedHead, lateHead)
	}
	if _, err := os.Stat(fx.repoDir + "/late.txt"); !os.IsNotExist(err) {
		t.Fatal("late child-branch commit leaked into the parent merge")
	}

	_, child = fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
}

// TestChildIntegrationParentBranchMismatch proves the merge only ever runs
// on the recorded parent branch: a switched parent worktree is attention,
// not a silent merge into whatever happens to be checked out.
func TestChildIntegrationParentBranchMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)

	preHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
	childIntegrationGit(t, fx.repoDir, "checkout", "-b", "stray-branch")

	if err := fx.orchestrator().runChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil with recorded attention", err)
	}
	_, child := fx.reload()
	integ := child.Parent.Integration
	if integ == nil || integ.Phase != feature.ChildIntegrationPhaseAttention {
		t.Fatalf("integration phase = %+v, want attention on branch mismatch", integ)
	}
	if !strings.Contains(integ.Attention, "recorded parent branch") {
		t.Fatalf("attention = %q, want recorded-branch diagnostic", integ.Attention)
	}
	if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "feature/parent"); got != preHEAD {
		t.Fatalf("parent branch moved to %s while another branch was checked out", got)
	}
	if child.Parent.CloseOutcome != "" {
		t.Fatal("child closed while the recorded parent branch was not checked out")
	}

	// Restore the recorded parent branch and retry: the merge lands on
	// feature/parent, never on the stray checkout.
	childIntegrationGit(t, fx.repoDir, "checkout", "feature/parent")
	outcome, err := fx.orchestrator().RestartPhase(fx.child.ID, 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase() error = %v", err)
	}
	if outcome.Action != RestartNoOp {
		t.Fatalf("RestartPhase() action = %v, want RestartNoOp", outcome.Action)
	}
	_, child = fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed after restoring the recorded branch", child.Parent.CloseOutcome)
	}
	mergeHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "feature/parent")
	if child.Parent.Integration.MergeHEAD != mergeHEAD {
		t.Fatalf("merge head = %s, want recorded %s on feature/parent", mergeHEAD, child.Parent.Integration.MergeHEAD)
	}
	parents := childIntegrationGit(t, fx.repoDir, "rev-list", "--parents", "-n", "1", "feature/parent")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("parent merge commit parents = %q, want two-parent boundary", parents)
	}
	if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "stray-branch"); got != preHEAD {
		t.Fatalf("stray branch moved to %s; integration must only touch the recorded parent branch", got)
	}
}

// failingCodeReadyLifecycle fails MarkCodeReady, simulating a persistence
// failure inside the durable parent transition.
type failingCodeReadyLifecycle struct {
	ports.FeatureLifecycle
	err error
}

func (l failingCodeReadyLifecycle) MarkCodeReady(featureID string) error { return l.err }

// TestChildIntegrationParentTransitionFailureIsRetryable proves a failure
// while moving the parent to CodeReady leaves the child open, so a retry
// repeats the relationship transition instead of being refused on an
// already-closed child.
func TestChildIntegrationParentTransitionFailureIsRetryable(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()
	o.deps.Lifecycle = failingCodeReadyLifecycle{FeatureLifecycle: fx.mgr, err: errors.New("simulated code-ready persistence failure")}

	preHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
	err := o.runChildIntegration(fx.child.ID)
	if err == nil || !strings.Contains(err.Error(), "mark parent code ready") {
		t.Fatalf("runChildIntegration() error = %v, want wrapped parent transition failure", err)
	}

	// The merge is durable, the child is still open at the merged boundary,
	// and the parent never transitioned.
	parent, child := fx.reload()
	if child.Parent.CloseOutcome != "" {
		t.Fatalf("child closed (%q) although the parent transition failed", child.Parent.CloseOutcome)
	}
	if child.Parent.Integration == nil || child.Parent.Integration.Phase != feature.ChildIntegrationPhaseMerged {
		t.Fatalf("integration phase = %+v, want merged", child.Parent.Integration)
	}
	if parent.Status != feature.StatusPublished {
		t.Fatalf("parent status = %s after failed transition, want still Published", parent.Status)
	}
	if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got == preHEAD {
		t.Fatal("merge was not durable although only the parent transition failed")
	}

	// Retry with a healthy lifecycle: the whole transition completes and the
	// merge is not repeated.
	retryHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
	if err := fx.orchestrator().runChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("retry runChildIntegration() error = %v", err)
	}
	parent, child = fx.reload()
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s after retry, want CodeReady", parent.Status)
	}
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q after retry, want completed", child.Parent.CloseOutcome)
	}
	if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != retryHEAD {
		t.Fatalf("retry created another merge: %s != %s", got, retryHEAD)
	}
}

// failNthModifyStore fails the Nth Modify call against the target feature,
// simulating a transient storage failure at a chosen persistence boundary.
type failNthModifyStore struct {
	ports.FeatureStore
	target string
	n      int
	err    error
	count  int
}

func (s *failNthModifyStore) Modify(id string, fn func(*feature.Feature) error) error {
	if id == s.target {
		s.count++
		if s.count == s.n {
			return s.err
		}
	}
	return s.FeatureStore.Modify(id, fn)
}

// TestChildIntegrationCloseWriteFailureIsRetryable proves a storage failure
// while writing the child's Completed outcome returns a wrapped error,
// leaves the child open (with the parent already CodeReady, since the parent
// transition is persisted first), and retries cleanly.
func TestChildIntegrationCloseWriteFailureIsRetryable(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()
	// Child Modify calls: 1 = anchors, 2 = merged phase, 3 = close write.
	store := &failNthModifyStore{FeatureStore: fx.store, target: fx.child.ID, n: 3, err: errors.New("simulated close-write failure")}
	o.deps.Store = store

	err := o.runChildIntegration(fx.child.ID)
	if err == nil || !strings.Contains(err.Error(), "close child relationship") {
		t.Fatalf("runChildIntegration() error = %v, want wrapped close-write failure", err)
	}
	parent, child := fx.reload()
	if child.Parent.CloseOutcome != "" {
		t.Fatalf("child closed (%q) although the close write failed", child.Parent.CloseOutcome)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady persisted before the child close write", parent.Status)
	}

	if err := fx.orchestrator().runChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("retry runChildIntegration() error = %v", err)
	}
	parent, child = fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q after retry, want completed", child.Parent.CloseOutcome)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s after retry, want CodeReady", parent.Status)
	}
}

// TestChildIntegrationCleanupWarningPersistenceFailure proves a storage
// failure while recording the cleanup outcome is returned as a wrapped error
// — the orchestrator never reports a settled closure tail whose durable
// state was not written — and the tail settles on the restart-driven retry.
func TestChildIntegrationCleanupWarningPersistenceFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()
	// Cleanup removal fails, so child Modify calls are: 1 = anchors,
	// 2 = merged phase, 3 = close write, 4 = cleanup warning record.
	o.deps.Worktrees = failingRemoveWorktrees{WorktreeManager: fx.wm, removeErr: errors.New("simulated worktree removal failure")}
	store := &failNthModifyStore{FeatureStore: fx.store, target: fx.child.ID, n: 4, err: errors.New("simulated warning-write failure")}
	o.deps.Store = store

	err := o.runChildIntegration(fx.child.ID)
	if err == nil || !strings.Contains(err.Error(), "record cleanup warning") {
		t.Fatalf("runChildIntegration() error = %v, want wrapped warning-write failure", err)
	}
	parent, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady", parent.Status)
	}
	if child.Parent.Integration == nil || child.Parent.Integration.CleanupWarning != "" {
		t.Fatalf("cleanup warning = %+v, want unset (the write failed and must not be faked)", child.Parent.Integration)
	}
	if !child.IntegrationResumable() {
		t.Fatal("child not resumable although the closure tail is unfinished")
	}

	// Restart re-enters only the closure tail with a healthy store and
	// worktree manager: cleanup settles and the warning state is durable.
	outcome, err := fx.orchestrator().RestartPhase(fx.child.ID, 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase() error = %v", err)
	}
	if outcome.Action != RestartNoOp {
		t.Fatalf("RestartPhase() action = %v, want RestartNoOp (closure tail resume)", outcome.Action)
	}
	_, child = fx.reload()
	if child.Parent.Integration.CleanupWarning != "" {
		t.Fatalf("cleanup warning = %q after settled retry, want cleared", child.Parent.Integration.CleanupWarning)
	}
	if child.Repos[0].WorktreePath != "" {
		t.Fatalf("child worktree path %q not cleared on retry", child.Repos[0].WorktreePath)
	}
	if child.IntegrationResumable() {
		t.Fatal("child still resumable after the closure tail settled")
	}
}
