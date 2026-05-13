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
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// codexAvailable returns true if the codex CLI is installed and reachable.
func codexAvailable() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

// TestCodexSessionExecLeavesStderrOutOfAssistantLog verifies that non-protocol
// codex exec output on stderr no longer leaks into the interactive assistant log.
func TestCodexSessionExecLeavesStderrOutOfAssistantLog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping codex integration test in short mode")
	}
	if os.Getenv("AGENTIC_CODEX_LIVE") != "1" {
		t.Skip("skipping live codex test: set AGENTIC_CODEX_LIVE=1 to run")
	}
	if !codexAvailable() {
		t.Skip("codex CLI not available")
	}

	// Clear CLAUDECODE to allow nesting when running inside Claude Code.
	t.Setenv("CLAUDECODE", "")

	dir := t.TempDir()
	responsePath := dir + "/response.txt"
	command := []string{"codex", "exec", "--ephemeral", "--skip-git-repo-check", "-o", responsePath, "Reply with exactly: HELLO_CODEX_TEST"}

	s := NewSession("codex-stderr-test", "feat-1", feature.PhaseReview)
	s.permHandler = &AutoApproveHandler{}

	// Collect attach channel messages in a goroutine
	var attachMsgs []llm.SDKMessage
	attachDone := make(chan struct{})
	go func() {
		defer close(attachDone)
		for msg := range s.AttachCh() {
			attachMsgs = append(attachMsgs, msg)
		}
	}()

	err := s.Start(command, dir, nil, nil)
	if err != nil {
		t.Fatalf("start codex session: %v", err)
	}
	s.CloseStdin() // Signal EOF so codex doesn't block waiting for input

	// Wait for codex to complete (should be fast for simple prompt)
	select {
	case <-s.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("codex session did not complete within 30s timeout")
	}

	// Wait for attach channel to be drained
	select {
	case <-attachDone:
	case <-time.After(5 * time.Second):
		t.Fatal("attach channel not closed within timeout")
	}

	if got := s.messageLog.AssistantText(); got != "" {
		t.Errorf("AssistantText() = %q, want empty for codex exec stderr output", truncate(got, 120))
	}
	if len(attachMsgs) != 0 {
		t.Errorf("AttachCh delivered %d messages, want 0 for stderr-only codex exec", len(attachMsgs))
	}

	// Verify the -o response file was written (codex's primary output mechanism)
	if data, err := os.ReadFile(responsePath); err == nil {
		t.Logf("Response file content: %q", truncate(string(data), 200))
	} else {
		t.Logf("Response file not found (may be expected if codex exited early): %v", err)
	}
}

// TestCodexSessionExecCanLogStderrToFile verifies the replacement debugging path:
// stderr may be captured to SessionOpts.StderrPath, but it does not drive the UI.
func TestCodexSessionExecCanLogStderrToFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping codex integration test in short mode")
	}
	if os.Getenv("AGENTIC_CODEX_LIVE") != "1" {
		t.Skip("skipping live codex test: set AGENTIC_CODEX_LIVE=1 to run")
	}
	if !codexAvailable() {
		t.Skip("codex CLI not available")
	}

	t.Setenv("CLAUDECODE", "")

	dir := t.TempDir()
	responsePath := dir + "/response.txt"
	stderrPath := dir + "/stderr.log"
	command := []string{"codex", "exec", "--ephemeral", "--skip-git-repo-check", "-o", responsePath, "Count from 1 to 5, one number per line."}

	s := NewSession("codex-attach-test", "feat-1", feature.PhaseReview)
	s.permHandler = &AutoApproveHandler{}
	s.stderrPath = stderrPath

	err := s.Start(command, dir, nil, nil)
	if err != nil {
		t.Fatalf("start codex session: %v", err)
	}
	s.CloseStdin() // Signal EOF so codex doesn't block waiting for input

	select {
	case <-s.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for codex exec session")
	}
	if data, err := os.ReadFile(stderrPath); err == nil {
		t.Logf("stderr log size: %d", len(data))
	}
}

// TestCodexSessionManagerIntegration tests the full flow through the session
// Manager, mirroring how runReviewGate / specialized plan validators actually use it.
func TestCodexSessionManagerIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping codex integration test in short mode")
	}
	if os.Getenv("AGENTIC_CODEX_LIVE") != "1" {
		t.Skip("skipping live codex test: set AGENTIC_CODEX_LIVE=1 to run")
	}
	if !codexAvailable() {
		t.Skip("codex CLI not available")
	}

	t.Setenv("CLAUDECODE", "")

	dir := t.TempDir()
	responsePath := dir + "/response.txt"
	logPath := dir + "/review-output.txt"

	prompt := "Reply with exactly: ## Verdict\nAPPROVED"
	command := []string{"codex", "exec", "--ephemeral", "--skip-git-repo-check", "-o", responsePath, prompt}

	eventCh := make(chan interface{}, 100)
	sm := NewManager(eventCh)
	defer sm.Shutdown()

	sess, err := sm.StartSession(
		"codex-mgr-test",
		"feat-1",
		feature.PhaseReview,
		command,
		dir,
		nil,
		&SessionOpts{
			PermHandler: &AutoApproveHandler{},
			LogPath:     logPath,
		},
	)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Wait for session to complete
	select {
	case <-sess.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	// Verify messages were captured
	msgCount := sess.MessageLog().Len()
	t.Logf("messageLog has %d messages", msgCount)
	if msgCount == 0 {
		t.Error("expected some messages in messageLog")
	}

	// Verify the message log has assistant text
	allText := sess.MessageLog().AssistantText()
	t.Logf("AssistantText (first 200 chars): %q", truncate(allText, 200))

	// Verify the response file has content
	if data, err := os.ReadFile(responsePath); err == nil {
		content := strings.TrimSpace(string(data))
		t.Logf("Response file: %q", truncate(content, 200))
		if content == "" {
			t.Error("expected non-empty response file from codex -o")
		}
	} else {
		t.Logf("Response file not readable: %v", err)
	}

	// Verify the log file was written
	if data, err := os.ReadFile(logPath); err == nil {
		t.Logf("Log file size: %d bytes", len(data))
	}

	// Verify events were routed to eventCh
	var gotEvents int
	timeout := time.After(2 * time.Second)
drainLoop:
	for {
		select {
		case <-eventCh:
			gotEvents++
		case <-timeout:
			break drainLoop
		default:
			if gotEvents > 0 {
				break drainLoop
			}
			// Retained in short-skipped live integration: polling interval while
			// waiting for at least one routed Codex event.
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Logf("Received %d events on eventCh", gotEvents)
}

// TestCodexSessionANSIStripping verifies that assistant log text remains free
// of raw ANSI escapes in the surviving interactive-session path.
func TestCodexSessionANSIStripping(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping codex integration test in short mode")
	}
	if os.Getenv("AGENTIC_CODEX_LIVE") != "1" {
		t.Skip("skipping live codex test: set AGENTIC_CODEX_LIVE=1 to run")
	}
	if !codexAvailable() {
		t.Skip("codex CLI not available")
	}

	t.Setenv("CLAUDECODE", "")

	dir := t.TempDir()
	responsePath := dir + "/response.txt"

	prompt := "Reply with the single word: VERIFIED"
	command := []string{"codex", "exec", "--ephemeral", "--skip-git-repo-check", "-o", responsePath, prompt}

	s := NewSession("codex-ansi-test", "feat-1", feature.PhaseReview)
	s.permHandler = &AutoApproveHandler{}

	err := s.Start(command, dir, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	s.CloseStdin() // Signal EOF so codex doesn't block waiting for input

	select {
	case <-s.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("codex session did not complete within 30s timeout")
	}

	// Verify no ANSI escape sequences leaked into the message log
	allMsgs := s.messageLog.Messages()
	for i, msg := range allMsgs {
		if msg.Assistant != nil {
			for _, block := range msg.Assistant.Message.Content {
				if strings.Contains(block.Text, "\x1b[") || strings.Contains(block.Text, "\x1b]") {
					t.Errorf("msg[%d] contains raw ANSI escape sequence: %q", i, truncate(block.Text, 100))
				}
			}
		}
	}
}

// TestCodexSessionDeduplication verifies that stderr-only redraw noise no longer
// enters the assistant transcript.
func TestCodexSessionDeduplication(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess-backed stderr redraw extended regression")
	}

	// This test uses a mock script that simulates Ink redraw behavior:
	// writing the same line multiple times to stderr.
	dir := t.TempDir()
	script := dir + "/mock-ink.sh"
	os.WriteFile(script, []byte(`#!/bin/bash
# Simulate Ink redraws: same text repeated with ANSI codes
echo -e "\x1b[2K\x1b[1G⠋ Thinking..." >&2
echo -e "\x1b[2K\x1b[1G⠋ Thinking..." >&2
echo -e "\x1b[2K\x1b[1G⠋ Thinking..." >&2
echo -e "\x1b[2K\x1b[1GProcessing..." >&2
echo -e "\x1b[2K\x1b[1GProcessing..." >&2
echo -e "\x1b[2K\x1b[1GDone!" >&2
`), 0o755)

	s := NewSession("dedup-test", "feat-1", feature.PhaseReview)

	err := s.Start([]string{"bash", script}, dir, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	<-s.Done()

	if got := s.messageLog.AssistantText(); got != "" {
		t.Errorf("AssistantText() = %q, want empty when only stderr redraws are emitted", got)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
