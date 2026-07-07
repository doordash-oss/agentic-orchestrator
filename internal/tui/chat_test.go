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
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
