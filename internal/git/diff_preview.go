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
	"crypto/sha1"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// DiffPreview aliases the port-native type so callers that already import
// the git package can keep their type references, while the canonical
// definition lives in internal/ports.
type DiffPreview = ports.DiffPreview

type statusEntry struct {
	path      string
	oldPath   string
	operation string
}

// BranchDiffPreviews returns compact per-file previews for the worktree's
// branch against the base branch. The diff captures both committed
// feature-branch changes and uncommitted working-tree changes — everything
// that would be published or merged if the branch were pushed and opened as
// a PR against the base. Untracked files appear as additions. The base
// branch is resolved via resolveBase when empty.
func BranchDiffPreviews(worktreePath, baseBranch string) ([]DiffPreview, error) {
	base := resolveBase(worktreePath, baseBranch)
	entries, err := branchDiffEntries(worktreePath, base)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	previews := make([]DiffPreview, 0, len(entries))
	for _, entry := range entries {
		patch, err := branchEntryPatch(worktreePath, base, entry)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(patch) == "" {
			continue
		}
		added, removed := countPatchLineChanges(patch)
		preview := DiffPreview{
			Path:         entry.path,
			OldPath:      entry.oldPath,
			Operation:    entry.operation,
			AddedLines:   added,
			RemovedLines: removed,
			Patch:        compactPatch(patch),
		}
		preview.Fingerprint = previewFingerprint(preview.Operation, preview.Path, preview.OldPath, preview.Patch)
		previews = append(previews, preview)
	}

	sort.Slice(previews, func(i, j int) bool {
		if previews[i].Path == previews[j].Path {
			return previews[i].Operation < previews[j].Operation
		}
		return previews[i].Path < previews[j].Path
	})
	return previews, nil
}

// branchDiffEntries lists files that differ between the working tree and the
// base branch (committed + uncommitted tracked changes), plus untracked
// files which git diff does not see.
func branchDiffEntries(worktreePath, base string) ([]statusEntry, error) {
	out, err := exec.Command("git", "-C", worktreePath, "diff", "--find-renames", "--no-ext-diff", "--name-status", base).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git diff --name-status %s: %s: %w", base, strings.TrimSpace(string(out)), err)
	}
	entries := parseNameStatus(string(out))
	untracked, err := untrackedEntries(worktreePath)
	if err != nil {
		return nil, err
	}
	return append(entries, untracked...), nil
}

func parseNameStatus(out string) []statusEntry {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(out, "\r\n", "\n"), "\n"), "\n")
	entries := make([]statusEntry, 0, len(lines))
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		entry := statusEntry{path: parts[1], operation: "update"}
		if len(parts) == 3 {
			entry.oldPath = parts[1]
			entry.path = parts[2]
			entry.operation = "rename"
		}
		switch {
		case strings.HasPrefix(parts[0], "R"):
			entry.operation = "rename"
		case strings.HasPrefix(parts[0], "D"):
			entry.operation = "delete"
		case strings.HasPrefix(parts[0], "A"):
			entry.operation = "add"
		default:
			entry.operation = "update"
		}
		entries = append(entries, entry)
	}
	return entries
}

func untrackedEntries(worktreePath string) ([]statusEntry, error) {
	out, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git status --porcelain: %s: %w", strings.TrimSpace(string(out)), err)
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n"), "\n")
	entries := make([]statusEntry, 0)
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if len(line) < 4 {
			continue
		}
		if line[:2] != "??" {
			continue
		}
		rest := strings.TrimSpace(line[3:])
		if rest == "" {
			continue
		}
		entries = append(entries, statusEntry{path: rest, operation: "add"})
	}
	return entries, nil
}

func branchEntryPatch(worktreePath, base string, entry statusEntry) (string, error) {
	if entry.operation == "add" && entry.oldPath == "" {
		if untracked, err := untrackedFilePatch(worktreePath, entry.path); err == nil && untracked != "" {
			return untracked, nil
		}
	}
	args := []string{"-C", worktreePath, "diff", "--find-renames", "--no-ext-diff", "--unified=3", base, "--"}
	if entry.oldPath != "" {
		args = append(args, entry.oldPath)
	}
	args = append(args, entry.path)
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff preview %s: %s: %w", entry.path, strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}

func untrackedFilePatch(worktreePath, relPath string) (string, error) {
	content, err := os.ReadFile(filepath.Join(worktreePath, relPath))
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var diff strings.Builder
	fmt.Fprintf(&diff, "diff --git a/%s b/%s\n", relPath, relPath)
	diff.WriteString("new file mode 100644\n")
	diff.WriteString("--- /dev/null\n")
	fmt.Fprintf(&diff, "+++ b/%s\n", relPath)
	fmt.Fprintf(&diff, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, line := range lines {
		diff.WriteString("+" + line + "\n")
	}
	return diff.String(), nil
}

func countPatchLineChanges(patch string) (added, removed int) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

// SingleFileDiffPreview returns a compact diff preview for a single file
// compared to the base branch. The relPath must be relative to the worktree
// root. Returns (nil, nil) if the file has no changes vs base.
func SingleFileDiffPreview(worktreePath, baseBranch, relPath string) (*DiffPreview, error) {
	base := resolveBase(worktreePath, baseBranch)
	entry, err := singleFileBranchStatus(worktreePath, base, relPath)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	patch, err := branchEntryPatch(worktreePath, base, *entry)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(patch) == "" {
		return nil, nil
	}
	added, removed := countPatchLineChanges(patch)
	compact := compactPatch(patch)
	if compact == "" {
		return nil, nil
	}
	return &DiffPreview{
		Path:         entry.path,
		OldPath:      entry.oldPath,
		Operation:    entry.operation,
		AddedLines:   added,
		RemovedLines: removed,
		Patch:        compact,
		Fingerprint:  previewFingerprint(entry.operation, entry.path, entry.oldPath, compact),
	}, nil
}

// singleFileBranchStatus checks whether a specific file differs from the base
// branch (tracked) or is untracked. Returns nil if the file has no changes.
func singleFileBranchStatus(worktreePath, base, relPath string) (*statusEntry, error) {
	out, err := exec.Command("git", "-C", worktreePath, "diff", "--find-renames", "--no-ext-diff", "--name-status", base, "--", relPath).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git diff --name-status %s -- %s: %s: %w", base, relPath, strings.TrimSpace(string(out)), err)
	}
	entries := parseNameStatus(string(out))
	if len(entries) > 0 {
		return &entries[0], nil
	}
	statusOut, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain", "--", relPath).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git status %s: %s: %w", relPath, strings.TrimSpace(string(statusOut)), err)
	}
	statusLine := strings.TrimRight(string(statusOut), "\r\n")
	if strings.TrimSpace(statusLine) == "" || len(statusLine) < 4 {
		return nil, nil
	}
	if statusLine[:2] == "??" {
		return &statusEntry{path: relPath, operation: "add"}, nil
	}
	return nil, nil
}

func previewFingerprint(operation, path, oldPath, patch string) string {
	sum := sha1.Sum([]byte(operation + "\x00" + path + "\x00" + oldPath + "\x00" + patch))
	return fmt.Sprintf("%x", sum[:])
}

func compactPatch(patch string) string {
	if strings.TrimSpace(patch) == "" {
		return ""
	}
	lines := strings.Split(patch, "\n")
	kept := make([]string, 0, len(lines))
	hunksSeen := 0
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git"),
			strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "new file mode "),
			strings.HasPrefix(line, "deleted file mode "),
			strings.HasPrefix(line, "similarity index "),
			strings.HasPrefix(line, "rename from "),
			strings.HasPrefix(line, "rename to "):
			continue
		case strings.HasPrefix(line, "@@"):
			hunksSeen++
			if hunksSeen > 4 {
				kept = append(kept, "...")
				return strings.TrimSpace(strings.Join(kept, "\n"))
			}
		}
		kept = append(kept, line)
		if len(kept) >= 30 {
			kept = append(kept, "...")
			break
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
