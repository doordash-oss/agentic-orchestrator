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
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// keyX are tea.KeyPressMsg.String() tokens for bubbletea key names shared
// across the chat, API app and help-overlay key handling.
const (
	keyDown  = "down"
	keyLeft  = "left"
	keyRight = "right"
	keySpace = "space"
	keyEnter = "enter"
	keyEsc   = "esc"
	keyPgUp  = "pgup"
)

// resultSubtypeError is the llm.ResultMessage.Subtype (and mirrored
// TranscriptMessageDTO status) value for an error-terminated turn.
const resultSubtypeError = "error"

// ChatExitMsg signals the chat view should close.
type ChatExitMsg struct{}

// These compatibility messages are still classified by APIAppModel, but the
// API-backed chat path no longer produces in-process stream or exit events.
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

const chatSessionID = server.ChatSessionID

const (
	chatInputMinLines                  = 1
	chatInputMaxLines                  = 6
	chatBorderHeight                   = 2
	chatFooterHeight                   = 1
	chatBottomPanelFooterGap           = 1
	chatTranscriptInputGapRows         = 2
	chatBottomPanelFrameHeight         = 2
	chatBottomPanelHFrame              = 4
	chatMinViewportHeight              = 1
	chatQuestionMinOptionLines         = 6
	chatActivePromptMaxHeight          = 24
	chatActivePromptPreferredBodyLines = 14
	chatActivePromptMaxBodyLines       = 16
)

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
	// Permission picker state. AMA surfaces top-level tool permissions inline
	// instead of relying on the feature-panel permission overlay.
	pendingPermRequestID   string
	pendingPermToolName    string
	pendingPermSummary     string
	pendingPermInput       json.RawMessage
	pendingPermRemember    *server.PermissionRememberPreviewDTO
	answeredPermRequestIDs map[string]struct{}
	inputHeight            int // current input textarea height in lines (1-6)
	// turnCostBaseline records the session cost when the current turn starts.
	// API refreshes update it as session snapshots arrive.
	turnCostBaseline *llm.ResultMessage
	startSession     func(string) tea.Msg
}

func newChatModel(width, height int) ChatModel {
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
		viewport: vp,
		input:    ta,
		width:    width,
		height:   height,
		focused:  true,
	}
	m = m.resize(width, height)
	return m
}

func NewAPIChatModel(width, height int, client APIClient) ChatModel {
	m := newChatModel(width, height)
	m.startSession = func(initialQuestion string) tea.Msg {
		resp, err := client.StartChat(context.Background(), server.ChatStartRequest{Message: initialQuestion})
		if err != nil {
			return chatSendErrorMsg{err: fmt.Errorf("error starting session: %w", err)}
		}
		return chatSessionStartedMsg{sess: newAPIChatSession(client, resp.SessionID)}
	}
	return m
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

// renderAgentThinkingTag renders the stable "[agent]" tag with the current
// spinner frame beside it, plus the muted tool-use/thinking snippet.
func renderAgentThinkingTag(spinnerFrame, thinkingLine string) string {
	frame := strings.TrimSpace(spinnerFrame)
	if frame == "" {
		frame = "·"
	}
	tag := chatAgentTagStyle.Render("[agent]")
	return tag + "  " + frame + " " + chatThinkingStyle.Render(thinkingLine)
}

// setInProgressAgentText updates (or starts) the trailing in-progress agent
// turn with newly streamed text. partial snapshots replace the turn's text
// (the SDK sends accumulated text, not deltas); a final block's text is the
// last write before finalizeInProgressTurn commits it.
func (m *ChatModel) setInProgressAgentText(text string, partial bool) {
	if n := len(m.turns); n > 0 && m.turns[n-1].Role == chatTurnAgent && m.turns[n-1].InProgress {
		if partial {
			m.turns[n-1].Text = text
		} else {
			m.turns[n-1].Text = mergeAssistantText(m.turns[n-1].Text, text)
		}
		return
	}
	m.turns = append(m.turns, chatTurn{Role: chatTurnAgent, Text: text, InProgress: true})
}

func mergeAssistantText(existing, next string) string {
	if strings.TrimSpace(existing) == "" {
		return next
	}
	if strings.TrimSpace(next) == "" {
		return existing
	}
	if strings.Contains(next, existing) {
		return next
	}
	if strings.Contains(existing, next) {
		return existing
	}
	separator := "\n\n"
	if strings.HasSuffix(existing, "\n") || strings.HasPrefix(next, "\n") {
		separator = ""
	}
	return existing + separator + next
}

// finalizeInProgressTurn marks the trailing in-progress agent turn (if any)
// as committed. No-op if there is no in-progress turn.
func (m *ChatModel) finalizeInProgressTurn() {
	if n := len(m.turns); n > 0 && m.turns[n-1].Role == chatTurnAgent && m.turns[n-1].InProgress {
		m.turns[n-1].InProgress = false
	}
}

func (m ChatModel) hasInProgressAgentText() bool {
	n := len(m.turns)
	return n > 0 && m.turns[n-1].Role == chatTurnAgent && m.turns[n-1].InProgress && strings.TrimSpace(m.turns[n-1].Text) != ""
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

// handleActiveQuestionKeyPress handles a key press while an active question
// is showing its option picker (not the freeform-text entry mode).
func (m ChatModel) handleActiveQuestionKeyPress(msg tea.KeyPressMsg) (ChatModel, tea.Cmd) {
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
	case keyDown, "j":
		if m.selectedOption < numOptions {
			m.selectedOption++
			m.updateChatQuestionScrollOffset()
		}
		return m, nil
	case keyLeft, "h":
		if m.currentQuestionIdx > 0 {
			m.advanceQuestionOpts(-1, true)
		}
		return m, nil
	case keyRight, "l":
		if _, answered := m.collectedAnswers[q.Question]; answered {
			m.advanceQuestionOpts(1, true)
		}
		return m, nil
	case " ", keySpace:
		m.toggleSelectedMulti()
		return m, nil
	case keyEnter:
		return m.handleActiveQuestionEnter(q, numOptions)
	case keyEsc:
		if m.fullscreen {
			m.fullscreen = false
			return m, nil
		}
		return m, func() tea.Msg { return ChatExitMsg{} }
	}
	return m, nil
}

// handleActiveQuestionEnter handles pressing enter on the option picker: it
// commits the selected option(s) if one is selected, or otherwise switches
// to freeform custom-text entry (restoring any previously typed draft).
func (m ChatModel) handleActiveQuestionEnter(q askUserQuestion, numOptions int) (ChatModel, tea.Cmd) {
	if m.selectedOption < numOptions {
		m.commitAnswer(m.selectedAnswerText(q))
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
}

// selectedAnswerText renders the currently selected option(s) as the answer
// text to commit: the comma-joined labels of checked options for multi-select
// questions (falling back to the highlighted option if none are checked), or
// the highlighted option's label otherwise.
func (m ChatModel) selectedAnswerText(q askUserQuestion) string {
	if !q.MultiSelect {
		return q.Options[m.selectedOption].Label
	}
	var labels []string
	for i := range q.Options {
		if m.selectedMulti[i] {
			labels = append(labels, q.Options[i].Label)
		}
	}
	if len(labels) == 0 {
		labels = []string{q.Options[m.selectedOption].Label}
	}
	return strings.Join(labels, ", ")
}

func (m ChatModel) Update(msg tea.Msg) (ChatModel, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+g" {
			m.fullscreen = !m.fullscreen
			return m, nil
		}
		if m.onRecapSlot() {
			switch msg.String() {
			case keyEnter:
				return m, m.submitAllQuestionAnswers()
			case keyLeft, "h":
				m.advanceQuestionOpts(-1, false)
				return m, nil
			case keyEsc:
				if m.fullscreen {
					m.fullscreen = false
					return m, nil
				}
				return m, func() tea.Msg { return ChatExitMsg{} }
			}
			return m, nil
		}
		if m.hasActivePermission() {
			switch msg.String() {
			case "y":
				return m.submitPermissionDecision(permission.DecisionAllowOnce)
			case "A":
				if m.pendingPermRemember != nil {
					return m.submitPermissionDecision(permission.DecisionAllowRemember)
				}
				return m, nil
			case "n":
				return m.submitPermissionDecision(permission.DecisionDeny)
			case keyEsc:
				if m.fullscreen {
					m.fullscreen = false
					return m, nil
				}
				return m, func() tea.Msg { return ChatExitMsg{} }
			}
			return m, nil
		}
		if m.hasActiveQuestion() && !m.typingCustom {
			return m.handleActiveQuestionKeyPress(msg)
		}
		if m.hasActiveQuestion() && m.typingCustom {
			switch {
			case key.Matches(msg, shiftEnterKey):
				m.inputHeight = growTextareaHeight(m.inputHeight)
				m.input.SetHeight(m.inputHeight)
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				m.syncChatInputHeight()
				return m, cmd
			case msg.String() == keyEsc:
				if questionUsesDirectFreeform(m.questions[m.currentQuestionIdx]) {
					m.input.Reset()
					m.syncChatInputHeight()
					return m, nil
				}
				m.typingCustom = false
				return m, nil
			case msg.String() == keyEnter:
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
		case "shift+enter":
			m.inputHeight = growTextareaHeight(m.inputHeight)
			m.input.SetHeight(m.inputHeight)
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m.syncChatInputHeight()
			return m, cmd

		case keyEsc:
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
				m.responding = false
				m.sess = nil
				m.thinkingLine = ""
				m.finalizeInProgressTurn()
				m.turns = append(m.turns, chatTurn{Role: chatTurnCancelled})
				m.rebuildViewport()
				return m, nil
			}
			return m, func() tea.Msg { return ChatExitMsg{} }

		case keyEnter:
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

			if m.startSession == nil {
				m.turns = append(m.turns, chatTurn{Role: chatTurnError, Text: "chat API unavailable"})
				m.rebuildViewport()
				return m, nil
			}

			m.responding = true
			m.thinkingLine = ""
			m.rebuildViewport()

			if m.sess == nil {
				// First message (or after session died) starts a REST-backed chat
				// session. APIAppModel schedules snapshot recovery after it starts.
				m.turnCostBaseline = nil
				return m, m.startSessionCmd(question)
			}

			// Subsequent messages use the API session adapter. Return a recovery
			// tick so APIAppModel fetches a fresh session snapshot.
			m.turnCostBaseline = m.sess.Cost()
			sess := m.sess
			return m, func() tea.Msg {
				if err := sess.SendUserMessage(question); err != nil {
					return chatSendErrorMsg{err: err}
				}
				return chatRecoveryTickMsg{sess: sess, baseline: m.turnCostBaseline}
			}
		}

	case chatSessionStartedMsg:
		m.sess = msg.sess
		m.turnCostBaseline = msg.sess.Cost()
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

// startSessionCmd starts a REST-backed chat session.
func (m ChatModel) startSessionCmd(initialQuestion string) tea.Cmd {
	return func() tea.Msg {
		return m.startSession(initialQuestion)
	}
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
