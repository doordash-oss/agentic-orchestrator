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
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/github"
)

// PushFunc is the function used by Push. Tests can replace it to avoid real
// git-push operations (e.g. when corporate hooks block pushes to local repos).
var PushFunc = defaultPush

const (
	indexLockRetryWindow       = 5 * time.Second
	indexLockRetryInitialDelay = 25 * time.Millisecond
	indexLockRetryMaxDelay     = 250 * time.Millisecond
)

var worktreeMutationLocks sync.Map

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

// CreatePR creates a GitHub PR for the branch pushed from repoPath.
// If baseBranch is provided and non-empty, the PR targets that branch
// instead of the repository's default branch (for stacked PRs). When
// draft is true the PR is created as a draft. If a PR already exists
// for the branch, the existing PR URL is returned instead of an error
// (the push already updated the remote branch).
func CreatePR(repoPath, branch, title, body string, draft bool, baseBranch ...string) (string, error) {
	body = InjectPRSignature(body)
	host, owner, repo, err := originRepo(repoPath)
	if err != nil {
		return "", fmt.Errorf("creating PR: %w", err)
	}
	client, err := github.ForHost(host)
	if err != nil {
		return "", fmt.Errorf("creating PR: %w", err)
	}
	base := ""
	if len(baseBranch) > 0 {
		base = baseBranch[0]
	}
	prURL, err := client.CreatePR(github.CreatePRParams{
		Owner: owner, Repo: repo, Head: branch, Base: base,
		Title: title, Body: body, Draft: draft,
	})
	if err != nil {
		return "", err
	}
	return prURL, nil
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

// CommitAll stages all changes (including untracked files) and creates a commit.
// The Agentic signature trailer is automatically appended to the commit message.
func CommitAll(worktreePath, message string) error {
	mu := worktreeMutationLock(worktreePath)
	mu.Lock()
	defer mu.Unlock()

	if out, err := runGitMutationWithIndexLockRetry(worktreePath, "add", "-A"); err != nil {
		return fmt.Errorf("staging changes: %s: %w", strings.TrimSpace(string(out)), err)
	}
	// Append Agentic signature trailer
	message = message + "\n\n" + CommitSignatureTrailer
	if out, err := runGitMutationWithIndexLockRetry(worktreePath, "commit", "-m", message); err != nil {
		return fmt.Errorf("creating commit: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// CommitAllAndGetHead stages all changes, creates a commit when needed, and
// returns the full HEAD SHA after the operation. A clean worktree is not an
// error; the existing HEAD SHA is returned.
func CommitAllAndGetHead(worktreePath, message string) (string, error) {
	mu := worktreeMutationLock(worktreePath)
	mu.Lock()
	defer mu.Unlock()

	if out, err := runGitMutationWithIndexLockRetry(worktreePath, "add", "-A"); err != nil {
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
	if out, err := runGitMutationWithIndexLockRetry(worktreePath, "commit", "-m", message); err != nil {
		return "", fmt.Errorf("creating commit: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return CurrentHeadSHA(worktreePath)
}

func worktreeMutationLock(worktreePath string) *sync.Mutex {
	key := filepath.Clean(worktreePath)
	actual, _ := worktreeMutationLocks.LoadOrStore(key, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// runGitMutationWithIndexLockRetry tolerates a short-lived lock held by
// another Git process. It never removes the lock: a lock that remains beyond
// the retry window is reported with Git's original diagnostic.
func runGitMutationWithIndexLockRetry(worktreePath string, args ...string) ([]byte, error) {
	deadline := time.Now().Add(indexLockRetryWindow)
	delay := indexLockRetryInitialDelay
	for {
		cmdArgs := append([]string{"-C", worktreePath}, args...)
		out, err := exec.Command("git", cmdArgs...).CombinedOutput()
		if err == nil || !isIndexLockContention(out) || !time.Now().Before(deadline) {
			return out, err
		}

		remaining := time.Until(deadline)
		if delay > remaining {
			delay = remaining
		}
		time.Sleep(delay)
		if delay < indexLockRetryMaxDelay {
			delay *= 2
			if delay > indexLockRetryMaxDelay {
				delay = indexLockRetryMaxDelay
			}
		}
	}
}

func isIndexLockContention(out []byte) bool {
	msg := strings.ToLower(string(out))
	return strings.Contains(msg, "index.lock") &&
		(strings.Contains(msg, "file exists") || strings.Contains(msg, "unable to create"))
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

// ClosePR closes a GitHub PR by URL.
// Errors are returned but callers should treat them as non-fatal.
func ClosePR(prURL string) error {
	owner, repo, number, err := ParsePRURL(prURL)
	if err != nil {
		return err
	}
	client, err := github.ForHost(prURLHost(prURL))
	if err != nil {
		return err
	}
	if err := client.ClosePR(owner, repo, number); err != nil {
		return fmt.Errorf("closing PR: %w", err)
	}
	return nil
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

// resolveBase returns the base ref to diff against. A matching origin
// remote-tracking branch takes precedence over the local branch because
// publish and completion diffs describe what a PR against the remote base
// would contain. Otherwise the supplied or detected local ref is used.
func resolveBase(worktreePath string, baseBranch ...string) string {
	base := ""
	if len(baseBranch) > 0 && baseBranch[0] != "" {
		base = baseBranch[0]
	} else {
		base = DefaultBranch(worktreePath)
	}
	remoteRef := "refs/remotes/origin/" + base
	if exec.Command("git", "-C", worktreePath, "show-ref", "--verify", "--quiet", remoteRef).Run() == nil {
		return "origin/" + base
	}
	return base
}
