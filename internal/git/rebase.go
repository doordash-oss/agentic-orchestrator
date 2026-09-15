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

	"github.com/doordash-oss/agentic-orchestrator/internal/github"
)

// resolveGitDir returns the .git directory for a worktree path.
// For worktrees, .git is a file pointing to the actual git dir.
func resolveGitDir(worktreePath string) string {
	cmd := readGitCmd(worktreePath, "rev-parse", "--git-dir")
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

// listConflictFiles returns the list of files with unmerged conflicts.
func listConflictFiles(worktreePath string) []string {
	cmd := readGitCmd(worktreePath, "diff", "--name-only", "--diff-filter=U")
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

// PRBaseBranch returns the base branch of an open PR via the GitHub API.
// prURL should be a full GitHub PR URL. Returns empty string on any error.
func PRBaseBranch(_ string, prURL string) string {
	owner, repo, number, err := ParsePRURL(prURL)
	if err != nil {
		return ""
	}
	client, err := github.ForHost(prURLHost(prURL))
	if err != nil {
		return ""
	}
	info, err := client.GetPR(owner, repo, number)
	if err != nil {
		return ""
	}
	return info.BaseRef
}

// IsBehindRemote checks if the local branch is behind the remote base branch.
// Returns true if there are commits on origin/<baseBranch> not in the local branch.
func IsBehindRemote(worktreePath, baseBranch string) bool {
	target := "origin/" + baseBranch
	cmd := readGitCmd(worktreePath, "rev-list", "--count", "HEAD.."+target)
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
	out, err := readGitCmd(repoPath, "config", "user.email").Output()
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
	cmd := readGitCmd(worktreePath, "rev-list", "--count", "HEAD.."+baseBranch)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	count := strings.TrimSpace(string(out))
	return count != "0"
}
