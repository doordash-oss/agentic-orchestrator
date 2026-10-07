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
	"strconv"
	"strings"
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

func supervisorBash(t *testing.T, command string) ports.PermissionDecision {
	t.Helper()
	got, err := (&SupervisorHandler{}).CanUseTool(ports.ToolPermissionRequest{
		ToolName: toolNameBash,
		Input:    `{"command":` + strconv.Quote(command) + `}`,
	})
	if err != nil {
		t.Fatalf("CanUseTool(%q) error = %v", command, err)
	}
	return got
}

func TestSupervisorHandler_AllowsBareHelperInvocations(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		`"$AGENTICO_BIN" api GET /api/v1/features`,
		`$AGENTICO_BIN api GET /api/v1/features`,
		`  "$AGENTICO_BIN" api GET /api/v1/features?state=running  `,
		`/Applications/Agentico.app/Contents/Resources/bin/agentico api GET /api/v1/health`,
		`agentico api DELETE /api/v1/features/feat-1`,
		`"$AGENTICO_BIN" api POST /api/v1/features '{"name":"my feature","description":"a b"}'`,
		`"$AGENTICO_BIN" api PATCH /api/v1/features/feat-1/config "{\"model\": \"claude:opus\", \"notes\": \"x y z\"}"`,
		`"$AGENTICO_BIN" api put /api/v1/runtime-config '{"k": "v"}'`,
		`"$AGENTICO_BIN" api GET /api/v1/events --timeout 30s`,
	} {
		got := supervisorBash(t, command)
		if got.Behavior != DecisionAllow {
			t.Errorf("CanUseTool(%q) = %+v; want allow", command, got)
		}
	}
}

func TestSupervisorHandler_DefersCompoundHelperCallsWithShapeReason(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		`"$AGENTICO_BIN" api GET /api/v1/features | jq .`,
		`"$AGENTICO_BIN" api GET /api/v1/features|jq .`,
		`cd /tmp && "$AGENTICO_BIN" api GET /api/v1/features`,
		`"$AGENTICO_BIN" api GET /api/v1/features; echo done`,
		`echo $("$AGENTICO_BIN" api GET /api/v1/features)`,
		"echo `\"$AGENTICO_BIN\" api GET /api/v1/features`",
		`"$AGENTICO_BIN" api GET "/api/v1/features/$HOME"`,
		`"$AGENTICO_BIN" api POST /api/v1/features "{\"name\":\"$USER\"}"`,
		`"$AGENTICO_BIN" api GET /api/v1/features > /tmp/out.json`,
		`"$AGENTICO_BIN" api POST /api/v1/features < body.json`,
		`"$AGENTICO_BIN" api GET /api/v1/features &`,
		"\"$AGENTICO_BIN\" api GET /api/v1/features\nrm -rf /tmp/x",
		`/opt/bin/agentico api GET /api/v1/health || true`,
		// A shell expansion must not pick which agentico binary runs.
		`$HOME/bin/agentico api GET /api/v1/features`,
		`~/bin/agentico api GET /api/v1/features`,
	} {
		got := supervisorBash(t, command)
		if got.Behavior != "" {
			t.Errorf("CanUseTool(%q) = %+v; want deferral to the user", command, got)
			continue
		}
		if got.Reason != supervisorHelperShapeReason {
			t.Errorf("CanUseTool(%q).Reason = %q; want the helper shape explanation", command, got.Reason)
		}
	}
	for _, part := range []string{"agentico api must be invoked as a single bare command", "&&", "pipes", "command substitution", "shell variables", `"$AGENTICO_BIN" api GET /api/v1/features`} {
		if !strings.Contains(supervisorHelperShapeReason, part) {
			t.Errorf("shape reason %q is missing %q", supervisorHelperShapeReason, part)
		}
	}
}

func TestSupervisorHandler_DefersOtherBashWithoutHelperReason(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		`"$AGENTICO_BIN" validate-artifacts --phase review --role final_reviewer --dir /tmp/iteration-01`,
		`"$AGENTICO_BIN" verify-evidence --contract /work/testing-contract.yaml --dir /work/iter`,
		`"$AGENTICO_BIN" server`,
		`"$OTHER_BIN" api GET /api/v1/features`,
		`curl -s http://127.0.0.1:7777/api/v1/features`,
		`ls -la`,
		`rm -rf /tmp/x`,
		`agentico-evil api GET /api/v1/features`,
		``,
	} {
		got := supervisorBash(t, command)
		if got.Behavior != "" || got.Reason != "" {
			t.Errorf("CanUseTool(%q) = %+v; want a plain deferral", command, got)
		}
	}
}

func TestSupervisorHandler_HelperAllowanceIsBashOnly(t *testing.T) {
	t.Parallel()
	got, err := (&SupervisorHandler{}).CanUseTool(ports.ToolPermissionRequest{
		ToolName: "mcp__shell__run",
		Input:    `{"command":"\"$AGENTICO_BIN\" api GET /api/v1/features"}`,
	})
	if err != nil || got.Behavior != "" {
		t.Fatalf("non-Bash tool carrying a helper command = %+v, %v; want deferral", got, err)
	}
}

func TestFeatureWorkerRecogniserDoesNotSanctionAPI(t *testing.T) {
	t.Parallel()
	if harnessCLISubcommands["api"] {
		t.Fatal("feature-worker sanctioned subcommands must not include api")
	}
	if isHarnessCLIInvocation(`"$AGENTICO_BIN" api GET /api/v1/features`) {
		t.Fatal("isHarnessCLIInvocation accepted the supervisor-only api subcommand")
	}
	if _, ok := attemptedHarnessCLISubcommand(`cd /x && "$AGENTICO_BIN" api GET /api/v1/features`); ok {
		t.Fatal("attemptedHarnessCLISubcommand recognised api as a sanctioned feature-worker subcommand")
	}
	// A general phase handler behind the session guard still refuses api
	// against the harness-owned contract as an unsanctioned subcommand.
	handler := Guarded(&AcceptEditsHandler{})
	got, err := handler.CanUseTool(ports.ToolPermissionRequest{
		ToolName:     toolNameBash,
		Input:        `{"command":` + strconv.Quote(`"$AGENTICO_BIN" api POST /api/v1/features /work/testing-contract.yaml`) + `}`,
		ProviderName: providerNameClaude,
	})
	if err != nil || got.Behavior != DecisionDeny || !strings.Contains(got.Reason, "testing-contract.yaml is harness-owned") {
		t.Fatalf("guarded general handler on api = %+v, %v; want the generic harness-owned deny", got, err)
	}
	// And a general phase handler never auto-allows a bare helper call.
	got, err = handler.CanUseTool(ports.ToolPermissionRequest{
		ToolName: toolNameBash,
		Input:    `{"command":` + strconv.Quote(`"$AGENTICO_BIN" api GET /api/v1/features`) + `}`,
	})
	if err != nil || got.Behavior != "" {
		t.Fatalf("general handler on bare api call = %+v, %v; want deferral", got, err)
	}
}
