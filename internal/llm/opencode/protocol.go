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
	"regexp"
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
	initID        int
	sessionNewID  int
	sessionLoadID int
	promptID      int

	negotiatedVersion int
	acpSessionID      string
	handshakeErr      error

	// resumeSessionID, when non-empty, asks the handshake to resume that ACP
	// session via session/load instead of creating a fresh one. loadSession
	// records whether the agent advertised the loadSession capability in its
	// initialize result, which gates whether resume is even attempted.
	resumeSessionID string
	loadSession     bool

	// transcriptPath is the OpenCode transcript file path, set only if the
	// provider ever proves one over ACP. It stays empty (best-effort) when
	// OpenCode exposes no transcript, rather than fabricating a path.
	transcriptPath string

	// Cumulative usage normalized from OpenCode's ACP lifecycle events.
	// inTok/outTok/cacheRead/cacheCreate are lifetime sums of the per-turn token
	// split from the prompt result's end-turn usage. contextFill is the latest
	// usage_update's `used` (tokens currently in context) and contextWindow its
	// `size` (model window); both drive context-% reporting. costUSD is the
	// latest cumulative session cost from a usage_update (zero when OpenCode
	// supplies none). usageSeen is true once any usage signal has been observed.
	inTok, outTok, cacheRead, cacheCreate int
	contextFill, contextWindow            int
	estimatedContextTokens                int
	costUSD                               float64
	usageSeen                             bool

	assistantBuf       strings.Builder
	assistantMessageID string
	resultEmitted      bool

	// toolCalls retains ACP tool-call metadata across start/update events. Some
	// OpenCode updates carry only the status and id after the initial event; the
	// attach/live-preview surfaces still need the normalized tool name and any
	// file target discovered earlier in the call lifecycle.
	toolCalls map[string]toolCallState

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

type toolCallState struct {
	title   string
	kind    string
	path    string
	command string
}

// NewProtocol creates a new OpenCode ACP protocol handler.
func NewProtocol(opts llm.ProtocolOpts) *Protocol {
	return &Protocol{
		opts:            opts,
		model:           BackendModel(opts.Model),
		contextWindow:   opts.ContextWindow,
		resumeSessionID: strings.TrimSpace(opts.ResumeSessionID),
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
//  1. initialize — negotiate the protocol version and read agent capabilities.
//  2. session/new (or session/load when resuming) — establish the session,
//     rooted at the resolved work directory, the prompt is delivered to.
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

	if err := p.startSession(ctx); err != nil {
		return err
	}

	return p.sendPrompt(p.opts.InitialPrompt)
}

// startSession establishes the ACP session the prompt is delivered to. With no
// resume request it creates a fresh session via session/new. With a resume
// request it resumes the prior session via session/load — but only when the
// agent advertised the loadSession capability; otherwise it fails clearly
// before any prompt runs, rather than silently starting an unrelated new
// session under a different identity.
func (p *Protocol) startSession(ctx context.Context) error {
	if p.resumeSessionID == "" {
		return p.runSessionHandshake(ctx, "session/new", p.sendSessionNew)
	}

	p.mu.Lock()
	canResume := p.loadSession
	p.mu.Unlock()
	if !canResume {
		return fmt.Errorf("opencode cannot resume session %q: the agent does not advertise the loadSession capability", p.resumeSessionID)
	}
	return p.runSessionHandshake(ctx, sessionLoadMethod, p.sendSessionLoad)
}

// runSessionHandshake sends a session-establishing request and blocks until the
// session is ready, the context is cancelled, or a handshake error is recorded.
func (p *Protocol) runSessionHandshake(ctx context.Context, method string, send func() error) error {
	if err := send(); err != nil {
		return err
	}
	select {
	case <-p.sessionReady:
	case <-ctx.Done():
		return fmt.Errorf("opencode %s timeout: %w", method, ctx.Err())
	}
	return p.handshakeError()
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

// sendSessionLoad resumes a prior ACP session. The response (and any replayed
// session/update notifications) arrive asynchronously; sessionReady unblocks
// once the load result is observed.
func (p *Protocol) sendSessionLoad() error {
	id := int(nextID.Add(1))
	p.mu.Lock()
	p.sessionLoadID = id
	resumeID := p.resumeSessionID
	p.mu.Unlock()

	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  sessionLoadMethod,
		Params: SessionLoadParams{
			SessionID:  resumeID,
			Cwd:        p.opts.WorkDir,
			MCPServers: []interface{}{},
		},
	}
	if err := p.writeJSON(req); err != nil {
		return fmt.Errorf("sending session/load request: %w", err)
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
	p.assistantMessageID = ""
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
	p.addEstimatedContextText(text)
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

	// Response to one of our requests: has id, no method. A response may yield
	// zero messages (handshake responses), one (a session-init message after
	// session establishment), or several (a prompt result emits a cumulative
	// usage update plus the terminal result).
	if hasID && env.Method == "" {
		return p.handleResponse(env), nil
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
		return p.parseNotification(env.Method, env.Params), nil
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

// handleResponse routes a JSON-RPC response to the request that issued it. The
// handshake (initialize) response only unblocks Handshake; a session
// establishment (session/new or session/load) response unblocks Handshake AND
// emits a session-init message so the session layer captures the session
// identity; the prompt response yields the terminal result (and a cumulative
// usage update).
func (p *Protocol) handleResponse(env inboundEnvelope) []llm.SDKMessage {
	id, err := parseID(env.ID)
	if err != nil {
		p.logDebug("[opencode] response with unparseable id %q", string(env.ID))
		return nil
	}

	p.mu.Lock()
	initID, sessionNewID, sessionLoadID, promptID := p.initID, p.sessionNewID, p.sessionLoadID, p.promptID
	p.mu.Unlock()

	hasErr := len(env.Error) > 0 && string(env.Error) != "null"

	switch id {
	case initID:
		if hasErr {
			p.failHandshake(fmt.Errorf("opencode initialize failed: %s", rpcErrorDetail(env.Error)), p.initDone)
			return nil
		}
		var res InitializeResult
		if jerr := json.Unmarshal(env.Result, &res); jerr != nil {
			p.failHandshake(fmt.Errorf("parsing initialize result: %s", sanitizeDiagnostic(jerr.Error())), p.initDone)
			return nil
		}
		p.mu.Lock()
		p.negotiatedVersion = res.ProtocolVersion
		p.loadSession = res.AgentCapabilities.LoadSession
		p.mu.Unlock()
		p.closeOnce(p.initDone)
		return nil

	case sessionNewID:
		if hasErr {
			p.failHandshake(fmt.Errorf("opencode session/new failed: %s", rpcErrorDetail(env.Error)), p.sessionReady)
			return nil
		}
		var res SessionNewResult
		if jerr := json.Unmarshal(env.Result, &res); jerr != nil {
			p.failHandshake(fmt.Errorf("parsing session/new result: %s", sanitizeDiagnostic(jerr.Error())), p.sessionReady)
			return nil
		}
		if strings.TrimSpace(res.SessionID) == "" {
			p.failHandshake(fmt.Errorf("opencode session/new returned no session id"), p.sessionReady)
			return nil
		}
		p.mu.Lock()
		p.acpSessionID = res.SessionID
		p.mu.Unlock()
		p.closeOnce(p.sessionReady)
		return []llm.SDKMessage{p.sessionInitMessage()}

	case sessionLoadID:
		// A resume that fails must fail clearly: never fall through to a fresh,
		// unrelated session under a different identity.
		if hasErr {
			p.failHandshake(fmt.Errorf("opencode session/load failed for session %q: %s", p.resumeSessionID, rpcErrorDetail(env.Error)), p.sessionReady)
			return nil
		}
		// session/load returns no new id — the resumed session keeps the
		// requested identity, which is what the next prompt is delivered to.
		p.mu.Lock()
		p.acpSessionID = p.resumeSessionID
		p.mu.Unlock()
		p.closeOnce(p.sessionReady)
		return []llm.SDKMessage{p.sessionInitMessage()}

	case promptID:
		return p.handlePromptResponse(env, hasErr)

	default:
		p.logDebug("[opencode] unhandled response for id %d", id)
		return nil
	}
}

// sessionInitMessage builds the system-init SDK message emitted once the ACP
// session is established (session/new) or resumed (session/load). It carries the
// captured session id and backend model so the session layer's existing
// init-message handling captures the model and updates the PID file with the
// provider session id — a path that is otherwise only driven by providers that
// emit a native init message (OpenCode does not).
func (p *Protocol) sessionInitMessage() llm.SDKMessage {
	p.mu.Lock()
	sid := p.acpSessionID
	model := p.model
	p.mu.Unlock()
	return llm.SDKMessage{
		Type:    "system",
		Subtype: "init",
		Init: &llm.SystemInitMessage{
			Type:      "system",
			Subtype:   "init",
			SessionID: sid,
			Model:     model,
		},
	}
}

// handlePromptResponse converts the session/prompt response into the terminal
// result, prefixed by a cumulative usage update when the result carried end-turn
// token usage. A clean end_turn is the only success; every other stop reason, an
// error response, or a malformed result is a non-success terminal state. The
// result usage is folded (and its usage update emitted) only on the first
// terminal seal, so a late or duplicate terminal can neither flip the sealed
// outcome nor double-count tokens.
func (p *Protocol) handlePromptResponse(env inboundEnvelope, hasErr bool) []llm.SDKMessage {
	if hasErr {
		return seal(p.terminalError(fmt.Sprintf("OpenCode prompt failed: %s", rpcErrorDetail(env.Error))))
	}
	var res PromptResult
	if jerr := json.Unmarshal(env.Result, &res); jerr != nil {
		return seal(p.terminalError(fmt.Sprintf("malformed OpenCode prompt result: %v", jerr)))
	}

	// Fold the end-turn token usage into the cumulative totals BEFORE building the
	// terminal result (so the result reflects it) and emit the resulting cumulative
	// usage update — the only carrier of the input/output/cache split. Fold only
	// when the outcome is not already sealed, so a late/duplicate terminal can
	// neither double-count tokens nor (below) flip the sealed outcome.
	p.mu.Lock()
	sealed := p.resultEmitted
	p.mu.Unlock()
	var usageMsgs []llm.SDKMessage
	if !sealed {
		usageMsgs = p.foldResultUsage(res.Usage)
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
				return append(usageMsgs, msg)
			}
			// A reformat-reminder turn was sent; no terminal yet — the reformatted
			// answer arrives on the follow-up turn's response.
			return usageMsgs
		}
		term, emit := p.terminalSuccess()
		return appendIfEmit(usageMsgs, term, emit)
	case StopReasonRefusal:
		term, emit := p.terminalError("OpenCode refused to complete the request")
		return appendIfEmit(usageMsgs, term, emit)
	case StopReasonCancelled:
		term, emit := p.terminalError("OpenCode prompt was cancelled")
		return appendIfEmit(usageMsgs, term, emit)
	case "":
		term, emit := p.terminalError("OpenCode prompt result carried no stop reason")
		return appendIfEmit(usageMsgs, term, emit)
	default:
		// max_tokens, max_turn_requests, and any future/unknown stop reason are
		// not a clean completion for the tracer.
		term, emit := p.terminalError(fmt.Sprintf("OpenCode prompt ended without completing (stop reason %q)", res.StopReason))
		return appendIfEmit(usageMsgs, term, emit)
	}
}

// seal wraps a terminal message into a result slice, dropping it when it was a
// suppressed duplicate (emit==false) so a late/duplicate terminal yields nothing.
func seal(term llm.SDKMessage, emit bool) []llm.SDKMessage {
	if !emit {
		return nil
	}
	return []llm.SDKMessage{term}
}

// appendIfEmit appends the terminal result to the (already-folded) usage updates
// when this is the first terminal seal; a suppressed duplicate (emit==false)
// contributes no terminal so a late/duplicate terminal cannot flip the outcome.
func appendIfEmit(usageMsgs []llm.SDKMessage, term llm.SDKMessage, emit bool) []llm.SDKMessage {
	if !emit {
		return usageMsgs
	}
	return append(usageMsgs, term)
}

// foldResultUsage folds the prompt result's end-turn token usage into the
// cumulative totals and returns the resulting cumulative usage update (empty when
// the result carried no usage). The streamed usage_update carries no token split,
// so this is what delivers input/output/cache to the session's accumulation.
func (p *Protocol) foldResultUsage(u *PromptUsage) []llm.SDKMessage {
	if msg, ok := p.accumulateResultUsage(u); ok {
		return []llm.SDKMessage{msg}
	}
	return nil
}

// parseNotification handles agent->client notifications (session/update). It
// normalizes assistant text, thoughts, and tool/terminal progress, and folds in
// any token-usage snapshot the update carries; everything else is ignored. A
// single update may yield both a content message and a usage update, so the
// result is a slice.
func (p *Protocol) parseNotification(method string, params json.RawMessage) []llm.SDKMessage {
	if method != "session/update" {
		p.logDebug("[opencode] ignoring notification %s", method)
		return nil
	}
	var su SessionUpdateParams
	if err := json.Unmarshal(params, &su); err != nil {
		p.logDebug("[opencode] failed to parse session/update: %v", err)
		return nil
	}

	var out []llm.SDKMessage
	switch su.Update.SessionUpdate {
	case UpdateAgentMessageChunk:
		if text := updateText(su.Update.Content); text != "" {
			p.mu.Lock()
			if su.Update.MessageID != "" && su.Update.MessageID != p.assistantMessageID {
				p.assistantBuf.Reset()
				p.assistantMessageID = su.Update.MessageID
			}
			p.assistantBuf.WriteString(text)
			p.estimatedContextTokens += estimateContextTokens(text)
			accumulated := p.assistantBuf.String()
			p.mu.Unlock()
			out = append(out, assistantPartial(llm.ContentBlock{Type: "text", Text: accumulated}))
		}

	case UpdateAgentThoughtChunk:
		if text := updateText(su.Update.Content); text != "" {
			out = append(out, assistantPartial(llm.ContentBlock{Type: "thinking", Thinking: "Thinking..."}))
		}

	case UpdateToolCall, UpdateToolCallUpdate:
		progress := p.toolProgressFromUpdate(su.Update)
		out = append(out, llm.SDKMessage{
			Type:         "tool_progress",
			ToolProgress: &progress,
		})

	case UpdateUsage:
		// usage_update carries context fill, context window, and cumulative cost;
		// normalize it into a cumulative usage update for context-% and cost.
		if msg, ok := p.applyUsageUpdate(su.Update); ok {
			out = append(out, msg)
		}
	}

	return out
}

// updateText returns the text of a message-chunk content block, or "" when
// absent.
func updateText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var c UpdateContent
	if err := json.Unmarshal(raw, &c); err != nil {
		return ""
	}
	return c.Text
}

func (p *Protocol) toolProgressFromUpdate(update SessionUpdate) llm.ToolProgressMessage {
	p.mu.Lock()
	if p.toolCalls == nil {
		p.toolCalls = make(map[string]toolCallState)
	}
	state := p.toolCalls[update.ToolCallID]
	state = mergeToolCallState(state, update)
	if update.ToolCallID != "" {
		p.toolCalls[update.ToolCallID] = state
	}
	p.mu.Unlock()

	return llm.ToolProgressMessage{
		Type:      "tool_progress",
		ToolUseID: update.ToolCallID,
		ToolName:  normalizedProgressToolName(state),
		Data:      normalizedProgressData(update.Status, state),
	}
}

func mergeToolCallState(state toolCallState, update SessionUpdate) toolCallState {
	if strings.TrimSpace(update.Title) != "" {
		state.title = strings.TrimSpace(update.Title)
	}
	if strings.TrimSpace(update.Kind) != "" {
		state.kind = strings.TrimSpace(update.Kind)
	}
	if command := firstStringField(update.RawInput, "", "command", "cmd", "script"); command != "" {
		state.command = command
	}
	if path := toolCallPath(update, state); path != "" {
		state.path = path
	}
	return state
}

func toolCallPath(update SessionUpdate, state toolCallState) string {
	if path := firstStringField(update.RawInput, "", "filePath", "file_path", "filepath", "path", "target_file"); path != "" {
		return path
	}
	kind := strings.ToLower(strings.TrimSpace(update.Kind))
	if kind == "" {
		kind = strings.ToLower(strings.TrimSpace(state.kind))
	}
	command := firstStringField(update.RawInput, "", "command", "cmd", "script")
	if command == "" {
		command = state.command
	}
	if kind == ToolKindExecute {
		return shellWriteTargetPath(command)
	}
	if len(update.Locations) > 0 {
		for _, loc := range update.Locations {
			if strings.TrimSpace(loc.Path) != "" {
				return strings.TrimSpace(loc.Path)
			}
		}
	}
	return ""
}

func normalizedProgressToolName(state toolCallState) string {
	kind := strings.ToLower(strings.TrimSpace(state.kind))
	title := strings.TrimSpace(state.title)
	switch kind {
	case ToolKindExecute:
		return "Bash"
	case ToolKindEdit:
		if strings.Contains(strings.ToLower(title), "write") {
			return "Write"
		}
		return "Edit"
	}
	if title != "" {
		return title
	}
	if kind != "" {
		return kind
	}
	return "tool"
}

func normalizedProgressData(status string, state toolCallState) string {
	var lines []string
	if strings.TrimSpace(state.command) != "" {
		lines = append(lines, strings.TrimSpace(state.command))
	}
	if strings.TrimSpace(state.path) != "" {
		lines = append(lines, "File: "+strings.TrimSpace(state.path))
	}
	status = strings.TrimSpace(status)
	if status != "" {
		if len(lines) == 0 {
			lines = append(lines, status)
		} else {
			lines = append(lines, "Status: "+status)
		}
	}
	return strings.Join(lines, "\n")
}

var shellWriteTargetRes = []*regexp.Regexp{
	regexp.MustCompile(`(?m)(?:^|[;&|]\s*)cat\s+>{1,2}\s*(?:"([^"]+)"|'([^']+)'|([^\s<>;&|]+))`),
	regexp.MustCompile(`(?m)(?:^|[;&|]\s*)tee(?:\s+-a)?\s+(?:"([^"]+)"|'([^']+)'|([^\s<>;&|]+))`),
	regexp.MustCompile(`(?m)(?:^|[;&|]\s*)touch\s+(?:"([^"]+)"|'([^']+)'|([^\s<>;&|]+))`),
}

func shellWriteTargetPath(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	for _, re := range shellWriteTargetRes {
		m := re.FindStringSubmatch(command)
		for i := 1; i < len(m); i++ {
			if strings.TrimSpace(m[i]) != "" {
				return strings.TrimSpace(m[i])
			}
		}
	}
	return ""
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

// --- usage normalization ---

// applyUsageUpdate folds an ACP usage_update into the protocol's context/cost
// state and returns a cumulative usage update SDK message. Used is the tokens
// currently in context and Size the model window — both SET, the latest snapshot
// wins, and absent values leave context unavailable. Cost.Amount is the
// cumulative session cost when the backend has pricing; absent or zero leaves the
// zero-cost fallback. The emitted message carries the FULL cumulative snapshot
// (including the token split accumulated from prompt results) so the session
// layer's SET-semantics accumulation stays correct across turns rather than being
// zeroed by an update that carries no token split.
func (p *Protocol) applyUsageUpdate(u SessionUpdate) (llm.SDKMessage, bool) {
	p.mu.Lock()
	p.usageSeen = true
	if u.Used > 0 {
		p.contextFill = u.Used
	}
	if u.Size > 0 {
		p.contextWindow = u.Size
	}
	if u.Cost != nil && u.Cost.Amount > 0 {
		p.costUSD = u.Cost.Amount
	}
	usage := p.usageLocked()
	p.mu.Unlock()
	return llm.SDKMessage{Type: "usage_update", UsageUpdate: &usage}, true
}

// accumulateResultUsage folds the end-turn token accounting from a session/prompt
// result into the cumulative input/output/cache totals and returns a cumulative
// usage update SDK message. This is the only carrier of the token split (the
// streamed usage_update has none), so each result's tokens are summed once into
// the lifetime totals — a multi-turn session accumulates without double-counting.
// ok is false when the result carried no usage, so callers emit nothing.
func (p *Protocol) accumulateResultUsage(u *PromptUsage) (llm.SDKMessage, bool) {
	if u == nil {
		return llm.SDKMessage{}, false
	}
	p.mu.Lock()
	p.usageSeen = true
	p.inTok += u.InputTokens
	p.outTok += u.OutputTokens
	p.cacheRead += u.CachedReadTokens
	p.cacheCreate += u.CachedWriteTokens
	usage := p.usageLocked()
	p.mu.Unlock()
	return llm.SDKMessage{Type: "usage_update", UsageUpdate: &usage}, true
}

// addEstimatedContextText records a coarse local token estimate for text Agentico
// knows entered the conversation. It is used only when OpenCode reports a
// context window but leaves both usage_update.used and result token usage at 0.
func (p *Protocol) addEstimatedContextText(text string) {
	tokens := estimateContextTokens(text)
	if tokens == 0 {
		return
	}
	p.mu.Lock()
	p.estimatedContextTokens += tokens
	p.mu.Unlock()
}

// estimateContextTokens intentionally stays simple and deterministic. OpenCode
// already owns exact accounting when the backend reports it; this keeps the
// context meter useful for zero-telemetry backends without pretending to be a
// provider tokenizer.
func estimateContextTokens(text string) int {
	if text == "" {
		return 0
	}
	const charsPerToken = 4
	return (len(text) + charsPerToken - 1) / charsPerToken
}

// usageLocked snapshots the cumulative normalized usage. Callers must hold p.mu.
// ContextBaseline is 0 (OpenCode, like Claude, has no fixed overhead to
// subtract). ContextWindow comes from OpenCode's usage_update.size or the
// provider-selected model metadata. ContextTotalTokens prefers OpenCode's exact
// usage_update.used, then result token usage, then the local text estimate for
// zero-telemetry backends.
func (p *Protocol) usageLocked() llm.Usage {
	contextFill := p.contextFill
	if contextFill == 0 {
		contextFill = p.inTok + p.cacheRead + p.cacheCreate
	}
	if contextFill == 0 {
		contextFill = p.estimatedContextTokens
	}
	return llm.Usage{
		InputTokens:              p.inTok,
		OutputTokens:             p.outTok,
		CacheReadInputTokens:     p.cacheRead,
		CacheCreationInputTokens: p.cacheCreate,
		ContextInputTokens:       contextFill,
		ContextTotalTokens:       contextFill,
		ContextWindow:            p.contextWindow,
		ContextBaseline:          0,
	}
}

// resultUsage returns the cumulative usage to attach to a terminal result, or
// nil when OpenCode emitted no usage this session (so the result carries no
// fabricated zero-token usage block).
func (p *Protocol) resultUsage() *llm.Usage {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.usageSeen {
		return nil
	}
	u := p.usageLocked()
	return &u
}

// resultCost returns the cumulative session cost OpenCode reported via the latest
// usage_update, or zero when OpenCode supplied no pricing. Agentico never invents
// a figure; zero is the documented fallback.
func (p *Protocol) resultCost() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.costUSD
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
			// Cost is OpenCode's reported cumulative session cost (zero when the
			// backend has no pricing) — never invented from tokens. Emitted token
			// usage is preserved.
			TotalCostUSD: p.resultCost(),
			Usage:        p.resultUsage(),
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
			// Preserve any token usage and cost observed before the failure (nil
			// usage / zero cost when none was seen, e.g. handshake or fail-closed
			// errors) — a failed OpenCode turn still reports what it spent.
			TotalCostUSD: p.resultCost(),
			Usage:        p.resultUsage(),
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

// --- llm.Protocol lifecycle methods ---

// SendUserMessage delivers a follow-up user turn as a new session/prompt.
func (p *Protocol) SendUserMessage(text string) error {
	return p.sendPrompt(text)
}

// RespondToHook is a no-op — OpenCode ACP has no PreToolUse hook callbacks.
func (p *Protocol) RespondToHook(string) error { return nil }

// RespondToControl and RespondToAskUser are implemented in control.go: they
// answer the Phase 2 permission and question surfaces back through the ACP
// protocol.

// Interrupt cancels the in-flight turn using the ACP session/cancel
// notification, which OpenCode answers by completing the pending session/prompt
// with stopReason "cancelled" (mapped to a terminal non-success result). It
// returns llm.ErrNotSupported only before a session exists (no stdin or no
// session id yet), so the session layer's process-group SIGINT fallback still
// applies in that window.
func (p *Protocol) Interrupt() error {
	p.mu.Lock()
	w := p.stdin
	sessionID := p.acpSessionID
	p.mu.Unlock()
	if w == nil || sessionID == "" {
		return llm.ErrNotSupported
	}
	if err := p.writeJSON(Notification{
		JSONRPC: "2.0",
		Method:  sessionCancelMethod,
		Params:  SessionCancelParams{SessionID: sessionID},
	}); err != nil {
		// Falling back to the session-level interrupt is safer than reporting a
		// hard error when the cancel notification cannot be written.
		p.logDebug("[opencode] session/cancel write failed: %v", err)
		return llm.ErrNotSupported
	}
	return nil
}

// SessionID returns the captured ACP session id so Agentico can scope session
// views, PID-file identity, and the permission cache to this OpenCode session,
// and so a later run can request a resume of it. It is empty until the handshake
// establishes (or resumes) a session.
func (p *Protocol) SessionID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.acpSessionID
}

// TranscriptPath returns a concrete OpenCode transcript path only when the
// provider has proven one over ACP. OpenCode exposes no transcript path through
// the ACP surface Agentico uses, so this is empty (best-effort) rather than a
// fabricated path; it never panics.
func (p *Protocol) TranscriptPath() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.transcriptPath
}

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

// SetRequestIDsForTest pins the handshake request ids (including session/load) so
// other packages can drive a real protocol through session establishment and
// prompt responses without running the full ACP handshake. Test-only.
func (p *Protocol) SetRequestIDsForTest(initID, sessionNewID, sessionLoadID, promptID int) {
	p.mu.Lock()
	p.initID, p.sessionNewID, p.sessionLoadID, p.promptID = initID, sessionNewID, sessionLoadID, promptID
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
