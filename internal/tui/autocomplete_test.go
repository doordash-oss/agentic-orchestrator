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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectTrigger(t *testing.T) {
	tests := []struct {
		name         string
		value        string
		cursorOffset int
		wantOK       bool
		wantMode     AutocompleteMode
		wantOffset   int
		wantQuery    string
	}{
		{
			name:         "SlashAtStart",
			value:        "/fo",
			cursorOffset: 3,
			wantOK:       true,
			wantMode:     AutocompleteSkill,
			wantOffset:   0,
			wantQuery:    "fo",
		},
		{
			name:         "SlashAfterSpace",
			value:        "hello /fo",
			cursorOffset: 9,
			wantOK:       true,
			wantMode:     AutocompleteSkill,
			wantOffset:   6,
			wantQuery:    "fo",
		},
		{
			name:         "SlashAfterNewline",
			value:        "line1\n/fo",
			cursorOffset: 9,
			wantOK:       true,
			wantMode:     AutocompleteSkill,
			wantOffset:   6,
			wantQuery:    "fo",
		},
		{
			name:         "NoTriggerMidWord",
			value:        "path/to",
			cursorOffset: 7,
			wantOK:       false,
		},
		{
			name:         "AtSignAtStart",
			value:        "@sr",
			cursorOffset: 3,
			wantOK:       true,
			wantMode:     AutocompleteFile,
			wantOffset:   0,
			wantQuery:    "sr",
		},
		{
			name:         "AtSignAfterSpace",
			value:        "use @file",
			cursorOffset: 9,
			wantOK:       true,
			wantMode:     AutocompleteFile,
			wantOffset:   4,
			wantQuery:    "file",
		},
		{
			name:         "EmptyQuery",
			value:        "/",
			cursorOffset: 1,
			wantOK:       true,
			wantMode:     AutocompleteSkill,
			wantOffset:   0,
			wantQuery:    "",
		},
		{
			name:         "NoTrigger",
			value:        "hello",
			cursorOffset: 5,
			wantOK:       false,
		},
		{
			name:         "AtSignWithSlashInPath",
			value:        "@src/ma",
			cursorOffset: 7,
			wantOK:       true,
			wantMode:     AutocompleteFile,
			wantOffset:   0,
			wantQuery:    "src/ma",
		},
		{
			name:         "AtSignWithSlashAfterSpace",
			value:        "use @src/main.go",
			cursorOffset: 16,
			wantOK:       true,
			wantMode:     AutocompleteFile,
			wantOffset:   4,
			wantQuery:    "src/main.go",
		},
		{
			name:         "AtSignWithNestedPath",
			value:        "@internal/tui/attach.go",
			cursorOffset: 23,
			wantOK:       true,
			wantMode:     AutocompleteFile,
			wantOffset:   0,
			wantQuery:    "internal/tui/attach.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, offset, query, ok := detectTrigger(tt.value, tt.cursorOffset)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if mode != tt.wantMode {
				t.Errorf("mode = %v, want %v", mode, tt.wantMode)
			}
			if offset != tt.wantOffset {
				t.Errorf("offset = %d, want %d", offset, tt.wantOffset)
			}
			if query != tt.wantQuery {
				t.Errorf("query = %q, want %q", query, tt.wantQuery)
			}
		})
	}
}

func TestCursorByteOffset_SingleLine(t *testing.T) {
	got := cursorByteOffset("hello", 0, 3)
	if got != 3 {
		t.Fatalf("cursorByteOffset(\"hello\", 0, 3) = %d, want 3", got)
	}
}

func TestCursorByteOffset_MultiLine(t *testing.T) {
	// "line1\nline2" — row 1, col 3 → offset past "line1\n" (6) + 3 = 9
	got := cursorByteOffset("line1\nline2", 1, 3)
	if got != 9 {
		t.Fatalf("cursorByteOffset(\"line1\\nline2\", 1, 3) = %d, want 9", got)
	}
}

func TestAutocomplete_ActivateAndFilter(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "commit", Description: "Create a git commit"},
		{Name: "review-pr", Description: "Review a pull request"},
		{Name: "debug", Description: "Debug issues"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "co", items)

	if !m.active {
		t.Fatal("expected active to be true")
	}
	if len(m.filtered) != 1 {
		t.Fatalf("filtered length = %d, want 1", len(m.filtered))
	}
	if m.filtered[0].Name != "commit" {
		t.Errorf("filtered[0].Name = %q, want \"commit\"", m.filtered[0].Name)
	}
}

func TestAutocomplete_ActivateWithEmptyItems(t *testing.T) {
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "", nil)

	if !m.active {
		t.Fatal("expected active to be true")
	}
	if len(m.filtered) != 0 {
		t.Fatalf("filtered length = %d, want 0", len(m.filtered))
	}
	view := m.View(40)
	if !strings.Contains(view, "No results") {
		t.Errorf("View should contain \"No results\", got: %q", view)
	}
}

func TestAutocomplete_MoveUpDown(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "alpha"},
		{Name: "bravo"},
		{Name: "charlie"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "", items)

	if m.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.cursor)
	}

	// Move down once.
	m = m.MoveDown()
	if m.cursor != 1 {
		t.Errorf("after MoveDown: cursor = %d, want 1", m.cursor)
	}

	// Move down twice more to wrap around.
	m = m.MoveDown()
	m = m.MoveDown()
	if m.cursor != 0 {
		t.Errorf("after wrapping MoveDown: cursor = %d, want 0", m.cursor)
	}

	// Move up from 0 wraps to bottom.
	m = m.MoveUp()
	if m.cursor != 2 {
		t.Errorf("after MoveUp wrap: cursor = %d, want 2", m.cursor)
	}
}

func TestAutocomplete_MoveUpDown_EmptyFiltered(t *testing.T) {
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "zzz", []AutocompleteItem{
		{Name: "alpha"},
	})

	if len(m.filtered) != 0 {
		t.Fatalf("filtered length = %d, want 0", len(m.filtered))
	}

	before := m.cursor
	m = m.MoveUp()
	if m.cursor != before {
		t.Errorf("MoveUp on empty: cursor changed from %d to %d", before, m.cursor)
	}
	m = m.MoveDown()
	if m.cursor != before {
		t.Errorf("MoveDown on empty: cursor changed from %d to %d", before, m.cursor)
	}
}

func TestAutocomplete_Selected(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "alpha"},
		{Name: "bravo"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "", items)
	m = m.MoveDown()

	sel := m.Selected()
	if sel == nil {
		t.Fatal("Selected returned nil")
	}
	if sel.Name != "bravo" {
		t.Errorf("Selected().Name = %q, want \"bravo\"", sel.Name)
	}
}

func TestAutocomplete_Selected_EmptyFiltered(t *testing.T) {
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "zzz", []AutocompleteItem{
		{Name: "alpha"},
	})

	if sel := m.Selected(); sel != nil {
		t.Errorf("Selected on empty filtered = %v, want nil", sel)
	}
}

func TestAutocomplete_Dismiss(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "alpha"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "a", items)
	m = m.Dismiss()

	if m.active {
		t.Error("active should be false after Dismiss")
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0 after Dismiss", m.cursor)
	}
	if len(m.items) != 0 {
		t.Errorf("items length = %d, want 0 after Dismiss", len(m.items))
	}
	if m.query != "" {
		t.Errorf("query = %q, want \"\" after Dismiss", m.query)
	}
}

func TestAutocomplete_UpdateQuery_FiltersItems(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "commit"},
		{Name: "create"},
		{Name: "debug"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "c", items)

	if len(m.filtered) != 2 {
		t.Fatalf("after 'c': filtered length = %d, want 2", len(m.filtered))
	}

	m = m.UpdateQuery("co")
	if len(m.filtered) != 1 {
		t.Fatalf("after 'co': filtered length = %d, want 1", len(m.filtered))
	}
	if m.filtered[0].Name != "commit" {
		t.Errorf("filtered[0].Name = %q, want \"commit\"", m.filtered[0].Name)
	}
}

func TestAutocomplete_UpdateQuery_NoMatch_StaysActive(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "commit"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "", items)
	m = m.UpdateQuery("zzz")

	if !m.active {
		t.Error("expected active to remain true with no matches")
	}
	if len(m.filtered) != 0 {
		t.Errorf("filtered length = %d, want 0", len(m.filtered))
	}
}

func TestAutocomplete_View_RendersItems(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "commit", Description: "Create a git commit"},
		{Name: "debug", Description: "Debug issues"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "", items)

	view := m.View(60)
	if !strings.Contains(view, "commit") {
		t.Errorf("View missing \"commit\": %q", view)
	}
	if !strings.Contains(view, "debug") {
		t.Errorf("View missing \"debug\": %q", view)
	}
}

func TestAutocomplete_View_HighlightsCursor(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "alpha"},
		{Name: "bravo"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "", items)

	view := m.View(40)
	if view == "" {
		t.Fatal("View returned empty string")
	}
	// The cursor item (alpha at index 0) should be rendered; we just
	// verify the view is non-empty and contains the item name.
	if !strings.Contains(view, "alpha") {
		t.Errorf("View missing cursor item \"alpha\": %q", view)
	}
}

func TestAutocomplete_View_EmptyState(t *testing.T) {
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "zzz", []AutocompleteItem{
		{Name: "alpha"},
	})

	view := m.View(40)
	if !strings.Contains(view, "No results") {
		t.Errorf("View should contain \"No results\" when filtered is empty, got: %q", view)
	}
}

func TestAutocomplete_FilterItems_PrefixMatch(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "commit"},
		{Name: "debug"},
	}
	result := filterItems(items, "co", AutocompleteSkill)
	if len(result) != 1 {
		t.Fatalf("filterItems length = %d, want 1", len(result))
	}
	if result[0].Name != "commit" {
		t.Errorf("result[0].Name = %q, want \"commit\"", result[0].Name)
	}
}

func TestAutocomplete_FilterItems_CaseInsensitive(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "commit"},
		{Name: "debug"},
	}
	result := filterItems(items, "CO", AutocompleteSkill)
	if len(result) != 1 {
		t.Fatalf("filterItems length = %d, want 1", len(result))
	}
	if result[0].Name != "commit" {
		t.Errorf("result[0].Name = %q, want \"commit\"", result[0].Name)
	}
}

func TestAutocomplete_FilterItems_EmptyQuery(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "alpha"},
		{Name: "bravo"},
		{Name: "charlie"},
	}
	result := filterItems(items, "", AutocompleteSkill)
	if len(result) != len(items) {
		t.Fatalf("filterItems length = %d, want %d", len(result), len(items))
	}
	for i, item := range result {
		if item.Name != items[i].Name {
			t.Errorf("result[%d].Name = %q, want %q", i, item.Name, items[i].Name)
		}
	}
}

func TestDiscoverAllSkills_EmbeddedSkills(t *testing.T) {
	// Use an empty global dir to isolate from the user's real ~/.claude/commands/.
	emptyGlobal := t.TempDir()
	items := discoverAllSkillsWith("", emptyGlobal, "")
	if len(items) == 0 {
		t.Fatal("discoverAllSkillsWith returned no items; expected embedded skills")
	}

	// Check for well-known embedded skills.
	found := make(map[string]bool)
	for _, item := range items {
		found[item.Name] = true
		if item.Source != "agentic" {
			t.Errorf("item %q: Source = %q, want \"agentic\"", item.Name, item.Source)
		}
	}
	for _, name := range []string{"implement", "frontend-design"} {
		if !found[name] {
			t.Errorf("expected well-known embedded skill %q not found", name)
		}
	}
}

func TestDiscoverAllSkills_GlobalCommands(t *testing.T) {
	dir := t.TempDir()

	// Flat command
	os.WriteFile(filepath.Join(dir, "deploy.md"), []byte("---\ndescription: Deploy app\n---\nBody"), 0o644)

	// Nested command
	nested := filepath.Join(dir, "ci")
	os.MkdirAll(nested, 0o755)
	os.WriteFile(filepath.Join(nested, "test.md"), []byte("---\ndescription: Run CI tests\n---\n"), 0o644)

	items := discoverGlobalCommandsFrom(dir)
	if len(items) != 2 {
		t.Fatalf("discoverGlobalCommandsFrom returned %d items, want 2", len(items))
	}

	byName := make(map[string]AutocompleteItem)
	for _, item := range items {
		byName[item.Name] = item
	}

	if item, ok := byName["deploy"]; !ok {
		t.Error("missing flat command 'deploy'")
	} else {
		if item.Source != "global" {
			t.Errorf("deploy.Source = %q, want \"global\"", item.Source)
		}
		if item.Description != "Deploy app" {
			t.Errorf("deploy.Description = %q, want \"Deploy app\"", item.Description)
		}
	}

	if item, ok := byName["ci:test"]; !ok {
		t.Error("missing nested command 'ci:test'")
	} else {
		if item.Description != "Run CI tests" {
			t.Errorf("ci:test.Description = %q, want \"Run CI tests\"", item.Description)
		}
	}
}

func TestDiscoverAllSkills_RepoItems(t *testing.T) {
	dir := t.TempDir()
	cmdDir := filepath.Join(dir, ".claude", "commands")
	os.MkdirAll(cmdDir, 0o755)
	os.WriteFile(filepath.Join(cmdDir, "foo.md"), []byte("---\ndescription: Foo cmd\n---\n"), 0o644)

	emptyGlobal := t.TempDir()
	items := discoverAllSkillsWith(dir, emptyGlobal, "")

	var found bool
	for _, item := range items {
		if item.Name == "foo" {
			found = true
			if item.Source != "repo" {
				t.Errorf("foo.Source = %q, want \"repo\"", item.Source)
			}
			if item.Description != "Foo cmd" {
				t.Errorf("foo.Description = %q, want \"Foo cmd\"", item.Description)
			}
			break
		}
	}
	if !found {
		t.Error("repo item 'foo' not found in discoverAllSkills results")
	}
}

func TestDiscoverAllSkills_KeepsDuplicates(t *testing.T) {
	dir := t.TempDir()
	cmdDir := filepath.Join(dir, ".claude", "commands")
	os.MkdirAll(cmdDir, 0o755)
	// "implement" exists in embedded skills; repo version should also appear.
	os.WriteFile(filepath.Join(cmdDir, "implement.md"), []byte("---\ndescription: Repo implement\n---\n"), 0o644)

	emptyGlobal := t.TempDir()
	items := discoverAllSkillsWith(dir, emptyGlobal, "")

	var matched []AutocompleteItem
	for _, item := range items {
		if item.Name == "implement" {
			matched = append(matched, item)
		}
	}
	if len(matched) < 2 {
		t.Fatalf("expected at least 2 'implement' items (repo + built-in), got %d", len(matched))
	}

	sources := make(map[string]bool)
	for _, item := range matched {
		sources[item.Source] = true
	}
	if !sources["repo"] {
		t.Error("missing 'implement' item with Source=\"repo\"")
	}
	if !sources["agentic"] {
		t.Error("missing 'implement' item with Source=\"agentic\"")
	}

	// Repo should sort before built-in for the same name.
	if matched[0].Source != "repo" {
		t.Errorf("first implement item Source = %q, want \"repo\" (highest priority first)", matched[0].Source)
	}
}

func TestDiscoverAllSkills_SortedByNameThenSource(t *testing.T) {
	emptyGlobal := t.TempDir()
	items := discoverAllSkillsWith("", emptyGlobal, "")
	for i := 1; i < len(items); i++ {
		if items[i].Name < items[i-1].Name {
			t.Errorf("items not sorted by name: [%d].Name=%q < [%d].Name=%q", i, items[i].Name, i-1, items[i-1].Name)
		}
		if items[i].Name == items[i-1].Name {
			if sourcePriority(items[i].Source) < sourcePriority(items[i-1].Source) {
				t.Errorf("same-name items not sorted by source: [%d]=%q(%s) before [%d]=%q(%s)",
					i-1, items[i-1].Name, items[i-1].Source, i, items[i].Name, items[i].Source)
			}
		}
	}
}

func TestDiscoverGlobalCommands_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	items := discoverGlobalCommandsFrom(dir)
	if len(items) != 0 {
		t.Fatalf("expected 0 items for empty dir, got %d", len(items))
	}
}

func TestDiscoverGlobalCommands_NonexistentDir(t *testing.T) {
	items := discoverGlobalCommandsFrom("/nonexistent/path/that/does/not/exist")
	if len(items) != 0 {
		t.Fatalf("expected 0 items for nonexistent dir, got %d", len(items))
	}
}

func TestDiscoverGlobalCommands_ParsesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mycmd.md"), []byte("---\ndescription: My command\n---\nContent"), 0o644)

	items := discoverGlobalCommandsFrom(dir)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Description != "My command" {
		t.Errorf("Description = %q, want \"My command\"", items[0].Description)
	}
}

func TestDiscoverGlobalCommands_NestedPaths(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "ci")
	os.MkdirAll(nested, 0o755)
	os.WriteFile(filepath.Join(nested, "deploy.md"), []byte("---\ndescription: CI deploy\n---\n"), 0o644)

	items := discoverGlobalCommandsFrom(dir)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Name != "ci:deploy" {
		t.Errorf("Name = %q, want \"ci:deploy\"", items[0].Name)
	}
}

func TestAutocomplete_ActivateLoading(t *testing.T) {
	m := AutocompleteModel{}
	m = m.ActivateLoading(AutocompleteSkill, 0, "co")

	if !m.active {
		t.Error("expected active to be true")
	}
	if !m.loading {
		t.Error("expected loading to be true")
	}
	if len(m.filtered) != 0 {
		t.Errorf("filtered length = %d, want 0", len(m.filtered))
	}
	if m.query != "co" {
		t.Errorf("query = %q, want \"co\"", m.query)
	}
}

func TestAutocomplete_View_LoadingState(t *testing.T) {
	m := AutocompleteModel{}
	m = m.ActivateLoading(AutocompleteSkill, 0, "")

	view := m.View(40)
	if !strings.Contains(view, "Loading skills...") {
		t.Errorf("View should contain \"Loading skills...\", got: %q", view)
	}
	if strings.Contains(view, "No results") {
		t.Error("View should not contain \"No results\" when loading")
	}
}

func TestAutocomplete_Activate_ClearsLoading(t *testing.T) {
	m := AutocompleteModel{}
	m = m.ActivateLoading(AutocompleteSkill, 0, "")
	if !m.loading {
		t.Fatal("precondition: loading should be true")
	}

	items := []AutocompleteItem{{Name: "commit", Description: "test"}}
	m = m.Activate(AutocompleteSkill, 0, "", items)

	if m.loading {
		t.Error("loading should be false after Activate")
	}
	if len(m.filtered) != 1 {
		t.Errorf("filtered length = %d, want 1", len(m.filtered))
	}
}

func TestAutocomplete_SetLoading(t *testing.T) {
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "", nil)

	m = m.SetLoading(true)
	if !m.Loading() {
		t.Error("Loading() should be true after SetLoading(true)")
	}

	m = m.SetLoading(false)
	if m.Loading() {
		t.Error("Loading() should be false after SetLoading(false)")
	}
}

func TestAutocomplete_View_LoadingFileMode(t *testing.T) {
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteFile, 0, "", nil)
	m = m.SetLoading(true)

	view := m.View(40)
	if !strings.Contains(view, "Loading files...") {
		t.Errorf("View should contain \"Loading files...\", got: %q", view)
	}
	if strings.Contains(view, "No results") {
		t.Error("View should not contain \"No results\" when loading")
	}
	if strings.Contains(view, "Loading skills...") {
		t.Error("View should not contain \"Loading skills...\" in file mode")
	}
}

func TestAutocomplete_View_LoadingWithResults(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "src/main.go", Description: "src/main.go", Source: "file"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteFile, 0, "", items)
	m = m.SetLoading(true)

	view := m.View(40)
	if !strings.Contains(view, "src/main.go") {
		t.Errorf("View should show results when loading but items exist, got: %q", view)
	}
	if strings.Contains(view, "Loading files...") {
		t.Error("Loading message should not appear when results are available")
	}
}

func TestAutocomplete_FilterItems_FileMode(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "src/main.go", Source: "file"},
		{Name: "cmd/app.go", Source: "file"},
	}
	result := filterItems(items, "main", AutocompleteFile)
	if len(result) != len(items) {
		t.Fatalf("filterItems file mode: got %d items, want %d (identity pass-through)", len(result), len(items))
	}
	for i, item := range result {
		if item.Name != items[i].Name {
			t.Errorf("result[%d].Name = %q, want %q", i, item.Name, items[i].Name)
		}
	}
}

func TestAutocomplete_View_ScrollsWithCursor(t *testing.T) {
	// Create more items than autocompleteMaxVisible (5).
	items := []AutocompleteItem{
		{Name: "alpha", Source: "agentic"},
		{Name: "bravo", Source: "agentic"},
		{Name: "charlie", Source: "agentic"},
		{Name: "delta", Source: "agentic"},
		{Name: "echo", Source: "agentic"},
		{Name: "foxtrot", Source: "agentic"},
		{Name: "golf", Source: "agentic"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "", items)

	// Move cursor past the visible window.
	for range 6 {
		m = m.MoveDown()
	}
	// Cursor should now be on "golf" (index 6).
	if sel := m.Selected(); sel == nil || sel.Name != "golf" {
		t.Fatalf("expected cursor on 'golf', got %v", sel)
	}

	view := m.View(80)
	stripped := ansiRegex.ReplaceAllString(view, "")

	// "golf" must be visible since the cursor is on it.
	if !strings.Contains(stripped, "golf") {
		t.Errorf("View should show 'golf' when cursor is on it, got: %q", stripped)
	}
	// "alpha" should have scrolled out of view.
	if strings.Contains(stripped, "alpha") {
		t.Errorf("View should not show 'alpha' when scrolled to bottom, got: %q", stripped)
	}
}

func TestAutocomplete_View_ScrollUpSymmetry(t *testing.T) {
	// Verify that going up moves the cursor within the window first,
	// then scrolls — symmetric with the down behavior.
	items := []AutocompleteItem{
		{Name: "alpha", Source: "agentic"},
		{Name: "bravo", Source: "agentic"},
		{Name: "charlie", Source: "agentic"},
		{Name: "delta", Source: "agentic"},
		{Name: "echo", Source: "agentic"},
		{Name: "foxtrot", Source: "agentic"},
		{Name: "golf", Source: "agentic"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "", items)

	// Move to the bottom (golf, index 6).
	for range 6 {
		m = m.MoveDown()
	}

	// Now move up once — cursor should go to foxtrot (index 5) but the
	// window should NOT scroll: golf stays visible at the bottom.
	m = m.MoveUp()
	if sel := m.Selected(); sel == nil || sel.Name != "foxtrot" {
		t.Fatalf("expected cursor on 'foxtrot', got %v", sel)
	}
	view := m.View(80)
	stripped := ansiRegex.ReplaceAllString(view, "")
	if !strings.Contains(stripped, "golf") {
		t.Errorf("'golf' should still be visible after one MoveUp, got: %q", stripped)
	}

	// Keep moving up — cursor should reach the top of the window before
	// the window scrolls. After 4 more MoveUp calls the cursor is at
	// bravo (index 1); the window should now show bravo through foxtrot.
	for range 4 {
		m = m.MoveUp()
	}
	if sel := m.Selected(); sel == nil || sel.Name != "bravo" {
		t.Fatalf("expected cursor on 'bravo', got %v", sel)
	}
	view = m.View(80)
	stripped = ansiRegex.ReplaceAllString(view, "")
	if !strings.Contains(stripped, "bravo") {
		t.Errorf("'bravo' should be visible, got: %q", stripped)
	}
	// alpha should have scrolled out since the window only scrolled when
	// cursor went above it.
	if strings.Contains(stripped, "alpha") {
		t.Errorf("'alpha' should not be visible yet (cursor at bravo, window hasn't scrolled that far), got: %q", stripped)
	}
}

func TestSourceDisplayLabel(t *testing.T) {
	tests := []struct {
		source string
		want   string
	}{
		{"agentic", "built-in"},
		{"repo", "repo"},
		{"global", "global"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			got := sourceDisplayLabel(tt.source)
			if got != tt.want {
				t.Errorf("sourceDisplayLabel(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

func TestSourcePriority(t *testing.T) {
	if sourcePriority("repo") >= sourcePriority("global") {
		t.Error("repo should have lower priority value (sort first) than global")
	}
	if sourcePriority("global") >= sourcePriority("agentic") {
		t.Error("global should have lower priority value than agentic")
	}
}

func TestAutocomplete_View_ShowsSourceLabel(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "commit", Description: "Create a git commit", Source: "agentic"},
		{Name: "commit", Description: "Repo commit", Source: "repo"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteSkill, 0, "", items)

	view := m.View(80)
	stripped := ansiRegex.ReplaceAllString(view, "")

	if !strings.Contains(stripped, "(built-in)") {
		t.Errorf("View missing source label \"(built-in)\": %q", stripped)
	}
	if !strings.Contains(stripped, "(repo)") {
		t.Errorf("View missing source label \"(repo)\": %q", stripped)
	}
}

func TestAutocomplete_View_NoSourceLabelForFiles(t *testing.T) {
	items := []AutocompleteItem{
		{Name: "src/main.go", Description: "src/main.go", Source: "file"},
	}
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteFile, 0, "", items)

	view := m.View(80)
	stripped := ansiRegex.ReplaceAllString(view, "")

	if strings.Contains(stripped, "(file)") {
		t.Errorf("View should not show source label for file mode: %q", stripped)
	}
}

func TestAutocomplete_Accessors(t *testing.T) {
	m := AutocompleteModel{}
	m = m.Activate(AutocompleteFile, 5, "main", nil)

	if !m.Active() {
		t.Error("Active() should be true")
	}
	if m.Mode() != AutocompleteFile {
		t.Errorf("Mode() = %d, want AutocompleteFile", m.Mode())
	}
	if m.Query() != "main" {
		t.Errorf("Query() = %q, want \"main\"", m.Query())
	}
	if m.TriggerOffset() != 5 {
		t.Errorf("TriggerOffset() = %d, want 5", m.TriggerOffset())
	}
}
