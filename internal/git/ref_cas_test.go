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
	"errors"
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

// TestUpdateRefsTransactionMovesTwoRefsAtomically proves a transaction moving
// two branches — one checked out in a linked worktree — succeeds, both refs
// resolve to the new SHAs, and the linked worktree keeps its branch name.
func TestUpdateRefsTransactionMovesTwoRefsAtomically(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	runGitRefTest(t, repo, "branch", "feature/a")
	runGitRefTest(t, repo, "branch", "feature/b")
	oldA := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a")
	oldB := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/b")

	linked := filepath.Join(t.TempDir(), "wt")
	runGitRefTest(t, repo, "worktree", "add", linked, "feature/a")
	t.Cleanup(func() {
		runGitRefTest(t, repo, "worktree", "remove", "--force", linked)
	})

	newA := testutil.CommitFile(t, repo, "a.txt", "a\n", "commit for a")
	newB := testutil.CommitFile(t, repo, "b.txt", "b\n", "commit for b")

	err := gitpkg.UpdateRefsTransaction(repo, []gitpkg.RefUpdate{
		{Ref: "refs/heads/feature/a", OldSHA: oldA, NewSHA: newA},
		{Ref: "refs/heads/feature/b", OldSHA: oldB, NewSHA: newB},
	})
	if err != nil {
		t.Fatalf("UpdateRefsTransaction() error = %v", err)
	}
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a"); got != newA {
		t.Fatalf("feature/a = %s, want %s", got, newA)
	}
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/b"); got != newB {
		t.Fatalf("feature/b = %s, want %s", got, newB)
	}
	if got := runGitRefTest(t, linked, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/a" {
		t.Fatalf("linked worktree branch = %q, want %q", got, "feature/a")
	}
}

// TestUpdateRefsTransactionMismatchIsAtomic proves a stale expected SHA fails
// with a mismatch naming the observed ref and SHA, and neither ref moved.
func TestUpdateRefsTransactionMismatchIsAtomic(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	runGitRefTest(t, repo, "branch", "feature/a")
	runGitRefTest(t, repo, "branch", "feature/b")
	oldA := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a")
	oldB := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/b")

	newA := testutil.CommitFile(t, repo, "a.txt", "a\n", "commit for a")
	newB := testutil.CommitFile(t, repo, "b.txt", "b\n", "commit for b")

	// feature/b moves after its expected SHA was captured.
	movedB := testutil.CommitFile(t, repo, "b2.txt", "b2\n", "commit that moves b")
	runGitRefTest(t, repo, "branch", "-f", "feature/b", movedB)

	err := gitpkg.UpdateRefsTransaction(repo, []gitpkg.RefUpdate{
		{Ref: "refs/heads/feature/a", OldSHA: oldA, NewSHA: newA},
		{Ref: "refs/heads/feature/b", OldSHA: oldB, NewSHA: newB},
	})
	var mismatch *gitpkg.RefCASMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("UpdateRefsTransaction() error = %v, want *RefCASMismatchError", err)
	}
	if mismatch.Ref != "refs/heads/feature/b" {
		t.Fatalf("mismatch ref = %q, want refs/heads/feature/b", mismatch.Ref)
	}
	if mismatch.Observed != movedB {
		t.Fatalf("mismatch observed = %s, want %s", mismatch.Observed, movedB)
	}
	// Neither ref moved.
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a"); got != oldA {
		t.Fatalf("feature/a moved to %s, want unchanged %s", got, oldA)
	}
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/b"); got != movedB {
		t.Fatalf("feature/b = %s, want %s (externally moved value)", got, movedB)
	}
}

// TestUpdateRefsTransactionEmptyIsNoOp proves an empty update list succeeds
// without touching anything.
func TestUpdateRefsTransactionEmptyIsNoOp(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	mainBefore := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")
	if err := gitpkg.UpdateRefsTransaction(repo, nil); err != nil {
		t.Fatalf("UpdateRefsTransaction(nil) error = %v", err)
	}
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/main"); got != mainBefore {
		t.Fatalf("main = %s, want unchanged %s", got, mainBefore)
	}
}

// refState reports a ref's SHA and whether it exists, independently of the
// functions under test: exit code 1 from rev-parse --verify --quiet means the
// ref is absent.
func refState(t *testing.T, repo, ref string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", ref)
	cmd.Env = testutil.GitTestEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// TestReadRefSHAOrAbsent proves the absent-aware reader returns the SHA for a
// present ref, absent=true with no error for a missing ref, and an error only
// when git itself fails (here: a path that is not a repository).
func TestReadRefSHAOrAbsent(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	headSHA := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")

	sha, absent, err := gitpkg.ReadRefSHAOrAbsent(repo, "refs/heads/main")
	if err != nil || absent || sha != headSHA {
		t.Fatalf("ReadRefSHAOrAbsent(present) = %q, %v, %v; want %s, false, nil", sha, absent, err, headSHA)
	}

	sha, absent, err = gitpkg.ReadRefSHAOrAbsent(repo, "refs/heads/nonexistent")
	if err != nil || !absent || sha != "" {
		t.Fatalf("ReadRefSHAOrAbsent(missing) = %q, %v, %v; want empty SHA, true, nil", sha, absent, err)
	}

	_, absent, err = gitpkg.ReadRefSHAOrAbsent(t.TempDir(), "refs/heads/main")
	if err == nil || absent {
		t.Fatalf("ReadRefSHAOrAbsent(non-repo) absent = %v, err = %v; want a genuine read error, not absent", absent, err)
	}
}

// TestUpdateRefsTransactionCreatesRefsOnAbsence proves a batch of create
// lines (empty old SHA) creates the refs at their new SHAs, and re-running
// the same batch fails with the compare-and-swap mismatch error naming the
// now-existing ref.
func TestUpdateRefsTransactionCreatesRefsOnAbsence(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	tip := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")
	next := testutil.CommitFile(t, repo, "next.txt", "next\n", "next commit")

	updates := []gitpkg.RefUpdate{
		{Ref: "refs/heads/stack/one", NewSHA: tip},
		{Ref: "refs/heads/stack/two", NewSHA: next},
	}
	if err := gitpkg.UpdateRefsTransaction(repo, updates); err != nil {
		t.Fatalf("UpdateRefsTransaction(create) error = %v", err)
	}
	if got, ok := refState(t, repo, "refs/heads/stack/one"); !ok || got != tip {
		t.Fatalf("stack/one = %q, %v; want %s, present", got, ok, tip)
	}
	if got, ok := refState(t, repo, "refs/heads/stack/two"); !ok || got != next {
		t.Fatalf("stack/two = %q, %v; want %s, present", got, ok, next)
	}

	// The same batch again: both refs exist, so the first create's
	// absence expectation is violated.
	err := gitpkg.UpdateRefsTransaction(repo, updates)
	var mismatch *gitpkg.RefCASMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("UpdateRefsTransaction(re-run) error = %v, want *RefCASMismatchError", err)
	}
	if mismatch.Ref != "refs/heads/stack/one" {
		t.Fatalf("mismatch ref = %q, want refs/heads/stack/one", mismatch.Ref)
	}
	if mismatch.Observed != tip {
		t.Fatalf("mismatch observed = %s, want %s", mismatch.Observed, tip)
	}
	if got, ok := refState(t, repo, "refs/heads/stack/one"); !ok || got != tip {
		t.Fatalf("stack/one = %q, %v; want unchanged %s, present", got, ok, tip)
	}
	if got, ok := refState(t, repo, "refs/heads/stack/two"); !ok || got != next {
		t.Fatalf("stack/two = %q, %v; want unchanged %s, present", got, ok, next)
	}
}

// TestUpdateRefsTransactionCreateVerifyUpdateIsAtomic proves one batch can
// mix a create, a verify of an unchanged ref, and a plain update; a wrong
// expectation on any line fails the whole batch with the mismatch error and
// leaves every ref as it was.
func TestUpdateRefsTransactionCreateVerifyUpdateIsAtomic(t *testing.T) {
	type lineFailure struct {
		name    string
		mutate  func(t *testing.T, repo, a, b, next string) []gitpkg.RefUpdate
		wantRef string
		// wantObservedKey names which per-subtest SHA ("a", "b", or "next")
		// the mismatch must report; the SHAs only exist inside each subtest.
		wantObservedKey string
		// wantNewPresent records whether the case pre-creates feature/new
		// (the create-line violation case); the failed batch must leave it
		// exactly as it was: present at next, or absent.
		wantNewPresent bool
	}
	cases := []lineFailure{
		{
			name: "create line finds the ref already present",
			mutate: func(t *testing.T, repo, a, b, next string) []gitpkg.RefUpdate {
				runGitRefTest(t, repo, "branch", "feature/new", next)
				return []gitpkg.RefUpdate{
					{Ref: "refs/heads/feature/new", NewSHA: next},
					{Ref: "refs/heads/feature/a", OldSHA: a, NewSHA: a},
					{Ref: "refs/heads/feature/b", OldSHA: b, NewSHA: next},
				}
			},
			wantRef:         "refs/heads/feature/new",
			wantObservedKey: "next",
			wantNewPresent:  true,
		},
		{
			name: "verify line expects a stale SHA",
			mutate: func(t *testing.T, repo, a, b, next string) []gitpkg.RefUpdate {
				return []gitpkg.RefUpdate{
					{Ref: "refs/heads/feature/new", NewSHA: next},
					{Ref: "refs/heads/feature/a", OldSHA: next, NewSHA: next},
					{Ref: "refs/heads/feature/b", OldSHA: b, NewSHA: next},
				}
			},
			wantRef:         "refs/heads/feature/a",
			wantObservedKey: "a",
		},
		{
			name: "update line expects a stale SHA",
			mutate: func(t *testing.T, repo, a, b, next string) []gitpkg.RefUpdate {
				return []gitpkg.RefUpdate{
					{Ref: "refs/heads/feature/new", NewSHA: next},
					{Ref: "refs/heads/feature/a", OldSHA: a, NewSHA: a},
					{Ref: "refs/heads/feature/b", OldSHA: next, NewSHA: next},
				}
			},
			wantRef:         "refs/heads/feature/b",
			wantObservedKey: "b",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := testutil.InitGitRepo(t)
			runGitRefTest(t, repo, "branch", "feature/a")
			runGitRefTest(t, repo, "branch", "feature/b")
			a := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a")
			b := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/b")
			next := testutil.CommitFile(t, repo, "next.txt", "next\n", "next commit")

			updates := tc.mutate(t, repo, a, b, next)
			err := gitpkg.UpdateRefsTransaction(repo, updates)
			var mismatch *gitpkg.RefCASMismatchError
			if !errors.As(err, &mismatch) {
				t.Fatalf("UpdateRefsTransaction() error = %v, want *RefCASMismatchError", err)
			}
			if mismatch.Ref != tc.wantRef {
				t.Fatalf("mismatch ref = %q, want %q", mismatch.Ref, tc.wantRef)
			}
			wantObserved := map[string]string{"a": a, "b": b, "next": next}[tc.wantObservedKey]
			if mismatch.Observed != wantObserved {
				t.Fatalf("mismatch observed = %s, want %s", mismatch.Observed, wantObserved)
			}
			if got, ok := refState(t, repo, "refs/heads/feature/new"); tc.wantNewPresent != (ok && got == next) {
				t.Fatalf("feature/new = %q, %v; wantNewPresent = %v", got, ok, tc.wantNewPresent)
			}
			if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a"); got != a {
				t.Fatalf("feature/a = %s, want unchanged %s", got, a)
			}
			if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/b"); got != b {
				t.Fatalf("feature/b = %s, want unchanged %s", got, b)
			}
		})
	}

	// With every expectation satisfied the mixed batch applies in one go.
	repo := testutil.InitGitRepo(t)
	runGitRefTest(t, repo, "branch", "feature/a")
	runGitRefTest(t, repo, "branch", "feature/b")
	a := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a")
	b := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/b")
	next := testutil.CommitFile(t, repo, "next.txt", "next\n", "next commit")

	err := gitpkg.UpdateRefsTransaction(repo, []gitpkg.RefUpdate{
		{Ref: "refs/heads/feature/new", NewSHA: next},
		{Ref: "refs/heads/feature/a", OldSHA: a, NewSHA: a},
		{Ref: "refs/heads/feature/b", OldSHA: b, NewSHA: next},
	})
	if err != nil {
		t.Fatalf("UpdateRefsTransaction(mixed) error = %v", err)
	}
	if got, ok := refState(t, repo, "refs/heads/feature/new"); !ok || got != next {
		t.Fatalf("feature/new = %q, %v; want %s, present", got, ok, next)
	}
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a"); got != a {
		t.Fatalf("feature/a = %s, want unchanged %s (verify)", got, a)
	}
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/b"); got != next {
		t.Fatalf("feature/b = %s, want %s (updated)", got, next)
	}
}

// TestUpdateRefsTransactionDeletesRefs proves a delete line (empty new SHA)
// removes the ref when it sits at the expected SHA, refuses with the mismatch
// error and leaves the ref when the expectation is stale, and refuses a
// delete of an absent ref without touching the batch's other lines.
func TestUpdateRefsTransactionDeletesRefs(t *testing.T) {
	t.Run("delete expecting the current SHA removes the ref", func(t *testing.T) {
		repo := testutil.InitGitRepo(t)
		runGitRefTest(t, repo, "branch", "feature/a")
		a := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a")

		err := gitpkg.UpdateRefsTransaction(repo, []gitpkg.RefUpdate{
			{Ref: "refs/heads/feature/a", OldSHA: a},
		})
		if err != nil {
			t.Fatalf("UpdateRefsTransaction(delete) error = %v", err)
		}
		if got, ok := refState(t, repo, "refs/heads/feature/a"); ok {
			t.Fatalf("feature/a = %s, want the ref deleted", got)
		}
	})

	t.Run("delete with a stale expectation fails and leaves the ref", func(t *testing.T) {
		repo := testutil.InitGitRepo(t)
		runGitRefTest(t, repo, "branch", "feature/a")
		a := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a")
		moved := testutil.CommitFile(t, repo, "moved.txt", "moved\n", "moved commit")
		runGitRefTest(t, repo, "branch", "-f", "feature/a", moved)

		err := gitpkg.UpdateRefsTransaction(repo, []gitpkg.RefUpdate{
			{Ref: "refs/heads/feature/a", OldSHA: a},
		})
		var mismatch *gitpkg.RefCASMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("UpdateRefsTransaction(stale delete) error = %v, want *RefCASMismatchError", err)
		}
		if mismatch.Ref != "refs/heads/feature/a" {
			t.Fatalf("mismatch ref = %q, want refs/heads/feature/a", mismatch.Ref)
		}
		if mismatch.Observed != moved {
			t.Fatalf("mismatch observed = %s, want %s", mismatch.Observed, moved)
		}
		if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a"); got != moved {
			t.Fatalf("feature/a = %s, want the ref left at %s", got, moved)
		}
	})

	t.Run("deleting an absent ref fails without touching other lines", func(t *testing.T) {
		repo := testutil.InitGitRepo(t)
		runGitRefTest(t, repo, "branch", "feature/a")
		a := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a")
		next := testutil.CommitFile(t, repo, "next.txt", "next\n", "next commit")

		err := gitpkg.UpdateRefsTransaction(repo, []gitpkg.RefUpdate{
			{Ref: "refs/heads/feature/missing", OldSHA: a},
			{Ref: "refs/heads/feature/a", OldSHA: a, NewSHA: next},
		})
		var mismatch *gitpkg.RefCASMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("UpdateRefsTransaction(absent delete) error = %v, want *RefCASMismatchError", err)
		}
		if mismatch.Ref != "refs/heads/feature/missing" {
			t.Fatalf("mismatch ref = %q, want refs/heads/feature/missing", mismatch.Ref)
		}
		if mismatch.Observed != "absent" {
			t.Fatalf("mismatch observed = %q, want the absent observation", mismatch.Observed)
		}
		if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/feature/a"); got != a {
			t.Fatalf("feature/a = %s, want unchanged %s", got, a)
		}
	})
}

// TestUpdateRefsTransactionRejectsDegenerateUpdates proves an update with
// neither an old nor a new SHA — neither a create nor a delete — is refused
// with a clear error before git runs.
func TestUpdateRefsTransactionRejectsDegenerateUpdates(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	err := gitpkg.UpdateRefsTransaction(repo, []gitpkg.RefUpdate{
		{Ref: "refs/heads/feature/a"},
	})
	if err == nil {
		t.Fatal("UpdateRefsTransaction(degenerate) error = nil, want a refusal")
	}
	var mismatch *gitpkg.RefCASMismatchError
	if errors.As(err, &mismatch) {
		t.Fatalf("UpdateRefsTransaction(degenerate) error = %v, want a plain error, not a mismatch", err)
	}
	if !strings.Contains(err.Error(), "refs/heads/feature/a") {
		t.Fatalf("error = %v, want it to name the degenerate ref", err)
	}
	if _, ok := refState(t, repo, "refs/heads/feature/a"); ok {
		t.Fatal("feature/a exists, want the refusal to have touched nothing")
	}
}
