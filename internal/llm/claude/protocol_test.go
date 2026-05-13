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
