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
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func advanceWizardToWhereViaUI(m WizardModel, name string) WizardModel {
	m, _ = m.Update(tea.KeyPressMsg{Text: name})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return m
}

func normalizedWizardViewText(view string) string {
	return strings.Join(strings.Fields(ansiRegex.ReplaceAllString(view, "")), " ")
}

func requireWizardViewContainsAll(t *testing.T, view string, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		if !strings.Contains(view, needle) {
			t.Fatalf("wizard view missing %q:\n%s", needle, view)
		}
	}
}

func TestWizardSteps(t *testing.T) {
	repos := []string{"repo-a", "repo-b"}
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Inquiry:        "opus",
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
		ExitCriteria: "tests pass",
	}

	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// Step 1 (What): Enter name, then advance
	m = advanceWizardToWhereViaUI(m, "test-feat")

	// Step 2 (Where): Select repos
	if m.step != wizardStepWhere {
		t.Errorf("expected step Where, got %d", m.step)
	}
	// Add first repo (Enter on list axis = add to chips), then Ctrl+D to advance
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})

	// Step 3 (Pipeline): Confirm selection
	if m.step != wizardStepPipeline {
		t.Errorf("expected step Pipeline, got %d", m.step)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Step 4 (Review): Confirm with G
	if m.step != wizardStepReview {
		t.Errorf("expected step Review, got %d", m.step)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})

	if !m.IsDone() {
		t.Error("expected wizard to be done")
	}

	result := m.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Name != "test-feat" {
		t.Errorf("name = %q, want %q", result.Name, "test-feat")
	}
	if result.ExitCriteria != "tests pass" {
		t.Errorf("exit criteria = %q, want %q", result.ExitCriteria, "tests pass")
	}
}

func TestWizardPanelWidthReviewScalesBeyondLegacyCap(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.width = 220
	m.step = wizardStepReview

	got := m.wizardPanelWidth()
	if got <= 100 {
		t.Fatalf("wizardPanelWidth() = %d, want > 100 on wide review screens", got)
	}
	if got > 160 {
		t.Fatalf("wizardPanelWidth() = %d, want <= 160 hard max", got)
	}
}

func TestWizardZeroConfigDefaultsToManualPublish(t *testing.T) {
	// Zero-config: use NewDefault() defaults, which should have ManualPublish=true.
	cfg := config.NewDefault()
	m := NewWizardModel(nil, nil, nil, cfg.Defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review
	m, _ = m.advance() // Review -> done

	if !m.IsDone() {
		t.Fatal("expected wizard to be done")
	}
	result := m.Result()
	if !result.Checkpoints.ManualPublish {
		t.Error("expected ManualPublish to be true for zero-config wizard (users should not auto-publish by default)")
	}
}

func TestWizardPipelineDefaultsApplyCheckpoints(t *testing.T) {
	// Config with explicit checkpoints that disable ManualPublish.
	defaults := config.NewDefault().Defaults
	defaults.Checkpoints.ManualPublish = false
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline (default: large)
	m, _ = m.advance() // Pipeline -> Review
	m, _ = m.advance() // Review -> done

	if !m.IsDone() {
		t.Fatal("expected wizard to be done")
	}
	result := m.Result()
	// Large applies compatible configured defaults and preserves explicit false.
	if !result.Checkpoints.DesignReview {
		t.Error("expected DesignReview to be true (configured default)")
	}
	if result.Checkpoints.ManualPublish {
		t.Error("expected ManualPublish to remain false from configured default")
	}
}

func TestWizardCancel(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.IsCancelled() {
		t.Error("expected wizard to be cancelled")
	}
}

func TestWizardViewRenders(t *testing.T) {
	m := NewWizardModel([]string{"repo-a"}, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	view := m.View()
	if view == "" {
		t.Error("expected non-empty view")
	}
	if !containsString(view, "New Feature") {
		t.Error("expected wizard title in view")
	}
}

func TestWizardKeyNotEatenInTextInput(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// We're on the What step (name text input)
	// Type "jk" — these should NOT be eaten by navigation
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})

	if m.nameInput.Value() != "jk" {
		t.Errorf("name input = %q, want %q (j/k keys should not be eaten on text input steps)", m.nameInput.Value(), "jk")
	}
}

func TestWizardImagePasteMsg(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// We're on What step. Switch focus to description.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab}) // whatFocus 0 -> 1

	if m.whatFocus != 1 {
		t.Fatalf("expected whatFocus=1, got %d", m.whatFocus)
	}

	// Simulate successful image paste
	m, _ = m.Update(ImagePastedMsg{Path: "/tmp/image-1.png"})
	if len(m.images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(m.images))
	}
	if m.images[0] != "/tmp/image-1.png" {
		t.Errorf("image path = %q, want %q", m.images[0], "/tmp/image-1.png")
	}
	if !containsString(m.descInput.Value(), "[Image #1]") {
		t.Errorf("expected '[Image #1]' in description, got %q", m.descInput.Value())
	}

	// Simulate second image paste
	m, _ = m.Update(ImagePastedMsg{Path: "/tmp/image-2.png"})
	if len(m.images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(m.images))
	}
	if !containsString(m.descInput.Value(), "[Image #2]") {
		t.Errorf("expected '[Image #2]' in description, got %q", m.descInput.Value())
	}

	// Simulate failed paste — should not add image
	m, _ = m.Update(ImagePasteFailedMsg{})
	if len(m.images) != 2 {
		t.Errorf("expected 2 images after failed paste, got %d", len(m.images))
	}
}

func TestWizardImagesInResult(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// Add images via messages
	m.nameInput.SetValue("image test")
	m, _ = m.Update(ImagePastedMsg{Path: "/tmp/img1.png"})
	m, _ = m.Update(ImagePastedMsg{Path: "/tmp/img2.png"})

	// Advance through all steps
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review
	m, _ = m.advance() // Review -> done

	if !m.IsDone() {
		t.Fatal("expected wizard to be done")
	}
	result := m.Result()
	if len(result.Images) != 2 {
		t.Fatalf("expected 2 images in result, got %d", len(result.Images))
	}
	if result.Images[0] != "/tmp/img1.png" {
		t.Errorf("result.Images[0] = %q, want %q", result.Images[0], "/tmp/img1.png")
	}
	if result.Images[1] != "/tmp/img2.png" {
		t.Errorf("result.Images[1] = %q, want %q", result.Images[1], "/tmp/img2.png")
	}
}

func TestWizardImageNumberingAfterFailedPaste(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// Simulate a failed paste followed by a successful paste.
	// The successful paste should still be numbered [Image #1] since
	// display numbering is derived from len(images), not from imageCounter.
	m, _ = m.Update(ImagePasteFailedMsg{})
	if len(m.images) != 0 {
		t.Fatalf("expected 0 images after failed paste, got %d", len(m.images))
	}

	// Now a successful paste
	m, _ = m.Update(ImagePastedMsg{Path: "/tmp/first.png"})
	if len(m.images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(m.images))
	}
	if !containsString(m.descInput.Value(), "[Image #1]") {
		t.Errorf("expected '[Image #1]' after failed-then-success, got %q", m.descInput.Value())
	}

	// Another failed paste, then another success — should be [Image #2]
	m, _ = m.Update(ImagePasteFailedMsg{})
	m, _ = m.Update(ImagePastedMsg{Path: "/tmp/second.png"})
	if len(m.images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(m.images))
	}
	if !containsString(m.descInput.Value(), "[Image #2]") {
		t.Errorf("expected '[Image #2]' after second failed-then-success, got %q", m.descInput.Value())
	}
}

func TestWizardBackFromWhere(t *testing.T) {
	m := NewWizardModel([]string{"repo-a"}, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("my-feature")
	m, _ = m.advance() // What -> Where

	if m.step != wizardStepWhere {
		t.Fatalf("expected step Where, got %d", m.step)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // Where -> What

	if m.step != wizardStepWhat {
		t.Errorf("expected step What after back, got %d", m.step)
	}
	if m.nameInput.Value() != "my-feature" {
		t.Errorf("name input = %q, want %q (should preserve value)", m.nameInput.Value(), "my-feature")
	}
}

func TestWizardBackFromReview(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	if m.step != wizardStepReview {
		t.Fatalf("expected step Review, got %d", m.step)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // Review -> Pipeline

	if m.step != wizardStepPipeline {
		t.Errorf("expected step Pipeline after back from Review, got %d", m.step)
	}
}

func TestWizardBackDoesNothingOnFirstStep(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})

	if m.step != wizardStepWhat {
		t.Errorf("expected to stay on What step, got %d", m.step)
	}
	if m.IsCancelled() {
		t.Error("back on first step should not cancel wizard")
	}
}

func TestWizardBackAndForwardPreservesState(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research: "sonnet", Planning: "sonnet",
			Implementation: "sonnet", Review: "sonnet",
		},
	}
	m := NewWizardModel([]string{"repo-a", "repo-b"}, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m.descInput.SetValue("my description")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["repo-a"] = true
	m.selectedRepos["repo-b"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	// Go back one step
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // Review -> Where

	// Go forward again
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Where -> Pipeline
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Pipeline -> Review

	if m.step != wizardStepReview {
		t.Fatalf("expected Review step, got %d", m.step)
	}

	// Selection state should be preserved
	if !m.selectedRepos["repo-a"] || !m.selectedRepos["repo-b"] {
		t.Error("repo selection should be preserved after back+forward")
	}
}

func TestWizardBackFooterHint(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.width = 80
	m.height = 24

	// On first step (What), footer should NOT show shift+tab as "Back"
	view := m.View()
	// The What step footer says "[tab] Switch field   [enter] Next   [esc] Cancel"
	if containsString(view, "shift+tab] Back") {
		t.Error("footer should not show 'Back' hint on first step")
	}

	// Advance to Where
	m.nameInput.SetValue("test")
	m, _ = m.advance()

	view = m.View()
	if !containsString(view, "esc") || !containsString(view, "Back") {
		t.Error("footer should show back hint after first step")
	}
}

func TestWizardCtrlVNoOpWithoutPasteSupport(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.canPasteImages = false // simulate paste not available

	// Type some text first
	m, _ = m.Update(tea.KeyPressMsg{Text: "hello"})
	valBefore := m.nameInput.Value()

	// Press Ctrl+V — should be consumed as a no-op
	m, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Error("expected nil cmd from Ctrl+V")
	}

	// Verify no state changes
	if m.nameInput.Value() != valBefore {
		t.Errorf("name changed after Ctrl+V: %q -> %q", valBefore, m.nameInput.Value())
	}
	if len(m.images) != 0 {
		t.Errorf("expected 0 images, got %d", len(m.images))
	}
}

func TestWizardTotalSteps(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	got := m.totalStepCount()
	// 4-step wizard: What, Where, Pipeline, Review
	if got != 4 {
		t.Errorf("totalStepCount() = %d, want 4", got)
	}
}

func TestWizardMultiRepoResult(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "sonnet",
			Planning:       "sonnet",
			Implementation: "sonnet",
			Review:         "sonnet",
		},
	}
	m := NewWizardModel([]string{"repo-a", "repo-b"}, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("multi-repo feature")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["repo-a"] = true
	m.selectedRepos["repo-b"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review
	m, _ = m.advance() // Review -> done

	if !m.IsDone() {
		t.Fatal("expected wizard to be done")
	}
	result := m.Result()
	if len(result.Repos) != 2 {
		t.Errorf("expected 2 repos, got %d", len(result.Repos))
	}
}

func TestWizardRepoOrderDeterministic(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "sonnet",
			Planning:       "sonnet",
			Implementation: "sonnet",
			Review:         "sonnet",
		},
	}
	// availRepos defines the deterministic order
	m := NewWizardModel([]string{"repo-c", "repo-a", "repo-b"}, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("order test")
	m, _ = m.advance() // What -> Where

	// Select all repos (in any order via map)
	m.selectedRepos["repo-b"] = true
	m.selectedRepos["repo-a"] = true
	m.selectedRepos["repo-c"] = true

	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review
	m, _ = m.advance() // Review -> done

	if !m.IsDone() {
		t.Fatal("expected wizard to be done")
	}
	result := m.Result()

	// Repos must follow availRepos order, not map iteration order
	expected := []string{"repo-c", "repo-a", "repo-b"}
	if len(result.Repos) != len(expected) {
		t.Fatalf("repos count = %d, want %d", len(result.Repos), len(expected))
	}
	for i, r := range result.Repos {
		if r != expected[i] {
			t.Errorf("repos[%d] = %q, want %q (must preserve availRepos order)", i, r, expected[i])
		}
	}
}

// --- Tab focus toggle on What step ---

func TestWizardWhatStepTabTogglesFocus(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	// Initially whatFocus=0 (name)
	if m.whatFocus != 0 {
		t.Fatalf("expected whatFocus=0 initially, got %d", m.whatFocus)
	}

	// Tab switches to description
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whatFocus != 1 {
		t.Errorf("expected whatFocus=1 after tab, got %d", m.whatFocus)
	}

	// Tab switches back to name
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whatFocus != 0 {
		t.Errorf("expected whatFocus=0 after second tab, got %d", m.whatFocus)
	}
}

func TestWizardWhatStepShiftTabFromDesc(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	// Switch to description
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whatFocus != 1 {
		t.Fatalf("expected whatFocus=1, got %d", m.whatFocus)
	}

	// Shift+tab goes back to name
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.whatFocus != 0 {
		t.Errorf("expected whatFocus=0 after shift+tab, got %d", m.whatFocus)
	}
	if m.step != wizardStepWhat {
		t.Errorf("expected to stay on What step, got %d", m.step)
	}
}

func TestWizardWhatStepShiftEnterInDescriptionInsertsNewline(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m, _ = m.Update(tea.KeyPressMsg{Text: "name"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Text: "line1"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m, _ = m.Update(tea.KeyPressMsg{Text: "line2"})

	if got := m.descInput.Value(); got != "line1\nline2" {
		t.Fatalf("description = %q, want %q", got, "line1\nline2")
	}
	if m.step != wizardStepWhat {
		t.Fatalf("step = %d, want What after Shift+Enter newline", m.step)
	}
}

func TestWizardWhatStepDownFromNameFocusesDescription(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m, _ = m.Update(tea.KeyPressMsg{Text: "name"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	if m.whatFocus != 1 {
		t.Fatalf("whatFocus = %d, want description focus", m.whatFocus)
	}
	if m.nameInput.Focused() {
		t.Fatal("name input should be blurred after Down")
	}
	if !m.descInput.Focused() {
		t.Fatal("description input should be focused after Down")
	}
}

func TestWizardWhatStepUpFromDescriptionFirstLineFocusesName(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("name")
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Text: "description"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})

	if m.whatFocus != 0 {
		t.Fatalf("whatFocus = %d, want name focus", m.whatFocus)
	}
	if !m.nameInput.Focused() {
		t.Fatal("name input should be focused after Up from first description line")
	}
	if m.descInput.Focused() {
		t.Fatal("description input should be blurred after Up from first description line")
	}
}

func TestWizardWhatStepUpInsideWrappedDescriptionStaysInDescription(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("name")
	m.descInput.SetWidth(4)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Text: "abcdef"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})

	if m.whatFocus != 1 {
		t.Fatalf("whatFocus = %d, want description focus", m.whatFocus)
	}
	if !m.descInput.Focused() {
		t.Fatal("description input should remain focused when moving within wrapped text")
	}
	if got := m.descInput.col; got != 2 {
		t.Fatalf("description cursor col = %d, want 2 after moving up one visual line", got)
	}
}

// --- Review step only responds to G ---

func TestWizardReviewEnterIsNoOp(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	if m.step != wizardStepReview {
		t.Fatalf("expected Review step, got %d", m.step)
	}

	// Enter on the default Risk row should open editing, but must NOT create the feature.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.IsDone() {
		t.Error("Enter should not create feature on Review step (only G should)")
	}
	if m.step != wizardStepReview {
		t.Error("expected to stay on Review step after Enter")
	}
	if !m.summaryEditing {
		t.Error("expected Enter on default Risk row to start editing")
	}
}

func TestWizardReviewGCreates(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	// G creates
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !m.IsDone() {
		t.Error("expected wizard to be done after G on Review step")
	}
}

func TestWizardReviewEscGoesBack(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	if m.step != wizardStepReview {
		t.Fatalf("expected Review step, got %d", m.step)
	}

	// Esc should go back to Where, NOT cancel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.IsCancelled() {
		t.Error("Esc on Review should go back, not cancel wizard")
	}
	if m.step != wizardStepPipeline {
		t.Errorf("expected Pipeline step after Esc from Review, got %d", m.step)
	}
}

func TestWizardFilePasteMsg(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// Simulate successful file paste
	m, _ = m.Update(FilesPastedMsg{
		Paths: []string{"/tmp/spec.pdf"},
		Names: []string{"spec.pdf"},
	})
	if len(m.attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(m.attachments))
	}
	if m.attachments[0] != "/tmp/spec.pdf" {
		t.Errorf("attachment path = %q, want %q", m.attachments[0], "/tmp/spec.pdf")
	}
	if !containsString(m.descInput.Value(), "[spec.pdf]") {
		t.Errorf("expected '[spec.pdf]' in description, got %q", m.descInput.Value())
	}

	// Simulate second file paste
	m, _ = m.Update(FilesPastedMsg{
		Paths: []string{"/tmp/design.txt"},
		Names: []string{"design.txt"},
	})
	if len(m.attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(m.attachments))
	}
	if !containsString(m.descInput.Value(), "[design.txt]") {
		t.Errorf("expected '[design.txt]' in description, got %q", m.descInput.Value())
	}
}

func TestWizardAttachmentsInResult(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("attach test")
	m, _ = m.Update(FilesPastedMsg{
		Paths: []string{"/tmp/doc.pdf"},
		Names: []string{"doc.pdf"},
	})

	// Advance through all steps
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review
	m, _ = m.advance() // Review -> done

	if !m.IsDone() {
		t.Fatal("expected wizard to be done")
	}
	result := m.Result()
	if len(result.Attachments) != 1 {
		t.Fatalf("expected 1 attachment in result, got %d", len(result.Attachments))
	}
	if result.Attachments[0] != "/tmp/doc.pdf" {
		t.Errorf("result.Attachments[0] = %q, want %q", result.Attachments[0], "/tmp/doc.pdf")
	}
}

func TestWizardMixedImagesAndAttachments(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("mixed test")

	// Paste an image and a file
	m, _ = m.Update(ImagePastedMsg{Path: "/tmp/screenshot.png"})
	m, _ = m.Update(FilesPastedMsg{
		Paths: []string{"/tmp/spec.pdf"},
		Names: []string{"spec.pdf"},
	})

	if len(m.images) != 1 {
		t.Errorf("expected 1 image, got %d", len(m.images))
	}
	if len(m.attachments) != 1 {
		t.Errorf("expected 1 attachment, got %d", len(m.attachments))
	}

	desc := m.descInput.Value()
	if !containsString(desc, "[Image #1]") {
		t.Errorf("expected '[Image #1]' in description, got %q", desc)
	}
	if !containsString(desc, "[spec.pdf]") {
		t.Errorf("expected '[spec.pdf]' in description, got %q", desc)
	}
}

func TestWizardRepoFilter(t *testing.T) {
	repos := []string{"alpha", "beta", "gamma", "geo-intel", "geo-proxy"}
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Inquiry:        "opus",
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// filteredRepos should start as all repos
	if len(m.filteredRepos) != 5 {
		t.Fatalf("initial filteredRepos = %d, want 5", len(m.filteredRepos))
	}

	// Navigate to Where step: enter name then advance
	m = advanceWizardToWhereViaUI(m, "test")

	if m.step != wizardStepWhere {
		t.Fatalf("expected step Where, got %d", m.step)
	}

	// Type "geo" to filter
	m, _ = m.Update(tea.KeyPressMsg{Text: "geo"})

	if len(m.filteredRepos) != 2 {
		t.Errorf("filteredRepos after 'geo' = %d, want 2", len(m.filteredRepos))
	}
	if m.repoCursor != 0 {
		t.Errorf("repoCursor after filter = %d, want 0", m.repoCursor)
	}

	// Enter to add first filtered repo (geo-intel). After adding, the picked
	// repo is removed from the available list and the cursor naturally
	// points at the next one (geo-proxy).
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.selectedRepos["geo-intel"] {
		t.Error("expected geo-intel to be selected after Enter")
	}

	// Enter again to add the next available repo (geo-proxy)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.selectedRepos["geo-proxy"] {
		t.Error("expected geo-proxy to be selected after second Enter")
	}

	// Clear filter by sending backspace keys
	for range 3 {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}

	if len(m.filteredRepos) != 5 {
		t.Errorf("filteredRepos after clear = %d, want 5", len(m.filteredRepos))
	}

	// Selections should persist after clearing filter
	if !m.selectedRepos["geo-intel"] {
		t.Error("expected geo-intel selection to persist after clearing filter")
	}
	if !m.selectedRepos["geo-proxy"] {
		t.Error("expected geo-proxy selection to persist after clearing filter")
	}
}

func TestWizardCtrlNCtrlPNavigation(t *testing.T) {
	repos := []string{"alpha", "beta", "gamma", "delta"}
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, []string{"/tmp/roots"})

	// Navigate to Where step
	m = advanceWizardToWhereViaUI(m, "test")

	if m.step != wizardStepWhere {
		t.Fatalf("expected step Where, got %d", m.step)
	}
	if m.whereFocus != whereFocusList {
		t.Fatalf("expected initial focus on the list, got %d", m.whereFocus)
	}
	if m.repoCursor != 0 {
		t.Fatalf("expected repoCursor = 0, got %d", m.repoCursor)
	}

	// ctrl+n moves down within the repo list
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if m.repoCursor != 1 {
		t.Errorf("expected repoCursor = 1 after ctrl+n, got %d", m.repoCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if m.repoCursor != 2 {
		t.Errorf("expected repoCursor = 2 after second ctrl+n, got %d", m.repoCursor)
	}

	// ctrl+p moves up
	m, _ = m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.repoCursor != 1 {
		t.Errorf("expected repoCursor = 1 after ctrl+p, got %d", m.repoCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.repoCursor != 0 {
		t.Errorf("expected repoCursor = 0 after second ctrl+p, got %d", m.repoCursor)
	}

	// ctrl+p at top of list stays at 0
	m, _ = m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.repoCursor != 0 {
		t.Errorf("expected repoCursor to stay at 0, got %d", m.repoCursor)
	}

	// ctrl+n past the bottom of the list steps into the action axis (Browse)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}) // 0 -> 1
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}) // 1 -> 2
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}) // 2 -> 3 (last list row)
	if m.repoCursor != 3 || m.whereFocus != whereFocusList {
		t.Fatalf("expected to be on last list row, got cursor=%d focus=%d", m.repoCursor, m.whereFocus)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}) // list -> Browse
	if m.whereFocus != whereFocusBrowse {
		t.Errorf("expected focus on Browse after stepping past last list row, got %d", m.whereFocus)
	}

	// Browse -> Create
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if m.whereFocus != whereFocusCreate {
		t.Errorf("expected focus on Create after ctrl+n, got %d", m.whereFocus)
	}

	// Create -> Continue
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if m.whereFocus != whereFocusContinue {
		t.Errorf("expected focus on Continue after ctrl+n, got %d", m.whereFocus)
	}

	// ctrl+n on Continue stays there (bottom of the page)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if m.whereFocus != whereFocusContinue {
		t.Errorf("expected focus to stay on Continue, got %d", m.whereFocus)
	}
}

func TestWizardRepoFilterNoMatches(t *testing.T) {
	repos := []string{"alpha", "beta", "gamma"}
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// Navigate to Where step
	m = advanceWizardToWhereViaUI(m, "test")

	// Type nonsense filter
	m, _ = m.Update(tea.KeyPressMsg{Text: "zzz"})

	if len(m.filteredRepos) != 0 {
		t.Errorf("filteredRepos with nonsense = %d, want 0", len(m.filteredRepos))
	}

	// Tab should be a no-op when filtered list is empty (no repo to toggle)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	for _, r := range repos {
		if m.selectedRepos[r] {
			t.Errorf("expected no repos selected, but %s is selected", r)
		}
	}
}

func TestWizardUnifiedModelList(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(
		nil, nil, nil, defaults, "",
		map[string][]string{"test": {"opus", "gpt-5.4", "codex"}},
		[]string{"test"},
		nil,
		nil,
		nil, nil)

	_ = m // constructor accepts unified model list
}

// --- Test Group 1: File Picker (@ in description) ---

func TestWizardAtInDescriptionActivatesPicker(t *testing.T) {
	repos := []string{"repo-a", "repo-b"}
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// Tab to description (whatFocus=1)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whatFocus != 1 {
		t.Fatalf("expected whatFocus=1, got %d", m.whatFocus)
	}

	// Type "@" via KeyRunes
	m, _ = m.Update(tea.KeyPressMsg{Code: '@', Text: "@"})

	if !m.filePicker.IsActive() {
		t.Error("expected filePicker to be active after typing @ in description")
	}
}

func TestWizardAtInNameIsLiteral(t *testing.T) {
	repos := []string{"repo-a", "repo-b"}
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// Stay on name (whatFocus=0)
	if m.whatFocus != 0 {
		t.Fatalf("expected whatFocus=0 initially, got %d", m.whatFocus)
	}

	// Type "@proj" via KeyRunes
	m, _ = m.Update(tea.KeyPressMsg{Text: "@proj"})

	if !containsString(m.nameInput.Value(), "@proj") {
		t.Errorf("expected name input to contain '@proj', got %q", m.nameInput.Value())
	}
	if m.filePicker.IsActive() {
		t.Error("expected filePicker to NOT be active when typing @ in name input")
	}
}

func TestWizardFilePickerViewShown(t *testing.T) {
	repos := []string{"repo-a", "repo-b"}
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// Tab to description, type "@"
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.Update(tea.KeyPressMsg{Code: '@', Text: "@"})

	m.width = 80
	m.height = 24
	_ = m.View()

	if !m.filePicker.IsActive() {
		t.Error("expected filePicker to be active after typing @ in description")
	}
}

// --- Test Group 2: Clipboard Paste (Ctrl+V in description) ---

func TestWizardCtrlVInDescriptionTriggersPaste(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// Tab to description (whatFocus=1)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whatFocus != 1 {
		t.Fatalf("expected whatFocus=1, got %d", m.whatFocus)
	}

	// Enable paste support
	m.canPasteImages = true

	// Send Ctrl+V
	m, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})

	if cmd == nil {
		t.Error("expected non-nil cmd from Ctrl+V in description with paste support")
	}
}

func TestWizardCtrlVInNameIsNoOp(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// Stay on name (whatFocus=0)
	if m.whatFocus != 0 {
		t.Fatalf("expected whatFocus=0 initially, got %d", m.whatFocus)
	}

	// Enable paste support
	m.canPasteImages = true

	// Send Ctrl+V
	m, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})

	if cmd != nil {
		t.Error("expected nil cmd from Ctrl+V in name input")
	}
}

// --- Test Group 4: Description Hints in View ---

func TestWizardDescriptionHintWithPaste(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// Tab to description
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whatFocus != 1 {
		t.Fatalf("expected whatFocus=1, got %d", m.whatFocus)
	}

	m.canPasteImages = true
	m.width = 80
	m.height = 24

	view := m.View()
	if !containsString(view, "@ files") {
		t.Error("expected '@ files' in view when paste is supported")
	}
	if !containsString(view, "Ctrl+V paste") {
		t.Error("expected 'Ctrl+V paste' in view when paste is supported")
	}
}

func TestWizardDescriptionHintWithoutPaste(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	// Tab to description
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whatFocus != 1 {
		t.Fatalf("expected whatFocus=1, got %d", m.whatFocus)
	}

	m.canPasteImages = false
	m.width = 80
	m.height = 24

	view := m.View()
	if !containsString(view, "@ for file paths") {
		t.Error("expected '@ for file paths' in view when paste is not supported")
	}
	if containsString(view, "Ctrl+V") {
		t.Error("expected NO 'Ctrl+V' in view when paste is not supported")
	}
}

// --- Branch Warning Helpers ---

func wizardBranchInfoStub(infos ...RepoBranchInfo) func(WizardModel) []RepoBranchInfo {
	return func(WizardModel) []RepoBranchInfo {
		return infos
	}
}

func setupGitRepoOnBranch(t *testing.T, branchName string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %s: %v", args, out, err)
		}
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644)
	for _, args := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "initial"},
		{"git", "checkout", "-b", branchName},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %s: %v", args, out, err)
		}
	}
	return dir
}

func setupGitRepoOnDefault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %s: %v", args, out, err)
		}
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644)
	for _, args := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "initial"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %s: %v", args, out, err)
		}
	}
	return dir
}

// --- Test Group: Branch Warning Detection ---

func TestWizardBranchWarningShownOnOffDefault(t *testing.T) {
	repos := []string{"repo-a"}
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.detectBranchesFn = wizardBranchInfoStub(RepoBranchInfo{
		Name:          "repo-a",
		CurrentBranch: "feature/xyz",
		DefaultBranch: "main",
		IsOffDefault:  true,
	})
	// Fast-forward to What step complete
	m.nameInput.SetValue("test-feature")
	m.descInput.SetValue("test description")
	m, _ = m.advance() // What -> Where
	// Select the repo
	m.selectedRepos["repo-a"] = true
	// Press Enter on Where step
	m, _ = m.advance()
	// Should show branch warning, stay on Where
	if !m.showBranchWarning {
		t.Error("expected showBranchWarning to be true")
	}
	if m.step != wizardStepWhere {
		t.Errorf("expected step wizardStepWhere, got %v", m.step)
	}
	if len(m.branchInfos) == 0 {
		t.Error("expected branchInfos to be populated")
	}
	if m.branchScreenIndex != 0 {
		t.Errorf("expected branchScreenIndex 0, got %d", m.branchScreenIndex)
	}
	if m.branchOptionCursor != 0 {
		t.Errorf("expected branchOptionCursor 0 (default-branch pre-selected), got %d", m.branchOptionCursor)
	}
	// Default choice for the single off-default repo should be "use default branch" (false)
	if got, ok := m.branchChoices["repo-a"]; !ok || got {
		t.Errorf("expected branchChoices[repo-a]=false, got value=%v present=%v", got, ok)
	}
}

func TestWizardNoBranchWarningOnDefault(t *testing.T) {
	repos := []string{"repo-a"}
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.detectBranchesFn = wizardBranchInfoStub(RepoBranchInfo{
		Name:          "repo-a",
		CurrentBranch: "main",
		DefaultBranch: "main",
		IsOffDefault:  false,
	})
	m.nameInput.SetValue("test-feature")
	m.descInput.SetValue("test description")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["repo-a"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review (no warning)
	if m.showBranchWarning {
		t.Error("expected showBranchWarning to be false")
	}
	if m.step != wizardStepReview {
		t.Errorf("expected step wizardStepReview, got %v", m.step)
	}
}

func TestWizardNoBranchWarningNoRepoPaths(t *testing.T) {
	repos := []string{"repo-a"}
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test-feature")
	m.descInput.SetValue("test description")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["repo-a"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review (no paths = no detection)
	if m.showBranchWarning {
		t.Error("expected showBranchWarning to be false")
	}
	if m.step != wizardStepReview {
		t.Errorf("expected step wizardStepReview, got %v", m.step)
	}
}

// --- Test Group: Branch Warning Navigation ---

func TestWizardBranchScreenOptionToggle(t *testing.T) {
	// Within a single repo's screen, ↑/↓/tab all flip between the two
	// options (default / current). Only two options, so toggle keys are
	// symmetric - they don't care about direction.
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.step = wizardStepWhere
	m.showBranchWarning = true
	m.branchInfos = []RepoBranchInfo{{Name: "repo-a", CurrentBranch: "feature/xyz", DefaultBranch: "main", IsOffDefault: true}}
	m.branchChoices = map[string]bool{"repo-a": false}
	m.branchScreenIndex = 0
	m.branchOptionCursor = 0

	// Down flips to "current branch"
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.branchOptionCursor != 1 {
		t.Errorf("expected option cursor 1 after down, got %d", m.branchOptionCursor)
	}
	// Up flips back to "default branch"
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.branchOptionCursor != 0 {
		t.Errorf("expected option cursor 0 after up, got %d", m.branchOptionCursor)
	}
	// Tab also toggles
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.branchOptionCursor != 1 {
		t.Errorf("expected option cursor 1 after tab, got %d", m.branchOptionCursor)
	}
}

func TestWizardBranchScreenEnterAdvancesPerRepo(t *testing.T) {
	// With two off-default repos: Enter on the first repo saves its choice
	// AND advances to the second repo's screen (not to Pipeline). Enter on
	// the second one (the last) advances to Pipeline.
	defaults := config.DefaultsConfig{}
	repos := []string{"repo-a", "repo-b"}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.step = wizardStepWhere
	m.showBranchWarning = true
	m.branchInfos = []RepoBranchInfo{
		{Name: "repo-a", CurrentBranch: "feature/xyz", DefaultBranch: "main", IsOffDefault: true},
		{Name: "repo-b", CurrentBranch: "bugfix/foo", DefaultBranch: "master", IsOffDefault: true},
	}
	m.branchChoices = map[string]bool{"repo-a": false, "repo-b": false}
	m.nameInput.SetValue("test-feature")
	m.descInput.SetValue("test description")
	m.selectedRepos["repo-a"] = true
	m.selectedRepos["repo-b"] = true

	// Pick "current branch" on repo-a (Tab to toggle), then Enter.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != wizardStepWhere {
		t.Errorf("expected to still be on Where step (next repo screen), got %v", m.step)
	}
	if m.branchScreenIndex != 1 {
		t.Errorf("expected to be on screen 1 (repo-b), got %d", m.branchScreenIndex)
	}
	if !m.branchChoices["repo-a"] {
		t.Error("expected repo-a choice to be saved as true")
	}
	// repo-b's screen should start with the recommended (default) option
	if m.branchOptionCursor != 0 {
		t.Errorf("expected option cursor to reset to 0 (recommended) on new screen, got %d", m.branchOptionCursor)
	}

	// Enter on the last screen advances to Pipeline.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != wizardStepPipeline {
		t.Errorf("expected step wizardStepPipeline after last Enter, got %v", m.step)
	}
	if m.branchChoices["repo-b"] {
		t.Error("expected repo-b choice to be saved as false (recommended)")
	}
}

func TestWizardBranchScreenEscBacksUpOneRepo(t *testing.T) {
	// On screen 2 (second repo): Esc backs to screen 1 (first repo), NOT
	// to the repo picker. Esc on screen 1 dismisses the branch flow.
	defaults := config.DefaultsConfig{}
	repos := []string{"repo-a", "repo-b"}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.step = wizardStepWhere
	m.showBranchWarning = true
	m.branchInfos = []RepoBranchInfo{
		{Name: "repo-a", CurrentBranch: "feature/xyz", DefaultBranch: "main", IsOffDefault: true},
		{Name: "repo-b", CurrentBranch: "bugfix/foo", DefaultBranch: "master", IsOffDefault: true},
	}
	m.branchChoices = map[string]bool{"repo-a": true, "repo-b": false}
	m.branchScreenIndex = 1

	// Esc backs to screen 0 and remembers the previously-saved choice
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.branchScreenIndex != 0 {
		t.Errorf("expected to back up to screen 0, got %d", m.branchScreenIndex)
	}
	if !m.showBranchWarning {
		t.Error("expected branch screen to still be active")
	}
	if m.branchOptionCursor != 1 {
		t.Errorf("expected option cursor to reflect saved repo-a choice (true), got %d", m.branchOptionCursor)
	}

	// Esc on screen 0 dismisses the branch flow and returns to repo picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.showBranchWarning {
		t.Error("expected branch screen to be dismissed after esc on first screen")
	}
}

func TestWizardBranchScreenSingleRepoEnterAdvances(t *testing.T) {
	defaults := config.DefaultsConfig{}
	repos := []string{"repo-a"}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.step = wizardStepWhere
	m.showBranchWarning = true
	m.branchInfos = []RepoBranchInfo{{Name: "repo-a", CurrentBranch: "feature/xyz", DefaultBranch: "main", IsOffDefault: true}}
	m.branchChoices = map[string]bool{"repo-a": false}
	m.nameInput.SetValue("test-feature")
	m.descInput.SetValue("test description")
	m.selectedRepos["repo-a"] = true

	// One off-default repo: Enter advances directly to Pipeline.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != wizardStepPipeline {
		t.Errorf("expected step wizardStepPipeline, got %v", m.step)
	}
}

func TestWizardBranchWarningBlocksRepoKeys(t *testing.T) {
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.step = wizardStepWhere
	m.showBranchWarning = true
	m.branchInfos = []RepoBranchInfo{{Name: "repo-a", CurrentBranch: "feature/xyz", DefaultBranch: "main", IsOffDefault: true}}

	// Type chars — should be blocked
	for _, ch := range "abc" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	if m.repoInput.Value() != "" {
		t.Errorf("expected empty repoInput, got %q", m.repoInput.Value())
	}
}

// --- Test Group: Branch Warning Back Navigation ---

func TestWizardBranchScreenEscOnFirstDismisses(t *testing.T) {
	// Esc on the first screen dismisses the branch flow entirely and
	// returns to the repo picker. (Replaces the prior shift+tab dismiss
	// path; shift+tab is no longer wired on the branch screen.)
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.step = wizardStepWhere
	m.showBranchWarning = true
	m.branchInfos = []RepoBranchInfo{{Name: "repo-a", CurrentBranch: "feature/xyz", DefaultBranch: "main", IsOffDefault: true}}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.showBranchWarning {
		t.Error("expected showBranchWarning to be false after esc on first screen")
	}
	if m.step != wizardStepWhere {
		t.Errorf("expected step wizardStepWhere (stays), got %v", m.step)
	}
}

func TestWizardBackFromReviewClearsBranchState(t *testing.T) {
	defaults := config.DefaultsConfig{}
	repos := []string{"repo-a"}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.step = wizardStepReview
	m.showBranchWarning = true // was set during advance
	m.branchInfos = []RepoBranchInfo{{Name: "repo-a", CurrentBranch: "feature/xyz", DefaultBranch: "main", IsOffDefault: true}}

	// Esc on Review goes back to Pipeline, then Pipeline->Where clears branch warning
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.step != wizardStepPipeline {
		t.Errorf("expected step wizardStepPipeline, got %v", m.step)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.step != wizardStepWhere {
		t.Errorf("expected step wizardStepWhere, got %v", m.step)
	}
	if m.showBranchWarning {
		t.Error("expected showBranchWarning to be false after going back to Where")
	}
}

// --- Test Group: Branch Warning Result ---

func TestWizardUseCurrentBranchTrue(t *testing.T) {
	repos := []string{"repo-a"}
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.detectBranchesFn = wizardBranchInfoStub(RepoBranchInfo{
		Name:          "repo-a",
		CurrentBranch: "feature/xyz",
		DefaultBranch: "main",
		IsOffDefault:  true,
	})
	m.nameInput.SetValue("test-feature")
	m.descInput.SetValue("test description")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["repo-a"] = true
	m, _ = m.advance() // First Continue -> shows branch screen
	if !m.showBranchWarning {
		t.Fatal("expected branch screen to be shown")
	}
	// Tab toggles the option cursor to "use current branch"
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.branchOptionCursor != 1 {
		t.Fatalf("expected branchOptionCursor=1 after tab, got %d", m.branchOptionCursor)
	}
	// Enter saves the choice and (last repo) advances to Pipeline
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != wizardStepPipeline {
		t.Fatalf("expected step wizardStepPipeline, got %v", m.step)
	}
	if !m.branchChoices["repo-a"] {
		t.Fatalf("expected branchChoices[repo-a]=true after enter, got %v", m.branchChoices)
	}
	// Enter -> Review
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Press G to create
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !m.IsDone() {
		t.Fatal("expected wizard to be done")
	}
	result := m.Result()
	if !result.UseCurrentBranch {
		t.Error("expected UseCurrentBranch (global fallback) to be true")
	}
	if v, ok := result.UseCurrentBranchPerRepo["repo-a"]; !ok || !v {
		t.Errorf("expected UseCurrentBranchPerRepo[repo-a]=true, got value=%v present=%v", v, ok)
	}
}

func TestWizardUseCurrentBranchFalseDefault(t *testing.T) {
	repos := []string{"repo-a"}
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.detectBranchesFn = wizardBranchInfoStub(RepoBranchInfo{
		Name:          "repo-a",
		CurrentBranch: "feature/xyz",
		DefaultBranch: "main",
		IsOffDefault:  true,
	})
	m.nameInput.SetValue("test-feature")
	m.descInput.SetValue("test description")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["repo-a"] = true
	m, _ = m.advance() // First Continue -> shows branch screen with default-branch pre-selected
	// Don't toggle - leave the default choice in place
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Branch -> Pipeline
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Pipeline -> Review
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !m.IsDone() {
		t.Fatal("expected wizard to be done")
	}
	result := m.Result()
	if result.UseCurrentBranch {
		t.Error("expected UseCurrentBranch to be false")
	}
	if v, ok := result.UseCurrentBranchPerRepo["repo-a"]; !ok || v {
		t.Errorf("expected UseCurrentBranchPerRepo[repo-a]=false, got value=%v present=%v", v, ok)
	}
}

// TestWizardUseCurrentBranchPerRepoMixed exercises the multi-repo case where
// the user picks "default branch" for one repo and "current branch" for
// another. The per-repo map must reflect each choice independently; the
// global UseCurrentBranch boolean is true because ANY repo opted into
// current branch.
func TestWizardUseCurrentBranchPerRepoMixed(t *testing.T) {
	repos := []string{"repo-a", "repo-b"}
	defaults := config.DefaultsConfig{}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.detectBranchesFn = func(WizardModel) []RepoBranchInfo {
		return []RepoBranchInfo{
			{Name: "repo-a", CurrentBranch: "feature/xyz", DefaultBranch: "main", IsOffDefault: true},
			{Name: "repo-b", CurrentBranch: "bugfix/foo", DefaultBranch: "master", IsOffDefault: true},
		}
	}
	m.nameInput.SetValue("test-feature")
	m.descInput.SetValue("test description")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["repo-a"] = true
	m.selectedRepos["repo-b"] = true
	m, _ = m.advance() // First Continue -> branch screen
	if !m.showBranchWarning {
		t.Fatal("expected branch screen to be shown")
	}

	// Screen 1 (repo-a): leave at default, Enter to save+advance to screen 2.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.branchScreenIndex != 1 {
		t.Fatalf("expected to advance to screen 1 (repo-b), got %d", m.branchScreenIndex)
	}
	if m.branchChoices["repo-a"] {
		t.Error("expected repo-a choice to remain false")
	}

	// Screen 2 (repo-b): Tab flips to current branch, then Enter saves+advances.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.branchOptionCursor != 1 {
		t.Fatalf("expected option cursor=1 after tab on repo-b, got %d", m.branchOptionCursor)
	}

	// Confirm and finish
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Branch -> Pipeline
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Pipeline -> Review
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !m.IsDone() {
		t.Fatal("expected wizard to be done")
	}
	result := m.Result()
	if !result.UseCurrentBranch {
		t.Error("expected UseCurrentBranch=true (any repo opted in)")
	}
	if v := result.UseCurrentBranchPerRepo["repo-a"]; v {
		t.Errorf("expected repo-a=false in per-repo map, got %v", v)
	}
	if v := result.UseCurrentBranchPerRepo["repo-b"]; !v {
		t.Errorf("expected repo-b=true in per-repo map, got %v", v)
	}
}

// --- Test Group: Branch Warning View ---

func TestWizardBranchWarningView(t *testing.T) {
	defaults := config.DefaultsConfig{}
	repos := []string{"repo-a"}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.step = wizardStepWhere
	m.showBranchWarning = true
	m.branchInfos = []RepoBranchInfo{{Name: "repo-a", CurrentBranch: "feature/xyz", DefaultBranch: "main", IsOffDefault: true}}
	m.branchChoices = map[string]bool{"repo-a": false}
	m.width = 80
	m.height = 24

	view := m.View()
	if !containsString(view, "Branch base") {
		t.Error("expected view title to contain 'Branch base'")
	}
	if !containsString(view, "Start from main") {
		t.Error("expected view to contain 'Start from main' (default-branch option)")
	}
	if !containsString(view, "Start from feature/xyz") {
		t.Error("expected view to contain 'Start from feature/xyz' (current-branch option)")
	}
	if !containsString(view, "Recommended") {
		t.Error("expected view to identify the default branch as recommended")
	}
	if !containsString(view, "Include current branch commits") {
		t.Error("expected view to explain the current-branch option")
	}
	if !containsString(view, "repo-a") {
		t.Error("expected view to contain 'repo-a'")
	}
}

func TestWizardBranchWarningFooter(t *testing.T) {
	defaults := config.DefaultsConfig{}
	repos := []string{"repo-a"}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.step = wizardStepWhere
	m.showBranchWarning = true
	m.branchInfos = []RepoBranchInfo{{Name: "repo-a", CurrentBranch: "feature/xyz", DefaultBranch: "main", IsOffDefault: true}}
	m.width = 80
	m.height = 24

	view := m.View()
	if !containsString(view, "Choose") {
		t.Error("expected footer to contain 'Choose' (enter action label)")
	}
	if !containsString(view, "Switch") {
		t.Error("expected footer to contain 'Switch' (toggle action label)")
	}
}

func TestWizardReviewShowsBranchChoice(t *testing.T) {
	defaults := config.DefaultsConfig{}
	repos := []string{"repo-a"}
	m := NewWizardModel(repos, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test-feature")
	m.descInput.SetValue("test description")
	m.selectedRepos["repo-a"] = true
	m.step = wizardStepReview
	m.branchInfos = []RepoBranchInfo{{Name: "repo-a", CurrentBranch: "feature/xyz", DefaultBranch: "main", IsOffDefault: true}}
	m.branchChoices = map[string]bool{"repo-a": true} // user picked current branch
	m.width = 80
	m.height = 24

	view := m.View()
	if !containsString(view, "current branch") {
		t.Error("expected review to show 'current branch'")
	}

	// Switch to default branch
	m.branchChoices["repo-a"] = false
	view = m.View()
	if !containsString(view, "default branch") {
		t.Error("expected review to show 'default branch'")
	}
}

// --- Test Group: Summary Navigation & Display (Phase 4) ---

func TestWizardSummaryFieldsOrder(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	fields := m.summaryFields()
	expected := []summaryField{
		summaryFieldName,
		summaryFieldRepos,
		summaryFieldRisk,
		summaryFieldModels,
		summaryFieldInquireness,
		summaryFieldCheckpoints,
		summaryFieldExitCriteria,
	}
	if len(fields) != len(expected) {
		t.Fatalf("summaryFields() returned %d fields, want %d", len(fields), len(expected))
	}
	for i, f := range fields {
		if f != expected[i] {
			t.Errorf("summaryFields()[%d] = %d, want %d", i, f, expected[i])
		}
	}
}

func TestWizardSummaryFieldsCount(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	fields := m.summaryFields()
	if len(fields) != 7 {
		t.Fatalf("summaryFields() returned %d fields, want 7", len(fields))
	}
}

func TestWizardSummaryCursorDown(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	if m.summaryCursor != summaryFieldRisk {
		t.Fatalf("expected summaryCursor = summaryFieldRisk, got %d", m.summaryCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.summaryCursor != summaryFieldModels {
		t.Errorf("expected summaryFieldModels after first down, got %d", m.summaryCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.summaryCursor != summaryFieldInquireness {
		t.Errorf("expected summaryFieldInquireness after second down, got %d", m.summaryCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.summaryCursor != summaryFieldCheckpoints {
		t.Errorf("expected summaryFieldCheckpoints after third down, got %d", m.summaryCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.summaryCursor != summaryFieldExitCriteria {
		t.Errorf("expected summaryFieldExitCriteria after fourth down, got %d", m.summaryCursor)
	}

	// One more down should stay at ExitCriteria (no wrap)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.summaryCursor != summaryFieldExitCriteria {
		t.Errorf("expected summaryFieldExitCriteria after fifth down (no wrap), got %d", m.summaryCursor)
	}
}

func TestWizardSummaryCursorUp(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	m.summaryCursor = summaryFieldExitCriteria

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.summaryCursor != summaryFieldCheckpoints {
		t.Errorf("expected summaryFieldCheckpoints, got %d", m.summaryCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.summaryCursor != summaryFieldInquireness {
		t.Errorf("expected summaryFieldInquireness, got %d", m.summaryCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.summaryCursor != summaryFieldModels {
		t.Errorf("expected summaryFieldModels, got %d", m.summaryCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.summaryCursor != summaryFieldRisk {
		t.Errorf("expected summaryFieldRisk, got %d", m.summaryCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.summaryCursor != summaryFieldRepos {
		t.Errorf("expected summaryFieldRepos, got %d", m.summaryCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.summaryCursor != summaryFieldName {
		t.Errorf("expected summaryFieldName, got %d", m.summaryCursor)
	}

	// One more up should stay at Name (no wrap)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.summaryCursor != summaryFieldName {
		t.Errorf("expected summaryFieldName after extra up (no wrap), got %d", m.summaryCursor)
	}
}

func TestWizardSummaryCursorJK(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	initial := m.summaryCursor // summaryFieldRisk

	// j moves down
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.summaryCursor == initial {
		t.Error("expected j to move cursor down")
	}
	afterJ := m.summaryCursor

	// k moves back up
	m, _ = m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if m.summaryCursor != initial {
		t.Errorf("expected k to move cursor back to %d, got %d", initial, m.summaryCursor)
	}
	_ = afterJ
}

func TestWizardSummaryCursorStopsAtExitCriteria(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	m.summaryCursor = summaryFieldExitCriteria

	// Down should stay at ExitCriteria (last field)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.summaryCursor != summaryFieldExitCriteria {
		t.Errorf("expected summaryFieldExitCriteria (last field), got %d", m.summaryCursor)
	}
}

func TestWizardSummaryEnterOnName(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	m.summaryCursor = summaryFieldName
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.step != wizardStepWhat {
		t.Errorf("expected wizardStepWhat after enter on Name, got %d", m.step)
	}
	if m.IsDone() {
		t.Error("expected wizard to not be done")
	}
	if m.IsCancelled() {
		t.Error("expected wizard to not be cancelled")
	}
}

func TestWizardSummaryEnterOnRepos(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	m.summaryCursor = summaryFieldRepos
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.step != wizardStepWhere {
		t.Errorf("expected wizardStepWhere after enter on Repos, got %d", m.step)
	}
	if m.showBranchWarning {
		t.Error("expected showBranchWarning to be false after navigating back to Where")
	}
}

func TestWizardSummaryEnterOnRiskStartsEditing(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	// Navigate to Risk (cursor starts at Pipeline)
	m.summaryCursor = summaryFieldRisk

	initialRisk := m.riskCursor
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.step != wizardStepReview {
		t.Errorf("expected to stay on wizardStepReview, got %d", m.step)
	}
	if m.IsDone() {
		t.Error("expected wizard to not be done")
	}
	if !m.summaryEditing {
		t.Error("expected Risk to enter summaryEditing")
	}
	if m.riskCursor != initialRisk {
		t.Error("Enter on Risk should not change riskCursor")
	}
}

func TestWizardSummaryGCreatesFromAnyCursor(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	m.summaryCursor = summaryFieldName // top of list
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})

	if !m.IsDone() {
		t.Error("expected wizard to be done after G on Review step regardless of cursor position")
	}
}

func TestWizardSummaryEscGoesBack(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	m.summaryCursor = summaryFieldModels // middle of list
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.step != wizardStepPipeline {
		t.Errorf("expected wizardStepPipeline after Esc from Review, got %d", m.step)
	}
	if m.IsCancelled() {
		t.Error("expected wizard to not be cancelled (Esc on Review goes back)")
	}
}

func TestWizardSummaryCursorIndicatorInView(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewWizardModel([]string{"repo-a"}, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	m.summaryCursor = summaryFieldRisk
	m.width = 120
	m.height = 40

	view := m.View()
	if !containsString(view, "\u25b8") {
		t.Error("expected cursor indicator '\u25b8' in Review view")
	}
}

func TestWizardSummaryAutoDetectedLabelInView(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	// "auth" is a high-risk keyword that triggers auto-detection
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("auth-refactor")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	m.width = 120
	m.height = 40

	view := m.View()
	// Only "(auto-detected)" should appear (for Risk). Other provenance labels removed.
	if !containsString(view, "(auto-detected)") {
		t.Error("expected '(auto-detected)' provenance label for risk with keyword match")
	}
	// "(from config)" and "(default)" should NOT appear
	if containsString(view, "(from config)") {
		t.Error("'(from config)' provenance labels should be removed")
	}
	if containsString(view, "(default)") {
		t.Error("'(default)' provenance labels should be removed")
	}
}

func TestWizardSummaryRiskNoAutoDetectedWhenDefault(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	m.width = 120
	m.height = 40

	view := m.View()
	// "test" has no risk keywords — no provenance labels should show
	if containsString(view, "(auto-detected)") {
		t.Error("expected no '(auto-detected)' label when risk is default medium")
	}
	if containsString(view, "(default)") {
		t.Error("'(default)' provenance label should be removed")
	}
}

func TestWizardSummaryDividerInView(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	m.width = 120
	m.height = 40

	view := m.View()
	if !containsString(view, "\u2500\u2500\u2500\u2500\u2500") {
		t.Error("expected divider '\u2500\u2500\u2500\u2500\u2500' in Review view")
	}
}

func TestWizardSummaryBackToStepOnePreservesState(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("my-feature")
	m.descInput.SetValue("my description")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	if m.step != wizardStepReview {
		t.Fatalf("expected wizardStepReview, got %d", m.step)
	}

	// Navigate back to What via Enter on Name field
	m.summaryCursor = summaryFieldName
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.step != wizardStepWhat {
		t.Errorf("expected wizardStepWhat, got %d", m.step)
	}
	if m.nameInput.Value() != "my-feature" {
		t.Errorf("name input = %q, want %q", m.nameInput.Value(), "my-feature")
	}

	// Advance back to Review
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	if m.step != wizardStepReview {
		t.Errorf("expected wizardStepReview after re-advance, got %d", m.step)
	}
}

func TestWizardSummaryBackToStepTwoPreservesState(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewWizardModel([]string{"repo-a", "repo-b"}, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["repo-a"] = true
	m.selectedRepos["repo-b"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	if m.step != wizardStepReview {
		t.Fatalf("expected wizardStepReview, got %d", m.step)
	}

	// Navigate back to Where via Enter on Repos field
	m.summaryCursor = summaryFieldRepos
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.step != wizardStepWhere {
		t.Errorf("expected wizardStepWhere, got %d", m.step)
	}
	if !m.selectedRepos["repo-a"] || !m.selectedRepos["repo-b"] {
		t.Error("expected selectedRepos to be preserved after going back to Where")
	}

	// Advance back through Pipeline to Review
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	if m.step != wizardStepReview {
		t.Errorf("expected wizardStepReview after re-advance, got %d", m.step)
	}
	if m.summaryCursor != summaryFieldRisk {
		t.Errorf("expected summaryCursor = summaryFieldRisk on re-entry, got %d", m.summaryCursor)
	}
}

func TestWizardSummaryFooterShowsNavigation(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	m.width = 120
	m.height = 40

	view := m.View()
	if !containsString(view, "\u2191\u2193") {
		t.Error("expected '\u2191\u2193' navigation hint in Review footer")
	}
}

// --- Test Group: Simple Inline Editing (Phase 5) ---

func TestWizardSummaryRiskPillNavigation(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	// Navigate to Risk (cursor starts at Pipeline now)
	m.summaryCursor = summaryFieldRisk
	if m.riskCursor != 1 {
		t.Fatalf("expected riskCursor=1 (medium), got %d", m.riskCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.summaryEditing {
		t.Fatal("expected summaryEditing=true after Enter on Risk")
	}

	// Right arrow changes to high (2)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.riskCursor != 2 {
		t.Errorf("expected riskCursor=2 (high), got %d", m.riskCursor)
	}

	// Right arrow at max: stays at 2
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.riskCursor != 2 {
		t.Errorf("expected riskCursor=2 (clamped), got %d", m.riskCursor)
	}

	// Left arrow: moves back to medium (1)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.riskCursor != 1 {
		t.Errorf("expected riskCursor=1 (medium), got %d", m.riskCursor)
	}

	// Left arrow: moves to low (0)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.riskCursor != 0 {
		t.Errorf("expected riskCursor=0 (low), got %d", m.riskCursor)
	}

	// Left arrow at min: stays at 0
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.riskCursor != 0 {
		t.Errorf("expected riskCursor=0 (clamped), got %d", m.riskCursor)
	}

	if !m.riskManuallySet {
		t.Error("expected riskManuallySet=true")
	}
	if m.riskAutoDetected {
		t.Error("expected riskAutoDetected=false after manual set")
	}
	if m.step != wizardStepReview {
		t.Error("should still be on Review step")
	}
	if m.IsDone() {
		t.Error("should not be done")
	}
}

func TestWizardSummaryRiskLeftRightRequireEditMode(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	// Navigate to Risk (cursor starts at Pipeline now)
	m.summaryCursor = summaryFieldRisk

	// Right on Risk does nothing until editing is opened with Enter.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.summaryEditing {
		t.Error("expected summaryEditing=false before Enter on Risk")
	}
	if m.riskCursor != 1 {
		t.Errorf("expected riskCursor to remain 1, got %d", m.riskCursor)
	}
}

func TestWizardSummaryInquirenessPillNavigation(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldInquireness
	if m.inquirenessCursor != 1 {
		t.Fatalf("expected inquirenessCursor=1 (medium), got %d", m.inquirenessCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.summaryEditing {
		t.Fatal("expected summaryEditing=true after Enter on Inquiry")
	}

	// Right arrow changes to high (2)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.inquirenessCursor != 2 {
		t.Errorf("expected inquirenessCursor=2 (high), got %d", m.inquirenessCursor)
	}

	// Left arrow twice: moves to none (0)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.inquirenessCursor != 0 {
		t.Errorf("expected inquirenessCursor=0 (none), got %d", m.inquirenessCursor)
	}

	if !m.inquirenessManuallySet {
		t.Error("expected inquirenessManuallySet=true")
	}
}

func TestWizardSeedsRememberedPreferencesForCurrentPipeline(t *testing.T) {
	defaults := config.NewDefault().Defaults
	defaults.PipelinePreferences = map[string]config.PipelinePreference{
		"large": {
			Models: config.ModelConfig{
				Research:       "claude:haiku",
				Planning:       "claude:haiku",
				Implementation: "claude:sonnet",
				Review:         "codex:gpt-5.4-mini",
				KBBuild:        "claude:opus",
			},
			Inquireness: "high",
		},
	}

	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)

	if got := m.models.Implementation; got != "claude:sonnet" {
		t.Errorf("implementation model = %q, want %q", got, "claude:sonnet")
	}
	if got := m.models.Review; got != "codex:gpt-5.4-mini" {
		t.Errorf("review model = %q, want %q", got, "codex:gpt-5.4-mini")
	}
	if got := m.inquirenessOptions[m.inquirenessCursor]; got != "high" {
		t.Errorf("inquireness = %q, want %q", got, "high")
	}
}

func TestWizardPipelineSwitchRestoresPerProfilePreferences(t *testing.T) {
	defaults := config.NewDefault().Defaults
	defaults.PipelinePreferences = map[string]config.PipelinePreference{
		"medium": {
			Models: config.ModelConfig{
				Implementation: "medium-impl",
				Review:         "medium-review",
			},
			Inquireness: "none",
		},
		"large": {
			Models: config.ModelConfig{
				Implementation: "large-impl",
				Review:         "large-review",
			},
			Inquireness: "medium",
		},
	}

	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.step = wizardStepPipeline

	m.models.Implementation = "large-impl-edited"
	m.inquirenessCursor = 2 // high

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.models.Implementation; got != "medium-impl" {
		t.Fatalf("after switch to medium, implementation = %q, want %q", got, "medium-impl")
	}
	if got := m.inquirenessOptions[m.inquirenessCursor]; got != "none" {
		t.Fatalf("after switch to medium, inquireness = %q, want %q", got, "none")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.models.Implementation; got != "large-impl-edited" {
		t.Errorf("after switch back to large, implementation = %q, want %q", got, "large-impl-edited")
	}
	if got := m.inquirenessOptions[m.inquirenessCursor]; got != "high" {
		t.Errorf("after switch back to large, inquireness = %q, want %q", got, "high")
	}
}

func TestWizardClampsUnavailableRememberedModelsToEligibleOptions(t *testing.T) {
	defaults := config.NewDefault().Defaults
	defaults.PipelinePreferences = map[string]config.PipelinePreference{
		"large": {
			Models: config.ModelConfig{
				Research:       "missing-research",
				Planning:       "missing-planning",
				Implementation: "missing-implementation",
				Review:         "missing-review",
				KBBuild:        "missing-kb",
			},
			Inquireness: "medium",
		},
	}

	providerModels := map[string][]string{
		"claude": {"opus", "sonnet"},
		"codex":  {"gpt-5.4"},
	}
	providerOrder := []string{"claude", "codex"}
	phaseModels := map[string]map[string][]string{
		"Research": {
			"claude": {"opus"},
		},
		"Planning": {
			"claude": {"opus"},
		},
		"Implementation": {
			"claude": {"sonnet"},
		},
		"Review": {
			"codex": {"gpt-5.4"},
		},
		"KB Build": {
			"claude": {"opus"},
		},
	}

	m := NewWizardModel(nil, nil, nil, defaults, "", providerModels, providerOrder, nil, phaseModels, nil, nil)

	if got := m.models.Research; got != "opus" {
		t.Errorf("research model = %q, want %q", got, "opus")
	}
	if got := m.models.Planning; got != "opus" {
		t.Errorf("planning model = %q, want %q", got, "opus")
	}
	if got := m.models.Implementation; got != "sonnet" {
		t.Errorf("implementation model = %q, want %q", got, "sonnet")
	}
	if got := m.models.Review; got != "gpt-5.4" {
		t.Errorf("review model = %q, want %q", got, "gpt-5.4")
	}
	if got := m.models.KBBuild; got != "opus" {
		t.Errorf("KB build model = %q, want %q", got, "opus")
	}
}

func TestWizardSummaryEditingBlocksNavigation(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	// Navigate to Models, Enter to start editing
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.summaryEditing {
		t.Fatal("expected editing mode on Models")
	}

	// G should be blocked during editing
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if m.IsDone() {
		t.Error("G should be blocked during editing")
	}
}

func TestWizardSummaryEditingBlocksG(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	// Enter edit mode on Models (Risk/Inquiry no longer use summaryEditing)
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// G should not create feature
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if m.IsDone() {
		t.Error("G should be blocked during editing")
	}
}

func TestWizardSummaryEditedValuesInResult(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	// Edit Risk: navigate to Risk field first, then Left arrow changes value
	m.summaryCursor = summaryFieldRisk
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft}) // medium → low
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Edit Inquireness: navigate to field, Left to change
	m.summaryCursor = summaryFieldInquireness
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft}) // medium → none
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Press G to create
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !m.IsDone() {
		t.Fatal("expected wizard to be done after G")
	}
	result := m.Result()
	if result.RiskLevel != "low" {
		t.Errorf("expected RiskLevel='low', got %q", result.RiskLevel)
	}
	if result.Inquireness != "none" {
		t.Errorf("expected Inquireness='none', got %q", result.Inquireness)
	}
}

func TestWizardSummaryRiskProvenanceClearedAfterEdit(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("auth migration")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.width = 120
	m.height = 40

	// Navigate to Risk field (cursor starts at Pipeline now)
	m.summaryCursor = summaryFieldRisk

	// Open editor, then change risk and confirm.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.riskManuallySet {
		t.Error("expected riskManuallySet=true after manual edit")
	}
	if m.riskAutoDetected {
		t.Error("expected riskAutoDetected=false after manual edit")
	}
}

func TestWizardSummaryInquirenessProvenanceClearedAfterEdit(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.width = 120
	m.height = 40

	// Open editor, then change inquiry and confirm.
	m.summaryCursor = summaryFieldInquireness
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.inquirenessManuallySet {
		t.Error("expected inquirenessManuallySet=true after edit")
	}
}

func TestWizardSummaryGAfterEditing(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	// Edit Models: Enter to start, Enter to exit
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// G should work after exiting editing
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !m.IsDone() {
		t.Error("expected G to work after editing mode is exited")
	}
}

func TestWizardSummaryRiskFocusShowsChangeHint(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.width = 120
	m.height = 40

	// Risk is focused by default, but not yet editing.
	view := m.View()
	if !containsString(view, "Edit") {
		t.Error("expected 'Edit' hint in footer when Risk is focused but not editing")
	}

	// Navigate to Models — footer remains edit-oriented.
	m.summaryCursor = summaryFieldModels
	view = m.View()
	if !containsString(view, "Edit") {
		t.Error("expected 'Edit' hint on Models when not editing")
	}
}

func TestWizardSummaryEditingFooter(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.width = 120
	m.height = 40

	// Risk is focused — footer shows Edit hint until Enter opens editor.
	view := m.View()
	if !containsString(view, "Edit") {
		t.Error("expected 'Edit' in footer when Risk is focused")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	view = m.View()
	if !containsString(view, "Change") {
		t.Error("expected 'Change' in footer when Risk editor is open")
	}

	// Exit risk editor, navigate to Models and enter editing.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	view = m.View()
	for _, needle := range []string{"Panel", "Filter"} {
		if !containsString(view, needle) {
			t.Errorf("expected %q in footer during Models editing", needle)
		}
	}

	// Exit editing
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	view = m.View()
	if !containsString(view, "Navigate") {
		t.Error("expected 'Navigate' in footer after exiting editing")
	}
}

// --- Test Group: Complex Inline Editing (Phase 6) ---

func TestWizardSummaryModelsEnterExpands(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance() // What → Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where → Pipeline
	m, _ = m.advance() // Pipeline → Review

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.summaryEditing {
		t.Error("expected summaryEditing=true after Enter on Models")
	}
	if m.modelCursor != 0 {
		t.Errorf("expected modelCursor=0, got %d", m.modelCursor)
	}
	if m.step != wizardStepReview {
		t.Errorf("expected wizardStepReview, got %d", m.step)
	}
	if m.IsDone() {
		t.Error("expected wizard to not be done")
	}
}

func TestWizardSummaryModelsUpDownNavigation(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.modelCursor != 0 {
		t.Fatalf("expected modelCursor=0, got %d", m.modelCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modelCursor != 1 {
		t.Errorf("expected modelCursor=1 after Down, got %d", m.modelCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modelCursor != 2 {
		t.Errorf("expected modelCursor=2 after Down, got %d", m.modelCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modelCursor != 3 {
		t.Errorf("expected modelCursor=3 after Down, got %d", m.modelCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modelCursor != 4 {
		t.Errorf("expected modelCursor=4 after Down, got %d", m.modelCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modelCursor != 5 {
		t.Errorf("expected modelCursor=5 after Down, got %d", m.modelCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modelCursor != 5 {
		t.Errorf("expected modelCursor=5 (clamped) after Down, got %d", m.modelCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.modelCursor != 4 {
		t.Errorf("expected modelCursor=4 after Up, got %d", m.modelCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.modelCursor != 3 {
		t.Errorf("expected modelCursor=3 after Up, got %d", m.modelCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.modelCursor != 2 {
		t.Errorf("expected modelCursor=2 after Up, got %d", m.modelCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.modelCursor != 1 {
		t.Errorf("expected modelCursor=1 after Up, got %d", m.modelCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.modelCursor != 0 {
		t.Errorf("expected modelCursor=0 after Up, got %d", m.modelCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.modelCursor != 0 {
		t.Errorf("expected modelCursor=0 (clamped) after Up, got %d", m.modelCursor)
	}
}

func TestWizardSummaryModelsTabCycles(t *testing.T) {
	defaults := config.DefaultsConfig{Models: config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"}}
	m := NewWizardModel(nil, nil, nil, defaults, "", map[string][]string{"test": {"opus", "sonnet"}}, []string{"test"}, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // Clarify -> Research

	origModel := m.models.Research
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})

	if got := m.configEditor.activeModelCell; got != modelCellAgent {
		t.Errorf("after Tab activeModelCell = %v, want modelCellAgent", got)
	}
	if m.models.Research != origModel {
		t.Errorf("Tab changed Research model: got %q, want %q", m.models.Research, origModel)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.configEditor.activeModelCell; got != modelCellModel {
		t.Errorf("after second Tab activeModelCell = %v, want modelCellModel", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if got := m.configEditor.activeModelCell; got != modelCellAgent {
		t.Errorf("after Shift+Tab activeModelCell = %v, want modelCellAgent", got)
	}
}

func TestWizardSummaryModelsVerticalSelectionCycles(t *testing.T) {
	defaults := config.DefaultsConfig{Models: config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"}}
	m := NewWizardModel(nil, nil, nil, defaults, "", map[string][]string{"test": {"opus", "sonnet"}}, []string{"test"}, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // Clarify -> Research

	origModel := m.models.Research
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.configEditor.activeModelCell; got != modelCellModel {
		t.Fatalf("second Right activeModelCell = %v, want modelCellModel", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.models.Research == origModel {
		t.Error("expected Research model to change after Down on Model panel")
	}

	afterDown := m.models.Research
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.models.Research == afterDown {
		t.Error("expected Research model to change after Up on Model panel")
	}
	if m.models.Research != origModel {
		t.Errorf("expected Research model to cycle back to %q, got %q", origModel, m.models.Research)
	}
}

func TestWizardSummaryModelsEnterCollapses(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.summaryEditing {
		t.Fatal("expected summaryEditing=true after Enter")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.summaryEditing {
		t.Error("expected summaryEditing=false after second Enter (collapse)")
	}
	if m.step != wizardStepReview {
		t.Errorf("expected wizardStepReview, got %d", m.step)
	}
}

func TestWizardSummaryModelsEscCollapses(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Change a model.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	changedModel := m.models.Research

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.summaryEditing {
		t.Error("expected summaryEditing=false after Esc")
	}
	if m.models.Research != changedModel {
		t.Error("expected changed model to be preserved after Esc")
	}
}

func TestWizardSummaryModelsSubRowNavAndCycle(t *testing.T) {
	defaults := config.DefaultsConfig{Models: config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"}}
	m := NewWizardModel(nil, nil, nil, defaults, "", map[string][]string{"test": {"opus", "sonnet"}}, []string{"test"}, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Navigate down to Implementation (index 3)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modelCursor != 3 {
		t.Fatalf("expected modelCursor=3, got %d", m.modelCursor)
	}

	origImpl := m.models.Implementation
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.configEditor.activeModelCell; got != modelCellModel {
		t.Fatalf("second Right activeModelCell = %v, want modelCellModel", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.models.Implementation == origImpl {
		t.Error("expected Implementation model to change after Down on Model panel")
	}
	if !m.modelsManuallySet {
		t.Error("expected modelsManuallySet=true")
	}
}

func TestWizardSummaryCheckpointsEnterExpands(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.summaryEditing {
		t.Error("expected summaryEditing=true after Enter on Checkpoints")
	}
	if m.checkpointsCursor != 0 {
		t.Errorf("expected checkpointsCursor=0, got %d", m.checkpointsCursor)
	}
	if m.step != wizardStepReview {
		t.Errorf("expected wizardStepReview, got %d", m.step)
	}
}

func TestWizardSummaryCheckpointsUpDownNavigation(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.NewDefault().Defaults, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m.pipelineCursor = 2
	m.applyPipelineDefaults()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.checkpointsCursor != 0 {
		t.Fatalf("expected checkpointsCursor=0, got %d", m.checkpointsCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.checkpointsCursor != 1 {
		t.Errorf("expected 1, got %d", m.checkpointsCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.checkpointsCursor != 2 {
		t.Errorf("expected 2, got %d", m.checkpointsCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.checkpointsCursor != 3 {
		t.Errorf("expected 3, got %d", m.checkpointsCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.checkpointsCursor != 4 {
		t.Errorf("expected 4, got %d", m.checkpointsCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.checkpointsCursor != 5 {
		t.Errorf("expected 5, got %d", m.checkpointsCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.checkpointsCursor != 5 {
		t.Errorf("expected 5 (clamped), got %d", m.checkpointsCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.checkpointsCursor != 4 {
		t.Errorf("expected 4, got %d", m.checkpointsCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.checkpointsCursor != 3 {
		t.Errorf("expected 3, got %d", m.checkpointsCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.checkpointsCursor != 2 {
		t.Errorf("expected 2, got %d", m.checkpointsCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.checkpointsCursor != 1 {
		t.Errorf("expected 1, got %d", m.checkpointsCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.checkpointsCursor != 0 {
		t.Errorf("expected 0, got %d", m.checkpointsCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.checkpointsCursor != 0 {
		t.Errorf("expected 0 (clamped), got %d", m.checkpointsCursor)
	}
}

func TestWizardSummaryCheckpointsSpaceToggles(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m.pipelineCursor = 2
	m.applyPipelineDefaults()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	orig := m.checkpoints[checkpointInquiryReview]
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if m.checkpoints[checkpointInquiryReview] == orig {
		t.Error("expected InquiryReview checkpoint to toggle after Space")
	}
	if !m.checkpointsManuallySet {
		t.Error("expected checkpointsManuallySet=true after Space")
	}

	// Toggle back
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if m.checkpoints[checkpointInquiryReview] != orig {
		t.Error("expected InquiryReview checkpoint to toggle back after second Space")
	}
}

func TestWizardSummaryCheckpointsTabToggles(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m.pipelineCursor = 2
	m.applyPipelineDefaults()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	orig := m.checkpoints[checkpointInquiryReview]
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.checkpoints[checkpointInquiryReview] == orig {
		t.Error("expected InquiryReview checkpoint to toggle after Tab")
	}
	if !m.checkpointsManuallySet {
		t.Error("expected checkpointsManuallySet=true after Tab")
	}
}

func TestWizardSummaryCheckpointsEnterCollapses(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m.pipelineCursor = 2
	m.applyPipelineDefaults()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Toggle
	orig := m.checkpoints[checkpointInquiryReview]
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})

	// Collapse
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.summaryEditing {
		t.Error("expected summaryEditing=false after Enter (collapse)")
	}
	if m.checkpoints[checkpointInquiryReview] == orig {
		t.Error("expected toggled value to be preserved after collapse")
	}
}

func TestWizardSummaryCheckpointsEscCollapses(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Toggle
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	toggled := m.checkpoints[checkpointInquiryReview]

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.summaryEditing {
		t.Error("expected summaryEditing=false after Esc")
	}
	if m.checkpoints[checkpointInquiryReview] != toggled {
		t.Error("expected toggled value to be preserved after Esc")
	}
}

func TestWizardSummaryExitCriteriaEnterOpensTextarea(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldExitCriteria
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.summaryEditing {
		t.Error("expected summaryEditing=true after Enter on ExitCriteria")
	}
	if !m.exitInput.Focused() {
		t.Error("expected exitInput to be focused")
	}
	if m.exitInput.Value() != m.exitCriteria {
		t.Errorf("expected exitInput pre-populated with exitCriteria=%q, got %q", m.exitCriteria, m.exitInput.Value())
	}
	if m.exitCriteriaOriginal != m.exitCriteria {
		t.Errorf("expected exitCriteriaOriginal=%q, got %q", m.exitCriteria, m.exitCriteriaOriginal)
	}
}

func TestWizardSummaryExitCriteriaTypingWorks(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldExitCriteria
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	for _, r := range " extra" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	if !containsString(m.exitInput.Value(), "extra") {
		t.Errorf("expected exitInput to contain 'extra', got %q", m.exitInput.Value())
	}
}

func TestWizardSummaryExitCriteriaEnterConfirms(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldExitCriteria
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	for _, r := range " edited" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	expected := strings.TrimSpace(m.exitInput.Value())

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.summaryEditing {
		t.Error("expected summaryEditing=false after Enter (confirm)")
	}
	if m.exitCriteria != expected {
		t.Errorf("expected exitCriteria=%q, got %q", expected, m.exitCriteria)
	}
	if !m.exitCriteriaManuallySet {
		t.Error("expected exitCriteriaManuallySet=true")
	}
	if m.exitInput.Focused() {
		t.Error("expected exitInput to not be focused after confirm")
	}
}

func TestWizardSummaryExitCriteriaEscCancels(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldExitCriteria
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	original := m.exitCriteria

	for _, r := range " changed" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.summaryEditing {
		t.Error("expected summaryEditing=false after Esc")
	}
	if m.exitCriteria != original {
		t.Errorf("expected exitCriteria reverted to %q, got %q", original, m.exitCriteria)
	}
	if m.exitCriteriaManuallySet {
		t.Error("expected exitCriteriaManuallySet=false after Esc cancel")
	}
	if m.exitInput.Focused() {
		t.Error("expected exitInput to not be focused after Esc")
	}
}

func TestWizardSummaryModelsEditingBlocksG(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if m.IsDone() {
		t.Error("expected G to be blocked while editing Models")
	}
	if m.summaryCursor != summaryFieldModels {
		t.Errorf("expected cursor to remain on Models, got %d", m.summaryCursor)
	}
}

func TestWizardSummaryModelsProvenanceClearedAfterEdit(t *testing.T) {
	defaults := config.DefaultsConfig{Models: config.ModelConfig{Research: "opus"}}
	m := NewWizardModel(nil, nil, nil, defaults, "", map[string][]string{"test": {"opus", "sonnet"}}, []string{"test"}, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	if m.modelsManuallySet {
		t.Fatal("expected modelsManuallySet=false initially")
	}

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // collapse

	if !m.modelsManuallySet {
		t.Error("expected modelsManuallySet=true after editing")
	}
}

func TestWizardSummaryCheckpointsProvenanceClearedAfterEdit(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "}) // toggle
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})   // collapse

	if !m.checkpointsManuallySet {
		t.Error("expected checkpointsManuallySet=true after editing")
	}
}

func TestWizardSummaryExitCriteriaProvenanceClearedAfterEdit(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldExitCriteria
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm

	if !m.exitCriteriaManuallySet {
		t.Error("expected exitCriteriaManuallySet=true after editing")
	}
}

func TestWizardSummaryModelsExpandedViewShowsSubRows(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := m.View()
	if !containsString(view, "Research") {
		t.Error("expected expanded Models view to contain 'Research'")
	}
	if !containsString(view, "Planning") {
		t.Error("expected expanded Models view to contain 'Planning'")
	}
	if !containsString(view, "Implementation") {
		t.Error("expected expanded Models view to contain 'Implementation'")
	}
	if !containsString(view, "Review") {
		t.Error("expected expanded Models view to contain 'Review'")
	}
	if !containsString(view, "▸") {
		t.Error("expected expanded Models view to contain '▸' indicator")
	}
}

func TestWizardSummaryModelsExpandedViewShowsSplitPaneTitles(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "claude:opus",
			Planning:       "claude:opus",
			Implementation: "claude:opus",
			Review:         "codex:gpt-5.4",
			KBBuild:        "claude:sonnet",
		},
	}
	providerModels := map[string][]string{
		"claude": {"opus", "sonnet"},
		"codex":  {"gpt-5.4"},
	}
	providerOrder := []string{"claude", "codex"}

	m := navigateToModelEditing(t, defaults, providerModels, providerOrder, map[string]string{"Research": "claude:opus"})
	m.width = 140
	m.height = 40

	view := m.View()
	for _, needle := range []string{"Model Selection", "Phases", "Agents", "Models for", "Research", "opus", "codex"} {
		if !containsString(view, needle) {
			t.Errorf("expected expanded Models view to contain %q", needle)
		}
	}
	for _, forbidden := range []string{"Selection for Research", "Choices for Research", "Details agent"} {
		if containsString(view, forbidden) {
			t.Errorf("expected expanded Models view to omit old model cascade copy %q", forbidden)
		}
	}
}

func TestWizardSummaryRiskAndInquiryUseEditorBoxes(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()
	m.width = 120
	m.height = 40

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	view := m.View()
	if !containsString(view, "Current:") {
		t.Error("expected focused risk view to contain 'Current:'")
	}
	if !containsString(view, "Options") {
		t.Error("expected focused risk view to contain 'Options'")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	m.summaryCursor = summaryFieldInquireness
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	view = m.View()
	if !containsString(view, "Inquiry") {
		t.Error("expected focused inquiry view to contain 'Inquiry'")
	}
	if !containsString(view, "Harness surfaces key planning questions") {
		t.Error("expected focused inquiry editor to contain the current harness-surfacing description")
	}
}

func TestWizardSummaryCheckpointsExpandedViewShowsToggleRows(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m.pipelineCursor = 2
	m.applyPipelineDefaults()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := m.View()
	if !containsString(view, "Inquiry Review") {
		t.Error("expected expanded Checkpoints view to contain 'Inquiry Review'")
	}
	if !containsString(view, "Manual Publish") {
		t.Error("expected expanded Checkpoints view to contain 'Manual Publish'")
	}
}

func TestWizardSummaryExitCriteriaExpandedViewShowsTextarea(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldExitCriteria
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := m.View()
	if !containsString(view, "done") {
		t.Error("expected expanded ExitCriteria view to contain 'done'")
	}
	if !containsString(view, "cancel") {
		t.Error("expected expanded ExitCriteria view to contain 'cancel'")
	}
}

func TestWizardSummaryEditedModelsInResult(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", map[string][]string{"test": {"opus", "sonnet", "haiku"}}, []string{"test"}, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	// Research starts at "opus" (first option). Move to the Model panel and
	// cycle it to "sonnet".
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})  // Clarify -> Research
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // focus Agent panel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // focus Model panel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})  // cycle Research: opus -> sonnet
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // return to Phases panel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // collapse

	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !m.IsDone() {
		t.Fatal("expected wizard to be done after G")
	}
	r := m.Result()
	if r == nil {
		t.Fatal("expected non-nil Result")
	}
	if r.Models.Research != "sonnet" {
		t.Errorf("expected Research model = %q after cycling, got %q", "sonnet", r.Models.Research)
	}
	// Planning and Implementation should remain at the default first option
	if r.Models.Planning != "opus" {
		t.Errorf("expected Planning model = %q (unchanged), got %q", "opus", r.Models.Planning)
	}
	if r.Models.Implementation != "opus" {
		t.Errorf("expected Implementation model = %q (unchanged), got %q", "opus", r.Models.Implementation)
	}
	// Review uses allModels list; default is first option "opus"
	if r.Models.Review != "opus" {
		t.Errorf("expected Review model = %q (unchanged), got %q", "opus", r.Models.Review)
	}
}

func TestWizardSummaryEditedCheckpointsInResult(t *testing.T) {
	// applyPipelineDefaults seeds the selected moonshot profile before review.
	m := NewWizardModel(nil, nil, nil, config.NewDefault().Defaults, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m.pipelineCursor = 2
	m.applyPipelineDefaults()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Cursor starts at row 0 (InquiryReview). Toggle it on.
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "}) // toggle InquiryReview
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})   // collapse

	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !m.IsDone() {
		t.Fatal("expected wizard to be done after G")
	}
	r := m.Result()
	if r == nil {
		t.Fatal("expected non-nil Result")
	}
	if r.Checkpoints.InquiryReview {
		t.Error("expected InquiryReview to be false after toggling the first visible moonshot gate")
	}
	if !r.Checkpoints.ResearchReview {
		t.Error("expected ResearchReview to remain true (moonshot pipeline default)")
	}
	if !r.Checkpoints.RoadmapReview {
		t.Error("expected RoadmapReview to remain true (moonshot pipeline default)")
	}
	if !r.Checkpoints.PhasePlanReview {
		t.Error("expected PhasePlanReview to remain true (moonshot pipeline default)")
	}
	if !r.Checkpoints.DesignReview {
		t.Error("expected DesignReview to remain true (moonshot pipeline default)")
	}
	if !r.Checkpoints.ManualPublish {
		t.Error("expected ManualPublish to remain true (moonshot pipeline default)")
	}
}

func TestWizardSummaryEditedExitCriteriaInResult(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldExitCriteria
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, r := range " custom" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm

	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !m.IsDone() {
		t.Fatal("expected wizard to be done after G")
	}
	r := m.Result()
	if r == nil {
		t.Fatal("expected non-nil Result")
	}
	if !containsString(r.ExitCriteria, "custom") {
		t.Errorf("expected ExitCriteria to contain 'custom', got %q", r.ExitCriteria)
	}
}

func TestWizardSummaryFullIntegration(t *testing.T) {
	defaults := config.DefaultsConfig{Models: config.ModelConfig{Research: "opus"}}
	m := NewWizardModel(nil, nil, nil, defaults, "", map[string][]string{"test": {"opus", "sonnet"}}, []string{"test"}, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	// 1. Edit Risk (navigate there first — cursor starts at Pipeline)
	m.summaryCursor = summaryFieldRisk
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // change risk value
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// 2. Edit Models
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // focus Agent panel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // focus Model panel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})  // change model
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // return to Phases panel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // collapse

	// 3. Edit Checkpoints
	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "}) // toggle
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})   // collapse

	// 4. Create
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !m.IsDone() {
		t.Fatal("expected wizard to be done after G")
	}
	r := m.Result()
	if r == nil {
		t.Fatal("expected non-nil Result")
	}
	if !m.riskManuallySet {
		t.Error("expected riskManuallySet=true")
	}
	if !m.modelsManuallySet {
		t.Error("expected modelsManuallySet=true")
	}
	if !m.checkpointsManuallySet {
		t.Error("expected checkpointsManuallySet=true")
	}
}

func TestWizardSummaryModelsFooterShowsHint(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := m.View()
	for _, needle := range []string{"Panel", "Select", "Filter"} {
		if !containsString(view, needle) {
			t.Errorf("expected footer to contain %q hint when editing Models", needle)
		}
	}
}

func TestWizardSummaryCheckpointsFooterShowsHint(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := m.View()
	if !containsString(view, "Toggle") {
		t.Error("expected footer to contain 'Toggle' hint when editing Checkpoints")
	}
}

func TestWizardSummaryExitCriteriaFooterShowsHint(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance()
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance()
	m, _ = m.advance()

	m.summaryCursor = summaryFieldExitCriteria
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := m.View()
	if !containsString(view, "Done") {
		t.Error("expected footer to contain 'Done' hint when editing ExitCriteria")
	}
	if !containsString(view, "Back") {
		t.Error("expected footer to contain 'Back' hint when editing ExitCriteria")
	}
}

func TestWizardBrowseForMoreAppearsInRepoList(t *testing.T) {
	repos := []string{"repo-a", "repo-b"}
	m := NewWizardModel(repos, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	// Advance to repos step: name -> desc -> repos
	m = advanceWizardToWhereViaUI(m, "test")

	view := m.View()
	if !strings.Contains(view, "Browse for more...") {
		t.Error("expected 'Browse for more...' in repo list view")
	}
}

func TestWizardBrowseForMoreAppearsWhenNoRepos(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	view := m.View()
	if !strings.Contains(view, "Browse for more...") {
		t.Error("expected 'Browse for more...' when no repos configured")
	}
	if strings.Contains(view, "Add repos to config.yaml first") {
		t.Error("old 'Add repos to config.yaml first' message should be removed")
	}
}

func TestWizardBrowseForMoreAppearsWithFilter(t *testing.T) {
	repos := []string{"repo-a", "repo-b"}
	m := NewWizardModel(repos, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	// Type a filter that matches nothing
	m, _ = m.Update(tea.KeyPressMsg{Text: "zzzzz"})

	view := m.View()
	if !strings.Contains(view, "Browse for more...") {
		t.Error("expected 'Browse for more...' to remain visible with filter")
	}
	if !strings.Contains(view, "No repos match filter") {
		t.Error("expected 'No repos match filter.' message")
	}
}

func TestWizardCursorCanReachBrowseItem(t *testing.T) {
	repos := []string{"repo-a", "repo-b"}
	m := NewWizardModel(repos, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, []string{"/tmp/roots"})
	m = advanceWizardToWhereViaUI(m, "test")

	// Tab cycles list -> Browse
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whereFocus != whereFocusBrowse {
		t.Errorf("expected focus on Browse after Tab, got %d", m.whereFocus)
	}

	// Tab again reaches Create
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whereFocus != whereFocusCreate {
		t.Errorf("expected focus on Create after second Tab, got %d", m.whereFocus)
	}

	// Tab again reaches Continue
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whereFocus != whereFocusContinue {
		t.Errorf("expected focus on Continue after third Tab, got %d", m.whereFocus)
	}

	// Tab again wraps back to the list
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whereFocus != whereFocusList {
		t.Errorf("expected focus to wrap to list, got %d", m.whereFocus)
	}

	// Up from Continue with the create row visible should land on Create
	m.whereFocus = whereFocusContinue
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.whereFocus != whereFocusCreate {
		t.Errorf("expected Up from Continue to land on Create, got %d", m.whereFocus)
	}
}

func TestWizardTabOnBrowseOpensPicker(t *testing.T) {
	repos := []string{"repo-a", "repo-b"}
	m := NewWizardModel(repos, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	// Tab cycles to Browse focus
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whereFocus != whereFocusBrowse {
		t.Fatalf("expected focus on Browse after Tab, got %d", m.whereFocus)
	}

	// Enter on Browse opens the picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.IsPickerActive() {
		t.Error("expected picker to be active after Enter on Browse focus")
	}
	if m.ConsumeBrowseRoot() != "" {
		t.Error("no root should be selected yet")
	}
}

func TestWizardPickerCancelReturnToRepos(t *testing.T) {
	repos := []string{"repo-a"}
	m := NewWizardModel(repos, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	// Tab to Browse, then Enter to open picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.IsPickerActive() {
		t.Fatal("precondition: picker should be active")
	}

	// Cancel picker with Escape
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.IsPickerActive() {
		t.Error("picker should be deactivated after cancel")
	}
	if m.ConsumeBrowseRoot() != "" {
		t.Error("no root should be set after cancel")
	}
}

func TestWizardConsumeBrowseRootIdempotent(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.browseRoot = "/tmp/test-root"

	got := m.ConsumeBrowseRoot()
	if got != "/tmp/test-root" {
		t.Errorf("expected /tmp/test-root, got %q", got)
	}
	got = m.ConsumeBrowseRoot()
	if got != "" {
		t.Errorf("expected empty string on second consume, got %q", got)
	}
}

func TestWizardRefreshReposPreservesSelections(t *testing.T) {
	repos := []string{"repo-a", "repo-b"}
	repoConfigs := map[string]config.RepoConfig{
		"repo-a": {Path: "/a"},
		"repo-b": {Path: "/b"},
	}
	m := NewWizardModel(repos, nil, repoConfigs, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	// Select repo-a (Space toggles repos on Where step)
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})

	if !m.selectedRepos["repo-a"] {
		t.Fatal("precondition: repo-a should be selected")
	}

	// Refresh with new repos
	newRepos := []string{"repo-a", "repo-b", "repo-c"}
	newConfigs := map[string]config.RepoConfig{
		"repo-a": {Path: "/a"},
		"repo-b": {Path: "/b"},
		"repo-c": {Path: "/c"},
	}
	m.RefreshRepos(newRepos, nil, newConfigs)

	if !m.selectedRepos["repo-a"] {
		t.Error("repo-a selection should be preserved after refresh")
	}
	if m.selectedRepos["repo-c"] {
		t.Error("new repo-c should NOT be auto-selected")
	}
	found := false
	for _, r := range m.filteredRepos {
		if r == "repo-c" {
			found = true
			break
		}
	}
	if !found {
		t.Error("repo-c should appear in filtered repos after refresh")
	}
}

func TestWizardRefreshReposPreservesSelectionsOnCollision(t *testing.T) {
	// Start with a single repo "myrepo" from rootA.
	repos := []string{"myrepo"}
	repoPaths := map[string]string{"myrepo": "/rootA/myrepo"}
	repoConfigs := map[string]config.RepoConfig{
		"myrepo": {Path: "/rootA/myrepo"},
	}
	m := NewWizardModel(repos, repoPaths, repoConfigs, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	// Select "myrepo" (Space toggles repos on Where step)
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if !m.selectedRepos["myrepo"] {
		t.Fatal("precondition: myrepo should be selected")
	}

	// Simulate adding rootB which also has "myrepo" — causes collision re-keying.
	// After re-discovery, both repos get qualified names.
	newRepos := []string{"rootA/myrepo", "rootB/myrepo"}
	newPaths := map[string]string{
		"rootA/myrepo": "/rootA/myrepo",
		"rootB/myrepo": "/rootB/myrepo",
	}
	newConfigs := map[string]config.RepoConfig{
		"rootA/myrepo": {Path: "/rootA/myrepo"},
		"rootB/myrepo": {Path: "/rootB/myrepo"},
	}
	m.RefreshRepos(newRepos, newPaths, newConfigs)

	// The old "myrepo" selection (path /rootA/myrepo) must survive under the new key.
	if !m.selectedRepos["rootA/myrepo"] {
		t.Errorf("selection for /rootA/myrepo should survive collision re-keying; selectedRepos=%v", m.selectedRepos)
	}
	if m.selectedRepos["myrepo"] {
		t.Error("old key 'myrepo' should no longer be in selectedRepos")
	}
	if m.selectedRepos["rootB/myrepo"] {
		t.Error("new rootB/myrepo should NOT be auto-selected")
	}
}

func TestWizardDirPickerDelegatesAllMessages(t *testing.T) {
	repos := []string{"repo-a"}
	m := NewWizardModel(repos, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	// Tab to Browse, then Enter to open the picker.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Send non-key message — should not panic
	m, _ = m.Update(gitRepoScanMsg{dir: "/tmp", count: 0, repoDirs: map[string]bool{}})

	// Send key message — should not panic
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	if !m.IsPickerActive() {
		t.Error("picker should still be active after delegated messages")
	}
}

func TestWizardMultipleBrowseRounds(t *testing.T) {
	m := NewWizardModel([]string{"repo-a"}, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	// Round 1: Tab to Browse, Enter to open, Esc to cancel.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.IsPickerActive() {
		t.Fatal("round 1: expected picker active")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.IsPickerActive() {
		t.Fatal("round 1: expected picker cancelled")
	}

	// Round 2: focus should still be on Browse, Enter reopens.
	if m.whereFocus != whereFocusBrowse {
		t.Fatalf("focus should remain on Browse, got %d", m.whereFocus)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.IsPickerActive() {
		t.Error("round 2: expected picker to reopen")
	}
}

func TestWizardPipelineDefaultsStandard(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	if m.pipelineCursor != 1 {
		t.Errorf("pipelineCursor = %d, want 1", m.pipelineCursor)
	}
	if len(m.pipelineOptions) != 3 {
		t.Fatalf("pipelineOptions length = %d, want 3", len(m.pipelineOptions))
	}
	if m.pipelineOptions[0] != "medium" {
		t.Errorf("pipelineOptions[0] = %q, want %q", m.pipelineOptions[0], "medium")
	}
	if m.pipelineOptions[1] != "large" {
		t.Errorf("pipelineOptions[1] = %q, want %q", m.pipelineOptions[1], "large")
	}
	if m.pipelineOptions[2] != "moonshot" {
		t.Errorf("pipelineOptions[2] = %q, want %q", m.pipelineOptions[2], "moonshot")
	}
}

func TestWizardPipelineStepNavigation(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m.descInput.SetValue("test description")

	// Advance to pipeline step
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline

	if m.step != wizardStepPipeline {
		t.Fatalf("expected pipeline step, got %v", m.step)
	}

	initial := m.pipelineCursor
	// Down arrow changes selection
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.pipelineCursor == initial {
		t.Error("Down arrow should change pipeline cursor")
	}

	// Up arrow goes back
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.pipelineCursor != initial {
		t.Errorf("Up arrow should restore pipeline cursor to %d, got %d", initial, m.pipelineCursor)
	}

	// Enter advances to review
	m, _ = m.advance()
	if m.step != wizardStepReview {
		t.Errorf("expected review step after advance from pipeline, got %v", m.step)
	}
}

func TestWizardResultIncludesPipeline(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("test")
	m.descInput.SetValue("test description")

	// Advance to review step
	m.selectedRepos["test-repo"] = true
	for m.step != wizardStepReview {
		m, _ = m.advance()
	}

	// Press G on review to create
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})

	if !m.IsDone() {
		t.Fatal("wizard should be done after G on review")
	}
	result := m.Result()
	if result == nil {
		t.Fatal("result should not be nil")
	}
	// Pipeline defaults to large
	if result.Pipeline != feature.PipelineLarge {
		t.Errorf("Pipeline = %v, want large (default)", result.Pipeline)
	}
}

// navigateToModelEditing is a helper that creates a wizard, advances to the
// Review step, and enters model editing mode (summaryEditing on the Models field).
func navigateToModelEditing(t *testing.T, defaults config.DefaultsConfig, providerModels map[string][]string, providerOrder []string, phaseDefaults map[string]string) WizardModel {
	t.Helper()
	m := NewWizardModel(nil, nil, nil, defaults, "", providerModels, providerOrder, phaseDefaults, nil, nil, nil)
	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	if m.step != wizardStepReview {
		t.Fatalf("expected Review step, got %d", m.step)
	}

	// summaryCursor starts at summaryFieldRisk; move down once to Models
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // Risk -> Models
	// Enter to start editing models
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.summaryEditing {
		t.Fatal("expected summaryEditing to be true after pressing Enter on Models")
	}
	return m
}

func TestWizardSummaryModelsIncludesKBBuild(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "codex",
			KBBuild:        "opus[1m]",
		},
	}
	providerModels := map[string][]string{"test": {"opus", "sonnet", "codex", "gpt-5.4", "opus[1m]"}}
	providerOrder := []string{"test"}
	m := navigateToModelEditing(t, defaults, providerModels, providerOrder, nil)

	if len(m.modelFields) != 6 {
		t.Errorf("expected 6 model fields, got %d", len(m.modelFields))
	}
	if m.modelFields[0] != "Clarify" {
		t.Errorf("expected modelFields[0] = %q, got %q", "Clarify", m.modelFields[0])
	}
	if m.modelFields[5] != "KB Build" {
		t.Errorf("expected modelFields[5] = %q, got %q", "KB Build", m.modelFields[5])
	}
}

func TestWizardSummaryModelsKBBuildCycles(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "codex",
			KBBuild:        "opus[1m]",
		},
	}
	providerModels := map[string][]string{"test": {"opus", "sonnet", "codex", "gpt-5.4", "opus[1m]"}}
	providerOrder := []string{"test"}
	m := navigateToModelEditing(t, defaults, providerModels, providerOrder, nil)

	// Navigate down to KB Build row (index 5)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 0 -> 1
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 1 -> 2
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 2 -> 3
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 3 -> 4
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 4 -> 5

	if m.modelCursor != 5 {
		t.Fatalf("expected modelCursor = 5, got %d", m.modelCursor)
	}

	// Cycle KB Build model from the Model panel.
	if m.models.KBBuild != "opus[1m]" {
		t.Fatalf("expected initial KBBuild = opus[1m], got %q", m.models.KBBuild)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.configEditor.activeModelCell; got != modelCellModel {
		t.Fatalf("second Right activeModelCell = %v, want modelCellModel", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.models.KBBuild != "opus" {
		t.Errorf("expected KBBuild = opus after cycle, got %q", m.models.KBBuild)
	}
}

func TestWizardSummaryEditedKBBuildInResult(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Inquiry:        "opus",
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "codex",
			KBBuild:        "opus[1m]",
		},
	}
	providerModels := map[string][]string{"test": {"opus", "sonnet", "codex", "gpt-5.4", "opus[1m]"}}
	providerOrder := []string{"test"}
	m := navigateToModelEditing(t, defaults, providerModels, providerOrder, nil)

	// Navigate to KB Build and cycle its Model panel.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})  // 0 -> 1
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})  // 1 -> 2
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})  // 2 -> 3
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})  // 3 -> 4
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})  // 4 -> 5
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // focus Agent panel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // focus Model panel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})  // cycle KB Build

	// Close model editing from the phase panel, then create feature (G)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})   // return to Phases panel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})   // close model editing
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"}) // create -> done

	if !m.IsDone() {
		t.Fatal("expected wizard to be done")
	}
	result := m.Result()
	if result.Models.KBBuild != "opus" {
		t.Errorf("result.Models.KBBuild = %q, want %q", result.Models.KBBuild, "opus")
	}
}

func TestWizardSummaryModelsUpDownNavigation_SixRows(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Inquiry:        "opus",
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "codex",
			KBBuild:        "opus[1m]",
		},
	}
	providerModels := map[string][]string{"test": {"opus", "sonnet", "codex", "gpt-5.4", "opus[1m]"}}
	providerOrder := []string{"test"}
	m := navigateToModelEditing(t, defaults, providerModels, providerOrder, nil)

	// Navigate down to index 5 (KB Build)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 0 -> 1
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 1 -> 2
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 2 -> 3
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 3 -> 4
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 4 -> 5

	if m.modelCursor != 5 {
		t.Errorf("expected modelCursor = 5, got %d", m.modelCursor)
	}

	// Down again: should stay at 5 (clamped)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modelCursor != 5 {
		t.Errorf("expected modelCursor to stay at 5 (clamped), got %d", m.modelCursor)
	}
}

func TestWizardSummaryModelsPrefixedModelsCycle(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Inquiry:        "claude:opus",
			Research:       "claude:opus",
			Planning:       "claude:opus",
			Implementation: "claude:opus",
			Review:         "claude:sonnet",
			KBBuild:        "claude:opus",
		},
	}
	providerModels := map[string][]string{
		"claude": {"opus", "sonnet"},
		"codex":  {"gpt-5.4"},
	}
	providerOrder := []string{"claude", "codex"}
	m := navigateToModelEditing(t, defaults, providerModels, providerOrder, nil)

	// Navigate to KB Build row
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 0 -> 1
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 1 -> 2
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 2 -> 3
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 3 -> 4
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 4 -> 5

	// Cycle through prefixed KB Build models scoped to the selected agent.
	if m.models.KBBuild != "claude:opus" {
		t.Fatalf("expected initial KBBuild = claude:opus, got %q", m.models.KBBuild)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // focus Agent panel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // focus Model panel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})  // cycle within claude
	if m.models.KBBuild != "claude:sonnet" {
		t.Errorf("expected KBBuild = claude:sonnet after first cycle, got %q", m.models.KBBuild)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // wrap within claude
	if m.models.KBBuild != "claude:opus" {
		t.Errorf("expected KBBuild = claude:opus after scoped wrap, got %q", m.models.KBBuild)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // focus Agent panel
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})                   // switch provider
	if m.models.KBBuild != "codex:gpt-5.4" {
		t.Errorf("expected KBBuild = codex:gpt-5.4 after provider cycle, got %q", m.models.KBBuild)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // provider wraps
	if m.models.KBBuild != "claude:opus" {
		t.Errorf("expected KBBuild = claude:opus after provider wrap, got %q", m.models.KBBuild)
	}
}

func TestWizardSummaryModelsReviewShowsKBBuild(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Inquiry:        "opus",
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "codex",
			KBBuild:        "opus[1m]",
		},
	}
	providerModels := map[string][]string{"test": {"opus", "sonnet", "codex", "gpt-5.4", "opus[1m]"}}
	providerOrder := []string{"test"}
	m := NewWizardModel(nil, nil, nil, defaults, "", providerModels, providerOrder, nil, nil, nil, nil)
	m.width = 80
	m.height = 24

	// Navigate to Review step
	m.nameInput.SetValue("test")
	m, _ = m.advance() // What -> Where
	m.selectedRepos["test-repo"] = true
	m, _ = m.advance() // Where -> Pipeline
	m, _ = m.advance() // Pipeline -> Review

	if m.step != wizardStepReview {
		t.Fatalf("expected Review step, got %d", m.step)
	}

	view := m.View()
	if !containsString(view, "KB") {
		t.Error("expected 'KB' in model summary on Review step")
	}
	if !containsString(view, "opus[1m]") {
		t.Error("expected 'opus[1m]' (KB Build model value) in Review view")
	}
}

func TestWizardModelPickerMultiProvider(t *testing.T) {
	providerModels := map[string][]string{
		"claude": {"opus", "sonnet"},
		"codex":  {"gpt-5.4"},
	}
	providerOrder := []string{"claude", "codex"}
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", providerModels, providerOrder, nil, nil, nil, nil)

	// allModels should be flattened in provider order
	wantAll := []string{"opus", "sonnet", "gpt-5.4"}
	if !reflect.DeepEqual(m.allModels, wantAll) {
		t.Errorf("allModels = %v, want %v", m.allModels, wantAll)
	}
	// modelOptionsForField should return allModels for any field
	if !reflect.DeepEqual(m.modelOptionsForField("Research"), wantAll) {
		t.Errorf("modelOptionsForField(Research) = %v, want %v", m.modelOptionsForField("Research"), wantAll)
	}
	if !reflect.DeepEqual(m.modelOptionsForField("Review"), wantAll) {
		t.Errorf("modelOptionsForField(Review) = %v, want %v", m.modelOptionsForField("Review"), wantAll)
	}
}

func TestWizardModelPickerSingleProvider(t *testing.T) {
	providerModels := map[string][]string{
		"claude": {"opus", "sonnet"},
	}
	providerOrder := []string{"claude"}
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", providerModels, providerOrder, nil, nil, nil, nil)

	wantAll := []string{"opus", "sonnet"}
	if !reflect.DeepEqual(m.allModels, wantAll) {
		t.Errorf("allModels = %v, want %v", m.allModels, wantAll)
	}
}

func TestWizardModelPickerEmptyProviders(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	if len(m.allModels) != 0 {
		t.Errorf("expected empty allModels, got %v", m.allModels)
	}
	// cycleModel should be a no-op
	m.modelCursor = 1
	m.cycleModel()
	// Should not panic
}

func TestWizardPhaseDefaultsClamping(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "nonexistent",
			Implementation: "sonnet",
			Review:         "gpt-5.4",
			KBBuild:        "opus",
		},
	}
	providerModels := map[string][]string{
		"claude": {"opus", "sonnet"},
		"codex":  {"gpt-5.4"},
	}
	providerOrder := []string{"claude", "codex"}
	phaseDefaults := map[string]string{
		"Research":       "opus",
		"Planning":       "opus",
		"Implementation": "opus",
		"Review":         "gpt-5.4",
	}
	m := NewWizardModel(nil, nil, nil, defaults, "", providerModels, providerOrder, phaseDefaults, nil, nil, nil)

	// "opus" exists → stays
	if m.models.Research != "opus" {
		t.Errorf("Research = %q, want opus", m.models.Research)
	}
	// "nonexistent" doesn't exist → clamped to first model
	if m.models.Planning != "opus" {
		t.Errorf("Planning = %q, want opus (clamped)", m.models.Planning)
	}
	// "sonnet" exists → stays
	if m.models.Implementation != "sonnet" {
		t.Errorf("Implementation = %q, want sonnet", m.models.Implementation)
	}
	// "gpt-5.4" exists → stays
	if m.models.Review != "gpt-5.4" {
		t.Errorf("Review = %q, want gpt-5.4", m.models.Review)
	}
	// phaseDefaults should be stored
	if m.phaseDefaults["Research"] != "opus" {
		t.Errorf("phaseDefaults[Research] = %q, want opus", m.phaseDefaults["Research"])
	}
}

// TestWizardClampKeepsPrefixedProviderDefault proves the wizard's default
// clamp keeps a valid multi-provider default persisted in "<provider>:<id>"
// routing form rather than discarding it for the first bare option. The
// per-provider option lists carry bare backend ids while the default is
// prefixed, exactly the Phase 6 multi-provider shape.
func TestWizardClampKeepsPrefixedProviderDefault(t *testing.T) {
	const prefixed = "gateway:vendor/sonnet[200K]"
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       prefixed,
			Planning:       prefixed,
			Implementation: prefixed,
			Review:         prefixed,
			KBBuild:        prefixed,
		},
	}
	providerModels := map[string][]string{
		"claude":  {"sonnet[200K]", "opus[200K]"},
		"gateway": {"vendor/sonnet[200K]", "vendor/gpt-5"},
	}
	providerOrder := []string{"claude", "gateway"}
	phaseModels := map[string]map[string][]string{}
	for _, f := range []string{"Clarify", "Research", "Planning", "Implementation", "Review", "KB Build"} {
		phaseModels[f] = map[string][]string{
			"claude":  {"sonnet[200K]", "opus[200K]"},
			"gateway": {"vendor/sonnet[200K]", "vendor/gpt-5"},
		}
	}

	m := NewWizardModel(nil, nil, nil, defaults, "", providerModels, providerOrder, nil, phaseModels, nil, nil)

	if len(m.pipelinePreferences) == 0 {
		t.Fatal("no pipeline preferences built")
	}
	for profile, pref := range m.pipelinePreferences {
		if pref.Models.Implementation != prefixed {
			t.Errorf("pipeline %q Implementation clamped to %q, want kept %q (first bare option would be sonnet[200K])",
				profile, pref.Models.Implementation, prefixed)
		}
		if pref.Models.Review != prefixed {
			t.Errorf("pipeline %q Review clamped to %q, want kept %q", profile, pref.Models.Review, prefixed)
		}
	}
}

func TestWizardModelCyclingForwardAndWrap(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{Research: "opus"},
	}
	providerModels := map[string][]string{"test": {"opus", "sonnet", "gpt-5.4"}}
	providerOrder := []string{"test"}
	m := NewWizardModel(nil, nil, nil, defaults, "", providerModels, providerOrder, nil, nil, nil, nil)

	// Forward: opus → sonnet
	m.modelCursor = 1
	m.cycleModel()
	if m.models.Research != "sonnet" {
		t.Errorf("after cycle forward: Research = %q, want sonnet", m.models.Research)
	}
	// Forward: sonnet → gpt-5.4
	m.cycleModel()
	if m.models.Research != "gpt-5.4" {
		t.Errorf("after second cycle: Research = %q, want gpt-5.4", m.models.Research)
	}
	// Wrap: gpt-5.4 → opus
	m.cycleModel()
	if m.models.Research != "opus" {
		t.Errorf("after wrap: Research = %q, want opus", m.models.Research)
	}
}

func TestWizardModelCyclingReverseAndWrap(t *testing.T) {
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{Research: "opus"},
	}
	providerModels := map[string][]string{"test": {"opus", "sonnet", "gpt-5.4"}}
	providerOrder := []string{"test"}
	m := NewWizardModel(nil, nil, nil, defaults, "", providerModels, providerOrder, nil, nil, nil, nil)

	// Reverse from opus → wraps to gpt-5.4
	m.modelCursor = 1
	m.cycleModelReverse()
	if m.models.Research != "gpt-5.4" {
		t.Errorf("after reverse wrap: Research = %q, want gpt-5.4", m.models.Research)
	}
	// Reverse: gpt-5.4 → sonnet
	m.cycleModelReverse()
	if m.models.Research != "sonnet" {
		t.Errorf("after reverse: Research = %q, want sonnet", m.models.Research)
	}
}

func TestWizardCreateNewRepoOptionRendered(t *testing.T) {
	// Create wizard with some repos and workspace roots (needed for "Create new repo..." to appear)
	m := NewWizardModel([]string{"repo-a"}, map[string]string{"repo-a": "/tmp/a"}, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, []string{"/tmp/roots"})
	m = advanceWizardToWhereViaUI(m, "test")
	m.width = 200
	m.height = 40

	view := m.View()
	// Should contain both special items
	if !strings.Contains(view, "Browse for more...") {
		t.Error("expected Browse for more... in view")
	}
	if !strings.Contains(view, "Create new repo...") {
		t.Error("expected Create new repo... in view")
	}
}

func TestWizardCreateNewRepoHiddenWithoutRoots(t *testing.T) {
	// No workspace roots → "Create new repo..." should not render
	m := NewWizardModel([]string{"repo-a"}, map[string]string{"repo-a": "/tmp/a"}, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")
	m.width = 200
	m.height = 40

	view := m.View()
	if !strings.Contains(view, "Browse for more...") {
		t.Error("expected Browse for more... in view")
	}
	if strings.Contains(view, "Create new repo...") {
		t.Error("Create new repo... should be hidden when no workspace roots are configured")
	}
}

func TestWizardCursorClampsAtBrowseWithoutRoots(t *testing.T) {
	repos := []string{"repo-a"}
	// No workspace roots → action axis only has Browse and Continue (no Create row).
	m := NewWizardModel(repos, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	// Tab cycles list -> Browse (skips Create when no workspace roots)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whereFocus != whereFocusBrowse {
		t.Errorf("expected focus on Browse after Tab, got %d", m.whereFocus)
	}

	// Tab again goes to Continue (NOT Create — Create is not visible)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whereFocus != whereFocusContinue {
		t.Errorf("expected focus to skip Create and land on Continue, got %d", m.whereFocus)
	}
}

func TestWizardCreateNewRepoRootPicker(t *testing.T) {
	repos := []string{"repo-a"}
	roots := []string{"/tmp/root-one", "/tmp/root-two"}
	m := NewWizardModel(repos, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, roots)
	m = advanceWizardToWhereViaUI(m, "test")

	// Tab twice to cycle list -> Browse -> Create
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.whereFocus != whereFocusCreate {
		t.Fatalf("expected focus on Create after two Tabs, got %d", m.whereFocus)
	}

	// Enter opens root picker overlay
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.IsRootPickerActive() {
		t.Fatal("expected root picker to be active after Enter on Create focus")
	}
	if m.rootPickerCursor != 0 {
		t.Errorf("expected root picker cursor at 0, got %d", m.rootPickerCursor)
	}

	// Navigate down in root picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.rootPickerCursor != 1 {
		t.Errorf("expected root picker cursor at 1 after down, got %d", m.rootPickerCursor)
	}

	// Navigate back up
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.rootPickerCursor != 0 {
		t.Errorf("expected root picker cursor at 0 after up, got %d", m.rootPickerCursor)
	}

	// Select first root with Enter — overlay stays open for name input (phase 2)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.IsRootPickerActive() {
		t.Error("root picker should stay active for name input phase")
	}
	if !m.createRepoActive {
		t.Error("createRepoActive should be true after root selection")
	}
	if m.createRepoParentPath != "/tmp/root-one" {
		t.Errorf("expected parent path /tmp/root-one, got %s", m.createRepoParentPath)
	}

	// Verify overlay shows name input phase
	m.width = 80
	view := m.RootPickerView()
	if !strings.Contains(view, "Create New Repo") {
		t.Error("expected 'Create New Repo' title in phase 2")
	}
	if !strings.Contains(view, "root-one") {
		t.Error("expected parent path in phase 2 view")
	}
}

func TestWizardRootPickerEscCancels(t *testing.T) {
	repos := []string{"repo-a"}
	roots := []string{"/tmp/root-one"}
	m := NewWizardModel(repos, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, roots)
	m = advanceWizardToWhereViaUI(m, "test")

	// Tab to Create then Enter to open root picker
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.IsRootPickerActive() {
		t.Fatal("expected root picker to be active")
	}

	// Esc from root selection phase closes overlay
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.IsRootPickerActive() {
		t.Error("root picker should be dismissed after esc from root selection")
	}
	if m.createRepoActive {
		t.Error("createRepoActive should remain false after cancel")
	}
}

func TestWizardRootPickerEscFromNameGoesBack(t *testing.T) {
	repos := []string{"repo-a"}
	roots := []string{"/tmp/root-one"}
	m := NewWizardModel(repos, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, roots)
	m = advanceWizardToWhereViaUI(m, "test")

	// Tab twice to Create, Enter opens root picker, Enter again selects first root.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // select root → name input phase

	if !m.createRepoActive {
		t.Fatal("expected createRepoActive after root selection")
	}

	// Esc from name input goes back to root selection (overlay stays open)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.IsRootPickerActive() {
		t.Error("root picker should still be active after esc from name input")
	}
	if m.createRepoActive {
		t.Error("createRepoActive should be false after esc from name input")
	}
	if m.createRepoParentPath != "" {
		t.Error("createRepoParentPath should be cleared")
	}
}

func TestWizardRootPickerOverlayView(t *testing.T) {
	roots := []string{"/tmp/root-one", "/tmp/root-two"}
	m := NewWizardModel([]string{"repo-a"}, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, roots)
	m.width = 80
	m.height = 40
	m.rootPickerActive = true
	m.rootPickerRoots = roots
	m.rootPickerCursor = 0

	view := m.RootPickerView()
	if !strings.Contains(view, "Select Parent Directory") {
		t.Error("expected title in root picker view")
	}
	if !strings.Contains(view, "/tmp/root-one") {
		t.Error("expected first root in view")
	}
	if !strings.Contains(view, "/tmp/root-two") {
		t.Error("expected second root in view")
	}
}

func TestWizardCreateNewRepoNameValidation(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	// Simulate: parent selected, active
	parentDir := t.TempDir()
	m.createRepoActive = true
	m.createRepoParentPath = parentDir
	m.createRepoNameInput.Focus()
	m.createRepoFn = func(parentPath, name string) error {
		return nil
	}

	t.Run("empty name rejected", func(t *testing.T) {
		mc := m // copy
		mc, _ = mc.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if mc.createRepoError == "" {
			t.Error("expected error for empty name")
		}
	})

	t.Run("invalid chars rejected", func(t *testing.T) {
		mc := m // copy
		mc.createRepoNameInput.SetValue("bad/name")
		mc, _ = mc.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if mc.createRepoError == "" {
			t.Error("expected error for invalid chars")
		}
	})

	t.Run("existing directory rejected", func(t *testing.T) {
		mc := m // copy
		// Create an existing dir
		existDir := filepath.Join(parentDir, "existing")
		os.MkdirAll(existDir, 0o755)
		mc.createRepoNameInput.SetValue("existing")
		mc, _ = mc.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if mc.createRepoError == "" {
			t.Error("expected error for existing directory")
		}
	})

	t.Run("valid name accepted", func(t *testing.T) {
		mc := m // copy
		mc.createRepoNameInput.SetValue("new-repo")
		mc, _ = mc.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if mc.createRepoError != "" {
			t.Errorf("unexpected error: %s", mc.createRepoError)
		}
	})
}

func TestWizardCreateNewRepoCreatesGitRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-git create-repo regression in short mode")
	}

	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	parentDir := t.TempDir()
	m.createRepoActive = true
	m.createRepoParentPath = parentDir
	m.createRepoNameInput.Focus()
	m.createRepoNameInput.SetValue("my-project")

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	repoPath := filepath.Join(parentDir, "my-project")
	// Verify directory exists
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		t.Fatal("repo directory was not created")
	}
	// Verify .git exists
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); os.IsNotExist(err) {
		t.Fatal(".git directory was not created")
	}
	// Verify initial commit
	cmd := exec.Command("git", "-C", repoPath, "log", "--oneline")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	if !strings.Contains(string(out), "Initial commit") {
		t.Errorf("expected initial commit, got: %s", string(out))
	}
	// Verify createRepoPath is set for app consumption
	if m.createRepoPath != repoPath {
		t.Errorf("createRepoPath = %q, want %q", m.createRepoPath, repoPath)
	}
	// Verify create-repo state cleared
	if m.createRepoActive {
		t.Error("createRepoActive should be false after creation")
	}
}

func TestWizardCreateNewRepoDeselectsExistingRepos(t *testing.T) {
	m := NewWizardModel([]string{"repo-a", "repo-b"}, map[string]string{"repo-a": "/tmp/a", "repo-b": "/tmp/b"}, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")
	m.selectedRepos["repo-a"] = true
	m.selectedRepos["repo-b"] = true

	parentDir := t.TempDir()
	m.createRepoActive = true
	m.createRepoParentPath = parentDir
	m.createRepoNameInput.Focus()
	m.createRepoNameInput.SetValue("new-repo")
	m.createRepoFn = func(parentPath, name string) error {
		return nil
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// All existing repos should be deselected
	for repo, sel := range m.selectedRepos {
		if sel {
			t.Errorf("repo %q should be deselected after create new repo", repo)
		}
	}
}

func TestWizardSelectExistingRepoClearsCreateRepoState(t *testing.T) {
	m := NewWizardModel([]string{"repo-a"}, map[string]string{"repo-a": "/tmp/a"}, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	// Simulate post-creation state: repo was created (active=false) but path lingers
	m.createRepoActive = false
	m.createRepoParentPath = "/tmp/parent"
	m.createRepoPath = "/tmp/parent/new"

	// Toggle repo-a on via Space (cursor should be at 0)
	m.repoCursor = 0
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})

	if m.createRepoActive {
		t.Error("createRepoActive should be cleared")
	}
	if m.createRepoPath != "" {
		t.Error("createRepoPath should be cleared")
	}
	if m.createRepoParentPath != "" {
		t.Error("createRepoParentPath should be cleared")
	}
}

func TestWizardProvisionalPublishabilityFromRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-git remote detection in short mode")
	}
	// Create repo with remote
	publishedRepo := testutil.InitGitRepo(t)
	testutil.InitBareRemote(t, publishedRepo)

	// Create repo without remote
	unpublishedRepo := testutil.InitGitRepo(t)

	repoPaths := map[string]string{
		"published":   publishedRepo,
		"unpublished": unpublishedRepo,
	}
	m := NewWizardModel([]string{"published", "unpublished"}, repoPaths, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	t.Run("published only", func(t *testing.T) {
		mc := m
		mc.selectedRepos["published"] = true
		mc.recomputeProvisionalPublishability()
		if !mc.provisionalPublishable {
			t.Error("expected provisionalPublishable=true for published repo")
		}
	})

	t.Run("unpublished only", func(t *testing.T) {
		mc := m
		mc.selectedRepos["unpublished"] = true
		mc.recomputeProvisionalPublishability()
		if mc.provisionalPublishable {
			t.Error("expected provisionalPublishable=false for unpublished repo")
		}
	})

	t.Run("both selected", func(t *testing.T) {
		mc := m
		mc.selectedRepos["published"] = true
		mc.selectedRepos["unpublished"] = true
		mc.recomputeProvisionalPublishability()
		if mc.provisionalPublishable {
			t.Error("expected provisionalPublishable=false when any repo is unpublished")
		}
	})
}

func TestWizardProvisionalPublishabilityNewRepo(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")

	m.createRepoActive = true
	m.recomputeProvisionalPublishability()
	if m.provisionalPublishable {
		t.Error("expected provisionalPublishable=false for new repo")
	}
}

func TestWizardCheckpointHidingReviewEditor(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.provisionalPublishable = false
	m.pipelineCursor = 2
	m.applyPipelineDefaults()
	m.width = 200
	m.height = 40

	rendered := m.renderCheckpointsEditor(80)
	if strings.Contains(rendered, "Manual Publish") {
		t.Error("Manual Publish should be hidden when provisionalPublishable=false")
	}
	if !strings.Contains(rendered, "Inquiry Review") {
		t.Error("Inquiry Review should still be visible")
	}
}

func TestWizardCheckpointVisibleWhenPublishable(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.provisionalPublishable = true
	m.width = 200
	m.height = 40

	rendered := m.renderCheckpointsEditor(80)
	if !strings.Contains(rendered, "Manual Publish") {
		t.Error("Manual Publish should be visible when provisionalPublishable=true")
	}
}

func TestWizardCheckpointHidingManualPublishForcedTrue(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m = advanceWizardToWhereViaUI(m, "test")
	m.provisionalPublishable = false

	m.applyPipelineDefaults()

	if !m.checkpoints[checkpointManualPublish] {
		t.Error("ManualPublish should be forced true when provisionalPublishable=false")
	}
}

func TestWizardCheckpointHidingCursorBound(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.NewDefault().Defaults, "", nil, nil, nil, nil, nil, nil)
	m.provisionalPublishable = false
	m.pipelineCursor = 2
	m.applyPipelineDefaults()
	m.step = wizardStepReview
	m.summaryEditing = true
	m.summaryCursor = summaryFieldCheckpoints
	m.checkpointsCursor = checkpointPhasePlanReview // at the max for unpublished

	// Try to go down — should not move to the hidden Manual Publish row.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.checkpointsCursor != checkpointPhasePlanReview {
		t.Errorf("checkpointsCursor = %d, want %d (max for unpublished)", m.checkpointsCursor, checkpointPhasePlanReview)
	}
}

func TestWizardApplyPipelineDefaultsUsesConfiguredCheckpointDefaults(t *testing.T) {
	defaults := config.NewDefault().Defaults
	defaults.Checkpoints.RoadmapReview = false
	defaults.Checkpoints.PhasePlanReview = false

	m := NewWizardModel(nil, nil, nil, defaults, "", nil, nil, nil, nil, nil, nil)
	m.pipelineCursor = 0 // medium
	m.provisionalPublishable = true

	projection := m.projectedPipelineCheckpoints(feature.PipelineMedium.ConfigKey())
	if projection.Checkpoints.RoadmapReview || projection.Checkpoints.PhasePlanReview {
		t.Fatalf("projected medium checkpoints = %+v, want planning reviews disabled by configured defaults", projection.Checkpoints)
	}

	m.applyPipelineDefaults()

	got := m.checkpointState()
	if got.RoadmapReview || got.PhasePlanReview {
		t.Fatalf("applied medium checkpoints = %+v, want planning reviews disabled by configured defaults", got)
	}
	if !got.ManualPublish {
		t.Fatalf("applied medium checkpoints = %+v, want ManualPublish preserved from configured defaults", got)
	}
}

func TestWizardAutoSelectCreatedRepo(t *testing.T) {
	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)

	repoPaths := map[string]string{
		"repo-a":   "/tmp/a",
		"repo-b":   "/tmp/b",
		"new-repo": "/tmp/new",
	}
	m.repoPaths = repoPaths
	m.selectedRepos["repo-a"] = true

	m.AutoSelectCreatedRepo("/tmp/new", repoPaths)

	if !m.selectedRepos["new-repo"] {
		t.Error("new-repo should be selected")
	}
	if m.selectedRepos["repo-a"] {
		t.Error("repo-a should be deselected")
	}
	if m.provisionalPublishable {
		t.Error("provisionalPublishable should be false for new repo")
	}
}

func TestWizardMergedPipelineCheckpointsNormalizeToSelectedProfile(t *testing.T) {
	tests := []struct {
		name        string
		profile     string
		repoConfigs map[string]config.RepoConfig
		want        feature.Checkpoints
	}{
		{
			name:    "medium keeps roadmap, phase plan, and manual publish",
			profile: "medium",
			repoConfigs: map[string]config.RepoConfig{
				"alpha": {
					PipelineGates: map[string]config.Checkpoints{
						"medium": {InquiryReview: true, DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
					},
				},
			},
			want: feature.Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
		},
		{
			name:    "large keeps only supported merged gates",
			profile: "large",
			repoConfigs: map[string]config.RepoConfig{
				"alpha": {
					PipelineGates: map[string]config.Checkpoints{
						"large": {InquiryReview: true, ManualPublish: true},
					},
				},
				"bravo": {
					PipelineGates: map[string]config.Checkpoints{
						"large": {ResearchReview: true, DesignReview: true, RoadmapReview: true, PhasePlanReview: true},
					},
				},
			},
			want: feature.Checkpoints{
				InquiryReview:   true,
				ResearchReview:  true,
				DesignReview:    true,
				RoadmapReview:   true,
				PhasePlanReview: true,
				ManualPublish:   true,
			},
		},
		{
			name:    "moonshot keeps the full merged gate set",
			profile: "moonshot",
			repoConfigs: map[string]config.RepoConfig{
				"alpha": {
					PipelineGates: map[string]config.Checkpoints{
						"moonshot": {InquiryReview: true, DesignReview: true},
					},
				},
				"bravo": {
					PipelineGates: map[string]config.Checkpoints{
						"moonshot": {ResearchReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
					},
				},
			},
			want: feature.Checkpoints{
				InquiryReview:   true,
				ResearchReview:  true,
				DesignReview:    true,
				RoadmapReview:   true,
				PhasePlanReview: true,
				ManualPublish:   true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var repos []string
			selected := make(map[string]bool, len(tt.repoConfigs))
			for repoName := range tt.repoConfigs {
				repos = append(repos, repoName)
				selected[repoName] = true
			}

			m := NewWizardModel(repos, nil, tt.repoConfigs, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
			m.selectedRepos = selected
			m.provisionalPublishable = true

			got, fromConfig := m.mergedPipelineCheckpoints(tt.profile)
			if !fromConfig {
				t.Fatal("expected fromConfig=true when selected repos declare pipeline_gates overrides")
			}
			if got != tt.want {
				t.Fatalf("mergedPipelineCheckpoints(%q) = %+v, want %+v", tt.profile, got, tt.want)
			}
		})
	}
}

func TestWizardPipelineCardAndReviewGatesTrackSelectedProfile(t *testing.T) {
	repoConfigs := map[string]config.RepoConfig{
		"alpha": {
			PipelineGates: map[string]config.Checkpoints{
				"medium":   {InquiryReview: true, ManualPublish: true},
				"large":    {InquiryReview: true, DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
				"moonshot": {InquiryReview: true, ResearchReview: true, DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
			},
		},
	}
	m := NewWizardModel([]string{"alpha"}, nil, repoConfigs, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("profile-switch")
	m.descInput.SetValue("desc")
	m.step = wizardStepPipeline
	m.selectedRepos = map[string]bool{"alpha": true}
	m.provisionalPublishable = true
	m.width = 120
	m.height = 40

	largeView := m.View()
	requireWizardViewContainsAll(t, largeView,
		"Gate options:", "Inquiry review", "Research review", "Design review",
		"Roadmap review", "Phase plan review", "Publish review")

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	requireWizardViewContainsAll(t, m.View(),
		"Gate options:", "Roadmap review", "Phase plan review", "Publish review")

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	moonshotView := m.View()
	requireWizardViewContainsAll(t, moonshotView,
		"Gate options:", "Inquiry review", "Research review", "Design review",
		"Roadmap review", "Phase plan review", "Publish review")

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	review := m.View()
	if !strings.Contains(review, "Inquiry review, Research review, Design review, Roadmap review,") ||
		!strings.Contains(review, "Phase plan review, Publish review") {
		t.Fatalf("review summary leaked the wrong gate set after profile switching:\n%s", review)
	}
}

func TestWizardExpressProjectionFiltersNonApplicableConfigGates(t *testing.T) {
	repoConfigs := map[string]config.RepoConfig{
		"alpha": {
			PipelineGates: map[string]config.Checkpoints{
				"medium": {
					InquiryReview:   true,
					DesignReview:    true,
					RoadmapReview:   true,
					PhasePlanReview: true,
					ManualPublish:   true,
				},
			},
		},
	}

	m := NewWizardModel([]string{"alpha"}, nil, repoConfigs, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.selectedRepos = map[string]bool{"alpha": true}
	m.provisionalPublishable = true

	projection := m.projectedPipelineCheckpoints("medium")
	if projection.Checkpoints != (feature.Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true}) {
		t.Fatalf("projectedPipelineCheckpoints(medium).Checkpoints = %+v, want RoadmapReview+PhasePlanReview+ManualPublish", projection.Checkpoints)
	}
	if !projection.FromConfig {
		t.Fatal("expected medium repo override to be detected")
	}
	if !slices.Equal(projection.Visible, []feature.GateIndex{feature.GateRoadmapReview, feature.GatePhasePlanReview, feature.GateManualPublish}) {
		t.Fatalf("projectedPipelineCheckpoints(medium).Visible = %v, want RoadmapReview+PhasePlanReview+ManualPublish", projection.Visible)
	}
}

func TestWizardExpressPipelineCardAndResultUseProjectedGates(t *testing.T) {
	repoConfigs := map[string]config.RepoConfig{
		"alpha": {
			PipelineGates: map[string]config.Checkpoints{
				"medium": {
					InquiryReview:   true,
					DesignReview:    true,
					RoadmapReview:   true,
					PhasePlanReview: true,
					ManualPublish:   true,
				},
			},
		},
	}

	m := NewWizardModel([]string{"alpha"}, nil, repoConfigs, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("medium-projection")
	m.descInput.SetValue("projection smoke")
	m.selectedRepos = map[string]bool{"alpha": true}
	m.provisionalPublishable = true
	m.width = 200
	m.height = 40

	m, _ = m.advance()
	m, _ = m.advance()
	if m.step != wizardStepPipeline {
		t.Fatalf("expected pipeline step, got %v", m.step)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.pipelineOptions[m.pipelineCursor]; got != string(feature.PipelineMedium) {
		t.Fatalf("expected medium pipeline selection, got %q", got)
	}

	view := m.View()
	if !strings.Contains(normalizedWizardViewText(view), "Gate options: Roadmap review, Phase plan review, Publish review") {
		t.Fatalf("expected medium pipeline card to contain roadmap, phase plan, and publish options; got:\n%s", view)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != wizardStepReview {
		t.Fatalf("expected review step, got %v", m.step)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !m.IsDone() {
		t.Fatal("expected wizard to be done")
	}

	result := m.Result()
	if result == nil {
		t.Fatal("expected result to be populated")
	}
	if result.Checkpoints != (feature.Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true}) {
		t.Fatalf("WizardResult.Checkpoints = %+v, want RoadmapReview+PhasePlanReview+ManualPublish", result.Checkpoints)
	}
}

func TestWizardReviewCheckpointEditorUsesProjectedGateRows(t *testing.T) {
	repoConfigs := map[string]config.RepoConfig{
		"alpha": {
			PipelineGates: map[string]config.Checkpoints{
				"medium": {
					InquiryReview:   true,
					DesignReview:    true,
					RoadmapReview:   true,
					PhasePlanReview: true,
					ManualPublish:   true,
				},
			},
		},
	}

	m := NewWizardModel([]string{"alpha"}, nil, repoConfigs, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil)
	m.nameInput.SetValue("medium-review")
	m.descInput.SetValue("projection review")
	m.selectedRepos = map[string]bool{"alpha": true}
	m.provisionalPublishable = true
	m.width = 120
	m.height = 40

	m, _ = m.advance()
	m, _ = m.advance()
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.pipelineOptions[m.pipelineCursor]; got != string(feature.PipelineMedium) {
		t.Fatalf("expected medium pipeline selection, got %q", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.summaryCursor = summaryFieldCheckpoints
	m.summaryEditing = true

	view := m.View()
	if !strings.Contains(view, "Manual Publish") {
		t.Fatalf("review checkpoint editor missing Manual Publish:\n%s", view)
	}
	if !strings.Contains(view, "Roadmap Review") {
		t.Fatalf("review checkpoint editor missing Roadmap Review:\n%s", view)
	}
	if !strings.Contains(view, "Phase Plan Review") {
		t.Fatalf("review checkpoint editor missing Phase Plan Review:\n%s", view)
	}
	for _, hidden := range []string{"Inquiry Review", "Research Review", "Design Review"} {
		if strings.Contains(view, hidden) {
			t.Fatalf("review checkpoint editor should hide %q:\n%s", hidden, view)
		}
	}
}
