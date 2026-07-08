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
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// chatTurnsContainText reports whether any turn's Text contains substr.
func chatTurnsContainText(turns []chatTurn, substr string) bool {
	for _, turn := range turns {
		if strings.Contains(turn.Text, substr) {
			return true
		}
	}
	return false
}

func TestChatModelEscExitsWhenEmpty(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test prompt", nil, "", "")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	_ = updated
	if cmd == nil {
		t.Fatal("expected non-nil cmd for esc with empty input")
	}
	msg := cmd()
	if _, ok := msg.(ChatExitMsg); !ok {
		t.Errorf("expected ChatExitMsg, got %T", msg)
	}
}

func TestChatModelEscClearsInput(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test prompt", nil, "", "")
	m.input.SetValue("some text")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Error("expected nil command on esc with non-empty input")
	}
	if updated.input.Value() != "" {
		t.Error("expected input to be cleared")
	}
}

func TestChatModelCtrlCExitsWhenNotResponding(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test prompt", nil, "", "")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("expected non-nil cmd for ctrl+c")
	}
	msg := cmd()
	if _, ok := msg.(ChatExitMsg); !ok {
		t.Errorf("expected ChatExitMsg, got %T", msg)
	}
}

func TestChatModelViewEmptyState(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test prompt", nil, "", "")
	view := m.View()
	if !strings.Contains(view, "Ask me Anything") {
		t.Error("expected title in view")
	}
	if !strings.Contains(view, "Ask anything about Agentic Orchestrator") {
		t.Error("expected empty state hint in view")
	}
}

func TestChatModelViewDoesNotUseDarkTextareaCursorLine(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test prompt", nil, "", "")
	view := m.View()
	if strings.Contains(view, "\x1b[40m") || strings.Contains(view, "\x1b[48;5;0m") {
		t.Fatalf("chat view rendered Bubble textarea's dark cursor-line background: %q", view)
	}
}

func TestChatModelViewFitsAllocatedEmptyPanelHeight(t *testing.T) {
	m := NewChatModel(100, 8, nil, "/tmp", "test prompt", nil, "", "")
	if got := lipgloss.Height(m.View()); got > m.height {
		t.Fatalf("empty chat view height = %d, want <= allocated height %d", got, m.height)
	}
}

func TestChatModelEscWhileRespondingMinimizes(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test prompt", nil, "", "")
	m.responding = true

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !updated.responding {
		t.Error("expected responding to remain true (session not cancelled)")
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd for esc while responding")
	}
	msg := cmd()
	if _, ok := msg.(ChatExitMsg); !ok {
		t.Errorf("expected ChatExitMsg, got %T", msg)
	}
}

func TestChatModelViewShowsFooter(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test prompt", nil, "", "")
	view := m.View()
	if !strings.Contains(view, "[enter] Send") {
		t.Error("expected [enter] Send in footer")
	}
	if !strings.Contains(view, "[esc] Close") {
		t.Error("expected [esc] Close in footer")
	}
}

func TestChatModelViewShowsRespondingFooter(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test prompt", nil, "", "")
	m.responding = true
	view := m.View()
	if !strings.Contains(view, "[esc] Background") {
		t.Error("expected [esc] Background in footer when responding")
	}
	if !strings.Contains(view, "[ctrl+c] Cancel") {
		t.Error("expected [ctrl+c] Cancel in footer when responding")
	}
}

func TestChatModelEnterWithEmptyInputIsNoop(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test prompt", nil, "", "")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("expected nil cmd for enter with empty input")
	}
}

func TestChatModelEnterWithNoSessionMgrShowsError(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test prompt", nil, "", "")
	m.input.SetValue("hello")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !chatTurnsContainText(updated.turns, "no session manager available") {
		t.Errorf("expected error turn in turns when no session manager, got: %+v", updated.turns)
	}
}

func TestChatModelResize(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test prompt", nil, "", "")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if updated.width != 120 {
		t.Errorf("width = %d, want 120", updated.width)
	}
	if updated.height != 40 {
		t.Errorf("height = %d, want 40", updated.height)
	}
}

func TestChatModelHistorySurvivesMultipleUpdateCycles(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test prompt", nil, "", "")
	m.input.SetValue("first question")
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !chatTurnsContainText(m.turns, "first question") {
		t.Fatal("expected first question in turns after first update")
	}
	m.input.SetValue("second question")
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !chatTurnsContainText(m.turns, "second question") {
		t.Fatal("expected second question in turns after second update")
	}
	if !chatTurnsContainText(m.turns, "first question") {
		t.Fatal("expected first question still in turns after second update")
	}
}

func TestChatMsgsMsgStreaming(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test", nil, "", "")
	m.responding = true
	// Simulate a session so polling continues
	m.sess = session.NewSession("", "", 0)

	// Simulate receiving assistant messages via chatMsgsMsg
	msgs := chatMsgsMsg{
		messages: []llm.SDKMessage{
			{
				Type: "assistant",
				Assistant: &llm.AssistantMessage{
					Message: llm.ConversationMsg{
						Content: []llm.ContentBlock{
							{Type: "text", Text: "Hello world"},
						},
					},
				},
			},
		},
	}
	updated, cmd := m.Update(msgs)
	if !chatTurnsContainText(updated.turns, "Hello world") {
		t.Fatalf("expected 'Hello world' in turns, got %+v", updated.turns)
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd to continue listening")
	}

	// Simulate a result message (turn complete)
	resultMsgs := chatMsgsMsg{
		messages: []llm.SDKMessage{
			{
				Type:   "result",
				Result: &llm.ResultMessage{Subtype: "success"},
			},
		},
	}
	updated, _ = updated.Update(resultMsgs)
	if updated.responding {
		t.Error("expected responding to be false after result message")
	}
}

func TestChatMsgsMsgAppendsNonPartialAssistantTextFragments(t *testing.T) {
	m := NewChatModel(100, 24, nil, "/tmp", "test", nil, "", "")
	m.responding = true

	messages := []llm.SDKMessage{
		{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
				Content: []llm.ContentBlock{{Type: "text", Text: "## Feature State\n\nStatus discrepancy: feature.yaml says CodeReady."}},
			}},
		},
		{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
				Content: []llm.ContentBlock{{Type: "text", Text: "Run 001 (single run) is complete and published."}},
			}},
		},
		{Type: "result", Result: &llm.ResultMessage{Subtype: "success"}},
	}

	updated, _ := m.Update(chatMsgsMsg{messages: messages})
	if updated.responding {
		t.Fatal("chat remained responding after result")
	}
	if len(updated.turns) != 1 {
		t.Fatalf("turn count = %d, want one assistant turn: %+v", len(updated.turns), updated.turns)
	}
	text := updated.turns[0].Text
	for _, want := range []string{"Feature State", "Status discrepancy", "Run 001", "complete and published"} {
		if !strings.Contains(text, want) {
			t.Fatalf("assistant turn missing %q after fragment merge:\n%s", want, text)
		}
	}
}

func TestChatMsgsMsgSnapshotDrivenModeDoesNotPollSessionChannel(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test", nil, "", "")
	m.responding = true
	m.pollSession = false
	m.sess = session.NewSession("", "", 0)

	updated, cmd := m.Update(chatMsgsMsg{
		messages: []llm.SDKMessage{
			{
				Type: "assistant",
				Assistant: &llm.AssistantMessage{
					Message: llm.ConversationMsg{
						Content: []llm.ContentBlock{{Type: "text", Text: "Snapshot answer"}},
					},
				},
			},
		},
	})

	if !chatTurnsContainText(updated.turns, "Snapshot answer") {
		t.Fatalf("expected snapshot answer in turns, got %+v", updated.turns)
	}
	if cmd != nil {
		t.Fatal("expected nil cmd in snapshot-driven mode")
	}
}

func TestChatDoneMsgResetsState(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test", nil, "", "")
	m.responding = true
	sess := session.NewSession("", "", 0)
	m.sess = sess

	// chatDoneMsg must match the current session to take effect
	updated, cmd := m.Update(chatDoneMsg{sess: sess})
	if updated.responding {
		t.Error("expected responding to be false after chatDoneMsg")
	}
	if updated.sess != nil {
		t.Error("expected sess to be nil after chatDoneMsg")
	}
	if cmd != nil {
		t.Error("expected nil cmd after chatDoneMsg")
	}
}

func TestChatDoneMsgIgnoresStaleSession(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test", nil, "", "")
	m.responding = true
	currentSess := session.NewSession("", "", 0)
	m.sess = currentSess

	// chatDoneMsg from an old (different) session should be ignored
	oldSess := session.NewSession("", "", 0)
	updated, _ := m.Update(chatDoneMsg{sess: oldSess})
	if !updated.responding {
		t.Error("expected responding to remain true for stale chatDoneMsg")
	}
	if updated.sess != currentSess {
		t.Error("expected sess to remain unchanged for stale chatDoneMsg")
	}
}

func TestChatSendErrorMsgResetsState(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test", nil, "", "")
	m.responding = true
	m.sess = session.NewSession("", "", 0)

	updated, cmd := m.Update(chatSendErrorMsg{err: nil})
	if updated.responding {
		t.Error("expected responding to be false after send error")
	}
	if updated.sess != nil {
		t.Error("expected sess to be nil after send error")
	}
	if !chatTurnsContainText(updated.turns, "session ended") {
		t.Error("expected reconnect hint in turns")
	}
	if cmd != nil {
		t.Error("expected nil cmd after send error")
	}
}

func TestIsChatSession(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"__chat__", true},
		{"abc123-research", false},
		{"abc123-plan", false},
		{"", false},
		{"__chat__0", false}, // old format no longer matches
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := isChatSession(tt.id); got != tt.want {
				t.Errorf("isChatSession(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestChatStartSessionUsesCallback(t *testing.T) {
	var capturedOpts agent.BuildSessionOpts
	var called bool
	mockBuildSession := func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		called = true
		capturedOpts = opts
		return nil, nil, nil, fmt.Errorf("test: stop here")
	}

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	m := NewChatModel(80, 24, sm, "/tmp", "test system prompt", mockBuildSession, "sonnet", "")
	cmd := m.startSessionCmd("hello world")
	// Execute the command
	msg := cmd()

	if !called {
		t.Fatal("BuildSession callback was not called")
	}
	if capturedOpts.Model != "sonnet" {
		t.Errorf("expected model 'sonnet', got %q", capturedOpts.Model)
	}
	if capturedOpts.SystemPrompt != "test system prompt" {
		t.Errorf("expected system prompt 'test system prompt', got %q", capturedOpts.SystemPrompt)
	}
	if capturedOpts.TurnMode != ports.TurnModeInteractive {
		t.Errorf("expected interactive turn mode, got %v", capturedOpts.TurnMode)
	}
	if capturedOpts.EffortLevel != llm.EffortLow {
		t.Errorf("expected low effort for chat, got %q", capturedOpts.EffortLevel)
	}
	// Check disallowed tools
	expectedDisallowed := []string{"Edit", "Write", "NotebookEdit", "Bash"}
	if !reflect.DeepEqual(capturedOpts.DisallowedTools, expectedDisallowed) {
		t.Errorf("expected disallowed tools %v, got %v", expectedDisallowed, capturedOpts.DisallowedTools)
	}
	// Verify it returned a terminal error message (since our callback returned an error).
	errMsg, ok := msg.(chatSendErrorMsg)
	if !ok {
		t.Fatalf("expected chatSendErrorMsg, got %T", msg)
	}
	if errMsg.err == nil || !strings.Contains(errMsg.err.Error(), "test: stop here") {
		t.Fatal("expected error message")
	}
}

func TestChatUsesConfiguredModel(t *testing.T) {
	var capturedOpts agent.BuildSessionOpts
	mockBuildSession := func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		capturedOpts = opts
		return nil, nil, nil, fmt.Errorf("test: stop here")
	}

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	m := NewChatModel(80, 24, sm, "/tmp", "test", mockBuildSession, "opus", "")
	cmd := m.startSessionCmd("test question")
	cmd()

	if capturedOpts.Model != "opus" {
		t.Errorf("expected model 'opus', got %q", capturedOpts.Model)
	}
}

// TestChatUsesConfiguredProviderModel proves chat hands an explicit provider
// model selection to the normal provider-neutral session builder unchanged, and
// stays intentionally markerless — chat is a conversational surface with no
// phase_complete contract, so it must never thread a marker path.
func TestChatUsesConfiguredProviderModel(t *testing.T) {
	var capturedOpts agent.BuildSessionOpts
	mockBuildSession := func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		capturedOpts = opts
		return nil, nil, nil, fmt.Errorf("test: stop here")
	}

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	const routedModel = "gateway:vendor/model"
	m := NewChatModel(80, 24, sm, "/tmp", "test", mockBuildSession, routedModel, "")
	cmd := m.startSessionCmd("test question")
	cmd()

	if capturedOpts.Model != routedModel {
		t.Errorf("expected model %q, got %q", routedModel, capturedOpts.Model)
	}
	if capturedOpts.MarkerPath != "" {
		t.Errorf("chat threaded a marker path %q; want markerless", capturedOpts.MarkerPath)
	}
}

// TestChatRecoveryTickClearsRespondingWhenResultArrives verifies the
// defensive safety net: if the session records a Result (Cost() returns
// a new pointer) but the attachCh forward dropped the SDKMessage, the
// periodic recovery tick clears responding so the chat doesn't hang in
// "Thinking…" forever.
func TestChatRecoveryTickClearsRespondingWhenResultArrives(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test", nil, "", "")
	sess := mocks.NewMockSessionView("__chat__", "")
	m.sess = sess
	m.responding = true
	m.thinkingLine = "Using Agent..."
	m.turns = append(m.turns, chatTurn{Role: chatTurnAgent, Text: "partial answer", InProgress: true})
	m.turnCostBaseline = nil // baseline before the Result landed

	// Simulate: Claude finished the turn and the session recorded the
	// Result on Cost() — but attachCh dropped the SDKMessage.
	sess.CostVal = &llm.ResultMessage{Type: "result", Subtype: "success"}

	updated, _ := m.Update(chatRecoveryTickMsg{sess: sess, baseline: nil})

	if updated.responding {
		t.Fatal("expected responding=false after recovery tick observed a new Result")
	}
	if updated.thinkingLine != "" {
		t.Errorf("expected thinkingLine cleared, got %q", updated.thinkingLine)
	}
	n := len(updated.turns)
	if n == 0 || updated.turns[n-1].InProgress || updated.turns[n-1].Text != "partial answer" {
		t.Errorf("expected in-progress turn finalized with text preserved, got %+v", updated.turns)
	}
	if updated.turnCostBaseline != sess.CostVal {
		t.Errorf("expected baseline advanced to new Cost pointer")
	}
}

// TestChatRecoveryTickRearmsWhenNoNewResult verifies the tick re-arms
// itself while responding and no Result has arrived, so we keep
// watching. Without this, a dropped-Result scenario could only recover
// once — subsequent turns would have no safety net.
func TestChatRecoveryTickRearmsWhenNoNewResult(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test", nil, "", "")
	sess := mocks.NewMockSessionView("__chat__", "")
	m.sess = sess
	m.responding = true
	m.turnCostBaseline = nil
	// No Result yet — Cost stays nil.

	updated, cmd := m.Update(chatRecoveryTickMsg{sess: sess, baseline: nil})

	if !updated.responding {
		t.Fatal("expected responding to remain true when no new Result observed")
	}
	if cmd == nil {
		t.Fatal("expected rearmed tick command, got nil")
	}
}

// TestChatRecoveryTickIgnoredForStaleSession verifies the tick only
// acts on the current session. A tick left over from a previous session
// (after ctrl+c + new message) must not touch the new session's state.
func TestChatRecoveryTickIgnoredForStaleSession(t *testing.T) {
	m := NewChatModel(80, 24, nil, "/tmp", "test", nil, "", "")
	currentSess := mocks.NewMockSessionView("__chat__", "")
	currentSess.CostVal = &llm.ResultMessage{Type: "result"}
	m.sess = currentSess
	m.responding = true
	m.turnCostBaseline = currentSess.CostVal // already seen

	// Tick references an OLD session (different pointer).
	oldSess := mocks.NewMockSessionView("__chat__", "")
	updated, cmd := m.Update(chatRecoveryTickMsg{sess: oldSess, baseline: nil})

	if !updated.responding {
		t.Error("expected responding unchanged for stale session tick")
	}
	if cmd != nil {
		t.Error("expected no rearm for stale session tick")
	}
}

func TestChatStartSession_SkillReadInstruction(t *testing.T) {
	var capturedOpts agent.BuildSessionOpts
	var called bool
	mockBuildSession := func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		called = true
		capturedOpts = opts
		return nil, nil, nil, fmt.Errorf("test: stop here")
	}

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	skillsDir := t.TempDir()
	m := NewChatModel(80, 24, sm, "/tmp", "test system prompt", mockBuildSession, "sonnet", skillsDir)
	cmd := m.startSessionCmd("hello world")
	cmd()

	if !called {
		t.Fatal("BuildSession callback was not called")
	}

	// System prompt should be the provided one (no raw chat template)
	if capturedOpts.SystemPrompt != "test system prompt" {
		t.Errorf("expected system prompt 'test system prompt', got %q", capturedOpts.SystemPrompt)
	}

	// User prompt should contain skill-read instruction for chat
	expectedSkillPath := filepath.Join(skillsDir, "chat", "SKILL.md")
	if !strings.Contains(capturedOpts.Prompt, expectedSkillPath) {
		t.Errorf("chat prompt missing skill-read instruction, expected path %q in prompt %q", expectedSkillPath, capturedOpts.Prompt)
	}

	// User prompt should still contain the original question
	if !strings.Contains(capturedOpts.Prompt, "hello world") {
		t.Errorf("chat prompt missing original question 'hello world'")
	}
}

func TestChatModelAppendsUserAndAgentTurns(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.turns = append(m.turns, chatTurn{Role: chatTurnUser, Text: "hello"})
	m.turns = append(m.turns, chatTurn{Role: chatTurnAgent, Text: "**hi**", InProgress: false})
	m.rebuildViewport()
	content := m.viewport.View()
	if !strings.Contains(content, "hello") {
		t.Errorf("viewport missing user turn text: %q", content)
	}
}

func TestChatModelRendersAgentTextThroughMarkdown(t *testing.T) {
	old := renderMarkdown
	defer func() { renderMarkdown = old }()
	var gotWidth int
	renderMarkdown = func(text string, width int) string {
		gotWidth = width
		return "RENDERED:" + text
	}
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.turns = append(m.turns, chatTurn{Role: chatTurnAgent, Text: "hello"})
	m.rebuildViewport()
	content := m.viewport.View()
	if !strings.Contains(content, "RENDERED:hello") {
		t.Errorf("expected agent turn text to pass through renderMarkdown, got: %q", content)
	}
	if gotWidth <= 0 {
		t.Errorf("expected a positive width passed to renderMarkdown, got %d", gotWidth)
	}
}

func TestChatModelShowsSpinnerInAgentTag(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.responding = true
	m.spinnerView = "⣹"
	m.thinkingLine = "Using Read..."
	m.rebuildViewport()
	content := m.viewport.View()
	if !strings.Contains(content, "⣹") {
		t.Errorf("expected spinner frame in viewport while responding, got: %q", content)
	}
	if !strings.Contains(content, "agent") {
		t.Errorf("expected the agent tag to still be visible while responding, got: %q", content)
	}
	if !strings.Contains(content, "Using Read...") {
		t.Errorf("expected the thinking line text to be visible, got: %q", content)
	}
}

func TestRenderAgentThinkingTagKeepsStableAgentLabel(t *testing.T) {
	rendered := stripANSI(renderAgentThinkingTag("::", "Using Read..."))
	if !strings.Contains(rendered, "[agent]") {
		t.Fatalf("thinking tag missing stable [agent] label: %q", rendered)
	}
	if strings.Contains(rendered, "[:: agent]") {
		t.Fatalf("thinking tag embedded spinner inside agent label: %q", rendered)
	}
	if !strings.Contains(rendered, "::") || !strings.Contains(rendered, "Using Read...") {
		t.Fatalf("thinking tag missing spinner or status text: %q", rendered)
	}
}

func TestChatModelActivatesQuestionPicker(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.activateQuestions([]askUserQuestion{{Question: "Pick one", Options: []askUserOption{{Label: "A"}, {Label: "B"}}}}, "req-1", nil)
	if !m.hasActiveQuestion() {
		t.Fatal("expected an active question after activateQuestions")
	}
	if m.selectedOption != 0 {
		t.Errorf("selectedOption = %d, want 0", m.selectedOption)
	}
}

func TestChatModelQuestionNavAndSubmit(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.activateQuestions([]askUserQuestion{
		{Question: "First?", Options: []askUserOption{{Label: "A"}, {Label: "B"}}},
		{Question: "Second?", Options: []askUserOption{{Label: "C"}, {Label: "D"}}},
	}, "req-1", nil)

	// Move selection down to "B" and submit it.
	m.selectedOption = 1
	m.commitAnswer("B")
	m.advanceQuestionOpts(1, false)
	if m.currentQuestionIdx != 1 {
		t.Fatalf("currentQuestionIdx = %d, want 1 after submitting question 1", m.currentQuestionIdx)
	}
	if got := m.collectedAnswers["First?"]; got != "B" {
		t.Errorf("collectedAnswers[First?] = %q, want %q", got, "B")
	}

	// Submit question 2, landing on the recap slot (index == len(questions)).
	m.commitAnswer("C")
	m.advanceQuestionOpts(1, false)
	if !m.onRecapSlot() {
		t.Fatalf("expected recap slot after answering both questions, currentQuestionIdx=%d", m.currentQuestionIdx)
	}
}

func TestChatModelSubmitQuestionAnswersAddsPromptAndAnswerToHistory(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.activateQuestions([]askUserQuestion{
		{Question: "Pick a direction", Options: []askUserOption{{Label: "Alpha"}, {Label: "Beta"}}},
	}, "req-1", nil)
	m.selectedOption = 1
	m.commitAnswer("Beta")
	m.advanceQuestionOpts(1, false)

	if cmd := m.submitAllQuestionAnswers(); cmd != nil {
		t.Fatalf("submitAllQuestionAnswers() command = %T, want nil without session", cmd())
	}

	if m.hasActiveQuestion() {
		t.Fatal("question remained active after submit")
	}
	if got := chatTurnTextCount(m.turns, chatTurnAgent, "Pick a direction"); got != 1 {
		t.Fatalf("agent question history count = %d, want 1: %+v", got, m.turns)
	}
	if got := chatTurnTextCount(m.turns, chatTurnUser, "Beta"); got != 1 {
		t.Fatalf("user answer history count = %d, want 1: %+v", got, m.turns)
	}
}

func TestChatModelMultiSelectToggle(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.activateQuestions([]askUserQuestion{
		{Question: "Which repos?", MultiSelect: true, Options: []askUserOption{{Label: "A"}, {Label: "B"}}},
	}, "req-1", nil)
	m.selectedOption = 1
	m.toggleSelectedMulti()
	if !m.selectedMulti[1] {
		t.Fatal("expected option 1 to be ticked after toggleSelectedMulti")
	}
	m.toggleSelectedMulti()
	if m.selectedMulti[1] {
		t.Fatal("expected option 1 to be unticked after toggling twice")
	}
}

func chatTurnTextCount(turns []chatTurn, role chatTurnRole, text string) int {
	count := 0
	for _, turn := range turns {
		if turn.Role == role && strings.Contains(turn.Text, text) {
			count++
		}
	}
	return count
}

// TestChatModelRenderQuestionPickerShowsFreeformRow covers Finding 1: the
// option list must advertise the "Type something." freeform escape hatch
// even when not selected, matching attach.go's renderQuestion.
func TestChatModelRenderQuestionPickerShowsFreeformRow(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.activateQuestions([]askUserQuestion{
		{Question: "Pick one", Options: []askUserOption{{Label: "Alpha"}, {Label: "Beta"}}},
	}, "req-1", nil)

	body, _ := m.renderQuestionPicker()
	if !strings.Contains(body, "Type something.") {
		t.Fatal(`expected renderQuestionPicker body to contain a "Type something." row`)
	}
}

func TestChatModelQuestionPickerShowsWrappedOptions(t *testing.T) {
	m := NewChatModel(72, 20, nil, "", "", nil, "", "")
	m.activateQuestions([]askUserQuestion{
		{
			Question: "What would you like help with?",
			Options: []askUserOption{
				{Label: "Understand Agentic Orchestrator (Recommended)", Description: "Learn features, phases, and how the TUI works end-to-end."},
				{Label: "Debug an issue", Description: "Trace errors, inspect feature state, and read logs to find root cause."},
				{Label: "Explore the codebase", Description: "Search files and explain internal implementation details."},
			},
		},
	}, "req-1", nil)

	body, _ := m.renderQuestionPicker()
	stripped := stripANSI(body)
	if !strings.Contains(stripped, "Understand Agentic Orchestrator") {
		t.Fatalf("expected wrapped first option to remain visible, got:\n%s", stripped)
	}
}

func TestChatModelQuestionPickerKeepsChoiceVisibleWhenTypeRowSelected(t *testing.T) {
	m := NewChatModel(72, 20, nil, "", "", nil, "", "")
	m.activateQuestions([]askUserQuestion{
		{
			Question: "What would you like help with?",
			Options: []askUserOption{
				{Label: "Understand Agentic Orchestrator (Recommended)", Description: "Learn features, phases, architecture, session lifecycle, and how the terminal UI works end-to-end."},
				{Label: "Debug an issue", Description: "Trace errors, inspect feature state, read logs, compare snapshots, and find the root cause."},
				{Label: "Explore the codebase", Description: "Search files, explain internal implementation details, and map the relevant packages."},
			},
		},
	}, "req-1", nil)

	for i := 0; i < len(m.questions[0].Options); i++ {
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m = updated
	}
	if m.selectedOption != len(m.questions[0].Options) {
		t.Fatalf("selectedOption = %d, want Type something row", m.selectedOption)
	}

	body, _ := m.renderQuestionPicker()
	stripped := stripANSI(body)
	if !strings.Contains(stripped, "Explore the codebase") {
		t.Fatalf("expected at least one real choice to remain visible above Type something, got:\n%s", stripped)
	}
}

func TestChatModelDoesNotDuplicateActiveAskUserQuestion(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"What would you like help with?","options":[{"label":"Understand Agentic Orchestrator (Recommended)","description":"Learn features, phases, and how the TUI works end-to-end."},{"label":"Debug an issue","description":"Trace errors, inspect feature state, and read logs to find root cause."},{"label":"Explore the codebase","description":"Search files and explain internal implementation details."}]}]}`)
	m := NewChatModel(100, 20, nil, "", "", nil, "", "")
	m.turns = append(m.turns, chatTurn{Role: chatTurnUser, Text: "yo?"})
	m.responding = true

	updated, _ := m.Update(chatMsgsMsg{messages: []llm.SDKMessage{{
		Type: "control_request",
		ControlRequest: &llm.ControlRequestMessage{
			RequestID: "ask-1",
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "AskUserQuestion",
				Input:    input,
			},
		},
	}}})

	view := stripANSI(updated.View())
	if count := strings.Count(view, "What would you like help with?"); count != 1 {
		t.Fatalf("active AskUserQuestion prompt rendered %d times, want 1:\n%s", count, view)
	}
}

// TestChatModelRenderQuestionPickerFreeformInput covers Finding 1: once the
// cursor reaches the freeform slot and enter is pressed, typingCustom must
// switch the rendered body to the input box instead of continuing to show
// the (now stale) option list.
func TestChatModelRenderQuestionPickerFreeformInput(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.activateQuestions([]askUserQuestion{
		{Question: "Pick one", Options: []askUserOption{{Label: "Alpha"}, {Label: "Beta"}}},
	}, "req-1", nil)

	downKey := tea.KeyPressMsg{Code: 'j', Text: "j"}
	for i := 0; i < len(m.questions[0].Options); i++ {
		updated, _ := m.Update(downKey)
		m = updated
	}
	if m.selectedOption != len(m.questions[0].Options) {
		t.Fatalf("selectedOption = %d, want freeform slot %d", m.selectedOption, len(m.questions[0].Options))
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if !m.typingCustom {
		t.Fatal("expected enter on freeform slot to enable typingCustom")
	}

	body, _ := m.renderQuestionPicker()
	if strings.Contains(body, "Alpha") || strings.Contains(body, "Beta") {
		t.Fatal("expected freeform render to hide the stale option list")
	}
	if !strings.Contains(body, m.input.View()) {
		t.Fatal("expected freeform render to show the input box")
	}
}

// TestChatModelBackNavRestoresQ0Selection covers Finding 2: navigating back
// to question 0 after answering it and advancing must restore the recorded
// selection instead of resetting the cursor to option 0.
func TestChatModelBackNavRestoresQ0Selection(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.activateQuestions([]askUserQuestion{
		{Question: "First?", Options: []askUserOption{{Label: "A"}, {Label: "B"}, {Label: "C"}}},
		{Question: "Second?", Options: []askUserOption{{Label: "D"}, {Label: "E"}}},
	}, "req-1", nil)

	m.selectedOption = 2
	m.commitAnswer("C")
	m.advanceQuestionOpts(1, false)
	if m.currentQuestionIdx != 1 {
		t.Fatalf("currentQuestionIdx = %d, want 1 after advancing past Q0", m.currentQuestionIdx)
	}

	m.advanceQuestionOpts(-1, true)
	if m.currentQuestionIdx != 0 {
		t.Fatalf("currentQuestionIdx = %d, want 0 after navigating back", m.currentQuestionIdx)
	}
	if m.selectedOption != 2 {
		t.Errorf("selectedOption = %d, want 2 (Q0's recorded answer), not reset to 0", m.selectedOption)
	}
}

// TestChatModelPickerScrollFollowsSelection covers Finding 2: descriptions
// make each option take 2 rendered lines, so questionVisibleWindowPure
// windows down to fewer visible rows than total options. Pressing "down"
// past the initial window must advance questionScrollOffset so the cursor
// stays inside the visible [start, end) range instead of scrolling off
// screen with no way back.
func TestChatModelPickerScrollFollowsSelection(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	options := []askUserOption{
		{Label: "Alpha", Description: "First option"},
		{Label: "Beta", Description: "Second option"},
		{Label: "Gamma", Description: "Third option"},
		{Label: "Delta", Description: "Fourth option"},
		{Label: "Epsilon", Description: "Fifth option"},
	}
	m.activateQuestions([]askUserQuestion{{Question: "Pick a direction", Options: options}}, "req-1", nil)

	contentWidth := max(m.width-6, 40)
	optionArea := max(len(options), 3)

	downKey := tea.KeyPressMsg{Code: 'j', Text: "j"}
	for i := 0; i < len(options)-1; i++ {
		updated, _ := m.Update(downKey)
		m = updated
		start, end, _, _ := questionVisibleWindowPure(options, m.selectedOption, m.questionScrollOffset, optionArea, contentWidth)
		if m.selectedOption < start || m.selectedOption >= end {
			t.Fatalf("after %d down presses, selectedOption=%d scrollOffset=%d outside visible window [%d, %d)",
				i+1, m.selectedOption, m.questionScrollOffset, start, end)
		}
	}
}

// TestChatModelRecapSlotLeftGoesBack covers Finding 1: the recap slot's
// footer advertises "[←] back" but the key handler never wired it up.
// Pressing "left" on the recap slot must return to the last question
// instead of being a silent no-op.
func TestChatModelRecapSlotLeftGoesBack(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.activateQuestions([]askUserQuestion{
		{Question: "First?", Options: []askUserOption{{Label: "A"}, {Label: "B"}}},
		{Question: "Second?", Options: []askUserOption{{Label: "C"}, {Label: "D"}}},
	}, "req-1", nil)
	m.commitAnswer("A")
	m.advanceQuestionOpts(1, false)
	m.commitAnswer("C")
	m.advanceQuestionOpts(1, false)
	if !m.onRecapSlot() {
		t.Fatalf("expected recap slot after answering both questions, currentQuestionIdx=%d", m.currentQuestionIdx)
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if updated.onRecapSlot() {
		t.Fatal("expected left on recap slot to leave the recap slot")
	}
	if updated.currentQuestionIdx != 1 {
		t.Fatalf("currentQuestionIdx = %d, want 1 (last question) after left on recap slot", updated.currentQuestionIdx)
	}
}

// TestChatModelSyncAutoPickedTurns covers the out-of-band auto-pick path:
// an AskUserQuestion answered by the harness's auto-pick policy is
// synthesized directly into the session's message log (bypassing AttachCh
// entirely), so ChatModel must proactively rescan the log rather than only
// reacting to streamed messages.
func TestChatModelSyncAutoPickedTurns(t *testing.T) {
	sess := mocks.NewMockSessionView("__chat__", "")
	sess.MessageLog().Append(llm.SDKMessage{
		Type:               "user",
		LocallyAppended:    true,
		AutoPicked:         true,
		AutoPickQuestion:   "Which environment?",
		AutoPickConfidence: 0.85,
		User: &llm.UserMessage{
			Message: llm.ConversationMsg{
				Role:    "user",
				Content: []llm.ContentBlock{{Type: "text", Text: "production"}},
			},
		},
	})
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.sess = sess
	m.syncAutoPickedTurns()
	if len(m.turns) != 1 {
		t.Fatalf("len(m.turns) = %d, want 1", len(m.turns))
	}
	if !m.turns[0].AutoPicked || m.turns[0].Confidence != 0.85 || m.turns[0].Text != "production" {
		t.Errorf("m.turns[0] = %+v, want AutoPicked=true Confidence=0.85 Text=%q", m.turns[0], "production")
	}
	// Calling again must not duplicate the turn.
	m.syncAutoPickedTurns()
	if len(m.turns) != 1 {
		t.Fatalf("len(m.turns) = %d after second sync, want 1 (no duplicate)", len(m.turns))
	}
}

func TestChatModelFullscreenToggleAndEscHierarchy(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")

	// ctrl+g toggles fullscreen on.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if !updated.fullscreen {
		t.Fatal("expected ctrl+g to enable fullscreen")
	}

	// esc while fullscreen drops to docked, chat stays open (no ChatExitMsg).
	updated, cmd := updated.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if updated.fullscreen {
		t.Fatal("expected esc to disable fullscreen")
	}
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, isExit := msg.(ChatExitMsg); isExit {
				t.Fatal("esc from fullscreen should not close the chat panel")
			}
		}
	}

	// esc again (docked, empty input) closes the chat.
	_, cmd = updated.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected esc on docked+empty input to return a close command")
	}
	if _, isExit := cmd().(ChatExitMsg); !isExit {
		t.Fatal("expected esc on docked+empty input to emit ChatExitMsg")
	}
}

func TestChatModelFullscreenToggleWorksWhileQuestionActive(t *testing.T) {
	cases := []struct {
		name  string
		setup func(ChatModel) ChatModel
	}{
		{
			name: "option picker",
			setup: func(m ChatModel) ChatModel {
				m.activateQuestions([]askUserQuestion{{Question: "Pick one", Options: []askUserOption{{Label: "A"}, {Label: "B"}}}}, "req-1", nil)
				return m
			},
		},
		{
			name: "recap",
			setup: func(m ChatModel) ChatModel {
				m.activateQuestions([]askUserQuestion{{Question: "Pick one", Options: []askUserOption{{Label: "A"}, {Label: "B"}}}}, "req-1", nil)
				m.commitAnswer("A")
				m.advanceQuestionOpts(1, false)
				return m
			},
		},
		{
			name: "custom text",
			setup: func(m ChatModel) ChatModel {
				m.activateQuestions([]askUserQuestion{{Question: "Describe it"}}, "req-1", nil)
				return m
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(NewChatModel(80, 20, nil, "", "", nil, "", ""))
			if !m.hasActiveQuestion() {
				t.Fatal("setup: expected active question")
			}

			updated, cmd := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
			if cmd != nil {
				t.Fatalf("ctrl+g returned command %T while question was active", cmd())
			}
			if !updated.fullscreen {
				t.Fatal("expected ctrl+g to enable fullscreen while question was active")
			}
			if !updated.hasActiveQuestion() {
				t.Fatal("fullscreen toggle cleared the active question")
			}
		})
	}
}

// TestChatPanelHeightEmptyVsNonEmpty verifies that empty and non-empty
// conversations use distinct height ceilings by design: an empty chat dock
// stays compact (ceiling 8, within floor/ceiling range [5, 8]) even at
// typical/large terminal sizes, while a populated conversation keeps the
// original, taller range (floor/ceiling [10, 18]) and hits the 18 ceiling
// at totalHeight=100.
func TestChatPanelHeightEmptyVsNonEmpty(t *testing.T) {
	empty := NewChatModel(80, 20, nil, "", "", nil, "", "")
	if h := empty.chatPanelHeight(100); h < 5 || h > 8 {
		t.Errorf("empty chatPanelHeight(100) = %d, want within [5, 8] (compact ceiling)", h)
	}

	nonEmpty := NewChatModel(80, 20, nil, "", "", nil, "", "")
	nonEmpty.turns = append(nonEmpty.turns, chatTurn{Role: chatTurnUser, Text: "hi"})
	if h := nonEmpty.chatPanelHeight(100); h != 18 {
		t.Errorf("non-empty chatPanelHeight(100) = %d, want exactly 18 (unchanged ceiling)", h)
	}

	responding := NewChatModel(80, 20, nil, "", "", nil, "", "")
	responding.responding = true
	if h := responding.chatPanelHeight(100); h != 18 {
		t.Errorf("responding chatPanelHeight(100) = %d, want exactly 18 (unchanged ceiling)", h)
	}
}

// TestChatModelShiftEnterInsertsNewlineInMainInput verifies that Shift+Enter
// inserts a literal newline into the main chat input when no question is
// active — matching attach.go's established convention and the footer hint
// chat.go itself renders ("[shift+enter] Newline"). Before this fix,
// shiftEnterKey was only wired inside the question-picker's freeform-answer
// branch; the main input had no handler for it at all, so the footer's
// promise was never honored outside an active question.
func TestChatModelShiftEnterInsertsNewlineInMainInput(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")

	for _, ch := range "line1" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	for _, ch := range "line2" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	if !strings.Contains(m.input.Value(), "\n") {
		t.Errorf("expected newline in main chat input after shift+enter, got %q", m.input.Value())
	}
}

func TestChatModelResizePreservesDynamicInputHeight(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	m.input.SetValue("line1\nline2\nline3\nline4")
	m.syncChatInputHeight()
	if got := m.input.Height(); got != 4 {
		t.Fatalf("test setup invalid: input height = %d, want 4", got)
	}

	m = m.resize(80, 20)
	if got := m.input.Height(); got != 4 {
		t.Fatalf("resize reset multiline input height to %d, want 4", got)
	}
}

// TestChatModelFooterMentionsFullscreenToggle verifies the idle and
// responding footers both advertise ctrl+g, since the key works in both
// states (only the active-question picker's own footer, covered elsewhere,
// intentionally swallows it while a question owns the keyboard).
func TestChatModelFooterMentionsFullscreenToggle(t *testing.T) {
	m := NewChatModel(80, 20, nil, "", "", nil, "", "")
	if idle := m.View(); !strings.Contains(idle, "ctrl+g") {
		t.Errorf("idle footer missing ctrl+g hint:\n%s", idle)
	}

	m.responding = true
	if responding := m.View(); !strings.Contains(responding, "ctrl+g") {
		t.Errorf("responding footer missing ctrl+g hint:\n%s", responding)
	}
}
