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
