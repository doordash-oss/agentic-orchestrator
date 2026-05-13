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

package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// skipDirsFileIndex defines directories to exclude from file indexing.
var skipDirsFileIndex = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"__pycache__":  true,
	"dist":         true,
	"build":        true,
	".next":        true,
	".cache":       true,
}

// FileIndex holds a pre-built index of relative file paths for substring search.
type FileIndex struct {
	paths []string // sorted relative paths
	lower []string // pre-lowercased paths for case-insensitive search
	ready bool
}

// Build walks rootDir and populates the index with relative file paths.
func (fi *FileIndex) Build(rootDir string) error {
	var paths []string

	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		name := d.Name()

		if d.IsDir() {
			// Skip the root itself
			if path == rootDir {
				return nil
			}
			// Skip known directories
			if skipDirsFileIndex[name] {
				return filepath.SkipDir
			}
			// Skip hidden directories (but not the root)
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			// Index the directory with a trailing "/" suffix
			rel, relErr := filepath.Rel(rootDir, path)
			if relErr != nil {
				return nil
			}
			paths = append(paths, rel+"/")
			return nil
		}

		rel, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return err
	}

	sort.Strings(paths)

	lower := make([]string, len(paths))
	for i, p := range paths {
		lower[i] = strings.ToLower(p)
	}

	fi.paths = paths
	fi.lower = lower
	fi.ready = true
	return nil
}

// Search returns up to max file paths that contain query as a case-insensitive substring.
// Returns nil for an empty query or if the index has not been built.
func (fi *FileIndex) Search(query string, max int) []string {
	if query == "" || !fi.ready {
		return nil
	}

	lowerQuery := strings.ToLower(query)
	var results []string
	for i, lp := range fi.lower {
		if strings.Contains(lp, lowerQuery) {
			results = append(results, fi.paths[i])
			if len(results) >= max {
				break
			}
		}
	}
	return results
}

// Ready reports whether the index has been built.
func (fi *FileIndex) Ready() bool {
	return fi.ready
}

// Len returns the number of indexed paths.
func (fi *FileIndex) Len() int {
	return len(fi.paths)
}

// fileIndexReadyMsg carries the built index back to the Update loop.
type fileIndexReadyMsg struct {
	index   *FileIndex
	workDir string
}

// buildFileIndexCmd returns a tea.Cmd that builds the index in a goroutine.
func buildFileIndexCmd(workDir string) tea.Cmd {
	return func() tea.Msg {
		fi := &FileIndex{}
		_ = fi.Build(workDir)
		return fileIndexReadyMsg{index: fi, workDir: workDir}
	}
}

// truncatePathLeft truncates long paths with "..." prefix, keeping the end visible.
func truncatePathLeft(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	// Keep the tail, prepend "..."
	tail := path[len(path)-(maxLen-3):]
	return "..." + tail
}
