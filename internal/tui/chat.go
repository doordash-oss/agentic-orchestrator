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
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/utilskill"
)

// ChatExitMsg signals the chat view should close.
type ChatExitMsg struct{}

// chatMsgsMsg carries new SDK messages from the chat session's attach channel.
type chatMsgsMsg struct {
	messages []llm.SDKMessage
}

// chatDoneMsg signals a chat session has exited.
// sess identifies which session exited so stale messages from old sessions
// don't interfere with newly started sessions.
type chatDoneMsg struct {
	sess session.SessionView
}

// chatSendErrorMsg is returned when SendUserMessage fails on an existing session.
type chatSendErrorMsg struct {
	err error
}

// chatRecoveryTickMsg fires periodically while the chat is responding.
// It lets the UI notice a turn-complete Result that was recorded on the
// session but never reached the chat via attachCh (e.g. dropped under
// heavy streaming). baseline holds the Cost() pointer captured when the
// user sent the current turn — a newer pointer means a Result landed on
// the session, so responding can be cleared regardless of attachCh.
type chatRecoveryTickMsg struct {
	sess     session.SessionView
	baseline *llm.ResultMessage
}

// chatRecoveryInterval is how often the recovery tick fires while
// responding. Short enough that a dropped Result surfaces quickly, long
// enough to stay out of the way of normal in-band delivery.
const chatRecoveryInterval = 2 * time.Second

const chatSessionID = "__chat__"

// chatThinkingStyle styles the thinking/progress status line.
var chatThinkingStyle = lipgloss.NewStyle().
	Foreground(compat.AdaptiveColor{Light: lipgloss.Color("#6c6f85"), Dark: lipgloss.Color("#6c7086")}).
	Italic(true)

// chatTurnRole identifies who a chatTurn belongs to, for tag/color selection.
type chatTurnRole int

const (
	chatTurnUser chatTurnRole = iota
	chatTurnAgent
	chatTurnError
	chatTurnCancelled
)

// chatTurn is one entry in the AMA transcript. InProgress is true only for
// the trailing agent turn while a response is still streaming — it is set
// back to false and the text finalized once the turn's Result arrives.
type chatTurn struct {
	Role       chatTurnRole
	Text       string
	InProgress bool
	AutoPicked bool
	Confidence float64
}

// ChatModel provides a bottom-panel chat interface backed by a single
// long-running interactive claude session. Messages are sent via
// SendUserMessage and responses stream via the session's AttachCh.
type ChatModel struct {
	viewport     viewport.Model
	input        textarea.Model
	turns        []chatTurn // one entry per user/agent/error/cancelled turn
	thinkingLine string     // current thinking/progress status (overwritten on each update)
	spinnerView  string     // animated spinner frame, updated from the app-level spinner
	width        int
	height       int
	focused      bool
	fullscreen   bool                // true when the panel occupies the full terminal (ctrl+g toggles)
	responding   bool                // true while claude is generating
	sess         session.SessionView // the persistent interactive session (nil before first message)
	// AskUserQuestion picker state — shares its rendering and layout math
	// with attach.go via question_picker.go; navigation transitions below
	// are ChatModel's own (see docs/superpowers/specs/2026-07-07-ama-chat-redesign-design.md
	// Scope Notes for why this arithmetic isn't code-shared with AttachModel).
	questions            []askUserQuestion
	questionStates       []questionUIState
	currentQuestionIdx   int
	selectedOption       int
	selectedMulti        map[int]bool
	questionScrollOffset int
	typingCustom         bool
	collectedAnswers     map[string]string
	pendingAskRequestID  string
	pendingAskRaw        json.RawMessage
	// answeredAskRequestIDs remembers request IDs already submitted so a
	// stale re-delivery of the same AskUserQuestion (e.g. a snapshot poll
	// that hasn't caught up with the just-sent answer yet) doesn't
	// re-activate the picker and duplicate the agent turn.
	answeredAskRequestIDs map[string]struct{}
	// autoPickedSeen dedups auto-picked messages already synced into m.turns,
	// keyed by autoPickedMessageKey.
	autoPickedSeen map[string]struct{}
	inputHeight    int // current input textarea height in lines (1-6)
	// turnCostBaseline is the Cost() pointer observed at the moment the
	// current turn started. The recovery tick clears responding when
	// sess.Cost() no longer matches this baseline — i.e. a new Result was
	// recorded on the session even if the attachCh forward dropped it.
	turnCostBaseline *llm.ResultMessage
	sessionMgr       *session.Manager // reference for session access
	workDir          string           // working directory for claude
	systemPrompt     string           // chat system prompt
	buildSession     agent.BuildSessionFunc
	chatModel        string
	skillsDir        string
	startSession     func(string) tea.Msg
	pollSession      bool
}

func NewChatModel(width, height int, sm *session.Manager, workDir string, systemPrompt string, buildSession agent.BuildSessionFunc, chatModel string, skillsDir string) ChatModel {
	ta := newStyledTextarea()
	ta.Placeholder = "Ask me anything about Agentic Orchestrator..."
	ta.CharLimit = 4096
	ta.ShowLineNumbers = false
	ta.Focus()

	vp := viewport.New()
	// Strip single-letter vim bindings (j/k/f/b/u/d/h/l/space) that conflict
	// with typing in the textarea. Keep arrow and page keys for trackpad scroll.
	vp.KeyMap = viewport.KeyMap{
		Up:       key.NewBinding(key.WithKeys("up")),
		Down:     key.NewBinding(key.WithKeys("down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
	}

	m := ChatModel{
		viewport:     vp,
		input:        ta,
		width:        width,
		height:       height,
		focused:      true,
		sessionMgr:   sm,
		workDir:      workDir,
		systemPrompt: systemPrompt,
		buildSession: buildSession,
		chatModel:    chatModel,
		skillsDir:    skillsDir,
		pollSession:  true,
	}
	m = m.resize(width, height)
	return m
}

func NewAPIChatModel(width, height int, client APIClient) ChatModel {
	m := NewChatModel(width, height, nil, "", "", nil, "", "")
	m.startSession = func(initialQuestion string) tea.Msg {
		resp, err := client.StartChat(context.Background(), server.ChatStartRequest{Message: initialQuestion})
		if err != nil {
			return chatSendErrorMsg{err: fmt.Errorf("Error starting session: %w", err)}
		}
		return chatSessionStartedMsg{sess: newAPIChatSession(client, resp.SessionID)}
	}
	m.pollSession = false
	return m
}

// chatPanelHeight returns the docked-mode panel height for the given total
// terminal height. The floor is smaller when there's no conversation to
// show yet; the ceiling (18 rows / 35% of height) is unchanged from before
// this method existed as a free function of total height only.
func (m ChatModel) chatPanelHeight(totalHeight int) int {
	h := totalHeight * 35 / 100
	floor := 10
	if len(m.turns) == 0 && !m.responding {
		floor = 5
	}
	if h < floor {
		h = floor
	}
	if h > 18 {
		h = 18
	}
	return h
}

// resize recalculates layout dimensions for the bottom-panel layout.
// h is the total height allocated to the chat panel (including border).
func (m ChatModel) resize(w, h int) ChatModel {
	m.width = w
	m.height = h
	// Content width: total - frame (4) - margin (2)
	contentWidth := max(w-6, 40)
	inputHeight := 3
	footerHeight := 1
	borderHeight := 2
	separators := 2 // newlines between viewport, input, and footer
	vpHeight := max(h-inputHeight-footerHeight-borderHeight-separators, 3)

	m.viewport.SetWidth(contentWidth)
	m.viewport.SetHeight(vpHeight)
	m.input.SetWidth(contentWidth)
	m.input.SetHeight(inputHeight)
	return m
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

// renderChatTurn renders a single turn with its role tag, deferring to
// renderMarkdown for agent text so code blocks/lists/emphasis render
// correctly. Tag styling (colors, the animated agent tag) is added in
// Task 4/5 — this task only wires the markdown call and turn-based layout.
func renderChatTurn(t chatTurn, width int) string {
	switch t.Role {
	case chatTurnUser:
		label, style := autoPickedTag(t.AutoPicked, t.Confidence)
		return style.Render(label) + "  " + t.Text
	case chatTurnAgent:
		return chatAgentTagStyle.Render("[agent]") + "  " + renderMarkdown(t.Text, width)
	case chatTurnError:
		return chatAgentTagErrorStyle.Render("[agent]") + "  " + ErrorStyle.Render(t.Text)
	case chatTurnCancelled:
		return MutedStyle.Render("[cancelled]")
	default:
		return t.Text
	}
}

// renderAgentThinkingTag renders the "[agent]" tag with the current spinner
// frame in place of the static glyph, plus the muted tool-use/thinking
// snippet beside it — the signature element that makes the tag itself the
// turn's live status indicator instead of a separate "Thinking..." line.
func renderAgentThinkingTag(spinnerFrame, thinkingLine string) string {
	frame := spinnerFrame
	if frame == "" {
		frame = "·"
	}
	tag := chatAgentTagStyle.Render("[" + frame + " agent]")
	return tag + "  " + chatThinkingStyle.Render(thinkingLine)
}

// setInProgressAgentText updates (or starts) the trailing in-progress agent
// turn with newly streamed text. partial snapshots replace the turn's text
// (the SDK sends accumulated text, not deltas); a final block's text is the
// last write before finalizeInProgressTurn commits it.
func (m *ChatModel) setInProgressAgentText(text string, partial bool) {
	if n := len(m.turns); n > 0 && m.turns[n-1].Role == chatTurnAgent && m.turns[n-1].InProgress {
		m.turns[n-1].Text = text
		return
	}
	m.turns = append(m.turns, chatTurn{Role: chatTurnAgent, Text: text, InProgress: true})
}

// finalizeInProgressTurn marks the trailing in-progress agent turn (if any)
// as committed. No-op if there is no in-progress turn.
func (m *ChatModel) finalizeInProgressTurn() {
	if n := len(m.turns); n > 0 && m.turns[n-1].Role == chatTurnAgent && m.turns[n-1].InProgress {
		m.turns[n-1].InProgress = false
	}
}

// discardInProgressTurn removes the trailing in-progress agent turn (if
// any) entirely — used when an error replaces a partial response rather
// than following it.
func (m *ChatModel) discardInProgressTurn() {
	if n := len(m.turns); n > 0 && m.turns[n-1].Role == chatTurnAgent && m.turns[n-1].InProgress {
		m.turns = m.turns[:n-1]
	}
}

// syncAutoPickedTurns scans the session's message log for AutoPicked
// messages not yet reflected in m.turns and appends them. Auto-picked
// answers are synthesized directly into the session's message log by
// whatever decided to auto-answer (see appendMissingAutoPickedMessages),
// bypassing the streamed AttachCh entirely — this is why ChatModel must
// proactively rescan the log instead of only reacting to streamed messages.
func (m *ChatModel) syncAutoPickedTurns() {
	if m.sess == nil || m.sess.MessageLog() == nil {
		return
	}
	appendMissingAutoPickedMessages(m.sess)
	for _, msg := range m.sess.MessageLog().Messages() {
		if !msg.AutoPicked || msg.User == nil {
			continue
		}
		for _, block := range msg.User.Message.Content {
			if !block.IsText() || block.Text == "" {
				continue
			}
			key := autoPickedMessageKey(msg.AutoPickQuestion, block.Text, msg.AutoPickConfidence)
			if m.autoPickedSeen == nil {
				m.autoPickedSeen = make(map[string]struct{})
			}
			if _, seen := m.autoPickedSeen[key]; seen {
				continue
			}
			m.autoPickedSeen[key] = struct{}{}
			m.turns = append(m.turns, chatTurn{Role: chatTurnUser, Text: block.Text, AutoPicked: true, Confidence: msg.AutoPickConfidence})
		}
	}
}

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
	if idx < len(m.questionStates) {
		st := &m.questionStates[idx]
		st.scrollOffset = m.questionScrollOffset
		if m.typingCustom {
			st.typingCustom = true
			st.customText = answer
			st.selectedOption = len(q.Options)
			st.selectedMulti = nil
		} else {
			st.typingCustom = false
			st.customText = ""
			st.selectedOption = m.selectedOption
			if q.MultiSelect {
				if len(m.selectedMulti) == 0 {
					st.selectedMulti = map[int]bool{m.selectedOption: true}
				} else {
					st.selectedMulti = cloneIntBoolMap(m.selectedMulti)
				}
			} else {
				st.selectedMulti = nil
			}
		}
	}
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
func (m *ChatModel) advanceQuestionOpts(delta int, snapshot bool) bool {
	if len(m.questions) == 0 {
		return false
	}
	newIdx := m.currentQuestionIdx + delta
	maxIdx := len(m.questions)
	if newIdx < 0 || newIdx > maxIdx || newIdx == m.currentQuestionIdx {
		return false
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
		return true
	}
	m.restoreQuestionState(newIdx)
	if newIdx < len(m.questionStates) {
		m.questionStates[newIdx].askedEmitted = true
	}
	m.syncChatInputHeight()
	return true
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

	// "Type something" (index == totalOptions) is always visible below the
	// separator; no scroll adjustment needed for it.
	if m.selectedOption >= totalOptions {
		return
	}

	contentWidth := max(m.width-6, 40)
	totalLines := 0
	for i, o := range q.Options {
		totalLines += questionOptionLineCount(o, i, contentWidth)
	}
	optionArea := max(len(q.Options), 3)

	if totalLines <= optionArea {
		m.questionScrollOffset = 0
		return
	}

	// If selected is above the window, scroll up.
	if m.selectedOption < m.questionScrollOffset {
		m.questionScrollOffset = m.selectedOption
		return
	}

	// Already visible from current offset.
	_, end, _, _ := questionVisibleWindowPure(q.Options, m.selectedOption, m.questionScrollOffset, optionArea, contentWidth)
	if m.selectedOption < end {
		return
	}

	// Scroll down: find minimum offset so selectedOption is the last visible
	// option, working backwards from selectedOption until the budget runs out.
	budget := optionArea - 1 // reserve for "above" indicator
	if m.selectedOption < totalOptions-1 {
		budget-- // reserve for "below" indicator
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
			m.turns = append(m.turns, chatTurn{Role: chatTurnUser, Text: a})
		}
	}

	if requestID != "" {
		if m.answeredAskRequestIDs == nil {
			m.answeredAskRequestIDs = make(map[string]struct{})
		}
		m.answeredAskRequestIDs[requestID] = struct{}{}
		// Clear session state synchronously (mirrors AttachModel.submitAllAnswers)
		// so re-attaching before the async control_response write completes
		// does not re-show the question via LastControlRequest.
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

// syncChatInputHeight recalculates the chat input's height from its content,
// delegating the arithmetic to the shared helper from Task 1.
func (m *ChatModel) syncChatInputHeight() {
	h := syncTextareaHeight(m.input.Value(), 1, 6)
	if h != m.inputHeight {
		m.inputHeight = h
		m.input.SetHeight(h)
	}
}

func (m ChatModel) Update(msg tea.Msg) (ChatModel, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if m.onRecapSlot() {
			switch msg.String() {
			case "enter":
				return m, m.submitAllQuestionAnswers()
			case "left", "h":
				m.advanceQuestionOpts(-1, false)
				return m, nil
			case "esc":
				if m.fullscreen {
					m.fullscreen = false
					return m, nil
				}
				return m, func() tea.Msg { return ChatExitMsg{} }
			}
			return m, nil
		}
		if m.hasActiveQuestion() && !m.typingCustom {
			q := m.questions[m.currentQuestionIdx]
			if questionUsesDirectFreeform(q) {
				m.typingCustom = true
				return m, nil
			}
			numOptions := len(q.Options)
			switch msg.String() {
			case "up", "k":
				if m.selectedOption > 0 {
					m.selectedOption--
					m.updateChatQuestionScrollOffset()
				}
				return m, nil
			case "down", "j":
				if m.selectedOption < numOptions {
					m.selectedOption++
					m.updateChatQuestionScrollOffset()
				}
				return m, nil
			case "left", "h":
				if m.currentQuestionIdx > 0 {
					m.advanceQuestionOpts(-1, true)
				}
				return m, nil
			case "right", "l":
				if _, answered := m.collectedAnswers[q.Question]; answered {
					m.advanceQuestionOpts(1, true)
				}
				return m, nil
			case " ", "space":
				m.toggleSelectedMulti()
				return m, nil
			case "enter":
				if m.selectedOption < numOptions {
					var answer string
					if q.MultiSelect {
						var labels []string
						for i := range q.Options {
							if m.selectedMulti[i] {
								labels = append(labels, q.Options[i].Label)
							}
						}
						if len(labels) == 0 {
							labels = []string{q.Options[m.selectedOption].Label}
						}
						answer = strings.Join(labels, ", ")
					} else {
						answer = q.Options[m.selectedOption].Label
					}
					m.commitAnswer(answer)
					m.advanceQuestionOpts(1, false)
					return m, nil
				}
				m.selectedMulti = nil
				m.typingCustom = true
				if m.currentQuestionIdx < len(m.questionStates) {
					if prior := m.questionStates[m.currentQuestionIdx].customText; prior != "" {
						m.input.SetValue(prior)
					}
				}
				return m, nil
			case "esc":
				if m.fullscreen {
					m.fullscreen = false
					return m, nil
				}
				return m, func() tea.Msg { return ChatExitMsg{} }
			}
			return m, nil
		}
		if m.hasActiveQuestion() && m.typingCustom {
			switch {
			case key.Matches(msg, shiftEnterKey):
				m.inputHeight = growTextareaHeight(m.inputHeight, 6)
				m.input.SetHeight(m.inputHeight)
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				m.syncChatInputHeight()
				return m, cmd
			case msg.String() == "esc":
				if questionUsesDirectFreeform(m.questions[m.currentQuestionIdx]) {
					m.input.Reset()
					m.syncChatInputHeight()
					return m, nil
				}
				m.typingCustom = false
				return m, nil
			case msg.String() == "enter":
				text := strings.TrimSpace(m.input.Value())
				if text != "" {
					m.input.Reset()
					m.syncChatInputHeight()
					m.commitAnswer(text)
					m.advanceQuestionOpts(1, false)
				}
				return m, nil
			default:
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				m.syncChatInputHeight()
				return m, cmd
			}
		}
		switch msg.String() {
		case "ctrl+g":
			m.fullscreen = !m.fullscreen
			return m, nil

		case "esc":
			if m.fullscreen {
				m.fullscreen = false
				return m, nil
			}
			if m.responding {
				// Minimize panel — response continues in the background.
				// Use ctrl+c to actually cancel.
				return m, func() tea.Msg { return ChatExitMsg{} }
			}
			// If input is empty, exit chat. Otherwise, clear input.
			if strings.TrimSpace(m.input.Value()) == "" {
				return m, func() tea.Msg { return ChatExitMsg{} }
			}
			m.input.Reset()
			return m, nil

		case "ctrl+c":
			if m.responding || m.sess != nil {
				if m.sessionMgr != nil && m.sess != nil {
					_ = m.sessionMgr.StopSession(chatSessionID)
				}
				m.responding = false
				m.sess = nil
				m.thinkingLine = ""
				m.finalizeInProgressTurn()
				m.turns = append(m.turns, chatTurn{Role: chatTurnCancelled})
				m.rebuildViewport()
				return m, nil
			}
			return m, func() tea.Msg { return ChatExitMsg{} }

		case "enter":
			if m.responding {
				return m, nil
			}
			question := strings.TrimSpace(m.input.Value())
			if question == "" {
				return m, nil
			}
			m.input.Reset()

			// Append user turn
			m.turns = append(m.turns, chatTurn{Role: chatTurnUser, Text: question})

			if m.sessionMgr == nil && m.startSession == nil {
				m.turns = append(m.turns, chatTurn{Role: chatTurnError, Text: "no session manager available"})
				m.rebuildViewport()
				return m, nil
			}

			m.responding = true
			m.thinkingLine = ""
			m.rebuildViewport()

			if m.sess == nil {
				// First message (or after session died) — start a new interactive session.
				// No session yet, so no baseline to capture; the recovery tick starts
				// running only once the session is up (chatSessionStartedMsg).
				m.turnCostBaseline = nil
				return m, m.startSessionCmd(question)
			}

			// Subsequent message — send via existing session. Capture the
			// current Cost() pointer so the recovery tick can detect when a
			// new Result lands on the session (even if attachCh drops it).
			m.turnCostBaseline = m.sess.Cost()
			sess := m.sess
			sendCmd := func() tea.Msg {
				if err := sess.SendUserMessage(question); err != nil {
					return chatSendErrorMsg{err: err}
				}
				return nil
			}
			if !m.pollSession {
				return m, sendCmd
			}
			return m, tea.Batch(sendCmd, chatRecoveryTickCmd(sess, m.turnCostBaseline))
		}

	case chatSessionStartedMsg:
		m.sess = msg.sess
		// Baseline is whatever Cost() reads now (nil for a fresh session,
		// but don't rely on it). Arm the recovery tick so a dropped first-turn
		// Result still clears responding.
		m.turnCostBaseline = msg.sess.Cost()
		if !m.pollSession {
			return m, nil
		}
		return m, tea.Batch(pollChatChCmd(msg.sess), chatRecoveryTickCmd(msg.sess, m.turnCostBaseline))

	case chatMsgsMsg:
		if text, ok := chatErrorResponseText(msg.messages); ok {
			m.discardInProgressTurn()
			m.responding = false
			m.thinkingLine = ""
			m.turns = append(m.turns, chatTurn{Role: chatTurnError, Text: text})
			if m.sess != nil {
				m.turnCostBaseline = m.sess.Cost()
			}
			m.rebuildViewport()
			return m, nil
		}
		for _, sdkMsg := range msg.messages {
			if sdkMsg.Assistant != nil {
				hasText := false
				for _, block := range sdkMsg.Assistant.Message.Content {
					if block.IsText() && block.Text != "" {
						m.setInProgressAgentText(block.Text, sdkMsg.Subtype == "partial")
						hasText = true
					}
					if block.IsThinking() && block.Thinking != "" {
						thinking := block.Thinking
						if len(thinking) > 120 {
							thinking = thinking[len(thinking)-120:]
						}
						if idx := strings.LastIndex(thinking, "\n"); idx >= 0 {
							thinking = thinking[idx+1:]
						}
						m.thinkingLine = strings.TrimSpace(thinking)
					}
					if block.IsToolUse() {
						m.thinkingLine = fmt.Sprintf("Using %s...", block.Name)
					}
				}
				if hasText {
					m.thinkingLine = ""
				}
			}
			if sdkMsg.ToolProgress != nil {
				m.thinkingLine = fmt.Sprintf("Using %s...", sdkMsg.ToolProgress.ToolName)
			}
			if sdkMsg.Status != nil && sdkMsg.Status.Message != "" {
				m.thinkingLine = sdkMsg.Status.Message
			}
			if sdkMsg.Result != nil {
				m.finalizeInProgressTurn()
				m.responding = false
				m.thinkingLine = ""
				if m.sess != nil {
					m.turnCostBaseline = m.sess.Cost()
				}
			}
			if sdkMsg.ControlRequest != nil && sdkMsg.ControlRequest.Request.ToolName == "AskUserQuestion" && sdkMsg.ControlRequest.RequestID != m.pendingAskRequestID {
				if _, alreadyAnswered := m.answeredAskRequestIDs[sdkMsg.ControlRequest.RequestID]; alreadyAnswered {
					continue
				}
				questions := parseAskUserQuestions(sdkMsg.ControlRequest.Request.Input)
				if len(questions) > 0 && !m.hasActiveQuestion() && !askUserQuestionsAlreadyAutoPicked(m.sess, questions) {
					m.finalizeInProgressTurn()
					m.turns = append(m.turns, chatTurn{Role: chatTurnAgent, Text: questions[0].Question})
					m.activateQuestions(questions, sdkMsg.ControlRequest.RequestID, sdkMsg.ControlRequest.Request.Input)
					m.responding = false
					m.thinkingLine = ""
					if m.sess != nil {
						m.turnCostBaseline = m.sess.Cost()
					}
				}
			}
		}
		m.syncAutoPickedTurns()
		m.rebuildViewport()
		if m.pollSession && m.sess != nil {
			cmds = append(cmds, pollChatChCmd(m.sess))
		}

	case chatRecoveryTickMsg:
		// Fires periodically while responding. Compare the session's
		// current Cost() pointer to the baseline captured at turn start.
		// If it changed, a Result message was recorded on the session even
		// if attachCh dropped it — clear responding and flush the pending
		// partial so the user isn't stuck staring at "Thinking…". If it
		// didn't change and we're still responding, rearm.
		if msg.sess != m.sess {
			return m, nil
		}
		if !m.responding {
			return m, nil
		}
		if m.sess == nil {
			return m, nil
		}
		if cur := m.sess.Cost(); cur != nil && cur != msg.baseline {
			m.finalizeInProgressTurn()
			m.responding = false
			m.thinkingLine = ""
			m.turnCostBaseline = cur
			m.rebuildViewport()
			return m, nil
		}
		cmds = append(cmds, chatRecoveryTickCmd(m.sess, msg.baseline))

	case chatDoneMsg:
		if msg.sess != m.sess {
			return m, nil
		}
		m.finalizeInProgressTurn()
		m.responding = false
		m.thinkingLine = ""
		m.sess = nil
		m.rebuildViewport()
		return m, nil

	case chatSendErrorMsg:
		m.responding = false
		m.thinkingLine = ""
		m.sess = nil
		m.finalizeInProgressTurn()
		detail := ""
		if msg.err != nil {
			detail = strings.TrimSpace(msg.err.Error())
		}
		if detail == "" {
			detail = "session ended"
		}
		m.turns = append(m.turns, chatTurn{Role: chatTurnError, Text: detail})
		m.rebuildViewport()
		return m, nil

	case tea.WindowSizeMsg:
		m = m.resize(msg.Width, msg.Height)
	}

	// Forward to viewport for scroll handling
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	if vpCmd != nil {
		cmds = append(cmds, vpCmd)
	}

	// Forward to textarea
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// chatSessionStartedMsg is sent when the interactive session is ready.
type chatSessionStartedMsg struct {
	sess session.SessionView
}

// startSessionCmd launches the interactive claude session for the chat.
func (m ChatModel) startSessionCmd(initialQuestion string) tea.Cmd {
	return func() tea.Msg {
		if m.startSession != nil {
			return m.startSession(initialQuestion)
		}
		prompt := initialQuestion
		skillInstruction := chatSkillInstruction(m.skillsDir)
		if skillInstruction != "" {
			prompt = skillInstruction + "\n\n" + prompt
		}
		cmd, env, sessOpts, err := m.buildSession(agent.BuildSessionOpts{
			Model:           m.chatModel,
			Prompt:          prompt,
			SystemPrompt:    m.systemPrompt,
			DisallowedTools: []string{"Edit", "Write", "NotebookEdit", "Bash"},
			WorkDir:         m.workDir,
			PermHandler:     &session.ReadOnlyHandler{},
			Phase:           utilskill.PhaseAll, // chat gets all utility skills for answering user questions
			TurnMode:        ports.TurnModeInteractive,
		})
		if err != nil {
			return chatSendErrorMsg{err: fmt.Errorf("Error starting session: %w", err)}
		}
		sessOpts.InitialPrompt = prompt
		sess, err := m.sessionMgr.StartSession(chatSessionID, "", feature.PhaseResearch, cmd, m.workDir, env, sessOpts)
		if err != nil {
			return chatSendErrorMsg{err: fmt.Errorf("Error starting session: %w", err)}
		}
		return chatSessionStartedMsg{sess: sess}
	}
}

func chatSkillInstruction(skillsDir string) string {
	if skillsDir == "" {
		return ""
	}
	return fmt.Sprintf("Before starting your task, read the methodology instructions at: %s\n\nRead the file completely, then follow its instructions as you work on the task below.", filepath.Join(skillsDir, "chat", "SKILL.md"))
}

func chatErrorResponseText(messages []llm.SDKMessage) (string, bool) {
	var lastText string
	var hasErrorResult bool
	for _, msg := range messages {
		if msg.Assistant != nil {
			for _, block := range msg.Assistant.Message.Content {
				if block.IsText() && strings.TrimSpace(block.Text) != "" {
					lastText = strings.TrimSpace(block.Text)
				}
			}
		}
		if msg.Result != nil && chatResultIsError(msg.Result) {
			hasErrorResult = true
			if strings.TrimSpace(msg.Result.Result) != "" {
				lastText = strings.TrimSpace(msg.Result.Result)
			}
		}
	}
	if !hasErrorResult {
		return "", false
	}
	if lastText == "" {
		lastText = "Session error"
	}
	return lastText, true
}

func chatResultIsError(result *llm.ResultMessage) bool {
	if result == nil {
		return false
	}
	return result.IsError || result.Subtype == "error" || result.Subtype == "max_budget"
}

// chatRecoveryTickCmd schedules a chatRecoveryTickMsg after chatRecoveryInterval.
// The baseline is the Cost() pointer observed when the current turn began —
// when the session records a different pointer, the Result was delivered on
// the session side and responding can safely be cleared even if attachCh
// dropped the message mid-stream.
func chatRecoveryTickCmd(sess session.SessionView, baseline *llm.ResultMessage) tea.Cmd {
	return tea.Tick(chatRecoveryInterval, func(time.Time) tea.Msg {
		return chatRecoveryTickMsg{sess: sess, baseline: baseline}
	})
}

// pollChatChCmd reads messages from the session's AttachCh in batches.
func pollChatChCmd(sess session.SessionView) tea.Cmd {
	return func() tea.Msg {
		unregister := registerAttachConsumer(sess)
		defer unregister()
		ch := sess.AttachCh()
		msg, ok := <-ch
		if !ok {
			return chatDoneMsg{sess: sess}
		}
		msgs := []llm.SDKMessage{msg}
		for {
			select {
			case m, ok := <-ch:
				if !ok {
					return chatMsgsMsg{messages: msgs}
				}
				msgs = append(msgs, m)
			default:
				return chatMsgsMsg{messages: msgs}
			}
		}
	}
}

func (m ChatModel) View() string {
	vpContent := m.viewport.View()
	if len(m.turns) == 0 && !m.responding {
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
		footer = KeyHelpStyle.Render("[esc] Background   [ctrl+c] Cancel")
	default:
		bottom = m.input.View()
		footer = KeyHelpStyle.Render("[enter] Send · [shift+enter] Newline · [esc] Close")
	}

	inner := vpContent + "\n" + bottom + "\n" + footer

	box := panelStyle(true).
		Width(m.width).
		Height(m.height).
		Render(inner)
	box = renderBorderTitle(box, "Ask me Anything", lipgloss.NewStyle().Foreground(colorBrand))

	return box
}

// renderQuestionPicker renders the active AskUserQuestion bundle inline —
// the option list (or recap) plus its contextual footer hint — using the
// shared layout math and rendering from question_picker.go.
func (m ChatModel) renderQuestionPicker() (body, footer string) {
	contentWidth := max(m.width-6, 40)
	if m.onRecapSlot() {
		var b strings.Builder
		b.WriteString(lipgloss.NewStyle().Bold(true).Render("Review & Submit"))
		b.WriteString("\n\n")
		for _, q := range m.questions {
			if a, ok := m.collectedAnswers[q.Question]; ok {
				b.WriteString(fmt.Sprintf("  %s → %s\n", q.Question, a))
			}
		}
		return b.String(), KeyHelpStyle.Render("[enter] Submit · [←] back · [esc] close")
	}
	q := m.questions[m.currentQuestionIdx]
	if questionUsesDirectFreeform(q) {
		body := lipgloss.NewStyle().Bold(true).Render(questionPromptText(q.Question, contentWidth)) + "\n\n" + m.input.View()
		return body, KeyHelpStyle.Render("[enter] Send · [shift+enter] Newline · [esc] Back")
	}
	optionArea := max(len(q.Options), 3)
	start, end, needAbove, needBelow := questionVisibleWindowPure(q.Options, m.selectedOption, m.questionScrollOffset, optionArea, contentWidth)
	topBlock := lipgloss.NewStyle().Bold(true).Render(questionPromptText(q.Question, contentWidth)) + "\n\n" +
		renderQuestionOptionsBlock(q, m.selectedOption, m.selectedMulti, start, end, needAbove, needBelow)

	// Join the focused option's preview side-by-side when Claude supplied
	// one and it fits — same width guard as attach.go's renderQuestion, so
	// a narrow terminal collapses to options-only instead of overflowing.
	if m.selectedOption < len(q.Options) {
		if preview := q.Options[m.selectedOption].Preview; preview != "" {
			previewW := 0
			for _, line := range strings.Split(preview, "\n") {
				if w := lipgloss.Width(line); w > previewW {
					previewW = w
				}
			}
			leftNaturalW := 0
			for _, line := range strings.Split(topBlock, "\n") {
				if w := lipgloss.Width(line); w > leftNaturalW {
					leftNaturalW = w
				}
			}
			const gap = 2
			if leftNaturalW+gap+previewW <= contentWidth {
				topBlock = lipgloss.JoinHorizontal(lipgloss.Top, topBlock, strings.Repeat(" ", gap), preview)
			}
		}
	}

	canBack := m.currentQuestionIdx > 0
	_, canForward := m.collectedAnswers[q.Question]
	footerText := renderQuestionFooterHint(q, m.currentQuestionIdx, len(m.questions), canBack, canForward, questionPromptIsTruncated(q.Question, contentWidth), "")
	return topBlock, footerText
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

// isChatSession returns true for chat session IDs.
func isChatSession(sessionID string) bool {
	return sessionID == chatSessionID
}
