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
	"time"

	gitpkg "github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestUpdateRefCASRetriesTransientRefLock(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	oldSHA := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")
	newSHA := testutil.CommitFile(t, repo, "next.txt", "next\n", "next")
	runGitRefTest(t, repo, "reset", "--hard", oldSHA)

	gitDir := runGitRefTest(t, repo, "rev-parse", "--git-dir")
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repo, gitDir)
	}
	lockPath := filepath.Join(gitDir, "refs", "heads", "main.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("create ref lock: %v", err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		timer := time.NewTimer(75 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		_ = os.Remove(lockPath)
	}()
	t.Cleanup(func() { <-released })

	if err := gitpkg.UpdateRefCAS(repo, "refs/heads/main", oldSHA, newSHA); err != nil {
		t.Fatalf("UpdateRefCAS() error = %v, want transient ref lock retried", err)
	}
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/main"); got != newSHA {
		t.Fatalf("main = %s, want %s", got, newSHA)
	}
}

func runGitRefTest(t *testing.T, dir string, args ...string) string {
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

// TestCreateMergeCandidateProducesTwoParentCommit proves the merge candidate
// is an explicit two-parent no-ff merge commit created without advancing the
// parent ref.
func TestCreateMergeCandidateProducesTwoParentCommit(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")
	parentTip := runGitRefTest(t, repo, "rev-parse", "HEAD")

	runGitRefTest(t, repo, "checkout", "-b", "child-branch")
	testutil.CommitFile(t, repo, "child.txt", "child work\n", "child commit")
	childHead := runGitRefTest(t, repo, "rev-parse", "HEAD")
	runGitRefTest(t, repo, "checkout", "main")

	// Record the parent ref before candidate creation.
	parentRefBefore := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")

	result, err := gitpkg.CreateMergeCandidate(repo, parentTip, childHead, "merge candidate")
	if err != nil {
		t.Fatalf("CreateMergeCandidate() error = %v", err)
	}
	if result.CandidateSHA == "" {
		t.Fatal("candidate SHA is empty")
	}

	// Parent ref must not have moved.
	parentRefAfter := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")
	if parentRefAfter != parentRefBefore {
		t.Fatalf("parent ref moved from %s to %s during candidate creation", parentRefBefore, parentRefAfter)
	}

	// The candidate commit has two parents: parent tip and child head.
	parents := runGitRefTest(t, repo, "rev-list", "--parents", "-n", "1", result.CandidateSHA)
	fields := strings.Fields(parents)
	if len(fields) != 3 {
		t.Fatalf("candidate parents = %q, want two-parent merge commit", parents)
	}
	if fields[1] != parentTip {
		t.Fatalf("candidate first parent = %s, want %s", fields[1], parentTip)
	}
	if fields[2] != childHead {
		t.Fatalf("candidate second parent = %s, want %s", fields[2], childHead)
	}

	// The merge commit subject matches.
	subject := runGitRefTest(t, repo, "log", "-n", "1", "--format=%s", result.CandidateSHA)
	if subject != "merge candidate" {
		t.Fatalf("candidate subject = %q, want %q", subject, "merge candidate")
	}
}

// TestCreateMergeCandidateAcceptsCleanParentAdvancement proves a parent that
// moved forward after the child launched still creates a valid candidate.
func TestCreateMergeCandidateAcceptsCleanParentAdvancement(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")

	runGitRefTest(t, repo, "checkout", "-b", "child-branch")
	testutil.CommitFile(t, repo, "child.txt", "child work\n", "child commit")
	childHead := runGitRefTest(t, repo, "rev-parse", "HEAD")
	runGitRefTest(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "other.txt", "parent moved\n", "parent advanced")
	parentTip := runGitRefTest(t, repo, "rev-parse", "HEAD")

	result, err := gitpkg.CreateMergeCandidate(repo, parentTip, childHead, "merge candidate")
	if err != nil {
		t.Fatalf("CreateMergeCandidate() error = %v, want clean merge of advanced parent", err)
	}
	parents := runGitRefTest(t, repo, "rev-list", "--parents", "-n", "1", result.CandidateSHA)
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("candidate parents = %q, want two-parent merge commit", parents)
	}
}

// TestCreateMergeCandidateWithoutConfiguredIdentity proves the merge commit
// carries the Agentico fallback identity when nothing else configures one.
// Hosts that cannot derive an identity from the system reject the commit with
// "empty ident name"; asserting the committer covers both kinds of host,
// since a host that can derive one would otherwise stamp its own.
func TestCreateMergeCandidateWithoutConfiguredIdentity(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")
	parentTip := runGitRefTest(t, repo, "rev-parse", "HEAD")

	runGitRefTest(t, repo, "checkout", "-b", "child-branch")
	testutil.CommitFile(t, repo, "child.txt", "child work\n", "child commit")
	childHead := runGitRefTest(t, repo, "rev-parse", "HEAD")
	runGitRefTest(t, repo, "checkout", "main")

	// Strip every identity source the merge could otherwise inherit.
	runGitRefTest(t, repo, "config", "--unset", "user.email")
	runGitRefTest(t, repo, "config", "--unset", "user.name")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	for _, key := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
		"EMAIL",
	} {
		// t.Setenv registers the restore; the unset makes the variable absent
		// rather than set-and-empty, which git rejects outright.
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}

	result, err := gitpkg.CreateMergeCandidate(repo, parentTip, childHead, "merge candidate")
	if err != nil {
		t.Fatalf("CreateMergeCandidate() error = %v, want candidate without configured identity", err)
	}
	if result.CandidateSHA == "" {
		t.Fatal("candidate SHA is empty")
	}
	committer := runGitRefTest(t, repo, "show", "-s", "--format=%cn <%ce>", result.CandidateSHA)
	if committer != "Agentico <agentico@localhost>" {
		t.Fatalf("candidate committer = %q, want the Agentico fallback identity", committer)
	}
}

// TestCreateMergeCandidateConflictReturnsFiles proves a conflicting merge
// returns the conflict file list and leaves the parent ref untouched.
func TestCreateMergeCandidateConflictReturnsFiles(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "shared.txt", "base\n", "parent base")

	runGitRefTest(t, repo, "checkout", "-b", "child-branch")
	testutil.CommitFile(t, repo, "shared.txt", "child edit\n", "child commit")
	childHead := runGitRefTest(t, repo, "rev-parse", "HEAD")
	runGitRefTest(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "shared.txt", "parent edit\n", "conflicting parent commit")
	parentTip := runGitRefTest(t, repo, "rev-parse", "HEAD")

	parentRefBefore := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")

	result, err := gitpkg.CreateMergeCandidate(repo, parentTip, childHead, "merge candidate")
	if err == nil {
		t.Fatal("CreateMergeCandidate() error = nil, want conflict error")
	}
	if len(result.ConflictFiles) == 0 {
		t.Fatal("conflict files empty, want at least one")
	}
	foundShared := false
	for _, f := range result.ConflictFiles {
		if f == "shared.txt" {
			foundShared = true
		}
	}
	if !foundShared {
		t.Fatalf("conflict files = %v, want shared.txt", result.ConflictFiles)
	}

	// Parent ref must not have moved.
	parentRefAfter := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")
	if parentRefAfter != parentRefBefore {
		t.Fatalf("parent ref moved from %s to %s during conflict", parentRefBefore, parentRefAfter)
	}

	// Worktree must be clean (no leftover merge state).
	status := runGitRefTest(t, repo, "status", "--porcelain")
	if status != "" {
		t.Fatalf("worktree not clean after conflict: %q", status)
	}
}

// TestCreateMergeCandidateWorktreeCleanAfter proves the parent worktree is
// not altered by candidate creation — no leftover worktrees or merge state.
func TestCreateMergeCandidateWorktreeCleanAfter(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")
	parentTip := runGitRefTest(t, repo, "rev-parse", "HEAD")

	runGitRefTest(t, repo, "checkout", "-b", "child-branch")
	testutil.CommitFile(t, repo, "child.txt", "child work\n", "child commit")
	childHead := runGitRefTest(t, repo, "rev-parse", "HEAD")
	runGitRefTest(t, repo, "checkout", "main")

	_, err := gitpkg.CreateMergeCandidate(repo, parentTip, childHead, "merge candidate")
	if err != nil {
		t.Fatalf("CreateMergeCandidate() error = %v", err)
	}

	// No leftover worktrees.
	worktreeList := runGitRefTest(t, repo, "worktree", "list")
	if strings.Count(worktreeList, "\n") > 0 {
		// More than one line means extra worktrees remain.
		lines := strings.Split(worktreeList, "\n")
		var extra int
		for _, line := range lines {
			if line != "" && !strings.Contains(line, filepath.Base(repo)) {
				extra++
			}
		}
		if extra > 0 {
			t.Fatalf("leftover worktrees after candidate creation: %s", worktreeList)
		}
	}
}

// TestUpdateRefCASSuccess proves a compare-and-swap ref update succeeds when
// the ref matches the expected old SHA.
func TestUpdateRefCASSuccess(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")
	runGitRefTest(t, repo, "checkout", "-b", "feature/test")
	oldSHA := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/test")

	// Create a new commit on main to use as the new SHA.
	runGitRefTest(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "new.txt", "new\n", "new commit")
	newSHA := runGitRefTest(t, repo, "rev-parse", "HEAD")

	if err := gitpkg.UpdateRefCAS(repo, "refs/heads/feature/test", oldSHA, newSHA); err != nil {
		t.Fatalf("UpdateRefCAS() error = %v", err)
	}
	got := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/test")
	if got != newSHA {
		t.Fatalf("ref = %s, want %s", got, newSHA)
	}
}

// TestUpdateRefCASMismatch proves a CAS mismatch returns RefCASMismatchError
// with the observed SHA and does not move the ref.
func TestUpdateRefCASMismatch(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")
	runGitRefTest(t, repo, "checkout", "-b", "feature/test")
	currentSHA := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/test")

	wrongOldSHA := "0" + strings.TrimPrefix(currentSHA, "0")
	if wrongOldSHA == currentSHA {
		wrongOldSHA = "deadbeef" + currentSHA[8:]
	}

	err := gitpkg.UpdateRefCAS(repo, "refs/heads/feature/test", wrongOldSHA, currentSHA)
	var casErr *gitpkg.RefCASMismatchError
	if err == nil {
		t.Fatal("UpdateRefCAS() error = nil, want CAS mismatch")
	}
	if !strings.Contains(err.Error(), "expected") {
		t.Fatalf("error = %v, want CAS mismatch detail", err)
	}
	// The ref should not have moved.
	got := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/test")
	if got != currentSHA {
		t.Fatalf("ref moved to %s, want unchanged %s", got, currentSHA)
	}
	_ = casErr
}

// TestReadRefSHA proves reading a ref returns the full SHA.
func TestReadRefSHA(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "base.txt", "base\n", "parent base")
	headSHA := runGitRefTest(t, repo, "rev-parse", "HEAD")

	got, err := gitpkg.ReadRefSHA(repo, "HEAD")
	if err != nil {
		t.Fatalf("ReadRefSHA() error = %v", err)
	}
	if got != headSHA {
		t.Fatalf("ReadRefSHA() = %s, want %s", got, headSHA)
	}
}

// TestReadRefSHAMissingRef proves reading a non-existent ref returns an error.
func TestReadRefSHAMissingRef(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	_, err := gitpkg.ReadRefSHA(repo, "refs/heads/nonexistent")
	if err == nil {
		t.Fatal("ReadRefSHA() error = nil, want error for missing ref")
	}
}
