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
	"testing"
)

func TestIsGitRepoAcceptsGitDirectoryAndWorktreeFile(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "regular")
	worktree := filepath.Join(root, "worktree")
	plain := filepath.Join(root, "plain")
	mkdirAll(t, filepath.Join(regular, ".git"))
	mkdirAll(t, worktree)
	mkdirAll(t, plain)
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: /tmp/main/.git/worktrees/worktree\n")

	if !IsGitRepo(regular) {
		t.Fatalf("IsGitRepo(%q) = false, want true for .git directory", regular)
	}
	if !IsGitRepo(worktree) {
		t.Fatalf("IsGitRepo(%q) = false, want true for .git file", worktree)
	}
	if IsGitRepo(plain) {
		t.Fatalf("IsGitRepo(%q) = true, want false without .git", plain)
	}
}

func TestBrowseAnnotatesRepoChildrenAndOneLevelRepoCounts(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "api", ".git"))
	mkdirAll(t, filepath.Join(root, "group", "web", ".git"))
	mkdirAll(t, filepath.Join(root, "empty"))
	mkdirAll(t, filepath.Join(root, ".hidden", ".git"))

	snapshot := Browse(root, false)

	if snapshot.Path != root {
		t.Fatalf("Browse path = %q, want %q", snapshot.Path, root)
	}
	if snapshot.ChildRepoCount != 1 {
		t.Fatalf("Browse child repo count = %d, want 1", snapshot.ChildRepoCount)
	}
	entries := browseEntriesByName(snapshot.Entries)
	if entries["api"].Path != filepath.Join(root, "api") || !entries["api"].IsGitRepo {
		t.Fatalf("api entry = %+v, want git repo at path", entries["api"])
	}
	if entries["group"].ChildRepoCount != 1 || entries["group"].IsGitRepo {
		t.Fatalf("group entry = %+v, want child repo count 1 and not direct repo", entries["group"])
	}
	if _, ok := entries[".hidden"]; ok {
		t.Fatalf("hidden entry present when showHidden=false: %+v", entries[".hidden"])
	}

	withHidden := browseEntriesByName(Browse(root, true).Entries)
	if !withHidden[".hidden"].IsGitRepo {
		t.Fatalf("hidden entry = %+v, want git repo when showHidden=true", withHidden[".hidden"])
	}
}

func TestDiscoverReposFromRootsHandlesWorktreeReposAndNameCollisions(t *testing.T) {
	parent := t.TempDir()
	rootA := filepath.Join(parent, "root-a")
	rootB := filepath.Join(parent, "root-b")
	mkdirAll(t, filepath.Join(rootA, "service"))
	mkdirAll(t, filepath.Join(rootB, "service"))
	writeFile(t, filepath.Join(rootA, "service", ".git"), "gitdir: /tmp/main/.git/worktrees/service-a\n")
	mkdirAll(t, filepath.Join(rootB, "service", ".git"))

	repos := DiscoverReposFromRoots([]string{rootA, rootB}, nil)
	byName := repositoriesByName(repos)

	if byName["root-a/service"] != filepath.Join(rootA, "service") {
		t.Fatalf("root-a/service path = %q, want %q; repos=%+v", byName["root-a/service"], filepath.Join(rootA, "service"), repos)
	}
	if byName["root-b/service"] != filepath.Join(rootB, "service") {
		t.Fatalf("root-b/service path = %q, want %q; repos=%+v", byName["root-b/service"], filepath.Join(rootB, "service"), repos)
	}
	if _, ok := byName["service"]; ok {
		t.Fatalf("plain service key present for colliding roots: %+v", repos)
	}
}

func browseEntriesByName(entries []BrowseEntry) map[string]BrowseEntry {
	byName := make(map[string]BrowseEntry, len(entries))
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	return byName
}

func repositoriesByName(repos []Repository) map[string]string {
	byName := make(map[string]string, len(repos))
	for _, repo := range repos {
		byName[repo.Name] = repo.Path
	}
	return byName
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
