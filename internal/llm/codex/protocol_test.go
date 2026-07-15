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

package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// Compile-time check that *Protocol satisfies llm.Protocol.
var _ llm.Protocol = (*Protocol)(nil)

func TestCodexProtocol_SessionIDAndTranscriptPath(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test"})

	if got := p.SessionID(); got != "" {
		t.Errorf("SessionID() = %q, want empty", got)
	}
	if got := p.TranscriptPath(); got != "" {
		t.Errorf("TranscriptPath() = %q, want empty", got)
	}
}

func TestCodexProtocol_Interrupt_ReturnsNotSupported(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test"})
	err := p.Interrupt()
	if !errors.Is(err, llm.ErrNotSupported) {
		t.Errorf("Interrupt() = %v, want llm.ErrNotSupported", err)
	}
}

func TestCodexProtocol_TokenUsageUpdated(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})

	ctxWindow := 200000
	notification := map[string]interface{}{
		"method": "thread/tokenUsage/updated",
		"params": map[string]interface{}{
			"threadId": "t1",
			"turnId":   "turn1",
			"tokenUsage": map[string]interface{}{
				"total": map[string]interface{}{
					"inputTokens":       150000,
					"cachedInputTokens": 20000,
					"outputTokens":      5000,
					"totalTokens":       155000,
				},
				"last": map[string]interface{}{
					"inputTokens":       80000,
					"cachedInputTokens": 10000,
					"outputTokens":      2000,
					"totalTokens":       82000,
				},
				"modelContextWindow": ctxWindow,
			},
		},
	}
	line, _ := json.Marshal(notification)
	msgs, err := p.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}

	msg := msgs[0]
	if msg.Type != "usage_update" {
		t.Fatalf("type=%q, want usage_update", msg.Type)
	}
	if msg.UsageUpdate == nil {
		t.Fatal("expected UsageUpdate on message")
	}

	u := msg.UsageUpdate
	// UsageUpdate carries Total (cumulative) tokens for cost tracking
	if u.InputTokens != 150000 {
		t.Errorf("InputTokens = %d, want 150000 (Total.InputTokens)", u.InputTokens)
	}
	if u.OutputTokens != 5000 {
		t.Errorf("OutputTokens = %d, want 5000 (Total.OutputTokens)", u.OutputTokens)
	}
	if u.CacheReadInputTokens != 20000 {
		t.Errorf("CacheReadInputTokens = %d, want 20000 (Total.CachedInputTokens)", u.CacheReadInputTokens)
	}
	if u.CacheCreationInputTokens != 0 {
		t.Errorf("CacheCreationInputTokens = %d, want 0", u.CacheCreationInputTokens)
	}
	// ContextInputTokens carries Last.InputTokens (informational)
	if u.ContextInputTokens != 80000 {
		t.Errorf("ContextInputTokens = %d, want 80000 (Last.InputTokens)", u.ContextInputTokens)
	}
	// ContextTotalTokens carries Last.TotalTokens (used by ContextPercentage)
	if u.ContextTotalTokens != 82000 {
		t.Errorf("ContextTotalTokens = %d, want 82000 (Last.TotalTokens)", u.ContextTotalTokens)
	}
	// ContextBaseline mirrors Codex's TokenUsage::BASELINE_TOKENS = 12000
	if u.ContextBaseline != codexContextBaselineTokens {
		t.Errorf("ContextBaseline = %d, want %d", u.ContextBaseline, codexContextBaselineTokens)
	}
	if u.ContextWindow != ctxWindow {
		t.Errorf("ContextWindow = %d, want %d", u.ContextWindow, ctxWindow)
	}

	// Verify internal state uses Total for cost tracking
	p.mu.Lock()
	if p.inputTokens != 150000 {
		t.Errorf("p.inputTokens = %d, want 150000 (Total.InputTokens)", p.inputTokens)
	}
	if p.outputTokens != 5000 {
		t.Errorf("p.outputTokens = %d, want 5000 (Total.OutputTokens)", p.outputTokens)
	}
	if p.cachedInputTokens != 20000 {
		t.Errorf("p.cachedInputTokens = %d, want 20000 (Total.CachedInputTokens)", p.cachedInputTokens)
	}
	if p.modelContextWindow != ctxWindow {
		t.Errorf("p.modelContextWindow = %d, want %d", p.modelContextWindow, ctxWindow)
	}
	p.mu.Unlock()
}

func TestCodexCommandExecutionCompletedIncludesStructuredFileReads(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	exitCode := 0
	params, err := json.Marshal(ItemCompletedParams{
		ThreadID: "thread-1",
		TurnID:   "turn-1",
		Item: ItemUnion{
			ID:               "call_read",
			Type:             "commandExecution",
			AggregatedOutput: "file contents",
			ExitCode:         &exitCode,
			CommandActions: []CommandAction{
				{Type: "read", Path: "/tmp/state/guidelines/go/index.md"},
				{Type: codexFileChangeOperationWrite, Path: "/tmp/state/output.txt"},
				{Type: "read", Path: ""},
			},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	msg, ok := p.parseNotification("item/completed", params)
	if !ok {
		t.Fatal("parseNotification returned false, want true")
	}
	if msg.ToolProgress == nil {
		t.Fatal("msg.ToolProgress is nil, want non-nil")
	}
	if msg.ToolProgress.ToolUseID != "call_read" {
		t.Fatalf("ToolUseID = %q, want %q", msg.ToolProgress.ToolUseID, "call_read")
	}
	if len(msg.FileReads) != 1 {
		t.Fatalf("len(FileReads) = %d, want 1", len(msg.FileReads))
	}
	read := msg.FileReads[0]
	if read.FilePath != "/tmp/state/guidelines/go/index.md" {
		t.Errorf("FilePath = %q", read.FilePath)
	}
	if read.Source != "codex.command_action" {
		t.Errorf("Source = %q", read.Source)
	}
	if read.ProviderItemID != "call_read" {
		t.Errorf("ProviderItemID = %q", read.ProviderItemID)
	}
	if read.ExitCode == nil || *read.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", read.ExitCode)
	}

	dup, ok := p.parseNotification("item/completed", params)
	if !ok {
		t.Fatal("duplicate parseNotification returned false, want true")
	}
	if len(dup.FileReads) != 0 {
		t.Fatalf("duplicate len(FileReads) = %d, want 0", len(dup.FileReads))
	}
}

func TestCodexFileChangeCompletedIncludesStructuredDiff(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})

	params := json.RawMessage(`{
		"threadId": "thread-1",
		"turnId": "turn-1",
		"item": {
			"id": "call_write",
			"type": "fileChange",
			"status": "completed",
			"changes": [{
				"path": "/tmp/test/README.md",
				"kind": {"type": "update", "move_path": null},
				"diff": "@@ -1,2 +1,2 @@\n-old\n+new\n"
			}]
		}
	}`)

	msg, ok := p.parseNotification("item/completed", params)
	if !ok {
		t.Fatal("parseNotification(item/completed fileChange) returned false, want true")
	}
	if msg.ToolProgress == nil {
		t.Fatal("msg.ToolProgress is nil, want non-nil")
	}
	if msg.ToolProgress.ToolUseID != "call_write" || msg.ToolProgress.ToolName != codexToolNameWrite {
		t.Fatalf("ToolProgress = %+v, want call_write Write", msg.ToolProgress)
	}
	if len(msg.FileChanges) != 1 {
		t.Fatalf("len(FileChanges) = %d, want 1", len(msg.FileChanges))
	}
	change := msg.FileChanges[0]
	if change.Path != "/tmp/test/README.md" {
		t.Fatalf("FileChanges[0].Path = %q", change.Path)
	}
	if change.Operation != codexFileChangeOperationUpdate {
		t.Fatalf("FileChanges[0].Operation = %q, want update", change.Operation)
	}
	if !change.HasDiffPatch {
		t.Fatal("FileChanges[0].HasDiffPatch = false, want true")
	}
	if change.AddedLines != 1 || change.RemovedLines != 1 {
		t.Fatalf("line counts = +%d -%d, want +1 -1", change.AddedLines, change.RemovedLines)
	}
	if !strings.Contains(change.Detail, "-old") || !strings.Contains(change.Detail, "+new") {
		t.Fatalf("FileChanges[0].Detail missing diff lines: %q", change.Detail)
	}
}

func TestCodexToolProgressIncludesProviderItemID(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})

	startedParams, err := json.Marshal(ItemStartedParams{
		ThreadID: "thread-1",
		TurnID:   "turn-1",
		Item: ItemUnion{
			ID:   "call_1",
			Type: "commandExecution",
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal started: %v", err)
	}
	started, ok := p.parseNotification("item/started", startedParams)
	if !ok {
		t.Fatal("parseNotification(item/started) returned false, want true")
	}
	if started.ToolProgress == nil {
		t.Fatal("started.ToolProgress is nil, want non-nil")
	}
	if started.ToolProgress.ToolUseID != "call_1" || started.ToolProgress.ToolName != "Bash" {
		t.Fatalf("started ToolProgress = %+v, want ToolUseID call_1 and Bash", started.ToolProgress)
	}

	deltaParams, err := json.Marshal(CommandOutputDelta{
		ThreadID: "thread-1",
		TurnID:   "turn-1",
		ItemID:   "call_1",
		Delta:    "PASS",
	})
	if err != nil {
		t.Fatalf("json.Marshal delta: %v", err)
	}
	delta, ok := p.parseNotification("item/commandExecution/outputDelta", deltaParams)
	if !ok {
		t.Fatal("parseNotification(outputDelta) returned false, want true")
	}
	if delta.ToolProgress == nil {
		t.Fatal("delta.ToolProgress is nil, want non-nil")
	}
	if delta.ToolProgress.ToolUseID != "call_1" || delta.ToolProgress.ToolName != "Bash" {
		t.Fatalf("delta ToolProgress = %+v, want ToolUseID call_1 and Bash", delta.ToolProgress)
	}
}

func TestCodexProtocolStartTurn_DeveloperInstructionsEncoding(t *testing.T) {
	tests := []struct {
		name         string
		systemPrompt string
		wantField    bool
	}{
		{
			name:      "omits empty developer instructions",
			wantField: false,
		},
		{
			name:         "includes non-empty developer instructions",
			systemPrompt: "Follow the system prompt",
			wantField:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			p := NewProtocol(llm.ProtocolOpts{
				Model:        "gpt-5.4",
				WorkDir:      "/tmp/test",
				SystemPrompt: tt.systemPrompt,
			})
			p.SetStdin(&buf)
			p.SetThreadIDForTest("thread-123")

			if err := p.startTurn("review this plan"); err != nil {
				t.Fatalf("startTurn() error: %v", err)
			}

			got := buf.String()
			hasField := strings.Contains(got, `"developer_instructions"`)
			if hasField != tt.wantField {
				t.Fatalf("developer_instructions presence = %v, want %v; payload=%s", hasField, tt.wantField, got)
			}
		})
	}
}

func TestCodexProtocol_StripsContextWindowFromModel(t *testing.T) {
	var buf bytes.Buffer

	p := NewProtocol(llm.ProtocolOpts{
		Model:   "gpt-5.4[1M]",
		WorkDir: "/tmp/test",
	})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-123")

	if err := p.startTurn("do something"); err != nil {
		t.Fatalf("startTurn() error: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, `"model":"gpt-5.4"`) {
		t.Fatalf("startTurn payload = %s, want base model gpt-5.4", got)
	}
	if strings.Contains(got, "gpt-5.4[1M]") {
		t.Fatalf("startTurn payload leaked context-window model ID: %s", got)
	}
}

func TestTokenUsageUpdatedSurfacesAsSDKMessage(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test"})
	p.SetThreadIDForTest("thread-abc")

	// Build the JSON-RPC notification params matching TokenUsageUpdatedParams.
	payload := TokenUsageUpdatedParams{
		ThreadID: "thread-abc",
		TurnID:   "turn-1",
		TokenUsage: ThreadTokenUsage{
			Total: TokenUsageBreakdown{
				InputTokens:  1500,
				OutputTokens: 750,
			},
		},
	}
	params, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	msg, ok := p.parseNotification("thread/tokenUsage/updated", params)
	if !ok {
		t.Fatal("parseNotification returned false, want true")
	}
	if msg.Type != "usage_update" {
		t.Errorf("msg.Type = %q, want %q", msg.Type, "usage_update")
	}
	if msg.UsageUpdate == nil {
		t.Fatal("msg.UsageUpdate is nil, want non-nil")
	}
	if msg.UsageUpdate.InputTokens != 1500 {
		t.Errorf("UsageUpdate.InputTokens = %d, want 1500", msg.UsageUpdate.InputTokens)
	}
	if msg.UsageUpdate.OutputTokens != 750 {
		t.Errorf("UsageUpdate.OutputTokens = %d, want 750", msg.UsageUpdate.OutputTokens)
	}

	// Verify the protocol's internal cumulative counters were updated.
	p.mu.Lock()
	gotIn := p.inputTokens
	gotOut := p.outputTokens
	p.mu.Unlock()
	if gotIn != 1500 {
		t.Errorf("protocol.inputTokens = %d, want 1500", gotIn)
	}
	if gotOut != 750 {
		t.Errorf("protocol.outputTokens = %d, want 750", gotOut)
	}
}

func TestCodexCommandApproval_NormalizesToBash(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test"})

	params, err := json.Marshal(CommandApprovalParams{Command: "ls -la"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	msg, ok := p.parseServerRequest("item/commandExecution/requestApproval", 42, params)
	if !ok {
		t.Fatal("parseServerRequest() ok = false, want true")
	}
	if msg.Type != "control_request" {
		t.Fatalf("msg.Type = %q, want %q", msg.Type, "control_request")
	}
	if msg.ControlRequest == nil {
		t.Fatal("msg.ControlRequest = nil, want non-nil")
	}
	if msg.ControlRequest.Request.ToolName != "Bash" {
		t.Fatalf("ToolName = %q, want %q", msg.ControlRequest.Request.ToolName, "Bash")
	}

	var payload map[string]string
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &payload); err != nil {
		t.Fatalf("json.Unmarshal(input): %v", err)
	}
	if payload["command"] != "ls -la" {
		t.Errorf("payload[command] = %q, want %q", payload["command"], "ls -la")
	}
}

func TestCodexProtocol_DefaultApprovalPolicyIsOnRequest(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	if p.approvalPolicy != "on-request" {
		t.Errorf("default approvalPolicy = %q, want %q", p.approvalPolicy, "on-request")
	}
}

func TestCodexProtocol_DSPApprovalPolicyIsNever(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex", DSP: true})
	if p.approvalPolicy != "never" {
		t.Errorf("DSP approvalPolicy = %q, want %q", p.approvalPolicy, "never")
	}
}

func TestCodexProtocol_StartTurnNetworkAccess(t *testing.T) {
	var buf bytes.Buffer

	p := NewProtocol(llm.ProtocolOpts{
		Model:         "gpt-5.4",
		WorkDir:       "/tmp/test",
		WritableRoots: []string{"/tmp/state"},
	})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-123")

	if err := p.startTurn("do something"); err != nil {
		t.Fatalf("startTurn() error: %v", err)
	}

	got := buf.String()
	// workspaceWrite sandbox should include networkAccess: true
	if !strings.Contains(got, `"networkAccess":true`) {
		t.Fatalf("startTurn payload missing networkAccess:true; payload=%s", got)
	}
	if !strings.Contains(got, `"type":"workspaceWrite"`) {
		t.Fatalf("startTurn payload missing workspaceWrite type; payload=%s", got)
	}
}

func TestCodexProtocol_ErrorResponseSurfacesAsResultError(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex", DSP: true})

	// A JSON-RPC error response to one of our requests (id, no method). Codex
	// returns this for turn/start when an MDM policy forbids approval_policy.
	// It must be surfaced to the user, not silently swallowed.
	line := []byte("{\"id\":3,\"error\":{\"code\":-32600,\"message\":\"invalid thread settings override: `Never` is not in the allowed set [OnRequest, OnFailure] (set by MDM com.openai.codex:requirements_toml_base64)\"}}")

	msgs, err := p.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (error must be surfaced, not swallowed)", len(msgs))
	}
	msg := msgs[0]
	if msg.Type != "result" || msg.Subtype != "error" {
		t.Fatalf("got type=%q subtype=%q, want result/error", msg.Type, msg.Subtype)
	}
	if msg.Result == nil || !msg.Result.IsError {
		t.Fatalf("expected Result with IsError=true, got %+v", msg.Result)
	}
	if !strings.Contains(msg.Result.Result, "not in the allowed set") {
		t.Fatalf("surfaced error should include codex's reason; got %q", msg.Result.Result)
	}
}

func TestParseNumberedOptions(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantOK     bool
		wantStem   string
		wantLabels []string
	}{
		{
			name: "three numbered options with descriptions",
			input: `Who should the rewritten README primarily speak to?
1. Internal engineers (Recommended): Focus on practical value. [confidence: 0.82]
2. Existing users: Focus on usage reference. [confidence: 0.41]
3. External readers: Focus on polished positioning. [confidence: 0.18]`,
			wantOK:     true,
			wantStem:   "Who should the rewritten README primarily speak to?",
			wantLabels: []string{"Internal engineers (Recommended)", "Existing users", "External readers"},
		},
		{
			name:     "free-form sentence with no numbered list",
			input:    "What level of marketing tone do you want: restrained and factual, moderately persuasive, or fairly bold?",
			wantOK:   false,
			wantStem: "",
		},
		{
			name: "only one numbered item is not enough",
			input: `Pick:
1. Only option`,
			wantOK: false,
		},
		{
			name: "bundle of multiple questions as numbered list",
			input: `Tell me:
1. Are we going ahead?
2. Should I include X?
3. Is Y necessary?`,
			wantOK: false,
		},
		{
			name: "continuation lines fold into previous option",
			input: `Question?
1. Short label: first line of
   description continues here.
2. Other label: second option desc.`,
			wantOK:     true,
			wantStem:   "Question?",
			wantLabels: []string{"Short label", "Other label"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stem, opts, ok := parseNumberedOptions(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (stem=%q opts=%+v)", ok, tt.wantOK, stem, opts)
			}
			if !tt.wantOK {
				return
			}
			if stem != tt.wantStem {
				t.Errorf("stem = %q, want %q", stem, tt.wantStem)
			}
			if len(opts) != len(tt.wantLabels) {
				t.Fatalf("opts len = %d, want %d", len(opts), len(tt.wantLabels))
			}
			for i, want := range tt.wantLabels {
				if opts[i].Label != want {
					t.Errorf("opts[%d].Label = %q, want %q", i, opts[i].Label, want)
				}
			}
			if tt.name == "three numbered options with descriptions" {
				if opts[0].Confidence == nil || *opts[0].Confidence != 0.82 {
					t.Fatalf("opts[0].Confidence = %v, want 0.82", opts[0].Confidence)
				}
			}
		})
	}
}

func TestTrimFreeFormSentinel(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantOK     bool
		wantResult string
	}{
		{"prefix only", "FREE_FORM: What version?", true, "What version?"},
		{"with leading whitespace", "  \nFREE_FORM:Name?", true, "Name?"},
		{"no prefix", "What do you want?", false, ""},
		{"prefix in middle ignored", "OK FREE_FORM: not at start", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := trimFreeFormSentinel(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.wantResult {
				t.Errorf("got %q, want %q", got, tt.wantResult)
			}
		})
	}
}

func TestSynthesizeAskUser_IncludesOptions(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test"})
	options := []parsedOption{
		{Label: "Alpha (Recommended)", Description: "first tradeoff", Confidence: floatPtr(0.83)},
		{Label: "Beta", Description: "second tradeoff", Confidence: floatPtr(0.41)},
		{Label: "Gamma", Description: "third tradeoff", Confidence: floatPtr(0.17)},
	}

	msg := p.synthesizeAskUser("Which one?", options)
	if msg.ControlRequest == nil {
		t.Fatal("ControlRequest is nil")
	}
	if msg.ControlRequest.Request.ToolName != "AskUserQuestion" {
		t.Fatalf("ToolName = %q, want AskUserQuestion", msg.ControlRequest.Request.ToolName)
	}

	var parsed struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label       string   `json:"label"`
				Description string   `json:"description"`
				Confidence  *float64 `json:"confidence"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &parsed); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	if len(parsed.Questions) != 1 {
		t.Fatalf("questions len = %d, want 1", len(parsed.Questions))
	}
	q := parsed.Questions[0]
	if q.Question != "Which one?" {
		t.Errorf("question = %q, want %q", q.Question, "Which one?")
	}
	if len(q.Options) != 3 {
		t.Fatalf("options len = %d, want 3", len(q.Options))
	}
	if q.Options[0].Label != "Alpha (Recommended)" || q.Options[0].Description != "first tradeoff" {
		t.Errorf("options[0] = %+v, want Alpha (Recommended)/first tradeoff", q.Options[0])
	}
	if q.Options[0].Confidence == nil || *q.Options[0].Confidence != 0.83 {
		t.Errorf("options[0].Confidence = %v, want 0.83", q.Options[0].Confidence)
	}
}

func floatPtr(v float64) *float64 { return &v }

// TestBuildAskUserAnswerEnvelope_RestatesQuestionAndOptions checks that the
// framed follow-up turn includes the original question, the options the agent
// presented, the user's chosen answer, and a reminder that the reply is an
// answer (not a fresh directive). This is the framing that prevents an agent
// from acting on a bare option label like "Replace README.md" as if it were
// a new instruction.
func TestBuildAskUserAnswerEnvelope_RestatesQuestionAndOptions(t *testing.T) {
	questions := json.RawMessage(`{"questions":[{
		"question":"I found one target: the root README.md. Should the translation replace it or live alongside it?",
		"options":[
			{"label":"Replace README.md (Recommended)","description":"Matches the literal request."},
			{"label":"Add README.scn.md","description":"Preserves the English README."},
			{"label":"Add bilingual README.md","description":"Keeps both in one file."}
		]
	}]}`)
	answers := map[string]string{
		"I found one target: the root README.md. Should the translation replace it or live alongside it?": "Replace README.md (Recommended)",
	}

	got := buildAskUserAnswerEnvelope(questions, answers)

	mustContain := []string{
		"[AskUserQuestion answer]",
		"The user has answered your question.",
		"Question you asked:",
		"> I found one target: the root README.md. Should the translation replace it or live alongside it?",
		"Options you presented:",
		"1. Replace README.md (Recommended) — Matches the literal request.",
		"2. Add README.scn.md — Preserves the English README.",
		"3. Add bilingual README.md — Keeps both in one file.",
		"User's selected answer: Replace README.md (Recommended)",
		"This answer clarifies requirements; it is not authorization to implement, edit repository files, or modify files outside your phase artifact/output directory.",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("envelope missing %q\n--- got ---\n%s", want, got)
		}
	}
	// The framing header must precede the question, so the agent reads it
	// before encountering the imperative-sounding option label.
	if idxHeader := strings.Index(got, "[AskUserQuestion answer]"); idxHeader == -1 || idxHeader >= strings.Index(got, "User's selected answer:") {
		t.Errorf("framing header must appear before the answer line; got:\n%s", got)
	}
}

// TestBuildAskUserAnswerEnvelope_HandlesMissingOptions verifies the envelope
// still frames the answer when the original question carried no options
// (e.g. a free-form question or a malformed payload). The question text and
// answer must still be present even without an options block.
func TestBuildAskUserAnswerEnvelope_HandlesMissingOptions(t *testing.T) {
	questions := json.RawMessage(`{"questions":[{"question":"What version should we bump to?"}]}`)
	answers := map[string]string{"What version should we bump to?": "2.0.0"}

	got := buildAskUserAnswerEnvelope(questions, answers)

	if !strings.Contains(got, "Question you asked:") {
		t.Errorf("missing question framing:\n%s", got)
	}
	if !strings.Contains(got, "> What version should we bump to?") {
		t.Errorf("missing question text:\n%s", got)
	}
	if !strings.Contains(got, "User's selected answer: 2.0.0") {
		t.Errorf("missing answer:\n%s", got)
	}
	if !strings.Contains(got, "This answer clarifies requirements; it is not authorization to implement") {
		t.Errorf("missing non-authorization reminder:\n%s", got)
	}
	if strings.Contains(got, "Options you presented:") {
		t.Errorf("should omit options block when none were presented:\n%s", got)
	}
}

// TestRespondToAskUser_SyntheticSendsFramedFollowUp pins down the wire
// behaviour the synthetic ask-user path produces. Before this change it sent
// the bare answer string ("Replace README.md") as a fresh user turn, and Codex
// was observed treating it as a new directive. The follow-up turn must now
// arrive wrapped in the framing envelope.
func TestRespondToAskUser_SyntheticSendsFramedFollowUp(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	questions := json.RawMessage(`{"questions":[{
		"question":"Replace or add alongside?",
		"options":[
			{"label":"Replace (Recommended)","description":"matches the literal request"},
			{"label":"Add alongside","description":"preserves the original"}
		]
	}]}`)
	answers := map[string]string{"Replace or add alongside?": "Replace (Recommended)"}

	if err := p.RespondToAskUser("codex-synthetic-123", questions, answers, nil); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}

	var sent Request
	if err := json.Unmarshal(buf.Bytes(), &sent); err != nil {
		t.Fatalf("unmarshal request: %v\nraw: %s", err, buf.String())
	}
	if sent.Method != "turn/start" {
		t.Fatalf("method = %q, want turn/start", sent.Method)
	}
	paramsBytes, err := json.Marshal(sent.Params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var tp TurnStartParams
	if err := json.Unmarshal(paramsBytes, &tp); err != nil {
		t.Fatalf("unmarshal params: %v\nraw: %s", err, string(paramsBytes))
	}
	if len(tp.Input) != 1 || tp.Input[0].Type != "text" {
		t.Fatalf("input = %+v, want one text item", tp.Input)
	}
	text := tp.Input[0].Text
	for _, want := range []string{
		"[AskUserQuestion answer]",
		"The user has answered your question.",
		"> Replace or add alongside?",
		"1. Replace (Recommended) — matches the literal request",
		"User's selected answer: Replace (Recommended)",
		"This answer clarifies requirements; it is not authorization to implement, edit repository files, or modify files outside your phase artifact/output directory.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("follow-up turn missing %q\n--- got ---\n%s", want, text)
		}
	}
	// Sanity: the bare answer is no longer the entire payload — that was the
	// pre-fix shape and is what allowed Codex to read it as a new directive.
	if strings.TrimSpace(text) == "Replace (Recommended)" {
		t.Errorf("follow-up turn is still a bare answer, framing not applied:\n%s", text)
	}
}

// completedTurnParams is a small helper that builds a turn/completed params
// JSON for the retry-path tests below.
func completedTurnParams(t *testing.T, threadID string) json.RawMessage {
	t.Helper()
	payload := TurnCompletedParams{
		ThreadID: threadID,
		Turn:     CompletedTurn{ID: "turn-1", Status: "completed"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal turn/completed: %v", err)
	}
	return raw
}

func TestTurnCompletedComputesCostWithCachedInputTokens(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "gpt-5.5"})

	usage := map[string]interface{}{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"tokenUsage": map[string]interface{}{
			"total": map[string]interface{}{
				"inputTokens":       1_000_000,
				"cachedInputTokens": 400_000,
				"outputTokens":      100_000,
				"totalTokens":       1_100_000,
			},
			"last": map[string]interface{}{
				"inputTokens":       1_000_000,
				"cachedInputTokens": 400_000,
				"outputTokens":      100_000,
				"totalTokens":       1_100_000,
			},
		},
	}
	params, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.parseNotification("thread/tokenUsage/updated", params); !ok {
		t.Fatal("token usage notification ok = false, want true")
	}

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("turn completed ok = false, want true")
	}
	if msg.Result == nil {
		t.Fatal("Result = nil")
	}
	const want = 10.90 // Long context: 600K at $10/M + 400K cached at $1/M + 100K output at $45/M
	if diff := msg.Result.TotalCostUSD - want; diff < -0.001 || diff > 0.001 {
		t.Fatalf("TotalCostUSD = %.6f, want %.2f", msg.Result.TotalCostUSD, want)
	}
	if msg.Result.Usage == nil {
		t.Fatal("Result.Usage = nil")
	}
	if msg.Result.Usage.CacheReadInputTokens != 400_000 {
		t.Fatalf("Result.Usage.CacheReadInputTokens = %d, want 400000", msg.Result.Usage.CacheReadInputTokens)
	}
}

func TestTurnCompletedAccumulatesShortAndLongContextRates(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "gpt-5.6-luna"})

	updates := []map[string]interface{}{
		{
			"total": map[string]interface{}{
				"inputTokens": 200_000, "cachedInputTokens": 20_000,
				"outputTokens": 10_000, "totalTokens": 210_000,
			},
			"last": map[string]interface{}{
				"inputTokens": 200_000, "cachedInputTokens": 20_000,
				"outputTokens": 10_000, "totalTokens": 210_000,
			},
		},
		{
			"total": map[string]interface{}{
				"inputTokens": 300_000, "cachedInputTokens": 30_000,
				"outputTokens": 20_000, "totalTokens": 320_000,
			},
			"last": map[string]interface{}{
				"inputTokens": 300_000, "cachedInputTokens": 30_000,
				"outputTokens": 20_000, "totalTokens": 320_000,
			},
		},
	}
	for _, tokenUsage := range updates {
		params, err := json.Marshal(map[string]interface{}{
			"threadId":   "thread-1",
			"turnId":     "turn-1",
			"tokenUsage": tokenUsage,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.parseNotification("thread/tokenUsage/updated", params); !ok {
			t.Fatal("token usage notification ok = false, want true")
		}
	}

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok || msg.Result == nil {
		t.Fatal("turn completed did not return a result")
	}
	// First update uses short-context rates ($0.242); the 100K/10K/10K
	// deltas in the second update use long-context rates ($0.272).
	const want = 0.514
	if diff := msg.Result.TotalCostUSD - want; diff < -0.001 || diff > 0.001 {
		t.Fatalf("TotalCostUSD = %.6f, want %.3f", msg.Result.TotalCostUSD, want)
	}
}

func completedAgentMessageParams(t *testing.T, threadID, itemID, text string) json.RawMessage {
	t.Helper()
	payload := ItemCompletedParams{
		ThreadID: threadID,
		TurnID:   "turn-1",
		Item: ItemUnion{
			ID:   itemID,
			Type: "agentMessage",
			Text: text,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal item/completed: %v", err)
	}
	return raw
}

func TestTurnCompleted_WellFormedQuestionSurfacesOptions(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = "Which audience should the README target?\n" +
		"1. Internal engineers (Recommended): Focus on practical value.\n" +
		"2. Existing users: Focus on usage reference.\n" +
		"3. External readers: Focus on positioning."
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "control_request" || msg.ControlRequest == nil {
		t.Fatalf("got Type=%q ControlRequest=%v, want control_request", msg.Type, msg.ControlRequest)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected follow-up turn written to stdin: %s", buf.String())
	}
	p.mu.Lock()
	retry := p.formatRetryCount
	p.mu.Unlock()
	if retry != 0 {
		t.Errorf("formatRetryCount = %d, want 0 after well-formed turn", retry)
	}
}

func TestTurnCompleted_BlankAgentMessageDoesNotEraseWellFormedQuestion(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	question := "How broad should the translation be?\n" +
		"1. README only (Recommended): Translate just the top-level README and leave docs untouched. [confidence: 0.94]\n" +
		"2. README + user docs: Translate the README plus user-facing docs like docs/. [confidence: 0.34]\n" +
		"3. Whole repo markdown: Translate every Markdown file in the repo. [confidence: 0.12]"
	msg, ok := p.parseNotification("item/completed", completedAgentMessageParams(t, "thread-1", "msg-question", question))
	if !ok {
		t.Fatal("question item parseNotification ok = false, want true")
	}
	if msg.Type != codexRoleAssistant {
		t.Fatalf("question item Type = %q, want assistant", msg.Type)
	}

	msg, ok = p.parseNotification("item/completed", completedAgentMessageParams(t, "thread-1", "msg-empty", ""))
	if ok {
		t.Fatalf("blank item parseNotification ok = true (msg=%+v), want false", msg)
	}

	msg, ok = p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("turn/completed parseNotification ok = false, want true")
	}
	if msg.Type != "control_request" || msg.ControlRequest == nil {
		t.Fatalf("got Type=%q ControlRequest=%v, want AskUserQuestion control_request", msg.Type, msg.ControlRequest)
	}
	if msg.ControlRequest.Request.ToolName != "AskUserQuestion" {
		t.Fatalf("ToolName = %q, want AskUserQuestion", msg.ControlRequest.Request.ToolName)
	}

	var parsed struct {
		Questions []struct {
			Question string           `json:"question"`
			Options  []map[string]any `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &parsed); err != nil {
		t.Fatalf("unmarshal control request input: %v", err)
	}
	if len(parsed.Questions) != 1 {
		t.Fatalf("questions len = %d, want 1", len(parsed.Questions))
	}
	if parsed.Questions[0].Question != "How broad should the translation be?" {
		t.Errorf("question = %q", parsed.Questions[0].Question)
	}
	if len(parsed.Questions[0].Options) != 3 {
		t.Errorf("options len = %d, want 3", len(parsed.Questions[0].Options))
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected follow-up turn written to stdin: %s", buf.String())
	}
}

func TestTurnCompleted_IllFormedQuestionTriggersReformatRetry(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = "What tone do you want: restrained, persuasive, or bold?"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if ok {
		t.Fatalf("parseNotification ok = true, want false (got msg=%+v)", msg)
	}
	if buf.Len() == 0 {
		t.Fatal("expected a follow-up turn to be written to stdin")
	}
	if !strings.Contains(buf.String(), "not in the required question format") {
		t.Errorf("reminder missing format-violation language; payload=%s", buf.String())
	}
	if !strings.Contains(buf.String(), "exactly 3 numbered options") {
		t.Errorf("reminder missing option-count directive; payload=%s", buf.String())
	}
	p.mu.Lock()
	retry := p.formatRetryCount
	p.mu.Unlock()
	if retry != 1 {
		t.Errorf("formatRetryCount = %d, want 1 after first violation", retry)
	}
}

func TestTurnCompleted_IllFormedFallsThroughAfterCap(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.formatRetryCount = maxQuestionFormatRetries
	p.lastAssistantText = "What is the target version?"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true after cap")
	}
	if msg.Type != "control_request" || msg.ControlRequest == nil {
		t.Fatalf("got Type=%q, want control_request", msg.Type)
	}
	if buf.Len() != 0 {
		t.Errorf("no follow-up should be written after cap; got %s", buf.String())
	}
	// Options should be empty in the fall-through path.
	var parsed struct {
		Questions []struct {
			Options []map[string]string `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Questions) != 1 || len(parsed.Questions[0].Options) != 0 {
		t.Errorf("expected 1 question with 0 options, got %+v", parsed.Questions)
	}
	p.mu.Lock()
	retry := p.formatRetryCount
	p.mu.Unlock()
	if retry != 0 {
		t.Errorf("formatRetryCount = %d, want 0 after fall-through reset", retry)
	}
}

func TestTurnCompleted_FreeFormSentinelSkipsRetry(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = "FREE_FORM: What exact version string should we pin?"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if buf.Len() != 0 {
		t.Errorf("no follow-up should be written for FREE_FORM; got %s", buf.String())
	}
	var parsed struct {
		Questions []struct {
			Question string              `json:"question"`
			Options  []map[string]string `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Questions) != 1 {
		t.Fatalf("questions len = %d, want 1", len(parsed.Questions))
	}
	if strings.HasPrefix(parsed.Questions[0].Question, "FREE_FORM:") {
		t.Errorf("question still contains FREE_FORM sentinel: %q", parsed.Questions[0].Question)
	}
	if len(parsed.Questions[0].Options) != 0 {
		t.Errorf("FREE_FORM question should have 0 options, got %d", len(parsed.Questions[0].Options))
	}
}

func TestTextContainsVerdictSentinel(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"verdict approved", "some rationale\n## Verdict\nAPPROVED\n", true},
		{"verdict changes_requested", "## Verdict\nCHANGES_REQUESTED", true},
		{"plain narrative with question mark", "Does this look right? I think so.", false},
		{"empty", "", false},
		{"verdict heading only without token", "## Verdict\nUNCLEAR", false},
		{"legacy stdout marker no longer matches", "REVIEW_STATUS: APPROVED", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := textContainsVerdictSentinel(tt.text); got != tt.want {
				t.Errorf("textContainsVerdictSentinel(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestParseNumberedOptions_RejectsVerdictSentinels(t *testing.T) {
	// A critic's structured verdict (## Findings / ## Suggestions / ## Verdict)
	// must never be parsed as AskUser options, even when some rubric bullets
	// contain '?' characters. The presence of `## Verdict\nAPPROVED|CHANGES_REQUESTED`
	// anywhere in the option body is decisive.
	input := `Evaluating the plan now.
1. Assessment
- Right level of detail? PASS
- Avoids contradictions? PASS
2. Verdict summary
APPROVED
3. Notes
No structural changes are required.
## Verdict
APPROVED`

	stem, opts, ok := parseNumberedOptions(input)
	if ok {
		t.Fatalf("parseNumberedOptions ok = true (stem=%q opts=%+v), want false for verdict-tainted numbered list", stem, opts)
	}
}

func TestTurnCompleted_ValidatorVerdictNotMisclassified(t *testing.T) {
	// Reproduces the Structural-validator false positive: a read-only
	// critic emits a final answer containing rubric bullets phrased as
	// questions ("Stubs clearly marked?") plus a numbered structure,
	// terminated by the `## Verdict\nAPPROVED` echo of what was written
	// to review-feedback.md. This must be treated as a completed success,
	// not a synthetic AskUser, and must not trigger a reformat follow-up.
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = `1. **Assessment**

- **Tracer Bullet: End-to-end wiring defined?** PASS
- **Tracer Bullet: Stubs clearly marked?** PASS
- **Structural Soundness: Avoids contradictions?** PASS

2. **Verdict summary**

APPROVED

3. **Specific feedback**

No structural changes are required.

I wrote review-feedback.md with the structured handoff:

## Verdict
APPROVED`
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true (validator verdict should take the success path)")
	}
	if msg.Type != "result" || msg.Subtype != "success" {
		t.Fatalf("got Type=%q Subtype=%q, want result/success (ControlRequest=%v)", msg.Type, msg.Subtype, msg.ControlRequest)
	}
	if msg.ControlRequest != nil {
		t.Errorf("unexpected ControlRequest on validator verdict: %+v", msg.ControlRequest)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected reformat follow-up written to stdin: %s", buf.String())
	}
	p.mu.Lock()
	retry := p.formatRetryCount
	p.mu.Unlock()
	if retry != 0 {
		t.Errorf("formatRetryCount = %d, want 0 (verdict should not enter the question branch)", retry)
	}
}

// TestTurnCompleted_NumberedOptionsAfterToolUseSurfacesQuestion reproduces
// the roadmap-FAILED case: the agent does extensive tool exploration in a
// single turn (rg/cat to ground its question in the codebase) and then
// asks the one remaining ambiguity as a well-formed numbered-options
// question. Even though turnHadToolUse=true, the question must surface
// as a control_request rather than emitting a SUCCESS Result that
// triggers session shutdown without a phase_complete.
func TestTurnCompleted_NumberedOptionsAfterToolUseSurfacesQuestion(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.turnHadToolUse = true
	p.lastAssistantText = "Which Napolitan register should the translations use across all three READMEs?\n" +
		"1. Neutral written Napolitan (Recommended): Documentation-friendly register that reads naturally in writing. [confidence: 0.89]\n" +
		"2. Colloquial Napolitan: More spoken, expressive tone — risks uneven technical clarity. [confidence: 0.48]\n" +
		"3. Conservative Italian-leaning Napolitan: Maximum readability but weakens the request for a distinctly Napolitan translation. [confidence: 0.36]"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "control_request" || msg.ControlRequest == nil {
		t.Fatalf("got Type=%q ControlRequest=%v, want control_request (tool-use turn ended in well-formed question)", msg.Type, msg.ControlRequest)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected follow-up turn written to stdin: %s", buf.String())
	}

	var parsed struct {
		Questions []struct {
			Options []map[string]any `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(msg.ControlRequest.Request.Input, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Questions) != 1 {
		t.Fatalf("questions len = %d, want 1", len(parsed.Questions))
	}
	if len(parsed.Questions[0].Options) != 3 {
		t.Errorf("options len = %d, want 3 parsed from numbered list", len(parsed.Questions[0].Options))
	}
}

// TestTurnCompleted_FreeFormSentinelAfterToolUse covers the FREE_FORM-sentinel
// path under the same tool-use scenario: an explicit FREE_FORM marker is an
// unambiguous question signal that should bypass the no-tool-use gate.
func TestTurnCompleted_FreeFormSentinelAfterToolUse(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.turnHadToolUse = true
	p.lastAssistantText = "FREE_FORM: What exact version string should we pin?"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "control_request" || msg.ControlRequest == nil {
		t.Fatalf("got Type=%q ControlRequest=%v, want control_request", msg.Type, msg.ControlRequest)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected follow-up turn written to stdin: %s", buf.String())
	}
}

// TestTurnCompleted_LooseQuestionAfterToolUseEmitsSuccess pins down the
// false-positive guard. A tool-heavy turn whose final text merely contains
// '?' without numbered options or a FREE_FORM sentinel — e.g., narrating
// intent like "Wrote the file. Is that what you wanted?" — must NOT be
// reclassified as a question when no MarkerPath is configured (legacy /
// test paths). Without the no-tool-use gate on the loose path, every mid-
// turn rhetorical '?' would synthesize an AskUser and stall the session.
func TestTurnCompleted_LooseQuestionAfterToolUseEmitsSuccess(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.turnHadToolUse = true
	p.lastAssistantText = "Wrote the README and touched phase_complete. Is that what you wanted?"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "result" || msg.Subtype != "success" {
		t.Fatalf("got Type=%q Subtype=%q, want result/success (ControlRequest=%v)", msg.Type, msg.Subtype, msg.ControlRequest)
	}
	if msg.ControlRequest != nil {
		t.Errorf("unexpected ControlRequest on loose-question tool-use turn: %+v", msg.ControlRequest)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected reformat follow-up written to stdin: %s", buf.String())
	}
}

func TestTurnCompleted_LooseQuestionAfterToolUse_NoMarker_TriggersReformat(t *testing.T) {
	tmp := t.TempDir()
	markerPath := tmp + "/phase_complete"

	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{
		WorkDir:    "/tmp/test",
		Model:      "codex",
		MarkerPath: markerPath,
	})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.turnHadToolUse = true
	p.lastAssistantText = "I'd recommend keeping the no-subcommand launch contract. Is that the startup contract you want for the web replacement?"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if ok {
		t.Fatalf("parseNotification ok = true (msg=%+v); want false (reformat-retry suppresses the message)", msg)
	}
	if buf.Len() == 0 {
		t.Fatal("expected a reformat follow-up turn to be written to stdin")
	}
	if !strings.Contains(buf.String(), "not in the required question format") {
		t.Errorf("reformat reminder missing format-violation language; payload=%s", buf.String())
	}
	p.mu.Lock()
	retry := p.formatRetryCount
	p.mu.Unlock()
	if retry != 1 {
		t.Errorf("formatRetryCount = %d, want 1 after first marker-absent loose-question turn", retry)
	}
}

// TestTurnCompleted_LooseQuestionAfterToolUse_WithMarker_EmitsSuccess
// covers the legitimate completion path. The agent did tool use, the
// marker file IS present on disk (i.e., the agent executed the
// completion contract), and the trailing "?" is rhetorical narration.
// Marker presence is the authoritative completion signal, so we emit a
// SUCCESS Result without sending a reformat reminder.
func TestTurnCompleted_LooseQuestionAfterToolUse_WithMarker_EmitsSuccess(t *testing.T) {
	tmp := t.TempDir()
	markerPath := tmp + "/phase_complete"
	if err := os.WriteFile(markerPath, nil, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{
		WorkDir:    "/tmp/test",
		Model:      "codex",
		MarkerPath: markerPath,
	})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.turnHadToolUse = true
	p.lastAssistantText = "Wrote the README and touched phase_complete. Is that what you wanted?"
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "result" || msg.Subtype != "success" {
		t.Fatalf("got Type=%q Subtype=%q, want result/success when marker is present", msg.Type, msg.Subtype)
	}
	if msg.ControlRequest != nil {
		t.Errorf("unexpected ControlRequest when marker is present: %+v", msg.ControlRequest)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected reformat follow-up written to stdin when marker is present: %s", buf.String())
	}
}

// TestTurnCompleted_InformationalNumberedListIsCleanSuccess proves a final text
// that merely summarizes findings as a numbered list — with a non-question stem
// — completes as a success result rather than being treated as an AskUserQuestion
// just because it enumerates items.
func TestTurnCompleted_InformationalNumberedListIsCleanSuccess(t *testing.T) {
	var buf bytes.Buffer
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex"})
	p.SetStdin(&buf)
	p.SetThreadIDForTest("thread-1")

	p.mu.Lock()
	p.lastAssistantText = "Here is what I found:\n" +
		"1. The config loader ignores env overrides.\n" +
		"2. The default timeout is 30s.\n" +
		"3. Logs are written to /tmp/agentico.log."
	p.mu.Unlock()

	msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
	if !ok {
		t.Fatal("parseNotification ok = false, want true")
	}
	if msg.Type != "result" || msg.Subtype != "success" {
		t.Fatalf("got Type=%q Subtype=%q ControlRequest=%v, want result/success", msg.Type, msg.Subtype, msg.ControlRequest)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected follow-up turn written to stdin: %s", buf.String())
	}
}

// TestTurnCompleted_InteractiveNeverSynthesizesQuestion proves an Interactive
// session (AMA chat, where a human answers every turn directly) never
// synthesizes an AskUserQuestion picker, no matter how question-shaped the
// final text is: the text-parsing pipeline exists only to imitate Claude's
// native tool-call UX for a provider that can otherwise only express a
// question as plain text, and a human reading the chat gets no benefit from
// that imitation — they can just read whatever the model asked and reply with
// an ordinary follow-up message.
func TestTurnCompleted_InteractiveNeverSynthesizesQuestion(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"numbered options", "Which audience should the README target?\n" +
			"1. Internal engineers (Recommended): Focus on practical value.\n" +
			"2. Existing users: Focus on usage reference.\n" +
			"3. External readers: Focus on positioning."},
		{"bare question", "Should I proceed with the destructive migration?"},
		{"FREE_FORM sentinel", "FREE_FORM: What exact version string should we pin?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := NewProtocol(llm.ProtocolOpts{WorkDir: "/tmp/test", Model: "codex", Interactive: true})
			p.SetStdin(&buf)
			p.SetThreadIDForTest("thread-1")

			p.mu.Lock()
			p.lastAssistantText = c.text
			p.mu.Unlock()

			msg, ok := p.parseNotification("turn/completed", completedTurnParams(t, "thread-1"))
			if !ok {
				t.Fatal("parseNotification ok = false, want true")
			}
			if msg.Type != "result" || msg.Subtype != "success" {
				t.Fatalf("got Type=%q Subtype=%q ControlRequest=%v, want result/success", msg.Type, msg.Subtype, msg.ControlRequest)
			}
			if buf.Len() != 0 {
				t.Errorf("sent a reformat reminder, want none: %s", buf.String())
			}
		})
	}
}

// TestBuildAskUserAnswerEnvelope_AppendsAskingFormatReminder verifies that
// every answer envelope re-anchors Codex on the question-format contract.
// The reminder is intentionally a short pointer back to the system prompt
// (the full spec lives there); this test pins the [Reminder] marker, the
// pointer phrasing, and the post-answer ordering — not specific format
// rules, which belong with the system prompt.
func TestBuildAskUserAnswerEnvelope_AppendsAskingFormatReminder(t *testing.T) {
	questions := json.RawMessage(`{"questions":[{
		"question":"Replace or add alongside?",
		"options":[
			{"label":"Replace (Recommended)","description":"matches the literal request"},
			{"label":"Add alongside","description":"preserves the original"}
		]
	}]}`)
	answers := map[string]string{"Replace or add alongside?": "Replace (Recommended)"}

	got := buildAskUserAnswerEnvelope(questions, answers)

	if !strings.Contains(got, "[Reminder]") {
		t.Errorf("envelope missing [Reminder] marker\n--- got ---\n%s", got)
	}
	if !strings.Contains(got, "asking-questions format from your system prompt") {
		t.Errorf("envelope missing pointer back to system prompt\n--- got ---\n%s", got)
	}
	if !strings.Contains(got, "not authorization to implement") {
		t.Errorf("envelope missing non-authorization reminder\n--- got ---\n%s", got)
	}
	// The reminder must come AFTER the answer block so the agent reads the
	// answer first and the format rule last (most-recent-wins anchoring).
	idxAnswer := strings.Index(got, "User's selected answer:")
	idxReminder := strings.Index(got, "[Reminder]")
	if idxAnswer == -1 || idxReminder == -1 || idxReminder < idxAnswer {
		t.Errorf("reminder must follow the answer block; got idxAnswer=%d idxReminder=%d in:\n%s", idxAnswer, idxReminder, got)
	}
}

func TestCodexAskingQuestionsClause_CallsOutConfirmationTrap(t *testing.T) {
	clause := (&Provider{}).AskingQuestionsClause()
	for _, want := range []string{
		"Confirmation traps to avoid",
		"every turn of an interview",
		"Yes, do X (Recommended)",
	} {
		if !strings.Contains(clause, want) {
			t.Errorf("AskingQuestionsClause missing %q\n--- got ---\n%s", want, clause)
		}
	}
}
