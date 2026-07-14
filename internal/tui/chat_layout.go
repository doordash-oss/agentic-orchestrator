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

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// chatPanelHeight returns the docked-mode panel height for the given total
// terminal height. Empty chats stay compact, ordinary conversations keep the
// 18-row dock ceiling, and active prompts can grow taller so controls fit.
func (m ChatModel) chatPanelHeight(totalHeight int) int {
	h := totalHeight * 35 / 100
	floor := 10
	ceiling := 18
	if len(m.turns) == 0 && !m.responding {
		floor = 5
		ceiling = 8
	}
	if m.hasActiveQuestion() || m.hasActivePermission() {
		floor = max(floor, m.minimumQuestionPanelHeight())
		ceiling = chatActivePromptMaxHeight
	}
	if h < floor {
		h = floor
	}
	if h > ceiling {
		h = ceiling
	}
	if needed := m.minimumPanelHeight(); h < needed {
		h = needed
	}
	if totalHeight > 0 && h > totalHeight {
		h = totalHeight
	}
	return h
}

func (m ChatModel) chatContentWidth() int {
	return max(m.width-6, 40)
}

func chatBottomPanelContentWidth(panelWidth int) int {
	return max(panelWidth-chatBottomPanelHFrame, 1)
}

func chatBottomPanelHeight(bodyHeight int) int {
	return bodyHeight + chatFooterHeight + chatBottomPanelFooterGap + chatBottomPanelFrameHeight
}

// resize recalculates layout dimensions for the bottom-panel layout.
// h is the total height allocated to the chat panel (including border).
func (m ChatModel) resize(w, h int) ChatModel {
	m.width = w
	m.height = h
	panelWidth := m.chatContentWidth()
	contentWidth := chatBottomPanelContentWidth(panelWidth)
	inputHeight := m.currentInputHeight()
	bottomHeight := inputHeight
	if m.hasActiveQuestion() {
		bottomHeight = m.activeQuestionBodyHeight(contentWidth)
	} else if m.hasActivePermission() {
		bottomHeight = m.minimumPermissionBodyHeight()
	}
	bottomPanelHeight := chatBottomPanelHeight(bottomHeight)
	gapRows := m.transcriptInputGapRows(bottomPanelHeight)
	vpHeight := max(h-bottomPanelHeight-chatBorderHeight-gapRows, chatMinViewportHeight)
	wasAtBottom := m.viewport.AtBottom() || m.viewport.TotalLineCount() <= m.viewport.Height()

	m.viewport.SetWidth(panelWidth)
	m.viewport.SetHeight(vpHeight)
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
	m.input.SetWidth(contentWidth)
	m.inputHeight = inputHeight
	m.input.SetHeight(inputHeight)
	return m
}

func (m ChatModel) currentInputHeight() int {
	h := syncTextareaHeight(m.input.Value(), chatInputMinLines, chatInputMaxLines)
	if h < chatInputMinLines {
		return chatInputMinLines
	}
	return h
}

func (m ChatModel) panelHeightFor(bottomPanelHeight int) int {
	return chatBorderHeight + chatMinViewportHeight + bottomPanelHeight + m.desiredTranscriptInputGapRows()
}

func (m ChatModel) minimumPanelHeight() int {
	if m.hasActiveQuestion() {
		return m.minimumQuestionPanelHeight()
	}
	if m.hasActivePermission() {
		return m.panelHeightFor(chatBottomPanelHeight(m.minimumPermissionBodyHeight()))
	}
	return m.panelHeightFor(chatBottomPanelHeight(m.currentInputHeight()))
}

func (m ChatModel) minimumQuestionPanelHeight() int {
	contentWidth := chatBottomPanelContentWidth(m.chatContentWidth())
	return m.panelHeightFor(chatBottomPanelHeight(m.minimumQuestionBodyHeight(contentWidth)))
}

func (m ChatModel) minimumQuestionBodyHeight(contentWidth int) int {
	if !m.onQuestionSlot() {
		return 3
	}
	q := m.questions[m.currentQuestionIdx]
	promptLines := questionPromptLineCount(q.Question, contentWidth)
	if questionUsesDirectFreeform(q) || m.typingCustom {
		return promptLines + 1 + m.currentInputHeight()
	}
	return promptLines + 1 + chatQuestionMinOptionLines + 1
}

func (m ChatModel) activeQuestionBodyHeight(contentWidth int) int {
	bodyHeight := m.minimumQuestionBodyHeight(contentWidth)
	maxBodyHeight := min(chatActivePromptMaxBodyLines, m.maxQuestionBodyHeightWithTranscriptReserve())
	// minimumQuestionPanelHeight already sized the panel to fit bodyHeight
	// (assuming only chatMinViewportHeight rows for the transcript); never
	// shrink below that just because the transcript reserve here is
	// stricter — the panel has the room, the transcript gets less instead.
	if maxBodyHeight < bodyHeight {
		maxBodyHeight = bodyHeight
	}
	bodyHeight = max(bodyHeight, min(chatActivePromptPreferredBodyLines, maxBodyHeight))
	if bodyHeight > maxBodyHeight {
		bodyHeight = maxBodyHeight
	}
	return bodyHeight
}

func (m ChatModel) maxQuestionBodyHeightWithTranscriptReserve() int {
	gapRows := m.desiredTranscriptInputGapRows()
	reserveRows := m.activeQuestionTranscriptReserveRows()
	maxPanelHeight := m.height - chatBorderHeight - reserveRows - gapRows
	if maxPanelHeight < chatBottomPanelHeight(1) {
		maxPanelHeight = m.height - chatBorderHeight - chatMinViewportHeight
	}
	return max(maxPanelHeight-chatFooterHeight-chatBottomPanelFooterGap-chatBottomPanelFrameHeight, 1)
}

func (m ChatModel) activeQuestionTranscriptReserveRows() int {
	if len(m.turns) == 0 && !m.responding {
		return chatMinViewportHeight
	}
	switch {
	case m.height >= 40:
		return 8
	case m.height >= 24:
		return 6
	case m.height >= 16:
		return 4
	default:
		return chatMinViewportHeight
	}
}

func (m ChatModel) minimumPermissionBodyHeight() int {
	return 4
}

func (m ChatModel) desiredTranscriptInputGapRows() int {
	if len(m.turns) == 0 && !m.responding {
		return 0
	}
	return chatTranscriptInputGapRows
}

func (m ChatModel) transcriptInputGapRows(bottomPanelHeight int) int {
	available := m.height - chatBorderHeight - chatMinViewportHeight - bottomPanelHeight
	return min(m.desiredTranscriptInputGapRows(), max(available, 0))
}

// rebuildViewport sets the viewport content from the turn list + thinking indicator.
func (m *ChatModel) rebuildViewport() {
	width := m.viewport.Width()
	var b strings.Builder
	for _, t := range m.turns {
		b.WriteString(renderChatTurn(t, width))
		b.WriteString("\n\n")
	}
	if m.responding {
		line := m.thinkingLine
		if line == "" {
			line = thinkingLineText
		}
		b.WriteString(renderAgentThinkingTag(m.spinnerView, line))
	}
	m.viewport.SetContent(wrapForViewport(strings.TrimRight(b.String(), "\n"), width))
	m.viewport.GotoBottom()
}

// syncChatInputHeight recalculates the chat input's height from its content.
func (m *ChatModel) syncChatInputHeight() {
	h := syncTextareaHeight(m.input.Value(), chatInputMinLines, chatInputMaxLines)
	if h != m.inputHeight {
		m.inputHeight = h
		m.input.SetHeight(h)
	}
}

func (m ChatModel) View() string {
	vpContent := m.viewport.View()
	vpContent = lipgloss.NewStyle().
		Width(m.viewport.Width()).
		Height(m.viewport.Height()).
		Render(vpContent)
	if len(m.turns) == 0 && !m.responding && !m.hasActiveQuestion() && !m.hasActivePermission() {
		vpContent = lipgloss.NewStyle().
			Foreground(colorSurface).
			Height(m.viewport.Height()).
			Render("Ask anything about Agentic Orchestrator... press Enter to send, Esc to close.")
	}

	var bottom, footer, bottomTitle string
	switch {
	case m.hasActiveQuestion():
		bottom, footer = m.renderQuestionPicker()
		bottom = lipgloss.NewStyle().
			Height(m.activeQuestionBodyHeight(chatBottomPanelContentWidth(m.chatContentWidth()))).
			Render(bottom)
		bottomTitle = attentionTypeLabelQuestion
	case m.hasActivePermission():
		bottom, footer = m.renderPermissionPrompt()
		bottomTitle = attentionTypeLabelPermission
	case m.responding:
		bottom = m.input.View()
		footer = KeyHelpStyle.Render("[esc] Background · [ctrl+c] Cancel · [ctrl+g] Full")
		bottomTitle = "Message"
	default:
		bottom = m.input.View()
		footer = KeyHelpStyle.Render("[enter] Send · [shift+enter] Newline · [esc] Close · [ctrl+g] Full")
		bottomTitle = "Message"
	}

	bottomPanelHeight := chatBottomPanelHeight(lipgloss.Height(bottom))
	separator := strings.Repeat("\n", m.transcriptInputGapRows(bottomPanelHeight)+1)
	inner := vpContent + separator + m.renderBottomPanel(bottomTitle, bottom, footer)

	box := panelStyle(true).
		Width(m.width).
		Height(m.height).
		Render(inner)
	box = renderBorderTitle(box, "Ask me Anything", lipgloss.NewStyle().Foreground(colorBrand))

	return box
}

func (m ChatModel) renderBottomPanel(title, body, footer string) string {
	content := body
	if footer != "" {
		content += "\n" + footer
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorActive).
		Padding(0, 1).
		Width(m.chatContentWidth()).
		Render(content)
	return renderBorderTitle(box, title, lipgloss.NewStyle().Foreground(colorActive))
}

// wrapForViewport applies ANSI-aware word-wrapping so long lines don't
// overflow the viewport horizontally. Uses ansi.Wrap (not Wordwrap) so
// words longer than width are hard-wrapped instead of overflowing.
func wrapForViewport(content string, width int) string {
	if width <= 0 {
		return content
	}
	return ansi.Wrap(content, width, "")
}
