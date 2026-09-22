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
	"os/exec"
	"path/filepath"
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

// setupRebaseRepo builds a real repo whose feature branch is behind main.
// Returns the repo path, the feature branch tip SHA, and the target (main) SHA.
func setupRebaseRepo(t *testing.T) (string, string, string) {
	t.Helper()
	repoDir := testutil.InitGitRepo(t)
	testutil.CreateBranch(t, repoDir, "feature/behind")
	featureSHA := testutil.CommitFile(t, repoDir, "base.txt", "feature v1\n", "feature base commit")
	mergeTestGit(t, repoDir, "checkout", "main")
	targetSHA := testutil.CommitFile(t, repoDir, "upstream.txt", "upstream change\n", "upstream advancement")
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
		Bases:     []feature.ChildRepoBase{{Repo: "repo-a", SHA: featureSHA, ParentBranch: "feature/behind"}},
		Targets:   []feature.RebaseRepoTarget{{Repo: "repo-a", Target: "main", Ref: "main", TargetSHA: targetSHA}},
		WorkRepos: []string{"repo-a"},
		LayerStates: []feature.RebaseLayerClassification{
			{Repo: "repo-a", LayerPosition: 1, LayerTitle: "Parent delivery", Branch: "feature/behind", State: feature.RebaseLayerStateKept},
		},
	})
	if err != nil {
		t.Fatalf("CreateRebaseChild: %v", err)
	}
	return mgr, child.ID
}

// TestRebaseChildSetupHasNoMergeTask pins the post-merge-setup world: a
// rebase child's queued setup intent carries only the worktree task (with
// exact-tip pinning), setup completes, and the child worktree sits at the
// captured fork point — the pass performs the target merge itself.
func TestRebaseChildSetupHasNoMergeTask(t *testing.T) {
	t.Parallel()
	repoDir, featureSHA, targetSHA := setupRebaseRepo(t)
	mgr, childID := newRebaseSetupChild(t, repoDir, featureSHA, targetSHA)

	child, err := mgr.Store.Load(childID)
	if err != nil {
		t.Fatal(err)
	}
	setup := child.Run().Setup
	for key, task := range setup.Tasks {
		if strings.HasPrefix(key, "merge:") {
			t.Fatalf("rebase child has merge task %q (%+v); the pass merges its target itself", key, task)
		}
	}
	if _, ok := setup.Tasks["worktree:repo-a"]; !ok {
		t.Fatalf("task order = %v, want the pinned worktree task", setup.TaskOrder)
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
	wt := done.Repos[0].WorktreePath
	if wt == "" || wt == repoDir {
		t.Fatalf("child worktree path = %q, want a fresh child worktree", wt)
	}
	// The worktree sits at the captured parent tip; the target is NOT merged
	// by setup — the pass does that.
	if got := mergeTestGit(t, wt, "rev-parse", "HEAD"); got != featureSHA {
		t.Fatalf("child worktree HEAD = %s, want the pinned fork point %s", got, featureSHA)
	}
	if git.IsAncestor(wt, targetSHA, "HEAD") {
		t.Fatalf("target %s already merged at setup; the pass must merge it itself", targetSHA)
	}
	// The parent branch itself is untouched.
	if got := mergeTestGit(t, repoDir, "rev-parse", "feature/behind"); got != featureSHA {
		t.Fatalf("parent branch moved: %s, want %s", got, featureSHA)
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
	for key := range child.Run().Setup.Tasks {
		if strings.HasPrefix(key, "merge:") {
			t.Fatalf("non-rebase child has merge task %q", key)
		}
	}
}
