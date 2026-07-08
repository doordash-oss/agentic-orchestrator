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
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

type eventBroker struct {
	mu     sync.Mutex
	nextID atomic.Uint64
	subs   map[chan SSEEventDTO]struct{}
}

func newEventBroker(input <-chan interface{}, domain <-chan ports.Event) *eventBroker {
	b := &eventBroker{subs: map[chan SSEEventDTO]struct{}{}}
	if input != nil {
		go func() {
			for msg := range input {
				b.publish(eventDTOFromRuntime(msg, b.newID()))
			}
		}()
	}
	if domain != nil {
		go func() {
			for ev := range domain {
				b.publish(eventDTOFromDomain(ev, b.newID()))
			}
		}()
	}
	return b
}

func (b *eventBroker) subscribe() chan SSEEventDTO {
	ch := make(chan SSEEventDTO, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *eventBroker) unsubscribe(ch chan SSEEventDTO) {
	b.mu.Lock()
	delete(b.subs, ch)
	close(ch)
	b.mu.Unlock()
}

func (b *eventBroker) publish(evt SSEEventDTO) {
	if evt.Kind == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- evt:
		default:
			// Channel full: evict the oldest queued event to make room, then
			// enqueue a coalesced marker carrying evt's own resource identity
			// (not a generic "runtime" one) so the client can still refetch
			// exactly the resource it missed — e.g. a chat session's
			// completion — instead of only a Health check that leaves that
			// resource stale until some other, unrelated event happens to
			// arrive for it.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- snapshotRequiredEvent(b.newID(), "backpressure.coalesced", evt.Resource):
			default:
			}
		}
	}
}

func (b *eventBroker) newID() string {
	return strconv.FormatUint(b.nextID.Add(1), 10)
}

func (h *apiHandler) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "streaming unavailable", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := h.broker.subscribe()
	defer h.broker.unsubscribe(ch)

	connected := snapshotRequiredEvent(h.broker.newID(), "connected", ResourceDTO{Type: "runtime"})
	if err := writeSSE(w, "connected", connected); err != nil {
		return
	}
	flusher.Flush()

	heartbeatEvery := heartbeatInterval(r)
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case evt := <-ch:
			if err := writeSSE(w, evt.Kind, evt); err != nil {
				return
			}
			flusher.Flush()
		case now := <-ticker.C:
			evt := SSEEventDTO{
				APIVersion:       APIVersion,
				ID:               h.broker.newID(),
				Kind:             "heartbeat",
				At:               now.UTC(),
				Resource:         ResourceDTO{Type: "runtime"},
				SnapshotRequired: false,
			}
			if err := writeSSE(w, "heartbeat", evt); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func heartbeatInterval(r *http.Request) time.Duration {
	ms, err := strconv.Atoi(r.URL.Query().Get("heartbeat_ms"))
	if err != nil || ms <= 0 {
		return 15 * time.Second
	}
	if ms < 10 {
		ms = 10
	}
	return time.Duration(ms) * time.Millisecond
}

func writeSSE(w http.ResponseWriter, event string, data SSEEventDTO) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", data.ID, event, payload); err != nil {
		return err
	}
	return nil
}

func snapshotRequiredEvent(id, kind string, resource ResourceDTO) SSEEventDTO {
	return SSEEventDTO{
		APIVersion:       APIVersion,
		ID:               id,
		Kind:             kind,
		At:               time.Now().UTC(),
		Resource:         resource,
		Revision:         revisionForAny(resource),
		SnapshotRequired: true,
	}
}

func eventDTOFromRuntime(msg interface{}, id string) SSEEventDTO {
	switch ev := msg.(type) {
	case ports.Event:
		return eventDTOFromDomain(ev, id)
	case session.SDKEventMsg:
		kind := "session.updated"
		if ev.Message.ControlRequest != nil {
			if ev.Message.ControlRequest.Request.ToolName == "AskUserQuestion" {
				kind = "prompt.updated"
			} else {
				kind = "permission.updated"
			}
		}
		if ev.Message.Type == "tool_progress" {
			kind = "log.updated"
		}
		return snapshotRequiredEvent(id, kind, ResourceDTO{Type: "session", ID: ev.SessionID, FeatureID: ev.FeatureID, Phase: ev.Phase.String()})
	case session.SessionDoneMsg:
		return snapshotRequiredEvent(id, "session.updated", ResourceDTO{Type: "session", ID: ev.SessionID, FeatureID: ev.FeatureID, Phase: ev.Phase.String()})
	default:
		return snapshotRequiredEvent(id, "lifecycle.updated", ResourceDTO{Type: "runtime"})
	}
}

func eventDTOFromDomain(ev ports.Event, id string) SSEEventDTO {
	kind := "lifecycle.updated"
	resourceType := "feature"
	switch ev.Type {
	case ports.FeatureConfigChanged:
		kind = "config.updated"
	case ports.NeedUserInputRequired:
		kind = "prompt.updated"
	case ports.RecoveryScanned, ports.RecoveryExecuted:
		kind = "recovery.updated"
		resourceType = "runtime"
	case ports.RuntimeShutdownStarted:
		kind = "shutdown.updated"
		resourceType = "runtime"
	case ports.SessionOutput:
		kind = "session.updated"
		resourceType = "session"
	case ports.RepoStatusChanged:
		kind = "lifecycle.updated"
	case ports.FeatureFailed:
		kind = "lifecycle.updated"
	}
	phase := ev.Phase.String()
	if ev.Phase == feature.Phase(0) && phase == feature.PhaseResearch.String() {
		phase = ""
	}
	dto := snapshotRequiredEvent(id, kind, ResourceDTO{Type: resourceType, FeatureID: ev.FeatureID, Phase: phase})
	dto.Summary = safeEventSummary(ev)
	return dto
}

func safeEventSummary(ev ports.Event) string {
	switch ev.Type {
	case ports.FeatureFailed:
		return "feature failed"
	case ports.FeatureRewound:
		return "feature rewound"
	case ports.PhaseCompleted:
		return "phase completed"
	case ports.PhaseStarted:
		return "phase started"
	case ports.NeedUserInputRequired:
		return "user input required"
	case ports.FeatureConfigChanged:
		return "config changed"
	default:
		return ""
	}
}
