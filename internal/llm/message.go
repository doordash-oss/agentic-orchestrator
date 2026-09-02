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

package llm

import (
	"encoding/json"
	"fmt"
	"time"
)

// SDKMessage is the envelope for all JSON messages from LLM CLI tools.
// Both Claude (stream-json) and Codex (JSON-RPC) normalize to this type.
type SDKMessage struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	// Origin is assigned by every provider adapter. It prevents child-agent
	// output from being mistaken for root-agent completion or user input.
	Origin EventOrigin `json:"-"`
	// OccurredAt is the provider timestamp when available, otherwise the
	// session receive time. It drives truthful task activity snapshots.
	OccurredAt time.Time `json:"-"`

	// Exactly one of these is non-nil after unmarshaling, based on Type.
	Init           *SystemInitMessage      `json:"-"`
	Assistant      *AssistantMessage       `json:"-"`
	User           *UserMessage            `json:"-"`
	Result         *ResultMessage          `json:"-"`
	ControlRequest *ControlRequestMessage  `json:"-"`
	Status         *StatusMessage          `json:"-"`
	ToolProgress   *ToolProgressMessage    `json:"-"`
	HookStarted    *HookStartedMessage     `json:"-"`
	HookProgress   *HookProgressMessage    `json:"-"`
	HookResponse   *HookResponseMessage    `json:"-"`
	RateLimit      *RateLimitMessage       `json:"-"`
	Compact        *CompactBoundaryMessage `json:"-"`
	FileReads      []FileReadEvent         `json:"-"`
	FileChanges    []FileChangeEvent       `json:"-"`

	// Provider-normalized subagent (Task tool) lifecycle messages. Claude
	// emits the corresponding system subtypes natively; other adapters may
	// synthesize the same lifecycle from their provider protocol.
	TaskStarted      *TaskStartedMessage      `json:"-"`
	TaskProgress     *TaskProgressMessage     `json:"-"`
	TaskNotification *TaskNotificationMessage `json:"-"`

	// UsageUpdate is a synthetic message containing cumulative token usage.
	// Produced by providers (e.g., Codex tokenUsage/updated) for real-time
	// session-level accumulation. Not a wire type — never unmarshaled from JSON.
	UsageUpdate *Usage `json:"-"`

	// StreamDeltaType is set for stream_event messages (from --include-partial-messages).
	// Contains the delta type: "thinking", "text", "input_json", or "" for
	// non-delta events (message_start, content_block_stop, etc.).
	// These messages are ephemeral — they drive UI activity indicators but
	// are NOT stored in the message log.
	StreamDeltaType string `json:"-"`

	// LocallyAppended is true for messages synthetically added by the desktop app
	// (e.g. user-typed chat input in the attach view).
	LocallyAppended bool `json:"-"`
	// AutoPicked marks locally appended AskUserQuestion answers selected by
	// the harness auto-pick policy rather than typed by the human.
	AutoPicked bool `json:"-"`
	// AutoPickQuestion is the AskUserQuestion text for an AutoPicked answer.
	AutoPickQuestion string `json:"-"`
	// AutoPickConfidence is the confidence attached to an AutoPicked answer.
	AutoPickConfidence float64 `json:"-"`
}

// UnmarshalJSON routes based on "type" (and "subtype") field.
func (m *SDKMessage) UnmarshalJSON(data []byte) error {
	var envelope struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype,omitempty"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("unmarshal SDKMessage envelope: %w", err)
	}
	m.Type = envelope.Type
	m.Subtype = envelope.Subtype

	switch envelope.Type {
	case "stream_event":
		m.StreamDeltaType = extractStreamDeltaType(data)
		return nil
	case "system":
		switch envelope.Subtype {
		case "init":
			m.Init = &SystemInitMessage{}
			return json.Unmarshal(data, m.Init)
		case "compact_boundary":
			m.Compact = &CompactBoundaryMessage{}
			return json.Unmarshal(data, m.Compact)
		case "task_started":
			m.TaskStarted = &TaskStartedMessage{}
			return json.Unmarshal(data, m.TaskStarted)
		case "task_progress":
			m.TaskProgress = &TaskProgressMessage{}
			return json.Unmarshal(data, m.TaskProgress)
		case "task_notification":
			m.TaskNotification = &TaskNotificationMessage{}
			return json.Unmarshal(data, m.TaskNotification)
		}
	case "assistant":
		m.Assistant = &AssistantMessage{}
		return json.Unmarshal(data, m.Assistant)
	case "user":
		m.User = &UserMessage{}
		return json.Unmarshal(data, m.User)
	case "result":
		m.Result = &ResultMessage{}
		return json.Unmarshal(data, m.Result)
	case "control_request":
		m.ControlRequest = &ControlRequestMessage{}
		return json.Unmarshal(data, m.ControlRequest)
	case "status":
		m.Status = &StatusMessage{}
		return json.Unmarshal(data, m.Status)
	case "tool_progress":
		m.ToolProgress = &ToolProgressMessage{}
		return json.Unmarshal(data, m.ToolProgress)
	case "hook_started":
		m.HookStarted = &HookStartedMessage{}
		return json.Unmarshal(data, m.HookStarted)
	case "hook_progress":
		m.HookProgress = &HookProgressMessage{}
		return json.Unmarshal(data, m.HookProgress)
	case "hook_response":
		m.HookResponse = &HookResponseMessage{}
		return json.Unmarshal(data, m.HookResponse)
	case "rate_limit":
		m.RateLimit = &RateLimitMessage{}
		return json.Unmarshal(data, m.RateLimit)
	}
	return nil
}

// extractStreamDeltaType pulls the delta type from a stream_event JSON envelope.
// Returns a short activity name ("thinking", "text", "input_json") for delta
// events, or "" for non-delta events (message_start, content_block_stop, etc.).
func extractStreamDeltaType(data []byte) string {
	var ev struct {
		Event struct {
			Delta struct {
				Type string `json:"type"`
			} `json:"delta"`
		} `json:"event"`
	}
	if json.Unmarshal(data, &ev) != nil || ev.Event.Delta.Type == "" {
		return ""
	}
	// Strip the "_delta" suffix for a cleaner activity name.
	// "thinking_delta" → "thinking", "text_delta" → "text", etc.
	if t := ev.Event.Delta.Type; len(t) > 6 && t[len(t)-6:] == "_delta" {
		return t[:len(t)-6]
	}
	return ev.Event.Delta.Type
}

// --- Concrete message types ---

// SystemInitMessage is the first message emitted by the CLI.
type SystemInitMessage struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	SessionID      string          `json:"session_id"`
	Model          string          `json:"model"`
	Tools          []string        `json:"tools,omitempty"`
	MCPServers     []MCPServerInfo `json:"mcp_servers,omitempty"`
	PermissionMode string          `json:"permissionMode,omitempty"`
}

// MCPServerInfo describes an MCP server connected to the session.
type MCPServerInfo struct {
	Name   string `json:"name"`
	Status string `json:"status,omitempty"`
}

// AssistantMessage is the LLM's response with content blocks.
type AssistantMessage struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	Message   ConversationMsg `json:"message"`
	SessionID string          `json:"session_id,omitempty"`
}

// ConversationMsg is a message in the conversation (assistant or user role).
type ConversationMsg struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
	Model   string         `json:"model,omitempty"`
	Usage   *Usage         `json:"usage,omitempty"`
}

// ContentBlock is a polymorphic block within a message.
type ContentBlock struct {
	Type string `json:"type"`

	// TextBlock fields
	Text string `json:"text,omitempty"`

	// ToolUseBlock fields
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// ToolResultBlock fields
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`

	// ThinkingBlock fields
	Thinking string `json:"thinking,omitempty"`
}

// IsText returns true if the content block is a text block.
func (b ContentBlock) IsText() bool { return b.Type == "text" }

// IsToolUse returns true if the content block is a tool use block.
func (b ContentBlock) IsToolUse() bool { return b.Type == "tool_use" }

// IsToolResult returns true if the content block is a tool result block.
func (b ContentBlock) IsToolResult() bool { return b.Type == "tool_result" }

// IsThinking returns true if the content block is a thinking block.
func (b ContentBlock) IsThinking() bool { return b.Type == "thinking" }

// Usage tracks token consumption.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	ContextWindow            int `json:"context_window,omitempty"`
	// CostUSD is the provider's latest cumulative cost snapshot for a running
	// session. Providers without live cost telemetry leave it zero; their final
	// ResultMessage remains authoritative.
	CostUSD float64 `json:"cost_usd,omitempty"`

	// ContextInputTokens is the current context fill (post-compaction)
	// expressed as input tokens only. Kept for informational purposes;
	// ContextPercentage prefers ContextTotalTokens when present.
	ContextInputTokens int `json:"-"`

	// ContextTotalTokens is the current context fill as a total-token
	// snapshot (input + output + reasoning). Populated by providers that
	// expose such a snapshot — notably Codex's tokenUsage.last.totalTokens.
	// When zero, ContextPercentage falls back to the Claude-style
	// InputTokens + cache_* sum.
	ContextTotalTokens int `json:"-"`

	// ContextBaseline is a fixed system-prompt / tool-schema overhead that
	// is subtracted from both numerator and denominator of the context %
	// calculation so the displayed figure represents user-controllable
	// space. Codex uses 12000 (TokenUsage::BASELINE_TOKENS); Claude uses 0.
	ContextBaseline int `json:"-"`
}

// UserMessage is an echoed user message or tool result.
type UserMessage struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	Message   ConversationMsg `json:"message"`
	SessionID string          `json:"session_id,omitempty"`
}

// ModelUsageEntry holds per-model usage metadata from the result message.
type ModelUsageEntry struct {
	ContextWindow int `json:"contextWindow,omitempty"`
}

// ResultMessage is the final message with completion status and cost.
type ResultMessage struct {
	Type         string                     `json:"type"`
	Subtype      string                     `json:"subtype"` // "success", "error", "max_turns", "max_budget"
	SessionID    string                     `json:"session_id"`
	TotalCostUSD float64                    `json:"total_cost_usd"`
	Usage        *Usage                     `json:"usage,omitempty"`
	ModelUsage   map[string]ModelUsageEntry `json:"modelUsage,omitempty"`
	Result       string                     `json:"result,omitempty"`
	IsError      bool                       `json:"is_error,omitempty"`
	DurationMS   float64                    `json:"duration_ms,omitempty"`
	DurationAPI  float64                    `json:"duration_api_ms,omitempty"`
	NumTurns     int                        `json:"num_turns,omitempty"`

	// StopReason is the model's reason for ending the final assistant message
	// of this invocation. Common values: "end_turn" (agent finished its turn),
	// "tool_use" (agent had a pending tool call when the CLI ended the
	// invocation — often an internal truncation), "max_tokens", "pause_turn",
	// "stop_sequence", "refusal". Empty when the provider doesn't emit it.
	StopReason string `json:"stop_reason,omitempty"`
}

// IsSuccess returns true if the result indicates successful completion.
func (r *ResultMessage) IsSuccess() bool { return r.Subtype == "success" && !r.IsError }

// IsClientSideResult returns true if the result was resolved by the CLI
// without model interaction (e.g. "Unknown skill: X"). These have
// IsSuccess() true but zero output tokens and non-empty result text.
func (r *ResultMessage) IsClientSideResult() bool {
	if !r.IsSuccess() || r.Result == "" {
		return false
	}
	if r.Usage != nil && r.Usage.OutputTokens == 0 {
		return true
	}
	return false
}

// IsMaxTurns returns true if the session hit the max turns limit.
func (r *ResultMessage) IsMaxTurns() bool { return r.Subtype == "max_turns" }

// IsTurnTruncated reports whether the provider ended this turn before a
// deliberate root outcome could be produced.
func (r *ResultMessage) IsTurnTruncated() bool {
	if r == nil || r.IsError || r.Subtype == "error" {
		return false
	}
	switch r.StopReason {
	case "tool_use", "max_tokens", "pause_turn":
		return true
	}
	return false
}

// ControlRequestMessage is a permission or hook callback request.
type ControlRequestMessage struct {
	Type         string         `json:"type"`
	RequestID    string         `json:"request_id"`
	Request      ControlRequest `json:"request"`
	WaitingSince time.Time      `json:"-"`
	Origin       EventOrigin    `json:"-"`
	// AutoApproveOffer is set when automatic Bash review is off for the
	// session but would have handled this request had it been on.
	AutoApproveOffer *AutoApproveOffer `json:"-"`
}

// AutoApproveOffer describes how automatic Bash review would have treated a
// deferred request, so the permission UI can offer to turn the feature on.
type AutoApproveOffer struct {
	// WouldFastPath is true when the deterministic guardrail alone would
	// have approved the command without a reviewer model.
	WouldFastPath bool
}

// ControlRequest is the inner request payload.
type ControlRequest struct {
	Subtype    string          `json:"subtype"` // "can_use_tool", "hook_callback"
	ToolName   string          `json:"tool_name,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	HookName   string          `json:"hook_name,omitempty"`
	CallbackID string          `json:"callback_id,omitempty"`
}

// ControlResponse is the wire format for responding to control_request messages.
type ControlResponse struct {
	Type     string               `json:"type"`
	Response ControlResponseInner `json:"response"`
}

// ControlResponseInner is the inner payload of a control response.
type ControlResponseInner struct {
	Subtype   string         `json:"subtype"`
	RequestID string         `json:"request_id"`
	Response  map[string]any `json:"response"`
}

// NewAllowResponse creates a control_response that allows a tool use.
func NewAllowResponse(requestID string, originalInput ...json.RawMessage) ControlResponse {
	resp := map[string]any{"behavior": "allow"}
	if len(originalInput) > 0 && len(originalInput[0]) > 0 {
		var inputVal any
		if err := json.Unmarshal(originalInput[0], &inputVal); err == nil {
			resp["updatedInput"] = inputVal
		}
	}
	return ControlResponse{
		Type: "control_response",
		Response: ControlResponseInner{
			Subtype:   "success",
			RequestID: requestID,
			Response:  resp,
		},
	}
}

// NewDenyResponse creates a control_response that denies a tool use.
func NewDenyResponse(requestID string, reason string) ControlResponse {
	return ControlResponse{
		Type: "control_response",
		Response: ControlResponseInner{
			Subtype:   "success",
			RequestID: requestID,
			Response:  map[string]any{"behavior": "deny", "message": reason},
		},
	}
}

// AskUserAnnotation carries optional per-question annotations returned alongside
// an AskUserQuestion answer. Mirrors the Claude Agent SDK shape
// (updatedInput.annotations[questionText] = {notes?, preview?}).
type AskUserAnnotation struct {
	Notes   string `json:"notes,omitempty"`
	Preview string `json:"preview,omitempty"`
}

// NewAskUserResponse creates a control_response that allows an AskUserQuestion
// tool use and supplies the user's answers. Pass annotations to attach
// per-question notes/preview; the key is omitted from the payload when no
// annotation entry carries a non-empty field.
func NewAskUserResponse(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]AskUserAnnotation) ControlResponse {
	var questionsVal any
	if err := json.Unmarshal(questions, &questionsVal); err != nil {
		questionsVal = questions
	}
	// Production callers pass the entire tool input ({"questions":[...]}),
	// while the SDK expects updatedInput.questions to be the inner array.
	// Unwrap the envelope so the CLI's response handler can iterate it.
	if envelope, ok := questionsVal.(map[string]any); ok {
		if inner, ok := envelope["questions"].([]any); ok {
			questionsVal = inner
		}
	}
	updated := map[string]any{
		"questions": questionsVal,
		"answers":   answers,
	}
	if filtered := filterAnnotations(annotations); len(filtered) > 0 {
		updated["annotations"] = filtered
	}
	return ControlResponse{
		Type: "control_response",
		Response: ControlResponseInner{
			Subtype:   "success",
			RequestID: requestID,
			Response: map[string]any{
				"behavior":     "allow",
				"updatedInput": updated,
			},
		},
	}
}

// filterAnnotations drops entries whose fields are all empty so the emitted
// JSON omits noise when the user skipped notes on every question.
func filterAnnotations(in map[string]AskUserAnnotation) map[string]AskUserAnnotation {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]AskUserAnnotation, len(in))
	for k, v := range in {
		if v.Notes == "" && v.Preview == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// InitializeRequestMessage is the handshake sent to the CLI to activate the
// control_request protocol.
type InitializeRequestMessage struct {
	Type      string                `json:"type"`
	RequestID string                `json:"request_id"`
	Request   InitializeRequestBody `json:"request"`
}

// InitializeRequestBody is the inner payload of the initialize handshake.
type InitializeRequestBody struct {
	Subtype string                         `json:"subtype"`
	Hooks   map[string][]InitializeHookRef `json:"hooks"`
}

// InitializeHookRef references hook callbacks by ID.
type InitializeHookRef struct {
	HookCallbackIDs []string `json:"hookCallbackIds"`
}

// NewInitializeRequest creates the SDK initialize handshake message.
func NewInitializeRequest() InitializeRequestMessage {
	return InitializeRequestMessage{
		Type:      "control_request",
		RequestID: "agentic-init-1",
		Request: InitializeRequestBody{
			Subtype: "initialize",
			Hooks: map[string][]InitializeHookRef{
				"PreToolUse": {
					{HookCallbackIDs: []string{"agentic-hook-1"}},
				},
			},
		},
	}
}

// NewHookContinueResponse creates a control_response that tells the CLI
// to continue with the tool execution (used for PreToolUse hook callbacks).
func NewHookContinueResponse(requestID string) ControlResponse {
	return ControlResponse{
		Type: "control_response",
		Response: ControlResponseInner{
			Subtype:   "success",
			RequestID: requestID,
			Response:  map[string]any{"continue": true},
		},
	}
}

// UserInputMessage is sent to stdin to deliver a user message.
type UserInputMessage struct {
	Type    string           `json:"type"`
	Message UserInputContent `json:"message"`
}

// UserInputContent is the inner content of a user input message.
type UserInputContent struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// NewUserInput creates a UserInputMessage for sending text to the session.
func NewUserInput(text string) UserInputMessage {
	return UserInputMessage{
		Type: "user",
		Message: UserInputContent{
			Role:    "user",
			Content: text,
		},
	}
}

// StatusMessage is a status update during execution.
type StatusMessage struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message,omitempty"`
}

// ToolProgressMessage is emitted during tool execution.
type ToolProgressMessage struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	Data      string `json:"data,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// TimeoutMS is the tool invocation's declared execution timeout in
	// milliseconds, when the provider reports one; 0 means unknown.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
}

// FileReadEvent is a provider-neutral signal that the provider reported a
// concrete file read. Providers may attach this to another SDKMessage when the
// read is metadata on a broader tool event.
type FileReadEvent struct {
	FilePath       string `json:"file_path"`
	Source         string `json:"source,omitempty"`
	ProviderItemID string `json:"provider_item_id,omitempty"`
	ExitCode       *int   `json:"exit_code,omitempty"`
}

// FileChangeEvent is a provider-neutral signal for a concrete file mutation.
// API transcript reconstruction uses this to preserve attach-view file cards
// without exposing raw provider tool inputs.
type FileChangeEvent struct {
	Path         string
	OldPath      string
	Operation    string
	Detail       string
	AddedLines   int
	RemovedLines int
	HasDiffPatch bool
}

// HookStartedMessage is emitted when a hook begins execution.
type HookStartedMessage struct {
	Type     string `json:"type"`
	HookName string `json:"hook_name,omitempty"`
}

// HookProgressMessage is emitted during hook execution.
type HookProgressMessage struct {
	Type     string `json:"type"`
	HookName string `json:"hook_name,omitempty"`
	Data     string `json:"data,omitempty"`
}

// HookResponseMessage is emitted when a hook completes.
type HookResponseMessage struct {
	Type     string `json:"type"`
	HookName string `json:"hook_name,omitempty"`
	Result   string `json:"result,omitempty"`
}

// CompactBoundaryMessage indicates context compaction occurred.
type CompactBoundaryMessage struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
}

// TaskUsage is the per-subagent cumulative usage payload carried inside
// TaskProgressMessage and TaskNotificationMessage. DurationMs is cumulative
// since the subagent started; a large and still-growing value indicates a
// runaway subagent that may warrant cancellation.
type TaskUsage struct {
	TotalTokens int   `json:"total_tokens,omitempty"`
	ToolUses    int   `json:"tool_uses,omitempty"`
	DurationMs  int64 `json:"duration_ms,omitempty"`
}

// TaskStartedMessage is the initial lifecycle event emitted when a subagent
// (Task tool invocation) begins. Claude emits this as type=system,
// subtype=task_started. The Prompt field can be very large (the full task
// prompt) — callers surfacing this to logs should NOT serialize Prompt.
type TaskStartedMessage struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	TaskID      string `json:"task_id,omitempty"`
	ToolUseID   string `json:"tool_use_id,omitempty"`
	Description string `json:"description,omitempty"`
	TaskType    string `json:"task_type,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	UUID        string `json:"uuid,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

// TaskProgressMessage is a periodic progress update for a running subagent
// (Task tool invocation). Claude emits this as type=system, subtype=task_progress.
type TaskProgressMessage struct {
	Type         string     `json:"type"`
	Subtype      string     `json:"subtype"`
	TaskID       string     `json:"task_id,omitempty"`
	ToolUseID    string     `json:"tool_use_id,omitempty"`
	Description  string     `json:"description,omitempty"`
	LastToolName string     `json:"last_tool_name,omitempty"`
	LastPath     string     `json:"last_path,omitempty"`
	Usage        *TaskUsage `json:"usage,omitempty"`
	UUID         string     `json:"uuid,omitempty"`
	SessionID    string     `json:"session_id,omitempty"`
}

// TaskNotificationMessage is the terminal status for a subagent (Task tool)
// invocation. Claude emits this as type=system, subtype=task_notification.
// Status is typically "completed" on normal return or non-empty on error.
type TaskNotificationMessage struct {
	Type       string     `json:"type"`
	Subtype    string     `json:"subtype"`
	TaskID     string     `json:"task_id,omitempty"`
	ToolUseID  string     `json:"tool_use_id,omitempty"`
	Status     string     `json:"status,omitempty"`
	Summary    string     `json:"summary,omitempty"`
	OutputFile string     `json:"output_file,omitempty"`
	Usage      *TaskUsage `json:"usage,omitempty"`
	UUID       string     `json:"uuid,omitempty"`
	SessionID  string     `json:"session_id,omitempty"`
}

// RateLimitMessage indicates rate limiting.
type RateLimitMessage struct {
	Type    string  `json:"type"`
	RetryMS float64 `json:"retry_after_ms,omitempty"`
	Message string  `json:"message,omitempty"`
}
