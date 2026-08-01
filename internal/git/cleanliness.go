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
	"os/exec"
	"strings"
)

// DefaultCleanlinessPathLimit caps each categorized path list in a
// CleanlinessReport so dirty-worktree diagnostics stay bounded.
const DefaultCleanlinessPathLimit = 50

// CleanlinessReport is the categorized result of inspecting a single git
// worktree. Staged / Unstaged / Untracked hold at most the requested number
// of paths each; the *Total fields always report full counts so callers can
// surface truncation. Ignored paths are excluded by git itself
// (git status --porcelain skips them without --ignored).
type CleanlinessReport struct {
	Staged         []string
	Unstaged       []string
	Untracked      []string
	StagedTotal    int
	UnstagedTotal  int
	UntrackedTotal int
}

// Dirty reports whether any category carries at least one path.
func (r *CleanlinessReport) Dirty() bool {
	if r == nil {
		return false
	}
	return r.StagedTotal > 0 || r.UnstagedTotal > 0 || r.UntrackedTotal > 0
}

// InspectCleanliness reports staged, unstaged, and untracked changes in the
// given worktree using `git status --porcelain --untracked-files=all`.
// Ignored files are absent (porcelain excludes them without --ignored).
// --untracked-files=all expands untracked directories so every nested file is
// counted and listed individually (no collapsed `dir/` entries); totals and
// bounded lists therefore reflect affected paths, not directory entries. Each
// category list is bounded to maxPerCategory entries (<= 0 applies
// DefaultCleanlinessPathLimit) while totals keep the true counts.
func (w *WorktreeManager) InspectCleanliness(worktreePath string, maxPerCategory int) (*CleanlinessReport, error) {
	if strings.TrimSpace(worktreePath) == "" {
		return nil, fmt.Errorf("worktree path is required")
	}
	if maxPerCategory <= 0 {
		maxPerCategory = DefaultCleanlinessPathLimit
	}
	cmd := exec.Command("git", "-C", worktreePath, "status", "--porcelain", "--untracked-files=all")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("checking git status in %s: %w", worktreePath, err)
	}
	report := &CleanlinessReport{}
	appendBounded := func(list []string, total int, path string) ([]string, int) {
		total++
		if len(list) < maxPerCategory {
			list = append(list, path)
		}
		return list, total
	}
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		x, y := line[0], line[1]
		path := strings.TrimSpace(line[3:])
		// Renames/copies record "orig -> new"; the new path is what matters.
		if idx := strings.LastIndex(path, " -> "); idx >= 0 {
			path = path[idx+4:]
		}
		path = strings.Trim(path, `"`)
		if path == "" {
			continue
		}
		if x == '?' && y == '?' {
			report.Untracked, report.UntrackedTotal = appendBounded(report.Untracked, report.UntrackedTotal, path)
			continue
		}
		if x != ' ' && x != '?' {
			report.Staged, report.StagedTotal = appendBounded(report.Staged, report.StagedTotal, path)
		}
		if y != ' ' && y != '?' {
			report.Unstaged, report.UnstagedTotal = appendBounded(report.Unstaged, report.UnstagedTotal, path)
		}
	}
	return report, nil
}
