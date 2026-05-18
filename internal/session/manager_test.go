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
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestSDKEventDelivery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	eventCh := make(chan interface{}, 100)
	sm := NewManager(eventCh)
	defer sm.Shutdown()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "rapid.sh")
	// Script outputs multiple JSONL messages
	script := `#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
for i in $(seq 1 20); do
  echo "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"msg $i\"}]}}"
done
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.05}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing script: %v", err)
	}

	sess, err := sm.StartSession(
		"rapid-test", "feat-1", feature.PhaseImplement,
		[]string{"bash", scriptPath}, tmpDir, nil,
	)
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	// Drain events in a separate goroutine. Assert FeatureID/Phase are
	// populated on every SDKEventMsg and on the SessionDoneMsg so the TUI
	// never needs to reverse-engineer identity from SessionID.
	var received atomic.Int64
	var badEvents atomic.Int64
	var doneFeatureID atomic.Value
	var donePhase atomic.Value
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		timeout := time.After(10 * time.Second)
		var drainDeadline <-chan time.Time
		for {
			select {
			case evt, ok := <-eventCh:
				if !ok {
					return
				}
				switch v := evt.(type) {
				case SDKEventMsg:
					received.Add(1)
					if v.FeatureID != "feat-1" || v.Phase != feature.PhaseImplement {
						badEvents.Add(1)
					}
				case SessionDoneMsg:
					doneFeatureID.Store(v.FeatureID)
					donePhase.Store(v.Phase)
					drainDeadline = time.After(500 * time.Millisecond)
				}
			case <-drainDeadline:
				return
			case <-timeout:
				return
			}
		}
	}()

	sess.Wait()
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
	}

	count := received.Load()
	// Should receive init + 20 assistant + result = 22 SDK events
	if count < 10 {
		t.Errorf("expected at least 10 SDK events delivered (got %d)", count)
	}
	if n := badEvents.Load(); n > 0 {
		t.Errorf("%d SDKEventMsgs had missing/incorrect FeatureID or Phase", n)
	}
	if fid, _ := doneFeatureID.Load().(string); fid != "feat-1" {
		t.Errorf("SessionDoneMsg.FeatureID = %q, want %q", fid, "feat-1")
	}
	if phase, _ := donePhase.Load().(feature.Phase); phase != feature.PhaseImplement {
		t.Errorf("SessionDoneMsg.Phase = %v, want %v", phase, feature.PhaseImplement)
	}
	t.Logf("received %d SDK events from rapid-fire script", count)
}

func TestSessionStatusTransitions(t *testing.T) {
	// Test the status transitions via direct session manipulation
	s := NewSession("unit-latch", "feat-1", feature.PhaseImplement)
	s.status = SessionRunning

	// Simulate control_request → WaitingPermission
	s.mu.Lock()
	s.status = SessionWaitingPermission
	s.mu.Unlock()

	if s.status != SessionWaitingPermission {
		t.Errorf("expected SessionWaitingPermission, got %v", s.status)
	}

	// ResetWaitingStatus should transition to running
	s.ResetWaitingStatus()
	if s.status != SessionRunning {
		t.Errorf("expected SessionRunning after reset, got %v", s.status)
	}

	// WaitingHelp → reset
	s.mu.Lock()
	s.status = SessionWaitingHelp
	s.mu.Unlock()
	s.ResetWaitingStatus()
	if s.status != SessionRunning {
		t.Errorf("expected SessionRunning after reset from WaitingHelp, got %v", s.status)
	}

	// Done should not be reset
	s.mu.Lock()
	s.status = SessionDone
	s.mu.Unlock()
	s.ResetWaitingStatus()
	if s.status != SessionDone {
		t.Errorf("expected SessionDone to not be reset, got %v", s.status)
	}
}

func TestResetWaitingStatus_JSONProtocol(t *testing.T) {
	s := NewSession("suppress-test", "feat-1", feature.PhaseImplement)

	s.mu.Lock()
	s.status = SessionWaitingHelp
	s.mu.Unlock()

	s.ResetWaitingStatus()
	if s.status != SessionRunning {
		t.Fatalf("expected SessionRunning after reset, got %v", s.status)
	}
}

// TestHighVolumePartialStreamNoBoundedGoroutines verifies that high-volume
// partial message streaming does not create unbounded goroutines. With a
// tiny event channel, the non-blocking select/drop approach should complete
// without hanging or leaking goroutines.
func TestHighVolumePartialStreamNoBoundedGoroutines(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Use a very small event channel to force drops under load.
	eventCh := make(chan interface{}, 2)
	sm := NewManager(eventCh)
	defer sm.Shutdown()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "partial.sh")
	// Emit 500 partial messages followed by a final result
	script := `#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
for i in $(seq 1 500); do
  echo "{\"type\":\"assistant\",\"subtype\":\"partial\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"token $i\"}]}}"
done
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"final response"}]}}'
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing script: %v", err)
	}

	sess, err := sm.StartSession(
		"partial-test", "feat-1", feature.PhaseImplement,
		[]string{"bash", scriptPath}, tmpDir, nil,
	)
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	// Slowly drain events — most partials will be dropped due to small channel.
	var received atomic.Int64
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		timeout := time.After(15 * time.Second)
		for {
			select {
			case _, ok := <-eventCh:
				if !ok {
					return
				}
				received.Add(1)
				// Retained in extended stress test: deliberate slow-consumer simulation.
				time.Sleep(time.Millisecond)
			case <-timeout:
				return
			}
		}
	}()

	// The session must complete without hanging (no goroutine accumulation).
	done := make(chan struct{})
	go func() {
		sess.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good — session completed
	case <-time.After(10 * time.Second):
		t.Fatal("session did not complete within timeout — possible goroutine leak")
	}

	// Wait for drain
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
	}

	count := received.Load()
	t.Logf("received %d events out of ~503 (most partials expected to be dropped)", count)
	// We should receive at least a few events (init + result + done + some partials)
	if count < 3 {
		t.Errorf("expected at least 3 events, got %d", count)
	}
}

func TestStartSessionWithInitialPrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	eventCh := make(chan interface{}, 100)
	sm := NewManager(eventCh)
	defer sm.Shutdown()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "initial-prompt.sh")
	// Script reads the legacy no-protocol initialize handshake and then the
	// initial user prompt from stdin.
	os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
if read -t 2 init_line && read -t 2 prompt_line && [[ "$init_line" == *'"subtype":"initialize"'* ]] && [[ "$prompt_line" == *'"content":"Hello from initial prompt"'* ]]; then
  echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"prompt_received"}]}}'
else
  echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"no_input"}]}}'
fi
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755)

	sess, err := sm.StartSession("prompt-test", "feat-1", feature.PhaseResearch,
		[]string{"bash", scriptPath}, tmpDir, nil,
		&SessionOpts{InitialPrompt: "Hello from initial prompt"})
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	output := sess.MessageLog().AssistantText()
	if !strings.Contains(output, "prompt_received") {
		t.Errorf("expected InitialPrompt to be delivered via stdin, got assistant text: %q", output)
	}
}

func TestStartSessionWithoutInitialPrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	eventCh := make(chan interface{}, 100)
	sm := NewManager(eventCh)
	defer sm.Shutdown()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "no-prompt.sh")
	// Without InitialPrompt, stdin should have nothing to read.
	os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}'
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755)

	sess, err := sm.StartSession("noprompt-test", "feat-1", feature.PhaseResearch,
		[]string{"bash", scriptPath}, tmpDir, nil)
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	if sess.Cost() == nil {
		t.Error("expected cost to be captured")
	}
}

func TestManager_StartSessionAfterShutdown(t *testing.T) {
	eventCh := make(chan interface{}, 100)
	sm := NewManager(eventCh)
	sm.Shutdown()

	sess, err := sm.StartSession("post-shutdown", "feat-1", feature.PhaseResearch,
		[]string{"echo", "hello"}, t.TempDir(), nil)
	if err != ErrShuttingDown {
		t.Errorf("expected ErrShuttingDown, got %v", err)
	}
	if sess != nil {
		t.Error("expected nil session after shutdown")
	}
}

func TestManager_SessionDoneMsgNeverDropped(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Use a minimal buffer to force SDK event drops
	eventCh := make(chan interface{}, 1)
	sm := NewManager(eventCh)
	defer sm.Shutdown()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "rapid.sh")
	// Emit 100 messages rapidly to fill the channel
	os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
for i in $(seq 1 100); do
  echo "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"msg $i\"}]}}"
done
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755)

	sess, err := sm.StartSession("done-msg-test", "feat-1", feature.PhaseImplement,
		[]string{"bash", scriptPath}, tmpDir, nil)
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	// Slowly drain events — check that SessionDoneMsg is always delivered
	gotDone := false
	timeout := time.After(10 * time.Second)
loop:
	for {
		select {
		case evt := <-eventCh:
			if _, ok := evt.(SessionDoneMsg); ok {
				gotDone = true
				break loop
			}
		case <-timeout:
			break loop
		}
	}

	_ = sess
	if !gotDone {
		t.Error("SessionDoneMsg was never received despite SDK event drops")
	}
}

// TestWaitingPermissionNotClobberedByAssistantPartial verifies that an
// assistant partial message (e.g. thread/tokenUsage/updated parsed as an
// assistant partial by the Codex protocol) does NOT reset the session status
// from SessionWaitingPermission to SessionRunning while a control request is
// still pending. This was the root cause of a stuck-session bug where the
// TUI's permission menu recovery failed because the status was prematurely
// reset, causing the user to respond via chat (SendUserMessage) instead of
// RespondToControl — leaving the approval request unanswered forever.
func TestWaitingPermissionNotClobberedByAssistantPartial(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	eventCh := make(chan interface{}, 100)
	sm := NewManager(eventCh)
	defer sm.Shutdown()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "perm-clobber.sh")
	// Simulate: control_request for Bash, immediately followed by an
	// assistant partial message (like Codex's thread/tokenUsage/updated).
	os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
# Control request for Bash tool
echo '{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf .cache"}}}'
# Immediate assistant partial (simulates token usage update)
echo '{"type":"assistant","subtype":"partial","message":{"role":"assistant","content":[{"type":"text","text":"usage update"}]}}'
# Keep process alive so we can inspect status
sleep 5
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755)

	sess, err := sm.StartSession("perm-clobber-test", "feat-1", feature.PhaseImplement,
		[]string{"bash", scriptPath}, tmpDir, nil,
		// No PermHandler → control_request is deferred to TUI (not auto-handled)
	)
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	// Wait for messages to be processed
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for SessionWaitingPermission")
		default:
		}
		if sess.Status() == SessionWaitingPermission {
			break
		}
		// Retained in extended manager test: bounded poll for asynchronous status.
		time.Sleep(10 * time.Millisecond)
	}

	// Retained in extended manager test: negative assertion window after the
	// assistant partial is processed.
	time.Sleep(200 * time.Millisecond)

	// The key assertion: status must still be SessionWaitingPermission
	// (not clobbered to SessionRunning by the assistant partial).
	if sess.Status() != SessionWaitingPermission {
		t.Errorf("expected SessionWaitingPermission to be preserved after assistant partial, got %v", sess.Status())
	}

	// Also verify lastControlRequest is still set
	if sess.LastControlRequest() == nil {
		t.Error("expected lastControlRequest to still be set")
	}

	// Now simulate responding to the control request — status should clear
	concrete := sess.(*Session)
	concrete.mu.Lock()
	concrete.clearPendingControlRequestsLocked()
	concrete.status = SessionRunning
	concrete.mu.Unlock()

	if sess.Status() != SessionRunning {
		t.Errorf("expected SessionRunning after clearing control request, got %v", sess.Status())
	}
}

func TestStartSessionWithPipedInput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	eventCh := make(chan interface{}, 100)
	sm := NewManager(eventCh)
	defer sm.Shutdown()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "piped.sh")
	os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755)

	stderrPath := filepath.Join(tmpDir, "stderr.log")
	sess, err := sm.StartSession("piped-test", "feat-1", feature.PhaseReview,
		[]string{"bash", scriptPath}, tmpDir, nil, &SessionOpts{StderrPath: stderrPath})
	if err != nil {
		t.Fatalf("starting piped session: %v", err)
	}

	sess.Wait()

	if got := sess.LogFilePath(); got != "" {
		t.Errorf("LogFilePath() = %q, want empty", got)
	}
	if _, err := os.Stat(stderrPath); err != nil {
		t.Fatalf("expected stderr log file at %s: %v", stderrPath, err)
	}
}
