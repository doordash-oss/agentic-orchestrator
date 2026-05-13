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

func TestNewDirPickerModel(t *testing.T) {
	home := setDirPickerTestHome(t)
	m := NewDirPickerModel()
	if m.IsDone() {
		t.Error("expected not done")
	}
	if m.IsCancelled() {
		t.Error("expected not cancelled")
	}
	if m.SelectedPath() != "" {
		t.Error("expected empty selected path")
	}
	// Should start at $HOME
	if m.currentDir() != home {
		t.Errorf("expected start dir %q, got %q", home, m.currentDir())
	}
	// Should have at least one column
	if len(m.columns) == 0 {
		t.Error("expected at least one column")
	}
}

func TestDirPickerStartsFromHome(t *testing.T) {
	home := setDirPickerTestHome(t)
	m := NewDirPickerModel()
	if m.currentDir() != home {
		t.Errorf("expected to start from home %q, got %q", home, m.currentDir())
	}
}

func TestDirPickerGitRepoScan(t *testing.T) {
	dir := t.TempDir()
	// Create 3 subdirs, 2 with .git
	if err := os.MkdirAll(filepath.Join(dir, "repo-a", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "repo-b", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "not-repo"), 0o755); err != nil {
		t.Fatal(err)
	}

	result := scanGitRepos(dir)
	if result.count != 2 {
		t.Errorf("expected 2 repos, got %d", result.count)
	}
	if !result.repoDirs["repo-a"] {
		t.Error("expected repo-a in repoDirs")
	}
	if !result.repoDirs["repo-b"] {
		t.Error("expected repo-b in repoDirs")
	}
	if result.repoDirs["not-repo"] {
		t.Error("expected not-repo NOT in repoDirs")
	}
}

func TestDirPickerGitRepoScanWorktree(t *testing.T) {
	dir := t.TempDir()
	// Regular repo with .git directory
	if err := os.MkdirAll(filepath.Join(dir, "regular-repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Worktree repo with .git file
	wtDir := filepath.Join(dir, "worktree-repo")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, ".git"), []byte("gitdir: /some/other/.git/worktrees/worktree-repo"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := scanGitRepos(dir)
	if result.count != 2 {
		t.Errorf("expected 2 repos, got %d", result.count)
	}
	if !result.repoDirs["regular-repo"] {
		t.Error("expected regular-repo in repoDirs")
	}
	if !result.repoDirs["worktree-repo"] {
		t.Error("expected worktree-repo in repoDirs")
	}
}

// setupPickerAt creates a DirPickerModel rooted at the given directory with a wide terminal.
func setupPickerAt(dir string) DirPickerModel {
	col := makeColumn(dir, false)
	m := DirPickerModel{
		columns:     []column{col},
		gitRepoDirs: make(map[string]bool),
	}
	m.rebuildPreview()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	return m
}

func setDirPickerTestHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("create test HOME: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestDirPickerGitRepoMarkers(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "repo-a", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "repo-b", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "not-repo"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := setupPickerAt(dir)

	// Simulate git scan completion
	m, _ = m.Update(gitRepoScanMsg{
		dir:           dir,
		count:         2,
		repoDirs:      map[string]bool{"repo-a": true, "repo-b": true},
		dirRepoCounts: map[string]int{},
	})

	if m.gitRepoCount != 2 {
		t.Errorf("expected gitRepoCount 2, got %d", m.gitRepoCount)
	}
	// Verify ★ annotations appear in the view
	view := m.View()
	if !strings.Contains(view, "★") {
		t.Errorf("expected ★ repo markers in view, got:\n%s", view)
	}
}

func TestDirPickerHiddenToggle(t *testing.T) {
	setDirPickerTestHome(t)
	m := NewDirPickerModel()
	if m.showHidden {
		t.Error("expected showHidden = false initially")
	}

	// Toggle hidden on
	m, _ = m.Update(tea.KeyPressMsg{Text: "."})
	if !m.showHidden {
		t.Error("expected showHidden = true after '.' press")
	}

	// Toggle hidden off
	m, _ = m.Update(tea.KeyPressMsg{Text: "."})
	if m.showHidden {
		t.Error("expected showHidden = false after second '.' press")
	}
}

func TestDirPickerSelectHighlighted(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := setupPickerAt(dir)
	// Mark scan done — "repo" is a git repo (shown as ★)
	m.columns[0].scanDone = true
	m.columns[0].repoDirs = map[string]bool{"repo": true}
	m.gitRepoCount = 1
	m.gitRepoDirs = map[string]bool{"repo": true}

	// The highlighted entry is "repo" — Space selects it
	expected := filepath.Join(dir, "repo")
	m, _ = m.Update(tea.KeyPressMsg{Code: ' '})
	if !m.IsDone() {
		t.Error("expected done after Space")
	}
	if m.IsCancelled() {
		t.Error("expected not cancelled")
	}
	if m.SelectedPath() != expected {
		t.Errorf("expected path %q, got %q", expected, m.SelectedPath())
	}
}

func TestDirPickerSelectViaEnter(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := setupPickerAt(dir)
	m.columns[0].scanDone = true
	m.columns[0].repoDirs = map[string]bool{"repo": true}
	m.gitRepoCount = 1

	// Enter selects the highlighted "repo" directory
	expected := filepath.Join(dir, "repo")
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.IsDone() {
		t.Error("expected done after Enter")
	}
	if m.SelectedPath() != expected {
		t.Errorf("expected path %q, got %q", expected, m.SelectedPath())
	}
}

func TestDirPickerEscCancels(t *testing.T) {
	setDirPickerTestHome(t)
	m := NewDirPickerModel()
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.IsDone() {
		t.Error("expected done after Esc")
	}
	if !m.IsCancelled() {
		t.Error("expected cancelled after Esc")
	}
}

func TestDirPickerEmptyRootConfirmation(t *testing.T) {
	dir := t.TempDir()
	// Create a non-repo subdir so there's something to highlight
	os.MkdirAll(filepath.Join(dir, "empty-dir"), 0o755)

	m := setupPickerAt(dir)
	m.columns[0].scanDone = true
	m.gitRepoCount = 0

	expected := filepath.Join(dir, "empty-dir")

	t.Run("confirm with enter", func(t *testing.T) {
		m2 := m
		// Press Space → enters confirmation (highlighted entry has no repos)
		m2, _ = m2.Update(tea.KeyPressMsg{Code: ' '})
		if m2.IsDone() {
			t.Error("expected not done yet (confirmation pending)")
		}
		if !m2.confirmEmpty {
			t.Error("expected confirmEmpty = true")
		}

		// Confirm with Enter
		m2, _ = m2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if !m2.IsDone() {
			t.Error("expected done after confirmation")
		}
		if m2.SelectedPath() != expected {
			t.Errorf("expected path %q, got %q", expected, m2.SelectedPath())
		}
	})

	t.Run("confirm with y", func(t *testing.T) {
		m2 := m
		m2, _ = m2.Update(tea.KeyPressMsg{Code: ' '})
		m2, _ = m2.Update(tea.KeyPressMsg{Text: "y"})
		if !m2.IsDone() {
			t.Error("expected done after 'y' confirmation")
		}
	})

	t.Run("dismiss with esc", func(t *testing.T) {
		m2 := m
		m2, _ = m2.Update(tea.KeyPressMsg{Code: ' '})
		if !m2.confirmEmpty {
			t.Fatal("precondition: expected confirmEmpty = true")
		}

		// Dismiss with Esc
		m2, _ = m2.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if m2.IsDone() {
			t.Error("expected not done after dismissing confirmation")
		}
		if m2.confirmEmpty {
			t.Error("expected confirmEmpty = false after dismissal")
		}
	})

	t.Run("dismiss with n", func(t *testing.T) {
		m2 := m
		m2, _ = m2.Update(tea.KeyPressMsg{Code: ' '})
		m2, _ = m2.Update(tea.KeyPressMsg{Text: "n"})
		if m2.IsDone() {
			t.Error("expected not done after 'n' dismissal")
		}
		if m2.confirmEmpty {
			t.Error("expected confirmEmpty = false after 'n'")
		}
	})
}

func TestDirPickerBreadcrumb(t *testing.T) {
	dir := t.TempDir()
	m := setupPickerAt(dir)
	view := m.View()
	if !strings.Contains(view, dir) {
		t.Error("expected breadcrumb to contain current directory path")
	}
}

func TestDirPickerBreadcrumbHome(t *testing.T) {
	setDirPickerTestHome(t)
	// Default constructor starts at $HOME, so breadcrumb should show ~
	m := NewDirPickerModel()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	view := m.View()
	if !strings.Contains(view, "~") {
		t.Errorf("expected breadcrumb with ~ substitution, got:\n%s", view)
	}
}

func TestDirPickerFooterKeyHints(t *testing.T) {
	setDirPickerTestHome(t)
	m := NewDirPickerModel()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	view := m.View()
	if !strings.Contains(view, "select highlighted") {
		t.Error("expected 'select highlighted' in footer")
	}
	if !strings.Contains(view, "esc cancel") {
		t.Error("expected 'esc cancel' in footer")
	}
}

func TestDirPickerGitRepoCount(t *testing.T) {
	setDirPickerTestHome(t)
	t.Run("with repos", func(t *testing.T) {
		m := NewDirPickerModel()
		m, _ = m.Update(gitRepoScanMsg{
			dir:           m.currentDir(),
			count:         3,
			repoDirs:      map[string]bool{"a": true, "b": true, "c": true},
			dirRepoCounts: map[string]int{},
		})
		if m.gitRepoCount != 3 {
			t.Errorf("expected gitRepoCount 3, got %d", m.gitRepoCount)
		}
	})

	t.Run("no repos", func(t *testing.T) {
		m := NewDirPickerModel()
		m, _ = m.Update(gitRepoScanMsg{
			dir:           m.currentDir(),
			count:         0,
			repoDirs:      map[string]bool{},
			dirRepoCounts: map[string]int{},
		})
		if m.gitRepoCount != 0 {
			t.Errorf("expected gitRepoCount 0, got %d", m.gitRepoCount)
		}
	})
}

func TestDirPickerWindowResize(t *testing.T) {
	setDirPickerTestHome(t)
	m := NewDirPickerModel()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 {
		t.Errorf("expected width 120, got %d", m.width)
	}
	if m.height != 40 {
		t.Errorf("expected height 40, got %d", m.height)
	}
}

func TestDirPickerPublicAPIPreserved(t *testing.T) {
	setDirPickerTestHome(t)
	// Verifies the public API contract used by WelcomeModel (compile-time checks).
	m := NewDirPickerModel()
	_ = m.Init()

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	_ = m.View()
	_ = m.SelectedPath()
	_ = m.IsDone()
	_ = m.IsCancelled()
}

func TestDirPickerDoneShortCircuits(t *testing.T) {
	setDirPickerTestHome(t)
	m := NewDirPickerModel()
	m.done = true
	m.selected = "/some/path"

	// Further updates should be no-ops
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.IsCancelled() {
		t.Error("expected done model to ignore further key events")
	}
	if m.SelectedPath() != "/some/path" {
		t.Error("expected selected path to remain unchanged")
	}
}

func TestDirPickerConfirmationView(t *testing.T) {
	setDirPickerTestHome(t)
	m := NewDirPickerModel()
	m.confirmEmpty = true
	m.confirmSelection = "/tmp/empty-dir"

	view := m.View()
	if !strings.Contains(view, "No git repos found") {
		t.Error("expected confirmation prompt text")
	}
	if !strings.Contains(view, "y/enter confirm") {
		t.Error("expected confirmation hints")
	}
}

func TestCompactHome(t *testing.T) {
	setDirPickerTestHome(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	tests := []struct {
		input string
		want  string
	}{
		{home, "~"},
		{filepath.Join(home, "Projects"), "~/Projects"},
		{"/usr/local/bin", "/usr/local/bin"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := compactHome(tt.input)
			if got != tt.want {
				t.Errorf("compactHome(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDirPickerScanPendingBlocksSelection(t *testing.T) {
	dir := t.TempDir()
	m := setupPickerAt(dir)
	// Scan not done — selection should be blocked

	// Space should be blocked while scan is pending
	m, _ = m.Update(tea.KeyPressMsg{Code: ' '})
	if m.IsDone() {
		t.Error("expected selection to be blocked while scan is pending")
	}

	// Enter should also be blocked
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.IsDone() {
		t.Error("expected selection to be blocked while scan is pending")
	}

	// Esc should still work (cancel is always allowed)
	m2 := setupPickerAt(dir)
	m2, _ = m2.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m2.IsDone() || !m2.IsCancelled() {
		t.Error("expected Esc to still cancel even while scanning")
	}
}

func TestDirPickerScanCompleteClearsState(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "b", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := setupPickerAt(dir)

	// Simulate scan completing
	m, _ = m.Update(gitRepoScanMsg{
		dir:           dir,
		count:         2,
		repoDirs:      map[string]bool{"a": true, "b": true},
		dirRepoCounts: map[string]int{},
	})

	if m.gitRepoCount != 2 {
		t.Errorf("expected gitRepoCount = 2, got %d", m.gitRepoCount)
	}
}

func TestDirPickerSelectionAfterScanComplete(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := setupPickerAt(dir)

	// Selection blocked while scan not done
	m, _ = m.Update(tea.KeyPressMsg{Code: ' '})
	if m.IsDone() {
		t.Error("expected selection blocked while scan pending")
	}

	// Scan completes
	m, _ = m.Update(gitRepoScanMsg{
		dir:           dir,
		count:         1,
		repoDirs:      map[string]bool{"repo": true},
		dirRepoCounts: map[string]int{},
	})

	// Now selection should work — selects highlighted entry "repo"
	expected := filepath.Join(dir, "repo")
	m, _ = m.Update(tea.KeyPressMsg{Code: ' '})
	if !m.IsDone() {
		t.Error("expected selection to work after scan completes")
	}
	if m.SelectedPath() != expected {
		t.Errorf("expected path %q, got %q", expected, m.SelectedPath())
	}
}

func TestDirPickerNavigation(t *testing.T) {
	// Create a directory tree: root/child/grandchild
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	grandchild := filepath.Join(child, "grandchild")
	if err := os.MkdirAll(grandchild, 0o755); err != nil {
		t.Fatal(err)
	}

	m := setupPickerAt(dir)

	// Should have "child" in entries
	if len(m.columns[0].entries) == 0 {
		t.Fatal("expected entries in root column")
	}
	if m.columns[0].entries[0] != "child" {
		t.Errorf("expected 'child' entry, got %q", m.columns[0].entries[0])
	}

	// Navigate right into child
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.currentDir() != child {
		t.Errorf("expected current dir %q after right, got %q", child, m.currentDir())
	}
	if len(m.columns) != 2 {
		t.Errorf("expected 2 columns after right, got %d", len(m.columns))
	}

	// Navigate back
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.currentDir() != dir {
		t.Errorf("expected current dir %q after left, got %q", dir, m.currentDir())
	}
	if len(m.columns) != 1 {
		t.Errorf("expected 1 column after left, got %d", len(m.columns))
	}
}

func TestReadDirEntries(t *testing.T) {
	dir := t.TempDir()
	// Create dirs and a file
	os.MkdirAll(filepath.Join(dir, "bravo"), 0o755)
	os.MkdirAll(filepath.Join(dir, "alpha"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644)

	t.Run("no hidden", func(t *testing.T) {
		entries := readDirEntries(dir, false)
		if len(entries) != 2 {
			t.Errorf("expected 2 entries, got %d: %v", len(entries), entries)
		}
		// Should be sorted
		if entries[0] != "alpha" || entries[1] != "bravo" {
			t.Errorf("expected [alpha, bravo], got %v", entries)
		}
	})

	t.Run("with hidden", func(t *testing.T) {
		entries := readDirEntries(dir, true)
		if len(entries) != 3 {
			t.Errorf("expected 3 entries, got %d: %v", len(entries), entries)
		}
	})
}

func TestDirPickerCreateRepoModeAcceptsAnyDir(t *testing.T) {
	// Create a directory with NO git repos
	dir := t.TempDir()

	// Create a subdirectory to navigate into
	subDir := filepath.Join(dir, "projects")
	os.MkdirAll(subDir, 0o755)

	// Set up picker in create-repo mode using the same struct literal pattern as setupPickerAt
	col := makeColumn(dir, false)
	m := DirPickerModel{
		columns:     []column{col},
		gitRepoDirs: make(map[string]bool),
		mode:        dirPickerModeCreateRepo,
	}
	m.rebuildPreview()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})

	// Mark scan done (no git repos)
	m.columns[0].scanDone = true

	// Select highlighted (should succeed in create-repo mode even without git repos)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.IsDone() {
		t.Error("expected picker to be done — create-repo mode should accept any directory")
	}
	if m.IsCancelled() {
		t.Error("picker should not be cancelled")
	}
	if m.SelectedPath() == "" {
		t.Error("expected a selected path")
	}

	// Verify mode getter
	if m.Mode() != dirPickerModeCreateRepo {
		t.Error("expected mode to be dirPickerModeCreateRepo")
	}
}

func TestDirPickerBrowseModeRejectsEmptyDir(t *testing.T) {
	// Create a directory with NO git repos
	dir := t.TempDir()

	// Set up picker in default browse mode
	m := setupPickerAt(dir)
	m.columns[0].scanDone = true

	// Enter should NOT complete — should show confirmation
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.IsDone() {
		t.Error("browse mode should NOT accept directory with no git repos on first enter")
	}
	if m.Mode() != dirPickerModeBrowse {
		t.Error("expected mode to be dirPickerModeBrowse")
	}
}
