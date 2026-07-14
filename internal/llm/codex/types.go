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

import "encoding/json"

// --- JSON-RPC envelope ---

// Request is an outbound JSON-RPC 2.0 request (has id, expects response).
type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	ID      int         `json:"id"`
	Params  interface{} `json:"params,omitempty"`
}

// Response is an outbound JSON-RPC 2.0 response to a server-initiated request.
type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Result  interface{} `json:"result"`
}

// OutboundNotification is an outbound JSON-RPC 2.0 notification (no id).
type OutboundNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// --- Initialize handshake ---

// InitializeParams holds the parameters for the initialize request.
type InitializeParams struct {
	ClientInfo   ClientInfo   `json:"clientInfo"`
	Capabilities Capabilities `json:"capabilities,omitempty"`
}

// ClientInfo identifies the client to the Codex server.
type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

// Capabilities declares client capabilities.
type Capabilities struct {
	ExperimentalAPI bool `json:"experimentalApi,omitempty"`
}

// InitializeResult is the response to the initialize request.
type InitializeResult struct {
	UserAgent      string `json:"userAgent"`
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOs     string `json:"platformOs"`
}

// --- Thread ---

// ThreadStartParams holds parameters for thread/start.
type ThreadStartParams struct {
	Model          string       `json:"model"`
	Cwd            string       `json:"cwd"`
	ApprovalPolicy string       `json:"approvalPolicy,omitempty"`
	Sandbox        *SandboxMode `json:"sandbox,omitempty"`
}

// SandboxMode selects the coarse sandbox mode used during thread/start.
type SandboxMode string

const (
	SandboxModeReadOnly         SandboxMode = "read-only"
	SandboxModeWorkspaceWrite   SandboxMode = "workspace-write"
	SandboxModeDangerFullAccess SandboxMode = "danger-full-access"
)

// SandboxPolicy configures the sandbox policy for turn/start.
type SandboxPolicy struct {
	Type          string   `json:"type"`
	WritableRoots []string `json:"writableRoots,omitempty"`
	NetworkAccess bool     `json:"networkAccess,omitempty"`
}

// ThreadStartResult is the response to thread/start.
type ThreadStartResult struct {
	Thread Thread `json:"thread"`
}

// Thread identifies a conversation thread.
type Thread struct {
	ID string `json:"id"`
}

// --- Turn ---

// TurnStartParams holds parameters for turn/start.
type TurnStartParams struct {
	ThreadID          string             `json:"threadId"`
	Input             []InputItem        `json:"input"`
	Model             string             `json:"model,omitempty"`
	ApprovalPolicy    string             `json:"approvalPolicy,omitempty"`
	SandboxPolicy     *SandboxPolicy     `json:"sandboxPolicy,omitempty"`
	CollaborationMode *CollaborationMode `json:"collaborationMode,omitempty"`
}

// CollaborationMode specifies the collaboration mode for a turn.
type CollaborationMode struct {
	Mode     string                `json:"mode"`
	Settings CollaborationSettings `json:"settings"`
}

// CollaborationSettings holds settings within a collaboration mode.
type CollaborationSettings struct {
	Model                 string  `json:"model"`
	DeveloperInstructions *string `json:"developer_instructions,omitempty"`
}

// InputItem is a single input in a turn/start request.
type InputItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// TurnStartResult is the response to turn/start.
type TurnStartResult struct {
	Turn Turn `json:"turn"`
}

// Turn identifies a conversation turn.
type Turn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// --- Notifications (inbound) ---

// AgentMessageDelta is an incremental text chunk from the agent.
type AgentMessageDelta struct {
	ThreadID string `json:"threadId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
	Phase    string `json:"phase,omitempty"`
}

// CommandApprovalParams is a server request for command execution approval.
type CommandApprovalParams struct {
	ItemID   string `json:"itemId"`
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Command  string `json:"command,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// FileChangeApprovalParams is a server request for file change approval.
type FileChangeApprovalParams struct {
	ItemID    string `json:"itemId"`
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	Reason    string `json:"reason,omitempty"`
	GrantRoot string `json:"grantRoot,omitempty"`
}

// TurnCompletedParams is a notification that a turn has completed.
type TurnCompletedParams struct {
	ThreadID string        `json:"threadId"`
	Turn     CompletedTurn `json:"turn"`
}

// CompletedTurn contains the final state of a completed turn.
type CompletedTurn struct {
	ID     string     `json:"id"`
	Status string     `json:"status"` // "completed", "failed", "interrupted"
	Error  *TurnError `json:"error,omitempty"`
}

// TokenUsageUpdatedParams is a notification with token usage.
// The wire format uses "tokenUsage" containing "total" (lifetime cumulative,
// for cost/billing) and "last" (current context fill after compaction, for
// computing context window usage %).
type TokenUsageUpdatedParams struct {
	ThreadID   string           `json:"threadId"`
	TurnID     string           `json:"turnId"`
	TokenUsage ThreadTokenUsage `json:"tokenUsage"`
}

// ThreadTokenUsage wraps cumulative and per-turn token breakdowns.
type ThreadTokenUsage struct {
	Total              TokenUsageBreakdown `json:"total"`
	Last               TokenUsageBreakdown `json:"last"`
	ModelContextWindow *int                `json:"modelContextWindow,omitempty"`
}

// TokenUsageBreakdown contains token counts for a usage snapshot.
type TokenUsageBreakdown struct {
	InputTokens           int `json:"inputTokens"`
	CachedInputTokens     int `json:"cachedInputTokens"`
	OutputTokens          int `json:"outputTokens"`
	ReasoningOutputTokens int `json:"reasoningOutputTokens"`
	TotalTokens           int `json:"totalTokens"`
}

// TurnError contains error details for a failed turn.
type TurnError struct {
	Message   string         `json:"message"`
	ErrorInfo *ErrorInfoType `json:"codexErrorInfo,omitempty"`
}

// ErrorInfoType contains the structured error classification.
type ErrorInfoType struct {
	Type           string `json:"type"`
	HttpStatusCode *int   `json:"httpStatusCode,omitempty"`
	RawKind        string `json:"-"`
}

// UnmarshalJSON handles the polymorphic codexErrorInfo field.
func (e *ErrorInfoType) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		e.RawKind = s
		e.Type = s
		return nil
	}
	type plain ErrorInfoType
	return json.Unmarshal(data, (*plain)(e))
}

// ErrorNotification is an inbound error notification with retry info.
type ErrorNotification struct {
	Error             TurnError `json:"error"`
	WillRetry         bool      `json:"willRetry"`
	ThreadID          string    `json:"threadId,omitempty"`
	TurnID            string    `json:"turnId,omitempty"`
	AdditionalDetails string    `json:"additionalDetails,omitempty"`
}

// CommandOutputDelta is an incremental output chunk from a command execution.
type CommandOutputDelta struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

// FileChangeOutputDelta is an incremental output chunk from a file change.
type FileChangeOutputDelta struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

// ReasoningSummaryDelta is an incremental reasoning summary text chunk.
type ReasoningSummaryDelta struct {
	ThreadID     string `json:"threadId"`
	TurnID       string `json:"turnId"`
	ItemID       string `json:"itemId"`
	Delta        string `json:"delta"`
	SummaryIndex int    `json:"summaryIndex"`
}

// RateLimitsUpdated carries rate limit information.
type RateLimitsUpdated struct {
	RateLimits RateLimitBucket `json:"rateLimits"`
}

// RateLimitBucket contains rate limit bucket info.
type RateLimitBucket struct {
	LimitID   string           `json:"limitId"`
	LimitName *string          `json:"limitName"`
	Primary   *RateLimitWindow `json:"primary"`
}

// RateLimitWindow contains timing info for a rate limit window.
type RateLimitWindow struct {
	UsedPercent       float64 `json:"usedPercent"`
	WindowDurationMin int     `json:"windowDurationMins"`
	ResetsAt          int64   `json:"resetsAt"`
}

// ItemCompletedParams wraps a completed ThreadItem.
type ItemCompletedParams struct {
	ThreadID string    `json:"threadId"`
	TurnID   string    `json:"turnId"`
	Item     ItemUnion `json:"item"`
}

// ItemStartedParams wraps a started ThreadItem.
type ItemStartedParams struct {
	ThreadID string    `json:"threadId"`
	TurnID   string    `json:"turnId"`
	Item     ItemUnion `json:"item"`
}

// ItemUnion is a polymorphic item; Type discriminates the variant.
type ItemUnion struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Phase            string          `json:"phase,omitempty"`
	Text             string          `json:"text,omitempty"`
	AggregatedOutput string          `json:"aggregatedOutput,omitempty"`
	ExitCode         *int            `json:"exitCode,omitempty"`
	Summary          []string        `json:"summary,omitempty"`
	CommandActions   []CommandAction `json:"commandActions,omitempty"`
	Changes          []FileChange    `json:"changes,omitempty"`
}

// CommandAction is Codex's structured description of a command's filesystem
// action. Read actions are provider-reported evidence that file contents were
// loaded.
type CommandAction struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command,omitempty"`
	Name    string `json:"name,omitempty"`
	Path    string `json:"path,omitempty"`
}

// FileChange is Codex's structured description of a file mutation.
type FileChange struct {
	Path string         `json:"path,omitempty"`
	Kind FileChangeKind `json:"kind,omitempty"`
	Diff string         `json:"diff,omitempty"`
}

// FileChangeKind describes the kind of a Codex file mutation.
type FileChangeKind struct {
	Type     string `json:"type,omitempty"`
	MovePath string `json:"move_path,omitempty"`
}

// --- tool/requestUserInput (inbound) ---

// UserInputRequestParams holds the params for tool/requestUserInput server requests.
type UserInputRequestParams struct {
	Questions []InputQuestion `json:"questions"`
}

// InputQuestion is a single question in a tool/requestUserInput request.
type InputQuestion struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	Options []string `json:"options,omitempty"`
}

// --- Response types (outbound) ---

// AskUserAnswer is a single answer in the response to tool/requestUserInput.
type AskUserAnswer struct {
	QuestionID string `json:"questionId"`
	Value      string `json:"value"`
}

// AskUserResult is the result sent in response to a tool/requestUserInput request.
type AskUserResult struct {
	Answers []AskUserAnswer `json:"answers"`
}

// ApprovalDecision is the result sent in response to an approval request.
type ApprovalDecision struct {
	Decision string `json:"decision"` // "accept", "decline", "cancel"
}

// --- Model discovery (app-server) ---

// ModelListParams holds the parameters for a model/list JSON-RPC request.
type ModelListParams struct {
	Limit         int    `json:"limit"`
	Cursor        string `json:"cursor,omitempty"`
	IncludeHidden bool   `json:"includeHidden"`
}

// ModelListResult is the response to a model/list request.
type ModelListResult struct {
	Data       []ModelListEntry `json:"data"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// ModelListEntry is a single model returned by model/list.
type ModelListEntry struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Hidden      bool   `json:"hidden"`
	IsDefault   bool   `json:"isDefault"`
}
