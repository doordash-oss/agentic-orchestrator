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

package guidelinedef

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseGuidelineFile(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		dirName   string
		want      GuidelineDef
		wantError bool
	}{
		{
			name:    "full frontmatter",
			content: "---\nname: go\ndescription: Go best practices\nlanguage: Go\n---\nThis is the body.",
			dirName: "fallback",
			want:    GuidelineDef{Name: "go", Description: "Go best practices", Language: "Go", Body: "This is the body."},
		},
		{
			name:    "name fallback to dirName",
			content: "---\ndescription: A guideline without a name\n---\nBody text.",
			dirName: "python",
			want:    GuidelineDef{Name: "python", Description: "A guideline without a name", Language: "Python", Body: "Body text."},
		},
		{
			name:    "language fallback to titlecase of name",
			content: "---\nname: cpp\ndescription: C++ practices\n---\nBody.",
			dirName: "cpp",
			want:    GuidelineDef{Name: "cpp", Description: "C++ practices", Language: "Cpp", Body: "Body."},
		},
		{
			name:      "no frontmatter",
			content:   "Just plain text without delimiters",
			dirName:   "whatever",
			wantError: true,
		},
		{
			name:      "unterminated frontmatter",
			content:   "---\nname: broken\ndescription: no closing delimiter",
			dirName:   "broken",
			wantError: true,
		},
		{
			name:      "empty description",
			content:   "---\nname: no-desc\n---\nBody here.",
			dirName:   "no-desc",
			wantError: true,
		},
		{
			name:    "extra fields ignored",
			content: "---\nname: extra\ndescription: Has extras\nlanguage: Extra\ncolor: blue\n---\nBody.",
			dirName: "extra",
			want:    GuidelineDef{Name: "extra", Description: "Has extras", Language: "Extra", Body: "Body."},
		},
		{
			name:    "body only after closing delimiter",
			content: "---\nname: bodied\ndescription: Has body\nlanguage: Bodied\n---\n\nMulti-line\nbody content\nhere.",
			dirName: "bodied",
			want:    GuidelineDef{Name: "bodied", Description: "Has body", Language: "Bodied", Body: "Multi-line\nbody content\nhere."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGuidelineFile(tt.content, tt.dirName)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Name != tt.want.Name {
				t.Errorf("Name = %q, want %q", got.Name, tt.want.Name)
			}
			if got.Description != tt.want.Description {
				t.Errorf("Description = %q, want %q", got.Description, tt.want.Description)
			}
			if got.Language != tt.want.Language {
				t.Errorf("Language = %q, want %q", got.Language, tt.want.Language)
			}
			if got.Body != tt.want.Body {
				t.Errorf("Body = %q, want %q", got.Body, tt.want.Body)
			}
		})
	}
}

func TestParseEmbedded(t *testing.T) {
	guidelines, err := ParseEmbedded()
	if err != nil {
		t.Fatalf("ParseEmbedded() error: %v", err)
	}
	if len(guidelines) != 7 {
		t.Fatalf("ParseEmbedded() returned %d guidelines, want 7", len(guidelines))
	}

	goG, ok := guidelines["go"]
	if !ok {
		t.Fatal("missing expected guideline: go")
	}
	if goG.Name != "go" {
		t.Errorf("go Name = %q, want %q", goG.Name, "go")
	}
	if goG.Description == "" {
		t.Error("go has empty Description")
	}
	if goG.Language != "Go" {
		t.Errorf("go Language = %q, want %q", goG.Language, "Go")
	}
	if goG.Body == "" {
		t.Error("go has empty Body")
	}

	// All entries must have non-empty Description, Language, and Body
	for name, def := range guidelines {
		if def.Description == "" {
			t.Errorf("guideline %q has empty Description", name)
		}
		if def.Language == "" {
			t.Errorf("guideline %q has empty Language", name)
		}
		if def.Body == "" {
			t.Errorf("guideline %q has empty Body", name)
		}
	}
}

func TestParseEmbedded_Cached(t *testing.T) {
	a, err1 := ParseEmbedded()
	b, err2 := ParseEmbedded()
	if err1 != nil {
		t.Fatalf("first call error: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("second call error: %v", err2)
	}
	if len(a) != len(b) {
		t.Fatalf("different lengths: %d vs %d", len(a), len(b))
	}
	for name, defA := range a {
		defB, ok := b[name]
		if !ok {
			t.Errorf("second call missing guideline %q", name)
			continue
		}
		if defA != defB {
			t.Errorf("guideline %q differs between calls", name)
		}
	}
}

func TestParseEmbedded_AllLanguages(t *testing.T) {
	languages := []string{"go", "python", "javascript-typescript", "java", "cpp", "rust", "kotlin"}
	guidelines, err := ParseEmbedded()
	if err != nil {
		t.Fatalf("ParseEmbedded() error: %v", err)
	}

	for _, lang := range languages {
		t.Run(lang, func(t *testing.T) {
			g, ok := guidelines[lang]
			if !ok {
				t.Fatalf("missing guideline for %q", lang)
			}
			if g.Name == "" {
				t.Error("empty Name")
			}
			if g.Description == "" {
				t.Error("empty Description")
			}
			if g.Language == "" {
				t.Error("empty Language")
			}
			if g.Body == "" {
				t.Error("empty Body")
			}
		})
	}
}

func TestReconcileGuidelines(t *testing.T) {
	guidelinesDir := filepath.Join(t.TempDir(), "guidelines")

	// First call succeeds
	if err := ReconcileGuidelines(guidelinesDir); err != nil {
		t.Fatalf("first ReconcileGuidelines() error: %v", err)
	}

	// Verify all 7 guideline directories exist
	entries, err := os.ReadDir(guidelinesDir)
	if err != nil {
		t.Fatalf("reading guidelines dir: %v", err)
	}
	if len(entries) != 7 {
		t.Fatalf("expected 7 guideline directories, got %d", len(entries))
	}

	// go/index.md exists with correct content
	guidelinePath := filepath.Join(guidelinesDir, "go", "index.md")
	data, err := os.ReadFile(guidelinePath)
	if err != nil {
		t.Fatalf("reading reconciled file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "name: go") {
		t.Error("reconciled file missing 'name: go'")
	}
	if !strings.Contains(content, "description:") {
		t.Error("reconciled file missing 'description:'")
	}

	// Idempotent on second call (modtime unchanged)
	info, err := os.Stat(guidelinePath)
	if err != nil {
		t.Fatalf("stat reconciled file: %v", err)
	}
	before := info.ModTime()
	// Retained: creates a visible filesystem mtime boundary for idempotence.
	time.Sleep(20 * time.Millisecond)
	if err := ReconcileGuidelines(guidelinesDir); err != nil {
		t.Fatalf("second ReconcileGuidelines() error: %v", err)
	}
	info, err = os.Stat(guidelinePath)
	if err != nil {
		t.Fatalf("stat after second reconcile: %v", err)
	}
	after := info.ModTime()
	if !before.Equal(after) {
		t.Error("modtime changed on idempotent reconcile")
	}

	// No leftover temp files
	matches, _ := filepath.Glob(filepath.Join(guidelinesDir, "*", ".tmp.*"))
	if len(matches) > 0 {
		t.Errorf("leftover temp files: %v", matches)
	}

	// Verify sub-files are written for languages that have them (e.g., Go)
	subFile := filepath.Join(guidelinesDir, "go", "error-handling", "index.md")
	if _, err := os.Stat(subFile); err != nil {
		t.Errorf("expected sub-file at %s: %v", subFile, err)
	}
}

func TestReconcileGuidelines_CreatesSubdirectories(t *testing.T) {
	guidelinesDir := filepath.Join(t.TempDir(), "deep", "nested", "guidelines")
	if err := ReconcileGuidelines(guidelinesDir); err != nil {
		t.Fatalf("ReconcileGuidelines() error: %v", err)
	}
	guidelinePath := filepath.Join(guidelinesDir, "go", "index.md")
	if _, err := os.Stat(guidelinePath); err != nil {
		t.Errorf("expected file at %s: %v", guidelinePath, err)
	}
}

func TestReconcileGuidelines_PartialFailureThenRetry(t *testing.T) {
	guidelinesDir := filepath.Join(t.TempDir(), "guidelines")
	os.MkdirAll(filepath.Join(guidelinesDir, "go"), 0o755)
	// Block index.md path with a directory
	blockedPath := filepath.Join(guidelinesDir, "go", "index.md")
	os.Mkdir(blockedPath, 0o755)

	err := ReconcileGuidelines(guidelinesDir)
	if err == nil {
		t.Fatal("ReconcileGuidelines() should fail with blocked path")
	}

	// Unblock and retry
	os.RemoveAll(blockedPath)
	if err := ReconcileGuidelines(guidelinesDir); err != nil {
		t.Fatalf("retry ReconcileGuidelines() error: %v", err)
	}

	// Assert file now has correct content
	data, err := os.ReadFile(filepath.Join(guidelinesDir, "go", "index.md"))
	if err != nil {
		t.Fatalf("reading retried file: %v", err)
	}
	if !strings.Contains(string(data), "name: go") {
		t.Error("retried file missing expected content")
	}
}

func TestBuildPreamble(t *testing.T) {
	guidelinesDir := "/test/path/to/guidelines"
	preamble := BuildPreamble(guidelinesDir)

	for _, want := range []string{
		"## Available Language Guidelines",
		"/test/path/to/guidelines/go/index.md",
		"Go",
		"Python",
		"JavaScript/TypeScript",
		"Java",
		"C++",
		"Rust",
		"Kotlin",
		"Do NOT stop at the top-level index.md",
	} {
		if !strings.Contains(preamble, want) {
			t.Errorf("preamble missing %q", want)
		}
	}

	// Verify descriptions from embedded data are present
	guidelines, _ := ParseEmbedded()
	goG := guidelines["go"]
	if !strings.Contains(preamble, goG.Description) {
		t.Errorf("preamble missing go description %q", goG.Description)
	}
}

func TestBuildPreamble_DifferentPaths(t *testing.T) {
	a := BuildPreamble("/path/alpha/guidelines")
	b := BuildPreamble("/path/beta/guidelines")
	if a == b {
		t.Error("expected different preambles for different guidelinesDir values")
	}
	if !strings.Contains(a, "/path/alpha/guidelines") {
		t.Error("preamble A missing alpha path")
	}
	if !strings.Contains(b, "/path/beta/guidelines") {
		t.Error("preamble B missing beta path")
	}
}

func TestBuildPreamble_AlphabeticalOrder(t *testing.T) {
	preamble := BuildPreamble("/test/guidelines")

	// Verify that entries appear in alphabetical order by name key
	expectedOrder := []string{"C++", "Go", "Java", "JavaScript/TypeScript", "Kotlin", "Python", "Rust"}
	lastIdx := -1
	for _, lang := range expectedOrder {
		idx := strings.Index(preamble, lang)
		if idx == -1 {
			t.Errorf("preamble missing language %q", lang)
			continue
		}
		if idx <= lastIdx {
			t.Errorf("language %q is not in alphabetical order", lang)
		}
		lastIdx = idx
	}
}

func TestBuildInlineContent(t *testing.T) {
	guidelinesDir := filepath.Join(t.TempDir(), "guidelines")
	if err := ReconcileGuidelines(guidelinesDir); err != nil {
		t.Fatalf("ReconcileGuidelines() error: %v", err)
	}

	content := BuildInlineContent(guidelinesDir)
	if content == "" {
		t.Fatal("BuildInlineContent returned empty string")
	}

	// Should contain the header
	if !strings.Contains(content, "## Language Guidelines") {
		t.Error("missing '## Language Guidelines' header")
	}

	// Should contain body content from each language
	for _, check := range []string{
		"Go Guidelines",
		"Python Guidelines",
		"JavaScript/TypeScript Guidelines",
		"Java Guidelines",
		"C++ Guidelines",
		"Rust Guidelines",
		"Kotlin Guidelines",
	} {
		if !strings.Contains(content, check) {
			t.Errorf("inline content missing %q", check)
		}
	}
}

func TestBuildInlineContent_Empty(t *testing.T) {
	content := BuildInlineContent("")
	if content != "" {
		t.Error("expected empty for empty guidelinesDir")
	}
}
