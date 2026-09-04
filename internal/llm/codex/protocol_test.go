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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// Compile-time check that *Protocol satisfies llm.Protocol.
var _ llm.Protocol = (*Protocol)(nil)

func TestCodexProtocol_SessionIDAndTranscriptPath(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test"})

	if got := p.SessionID(); got != "" {
		t.Errorf("SessionID() = %q, want empty before thread start", got)
	}
	if got := p.TranscriptPath(); got != "" {
		t.Errorf("TranscriptPath() = %q, want empty", got)
	}

	if _, err := p.ParseLine([]byte(`{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"thread-abc"}}}`)); err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if got := p.SessionID(); got != "thread-abc" {
		t.Errorf("SessionID() = %q, want thread-abc after thread start", got)
	}
}

func TestCodexProtocol_StartThread_FreshVsResume(t *testing.T) {
	t.Run("fresh_sends_thread_start", func(t *testing.T) {
		p := NewProtocol(llm.ProtocolOpts{WorkDir: "/w", Model: "test-model"})
		var buf bytes.Buffer
		p.SetStdin(&buf)
		if err := p.startThread(); err != nil {
			t.Fatalf("startThread error: %v", err)
		}
		var req struct {
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		if err := json.Unmarshal(buf.Bytes(), &req); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		if req.Method != "thread/start" {
			t.Errorf("method = %q, want thread/start", req.Method)
		}
	})

	t.Run("resume_sends_thread_resume_with_thread_id", func(t *testing.T) {
		p := NewProtocol(llm.ProtocolOpts{WorkDir: "/w", Model: "test-model", ResumeSessionID: "thread-abc"})
		var buf bytes.Buffer
		p.SetStdin(&buf)
		if err := p.startThread(); err != nil {
			t.Fatalf("startThread error: %v", err)
		}
		var req struct {
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		if err := json.Unmarshal(buf.Bytes(), &req); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		if req.Method != "thread/resume" {
			t.Errorf("method = %q, want thread/resume", req.Method)
		}
		if got := req.Params["threadId"]; got != "thread-abc" {
			t.Errorf("params.threadId = %v, want thread-abc", got)
		}
	})
}

func TestCodexProtocol_NativeToollessReviewConfiguresOneEphemeralTurn(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{
		Model:                "gpt-5.4-mini[400K]",
		WorkDir:              "/workspace",
		InitialPrompt:        "classify",
		NativeToollessReview: true,
	})
	var buf bytes.Buffer
	p.SetStdin(&buf)
	if err := p.startThread(); err != nil {
		t.Fatalf("startThread() error: %v", err)
	}

	var threadReq struct {
		Method string            `json:"method"`
		Params ThreadStartParams `json:"params"`
	}
	if err := json.Unmarshal(buf.Bytes(), &threadReq); err != nil {
		t.Fatalf("unmarshal thread/start: %v", err)
	}
	if threadReq.Method != "thread/start" {
		t.Fatalf("method = %q, want thread/start", threadReq.Method)
	}
	if threadReq.Params.Model != "gpt-5.4-mini" {
		t.Errorf("thread model = %q, want canonical model", threadReq.Params.Model)
	}
	if !threadReq.Params.Ephemeral {
		t.Error("thread ephemeral = false")
	}
	if threadReq.Params.Sandbox == nil || *threadReq.Params.Sandbox != SandboxModeReadOnly {
		t.Errorf("thread sandbox = %v, want read-only", threadReq.Params.Sandbox)
	}
	if threadReq.Params.ApprovalPolicy != "on-request" {
		t.Errorf("thread approvalPolicy = %q, want on-request", threadReq.Params.ApprovalPolicy)
	}
	if threadReq.Params.Environments == nil || len(*threadReq.Params.Environments) != 0 {
		t.Errorf("thread environments = %v, want explicit empty list", threadReq.Params.Environments)
	}
	if threadReq.Params.DynamicTools == nil || len(*threadReq.Params.DynamicTools) != 0 {
		t.Errorf("thread dynamicTools = %v, want explicit empty list", threadReq.Params.DynamicTools)
	}
	if threadReq.Params.SelectedCapabilityRoots == nil || len(*threadReq.Params.SelectedCapabilityRoots) != 0 {
		t.Errorf("thread selectedCapabilityRoots = %v, want explicit empty list", threadReq.Params.SelectedCapabilityRoots)
	}

	buf.Reset()
	p.SetThreadIDForTest("thread-review")
	if err := p.startTurn("classify"); err != nil {
		t.Fatalf("startTurn() error: %v", err)
	}
	var turnReq struct {
		Method string          `json:"method"`
		Params TurnStartParams `json:"params"`
	}
	if err := json.Unmarshal(buf.Bytes(), &turnReq); err != nil {
		t.Fatalf("unmarshal turn/start: %v", err)
	}
	if turnReq.Method != "turn/start" {
		t.Fatalf("method = %q, want turn/start", turnReq.Method)
	}
	if turnReq.Params.Model != "gpt-5.4-mini" || turnReq.Params.Effort != "low" {
		t.Errorf("turn model/effort = %q/%q, want gpt-5.4-mini/low", turnReq.Params.Model, turnReq.Params.Effort)
	}
	if turnReq.Params.ApprovalPolicy != "on-request" {
		t.Errorf("turn approvalPolicy = %q, want on-request", turnReq.Params.ApprovalPolicy)
	}
	if turnReq.Params.SandboxPolicy == nil || turnReq.Params.SandboxPolicy.Type != "readOnly" ||
		turnReq.Params.SandboxPolicy.NetworkAccess {
		t.Errorf("turn sandboxPolicy = %+v, want readOnly without network", turnReq.Params.SandboxPolicy)
	}
	if turnReq.Params.CollaborationMode != nil {
		t.Errorf("turn collaborationMode = %+v, want absent", turnReq.Params.CollaborationMode)
	}

	buf.Reset()
	if err := p.SendUserMessage("continue"); err == nil {
		t.Fatal("SendUserMessage() error = nil in native tool-less review")
	}
	if buf.Len() != 0 {
		t.Fatalf("SendUserMessage() wrote a second turn: %s", buf.String())
	}
}

func TestCodexProtocol_NativeToollessHandshakeResponsesAreHandledWithoutActivity(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{NativeToollessReview: true})
	p.handshakeDone = make(chan struct{})
	p.threadReady = make(chan struct{})

	messages, err := p.ParseLine([]byte(`{"id":1,"result":{"userAgent":"codex-cli/0.144.5","codexHome":"/tmp/codex"}}`))
	if err != nil {
		t.Fatalf("ParseLine(initialize response) error: %v", err)
	}
	if len(messages) != 0 || p.nativeReviewFailed {
		t.Fatalf("initialize response = %+v, failed=%t; want handled handshake activity", messages, p.nativeReviewFailed)
	}
	select {
	case <-p.handshakeDone:
	default:
		t.Fatal("initialize response did not complete handshake")
	}

	for _, method := range []string{
		"configWarning",
		"deprecationNotice",
		"warning",
		"remoteControl/status/changed",
	} {
		messages, err = p.ParseLine([]byte(`{"method":"` + method + `","params":{}}`))
		if err != nil {
			t.Fatalf("ParseLine(%s) error: %v", method, err)
		}
		if len(messages) != 0 || p.nativeReviewFailed {
			t.Fatalf("%s = %+v, failed=%t; want ignored diagnostic notification", method, messages, p.nativeReviewFailed)
		}
	}

	messages, err = p.ParseLine([]byte(`{"id":2,"result":{"thread":{"id":"thread-review"},"approvalPolicy":"never"}}`))
	if err != nil {
		t.Fatalf("ParseLine(thread response) error: %v", err)
	}
	if len(messages) != 0 || p.nativeReviewFailed {
		t.Fatalf("thread response = %+v, failed=%t; want handled handshake activity", messages, p.nativeReviewFailed)
	}
	select {
	case <-p.threadReady:
	default:
		t.Fatal("thread response did not complete thread start")
	}
}

func TestCodexProtocol_NativeToollessReviewExactDecisions(t *testing.T) {
	for _, decision := range []string{"ALLOW", "DEFER"} {
		t.Run(decision, func(t *testing.T) {
			p := NewProtocol(llm.ProtocolOpts{NativeToollessReview: true})
			p.SetThreadIDForTest("thread-review")
			msgs, err := p.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"inProgress","items":[]}}}`))
			if err != nil {
				t.Fatalf("ParseLine(turn started) error: %v", err)
			}
			if len(msgs) != 0 {
				t.Fatalf("turn started = %+v, want no messages", msgs)
			}

			line := []byte(`{"method":"item/completed","params":{"threadId":"thread-review","turnId":"turn-1","item":{"id":"item-1","type":"agentMessage","text":"` + decision + `","phase":"final_answer"}}}`)
			msgs, err = p.ParseLine(line)
			if err != nil {
				t.Fatalf("ParseLine(agent message) error: %v", err)
			}
			if len(msgs) != 1 || msgs[0].Assistant == nil ||
				msgs[0].Assistant.Message.Content[0].Text != decision {
				t.Fatalf("agent message = %+v, want exact %s", msgs, decision)
			}

			msgs, err = p.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"completed"}}}`))
			if err != nil {
				t.Fatalf("ParseLine(turn completed) error: %v", err)
			}
			if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsSuccess() {
				t.Fatalf("turn completion = %+v, want success", msgs)
			}
		})
	}
}

func TestCodexProtocol_NativeToollessReviewFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"command approval", `{"id":7,"method":"item/commandExecution/requestApproval","params":{"command":"pwd"}}`},
		{"file approval", `{"id":8,"method":"item/fileChange/requestApproval","params":{"grantRoot":"/tmp"}}`},
		{"question", `{"id":9,"method":"tool/requestUserInput","params":{"questions":[]}}`},
		{"unknown request", `{"id":10,"method":"mcpServer/elicitation/request","params":{}}`},
		{"command activity", `{"method":"item/started","params":{"threadId":"thread-review","item":{"id":"i","type":"commandExecution"}}}`},
		{"file activity", `{"method":"item/started","params":{"threadId":"thread-review","item":{"id":"i","type":"fileChange"}}}`},
		{"web activity", `{"method":"item/started","params":{"threadId":"thread-review","item":{"id":"i","type":"webSearch"}}}`},
		{"MCP activity", `{"method":"item/started","params":{"threadId":"thread-review","item":{"id":"i","type":"mcpToolCall"}}}`},
		{"dynamic tool activity", `{"method":"item/started","params":{"threadId":"thread-review","item":{"id":"i","type":"dynamicToolCall"}}}`},
		{"child agent activity", `{"method":"item/started","params":{"threadId":"thread-review","item":{"id":"i","type":"collabAgentToolCall"}}}`},
		{"provider error", `{"method":"error","params":{"error":{"message":"provider failed"}}}`},
		{"malformed decision", `{"method":"turn/completed","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"completed"}}}`},
		{"refusal", `{"method":"item/completed","params":{"threadId":"thread-review","item":{"id":"i","type":"agentMessage","text":"I cannot comply"}}}`},
		{"truncation", `{"method":"item/completed","params":{"threadId":"thread-review","item":{"id":"i","type":"contextCompaction"}}}`},
		{"cancellation", `{"method":"turn/completed","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"interrupted"}}}`},
		{"malformed JSON", `{`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProtocol(llm.ProtocolOpts{NativeToollessReview: true})
			p.SetThreadIDForTest("thread-review")
			msgs, err := p.ParseLine([]byte(tt.line))
			if err != nil {
				return
			}
			if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
				t.Fatalf("ParseLine() = %+v, want result/error or parse error", msgs)
			}
		})
	}
}

func TestCodexProtocol_NormalizesRootAndDelegatedTaskActivity(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{})
	p.SetThreadIDForTest("thread-root")

	root, err := p.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"thread-root","turnId":"turn-root","item":{"id":"message-1","type":"agentMessage","text":"working"}}}`))
	if err != nil {
		t.Fatalf("ParseLine(root): %v", err)
	}
	if len(root) != 1 || root[0].Assistant == nil || !root[0].Origin.IsRoot() {
		t.Fatalf("root messages = %+v", root)
	}

	started, err := p.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"thread-root","turnId":"turn-root","item":{"id":"collab-1","type":"collabAgentToolCall","tool":"spawn_agent","status":"inProgress","receiverThreadIds":["thread-child"],"prompt":"Inspect the server package."}}}`))
	if err != nil {
		t.Fatalf("ParseLine(task start): %v", err)
	}
	if len(started) != 1 ||
		started[0].TaskStarted == nil ||
		started[0].Origin.Kind != llm.EventOriginTask ||
		started[0].Origin.TaskID != "collab-1" ||
		started[0].Origin.ChildSessionID != "thread-child" {
		t.Fatalf("task start = %+v", started)
	}

	progress, err := p.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"thread-child","turnId":"turn-child","item":{"id":"cmd-1","type":"commandExecution","command":"go test ./..."}}}`))
	if err != nil {
		t.Fatalf("ParseLine(child progress): %v", err)
	}
	if len(progress) != 1 ||
		progress[0].TaskProgress == nil ||
		progress[0].TaskProgress.LastToolName != "Bash" ||
		progress[0].Origin.TaskID != "collab-1" ||
		progress[0].Origin.ChildSessionID != "thread-child" {
		t.Fatalf("child progress = %+v", progress)
	}

	completed, err := p.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"thread-root","turnId":"turn-root","item":{"id":"collab-1","type":"collabAgentToolCall","tool":"spawn_agent","status":"completed","receiverThreadIds":["thread-child"]}}}`))
	if err != nil {
		t.Fatalf("ParseLine(task completed): %v", err)
	}
	if len(completed) != 1 ||
		completed[0].TaskNotification == nil ||
		completed[0].TaskNotification.Status != "completed" ||
		completed[0].Origin.TaskID != "collab-1" {
		t.Fatalf("task completed = %+v", completed)
	}
}

func TestCodexProtocol_IgnoresUnboundDelegatedTaskStart(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{})
	p.SetThreadIDForTest("thread-root")

	msgs, err := p.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"thread-root","turnId":"turn-root","item":{"id":"collab-abandoned","type":"collabAgentToolCall","tool":"spawnAgent","status":"inProgress","receiverThreadIds":[],"prompt":"Inspect the server package."}}}`))
	if err != nil {
		t.Fatalf("ParseLine(task start): %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("unbound delegated task messages = %+v, want no running task before Codex assigns a child thread", msgs)
	}
}

func TestCodexProtocol_IgnoresUncorrelatedChildThreadActivity(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{})
	p.SetThreadIDForTest("thread-root")

	msgs, err := p.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"thread-unknown","turnId":"turn-child","item":{"id":"cmd-1","type":"commandExecution","command":"go test ./..."}}}`))
	if err != nil {
		t.Fatalf("ParseLine(child): %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("uncorrelated child messages = %+v, want no invented task identity", msgs)
	}
}

func TestCodexProtocol_NativeToollessReviewViolationIsTerminal(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"off-main agent delta", `{"method":"item/agentMessage/delta","params":{"threadId":"thread-child","turnId":"turn-1","itemId":"i","delta":"ALLOW"}}`},
		{"off-main turn completed", `{"method":"turn/completed","params":{"threadId":"thread-child","turn":{"id":"turn-child","status":"completed"}}}`},
		{"off-main item completed", `{"method":"item/completed","params":{"threadId":"thread-child","turnId":"turn-child","item":{"id":"i","type":"agentMessage","text":"ALLOW"}}}`},
		{"off-main item started", `{"method":"item/started","params":{"threadId":"thread-child","turnId":"turn-child","item":{"id":"i","type":"agentMessage"}}}`},
		{"off-main token usage", `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-child","turnId":"turn-child","tokenUsage":{"total":{},"last":{}}}}`},
		{"off-main reasoning delta", `{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"thread-child","turnId":"turn-child","itemId":"i","delta":"thinking"}}`},
		{"command output delta", `{"method":"item/commandExecution/outputDelta","params":{"threadId":"thread-review","turnId":"turn-1","itemId":"i","delta":"output"}}`},
		{"file output delta", `{"method":"item/fileChange/outputDelta","params":{"threadId":"thread-review","turnId":"turn-1","itemId":"i","delta":"patch"}}`},
		{"child thread started", `{"method":"thread/started","params":{"thread":{"id":"thread-child"}}}`},
		{"child thread status", `{"method":"thread/status/changed","params":{"threadId":"thread-child","status":"active"}}`},
		{"malformed agent delta", `{"method":"item/agentMessage/delta","params":[]}`},
		{"malformed turn completed", `{"method":"turn/completed","params":[]}`},
		{"malformed item completed", `{"method":"item/completed","params":[]}`},
		{"malformed item started", `{"method":"item/started","params":[]}`},
		{"malformed turn started", `{"method":"turn/started","params":[]}`},
		{"malformed thread started", `{"method":"thread/started","params":[]}`},
		{"malformed thread status", `{"method":"thread/status/changed","params":[]}`},
		{"malformed token usage", `{"method":"thread/tokenUsage/updated","params":[]}`},
		{"malformed command output", `{"method":"item/commandExecution/outputDelta","params":[]}`},
		{"malformed file output", `{"method":"item/fileChange/outputDelta","params":[]}`},
		{"malformed reasoning delta", `{"method":"item/reasoning/summaryTextDelta","params":[]}`},
		{"malformed rate limits", `{"method":"account/rateLimits/updated","params":[]}`},
		{"malformed error", `{"method":"error","params":[]}`},
		{"JSON-RPC error", `{"id":42,"error":{"code":-32603,"message":"provider failed"}}`},
		{"empty agent output", `{"method":"item/completed","params":{"threadId":"thread-review","turnId":"turn-1","item":{"id":"i","type":"agentMessage","text":""}}}`},
		{"failed turn", `{"method":"turn/completed","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"failed"}}}`},
		{"interrupted turn", `{"method":"turn/completed","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"interrupted"}}}`},
		{"unknown turn status", `{"method":"turn/completed","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"mystery"}}}`},
		{"unrecognized envelope", `{}`},
		{"unrecognized response", `{"id":42,"result":{"unexpected":true}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProtocol(llm.ProtocolOpts{NativeToollessReview: true})
			p.SetThreadIDForTest("thread-review")
			msgs, err := p.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"inProgress","items":[]}}}`))
			if err != nil {
				t.Fatalf("ParseLine(turn started) error: %v", err)
			}
			if len(msgs) != 0 {
				t.Fatalf("ParseLine(turn started) = %+v, want no messages", msgs)
			}

			msgs, err = p.ParseLine([]byte(tt.line))
			if err != nil {
				t.Fatalf("ParseLine(violation) error: %v", err)
			}
			if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
				t.Fatalf("ParseLine(violation) = %+v, want terminal result/error", msgs)
			}

			msgs, err = p.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"thread-review","turnId":"turn-1","item":{"id":"item-1","type":"agentMessage","text":"ALLOW","phase":"final_answer"}}}`))
			if err != nil {
				t.Fatalf("ParseLine(later ALLOW) error: %v", err)
			}
			if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
				t.Fatalf("ParseLine(later ALLOW) = %+v, want attempt to remain failed", msgs)
			}
		})
	}
}

func TestCodexProtocol_NativeToollessReviewRejectsInvalidTurnOrdering(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"agent delta before turn", `{"method":"item/agentMessage/delta","params":{"threadId":"thread-review","turnId":"turn-1","itemId":"i","delta":"ALLOW"}}`},
		{"assistant completion before turn", `{"method":"item/completed","params":{"threadId":"thread-review","turnId":"turn-1","item":{"id":"i","type":"agentMessage","text":"ALLOW"}}}`},
		{"turn completion before turn", `{"method":"turn/completed","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"completed"}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProtocol(llm.ProtocolOpts{NativeToollessReview: true})
			p.SetThreadIDForTest("thread-review")

			msgs, err := p.ParseLine([]byte(tt.line))
			if err != nil {
				t.Fatalf("ParseLine(violation) error: %v", err)
			}
			if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
				t.Fatalf("ParseLine(violation) = %+v, want terminal result/error", msgs)
			}

			msgs, err = p.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"inProgress","items":[]}}}`))
			if err != nil {
				t.Fatalf("ParseLine(later turn started) error: %v", err)
			}
			if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
				t.Fatalf("ParseLine(later turn started) = %+v, want attempt to remain failed", msgs)
			}
		})
	}
}

func TestCodexProtocol_NativeToollessReviewRejectsMismatchedTurnActivity(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"agent delta missing turn ID", `{"method":"item/agentMessage/delta","params":{"threadId":"thread-review","itemId":"i","delta":"ALLOW"}}`},
		{"agent delta from another turn", `{"method":"item/agentMessage/delta","params":{"threadId":"thread-review","turnId":"turn-other","itemId":"i","delta":"ALLOW"}}`},
		{"assistant completion missing turn ID", `{"method":"item/completed","params":{"threadId":"thread-review","item":{"id":"i","type":"agentMessage","text":"ALLOW"}}}`},
		{"assistant completion from another turn", `{"method":"item/completed","params":{"threadId":"thread-review","turnId":"turn-other","item":{"id":"i","type":"agentMessage","text":"ALLOW"}}}`},
		{"completion from another turn", `{"method":"turn/completed","params":{"threadId":"thread-review","turn":{"id":"turn-other","status":"completed"}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProtocol(llm.ProtocolOpts{NativeToollessReview: true})
			p.SetThreadIDForTest("thread-review")
			msgs, err := p.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"inProgress","items":[]}}}`))
			if err != nil {
				t.Fatalf("ParseLine(turn started) error: %v", err)
			}
			if len(msgs) != 0 {
				t.Fatalf("ParseLine(turn started) = %+v, want no messages", msgs)
			}

			msgs, err = p.ParseLine([]byte(tt.line))
			if err != nil {
				t.Fatalf("ParseLine(violation) error: %v", err)
			}
			if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
				t.Fatalf("ParseLine(violation) = %+v, want terminal result/error", msgs)
			}

			msgs, err = p.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"thread-review","turnId":"turn-1","item":{"id":"item-1","type":"agentMessage","text":"ALLOW","phase":"final_answer"}}}`))
			if err != nil {
				t.Fatalf("ParseLine(later ALLOW) error: %v", err)
			}
			if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
				t.Fatalf("ParseLine(later ALLOW) = %+v, want attempt to remain failed", msgs)
			}
		})
	}
}

func TestCodexProtocol_NativeToollessReviewRejectsDuplicateAssistantCompletion(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{NativeToollessReview: true})
	p.SetThreadIDForTest("thread-review")
	lines := []string{
		`{"method":"turn/started","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"inProgress","items":[]}}}`,
		`{"method":"item/completed","params":{"threadId":"thread-review","turnId":"turn-1","item":{"id":"item-1","type":"agentMessage","text":"DEFER","phase":"final_answer"}}}`,
	}
	for _, line := range lines {
		if _, err := p.ParseLine([]byte(line)); err != nil {
			t.Fatalf("ParseLine(%s) error: %v", line, err)
		}
	}

	msgs, err := p.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"thread-review","turnId":"turn-1","item":{"id":"item-2","type":"agentMessage","text":"ALLOW","phase":"final_answer"}}}`))
	if err != nil {
		t.Fatalf("ParseLine(second assistant completion) error: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
		t.Fatalf("ParseLine(second assistant completion) = %+v, want terminal result/error", msgs)
	}

	msgs, err = p.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"completed"}}}`))
	if err != nil {
		t.Fatalf("ParseLine(later turn completion) error: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
		t.Fatalf("ParseLine(later turn completion) = %+v, want attempt to remain failed", msgs)
	}
}

func TestCodexProtocol_NativeToollessReviewMalformedJSONIsTerminal(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{NativeToollessReview: true})
	p.SetThreadIDForTest("thread-review")

	if _, err := p.ParseLine([]byte(`{`)); err == nil {
		t.Fatal("ParseLine(malformed JSON) error = nil, want parse error")
	}

	msgs, err := p.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"thread-review","turnId":"turn-1","item":{"id":"item-1","type":"agentMessage","text":"ALLOW","phase":"final_answer"}}}`))
	if err != nil {
		t.Fatalf("ParseLine(later ALLOW) error: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
		t.Fatalf("ParseLine(later ALLOW) = %+v, want attempt to remain failed", msgs)
	}
}

func TestCodexProtocol_NativeToollessReviewRejectsExtraTurn(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{NativeToollessReview: true})
	p.SetThreadIDForTest("thread-review")
	line := []byte(`{"method":"turn/started","params":{"threadId":"thread-review","turn":{"id":"turn-1","status":"inProgress","items":[]}}}`)
	if msgs, err := p.ParseLine(line); err != nil || len(msgs) != 0 {
		t.Fatalf("first turn/started = (%+v, %v), want no message", msgs, err)
	}
	msgs, err := p.ParseLine(line)
	if err != nil {
		t.Fatalf("second turn/started error: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
		t.Fatalf("second turn/started = %+v, want result/error", msgs)
	}
}

func TestCodexProtocol_Interrupt_ReturnsNotSupported(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test"})
	err := p.Interrupt()
	if !errors.Is(err, llm.ErrNotSupported) {
		t.Errorf("Interrupt() = %v, want llm.ErrNotSupported", err)
	}
}

func TestCodexProtocol_TokenUsageUpdated(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})

	ctxWindow := 200000
	notification := map[string]interface{}{
		"method": "thread/tokenUsage/updated",
		"params": map[string]interface{}{
			"threadId": "t1",
			"turnId":   "turn1",
			"tokenUsage": map[string]interface{}{
				"total": map[string]interface{}{
					"inputTokens":       150000,
					"cachedInputTokens": 20000,
					"outputTokens":      5000,
					"totalTokens":       155000,
				},
				"last": map[string]interface{}{
					"inputTokens":       80000,
					"cachedInputTokens": 10000,
					"outputTokens":      2000,
					"totalTokens":       82000,
				},
				"modelContextWindow": ctxWindow,
			},
		},
	}
	line, _ := json.Marshal(notification)
	msgs, err := p.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}

	msg := msgs[0]
	if msg.Type != "usage_update" {
		t.Fatalf("type=%q, want usage_update", msg.Type)
	}
	if msg.UsageUpdate == nil {
		t.Fatal("expected UsageUpdate on message")
	}

	u := msg.UsageUpdate
	// UsageUpdate carries Total (cumulative) tokens for cost tracking
	if u.InputTokens != 150000 {
		t.Errorf("InputTokens = %d, want 150000 (Total.InputTokens)", u.InputTokens)
	}
	if u.OutputTokens != 5000 {
		t.Errorf("OutputTokens = %d, want 5000 (Total.OutputTokens)", u.OutputTokens)
	}
	if u.CacheReadInputTokens != 20000 {
		t.Errorf("CacheReadInputTokens = %d, want 20000 (Total.CachedInputTokens)", u.CacheReadInputTokens)
	}
	if u.CacheCreationInputTokens != 0 {
		t.Errorf("CacheCreationInputTokens = %d, want 0", u.CacheCreationInputTokens)
	}
	// ContextInputTokens carries Last.InputTokens (informational)
	if u.ContextInputTokens != 80000 {
		t.Errorf("ContextInputTokens = %d, want 80000 (Last.InputTokens)", u.ContextInputTokens)
	}
	// ContextTotalTokens carries Last.TotalTokens (used by ContextPercentage)
	if u.ContextTotalTokens != 82000 {
		t.Errorf("ContextTotalTokens = %d, want 82000 (Last.TotalTokens)", u.ContextTotalTokens)
	}
	// ContextBaseline mirrors Codex's TokenUsage::BASELINE_TOKENS = 12000
	if u.ContextBaseline != codexContextBaselineTokens {
		t.Errorf("ContextBaseline = %d, want %d", u.ContextBaseline, codexContextBaselineTokens)
	}
	if u.ContextWindow != ctxWindow {
		t.Errorf("ContextWindow = %d, want %d", u.ContextWindow, ctxWindow)
	}

	// Verify internal state uses Total for cost tracking
	p.mu.Lock()
	if p.inputTokens != 150000 {
		t.Errorf("p.inputTokens = %d, want 150000 (Total.InputTokens)", p.inputTokens)
	}
	if p.outputTokens != 5000 {
		t.Errorf("p.outputTokens = %d, want 5000 (Total.OutputTokens)", p.outputTokens)
	}
	if p.cachedInputTokens != 20000 {
		t.Errorf("p.cachedInputTokens = %d, want 20000 (Total.CachedInputTokens)", p.cachedInputTokens)
	}
	if p.modelContextWindow != ctxWindow {
		t.Errorf("p.modelContextWindow = %d, want %d", p.modelContextWindow, ctxWindow)
	}
	p.mu.Unlock()
}

func TestCodexCommandExecutionCompletedIncludesStructuredFileReads(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	exitCode := 0
	params, err := json.Marshal(ItemCompletedParams{
		ThreadID: "thread-1",
		TurnID:   "turn-1",
		Item: ItemUnion{
			ID:               "call_read",
			Type:             "commandExecution",
			AggregatedOutput: "file contents",
			ExitCode:         &exitCode,
			CommandActions: []CommandAction{
				{Type: "read", Path: "/tmp/state/guidelines/go/index.md"},
				{Type: codexFileChangeOperationWrite, Path: "/tmp/state/output.txt"},
				{Type: "read", Path: ""},
			},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	msg, ok := p.parseNotification("item/completed", params)
	if !ok {
		t.Fatal("parseNotification returned false, want true")
	}
	if msg.ToolProgress == nil {
		t.Fatal("msg.ToolProgress is nil, want non-nil")
	}
	if msg.ToolProgress.ToolUseID != "call_read" {
		t.Fatalf("ToolUseID = %q, want %q", msg.ToolProgress.ToolUseID, "call_read")
	}
	if len(msg.FileReads) != 1 {
		t.Fatalf("len(FileReads) = %d, want 1", len(msg.FileReads))
	}
	read := msg.FileReads[0]
	if read.FilePath != "/tmp/state/guidelines/go/index.md" {
		t.Errorf("FilePath = %q", read.FilePath)
	}
	if read.Source != "codex.command_action" {
		t.Errorf("Source = %q", read.Source)
	}
	if read.ProviderItemID != "call_read" {
		t.Errorf("ProviderItemID = %q", read.ProviderItemID)
	}
	if read.ExitCode == nil || *read.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", read.ExitCode)
	}

	dup, ok := p.parseNotification("item/completed", params)
	if !ok {
		t.Fatal("duplicate parseNotification returned false, want true")
	}
	if len(dup.FileReads) != 0 {
		t.Fatalf("duplicate len(FileReads) = %d, want 0", len(dup.FileReads))
	}
}

func TestCodexFileChangeCompletedIncludesStructuredDiff(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})

	params := json.RawMessage(`{
		"threadId": "thread-1",
		"turnId": "turn-1",
		"item": {
			"id": "call_write",
			"type": "fileChange",
			"status": "completed",
			"changes": [{
				"path": "/tmp/test/README.md",
				"kind": {"type": "update", "move_path": null},
				"diff": "@@ -1,2 +1,2 @@\n-old\n+new\n"
			}]
		}
	}`)

	msg, ok := p.parseNotification("item/completed", params)
	if !ok {
		t.Fatal("parseNotification(item/completed fileChange) returned false, want true")
	}
	if msg.ToolProgress == nil {
		t.Fatal("msg.ToolProgress is nil, want non-nil")
	}
	if msg.ToolProgress.ToolUseID != "call_write" || msg.ToolProgress.ToolName != codexToolNameWrite {
		t.Fatalf("ToolProgress = %+v, want call_write Write", msg.ToolProgress)
	}
	if len(msg.FileChanges) != 1 {
		t.Fatalf("len(FileChanges) = %d, want 1", len(msg.FileChanges))
	}
	change := msg.FileChanges[0]
	if change.Path != "/tmp/test/README.md" {
		t.Fatalf("FileChanges[0].Path = %q", change.Path)
	}
	if change.Operation != codexFileChangeOperationUpdate {
		t.Fatalf("FileChanges[0].Operation = %q, want update", change.Operation)
	}
	if !change.HasDiffPatch {
		t.Fatal("FileChanges[0].HasDiffPatch = false, want true")
	}
	if change.AddedLines != 1 || change.RemovedLines != 1 {
		t.Fatalf("line counts = +%d -%d, want +1 -1", change.AddedLines, change.RemovedLines)
	}
	if !strings.Contains(change.Detail, "-old") || !strings.Contains(change.Detail, "+new") {
		t.Fatalf("FileChanges[0].Detail missing diff lines: %q", change.Detail)
	}
}

func TestCodexToolProgressIncludesProviderItemID(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})

	startedParams, err := json.Marshal(ItemStartedParams{
		ThreadID: "thread-1",
		TurnID:   "turn-1",
		Item: ItemUnion{
			ID:   "call_1",
			Type: "commandExecution",
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal started: %v", err)
	}
	started, ok := p.parseNotification("item/started", startedParams)
	if !ok {
		t.Fatal("parseNotification(item/started) returned false, want true")
	}
	if started.ToolProgress == nil {
		t.Fatal("started.ToolProgress is nil, want non-nil")
	}
	if started.ToolProgress.ToolUseID != "call_1" || started.ToolProgress.ToolName != "Bash" {
		t.Fatalf("started ToolProgress = %+v, want ToolUseID call_1 and Bash", started.ToolProgress)
	}

	deltaParams, err := json.Marshal(CommandOutputDelta{
		ThreadID: "thread-1",
		TurnID:   "turn-1",
		ItemID:   "call_1",
		Delta:    "PASS",
	})
	if err != nil {
		t.Fatalf("json.Marshal delta: %v", err)
	}
	delta, ok := p.parseNotification("item/commandExecution/outputDelta", deltaParams)
	if !ok {
		t.Fatal("parseNotification(outputDelta) returned false, want true")
	}
	if delta.ToolProgress == nil {
		t.Fatal("delta.ToolProgress is nil, want non-nil")
	}
	if delta.ToolProgress.ToolUseID != "call_1" || delta.ToolProgress.ToolName != "Bash" {
		t.Fatalf("delta ToolProgress = %+v, want ToolUseID call_1 and Bash", delta.ToolProgress)
	}
}

func TestCodexProtocolStartTurn_DeveloperInstructionsEncoding(t *testing.T) {
	tests := []struct {
		name         string
		systemPrompt string
		wantField    bool
	}{
		{
			name:      "omits empty developer instructions",
			wantField: false,
		},
		{
			name:         "includes non-empty developer instructions",
			systemPrompt: "Follow the system prompt",
			wantField:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			p := NewProtocol(llm.ProtocolOpts{
				Model:        "gpt-5.4",
				WorkDir:      "/tmp/test",
				SystemPrompt: tt.systemPrompt,
			})
			p.SetStdin(&buf)
			p.SetThreadIDForTest("thread-123")

			if err := p.startTurn("review this plan"); err != nil {
				t.Fatalf("startTurn() error: %v", err)
			}

			got := buf.String()
			hasField := strings.Contains(got, `"developer_instructions"`)
			if hasField != tt.wantField {
				t.Fatalf("developer_instructions presence = %v, want %v; payload=%s", hasField, tt.wantField, got)
			}
		})
	}
}

func TestCodexProtocol_StripsContextWindowFromModel(t *testing.T) {
	var buf bytes.Buffer

	p := NewProtocol(llm.ProtocolOpts{
		Model:   "gpt-5.4[1M]",
		WorkDir: "/tmp/test",
	})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-123")

	if err := p.startTurn("do something"); err != nil {
		t.Fatalf("startTurn() error: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, `"model":"gpt-5.4"`) {
		t.Fatalf("startTurn payload = %s, want base model gpt-5.4", got)
	}
	if strings.Contains(got, "gpt-5.4[1M]") {
		t.Fatalf("startTurn payload leaked context-window model ID: %s", got)
	}
}

func TestTokenUsageUpdatedSurfacesAsSDKMessage(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test"})
	p.SetThreadIDForTest("thread-abc")

	// Build the JSON-RPC notification params matching TokenUsageUpdatedParams.
	payload := TokenUsageUpdatedParams{
		ThreadID: "thread-abc",
		TurnID:   "turn-1",
		TokenUsage: ThreadTokenUsage{
			Total: TokenUsageBreakdown{
				InputTokens:  1500,
				OutputTokens: 750,
			},
		},
	}
	params, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	msg, ok := p.parseNotification("thread/tokenUsage/updated", params)
	if !ok {
		t.Fatal("parseNotification returned false, want true")
	}
	if msg.Type != "usage_update" {
		t.Errorf("msg.Type = %q, want %q", msg.Type, "usage_update")
	}
	if msg.UsageUpdate == nil {
		t.Fatal("msg.UsageUpdate is nil, want non-nil")
	}
	if msg.UsageUpdate.InputTokens != 1500 {
		t.Errorf("UsageUpdate.InputTokens = %d, want 1500", msg.UsageUpdate.InputTokens)
	}
	if msg.UsageUpdate.OutputTokens != 750 {
		t.Errorf("UsageUpdate.OutputTokens = %d, want 750", msg.UsageUpdate.OutputTokens)
	}

	// Verify the protocol's internal cumulative counters were updated.
	p.mu.Lock()
	gotIn := p.inputTokens
	gotOut := p.outputTokens
	p.mu.Unlock()
	if gotIn != 1500 {
		t.Errorf("protocol.inputTokens = %d, want 1500", gotIn)
	}
	if gotOut != 750 {
		t.Errorf("protocol.outputTokens = %d, want 750", gotOut)
	}
}

func TestCodexCommandApproval_NormalizesToBash(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test"})

	params, err := json.Marshal(CommandApprovalParams{Command: "ls -la"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	msg, ok := p.parseServerRequest("item/commandExecution/requestApproval", 42, params)
	if !ok {
		t.Fatal("parseServerRequest() ok = false, want true")
	}
	if msg.Type != "control_request" {
		t.Fatalf("msg.Type = %q, want %q", msg.Type, "control_request")
	}
	if msg.ControlRequest == nil {
		t.Fatal("msg.ControlRequest = nil, want non-nil")
	}
	if msg.ControlRequest.Request.ToolName != "Bash" {
		t.Fatalf("ToolName = %q, want %q", msg.ControlRequest.Request.ToolName, "Bash")
	}

	var payload map[string]string
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &payload); err != nil {
		t.Fatalf("json.Unmarshal(input): %v", err)
	}
	if payload["command"] != "ls -la" {
		t.Errorf("payload[command] = %q, want %q", payload["command"], "ls -la")
	}
}

func TestCodexProtocol_DefaultApprovalPolicyIsOnRequest(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	if p.approvalPolicy != "on-request" {
		t.Errorf("default approvalPolicy = %q, want %q", p.approvalPolicy, "on-request")
	}
}

func TestCodexProtocol_DSPUsesMDMCompatiblePolicyWithDangerFullAccess(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex", DSP: true})
	if p.approvalPolicy != "on-request" {
		t.Errorf("DSP approvalPolicy = %q, want %q", p.approvalPolicy, "on-request")
	}

	var buf bytes.Buffer
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-123")
	if err := p.startTurn("do something"); err != nil {
		t.Fatalf("startTurn() error: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, `"approvalPolicy":"on-request"`) {
		t.Fatalf("DSP turn must use MDM-compatible on-request policy; payload=%s", got)
	}
	if !strings.Contains(got, `"type":"dangerFullAccess"`) {
		t.Fatalf("DSP turn must retain dangerFullAccess sandbox; payload=%s", got)
	}
}

func TestCodexProtocol_AdoptsEffectiveThreadApprovalPolicy(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{
		WorkDir:        "/tmp/test",
		Model:          "codex",
		DSP:            true,
		ApprovalPolicy: "never",
	})
	threadStarted := []byte(`{"id":2,"result":{"thread":{"id":"thread-123"},"approvalPolicy":"on-request","sandbox":{"type":"dangerFullAccess"}}}`)
	if _, err := p.ParseLine(threadStarted); err != nil {
		t.Fatalf("ParseLine(thread/start) error: %v", err)
	}

	var buf bytes.Buffer
	p.SetStdin(&buf)
	if err := p.startTurn("review the plan"); err != nil {
		t.Fatalf("startTurn() error: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, `"approvalPolicy":"never"`) || !strings.Contains(got, `"approvalPolicy":"on-request"`) {
		t.Fatalf("turn must adopt effective thread approval policy; payload=%s", got)
	}
	if !strings.Contains(got, `"type":"dangerFullAccess"`) {
		t.Fatalf("effective policy adoption must not downgrade DSP sandbox; payload=%s", got)
	}

	buf.Reset()
	if err := p.sendFollowUpTurn("continue"); err != nil {
		t.Fatalf("sendFollowUpTurn() error: %v", err)
	}
	got = buf.String()
	if strings.Contains(got, `"approvalPolicy":"never"`) || !strings.Contains(got, `"approvalPolicy":"on-request"`) {
		t.Fatalf("follow-up must retain effective thread approval policy; payload=%s", got)
	}
	if !strings.Contains(got, `"type":"dangerFullAccess"`) {
		t.Fatalf("follow-up must retain DSP sandbox; payload=%s", got)
	}
}

func TestCodexProtocol_StartTurnNetworkAccess(t *testing.T) {
	var buf bytes.Buffer

	p := NewProtocol(llm.ProtocolOpts{
		Model:         "gpt-5.4",
		WorkDir:       "/tmp/test",
		WritableRoots: []string{"/tmp/state"},
	})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-123")

	if err := p.startTurn("do something"); err != nil {
		t.Fatalf("startTurn() error: %v", err)
	}

	got := buf.String()
	// workspaceWrite sandbox should include networkAccess: true
	if !strings.Contains(got, `"networkAccess":true`) {
		t.Fatalf("startTurn payload missing networkAccess:true; payload=%s", got)
	}
	if !strings.Contains(got, `"type":"workspaceWrite"`) {
		t.Fatalf("startTurn payload missing workspaceWrite type; payload=%s", got)
	}
}

func TestCodexProtocol_ErrorResponseSurfacesAsResultError(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex", DSP: true})

	// A JSON-RPC error response to one of our requests (id, no method). Codex
	// returns this for turn/start when an MDM policy forbids approval_policy.
	// It must be surfaced to the user, not silently swallowed.
	line := []byte("{\"id\":3,\"error\":{\"code\":-32600,\"message\":\"invalid thread settings override: `Never` is not in the allowed set [OnRequest, OnFailure] (set by MDM com.openai.codex:requirements_toml_base64)\"}}")

	msgs, err := p.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (error must be surfaced, not swallowed)", len(msgs))
	}
	msg := msgs[0]
	if msg.Type != "result" || msg.Subtype != "error" {
		t.Fatalf("got type=%q subtype=%q, want result/error", msg.Type, msg.Subtype)
	}
	if msg.Result == nil || !msg.Result.IsError {
		t.Fatalf("expected Result with IsError=true, got %+v", msg.Result)
	}
	if !strings.Contains(msg.Result.Result, "not in the allowed set") {
		t.Fatalf("surfaced error should include codex's reason; got %q", msg.Result.Result)
	}
}

func TestParseNumberedOptions(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantOK     bool
		wantStem   string
		wantLabels []string
	}{
		{
			name: "three numbered options with descriptions",
			input: `Who should the rewritten README primarily speak to?
1. Internal engineers (Recommended): Focus on practical value. [confidence: 0.82]
2. Existing users: Focus on usage reference. [confidence: 0.41]
3. External readers: Focus on polished positioning. [confidence: 0.18]`,
			wantOK:     true,
			wantStem:   "Who should the rewritten README primarily speak to?",
			wantLabels: []string{"Internal engineers (Recommended)", "Existing users", "External readers"},
		},
		{
			name:     "free-form sentence with no numbered list",
			input:    "What level of marketing tone do you want: restrained and factual, moderately persuasive, or fairly bold?",
			wantOK:   false,
			wantStem: "",
		},
		{
			name: "only one numbered item is not enough",
			input: `Pick:
1. Only option`,
			wantOK: false,
		},
		{
			name: "bundle of multiple questions as numbered list",
			input: `Tell me:
1. Are we going ahead?
2. Should I include X?
3. Is Y necessary?`,
			wantOK: false,
		},
		{
			name: "continuation lines fold into previous option",
			input: `Question?
1. Short label: first line of
   description continues here.
2. Other label: second option desc.`,
			wantOK:     true,
			wantStem:   "Question?",
			wantLabels: []string{"Short label", "Other label"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stem, opts, ok := parseNumberedOptions(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (stem=%q opts=%+v)", ok, tt.wantOK, stem, opts)
			}
			if !tt.wantOK {
				return
			}
			if stem != tt.wantStem {
				t.Errorf("stem = %q, want %q", stem, tt.wantStem)
			}
			if len(opts) != len(tt.wantLabels) {
				t.Fatalf("opts len = %d, want %d", len(opts), len(tt.wantLabels))
			}
			for i, want := range tt.wantLabels {
				if opts[i].Label != want {
					t.Errorf("opts[%d].Label = %q, want %q", i, opts[i].Label, want)
				}
			}
			if tt.name == "three numbered options with descriptions" {
				if opts[0].Confidence == nil || *opts[0].Confidence != 0.82 {
					t.Fatalf("opts[0].Confidence = %v, want 0.82", opts[0].Confidence)
				}
			}
		})
	}
}

func TestTrimFreeFormSentinel(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantOK     bool
		wantResult string
	}{
		{"prefix only", "FREE_FORM: What version?", true, "What version?"},
		{"with leading whitespace", "  \nFREE_FORM:Name?", true, "Name?"},
		{"no prefix", "What do you want?", false, ""},
		{"prefix in middle ignored", "OK FREE_FORM: not at start", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := trimFreeFormSentinel(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.wantResult {
				t.Errorf("got %q, want %q", got, tt.wantResult)
			}
		})
	}
}

func TestSynthesizeAskUser_IncludesOptions(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test"})
	options := []parsedOption{
		{Label: "Alpha (Recommended)", Description: "first tradeoff", Confidence: floatPtr(0.83)},
		{Label: "Beta", Description: "second tradeoff", Confidence: floatPtr(0.41)},
		{Label: "Gamma", Description: "third tradeoff", Confidence: floatPtr(0.17)},
	}

	msg := p.synthesizeAskUser("Which one?", options)
	if msg.ControlRequest == nil {
		t.Fatal("ControlRequest is nil")
	}
	if msg.ControlRequest.Request.ToolName != "AskUserQuestion" {
		t.Fatalf("ToolName = %q, want AskUserQuestion", msg.ControlRequest.Request.ToolName)
	}

	var parsed struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label       string   `json:"label"`
				Description string   `json:"description"`
				Confidence  *float64 `json:"confidence"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &parsed); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	if len(parsed.Questions) != 1 {
		t.Fatalf("questions len = %d, want 1", len(parsed.Questions))
	}
	q := parsed.Questions[0]
	if q.Question != "Which one?" {
		t.Errorf("question = %q, want %q", q.Question, "Which one?")
	}
	if len(q.Options) != 3 {
		t.Fatalf("options len = %d, want 3", len(q.Options))
	}
	if q.Options[0].Label != "Alpha (Recommended)" || q.Options[0].Description != "first tradeoff" {
		t.Errorf("options[0] = %+v, want Alpha (Recommended)/first tradeoff", q.Options[0])
	}
	if q.Options[0].Confidence == nil || *q.Options[0].Confidence != 0.83 {
		t.Errorf("options[0].Confidence = %v, want 0.83", q.Options[0].Confidence)
	}
}

func floatPtr(v float64) *float64 { return &v }

// TestBuildAskUserAnswerEnvelope_RestatesQuestionAndOptions checks that the
// framed follow-up turn includes the original question, the options the agent
// presented, the user's chosen answer, and a reminder that the reply is an
// answer (not a fresh directive). This is the framing that prevents an agent
// from acting on a bare option label like "Replace README.md" as if it were
// a new instruction.
func TestBuildAskUserAnswerEnvelope_RestatesQuestionAndOptions(t *testing.T) {
	questions := json.RawMessage(`{"questions":[{
		"question":"I found one target: the root README.md. Should the translation replace it or live alongside it?",
		"options":[
			{"label":"Replace README.md (Recommended)","description":"Matches the literal request."},
			{"label":"Add README.scn.md","description":"Preserves the English README."},
			{"label":"Add bilingual README.md","description":"Keeps both in one file."}
		]
	}]}`)
	answers := map[string]string{
		"I found one target: the root README.md. Should the translation replace it or live alongside it?": "Replace README.md (Recommended)",
	}

	got := buildAskUserAnswerEnvelope(questions, answers)

	mustContain := []string{
		"[AskUserQuestion answer]",
		"The user has answered your question.",
		"Question you asked:",
		"> I found one target: the root README.md. Should the translation replace it or live alongside it?",
		"Options you presented:",
		"1. Replace README.md (Recommended) — Matches the literal request.",
		"2. Add README.scn.md — Preserves the English README.",
		"3. Add bilingual README.md — Keeps both in one file.",
		"User's selected answer: Replace README.md (Recommended)",
		"This answer clarifies requirements; it is not authorization to implement, edit repository files, or modify files outside your phase artifact/output directory.",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("envelope missing %q\n--- got ---\n%s", want, got)
		}
	}
	// The framing header must precede the question, so the agent reads it
	// before encountering the imperative-sounding option label.
	if idxHeader := strings.Index(got, "[AskUserQuestion answer]"); idxHeader == -1 || idxHeader >= strings.Index(got, "User's selected answer:") {
		t.Errorf("framing header must appear before the answer line; got:\n%s", got)
	}
}

// TestBuildAskUserAnswerEnvelope_HandlesMissingOptions verifies the envelope
// still frames the answer when the original question carried no options
// (e.g. a free-form question or a malformed payload). The question text and
// answer must still be present even without an options block.
func TestBuildAskUserAnswerEnvelope_HandlesMissingOptions(t *testing.T) {
	questions := json.RawMessage(`{"questions":[{"question":"What version should we bump to?"}]}`)
	answers := map[string]string{"What version should we bump to?": "2.0.0"}

	got := buildAskUserAnswerEnvelope(questions, answers)

	if !strings.Contains(got, "Question you asked:") {
		t.Errorf("missing question framing:\n%s", got)
	}
	if !strings.Contains(got, "> What version should we bump to?") {
		t.Errorf("missing question text:\n%s", got)
	}
	if !strings.Contains(got, "User's selected answer: 2.0.0") {
		t.Errorf("missing answer:\n%s", got)
	}
	if !strings.Contains(got, "This answer clarifies requirements; it is not authorization to implement") {
		t.Errorf("missing non-authorization reminder:\n%s", got)
	}
	if strings.Contains(got, "Options you presented:") {
		t.Errorf("should omit options block when none were presented:\n%s", got)
	}
}

// TestRespondToAskUser_NativePreservesCustomAnswer pins the native JSON-RPC
// response contract for a free-text custom answer.
func TestRespondToAskUser_NativePreservesCustomAnswer(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{})
	p.SetStdin(&buf)
	p.SetQuestionIDsForTest(map[string]string{"Which approach?": "question-7"})

	const customAnswer = "Use a third custom approach"
	if err := p.RespondToAskUser(
		"42",
		json.RawMessage(`{"questions":[{"question":"Which approach?"}]}`),
		map[string]string{"Which approach?": customAnswer},
		nil,
	); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}

	var out struct {
		JSONRPC string        `json:"jsonrpc"`
		ID      int           `json:"id"`
		Result  AskUserResult `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &out); err != nil {
		t.Fatalf("unmarshal response: %v (raw=%q)", err, buf.String())
	}
	if out.JSONRPC != "2.0" || out.ID != 42 {
		t.Fatalf("response envelope = %+v, want JSON-RPC id 42", out)
	}
	if len(out.Result.Answers) != 1 || out.Result.Answers[0].QuestionID != "question-7" || out.Result.Answers[0].Value != customAnswer {
		t.Fatalf("answers = %+v, want question-7 with verbatim custom value", out.Result.Answers)
	}
}

// TestRespondToAskUser_SyntheticSendsFramedFollowUp pins down the wire
// behaviour the synthetic ask-user path produces. Before this change it sent
// the bare answer string ("Replace README.md") as a fresh user turn, and Codex
// was observed treating it as a new directive. The follow-up turn must now
// arrive wrapped in the framing envelope.
func TestRespondToAskUser_SyntheticSendsFramedFollowUp(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	questions := json.RawMessage(`{"questions":[{
		"question":"Replace or add alongside?",
		"options":[
			{"label":"Replace (Recommended)","description":"matches the literal request"},
			{"label":"Add alongside","description":"preserves the original"}
		]
	}]}`)
	answers := map[string]string{"Replace or add alongside?": "Replace (Recommended)"}

	if err := p.RespondToAskUser("codex-synthetic-123", questions, answers, nil); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}

	var sent Request
	if err := json.Unmarshal(buf.Bytes(), &sent); err != nil {
		t.Fatalf("unmarshal request: %v\nraw: %s", err, buf.String())
	}
	if sent.Method != "turn/start" {
		t.Fatalf("method = %q, want turn/start", sent.Method)
	}
	paramsBytes, err := json.Marshal(sent.Params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var tp TurnStartParams
	if err := json.Unmarshal(paramsBytes, &tp); err != nil {
		t.Fatalf("unmarshal params: %v\nraw: %s", err, string(paramsBytes))
	}
	if len(tp.Input) != 1 || tp.Input[0].Type != "text" {
		t.Fatalf("input = %+v, want one text item", tp.Input)
	}
	text := tp.Input[0].Text
	for _, want := range []string{
		"[AskUserQuestion answer]",
		"The user has answered your question.",
		"> Replace or add alongside?",
		"1. Replace (Recommended) — matches the literal request",
		"User's selected answer: Replace (Recommended)",
		"This answer clarifies requirements; it is not authorization to implement, edit repository files, or modify files outside your phase artifact/output directory.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("follow-up turn missing %q\n--- got ---\n%s", want, text)
		}
	}
	// Sanity: the bare answer is no longer the entire payload — that was the
	// pre-fix shape and is what allowed Codex to read it as a new directive.
	if strings.TrimSpace(text) == "Replace (Recommended)" {
		t.Errorf("follow-up turn is still a bare answer, framing not applied:\n%s", text)
	}
}

// completedTurnParams is a small helper that builds a turn/completed params
// JSON for the retry-path tests below.
func completedTurnParams(t *testing.T, threadID string) json.RawMessage {
	t.Helper()
	payload := TurnCompletedParams{
		ThreadID: threadID,
		Turn:     CompletedTurn{ID: "turn-1", Status: "completed"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal turn/completed: %v", err)
	}
	return raw
}

func TestTurnCompletedComputesCostWithCachedInputTokens(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "gpt-5.5"})

	usage := map[string]interface{}{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"tokenUsage": map[string]interface{}{
			"total": map[string]interface{}{
				"inputTokens":       1_000_000,
				"cachedInputTokens": 400_000,
				"outputTokens":      100_000,
				"totalTokens":       1_100_000,
			},
			"last": map[string]interface{}{
				"inputTokens":       1_000_000,
				"cachedInputTokens": 400_000,
				"outputTokens":      100_000,
				"totalTokens":       1_100_000,
			},
		},
	}
	params, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	usageMsg, ok := p.parseNotification("thread/tokenUsage/updated", params)
	if !ok {
		t.Fatal("token usage notification ok = false, want true")
	}
	const want = 10.90 // Long context: 600K at $10/M + 400K cached at $1/M + 100K output at $45/M
	if usageMsg.UsageUpdate == nil {
		t.Fatal("usage notification UsageUpdate = nil")
	}
	if diff := usageMsg.UsageUpdate.CostUSD - want; diff < -0.001 || diff > 0.001 {
		t.Fatalf("running CostUSD = %.6f, want %.2f", usageMsg.UsageUpdate.CostUSD, want)
	}

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("turn completed ok = false, want true")
	}
	if msg.Result == nil {
		t.Fatal("Result = nil")
	}
	if diff := msg.Result.TotalCostUSD - want; diff < -0.001 || diff > 0.001 {
		t.Fatalf("TotalCostUSD = %.6f, want %.2f", msg.Result.TotalCostUSD, want)
	}
	if msg.Result.Usage == nil {
		t.Fatal("Result.Usage = nil")
	}
	if msg.Result.Usage.CacheReadInputTokens != 400_000 {
		t.Fatalf("Result.Usage.CacheReadInputTokens = %d, want 400000", msg.Result.Usage.CacheReadInputTokens)
	}
}

func TestTurnCompletedAccumulatesShortAndLongContextRates(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "gpt-5.6-luna"})

	updates := []map[string]interface{}{
		{
			"total": map[string]interface{}{
				"inputTokens": 200_000, "cachedInputTokens": 20_000,
				"outputTokens": 10_000, "totalTokens": 210_000,
			},
			"last": map[string]interface{}{
				"inputTokens": 200_000, "cachedInputTokens": 20_000,
				"outputTokens": 10_000, "totalTokens": 210_000,
			},
		},
		{
			"total": map[string]interface{}{
				"inputTokens": 300_000, "cachedInputTokens": 30_000,
				"outputTokens": 20_000, "totalTokens": 320_000,
			},
			"last": map[string]interface{}{
				"inputTokens": 300_000, "cachedInputTokens": 30_000,
				"outputTokens": 20_000, "totalTokens": 320_000,
			},
		},
	}
	for _, tokenUsage := range updates {
		params, err := json.Marshal(map[string]interface{}{
			"threadId":   "thread-1",
			"turnId":     "turn-1",
			"tokenUsage": tokenUsage,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.parseNotification("thread/tokenUsage/updated", params); !ok {
			t.Fatal("token usage notification ok = false, want true")
		}
	}

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok || msg.Result == nil {
		t.Fatal("turn completed did not return a result")
	}
	// First update uses short-context rates ($0.0484); the 100K/10K/10K
	// deltas in the second update use long-context rates ($0.0544).
	const want = 0.1028
	if diff := msg.Result.TotalCostUSD - want; diff < -0.001 || diff > 0.001 {
		t.Fatalf("TotalCostUSD = %.6f, want %.3f", msg.Result.TotalCostUSD, want)
	}
}

func completedAgentMessageParams(t *testing.T, threadID, itemID, text string) json.RawMessage {
	t.Helper()
	payload := ItemCompletedParams{
		ThreadID: threadID,
		TurnID:   "turn-1",
		Item: ItemUnion{
			ID:   itemID,
			Type: "agentMessage",
			Text: text,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal item/completed: %v", err)
	}
	return raw
}

func TestTurnCompleted_WellFormedQuestionSurfacesOptions(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = "Which audience should the README target?\n" +
		"1. Internal engineers (Recommended): Focus on practical value.\n" +
		"2. Existing users: Focus on usage reference.\n" +
		"3. External readers: Focus on positioning."
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "control_request" || msg.ControlRequest == nil {
		t.Fatalf("got Type=%q ControlRequest=%v, want control_request", msg.Type, msg.ControlRequest)
	}
	if strings.Contains(buf.String(), `"method":"turn/start"`) {
		t.Errorf("unexpected follow-up turn written to stdin: %s", buf.String())
	}
	p.mu.Lock()
	retry := p.formatRetryCount
	p.mu.Unlock()
	if retry != 0 {
		t.Errorf("formatRetryCount = %d, want 0 after well-formed turn", retry)
	}
}

func TestTurnCompleted_MultiSentenceStemQuestionSurfacesOptions(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	// Verbatim shape from a real gpt-5.6 plan interview: the '?' ends the
	// first stem sentence and a declarative clarifier follows, so the stem's
	// final character is '.'.
	p.mu.Lock()
	p.lastAssistantText = "How should the opt-in notification preview setting be scoped? Existing server settings already control whether a feature may notify; this decision only governs how much content an allowed native notification reveals.\n" +
		"\n" +
		"1. Global preview toggle (Recommended): Keep previews off by default with one app-local privacy setting. [confidence: 0.88]\n" +
		"2. Per-feature preview toggle: Allow previews selectively for trusted features. [confidence: 0.62]\n" +
		"3. Per-attention-type toggle: Configure previews separately per attention type. [confidence: 0.38]"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "control_request" || msg.ControlRequest == nil {
		t.Fatalf("got Type=%q ControlRequest=%v, want AskUserQuestion control_request", msg.Type, msg.ControlRequest)
	}
	if msg.ControlRequest.Request.ToolName != "AskUserQuestion" {
		t.Fatalf("ToolName = %q, want AskUserQuestion", msg.ControlRequest.Request.ToolName)
	}
	var parsed struct {
		Questions []struct {
			Question string           `json:"question"`
			Options  []map[string]any `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &parsed); err != nil {
		t.Fatalf("unmarshal control request input: %v", err)
	}
	if len(parsed.Questions) != 1 || len(parsed.Questions[0].Options) != 3 {
		t.Fatalf("expected 1 question with 3 options, got %+v", parsed.Questions)
	}
	if !strings.HasPrefix(parsed.Questions[0].Question, "How should the opt-in notification preview setting be scoped?") {
		t.Errorf("question = %q", parsed.Questions[0].Question)
	}
	if strings.Contains(buf.String(), `"method":"turn/start"`) {
		t.Errorf("unexpected follow-up turn written to stdin: %s", buf.String())
	}
}

func TestTurnCompleted_MarkdownOptionsSeparateLabelsFromDescriptions(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = "What scope should control the maximum linearized snapshot size?\n" +
		"1. **Service-level row and byte caps — Recommended (High confidence).** Configure global maximum rows and decoded bytes, stop scanning as soon as either limit is exceeded.\n" +
		"2. **Per-table metadata cap (Medium confidence).** Add a table-specific limit to the control plane.\n" +
		"3. **Fixed code constant (Low confidence).** Enforce one compiled limit with no runtime configuration."
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok || msg.ControlRequest == nil {
		t.Fatalf("got ok=%v message=%+v, want AskUserQuestion control request", ok, msg)
	}
	var parsed struct {
		Questions []struct {
			Options []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &parsed); err != nil {
		t.Fatalf("unmarshal control request input: %v", err)
	}
	if len(parsed.Questions) != 1 || len(parsed.Questions[0].Options) != 3 {
		t.Fatalf("questions = %+v, want one question with three options", parsed.Questions)
	}
	first := parsed.Questions[0].Options[0]
	if first.Label != "Service-level row and byte caps — Recommended (High confidence)" {
		t.Errorf("first label = %q, want concise Markdown label", first.Label)
	}
	if first.Description != "Configure global maximum rows and decoded bytes, stop scanning as soon as either limit is exceeded." {
		t.Errorf("first description = %q, want trailing option explanation", first.Description)
	}
}

func TestTurnCompleted_NonQuestionStemWithContractMarkersSurfacesOptions(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = "Pick the notification preview scope.\n" +
		"1. Global toggle (Recommended): One app-local setting. [confidence: 0.85]\n" +
		"2. Per-feature toggle: More configuration and state. [confidence: 0.40]"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "control_request" || msg.ControlRequest == nil {
		t.Fatalf("got Type=%q, want control_request (confidence markers mark the list as a question)", msg.Type)
	}
}

func TestTurnCompleted_InformationalListStaysNormalCompletion(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = "Here's what I changed.\n" +
		"1. Updated the handler to reject oversized bodies.\n" +
		"2. Added a regression test for the new limit."
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "result" || msg.Subtype != "success" {
		t.Fatalf("got Type=%q Subtype=%q, want result/success for informational list", msg.Type, msg.Subtype)
	}
}

func TestTurnCompleted_UnparseableContractMarkedTextTriggersReformatRetry(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	// A single option can't parse into a question, but the confidence marker
	// proves this was contract output — remind instead of silently completing.
	p.mu.Lock()
	p.lastAssistantText = "Pick the notification preview scope.\n" +
		"1. Global toggle (Recommended): One app-local setting. [confidence: 0.85]"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if ok {
		t.Fatalf("parseNotification ok = true (msg=%+v), want false while reminder is pending", msg)
	}
	if !strings.Contains(buf.String(), "not in the required question format") {
		t.Errorf("expected reformat reminder on stdin; got %s", buf.String())
	}
}

func TestStemLooksLikeQuestion(t *testing.T) {
	tests := []struct {
		stem string
		want bool
	}{
		{"Which one?", true},
		{"Which one? The choice only affects defaults.", true},
		{"Which one?\nThe choice only affects defaults.", true},
		{"Pick the scope below.", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := stemLooksLikeQuestion(tt.stem); got != tt.want {
			t.Errorf("stemLooksLikeQuestion(%q) = %v, want %v", tt.stem, got, tt.want)
		}
	}
}

func TestTurnCompleted_BlankAgentMessageDoesNotEraseWellFormedQuestion(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	question := "How broad should the translation be?\n" +
		"1. README only (Recommended): Translate just the top-level README and leave docs untouched. [confidence: 0.94]\n" +
		"2. README + user docs: Translate the README plus user-facing docs like docs/. [confidence: 0.34]\n" +
		"3. Whole repo markdown: Translate every Markdown file in the repo. [confidence: 0.12]"
	msg, ok := p.parseNotification("item/completed", completedAgentMessageParams(t, "thread-1", "msg-question", question))
	if !ok {
		t.Fatal("question item parseNotification ok = false, want true")
	}
	if msg.Type != codexRoleAssistant {
		t.Fatalf("question item Type = %q, want assistant", msg.Type)
	}

	msg, ok = p.parseNotification("item/completed", completedAgentMessageParams(t, "thread-1", "msg-empty", ""))
	if ok {
		t.Fatalf("blank item parseNotification ok = true (msg=%+v), want false", msg)
	}

	msg, ok = p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("turn/completed parseNotification ok = false, want true")
	}
	if msg.Type != "control_request" || msg.ControlRequest == nil {
		t.Fatalf("got Type=%q ControlRequest=%v, want AskUserQuestion control_request", msg.Type, msg.ControlRequest)
	}
	if msg.ControlRequest.Request.ToolName != "AskUserQuestion" {
		t.Fatalf("ToolName = %q, want AskUserQuestion", msg.ControlRequest.Request.ToolName)
	}

	var parsed struct {
		Questions []struct {
			Question string           `json:"question"`
			Options  []map[string]any `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &parsed); err != nil {
		t.Fatalf("unmarshal control request input: %v", err)
	}
	if len(parsed.Questions) != 1 {
		t.Fatalf("questions len = %d, want 1", len(parsed.Questions))
	}
	if parsed.Questions[0].Question != "How broad should the translation be?" {
		t.Errorf("question = %q", parsed.Questions[0].Question)
	}
	if len(parsed.Questions[0].Options) != 3 {
		t.Errorf("options len = %d, want 3", len(parsed.Questions[0].Options))
	}
	if strings.Contains(buf.String(), `"method":"turn/start"`) {
		t.Errorf("unexpected follow-up turn written to stdin: %s", buf.String())
	}
}

func TestTurnCompleted_IllFormedQuestionTriggersReformatRetry(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = "What tone do you want: restrained, persuasive, or bold?"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if ok {
		t.Fatalf("parseNotification ok = true, want false (got msg=%+v)", msg)
	}
	if !strings.Contains(buf.String(), `"method":"turn/start"`) {
		t.Fatal("expected a follow-up turn to be written to stdin")
	}
	if !strings.Contains(buf.String(), "not in the required question format") {
		t.Errorf("reminder missing format-violation language; payload=%s", buf.String())
	}
	if !strings.Contains(buf.String(), "exactly 3 numbered options") {
		t.Errorf("reminder missing option-count directive; payload=%s", buf.String())
	}
	p.mu.Lock()
	retry := p.formatRetryCount
	p.mu.Unlock()
	if retry != 1 {
		t.Errorf("formatRetryCount = %d, want 1 after first violation", retry)
	}
}

func TestTurnCompleted_IllFormedFallsThroughAfterCap(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.formatRetryCount = maxQuestionFormatRetries
	p.lastAssistantText = "What is the target version?"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true after cap")
	}
	if msg.Type != "control_request" || msg.ControlRequest == nil {
		t.Fatalf("got Type=%q, want control_request", msg.Type)
	}
	if strings.Contains(buf.String(), `"method":"turn/start"`) {
		t.Errorf("no follow-up should be written after cap; got %s", buf.String())
	}
	// Options should be empty in the fall-through path.
	var parsed struct {
		Questions []struct {
			Options []map[string]string `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Questions) != 1 || len(parsed.Questions[0].Options) != 0 {
		t.Errorf("expected 1 question with 0 options, got %+v", parsed.Questions)
	}
	p.mu.Lock()
	retry := p.formatRetryCount
	p.mu.Unlock()
	if retry != 0 {
		t.Errorf("formatRetryCount = %d, want 0 after fall-through reset", retry)
	}
}

func TestTurnCompleted_FreeFormSentinelSkipsRetry(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = "FREE_FORM: What exact version string should we pin?"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if strings.Contains(buf.String(), `"method":"turn/start"`) {
		t.Errorf("no follow-up should be written for FREE_FORM; got %s", buf.String())
	}
	var parsed struct {
		Questions []struct {
			Question string              `json:"question"`
			Options  []map[string]string `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Questions) != 1 {
		t.Fatalf("questions len = %d, want 1", len(parsed.Questions))
	}
	if strings.HasPrefix(parsed.Questions[0].Question, "FREE_FORM:") {
		t.Errorf("question still contains FREE_FORM sentinel: %q", parsed.Questions[0].Question)
	}
	if len(parsed.Questions[0].Options) != 0 {
		t.Errorf("FREE_FORM question should have 0 options, got %d", len(parsed.Questions[0].Options))
	}
}

func TestTextContainsVerdictSentinel(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"verdict approved", "some rationale\n## Verdict\nAPPROVED\n", true},
		{"verdict changes_requested", "## Verdict\nCHANGES_REQUESTED", true},
		{"plain narrative with question mark", "Does this look right? I think so.", false},
		{"empty", "", false},
		{"verdict heading only without token", "## Verdict\nUNCLEAR", false},
		{"legacy stdout marker no longer matches", "REVIEW_STATUS: APPROVED", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := textContainsVerdictSentinel(tt.text); got != tt.want {
				t.Errorf("textContainsVerdictSentinel(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestParseNumberedOptions_RejectsVerdictSentinels(t *testing.T) {
	// A critic's structured verdict (## Findings / ## Suggestions / ## Verdict)
	// must never be parsed as AskUser options, even when some rubric bullets
	// contain '?' characters. The presence of `## Verdict\nAPPROVED|CHANGES_REQUESTED`
	// anywhere in the option body is decisive.
	input := `Evaluating the plan now.
1. Assessment
- Right level of detail? PASS
- Avoids contradictions? PASS
2. Verdict summary
APPROVED
3. Notes
No structural changes are required.
## Verdict
APPROVED`

	stem, opts, ok := parseNumberedOptions(input)
	if ok {
		t.Fatalf("parseNumberedOptions ok = true (stem=%q opts=%+v), want false for verdict-tainted numbered list", stem, opts)
	}
}

func TestTurnCompleted_ValidatorVerdictNotMisclassified(t *testing.T) {
	// Reproduces the Structural-validator false positive: a read-only
	// critic emits a final answer containing rubric bullets phrased as
	// questions ("Stubs clearly marked?") plus a numbered structure,
	// terminated by the `## Verdict\nAPPROVED` echo of what was written
	// to review-feedback.md. This must be treated as a completed success,
	// not a synthetic AskUser, and must not trigger a reformat follow-up.
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = `1. **Assessment**

- **Tracer Bullet: End-to-end wiring defined?** PASS
- **Tracer Bullet: Stubs clearly marked?** PASS
- **Structural Soundness: Avoids contradictions?** PASS

2. **Verdict summary**

APPROVED

3. **Specific feedback**

No structural changes are required.

I wrote review-feedback.md with the structured handoff:

## Verdict
APPROVED`
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true (validator verdict should take the success path)")
	}
	if msg.Type != "result" || msg.Subtype != "success" {
		t.Fatalf("got Type=%q Subtype=%q, want result/success (ControlRequest=%v)", msg.Type, msg.Subtype, msg.ControlRequest)
	}
	if msg.ControlRequest != nil {
		t.Errorf("unexpected ControlRequest on validator verdict: %+v", msg.ControlRequest)
	}
	if strings.Contains(buf.String(), `"method":"turn/start"`) {
		t.Errorf("unexpected reformat follow-up written to stdin: %s", buf.String())
	}
	p.mu.Lock()
	retry := p.formatRetryCount
	p.mu.Unlock()
	if retry != 0 {
		t.Errorf("formatRetryCount = %d, want 0 (verdict should not enter the question branch)", retry)
	}
}

// TestTurnCompleted_NumberedOptionsAfterToolUseSurfacesQuestion reproduces
// the roadmap-FAILED case: the agent does extensive tool exploration in a
// single turn (rg/cat to ground its question in the codebase) and then
// asks the one remaining ambiguity as a well-formed numbered-options
// question. Even though turnHadToolUse=true, the question must surface
// as a control_request rather than emitting a SUCCESS result that the harness
// would classify as a missing-root-outcome violation.
func TestTurnCompleted_NumberedOptionsAfterToolUseSurfacesQuestion(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.turnHadToolUse = true
	p.lastAssistantText = "Which Napolitan register should the translations use across all three READMEs?\n" +
		"1. Neutral written Napolitan (Recommended): Documentation-friendly register that reads naturally in writing. [confidence: 0.89]\n" +
		"2. Colloquial Napolitan: More spoken, expressive tone — risks uneven technical clarity. [confidence: 0.48]\n" +
		"3. Conservative Italian-leaning Napolitan: Maximum readability but weakens the request for a distinctly Napolitan translation. [confidence: 0.36]"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "control_request" || msg.ControlRequest == nil {
		t.Fatalf("got Type=%q ControlRequest=%v, want control_request (tool-use turn ended in well-formed question)", msg.Type, msg.ControlRequest)
	}
	if strings.Contains(buf.String(), `"method":"turn/start"`) {
		t.Errorf("unexpected follow-up turn written to stdin: %s", buf.String())
	}

	var parsed struct {
		Questions []struct {
			Options []map[string]any `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Questions) != 1 {
		t.Fatalf("questions len = %d, want 1", len(parsed.Questions))
	}
	if len(parsed.Questions[0].Options) != 3 {
		t.Errorf("options len = %d, want 3 parsed from numbered list", len(parsed.Questions[0].Options))
	}
}

// TestTurnCompleted_FreeFormSentinelAfterToolUse covers the FREE_FORM-sentinel
// path under the same tool-use scenario: an explicit FREE_FORM marker is an
// unambiguous question signal that should bypass the no-tool-use gate.
func TestTurnCompleted_FreeFormSentinelAfterToolUse(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.turnHadToolUse = true
	p.lastAssistantText = "FREE_FORM: What exact version string should we pin?"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "control_request" || msg.ControlRequest == nil {
		t.Fatalf("got Type=%q ControlRequest=%v, want control_request", msg.Type, msg.ControlRequest)
	}
	if strings.Contains(buf.String(), `"method":"turn/start"`) {
		t.Errorf("unexpected follow-up turn written to stdin: %s", buf.String())
	}
}

// TestTurnCompleted_LooseQuestionAfterToolUseEmitsSuccess pins down the
// false-positive guard. A tool-heavy turn whose final text merely contains
// '?' without numbered options or a FREE_FORM sentinel — e.g., narrating
// intent like "Wrote the file. Is that what you wanted?" — must NOT be
// reclassified as a question. Without the no-tool-use gate on the loose path,
// every mid-turn rhetorical '?' would synthesize an AskUser and stall the
// session.
func TestTurnCompleted_LooseQuestionAfterToolUseEmitsSuccess(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.turnHadToolUse = true
	p.lastAssistantText = "Wrote the README. Is that what you wanted?"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "result" || msg.Subtype != "success" {
		t.Fatalf("got Type=%q Subtype=%q, want result/success (ControlRequest=%v)", msg.Type, msg.Subtype, msg.ControlRequest)
	}
	if msg.ControlRequest != nil {
		t.Errorf("unexpected ControlRequest on loose-question tool-use turn: %+v", msg.ControlRequest)
	}
	if strings.Contains(buf.String(), `"method":"turn/start"`) {
		t.Errorf("unexpected reformat follow-up written to stdin: %s", buf.String())
	}
}

// TestTurnCompleted_InformationalNumberedListIsCleanSuccess proves a final text
// that merely summarizes findings as a numbered list — with a non-question stem
// — completes as a success result rather than being treated as an AskUserQuestion
// just because it enumerates items.
func TestTurnCompleted_InformationalNumberedListIsCleanSuccess(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = "Here is what I found:\n" +
		"1. The config loader ignores env overrides.\n" +
		"2. The default timeout is 30s.\n" +
		"3. Logs are written to /tmp/agentico.log."
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "result" || msg.Subtype != "success" {
		t.Fatalf("got Type=%q Subtype=%q ControlRequest=%v, want result/success", msg.Type, msg.Subtype, msg.ControlRequest)
	}
	if strings.Contains(buf.String(), `"method":"turn/start"`) {
		t.Errorf("unexpected follow-up turn written to stdin: %s", buf.String())
	}
}

// TestTurnCompleted_InteractiveNeverSynthesizesQuestion proves an Interactive
// session (AMA chat, where a human answers every turn directly) never
// synthesizes an AskUserQuestion picker, no matter how question-shaped the
// final text is: the text-parsing pipeline exists only to imitate Claude's
// native tool-call UX for a provider that can otherwise only express a
// question as plain text, and a human reading the chat gets no benefit from
// that imitation — they can just read whatever the model asked and reply with
// an ordinary follow-up message.
func TestTurnCompleted_InteractiveNeverSynthesizesQuestion(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"numbered options", "Which audience should the README target?\n" +
			"1. Internal engineers (Recommended): Focus on practical value.\n" +
			"2. Existing users: Focus on usage reference.\n" +
			"3. External readers: Focus on positioning."},
		{"bare question", "Should I proceed with the destructive migration?"},
		{"FREE_FORM sentinel", "FREE_FORM: What exact version string should we pin?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex", Interactive: true})
			p.SetStdin(&buf)
			p.SetThreadIDForTest("thread-1")

			p.mu.Lock()
			p.lastAssistantText = c.text
			p.mu.Unlock()

			msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
			if !ok {
				t.Fatal("parseNotification ok = false, want true")
			}
			if msg.Type != "result" || msg.Subtype != "success" {
				t.Fatalf("got Type=%q Subtype=%q ControlRequest=%v, want result/success", msg.Type, msg.Subtype, msg.ControlRequest)
			}
			if strings.Contains(buf.String(), `"method":"turn/start"`) {
				t.Errorf("sent a reformat reminder, want none: %s", buf.String())
			}
		})
	}
}

// TestBuildAskUserAnswerEnvelope_AppendsAskingFormatReminder verifies that
// every answer envelope re-anchors Codex on the question-format contract.
// The reminder is intentionally a short pointer back to the system prompt
// (the full spec lives there); this test pins the [Reminder] marker, the
// pointer phrasing, and the post-answer ordering — not specific format
// rules, which belong with the system prompt.
func TestBuildAskUserAnswerEnvelope_AppendsAskingFormatReminder(t *testing.T) {
	questions := json.RawMessage(`{"questions":[{
		"question":"Replace or add alongside?",
		"options":[
			{"label":"Replace (Recommended)","description":"matches the literal request"},
			{"label":"Add alongside","description":"preserves the original"}
		]
	}]}`)
	answers := map[string]string{"Replace or add alongside?": "Replace (Recommended)"}

	got := buildAskUserAnswerEnvelope(questions, answers)

	if !strings.Contains(got, "[Reminder]") {
		t.Errorf("envelope missing [Reminder] marker\n--- got ---\n%s", got)
	}
	if !strings.Contains(got, "asking-questions format from your system prompt") {
		t.Errorf("envelope missing pointer back to system prompt\n--- got ---\n%s", got)
	}
	if !strings.Contains(got, "not authorization to implement") {
		t.Errorf("envelope missing non-authorization reminder\n--- got ---\n%s", got)
	}
	// The reminder must come AFTER the answer block so the agent reads the
	// answer first and the format rule last (most-recent-wins anchoring).
	idxAnswer := strings.Index(got, "User's selected answer:")
	idxReminder := strings.Index(got, "[Reminder]")
	if idxAnswer == -1 || idxReminder == -1 || idxReminder < idxAnswer {
		t.Errorf("reminder must follow the answer block; got idxAnswer=%d idxReminder=%d in:\n%s", idxAnswer, idxReminder, got)
	}
}

func TestCodexAskingQuestionsClause_CallsOutConfirmationTrap(t *testing.T) {
	clause := (&Provider{}).AskingQuestionsClause()
	for _, want := range []string{
		"Confirmation traps to avoid",
		"every turn of an interview",
		"Yes, do X (Recommended)",
	} {
		if !strings.Contains(clause, want) {
			t.Errorf("AskingQuestionsClause missing %q\n--- got ---\n%s", want, clause)
		}
	}
}
