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

// denyAllTestHandler is a session-test double that denies every tool request.
type denyAllTestHandler struct{}

func (denyAllTestHandler) CanUseTool(ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	return ports.PermissionDecision{Behavior: "deny", Reason: "all tools denied"}, nil
}

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

func TestPermissionHandlerInterface(t *testing.T) {
	// Verify all handlers satisfy the interface.
	var _ ports.PermissionHandler = &permission.AutoApproveHandler{}
	var _ ports.PermissionHandler = &permission.SupervisorHandler{}
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
