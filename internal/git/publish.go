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

// PushFunc is the function used by Push. Tests can replace it to avoid real
// git-push operations (e.g. when corporate hooks block pushes to local repos).
var PushFunc = defaultPush

// Push pushes a worktree's branch to origin.
func Push(worktreePath, branch string) error {
	return PushFunc(worktreePath, branch)
}

func defaultPush(worktreePath, branch string) error {
	cmd := exec.Command("git", "-C", worktreePath, "push", "-u", "origin", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pushing branch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// CreatePR creates a GitHub PR using the gh CLI.
// If baseBranch is provided and non-empty, the PR targets that branch
// instead of the repository's default branch (for stacked PRs).
//
// If a PR already exists for the branch, the existing PR URL is returned
// instead of an error (the push already updated the remote branch).
func CreatePR(repoPath, branch, title, body string, baseBranch ...string) (string, error) {
	body = InjectPRSignature(body)
	args := []string{"pr", "create",
		"--title", title,
		"--body", body,
		"--head", branch,
	}
	if len(baseBranch) > 0 && baseBranch[0] != "" {
		args = append(args, "--base", baseBranch[0])
	}
	cmd := exec.Command("gh", args...)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		output := strings.TrimSpace(string(out))
		if url := extractExistingPRURL(output); url != "" {
			return url, nil
		}
		return "", fmt.Errorf("creating PR: %s: %w", output, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// extractExistingPRURL extracts a GitHub PR URL from gh's "already exists"
// error output. Returns empty string if the output doesn't match.
//
// Example gh output:
//
//	a pull request for branch "feature/foo" into branch "main" already exists:
//	https://github.com/org/repo/pull/123
func extractExistingPRURL(output string) string {
	if !strings.Contains(output, "already exists") {
		return ""
	}
	idx := strings.LastIndex(output, "https://")
	if idx < 0 {
		return ""
	}
	url := strings.Fields(output[idx:])[0]
	if strings.Contains(url, "/pull/") {
		return url
	}
	return ""
}

// DiffSummary returns the diff between the worktree branch and its base branch,
// including both committed and uncommitted (staged + unstaged) changes.
// If baseBranch is empty, it falls back to main/master.
func DiffSummary(worktreePath string, baseBranch ...string) (string, error) {
	base := resolveBase(worktreePath, baseBranch...)
	var parts []string

	committed, _ := diffRange(worktreePath, base+"...HEAD")
	if committed != "" {
		parts = append(parts, committed)
	}

	// Uncommitted diff: working tree changes not yet committed (tracked files)
	cmd := exec.Command("git", "-C", worktreePath, "diff", "HEAD")
	out, err := cmd.CombinedOutput()
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		parts = append(parts, string(out))
	}

	// Untracked files: new files not yet staged
	// git diff doesn't show untracked files, so we use git ls-files
	// and generate a diff-style output for each one
	untrackedDiff, _ := untrackedFilesDiff(worktreePath)
	if untrackedDiff != "" {
		parts = append(parts, untrackedDiff)
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("no changes found in %s", worktreePath)
	}
	return strings.Join(parts, "\n"), nil
}

func diffRange(worktreePath, rangeSpec string) (string, error) {
	cmd := exec.Command("git", "-C", worktreePath, "diff", rangeSpec)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return "", nil
	}
	return string(out), nil
}

// untrackedFilesDiff generates a unified diff representation for untracked files.
func untrackedFilesDiff(worktreePath string) (string, error) {
	cmd := exec.Command("git", "-C", worktreePath, "ls-files", "--others", "--exclude-standard")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	files := strings.TrimSpace(string(out))
	if files == "" {
		return "", nil
	}

	var parts []string
	for _, file := range strings.Split(files, "\n") {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(worktreePath, file))
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		// Build a unified diff block for this new file
		var diff strings.Builder
		fmt.Fprintf(&diff, "diff --git a/%s b/%s\n", file, file)
		diff.WriteString("new file mode 100644\n")
		diff.WriteString("--- /dev/null\n")
		fmt.Fprintf(&diff, "+++ b/%s\n", file)
		fmt.Fprintf(&diff, "@@ -0,0 +1,%d @@\n", len(lines))
		for _, line := range lines {
			diff.WriteString("+" + line + "\n")
		}
		parts = append(parts, diff.String())
	}
	return strings.Join(parts, ""), nil
}

// HasUncommittedChanges returns true if the worktree has staged, unstaged,
// or untracked changes.
func HasUncommittedChanges(worktreePath string) bool {
	cmd := exec.Command("git", "-C", worktreePath, "status", "--porcelain")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// HasLocalCommits returns true if the current branch has commits not yet
// pushed to its upstream tracking branch. Returns false when no upstream
// is configured (e.g. branch hasn't been pushed yet).
func HasLocalCommits(worktreePath string) bool {
	cmd := exec.Command("git", "-C", worktreePath, "rev-list", "--count", "@{u}..HEAD")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != "0"
}

// CommitAll stages all changes (including untracked files) and creates a commit.
// The Agentic signature trailer is automatically appended to the commit message.
func CommitAll(worktreePath, message string) error {
	add := exec.Command("git", "-C", worktreePath, "add", "-A")
	if out, err := add.CombinedOutput(); err != nil {
		return fmt.Errorf("staging changes: %s: %w", strings.TrimSpace(string(out)), err)
	}
	// Append Agentic signature trailer
	message = message + "\n\n" + CommitSignatureTrailer
	commit := exec.Command("git", "-C", worktreePath, "commit", "-m", message)
	if out, err := commit.CombinedOutput(); err != nil {
		return fmt.Errorf("creating commit: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// CommitAllAndGetHead stages all changes, creates a commit when needed, and
// returns the full HEAD SHA after the operation. A clean worktree is not an
// error; the existing HEAD SHA is returned.
func CommitAllAndGetHead(worktreePath, message string) (string, error) {
	add := exec.Command("git", "-C", worktreePath, "add", "-A")
	if out, err := add.CombinedOutput(); err != nil {
		return "", fmt.Errorf("staging changes: %s: %w", strings.TrimSpace(string(out)), err)
	}
	status := exec.Command("git", "-C", worktreePath, "status", "--porcelain")
	out, err := status.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("checking staged changes: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return CurrentHeadSHA(worktreePath)
	}
	message = message + "\n\n" + CommitSignatureTrailer
	commit := exec.Command("git", "-C", worktreePath, "commit", "-m", message)
	if out, err := commit.CombinedOutput(); err != nil {
		return "", fmt.Errorf("creating commit: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return CurrentHeadSHA(worktreePath)
}

// CurrentHeadSHA returns the full SHA of HEAD in the given worktree.
func CurrentHeadSHA(worktreePath string) (string, error) {
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("getting HEAD SHA: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ClosePR closes a GitHub PR by URL using the gh CLI.
// Errors are returned but callers should treat them as non-fatal.
func ClosePR(prURL string) error {
	cmd := exec.Command("gh", "pr", "close", prURL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("closing PR: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// CommitLog returns the commit log between the worktree branch and its base branch.
func CommitLog(worktreePath string, baseBranch ...string) (string, error) {
	base := resolveBase(worktreePath, baseBranch...)
	cmd := exec.Command("git", "-C", worktreePath, "log", "--oneline", base+"..HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("getting commit log: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}

// CommitBodies returns the full commit messages (subject + body) between the
// worktree branch and its base branch, separated by blank lines. Feeds PR
// description generation where the short oneline log is not descriptive enough.
func CommitBodies(worktreePath string, baseBranch ...string) (string, error) {
	base := resolveBase(worktreePath, baseBranch...)
	cmd := exec.Command("git", "-C", worktreePath, "log", "--format=%B%n---commit---", base+"..HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("getting commit bodies: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}

// DiffStat returns a per-file summary of additions/deletions between the
// worktree branch and its base branch. Useful as a compact signal of scope
// when the full diff is too large to prompt with.
func DiffStat(worktreePath string, baseBranch ...string) (string, error) {
	base := resolveBase(worktreePath, baseBranch...)
	cmd := exec.Command("git", "-C", worktreePath, "diff", "--stat", base+"...HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("getting diff stat: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}

// resolveBase returns the base branch to diff against. If an explicit base is
// provided, it's used directly. Otherwise falls back to detecting main/master.
func resolveBase(worktreePath string, baseBranch ...string) string {
	if len(baseBranch) > 0 && baseBranch[0] != "" {
		return baseBranch[0]
	}
	return DefaultBranch(worktreePath)
}
