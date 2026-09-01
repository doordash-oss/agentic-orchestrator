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

package feature_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func mergeTestGit(t *testing.T, dir string, args ...string) string {
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

// setupMergeRepo builds a real repo whose feature branch is behind main. When
// conflicting is true main rewrites the same file the feature branch owns, so
// merging main produces a conflict; otherwise main advances on a disjoint file.
// Returns the repo path, the feature branch tip SHA, and the target (main) SHA.
func setupMergeRepo(t *testing.T, conflicting bool) (string, string, string) {
	t.Helper()
	repoDir := testutil.InitGitRepo(t)
	testutil.CreateBranch(t, repoDir, "feature/behind")
	featureSHA := testutil.CommitFile(t, repoDir, "base.txt", "feature v1\n", "feature base commit")
	mergeTestGit(t, repoDir, "checkout", "main")
	var targetSHA string
	if conflicting {
		targetSHA = testutil.CommitFile(t, repoDir, "base.txt", "upstream conflicting v2\n", "upstream conflicting change")
	} else {
		targetSHA = testutil.CommitFile(t, repoDir, "upstream.txt", "upstream change\n", "upstream advancement")
	}
	mergeTestGit(t, repoDir, "checkout", "feature/behind")
	return repoDir, featureSHA, targetSHA
}

// newRebaseSetupChild creates a rebase child of a real-git parent through
// CreateRebaseChild, so RunSetup exercises the full queued setup intent.
func newRebaseSetupChild(t *testing.T, repoDir, featureSHA, targetSHA string) (*feature.Manager, string) {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	mgr := feature.NewManager(store, config.NewDefault())
	mgr.Worktrees = git.NewWorktreeManager(filepath.Join(t.TempDir(), "wt"))
	saveChildTestParent(t, mgr, &feature.Feature{
		ID:     "p-merge",
		Slug:   "p-merge",
		Status: feature.StatusPublished,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: repoDir, WorktreePath: repoDir, Branch: "feature/behind", BaseBranch: "main"},
		},
	})
	child, err := mgr.CreateRebaseChild("p-merge", feature.RebaseChildSpec{
		Bases:   []feature.ChildRepoBase{{Repo: "repo-a", SHA: featureSHA, ParentBranch: "feature/behind"}},
		Targets: []feature.RebaseRepoTarget{{Repo: "repo-a", Target: "main", Ref: "main", TargetSHA: targetSHA}},
		Behind:  []string{"repo-a"},
	})
	if err != nil {
		t.Fatalf("CreateRebaseChild: %v", err)
	}
	return mgr, child.ID
}

func TestRunSetupMergesRebaseTargetCleanly(t *testing.T) {
	t.Parallel()
	repoDir, featureSHA, targetSHA := setupMergeRepo(t, false)
	mgr, childID := newRebaseSetupChild(t, repoDir, featureSHA, targetSHA)

	child, err := mgr.Store.Load(childID)
	if err != nil {
		t.Fatal(err)
	}
	setup := child.Run().Setup
	wtIdx := slices.Index(setup.TaskOrder, "worktree:repo-a")
	mergeIdx := slices.Index(setup.TaskOrder, "merge:repo-a")
	if wtIdx < 0 || mergeIdx < 0 || mergeIdx < wtIdx {
		t.Fatalf("task order = %v, want merge:repo-a after worktree:repo-a", setup.TaskOrder)
	}

	if err := mgr.RunSetup(childID); err != nil {
		t.Fatalf("RunSetup: %v", err)
	}

	done, err := mgr.Store.Load(childID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != feature.StatusCreated || done.Run().Setup.Status != feature.SetupStatusDone {
		t.Fatalf("status=%v setup=%v, want Created/done", done.Status, done.Run().Setup.Status)
	}
	task := done.Run().Setup.Tasks["merge:repo-a"]
	if task.Status != feature.SetupStatusDone {
		t.Fatalf("merge task = %+v, want done", task)
	}
	wt := done.Repos[0].WorktreePath
	if wt == "" || wt == repoDir {
		t.Fatalf("child worktree path = %q, want a fresh child worktree", wt)
	}
	parents := mergeTestGit(t, wt, "rev-list", "--parents", "-n", "1", "HEAD")
	if len(strings.Fields(parents)) != 3 {
		t.Fatalf("child HEAD parents = %q, want a two-parent merge commit", parents)
	}
	if !git.IsAncestor(wt, targetSHA, "HEAD") {
		t.Fatalf("target %s is not an ancestor of the child worktree HEAD", targetSHA)
	}
	// The parent branch itself is untouched.
	if got := mergeTestGit(t, repoDir, "rev-parse", "feature/behind"); got != featureSHA {
		t.Fatalf("parent branch moved: %s, want %s", got, featureSHA)
	}
}

func TestRunSetupLeavesConflictedMergeInProgress(t *testing.T) {
	t.Parallel()
	repoDir, featureSHA, targetSHA := setupMergeRepo(t, true)
	mgr, childID := newRebaseSetupChild(t, repoDir, featureSHA, targetSHA)

	if err := mgr.RunSetup(childID); err != nil {
		t.Fatalf("RunSetup: %v (a conflicted merge is not a setup failure)", err)
	}

	done, err := mgr.Store.Load(childID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != feature.StatusCreated || done.Run().Setup.Status != feature.SetupStatusDone {
		t.Fatalf("status=%v setup=%v, want Created/done", done.Status, done.Run().Setup.Status)
	}
	if task := done.Run().Setup.Tasks["merge:repo-a"]; task.Status != feature.SetupStatusDone {
		t.Fatalf("merge task = %+v, want done despite conflicts", task)
	}
	wt := done.Repos[0].WorktreePath
	if !git.MergeInProgress(wt) {
		t.Fatalf("no in-progress merge (MERGE_HEAD) in child worktree %s", wt)
	}
	content, err := os.ReadFile(filepath.Join(wt, "base.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "<<<<<<<") {
		t.Fatalf("base.txt has no conflict markers:\n%s", content)
	}
}

func TestNonRebaseChildEmitsNoMergeTask(t *testing.T) {
	t.Parallel()
	mgr := newChildTestManager(t, map[string]string{"/wt/repo-a": "aaaa"}, cleanEverywhere())
	saveChildTestParent(t, mgr, &feature.Feature{
		ID:     "p-plain",
		Slug:   "p-plain",
		Status: feature.StatusPublished,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/src/repo-a", WorktreePath: "/wt/repo-a", BaseBranch: "main"},
		},
	})
	child, err := mgr.CreateRefactorChild("p-plain", childTestSpec())
	if err != nil {
		t.Fatalf("CreateRefactorChild: %v", err)
	}
	for key, task := range child.Run().Setup.Tasks {
		if task.Kind == feature.SetupTaskMerge || strings.HasPrefix(key, "merge:") {
			t.Fatalf("non-rebase child has merge task %q", key)
		}
	}
}

func TestRetrySetupDoesNotReMerge(t *testing.T) {
	t.Parallel()
	repoDir, featureSHA, targetSHA := setupMergeRepo(t, false)
	mgr, childID := newRebaseSetupChild(t, repoDir, featureSHA, targetSHA)

	// Queue a failing image task after the merge so the first run fails
	// mid-setup with the merge already performed.
	goodImage := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(goodImage, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Store.Modify(childID, func(f *feature.Feature) error {
		setup := f.Run().Setup
		setup.Tasks["image:1"] = feature.SetupTask{
			Key: "image:1", Kind: feature.SetupTaskImage, Label: "Image 1",
			Status: feature.SetupStatusQueued, SourcePath: filepath.Join(t.TempDir(), "missing.png"), Attempt: 1,
		}
		setup.TaskOrder = append(setup.TaskOrder, "image:1")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RunSetup(childID); err == nil {
		t.Fatal("RunSetup succeeded, want image copy failure")
	}

	failed, err := mgr.Store.Load(childID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Run().Setup.Tasks["merge:repo-a"].Status != feature.SetupStatusDone {
		t.Fatalf("merge task = %+v, want done before the failing task", failed.Run().Setup.Tasks["merge:repo-a"])
	}
	wt := failed.Repos[0].WorktreePath
	headAfterMerge := mergeTestGit(t, wt, "rev-parse", "HEAD")

	// Simulate a crash that lost the merge task's completion, then retry with
	// the image source fixed: the re-executed merge must be a no-op.
	if err := mgr.Store.Modify(childID, func(f *feature.Feature) error {
		setup := f.Run().Setup
		mergeTask := setup.Tasks["merge:repo-a"]
		mergeTask.Status = feature.SetupStatusQueued
		setup.Tasks["merge:repo-a"] = mergeTask
		imageTask := setup.Tasks["image:1"]
		imageTask.SourcePath = goodImage
		setup.Tasks["image:1"] = imageTask
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RetrySetup(childID); err != nil {
		t.Fatalf("RetrySetup: %v", err)
	}

	done, err := mgr.Store.Load(childID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != feature.StatusCreated || done.Run().Setup.Status != feature.SetupStatusDone {
		t.Fatalf("status=%v setup=%v, want Created/done", done.Status, done.Run().Setup.Status)
	}
	if head := mergeTestGit(t, wt, "rev-parse", "HEAD"); head != headAfterMerge {
		t.Fatalf("retry moved the child worktree HEAD: %s, want stable %s", head, headAfterMerge)
	}
}
