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
