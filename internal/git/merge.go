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
	"os/exec"
	"strings"
)

// MergeIntoOutcome categorises the result of a MergeInto operation.
type MergeIntoOutcome int

const (
	// MergeIntoSuccess means the merge completed (or ref was already merged).
	MergeIntoSuccess MergeIntoOutcome = iota
	// MergeIntoConflict means conflicts remain in the worktree.
	MergeIntoConflict
	// MergeIntoFailed means a non-conflict failure occurred and the merge was aborted.
	MergeIntoFailed
)

// MergeIntoResult is the outcome, conflict files, and error from MergeInto.
type MergeIntoResult struct {
	Outcome       MergeIntoOutcome
	ConflictFiles []string
	Err           error
}

// MergeInto merges ref into the branch checked out in the worktree with an
// explicit --no-ff merge commit. On conflict the merge is
// NOT aborted — MERGE_HEAD and conflict markers are left in place so an agent
// can resolve them and commit. Re-entry while a merge is already in progress
// returns Conflict again; a ref already reachable from HEAD returns Success
// without a new commit.
func MergeInto(worktreePath, ref, message string) MergeIntoResult {
	if MergeInProgress(worktreePath) {
		return MergeIntoResult{
			Outcome:       MergeIntoConflict,
			ConflictFiles: listConflictFiles(worktreePath),
			Err:           fmt.Errorf("merge already in progress in %s", worktreePath),
		}
	}
	if IsAncestor(worktreePath, ref, "HEAD") {
		return MergeIntoResult{Outcome: MergeIntoSuccess}
	}

	args := append([]string{"-C", worktreePath}, identityFallbackArgs(worktreePath)...)
	args = append(args, "merge", "--no-ff", "-m", message, ref)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err == nil {
		return MergeIntoResult{Outcome: MergeIntoSuccess}
	}

	if MergeInProgress(worktreePath) {
		return MergeIntoResult{
			Outcome:       MergeIntoConflict,
			ConflictFiles: listConflictFiles(worktreePath),
			Err:           fmt.Errorf("merge conflicts: %s", strings.TrimSpace(string(out))),
		}
	}

	_ = abortMerge(worktreePath)
	return MergeIntoResult{
		Outcome: MergeIntoFailed,
		Err:     fmt.Errorf("merge failed: %s: %w", strings.TrimSpace(string(out)), err),
	}
}

// abortMerge attempts `git merge --abort`; nil means the worktree is back at
// its pre-merge state, an error means there was no merge in progress (which
// equally means no ref movement) or the abort itself failed.
func abortMerge(worktreePath string) error {
	cmd := exec.Command("git", "-C", worktreePath, "merge", "--abort")
	return cmd.Run()
}

// CurrentBranch returns the branch checked out in the given worktree (or ""
// when it cannot be determined), exposed on the manager so the orchestrator
// can reach it through a narrow structural interface.
func (m *WorktreeManager) CurrentBranch(worktreePath string) string {
	return CurrentBranch(worktreePath)
}
