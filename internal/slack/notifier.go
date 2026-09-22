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

package slack

import (
	"context"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// defaultQueueCapacity bounds the intake queue so producers never wait and
// a slow Slack never back-pressures the orchestrator.
const defaultQueueCapacity = 256

// FeatureLoader loads feature records by ID. Single-record loads are
// lock-free by design and safe from another goroutine.
type FeatureLoader interface {
	Load(id string) (*feature.Feature, error)
}

// EventObserver receives the notifier's observability events.
type EventObserver interface {
	Emit(observe.Event) error
}

// Clock abstracts time so pacing, retries, and rate-limit waits run
// without sleeping in tests.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) bool
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// NotifierOptions carries the notifier's constructor dependencies.
type NotifierOptions struct {
	// Settings supplies the live Slack configuration at processing time.
	// The token is read per item and never cached.
	Settings  ports.SlackSettingsSource
	Store     FeatureLoader
	StateDir  string
	Observer  EventObserver
	NewClient ClientFactory
	// QueueCapacity bounds the intake queue; tests lower it.
	QueueCapacity int
	// Clock controls pacing and backoff; tests inject a fake.
	Clock Clock
	// Jitter supplies the backoff jitter fraction in [0,1).
	Jitter func() float64
}

// Notifier turns feature lifecycle events into Slack notifications: one
// root card per default destination that is edited in place, with compact
// Progress replies accumulating in its thread.
type Notifier struct {
	settings  ports.SlackSettingsSource
	store     FeatureLoader
	stateDir  string
	observer  EventObserver
	newClient ClientFactory
	clock     Clock
	jitter    func() float64

	serverNameMu sync.RWMutex
	serverName   string

	stopped  atomic.Bool
	stopOnce sync.Once
	stopCh   chan struct{}

	requestBase context.Context
	cancelBase  context.CancelFunc

	queue *itemQueue

	dispatcherWG sync.WaitGroup
	workerWG     sync.WaitGroup
	observerWG   sync.WaitGroup
	dropEvents   chan observe.Event

	recordMu sync.Mutex
	records  map[string]*featureRecord
	// pendingPersistence retains failed durable writes so the next eligible
	// lifecycle event can retry even when it only refreshes the root card.
	pendingPersistence map[string]ports.SlackRecipientKind

	workerMu sync.Mutex
	workers  map[string]*destinationWorker
}

// NewNotifier constructs the notifier and its intake queue. Call Start
// before events flow, and give it the resolved server name first through
// SetServerName.
func NewNotifier(opts NotifierOptions) *Notifier {
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}
	jitter := opts.Jitter
	if jitter == nil {
		jitter = defaultJitter
	}
	notifier := &Notifier{
		settings:           opts.Settings,
		store:              opts.Store,
		stateDir:           opts.StateDir,
		observer:           opts.Observer,
		newClient:          opts.NewClient,
		clock:              clock,
		jitter:             jitter,
		stopCh:             make(chan struct{}),
		records:            map[string]*featureRecord{},
		pendingPersistence: map[string]ports.SlackRecipientKind{},
		workers:            map[string]*destinationWorker{},
	}
	notifier.requestBase, notifier.cancelBase = context.WithCancel(context.Background())
	dropCapacity := opts.QueueCapacity
	if dropCapacity <= 0 {
		dropCapacity = defaultQueueCapacity
	}
	notifier.dropEvents = make(chan observe.Event, dropCapacity)
	notifier.observerWG.Add(1)
	go notifier.runDropReporter()
	notifier.queue = newItemQueue(opts.QueueCapacity, func(item queueItem) {
		notifier.reportDrop(observe.Event{
			Timestamp: notifier.clock.Now(),
			EventType: "slack.event_dropped",
			FeatureID: item.event.FeatureID,
			Data: map[string]any{
				"event_type": eventTypeName(item.event.Type),
				"reason":     "queue_overflow",
			},
		})
	})
	return notifier
}

func defaultJitter() float64 {
	return rand.Float64()
}

// SetServerName gives the notifier the resolved runtime name, which is
// known only after the Fx graph starts. It must be set before processing
// begins.
func (n *Notifier) SetServerName(name string) {
	n.serverNameMu.Lock()
	n.serverName = name
	n.serverNameMu.Unlock()
}

func (n *Notifier) resolvedServerName() string {
	n.serverNameMu.RLock()
	defer n.serverNameMu.RUnlock()
	return n.serverName
}

// Start launches the dispatcher goroutine and opens intake.
func (n *Notifier) Start() {
	if n.stopped.Load() {
		return
	}
	n.dispatcherWG.Add(1)
	go n.runDispatcher()
}

// Stop shuts the notifier down inside the carried deadline: intake stops so
// a late tap is a no-op, pacing waits abort, in-flight requests finish
// within the deadline (or are aborted when it expires), undelivered items
// are dropped, and a second stop returns immediately.
func (n *Notifier) Stop(ctx context.Context) {
	n.stopOnce.Do(func() {
		n.stopped.Store(true)
		close(n.stopCh)
		done := make(chan struct{})
		go func() {
			n.dispatcherWG.Wait()
			n.workerWG.Wait()
			n.observerWG.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			// The deadline expired with requests still in flight; abort
			// them so the dispatcher and worker goroutines exit promptly.
			n.cancelBase()
		}
	})
}

// DomainEventTap is the non-blocking tap the SSE broker invokes for every
// supported orchestrator domain event. Other events are discarded
// before any lookup.
func (n *Notifier) DomainEventTap(ev ports.Event) {
	if n.stopped.Load() || !handledEvent(ev) {
		return
	}
	n.queue.enqueue(queueItem{kind: eventItemKind(ev), event: ev})
}

// RuntimeMessageTap is the non-blocking tap the SSE broker invokes for
// session runtime messages. Runtime notifications are intentionally ignored
// until a message category consumes them.
func (n *Notifier) RuntimeMessageTap(any) {}

func handledEvent(ev ports.Event) bool {
	switch ev.Type {
	case ports.FeatureStarted,
		ports.FeatureAdvanced,
		ports.PhaseStarted,
		ports.PhaseCompleted,
		ports.PublishStarted,
		ports.PublishCompleted,
		ports.FeatureCompleted,
		ports.FeatureFailed,
		ports.SetupFailed,
		ports.FeatureInterrupted,
		ports.FeatureRewound:
		return true
	case ports.RelationshipIntegrationChanged:
		return ev.CanonicalError != nil
	default:
		return false
	}
}

func eventItemKind(ev ports.Event) itemKind {
	switch ev.Type {
	case ports.FeatureFailed, ports.SetupFailed:
		return kindProblems
	case ports.PublishCompleted, ports.RelationshipIntegrationChanged:
		if ev.CanonicalError != nil || ev.Error != nil {
			return kindProblems
		}
	case ports.FeatureInterrupted, ports.FeatureRewound:
		return kindLifecycle
	}
	return kindProgress
}

func eventTypeName(t ports.EventType) string {
	switch t {
	case ports.FeatureStarted:
		return "feature.started"
	case ports.FeatureAdvanced:
		return "feature.advanced"
	case ports.PhaseStarted:
		return "phase.started"
	case ports.PhaseCompleted:
		return "phase.completed"
	case ports.PublishStarted:
		return "publish.started"
	case ports.PublishCompleted:
		return "publish.completed"
	case ports.FeatureCompleted:
		return "feature.completed"
	case ports.FeatureFailed:
		return "feature.failed"
	case ports.SetupFailed:
		return "setup.failed"
	case ports.FeatureInterrupted:
		return "feature.interrupted"
	case ports.FeatureRewound:
		return "feature.rewound"
	case ports.RelationshipIntegrationChanged:
		return "relationship.integration_changed"
	default:
		return "event"
	}
}

func (n *Notifier) runDispatcher() {
	defer n.dispatcherWG.Done()
	for {
		item, ok := n.queue.next()
		if ok {
			if item.reservation != nil && item.reservation.canceled.Load() {
				n.queue.complete(item)
			} else {
				n.processItem(item)
			}
		}
		if !ok {
			select {
			case <-n.stopCh:
				return
			case <-n.queue.signal:
			}
			continue
		}
		select {
		case <-n.stopCh:
			return
		default:
		}
	}
}

// processItem resolves one event into per-destination work. Every step is
// guarded: settings are read live, unusable configurations discard the
// event with zero Slack calls and no record writes.
func (n *Notifier) processItem(item queueItem) {
	settings := n.settings.SlackSettings()
	if !settings.Enabled || settings.Token == "" || len(settings.Recipients) == 0 {
		n.queue.complete(item)
		return
	}
	eventFeature := item.event.Feature
	var err error
	if eventFeature == nil {
		eventFeature, err = n.store.Load(item.event.FeatureID)
	}
	if err != nil || eventFeature == nil {
		n.emitEvent(item.event.FeatureID, "slack.event_dropped", map[string]any{
			"event_type": eventTypeName(item.event.Type),
			"reason":     "feature_load_failed",
		})
		if err != nil {
			log.Printf("slack-notifier: skipping %s for feature that cannot be loaded: %v",
				eventTypeName(item.event.Type), err)
		}
		n.queue.complete(item)
		return
	}

	owner := eventFeature
	if eventFeature.IsChild() {
		parent, err := n.store.Load(eventFeature.Parent.ParentID)
		if err != nil || parent == nil {
			n.emitEvent(item.event.FeatureID, "slack.event_dropped", map[string]any{
				"event_type": eventTypeName(item.event.Type),
				"reason":     "feature_load_failed",
			})
			if err != nil {
				log.Printf("slack-notifier: skipping %s for child whose parent cannot be loaded: %v",
					eventTypeName(item.event.Type), err)
			}
			n.queue.complete(item)
			return
		}
		owner = parent
	}

	record, err := n.recordFor(owner.ID)
	if err != nil {
		n.emitEvent(item.event.FeatureID, "slack.event_dropped", map[string]any{
			"event_type": eventTypeName(item.event.Type),
			"reason":     "record_load_failed",
		})
		log.Printf("slack-notifier: skipping %s for feature whose Slack record cannot be loaded: %v",
			eventTypeName(item.event.Type), err)
		n.queue.complete(item)
		return
	}
	n.retryPendingRecord(owner.ID, record)

	reply := replyForEvent(settings.Token, item.event, eventFeature)
	var work []workItem
	for _, recipient := range settings.Recipients {
		channelID, err := n.resolveDestination(settings, record, owner.ID, recipient)
		if err != nil {
			log.Printf("slack-notifier: skipping %s destination for %s: %v",
				recipient.Kind, eventTypeName(item.event.Type), err)
			continue
		}
		work = append(work, workItem{
			featureID:       owner.ID,
			sourceFeatureID: eventFeature.ID,
			destinationKey:  destinationKey(string(recipient.Kind), recipient.ID),
			kind:            string(recipient.Kind),
			channelID:       channelID,
			needsCard:       true,
			refresh:         true,
			reply:           reply,
		})
	}
	if len(work) == 0 {
		n.queue.complete(item)
		return
	}
	group := newDeliveryGroup(n.queue, item, len(work))
	for i := range work {
		work[i].delivery = group
		n.workerFor(work[i].channelID).enqueue(work[i])
	}
}

func replyForEvent(token string, ev ports.Event, f *feature.Feature) replyPayload {
	switch eventItemKind(ev) {
	case kindProblems:
		canonical := ev.CanonicalError
		if canonical == nil {
			detail := strings.TrimSpace(ev.Message)
			if detail == "" && ev.Error != nil {
				detail = ev.Error.Error()
			}
			fallback := errcat.New(errcat.InternalError, errcat.WithDiagnostics(detail))
			canonical = &fallback
		}
		if canonical.Class == errcat.ClassWarning {
			return replyPayload{kind: kindProblems}
		}
		blocks, fallback, code := renderProblem(token, *canonical, f)
		return replyPayload{kind: kindProblems, blocks: blocks, fallback: fallback, errorCode: code}
	default:
		line := renderProgress(ev, f)
		return replyPayload{kind: kindProgress, fallback: scrub(token, line)}
	}
}

// recordFor returns the in-memory record for a feature, loading it from disk
// on first contact after process start. The in-memory record is
// authoritative.
func (n *Notifier) recordFor(featureID string) (*featureRecord, error) {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()
	if record, ok := n.records[featureID]; ok {
		return record, nil
	}
	record, err := loadFeatureRecord(n.stateDir, featureID)
	if err != nil {
		return nil, err
	}
	n.records[featureID] = record
	return record, nil
}

// resolveDestination returns the Slack channel ID to write to, opening the
// direct message on first contact and caching it in the record.
func (n *Notifier) resolveDestination(
	settings ports.SlackRuntimeSettings,
	record *featureRecord,
	featureID string,
	recipient ports.SlackRecipient,
) (string, error) {
	key := destinationKey(string(recipient.Kind), recipient.ID)
	n.recordMu.Lock()
	entry, ok := record.Destinations[key]
	if ok && entry.ChannelID != "" {
		n.recordMu.Unlock()
		return entry.ChannelID, nil
	}
	n.recordMu.Unlock()

	if recipient.Kind == ports.SlackRecipientUser {
		client, err := n.newClient(settings.Token)
		if err != nil {
			return "", err
		}
		channelID, err := client.OpenConversation(n.requestBase, recipient.ID)
		if err != nil {
			return "", err
		}
		n.recordMu.Lock()
		entry = record.Destinations[key]
		entry.Kind = string(recipient.Kind)
		entry.SlackID = recipient.ID
		entry.DisplayName = recipient.DisplayName
		entry.ChannelID = channelID
		record.Destinations[key] = entry
		persistErr := n.persistRecordLocked(featureID, record, recipient.Kind)
		n.recordMu.Unlock()
		n.logPersistError(persistErr, recipient.Kind)
		return channelID, nil
	}
	if recipient.Kind != ports.SlackRecipientChannel {
		return "", errUnsupportedRecipientKind
	}
	n.recordMu.Lock()
	entry = record.Destinations[key]
	entry.Kind = string(recipient.Kind)
	entry.SlackID = recipient.ID
	entry.DisplayName = recipient.DisplayName
	entry.ChannelID = recipient.ID
	record.Destinations[key] = entry
	persistErr := n.persistRecordLocked(featureID, record, recipient.Kind)
	n.recordMu.Unlock()
	n.logPersistError(persistErr, recipient.Kind)
	return recipient.ID, nil
}

// persistRecordLocked writes the authoritative in-memory record and tracks a
// failed write for retry. The caller must hold recordMu.
func (n *Notifier) persistRecordLocked(
	featureID string,
	record *featureRecord,
	kind ports.SlackRecipientKind,
) error {
	err := persistFeatureRecord(n.stateDir, featureID, record)
	if err != nil {
		n.pendingPersistence[featureID] = kind
		return err
	}
	delete(n.pendingPersistence, featureID)
	return nil
}

// retryPendingRecord repairs a prior failed write on the next eligible
// lifecycle event, including events that only produce a chat.update.
func (n *Notifier) retryPendingRecord(featureID string, record *featureRecord) {
	n.recordMu.Lock()
	kind, pending := n.pendingPersistence[featureID]
	if !pending {
		n.recordMu.Unlock()
		return
	}
	err := n.persistRecordLocked(featureID, record, kind)
	n.recordMu.Unlock()
	n.logPersistError(err, kind)
}

var errUnsupportedRecipientKind = &unsupportedRecipientKindError{}

type unsupportedRecipientKindError struct{}

func (*unsupportedRecipientKindError) Error() string {
	return "unsupported Slack recipient kind"
}

func (n *Notifier) logPersistError(err error, kind ports.SlackRecipientKind) {
	if err != nil {
		log.Printf("slack-notifier: persisting the Slack record failed (destination kind %s): %v",
			kind, err)
	}
}

func (n *Notifier) emitEvent(featureID, eventType string, data map[string]any) {
	if n.observer == nil {
		return
	}
	_ = n.observer.Emit(observe.Event{
		Timestamp: n.clock.Now(),
		EventType: eventType,
		FeatureID: featureID,
		Data:      data,
	})
}

func (n *Notifier) reportDrop(evt observe.Event) {
	if n.observer == nil {
		return
	}
	select {
	case n.dropEvents <- evt:
	default:
	}
}

func (n *Notifier) runDropReporter() {
	defer n.observerWG.Done()
	for {
		select {
		case <-n.stopCh:
			return
		case evt := <-n.dropEvents:
			_ = n.observer.Emit(evt)
		}
	}
}

type deliveryGroup struct {
	queue     *itemQueue
	item      queueItem
	remaining atomic.Int64
}

func newDeliveryGroup(queue *itemQueue, item queueItem, count int) *deliveryGroup {
	group := &deliveryGroup{queue: queue, item: item}
	group.remaining.Store(int64(count))
	return group
}

func (g *deliveryGroup) done() {
	if g == nil {
		return
	}
	if g.remaining.Add(-1) == 0 {
		g.queue.complete(g.item)
	}
}
