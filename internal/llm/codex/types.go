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

// outboundNotification is an outbound JSON-RPC 2.0 notification (no id).
type outboundNotification struct {
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

// threadStartParams holds parameters for thread/start.
type threadStartParams struct {
	Model                   string                    `json:"model"`
	Cwd                     string                    `json:"cwd"`
	ApprovalPolicy          string                    `json:"approvalPolicy,omitempty"`
	Sandbox                 *sandboxMode              `json:"sandbox,omitempty"`
	Ephemeral               bool                      `json:"ephemeral,omitempty"`
	Config                  map[string]interface{}    `json:"config,omitempty"`
	BaseInstructions        *string                   `json:"baseInstructions,omitempty"`
	DeveloperInstructions   *string                   `json:"developerInstructions,omitempty"`
	Environments            *[]map[string]interface{} `json:"environments,omitempty"`
	DynamicTools            *[]map[string]interface{} `json:"dynamicTools,omitempty"`
	SelectedCapabilityRoots *[]map[string]interface{} `json:"selectedCapabilityRoots,omitempty"`
}

// sandboxMode selects the coarse sandbox mode used during thread/start.
type sandboxMode string

const (
	SandboxModeReadOnly         sandboxMode = "read-only"
	SandboxModeWorkspaceWrite   sandboxMode = "workspace-write"
	SandboxModeDangerFullAccess sandboxMode = "danger-full-access"
)

// SandboxPolicy configures the sandbox policy for turn/start.
type SandboxPolicy struct {
	Type          string   `json:"type"`
	WritableRoots []string `json:"writableRoots,omitempty"`
	NetworkAccess bool     `json:"networkAccess,omitempty"`
}

// threadResumeParams holds parameters for thread/resume.
type threadResumeParams struct {
	ThreadID              string                 `json:"threadId"`
	DeveloperInstructions *string                `json:"developerInstructions,omitempty"`
	Config                map[string]interface{} `json:"config,omitempty"`
}

// threadSettingsUpdateParams changes the model and effort on an existing thread.
type threadSettingsUpdateParams struct {
	ThreadID string  `json:"threadId"`
	Model    string  `json:"model"`
	Effort   *string `json:"effort"`
}

// threadStartResult is the response to thread/start and thread/resume.
type threadStartResult struct {
	Thread         Thread `json:"thread"`
	ApprovalPolicy string `json:"approvalPolicy,omitempty"`
	Model          string `json:"model,omitempty"`
	ServiceTier    string `json:"serviceTier,omitempty"`
}

// Thread identifies a conversation thread.
type Thread struct {
	ID string `json:"id"`
}

// --- Turn ---

// turnStartParams holds parameters for turn/start.
type turnStartParams struct {
	ThreadID       string         `json:"threadId"`
	Input          []inputItem    `json:"input"`
	Model          string         `json:"model,omitempty"`
	Effort         string         `json:"effort,omitempty"`
	ApprovalPolicy string         `json:"approvalPolicy,omitempty"`
	SandboxPolicy  *SandboxPolicy `json:"sandboxPolicy,omitempty"`
}

// inputItem is a single input in a turn/start request.
type inputItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// turnStartResult is the response to turn/start.
type turnStartResult struct {
	Turn Turn `json:"turn"`
}

// Turn identifies a conversation turn.
type Turn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// turnInterruptParams holds parameters for turn/interrupt.
type turnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// --- Notifications (inbound) ---

// agentMessageDelta is an incremental text chunk from the agent.
type agentMessageDelta struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
	Phase    string `json:"phase,omitempty"`
}

// commandApprovalParams is a server request for command execution approval.
type commandApprovalParams struct {
	ItemID   string `json:"itemId"`
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Command  string `json:"command,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// fileChangeApprovalParams is a server request for file change approval.
type fileChangeApprovalParams struct {
	ItemID    string `json:"itemId"`
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	Reason    string `json:"reason,omitempty"`
	GrantRoot string `json:"grantRoot,omitempty"`
}

// turnCompletedParams is a notification that a turn has completed.
type turnCompletedParams struct {
	ThreadID string        `json:"threadId"`
	Turn     completedTurn `json:"turn"`
}

// completedTurn contains the final state of a completed turn.
type completedTurn struct {
	ID     string     `json:"id"`
	Status string     `json:"status"` // "completed", "failed", "interrupted"
	Error  *turnError `json:"error,omitempty"`
}

// tokenUsageUpdatedParams is a notification with token usage.
// The wire format uses "tokenUsage" containing "total" (lifetime cumulative,
// for cost/billing) and "last" (current context fill after compaction, for
// computing context window usage %).
type tokenUsageUpdatedParams struct {
	ThreadID   string           `json:"threadId"`
	TurnID     string           `json:"turnId"`
	TokenUsage threadTokenUsage `json:"tokenUsage"`
}

// threadTokenUsage wraps cumulative and per-turn token breakdowns.
type threadTokenUsage struct {
	Total              tokenUsageBreakdown `json:"total"`
	Last               tokenUsageBreakdown `json:"last"`
	ModelContextWindow *int                `json:"modelContextWindow,omitempty"`
}

// tokenUsageBreakdown contains token counts for a usage snapshot.
type tokenUsageBreakdown struct {
	InputTokens           int `json:"inputTokens"`
	CachedInputTokens     int `json:"cachedInputTokens"`
	CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
	OutputTokens          int `json:"outputTokens"`
	ReasoningOutputTokens int `json:"reasoningOutputTokens"`
	TotalTokens           int `json:"totalTokens"`
}

// turnError contains error details for a failed turn.
type turnError struct {
	Message   string         `json:"message"`
	ErrorInfo *errorInfoType `json:"codexErrorInfo,omitempty"`
}

// errorInfoType contains the structured error classification.
type errorInfoType struct {
	Type           string `json:"type"`
	HttpStatusCode *int   `json:"httpStatusCode,omitempty"`
	RawKind        string `json:"-"`
}

// UnmarshalJSON handles the polymorphic codexErrorInfo field.
func (e *errorInfoType) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		e.RawKind = s
		e.Type = s
		return nil
	}
	type plain errorInfoType
	return json.Unmarshal(data, (*plain)(e))
}

// errorNotification is an inbound error notification with retry info.
type errorNotification struct {
	Error             turnError `json:"error"`
	WillRetry         bool      `json:"willRetry"`
	ThreadID          string    `json:"threadId,omitempty"`
	TurnID            string    `json:"turnId,omitempty"`
	AdditionalDetails string    `json:"additionalDetails,omitempty"`
}

// commandOutputDelta is an incremental output chunk from a command execution.
type commandOutputDelta struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

// fileChangeOutputDelta is an incremental output chunk from a file change.
type fileChangeOutputDelta struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

// reasoningSummaryDelta is an incremental reasoning summary text chunk.
type reasoningSummaryDelta struct {
	ThreadID     string `json:"threadId"`
	TurnID       string `json:"turnId"`
	ItemID       string `json:"itemId"`
	Delta        string `json:"delta"`
	SummaryIndex int    `json:"summaryIndex"`
}

// rateLimitsUpdated carries rate limit information.
type rateLimitsUpdated struct {
	RateLimits rateLimitBucket `json:"rateLimits"`
}

// rateLimitBucket contains rate limit bucket info.
type rateLimitBucket struct {
	LimitID   string           `json:"limitId"`
	LimitName *string          `json:"limitName"`
	Primary   *rateLimitWindow `json:"primary"`
}

// rateLimitWindow contains timing info for a rate limit window.
type rateLimitWindow struct {
	UsedPercent       float64 `json:"usedPercent"`
	WindowDurationMin int     `json:"windowDurationMins"`
	ResetsAt          int64   `json:"resetsAt"`
}

// itemCompletedParams wraps a completed ThreadItem.
type itemCompletedParams struct {
	ThreadID string    `json:"threadId"`
	TurnID   string    `json:"turnId"`
	Item     itemUnion `json:"item"`
}

// itemStartedParams wraps a started ThreadItem.
type itemStartedParams struct {
	ThreadID string    `json:"threadId"`
	TurnID   string    `json:"turnId"`
	Item     itemUnion `json:"item"`
}

// itemUnion is a polymorphic item; Type discriminates the variant.
type itemUnion struct {
	Cwd               string          `json:"cwd,omitempty"`
	ID                string          `json:"id"`
	Type              string          `json:"type"`
	Phase             string          `json:"phase,omitempty"`
	Text              string          `json:"text,omitempty"`
	Tool              string          `json:"tool,omitempty"`
	Status            string          `json:"status,omitempty"`
	Prompt            string          `json:"prompt,omitempty"`
	Command           string          `json:"command,omitempty"`
	ReceiverThreadIDs []string        `json:"receiverThreadIds,omitempty"`
	AggregatedOutput  string          `json:"aggregatedOutput,omitempty"`
	ExitCode          *int            `json:"exitCode,omitempty"`
	Summary           []string        `json:"summary,omitempty"`
	CommandActions    []commandAction `json:"commandActions,omitempty"`
	Changes           []FileChange    `json:"changes,omitempty"`
}

// commandAction is Codex's structured description of a command's filesystem
// action. Read actions are provider-reported evidence that file contents were
// loaded.
type commandAction struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command,omitempty"`
	Name    string `json:"name,omitempty"`
	Path    string `json:"path,omitempty"`
}

// FileChange is Codex's structured description of a file mutation.
type FileChange struct {
	Path string         `json:"path,omitempty"`
	Kind fileChangeKind `json:"kind,omitempty"`
	Diff string         `json:"diff,omitempty"`
}

// fileChangeKind describes the kind of a Codex file mutation.
type fileChangeKind struct {
	Type     string `json:"type,omitempty"`
	MovePath string `json:"move_path,omitempty"`
}

// approvalDecision is the result sent in response to an approval request.
type approvalDecision struct {
	Decision string `json:"decision"` // "accept", "decline", "cancel"
}

// --- Model discovery (app-server) ---

// modelListParams holds the parameters for a model/list JSON-RPC request.
type modelListParams struct {
	Limit         int    `json:"limit"`
	Cursor        string `json:"cursor,omitempty"`
	IncludeHidden bool   `json:"includeHidden"`
}

// modelListResult is the response to a model/list request.
type modelListResult struct {
	Data       []modelListEntry `json:"data"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// modelListEntry is a single model returned by model/list.
type modelListEntry struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Hidden      bool   `json:"hidden"`
	IsDefault   bool   `json:"isDefault"`
}
