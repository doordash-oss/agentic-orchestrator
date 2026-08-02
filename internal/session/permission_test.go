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

package session

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestAutoApproveHandler(t *testing.T) {
	handler := &permission.AutoApproveHandler{}
	req := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  "Bash",
		Input:     `{"command": "rm -rf /"}`,
		SessionID: "sess_1",
		FeatureID: "feat_1",
	}
	decision, err := handler.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("behavior = %q, want allow", decision.Behavior)
	}
}

func TestDenyAllHandler(t *testing.T) {
	handler := &permission.DenyAllHandler{}
	req := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  "Bash",
		Input:     `{"command": "ls"}`,
		SessionID: "sess_1",
		FeatureID: "feat_1",
	}
	decision, err := handler.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != "deny" {
		t.Errorf("behavior = %q, want deny", decision.Behavior)
	}
	if decision.Reason != "all tools denied" {
		t.Errorf("reason = %q, want 'all tools denied'", decision.Reason)
	}
}

func TestPermissionHandlerInterface(t *testing.T) {
	// Verify all handlers satisfy the interface.
	var _ ports.PermissionHandler = &permission.AutoApproveHandler{}
	var _ ports.PermissionHandler = &permission.DenyAllHandler{}
	var _ ports.PermissionHandler = &permission.ReadOnlyHandler{}
	var _ ports.PermissionHandler = &permission.AMAHandler{}
}

func TestReadOnlyHandler(t *testing.T) {
	handler := &permission.ReadOnlyHandler{}

	tests := []struct {
		name    string
		tool    string
		input   string
		wantBeh string // "allow" or "deny"
	}{
		// Read-only tools: allowed
		{"read allowed", "Read", `{"file_path":"/some/file.go"}`, "allow"},
		{"glob allowed", "Glob", `{"pattern":"**/*.go"}`, "allow"},
		{"grep allowed", "Grep", `{"pattern":"foo"}`, "allow"},
		{"ls allowed", "LS", `{}`, "allow"},
		{"lsp allowed", "LSP", `{}`, "allow"},
		{"websearch allowed", "WebSearch", `{"query":"test"}`, "allow"},
		{"webfetch allowed", "WebFetch", `{"url":"https://example.com"}`, "allow"},
		{"agent allowed", "Agent", `{"prompt":"research"}`, "allow"},
		{"taskcreate allowed", "TaskCreate", `{}`, "allow"},

		// Write tools: denied
		{"edit denied", "Edit", `{"file_path":"/any/file.go"}`, "deny"},
		{"write denied", "Write", `{"file_path":"/any/file.go"}`, "deny"},
		{"notebookedit denied", "NotebookEdit", `{"notebook_path":"/any/nb.ipynb"}`, "deny"},

		// Bash: denied
		{"bash denied", "Bash", `{"command":"ls"}`, "deny"},

		// Unknown tools: denied
		{"unknown denied", "SomeNewTool", `{}`, "deny"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := ports.ToolPermissionRequest{
				RequestID: "req_1",
				ToolName:  tt.tool,
				Input:     tt.input,
			}
			decision, err := handler.CanUseTool(req)
			if err != nil {
				t.Fatalf("CanUseTool: %v", err)
			}
			if decision.Behavior != tt.wantBeh {
				t.Errorf("behavior = %q, want %q", decision.Behavior, tt.wantBeh)
			}
		})
	}
}

func TestAMAHandler(t *testing.T) {
	handler := &permission.AMAHandler{}

	tests := []struct {
		name    string
		tool    string
		input   string
		wantBeh string
	}{
		{"read allowed", "Read", `{"file_path":"/some/file.go"}`, "allow"},
		{"grep allowed", "Grep", `{"pattern":"foo"}`, "allow"},
		{"websearch allowed", "WebSearch", `{"query":"test"}`, "allow"},
		{"todo allowed", "TodoWrite", `{}`, "allow"},
		{"agent denied", "Agent", `{"prompt":"research"}`, "deny"},
		{"task denied", "Task", `{"prompt":"research"}`, "deny"},
		{"bash deferred", "Bash", `{"command":"ps -p 123"}`, ""},
		{"edit deferred", "Edit", `{"file_path":"/tmp/diagnostic.sh"}`, ""},
		{"unknown deferred", "SomeNewTool", `{}`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, err := handler.CanUseTool(ports.ToolPermissionRequest{
				RequestID: "req_1",
				ToolName:  tt.tool,
				Input:     tt.input,
			})
			if err != nil {
				t.Fatalf("CanUseTool: %v", err)
			}
			if decision.Behavior != tt.wantBeh {
				t.Errorf("behavior = %q, want %q", decision.Behavior, tt.wantBeh)
			}
		})
	}
}

func TestAutoApproveHandler_MultipleCalls(t *testing.T) {
	handler := &permission.AutoApproveHandler{}
	tools := []string{"Bash", "Read", "Write", "Edit", "Glob", "Grep"}
	for _, tool := range tools {
		req := ports.ToolPermissionRequest{
			RequestID: "req_" + tool,
			ToolName:  tool,
		}
		decision, err := handler.CanUseTool(req)
		if err != nil {
			t.Fatalf("CanUseTool(%s): %v", tool, err)
		}
		if decision.Behavior != "allow" {
			t.Errorf("CanUseTool(%s) = %q, want allow", tool, decision.Behavior)
		}
	}
}
