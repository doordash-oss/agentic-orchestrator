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

package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

var sessionParallelUnsafeVars = map[string]bool{
	"criticalAttachSendTimeout": true,
	"codexHandshakeTimeout":     true,
	"resultShutdownGrace":       true,
}

func TestSessionParallelCandidatesCallTParallel(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob session tests: %v", err)
	}
	for _, path := range files {
		fset, file := parseSessionTestFile(t, path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			comments := sessionCommentsInFunc(file.Comments, fn)
			switch {
			case strings.Contains(comments, "parallel-candidate"):
				if !sessionBodyStartsWithTParallel(fn.Body) {
					t.Errorf("%s:%d %s is parallel-candidate but does not call t.Parallel as first statement",
						path, fset.Position(fn.Pos()).Line, fn.Name.Name)
				}
			case strings.Contains(comments, "parallel-exempt"):
				if sessionBodyContainsTParallel(fn.Body) {
					t.Errorf("%s:%d %s is parallel-exempt but calls t.Parallel",
						path, fset.Position(fn.Pos()).Line, fn.Name.Name)
				}
			}
		}
	}
}

func TestSessionPackageVarsNotMutatedByParallelTests(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob session tests: %v", err)
	}
	for _, path := range files {
		fset, file := parseSessionTestFile(t, path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			if !sessionBodyStartsWithTParallel(fn.Body) {
				continue
			}
			for _, name := range mutatedSessionPackageVars(fn.Body) {
				t.Errorf("%s:%d %s calls t.Parallel and mutates package var %s",
					path, fset.Position(fn.Pos()).Line, fn.Name.Name, name)
			}
		}
	}
}

func parseSessionTestFile(t *testing.T, path string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return fset, file
}

func sessionCommentsInFunc(groups []*ast.CommentGroup, fn *ast.FuncDecl) string {
	var b strings.Builder
	for _, group := range groups {
		if group.Pos() < fn.Body.Pos() || group.End() > fn.Body.End() {
			continue
		}
		b.WriteString(group.Text())
		b.WriteByte('\n')
	}
	return b.String()
}

func sessionBodyStartsWithTParallel(body *ast.BlockStmt) bool {
	if len(body.List) == 0 {
		return false
	}
	return sessionIsTParallelCall(body.List[0])
}

func sessionBodyContainsTParallel(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		stmt, ok := n.(ast.Stmt)
		if ok && sessionIsTParallelCall(stmt) {
			found = true
			return false
		}
		return true
	})
	return found
}

func sessionIsTParallelCall(stmt ast.Stmt) bool {
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

func mutatedSessionPackageVars(body *ast.BlockStmt) []string {
	seen := make(map[string]bool)
	ast.Inspect(body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range stmt.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && sessionParallelUnsafeVars[ident.Name] {
					seen[ident.Name] = true
				}
			}
		case *ast.IncDecStmt:
			if ident, ok := stmt.X.(*ast.Ident); ok && sessionParallelUnsafeVars[ident.Name] {
				seen[ident.Name] = true
			}
		}
		return true
	})
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}
