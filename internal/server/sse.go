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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

const (
	defaultEventReplayLimit      = 4096
	subscriberFIFOSize           = 16
	maxSubscriberCoalescedEvents = 1024
	maxOutputActivityKeys        = 4096
	streamWriteTimeout           = 5 * time.Second
)

// SSE event-kind literals shared between the broker, the client's refresh
// dispatcher (client_sse.go) and their tests.
const (
	// sseEventConnected is the synthetic full-resync event kind sent to a
	// client with no replay cursor.
	sseEventConnected = "connected"
	// sseEventConfigUpdated is the event kind for a feature or runtime
	// config change.
	sseEventConfigUpdated = "config.updated"
	// sseEventHeartbeat is the periodic keep-alive event kind.
	sseEventHeartbeat = "heartbeat"
	// sseEventLifecycleUpdated is the catch-all/default event kind for a
	// feature or runtime lifecycle change with no more specific kind.
	sseEventLifecycleUpdated = "lifecycle.updated"
	// sseEventRecoveryUpdated is the event kind for a recovery-scan change.
	sseEventRecoveryUpdated = "recovery.updated"
	// sseEventSessionOutputActivity is the event kind for a session output
	// append; it carries no snapshot-worthy resource change.
	sseEventSessionOutputActivity = "session.output.activity"
	// sseEventSessionUpdated is the event kind for a session lifecycle change.
	sseEventSessionUpdated = "session.updated"
	// sseEventShutdownUpdated is the event kind for a runtime shutdown
	// schedule change.
	sseEventShutdownUpdated = "shutdown.updated"
	// sseEventStreamReset is the event kind sent when the client's replay
	// cursor has fallen outside the broker's retained buffer, forcing a full
	// resync.
	sseEventStreamReset = "stream.reset"
)

type eventBroker struct {
	mu                 sync.Mutex
	nextSeq            uint64
	epoch              string
	replayLimit        int
	ring               []SSEEvent
	subs               map[chan SSEEvent]*subscriberState
	resourceVersions   map[string]uint64
	lastOutputActivity map[string]time.Time
}

type eventBrokerOptions struct {
	Epoch       string
	ReplayLimit int
}

type subscriberState struct {
	coalesced    map[string]SSEEvent
	resetPending bool
}

func newEventBroker(input <-chan interface{}, domain <-chan ports.Event) *eventBroker {
	b := newEventBrokerWithOptions(eventBrokerOptions{})
	if input != nil {
		go func() {
			for msg := range input {
				b.publish(eventDTOFromRuntime(msg))
			}
		}()
	}
	if domain != nil {
		go func() {
			for ev := range domain {
				b.publish(eventDTOFromDomain(ev))
			}
		}()
	}
	return b
}

func newEventBrokerWithOptions(opts eventBrokerOptions) *eventBroker {
	epoch := opts.Epoch
	if epoch == "" {
		epoch = newEventEpoch()
	}
	replayLimit := opts.ReplayLimit
	if replayLimit <= 0 {
		replayLimit = defaultEventReplayLimit
	}
	return &eventBroker{
		epoch:              epoch,
		replayLimit:        replayLimit,
		subs:               map[chan SSEEvent]*subscriberState{},
		resourceVersions:   map[string]uint64{},
		lastOutputActivity: map[string]time.Time{},
	}
}

func (b *eventBroker) subscribeAfter(after uint64, epoch string) (chan SSEEvent, []SSEEvent, *SSEEvent) {
	ch := make(chan SSEEvent, subscriberFIFOSize)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[ch] = &subscriberState{coalesced: map[string]SSEEvent{}}
	if after > 0 && epoch == "" {
		reset := b.streamResetEventLocked()
		return ch, nil, &reset
	}
	if epoch != "" && epoch != b.epoch {
		reset := b.streamResetEventLocked()
		return ch, nil, &reset
	}
	replay, ok := b.replayAfterLocked(after)
	if !ok {
		reset := b.streamResetEventLocked()
		return ch, nil, &reset
	}
	return ch, replay, nil
}

func (b *eventBroker) unsubscribe(ch chan SSEEvent) {
	b.mu.Lock()
	delete(b.subs, ch)
	close(ch)
	b.mu.Unlock()
}

func (b *eventBroker) publish(evt SSEEvent) {
	if evt.Kind == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.shouldPublishEventLocked(evt) {
		return
	}
	evt = b.assignEnvelopeLocked(evt)
	b.appendReplayLocked(evt)
	for ch, sub := range b.subs {
		select {
		case ch <- evt:
		default:
			if sub.coalesced == nil {
				sub.coalesced = map[string]SSEEvent{}
			}
			sub.coalesced[resourceKey(evt.Resource)] = evt
			if len(sub.coalesced) > maxSubscriberCoalescedEvents {
				sub.coalesced = map[string]SSEEvent{}
				sub.resetPending = true
			}
		}
	}
}

func (b *eventBroker) shouldPublishEventLocked(evt SSEEvent) bool {
	if evt.Kind != sseEventSessionOutputActivity {
		return true
	}
	key := resourceKey(evt.Resource)
	now := evt.At
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if last := b.lastOutputActivity[key]; !last.IsZero() && now.Sub(last) < time.Second {
		return false
	}
	b.lastOutputActivity[key] = now
	b.pruneOutputActivityLocked(now)
	return true
}

func (b *eventBroker) pruneOutputActivityLocked(now time.Time) {
	if len(b.lastOutputActivity) <= maxOutputActivityKeys {
		return
	}
	for key, at := range b.lastOutputActivity {
		if now.Sub(at) > time.Minute {
			delete(b.lastOutputActivity, key)
		}
	}
	// Hard-cap eviction only ever removes entries already outside the
	// throttle window — evicting a fresh entry here would let an activity
	// burst for that resource straight through shouldPublishEventLocked.
	for key, at := range b.lastOutputActivity {
		if len(b.lastOutputActivity) <= maxOutputActivityKeys/2 {
			return
		}
		if now.Sub(at) < time.Second {
			continue
		}
		delete(b.lastOutputActivity, key)
	}
}

func (b *eventBroker) flushSubscriber(ch chan SSEEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub := b.subs[ch]
	if sub == nil {
		return
	}
	if sub.resetPending {
		reset := b.streamResetEventLocked()
		select {
		case ch <- reset:
			sub.resetPending = false
		default:
			return
		}
	}
	if len(sub.coalesced) == 0 {
		return
	}
	events := make([]SSEEvent, 0, len(sub.coalesced))
	for _, evt := range sub.coalesced {
		events = append(events, evt)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	for _, evt := range events {
		select {
		case ch <- evt:
			delete(sub.coalesced, resourceKey(evt.Resource))
		default:
			return
		}
	}
}

func (b *eventBroker) assignEnvelopeLocked(evt SSEEvent) SSEEvent {
	b.nextSeq++
	evt.Seq = b.nextSeq
	evt.ID = strconv.FormatUint(evt.Seq, 10)
	evt.Epoch = b.epoch
	if evt.APIVersion == "" {
		evt.APIVersion = APIVersion
	}
	if evt.At.IsZero() {
		evt.At = time.Now().UTC()
	}
	if evt.Revision == "" {
		evt.Revision = revisionForAny(evt.Resource)
	}
	if evt.ResourceVersion == 0 {
		key := resourceKey(evt.Resource)
		b.resourceVersions[key]++
		evt.ResourceVersion = b.resourceVersions[key]
	}
	return evt
}

func (b *eventBroker) appendReplayLocked(evt SSEEvent) {
	b.ring = append(b.ring, evt)
	if len(b.ring) > b.replayLimit {
		copy(b.ring, b.ring[len(b.ring)-b.replayLimit:])
		b.ring = b.ring[:b.replayLimit]
	}
}

func (b *eventBroker) replayAfterLocked(after uint64) ([]SSEEvent, bool) {
	if len(b.ring) == 0 {
		return nil, true
	}
	current := b.nextSeq
	if after > current {
		return nil, false
	}
	oldest := b.ring[0].Seq
	if after < oldest-1 {
		return nil, false
	}
	replay := make([]SSEEvent, 0, len(b.ring))
	for _, evt := range b.ring {
		if evt.Seq > after {
			replay = append(replay, evt)
		}
	}
	return replay, true
}

func (b *eventBroker) currentSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextSeq
}

func (b *eventBroker) currentCursor() (uint64, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextSeq, b.epoch
}

func (b *eventBroker) currentEpoch() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.epoch
}

func (b *eventBroker) streamResetEventLocked() SSEEvent {
	seq := b.nextSeq
	return SSEEvent{
		APIVersion:       APIVersion,
		ID:               strconv.FormatUint(seq, 10),
		Seq:              seq,
		Epoch:            b.epoch,
		Kind:             sseEventStreamReset,
		At:               time.Now().UTC(),
		Resource:         Resource{Type: resourceTypeRuntime},
		Revision:         revisionForAny(Resource{Type: resourceTypeRuntime}),
		SnapshotRequired: true,
	}
}

func (h *apiHandler) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError, errcat.WithDiagnostics("streaming unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	after, epoch, hasCursor := eventCursor(r)
	ch, replay, reset := h.broker.subscribeAfter(after, epoch)
	defer h.broker.unsubscribe(ch)

	switch {
	case !hasCursor:
		// No cursor at all: always a full resync. A client with no cursor
		// wants a snapshot marker, not a replay/reset decision keyed off a
		// wire value (after=0) that's indistinguishable from a genuine
		// "I read as_of_seq=0" cursor — see subscribeAfter/replayAfterLocked,
		// which now correctly treats after=0 as a real low-water-mark.
		connected := h.broker.snapshotRequiredEvent(sseEventConnected, Resource{Type: resourceTypeRuntime})
		if err := writeSSE(w, sseEventConnected, connected); err != nil {
			return
		}
		flusher.Flush()
	case reset != nil:
		if err := writeSSE(w, reset.Kind, *reset); err != nil {
			return
		}
		flusher.Flush()
	default:
		for _, evt := range replay {
			if err := writeSSE(w, evt.Kind, evt); err != nil {
				return
			}
			flusher.Flush()
		}
	}

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
			h.broker.flushSubscriber(ch)
		case now := <-ticker.C:
			h.broker.flushSubscriber(ch)
			seq, epoch := h.broker.currentCursor()
			evt := SSEEvent{
				APIVersion:       APIVersion,
				ID:               strconv.FormatUint(seq, 10),
				Seq:              seq,
				Epoch:            epoch,
				Kind:             sseEventHeartbeat,
				At:               now.UTC(),
				Resource:         Resource{Type: resourceTypeRuntime},
				SnapshotRequired: false,
			}
			if err := writeSSE(w, sseEventHeartbeat, evt); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func eventCursor(r *http.Request) (after uint64, epoch string, hasCursor bool) {
	raw := r.URL.Query().Get("after")
	if raw == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	if raw == "" {
		return 0, r.URL.Query().Get("epoch"), false
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, r.URL.Query().Get("epoch"), false
	}
	return parsed, r.URL.Query().Get("epoch"), true
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

func writeSSE(w http.ResponseWriter, event string, data SSEEvent) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	setStreamWriteDeadline(w)
	if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", data.ID, event, payload); err != nil {
		return err
	}
	return nil
}

func setStreamWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
}

func (b *eventBroker) snapshotRequiredEvent(kind string, resource Resource) SSEEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.assignEnvelopeLocked(SSEEvent{
		APIVersion:       APIVersion,
		Kind:             kind,
		Resource:         resource,
		SnapshotRequired: true,
	})
}

func eventDTOFromRuntime(msg interface{}) SSEEvent {
	switch ev := msg.(type) {
	case ports.Event:
		return eventDTOFromDomain(ev)
	case session.SDKEventMsg:
		resource := Resource{Type: resourceTypeSession, ID: ev.SessionID, FeatureID: ev.FeatureID, Phase: ev.Phase.String()}
		if ev.Message.ControlRequest != nil {
			kind := "permission.updated"
			if ev.Message.ControlRequest.Request.ToolName == toolNameAskUserQuestion {
				kind = "prompt.updated"
			}
			return snapshotRequiredEventDTO(kind, resource)
		}
		return SSEEvent{
			APIVersion:       APIVersion,
			Kind:             sseEventSessionOutputActivity,
			At:               time.Now().UTC(),
			Resource:         resource,
			Revision:         revisionForAny(resource),
			SnapshotRequired: false,
			RecordCount:      ev.RecordCount,
		}
	case session.SessionStartedMsg:
		return snapshotRequiredEventDTO(sseEventSessionUpdated, Resource{Type: resourceTypeSession, ID: ev.SessionID, FeatureID: ev.FeatureID, Phase: ev.Phase.String()})
	case session.SessionDoneMsg:
		return snapshotRequiredEventDTO(sseEventSessionUpdated, Resource{Type: resourceTypeSession, ID: ev.SessionID, FeatureID: ev.FeatureID, Phase: ev.Phase.String()})
	default:
		return snapshotRequiredEventDTO(sseEventLifecycleUpdated, Resource{Type: resourceTypeRuntime})
	}
}

func eventDTOFromDomain(ev ports.Event) SSEEvent {
	kind := sseEventLifecycleUpdated
	resourceType := entityFeature
	if relationshipEventType(ev.Type) {
		resource := Resource{
			Type:                resourceTypeRelationship,
			ID:                  relationshipResourceID(ev.ParentID, ev.ChildID),
			ParentID:            ev.ParentID,
			ChildID:             ev.ChildID,
			RelationshipDeleted: ev.Type == ports.RelationshipCascadeDeleted,
		}
		dto := snapshotRequiredEventDTO(sseEventLifecycleUpdated, resource)
		dto.Summary = safeEventSummary(ev)
		if ev.CanonicalError != nil {
			// A relationship integration event carrying a canonical error
			// (a parked transaction) carries the catalog title as the
			// summary and the canonical code/class identity as the error
			// object, exactly as failure-carrying feature events do.
			dto.Summary = ev.CanonicalError.Title
			dto.Error = &SSEEventError{
				Code:  string(ev.CanonicalError.Code),
				Class: SSEEventErrorClass(ev.CanonicalError.Class),
			}
		}
		return dto
	}
	switch ev.Type {
	case ports.FeatureConfigChanged:
		kind = sseEventConfigUpdated
	case ports.NeedUserInputRequired:
		kind = "prompt.updated"
	case ports.RecoveryScanned, ports.RecoveryExecuted:
		kind = sseEventRecoveryUpdated
		resourceType = resourceTypeRuntime
	case ports.RuntimeShutdownStarted:
		kind = sseEventShutdownUpdated
		resourceType = resourceTypeRuntime
	case ports.SessionOutput:
		kind = sseEventSessionOutputActivity
		resourceType = resourceTypeSession
	case ports.RepoStatusChanged:
		kind = sseEventLifecycleUpdated
	case ports.FeatureResumed:
		kind = sseEventLifecycleUpdated
	case ports.FeatureFailed:
		kind = sseEventLifecycleUpdated
	}
	phase := ev.Phase.String()
	if ev.Phase == feature.Phase(0) && phase == feature.PhaseResearch.String() {
		phase = ""
	}
	if ev.PhaseKey != "" {
		phase = ev.PhaseKey
	}
	// A PhaseKey on the event overrides the derived phase name so roadmap phase
	// keys survive the DTO mapping.
	resource := Resource{Type: resourceType, FeatureID: ev.FeatureID, Phase: phase}
	var dto SSEEvent
	if kind == sseEventSessionOutputActivity {
		dto = SSEEvent{
			APIVersion:       APIVersion,
			Kind:             kind,
			At:               time.Now().UTC(),
			Resource:         resource,
			Revision:         revisionForAny(resource),
			SnapshotRequired: false,
		}
	} else {
		dto = snapshotRequiredEventDTO(kind, resource)
	}
	dto.Summary = safeEventSummary(ev)
	if (ev.Type == ports.FeatureFailed || ev.Type == ports.SetupFailed ||
		ev.Type == ports.PublishCompleted || ev.Type == ports.RepoStatusChanged) &&
		ev.CanonicalError != nil {
		// Failure-carrying lifecycle events carry the catalog title as the
		// summary and the canonical code/class identity as the error object.
		// A repository publish failure rides the publish-completed and
		// repository-status-changed kinds the same way, snapshot-required
		// semantics unchanged.
		dto.Summary = ev.CanonicalError.Title
		dto.Error = &SSEEventError{
			Code:  string(ev.CanonicalError.Code),
			Class: SSEEventErrorClass(ev.CanonicalError.Class),
		}
	}
	return dto
}

func relationshipEventType(eventType ports.EventType) bool {
	switch eventType {
	case ports.RelationshipChildCreated,
		ports.RelationshipIntegrationChanged,
		ports.RelationshipClosed,
		ports.RelationshipDiscardProgress,
		ports.RelationshipCascadeProgress,
		ports.RelationshipCascadeDeleted:
		return true
	default:
		return false
	}
}

func relationshipResourceID(parentID, childID string) string {
	return parentID + ":" + childID
}

func snapshotRequiredEventDTO(kind string, resource Resource) SSEEvent {
	return SSEEvent{
		APIVersion:       APIVersion,
		Kind:             kind,
		At:               time.Now().UTC(),
		Resource:         resource,
		Revision:         revisionForAny(resource),
		SnapshotRequired: true,
	}
}

func resourceKey(resource Resource) string {
	return resource.Type + "\x00" + resource.ID + "\x00" + resource.FeatureID + "\x00" + resource.ParentID + "\x00" + resource.ChildID + "\x00" + resource.Phase
}

func newEventEpoch() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf[:])
}

func safeEventSummary(ev ports.Event) string {
	switch ev.Type {
	case ports.FeatureFailed:
		return "feature failed"
	case ports.FeatureRewound:
		return "feature rewound"
	case ports.FeatureResumed:
		return "feature resumed"
	case ports.PhaseCompleted:
		return "phase completed"
	case ports.PhaseStarted:
		return "phase started"
	case ports.NeedUserInputRequired:
		return "user input required"
	case ports.FeatureConfigChanged:
		return "config changed"
	case ports.RelationshipChildCreated:
		return "relationship child created"
	case ports.RelationshipIntegrationChanged:
		return "relationship integration changed"
	case ports.RelationshipClosed:
		return "relationship closed"
	case ports.RelationshipDiscardProgress:
		return "relationship discard progressed"
	case ports.RelationshipCascadeProgress:
		return "relationship cascade progressed"
	case ports.RelationshipCascadeDeleted:
		return "relationship cascade deleted"
	default:
		return ""
	}
}
