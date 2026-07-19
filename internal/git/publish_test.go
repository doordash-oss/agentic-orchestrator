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

func TestCreatePR_DraftFlag_IncludesArg(t *testing.T) {
	args := buildCreatePRArgs("feature/x", "title", "body", true)
	for _, a := range args {
		if a == "--draft" {
			return
		}
	}
	t.Errorf("expected --draft in args %v", args)
}

func TestCreatePR_NoDraftFlag_ExcludesArg(t *testing.T) {
	args := buildCreatePRArgs("feature/x", "title", "body", false)
	for _, a := range args {
		if a == "--draft" {
			t.Errorf("unexpected --draft in args %v", args)
		}
	}
}

func TestCreatePR_DraftFlag_WithBaseBranch(t *testing.T) {
	args := buildCreatePRArgs("feature/x", "title", "body", true, "main")
	hasDraft, hasBase := false, false
	for _, a := range args {
		if a == "--draft" {
			hasDraft = true
		}
		if a == "--base" {
			hasBase = true
		}
	}
	if !hasDraft {
		t.Errorf("expected --draft in args %v", args)
	}
	if !hasBase {
		t.Errorf("expected --base in args %v", args)
	}
}

func TestDiffSummary_MixedChanges(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	testutil.CreateBranch(t, repo, "feature/test")
	testutil.CommitFile(t, repo, "new.go", "package main\n", "add new file")

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diff, err := DiffSummary(repo, "main")
	if err != nil {
		t.Fatalf("DiffSummary: %v", err)
	}
	checks := []string{"new.go", "README.md", "untracked.txt", "+hello"}
	for _, want := range checks {
		if !strings.Contains(diff, want) {
			t.Errorf("DiffSummary() missing %q in:\n%s", want, diff)
		}
	}
}

func TestDiffSummary_NoChanges(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	testutil.CreateBranch(t, repo, "feature/test")

	_, err := DiffSummary(repo, "main")
	if err == nil {
		t.Fatal("expected error for no changes")
	}
	if !strings.Contains(err.Error(), "no changes") {
		t.Errorf("expected 'no changes' error, got: %v", err)
	}
}

func TestBranchDiffPreviews_UpdateAndAdd(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	testutil.CreateBranch(t, repo, "feature/test")

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	previews, err := BranchDiffPreviews(repo, "main")
	if err != nil {
		t.Fatalf("BranchDiffPreviews: %v", err)
	}
	if len(previews) != 2 {
		t.Fatalf("len(previews) = %d, want 2", len(previews))
	}
	if previews[0].Path != "README.md" || previews[0].Operation != "update" {
		t.Errorf("preview[0] = %+v, want README.md update", previews[0])
	}
	if previews[1].Path != "new.txt" || previews[1].Operation != "add" {
		t.Errorf("preview[1] = %+v, want new.txt add", previews[1])
	}
	if previews[1].AddedLines != 2 {
		t.Errorf("new.txt AddedLines = %d, want 2", previews[1].AddedLines)
	}
	if !strings.Contains(previews[1].Patch, "+hello") {
		t.Errorf("new.txt patch = %q, want added content", previews[1].Patch)
	}
}

func TestBranchDiffPreviews_CommittedFeatureChange(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	testutil.CreateBranch(t, repo, "feature/test")
	testutil.CommitFile(t, repo, "README.md", "# Feature change\n", "feature change on feature branch")

	previews, err := BranchDiffPreviews(repo, "main")
	if err != nil {
		t.Fatalf("BranchDiffPreviews: %v", err)
	}
	if len(previews) != 1 {
		t.Fatalf("len(previews) = %d, want 1 (committed feature change vs main)", len(previews))
	}
	if previews[0].Path != "README.md" {
		t.Fatalf("preview[0].Path = %q, want README.md", previews[0].Path)
	}
	if previews[0].Operation != "update" {
		t.Errorf("preview[0].Operation = %q, want update (modified vs main)", previews[0].Operation)
	}
	if previews[0].AddedLines == 0 {
		t.Errorf("README.md AddedLines = %d, want > 0", previews[0].AddedLines)
	}
}

func TestBranchDiffPreviews_DeleteAndRename(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping rename/delete diff preview extended regression in short mode")
	}
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	testutil.CommitFile(t, repo, "delete.txt", "gone\n", "add delete target on main")
	testutil.CommitFile(t, repo, "rename.txt", "same\n", "add rename target on main")
	testutil.CreateBranch(t, repo, "feature/test")

	if err := os.Remove(filepath.Join(repo, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	mv := exec.Command("git", "-C", repo, "mv", "rename.txt", "renamed.txt")
	if out, err := mv.CombinedOutput(); err != nil {
		t.Fatalf("git mv: %s: %v", string(out), err)
	}
	runGit(t, repo, "add", "delete.txt", "renamed.txt")

	previews, err := BranchDiffPreviews(repo, "main")
	if err != nil {
		t.Fatalf("BranchDiffPreviews: %v", err)
	}

	var sawDelete, sawRename bool
	for _, preview := range previews {
		switch preview.Path {
		case "delete.txt":
			sawDelete = preview.Operation == "delete"
		case "renamed.txt":
			sawRename = preview.Operation == "rename" && preview.OldPath == "rename.txt"
		}
	}
	if !sawDelete {
		t.Fatalf("expected delete preview, got %+v", previews)
	}
	if !sawRename {
		t.Fatalf("expected rename preview, got %+v", previews)
	}
}

func TestSingleFileDiffPreviewVariants(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping repeated single-file diff preview variants in short mode")
	}

	tests := []struct {
		name      string
		path      string
		setup     func(t *testing.T, repo string)
		wantNil   bool
		wantPatch string
	}{
		{
			name: "update",
			path: "README.md",
			setup: func(t *testing.T, repo string) {
				t.Helper()
				testutil.CreateBranch(t, repo, "feature/test")
				if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Changed\nNew line\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantPatch: "+# Changed",
		},
		{
			name: "new file",
			path: "new.txt",
			setup: func(t *testing.T, repo string) {
				t.Helper()
				testutil.CreateBranch(t, repo, "feature/test")
				if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantPatch: "+hello",
		},
		{
			name:    "no changes",
			path:    "README.md",
			setup:   func(t *testing.T, repo string) {},
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo := testutil.InitGitRepo(t)
			tt.setup(t, repo)

			preview, err := SingleFileDiffPreview(repo, "main", tt.path)
			if err != nil {
				t.Fatalf("SingleFileDiffPreview(%q) error = %v", tt.path, err)
			}
			if tt.wantNil {
				if preview != nil {
					t.Fatalf("SingleFileDiffPreview(%q) = %+v, want nil", tt.path, preview)
				}
				return
			}
			if preview == nil {
				t.Fatalf("SingleFileDiffPreview(%q) = nil, want preview", tt.path)
			}
			if preview.Path != tt.path {
				t.Errorf("SingleFileDiffPreview(%q).Path = %q, want %q", tt.path, preview.Path, tt.path)
			}
			if preview.AddedLines == 0 {
				t.Errorf("SingleFileDiffPreview(%q).AddedLines = %d, want > 0", tt.path, preview.AddedLines)
			}
			if !strings.Contains(preview.Patch, tt.wantPatch) {
				t.Errorf("SingleFileDiffPreview(%q).Patch = %q, want %q", tt.path, preview.Patch, tt.wantPatch)
			}
		})
	}
}

func TestCommitAll(t *testing.T) {
	if testing.Short() {
		t.Skip("covered by TestFastPublishCommitRepresentative in short mode")
	}
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	files := map[string]string{
		"file.txt":       "content\n",
		"phase_complete": "not scrubbed here\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	message := "test commit"
	if err := CommitAll(repo, message); err != nil {
		t.Fatalf("CommitAll() error = %v", err)
	}

	log, err := CommitLog(repo, "HEAD~1")
	if err != nil {
		t.Fatalf("CommitLog() error = %v", err)
	}
	if !strings.Contains(log, message) {
		t.Errorf("CommitLog() = %q, want message %q", log, message)
	}

	fullMsg := exec.Command("git", "-C", repo, "log", "-1", "--format=%B")
	out, err := fullMsg.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), CommitSignatureTrailer) {
		t.Errorf("git log -1 --format=%%B = %q, want signature trailer", out)
	}

	out, err = exec.Command("git", "-C", repo, "show", "--name-only", "--format=", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git show: %v\n%s", err, out)
	}
	for name := range files {
		if !strings.Contains(string(out), name) {
			t.Errorf("CommitAll() did not stage %s:\n%s", name, out)
		}
	}

	if HasUncommittedChanges(repo) {
		t.Error("HasUncommittedChanges() = true, want false after CommitAll")
	}
}

func TestCommitAllAndGetHead_CommitsChangesAndReturnsFullSHA(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sha, err := CommitAllAndGetHead(repo, "test commit with sha")
	if err != nil {
		t.Fatalf("CommitAllAndGetHead: %v", err)
	}
	if len(sha) != 40 {
		t.Fatalf("sha length = %d, want full 40-char SHA: %q", len(sha), sha)
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != sha {
		t.Errorf("returned SHA = %q, HEAD = %q", sha, got)
	}
	if HasUncommittedChanges(repo) {
		t.Error("expected clean working tree after CommitAllAndGetHead")
	}
}

func TestCommitAllAndGetHead_NoChangesReturnsExistingHead(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	before, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse before: %v\n%s", err, before)
	}

	sha, err := CommitAllAndGetHead(repo, "no changes")
	if err != nil {
		t.Fatalf("CommitAllAndGetHead with clean tree: %v", err)
	}
	if sha != strings.TrimSpace(string(before)) {
		t.Errorf("sha = %q, want existing HEAD %q", sha, strings.TrimSpace(string(before)))
	}
}

func TestCommitLog(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	testutil.CreateBranch(t, repo, "feature/test")
	testutil.CommitFile(t, repo, "a.txt", "a\n", "commit A")
	testutil.CommitFile(t, repo, "b.txt", "b\n", "commit B")

	log, err := CommitLog(repo, "main")
	if err != nil {
		t.Fatalf("CommitLog: %v", err)
	}
	if !strings.Contains(log, "commit A") {
		t.Error("expected 'commit A' in log")
	}
	if !strings.Contains(log, "commit B") {
		t.Error("expected 'commit B' in log")
	}
}

func TestHasUncommittedChanges(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	if HasUncommittedChanges(repo) {
		t.Error("HasUncommittedChanges() = true, want false for clean repo")
	}
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasUncommittedChanges(repo) {
		t.Error("HasUncommittedChanges() = false, want true for dirty repo")
	}
}

func TestHasLocalCommits(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	if HasLocalCommits(repo) {
		t.Error("HasLocalCommits() = true, want false when no upstream is configured")
	}
	testutil.InitBareRemote(t, repo)
	if HasLocalCommits(repo) {
		t.Error("HasLocalCommits() = true, want false when branch is up to date with upstream")
	}
	testutil.CommitFile(t, repo, "local.txt", "local\n", "local only commit")
	if !HasLocalCommits(repo) {
		t.Error("HasLocalCommits() = false, want true when local branch is ahead of upstream")
	}
}

func TestExtractExistingPRURL(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "standard already exists message",
			output: `a pull request for branch "feature/foo" into branch "master" already exists: https://github.com/org/repo/pull/116`,
			want:   "https://github.com/org/repo/pull/116",
		},
		{
			name:   "multiline output",
			output: "some preamble\na pull request for branch \"feature/bar\" into branch \"main\" already exists:\nhttps://github.com/org/repo/pull/42\n",
			want:   "https://github.com/org/repo/pull/42",
		},
		{
			name:   "no match - different error",
			output: "could not connect to GitHub",
			want:   "",
		},
		{
			name:   "already exists but no URL",
			output: `a pull request already exists but no link`,
			want:   "",
		},
		{
			name:   "already exists with non-PR URL",
			output: `something already exists: https://github.com/org/repo/issues/5`,
			want:   "",
		},
		{
			name:   "empty output",
			output: "",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractExistingPRURL(tt.output)
			if got != tt.want {
				t.Errorf("extractExistingPRURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
