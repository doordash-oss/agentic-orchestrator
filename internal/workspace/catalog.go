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

package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BrowseEntry is a directory entry annotated with repo discovery metadata.
type BrowseEntry struct {
	Name           string
	Path           string
	IsGitRepo      bool
	ChildRepoCount int
}

// BrowseSnapshot describes a directory and its immediate child directories.
type BrowseSnapshot struct {
	Path           string
	Entries        []BrowseEntry
	IsGitRepo      bool
	ChildRepoCount int
}

// Repository is a discovered git repository with the config key Agentico uses.
type Repository struct {
	Name string
	Path string
}

// IsGitRepo reports whether dir is a git repository or worktree.
func IsGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// Browse reads path and annotates its immediate child directories.
func Browse(path string, showHidden bool) BrowseSnapshot {
	path = resolvePath(path)
	snapshot := BrowseSnapshot{
		Path:           path,
		IsGitRepo:      IsGitRepo(path),
		ChildRepoCount: countImmediateRepos(path, showHidden),
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return snapshot
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		childPath := filepath.Join(path, name)
		isRepo := IsGitRepo(childPath)
		childRepoCount := 0
		if !isRepo {
			childRepoCount = countImmediateRepos(childPath, showHidden)
		}
		snapshot.Entries = append(snapshot.Entries, BrowseEntry{
			Name:           name,
			Path:           childPath,
			IsGitRepo:      isRepo,
			ChildRepoCount: childRepoCount,
		})
	}
	sort.Slice(snapshot.Entries, func(i, j int) bool {
		return snapshot.Entries[i].Name < snapshot.Entries[j].Name
	})
	return snapshot
}

// DiscoverReposFromRoots scans workspace roots and returns collision-safe repo keys.
func DiscoverReposFromRoots(roots []string, explicitRepoPaths map[string]string) []Repository {
	explicitNames := make(map[string]bool, len(explicitRepoPaths))
	for name, path := range explicitRepoPaths {
		if path != "" {
			explicitNames[name] = true
		}
	}

	type repoTuple struct {
		rootResolved string
		rootBasename string
		repoName     string
		repoPath     string
	}

	seenRoots := make(map[string]bool, len(roots))
	var tuples []repoTuple
	for _, root := range roots {
		expanded := ExpandHome(root)
		resolved, err := filepath.Abs(expanded)
		if err != nil {
			resolved = expanded
		}
		if seenRoots[resolved] {
			continue
		}
		seenRoots[resolved] = true

		if IsGitRepo(expanded) {
			repoName := filepath.Base(resolved)
			if !explicitNames[repoName] {
				parent := filepath.Dir(resolved)
				tuples = append(tuples, repoTuple{
					rootResolved: parent,
					rootBasename: filepath.Base(parent),
					repoName:     repoName,
					repoPath:     expanded,
				})
			}
			continue
		}

		entries, err := os.ReadDir(expanded)
		if err != nil {
			continue
		}
		rootBase := filepath.Base(resolved)
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			repoPath := filepath.Join(expanded, entry.Name())
			if !IsGitRepo(repoPath) {
				continue
			}
			repoName := entry.Name()
			if explicitNames[repoName] {
				continue
			}
			tuples = append(tuples, repoTuple{
				rootResolved: resolved,
				rootBasename: rootBase,
				repoName:     repoName,
				repoPath:     repoPath,
			})
		}
	}

	nameRoots := make(map[string]map[string]bool)
	for _, tuple := range tuples {
		if nameRoots[tuple.repoName] == nil {
			nameRoots[tuple.repoName] = make(map[string]bool)
		}
		nameRoots[tuple.repoName][tuple.rootResolved] = true
	}

	basenameCounts := make(map[string]map[string]bool)
	for _, tuple := range tuples {
		if len(nameRoots[tuple.repoName]) <= 1 {
			continue
		}
		if basenameCounts[tuple.repoName] == nil {
			basenameCounts[tuple.repoName] = make(map[string]bool)
		}
		basenameCounts[tuple.repoName][tuple.rootBasename] = true
	}

	repos := make([]Repository, 0, len(tuples))
	for _, tuple := range tuples {
		key := tuple.repoName
		if len(nameRoots[tuple.repoName]) > 1 {
			if len(basenameCounts[tuple.repoName]) == len(nameRoots[tuple.repoName]) {
				key = tuple.rootBasename + "/" + tuple.repoName
			} else {
				key = uniqueRootPrefix(tuple.rootResolved, nameRoots[tuple.repoName]) + "/" + tuple.repoName
			}
		}
		repos = append(repos, Repository{Name: key, Path: tuple.repoPath})
	}
	sort.Slice(repos, func(i, j int) bool {
		return repos[i].Name < repos[j].Name
	})
	return repos
}

func countImmediateRepos(path string, showHidden bool) int {
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !showHidden && strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.IsDir() && IsGitRepo(filepath.Join(path, entry.Name())) {
			count++
		}
	}
	return count
}

func resolvePath(path string) string {
	path = ExpandHome(path)
	if path == "" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			path = home
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// ExpandHome expands a leading "~" or "~/" in a path to the user's home directory.
func ExpandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		return home
	}
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	return filepath.Join(home, path[2:])
}

func uniqueRootPrefix(rootPath string, allRoots map[string]bool) string {
	parts := strings.Split(filepath.ToSlash(rootPath), "/")
	var cleaned []string
	for _, part := range parts {
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	if len(cleaned) == 0 {
		return rootPath
	}

	var others [][]string
	for root := range allRoots {
		if root == rootPath {
			continue
		}
		rootParts := strings.Split(filepath.ToSlash(root), "/")
		var rootCleaned []string
		for _, part := range rootParts {
			if part != "" {
				rootCleaned = append(rootCleaned, part)
			}
		}
		others = append(others, rootCleaned)
	}

	for depth := 1; depth <= len(cleaned); depth++ {
		suffix := cleaned[len(cleaned)-depth:]
		candidate := strings.Join(suffix, "/")
		unique := true
		for _, other := range others {
			if len(other) < depth {
				continue
			}
			otherSuffix := other[len(other)-depth:]
			if strings.Join(otherSuffix, "/") == candidate {
				unique = false
				break
			}
		}
		if unique {
			return candidate
		}
	}
	return strings.Join(cleaned, "/")
}
