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
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
)

func deferredBashControlRequest(requestID, command string) llm.SDKMessage {
	input, _ := json.Marshal(map[string]string{"command": command})
	return llm.SDKMessage{
		Type: "control_request",
		ControlRequest: &llm.ControlRequestMessage{
			Type:      "control_request",
			RequestID: requestID,
			Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "Bash", Input: input},
		},
	}
}

// A handler that defers with a Reason still leaves the request to the user;
// if the user denies it, the model receives the handler's explanation beside
// the user's denial so it can retry with the sanctioned shape.
func TestDeferredReasonReachesModelOnUserDeny(t *testing.T) {
	t.Parallel()
	s := NewSession("supervisor-defer", "supervisor", feature.PhaseResearch)
	var stdin bytes.Buffer
	s.SetStdinForTest(nopWriteCloser{Writer: &stdin})
	s.permHandler = &permission.SupervisorHandler{}

	msg := deferredBashControlRequest("compound-1", `"$AGENTICO_BIN" api GET /api/v1/features | jq .`)
	if s.tryHandleControlRequest(msg) {
		t.Fatalf("compound helper call was auto-handled (stdin %q); want deferral to the user", stdin.String())
	}
	if !strings.Contains(msg.ControlRequest.DeferralReason, "agentico api must be invoked as a single bare command") {
		t.Fatalf("DeferralReason = %q, want the helper shape explanation", msg.ControlRequest.DeferralReason)
	}
	s.mu.Lock()
	s.status = SessionWaitingPermission
	s.recordPendingControlRequestLocked(msg.ControlRequest)
	s.mu.Unlock()

	if err := s.RespondToControl("compound-1", false, "denied by user"); err != nil {
		t.Fatalf("RespondToControl: %v", err)
	}
	wire := stdin.String()
	if !strings.Contains(wire, `"behavior":"deny"`) || !strings.Contains(wire, "denied by user") ||
		!strings.Contains(wire, "agentico api must be invoked as a single bare command") {
		t.Fatalf("deny response = %q, want the user's denial plus the deferral reason", wire)
	}
}

func TestDeferredReasonDoesNotChangeAllowOrPlainDeny(t *testing.T) {
	t.Parallel()
	s := NewSession("supervisor-defer-plain", "supervisor", feature.PhaseResearch)
	var stdin bytes.Buffer
	s.SetStdinForTest(nopWriteCloser{Writer: &stdin})
	s.permHandler = &permission.SupervisorHandler{}

	plain := deferredBashControlRequest("plain-1", `rm -rf /tmp/x`)
	if s.tryHandleControlRequest(plain) {
		t.Fatal("arbitrary Bash was auto-handled; want deferral")
	}
	if plain.ControlRequest.DeferralReason != "" {
		t.Fatalf("DeferralReason = %q, want empty for a plain deferral", plain.ControlRequest.DeferralReason)
	}
	allowed := deferredBashControlRequest("allowed-1", `"$AGENTICO_BIN" api GET /api/v1/features | jq .`)
	if s.tryHandleControlRequest(allowed) {
		t.Fatal("compound helper call was auto-handled; want deferral")
	}
	s.mu.Lock()
	s.recordPendingControlRequestLocked(plain.ControlRequest)
	s.recordPendingControlRequestLocked(allowed.ControlRequest)
	s.mu.Unlock()

	if err := s.RespondToControl("plain-1", false, "denied by user"); err != nil {
		t.Fatalf("RespondToControl(plain-1): %v", err)
	}
	if err := s.RespondToControl("allowed-1", true, ""); err != nil {
		t.Fatalf("RespondToControl(allowed-1): %v", err)
	}
	wire := stdin.String()
	if strings.Contains(wire, "single bare command") {
		t.Fatalf("responses = %q; the shape reason must ride only on a deny of the request that carried it", wire)
	}
	if !strings.Contains(wire, `"message":"denied by user"`) {
		t.Fatalf("plain deny response = %q, want the unchanged user denial", wire)
	}
}
