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

package permission

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestSupervisorHandler_AllowsReadOnlyAndWebTools(t *testing.T) {
	t.Parallel()
	h := &SupervisorHandler{}
	for _, tool := range []string{"Read", "Glob", "Grep", "LS", "WebSearch", "WebFetch", "TodoWrite"} {
		got, err := h.CanUseTool(ports.ToolPermissionRequest{ToolName: tool, Input: `{}`})
		if err != nil || got.Behavior != DecisionAllow {
			t.Fatalf("CanUseTool(%s) = %+v, %v; want allow", tool, got, err)
		}
	}
}

func TestSupervisorHandler_DefersShellEditsAndSubAgents(t *testing.T) {
	t.Parallel()
	h := &SupervisorHandler{}
	for _, tool := range []string{"Bash", "Edit", "Write", "MultiEdit", "NotebookEdit", "Agent", "Task", "mcp__server__tool"} {
		got, err := h.CanUseTool(ports.ToolPermissionRequest{ToolName: tool, Input: `{"command":"ls"}`})
		if err != nil || got.Behavior != "" {
			t.Fatalf("CanUseTool(%s) = %+v, %v; want deferral to the user", tool, got, err)
		}
	}
}

func TestSupervisorHandler_ParticipatesInReviewWithoutGeneralPhaseExceptions(t *testing.T) {
	t.Parallel()
	h := &SupervisorHandler{}
	if !IsAutomaticReviewHandler(h) {
		t.Fatal("IsAutomaticReviewHandler(SupervisorHandler) = false, want true")
	}
	if IsGeneralPhaseHandler(h) {
		t.Fatal("IsGeneralPhaseHandler(SupervisorHandler) = true, want false")
	}
	if wrapped := WrapGeneralPhaseHandlerWithSafeCreate(h, []string{t.TempDir()}); wrapped != h {
		t.Fatal("safe-create wrapper must leave the supervisor handler unwrapped")
	}
}
