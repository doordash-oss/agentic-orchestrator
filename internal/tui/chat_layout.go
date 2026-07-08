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
// terminal height. The floor is smaller when there's no conversation to
// show yet; the ceiling (18 rows / 35% of height) is unchanged from before
// this method existed as a free function of total height only.
func (m ChatModel) chatPanelHeight(totalHeight int) int {
	h := totalHeight * 35 / 100
	floor := 10
	ceiling := 18
	if len(m.turns) == 0 && !m.responding {
		floor = 5
		ceiling = 8
	}
	if m.hasActiveQuestion() {
		floor = max(floor, m.minimumQuestionPanelHeight())
		ceiling = 18
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

// resize recalculates layout dimensions for the bottom-panel layout.
// h is the total height allocated to the chat panel (including border).
func (m ChatModel) resize(w, h int) ChatModel {
	m.width = w
	m.height = h
	contentWidth := max(w-6, 40)
	inputHeight := m.currentInputHeight()
	bottomHeight := inputHeight
	if m.hasActiveQuestion() {
		bottomHeight = m.minimumQuestionBodyHeight(contentWidth)
	}
	vpHeight := max(h-bottomHeight-chatFooterHeight-chatBorderHeight-chatSectionSeparators, chatMinViewportHeight)

	m.viewport.SetWidth(contentWidth)
	m.viewport.SetHeight(vpHeight)
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

func (m ChatModel) minimumPanelHeight() int {
	return chatBorderHeight + chatMinViewportHeight + m.currentInputHeight() + chatFooterHeight + chatSectionSeparators
}

func (m ChatModel) minimumQuestionPanelHeight() int {
	return chatBorderHeight + chatMinViewportHeight + m.minimumQuestionBodyHeight(max(m.width-6, 40)) + chatFooterHeight + chatSectionSeparators
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
			line = "Thinking..."
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
	if len(m.turns) == 0 && !m.responding && !m.hasActiveQuestion() {
		vpContent = lipgloss.NewStyle().
			Foreground(colorSurface).
			Height(m.viewport.Height()).
			Render("Ask anything about Agentic Orchestrator... press Enter to send, Esc to close.")
	}

	var bottom, footer string
	switch {
	case m.hasActiveQuestion():
		bottom, footer = m.renderQuestionPicker()
	case m.responding:
		bottom = m.input.View()
		footer = KeyHelpStyle.Render("[esc] Background   [ctrl+c] Cancel   [ctrl+g] Full screen")
	default:
		bottom = m.input.View()
		footer = KeyHelpStyle.Render("[enter] Send · [shift+enter] Newline · [esc] Close · [ctrl+g] Full screen")
	}

	inner := vpContent + "\n" + bottom + "\n" + footer

	box := panelStyle(true).
		Width(m.width).
		Height(m.height).
		Render(inner)
	box = renderBorderTitle(box, "Ask me Anything", lipgloss.NewStyle().Foreground(colorBrand))

	return box
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
