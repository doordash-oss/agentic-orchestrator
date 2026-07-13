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

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// ArtifactReviewModel edits a server-owned review draft. It never opens or
// writes the canonical artifact path directly; all persistence is requested via
// review-session messages that the API app sends to the REST server.
type ArtifactReviewModel struct {
	editor MarkdownEditor

	featureID     string
	reviewID      string
	reviewMode    string
	rewindPhase   feature.Phase
	artifactID    string
	draftRevision string
	canIterate    bool

	width, height int

	showMenu   bool
	menuChoice int

	detached bool
	decided  bool
}

const (
	artifactReviewHeaderH  = 3
	artifactReviewFooterH  = 1
	artifactReviewSpacingH = 2
)

// NewArtifactReviewModel creates a pathless review editor from a REST review
// session response.
func NewArtifactReviewModel(session server.ReviewSessionResponse, rewindPhase feature.Phase, width, height int) ArtifactReviewModel {
	m := ArtifactReviewModel{
		featureID:     session.FeatureID,
		reviewID:      session.ReviewID,
		reviewMode:    session.ReviewMode,
		rewindPhase:   rewindPhase,
		artifactID:    session.ArtifactID,
		draftRevision: session.DraftRevision,
		canIterate:    session.CanIterate,
		width:         width,
		height:        height,
	}
	if m.reviewMode == "" {
		m.reviewMode = "plan"
	}
	m.recalcLayout()
	m.editor = NewMarkdownEditor(m.editorContentWidth(), m.editorHeight())
	m.editor.SetContent(session.Text, false)
	m.editor.MarkClean()
	return m
}

// Init returns the initial command.
func (m ArtifactReviewModel) Init() tea.Cmd {
	return nil
}

// Update handles messages for the artifact review model.
func (m ArtifactReviewModel) Update(msg tea.Msg) (ArtifactReviewModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalcLayout()
		return m, nil
	case tea.PasteMsg:
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	default:
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		return m, cmd
	}
}

func (m ArtifactReviewModel) handleKey(msg tea.KeyPressMsg) (ArtifactReviewModel, tea.Cmd) {
	if m.showMenu {
		return m.handleMenuKey(msg)
	}

	switch msg.String() {
	case "ctrl+d", "ctrl+]":
		m.showMenu = true
		m.menuChoice = 0
		return m, nil
	case "ctrl+s":
		return m, m.emitDraftSave()
	case "tab":
		return m, nil
	}

	if msg.String() == "esc" && m.editor.Mode() == NormalMode {
		m.showMenu = true
		m.menuChoice = 0
		return m, nil
	}

	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return m, cmd
}

func (m ArtifactReviewModel) handleMenuKey(msg tea.KeyPressMsg) (ArtifactReviewModel, tea.Cmd) {
	menuItems := m.menuItems()

	switch msg.String() {
	case "up", "k":
		if m.menuChoice > 0 {
			m.menuChoice--
		}
		return m, nil
	case "down", "j":
		if m.menuChoice < len(menuItems)-1 {
			m.menuChoice++
		}
		return m, nil
	case "enter":
		decision := menuItems[m.menuChoice].decision
		if decision == "detach" {
			m.detached = true
			m.showMenu = false
			return m, nil
		}
		m.detached = true
		m.decided = true
		return m, m.emitDecision(decision)
	case "esc":
		m.showMenu = false
		return m, nil
	}

	return m, nil
}

type menuItem struct {
	label    string
	decision string
}

func (m ArtifactReviewModel) menuItems() []menuItem {
	switch m.reviewMode {
	case "plan":
		items := []menuItem{}
		if m.canIterate {
			items = append(items, menuItem{label: "Iterate more (+3 rounds)", decision: "iterate"})
		}
		return append(items,
			menuItem{label: "Proceed with current plan", decision: "proceed"},
			menuItem{label: "Return to dashboard", decision: "detach"},
		)
	case "gate":
		return []menuItem{
			{label: "Proceed to next phase", decision: "proceed"},
			{label: "Return to dashboard", decision: "detach"},
		}
	default:
		return []menuItem{
			{label: "Proceed with rewind", decision: "proceed"},
			{label: "Return to dashboard", decision: "detach"},
		}
	}
}

func (m ArtifactReviewModel) emitDraftSave() tea.Cmd {
	msg := ArtifactReviewDraftSaveMsg{
		FeatureID:    m.featureID,
		ReviewID:     m.reviewID,
		BaseRevision: m.draftRevision,
		Text:         m.editor.Content(),
	}
	return func() tea.Msg { return msg }
}

func (m ArtifactReviewModel) emitDecision(decision string) tea.Cmd {
	msg := ArtifactReviewSessionDecisionMsg{
		FeatureID:    m.featureID,
		ReviewID:     m.reviewID,
		Decision:     decision,
		BaseRevision: m.draftRevision,
		Text:         m.editor.Content(),
	}
	return func() tea.Msg { return msg }
}

func (m *ArtifactReviewModel) ApplyDraft(resp server.ReviewSessionResponse) {
	if m == nil {
		return
	}
	m.draftRevision = resp.DraftRevision
	m.editor.SetContent(resp.Text, false)
	m.editor.MarkClean()
}

func (m *ArtifactReviewModel) MarkDraftSaved(resp server.ReviewSessionResponse) {
	if m == nil {
		return
	}
	m.draftRevision = resp.DraftRevision
	m.editor.MarkClean()
}

// Detached returns true if the user chose to detach.
func (m ArtifactReviewModel) Detached() bool {
	return m.detached
}

// FeatureID returns the feature ID for this review.
func (m ArtifactReviewModel) FeatureID() string {
	return m.featureID
}

// ReviewID returns the server-owned review session ID.
func (m ArtifactReviewModel) ReviewID() string {
	return m.reviewID
}

// ArtifactID returns the artifact identifier under review.
func (m ArtifactReviewModel) ArtifactID() string {
	return m.artifactID
}

// ReviewMode returns the review mode ("plan", "gate", or "rewind").
func (m ArtifactReviewModel) ReviewMode() string {
	return m.reviewMode
}

// Decided returns true if the user has already made a menu decision.
func (m ArtifactReviewModel) Decided() bool {
	return m.decided
}

// Reattach resets the detached state so the model can be re-shown.
func (m *ArtifactReviewModel) Reattach() tea.Cmd {
	m.detached = false
	return m.editor.Focus()
}

// MenuOpen reports whether the Ctrl+D action menu overlay is active.
func (m ArtifactReviewModel) MenuOpen() bool { return m.showMenu }

func (m ArtifactReviewModel) editorHeight() int {
	return max(m.height-artifactReviewHeaderH-artifactReviewFooterH-artifactReviewSpacingH, 4)
}

func (m ArtifactReviewModel) editorContentWidth() int {
	panelW := max(m.width-2, 20)
	return max(panelW-4, 10)
}

func (m *ArtifactReviewModel) recalcLayout() {
	m.editor.SetSize(m.editorContentWidth(), m.editorHeight())
}

// View renders the artifact review model.
func (m ArtifactReviewModel) View() string {
	var sb strings.Builder
	sb.WriteString(m.renderHeader())

	panelW := max(m.width-2, 20)
	editorView := m.editor.View()
	editorPanel := panelStyle(true).Width(panelW).Render(editorView)

	titleMode := "NORMAL"
	if m.editor.Mode() == InsertMode {
		titleMode = "INSERT"
	}
	title := " " + m.artifactID + " [" + titleMode + "] "
	if m.editor.Dirty() {
		title = " " + m.artifactID + " [" + titleMode + "] [+] "
	}
	titleStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	editorPanel = renderBorderTitle(editorPanel, title, titleStyle)

	sb.WriteString(editorPanel)
	sb.WriteString("\n")
	sb.WriteString(m.renderFooter())

	if m.showMenu {
		return m.renderMenuOverlay(sb.String())
	}
	return sb.String()
}

func (m ArtifactReviewModel) renderHeader() string {
	artLines := []string{
		" \u2584\u2580\u2588 \u2588\u2580\u2580 \u2588\u2580\u2580 \u2588\u2584\u2591\u2588 \u2580\u2588\u2580 \u2588 \u2588\u2580\u2580 \u2588\u2580\u2588",
		" \u2588\u2580\u2588 \u2588\u2584\u2588 \u2588\u2588\u2584 \u2588\u2591\u2580\u2588 \u2591\u2588\u2591 \u2588 \u2588\u2584\u2584 \u2588\u2584\u2588",
	}
	style := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	return style.Render(strings.Join(artLines, "\n")) + "\n"
}

func (m ArtifactReviewModel) renderFooter() string {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	keys := "i edit • Ctrl+S save draft • Ctrl+D menu • Esc menu"
	return style.Render(keys)
}

func (m ArtifactReviewModel) renderMenuOverlay(bg string) string {
	menuItems := m.menuItems()
	var menu strings.Builder
	menu.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorBrand).Render("Review Decision"))
	menu.WriteString("\n\n")
	for i, item := range menuItems {
		prefix := "  "
		if i == m.menuChoice {
			prefix = "> "
		}
		line := prefix + item.label
		if i == m.menuChoice {
			line = lipgloss.NewStyle().Foreground(colorBrand).Bold(true).Render(line)
		}
		menu.WriteString(line)
		menu.WriteString("\n")
	}

	menuBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(1, 2).
		Width(40).
		Render(menu.String())

	lines := strings.Split(bg, "\n")
	menuLines := strings.Split(menuBox, "\n")

	startRow := max((len(lines)-len(menuLines))/2, 0)
	overlayWidth := max(m.width, lipgloss.Width(menuBox))
	for i, menuLine := range menuLines {
		row := startRow + i
		if row >= len(lines) {
			break
		}
		lines[row] = lipgloss.PlaceHorizontal(overlayWidth, lipgloss.Center, menuLine)
	}

	return strings.Join(lines, "\n")
}
