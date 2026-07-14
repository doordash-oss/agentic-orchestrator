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
	repoName      string
	cycleType     feature.RepoCycleType
	rewindPhase   feature.Phase
	artifactID    string
	draftRevision string
	canIterate    bool

	width, height int

	showMenu   bool
	menuChoice int

	detached bool
	decided  bool

	nuiForm *needUserInputForm
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

func NewNeedUserInputReviewModel(featureID string, gate server.NeedInputGateDTO, width, height int) ArtifactReviewModel {
	if gate.FeatureID != "" {
		featureID = gate.FeatureID
	}
	gate.FeatureID = featureID
	return ArtifactReviewModel{
		featureID:  featureID,
		reviewMode: reviewModeNeedUserInput,
		repoName:   gate.RepoName,
		cycleType:  feature.RepoCycleType(gate.CycleType),
		artifactID: "need-user-input",
		width:      width,
		height:     height,
		nuiForm:    newNeedUserInputForm(gate, width),
	}
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
		if m.nuiForm != nil {
			m.nuiForm.SetWidth(msg.Width)
			return m, nil
		}
		m.recalcLayout()
		return m, nil
	case tea.PasteMsg:
		if m.nuiForm != nil {
			return m, m.nuiForm.Forward(msg)
		}
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	default:
		if m.nuiForm != nil {
			return m, m.nuiForm.Forward(msg)
		}
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		return m, cmd
	}
}

func (m ArtifactReviewModel) handleKey(msg tea.KeyPressMsg) (ArtifactReviewModel, tea.Cmd) {
	if m.showMenu {
		return m.handleMenuKey(msg)
	}

	if m.nuiForm != nil {
		switch msg.String() {
		case "ctrl+d", "ctrl+]", "esc":
			m.showMenu = true
			m.menuChoice = 0
			return m, nil
		}
		return m, m.nuiForm.HandleKey(msg)
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
		if m.nuiForm != nil && decision == "resume" && !m.nuiForm.AllAnswered() {
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
	case reviewModeNeedUserInput:
		resumeLabel := "Resume implementation"
		if m.nuiForm != nil && !m.nuiForm.AllAnswered() {
			resumeLabel += " (answer all questions to enable)"
		}
		return []menuItem{
			{label: resumeLabel, decision: "resume"},
			{label: "Abort", decision: "abort"},
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
	if m.reviewMode == reviewModeNeedUserInput {
		msg := NeedUserInputDecisionMsg{
			FeatureID: m.featureID,
			RepoName:  m.repoName,
			CycleType: m.cycleType,
			Decision:  decision,
		}
		return func() tea.Msg { return msg }
	}
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

func (m ArtifactReviewModel) RepoName() string {
	return m.repoName
}

func (m ArtifactReviewModel) SetRepoName(repoName string) ArtifactReviewModel {
	m.repoName = repoName
	return m
}

func (m ArtifactReviewModel) CycleType() feature.RepoCycleType {
	return m.cycleType
}

func (m ArtifactReviewModel) SetCycleType(cycleType feature.RepoCycleType) ArtifactReviewModel {
	m.cycleType = cycleType
	return m
}

// Decided returns true if the user has already made a menu decision.
func (m ArtifactReviewModel) Decided() bool {
	return m.decided
}

// Reattach resets the detached state so the model can be re-shown.
func (m *ArtifactReviewModel) Reattach() tea.Cmd {
	m.detached = false
	if m.nuiForm != nil {
		m.showMenu = false
		return m.nuiForm.Focus()
	}
	return m.editor.Focus()
}

func (m ArtifactReviewModel) WithDecisionError(err error) ArtifactReviewModel {
	if m.nuiForm == nil {
		return m
	}
	m.nuiForm.decisionErr = err
	m.detached = false
	m.decided = false
	m.showMenu = false
	_ = m.nuiForm.Focus()
	return m
}

func (m ArtifactReviewModel) DecisionError() error {
	if m.nuiForm == nil {
		return nil
	}
	return m.nuiForm.decisionErr
}

func (m ArtifactReviewModel) AllAnswered() bool {
	if m.nuiForm == nil {
		return false
	}
	return m.nuiForm.AllAnswered()
}

func (m ArtifactReviewModel) SetAnswer(i int, answer string) ArtifactReviewModel {
	if m.nuiForm == nil {
		return m
	}
	m.nuiForm.SetAnswer(i, answer)
	return m
}

// MenuOpen reports whether the Ctrl+D action menu overlay is active.
func (m ArtifactReviewModel) MenuOpen() bool { return m.showMenu }

func (m ArtifactReviewModel) MenuItemLabels() []string {
	items := m.menuItems()
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.label)
	}
	return out
}

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
	if m.nuiForm != nil {
		return m.renderNeedUserInputView()
	}
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
	items := m.menuItems()
	var menuLines []string

	title := "Review Decision"
	if m.nuiForm != nil {
		title = "Choose an action:"
	}
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	menuLines = append(menuLines, titleStyle.Render(title))
	menuLines = append(menuLines, "")

	for i, item := range items {
		prefix := "  "
		style := lipgloss.NewStyle()
		if i == m.menuChoice {
			prefix = "▸ "
			style = style.Bold(true).Foreground(colorBrand)
		}
		if m.nuiForm != nil && item.decision == "resume" && !m.nuiForm.AllAnswered() {
			style = style.Foreground(lipgloss.Color("240"))
		}
		menuLines = append(menuLines, style.Render(prefix+item.label))
	}

	menuLines = append(menuLines, "")
	dimStyle := lipgloss.NewStyle().Foreground(colorSurface)
	menuLines = append(menuLines, dimStyle.Render("Enter: select │ Esc: cancel"))
	menuContent := strings.Join(menuLines, "\n")

	menuBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(1, 2).
		Render(menuContent)

	return overlayModal(bg, menuBox, m.width, m.height)
}

func (m ArtifactReviewModel) renderNeedUserInputView() string {
	var sb strings.Builder
	sb.WriteString(m.renderHeader())

	panelW := max(m.width-2, 20)
	body := m.nuiForm.View()
	box := panelStyle(true).Width(panelW).Render(body)
	titleStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	box = renderBorderTitle(box, " Need User Input ", titleStyle)
	sb.WriteString(box)
	sb.WriteString("\n")

	hintStyle := lipgloss.NewStyle().Foreground(colorSubtext)
	sb.WriteString(hintStyle.Render("Tab/Shift+Tab: navigate │ Ctrl+D: actions menu │ Esc: actions menu"))

	if m.showMenu {
		return m.renderMenuOverlay(sb.String())
	}
	return sb.String()
}
