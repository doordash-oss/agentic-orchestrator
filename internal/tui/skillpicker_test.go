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

	tea "charm.land/bubbletea/v2"
)

// setupTestRepos creates a temp directory with realistic .claude/ structures.
// Returns the temp dir and a map of repo names to paths.
//
//	repo-a/
//	  .claude/
//	    skills/validate-api/SKILL.md  (name: validate-api, description: Validate API endpoints)
//	    commands/commit.md            (description: Create a git commit)
//	    commands/ci/deploy.md         (description: Deploy to CI)
//	    agents/analyzer.md            (name: analyzer, description: Code analysis)
//	repo-b/
//	  .claude/
//	    commands/lint.md              (description: Run linters)
func setupTestRepos(t *testing.T) (map[string]string, map[string]bool) {
	t.Helper()
	dir := t.TempDir()

	// repo-a
	repoA := filepath.Join(dir, "repo-a")
	mkdirAll(t, filepath.Join(repoA, ".claude", "skills", "validate-api"))
	writeFile(t, filepath.Join(repoA, ".claude", "skills", "validate-api", "SKILL.md"),
		"---\nname: validate-api\ndescription: Validate API endpoints\n---\nSkill body")
	mkdirAll(t, filepath.Join(repoA, ".claude", "commands", "ci"))
	writeFile(t, filepath.Join(repoA, ".claude", "commands", "commit.md"),
		"---\ndescription: Create a git commit\n---\nCommand body")
	writeFile(t, filepath.Join(repoA, ".claude", "commands", "ci", "deploy.md"),
		"---\ndescription: Deploy to CI\n---\nCommand body")
	mkdirAll(t, filepath.Join(repoA, ".claude", "agents"))
	writeFile(t, filepath.Join(repoA, ".claude", "agents", "analyzer.md"),
		"---\nname: analyzer\ndescription: Code analysis\n---\nAgent body")

	// repo-b
	repoB := filepath.Join(dir, "repo-b")
	mkdirAll(t, filepath.Join(repoB, ".claude", "commands"))
	writeFile(t, filepath.Join(repoB, ".claude", "commands", "lint.md"),
		"---\ndescription: Run linters\n---\nCommand body")

	repoPaths := map[string]string{"repo-a": repoA, "repo-b": repoB}
	selected := map[string]bool{"repo-a": true, "repo-b": true}
	return repoPaths, selected
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillPickerActivateDeactivate(t *testing.T) {
	sp := NewSkillPickerModel()
	if sp.IsActive() {
		t.Error("expected inactive initially")
	}
	sp.Activate("")
	if !sp.IsActive() {
		t.Error("expected active after Activate")
	}
	sp.Deactivate()
	if sp.IsActive() {
		t.Error("expected inactive after Deactivate")
	}
}

func TestSkillPickerLoadItems(t *testing.T) {
	repoPaths, selected := setupTestRepos(t)
	sp := NewSkillPickerModel()
	sp.LoadItems(repoPaths, selected)

	if !sp.HasItems() {
		t.Error("expected items to be loaded")
	}
	// repo-a: 1 skill + 2 commands + 1 agent = 4
	// repo-b: 1 command = 1
	// total = 5
	if len(sp.allItems) != 5 {
		t.Errorf("expected 5 items, got %d", len(sp.allItems))
	}
}

func TestSkillPickerDiscoverSkills(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".claude", "skills", "my-skill"))
	writeFile(t, filepath.Join(dir, ".claude", "skills", "my-skill", "SKILL.md"),
		"---\nname: my-skill\ndescription: A test skill\n---\nBody")

	items := discoverRepoItems("test-repo", dir)
	var skills []SkillItem
	for _, item := range items {
		if item.Type == "skill" {
			skills = append(skills, item)
		}
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("skill name = %q, want %q", skills[0].Name, "my-skill")
	}
	if skills[0].Description != "A test skill" {
		t.Errorf("skill description = %q, want %q", skills[0].Description, "A test skill")
	}
	if skills[0].RepoName != "test-repo" {
		t.Errorf("skill repo = %q, want %q", skills[0].RepoName, "test-repo")
	}
}

func TestSkillPickerDiscoverCommands(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".claude", "commands"))
	writeFile(t, filepath.Join(dir, ".claude", "commands", "commit.md"),
		"---\ndescription: Create a git commit\n---\nBody")

	items := discoverRepoItems("test-repo", dir)
	var commands []SkillItem
	for _, item := range items {
		if item.Type == "command" {
			commands = append(commands, item)
		}
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(commands))
	}
	if commands[0].Name != "commit" {
		t.Errorf("command name = %q, want %q", commands[0].Name, "commit")
	}
	if commands[0].Description != "Create a git commit" {
		t.Errorf("command description = %q, want %q", commands[0].Description, "Create a git commit")
	}
}

func TestSkillPickerDiscoverCommandsNested(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".claude", "commands", "ci"))
	writeFile(t, filepath.Join(dir, ".claude", "commands", "ci", "deploy.md"),
		"---\ndescription: Deploy to CI\n---\nBody")

	items := discoverRepoItems("test-repo", dir)
	var commands []SkillItem
	for _, item := range items {
		if item.Type == "command" {
			commands = append(commands, item)
		}
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(commands))
	}
	if commands[0].Name != "ci:deploy" {
		t.Errorf("nested command name = %q, want %q", commands[0].Name, "ci:deploy")
	}
}

func TestSkillPickerDiscoverAgents(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".claude", "agents"))
	writeFile(t, filepath.Join(dir, ".claude", "agents", "analyzer.md"),
		"---\nname: analyzer\ndescription: Code analysis\n---\nBody")

	items := discoverRepoItems("test-repo", dir)
	var agents []SkillItem
	for _, item := range items {
		if item.Type == "agent" {
			agents = append(agents, item)
		}
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "analyzer" {
		t.Errorf("agent name = %q, want %q", agents[0].Name, "analyzer")
	}
	if agents[0].Description != "Code analysis" {
		t.Errorf("agent description = %q, want %q", agents[0].Description, "Code analysis")
	}
}

func TestSkillPickerFilterByPrefix(t *testing.T) {
	repoPaths, selected := setupTestRepos(t)
	sp := NewSkillPickerModel()
	sp.LoadItems(repoPaths, selected)
	sp.Activate("")

	// All items should match with empty prefix
	allCount := len(sp.matches)
	if allCount != 5 {
		t.Errorf("expected 5 matches with empty prefix, got %d", allCount)
	}

	// Filter by "co" — should match "commit" only
	sp.SetPrefix("co")
	var names []string
	for _, m := range sp.matches {
		names = append(names, m.Name)
	}
	for _, name := range names {
		if name != "commit" {
			t.Errorf("unexpected match %q for prefix 'co'", name)
		}
	}
}

func TestSkillPickerNavigation(t *testing.T) {
	sp := NewSkillPickerModel()
	sp.allItems = []SkillItem{
		{Name: "aaa", Type: "command"},
		{Name: "bbb", Type: "command"},
		{Name: "ccc", Type: "command"},
	}
	sp.Activate("")

	if sp.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", sp.cursor)
	}

	sp, _, _ = sp.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if sp.cursor != 1 {
		t.Errorf("expected cursor at 1 after down, got %d", sp.cursor)
	}

	sp, _, _ = sp.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if sp.cursor != 2 {
		t.Errorf("expected cursor at 2 after second down, got %d", sp.cursor)
	}

	// Down at end should not move further
	sp, _, _ = sp.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if sp.cursor != 2 {
		t.Errorf("expected cursor to stay at 2, got %d", sp.cursor)
	}

	sp, _, _ = sp.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if sp.cursor != 1 {
		t.Errorf("expected cursor at 1 after up, got %d", sp.cursor)
	}

	// Up at top should not go negative
	sp, _, _ = sp.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	sp, _, _ = sp.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if sp.cursor != 0 {
		t.Errorf("expected cursor to stay at 0, got %d", sp.cursor)
	}
}

func TestSkillPickerCtrlNCtrlPNavigation(t *testing.T) {
	sp := NewSkillPickerModel()
	sp.allItems = []SkillItem{
		{Name: "aaa", Type: "command"},
		{Name: "bbb", Type: "command"},
		{Name: "ccc", Type: "command"},
	}
	sp.Activate("")

	if sp.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", sp.cursor)
	}

	// ctrl+n moves down
	sp, _, _ = sp.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if sp.cursor != 1 {
		t.Errorf("expected cursor at 1 after ctrl+n, got %d", sp.cursor)
	}

	sp, _, _ = sp.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if sp.cursor != 2 {
		t.Errorf("expected cursor at 2 after second ctrl+n, got %d", sp.cursor)
	}

	// ctrl+n at bottom stays at max
	sp, _, _ = sp.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if sp.cursor != 2 {
		t.Errorf("expected cursor to stay at 2, got %d", sp.cursor)
	}

	// ctrl+p moves up
	sp, _, _ = sp.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if sp.cursor != 1 {
		t.Errorf("expected cursor at 1 after ctrl+p, got %d", sp.cursor)
	}

	sp, _, _ = sp.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if sp.cursor != 0 {
		t.Errorf("expected cursor at 0 after second ctrl+p, got %d", sp.cursor)
	}

	// ctrl+p at top stays at 0
	sp, _, _ = sp.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if sp.cursor != 0 {
		t.Errorf("expected cursor to stay at 0, got %d", sp.cursor)
	}
}

func TestSkillPickerSelectionEnter(t *testing.T) {
	sp := NewSkillPickerModel()
	sp.allItems = []SkillItem{{Name: "commit", Type: "command"}}
	sp.Activate("")

	sp, selected, consumed := sp.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !consumed {
		t.Error("expected enter to be consumed")
	}
	if selected != "commit" {
		t.Errorf("expected selected = %q, got %q", "commit", selected)
	}
	if sp.IsActive() {
		t.Error("expected picker to deactivate after enter")
	}
}

func TestSkillPickerSelectionTab(t *testing.T) {
	sp := NewSkillPickerModel()
	sp.allItems = []SkillItem{{Name: "deploy", Type: "command"}}
	sp.Activate("")

	sp, selected, consumed := sp.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !consumed {
		t.Error("expected tab to be consumed")
	}
	if selected != "deploy" {
		t.Errorf("expected selected = %q, got %q", "deploy", selected)
	}
	if sp.IsActive() {
		t.Error("expected picker to deactivate after tab")
	}
}

func TestSkillPickerEscCancels(t *testing.T) {
	sp := NewSkillPickerModel()
	sp.allItems = []SkillItem{{Name: "commit", Type: "command"}}
	sp.Activate("")

	sp, selected, consumed := sp.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !consumed {
		t.Error("expected esc to be consumed")
	}
	if selected != "" {
		t.Error("expected empty selection on esc")
	}
	if sp.IsActive() {
		t.Error("expected picker to deactivate after esc")
	}
}

func TestSkillPickerRegularCharsNotConsumed(t *testing.T) {
	sp := NewSkillPickerModel()
	sp.allItems = []SkillItem{{Name: "commit", Type: "command"}}
	sp.Activate("")

	_, _, consumed := sp.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if consumed {
		t.Error("regular characters should not be consumed by the picker")
	}
}

func TestSkillPickerViewRendering(t *testing.T) {
	sp := NewSkillPickerModel()
	sp.allItems = []SkillItem{
		{Name: "commit", Type: "command", Description: "Create a git commit"},
	}
	sp.Activate("")

	view := sp.View()
	if view == "" {
		t.Error("expected non-empty view when active with matches")
	}
	if !strings.Contains(view, "Skill completions") {
		t.Error("expected header in view")
	}
	if !strings.Contains(view, "/commit") {
		t.Error("expected /commit in view")
	}
}

func TestSkillPickerViewInactive(t *testing.T) {
	sp := NewSkillPickerModel()
	view := sp.View()
	if view != "" {
		t.Error("expected empty view when inactive")
	}
}

func TestSkillPickerMultiRepo(t *testing.T) {
	repoPaths, selected := setupTestRepos(t)
	sp := NewSkillPickerModel()
	sp.LoadItems(repoPaths, selected)

	if !sp.multiRepo {
		t.Error("expected multiRepo to be true")
	}

	sp.Activate("")
	view := sp.View()
	// Multi-repo display should include repo names
	if !strings.Contains(view, "repo-a") {
		t.Error("expected repo-a in multi-repo view")
	}
	if !strings.Contains(view, "repo-b") {
		t.Error("expected repo-b in multi-repo view")
	}
}

func TestSkillPickerSingleRepo(t *testing.T) {
	repoPaths, _ := setupTestRepos(t)
	// Select only one repo
	selected := map[string]bool{"repo-a": true}
	sp := NewSkillPickerModel()
	sp.LoadItems(repoPaths, selected)

	if sp.multiRepo {
		t.Error("expected multiRepo to be false for single repo")
	}

	sp.Activate("")
	view := sp.View()
	// Single-repo display should NOT include repo name
	if strings.Contains(view, "repo-a ·") {
		t.Error("expected no repo name prefix in single-repo view")
	}
}

func TestSkillPickerEmptyClaudeDir(t *testing.T) {
	dir := t.TempDir()
	repoPaths := map[string]string{"empty-repo": dir}
	selected := map[string]bool{"empty-repo": true}

	sp := NewSkillPickerModel()
	sp.LoadItems(repoPaths, selected)

	if sp.HasItems() {
		t.Error("expected no items from repo without .claude/ dir")
	}
}

func TestSkillPickerNoSelectedRepos(t *testing.T) {
	repoPaths, _ := setupTestRepos(t)
	// Select no repos
	selected := map[string]bool{}

	sp := NewSkillPickerModel()
	sp.LoadItems(repoPaths, selected)

	if sp.HasItems() {
		t.Error("expected no items when no repos selected")
	}
}

func TestSkillPickerSkillWithoutFrontmatter(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".claude", "skills", "no-frontmatter"))
	writeFile(t, filepath.Join(dir, ".claude", "skills", "no-frontmatter", "SKILL.md"),
		"Just a skill body with no frontmatter")

	items := discoverRepoItems("test-repo", dir)
	var skills []SkillItem
	for _, item := range items {
		if item.Type == "skill" {
			skills = append(skills, item)
		}
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	// Should fallback to directory name
	if skills[0].Name != "no-frontmatter" {
		t.Errorf("skill name = %q, want %q (fallback to dir name)", skills[0].Name, "no-frontmatter")
	}
}

func TestSkillPickerAgentWithoutName(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".claude", "agents"))
	writeFile(t, filepath.Join(dir, ".claude", "agents", "codebase-analyzer.md"),
		"---\ndescription: Analyze code\n---\nBody")

	items := discoverRepoItems("test-repo", dir)
	var agents []SkillItem
	for _, item := range items {
		if item.Type == "agent" {
			agents = append(agents, item)
		}
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	// Should fallback to filename without .md
	if agents[0].Name != "codebase-analyzer" {
		t.Errorf("agent name = %q, want %q (fallback to filename)", agents[0].Name, "codebase-analyzer")
	}
}

func TestFindSkillTriggerSlash(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"/", 0},
		{"/commit", 0},
		{"go test /", 8},
		{" /skill", 1},
		{"test /sk", 5},
		{"./...", -1},
		{"path/to/file", -1},
		{"", -1},
		{"a/b", -1},
		{"hello", -1},
		{"  /", 2},
		{"text /a/b", 5}, // first slash is preceded by space, so it's a valid trigger
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := findSkillTriggerSlash(tt.input)
			if got != tt.want {
				t.Errorf("findSkillTriggerSlash(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSkillFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantKey string
		wantVal string
		wantNil bool
	}{
		{
			name:    "valid frontmatter",
			content: "---\nname: my-skill\ndescription: A test skill\n---\nBody",
			wantKey: "name",
			wantVal: "my-skill",
		},
		{
			name:    "no frontmatter",
			content: "Just a body",
			wantNil: true,
		},
		{
			name:    "unclosed frontmatter",
			content: "---\nname: my-skill\nno closing",
			wantNil: true,
		},
		{
			name:    "empty frontmatter",
			content: "---\n---\nBody",
			wantKey: "name",
			wantVal: "",
		},
		{
			name:    "multiline description takes first line",
			content: "---\ndescription: First line\n---\nBody",
			wantKey: "description",
			wantVal: "First line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm := parseSkillFrontmatter(tt.content)
			if tt.wantNil {
				if fm != nil {
					t.Errorf("expected nil, got %v", fm)
				}
				return
			}
			if fm == nil {
				t.Fatal("expected non-nil frontmatter")
			}
			if tt.wantVal != "" && fm[tt.wantKey] != tt.wantVal {
				t.Errorf("fm[%q] = %q, want %q", tt.wantKey, fm[tt.wantKey], tt.wantVal)
			}
		})
	}
}

func TestSkillPickerSelectedNameFallback(t *testing.T) {
	sp := NewSkillPickerModel()
	sp.Activate("typed")
	// No items loaded, so matches is empty — should fallback to prefix
	if sp.SelectedName() != "typed" {
		t.Errorf("SelectedName() = %q, want %q (fallback to prefix)", sp.SelectedName(), "typed")
	}
}

func TestSkillPickerSortOrder(t *testing.T) {
	repoPaths, selected := setupTestRepos(t)
	sp := NewSkillPickerModel()
	sp.LoadItems(repoPaths, selected)

	// Items should be sorted by type first, then by name
	// Expected types order: agent < command < skill
	prevType := ""
	prevName := ""
	for _, item := range sp.allItems {
		if item.Type < prevType {
			t.Errorf("items not sorted by type: %q after %q", item.Type, prevType)
		}
		if item.Type == prevType && item.Name < prevName {
			t.Errorf("items not sorted by name within type: %q after %q", item.Name, prevName)
		}
		prevType = item.Type
		prevName = item.Name
	}
}
