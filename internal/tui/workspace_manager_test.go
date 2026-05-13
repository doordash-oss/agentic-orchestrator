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

func makeRoots(paths ...string) []workspaceRoot {
	roots := make([]workspaceRoot, len(paths))
	for i, p := range paths {
		roots[i] = workspaceRoot{Path: p, RepoCount: i + 1}
	}
	return roots
}

func setWorkspacePickerHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create picker HOME entry: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestNewWorkspaceManagerModel(t *testing.T) {
	roots := makeRoots("/tmp/workspace-a", "/tmp/workspace-b")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	if m.IsClosed() {
		t.Error("expected IsClosed() == false for new model")
	}
	if m.IsPickerActive() {
		t.Error("expected IsPickerActive() == false for new model")
	}
	if got := m.ConsumeAddedRoot(); got != "" {
		t.Errorf("expected ConsumeAddedRoot() == \"\", got %q", got)
	}
	if got := m.ConsumeRemovedRoot(); got != "" {
		t.Errorf("expected ConsumeRemovedRoot() == \"\", got %q", got)
	}

	view := m.View()
	if !strings.Contains(view, "/tmp/workspace-a") {
		t.Errorf("expected view to contain /tmp/workspace-a, got:\n%s", view)
	}
	if !strings.Contains(view, "/tmp/workspace-b") {
		t.Errorf("expected view to contain /tmp/workspace-b, got:\n%s", view)
	}
}

func TestWorkspaceManagerEmptyState(t *testing.T) {
	m := NewWorkspaceManagerModel(nil, 80, 40)
	view := m.View()

	if !strings.Contains(view, "No workspace roots") {
		t.Errorf("expected 'No workspace roots' in empty view, got:\n%s", view)
	}
	if !strings.Contains(view, "'a'") {
		t.Errorf("expected 'a' hint in empty view, got:\n%s", view)
	}
}

func TestWorkspaceManagerNavigation(t *testing.T) {
	roots := makeRoots("/tmp/r0", "/tmp/r1", "/tmp/r2")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	// Cursor starts at 0
	if m.cursor != 0 {
		t.Fatalf("expected initial cursor at 0, got %d", m.cursor)
	}

	t.Run("j moves down", func(t *testing.T) {
		m2 := m
		m2, _ = m2.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
		if m2.cursor != 1 {
			t.Errorf("expected cursor 1 after j, got %d", m2.cursor)
		}
	})

	t.Run("down arrow moves down", func(t *testing.T) {
		m2 := m
		m2, _ = m2.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		if m2.cursor != 1 {
			t.Errorf("expected cursor 1 after down arrow, got %d", m2.cursor)
		}
	})

	t.Run("k moves up", func(t *testing.T) {
		m2 := m
		// Move down first
		m2, _ = m2.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
		m2, _ = m2.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
		if m2.cursor != 0 {
			t.Errorf("expected cursor 0 after k, got %d", m2.cursor)
		}
	})

	t.Run("up arrow moves up", func(t *testing.T) {
		m2 := m
		m2, _ = m2.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m2, _ = m2.Update(tea.KeyPressMsg{Code: tea.KeyUp})
		if m2.cursor != 0 {
			t.Errorf("expected cursor 0 after up arrow, got %d", m2.cursor)
		}
	})

	t.Run("clamps at bottom", func(t *testing.T) {
		m2 := m
		// Move down 5 times, should clamp at 2
		for i := 0; i < 5; i++ {
			m2, _ = m2.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
		}
		if m2.cursor != 2 {
			t.Errorf("expected cursor clamped at 2, got %d", m2.cursor)
		}
	})

	t.Run("clamps at top", func(t *testing.T) {
		m2 := m
		// Already at 0, press up
		m2, _ = m2.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
		if m2.cursor != 0 {
			t.Errorf("expected cursor clamped at 0, got %d", m2.cursor)
		}
	})
}

func TestWorkspaceManagerEscCloses(t *testing.T) {
	roots := makeRoots("/tmp/r0")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.IsClosed() {
		t.Error("expected IsClosed() == true after Esc")
	}
}

func TestWorkspaceManagerAddRootOpensPicker(t *testing.T) {
	roots := makeRoots("/tmp/r0")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	m, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if !m.IsPickerActive() {
		t.Error("expected IsPickerActive() == true after 'a'")
	}
}

func TestWorkspaceManagerAddRootPickerCancel(t *testing.T) {
	roots := makeRoots("/tmp/r0")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	// Open the picker
	m, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if !m.IsPickerActive() {
		t.Fatal("precondition: expected picker active")
	}

	// Send Esc through the workspace manager — forwarded to the picker,
	// which sets cancelled + done, triggering IsCancelled() in the WS manager.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.IsPickerActive() {
		t.Error("expected IsPickerActive() == false after picker cancel")
	}
	if got := m.ConsumeAddedRoot(); got != "" {
		t.Errorf("expected ConsumeAddedRoot() == \"\", got %q", got)
	}
}

func TestWorkspaceManagerRemoveRoot(t *testing.T) {
	roots := makeRoots("/tmp/r0", "/tmp/r1", "/tmp/r2")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	// Move cursor to index 1
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.cursor != 1 {
		t.Fatalf("expected cursor at 1, got %d", m.cursor)
	}

	// Press 'd' to initiate delete
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	view := m.View()
	if !strings.Contains(view, "Remove") {
		t.Errorf("expected confirmation prompt in view, got:\n%s", view)
	}

	// Confirm with 'y'
	m, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	removed := m.ConsumeRemovedRoot()
	if removed != "/tmp/r1" {
		t.Errorf("expected removed root /tmp/r1, got %q", removed)
	}
	if len(m.roots) != 2 {
		t.Errorf("expected 2 roots remaining, got %d", len(m.roots))
	}
	// Verify the remaining roots are r0 and r2
	if m.roots[0].Path != "/tmp/r0" || m.roots[1].Path != "/tmp/r2" {
		t.Errorf("unexpected remaining roots: %v", m.roots)
	}
}

func TestWorkspaceManagerRemoveRootCancel(t *testing.T) {
	roots := makeRoots("/tmp/r0", "/tmp/r1")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	// Press 'd' to initiate delete, then 'n' to cancel
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})

	if got := m.ConsumeRemovedRoot(); got != "" {
		t.Errorf("expected ConsumeRemovedRoot() == \"\", got %q", got)
	}
	if len(m.roots) != 2 {
		t.Errorf("expected roots unchanged (2), got %d", len(m.roots))
	}
}

func TestWorkspaceManagerRemoveLastRoot(t *testing.T) {
	roots := makeRoots("/tmp/only")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})

	removed := m.ConsumeRemovedRoot()
	if removed != "/tmp/only" {
		t.Errorf("expected removed /tmp/only, got %q", removed)
	}
	if len(m.roots) != 0 {
		t.Errorf("expected 0 roots, got %d", len(m.roots))
	}

	view := m.View()
	if !strings.Contains(view, "No workspace roots") {
		t.Errorf("expected empty state in view after removing last root, got:\n%s", view)
	}
}

func TestWorkspaceManagerCursorAdjustAfterRemove(t *testing.T) {
	roots := makeRoots("/tmp/r0", "/tmp/r1", "/tmp/r2")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	// Move cursor to index 2 (last)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.cursor != 2 {
		t.Fatalf("expected cursor at 2, got %d", m.cursor)
	}

	// Remove last item
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})

	if m.cursor != 1 {
		t.Errorf("expected cursor adjusted to 1 after removing last item, got %d", m.cursor)
	}
	if len(m.roots) != 2 {
		t.Errorf("expected 2 roots remaining, got %d", len(m.roots))
	}
}

func TestWorkspaceManagerViewRendering(t *testing.T) {
	roots := []workspaceRoot{
		{Path: "/tmp/workspace-alpha", RepoCount: 3},
		{Path: "/tmp/workspace-beta", RepoCount: 7},
	}
	m := NewWorkspaceManagerModel(roots, 80, 40)

	view := m.View()

	// Title
	if !strings.Contains(view, "Workspace Manager") {
		t.Error("expected 'Workspace Manager' title in view")
	}

	// Root paths
	if !strings.Contains(view, "/tmp/workspace-alpha") {
		t.Error("expected workspace-alpha in view")
	}
	if !strings.Contains(view, "/tmp/workspace-beta") {
		t.Error("expected workspace-beta in view")
	}

	// Repo counts
	if !strings.Contains(view, "3 repos") {
		t.Error("expected '3 repos' count in view")
	}
	if !strings.Contains(view, "7 repos") {
		t.Error("expected '7 repos' count in view")
	}

	// Cursor indicator on first item (cursor at 0)
	if !strings.Contains(view, ">") {
		t.Error("expected cursor indicator '>' in view")
	}

	// Key hints
	if !strings.Contains(view, "esc close") {
		t.Error("expected 'esc close' in key hints")
	}
	if !strings.Contains(view, "a add") {
		t.Error("expected 'a add' in key hints")
	}
	if !strings.Contains(view, "d remove") {
		t.Error("expected 'd remove' in key hints")
	}
}

func TestWorkspaceManagerPickerView(t *testing.T) {
	roots := makeRoots("/tmp/r0")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	// Before opening picker, PickerView should be empty
	if got := m.PickerView(); got != "" {
		t.Errorf("expected empty PickerView before opening picker, got %q", got)
	}

	// Open picker
	m, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if !m.IsPickerActive() {
		t.Fatal("precondition: expected picker active")
	}

	pickerView := m.PickerView()
	if pickerView == "" {
		t.Error("expected non-empty PickerView() when picker is active")
	}
}

func TestWorkspaceManagerEscDuringConfirmDismisses(t *testing.T) {
	roots := makeRoots("/tmp/r0", "/tmp/r1")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	// Press 'd' to enter confirm state
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if !m.confirmDelete {
		t.Fatal("precondition: expected confirmDelete == true")
	}

	// Press Esc — should dismiss confirm but NOT close the overlay
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.confirmDelete {
		t.Error("expected confirmDelete == false after Esc during confirm")
	}
	if m.IsClosed() {
		t.Error("expected overlay still open after Esc during confirm")
	}
	// Root list should be unchanged
	if len(m.roots) != 2 {
		t.Errorf("expected roots unchanged (2), got %d", len(m.roots))
	}
}

func TestWorkspaceManagerConsumeIsIdempotent(t *testing.T) {
	roots := makeRoots("/tmp/r0")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	// Remove the root
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})

	// First consume returns the path
	first := m.ConsumeRemovedRoot()
	if first != "/tmp/r0" {
		t.Errorf("expected first ConsumeRemovedRoot() == /tmp/r0, got %q", first)
	}

	// Second consume returns empty
	second := m.ConsumeRemovedRoot()
	if second != "" {
		t.Errorf("expected second ConsumeRemovedRoot() == \"\", got %q", second)
	}
}

func TestWorkspaceManagerSetRoots(t *testing.T) {
	roots := makeRoots("/tmp/old-a", "/tmp/old-b")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	newRoots := []workspaceRoot{
		{Path: "/tmp/new-x", RepoCount: 5},
		{Path: "/tmp/new-y", RepoCount: 10},
		{Path: "/tmp/new-z", RepoCount: 2},
	}
	m.SetRoots(newRoots)

	view := m.View()
	if strings.Contains(view, "/tmp/old-a") {
		t.Error("expected old root /tmp/old-a to be gone")
	}
	if !strings.Contains(view, "/tmp/new-x") {
		t.Error("expected new root /tmp/new-x in view")
	}
	if !strings.Contains(view, "/tmp/new-y") {
		t.Error("expected new root /tmp/new-y in view")
	}
	if !strings.Contains(view, "/tmp/new-z") {
		t.Error("expected new root /tmp/new-z in view")
	}
	if len(m.roots) != 3 {
		t.Errorf("expected 3 roots, got %d", len(m.roots))
	}
}

func TestWorkspaceManagerSetRootsClampsCursor(t *testing.T) {
	roots := makeRoots("/tmp/a", "/tmp/b", "/tmp/c")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	// Move cursor to 2
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.cursor != 2 {
		t.Fatalf("precondition: expected cursor at 2, got %d", m.cursor)
	}

	// Set fewer roots — cursor should clamp
	m.SetRoots([]workspaceRoot{{Path: "/tmp/only", RepoCount: 1}})
	if m.cursor != 0 {
		t.Errorf("expected cursor clamped to 0, got %d", m.cursor)
	}

	// Set empty roots — cursor should be 0
	m.SetRoots(nil)
	if m.cursor != 0 {
		t.Errorf("expected cursor 0 for empty roots, got %d", m.cursor)
	}
}

func TestCountGitReposInDir(t *testing.T) {
	dir := t.TempDir()

	// Create directories: 2 with .git, 1 without, 1 file (not a dir)
	if err := os.MkdirAll(filepath.Join(dir, "repo-a", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "repo-b", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "not-a-repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a-file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	count := countGitReposInDir(dir)
	if count != 2 {
		t.Errorf("expected 2 git repos, got %d", count)
	}

	t.Run("worktree .git file counts", func(t *testing.T) {
		dir2 := t.TempDir()
		wtDir := filepath.Join(dir2, "wt-repo")
		if err := os.MkdirAll(wtDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Worktrees have .git as a file
		if err := os.WriteFile(filepath.Join(wtDir, ".git"), []byte("gitdir: /some/path"), 0o644); err != nil {
			t.Fatal(err)
		}
		count := countGitReposInDir(dir2)
		if count != 1 {
			t.Errorf("expected 1 worktree repo, got %d", count)
		}
	})

	t.Run("empty dir returns 0", func(t *testing.T) {
		dir2 := t.TempDir()
		count := countGitReposInDir(dir2)
		if count != 0 {
			t.Errorf("expected 0 repos in empty dir, got %d", count)
		}
	})

	t.Run("nonexistent dir returns 0", func(t *testing.T) {
		count := countGitReposInDir("/nonexistent/path/xyz")
		if count != 0 {
			t.Errorf("expected 0 repos for nonexistent dir, got %d", count)
		}
	})
}

func TestWorkspaceManagerAddRootPickerComplete(t *testing.T) {
	setWorkspacePickerHome(t)
	roots := makeRoots("/tmp/r0")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	// Open the picker — returns the picker's Init cmd (scan + readDir).
	m, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if !m.IsPickerActive() {
		t.Fatal("precondition: expected picker active")
	}

	// Get the highlighted entry path (what enter will select)
	active := &m.picker.columns[len(m.picker.columns)-1]
	highlightedPath := active.highlightedPath()

	// Send a gitRepoScanMsg to mark scan done (real message flow).
	pickerDir := m.picker.currentDir()
	m, _ = m.Update(gitRepoScanMsg{
		dir:           pickerDir,
		count:         1,
		repoDirs:      map[string]bool{active.highlightedName(): true},
		dirRepoCounts: map[string]int{},
	})

	// Press Enter to select the highlighted directory.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.IsPickerActive() {
		t.Error("expected picker deactivated after completion")
	}
	added := m.ConsumeAddedRoot()
	if added != highlightedPath {
		t.Errorf("expected added root %q, got %q", highlightedPath, added)
	}
}

func TestWorkspaceManagerConsumeAddedIsIdempotent(t *testing.T) {
	setWorkspacePickerHome(t)
	roots := makeRoots("/tmp/r0")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	// Open picker and complete via real message flow
	m, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	active := &m.picker.columns[len(m.picker.columns)-1]
	highlightedPath := active.highlightedPath()
	pickerDir := m.picker.currentDir()
	m, _ = m.Update(gitRepoScanMsg{
		dir:           pickerDir,
		count:         1,
		repoDirs:      map[string]bool{active.highlightedName(): true},
		dirRepoCounts: map[string]int{},
	})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	first := m.ConsumeAddedRoot()
	if first != highlightedPath {
		t.Errorf("expected first ConsumeAddedRoot() == %q, got %q", highlightedPath, first)
	}
	second := m.ConsumeAddedRoot()
	if second != "" {
		t.Errorf("expected second ConsumeAddedRoot() == \"\", got %q", second)
	}
}

func TestWorkspaceManagerDeleteOnEmpty(t *testing.T) {
	m := NewWorkspaceManagerModel(nil, 80, 40)

	// Press 'd' on empty list — should be a no-op, not panic
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if m.confirmDelete {
		t.Error("expected no confirm state when root list is empty")
	}
}

func TestWorkspaceManagerEmptyKeyHints(t *testing.T) {
	m := NewWorkspaceManagerModel(nil, 80, 40)
	view := m.View()

	// Empty state should only show 'a add' and 'esc close', not 'd remove'
	if !strings.Contains(view, "a add") {
		t.Error("expected 'a add' in empty state hints")
	}
	if !strings.Contains(view, "esc close") {
		t.Error("expected 'esc close' in empty state hints")
	}
	if strings.Contains(view, "d remove") {
		t.Error("expected no 'd remove' in empty state hints")
	}
}

func TestWorkspaceManagerNonKeyMsgPassthrough(t *testing.T) {
	roots := makeRoots("/tmp/r0")
	m := NewWorkspaceManagerModel(roots, 80, 40)

	// Non-key messages should not change state when picker is not active
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	if m.IsClosed() {
		t.Error("expected non-key message to not close overlay")
	}
	if m.IsPickerActive() {
		t.Error("expected non-key message to not activate picker")
	}
}

func TestWorkspaceManagerInitReturnsNil(t *testing.T) {
	m := NewWorkspaceManagerModel(nil, 80, 40)
	cmd := m.Init()
	if cmd != nil {
		t.Error("expected Init() to return nil cmd")
	}
}

func TestWorkspaceManagerPickerReceivesInitialSize(t *testing.T) {
	// When a picker is opened via 'a', the workspace manager should inject
	// its known terminal dimensions into the picker immediately (matching
	// the welcome flow pattern at welcome.go:76-81).
	m := NewWorkspaceManagerModel(nil, 120, 50)

	m, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if !m.IsPickerActive() {
		t.Fatal("expected picker active after 'a'")
	}

	// The picker's width/height should match the manager's dimensions.
	if m.picker.width != 120 {
		t.Errorf("picker.width = %d, want 120", m.picker.width)
	}
	if m.picker.height != 50 {
		t.Errorf("picker.height = %d, want 50", m.picker.height)
	}
}

func TestWorkspaceManagerWindowSizeForwardedToPicker(t *testing.T) {
	// When the workspace manager receives a WindowSizeMsg while the picker
	// is active, it must forward the message to the picker so it can resize.
	m := NewWorkspaceManagerModel(nil, 80, 40)

	// Open picker
	m, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if !m.IsPickerActive() {
		t.Fatal("expected picker active")
	}

	// Verify initial size was set
	if m.picker.width != 80 || m.picker.height != 40 {
		t.Fatalf("initial picker size = %dx%d, want 80x40", m.picker.width, m.picker.height)
	}

	// Send WindowSizeMsg — should update both manager and picker
	m, _ = m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})

	if m.width != 200 || m.height != 60 {
		t.Errorf("manager size = %dx%d, want 200x60", m.width, m.height)
	}
	if m.picker.width != 200 || m.picker.height != 60 {
		t.Errorf("picker size = %dx%d, want 200x60", m.picker.width, m.picker.height)
	}
}

func TestWorkspaceManagerWindowSizeUpdatesManagerWithoutPicker(t *testing.T) {
	// WindowSizeMsg should update the manager's own dimensions even when
	// the picker is not active (no panic, no no-op).
	m := NewWorkspaceManagerModel(nil, 80, 40)

	m, _ = m.Update(tea.WindowSizeMsg{Width: 150, Height: 70})

	if m.width != 150 || m.height != 70 {
		t.Errorf("manager size = %dx%d, want 150x70", m.width, m.height)
	}
}
