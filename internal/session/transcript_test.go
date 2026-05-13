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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeProjectsDirName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/alice/Projects/myapp", "-Users-alice-Projects-myapp"},
		{"/Users/bob.smith/code", "-Users-bob-smith-code"},
		{"/home/user/.hidden/dir", "-home-user--hidden-dir"},
	}
	for _, tt := range tests {
		got := claudeProjectsDirName(tt.input)
		if got != tt.want {
			t.Errorf("claudeProjectsDirName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReadTranscriptError_ApiErrorMessage(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "test-session.jsonl")

	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s1","model":"opus"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I'll read the files."}]}}`,
		`{"type":"assistant","isApiErrorMessage":true,"error":"invalid_request","message":{"role":"assistant","content":[{"type":"text","text":"Request too large (max 20MB). Try with a smaller file."}]}}`,
		`{"type":"last-prompt","lastPrompt":"..."}`,
	}
	os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	got := readTranscriptError(transcript)
	if got != "Request too large (max 20MB). Try with a smaller file." {
		t.Errorf("readTranscriptError() = %q, want request-too-large message", got)
	}
}

func TestReadTranscriptError_ResultError(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "test-session.jsonl")

	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s1","model":"opus"}`,
		`{"type":"result","subtype":"error","session_id":"s1","total_cost_usd":0.01,"result":"API rate limit exceeded"}`,
	}
	os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	got := readTranscriptError(transcript)
	if got != "API rate limit exceeded" {
		t.Errorf("readTranscriptError() = %q, want 'API rate limit exceeded'", got)
	}
}

func TestReadTranscriptError_NoError(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "test-session.jsonl")

	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s1","model":"opus"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"All done!"}]}}`,
		`{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.05}`,
	}
	os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	got := readTranscriptError(transcript)
	if got != "" {
		t.Errorf("readTranscriptError() = %q, want empty for success", got)
	}
}

func TestReadTranscriptError_MissingFile(t *testing.T) {
	got := readTranscriptError("/nonexistent/path.jsonl")
	if got != "" {
		t.Errorf("readTranscriptError() = %q, want empty for missing file", got)
	}
}

func TestReadTranscriptError_Truncation(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "test-session.jsonl")

	longText := strings.Repeat("x", 300)
	line := `{"type":"assistant","isApiErrorMessage":true,"message":{"role":"assistant","content":[{"type":"text","text":"` + longText + `"}]}}`
	os.WriteFile(transcript, []byte(line+"\n"), 0o644)

	got := readTranscriptError(transcript)
	if len(got) > 210 {
		t.Errorf("readTranscriptError() len = %d, should be truncated", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("truncated text should end with ellipsis")
	}
}

func TestReadTranscriptError_LargeLinesBeforeError(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "test-session.jsonl")

	// Simulate a transcript with a huge line (like a base64 PDF tool result)
	// followed by the actual error message.
	hugeLine := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"` + strings.Repeat("A", 2*1024*1024) + `"}]}}`
	errorLine := `{"type":"assistant","isApiErrorMessage":true,"error":"invalid_request","message":{"role":"assistant","content":[{"type":"text","text":"Request too large (max 20MB). Try with a smaller file."}]}}`
	lastPrompt := `{"type":"last-prompt","lastPrompt":"..."}`

	content := hugeLine + "\n" + errorLine + "\n" + lastPrompt + "\n"
	os.WriteFile(transcript, []byte(content), 0o644)

	got := readTranscriptError(transcript)
	if got != "Request too large (max 20MB). Try with a smaller file." {
		t.Errorf("readTranscriptError() = %q, want request-too-large message", got)
	}
}

func TestReadLastLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := "line1\nline2\nline3\nline4\nline5\n"
	os.WriteFile(path, []byte(content), 0o644)

	got := readLastLines(path, 3)
	if len(got) != 3 {
		t.Fatalf("readLastLines(3) = %d lines, want 3", len(got))
	}
	if got[0] != "line3" || got[1] != "line4" || got[2] != "line5" {
		t.Errorf("readLastLines(3) = %v, want [line3 line4 line5]", got)
	}

	all := readLastLines(path, 100)
	if len(all) != 5 {
		t.Fatalf("readLastLines(100) = %d lines, want 5", len(all))
	}
}

func TestExtractSessionIDFromOutput(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.txt")

	os.WriteFile(outputPath, []byte("[init] session=abc-123-def model=opus\n[thinking] hello\n"), 0o644)
	got := extractSessionIDFromOutput(outputPath)
	if got != "abc-123-def" {
		t.Errorf("extractSessionIDFromOutput() = %q, want %q", got, "abc-123-def")
	}
}

func TestErrorDetailFromOutput(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.txt")
	os.WriteFile(outputPath, []byte("[init] session=nonexistent model=opus\n"), 0o644)

	got := ErrorDetailFromOutput(outputPath, "/some/path")
	if got != "" {
		t.Errorf("ErrorDetailFromOutput with missing transcript = %q, want empty", got)
	}
}

func TestLooksLikeNormalChat(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"I'll start by reading the files.", true},
		{"Let me analyze the codebase.", true},
		{"I'm going to research this topic.", true},
		{"Request too large (max 20MB). Try with a smaller file.", false},
		{"API error: rate limit exceeded", false},
		{"max turns reached", false},
		{"", false},
	}
	for _, tt := range tests {
		got := looksLikeNormalChat(tt.text)
		if got != tt.want {
			t.Errorf("looksLikeNormalChat(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}
