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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// SymbolEntry represents a single symbol (function, type, class, etc.) with its location.
type SymbolEntry struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // "func", "type", "interface", "const", "class", "method"
	Package   string `json:"package"`
	File      string `json:"file"` // relative to repo root
	Line      int    `json:"line"`
	Receiver  string `json:"receiver,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// ImportEdge represents a dependency between two packages.
type ImportEdge struct {
	From string `json:"from"` // importing package (relative path)
	To   string `json:"to"`   // imported package
}

// FileSummary provides a brief overview of a single source file.
type FileSummary struct {
	Path        string   `json:"path"`
	LineCount   int      `json:"line_count"`
	SymbolCount int      `json:"symbol_count"`
	TopSymbols  []string `json:"top_symbols"` // up to 5 exported names
	Purpose     string   `json:"purpose"`     // heuristic from package doc / filename
}

// DirEntry describes a directory in the repo tree.
type DirEntry struct {
	Path     string   `json:"path"`
	Children []string `json:"children,omitempty"`
	Files    int      `json:"files"`
	Purpose  string   `json:"purpose,omitempty"`
}

// CodebaseIndex is the top-level container for a structured codebase index.
type CodebaseIndex struct {
	Symbols   []SymbolEntry `json:"symbols"`
	Imports   []ImportEdge  `json:"imports"`
	Summaries []FileSummary `json:"summaries"`
	Tree      []DirEntry    `json:"tree"`
}

// CodebaseIndexState tracks freshness metadata for a codebase index.
type CodebaseIndexState struct {
	HeadCommit  string    `json:"head_commit"`
	LastUpdated time.Time `json:"last_updated"`
	Version     int       `json:"version"`
	SymbolCount int       `json:"symbol_count"`
	FileCount   int       `json:"file_count"`
}

// IndexDir returns the directory where the codebase index is stored.
func IndexDir(kbDir string) string {
	return filepath.Join(kbDir, "index")
}

// SaveCodebaseIndex atomically writes the codebase index to disk.
func SaveCodebaseIndex(kbDir string, idx *CodebaseIndex) error {
	dir := IndexDir(kbDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating index dir: %w", err)
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling index: %w", err)
	}
	path := filepath.Join(dir, "index.json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("writing temp index: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming index: %w", err)
	}
	return nil
}

// LoadCodebaseIndex reads the codebase index from disk. Returns nil, nil if not found.
func LoadCodebaseIndex(kbDir string) (*CodebaseIndex, error) {
	data, err := os.ReadFile(filepath.Join(IndexDir(kbDir), "index.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading index: %w", err)
	}
	var idx CodebaseIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parsing index: %w", err)
	}
	return &idx, nil
}

// SaveIndexState atomically writes index state metadata to disk.
func SaveIndexState(kbDir string, state *CodebaseIndexState) error {
	dir := IndexDir(kbDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating index dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling index state: %w", err)
	}
	path := filepath.Join(dir, "index-state.json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("writing temp index state: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming index state: %w", err)
	}
	return nil
}

// LoadIndexState reads index state from disk. Returns nil, nil if not found.
func LoadIndexState(kbDir string) (*CodebaseIndexState, error) {
	data, err := os.ReadFile(filepath.Join(IndexDir(kbDir), "index-state.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading index state: %w", err)
	}
	var state CodebaseIndexState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing index state: %w", err)
	}
	return &state, nil
}

// IsCodebaseIndexFresh checks if the index is up-to-date with the repo's current HEAD.
func IsCodebaseIndexFresh(ctx context.Context, runner ports.CommandRunner, kbDir, repoPath string) bool {
	state, err := LoadIndexState(kbDir)
	if err != nil || state == nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(IndexDir(kbDir), "index.json")); err != nil {
		return false
	}
	currentCommit, err := GetCurrentCommit(ctx, runner, repoPath)
	if err != nil {
		return false
	}
	return state.HeadCommit == currentCommit
}

// MarkCodebaseIndexFresh saves the index state with the current HEAD commit.
func MarkCodebaseIndexFresh(ctx context.Context, runner ports.CommandRunner, kbDir, repoPath string, symbolCount, fileCount int) error {
	commit, err := GetCurrentCommit(ctx, runner, repoPath)
	if err != nil {
		return fmt.Errorf("getting current commit: %w", err)
	}
	return SaveIndexState(kbDir, &CodebaseIndexState{
		HeadCommit:  commit,
		LastUpdated: time.Now(),
		Version:     1,
		SymbolCount: symbolCount,
		FileCount:   fileCount,
	})
}

// BuildCodebaseIndex constructs a full codebase index for the given repo.
// The budget is split: symbols 40%, imports 15%, summaries 25%, tree 20%.
func BuildCodebaseIndex(repoPath string, budget time.Duration) (*CodebaseIndex, error) {
	if _, err := os.Stat(repoPath); err != nil {
		return nil, fmt.Errorf("repo path: %w", err)
	}

	symbolBudget := time.Duration(float64(budget) * 0.40)
	importBudget := time.Duration(float64(budget) * 0.15)
	summaryBudget := time.Duration(float64(budget) * 0.25)
	treeBudget := time.Duration(float64(budget) * 0.20)

	symbols := extractSymbols(repoPath, symbolBudget)

	// Get module path for import resolution
	_, modulePath := extractDependencies(repoPath)
	imports := extractImports(repoPath, modulePath, importBudget)

	summaries := extractFileSummaries(repoPath, symbols, summaryBudget)
	tree := extractDirectoryTree(repoPath, treeBudget)

	return &CodebaseIndex{
		Symbols:   symbols,
		Imports:   imports,
		Summaries: summaries,
		Tree:      tree,
	}, nil
}

// extractSymbols walks the repo and extracts symbol entries with file:line info.
func extractSymbols(repoPath string, budget time.Duration) []SymbolEntry {
	start := time.Now()
	var symbols []SymbolEntry

	_ = filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if skipDirs[name] || name == "testdata" || name == "test_fixtures" {
				return filepath.SkipDir
			}
			return nil
		}

		base := d.Name()
		if strings.HasSuffix(base, "_test.go") ||
			strings.HasSuffix(base, ".test.ts") ||
			strings.HasSuffix(base, ".test.js") ||
			strings.HasSuffix(base, ".test.tsx") ||
			strings.HasSuffix(base, ".test.jsx") ||
			strings.HasSuffix(base, "_test.py") {
			return nil
		}

		ext := filepath.Ext(base)
		relPath, _ := filepath.Rel(repoPath, path)

		var extractor func(string, int, string) (*SymbolEntry, bool)
		switch ext {
		case ".go":
			pkgName := detectGoPackage(path)
			extractor = func(line string, lineNum int, rel string) (*SymbolEntry, bool) {
				return extractGoSymbol(line, lineNum, rel, pkgName)
			}
		case ".ts", ".tsx", ".js", ".jsx":
			extractor = func(line string, lineNum int, rel string) (*SymbolEntry, bool) {
				return extractTSSymbol(line, lineNum, rel)
			}
		case ".py":
			extractor = func(line string, lineNum int, rel string) (*SymbolEntry, bool) {
				return extractPySymbol(line, lineNum, rel)
			}
		default:
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer func() { _ = f.Close() }()

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			if sym, ok := extractor(scanner.Text(), lineNum, relPath); ok {
				symbols = append(symbols, *sym)
			}
		}

		if time.Since(start) > budget {
			return filepath.SkipAll
		}
		return nil
	})
	return symbols
}

// detectGoPackage reads the first few lines of a Go file to find the package name.
func detectGoPackage(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "package ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return fields[1]
			}
		}
		// Stop looking after a non-comment, non-empty, non-build-tag line
		if line != "" && !strings.HasPrefix(line, "//") && !strings.HasPrefix(line, "/*") && !strings.HasPrefix(line, "+build") {
			break
		}
	}
	return ""
}

// extractGoSymbol parses a Go source line and returns a SymbolEntry if it contains
// an exported symbol declaration.
func extractGoSymbol(line string, lineNum int, relPath, pkgName string) (*SymbolEntry, bool) {
	if strings.HasPrefix(line, "func ") {
		rest := strings.TrimPrefix(line, "func ")
		var receiver string
		// Method: func (r *Receiver) Name(...)
		if strings.HasPrefix(rest, "(") {
			closeIdx := strings.Index(rest, ")")
			if closeIdx < 0 {
				return nil, false
			}
			recFields := strings.Fields(rest[1:closeIdx])
			if len(recFields) >= 1 {
				receiver = strings.TrimPrefix(recFields[len(recFields)-1], "*")
			}
			rest = strings.TrimSpace(rest[closeIdx+1:])
		}
		fields := strings.FieldsFunc(rest, func(r rune) bool {
			return r == '(' || r == ' '
		})
		if len(fields) == 0 {
			return nil, false
		}
		name := fields[0]
		if !unicode.IsUpper(rune(name[0])) {
			return nil, false
		}
		kind := "func"
		if receiver != "" {
			kind = "method"
		}
		// Extract signature up to the opening brace
		sig := strings.TrimSpace(line)
		if idx := strings.Index(sig, " {"); idx > 0 {
			sig = sig[:idx]
		}
		return &SymbolEntry{
			Name:      name,
			Kind:      kind,
			Package:   pkgName,
			File:      relPath,
			Line:      lineNum,
			Receiver:  receiver,
			Signature: sig,
		}, true
	}
	if strings.HasPrefix(line, "type ") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return nil, false
		}
		name := fields[1]
		if !unicode.IsUpper(rune(name[0])) {
			return nil, false
		}
		kind := "type"
		if fields[2] == "interface" {
			kind = "interface"
		}
		return &SymbolEntry{
			Name:    name,
			Kind:    kind,
			Package: pkgName,
			File:    relPath,
			Line:    lineNum,
		}, true
	}
	if strings.HasPrefix(line, "const ") || strings.HasPrefix(line, "var ") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, false
		}
		name := fields[1]
		// Handle "const (" blocks — we skip those (only single-line)
		if name == "(" {
			return nil, false
		}
		if !unicode.IsUpper(rune(name[0])) {
			return nil, false
		}
		return &SymbolEntry{
			Name:    name,
			Kind:    "const",
			Package: pkgName,
			File:    relPath,
			Line:    lineNum,
		}, true
	}
	return nil, false
}

// extractTSSymbol parses a TypeScript/JavaScript source line for exported symbols.
func extractTSSymbol(line string, lineNum int, relPath string) (*SymbolEntry, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "export ") {
		return nil, false
	}
	rest := strings.TrimPrefix(trimmed, "export ")
	rest = strings.TrimPrefix(rest, "default ")

	type kwKind struct {
		keyword string
		kind    string
	}
	keywords := []kwKind{
		{"function ", "func"},
		{"class ", "class"},
		{"const ", "const"},
		{"interface ", "interface"},
		{"type ", "type"},
		{"enum ", "type"},
	}
	for _, kw := range keywords {
		if strings.HasPrefix(rest, kw.keyword) {
			after := strings.TrimPrefix(rest, kw.keyword)
			fields := strings.FieldsFunc(after, func(r rune) bool {
				return r == '(' || r == ' ' || r == '<' || r == '{' || r == '=' || r == ':'
			})
			if len(fields) == 0 {
				continue
			}
			name := fields[0]
			return &SymbolEntry{
				Name: name,
				Kind: kw.kind,
				File: relPath,
				Line: lineNum,
			}, true
		}
	}
	return nil, false
}

// extractPySymbol parses a Python source line for top-level symbols.
func extractPySymbol(line string, lineNum int, relPath string) (*SymbolEntry, bool) {
	if len(line) == 0 || line[0] == ' ' || line[0] == '\t' {
		return nil, false
	}
	if strings.HasPrefix(line, "def ") {
		fields := strings.FieldsFunc(strings.TrimPrefix(line, "def "), func(r rune) bool {
			return r == '(' || r == ' ' || r == ':'
		})
		if len(fields) >= 1 {
			return &SymbolEntry{
				Name: fields[0],
				Kind: "func",
				File: relPath,
				Line: lineNum,
			}, true
		}
	}
	if strings.HasPrefix(line, "class ") {
		fields := strings.FieldsFunc(strings.TrimPrefix(line, "class "), func(r rune) bool {
			return r == '(' || r == ' ' || r == ':'
		})
		if len(fields) >= 1 {
			return &SymbolEntry{
				Name: fields[0],
				Kind: "class",
				File: relPath,
				Line: lineNum,
			}, true
		}
	}
	return nil, false
}

// extractImports walks Go and TS files to build an import edge graph.
func extractImports(repoPath, modulePath string, budget time.Duration) []ImportEdge {
	start := time.Now()
	var edges []ImportEdge
	seen := make(map[string]bool) // dedup "from->to"

	_ = filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if skipDirs[name] || name == "testdata" || name == "test_fixtures" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := filepath.Ext(d.Name())
		relPath, _ := filepath.Rel(repoPath, path)
		fromPkg := filepath.Dir(relPath)

		switch ext {
		case ".go":
			edges = appendGoImports(path, fromPkg, modulePath, edges, seen)
		case ".ts", ".tsx", ".js", ".jsx":
			edges = appendTSImports(path, fromPkg, edges, seen)
		}

		if time.Since(start) > budget {
			return filepath.SkipAll
		}
		return nil
	})
	return edges
}

// appendGoImports parses Go import blocks and appends edges.
func appendGoImports(path, fromPkg, modulePath string, edges []ImportEdge, seen map[string]bool) []ImportEdge {
	f, err := os.Open(path)
	if err != nil {
		return edges
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	inBlock := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "import (" {
			inBlock = true
			continue
		}
		if inBlock && line == ")" {
			inBlock = false
			continue
		}

		var importPath string
		if inBlock {
			// Inside import block: optional alias + "path"
			importPath = extractQuotedString(line)
		} else if strings.HasPrefix(line, "import ") {
			// Single import: import "path" or import alias "path"
			importPath = extractQuotedString(strings.TrimPrefix(line, "import "))
		} else {
			continue
		}

		if importPath == "" {
			continue
		}

		// Only track internal imports (within the module)
		if modulePath != "" && strings.HasPrefix(importPath, modulePath) {
			to := strings.TrimPrefix(importPath, modulePath+"/")
			key := fromPkg + "->" + to
			if !seen[key] {
				seen[key] = true
				edges = append(edges, ImportEdge{From: fromPkg, To: to})
			}
		}
	}
	return edges
}

// appendTSImports parses TypeScript/JavaScript import statements and appends edges.
func appendTSImports(path, fromPkg string, edges []ImportEdge, seen map[string]bool) []ImportEdge {
	f, err := os.Open(path)
	if err != nil {
		return edges
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		var importPath string
		if strings.Contains(line, " from ") {
			// import ... from '...' or import ... from "..."
			parts := strings.SplitN(line, " from ", 2)
			if len(parts) == 2 {
				importPath = extractQuotedString(parts[1])
			}
		} else if strings.Contains(line, "require(") {
			// const x = require('...')
			idx := strings.Index(line, "require(")
			if idx >= 0 {
				importPath = extractQuotedString(line[idx+len("require("):])
			}
		}

		if importPath == "" || !strings.HasPrefix(importPath, ".") {
			continue // only relative imports
		}

		// Resolve relative import to a package-like path
		to := filepath.Clean(filepath.Join(fromPkg, importPath))
		key := fromPkg + "->" + to
		if !seen[key] {
			seen[key] = true
			edges = append(edges, ImportEdge{From: fromPkg, To: to})
		}
	}
	return edges
}

// extractQuotedString extracts the content of the first quoted string on the line.
func extractQuotedString(s string) string {
	s = strings.TrimSpace(s)
	for _, q := range []byte{'"', '\'', '`'} {
		start := strings.IndexByte(s, q)
		if start < 0 {
			continue
		}
		end := strings.IndexByte(s[start+1:], q)
		if end < 0 {
			continue
		}
		return s[start+1 : start+1+end]
	}
	return ""
}

// extractFileSummaries builds a summary for each source file.
func extractFileSummaries(repoPath string, symbols []SymbolEntry, budget time.Duration) []FileSummary {
	start := time.Now()

	// Build per-file symbol index
	fileSymbols := make(map[string][]string)
	for _, sym := range symbols {
		fileSymbols[sym.File] = append(fileSymbols[sym.File], sym.Name)
	}

	var summaries []FileSummary

	_ = filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if skipDirs[name] || name == "testdata" || name == "test_fixtures" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := filepath.Ext(d.Name())
		switch ext {
		case ".go", ".ts", ".tsx", ".js", ".jsx", ".py":
		default:
			return nil
		}

		relPath, _ := filepath.Rel(repoPath, path)
		lineCount := countLines(path)

		syms := fileSymbols[relPath]
		topSyms := syms
		if len(topSyms) > 5 {
			topSyms = topSyms[:5]
		}

		purpose := inferPurpose(relPath, path)

		summaries = append(summaries, FileSummary{
			Path:        relPath,
			LineCount:   lineCount,
			SymbolCount: len(syms),
			TopSymbols:  topSyms,
			Purpose:     purpose,
		})

		if time.Since(start) > budget {
			return filepath.SkipAll
		}
		return nil
	})
	return summaries
}

// countLines counts the number of lines in a file.
func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		count++
	}
	return count
}

// inferPurpose generates a brief description of a file's purpose from its path and content.
func inferPurpose(relPath, absPath string) string {
	base := filepath.Base(relPath)
	dir := filepath.Dir(relPath)

	// Common Go conventions
	if strings.HasSuffix(base, "_test.go") {
		return "tests for " + strings.TrimSuffix(base, "_test.go")
	}

	// Try to read the first comment/docstring
	f, err := os.Open(absPath)
	if err != nil {
		return dir + " module"
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and build tags
		if line == "" || strings.HasPrefix(line, "//go:build") || strings.HasPrefix(line, "// +build") {
			continue
		}
		// Go package comment
		if strings.HasPrefix(line, "// Package ") {
			return strings.TrimPrefix(line, "// ")
		}
		// Python/JS doc comment
		if strings.HasPrefix(line, "// ") && !strings.HasPrefix(line, "// Copyright") {
			comment := strings.TrimPrefix(line, "// ")
			if len(comment) > 80 {
				comment = comment[:80]
			}
			return comment
		}
		if strings.HasPrefix(line, "# ") && !strings.HasPrefix(line, "#!") {
			comment := strings.TrimPrefix(line, "# ")
			if len(comment) > 80 {
				comment = comment[:80]
			}
			return comment
		}
		// Stop after first non-blank, non-comment line
		break
	}

	// Fall back to directory-based naming
	return dir + "/" + strings.TrimSuffix(base, filepath.Ext(base))
}

// extractDirectoryTree builds a summary of the directory structure.
func extractDirectoryTree(repoPath string, budget time.Duration) []DirEntry {
	start := time.Now()
	var entries []DirEntry

	_ = filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if skipDirs[name] || name == "testdata" || name == "test_fixtures" {
			return filepath.SkipDir
		}

		relPath, _ := filepath.Rel(repoPath, path)
		if relPath == "." {
			relPath = ""
		}

		// Read directory entries
		dirEntries, err := os.ReadDir(path)
		if err != nil {
			return nil
		}

		var children []string
		fileCount := 0
		for _, e := range dirEntries {
			if e.IsDir() {
				if !skipDirs[e.Name()] {
					children = append(children, e.Name())
				}
			} else {
				fileCount++
			}
		}

		purpose := inferDirPurpose(relPath, path)

		entries = append(entries, DirEntry{
			Path:     relPath,
			Children: children,
			Files:    fileCount,
			Purpose:  purpose,
		})

		if time.Since(start) > budget {
			return filepath.SkipAll
		}
		return nil
	})
	return entries
}

// inferDirPurpose generates a brief description of a directory's purpose.
func inferDirPurpose(relPath, absPath string) string {
	if relPath == "" || relPath == "." {
		return "repository root"
	}

	base := filepath.Base(relPath)

	// Common directory names
	purposeMap := map[string]string{
		"cmd":        "CLI entry points",
		"internal":   "private packages",
		"pkg":        "public packages",
		"test":       "test suites",
		"tests":      "test suites",
		"e2e":        "end-to-end tests",
		"docs":       "documentation",
		"config":     "configuration",
		"scripts":    "build/utility scripts",
		"api":        "API definitions",
		"models":     "data models",
		"handlers":   "request handlers",
		"middleware": "middleware",
		"utils":      "utility functions",
		"helpers":    "helper functions",
		"types":      "type definitions",
		"components": "UI components",
		"hooks":      "React hooks",
		"pages":      "page components",
		"lib":        "library code",
		"src":        "source code",
	}
	if purpose, ok := purposeMap[base]; ok {
		return purpose
	}

	// Try to read a README for purpose
	for _, name := range []string{"README.md", "README", "readme.md"} {
		data, err := os.ReadFile(filepath.Join(absPath, name))
		if err == nil {
			lines := strings.SplitN(string(data), "\n", 3)
			for _, line := range lines {
				line = strings.TrimSpace(line)
				line = strings.TrimLeft(line, "# ")
				if line != "" {
					if len(line) > 80 {
						line = line[:80]
					}
					return line
				}
			}
		}
	}

	return ""
}

// FindRelevantFiles scores index file summaries against query keywords and
// returns the top N most relevant files sorted by relevance score.
func FindRelevantFiles(index *CodebaseIndex, query string, topN int) []FileSummary {
	if index == nil || len(index.Summaries) == 0 || topN <= 0 {
		return nil
	}

	words := tokenizeQuery(query)
	if len(words) == 0 {
		// Fallback: return the largest files by symbol count
		sorted := make([]FileSummary, len(index.Summaries))
		copy(sorted, index.Summaries)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].SymbolCount > sorted[j].SymbolCount
		})
		if topN > len(sorted) {
			topN = len(sorted)
		}
		return sorted[:topN]
	}

	// Build per-file symbol name index (lowercased)
	fileSymNames := make(map[string][]string)
	for _, sym := range index.Symbols {
		fileSymNames[sym.File] = append(fileSymNames[sym.File], strings.ToLower(sym.Name))
	}

	type scored struct {
		fs    FileSummary
		score int
	}

	var results []scored
	for _, fs := range index.Summaries {
		score := 0
		pathLower := strings.ToLower(fs.Path)
		purposeLower := strings.ToLower(fs.Purpose)

		for _, w := range words {
			if strings.Contains(pathLower, w) {
				score += 3
			}
			if strings.Contains(purposeLower, w) {
				score += 2
			}
			for _, symName := range fileSymNames[fs.Path] {
				if strings.Contains(symName, w) {
					score += 2
					break // count once per word per file
				}
			}
		}

		if score > 0 {
			results = append(results, scored{fs: fs, score: score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].fs.SymbolCount > results[j].fs.SymbolCount
	})

	if topN > len(results) {
		topN = len(results)
	}
	out := make([]FileSummary, topN)
	for i := 0; i < topN; i++ {
		out[i] = results[i].fs
	}
	return out
}

// tokenizeQuery splits a query string into lowercase search terms, filtering
// out stop words and short tokens.
func tokenizeQuery(query string) []string {
	raw := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})

	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"how": true, "does": true, "do": true, "what": true, "where": true,
		"from": true, "through": true, "via": true, "and": true, "or": true,
		"to": true, "in": true, "of": true, "for": true, "with": true,
		"this": true, "that": true, "it": true, "its": true, "not": true,
	}

	seen := make(map[string]bool)
	var words []string
	for _, w := range raw {
		if len(w) < 2 || stopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		words = append(words, w)
	}
	return words
}
