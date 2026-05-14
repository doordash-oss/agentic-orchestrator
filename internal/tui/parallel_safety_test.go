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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Phase 10 parallel-safety inventory:
//
// Serial / parallel-ineligible until their global or external-state ownership
// is narrowed:
//   - app_test.go, grill_me_smoke_test.go, qa_persistence_gate_test.go:
//     exercise real feature stores, session managers, artifact gates, or
//     background goroutines.
//   - artifact_review_test.go, attach_test.go, attach_askuser_test.go,
//     chat_test.go, editconfig_test.go, help_test.go, need_user_input_test.go,
//     observe_test.go, orchestrator_bridge_test.go, publish_test.go,
//     recovery_test.go, roadmap_rewind_test.go, welcome_test.go, wizard_test.go,
//     wizard_delegation_test.go: use temp files, session fakes, callback
//     behavior, real-git coverage, or orchestration fakes that need a Phase 10
//     audit before parallel execution.
//   - autocomplete_test.go, dirpicker_test.go, fileindex_test.go,
//     filepicker_test.go, markdown_editor_test.go, skillpicker_test.go,
//     workspace_manager_test.go: scan or mutate test-scoped filesystem trees
//     and should stay serial until the filesystem fixtures are narrowed.
//   - icons_test.go and notify_test.go: mutate terminal-related environment
//     with t.Setenv.
//   - clipboard_test.go: probes host clipboard integration.
//
// Phase 10 candidates because they are model-layer or source-boundary checks
// over in-memory state:
//   - activity_test.go
//   - branding_test.go
//   - configeditor_test.go
//   - cycle_dispatch_test.go
//   - dashboard_test.go
//   - detail_test.go
//   - live_preview_test.go
//   - logs_test.go
//   - observer_fake_test.go
//   - parallel_safety_test.go
//   - phase_catalog_test.go
//   - repos_block_test.go
//   - simpletextarea_test.go
//   - styles_test.go
//   - tui_boundary_test.go
//   - tweak_removal_test.go

var tuiParallelIneligibleTestFiles = []string{
	"app_test.go",
	"artifact_review_test.go",
	"attach_test.go",
	"attach_askuser_test.go",
	"autocomplete_test.go",
	"chat_test.go",
	"clipboard_test.go",
	"dirpicker_test.go",
	"editconfig_test.go",
	"fileindex_test.go",
	"filepicker_test.go",
	"grill_me_smoke_test.go",
	"help_test.go",
	"icons_test.go",
	"keys_test.go",
	"markdown/markdown_test.go",
	"markdown_editor_test.go",
	"need_user_input_test.go",
	"notify_test.go",
	"observe_test.go",
	"orchestrator_bridge_test.go",
	"publish_test.go",
	"qa_persistence_gate_test.go",
	"recovery_test.go",
	"roadmap_rewind_test.go",
	"skillpicker_test.go",
	"welcome_test.go",
	"wizard_delegation_test.go",
	"wizard_test.go",
	"workspace_manager_test.go",
}

var tuiParallelCandidateTestFiles = []string{
	"activity_test.go",
	"branding_test.go",
	"configeditor_test.go",
	"cycle_dispatch_test.go",
	"dashboard_test.go",
	"detail_test.go",
	"live_preview_test.go",
	"logs_test.go",
	"observer_fake_test.go",
	"parallel_safety_test.go",
	"phase_catalog_test.go",
	"repos_block_test.go",
	"simpletextarea_test.go",
	"styles_test.go",
	"tui_boundary_test.go",
	"tweak_removal_test.go",
}

func TestTUIParallelSafetyInventoryCoversAllTestFiles(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(repoRoot(t), "internal", "tui")

	actual := make(map[string]bool)
	for _, file := range tuiTestFiles(t, dir) {
		rel, err := filepath.Rel(dir, file)
		if err != nil {
			t.Fatalf("rel path for %s: %v", file, err)
		}
		actual[filepath.ToSlash(rel)] = true
	}

	listed := make(map[string]bool)
	for _, name := range tuiParallelInventoryFiles() {
		if listed[name] {
			t.Errorf("parallel-safety inventory lists %s more than once", name)
		}
		listed[name] = true
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("parallel-safety inventory references %s: %v", name, err)
		}
	}

	for _, name := range sortedKeys(actual) {
		if !listed[name] {
			t.Errorf("parallel-safety inventory missing %s", name)
		}
	}
	for _, name := range sortedKeys(listed) {
		if !actual[name] {
			t.Errorf("parallel-safety inventory lists non-test file %s", name)
		}
	}
}

func tuiParallelInventoryFiles() []string {
	files := append([]string(nil), tuiParallelIneligibleTestFiles...)
	files = append(files, tuiParallelCandidateTestFiles...)
	return files
}

func TestTUIFastTestsDoNotUseFullProgramDrivers(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(repoRoot(t), "internal", "tui")
	for _, file := range tuiTestFiles(t, dir) {
		body := readFileOrFatal(t, file)
		for _, needle := range []string{"tea." + "NewProgram", "tea" + "test"} {
			if strings.Contains(body, needle) {
				t.Errorf("%s contains full-program test driver marker %q", filepath.Base(file), needle)
			}
		}
	}
}

func TestTUITestProcessGlobalMutationsAreScoped(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(repoRoot(t), "internal", "tui")
	for _, file := range tuiTestFiles(t, dir) {
		body := readFileOrFatal(t, file)
		for _, needle := range []string{"os." + "Setenv(", "os." + "Chdir("} {
			if strings.Contains(body, needle) {
				t.Errorf("%s uses %s; use a test-scoped helper such as t.Setenv or t.Chdir", filepath.Base(file), needle)
			}
		}
	}
}

func TestTUIParallelCandidateFilesCallTParallel(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(repoRoot(t), "internal", "tui")
	candidates := make(map[string]bool, len(tuiParallelCandidateTestFiles))
	for _, file := range tuiParallelCandidateTestFiles {
		candidates[file] = true
	}
	ineligible := make(map[string]bool, len(tuiParallelIneligibleTestFiles))
	for _, file := range tuiParallelIneligibleTestFiles {
		ineligible[file] = true
	}

	for _, file := range tuiTestFiles(t, dir) {
		rel, err := filepath.Rel(dir, file)
		if err != nil {
			t.Fatalf("rel path for %s: %v", file, err)
		}
		name := filepath.ToSlash(rel)
		fset, parsed := parseTUITestFile(t, file)
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			switch {
			case candidates[name]:
				if !tuiBodyStartsWithTParallel(fn.Body) {
					t.Errorf("%s:%d %s is in tuiParallelCandidateTestFiles but does not call t.Parallel as first statement",
						name, fset.Position(fn.Pos()).Line, fn.Name.Name)
				}
			case ineligible[name]:
				if tuiBodyContainsTParallel(fn.Body) {
					t.Errorf("%s:%d %s is in tuiParallelIneligibleTestFiles but calls t.Parallel",
						name, fset.Position(fn.Pos()).Line, fn.Name.Name)
				}
			}
		}
	}
}

func parseTUITestFile(t *testing.T, path string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return fset, file
}

func tuiBodyStartsWithTParallel(body *ast.BlockStmt) bool {
	if len(body.List) == 0 {
		return false
	}
	return tuiIsTParallelCall(body.List[0])
}

func tuiBodyContainsTParallel(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		stmt, ok := n.(ast.Stmt)
		if ok && tuiIsTParallelCall(stmt) {
			found = true
			return false
		}
		return true
	})
	return found
}

func tuiIsTParallelCall(stmt ast.Stmt) bool {
	expr, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Parallel" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "t"
}

func tuiTestFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	sort.Strings(files)
	return files
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
