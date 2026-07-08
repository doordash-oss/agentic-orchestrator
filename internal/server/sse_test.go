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

	b := newEventBroker(nil, nil)
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

	// Drain to the last queued event — the coalesced marker should be at
	// the tail (it replaced whatever was evicted to make room).
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
	if last.Kind != "backpressure.coalesced" {
		t.Fatalf("last queued event kind = %q, want backpressure.coalesced", last.Kind)
	}
	if last.Resource != triggering.Resource {
		t.Fatalf("coalesced marker resource = %+v, want %+v (the triggering event's resource, so the client can refetch exactly what it missed)", last.Resource, triggering.Resource)
	}
}
