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

package git

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestRemoteUpToDateGuards(t *testing.T) {
	t.Parallel()

	repo, bare := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repo, "feature/test")
	testutil.CommitFile(t, repo, "feature.txt", "feature work\n", "feature commit")
	testutil.SimulatePush(t, repo, bare, "feature/test", "feature/test")

	if IsBehindRemote(repo, "main") {
		t.Error("IsBehindRemote() = true, want false when up to date")
	}

	result := PullRebase(repo, "feature/test")
	if result.Outcome != PullRebaseSuccess {
		t.Errorf("PullRebase() outcome = %d, want %d (err: %v)", result.Outcome, PullRebaseSuccess, result.Err)
	}
	if result.Err != nil {
		t.Errorf("PullRebase() error = %v, want nil", result.Err)
	}
}

func TestIsBehindRemote_Behind(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping behind-remote multi-repo regression in short mode")
	}
	t.Parallel()

	repo, bare := testutil.InitPublishReadyGitRepo(t)

	// Create feature branch
	testutil.CreateBranch(t, repo, "feature/test")

	// Simulate a new commit on remote main by pushing from a clone
	clone := t.TempDir()
	gitClone(t, bare, clone)
	testutil.CommitFile(t, clone, "remote.txt", "remote\n", "remote commit")
	gitPush(t, clone, "main")

	// Fetch in original repo
	if err := Fetch(repo); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if !IsBehindRemote(repo, "main") {
		t.Error("expected behind remote after new remote commit")
	}
}

func TestRebase_LinearHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("covered by TestFastRebaseRepresentatives in short mode")
	}
	t.Parallel()

	repo := testutil.InitGitRepo(t)

	testutil.CreateBranch(t, repo, "feature/test")
	testutil.CommitFile(t, repo, "feature.txt", "feat\n", "feature commit")

	runGit(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "base.txt", "base\n", "base commit")
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	runGit(t, repo, "checkout", "feature/test")

	if err := Rebase(repo, "main"); err != nil {
		t.Fatalf("Rebase: %v", err)
	}

	// After rebase, should no longer be behind
	if IsBehindRemote(repo, "main") {
		t.Error("expected not behind after rebase")
	}
}

func TestRebase_ConflictAborts(t *testing.T) {
	if testing.Short() {
		t.Skip("covered by TestFastRebaseRepresentatives in short mode")
	}
	t.Parallel()

	repo := testutil.InitGitRepo(t)

	testutil.CreateBranch(t, repo, "feature/test")
	testutil.CommitFile(t, repo, "conflict.txt", "feature version\n", "feature change")

	runGit(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "conflict.txt", "main version\n", "main change")
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	runGit(t, repo, "checkout", "feature/test")

	err := Rebase(repo, "main")
	if err == nil {
		t.Fatal("expected rebase to fail with conflicts")
	}
	if RebaseInProgress(repo) {
		t.Fatal("expected rebase to be aborted")
	}

	// Verify rebase was aborted (no .git/rebase-merge dir)
	branch := CurrentBranch(repo)
	if branch != "feature/test" {
		t.Errorf("expected branch feature/test after abort, got %s", branch)
	}
	statusCmd := exec.Command("git", "-C", repo, "status", "--porcelain")
	statusOut, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status failed: %v", err)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		t.Errorf("expected clean worktree after conflict abort, got: %s", string(statusOut))
	}
}

func TestPRBaseBranch_NoGH(t *testing.T) {
	t.Parallel()

	// PRBaseBranch returns "" when gh is unavailable or URL is invalid
	result := PRBaseBranch("/nonexistent", "https://example.com/not-a-pr")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

// helpers for rebase tests

func gitClone(t *testing.T, bare, dest string) {
	t.Helper()
	runGit(t, "", "clone", bare, dest)
	runGit(t, dest, "config", "user.email", "test@test.com")
	runGit(t, dest, "config", "user.name", "Test")
	runGit(t, dest, "config", "commit.gpgsign", "false")
	runGit(t, dest, "config", "tag.gpgsign", "false")
}

func gitPush(t *testing.T, repoPath, branch string) {
	t.Helper()
	// Discover the bare remote path and use SimulatePush to avoid git-push.
	cmd := gitCmd(repoPath, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("gitPush: get remote url: %v", err)
	}
	bareDir := strings.TrimSpace(string(out))
	testutil.SimulatePush(t, repoPath, bareDir, branch, branch)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gitCmd(dir, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_EDITOR=:",
		"GIT_SEQUENCE_EDITOR=:",
		"GIT_TERMINAL_PROMPT=0",
	)
	return cmd
}

// PullRebase tests

func TestPullRebase_BehindRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping pull-rebase behind-remote multi-repo regression in short mode")
	}
	t.Parallel()

	repo, bare := testutil.InitPublishReadyGitRepo(t)

	// Create a feature branch and push it
	testutil.CreateBranch(t, repo, "feature/test")
	testutil.CommitFile(t, repo, "feature.txt", "feature work\n", "feature commit")
	gitPush(t, repo, "feature/test")

	// Simulate remote changes via a second clone
	clone2 := t.TempDir()
	gitClone(t, bare, clone2)
	runGit(t, clone2, "checkout", "feature/test")
	testutil.CommitFile(t, clone2, "remote-change.txt", "remote work\n", "remote commit")
	gitPush(t, clone2, "feature/test")

	// Add a local non-conflicting commit
	testutil.CommitFile(t, repo, "local-change.txt", "local work\n", "local commit")

	// PullRebase should succeed
	result := PullRebase(repo, "feature/test")
	if result.Outcome != PullRebaseSuccess {
		t.Errorf("expected PullRebaseSuccess, got %d (err: %v)", result.Outcome, result.Err)
	}

	// Verify both commits are present
	cmd := exec.Command("git", "-C", repo, "log", "--oneline")
	out, _ := cmd.Output()
	log := string(out)
	if !strings.Contains(log, "remote commit") {
		t.Error("expected remote commit in log after rebase")
	}
	if !strings.Contains(log, "local commit") {
		t.Error("expected local commit in log after rebase")
	}
}

func TestPullRebase_ConflictAbortsCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping second pull-rebase conflict regression in short mode")
	}
	t.Parallel()

	repo, bare := testutil.InitPublishReadyGitRepo(t)

	// Create a feature branch with a shared file and push
	testutil.CreateBranch(t, repo, "feature/test")
	testutil.CommitFile(t, repo, "shared.txt", "original content\n", "add shared file")
	gitPush(t, repo, "feature/test")

	// Simulate conflicting remote changes
	clone2 := t.TempDir()
	gitClone(t, bare, clone2)
	runGit(t, clone2, "checkout", "feature/test")
	testutil.CommitFile(t, clone2, "shared.txt", "remote version\n", "remote conflicting commit")
	gitPush(t, clone2, "feature/test")

	// Make local conflicting change
	testutil.CommitFile(t, repo, "shared.txt", "local version\n", "local conflicting commit")

	// PullRebase should detect conflict
	result := PullRebase(repo, "feature/test")
	if result.Outcome != PullRebaseConflict {
		t.Errorf("expected PullRebaseConflict, got %d (err: %v)", result.Outcome, result.Err)
	}
	if result.Err == nil {
		t.Error("expected non-nil error for conflict")
	}

	// Verify worktree is clean (rebase was aborted)
	statusCmd := exec.Command("git", "-C", repo, "status", "--porcelain")
	statusOut, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status failed: %v", err)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		t.Errorf("expected clean worktree after conflict abort, got: %s", string(statusOut))
	}
}

func TestPullRebase_RemoteBranchAbsent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping absent-remote-branch first-publish regression in short mode")
	}
	t.Parallel()

	repo, _ := testutil.InitPublishReadyGitRepo(t)

	// Create a local branch but don't push it
	testutil.CreateBranch(t, repo, "feature/new-feature")
	testutil.CommitFile(t, repo, "new.txt", "new feature\n", "new feature commit")

	// PullRebase should succeed (no-op, remote branch doesn't exist)
	result := PullRebase(repo, "feature/new-feature")
	if result.Outcome != PullRebaseSuccess {
		t.Errorf("expected PullRebaseSuccess, got %d (err: %v)", result.Outcome, result.Err)
	}
	if result.Err != nil {
		t.Errorf("expected nil error, got %v", result.Err)
	}
}

func TestPullRebase_FetchFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping redundant fetch-failure regression in short mode")
	}
	t.Parallel()

	repo, _ := testutil.InitPublishReadyGitRepo(t)

	// Point remote to an invalid URL to simulate fetch failure
	runGit(t, repo, "remote", "set-url", "origin", "/nonexistent/path")

	result := PullRebase(repo, "feature/test")
	if result.Outcome != PullRebaseFailure {
		t.Errorf("expected PullRebaseFailure, got %d", result.Outcome)
	}
	if result.Outcome == PullRebaseConflict {
		t.Error("fetch failure should NOT be classified as conflict")
	}
	if result.Err == nil {
		t.Error("expected non-nil error for fetch failure")
	}
}

func TestPullRebase_FirstPublishNoRemoteBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-commit first-publish regression in short mode")
	}
	t.Parallel()

	repo, _ := testutil.InitPublishReadyGitRepo(t)

	// Create a local branch with multiple commits, never pushed
	testutil.CreateBranch(t, repo, "feature/first-publish")
	testutil.CommitFile(t, repo, "a.txt", "first file\n", "first commit")
	testutil.CommitFile(t, repo, "b.txt", "second file\n", "second commit")

	// Record commit count before
	cmd := exec.Command("git", "-C", repo, "log", "--oneline")
	out, _ := cmd.Output()
	countBefore := len(strings.Split(strings.TrimSpace(string(out)), "\n"))

	result := PullRebase(repo, "feature/first-publish")
	if result.Outcome != PullRebaseSuccess {
		t.Errorf("expected PullRebaseSuccess, got %d (err: %v)", result.Outcome, result.Err)
	}

	// Verify local commits are untouched
	cmd = exec.Command("git", "-C", repo, "log", "--oneline")
	out, _ = cmd.Output()
	countAfter := len(strings.Split(strings.TrimSpace(string(out)), "\n"))
	if countBefore != countAfter {
		t.Errorf("expected %d commits, got %d", countBefore, countAfter)
	}
}

func TestPullRebase_NonConflictFailureIsNotConflict(t *testing.T) {
	t.Parallel()

	repo, _ := testutil.InitPublishReadyGitRepo(t)

	// Set remote to invalid path
	runGit(t, repo, "remote", "set-url", "origin", "/totally/invalid/repo")

	result := PullRebase(repo, "main")
	if result.Outcome == PullRebaseConflict {
		t.Error("non-conflict failure must NOT be classified as PullRebaseConflict")
	}
	if result.Outcome != PullRebaseFailure {
		t.Errorf("expected PullRebaseFailure, got %d", result.Outcome)
	}
	if result.Err == nil {
		t.Error("expected non-nil error")
	}
}
