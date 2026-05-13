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
	"testing"
)

func TestSDKMessage_UnmarshalJSON_SystemInit(t *testing.T) {
	data := `{
		"type": "system",
		"subtype": "init",
		"session_id": "sess_abc123",
		"model": "claude-sonnet-4-6",
		"tools": ["Bash"],
		"mcp_servers": [{"name": "test-server", "status": "connected"}],
		"permissionMode": "acceptEdits"
	}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "system" {
		t.Errorf("type = %q, want system", msg.Type)
	}
	if msg.Subtype != "init" {
		t.Errorf("subtype = %q, want init", msg.Subtype)
	}
	if msg.Init == nil {
		t.Fatal("Init is nil")
	}
	if msg.Init.SessionID != "sess_abc123" {
		t.Errorf("session_id = %q, want sess_abc123", msg.Init.SessionID)
	}
	if msg.Init.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q, want claude-sonnet-4-6", msg.Init.Model)
	}
	if len(msg.Init.Tools) != 1 || msg.Init.Tools[0] != "Bash" {
		t.Errorf("tools = %+v, want [Bash]", msg.Init.Tools)
	}
	if len(msg.Init.MCPServers) != 1 || msg.Init.MCPServers[0].Name != "test-server" {
		t.Errorf("mcp_servers = %+v, want [{test-server connected}]", msg.Init.MCPServers)
	}
	if msg.Init.PermissionMode != "acceptEdits" {
		t.Errorf("permissionMode = %q, want acceptEdits", msg.Init.PermissionMode)
	}
}

func TestSDKMessage_UnmarshalJSON_Assistant(t *testing.T) {
	data := `{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "text", "text": "Hello, world!"},
				{"type": "tool_use", "id": "tu_1", "name": "Bash", "input": {"command": "ls"}},
				{"type": "thinking", "thinking": "Let me think about this..."}
			],
			"model": "claude-sonnet-4-6",
			"usage": {"input_tokens": 100, "output_tokens": 50}
		},
		"session_id": "sess_abc123"
	}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "assistant" {
		t.Errorf("type = %q, want assistant", msg.Type)
	}
	if msg.Assistant == nil {
		t.Fatal("Assistant is nil")
	}
	content := msg.Assistant.Message.Content
	if len(content) != 3 {
		t.Fatalf("content blocks = %d, want 3", len(content))
	}
	if !content[0].IsText() || content[0].Text != "Hello, world!" {
		t.Errorf("block[0] = %+v, want text 'Hello, world!'", content[0])
	}
	if !content[1].IsToolUse() || content[1].Name != "Bash" {
		t.Errorf("block[1] = %+v, want tool_use Bash", content[1])
	}
	if !content[2].IsThinking() || content[2].Thinking != "Let me think about this..." {
		t.Errorf("block[2] = %+v, want thinking", content[2])
	}
	if msg.Assistant.Message.Usage == nil || msg.Assistant.Message.Usage.InputTokens != 100 {
		t.Errorf("usage = %+v, want input_tokens=100", msg.Assistant.Message.Usage)
	}
}

func TestSDKMessage_UnmarshalJSON_User(t *testing.T) {
	data := `{
		"type": "user",
		"message": {
			"role": "user",
			"content": [
				{"type": "tool_result", "tool_use_id": "tu_1", "content": "output here"}
			]
		}
	}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.User == nil {
		t.Fatal("User is nil")
	}
	if len(msg.User.Message.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(msg.User.Message.Content))
	}
	block := msg.User.Message.Content[0]
	if !block.IsToolResult() || block.ToolUseID != "tu_1" {
		t.Errorf("block = %+v, want tool_result for tu_1", block)
	}
}

func TestSDKMessage_UnmarshalJSON_Result(t *testing.T) {
	data := `{
		"type": "result",
		"subtype": "success",
		"session_id": "sess_abc123",
		"total_cost_usd": 0.0342,
		"usage": {
			"input_tokens": 15000,
			"output_tokens": 3000,
			"cache_read_input_tokens": 8000,
			"cache_creation_input_tokens": 2000
		},
		"duration_ms": 12500.5,
		"num_turns": 3
	}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Result == nil {
		t.Fatal("Result is nil")
	}
	r := msg.Result
	if !r.IsSuccess() {
		t.Errorf("IsSuccess() = false, want true")
	}
	if r.TotalCostUSD != 0.0342 {
		t.Errorf("cost = %f, want 0.0342", r.TotalCostUSD)
	}
	if r.Usage.InputTokens != 15000 {
		t.Errorf("input_tokens = %d, want 15000", r.Usage.InputTokens)
	}
	if r.Usage.CacheReadInputTokens != 8000 {
		t.Errorf("cache_read = %d, want 8000", r.Usage.CacheReadInputTokens)
	}
	if r.NumTurns != 3 {
		t.Errorf("num_turns = %d, want 3", r.NumTurns)
	}
}

func TestSDKMessage_UnmarshalJSON_ResultError(t *testing.T) {
	data := `{
		"type": "result",
		"subtype": "error",
		"session_id": "sess_abc123",
		"total_cost_usd": 0.01,
		"is_error": true,
		"result": "API rate limit exceeded"
	}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Result == nil {
		t.Fatal("Result is nil")
	}
	if msg.Result.IsSuccess() {
		t.Error("IsSuccess() = true, want false")
	}
	if !msg.Result.IsError {
		t.Error("IsError = false, want true")
	}
	if msg.Result.Result != "API rate limit exceeded" {
		t.Errorf("result = %q, want 'API rate limit exceeded'", msg.Result.Result)
	}
}

func TestSDKMessage_UnmarshalJSON_ResultMaxTurns(t *testing.T) {
	data := `{"type": "result", "subtype": "max_turns", "session_id": "s1", "total_cost_usd": 0.05}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Result.Subtype != "max_turns" {
		t.Errorf("Subtype = %q, want max_turns", msg.Result.Subtype)
	}
}

func TestSDKMessage_UnmarshalJSON_ResultStopReason(t *testing.T) {
	tests := []struct {
		name      string
		json      string
		wantStop  string
		wantTrunc bool
		wantClass TerminationClass
		inputs    TerminationInputs
	}{
		{
			name:      "success with tool_use stop is truncated",
			json:      `{"type":"result","subtype":"success","session_id":"s1","stop_reason":"tool_use"}`,
			wantStop:  "tool_use",
			wantTrunc: true,
			wantClass: TermTurnTruncated,
		},
		{
			name:      "success with end_turn stop is deliberate",
			json:      `{"type":"result","subtype":"success","session_id":"s1","stop_reason":"end_turn"}`,
			wantStop:  "end_turn",
			wantTrunc: false,
			wantClass: TermEndedAfterText,
		},
		{
			name:      "success with max_tokens stop is truncated",
			json:      `{"type":"result","subtype":"success","session_id":"s1","stop_reason":"max_tokens"}`,
			wantStop:  "max_tokens",
			wantTrunc: true,
			wantClass: TermTurnTruncated,
		},
		{
			name:      "success with pause_turn stop is truncated",
			json:      `{"type":"result","subtype":"success","session_id":"s1","stop_reason":"pause_turn"}`,
			wantStop:  "pause_turn",
			wantTrunc: true,
			wantClass: TermTurnTruncated,
		},
		{
			name:      "refusal is not truncation",
			json:      `{"type":"result","subtype":"success","session_id":"s1","stop_reason":"refusal"}`,
			wantStop:  "refusal",
			wantTrunc: false,
			wantClass: TermRefused,
		},
		{
			name:      "missing stop_reason defaults to deliberate end",
			json:      `{"type":"result","subtype":"success","session_id":"s1"}`,
			wantStop:  "",
			wantTrunc: false,
			wantClass: TermEndedAfterText,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var msg SDKMessage
			if err := json.Unmarshal([]byte(tc.json), &msg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if msg.Result == nil {
				t.Fatal("Result is nil")
			}
			if msg.Result.StopReason != tc.wantStop {
				t.Errorf("StopReason = %q, want %q", msg.Result.StopReason, tc.wantStop)
			}
			if got := msg.Result.IsTurnTruncated(); got != tc.wantTrunc {
				t.Errorf("IsTurnTruncated() = %v, want %v", got, tc.wantTrunc)
			}
			in := tc.inputs
			in.Result = msg.Result
			if got := ClassifyTermination(in); got != tc.wantClass {
				t.Errorf("ClassifyTermination() = %s, want %s", got, tc.wantClass)
			}
		})
	}
}

func TestClassifyTermination_Priorities(t *testing.T) {
	// phase_complete wins over everything.
	t.Run("phase_complete beats tool_use truncation", func(t *testing.T) {
		in := TerminationInputs{
			Result:              &ResultMessage{Subtype: "success", StopReason: "tool_use"},
			PhaseCompleteExists: true,
		}
		if got := ClassifyTermination(in); got != TermCompleted {
			t.Errorf("got %s, want Completed", got)
		}
	})
	// AskUserQuestion beats plain truncation but not completion.
	t.Run("AskUserQuestion beats truncation", func(t *testing.T) {
		in := TerminationInputs{
			Result:                 &ResultMessage{Subtype: "success", StopReason: "tool_use"},
			AskUserQuestionPending: true,
		}
		if got := ClassifyTermination(in); got != TermAskedFormal {
			t.Errorf("got %s, want AskedFormal", got)
		}
	})
	// Error subtype beats any stop_reason.
	t.Run("error subtype beats stop_reason", func(t *testing.T) {
		in := TerminationInputs{
			Result: &ResultMessage{Subtype: "error", IsError: true, StopReason: "end_turn"},
		}
		if got := ClassifyTermination(in); got != TermErrored {
			t.Errorf("got %s, want Errored", got)
		}
	})
	t.Run("nil result is Unknown", func(t *testing.T) {
		if got := ClassifyTermination(TerminationInputs{}); got != TermUnknown {
			t.Errorf("got %s, want Unknown", got)
		}
	})
}

func TestSDKMessage_UnmarshalJSON_ControlRequest(t *testing.T) {
	data := `{
		"type": "control_request",
		"request_id": "req_1_abc123",
		"request": {
			"subtype": "can_use_tool",
			"tool_name": "Bash",
			"input": {"command": "git diff"}
		}
	}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.ControlRequest == nil {
		t.Fatal("ControlRequest is nil")
	}
	if msg.ControlRequest.RequestID != "req_1_abc123" {
		t.Errorf("request_id = %q, want req_1_abc123", msg.ControlRequest.RequestID)
	}
	if msg.ControlRequest.Request.Subtype != "can_use_tool" {
		t.Errorf("subtype = %q, want can_use_tool", msg.ControlRequest.Request.Subtype)
	}
	if msg.ControlRequest.Request.ToolName != "Bash" {
		t.Errorf("tool_name = %q, want Bash", msg.ControlRequest.Request.ToolName)
	}
}

func TestSDKMessage_UnmarshalJSON_Status(t *testing.T) {
	data := `{"type": "status", "message": "Loading model..."}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Status == nil {
		t.Fatal("Status is nil")
	}
	if msg.Status.Message != "Loading model..." {
		t.Errorf("message = %q, want 'Loading model...'", msg.Status.Message)
	}
}

func TestSDKMessage_UnmarshalJSON_ToolProgress(t *testing.T) {
	data := `{"type": "tool_progress", "tool_use_id": "tu_1", "tool_name": "Bash", "data": "PASS ok"}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.ToolProgress == nil {
		t.Fatal("ToolProgress is nil")
	}
	if msg.ToolProgress.ToolName != "Bash" {
		t.Errorf("tool_name = %q, want Bash", msg.ToolProgress.ToolName)
	}
	if msg.ToolProgress.Data != "PASS ok" {
		t.Errorf("data = %q, want 'PASS ok'", msg.ToolProgress.Data)
	}
}

func TestSDKMessage_UnmarshalJSON_CompactBoundary(t *testing.T) {
	data := `{"type": "system", "subtype": "compact_boundary"}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Compact == nil {
		t.Fatal("Compact is nil")
	}
	if msg.Type != "system" || msg.Subtype != "compact_boundary" {
		t.Errorf("type/subtype = %s/%s, want system/compact_boundary", msg.Type, msg.Subtype)
	}
}

func TestSDKMessage_UnmarshalJSON_TaskStarted(t *testing.T) {
	data := `{
        "type": "system",
        "subtype": "task_started",
        "task_id": "ac70c5db86d251745",
        "tool_use_id": "toolu_01Lcugk1pteTjZcDGdvtNb2p",
        "description": "Feature state + schema + concurrency + recovery",
        "task_type": "local_agent",
        "prompt": "Context: I am researching the codebase...",
        "session_id": "sess-1"
    }`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.TaskStarted == nil {
		t.Fatal("TaskStarted is nil")
	}
	ts := msg.TaskStarted
	if ts.TaskID != "ac70c5db86d251745" {
		t.Errorf("task_id = %q", ts.TaskID)
	}
	if ts.ToolUseID != "toolu_01Lcugk1pteTjZcDGdvtNb2p" {
		t.Errorf("tool_use_id = %q", ts.ToolUseID)
	}
	if ts.Description != "Feature state + schema + concurrency + recovery" {
		t.Errorf("description = %q", ts.Description)
	}
	if ts.TaskType != "local_agent" {
		t.Errorf("task_type = %q", ts.TaskType)
	}
	// Prompt should round-trip but is not logged; sanity-check it's populated.
	if ts.Prompt == "" {
		t.Error("prompt should be parsed into the struct")
	}
}

func TestSDKMessage_UnmarshalJSON_TaskProgress(t *testing.T) {
	data := `{
        "type": "system",
        "subtype": "task_progress",
        "task_id": "a41781543410527d1",
        "tool_use_id": "toolu_01PjANDr7xp1ZEcgzhd5L8p6",
        "description": "Reading go.mod",
        "last_tool_name": "Read",
        "usage": {"total_tokens": 202895, "tool_uses": 96, "duration_ms": 2034491},
        "session_id": "92b2ea6d-5ab4-43f2-8dc5-0646813b7542"
    }`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.TaskProgress == nil {
		t.Fatal("TaskProgress is nil")
	}
	if msg.Type != "system" || msg.Subtype != "task_progress" {
		t.Errorf("type/subtype = %s/%s, want system/task_progress", msg.Type, msg.Subtype)
	}
	tp := msg.TaskProgress
	if tp.TaskID != "a41781543410527d1" {
		t.Errorf("task_id = %q", tp.TaskID)
	}
	if tp.ToolUseID != "toolu_01PjANDr7xp1ZEcgzhd5L8p6" {
		t.Errorf("tool_use_id = %q", tp.ToolUseID)
	}
	if tp.Description != "Reading go.mod" {
		t.Errorf("description = %q", tp.Description)
	}
	if tp.LastToolName != "Read" {
		t.Errorf("last_tool_name = %q", tp.LastToolName)
	}
	if tp.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if tp.Usage.TotalTokens != 202895 || tp.Usage.ToolUses != 96 || tp.Usage.DurationMs != 2034491 {
		t.Errorf("usage = %+v, want {202895, 96, 2034491}", tp.Usage)
	}
}

func TestSDKMessage_UnmarshalJSON_TaskProgress_NoUsage(t *testing.T) {
	// A progress event with no usage field must not panic and must still populate
	// the surrounding fields. Defensive: SDK may omit usage at early stages.
	data := `{"type":"system","subtype":"task_progress","task_id":"t1"}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.TaskProgress == nil {
		t.Fatal("TaskProgress is nil")
	}
	if msg.TaskProgress.Usage != nil {
		t.Errorf("Usage should be nil when absent, got %+v", msg.TaskProgress.Usage)
	}
}

func TestSDKMessage_UnmarshalJSON_TaskNotification(t *testing.T) {
	data := `{
        "type": "system",
        "subtype": "task_notification",
        "task_id": "ad5c4d78dad2b6b12",
        "tool_use_id": "toolu_01P1e1DMRoAu3wcSPfvJ3zXC",
        "status": "completed",
        "summary": "Re-run conventions research",
        "output_file": "",
        "usage": {"total_tokens": 3247, "tool_uses": 95, "duration_ms": 4624037},
        "session_id": "92b2ea6d-5ab4-43f2-8dc5-0646813b7542"
    }`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.TaskNotification == nil {
		t.Fatal("TaskNotification is nil")
	}
	tn := msg.TaskNotification
	if tn.TaskID != "ad5c4d78dad2b6b12" {
		t.Errorf("task_id = %q", tn.TaskID)
	}
	if tn.Status != "completed" {
		t.Errorf("status = %q", tn.Status)
	}
	if tn.Summary != "Re-run conventions research" {
		t.Errorf("summary = %q", tn.Summary)
	}
	if tn.Usage == nil || tn.Usage.DurationMs != 4624037 {
		t.Errorf("usage = %+v, want duration_ms=4624037", tn.Usage)
	}
}

func TestSDKMessage_UnmarshalJSON_RateLimit(t *testing.T) {
	data := `{"type": "rate_limit", "retry_after_ms": 5000, "message": "Rate limited"}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.RateLimit == nil {
		t.Fatal("RateLimit is nil")
	}
	if msg.RateLimit.RetryMS != 5000 {
		t.Errorf("retry_ms = %f, want 5000", msg.RateLimit.RetryMS)
	}
}

func TestSDKMessage_UnmarshalJSON_HookMessages(t *testing.T) {
	tests := []struct {
		name string
		data string
		typ  string
	}{
		{"hook_started", `{"type": "hook_started", "hook_name": "pre-commit"}`, "hook_started"},
		{"hook_progress", `{"type": "hook_progress", "hook_name": "pre-commit", "data": "running..."}`, "hook_progress"},
		{"hook_response", `{"type": "hook_response", "hook_name": "pre-commit", "result": "ok"}`, "hook_response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg SDKMessage
			if err := json.Unmarshal([]byte(tt.data), &msg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if msg.Type != tt.typ {
				t.Errorf("type = %q, want %q", msg.Type, tt.typ)
			}
		})
	}
}

func TestSDKMessage_UnmarshalJSON_UnknownType(t *testing.T) {
	data := `{"type": "future_type", "subtype": "unknown", "data": "test"}`
	var msg SDKMessage
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		t.Fatalf("should not error on unknown type: %v", err)
	}
	if msg.Type != "future_type" {
		t.Errorf("type = %q, want future_type", msg.Type)
	}
	if msg.Init != nil || msg.Assistant != nil || msg.Result != nil {
		t.Error("unknown type should have nil concrete pointers")
	}
}

func TestNewAllowResponse(t *testing.T) {
	resp := NewAllowResponse("req_1")
	if resp.Type != "control_response" {
		t.Errorf("type = %q, want control_response", resp.Type)
	}
	if resp.Response.RequestID != "req_1" {
		t.Errorf("request_id = %q, want req_1", resp.Response.RequestID)
	}
	if resp.Response.Response["behavior"] != "allow" {
		t.Errorf("behavior = %v, want allow", resp.Response.Response["behavior"])
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(data) {
		t.Error("invalid JSON output")
	}
}

func TestNewDenyResponse(t *testing.T) {
	resp := NewDenyResponse("req_2", "dangerous command")
	if resp.Response.Response["behavior"] != "deny" {
		t.Errorf("behavior = %v, want deny", resp.Response.Response["behavior"])
	}
	if resp.Response.Response["message"] != "dangerous command" {
		t.Errorf("message = %v, want 'dangerous command'", resp.Response.Response["message"])
	}
}

func TestNewUserInput(t *testing.T) {
	input := NewUserInput("Hello, Claude!")
	if input.Type != "user" {
		t.Errorf("type = %q, want user", input.Type)
	}
	if input.Message.Role != "user" {
		t.Errorf("role = %q, want user", input.Message.Role)
	}
	if input.Message.Content != "Hello, Claude!" {
		t.Errorf("content = %q, want 'Hello, Claude!'", input.Message.Content)
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(data) {
		t.Error("invalid JSON output")
	}
}
