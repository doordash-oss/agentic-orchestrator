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

// MergeNoFF merges ref into the checked-out branch of the worktree at
// worktreePath with an explicit two-parent --no-ff merge commit, even when a
// fast-forward would be possible. Callers (child-to-parent integration) use
// this to create a durable merge boundary on an already checked-out parent
// branch — unlike MergeFeatureBranch, no checkout dance happens, so the
// parent worktree, branch, and HEAD are never moved except by the merge
// commit itself. ref may be a branch name or a full commit SHA; integration
// passes the recorded child head SHA so the merge applies exactly the
// durable anchor regardless of later child-branch movement.
//
// On failure any recorded in-progress merge (conflicts) is aborted so HEAD
// and the worktree return to the exact pre-merge state; a pre-apply refusal
// (dirty working tree blocking the merge) needs no abort because git never
// started one. Either way the branch ref is guaranteed unchanged when an
// error is returned.
func MergeNoFF(worktreePath, ref, message string) error {
	mergeCmd := exec.Command("git", "-C", worktreePath, "merge", "--no-ff", "-m", message, ref)
	out, err := mergeCmd.CombinedOutput()
	if err == nil {
		return nil
	}
	failure := strings.TrimSpace(string(out))
	return fmt.Errorf("merge failed (aborted: %v): %s: %w", abortMerge(worktreePath), failure, err)
}

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
// explicit --no-ff merge commit. Unlike MergeNoFF, on conflict the merge is
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

// MergeNoFF exposes the package-level merge on the manager so the
// orchestrator can reach it through a narrow structural interface.
func (m *WorktreeManager) MergeNoFF(worktreePath, ref, message string) error {
	return MergeNoFF(worktreePath, ref, message)
}
