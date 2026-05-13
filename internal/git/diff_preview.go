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

// WorkingTreeDiffPreviews returns compact per-file previews for the current
// working tree, including tracked, staged, unstaged, and untracked changes.
func WorkingTreeDiffPreviews(worktreePath string) ([]DiffPreview, error) {
	entries, err := statusEntries(worktreePath)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	previews := make([]DiffPreview, 0, len(entries))
	for _, entry := range entries {
		patch, err := previewPatchForEntry(worktreePath, entry)
		if err != nil {
			return nil, err
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
		sum := sha1.Sum([]byte(preview.Operation + "\x00" + preview.Path + "\x00" + preview.OldPath + "\x00" + preview.Patch))
		preview.Fingerprint = fmt.Sprintf("%x", sum[:])
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

func statusEntries(worktreePath string) ([]statusEntry, error) {
	cmd := exec.Command("git", "-C", worktreePath, "status", "--porcelain")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git status --porcelain: %s: %w", strings.TrimSpace(string(out)), err)
	}

	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n"), "\n")
	entries := make([]statusEntry, 0, len(lines))
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" || len(line) < 4 {
			continue
		}
		code := line[:2]
		rest := strings.TrimSpace(line[3:])
		entry := statusEntry{path: rest, operation: "update"}

		if code == "??" {
			entry.operation = "add"
			entries = append(entries, entry)
			continue
		}

		if strings.Contains(rest, " -> ") {
			parts := strings.SplitN(rest, " -> ", 2)
			if len(parts) == 2 {
				entry.oldPath = strings.TrimSpace(parts[0])
				entry.path = strings.TrimSpace(parts[1])
				entry.operation = "rename"
			}
		}

		switch {
		case strings.Contains(code, "R"):
			entry.operation = "rename"
		case strings.Contains(code, "D"):
			entry.operation = "delete"
		case strings.Contains(code, "A"):
			entry.operation = "add"
		default:
			entry.operation = "update"
		}

		entries = append(entries, entry)
	}
	return entries, nil
}

func previewPatchForEntry(worktreePath string, entry statusEntry) (string, error) {
	if entry.operation == "add" && entry.oldPath == "" {
		if untracked, err := untrackedFilePatch(worktreePath, entry.path); err == nil && untracked != "" {
			return untracked, nil
		}
	}

	args := []string{"-C", worktreePath, "diff", "--find-renames", "--no-ext-diff", "--unified=3", "HEAD", "--"}
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

// SingleFileDiffPreview returns a compact diff preview for a single file.
// The relPath must be relative to the worktree root.
// Returns (nil, nil) if the file has no changes.
func SingleFileDiffPreview(worktreePath, relPath string) (*DiffPreview, error) {
	entry, err := singleFileStatus(worktreePath, relPath)
	if err != nil || entry == nil {
		return nil, nil
	}
	patch, err := previewPatchForEntry(worktreePath, *entry)
	if err != nil || strings.TrimSpace(patch) == "" {
		return nil, nil
	}
	added, removed := countPatchLineChanges(patch)
	compact := compactPatch(patch)
	if compact == "" {
		return nil, nil
	}
	sum := sha1.Sum([]byte(entry.operation + "\x00" + relPath + "\x00\x00" + compact))
	return &DiffPreview{
		Path:         relPath,
		Operation:    entry.operation,
		AddedLines:   added,
		RemovedLines: removed,
		Patch:        compact,
		Fingerprint:  fmt.Sprintf("%x", sum[:]),
	}, nil
}

// singleFileStatus checks whether a specific file has changes and returns its status entry.
// Returns nil if the file has no changes.
func singleFileStatus(worktreePath, relPath string) (*statusEntry, error) {
	cmd := exec.Command("git", "-C", worktreePath, "status", "--porcelain", "--", relPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git status %s: %w", relPath, err)
	}
	line := strings.TrimRight(string(out), "\r\n")
	if strings.TrimSpace(line) == "" {
		return nil, nil
	}
	if len(line) < 4 {
		return nil, nil
	}
	code := line[:2]
	rest := strings.TrimSpace(line[3:])
	entry := &statusEntry{path: rest, operation: "update"}
	switch {
	case code == "??":
		entry.operation = "add"
	case strings.Contains(code, "D"):
		entry.operation = "delete"
	case strings.Contains(code, "A"):
		entry.operation = "add"
	case strings.Contains(code, "R"):
		entry.operation = "rename"
		if parts := strings.SplitN(rest, " -> ", 2); len(parts) == 2 {
			entry.oldPath = strings.TrimSpace(parts[0])
			entry.path = strings.TrimSpace(parts[1])
		}
	}
	return entry, nil
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
