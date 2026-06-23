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
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// These tests prove OpenCode control requests drive the same session lifecycle
// as existing providers: a real opencode.Protocol parses fake ACP control lines
// into shared control requests, the session parks in the right waiting state,
// the pending request is replayable, and the user's decision resumes the session
// through the provider protocol's ACP outcome (Task 4).

// newOpenCodeProtocol returns a real opencode protocol writing its outbound ACP
// messages into buf, so a test can assert the exact wire response.
func newOpenCodeProtocol(t *testing.T) (*opencode.Protocol, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	p := opencode.NewProtocol(llm.ProtocolOpts{Model: "opencode:anthropic/claude-sonnet-4-5"})
	p.SetStdin(buf)
	return p, buf
}

func ocLine(t *testing.T, v map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal ACP line: %v", err)
	}
	return b
}

func ocPermissionLine(t *testing.T, id int, kind, title string, rawInput map[string]any) []byte {
	t.Helper()
	return ocLine(t, map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "session/request_permission",
		"params": map[string]any{
			"sessionId": "ses_x",
			"toolCall":  map[string]any{"kind": kind, "title": title, "rawInput": rawInput},
			"options": []map[string]any{
				{"optionId": "opt-allow", "name": "Allow", "kind": "allow_once"},
				{"optionId": "opt-reject", "name": "Reject", "kind": "reject_once"},
			},
		},
	})
}

func parseOneControl(t *testing.T, p *opencode.Protocol, line []byte) llm.SDKMessage {
	t.Helper()
	msgs, err := p.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ControlRequest == nil {
		t.Fatalf("ParseLine produced %+v, want one control_request", msgs)
	}
	return msgs[0]
}

// TestOpenCodePermissionRequestParksReplaysAndApproves proves an OpenCode shell
// permission request transitions the session to the permission-waiting state, is
// replayable for a user who attaches after it arrived, and that an approval
// resumes the session by selecting the allow option through the ACP outcome.
func TestOpenCodePermissionRequestParksReplaysAndApproves(t *testing.T) {
	p, buf := newOpenCodeProtocol(t)
	msg := parseOneControl(t, p, ocPermissionLine(t, 4242, "execute", "Run tests", map[string]any{"command": "go test ./..."}))
	if msg.ControlRequest.Request.ToolName != "Bash" {
		t.Fatalf("tool name = %q, want Bash", msg.ControlRequest.Request.ToolName)
	}

	mgr := NewManager(make(chan interface{}, 10))
	sess := NewSession("oc-perm", "feat-1", feature.PhaseImplement)
	sess.protocol = p
	// Record the request as readMessages would, so an attaching user can replay it.
	sess.SetLastControlRequest(msg.ControlRequest)
	mgr.handleSessionMessage(sess, sess.ID(), sess.FeatureID(), sess.Phase(), msg)

	if got := sess.Status(); got != SessionWaitingPermission {
		t.Fatalf("status = %v, want SessionWaitingPermission", got)
	}
	pending := sess.PendingControlRequests()
	if len(pending) != 1 || pending[0].RequestID != "4242" {
		t.Fatalf("PendingControlRequests() = %+v, want one replayable request id 4242", pending)
	}

	if err := sess.RespondToControl("4242", true, ""); err != nil {
		t.Fatalf("RespondToControl(approve) error: %v", err)
	}
	if !strings.Contains(buf.String(), `"outcome":"selected"`) || !strings.Contains(buf.String(), "opt-allow") {
		t.Fatalf("approval did not select the allow option via ACP outcome: %q", buf.String())
	}
}

// TestOpenCodeReadPermissionAutoApprovedByDefault proves OpenCode read-scoped
// permission requests stay inside Agentico's shared read-only auto-approval
// path instead of surfacing to the TUI as ExternalDirectory prompts.
func TestOpenCodeReadPermissionAutoApprovedByDefault(t *testing.T) {
	p, buf := newOpenCodeProtocol(t)
	msg := parseOneControl(t, p, ocPermissionLine(t, 73, "read", "Read guidelines", map[string]any{"path": "/repo/AGENTS.md"}))
	if msg.ControlRequest.Request.ToolName != "ExternalDirectory" {
		t.Fatalf("tool name = %q, want ExternalDirectory", msg.ControlRequest.Request.ToolName)
	}

	sess := NewSession("oc-read", "feat-1", feature.PhaseResearch)
	sess.protocol = p
	sess.permHandler = &AcceptEditsHandler{}

	if handled := sess.tryHandleControlRequest(msg); !handled {
		t.Fatal("tryHandleControlRequest = false, want true so default read permissions do not reach the TUI")
	}
	if !strings.Contains(buf.String(), `"outcome":"selected"`) || !strings.Contains(buf.String(), "opt-allow") {
		t.Fatalf("read permission did not select the allow option via ACP outcome: %q", buf.String())
	}
}

// TestOpenCodeExternalDirectoryFallbackPermissionAutoApprovedByDefault proves
// OpenCode's current ACP shape for external-directory path checks
// (kind="other", title="external_directory") still normalizes into Agentico's
// read-only permission model.
func TestOpenCodeExternalDirectoryFallbackPermissionAutoApprovedByDefault(t *testing.T) {
	p, buf := newOpenCodeProtocol(t)
	msg := parseOneControl(t, p, ocPermissionLine(t, 74, "other", "external_directory", map[string]any{
		"filepath":  "/repo/AGENTS.md",
		"parentDir": "/repo",
	}))
	if msg.ControlRequest.Request.ToolName != "ExternalDirectory" {
		t.Fatalf("tool name = %q, want ExternalDirectory", msg.ControlRequest.Request.ToolName)
	}

	sess := NewSession("oc-external-dir", "feat-1", feature.PhaseResearch)
	sess.protocol = p
	sess.permHandler = &AcceptEditsHandler{}

	if handled := sess.tryHandleControlRequest(msg); !handled {
		t.Fatal("tryHandleControlRequest = false, want true so external-directory path checks do not reach the TUI")
	}
	if !strings.Contains(buf.String(), `"outcome":"selected"`) || !strings.Contains(buf.String(), "opt-allow") {
		t.Fatalf("external-directory permission did not select the allow option via ACP outcome: %q", buf.String())
	}
}

// TestOpenCodePermissionDenialResumesViaRejectOutcome proves a user denial of an
// OpenCode permission resumes the session by selecting the reject option through
// the provider protocol, rather than bypassing the adapter.
func TestOpenCodePermissionDenialResumesViaRejectOutcome(t *testing.T) {
	p, buf := newOpenCodeProtocol(t)
	msg := parseOneControl(t, p, ocPermissionLine(t, 7, "edit", "Edit main.go", map[string]any{"filePath": "/repo/main.go"}))
	if msg.ControlRequest.Request.ToolName != "Write" {
		t.Fatalf("tool name = %q, want Write", msg.ControlRequest.Request.ToolName)
	}

	mgr := NewManager(make(chan interface{}, 10))
	sess := NewSession("oc-deny", "feat-1", feature.PhaseImplement)
	sess.protocol = p
	sess.SetLastControlRequest(msg.ControlRequest)
	mgr.handleSessionMessage(sess, sess.ID(), sess.FeatureID(), sess.Phase(), msg)
	if got := sess.Status(); got != SessionWaitingPermission {
		t.Fatalf("status = %v, want SessionWaitingPermission", got)
	}

	if err := sess.RespondToControl("7", false, "user denied"); err != nil {
		t.Fatalf("RespondToControl(deny) error: %v", err)
	}
	if !strings.Contains(buf.String(), `"outcome":"selected"`) || !strings.Contains(buf.String(), "opt-reject") {
		t.Fatalf("denial did not select the reject option via ACP outcome: %q", buf.String())
	}
}

// TestOpenCodeQuestionParksAndClearsAfterAnswer proves an OpenCode structured
// question transitions the session to the help-waiting state, marks an
// unanswered question pending, and clears only after the matching answer is sent
// through the native ACP outcome.
func TestOpenCodeQuestionParksAndClearsAfterAnswer(t *testing.T) {
	p, buf := newOpenCodeProtocol(t)
	line := ocLine(t, map[string]any{
		"jsonrpc": "2.0", "id": 91, "method": "session/request_permission",
		"params": map[string]any{
			"sessionId": "ses_x",
			"toolCall":  map[string]any{"kind": "question", "title": "Which migration strategy?"},
			"options": []map[string]any{
				{"optionId": "opt-online", "name": "Online", "recommended": true, "confidence": 0.82},
				{"optionId": "opt-offline", "name": "Offline", "confidence": 0.3},
			},
		},
	})
	msg := parseOneControl(t, p, line)
	if msg.ControlRequest.Request.ToolName != "AskUserQuestion" {
		t.Fatalf("tool name = %q, want AskUserQuestion", msg.ControlRequest.Request.ToolName)
	}

	mgr := NewManager(make(chan interface{}, 10))
	sess := NewSession("oc-q", "feat-1", feature.PhaseInquire)
	sess.protocol = p
	sess.SetLastControlRequest(msg.ControlRequest)
	mgr.handleSessionMessage(sess, sess.ID(), sess.FeatureID(), sess.Phase(), msg)
	if got := sess.Status(); got != SessionWaitingHelp {
		t.Fatalf("status = %v, want SessionWaitingHelp", got)
	}
	if !sess.HasUnansweredQuestion() {
		t.Fatal("HasUnansweredQuestion() = false, want true while the question is pending")
	}

	answers := map[string]string{"Which migration strategy?": "Online (Recommended)"}
	if err := sess.RespondToAskUser("91", msg.ControlRequest.Request.Input, answers, nil); err != nil {
		t.Fatalf("RespondToAskUser error: %v", err)
	}
	if sess.HasUnansweredQuestion() {
		t.Fatal("HasUnansweredQuestion() = true after answering, want false")
	}
	if got := sess.Status(); got == SessionWaitingHelp {
		t.Fatal("status still SessionWaitingHelp after answer, want cleared")
	}
	if !strings.Contains(buf.String(), `"outcome":"selected"`) || !strings.Contains(buf.String(), "opt-online") {
		t.Fatalf("answer did not select the chosen option via ACP outcome: %q", buf.String())
	}
}

// TestOpenCodeQuestionAutoPickIntegration proves an OpenCode structured question
// integrates with the existing AskUserQuestion auto-pick: when auto-pick is
// allowed and the recommended option clears the confidence threshold, the
// session answers it automatically through the provider protocol without parking
// for the user.
func TestOpenCodeQuestionAutoPickIntegration(t *testing.T) {
	p, buf := newOpenCodeProtocol(t)
	line := ocLine(t, map[string]any{
		"jsonrpc": "2.0", "id": 95, "method": "session/request_permission",
		"params": map[string]any{
			"sessionId": "ses_x",
			"toolCall":  map[string]any{"kind": "question", "title": "Proceed with the safe default?"},
			"options": []map[string]any{
				{"optionId": "opt-yes", "name": "Yes", "description": "Use the safe default", "recommended": true, "confidence": 0.9},
				{"optionId": "opt-no", "name": "No", "description": "Do something else", "confidence": 0.1},
			},
		},
	})
	msg := parseOneControl(t, p, line)

	sess := NewSession("oc-autopick", "feat-1", feature.PhaseInquire)
	sess.protocol = p
	var picked []string
	sess.SetAskUserAutoPickConfig(&ports.AskUserAutoPickConfig{
		Purpose:         ports.AskUserAutoPickPurposeInquire,
		LoadInquireness: func() (feature.Inquireness, error) { return feature.InquirenessNone, nil },
		OnQuestionAutoPicked: func(question, answer string, confidence float64) {
			picked = append(picked, question+"="+answer)
		},
	})

	if handled := sess.tryHandleControlRequest(msg); !handled {
		t.Fatal("tryHandleControlRequest = false, want true (auto-pick should answer the high-confidence question)")
	}
	if len(picked) != 1 {
		t.Fatalf("auto-pick callbacks = %v, want one", picked)
	}
	if !strings.Contains(buf.String(), `"outcome":"selected"`) || !strings.Contains(buf.String(), "opt-yes") {
		t.Fatalf("auto-pick did not select the recommended option via ACP outcome: %q", buf.String())
	}
}
