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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// MergeInProgress reports whether a merge is underway in the worktree at
// worktreePath. An in-progress merge leaves a MERGE_HEAD file under the
// worktree's resolved git directory until `git merge --continue` /
// `git commit` / `git merge --abort` clears it. The probe mirrors the
// existing rebase-in-progress probe: it reports conservatively (false) on
// any error resolving or stating the git directory.
func MergeInProgress(worktreePath string) bool {
	gitDir := resolveGitDir(worktreePath)
	if _, err := os.Stat(filepath.Join(gitDir, "MERGE_HEAD")); err != nil {
		return false
	}
	return true
}

// ConflictMarkerFiles lists the tracked files in the worktree at
// worktreePath that contain literal git conflict marker lines. The scan
// matches the `git grep`-based contract the generated rebase-child prompt and
// exit criteria already impose: the three conflict markers (start, middle
// separator, end) searched as fixed strings. It is a literal marker scan, not
// an unmerged-index check — after the transaction commits child changes,
// `git diff --name-only --diff-filter=U` is vacuous, so only a content scan
// can prove a worktree is free of markers.
//
// The marker patterns are constructed from split strings so this source file
// does not itself contain the literal marker sequences and false-positive on
// its own content.
//
// Untracked files are ignored: `git grep` searches only tracked files. A clean
// tree returns an empty slice with a nil error. A git failure returns a nil
// slice and the error so callers can fail closed.
func ConflictMarkerFiles(worktreePath string) ([]string, error) {
	// Construct the marker patterns from split strings so this source file
	// does not match its own scan.
	start := "<" + "<<<<" + "<< "
	mid := "=" + "=====" + "="
	end := ">" + ">>>>" + ">> "

	cmd := exec.Command("git", "-C", worktreePath, "grep", "-l",
		"-e", start, "-e", mid, "-e", end, "--", ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// git grep exits 1 when there are no matches: that is the clean-tree
		// case, not an error. Distinguish it from a real failure by checking
		// the exit code via the typed *exec.ExitError.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, &ConflictMarkerScanError{WorktreePath: worktreePath, Err: err, Output: string(out)}
	}

	files := parseGrepFiles(string(out))
	sort.Strings(files)
	return files, nil
}

// ConflictMarkerScanError records a failure scanning a worktree for conflict
// markers. Callers should fail closed on a non-nil scan error.
type ConflictMarkerScanError struct {
	WorktreePath string
	Err          error
	Output       string
}

func (e *ConflictMarkerScanError) Error() string {
	return "scanning " + e.WorktreePath + " for conflict markers: " + strings.TrimSpace(e.Output) + ": " + e.Err.Error()
}

func (e *ConflictMarkerScanError) Unwrap() error {
	return e.Err
}

// parseGrepFiles extracts the unique file paths from `git grep -l` output.
// With -l, git grep prints one path per matching file (de-duplicated by git
// itself across patterns); de-duplicate defensively anyway.
func parseGrepFiles(output string) []string {
	seen := make(map[string]bool)
	var files []string
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		path := line
		if seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	return files
}
