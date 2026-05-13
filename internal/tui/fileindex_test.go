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
	"testing"
)

func TestFileIndex_Build(t *testing.T) {
	dir := t.TempDir()

	// Create nested directory structure:
	// dir/
	//   a.go
	//   src/
	//     main.go
	//     util/
	//       helper.go
	os.MkdirAll(filepath.Join(dir, "src", "util"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "util", "helper.go"), []byte("package util"), 0o644)

	fi := &FileIndex{}
	if err := fi.Build(dir); err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if !fi.Ready() {
		t.Fatal("Ready() should be true after Build()")
	}

	// Expect sorted relative paths with "/" suffix on directories
	want := []string{"a.go", "src/", "src/main.go", "src/util/", "src/util/helper.go"}
	if fi.Len() != len(want) {
		t.Fatalf("Len() = %d, want %d; paths = %v", fi.Len(), len(want), fi.paths)
	}
	for i, w := range want {
		if fi.paths[i] != w {
			t.Errorf("paths[%d] = %q, want %q", i, fi.paths[i], w)
		}
	}
}

func TestFileIndex_Build_SkipDirs(t *testing.T) {
	dir := t.TempDir()

	skipped := []string{"node_modules", ".git", "vendor", "__pycache__", "dist", "build", ".next", ".cache"}
	for _, name := range skipped {
		os.MkdirAll(filepath.Join(dir, name), 0o755)
		os.WriteFile(filepath.Join(dir, name, "shouldskip.txt"), []byte("x"), 0o644)
	}
	// One valid file
	os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("y"), 0o644)

	fi := &FileIndex{}
	if err := fi.Build(dir); err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if fi.Len() != 1 {
		t.Fatalf("Len() = %d, want 1 (only keep.txt)", fi.Len())
	}
	if fi.paths[0] != "keep.txt" {
		t.Errorf("paths[0] = %q, want \"keep.txt\"", fi.paths[0])
	}
}

func TestFileIndex_Build_SkipHiddenDirs(t *testing.T) {
	dir := t.TempDir()

	// Hidden directory should be skipped
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755)
	os.WriteFile(filepath.Join(dir, ".hidden", "secret.txt"), []byte("x"), 0o644)

	// Hidden file at root should be included
	os.WriteFile(filepath.Join(dir, ".dotfile"), []byte("y"), 0o644)

	// Normal file
	os.WriteFile(filepath.Join(dir, "normal.txt"), []byte("z"), 0o644)

	fi := &FileIndex{}
	if err := fi.Build(dir); err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Expect .dotfile and normal.txt (hidden dir's contents excluded)
	if fi.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", fi.Len())
	}

	found := make(map[string]bool)
	for _, p := range fi.paths {
		found[p] = true
	}
	if !found[".dotfile"] {
		t.Error("expected .dotfile to be indexed")
	}
	if !found["normal.txt"] {
		t.Error("expected normal.txt to be indexed")
	}
	if found[".hidden/secret.txt"] {
		t.Error("hidden dir content should be excluded")
	}
}

func TestFileIndex_Build_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	fi := &FileIndex{}
	if err := fi.Build(dir); err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if !fi.Ready() {
		t.Fatal("Ready() should be true after Build() on empty dir")
	}
	if fi.Len() != 0 {
		t.Errorf("Len() = %d, want 0", fi.Len())
	}
}

func TestFileIndex_Build_NonExistentDir(t *testing.T) {
	fi := &FileIndex{}
	err := fi.Build("/nonexistent/dir/that/does/not/exist")
	if err == nil {
		t.Fatal("Build() should return error for non-existent directory")
	}
}

func TestFileIndex_Search_SubstringMatch(t *testing.T) {
	fi := &FileIndex{}
	fi.paths = []string{"cmd/main.go", "cmd/main_test.go", "src/util.go", "README.md"}
	fi.lower = []string{"cmd/main.go", "cmd/main_test.go", "src/util.go", "readme.md"}
	fi.ready = true

	results := fi.Search("main", 5)
	if len(results) != 2 {
		t.Fatalf("Search(\"main\") returned %d results, want 2", len(results))
	}
	if results[0] != "cmd/main.go" {
		t.Errorf("results[0] = %q, want \"cmd/main.go\"", results[0])
	}
	if results[1] != "cmd/main_test.go" {
		t.Errorf("results[1] = %q, want \"cmd/main_test.go\"", results[1])
	}
}

func TestFileIndex_Search_CaseInsensitive(t *testing.T) {
	fi := &FileIndex{}
	fi.paths = []string{"docs/README.txt", "readme.md", "src/main.go"}
	fi.lower = []string{"docs/readme.txt", "readme.md", "src/main.go"}
	fi.ready = true

	results := fi.Search("README", 5)
	if len(results) != 2 {
		t.Fatalf("Search(\"README\") returned %d results, want 2", len(results))
	}
	if results[0] != "docs/README.txt" {
		t.Errorf("results[0] = %q, want \"docs/README.txt\"", results[0])
	}
	if results[1] != "readme.md" {
		t.Errorf("results[1] = %q, want \"readme.md\"", results[1])
	}
}

func TestFileIndex_Search_MaxResults(t *testing.T) {
	fi := &FileIndex{}
	fi.paths = []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"}
	fi.lower = []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"}
	fi.ready = true

	results := fi.Search(".go", 3)
	if len(results) != 3 {
		t.Fatalf("Search(\".go\", 3) returned %d results, want 3", len(results))
	}
}

func TestFileIndex_Search_EmptyQuery(t *testing.T) {
	fi := &FileIndex{}
	fi.paths = []string{"a.go", "b.go"}
	fi.lower = []string{"a.go", "b.go"}
	fi.ready = true

	results := fi.Search("", 5)
	if len(results) != 0 {
		t.Errorf("Search(\"\") returned %d results, want 0", len(results))
	}
}

func TestFileIndex_Search_NoMatches(t *testing.T) {
	fi := &FileIndex{}
	fi.paths = []string{"a.go", "b.go"}
	fi.lower = []string{"a.go", "b.go"}
	fi.ready = true

	results := fi.Search("zzz", 5)
	if len(results) != 0 {
		t.Errorf("Search(\"zzz\") returned %d results, want 0", len(results))
	}
}

func TestFileIndex_Ready(t *testing.T) {
	fi := &FileIndex{}
	if fi.Ready() {
		t.Error("Ready() should be false on zero value")
	}

	dir := t.TempDir()
	if err := fi.Build(dir); err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if !fi.Ready() {
		t.Error("Ready() should be true after Build()")
	}
}

func TestFileIndex_Search_BeforeBuild(t *testing.T) {
	fi := &FileIndex{}
	results := fi.Search("anything", 5)
	if len(results) != 0 {
		t.Errorf("Search before Build returned %d results, want 0", len(results))
	}
}

func TestTruncatePathLeft(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		maxLen int
		want   string
	}{
		{
			name:   "short_path_unchanged",
			path:   "src/main.go",
			maxLen: 20,
			want:   "src/main.go",
		},
		{
			name:   "exact_length",
			path:   "abcdefghij",
			maxLen: 10,
			want:   "abcdefghij",
		},
		{
			name:   "long_path_truncated",
			path:   "very/long/nested/path/to/some/file.go",
			maxLen: 20,
			want:   "...ted/path/to/some/file.go",
		},
		{
			name:   "very_short_max",
			path:   "some/long/path.go",
			maxLen: 5,
			want:   "...th.go",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncatePathLeft(tt.path, tt.maxLen)
			if len(tt.path) <= tt.maxLen {
				// Short paths should be unchanged
				if got != tt.path {
					t.Errorf("truncatePathLeft(%q, %d) = %q, want %q", tt.path, tt.maxLen, got, tt.path)
				}
			} else {
				// Long paths should start with "..."
				if got[:3] != "..." {
					t.Errorf("truncatePathLeft(%q, %d) = %q, should start with \"...\"", tt.path, tt.maxLen, got)
				}
			}
		})
	}
}
