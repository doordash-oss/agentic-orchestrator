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
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// skipDirs contains directory names that should be excluded from recursive scanning.
var skipDirs = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	".git":         true,
	"dist":         true,
	"build":        true,
	"__pycache__":  true,
	".cache":       true,
	".venv":        true,
}

// RepoFeatures holds raw extracted content from a repository.
// Each field contains raw text/identifiers — tokenization happens downstream.
type RepoFeatures struct {
	RepoName       string
	Documentation  string
	Dependencies   []string
	ModulePath     string
	Directories    []string
	FileExtensions map[string]int
	BuildTargets   []string
	ConfigSignals  []string
	SourceIdents   []string
	CommitMessages []string
	Language       string
	ExtractionTime time.Duration
}

// extractDocumentation reads CLAUDE.md, README.md, and AGENTS.md from repoPath,
// concatenating their contents with newline separators.
func extractDocumentation(repoPath string) string {
	files := []string{"CLAUDE.md", "README.md", "AGENTS.md"}
	var parts []string
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(repoPath, f))
		if err != nil {
			continue
		}
		if len(data) > 0 {
			parts = append(parts, string(data))
		}
	}
	return strings.Join(parts, "\n")
}

// extractDependencies reads dependency manifests and returns dependency names
// (each repeated 3x for weighting) and the module path.
func extractDependencies(repoPath string) (deps []string, modulePath string) {
	// Go: parse go.mod
	if data, err := os.ReadFile(filepath.Join(repoPath, "go.mod")); err == nil {
		return parseGoMod(string(data))
	}

	// Node: parse package.json
	if data, err := os.ReadFile(filepath.Join(repoPath, "package.json")); err == nil {
		return parsePackageJSON(data)
	}

	// Python: try pyproject.toml first, then requirements.txt
	if data, err := os.ReadFile(filepath.Join(repoPath, "pyproject.toml")); err == nil {
		deps = parsePyprojectTOML(string(data))
		if len(deps) > 0 {
			return deps, ""
		}
	}
	if data, err := os.ReadFile(filepath.Join(repoPath, "requirements.txt")); err == nil {
		return parseRequirementsTxt(string(data)), ""
	}

	return nil, ""
}

func parseGoMod(content string) (deps []string, modulePath string) {
	inRequire := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "module ") {
			modulePath = strings.TrimPrefix(line, "module ")
			continue
		}

		if line == "require (" {
			inRequire = true
			continue
		}
		if inRequire && line == ")" {
			inRequire = false
			continue
		}

		if inRequire {
			fields := strings.Fields(line)
			if len(fields) >= 1 {
				deps = appendDepParts(deps, fields[0])
			}
		} else if strings.HasPrefix(line, "require ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				deps = appendDepParts(deps, fields[1])
			}
		}
	}
	return deps, modulePath
}

// appendDepParts extracts the last two segments and last segment from a
// dependency path, adding each 3x to the deps slice.
func appendDepParts(deps []string, depPath string) []string {
	parts := strings.Split(depPath, "/")
	if len(parts) >= 2 {
		twoSeg := parts[len(parts)-2] + "/" + parts[len(parts)-1]
		for i := 0; i < 3; i++ {
			deps = append(deps, twoSeg)
		}
	}
	lastSeg := parts[len(parts)-1]
	for i := 0; i < 3; i++ {
		deps = append(deps, lastSeg)
	}
	return deps
}

func parsePackageJSON(data []byte) (deps []string, modulePath string) {
	var pkg struct {
		Name            string                 `json:"name"`
		Dependencies    map[string]interface{} `json:"dependencies"`
		DevDependencies map[string]interface{} `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, ""
	}
	modulePath = pkg.Name

	for name := range pkg.Dependencies {
		clean := stripScopePrefix(name)
		for i := 0; i < 3; i++ {
			deps = append(deps, clean)
		}
	}
	for name := range pkg.DevDependencies {
		clean := stripScopePrefix(name)
		for i := 0; i < 3; i++ {
			deps = append(deps, clean)
		}
	}
	return deps, modulePath
}

func stripScopePrefix(name string) string {
	if strings.HasPrefix(name, "@") {
		if idx := strings.Index(name, "/"); idx >= 0 {
			return name[idx+1:]
		}
	}
	return name
}

func parsePyprojectTOML(content string) []string {
	var deps []string
	inDeps := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "dependencies") && strings.Contains(trimmed, "[") {
			inDeps = true
			// Handle inline list on same line
			if idx := strings.Index(trimmed, "["); idx >= 0 {
				rest := trimmed[idx:]
				deps = append(deps, extractPyDepsFromBrackets(rest)...)
			}
			if strings.Contains(trimmed, "]") {
				inDeps = false
			}
			continue
		}
		if inDeps {
			if strings.Contains(trimmed, "]") {
				deps = append(deps, extractPyDepsFromBrackets(trimmed)...)
				inDeps = false
				continue
			}
			deps = append(deps, extractPyDepsFromBrackets(trimmed)...)
		}
	}
	return deps
}

func extractPyDepsFromBrackets(line string) []string {
	var deps []string
	// Remove brackets
	line = strings.NewReplacer("[", "", "]", "").Replace(line)
	for _, part := range strings.Split(line, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "\"'")
		if part == "" {
			continue
		}
		// Strip version specifiers
		for _, sep := range []string{">=", "<=", "==", "!=", "~=", ">", "<"} {
			if idx := strings.Index(part, sep); idx >= 0 {
				part = part[:idx]
			}
		}
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for i := 0; i < 3; i++ {
			deps = append(deps, part)
		}
	}
	return deps
}

func parseRequirementsTxt(content string) []string {
	var deps []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		// Strip version specifiers
		name := line
		for _, sep := range []string{">=", "<=", "==", "!=", "~=", ">", "<", "["} {
			if idx := strings.Index(name, sep); idx >= 0 {
				name = name[:idx]
			}
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		for i := 0; i < 3; i++ {
			deps = append(deps, name)
		}
	}
	return deps
}

// extractDirectories returns top-level directory names prefixed with "dir_".
func extractDirectories(repoPath string) []string {
	entries, err := os.ReadDir(repoPath)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, "dir_"+e.Name())
		}
	}
	return dirs
}

// extractFileExtensions walks the repository and counts file extensions.
// Skips directories in skipDirs and caps at 10000 files.
func extractFileExtensions(repoPath string) map[string]int {
	exts := make(map[string]int)
	count := 0
	_ = filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		count++
		if count > 10000 {
			return filepath.SkipAll
		}
		ext := filepath.Ext(d.Name())
		if ext == "" {
			return nil
		}
		ext = strings.ToLower(strings.TrimPrefix(ext, "."))
		exts[ext]++
		return nil
	})
	return exts
}

// extractBuildTargets reads Makefile and Taskfile.yml/yaml for build target names.
func extractBuildTargets(repoPath string) []string {
	var targets []string
	targets = append(targets, extractMakefileTargets(repoPath)...)
	targets = append(targets, extractTaskfileTargets(repoPath)...)
	return targets
}

func extractMakefileTargets(repoPath string) []string {
	f, err := os.Open(filepath.Join(repoPath, "Makefile"))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var targets []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '\t' || line[0] == '#' {
			continue
		}
		if strings.HasPrefix(line, ".PHONY") {
			continue
		}
		// Check for target pattern: starts with letter or underscore, followed by
		// alphanumeric/hyphen/underscore, then colon
		colonIdx := strings.IndexByte(line, ':')
		if colonIdx < 0 {
			continue
		}
		// Skip variable assignments: = before :, or := immediately after identifier
		eqIdx := strings.IndexByte(line, '=')
		if eqIdx >= 0 && eqIdx < colonIdx {
			continue
		}
		// Check for := (colon immediately followed by =)
		if colonIdx+1 < len(line) && line[colonIdx+1] == '=' {
			continue
		}
		name := strings.TrimSpace(line[:colonIdx])
		if isValidTargetName(name) {
			targets = append(targets, "target_"+name)
		}
	}
	return targets
}

func isValidTargetName(name string) bool {
	if name == "" {
		return false
	}
	first := rune(name[0])
	if !unicode.IsLetter(first) && first != '_' {
		return false
	}
	for _, r := range name[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func extractTaskfileTargets(repoPath string) []string {
	var data []byte
	var err error
	data, err = os.ReadFile(filepath.Join(repoPath, "Taskfile.yml"))
	if err != nil {
		data, err = os.ReadFile(filepath.Join(repoPath, "Taskfile.yaml"))
		if err != nil {
			return nil
		}
	}

	var targets []string
	inTasks := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "tasks:" {
			inTasks = true
			continue
		}
		if inTasks {
			// A line at 2-space indent ending with colon is a task name
			if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") {
				if strings.HasSuffix(trimmed, ":") {
					name := strings.TrimSuffix(trimmed, ":")
					if name != "" {
						targets = append(targets, "target_"+name)
					}
				}
			}
			// A line at zero indent that's not empty means we left the tasks section
			if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
				inTasks = false
			}
		}
	}
	return targets
}

// extractConfigSignals reads Dockerfile, devbox.json, and .tool-versions for config signals.
func extractConfigSignals(repoPath string) []string {
	var signals []string
	signals = append(signals, extractDockerfileSignals(repoPath)...)
	signals = append(signals, extractDevboxSignals(repoPath)...)
	signals = append(signals, extractToolVersionsSignals(repoPath)...)
	return signals
}

func extractDockerfileSignals(repoPath string) []string {
	f, err := os.Open(filepath.Join(repoPath, "Dockerfile"))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var signals []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		upper := strings.ToUpper(strings.TrimSpace(line))
		if !strings.HasPrefix(upper, "FROM ") {
			continue
		}
		// Extract image name from "FROM image:tag AS alias"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		image := fields[1]
		// Strip " AS ..." suffix
		if asIdx := strings.Index(strings.ToUpper(image), " AS "); asIdx >= 0 {
			image = image[:asIdx]
		}
		// Strip tag
		if tagIdx := strings.LastIndex(image, ":"); tagIdx >= 0 {
			image = image[:tagIdx]
		}
		// Strip registry prefix (everything up to and including last /)
		if slashIdx := strings.LastIndex(image, "/"); slashIdx >= 0 {
			image = image[slashIdx+1:]
		}
		if image != "" {
			signals = append(signals, "docker_"+image)
		}
	}
	return signals
}

func extractDevboxSignals(repoPath string) []string {
	data, err := os.ReadFile(filepath.Join(repoPath, "devbox.json"))
	if err != nil {
		return nil
	}
	var devbox struct {
		Packages json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal(data, &devbox); err != nil {
		return nil
	}

	var signals []string

	// Try as []string first
	var pkgList []string
	if json.Unmarshal(devbox.Packages, &pkgList) == nil {
		for _, pkg := range pkgList {
			name := pkg
			if atIdx := strings.Index(name, "@"); atIdx >= 0 {
				name = name[:atIdx]
			}
			if name != "" {
				signals = append(signals, "devbox_"+name)
			}
		}
		return signals
	}

	// Try as map[string]interface{} (devbox also supports object format)
	var pkgMap map[string]interface{}
	if json.Unmarshal(devbox.Packages, &pkgMap) == nil {
		for pkg := range pkgMap {
			name := pkg
			if atIdx := strings.Index(name, "@"); atIdx >= 0 {
				name = name[:atIdx]
			}
			if name != "" {
				signals = append(signals, "devbox_"+name)
			}
		}
	}
	return signals
}

func extractToolVersionsSignals(repoPath string) []string {
	data, err := os.ReadFile(filepath.Join(repoPath, ".tool-versions"))
	if err != nil {
		return nil
	}
	var signals []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		version := fields[1]
		signals = append(signals, "tool_"+name)
		// Extract major version
		major := version
		if dotIdx := strings.Index(version, "."); dotIdx >= 0 {
			major = version[:dotIdx]
		}
		signals = append(signals, fmt.Sprintf("toolver_%s_%s", name, major))
	}
	return signals
}

// extractSourceIdentifiers scans source files for exported identifiers,
// respecting the given time budget.
func extractSourceIdentifiers(repoPath string, budget time.Duration) []string {
	start := time.Now()
	var idents []string

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

		// Skip test files
		base := d.Name()
		if strings.HasSuffix(base, "_test.go") ||
			strings.HasSuffix(base, ".test.ts") ||
			strings.HasSuffix(base, ".test.js") ||
			strings.HasSuffix(base, ".test.tsx") ||
			strings.HasSuffix(base, ".test.jsx") {
			return nil
		}

		ext := filepath.Ext(base)
		var extractor func(string) (string, bool)
		switch ext {
		case ".go":
			extractor = extractGoIdent
		case ".ts", ".tsx", ".js", ".jsx":
			extractor = extractTSIdent
		case ".py":
			extractor = extractPyIdent
		default:
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer func() { _ = f.Close() }()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			if ident, ok := extractor(scanner.Text()); ok {
				idents = append(idents, ident)
			}
		}

		if time.Since(start) > budget {
			return filepath.SkipAll
		}
		return nil
	})
	return idents
}

// extractGoIdent parses a line for Go exported identifiers (func, type, package).
func extractGoIdent(line string) (string, bool) {
	if strings.HasPrefix(line, "func ") {
		name := extractGoFuncName(line)
		if name != "" && unicode.IsUpper(rune(name[0])) {
			return "go_" + name, true
		}
	} else if strings.HasPrefix(line, "type ") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			name := fields[1]
			if unicode.IsUpper(rune(name[0])) {
				return "go_" + name, true
			}
		}
	} else if strings.HasPrefix(line, "package ") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			return "go_" + fields[1], true
		}
	}
	return "", false
}

func extractGoFuncName(line string) string {
	// Remove "func " prefix
	rest := strings.TrimPrefix(line, "func ")
	// If it starts with '(', it's a method — skip past the receiver
	if strings.HasPrefix(rest, "(") {
		closeIdx := strings.Index(rest, ")")
		if closeIdx < 0 {
			return ""
		}
		rest = strings.TrimSpace(rest[closeIdx+1:])
	}
	// Extract the function name (first word before '(' or space)
	fields := strings.FieldsFunc(rest, func(r rune) bool {
		return r == '(' || r == ' '
	})
	if len(fields) >= 1 {
		return fields[0]
	}
	return ""
}

// extractTSIdent parses a line for TypeScript/JavaScript exported identifiers.
func extractTSIdent(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "export ") {
		return "", false
	}
	rest := strings.TrimPrefix(trimmed, "export ")

	// Handle "export default function/class ..."
	rest = strings.TrimPrefix(rest, "default ")

	keywords := []string{"function ", "class ", "const ", "interface ", "type ", "enum "}
	for _, kw := range keywords {
		if strings.HasPrefix(rest, kw) {
			after := strings.TrimPrefix(rest, kw)
			fields := strings.FieldsFunc(after, func(r rune) bool {
				return r == '(' || r == ' ' || r == '<' || r == '{' || r == '='
			})
			if len(fields) >= 1 {
				return "ts_" + fields[0], true
			}
		}
	}
	return "", false
}

// extractPyIdent parses a line for Python top-level def/class identifiers.
func extractPyIdent(line string) (string, bool) {
	// Only match lines with no leading whitespace
	if len(line) == 0 || line[0] == ' ' || line[0] == '\t' {
		return "", false
	}
	if strings.HasPrefix(line, "def ") {
		fields := strings.FieldsFunc(strings.TrimPrefix(line, "def "), func(r rune) bool {
			return r == '(' || r == ' ' || r == ':'
		})
		if len(fields) >= 1 {
			return "py_" + fields[0], true
		}
	} else if strings.HasPrefix(line, "class ") {
		fields := strings.FieldsFunc(strings.TrimPrefix(line, "class "), func(r rune) bool {
			return r == '(' || r == ' ' || r == ':'
		})
		if len(fields) >= 1 {
			return "py_" + fields[0], true
		}
	}
	return "", false
}

// extractCommitMessages extracts recent commit subject lines from git history.
func extractCommitMessages(ctx context.Context, runner ports.CommandRunner, repoPath string, budget time.Duration) []string {
	if budget < time.Second {
		return nil
	}
	timeout := budget
	if timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := runner.Run(ctx, "git", []string{"-C", repoPath, "log", "--oneline", "-50", "--format=%s"}, ports.CommandOpts{})
	if err != nil {
		return nil
	}

	var messages []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			messages = append(messages, line)
		}
	}
	return messages
}

// detectLanguage determines the primary language based on file extension counts.
func detectLanguage(extensions map[string]int) string {
	goCount := extensions["go"]
	tsCount := extensions["ts"] + extensions["tsx"]
	jsCount := extensions["js"] + extensions["jsx"]
	pyCount := extensions["py"]

	maxCount := 0
	lang := ""
	if goCount > maxCount {
		maxCount = goCount
		lang = "go"
	}
	if tsCount > maxCount {
		maxCount = tsCount
		lang = "typescript"
	}
	if jsCount > maxCount {
		maxCount = jsCount
		lang = "javascript"
	}
	if pyCount > maxCount {
		lang = "python"
	}
	return lang
}

// ExtractRepoFeatures extracts all features from a single repository within the given time budget.
func ExtractRepoFeatures(ctx context.Context, runner ports.CommandRunner, repoName, repoPath string, budget time.Duration) (*RepoFeatures, error) {
	start := time.Now()

	rf := &RepoFeatures{
		RepoName: repoName,
	}

	// Cheap operations — always run
	rf.Documentation = extractDocumentation(repoPath)
	rf.Dependencies, rf.ModulePath = extractDependencies(repoPath)
	rf.Directories = extractDirectories(repoPath)
	rf.FileExtensions = extractFileExtensions(repoPath)
	rf.BuildTargets = extractBuildTargets(repoPath)
	rf.ConfigSignals = extractConfigSignals(repoPath)
	rf.Language = detectLanguage(rf.FileExtensions)

	// Expensive operations — time-budgeted
	remaining := budget - time.Since(start)
	if remaining > 0 {
		identBudget := remaining * 80 / 100
		rf.SourceIdents = extractSourceIdentifiers(repoPath, identBudget)

		remaining = budget - time.Since(start)
		rf.CommitMessages = extractCommitMessages(ctx, runner, repoPath, remaining)
	}

	rf.ExtractionTime = time.Since(start)
	return rf, nil
}

// ExtractAllRepoFeatures extracts features from multiple repositories concurrently.
// An optional onProgress callback is invoked after each repo completes with (done, total).
func ExtractAllRepoFeatures(ctx context.Context, runner ports.CommandRunner, repos map[string]config.RepoConfig, totalBudget time.Duration, onProgress ...func(done, total int)) map[string]*RepoFeatures {
	if len(repos) == 0 {
		return nil
	}

	perRepoBudget := totalBudget / time.Duration(len(repos))
	semaphore := make(chan struct{}, runtime.NumCPU())

	var mu sync.Mutex
	results := make(map[string]*RepoFeatures)

	var completed atomic.Int32
	total := len(repos)
	var progressFn func(done, total int)
	if len(onProgress) > 0 {
		progressFn = onProgress[0]
	}

	var wg sync.WaitGroup

	for name, repo := range repos {
		wg.Add(1)
		go func(name string, repo config.RepoConfig) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			rf, err := ExtractRepoFeatures(ctx, runner, name, repo.Path, perRepoBudget)
			if err != nil {
				if progressFn != nil {
					progressFn(int(completed.Add(1)), total)
				}
				return
			}
			mu.Lock()
			results[name] = rf
			mu.Unlock()
			if progressFn != nil {
				progressFn(int(completed.Add(1)), total)
			}
		}(name, repo)
	}

	wg.Wait()
	return results
}

// ToText concatenates all features into a single text blob for downstream tokenization.
func ToText(rf *RepoFeatures) string {
	var b strings.Builder

	b.WriteString(rf.Documentation)
	b.WriteByte('\n')

	b.WriteString(strings.Join(rf.Dependencies, " "))
	b.WriteByte('\n')

	b.WriteString(rf.ModulePath)
	b.WriteByte('\n')

	b.WriteString(strings.Join(rf.Directories, " "))
	b.WriteByte('\n')

	// Extension tokens weighted by count
	var extKeys []string
	for k := range rf.FileExtensions {
		extKeys = append(extKeys, k)
	}
	sort.Strings(extKeys)
	for _, ext := range extKeys {
		count := rf.FileExtensions[ext]
		reps := count / 10
		if reps > 5 {
			reps = 5
		}
		for i := 0; i < reps; i++ {
			b.WriteString("lang_" + ext + " ")
		}
	}

	b.WriteByte('\n')
	b.WriteString(strings.Join(rf.BuildTargets, " "))
	b.WriteByte('\n')

	b.WriteString(strings.Join(rf.ConfigSignals, " "))
	b.WriteByte('\n')

	b.WriteString(strings.Join(rf.SourceIdents, " "))
	b.WriteByte('\n')

	b.WriteString(strings.Join(rf.CommitMessages, " "))

	return b.String()
}
