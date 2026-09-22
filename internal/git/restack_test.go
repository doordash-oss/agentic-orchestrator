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

	gitpkg "github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// commitAsAuthor creates a commit with a distinct author identity so tests
// can prove author preservation through a replay.
func commitAsAuthor(t *testing.T, repo, filename, content, message, authorName, authorEmail string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, filename), []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", filename, err)
	}
	cmd := exec.Command("git", "add", filename)
	cmd.Dir = repo
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME="+authorName,
		"GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v\n%s", filename, err, out)
	}
	cmd = exec.Command("git", "commit", "-m", message)
	cmd.Dir = repo
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME="+authorName,
		"GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit %s: %v\n%s", message, err, out)
	}
	return runGitRefTest(t, repo, "rev-parse", "HEAD")
}

func cutPointSHA(result *gitpkg.RestackResult, label string) string {
	for _, cp := range result.CutPoints {
		if cp.Label == label {
			return cp.SHA
		}
	}
	return ""
}

func subjectsBetween(t *testing.T, repo, from, to string) []string {
	t.Helper()
	out := runGitRefTest(t, repo, "log", "--format=%s", from+".."+to)
	var subjects []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			subjects = append(subjects, line)
		}
	}
	return subjects
}

func assertOnlyMainWorktree(t *testing.T, repo string) {
	t.Helper()
	list := runGitRefTest(t, repo, "worktree", "list")
	lines := 0
	for _, line := range strings.Split(list, "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("git worktree list shows %d worktrees, want only the main repository:\n%s", lines, list)
	}
}

// TestRestackChainInsertAfterLayerTip proves an insert-after-t1 restack puts
// the inserted change in the layer-1 tip, replays the layer-2 segment with
// identical messages and authors, keeps every lower cut point, and produces
// the same top tree as applying the change on the old top.
func TestRestackChainInsertAfterLayerTip(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	sha := map[string]string{}
	sha["base"] = runGitRefTest(t, repo, "rev-parse", "HEAD")
	commit := func(label, filename, content, message string) {
		sha[label] = testutil.CommitFile(t, repo, filename, content, message)
	}
	commit("a1", "a1.txt", "a1 content\n", "a1 commit")
	commit("a2", "a2.txt", "a2 content\n", "a2 commit")
	commit("t1", "l1.txt", "layer 1 work\n", "layer 1 tip")
	commit("b1", "b1.txt", "b1 content\n", "layer 2 first commit")
	commit("t2", "l2.txt", "layer 2 work\n", "layer 2 tip")

	// The fix commit is authored by someone else on a branch off the old top.
	runGitRefTest(t, repo, "checkout", "-b", "fix-branch")
	fixSHA := commitAsAuthor(t, repo, "fix.txt", "fix content\n", "fix commit", "Fixer", "fixer@fix.test")
	runGitRefTest(t, repo, "checkout", "main")

	mainBefore := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")
	cutPoints := []gitpkg.RestackCutPoint{}
	for _, label := range []string{"base", "a1", "a2", "t1", "t2"} {
		cutPoints = append(cutPoints, gitpkg.RestackCutPoint{Label: label, SHA: sha[label]})
	}
	result, err := gitpkg.RestackChain(repo, cutPoints, []gitpkg.RestackOp{
		{Kind: gitpkg.RestackInsertAfter, CutPointLabel: "t1", CommitSHAs: []string{fixSHA}},
	})
	if err != nil {
		t.Fatalf("RestackChain() error = %v", err)
	}

	// Lower cut points unchanged; t1 and t2 remapped.
	for _, label := range []string{"base", "a1", "a2"} {
		if got := cutPointSHA(result, label); got != sha[label] {
			t.Fatalf("cut point %s = %s, want unchanged %s", label, got, sha[label])
		}
	}
	inserted := result.CommitMap[fixSHA]
	if inserted == "" || inserted == fixSHA || inserted == sha["t1"] {
		t.Fatalf("t1 remap = %q, want the replayed fix commit", inserted)
	}
	if got := cutPointSHA(result, "t1"); got != inserted {
		t.Fatalf("cut point t1 = %s, want the inserted fix %s", got, inserted)
	}
	replayedT2 := result.CommitMap[sha["t2"]]
	if replayedT2 == "" || replayedT2 == sha["t2"] {
		t.Fatalf("cut point t2 = %s, want a remapped SHA", replayedT2)
	}
	if result.HeadSHA != replayedT2 {
		t.Fatalf("HeadSHA = %s, want the replayed top %s", result.HeadSHA, replayedT2)
	}

	// Layer-1 tip now carries the inserted change with its author preserved.
	subject := runGitRefTest(t, repo, "log", "-n", "1", "--format=%s", inserted)
	if subject != "fix commit" {
		t.Fatalf("layer-1 tip subject = %q, want %q", subject, "fix commit")
	}
	author := runGitRefTest(t, repo, "log", "-n", "1", "--format=%an <%ae>", inserted)
	if author != "Fixer <fixer@fix.test>" {
		t.Fatalf("layer-1 tip author = %q, want the preserved fix author", author)
	}

	// Replayed layer-2 segment keeps messages and authors.
	for old, wantSubject := range map[string]string{
		sha["b1"]: "layer 2 first commit",
		sha["t2"]: "layer 2 tip",
	} {
		got := result.CommitMap[old]
		if got == "" || got == old {
			t.Fatalf("commit %s (%s) was not replayed", wantSubject, old)
		}
		if s := runGitRefTest(t, repo, "log", "-n", "1", "--format=%s", got); s != wantSubject {
			t.Fatalf("replayed subject = %q, want %q", s, wantSubject)
		}
		if a := runGitRefTest(t, repo, "log", "-n", "1", "--format=%an <%ae>", got); a != "Test <test@test.com>" {
			t.Fatalf("replayed author = %q, want preserved author", a)
		}
	}

	// The whole chain stays linear with the expected message order.
	wantSubjects := []string{"layer 2 tip", "layer 2 first commit", "fix commit", "layer 1 tip", "a2 commit", "a1 commit"}
	if got := subjectsBetween(t, repo, sha["base"], result.HeadSHA); !equalStrings(got, wantSubjects) {
		t.Fatalf("chain subjects = %v, want %v", got, wantSubjects)
	}

	// The new top tree equals applying the same change on the old top.
	scratch := filepath.Join(t.TempDir(), "wt")
	runGitRefTest(t, repo, "worktree", "add", "--detach", scratch, sha["t2"])
	runGitRefTest(t, scratch, "cherry-pick", fixSHA)
	expectedTree := runGitRefTest(t, scratch, "rev-parse", "HEAD^{tree}")
	runGitRefTest(t, repo, "worktree", "remove", "--force", scratch)

	newTopTree, err := gitpkg.CommitTreeSHA(repo, result.HeadSHA)
	if err != nil {
		t.Fatalf("CommitTreeSHA() error = %v", err)
	}
	if newTopTree != expectedTree {
		t.Fatalf("new top tree = %s, want %s (same change on the old top)", newTopTree, expectedTree)
	}

	// No refs moved, the calling worktree is untouched, and no temporary
	// worktree remains.
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/main"); got != mainBefore {
		t.Fatalf("main moved from %s to %s", mainBefore, got)
	}
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/fix-branch"); got != fixSHA {
		t.Fatalf("fix-branch moved from %s to %s", fixSHA, got)
	}
	if status := runGitRefTest(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("main worktree dirty after restack: %q", status)
	}
	assertOnlyMainWorktree(t, repo)
}

// TestRestackChainReplaceBase proves a base replacement replays every segment
// onto the new base and remaps every cut point.
func TestRestackChainReplaceBase(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	sha := map[string]string{}
	sha["base"] = runGitRefTest(t, repo, "rev-parse", "HEAD")
	commit := func(label, filename, content, message string) {
		sha[label] = testutil.CommitFile(t, repo, filename, content, message)
	}
	commit("a1", "a1.txt", "a1 content\n", "a1 commit")
	commit("t1", "l1.txt", "layer 1 work\n", "layer 1 tip")
	commit("t2", "l2.txt", "layer 2 work\n", "layer 2 tip")

	runGitRefTest(t, repo, "checkout", "-b", "newbase", sha["base"])
	newBase := testutil.CommitFile(t, repo, "newbase.txt", "new base\n", "replacement base")
	runGitRefTest(t, repo, "checkout", "main")

	cutPoints := []gitpkg.RestackCutPoint{}
	for _, label := range []string{"base", "a1", "t1", "t2"} {
		cutPoints = append(cutPoints, gitpkg.RestackCutPoint{Label: label, SHA: sha[label]})
	}
	result, err := gitpkg.RestackChain(repo, cutPoints, []gitpkg.RestackOp{
		{Kind: gitpkg.RestackReplaceBase, ReplaceBaseSHA: newBase},
	})
	if err != nil {
		t.Fatalf("RestackChain() error = %v", err)
	}

	if got := cutPointSHA(result, "base"); got != newBase {
		t.Fatalf("base cut point = %s, want the replacement %s", got, newBase)
	}
	for _, label := range []string{"a1", "t1", "t2"} {
		got := cutPointSHA(result, label)
		if got == "" || got == sha[label] {
			t.Fatalf("cut point %s = %s, want a remapped SHA", label, got)
		}
		if result.CommitMap[sha[label]] != got {
			t.Fatalf("cut point %s = %s, want the replayed commit %s", label, got, result.CommitMap[sha[label]])
		}
	}
	if got := runGitRefTest(t, repo, "rev-list", "--count", newBase+".."+result.HeadSHA); got != "3" {
		t.Fatalf("chain length above the new base = %s, want 3", got)
	}
	assertOnlyMainWorktree(t, repo)
}

// TestRestackChainDropSegment proves dropping the layer-1 segment leaves the
// layer-2 commits replayed directly on the base, with layer 1's cut point
// mapping to the base.
func TestRestackChainDropSegment(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	sha := map[string]string{}
	sha["base"] = runGitRefTest(t, repo, "rev-parse", "HEAD")
	commit := func(label, filename, content, message string) {
		sha[label] = testutil.CommitFile(t, repo, filename, content, message)
	}
	commit("a1", "a1.txt", "a1 content\n", "a1 commit")
	commit("a2", "a2.txt", "a2 content\n", "a2 commit")
	commit("t1", "l1.txt", "layer 1 work\n", "layer 1 tip")
	commit("b1", "b1.txt", "b1 content\n", "layer 2 first commit")
	commit("t2", "l2.txt", "layer 2 work\n", "layer 2 tip")

	cutPoints := []gitpkg.RestackCutPoint{}
	for _, label := range []string{"base", "t1", "t2"} {
		cutPoints = append(cutPoints, gitpkg.RestackCutPoint{Label: label, SHA: sha[label]})
	}
	result, err := gitpkg.RestackChain(repo, cutPoints, []gitpkg.RestackOp{
		{Kind: gitpkg.RestackDropSegment, CutPointLabel: "t1"},
	})
	if err != nil {
		t.Fatalf("RestackChain() error = %v", err)
	}

	if got := cutPointSHA(result, "t1"); got != sha["base"] {
		t.Fatalf("layer-1 cut point = %s, want the base %s", got, sha["base"])
	}
	newT2 := cutPointSHA(result, "t2")
	if newT2 == "" || newT2 == sha["t2"] {
		t.Fatalf("layer-2 cut point = %s, want a remapped SHA", newT2)
	}
	if result.HeadSHA != newT2 {
		t.Fatalf("HeadSHA = %s, want the remapped layer-2 tip %s", result.HeadSHA, newT2)
	}

	wantSubjects := []string{"layer 2 tip", "layer 2 first commit"}
	if got := subjectsBetween(t, repo, sha["base"], result.HeadSHA); !equalStrings(got, wantSubjects) {
		t.Fatalf("chain subjects = %v, want %v", got, wantSubjects)
	}

	for _, label := range []string{"a1", "a2", "t1"} {
		if result.CommitMap[sha[label]] != sha["base"] {
			t.Fatalf("dropped commit %s maps to %s, want the base %s", label, result.CommitMap[sha[label]], sha["base"])
		}
	}
	if len(result.Dropped) != 3 {
		t.Fatalf("dropped = %v, want the three layer-1 commits", result.Dropped)
	}
	assertOnlyMainWorktree(t, repo)
}

// TestRestackChainAppendChain proves appended commits land above the old top
// with the old cut points unchanged.
func TestRestackChainAppendChain(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	sha := map[string]string{}
	sha["base"] = runGitRefTest(t, repo, "rev-parse", "HEAD")
	commit := func(label, filename, content, message string) {
		sha[label] = testutil.CommitFile(t, repo, filename, content, message)
	}
	commit("a1", "a1.txt", "a1 content\n", "a1 commit")
	commit("t1", "l1.txt", "layer 1 work\n", "layer 1 tip")
	commit("t2", "l2.txt", "layer 2 work\n", "layer 2 tip")

	runGitRefTest(t, repo, "checkout", "-b", "app-branch")
	commit("c1", "c1.txt", "c1 content\n", "appended one")
	commit("c2", "c2.txt", "c2 content\n", "appended two")
	runGitRefTest(t, repo, "checkout", "main")

	cutPoints := []gitpkg.RestackCutPoint{}
	for _, label := range []string{"base", "a1", "t1", "t2"} {
		cutPoints = append(cutPoints, gitpkg.RestackCutPoint{Label: label, SHA: sha[label]})
	}
	result, err := gitpkg.RestackChain(repo, cutPoints, []gitpkg.RestackOp{
		{Kind: gitpkg.RestackAppendChain, CommitSHAs: []string{sha["c1"], sha["c2"]}},
	})
	if err != nil {
		t.Fatalf("RestackChain() error = %v", err)
	}

	for _, label := range []string{"base", "a1", "t1", "t2"} {
		if got := cutPointSHA(result, label); got != sha[label] {
			t.Fatalf("cut point %s = %s, want unchanged %s", label, got, sha[label])
		}
	}
	if result.HeadSHA != result.CommitMap[sha["c2"]] {
		t.Fatalf("HeadSHA = %s, want the replayed last appended commit", result.HeadSHA)
	}
	wantSubjects := []string{"appended two", "appended one"}
	if got := subjectsBetween(t, repo, sha["t2"], result.HeadSHA); !equalStrings(got, wantSubjects) {
		t.Fatalf("appended subjects = %v, want %v", got, wantSubjects)
	}
	assertOnlyMainWorktree(t, repo)
}

// TestRestackChainDropsEmptyReplay proves a replayed commit that becomes
// empty is dropped, mapped to its predecessor's new SHA, and that a cut point
// sitting on it shares the new SHA of the cut point below.
func TestRestackChainDropsEmptyReplay(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	sha := map[string]string{}
	sha["base"] = runGitRefTest(t, repo, "rev-parse", "HEAD")
	commit := func(label, filename, content, message string) {
		sha[label] = testutil.CommitFile(t, repo, filename, content, message)
	}
	commit("a1", "a1.txt", "a1 content\n", "a1 commit")
	commit("t1", "x.txt", "one\n", "layer 1 tip")
	commit("b1", "x.txt", "one\ntwo\n", "layer 2 first commit")
	commit("t2", "y.txt", "y content\n", "layer 2 tip")

	// A fix off t1 applying the same change b1 makes: replaying b1 afterwards
	// leaves the index identical to HEAD.
	runGitRefTest(t, repo, "checkout", "-b", "fix-branch", sha["t1"])
	fixSHA := testutil.CommitFile(t, repo, "x.txt", "one\ntwo\n", "fix commit")
	runGitRefTest(t, repo, "checkout", "main")

	cutPoints := []gitpkg.RestackCutPoint{}
	for _, label := range []string{"base", "a1", "t1", "b1", "t2"} {
		cutPoints = append(cutPoints, gitpkg.RestackCutPoint{Label: label, SHA: sha[label]})
	}
	result, err := gitpkg.RestackChain(repo, cutPoints, []gitpkg.RestackOp{
		{Kind: gitpkg.RestackInsertAfter, CutPointLabel: "t1", CommitSHAs: []string{fixSHA}},
	})
	if err != nil {
		t.Fatalf("RestackChain() error = %v", err)
	}

	inserted := result.CommitMap[fixSHA]
	if inserted == "" {
		t.Fatal("the inserted fix commit was not applied")
	}
	if result.CommitMap[sha["b1"]] != inserted {
		t.Fatalf("empty replay of b1 maps to %s, want its predecessor %s", result.CommitMap[sha["b1"]], inserted)
	}
	if len(result.Dropped) != 1 || result.Dropped[0] != sha["b1"] {
		t.Fatalf("dropped = %v, want [%s]", result.Dropped, sha["b1"])
	}
	if got := cutPointSHA(result, "b1"); got != cutPointSHA(result, "t1") {
		t.Fatalf("cut point b1 = %s, want the cut point below (%s)", got, cutPointSHA(result, "t1"))
	}
	if got := cutPointSHA(result, "t2"); got == "" || got == sha["t2"] {
		t.Fatalf("cut point t2 = %s, want a remapped SHA", got)
	}
	wantSubjects := []string{"layer 2 tip", "fix commit", "layer 1 tip", "a1 commit"}
	if got := subjectsBetween(t, repo, sha["base"], result.HeadSHA); !equalStrings(got, wantSubjects) {
		t.Fatalf("chain subjects = %v, want %v", got, wantSubjects)
	}
	assertOnlyMainWorktree(t, repo)
}

// TestRestackChainConflictReturnsStructuredError proves a conflicting replay
// returns the segment, commit, and conflicted files, and leaves the
// repository exactly as it was: no extra worktree, no cherry-pick in
// progress, no ref moved.
func TestRestackChainConflictReturnsStructuredError(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	sha := map[string]string{}
	sha["base"] = runGitRefTest(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("line1\n"), 0o644); err != nil {
		t.Fatalf("writing shared.txt: %v", err)
	}
	runGitRefTest(t, repo, "add", "shared.txt")
	runGitRefTest(t, repo, "commit", "-m", "shared base")
	commit := func(label, filename, content, message string) {
		sha[label] = testutil.CommitFile(t, repo, filename, content, message)
	}
	commit("t1", "l1.txt", "layer 1 work\n", "layer 1 tip")
	commit("b1", "shared.txt", "line1 changed by layer 2\n", "layer 2 first commit")
	commit("t2", "l2.txt", "layer 2 work\n", "layer 2 tip")

	runGitRefTest(t, repo, "checkout", "-b", "fix-branch", sha["t1"])
	fixSHA := testutil.CommitFile(t, repo, "shared.txt", "line1 changed by the fix\n", "fix commit")
	runGitRefTest(t, repo, "checkout", "main")

	mainBefore := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")
	cutPoints := []gitpkg.RestackCutPoint{}
	for _, label := range []string{"base", "t1", "t2"} {
		cutPoints = append(cutPoints, gitpkg.RestackCutPoint{Label: label, SHA: sha[label]})
	}
	_, err := gitpkg.RestackChain(repo, cutPoints, []gitpkg.RestackOp{
		{Kind: gitpkg.RestackInsertAfter, CutPointLabel: "t1", CommitSHAs: []string{fixSHA}},
	})
	var conflict *gitpkg.RestackConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("RestackChain() error = %v, want *RestackConflictError", err)
	}
	if conflict.SegmentFrom != "t1" || conflict.SegmentTo != "t2" {
		t.Fatalf("conflict segment = %s..%s, want t1..t2", conflict.SegmentFrom, conflict.SegmentTo)
	}
	if conflict.CommitSHA != sha["b1"] {
		t.Fatalf("conflict commit = %s, want %s (b1)", conflict.CommitSHA, sha["b1"])
	}
	if len(conflict.ConflictFiles) != 1 || conflict.ConflictFiles[0] != "shared.txt" {
		t.Fatalf("conflict files = %v, want [shared.txt]", conflict.ConflictFiles)
	}

	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/main"); got != mainBefore {
		t.Fatalf("main moved from %s to %s during the conflict", mainBefore, got)
	}
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/fix-branch"); got != fixSHA {
		t.Fatalf("fix-branch moved from %s to %s during the conflict", fixSHA, got)
	}
	if status := runGitRefTest(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree dirty after conflict: %q", status)
	}
	assertOnlyMainWorktree(t, repo)
	gitDir := runGitRefTest(t, repo, "rev-parse", "--git-dir")
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repo, gitDir)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "CHERRY_PICK_HEAD")); err == nil {
		t.Fatal("a cherry-pick is still in progress in the main repository")
	}
}

// TestRestackChainValidatesCutPoints proves misordered or unknown labels fail
// before any worktree is created.
func TestRestackChainValidatesCutPoints(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	base := runGitRefTest(t, repo, "rev-parse", "HEAD")
	tip := testutil.CommitFile(t, repo, "f.txt", "f\n", "tip")

	if _, err := gitpkg.RestackChain(repo, nil, nil); err == nil {
		t.Fatal("RestackChain() with no cut points = nil error, want validation error")
	}
	// Reversed order: tip is not above base's successor ordering.
	if _, err := gitpkg.RestackChain(repo, []gitpkg.RestackCutPoint{
		{Label: "base", SHA: base},
		{Label: "a", SHA: base},
	}, nil); err == nil {
		t.Fatal("RestackChain() with duplicate SHAs = nil error, want validation error")
	}
	if _, err := gitpkg.RestackChain(repo, []gitpkg.RestackCutPoint{
		{Label: "top", SHA: tip},
		{Label: "base", SHA: base},
	}, nil); err == nil {
		t.Fatal("RestackChain() with reversed cut points = nil error, want validation error")
	}
	if _, err := gitpkg.RestackChain(repo, []gitpkg.RestackCutPoint{
		{Label: "base", SHA: base},
		{Label: "top", SHA: tip},
	}, []gitpkg.RestackOp{{Kind: gitpkg.RestackDropSegment, CutPointLabel: "missing"}}); err == nil {
		t.Fatal("RestackChain() with an unknown label = nil error, want validation error")
	}
	if _, err := gitpkg.RestackChain(repo, []gitpkg.RestackCutPoint{
		{Label: "base", SHA: base},
		{Label: "top", SHA: tip},
	}, []gitpkg.RestackOp{{Kind: gitpkg.RestackDropSegment, CutPointLabel: "base"}}); err == nil {
		t.Fatal("RestackChain() dropping the base segment = nil error, want validation error")
	}
	assertOnlyMainWorktree(t, repo)
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
