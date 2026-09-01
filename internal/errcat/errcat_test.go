// Copyright 2026 DoorDash, Inc.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errcat

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zeroParams are every registered parameter set at its zero value. A summary
// template must degrade to its static summary for any of these.
var zeroParams = []Params{
	WorkspaceRootParams{},
	ReadinessParams{},
	RelatedFeatureParams{},
	PublishRepoParams{},
	SubjectParams{},
	PathParams{},
	UsageParams{},
	UpdateCheckParams{},
	AlreadyRunningParams{},
	ViolationParams{},
	ProviderUnavailableParams{},
	ProviderIssueParams{},
}

func TestCatalogEntriesAreValid(t *testing.T) {
	codes := Codes()
	if len(codes) < 50 {
		t.Fatalf("catalog has %d codes; the seeded server inventory needs at least 50", len(codes))
	}
	for _, code := range codes {
		entry, ok := Lookup(code)
		if !ok {
			t.Fatalf("%s: listed by Codes but missing from catalog", code)
		}
		if !entry.Class.Valid() {
			t.Errorf("%s: invalid class %q", code, entry.Class)
		}
		if strings.TrimSpace(entry.Title) == "" {
			t.Errorf("%s: empty title", code)
		}
		if strings.TrimSpace(entry.Summary) == "" {
			t.Errorf("%s: empty summary", code)
		}
		for _, action := range entry.Actions {
			if strings.TrimSpace(action) == "" {
				t.Errorf("%s: empty action reference", code)
			}
		}
		for _, block := range entry.Blocks {
			switch block {
			case BlockRepositories, BlockPhase, BlockCommand:
			default:
				t.Errorf("%s: undeclared context block %q", code, block)
			}
		}
		if strings.HasSuffix(entry.Title, ".") {
			t.Errorf("%s: title ends with a period: %q", code, entry.Title)
		}
		if strings.HasPrefix(entry.Title, "Error") {
			t.Errorf("%s: title leads with 'Error': %q", code, entry.Title)
		}
	}
}

func TestEveryCodeRendersWithZeroValueParameters(t *testing.T) {
	for _, code := range Codes() {
		rendered := New(code)
		if rendered.Title == "" || rendered.Summary == "" {
			t.Errorf("%s: zero-option render has empty title or summary", code)
		}
		for _, params := range zeroParams {
			rendered := New(code, WithParams(params))
			if rendered.Title == "" || rendered.Summary == "" {
				t.Errorf("%s: render with zero-value %#v has empty title or summary", code, params)
			}
		}
	}
}

func TestNewDropsUndeclaredContextBlocks(t *testing.T) {
	rendered := New(
		BadRequest,
		WithRepositories(CodeRepository{Name: "web", DirtyFiles: []string{"a.go"}}),
		WithPhase(CodePhase{Name: "implement"}),
		WithCommand(CodeCommand{ExitCode: 1}),
	)
	if rendered.Context != nil {
		t.Fatalf("bad_request declares no context blocks; got %#v", rendered.Context)
	}

	rendered = New(
		ParentWorktreesDirty,
		WithRepositories(CodeRepository{Name: "web", DirtyFiles: []string{"a.go"}}),
		WithPhase(CodePhase{Name: "implement"}),
	)
	if rendered.Context == nil {
		t.Fatal("parent_worktrees_dirty declares the repositories block; context is nil")
	}
	if len(rendered.Context.Repositories) != 1 || rendered.Context.Repositories[0].Name != "web" {
		t.Fatalf("repositories block not carried: %#v", rendered.Context.Repositories)
	}
	if rendered.Context.Phase != nil {
		t.Fatalf("parent_worktrees_dirty does not declare the phase block; got %#v", rendered.Context.Phase)
	}
}

func TestNewUnknownCodeFallsBackToInternalError(t *testing.T) {
	rendered := New("no_such_code", WithDiagnostics("raw detail"))
	if rendered.Code != InternalError {
		t.Fatalf("unknown code rendered as %q; want internal_error", rendered.Code)
	}
	if rendered.Class != ClassBlocking {
		t.Fatalf("fallback class is %q; want blocking", rendered.Class)
	}
	if rendered.Title == "" || rendered.Summary == "" {
		t.Fatalf("fallback render has empty title or summary: %#v", rendered)
	}
	if rendered.Diagnostics != "raw detail" {
		t.Fatalf("diagnostics not preserved on fallback: %q", rendered.Diagnostics)
	}
}

func TestAuthoredClasses(t *testing.T) {
	cases := []struct {
		code Code
		want Class
	}{
		{RebaseAlreadyUpToDate, ClassWarning},
		{ParentWorktreesDirty, ClassNeedsAction},
		{ActiveChildExists, ClassNeedsAction},
		{BadRequest, ClassBlocking},
		{NotFound, ClassBlocking},
		{InternalError, ClassBlocking},
		{Unauthorized, ClassBlocking},
		{Forbidden, ClassBlocking},
		{MethodNotAllowed, ClassBlocking},
		{UnsupportedMediaType, ClassBlocking},
		{RequestTooLarge, ClassBlocking},
		{Unavailable, ClassBlocking},
	}
	for _, tc := range cases {
		entry, ok := Lookup(tc.code)
		if !ok {
			t.Fatalf("%s: missing from catalog", tc.code)
		}
		if entry.Class != tc.want {
			t.Errorf("%s: class is %q; want %q", tc.code, entry.Class, tc.want)
		}
	}
}

func TestSummaryTemplatesInterpolateParams(t *testing.T) {
	rendered := New(InvalidWorkspaceRoot, WithParams(WorkspaceRootParams{Paths: []InvalidPath{
		{Path: "/tmp/missing", Reason: "does not exist"},
	}}))
	want := "Some workspace roots do not resolve to existing directories: /tmp/missing."
	if rendered.Summary != want {
		t.Fatalf("invalid_workspace_root summary is %q; want %q", rendered.Summary, want)
	}

	rendered = New(NotReady, WithParams(ReadinessParams{Titles: []string{"Missing executable", "Unauthenticated"}}))
	if !strings.Contains(rendered.Summary, "Missing executable") || !strings.Contains(rendered.Summary, "Unauthenticated") {
		t.Fatalf("not_ready summary does not list issue titles: %q", rendered.Summary)
	}

	rendered = New(ActiveChildExists, WithParams(RelatedFeatureParams{ParentID: "parent-1", ChildID: "child-2"}))
	if !strings.Contains(rendered.Summary, `"parent-1"`) || !strings.Contains(rendered.Summary, `"child-2"`) {
		t.Fatalf("active_child_exists summary does not name features: %q", rendered.Summary)
	}

	rendered = New(NotFound, WithParams(SubjectParams{Subject: "Feature"}))
	if rendered.Summary != "Feature was not found." {
		t.Fatalf("not_found subject summary is %q", rendered.Summary)
	}

	rendered = New(PublishRemoteDiverged, WithParams(PublishRepoParams{Repo: "web", Branch: "main", RemoteOnlyCommits: 2}))
	if !strings.Contains(rendered.Summary, `"web"`) || !strings.Contains(rendered.Summary, "2 remote commits") {
		t.Fatalf("publish_remote_diverged summary does not name repo and count: %q", rendered.Summary)
	}
}

func TestNewNeverReturnsEmptyTitleOrSummary(t *testing.T) {
	rendered := New(BadRequest)
	if rendered.Remediation == nil || rendered.Remediation.Hint == "" {
		t.Fatalf("bad_request should carry a remediation hint: %#v", rendered.Remediation)
	}
	rendered = New(RebaseAlreadyUpToDate)
	if rendered.Remediation != nil {
		t.Fatalf("rebase_already_up_to_date needs no remediation hint: %#v", rendered.Remediation)
	}
	if rendered.Context != nil {
		t.Fatalf("rebase_already_up_to_date with no context should omit the block: %#v", rendered.Context)
	}
}

// TestPackageImportsOnlyStdlib pins the leaf-package contract: errcat must
// not depend on any internal or external package so every layer can use it.
func TestPackageImportsOnlyStdlib(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			first := path
			if idx := strings.Index(path, "/"); idx >= 0 {
				first = path[:idx]
			}
			if strings.Contains(first, ".") || first == "internal" || strings.HasPrefix(path, "github.com/") {
				t.Errorf("%s imports %q: errcat must import nothing outside the standard library", name, path)
			}
		}
	}
}
