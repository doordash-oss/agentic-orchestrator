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
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
)

// Phase 1: Cheap extractor tests

func TestExtractRepoFeatures_EmptyRepo(t *testing.T) {
	dir := t.TempDir()
	doc := extractDocumentation(dir)
	if doc != "" {
		t.Errorf("expected empty documentation, got %q", doc)
	}
	deps, modPath := extractDependencies(dir)
	if len(deps) != 0 {
		t.Errorf("expected no deps, got %v", deps)
	}
	if modPath != "" {
		t.Errorf("expected empty module path, got %q", modPath)
	}
	dirs := extractDirectories(dir)
	if len(dirs) != 0 {
		t.Errorf("expected no dirs, got %v", dirs)
	}
	exts := extractFileExtensions(dir)
	if len(exts) != 0 {
		t.Errorf("expected no exts, got %v", exts)
	}
}

func TestExtractRepoFeatures_MissingDocs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/test\n\ngo 1.24\n")
	doc := extractDocumentation(dir)
	if doc != "" {
		t.Errorf("expected empty documentation, got %q", doc)
	}
}

func TestExtractDependencies_GoMod(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", `module github.com/example/myproject

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

	// Each dep should have last-two-segments 3x and last-segment 3x
	assertContainsN(t, deps, "charmbracelet/bubbletea", 3)
	assertContainsN(t, deps, "bubbletea", 3)
	assertContainsN(t, deps, "creack/pty", 3)
	assertContainsN(t, deps, "pty", 3)
	assertContainsN(t, deps, "yaml.v3", 3)
}

func TestExtractDependencies_PackageJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
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
	assertContainsN(t, deps, "node", 3) // stripped @types/ scope
	assertContainsN(t, deps, "typescript", 3)
}

func TestExtractDependencies_NoDeps(t *testing.T) {
	dir := t.TempDir()
	deps, modPath := extractDependencies(dir)
	if len(deps) != 0 {
		t.Errorf("expected no deps, got %v", deps)
	}
	if modPath != "" {
		t.Errorf("expected empty module path, got %q", modPath)
	}
}

func TestExtractDirectories(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"cmd", "internal", "src", ".git", ".hidden"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dirs := extractDirectories(dir)
	assertContains(t, dirs, "dir_cmd")
	assertContains(t, dirs, "dir_internal")
	assertContains(t, dirs, "dir_src")
	assertNotContains(t, dirs, "dir_.git")
	assertNotContains(t, dirs, "dir_.hidden")
}

func TestExtractFileExtensions(t *testing.T) {
	dir := t.TempDir()
	// Create nested structure
	os.MkdirAll(filepath.Join(dir, "cmd"), 0o755)
	os.MkdirAll(filepath.Join(dir, "internal"), 0o755)
	os.MkdirAll(filepath.Join(dir, "vendor", "lib"), 0o755)

	writeFile(t, dir, "cmd/main.go", "package main")
	writeFile(t, dir, "internal/app.go", "package internal")
	writeFile(t, dir, "internal/app_test.go", "package internal")
	writeFile(t, dir, "README.md", "# README")
	writeFile(t, dir, "vendor/lib/dep.go", "package lib")
	writeFile(t, dir, "Makefile", "build:\n\tgo build")

	exts := extractFileExtensions(dir)
	if exts["go"] != 3 { // cmd/main.go, internal/app.go, internal/app_test.go (vendor excluded)
		t.Errorf("expected 3 .go files, got %d", exts["go"])
	}
	if exts["md"] != 1 {
		t.Errorf("expected 1 .md file, got %d", exts["md"])
	}
}

func TestExtractRepoFeatures_GoRepo(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "cmd"), 0o755)
	os.MkdirAll(filepath.Join(dir, "internal"), 0o755)

	writeFile(t, dir, "CLAUDE.md", "# My Project\n\n## Overview\nA cool project.")
	writeFile(t, dir, "go.mod", `module github.com/example/myproject

go 1.24

require (
	github.com/charmbracelet/bubbletea v1.3.10
)
`)
	writeFile(t, dir, "cmd/main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, dir, "internal/app.go", "package internal\n\nfunc Run() {}\n")

	doc := extractDocumentation(dir)
	if doc == "" {
		t.Error("expected non-empty documentation")
	}
	deps, modPath := extractDependencies(dir)
	if modPath != "github.com/example/myproject" {
		t.Errorf("unexpected module path: %q", modPath)
	}
	if len(deps) == 0 {
		t.Error("expected non-empty deps")
	}
	dirs := extractDirectories(dir)
	assertContains(t, dirs, "dir_cmd")
	assertContains(t, dirs, "dir_internal")
	exts := extractFileExtensions(dir)
	if exts["go"] < 2 {
		t.Errorf("expected at least 2 .go files, got %d", exts["go"])
	}
}

// Phase 2: Build/config extraction + ToText tests

func TestExtractBuildTargets_Makefile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", `BINARY := myapp
.PHONY: build test clean

build:
	go build ./...

test:
	go test ./...

clean:
	rm -rf bin/
`)
	targets := extractBuildTargets(dir)
	assertContains(t, targets, "target_build")
	assertContains(t, targets, "target_test")
	assertContains(t, targets, "target_clean")
	// Should not contain BINARY (variable assignment) or .PHONY
	assertNotContains(t, targets, "target_BINARY")
	assertNotContains(t, targets, "target_.PHONY")
}

func TestExtractBuildTargets_Taskfile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Taskfile.yml", `version: '3'

tasks:
  build:
    cmds:
      - go build ./...
  test:
    cmds:
      - go test ./...
  lint:
    cmds:
      - golangci-lint run
`)
	targets := extractBuildTargets(dir)
	assertContains(t, targets, "target_build")
	assertContains(t, targets, "target_test")
	assertContains(t, targets, "target_lint")
}

func TestExtractConfigSignals_Dockerfile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile", `FROM golang:1.24 AS builder
RUN go build ./...

FROM alpine:3.19
COPY --from=builder /app /app
`)
	signals := extractConfigSignals(dir)
	assertContains(t, signals, "docker_golang")
	assertContains(t, signals, "docker_alpine")
}

func TestExtractConfigSignals_DevboxJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "devbox.json", `{"packages": ["go@1.24", "nodejs@20"]}`)
	signals := extractConfigSignals(dir)
	assertContains(t, signals, "devbox_go")
	assertContains(t, signals, "devbox_nodejs")
}

func TestExtractConfigSignals_ToolVersions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".tool-versions", "golang 1.24.3\nnodejs 20.11.0\n")
	signals := extractConfigSignals(dir)
	assertContains(t, signals, "tool_golang")
	assertContains(t, signals, "tool_nodejs")
	assertContains(t, signals, "toolver_golang_1")
	assertContains(t, signals, "toolver_nodejs_20")
}

func TestToText(t *testing.T) {
	rf := &RepoFeatures{
		Documentation:  "# Project\nA cool project.",
		Dependencies:   []string{"bubbletea", "bubbletea", "bubbletea"},
		ModulePath:     "github.com/example/myproject",
		Directories:    []string{"dir_cmd", "dir_internal"},
		FileExtensions: map[string]int{"go": 50, "md": 5},
		BuildTargets:   []string{"target_build", "target_test"},
		ConfigSignals:  []string{"docker_golang"},
		SourceIdents:   []string{"go_ExportedFunc", "go_ExportedType"},
		CommitMessages: []string{"Initial commit", "Add feature"},
	}

	text := ToText(rf)
	if !strings.Contains(text, "# Project") {
		t.Error("expected documentation in output")
	}
	if !strings.Contains(text, "bubbletea") {
		t.Error("expected dependencies in output")
	}
	if !strings.Contains(text, "github.com/example/myproject") {
		t.Error("expected module path in output")
	}
	if !strings.Contains(text, "dir_cmd") {
		t.Error("expected directories in output")
	}
	// go extension: 50/10 = 5 repetitions
	if strings.Count(text, "lang_go") != 5 {
		t.Errorf("expected 5 lang_go tokens, got %d", strings.Count(text, "lang_go"))
	}
	// md extension: 5/10 = 0 repetitions
	if strings.Contains(text, "lang_md") {
		t.Error("expected no lang_md tokens (count < 10)")
	}
	if !strings.Contains(text, "target_build") {
		t.Error("expected build targets in output")
	}
	if !strings.Contains(text, "docker_golang") {
		t.Error("expected config signals in output")
	}
	if !strings.Contains(text, "go_ExportedFunc") {
		t.Error("expected source identifiers in output")
	}
	if !strings.Contains(text, "Initial commit") {
		t.Error("expected commit messages in output")
	}
}

func TestToText_Empty(t *testing.T) {
	rf := &RepoFeatures{
		FileExtensions: map[string]int{},
	}
	text := ToText(rf)
	if text == "" {
		t.Error("expected non-empty string (at least newlines)")
	}
	// Should not panic — that's the main assertion
}

// Phase 3: Time-budgeted operations + orchestrators

func TestExtractSourceIdentifiers_Go(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", `package main

func ExportedFunc() {}
func unexportedFunc() {}
type ExportedType struct{}
type unexportedType struct{}
`)
	idents := extractSourceIdentifiers(dir, 10*time.Second)
	assertContains(t, idents, "go_ExportedFunc")
	assertContains(t, idents, "go_ExportedType")
	assertContains(t, idents, "go_main") // package name
	assertNotContains(t, idents, "go_unexportedFunc")
	assertNotContains(t, idents, "go_unexportedType")
}

func TestExtractSourceIdentifiers_TypeScript(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "index.ts", `export function fetchData() {}
export class UserService {}
export const API_URL = "https://example.com"
export interface Config {}
export type Result = {}
function internalFn() {}
`)
	idents := extractSourceIdentifiers(dir, 10*time.Second)
	assertContains(t, idents, "ts_fetchData")
	assertContains(t, idents, "ts_UserService")
	assertContains(t, idents, "ts_API_URL")
	assertContains(t, idents, "ts_Config")
	assertContains(t, idents, "ts_Result")
	assertNotContains(t, idents, "ts_internalFn")
}

func TestExtractSourceIdentifiers_Python(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.py", `def public_function():
    pass

class MyClass:
    def method(self):
        pass

    def nested():
        pass
`)
	idents := extractSourceIdentifiers(dir, 10*time.Second)
	assertContains(t, idents, "py_public_function")
	assertContains(t, idents, "py_MyClass")
	assertNotContains(t, idents, "py_method")
	assertNotContains(t, idents, "py_nested")
}

func TestExtractSourceIdentifiers_BudgetRespected(t *testing.T) {
	dir := t.TempDir()
	// Create many Go files to ensure budget is exceeded
	for i := range 300 {
		content := "package main\n\nfunc ExportedFunc" + itoa(i) + "() {}\n"
		writeFile(t, dir, "file"+itoa(i)+".go", content)
	}

	budget := 20 * time.Millisecond
	start := time.Now()
	idents := extractSourceIdentifiers(dir, budget)
	elapsed := time.Since(start)

	// Should return within reasonable time (budget + slack)
	if elapsed > 2*time.Second {
		t.Errorf("expected completion within 2s, took %v", elapsed)
	}
	// Should have some results (partial extraction)
	if len(idents) == 0 {
		t.Error("expected at least some identifiers")
	}
	t.Logf("extracted %d identifiers in %v with %v budget", len(idents), elapsed, budget)
}

func TestExtractSourceIdentifiers_SkipsDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "vendor"), 0o755)
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)

	writeFile(t, dir, "vendor/dep.go", "package dep\n\nfunc VendorFunc() {}\n")
	writeFile(t, dir, "node_modules/mod.js", "export function ModFunc() {}\n")
	writeFile(t, dir, ".git/hook.go", "package hook\n\nfunc HookFunc() {}\n")
	writeFile(t, dir, "main.go", "package main\n\nfunc MainFunc() {}\n")

	idents := extractSourceIdentifiers(dir, 10*time.Second)
	assertContains(t, idents, "go_MainFunc")
	assertNotContains(t, idents, "go_VendorFunc")
	assertNotContains(t, idents, "ts_ModFunc")
	assertNotContains(t, idents, "go_HookFunc")
}

func TestExtractCommitMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping git test in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "commit", "--allow-empty", "-m", "First commit")
	runGit(t, dir, "commit", "--allow-empty", "-m", "Second commit")

	messages := extractCommitMessages(context.Background(), &execCommandRunner{}, dir, 5*time.Second)
	if len(messages) < 2 {
		t.Fatalf("expected at least 2 commit messages, got %d", len(messages))
	}
	// Most recent first
	if messages[0] != "Second commit" {
		t.Errorf("expected first message to be 'Second commit', got %q", messages[0])
	}
	if messages[1] != "First commit" {
		t.Errorf("expected second message to be 'First commit', got %q", messages[1])
	}
}

func TestExtractRepoFeatures_NodeRepo(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)

	writeFile(t, dir, "package.json", `{
  "name": "my-node-app",
  "dependencies": {
    "express": "^4.0.0",
    "react": "^18.0.0"
  },
  "devDependencies": {
    "typescript": "^5.0.0"
  }
}`)
	writeFile(t, dir, "README.md", "# My Node App\n\nA web application.")
	writeFile(t, dir, "src/index.ts", `export function main() {}
export class App {}
`)

	rf, err := ExtractRepoFeatures(context.Background(), &execCommandRunner{}, "my-node-app", dir, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if rf.Documentation == "" {
		t.Error("expected non-empty documentation")
	}
	if rf.ModulePath != "my-node-app" {
		t.Errorf("expected module path my-node-app, got %q", rf.ModulePath)
	}
	if len(rf.Dependencies) == 0 {
		t.Error("expected non-empty dependencies")
	}
	assertContains(t, rf.Dependencies, "express")
	assertContains(t, rf.Directories, "dir_src")
	if rf.FileExtensions["ts"] < 1 {
		t.Error("expected at least 1 .ts file")
	}
	assertContains(t, rf.SourceIdents, "ts_main")
	assertContains(t, rf.SourceIdents, "ts_App")
	if rf.Language != "typescript" {
		t.Errorf("expected language typescript, got %q", rf.Language)
	}
}

func TestExtractRepoFeatures_PythonRepo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt", "flask>=2.0\nrequests==2.28.0\n")
	writeFile(t, dir, "app.py", `def create_app():
    pass

class Config:
    DEBUG = True
`)

	rf, err := ExtractRepoFeatures(context.Background(), &execCommandRunner{}, "my-python-app", dir, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(rf.Dependencies) == 0 {
		t.Error("expected non-empty dependencies")
	}
	assertContains(t, rf.Dependencies, "flask")
	assertContains(t, rf.Dependencies, "requests")
	assertContains(t, rf.SourceIdents, "py_create_app")
	assertContains(t, rf.SourceIdents, "py_Config")
	if rf.Language != "python" {
		t.Errorf("expected language python, got %q", rf.Language)
	}
}

func TestExtractAllRepoFeatures_Concurrent(t *testing.T) {
	repos := make(map[string]config.RepoConfig)
	for i := range 5 {
		dir := t.TempDir()
		writeFile(t, dir, "go.mod", "module example.com/repo"+itoa(i)+"\n\ngo 1.24\n")
		writeFile(t, dir, "main.go", "package main\n\nfunc Main() {}\n")
		repos["repo"+itoa(i)] = config.RepoConfig{Path: dir}
	}

	start := time.Now()
	results := ExtractAllRepoFeatures(context.Background(), &execCommandRunner{}, repos, 30*time.Second)
	elapsed := time.Since(start)

	if len(results) != 5 {
		t.Errorf("expected 5 results, got %d", len(results))
	}
	for name, rf := range results {
		if rf.RepoName != name {
			t.Errorf("expected repo name %q, got %q", name, rf.RepoName)
		}
	}
	t.Logf("concurrent extraction of %d repos took %v", len(results), elapsed)
}

func TestExtractRepoFeatures_AgenticRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-repo test in -short mode")
	}

	// Navigate to the agentic repo root
	repoPath, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// Verify it's the right repo
	if _, err := os.Stat(filepath.Join(repoPath, "CLAUDE.md")); err != nil {
		t.Skipf("agentic repo not found at %s: %v", repoPath, err)
	}

	rf, err := ExtractRepoFeatures(context.Background(), &execCommandRunner{}, "agentic", repoPath, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if rf.Documentation == "" {
		t.Error("expected non-empty documentation (CLAUDE.md exists)")
	}
	// Dependencies should contain bubbletea
	found := false
	for _, d := range rf.Dependencies {
		if strings.Contains(d, "bubbletea") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected dependencies to contain bubbletea")
	}
	assertContains(t, rf.Directories, "dir_cmd")
	assertContains(t, rf.Directories, "dir_internal")
	if rf.FileExtensions["go"] < 10 {
		t.Errorf("expected at least 10 .go files, got %d", rf.FileExtensions["go"])
	}
	if len(rf.SourceIdents) == 0 {
		t.Error("expected at least some go_ source identifiers")
	}
	hasGoPrefix := false
	for _, ident := range rf.SourceIdents {
		if strings.HasPrefix(ident, "go_") {
			hasGoPrefix = true
			break
		}
	}
	if !hasGoPrefix {
		t.Error("expected at least some go_ prefixed identifiers")
	}
	if rf.Language != "go" {
		t.Errorf("expected language go, got %q", rf.Language)
	}
	if rf.ExtractionTime <= 0 {
		t.Error("expected positive extraction time")
	}

	// Check build targets from Makefile
	assertContains(t, rf.BuildTargets, "target_build")

	t.Logf("extracted %d deps, %d dirs, %d idents, %d commits in %v",
		len(rf.Dependencies), len(rf.Directories), len(rf.SourceIdents), len(rf.CommitMessages), rf.ExtractionTime)
}

// Benchmarks

func BenchmarkExtractRepoFeatures_RealRepo(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in -short mode")
	}
	repoPath, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		b.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "CLAUDE.md")); err != nil {
		b.Skipf("agentic repo not found at %s", repoPath)
	}

	b.ReportAllocs()
	for range b.N {
		rf, err := ExtractRepoFeatures(context.Background(), &execCommandRunner{}, "agentic", repoPath, 10*time.Second)
		if err != nil {
			b.Fatal(err)
		}
		if rf.Documentation == "" {
			b.Fatal("empty documentation")
		}
	}
}

func BenchmarkToText(b *testing.B) {
	rf := &RepoFeatures{
		Documentation:  strings.Repeat("word ", 100),
		Dependencies:   make([]string, 50),
		ModulePath:     "github.com/example/project",
		Directories:    []string{"dir_cmd", "dir_internal", "dir_pkg", "dir_test"},
		FileExtensions: map[string]int{"go": 100, "ts": 50, "md": 10, "json": 20},
		BuildTargets:   []string{"target_build", "target_test", "target_lint"},
		ConfigSignals:  []string{"docker_golang", "tool_go"},
		SourceIdents:   make([]string, 100),
		CommitMessages: make([]string, 50),
	}
	for i := range rf.Dependencies {
		rf.Dependencies[i] = "dep" + itoa(i)
	}
	for i := range rf.SourceIdents {
		rf.SourceIdents[i] = "go_Func" + itoa(i)
	}
	for i := range rf.CommitMessages {
		rf.CommitMessages[i] = "commit message " + itoa(i)
	}

	b.ReportAllocs()
	for range b.N {
		_ = ToText(rf)
	}
}

// Test helpers

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func assertContains(t *testing.T, slice []string, want string) {
	t.Helper()
	for _, s := range slice {
		if s == want {
			return
		}
	}
	t.Errorf("expected slice to contain %q, got %v", want, slice)
}

func assertNotContains(t *testing.T, slice []string, unwant string) {
	t.Helper()
	for _, s := range slice {
		if s == unwant {
			t.Errorf("expected slice to NOT contain %q", unwant)
			return
		}
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

// itoa is a simple int-to-string helper to avoid importing strconv.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}
