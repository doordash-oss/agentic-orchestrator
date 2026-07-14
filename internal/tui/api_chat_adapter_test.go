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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// testTurnStateWaitingInput, testControlStatusPending, and testTurnStateFailed
// are SessionDetailDTO/ControlRequestDTO fixture values for this file's tests.
const (
	testTurnStateWaitingInput = "waiting_input"
	testControlStatusPending  = "pending"
	testTurnStateFailed       = "failed"
)

func TestAPIChatEventsFromSnapshotNormalizesTranscriptAndCompletion(t *testing.T) {
	t.Parallel()

	active := newTestAPIChatSession()
	detail := apiTestSessionDetail(server.SessionSummaryDTO{
		ID:        chatSessionID,
		FeatureID: chatSessionID,
		Status:    session.SessionRunning.String(),
		TurnState: testTurnStateWaitingInput,
	})
	transcript := &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
		{Index: 0, BlockIndex: 0, Role: roleAssistant, Type: blockTypeText, Text: testAssistantTextFirstParagraph},
		{Index: 0, BlockIndex: 1, Role: roleAssistant, Type: blockTypeText, Text: testAssistantTextSecondParagraph},
	}}

	events := apiChatEventsFromSnapshot(apiChatSnapshotInput{
		Session:       active,
		Detail:        detail,
		Transcript:    transcript,
		WasResponding: true,
	})

	assertChatEventKinds(t, events, chatEventAssistantText, chatEventAssistantText, chatEventCompleted)
	if events[0].Text != testAssistantTextFirstParagraph || events[1].Text != testAssistantTextSecondParagraph {
		t.Fatalf("assistant text events = %#v, want both transcript fragments", events)
	}
}

func TestAPIChatEventsFromSnapshotEmitsNoAnswerCompletion(t *testing.T) {
	t.Parallel()

	active := newTestAPIChatSession()
	detail := apiTestSessionDetail(server.SessionSummaryDTO{
		ID:        chatSessionID,
		FeatureID: chatSessionID,
		Status:    session.SessionRunning.String(),
		TurnState: testTurnStateWaitingInput,
	})
	transcript := &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
		{Index: 0, Role: roleSystem, Type: transcriptTypeToolProgress, Tool: toolNameRead, Redacted: true},
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
		RequestID: testAskRequestID,
		SessionID: chatSessionID,
		FeatureID: chatSessionID,
		ToolName:  toolNameAskUserQuestion,
		Status:    testControlStatusPending,
		Questions: []server.AskUserQuestionDTO{{
			Question: testQuestionPickDirection,
			Options:  []server.AskUserOptionDTO{{Label: testOptionLabelAlpha}, {Label: testOptionLabelBeta}},
		}},
	}
	detail := apiTestSessionDetailWith(
		server.SessionSummaryDTO{
			ID:        chatSessionID,
			FeatureID: chatSessionID,
			Status:    session.SessionWaitingHelp.String(),
		},
		server.SessionDetailDTO{
			PendingControls: []server.ControlRequestDTO{askControl},
		})

	events := apiChatEventsFromSnapshot(apiChatSnapshotInput{
		Session:       active,
		Detail:        detail,
		Controls:      []server.ControlRequestDTO{askControl},
		WasResponding: true,
	})

	assertChatEventKinds(t, events, chatEventPendingQuestion)
	if events[0].RequestID != testAskRequestID || len(events[0].Questions) != 1 || events[0].Questions[0].Question != testQuestionPickDirection {
		t.Fatalf("pending question event = %#v, want parsed AskUser question", events[0])
	}
}

func TestAPIChatTranscriptRowUpdateEmitsPartialAssistantText(t *testing.T) {
	t.Parallel()

	active := newTestAPIChatSession()
	detail := apiTestSessionDetail(server.SessionSummaryDTO{
		ID:        chatSessionID,
		FeatureID: chatSessionID,
		Status:    session.SessionRunning.String(),
		TurnState: turnStateRunning,
	})

	events := apiChatEventsFromSnapshot(apiChatSnapshotInput{
		Session: active,
		Detail:  detail,
		Transcript: &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
			{Index: 1, BlockIndex: 0, Role: roleAssistant, Type: blockTypeText, Text: "What would you like"},
		}},
	})
	assertChatEventKinds(t, events, chatEventAssistantText)
	if events[0].Partial {
		t.Fatalf("first transcript row event unexpectedly partial: %#v", events[0])
	}

	events = apiChatEventsFromSnapshot(apiChatSnapshotInput{
		Session: active,
		Detail:  detail,
		Transcript: &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
			{Index: 1, BlockIndex: 0, Role: roleAssistant, Type: blockTypeText, Text: "What would you like me to help you with?"},
		}},
	})

	assertChatEventKinds(t, events, chatEventAssistantText)
	if !events[0].Partial {
		t.Fatalf("updated transcript row event Partial=false, want partial replacement: %#v", events[0])
	}
}

func TestAPIChatEventsFromSnapshotPendingPermissionSuppressesCompletion(t *testing.T) {
	t.Parallel()

	active := newTestAPIChatSession()
	permControl := server.ControlRequestDTO{
		RequestID: testPermissionRequestIDPerm1,
		SessionID: chatSessionID,
		FeatureID: chatSessionID,
		ToolName:  toolNameBash,
		Status:    testControlStatusPending,
		Summary:   "ps -p 74045",
		Input:     map[string]interface{}{"command": "ps -p 74045"},
	}
	detail := apiTestSessionDetailWith(
		server.SessionSummaryDTO{
			ID:        chatSessionID,
			FeatureID: chatSessionID,
			Status:    session.SessionWaitingPermission.String(),
		},
		server.SessionDetailDTO{
			PendingControls: []server.ControlRequestDTO{permControl},
		})

	events := apiChatEventsFromSnapshot(apiChatSnapshotInput{
		Session:       active,
		Detail:        detail,
		Controls:      []server.ControlRequestDTO{permControl},
		WasResponding: true,
	})

	assertChatEventKinds(t, events, chatEventPendingPermission)
	if events[0].RequestID != testPermissionRequestIDPerm1 || events[0].ToolName != toolNameBash || events[0].Text != "ps -p 74045" {
		t.Fatalf("pending permission event = %#v, want Bash permission with summary", events[0])
	}
}

func TestAPIChatEventsFromSnapshotFailedWithoutTranscriptEmitsError(t *testing.T) {
	t.Parallel()

	active := newTestAPIChatSession()
	detail := apiTestSessionDetailWith(
		server.SessionSummaryDTO{
			ID:        chatSessionID,
			FeatureID: chatSessionID,
			Status:    session.SessionFailed.String(),
			TurnState: testTurnStateFailed,
		},
		server.SessionDetailDTO{
			SafeError: "process exited with code 1",
		})

	events := apiChatEventsFromSnapshot(apiChatSnapshotInput{
		Session:       active,
		Detail:        detail,
		Transcript:    &server.TranscriptResponse{},
		WasResponding: true,
	})

	assertChatEventKinds(t, events, chatEventError)
	if events[0].Text != "process exited with code 1" {
		t.Fatalf("error text = %q, want safe session error", events[0].Text)
	}
}

func TestAPIChatAnswerEchoSuppressesLocalTranscriptEcho(t *testing.T) {
	t.Parallel()

	active := newTestAPIChatSession()
	active.client = &fakeTUIAPIClient{}
	raw := json.RawMessage(`{"questions":[{"question":"Pick a direction"}]}`)
	if err := active.RespondToAskUser(testAskRequestID, raw, map[string]string{testQuestionPickDirection: "nothing"}, nil); err != nil {
		t.Fatalf("RespondToAskUser() error = %v", err)
	}
	if !apiHasLocalUserEcho(active.log, "nothing") {
		t.Fatal("api chat session log missing local answer echo")
	}

	events := apiChatEventsFromSnapshot(apiChatSnapshotInput{
		Session: active,
		Detail: apiTestSessionDetail(server.SessionSummaryDTO{
			ID:        chatSessionID,
			FeatureID: chatSessionID,
			Status:    session.SessionRunning.String(),
		}),
		Transcript: &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
			{Index: 1, Role: roleUser, Type: blockTypeText, Text: "nothing", LocallyAppended: true},
		}},
	})

	if len(events) != 0 {
		t.Fatalf("events = %#v, want local transcript echo suppressed", events)
	}
}

func newTestAPIChatSession() *apiSessionView {
	return &apiSessionView{
		id:                    chatSessionID,
		featureID:             chatSessionID,
		log:                   session.NewMessageLog(),
		lastTranscriptMessage: -1,
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
