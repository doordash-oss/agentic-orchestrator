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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type LocalSourceMode string

const (
	LocalSourceModeDefault LocalSourceMode = "default"
	LocalSourceModeCurrent LocalSourceMode = "current"
	LocalSourceBranch                      = "branch"
	LocalSourceDetached                    = "detached"
)

var ErrLocalSourceMissing = errors.New("local source is missing")

type LocalSource struct {
	Mode   LocalSourceMode
	Kind   string
	Branch string
	Commit string
}

type WorktreeManager struct {
	BaseDir string
}

func NewWorktreeManager(baseDir string) *WorktreeManager {
	return &WorktreeManager{BaseDir: baseDir}
}

// CurrentHeadSHA reports the full SHA of HEAD in the given worktree,
// exposing the package-level helper through the manager so refactor-child
// exact-base capture works through the feature.WorktreeOps wiring.
func (w *WorktreeManager) CurrentHeadSHA(worktreePath string) (string, error) {
	return CurrentHeadSHA(worktreePath)
}

func (w *WorktreeManager) ExpectedPath(featureSlug, repoName string) string {
	return filepath.Join(w.BaseDir, featureSlug, repoName)
}

// Create creates a new worktree branching from startPoint. If startPoint is
// empty, HEAD is used (preserving legacy behavior).
func (w *WorktreeManager) Create(repoPath, featureSlug, repoName, startPoint string) (string, error) {
	if strings.TrimSpace(repoPath) == "" {
		return "", fmt.Errorf("repo path is required for %q", repoName)
	}

	branch := BranchName(featureSlug)
	wtPath := w.ExpectedPath(featureSlug, repoName)

	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return "", fmt.Errorf("creating worktree directory: %w", err)
	}

	if startPoint == "" {
		startPoint = "HEAD"
	}
	exactStart := validFullCommit(startPoint)

	// Prune stale worktrees before creating to avoid conflicts
	pruneCmd := exec.Command("git", "-C", repoPath, "worktree", "prune")
	_ = pruneCmd.Run()

	// A repository with no commits has an unborn HEAD, so git cannot branch a
	// worktree from it. Surface a clear, actionable error instead of git's
	// opaque "invalid reference" message (which otherwise leaks from the
	// existing-branch fallback below).
	if !hasCommits(repoPath) {
		return "", fmt.Errorf("repository %q has no commits yet; create an initial commit before starting a feature", repoName)
	}

	// Try creating with a new branch first
	cmd := exec.Command("git", "-C", repoPath, "worktree", "add", wtPath, "-b", branch, startPoint)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Branch may already exist from a previous run; try using it. If that
		// also fails, report the original error so the real cause isn't masked.
		fbCmd := exec.Command("git", "-C", repoPath, "worktree", "add", wtPath, branch)
		if _, fbErr := fbCmd.CombinedOutput(); fbErr != nil {
			return "", fmt.Errorf("creating worktree: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	if exactStart {
		head, headErr := CurrentHeadSHA(wtPath)
		if headErr != nil {
			return "", fmt.Errorf("verifying created worktree HEAD: %w", headErr)
		}
		if !strings.EqualFold(head, startPoint) {
			return "", fmt.Errorf("created worktree is at commit %s, want accepted commit %s", head, startPoint)
		}
	}

	return wtPath, nil
}

// hasCommits reports whether the repository has at least one commit (a born
// HEAD). A freshly initialized repo returns false.
func hasCommits(repoPath string) bool {
	cmd := readGitCmd(repoPath, "rev-parse", "--verify", "--quiet", "HEAD")
	return cmd.Run() == nil
}

// DefaultBranch returns the default branch for a repo by checking the remote
// HEAD, then the local HEAD symref, then falling back to well-known names
// (main, master).
func DefaultBranch(repoPath string) string {
	branch, err := defaultLocalBranch(context.Background(), repoPath)
	if err != nil {
		return ""
	}
	if _, err := resolveLocalCommit(context.Background(), repoPath, "refs/heads/"+branch); err != nil {
		return ""
	}
	return branch
}

// InspectLocalSource resolves the exact local commit selected by the shared
// creation mode. Remote refs can nominate a default branch but can never act
// as its source; the corresponding refs/heads branch must exist locally.
func InspectLocalSource(ctx context.Context, repoPath string, mode LocalSourceMode) (LocalSource, error) {
	source := LocalSource{Mode: mode}
	switch mode {
	case LocalSourceModeDefault:
		branch, err := defaultLocalBranch(ctx, repoPath)
		if err != nil {
			return LocalSource{}, err
		}
		source.Kind = LocalSourceBranch
		source.Branch = branch
		source.Commit, err = resolveLocalCommit(ctx, repoPath, "refs/heads/"+branch)
		if err != nil {
			return LocalSource{}, fmt.Errorf("%w: default branch %q does not resolve locally", ErrLocalSourceMissing, branch)
		}
	case LocalSourceModeCurrent:
		if branch, ok := symbolicBranch(ctx, repoPath, "HEAD", "refs/heads/"); ok {
			source.Kind = LocalSourceBranch
			source.Branch = branch
		} else {
			source.Kind = LocalSourceDetached
		}
		commit, err := resolveLocalCommit(ctx, repoPath, "HEAD")
		if err != nil {
			return LocalSource{}, fmt.Errorf("%w: current checkout has no commit", ErrLocalSourceMissing)
		}
		source.Commit = commit
	default:
		return LocalSource{}, fmt.Errorf("unsupported local source mode %q", mode)
	}
	return source, nil
}

func defaultLocalBranch(ctx context.Context, repoPath string) (string, error) {
	if branch, ok := symbolicBranch(ctx, repoPath, "refs/remotes/origin/HEAD", "refs/remotes/origin/"); ok {
		return branch, nil
	}
	if branch, ok := symbolicBranch(ctx, repoPath, "HEAD", "refs/heads/"); ok {
		return branch, nil
	}
	for _, branch := range []string{"main", "master"} {
		if _, err := resolveLocalCommit(ctx, repoPath, "refs/heads/"+branch); err == nil {
			return branch, nil
		}
	}
	return "", ErrLocalSourceMissing
}

func symbolicBranch(ctx context.Context, repoPath, ref, prefix string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "symbolic-ref", "--quiet", ref)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	branch, ok := strings.CutPrefix(strings.TrimSpace(string(out)), prefix)
	return branch, ok && branch != ""
}

func resolveLocalCommit(ctx context.Context, repoPath, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(out))
	if commit == "" {
		return "", ErrLocalSourceMissing
	}
	return commit, nil
}

// CurrentBranch returns the branch currently checked out in the given repo.
func CurrentBranch(repoPath string) string {
	cmd := readGitCmd(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Remove deletes a worktree and, when requested, its ephemeral branch,
// discovering the main repository and branch from the live worktree.
// Material cleanup failures (unremovable worktree, failed prune after a
// manual fallback, failed branch deletion) are returned so callers can
// record and retry them; genuinely absent resources (missing worktree
// directory, already-deleted branch) are idempotent success.
//
// Once a worktree is deregistered its identity can no longer be discovered
// from the path, so callers that must guarantee branch deletion across
// retries (e.g. child integration cleanup) should use RemoveRef with the
// recorded identity instead.
func (w *WorktreeManager) Remove(worktreePath string, deleteBranch bool) error {
	// Find the main repo for this worktree
	cmd := readGitCmd(worktreePath, "rev-parse", "--git-common-dir")
	out, err := cmd.Output()
	if err != nil {
		// Worktree may already be gone; just remove the directory. Removing
		// an absent path is idempotent success.
		if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
			return fmt.Errorf("removing worktree directory %s: %w", worktreePath, rmErr)
		}
		return nil
	}
	commonDir := strings.TrimSpace(string(out))
	mainRepo := filepath.Dir(commonDir)

	// Get branch name before removing
	var branch string
	if deleteBranch {
		branchCmd := readGitCmd(worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
		branchOut, err := branchCmd.Output()
		if err == nil {
			branch = strings.TrimSpace(string(branchOut))
		}
	}

	return w.RemoveRef(worktreePath, mainRepo, branch)
}

// RemoveRef deletes a worktree and its ephemeral branch using the recorded
// main-repository and branch identity, so a retried cleanup still reaches
// the branch even after an earlier partial removal deregistered the
// worktree. An empty branch skips branch deletion; an already-absent branch
// is success.
func (w *WorktreeManager) RemoveRef(worktreePath, mainRepo, branch string) error {
	if mainRepo == "" {
		return fmt.Errorf("main repository is required to remove worktree %s and branch %q", worktreePath, branch)
	}

	// Remove worktree
	removeCmd := exec.Command("git", "-C", mainRepo, "worktree", "remove", worktreePath, "--force")
	out, removeErr := removeCmd.CombinedOutput()
	// git can report success while leaving the directory behind (it logs
	// "failed to delete" but still deregisters the worktree), so the absence
	// of the directory is the real success signal. Fall back to manual
	// removal plus a prune; both must succeed for cleanup to count as done.
	if _, statErr := os.Stat(worktreePath); removeErr != nil || !os.IsNotExist(statErr) {
		if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
			return fmt.Errorf("removing worktree %s: %s (manual removal failed: %v)", worktreePath, strings.TrimSpace(string(out)), rmErr)
		}
		if pruneErr := exec.Command("git", "-C", mainRepo, "worktree", "prune").Run(); pruneErr != nil {
			return fmt.Errorf("pruning worktree registry for %s after manual removal: %w", worktreePath, pruneErr)
		}
	}

	// Delete branch if requested; an already-absent branch is success.
	if branch != "" && branch != "main" && branch != "master" {
		verify := readGitCmd(mainRepo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
		if verify.Run() == nil {
			delCmd := exec.Command("git", "-C", mainRepo, "branch", "-D", branch)
			if out, err := delCmd.CombinedOutput(); err != nil {
				return fmt.Errorf("deleting branch %s: %s: %w", branch, strings.TrimSpace(string(out)), err)
			}
		}
	}

	return nil
}

// ResetToBase hard-resets a worktree back to its base branch, discarding all
// local commits and changes on the feature branch.
func (w *WorktreeManager) ResetToBase(worktreePath, baseBranch string) error {
	mu := worktreeMutationLock(worktreePath)
	mu.Lock()
	defer mu.Unlock()

	// Fetch the latest base branch so origin/<baseBranch> is up-to-date
	fetchCmd := exec.Command("git", "-C", worktreePath, "fetch", "origin", baseBranch)
	if out, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fetching origin/%s: %s: %w", baseBranch, strings.TrimSpace(string(out)), err)
	}
	if out, err := runGitMutationWithLockRetry(worktreePath, "reset", "--hard", "origin/"+baseBranch); err != nil {
		return fmt.Errorf("resetting worktree to %s: %s: %w", baseBranch, strings.TrimSpace(string(out)), err)
	}
	if out, err := runGitMutationWithLockRetry(worktreePath, "clean", "-fd"); err != nil {
		return fmt.Errorf("cleaning worktree after reset to %s: %s: %w", baseBranch, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ResetToBaseLocal hard-resets a worktree back to its local base branch ref,
// without fetching from any remote. Used for repos without an origin remote.
func (w *WorktreeManager) ResetToBaseLocal(worktreePath, baseBranch string) error {
	mu := worktreeMutationLock(worktreePath)
	mu.Lock()
	defer mu.Unlock()

	if out, err := runGitMutationWithLockRetry(worktreePath, "reset", "--hard", baseBranch); err != nil {
		return fmt.Errorf("resetting to local %s: %s: %w", baseBranch, strings.TrimSpace(string(out)), err)
	}
	if out, err := runGitMutationWithLockRetry(worktreePath, "clean", "-fd"); err != nil {
		return fmt.Errorf("cleaning worktree after reset to local %s: %s: %w", baseBranch, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ResetToCommit hard-resets a worktree to a local commit SHA without fetching
// from any remote, then cleans untracked files.
func (w *WorktreeManager) ResetToCommit(worktreePath, commitSHA string) error {
	mu := worktreeMutationLock(worktreePath)
	mu.Lock()
	defer mu.Unlock()

	if out, err := runGitMutationWithLockRetry(worktreePath, "reset", "--hard", commitSHA); err != nil {
		return fmt.Errorf("resetting to commit %s: %s: %w", commitSHA, strings.TrimSpace(string(out)), err)
	}
	if out, err := runGitMutationWithLockRetry(worktreePath, "clean", "-fd"); err != nil {
		return fmt.Errorf("cleaning worktree after reset to commit %s: %s: %w", commitSHA, strings.TrimSpace(string(out)), err)
	}
	return nil
}
