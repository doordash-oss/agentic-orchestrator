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

package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestOpenCodeAttachThinkingChunkRendersGenericThinking(t *testing.T) {
	p := opencode.NewProtocol(llm.ProtocolOpts{Model: "opencode:anthropic/claude-sonnet-4-5"})
	line := mustJSONLine(t, map[string]any{
		"jsonrpc": "2.0", "method": "session/update",
		"params": map[string]any{
			"sessionId": "ses_x",
			"update": map[string]any{
				"sessionUpdate": "agent_thought_chunk",
				"content":       map[string]any{"type": "text", "text": "for"},
			},
		},
	})
	msgs, err := p.ParseLine(line)
	if err != nil || len(msgs) != 1 || msgs[0].Assistant == nil {
		t.Fatalf("thought ParseLine = %+v, err %v; want one assistant message", msgs, err)
	}

	sess := session.NewSession("oc-thinking", "feat-1", 0)
	m := testAttachModel(sess, 80, 24, nil, 0)
	m.spinnerView = "spin"

	updated, _ := m.Update(attachMsgsMsg{generation: m.tabGeneration, messages: msgs})
	if updated.thinkingLine != "Thinking..." {
		t.Fatalf("thinkingLine = %q, want generic Thinking... label", updated.thinkingLine)
	}
	if line := stripANSI(updated.renderSpinnerLine()); strings.Contains(line, "for") {
		t.Fatalf("spinner line leaked raw OpenCode thought token: %q", line)
	}
}

func TestOpenCodeAttachBuildsFileEventsFromToolMetadata(t *testing.T) {
	p := opencode.NewProtocol(llm.ProtocolOpts{Model: "opencode:anthropic/claude-sonnet-4-5"})

	editMsgs, err := p.ParseLine(mustJSONLine(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": "ses_x",
			"update": map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    "call_edit",
				"kind":          "edit",
				"title":         "write",
				"status":        "completed",
				"locations":     []map[string]any{{"path": "/repo/README.md"}},
				"content":       []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "wrote file\n"}}},
				"rawInput": map[string]any{
					"filePath": "/repo/README.md",
					"oldText":  "old",
					"newText":  "new",
				},
			},
		},
	}))
	if err != nil {
		t.Fatalf("ParseLine edit: %v", err)
	}
	editEvents := buildAttachFileEvents(editMsgs, 0)
	if len(editEvents) != 1 {
		t.Fatalf("edit file events = %+v, want one", editEvents)
	}
	if got := editEvents[0].change.Path; got != "/repo/README.md" {
		t.Fatalf("edit event path = %q, want /repo/README.md", got)
	}

	bashMsgs, err := p.ParseLine(mustJSONLine(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": "ses_x",
			"update": map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    "call_bash",
				"kind":          "execute",
				"title":         "cat > /repo/README.it.md <<'EOF'\nciao\nEOF",
				"status":        "completed",
				"content":       []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "done\n"}}},
				"rawInput": map[string]any{
					"command": "cat > /repo/README.it.md <<'EOF'\nciao\nEOF",
				},
			},
		},
	}))
	if err != nil {
		t.Fatalf("ParseLine bash: %v", err)
	}
	bashEvents := buildAttachFileEvents(bashMsgs, len(editMsgs))
	if len(bashEvents) != 1 {
		t.Fatalf("bash file events = %+v, want one", bashEvents)
	}
	if got := bashEvents[0].change.Path; got != "/repo/README.it.md" {
		t.Fatalf("bash event path = %q, want /repo/README.it.md", got)
	}
}

// TestOpenCodeAttachRenderArtifacts proves an OpenCode permission request and an
// OpenCode question request render through the existing attach-view controls
// (the same renderPermMenu / renderQuestion paths every provider uses), and
// captures the rendered text as visual evidence. The control requests are
// produced by a real opencode.Protocol from fake ACP lines, so the rendered
// prompts reflect the actual Phase 2 normalization.
//
// When AGENTIC_OPENCODE_SCREENSHOT_DIR is set, the ANSI-stripped renders are
// written there as .txt artifacts; the content assertions run unconditionally.
func TestOpenCodeAttachRenderArtifacts(t *testing.T) {
	p := opencode.NewProtocol(llm.ProtocolOpts{Model: "opencode:anthropic/claude-sonnet-4-5"})

	// --- permission prompt (shell execution) ---
	permLine := mustJSONLine(t, map[string]any{
		"jsonrpc": "2.0", "id": 4242, "method": "session/request_permission",
		"params": map[string]any{
			"sessionId": "ses_x",
			"toolCall":  map[string]any{"kind": "execute", "title": "Run the test suite", "rawInput": map[string]any{"command": "go test ./... -count=1"}},
			"options": []map[string]any{
				{"optionId": "opt-allow", "name": "Allow", "kind": "allow_once"},
				{"optionId": "opt-reject", "name": "Reject", "kind": "reject_once"},
			},
		},
	})
	permMsgs, err := p.ParseLine(permLine)
	if err != nil || len(permMsgs) != 1 || permMsgs[0].ControlRequest == nil {
		t.Fatalf("permission ParseLine = %+v, err %v; want one control_request", permMsgs, err)
	}

	permModel := AttachModel{width: 100, inputHeight: 1}
	permModel.activatePermissionRequest(permMsgs[0].ControlRequest)
	permRender := stripANSI(permModel.renderPermMenu())
	for _, want := range []string{"Allow Bash?", "go test ./... -count=1", "[y] Allow", "[n] Deny"} {
		if !strings.Contains(permRender, want) {
			t.Fatalf("permission render missing %q:\n%s", want, permRender)
		}
	}

	// --- question prompt (structured multiple choice) ---
	qLine := mustJSONLine(t, map[string]any{
		"jsonrpc": "2.0", "id": 91, "method": "session/request_permission",
		"params": map[string]any{
			"sessionId": "ses_x",
			"toolCall":  map[string]any{"kind": "question", "title": "Which migration strategy should I use?"},
			"options": []map[string]any{
				{"optionId": "o1", "name": "Online", "description": "No downtime; more complex.", "recommended": true, "confidence": 0.82},
				{"optionId": "o2", "name": "Offline", "description": "Simpler; needs a maintenance window.", "confidence": 0.40},
			},
		},
	})
	qMsgs, err := p.ParseLine(qLine)
	if err != nil || len(qMsgs) != 1 || qMsgs[0].ControlRequest == nil {
		t.Fatalf("question ParseLine = %+v, err %v; want one control_request", qMsgs, err)
	}

	qModel := AttachModel{width: 100, inputHeight: 1}
	questions := qModel.parseAskUserQuestionsForDisplay(qMsgs[0].ControlRequest.Request.Input)
	if len(questions) != 1 {
		t.Fatalf("parsed questions = %+v, want one", questions)
	}
	qModel.pendingQuestions = questions
	qModel.questionStates = make([]questionUIState, len(questions))
	qRender := stripANSI(qModel.renderQuestion())
	for _, want := range []string{"Which migration strategy should I use?", "Online (Recommended)", "No downtime", "Confidence: 0.82", "Offline"} {
		if !strings.Contains(qRender, want) {
			t.Fatalf("question render missing %q:\n%s", want, qRender)
		}
	}

	if dir := os.Getenv("AGENTIC_OPENCODE_SCREENSHOT_DIR"); dir != "" {
		writeArtifact(t, dir, "opencode-permission-prompt.txt",
			"OpenCode permission prompt (shell execution) — rendered via attach-view renderPermMenu\n"+
				"Source: ACP session/request_permission {kind:execute} normalized to tool \"Bash\".\n\n"+permRender+"\n")
		writeArtifact(t, dir, "opencode-question-prompt.txt",
			"OpenCode question prompt (structured multiple choice) — rendered via attach-view renderQuestion\n"+
				"Source: ACP session/request_permission {kind:question} normalized to AskUserQuestion.\n\n"+qRender+"\n")
	}
}

func mustJSONLine(t *testing.T, v map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal ACP line: %v", err)
	}
	return b
}

func writeArtifact(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
