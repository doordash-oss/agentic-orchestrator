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

// newResolverConflictRepo builds the canonical conflicting-replay fixture:
// a chain whose second-segment commit b1 touches shared.txt, and a fix
// inserted after the layer-1 tip that touches the same file differently, so
// replaying b1 onto the inserted fix conflicts on shared.txt. It returns the
// repository, the SHA map, the inserted fix SHA, and the assembled cut
// points.
func newResolverConflictRepo(t *testing.T) (repo string, sha map[string]string, fixSHA string, cutPoints []gitpkg.RestackCutPoint) {
	t.Helper()
	repo = testutil.InitGitRepo(t)
	sha = map[string]string{}
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
	// The conflicting commit carries a trailer so the continuation's
	// message preservation is observable beyond the subject.
	sha["b1"] = commitAsAuthor(t, repo, "shared.txt", "line1 changed by layer 2\n",
		"layer 2 first commit\n\nSigned-off-by: Layer Two <layer2@test.test>", "Layer Two", "layer2@test.test")
	commit("t2", "l2.txt", "layer 2 work\n", "layer 2 tip")

	runGitRefTest(t, repo, "checkout", "-b", "fix-branch", sha["t1"])
	fixSHA = testutil.CommitFile(t, repo, "shared.txt", "line1 changed by the fix\n", "fix commit")
	runGitRefTest(t, repo, "checkout", "main")

	for _, label := range []string{"base", "t1", "t2"} {
		cutPoints = append(cutPoints, gitpkg.RestackCutPoint{Label: label, SHA: sha[label]})
	}
	return repo, sha, fixSHA, cutPoints
}

func resolverConflictOps(fixSHA string) []gitpkg.RestackOp {
	return []gitpkg.RestackOp{{Kind: gitpkg.RestackInsertAfter, CutPointLabel: "t1", CommitSHAs: []string{fixSHA}}}
}

// assertResolverCleanup proves the no-side-effect contract: no ref moved,
// the main worktree is clean, and no temporary worktree remains.
func assertResolverCleanup(t *testing.T, repo, mainBefore, fixBefore string) {
	t.Helper()
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/main"); got != mainBefore {
		t.Fatalf("main moved from %s to %s", mainBefore, got)
	}
	if got := runGitRefTest(t, repo, "rev-parse", "refs/heads/fix-branch"); got != fixBefore {
		t.Fatalf("fix-branch moved from %s to %s during the restack", fixBefore, got)
	}
	if status := runGitRefTest(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("main worktree dirty after restack: %q", status)
	}
	assertOnlyMainWorktree(t, repo)
}

// TestRestackResolverResolved proves a resolver that writes marker-free
// content yields a rewritten chain whose replayed commit keeps the original
// author, message, and trailers and contains the resolved content, with the
// pick paused in the temporary worktree while the resolver runs and no ref or
// worktree left behind afterwards.
func TestRestackResolverResolved(t *testing.T) {
	repo, sha, fixSHA, cutPoints := newResolverConflictRepo(t)
	mainBefore := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")

	type call struct {
		input          gitpkg.RestackResolverInput
		pickInProgress bool
		markersPresent bool
	}
	var calls []call
	resolver := func(input gitpkg.RestackResolverInput) (gitpkg.RestackResolverResult, error) {
		gitDir := runGitRefTest(t, input.WorktreePath, "rev-parse", "--git-dir")
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(input.WorktreePath, gitDir)
		}
		_, statErr := os.Stat(filepath.Join(gitDir, "CHERRY_PICK_HEAD"))
		content, readErr := os.ReadFile(filepath.Join(input.WorktreePath, "shared.txt"))
		calls = append(calls, call{
			input:          input,
			pickInProgress: statErr == nil,
			markersPresent: readErr == nil && strings.Contains(string(content), "<<<<<<<"),
		})
		// Resolve by combining both sides, marker-free.
		if err := os.WriteFile(filepath.Join(input.WorktreePath, "shared.txt"),
			[]byte("line1 changed by the fix and layer 2\n"), 0o644); err != nil {
			t.Fatalf("writing the resolution: %v", err)
		}
		return gitpkg.RestackResolverResult{Resolution: gitpkg.RestackResolutionResolved, Attempts: 1}, nil
	}

	result, err := gitpkg.RestackChainWithResolver(repo, cutPoints, resolverConflictOps(fixSHA), resolver, "/tmp/attempts")
	if err != nil {
		t.Fatalf("RestackChainWithResolver() error = %v", err)
	}

	if len(calls) != 1 {
		t.Fatalf("resolver called %d times, want once", len(calls))
	}
	got := calls[0].input
	if got.SegmentFrom != "t1" || got.SegmentTo != "t2" {
		t.Fatalf("resolver segment = %s..%s, want t1..t2", got.SegmentFrom, got.SegmentTo)
	}
	if got.CommitSHA != sha["b1"] {
		t.Fatalf("resolver commit = %s, want %s (b1)", got.CommitSHA, sha["b1"])
	}
	if len(got.ConflictFiles) != 1 || got.ConflictFiles[0] != "shared.txt" {
		t.Fatalf("resolver conflict files = %v, want [shared.txt]", got.ConflictFiles)
	}
	if got.AttemptRoot != "/tmp/attempts" {
		t.Fatalf("resolver attempt root = %q, want the caller-supplied root", got.AttemptRoot)
	}
	if !calls[0].pickInProgress {
		t.Fatal("the cherry-pick was not in progress while the resolver ran")
	}
	if !calls[0].markersPresent {
		t.Fatal("the conflicted file did not hold markers while the resolver ran")
	}
	if _, err := os.Stat(got.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("the temporary worktree %s still exists", got.WorktreePath)
	}

	// The replayed b1 keeps its author, message, and trailer.
	replayed := result.CommitMap[sha["b1"]]
	if replayed == "" || replayed == sha["b1"] {
		t.Fatalf("b1 remap = %q, want a replayed commit", replayed)
	}
	if a := runGitRefTest(t, repo, "log", "-n", "1", "--format=%an <%ae>", replayed); a != "Layer Two <layer2@test.test>" {
		t.Fatalf("replayed b1 author = %q, want the preserved author", a)
	}
	body := runGitRefTest(t, repo, "log", "-n", "1", "--format=%B", replayed)
	if !strings.Contains(body, "layer 2 first commit") || !strings.Contains(body, "Signed-off-by: Layer Two <layer2@test.test>") {
		t.Fatalf("replayed b1 message = %q, want the original subject and trailer", body)
	}
	if content := runGitRefTest(t, repo, "show", replayed+":shared.txt"); content != "line1 changed by the fix and layer 2" {
		t.Fatalf("replayed b1 shared.txt = %q, want the resolved content", content)
	}

	// The chain above the resolved commit still replays.
	if newT2 := result.CommitMap[sha["t2"]]; newT2 == "" || newT2 == sha["t2"] {
		t.Fatalf("t2 remap = %q, want a replayed commit", newT2)
	}
	if len(result.Dropped) != 0 {
		t.Fatalf("dropped = %v, want none", result.Dropped)
	}
	assertResolverCleanup(t, repo, mainBefore, fixSHA)
}

// TestRestackResolverLeftMarkersFails proves a resolver that reports
// resolved while a conflicted file still holds markers fails the run with an
// error naming the file, with the pick aborted and the worktree removed.
func TestRestackResolverLeftMarkersFails(t *testing.T) {
	repo, _, fixSHA, cutPoints := newResolverConflictRepo(t)
	mainBefore := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")

	resolver := func(input gitpkg.RestackResolverInput) (gitpkg.RestackResolverResult, error) {
		return gitpkg.RestackResolverResult{Resolution: gitpkg.RestackResolutionResolved, Attempts: 1}, nil
	}
	_, err := gitpkg.RestackChainWithResolver(repo, cutPoints, resolverConflictOps(fixSHA), resolver, "/tmp/attempts")
	if err == nil {
		t.Fatal("RestackChainWithResolver() = nil error, want a marker failure")
	}
	if !strings.Contains(err.Error(), "shared.txt") {
		t.Fatalf("error %q does not name the file holding markers", err)
	}
	var conflict *gitpkg.RestackConflictError
	if errors.As(err, &conflict) {
		t.Fatal("a marker failure must not surface as a restack conflict")
	}
	assertResolverCleanup(t, repo, mainBefore, fixSHA)
}

// TestRestackResolverTargetSideDropsCommit proves a resolver that takes the
// target's side entirely produces a dropped commit recorded in the dropped
// list with its SHA mapped to its predecessor, and the cut point above it
// still resolves.
func TestRestackResolverTargetSideDropsCommit(t *testing.T) {
	repo, sha, fixSHA, cutPoints := newResolverConflictRepo(t)
	mainBefore := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")

	resolver := func(input gitpkg.RestackResolverInput) (gitpkg.RestackResolverResult, error) {
		if err := os.WriteFile(filepath.Join(input.WorktreePath, "shared.txt"),
			[]byte("line1 changed by the fix\n"), 0o644); err != nil {
			t.Fatalf("writing the target-side resolution: %v", err)
		}
		return gitpkg.RestackResolverResult{Resolution: gitpkg.RestackResolutionResolved, Attempts: 1}, nil
	}
	result, err := gitpkg.RestackChainWithResolver(repo, cutPoints, resolverConflictOps(fixSHA), resolver, "/tmp/attempts")
	if err != nil {
		t.Fatalf("RestackChainWithResolver() error = %v", err)
	}

	inserted := result.CommitMap[fixSHA]
	if inserted == "" {
		t.Fatal("the inserted fix commit was not applied")
	}
	if result.CommitMap[sha["b1"]] != inserted {
		t.Fatalf("dropped b1 maps to %s, want its predecessor %s", result.CommitMap[sha["b1"]], inserted)
	}
	if len(result.Dropped) != 1 || result.Dropped[0] != sha["b1"] {
		t.Fatalf("dropped = %v, want [%s]", result.Dropped, sha["b1"])
	}
	newT2 := result.CommitMap[sha["t2"]]
	if newT2 == "" || newT2 == sha["t2"] {
		t.Fatalf("t2 remap = %q, want the cut point above the dropped commit to resolve", newT2)
	}
	if got := cutPointSHA(result, "t2"); got != newT2 {
		t.Fatalf("cut point t2 = %s, want %s", got, newT2)
	}
	wantSubjects := []string{"layer 2 tip", "fix commit", "layer 1 tip", "shared base"}
	if got := subjectsBetween(t, repo, sha["base"], result.HeadSHA); !equalStrings(got, wantSubjects) {
		t.Fatalf("chain subjects = %v, want %v", got, wantSubjects)
	}
	assertResolverCleanup(t, repo, mainBefore, fixSHA)
}

// TestRestackResolverExhausted proves an exhausted resolver surfaces the
// conflict error carrying the attempt count and failure detail, with the
// pick aborted and the worktree removed.
func TestRestackResolverExhausted(t *testing.T) {
	repo, sha, fixSHA, cutPoints := newResolverConflictRepo(t)
	mainBefore := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")

	resolver := func(input gitpkg.RestackResolverInput) (gitpkg.RestackResolverResult, error) {
		return gitpkg.RestackResolverResult{
			Resolution:  gitpkg.RestackResolutionExhausted,
			Attempts:    3,
			LastFailure: "markers remain in shared.txt",
			AttemptDir:  "/tmp/attempts/attempt-03",
		}, nil
	}
	_, err := gitpkg.RestackChainWithResolver(repo, cutPoints, resolverConflictOps(fixSHA), resolver, "/tmp/attempts")
	var conflict *gitpkg.RestackConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("RestackChainWithResolver() error = %v, want *RestackConflictError", err)
	}
	if conflict.CommitSHA != sha["b1"] {
		t.Fatalf("conflict commit = %s, want %s", conflict.CommitSHA, sha["b1"])
	}
	if conflict.Attempts != 3 {
		t.Fatalf("conflict attempts = %d, want 3", conflict.Attempts)
	}
	if conflict.LastFailure != "markers remain in shared.txt" {
		t.Fatalf("conflict last failure = %q", conflict.LastFailure)
	}
	if conflict.AttemptDir != "/tmp/attempts/attempt-03" {
		t.Fatalf("conflict attempt dir = %q", conflict.AttemptDir)
	}
	if !strings.Contains(conflict.Error(), "3 resolution attempts") {
		t.Fatalf("conflict error %q does not carry the attempt count", conflict.Error())
	}
	assertResolverCleanup(t, repo, mainBefore, fixSHA)
}

// TestRestackResolverErrorPropagates proves a resolver error aborts the pick
// and propagates with the same cleanup, never surfacing as a conflict.
func TestRestackResolverErrorPropagates(t *testing.T) {
	repo, _, fixSHA, cutPoints := newResolverConflictRepo(t)
	mainBefore := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")

	sentinel := errors.New("session stopped")
	resolver := func(input gitpkg.RestackResolverInput) (gitpkg.RestackResolverResult, error) {
		return gitpkg.RestackResolverResult{}, sentinel
	}
	_, err := gitpkg.RestackChainWithResolver(repo, cutPoints, resolverConflictOps(fixSHA), resolver, "/tmp/attempts")
	if !errors.Is(err, sentinel) {
		t.Fatalf("RestackChainWithResolver() error = %v, want the resolver error propagated", err)
	}
	var conflict *gitpkg.RestackConflictError
	if errors.As(err, &conflict) {
		t.Fatal("a resolver error must not surface as a restack conflict")
	}
	assertResolverCleanup(t, repo, mainBefore, fixSHA)
}

// newPausedPickRepo builds a repository with a cherry-pick paused on a
// conflict over shared.txt, the state a conflict-resolution session works
// in. It returns the repository and its clean pre-conflict content.
func newPausedPickRepo(t *testing.T) string {
	t.Helper()
	repo := testutil.InitGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("line1\n"), 0o644); err != nil {
		t.Fatalf("writing shared.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("other base\n"), 0o644); err != nil {
		t.Fatalf("writing other.txt: %v", err)
	}
	runGitRefTest(t, repo, "add", ".")
	runGitRefTest(t, repo, "commit", "-m", "base")

	runGitRefTest(t, repo, "checkout", "-b", "side")
	testutil.CommitFile(t, repo, "shared.txt", "line1 side\n", "side change")
	runGitRefTest(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "shared.txt", "line1 main\n", "main change")

	cmd := exec.Command("git", "-C", repo, "cherry-pick", "side")
	cmd.Env = testutil.GitTestEnv()
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("cherry-pick unexpectedly succeeded:\n%s", out)
	}
	return repo
}

// TestConflictMarkerFilesInPaths proves the path-scoped marker scan reports
// only paths holding all three marker forms, skips decorative separators and
// missing files, and ignores paths outside the set.
func TestConflictMarkerFilesInPaths(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	markerStart := strings.Join([]string{"<<<<<<<", "HEAD"}, " ")
	markerMid := strings.Repeat("=", 7)
	markerEnd := strings.Join([]string{">>>>>>>", "side"}, " ")
	marked := strings.Join([]string{markerStart, "line1", markerMid, "line1", markerEnd}, "\n") + "\n"
	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	writeFile("conflicted.txt", marked)
	writeFile("partial.txt", strings.Join([]string{markerStart, "line1"}, "\n")+"\n")
	writeFile("divider.txt", "text\n"+markerMid+"\ntext\n")
	writeFile("clean.txt", "clean\n")
	paths := []string{"conflicted.txt", "partial.txt", "divider.txt", "clean.txt", "missing.txt"}

	got, err := gitpkg.ConflictMarkerFilesInPaths(repo, paths)
	if err != nil {
		t.Fatalf("ConflictMarkerFilesInPaths() error = %v", err)
	}
	if len(got) != 1 || got[0] != "conflicted.txt" {
		t.Fatalf("ConflictMarkerFilesInPaths() = %v, want [conflicted.txt]", got)
	}
}

// TestChangedPathsOutsideSetAndRevert proves, against a paused conflicted
// pick, that the outside-set listing names tracked modifications, staged
// additions, and untracked files (including inside new directories and a
// symlink), that the allowed set is excluded, and that reverting restores
// tracked files and removes new paths and their now-empty directories.
func TestChangedPathsOutsideSetAndRevert(t *testing.T) {
	repo := newPausedPickRepo(t)

	// The allowed (conflicted) file is edited; everything else is the
	// session's out-of-scope footprint.
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("resolved by session\n"), 0o644); err != nil {
		t.Fatalf("editing shared.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("other touched\n"), 0o644); err != nil {
		t.Fatalf("editing other.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "extra", "nested"), 0o755); err != nil {
		t.Fatalf("creating extra/nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "extra", "note.txt"), []byte("note\n"), 0o644); err != nil {
		t.Fatalf("writing extra/note.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "extra", "nested", "deep.txt"), []byte("deep\n"), 0o644); err != nil {
		t.Fatalf("writing extra/nested/deep.txt: %v", err)
	}
	if err := os.Symlink("other.txt", filepath.Join(repo, "link.txt")); err != nil {
		t.Fatalf("creating link.txt: %v", err)
	}
	// One of the new files is staged, as a permissive session might leave.
	runGitRefTest(t, repo, "add", "extra/note.txt")

	outside, err := gitpkg.ChangedPathsOutsideSet(repo, []string{"shared.txt"})
	if err != nil {
		t.Fatalf("ChangedPathsOutsideSet() error = %v", err)
	}
	want := []string{"extra/nested/deep.txt", "extra/note.txt", "link.txt", "other.txt"}
	if !equalStrings(outside, want) {
		t.Fatalf("ChangedPathsOutsideSet() = %v, want %v", outside, want)
	}

	if err := gitpkg.RevertPaths(repo, outside); err != nil {
		t.Fatalf("RevertPaths() error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(repo, "other.txt")); err != nil || string(got) != "other base\n" {
		t.Fatalf("other.txt = %q, %v; want the restored base content", got, err)
	}
	if _, err := os.Lstat(filepath.Join(repo, "link.txt")); !os.IsNotExist(err) {
		t.Fatal("link.txt symlink still exists after the revert")
	}
	if _, err := os.Lstat(filepath.Join(repo, "extra")); !os.IsNotExist(err) {
		t.Fatal("the extra directory tree still exists after the revert")
	}
	// The staged addition left the index; the conflicted file's edit is the
	// only remaining change and it is still unstaged working-tree content.
	if got := runGitRefTest(t, repo, "status", "--porcelain", "--untracked-files=all"); got != "UU shared.txt" {
		t.Fatalf("status after revert = %q, want only the unresolved shared.txt", got)
	}
}

// TestRevertPathsRejectsEscapes proves the revert helper refuses paths that
// leave the worktree.
func TestRevertPathsRejectsEscapes(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	if err := gitpkg.RevertPaths(repo, []string{"../outside.txt"}); err == nil {
		t.Fatal("RevertPaths() with a parent-relative path = nil error, want a refusal")
	}
	if err := gitpkg.RevertPaths(repo, []string{"/etc/passwd"}); err == nil {
		t.Fatal("RevertPaths() with an absolute path = nil error, want a refusal")
	}
}

// TestDiffPathsBetween proves the prompt-facing diff renders only the
// requested path set between two commits.
func TestDiffPathsBetween(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	base := runGitRefTest(t, repo, "rev-parse", "HEAD")
	testutil.CommitFile(t, repo, "a.txt", "a new\n", "a commit")
	testutil.CommitFile(t, repo, "b.txt", "b new\n", "b commit")
	tip := runGitRefTest(t, repo, "rev-parse", "HEAD")

	diff, err := gitpkg.DiffPathsBetween(repo, base, tip, []string{"b.txt"})
	if err != nil {
		t.Fatalf("DiffPathsBetween() error = %v", err)
	}
	if !strings.Contains(diff, "b.txt") || strings.Contains(diff, "a.txt") {
		t.Fatalf("DiffPathsBetween() = %q, want only b.txt's change", diff)
	}
	if diff == "" {
		t.Fatal("DiffPathsBetween() returned an empty diff for a changed path")
	}

	empty, err := gitpkg.DiffPathsBetween(repo, base, tip, nil)
	if err != nil || empty != "" {
		t.Fatalf("DiffPathsBetween() with no paths = %q, %v; want empty, nil", empty, err)
	}
}

// TestRestackChainWithResolverNilResolverKeepsAbort proves a nil resolver
// keeps RestackChain's abort-on-conflict behavior, surfacing a conflict with
// a zero attempt count.
func TestRestackChainWithResolverNilResolverKeepsAbort(t *testing.T) {
	repo, sha, fixSHA, cutPoints := newResolverConflictRepo(t)
	mainBefore := runGitRefTest(t, repo, "rev-parse", "refs/heads/main")

	_, err := gitpkg.RestackChainWithResolver(repo, cutPoints, resolverConflictOps(fixSHA), nil, "/tmp/attempts")
	var conflict *gitpkg.RestackConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("RestackChainWithResolver() error = %v, want *RestackConflictError", err)
	}
	if conflict.Attempts != 0 {
		t.Fatalf("conflict attempts = %d, want 0 for a nil resolver", conflict.Attempts)
	}
	if conflict.CommitSHA != sha["b1"] {
		t.Fatalf("conflict commit = %s, want %s", conflict.CommitSHA, sha["b1"])
	}
	assertResolverCleanup(t, repo, mainBefore, fixSHA)
}
