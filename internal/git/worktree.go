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

// WorktreeInfo aliases the canonical port type.
type WorktreeInfo = ports.WorktreeInfo

type WorktreeManager struct {
	BaseDir string
}

func NewWorktreeManager(baseDir string) *WorktreeManager {
	return &WorktreeManager{BaseDir: baseDir}
}

// Create creates a new worktree branching from startPoint. If startPoint is
// empty, HEAD is used (preserving legacy behavior).
func (w *WorktreeManager) Create(repoPath, featureSlug, repoName, startPoint string) (string, error) {
	branch := BranchName(featureSlug)
	wtPath := filepath.Join(w.BaseDir, featureSlug, repoName)

	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return "", fmt.Errorf("creating worktree directory: %w", err)
	}

	if startPoint == "" {
		startPoint = "HEAD"
	}

	// Prune stale worktrees before creating to avoid conflicts
	pruneCmd := exec.Command("git", "-C", repoPath, "worktree", "prune")
	_ = pruneCmd.Run()

	// Try creating with a new branch first
	cmd := exec.Command("git", "-C", repoPath, "worktree", "add", wtPath, "-b", branch, startPoint)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Branch may already exist from a previous run; try using it
		cmd = exec.Command("git", "-C", repoPath, "worktree", "add", wtPath, branch)
		out, err = cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("creating worktree: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}

	return wtPath, nil
}

// DefaultBranch returns the default branch for a repo by checking the remote
// HEAD, then the local HEAD symref, then falling back to well-known names
// (main, master).
func DefaultBranch(repoPath string) string {
	// Try remote HEAD symref (most reliable for repos with a remote)
	cmd := exec.Command("git", "-C", repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
	if out, err := cmd.Output(); err == nil {
		ref := strings.TrimSpace(string(out))
		// refs/remotes/origin/main → main
		if parts := strings.Split(ref, "/"); len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}

	// Check local HEAD symref — works for any repo regardless of remote
	cmd = exec.Command("git", "-C", repoPath, "symbolic-ref", "HEAD")
	if out, err := cmd.Output(); err == nil {
		ref := strings.TrimSpace(string(out))
		// refs/heads/trunk → trunk
		if parts := strings.Split(ref, "/"); len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}

	// Fallback: check if main or master branches exist locally
	for _, name := range []string{"main", "master"} {
		cmd = exec.Command("git", "-C", repoPath, "rev-parse", "--verify", name)
		if err := cmd.Run(); err == nil {
			return name
		}
	}

	return "main"
}

// CurrentBranch returns the branch currently checked out in the given repo.
func CurrentBranch(repoPath string) string {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (w *WorktreeManager) Remove(worktreePath string, deleteBranch bool) error {
	// Find the main repo for this worktree
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--git-common-dir")
	out, err := cmd.Output()
	if err != nil {
		// Worktree may already be gone; just remove directory
		return os.RemoveAll(worktreePath)
	}
	commonDir := strings.TrimSpace(string(out))
	mainRepo := filepath.Dir(commonDir)

	// Get branch name before removing
	var branch string
	if deleteBranch {
		branchCmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
		branchOut, err := branchCmd.Output()
		if err == nil {
			branch = strings.TrimSpace(string(branchOut))
		}
	}

	// Remove worktree
	removeCmd := exec.Command("git", "-C", mainRepo, "worktree", "remove", worktreePath, "--force")
	if out, err := removeCmd.CombinedOutput(); err != nil {
		// Try manual removal
		_ = os.RemoveAll(worktreePath)
		pruneCmd := exec.Command("git", "-C", mainRepo, "worktree", "prune")
		_ = pruneCmd.Run()
		_ = out // suppress unused
	}

	// Delete branch if requested
	if deleteBranch && branch != "" && branch != "main" && branch != "master" {
		delCmd := exec.Command("git", "-C", mainRepo, "branch", "-D", branch)
		_ = delCmd.Run()
	}

	return nil
}

func (w *WorktreeManager) List() ([]WorktreeInfo, error) {
	var result []WorktreeInfo

	entries, err := os.ReadDir(w.BaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing worktree directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		featureDir := filepath.Join(w.BaseDir, entry.Name())
		repoEntries, err := os.ReadDir(featureDir)
		if err != nil {
			continue
		}
		for _, re := range repoEntries {
			if !re.IsDir() {
				continue
			}
			wtPath := filepath.Join(featureDir, re.Name())
			branch := CurrentBranch(wtPath)
			result = append(result, WorktreeInfo{
				Path:      wtPath,
				Branch:    branch,
				RepoName:  re.Name(),
				FeatureID: entry.Name(),
			})
		}
	}

	return result, nil
}

func (w *WorktreeManager) DetectStale(activeFeatureIDs []string) ([]WorktreeInfo, error) {
	all, err := w.List()
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool)
	for _, id := range activeFeatureIDs {
		active[id] = true
	}
	var stale []WorktreeInfo
	for _, wt := range all {
		if !active[wt.FeatureID] {
			stale = append(stale, wt)
		}
	}
	return stale, nil
}

// ResetToBase hard-resets a worktree back to its base branch, discarding all
// local commits and changes on the feature branch.
func (w *WorktreeManager) ResetToBase(worktreePath, baseBranch string) error {
	// Fetch the latest base branch so origin/<baseBranch> is up-to-date
	fetchCmd := exec.Command("git", "-C", worktreePath, "fetch", "origin", baseBranch)
	if out, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fetching origin/%s: %s: %w", baseBranch, strings.TrimSpace(string(out)), err)
	}
	cmd := exec.Command("git", "-C", worktreePath, "reset", "--hard", "origin/"+baseBranch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("resetting worktree to %s: %s: %w", baseBranch, strings.TrimSpace(string(out)), err)
	}
	cmd = exec.Command("git", "-C", worktreePath, "clean", "-fd")
	_ = cmd.Run()
	return nil
}

// ResetToBaseLocal hard-resets a worktree back to its local base branch ref,
// without fetching from any remote. Used for repos without an origin remote.
func (w *WorktreeManager) ResetToBaseLocal(worktreePath, baseBranch string) error {
	cmd := exec.Command("git", "-C", worktreePath, "reset", "--hard", baseBranch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("resetting to local %s: %s: %w", baseBranch, strings.TrimSpace(string(out)), err)
	}
	cmd = exec.Command("git", "-C", worktreePath, "clean", "-fd")
	_ = cmd.Run()
	return nil
}

// ResetToCommit hard-resets a worktree to a local commit SHA without fetching
// from any remote, then cleans untracked files.
func (w *WorktreeManager) ResetToCommit(worktreePath, commitSHA string) error {
	cmd := exec.Command("git", "-C", worktreePath, "reset", "--hard", commitSHA)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("resetting to commit %s: %s: %w", commitSHA, strings.TrimSpace(string(out)), err)
	}
	cmd = exec.Command("git", "-C", worktreePath, "clean", "-fd")
	_ = cmd.Run()
	return nil
}

func (w *WorktreeManager) HasUncommittedChanges(repoPath string) (bool, error) {
	cmd := exec.Command("git", "-C", repoPath, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("checking git status: %w", err)
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}
