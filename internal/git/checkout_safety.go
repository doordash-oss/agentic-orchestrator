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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// checkoutSafetyOutputBound bounds the captured stdout of checkout-safety
// inspections (status, diff, ls-files). It exceeds the diagnostic limit
// because these outputs are parsed, not displayed. One extra byte is
// captured beyond the bound so that a buffer filled exactly at a NUL record
// boundary is distinguishable from a genuinely complete result; output
// reaching bound+1 is proven overflow and fails closed instead of silently
// truncating a path list and missing a collision.
const checkoutSafetyOutputBound = 1 << 20

// checkoutOperationStateFiles are the per-worktree state markers whose
// presence proves a merge, cherry-pick, revert, or rebase — including a
// multi-commit sequence between commits — is in progress. They are resolved
// under the worktree's own git directory, never the shared common directory,
// because operation state is per-worktree.
var checkoutOperationStateNames = []string{
	"MERGE_HEAD",
	"CHERRY_PICK_HEAD",
	"REVERT_HEAD",
	"rebase-merge",
	"rebase-apply",
	"sequencer",
}

// checkoutOperationInProgress reports whether a Git operation is in progress
// in the worktree at repoPath. Operation state is resolved for the actual
// worktree's git directory; an unresolvable git directory or an unreadable
// state path is an inspection failure, never evidence that no operation is
// running.
func checkoutOperationInProgress(ctx context.Context, repoPath string, options OriginCheckOptions) (bool, error) {
	result := runOriginCommand(ctx, repoPath, []string{"rev-parse", "--git-dir"}, options.commandTimeout(), options)
	if result.ExitCode != 0 {
		return false, fmt.Errorf("resolving the worktree git directory: %s", nonemptyBranchProbeDiagnostic(result.Diagnostics))
	}
	gitDir := strings.TrimSpace(result.Stdout)
	if gitDir == "" {
		return false, fmt.Errorf("resolving the worktree git directory: empty result")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoPath, gitDir)
	}
	for _, name := range checkoutOperationStateNames {
		if _, err := os.Stat(filepath.Join(gitDir, name)); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("inspecting git operation state %s: %v", name, err)
		}
	}
	return false, nil
}

// checkoutStatusClean reports whether the worktree's index and working tree
// are clean according to `git status --porcelain -z`. With untrackedAll the
// requirement includes every untracked file; ignored content is not listed
// and remains allowed. A nonzero status exit or truncated output is an
// inspection failure, never evidence of cleanliness.
func checkoutStatusClean(ctx context.Context, repoPath string, untrackedAll bool, options OriginCheckOptions) (bool, error) {
	args := []string{"status", "--porcelain", "-z"}
	if untrackedAll {
		args = append(args, "--untracked-files=all")
	} else {
		args = append(args, "--untracked-files=no")
	}
	output, err := boundedZOutput(ctx, repoPath, args, options)
	if err != nil {
		return false, err
	}
	return len(output) == 0, nil
}

// checkoutIgnoredPathCollision reports whether advancing repoPath's checkout
// from oldSHA to newSHA would overwrite ignored content. Incoming paths are
// the added, modified, copied, or type-changed paths between the two commits;
// ignored entries are the worktree's ignored files listed individually. A
// collision is any intersection where an ignored entry occupies an incoming
// path, an incoming path needs an ignored entry's parent location, or an
// ignored entry is nested where an incoming path must be a file or directory
// — covering nested paths, file/directory conflicts, and symlink collision
// shapes. Non-conflicting ignored content elsewhere is allowed and never
// rewritten by this check's caller. Inspection failures fail closed.
func checkoutIgnoredPathCollision(ctx context.Context, repoPath, oldSHA, newSHA string, options OriginCheckOptions) (bool, error) {
	ignored, err := boundedZOutput(ctx, repoPath, []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"}, options)
	if err != nil {
		return false, err
	}
	if len(ignored) == 0 {
		return false, nil
	}
	ignoredPaths := strings.Split(strings.TrimSuffix(ignored, "\x00"), "\x00")
	incoming, err := boundedZOutput(ctx, repoPath, []string{"diff", "--name-only", "--no-renames", "--diff-filter=AMCT", "-z", oldSHA, newSHA}, options)
	if err != nil {
		return false, err
	}
	if len(incoming) == 0 {
		return false, nil
	}
	for _, path := range strings.Split(strings.TrimSuffix(incoming, "\x00"), "\x00") {
		if path == "" {
			continue
		}
		for _, ignoredPath := range ignoredPaths {
			if ignoredPath == "" {
				continue
			}
			if ignoredPath == path ||
				strings.HasPrefix(ignoredPath, path+"/") ||
				strings.HasPrefix(path, ignoredPath+"/") {
				return true, nil
			}
		}
	}
	return false, nil
}

// boundedZOutput runs one NUL-record git command whose stdout is parsed, not
// displayed. The captured bound exceeds the diagnostic limit so realistic
// path lists are complete. One extra byte is captured beyond the bound so
// that a buffer filled exactly at a NUL record boundary is distinguishable
// from a genuinely complete result: any capture larger than the bound proves
// the git output itself was larger and fails closed.
func boundedZOutput(ctx context.Context, repoPath string, args []string, options OriginCheckOptions) (string, error) {
	safety := options
	if safety.DiagnosticLimit < checkoutSafetyOutputBound+1 {
		safety.DiagnosticLimit = checkoutSafetyOutputBound + 1
	}
	result := runOriginCommand(ctx, repoPath, args, options.commandTimeout(), safety)
	if result.ExitCode != 0 {
		return "", fmt.Errorf("inspecting the checkout (%s): %s", args[0], nonemptyBranchProbeDiagnostic(result.Diagnostics))
	}
	output := result.Stdout
	if len(output) > checkoutSafetyOutputBound {
		return "", fmt.Errorf("inspecting the checkout (%s): the result exceeded the bounded output size", args[0])
	}
	if len(output) > 0 && !strings.HasSuffix(output, "\x00") {
		return "", fmt.Errorf("inspecting the checkout (%s): the result was truncated", args[0])
	}
	return output, nil
}
