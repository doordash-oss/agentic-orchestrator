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
	"os"
	"regexp"
	"sort"
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
	threadID           string
	turnID             string
	model              string
	approvalPolicy     string
	dangerFullAccess   bool
	inputTokens        int
	cachedInputTokens  int
	outputTokens       int
	totalCostUSD       float64
	modelContextWindow int
	deltaBuf           map[string]string
	questionIDs        map[string]string
	turnHadToolUse     bool
	lastAssistantText  string
	lastAssistantDraft string
	formatRetryCount   int
	fileReadSeen       map[string]struct{}
	turnStarted        bool
	nativeReviewTurnID string
	nativeDecisionSeen bool
	nativeReviewFailed bool

	logFunc func(string, ...interface{})
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
		if p.opts.NativeToollessReview && len(env.Error) > 0 && string(env.Error) != "null" {
			return []llm.SDKMessage{p.nativeToollessViolation("JSON-RPC error response")}, nil
		}
		if msg, emit, handled := p.handleResponse(*env.ID, env.Result, env.Error); handled {
			if emit {
				return []llm.SDKMessage{msg}, nil
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
			return []llm.SDKMessage{msg}, nil
		}
		return nil, nil
	}

	// Notification (method only, no id)
	if env.Method != "" {
		msg, ok := p.parseNotification(env.Method, env.Params)
		if ok {
			return []llm.SDKMessage{msg}, nil
		}
		return nil, nil
	}

	p.logDebug("[codex] unrecognized JSON-RPC message (no method or id)")
	if p.opts.NativeToollessReview {
		return []llm.SDKMessage{p.nativeToollessViolation("malformed JSON-RPC envelope")}, nil
	}
	return nil, nil
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

// RespondToAskUser sends a response to an AskUserQuestion request.
// annotations is accepted for parity with the Claude protocol but is not
// forwarded — Codex's native ask-user wire format carries a single answer
// string per question with no side-channel for notes.
func (p *Protocol) RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, _ map[string]llm.AskUserAnnotation) error {
	// Synthetic ask-user requests don't have a pending JSON-RPC server request,
	// so the answer must be delivered as a new follow-up turn. A bare answer
	// like "Replace README.md" is indistinguishable from a fresh directive in
	// that channel, so we wrap it with the original question and options.
	if strings.HasPrefix(requestID, "codex-synthetic-") {
		return p.sendFollowUpTurn(buildAskUserAnswerEnvelope(questions, answers))
	}
	return p.respondToAskUser(requestID, answers)
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
				Title:   "Agentic Workflow Orchestrator",
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
	id := int(nextID.Add(1))

	if p.opts.NativeToollessReview && p.opts.ResumeSessionID != "" {
		return fmt.Errorf("Codex native tool-less review cannot resume a persisted thread")
	}

	// Resume a persisted thread instead of starting a fresh one. The
	// response carries the same {thread:{id}} shape as thread/start, so
	// handleResponse closes threadReady for both paths. Model, approval
	// policy, and sandbox are re-supplied per-turn by turn/start.
	if p.opts.ResumeSessionID != "" {
		req := Request{
			JSONRPC: "2.0",
			Method:  "thread/resume",
			ID:      id,
			Params: ThreadResumeParams{
				ThreadID: p.opts.ResumeSessionID,
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
			Model:          model,
			Cwd:            p.opts.WorkDir,
			ApprovalPolicy: p.approvalPolicy,
			Sandbox:        &sandbox,
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
	systemPrompt := p.opts.SystemPrompt
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

	collabSettings := CollaborationSettings{
		Model: model,
	}
	if systemPrompt != "" {
		collabSettings.DeveloperInstructions = &systemPrompt
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
			CollaborationMode: &CollaborationMode{
				Mode:     "default",
				Settings: collabSettings,
			},
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
			CollaborationMode: &CollaborationMode{
				Mode: "default",
				Settings: CollaborationSettings{
					Model: model,
				},
			},
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

func (p *Protocol) respondToAskUser(requestID string, answers map[string]string) error {
	id, err := strconv.Atoi(requestID)
	if err != nil {
		return fmt.Errorf("invalid codex request ID %q: %w", requestID, err)
	}

	p.mu.Lock()
	qIDs := p.questionIDs
	p.mu.Unlock()

	var codexAnswers []AskUserAnswer
	keys := make([]string, 0, len(answers))
	for q := range answers {
		keys = append(keys, q)
	}
	sort.Strings(keys)
	for _, q := range keys {
		questionID := qIDs[q]
		if questionID == "" {
			questionID = q
		}
		codexAnswers = append(codexAnswers, AskUserAnswer{
			QuestionID: questionID,
			Value:      answers[q],
		})
	}

	return p.writeJSON(Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  AskUserResult{Answers: codexAnswers},
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
		p.mu.Lock()
		p.threadID = threadResult.Thread.ID
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
		return llm.SDKMessage{
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
		}, true

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
		return llm.SDKMessage{
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
		}, true

	case "tool/requestUserInput":
		var uiParams UserInputRequestParams
		if err := json.Unmarshal(params, &uiParams); err != nil {
			p.logDebug("[codex] failed to parse tool/requestUserInput: %v", err)
			return llm.SDKMessage{}, false
		}

		seen := make(map[string]int)
		qIDMap := make(map[string]string)
		type claudeOption struct {
			Label       string   `json:"label"`
			Description string   `json:"description"`
			Confidence  *float64 `json:"confidence,omitempty"`
		}
		type claudeQuestion struct {
			Question    string         `json:"question"`
			Header      string         `json:"header"`
			MultiSelect bool           `json:"multiSelect"`
			Options     []claudeOption `json:"options"`
		}

		var claudeQuestions []claudeQuestion
		for _, q := range uiParams.Questions {
			displayLabel := q.Label
			seen[q.Label]++
			if seen[q.Label] > 1 {
				displayLabel = fmt.Sprintf("%s (#%d)", q.Label, seen[q.Label])
			}
			qIDMap[displayLabel] = q.ID

			var opts []claudeOption
			if q.Type == "select" {
				for _, o := range q.Options {
					opts = append(opts, claudeOption{Label: o, Description: ""})
				}
			}
			if opts == nil {
				opts = []claudeOption{}
			}

			claudeQuestions = append(claudeQuestions, claudeQuestion{
				Question: displayLabel,
				Header:   "",
				// Codex's native answer shape is a single Value string per question,
				// so multi-select is intentionally not propagated from the Codex side.
				MultiSelect: false,
				Options:     opts,
			})
		}

		p.mu.Lock()
		p.questionIDs = qIDMap
		p.mu.Unlock()

		inputMap := map[string]interface{}{
			"questions": claudeQuestions,
		}
		inputJSON, _ := json.Marshal(inputMap)

		return llm.SDKMessage{
			Type:    "control_request",
			Subtype: "can_use_tool",
			ControlRequest: &llm.ControlRequestMessage{
				Type:      "control_request",
				RequestID: strconv.Itoa(id),
				Request: llm.ControlRequest{
					Subtype:  "can_use_tool",
					ToolName: "AskUserQuestion",
					Input:    json.RawMessage(inputJSON),
				},
			},
		}, true

	default:
		p.logDebug("[codex] unhandled server request: %s", method)
		return llm.SDKMessage{}, false
	}
}

func (p *Protocol) isMainThread(threadID string) bool {
	mainThread := p.threadID
	return threadID == "" || mainThread == "" || threadID == mainThread
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
			return llm.SDKMessage{}, false
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
			return llm.SDKMessage{}, false
		}

		switch completed.Turn.Status {
		case "completed":
			p.mu.Lock()
			inTok := p.inputTokens
			cachedInTok := p.cachedInputTokens
			outTok := p.outputTokens
			costUSD := p.totalCostUSD
			hadToolUse := p.turnHadToolUse
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
				return p.successResult(inTok, cachedInTok, outTok, costUSD), true
			}

			// The entire text-parsed AskUserQuestion pipeline below exists only to
			// imitate Claude's native AskUserQuestion tool call for a provider whose
			// questions are otherwise just plain text. Interactive sessions (a human
			// answers every turn directly, e.g. AMA chat) get no benefit from that
			// imitation — the human can read the model's question and reply with an
			// ordinary chat message exactly as they would with bare Codex — so they
			// always fall through to a normal completion below and never synthesize
			// a picker.
			if !p.opts.Interactive && !textContainsVerdictSentinel(lastText) {
				if stripped, ok := trimFreeFormSentinel(lastText); ok {
					p.mu.Lock()
					p.formatRetryCount = 0
					p.mu.Unlock()
					return p.synthesizeAskUser(stripped, nil), true
				}

				// A numbered list is only a candidate AskUserQuestion when its stem
				// actually reads like a question or its options carry
				// question-contract markers (confidence scores / "(Recommended)").
				// An informational list ("Here's what I found: 1. ... 2. ...") has
				// neither and is a normal completion; it must not be forced through
				// the question pipeline just because it enumerates items.
				if stem, options, ok := parseNumberedOptions(lastText); ok && (stemLooksLikeQuestion(stem) || optionsCarryQuestionContract(options)) {
					p.mu.Lock()
					p.formatRetryCount = 0
					p.mu.Unlock()
					return p.synthesizeAskUser(stem, options), true
				}

				if (textLooksLikeQuestion(lastText) || textCarriesQuestionContract(lastText)) && p.shouldReformatRetryLoose(hadToolUse) {
					p.mu.Lock()
					retry := p.formatRetryCount
					p.mu.Unlock()

					if retry < maxQuestionFormatRetries {
						if err := p.sendFollowUpTurn(questionFormatReminder(lastText)); err != nil {
							p.logDebug("[codex] failed to send reformat reminder: %v", err)
						} else {
							p.mu.Lock()
							p.formatRetryCount++
							p.mu.Unlock()
							return llm.SDKMessage{}, false
						}
					}

					p.mu.Lock()
					p.formatRetryCount = 0
					p.mu.Unlock()
					return p.synthesizeAskUser(lastText, nil), true
				}
			}

			p.mu.Lock()
			p.formatRetryCount = 0
			p.mu.Unlock()

			return p.successResult(inTok, cachedInTok, outTok, costUSD), true

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
			return msg, true

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
			return msg, true

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
		// Total = lifetime cumulative (for cost calculation on turn completion).
		// Last = current context fill (resets on compaction; use for context %).
		p.mu.Lock()
		inputDelta := usage.TokenUsage.Total.InputTokens - p.inputTokens
		cachedInputDelta := usage.TokenUsage.Total.CachedInputTokens - p.cachedInputTokens
		outputDelta := usage.TokenUsage.Total.OutputTokens - p.outputTokens
		if inputDelta >= 0 && cachedInputDelta >= 0 && outputDelta >= 0 {
			// The app-server protocol does not expose cache-write token counts,
			// so cache writes remain zero until that telemetry becomes available.
			p.totalCostUSD += computeCostForContext(
				p.model,
				inputDelta,
				cachedInputDelta,
				0,
				outputDelta,
				usage.TokenUsage.Last.InputTokens,
			)
		}
		p.inputTokens = usage.TokenUsage.Total.InputTokens
		p.cachedInputTokens = usage.TokenUsage.Total.CachedInputTokens
		p.outputTokens = usage.TokenUsage.Total.OutputTokens
		if usage.TokenUsage.ModelContextWindow != nil {
			p.modelContextWindow = *usage.TokenUsage.ModelContextWindow
		}
		ctxWindow := p.modelContextWindow
		p.mu.Unlock()

		// Surface as synthetic SDKMessage so session layer can accumulate.
		// Total = lifetime cumulative (for cost); Last = current context fill
		// (resets on compaction, for context % display).
		//
		// ContextTotalTokens carries Last.TotalTokens (input + output +
		// reasoning) so the session's ContextPercentage() can match Codex's
		// own `/status` formula: (total - baseline) / (window - baseline).
		return llm.SDKMessage{
			Type: "usage_update",
			UsageUpdate: &llm.Usage{
				InputTokens:          usage.TokenUsage.Total.InputTokens,
				CacheReadInputTokens: usage.TokenUsage.Total.CachedInputTokens,
				OutputTokens:         usage.TokenUsage.Total.OutputTokens,
				ContextInputTokens:   usage.TokenUsage.Last.InputTokens,
				ContextTotalTokens:   usage.TokenUsage.Last.TotalTokens,
				ContextBaseline:      codexContextBaselineTokens,
				ContextWindow:        ctxWindow,
			},
		}, true

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
		p.mu.Lock()
		p.turnHadToolUse = true
		p.mu.Unlock()
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
		p.mu.Lock()
		p.turnHadToolUse = true
		p.mu.Unlock()
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
			return llm.SDKMessage{}, false
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
		if isMainItem && (started.Item.Type == "commandExecution" || started.Item.Type == codexItemTypeFileChange) {
			p.turnHadToolUse = true
		}
		p.mu.Unlock()
		if !isMainItem {
			if p.opts.NativeToollessReview {
				return p.nativeToollessViolation("unexpected child-thread item activity"), true
			}
			return llm.SDKMessage{}, false
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
		default:
			if p.opts.NativeToollessReview && started.Item.Type != "userMessage" &&
				started.Item.Type != "agentMessage" && started.Item.Type != "reasoning" {
				return p.nativeToollessViolation("unexpected item activity: " + started.Item.Type), true
			}
			return llm.SDKMessage{}, false
		}

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
		if isMain && !alreadyStarted {
			p.turnStarted = true
			p.nativeReviewTurnID = turnStarted.Turn.ID
			p.nativeDecisionSeen = false
			p.turnHadToolUse = false
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

// textLooksLikeQuestion reports whether text's final utterance is a question.
// It checks the trailing character rather than scanning for '?' anywhere, so a
// completion that merely mentions a "?" mid-answer (or a follow-up offer tacked
// onto an otherwise-finished answer) is not misread as a blocking question.
func textLooksLikeQuestion(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	return strings.HasSuffix(text, "?")
}

// stemLooksLikeQuestion reports whether any sentence in a numbered-options
// stem ends with '?'. The question contract allows a short stem where the
// question leads and a declarative clarifier follows, so unlike
// textLooksLikeQuestion the '?' need not be the stem's final character.
func stemLooksLikeQuestion(stem string) bool {
	stem = strings.TrimSpace(stem)
	return strings.HasSuffix(stem, "?") ||
		strings.Contains(stem, "? ") ||
		strings.Contains(stem, "?\n")
}

// optionsCarryQuestionContract reports whether parsed options carry markers
// only the question contract produces — confidence scores or a
// "(Recommended)" label — which mark the list as a question regardless of
// stem punctuation.
func optionsCarryQuestionContract(options []parsedOption) bool {
	for _, o := range options {
		if o.Confidence != nil || strings.Contains(strings.ToLower(o.Label), "(recommended)") {
			return true
		}
	}
	return false
}

var confidenceMarkerRe = regexp.MustCompile(`(?i)\[confidence:\s*(?:0(?:\.\d+)?|1(?:\.0+)?)\]`)

// textCarriesQuestionContract reports whether final text that failed option
// parsing still carries question-contract confidence markers; such a turn is
// a malformed question and should get a reformat reminder rather than being
// misread as a silent completion.
func textCarriesQuestionContract(text string) bool {
	return confidenceMarkerRe.MatchString(text)
}

// verdictSentinelRe matches the structured `## Verdict` section the
// review / validator handoff emits when summarizing what the agent wrote to
// the review-feedback file. The token may be separated from the heading by
// either a newline (the canonical schema) or a space (when the surrounding
// numbered-list parser has flattened multi-line option bodies into a single
// line). Presence anywhere in the final text is a strong signal that the
// turn should be treated as a completed answer regardless of stray '?'
// characters in the preceding rationale.
var verdictSentinelRe = regexp.MustCompile(`## Verdict[\s]+(APPROVED|CHANGES_REQUESTED)\b`)

func textContainsVerdictSentinel(text string) bool {
	return verdictSentinelRe.MatchString(text)
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

func (p *Protocol) synthesizeAskUser(text string, options []parsedOption) llm.SDKMessage {
	requestID := fmt.Sprintf("codex-synthetic-%d", time.Now().UnixNano())

	opts := make([]map[string]any, 0, len(options))
	for _, o := range options {
		opt := map[string]any{
			"label":       o.Label,
			"description": o.Description,
		}
		if o.Confidence != nil {
			opt["confidence"] = *o.Confidence
		}
		opts = append(opts, opt)
	}

	questionsJSON, _ := json.Marshal([]map[string]interface{}{
		{
			"question":    text,
			"header":      "Agent Question",
			"multiSelect": false,
			"options":     opts,
		},
	})

	inputJSON, _ := json.Marshal(map[string]interface{}{
		"questions": json.RawMessage(questionsJSON),
	})

	return llm.SDKMessage{
		Type: "control_request",
		ControlRequest: &llm.ControlRequestMessage{
			Type:      "control_request",
			RequestID: requestID,
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "AskUserQuestion",
				Input:    json.RawMessage(inputJSON),
			},
		},
	}
}

// askUserOptionView is the subset of an AskUserQuestion option that the
// answer-envelope renderer needs to restate context to the agent.
type askUserOptionView struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// askUserQuestionView is the subset of an AskUserQuestion entry that the
// answer-envelope renderer needs to restate context to the agent.
type askUserQuestionView struct {
	Question string              `json:"question"`
	Options  []askUserOptionView `json:"options,omitempty"`
}

func buildAskUserAnswerEnvelope(questions json.RawMessage, answers map[string]string) string {
	parsed := parseAskUserQuestions(questions)
	byText := make(map[string]askUserQuestionView, len(parsed))
	for _, q := range parsed {
		byText[q.Question] = q
	}

	keys := make([]string, 0, len(answers))
	for q := range answers {
		keys = append(keys, q)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString("[AskUserQuestion answer]\n")
	sb.WriteString("The user has answered your question.\n")

	for _, q := range keys {
		sb.WriteString("\nQuestion you asked:\n> ")
		sb.WriteString(strings.ReplaceAll(strings.TrimSpace(q), "\n", "\n> "))
		sb.WriteString("\n")
		if qv, ok := byText[q]; ok && len(qv.Options) > 0 {
			sb.WriteString("\nOptions you presented:\n")
			for i, opt := range qv.Options {
				fmt.Fprintf(&sb, "  %d. %s", i+1, opt.Label)
				if opt.Description != "" {
					sb.WriteString(" — ")
					sb.WriteString(opt.Description)
				}
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\nUser's selected answer: ")
		sb.WriteString(answers[q])
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString("This answer clarifies requirements; it is not authorization to implement, edit repository files, or modify files outside your phase artifact/output directory.\n\n")
	sb.WriteString(askingFormatReminder)
	return strings.TrimSpace(sb.String())
}

const askingFormatReminder = `[Reminder] When you ask your next question, follow the asking-questions format from your system prompt.`

func parseAskUserQuestions(raw json.RawMessage) []askUserQuestionView {
	if len(raw) == 0 {
		return nil
	}
	var envelope struct {
		Questions []askUserQuestionView `json:"questions"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Questions) > 0 {
		return envelope.Questions
	}
	var bare []askUserQuestionView
	if err := json.Unmarshal(raw, &bare); err == nil && len(bare) > 0 {
		return bare
	}
	return nil
}

const maxQuestionFormatRetries = 2

func (p *Protocol) shouldReformatRetryLoose(hadToolUse bool) bool {
	if p.opts.MarkerPath == "" {
		return !hadToolUse
	}
	_, err := os.Stat(p.opts.MarkerPath)
	return err != nil
}

// parsedOption holds one numbered alternative extracted from a Codex question.
type parsedOption struct {
	Label       string
	Description string
	Confidence  *float64
}

var numberedOptionRe = regexp.MustCompile(`^\d+\.\s+(.+)$`)
var confidenceSuffixRe = regexp.MustCompile(`(?i)\s+\[confidence:\s*(0(?:\.\d+)?|1(?:\.0+)?)\]\s*$`)

func parseNumberedOptions(text string) (string, []parsedOption, bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	stem := make([]string, 0, len(lines))
	raw := make([]string, 0, 4)
	inOptions := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if !inOptions && len(stem) > 0 && stem[len(stem)-1] != "" {
				stem = append(stem, "")
			}
			continue
		}
		if m := numberedOptionRe.FindStringSubmatch(trimmed); m != nil {
			inOptions = true
			raw = append(raw, strings.TrimSpace(m[1]))
			continue
		}
		if inOptions {
			if len(raw) > 0 {
				raw[len(raw)-1] += " " + trimmed
			}
			continue
		}
		stem = append(stem, trimmed)
	}

	if len(raw) < 2 {
		return "", nil, false
	}
	questionLines := 0
	for _, r := range raw {
		if strings.Contains(r, "?") {
			questionLines++
		}
	}
	if questionLines == len(raw) {
		// Every option contains "?" — this is a bundle of questions, not a
		// stem + choices. Let the caller treat it as unformatted.
		return "", nil, false
	}
	for _, r := range raw {
		// An option body carrying a verdict sentinel means the numbered
		// list is a critic's "1. Assessment / 2. Verdict / 3. Feedback"
		// report, not a real AskUser — never surface it as options.
		if textContainsVerdictSentinel(r) {
			return "", nil, false
		}
	}

	options := make([]parsedOption, 0, len(raw))
	for _, r := range raw {
		trimmed, confidence := splitOptionConfidence(r)
		label, desc := splitOptionLabelDesc(trimmed)
		if label == "" {
			return "", nil, false
		}
		options = append(options, parsedOption{Label: label, Description: desc, Confidence: confidence})
	}

	cleaned := strings.TrimSpace(strings.Join(stem, "\n"))
	if cleaned == "" {
		cleaned = text
	}
	return cleaned, options, true
}

// trimFreeFormSentinel recognises the explicit "FREE_FORM:" opt-out that the
// reformat reminder teaches Codex to emit when a question genuinely needs a
// free-text answer. When present, we skip retry logic and surface the question
// with no options. Returns the stripped text and ok=true when the sentinel was
// found.
func trimFreeFormSentinel(text string) (string, bool) {
	trimmed := strings.TrimLeft(text, " \t\n\r")
	const prefix = "FREE_FORM:"
	if !strings.HasPrefix(trimmed, prefix) {
		return "", false
	}
	return strings.TrimSpace(trimmed[len(prefix):]), true
}

func splitOptionLabelDesc(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	label := raw
	desc := ""
	if idx := strings.Index(raw, ":"); idx >= 0 {
		label = strings.TrimSpace(raw[:idx])
		desc = strings.TrimSpace(raw[idx+1:])
	}
	label = strings.Trim(label, "`")
	label = strings.TrimSpace(label)
	return label, desc
}

func splitOptionConfidence(raw string) (string, *float64) {
	raw = strings.TrimSpace(raw)
	matches := confidenceSuffixRe.FindStringSubmatch(raw)
	if matches == nil {
		return raw, nil
	}
	confidence, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return raw, nil
	}
	trimmed := strings.TrimSpace(raw[:len(raw)-len(matches[0])])
	return trimmed, &confidence
}

// questionFormatReminder is the follow-up user turn sent to Codex when it
// emits a question that lacks the required numbered options.
func questionFormatReminder(violating string) string {
	return strings.Join([]string{
		"Your previous message was not in the required question format:",
		"",
		"> " + strings.ReplaceAll(strings.TrimSpace(violating), "\n", "\n> "),
		"",
		"Reformat and resend the question using exactly 3 numbered options, one marked (Recommended).",
		"Use this structure and output nothing else:",
		"",
		"<question stem ending with '?'>",
		"1. <Label> (Recommended): <one-line tradeoff> [confidence: 0.00]",
		"2. <Label>: <one-line tradeoff> [confidence: 0.00]",
		"3. <Label>: <one-line tradeoff> [confidence: 0.00]",
		"",
		"Only skip numbered options if the answer is inherently unconstrained (an exact version string, a free-form name, or an arbitrary identifier). In that case, prefix the question with the literal string 'FREE_FORM:' so the orchestrator knows it is intentional.",
	}, "\n")
}

// --- Test helpers ---

// SetThreadIDForTest sets the thread ID for testing without a real handshake.
func (p *Protocol) SetThreadIDForTest(threadID string) {
	p.mu.Lock()
	p.threadID = threadID
	p.mu.Unlock()
}

// SetQuestionIDsForTest sets the question ID map for testing.
func (p *Protocol) SetQuestionIDsForTest(qIDs map[string]string) {
	p.mu.Lock()
	p.questionIDs = qIDs
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
