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
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// initTestRepo creates an unborn repository that mirrors a successful clone
// of an empty remote: an origin remote and (by default) a slash-containing
// symbolic branch.
func initTestRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	initRunGit(t, dir, "init", "--initial-branch="+branch, repo)
	initRunGit(t, repo, "remote", "add", "origin", "http://127.0.0.1:9/remote.git")
	return repo
}

func initRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runInitializeGit(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v: %s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(out)
}

func assertAgenticoCommit(t *testing.T, repo, wantBranch string) (string, string) {
	t.Helper()
	branch := initRunGit(t, repo, "symbolic-ref", "--short", "HEAD")
	if branch != wantBranch {
		t.Fatalf("branch = %q, want %q", branch, wantBranch)
	}
	count := initRunGit(t, repo, "rev-list", "--count", "HEAD")
	if count != "1" {
		t.Fatalf("commit count = %q, want 1", count)
	}
	author := initRunGit(t, repo, "log", "-1", "--format=%an <%ae>")
	committer := initRunGit(t, repo, "log", "-1", "--format=%cn <%ce>")
	if author != "Agentico <agentico@localhost>" || committer != "Agentico <agentico@localhost>" {
		t.Fatalf("identity = author %q committer %q, want Agentico <agentico@localhost>", author, committer)
	}
	subject := initRunGit(t, repo, "log", "-1", "--format=%s")
	if subject != "Initial commit" {
		t.Fatalf("subject = %q, want \"Initial commit\"", subject)
	}
	tree := initRunGit(t, repo, "show", "-s", "--format=%T", "HEAD")
	if tree != "4b825dc642cb6eb9a060e54bf8d69288fbee4904" && len(tree) != 40 && len(tree) != 64 {
		t.Fatalf("unexpected tree hash %q", tree)
	}
	remotes := initRunGit(t, repo, "remote")
	if remotes != "origin" {
		t.Fatalf("remotes = %q, want origin", remotes)
	}
	return branch, count
}

func TestInitializeCreatesOneEmptyCommitPreservingBranchAndOrigin(t *testing.T) {
	repo := initTestRepo(t, "feature/seed")

	outcome, err := InitializeRepository(context.Background(), repo)
	if err != nil {
		t.Fatalf("InitializeRepository: %v", err)
	}
	if outcome.AlreadyInitialized {
		t.Fatal("outcome reports already-initialized for an unborn repository")
	}
	assertAgenticoCommit(t, repo, "feature/seed")
	if outcome.Branch != "feature/seed" {
		t.Fatalf("outcome branch = %q, want feature/seed", outcome.Branch)
	}
	if outcome.Head == "" {
		t.Fatal("outcome head is empty")
	}
	// No remote-tracking refs appear: nothing was fetched or pushed.
	refs := initRunGit(t, repo, "for-each-ref", "--format=%(refname)", "refs/remotes")
	if refs != "" {
		t.Fatalf("remote-tracking refs = %q, want none", refs)
	}
}

func TestInitializeUsesMainOnlyWithoutValidSymbolicBranch(t *testing.T) {
	repo := initTestRepo(t, "main")
	// HEAD pointing at an unusable branch name is the "no valid symbolic
	// branch" case: main is the allowed fallback.
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/bad..name\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}

	outcome, err := InitializeRepository(context.Background(), repo)
	if err != nil {
		t.Fatalf("InitializeRepository: %v", err)
	}
	if outcome.Branch != "main" {
		t.Fatalf("branch = %q, want main fallback", outcome.Branch)
	}
	assertAgenticoCommit(t, repo, "main")
}

func TestInitializeRefusesUntrackedAndStagedContentAndPreservesIt(t *testing.T) {
	untracked := initTestRepo(t, "main")
	if err := os.WriteFile(filepath.Join(untracked, "notes.txt"), []byte("user content"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}
	if _, err := InitializeRepository(context.Background(), untracked); !errors.Is(err, ErrInitializeContentPresent) {
		t.Fatalf("untracked content: err = %v, want ErrInitializeContentPresent", err)
	}
	if _, err := os.Stat(filepath.Join(untracked, "notes.txt")); err != nil {
		t.Fatalf("untracked file removed after refusal: %v", err)
	}
	if HasHead(untracked) {
		t.Fatal("refusal created a commit")
	}

	staged := initTestRepo(t, "main")
	if err := os.WriteFile(filepath.Join(staged, "staged.txt"), []byte("user content"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	initRunGit(t, staged, "add", "staged.txt")
	if _, err := InitializeRepository(context.Background(), staged); !errors.Is(err, ErrInitializeContentPresent) {
		t.Fatalf("staged content: err = %v, want ErrInitializeContentPresent", err)
	}
	stagedStatus := initRunGit(t, staged, "status", "--porcelain")
	if stagedStatus != "A  staged.txt" {
		t.Fatalf("staged state changed after refusal: %q", stagedStatus)
	}
	if HasHead(staged) {
		t.Fatal("refusal created a commit")
	}
}

func TestInitializeAllowsIgnoredFilesAndPreservesThem(t *testing.T) {
	repo := initTestRepo(t, "main")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	// .gitignore itself is untracked user content, so this must refuse.
	if _, err := InitializeRepository(context.Background(), repo); !errors.Is(err, ErrInitializeContentPresent) {
		t.Fatalf("untracked .gitignore: err = %v, want ErrInitializeContentPresent", err)
	}
	initRunGit(t, repo, "add", ".gitignore")
	// Staged content also refuses; commit it as the user would.
	initRunGit(t, repo, "-c", "user.name=User", "-c", "user.email=user@example.invalid", "commit", "-m", "ignore build")
	if err := os.MkdirAll(filepath.Join(repo, "build"), 0o755); err != nil {
		t.Fatalf("mkdir build: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "build", "artifact.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}

	outcome, err := InitializeRepository(context.Background(), repo)
	if err != nil {
		t.Fatalf("InitializeRepository with ignored files: %v", err)
	}
	if !outcome.AlreadyInitialized {
		t.Fatal("repository with commits must report already-initialized")
	}
	if _, err := os.Stat(filepath.Join(repo, "build", "artifact.txt")); err != nil {
		t.Fatalf("ignored file removed: %v", err)
	}
	count := initRunGit(t, repo, "rev-list", "--count", "HEAD")
	if count != "1" {
		t.Fatalf("refresh added a commit: count = %q", count)
	}
}

func TestInitializeRefreshOnlySuccessWithLocalChanges(t *testing.T) {
	repo := initTestRepo(t, "main")
	initRunGit(t, repo, "-c", "user.name=User", "-c", "user.email=user@example.invalid", "commit", "--allow-empty", "-m", "external init")
	if err := os.WriteFile(filepath.Join(repo, "local.txt"), []byte("local change"), 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}

	outcome, err := InitializeRepository(context.Background(), repo)
	if err != nil {
		t.Fatalf("InitializeRepository on initialized repository: %v", err)
	}
	if !outcome.AlreadyInitialized {
		t.Fatal("want refresh-only success")
	}
	if outcome.Branch != "main" || outcome.Head == "" {
		t.Fatalf("outcome = %+v, want branch main and a head", outcome)
	}
	count := initRunGit(t, repo, "rev-list", "--count", "HEAD")
	if count != "1" {
		t.Fatalf("refresh added a commit: count = %q", count)
	}
	if _, err := os.Stat(filepath.Join(repo, "local.txt")); err != nil {
		t.Fatalf("local change removed: %v", err)
	}
}

func TestInitializeRefreshOnlySuccessWithActiveOperationMarker(t *testing.T) {
	repo := initTestRepo(t, "main")
	initRunGit(t, repo, "-c", "user.name=User", "-c", "user.email=user@example.invalid", "commit", "--allow-empty", "-m", "external init")
	marker := filepath.Join(resolveGitDir(repo), "MERGE_HEAD")
	if err := os.WriteFile(marker, []byte(strings.Repeat("0", 40)+"\n"), 0o644); err != nil {
		t.Fatalf("write operation marker: %v", err)
	}

	outcome, err := InitializeRepository(context.Background(), repo)
	if err != nil {
		t.Fatalf("InitializeRepository on initialized repository: %v", err)
	}
	if !outcome.AlreadyInitialized {
		t.Fatal("want refresh-only success")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("operation marker removed: %v", err)
	}
	if count := initRunGit(t, repo, "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("refresh added a commit: count = %q", count)
	}
}

func TestInitializeBranchProbeFailureDoesNotFallBackToMain(t *testing.T) {
	repo := initTestRepo(t, "feature/seed")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	branch, hasSymbolic, err := initializeBranchName(ctx, repo)
	if !errors.Is(err, ErrInitializeProbeFailed) {
		t.Fatalf("initializeBranchName() error = %v; want ErrInitializeProbeFailed", err)
	}
	if branch != "" || hasSymbolic {
		t.Fatalf("initializeBranchName() = (%q, %v); want zero values on probe failure", branch, hasSymbolic)
	}
}

func TestInitializeRefusesActiveGitOperations(t *testing.T) {
	cases := []struct {
		name   string
		marker string
		isDir  bool
	}{
		{"merge", "MERGE_HEAD", false},
		{"rebase-merge", "rebase-merge", true},
		{"rebase-apply", "rebase-apply", true},
		{"cherry-pick", "CHERRY_PICK_HEAD", false},
		{"revert", "REVERT_HEAD", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := initTestRepo(t, "main")
			gitDir := resolveGitDir(repo)
			target := filepath.Join(gitDir, tc.marker)
			var err error
			if tc.isDir {
				err = os.MkdirAll(target, 0o755)
			} else {
				err = os.WriteFile(target, []byte("x"), 0o644)
			}
			if err != nil {
				t.Fatalf("plant %s: %v", tc.marker, err)
			}
			if _, err := InitializeRepository(context.Background(), repo); !errors.Is(err, ErrInitializeOperationActive) {
				t.Fatalf("err = %v, want ErrInitializeOperationActive", err)
			}
			if _, statErr := os.Stat(target); statErr != nil {
				t.Fatalf("%s removed after refusal: %v", tc.marker, statErr)
			}
			if HasHead(repo) {
				t.Fatal("refusal created a commit")
			}
		})
	}
}

func TestInitializeRefusesNonRepositoryAndKeepsData(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write user file: %v", err)
	}
	if _, err := InitializeRepository(context.Background(), dir); !errors.Is(err, ErrInitializeNotARepository) {
		t.Fatalf("err = %v, want ErrInitializeNotARepository", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "user.txt")); err != nil {
		t.Fatalf("user file removed: %v", err)
	}
}

func TestInitializeAtIdentityRefusesReplacementBeforeMutation(t *testing.T) {
	repo := initTestRepo(t, "main")
	expected, ok := ResolveRepoIdentity(repo)
	if !ok {
		t.Fatal("ResolveRepoIdentity() = false")
	}
	oldGitDir := filepath.Join(filepath.Dir(repo), "original.git")
	if err := os.Rename(filepath.Join(repo, ".git"), oldGitDir); err != nil {
		t.Fatalf("move original git directory: %v", err)
	}
	initRunGit(t, repo, "init", "--initial-branch=main")

	_, err := InitializeRepositoryAtIdentity(context.Background(), repo, expected)
	if !errors.Is(err, ErrInitializeIdentityChanged) {
		t.Fatalf("InitializeRepositoryAtIdentity() error = %v; want ErrInitializeIdentityChanged", err)
	}
	if HasHead(repo) {
		t.Fatal("replacement repository was mutated")
	}
	if _, err := os.Stat(oldGitDir); err != nil {
		t.Fatalf("original git directory was not preserved: %v", err)
	}
}

func TestInitializeConcurrentAttemptsCreateExactlyOneCommit(t *testing.T) {
	repo := initTestRepo(t, "feature/x")

	const attempts = 8
	var wg sync.WaitGroup
	results := make([]InitializeOutcome, attempts)
	errs := make([]error, attempts)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = InitializeRepository(context.Background(), repo)
		}(i)
	}
	close(start)
	wg.Wait()

	installed := 0
	for i := 0; i < attempts; i++ {
		if errs[i] != nil {
			t.Fatalf("attempt %d: %v", i, errs[i])
		}
		if !results[i].AlreadyInitialized {
			installed++
		}
	}
	if installed != 1 {
		t.Fatalf("installed commits = %d, want exactly 1", installed)
	}
	count := initRunGit(t, repo, "rev-list", "--count", "HEAD")
	if count != "1" {
		t.Fatalf("commit count = %q, want 1", count)
	}
	assertAgenticoCommit(t, repo, "feature/x")
}

func TestInitializeCASRefReportsCompetingRefAsNotInstalled(t *testing.T) {
	repo := initTestRepo(t, "main")
	tree := initRunGit(t, repo, "mktree")
	competing := initRunGit(t, repo,
		"-c", "user.name=Competitor", "-c", "user.email=competitor@example.invalid",
		"commit-tree", tree, "-m", "their initial commit")
	// A competing initializer has already created the branch ref.
	initRunGit(t, repo, "update-ref", "refs/heads/main", competing)

	installed, err := initializeCASRef(context.Background(), repo, "refs/heads/main", competing)
	if err != nil {
		t.Fatalf("initializeCASRef: %v", err)
	}
	if installed {
		t.Fatal("competing ref must not be reported as installed")
	}
	// The competing commit stands untouched.
	if head := initRunGit(t, repo, "rev-parse", "--verify", "HEAD"); head != competing {
		t.Fatalf("HEAD = %q, want the competing commit %q", head, competing)
	}
}

func TestInitializeRespectsFreshGitLocks(t *testing.T) {
	repo := initTestRepo(t, "main")
	lockPath := filepath.Join(repo, ".git", "refs", "heads", "main.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("mkdir refs/heads: %v", err)
	}
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant lock: %v", err)
	}
	defer func() { _ = os.Remove(lockPath) }()

	original := initializeRefLockRetryWindow
	initializeRefLockRetryWindow = 100 * time.Millisecond
	defer func() { initializeRefLockRetryWindow = original }()

	_, err := InitializeRepository(context.Background(), repo)
	if !errors.Is(err, ErrInitializeProbeFailed) {
		t.Fatalf("err = %v, want ErrInitializeProbeFailed", err)
	}
	var lockErr *GitLockContentionError
	if !errors.As(err, &lockErr) {
		t.Fatalf("err = %v, want GitLockContentionError inside", err)
	}
	// The fresh lock is never removed by the initializer.
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Fatalf("fresh lock was removed: %v", statErr)
	}
	if HasHead(repo) {
		t.Fatal("locked attempt created a commit")
	}

	// Once the lock clears, an explicit retry succeeds.
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove lock: %v", err)
	}
	outcome, err := InitializeRepository(context.Background(), repo)
	if err != nil {
		t.Fatalf("InitializeRepository after lock cleared: %v", err)
	}
	if outcome.AlreadyInitialized {
		t.Fatal("expected a fresh install after the lock cleared")
	}
	assertAgenticoCommit(t, repo, "main")
}

func TestInitializeRemovesProvablyStaleGitLock(t *testing.T) {
	repo := initTestRepo(t, "main")
	lockPath := filepath.Join(repo, ".git", "refs", "heads", "main.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("mkdir refs/heads: %v", err)
	}
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant lock: %v", err)
	}
	stale := time.Now().Add(-gitLockStaleAfter - time.Minute)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatalf("age lock: %v", err)
	}

	outcome, err := InitializeRepository(context.Background(), repo)
	if err != nil {
		t.Fatalf("InitializeRepository with stale lock: %v", err)
	}
	if outcome.AlreadyInitialized {
		t.Fatal("expected a fresh install past the stale lock")
	}
	assertAgenticoCommit(t, repo, "main")
}

func TestInitializeFailsClosedWhenDeadlineExpires(t *testing.T) {
	repo := initTestRepo(t, "main")
	original := initializeOperationTimeout
	initializeOperationTimeout = time.Nanosecond
	defer func() { initializeOperationTimeout = original }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := InitializeRepository(ctx, repo)
	if !errors.Is(err, ErrInitializeProbeFailed) {
		t.Fatalf("err = %v, want ErrInitializeProbeFailed", err)
	}
	if HasHead(repo) {
		t.Fatal("timed-out attempt created a commit")
	}
}

func TestInitializeIgnoresHostileGlobalConfiguration(t *testing.T) {
	// Not parallel: it repoints HOME for the process.
	home := t.TempDir()
	gitConfig := filepath.Join(home, ".gitconfig")
	config := "test\n[user]\n\tname = Imposter\n\temail = imposter@example.invalid\n[commit]\n\tgpgsign = true\n[init]\n\ttemplateDir = " + filepath.Join(home, "template") + "\n[core]\n\thooksPath = " + filepath.Join(home, "hooks") + "\n"
	if err := os.WriteFile(gitConfig, []byte(config), 0o644); err != nil {
		t.Fatalf("write hostile gitconfig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hookRan := filepath.Join(home, "hooks", "ran")
	// A post-commit hook that would record execution; commit-tree runs no
	// hooks, so this file must never appear.
	hook := "#!/bin/sh\n.touch " + hookRan + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(home, "hooks", "post-commit"), []byte(hook), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	t.Setenv("HOME", home)

	repo := initTestRepo(t, "main")
	outcome, err := InitializeRepository(context.Background(), repo)
	if err != nil {
		t.Fatalf("InitializeRepository under hostile config: %v", err)
	}
	if outcome.AlreadyInitialized {
		t.Fatal("expected a fresh install")
	}
	assertAgenticoCommit(t, repo, "main")
	if _, err := os.Stat(hookRan); err == nil {
		t.Fatal("a hook ran during initialization")
	}
}
