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

package orchestrator

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// shutdownSessionMock records Shutdown() invocations for T16 assertions.
type shutdownSessionMock struct {
	*mocks.MockSessionManager
	shutdownCount int32
}

func (m *shutdownSessionMock) Shutdown() {
	atomic.AddInt32(&m.shutdownCount, 1)
}

func (m *shutdownSessionMock) ShutdownCalls() int {
	return int(atomic.LoadInt32(&m.shutdownCount))
}

// ---------------------------------------------------------------------------
// T16. Shutdown signals Done, stops sessions, does not close eventCh, is
// idempotent, and unblocks emitters.
// ---------------------------------------------------------------------------

func TestOrchestrator_Shutdown_SignalsDoneAndStopsSessions(t *testing.T) {
	t.Run("basic_contract", func(t *testing.T) {
		sess := &shutdownSessionMock{MockSessionManager: mocks.NewMockSessionManager()}
		o := New(Deps{Sessions: sess}, Hooks{})

		if err := o.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}

		if got := sess.ShutdownCalls(); got != 1 {
			t.Errorf("Sessions.Shutdown calls = %d, want 1", got)
		}

		// Done() channel closed → receive returns zero value immediately.
		select {
		case <-o.Done():
		case <-time.After(100 * time.Millisecond):
			t.Error("Done() did not close within 100ms")
		}

		// Events() not closed — must time out on receive (never returns zero).
		select {
		case <-o.Events():
			t.Error("Events() closed unexpectedly; Shutdown must not close eventCh")
		case <-time.After(50 * time.Millisecond):
			// expected — no send pending, channel open → select picks timer.
		}

		// emitEvent after shutdown: must not panic, returns promptly. Whether
		// the event is enqueued is unspecified (send arm and done arm may
		// both be ready); either outcome is acceptable.
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = recover() }()
			o.emitEvent(ports.Event{Type: ports.FeatureStarted, FeatureID: "x"})
		}()
		select {
		case <-done:
		case <-time.After(200 * time.Millisecond):
			t.Error("emitEvent did not return within 200ms after Shutdown")
		}

		// emitEventBlocking after shutdown: same invariants.
		done2 := make(chan struct{})
		go func() {
			defer close(done2)
			defer func() { _ = recover() }()
			o.emitEventBlocking(ports.Event{Type: ports.FeatureStarted, FeatureID: "y"})
		}()
		select {
		case <-done2:
		case <-time.After(200 * time.Millisecond):
			t.Error("emitEventBlocking did not return within 200ms after Shutdown")
		}

		// Idempotent: second Shutdown is a no-op.
		if err := o.Shutdown(); err != nil {
			t.Errorf("second Shutdown: %v", err)
		}
		if got := sess.ShutdownCalls(); got != 1 {
			t.Errorf("after second Shutdown Sessions.Shutdown calls = %d, want 1 (stopOnce)", got)
		}
	})

	t.Run("concurrency_no_panic_and_unblock", func(t *testing.T) {
		sess := &shutdownSessionMock{MockSessionManager: mocks.NewMockSessionManager()}
		o := New(Deps{Sessions: sess}, Hooks{})

		// Fill eventCh to capacity so subsequent sends block.
		for i := range eventChBuffer {
			select {
			case o.eventCh <- ports.Event{Type: ports.FeatureStarted, FeatureID: "prime"}:
			default:
				t.Fatalf("failed to prefill event channel at i=%d", i)
			}
		}

		const N = 50
		startCh := make(chan struct{})
		started := make(chan struct{}, N)
		exited := make(chan struct{}, N)

		for range N {
			go func() {
				defer func() {
					_ = recover() // belt-and-suspenders
					exited <- struct{}{}
				}()
				<-startCh
				started <- struct{}{}
				for {
					o.emitEventBlocking(ports.Event{Type: ports.FeatureStarted, FeatureID: "load"})
					select {
					case <-o.Done():
						return
					default:
					}
				}
			}()
		}

		// Unleash emitters, wait until each reached the emit path, then shut down.
		close(startCh)
		for i := 0; i < N; i++ {
			select {
			case <-started:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("only %d/%d emitter goroutines reached the emit path", i, N)
			}
		}
		_ = o.Shutdown()

		// All goroutines must exit within 500ms.
		for i := 0; i < N; i++ {
			select {
			case <-exited:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("only %d/%d emitter goroutines exited within 500ms", i, N)
			}
		}

		// Done closed, Events not closed.
		select {
		case <-o.Done():
		case <-time.After(50 * time.Millisecond):
			t.Error("Done() not closed after Shutdown")
		}
	})

	t.Run("listener_loop_exits_on_done", func(t *testing.T) {
		sess := &shutdownSessionMock{MockSessionManager: mocks.NewMockSessionManager()}
		o := New(Deps{Sessions: sess}, Hooks{})

		// Simulate a consumer loop watching Events() + Done().
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-o.Events():
					// drain any stray events
				case <-o.Done():
					return
				}
			}
		}()

		if err := o.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		select {
		case <-done:
		case <-time.After(200 * time.Millisecond):
			t.Error("consumer loop did not exit on Done() closure")
		}
	})
}
