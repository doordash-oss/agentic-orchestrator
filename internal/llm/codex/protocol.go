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
	inputTokens        int
	outputTokens       int
	modelContextWindow int
	deltaBuf           map[string]string
	questionIDs        map[string]string
	turnHadToolUse     bool
	lastAssistantText  string
	lastAssistantDraft string
	formatRetryCount   int
	fileReadSeen       map[string]struct{}

	logFunc func(string, ...interface{})
}

// NewProtocol creates a new Codex protocol handler.
func NewProtocol(opts llm.ProtocolOpts) *Protocol {
	policy := opts.ApprovalPolicy
	if policy == "" {
		if opts.DSP {
			policy = "never"
		} else {
			policy = "on-request"
		}
	}
	return &Protocol{
		opts:           opts,
		model:          llm.StripModelContextWindow(opts.Model),
		approvalPolicy: policy,
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
		return nil, nil
	}

	// Response to our request (has id but no method)
	if env.ID != nil && env.Method == "" {
		p.handleResponse(*env.ID, env.Result, env.Error)
		return nil, nil
	}

	// Server-initiated request (has both id and method)
	if env.ID != nil && env.Method != "" {
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
	return nil, nil
}

// SendUserMessage sends a follow-up turn with user text.
func (p *Protocol) SendUserMessage(text string) error {
	return p.sendFollowUpTurn(text)
}

// RespondToControl sends an allow/deny response for an approval request.
// originalInput is ignored for Codex (only used by Claude).
func (p *Protocol) RespondToControl(requestID string, allow bool, originalInput json.RawMessage, reason string) error {
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

// SessionID returns "" — Codex has no session ID concept.
func (p *Protocol) SessionID() string { return "" }

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

	sandbox := SandboxModeWorkspaceWrite
	if p.approvalPolicy == "never" {
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
	return p.writeJSON(req)
}

func (p *Protocol) startTurn(userPrompt string) error {
	id := int(nextID.Add(1))

	p.mu.Lock()
	threadID := p.threadID
	systemPrompt := p.opts.SystemPrompt
	writableRoots := append([]string(nil), p.opts.WritableRoots...)
	p.mu.Unlock()

	model := p.model

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
	if p.approvalPolicy == "never" {
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
			ApprovalPolicy: p.approvalPolicy,
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
	id := int(nextID.Add(1))

	p.mu.Lock()
	threadID := p.threadID
	policy := p.approvalPolicy
	model := p.model
	writableRoots := append([]string(nil), p.opts.WritableRoots...)
	p.mu.Unlock()

	if policy == "" {
		policy = "on-request"
	}

	sandbox := &SandboxPolicy{
		Type:          "workspaceWrite",
		WritableRoots: writableRoots,
		NetworkAccess: true,
	}
	if policy == "never" {
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

func (p *Protocol) handleResponse(id int, result, errData json.RawMessage) {
	if len(errData) > 0 && string(errData) != "null" {
		p.logDebug("[codex] error response for request %d: %s", id, string(errData))
		return
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
		return
	}

	var threadResult ThreadStartResult
	if err := json.Unmarshal(result, &threadResult); err == nil && threadResult.Thread.ID != "" {
		p.mu.Lock()
		p.threadID = threadResult.Thread.ID
		if p.threadReady != nil {
			select {
			case <-p.threadReady:
			default:
				close(p.threadReady)
			}
		}
		p.mu.Unlock()
		p.logDebug("[codex] thread started: %s", threadResult.Thread.ID)
		return
	}

	var turnResult TurnStartResult
	if err := json.Unmarshal(result, &turnResult); err == nil && turnResult.Turn.ID != "" {
		p.mu.Lock()
		p.turnID = turnResult.Turn.ID
		p.mu.Unlock()
		p.logDebug("[codex] turn started: %s (status=%s)", turnResult.Turn.ID, turnResult.Turn.Status)
		return
	}

	p.logDebug("[codex] unhandled response for request %d", id)
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
					ToolName: "Write",
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
	case "item/agentMessage/delta":
		var delta AgentMessageDelta
		if err := json.Unmarshal(params, &delta); err != nil {
			p.logDebug("[codex] failed to parse agent message delta: %v", err)
			return llm.SDKMessage{}, false
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
			return llm.SDKMessage{}, false
		}

		if shouldSuppressFinalAnswerDelta(delta.Phase, previousText, accumulated) {
			return llm.SDKMessage{}, false
		}

		return llm.SDKMessage{
			Type:    "assistant",
			Subtype: "partial",
			Assistant: &llm.AssistantMessage{
				Type:    "assistant",
				Subtype: "partial",
				Message: llm.ConversationMsg{
					Role: "assistant",
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
			return llm.SDKMessage{}, false
		}

		p.mu.Lock()
		isMainTurn := p.isMainThread(completed.ThreadID)
		p.mu.Unlock()
		if !isMainTurn {
			return llm.SDKMessage{}, false
		}

		switch completed.Turn.Status {
		case "completed":
			p.mu.Lock()
			model := p.model
			inTok := p.inputTokens
			outTok := p.outputTokens
			hadToolUse := p.turnHadToolUse
			lastText := p.lastAssistantText
			if lastText == "" {
				lastText = p.lastAssistantDraft
			}
			p.mu.Unlock()

			if !textContainsVerdictSentinel(lastText) {
				if stripped, ok := trimFreeFormSentinel(lastText); ok {
					p.mu.Lock()
					p.formatRetryCount = 0
					p.mu.Unlock()
					return p.synthesizeAskUser(stripped, nil), true
				}

				if stem, options, ok := parseNumberedOptions(lastText); ok {
					p.mu.Lock()
					p.formatRetryCount = 0
					p.mu.Unlock()
					return p.synthesizeAskUser(stem, options), true
				}

				if textLooksLikeQuestion(lastText) && p.shouldReformatRetryLoose(hadToolUse) {
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

			costUSD := computeCost(model, inTok, outTok)
			msg := llm.SDKMessage{
				Type:    "result",
				Subtype: "success",
				Result: &llm.ResultMessage{
					Type:         "result",
					Subtype:      "success",
					TotalCostUSD: costUSD,
				},
			}
			if inTok > 0 || outTok > 0 {
				msg.Result.Usage = &llm.Usage{InputTokens: inTok, OutputTokens: outTok}
			}
			return msg, true

		case "failed":
			p.mu.Lock()
			model := p.model
			inTok := p.inputTokens
			outTok := p.outputTokens
			p.mu.Unlock()

			costUSD := computeCost(model, inTok, outTok)
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
				msg.Result.Usage = &llm.Usage{InputTokens: inTok, OutputTokens: outTok}
			}
			return msg, true

		case "interrupted":
			p.mu.Lock()
			model := p.model
			inTok := p.inputTokens
			outTok := p.outputTokens
			p.mu.Unlock()

			costUSD := computeCost(model, inTok, outTok)
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
				msg.Result.Usage = &llm.Usage{InputTokens: inTok, OutputTokens: outTok}
			}
			return msg, true

		default:
			p.logDebug("[codex] turn/completed with unknown status: %s", completed.Turn.Status)
			return llm.SDKMessage{}, false
		}

	case "thread/tokenUsage/updated":
		var usage TokenUsageUpdatedParams
		if err := json.Unmarshal(params, &usage); err != nil {
			return llm.SDKMessage{}, false
		}
		// Total = lifetime cumulative (for cost calculation on turn completion).
		// Last = current context fill (resets on compaction; use for context %).
		p.mu.Lock()
		p.inputTokens = usage.TokenUsage.Total.InputTokens
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
				InputTokens:        usage.TokenUsage.Total.InputTokens,
				OutputTokens:       usage.TokenUsage.Total.OutputTokens,
				ContextInputTokens: usage.TokenUsage.Last.InputTokens,
				ContextTotalTokens: usage.TokenUsage.Last.TotalTokens,
				ContextBaseline:    codexContextBaselineTokens,
				ContextWindow:      ctxWindow,
			},
		}, true

	case "item/commandExecution/outputDelta":
		var delta CommandOutputDelta
		if err := json.Unmarshal(params, &delta); err != nil {
			return llm.SDKMessage{}, false
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
			return llm.SDKMessage{}, false
		}
		p.mu.Lock()
		p.turnHadToolUse = true
		p.mu.Unlock()
		return llm.SDKMessage{
			Type: "tool_progress",
			ToolProgress: &llm.ToolProgressMessage{
				Type:      "tool_progress",
				ToolUseID: delta.ItemID,
				ToolName:  "Write",
				Data:      delta.Delta,
			},
		}, true

	case "item/reasoning/summaryTextDelta":
		var delta ReasoningSummaryDelta
		if err := json.Unmarshal(params, &delta); err != nil {
			return llm.SDKMessage{}, false
		}
		return llm.SDKMessage{
			Type:    "assistant",
			Subtype: "partial",
			Assistant: &llm.AssistantMessage{
				Type:    "assistant",
				Subtype: "partial",
				Message: llm.ConversationMsg{
					Role: "assistant",
					Content: []llm.ContentBlock{
						{Type: "thinking", Thinking: delta.Delta},
					},
				},
			},
		}, true

	case "account/rateLimits/updated":
		var rl RateLimitsUpdated
		if err := json.Unmarshal(params, &rl); err != nil {
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
			return llm.SDKMessage{}, false
		}

		p.mu.Lock()
		isMainItem := p.isMainThread(completed.ThreadID)
		p.mu.Unlock()
		if !isMainItem {
			return llm.SDKMessage{}, false
		}

		switch completed.Item.Type {
		case "agentMessage":
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
				Type: "assistant",
				Assistant: &llm.AssistantMessage{
					Type: "assistant",
					Message: llm.ConversationMsg{
						Role: "assistant",
						Content: []llm.ContentBlock{
							{Type: "text", Text: completed.Item.Text},
						},
					},
				},
			}, true
		case "commandExecution":
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
		default:
			return llm.SDKMessage{}, false
		}

	case "item/started":
		var started ItemStartedParams
		if err := json.Unmarshal(params, &started); err != nil {
			return llm.SDKMessage{}, false
		}
		p.mu.Lock()
		isMainItem := p.isMainThread(started.ThreadID)
		if isMainItem && (started.Item.Type == "commandExecution" || started.Item.Type == "fileChange") {
			p.turnHadToolUse = true
		}
		p.mu.Unlock()
		if !isMainItem {
			return llm.SDKMessage{}, false
		}
		switch started.Item.Type {
		case "commandExecution":
			return llm.SDKMessage{
				Type: "tool_progress",
				ToolProgress: &llm.ToolProgressMessage{
					Type:      "tool_progress",
					ToolUseID: started.Item.ID,
					ToolName:  "Bash",
				},
			}, true
		case "fileChange":
			return llm.SDKMessage{
				Type: "tool_progress",
				ToolProgress: &llm.ToolProgressMessage{
					Type:      "tool_progress",
					ToolUseID: started.Item.ID,
					ToolName:  "Write",
				},
			}, true
		default:
			return llm.SDKMessage{}, false
		}

	case "turn/started":
		var turnStarted struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(params, &turnStarted)
		p.mu.Lock()
		isMain := turnStarted.ThreadID == "" || p.threadID == "" || turnStarted.ThreadID == p.threadID
		if isMain {
			p.turnHadToolUse = false
			p.lastAssistantText = ""
			p.lastAssistantDraft = ""
		}
		p.mu.Unlock()
		return llm.SDKMessage{}, false

	case "thread/status/changed":
		return llm.SDKMessage{}, false

	case "error":
		var errNotif ErrorNotification
		if err := json.Unmarshal(params, &errNotif); err != nil {
			return llm.SDKMessage{}, false
		}
		errText := errNotif.Error.Message
		if errNotif.Error.ErrorInfo != nil && errNotif.Error.ErrorInfo.RawKind != "" {
			errText = fmt.Sprintf("%s (%s)", errText, errNotif.Error.ErrorInfo.RawKind)
		}
		return llm.SDKMessage{
			Type:    "assistant",
			Subtype: "partial",
			Assistant: &llm.AssistantMessage{
				Type:    "assistant",
				Subtype: "partial",
				Message: llm.ConversationMsg{
					Role: "assistant",
					Content: []llm.ContentBlock{
						{Type: "text", Text: fmt.Sprintf("[codex error] %s", errText)},
					},
				},
			},
		}, true

	default:
		p.logDebug("[codex] unhandled notification: %s", method)
		return llm.SDKMessage{}, false
	}
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

func textLooksLikeQuestion(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	return strings.HasSuffix(text, "?") ||
		strings.HasSuffix(text, "?\n") ||
		strings.Contains(text, "?")
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

func computeCost(model string, inputTokens, outputTokens int) float64 {
	r, ok := lookupRate(model)
	if !ok {
		return 0
	}
	return (float64(inputTokens)/1_000_000)*r.inputPerMToken +
		(float64(outputTokens)/1_000_000)*r.outputPerMToken
}
