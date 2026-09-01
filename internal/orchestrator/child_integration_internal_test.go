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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
		Worktrees: fx.wm,
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

// TestCaptureChildDiffSummaryConcatenatesPerRepo proves the preserved diff
// anchors at each repo's launch base SHA, prefixes a per-repo header, and
// skips repos whose diff fails or is empty without failing the capture.
func TestCaptureChildDiffSummaryConcatenatesPerRepo(t *testing.T) {
	t.Parallel()
	repoA := testutil.InitGitRepo(t)
	baseA, err := execCommandGit(repoA, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	testutil.CommitFile(t, repoA, "a", "one\n", "child change")
	o := New(Deps{Store: feature.NewStore(t.TempDir())}, Hooks{})

	child := &feature.Feature{
		ID: "child-diff-capture",
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: repoA, WorktreePath: repoA},
			{Name: "repo-b", Path: "/repos/repo-b", WorktreePath: "/worktree/gone"},
		},
		Parent: &feature.ChildRelationship{
			ParentID: "parent",
			Kind:     feature.ChildKindRefactor,
			Bases: []feature.ChildRepoBase{
				{Repo: "repo-a", SHA: strings.TrimSpace(string(baseA)), ParentBranch: "main"},
				{Repo: "repo-b", SHA: "base-sha-b", ParentBranch: "main"},
			},
		},
	}

	got := o.captureChildDiffSummary(child)
	if !strings.Contains(got, "Repository: repo-a\n") || !strings.Contains(got, "+one") {
		t.Fatalf("captureChildDiffSummary = %q, want repo-a diff", got)
	}
}

// TestPreserveChildDiffSummaryBoundsHugeDiff proves closure persists a
// bounded stat-headed summary — never the raw multi-megabyte diff — so a
// single huge child cannot bloat the feature record.
func TestPreserveChildDiffSummaryBoundsHugeDiff(t *testing.T) {
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	base := childIntegrationGit(t, repo, "rev-parse", "HEAD")
	var body strings.Builder
	for i := 0; body.Len() <= feature.DiffSummaryBudget*4; i++ {
		fmt.Fprintf(&body, "huge line %08d\n", i)
	}
	testutil.CommitFile(t, repo, "huge.txt", body.String(), "huge child change")

	store := feature.NewStore(t.TempDir())
	closedAt := time.Now()
	child := &feature.Feature{
		ID: "child-huge-diff", Name: "huge", Slug: "child-huge-diff",
		Status: feature.StatusReviewPassed, SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun: 1, RunCount: 1,
		Repos: []feature.FeatureRepo{{Name: "repo-a", Path: repo, WorktreePath: repo}},
		Parent: &feature.ChildRelationship{
			ParentID: "parent", Kind: feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeCompleted, ClosedAt: &closedAt,
			Bases: []feature.ChildRepoBase{{Repo: "repo-a", SHA: base, ParentBranch: "main"}},
		},
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}
	o := New(Deps{Store: store, Lifecycle: feature.NewManager(store, config.NewDefault())}, Hooks{})

	o.preserveChildDiffSummary(child.ID)

	got, err := store.Load(child.ID)
	if err != nil {
		t.Fatalf("reload child: %v", err)
	}
	summary := got.Parent.DiffSummary
	if summary == "" || len(summary) > feature.DiffSummaryBudget {
		t.Fatalf("persisted summary length = %d, want non-empty and <= %d", len(summary), feature.DiffSummaryBudget)
	}
	if !strings.Contains(summary, " huge.txt | ") || !strings.Contains(summary, "file changed") {
		t.Fatalf("summary missing stat header, head = %q", summary[:200])
	}
	if !strings.HasSuffix(summary, " bytes omitted]") {
		t.Fatalf("summary missing truncation marker, tail = %q", summary[len(summary)-120:])
	}
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

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
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
	tx := child.Parent.Transaction
	if tx == nil {
		t.Fatal("child transaction record missing")
	}
	if len(tx.Entries) != 1 || tx.Entries[0].ParentBranch != fx.parentBranch || tx.Entries[0].ParentAnchorSHA == "" || tx.Entries[0].ChildHeadSHA == "" {
		t.Fatalf("transaction anchors incomplete: %+v", tx)
	}
	mergeHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
	if tx.Entries[0].MergeHEAD != mergeHEAD {
		t.Fatalf("transaction merge head = %s, want parent tip %s", tx.Entries[0].MergeHEAD, mergeHEAD)
	}
	if tx.Entries[0].CleanupWarning != "" {
		t.Fatalf("unexpected cleanup warning: %q", tx.Entries[0].CleanupWarning)
	}

	// The child's diff was captured before cleanup removed the disposable
	// worktree, so the preserved read-only summary survives closure.
	if !strings.Contains(child.Parent.DiffSummary, "Repository: repoA") ||
		!strings.Contains(child.Parent.DiffSummary, "child.txt") {
		t.Fatalf("preserved diff summary = %q, want repo header and child change", child.Parent.DiffSummary)
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

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil with recorded attention", err)
	}
	if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != preHEAD {
		t.Fatalf("parent ref = %s, want unchanged %s on dirty preflight", got, preHEAD)
	}

	_, child := fx.reload()
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention", tx)
	}
	if len(tx.Entries) != 1 || tx.Entries[0].ChildHeadSHA == "" || tx.Entries[0].ParentAnchorSHA == "" {
		t.Fatalf("anchors must be durable before parent mutation: %+v", tx)
	}
	if tx.Entries[0].MergeHEAD != "" {
		t.Fatalf("merge head recorded (%s) although the merge was blocked", tx.Entries[0].MergeHEAD)
	}
	if len(tx.Entries[0].Dirty) != 1 || tx.Entries[0].Dirty[0].UntrackedTotal != 1 {
		t.Fatalf("dirty diagnostics = %+v, want categorized untracked entry", tx.Entries[0].Dirty)
	}
	if !strings.Contains(tx.Attention, "uncommitted changes") {
		t.Fatalf("attention = %q, want dirty summary", tx.Attention)
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
	// Pin the persisted base to the new tip so the parent-drift gate does not
	// fire and the merge-conflict path is exercised.
	pinned, _ := fx.store.Load(fx.child.ID)
	pinned.Parent.Bases[0].SHA = preHEAD
	if err := fx.store.Save(pinned); err != nil {
		t.Fatalf("save child with pinned base: %v", err)
	}

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil with recorded attention", err)
	}
	if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != preHEAD {
		t.Fatalf("parent ref = %s, want unchanged %s after conflict abort", got, preHEAD)
	}
	if status := childIntegrationGit(t, fx.repoDir, "status", "--porcelain"); status != "" {
		t.Fatalf("parent worktree not clean after abort: %q", status)
	}

	_, child := fx.reload()
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention", tx)
	}
	if !strings.Contains(tx.Attention, "merge") {
		t.Fatalf("attention = %q, want merge failure detail", tx.Attention)
	}
	if child.Parent.CloseOutcome != "" {
		t.Fatalf("child closed on conflicted integration")
	}
	// Child branch and worktree survive.
	if branches := childIntegrationGit(t, fx.repoDir, "branch", "--list", fx.childBranch); branches == "" {
		t.Fatal("child branch was deleted on conflicted integration")
	}

	// Resolve the divergence on the parent side and retry via Restart,
	// re-pinning the base to the restored tip.
	childIntegrationGit(t, fx.repoDir, "reset", "--hard", preHEAD+"^")
	pinned, _ = fx.store.Load(fx.child.ID)
	pinned.Parent.Bases[0].SHA = childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
	if err := fx.store.Save(pinned); err != nil {
		t.Fatalf("re-pin child base: %v", err)
	}
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
	if child.Parent.Transaction.Entries[0].MergeHEAD == "" {
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
		err := fx.orchestrator().RunChildIntegration(fx.child.ID)
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
		err := fx.orchestrator().RunChildIntegration(fx.child.ID)
		if !isIntegrationRefused(err) {
			t.Fatalf("runChildIntegration() error = %v, want refusal", err)
		}
		if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != preHEAD {
			t.Fatalf("parent ref moved without an approved final review: %s", got)
		}
	})

	t.Run("multi-repository child not refused on repo count", func(t *testing.T) {
		fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
		preHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
		if err := fx.store.Modify(fx.child.ID, func(f *feature.Feature) error {
			f.Repos = append(f.Repos, feature.FeatureRepo{Name: "repoB", Path: t.TempDir(), Branch: "b"})
			return nil
		}); err != nil {
			t.Fatalf("add repo: %v", err)
		}
		err := fx.orchestrator().RunChildIntegration(fx.child.ID)
		// Multi-repo children are now supported; the error should not be
		// a refusal on repo count. A preparation failure from the missing
		// second repo's parent mapping is expected (the parent has only
		// repoA), which is a durable refusal about the parent not having
		// the repository, not a repo-count restriction.
		if err == nil {
			t.Fatalf("runChildIntegration() error = nil, want error for missing parent repo mapping")
		}
		if got := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD"); got != preHEAD {
			t.Fatalf("parent ref moved for a child with an unmapped repo: %s", got)
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
		err := fx.orchestrator().RunChildIntegration(fx.child.ID)
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
	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v", err)
	}
	mergeHEAD := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD")
	_, child := fx.reload()
	closedAt := child.Parent.ClosedAt

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
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

// TestChildIntegrationFinalEventIsParentScoped proves the closure tail emits
// a parent-scoped event after all of its mutations, so a client that reloads
// the parent on the stream's last event observes settled state — including
// read-time gates like the worktree-cleanliness check on the refactor action.
func TestChildIntegrationFinalEventIsParentScoped(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v", err)
	}

	var events []ports.Event
	for {
		select {
		case ev := <-o.Events():
			events = append(events, ev)
			continue
		default:
		}
		break
	}
	closedIdx, lastParentIdx := -1, -1
	for i, ev := range events {
		if ev.Type == ports.RelationshipClosed {
			closedIdx = i
		}
		if ev.FeatureID == fx.parent.ID {
			lastParentIdx = i
		}
	}
	if closedIdx == -1 {
		t.Fatalf("RelationshipClosed not emitted; events = %+v", events)
	}
	if lastParentIdx <= closedIdx {
		t.Fatalf("no parent-scoped event after RelationshipClosed (closed at %d, last parent event at %d); events = %+v", closedIdx, lastParentIdx, events)
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

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
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
	if child.Parent.Transaction == nil || child.Parent.Transaction.Entries[0].CleanupWarning == "" {
		t.Fatalf("cleanup warning not recorded: %+v", child.Parent.Transaction)
	}
	// The merge boundary is durable and complete.
	parents := childIntegrationGit(t, fx.repoDir, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("parent merge commit parents = %q, want two parents", parents)
	}

	// Automatic reconciliation owns the post-close cleanup tail. Closed
	// children never regain a user-visible Restart path.
	if err := fx.orchestrator().ReconcileIntegrationTransactions(); err != nil {
		t.Fatalf("ReconcileIntegrationTransactions() error = %v", err)
	}

	parent, child = fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q after cleanup retry, want still completed", child.Parent.CloseOutcome)
	}
	if child.Parent.Transaction.Entries[0].CleanupWarning != "" {
		t.Fatalf("cleanup warning not cleared by retry: %q", child.Parent.Transaction.Entries[0].CleanupWarning)
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

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
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

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
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

// TestReviewFeedbackIntegrationTailReturnsParentPublished proves the
// review-feedback closure tail never enters the ordinary publish path, even
// when the parent's checkpoints would otherwise enable auto-publish.
func TestReviewFeedbackIntegrationTailReturnsParentPublished(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, false)
	if err := fx.store.Modify(fx.parent.ID, func(f *feature.Feature) error {
		f.RepoStates["repoA"].PRURL = "https://example.test/org/repo/pull/1"
		return nil
	}); err != nil {
		t.Fatalf("seed parent PR URL: %v", err)
	}
	if err := fx.store.Modify(fx.child.ID, func(f *feature.Feature) error {
		f.Parent.Kind = feature.ChildKindReviewFeedback
		return nil
	}); err != nil {
		t.Fatalf("set child kind: %v", err)
	}

	publishStarts := 0
	o := New(Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Worktrees: fx.wm,
	}, Hooks{OnPublishStarted: func(string) { publishStarts++ }})
	publishCalls := 0
	o.publishRepoFn = func(featureID, repoName string) (string, error) {
		publishCalls++
		return "", errors.New("review-feedback tail must not publish")
	}

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v", err)
	}
	parent, child := fx.reload()
	if publishCalls != 0 {
		t.Fatalf("publish calls = %d, want 0", publishCalls)
	}
	if publishStarts != 0 {
		t.Fatalf("publish starts = %d, want 0", publishStarts)
	}
	if parent.Status != feature.StatusPublished {
		t.Fatalf("parent status = %s, want Published", parent.Status)
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
// the recorded child head SHA, not the mutable child branch: the candidate
// creation uses the durable ChildHeadSHA captured during preparation.
func TestChildIntegrationMergeAppliesRecordedChildHead(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v", err)
	}
	_, child := fx.reload()
	recordedHead := child.Parent.Transaction.Entries[0].ChildHeadSHA
	if recordedHead == "" {
		t.Fatal("child head anchor not recorded")
	}

	// The merge second parent must be the recorded child head, proving the
	// candidate used the durable SHA, not the mutable branch name.
	second := childIntegrationGit(t, fx.repoDir, "rev-parse", "HEAD^2")
	if second != recordedHead {
		t.Fatalf("merge second parent = %s, want recorded child head %s", second, recordedHead)
	}

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

	if err := fx.orchestrator().RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("runChildIntegration() error = %v, want nil with recorded attention", err)
	}
	_, child := fx.reload()
	tx := child.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction phase = %+v, want attention on branch mismatch", tx)
	}
	if !strings.Contains(tx.Attention, "recorded parent branch") {
		t.Fatalf("attention = %q, want recorded-branch diagnostic", tx.Attention)
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
	if child.Parent.Transaction.Entries[0].MergeHEAD != mergeHEAD {
		t.Fatalf("merge head = %s, want recorded %s on feature/parent", mergeHEAD, child.Parent.Transaction.Entries[0].MergeHEAD)
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
	err := o.RunChildIntegration(fx.child.ID)
	if err == nil || !strings.Contains(err.Error(), "mark parent code ready") {
		t.Fatalf("runChildIntegration() error = %v, want wrapped parent transition failure", err)
	}

	// The merge is durable, the child is still open at the merged boundary,
	// and the parent never transitioned.
	parent, child := fx.reload()
	if child.Parent.CloseOutcome != "" {
		t.Fatalf("child closed (%q) although the parent transition failed", child.Parent.CloseOutcome)
	}
	if child.Parent.Transaction == nil || child.Parent.Transaction.Phase != feature.TransactionPhaseApplied {
		t.Fatalf("transaction phase = %+v, want applied (parent transition failed before merged)", child.Parent.Transaction)
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
	if err := fx.orchestrator().RunChildIntegration(fx.child.ID); err != nil {
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
	// Transaction path Modify calls on child: 1=prep progress, 2=prepared,
	// 3=applying, 4=apply progress, 5=applied, 6=close write.
	store := &failNthModifyStore{FeatureStore: fx.store, target: fx.child.ID, n: 6, err: errors.New("simulated close-write failure")}
	o.deps.Store = store

	err := o.RunChildIntegration(fx.child.ID)
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

	if err := fx.orchestrator().RunChildIntegration(fx.child.ID); err != nil {
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
	// Transaction path Modify calls on child: 1=prep progress, 2=prepared,
	// 3=applying, 4=apply progress, 5=applied, 6=close write,
	// 7=clear closure error, 8=merged, 9=cleanup warning record.
	o.deps.Worktrees = failingRemoveWorktrees{WorktreeManager: fx.wm, removeErr: errors.New("simulated worktree removal failure")}
	store := &failNthModifyStore{FeatureStore: fx.store, target: fx.child.ID, n: 9, err: errors.New("simulated warning-write failure")}
	o.deps.Store = store

	err := o.RunChildIntegration(fx.child.ID)
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
	if child.Parent.Transaction == nil || child.Parent.Transaction.Entries[0].CleanupWarning != "" {
		t.Fatalf("cleanup warning = %+v, want unset (the write failed and must not be faked)", child.Parent.Transaction)
	}
	if child.IntegrationResumable() {
		t.Fatal("closed child must never become user-restartable")
	}

	// Startup reconciliation re-enters only the closure tail with a healthy
	// store and worktree manager.
	if err := fx.orchestrator().ReconcileIntegrationTransactions(); err != nil {
		t.Fatalf("ReconcileIntegrationTransactions() error = %v", err)
	}
	_, child = fx.reload()
	if child.Parent.Transaction.Entries[0].CleanupWarning != "" {
		t.Fatalf("cleanup warning = %q after settled retry, want cleared", child.Parent.Transaction.Entries[0].CleanupWarning)
	}
	if child.Repos[0].WorktreePath != "" {
		t.Fatalf("child worktree path %q not cleared on retry", child.Repos[0].WorktreePath)
	}
	if child.IntegrationResumable() {
		t.Fatal("child still resumable after the closure tail settled")
	}
}

// TestRunChildIntegrationRefusesWithDiscardIntent proves that when a child
// has a durable discard intent, RunChildIntegration refuses to proceed and
// never moves the parent to CodeReady or records a merged transaction.
func TestRunChildIntegrationRefusesWithDiscardIntent(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)

	// Pre-seed a discard intent on the child so RunChildIntegration sees it.
	if err := fx.store.Modify(fx.child.ID, func(f *feature.Feature) error {
		f.DiscardIntent = &feature.DiscardIntent{
			RequestedAt: time.Now(),
			Step:        feature.DiscardStepIntentRecorded,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed discard intent: %v", err)
	}

	o := fx.orchestrator()
	err := o.RunChildIntegration(fx.child.ID)
	if err == nil {
		t.Fatal("RunChildIntegration succeeded with discard intent; should refuse")
	}
	if !errors.Is(err, ErrChildDiscardInProgress) {
		t.Fatalf("RunChildIntegration error = %v, want ErrChildDiscardInProgress", err)
	}

	parent, child := fx.reload()
	if parent.Status == feature.StatusCodeReady {
		t.Fatal("parent moved to CodeReady despite discard intent")
	}
	if child.Parent.Transaction != nil && child.Parent.Transaction.Phase == feature.TransactionPhaseMerged {
		t.Fatal("transaction merged despite discard intent")
	}
	if child.Parent.CloseOutcome == feature.ChildCloseOutcomeCompleted {
		t.Fatal("child closed as Completed despite discard intent")
	}
}

// TestConcurrentDiscardAndIntegrationSerialization verifies that the
// relationship lock serializes discard and integration so that a discard
// request racing with an in-flight integration can never produce a parent
// CodeReady, a merged transaction, or a publication from the discard path.
// One of the two operations wins the lock; the loser observes the durable
// outcome and refuses. If discard wins, the parent must NOT be CodeReady.
// If integration wins, the child is Completed and discard fails.
func TestConcurrentDiscardAndIntegrationSerialization(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()

	var wg sync.WaitGroup
	wg.Add(2)

	var integErr, discardErr error
	start := make(chan struct{})

	go func() {
		defer wg.Done()
		<-start
		integErr = o.RunChildIntegration(fx.child.ID)
	}()

	go func() {
		defer wg.Done()
		<-start
		discardErr = o.DiscardChild(fx.child.ID)
	}()

	close(start)
	wg.Wait()

	parent, child := fx.reload()

	// Exactly one of the two should succeed.
	integOK := integErr == nil
	discardOK := discardErr == nil
	if integOK && discardOK {
		t.Fatal("both integration and discard succeeded; lock serialization failed")
	}
	if !integOK && !discardOK {
		t.Fatalf("both failed: integration=%v, discard=%v", integErr, discardErr)
	}

	if discardOK {
		// Discard won: parent must NOT be CodeReady, no merged transaction,
		// no publish, child must be Discarded.
		if parent.Status == feature.StatusCodeReady {
			t.Fatal("parent moved to CodeReady from discard path")
		}
		if child.Parent.Transaction != nil && child.Parent.Transaction.Phase == feature.TransactionPhaseMerged {
			t.Fatal("transaction merged from discard path")
		}
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
			t.Fatalf("child close outcome = %q, want discarded", child.Parent.CloseOutcome)
		}
		if integErr == nil {
			t.Fatal("integration should have failed when discard won")
		}
		if !errors.Is(integErr, ErrChildDiscardInProgress) {
			t.Fatalf("integration error = %v, want ErrChildDiscardInProgress", integErr)
		}
	}

	if integOK {
		// Integration won: child must be Completed, parent CodeReady,
		// discard must have failed.
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
		}
		if parent.Status != feature.StatusCodeReady {
			t.Fatalf("parent status = %s, want CodeReady", parent.Status)
		}
		if discardErr == nil {
			t.Fatal("discard should have failed when integration won")
		}
	}
}

// TestDiscardChildRefusesAfterIntegrationCompleted verifies that once
// integration has closed the child as Completed, a subsequent discard
// request is rejected rather than overwriting the close outcome.
func TestDiscardChildRefusesAfterIntegrationCompleted(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	o := fx.orchestrator()

	if err := o.RunChildIntegration(fx.child.ID); err != nil {
		t.Fatalf("RunChildIntegration: %v", err)
	}

	parent, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady", parent.Status)
	}

	err := o.DiscardChild(fx.child.ID)
	if err == nil {
		t.Fatal("DiscardChild succeeded after completion; should refuse")
	}

	// State must be unchanged after the refused discard.
	parent2, child2 := fx.reload()
	if child2.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome changed to %q after refused discard", child2.Parent.CloseOutcome)
	}
	if parent2.Status != feature.StatusCodeReady {
		t.Fatalf("parent status changed to %s after refused discard", parent2.Status)
	}
}

// TestConcurrentDiscardAndIntegrationAutoPublish verifies that auto-publish
// (parent with ManualPublish=false) does not deadlock when a concurrent
// discard request arrives while integration holds the relationship read lock.
// RunChildIntegration holds the read lock for the entire integration; when
// the child is Completed and auto-publish is configured, settleChildClosureTail
// publishes the parent. Previously, Publish took the relationship read lock
// again, which deadlocked when a concurrent DiscardChild writer was waiting.
// This test uses an auto-publish parent and runs integration + discard
// concurrently to exercise the nested-lock path.
func TestConcurrentDiscardAndIntegrationAutoPublish(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	// manualPublish=false → parent.Checkpoints.AutoPublish() == true.
	fx := newChildIntegrationFixture(t, feature.StatusPublished, false)
	o := fx.orchestrator()

	var wg sync.WaitGroup
	wg.Add(2)

	var integErr, discardErr error
	start := make(chan struct{})

	go func() {
		defer wg.Done()
		<-start
		integErr = o.RunChildIntegration(fx.child.ID)
	}()

	go func() {
		defer wg.Done()
		<-start
		discardErr = o.DiscardChild(fx.child.ID)
	}()

	close(start)
	wg.Wait()

	parent, child := fx.reload()

	// Exactly one of the two should succeed.
	integOK := integErr == nil
	discardOK := discardErr == nil
	if integOK && discardOK {
		t.Fatal("both integration and discard succeeded; lock serialization failed")
	}
	if !integOK && !discardOK {
		t.Fatalf("both failed: integration=%v, discard=%v", integErr, discardErr)
	}

	if discardOK {
		// Discard won: parent must NOT be CodeReady, no merged transaction,
		// no publish, child must be Discarded.
		if parent.Status == feature.StatusCodeReady {
			t.Fatal("parent moved to CodeReady from discard path")
		}
		if child.Parent.Transaction != nil && child.Parent.Transaction.Phase == feature.TransactionPhaseMerged {
			t.Fatal("transaction merged from discard path")
		}
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
			t.Fatalf("child close outcome = %q, want discarded", child.Parent.CloseOutcome)
		}
		if integErr == nil {
			t.Fatal("integration should have failed when discard won")
		}
		if !errors.Is(integErr, ErrChildDiscardInProgress) {
			t.Fatalf("integration error = %v, want ErrChildDiscardInProgress", integErr)
		}
	}

	if integOK {
		// Integration won: child must be Completed, parent CodeReady,
		// discard must have failed. The auto-publish path ran without
		// deadlocking under the relationship read lock.
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
		}
		if parent.Status != feature.StatusCodeReady {
			t.Fatalf("parent status = %s, want CodeReady", parent.Status)
		}
		if discardErr == nil {
			t.Fatal("discard should have failed when integration won")
		}
	}
}

// TestRestartPhase_ReviewPassedFinalReviewNilTransaction_DispatchesIntegration
// pins the crash-recovery route: integration was dispatched but died before
// creating a durable journal, stranding the child at ReviewPassed@FinalReview
// with a nil transaction. Restart must replay the integration boundary — not
// return NoOp and leave the child unrecoverable.
func TestRestartPhase_ReviewPassedFinalReviewNilTransaction_DispatchesIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newChildIntegrationFixture(t, feature.StatusPublished, true)
	if fx.child.Parent.Transaction != nil {
		t.Fatal("fixture precondition: child transaction must be nil")
	}

	outcome, err := fx.orchestrator().RestartPhase(fx.child.ID, 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase() error = %v", err)
	}
	if outcome.Action != RestartNoOp {
		t.Fatalf("RestartPhase() action = %v, want RestartNoOp after completed integration", outcome.Action)
	}

	parent, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed integration", child.Parent.CloseOutcome)
	}
	parents := childIntegrationGit(t, fx.repoDir, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("parent merge commit parents = %q, want two-parent merge", parents)
	}
	if parent.Status != feature.StatusCodeReady {
		t.Fatalf("parent status = %s, want CodeReady", parent.Status)
	}
}
