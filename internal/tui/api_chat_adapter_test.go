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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestAPIChatEventsFromSnapshotNormalizesTranscriptAndCompletion(t *testing.T) {
	t.Parallel()

	active := newTestAPIChatSession()
	detail := server.SessionDetailDTO{SessionSummaryDTO: server.SessionSummaryDTO{
		ID:        chatSessionID,
		FeatureID: chatSessionID,
		Status:    "Running",
		TurnState: "waiting_input",
	}}
	transcript := &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
		{Index: 0, BlockIndex: 0, Role: "assistant", Type: "text", Text: "First paragraph."},
		{Index: 0, BlockIndex: 1, Role: "assistant", Type: "text", Text: "Second paragraph."},
	}}

	events := apiChatEventsFromSnapshot(apiChatSnapshotInput{
		Session:       active,
		Detail:        detail,
		Transcript:    transcript,
		WasResponding: true,
	})

	assertChatEventKinds(t, events, chatEventAssistantText, chatEventAssistantText, chatEventCompleted)
	if events[0].Text != "First paragraph." || events[1].Text != "Second paragraph." {
		t.Fatalf("assistant text events = %#v, want both transcript fragments", events)
	}
}

func TestAPIChatEventsFromSnapshotEmitsNoAnswerCompletion(t *testing.T) {
	t.Parallel()

	active := newTestAPIChatSession()
	detail := server.SessionDetailDTO{SessionSummaryDTO: server.SessionSummaryDTO{
		ID:        chatSessionID,
		FeatureID: chatSessionID,
		Status:    "Running",
		TurnState: "waiting_input",
	}}
	transcript := &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
		{Index: 0, Role: "system", Type: "tool_progress", Tool: "Read", Redacted: true},
	}}

	events := apiChatEventsFromSnapshot(apiChatSnapshotInput{
		Session:       active,
		Detail:        detail,
		Transcript:    transcript,
		WasResponding: true,
	})

	assertChatEventKinds(t, events, chatEventToolActivity, chatEventAssistantText, chatEventCompleted)
	if events[1].Text != apiChatNoAnswerText {
		t.Fatalf("no-answer text = %q, want %q", events[1].Text, apiChatNoAnswerText)
	}
}

func TestAPIChatEventsFromSnapshotPendingAskUserSuppressesCompletion(t *testing.T) {
	t.Parallel()

	active := newTestAPIChatSession()
	askControl := server.ControlRequestDTO{
		RequestID: "ask-1",
		SessionID: chatSessionID,
		FeatureID: chatSessionID,
		ToolName:  "AskUserQuestion",
		Status:    "pending",
		Questions: []server.AskUserQuestionDTO{{
			Question: "Pick a direction",
			Options:  []server.AskUserOptionDTO{{Label: "Alpha"}, {Label: "Beta"}},
		}},
	}
	detail := server.SessionDetailDTO{
		SessionSummaryDTO: server.SessionSummaryDTO{
			ID:        chatSessionID,
			FeatureID: chatSessionID,
			Status:    "WaitingHelp",
		},
		PendingControls: []server.ControlRequestDTO{askControl},
	}

	events := apiChatEventsFromSnapshot(apiChatSnapshotInput{
		Session:       active,
		Detail:        detail,
		Controls:      []server.ControlRequestDTO{askControl},
		WasResponding: true,
	})

	assertChatEventKinds(t, events, chatEventPendingQuestion)
	if events[0].RequestID != "ask-1" || len(events[0].Questions) != 1 || events[0].Questions[0].Question != "Pick a direction" {
		t.Fatalf("pending question event = %#v, want parsed AskUser question", events[0])
	}
}

func newTestAPIChatSession() *apiSessionView {
	return &apiSessionView{
		id:        chatSessionID,
		featureID: chatSessionID,
		log:       session.NewMessageLog(),
	}
}

func assertChatEventKinds(t *testing.T, events []chatEvent, want ...chatEventKind) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("event count = %d, want %d: %#v", len(events), len(want), events)
	}
	for i, event := range events {
		if event.Kind != want[i] {
			t.Fatalf("event[%d].Kind = %v, want %v: %#v", i, event.Kind, want[i], events)
		}
	}
}
