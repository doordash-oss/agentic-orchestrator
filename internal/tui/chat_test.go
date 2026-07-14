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
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// testQuestionFirst and testQuestionSecond are fixture question texts reused
// across this file's multi-question test tables.
const (
	testQuestionFirst  = "First?"
	testQuestionSecond = "Second?"
)

// testQuestionPickOne is a fixture AskUserQuestion prompt reused across this
// file, chat_events_test.go and question_picker_test.go.
const testQuestionPickOne = "Pick one"

// testQuestionPickDirection is a fixture AskUserQuestion prompt reused
// across this file, api_app_test.go and api_chat_adapter_test.go.
const testQuestionPickDirection = "Pick a direction"

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
	m := newChatModel(80, 24)
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
	m := newChatModel(80, 24)
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
	m := newChatModel(80, 24)
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
	m := newChatModel(80, 24)
	view := m.View()
	if !strings.Contains(view, "Ask me Anything") {
		t.Error("expected title in view")
	}
	if !strings.Contains(view, "Ask anything about Agentic Orchestrator") {
		t.Error("expected empty state hint in view")
	}
}

func TestChatModelViewDoesNotUseDarkTextareaCursorLine(t *testing.T) {
	m := newChatModel(80, 24)
	view := m.View()
	if strings.Contains(view, "\x1b[40m") || strings.Contains(view, "\x1b[48;5;0m") {
		t.Fatalf("chat view rendered Bubble textarea's dark cursor-line background: %q", view)
	}
}

func TestChatModelViewFitsAllocatedEmptyPanelHeight(t *testing.T) {
	m := newChatModel(100, 8)
	if got := lipgloss.Height(m.View()); got > m.height {
		t.Fatalf("empty chat view height = %d, want <= allocated height %d", got, m.height)
	}
}

func TestChatModelEscWhileRespondingMinimizes(t *testing.T) {
	m := newChatModel(80, 24)
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
	m := newChatModel(80, 24)
	view := m.View()
	if !strings.Contains(view, "[enter] Send") {
		t.Error("expected [enter] Send in footer")
	}
	if !strings.Contains(view, "[esc] Close") {
		t.Error("expected [esc] Close in footer")
	}
}

func TestChatModelViewBoxesMessageInputPanel(t *testing.T) {
	m := newChatModel(100, 20)
	m.turns = append(m.turns,
		chatTurn{Role: chatTurnUser, Text: "what is running?"},
		chatTurn{Role: chatTurnAgent, Text: strings.Repeat("Transcript content. ", 18)},
	)
	m.rebuildViewport()
	m = m.resize(100, 20)

	view := stripANSI(m.View())
	if !strings.Contains(view, " Message ") {
		t.Fatalf("message input should render with an inner Message box title:\n%s", view)
	}
	if strings.Count(view, "╭") < 2 || strings.Count(view, "╰") < 2 {
		t.Fatalf("message input should add an inner box distinct from the outer chat panel:\n%s", view)
	}
}

func TestChatModelViewLeavesTwoBlankRowsAboveMessageInput(t *testing.T) {
	const finalAnswer = "final answer above the message box"
	m := newChatModel(100, 14)
	for i := range 8 {
		text := fmt.Sprintf("earlier answer %d", i)
		if i == 7 {
			text = finalAnswer
		}
		m.turns = append(m.turns, chatTurn{Role: chatTurnAgent, Text: text})
	}
	m.rebuildViewport()
	m = m.resize(100, 14)

	view := stripANSI(m.View())
	lines := strings.Split(view, "\n")
	finalLine := -1
	for i, line := range lines {
		if strings.Contains(line, finalAnswer) {
			finalLine = i
		}
	}
	if finalLine == -1 {
		t.Fatalf("chat view missing final answer %q:\n%s", finalAnswer, view)
	}
	messageLine := -1
	for i := finalLine + 1; i < len(lines); i++ {
		if strings.Contains(lines[i], " Message ") {
			messageLine = i
			break
		}
	}
	if messageLine == -1 {
		t.Fatalf("chat view missing Message input box after final answer:\n%s", view)
	}

	blankRows := 0
	for _, line := range lines[finalLine+1 : messageLine] {
		if strings.Trim(line, " │") != "" {
			t.Fatalf("expected only blank rows between final answer and Message box, got %q in:\n%s", line, view)
		}
		blankRows++
	}
	if blankRows != 2 {
		t.Fatalf("blank rows between final answer and Message box = %d, want 2:\n%s", blankRows, view)
	}
}

func TestChatModelViewShowsRespondingFooter(t *testing.T) {
	m := newChatModel(80, 24)
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
	m := newChatModel(80, 24)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("expected nil cmd for enter with empty input")
	}
}

func TestChatModelEnterWithNoAPIShowsError(t *testing.T) {
	m := newChatModel(80, 24)
	m.input.SetValue("hello")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !chatTurnsContainText(updated.turns, "chat API unavailable") {
		t.Errorf("expected error turn in turns when chat API is unavailable, got: %+v", updated.turns)
	}
}

func TestChatModelResize(t *testing.T) {
	m := newChatModel(80, 24)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if updated.width != 120 {
		t.Errorf("width = %d, want 120", updated.width)
	}
	if updated.height != 40 {
		t.Errorf("height = %d, want 40", updated.height)
	}
}

func TestChatModelHistorySurvivesMultipleUpdateCycles(t *testing.T) {
	m := newChatModel(80, 24)
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

func TestChatModelAppendsUserAndAgentTurns(t *testing.T) {
	m := newChatModel(80, 20)
	m.turns = append(m.turns, chatTurn{Role: chatTurnUser, Text: "hello"}) //nolint:goconst // arbitrary filler text coincidentally shared with an unrelated textarea test fixture
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
	m := newChatModel(80, 20)
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
	m := newChatModel(80, 20)
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
	m := newChatModel(80, 20)
	m.activateQuestions([]askUserQuestion{{Question: testQuestionPickOne, Options: []askUserOption{{Label: "A"}, {Label: "B"}}}}, "req-1", nil)
	if !m.hasActiveQuestion() {
		t.Fatal("expected an active question after activateQuestions")
	}
	if m.selectedOption != 0 {
		t.Errorf("selectedOption = %d, want 0", m.selectedOption)
	}
}

func TestChatModelQuestionNavAndSubmit(t *testing.T) {
	m := newChatModel(80, 20)
	m.activateQuestions([]askUserQuestion{
		{Question: testQuestionFirst, Options: []askUserOption{{Label: "A"}, {Label: "B"}}},
		{Question: testQuestionSecond, Options: []askUserOption{{Label: "C"}, {Label: "D"}}},
	}, "req-1", nil)

	// Move selection down to "B" and submit it.
	m.selectedOption = 1
	m.commitAnswer("B")
	m.advanceQuestionOpts(1, false)
	if m.currentQuestionIdx != 1 {
		t.Fatalf("currentQuestionIdx = %d, want 1 after submitting question 1", m.currentQuestionIdx)
	}
	if got := m.collectedAnswers[testQuestionFirst]; got != "B" {
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
	m := newChatModel(80, 20)
	m.activateQuestions([]askUserQuestion{
		{Question: testQuestionPickDirection, Options: []askUserOption{{Label: testOptionLabelAlpha}, {Label: testOptionLabelBeta}}},
	}, "req-1", nil)
	m.selectedOption = 1
	m.commitAnswer(testOptionLabelBeta)
	m.advanceQuestionOpts(1, false)

	if cmd := m.submitAllQuestionAnswers(); cmd != nil {
		t.Fatalf("submitAllQuestionAnswers() command = %T, want nil without session", cmd())
	}

	if m.hasActiveQuestion() {
		t.Fatal("question remained active after submit")
	}
	if got := chatTurnTextCount(m.turns, chatTurnAgent, testQuestionPickDirection); got != 1 {
		t.Fatalf("agent question history count = %d, want 1: %+v", got, m.turns)
	}
	if got := chatTurnTextCount(m.turns, chatTurnUser, testOptionLabelBeta); got != 1 {
		t.Fatalf("user answer history count = %d, want 1: %+v", got, m.turns)
	}
}

func TestAPIChatModelSubmitQuestionAnswerSchedulesRecoveryTick(t *testing.T) {
	client := &fakeTUIAPIClient{}
	sess := newAPIChatSession(client, chatSessionID)
	m := NewAPIChatModel(80, 20, client)
	m.sess = sess
	m.activateQuestions([]askUserQuestion{
		{Question: testQuestionPickDirection, Options: []askUserOption{{Label: testOptionLabelAlpha}, {Label: testOptionLabelBeta}}},
	}, testAskRequestID, nil)
	m.selectedOption = 1
	m.commitAnswer(testOptionLabelBeta)
	m.advanceQuestionOpts(1, false)

	cmd := m.submitAllQuestionAnswers()
	if cmd == nil {
		t.Fatal("submitAllQuestionAnswers() returned nil command, want API answer command")
	}
	msg := cmd()

	if got := client.askUserAnswers; len(got) != 1 || got[0].RequestID != testAskRequestID || got[0].SessionID != chatSessionID || got[0].Answers[testQuestionPickDirection] != testOptionLabelBeta {
		t.Fatalf("AskUser answers = %+v, want chat question answer", got)
	}
	tick, ok := msg.(chatRecoveryTickMsg)
	if !ok || tick.sess != sess {
		t.Fatalf("question answer command returned %#v, want chatRecoveryTickMsg for chat session", msg)
	}
}

func TestChatModelMultiSelectToggle(t *testing.T) {
	m := newChatModel(80, 20)
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
	m := newChatModel(80, 20)
	m.activateQuestions([]askUserQuestion{
		{Question: testQuestionPickOne, Options: []askUserOption{{Label: testOptionLabelAlpha}, {Label: testOptionLabelBeta}}},
	}, "req-1", nil)

	body, _ := m.renderQuestionPicker()
	if !strings.Contains(body, "Type something.") {
		t.Fatal(`expected renderQuestionPicker body to contain a "Type something." row`)
	}
}

func TestChatModelQuestionPickerShowsWrappedOptions(t *testing.T) {
	m := newChatModel(72, 20)
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
	m := newChatModel(72, 20)
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

func TestChatModelViewBoxesActiveQuestionPanel(t *testing.T) {
	m := newChatModel(100, 24)
	m.turns = append(m.turns,
		chatTurn{Role: chatTurnUser, Text: "another 3 choices question pls"},
		chatTurn{Role: chatTurnAgent, Text: strings.Repeat("Long answer content. ", 24)},
	)
	m.rebuildViewport()
	m.activateQuestions([]askUserQuestion{{
		Question: "What would you like to explore next?",
		Options: []askUserOption{
			{Label: "State machine & checkpoints", Description: "Feature states and review gates."},
			{Label: "Post-publish actions", Description: "What happens after PR creation."},
			{Label: "Something else entirely", Description: "Ask a different question."},
		},
	}}, "req-1", nil)
	m = m.resize(100, 24)

	view := stripANSI(m.View())
	if !strings.Contains(view, " Question ") {
		t.Fatalf("active question should render with an inner Question box title:\n%s", view)
	}
	if strings.Count(view, "╭") < 2 || strings.Count(view, "╰") < 2 {
		t.Fatalf("active question should add an inner box distinct from the outer chat panel:\n%s", view)
	}
	if questionIdx, promptIdx := strings.Index(view, " Question "), strings.Index(view, "What would you like to explore next?"); questionIdx < 0 || promptIdx < questionIdx {
		t.Fatalf("Question box title should appear before the prompt:\n%s", view)
	}
}

func TestChatModelActiveQuestionPanelGetsMoreDockedHeight(t *testing.T) {
	m := newChatModel(100, 20)
	m.activateQuestions([]askUserQuestion{{
		Question: "What would you like me to help you with?",
		Options: []askUserOption{
			{Label: "Ask about Agentic Orchestrator features", Description: "Quick answers from the user guide and codebase."},
			{Label: "Search the codebase", Description: "Find files, patterns, and trace code paths."},
			{Label: "Debug an issue", Description: "Inspect local state and logs."},
			{Label: "Review current progress", Description: "Summarize the selected feature and recent run status."},
		},
	}}, "req-1", nil)

	if got := m.chatPanelHeight(80); got <= 18 {
		t.Fatalf("active question chatPanelHeight(80) = %d, want more than the ordinary 18-line dock", got)
	}
}

func TestChatModelQuestionViewFitsAllocatedPanel(t *testing.T) {
	m := newChatModel(72, 20)
	m.turns = append(m.turns, chatTurn{Role: chatTurnAgent, Text: "Ready."})
	m.activateQuestions([]askUserQuestion{{
		Question: "Which very long Agentic Orchestrator investigation path should the AMA session take before answering?",
		Options: []askUserOption{
			{
				Label:       "Search the codebase for the complete AMA permission and AskUserQuestion control flow",
				Description: "Trace API snapshots, transcript updates, pending controls, and inline picker rendering without overflowing the panel.",
			},
			{
				Label:       "Inspect current logs and state",
				Description: "Use read-only diagnostics to identify the stuck session and explain the result.",
			},
			{
				Label:       "Explain the user guide",
				Description: "Summarize the relevant behavior from docs and code.",
			},
		},
	}}, "req-1", nil)
	m = m.resize(72, m.chatPanelHeight(80))

	view := m.View()
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("question chat view height = %d, want <= allocated height %d:\n%s", got, m.height, stripANSI(view))
	}
	if got := maxLineWidth(view); got > m.width {
		t.Fatalf("question chat view width = %d, want <= allocated width %d:\n%s", got, m.width, stripANSI(view))
	}
}

func TestChatModelFullscreenQuestionReservesTranscriptSpace(t *testing.T) {
	m := newChatModel(120, 60)
	m.fullscreen = true
	m.turns = append(m.turns,
		chatTurn{Role: chatTurnUser, Text: "yo?"},
		chatTurn{Role: chatTurnAgent, Text: "I can help with Agentic Orchestrator usage, codebase exploration, or debugging."},
	)
	m.rebuildViewport()
	m.activateQuestions([]askUserQuestion{{
		Question: "What would you like help with?",
		Options: []askUserOption{
			{Label: "Agentic Orchestrator usage", Description: "Quick answers on features, phases, and workflows."},
			{Label: "Codebase exploration", Description: "Search and explain how the internals work."},
			{Label: "Debugging an issue", Description: "Trace errors, inspect feature state, and read logs."},
			{Label: "Something else"},
		},
	}}, "req-1", nil)
	m = m.resize(120, 60)

	if got := m.viewport.Height(); got < 8 {
		t.Fatalf("fullscreen active question viewport height = %d, want at least 8 transcript rows", got)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "I can help with Agentic Orchestrator") {
		t.Fatalf("fullscreen active question view hid the prior agent response:\n%s", view)
	}
}

// TestChatModelRenderQuestionPickerFreeformInput covers Finding 1: once the
// cursor reaches the freeform slot and enter is pressed, typingCustom must
// switch the rendered body to the input box instead of continuing to show
// the (now stale) option list.
func TestChatModelRenderQuestionPickerFreeformInput(t *testing.T) {
	m := newChatModel(80, 20)
	m.activateQuestions([]askUserQuestion{
		{Question: testQuestionPickOne, Options: []askUserOption{{Label: testOptionLabelAlpha}, {Label: testOptionLabelBeta}}},
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
	if strings.Contains(body, testOptionLabelAlpha) || strings.Contains(body, testOptionLabelBeta) {
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
	m := newChatModel(80, 20)
	m.activateQuestions([]askUserQuestion{
		{Question: testQuestionFirst, Options: []askUserOption{{Label: "A"}, {Label: "B"}, {Label: "C"}}},
		{Question: testQuestionSecond, Options: []askUserOption{{Label: "D"}, {Label: "E"}}},
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
	m := newChatModel(80, 20)
	options := []askUserOption{
		{Label: testOptionLabelAlpha, Description: "First option"},
		{Label: testOptionLabelBeta, Description: "Second option"},
		{Label: testOptionLabelGamma, Description: "Third option"},
		{Label: "Delta", Description: "Fourth option"},
		{Label: "Epsilon", Description: "Fifth option"},
	}
	m.activateQuestions([]askUserQuestion{{Question: testQuestionPickDirection, Options: options}}, "req-1", nil)

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
	m := newChatModel(80, 20)
	m.activateQuestions([]askUserQuestion{
		{Question: testQuestionFirst, Options: []askUserOption{{Label: "A"}, {Label: "B"}}},
		{Question: testQuestionSecond, Options: []askUserOption{{Label: "C"}, {Label: "D"}}},
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
		Type:               roleUser,
		LocallyAppended:    true,
		AutoPicked:         true,
		AutoPickQuestion:   "Which environment?",
		AutoPickConfidence: 0.85,
		User: &llm.UserMessage{
			Message: llm.ConversationMsg{
				Role:    roleUser,
				Content: []llm.ContentBlock{{Type: blockTypeText, Text: "production"}}, //nolint:goconst // arbitrary fixture text coincidentally shared with an unrelated env-option-label test
			},
		},
	})
	m := newChatModel(80, 20)
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
	m := newChatModel(80, 20)

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
				m.activateQuestions([]askUserQuestion{{Question: testQuestionPickOne, Options: []askUserOption{{Label: "A"}, {Label: "B"}}}}, "req-1", nil)
				return m
			},
		},
		{
			name: "recap",
			setup: func(m ChatModel) ChatModel {
				m.activateQuestions([]askUserQuestion{{Question: testQuestionPickOne, Options: []askUserOption{{Label: "A"}, {Label: "B"}}}}, "req-1", nil)
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
			m := tc.setup(newChatModel(80, 20))
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
	empty := newChatModel(80, 20)
	if h := empty.chatPanelHeight(100); h < 5 || h > 8 {
		t.Errorf("empty chatPanelHeight(100) = %d, want within [5, 8] (compact ceiling)", h)
	}

	nonEmpty := newChatModel(80, 20)
	nonEmpty.turns = append(nonEmpty.turns, chatTurn{Role: chatTurnUser, Text: "hi"})
	if h := nonEmpty.chatPanelHeight(100); h != 18 {
		t.Errorf("non-empty chatPanelHeight(100) = %d, want exactly 18 (unchanged ceiling)", h)
	}

	responding := newChatModel(80, 20)
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
	m := newChatModel(80, 20)

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
	m := newChatModel(80, 20)
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
	m := newChatModel(80, 20)
	if idle := m.View(); !strings.Contains(idle, "ctrl+g") {
		t.Errorf("idle footer missing ctrl+g hint:\n%s", idle)
	}

	m.responding = true
	if responding := m.View(); !strings.Contains(responding, "ctrl+g") {
		t.Errorf("responding footer missing ctrl+g hint:\n%s", responding)
	}
}
