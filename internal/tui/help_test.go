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
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestAllHelpContexts(t *testing.T) {
	contexts := AllHelpContexts()
	expectedNames := []string{
		"Dashboard", "Detail Panel", "Detail", "Wizard",
		"Publish", "Recovery", "Logs", "Review Comments",
	}
	for _, name := range expectedNames {
		t.Run(name, func(t *testing.T) {
			ctx, ok := contexts[name]
			if !ok {
				t.Fatalf("AllHelpContexts() missing context %q", name)
			}
			if ctx.Name != name {
				t.Errorf("context[%q].Name = %q, want %q", name, ctx.Name, name)
			}
			if len(ctx.Sections) == 0 {
				t.Errorf("context[%q] has no sections", name)
			}
			for _, s := range ctx.Sections {
				if s.Title == "" {
					t.Errorf("context[%q] has a section with empty title", name)
				}
				if len(s.Bindings) == 0 {
					t.Errorf("context[%q] section %q has no bindings", name, s.Title)
				}
			}
		})
	}
}

func TestNewHelpOverlayModel(t *testing.T) {
	ctx := dashboardLeftHelp()
	m := NewHelpOverlayModel(ctx, 80, 24)

	if m.context.Name != "Dashboard" {
		t.Errorf("context.Name = %q, want %q", m.context.Name, "Dashboard")
	}
	if m.width != 80 {
		t.Errorf("width = %d, want 80", m.width)
	}
	if m.height != 24 {
		t.Errorf("height = %d, want 24", m.height)
	}
}

func TestHelpOverlayView(t *testing.T) {
	ctx := dashboardLeftHelp()
	m := NewHelpOverlayModel(ctx, 80, 40)
	view := m.View()

	// Should contain the title
	if !strings.Contains(view, "Help: Dashboard") {
		t.Error("View() missing 'Help: Dashboard' title")
	}

	// Should contain keybinding sections
	if !strings.Contains(view, "NAVIGATION") {
		t.Error("View() missing NAVIGATION section")
	}
	if !strings.Contains(view, "FEATURES") {
		t.Error("View() missing FEATURES section")
	}
	if !strings.Contains(view, "TOOLS") {
		t.Error("View() missing TOOLS section")
	}

	// Should contain close hint
	if !strings.Contains(view, "Close") {
		t.Error("View() missing close hint")
	}
}

func TestHelpContextsDescribeContextualAKey(t *testing.T) {
	contexts := AllHelpContexts()
	tests := []struct {
		name    string
		context string
	}{
		{name: "dashboard_list", context: "Dashboard"},
		{name: "detail_panel", context: "Detail Panel"},
		{name: "detail_view", context: "Detail"},
	}

	wantDescriptions := []string{
		"Watch active work",
		"Answer questions and input gates",
		"Approve pending permissions",
		"Review pending gates",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, ok := contexts[tt.context]
			if !ok {
				t.Fatalf("AllHelpContexts() missing %q context", tt.context)
			}

			section := findSectionByTitle(&ctx, "CONTEXTUAL A")
			if section == nil {
				t.Fatalf("%s help missing CONTEXTUAL A section", tt.context)
			}
			for _, want := range wantDescriptions {
				found := false
				for _, binding := range section.Bindings {
					if binding.Key == "a" && binding.Desc == want {
						found = true
					}
					if strings.Contains(binding.Desc, "Attach") || strings.Contains(binding.Desc, "attach") {
						t.Errorf("%s contextual binding uses retired attach copy: %q", tt.context, binding.Desc)
					}
				}
				if !found {
					t.Errorf("%s contextual section missing %q", tt.context, want)
				}
			}
		})
	}
}

func TestHelpOverlayCloseOnEsc(t *testing.T) {
	ctx := dashboardLeftHelp()
	m := NewHelpOverlayModel(ctx, 80, 24)

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Update(esc) returned nil cmd, want HelpOverlayCloseMsg")
	}

	msg := cmd()
	if _, ok := msg.(HelpOverlayCloseMsg); !ok {
		t.Errorf("Update(esc) cmd returned %T, want HelpOverlayCloseMsg", msg)
	}
}

func TestHelpOverlayCloseOnQuestionMark(t *testing.T) {
	ctx := dashboardLeftHelp()
	m := NewHelpOverlayModel(ctx, 80, 24)

	_, cmd := m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	if cmd == nil {
		t.Fatal("Update(?) returned nil cmd, want HelpOverlayCloseMsg")
	}

	msg := cmd()
	if _, ok := msg.(HelpOverlayCloseMsg); !ok {
		t.Errorf("Update(?) cmd returned %T, want HelpOverlayCloseMsg", msg)
	}
}

func TestHelpOverlayIgnoresOtherKeys(t *testing.T) {
	ctx := dashboardLeftHelp()
	m := NewHelpOverlayModel(ctx, 80, 24)

	// Pressing 'a' should not close the overlay
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(HelpOverlayCloseMsg); ok {
			t.Error("pressing 'a' should not close the help overlay")
		}
	}
}

func TestHelpOverlayPerContext(t *testing.T) {
	contexts := AllHelpContexts()
	for name, ctx := range contexts {
		t.Run(name, func(t *testing.T) {
			// Use a tall terminal so all sections are visible in the viewport
			m := NewHelpOverlayModel(ctx, 80, 60)
			view := m.View()
			if !strings.Contains(view, "Help: "+name) {
				t.Errorf("View() for %q missing title 'Help: %s'", name, name)
			}
			// Each context should render at least one section title
			for _, s := range ctx.Sections {
				if !strings.Contains(view, s.Title) {
					t.Errorf("View() for %q missing section %q", name, s.Title)
				}
			}
		})
	}
}

func TestRenderHelpContent(t *testing.T) {
	ctx := ViewHelpContext{
		Name: "Test",
		Sections: []HelpSection{
			{
				Title: "SECTION ONE",
				Bindings: []HelpBinding{
					{"a", "first action"},
					{"b", "second action"},
				},
			},
		},
	}

	content := renderHelpContent(ctx)
	if !strings.Contains(content, "SECTION ONE") {
		t.Error("renderHelpContent missing section title")
	}
	if !strings.Contains(content, "first action") {
		t.Error("renderHelpContent missing binding description")
	}
	if !strings.Contains(content, "second action") {
		t.Error("renderHelpContent missing second binding description")
	}
}

func TestHelpOverlaySmallTerminal(t *testing.T) {
	ctx := dashboardLeftHelp()
	// Very small terminal — should still work without panic
	m := NewHelpOverlayModel(ctx, 30, 10)
	view := m.View()
	if view == "" {
		t.Error("View() returned empty string for small terminal")
	}
}

// newHelpTestApp creates a minimal AppModel for help overlay tests.
func newHelpTestApp(t *testing.T) AppModel {
	t.Helper()
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	fm := feature.NewManager(store, cfg)
	dash := NewDashboardModel(nil, "")
	dash.width = 80
	dash.height = 24
	return AppModel{
		currentView:    ViewDashboard,
		dashboard:      dash,
		featureManager: fm,
		programRef:     &ProgramRef{},
		width:          80,
		height:         24,
	}
}

func questionMarkKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: '?', Text: "?"}
}

func escKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEscape}
}

// TestHelpOverlayOpensInAllViews verifies ? opens the help overlay in every
// supported view that does not have an active text input.
func TestHelpOverlayOpensInAllViews(t *testing.T) {
	tests := []struct {
		name    string
		view    View
		wantCtx string
		setup   func(*AppModel)
	}{
		{"Dashboard left panel", ViewDashboard, "Dashboard", nil},
		{"Dashboard right panel", ViewDashboard, "Detail Panel", func(app *AppModel) {
			app.dashboard.focusPanel = 1
		}},
		{"Detail", ViewDetail, "Detail", func(app *AppModel) {
			app.detail = NewDetailModel(&feature.Feature{ID: "test", Name: "Test"}, t.TempDir())
			app.detail.width = 80
			app.detail.height = 24
		}},
		{"Wizard (non-text step)", ViewWizard, "Wizard", func(app *AppModel) {
			app.wizard = NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil, nil)
			app.wizard.step = wizardStepReview
		}},
		{"Publish", ViewPublish, "Publish", nil},
		{"Recovery", ViewRecovery, "Recovery", nil},
		{"Logs", ViewLogs, "Logs", nil},
		{"Review Comments", ViewReviewComments, "Review Comments", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newHelpTestApp(t)
			app.currentView = tt.view
			if tt.setup != nil {
				tt.setup(&app)
			}

			result, _ := app.Update(questionMarkKey())
			got := result.(AppModel)

			if !got.helpOverlayActive {
				t.Fatal("helpOverlayActive = false, want true")
			}
			if got.helpOverlay.context.Name != tt.wantCtx {
				t.Errorf("help context = %q, want %q", got.helpOverlay.context.Name, tt.wantCtx)
			}
		})
	}
}

// TestHelpOverlayCloseRestoresView verifies that closing the help overlay
// returns to the original view with helpOverlayActive = false.
func TestHelpOverlayCloseRestoresView(t *testing.T) {
	app := newHelpTestApp(t)
	app.currentView = ViewLogs

	// Open
	result, _ := app.Update(questionMarkKey())
	app = result.(AppModel)
	if !app.helpOverlayActive {
		t.Fatal("help overlay should be active after pressing ?")
	}

	// Close with esc — the overlay sends HelpOverlayCloseMsg
	result, cmd := app.Update(escKey())
	app = result.(AppModel)
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(HelpOverlayCloseMsg); ok {
			result, _ = app.Update(msg)
			app = result.(AppModel)
		}
	}

	if app.helpOverlayActive {
		t.Error("helpOverlayActive should be false after esc")
	}
	if app.currentView != ViewLogs {
		t.Errorf("currentView = %d, want ViewLogs (%d)", app.currentView, ViewLogs)
	}
}

// TestHelpOverlayToggle verifies ? toggles the overlay (opens then closes).
func TestHelpOverlayToggle(t *testing.T) {
	app := newHelpTestApp(t)

	// Open with ?
	result, _ := app.Update(questionMarkKey())
	app = result.(AppModel)
	if !app.helpOverlayActive {
		t.Fatal("help overlay should be active")
	}

	// Close with ? (overlay handles it)
	result, cmd := app.Update(questionMarkKey())
	app = result.(AppModel)
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(HelpOverlayCloseMsg); ok {
			result, _ = app.Update(msg)
			app = result.(AppModel)
		}
	}

	if app.helpOverlayActive {
		t.Error("helpOverlayActive should be false after second ?")
	}
}

// TestHelpOverlaySuppressedDuringTextInput verifies ? is not intercepted when
// a text input is active (wizard text step, chat, help answer).
func TestHelpOverlaySuppressedDuringTextInput(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*AppModel)
	}{
		{"help input active", func(app *AppModel) {
			app.helpInputActive = true
		}},
		{"chat open", func(app *AppModel) {
			app.chatOpen = true
		}},
		{"wizard text step", func(app *AppModel) {
			app.currentView = ViewWizard
			app.wizard = NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "", nil, nil, nil, nil, nil, nil, nil)
			app.wizard.step = wizardStepWhat // text input step
		}},
		{"publish PR description title", func(app *AppModel) {
			app.currentView = ViewPublish
			app.publish = newTestPublishModel("f1", "", "", "", "", 80, 24)
			app.publish.step = publishStepPRDesc
			app.publish.editingBody = false // editing title
		}},
		{"publish PR description body", func(app *AppModel) {
			app.currentView = ViewPublish
			app.publish = newTestPublishModel("f1", "", "", "", "", 80, 24)
			app.publish.step = publishStepPRDesc
			app.publish.editingBody = true // editing body
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newHelpTestApp(t)
			tt.setup(&app)

			result, _ := app.Update(questionMarkKey())
			got := result.(AppModel)

			if got.helpOverlayActive {
				t.Error("helpOverlayActive = true, want false (text input active)")
			}
		})
	}
}

// TestHelpOverlayConsumesKeys verifies that when the overlay is open,
// other keys don't leak to the underlying view.
func TestHelpOverlayConsumesKeys(t *testing.T) {
	app := newHelpTestApp(t)

	// Open help overlay
	result, _ := app.Update(questionMarkKey())
	app = result.(AppModel)

	// Press 'n' (new feature in dashboard) — should NOT create a wizard transition
	result, _ = app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	got := result.(AppModel)

	if got.currentView != ViewDashboard {
		t.Errorf("currentView changed to %d while overlay was active", got.currentView)
	}
	if !got.helpOverlayActive {
		t.Error("helpOverlayActive became false from pressing 'n'")
	}
}

// TestHelpStopBindingInDetailAndDashboardRight verifies the "s" stop binding
// appears in both the "Detail Panel" and "Detail" help contexts.
func TestHelpStopBindingInDetailAndDashboardRight(t *testing.T) {
	for _, name := range []string{"Detail Panel", "Detail"} {
		t.Run(name, func(t *testing.T) {
			contexts := AllHelpContexts()
			ctx, ok := contexts[name]
			if !ok {
				t.Fatalf("missing help context %q", name)
			}

			found := false
			for _, section := range ctx.Sections {
				for _, b := range section.Bindings {
					if b.Key == "s" && strings.Contains(b.Desc, "Stop") {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("help context %q missing 's' / 'Stop running feature' binding", name)
			}

			// Also verify it renders in the overlay view
			m := NewHelpOverlayModel(ctx, 80, 60)
			view := m.View()
			if !strings.Contains(view, "Stop running feature") {
				t.Errorf("help overlay for %q does not contain 'Stop running feature'", name)
			}
		})
	}
}
