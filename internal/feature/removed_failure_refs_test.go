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

package feature

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// removedFailureConstants are the seven legacy failure-type identifiers
// deleted from this package together with Run.LastError/Run.FailureType and
// their Feature shadows. No non-test source anywhere in the repository may
// reference them, plain or package-qualified.
var removedFailureConstants = map[string]bool{
	"FailureSafetyRail":        true,
	"FailureMaxIterations":     true,
	"FailureSessionCrash":      true,
	"FailureMissingArtifact":   true,
	"FailureProtocolViolation": true,
	"FailureInfrastructure":    true,
	"FailureWorktreeSetup":     true,
}

// TestNoNonTestSourceReferencesRemovedFailureAPIs parses every non-test .go
// file under the repository root and fails on any reference to the removed
// failure constants or on any .FailureType selector (no legitimate use of
// that field name remains anywhere in the repo).
func TestNoNonTestSourceReferencesRemovedFailureAPIs(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	var violations []string
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "node_modules":
				return filepath.SkipDir
			}
			if path != repoRoot && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.Ident:
				if removedFailureConstants[node.Name] {
					violations = append(violations, fmt.Sprintf("%s:%d references removed failure constant %s",
						path, fset.Position(node.Pos()).Line, node.Name))
				}
			case *ast.SelectorExpr:
				if node.Sel.Name == "FailureType" {
					violations = append(violations, fmt.Sprintf("%s:%d uses removed .FailureType selector",
						path, fset.Position(node.Sel.Pos()).Line))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking repository sources: %v", err)
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("removed failure API still referenced: %s", v)
	}
}

// TestTypecheckBansRemovedRunFailureFields loads every repository package
// with full type information (non-test sources only) and fails when a
// selector x.LastError or x.FailureType resolves to a field of feature.Feature,
// feature.Run, feature.SetupState, or feature.SetupTask, or when any
// identifier resolves to one of the removed failure constants declared in
// this package. This is the precise, receiver-aware guard for .LastError:
// the per-repo RepoState and result-holder LastError fields remain legal.
func TestTypecheckBansRemovedRunFailureFields(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	pkgs := loadRepoPackages(t, repoRoot, "./...", nil)
	violations, typechecked := scanPackagesForRemovedFailureRefs(pkgs, reflect.TypeOf(Feature{}).PkgPath())
	if typechecked == 0 {
		t.Fatal("no repository package carried syntax and type info; typecheck guard could not run")
	}
	for _, v := range violations {
		t.Errorf("removed run failure field still referenced: %s", v)
	}
}

// TestTypecheckGuardFlagsSetupLastErrorFixture pins the receiver-aware guard
// itself: with the removed fields synthetically reintroduced (via source
// overlays that never touch disk), a non-test source selecting .LastError on
// the setup aggregate or a setup task must be flagged, so the guard cannot
// silently stop covering the removed setup fields.
func TestTypecheckGuardFlagsSetupLastErrorFixture(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	setupPath := filepath.Join(repoRoot, "internal", "feature", "setup.go")
	setupSrcBytes, err := os.ReadFile(setupPath)
	if err != nil {
		t.Fatalf("read setup.go: %v", err)
	}
	// Reintroduce the removed fields so the fixture compiles; the guard must
	// still flag every selector that resolves to them.
	pkgIdx := strings.Index(string(setupSrcBytes), "package feature\n")
	if pkgIdx < 0 {
		t.Fatal("setup.go does not carry the expected package clause")
	}
	setupSrc := string(setupSrcBytes[pkgIdx:])
	taskAnchor := "Error *errcat.FailureRecord `yaml:\"error,omitempty\"`\n}"
	if !strings.Contains(setupSrc, taskAnchor) {
		t.Fatal("setup.go no longer carries the SetupTask Error field anchor")
	}
	setupSrc = strings.Replace(setupSrc, taskAnchor,
		"Error *errcat.FailureRecord `yaml:\"error,omitempty\"`\n\tLastError string `yaml:\"last_error,omitempty\"`\n}", 1)
	stateAnchor := "TaskOrder     []string             `yaml:\"task_order,omitempty\"`\n}"
	if !strings.Contains(setupSrc, stateAnchor) {
		t.Fatal("setup.go no longer carries the SetupState TaskOrder anchor")
	}
	setupSrc = strings.Replace(setupSrc, stateAnchor,
		"TaskOrder     []string             `yaml:\"task_order,omitempty\"`\n\tLastError     string               `yaml:\"last_error,omitempty\"`\n}", 1)

	fixture := `package feature

func lastErrorFixtureProbe(s *SetupState, task *SetupTask) (string, string) {
	return s.LastError, task.LastError
}
`
	overlayPath := filepath.Join(repoRoot, "internal", "feature", "zzz_lasterror_fixture_probe.go")
	pkgs := loadRepoPackages(t, repoRoot, "./internal/feature", map[string][]byte{
		setupPath:   []byte(setupSrc),
		overlayPath: []byte(fixture),
	})
	violations, typechecked := scanPackagesForRemovedFailureRefs(pkgs, reflect.TypeOf(Feature{}).PkgPath())
	if typechecked == 0 {
		t.Fatal("fixture package carried no syntax and type info; guard could not run")
	}
	setupStateFlagged, setupTaskFlagged := false, false
	for _, v := range violations {
		if !strings.Contains(v, "zzz_lasterror_fixture_probe.go") {
			t.Errorf("unexpected violation outside the fixture: %s", v)
			continue
		}
		if strings.Contains(v, "feature.SetupState") {
			setupStateFlagged = true
		}
		if strings.Contains(v, "feature.SetupTask") {
			setupTaskFlagged = true
		}
	}
	if !setupStateFlagged || !setupTaskFlagged {
		t.Fatalf("fixture violations = %v; want .LastError flagged on both SetupState and SetupTask", violations)
	}
}

// loadRepoPackages loads packages with full type information under root,
// optionally with source overlays (synthetic files that never touch disk).
func loadRepoPackages(t *testing.T, root, pattern string, overlay map[string][]byte) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		// LoadSyntax = NeedTypes | NeedSyntax | NeedTypesInfo (plus the
		// bits they imply): typed syntax trees for the root packages, which
		// is exactly what the selector-resolution guard needs.
		Mode:    packages.LoadSyntax,
		Dir:     root,
		Overlay: overlay,
	}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		t.Fatalf("loading repository packages: %v", err)
	}
	return pkgs
}

// scanPackagesForRemovedFailureRefs inspects loaded packages for selectors
// resolving to the removed LastError/FailureType fields or identifiers
// resolving to the removed failure constants. It returns the sorted
// violations and the number of type-checked files.
func scanPackagesForRemovedFailureRefs(pkgs []*packages.Package, featurePkgPath string) ([]string, int) {
	violationSet := make(map[string]bool)
	record := func(format string, args ...any) {
		violationSet[fmt.Sprintf(format, args...)] = true
	}
	typechecked := 0
	for _, pkg := range pkgs {
		info := pkg.TypesInfo
		if info == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			typechecked++
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.Ident:
					if obj := info.Uses[node]; removedFailureConstantObject(obj, featurePkgPath) {
						record("%s: identifier %s resolves to the removed feature failure constant",
							pkg.Fset.Position(node.Pos()), node.Name)
					}
				case *ast.SelectorExpr:
					if node.Sel.Name != "LastError" && node.Sel.Name != "FailureType" {
						return true
					}
					selection, ok := info.Selections[node]
					if !ok || selection.Kind() != types.FieldVal {
						return true
					}
					if recvName, ok := featureRunRecvName(selection.Recv(), featurePkgPath); ok {
						record("%s: selector %s resolves to the removed feature.%s field",
							pkg.Fset.Position(node.Sel.Pos()), node.Sel.Name, recvName)
					}
				}
				return true
			})
		}
	}
	if typechecked == 0 {
		return nil, 0
	}
	violations := make([]string, 0, len(violationSet))
	for v := range violationSet {
		violations = append(violations, v)
	}
	sort.Strings(violations)
	return violations, typechecked
}

// removedFailureConstantObject reports whether obj is one of the removed
// failure constants declared in the feature package.
func removedFailureConstantObject(obj types.Object, featurePkgPath string) bool {
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == featurePkgPath && removedFailureConstants[obj.Name()]
}

// featureRunRecvName reports whether recv (after pointer dereference) is
// one of the feature types whose LastError field was removed — Feature, Run,
// SetupState, or SetupTask — returning the type name when it is.
func featureRunRecvName(recv types.Type, featurePkgPath string) (string, bool) {
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = ptr.Elem()
	}
	named, ok := recv.(*types.Named)
	if !ok {
		return "", false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != featurePkgPath {
		return "", false
	}
	switch obj.Name() {
	case "Feature", "Run", "SetupState", "SetupTask":
		return obj.Name(), true
	default:
		return "", false
	}
}
