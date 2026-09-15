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

func TestFastPublishCommitRepresentative(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	testutil.CreateBranch(t, repo, "feature/test")

	for path, content := range map[string]string{
		"README.md":      "# Changed\n",
		"new.txt":        "hello\nworld\n",
		"phase_complete": "not scrubbed here\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	diff, err := DiffSummary(repo, "main")
	if err != nil {
		t.Fatalf("DiffSummary() error = %v", err)
	}
	for _, want := range []string{"README.md", "new.txt", "phase_complete", "+hello"} {
		if !strings.Contains(diff, want) {
			t.Errorf("DiffSummary() missing %q in:\n%s", want, diff)
		}
	}

	previews, err := BranchDiffPreviews(repo, "main")
	if err != nil {
		t.Fatalf("BranchDiffPreviews() error = %v", err)
	}
	if len(previews) < 3 {
		t.Fatalf("len(previews) = %d, want at least 3", len(previews))
	}

	message := "fast representative commit"
	if err := CommitAll(repo, message); err != nil {
		t.Fatalf("CommitAll() error = %v", err)
	}

	out, err := exec.Command("git", "-C", repo, "log", "-1", "--format=%B").CombinedOutput()
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
	for _, want := range []string{"README.md", "new.txt", "phase_complete"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("git show --name-only missing %s:\n%s", want, out)
		}
	}
	if HasUncommittedChanges(repo) {
		t.Error("HasUncommittedChanges() = true, want false after CommitAll")
	}
}

func TestFastWorktreeRepresentative(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	mgr := NewWorktreeManager(t.TempDir())

	baseBranch := DefaultBranch(repo)
	baseCommit := gitOutput(t, repo, "rev-parse", baseBranch)
	fastBranch := LayerBranchName("fast-worktree", 1, "fast-worktree")
	wtPath, err := mgr.Create(repo, "fast-worktree", fastBranch, "repo", "")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if branch := CurrentBranch(wtPath); branch != fastBranch {
		t.Errorf("CurrentBranch() = %q, want %s", branch, fastBranch)
	}

	testutil.CommitFile(t, wtPath, "file.txt", "data\n", "worktree commit")
	if err := mgr.ResetToBaseLocal(wtPath, baseBranch); err != nil {
		t.Fatalf("ResetToBaseLocal() error = %v", err)
	}
	if headCommit := gitOutput(t, wtPath, "rev-parse", "HEAD"); headCommit != baseCommit {
		t.Errorf("HEAD = %s, want base %s", headCommit, baseCommit)
	}
	if err := mgr.Remove(wtPath, true); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
}
