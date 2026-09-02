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
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// removedTransactionConstants are the legacy gate-code identifiers deleted
// from this package together with the journal's free-form attention string
// and the per-entry diagnostics, gate-code, conflict-file, and dirty fields,
// plus the orchestrator's free-form attention summarizer. No non-test source
// anywhere in the repository may reference them, plain or package-qualified.
var removedTransactionConstants = map[string]bool{
	"GateCodeParentDrift":      true,
	"GateCodeNotAncestor":      true,
	"GateCodeMergeInProgress":  true,
	"GateCodeConflictMarkers":  true,
	"GateCodeMissingTargetSHA": true,
	// The orchestrator's per-repo attention summarizer was deleted with the
	// entry diagnostics it read; no source may format repository names or
	// file lists into attention text again.
	"transactionAttentionSummary": true,
}

// removedTransactionEntryFields are the RepoTransactionEntry field names
// deleted with the single-record refactor. They are banned receiver-aware
// (x.Field where x is feature.RepoTransactionEntry) because identically
// named fields remain legitimate on other types: errcat.FailureRecord
// carries Diagnostics and errcat.CodeRepository carries ConflictFiles.
var removedTransactionEntryFields = map[string]bool{
	"Diagnostics":   true,
	"GateCode":      true,
	"ConflictFiles": true,
	"Dirty":         true,
}

// TestNoNonTestSourceReferencesRemovedTransactionAPIs parses every non-test
// .go file under the repository root and fails on any reference to the
// removed gate-code constants or the removed attention summarizer, or on any
// .GateCode selector (no legitimate use of that field name remains).
func TestNoNonTestSourceReferencesRemovedTransactionAPIs(t *testing.T) {
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
				if removedTransactionConstants[node.Name] {
					violations = append(violations, fmt.Sprintf("%s:%d references removed transaction constant %s",
						path, fset.Position(node.Pos()).Line, node.Name))
				}
			case *ast.SelectorExpr:
				if node.Sel.Name == "GateCode" {
					violations = append(violations, fmt.Sprintf("%s:%d uses removed .GateCode selector",
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
		t.Errorf("removed transaction API still referenced: %s", v)
	}
}

// TestTypecheckBansRemovedTransactionEntryFields loads every repository
// package with full type information (non-test sources only) and fails when
// a selector x.Diagnostics, x.GateCode, x.ConflictFiles, or x.Dirty resolves
// to a field of feature.RepoTransactionEntry, or when any identifier
// resolves to one of the removed transaction constants declared in this
// package. This is the precise, receiver-aware guard: the identically named
// fields on errcat.FailureRecord, errcat.CodeRepository, and the generated
// server models remain legal.
func TestTypecheckBansRemovedTransactionEntryFields(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	featurePkgPath := reflect.TypeOf(Feature{}).PkgPath()
	cfg := &packages.Config{
		Mode: packages.LoadSyntax,
		Dir:  repoRoot,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatalf("loading repository packages: %v", err)
	}
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
					if obj := info.Uses[node]; removedTransactionConstantObject(obj, featurePkgPath) {
						record("%s: identifier %s resolves to the removed feature transaction constant",
							pkg.Fset.Position(node.Pos()), node.Name)
					}
				case *ast.SelectorExpr:
					if !removedTransactionEntryFields[node.Sel.Name] {
						return true
					}
					selection, ok := info.Selections[node]
					if !ok || selection.Kind() != types.FieldVal {
						return true
					}
					if transactionEntryRecv(selection.Recv(), featurePkgPath) {
						record("%s: selector %s resolves to the removed feature.RepoTransactionEntry field",
							pkg.Fset.Position(node.Sel.Pos()), node.Sel.Name)
					}
				}
				return true
			})
		}
	}
	if typechecked == 0 {
		t.Fatal("no repository package carried syntax and type info; typecheck guard could not run")
	}
	violations := make([]string, 0, len(violationSet))
	for v := range violationSet {
		violations = append(violations, v)
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("removed transaction entry field still referenced: %s", v)
	}
}

// removedTransactionConstantObject reports whether obj is one of the removed
// transaction constants declared in the feature package.
func removedTransactionConstantObject(obj types.Object, featurePkgPath string) bool {
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == featurePkgPath && removedTransactionConstants[obj.Name()]
}

// transactionEntryRecv reports whether recv (after pointer dereference) is
// feature.RepoTransactionEntry.
func transactionEntryRecv(recv types.Type, featurePkgPath string) bool {
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = ptr.Elem()
	}
	named, ok := recv.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == featurePkgPath && obj.Name() == "RepoTransactionEntry"
}
