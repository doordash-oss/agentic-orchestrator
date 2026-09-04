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
	if strings.Contains(buf.String(), `"collaborationMode"`) {
		t.Error("native review must not set collaboration mode")
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
