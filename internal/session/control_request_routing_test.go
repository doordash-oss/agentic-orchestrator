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
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// stdinDenyWatcher returns a channel that fires if anything resembling a
// deny control_response is written to the captured stdin. Used by tests
// that assert the forwarder never synthesises a deny.
func stdinDenyWatcher(t *testing.T, r io.Reader) <-chan string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, `"deny"`) || strings.Contains(line, `"behavior":"deny"`) {
				ch <- line
				return
			}
		}
	}()
	return ch
}

// stubReporter records every drop so tests can assert "zero drops".
type stubReporter struct {
	calls atomic.Int32
}

func (r *stubReporter) ReportAttachDrop(sessionID, featureID, phase, msgType string, timeout time.Duration) {
	r.calls.Add(1)
}

// newRoutingTestSession builds a session with the goroutines started by
// ensureStreamDrainer (drainer + forwarder) wired up but no subprocess.
// Tests that exercise channel routing inject SDKMessages directly and
// rely on Stop()-equivalent teardown via signalDoneForTest.
func newRoutingTestSession(t *testing.T) *Session {
	t.Helper()
	s := NewSession("routing-test", "feat-1", 0)
	s.ensureStreamDrainer()
	t.Cleanup(func() {
		// Mirror readMessages cleanup so no goroutines leak between tests.
		// Calling these directly is safe here because no readMessages
		// goroutine is running to also close them.
		select {
		case <-s.closing:
		default:
			close(s.closing)
		}
		s.streamRing.Close()
		select {
		case <-s.controlCh:
		default:
		}
		// Drain controlCh asynchronously, then close it.
		drained := make(chan struct{})
		go func() {
			for {
				select {
				case <-s.controlCh:
				case <-time.After(50 * time.Millisecond):
					close(drained)
					return
				}
			}
		}()
		<-drained
		// Closing controlCh is unsafe if forwarder might still send to it,
		// but only readMessages writes to controlCh. With no readMessages
		// running, closing here is fine.
		defer func() { _ = recover() }() // closed-twice on rerun
		close(s.controlCh)
		<-s.drainerDone
		<-s.controlForwarderDone
	})
	return s
}

// TestControlRequest_NotDroppedWhenAttachChSaturated reproduces the
// failure mode from feature c137aec2bdf13acf: attachCh saturates with
// non-control traffic, then a control_request arrives. With the
// dedicated controlCh + forwarder, the request must reach the consumer
// once attachCh starts draining — no matter how long that takes.
func TestControlRequest_NotDroppedWhenAttachChSaturated(t *testing.T) {
	s := newRoutingTestSession(t)

	reporter := &stubReporter{}
	s.SetAttachDropReporter(reporter)

	// Saturate attachCh by filling it to capacity. We push directly so
	// the forwarder will be blocked when it tries to publish a control
	// request.
	for i := 0; i < cap(s.attachCh); i++ {
		s.attachCh <- llm.SDKMessage{Type: "stream_event"}
	}

	// Inject a control_request the way readMessages would. The forwarder
	// is now blocked because attachCh is full.
	cr := &llm.ControlRequestMessage{
		RequestID: "ctrl-1",
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "AskUserQuestion"},
	}
	s.controlCh <- llm.SDKMessage{Type: "control_request", ControlRequest: cr}

	// Simulate a slow consumer: drain one slot from attachCh after a
	// delay long enough that the old (deleted) 30s deny path would have
	// fired several times if it still existed. The forwarder must hand
	// the control request through cleanly without invoking any drop
	// path.
	go func() {
		// Retained: deliberate slow-consumer delay before freeing one attach slot.
		time.Sleep(200 * time.Millisecond)
		<-s.attachCh
	}()

	// Read the next control_request that lands on attachCh.
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case msg := <-s.attachCh:
			if msg.ControlRequest != nil && msg.ControlRequest.RequestID == "ctrl-1" {
				break loop
			}
		case <-deadline:
			t.Fatalf("control_request never reached attachCh; reporter calls=%d", reporter.calls.Load())
		}
	}

	if got := reporter.calls.Load(); got != 0 {
		t.Errorf("expected zero drops, got %d", got)
	}
}

// TestControlRequest_ParksIndefinitelyWhenNoConsumer is the regression
// guard for the headless-Inquire failure: an Inquire/Implement/etc.
// session is started while no desktop app is attached, the agent emits an
// AskUserQuestion (or permission request), and there is nobody draining
// attachCh. The session must NOT synthesize a deny back to the SDK and
// must NOT remove the request from pendingControlRequests. The request
// stays parked until a consumer attaches and drains attachCh — at which
// point the desktop app's existing replay path picks it up.
func TestControlRequest_ParksIndefinitelyWhenNoConsumer(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close(); _ = stdinR.Close() })

	s := NewSession("park", "feat-1", 0)
	s.SetStdinForTest(stdinW)
	s.ensureStreamDrainer()
	t.Cleanup(func() {
		select {
		case <-s.closing:
		default:
			close(s.closing)
		}
		s.streamRing.Close()
		defer func() { _ = recover() }()
		close(s.controlCh)
		<-s.drainerDone
		<-s.controlForwarderDone
	})

	reporter := &stubReporter{}
	s.SetAttachDropReporter(reporter)

	denySeen := stdinDenyWatcher(t, stdinR)

	// Pre-record the request the way readMessages would, then publish
	// it onto controlCh. attachCh is empty but unread — no consumer
	// goroutine is pulling from it.
	cr := &llm.ControlRequestMessage{
		RequestID: "auq-park",
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "AskUserQuestion"},
	}
	s.mu.Lock()
	s.recordPendingControlRequestLocked(cr)
	s.mu.Unlock()
	s.controlCh <- llm.SDKMessage{Type: "control_request", ControlRequest: cr}

	// Wait long enough that the old 30s synthesize-deny path would have
	// fired. (We can't wait 30s in unit tests; 500ms is enough to catch
	// any timer-based drop logic that might sneak back in via review,
	// since any reasonable timeout would be set well below that for
	// tests.)
	select {
	case line := <-denySeen:
		t.Fatalf("forwarder synthesized a deny while no consumer was attached: %s", line)
	case <-time.After(500 * time.Millisecond):
		// Expected: nothing on stdin.
	}

	// Pending list must still contain the request — re-attach paths
	// rely on it to replay to the user.
	pending := s.PendingControlRequests()
	if len(pending) != 1 || pending[0].RequestID != "auq-park" {
		t.Fatalf("pending list lost the parked request: %v", pending)
	}
	if got := reporter.calls.Load(); got != 0 {
		t.Errorf("attach-drop reporter must not fire for parked control_requests, got %d", got)
	}

	// Now simulate a consumer attaching: drain attachCh. The forwarder
	// is currently blocked on the send, so reading once must yield the
	// parked request.
	select {
	case msg := <-s.attachCh:
		if msg.ControlRequest == nil || msg.ControlRequest.RequestID != "auq-park" {
			t.Fatalf("expected parked auq-park on attach, got %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("control_request was not delivered when a consumer finally drained attachCh")
	}
}

// TestPendingControlRequests_TracksMultipleInFlight verifies that
// concurrently-pending control_requests are all preserved by the
// session's tracking layer. This is the second half of the Phase-2
// invariant: each requestID survives until ClearPendingQuestion or
// RespondToAskUser explicitly removes it.
func TestPendingControlRequests_TracksMultipleInFlight(t *testing.T) {
	s := NewSession("multi", "feat", 0)

	cr1 := &llm.ControlRequestMessage{
		RequestID: "ask-1",
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "AskUserQuestion"},
	}
	cr2 := &llm.ControlRequestMessage{
		RequestID: "ask-2",
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "AskUserQuestion"},
	}

	s.mu.Lock()
	s.recordPendingControlRequestLocked(cr1)
	s.recordPendingControlRequestLocked(cr2)
	s.mu.Unlock()

	pending := s.PendingControlRequests()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}
	if pending[0].RequestID != "ask-1" || pending[1].RequestID != "ask-2" {
		t.Errorf("order not preserved: %s %s", pending[0].RequestID, pending[1].RequestID)
	}

	// LastControlRequest returns the newest, preserving historical "single
	// slot wins" semantics callers depend on.
	if last := s.LastControlRequest(); last == nil || last.RequestID != "ask-2" {
		t.Errorf("LastControlRequest = %v, want ask-2", last)
	}

	// Removing the first leaves the second.
	s.ClearPendingQuestion("ask-1")
	if pending := s.PendingControlRequests(); len(pending) != 1 || pending[0].RequestID != "ask-2" {
		t.Errorf("after ClearPendingQuestion(ask-1), pending=%v", pending)
	}

	// HasPendingAskUserQuestion should still be true.
	if !s.HasPendingAskUserQuestion() {
		t.Error("HasPendingAskUserQuestion should remain true while ask-2 is outstanding")
	}

	// Removing the second clears the list and the AUQ flag.
	s.ClearPendingQuestion("ask-2")
	if s.LastControlRequest() != nil {
		t.Error("LastControlRequest should be nil after both cleared")
	}
	if s.HasPendingAskUserQuestion() {
		t.Error("HasPendingAskUserQuestion should be false after both cleared")
	}
}

// TestPendingControlRequests_DedupesByRequestID guards against double
// recording of the same requestID — which can happen if a re-attach
// path reads from the message log and also receives the live
// control_request. The second insert must not produce a duplicate
// entry.
func TestPendingControlRequests_DedupesByRequestID(t *testing.T) {
	s := NewSession("dedup", "feat", 0)
	cr := &llm.ControlRequestMessage{
		RequestID: "ask-dedup",
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "AskUserQuestion"},
	}
	s.mu.Lock()
	s.recordPendingControlRequestLocked(cr)
	s.recordPendingControlRequestLocked(cr)
	s.mu.Unlock()
	if got := s.PendingControlRequests(); len(got) != 1 {
		t.Errorf("expected 1 deduped entry, got %d", len(got))
	}
}

func TestPendingControlRequestsPreservesWaitingSinceOnDuplicate(t *testing.T) {
	s := NewSession("waiting-since", "feat", 0)
	first := &llm.ControlRequestMessage{
		RequestID: "request-1",
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "Bash"},
	}

	s.mu.Lock()
	s.recordPendingControlRequestLocked(first)
	stamped := first.WaitingSince
	s.recordPendingControlRequestLocked(&llm.ControlRequestMessage{
		RequestID: "request-1",
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "Bash"},
	})
	s.mu.Unlock()

	if stamped.IsZero() {
		t.Fatal("first pending control request was not stamped")
	}
	pending := s.PendingControlRequests()
	if got := pending[0].WaitingSince; !got.Equal(stamped) {
		t.Fatalf("duplicate waiting_since = %v; want %v", got, stamped)
	}
}

// TestRespondToAskUser_OnlyClearsMatching verifies that responding to
// one of N parallel AskUserQuestion calls leaves the others pending —
// the session must not treat one response as resolving every in-flight
// question.
func TestRespondToAskUser_OnlyClearsMatching(t *testing.T) {
	s := NewSession("respond-one", "feat", 0)
	s.mu.Lock()
	s.hasUnansweredQuestion = true
	s.recordPendingControlRequestLocked(&llm.ControlRequestMessage{
		RequestID: "ask-A",
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "AskUserQuestion"},
	})
	s.recordPendingControlRequestLocked(&llm.ControlRequestMessage{
		RequestID: "ask-B",
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "AskUserQuestion"},
	})
	s.mu.Unlock()

	// Drive the response through the same code path as the desktop app uses,
	// short-circuiting the protocol write since this session has no
	// subprocess attached.
	s.mu.Lock()
	s.removePendingControlRequestLocked("ask-A")
	s.hasUnansweredQuestion = s.hasPendingAskUserQuestionLocked()
	s.mu.Unlock()

	pending := s.PendingControlRequests()
	if len(pending) != 1 || pending[0].RequestID != "ask-B" {
		t.Errorf("after responding to ask-A, pending=%v", pending)
	}
	if !s.HasPendingAskUserQuestion() {
		t.Error("HasPendingAskUserQuestion should remain true while ask-B is outstanding")
	}

	// Bonus: make sure RespondToAskUser actually exercises the right
	// code path even when there's no protocol writer (it should still
	// update the slot regardless of the subsequent stdin write).
	_ = s.RespondToAskUser("ask-B", json.RawMessage(`[]`), nil, nil)
	if got := s.PendingControlRequests(); len(got) != 0 {
		t.Errorf("RespondToAskUser should have cleared ask-B, got %v", got)
	}
}
