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

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// PullRebaseOutcome / PullRebaseResult alias the port-native types so the
// canonical definition lives in ports; git keeps these aliases for source
// compatibility with existing callers.
type (
	PullRebaseOutcome = ports.PullRebaseOutcome
	PullRebaseResult  = ports.PullRebaseResult
)

// Re-exported PullRebase outcomes. Canonical constants live in ports.
const (
	PullRebaseSuccess  = ports.PullRebaseSuccess
	PullRebaseConflict = ports.PullRebaseConflict
	PullRebaseFailure  = ports.PullRebaseFailure
)

// PullRebase fetches from origin and rebases the current branch onto the
// remote tracking branch. This syncs local commits on top of any remote
// changes to the same branch before pushing.
//
// Outcomes:
//   - Success: remote branch absent (first publish), already up-to-date, or rebase succeeded
//   - Conflict: rebase had conflicts; rebase aborted, worktree left clean
//   - Failure: network/auth/fetch error or other non-conflict failure
func PullRebase(worktreePath, branch string) PullRebaseResult {
	// 1. Fetch from origin
	fetchCmd := exec.Command("git", "-C", worktreePath, "fetch", "origin")
	if out, err := fetchCmd.CombinedOutput(); err != nil {
		return PullRebaseResult{
			Outcome: PullRebaseFailure,
			Err:     fmt.Errorf("fetch failed: %s: %w", strings.TrimSpace(string(out)), err),
		}
	}

	// 2. Check if origin/<branch> exists
	verifyCmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--verify", "origin/"+branch)
	if err := verifyCmd.Run(); err != nil {
		// Remote branch doesn't exist (first publish) — no-op
		return PullRebaseResult{Outcome: PullRebaseSuccess}
	}

	// 3. Rebase onto origin/<branch>
	target := "origin/" + branch
	rebaseCmd := exec.Command("git", "-C", worktreePath, "rebase", target)
	if out, err := rebaseCmd.CombinedOutput(); err != nil {
		// 4. Check if this is a conflict (rebase-merge or rebase-apply directory exists)
		gitDir := resolveGitDir(worktreePath)
		isConflict := false
		for _, dir := range []string{"rebase-merge", "rebase-apply"} {
			if _, statErr := os.Stat(filepath.Join(gitDir, dir)); statErr == nil {
				isConflict = true
				break
			}
		}

		// Abort the rebase to leave the worktree clean
		abortCmd := exec.Command("git", "-C", worktreePath, "rebase", "--abort")
		_ = abortCmd.Run()

		if isConflict {
			return PullRebaseResult{
				Outcome: PullRebaseConflict,
				Err:     fmt.Errorf("pull-rebase conflict: %s", strings.TrimSpace(string(out))),
			}
		}
		return PullRebaseResult{
			Outcome: PullRebaseFailure,
			Err:     fmt.Errorf("rebase failed: %s: %w", strings.TrimSpace(string(out)), err),
		}
	}

	return PullRebaseResult{Outcome: PullRebaseSuccess}
}

// resolveGitDir returns the .git directory for a worktree path.
// For worktrees, .git is a file pointing to the actual git dir.
func resolveGitDir(worktreePath string) string {
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--git-dir")
	out, err := cmd.Output()
	if err != nil {
		return filepath.Join(worktreePath, ".git")
	}
	dir := strings.TrimSpace(string(out))
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(worktreePath, dir)
}

// Fetch fetches the latest changes from origin for a worktree.
func Fetch(worktreePath string) error {
	cmd := exec.Command("git", "-C", worktreePath, "fetch", "origin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetching origin: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// RebaseOutcome / RebaseResult alias the port-native types.
type (
	RebaseOutcome = ports.RebaseOutcome
	RebaseResult  = ports.RebaseResult
)

// Re-exported rebase outcomes. Canonical constants live in ports.
const (
	RebaseSuccess  = ports.RebaseSuccess
	RebaseConflict = ports.RebaseConflict
	RebaseFailed   = ports.RebaseFailed
)

// RebaseOnto rebases the current branch onto the given target ref (e.g.
// "origin/master"). Unlike Rebase, on conflict the rebase is NOT aborted —
// the worktree is left mid-rebase with conflict markers in the files so an
// agent can resolve them and run "git rebase --continue".
func RebaseOnto(worktreePath, target string) RebaseResult {
	cmd := exec.Command("git", "-C", worktreePath, "rebase", target)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return RebaseResult{Outcome: RebaseSuccess}
	}

	// Check if this is a conflict (rebase-merge or rebase-apply directory exists)
	gitDir := resolveGitDir(worktreePath)
	isConflict := false
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if _, statErr := os.Stat(filepath.Join(gitDir, dir)); statErr == nil {
			isConflict = true
			break
		}
	}

	if isConflict {
		// List conflicted files — leave rebase in progress
		conflictFiles := listConflictFiles(worktreePath)
		return RebaseResult{
			Outcome:       RebaseConflict,
			ConflictFiles: conflictFiles,
			Err:           fmt.Errorf("rebase conflicts: %s", strings.TrimSpace(string(out))),
		}
	}

	// Non-conflict failure — abort to leave worktree clean
	abortCmd := exec.Command("git", "-C", worktreePath, "rebase", "--abort")
	_ = abortCmd.Run()
	return RebaseResult{
		Outcome: RebaseFailed,
		Err:     fmt.Errorf("rebase failed: %s: %w", strings.TrimSpace(string(out)), err),
	}
}

// listConflictFiles returns the list of files with unmerged conflicts.
func listConflictFiles(worktreePath string) []string {
	cmd := exec.Command("git", "-C", worktreePath, "diff", "--name-only", "--diff-filter=U")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

// Rebase rebases the current branch onto the specified base branch.
// Returns nil on success. If there are conflicts, returns an error and
// aborts the rebase to leave the worktree clean.
func Rebase(worktreePath, baseBranch string) error {
	target := "origin/" + baseBranch
	cmd := exec.Command("git", "-C", worktreePath, "rebase", target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Abort the rebase to leave the worktree clean
		abortCmd := exec.Command("git", "-C", worktreePath, "rebase", "--abort")
		_ = abortCmd.Run()
		return fmt.Errorf("rebase failed (conflicts likely): %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ForcePushFunc is the function used by ForcePush. Tests can replace it to
// avoid real git-push operations.
var ForcePushFunc = defaultForcePush

// ForcePush force-pushes the current branch to origin.
func ForcePush(worktreePath, branch string) error {
	return ForcePushFunc(worktreePath, branch)
}

func defaultForcePush(worktreePath, branch string) error {
	cmd := exec.Command("git", "-C", worktreePath, "push", "--force-with-lease", "-u", "origin", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("force pushing branch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// PRBaseBranch returns the base branch of an open PR using the gh CLI.
// prURL should be a full GitHub PR URL (e.g. https://github.com/owner/repo/pull/42).
// Returns empty string on any error.
func PRBaseBranch(repoPath, prURL string) string {
	cmd := exec.Command("gh", "pr", "view", prURL, "--json", "baseRefName", "--jq", ".baseRefName")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// IsBehindRemote checks if the local branch is behind the remote base branch.
// Returns true if there are commits on origin/<baseBranch> not in the local branch.
func IsBehindRemote(worktreePath, baseBranch string) bool {
	target := "origin/" + baseBranch
	cmd := exec.Command("git", "-C", worktreePath, "rev-list", "--count", "HEAD.."+target)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	count := strings.TrimSpace(string(out))
	return count != "0"
}

// identityFallbackArgs returns -c committer-identity fallbacks when neither
// the repo nor the environment configures one; an explicit identity wins.
func identityFallbackArgs(repoPath string) []string {
	out, err := exec.Command("git", "-C", repoPath, "config", "user.email").Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return nil
	}
	return []string{"-c", "user.name=Agentico", "-c", "user.email=agentico@localhost"}
}

// MergeFeatureBranch merges the given feature branch into baseBranch in the repo at repoPath.
// It checks out baseBranch, performs a --no-ff merge, then checks out the original branch.
// Returns an error with a conflict hint if the merge fails due to conflicts.
func MergeFeatureBranch(repoPath, featureBranch, baseBranch string) error {
	// Check out the base branch
	checkoutCmd := exec.Command("git", "-C", repoPath, "checkout", baseBranch)
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("checkout %s: %s: %w", baseBranch, strings.TrimSpace(string(out)), err)
	}

	// Merge with --no-ff. The merge commit needs a committer identity; fall
	// back to the Agentico identity when the environment has none configured.
	mergeArgs := append([]string{"-C", repoPath}, identityFallbackArgs(repoPath)...)
	mergeArgs = append(mergeArgs, "merge", "--no-ff", featureBranch, "-m",
		fmt.Sprintf("Merge branch '%s'", featureBranch))
	mergeCmd := exec.Command("git", mergeArgs...)
	if out, err := mergeCmd.CombinedOutput(); err != nil {
		// Abort the failed merge
		abortCmd := exec.Command("git", "-C", repoPath, "merge", "--abort")
		_ = abortCmd.Run()
		// Return to feature branch
		backCmd := exec.Command("git", "-C", repoPath, "checkout", featureBranch)
		_ = backCmd.Run()
		return fmt.Errorf("merge conflicts — rebase with [b] first, then retry: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Return to feature branch
	backCmd := exec.Command("git", "-C", repoPath, "checkout", featureBranch)
	_ = backCmd.Run()

	return nil
}

// RebaseLocal rebases the current branch onto a local base branch (no origin/ prefix).
// Used for repos without a remote. Aborts on conflict to leave the worktree clean.
func RebaseLocal(worktreePath, baseBranch string) error {
	cmd := exec.Command("git", "-C", worktreePath, "rebase", baseBranch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		abortCmd := exec.Command("git", "-C", worktreePath, "rebase", "--abort")
		_ = abortCmd.Run()
		return fmt.Errorf("rebase failed (conflicts likely): %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// RebaseInProgress reports whether the worktree has an unfinished rebase.
// A rebase leaves either a rebase-merge/ (interactive / merge-strategy rebase)
// or a rebase-apply/ (am-based rebase) directory inside the worktree's git
// dir until `git rebase --continue` / `--abort` / `--skip` clears it. Callers
// use this to detect stuck rebases before treating the branch as "done".
func RebaseInProgress(worktreePath string) bool {
	gitDir := resolveGitDir(worktreePath)
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, dir)); err == nil {
			return true
		}
	}
	return false
}

// IsBehindLocal checks if the current branch is behind a local base branch.
// Returns true if there are commits on baseBranch not in the current branch.
func IsBehindLocal(worktreePath, baseBranch string) bool {
	cmd := exec.Command("git", "-C", worktreePath, "rev-list", "--count", "HEAD.."+baseBranch)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	count := strings.TrimSpace(string(out))
	return count != "0"
}
