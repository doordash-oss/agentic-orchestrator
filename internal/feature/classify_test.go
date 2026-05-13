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
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestClassify_FrontendKeywords(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	tests := []struct {
		name        string
		description string
		wantTag     string
	}{
		{"frontend word", "Build a new frontend for the dashboard", TagFrontend},
		{"ui word", "Redesign the UI to be simpler", TagFrontend},
		{"tui word", "Add a TUI for managing features", TagFrontend},
		{"native app phrase", "Replace the TUI with a native app", TagFrontend},
		{"react framework", "Build a React web component", TagFrontend},
		{"tailwind library", "Migrate styles to Tailwind CSS", TagFrontend},
		{"bubbletea library", "Add a Bubbletea chat screen", TagFrontend},
		{"dashboard word", "Build a dashboard view", TagFrontend},
		{"api word (backend)", "Add an API endpoint for orders", TagBackend},
		{"cli word", "Add a CLI subcommand to list features", TagCLI},
		{"infra word", "Set up a CI/CD pipeline on GitHub", TagInfra},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.description, nil, nil)
			if !slices.Contains(got, tt.wantTag) {
				t.Errorf("Classify(%q) = %v, want tag %q", tt.description, got, tt.wantTag)
			}
		})
	}
}

func TestClassify_ImagesImplyFrontend(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	got := Classify("Refactor internal helpers", []string{"/tmp/mockup.png"}, nil)
	if !slices.Contains(got, TagFrontend) {
		t.Errorf("Classify with image attached did not produce frontend tag; got %v", got)
	}
}

func TestClassify_DedupAndSort(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	// "frontend" and "ui" both imply frontend; image attaches frontend too.
	// Expect a single frontend tag in the result, sorted.
	got := Classify("Build a frontend UI with React components", []string{"/tmp/design.png"}, nil)
	count := 0
	for _, tag := range got {
		if tag == TagFrontend {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Classify produced %d frontend tags, want 1; got %v", count, got)
	}
	// Verify sort
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Errorf("Classify result not sorted: %v", got)
		}
	}
}

func TestClassify_WordBoundary(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	// "api" in "rapid" must not trigger backend; "ui" in "fluid" must not
	// trigger frontend. Regression guard against greedy substring matching.
	got := Classify("Make the rapid fluid animation smoother with better tracking", nil, nil)
	if slices.Contains(got, TagBackend) {
		t.Errorf("Classify matched 'api' inside 'rapid'; tags=%v", got)
	}
	if slices.Contains(got, TagFrontend) {
		t.Errorf("Classify matched 'ui' inside 'fluid'; tags=%v", got)
	}
}

func TestClassify_RepoWithFrontendFiles(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	dir := t.TempDir()
	// Create a .tsx file at a shallow depth
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "App.tsx"), []byte("export {}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	repos := []FeatureRepo{{Name: "r", Path: dir}}
	got := Classify("Refactor some helper functions", nil, repos)
	if !slices.Contains(got, TagFrontend) {
		t.Errorf("Classify did not detect frontend from repo contents; got %v", got)
	}
}

func TestClassify_PureBackendDescription(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	got := Classify("Add a database migration for the orders table", nil, nil)
	if slices.Contains(got, TagFrontend) {
		t.Errorf("Classify tagged pure backend feature as frontend; got %v", got)
	}
	if !slices.Contains(got, TagBackend) {
		t.Errorf("Classify missed backend tag; got %v", got)
	}
}

func TestHasTag(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure value, table-driven, or per-test temp-dir assertions with no shared state.
	f := &Feature{Tags: []string{TagFrontend, TagBackend}}
	if !f.HasTag(TagFrontend) {
		t.Error("HasTag(frontend) = false on feature with frontend tag")
	}
	if f.HasTag(TagCLI) {
		t.Error("HasTag(cli) = true on feature without cli tag")
	}
	if (&Feature{}).HasTag(TagFrontend) {
		t.Error("HasTag on empty Feature returned true")
	}
}
