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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestWorktreeCreateAndRemove(t *testing.T) {
	if testing.Short() {
		t.Skip("covered by TestFastWorktreeRepresentative in short mode")
	}
	t.Parallel()

	repoDir := testutil.InitGitRepo(t)
	wtBaseDir := t.TempDir()

	mgr := NewWorktreeManager(wtBaseDir)

	// Create worktree from HEAD (empty start point)
	wtPath, err := mgr.Create(repoDir, "test-feature", "test-repo", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Verify worktree exists
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree not created: %v", err)
	}

	// Verify branch
	branch := CurrentBranch(wtPath)
	if branch != "feature/test-feature" {
		t.Errorf("expected branch feature/test-feature, got %s", branch)
	}

	// Make a commit in worktree
	testutil.CommitFile(t, wtPath, "test.txt", "test content\n", "Test commit")

	// Remove worktree
	if err := mgr.Remove(wtPath, true); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
}

// TestWorktreeRemoveMaterialFailureReturned pins the cleanup contract the
// child-integration warning path relies on: when neither `git worktree
// remove` nor the manual directory fallback can clear the worktree, Remove
// must surface the failure instead of silently returning nil.
func TestWorktreeRemoveMaterialFailureReturned(t *testing.T) {
	if testing.Short() {
		t.Skip("covered by TestFastWorktreeRepresentative in short mode")
	}
	t.Parallel()

	repoDir := testutil.InitGitRepo(t)
	parentDir := t.TempDir()
	mgr := NewWorktreeManager(parentDir)

	wtPath, err := mgr.Create(repoDir, "stuck-feature", "test-repo", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	testutil.CommitFile(t, wtPath, "stuck.txt", "stuck\n", "work")

	// Make the worktree directory unremovable: both git worktree remove and
	// the os.RemoveAll fallback fail while its parent directory is read-only.
	wtParent := filepath.Dir(wtPath)
	if err := os.Chmod(wtParent, 0o555); err != nil {
		t.Fatalf("chmod worktree parent dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(wtParent, 0o755); err != nil {
			t.Fatalf("restore worktree parent dir permissions: %v", err)
		}
	})

	if err := mgr.RemoveRef(wtPath, repoDir, "feature/stuck-feature"); err == nil {
		t.Fatal("RemoveRef() error = nil, want the material cleanup failure surfaced")
	}

	// Once the blocker clears, the retry succeeds idempotently and the
	// ephemeral branch is actually deleted (not silently leaked).
	if err := os.Chmod(wtParent, 0o755); err != nil {
		t.Fatalf("restore worktree parent dir permissions: %v", err)
	}
	if err := mgr.RemoveRef(wtPath, repoDir, "feature/stuck-feature"); err != nil {
		t.Fatalf("RemoveRef() retry error = %v, want nil after the blocker cleared", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still present after successful retry: %v", err)
	}
	if got := gitOutput(t, repoDir, "branch", "--list", "feature/stuck-feature"); got != "" {
		t.Fatalf("ephemeral branch feature/stuck-feature still present after successful retry")
	}
}

// TestWorktreeRemoveRefDeletesBranchAfterDeregistration pins the partial-
// removal sequence the reviewer flagged: a first attempt can deregister the
// worktree while leaving the directory behind, after which the branch can no
// longer be rediscovered from the path. RemoveRef (and the plain Remove
// discovery wrapper while the worktree is still resolvable) must still reach
// the recorded branch so cleanup never reports success over a leaked branch.
func TestWorktreeRemoveRefDeletesBranchAfterDeregistration(t *testing.T) {
	if testing.Short() {
		t.Skip("covered by TestFastWorktreeRepresentative in short mode")
	}
	t.Parallel()

	repoDir := testutil.InitGitRepo(t)
	mgr := NewWorktreeManager(t.TempDir())

	wtPath, err := mgr.Create(repoDir, "partial-feature", "test-repo", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	testutil.CommitFile(t, wtPath, "work.txt", "work\n", "work")

	// Reproduce the partial-removal state: git deregisters the worktree (so
	// rev-parse from the path no longer resolves the main repo) while the
	// directory with leftover content stays behind.
	runGit(t, repoDir, "worktree", "remove", wtPath, "--force")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("recreate stuck worktree dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "stuck.txt"), []byte("stuck\n"), 0o644); err != nil {
		t.Fatalf("write stuck file: %v", err)
	}
	if got := gitOutput(t, repoDir, "branch", "--list", "feature/partial-feature"); got == "" {
		t.Fatal("test setup expects the ephemeral branch to survive deregistration")
	}

	// Plain Remove can only clear the directory here; the identity-carrying
	// RemoveRef is what production retries use to reach the branch.
	if err := mgr.Remove(wtPath, true); err != nil {
		t.Fatalf("Remove() on deregistered worktree error = %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("stuck worktree directory not cleared: %v", err)
	}
	if err := mgr.RemoveRef(wtPath, repoDir, "feature/partial-feature"); err != nil {
		t.Fatalf("RemoveRef() error = %v", err)
	}
	if got := gitOutput(t, repoDir, "branch", "--list", "feature/partial-feature"); got != "" {
		t.Fatal("ephemeral branch feature/partial-feature still present after RemoveRef")
	}

	// A further retry over fully-absent resources stays idempotent success.
	if err := mgr.RemoveRef(wtPath, repoDir, "feature/partial-feature"); err != nil {
		t.Fatalf("RemoveRef() idempotent retry error = %v", err)
	}
}

// TestWorktreeRemoveIdempotentOnAbsentResources pins the retry tail of the
// cleanup contract: removing an already-removed worktree (and its
// already-deleted branch) is success, so cleanup retries converge instead of
// re-recording warnings for resources that no longer exist.
func TestWorktreeRemoveIdempotentOnAbsentResources(t *testing.T) {
	if testing.Short() {
		t.Skip("covered by TestFastWorktreeRepresentative in short mode")
	}
	t.Parallel()

	repoDir := testutil.InitGitRepo(t)
	mgr := NewWorktreeManager(t.TempDir())

	wtPath, err := mgr.Create(repoDir, "gone-feature", "test-repo", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := mgr.Remove(wtPath, true); err != nil {
		t.Fatalf("first remove: %v", err)
	}
	if err := mgr.Remove(wtPath, true); err != nil {
		t.Fatalf("second remove: %v, want idempotent success on absent resources", err)
	}
}

func TestWorktreeCreateRejectsEmptyRepoPath(t *testing.T) {
	repoDir := testutil.InitGitRepo(t)
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	mgr := NewWorktreeManager(t.TempDir())
	if _, err := mgr.Create("", "empty-repo-path", "test-repo", ""); err == nil {
		t.Fatal("Create() error = nil, want empty repo path rejected")
	}
}

func TestWorktreeThinStateQueries(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "default branch",
			run: func(t *testing.T) {
				t.Parallel()

				repo := testutil.InitGitRepo(t)
				branch := DefaultBranch(repo)
				if branch != "main" {
					t.Fatalf("DefaultBranch() = %q, want main", branch)
				}
				if got := gitOutput(t, repo, "rev-parse", "--verify", branch); got == "" {
					t.Errorf("git rev-parse --verify %s returned empty output", branch)
				}
			},
		},
		{
			name: "current branch",
			run: func(t *testing.T) {
				t.Parallel()

				repo := testutil.InitGitRepo(t)
				if branch := CurrentBranch(repo); branch != "main" {
					t.Fatalf("CurrentBranch() = %q, want main", branch)
				}
				testutil.CreateBranch(t, repo, "feature/xyz")
				if got := CurrentBranch(repo); got != "feature/xyz" {
					t.Errorf("CurrentBranch() = %q, want feature/xyz", got)
				}
			},
		},
		{
			name: "local symref default branch",
			run: func(t *testing.T) {
				t.Parallel()

				repo := initRepoOnBranch(t, "trunk")
				if got := DefaultBranch(repo); got != "trunk" {
					t.Errorf("DefaultBranch() = %q, want trunk", got)
				}
			},
		},
		{
			name: "reset local without origin",
			run: func(t *testing.T) {
				t.Parallel()

				repo := testutil.InitGitRepo(t)
				if HasOriginRemote(repo) {
					t.Fatal("HasOriginRemote() = true, want false before remote setup")
				}
				mgr := NewWorktreeManager(t.TempDir())
				wtPath, err := mgr.Create(repo, "no-origin-feat", "repo", "")
				if err != nil {
					t.Fatalf("Create() error = %v", err)
				}
				testutil.CommitFile(t, wtPath, "file.txt", "data\n", "wt commit")
				if err := mgr.ResetToBaseLocal(wtPath, "main"); err != nil {
					t.Fatalf("ResetToBaseLocal() error = %v", err)
				}
				baseCommit := gitOutput(t, repo, "rev-parse", "main")
				headCommit := gitOutput(t, wtPath, "rev-parse", "HEAD")
				if headCommit != baseCommit {
					t.Errorf("HEAD = %s, want base %s", headCommit, baseCommit)
				}
			},
		},
		{
			name: "origin remote detection",
			run: func(t *testing.T) {
				t.Parallel()

				noRemote := testutil.InitMinimalGitRepo(t)
				if HasOriginRemote(noRemote) {
					t.Error("HasOriginRemote() = true, want false without origin")
				}
				withRemote, _ := testutil.InitPublishReadyGitRepo(t)
				if !HasOriginRemote(withRemote) {
					t.Error("HasOriginRemote() = false, want true with origin")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestDefaultBranchPreferRemoteOverLocal(t *testing.T) {
	t.Parallel()

	localDir := initRepoOnBranch(t, "local")
	testutil.PairWithBareRemote(t, localDir, "local", "main")

	// Set origin/HEAD to point to origin/main
	runGit(t, localDir, "remote", "set-head", "origin", "main")

	got := DefaultBranch(localDir)
	if got != "main" {
		t.Errorf("expected remote default branch %q to take precedence, got %q", "main", got)
	}
}

func TestResetToBaseLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("covered by TestFastWorktreeRepresentative in short mode")
	}
	t.Parallel()

	repoDir := testutil.InitGitRepo(t)
	wtBaseDir := t.TempDir()
	mgr := NewWorktreeManager(wtBaseDir)

	baseBranch := DefaultBranch(repoDir)

	// Record base branch commit
	baseCommit := gitOutput(t, repoDir, "rev-parse", baseBranch)

	// Create worktree
	wtPath, err := mgr.Create(repoDir, "reset-local-feat", "repo", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Make commits on the worktree branch
	testutil.CommitFile(t, wtPath, "extra.txt", "extra\n", "extra commit")

	// Also create an untracked file to verify clean
	if err := os.WriteFile(filepath.Join(wtPath, "untracked.txt"), []byte("junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Act
	if err := mgr.ResetToBaseLocal(wtPath, baseBranch); err != nil {
		t.Fatalf("ResetToBaseLocal: %v", err)
	}

	// Assert: HEAD matches the base branch commit
	headCommit := gitOutput(t, wtPath, "rev-parse", "HEAD")
	if headCommit != baseCommit {
		t.Errorf("expected HEAD %s to match base %s", headCommit, baseCommit)
	}

	// Assert: untracked file was cleaned up
	if _, err := os.Stat(filepath.Join(wtPath, "untracked.txt")); !os.IsNotExist(err) {
		t.Error("expected untracked.txt to be cleaned up")
	}
}

func initRepoOnBranch(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", branch)
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	runGit(t, dir, "config", "tag.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("writing README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "Initial commit")
	return dir
}

func TestResetToCommit_NoOriginNeeded(t *testing.T) {
	t.Parallel()

	repoDir := testutil.InitGitRepo(t)
	anchor := testutil.CommitFile(t, repoDir, "anchor.txt", "anchor\n", "anchor commit")
	later := testutil.CommitFile(t, repoDir, "later.txt", "later\n", "later commit")
	if later == anchor {
		t.Fatal("test setup produced identical commits")
	}

	mgr := NewWorktreeManager(t.TempDir())
	wtPath, err := mgr.Create(repoDir, "reset-to-commit", "test-repo", "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	if err := mgr.ResetToCommit(wtPath, anchor); err != nil {
		t.Fatalf("ResetToCommit: %v", err)
	}
	if got := gitOutput(t, wtPath, "rev-parse", "HEAD"); got != anchor {
		t.Errorf("HEAD = %s, want anchor %s", got, anchor)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "untracked.txt")); !os.IsNotExist(err) {
		t.Errorf("untracked.txt should be cleaned, err=%v", err)
	}
}

func TestResetToCommitRetriesTransientIndexLock(t *testing.T) {
	t.Parallel()

	repoDir := testutil.InitGitRepo(t)
	anchor := testutil.CommitFile(t, repoDir, "anchor.txt", "anchor\n", "anchor commit")
	testutil.CommitFile(t, repoDir, "later.txt", "later\n", "later commit")

	mgr := NewWorktreeManager(t.TempDir())
	wtPath, err := mgr.Create(repoDir, "reset-lock-retry", "test-repo", "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	lockPath := gitOutput(t, wtPath, "rev-parse", "--git-path", "index.lock")
	if !filepath.IsAbs(lockPath) {
		lockPath = filepath.Join(wtPath, lockPath)
	}
	if err := os.WriteFile(lockPath, []byte("transient test lock\n"), 0o644); err != nil {
		t.Fatalf("create transient index lock: %v", err)
	}

	lockReleased := make(chan struct{})
	go func() {
		defer close(lockReleased)
		timer := time.NewTimer(75 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		_ = os.Remove(lockPath)
	}()
	t.Cleanup(func() { <-lockReleased })

	if err := mgr.ResetToCommit(wtPath, anchor); err != nil {
		t.Fatalf("ResetToCommit() error = %v, want transient index lock retried", err)
	}
	if got := gitOutput(t, wtPath, "rev-parse", "HEAD"); got != anchor {
		t.Fatalf("HEAD = %s, want anchor %s", got, anchor)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitCmd(dir, args...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
