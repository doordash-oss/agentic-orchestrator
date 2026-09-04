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

package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

const validQuestion = `{"id":"scope","kind":"choice","question":"Which scope?","header":"Scope","options":[{"label":"Source","description":"Committed sources","confidence":0.9,"recommended":true},{"label":"Generated","description":"Include generated files","confidence":0.5,"recommended":false},{"label":"Everything","description":"Include temporary files","confidence":0.2,"recommended":false}]}`

func dynamicRequest(t *testing.T, id int, tool, arguments, thread string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "item/tool/call", "params": map[string]any{"threadId": thread, "turnId": "turn-1", "callId": "call-1", "tool": tool, "arguments": json.RawMessage(arguments)}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestThreadContractSurvivesResumeAndFollowups(t *testing.T) {
	for _, resume := range []string{"", "existing-thread"} {
		t.Run("resume="+resume, func(t *testing.T) {
			var buf bytes.Buffer
			p := NewProtocol(llm.ProtocolOpts{Model: "gpt-6-astra", SystemPrompt: "Phase rules", StructuredCompletion: true, ResumeSessionID: resume, StateDir: t.TempDir()})
			p.SetStdin(&buf)
			if resume != "" {
				fresh := NewProtocol(llm.ProtocolOpts{StructuredCompletion: true, StateDir: p.opts.StateDir})
				if err := fresh.recordThreadContract(resume); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.startThread(); err != nil {
				t.Fatal(err)
			}
			var req struct {
				Method string
				Params map[string]json.RawMessage
			}
			if err := json.Unmarshal(buf.Bytes(), &req); err != nil {
				t.Fatal(err)
			}
			if string(req.Params["developerInstructions"]) != `"Phase rules"` {
				t.Fatalf("developer instructions missing: %s", buf.Bytes())
			}
			if !bytes.Contains(req.Params["config"], []byte(`"include_collaboration_mode_instructions":false`)) {
				t.Fatalf("collaboration boilerplate enabled: %s", buf.Bytes())
			}
			if resume == "" {
				var tools []struct{ Name string }
				if err := json.Unmarshal(req.Params["dynamicTools"], &tools); err != nil {
					t.Fatal(err)
				}
				if len(tools) != 2 || tools[0].Name != "ask_user" || tools[1].Name != "complete_phase" {
					t.Fatalf("tools=%+v", tools)
				}
			} else if req.Method != "thread/resume" {
				t.Fatalf("method=%s", req.Method)
			}
			p.SetThreadIDForTest("thread-1")
			for _, followup := range []bool{false, true} {
				buf.Reset()
				var err error
				if followup {
					err = p.SendUserMessage("continue")
				} else {
					err = p.startTurn("begin")
				}
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(buf.String(), "collaborationMode") || !strings.Contains(buf.String(), `"model":"gpt-6-astra"`) {
					t.Fatalf("turn resets contract or loses model: %s", buf.Bytes())
				}
			}
		})
	}
}

func TestStructuredQuestionAfterToolsRetainsCallAndQuestionIDs(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{StructuredCompletion: true})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")
	_, err := p.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"id":"read-1","type":"commandExecution","aggregatedOutput":"repository context"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := p.ParseLine(dynamicRequest(t, 41, "ask_user", validQuestion, "thread-1"))
	if err != nil || len(messages) != 1 || messages[0].ControlRequest == nil {
		t.Fatalf("request messages=%+v err=%v", messages, err)
	}
	request := messages[0].ControlRequest
	if request.Request.ToolName != "AskUserQuestion" || request.RequestID != "41" {
		t.Fatalf("request=%+v", request)
	}
	if !bytes.Contains(request.Request.Input, []byte(`"label":"Source (Recommended)"`)) || !bytes.Contains(request.Request.Input, []byte(`"confidence":0.9`)) {
		t.Fatalf("question UI contract lost: %s", request.Request.Input)
	}
	messages, err = p.ParseLine(dynamicRequest(t, 42, "complete_phase", `{"status":"success"}`, "thread-1"))
	if err != nil || len(messages) != 0 || !strings.Contains(buf.String(), "pending question") {
		t.Fatalf("pending question did not prevent completion: %+v %s %v", messages, buf.Bytes(), err)
	}
	buf.Reset()
	if err := p.RespondToAskUser("41", request.Request.Input, map[string]string{"Which scope?": "Custom answer"}, map[string]llm.AskUserAnnotation{"Which scope?": {Notes: "Preserve originals"}}); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID     int
		Result struct {
			Success      bool
			ContentItems []struct {
				Type string
				Text string
			}
		}
	}
	if err := json.Unmarshal(buf.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != 41 || !response.Result.Success || len(response.Result.ContentItems) != 1 || response.Result.ContentItems[0].Type != "inputText" {
		t.Fatalf("wire response=%s", buf.Bytes())
	}
	var answer struct {
		CallID      string
		Answers     map[string]struct{ Answers []string }
		Annotations map[string]llm.AskUserAnnotation
	}
	if err := json.Unmarshal([]byte(response.Result.ContentItems[0].Text), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.CallID != "call-1" || len(answer.Answers["scope"].Answers) != 1 || answer.Answers["scope"].Answers[0] != "Custom answer" || answer.Annotations["scope"].Notes != "Preserve originals" {
		t.Fatalf("answer=%+v", answer)
	}
	if err := p.RespondToAskUser("41", nil, map[string]string{"Which scope?": "again"}, nil); err == nil {
		t.Fatal("answered request accepted twice")
	}
	messages, err = p.ParseLine(dynamicRequest(t, 43, "complete_phase", `{"status":"success","summary":"Ready"}`, "thread-1"))
	if err != nil || len(messages) != 1 || messages[0].CompletionRequest == nil || messages[0].CompletionRequest.Intent.Status != llm.CompletionIntentSuccess {
		t.Fatalf("completion=%+v err=%v", messages, err)
	}
	buf.Reset()
	if err := p.RespondToCompletion("43", false, "Artifact is missing research questions"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"success":false`) || !strings.Contains(buf.String(), "missing research questions") {
		t.Fatalf("rejection=%s", buf.Bytes())
	}
}

func TestInvalidQuestionReturnsInlineCorrection(t *testing.T) {
	cases := map[string]string{
		"missing recommended":      strings.Replace(validQuestion, `,"recommended":false`, "", 1),
		"missing confidence":       strings.Replace(validQuestion, `"confidence":0.9,`, "", 1),
		"confidence out of bounds": strings.Replace(validQuestion, `"confidence":0.9`, `"confidence":1.1`, 1),
		"tied recommendation":      strings.Replace(validQuestion, `"confidence":0.5`, `"confidence":0.9`, 1),
		"duplicate recommendation": strings.Replace(validQuestion, `"recommended":false`, `"recommended":true`, 1),
		"unknown field":            strings.Replace(validQuestion, `"kind":"choice"`, `"kind":"choice","extra":true`, 1),
		"implicit freeform":        `{"id":"name","question":"Name?","header":"Name","options":[]}`,
		"too few choices":          `{"id":"name","kind":"choice","question":"Name?","header":"Name","options":[]}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			p := NewProtocol(llm.ProtocolOpts{})
			p.SetStdin(&buf)
			messages, err := p.ParseLine(dynamicRequest(t, 51, "ask_user", args, "thread-1"))
			if err != nil || len(messages) != 0 || !strings.Contains(buf.String(), `"success":false`) {
				t.Fatalf("messages=%+v err=%v response=%s", messages, err, buf.Bytes())
			}
			if len(p.pendingQuestions) != 0 {
				t.Fatal("invalid question became pending")
			}
		})
	}
}

func TestExplicitFreeFormBlocksAnotherQuestion(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{})
	p.SetStdin(&buf)
	args := `{"id":"version","kind":"free_form","question":"Exact version?","header":"Version","options":[]}`
	messages, err := p.ParseLine(dynamicRequest(t, 61, "ask_user", args, "thread-1"))
	if err != nil || len(messages) != 1 || messages[0].ControlRequest == nil {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	messages, err = p.ParseLine(dynamicRequest(t, 62, "ask_user", args, "thread-1"))
	if err != nil || len(messages) != 0 || !strings.Contains(buf.String(), "Wait for the pending question") {
		t.Fatalf("second question accepted: %+v %v %s", messages, err, buf.Bytes())
	}
	if err := p.RespondToAskUser("61", nil, map[string]string{"Exact version?": "1.2.3"}, nil); err != nil {
		t.Fatal(err)
	}
	messages, err = p.ParseLine(dynamicRequest(t, 63, "ask_user", args, "thread-1"))
	if err != nil || len(messages) != 1 {
		t.Fatalf("next question failed: %+v %v", messages, err)
	}
}

func TestNativeInteractiveQuestionUsesCurrentWireSchema(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{Interactive: true})
	p.SetStdin(&buf)
	messages, err := p.ParseLine([]byte(`{"id":71,"method":"item/tool/requestUserInput","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"scope","question":"Which scope?","header":"Scope","options":[{"label":"Source","description":"Committed files"}]}]}}`))
	if err != nil || len(messages) != 1 || messages[0].ControlRequest == nil {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	if err := p.RespondToAskUser("71", nil, map[string]string{"Which scope?": "Custom answer"}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"answers":{"scope":{"answers":["Custom answer"]}}`) {
		t.Fatalf("native response=%s", buf.Bytes())
	}
	if p.dynamicTools() != nil || p.UsesStructuredCompletion() {
		t.Fatal("interactive chat acquired phase tools")
	}
}

func TestProseQuestionsNeverBecomeSyntheticControlRequests(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		p := NewProtocol(llm.ProtocolOpts{Interactive: interactive})
		p.SetThreadIDForTest("thread-1")
		p.lastAssistantText = "Which scope?\n1. Source (Recommended): Source files [confidence: 0.9]\n2. All files: Entire repository [confidence: 0.4]\n3. None: Stop [confidence: 0.1]"
		message, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
		if !ok || message.ControlRequest != nil || message.Result == nil {
			t.Fatalf("turn=%+v ok=%v", message, ok)
		}
	}
}

func TestStructuredResumeRequiresMatchingThreadContract(t *testing.T) {
	dir := t.TempDir()
	fresh := NewProtocol(llm.ProtocolOpts{StructuredCompletion: true, StateDir: dir})
	if err := fresh.recordThreadContract("new-thread"); err != nil {
		t.Fatal(err)
	}
	for _, thread := range []string{"new-thread", "legacy-thread"} {
		p := NewProtocol(llm.ProtocolOpts{StructuredCompletion: true, StateDir: dir, ResumeSessionID: thread})
		var buf bytes.Buffer
		p.SetStdin(&buf)
		err := p.startThread()
		if thread == "legacy-thread" {
			if err == nil || !strings.Contains(err.Error(), "restart the phase") || buf.Len() != 0 {
				t.Fatalf("legacy resume did not fail before RPC: %v %s", err, buf.Bytes())
			}
		} else if err != nil {
			t.Fatalf("new thread could not resume: %v", err)
		}
	}
}

func TestDynamicToolsRejectStaleTurnsAndChildCompletion(t *testing.T) {
	for _, tc := range []struct{ tool, thread, turn, args string }{
		{"ask_user", "thread-1", "stale", validQuestion},
		{"ask_user", "child-thread", "turn-1", validQuestion},
		{"complete_phase", "thread-1", "stale", `{"status":"success"}`},
		{"complete_phase", "child-thread", "turn-1", `{"status":"success"}`},
	} {
		p := NewProtocol(llm.ProtocolOpts{StructuredCompletion: true})
		p.SetThreadIDForTest("thread-1")
		p.turnID = tc.turn
		var buf bytes.Buffer
		p.SetStdin(&buf)
		messages, err := p.ParseLine(dynamicRequest(t, 81, tc.tool, tc.args, tc.thread))
		if err != nil || len(messages) != 0 || !strings.Contains(buf.String(), `"success":false`) {
			t.Fatalf("unexpected call accepted: %+v %v %s", messages, err, buf.Bytes())
		}
	}
}

func TestThreadContractWriteFailureReleasesHandshake(t *testing.T) {
	dir := t.TempDir()
	// A file where the contract directory must be created makes MkdirAll fail.
	if err := os.WriteFile(filepath.Join(dir, "codex-contracts"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewProtocol(llm.ProtocolOpts{StructuredCompletion: true, StateDir: dir})
	var buf bytes.Buffer
	p.SetStdin(&buf)
	if err := p.sendInitialize(); err != nil {
		t.Fatal(err)
	}
	msg, emit, handled := p.handleResponse(2, json.RawMessage(`{"thread":{"id":"fresh-thread"}}`), nil)
	if !emit || !handled || msg.Result == nil || !msg.Result.IsError {
		t.Fatalf("contract write failure not reported: emit=%v handled=%v msg=%+v", emit, handled, msg)
	}
	select {
	case <-p.threadReady:
	default:
		t.Fatal("threadReady still blocks Handshake after the contract write failed")
	}
	p.mu.Lock()
	err := p.threadErr
	p.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "record Codex phase contract") {
		t.Fatalf("threadErr=%v, want contract write error", err)
	}
}

func TestFreshThreadPersistsContractOnlyForStructuredPhases(t *testing.T) {
	for _, structured := range []bool{false, true} {
		dir := t.TempDir()
		p := NewProtocol(llm.ProtocolOpts{StructuredCompletion: structured, StateDir: dir})
		_, emit, handled := p.handleResponse(1, json.RawMessage(`{"thread":{"id":"fresh-thread"}}`), nil)
		if emit || !handled {
			t.Fatalf("thread response emit=%v handled=%v", emit, handled)
		}
		resumed := NewProtocol(llm.ProtocolOpts{StructuredCompletion: true, StateDir: dir, ResumeSessionID: "fresh-thread"})
		err := resumed.checkResumableContract()
		if structured && err != nil {
			t.Fatalf("structured thread did not persist tools contract: %v", err)
		}
		if !structured && err == nil {
			t.Fatal("ordinary thread falsely marked compatible")
		}
	}
}

// A writer that delivers the next server request before returning reproduces
// the response-order boundary without depending on goroutine scheduling.
type responseBoundaryWriter struct {
	onWrite func()
	err     error
}

func (w *responseBoundaryWriter) Write(data []byte) (int, error) {
	if callback := w.onWrite; callback != nil {
		w.onWrite = nil
		callback()
	}
	if w.err != nil {
		return 0, w.err
	}
	return len(data), nil
}

func TestResponseReleasesPendingStateBeforeNextServerRequest(t *testing.T) {
	for _, firstTool := range []string{"ask_user", "complete_phase"} {
		for _, nextTool := range []string{"ask_user", "complete_phase"} {
			t.Run(firstTool+" then "+nextTool, func(t *testing.T) {
				p := NewProtocol(llm.ProtocolOpts{StructuredCompletion: true})
				p.SetThreadIDForTest("thread-1")
				args := func(tool string) string {
					if tool == "ask_user" {
						return validQuestion
					}
					return `{"status":"success"}`
				}
				first, err := p.ParseLine(dynamicRequest(t, 91, firstTool, args(firstTool), "thread-1"))
				if err != nil || len(first) != 1 {
					t.Fatalf("first request: %+v %v", first, err)
				}
				writer := &responseBoundaryWriter{onWrite: func() {
					messages, err := p.ParseLine(dynamicRequest(t, 92, nextTool, args(nextTool), "thread-1"))
					if err != nil || len(messages) != 1 {
						t.Fatalf("immediate next request rejected: %+v %v", messages, err)
					}
				}}
				p.SetStdin(writer)
				if firstTool == "ask_user" {
					err = p.RespondToAskUser("91", nil, map[string]string{"Which scope?": "Source"}, nil)
				} else {
					err = p.RespondToCompletion("91", false, "Revise artifacts")
				}
				if err != nil {
					t.Fatal(err)
				}
				if nextTool == "ask_user" {
					if _, ok := p.pendingQuestions["92"]; !ok {
						t.Fatal("new pending question erased after response")
					}
				} else if p.pendingCompletion != "92" {
					t.Fatal("new pending completion erased after response")
				}
			})
		}
	}
}

func TestResponseWriteFailureRestoresPendingRequest(t *testing.T) {
	for _, tool := range []string{"ask_user", "complete_phase"} {
		t.Run(tool, func(t *testing.T) {
			p := NewProtocol(llm.ProtocolOpts{StructuredCompletion: true})
			p.SetThreadIDForTest("thread-1")
			args := validQuestion
			if tool == "complete_phase" {
				args = `{"status":"success"}`
			}
			messages, err := p.ParseLine(dynamicRequest(t, 93, tool, args, "thread-1"))
			if err != nil || len(messages) != 1 {
				t.Fatalf("request: %+v %v", messages, err)
			}
			p.SetStdin(&responseBoundaryWriter{err: errors.New("broken pipe")})
			if tool == "ask_user" {
				err = p.RespondToAskUser("93", nil, map[string]string{"Which scope?": "Source"}, nil)
			} else {
				err = p.RespondToCompletion("93", false, "Revise artifacts")
			}
			if err == nil {
				t.Fatal("write error ignored")
			}
			if tool == "ask_user" {
				if _, ok := p.pendingQuestions["93"]; !ok {
					t.Fatal("failed answer lost pending state")
				}
			} else if p.pendingCompletion != "93" {
				t.Fatal("failed completion response lost pending state")
			}
		})
	}
}
