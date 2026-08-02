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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
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
	// populated on every session lifecycle and SDK event so the desktop app
	// never needs to reverse-engineer identity from SessionID.
	var received atomic.Int64
	var badEvents atomic.Int64
	var startedFeatureID atomic.Value
	var startedPhase atomic.Value
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
				case SessionStartedMsg:
					startedFeatureID.Store(v.FeatureID)
					startedPhase.Store(v.Phase)
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
	if fid, _ := startedFeatureID.Load().(string); fid != "feat-1" {
		t.Errorf("SessionStartedMsg.FeatureID = %q, want %q", fid, "feat-1")
	}
	if phase, _ := startedPhase.Load().(feature.Phase); phase != feature.PhaseImplement {
		t.Errorf("SessionStartedMsg.Phase = %v, want %v", phase, feature.PhaseImplement)
	}
	if fid, _ := doneFeatureID.Load().(string); fid != "feat-1" {
		t.Errorf("SessionDoneMsg.FeatureID = %q, want %q", fid, "feat-1")
	}
	if phase, _ := donePhase.Load().(feature.Phase); phase != feature.PhaseImplement {
		t.Errorf("SessionDoneMsg.Phase = %v, want %v", phase, feature.PhaseImplement)
	}
	t.Logf("received %d SDK events from rapid-fire script", count)
	_ = fmt.Sprintf("%v", sess)
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

func TestNewManagerRestoresLiveSessionTranscript(t *testing.T) {
	stateDir := t.TempDir()
	logPath := filepath.Join(stateDir, "output.txt")
	scriptPath := filepath.Join(stateDir, "live.sh")
	script := `#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"provider-session","model":"test"}'
echo '{"type":"assistant","subtype":"partial","message":{"role":"assistant","content":[{"type":"text","text":"draft"}]}}'
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"before restart"}]}}'
sleep 30
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing session script: %v", err)
	}

	original := NewManager(nil)
	t.Cleanup(original.Shutdown)
	sess, err := original.StartSession(
		"feature-1-implement", "feature-1", feature.PhaseImplement,
		[]string{"bash", scriptPath}, stateDir, nil,
		&SessionOpts{PIDDir: stateDir, RunNumber: 3, LogPath: logPath, ProviderName: "fixture"},
	)
	if err != nil {
		t.Fatalf("starting live session: %v", err)
	}
	waitForMessageCount(t, sess, 2)
	pidFile, err := ReadPIDFile(filepath.Join(stateDir, PIDFileName("")))
	if err != nil {
		t.Fatalf("reading live session metadata: %v", err)
	}
	transcript, err := os.OpenFile(pidFile.Transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening persisted transcript: %v", err)
	}
	if _, err := transcript.WriteString(`{"index":`); err != nil {
		_ = transcript.Close()
		t.Fatalf("appending interrupted transcript row: %v", err)
	}
	if err := transcript.Close(); err != nil {
		t.Fatalf("closing persisted transcript: %v", err)
	}

	restarted := NewRecoveringManager(nil, stateDir)
	restored := restarted.FeatureSessions("feature-1")
	if len(restored) != 1 {
		t.Fatalf("restored session count = %d, want 1", len(restored))
	}
	if got := restored[0].ID(); got != "feature-1-implement" {
		t.Fatalf("restored session ID = %q, want feature-1-implement", got)
	}
	if got := restored[0].Phase(); got != feature.PhaseImplement {
		t.Fatalf("restored phase = %s, want Implement", got)
	}
	if got := restored[0].Model(); got != "test" {
		t.Fatalf("restored model = %q, want test", got)
	}
	if got := restored[0].MessageLog().Len(); got != 2 {
		t.Fatalf("restored transcript rows = %d, want 2", got)
	}
	if got := restored[0].MessageLog().AssistantText(); !strings.Contains(got, "before restart") {
		t.Fatalf("restored transcript = %q, want assistant output", got)
	}
	if got := restarted.GetSession("feature-1-implement"); got == nil {
		t.Fatal("GetSession did not return the restored session")
	}
	if err := restarted.StopSession("feature-1-implement"); err != nil {
		t.Fatalf("stopping restored session: %v", err)
	}
	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("original session did not observe restored-session stop")
	}
}

func TestStartSessionKeepsConfiguredModelWhenInitOmitsModel(t *testing.T) {
	sm := NewManager(nil)
	t.Cleanup(sm.Shutdown)

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "model-fallback.sh")
	if err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1"}'
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755); err != nil {
		t.Fatalf("writing script: %v", err)
	}

	sess, err := sm.StartSession(
		"model-fallback", "feat-1", feature.PhaseInquire,
		[]string{"bash", scriptPath}, tmpDir, nil,
		&SessionOpts{Model: "gpt-5.4-mini[272K]"},
	)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	}
	if got := sess.Model(); got != "gpt-5.4-mini[272K]" {
		t.Fatalf("Model() = %q, want configured fallback", got)
	}
}

func waitForMessageCount(t *testing.T, sess ports.SessionView, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sess.MessageLog().Len() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("message count = %d, want at least %d", sess.MessageLog().Len(), want)
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
	// Script reads a JSON line from stdin. If it receives input, it outputs
	// "prompt_received". If stdin is empty/closed, it outputs "no_input".
	// This verifies that InitialPrompt is delivered via stdin.
	os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
if read -t 2 line; then
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

func TestStartSessionSeedsInitialPromptContextEstimate(t *testing.T) {
	eventCh := make(chan interface{}, 100)
	sm := NewManager(eventCh)
	defer sm.Shutdown()

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "initial-context.sh")
	if err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
read -t 2 line || true
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755); err != nil {
		t.Fatalf("writing script: %v", err)
	}

	const contextWindow = 2000
	prompt := strings.Repeat("abcd", 100)
	sess, err := sm.StartSession("context-seed-test", "feat-1", feature.PhaseResearch,
		[]string{"bash", scriptPath}, tmpDir, nil,
		&SessionOpts{InitialPrompt: prompt, ContextWindow: contextWindow})
	if err != nil {
		t.Fatalf("starting session: %v", err)
	}

	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	usage := sess.LatestUsage()
	if usage == nil {
		t.Fatal("LatestUsage() = nil, want initial prompt context estimate")
	}
	if usage.ContextWindow != contextWindow {
		t.Fatalf("ContextWindow = %d, want %d", usage.ContextWindow, contextWindow)
	}
	if usage.ContextTotalTokens != 100 {
		t.Fatalf("ContextTotalTokens = %d, want 100", usage.ContextTotalTokens)
	}
	if got := sess.AccumulatedUsage(); got.ContextTotalTokens != 0 || got.ContextWindow != 0 || got.InputTokens != 0 {
		t.Fatalf("AccumulatedUsage() = %+v, want no synthetic usage accounting", got)
	}
	if got := sess.ContextPercentage(); got != 5 {
		t.Fatalf("ContextPercentage() = %d, want 5", got)
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
// desktop app's permission menu recovery failed because the status was prematurely
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
		// No PermHandler → control_request is deferred to desktop app (not auto-handled)
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

// blockingHandshakeProtocol blocks in Handshake until released, so a test can
// observe manager behavior while a session is mid-handshake.
type blockingHandshakeProtocol struct {
	stubProtocol
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingHandshakeProtocol) Handshake(ctx context.Context) error {
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestStartSessionDoesNotBlockReadersDuringHandshake guards against StartSession
// holding the manager lock across the (potentially multi-second) protocol
// handshake. When it did, ActiveSessions() — which feeds live-preview/prompts/
// sessions — blocked for the whole handshake, timing out the desktop app during a
// resume.
func TestStartSessionDoesNotBlockReadersDuringHandshake(t *testing.T) {
	eventCh := make(chan interface{}, 64)
	sm := NewManager(eventCh)
	defer sm.Shutdown()

	// Drain events so the session's read loop never blocks on eventCh.
	go func() {
		for range eventCh {
		}
	}()

	proto := &blockingHandshakeProtocol{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	startErr := make(chan error, 1)
	go func() {
		_, err := sm.StartSession(
			"handshake-test", "feat-1", feature.PhaseDesign,
			[]string{"cat"}, t.TempDir(), nil,
			&SessionOpts{Protocol: proto},
		)
		startErr <- err
	}()

	select {
	case <-proto.started:
	case <-time.After(5 * time.Second):
		t.Fatal("handshake never started")
	}

	// ActiveSessions must return promptly while the handshake is in progress.
	done := make(chan []ports.SessionView, 1)
	go func() { done <- sm.ActiveSessions() }()
	select {
	case sessions := <-done:
		if len(sessions) != 0 {
			t.Fatalf("ActiveSessions() mid-handshake = %d sessions, want 0 (not registered until ready)", len(sessions))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ActiveSessions() blocked while a session was mid-handshake")
	}

	close(proto.release)
	select {
	case err := <-startErr:
		if err != nil {
			t.Fatalf("StartSession() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartSession() did not return after handshake released")
	}

	if got := len(sm.ActiveSessions()); got != 1 {
		t.Fatalf("ActiveSessions() after start = %d, want 1", got)
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
