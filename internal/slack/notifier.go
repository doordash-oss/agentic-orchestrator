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
	"sync"
	"sync/atomic"
	"time"

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
	settings      ports.SlackSettingsSource
	store         FeatureLoader
	stateDir      string
	observer      EventObserver
	newClient     ClientFactory
	clock         Clock
	jitter        func() float64
	queueCapacity int

	serverNameMu sync.RWMutex
	serverName   string

	stopped  atomic.Bool
	stopOnce sync.Once
	stopCh   chan struct{}

	requestBase context.Context
	cancelBase  context.CancelFunc

	// processing spans each dispatcher item from dequeue to completion so
	// the coalesced card refresh can distinguish a drained pipeline from
	// one with an item still in flight.
	processing atomic.Bool

	queue *itemQueue

	dispatcherWG sync.WaitGroup
	workerWG     sync.WaitGroup

	recordMu sync.Mutex
	records  map[string]*featureRecord

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
		settings:      opts.Settings,
		store:         opts.Store,
		stateDir:      opts.StateDir,
		observer:      opts.Observer,
		newClient:     opts.NewClient,
		clock:         clock,
		jitter:        jitter,
		queueCapacity: opts.QueueCapacity,
		stopCh:        make(chan struct{}),
		records:       map[string]*featureRecord{},
		workers:       map[string]*destinationWorker{},
	}
	notifier.requestBase, notifier.cancelBase = context.WithCancel(context.Background())
	notifier.queue = newItemQueue(opts.QueueCapacity, func(item queueItem) {
		notifier.emitEvent(item.event.FeatureID, "slack.event_dropped", map[string]any{
			"event_type": eventTypeName(item.event.Type),
			"reason":     "queue_overflow",
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
// orchestrator domain event. Events outside this phase's set are discarded
// before any lookup.
func (n *Notifier) DomainEventTap(ev ports.Event) {
	if n.stopped.Load() || !handledEventType(ev.Type) {
		return
	}
	n.queue.enqueue(queueItem{kind: kindProgress, event: ev})
}

// RuntimeMessageTap is the non-blocking tap the SSE broker invokes for
// session runtime messages. This phase discards them; Phase 6 acts on them.
func (n *Notifier) RuntimeMessageTap(any) {}

func handledEventType(t ports.EventType) bool {
	switch t {
	case ports.FeatureStarted,
		ports.FeatureAdvanced,
		ports.PhaseStarted,
		ports.PhaseCompleted,
		ports.PublishStarted,
		ports.PublishCompleted,
		ports.FeatureCompleted:
		return true
	default:
		return false
	}
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
	default:
		return "event"
	}
}

func (n *Notifier) runDispatcher() {
	defer n.dispatcherWG.Done()
	for {
		n.processing.Store(true)
		item, ok := n.queue.next()
		if ok {
			n.processItem(item)
		}
		n.processing.Store(false)
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
		return
	}
	eventFeature, err := n.store.Load(item.event.FeatureID)
	if err != nil || eventFeature == nil {
		n.emitEvent(item.event.FeatureID, "slack.event_dropped", map[string]any{
			"event_type": eventTypeName(item.event.Type),
			"reason":     "feature_load_failed",
		})
		if err != nil {
			log.Printf("slack-notifier: skipping %s for feature that cannot be loaded: %v",
				eventTypeName(item.event.Type), err)
		}
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
		return
	}

	line := renderProgress(item.event, eventFeature)
	if line != "" && !settings.Categories.Progress {
		line = ""
	}

	for _, recipient := range settings.Recipients {
		channelID, err := n.resolveDestination(settings, record, owner.ID, recipient)
		if err != nil {
			log.Printf("slack-notifier: skipping %s destination for %s: %v",
				recipient.Kind, eventTypeName(item.event.Type), err)
			continue
		}
		worker := n.workerFor(channelID)
		worker.enqueue(workItem{
			featureID:      owner.ID,
			feature:        owner,
			destinationKey: destinationKey(string(recipient.Kind), recipient.ID),
			kind:           string(recipient.Kind),
			channelID:      channelID,
			token:          settings.Token,
			line:           line,
			needsCard:      true,
			refresh:        true,
		})
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
		persistErr := persistFeatureRecord(n.stateDir, featureID, record)
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
	persistErr := persistFeatureRecord(n.stateDir, featureID, record)
	n.recordMu.Unlock()
	n.logPersistError(persistErr, recipient.Kind)
	return recipient.ID, nil
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
