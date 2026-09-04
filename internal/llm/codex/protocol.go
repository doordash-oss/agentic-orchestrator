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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// nextID is an atomic counter for JSON-RPC request IDs.
var nextID atomic.Int64

// codexContextBaselineTokens mirrors Codex's own TokenUsage::BASELINE_TOKENS
// (openai/codex, codex-rs/protocol/src/protocol.rs). It is the fixed
// system-prompt / tool-schema overhead that Codex subtracts from both
// numerator and denominator when computing context window usage in its
// `/status` display. We carry it through so ContextPercentage() matches
// what a user sees inside Codex itself.
const codexContextBaselineTokens = 12000

// codexItemTypeFileChange is the ItemUnion.Type discriminator for a Codex
// thread item representing a file mutation (item/started, item/completed).
const codexItemTypeFileChange = "fileChange"
const codexItemTypeCollabAgentToolCall = "collabAgentToolCall"

// codexFileChangeOperationWrite and codexFileChangeOperationUpdate are the
// normalized llm.FileChangeEvent.Operation values produced by
// normalizeFileChangeOperation, and also the raw FileChangeKind.Type /
// CommandAction.Type values reported by Codex for a write-style action.
const (
	codexFileChangeOperationWrite  = "write"
	codexFileChangeOperationUpdate = "update"
)

// codexToolNameWrite is the llm.ToolProgressMessage.ToolName Codex reports
// for file-change tool activity, mirroring Claude's "Write" tool name so
// downstream consumers treat it consistently across providers.
const codexToolNameWrite = "Write"

// codexRoleAssistant is the llm.SDKMessage/AssistantMessage Type and
// llm.ConversationMsg Role value for assistant-authored content.
const codexRoleAssistant = "assistant"

// Protocol implements llm.Protocol for the Codex app-server JSON-RPC protocol.
type Protocol struct {
	opts llm.ProtocolOpts

	stdin io.Writer
	mu    sync.Mutex

	// Handshake state
	handshakeDone chan struct{}
	threadReady   chan struct{}

	// Session state
	threadID              string
	turnID                string
	model                 string
	approvalPolicy        string
	dangerFullAccess      bool
	inputTokens           int
	cachedInputTokens     int
	cacheWriteInputTokens int
	pricingModel          string
	serviceTier           string
	usageState            usageState
	outputTokens          int
	totalCostUSD          float64
	modelContextWindow    int
	deltaBuf              map[string]string
	pendingQuestions      map[string]pendingQuestionRequest
	pendingCompletion     string
	lastAssistantText     string
	lastAssistantDraft    string
	fileReadSeen          map[string]struct{}
	turnStarted           bool
	nativeReviewTurnID    string
	nativeDecisionSeen    bool
	nativeReviewFailed    bool
	tasksByThread         map[string]codexTaskRef

	logFunc func(string, ...interface{})
}

type codexTaskRef struct {
	taskID         string
	childSessionID string
	description    string
}

// NewProtocol creates a new Codex protocol handler.
func NewProtocol(opts llm.ProtocolOpts) *Protocol {
	policy := strings.TrimSpace(opts.ApprovalPolicy)
	if policy == "" {
		// DSP controls the sandbox boundary, not the approval-policy enum.
		// Managed Codex installations may require on-request even when the
		// sandbox itself is danger-full-access; sending never is rejected by
		// those installations before the first turn starts.
		policy = "on-request"
	}
	if opts.NativeToollessReview {
		// Managed installations may disallow "never". The reviewer still
		// exposes no tools, and any unexpected approval request fails closed.
		policy = "on-request"
	}
	return &Protocol{
		opts:             opts,
		usageState:       usageState{resumeBaselinePending: opts.ResumeSessionID != ""},
		model:            llm.StripModelContextWindow(opts.Model),
		approvalPolicy:   policy,
		dangerFullAccess: opts.DSP,
	}
}

func (p *Protocol) SetStdin(w io.Writer) {
	p.mu.Lock()
	p.stdin = w
	p.mu.Unlock()
}

// SetLogFunc sets a logging function for debug output.
func (p *Protocol) SetLogFunc(f func(string, ...interface{})) {
	p.logFunc = f
}

func (p *Protocol) logDebug(format string, args ...interface{}) {
	if p.logFunc != nil {
		p.logFunc(format, args...)
	}
}

// Handshake performs the 3-step Codex initialization:
// 1. Send initialize + initialized notification
// 2. Wait for initialize response, then send thread/start
// 3. Wait for thread/start response, then send turn/start with initial prompt
func (p *Protocol) Handshake(ctx context.Context) error {
	// Step 1: Send initialize
	if err := p.sendInitialize(); err != nil {
		return err
	}

	// Step 2: Wait for handshake response
	select {
	case <-p.handshakeDone:
	case <-ctx.Done():
		return fmt.Errorf("codex handshake timeout: %w", ctx.Err())
	}

	// Step 3: Send thread/start
	if err := p.startThread(); err != nil {
		return err
	}

	// Step 4: Wait for thread ready
	select {
	case <-p.threadReady:
	case <-ctx.Done():
		return fmt.Errorf("codex thread start timeout: %w", ctx.Err())
	}

	// Step 5: Send initial turn
	if err := p.startTurn(p.opts.InitialPrompt); err != nil {
		return err
	}

	return nil
}

// ParseLine translates a JSON-RPC line from Codex into SDKMessages.
func (p *Protocol) ParseLine(line []byte) ([]llm.SDKMessage, error) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		p.logDebug("[codex] failed to parse JSON-RPC envelope: %v", err)
		if p.opts.NativeToollessReview {
			p.markNativeToollessFailed()
			return nil, fmt.Errorf("parsing Codex native tool-less review response: %w", err)
		}
		return nil, nil
	}

	if p.opts.NativeToollessReview {
		p.mu.Lock()
		failed := p.nativeReviewFailed
		p.mu.Unlock()
		if failed {
			return []llm.SDKMessage{p.nativeToollessViolation("activity after terminal failure")}, nil
		}
	}

	// Response to our request (has id but no method)
	if env.ID != nil && env.Method == "" {
		// Negative IDs are reserved for optional billing lookups. Their failures
		// must never turn a successful agent turn into a protocol error.
		if *env.ID < 0 && !p.opts.NativeToollessReview {
			msg, emit := p.handleUsageResponse(*env.ID, env.Result, env.Error)
			if emit {
				return []llm.SDKMessage{p.stampMessage(msg)}, nil
			}
			return nil, nil
		}
		if p.opts.NativeToollessReview && len(env.Error) > 0 && string(env.Error) != "null" {
			return []llm.SDKMessage{p.nativeToollessViolation("JSON-RPC error response")}, nil
		}
		if msg, emit, handled := p.handleResponse(*env.ID, env.Result, env.Error); handled {
			if emit {
				return []llm.SDKMessage{p.stampMessage(msg)}, nil
			}
			return nil, nil
		}
		if p.opts.NativeToollessReview {
			return []llm.SDKMessage{p.nativeToollessViolation("malformed or unexpected JSON-RPC response")}, nil
		}
		return nil, nil
	}

	// Server-initiated request (has both id and method)
	if env.ID != nil && env.Method != "" {
		if p.opts.NativeToollessReview {
			return []llm.SDKMessage{p.nativeToollessViolation("unexpected server request: " + env.Method)}, nil
		}
		msg, ok := p.parseServerRequest(env.Method, *env.ID, env.Params)
		if ok {
			return []llm.SDKMessage{p.stampMessage(msg)}, nil
		}
		return nil, nil
	}

	// Notification (method only, no id)
	if env.Method != "" {
		msg, ok := p.parseNotification(env.Method, env.Params)
		if ok {
			return []llm.SDKMessage{p.stampMessage(msg)}, nil
		}
		return nil, nil
	}

	p.logDebug("[codex] unrecognized JSON-RPC message (no method or id)")
	if p.opts.NativeToollessReview {
		return []llm.SDKMessage{p.nativeToollessViolation("malformed JSON-RPC envelope")}, nil
	}
	return nil, nil
}

func (p *Protocol) stampMessage(msg llm.SDKMessage) llm.SDKMessage {
	if msg.Origin.Kind == "" {
		msg.Origin = llm.EventOrigin{Kind: llm.EventOriginRoot}
	}
	if msg.OccurredAt.IsZero() {
		msg.OccurredAt = time.Now().UTC()
	}
	return msg
}

// SendUserMessage sends a follow-up turn with user text.
func (p *Protocol) SendUserMessage(text string) error {
	if p.opts.NativeToollessReview {
		return fmt.Errorf("Codex native tool-less review permits exactly one turn")
	}
	return p.sendFollowUpTurn(text)
}

// RespondToControl sends an allow/deny response for an approval request.
// originalInput is ignored for Codex (only used by Claude).
func (p *Protocol) RespondToControl(requestID string, allow bool, originalInput json.RawMessage, reason string) error {
	if p.opts.NativeToollessReview {
		return fmt.Errorf("Codex native tool-less review does not answer unexpected control requests")
	}
	if allow {
		return p.writeAllowResponse(requestID)
	}
	return p.writeDenyResponse(requestID)
}

// RespondToHook is a no-op for Codex (hooks are Claude-specific).
func (p *Protocol) RespondToHook(_ string) error {
	return nil
}

// RespondToAskUser returns the actual answer to its pending Codex tool call.
func (p *Protocol) RespondToAskUser(requestID string, _ json.RawMessage, answers map[string]string, annotations map[string]llm.AskUserAnnotation) error {
	return p.respondToAskUser(requestID, answers, annotations)
}

// Interrupt returns ErrNotSupported — Codex's app-server protocol has no
// outbound cancel message. The session layer falls back to SIGINT on the
// process group, which the codex CLI converts into a turn/completed with
// status="interrupted" (handled in ParseLine).
func (p *Protocol) Interrupt() error { return llm.ErrNotSupported }

// SessionID returns the Codex thread ID once thread/start (or thread/resume)
// has completed, "" before that. The thread ID is resumable via
// ProtocolOpts.ResumeSessionID in a later session.
func (p *Protocol) SessionID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.threadID
}

// TranscriptPath returns "" — Codex has no transcript file.
func (p *Protocol) TranscriptPath() string { return "" }

func (p *Protocol) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.usageState.closed = true
	p.finishUsageReadLocked()
	return nil
}

// --- Internal envelope ---

type envelope struct {
	Method string          `json:"method,omitempty"`
	ID     *int            `json:"id,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// --- Write methods ---

func (p *Protocol) sendInitialize() error {
	id := int(nextID.Add(1))

	p.mu.Lock()
	p.handshakeDone = make(chan struct{})
	p.threadReady = make(chan struct{})
	p.mu.Unlock()

	req := Request{
		JSONRPC: "2.0",
		Method:  "initialize",
		ID:      id,
		Params: InitializeParams{
			ClientInfo: ClientInfo{
				Name:    "agentic",
				Title:   "Agentic Orchestrator",
				Version: "0.1.0",
			},
			Capabilities: Capabilities{
				ExperimentalAPI: true,
			},
		},
	}
	if err := p.writeJSON(req); err != nil {
		return fmt.Errorf("sending initialize request: %w", err)
	}

	notif := OutboundNotification{
		JSONRPC: "2.0",
		Method:  "initialized",
	}
	if err := p.writeJSON(notif); err != nil {
		return fmt.Errorf("sending initialized notification: %w", err)
	}

	return nil
}

func (p *Protocol) startThread() error {
	if p.UsesStructuredCompletion() && p.opts.StateDir == "" {
		return fmt.Errorf("Codex structured phases require a provider state directory for resumable tool contracts")
	}
	id := int(nextID.Add(1))

	if p.opts.NativeToollessReview && p.opts.ResumeSessionID != "" {
		return fmt.Errorf("Codex native tool-less review cannot resume a persisted thread")
	}

	// Resume a persisted thread instead of starting a fresh one. The
	// response carries the same {thread:{id}} shape as thread/start, so
	// handleResponse closes threadReady for both paths. Model, approval
	// policy, and sandbox are re-supplied per-turn by turn/start.
	if p.opts.ResumeSessionID != "" {
		if err := p.checkResumableContract(); err != nil {
			return err
		}
		req := Request{
			JSONRPC: "2.0",
			Method:  "thread/resume",
			ID:      id,
			Params: ThreadResumeParams{
				ThreadID:              p.opts.ResumeSessionID,
				DeveloperInstructions: p.developerInstructions(),
				Config:                p.threadConfig(),
			},
		}
		return p.writeJSON(req)
	}

	sandbox := SandboxModeWorkspaceWrite
	if p.dangerFullAccess {
		sandbox = SandboxModeDangerFullAccess
	}

	model := p.model
	if model == "" {
		model = "gpt-5.4"
	}

	req := Request{
		JSONRPC: "2.0",
		Method:  "thread/start",
		ID:      id,
		Params: ThreadStartParams{
			Model:                 model,
			Cwd:                   p.opts.WorkDir,
			ApprovalPolicy:        p.approvalPolicy,
			Sandbox:               &sandbox,
			DeveloperInstructions: p.developerInstructions(),
			Config:                p.threadConfig(),
			DynamicTools:          p.dynamicTools(),
		},
	}
	if p.opts.NativeToollessReview {
		empty := ""
		emptyList := []map[string]interface{}{}
		req.Params = ThreadStartParams{
			Model:                   model,
			Cwd:                     p.opts.WorkDir,
			ApprovalPolicy:          p.approvalPolicy,
			Sandbox:                 ptrSandboxMode(SandboxModeReadOnly),
			Ephemeral:               true,
			Config:                  nativeToollessThreadConfig(),
			BaseInstructions:        &empty,
			DeveloperInstructions:   &empty,
			Environments:            &emptyList,
			DynamicTools:            &emptyList,
			SelectedCapabilityRoots: &emptyList,
		}
	}
	return p.writeJSON(req)
}

func (p *Protocol) startTurn(userPrompt string) error {
	id := int(nextID.Add(1))

	p.mu.Lock()
	threadID := p.threadID
	writableRoots := append([]string(nil), p.opts.WritableRoots...)
	policy := p.approvalPolicy
	dangerFullAccess := p.dangerFullAccess
	p.mu.Unlock()

	model := p.model
	if p.opts.NativeToollessReview {
		req := Request{
			JSONRPC: "2.0",
			Method:  "turn/start",
			ID:      id,
			Params: TurnStartParams{
				ThreadID: threadID,
				Input: []InputItem{
					{Type: "text", Text: userPrompt},
				},
				Model:          model,
				Effort:         "low",
				ApprovalPolicy: p.approvalPolicy,
				SandboxPolicy: &SandboxPolicy{
					Type:          "readOnly",
					NetworkAccess: false,
				},
			},
		}
		return p.writeJSON(req)
	}

	sandboxPolicy := &SandboxPolicy{
		Type:          "workspaceWrite",
		WritableRoots: writableRoots,
		NetworkAccess: true,
	}
	if dangerFullAccess {
		sandboxPolicy = &SandboxPolicy{
			Type:          "dangerFullAccess",
			NetworkAccess: true,
		}
	}

	input := []InputItem{
		{Type: "text", Text: userPrompt},
	}

	req := Request{
		JSONRPC: "2.0",
		Method:  "turn/start",
		ID:      id,
		Params: TurnStartParams{
			ThreadID:       threadID,
			Input:          input,
			ApprovalPolicy: policy,
			SandboxPolicy:  sandboxPolicy,
			Model:          model,
		},
	}
	return p.writeJSON(req)
}

func (p *Protocol) sendFollowUpTurn(text string) error {
	if p.opts.NativeToollessReview {
		return fmt.Errorf("Codex native tool-less review permits exactly one turn")
	}
	id := int(nextID.Add(1))

	p.mu.Lock()
	p.usageState.revision++
	p.pricingModel = p.model
	threadID := p.threadID
	policy := p.approvalPolicy
	model := p.model
	writableRoots := append([]string(nil), p.opts.WritableRoots...)
	dangerFullAccess := p.dangerFullAccess
	p.mu.Unlock()

	if policy == "" {
		policy = "on-request"
	}

	sandbox := &SandboxPolicy{
		Type:          "workspaceWrite",
		WritableRoots: writableRoots,
		NetworkAccess: true,
	}
	if dangerFullAccess {
		sandbox = &SandboxPolicy{
			Type:          "dangerFullAccess",
			NetworkAccess: true,
		}
	}

	req := Request{
		JSONRPC: "2.0",
		Method:  "turn/start",
		ID:      id,
		Params: TurnStartParams{
			ThreadID: threadID,
			Input: []InputItem{
				{Type: "text", Text: text},
			},
			ApprovalPolicy: policy,
			SandboxPolicy:  sandbox,
			Model:          model,
		},
	}
	return p.writeJSON(req)
}

func (p *Protocol) writeAllowResponse(requestID string) error {
	id, err := strconv.Atoi(requestID)
	if err != nil {
		return fmt.Errorf("invalid codex request ID %q: %w", requestID, err)
	}
	return p.writeJSON(Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  ApprovalDecision{Decision: "accept"},
	})
}

func (p *Protocol) writeDenyResponse(requestID string) error {
	id, err := strconv.Atoi(requestID)
	if err != nil {
		return fmt.Errorf("invalid codex request ID %q: %w", requestID, err)
	}
	return p.writeJSON(Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  ApprovalDecision{Decision: "decline"},
	})
}

func (p *Protocol) writeJSON(v interface{}) error {
	p.mu.Lock()
	w := p.stdin
	p.mu.Unlock()

	if w == nil {
		return fmt.Errorf("codex protocol: stdin not set")
	}

	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// --- Read methods ---

func (p *Protocol) handleResponse(id int, result, errData json.RawMessage) (msg llm.SDKMessage, emit, handled bool) {
	if len(errData) > 0 && string(errData) != "null" {
		p.logDebug("[codex] error response for request %d: %s", id, string(errData))
		return p.errorResultMessage(errData), true, true
	}

	var initResult InitializeResult
	if err := json.Unmarshal(result, &initResult); err == nil && initResult.UserAgent != "" {
		p.logDebug("[codex] initialized: userAgent=%s codexHome=%s", initResult.UserAgent, initResult.CodexHome)
		p.mu.Lock()
		if p.handshakeDone != nil {
			select {
			case <-p.handshakeDone:
			default:
				close(p.handshakeDone)
			}
		}
		p.mu.Unlock()
		return llm.SDKMessage{}, false, true
	}

	var threadResult ThreadStartResult
	if err := json.Unmarshal(result, &threadResult); err == nil && threadResult.Thread.ID != "" {
		if err := p.recordThreadContract(threadResult.Thread.ID); err != nil {
			return llm.SDKMessage{Type: "result", Subtype: "error", Result: &llm.ResultMessage{Type: "result", Subtype: "error", IsError: true, Result: err.Error()}}, true, true
		}
		p.mu.Lock()
		p.threadID = threadResult.Thread.ID
		if threadResult.Model != "" {
			p.pricingModel = threadResult.Model
		}
		p.serviceTier = threadResult.ServiceTier
		if effectivePolicy := strings.TrimSpace(threadResult.ApprovalPolicy); effectivePolicy != "" && !p.opts.NativeToollessReview {
			p.approvalPolicy = effectivePolicy
		}
		if p.threadReady != nil {
			select {
			case <-p.threadReady:
			default:
				close(p.threadReady)
			}
		}
		p.mu.Unlock()
		if p.opts.ResumeSessionID != "" {
			p.requestUsageRead()
		}
		p.logDebug("[codex] thread started: %s", threadResult.Thread.ID)
		return llm.SDKMessage{}, false, true
	}

	var turnResult TurnStartResult
	if err := json.Unmarshal(result, &turnResult); err == nil && turnResult.Turn.ID != "" {
		p.mu.Lock()
		p.turnID = turnResult.Turn.ID
		p.mu.Unlock()
		p.logDebug("[codex] turn started: %s (status=%s)", turnResult.Turn.ID, turnResult.Turn.Status)
		return llm.SDKMessage{}, false, true
	}

	p.logDebug("[codex] unhandled response for request %d", id)
	return llm.SDKMessage{}, false, false
}

// errorResultMessage converts a JSON-RPC error response into a user-visible
// result/error so a rejected request fails the session loudly instead of
// leaving it to hang waiting for output that will never arrive.
func (p *Protocol) errorResultMessage(errData json.RawMessage) llm.SDKMessage {
	var rpcErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(errData, &rpcErr)
	detail := rpcErr.Message
	if detail == "" {
		detail = string(errData)
	}
	return llm.SDKMessage{
		Type:    "result",
		Subtype: "error",
		Result: &llm.ResultMessage{
			Type:    "result",
			Subtype: "error",
			Result:  fmt.Sprintf("codex rejected the request (code %d): %s", rpcErr.Code, detail),
			IsError: true,
		},
	}
}

func (p *Protocol) parseServerRequest(method string, id int, params json.RawMessage) (llm.SDKMessage, bool) {
	switch method {
	case "item/commandExecution/requestApproval":
		var approval CommandApprovalParams
		if err := json.Unmarshal(params, &approval); err != nil {
			p.logDebug("[codex] failed to parse command approval: %v", err)
			return llm.SDKMessage{}, false
		}
		inputJSON, _ := json.Marshal(map[string]string{"command": approval.Command})
		return p.controlMessageOrigin(llm.SDKMessage{
			Type:    "control_request",
			Subtype: "can_use_tool",
			ControlRequest: &llm.ControlRequestMessage{
				Type:      "control_request",
				RequestID: strconv.Itoa(id),
				Request: llm.ControlRequest{
					Subtype:  "can_use_tool",
					ToolName: "Bash",
					Input:    json.RawMessage(inputJSON),
				},
			},
		}, approval.ThreadID), true

	case "item/fileChange/requestApproval":
		var approval FileChangeApprovalParams
		if err := json.Unmarshal(params, &approval); err != nil {
			p.logDebug("[codex] failed to parse file change approval: %v", err)
			return llm.SDKMessage{}, false
		}
		inputFields := map[string]string{}
		if approval.GrantRoot != "" {
			inputFields["file_path"] = approval.GrantRoot
		}
		if approval.Reason != "" {
			inputFields["reason"] = approval.Reason
		}
		inputJSON, _ := json.Marshal(inputFields)
		return p.controlMessageOrigin(llm.SDKMessage{
			Type:    "control_request",
			Subtype: "can_use_tool",
			ControlRequest: &llm.ControlRequestMessage{
				Type:      "control_request",
				RequestID: strconv.Itoa(id),
				Request: llm.ControlRequest{
					Subtype:  "can_use_tool",
					ToolName: codexToolNameWrite,
					Input:    json.RawMessage(inputJSON),
				},
			},
		}, approval.ThreadID), true

	case "item/tool/call":
		return p.handleDynamicToolCall(id, params)
	case "item/tool/requestUserInput":
		return p.handleNativeUserInput(id, params)

	default:
		p.logDebug("[codex] unhandled server request: %s", method)
		return llm.SDKMessage{}, false
	}
}

func (p *Protocol) controlMessageOrigin(msg llm.SDKMessage, threadID string) llm.SDKMessage {
	origin := llm.EventOrigin{Kind: llm.EventOriginRoot}
	if !p.isMainThread(threadID) {
		origin = llm.EventOrigin{
			Kind:           llm.EventOriginTask,
			TaskID:         threadID,
			ChildSessionID: threadID,
		}
	}
	msg.Origin = origin
	if msg.ControlRequest != nil {
		msg.ControlRequest.Origin = origin
	}
	return msg
}

func (p *Protocol) isMainThread(threadID string) bool {
	mainThread := p.threadID
	return threadID == "" || mainThread == "" || threadID == mainThread
}

func (p *Protocol) taskStartedForCollab(item ItemUnion) llm.SDKMessage {
	childSessionID := ""
	if len(item.ReceiverThreadIDs) > 0 {
		childSessionID = item.ReceiverThreadIDs[0]
	}
	ref := codexTaskRef{
		taskID:         item.ID,
		childSessionID: childSessionID,
		description:    strings.TrimSpace(item.Prompt),
	}
	p.mu.Lock()
	if p.tasksByThread == nil {
		p.tasksByThread = make(map[string]codexTaskRef)
	}
	for _, threadID := range item.ReceiverThreadIDs {
		if threadID != "" {
			p.tasksByThread[threadID] = ref
		}
	}
	p.mu.Unlock()
	return llm.SDKMessage{
		Type:    "system",
		Subtype: "task_started",
		Origin: llm.EventOrigin{
			Kind:           llm.EventOriginTask,
			TaskID:         item.ID,
			ChildSessionID: childSessionID,
		},
		TaskStarted: &llm.TaskStartedMessage{
			Type:        "system",
			Subtype:     "task_started",
			TaskID:      item.ID,
			ToolUseID:   item.ID,
			Description: strings.TrimSpace(item.Prompt),
			TaskType:    strings.TrimSpace(item.Tool),
			Prompt:      item.Prompt,
			SessionID:   childSessionID,
		},
	}
}

func (p *Protocol) taskNotificationForCollab(item ItemUnion) llm.SDKMessage {
	status := strings.ToLower(strings.TrimSpace(item.Status))
	if status == "" || status == "inprogress" || status == "in_progress" {
		status = "completed"
	}
	childSessionID := ""
	if len(item.ReceiverThreadIDs) > 0 {
		childSessionID = item.ReceiverThreadIDs[0]
	}
	return llm.SDKMessage{
		Type:    "system",
		Subtype: "task_notification",
		Origin: llm.EventOrigin{
			Kind:           llm.EventOriginTask,
			TaskID:         item.ID,
			ChildSessionID: childSessionID,
		},
		TaskNotification: &llm.TaskNotificationMessage{
			Type:      "system",
			Subtype:   "task_notification",
			TaskID:    item.ID,
			ToolUseID: item.ID,
			Status:    status,
			SessionID: childSessionID,
		},
	}
}

func (p *Protocol) taskProgressForChildItem(threadID string, item ItemUnion) (llm.SDKMessage, bool) {
	toolName := item.Type
	lastPath := ""
	description := ""
	switch item.Type {
	case "commandExecution":
		toolName = "Bash"
	case codexItemTypeFileChange:
		toolName = codexToolNameWrite
		if len(item.Changes) > 0 {
			lastPath = item.Changes[0].Path
		}
	case "agentMessage":
		toolName = "assistant"
		description = strings.TrimSpace(item.Text)
	}
	return p.taskProgressForChildThread(threadID, toolName, lastPath, description)
}

func (p *Protocol) taskProgressForChildThread(threadID, toolName, lastPath, description string) (llm.SDKMessage, bool) {
	p.mu.Lock()
	ref, ok := p.tasksByThread[threadID]
	p.mu.Unlock()
	if !ok {
		// A child thread without a preceding root collabAgentToolCall cannot be
		// assigned to a delegated task safely. Ignore it instead of inventing a
		// second live task that no parent terminal event could complete.
		return llm.SDKMessage{}, false
	}
	if strings.TrimSpace(description) == "" {
		description = ref.description
	}
	return llm.SDKMessage{
		Type:    "system",
		Subtype: "task_progress",
		Origin: llm.EventOrigin{
			Kind:           llm.EventOriginTask,
			TaskID:         ref.taskID,
			ChildSessionID: ref.childSessionID,
		},
		TaskProgress: &llm.TaskProgressMessage{
			Type:         "system",
			Subtype:      "task_progress",
			TaskID:       ref.taskID,
			Description:  strings.TrimSpace(description),
			LastToolName: toolName,
			LastPath:     lastPath,
			SessionID:    ref.childSessionID,
		},
	}, true
}

func (p *Protocol) parseNotification(method string, params json.RawMessage) (llm.SDKMessage, bool) {
	switch method {
	case "configWarning", "deprecationNotice", "warning", "remoteControl/status/changed":
		// App-server emits these diagnostic/status notifications during a
		// successful handshake, including configWarning for unknown
		// defense-in-depth overrides on older CLIs. They carry no model, tool,
		// approval, or child-session activity. Server-initiated requests and
		// all unknown notifications remain fail-closed below.
		return llm.SDKMessage{}, false

	case "item/agentMessage/delta":
		var delta AgentMessageDelta
		if err := json.Unmarshal(params, &delta); err != nil {
			p.logDebug("[codex] failed to parse agent message delta: %v", err)
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed item/agentMessage/delta notification"), true
			}
			return llm.SDKMessage{}, false
		}
		if p.opts.NativeToollessReview && (delta.ThreadID == "" || delta.TurnID == "" || delta.ItemID == "") {
			return p.nativeToollessViolation("malformed item/agentMessage/delta notification"), true
		}
		if p.opts.NativeToollessReview {
			if detail := p.nativeToollessTurnMismatch(delta.ThreadID, delta.TurnID); detail != "" {
				return p.nativeToollessViolation(detail), true
			}
		}

		p.mu.Lock()
		isMain := p.isMainThread(delta.ThreadID)
		if p.deltaBuf == nil {
			p.deltaBuf = make(map[string]string)
		}
		p.deltaBuf[delta.ItemID] += delta.Delta
		accumulated := p.deltaBuf[delta.ItemID]
		if isMain {
			p.lastAssistantDraft = accumulated
		}
		previousText := p.lastAssistantText
		p.mu.Unlock()

		if !isMain {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("unexpected child-thread agent message activity"), true
			}
			return p.taskProgressForChildThread(delta.ThreadID, "", "", accumulated)
		}

		// Hidden review consumes only the completed final item. Codex deltas are
		// cumulative snapshots, so forwarding them would concatenate prefixes
		// (A + AL + ALLOW) and corrupt the exact-token decision.
		if p.opts.NativeToollessReview {
			return llm.SDKMessage{}, false
		}

		if shouldSuppressFinalAnswerDelta(delta.Phase, previousText, accumulated) {
			return llm.SDKMessage{}, false
		}

		return llm.SDKMessage{
			Type:    codexRoleAssistant,
			Subtype: "partial",
			Assistant: &llm.AssistantMessage{
				Type:    codexRoleAssistant,
				Subtype: "partial",
				Message: llm.ConversationMsg{
					Role: codexRoleAssistant,
					Content: []llm.ContentBlock{
						{Type: "text", Text: accumulated},
					},
				},
			},
		}, true

	case "turn/completed":
		var completed TurnCompletedParams
		if err := json.Unmarshal(params, &completed); err != nil {
			p.logDebug("[codex] failed to parse turn/completed: %v", err)
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed turn/completed notification"), true
			}
			return llm.SDKMessage{}, false
		}
		if p.opts.NativeToollessReview &&
			(completed.ThreadID == "" || completed.Turn.ID == "" || completed.Turn.Status == "") {
			return p.nativeToollessViolation("malformed turn/completed notification"), true
		}
		if p.opts.NativeToollessReview {
			if detail := p.nativeToollessTurnMismatch(completed.ThreadID, completed.Turn.ID); detail != "" {
				return p.nativeToollessViolation(detail), true
			}
		}

		p.mu.Lock()
		isMainTurn := p.isMainThread(completed.ThreadID)
		p.mu.Unlock()
		if !isMainTurn {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("unexpected child-thread turn completion"), true
			}
			return p.taskProgressForChildThread(completed.ThreadID, "", "", "child turn "+completed.Turn.Status)
		}

		p.requestUsageRead()

		switch completed.Turn.Status {
		case "completed":
			p.mu.Lock()
			inTok := p.inputTokens
			cachedInTok := p.cachedInputTokens
			outTok := p.outputTokens
			costUSD := p.totalCostUSD
			decisionSeen := p.nativeDecisionSeen
			lastText := p.lastAssistantText
			if lastText == "" {
				lastText = p.lastAssistantDraft
			}
			p.mu.Unlock()

			if p.opts.NativeToollessReview {
				decision := strings.TrimSpace(lastText)
				if !decisionSeen || (decision != "ALLOW" && decision != "DEFER") {
					return p.nativeToollessViolation("malformed reviewer decision"), true
				}
				return p.withUsage(p.successResult(inTok, cachedInTok, outTok, costUSD)), true
			}

			return p.withUsage(p.successResult(inTok, cachedInTok, outTok, costUSD)), true

		case "failed":
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("review turn failed"), true
			}
			p.mu.Lock()
			inTok := p.inputTokens
			cachedInTok := p.cachedInputTokens
			outTok := p.outputTokens
			costUSD := p.totalCostUSD
			p.mu.Unlock()

			errMsg := fmt.Sprintf("codex turn failed: %s", completed.Turn.ID)
			if completed.Turn.Error != nil {
				errMsg = completed.Turn.Error.Message
				if completed.Turn.Error.ErrorInfo != nil {
					switch completed.Turn.Error.ErrorInfo.Type {
					case "ContextWindowExceeded":
						errMsg = "Context window exceeded"
					case "UsageLimitExceeded":
						errMsg = "Usage limit exceeded"
					case "ServerOverloaded":
						errMsg = "Server overloaded"
					}
				}
			}

			msg := llm.SDKMessage{
				Type:    "result",
				Subtype: "error",
				Result: &llm.ResultMessage{
					Type:         "result",
					Subtype:      "error",
					Result:       errMsg,
					IsError:      true,
					TotalCostUSD: costUSD,
				},
			}
			if inTok > 0 || outTok > 0 {
				msg.Result.Usage = &llm.Usage{
					InputTokens:          inTok,
					CacheReadInputTokens: cachedInTok,
					OutputTokens:         outTok,
				}
			}
			return p.withUsage(msg), true

		case "interrupted":
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("review turn interrupted"), true
			}
			p.mu.Lock()
			inTok := p.inputTokens
			cachedInTok := p.cachedInputTokens
			outTok := p.outputTokens
			costUSD := p.totalCostUSD
			p.mu.Unlock()

			msg := llm.SDKMessage{
				Type:    "result",
				Subtype: "error",
				Result: &llm.ResultMessage{
					Type:         "result",
					Subtype:      "error",
					Result:       "Turn interrupted",
					IsError:      true,
					TotalCostUSD: costUSD,
				},
			}
			if inTok > 0 || outTok > 0 {
				msg.Result.Usage = &llm.Usage{
					InputTokens:          inTok,
					CacheReadInputTokens: cachedInTok,
					OutputTokens:         outTok,
				}
			}
			return p.withUsage(msg), true

		default:
			p.logDebug("[codex] turn/completed with unknown status: %s", completed.Turn.Status)
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("unknown review turn status: " + completed.Turn.Status), true
			}
			return llm.SDKMessage{}, false
		}

	case "thread/tokenUsage/updated":
		var usage TokenUsageUpdatedParams
		if err := json.Unmarshal(params, &usage); err != nil {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed thread/tokenUsage/updated notification"), true
			}
			return llm.SDKMessage{}, false
		}
		if p.opts.NativeToollessReview {
			if usage.ThreadID == "" || usage.TurnID == "" {
				return p.nativeToollessViolation("malformed thread/tokenUsage/updated notification"), true
			}
			if detail := p.nativeToollessTurnMismatch(usage.ThreadID, usage.TurnID); detail != "" {
				return p.nativeToollessViolation(detail), true
			}
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		// Child threads have independent cumulative counters. Mixing them
		// with the root would corrupt both deltas and context usage.
		if !p.isMainThread(usage.ThreadID) {
			return llm.SDKMessage{}, false
		}
		p.updateTokenUsageLocked(usage.TokenUsage)
		return p.usageMessageLocked(), true

	case "item/commandExecution/outputDelta":
		var delta CommandOutputDelta
		if err := json.Unmarshal(params, &delta); err != nil {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed item/commandExecution/outputDelta notification"), true
			}
			return llm.SDKMessage{}, false
		}
		if p.opts.NativeToollessReview {
			if delta.ThreadID == "" || delta.TurnID == "" || delta.ItemID == "" {
				return p.nativeToollessViolation("malformed item/commandExecution/outputDelta notification"), true
			}
			return p.nativeToollessViolation("unexpected command activity"), true
		}
		return llm.SDKMessage{
			Type: "tool_progress",
			ToolProgress: &llm.ToolProgressMessage{
				Type:      "tool_progress",
				ToolUseID: delta.ItemID,
				ToolName:  "Bash",
				Data:      delta.Delta,
			},
		}, true

	case "item/fileChange/outputDelta":
		var delta FileChangeOutputDelta
		if err := json.Unmarshal(params, &delta); err != nil {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed item/fileChange/outputDelta notification"), true
			}
			return llm.SDKMessage{}, false
		}
		if p.opts.NativeToollessReview {
			if delta.ThreadID == "" || delta.TurnID == "" || delta.ItemID == "" {
				return p.nativeToollessViolation("malformed item/fileChange/outputDelta notification"), true
			}
			return p.nativeToollessViolation("unexpected file activity"), true
		}
		return llm.SDKMessage{
			Type: "tool_progress",
			ToolProgress: &llm.ToolProgressMessage{
				Type:      "tool_progress",
				ToolUseID: delta.ItemID,
				ToolName:  codexToolNameWrite,
				Data:      delta.Delta,
			},
		}, true

	case "item/reasoning/summaryTextDelta":
		var delta ReasoningSummaryDelta
		if err := json.Unmarshal(params, &delta); err != nil {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed item/reasoning/summaryTextDelta notification"), true
			}
			return llm.SDKMessage{}, false
		}
		if p.opts.NativeToollessReview {
			if delta.ThreadID == "" || delta.TurnID == "" || delta.ItemID == "" {
				return p.nativeToollessViolation("malformed item/reasoning/summaryTextDelta notification"), true
			}
			if detail := p.nativeToollessTurnMismatch(delta.ThreadID, delta.TurnID); detail != "" {
				return p.nativeToollessViolation(detail), true
			}
		}
		return llm.SDKMessage{
			Type:    codexRoleAssistant,
			Subtype: "partial",
			Assistant: &llm.AssistantMessage{
				Type:    codexRoleAssistant,
				Subtype: "partial",
				Message: llm.ConversationMsg{
					Role: codexRoleAssistant,
					Content: []llm.ContentBlock{
						{Type: "thinking", Thinking: delta.Delta},
					},
				},
			},
		}, true

	case "account/rateLimits/updated":
		var rl RateLimitsUpdated
		if err := json.Unmarshal(params, &rl); err != nil {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed account/rateLimits/updated notification"), true
			}
			return llm.SDKMessage{}, false
		}
		var retryMS float64
		message := "Rate limit info received"
		if rl.RateLimits.Primary != nil {
			message = fmt.Sprintf("Rate limit: %.1f%% used", rl.RateLimits.Primary.UsedPercent)
			resetsAt := time.Unix(rl.RateLimits.Primary.ResetsAt, 0)
			retryMS = float64(time.Until(resetsAt).Milliseconds())
			if retryMS < 0 {
				retryMS = 0
			}
		}
		return llm.SDKMessage{
			Type: "rate_limit",
			RateLimit: &llm.RateLimitMessage{
				Type:    "rate_limit",
				RetryMS: retryMS,
				Message: message,
			},
		}, true

	case "item/completed":
		var completed ItemCompletedParams
		if err := json.Unmarshal(params, &completed); err != nil {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed item/completed notification"), true
			}
			return llm.SDKMessage{}, false
		}
		if p.opts.NativeToollessReview &&
			(completed.ThreadID == "" || completed.TurnID == "" || completed.Item.ID == "" || completed.Item.Type == "") {
			return p.nativeToollessViolation("malformed item/completed notification"), true
		}
		if p.opts.NativeToollessReview {
			if detail := p.nativeToollessTurnMismatch(completed.ThreadID, completed.TurnID); detail != "" {
				return p.nativeToollessViolation(detail), true
			}
		}

		p.mu.Lock()
		isMainItem := p.isMainThread(completed.ThreadID)
		p.mu.Unlock()
		if !isMainItem {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("unexpected child-thread item completion"), true
			}
			return p.taskProgressForChildItem(completed.ThreadID, completed.Item)
		}

		switch completed.Item.Type {
		case "agentMessage":
			if strings.TrimSpace(completed.Item.Text) == "" {
				p.mu.Lock()
				if p.deltaBuf != nil {
					delete(p.deltaBuf, completed.Item.ID)
				}
				p.mu.Unlock()
				if p.opts.NativeToollessReview {
					return p.nativeToollessViolation("empty reviewer output"), true
				}
				return llm.SDKMessage{}, false
			}

			if p.opts.NativeToollessReview {
				decision := strings.TrimSpace(completed.Item.Text)
				if decision != "ALLOW" && decision != "DEFER" {
					return p.nativeToollessViolation("malformed reviewer output"), true
				}
				p.mu.Lock()
				decisionSeen := p.nativeDecisionSeen
				if !decisionSeen {
					p.nativeDecisionSeen = true
					p.lastAssistantText = completed.Item.Text
					p.lastAssistantDraft = completed.Item.Text
					if p.deltaBuf != nil {
						delete(p.deltaBuf, completed.Item.ID)
					}
				}
				p.mu.Unlock()
				if decisionSeen {
					return p.nativeToollessViolation("unexpected additional assistant completion"), true
				}
				return llm.SDKMessage{
					Type: codexRoleAssistant,
					Assistant: &llm.AssistantMessage{
						Type: codexRoleAssistant,
						Message: llm.ConversationMsg{
							Role: codexRoleAssistant,
							Content: []llm.ContentBlock{
								{Type: "text", Text: completed.Item.Text},
							},
						},
					},
				}, true
			}

			p.mu.Lock()
			previousText := p.lastAssistantText
			if p.deltaBuf != nil {
				delete(p.deltaBuf, completed.Item.ID)
			}
			if isDuplicateFinalAnswer(completed.Item.Phase, previousText, completed.Item.Text) {
				p.lastAssistantDraft = completed.Item.Text
				p.mu.Unlock()
				return llm.SDKMessage{}, false
			}
			p.lastAssistantText = completed.Item.Text
			p.lastAssistantDraft = completed.Item.Text
			p.mu.Unlock()

			return llm.SDKMessage{
				Type: codexRoleAssistant,
				Assistant: &llm.AssistantMessage{
					Type: codexRoleAssistant,
					Message: llm.ConversationMsg{
						Role: codexRoleAssistant,
						Content: []llm.ContentBlock{
							{Type: "text", Text: completed.Item.Text},
						},
					},
				},
			}, true
		case "commandExecution":
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("unexpected command activity"), true
			}
			return llm.SDKMessage{
				Type: "tool_progress",
				ToolProgress: &llm.ToolProgressMessage{
					Type:      "tool_progress",
					ToolUseID: completed.Item.ID,
					ToolName:  "Bash",
					Data:      completed.Item.AggregatedOutput,
				},
				FileReads: p.fileReadEventsForCommand(completed.Item),
			}, true
		case codexItemTypeFileChange:
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("unexpected file activity"), true
			}
			return llm.SDKMessage{
				Type: "tool_progress",
				ToolProgress: &llm.ToolProgressMessage{
					Type:      "tool_progress",
					ToolUseID: completed.Item.ID,
					ToolName:  codexToolNameWrite,
				},
				FileChanges: fileChangeEventsForItem(completed.Item),
			}, true
		case codexItemTypeCollabAgentToolCall:
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("unexpected child agent activity"), true
			}
			return p.taskNotificationForCollab(completed.Item), true
		default:
			if p.opts.NativeToollessReview && completed.Item.Type != "userMessage" && completed.Item.Type != "reasoning" {
				return p.nativeToollessViolation("unexpected item activity: " + completed.Item.Type), true
			}
			return llm.SDKMessage{}, false
		}

	case "item/started":
		var started ItemStartedParams
		if err := json.Unmarshal(params, &started); err != nil {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed item/started notification"), true
			}
			return llm.SDKMessage{}, false
		}
		if p.opts.NativeToollessReview &&
			(started.ThreadID == "" || started.TurnID == "" || started.Item.ID == "" || started.Item.Type == "") {
			return p.nativeToollessViolation("malformed item/started notification"), true
		}
		if p.opts.NativeToollessReview {
			if detail := p.nativeToollessTurnMismatch(started.ThreadID, started.TurnID); detail != "" {
				return p.nativeToollessViolation(detail), true
			}
		}
		p.mu.Lock()
		isMainItem := p.isMainThread(started.ThreadID)
		p.mu.Unlock()
		if !isMainItem {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("unexpected child-thread item activity"), true
			}
			return p.taskProgressForChildItem(started.ThreadID, started.Item)
		}
		switch started.Item.Type {
		case "commandExecution":
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("unexpected command activity"), true
			}
			return llm.SDKMessage{
				Type: "tool_progress",
				ToolProgress: &llm.ToolProgressMessage{
					Type:      "tool_progress",
					ToolUseID: started.Item.ID,
					ToolName:  "Bash",
				},
			}, true
		case codexItemTypeFileChange:
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("unexpected file activity"), true
			}
			return llm.SDKMessage{
				Type: "tool_progress",
				ToolProgress: &llm.ToolProgressMessage{
					Type:      "tool_progress",
					ToolUseID: started.Item.ID,
					ToolName:  codexToolNameWrite,
				},
			}, true
		case codexItemTypeCollabAgentToolCall:
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("unexpected child agent activity"), true
			}
			if len(started.Item.ReceiverThreadIDs) == 0 {
				return llm.SDKMessage{}, false
			}
			return p.taskStartedForCollab(started.Item), true
		default:
			if p.opts.NativeToollessReview && started.Item.Type != "userMessage" &&
				started.Item.Type != "agentMessage" && started.Item.Type != "reasoning" {
				return p.nativeToollessViolation("unexpected item activity: " + started.Item.Type), true
			}
			return llm.SDKMessage{}, false
		}

	case "model/rerouted":
		if p.opts.NativeToollessReview {
			return p.nativeToollessViolation("unexpected model rerouting"), true
		}
		var rerouted struct {
			ThreadID string `json:"threadId"`
			ToModel  string `json:"toModel"`
		}
		if json.Unmarshal(params, &rerouted) != nil {
			return llm.SDKMessage{}, false
		}
		p.mu.Lock()
		if p.isMainThread(rerouted.ThreadID) && rerouted.ToModel != "" {
			p.pricingModel = rerouted.ToModel
		}
		p.mu.Unlock()
		return llm.SDKMessage{}, false

	case "turn/started":
		var turnStarted struct {
			ThreadID string `json:"threadId"`
			Turn     Turn   `json:"turn"`
		}
		if err := json.Unmarshal(params, &turnStarted); err != nil {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed turn/started notification"), true
			}
			return llm.SDKMessage{}, false
		}
		if p.opts.NativeToollessReview && (turnStarted.ThreadID == "" || turnStarted.Turn.ID == "") {
			return p.nativeToollessViolation("malformed turn/started notification"), true
		}
		p.mu.Lock()
		isMain := turnStarted.ThreadID == "" || p.threadID == "" || turnStarted.ThreadID == p.threadID
		alreadyStarted := p.turnStarted
		if isMain {
			p.usageState.revision++
			p.turnID = turnStarted.Turn.ID
		}
		if isMain && !alreadyStarted {
			p.turnStarted = true
			p.nativeReviewTurnID = turnStarted.Turn.ID
			p.nativeDecisionSeen = false
			p.lastAssistantText = ""
			p.lastAssistantDraft = ""
		}
		p.mu.Unlock()
		if p.opts.NativeToollessReview && !isMain {
			return p.nativeToollessViolation("unexpected child-thread turn activity"), true
		}
		if p.opts.NativeToollessReview && isMain && alreadyStarted {
			return p.nativeToollessViolation("unexpected extra turn"), true
		}
		return llm.SDKMessage{}, false

	case "thread/started":
		var started struct {
			Thread Thread `json:"thread"`
		}
		if err := json.Unmarshal(params, &started); err != nil {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed thread/started notification"), true
			}
			return llm.SDKMessage{}, false
		}
		if p.opts.NativeToollessReview {
			p.mu.Lock()
			mainThread := p.threadID
			p.mu.Unlock()
			if started.Thread.ID == "" {
				return p.nativeToollessViolation("malformed thread/started notification"), true
			}
			if mainThread != "" && started.Thread.ID != mainThread {
				return p.nativeToollessViolation("unexpected child thread"), true
			}
		}
		return llm.SDKMessage{}, false

	case "thread/status/changed":
		var changed struct {
			ThreadID string `json:"threadId"`
		}
		if err := json.Unmarshal(params, &changed); err != nil {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed thread/status/changed notification"), true
			}
			return llm.SDKMessage{}, false
		}
		if p.opts.NativeToollessReview {
			p.mu.Lock()
			mainThread := p.threadID
			p.mu.Unlock()
			if changed.ThreadID == "" {
				return p.nativeToollessViolation("malformed thread/status/changed notification"), true
			}
			if mainThread != "" && changed.ThreadID != mainThread {
				return p.nativeToollessViolation("unexpected child-thread status activity"), true
			}
		}
		return llm.SDKMessage{}, false

	case "error":
		var errNotif ErrorNotification
		if err := json.Unmarshal(params, &errNotif); err != nil {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("malformed error notification"), true
			}
			return llm.SDKMessage{}, false
		}
		if p.opts.NativeToollessReview && errNotif.Error.Message == "" {
			return p.nativeToollessViolation("malformed error notification"), true
		}
		errText := errNotif.Error.Message
		if errNotif.Error.ErrorInfo != nil && errNotif.Error.ErrorInfo.RawKind != "" {
			errText = fmt.Sprintf("%s (%s)", errText, errNotif.Error.ErrorInfo.RawKind)
		}
		if p.opts.NativeToollessReview {
			return p.nativeToollessViolation("provider error: " + errText), true
		}
		return llm.SDKMessage{
			Type:    codexRoleAssistant,
			Subtype: "partial",
			Assistant: &llm.AssistantMessage{
				Type:    codexRoleAssistant,
				Subtype: "partial",
				Message: llm.ConversationMsg{
					Role: codexRoleAssistant,
					Content: []llm.ContentBlock{
						{Type: "text", Text: fmt.Sprintf("[codex error] %s", errText)},
					},
				},
			},
		}, true

	default:
		p.logDebug("[codex] unhandled notification: %s", method)
		if p.opts.NativeToollessReview {
			return p.nativeToollessViolation("unexpected notification: " + method), true
		}
		return llm.SDKMessage{}, false
	}
}

func ptrSandboxMode(mode SandboxMode) *SandboxMode {
	return &mode
}

func nativeToollessThreadConfig() map[string]interface{} {
	return map[string]interface{}{
		"web_search":  "disabled",
		"mcp_servers": map[string]interface{}{},
		"plugins":     map[string]interface{}{},
		"features": map[string]interface{}{
			"shell_tool":               false,
			"multi_agent":              false,
			"apps":                     false,
			"plugins":                  false,
			"connectors":               false,
			"web_search":               false,
			"standalone_web_search":    false,
			"web_search_request":       false,
			"search_tool":              false,
			"tool_search":              false,
			"tool_suggest":             false,
			"request_permissions_tool": false,
			"memory_tool":              false,
			"goals":                    false,
			"image_generation":         false,
			"computer_use":             false,
			"browser_use":              false,
			"in_app_browser":           false,
			"js_repl":                  false,
			"code_mode":                false,
		},
		"tools": map[string]interface{}{
			"update_plan":                     map[string]interface{}{"enabled": false},
			"experimental_request_user_input": map[string]interface{}{"enabled": false},
		},
		"skills": map[string]interface{}{
			"bundled":              map[string]interface{}{"enabled": false},
			"include_instructions": false,
		},
		"agents":                                  map[string]interface{}{},
		"include_apps_instructions":               false,
		"include_environment_context":             false,
		"include_permissions_instructions":        false,
		"include_collaboration_mode_instructions": false,
	}
}

func (p *Protocol) nativeToollessViolation(detail string) llm.SDKMessage {
	p.markNativeToollessFailed()
	return llm.SDKMessage{
		Type:    "result",
		Subtype: "error",
		Result: &llm.ResultMessage{
			Type:    "result",
			Subtype: "error",
			Result:  "Codex native tool-less review failed closed: " + detail,
			IsError: true,
		},
	}
}

func (p *Protocol) markNativeToollessFailed() {
	p.mu.Lock()
	p.nativeReviewFailed = true
	p.mu.Unlock()
}

func (p *Protocol) nativeToollessTurnMismatch(threadID, turnID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.threadID != "" && threadID != p.threadID {
		return "unexpected child-thread turn activity"
	}
	if !p.turnStarted || p.nativeReviewTurnID == "" {
		return "review activity before turn started"
	}
	if turnID != p.nativeReviewTurnID {
		return "review activity for unexpected turn"
	}
	return ""
}

func (p *Protocol) successResult(inputTokens, cachedInputTokens, outputTokens int, costUSD float64) llm.SDKMessage {
	msg := llm.SDKMessage{
		Type:    "result",
		Subtype: "success",
		Result: &llm.ResultMessage{
			Type:         "result",
			Subtype:      "success",
			TotalCostUSD: costUSD,
		},
	}
	if inputTokens > 0 || outputTokens > 0 {
		msg.Result.Usage = &llm.Usage{
			InputTokens:          inputTokens,
			CacheReadInputTokens: cachedInputTokens,
			OutputTokens:         outputTokens,
		}
	}
	return msg
}

// --- Helper functions ---

func (p *Protocol) fileReadEventsForCommand(item ItemUnion) []llm.FileReadEvent {
	if len(item.CommandActions) == 0 {
		return nil
	}
	var events []llm.FileReadEvent
	for _, action := range item.CommandActions {
		if action.Type != "read" || action.Path == "" {
			continue
		}
		if item.ID != "" {
			key := item.ID + "\x00" + action.Path
			p.mu.Lock()
			if p.fileReadSeen == nil {
				p.fileReadSeen = make(map[string]struct{})
			}
			_, seen := p.fileReadSeen[key]
			if !seen {
				p.fileReadSeen[key] = struct{}{}
			}
			p.mu.Unlock()
			if seen {
				continue
			}
		}
		event := llm.FileReadEvent{
			FilePath:       action.Path,
			Source:         "codex.command_action",
			ProviderItemID: item.ID,
		}
		if item.ExitCode != nil {
			exitCode := *item.ExitCode
			event.ExitCode = &exitCode
		}
		events = append(events, event)
	}
	return events
}

func fileChangeEventsForItem(item ItemUnion) []llm.FileChangeEvent {
	if len(item.Changes) == 0 {
		return nil
	}
	events := make([]llm.FileChangeEvent, 0, len(item.Changes))
	for _, change := range item.Changes {
		path := strings.TrimSpace(change.Path)
		if path == "" {
			continue
		}
		detail := strings.TrimSpace(change.Diff)
		added, removed := countDiffPatchLines(detail)
		events = append(events, llm.FileChangeEvent{
			Path:         path,
			OldPath:      strings.TrimSpace(change.Kind.MovePath),
			Operation:    normalizeFileChangeOperation(change.Kind.Type),
			Detail:       detail,
			AddedLines:   added,
			RemovedLines: removed,
			HasDiffPatch: detail != "",
		})
	}
	return events
}

func normalizeFileChangeOperation(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "add", "create", codexFileChangeOperationWrite:
		return codexFileChangeOperationWrite
	case "delete", "remove":
		return "delete"
	case "move", "rename":
		return "rename"
	case codexFileChangeOperationUpdate, "modify", "modified":
		return codexFileChangeOperationUpdate
	default:
		return codexFileChangeOperationUpdate
	}
}

func countDiffPatchLines(patch string) (added, removed int) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

func shouldSuppressFinalAnswerDelta(phase, previousText, currentText string) bool {
	if phase != "final_answer" {
		return false
	}
	previousText = strings.TrimSpace(previousText)
	currentText = strings.TrimSpace(currentText)
	if previousText == "" || currentText == "" {
		return false
	}
	return strings.HasPrefix(previousText, currentText)
}

func isDuplicateFinalAnswer(phase, previousText, currentText string) bool {
	if phase != "final_answer" {
		return false
	}
	previousText = strings.TrimSpace(previousText)
	currentText = strings.TrimSpace(currentText)
	return previousText != "" && previousText == currentText
}

// --- Test helpers ---

// SetThreadIDForTest sets the thread ID for testing without a real handshake.
func (p *Protocol) SetThreadIDForTest(threadID string) {
	p.mu.Lock()
	p.threadID = threadID
	p.mu.Unlock()
}

// WritableRootsForTest returns the WritableRoots from the protocol options.
func (p *Protocol) WritableRootsForTest() []string {
	return p.opts.WritableRoots
}

// SystemPromptForTest returns the SystemPrompt from the protocol options.
func (p *Protocol) SystemPromptForTest() string {
	return p.opts.SystemPrompt
}

func computeCost(model string, inputTokens, cachedInputTokens, cacheWriteTokens, outputTokens int) float64 {
	return computeCostForContext(model, inputTokens, cachedInputTokens, cacheWriteTokens, outputTokens, inputTokens)
}

func computeCostForContext(model string, inputTokens, cachedInputTokens, cacheWriteTokens, outputTokens, contextInputTokens int) float64 {
	r, ok := lookupRate(model)
	if !ok {
		return 0
	}
	inputTokens = max(inputTokens, 0)
	cachedInputTokens = max(cachedInputTokens, 0)
	cacheWriteTokens = max(cacheWriteTokens, 0)
	outputTokens = max(outputTokens, 0)
	if cachedInputTokens > inputTokens {
		cachedInputTokens = inputTokens
	}
	inputRate := r.inputPerMToken
	cachedInputRate := r.cachedInputPerMToken
	cacheWriteRate := r.cacheWritePerMToken
	outputRate := r.outputPerMToken
	if contextInputTokens > longContextInputThreshold && r.longInputPerMToken > 0 {
		inputRate = r.longInputPerMToken
		cachedInputRate = r.longCachedInputPerMToken
		cacheWriteRate = r.longCacheWritePerMToken
		outputRate = r.longOutputPerMToken
	}
	if cacheWriteRate == 0 {
		cacheWriteTokens = 0
	} else if cacheWriteTokens > inputTokens-cachedInputTokens {
		cacheWriteTokens = inputTokens - cachedInputTokens
	}
	uncachedInputTokens := inputTokens - cachedInputTokens - cacheWriteTokens

	return (float64(uncachedInputTokens)/1_000_000)*inputRate +
		(float64(cachedInputTokens)/1_000_000)*cachedInputRate +
		(float64(cacheWriteTokens)/1_000_000)*cacheWriteRate +
		(float64(outputTokens)/1_000_000)*outputRate
}
