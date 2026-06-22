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

func newTestWelcomeModel(t *testing.T) WelcomeModel {
	t.Helper()
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "workspace"), 0o755); err != nil {
		t.Fatalf("create welcome picker HOME entry: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return NewWelcomeModel()
}

func TestWelcomeModelInitialView(t *testing.T) {
	m := newTestWelcomeModel(t)
	view := m.View()
	if !strings.Contains(view, "AI-assisted development workflows") {
		t.Error("expected welcome description in initial view")
	}
}

func TestWelcomeIntroArtSpellsAgentico(t *testing.T) {
	m := newTestWelcomeModel(t)
	view := m.View()
	if !strings.Contains(view, " ▄▀█ █▀▀ █▀▀ █▄░█ ▀█▀ █ █▀▀ █▀█") {
		t.Errorf("welcome intro art missing AGENTICO top row:\n%s", view)
	}
	if !strings.Contains(view, " █▀█ █▄█ ██▄ █░▀█ ░█░ █ █▄▄ █▄█") {
		t.Errorf("welcome intro art missing AGENTICO bottom row:\n%s", view)
	}
}

func TestWelcomeModelEnterGoesToPicker(t *testing.T) {
	m := newTestWelcomeModel(t)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != welcomeStepPicker {
		t.Errorf("expected step picker, got %d", m.step)
	}
	// Should show dir picker view (filepicker-based browser with key hints)
	view := m.View()
	if !strings.Contains(view, "space select") {
		t.Error("expected dir picker footer in view after Enter")
	}
}

func TestWelcomeModelEscCancels(t *testing.T) {
	m := newTestWelcomeModel(t)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.IsDone() {
		t.Error("expected done after Esc")
	}
	if !m.IsCancelled() {
		t.Error("expected cancelled after Esc")
	}
}

func TestWelcomeStepTransitions(t *testing.T) {
	dir := t.TempDir()
	m := newTestWelcomeModel(t)

	// Intro → Picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != welcomeStepPicker {
		t.Fatalf("expected picker step, got %d", m.step)
	}

	// Simulate picker completion
	m.picker.selected = dir
	m.picker.done = true
	m.picker.gitRepoCount = 5

	// Send a key to trigger welcome to detect picker completion → Confirm
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != welcomeStepConfirm {
		t.Fatalf("expected confirm step, got %d", m.step)
	}

	// PendingRoot should be available
	pending := m.PendingRoot()
	if pending != dir {
		t.Errorf("expected PendingRoot() = %q, got %q", dir, pending)
	}

	// Consume the pending root
	consumed := m.ConsumePendingRoot()
	if consumed != dir {
		t.Errorf("expected ConsumePendingRoot() = %q, got %q", dir, consumed)
	}

	// Press Enter on confirm → done
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.IsDone() {
		t.Error("expected done after Enter on confirm")
	}
	result := m.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.SelectedRoots) != 1 || result.SelectedRoots[0] != dir {
		t.Errorf("expected SelectedRoots = [%q], got %v", dir, result.SelectedRoots)
	}
}

func TestWelcomeAddAnotherLoop(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	m := newTestWelcomeModel(t)

	// Intro → Picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Select root1 via picker
	m.picker.selected = root1
	m.picker.done = true
	m.picker.gitRepoCount = 2
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != welcomeStepConfirm {
		t.Fatalf("expected confirm step after root1, got %d", m.step)
	}

	// Consume root1
	consumed := m.ConsumePendingRoot()
	if consumed != root1 {
		t.Errorf("expected consumed root1 = %q, got %q", root1, consumed)
	}

	// Press 'a' to add another → back to picker
	m, _ = m.Update(tea.KeyPressMsg{Text: "a"})
	if m.step != welcomeStepPicker {
		t.Fatalf("expected picker step after 'a', got %d", m.step)
	}

	// Select root2 via picker
	m.picker.selected = root2
	m.picker.done = true
	m.picker.gitRepoCount = 4
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != welcomeStepConfirm {
		t.Fatalf("expected confirm step after root2, got %d", m.step)
	}

	// Consume root2
	consumed = m.ConsumePendingRoot()
	if consumed != root2 {
		t.Errorf("expected consumed root2 = %q, got %q", root2, consumed)
	}

	// Press Enter to finalize
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.IsDone() {
		t.Error("expected done after Enter on confirm")
	}
	result := m.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.SelectedRoots) != 2 {
		t.Fatalf("expected 2 roots, got %d", len(result.SelectedRoots))
	}
	if result.SelectedRoots[0] != root1 {
		t.Errorf("expected SelectedRoots[0] = %q, got %q", root1, result.SelectedRoots[0])
	}
	if result.SelectedRoots[1] != root2 {
		t.Errorf("expected SelectedRoots[1] = %q, got %q", root2, result.SelectedRoots[1])
	}
}

func TestWelcomeConfirmView(t *testing.T) {
	dir := t.TempDir()
	m := newTestWelcomeModel(t)

	// Intro → Picker → Confirm
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.picker.selected = dir
	m.picker.done = true
	m.picker.gitRepoCount = 7
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != welcomeStepConfirm {
		t.Fatalf("expected confirm step, got %d", m.step)
	}

	// Consume pending root so it appears in selectedRoots for the view
	m.ConsumePendingRoot()

	// Set up another pending root to test the pendingRoot display in the view
	m.pendingRoot = dir
	m.pendingCount = 7

	view := m.View()
	if !strings.Contains(view, dir) {
		t.Errorf("expected view to contain dir path %q", dir)
	}
	if !strings.Contains(view, "7 git repos found") {
		t.Error("expected view to contain repo count")
	}
	if !strings.Contains(view, "add another") {
		t.Error("expected view to contain 'add another' hint")
	}
	if !strings.Contains(view, "continue to dashboard") {
		t.Error("expected view to contain 'continue to dashboard' hint")
	}
}

func TestWelcomeConfirmRepoCount(t *testing.T) {
	t.Run("plural", func(t *testing.T) {
		dir := t.TempDir()
		m := newTestWelcomeModel(t)

		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m.picker.selected = dir
		m.picker.done = true
		m.picker.gitRepoCount = 3
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

		view := m.View()
		if !strings.Contains(view, "3 git repos found") {
			t.Errorf("expected '3 git repos found' in view, got:\n%s", view)
		}
	})

	t.Run("singular", func(t *testing.T) {
		dir := t.TempDir()
		m := newTestWelcomeModel(t)

		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m.picker.selected = dir
		m.picker.done = true
		m.picker.gitRepoCount = 1
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

		view := m.View()
		if !strings.Contains(view, "1 git repo found") {
			t.Errorf("expected '1 git repo found' in view, got:\n%s", view)
		}
	})
}

func TestWelcomeEscOnIntroSetsSkipped(t *testing.T) {
	m := newTestWelcomeModel(t)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.IsDone() {
		t.Error("expected done after Esc on intro")
	}
	if !m.IsCancelled() {
		t.Error("expected cancelled (skipped) after Esc on intro")
	}
}

func TestWelcomeEscOnConfirmGoesBackToPicker(t *testing.T) {
	dir := t.TempDir()
	m := newTestWelcomeModel(t)

	// Intro → Picker → Confirm
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.picker.selected = dir
	m.picker.done = true
	m.picker.gitRepoCount = 2
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != welcomeStepConfirm {
		t.Fatalf("expected confirm step, got %d", m.step)
	}

	// Press Esc on confirm → back to picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.step != welcomeStepPicker {
		t.Errorf("expected picker step after Esc on confirm, got %d", m.step)
	}
	if m.IsDone() {
		t.Error("expected not done after Esc on confirm")
	}
}

func TestWelcomeModelPickerCancel(t *testing.T) {
	m := newTestWelcomeModel(t)
	// Go to picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != welcomeStepPicker {
		t.Fatal("expected picker step")
	}
	// Cancel picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	// Should go back to intro, not be done
	if m.step != welcomeStepIntro {
		t.Errorf("expected back to intro, got step %d", m.step)
	}
	if m.IsDone() {
		t.Error("expected not done after picker cancel (goes back to intro)")
	}
}

func TestWelcomePickerCancelWithRootsGoesToConfirm(t *testing.T) {
	root1 := t.TempDir()
	m := newTestWelcomeModel(t)

	// Intro → Picker → select root1 → Confirm
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.picker.selected = root1
	m.picker.done = true
	m.picker.gitRepoCount = 2
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != welcomeStepConfirm {
		t.Fatalf("expected confirm step, got %d", m.step)
	}

	// Consume root1
	m.ConsumePendingRoot()

	// Press 'a' to add another → picker
	m, _ = m.Update(tea.KeyPressMsg{Text: "a"})
	if m.step != welcomeStepPicker {
		t.Fatalf("expected picker step after 'a', got %d", m.step)
	}

	// Cancel the picker (Esc)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	// Should go to confirm (not intro), because roots already exist
	if m.step != welcomeStepConfirm {
		t.Errorf("expected confirm step after picker cancel with roots, got %d", m.step)
	}
	if m.IsDone() {
		t.Error("expected not done after picker cancel")
	}
}

func TestWelcomeMultipleRootsAccumulate(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	root3 := t.TempDir()
	m := newTestWelcomeModel(t)

	roots := []string{root1, root2, root3}
	for i, root := range roots {
		if i == 0 {
			// First time: Intro → Picker
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		} else {
			// Subsequent: press 'a' on confirm → Picker
			m, _ = m.Update(tea.KeyPressMsg{Text: "a"})
		}

		// Select root via picker
		m.picker.selected = root
		m.picker.done = true
		m.picker.gitRepoCount = i + 1
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.step != welcomeStepConfirm {
			t.Fatalf("expected confirm step after root %d, got %d", i+1, m.step)
		}

		// Consume each root
		consumed := m.ConsumePendingRoot()
		if consumed != root {
			t.Errorf("expected consumed root %d = %q, got %q", i+1, root, consumed)
		}
	}

	// Finalize
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.IsDone() {
		t.Fatal("expected done after Enter on confirm")
	}
	result := m.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.SelectedRoots) != 3 {
		t.Fatalf("expected 3 roots, got %d", len(result.SelectedRoots))
	}
	for i, root := range roots {
		if result.SelectedRoots[i] != root {
			t.Errorf("expected SelectedRoots[%d] = %q, got %q", i, root, result.SelectedRoots[i])
		}
	}
}

func TestWelcomeImmediateRootNotification(t *testing.T) {
	dir := t.TempDir()
	m := newTestWelcomeModel(t)

	// PendingRoot() should be empty initially
	if got := m.PendingRoot(); got != "" {
		t.Errorf("expected empty PendingRoot() initially, got %q", got)
	}

	// Intro → Picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Simulate picker completion
	m.picker.selected = dir
	m.picker.done = true
	m.picker.gitRepoCount = 4
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// After picker completes, rootReady should be true and PendingRoot returns the path
	if !m.rootReady {
		t.Error("expected rootReady to be true after picker completion")
	}
	pending := m.PendingRoot()
	if pending != dir {
		t.Errorf("expected PendingRoot() = %q, got %q", dir, pending)
	}

	// Consume the pending root
	consumed := m.ConsumePendingRoot()
	if consumed != dir {
		t.Errorf("expected ConsumePendingRoot() = %q, got %q", dir, consumed)
	}

	// After consuming, PendingRoot should be empty
	if got := m.PendingRoot(); got != "" {
		t.Errorf("expected empty PendingRoot() after consume, got %q", got)
	}

	// The consumed root should be in selectedRoots
	if len(m.selectedRoots) != 1 || m.selectedRoots[0] != dir {
		t.Errorf("expected selectedRoots = [%q], got %v", dir, m.selectedRoots)
	}
}

func TestWelcomeModelPickerCompletion(t *testing.T) {
	dir := t.TempDir()
	m := newTestWelcomeModel(t)

	// Go to picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Set picker to completed state
	m.picker.selected = dir
	m.picker.done = true
	m.picker.gitRepoCount = 2

	// Send a message to trigger welcome model to detect picker completion
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// After picker completes, model goes to CONFIRM step (not done)
	if m.step != welcomeStepConfirm {
		t.Errorf("expected confirm step after picker completion, got %d", m.step)
	}
	if m.IsDone() {
		t.Error("expected not done after picker completion (should be at confirm)")
	}

	// Consume pending root
	consumed := m.ConsumePendingRoot()
	if consumed != dir {
		t.Errorf("expected consumed = %q, got %q", dir, consumed)
	}

	// Press Enter on confirm to finalize
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.IsDone() {
		t.Error("expected done after Enter on confirm")
	}
	if m.IsCancelled() {
		t.Error("expected not cancelled")
	}
	result := m.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.SelectedRoots) != 1 || result.SelectedRoots[0] != dir {
		t.Errorf("expected SelectedRoots = [%q], got %v", dir, result.SelectedRoots)
	}
}

func TestWelcomeModelWindowSizeForwardedToPicker(t *testing.T) {
	m := newTestWelcomeModel(t)

	// Simulate initial WindowSizeMsg arriving before entering picker
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 || m.height != 40 {
		t.Fatalf("expected stored size 120x40, got %dx%d", m.width, m.height)
	}

	// Transition to picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != welcomeStepPicker {
		t.Fatal("expected picker step")
	}

	// Picker should have the terminal size applied
	if m.picker.width != 120 || m.picker.height != 40 {
		t.Errorf("expected picker dimensions 120x40, got %dx%d", m.picker.width, m.picker.height)
	}

	// Now send another WindowSizeMsg while in picker step — should forward
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.picker.width != 80 || m.picker.height != 24 {
		t.Errorf("expected picker dimensions 80x24 after resize, got %dx%d", m.picker.width, m.picker.height)
	}
}
