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
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestChatModelApplyEventsFinalizesAssistantTurn(t *testing.T) {
	t.Parallel()

	m := newChatModel(100, 24)
	m.responding = true

	m = m.ApplyEvents([]chatEvent{
		{Kind: chatEventAssistantText, Text: testAssistantTextFirstParagraph},
		{Kind: chatEventAssistantText, Text: testAssistantTextSecondParagraph},
		{Kind: chatEventCompleted},
	})

	if m.responding {
		t.Fatal("chat remained responding after completed event")
	}
	if len(m.turns) != 1 {
		t.Fatalf("turn count = %d, want one assistant turn: %+v", len(m.turns), m.turns)
	}
	if m.turns[0].InProgress {
		t.Fatal("assistant turn remained in progress after completed event")
	}
	for _, want := range []string{testAssistantTextFirstParagraph, testAssistantTextSecondParagraph} {
		if !strings.Contains(m.turns[0].Text, want) {
			t.Fatalf("assistant turn missing %q: %q", want, m.turns[0].Text)
		}
	}
}

func TestChatModelApplyEventsActivatesPendingQuestion(t *testing.T) {
	t.Parallel()

	m := newChatModel(100, 24)
	m.responding = true
	raw := []byte(`{"questions":[{"question":"Pick one","options":[{"label":"A"},{"label":"B"}]}]}`)

	m = m.ApplyEvents([]chatEvent{{
		Kind:      chatEventPendingQuestion,
		RequestID: testAskRequestID,
		Raw:       raw,
		Questions: []askUserQuestion{{
			Question: testQuestionPickOne,
			Options:  []askUserOption{{Label: "A"}, {Label: "B"}},
		}},
	}})

	if m.responding {
		t.Fatal("chat remained responding while waiting on a pending question")
	}
	if !m.hasActiveQuestion() || m.pendingAskRequestID != testAskRequestID {
		t.Fatalf("pending question was not activated: requestID=%q questions=%+v", m.pendingAskRequestID, m.questions)
	}
}

func TestChatModelPendingQuestionTrimsStreamedQuestionDraft(t *testing.T) {
	t.Parallel()

	const intro = "Yo! What can I help you with?"
	const question = "What would you like me to help you with?"
	raw := []byte(`{"questions":[{"question":"What would you like me to help you with?","options":[{"label":"Ask about Agentic Orchestrator features (Recommended)","description":"Quick answers from the user guide and codebase","confidence":0.9},{"label":"Search the codebase","description":"Find files, patterns, and trace code paths"},{"label":"Debug an issue","description":"Inspect local state and logs"}]}]}`)
	m := newChatModel(120, 24)
	m.responding = true

	m = m.ApplyEvents([]chatEvent{
		{Kind: chatEventAssistantText, Text: intro + "\n\n" + question + "\n\n1. Ask"},
		{Kind: chatEventAssistantText, Text: intro + "\n\n" + question + "\n\n1. Ask about Agentic Orchestrator features (Recommended): Quick answers from the user guide and codebase [confidence: 0.90]\n2. Search the codebase: Find files"},
		{
			Kind:      chatEventPendingQuestion,
			RequestID: testAskRequestID,
			Raw:       raw,
			Questions: []askUserQuestion{{
				Question: question,
				Options: []askUserOption{
					{Label: "Ask about Agentic Orchestrator features (Recommended)", Description: "Quick answers from the user guide and codebase"},
					{Label: "Search the codebase", Description: "Find files, patterns, and trace code paths"},
					{Label: "Debug an issue", Description: "Inspect local state and logs"},
				},
			}},
		},
	})

	if !m.hasActiveQuestion() {
		t.Fatal("pending question was not activated")
	}
	if len(m.turns) != 1 {
		t.Fatalf("turn count = %d, want only the intro assistant turn: %+v", len(m.turns), m.turns)
	}
	if got := strings.TrimSpace(m.turns[0].Text); got != intro {
		t.Fatalf("assistant transcript text = %q, want only intro %q", got, intro)
	}
	view := stripANSI(m.View())
	if count := strings.Count(view, question); count != 1 {
		t.Fatalf("AskUser question rendered %d times, want only picker copy:\n%s", count, view)
	}
}

// TestChatModelPendingQuestionNeverDeletesWholeTurn guards against the
// disappearing-response bug: when the streamed text has no separate intro (the
// question stem is a prefix of the entire in-progress turn, as happens for a
// synthesized AskUserQuestion whose stem leads straight into its options), the
// turn must still be visible in the transcript once the picker activates rather
// than being wiped out entirely.
func TestChatModelPendingQuestionNeverDeletesWholeTurn(t *testing.T) {
	t.Parallel()

	const question = "Which database should I target?"
	const text = question + "\n\n1. Postgres (Recommended): Mature. [confidence: 0.88]\n2. MySQL: Familiar. [confidence: 0.40]\n3. SQLite: Simplest. [confidence: 0.20]"
	raw := []byte(`{"questions":[{"question":"Which database should I target?","options":[{"label":"Postgres (Recommended)"},{"label":"MySQL"},{"label":"SQLite"}]}]}`)
	m := newChatModel(120, 24)
	m.responding = true

	m = m.ApplyEvents([]chatEvent{
		{Kind: chatEventAssistantText, Text: text},
		{
			Kind:      chatEventPendingQuestion,
			RequestID: testAskRequestID,
			Raw:       raw,
			Questions: []askUserQuestion{{
				Question: question,
				Options: []askUserOption{
					{Label: "Postgres (Recommended)"},
					{Label: "MySQL"},
					{Label: "SQLite"},
				},
			}},
		},
	})

	if !m.hasActiveQuestion() {
		t.Fatal("pending question was not activated")
	}
	if len(m.turns) != 1 {
		t.Fatalf("turn count = %d, want the assistant turn preserved: %+v", len(m.turns), m.turns)
	}
	if strings.TrimSpace(m.turns[0].Text) == "" {
		t.Fatal("assistant turn text was wiped when the question activated, want the original response kept")
	}
}

func TestChatModelPendingPermissionAnswerUsesSessionPermissionDecision(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{}
	sess := newAPIChatSession(client, chatSessionID)
	m := NewAPIChatModel(100, 24, client)
	m.sess = sess
	m.responding = true

	m = m.ApplyEvents([]chatEvent{{
		Kind:      chatEventPendingPermission,
		RequestID: testPermissionRequestIDPerm1,
		ToolName:  toolNameBash,
		Text:      "ps -p 74045",
		Raw:       []byte(`{"command":"ps -p 74045"}`),
		Remember:  &server.PermissionRememberPreviewDTO{Pattern: "Bash(ps *)", Scope: ""},
	}})
	if m.responding {
		t.Fatal("chat remained responding while waiting on a pending permission")
	}
	if !m.hasActivePermission() || m.pendingPermRequestID != testPermissionRequestIDPerm1 {
		t.Fatalf("pending permission was not activated: requestID=%q", m.pendingPermRequestID)
	}

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("Update(y) returned nil command, want permission answer command")
	}
	msg := cmd()

	if updated.hasActivePermission() {
		t.Fatal("pending permission remained active after answer")
	}
	if !updated.responding {
		t.Fatal("chat did not resume responding after permission answer")
	}
	if got := client.permissionAnswers; len(got) != 1 || got[0].RequestID != testPermissionRequestIDPerm1 || got[0].SessionID != chatSessionID || got[0].Decision != "allow_once" {
		t.Fatalf("permission answers = %+v, want allow_once for chat permission", got)
	}
	tick, ok := msg.(chatRecoveryTickMsg)
	if !ok || tick.sess != sess {
		t.Fatalf("permission answer command returned %#v, want chatRecoveryTickMsg for chat session", msg)
	}
}
