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

func TestWorktreeList(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping worktree list extended regression in short mode")
	}
	t.Parallel()

	repoDir := testutil.InitGitRepo(t)
	wtBaseDir := t.TempDir()

	mgr := NewWorktreeManager(wtBaseDir)

	// Empty list
	wts, err := mgr.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(wts) != 0 {
		t.Errorf("expected 0 worktrees, got %d", len(wts))
	}

	// Create one
	_, err = mgr.Create(repoDir, "feat-1", "repo-a", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	wts, err = mgr.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(wts) != 1 {
		t.Errorf("expected 1 worktree, got %d", len(wts))
	}
}

func TestDetectStale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stale worktree detection extended regression in short mode")
	}
	t.Parallel()

	repoDir := testutil.InitGitRepo(t)
	wtBaseDir := t.TempDir()
	mgr := NewWorktreeManager(wtBaseDir)

	// Create two worktrees for different features
	_, err := mgr.Create(repoDir, "active-feat", "repo", "")
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	_, err = mgr.Create(repoDir, "stale-feat", "repo", "")
	if err != nil {
		t.Fatalf("create stale: %v", err)
	}

	// Only "active-feat" is active
	stale, err := mgr.DetectStale([]string{"active-feat"})
	if err != nil {
		t.Fatalf("DetectStale: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale worktree, got %d", len(stale))
	}
	if stale[0].FeatureID != "stale-feat" {
		t.Errorf("expected stale-feat, got %s", stale[0].FeatureID)
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
			name: "uncommitted changes",
			run: func(t *testing.T) {
				t.Parallel()

				repo := testutil.InitGitRepo(t)
				mgr := NewWorktreeManager(t.TempDir())
				has, err := mgr.HasUncommittedChanges(repo)
				if err != nil {
					t.Fatalf("HasUncommittedChanges() error = %v", err)
				}
				if has {
					t.Error("HasUncommittedChanges() = true, want false for clean repo")
				}
				if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
					t.Fatal(err)
				}
				has, err = mgr.HasUncommittedChanges(repo)
				if err != nil {
					t.Fatalf("HasUncommittedChanges() error = %v", err)
				}
				if !has {
					t.Error("HasUncommittedChanges() = false, want true for dirty repo")
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

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitCmd(dir, args...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
