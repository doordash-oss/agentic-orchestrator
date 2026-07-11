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
)

func TestChatModelApplyEventsFinalizesAssistantTurn(t *testing.T) {
	t.Parallel()

	m := NewChatModel(100, 24, nil, "/tmp", "test", nil, "", "")
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

	m := NewChatModel(100, 24, nil, "/tmp", "test", nil, "", "")
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
