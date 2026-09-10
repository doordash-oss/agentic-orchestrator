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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestWorktreeMergeCompletion is the opt-in final gate for a completed
// provisioned merge. Fixture-based tests stay independent of this branch:
// the gate runs only when AGENTICO_VERIFY_MERGE=1 and takes the expected
// fork and target commits from AGENTICO_MERGE_FORK / AGENTICO_MERGE_TARGET.
// It is strictly read-only — every check inspects Git state without
// mutating the repository.
func TestWorktreeMergeCompletion(t *testing.T) {
	if os.Getenv("AGENTICO_VERIFY_MERGE") != "1" {
		t.Skip("set AGENTICO_VERIFY_MERGE=1 with AGENTICO_MERGE_FORK and AGENTICO_MERGE_TARGET to verify the provisioned merge")
	}
	fork := strings.TrimSpace(os.Getenv("AGENTICO_MERGE_FORK"))
	target := strings.TrimSpace(os.Getenv("AGENTICO_MERGE_TARGET"))
	if !isFullSHA(fork) || !isFullSHA(target) {
		t.Fatalf("incomplete inputs: AGENTICO_MERGE_FORK=%q AGENTICO_MERGE_TARGET=%q; want two full 40-hex commit ids", fork, target)
	}

	root, err := findRepositoryRoot(t)
	if err != nil {
		t.Fatalf("resolving provisioned worktree root: %v", err)
	}

	// The repository's tracked-file conflict detector: no files, no error.
	files, err := ConflictMarkerFiles(root)
	if err != nil {
		t.Fatalf("ConflictMarkerFiles() error = %v; want clean scan", err)
	}
	if len(files) != 0 {
		t.Fatalf("ConflictMarkerFiles() = %v; want no tracked files carrying conflict markers", files)
	}

	// Actual Git state: an empty unmerged index.
	if out := gitOut(t, root, "ls-files", "-u"); strings.TrimSpace(out) != "" {
		t.Fatalf("git ls-files -u = %q; want an empty unmerged index", out)
	}

	// Resolved Git metadata: no merge or rebase operation in progress.
	if MergeInProgress(root) {
		t.Fatal("MergeInProgress() = true; want the merge committed with MERGE_HEAD cleared")
	}
	if RebaseInProgress(root) {
		t.Fatal("RebaseInProgress() = true; want no rebase operation state in this worktree")
	}

	// Both specified commits are ancestors of HEAD.
	for _, sha := range []string{fork, target} {
		if err := gitRun(root, "merge-base", "--is-ancestor", sha, "HEAD"); err != nil {
			t.Fatalf("commit %s is not an ancestor of HEAD: %v", sha, err)
		}
	}

	// History retains a merge whose first parent is the fork and second
	// parent is the target, with exactly two parents. Later corrections may
	// descend from it, so the walk follows the first-parent chain.
	if !hasRetainedMerge(t, root, fork, target) {
		t.Fatalf("no commit on the first-parent chain of HEAD has parents exactly [%s %s]", fork, target)
	}
}

// findRepositoryRoot resolves the provisioned worktree containing this test
// binary by walking upward from the current directory to the enclosing Git
// worktree. A linked worktree carries a .git file; a plain checkout a .git
// directory.
func findRepositoryRoot(t *testing.T) (string, error) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, ".git")
		if _, err := os.Stat(candidate); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .git entry found above %s", dir)
		}
		dir = parent
	}
}

// hasRetainedMerge walks the first-parent chain of HEAD looking for the
// retained merge commit with exactly the expected two parents.
func hasRetainedMerge(t *testing.T, root, fork, target string) bool {
	t.Helper()
	out := gitOut(t, root, "rev-list", "--parents", "--first-parent", "HEAD")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		if fields[1] == fork && fields[2] == target {
			return true
		}
	}
	return false
}

// gitOut runs a read-only Git command in the worktree and returns stdout.
func gitOut(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = testutil.GitTestEnv()
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, exitErr.Stderr)
		}
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

// gitRun runs a read-only Git command and returns only its error so callers
// can interpret exit status (e.g. merge-base --is-ancestor).
func gitRun(root string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = testutil.GitTestEnv()
	return cmd.Run()
}

// isFullSHA reports whether s is a full 40-character lowercase hex commit id.
func isFullSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
