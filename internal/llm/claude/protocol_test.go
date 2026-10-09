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

package claude

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/askuser"
)

// Compile-time check that *Protocol satisfies llm.Protocol.
var _ llm.Protocol = (*Protocol)(nil)

func TestClaudeProtocol_SessionID(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test"})

	// Before any init message, SessionID should be empty.
	if got := p.SessionID(); got != "" {
		t.Errorf("SessionID() before init = %q, want empty", got)
	}

	// Parse an init message with a session ID.
	initJSON := []byte(`{"type":"system","subtype":"init","session_id":"test-sess-123","model":"test"}`)
	if _, err := p.ParseLine(initJSON); err != nil {
		t.Fatalf("ParseLine: %v", err)
	}

	if got := p.SessionID(); got != "test-sess-123" {
		t.Errorf("SessionID() after init = %q, want %q", got, "test-sess-123")
	}
}

func TestClaudeProtocol_TranscriptPath(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/Users/test/project"})

	// Before init, TranscriptPath should be empty.
	if got := p.TranscriptPath(); got != "" {
		t.Errorf("TranscriptPath() before init = %q, want empty", got)
	}

	// Parse an init message.
	initJSON := []byte(`{"type":"system","subtype":"init","session_id":"sess-abc","model":"test"}`)
	if _, err := p.ParseLine(initJSON); err != nil {
		t.Fatalf("ParseLine: %v", err)
	}

	got := p.TranscriptPath()
	// The path should contain the encoded work dir and session ID.
	if got == "" {
		t.Fatal("TranscriptPath() after init is empty, want non-empty")
	}

	// Verify the path ends with the expected session JSONL filename.
	wantSuffix := "/sess-abc.jsonl"
	if !containsSuffix(got, wantSuffix) {
		t.Errorf("TranscriptPath() = %q, want suffix %q", got, wantSuffix)
	}

	// Verify it contains the encoded project dir name.
	wantDir := "-Users-test-project"
	if !containsSubstring(got, wantDir) {
		t.Errorf("TranscriptPath() = %q, want to contain %q", got, wantDir)
	}
}

func TestClaudeProtocol_TranscriptPath_EmptyWorkDir(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: ""})

	initJSON := []byte(`{"type":"system","subtype":"init","session_id":"sess-abc","model":"test"}`)
	if _, err := p.ParseLine(initJSON); err != nil {
		t.Fatalf("ParseLine: %v", err)
	}

	if got := p.TranscriptPath(); got != "" {
		t.Errorf("TranscriptPath() with empty WorkDir = %q, want empty", got)
	}
}

func TestClaudeProtocol_InjectsContextWindowIntoAssistantUsage(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{Model: "opus", ContextWindow: 200_000})

	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1000,"output_tokens":10}}}`)
	msgs, err := p.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Assistant == nil || msgs[0].Assistant.Message.Usage == nil {
		t.Fatalf("expected one assistant message with usage, got %+v", msgs)
	}
	if got := msgs[0].Assistant.Message.Usage.ContextWindow; got != 200_000 {
		t.Errorf("assistant usage contextWindow = %d, want %d", got, 200_000)
	}
}

func TestClaudeProtocol_UpdatesContextWindowFromResultModelUsage(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{Model: "opus"})

	resultLine := []byte(`{"type":"result","subtype":"success","session_id":"s1","modelUsage":{"opus":{"contextWindow":128000}}}`)
	if _, err := p.ParseLine(resultLine); err != nil {
		t.Fatalf("ParseLine(result): %v", err)
	}

	assistantLine := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1000,"output_tokens":10}}}`)
	msgs, err := p.ParseLine(assistantLine)
	if err != nil {
		t.Fatalf("ParseLine(assistant): %v", err)
	}
	if len(msgs) != 1 || msgs[0].Assistant == nil || msgs[0].Assistant.Message.Usage == nil {
		t.Fatalf("expected one assistant message with usage, got %+v", msgs)
	}
	if got := msgs[0].Assistant.Message.Usage.ContextWindow; got != 128_000 {
		t.Errorf("assistant usage contextWindow = %d, want %d", got, 128_000)
	}
}

func TestClaudeProtocol_AssignsRootAndTaskOrigins(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{})

	root, err := p.ParseLine([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`))
	if err != nil {
		t.Fatalf("ParseLine(root): %v", err)
	}
	if len(root) != 1 || !root[0].Origin.IsRoot() {
		t.Fatalf("root origin = %+v", root)
	}

	task, err := p.ParseLine([]byte(`{"type":"system","subtype":"task_progress","task_id":"task-1","session_id":"child-1","description":"checking tests","last_tool_name":"Bash"}`))
	if err != nil {
		t.Fatalf("ParseLine(task): %v", err)
	}
	if len(task) != 1 ||
		task[0].Origin.Kind != llm.EventOriginTask ||
		task[0].Origin.TaskID != "task-1" ||
		task[0].Origin.ChildSessionID != "child-1" {
		t.Fatalf("task origin = %+v", task)
	}
}

// TestClaudeProtocol_SubagentControlRequestOrigin pins that an interactive
// session tags a can_use_tool request carrying agent_id as the sub-agent's,
// while an orchestrated phase keeps it on the root agent.
func TestClaudeProtocol_SubagentControlRequestOrigin(t *testing.T) {
	line := []byte(`{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"toolu_1","agent_id":"agent-7"}}`)
	rootLine := []byte(`{"type":"control_request","request_id":"req_2","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"}}}`)

	interactive := NewProtocol(llm.ProtocolOpts{Interactive: true})
	msgs, err := interactive.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine(sub-agent): %v", err)
	}
	if len(msgs) != 1 || msgs[0].ControlRequest == nil {
		t.Fatalf("messages = %+v", msgs)
	}
	want := llm.EventOrigin{Kind: llm.EventOriginTask, TaskID: "agent-7", ChildSessionID: "agent-7"}
	if msgs[0].Origin != want || msgs[0].ControlRequest.Origin != want {
		t.Fatalf("origin = %+v / %+v, want %+v", msgs[0].Origin, msgs[0].ControlRequest.Origin, want)
	}
	msgs, err = interactive.ParseLine(rootLine)
	if err != nil {
		t.Fatalf("ParseLine(root): %v", err)
	}
	if !msgs[0].Origin.IsRoot() || !msgs[0].ControlRequest.Origin.IsRoot() {
		t.Fatalf("root request origin = %+v", msgs[0].Origin)
	}

	phase := NewProtocol(llm.ProtocolOpts{})
	msgs, err = phase.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine(phase): %v", err)
	}
	if !msgs[0].Origin.IsRoot() {
		t.Fatalf("phase sub-agent request origin = %+v, want root", msgs[0].Origin)
	}
}

func TestClaudeProtocol_Interrupt_WritesControlRequest(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{})
	var buf bytes.Buffer
	p.SetStdin(&buf)

	if err := p.Interrupt(); err != nil {
		t.Fatalf("Interrupt(): %v", err)
	}

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("Interrupt() wrote nothing to stdin")
	}

	var msg struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("unmarshal interrupt JSON: %v (line=%q)", err, line)
	}
	if msg.Type != "control_request" {
		t.Errorf("type = %q, want control_request", msg.Type)
	}
	if msg.Request.Subtype != "interrupt" {
		t.Errorf("request.subtype = %q, want interrupt", msg.Request.Subtype)
	}
	if !strings.HasPrefix(msg.RequestID, "agentic-interrupt-") {
		t.Errorf("request_id = %q, want prefix agentic-interrupt-", msg.RequestID)
	}
}

func TestClaudeProtocol_Interrupt_WithoutStdin_ReturnsError(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{})
	// No SetStdin call — writer is nil.
	if err := p.Interrupt(); err == nil {
		t.Fatal("Interrupt() with nil stdin should return error, got nil")
	}
}

const askUserQuestionsInput = `{"questions":[{"question":"Which approach?","header":"Scope","multiSelect":false,"options":[{"label":"Option A (Recommended)","description":"a","confidence":0.8},{"label":"Option B","description":"b","confidence":0.2}]}]}`

type askUserControlResponse struct {
	Type     string `json:"type"`
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Response  struct {
			Behavior     string `json:"behavior"`
			UpdatedInput struct {
				RawQuestions json.RawMessage    `json:"questions"`
				Questions    []askuser.Question `json:"-"`
				Answers      map[string]string  `json:"answers"`
			} `json:"updatedInput"`
		} `json:"response"`
	} `json:"response"`
}

func respondToAskUser(t *testing.T, answers map[string]string) askUserControlResponse {
	return respondToAskUserInput(t, json.RawMessage(askUserQuestionsInput), answers)
}

func respondToAskUserInput(t *testing.T, questions json.RawMessage, answers map[string]string) askUserControlResponse {
	t.Helper()
	bundle, err := askuser.Parse(questions)
	if err != nil {
		t.Fatalf("parse questions: %v", err)
	}
	resolved, err := bundle.Resolve(answers)
	if err != nil {
		t.Fatalf("resolve answers: %v", err)
	}
	p := NewProtocol(llm.ProtocolOpts{})
	var buf bytes.Buffer
	p.SetStdin(&buf)
	if err := p.RespondToAskUser("req-1", resolved); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}
	var out askUserControlResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &out); err != nil {
		t.Fatalf("unmarshal control response: %v (raw=%q)", err, buf.String())
	}
	updated := &out.Response.Response.UpdatedInput
	envelope := append(append([]byte(`{"questions":`), updated.RawQuestions...), '}')
	sent, err := askuser.Parse(envelope)
	if err != nil {
		t.Fatalf("parse updatedInput.questions: %v (raw=%s)", err, updated.RawQuestions)
	}
	updated.Questions = sent.Questions
	if out.Response.Response.Behavior != "allow" {
		t.Fatalf("behavior = %q, want allow", out.Response.Response.Behavior)
	}
	return out
}

func TestClaudeProtocol_RespondToAskUser_SelectedLabelPassesThroughVerbatim(t *testing.T) {
	for _, raw := range []string{"Option A (Recommended)", "  Option A (Recommended) "} {
		out := respondToAskUser(t, map[string]string{"1": raw})
		updated := out.Response.Response.UpdatedInput
		if got := updated.Answers["Which approach?"]; got != "Option A (Recommended)" {
			t.Errorf("answer for %q = %q, want the label verbatim", raw, got)
		}
		if got := len(updated.Questions[0].Options); got != 2 {
			t.Errorf("options len = %d, want 2 (no injection for a selected answer)", got)
		}
		if got := string(out.Response.Response.UpdatedInput.RawQuestions); !strings.Contains(got, `"confidence":0.8`) {
			t.Errorf("questions = %s, want the original envelope re-encoded", got)
		}
	}
}

func TestClaudeProtocol_RespondToAskUser_MultiSelectLabelsPassThrough(t *testing.T) {
	questions := json.RawMessage(`{"questions":[{"question":"Which areas?","header":"Areas","multiSelect":true,"options":[{"label":"API","description":"a"},{"label":"UI","description":"b"},{"label":"Docs","description":"c"}]}]}`)
	out := respondToAskUserInput(t, questions, map[string]string{"1": "API, Docs"})
	updated := out.Response.Response.UpdatedInput
	if got := updated.Answers["Which areas?"]; got != "API, Docs" {
		t.Errorf("answer = %q, want the selected labels", got)
	}
	if got := len(updated.Questions[0].Options); got != 3 {
		t.Errorf("options len = %d, want 3 (no injection for selected labels)", got)
	}
}

func TestClaudeProtocol_RespondToAskUser_FreeTextInjectedAsOption(t *testing.T) {
	// "Option A" is not a label: only a verbatim label selects an option.
	for _, customAnswer := range []string{"use a third custom approach", "Option A"} {
		out := respondToAskUser(t, map[string]string{"1": customAnswer})
		updated := out.Response.Response.UpdatedInput
		if got := updated.Answers["Which approach?"]; got != customAnswer {
			t.Errorf("answer = %q, want the free text verbatim", got)
		}
		opts := updated.Questions[0].Options
		if len(opts) != 3 {
			t.Fatalf("options len = %d, want 3", len(opts))
		}
		if opts[2].Label != customAnswer || opts[2].Description != "User-provided custom answer." {
			t.Errorf("injected option = %+v, want schema-valid custom option", opts[2])
		}
	}
}

func TestClaudeProtocol_RespondToAskUser_FreeTextForOptionlessQuestionIsPadded(t *testing.T) {
	questions := json.RawMessage(`{"questions":[{"question":"What version?","header":"Version","multiSelect":false,"options":[]}]}`)
	for _, tc := range []struct{ answer, padding string }{
		{answer: "1.2.3", padding: "Other"},
		{answer: "Other", padding: "Alternative answer"},
	} {
		out := respondToAskUserInput(t, questions, map[string]string{"1": tc.answer})
		updated := out.Response.Response.UpdatedInput
		if got := updated.Answers["What version?"]; got != tc.answer {
			t.Errorf("answer = %q, want %q", got, tc.answer)
		}
		opts := updated.Questions[0].Options
		if len(opts) != 2 {
			t.Fatalf("options = %+v, want two schema-valid options", opts)
		}
		if opts[0].Label != tc.padding || opts[0].Description != "Provide a different custom answer." {
			t.Errorf("padding option = %+v, want %q placeholder", opts[0], tc.padding)
		}
		if opts[1].Label != tc.answer || opts[1].Description != "User-provided custom answer." {
			t.Errorf("custom option = %+v, want schema-valid free-text selection", opts[1])
		}
	}
}

func TestClaudeProtocol_RespondToAskUser_FreeTextForFourOptionQuestionKeepsFirstThree(t *testing.T) {
	questions := json.RawMessage(`{"questions":[{"question":"Which approach?","header":"Scope","multiSelect":false,"options":[{"label":"A","description":"first"},{"label":"B","description":"second"},{"label":"C","description":"third"},{"label":"D","description":"fourth"}]}]}`)
	const customAnswer = "Use a custom fifth approach"
	out := respondToAskUserInput(t, questions, map[string]string{"1": customAnswer})
	updated := out.Response.Response.UpdatedInput
	if got := updated.Answers["Which approach?"]; got != customAnswer {
		t.Errorf("answer = %q, want the free text verbatim", got)
	}
	opts := updated.Questions[0].Options
	if len(opts) != 4 {
		t.Fatalf("options = %+v, want four schema-valid options", opts)
	}
	for i, want := range []string{"A", "B", "C"} {
		if opts[i].Label != want {
			t.Errorf("option %d label = %q, want preserved %q", i, opts[i].Label, want)
		}
	}
	if opts[3].Label != customAnswer || opts[3].Description != "User-provided custom answer." {
		t.Errorf("final option = %+v, want schema-valid custom answer replacing D", opts[3])
	}
}

func containsSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
