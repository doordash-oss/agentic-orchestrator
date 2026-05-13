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
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestAllHelpContextsNonEmpty(t *testing.T) {
	contexts := AllHelpContexts()
	if len(contexts) == 0 {
		t.Fatal("AllHelpContexts() returned empty map")
	}

	expectedContexts := []string{
		"Dashboard",
		"Detail Panel",
		"Detail",
		"Wizard",
		"Publish",
		"Recovery",
		"Logs",
		"Review Comments",
	}

	for _, name := range expectedContexts {
		if _, ok := contexts[name]; !ok {
			t.Errorf("missing expected context %q", name)
		}
	}
}

func TestAllHelpContextsHaveSections(t *testing.T) {
	contexts := AllHelpContexts()
	for name, ctx := range contexts {
		if len(ctx.Sections) == 0 {
			t.Errorf("context %q has no sections", name)
		}
		for _, sec := range ctx.Sections {
			if sec.Title == "" {
				t.Errorf("context %q has section with empty title", name)
			}
			if len(sec.Bindings) == 0 {
				t.Errorf("context %q section %q has no bindings", name, sec.Title)
			}
		}
	}
}

func TestKeyChatBinding(t *testing.T) {
	chatKeys := keys.Chat.Keys()
	found := false
	for _, k := range chatKeys {
		if k == "/" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("keys.Chat should be bound to '/', got %v", chatKeys)
	}
}

func TestKeyHelpOverlayBinding(t *testing.T) {
	helpKeys := keys.HelpOverlay.Keys()
	found := false
	for _, k := range helpKeys {
		if k == "?" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("keys.HelpOverlay should be bound to '?', got %v", helpKeys)
	}
}

// findSectionByTitle returns the HelpSection with the given title within a context, or nil.
func findSectionByTitle(ctx *ViewHelpContext, title string) *HelpSection {
	for i := range ctx.Sections {
		if ctx.Sections[i].Title == title {
			return &ctx.Sections[i]
		}
	}
	return nil
}

// sectionContainsKey returns true if the section has a binding with the given key string.
func sectionContainsKey(sec *HelpSection, keyStr string) bool {
	for _, b := range sec.Bindings {
		if b.Key == keyStr {
			return true
		}
	}
	return false
}

func TestDashboardContextBindings(t *testing.T) {
	contexts := AllHelpContexts()
	ctx, ok := contexts["Dashboard"]
	if !ok {
		t.Fatal("missing Dashboard context")
	}

	nav := findSectionByTitle(&ctx, "NAVIGATION")
	if nav == nil {
		t.Fatal("missing NAVIGATION section")
	}
	for _, k := range []string{"↑/k", "↓/j", "→/enter", "tab", "enter"} {
		if !sectionContainsKey(nav, k) {
			t.Errorf("NAVIGATION missing key %q", k)
		}
	}

	features := findSectionByTitle(&ctx, "FEATURES")
	if features == nil {
		t.Fatal("missing FEATURES section")
	}
	for _, k := range []string{"a", "n", "Shift+N", "d", "v", "p", "Shift+R", "Shift+A"} {
		if !sectionContainsKey(features, k) {
			t.Errorf("FEATURES missing key %q", k)
		}
	}

	tools := findSectionByTitle(&ctx, "TOOLS")
	if tools == nil {
		t.Fatal("missing TOOLS section")
	}
	for _, k := range []string{"/", "?", "q"} {
		if !sectionContainsKey(tools, k) {
			t.Errorf("TOOLS missing key %q", k)
		}
	}
}

func TestDetailPanelContextBindings(t *testing.T) {
	contexts := AllHelpContexts()
	ctx, ok := contexts["Detail Panel"]
	if !ok {
		t.Fatal("missing Detail Panel context")
	}

	nav := findSectionByTitle(&ctx, "NAVIGATION")
	if nav == nil {
		t.Fatal("missing NAVIGATION section")
	}
	for _, k := range []string{"←/esc", "tab"} {
		if !sectionContainsKey(nav, k) {
			t.Errorf("NAVIGATION missing key %q", k)
		}
	}

	actions := findSectionByTitle(&ctx, "ACTIONS")
	if actions == nil {
		t.Fatal("missing ACTIONS section")
	}
	for _, k := range []string{"a", "y", "h", "Shift+N", "r", "ctrl+r", "l", "v", "d"} {
		if !sectionContainsKey(actions, k) {
			t.Errorf("ACTIONS missing key %q", k)
		}
	}

	publish := findSectionByTitle(&ctx, "PUBLISH")
	if publish == nil {
		t.Fatal("missing PUBLISH section")
	}
	for _, k := range []string{"p", "m", "t", "b", "Shift+D", "g", "c"} {
		if !sectionContainsKey(publish, k) {
			t.Errorf("PUBLISH missing key %q", k)
		}
	}

	tools := findSectionByTitle(&ctx, "TOOLS")
	if tools == nil {
		t.Fatal("missing TOOLS section")
	}
	for _, k := range []string{"/", "?"} {
		if !sectionContainsKey(tools, k) {
			t.Errorf("TOOLS missing key %q", k)
		}
	}
}

func TestWizardContextBindings(t *testing.T) {
	contexts := AllHelpContexts()
	ctx, ok := contexts["Wizard"]
	if !ok {
		t.Fatal("missing Wizard context")
	}

	nav := findSectionByTitle(&ctx, "NAVIGATION")
	if nav == nil {
		t.Fatal("missing NAVIGATION section")
	}
	for _, k := range []string{"enter", "shift+tab", "esc"} {
		if !sectionContainsKey(nav, k) {
			t.Errorf("NAVIGATION missing key %q", k)
		}
	}

	input := findSectionByTitle(&ctx, "INPUT")
	if input == nil {
		t.Fatal("missing INPUT section")
	}
	for _, k := range []string{"tab", "ctrl+v"} {
		if !sectionContainsKey(input, k) {
			t.Errorf("INPUT missing key %q", k)
		}
	}
}

func TestRecoveryContextBindings(t *testing.T) {
	contexts := AllHelpContexts()
	ctx, ok := contexts["Recovery"]
	if !ok {
		t.Fatal("missing Recovery context")
	}

	actions := findSectionByTitle(&ctx, "ACTIONS")
	if actions == nil {
		t.Fatal("missing ACTIONS section")
	}
	for _, k := range []string{"r", "k", "s", "enter"} {
		if !sectionContainsKey(actions, k) {
			t.Errorf("ACTIONS missing key %q", k)
		}
	}
}

func TestReviewCommentsContextBindings(t *testing.T) {
	contexts := AllHelpContexts()
	ctx, ok := contexts["Review Comments"]
	if !ok {
		t.Fatal("missing Review Comments context")
	}

	actions := findSectionByTitle(&ctx, "ACTIONS")
	if actions == nil {
		t.Fatal("missing ACTIONS section")
	}
	for _, k := range []string{"Shift+A", "esc"} {
		if !sectionContainsKey(actions, k) {
			t.Errorf("ACTIONS missing key %q", k)
		}
	}
}

func TestPublishContextBindings(t *testing.T) {
	contexts := AllHelpContexts()
	ctx, ok := contexts["Publish"]
	if !ok {
		t.Fatal("missing Publish context")
	}

	actions := findSectionByTitle(&ctx, "ACTIONS")
	if actions == nil {
		t.Fatal("missing ACTIONS section")
	}
	for _, k := range []string{"enter", "tab", "esc"} {
		if !sectionContainsKey(actions, k) {
			t.Errorf("ACTIONS missing key %q", k)
		}
	}
}

func TestLogsContextBindings(t *testing.T) {
	contexts := AllHelpContexts()
	ctx, ok := contexts["Logs"]
	if !ok {
		t.Fatal("missing Logs context")
	}

	nav := findSectionByTitle(&ctx, "NAVIGATION")
	if nav == nil {
		t.Fatal("missing NAVIGATION section")
	}
	for _, k := range []string{"↑/k", "↓/j"} {
		if !sectionContainsKey(nav, k) {
			t.Errorf("NAVIGATION missing key %q", k)
		}
	}

	actions := findSectionByTitle(&ctx, "ACTIONS")
	if actions == nil {
		t.Fatal("missing ACTIONS section")
	}
	if !sectionContainsKey(actions, "p") {
		t.Errorf("ACTIONS missing key %q", "p")
	}
}

func TestDetailViewContextBindings(t *testing.T) {
	contexts := AllHelpContexts()
	ctx, ok := contexts["Detail"]
	if !ok {
		t.Fatal("missing Detail context")
	}

	nav := findSectionByTitle(&ctx, "NAVIGATION")
	if nav == nil {
		t.Fatal("missing NAVIGATION section")
	}
	if !sectionContainsKey(nav, "esc") {
		t.Errorf("NAVIGATION missing key %q", "esc")
	}

	actions := findSectionByTitle(&ctx, "ACTIONS")
	if actions == nil {
		t.Fatal("missing ACTIONS section")
	}
	for _, k := range []string{"a", "y", "h", "Shift+N", "r", "ctrl+r"} {
		if !sectionContainsKey(actions, k) {
			t.Errorf("ACTIONS missing key %q", k)
		}
	}

	publish := findSectionByTitle(&ctx, "PUBLISH")
	if publish == nil {
		t.Fatal("missing PUBLISH section")
	}
	for _, k := range []string{"p", "m", "t", "b", "Shift+D", "g", "c"} {
		if !sectionContainsKey(publish, k) {
			t.Errorf("PUBLISH missing key %q", k)
		}
	}
}

func TestApplyKeyboardLayout_Nordic(t *testing.T) {
	// Save original state
	origChatKeys := append([]string{}, keys.Chat.Keys()...)
	origDetachKeys := append([]string{}, keys.Detach.Keys()...)
	origLayout := keyboardLayout
	defer func() {
		keys.Chat.SetKeys(origChatKeys...)
		keys.Detach.SetKeys(origDetachKeys...)
		keyboardLayout = origLayout
	}()

	ApplyKeyboardLayout("nordic")

	// Assert "-" added to Chat keys
	chatKeys := keys.Chat.Keys()
	found := false
	for _, k := range chatKeys {
		if k == "-" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected '-' in Chat keys after Nordic layout, got %v", chatKeys)
	}

	// Assert "ctrl+x" added to Detach keys
	detachKeys := keys.Detach.Keys()
	found = false
	for _, k := range detachKeys {
		if k == "ctrl+x" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'ctrl+x' in Detach keys after Nordic layout, got %v", detachKeys)
	}

	// Assert hints
	if got := ChatKeyHint(); got != "-" {
		t.Errorf("ChatKeyHint() = %q, want %q", got, "-")
	}
	if got := HelpKeyHint(); got != "?" {
		t.Errorf("HelpKeyHint() = %q, want %q", got, "?")
	}
	if got := DetachKeyHint(); got != "ctrl+x/esc" {
		t.Errorf("DetachKeyHint() = %q, want %q", got, "ctrl+x/esc")
	}
}

func TestDetachKeyHint(t *testing.T) {
	origLayout := keyboardLayout
	defer func() { keyboardLayout = origLayout }()

	keyboardLayout = ""
	if got := DetachKeyHint(); got != "ctrl+]/esc" {
		t.Errorf("DetachKeyHint() default = %q, want %q", got, "ctrl+]/esc")
	}

	keyboardLayout = "nordic"
	if got := DetachKeyHint(); got != "ctrl+x/esc" {
		t.Errorf("DetachKeyHint() nordic = %q, want %q", got, "ctrl+x/esc")
	}
}

func TestApplyKeyboardLayout_Empty(t *testing.T) {
	origLayout := keyboardLayout
	defer func() {
		keyboardLayout = origLayout
	}()

	origChatKeys := append([]string{}, keys.Chat.Keys()...)
	ApplyKeyboardLayout("")

	if got := ChatKeyHint(); got != "/" {
		t.Errorf("ChatKeyHint() = %q, want %q", got, "/")
	}
	// Keys should be unchanged
	if len(keys.Chat.Keys()) != len(origChatKeys) {
		t.Errorf("Chat keys changed for empty layout")
	}
}

func TestApplyKeyboardLayout_Unknown(t *testing.T) {
	origLayout := keyboardLayout
	defer func() {
		keyboardLayout = origLayout
	}()

	origChatKeys := append([]string{}, keys.Chat.Keys()...)
	ApplyKeyboardLayout("qwerty")

	// Keys should be unchanged
	if len(keys.Chat.Keys()) != len(origChatKeys) {
		t.Errorf("Chat keys changed for unknown layout")
	}
}

func TestApplyKeyboardLayout_Idempotent(t *testing.T) {
	origChatKeys := append([]string{}, keys.Chat.Keys()...)
	origDetachKeys := append([]string{}, keys.Detach.Keys()...)
	origLayout := keyboardLayout
	defer func() {
		keys.Chat.SetKeys(origChatKeys...)
		keys.Detach.SetKeys(origDetachKeys...)
		keyboardLayout = origLayout
	}()

	ApplyKeyboardLayout("nordic")
	ApplyKeyboardLayout("nordic") // second call should be no-op

	// Count occurrences of "-" in Chat keys
	count := 0
	for _, k := range keys.Chat.Keys() {
		if k == "-" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected '-' to appear exactly once in Chat keys, found %d times", count)
	}

	// Count occurrences of "ctrl+x" in Detach keys
	count = 0
	for _, k := range keys.Detach.Keys() {
		if k == "ctrl+x" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 'ctrl+x' to appear exactly once in Detach keys, found %d times", count)
	}
}

func TestRecoveryModelKeyK_TriggersKill(t *testing.T) {
	// Regression: "k" is in both keys.Up and keys.RecoveryKill.
	// RecoveryModel must check recovery keys before navigation keys
	// so that pressing "k" sets RecoveryKill, not cursor movement.
	items := []session.RecoveryItem{
		{PIDFile: session.PIDFile{FeatureID: "feat-1", Phase: "implement", Iteration: 1}},
		{PIDFile: session.PIDFile{FeatureID: "feat-2", Phase: "plan", Iteration: 1}},
	}
	m := NewRecoveryModel(items)

	// Cursor starts at 0; default action is Skip for both items.
	if m.actions["feat-1"] != session.RecoverySkip {
		t.Fatalf("expected initial action Skip, got %v", m.actions["feat-1"])
	}

	// Press "k" — should set action to Kill, NOT move cursor up.
	msg := tea.KeyPressMsg{Code: 'k', Text: "k"}
	m, _ = m.Update(msg)

	if m.actions["feat-1"] != session.RecoveryKill {
		t.Errorf("pressing 'k' should set RecoveryKill, got action %v", m.actions["feat-1"])
	}
	if m.cursor != 0 {
		t.Errorf("pressing 'k' should not move cursor, got cursor=%d", m.cursor)
	}
}

func TestRecoveryModelArrowUp_StillNavigates(t *testing.T) {
	// After reordering, arrow-up must still work for navigation.
	items := []session.RecoveryItem{
		{PIDFile: session.PIDFile{FeatureID: "feat-1", Phase: "implement", Iteration: 1}},
		{PIDFile: session.PIDFile{FeatureID: "feat-2", Phase: "plan", Iteration: 1}},
	}
	m := NewRecoveryModel(items)

	// Move cursor to item 1 first using down-arrow.
	downMsg := tea.KeyPressMsg{Code: tea.KeyDown}
	m, _ = m.Update(downMsg)
	if m.cursor != 1 {
		t.Fatalf("expected cursor=1 after down, got %d", m.cursor)
	}

	// Press up-arrow — should navigate up.
	upMsg := tea.KeyPressMsg{Code: tea.KeyUp}
	m, _ = m.Update(upMsg)
	if m.cursor != 0 {
		t.Errorf("up-arrow should move cursor to 0, got %d", m.cursor)
	}
}

func TestRecoveryModelKeyR_TriggersResume(t *testing.T) {
	items := []session.RecoveryItem{
		{PIDFile: session.PIDFile{FeatureID: "feat-1", Phase: "implement", Iteration: 1}},
	}
	m := NewRecoveryModel(items)

	msg := tea.KeyPressMsg{Code: 'r', Text: "r"}
	m, _ = m.Update(msg)

	if m.actions["feat-1"] != session.RecoveryResume {
		t.Errorf("pressing 'r' should set RecoveryResume, got action %v", m.actions["feat-1"])
	}
}

func TestRecoveryModelKeyS_TriggersSkip(t *testing.T) {
	items := []session.RecoveryItem{
		{PIDFile: session.PIDFile{FeatureID: "feat-1", Phase: "implement", Iteration: 1}},
	}
	m := NewRecoveryModel(items)

	// Change to Kill first, then verify "s" sets Skip.
	killMsg := tea.KeyPressMsg{Code: 'k', Text: "k"}
	m, _ = m.Update(killMsg)
	if m.actions["feat-1"] != session.RecoveryKill {
		t.Fatalf("expected Kill after 'k', got %v", m.actions["feat-1"])
	}

	skipMsg := tea.KeyPressMsg{Code: 's', Text: "s"}
	m, _ = m.Update(skipMsg)
	if m.actions["feat-1"] != session.RecoverySkip {
		t.Errorf("pressing 's' should set RecoverySkip, got action %v", m.actions["feat-1"])
	}
}

func TestApproveAndRememberBinding(t *testing.T) {
	boundKeys := keys.ApproveAndRemember.Keys()
	found := false
	for _, k := range boundKeys {
		if k == "A" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("keys.ApproveAndRemember should be bound to 'A', got %v", boundKeys)
	}

	helpDesc := keys.ApproveAndRemember.Help().Desc
	if helpDesc != "approve & remember" {
		t.Errorf("ApproveAndRemember help desc = %q, want %q", helpDesc, "approve & remember")
	}
}

func TestAllHelpContexts_ApproveAndRemember(t *testing.T) {
	// Verify the rendered help overlay contexts include the "A" binding
	// in all dashboard/detail views (not just help_data.go structs).
	contexts := AllHelpContexts()

	tests := []struct {
		name    string
		context string
	}{
		{"Dashboard", "Dashboard"},
		{"Detail Panel", "Detail Panel"},
		{"Detail", "Detail"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, ok := contexts[tt.context]
			if !ok {
				t.Fatalf("AllHelpContexts() missing %q context", tt.context)
			}
			found := false
			for _, section := range ctx.Sections {
				for _, b := range section.Bindings {
					if b.Key == "Shift+A" && b.Desc == "Approve & remember permissions" {
						found = true
						break
					}
				}
			}
			if !found {
				t.Errorf("AllHelpContexts()[%q] should include 'Shift+A' binding for approve & remember permissions", tt.context)
			}
		})
	}
}
