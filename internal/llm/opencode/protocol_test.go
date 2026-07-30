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

package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// --- handshake harness ---

type handshakeHarness struct {
	p     *Protocol
	pw    *io.PipeWriter
	lines chan []byte
}

func newHandshakeHarness(t *testing.T, opts llm.ProtocolOpts) *handshakeHarness {
	t.Helper()
	pr, pw := io.Pipe()
	p := NewProtocol(opts)
	p.SetStdin(pw)

	lines := make(chan []byte, 16)
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			lines <- append([]byte(nil), sc.Bytes()...)
		}
		close(lines)
	}()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	return &handshakeHarness{p: p, pw: pw, lines: lines}
}

// nextRequest reads the protocol's next outbound JSON-RPC line.
func (h *handshakeHarness) nextRequest(t *testing.T) inboundEnvelope {
	t.Helper()
	select {
	case b, ok := <-h.lines:
		if !ok {
			t.Fatal("protocol stdin closed before expected request")
		}
		var env inboundEnvelope
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatalf("outbound line is not JSON-RPC: %q: %v", b, err)
		}
		return env
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for outbound request")
		return inboundEnvelope{}
	}
}

// feed delivers an inbound line to the protocol (as readMessages would).
func (h *handshakeHarness) feed(t *testing.T, line []byte) []llm.SDKMessage {
	t.Helper()
	msgs, err := h.p.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine(%s) error: %v", line, err)
	}
	return msgs
}

func responseLine(t *testing.T, id int, result any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return b
}

func errorResponseLine(t *testing.T, id int, code int, msg string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg},
	})
	if err != nil {
		t.Fatalf("marshal error response: %v", err)
	}
	return b
}

func notificationLine(t *testing.T, method string, params any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	return b
}

func serverRequestLine(t *testing.T, id int, method string, params any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal server request: %v", err)
	}
	return b
}

func mustID(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	id, err := parseID(raw)
	if err != nil {
		t.Fatalf("parse id %q: %v", string(raw), err)
	}
	return id
}

// --- handshake tests (Task 3) ---

func TestHandshake_HappyPath(t *testing.T) {
	h := newHandshakeHarness(t, llm.ProtocolOpts{
		Model:         "opencode:anthropic/claude-sonnet-4-5",
		WorkDir:       "/work/dir",
		InitialPrompt: "RENDERED PHASE PROMPT with marker path",
	})

	errCh := make(chan error, 1)
	go func() { errCh <- h.p.Handshake(context.Background()) }()

	// 1. initialize negotiates protocol version 1 with no client fs/terminal caps.
	initReq := h.nextRequest(t)
	if initReq.Method != "initialize" {
		t.Fatalf("first request method = %q, want initialize", initReq.Method)
	}
	var initParams InitializeParams
	if err := json.Unmarshal(initReq.Params, &initParams); err != nil {
		t.Fatalf("unmarshal initialize params: %v", err)
	}
	if initParams.ProtocolVersion != 1 {
		t.Fatalf("initialize protocolVersion = %d, want 1", initParams.ProtocolVersion)
	}
	// Agentico hosts the client filesystem surface (see clientfs.go) so it must
	// advertise it; it does not host a client terminal.
	if !initParams.ClientCapabilities.FS.ReadTextFile || !initParams.ClientCapabilities.FS.WriteTextFile {
		t.Fatalf("initialize FS capability = %+v, want both true", initParams.ClientCapabilities.FS)
	}
	if initParams.ClientCapabilities.Terminal {
		t.Fatalf("initialize declared terminal capability = true, want false")
	}
	h.feed(t, responseLine(t, mustID(t, initReq.ID), map[string]any{
		"protocolVersion": 1,
		"agentInfo":       map[string]any{"name": "OpenCode", "version": "1.17.9"},
	}))

	// 2. session/new is rooted at the resolved work directory.
	snReq := h.nextRequest(t)
	if snReq.Method != "session/new" {
		t.Fatalf("second request method = %q, want session/new", snReq.Method)
	}
	var snParams SessionNewParams
	if err := json.Unmarshal(snReq.Params, &snParams); err != nil {
		t.Fatalf("unmarshal session/new params: %v", err)
	}
	if snParams.Cwd != "/work/dir" {
		t.Fatalf("session/new cwd = %q, want /work/dir", snParams.Cwd)
	}
	h.feed(t, responseLine(t, mustID(t, snReq.ID), map[string]any{"sessionId": "ses_abc123"}))

	// 3. session/prompt delivers the rendered phase prompt as the first turn.
	promptReq := h.nextRequest(t)
	if promptReq.Method != "session/prompt" {
		t.Fatalf("third request method = %q, want session/prompt", promptReq.Method)
	}
	var promptParams PromptParams
	if err := json.Unmarshal(promptReq.Params, &promptParams); err != nil {
		t.Fatalf("unmarshal session/prompt params: %v", err)
	}
	if promptParams.SessionID != "ses_abc123" {
		t.Fatalf("session/prompt sessionId = %q, want ses_abc123", promptParams.SessionID)
	}
	if len(promptParams.Prompt) != 1 || promptParams.Prompt[0].Text != "RENDERED PHASE PROMPT with marker path" {
		t.Fatalf("session/prompt prompt = %+v, want the rendered phase prompt", promptParams.Prompt)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("Handshake() error: %v", err)
	}
	if got := h.p.ACPSessionID(); got != "ses_abc123" {
		t.Fatalf("ACPSessionID() = %q, want ses_abc123", got)
	}
	if got := h.p.NegotiatedVersion(); got != 1 {
		t.Fatalf("NegotiatedVersion() = %d, want 1", got)
	}
	// Session-identity parity: the captured ACP session id is surfaced through
	// the Agentico-facing SessionID so session views, PID-file identity, and the
	// permission cache scope to this OpenCode session.
	if got := h.p.SessionID(); got != "ses_abc123" {
		t.Fatalf("SessionID() = %q, want ses_abc123", got)
	}
}

func TestHandshake_NativeToollessReviewDeclaresNoClientToolSurface(t *testing.T) {
	h := newHandshakeHarness(t, llm.ProtocolOpts{
		Model:                "opencode:anthropic/claude-haiku-4-5",
		WorkDir:              "/work/dir",
		InitialPrompt:        "Classify one command.",
		NativeToollessReview: true,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- h.p.Handshake(context.Background()) }()

	initReq := h.nextRequest(t)
	var initParams InitializeParams
	if err := json.Unmarshal(initReq.Params, &initParams); err != nil {
		t.Fatalf("unmarshal initialize params: %v", err)
	}
	if initParams.ClientCapabilities.FS.ReadTextFile || initParams.ClientCapabilities.FS.WriteTextFile {
		t.Fatalf("review initialize FS capability = %+v, want no client filesystem", initParams.ClientCapabilities.FS)
	}
	if initParams.ClientCapabilities.Terminal {
		t.Fatal("review initialize declared terminal capability")
	}
	h.feed(t, responseLine(t, mustID(t, initReq.ID), map[string]any{"protocolVersion": 1}))

	sessionReq := h.nextRequest(t)
	var sessionParams SessionNewParams
	if err := json.Unmarshal(sessionReq.Params, &sessionParams); err != nil {
		t.Fatalf("unmarshal session/new params: %v", err)
	}
	if len(sessionParams.MCPServers) != 0 {
		t.Fatalf("review session/new MCP servers = %v, want none", sessionParams.MCPServers)
	}
	h.feed(t, responseLine(t, mustID(t, sessionReq.ID), map[string]any{"sessionId": "hidden-review"}))

	promptReq := h.nextRequest(t)
	var promptParams PromptParams
	if err := json.Unmarshal(promptReq.Params, &promptParams); err != nil {
		t.Fatalf("unmarshal session/prompt params: %v", err)
	}
	if len(promptParams.Prompt) != 1 || promptParams.Prompt[0].Text != "Classify one command." {
		t.Fatalf("review prompt = %+v, want exact minimal prompt", promptParams.Prompt)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Handshake() error: %v", err)
	}
}

func TestHandshake_RejectsIncompatibleProtocolVersion(t *testing.T) {
	h := newHandshakeHarness(t, llm.ProtocolOpts{WorkDir: "/w", InitialPrompt: "p"})
	errCh := make(chan error, 1)
	go func() { errCh <- h.p.Handshake(context.Background()) }()

	initReq := h.nextRequest(t)
	h.feed(t, responseLine(t, mustID(t, initReq.ID), map[string]any{"protocolVersion": 2}))

	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("Handshake() error = %v, want protocol-version incompatibility", err)
	}
}

func TestHandshake_InitializeErrorFailsFast(t *testing.T) {
	h := newHandshakeHarness(t, llm.ProtocolOpts{WorkDir: "/w", InitialPrompt: "p"})
	errCh := make(chan error, 1)
	go func() { errCh <- h.p.Handshake(context.Background()) }()

	initReq := h.nextRequest(t)
	h.feed(t, errorResponseLine(t, mustID(t, initReq.ID), -32000, "initialize boom"))

	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), "initialize failed") {
		t.Fatalf("Handshake() error = %v, want initialize failure", err)
	}
}

func TestHandshake_TimesOutWhenNoResponse(t *testing.T) {
	h := newHandshakeHarness(t, llm.ProtocolOpts{WorkDir: "/w", InitialPrompt: "p"})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- h.p.Handshake(ctx) }()

	_ = h.nextRequest(t) // drain initialize; never respond
	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("Handshake() error = %v, want timeout", err)
	}
}

// --- stream normalization tests (Task 4) ---

// newPostHandshakeProtocol returns a protocol with handshake request ids pinned
// so response/notification handling can be exercised directly.
func newPostHandshakeProtocol(t *testing.T, opts ...llm.ProtocolOpts) (*Protocol, *syncBuffer, int) {
	t.Helper()
	buf := &syncBuffer{}
	protocolOpts := llm.ProtocolOpts{Model: "opencode:anthropic/claude-sonnet-4-5"}
	if len(opts) > 0 {
		protocolOpts = opts[0]
	}
	p := NewProtocol(protocolOpts)
	p.SetStdin(buf)
	const initID, sessionNewID, promptID = 100, 101, 102
	p.setRequestIDsForTest(initID, sessionNewID, promptID)
	p.acpSessionID = "ses_x"
	return p, buf, promptID
}

func TestParseLine_AssistantTextAccumulatesWithoutLoss(t *testing.T) {
	p, _, _ := newPostHandshakeProtocol(t)

	msgs := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "msg_1", "content": map[string]any{"type": "text", "text": "Hello "}},
	}))
	assertAssistantText(t, msgs, "Hello ")

	msgs = mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "msg_1", "content": map[string]any{"type": "text", "text": "world"}},
	}))
	// Chunks accumulate; the partial carries the full text so far, never just
	// the latest delta and never a duplicate.
	assertAssistantText(t, msgs, "Hello world")
}

func TestParseLine_NativeToollessReviewFailsClosedOnUnexpectedACPInteractions(t *testing.T) {
	tests := []struct {
		name string
		line func(*testing.T) []byte
	}{
		{
			name: "permission request",
			line: func(t *testing.T) []byte {
				return serverRequestLine(t, 201, requestPermissionMethod, map[string]any{
					"sessionId": "ses_x",
					"toolCall":  map[string]any{"kind": ToolKindExecute, "title": "run command"},
					"options":   []any{},
				})
			},
		},
		{
			name: "question request",
			line: func(t *testing.T) []byte {
				return serverRequestLine(t, 202, requestPermissionMethod, map[string]any{
					"sessionId": "ses_x",
					"toolCall":  map[string]any{"kind": ToolKindQuestion, "title": "Proceed?"},
					"options":   []any{},
				})
			},
		},
		{
			name: "client filesystem request",
			line: func(t *testing.T) []byte {
				return serverRequestLine(t, 203, readTextFileMethod, map[string]any{"path": "/secret"})
			},
		},
		{
			name: "tool call notification",
			line: func(t *testing.T) []byte {
				return notificationLine(t, "session/update", map[string]any{
					"sessionId": "ses_x",
					"update": map[string]any{
						"sessionUpdate": UpdateToolCall,
						"toolCallId":    "call_1",
						"kind":          ToolKindThink,
						"title":         "spawn child",
					},
				})
			},
		},
		{
			name: "child session activity",
			line: func(t *testing.T) []byte {
				return notificationLine(t, "session/update", map[string]any{
					"sessionId": "ses_child",
					"update": map[string]any{
						"sessionUpdate": UpdateAgentMessageChunk,
						"messageId":     "child_message",
						"content":       map[string]any{"type": "text", "text": "child output"},
					},
				})
			},
		},
		{
			name: "malformed session update",
			line: func(t *testing.T) []byte {
				return notificationLine(t, "session/update", "not an update object")
			},
		},
		{
			name: "unknown session update",
			line: func(t *testing.T) []byte {
				return notificationLine(t, "session/update", map[string]any{
					"sessionId": "ses_x",
					"update":    map[string]any{"sessionUpdate": "future_interaction_update"},
				})
			},
		},
		{
			name: "unexpected response",
			line: func(t *testing.T) []byte {
				return responseLine(t, 999, map[string]any{})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _ := newPostHandshakeProtocol(t, llm.ProtocolOpts{NativeToollessReview: true})
			msgs := mustParse(t, p, tc.line(t))
			if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
				t.Fatalf("unexpected review interaction produced %+v, want one terminal error", msgs)
			}
			if msgs[0].ControlRequest != nil || msgs[0].ToolProgress != nil {
				t.Fatalf("unexpected review interaction escaped as interactive activity: %+v", msgs[0])
			}
		})
	}
}

func TestParseLine_NativeToollessReviewIgnoresAvailableCommandMetadata(t *testing.T) {
	p, _, _ := newPostHandshakeProtocol(t, llm.ProtocolOpts{NativeToollessReview: true})
	messages := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update": map[string]any{
			"sessionUpdate": UpdateAvailableCommands,
			"availableCommands": []map[string]any{
				{"name": "review", "description": "review changes"},
			},
		},
	}))
	if len(messages) != 0 {
		t.Fatalf("available-command metadata produced %+v, want no activity", messages)
	}
	p.mu.Lock()
	failed := p.resultEmitted
	p.mu.Unlock()
	if failed {
		t.Fatal("available-command metadata sealed native review")
	}
}

func TestNativeToollessReviewRejectsQuestionAndAdditionalTurn(t *testing.T) {
	p, buf, promptID := newPostHandshakeProtocol(t, llm.ProtocolOpts{NativeToollessReview: true})
	mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update": map[string]any{
			"sessionUpdate": UpdateAgentMessageChunk,
			"messageId":     "msg_question",
			"content":       map[string]any{"type": "text", "text": "Should this command be allowed?"},
		},
	}))

	msgs := mustParse(t, p, responseLine(t, promptID, map[string]any{"stopReason": StopReasonEndTurn}))
	if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
		t.Fatalf("question-shaped review response produced %+v, want terminal error", msgs)
	}
	if got := strings.Count(buf.String(), `"method":"session/prompt"`); got != 0 {
		t.Fatalf("question-shaped review response sent %d continuation prompts, want none: %s", got, buf.String())
	}
	if err := p.SendUserMessage("second turn"); err == nil {
		t.Fatal("SendUserMessage() in native tool-less review succeeded, want rejection")
	}
}

func TestNativeToollessReviewPreservesExactDecisionAndCompletesOneTurn(t *testing.T) {
	for _, decision := range []string{"ALLOW", "DEFER"} {
		t.Run(decision, func(t *testing.T) {
			p, _, promptID := newPostHandshakeProtocol(t, llm.ProtocolOpts{NativeToollessReview: true})
			msgs := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
				"sessionId": "ses_x",
				"update": map[string]any{
					"sessionUpdate": UpdateAgentMessageChunk,
					"messageId":     "decision",
					"content":       map[string]any{"type": "text", "text": decision},
				},
			}))
			assertAssistantText(t, msgs, decision)

			msgs = mustParse(t, p, responseLine(t, promptID, map[string]any{"stopReason": StopReasonEndTurn}))
			if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsSuccess() {
				t.Fatalf("%s review completion produced %+v, want one success result", decision, msgs)
			}
		})
	}
}

func TestParseLine_AssistantTextResetsOnNewMessageID(t *testing.T) {
	p, _, _ := newPostHandshakeProtocol(t)

	msgs := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "msg_1", "content": map[string]any{"type": "text", "text": "First."}},
	}))
	assertAssistantText(t, msgs, "First.")

	msgs = mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update": map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    "call_read",
			"status":        "completed",
			"kind":          "read",
			"title":         "README.md",
		},
	}))
	if len(msgs) != 1 || msgs[0].ToolProgress == nil {
		t.Fatalf("tool update = %+v, want tool_progress", msgs)
	}

	msgs = mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "msg_2", "content": map[string]any{"type": "text", "text": "Second."}},
	}))
	assertAssistantText(t, msgs, "Second.")
}

func TestParseLine_ThoughtChunkSurfacesAsThinking(t *testing.T) {
	p, _, _ := newPostHandshakeProtocol(t)
	msgs := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update":    map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "raw hidden thought token"}},
	}))
	if len(msgs) != 1 || msgs[0].Assistant == nil {
		t.Fatalf("thought chunk produced %+v, want one assistant message", msgs)
	}
	block := msgs[0].Assistant.Message.Content[0]
	if !block.IsThinking() {
		t.Fatalf("thought block = %+v, want thinking block", block)
	}
	if block.Thinking != "Thinking..." {
		t.Fatalf("thought block = %+v, want generic Thinking... marker", block)
	}
	if strings.Contains(block.Thinking, "raw hidden thought token") {
		t.Fatalf("thought block leaked raw OpenCode thought text: %+v", block)
	}
}

func TestParseLine_EstimatesContextUntilUsageUpdateArrives(t *testing.T) {
	p, _, _ := newPostHandshakeProtocol(t, llm.ProtocolOpts{ContextWindow: 2000})
	p.SetStdin(io.Discard)
	if err := p.SendUserMessage(strings.Repeat("abcd", 100)); err != nil {
		t.Fatalf("SendUserMessage() error = %v", err)
	}

	msgs := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update": map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"content":       map[string]any{"type": "text", "text": strings.Repeat("abcd", 100)},
		},
	}))
	u := lastUsageUpdate(t, msgs)
	if u.ContextTotalTokens != 200 || u.ContextWindow != 2000 {
		t.Fatalf("estimated context = total %d window %d, want 200/2000", u.ContextTotalTokens, u.ContextWindow)
	}

	msgs = mustParse(t, p, usageUpdateLine(t, map[string]any{"used": 512, "size": 2000}))
	u = lastUsageUpdate(t, msgs)
	if u.ContextTotalTokens != 512 || u.ContextWindow != 2000 {
		t.Fatalf("provider context = total %d window %d, want 512/2000", u.ContextTotalTokens, u.ContextWindow)
	}
}

// TestEstimatedUsageEmitStep_CapsAt1500ForLargeWindows guards against the
// emit-step growing so coarse that large-context models (>=150K tokens)
// visibly stall: with the old 5000-token cap, a 1.04M-window model (typical
// for zero-telemetry backends like opencode's fireworks/glm-5p2 route) could
// take several minutes of real tool-call activity before the estimated
// context display moved at all, because so little text crosses the tracked
// tool-call boundary while the parent session is idle waiting on delegated
// Task() subagents.
func TestEstimatedUsageEmitStep_CapsAt1500ForLargeWindows(t *testing.T) {
	cases := []struct {
		name   string
		window int
		want   int
	}{
		{"tiny window floors at 100", 5000, 100},
		{"unaffected mid window", 50000, 500},
		{"large window clamps to new cap", 200000, 1500},
		{"huge window clamps to new cap", 1040000, 1500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProtocol(llm.ProtocolOpts{ContextWindow: tc.window})
			p.mu.Lock()
			got := p.estimatedUsageEmitStepLocked()
			p.mu.Unlock()
			if got != tc.want {
				t.Fatalf("estimatedUsageEmitStepLocked() for window %d = %d, want %d", tc.window, got, tc.want)
			}
		})
	}
}

func TestParseLine_ToolProgressKeepsStableID(t *testing.T) {
	p, _, _ := newPostHandshakeProtocol(t)

	msgs := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update": map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    "call_42",
			"title":         "Read file",
			"kind":          "read",
			"status":        "in_progress",
		},
	}))
	if len(msgs) != 1 || msgs[0].ToolProgress == nil {
		t.Fatalf("tool_call produced %+v, want one tool_progress message", msgs)
	}
	tp := msgs[0].ToolProgress
	if tp.ToolUseID != "call_42" {
		t.Fatalf("tool progress id = %q, want call_42", tp.ToolUseID)
	}
	if tp.ToolName != "Read file" {
		t.Fatalf("tool progress name = %q, want 'Read file'", tp.ToolName)
	}

	msgs = mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update":    map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call_42", "status": "completed"},
	}))
	if len(msgs) != 1 || msgs[0].ToolProgress == nil || msgs[0].ToolProgress.ToolUseID != "call_42" {
		t.Fatalf("tool_call_update produced %+v, want tool_progress with stable id call_42", msgs)
	}
}

func TestParseLine_OpenCodeTaskUpdateEmitsTaskToolUsePrompt(t *testing.T) {
	p, _, _ := newPostHandshakeProtocol(t)

	msgs := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update": map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    "call_task",
			"kind":          "think",
			"title":         "Find markdown/link CI checks & hooks",
			"status":        "in_progress",
			"rawInput": map[string]any{
				"description":   "Find markdown/link CI checks & hooks",
				"subagent_type": "codebase-locator",
				"prompt":        "Inspect README.md, docs, and CI config. Return the files and commands that validate markdown links.",
			},
		},
	}))
	if len(msgs) != 3 {
		t.Fatalf("task update produced %+v, want assistant task tool_use, task start, and tool_progress", msgs)
	}
	if msgs[0].Assistant == nil || len(msgs[0].Assistant.Message.Content) != 1 {
		t.Fatalf("first message = %+v, want assistant task tool_use", msgs[0])
	}
	block := msgs[0].Assistant.Message.Content[0]
	if !block.IsToolUse() || block.Name != "Task" || block.ID != "call_task" {
		t.Fatalf("task block = %+v, want Task tool_use with id call_task", block)
	}
	var input map[string]string
	if err := json.Unmarshal(block.Input, &input); err != nil {
		t.Fatalf("task input is not JSON object: %v", err)
	}
	if input["prompt"] == "" || input["description"] != "Find markdown/link CI checks & hooks" {
		t.Fatalf("task input = %+v, want preserved description and prompt", input)
	}
	if msgs[1].TaskStarted == nil ||
		msgs[1].TaskStarted.TaskID != "call_task" ||
		msgs[1].TaskStarted.ToolUseID != "call_task" ||
		msgs[1].TaskStarted.Description != "Find markdown/link CI checks & hooks" ||
		msgs[1].TaskStarted.TaskType != "codebase-locator" {
		t.Fatalf("second message = %+v, want normalized task start for call_task", msgs[1])
	}
	if msgs[2].ToolProgress == nil || msgs[2].ToolProgress.ToolName != "Find markdown/link CI checks & hooks" {
		t.Fatalf("third message = %+v, want task tool_progress", msgs[2])
	}

	terminal := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update": map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    "call_task",
			"kind":          "think",
			"title":         "Find markdown/link CI checks & hooks",
			"status":        "completed",
			"rawInput": map[string]any{
				"description":   "Find markdown/link CI checks & hooks",
				"subagent_type": "codebase-locator",
				"prompt":        "Inspect README.md, docs, and CI config. Return the files and commands that validate markdown links.",
			},
		},
	}))
	if len(terminal) != 2 ||
		terminal[0].TaskNotification == nil ||
		terminal[0].TaskNotification.TaskID != "call_task" ||
		terminal[0].TaskNotification.ToolUseID != "call_task" ||
		terminal[0].TaskNotification.Status != "completed" ||
		terminal[1].ToolProgress == nil {
		t.Fatalf("terminal task update produced %+v, want task notification plus tool_progress", terminal)
	}

	duplicateTerminal := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update": map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    "call_task",
			"status":        "completed",
		},
	}))
	if len(duplicateTerminal) != 1 || duplicateTerminal[0].ToolProgress == nil {
		t.Fatalf("duplicate terminal task update produced %+v, want only tool_progress", duplicateTerminal)
	}
}

func TestParseLine_OpenCodeTaskLifecycleTerminalStatuses(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled"} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			p, _, _ := newPostHandshakeProtocol(t)
			started := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
				"sessionId": "ses_x",
				"update": map[string]any{
					"sessionUpdate": "tool_call_update",
					"toolCallId":    "call_" + status,
					"kind":          "think",
					"title":         "Research " + status,
					"status":        "in_progress",
					"rawInput": map[string]any{
						"description":   "Research " + status,
						"subagent_type": "researcher",
						"prompt":        "Inspect the repository.",
					},
				},
			}))
			if len(started) != 3 || started[1].TaskStarted == nil {
				t.Fatalf("start update produced %+v, want normalized task start", started)
			}

			terminal := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
				"sessionId": "ses_x",
				"update": map[string]any{
					"sessionUpdate": "tool_call_update",
					"toolCallId":    "call_" + status,
					"status":        status,
				},
			}))
			if len(terminal) != 2 ||
				terminal[0].TaskNotification == nil ||
				terminal[0].TaskNotification.TaskID != "call_"+status ||
				terminal[0].TaskNotification.ToolUseID != "call_"+status ||
				terminal[0].TaskNotification.Status != status ||
				terminal[1].ToolProgress == nil {
				t.Fatalf("terminal %q update produced %+v, want normalized task notification plus tool progress", status, terminal)
			}
		})
	}
}

func TestParseLine_OpenCodeNonTaskToolHasNoTaskLifecycle(t *testing.T) {
	t.Parallel()

	p, _, _ := newPostHandshakeProtocol(t)
	msgs := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update": map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    "call_read",
			"kind":          "read",
			"title":         "Read README",
			"status":        "in_progress",
			"rawInput":      map[string]any{"filePath": "README.md"},
		},
	}))
	if len(msgs) != 1 || msgs[0].ToolProgress == nil {
		t.Fatalf("non-task update produced %+v, want one tool progress message", msgs)
	}
	if msgs[0].TaskStarted != nil || msgs[0].TaskProgress != nil || msgs[0].TaskNotification != nil {
		t.Fatalf("non-task update produced task lifecycle: %+v", msgs[0])
	}
}

func TestParseLine_PromptEndTurnIsSuccess(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	msgs := mustParse(t, p, responseLine(t, promptID, map[string]any{"stopReason": "end_turn"}))
	if len(msgs) != 1 || msgs[0].Result == nil {
		t.Fatalf("end_turn produced %+v, want one result message", msgs)
	}
	if !msgs[0].Result.IsSuccess() {
		t.Fatalf("end_turn result = %+v, want success", msgs[0].Result)
	}
}

func TestParseLine_PromptNonSuccessStopReasons(t *testing.T) {
	cases := []struct {
		stopReason string
		detailHas  string
	}{
		{"refusal", "refused"},
		{"cancelled", "cancelled"},
		{"max_tokens", "without completing"},
		{"max_turn_requests", "without completing"},
		{"", "no stop reason"},
	}
	for _, tc := range cases {
		t.Run(tc.stopReason, func(t *testing.T) {
			p, _, promptID := newPostHandshakeProtocol(t)
			msgs := mustParse(t, p, responseLine(t, promptID, map[string]any{"stopReason": tc.stopReason}))
			if len(msgs) != 1 || msgs[0].Result == nil {
				t.Fatalf("stop reason %q produced %+v, want one result", tc.stopReason, msgs)
			}
			r := msgs[0].Result
			if r.IsSuccess() || !r.IsError {
				t.Fatalf("stop reason %q result = %+v, want non-success error", tc.stopReason, r)
			}
			if !strings.Contains(r.Result, tc.detailHas) {
				t.Fatalf("stop reason %q detail = %q, want substring %q", tc.stopReason, r.Result, tc.detailHas)
			}
		})
	}
}

func TestParseLine_PromptErrorResponseIsNonSuccess(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	msgs := mustParse(t, p, errorResponseLine(t, promptID, -32000, "model unavailable"))
	if len(msgs) != 1 || msgs[0].Result == nil || msgs[0].Result.IsSuccess() {
		t.Fatalf("prompt error produced %+v, want non-success result", msgs)
	}
	if !strings.Contains(msgs[0].Result.Result, "prompt failed") {
		t.Fatalf("prompt error detail = %q, want 'prompt failed'", msgs[0].Result.Result)
	}
}

func TestParseLine_AssistantErrorSurfacesRetryMetadata(t *testing.T) {
	p, _, _ := newPostHandshakeProtocol(t)
	msgs := mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update": map[string]any{
			"sessionUpdate": "assistant_message",
			"error": map[string]any{
				"name": "APIError",
				"data": map[string]any{
					"message":     "backend overloaded",
					"statusCode":  503,
					"isRetryable": true,
				},
			},
		},
	}))
	if len(msgs) != 1 || msgs[0].Result == nil || msgs[0].Result.Failure == nil {
		t.Fatalf("assistant error produced %+v, want one structured terminal error", msgs)
	}
	got := msgs[0].Result
	if got.Result != "backend overloaded" ||
		got.Failure.StatusCode != 503 ||
		got.Failure.Retryable == nil ||
		!*got.Failure.Retryable {
		t.Errorf("assistant error result = %+v, want surfaced retry metadata", got)
	}
}

func TestParseLine_MalformedPromptResultIsNonSuccess(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	// A result that is a JSON array rather than the expected object.
	line := []byte(`{"jsonrpc":"2.0","id":102,"result":[1,2,3]}`)
	_ = promptID
	msgs := mustParse(t, p, line)
	if len(msgs) != 1 || msgs[0].Result == nil || msgs[0].Result.IsSuccess() {
		t.Fatalf("malformed prompt result produced %+v, want non-success result", msgs)
	}
	if !strings.Contains(msgs[0].Result.Result, "malformed") {
		t.Fatalf("malformed result detail = %q, want 'malformed'", msgs[0].Result.Result)
	}
}

// TestParseLine_PromptErrorRedactsSecrets proves an ACP prompt error result
// surfaces a useful diagnostic while redacting a credential embedded in the
// JSON-RPC error message and omitting the structured `data` member entirely —
// the usual home for provider config or credentials. Task 4 requires ACP error
// results to omit or redact OpenCode auth tokens, API keys, provider config
// contents, and environment-derived secrets.
func TestParseLine_PromptErrorRedactsSecrets(t *testing.T) {
	withEnv(t, []string{"PATH=/usr/bin"})
	p, _, _ := newPostHandshakeProtocol(t)
	line := []byte(`{"jsonrpc":"2.0","id":102,"error":{"code":-32000,"message":"auth failed for apiKey sk-ant-PROMPTMSGLEAK111222333","data":{"provider":{"anthropic":{"options":{"apiKey":"sk-ant-PROMPTDATALEAK444555666"}}}}}}`)
	msgs := mustParse(t, p, line)
	if len(msgs) != 1 || msgs[0].Result == nil || msgs[0].Result.IsSuccess() {
		t.Fatalf("prompt error produced %+v, want non-success result", msgs)
	}
	detail := msgs[0].Result.Result
	for _, leak := range []string{"sk-ant-PROMPTMSGLEAK111222333", "sk-ant-PROMPTDATALEAK444555666", "options", "anthropic"} {
		if strings.Contains(detail, leak) {
			t.Fatalf("prompt error detail leaked %q: %q", leak, detail)
		}
	}
	if !strings.Contains(detail, "prompt failed") {
		t.Fatalf("prompt error detail = %q, want 'prompt failed' framing", detail)
	}
}

// TestParseLine_PromptErrorOmitsProviderConfigInMessage proves provider config
// is omitted from a terminal ACP error even when the config object is embedded
// in the JSON-RPC error MESSAGE itself rather than the structured `data` member.
// rpcErrorDetail drops `data`, but a config blob inside the human-readable
// message must still be omitted by the central sanitizer so neither the config
// structure nor its contents reach a surfaced or persisted diagnostic.
func TestParseLine_PromptErrorOmitsProviderConfigInMessage(t *testing.T) {
	withEnv(t, []string{"PATH=/usr/bin"})
	p, _, _ := newPostHandshakeProtocol(t)
	line := []byte(`{"jsonrpc":"2.0","id":102,"error":{"code":-32000,"message":"auth rejected for config {\"provider\":{\"anthropic\":{\"options\":{\"apiKey\":\"sk-ant-MSGCONFIGLEAK1234567890\"}}}}"}}`)
	msgs := mustParse(t, p, line)
	if len(msgs) != 1 || msgs[0].Result == nil || msgs[0].Result.IsSuccess() {
		t.Fatalf("prompt error produced %+v, want non-success result", msgs)
	}
	detail := msgs[0].Result.Result
	for _, leak := range []string{"sk-ant-MSGCONFIGLEAK1234567890", `"provider"`, `"options"`, "anthropic", "{", "}"} {
		if strings.Contains(detail, leak) {
			t.Fatalf("prompt error detail surfaced provider config content %q: %q", leak, detail)
		}
	}
	if !strings.Contains(detail, "prompt failed") {
		t.Fatalf("prompt error detail = %q, want 'prompt failed' framing", detail)
	}
}

// TestHandshake_InitializeErrorRedactsSecrets proves the handshake error
// returned to the session layer (and thus to startup logs) redacts credentials
// carried in an initialize error response. Task 3 requires ACP launch errors to
// omit or redact OpenCode credentials and provider config contents.
func TestHandshake_InitializeErrorRedactsSecrets(t *testing.T) {
	withEnv(t, []string{"PATH=/usr/bin"})
	h := newHandshakeHarness(t, llm.ProtocolOpts{WorkDir: "/w", InitialPrompt: "p"})
	errCh := make(chan error, 1)
	go func() { errCh <- h.p.Handshake(context.Background()) }()

	initReq := h.nextRequest(t)
	h.feed(t, errorResponseLine(t, mustID(t, initReq.ID), -32000, "auth rejected: apiKey=sk-ant-INITHANDSHAKELEAK0987654321"))

	err := <-errCh
	if err == nil {
		t.Fatal("Handshake() = nil, want initialize failure")
	}
	if strings.Contains(err.Error(), "sk-ant-INITHANDSHAKELEAK0987654321") {
		t.Fatalf("handshake error leaked credential: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "initialize failed") {
		t.Fatalf("handshake error = %q, want 'initialize failed' framing", err.Error())
	}
}

// TestParseLine_MalformedStdoutFailsClosed proves that corrupt protocol output
// on stdout — whether it is non-JSON or valid JSON that is not a JSON-RPC
// envelope — fails the tracer closed with a diagnostic, as Task 4 requires.
// OpenCode keeps its logs off stdout, so any unparseable line is genuine
// protocol corruption and must not be silently dropped.
func TestParseLine_MalformedStdoutFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		line []byte
	}{
		{"not json", []byte("not json at all")},
		{"leaked log line", []byte("INFO some opencode log line leaked to stdout")},
		{"truncated json", []byte("{ broken json")},
		{"valid json without id or method", []byte(`{"jsonrpc":"2.0","params":{"x":1}}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _ := newPostHandshakeProtocol(t)
			msgs := mustParse(t, p, tc.line)
			if len(msgs) != 1 || msgs[0].Result == nil || msgs[0].Result.IsSuccess() {
				t.Fatalf("malformed line %q produced %+v, want a non-success terminal result", tc.line, msgs)
			}
			if !strings.Contains(msgs[0].Result.Result, "malformed") {
				t.Fatalf("malformed line %q detail = %q, want a 'malformed' diagnostic", tc.line, msgs[0].Result.Result)
			}
		})
	}
}

// TestParseLine_BlankLineSkipped proves blank framing lines are benign: an empty
// or whitespace-only line is not protocol corruption and must not fail closed.
func TestParseLine_BlankLineSkipped(t *testing.T) {
	p, _, _ := newPostHandshakeProtocol(t)
	for _, line := range [][]byte{[]byte(""), []byte("   "), []byte("\t")} {
		if msgs := mustParse(t, p, line); len(msgs) != 0 {
			t.Fatalf("blank line %q produced %+v, want no messages", line, msgs)
		}
	}
}

// TestParseLine_MalformedStdoutAfterResultIsIgnored proves trailing garbage on
// stdout after a terminal result cannot corrupt completion: a malformed line
// arriving after a success result is ignored rather than flipping the session
// to a failure.
func TestParseLine_MalformedStdoutAfterResultIsIgnored(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	msgs := mustParse(t, p, responseLine(t, promptID, map[string]any{"stopReason": "end_turn"}))
	if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsSuccess() {
		t.Fatalf("end_turn produced %+v, want a success result", msgs)
	}
	if trailing := mustParse(t, p, []byte("garbage trailing line")); len(trailing) != 0 {
		t.Fatalf("malformed line after terminal result produced %+v, want no messages", trailing)
	}
}

// TestPromptSuccessRequiresMarkerToCompletePhase proves the provider's success
// result cannot, by itself, satisfy phase completion: the standard
// marker-backed termination classifier still gates on the phase_complete
// marker, exactly as for existing providers.
func TestPromptSuccessRequiresMarkerToCompletePhase(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	msgs := mustParse(t, p, responseLine(t, promptID, map[string]any{"stopReason": "end_turn"}))
	result := msgs[0].Result

	if cls := llm.ClassifyTermination(llm.TerminationInputs{Result: result, PhaseCompleteExists: false}); cls == llm.TermCompleted {
		t.Fatal("ACP end_turn success classified as Completed without the phase_complete marker")
	}
	if cls := llm.ClassifyTermination(llm.TerminationInputs{Result: result, PhaseCompleteExists: true}); cls != llm.TermCompleted {
		t.Fatalf("ACP end_turn success with marker classified as %v, want Completed", cls)
	}
}

// --- fail-closed control tests (Task 5) ---

func TestParseLine_FailsClosedOnUnsupportedServerRequests(t *testing.T) {
	// session/request_permission and the client filesystem surface
	// (fs/read_text_file, fs/write_text_file) are now supported (see
	// control_test.go and clientfs_test.go); the surfaces below remain
	// unsupported because Agentico declared no client terminal capability, and any
	// other method is treated as corruption.
	cases := []struct {
		name   string
		method string
	}{
		{"terminal create", "terminal/create"},
		{"terminal output", "terminal/output"},
		{"unknown method", "session/some_future_thing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, buf, _ := newPostHandshakeProtocol(t)
			const reqID = 777
			msgs := mustParse(t, p, serverRequestLine(t, reqID, tc.method, map[string]any{"sessionId": "ses_x"}))

			// A terminal non-success result is produced, naming the surface.
			if len(msgs) != 1 || msgs[0].Result == nil || msgs[0].Result.IsSuccess() {
				t.Fatalf("%s produced %+v, want non-success result", tc.method, msgs)
			}
			if !strings.Contains(msgs[0].Result.Result, tc.method) {
				t.Fatalf("fail-closed detail = %q, want to name the surface %q", msgs[0].Result.Result, tc.method)
			}
			if !strings.Contains(msgs[0].Result.Result, "does not host") {
				t.Fatalf("fail-closed detail = %q, want to document the unsupported-capability limitation", msgs[0].Result.Result)
			}

			// A JSON-RPC error response is written back so OpenCode is not
			// left waiting on the request.
			var resp struct {
				ID    int `json:"id"`
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(buf.lastLine(t), &resp); err != nil {
				t.Fatalf("fail-closed response is not JSON-RPC: %v (raw %q)", err, buf.String())
			}
			if resp.ID != reqID {
				t.Fatalf("fail-closed response id = %d, want %d", resp.ID, reqID)
			}
			if resp.Error == nil {
				t.Fatalf("fail-closed response carried no error object: %q", buf.String())
			}
		})
	}
}

// TestFailClosedControlNeverSatisfiesPhase proves a failed control event yields
// a non-success result that the marker classifier never treats as Completed —
// whether or not a phase_complete marker is present on disk. Task 5 requires
// that a failed control event cannot satisfy marker-backed completion, so the
// marker-present case is asserted explicitly, not assumed.
func TestFailClosedControlNeverSatisfiesPhase(t *testing.T) {
	p, _, _ := newPostHandshakeProtocol(t)
	msgs := mustParse(t, p, serverRequestLine(t, 5, "terminal/create", map[string]any{"sessionId": "ses_x"}))
	result := msgs[0].Result
	if result.IsSuccess() {
		t.Fatal("fail-closed result reported success")
	}
	// Without a marker the fail-closed error is plainly non-success.
	if cls := llm.ClassifyTermination(llm.TerminationInputs{Result: result, PhaseCompleteExists: false}); cls == llm.TermCompleted {
		t.Fatal("fail-closed control event classified as Completed (no marker)")
	}
	// And even if a phase_complete marker happens to exist on disk, the error
	// result must remain non-successful — the marker cannot launder a failed
	// control event into a clean completion.
	if cls := llm.ClassifyTermination(llm.TerminationInputs{Result: result, PhaseCompleteExists: true}); cls != llm.TermErrored {
		t.Fatalf("fail-closed control event with marker classified as %v, want Errored", cls)
	}
}

// TestParseLine_FailClosedControlIsStickyOverLaterPromptSuccess proves the exact
// ordering the Phase 1 contract forbids: a fail-closed control event emits a
// terminal error, and a LATER prompt end_turn response — which OpenCode may
// still send after we reject the control request — must NOT be converted into a
// success. The first terminal result is sticky, so the session can never observe
// a success after the fail-closed error, and a phase_complete marker present on
// disk cannot launder the rejected turn into a clean completion (Task 5).
func TestParseLine_FailClosedControlIsStickyOverLaterPromptSuccess(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)

	// 1. An unsupported control request fails closed with a terminal error.
	failMsgs := mustParse(t, p, serverRequestLine(t, 777, "terminal/create", map[string]any{"sessionId": "ses_x"}))
	if len(failMsgs) != 1 || failMsgs[0].Result == nil || failMsgs[0].Result.IsSuccess() {
		t.Fatalf("control request produced %+v, want a non-success terminal result", failMsgs)
	}

	// 2. The original prompt completes with end_turn AFTER the control rejection.
	//    No success may be emitted — the fail-closed error already sealed the turn.
	later := mustParse(t, p, responseLine(t, promptID, map[string]any{"stopReason": "end_turn"}))
	if len(later) != 0 {
		t.Fatalf("prompt end_turn after fail-closed control produced %+v, want no messages (no success may follow the terminal error)", later)
	}

	// Even with a phase_complete marker on disk, the only terminal result the
	// session ever saw is the fail-closed error, which never classifies as
	// Completed.
	if cls := llm.ClassifyTermination(llm.TerminationInputs{Result: failMsgs[0].Result, PhaseCompleteExists: true}); cls != llm.TermErrored {
		t.Fatalf("fail-closed control event with marker classified as %v, want Errored", cls)
	}
}

// TestParseLine_TerminalResultIsStickyOverLaterControlRequest proves the
// symmetric guarantee: once a terminal result has been emitted (here a clean
// prompt success), a later unsupported control request must NOT emit a second
// terminal result that could flip the sealed outcome — yet OpenCode must still
// receive its JSON-RPC error response so it is never left waiting on the request.
func TestParseLine_TerminalResultIsStickyOverLaterControlRequest(t *testing.T) {
	p, buf, promptID := newPostHandshakeProtocol(t)

	// 1. The prompt completes cleanly; this is the sole terminal result.
	success := mustParse(t, p, responseLine(t, promptID, map[string]any{"stopReason": "end_turn"}))
	if len(success) != 1 || success[0].Result == nil || !success[0].Result.IsSuccess() {
		t.Fatalf("end_turn produced %+v, want a success result", success)
	}

	// 2. A late unsupported control request emits no further terminal SDK message...
	const reqID = 888
	later := mustParse(t, p, serverRequestLine(t, reqID, "terminal/create", map[string]any{"sessionId": "ses_x"}))
	if len(later) != 0 {
		t.Fatalf("control request after terminal success produced %+v, want no messages", later)
	}

	// ...but OpenCode still receives a JSON-RPC error response for the request.
	var resp struct {
		ID    int `json:"id"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.lastLine(t), &resp); err != nil {
		t.Fatalf("late control response is not JSON-RPC: %v (raw %q)", err, buf.String())
	}
	if resp.ID != reqID || resp.Error == nil {
		t.Fatalf("late control response = %+v, want a JSON-RPC error for id %d", resp, reqID)
	}
}

// --- lifecycle method coverage (parity) ---

// TestLifecycleMethodsBeforeHandshake covers the lifecycle methods on a fresh
// protocol that has not yet established a session. SessionID/TranscriptPath are
// empty until a session exists, and Interrupt returns ErrNotSupported so the
// session layer's SIGINT fallback applies in that pre-session window. The
// no-op hook callback and Close round out the surface.
func TestLifecycleMethodsBeforeHandshake(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{})
	if p.SessionID() != "" {
		t.Error("SessionID() should be empty before a session is established")
	}
	if p.TranscriptPath() != "" {
		t.Error("TranscriptPath() should be empty (OpenCode exposes none over ACP)")
	}
	if err := p.Interrupt(); err != llm.ErrNotSupported {
		t.Errorf("Interrupt() before a session = %v, want ErrNotSupported (SIGINT fallback)", err)
	}
	if err := p.RespondToHook("1"); err != nil {
		t.Errorf("RespondToHook() = %v, want nil", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// --- helpers ---

func mustParse(t *testing.T, p *Protocol, line []byte) []llm.SDKMessage {
	t.Helper()
	msgs, err := p.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine(%s) error: %v", line, err)
	}
	return msgs
}

func assertAssistantText(t *testing.T, msgs []llm.SDKMessage, want string) {
	t.Helper()
	if len(msgs) != 1 || msgs[0].Assistant == nil {
		t.Fatalf("got %+v, want one assistant message", msgs)
	}
	content := msgs[0].Assistant.Message.Content
	if len(content) != 1 || !content[0].IsText() || content[0].Text != want {
		t.Fatalf("assistant text = %+v, want text %q", content, want)
	}
	if msgs[0].Subtype != "partial" {
		t.Fatalf("assistant subtype = %q, want partial", msgs[0].Subtype)
	}
}

// syncBuffer is a goroutine-safe io.Writer capturing protocol output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// lastLine returns the last non-empty newline-delimited line written.
func (b *syncBuffer) lastLine(t *testing.T) []byte {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		t.Fatalf("no output captured")
	}
	return []byte(lines[len(lines)-1])
}
