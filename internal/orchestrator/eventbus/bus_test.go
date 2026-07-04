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

package eventbus

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestSingleSubscriberOrderedDelivery(t *testing.T) {
	b := New()
	sub := b.Subscribe("test", 16)
	defer sub.Cancel()

	const n = 10
	go func() {
		for i := 0; i < n; i++ {
			b.Publish(ports.Event{Type: ports.PhaseStarted, FeatureID: "f", Message: itoa(i)})
		}
	}()

	for i := 0; i < n; i++ {
		select {
		case ev := <-sub.C:
			if ev.Message != itoa(i) {
				t.Fatalf("event %d out of order: got %q want %q", i, ev.Message, itoa(i))
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
}

func TestMultipleSubscribersAllReceive(t *testing.T) {
	b := New()
	a := b.Subscribe("a", 16)
	defer a.Cancel()
	c := b.Subscribe("c", 16)
	defer c.Cancel()

	b.Publish(ports.Event{Type: ports.FeatureCreated, FeatureID: "f1"})

	for name, sub := range map[string]*Subscription{"a": a, "c": c} {
		select {
		case ev := <-sub.C:
			if ev.FeatureID != "f1" {
				t.Errorf("%s: got FeatureID=%q, want f1", name, ev.FeatureID)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s: timed out", name)
		}
	}
}

func TestSlowSubscriberSeesDrops(t *testing.T) {
	b := New()
	fast := b.Subscribe("fast", 1024)
	defer fast.Cancel()
	slow := b.Subscribe("slow", 4)
	defer slow.Cancel()

	const n = 1000
	for i := 0; i < n; i++ {
		b.Publish(ports.Event{Type: ports.PhaseStarted, Message: itoa(i)})
	}

	if slow.Stats().Dropped == 0 {
		t.Error("slow subscriber should have seen drops")
	}
	// fast keeps up easily
	if fast.Stats().Dropped != 0 {
		t.Errorf("fast subscriber dropped %d events, want 0", fast.Stats().Dropped)
	}

	bus := b.Stats()
	if bus.Dropped == 0 {
		t.Error("bus-wide drop counter should be non-zero")
	}
}

func TestCancelledSubscriberStopsReceiving(t *testing.T) {
	b := New()
	sub := b.Subscribe("temp", 16)

	b.Publish(ports.Event{Type: ports.PhaseStarted, Message: "first"})
	select {
	case ev := <-sub.C:
		if ev.Message != "first" {
			t.Fatalf("want first, got %q", ev.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out reading first event")
	}

	sub.Cancel()

	// Publishing after cancel must not panic; subscriber count drops.
	b.Publish(ports.Event{Type: ports.PhaseStarted, Message: "ignored"})
	if got := b.SubscriberCount(); got != 0 {
		t.Errorf("subscriber count = %d after cancel, want 0", got)
	}

	// Cancel is idempotent.
	sub.Cancel()
	sub.Cancel()

	// Channel must be closed (drain returns zero value + !ok).
	if _, ok := <-sub.C; ok {
		t.Error("subscription channel should be closed after Cancel")
	}
}

func TestConcurrentPublishSubscribeCancel(t *testing.T) {
	b := New()
	const events = 5000
	const subs = 50

	var wg sync.WaitGroup
	var totalReceived atomic.Uint64

	// publisher
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < events; i++ {
			b.Publish(ports.Event{Type: ports.PhaseStarted, Message: itoa(i)})
		}
	}()

	// churning subscribers
	for i := 0; i < subs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := b.Subscribe("churn", 8)
			defer s.Cancel()
			// Drain a few then cancel — exercises subscribe/publish/cancel races.
			for j := 0; j < 20; j++ {
				select {
				case _, ok := <-s.C:
					if !ok {
						return
					}
					totalReceived.Add(1)
				case <-time.After(50 * time.Millisecond):
					return
				}
			}
		}()
	}

	wg.Wait()
	// We don't assert exact counts (race-dependent), only that we
	// neither deadlocked nor panicked. The Stats() snapshot must be
	// consistent: bus-wide drops can legitimately exceed sent because
	// each Publish loop can drop on up to N subscribers (so the
	// invariant is drops <= sent * subscriberCount), and per-subscriber
	// drops must never exceed sent.
	st := b.Stats()
	for _, sub := range st.Subscribers {
		if sub.Dropped > st.Sent {
			t.Errorf("per-subscriber drops (%d) > sent (%d) for %q — counter corruption",
				sub.Dropped, st.Sent, sub.Name)
		}
	}
}

func TestStatsReportsSubscriberOccupancy(t *testing.T) {
	b := New()
	sub := b.Subscribe("blocked", 4)
	defer sub.Cancel()

	for i := 0; i < 3; i++ {
		b.Publish(ports.Event{Type: ports.PhaseStarted})
	}

	st := b.Stats()
	if len(st.Subscribers) != 1 {
		t.Fatalf("subscribers = %d, want 1", len(st.Subscribers))
	}
	got := st.Subscribers[0]
	if got.Name != "blocked" {
		t.Errorf("name = %q", got.Name)
	}
	if got.Cap != 4 {
		t.Errorf("cap = %d, want 4", got.Cap)
	}
	if got.Len != 3 {
		t.Errorf("len = %d, want 3", got.Len)
	}
}

func TestZeroBufferUsesDefault(t *testing.T) {
	b := New()
	sub := b.Subscribe("default", 0)
	defer sub.Cancel()
	if cap(sub.C) != DefaultBuffer {
		t.Errorf("cap = %d, want %d (default)", cap(sub.C), DefaultBuffer)
	}
}

// itoa avoids strconv for a smaller fixture surface.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [16]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
