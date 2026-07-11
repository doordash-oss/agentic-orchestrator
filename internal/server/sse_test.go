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

package server

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// testFeatureID is a fake feature resource ID reused across several broker
// tests in this file.
const testFeatureID = "F-1"

// testEventEpoch is the fake broker epoch reused across broker/replay tests
// in this file and sse_shutdown_test.go.
const testEventEpoch = "epoch-test"

// testEventKindFeatureState is the fake SSEEventDTO.Kind used across broker
// tests in this file and handler_test.go.
const testEventKindFeatureState = "feature.state"

func TestEventBrokerAssignsMonotonicEnvelopeFields(t *testing.T) {
	t.Parallel()

	b := newEventBrokerWithOptions(eventBrokerOptions{Epoch: testEventEpoch, ReplayLimit: 4})
	ch := b.subscribe()
	defer b.unsubscribe(ch)

	b.publish(SSEEventDTO{Kind: testEventKindFeatureState, Resource: ResourceDTO{Type: entityFeature, ID: testFeatureID}})
	b.publish(SSEEventDTO{Kind: testEventKindFeatureState, Resource: ResourceDTO{Type: entityFeature, ID: testFeatureID}})

	first := <-ch
	second := <-ch
	if first.Seq != 1 || first.ID != "1" || first.Epoch != testEventEpoch {
		t.Fatalf("first event envelope = %+v, want seq/id 1 and epoch-test", first)
	}
	if second.Seq != 2 || second.ID != "2" || second.Epoch != testEventEpoch {
		t.Fatalf("second event envelope = %+v, want seq/id 2 and epoch-test", second)
	}
	if first.ResourceVersion != 1 || second.ResourceVersion != 2 {
		t.Fatalf("resource versions = %d, %d; want 1, 2", first.ResourceVersion, second.ResourceVersion)
	}
}

func TestEventBrokerConcurrentPublishAssignsUniqueMonotonicSeqs(t *testing.T) {
	b := newEventBrokerWithOptions(eventBrokerOptions{Epoch: testEventEpoch, ReplayLimit: 512})

	const publishers = 16
	const perPublisher = 32
	var wg sync.WaitGroup
	wg.Add(publishers)
	for p := 0; p < publishers; p++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perPublisher; i++ {
				b.publish(SSEEventDTO{
					Kind:     testEventKindFeatureState,
					Resource: ResourceDTO{Type: entityFeature, ID: fmt.Sprintf("F-%02d", p)},
				})
			}
		}()
	}
	wg.Wait()

	want := uint64(publishers * perPublisher)
	if got := b.currentSeq(); got != want {
		t.Fatalf("current seq = %d, want %d", got, want)
	}
	// after=0 with a full, unevicted ring now replays everything (seq 0 is
	// a genuine low-water-mark, not just a "no cursor" sentinel) — the
	// uniqueness/monotonicity check below is this test's actual point.
	ch, replay, reset := b.subscribeAfter(0, "")
	defer b.unsubscribe(ch)
	if reset != nil {
		t.Fatalf("initial subscribe reset = %+v, want none", *reset)
	}
	if len(replay) != int(want) {
		t.Fatalf("initial replay len = %d, want %d", len(replay), want)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	seen := make(map[uint64]bool, len(b.ring))
	for _, evt := range b.ring {
		if seen[evt.Seq] {
			t.Fatalf("duplicate seq in replay ring: %d", evt.Seq)
		}
		seen[evt.Seq] = true
	}
}

func TestEventBrokerReplayFromCursor(t *testing.T) {
	t.Parallel()

	b := newEventBrokerWithOptions(eventBrokerOptions{Epoch: testEventEpoch, ReplayLimit: 4})
	b.publish(SSEEventDTO{Kind: testEventKindFeatureState, Resource: ResourceDTO{Type: entityFeature, ID: testFeatureID}})
	b.publish(SSEEventDTO{Kind: testEventKindFeatureState, Resource: ResourceDTO{Type: entityFeature, ID: "F-2"}})
	b.publish(SSEEventDTO{Kind: testEventKindFeatureState, Resource: ResourceDTO{Type: entityFeature, ID: "F-3"}})

	ch, replay, reset := b.subscribeAfter(1, testEventEpoch)
	defer b.unsubscribe(ch)
	if reset != nil {
		t.Fatalf("subscribeAfter reset = %+v, want replay", *reset)
	}
	if len(replay) != 2 {
		t.Fatalf("replay len = %d, want 2", len(replay))
	}
	if replay[0].Seq != 2 || replay[1].Seq != 3 {
		t.Fatalf("replay seqs = %d, %d; want 2, 3", replay[0].Seq, replay[1].Seq)
	}
}

func TestEventBrokerStaleCursorReturnsReset(t *testing.T) {
	t.Parallel()

	b := newEventBrokerWithOptions(eventBrokerOptions{Epoch: testEventEpoch, ReplayLimit: 2})
	for i := 0; i < 4; i++ {
		b.publish(SSEEventDTO{Kind: testEventKindFeatureState, Resource: ResourceDTO{Type: entityFeature, ID: testFeatureID}})
	}

	ch, replay, reset := b.subscribeAfter(1, testEventEpoch)
	defer b.unsubscribe(ch)
	if len(replay) != 0 {
		t.Fatalf("replay len = %d, want reset only", len(replay))
	}
	if reset == nil {
		t.Fatal("reset = nil, want stream.reset")
	}
	if reset.Kind != sseEventStreamReset || reset.Seq != 4 || reset.Epoch != testEventEpoch {
		t.Fatalf("reset = %+v, want stream.reset at current seq 4", *reset)
	}
}

func TestEventBrokerReplayRingBoundaries(t *testing.T) {
	t.Parallel()

	b := newEventBrokerWithOptions(eventBrokerOptions{Epoch: testEventEpoch, ReplayLimit: 3})
	for i := 1; i <= 5; i++ {
		b.publish(SSEEventDTO{Kind: testEventKindFeatureState, Resource: ResourceDTO{Type: entityFeature, ID: fmt.Sprintf("F-%d", i)}})
	}

	ch, replay, reset := b.subscribeAfter(2, testEventEpoch)
	defer b.unsubscribe(ch)
	if reset != nil {
		t.Fatalf("exact oldest-1 cursor reset = %+v, want replay", *reset)
	}
	if got := eventSeqs(replay); fmt.Sprint(got) != "[3 4 5]" {
		t.Fatalf("replay seqs from oldest-1 = %v, want [3 4 5]", got)
	}

	ch, replay, reset = b.subscribeAfter(1, testEventEpoch)
	defer b.unsubscribe(ch)
	if reset == nil || len(replay) != 0 {
		t.Fatalf("evicted cursor replay=%+v reset=%+v, want reset only", replay, reset)
	}

	ch, replay, reset = b.subscribeAfter(6, testEventEpoch)
	defer b.unsubscribe(ch)
	if reset == nil || len(replay) != 0 {
		t.Fatalf("future cursor replay=%+v reset=%+v, want reset only", replay, reset)
	}
}

func TestEventBrokerCursorWithoutEpochReturnsReset(t *testing.T) {
	t.Parallel()

	b := newEventBrokerWithOptions(eventBrokerOptions{Epoch: testEventEpoch, ReplayLimit: 4})
	b.publish(SSEEventDTO{Kind: testEventKindFeatureState, Resource: ResourceDTO{Type: entityFeature, ID: testFeatureID}})

	ch, replay, reset := b.subscribeAfter(1, "")
	defer b.unsubscribe(ch)
	if len(replay) != 0 {
		t.Fatalf("replay len = %d, want reset only when cursor has no epoch", len(replay))
	}
	if reset == nil {
		t.Fatal("reset = nil, want stream.reset for epoch-less cursor")
	}
	if reset.Kind != sseEventStreamReset || reset.Epoch != testEventEpoch {
		t.Fatalf("reset = %+v, want stream.reset with current epoch", *reset)
	}
}

func TestEventBrokerEpochMismatchReturnsReset(t *testing.T) {
	t.Parallel()

	b := newEventBrokerWithOptions(eventBrokerOptions{Epoch: "epoch-new", ReplayLimit: 4})
	b.publish(SSEEventDTO{Kind: testEventKindFeatureState, Resource: ResourceDTO{Type: entityFeature, ID: testFeatureID}})

	ch, replay, reset := b.subscribeAfter(1, "epoch-old")
	defer b.unsubscribe(ch)
	if len(replay) != 0 {
		t.Fatalf("replay len = %d, want reset only on epoch mismatch", len(replay))
	}
	if reset == nil || reset.Kind != sseEventStreamReset || reset.Epoch != "epoch-new" {
		t.Fatalf("reset = %+v, want stream.reset with new epoch", reset)
	}
}

func TestEventBrokerThrottlesSessionOutputActivity(t *testing.T) {
	t.Parallel()

	b := newEventBrokerWithOptions(eventBrokerOptions{Epoch: testEventEpoch, ReplayLimit: 8})
	ch := b.subscribe()
	defer b.unsubscribe(ch)

	activity := SSEEventDTO{Kind: sseEventSessionOutputActivity, Resource: ResourceDTO{Type: resourceTypeSession, ID: fixtureSessionID, FeatureID: fixtureFeatureID}}
	b.publish(activity)
	b.publish(activity)

	var got []SSEEventDTO
	for {
		select {
		case evt := <-ch:
			got = append(got, evt)
		default:
			if len(got) != 1 {
				t.Fatalf("activity events delivered = %d (%+v), want one throttled activity event", len(got), got)
			}
			return
		}
	}
}

// TestEventBrokerCoalescedMarkerCarriesTriggeringResource verifies that when
// a subscriber's channel is full, the "backpressure.coalesced" replacement
// event carries the resource identity of the event that couldn't be
// delivered — not a generic runtime marker. A generic marker only triggers a
// Health-only refetch client-side (see client_sse.go's dispatch), which can
// permanently strand a session (e.g. the AMA chat session waiting on a
// completion event) that never gets another event of its own to retrigger a
// real refresh.
func TestEventBrokerCoalescedMarkerCarriesTriggeringResource(t *testing.T) {
	t.Parallel()

	b := newEventBrokerWithOptions(eventBrokerOptions{Epoch: testEventEpoch, ReplayLimit: 64})
	ch := b.subscribe()
	defer b.unsubscribe(ch)

	// Fill the channel to capacity (16) with unrelated events so the next
	// publish is guaranteed to hit the backpressure branch.
	for i := 0; i < 16; i++ {
		b.publish(SSEEventDTO{Kind: sseEventConfigUpdated, Resource: ResourceDTO{Type: resourceTypeRuntime}})
	}

	triggering := SSEEventDTO{
		Kind:     sseEventSessionUpdated,
		Resource: ResourceDTO{Type: resourceTypeSession, ID: ChatSessionID, FeatureID: "some-feature"},
	}
	b.publish(triggering)
	<-ch
	b.flushSubscriber(ch)

	// Drain to the last queued event — the latest coalesced event should be at
	// the tail after the consumer makes room.
	var last SSEEventDTO
	for {
		select {
		case evt := <-ch:
			last = evt
		default:
			goto drained
		}
	}
drained:
	if last.Kind != sseEventSessionUpdated {
		t.Fatalf("last queued event kind = %q, want the latest coalesced session.updated", last.Kind)
	}
	if last.Resource != triggering.Resource {
		t.Fatalf("coalesced resource = %+v, want %+v (the triggering event's resource, so the client can refetch exactly what it missed)", last.Resource, triggering.Resource)
	}
}

func TestEventBrokerSlowSubscriberReceivesNewestTerminalState(t *testing.T) {
	t.Parallel()

	b := newEventBrokerWithOptions(eventBrokerOptions{Epoch: testEventEpoch, ReplayLimit: 128})
	ch := b.subscribe()
	defer b.unsubscribe(ch)

	for i := 0; i < subscriberFIFOSize; i++ {
		b.publish(SSEEventDTO{Kind: sseEventConfigUpdated, Resource: ResourceDTO{Type: resourceTypeRuntime}})
	}
	resource := ResourceDTO{Type: resourceTypeSession, ID: fixtureSessionID, FeatureID: fixtureFeatureID}
	for i := 0; i < 8; i++ {
		b.publish(SSEEventDTO{Kind: sseEventSessionUpdated, Resource: resource, Summary: fmt.Sprintf("intermediate-%d", i)})
	}
	b.publish(SSEEventDTO{Kind: "session.ended", Resource: resource, SnapshotRequired: true, Summary: "terminal"})

	<-ch
	b.flushSubscriber(ch)

	var terminal *SSEEventDTO
	for {
		select {
		case evt := <-ch:
			if evt.Resource == resource {
				copied := evt
				terminal = &copied
			}
		default:
			if terminal == nil {
				t.Fatal("no coalesced session event delivered to slow subscriber")
			}
			if terminal.Kind != "session.ended" || terminal.Summary != "terminal" {
				t.Fatalf("coalesced session event = %+v, want newest terminal state", *terminal)
			}
			return
		}
	}
}

func TestEventBrokerCoalescedOverflowDeliversReset(t *testing.T) {
	t.Parallel()

	b := newEventBrokerWithOptions(eventBrokerOptions{Epoch: testEventEpoch, ReplayLimit: 2048})
	ch := b.subscribe()
	defer b.unsubscribe(ch)

	for i := 0; i < subscriberFIFOSize; i++ {
		b.publish(SSEEventDTO{Kind: testEventKindFeatureState, Resource: ResourceDTO{Type: entityFeature, ID: fmt.Sprintf("queued-%d", i)}})
	}
	for i := 0; i <= maxSubscriberCoalescedEvents; i++ {
		b.publish(SSEEventDTO{Kind: testEventKindFeatureState, Resource: ResourceDTO{Type: entityFeature, ID: fmt.Sprintf("overflow-%d", i)}})
	}

	<-ch
	b.flushSubscriber(ch)
	for {
		select {
		case evt := <-ch:
			if evt.Kind == sseEventStreamReset {
				return
			}
		default:
			t.Fatal("coalesced overflow did not deliver stream.reset after subscriber drained")
		}
	}
}

// TestSnapshotThenSubscribeConverges interleaves broker publishes with the
// exact snapshot->subscribe bootstrap real clients perform (GET a revisioned
// resource to learn as_of_seq, then subscribe with after=as_of_seq) and
// asserts the subscriber never misses an event published in the gap between
// the two calls. Run with -race to catch broker locking regressions too.
func TestSnapshotThenSubscribeConverges(t *testing.T) {
	t.Parallel()
	const iterations = 200

	for i := 0; i < iterations; i++ {
		b := newEventBrokerWithOptions(eventBrokerOptions{})

		// A baseline subscriber attached before any publishing starts —
		// the ground truth for "every event this broker ever emits". It's
		// drained continuously by a goroutine (rather than read after the
		// fact) so it never overflows its buffer into the coalescing path,
		// and stopped via b.unsubscribe (not a bare close) so the channel
		// is only closed once it's already removed from b.subs under the
		// broker's lock — a bare close here would race a concurrent
		// publish still holding the lock and iterating b.subs to send.
		baseline := b.subscribe()
		var baselineEvents []SSEEventDTO
		baselineDone := make(chan struct{})
		go func() {
			defer close(baselineDone)
			for evt := range baseline {
				baselineEvents = append(baselineEvents, evt)
			}
		}()

		// Simulate concurrent mutation traffic racing the snapshot read.
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 5; n++ {
				b.publish(SSEEventDTO{Kind: sseEventLifecycleUpdated, Resource: ResourceDTO{Type: resourceTypeRuntime}})
			}
		}()

		// The "GET snapshot" half of the race: read as_of_seq concurrently
		// with the publishes above.
		asOfSeq := b.currentSeq()

		wg.Wait()

		// The "subscribe(after)" half: bootstrap from exactly the seq the
		// snapshot read observed.
		ch, replay, reset := b.subscribeAfter(asOfSeq, b.currentEpoch())
		if reset != nil {
			t.Fatalf("iteration %d: unexpected stream reset: %+v", i, reset)
		}

		b.unsubscribe(baseline)
		<-baselineDone

		// Compare replay against the baseline's full history filtered to
		// events after asOfSeq: every such event must be present, in
		// order, with none skipped or duplicated.
		var baselineAfter []SSEEventDTO
		for _, evt := range baselineEvents {
			if evt.Seq > asOfSeq {
				baselineAfter = append(baselineAfter, evt)
			}
		}
		if len(replay) != len(baselineAfter) {
			t.Fatalf("iteration %d: replay len = %d, want %d (asOfSeq=%d)", i, len(replay), len(baselineAfter), asOfSeq)
		}
		for j := range replay {
			if replay[j].Seq != baselineAfter[j].Seq {
				t.Fatalf("iteration %d: replay[%d].Seq = %d, want %d", i, j, replay[j].Seq, baselineAfter[j].Seq)
			}
		}
		b.unsubscribe(ch)
	}
}

// TestEventsConnectedSurvivesRingEvictionForCursorlessClient guards against a
// regression from the after=0 fix above: a client with no cursor at all must
// always get "connected" (full resync), never a sseEventStreamReset (Health-only
// resync) — even once the broker's ring has evicted its earliest entries.
func TestEventsConnectedSurvivesRingEvictionForCursorlessClient(t *testing.T) {
	t.Parallel()

	h := newAPIHandler(HandlerOptions{DisableHostValidation: true})
	for i := 0; i < defaultEventReplayLimit+10; i++ {
		h.broker.publish(SSEEventDTO{Kind: sseEventLifecycleUpdated, Resource: ResourceDTO{Type: resourceTypeRuntime}})
	}

	srv := httptest.NewServer(h.routes())
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v1/events?heartbeat_ms=10", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	readSSEBlock(t, reader, "connected")
}

func TestPruneOutputActivityNeverEvictsWithinThrottleWindow(t *testing.T) {
	t.Parallel()

	b := newEventBrokerWithOptions(eventBrokerOptions{})
	now := time.Now().UTC()

	// Fill past the hard cap with entries all inside the throttle window.
	b.mu.Lock()
	for i := 0; i < maxOutputActivityKeys+10; i++ {
		key := fmt.Sprintf("session\x00sess-%d\x00feat\x00", i)
		b.lastOutputActivity[key] = now
	}
	b.pruneOutputActivityLocked(now)
	remaining := len(b.lastOutputActivity)
	b.mu.Unlock()

	if remaining != maxOutputActivityKeys+10 {
		t.Fatalf("prune evicted %d fresh (within-window) entries; map size = %d, want %d untouched", (maxOutputActivityKeys+10)-remaining, remaining, maxOutputActivityKeys+10)
	}
}

func eventSeqs(events []SSEEventDTO) []uint64 {
	seqs := make([]uint64, 0, len(events))
	for _, evt := range events {
		seqs = append(seqs, evt.Seq)
	}
	return seqs
}

func TestSessionOutputActivityCarriesCurrentRecordCount(t *testing.T) {
	t.Parallel()

	msg := session.SDKEventMsg{
		SessionID:   fixtureSessionID,
		FeatureID:   fixtureFeatureID,
		RecordCount: 3,
		Message:     llm.SDKMessage{Type: "assistant"},
	}
	evt := eventDTOFromRuntime(msg)
	if evt.Kind != sseEventSessionOutputActivity || evt.RecordCount != 3 {
		t.Fatalf("eventDTOFromRuntime = %+v, want session.output.activity with RecordCount 3", evt)
	}
}
