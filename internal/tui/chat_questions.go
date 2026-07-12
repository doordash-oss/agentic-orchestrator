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
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// hasActiveQuestion reports whether the picker is showing a question or the
// recap slot (mirrors AttachModel.hasActiveQuestion).
func (m ChatModel) hasActiveQuestion() bool {
	return len(m.questions) > 0 && m.currentQuestionIdx <= len(m.questions)
}

// onRecapSlot reports whether the picker is on the "Review & Submit" slot
// one past the final question (mirrors AttachModel.onRecapSlot).
func (m ChatModel) onRecapSlot() bool {
	return len(m.questions) > 0 && m.currentQuestionIdx == len(m.questions)
}

// onQuestionSlot reports whether the picker is showing a real question,
// not the recap slot (mirrors AttachModel.onQuestionSlot).
func (m ChatModel) onQuestionSlot() bool {
	return len(m.questions) > 0 && m.currentQuestionIdx < len(m.questions)
}

// activateQuestions starts a fresh multi-question AskUserQuestion bundle.
// AMA does not queue a second bundle that arrives mid-answer (see Scope
// Notes) — if one arrives while hasActiveQuestion() is true, the caller
// should simply not call activateQuestions again until the current bundle
// is submitted.
func (m *ChatModel) activateQuestions(questions []askUserQuestion, requestID string, raw json.RawMessage) {
	if len(questions) == 0 {
		return
	}
	m.questions = questions
	m.questionStates = make([]questionUIState, len(questions))
	m.questionStates[0].askedEmitted = true
	m.pendingAskRequestID = requestID
	m.pendingAskRaw = raw
	m.collectedAnswers = make(map[string]string)
	m.currentQuestionIdx = 0
	m.selectedOption = 0
	m.selectedMulti = nil
	m.questionScrollOffset = 0
	m.typingCustom = questionUsesDirectFreeform(questions[0])
	m.input.Reset()
	m.syncChatInputHeight()
	*m = m.resize(m.width, m.height)
}

// toggleSelectedMulti flips the ticked state of the focused option on a
// multi-select question (mirrors the " "/"space" case in AttachModel.Update).
func (m *ChatModel) toggleSelectedMulti() {
	if !m.onQuestionSlot() {
		return
	}
	q := m.questions[m.currentQuestionIdx]
	if !q.MultiSelect || m.selectedOption >= len(q.Options) {
		return
	}
	if m.selectedMulti == nil {
		m.selectedMulti = make(map[int]bool)
	}
	if m.selectedMulti[m.selectedOption] {
		delete(m.selectedMulti, m.selectedOption)
	} else {
		m.selectedMulti[m.selectedOption] = true
	}
}

// commitAnswer records the answer for the currently focused question,
// mirroring AttachModel.commitCurrentAnswer (minus the observability emit,
// which chat.go does not have).
func (m *ChatModel) commitAnswer(answer string) {
	if !m.onQuestionSlot() || m.collectedAnswers == nil {
		return
	}
	idx := m.currentQuestionIdx
	q := m.questions[idx]
	m.collectedAnswers[q.Question] = answer
	if idx >= len(m.questionStates) {
		return
	}
	m.snapshotQuestionState(&m.questionStates[idx], q, answer)
}

// snapshotQuestionState records the live picker state (selection or custom
// text) into st after answer is committed for q, so restoreQuestionState can
// later reconstruct the picker exactly as it was left.
func (m *ChatModel) snapshotQuestionState(st *questionUIState, q askUserQuestion, answer string) {
	st.scrollOffset = m.questionScrollOffset
	if m.typingCustom {
		st.typingCustom = true
		st.customText = answer
		st.selectedOption = len(q.Options)
		st.selectedMulti = nil
		return
	}
	st.typingCustom = false
	st.customText = ""
	st.selectedOption = m.selectedOption
	st.selectedMulti = m.snapshotSelectedMulti(q)
}

// snapshotSelectedMulti returns the multi-select snapshot for q: nil for
// single-select questions, the live selection cloned when non-empty, or the
// highlighted option alone when nothing has been ticked yet.
func (m *ChatModel) snapshotSelectedMulti(q askUserQuestion) map[int]bool {
	if !q.MultiSelect {
		return nil
	}
	if len(m.selectedMulti) == 0 {
		return map[int]bool{m.selectedOption: true}
	}
	return cloneIntBoolMap(m.selectedMulti)
}

// restoreQuestionState primes the live UI state for the question at idx
// (mirrors AttachModel.restoreQuestion).
func (m *ChatModel) restoreQuestionState(idx int) {
	if idx < 0 || idx >= len(m.questions) {
		return
	}
	q := m.questions[idx]
	m.input.Reset()
	st := m.questionStates[idx]
	if questionUsesDirectFreeform(q) {
		m.selectedOption = 0
		m.selectedMulti = nil
		m.questionScrollOffset = 0
		m.typingCustom = true
		if st.askedEmitted {
			m.input.SetValue(st.customText)
		}
		return
	}
	if st.askedEmitted {
		m.selectedOption = st.selectedOption
		m.selectedMulti = cloneIntBoolMap(st.selectedMulti)
		m.questionScrollOffset = st.scrollOffset
	} else {
		m.selectedOption = 0
		m.selectedMulti = nil
		m.questionScrollOffset = 0
	}
	m.typingCustom = false
}

// advanceQuestionOpts moves currentQuestionIdx by delta, snapshotting the
// current question first when snapshot is true (mirrors
// AttachModel.advanceQuestionOpts — pass snapshot=false when the caller,
// e.g. commitAnswer, has already written authoritative state).
func (m *ChatModel) advanceQuestionOpts(delta int, snapshot bool) {
	if len(m.questions) == 0 {
		return
	}
	newIdx := m.currentQuestionIdx + delta
	maxIdx := len(m.questions)
	if newIdx < 0 || newIdx > maxIdx || newIdx == m.currentQuestionIdx {
		return
	}
	if snapshot && m.onQuestionSlot() && m.currentQuestionIdx < len(m.questionStates) {
		st := &m.questionStates[m.currentQuestionIdx]
		st.selectedOption = m.selectedOption
		st.selectedMulti = cloneIntBoolMap(m.selectedMulti)
		st.scrollOffset = m.questionScrollOffset
		st.typingCustom = m.typingCustom
		if m.typingCustom {
			st.customText = m.input.Value()
		}
	}
	m.currentQuestionIdx = newIdx
	if newIdx == maxIdx {
		m.selectedOption = 0
		m.selectedMulti = nil
		m.questionScrollOffset = 0
		m.typingCustom = false
		m.input.Reset()
		m.syncChatInputHeight()
		return
	}
	m.restoreQuestionState(newIdx)
	if newIdx < len(m.questionStates) {
		m.questionStates[newIdx].askedEmitted = true
	}
	m.syncChatInputHeight()
}

// updateChatQuestionScrollOffset adjusts questionScrollOffset so that
// selectedOption stays visible within the windowed option list (mirrors
// AttachModel.updateQuestionScrollOffset), using the same optionArea/
// contentWidth derivation as renderQuestionPicker.
func (m *ChatModel) updateChatQuestionScrollOffset() {
	if len(m.questions) == 0 || m.currentQuestionIdx >= len(m.questions) {
		return
	}
	q := m.questions[m.currentQuestionIdx]
	totalOptions := len(q.Options)

	if m.selectedOption >= totalOptions {
		return
	}

	contentWidth := m.chatContentWidth()
	totalLines := 0
	for i, o := range q.Options {
		totalLines += questionOptionLineCount(o, i, contentWidth)
	}
	optionArea := max(len(q.Options), 3)

	if totalLines <= optionArea {
		m.questionScrollOffset = 0
		return
	}

	if m.selectedOption < m.questionScrollOffset {
		m.questionScrollOffset = m.selectedOption
		return
	}

	_, end, _, _ := questionVisibleWindowPure(q.Options, m.selectedOption, m.questionScrollOffset, optionArea, contentWidth)
	if m.selectedOption < end {
		return
	}

	budget := optionArea - 1
	if m.selectedOption < totalOptions-1 {
		budget--
	}
	usedLines := 0
	newOffset := m.selectedOption
	for i := m.selectedOption; i >= 0; i-- {
		ol := questionOptionLineCount(q.Options[i], i, contentWidth)
		if usedLines+ol > budget {
			newOffset = i + 1
			break
		}
		usedLines += ol
		newOffset = i
	}
	m.questionScrollOffset = newOffset
}

// submitAllQuestionAnswers dispatches the collected answers via the same
// RespondToAskUser protocol chat.go already uses for a single pending
// question, clears picker state, and echoes each answer as a user turn.
func (m *ChatModel) submitAllQuestionAnswers() tea.Cmd {
	requestID := m.pendingAskRequestID
	raw := m.pendingAskRaw
	answers := m.collectedAnswers
	sess := m.sess

	for _, q := range m.questions {
		if a, ok := answers[q.Question]; ok && a != "" {
			if question := chatQuestionHistoryText(q); question != "" {
				m.turns = append(m.turns, chatTurn{Role: chatTurnAgent, Text: question})
			}
			m.turns = append(m.turns, chatTurn{Role: chatTurnUser, Text: a})
		}
	}

	if requestID != "" {
		if m.answeredAskRequestIDs == nil {
			m.answeredAskRequestIDs = make(map[string]struct{})
		}
		m.answeredAskRequestIDs[requestID] = struct{}{}
		if sess != nil {
			sess.ClearPendingQuestion(requestID)
		}
	}

	m.questions = nil
	m.questionStates = nil
	m.currentQuestionIdx = 0
	m.selectedOption = 0
	m.selectedMulti = nil
	m.questionScrollOffset = 0
	m.typingCustom = false
	m.collectedAnswers = nil
	m.pendingAskRequestID = ""
	m.pendingAskRaw = nil
	m.responding = true
	m.rebuildViewport()

	if sess == nil || requestID == "" {
		return nil
	}
	sendCmd := func() tea.Msg {
		if err := sess.RespondToAskUser(requestID, raw, answers, nil); err != nil {
			return chatSendErrorMsg{err: err}
		}
		return nil
	}
	if !m.pollSession {
		return sendCmd
	}
	m.turnCostBaseline = sess.Cost()
	return tea.Batch(sendCmd, chatRecoveryTickCmd(sess, m.turnCostBaseline))
}

// appendOptionPreview joins preview beside topBlock, side by side, when
// there is enough horizontal room within contentWidth. Returns topBlock
// unchanged if preview is empty or there isn't room.
func appendOptionPreview(topBlock, preview string, contentWidth int) string {
	if preview == "" {
		return topBlock
	}
	const gap = 2
	if maxLineWidth(topBlock)+gap+maxLineWidth(preview) > contentWidth {
		return topBlock
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, topBlock, strings.Repeat(" ", gap), preview)
}

// maxLineWidth returns the widest rendered line width across s.
func maxLineWidth(s string) int {
	w := 0
	for _, line := range strings.Split(s, "\n") {
		if lw := lipgloss.Width(line); lw > w {
			w = lw
		}
	}
	return w
}

func chatQuestionHistoryText(q askUserQuestion) string {
	if text := strings.TrimSpace(q.Question); text != "" {
		return text
	}
	return strings.TrimSpace(q.Header)
}

// renderQuestionPicker renders the active AskUserQuestion bundle inline.
func (m ChatModel) renderQuestionPicker() (body, footer string) {
	contentWidth := chatBottomPanelContentWidth(m.chatContentWidth())
	if m.onRecapSlot() {
		var b strings.Builder
		b.WriteString(lipgloss.NewStyle().Bold(true).Render("Review & Submit"))
		b.WriteString("\n\n")
		for _, q := range m.questions {
			if a, ok := m.collectedAnswers[q.Question]; ok {
				fmt.Fprintf(&b, "  %s → %s\n", q.Question, a)
			}
		}
		return b.String(), KeyHelpStyle.Render("[enter] Submit · [←] back · [esc] close")
	}
	q := m.questions[m.currentQuestionIdx]
	if questionUsesDirectFreeform(q) || m.typingCustom {
		body := lipgloss.NewStyle().Bold(true).Render(questionPromptText(q.Question, contentWidth)) + "\n\n" + m.input.View()
		return body, KeyHelpStyle.Render("[enter] Send · [shift+enter] Newline · [esc] Back")
	}
	optionArea := m.questionOptionArea(q, contentWidth)
	start, end, needAbove, needBelow := questionVisibleWindowPure(q.Options, m.selectedOption, m.questionScrollOffset, optionArea, contentWidth)
	typeIdx := len(q.Options)
	var typeRow string
	if m.selectedOption == typeIdx {
		typeRow = lipgloss.NewStyle().Foreground(colorBrand).Bold(true).Render(fmt.Sprintf("> %d. Type something.", typeIdx+1))
	} else {
		typeRow = lipgloss.NewStyle().Render(fmt.Sprintf("  %d. Type something.", typeIdx+1))
	}
	topBlock := lipgloss.NewStyle().Bold(true).Render(questionPromptText(q.Question, contentWidth)) + "\n\n" +
		renderQuestionOptionsBlock(q, m.selectedOption, m.selectedMulti, start, end, needAbove, needBelow, contentWidth) +
		typeRow

	if m.selectedOption < len(q.Options) {
		topBlock = appendOptionPreview(topBlock, q.Options[m.selectedOption].Preview, contentWidth)
	}

	canBack := m.currentQuestionIdx > 0
	_, canForward := m.collectedAnswers[q.Question]
	footerText := renderQuestionFooterHint(q, m.currentQuestionIdx, len(m.questions), canBack, canForward, questionPromptIsTruncated(q.Question, contentWidth), "")
	return topBlock, footerText
}

func (m ChatModel) questionOptionArea(q askUserQuestion, contentWidth int) int {
	promptLines := questionPromptLineCount(q.Question, contentWidth)
	available := m.activeQuestionBodyHeight(contentWidth) - promptLines - 1 - 1
	return max(available, 1)
}
