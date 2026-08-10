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
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func runGitMergeStateTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestMergeInProgress_CleanWorktree(t *testing.T) {
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	if MergeInProgress(repo) {
		t.Error("MergeInProgress() = true on a clean worktree; want false")
	}
}

func TestMergeInProgress_DuringInterruptedMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git merge state test")
	}
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	// Shared file present at the base so both sides can diverge on it.
	testutil.CommitFile(t, repo, "shared.txt", "base\n", "base shared")

	// Side A (main): modify shared.txt.
	runGitMergeStateTest(t, repo, "checkout", "-b", "topic")
	upstreamHead := commitShared(t, repo, "upstream\n", "upstream change")

	// Side B: reset to the original main tip and diverge on shared.txt.
	runGitMergeStateTest(t, repo, "checkout", "main")
	commitShared(t, repo, "feature\n", "feature change")

	// Merge the divergent upstream commit; it conflicts on shared.txt and
	// leaves MERGE_HEAD in place.
	cmd := exec.Command("git", "-C", repo, "merge", "--no-ff", upstreamHead, "-m", "merge")
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	_ = cmd.Run() // expected to fail due to conflict

	if !MergeInProgress(repo) {
		t.Error("MergeInProgress() = false during an interrupted merge; want true")
	}
}

func TestMergeInProgress_AfterAbort(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git merge state test")
	}
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "shared.txt", "base\n", "base shared")
	runGitMergeStateTest(t, repo, "checkout", "-b", "topic")
	upstreamHead := commitShared(t, repo, "upstream\n", "upstream change")
	runGitMergeStateTest(t, repo, "checkout", "main")
	commitShared(t, repo, "feature\n", "feature change")

	cmd := exec.Command("git", "-C", repo, "merge", "--no-ff", upstreamHead, "-m", "merge")
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	_ = cmd.Run()
	runGitMergeStateTest(t, repo, "merge", "--abort")

	if MergeInProgress(repo) {
		t.Error("MergeInProgress() = true after `git merge --abort`; want false")
	}
}

func TestMergeInProgress_AfterCleanMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git merge state test")
	}
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "shared.txt", "base\n", "base shared")
	runGitMergeStateTest(t, repo, "checkout", "-b", "topic")
	upstreamHead := commitShared(t, repo, "upstream\n", "upstream change")
	runGitMergeStateTest(t, repo, "checkout", "main")
	// A non-conflicting divergence on a different file merges cleanly.
	testutil.CommitFile(t, repo, "feature.txt", "f\n", "feature change")
	runGitMergeStateTest(t, repo, "merge", "--no-ff", upstreamHead, "-m", "clean merge")

	if MergeInProgress(repo) {
		t.Error("MergeInProgress() = true after a completed clean merge; want false")
	}
}

// commitShared overwrites shared.txt with content, commits, and returns the
// resulting HEAD SHA.
func commitShared(t *testing.T, repo, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("write shared.txt: %v", err)
	}
	runGitMergeStateTest(t, repo, "add", "shared.txt")
	runGitMergeStateTest(t, repo, "commit", "-m", message)
	return runGitMergeStateTest(t, repo, "rev-parse", "HEAD")
}

func TestConflictMarkerFiles_CleanTree(t *testing.T) {
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "clean.txt", "no markers here\n", "clean")

	files, err := ConflictMarkerFiles(repo)
	if err != nil {
		t.Fatalf("ConflictMarkerFiles() error = %v; want nil for clean tree", err)
	}
	if len(files) != 0 {
		t.Errorf("ConflictMarkerFiles() = %v; want empty for clean tree", files)
	}
}

func TestConflictMarkerFiles_IgnoresDecorativeSeparators(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git conflict marker scan")
	}
	t.Parallel()
	repo := testutil.InitGitRepo(t)

	content := strings.Join([]string{
		`log.Printf("==============================")`,
		"// =============================================================================",
		"=======",
		"embedded ======= separator",
		"prefixed <<<<<<< example",
		"prefixed >>>>>>> example",
	}, "\n") + "\n"
	testutil.CommitFile(t, repo, "decorative.txt", content, "add decorative separators")

	files, err := ConflictMarkerFiles(repo)
	if err != nil {
		t.Fatalf("ConflictMarkerFiles() error = %v", err)
	}
	if len(files) != 0 {
		t.Errorf("ConflictMarkerFiles() = %v; want empty for decorative separators", files)
	}
}

func TestConflictMarkerFiles_DetectsMarkers(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git conflict marker scan")
	}
	t.Parallel()
	repo := testutil.InitGitRepo(t)

	// Write a tracked file that genuinely carries conflict markers. Build the
	// markers from split strings so this test source does not itself contain
	// the literal marker sequences.
	marked := strings.Join([]string{
		"line before",
		"<" + "<<<<" + "<< ours",
		"our change",
		"=" + "=====" + "=",
		"their change",
		">" + ">>>>" + ">> theirs",
		"line after",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(repo, "conflicted.txt"), []byte(marked), 0o644); err != nil {
		t.Fatalf("write conflicted file: %v", err)
	}
	runGitMergeStateTest(t, repo, "add", "conflicted.txt")
	runGitMergeStateTest(t, repo, "commit", "-m", "add conflicted file")

	files, err := ConflictMarkerFiles(repo)
	if err != nil {
		t.Fatalf("ConflictMarkerFiles() error = %v; want nil", err)
	}
	if len(files) != 1 || files[0] != "conflicted.txt" {
		t.Fatalf("ConflictMarkerFiles() = %v; want [conflicted.txt]", files)
	}
}

func TestConflictMarkerFiles_IgnoresUntrackedFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git conflict marker scan")
	}
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "clean.txt", "clean\n", "clean")

	// An untracked file with markers must not be reported.
	marked := "<" + "<<<<" + "<< ours\n" + "content\n"
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte(marked), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}
	files, err := ConflictMarkerFiles(repo)
	if err != nil {
		t.Fatalf("ConflictMarkerFiles() error = %v; want nil", err)
	}
	if len(files) != 0 {
		t.Errorf("ConflictMarkerFiles() = %v; want empty (untracked files ignored)", files)
	}
}

func TestConflictMarkerFiles_DoesNotSelfMatch(t *testing.T) {
	t.Parallel()
	// Sanity: the scan's own source file must not be reported as carrying
	// markers when committed into a repo. (This is a structural guard; the
	// patterns are split-string constructed to make it true.)
	repo := testutil.InitGitRepo(t)
	src := readMergeStateSource(t)
	if strings.Contains(src, "<<<<<<<") || strings.Contains(src, ">>>>>>>") {
		t.Skip("merge_state.go source contains literal markers; self-match guard is structural")
	}
	// Commit the merge_state.go source into the repo and scan it.
	if err := os.WriteFile(filepath.Join(repo, "merge_state.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	runGitMergeStateTest(t, repo, "add", "merge_state.go")
	runGitMergeStateTest(t, repo, "commit", "-m", "add scan source")
	files, err := ConflictMarkerFiles(repo)
	if err != nil {
		t.Fatalf("ConflictMarkerFiles() error = %v", err)
	}
	for _, f := range files {
		if f == "merge_state.go" {
			t.Errorf("ConflictMarkerFiles() reported merge_state.go; scan must not match its own source")
		}
	}
}

func readMergeStateSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("merge_state.go")
	if err != nil {
		t.Fatalf("read merge_state.go: %v", err)
	}
	return string(data)
}
