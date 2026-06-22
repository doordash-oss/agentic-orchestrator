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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// nextID is an atomic counter for JSON-RPC request IDs, shared across protocol
// instances so concurrent OpenCode sessions never collide on an id.
var nextID atomic.Int64

// requestedProtocolVersion is the ACP protocol version Agentico negotiates.
// OpenCode 1.17.9 advertises and accepts protocol version 1.
const requestedProtocolVersion = 1

// jsonRPCMethodNotSupported is the JSON-RPC error code used when Agentico
// declines an agent->client request it does not implement during Phase 1.
const jsonRPCMethodNotSupported = -32601

// Protocol implements llm.Protocol for the OpenCode ACP (Agent Client Protocol)
// JSON-RPC stdio transport. One instance is created per session.
type Protocol struct {
	opts  llm.ProtocolOpts
	model string // backend "provider/model" with the routing prefix stripped

	stdin io.Writer
	mu    sync.Mutex

	// Handshake state. initDone closes when the initialize response arrives;
	// sessionReady closes when session/new returns a session id.
	initDone     chan struct{}
	sessionReady chan struct{}

	// Request ids for the handshake exchanges, used to route responses.
	initID       int
	sessionNewID int
	promptID     int

	negotiatedVersion int
	acpSessionID      string
	handshakeErr      error

	assistantBuf  strings.Builder
	resultEmitted bool

	// Control-request bookkeeping. pendingPerms maps a permission request id to
	// the allow/reject option ids the user's approve/deny decision selects;
	// pendingQuestionOpts maps a structured-question request id to its answer
	// label -> optionId map so a chosen answer resolves to the right ACP option.
	// Both are keyed by the string request id surfaced to the session layer.
	pendingPerms        map[string]permissionOptions
	pendingQuestionOpts map[string]map[string]string

	// formatRetryCount tracks reformat-reminder turns sent for a plain-text
	// question that lacked the required numbered options; synthSeq makes
	// synthetic AskUserQuestion request ids unique without a wall-clock source.
	formatRetryCount int
	synthSeq         int

	logFunc func(string, ...interface{})
}

// permissionOptions records the option ids a permission request offers, so an
// approve/deny decision from the session layer maps to a concrete ACP outcome.
type permissionOptions struct {
	allowID  string
	rejectID string
}

// NewProtocol creates a new OpenCode ACP protocol handler.
func NewProtocol(opts llm.ProtocolOpts) *Protocol {
	return &Protocol{
		opts:  opts,
		model: BackendModel(opts.Model),
	}
}

// SetLogFunc sets a logging function for debug output.
func (p *Protocol) SetLogFunc(f func(string, ...interface{})) { p.logFunc = f }

func (p *Protocol) logDebug(format string, args ...interface{}) {
	if p.logFunc != nil {
		p.logFunc(format, args...)
	}
}

func (p *Protocol) SetStdin(w io.Writer) {
	p.mu.Lock()
	p.stdin = w
	p.mu.Unlock()
}

// Handshake performs the ACP bootstrap:
//  1. initialize — negotiate the protocol version.
//  2. session/new — create a session rooted at the resolved work directory.
//  3. session/prompt — deliver the rendered Agentico phase prompt as the first
//     user turn. The prompt response arrives asynchronously and is surfaced as
//     a terminal result by ParseLine.
func (p *Protocol) Handshake(ctx context.Context) error {
	if err := p.sendInitialize(); err != nil {
		return err
	}
	select {
	case <-p.initDone:
	case <-ctx.Done():
		return fmt.Errorf("opencode initialize timeout: %w", ctx.Err())
	}
	if err := p.handshakeError(); err != nil {
		return err
	}
	if err := p.checkProtocolVersion(); err != nil {
		return err
	}

	if err := p.sendSessionNew(); err != nil {
		return err
	}
	select {
	case <-p.sessionReady:
	case <-ctx.Done():
		return fmt.Errorf("opencode session/new timeout: %w", ctx.Err())
	}
	if err := p.handshakeError(); err != nil {
		return err
	}

	return p.sendPrompt(p.opts.InitialPrompt)
}

func (p *Protocol) handshakeError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handshakeErr
}

// checkProtocolVersion verifies OpenCode negotiated a compatible ACP version.
// OpenCode echoes the version it will speak; a higher version than requested,
// or zero, means the tracer cannot safely proceed.
func (p *Protocol) checkProtocolVersion() error {
	p.mu.Lock()
	v := p.negotiatedVersion
	p.mu.Unlock()
	if v == 0 {
		return fmt.Errorf("opencode did not negotiate an ACP protocol version")
	}
	if v > requestedProtocolVersion {
		return fmt.Errorf("opencode requires ACP protocol version %d, which this tracer does not support (supports %d)", v, requestedProtocolVersion)
	}
	return nil
}

// --- outbound requests ---

func (p *Protocol) sendInitialize() error {
	id := int(nextID.Add(1))
	p.mu.Lock()
	p.initDone = make(chan struct{})
	p.sessionReady = make(chan struct{})
	p.initID = id
	p.mu.Unlock()

	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "initialize",
		Params: InitializeParams{
			ProtocolVersion: requestedProtocolVersion,
			ClientInfo: &ClientInfo{
				Name:    "agentic",
				Version: "0.1.0",
			},
			ClientCapabilities: ClientCapabilities{
				FS:       FSCapability{ReadTextFile: false, WriteTextFile: false},
				Terminal: false,
			},
		},
	}
	if err := p.writeJSON(req); err != nil {
		return fmt.Errorf("sending initialize request: %w", err)
	}
	return nil
}

func (p *Protocol) sendSessionNew() error {
	id := int(nextID.Add(1))
	p.mu.Lock()
	p.sessionNewID = id
	p.mu.Unlock()

	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "session/new",
		Params: SessionNewParams{
			Cwd:        p.opts.WorkDir,
			MCPServers: []interface{}{},
		},
	}
	if err := p.writeJSON(req); err != nil {
		return fmt.Errorf("sending session/new request: %w", err)
	}
	return nil
}

func (p *Protocol) sendPrompt(text string) error {
	id := int(nextID.Add(1))
	p.mu.Lock()
	p.promptID = id
	sessionID := p.acpSessionID
	// Each prompt is a fresh turn: reset the accumulated assistant text so the
	// next turn's question detection (and streamed partial) reflects only that
	// turn, not text carried over from a prior answered question.
	p.assistantBuf.Reset()
	p.mu.Unlock()

	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "session/prompt",
		Params: PromptParams{
			SessionID: sessionID,
			Prompt:    []ContentBlock{{Type: "text", Text: text}},
		},
	}
	if err := p.writeJSON(req); err != nil {
		return fmt.Errorf("sending session/prompt request: %w", err)
	}
	return nil
}

func (p *Protocol) writeJSON(v interface{}) error {
	p.mu.Lock()
	w := p.stdin
	p.mu.Unlock()
	if w == nil {
		return fmt.Errorf("opencode protocol: stdin not set")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// ParseLine translates one JSON-RPC line from OpenCode's stdout into SDKMessages.
// Returns a nil slice for lines that produce no Agentico message (handshake
// responses, ignored notifications, blank framing lines). OpenCode's stdout is
// contractually clean newline-delimited JSON-RPC — its logs go to stderr — so a
// line that is not a valid JSON-RPC envelope is genuine protocol corruption and
// fails the tracer closed with a terminal non-success result rather than being
// silently dropped.
func (p *Protocol) ParseLine(line []byte) ([]llm.SDKMessage, error) {
	// Blank framing lines carry no message and are not corruption; skip them
	// before treating unparseable content as a protocol violation.
	if len(bytes.TrimSpace(line)) == 0 {
		return nil, nil
	}

	var env inboundEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return p.malformedStdout(fmt.Sprintf("unparseable JSON-RPC line: %v", err))
	}

	hasID := len(env.ID) > 0 && string(env.ID) != "null"

	// Response to one of our requests: has id, no method.
	if hasID && env.Method == "" {
		if msg, ok := p.handleResponse(env); ok {
			return []llm.SDKMessage{msg}, nil
		}
		return nil, nil
	}

	// Server-initiated (agent->client) request: has id and method. Phase 2
	// supports the permission/question control surface (session/request_permission)
	// and surfaces it through Agentico's shared decision flow; client filesystem
	// and terminal requests and any unknown method are capabilities Agentico did
	// not declare, so they still fail closed. The JSON-RPC reply is always written
	// so OpenCode can unblock, and only the first terminal result reaches the
	// session.
	if hasID && env.Method != "" {
		if env.Method == requestPermissionMethod {
			if msg, ok := p.handleRequestPermission(env.ID, env.Params); ok {
				return []llm.SDKMessage{msg}, nil
			}
			return nil, nil
		}
		if msg, ok := p.failClosed(env.ID, env.Method); ok {
			return []llm.SDKMessage{msg}, nil
		}
		return nil, nil
	}

	// Notification: method only.
	if env.Method != "" {
		if msg, ok := p.parseNotification(env.Method, env.Params); ok {
			return []llm.SDKMessage{msg}, nil
		}
		return nil, nil
	}

	// Well-formed JSON that is not a valid JSON-RPC envelope (neither id nor
	// method). Like unparseable output, this is a corrupt protocol stream and
	// must fail closed.
	return p.malformedStdout("JSON-RPC line carried neither an id nor a method")
}

// malformedStdout produces a terminal non-success result for corrupt protocol
// stdout. OpenCode's stdout is contractually clean JSON-RPC, so malformed output
// is a protocol violation that fails the tracer closed. Once a terminal result
// has already been emitted the corrupt line is ignored, so trailing garbage can
// never flip a completed turn's status.
func (p *Protocol) malformedStdout(detail string) ([]llm.SDKMessage, error) {
	msg, ok := p.terminalError(malformedStdoutDiagnostic(detail))
	if !ok {
		p.logDebug("[opencode] ignoring malformed stdout after terminal result: %s", detail)
		return nil, nil
	}
	return []llm.SDKMessage{msg}, nil
}

// malformedStdoutDiagnostic returns the user-facing explanation for a corrupt
// protocol stdout line.
func malformedStdoutDiagnostic(detail string) string {
	return fmt.Sprintf(
		"OpenCode emitted malformed JSON-RPC on stdout (%s). The Phase 1 tracer requires clean newline-delimited JSON-RPC on stdout; corrupt protocol output is treated as a failed session. Failing closed.",
		detail,
	)
}

// handleResponse routes a JSON-RPC response to the request that issued it. Only
// the prompt response yields an Agentico message (the terminal result); the
// handshake responses unblock Handshake instead.
func (p *Protocol) handleResponse(env inboundEnvelope) (llm.SDKMessage, bool) {
	id, err := parseID(env.ID)
	if err != nil {
		p.logDebug("[opencode] response with unparseable id %q", string(env.ID))
		return llm.SDKMessage{}, false
	}

	p.mu.Lock()
	initID, sessionNewID, promptID := p.initID, p.sessionNewID, p.promptID
	p.mu.Unlock()

	hasErr := len(env.Error) > 0 && string(env.Error) != "null"

	switch id {
	case initID:
		if hasErr {
			p.failHandshake(fmt.Errorf("opencode initialize failed: %s", rpcErrorDetail(env.Error)), p.initDone)
			return llm.SDKMessage{}, false
		}
		var res InitializeResult
		if jerr := json.Unmarshal(env.Result, &res); jerr != nil {
			p.failHandshake(fmt.Errorf("parsing initialize result: %s", sanitizeDiagnostic(jerr.Error())), p.initDone)
			return llm.SDKMessage{}, false
		}
		p.mu.Lock()
		p.negotiatedVersion = res.ProtocolVersion
		p.mu.Unlock()
		p.closeOnce(p.initDone)
		return llm.SDKMessage{}, false

	case sessionNewID:
		if hasErr {
			p.failHandshake(fmt.Errorf("opencode session/new failed: %s", rpcErrorDetail(env.Error)), p.sessionReady)
			return llm.SDKMessage{}, false
		}
		var res SessionNewResult
		if jerr := json.Unmarshal(env.Result, &res); jerr != nil {
			p.failHandshake(fmt.Errorf("parsing session/new result: %s", sanitizeDiagnostic(jerr.Error())), p.sessionReady)
			return llm.SDKMessage{}, false
		}
		if strings.TrimSpace(res.SessionID) == "" {
			p.failHandshake(fmt.Errorf("opencode session/new returned no session id"), p.sessionReady)
			return llm.SDKMessage{}, false
		}
		p.mu.Lock()
		p.acpSessionID = res.SessionID
		p.mu.Unlock()
		p.closeOnce(p.sessionReady)
		return llm.SDKMessage{}, false

	case promptID:
		return p.handlePromptResponse(env, hasErr)

	default:
		p.logDebug("[opencode] unhandled response for id %d", id)
		return llm.SDKMessage{}, false
	}
}

// handlePromptResponse converts the session/prompt response into a terminal
// result. A clean end_turn is the only success; every other stop reason, an
// error response, or a malformed result is a non-success terminal state.
func (p *Protocol) handlePromptResponse(env inboundEnvelope, hasErr bool) (llm.SDKMessage, bool) {
	if hasErr {
		return p.terminalError(fmt.Sprintf("OpenCode prompt failed: %s", rpcErrorDetail(env.Error)))
	}
	var res PromptResult
	if jerr := json.Unmarshal(env.Result, &res); jerr != nil {
		return p.terminalError(fmt.Sprintf("malformed OpenCode prompt result: %v", jerr))
	}
	switch res.StopReason {
	case StopReasonEndTurn:
		// A clean end_turn is a completion only when the agent's final text is
		// not really a user-facing question. When it is, surface a question pause
		// (synthetic AskUserQuestion or a reformat-reminder turn) instead of a
		// success result, so a pending question can never be mistaken for phase
		// completion.
		p.mu.Lock()
		lastText := p.assistantBuf.String()
		p.mu.Unlock()
		if msg, emit, done := p.maybeSynthesizeQuestion(lastText); done {
			if emit {
				return msg, true
			}
			// A reformat-reminder turn was sent; no message yet — the reformatted
			// answer arrives on the follow-up turn's response.
			return llm.SDKMessage{}, false
		}
		return p.terminalSuccess()
	case StopReasonRefusal:
		return p.terminalError("OpenCode refused to complete the request")
	case StopReasonCancelled:
		return p.terminalError("OpenCode prompt was cancelled")
	case "":
		return p.terminalError("OpenCode prompt result carried no stop reason")
	default:
		// max_tokens, max_turn_requests, and any future/unknown stop reason are
		// not a clean completion for the tracer.
		return p.terminalError(fmt.Sprintf("OpenCode prompt ended without completing (stop reason %q)", res.StopReason))
	}
}

// parseNotification handles agent->client notifications (session/update). It
// normalizes assistant text, thoughts, and tool/terminal progress; everything
// else is ignored.
func (p *Protocol) parseNotification(method string, params json.RawMessage) (llm.SDKMessage, bool) {
	if method != "session/update" {
		p.logDebug("[opencode] ignoring notification %s", method)
		return llm.SDKMessage{}, false
	}
	var su SessionUpdateParams
	if err := json.Unmarshal(params, &su); err != nil {
		p.logDebug("[opencode] failed to parse session/update: %v", err)
		return llm.SDKMessage{}, false
	}

	switch su.Update.SessionUpdate {
	case UpdateAgentMessageChunk:
		text := ""
		if su.Update.Content != nil {
			text = su.Update.Content.Text
		}
		if text == "" {
			return llm.SDKMessage{}, false
		}
		p.mu.Lock()
		p.assistantBuf.WriteString(text)
		accumulated := p.assistantBuf.String()
		p.mu.Unlock()
		return assistantPartial(llm.ContentBlock{Type: "text", Text: accumulated}), true

	case UpdateAgentThoughtChunk:
		text := ""
		if su.Update.Content != nil {
			text = su.Update.Content.Text
		}
		if text == "" {
			return llm.SDKMessage{}, false
		}
		return assistantPartial(llm.ContentBlock{Type: "thinking", Thinking: text}), true

	case UpdateToolCall, UpdateToolCallUpdate:
		name := su.Update.Title
		if name == "" {
			name = su.Update.Kind
		}
		return llm.SDKMessage{
			Type: "tool_progress",
			ToolProgress: &llm.ToolProgressMessage{
				Type:      "tool_progress",
				ToolUseID: su.Update.ToolCallID,
				ToolName:  name,
				Data:      su.Update.Status,
			},
		}, true

	default:
		return llm.SDKMessage{}, false
	}
}

// failClosed responds to an unsupported agent->client request with a JSON-RPC
// error (so OpenCode is never left waiting) and produces a terminal non-success
// result naming the unsupported surface. The tracer supports the permission and
// question control surface (session/request_permission); it does NOT host client
// filesystem (fs/*) or client terminal (terminal/*) capabilities — Agentico
// declared neither — and treats any other unknown method as corruption. The
// JSON-RPC error is always written so OpenCode can unblock, but the bool is false
// when a terminal result was already emitted, in which case the caller suppresses
// the duplicate terminal message: the first terminal result wins.
func (p *Protocol) failClosed(rawID json.RawMessage, method string) (llm.SDKMessage, bool) {
	_ = p.writeJSON(ErrorResponse{
		JSONRPC: "2.0",
		ID:      rawID,
		Error: RPCError{
			Code:    jsonRPCMethodNotSupported,
			Message: fmt.Sprintf("%s is not supported by the OpenCode tracer", method),
		},
	})
	return p.terminalError(unsupportedSurfaceDiagnostic(method))
}

// unsupportedSurfaceDiagnostic returns the user-facing explanation for a
// fail-closed control path, documenting the tracer's supported surface.
func unsupportedSurfaceDiagnostic(method string) string {
	return fmt.Sprintf(
		"OpenCode requested an unsupported ACP control path (%s). The tracer supports initialize, session/new, session/prompt, streamed session/update events, and the session/request_permission permission/question surface; it does not host client filesystem or client terminal capabilities, and treats any other method as corruption. Failing closed.",
		method,
	)
}

// --- terminal result helpers ---

// markTerminal seals the session's outcome on the first terminal result and
// reports whether this call is that first result. Once a terminal result has
// been emitted the outcome is fixed: a later prompt success can never overturn a
// fail-closed control error or a malformed-stdout failure, and trailing output
// can never undo a clean completion. Callers that observe false must suppress
// their terminal message so only the first terminal result reaches the session.
func (p *Protocol) markTerminal() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.resultEmitted {
		return false
	}
	p.resultEmitted = true
	return true
}

// terminalSuccess returns the success result for a clean prompt completion. The
// bool is false when a terminal result was already emitted, in which case the
// caller must suppress this message: the earlier terminal result wins.
func (p *Protocol) terminalSuccess() (llm.SDKMessage, bool) {
	if !p.markTerminal() {
		return llm.SDKMessage{}, false
	}
	return llm.SDKMessage{
		Type:    "result",
		Subtype: "success",
		Result: &llm.ResultMessage{
			Type:       "result",
			Subtype:    "success",
			StopReason: "end_turn",
		},
	}, true
}

// terminalError returns a non-success result for a failed session. The bool is
// false when a terminal result was already emitted, in which case the caller
// must suppress this message: the earlier terminal result wins.
func (p *Protocol) terminalError(detail string) (llm.SDKMessage, bool) {
	if !p.markTerminal() {
		return llm.SDKMessage{}, false
	}
	// Every terminal ACP error diagnostic is the last gate before the detail is
	// surfaced to the session and persisted, so credential-like content is
	// redacted here once for all callers (handshake, prompt, malformed stdout,
	// fail-closed control).
	detail = sanitizeDiagnostic(detail)
	return llm.SDKMessage{
		Type:    "result",
		Subtype: "error",
		Result: &llm.ResultMessage{
			Type:    "result",
			Subtype: "error",
			Result:  detail,
			IsError: true,
		},
	}, true
}

func assistantPartial(block llm.ContentBlock) llm.SDKMessage {
	return llm.SDKMessage{
		Type:    "assistant",
		Subtype: "partial",
		Assistant: &llm.AssistantMessage{
			Type:    "assistant",
			Subtype: "partial",
			Message: llm.ConversationMsg{
				Role:    "assistant",
				Content: []llm.ContentBlock{block},
			},
		},
	}
}

// --- channel + id helpers ---

// failHandshake records a handshake error and unblocks the waiter so Handshake
// returns the error instead of timing out.
func (p *Protocol) failHandshake(err error, ch chan struct{}) {
	p.mu.Lock()
	if p.handshakeErr == nil {
		p.handshakeErr = err
	}
	p.mu.Unlock()
	p.closeOnce(ch)
}

// closeOnce closes ch if it is non-nil and not already closed.
func (p *Protocol) closeOnce(ch chan struct{}) {
	if ch == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func parseID(raw json.RawMessage) (int, error) {
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, err
	}
	return n, nil
}

// --- llm.Protocol methods stubbed for Phase 1 ---

// SendUserMessage delivers a follow-up user turn as a new session/prompt.
func (p *Protocol) SendUserMessage(text string) error {
	return p.sendPrompt(text)
}

// RespondToHook is a no-op — OpenCode ACP has no PreToolUse hook callbacks.
func (p *Protocol) RespondToHook(string) error { return nil }

// RespondToControl and RespondToAskUser are implemented in control.go: they
// answer the Phase 2 permission and question surfaces back through the ACP
// protocol.

// Interrupt returns ErrNotSupported; the session layer falls back to signalling
// the process group. Protocol-level interrupt arrives in a later phase.
func (p *Protocol) Interrupt() error { return llm.ErrNotSupported }

// SessionID returns "" — Agentico resume/session-identity parity for OpenCode is
// out of scope for Phase 1. The internal ACP session id used for prompt
// delivery is tracked separately (see ACPSessionID).
func (p *Protocol) SessionID() string { return "" }

// TranscriptPath returns "" — OpenCode transcript capture is out of scope.
func (p *Protocol) TranscriptPath() string { return "" }

// Close performs no cleanup; the session layer owns process teardown.
func (p *Protocol) Close() error { return nil }

// --- test accessors ---

// ACPSessionID returns the internal ACP session id created during the handshake.
func (p *Protocol) ACPSessionID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.acpSessionID
}

// NegotiatedVersion returns the ACP protocol version OpenCode negotiated.
func (p *Protocol) NegotiatedVersion() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.negotiatedVersion
}

// InitialPromptForTest returns the rendered phase prompt the protocol will send
// as the first user turn.
func (p *Protocol) InitialPromptForTest() string { return p.opts.InitialPrompt }

// WorkDirForTest returns the resolved work directory used for session/new.
func (p *Protocol) WorkDirForTest() string { return p.opts.WorkDir }

// MarkerPathForTest returns the phase_complete marker path threaded through the
// protocol options.
func (p *Protocol) MarkerPathForTest() string { return p.opts.MarkerPath }

// BackendModelForTest returns the backend "provider/model" the protocol selected.
func (p *Protocol) BackendModelForTest() string { return p.model }

// setRequestIDsForTest pins the handshake request ids so response-handling can
// be exercised without running the full Handshake.
func (p *Protocol) setRequestIDsForTest(initID, sessionNewID, promptID int) {
	p.mu.Lock()
	p.initID, p.sessionNewID, p.promptID = initID, sessionNewID, promptID
	p.mu.Unlock()
}

// promptIDForTest returns the id of the most recent session/prompt request,
// which advances each time a follow-up turn (e.g. a reformat reminder or an
// answer envelope) is sent.
func (p *Protocol) promptIDForTest() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.promptID
}
