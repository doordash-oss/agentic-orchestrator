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
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// taskTypeLocalAgent is the llm.TaskStartedMessage.TaskType value for a
// locally-delegated agent task, shared with other tui tests.
const taskTypeLocalAgent = "local_agent"

// blockTypeToolResult is the llm.ContentBlock.Type value for a tool-result
// block, shared with other tui tests that build ContentBlock fixtures.
const blockTypeToolResult = "tool_result"

// testAskToolUseID1/2/3 are fixture tool_use IDs reused across this file's
// AskUserQuestion message-matching test table.
const (
	testAskToolUseID1 = "toolu_1"
	testAskToolUseID2 = "toolu_2"
	testAskToolUseID3 = "toolu_3"
)

func floatPtr(v float64) *float64 { return &v }

func pendingAskUserControlRequest(requestID, question string) *llm.ControlRequestMessage {
	return &llm.ControlRequestMessage{
		RequestID: requestID,
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolNameAskUserQuestion,
			Input: json.RawMessage(fmt.Sprintf(`{"questions":[{"question":%q,"header":"Question","multiSelect":false,"options":[`+
				`{"label":"A","description":""},{"label":"B","description":""}]}]}`, question)),
		},
	}
}

func pendingPermissionControlRequest(requestID string) *llm.ControlRequestMessage {
	return &llm.ControlRequestMessage{
		RequestID: requestID,
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolNameBash,
			Input:    json.RawMessage(`{"command":"go test ./internal/tui"}`),
		},
	}
}

type staleControlSessionView struct {
	*mocks.MockSessionView
}

func (s staleControlSessionView) PendingControlRequests() []*llm.ControlRequestMessage {
	return nil
}

func TestAttachModelFindsAskUserQuestion(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"What version?","header":"Version","multiSelect":false,"options":[{"label":"Patch","description":"Bug fix","confidence":0.82},{"label":"Minor","description":"Feature","confidence":0.31}]}]}`)

	tests := []struct {
		name     string
		messages []llm.SDKMessage
		wantQ    bool
	}{
		{
			name: "complete assistant message with AskUserQuestion",
			messages: []llm.SDKMessage{
				{
					Type: roleAssistant,
					Assistant: &llm.AssistantMessage{
						Message: llm.ConversationMsg{
							Content: []llm.ContentBlock{
								{Type: blockTypeToolUse, Name: toolNameAskUserQuestion, Input: input, ID: testAskToolUseID1},
							},
						},
					},
				},
			},
			wantQ: true,
		},
		{
			name: "partial assistant message with AskUserQuestion",
			messages: []llm.SDKMessage{
				{
					Type:    roleAssistant,
					Subtype: "partial",
					Assistant: &llm.AssistantMessage{
						Subtype: "partial",
						Message: llm.ConversationMsg{
							Content: []llm.ContentBlock{
								{Type: blockTypeToolUse, Name: toolNameAskUserQuestion, Input: input, ID: testAskToolUseID2},
							},
						},
					},
				},
			},
			wantQ: true,
		},
		{
			name: "AskUserQuestion followed by more messages",
			messages: []llm.SDKMessage{
				{
					Type: roleAssistant,
					Assistant: &llm.AssistantMessage{
						Message: llm.ConversationMsg{
							Content: []llm.ContentBlock{
								{Type: blockTypeToolUse, Name: toolNameAskUserQuestion, Input: input, ID: testAskToolUseID3},
							},
						},
					},
				},
				{
					Type: "user",
					User: &llm.UserMessage{
						Message: llm.ConversationMsg{
							Content: []llm.ContentBlock{
								{Type: blockTypeToolResult, ToolUseID: testAskToolUseID3, IsError: true},
							},
						},
					},
				},
				{
					Type: roleAssistant,
					Assistant: &llm.AssistantMessage{
						Message: llm.ConversationMsg{
							Content: []llm.ContentBlock{
								{Type: "text", Text: "I've asked my question."},
							},
						},
					},
				},
				{
					Type:   "result",
					Result: &llm.ResultMessage{Subtype: "success"},
				},
			},
			wantQ: true,
		},
		{
			name: "AskUserQuestion answered with successful tool_result",
			messages: []llm.SDKMessage{
				{
					Type: roleAssistant,
					Assistant: &llm.AssistantMessage{
						Message: llm.ConversationMsg{
							Content: []llm.ContentBlock{
								{Type: blockTypeToolUse, Name: toolNameAskUserQuestion, Input: input, ID: "toolu_4"},
							},
						},
					},
				},
				{
					Type: "user",
					User: &llm.UserMessage{
						Message: llm.ConversationMsg{
							Content: []llm.ContentBlock{
								{Type: blockTypeToolResult, ToolUseID: "toolu_4"},
							},
						},
					},
				},
			},
			wantQ: false,
		},
		{
			name: "AskUserQuestion answered with plain user message",
			messages: []llm.SDKMessage{
				{
					Type: roleAssistant,
					Assistant: &llm.AssistantMessage{
						Message: llm.ConversationMsg{
							Content: []llm.ContentBlock{
								{Type: blockTypeToolUse, Name: toolNameAskUserQuestion, Input: input, ID: "toolu_5"},
							},
						},
					},
				},
				{
					Type: "user",
					User: &llm.UserMessage{
						Message: llm.ConversationMsg{
							Content: []llm.ContentBlock{
								{Type: "text", Text: "my answer"},
							},
						},
					},
				},
			},
			wantQ: false,
		},
		{
			name:     "no AskUserQuestion",
			messages: []llm.SDKMessage{},
			wantQ:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := session.NewSession("test", "feat", 0)
			for _, msg := range tt.messages {
				sess.MessageLog().Append(msg)
			}
			sess.CloseDone() // prevent Init() from blocking

			m := attachModelFromSession(sess, 120, 40)
			if tt.wantQ && !m.hasActiveQuestion() {
				t.Errorf("expected active question, got none; pendingQuestions=%v", m.pendingQuestions)
				// Include the captured output to make failures actionable.
				for _, msg := range sess.MessageLog().LastN(50) {
					if msg.Assistant != nil {
						for _, block := range msg.Assistant.Message.Content {
							t.Logf("  block: type=%s name=%s isToolUse=%v inputLen=%d",
								block.Type, block.Name, block.IsToolUse(), len(block.Input))
						}
					}
				}
			}
			if !tt.wantQ && m.hasActiveQuestion() {
				t.Errorf("expected no question, got %+v", m.pendingQuestions)
			}
		})
	}
}

func TestNewAttachModel_SkipsResolvedAskUserQuestionFromSessionState(t *testing.T) {
	base := mocks.NewMockSessionView("resolved-state", "feat")
	base.StatusVal = session.SessionRunning
	base.LastControlRequestVal = &llm.ControlRequestMessage{
		RequestID: "ask-auto-picked",
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolNameAskUserQuestion,
			Input:    json.RawMessage(`{"questions":[{"question":"Already answered?","options":[{"label":"Yes (Recommended)","confidence":0.9},{"label":"No","confidence":0.1}]}]}`),
		},
	}

	m := attachModelFromSession(staleControlSessionView{MockSessionView: base}, 120, 40)
	if m.hasActiveQuestion() {
		t.Fatalf("resolved AskUserQuestion from session state must not activate; requestID=%q", m.pendingAskRequestID)
	}
	if len(m.pendingAskQueue) != 0 {
		t.Fatalf("resolved AskUserQuestion from session state must not queue, got %v", m.pendingAskQueue)
	}
}

func TestNewAttachModel_SkipsAutoPickedAskUserQuestionFromMessageLogFallback(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"Already answered?","options":[{"label":"Yes (Recommended)","confidence":0.9},{"label":"No","confidence":0.1}]}]}`)
	sess := mocks.NewMockSessionView("resolved-log", "feat")
	sess.MessageLog().Append(llm.SDKMessage{
		Type: roleAssistant,
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Content: []llm.ContentBlock{
					{Type: blockTypeToolUse, Name: toolNameAskUserQuestion, Input: input, ID: "toolu_auto"},
				},
			},
		},
	})
	sess.QALogVal = []session.QAPair{
		{
			Question:   "Already answered?",
			Answer:     "Yes (Recommended)",
			AutoPicked: true,
			Confidence: 0.9,
		},
	}

	m := attachModelFromSession(sess, 120, 40)
	if m.hasActiveQuestion() {
		t.Fatalf("auto-picked AskUserQuestion from message-log fallback must not activate; requestID=%q", m.pendingAskRequestID)
	}
	rendered := renderAttachMessages(sess.MessageLog().Messages(), filterAll, 120, nil)
	stripped := ansiRegex.ReplaceAllString(rendered, "")
	if !strings.Contains(stripped, "[auto-picked, confidence: 0.90] Yes (Recommended)") {
		t.Fatalf("auto-picked answer should be visible in attach transcript, got %q", stripped)
	}
	if strings.Contains(stripped, "[you] Yes (Recommended)") {
		t.Fatalf("auto-picked answer should not be labeled as [you], got %q", stripped)
	}

	before := sess.MessageLog().Len()
	_ = attachModelFromSession(sess, 120, 40)
	if after := sess.MessageLog().Len(); after != before {
		t.Fatalf("reattach duplicated auto-picked display message: before=%d after=%d", before, after)
	}
}

func TestAttachReplayReadsQALogWhileAskUserResponseCaptured(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"Replay answer?","options":[{"label":"Yes (Recommended)","confidence":0.9},{"label":"No","confidence":0.1}]}]}`)
	sess := session.NewSession("qa-log-race", "feat", 0)
	sess.SetStdinForTest(noopWriteCloser{})
	sess.MessageLog().Append(llm.SDKMessage{
		Type: roleAssistant,
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Content: []llm.ContentBlock{
					{Type: blockTypeToolUse, Name: toolNameAskUserQuestion, Input: input, ID: "toolu_replay"},
				},
			},
		},
	})
	sess.CloseDone()

	start := make(chan struct{})
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 250; i++ {
			if err := sess.RespondToAskUser(
				fmt.Sprintf("req-%d", i),
				input,
				map[string]string{"Replay answer?": "Yes (Recommended)"},
				nil,
			); err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 250; i++ {
			_ = attachModelFromSession(sess, 120, 40)
		}
	}()

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("RespondToAskUser: %v", err)
	}
	if got := len(sess.QALog()); got != 250 {
		t.Fatalf("len(QALog()) = %d, want 250", got)
	}
}

func TestNewAttachModel_ZeroOptionAskUserQuestionUsesDirectFreeform(t *testing.T) {
	sess := session.NewSession("test", "feat", 0)
	sess.SetStatus(session.SessionWaitingHelp)
	sess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: testAskRequestID,
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolNameAskUserQuestion,
			Input:    json.RawMessage(`{"questions":[{"question":"What version should Agentic be bumped to?","header":"Version","multiSelect":false,"options":[]}]}`),
		},
	})
	sess.CloseDone()

	m := attachModelFromSession(sess, 120, 40)
	if !m.hasActiveQuestion() {
		t.Fatal("expected active question")
	}
	if !m.typingCustom {
		t.Fatal("expected zero-option question to open direct freeform input")
	}
	if !m.input.Focused() {
		t.Fatal("expected direct freeform input to be focused")
	}
	if got := m.renderQuestion(); strings.Contains(got, "Type something") {
		t.Fatalf("expected direct freeform question UI, got: %s", got)
	}
}

func TestParseAskUserQuestions_NumberedQuestionBundleStaysFreeform(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"1. For question 5, should I document only version-related inputs inside this repository, or also adjacent workflow state under ~/.agentic-workflow if the repo interacts with it?\n2. For question 6, do you want an exhaustive inventory of every explicit version string in fixtures/generated assets, or only references that are part of normal product, build, and documentation surfaces?\n3. For question 9, should repository-specific constraints include only code-enforced constraints, or also conventions documented in AGENTS.md and the repo knowledge base when they affect version updates?","header":"Scope","multiSelect":false,"options":[]}]}`)

	questions := parseAskUserQuestions(input)
	if len(questions) != 1 {
		t.Fatalf("len(questions) = %d, want 1", len(questions))
	}
	if len(questions[0].Options) != 0 {
		t.Fatalf("expected no inferred options, got %+v", questions[0].Options)
	}
	if !questionUsesDirectFreeform(questions[0]) {
		t.Fatal("expected numbered question bundle to stay freeform")
	}
}

func TestParseAskUserQuestions_NumberedOptionsStillInferChoices(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"Which scope should count as in-scope?\n1. Repository-first (Recommended): cover everything inside the repo and mention external state only if the repo clearly depends on it. [confidence: 0.81]\n2. Repo plus workflow state: also include adjacent ~/.agentic-workflow files when they influence version behavior. [confidence: 0.34]\nReply with 1 or 2.","header":"Scope","multiSelect":false,"options":[]}]}`)

	questions := parseAskUserQuestions(input)
	if len(questions) != 1 {
		t.Fatalf("len(questions) = %d, want 1", len(questions))
	}
	if got := len(questions[0].Options); got != 2 {
		t.Fatalf("len(options) = %d, want 2", got)
	}
	if questions[0].Options[0].Confidence == nil || *questions[0].Options[0].Confidence != 0.81 {
		t.Fatalf("options[0].Confidence = %v, want 0.81", questions[0].Options[0].Confidence)
	}
	if questionUsesDirectFreeform(questions[0]) {
		t.Fatal("expected numbered options to infer multiple choice")
	}
}

func TestParseAskUserQuestions_PreservesStructuredConfidence(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"What version?","header":"Version","multiSelect":false,"options":[{"label":"Patch","description":"Bug fix","confidence":0.82},{"label":"Minor","description":"Feature","confidence":0.31}]}]}`)

	questions := parseAskUserQuestions(input)
	if len(questions) != 1 || len(questions[0].Options) != 2 {
		t.Fatalf("parsed questions = %+v", questions)
	}
	if questions[0].Options[0].Confidence == nil || *questions[0].Options[0].Confidence != 0.82 {
		t.Fatalf("options[0].Confidence = %v, want 0.82", questions[0].Options[0].Confidence)
	}
}

func TestRenderQuestion_ShowsConfidencePerOption(t *testing.T) {
	m := AttachModel{
		width: 120,
		pendingQuestions: []askUserQuestion{{
			Question: "Pick one",
			Options: []askUserOption{
				{Label: "Patch (Recommended)", Description: "Bug fix", Confidence: floatPtr(0.82)},
				{Label: "Minor", Description: "Feature", Confidence: floatPtr(0.31)},
			},
		}},
		currentQuestionIdx: 0,
		selectedOption:     0,
		inputHeight:        1,
	}

	output := m.renderQuestion()
	if !strings.Contains(output, "Confidence: 0.82") {
		t.Fatalf("expected rendered output to show first confidence, got: %s", output)
	}
	if !strings.Contains(output, "Confidence: 0.31") {
		t.Fatalf("expected rendered output to show second confidence, got: %s", output)
	}
}

func longQuestionAnswerModel() AttachModel {
	longQuestion := strings.Join([]string{
		"I've read the research, user answers, and skill. The user has made 8 decisions already.",
		"Let me identify the unresolved design decisions and their dependencies.",
		"The research surfaced a critical binding constraint that the answered questions did not address.",
		"Question 9 - CI content-contract test literals:",
		"The research found that two Go test files run in CI and assert literal substrings in README.md.",
		"How should the translation handle these prose-like contract tokens?",
	}, "\n\n")
	return AttachModel{
		width: 120,
		pendingQuestions: []askUserQuestion{{
			Question: longQuestion,
			Options: []askUserOption{
				{Label: "Preserve contract tokens in English", Confidence: floatPtr(0.82)},
				{Label: "Translate naturally and update tests", Confidence: floatPtr(0.46)},
				{Label: "Translate with additional accepted literals", Confidence: floatPtr(0.31)},
			},
		}},
		currentQuestionIdx: 0,
		selectedOption:     0,
		inputHeight:        1,
	}
}

func TestRenderQuestion_LongQuestionStillShowsAnswerLabels(t *testing.T) {
	m := longQuestionAnswerModel()

	output := m.renderQuestion()
	for _, want := range []string{
		"1. Preserve contract tokens in English",
		"2. Translate naturally and update tests",
		"3. Translate with additional accepted literals",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("long question render missing answer label %q:\n%s", want, output)
		}
	}
	if !strings.Contains(output, "? full question") {
		t.Fatalf("long question render missing full-question affordance:\n%s", output)
	}
}

func TestRenderQuestion_LongQuestionCanExpandAndReturnToChoices(t *testing.T) {
	m := longQuestionAnswerModel()
	collapsed := m.renderQuestion()
	if strings.Contains(collapsed, "How should the translation handle these prose-like contract tokens?") {
		t.Fatalf("collapsed render should not spend option space on the full question:\n%s", collapsed)
	}

	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	if cmd != nil {
		t.Fatal("question expansion should not emit a command")
	}
	expanded := m.renderQuestion()
	if !strings.Contains(expanded, "How should the translation handle these prose-like contract tokens?") {
		t.Fatalf("expanded render missing full question tail:\n%s", expanded)
	}
	if !strings.Contains(expanded, "? back to choices") {
		t.Fatalf("expanded render missing return affordance:\n%s", expanded)
	}

	m, cmd = m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	if cmd != nil {
		t.Fatal("question collapse should not emit a command")
	}
	collapsed = m.renderQuestion()
	if !strings.Contains(collapsed, "1. Preserve contract tokens in English") {
		t.Fatalf("collapsed render after returning should show choices:\n%s", collapsed)
	}
}

func TestRenderQuestion_TallTerminalUsesMoreQuestionPanelSpace(t *testing.T) {
	m := longQuestionAnswerModel()
	m.height = 60
	m.pendingQuestions[0].Options = []askUserOption{
		{
			Label:       "Keep all tier names in English",
			Description: "The tier names function as proper labels referenced in both the table and prose.",
			Confidence:  floatPtr(0.78),
		},
		{
			Label:       "Translate all tier names except TUI observability",
			Description: "Maximizes Italian content but creates a visible inconsistency in a single column.",
			Confidence:  floatPtr(0.32),
		},
		{
			Label:       "Translate all tier names including TUI observability",
			Description: "Fully Italian, but re-opens the CI-contract scope already closed earlier.",
			Confidence:  floatPtr(0.15),
		},
	}

	output := m.renderQuestion()
	if got := m.chatPanelHeight(); got <= 20 {
		t.Fatalf("chatPanelHeight() = %d, want tall terminal to allocate more than 20 lines", got)
	}
	for _, want := range []string{
		"1. Keep all tier names in English",
		"2. Translate all tier names except TUI observability",
		"3. Translate all tier names including TUI observability",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("tall terminal render missing answer label %q:\n%s", want, output)
		}
	}
}

func TestParseAskUserQuestionsForDisplay_RecoversConfidenceFromToolUse(t *testing.T) {
	strippedInput := json.RawMessage(`{"questions":[{"question":"Should games have a clock/time control?","header":"Time controls","multiSelect":false,"options":[{"label":"No clock - untimed only (Recommended)","description":"Players take as long as they want."},{"label":"Optional preset time controls","description":"User can choose untimed or pick a preset."}]}]}`)
	toolUseInput := json.RawMessage(`{"questions":[{"question":"Should games have a clock/time control?","header":"Time controls","multiSelect":false,"options":[{"label":"No clock - untimed only (Recommended)","description":"Players take as long as they want.","confidence":0.6},{"label":"Optional preset time controls","description":"User can choose untimed or pick a preset.","confidence":0.35}]}]}`)

	sess := mocks.NewMockSessionView("confidence-recovery", "feat")
	sess.MessageLog().Append(llm.SDKMessage{
		Type: roleAssistant,
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Content: []llm.ContentBlock{
					{Type: blockTypeToolUse, ID: "toolu_ask", Name: toolNameAskUserQuestion, Input: toolUseInput},
				},
			},
		},
	})

	m := AttachModel{
		sess:        sess,
		width:       120,
		inputHeight: 1,
	}
	questions := m.parseAskUserQuestionsForDisplay(strippedInput)
	if len(questions) != 1 || len(questions[0].Options) != 2 {
		t.Fatalf("questions = %+v", questions)
	}
	if questions[0].Options[0].Confidence == nil || *questions[0].Options[0].Confidence != 0.6 {
		t.Fatalf("options[0].Confidence = %v, want 0.6", questions[0].Options[0].Confidence)
	}
	if questions[0].Options[1].Confidence == nil || *questions[0].Options[1].Confidence != 0.35 {
		t.Fatalf("options[1].Confidence = %v, want 0.35", questions[0].Options[1].Confidence)
	}

	m.pendingQuestions = questions
	output := m.renderQuestion()
	if !strings.Contains(output, "Confidence: 0.60") || !strings.Contains(output, "Confidence: 0.35") {
		t.Fatalf("expected rendered output to show recovered confidence values, got: %s", output)
	}
}

// ---------------------------------------------------------------------------
// Question scrolling tests
// ---------------------------------------------------------------------------

// makeOptions creates n options without descriptions.
func makeOptions(n int) []askUserOption {
	opts := make([]askUserOption, n)
	for i := range opts {
		opts[i] = askUserOption{Label: fmt.Sprintf("Option%d", i+1)}
	}
	return opts
}

// makeOptionsWithDesc creates n options, each with a description.
func makeOptionsWithDesc(n int) []askUserOption {
	opts := make([]askUserOption, n)
	for i := range opts {
		opts[i] = askUserOption{
			Label:       fmt.Sprintf("Option%d", i+1),
			Description: fmt.Sprintf("Description for option %d", i+1),
		}
	}
	return opts
}

func TestRenderQuestion_AllOptionsFit_NoScrollIndicators(t *testing.T) {
	options := makeOptions(5)
	m := AttachModel{
		pendingQuestions: []askUserQuestion{{
			Question: "Pick one",
			Options:  options,
		}},
		currentQuestionIdx: 0,
		selectedOption:     0,
		inputHeight:        1,
	}

	output := m.renderQuestion()

	// All 5 labels must appear.
	for i, opt := range options {
		needle := fmt.Sprintf("%d. %s", i+1, opt.Label)
		if !strings.Contains(output, needle) {
			t.Errorf("expected output to contain %q, but it did not", needle)
		}
	}

	// No scroll indicators.
	if strings.Contains(output, "more above") {
		t.Error("expected no 'more above' indicator, but found one")
	}
	if strings.Contains(output, "more below") {
		t.Error("expected no 'more below' indicator, but found one")
	}

	// chatPanelHeight = overhead (8 = q(1) + blank(1) + separator(1) +
	// "Type something"(1) + blank(1) + notes(1) + blank(1) + hint(1)) +
	// option lines (5) = 13.
	if got := m.chatPanelHeight(); got != 13 {
		t.Errorf("chatPanelHeight() = %d, want 13", got)
	}
}

func TestRenderQuestion_ManyOptions_ShowsScrollIndicators(t *testing.T) {
	options := makeOptions(25)
	m := AttachModel{
		pendingQuestions: []askUserQuestion{{
			Question: "Pick one",
			Options:  options,
		}},
		currentQuestionIdx: 0,
		selectedOption:     0,
		inputHeight:        1,
	}

	// At top: first option visible, "more below" present, "more above" absent.
	output := m.renderQuestion()
	if !strings.Contains(output, fmt.Sprintf("%d. %s", 1, options[0].Label)) {
		t.Error("expected first option to be visible at top")
	}
	if !strings.Contains(output, "more below") {
		t.Error("expected 'more below' indicator at top")
	}
	if strings.Contains(output, "more above") {
		t.Error("expected no 'more above' indicator at top")
	}

	// At bottom: last option visible, "more above" present, "more below" absent.
	m.selectedOption = 24
	m.updateQuestionScrollOffset()
	output = m.renderQuestion()
	if !strings.Contains(output, fmt.Sprintf("%d. %s", 25, options[24].Label)) {
		t.Error("expected last option to be visible at bottom")
	}
	if !strings.Contains(output, "more above") {
		t.Error("expected 'more above' indicator at bottom")
	}
	if strings.Contains(output, "more below") {
		t.Error("expected no 'more below' indicator at bottom")
	}
}

func TestRenderQuestion_WindowFollowsCursor(t *testing.T) {
	options := makeOptions(20)
	m := AttachModel{
		pendingQuestions: []askUserQuestion{{
			Question: "Pick one",
			Options:  options,
		}},
		currentQuestionIdx: 0,
		selectedOption:     15,
		inputHeight:        1,
	}
	m.updateQuestionScrollOffset()

	output := m.renderQuestion()

	// Both indicators should be present when cursor is past the initial window.
	if !strings.Contains(output, "more above") {
		t.Error("expected 'more above' indicator when cursor is past initial window")
	}
	if !strings.Contains(output, "more below") {
		t.Error("expected 'more below' indicator when options remain below cursor")
	}

	// The selected option must be visible.
	needle := fmt.Sprintf("%d. %s", 16, options[15].Label)
	if !strings.Contains(output, needle) {
		t.Errorf("expected selected option %q to be visible", needle)
	}
}

func TestChatPanelHeight_UsesVisibleWindowSize(t *testing.T) {
	tests := []struct {
		name       string
		numOptions int
		wantHeight int
	}{
		{
			name:       "25 options caps at 20",
			numOptions: 25,
			wantHeight: 20,
		},
		{
			name:       "5 options = overhead + 5 = 13",
			numOptions: 5,
			wantHeight: 13,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := AttachModel{
				pendingQuestions: []askUserQuestion{{
					Question: "Pick one",
					Options:  makeOptions(tt.numOptions),
				}},
				currentQuestionIdx: 0,
				selectedOption:     0,
				inputHeight:        1,
			}
			if got := m.chatPanelHeight(); got != tt.wantHeight {
				t.Errorf("chatPanelHeight() = %d, want %d", got, tt.wantHeight)
			}
		})
	}
}

func TestChatPanelHeight_OptionsWithDescriptions(t *testing.T) {
	// 12 options with descriptions = 24 option lines, which exceeds the cap.
	m := AttachModel{
		pendingQuestions: []askUserQuestion{{
			Question: "Pick one",
			Options:  makeOptionsWithDesc(12),
		}},
		currentQuestionIdx: 0,
		selectedOption:     0,
		inputHeight:        1,
	}
	if got := m.chatPanelHeight(); got != 20 {
		t.Errorf("chatPanelHeight() = %d, want 20 (capped)", got)
	}
}

func TestRenderQuestion_UpDownNavigation_UpdatesWindow(t *testing.T) {
	options := makeOptions(25)
	m := AttachModel{
		pendingQuestions: []askUserQuestion{{
			Question: "Pick one",
			Options:  options,
		}},
		currentQuestionIdx: 0,
		selectedOption:     0,
		inputHeight:        1,
	}

	// Simulate pressing down 15 times.
	for i := 0; i < 15; i++ {
		m.selectedOption++
		m.updateQuestionScrollOffset()

		output := m.renderQuestion()
		needle := fmt.Sprintf("%d. %s", m.selectedOption+1, options[m.selectedOption].Label)
		if !strings.Contains(output, needle) {
			t.Fatalf("after pressing down %d times, expected %q to be visible in output",
				i+1, needle)
		}
	}

	// After 15 presses (selectedOption=15), we should have scrolled past the top.
	output := m.renderQuestion()
	if !strings.Contains(output, "more above") {
		t.Error("expected 'more above' indicator after pressing down 15 times")
	}
}

func TestRenderQuestion_NavigationWrapsAtBoundaries(t *testing.T) {
	options := makeOptions(25)
	m := AttachModel{
		pendingQuestions: []askUserQuestion{{
			Question: "Pick one",
			Options:  options,
		}},
		currentQuestionIdx: 0,
		selectedOption:     0,
		inputHeight:        1,
	}

	maxIdx := len(options) // "Type something" index

	// At the bottom boundary: selectedOption = len(options), pressing down stays.
	m.selectedOption = maxIdx
	m.updateQuestionScrollOffset()
	// Simulate pressing down: should not go past maxIdx.
	next := m.selectedOption + 1
	if next > maxIdx {
		next = maxIdx
	}
	m.selectedOption = next
	m.updateQuestionScrollOffset()
	if m.selectedOption != maxIdx {
		t.Errorf("expected selectedOption to stay at %d (Type something), got %d", maxIdx, m.selectedOption)
	}

	// At the top boundary: selectedOption = 0, pressing up stays at 0.
	m.selectedOption = 0
	m.questionScrollOffset = 0
	m.updateQuestionScrollOffset()
	prev := m.selectedOption - 1
	if prev < 0 {
		prev = 0
	}
	m.selectedOption = prev
	m.updateQuestionScrollOffset()
	if m.selectedOption != 0 {
		t.Errorf("expected selectedOption to stay at 0, got %d", m.selectedOption)
	}
	if m.questionScrollOffset != 0 {
		t.Errorf("expected questionScrollOffset to be 0 at top, got %d", m.questionScrollOffset)
	}
}

func TestRenderQuestion_TypingCustom_ReducesOptionWindow(t *testing.T) {
	options := makeOptions(15)
	m := AttachModel{
		pendingQuestions: []askUserQuestion{{
			Question: "Pick one",
			Options:  options,
		}},
		currentQuestionIdx: 0,
		selectedOption:     0,
		inputHeight:        1,
		typingCustom:       false,
	}

	normalVisible := m.questionVisibleOptions()

	// Enable typing custom with a taller input.
	m.typingCustom = true
	m.inputHeight = 3
	typingVisible := m.questionVisibleOptions()

	if typingVisible >= normalVisible {
		t.Errorf("expected fewer visible options when typingCustom (got %d vs normal %d)",
			typingVisible, normalVisible)
	}
}

func TestActivateAskUserQuestions_ResetsScrollOffset(t *testing.T) {
	sess := session.NewSession("test", "feat", 0)
	sess.SetStatus(session.SessionWaitingHelp)
	sess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: "ask-reset",
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolNameAskUserQuestion,
			Input:    json.RawMessage(`{"questions":[{"question":"Pick one","header":"Test","multiSelect":false,"options":[{"label":"A","description":""},{"label":"B","description":""}]}]}`),
		},
	})
	sess.CloseDone()

	m := attachModelFromSession(sess, 120, 40)
	// Manually set a non-zero scroll offset.
	m.questionScrollOffset = 5

	// Clear active state so activateAskUserQuestions falls into the
	// activation path rather than queuing the new bundle. (When a bundle
	// is already active, parallel AUQ requests are queued; that's the
	// Phase-2 multi-AUQ behaviour. This test exercises the reset
	// invariant on the fresh-activation path.)
	m.pendingQuestions = nil
	m.currentQuestionIdx = 0

	// Re-activate questions — offset should be reset to 0.
	questions := []askUserQuestion{{
		Question: "New question",
		Options:  makeOptions(10),
	}}
	m.activateAskUserQuestions(questions, "ask-reset-2", json.RawMessage(`{}`))

	if m.questionScrollOffset != 0 {
		t.Errorf("expected questionScrollOffset=0 after activateAskUserQuestions, got %d",
			m.questionScrollOffset)
	}
}

func TestRenderQuestion_ScrollIndicatorCounts(t *testing.T) {
	options := makeOptions(20)
	m := AttachModel{
		pendingQuestions: []askUserQuestion{{
			Question: "Pick one",
			Options:  options,
		}},
		currentQuestionIdx: 0,
		selectedOption:     0,
		inputHeight:        1,
	}

	// Scroll so that exactly 3 options are hidden above.
	m.questionScrollOffset = 3
	m.selectedOption = 3
	m.updateQuestionScrollOffset()

	output := m.renderQuestion()
	start, end, needAbove, needBelow := m.questionVisibleWindow()

	if !needAbove {
		t.Fatal("expected needAbove=true after scrolling past the first 3 options")
	}
	aboveNeedle := fmt.Sprintf("%d more above", start)
	if !strings.Contains(output, aboveNeedle) {
		t.Errorf("expected %q in output, but not found", aboveNeedle)
	}

	if needBelow {
		hiddenBelow := len(options) - end
		belowNeedle := fmt.Sprintf("%d more below", hiddenBelow)
		if !strings.Contains(output, belowNeedle) {
			t.Errorf("expected %q in output, but not found", belowNeedle)
		}
	}
}

// ---------------------------------------------------------------------------
// Multi-select tests
// ---------------------------------------------------------------------------

func TestParseAskUserQuestions_MultiSelectFlag(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"Pick any","header":"Multi","multiSelect":true,"options":[{"label":"A","description":""},{"label":"B","description":""}]}]}`)
	qs := parseAskUserQuestions(input)
	if len(qs) != 1 {
		t.Fatalf("len(questions) = %d, want 1", len(qs))
	}
	if !qs[0].MultiSelect {
		t.Fatal("expected MultiSelect=true after parse")
	}
}

func TestRenderQuestion_MultiSelect_ShowsCheckboxesAndHint(t *testing.T) {
	m := AttachModel{
		pendingQuestions: []askUserQuestion{{
			Question:    "Pick any",
			MultiSelect: true,
			Options:     makeOptions(3),
		}},
		selectedOption: 1,
		selectedMulti:  map[int]bool{0: true, 2: true},
		inputHeight:    1,
	}

	out := m.renderQuestion()

	for _, want := range []string{"[x] Option1", "[ ] Option2", "[x] Option3"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in render, got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "Space to toggle") {
		t.Errorf("expected multi-select hint to mention Space, got:\n%s", out)
	}
}

func TestRenderQuestion_SingleSelect_NoCheckboxes(t *testing.T) {
	m := AttachModel{
		pendingQuestions: []askUserQuestion{{
			Question: "Pick one",
			Options:  makeOptions(2),
		}},
		inputHeight: 1,
	}

	out := m.renderQuestion()
	if strings.Contains(out, "[ ]") || strings.Contains(out, "[x]") {
		t.Errorf("single-select render should not contain checkboxes, got:\n%s", out)
	}
	if !strings.Contains(out, testHintEnterToSelect) {
		t.Errorf("expected single-select hint, got:\n%s", out)
	}
	if strings.Contains(out, "Space to toggle") {
		t.Errorf("single-select render should not show Space hint")
	}
}

// makeMultiSelectSession builds a session with a pending AskUserQuestion control
// request that has multiSelect=true and three labelled options (A, B, C).
func makeMultiSelectSession(t *testing.T) *session.Session {
	t.Helper()
	sess := session.NewSession("test", "feat", 0)
	sess.SetStatus(session.SessionWaitingHelp)
	sess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: "ask-multi",
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolNameAskUserQuestion,
			Input: json.RawMessage(`{"questions":[{"question":"Pick any","header":"Multi","multiSelect":true,"options":[` +
				`{"label":"A","description":""},{"label":"B","description":""},{"label":"C","description":""}]}]}`),
		},
	})
	sess.CloseDone()
	return sess
}

// lastUserMessageText returns the text of the most recently appended user
// message in the log, or "" if none.
func lastUserMessageText(sess *session.Session) string {
	msgs := sess.MessageLog().Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Type != "user" || msg.User == nil {
			continue
		}
		for _, b := range msg.User.Message.Content {
			if b.Type == "text" {
				return b.Text
			}
		}
	}
	return ""
}

func TestAttachMultiSelect_SpaceTogglesAndEnterJoinsLabels(t *testing.T) {
	sess := makeMultiSelectSession(t)
	m := attachModelFromSession(sess, 120, 40)
	if !m.hasActiveQuestion() {
		t.Fatal("expected active question")
	}
	if !m.pendingQuestions[0].MultiSelect {
		t.Fatal("expected MultiSelect=true")
	}

	space := tea.KeyPressMsg{Code: ' ', Text: " "}
	down := tea.KeyPressMsg{Code: 'j', Text: "j"}
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}

	// Tick option 0 (A).
	m, _ = m.Update(space)
	// Move to option 2 (C), tick it.
	m, _ = m.Update(down)
	m, _ = m.Update(down)
	m, _ = m.Update(space)
	// Un-toggle and re-toggle to verify Space flips.
	m, _ = m.Update(space)
	m, _ = m.Update(space)

	if !m.selectedMulti[0] || !m.selectedMulti[2] {
		t.Fatalf("expected options 0 and 2 ticked, got %v", m.selectedMulti)
	}
	if m.selectedMulti[1] {
		t.Fatalf("option 1 should not be ticked")
	}

	// First Enter commits the answer and advances to the recap slot. The
	// chat log is NOT yet updated — echo happens once on recap-submit to
	// avoid cluttering the log when users revise via back-nav.
	m, _ = m.Update(enter)
	if !m.onRecapSlot() {
		t.Fatalf("expected recap slot after committing last question, got idx=%d", m.currentQuestionIdx)
	}
	if got := lastUserMessageText(sess); got == "A, C" {
		t.Error("answer must not be echoed to log until recap submit")
	}

	// Second Enter on recap dispatches and echoes the final answer to the log.
	m, _ = m.Update(enter)
	if m.hasActiveQuestion() {
		t.Error("expected pendingQuestions cleared after recap submit")
	}
	if got := lastUserMessageText(sess); got != "A, C" {
		t.Errorf("expected joined answer %q in log after recap submit, got %q", "A, C", got)
	}
}

func TestAttachMultiSelect_EnterWithoutTicksSubmitsFocused(t *testing.T) {
	sess := makeMultiSelectSession(t)
	m := attachModelFromSession(sess, 120, 40)

	down := tea.KeyPressMsg{Code: 'j', Text: "j"}
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}

	// Move to option B (index 1), commit (→ recap), then confirm on recap.
	m, _ = m.Update(down)
	m, _ = m.Update(enter)
	_, _ = m.Update(enter)

	got := lastUserMessageText(sess)
	if got != "B" {
		t.Errorf("expected implicit-tick submit %q, got %q", "B", got)
	}
}

func TestAttachMultiSelect_TypeSomethingClearsTicks(t *testing.T) {
	sess := makeMultiSelectSession(t)
	m := attachModelFromSession(sess, 120, 40)

	space := tea.KeyPressMsg{Code: ' ', Text: " "}
	down := tea.KeyPressMsg{Code: 'j', Text: "j"}
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}

	// Tick option 0.
	m, _ = m.Update(space)
	if !m.selectedMulti[0] {
		t.Fatal("expected option 0 ticked")
	}

	// Navigate down past all 3 real options to the "Type something" slot
	// (index == len(Options)), then Enter.
	m, _ = m.Update(down)
	m, _ = m.Update(down)
	m, _ = m.Update(down)
	m, _ = m.Update(enter)

	if !m.typingCustom {
		t.Fatal("expected typingCustom=true after selecting Type something")
	}
	if m.selectedMulti != nil {
		t.Errorf("expected selectedMulti cleared, got %v", m.selectedMulti)
	}
}

// ---------------------------------------------------------------------------
// Preview + notes tests
// ---------------------------------------------------------------------------

func TestParseAskUserQuestions_ParsesPreview(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"Pick layout","header":"Layout","multiSelect":false,"options":[` +
		`{"label":"Sidebar","description":"","preview":"┌───┐\n│box│\n└───┘"},` +
		`{"label":"Topbar","description":""}]}]}`)
	qs := parseAskUserQuestions(input)
	if len(qs) != 1 || len(qs[0].Options) != 2 {
		t.Fatalf("parse: got %d questions with %d options", len(qs), len(qs[0].Options))
	}
	if qs[0].Options[0].Preview == "" {
		t.Fatal("expected Preview on first option")
	}
	if !strings.Contains(qs[0].Options[0].Preview, "box") {
		t.Errorf("preview content lost; got %q", qs[0].Options[0].Preview)
	}
	if qs[0].Options[1].Preview != "" {
		t.Errorf("option without preview should be empty, got %q", qs[0].Options[1].Preview)
	}
}

func TestRenderQuestion_WithPreview_RendersBoxWithContent(t *testing.T) {
	m := AttachModel{
		width: 120,
		pendingQuestions: []askUserQuestion{{
			Question: "Which layout?",
			Options: []askUserOption{
				{Label: "Sidebar", Preview: "PREVIEW_SIDEBAR_MOCKUP"},
				{Label: "Topbar", Preview: "PREVIEW_TOPBAR_MOCKUP"},
			},
		}},
		selectedOption: 0,
		inputHeight:    1,
	}

	out := m.renderQuestion()
	if !strings.Contains(out, "PREVIEW_SIDEBAR_MOCKUP") {
		t.Errorf("expected focused option's preview in render, got:\n%s", out)
	}
	// Only the focused option's preview shows.
	if strings.Contains(out, "PREVIEW_TOPBAR_MOCKUP") {
		t.Errorf("non-focused option's preview leaked into render, got:\n%s", out)
	}
	if !strings.Contains(out, "Sidebar") {
		t.Errorf("expected option label alongside preview, got:\n%s", out)
	}
}

func TestRenderQuestion_NoPreview_UnchangedLayout(t *testing.T) {
	m := AttachModel{
		width: 120,
		pendingQuestions: []askUserQuestion{{
			Question: "Plain",
			Options:  makeOptions(2),
		}},
		inputHeight: 1,
	}
	out := m.renderQuestion()
	// No rounded-corner glyph should appear when no option has a preview —
	// it's the distinctive marker of the preview box style.
	if strings.Contains(out, "╭") || strings.Contains(out, "╰") {
		t.Errorf("no-preview question should not render a rounded border, got:\n%s", out)
	}
}

func TestRenderQuestion_ShowsNotesLineWithHint(t *testing.T) {
	m := AttachModel{
		pendingQuestions: []askUserQuestion{{
			Question: "Pick",
			Options:  makeOptions(2),
		}},
		inputHeight: 1,
	}
	out := m.renderQuestion()
	if !strings.Contains(out, "Notes:") {
		t.Errorf("expected 'Notes:' label in render, got:\n%s", out)
	}
	if !strings.Contains(out, "press n to add notes") {
		t.Errorf("expected press-n hint, got:\n%s", out)
	}
	if !strings.Contains(out, "n for notes") {
		t.Errorf("expected footer to mention 'n for notes', got:\n%s", out)
	}
}

func TestRenderQuestion_NotesLineShowsSavedValue(t *testing.T) {
	m := AttachModel{
		pendingQuestions: []askUserQuestion{{
			Question: "Pick",
			Options:  makeOptions(2),
		}},
		collectedNotes: map[string]string{"Pick": "remember this"},
		inputHeight:    1,
	}
	out := m.renderQuestion()
	if !strings.Contains(out, "remember this") {
		t.Errorf("expected saved notes in render, got:\n%s", out)
	}
	if strings.Contains(out, "press n to add notes") {
		t.Errorf("saved-notes render should not show placeholder hint")
	}
}

func TestAttachNotes_NOpensEditor_EnterSavesNotes(t *testing.T) {
	sess := makeMultiSelectSession(t)
	m := attachModelFromSession(sess, 120, 40)

	// Press `n` to enter notes mode.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if !m.typingNotes {
		t.Fatal("expected typingNotes=true after pressing n")
	}

	// Type "abc" and press Enter to save.
	for _, ch := range "abc" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.typingNotes {
		t.Error("expected typingNotes=false after Enter")
	}
	if got := m.collectedNotes["Pick any"]; got != "abc" {
		t.Errorf("expected saved note %q, got %q (all=%v)", "abc", got, m.collectedNotes)
	}
}

func TestAttachNotes_EscCancelsEditor(t *testing.T) {
	sess := makeMultiSelectSession(t)
	m := attachModelFromSession(sess, 120, 40)

	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	for _, ch := range "draft" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.typingNotes {
		t.Error("expected typingNotes=false after Esc")
	}
	if got := m.collectedNotes["Pick any"]; got != "" {
		t.Errorf("expected notes discarded, got %q", got)
	}
}

func TestAttachAnswer_SendsAnnotationsAndPersistsToQALog(t *testing.T) {
	sess := makeMultiSelectSession(t)
	m := attachModelFromSession(sess, 120, 40)

	// Add notes via the `n` editor.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	for _, ch := range "remember-me" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // save notes

	// First Enter commits the focused option (implicit-tick of option A) and
	// advances to the recap slot without calling RespondToAskUser yet.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.onRecapSlot() {
		t.Fatalf("expected recap slot after first Enter, got idx=%d", m.currentQuestionIdx)
	}

	// Second Enter on the recap slot dispatches. Run the returned cmd so
	// RespondToAskUser fires — that call is what appends to sess.qaLog
	// (under lock, before the wire write).
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected non-nil cmd on recap Enter")
	}
	_ = cmd() // protocol write may fail without a real pipe; QALog is written before the write

	qa := sess.QALog()
	if len(qa) == 0 {
		t.Fatal("expected QALog entry after submission")
	}
	if qa[0].Answer != "A" {
		t.Errorf("expected Answer=A, got %q", qa[0].Answer)
	}
	if qa[0].Notes != "remember-me" {
		t.Errorf("expected Notes=remember-me persisted in QALog, got %q", qa[0].Notes)
	}
}

// ---------------------------------------------------------------------------
// Navigation + recap tests
// ---------------------------------------------------------------------------

// makeTwoQuestionModel sets up an AttachModel with two single-select questions
// (Q1: A/B, Q2: X/Y) via activateAskUserQuestions, so questionStates and
// collectedAnswers are primed correctly.
func makeTwoQuestionModel(t *testing.T) AttachModel {
	t.Helper()
	sess := session.NewSession("test-nav", "feat-nav", 0)
	sess.CloseDone()
	m := attachModelFromSession(sess, 120, 40)
	raw := json.RawMessage(`{"questions":[` +
		`{"question":"Q1","options":[{"label":"A"},{"label":"B"}]},` +
		`{"question":"Q2","options":[{"label":"X"},{"label":"Y"}]}]}`)
	m.activateAskUserQuestions(
		[]askUserQuestion{
			{Question: "Q1", Options: []askUserOption{{Label: "A"}, {Label: "B"}}},
			{Question: "Q2", Options: []askUserOption{{Label: "X"}, {Label: "Y"}}},
		},
		"req-nav", raw,
	)
	return m
}

func TestAttachNavigation_LeftArrowRestoresPriorSelection(t *testing.T) {
	m := makeTwoQuestionModel(t)

	// Answer Q1 with "B" (cursor on index 1, Enter).
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // down to B
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})   // commit + advance to Q2
	if m.currentQuestionIdx != 1 {
		t.Fatalf("expected Q2 after first Enter, got idx=%d", m.currentQuestionIdx)
	}

	// Press left-arrow to go back to Q1; prior selection (index 1 = B) must be restored.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.currentQuestionIdx != 0 {
		t.Fatalf("expected left-arrow to return to Q1, got idx=%d", m.currentQuestionIdx)
	}
	if m.selectedOption != 1 {
		t.Errorf("expected selectedOption=1 (prior pick B), got %d", m.selectedOption)
	}
	if m.collectedAnswers["Q1"] != "B" {
		t.Errorf("expected stored answer B for Q1, got %q", m.collectedAnswers["Q1"])
	}
}

func TestAttachNavigation_LeftArrowAtFirstQuestionIsNoop(t *testing.T) {
	m := makeTwoQuestionModel(t)

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.currentQuestionIdx != 0 {
		t.Errorf("expected still at Q1 after left-arrow, got idx=%d", m.currentQuestionIdx)
	}
}

func TestAttachNavigation_RightArrowWithoutAnswerIsNoop(t *testing.T) {
	m := makeTwoQuestionModel(t)

	// Right-arrow on Q1 before committing should stay at Q1.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.currentQuestionIdx != 0 {
		t.Errorf("expected still at Q1 (no commit), got idx=%d", m.currentQuestionIdx)
	}
}

func TestAttachNavigation_RightArrowAfterCommitAdvances(t *testing.T) {
	m := makeTwoQuestionModel(t)

	// Commit Q1=A, advance to Q2 (via Enter).
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.currentQuestionIdx != 1 {
		t.Fatalf("expected Q2 after Enter, got idx=%d", m.currentQuestionIdx)
	}

	// Go back to Q1 (answered), then right-arrow should move forward to Q2 again.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.currentQuestionIdx != 0 {
		t.Fatalf("expected Q1 after left, got idx=%d", m.currentQuestionIdx)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.currentQuestionIdx != 1 {
		t.Errorf("expected Q2 after right-arrow, got idx=%d", m.currentQuestionIdx)
	}
}

func TestAttachNavigation_ReviseAnswerOnBackNav(t *testing.T) {
	m := makeTwoQuestionModel(t)

	// Commit Q1=A, then back, move cursor to B, commit again.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})   // Q1: A → advance
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})    // back to Q1
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // cursor to B
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})   // commit B + advance
	if got := m.collectedAnswers["Q1"]; got != "B" {
		t.Errorf("expected revised Q1 answer = B, got %q", got)
	}
	if m.currentQuestionIdx != 1 {
		t.Errorf("expected to land back on Q2, got idx=%d", m.currentQuestionIdx)
	}
}

func TestAttachNavigation_LastEnterGoesToRecap(t *testing.T) {
	m := makeTwoQuestionModel(t)

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})    // Q1: A → Q2
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Q2: X → recap
	if !m.onRecapSlot() {
		t.Fatalf("expected recap slot after committing Q2, got idx=%d", m.currentQuestionIdx)
	}
	if cmd != nil {
		t.Error("Enter on last question must not dispatch — only advance to recap")
	}
}

func TestAttachNavigation_RecapEnterSubmitsAll(t *testing.T) {
	m := makeTwoQuestionModel(t)

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Q1 → Q2
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Q2 → recap

	if !m.onRecapSlot() {
		t.Fatalf("expected to be on recap slot")
	}

	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected non-nil dispatch cmd on recap Enter")
	}
	if m.hasActiveQuestion() {
		t.Error("expected AUQ state cleared after recap submit")
	}
}

func TestAttachNavigation_RecapLeftArrowReturnsToLastQuestion(t *testing.T) {
	m := makeTwoQuestionModel(t)

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.onRecapSlot() {
		t.Fatalf("expected recap, got idx=%d", m.currentQuestionIdx)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.currentQuestionIdx != len(m.pendingQuestions)-1 {
		t.Errorf("expected last question after left from recap, got idx=%d", m.currentQuestionIdx)
	}
}

func TestAttachNavigation_RecapRenderListsAllAnswers(t *testing.T) {
	m := makeTwoQuestionModel(t)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Q1: A
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Q2: X → recap

	out := m.renderQuestion()
	if !strings.Contains(out, "Review & Submit") {
		t.Errorf("recap render missing title, got:\n%s", out)
	}
	// Arrow runs are interleaved with ANSI reset codes, so check tokens
	// individually rather than as a literal "→ A" substring.
	if !strings.Contains(out, "Q1") || !strings.Contains(out, "A") {
		t.Errorf("recap missing Q1/answer A, got:\n%s", out)
	}
	if !strings.Contains(out, "Q2") || !strings.Contains(out, "X") {
		t.Errorf("recap missing Q2/answer X, got:\n%s", out)
	}
}

func TestAttachNavigation_LeftArrowPassesThroughInTypingCustom(t *testing.T) {
	m := makeTwoQuestionModel(t)

	// Switch Q1 to freeform typing mode via Enter on "Type something".
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // A → B
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // B → "Type something"
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})   // enter typing mode
	if !m.typingCustom {
		t.Fatal("expected typingCustom=true after Enter on Type something")
	}

	// Type some characters, then press Left. Left should move the text cursor,
	// NOT navigate to a prior question (there is none, but the key must be
	// consumed by the textarea).
	for _, ch := range "hi" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	beforeIdx := m.currentQuestionIdx
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.currentQuestionIdx != beforeIdx {
		t.Errorf("left-arrow in typingCustom must not change question idx (was %d, now %d)",
			beforeIdx, m.currentQuestionIdx)
	}
	if !m.typingCustom {
		t.Error("expected to remain in typingCustom after Left in typing mode")
	}
}

func TestAttachNavigation_MultiSelectTicksRestoredOnBackNav(t *testing.T) {
	sess := session.NewSession("test-multi-nav", "feat-mn", 0)
	sess.CloseDone()
	m := attachModelFromSession(sess, 120, 40)
	raw := json.RawMessage(`{"questions":[` +
		`{"question":"Q1","multiSelect":true,"options":[{"label":"A"},{"label":"B"},{"label":"C"}]},` +
		`{"question":"Q2","options":[{"label":"X"},{"label":"Y"}]}]}`)
	m.activateAskUserQuestions(
		[]askUserQuestion{
			{Question: "Q1", MultiSelect: true, Options: []askUserOption{{Label: "A"}, {Label: "B"}, {Label: "C"}}},
			{Question: "Q2", Options: []askUserOption{{Label: "X"}, {Label: "Y"}}},
		},
		"req-mn", raw,
	)

	// Tick A and C on Q1, then Enter.
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "}) // tick A
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // down to B
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // down to C
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "}) // tick C
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})   // commit "A, C" → advance to Q2
	if m.currentQuestionIdx != 1 {
		t.Fatalf("expected Q2 after first commit, got idx=%d", m.currentQuestionIdx)
	}
	if got := m.collectedAnswers["Q1"]; got != "A, C" {
		t.Errorf("expected Q1 answer 'A, C', got %q", got)
	}

	// Back to Q1 — ticks for A and C must be restored.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if !m.selectedMulti[0] || !m.selectedMulti[2] {
		t.Errorf("expected ticks restored at A and C, got %v", m.selectedMulti)
	}
	if m.selectedMulti[1] {
		t.Error("B should not be ticked")
	}
}

func TestAttachNavigation_FreeformAnswerRestoredAsPreview(t *testing.T) {
	sess := session.NewSession("test-freeform-nav", "feat-fn", 0)
	sess.CloseDone()
	m := attachModelFromSession(sess, 120, 40)
	raw := json.RawMessage(`{"questions":[` +
		`{"question":"Q1","options":[{"label":"A"},{"label":"B"}]},` +
		`{"question":"Q2","options":[{"label":"X"},{"label":"Y"}]}]}`)
	m.activateAskUserQuestions(
		[]askUserQuestion{
			{Question: "Q1", Options: []askUserOption{{Label: "A"}, {Label: "B"}}},
			{Question: "Q2", Options: []askUserOption{{Label: "X"}, {Label: "Y"}}},
		},
		"req-fn", raw,
	)

	// Enter "Type something" on Q1 by pressing down past the options to the
	// typing row, then Enter.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // typingCustom=true
	for _, ch := range "my custom answer" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // commit + advance to Q2

	if got := m.collectedAnswers["Q1"]; got != "my custom answer" {
		t.Fatalf("expected freeform answer stored, got %q", got)
	}

	// Back to Q1 — cursor should sit on the "Type something" row with prior
	// text visible as a muted preview.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.typingCustom {
		t.Error("expected NOT to be in typingCustom on back-nav; should land on option row for left/right nav")
	}
	q := m.pendingQuestions[m.currentQuestionIdx]
	if m.selectedOption != len(q.Options) {
		t.Errorf("expected cursor on Type something row (idx=%d), got %d", len(q.Options), m.selectedOption)
	}
	out := m.renderQuestion()
	if !strings.Contains(out, "my custom answer") {
		t.Errorf("expected prior freeform text in muted preview, got:\n%s", out)
	}

	// Pressing Enter from that row re-enters typingCustom with the prior text populated.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.typingCustom {
		t.Error("expected typingCustom=true after Enter on Type something row")
	}
	if got := m.input.Value(); got != "my custom answer" {
		t.Errorf("expected textarea pre-filled with prior text, got %q", got)
	}
}

// TestParallelAskUserQuestion_QueuedAndAnsweredFIFO is the regression
// test for feature c137aec2bdf13acf: when the LLM issues two
// AskUserQuestion tool calls in the same turn, the TUI must show the
// first one immediately and queue the second instead of overwriting
// state. Submitting the first answer must promote the queued second
// bundle and dispatch a control_response for both requestIDs.
func TestParallelAskUserQuestion_QueuedAndAnsweredFIFO(t *testing.T) {
	sess := session.NewSession("parallel-auq", "feat", 0)
	sess.SetStatus(session.SessionWaitingHelp)
	// Start with the first AUQ already pending so the constructor
	// activates it. The second arrives via the attachMsgsMsg poll path.
	sess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: testAskRequestID,
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolNameAskUserQuestion,
			Input: json.RawMessage(`{"questions":[{"question":"Pick one","header":"Q1","multiSelect":false,"options":[` +
				`{"label":"A","description":""},{"label":"B","description":""}]}]}`),
		},
	})
	sess.CloseDone()

	m := attachModelFromSession(sess, 120, 40)
	if !m.hasActiveQuestion() || m.pendingAskRequestID != testAskRequestID {
		t.Fatalf("expected ask-1 active on construction, got requestID=%q hasActive=%v",
			m.pendingAskRequestID, m.hasActiveQuestion())
	}

	// Deliver a second AUQ via the live message path while ask-1 is still
	// active. In production readMessages records every incoming
	// control_request to the session's pending list before the forwarder
	// publishes the message onto attachCh; mirror that here so the TUI's
	// stale-request guard (Phase 1.5) sees ask-2 as still-pending.
	secondCR := &llm.ControlRequestMessage{
		RequestID: "ask-2",
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolNameAskUserQuestion,
			Input: json.RawMessage(`{"questions":[{"question":"Pick another","header":"Q2","multiSelect":false,"options":[` +
				`{"label":"X","description":""},{"label":"Y","description":""}]}]}`),
		},
	}
	sess.SetLastControlRequest(secondCR)
	second := llm.SDKMessage{Type: msgTypeControlRequest, ControlRequest: secondCR}
	updated, _ := m.Update(attachMsgsMsg{generation: m.tabGeneration, messages: []llm.SDKMessage{second}})
	m = updated

	if m.pendingAskRequestID != testAskRequestID {
		t.Errorf("active requestID should still be ask-1 (got %q) — second AUQ must queue, not overwrite",
			m.pendingAskRequestID)
	}
	if len(m.pendingAskQueue) != 1 || m.pendingAskQueue[0].requestID != "ask-2" {
		t.Errorf("expected ask-2 queued, got queue=%v", m.pendingAskQueue)
	}

	// Answer ask-1 and submit: select option, advance to recap, submit.
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	updated, _ = m.Update(enter) // commit Q1 answer, advance to recap
	m = updated
	if !m.onRecapSlot() {
		t.Fatalf("expected recap slot after committing single-question answer, got idx=%d", m.currentQuestionIdx)
	}
	updated, cmd := m.Update(enter) // submit
	m = updated
	// Drain the returned cmd so RespondToAskUser is invoked.
	if cmd != nil {
		_ = cmd()
	}

	// After submit, ask-2 must have been promoted to active.
	if m.pendingAskRequestID != "ask-2" {
		t.Errorf("expected ask-2 promoted after ask-1 submit, got requestID=%q",
			m.pendingAskRequestID)
	}
	if len(m.pendingAskQueue) != 0 {
		t.Errorf("queue should be empty after promoting ask-2, got %v", m.pendingAskQueue)
	}
	if !m.hasActiveQuestion() {
		t.Error("ask-2 should be the active question after promotion")
	}

	// The session should have received a control_response for ask-1 and
	// ask-1 should be removed from the pending list, leaving ask-2.
	pending := sess.PendingControlRequests()
	for _, cr := range pending {
		if cr.RequestID == testAskRequestID {
			t.Errorf("ask-1 should be cleared from pending after submit, got %v", pending)
		}
	}
}

func TestNewAttachModel_RestoresPendingAskUserQuestionsFIFOFromSessionState(t *testing.T) {
	sess := session.NewSession("restore-auq", "feat", 0)
	sess.SetStatus(session.SessionWaitingHelp)
	sess.SetLastControlRequest(pendingAskUserControlRequest(testAskRequestID, "First question?"))
	sess.SetLastControlRequest(pendingAskUserControlRequest("ask-2", "Second question?"))
	sess.CloseDone()

	m := attachModelFromSession(sess, 120, 40)
	if !m.hasActiveQuestion() {
		t.Fatal("NewAttachModel() should activate the oldest pending AskUserQuestion")
	}
	if got, want := m.pendingAskRequestID, testAskRequestID; got != want {
		t.Fatalf("NewAttachModel() active requestID = %q, want %q", got, want)
	}
	if got, want := len(m.pendingAskQueue), 1; got != want {
		t.Fatalf("NewAttachModel() queued request count = %d, want %d", got, want)
	}
	if got, want := m.pendingAskQueue[0].requestID, "ask-2"; got != want {
		t.Errorf("NewAttachModel() queued requestID = %q, want %q", got, want)
	}
}

func TestNewAttachModel_RestoresPendingAskUserQuestionMatchingEarlierAutoPickText(t *testing.T) {
	sess := mocks.NewMockSessionView("restore-auto-pick-text-collision", "feat")
	sess.StatusVal = session.SessionWaitingHelp
	sess.PendingControlRequestsVal = []*llm.ControlRequestMessage{
		pendingAskUserControlRequest("ask-later", "Repeat this question?"),
	}
	sess.QALogVal = []session.QAPair{
		{
			Question:   "Repeat this question?",
			Answer:     "A",
			AutoPicked: true,
			Confidence: 0.91,
		},
	}

	m := attachModelFromSession(sess, 120, 40)
	if !m.hasActiveQuestion() {
		t.Fatal("NewAttachModel() should activate a still-pending AskUserQuestion even when an earlier auto-pick used the same question text")
	}
	if got, want := m.pendingAskRequestID, "ask-later"; got != want {
		t.Fatalf("NewAttachModel() active requestID = %q, want %q", got, want)
	}
}

func TestSubmitAskUserAnswers_RestoresPendingPermissionAfterAskQueueDrains(t *testing.T) {
	sess := session.NewSession("mixed-pending-controls", "feat", 0)
	sess.SetStatus(session.SessionWaitingPermission)
	sess.SetLastControlRequest(pendingAskUserControlRequest(testAskRequestID, "Answer before permission?"))
	sess.SetLastControlRequest(pendingPermissionControlRequest("perm-1"))
	sess.CloseDone()

	m := attachModelFromSession(sess, 120, 40)
	if got, want := m.pendingAskRequestID, testAskRequestID; got != want {
		t.Fatalf("NewAttachModel() active requestID = %q, want %q", got, want)
	}
	if m.showPermMenu {
		t.Fatal("NewAttachModel() should not show the later permission while the AskUserQuestion is active")
	}

	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	updated, _ := m.Update(enter)
	m = updated
	if !m.onRecapSlot() {
		t.Fatalf("Update(enter) should advance to recap before submitting, got currentQuestionIdx=%d", m.currentQuestionIdx)
	}
	updated, _ = m.Update(enter)
	m = updated

	if !m.showPermMenu {
		t.Fatal("submitting the AskUserQuestion should restore the still-pending permission menu")
	}
	if got, want := m.pendingPermRequestID, "perm-1"; got != want {
		t.Errorf("pendingPermRequestID = %q, want %q", got, want)
	}
	if m.hasActiveQuestion() {
		t.Errorf("AskUserQuestion should be cleared after submit; active requestID=%q", m.pendingAskRequestID)
	}
}

func TestSwitchToTab_RestoresPendingAskUserQuestionsFIFOFromSessionState(t *testing.T) {
	sess1 := session.NewSession("restore-tab-one", "feat", 0)
	sess2 := session.NewSession("restore-tab-two", "feat", 0)
	sess2.SetStatus(session.SessionWaitingHelp)
	sess2.SetLastControlRequest(pendingAskUserControlRequest(testAskRequestID, "First question?"))
	sess2.SetLastControlRequest(pendingAskUserControlRequest("ask-2", "Second question?"))
	sess1.CloseDone()
	sess2.CloseDone()

	tabs := []repoTab{
		{repoName: "one", sess: sess1},
		{repoName: "two", sess: sess2},
	}
	m := testAttachModel(sess1, 120, 40, tabs, 0)
	m.pendingAskQueue = []pendingAskBundle{{requestID: "stale-old"}}

	updated, _ := m.switchToTab(1)
	m = updated
	if !m.hasActiveQuestion() {
		t.Fatal("switchToTab() should activate the oldest pending AskUserQuestion")
	}
	if got, want := m.pendingAskRequestID, testAskRequestID; got != want {
		t.Fatalf("switchToTab() active requestID = %q, want %q", got, want)
	}
	if got, want := len(m.pendingAskQueue), 1; got != want {
		t.Fatalf("switchToTab() queued request count = %d, want %d", got, want)
	}
	if got, want := m.pendingAskQueue[0].requestID, "ask-2"; got != want {
		t.Errorf("switchToTab() queued requestID = %q, want %q", got, want)
	}
}

func TestSwitchToTab_RestoresPendingPermissionAfterAskQueueDrains(t *testing.T) {
	sess1 := session.NewSession("mixed-tab-one", "feat", 0)
	sess2 := session.NewSession("mixed-tab-two", "feat", 0)
	sess2.SetStatus(session.SessionWaitingPermission)
	sess2.SetLastControlRequest(pendingAskUserControlRequest(testAskRequestID, "Answer before permission?"))
	sess2.SetLastControlRequest(pendingPermissionControlRequest("perm-1"))
	sess1.CloseDone()
	sess2.CloseDone()

	tabs := []repoTab{
		{repoName: "one", sess: sess1},
		{repoName: "two", sess: sess2},
	}
	m := testAttachModel(sess1, 120, 40, tabs, 0)
	updated, _ := m.switchToTab(1)
	m = updated
	if got, want := m.pendingAskRequestID, testAskRequestID; got != want {
		t.Fatalf("switchToTab() active requestID = %q, want %q", got, want)
	}
	if m.showPermMenu {
		t.Fatal("switchToTab() should not show the later permission while the AskUserQuestion is active")
	}

	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	updated, _ = m.Update(enter)
	m = updated
	if !m.onRecapSlot() {
		t.Fatalf("Update(enter) should advance to recap before submitting, got currentQuestionIdx=%d", m.currentQuestionIdx)
	}
	updated, _ = m.Update(enter)
	m = updated

	if !m.showPermMenu {
		t.Fatal("submitting the AskUserQuestion after tab switch should restore the still-pending permission menu")
	}
	if got, want := m.pendingPermRequestID, "perm-1"; got != want {
		t.Errorf("pendingPermRequestID = %q, want %q", got, want)
	}
	if m.hasActiveQuestion() {
		t.Errorf("AskUserQuestion should be cleared after submit; active requestID=%q", m.pendingAskRequestID)
	}
}

func TestParallelAskUserQuestion_ResolvedQueuedRequestSkippedOnPromotion(t *testing.T) {
	sess := session.NewSession("parallel-auq-resolved", "feat", 0)
	sess.SetStatus(session.SessionWaitingHelp)
	sess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: testAskRequestID,
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolNameAskUserQuestion,
			Input: json.RawMessage(`{"questions":[{"question":"Pick one","header":"Q1","multiSelect":false,"options":[` +
				`{"label":"A","description":""},{"label":"B","description":""}]}]}`),
		},
	})
	sess.CloseDone()

	m := attachModelFromSession(sess, 120, 40)
	secondCR := &llm.ControlRequestMessage{
		RequestID: "ask-2",
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolNameAskUserQuestion,
			Input: json.RawMessage(`{"questions":[{"question":"Pick another","header":"Q2","multiSelect":false,"options":[` +
				`{"label":"X","description":""},{"label":"Y","description":""}]}]}`),
		},
	}
	sess.SetLastControlRequest(secondCR)
	m.activateAskUserQuestions(parseAskUserQuestions(secondCR.Request.Input), secondCR.RequestID, secondCR.Request.Input)
	if len(m.pendingAskQueue) != 1 {
		t.Fatalf("setup: queued requests = %d, want 1", len(m.pendingAskQueue))
	}

	// Mirror the auto-pick/stale-resolution path: ask-2 is answered upstream
	// while ask-1 is still active in the TUI. Promotion must re-check pending
	// state instead of showing ask-2 after ask-1 submits.
	sess.ClearPendingQuestion("ask-2")

	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	updated, _ := m.Update(enter)
	m = updated
	if !m.onRecapSlot() {
		t.Fatalf("expected recap slot after committing first answer, got idx=%d", m.currentQuestionIdx)
	}
	updated, cmd := m.Update(enter)
	m = updated
	if cmd != nil {
		_ = cmd()
	}

	if m.hasActiveQuestion() {
		t.Fatalf("resolved queued AskUserQuestion must not be promoted; active requestID=%q", m.pendingAskRequestID)
	}
	if len(m.pendingAskQueue) != 0 {
		t.Fatalf("resolved queued AskUserQuestion must be discarded, got queue=%v", m.pendingAskQueue)
	}
}

// TestAttachMsgs_SkipsAlreadyResolvedControlRequest covers the
// Phase-1.5 stale-request scenario: the session's forwarder
// synthesised a structured deny for an AUQ while no TUI was
// attached, removing the requestID from the pending list. The
// stale control_request message is still buffered in attachCh and
// gets delivered to the TUI on re-attach. The TUI must skip
// activation for any requestID that is no longer pending — re-prompting
// would cause a double-response and a UX dead end.
func TestAttachMsgs_SkipsAlreadyResolvedControlRequest(t *testing.T) {
	sess := session.NewSession("stale", "feat", 0)
	sess.SetStatus(session.SessionRunning)
	sess.CloseDone()

	m := attachModelFromSession(sess, 120, 40)
	if m.hasActiveQuestion() {
		t.Fatal("setup: no question should be active")
	}

	// Deliver a stale control_request that the session has already
	// resolved (not in pending list).
	stale := llm.SDKMessage{
		Type: msgTypeControlRequest,
		ControlRequest: &llm.ControlRequestMessage{
			RequestID: "ask-stale",
			Request: llm.ControlRequest{
				Subtype:  controlRequestSubtypeCanUseTool,
				ToolName: toolNameAskUserQuestion,
				Input: json.RawMessage(`{"questions":[{"question":"Stale?","header":"X","multiSelect":false,"options":[` +
					`{"label":"Y","description":""}]}]}`),
			},
		},
	}
	m, _ = m.Update(attachMsgsMsg{generation: m.tabGeneration, messages: []llm.SDKMessage{stale}})
	if m.hasActiveQuestion() {
		t.Errorf("stale AUQ control_request must not activate (requestID not in session.PendingControlRequests)")
	}
	if len(m.pendingAskQueue) != 0 {
		t.Errorf("stale request must not be queued, got %v", m.pendingAskQueue)
	}

	// And a stale permission request must not show the perm menu.
	stalePerm := llm.SDKMessage{
		Type: msgTypeControlRequest,
		ControlRequest: &llm.ControlRequestMessage{
			RequestID: "perm-stale",
			Request: llm.ControlRequest{
				Subtype:  controlRequestSubtypeCanUseTool,
				ToolName: toolNameBash,
				Input:    json.RawMessage(`{"command":"ls"}`),
			},
		},
	}
	m, _ = m.Update(attachMsgsMsg{generation: m.tabGeneration, messages: []llm.SDKMessage{stalePerm}})
	if m.showPermMenu {
		t.Error("stale permission control_request must not show the permission menu")
	}
}

func TestAttachMsgs_ProgressAfterAskUserAnswerDoesNotRenderWaitingTitle(t *testing.T) {
	sess := session.NewSession("progress-after-ask", "feat", 0)
	sess.SetStatus(session.SessionWaitingHelp)
	sess.SetLastControlRequest(pendingAskUserControlRequest(testAskRequestID, "Continue?"))
	sess.CloseDone()

	m := attachModelFromSession(sess, 120, 40)
	if !m.hasActiveQuestion() {
		t.Fatal("setup: AskUserQuestion should be active")
	}

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if cmd != nil {
		t.Fatal("first Enter should move to recap without dispatching")
	}
	updated, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated
	if cmd == nil {
		t.Fatal("second Enter should dispatch AskUser answer")
	}
	_ = cmd()
	if m.awaitingInput {
		t.Fatal("setup: submitting AskUser answer should clear awaitingInput")
	}

	m, _ = m.Update(attachMsgsMsg{
		generation: m.tabGeneration,
		messages: []llm.SDKMessage{
			{Type: "result", Result: &llm.ResultMessage{Subtype: "success"}},
			{Type: roleSystem, Subtype: "task_started", TaskStarted: &llm.TaskStartedMessage{
				Description: "continuing plan",
				TaskType:    taskTypeLocalAgent,
			}},
		},
	})

	view := stripANSI(m.View())
	if strings.Contains(view, "waiting for your response") {
		t.Fatalf("progress after AskUser answer should render running title, got:\n%s", view)
	}
}

// TestParallelAskUserQuestion_DuplicateRequestIDIgnored guards against
// double-activation when the same control_request is replayed (e.g.,
// during re-attach the constructor sees both the live message and the
// session's pending list).
func TestParallelAskUserQuestion_DuplicateRequestIDIgnored(t *testing.T) {
	sess := session.NewSession("dup-auq", "feat", 0)
	sess.SetStatus(session.SessionWaitingHelp)
	sess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: "ask-dup",
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolNameAskUserQuestion,
			Input: json.RawMessage(`{"questions":[{"question":"Q","header":"H","multiSelect":false,"options":[` +
				`{"label":"A","description":""}]}]}`),
		},
	})
	sess.CloseDone()
	m := attachModelFromSession(sess, 120, 40)
	if m.pendingAskRequestID != "ask-dup" {
		t.Fatalf("setup: expected ask-dup active, got %q", m.pendingAskRequestID)
	}

	// Replay the same control_request — must not enqueue a duplicate.
	replay := llm.SDKMessage{
		Type: msgTypeControlRequest,
		ControlRequest: &llm.ControlRequestMessage{
			RequestID: "ask-dup",
			Request: llm.ControlRequest{
				Subtype:  controlRequestSubtypeCanUseTool,
				ToolName: toolNameAskUserQuestion,
				Input: json.RawMessage(`{"questions":[{"question":"Q","header":"H","multiSelect":false,"options":[` +
					`{"label":"A","description":""}]}]}`),
			},
		},
	}
	m, _ = m.Update(attachMsgsMsg{generation: m.tabGeneration, messages: []llm.SDKMessage{replay}})
	if len(m.pendingAskQueue) != 0 {
		t.Errorf("duplicate requestID should not enqueue, got queue=%v", m.pendingAskQueue)
	}
}
