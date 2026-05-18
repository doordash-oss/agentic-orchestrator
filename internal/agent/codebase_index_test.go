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
)

func TestExtractSymbols_Go(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "main.go", `package main

func main() {}

func ExportedFunc(x int) string {
	return ""
}

type MyStruct struct {
	Name string
}

type MyInterface interface {
	Do()
}

func (m *MyStruct) ExportedMethod() {}

func unexported() {}

const Version = "1.0"

var GlobalVar = 42
`)

	syms := extractSymbols(dir, 10*time.Second)

	// Should find: ExportedFunc, MyStruct, MyInterface, ExportedMethod, Version, GlobalVar
	// Should NOT find: main, unexported
	symNames := make(map[string]SymbolEntry)
	for _, s := range syms {
		symNames[s.Name] = s
	}

	tests := []struct {
		name     string
		wantKind string
		wantIn   bool
	}{
		{"ExportedFunc", "func", true},
		{"MyStruct", "type", true},
		{"MyInterface", "interface", true},
		{"ExportedMethod", "method", true},
		{"Version", "const", true},
		{"GlobalVar", "const", true},
		{"main", "", false},
		{"unexported", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sym, found := symNames[tt.name]
			if found != tt.wantIn {
				t.Errorf("symbol %q: found=%v, want=%v", tt.name, found, tt.wantIn)
				return
			}
			if tt.wantIn && sym.Kind != tt.wantKind {
				t.Errorf("symbol %q: kind=%q, want=%q", tt.name, sym.Kind, tt.wantKind)
			}
			if tt.wantIn && sym.File != "main.go" {
				t.Errorf("symbol %q: file=%q, want main.go", tt.name, sym.File)
			}
			if tt.wantIn && sym.Line == 0 {
				t.Errorf("symbol %q: line=0, want > 0", tt.name)
			}
		})
	}

	// Verify method has receiver
	if sym, ok := symNames["ExportedMethod"]; ok {
		if sym.Receiver != "MyStruct" {
			t.Errorf("ExportedMethod receiver=%q, want MyStruct", sym.Receiver)
		}
	}

	// Verify package is set
	if sym, ok := symNames["ExportedFunc"]; ok {
		if sym.Package != "main" {
			t.Errorf("ExportedFunc package=%q, want main", sym.Package)
		}
	}
}

func TestExtractSymbols_TypeScript(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "app.ts", `
export function handleRequest(req: Request): Response {
  return new Response();
}

export class UserService {
  async getUser(id: string) {}
}

export const API_BASE = "/api";

export interface Config {
  port: number;
}

export type UserID = string;

export default function main() {}

function privateFunc() {}
`)

	syms := extractSymbols(dir, 10*time.Second)
	symNames := make(map[string]bool)
	for _, s := range syms {
		symNames[s.Name] = true
	}

	for _, want := range []string{"handleRequest", "UserService", "API_BASE", "Config", "UserID", "main"} {
		if !symNames[want] {
			t.Errorf("expected symbol %q not found", want)
		}
	}
	if symNames["privateFunc"] {
		t.Errorf("unexpected symbol privateFunc")
	}
}

func TestExtractSymbols_Python(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "app.py", `
def process_data(items):
    pass

class DataProcessor:
    def method(self):
        pass

def _private():
    pass
`)

	syms := extractSymbols(dir, 10*time.Second)
	symNames := make(map[string]bool)
	for _, s := range syms {
		symNames[s.Name] = true
	}

	if !symNames["process_data"] {
		t.Error("expected process_data")
	}
	if !symNames["DataProcessor"] {
		t.Error("expected DataProcessor")
	}
	// _private is at top level so it should be found (Python extractor doesn't filter by name)
	if !symNames["_private"] {
		t.Error("expected _private")
	}
	// method is indented so it should NOT be found
	if symNames["method"] {
		t.Error("unexpected method (indented)")
	}
}

func TestExtractImports_Go(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", `module github.com/example/project

go 1.24
`)
	writeTestFile(t, dir, "cmd/main.go", `package main

import (
	"fmt"
	"github.com/example/project/internal/feature"
	"github.com/example/project/internal/agent"
)

func main() {
	fmt.Println("hello")
}
`)
	writeTestFile(t, dir, "internal/agent/runner.go", `package agent

import "github.com/example/project/internal/feature"
`)

	edges := extractImports(dir, "github.com/example/project", 10*time.Second)

	// Should have: cmd -> internal/feature, cmd -> internal/agent, internal/agent -> internal/feature
	// Should NOT have: cmd -> fmt (standard library)
	edgeSet := make(map[string]bool)
	for _, e := range edges {
		edgeSet[e.From+"->"+e.To] = true
	}

	expected := []string{
		"cmd->internal/feature",
		"cmd->internal/agent",
		"internal/agent->internal/feature",
	}
	for _, want := range expected {
		if !edgeSet[want] {
			t.Errorf("expected import edge %q not found; have %v", want, edgeSet)
		}
	}
	// Standard library should be excluded
	for key := range edgeSet {
		if strings.Contains(key, "fmt") {
			t.Errorf("unexpected standard library import edge: %s", key)
		}
	}
}

func TestExtractFileSummaries(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "main.go", `package main

// main is the entry point.
func main() {}

func ExportedA() {}
func ExportedB() {}
`)
	writeTestFile(t, dir, "lib.go", `package main

func Helper() {}
`)

	symbols := extractSymbols(dir, 10*time.Second)
	summaries := extractFileSummaries(dir, symbols, 10*time.Second)

	if len(summaries) < 2 {
		t.Fatalf("expected at least 2 summaries, got %d", len(summaries))
	}

	// Find main.go summary
	var mainSum *FileSummary
	for i := range summaries {
		if summaries[i].Path == "main.go" {
			mainSum = &summaries[i]
			break
		}
	}
	if mainSum == nil {
		t.Fatal("main.go summary not found")
	}
	if mainSum.LineCount < 5 {
		t.Errorf("main.go line count %d, expected >= 5", mainSum.LineCount)
	}
	if mainSum.SymbolCount < 2 {
		t.Errorf("main.go symbol count %d, expected >= 2", mainSum.SymbolCount)
	}
}

func TestExtractDirectoryTree(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "main.go", "package main\n")
	writeTestFile(t, dir, "cmd/app/main.go", "package main\n")
	writeTestFile(t, dir, "internal/agent/runner.go", "package agent\n")
	// node_modules should be skipped
	writeTestFile(t, dir, "node_modules/pkg/index.js", "module.exports = {}\n")

	tree := extractDirectoryTree(dir, 10*time.Second)

	dirPaths := make(map[string]DirEntry)
	for _, d := range tree {
		dirPaths[d.Path] = d
	}

	// Root should exist
	if _, ok := dirPaths[""]; !ok {
		t.Error("root directory not found in tree")
	}
	// cmd should exist
	if _, ok := dirPaths["cmd"]; !ok {
		t.Error("cmd directory not found")
	}
	// node_modules should NOT exist
	if _, ok := dirPaths["node_modules"]; ok {
		t.Error("node_modules should be skipped")
	}
}

func TestBuildCodebaseIndex(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module github.com/test/project\n\ngo 1.24\n")
	writeTestFile(t, dir, "main.go", `package main

import "github.com/test/project/pkg/util"

func main() { util.Do() }
`)
	writeTestFile(t, dir, "pkg/util/util.go", `package util

// Do performs an operation.
func Do() {}
func Helper() {}
`)
	writeTestFile(t, dir, "internal/core/core.go", `package core

type Engine struct{}
func (e *Engine) Run() {}
`)

	idx, err := BuildCodebaseIndex(dir, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Symbols) == 0 {
		t.Error("expected symbols")
	}
	if len(idx.Tree) == 0 {
		t.Error("expected tree entries")
	}
	if len(idx.Summaries) == 0 {
		t.Error("expected file summaries")
	}

	// Verify specific symbols exist
	symNames := make(map[string]bool)
	for _, s := range idx.Symbols {
		symNames[s.Name] = true
	}
	for _, want := range []string{"Do", "Helper", "Engine", "Run"} {
		if !symNames[want] {
			t.Errorf("expected symbol %q in index", want)
		}
	}
}

func TestSaveLoadCodebaseIndex(t *testing.T) {
	dir := t.TempDir()

	original := &CodebaseIndex{
		Symbols: []SymbolEntry{
			{Name: "Foo", Kind: "func", Package: "main", File: "main.go", Line: 10},
			{Name: "Bar", Kind: "type", Package: "main", File: "main.go", Line: 20, Signature: "type Bar struct"},
		},
		Imports: []ImportEdge{
			{From: "cmd", To: "internal/feature"},
		},
		Summaries: []FileSummary{
			{Path: "main.go", LineCount: 50, SymbolCount: 2, TopSymbols: []string{"Foo", "Bar"}, Purpose: "entry point"},
		},
		Tree: []DirEntry{
			{Path: "", Children: []string{"cmd", "internal"}, Files: 1, Purpose: "repository root"},
		},
	}

	if err := SaveCodebaseIndex(dir, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadCodebaseIndex(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded index is nil")
	}

	// Verify round-trip
	if len(loaded.Symbols) != len(original.Symbols) {
		t.Errorf("symbols: got %d, want %d", len(loaded.Symbols), len(original.Symbols))
	}
	if loaded.Symbols[0].Name != "Foo" {
		t.Errorf("first symbol: got %q, want Foo", loaded.Symbols[0].Name)
	}
	if loaded.Symbols[1].Signature != "type Bar struct" {
		t.Errorf("second symbol signature: got %q", loaded.Symbols[1].Signature)
	}
	if len(loaded.Imports) != 1 || loaded.Imports[0].From != "cmd" {
		t.Errorf("imports mismatch: %+v", loaded.Imports)
	}
	if len(loaded.Tree) != 1 || loaded.Tree[0].Purpose != "repository root" {
		t.Errorf("tree mismatch: %+v", loaded.Tree)
	}
}

func TestLoadCodebaseIndex_NotFound(t *testing.T) {
	dir := t.TempDir()
	idx, err := LoadCodebaseIndex(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx != nil {
		t.Error("expected nil index for missing file")
	}
}

func TestSaveLoadIndexState(t *testing.T) {
	dir := t.TempDir()

	original := &CodebaseIndexState{
		HeadCommit:  "abc123",
		LastUpdated: time.Now().Truncate(time.Second),
		Version:     1,
		SymbolCount: 42,
		FileCount:   10,
	}

	if err := SaveIndexState(dir, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadIndexState(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded state is nil")
	}
	if loaded.HeadCommit != "abc123" {
		t.Errorf("head commit: got %q, want abc123", loaded.HeadCommit)
	}
	if loaded.SymbolCount != 42 {
		t.Errorf("symbol count: got %d, want 42", loaded.SymbolCount)
	}
	if loaded.FileCount != 10 {
		t.Errorf("file count: got %d, want 10", loaded.FileCount)
	}
}

func TestIsCodebaseIndexFresh(t *testing.T) {
	dir := t.TempDir()
	repoDir := t.TempDir()
	runner := &fakeGitRunner{head: "commit-a"}
	ctx := context.Background()

	// Not fresh when no state exists
	if IsCodebaseIndexFresh(ctx, runner, dir, repoDir) {
		t.Error("should not be fresh without state")
	}

	// Mark fresh
	if err := MarkCodebaseIndexFresh(ctx, runner, dir, repoDir, 10, 5); err != nil {
		t.Fatalf("mark fresh: %v", err)
	}

	// Save a dummy index file too
	if err := SaveCodebaseIndex(dir, &CodebaseIndex{}); err != nil {
		t.Fatalf("save index: %v", err)
	}

	// Now should be fresh
	if !IsCodebaseIndexFresh(ctx, runner, dir, repoDir) {
		t.Error("should be fresh after marking")
	}

	runner.head = "commit-b"

	// Should be stale now
	if IsCodebaseIndexFresh(ctx, runner, dir, repoDir) {
		t.Error("should be stale after new commit")
	}
}

func TestExtractGoSymbol_Signatures(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantName string
		wantKind string
		wantRecv string
		want     bool
	}{
		{
			name:     "exported func",
			line:     "func HandleRequest(w http.ResponseWriter, r *http.Request) {",
			wantName: "HandleRequest",
			wantKind: "func",
			want:     true,
		},
		{
			name:     "method with pointer receiver",
			line:     "func (s *Server) Start() error {",
			wantName: "Start",
			wantKind: "method",
			wantRecv: "Server",
			want:     true,
		},
		{
			name:     "method with value receiver",
			line:     "func (s Server) String() string {",
			wantName: "String",
			wantKind: "method",
			wantRecv: "Server",
			want:     true,
		},
		{
			name: "unexported func",
			line: "func helper() {}",
			want: false,
		},
		{
			name:     "type struct",
			line:     "type Config struct {",
			wantName: "Config",
			wantKind: "type",
			want:     true,
		},
		{
			name:     "type interface",
			line:     "type Handler interface {",
			wantName: "Handler",
			wantKind: "interface",
			want:     true,
		},
		{
			name: "unexported type",
			line: "type internalConfig struct {",
			want: false,
		},
		{
			name:     "const",
			line:     "const MaxRetries = 3",
			wantName: "MaxRetries",
			wantKind: "const",
			want:     true,
		},
		{
			name: "const block start",
			line: "const (",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sym, ok := extractGoSymbol(tt.line, 1, "test.go", "main")
			if ok != tt.want {
				t.Errorf("extractGoSymbol(%q): got ok=%v, want %v", tt.line, ok, tt.want)
				return
			}
			if !tt.want {
				return
			}
			if sym.Name != tt.wantName {
				t.Errorf("name: got %q, want %q", sym.Name, tt.wantName)
			}
			if sym.Kind != tt.wantKind {
				t.Errorf("kind: got %q, want %q", sym.Kind, tt.wantKind)
			}
			if tt.wantRecv != "" && sym.Receiver != tt.wantRecv {
				t.Errorf("receiver: got %q, want %q", sym.Receiver, tt.wantRecv)
			}
		})
	}
}

func TestExtractTSSymbol(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantName string
		wantKind string
		want     bool
	}{
		{"export function", "export function createApp() {", "createApp", "func", true},
		{"export class", "export class Router {", "Router", "class", true},
		{"export const", "export const PORT = 3000;", "PORT", "const", true},
		{"export interface", "export interface Props {", "Props", "interface", true},
		{"export type", "export type ID = string;", "ID", "type", true},
		{"export default function", "export default function main() {", "main", "func", true},
		{"non-export", "function helper() {}", "", "", false},
		{"export enum", "export enum Status {", "Status", "type", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sym, ok := extractTSSymbol(tt.line, 1, "test.ts")
			if ok != tt.want {
				t.Errorf("ok=%v, want %v", ok, tt.want)
				return
			}
			if !tt.want {
				return
			}
			if sym.Name != tt.wantName {
				t.Errorf("name=%q, want %q", sym.Name, tt.wantName)
			}
			if sym.Kind != tt.wantKind {
				t.Errorf("kind=%q, want %q", sym.Kind, tt.wantKind)
			}
		})
	}
}

func TestExtractPySymbol(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantName string
		wantKind string
		want     bool
	}{
		{"top-level def", "def process():", "process", "func", true},
		{"top-level class", "class Engine:", "Engine", "class", true},
		{"indented def", "    def method(self):", "", "", false},
		{"empty line", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sym, ok := extractPySymbol(tt.line, 1, "test.py")
			if ok != tt.want {
				t.Errorf("ok=%v, want %v", ok, tt.want)
				return
			}
			if !tt.want {
				return
			}
			if sym.Name != tt.wantName {
				t.Errorf("name=%q, want %q", sym.Name, tt.wantName)
			}
		})
	}
}

func TestExtractQuotedString(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`"github.com/foo/bar"`, "github.com/foo/bar"},
		{`'./relative'`, "./relative"},
		{`alias "path/to/pkg"`, "path/to/pkg"},
		{`no quotes here`, ""},
		{`""`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractQuotedString(tt.input)
			if got != tt.want {
				t.Errorf("extractQuotedString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestInferDirPurpose(t *testing.T) {
	tests := []struct {
		relPath string
		want    string
	}{
		{"", "repository root"},
		{"cmd", "CLI entry points"},
		{"internal", "private packages"},
		{"test", "test suites"},
	}
	for _, tt := range tests {
		t.Run(tt.relPath, func(t *testing.T) {
			got := inferDirPurpose(tt.relPath, t.TempDir())
			if got != tt.want {
				t.Errorf("inferDirPurpose(%q) = %q, want %q", tt.relPath, got, tt.want)
			}
		})
	}
}

// Helpers

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initTestRepo(t *testing.T, dir string) {
	t.Helper()
	runTestGit(t, dir, "init")
	runTestGit(t, dir, "config", "user.email", "test@test.com")
	runTestGit(t, dir, "config", "user.name", "Test")
}

func runTestGit(t *testing.T, dir string, args ...string) {
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
