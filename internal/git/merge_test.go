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

package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func runGitMergeTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestMergeNoFFProducesTwoParentCommit pins the boundary contract: merging a
// cleanly advanced branch still produces an explicit two-parent merge commit
// even though a fast-forward would be possible.
func TestMergeNoFFProducesTwoParentCommit(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")

	runGitMergeTest(t, repo, "checkout", "-b", "child-branch")
	testutil.CommitFile(t, repo, "child.txt", "child work\n", "child commit")
	runGitMergeTest(t, repo, "checkout", "main")

	if err := gitpkg.MergeNoFF(repo, "child-branch", "merge child"); err != nil {
		t.Fatalf("MergeNoFF() error = %v", err)
	}

	parents := runGitMergeTest(t, repo, "rev-list", "--parents", "-n", "1", "HEAD")
	// "HEAD parent1 parent2" — the merge commit itself plus two parents.
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("merge HEAD parents = %q, want two-parent merge commit", parents)
	}
	if got := runGitMergeTest(t, repo, "log", "-n", "1", "--format=%s"); got != "merge child" {
		t.Fatalf("merge commit subject = %q, want %q", got, "merge child")
	}
	if _, err := os.Stat(filepath.Join(repo, "child.txt")); err != nil {
		t.Fatalf("merged content missing: %v", err)
	}
}

// TestMergeNoFFAcceptsCleanParentAdvancement proves a parent that moved
// forward after the child launched still merges when git can combine both.
func TestMergeNoFFAcceptsCleanParentAdvancement(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")

	runGitMergeTest(t, repo, "checkout", "-b", "child-branch")
	testutil.CommitFile(t, repo, "child.txt", "child work\n", "child commit")
	runGitMergeTest(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "other.txt", "parent moved\n", "parent advanced after launch")

	if err := gitpkg.MergeNoFF(repo, "child-branch", "merge child"); err != nil {
		t.Fatalf("MergeNoFF() error = %v, want clean merge of advanced parent", err)
	}
	parents := runGitMergeTest(t, repo, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("merge HEAD parents = %q, want two-parent merge commit", parents)
	}
}

// TestMergeNoFFConflictAbortsCleanly proves a conflicting merge aborts to the
// exact pre-merge state: the parent ref does not move, no merge is left in
// progress, and the worktree is clean again.
func TestMergeNoFFConflictAbortsCleanly(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "shared.txt", "base\n", "parent base")

	runGitMergeTest(t, repo, "checkout", "-b", "child-branch")
	testutil.CommitFile(t, repo, "shared.txt", "child edit\n", "child commit")
	runGitMergeTest(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "shared.txt", "parent edit\n", "conflicting parent commit")
	preMerge := runGitMergeTest(t, repo, "rev-parse", "HEAD")

	if err := gitpkg.MergeNoFF(repo, "child-branch", "merge child"); err == nil {
		t.Fatal("MergeNoFF() error = nil, want conflict failure")
	}

	if got := runGitMergeTest(t, repo, "rev-parse", "HEAD"); got != preMerge {
		t.Fatalf("parent HEAD = %s after conflict, want unchanged %s", got, preMerge)
	}
	if status := runGitMergeTest(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree status = %q after abort, want clean", status)
	}
	if _, err := os.Stat(filepath.Join(repo, "MERGE_HEAD")); err == nil {
		// MERGE_HEAD lives in the git dir; also check via git directly.
		if out := runGitMergeTest(t, repo, "rev-parse", "--verify", "MERGE_HEAD"); out != "" {
			t.Fatal("MERGE_HEAD still resolvable after abort")
		}
	}
}

// TestMergeIntoCleanMergeCreatesTwoParentCommit pins the happy path: a clean
// MergeInto produces an explicit two-parent merge commit with the given
// message and leaves the worktree clean.
func TestMergeIntoCleanMergeCreatesTwoParentCommit(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")

	runGitMergeTest(t, repo, "checkout", "-b", "child-branch")
	testutil.CommitFile(t, repo, "child.txt", "child work\n", "child commit")
	runGitMergeTest(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "other.txt", "parent moved\n", "parent advanced")

	res := gitpkg.MergeInto(repo, "child-branch", "merge child into parent")
	if res.Outcome != gitpkg.MergeIntoSuccess {
		t.Fatalf("MergeInto() outcome = %v, err = %v, want success", res.Outcome, res.Err)
	}

	parents := runGitMergeTest(t, repo, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("merge HEAD parents = %q, want two-parent merge commit", parents)
	}
	if got := runGitMergeTest(t, repo, "log", "-n", "1", "--format=%s"); got != "merge child into parent" {
		t.Fatalf("merge commit subject = %q, want %q", got, "merge child into parent")
	}
	if status := runGitMergeTest(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree status = %q after clean merge, want clean", status)
	}
}

// TestMergeIntoConflictLeavesMergeInProgress pins the core contract: a
// conflicting merge is NOT aborted — MERGE_HEAD stays, conflict files are
// reported, and the files contain literal conflict markers for resolution.
func TestMergeIntoConflictLeavesMergeInProgress(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "shared.txt", "base\n", "parent base")

	runGitMergeTest(t, repo, "checkout", "-b", "child-branch")
	testutil.CommitFile(t, repo, "shared.txt", "child edit\n", "child commit")
	runGitMergeTest(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "shared.txt", "parent edit\n", "conflicting parent commit")

	res := gitpkg.MergeInto(repo, "child-branch", "merge child")
	if res.Outcome != gitpkg.MergeIntoConflict {
		t.Fatalf("MergeInto() outcome = %v, err = %v, want conflict", res.Outcome, res.Err)
	}
	if !gitpkg.MergeInProgress(repo) {
		t.Fatal("MergeInProgress() = false after conflict, want merge left in progress")
	}
	if len(res.ConflictFiles) != 1 || res.ConflictFiles[0] != "shared.txt" {
		t.Fatalf("ConflictFiles = %v, want [shared.txt]", res.ConflictFiles)
	}
	content, err := os.ReadFile(filepath.Join(repo, "shared.txt"))
	if err != nil {
		t.Fatalf("reading conflicted file: %v", err)
	}
	// Markers built from split strings so this file does not match marker scans.
	start := "<" + "<<<<" + "<<"
	end := ">" + ">>>>" + ">>"
	if !strings.Contains(string(content), start) || !strings.Contains(string(content), end) {
		t.Fatalf("conflicted file content = %q, want literal conflict markers", content)
	}
}

// TestMergeIntoIdempotentWhileConflicted pins re-entry: calling MergeInto
// while a conflicted merge is in progress returns Conflict again without
// aborting or corrupting the sequencer state.
func TestMergeIntoIdempotentWhileConflicted(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "shared.txt", "base\n", "parent base")

	runGitMergeTest(t, repo, "checkout", "-b", "child-branch")
	testutil.CommitFile(t, repo, "shared.txt", "child edit\n", "child commit")
	runGitMergeTest(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "shared.txt", "parent edit\n", "conflicting parent commit")

	first := gitpkg.MergeInto(repo, "child-branch", "merge child")
	if first.Outcome != gitpkg.MergeIntoConflict {
		t.Fatalf("first MergeInto() outcome = %v, want conflict", first.Outcome)
	}

	second := gitpkg.MergeInto(repo, "child-branch", "merge child")
	if second.Outcome != gitpkg.MergeIntoConflict {
		t.Fatalf("second MergeInto() outcome = %v, err = %v, want conflict", second.Outcome, second.Err)
	}
	if !gitpkg.MergeInProgress(repo) {
		t.Fatal("MergeInProgress() = false after re-entry, want merge still in progress")
	}
	if len(second.ConflictFiles) != 1 || second.ConflictFiles[0] != "shared.txt" {
		t.Fatalf("re-entry ConflictFiles = %v, want [shared.txt]", second.ConflictFiles)
	}
}

// TestMergeIntoAncestorIsNoOp pins the no-op path: a ref already reachable
// from HEAD returns Success without moving HEAD.
func TestMergeIntoAncestorIsNoOp(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")

	runGitMergeTest(t, repo, "checkout", "-b", "child-branch")
	runGitMergeTest(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "other.txt", "parent moved\n", "parent advanced")
	preMerge := runGitMergeTest(t, repo, "rev-parse", "HEAD")

	res := gitpkg.MergeInto(repo, "child-branch", "merge child")
	if res.Outcome != gitpkg.MergeIntoSuccess {
		t.Fatalf("MergeInto() outcome = %v, err = %v, want success no-op", res.Outcome, res.Err)
	}
	if got := runGitMergeTest(t, repo, "rev-parse", "HEAD"); got != preMerge {
		t.Fatalf("HEAD = %s after ancestor no-op, want unchanged %s", got, preMerge)
	}
}

// TestMergeIntoWithoutConfiguredIdentity pins the CI contract: the merge
// commit succeeds with the Agentico fallback identity when neither the repo
// nor any git config supplies one (fresh CI runners).
func TestMergeIntoWithoutConfiguredIdentity(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")

	runGitMergeTest(t, repo, "checkout", "-b", "child-branch")
	testutil.CommitFile(t, repo, "child.txt", "child work\n", "child commit")
	runGitMergeTest(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "other.txt", "parent moved\n", "parent advanced")
	runGitMergeTest(t, repo, "config", "--unset", "user.email")
	runGitMergeTest(t, repo, "config", "--unset", "user.name")

	res := gitpkg.MergeInto(repo, "child-branch", "merge child")
	if res.Outcome != gitpkg.MergeIntoSuccess {
		t.Fatalf("MergeInto() outcome = %v, err = %v, want success via identity fallback", res.Outcome, res.Err)
	}
	parents := runGitMergeTest(t, repo, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("merge HEAD parents = %q, want two-parent merge commit", parents)
	}
}

// TestMergeIntoBogusRefFailsClean pins the non-conflict failure path: a ref
// that does not exist returns Failed with a clean worktree and no merge left
// in progress.
func TestMergeIntoBogusRefFailsClean(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")

	res := gitpkg.MergeInto(repo, "no-such-ref", "merge nothing")
	if res.Outcome != gitpkg.MergeIntoFailed {
		t.Fatalf("MergeInto() outcome = %v, err = %v, want failed", res.Outcome, res.Err)
	}
	if res.Err == nil {
		t.Fatal("MergeInto() Err = nil, want failure error")
	}
	if gitpkg.MergeInProgress(repo) {
		t.Fatal("MergeInProgress() = true after non-conflict failure, want clean")
	}
	if status := runGitMergeTest(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree status = %q after failure, want clean", status)
	}
}

// TestMergeFeatureBranchWithoutConfiguredIdentity pins the CI contract: the
// no-ff merge commit succeeds with the Agentico fallback identity when
// neither the repo nor any git config supplies one (fresh CI runners).
func TestMergeFeatureBranchWithoutConfiguredIdentity(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	runGitMergeTest(t, repo, "checkout", "-b", "feature/ident")
	testutil.CommitFile(t, repo, "work.txt", "work\n", "feature work")

	if err := gitpkg.MergeFeatureBranch(repo, "feature/ident", "main"); err != nil {
		t.Fatalf("MergeFeatureBranch() error = %v", err)
	}
	parents := runGitMergeTest(t, repo, "rev-list", "--parents", "-n", "1", "main")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("merge HEAD parents = %q, want two-parent merge commit", parents)
	}
	if got := runGitMergeTest(t, repo, "branch", "--show-current"); got != "feature/ident" {
		t.Fatalf("current branch = %q, want feature branch restored", got)
	}
}
