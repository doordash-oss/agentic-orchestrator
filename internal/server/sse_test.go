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

import "testing"

func TestEventBrokerAssignsMonotonicEnvelopeFields(t *testing.T) {
	t.Parallel()

	b := newEventBrokerForTest(eventBrokerOptions{Epoch: "epoch-test", ReplayLimit: 4})
	ch := b.subscribe()
	defer b.unsubscribe(ch)

	b.publish(SSEEventDTO{Kind: "feature.state", Resource: ResourceDTO{Type: "feature", ID: "F-1"}})
	b.publish(SSEEventDTO{Kind: "feature.state", Resource: ResourceDTO{Type: "feature", ID: "F-1"}})

	first := <-ch
	second := <-ch
	if first.Seq != 1 || first.ID != "1" || first.Epoch != "epoch-test" {
		t.Fatalf("first event envelope = %+v, want seq/id 1 and epoch-test", first)
	}
	if second.Seq != 2 || second.ID != "2" || second.Epoch != "epoch-test" {
		t.Fatalf("second event envelope = %+v, want seq/id 2 and epoch-test", second)
	}
	if first.ResourceVersion != 1 || second.ResourceVersion != 2 {
		t.Fatalf("resource versions = %d, %d; want 1, 2", first.ResourceVersion, second.ResourceVersion)
	}
}

func TestEventBrokerReplayFromCursor(t *testing.T) {
	t.Parallel()

	b := newEventBrokerForTest(eventBrokerOptions{Epoch: "epoch-test", ReplayLimit: 4})
	b.publish(SSEEventDTO{Kind: "feature.state", Resource: ResourceDTO{Type: "feature", ID: "F-1"}})
	b.publish(SSEEventDTO{Kind: "feature.state", Resource: ResourceDTO{Type: "feature", ID: "F-2"}})
	b.publish(SSEEventDTO{Kind: "feature.state", Resource: ResourceDTO{Type: "feature", ID: "F-3"}})

	ch, replay, reset := b.subscribeAfter(1, "epoch-test")
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

	b := newEventBrokerForTest(eventBrokerOptions{Epoch: "epoch-test", ReplayLimit: 2})
	for i := 0; i < 4; i++ {
		b.publish(SSEEventDTO{Kind: "feature.state", Resource: ResourceDTO{Type: "feature", ID: "F-1"}})
	}

	ch, replay, reset := b.subscribeAfter(1, "epoch-test")
	defer b.unsubscribe(ch)
	if len(replay) != 0 {
		t.Fatalf("replay len = %d, want reset only", len(replay))
	}
	if reset == nil {
		t.Fatal("reset = nil, want stream.reset")
	}
	if reset.Kind != "stream.reset" || reset.Seq != 4 || reset.Epoch != "epoch-test" {
		t.Fatalf("reset = %+v, want stream.reset at current seq 4", *reset)
	}
}

func TestEventBrokerThrottlesSessionOutputActivity(t *testing.T) {
	t.Parallel()

	b := newEventBrokerForTest(eventBrokerOptions{Epoch: "epoch-test", ReplayLimit: 8})
	ch := b.subscribe()
	defer b.unsubscribe(ch)

	activity := SSEEventDTO{Kind: "session.output.activity", Resource: ResourceDTO{Type: "session", ID: "sess-1", FeatureID: "feat-1"}}
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

	b := newEventBrokerForTest(eventBrokerOptions{Epoch: "epoch-test", ReplayLimit: 64})
	ch := b.subscribe()
	defer b.unsubscribe(ch)

	// Fill the channel to capacity (16) with unrelated events so the next
	// publish is guaranteed to hit the backpressure branch.
	for i := 0; i < 16; i++ {
		b.publish(SSEEventDTO{Kind: "config.updated", Resource: ResourceDTO{Type: "runtime"}})
	}

	triggering := SSEEventDTO{
		Kind:     "session.updated",
		Resource: ResourceDTO{Type: "session", ID: "__chat__", FeatureID: "some-feature"},
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
	if last.Kind != "session.updated" {
		t.Fatalf("last queued event kind = %q, want the latest coalesced session.updated", last.Kind)
	}
	if last.Resource != triggering.Resource {
		t.Fatalf("coalesced resource = %+v, want %+v (the triggering event's resource, so the client can refetch exactly what it missed)", last.Resource, triggering.Resource)
	}
}
