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

import "encoding/json"

// --- JSON-RPC 2.0 envelope ---

// Request is an outbound JSON-RPC 2.0 request (has id, expects a response).
type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// ErrorResponse is an outbound JSON-RPC 2.0 error response to a server-initiated
// (agent->client) request. Phase 1 fails closed by replying with this whenever
// OpenCode asks the client for a capability the tracer does not implement.
type ErrorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   RPCError        `json:"error"`
}

// RPCError is the JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// inboundEnvelope decodes any inbound JSON-RPC line. A response carries id +
// (result|error) and no method; a server request carries id + method; a
// notification carries method only. The id is kept raw so it can be echoed
// verbatim in a fail-closed error response without assuming a number/string.
type inboundEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// --- initialize ---

// InitializeParams are the parameters for the ACP initialize request.
type InitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *ClientInfo        `json:"clientInfo,omitempty"`
}

// ClientInfo identifies the Agentico client to OpenCode.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientCapabilities declares which client-side capabilities Agentico supports.
// Phase 1 declares none: OpenCode must use its own filesystem/terminal tools,
// and any agent->client filesystem or terminal request fails closed.
type ClientCapabilities struct {
	FS FSCapability `json:"fs"`
	// Terminal is intentionally false for Phase 1 — Agentico does not host an
	// ACP terminal for OpenCode.
	Terminal bool `json:"terminal"`
}

// FSCapability declares client filesystem capabilities. Both false for Phase 1.
type FSCapability struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

// InitializeResult is OpenCode's response to initialize.
type InitializeResult struct {
	ProtocolVersion   int             `json:"protocolVersion"`
	AgentInfo         *AgentInfo      `json:"agentInfo,omitempty"`
	AuthMethods       []AuthMethod    `json:"authMethods,omitempty"`
	AgentCapabilities json.RawMessage `json:"agentCapabilities,omitempty"`
}

// AgentInfo identifies the OpenCode agent.
type AgentInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// AuthMethod describes an authentication method OpenCode offers.
type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// --- session/new ---

// SessionNewParams are the parameters for session/new.
type SessionNewParams struct {
	Cwd        string        `json:"cwd"`
	MCPServers []interface{} `json:"mcpServers"`
}

// SessionNewResult is OpenCode's response to session/new.
type SessionNewResult struct {
	SessionID string `json:"sessionId"`
}

// --- session/prompt ---

// PromptParams are the parameters for session/prompt.
type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// ContentBlock is an ACP content block. Phase 1 only sends text blocks.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// PromptResult is OpenCode's response to session/prompt.
type PromptResult struct {
	StopReason string `json:"stopReason"`
}

// ACP stop reasons returned by session/prompt.
const (
	StopReasonEndTurn         = "end_turn"
	StopReasonMaxTokens       = "max_tokens"
	StopReasonMaxTurnRequests = "max_turn_requests"
	StopReasonRefusal         = "refusal"
	StopReasonCancelled       = "cancelled"
)

// --- session/update (agent->client notification) ---

// SessionUpdateParams is the params object of a session/update notification.
type SessionUpdateParams struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

// SessionUpdate is the polymorphic body of a session/update; SessionUpdate
// (the discriminator) selects which fields are populated.
type SessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`

	// agent_message_chunk / agent_thought_chunk / user_message_chunk
	Content *UpdateContent `json:"content,omitempty"`

	// tool_call / tool_call_update
	ToolCallID string `json:"toolCallId,omitempty"`
	Title      string `json:"title,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Status     string `json:"status,omitempty"`
}

// session/update discriminator values handled by the tracer.
const (
	UpdateAgentMessageChunk = "agent_message_chunk"
	UpdateAgentThoughtChunk = "agent_thought_chunk"
	UpdateToolCall          = "tool_call"
	UpdateToolCallUpdate    = "tool_call_update"
)

// UpdateContent is the content block carried by message-chunk updates.
type UpdateContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// --- session/request_permission (agent->client request) ---

// requestPermissionMethod is the ACP method OpenCode uses to ask the client to
// approve a tool action or answer a user-facing question. It is the single
// control surface Phase 2 supports; client filesystem and terminal requests
// (fs/*, terminal/*) and unknown methods still fail closed.
const requestPermissionMethod = "session/request_permission"

// RequestPermissionParams is the params object of a session/request_permission
// request. OpenCode describes the gated action in ToolCall and offers the
// selectable Options the user picks among.
type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  PermissionToolCall `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// PermissionToolCall describes the action OpenCode wants permission for, or the
// question it wants answered. Kind classifies the surface (execute, edit, fetch,
// search, read, question); RawInput carries the tool's native input so the
// permission prompt and cache can show and match concrete detail; Title is a
// human summary used as the question stem and as a detail fallback.
type PermissionToolCall struct {
	ToolCallID string          `json:"toolCallId,omitempty"`
	Title      string          `json:"title,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
}

// PermissionOption is one selectable response to a session/request_permission
// request. For a tool permission, Kind is one of the allow_*/reject_* values and
// Name is the human label ("Allow", "Reject"). For a question (Kind=="question"
// on the tool call), each option is an answer choice: Name is the answer label,
// and the optional Description/Recommended/Confidence enrich the surfaced
// AskUserQuestion when OpenCode provides them.
type PermissionOption struct {
	OptionID    string   `json:"optionId"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind,omitempty"`
	Description string   `json:"description,omitempty"`
	Recommended bool     `json:"recommended,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
}

// ACP tool-call kinds the tracer recognizes. Unknown kinds still surface as a
// permission prompt (the user decides) rather than failing closed; only the
// "question" kind diverts to the AskUserQuestion flow.
const (
	ToolKindExecute  = "execute"
	ToolKindEdit     = "edit"
	ToolKindFetch    = "fetch"
	ToolKindSearch   = "search"
	ToolKindRead     = "read"
	ToolKindQuestion = "question"
)

// ACP permission option kinds. The allow_* kinds approve the action; the
// reject_* kinds decline it.
const (
	OptionKindAllowOnce    = "allow_once"
	OptionKindAllowAlways  = "allow_always"
	OptionKindRejectOnce   = "reject_once"
	OptionKindRejectAlways = "reject_always"
)

// PermissionResponse is the outbound JSON-RPC result for a
// session/request_permission request. It carries the user's outcome so OpenCode
// either runs the gated action / records the answer or skips it.
type PermissionResponse struct {
	JSONRPC string                  `json:"jsonrpc"`
	ID      int                     `json:"id"`
	Result  PermissionOutcomeResult `json:"result"`
}

// PermissionOutcomeResult wraps the outcome in the ACP result envelope.
type PermissionOutcomeResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// PermissionOutcome is the user's decision. Outcome is "selected" with the
// chosen OptionID, or "cancelled" when no option applies (e.g. a denial with no
// reject option, or a free-form answer that matched no listed choice).
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// ACP permission outcome discriminators.
const (
	OutcomeSelected  = "selected"
	OutcomeCancelled = "cancelled"
)
