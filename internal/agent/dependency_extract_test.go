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

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractDependencies_GoMod(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeDependencyTestFile(t, dir, "go.mod", `module github.com/example/myproject

go 1.24

require (
	github.com/charmbracelet/bubbletea v1.3.10
	github.com/creack/pty v1.1.24
	gopkg.in/yaml.v3 v3.0.1
)
`)
	deps, modPath := extractDependencies(dir)
	if modPath != "github.com/example/myproject" {
		t.Errorf("expected module path github.com/example/myproject, got %q", modPath)
	}

	assertContainsN(t, deps, "charmbracelet/bubbletea", 3)
	assertContainsN(t, deps, "bubbletea", 3)
	assertContainsN(t, deps, "creack/pty", 3)
	assertContainsN(t, deps, "pty", 3)
	assertContainsN(t, deps, "yaml.v3", 3)
}

func TestExtractDependencies_PackageJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeDependencyTestFile(t, dir, "package.json", `{
  "name": "my-app",
  "dependencies": {
    "react": "^18.0.0",
    "@types/node": "^20.0.0"
  },
  "devDependencies": {
    "typescript": "^5.0.0",
    "@testing-library/react": "^14.0.0"
  }
}`)
	deps, modPath := extractDependencies(dir)
	if modPath != "my-app" {
		t.Errorf("expected module path my-app, got %q", modPath)
	}
	assertContainsN(t, deps, "react", 3)
	assertContainsN(t, deps, "node", 3)
	assertContainsN(t, deps, "typescript", 3)
}

func TestExtractDependencies_Python(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeDependencyTestFile(t, dir, "pyproject.toml", `dependencies = [
  "requests>=2.0",
  "fastapi",
]`)
	deps, modPath := extractDependencies(dir)
	if modPath != "" {
		t.Errorf("expected empty module path, got %q", modPath)
	}
	assertContainsN(t, deps, "requests", 3)
	assertContainsN(t, deps, "fastapi", 3)
}

func TestExtractDependencies_RequirementsFallback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeDependencyTestFile(t, dir, "requirements.txt", "pytest==8.0\n# comment\n-r base.txt\nflask[async]>=3\n")
	deps, modPath := extractDependencies(dir)
	if modPath != "" {
		t.Errorf("expected empty module path, got %q", modPath)
	}
	assertContainsN(t, deps, "pytest", 3)
	assertContainsN(t, deps, "flask", 3)
}

func TestExtractDependencies_NoDeps(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	deps, modPath := extractDependencies(dir)
	if len(deps) != 0 {
		t.Errorf("expected no deps, got %v", deps)
	}
	if modPath != "" {
		t.Errorf("expected empty module path, got %q", modPath)
	}
}

func writeDependencyTestFile(t *testing.T, dir, name, content string) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertContainsN(t *testing.T, slice []string, want string, n int) {
	t.Helper()

	count := 0
	for _, s := range slice {
		if s == want {
			count++
		}
	}
	if count < n {
		t.Errorf("expected at least %d occurrences of %q, got %d", n, want, count)
	}
}
