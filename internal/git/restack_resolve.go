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
	"sort"
	"strings"
)

// This file owns the resumable conflict handling of the restack primitive:
// the optional resolver contract, the non-interactive continuation of a
// resolved cherry-pick, and the worktree-scoped helpers the orchestrator's
// resolver needs to verify and clean a session's work (marker scan, outside-
// set change listing, revert, and the prompt-facing diff).

// RestackResolution is the outcome of one conflict-resolution engagement a
// resolver reports back to the restack primitive.
type RestackResolution int

const (
	// RestackResolutionResolved means the conflicted files are marker-free
	// and the worktree holds no other change: the primitive stages the
	// conflicted files and continues the cherry-pick.
	RestackResolutionResolved RestackResolution = iota
	// RestackResolutionExhausted means the resolver's attempt budget ran
	// out: the primitive aborts the pick and surfaces the conflict error
	// extended with the attempt count.
	RestackResolutionExhausted
)

// RestackResolverInput carries everything a conflict resolver needs about one
// conflicting cherry-pick: the temporary worktree the pick is paused in (the
// resolver's working directory), the repository the chain lives in, the
// bounding segment labels, the commit, the ordered conflicted paths, and the
// attempt-directory root the caller supplied.
type RestackResolverInput struct {
	WorktreePath  string
	MainRepo      string
	SegmentFrom   string
	SegmentTo     string
	CommitSHA     string
	ConflictFiles []string
	AttemptRoot   string
}

// RestackResolverResult reports the engagement's outcome. Attempts is the
// number of resolution attempts used; LastFailure and AttemptDir describe the
// final failed attempt for an exhausted budget.
type RestackResolverResult struct {
	Resolution  RestackResolution
	Attempts    int
	LastFailure string
	AttemptDir  string
}

// RestackConflictResolver resolves one conflicting cherry-pick inside the
// restack primitive's temporary worktree. It returns resolved, exhausted, or
// an error; an error aborts the pick and fails the restack run without
// surfacing a conflict.
type RestackConflictResolver func(input RestackResolverInput) (RestackResolverResult, error)

// RestackChainWithResolver runs a restack with an optional conflict
// resolver. A conflicting cherry-pick is left in progress inside the
// primitive's temporary worktree and handed to the resolver together with the
// attempt-directory root; a resolved result re-scans the conflicted files for
// markers, stages them, and continues the pick non-interactively so the
// original author, message, and trailers are preserved. A resolver that
// reports exhaustion surfaces the conflict error extended with the attempt
// count; a resolver error propagates with the pick aborted. A nil resolver
// keeps RestackChain's abort-on-conflict behavior.
func RestackChainWithResolver(mainRepo string, cutPoints []RestackCutPoint, ops []RestackOp, resolver RestackConflictResolver, attemptsRoot string) (*RestackResult, error) {
	return restackChain(mainRepo, cutPoints, ops, resolver, attemptsRoot)
}

// ConflictMarkerFilesInPaths lists the paths in the given set whose worktree
// content still carries git conflict marker lines. A path counts only when
// all three marker forms (start, middle separator, end) are present, so
// ordinary dividers do not trip the scan; the patterns are built from split
// strings so this file cannot match its own scan. Unmerged-index state is
// irrelevant — the scan reads worktree content — and a missing path is
// skipped (a resolver may legitimately delete a conflicted file).
func ConflictMarkerFilesInPaths(worktreePath string, paths []string) ([]string, error) {
	start := "^" + "<" + "<<<<" + "<< "
	mid := "^" + "=" + "=====" + "=$"
	end := "^" + ">" + ">>>>" + ">> "
	var marked []string
	for _, path := range paths {
		content, err := os.ReadFile(filepath.Join(worktreePath, filepath.FromSlash(path)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading %s in %s for the marker scan: %w", path, worktreePath, err)
		}
		if contentHasMarkerLine(content, start) && contentHasMarkerLine(content, mid) && contentHasMarkerLine(content, end) {
			marked = append(marked, path)
		}
	}
	return marked, nil
}

// contentHasMarkerLine reports whether one of the content's lines matches the
// anchored marker expression.
func contentHasMarkerLine(content []byte, expr string) bool {
	body := strings.TrimPrefix(expr, "^")
	anchored := strings.HasSuffix(expr, "$")
	if anchored {
		body = strings.TrimSuffix(body, "$")
	}
	for _, line := range strings.Split(string(content), "\n") {
		if anchored {
			if line == body {
				return true
			}
			continue
		}
		if strings.HasPrefix(line, body) {
			return true
		}
	}
	return false
}

// ChangedPathsOutsideSet lists every path the worktree changed — tracked
// modifications, staged changes, and untracked files — that is not in the
// allowed set. The listing is sorted and deduplicated; rename records count
// on both the old and the new path. It is a pure listing: nothing is
// reverted.
func ChangedPathsOutsideSet(worktreePath string, allowed []string) ([]string, error) {
	allowedSet := make(map[string]bool, len(allowed))
	for _, p := range allowed {
		allowedSet[p] = true
	}
	cmd := readGitCmd(worktreePath, "status", "--porcelain", "-z", "--untracked-files=all")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing changes in %s: %w", worktreePath, err)
	}
	changed := make(map[string]bool)
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if len(field) < 3 {
			continue
		}
		status := field[:2]
		path := field[3:]
		changed[path] = true
		// Rename and copy records carry the original path in the next
		// field; it changed too.
		if status[0] == 'R' || status[0] == 'C' {
			if i+1 < len(fields) {
				changed[fields[i+1]] = true
				i++
			}
		}
	}
	var outside []string
	for path := range changed {
		if !allowedSet[path] {
			outside = append(outside, path)
		}
	}
	sort.Strings(outside)
	return outside, nil
}

// RevertPaths restores the given paths to the state HEAD records: tracked
// files are restored in the index and the worktree, and paths HEAD does not
// know are removed from the index when staged and deleted from disk,
// including now-empty parent directories up to the worktree root. Symlinks
// are removed like any other file. It is the harness-side cleanup for a
// resolution session that touched files outside the conflicted set.
func RevertPaths(worktreePath string, paths []string) error {
	root, err := filepath.Abs(worktreePath)
	if err != nil {
		return fmt.Errorf("resolving the worktree root %s: %w", worktreePath, err)
	}
	var tracked, untracked []string
	for _, path := range paths {
		cleaned := filepath.ToSlash(filepath.Clean(path))
		if cleaned == "" || cleaned == "." || strings.HasPrefix(cleaned, "../") || filepath.IsAbs(cleaned) {
			return fmt.Errorf("refusing to revert the out-of-worktree path %q", path)
		}
		cmd := readGitCmd(worktreePath, "cat-file", "-e", "HEAD:"+cleaned)
		if cmd.Run() == nil {
			tracked = append(tracked, cleaned)
		} else {
			untracked = append(untracked, cleaned)
		}
	}
	if len(tracked) > 0 {
		args := append([]string{"-C", worktreePath, "restore", "--source=HEAD", "--staged", "--worktree", "--"}, tracked...)
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("restoring tracked paths %v in %s: %s: %w", tracked, worktreePath, strings.TrimSpace(string(out)), err)
		}
	}
	for _, path := range untracked {
		// A staged addition must leave the index before the file goes.
		rmArgs := []string{"-C", worktreePath, "rm", "--cached", "--", path}
		if err := exec.Command("git", rmArgs...).Run(); err != nil {
			// Not staged: an untracked worktree file only.
			_ = err
		}
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(path))); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing the untracked path %s in %s: %w", path, worktreePath, err)
		}
		removeEmptyDirsAbove(filepath.Dir(filepath.Join(root, filepath.FromSlash(path))), root)
	}
	return nil
}

// removeEmptyDirsAbove removes dir and its parents while they are empty,
// stopping at (and never removing) the worktree root.
func removeEmptyDirsAbove(dir, root string) {
	for dir != root && strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// DiffPathsBetween renders the diff of the given path set between two
// commits, the upstream movement a conflict-resolution prompt shows next to
// the commit being replayed.
func DiffPathsBetween(repoPath, fromSHA, toSHA string, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	args := append([]string{"-C", repoPath, "diff", fromSHA, toSHA, "--"}, paths...)
	cmd := readGitCmd(repoPath, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("diffing %s..%s on %v in %s: %w", fromSHA, toSHA, paths, repoPath, err)
	}
	return string(out), nil
}

// CommitMessage returns one commit's full message (subject and body).
func CommitMessage(repoPath, commitSHA string) (string, error) {
	out, err := readGitCmd(repoPath, "show", "-s", "--format=%B", commitSHA).Output()
	if err != nil {
		return "", fmt.Errorf("reading the message of %s in %s: %w", commitSHA, repoPath, err)
	}
	return string(out), nil
}

// CommitPatch returns one commit's full patch, the change a
// conflict-resolution prompt shows next to the commit's message.
func CommitPatch(repoPath, commitSHA string) (string, error) {
	out, err := readGitCmd(repoPath, "show", "--format=", "--no-color", commitSHA).Output()
	if err != nil {
		return "", fmt.Errorf("reading the patch of %s in %s: %w", commitSHA, repoPath, err)
	}
	return string(out), nil
}

// RestackChainWithResolver exposes the resolver-aware restack primitive on
// the manager.
func (m *WorktreeManager) RestackChainWithResolver(mainRepo string, cutPoints []RestackCutPoint, ops []RestackOp, resolver RestackConflictResolver, attemptsRoot string) (*RestackResult, error) {
	return RestackChainWithResolver(mainRepo, cutPoints, ops, resolver, attemptsRoot)
}
