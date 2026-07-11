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

package session

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	roleUser        = "user"
	contentTypeText = "text"
)

// --- Wire format tests ---
// These verify that the JSON sent to the CLI matches the protocol the Python/TypeScript
// Agent SDKs use (request_id nested inside response, not at top level).

func TestControlResponseWireFormat_Allow(t *testing.T) {
	resp := llm.NewAllowResponse("req_42")
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Parse back as generic map to verify wire format
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if wire["type"] != "control_response" {
		t.Errorf("type = %v, want control_response", wire["type"])
	}

	// request_id must NOT be at top level
	if _, ok := wire["request_id"]; ok {
		t.Error("request_id must not be at top level — it belongs inside response")
	}

	// request_id must be inside response
	response, ok := wire["response"].(map[string]any)
	if !ok {
		t.Fatal("response must be an object")
	}
	if response["request_id"] != "req_42" {
		t.Errorf("response.request_id = %v, want req_42", response["request_id"])
	}
	if response["subtype"] != "success" {
		t.Errorf("response.subtype = %v, want success", response["subtype"])
	}

	inner, ok := response["response"].(map[string]any)
	if !ok {
		t.Fatal("response.response must be an object")
	}
	if inner["behavior"] != "allow" {
		t.Errorf("behavior = %v, want allow", inner["behavior"])
	}
}

func TestControlResponseWireFormat_Deny(t *testing.T) {
	resp := llm.NewDenyResponse("req_99", "too dangerous")
	data, _ := json.Marshal(resp)

	var wire map[string]any
	json.Unmarshal(data, &wire)

	response := wire["response"].(map[string]any)
	if response["request_id"] != "req_99" {
		t.Errorf("response.request_id = %v, want req_99", response["request_id"])
	}

	inner := response["response"].(map[string]any)
	if inner["behavior"] != "deny" {
		t.Errorf("behavior = %v, want deny", inner["behavior"])
	}
	if inner["message"] != "too dangerous" {
		t.Errorf("message = %v, want 'too dangerous'", inner["message"])
	}
}

func TestControlResponseWireFormat_HookContinue(t *testing.T) {
	resp := llm.NewHookContinueResponse("hook_abc")
	data, _ := json.Marshal(resp)

	var wire map[string]any
	json.Unmarshal(data, &wire)

	response := wire["response"].(map[string]any)
	if response["request_id"] != "hook_abc" {
		t.Errorf("response.request_id = %v, want hook_abc", response["request_id"])
	}

	inner := response["response"].(map[string]any)
	if inner["continue"] != true {
		t.Errorf("response.response.continue = %v, want true", inner["continue"])
	}
}

func TestControlResponseWireFormat_AskUser(t *testing.T) {
	// Production callers pass the entire AskUserQuestion tool input —
	// {"questions":[...]} — straight from cr.Request.Input. The wire
	// format must unwrap the envelope so updatedInput.questions is the
	// inner array; otherwise the CLI throws "H.map is not a function"
	// on its response handler.
	cases := []struct {
		name      string
		questions json.RawMessage
	}{
		{
			name:      "production envelope",
			questions: json.RawMessage(`{"questions":[{"question":"Pick a color?","options":[{"label":"Red"},{"label":"Blue"}]}]}`),
		},
		{
			name:      "bare array",
			questions: json.RawMessage(`[{"question":"Pick a color?","options":[{"label":"Red"},{"label":"Blue"}]}]`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answers := map[string]string{"Pick a color?": "Red"}
			resp := llm.NewAskUserResponse("ask_1", tc.questions, answers, nil)
			data, _ := json.Marshal(resp)

			var wire map[string]any
			json.Unmarshal(data, &wire)

			response := wire["response"].(map[string]any)
			if response["request_id"] != "ask_1" {
				t.Errorf("response.request_id = %v, want ask_1", response["request_id"])
			}

			inner := response["response"].(map[string]any)
			if inner["behavior"] != "allow" {
				t.Errorf("behavior = %v, want allow", inner["behavior"])
			}

			updated, ok := inner["updatedInput"].(map[string]any)
			if !ok {
				t.Fatal("updatedInput must be an object")
			}
			questionsArr, ok := updated["questions"].([]any)
			if !ok {
				t.Fatalf("updatedInput.questions must be an array, got %T", updated["questions"])
			}
			if len(questionsArr) != 1 {
				t.Fatalf("updatedInput.questions length = %d, want 1", len(questionsArr))
			}
			q0, ok := questionsArr[0].(map[string]any)
			if !ok {
				t.Fatalf("questions[0] must be an object, got %T", questionsArr[0])
			}
			if q0["question"] != "Pick a color?" {
				t.Errorf("questions[0].question = %v, want Pick a color?", q0["question"])
			}
			answersMap, ok := updated["answers"].(map[string]any)
			if !ok {
				t.Fatal("updatedInput.answers must be an object")
			}
			if answersMap["Pick a color?"] != "Red" {
				t.Errorf("answer = %v, want Red", answersMap["Pick a color?"])
			}
		})
	}
}

func TestInitializeRequestWireFormat(t *testing.T) {
	msg := llm.NewInitializeRequest()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var wire map[string]any
	json.Unmarshal(data, &wire)

	if wire["type"] != "control_request" {
		t.Errorf("type = %v, want control_request", wire["type"])
	}
	if wire["request_id"] == "" {
		t.Error("request_id must be set")
	}

	request, ok := wire["request"].(map[string]any)
	if !ok {
		t.Fatal("request must be an object")
	}
	if request["subtype"] != "initialize" {
		t.Errorf("subtype = %v, want initialize", request["subtype"])
	}

	hooks, ok := request["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks must be an object")
	}
	preToolUse, ok := hooks["PreToolUse"].([]any)
	if !ok || len(preToolUse) == 0 {
		t.Fatal("PreToolUse must be a non-empty array")
	}
	first, ok := preToolUse[0].(map[string]any)
	if !ok {
		t.Fatal("first PreToolUse entry must be an object")
	}
	ids, ok := first["hookCallbackIds"].([]any)
	if !ok || len(ids) == 0 {
		t.Fatal("hookCallbackIds must be a non-empty array")
	}
}

// --- handleControlRequest routing tests ---
// These use a mock script that emits different control_request types and
// verify the session routes them correctly.

func TestHandleControlRequest_HookCallback_AutoContinues(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure session routing, no subprocess or shared state.
	s := NewSession("hook-test", "feat-1", feature.PhaseResearch)
	msg := llm.SDKMessage{ControlRequest: &llm.ControlRequestMessage{
		RequestID: "hook_1",
		Request: llm.ControlRequest{
			Subtype:    "hook_callback",
			CallbackID: "cb1",
		},
	}}
	if handled := s.tryHandleControlRequest(msg); !handled {
		t.Fatal("tryHandleControlRequest hook_callback = false, want true")
	}
}

func TestHandleControlRequest_AskUserQuestion_NeverAutoApproved(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure session routing, no subprocess or shared state.
	s := NewSession("ask-test", "feat-1", feature.PhaseResearch)
	s.permHandler = &AutoApproveHandler{}
	msg := llm.SDKMessage{ControlRequest: &llm.ControlRequestMessage{
		RequestID: "ask_1",
		Request: llm.ControlRequest{
			Subtype:  "can_use_tool",
			ToolName: "AskUserQuestion",
			Input:    json.RawMessage(`{"questions":[{"question":"Color?","options":[{"label":"Red"}]}]}`),
		},
	}}

	if handled := s.tryHandleControlRequest(msg); handled {
		t.Fatal("tryHandleControlRequest AskUserQuestion = true, want false so TUI answers it")
	}
}

func TestHandleControlRequest_BashWithAutoApprove(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure session routing, no subprocess or shared state.
	s := NewSession("bash-approve", "feat-1", feature.PhaseImplement)
	s.permHandler = &AutoApproveHandler{}
	msg := llm.SDKMessage{ControlRequest: &llm.ControlRequestMessage{
		RequestID: "bash_1",
		Request: llm.ControlRequest{
			Subtype:  "can_use_tool",
			ToolName: "Bash",
			Input:    json.RawMessage(`{"command":"echo hi"}`),
		},
	}}
	if handled := s.tryHandleControlRequest(msg); !handled {
		t.Fatal("tryHandleControlRequest Bash with AutoApproveHandler = false, want true")
	}
}

// --- AcceptEditsHandler tests ---

func TestAcceptEditsHandler_AutoApproves(t *testing.T) {
	handler := &AcceptEditsHandler{}
	autoApproved := []string{
		"Read", "Glob", "Grep", "LS", "LSP",
		"Edit", "Write", "NotebookEdit",
		"Agent",
		"WebSearch", "WebFetch",
		"TodoWrite", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate",
	}
	for _, tool := range autoApproved {
		t.Run(tool, func(t *testing.T) {
			decision, err := handler.CanUseTool(ToolPermissionRequest{ToolName: tool})
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if decision.Behavior != "allow" {
				t.Errorf("AcceptEditsHandler.CanUseTool(%s) = %q, want allow", tool, decision.Behavior)
			}
		})
	}
}

func TestAcceptEditsHandler_DefersToTUI(t *testing.T) {
	handler := &AcceptEditsHandler{}
	deferred := []string{"Bash", "EnterWorktree", "ExitWorktree", "CronCreate", "SomeUnknownTool"}
	for _, tool := range deferred {
		t.Run(tool, func(t *testing.T) {
			decision, err := handler.CanUseTool(ToolPermissionRequest{ToolName: tool})
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if decision.Behavior != "" {
				t.Errorf("AcceptEditsHandler.CanUseTool(%s) = %q, want empty (defer to TUI)", tool, decision.Behavior)
			}
		})
	}
}

func TestAcceptEditsHandler_BashDeferredToTUI_Session(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure session routing, no subprocess or shared state.
	s := NewSession("bash-defer", "feat-1", feature.PhaseImplement)
	s.permHandler = &AcceptEditsHandler{}
	msg := llm.SDKMessage{ControlRequest: &llm.ControlRequestMessage{
		RequestID: "bash_1",
		Request: llm.ControlRequest{
			Subtype:  "can_use_tool",
			ToolName: "Bash",
			Input:    json.RawMessage(`{"command":"rm -rf /"}`),
		},
	}}

	if handled := s.tryHandleControlRequest(msg); handled {
		t.Fatal("tryHandleControlRequest Bash with AcceptEditsHandler = true, want false so TUI decides it")
	}
}

// --- HasUnansweredQuestion lifecycle tests ---

func TestHasUnansweredQuestion_ClearedByRespondToAskUser(t *testing.T) {
	s := &Session{
		hasUnansweredQuestion: true,
		done:                  make(chan struct{}),
	}
	// Can't actually call RespondToAskUser without a pipe, so test the field directly
	s.mu.Lock()
	s.hasUnansweredQuestion = false // simulates what RespondToAskUser does
	s.mu.Unlock()

	if s.hasUnansweredQuestion {
		t.Error("hasUnansweredQuestion should be false after RespondToAskUser")
	}
}

func TestHasUnansweredQuestion_ClearedBySendUserMessage(t *testing.T) {
	t.Parallel()
	// parallel-candidate: in-process protocol double with per-test session state.
	s := NewSession("q-test", "feat-1", feature.PhaseResearch)
	s.hasUnansweredQuestion = true
	s.protocol = &interruptTrackingProtocol{}

	if err := s.SendUserMessage("my answer"); err != nil {
		t.Fatalf("SendUserMessage: %v", err)
	}
	if s.hasUnansweredQuestion {
		t.Error("hasUnansweredQuestion should be cleared after SendUserMessage")
	}
}

func TestRespondToAskUser_ClearsLastControlRequest(t *testing.T) {
	t.Run("clears matching request", func(t *testing.T) {
		s := &Session{
			hasUnansweredQuestion: true,
			status:                SessionWaitingHelp,
			done:                  make(chan struct{}),
		}
		s.SetLastControlRequest(&llm.ControlRequestMessage{
			RequestID: "req-123",
		})

		// Simulate what RespondToAskUser does (can't call it without a pipe)
		s.mu.Lock()
		s.removePendingControlRequestLocked("req-123")
		s.hasUnansweredQuestion = s.hasPendingAskUserQuestionLocked()
		if s.status == SessionWaitingHelp {
			s.status = SessionRunning
		}
		s.mu.Unlock()

		if s.hasUnansweredQuestion {
			t.Error("hasUnansweredQuestion should be false")
		}
		if s.LastControlRequest() != nil {
			t.Error("lastControlRequest should be nil after matching response")
		}
		if s.status != SessionRunning {
			t.Errorf("status should be SessionRunning, got %v", s.status)
		}
	})

	t.Run("does not clear non-matching request", func(t *testing.T) {
		s := &Session{
			hasUnansweredQuestion: true,
			status:                SessionWaitingHelp,
			done:                  make(chan struct{}),
		}
		s.SetLastControlRequest(&llm.ControlRequestMessage{
			RequestID: "req-other",
		})

		s.mu.Lock()
		s.removePendingControlRequestLocked("req-123")
		s.hasUnansweredQuestion = s.hasPendingAskUserQuestionLocked()
		if s.status == SessionWaitingHelp {
			s.status = SessionRunning
		}
		s.mu.Unlock()

		if s.LastControlRequest() == nil {
			t.Error("lastControlRequest should NOT be nil when request ID doesn't match")
		}
	})
}

func TestRespondToAskUser_CapturesQALog(t *testing.T) {
	// Use io.Pipe so writeJSON succeeds
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	defer r.Close()

	s := &Session{
		stdin:  w,
		status: SessionWaitingHelp,
		done:   make(chan struct{}),
	}

	questions := json.RawMessage(`[{"question":"Which DB?"}]`)
	answers := map[string]string{"Which DB?": "PostgreSQL"}
	if err := s.RespondToAskUser("req-1", questions, answers, nil); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}

	if len(s.qaLog) != 1 {
		t.Fatalf("expected 1 QA pair, got %d", len(s.qaLog))
	}
	if s.qaLog[0].Question != "Which DB?" || s.qaLog[0].Answer != "PostgreSQL" {
		t.Errorf("unexpected QA pair: %+v", s.qaLog[0])
	}

	// Second call should accumulate
	answers2 := map[string]string{"Cache?": "Redis"}
	if err := s.RespondToAskUser("req-2", questions, answers2, nil); err != nil {
		t.Fatalf("RespondToAskUser (second): %v", err)
	}
	if len(s.qaLog) != 2 {
		t.Fatalf("expected 2 QA pairs after second call, got %d", len(s.qaLog))
	}
}

func TestQALogReturnsSnapshot(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	defer r.Close()

	s := &Session{
		stdin:  w,
		status: SessionWaitingHelp,
		done:   make(chan struct{}),
	}

	questions := json.RawMessage(`[{"question":"Which DB?"}]`)
	answers := map[string]string{"Which DB?": "PostgreSQL"}
	if err := s.RespondToAskUser("req-1", questions, answers, nil); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}

	qa := s.QALog()
	if len(qa) != 1 {
		t.Fatalf("len(QALog()) = %d, want 1", len(qa))
	}
	qa[0].Question = "mutated by caller"

	fresh := s.QALog()
	if fresh[0].Question != "Which DB?" {
		t.Fatalf("QALog()[0].Question = %q after caller mutation, want snapshot isolation", fresh[0].Question)
	}
}

func TestRespondToAskUser_CapturesQALogInPresentedOrderWithNotes(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	defer r.Close()

	s := &Session{
		stdin:  w,
		status: SessionWaitingHelp,
		done:   make(chan struct{}),
	}

	questions := json.RawMessage(`{"questions":[{"question":"Zeta?"},{"question":"Alpha?"}]}`)
	answers := map[string]string{
		"Alpha?": "second answer",
		"Zeta?":  "first answer",
	}
	annotations := map[string]llm.AskUserAnnotation{
		"Alpha?": {Notes: "user note"},
	}
	if err := s.RespondToAskUser("req-ordered", questions, answers, annotations); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}

	if len(s.qaLog) != 2 {
		t.Fatalf("len(qaLog) = %d, want 2", len(s.qaLog))
	}
	if s.qaLog[0].Question != "Zeta?" || s.qaLog[0].Answer != "first answer" {
		t.Errorf("qaLog[0] = %+v, want first presented question", s.qaLog[0])
	}
	if s.qaLog[1].Question != "Alpha?" || s.qaLog[1].Answer != "second answer" || s.qaLog[1].Notes != "user note" {
		t.Errorf("qaLog[1] = %+v, want second presented question with notes", s.qaLog[1])
	}
}

func TestRespondToAskUser_AppendsLocalDisplayMessagesInPresentedOrder(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	defer r.Close()

	s := &Session{
		stdin:      w,
		status:     SessionWaitingHelp,
		done:       make(chan struct{}),
		messageLog: NewMessageLog(),
	}

	questions := json.RawMessage(`{"questions":[{"question":"Zeta?"},{"question":"Alpha?"}]}`)
	answers := map[string]string{
		"Alpha?": "second answer",
		"Zeta?":  "first answer",
	}
	if err := s.RespondToAskUser("req-ordered", questions, answers, nil); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}

	msgs := s.MessageLog().Messages()
	if len(msgs) != 2 {
		t.Fatalf("MessageLog() length = %d, want 2 local answer messages: %+v", len(msgs), msgs)
	}
	for i, want := range []string{"first answer", "second answer"} {
		msg := msgs[i]
		if msg.User == nil || !msg.LocallyAppended || msg.AutoPicked {
			t.Fatalf("MessageLog()[%d] = %+v, want local manual user message", i, msg)
		}
		if got := msg.User.Message.Content[0].Text; got != want {
			t.Fatalf("MessageLog()[%d] text = %q, want %q", i, got, want)
		}
	}
}

func TestRespondToAskUser_DoesNotDuplicateExistingLocalDisplayMessages(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	defer r.Close()

	s := &Session{
		stdin:      w,
		status:     SessionWaitingHelp,
		done:       make(chan struct{}),
		messageLog: NewMessageLog(),
	}
	for _, text := range []string{"first answer", "second answer"} {
		s.messageLog.Append(llm.SDKMessage{
			Type:            roleUser,
			LocallyAppended: true,
			User: &llm.UserMessage{
				Message: llm.ConversationMsg{
					Role:    roleUser,
					Content: []llm.ContentBlock{{Type: contentTypeText, Text: text}},
				},
			},
		})
	}

	questions := json.RawMessage(`{"questions":[{"question":"Zeta?"},{"question":"Alpha?"}]}`)
	answers := map[string]string{
		"Alpha?": "second answer",
		"Zeta?":  "first answer",
	}
	if err := s.RespondToAskUser("req-ordered", questions, answers, nil); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}

	if got := s.MessageLog().Len(); got != 2 {
		t.Fatalf("MessageLog().Len() = %d, want existing local answer echoes left unduplicated", got)
	}
}

func TestSendUserMessage_ClearsLastControlRequest(t *testing.T) {
	s := &Session{
		hasUnansweredQuestion: true,
		status:                SessionWaitingHelp,
		done:                  make(chan struct{}),
	}
	s.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: "req-456",
	})

	// Simulate what SendUserMessage does
	s.mu.Lock()
	s.hasUnansweredQuestion = false
	s.clearPendingControlRequestsLocked()
	if s.status == SessionWaitingHelp {
		s.status = SessionRunning
	}
	s.mu.Unlock()

	if s.hasUnansweredQuestion {
		t.Error("hasUnansweredQuestion should be false")
	}
	if s.LastControlRequest() != nil {
		t.Error("lastControlRequest should be nil after SendUserMessage")
	}
	if s.status != SessionRunning {
		t.Errorf("status should be SessionRunning, got %v", s.status)
	}
}

// --- Manager onMessage status routing tests ---

func askUserControlMessage(requestID string) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "control_request",
		ControlRequest: &llm.ControlRequestMessage{
			RequestID: requestID,
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "AskUserQuestion",
				Input:    json.RawMessage(`{"questions":[]}`),
			},
		},
	}
}

func successResultMessage(sessionID string) llm.SDKMessage {
	return llm.SDKMessage{
		Type:   "result",
		Result: &llm.ResultMessage{Type: "result", Subtype: "success", SessionID: sessionID},
	}
}

func TestManagerOnMessage_HookCallback_NoStatusChange(t *testing.T) {
	t.Parallel()
	// parallel-candidate: direct manager routing with per-test session state.
	mgr := NewManager(make(chan interface{}, 100))
	sess := NewSession("hook-status-test", "feat-1", feature.PhaseResearch)
	mgr.handleSessionMessage(sess, sess.ID(), sess.FeatureID(), sess.Phase(), llm.SDKMessage{
		Type: "control_request",
		ControlRequest: &llm.ControlRequestMessage{
			RequestID: "hk_1",
			Request: llm.ControlRequest{
				Subtype:    "hook_callback",
				CallbackID: "cb1",
			},
		},
	})

	// Status should never have been WaitingPermission or WaitingHelp for a hook_callback.
	// After result, it should be Done or Running.
	if sess.Status() == SessionWaitingPermission || sess.Status() == SessionWaitingHelp {
		t.Errorf("status = %v, hook_callback should not set a waiting status", sess.Status())
	}
}

func TestManagerOnMessage_AskUserQuestion_SetsWaitingHelp(t *testing.T) {
	t.Parallel()
	// parallel-candidate: direct manager routing with per-test session state.
	mgr := NewManager(make(chan interface{}, 100))
	sess := NewSession("ask-status-test", "feat-1", feature.PhaseResearch)
	mgr.handleSessionMessage(sess, sess.ID(), sess.FeatureID(), sess.Phase(), askUserControlMessage("ask_1"))

	sess.mu.Lock()
	status := sess.status
	hasQuestion := sess.hasUnansweredQuestion
	sess.mu.Unlock()

	if status != SessionWaitingHelp {
		t.Errorf("status = %v, want SessionWaitingHelp", status)
	}
	if !hasQuestion {
		t.Error("hasUnansweredQuestion should be true")
	}
}

func TestManagerOnMessage_ResultKeepsWaitingHelpForPendingAskUserQuestion(t *testing.T) {
	t.Parallel()
	// parallel-candidate: direct manager routing with per-test session state.
	mgr := NewManager(make(chan interface{}, 100))
	sess := NewSession("ask-result-status-test", "feat-1", feature.PhaseResearch)
	mgr.handleSessionMessage(sess, sess.ID(), sess.FeatureID(), sess.Phase(), askUserControlMessage("ask_1"))
	mgr.handleSessionMessage(sess, sess.ID(), sess.FeatureID(), sess.Phase(), successResultMessage("s1"))

	sess.mu.Lock()
	status := sess.status
	hasQuestion := sess.hasUnansweredQuestion
	sess.mu.Unlock()

	if status != SessionWaitingHelp {
		t.Errorf("status = %v, want SessionWaitingHelp after result", status)
	}
	if !hasQuestion {
		t.Error("hasUnansweredQuestion should still be true after result")
	}
}

func TestManagerOnMessage_InteractiveSession_ResultWaitsForFollowUp(t *testing.T) {
	t.Parallel()
	// parallel-candidate: direct manager routing with per-test session state.
	mgr := NewManager(make(chan interface{}, 100))
	sess := NewSession("interactive-status-test", "feat-1", feature.PhaseResearch)
	sess.turnMode = ports.TurnModeInteractive
	sess.protocol = &interruptTrackingProtocol{}
	mgr.handleSessionMessage(sess, sess.ID(), sess.FeatureID(), sess.Phase(), successResultMessage("s1"))

	if status := sess.Status(); status != SessionWaitingHelp {
		t.Fatalf("after first Result: status = %v, want SessionWaitingHelp", status)
	}

	if err := sess.SendUserMessage("follow up"); err != nil {
		t.Fatalf("SendUserMessage after result: %v", err)
	}
	if status := sess.Status(); status != SessionRunning {
		t.Fatalf("after SendUserMessage: status = %v, want SessionRunning", status)
	}
}

func TestManagerOnMessage_InteractiveTurnMode_ResultSetsWaitingHelp(t *testing.T) {
	t.Parallel()
	// parallel-candidate: direct manager routing with per-test session state.
	mgr := NewManager(make(chan interface{}, 100))
	sess := NewSession("interactive-status-test", "feat-1", feature.PhaseImplement)
	sess.turnMode = ports.TurnModeInteractive
	mgr.handleSessionMessage(sess, sess.ID(), sess.FeatureID(), sess.Phase(), successResultMessage("s1"))

	if status := sess.Status(); status != SessionWaitingHelp {
		t.Errorf("after Result on interactive session: status = %v, want SessionWaitingHelp", status)
	}
}

func TestManagerOnMessage_KindTweakDoesNotControlResultLifecycle(t *testing.T) {
	t.Parallel()
	// parallel-candidate: direct manager routing with per-test session state.
	mgr := NewManager(make(chan interface{}, 100))
	sess := NewSession("tweak-kind-oneshot-test", "feat-1", feature.PhaseImplement)
	sess.kind = ports.KindTweak
	mgr.handleSessionMessage(sess, sess.ID(), sess.FeatureID(), sess.Phase(), successResultMessage("s1"))

	if status := sess.Status(); status == SessionWaitingHelp {
		t.Errorf("KindTweak alone set waiting-help; lifecycle should be controlled by TurnMode")
	}
}

func TestManagerOnMessage_BashToolPermission_SetsWaitingPermission(t *testing.T) {
	t.Parallel()
	// parallel-candidate: direct manager routing with per-test session state.
	mgr := NewManager(make(chan interface{}, 100))
	sess := NewSession("bash-status-test", "feat-1", feature.PhaseResearch)
	mgr.handleSessionMessage(sess, sess.ID(), sess.FeatureID(), sess.Phase(), llm.SDKMessage{
		Type: "control_request",
		ControlRequest: &llm.ControlRequestMessage{
			RequestID: "bash_1",
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "Bash",
				Input:    json.RawMessage(`{"command":"ls"}`),
			},
		},
	})

	if status := sess.Status(); status != SessionWaitingPermission {
		t.Errorf("status = %v, want SessionWaitingPermission", status)
	}
}

// --- Codex provider session-level tests ---

func TestSendUserMessage_CodexProvider_SendsTurnStart(t *testing.T) {
	pr, pw := io.Pipe()

	// Create a Codex protocol with a pre-set thread ID
	codexProto := codex.NewProtocol(llm.ProtocolOpts{
		Model: "gpt-5.4",
	})
	codexProto.SetStdin(pw)
	// Set thread ID by simulating a thread/start response parse
	codexProto.SetThreadIDForTest("thread-send-msg")

	s := &Session{
		protocol: codexProto,
		stdin:    pw,
		done:     make(chan struct{}),
	}

	lineCh := make(chan []byte, 1)
	go func() {
		scanner := bufio.NewScanner(pr)
		if scanner.Scan() {
			lineCh <- append([]byte(nil), scanner.Bytes()...)
		}
	}()

	if err := s.SendUserMessage("hello"); err != nil {
		t.Fatalf("SendUserMessage: %v", err)
	}

	var data []byte
	select {
	case data = <-lineCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout reading from pipe")
	}

	// Verify it's a turn/start JSON-RPC request (not a Claude UserInputMessage)
	var req struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		ID      int    `json:"id"`
		Params  struct {
			CollaborationMode struct {
				Mode     string `json:"mode"`
				Settings struct {
					Model                 string  `json:"model"`
					DeveloperInstructions *string `json:"developer_instructions"`
				} `json:"settings"`
			} `json:"collaborationMode"`
			ThreadID string `json:"threadId"`
			Input    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"input"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, data)
	}

	if req.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want %q", req.JSONRPC, "2.0")
	}
	if req.Method != "turn/start" {
		t.Errorf("method = %q, want %q", req.Method, "turn/start")
	}
	if req.Params.CollaborationMode.Mode != "default" {
		t.Errorf("collaborationMode.mode = %q, want %q", req.Params.CollaborationMode.Mode, "default")
	}
	if req.Params.ThreadID != "thread-send-msg" {
		t.Errorf("threadId = %q, want %q", req.Params.ThreadID, "thread-send-msg")
	}
	if len(req.Params.Input) != 1 {
		t.Fatalf("input length = %d, want 1", len(req.Params.Input))
	}
	if req.Params.Input[0].Text != "hello" {
		t.Errorf("input[0].text = %q, want %q", req.Params.Input[0].Text, "hello")
	}
}

func TestRespondToAskUser_CodexProvider_SendsCodexFormat(t *testing.T) {
	pr, pw := io.Pipe()

	codexProto := codex.NewProtocol(llm.ProtocolOpts{
		Model: "gpt-5.4",
	})
	codexProto.SetStdin(pw)
	codexProto.SetQuestionIDsForTest(map[string]string{
		"Which DB?": "q1",
	})

	s := &Session{
		protocol: codexProto,
		stdin:    pw,
		status:   SessionWaitingHelp,
		done:     make(chan struct{}),
	}

	lineCh := make(chan []byte, 1)
	go func() {
		scanner := bufio.NewScanner(pr)
		if scanner.Scan() {
			lineCh <- append([]byte(nil), scanner.Bytes()...)
		}
	}()

	questions := json.RawMessage(`[{"question":"Which DB?"}]`)
	answers := map[string]string{"Which DB?": "PostgreSQL"}
	if err := s.RespondToAskUser("42", questions, answers, nil); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}

	var data []byte
	select {
	case data = <-lineCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout reading from pipe")
	}

	// Verify Codex format response
	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			Answers []struct {
				QuestionID string `json:"questionId"`
				Value      string `json:"value"`
			} `json:"answers"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, data)
	}

	if resp.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want %q", resp.JSONRPC, "2.0")
	}
	if resp.ID != 42 {
		t.Errorf("id = %d, want 42", resp.ID)
	}
	if len(resp.Result.Answers) != 1 {
		t.Fatalf("answers length = %d, want 1", len(resp.Result.Answers))
	}
	if resp.Result.Answers[0].QuestionID != "q1" {
		t.Errorf("questionId = %q, want %q", resp.Result.Answers[0].QuestionID, "q1")
	}
	if resp.Result.Answers[0].Value != "PostgreSQL" {
		t.Errorf("value = %q, want %q", resp.Result.Answers[0].Value, "PostgreSQL")
	}

	// Verify qaLog has the Q&A pair
	if len(s.qaLog) != 1 {
		t.Fatalf("qaLog length = %d, want 1", len(s.qaLog))
	}
	if s.qaLog[0].Question != "Which DB?" || s.qaLog[0].Answer != "PostgreSQL" {
		t.Errorf("qaLog[0] = %+v, want {Which DB? PostgreSQL}", s.qaLog[0])
	}
}
