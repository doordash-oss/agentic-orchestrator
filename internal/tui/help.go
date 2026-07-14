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
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// HelpOverlayCloseMsg signals the help overlay should close.
type HelpOverlayCloseMsg struct{}

const (
	helpContextDashboard      = "Dashboard"
	helpContextDetailPanel    = "Detail Panel"
	helpContextDetail         = "Detail"
	helpContextLogs           = "Logs"
	helpContextPublish        = "Publish"
	helpContextRecovery       = "Recovery"
	helpContextWizard         = "Wizard"
	helpContextReviewComments = "Review Comments"
)

// HelpBinding represents a single keybinding displayed in the help overlay.
type HelpBinding struct {
	Key  string
	Desc string
}

// HelpSection groups related keybindings under a titled section.
type HelpSection struct {
	Title    string
	Bindings []HelpBinding
}

// ViewHelpContext defines the help content for a specific view/context.
type ViewHelpContext struct {
	Name     string
	Sections []HelpSection
}

func contextualAHelpSection() HelpSection {
	return HelpSection{
		Title: "CONTEXTUAL A",
		Bindings: []HelpBinding{
			{"a", "Watch active work"},
			{"a", "Answer questions and input gates"},
			{"a", "Approve pending permissions"},
			{"a", "Review pending gates"},
		},
	}
}

// AllHelpContexts returns the registry of help contexts keyed by name.
func AllHelpContexts() map[string]ViewHelpContext {
	return map[string]ViewHelpContext{
		helpContextDashboard:      dashboardLeftHelp(),
		helpContextDetailPanel:    dashboardRightHelp(),
		helpContextDetail:         detailHelp(),
		helpContextWizard:         wizardHelp(),
		helpContextPublish:        publishHelp(),
		helpContextRecovery:       recoveryHelp(),
		helpContextLogs:           logsHelp(),
		helpContextReviewComments: reviewCommentsHelp(),
	}
}

func dashboardLeftHelp() ViewHelpContext {
	return ViewHelpContext{
		Name: helpContextDashboard,
		Sections: []HelpSection{
			{
				Title: "NAVIGATION",
				Bindings: []HelpBinding{
					{"↑/k", "Move up"},
					{"↓/j", "Move down"},
					{"→/enter", "Focus feature"},
					{"tab", "Switch panel"},
					{"enter", "Toggle section / Focus feature"},
				},
			},
			contextualAHelpSection(),
			{
				Title: "FEATURES",
				Bindings: []HelpBinding{
					{"n", "New feature"},
					{"d", "Delete feature"},
					{"v", "View diff"},
					{"p", "Publish"},
					{"Shift+R", "Resume all"},
					{"Shift+A", "Approve & remember permissions"},
				},
			},
			{
				Title: "TOOLS",
				Bindings: []HelpBinding{
					{"Shift+W", "Manage workspaces"},
					{"Shift+E", "Edit workspace config"},
					{ChatKeyHint(), "Ask AI"},
					{"?", "Help"},
					{"q", "Quit"},
				},
			},
		},
	}
}

func dashboardRightHelp() ViewHelpContext {
	return ViewHelpContext{
		Name: helpContextDetailPanel,
		Sections: []HelpSection{
			{
				Title: "NAVIGATION",
				Bindings: []HelpBinding{
					{"←/esc", "Back to list"},
					{"tab", "Switch panel"},
				},
			},
			contextualAHelpSection(),
			{
				Title: "ACTIONS",
				Bindings: []HelpBinding{
					{"o", "Show overview"},
					{"y", "Approve permissions"},
					{"Shift+A", "Approve & remember permissions"},
					{"h", "Answer help question"},
					{"s", "Stop running feature"},
					{"r", "Restart phase"},
					{"ctrl+r", "Rewind"},
					{"l", "Live Preview / View logs"},
					{"v", "View diff"},
					{"d", "Delete feature"},
				},
			},
			{
				Title: "PUBLISH",
				Bindings: []HelpBinding{
					{"p", "Publish PR"},
					{"b", "Rebase on main"},
					{"Shift+D", "Mark as done"},
					{"g", "Review comments"},
					{"c", "Clean worktree"},
				},
			},
			{
				Title: "TOOLS",
				Bindings: []HelpBinding{
					{"Shift+E", "Edit workspace config"},
					{ChatKeyHint(), "Ask AI"},
					{"?", "Help"},
				},
			},
		},
	}
}

func detailHelp() ViewHelpContext {
	return ViewHelpContext{
		Name: helpContextDetail,
		Sections: []HelpSection{
			{
				Title: "NAVIGATION",
				Bindings: []HelpBinding{
					{"esc", "Back to dashboard"},
				},
			},
			contextualAHelpSection(),
			{
				Title: "ACTIONS",
				Bindings: []HelpBinding{
					{"y", "Approve permissions"},
					{"Shift+A", "Approve & remember permissions"},
					{"h", "Answer help question"},
					{"Shift+E", "Edit workspace config"},
					{"s", "Stop running feature"},
					{"r", "Restart phase"},
					{"ctrl+r", "Rewind"},
					{"l", "View logs"},
					{"v", "View diff"},
					{"d", "Delete feature"},
				},
			},
			{
				Title: "PUBLISH",
				Bindings: []HelpBinding{
					{"p", "Publish PR"},
					{"b", "Rebase on main"},
					{"Shift+D", "Mark as done"},
					{"g", "Review comments"},
					{"c", "Clean worktree"},
				},
			},
		},
	}
}

func wizardHelp() ViewHelpContext {
	return ViewHelpContext{
		Name: helpContextWizard,
		Sections: []HelpSection{
			{
				Title: "NAVIGATION",
				Bindings: []HelpBinding{
					{"enter", "Next step"},
					{"shift+tab", "Previous step"},
					{"esc", "Cancel"},
				},
			},
			{
				Title: "INPUT",
				Bindings: []HelpBinding{
					{"tab", "Toggle / cycle options"},
					{"↑/k", "Move selection up"},
					{"↓/j", "Move selection down"},
					{"←/→", "Cycle model"},
					{"ctrl+v", "Paste image"},
					{"@", "File picker"},
				},
			},
		},
	}
}

func publishHelp() ViewHelpContext {
	return ViewHelpContext{
		Name: helpContextPublish,
		Sections: []HelpSection{
			{
				Title: "ACTIONS",
				Bindings: []HelpBinding{
					{"enter", "Next step"},
					{"tab", "Toggle title/body"},
					{"esc", "Cancel"},
				},
			},
		},
	}
}

func recoveryHelp() ViewHelpContext {
	return ViewHelpContext{
		Name: helpContextRecovery,
		Sections: []HelpSection{
			{
				Title: "ACTIONS",
				Bindings: []HelpBinding{
					{"r", "Resume"},
					{"k", "Kill"},
					{"s", "Skip"},
					{"enter", "Confirm"},
				},
			},
		},
	}
}

func logsHelp() ViewHelpContext {
	return ViewHelpContext{
		Name: helpContextLogs,
		Sections: []HelpSection{
			{
				Title: "NAVIGATION",
				Bindings: []HelpBinding{
					{"↑/k", "Scroll up"},
					{"↓/j", "Scroll down"},
					{"esc/q", "Back"},
				},
			},
			{
				Title: "ACTIONS",
				Bindings: []HelpBinding{
					{"p", "Publish (if CodeReady)"},
				},
			},
		},
	}
}

func reviewCommentsHelp() ViewHelpContext {
	return ViewHelpContext{
		Name: helpContextReviewComments,
		Sections: []HelpSection{
			{
				Title: "NAVIGATION",
				Bindings: []HelpBinding{
					{"←/→", "Switch queue/detail panel"},
					{"↑/k", "Previous comment or scroll detail up"},
					{"↓/j", "Next comment or scroll detail down"},
					{"PgUp/PgDn", "Scroll detail"},
					{"/", "Filter comments"},
					{"esc", "Clear filter / Back"},
				},
			},
			{
				Title: "ACTIONS",
				Bindings: []HelpBinding{
					{"Shift+A", "Address all comments"},
					{"enter", "Address included comments"},
					{keySpace, "Include / exclude selected comment"},
				},
			},
		},
	}
}

// HelpOverlayModel renders a context-sensitive help overlay.
type HelpOverlayModel struct {
	context  ViewHelpContext
	viewport viewport.Model
	width    int
	height   int
}

// NewHelpOverlayModel creates a help overlay for the given context.
func NewHelpOverlayModel(ctx ViewHelpContext, width, height int) HelpOverlayModel {
	// Reserve space for the border (2 lines top/bottom) and close hint (1 line)
	modalWidth := max(min(width-2, 52), 22)

	content := strings.TrimRight(renderHelpContent(ctx), "\n")
	contentLines := strings.Count(content, "\n") + 1
	maxHeight := max(height-6, 5)
	modalHeight := max(min(contentLines, maxHeight), 5)

	vp := viewport.New(viewport.WithWidth(modalWidth-4), viewport.WithHeight(modalHeight))
	vp.SetContent(content)
	vp.KeyMap = viewport.KeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k")),
		Down:     key.NewBinding(key.WithKeys("down", "j")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
	}

	return HelpOverlayModel{
		context:  ctx,
		viewport: vp,
		width:    width,
		height:   height,
	}
}

// Update handles key events for the help overlay.
func (m HelpOverlayModel) Update(msg tea.Msg) (HelpOverlayModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, keys.Back), key.Matches(msg, keys.HelpOverlay):
			return m, func() tea.Msg { return HelpOverlayCloseMsg{} }
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// View renders the help overlay modal.
func (m HelpOverlayModel) View() string {
	modalWidth := max(min(m.width-2, 52), 22)

	// Build the bordered box with viewport content
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(0, 1).
		Width(modalWidth)

	box := boxStyle.Render(m.viewport.View())
	box = renderBorderTitle(box, "Help: "+m.context.Name, TitleStyle)

	// Scroll indicator
	scrollHint := ""
	if m.viewport.TotalLineCount() > m.viewport.VisibleLineCount() {
		pct := m.viewport.ScrollPercent()
		scrollHint = fmt.Sprintf(" %d%%", int(pct*100))
	}

	// Close hint at the bottom
	closeHint := MutedStyle.Render("  [esc]") +
		lipgloss.NewStyle().Foreground(colorOverlay).Render(" or ") +
		MutedStyle.Render("[?]") +
		lipgloss.NewStyle().Foreground(colorOverlay).Render(" Close") +
		lipgloss.NewStyle().Foreground(colorOverlay).Render(scrollHint)

	return box + "\n" + closeHint
}

// renderHelpContent renders the keybinding sections as formatted text.
func renderHelpContent(ctx ViewHelpContext) string {
	var b strings.Builder

	keyStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(colorText)
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(colorSubtext)

	keyColWidth := 10 // width for the key column

	for i, section := range ctx.Sections {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(headerStyle.Render(section.Title) + "\n")

		for _, binding := range section.Bindings {
			keyStr := keyStyle.Render(fmt.Sprintf("  %-*s", keyColWidth, binding.Key))
			descStr := descStyle.Render(binding.Desc)
			b.WriteString(keyStr + descStr + "\n")
		}
	}

	return b.String()
}
