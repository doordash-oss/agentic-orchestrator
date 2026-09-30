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
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestEventBrokerRuntimeTapReceivesEveryMessageInSSEOrder(t *testing.T) {
	input := make(chan interface{}, 3)
	tapped := make(chan interface{}, 3)
	b := newEventBrokerTaps(input, nil, func(message interface{}) {
		tapped <- message
	}, nil)
	events, _, _ := b.subscribeAfter(0, "")
	defer b.unsubscribe(events)

	want := []session.SessionStartedMsg{
		{SessionID: "session-1", FeatureID: "feature-1", Phase: feature.PhaseResearch},
		{SessionID: "session-2", FeatureID: "feature-2", Phase: feature.PhaseDesign},
		{SessionID: "session-3", FeatureID: "feature-3", Phase: feature.PhasePlan},
	}
	for _, message := range want {
		input <- message
	}
	close(input)

	for i, wantMessage := range want {
		gotMessage := receiveRuntimeTap(t, tapped)
		if gotMessage != wantMessage {
			t.Errorf("runtime tap message %d = %+v; want %+v", i, gotMessage, wantMessage)
		}

		gotEvent := receiveBrokerEvent(t, events)
		if gotEvent.Kind != sseEventSessionUpdated {
			t.Errorf("runtime SSE event %d kind = %q; want %q", i, gotEvent.Kind, sseEventSessionUpdated)
		}
		if gotEvent.Resource.ID != wantMessage.SessionID || gotEvent.Resource.FeatureID != wantMessage.FeatureID {
			t.Errorf("runtime SSE event %d resource = %+v; want session %q for feature %q", i, gotEvent.Resource, wantMessage.SessionID, wantMessage.FeatureID)
		}
		if gotEvent.Seq != uint64(i+1) {
			t.Errorf("runtime SSE event %d seq = %d; want %d", i, gotEvent.Seq, i+1)
		}
	}
}

func TestEventBrokerDomainTapReceivesEveryEventInSSEOrder(t *testing.T) {
	domain := make(chan ports.Event, 3)
	tapped := make(chan ports.Event, 3)
	b := newEventBrokerTaps(nil, domain, nil, func(event ports.Event) {
		tapped <- event
	})
	events, _, _ := b.subscribeAfter(0, "")
	defer b.unsubscribe(events)

	want := []ports.Event{
		{Type: ports.FeatureStarted, FeatureID: "feature-1"},
		{Type: ports.PhaseStarted, FeatureID: "feature-2", Phase: feature.PhaseDesign},
		{Type: ports.FeatureCompleted, FeatureID: "feature-3"},
	}
	for _, event := range want {
		domain <- event
	}
	close(domain)

	for i, wantEvent := range want {
		gotTap := receiveDomainTap(t, tapped)
		if gotTap.Type != wantEvent.Type || gotTap.FeatureID != wantEvent.FeatureID || gotTap.Phase != wantEvent.Phase {
			t.Errorf("domain tap event %d = %+v; want %+v", i, gotTap, wantEvent)
		}

		gotEvent := receiveBrokerEvent(t, events)
		if gotEvent.Kind != sseEventLifecycleUpdated {
			t.Errorf("domain SSE event %d kind = %q; want %q", i, gotEvent.Kind, sseEventLifecycleUpdated)
		}
		if gotEvent.Resource.FeatureID != wantEvent.FeatureID || gotEvent.Resource.Phase != eventDTOFromDomain(wantEvent).Resource.Phase {
			t.Errorf("domain SSE event %d resource = %+v; want feature %q phase %q", i, gotEvent.Resource, wantEvent.FeatureID, eventDTOFromDomain(wantEvent).Resource.Phase)
		}
		if gotEvent.Seq != uint64(i+1) {
			t.Errorf("domain SSE event %d seq = %d; want %d", i, gotEvent.Seq, i+1)
		}
	}
}

func TestEventBrokerSkipsNilTaps(t *testing.T) {
	t.Run("runtime", func(t *testing.T) {
		input := make(chan interface{}, 2)
		b := newEventBrokerTaps(input, nil, nil, nil)
		events, _, _ := b.subscribeAfter(0, "")
		defer b.unsubscribe(events)

		input <- session.SessionStartedMsg{SessionID: "session-1", FeatureID: "feature-1", Phase: feature.PhaseResearch}
		input <- session.SessionStartedMsg{SessionID: "session-2", FeatureID: "feature-2", Phase: feature.PhaseDesign}
		close(input)

		for i, wantSessionID := range []string{"session-1", "session-2"} {
			got := receiveBrokerEvent(t, events)
			if got.Resource.ID != wantSessionID || got.Seq != uint64(i+1) {
				t.Errorf("runtime SSE event %d = %+v; want session %q with seq %d", i, got, wantSessionID, i+1)
			}
		}
	})

	t.Run("domain", func(t *testing.T) {
		domain := make(chan ports.Event, 2)
		b := newEventBrokerTaps(nil, domain, nil, nil)
		events, _, _ := b.subscribeAfter(0, "")
		defer b.unsubscribe(events)

		domain <- ports.Event{Type: ports.FeatureStarted, FeatureID: "feature-1"}
		domain <- ports.Event{Type: ports.FeatureCompleted, FeatureID: "feature-2"}
		close(domain)

		for i, wantFeatureID := range []string{"feature-1", "feature-2"} {
			got := receiveBrokerEvent(t, events)
			if got.Resource.FeatureID != wantFeatureID || got.Seq != uint64(i+1) {
				t.Errorf("domain SSE event %d = %+v; want feature %q with seq %d", i, got, wantFeatureID, i+1)
			}
		}
	})
}

func receiveRuntimeTap(t testing.TB, tapped <-chan interface{}) session.SessionStartedMsg {
	t.Helper()
	select {
	case message := <-tapped:
		started, ok := message.(session.SessionStartedMsg)
		if !ok {
			t.Fatalf("runtime tap message type = %T; want session.SessionStartedMsg", message)
		}
		return started
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime tap")
		return session.SessionStartedMsg{}
	}
}

func receiveDomainTap(t testing.TB, tapped <-chan ports.Event) ports.Event {
	t.Helper()
	select {
	case event := <-tapped:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for domain tap")
		return ports.Event{}
	}
}

func receiveBrokerEvent(t testing.TB, events <-chan SSEEvent) SSEEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broker event")
		return SSEEvent{}
	}
}
