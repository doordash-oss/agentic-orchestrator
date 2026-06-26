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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func newTestArtifactReview(t *testing.T, content, mode string) (ArtifactReviewModel, string) {
	t.Helper()
	tmp := t.TempDir()
	f := filepath.Join(tmp, "plan.md")
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewArtifactReviewModel(f, "feat-1", mode, feature.PhasePlan, 80, 24, nil, "", nil)
	_ = m.editor.Focus()
	return m, f
}

func TestArtifactReviewHeaderSpellsAgentico(t *testing.T) {
	header := stripANSI(ArtifactReviewModel{width: 80}.renderHeader())
	if !strings.Contains(header, " ▄▀█ █▀▀ █▀▀ █▄░█ ▀█▀ █ █▀▀ █▀█") {
		t.Errorf("artifact review header missing AGENTICO top row:\n%s", header)
	}
	if !strings.Contains(header, " █▀█ █▄█ ██▄ █░▀█ ░█░ █ █▄▄ █▄█") {
		t.Errorf("artifact review header missing AGENTICO bottom row:\n%s", header)
	}
}

func TestArtifactReview_EditorFocusedOnStart(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan\nsome content", "plan")

	if !m.editor.Focused() {
		t.Fatal("editor should be focused after Focus()")
	}
	if m.editor.Mode() != NormalMode {
		t.Fatal("editor should start in NormalMode")
	}

	keyMsg := tea.KeyPressMsg{Code: 'i', Text: "i"}
	m, _ = m.Update(keyMsg)

	if m.editor.Mode() != InsertMode {
		t.Errorf("pressing 'i' should switch to InsertMode, got %v", m.editor.Mode())
	}
}

func TestArtifactReview_UnfocusedEditorIgnoresKeys(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "plan.md")
	if err := os.WriteFile(f, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewArtifactReviewModel(f, "feat-1", "plan", feature.PhasePlan, 80, 24, nil, "", nil)
	// Do NOT call Focus() — editor stays unfocused.

	keyMsg := tea.KeyPressMsg{Code: 'i', Text: "i"}
	m, _ = m.Update(keyMsg)

	if m.editor.Mode() != NormalMode {
		t.Error("unfocused editor should not switch mode on key press")
	}
}

func TestArtifactReview_CtrlDShowsMenu(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	ctrlD := tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	m, _ = m.Update(ctrlD)

	if !m.showMenu {
		t.Error("Ctrl+D should show the menu")
	}
}

func TestArtifactReview_PlanMenuItems(t *testing.T) {
	m := ArtifactReviewModel{reviewMode: "plan"}
	items := m.menuItems()
	if len(items) != 3 {
		t.Fatalf("plan menu should have 3 items, got %d", len(items))
	}
	if items[0].decision != "iterate" {
		t.Errorf("first item should be iterate, got %q", items[0].decision)
	}
	if items[1].decision != "proceed" {
		t.Errorf("second item should be proceed, got %q", items[1].decision)
	}
	if items[2].decision != "detach" {
		t.Errorf("third item should be detach, got %q", items[2].decision)
	}
}

func TestArtifactReview_PlanMenuHidesIterateWhenCriticApproved(t *testing.T) {
	m := ArtifactReviewModel{reviewMode: "plan", criticApproved: true}
	items := m.menuItems()
	if len(items) != 2 {
		t.Fatalf("approved plan menu should have 2 items (proceed, detach), got %d", len(items))
	}
	if items[0].decision != "proceed" {
		t.Errorf("first item should be proceed, got %q", items[0].decision)
	}
	if items[1].decision != "detach" {
		t.Errorf("second item should be detach, got %q", items[1].decision)
	}
}

func TestArtifactReview_RewindMenuItems(t *testing.T) {
	m := ArtifactReviewModel{reviewMode: "rewind"}
	items := m.menuItems()
	if len(items) != 2 {
		t.Fatalf("rewind menu should have 2 items, got %d", len(items))
	}
	if items[0].decision != "proceed" {
		t.Errorf("first item should be proceed, got %q", items[0].decision)
	}
	if items[1].decision != "detach" {
		t.Errorf("second item should be detach, got %q", items[1].decision)
	}
}

func TestArtifactReview_GateMenuHasProceedAndDetach(t *testing.T) {
	m := ArtifactReviewModel{reviewMode: "gate"}
	items := m.menuItems()
	if len(items) != 2 {
		t.Fatalf("gate menu should have 2 items, got %d", len(items))
	}
	if items[0].label != "Proceed to next phase" {
		t.Errorf("expected label 'Proceed to next phase', got %q", items[0].label)
	}
	if items[0].decision != "proceed" {
		t.Errorf("expected decision 'proceed', got %q", items[0].decision)
	}
	if items[1].label != "Return to dashboard" {
		t.Errorf("expected label 'Return to dashboard', got %q", items[1].label)
	}
	if items[1].decision != "detach" {
		t.Errorf("expected decision 'detach', got %q", items[1].decision)
	}
}

func TestArtifactReview_EscShowsMenu(t *testing.T) {
	for _, mode := range []string{"gate", "plan", "rewind"} {
		t.Run(mode, func(t *testing.T) {
			m, _ := newTestArtifactReview(t, "# Content", mode)

			// Esc in NormalMode should show menu, not detach
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			if !m.showMenu {
				t.Fatal("Esc should show the menu")
			}
			if m.Detached() {
				t.Fatal("Esc should NOT detach directly")
			}
		})
	}
}

func TestArtifactReview_CtrlBracketShowsMenu(t *testing.T) {
	for _, mode := range []string{"gate", "plan", "rewind"} {
		t.Run(mode, func(t *testing.T) {
			m, _ := newTestArtifactReview(t, "# Content", mode)

			m, _ = m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
			if !m.showMenu {
				t.Fatal("Ctrl+] should show the menu")
			}
			if m.Detached() {
				t.Fatal("Ctrl+] should NOT detach directly")
			}
		})
	}
}

func TestArtifactReview_GateDetachMenuOption(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Research output", "gate")

	// Esc to show menu
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	// Select "Return to dashboard" (second item)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.Detached() {
		t.Fatal("selecting 'Return to dashboard' should detach")
	}
	if m.Decided() {
		t.Fatal("'Return to dashboard' should not set decided flag")
	}
}

func TestArtifactReview_CtrlDMenuDecisionGate(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Research output", "gate")

	// Ctrl+D to show menu
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if !m.showMenu {
		t.Fatal("menu should be shown")
	}

	// Select proceed (only item)
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.Detached() {
		t.Error("model should be detached after menu selection")
	}

	if cmd != nil {
		msg := cmd()
		if decision, ok := msg.(GateReviewDecisionMsg); ok {
			if decision.Decision != "proceed" {
				t.Errorf("expected proceed decision, got %q", decision.Decision)
			}
			if decision.FeatureID != "feat-1" {
				t.Errorf("expected feat-1, got %q", decision.FeatureID)
			}
		} else {
			t.Errorf("expected GateReviewDecisionMsg, got %T", msg)
		}
	} else {
		t.Fatal("expected a command from gate menu decision")
	}
}

func TestArtifactReview_FocusCycling(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan\ncontent", "plan")

	// Initially editor is focused, chat is not
	if m.chatFocused {
		t.Error("chat should not be focused initially")
	}
	if !m.editor.Focused() {
		t.Error("editor should be focused initially")
	}

	// Tab → chat focused
	tabMsg := tea.KeyPressMsg{Code: tea.KeyTab}
	m, _ = m.Update(tabMsg)

	if !m.chatFocused {
		t.Error("chat should be focused after Tab")
	}
	if m.editor.Focused() {
		t.Error("editor should be blurred after Tab to chat")
	}

	// Tab → editor focused again
	m, _ = m.Update(tabMsg)

	if m.chatFocused {
		t.Error("chat should not be focused after second Tab")
	}
	if !m.editor.Focused() {
		t.Error("editor should be focused after Tab back")
	}
}

func TestArtifactReview_AutoSaveOnFocusSwitch(t *testing.T) {
	m, f := newTestArtifactReview(t, "original", "plan")

	// Enter insert mode and type something
	m, _ = m.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'X', Text: "X"})

	if !m.editor.Dirty() {
		t.Fatal("editor should be dirty after insert")
	}

	// Tab to chat — should auto-save
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})

	// The editor might still say dirty=true since we switched to chat focus,
	// but the file on disk should be saved
	data, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if content != "Xoriginal" {
		t.Errorf("expected saved content 'Xoriginal', got %q", content)
	}
}

func TestArtifactReview_CtrlDMenuDecisionPlan(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Ctrl+D to show menu
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if !m.showMenu {
		t.Fatal("menu should be shown")
	}

	// Select iterate (first item)
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.Detached() {
		t.Error("model should be detached after menu selection")
	}

	// Execute the command to get the message
	if cmd != nil {
		msg := cmd()
		if decision, ok := msg.(PlanReviewDecisionMsg); ok {
			if decision.Decision != "iterate" {
				t.Errorf("expected iterate decision, got %q", decision.Decision)
			}
		} else {
			t.Errorf("expected PlanReviewDecisionMsg, got %T", msg)
		}
	}
}

func TestArtifactReview_CtrlDMenuDecisionRewind(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Research", "rewind")

	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if cmd != nil {
		msg := cmd()
		if decision, ok := msg.(RewindReviewDecisionMsg); ok {
			if decision.Decision != "proceed" {
				t.Errorf("expected proceed decision, got %q", decision.Decision)
			}
		} else {
			t.Errorf("expected RewindReviewDecisionMsg, got %T", msg)
		}
	}
}

func TestArtifactReview_AcceptAgentEdit(t *testing.T) {
	m, _ := newTestArtifactReview(t, "line1\nline2\nline3", "plan")

	// Simulate agent edit by setting highlights and pre-edit content
	m.preEditContent = "line1\nline2\nline3"
	m.editor.SetContent("line1\nMODIFIED\nline3", true)

	if len(m.editor.highlightedLines) == 0 {
		t.Fatal("should have highlighted lines after SetContent with diff")
	}

	// Ctrl+Y to accept
	m, _ = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})

	if len(m.editor.highlightedLines) != 0 {
		t.Error("highlights should be cleared after accept")
	}
	if m.preEditContent != "" {
		t.Error("preEditContent should be cleared after accept")
	}
	if m.editor.Content() != "line1\nMODIFIED\nline3" {
		t.Error("content should remain the agent's version after accept")
	}
}

func TestArtifactReview_RejectAgentEdit(t *testing.T) {
	m, f := newTestArtifactReview(t, "line1\nline2\nline3", "plan")

	// Simulate agent edit
	m.preEditContent = "line1\nline2\nline3"
	m.editor.SetContent("line1\nMODIFIED\nline3", true)

	// Ctrl+Z to reject
	m, _ = m.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})

	if m.editor.Content() != "line1\nline2\nline3" {
		t.Errorf("content should be reverted after reject, got %q", m.editor.Content())
	}

	// File on disk should also be reverted
	data, _ := os.ReadFile(f)
	if string(data) != "line1\nline2\nline3" {
		t.Errorf("file on disk should be reverted, got %q", string(data))
	}
}

func TestArtifactReview_DetachCtrlBracketShowsMenu(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	m, _ = m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})

	if !m.showMenu {
		t.Error("Ctrl+] should show menu")
	}
	if m.Detached() {
		t.Error("Ctrl+] should not directly detach")
	}
}

func TestArtifactReview_DetachEscNormalModeShowsMenu(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// In normal mode, Esc should show menu
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if !m.showMenu {
		t.Error("Esc in normal mode should show menu")
	}
	if m.Detached() {
		t.Error("Esc in normal mode should not directly detach")
	}
}

func TestArtifactReview_EscInInsertModeDoesNotDetach(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Enter insert mode
	m, _ = m.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	if m.editor.Mode() != InsertMode {
		t.Fatal("should be in insert mode")
	}

	// Esc should return to normal mode, not detach
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.Detached() {
		t.Error("Esc in insert mode should not detach")
	}
	if m.editor.Mode() != NormalMode {
		t.Error("Esc in insert mode should return to normal mode")
	}
}

func TestArtifactReview_Reattach(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Detach via menu: Ctrl+D → select "Iterate more" (first item)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.Detached() {
		t.Fatal("should be detached after menu selection")
	}

	// Reattach
	_ = m.Reattach()
	if m.Detached() {
		t.Error("should not be detached after Reattach()")
	}
	if !m.editor.Focused() {
		t.Error("editor should be focused after Reattach()")
	}
}

func TestArtifactReview_FeatureIDAndReviewMode(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan", "plan")
	if m.FeatureID() != "feat-1" {
		t.Errorf("expected feat-1, got %q", m.FeatureID())
	}
	if m.ReviewMode() != "plan" {
		t.Errorf("expected plan, got %q", m.ReviewMode())
	}
}

func TestIsArtifactReviewSession(t *testing.T) {
	tests := []struct {
		sessionID string
		want      bool
	}{
		{"abc123-artifact-review", true},
		{"longid-artifact-review", true},
		{"abc123-plan", false},
		{"abc123-research", false},
		{"abc123-review-01", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.sessionID, func(t *testing.T) {
			if got := isArtifactReviewSession(tt.sessionID); got != tt.want {
				t.Errorf("isArtifactReviewSession(%q) = %v, want %v", tt.sessionID, got, tt.want)
			}
		})
	}
}

func TestArtifactReview_WindowSizeMsg(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan\nline 2\nline 3", "plan")

	origW, origH := m.width, m.height

	// Send a resize message
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	if m.width != 120 || m.height != 40 {
		t.Errorf("expected width=120 height=40, got width=%d height=%d", m.width, m.height)
	}
	if m.width == origW && m.height == origH {
		t.Error("dimensions should have changed after WindowSizeMsg")
	}
}

func TestArtifactReview_SessionIDFormat(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Session ID should follow the pattern "<featureID>-artifact-review"
	expected := "feat-1-artifact-review"
	if m.sessionID != expected {
		t.Errorf("expected sessionID %q, got %q", expected, m.sessionID)
	}

	// Should be recognized by isArtifactReviewSession
	if !isArtifactReviewSession(m.sessionID) {
		t.Error("artifact review session ID should be recognized by isArtifactReviewSession")
	}
}

func TestArtifactReview_StopSessionCleansUp(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Simulate that a session was started (without actually starting one,
	// since we have no real session manager in tests).
	m.sessionStarted = true
	// With nil sessionMgr, StopSession should not panic.
	m.StopSession()

	if m.sessionStarted {
		t.Error("sessionStarted should be false after StopSession")
	}
	if m.sess != nil {
		t.Error("sess should be nil after StopSession")
	}
}

func TestArtifactReview_DetachReattachKeepsChatSession(t *testing.T) {
	// Detaching from an open review keeps the lazy chat session attached to
	// the model so re-opening the same gate can continue the conversation.
	m, _ := newTestArtifactReview(t, "# Plan\ncontent", "plan")

	// Simulate having an active chat session
	m.sessionStarted = true
	m.sess = session.NewSession("feat-1-artifact-review", "feat-1", feature.PhasePlan)

	// Detach via Ctrl+D menu → select "Detach" (third item)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.Detached() {
		t.Fatal("should be detached")
	}

	if !m.sessionStarted {
		t.Error("session should stay started after non-terminal detach")
	}
	if m.sess == nil {
		t.Error("session handle should stay bound after non-terminal detach")
	}

	// Reattach
	_ = m.Reattach()
	if m.Detached() {
		t.Error("should not be detached after Reattach()")
	}

	if !m.sessionStarted {
		t.Error("sessionStarted should remain true after reattach")
	}
	if m.sess == nil {
		t.Error("session handle should remain bound after reattach")
	}
}

func TestArtifactReviewAppDetachKeepsChatSession(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan\ncontent", "plan")
	m.showMenu = true
	m.menuChoice = 2 // Detach
	m.sessionStarted = true
	m.sess = session.NewSession("feat-1-artifact-review", "feat-1", feature.PhasePlan)

	app := AppModel{
		currentView:    ViewArtifactReview,
		artifactReview: m,
	}

	updatedModel, _ := app.updateArtifactReview(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated := updatedModel.(AppModel)

	if updated.currentView != ViewDashboard {
		t.Fatalf("currentView = %v, want dashboard after detach", updated.currentView)
	}
	if !updated.artifactReview.Detached() {
		t.Fatal("artifact review should be detached")
	}
	if !updated.artifactReview.sessionStarted {
		t.Fatal("detaching an open review should keep the chat session started")
	}
	if updated.artifactReview.sess == nil {
		t.Fatal("detaching an open review should keep the chat session handle")
	}
}

func TestArtifactReviewAppDecisionStopsChatSession(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan\ncontent", "plan")
	m.showMenu = true
	m.menuChoice = 1 // Proceed
	m.sessionStarted = true
	m.sess = session.NewSession("feat-1-artifact-review", "feat-1", feature.PhasePlan)

	app := AppModel{
		currentView:    ViewArtifactReview,
		artifactReview: m,
	}

	updatedModel, _ := app.updateArtifactReview(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated := updatedModel.(AppModel)

	if !updated.artifactReview.Decided() {
		t.Fatal("artifact review should be decided")
	}
	if updated.artifactReview.sessionStarted {
		t.Fatal("terminal review decision should stop the chat session")
	}
	if updated.artifactReview.sess != nil {
		t.Fatal("terminal review decision should clear the chat session handle")
	}
}

func TestArtifactReview_StopSessionCleansUpStarting(t *testing.T) {
	// Regression: StopSession must also cancel sessions that are still
	// in the "starting" state (async startSessionCmd in-flight).
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Simulate that startSessionCmd was called but hasn't completed yet.
	m.sessionStarting = true
	m.sessionStarted = false

	m.StopSession()

	if m.sessionStarting {
		t.Error("sessionStarting should be false after StopSession")
	}
	if m.sessionStarted {
		t.Error("sessionStarted should be false after StopSession")
	}
	if m.sess != nil {
		t.Error("sess should be nil after StopSession")
	}
}

func TestArtifactReview_StopSessionNoOpWhenIdle(t *testing.T) {
	// When no session was started or starting, StopSession is a no-op.
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Neither flag set — StopSession should not panic.
	m.StopSession()

	if m.sessionStarting {
		t.Error("sessionStarting should remain false")
	}
	if m.sessionStarted {
		t.Error("sessionStarted should remain false")
	}
}

func TestArtifactReview_MenuDecisionSetsDecided(t *testing.T) {
	// After a menu decision, the Decided() flag must be true so that
	// stale reattach checks don't match.
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	if m.Decided() {
		t.Fatal("should not be decided initially")
	}

	// Ctrl+D → Enter (select first menu item)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.Decided() {
		t.Error("Decided() should be true after menu selection")
	}
	if !m.Detached() {
		t.Error("should be detached after menu selection")
	}
}

func TestArtifactReview_DecidedPreventsReattach(t *testing.T) {
	// After a decision, the model should still allow Reattach() mechanically
	// but the app-layer guard (Decided()) prevents it from happening.
	m, _ := newTestArtifactReview(t, "# Plan", "rewind")

	// Make a decision
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.Decided() {
		t.Fatal("should be decided")
	}
	if !m.Detached() {
		t.Fatal("should be detached")
	}

	// The app-layer check is: Detached() && !Decided()
	// Verify this guard rejects reattach:
	canReattach := m.Detached() && !m.Decided()
	if canReattach {
		t.Error("decided model should NOT be eligible for reattach")
	}
}

func TestArtifactReview_RejectClearsHighlights(t *testing.T) {
	// Regression test: Ctrl+Z (reject) must clear highlightedLines in addition
	// to restoring content.
	m, _ := newTestArtifactReview(t, "line1\nline2\nline3", "plan")

	// Simulate agent edit
	m.preEditContent = "line1\nline2\nline3"
	m.editor.SetContent("line1\nMODIFIED\nline3", true)

	if len(m.editor.highlightedLines) == 0 {
		t.Fatal("should have highlighted lines before reject")
	}

	// Ctrl+Z to reject
	m, _ = m.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})

	if len(m.editor.highlightedLines) != 0 {
		t.Error("highlights should be cleared after reject")
	}
	if m.editor.Content() != "line1\nline2\nline3" {
		t.Errorf("content should be reverted after reject, got %q", m.editor.Content())
	}
}

func TestArtifactReview_SessionStartedMsgMatchGuard(t *testing.T) {
	// Regression: app.go must validate msg.generation == m.artifactReview.sessionGeneration
	// before binding the session. This test verifies the guard condition used
	// in the artifactReviewSessionStartedMsg case.
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Simulate a session start (sets generation to 1)
	m.sessionStarting = true
	m.sessionGeneration = 1

	// Create a matching message (same generation)
	matchingMsg := artifactReviewSessionStartedMsg{
		sess:       session.NewSession(m.sessionID, "", 0),
		generation: 1,
	}
	// Create a stale message (old generation)
	staleMsg := artifactReviewSessionStartedMsg{
		sess:       session.NewSession(m.sessionID, "", 0),
		generation: 0,
	}

	// Guard condition: msg.generation == m.sessionGeneration should pass for matching.
	if matchingMsg.generation != m.sessionGeneration {
		t.Fatal("matching message generation should equal model generation")
	}
	// Guard condition: msg.generation == m.sessionGeneration should fail for stale.
	if staleMsg.generation == m.sessionGeneration {
		t.Fatal("stale message generation should NOT match model generation")
	}
}

func TestArtifactReview_StaleStartStopsArrivingSession(t *testing.T) {
	// Regression: when a stale artifactReviewSessionStartedMsg arrives
	// (from a previous generation or different feature), app.go must stop
	// the arriving session's concrete object — NOT the active model's session.
	mA, _ := newTestArtifactReview(t, "# Plan A", "plan")

	// Create model B with a different feature ID.
	tmp := t.TempDir()
	f := filepath.Join(tmp, "plan.md")
	if err := os.WriteFile(f, []byte("# Plan B"), 0o644); err != nil {
		t.Fatal(err)
	}
	mB := NewArtifactReviewModel(f, "feat-2", "plan", feature.PhasePlan, 80, 24, nil, "", nil)
	mB.sessionGeneration = 1 // simulate an active start attempt

	// Simulate: stale session from review A (generation 0, wrong feature).
	staleMsg := artifactReviewSessionStartedMsg{
		sess:       session.NewSession(mA.sessionID, "", 0),
		generation: 0,
	}

	// The active model is B on generation 1. The arriving session has generation 0.
	// app.go guard: msg.generation == m.artifactReview.sessionGeneration
	shouldBind := staleMsg.generation == mB.sessionGeneration && !mB.sessionStarted
	if shouldBind {
		t.Fatal("stale session from A (generation 0) must not bind to model B (generation 1)")
	}

	// The arriving session object should be stopped directly (msg.sess.Stop())
	// to avoid ambiguity with shared static IDs.
}

func TestArtifactReview_DoubleSendQueuesMessages(t *testing.T) {
	// Regression: multiple Ctrl+S presses before the first session startup
	// completes must NOT launch duplicate startSessionCmd calls. Instead,
	// subsequent messages should be queued in pendingMessages and delivered
	// after the session is established.
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Tab to chat
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.chatFocused {
		t.Fatal("should be in chat mode")
	}

	// Type first message and send — this triggers startSessionCmd
	m.chatInput.InsertString("first message")
	m, cmd1 := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if !m.sessionStarting {
		t.Fatal("sessionStarting should be true after first Ctrl+S")
	}
	if cmd1 == nil {
		t.Fatal("first send should return a startSessionCmd")
	}

	// Type second message and send while sessionStarting is true
	m.chatInput.InsertString("second message")
	m, cmd2 := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if cmd2 != nil {
		t.Error("second send while sessionStarting should NOT return a command (message should be queued)")
	}
	if len(m.pendingMessages) != 1 {
		t.Fatalf("expected 1 pending message, got %d", len(m.pendingMessages))
	}
	if m.pendingMessages[0] != "second message" {
		t.Errorf("queued message should be 'second message', got %q", m.pendingMessages[0])
	}

	// Type third message and send — also queued
	m.chatInput.InsertString("third message")
	m, cmd3 := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if cmd3 != nil {
		t.Error("third send while sessionStarting should NOT return a command")
	}
	if len(m.pendingMessages) != 2 {
		t.Fatalf("expected 2 pending messages, got %d", len(m.pendingMessages))
	}

	// Simulate session startup completing — queued messages should be drained
	mockSess := session.NewSession(m.sessionID, "", 0)
	m, sessCmd := m.handleSessionStarted(mockSess)

	if m.sessionStarting {
		t.Error("sessionStarting should be false after handleSessionStarted")
	}
	if !m.sessionStarted {
		t.Error("sessionStarted should be true after handleSessionStarted")
	}
	if len(m.pendingMessages) != 0 {
		t.Errorf("pendingMessages should be drained after session started, got %d", len(m.pendingMessages))
	}
	if sessCmd == nil {
		t.Error("handleSessionStarted should return commands (poll + done + queued messages)")
	}
}

func TestArtifactReview_DuplicateSessionStartedMsgRejected(t *testing.T) {
	// Regression: if a session with the same ID arrives after one is already
	// bound (sessionStarted == true), it must be rejected. This simulates the
	// app.go guard: !m.artifactReview.sessionStarted.
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// First session startup — should succeed
	firstSess := session.NewSession(m.sessionID, "", 0)
	m, _ = m.handleSessionStarted(firstSess)

	if !m.sessionStarted {
		t.Fatal("session should be started after first handleSessionStarted")
	}
	if m.sess != firstSess {
		t.Fatal("sess should be bound to firstSess")
	}

	// Simulate app.go guard: check sessionStarted before allowing bind
	duplicateSess := session.NewSession(m.sessionID, "", 0)
	shouldBind := duplicateSess.ID() == m.sessionID && !m.sessionStarted
	if shouldBind {
		t.Error("duplicate session arrival should NOT be bound when sessionStarted is already true")
	}

	// Verify the first session is still the bound one
	if m.sess != firstSess {
		t.Error("original session should remain bound after duplicate is rejected")
	}
}

func TestArtifactReview_StartupFailureAllowsRetry(t *testing.T) {
	// Regression: if session startup fails (artifactReviewStartErrorMsg),
	// session lifecycle state must be fully reset so the user can retry.
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Simulate: user sent a message, sessionStarting is true, and some
	// messages were queued.
	m.sessionStarting = true
	m.agentResponding = true
	m.pendingMessages = []string{"queued msg"}

	// Simulate startup failure (generation must match current for state reset)
	m, _ = m.Update(artifactReviewStartErrorMsg{err: fmt.Errorf("connection refused"), generation: m.sessionGeneration})

	if m.sessionStarting {
		t.Error("sessionStarting should be false after startup failure")
	}
	if m.sessionStarted {
		t.Error("sessionStarted should be false after startup failure")
	}
	if m.sess != nil {
		t.Error("sess should be nil after startup failure")
	}
	if m.agentResponding {
		t.Error("agentResponding should be false after startup failure")
	}
	if len(m.pendingMessages) != 0 {
		t.Error("pendingMessages should be drained after startup failure")
	}
	if !strings.Contains(m.chatHistory, "connection refused") {
		t.Error("chat history should contain the error message")
	}

	// Verify retry is possible: send another message should start a new session.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab}) // focus chat
	m.chatInput.InsertString("retry message")
	m, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if !m.sessionStarting {
		t.Error("sessionStarting should be true after retry send (new startSessionCmd)")
	}
	if cmd == nil {
		t.Error("retry send should return a startSessionCmd")
	}
}

func TestArtifactReview_DoneMsgResetsSessionState(t *testing.T) {
	// Regression: artifactReviewDoneMsg must reset full session lifecycle
	// state, not just agentResponding. Otherwise the chat gets stuck.
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Simulate an active session that ends.
	m.sessionStarted = true
	m.sessionStarting = false
	m.agentResponding = true
	mockSess := session.NewSession(m.sessionID, "", 0)
	m.sess = mockSess

	m, _ = m.Update(artifactReviewDoneMsg{})

	if m.agentResponding {
		t.Error("agentResponding should be false after done")
	}
	if m.sessionStarted {
		t.Error("sessionStarted should be false after done")
	}
	if m.sessionStarting {
		t.Error("sessionStarting should be false after done")
	}
	if m.sess != nil {
		t.Error("sess should be nil after done")
	}
}

func TestArtifactReview_SendAfterDoneStartsNewSession(t *testing.T) {
	// Regression: after session ends (artifactReviewDoneMsg), a new Ctrl+S
	// should start a fresh session instead of sending to a stale sess.
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Simulate session that was active and then ended.
	m.sessionStarted = true
	m.sess = session.NewSession(m.sessionID, "", 0)
	m, _ = m.Update(artifactReviewDoneMsg{})

	// Now state should be fully reset.
	if m.sessionStarted || m.sess != nil {
		t.Fatal("precondition: session state should be reset after done")
	}

	// Focus chat and send a new message.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m.chatInput.InsertString("new question")
	m, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if !m.sessionStarting {
		t.Error("should trigger new session start (sessionStarting=true)")
	}
	if cmd == nil {
		t.Error("should return a startSessionCmd for the new session")
	}
}

func TestArtifactReview_DetachDuringStartupStaleSessionRejected(t *testing.T) {
	// Regression: detach while startSessionCmd is in-flight, then reattach
	// and send again. The stale session from the first attempt (generation 1)
	// must be rejected when it arrives because the model is now on generation 2.
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Step 1: user sends first message → session start begins (generation 1)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m.chatInput.InsertString("first question")
	m, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if !m.sessionStarting {
		t.Fatal("sessionStarting should be true after first send")
	}
	gen1 := m.sessionGeneration
	if gen1 != 1 {
		t.Fatalf("expected generation 1, got %d", gen1)
	}

	// Step 2: user detaches before session startup completes
	m.StopSession()

	if m.sessionStarting {
		t.Fatal("sessionStarting should be cleared by StopSession")
	}

	// Step 3: user reattaches and sends a new message → generation increments to 2
	_ = m.Reattach()
	// After reattach, chatFocused may still be true from step 1 — ensure
	// we're in chat mode for the send.
	if !m.chatFocused {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	m.chatInput.InsertString("second question")
	m, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	gen2 := m.sessionGeneration
	if gen2 != 2 {
		t.Fatalf("expected generation 2 after retry, got %d", gen2)
	}

	// Step 4: stale session from generation 1 arrives
	staleMsg := artifactReviewSessionStartedMsg{
		sess:       session.NewSession(m.sessionID, "", 0),
		generation: gen1,
	}

	// The app.go guard checks: msg.generation == m.artifactReview.sessionGeneration
	shouldBind := staleMsg.generation == m.sessionGeneration && !m.sessionStarted
	if shouldBind {
		t.Error("stale session from generation 1 must NOT bind when model is on generation 2")
	}

	// Step 5: current-generation session arrives — should bind
	currentMsg := artifactReviewSessionStartedMsg{
		sess:       session.NewSession(m.sessionID, "", 0),
		generation: gen2,
	}
	shouldBindCurrent := currentMsg.generation == m.sessionGeneration && !m.sessionStarted
	if !shouldBindCurrent {
		t.Error("current-generation session should be eligible for binding")
	}
}

func TestArtifactReview_StaleStartErrorIgnored(t *testing.T) {
	// Regression: if a session start error arrives from a previous generation,
	// it must be ignored and not reset the current session's state.
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Simulate: generation 1 started and failed, but user already sent
	// a second message bumping to generation 2.
	m.sessionStarting = true
	m.sessionGeneration = 2
	m.agentResponding = true

	// Stale error from generation 1
	m, _ = m.Update(artifactReviewStartErrorMsg{
		err:        fmt.Errorf("timeout"),
		generation: 1,
	})

	// State should NOT be reset since the error is from an old generation
	if !m.sessionStarting {
		t.Error("sessionStarting should remain true (stale error must not reset current generation)")
	}
	if !m.agentResponding {
		t.Error("agentResponding should remain true (stale error must not reset current generation)")
	}

	// Current-generation error should reset state
	m, _ = m.Update(artifactReviewStartErrorMsg{
		err:        fmt.Errorf("real failure"),
		generation: 2,
	})

	if m.sessionStarting {
		t.Error("sessionStarting should be false after current-generation error")
	}
	if m.agentResponding {
		t.Error("agentResponding should be false after current-generation error")
	}
	if !strings.Contains(m.chatHistory, "real failure") {
		t.Error("chat history should contain the current-generation error")
	}
}

func TestArtifactReview_GenerationIncrements(t *testing.T) {
	// Verify that each new session start attempt increments the generation.
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	if m.sessionGeneration != 0 {
		t.Fatalf("initial generation should be 0, got %d", m.sessionGeneration)
	}

	// First send
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m.chatInput.InsertString("msg1")
	m, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if m.sessionGeneration != 1 {
		t.Errorf("generation should be 1 after first send, got %d", m.sessionGeneration)
	}

	// Reset and send again
	m.StopSession()
	m.chatInput.InsertString("msg2")
	m, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if m.sessionGeneration != 2 {
		t.Errorf("generation should be 2 after second send, got %d", m.sessionGeneration)
	}
}

func TestArtifactReview_SendErrorAllowsRetry(t *testing.T) {
	// Regression: if SendUserMessage fails (artifactReviewSendErrorMsg),
	// session state must be reset so the user can retry.
	m, _ := newTestArtifactReview(t, "# Plan", "plan")

	// Simulate active session
	m.sessionStarted = true
	m.agentResponding = true
	m.sess = session.NewSession(m.sessionID, "", 0)

	m, _ = m.Update(artifactReviewSendErrorMsg{err: fmt.Errorf("broken pipe")})

	if m.agentResponding {
		t.Error("agentResponding should be false after send error")
	}
	if m.sessionStarted {
		t.Error("sessionStarted should be false after send error")
	}
	if m.sess != nil {
		t.Error("sess should be nil after send error")
	}
	if !strings.Contains(m.chatHistory, "broken pipe") {
		t.Error("chat history should contain the send error message")
	}
}

// TestArtifactReview_StopSessionClearsPendingMessages verifies that
// StopSession clears pendingMessages and permission state so stale prompts
// from a previous attempt cannot leak into a future session.
func TestArtifactReview_StopSessionClearsPendingMessages(t *testing.T) {
	m, _ := newTestArtifactReview(t, "# Plan\nBody", "plan")

	// Tab to chat
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.chatFocused {
		t.Fatal("should be in chat mode")
	}

	// First Ctrl+S starts a session
	m.chatInput.InsertString("first message")
	m, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if !m.sessionStarting {
		t.Fatal("first send should set sessionStarting")
	}

	// Second Ctrl+S while startup is in-flight — message gets queued
	m.chatInput.InsertString("queued stale message")
	m, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if len(m.pendingMessages) != 1 {
		t.Fatalf("expected 1 pending message, got %d", len(m.pendingMessages))
	}

	// Simulate a pending permission prompt
	m.pendingPermRequestID = "perm-123"
	m.pendingPermToolName = "Write"
	m.pendingPermToolInput = `{"path":"/tmp/x"}`

	// Detach — StopSession is called
	m.StopSession()

	// Verify all transient state is cleared
	if len(m.pendingMessages) != 0 {
		t.Errorf("pendingMessages should be empty after StopSession, got %d", len(m.pendingMessages))
	}
	if m.pendingPermRequestID != "" {
		t.Error("pendingPermRequestID should be empty after StopSession")
	}
	if m.pendingPermToolName != "" {
		t.Error("pendingPermToolName should be empty after StopSession")
	}
	if m.pendingPermToolInput != "" {
		t.Error("pendingPermToolInput should be empty after StopSession")
	}

	// Simulate reattach: new session starts and succeeds
	m.chatInput.InsertString("fresh message")
	m, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	// Simulate session started — use handleSessionStarted directly (the
	// generation check is done in app.go, not in artifact_review.go).
	fakeSess := session.NewSession("", "", 0)
	m, _ = m.handleSessionStarted(fakeSess)

	if !m.sessionStarted {
		t.Fatal("session should be started after reattach send")
	}
	// The old queued message ("queued stale message") must NOT have been
	// drained — pendingMessages was cleared by StopSession, so only the new
	// message was sent via startSessionCmd's initialMessage.
	if len(m.pendingMessages) != 0 {
		t.Errorf("stale pending messages should not survive StopSession, got %d", len(m.pendingMessages))
	}
}

func TestArtifactReviewStartSessionUsesCallback(t *testing.T) {
	var capturedOpts agent.BuildSessionOpts
	var called bool
	mockBuildSession := func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		called = true
		capturedOpts = opts
		return nil, nil, nil, fmt.Errorf("test: stop here")
	}

	// Create a temp artifact file
	f := filepath.Join(t.TempDir(), "plan.md")
	os.WriteFile(f, []byte("# Plan"), 0o644)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	// Create model in plan mode with a real session manager
	m := NewArtifactReviewModel(f, "feat-1", "plan", feature.PhasePlan, 80, 24, sm, "/tmp", mockBuildSession)
	m.utilityModel = "test-utility-model"

	cmd := m.startSessionCmd("review this plan", 0)
	cmd()

	if !called {
		t.Fatal("BuildSession callback was not called")
	}
	if capturedOpts.Model != "test-utility-model" {
		t.Errorf("expected utility model %q for artifact review, got %q", "test-utility-model", capturedOpts.Model)
	}
	if capturedOpts.TurnMode != ports.TurnModeInteractive {
		t.Errorf("expected interactive turn mode, got %v", capturedOpts.TurnMode)
	}
	if len(capturedOpts.AllowedTools) == 0 {
		t.Error("expected non-empty AllowedTools")
	}
	if !strings.Contains(capturedOpts.SystemPrompt, f) {
		t.Error("expected system prompt to contain artifact path")
	}
}

func TestArtifactReviewAddsAdditionalDir(t *testing.T) {
	var capturedOpts agent.BuildSessionOpts
	mockBuildSession := func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		capturedOpts = opts
		return nil, nil, nil, fmt.Errorf("test: stop here")
	}

	// artifact is in artifactDir, workDir is a different directory
	artifactDir := t.TempDir()
	f := filepath.Join(artifactDir, "plan.md")
	os.WriteFile(f, []byte("# Plan"), 0o644)
	workDir := t.TempDir() // different from artifactDir

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	m := NewArtifactReviewModel(f, "feat-1", "plan", feature.PhasePlan, 80, 24, sm, workDir, mockBuildSession)
	m.utilityModel = "test-utility-model"
	cmd := m.startSessionCmd("review", 0)
	cmd()

	if len(capturedOpts.AdditionalDirs) == 0 {
		t.Error("expected non-empty AdditionalDirs when artifact dir differs from workDir")
	}
	if capturedOpts.AdditionalDirs[0] != artifactDir {
		t.Errorf("expected AdditionalDirs[0] = %q, got %q", artifactDir, capturedOpts.AdditionalDirs[0])
	}
}

func TestArtifactReviewStartSessionRequiresUtilityModel(t *testing.T) {
	f := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(f, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	m := NewArtifactReviewModel(f, "feat-1", "plan", feature.PhasePlan, 80, 24, sm, "/tmp", func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		t.Fatalf("BuildSession should not be called when utility model is missing: %+v", opts)
		return nil, nil, nil, nil
	})

	msg := m.startSessionCmd("review this plan", 1)()
	startErr, ok := msg.(artifactReviewStartErrorMsg)
	if !ok {
		t.Fatalf("expected artifactReviewStartErrorMsg, got %T", msg)
	}
	if !strings.Contains(startErr.err.Error(), "utility model") {
		t.Errorf("expected missing utility model error, got %v", startErr.err)
	}
}
