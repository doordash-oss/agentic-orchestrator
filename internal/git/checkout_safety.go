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

	"golang.org/x/text/unicode/norm"
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
// shapes. Paths are compared as the worktree's filesystem resolves them, so
// spellings that differ only by letter case or Unicode normalization on a
// folding filesystem still collide. Non-conflicting ignored content elsewhere
// is allowed and never rewritten by this check's caller. Inspection failures
// fail closed.
func checkoutIgnoredPathCollision(ctx context.Context, repoPath, oldSHA, newSHA string, options OriginCheckOptions) (bool, error) {
	ignored, err := boundedZOutput(ctx, repoPath, []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"}, options)
	if err != nil {
		return false, err
	}
	if len(ignored) == 0 {
		return false, nil
	}
	incoming, err := boundedZOutput(ctx, repoPath, []string{"diff", "--name-only", "--no-renames", "--diff-filter=AMCT", "-z", oldSHA, newSHA}, options)
	if err != nil {
		return false, err
	}
	if len(incoming) == 0 {
		return false, nil
	}
	folding, err := checkoutPathFoldingFor(ctx, repoPath, options)
	if err != nil {
		return false, err
	}
	var ignoredKeys []string
	for _, ignoredPath := range strings.Split(strings.TrimSuffix(ignored, "\x00"), "\x00") {
		if ignoredPath == "" {
			continue
		}
		ignoredKeys = append(ignoredKeys, folding.key(ignoredPath))
	}
	for _, path := range strings.Split(strings.TrimSuffix(incoming, "\x00"), "\x00") {
		if path == "" {
			continue
		}
		key := folding.key(path)
		for _, ignoredKey := range ignoredKeys {
			if ignoredKey == key ||
				strings.HasPrefix(ignoredKey, key+"/") ||
				strings.HasPrefix(key, ignoredKey+"/") {
				return true, nil
			}
		}
	}
	return false, nil
}

// checkoutPathFolding records how the worktree's filesystem collapses
// distinct path spellings onto one directory entry. Comparing raw bytes is
// not enough: on the case-insensitive, Unicode-precomposing filesystems
// macOS uses by default, an ignored entry and an incoming tracked path that
// differ only by letter case or by Unicode normalization occupy the same
// entry, and the checkout that follows this check treats ignored content as
// expendable.
type checkoutPathFolding struct {
	foldCase   bool
	precompose bool
}

// key maps a path to the identity its filesystem gives it, so spellings that
// name the same entry compare equal. Folding more aggressively than the
// filesystem does can only make this advisory check refuse an update; it can
// never let an overwrite through, so the safe direction is to over-match.
func (folding checkoutPathFolding) key(path string) string {
	if folding.precompose {
		path = norm.NFC.String(path)
	}
	if folding.foldCase {
		path = strings.ToLower(path)
	}
	return path
}

// checkoutPathFoldingFor reads the worktree's recorded path-folding
// properties. Git writes core.ignorecase and core.precomposeunicode when the
// repository is created, after probing the filesystem itself. An absent
// core.ignorecase is not assumed to mean case-sensitive — assuming that
// wrongly is exactly what overwrites ignored local files — so the filesystem
// is probed instead. Any inspection failure fails closed.
func checkoutPathFoldingFor(ctx context.Context, repoPath string, options OriginCheckOptions) (checkoutPathFolding, error) {
	folding := checkoutPathFolding{}
	ignoreCase, err := checkoutBoolConfig(ctx, repoPath, "core.ignorecase", options)
	if err != nil {
		return checkoutPathFolding{}, err
	}
	if ignoreCase == "" {
		probed, probeErr := checkoutFilesystemFoldsCase(repoPath)
		if probeErr != nil {
			return checkoutPathFolding{}, probeErr
		}
		folding.foldCase = probed
	} else {
		folding.foldCase = ignoreCase == "true"
	}
	precompose, err := checkoutBoolConfig(ctx, repoPath, "core.precomposeunicode", options)
	if err != nil {
		return checkoutPathFolding{}, err
	}
	folding.precompose = precompose == "true"
	return folding, nil
}

// checkoutBoolConfig reads one boolean repository config value, returning ""
// for an unset key so callers can tell absence from a recorded false. A
// value Git does not render as a boolean, or any other failure, is an
// inspection failure rather than a default.
func checkoutBoolConfig(ctx context.Context, repoPath, key string, options OriginCheckOptions) (string, error) {
	result := runOriginCommand(ctx, repoPath, []string{"config", "--type=bool", "--get", key}, options.commandTimeout(), options)
	switch {
	case result.ExitCode == 0:
		value := strings.TrimSpace(result.Stdout)
		if value != "true" && value != "false" {
			return "", fmt.Errorf("reading %s configuration: unexpected value %q", key, value)
		}
		return value, nil
	case result.ExitCode == 1:
		return "", nil
	default:
		return "", fmt.Errorf("reading %s configuration: %s", key, nonemptyBranchProbeDiagnostic(result.Diagnostics))
	}
}

// checkoutFilesystemFoldsCase reports whether repoPath's filesystem resolves
// one directory entry under differently cased names. It only reads: the
// worktree's always-present .git entry is looked up again under a
// case-swapped name and the two results are compared by identity, so the
// probe never writes to the worktree it is inspecting.
func checkoutFilesystemFoldsCase(repoPath string) (bool, error) {
	info, err := os.Lstat(filepath.Join(repoPath, ".git"))
	if err != nil {
		return false, fmt.Errorf("probing the worktree filesystem for case folding: %v", err)
	}
	swapped, err := os.Lstat(filepath.Join(repoPath, ".GIT"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("probing the worktree filesystem for case folding: %v", err)
	}
	return os.SameFile(info, swapped), nil
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
