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
)

// MergeCandidateResult holds the outcome of creating a merge candidate without
// advancing the parent ref.
type MergeCandidateResult struct {
	CandidateSHA  string
	ConflictFiles []string
}

// CreateMergeCandidate creates an explicit two-parent no-fast-forward merge
// commit in a temporary detached worktree at parentTip, merging childHead
// into it, and returns the resulting merge commit SHA. The parent ref and
// parent worktree are never touched: the merge runs in a disposable worktree
// that is removed after the candidate SHA is captured.
//
// The merge commit's first parent is parentTip and its second parent is
// childHead, matching the integration boundary contract. Even when a
// fast-forward would be possible, the --no-ff flag forces an explicit merge
// commit.
//
// On conflict, the temporary worktree is cleaned up and ConflictFiles is
// populated; the returned error is a *MergeCandidateConflictError.
func CreateMergeCandidate(mainRepo, parentTip, childHead, message string) (*MergeCandidateResult, error) {
	// Create a temporary worktree detached at the parent tip. This does not
	// touch the parent's checked-out branch or worktree.
	tmpDir, err := os.MkdirTemp("", "merge-candidate-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir for merge candidate: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpWorktree := filepath.Join(tmpDir, "wt")
	addCmd := exec.Command("git", "-C", mainRepo, "worktree", "add", "--detach", tmpWorktree, parentTip)
	if out, err := addCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("creating temp worktree at %s: %s: %w", parentTip, strings.TrimSpace(string(out)), err)
	}
	// Ensure the temp worktree is removed from the worktree registry.
	defer func() {
		_ = exec.Command("git", "-C", mainRepo, "worktree", "remove", "--force", tmpWorktree).Run()
	}()

	// Merge the child head with --no-ff to force an explicit merge commit.
	mergeCmd := exec.Command("git", "-C", tmpWorktree, "merge", "--no-ff", "-m", message, childHead)
	out, err := mergeCmd.CombinedOutput()
	if err != nil {
		// Check if this is a conflict (merge in progress).
		conflictFiles := extractConflictFiles(tmpWorktree)
		// Abort the merge to clean the temp worktree before removal.
		_ = exec.Command("git", "-C", tmpWorktree, "merge", "--abort").Run()
		if len(conflictFiles) > 0 {
			return &MergeCandidateResult{ConflictFiles: conflictFiles}, &MergeCandidateConflictError{
				ParentTip:     parentTip,
				ChildHead:     childHead,
				ConflictFiles: conflictFiles,
			}
		}
		return nil, fmt.Errorf("merge candidate failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Capture the resulting merge commit SHA.
	headCmd := exec.Command("git", "-C", tmpWorktree, "rev-parse", "HEAD")
	headOut, err := headCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("capturing merge candidate HEAD: %w", err)
	}
	candidateSHA := strings.TrimSpace(string(headOut))
	if candidateSHA == "" {
		return nil, fmt.Errorf("merge candidate HEAD is empty")
	}

	// Verify the merge commit has exactly two parents.
	parentsCmd := exec.Command("git", "-C", tmpWorktree, "rev-list", "--parents", "-n", "1", "HEAD")
	parentsOut, err := parentsCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("verifying merge parents: %w", err)
	}
	parentFields := strings.Fields(strings.TrimSpace(string(parentsOut)))
	if len(parentFields) != 3 {
		return nil, fmt.Errorf("merge candidate has %d parents, want 2: %s", len(parentFields)-1, strings.TrimSpace(string(parentsOut)))
	}

	return &MergeCandidateResult{CandidateSHA: candidateSHA}, nil
}

// MergeCandidateConflictError indicates the merge candidate creation failed
// due to a conflict. The parent ref and worktree are untouched.
type MergeCandidateConflictError struct {
	ParentTip     string
	ChildHead     string
	ConflictFiles []string
}

func (e *MergeCandidateConflictError) Error() string {
	return fmt.Sprintf("merge candidate conflict between %s and %s: %v", e.ParentTip, e.ChildHead, e.ConflictFiles)
}

// extractConflictFiles reads the conflict file list from a worktree with an
// in-progress merge.
func extractConflictFiles(worktreePath string) []string {
	cmd := exec.Command("git", "-C", worktreePath, "diff", "--name-only", "--diff-filter=U")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

// CreateMergeCandidate exposes the package-level function on the manager.
func (m *WorktreeManager) CreateMergeCandidate(mainRepo, parentTip, childHead, message string) (*MergeCandidateResult, error) {
	return CreateMergeCandidate(mainRepo, parentTip, childHead, message)
}
