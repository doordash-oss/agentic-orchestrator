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
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
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

// DeliveryReporter receives delivery-time credential state changes.
type DeliveryReporter interface {
	ReportSlackDeliveryFailure(
		at time.Time,
		credentialGeneration uint64,
		canonical errcat.Error,
	)
	ReportSlackDeliverySuccess(at time.Time, credentialGeneration uint64)
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
	Reporter  DeliveryReporter
	Pending   ports.SlackPendingInputSource
	Answer    ports.SlackAnswerPort
	NewClient ClientFactory
	// QueueCapacity bounds the intake queue; tests lower it.
	QueueCapacity int
	// Clock controls pacing and backoff; tests inject a fake.
	Clock Clock
	// ResponderClock controls the Slack reply polling cadence independently
	// from delivery pacing. Tests use a manually advanced clock.
	ResponderClock Clock
	// Jitter supplies the backoff jitter fraction in [0,1).
	Jitter func() float64
}

// Notifier turns feature lifecycle events into Slack notifications: one
// root card per default destination that is edited in place, with compact
// Progress replies accumulating in its thread.
type Notifier struct {
	settings       ports.SlackSettingsSource
	store          FeatureLoader
	stateDir       string
	observer       EventObserver
	reporter       DeliveryReporter
	pending        ports.SlackPendingInputSource
	answer         ports.SlackAnswerPort
	newClient      ClientFactory
	clock          Clock
	responderClock Clock
	jitter         func() float64

	serverNameMu sync.RWMutex
	serverName   string

	stopped  atomic.Bool
	stopOnce sync.Once
	stopCh   chan struct{}

	requestBase     context.Context
	cancelBase      context.CancelFunc
	responderBase   context.Context
	cancelResponder context.CancelFunc

	queue *itemQueue

	dispatcherWG sync.WaitGroup
	responderWG  sync.WaitGroup
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

	recheckMu     sync.Mutex
	recheckQueued map[string]bool

	inputMu           sync.Mutex
	trackedInputs     map[string]bool
	pendingDeliveries map[string]struct{}

	responderPollMu sync.Mutex
	responderPolls  map[string]responderPollState

	responderNameMu sync.Mutex
	responderNames  map[string]string

	responderThreadMu    sync.Mutex
	responderThreadLocks map[string]*sync.RWMutex

	responderFeedbackMu    sync.Mutex
	responderFeedback      map[string]int
	responderClaims        map[string]struct{}
	responderDeferred      map[string]responderDeferredFeedback
	responderDeferredOrder []string
	responderSubmissions   map[string]int
}

// NewNotifier constructs the notifier and its intake queue. Call Start
// before events flow, and give it the resolved server name first through
// SetServerName.
func NewNotifier(opts NotifierOptions) *Notifier {
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}
	responderClock := opts.ResponderClock
	if responderClock == nil {
		responderClock = clock
	}
	jitter := opts.Jitter
	if jitter == nil {
		jitter = defaultJitter
	}
	newClient := opts.NewClient
	if newClient == nil {
		newClient = func(token string) (slackClient, error) {
			return NewClient(token)
		}
	}
	notifier := &Notifier{
		settings:             opts.Settings,
		store:                opts.Store,
		stateDir:             opts.StateDir,
		observer:             opts.Observer,
		reporter:             opts.Reporter,
		pending:              opts.Pending,
		answer:               opts.Answer,
		newClient:            newClient,
		clock:                clock,
		responderClock:       responderClock,
		jitter:               jitter,
		stopCh:               make(chan struct{}),
		records:              map[string]*featureRecord{},
		pendingPersistence:   map[string]ports.SlackRecipientKind{},
		workers:              map[string]*destinationWorker{},
		recheckQueued:        map[string]bool{},
		trackedInputs:        map[string]bool{},
		pendingDeliveries:    map[string]struct{}{},
		responderPolls:       map[string]responderPollState{},
		responderNames:       map[string]string{},
		responderThreadLocks: map[string]*sync.RWMutex{},
		responderFeedback:    map[string]int{},
		responderClaims:      map[string]struct{}{},
		responderDeferred:    map[string]responderDeferredFeedback{},
		responderSubmissions: map[string]int{},
	}
	notifier.requestBase, notifier.cancelBase = context.WithCancel(context.Background())
	notifier.responderBase, notifier.cancelResponder = context.WithCancel(context.Background())
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
	if n.answer == nil {
		log.Printf("slack-notifier: answering from Slack is unavailable")
	}
	n.warmPendingRecords()
	n.dispatcherWG.Add(1)
	go n.runDispatcher()
	if n.answer != nil {
		n.responderWG.Add(1)
		go n.runResponder()
	}
}

// Stop shuts the notifier down inside the carried deadline: intake stops so
// a late tap is a no-op, pacing waits abort, in-flight requests finish
// within the deadline (or are aborted when it expires), undelivered items
// are dropped, and a second stop returns immediately.
func (n *Notifier) Stop(ctx context.Context) {
	n.stopOnce.Do(func() {
		n.stopped.Store(true)
		close(n.stopCh)
		n.cancelResponder()
		done := make(chan struct{})
		go func() {
			n.dispatcherWG.Wait()
			n.responderWG.Wait()
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
// session runtime messages.
func (n *Notifier) RuntimeMessageTap(message any) {
	if n.stopped.Load() {
		return
	}
	switch msg := message.(type) {
	case session.SDKEventMsg:
		if msg.FeatureID == "" || msg.SessionID == "__chat__" {
			return
		}
		if msg.Message.ControlRequest != nil {
			n.queue.enqueue(queueItem{
				kind:  kindNeedsInput,
				event: ports.Event{Type: ports.SessionOutput, FeatureID: msg.FeatureID},
			})
			return
		}
		n.enqueueRecheck(msg.FeatureID)
	case session.SessionDoneMsg:
		if msg.FeatureID != "" && msg.SessionID != "__chat__" {
			n.enqueueRecheck(msg.FeatureID)
		}
	}
}

func (n *Notifier) enqueueRecheck(featureID string) {
	n.inputMu.Lock()
	tracked := n.trackedInputs[featureID]
	n.inputMu.Unlock()
	if !tracked {
		return
	}
	n.recheckMu.Lock()
	if n.recheckQueued[featureID] {
		n.recheckMu.Unlock()
		return
	}
	n.recheckQueued[featureID] = true
	n.recheckMu.Unlock()
	if !n.queue.enqueue(queueItem{
		kind:        kindNeedsInput,
		event:       ports.Event{Type: ports.SessionOutput, FeatureID: featureID},
		recheckOnly: true,
	}) {
		n.clearRecheck(featureID)
	}
}

func (n *Notifier) clearRecheck(featureID string) {
	n.recheckMu.Lock()
	delete(n.recheckQueued, featureID)
	n.recheckMu.Unlock()
}

// SlackWarnings returns the current destination failures for configured recipients.
func (n *Notifier) SlackWarnings(featureID string) []errcat.Error {
	settings := n.settings.SlackSettings()
	if len(settings.Recipients) == 0 {
		return nil
	}
	record, err := n.recordFor(featureID)
	if err != nil {
		return nil
	}
	type warningSource struct {
		recipient string
		failure   destinationFailure
	}
	sources := make([]warningSource, 0, len(settings.Recipients))
	n.recordMu.Lock()
	for _, recipient := range settings.Recipients {
		key := destinationKey(string(recipient.Kind), recipient.ID)
		entry, ok := record.Destinations[key]
		if !ok || entry.Failure == nil {
			continue
		}
		sources = append(sources, warningSource{
			recipient: scrub(
				settings.Token,
				firstNonempty(entry.DisplayName, recipient.DisplayName, recipient.TypedText, recipient.ID),
			),
			failure: *entry.Failure,
		})
	}
	n.recordMu.Unlock()

	warnings := make([]errcat.Error, 0, len(sources))
	for _, source := range sources {
		warnings = append(warnings, errcat.New(
			source.failure.Code,
			errcat.WithParams(errcat.SlackDeliveryFailureParams{
				Recipient:    source.recipient,
				Cause:        scrub(settings.Token, source.failure.SlackError),
				MissedCount:  source.failure.Count,
				FirstFailure: source.failure.FirstFailedAt,
			}),
		))
	}
	return warnings
}

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
		ports.FeatureRewound,
		ports.NeedUserInputRequired,
		ports.ReviewRequired:
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
	case ports.NeedUserInputRequired, ports.ReviewRequired:
		return kindNeedsInput
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
	case ports.ReviewRequired:
		return "review.required"
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
	if item.recheckOnly {
		defer n.clearRecheck(item.event.FeatureID)
	}
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

	resolutionKind := resolutionAgentico
	if item.event.Type == ports.FeatureInterrupted || item.event.Type == ports.FeatureRewound {
		resolutionKind = resolutionCleared
	}
	work := n.reconcilePending(settings, owner, eventFeature, record, resolutionKind)
	if item.recheckOnly ||
		item.event.Type == ports.NeedUserInputRequired ||
		item.event.Type == ports.ReviewRequired ||
		item.event.Type == ports.SessionOutput {
		n.dispatchWork(item, work)
		return
	}

	reply := replyForEvent(settings.Token, item.event, eventFeature)
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
	n.dispatchWork(item, work)
}

func (n *Notifier) dispatchWork(item queueItem, work []workItem) {
	if len(work) == 0 {
		n.queue.complete(item)
		return
	}
	if item.kind != kindNeedsInput {
		var needsInputWork []workItem
		remainingWork := work[:0]
		for _, delivery := range work {
			if delivery.reply.kind == kindNeedsInput {
				needsInputWork = append(needsInputWork, delivery)
				continue
			}
			remainingWork = append(remainingWork, delivery)
		}
		if len(needsInputWork) > 0 {
			protectedItem := queueItem{
				kind:        kindNeedsInput,
				event:       item.event,
				reservation: n.queue.reserveProtected(item.event),
			}
			if (item.event.Type == ports.FeatureInterrupted ||
				item.event.Type == ports.FeatureRewound) &&
				len(remainingWork) > 0 {
				n.dispatchDeliveryGroup(item, remainingWork)
				n.dispatchDeliveryGroup(protectedItem, needsInputWork)
				return
			}
			n.dispatchDeliveryGroup(protectedItem, needsInputWork)
			if len(remainingWork) == 0 {
				n.queue.complete(item)
				return
			}
			work = remainingWork
		}
	}
	n.dispatchDeliveryGroup(item, work)
}

func (n *Notifier) dispatchDeliveryGroup(item queueItem, work []workItem) {
	n.dispatchDeliveryGroupWithDone(item, work, nil)
}

func (n *Notifier) dispatchDeliveryGroupWithDone(
	item queueItem,
	work []workItem,
	onDone func(),
) {
	group := newDeliveryGroup(n.queue, item, len(work))
	group.onDone = onDone
	for i := range work {
		work[i].delivery = group
		n.workerFor(work[i].channelID).enqueue(work[i])
	}
}

func (n *Notifier) reconcilePending(
	settings ports.SlackRuntimeSettings,
	owner, trigger *feature.Feature,
	record *featureRecord,
	retiredResolutionKind string,
) []workItem {
	if n.pending == nil {
		return nil
	}
	sourceIDs := map[string]bool{trigger.ID: true}
	n.recordMu.Lock()
	for _, tracked := range record.Pending {
		sourceIDs[tracked.SourceFeatureID] = true
	}
	n.recordMu.Unlock()
	orderedSources := make([]string, 0, len(sourceIDs))
	for sourceID := range sourceIDs {
		orderedSources = append(orderedSources, sourceID)
	}
	sort.Strings(orderedSources)

	liveByIdentity := map[string]ports.SlackPendingInput{}
	var liveOrder []string
	sourceFeatures := map[string]*feature.Feature{}
	for _, sourceID := range orderedSources {
		sourceFeature, err := n.store.Load(sourceID)
		if err != nil || sourceFeature == nil {
			continue
		}
		live, err := n.pending.PendingSlackInputs(sourceID)
		if err != nil {
			log.Printf("slack-notifier: reading pending inputs for feature %s failed: %v", sourceID, err)
			continue
		}
		sourceFeatures[sourceID] = sourceFeature
		for _, input := range live {
			input.FeatureID = sourceID
			if identity := pendingInputIdentity(input); identity != "" {
				if _, exists := liveByIdentity[identity]; !exists {
					liveOrder = append(liveOrder, identity)
				}
				liveByIdentity[identity] = input
			}
		}
	}

	changed := false
	n.recordMu.Lock()
	kept := record.Pending[:0]
	var retired []pendingInputRecord
	var closures []pendingInputRecord
	for _, tracked := range record.Pending {
		if _, sourceKnown := sourceFeatures[tracked.SourceFeatureID]; !sourceKnown {
			kept = append(kept, tracked)
			continue
		}
		if _, live := liveByIdentity[tracked.Identity]; live {
			kept = append(kept, tracked)
		} else if n.responderSubmissionInFlight(owner.ID, tracked.Identity) {
			kept = append(kept, tracked)
		} else {
			if tracked.Resolution == nil {
				tracked.Resolution = &postingResolution{
					Kind:       retiredResolutionKind,
					ResolvedAt: n.clock.Now().UTC(),
				}
			}
			if tracked.Resolution.Kind != resolutionSlack &&
				!tracked.Resolution.ClosureSent {
				tracked.Resolution.ClosureSent = true
				closures = append(closures, tracked)
			}
			record.propagatePostingResolution(tracked.Identity, tracked.Resolution)
			retired = append(retired, tracked)
			changed = true
		}
	}
	record.Pending = kept
	existing := make(map[string]int, len(record.Pending))
	for i := range record.Pending {
		existing[record.Pending[i].Identity] = i
	}
	for _, identity := range liveOrder {
		input := liveByIdentity[identity]
		if _, ok := existing[identity]; ok {
			continue
		}
		record.Pending = append(record.Pending, pendingInputRecord{
			Identity:        identity,
			SourceFeatureID: input.FeatureID,
			Kind:            string(input.Kind),
			RequestID:       input.RequestID,
			QuestionIndex:   input.QuestionIndex,
			GatePath:        input.GatePath,
			Iteration:       input.Iteration,
			WaitingSince:    input.WaitingSince.UTC(),
			ReviewID:        scrub(settings.Token, input.ReviewID),
			ReviewMode:      scrub(settings.Token, input.ReviewMode),
			TargetPhase:     scrub(settings.Token, input.TargetPhase),
			ArtifactID:      scrub(settings.Token, input.ArtifactID),
			RunNumber:       input.RunNumber,
			SourceRevision:  scrub(settings.Token, input.SourceRevision),
		})
		existing[identity] = len(record.Pending) - 1
		changed = true
	}
	if settings.Categories.NeedsInput {
		for i := range record.Pending {
			if record.Pending[i].Tag != "" {
				continue
			}
			if _, live := liveByIdentity[record.Pending[i].Identity]; !live {
				continue
			}
			record.TagCounter++
			record.Pending[i].Tag = fmt.Sprintf("#%d", record.TagCounter)
			changed = true
		}
	}
	hasPostedPending := false
	for _, tracked := range record.Pending {
		if pendingHasPostedMessage(tracked) {
			hasPostedPending = true
			break
		}
	}
	if hasPostedPending {
		for _, tracked := range retired {
			if pendingHasPostedMessage(tracked) {
				record.Resolved = append(record.Resolved, tracked)
			}
		}
		if len(record.Resolved) > responderResolvedRetentionLimit {
			record.Resolved = append(
				[]pendingInputRecord(nil),
				record.Resolved[len(record.Resolved)-responderResolvedRetentionLimit:]...,
			)
		}
	} else if len(record.Resolved) > 0 || len(retired) > 0 {
		record.Resolved = nil
		for key, destination := range record.Destinations {
			destination.PostingIndex = nil
			destination.SubmittedReplies = nil
			record.Destinations[key] = destination
		}
	}
	n.refreshTrackedInputsLocked()
	if changed {
		var kind ports.SlackRecipientKind
		if len(settings.Recipients) > 0 {
			kind = settings.Recipients[0].Kind
		}
		err := n.persistRecordLocked(owner.ID, record, kind)
		n.recordMu.Unlock()
		n.logPersistError(err, kind)
	} else {
		n.recordMu.Unlock()
	}

	var work []workItem
	for _, tracked := range closures {
		line := tracked.Tag + " was resolved in Agentico."
		if tracked.Resolution.Kind == resolutionCleared {
			line = tracked.Tag + " is no longer pending."
		}
		for _, recipient := range settings.Recipients {
			key := destinationKey(string(recipient.Kind), recipient.ID)
			if tracked.MessageTS[key] == "" {
				continue
			}
			destination := record.Destinations[key]
			if destination.ChannelID == "" || destination.RootTS == "" {
				continue
			}
			work = append(work, workItem{
				featureID:       owner.ID,
				sourceFeatureID: tracked.SourceFeatureID,
				destinationKey:  key,
				kind:            string(recipient.Kind),
				channelID:       destination.ChannelID,
				responder:       true,
				reply: replyPayload{
					kind: kindNeedsInput, fallback: scrub(settings.Token, line),
				},
			})
		}
	}
	for _, recipient := range settings.Recipients {
		channelID, err := n.resolveDestination(settings, record, owner.ID, recipient)
		if err != nil {
			log.Printf("slack-notifier: skipping %s destination for needs input: %v", recipient.Kind, err)
			continue
		}
		key := destinationKey(string(recipient.Kind), recipient.ID)
		postedAny := false
		if settings.Categories.NeedsInput {
			n.recordMu.Lock()
			type pendingDelivery struct {
				sourceFeatureID string
				kind            string
				identity        string
				tag             string
			}
			pendingSnapshot := make([]pendingDelivery, 0, len(record.Pending))
			for _, tracked := range record.Pending {
				if tracked.Tag == "" || tracked.MessageTS[key] != "" {
					continue
				}
				if !n.reservePendingDelivery(owner.ID, tracked.Identity, key) {
					continue
				}
				pendingSnapshot = append(pendingSnapshot, pendingDelivery{
					sourceFeatureID: tracked.SourceFeatureID,
					kind:            tracked.Kind,
					identity:        tracked.Identity,
					tag:             tracked.Tag,
				})
			}
			n.recordMu.Unlock()
			sort.SliceStable(pendingSnapshot, func(i, j int) bool {
				return tagNumber(pendingSnapshot[i].tag) < tagNumber(pendingSnapshot[j].tag)
			})
			for _, tracked := range pendingSnapshot {
				input, live := liveByIdentity[tracked.identity]
				if !live {
					n.releasePendingDelivery(owner.ID, tracked.identity, key)
					continue
				}
				blocks, fallback := renderPendingInput(
					settings.Token, tracked.tag, input, sourceFeatures[tracked.sourceFeatureID],
				)
				work = append(work, workItem{
					featureID:       owner.ID,
					sourceFeatureID: tracked.sourceFeatureID,
					destinationKey:  key,
					kind:            string(recipient.Kind),
					channelID:       channelID,
					needsCard:       true,
					refresh:         true,
					reply: replyPayload{
						kind: kindNeedsInput, fallback: fallback, blocks: blocks,
						inputKind: tracked.kind, identity: tracked.identity, tag: tracked.tag,
						review: reviewDeliveryFor(
							input,
							sourceFeatures[tracked.sourceFeatureID],
						),
					},
				})
				postedAny = true
			}
		}
		if changed && !postedAny {
			work = append(work, workItem{
				featureID:       owner.ID,
				sourceFeatureID: trigger.ID,
				destinationKey:  key,
				kind:            string(recipient.Kind),
				channelID:       channelID,
				needsCard:       true,
				refresh:         true,
				reply:           replyPayload{kind: kindNeedsInput},
			})
		}
	}
	return work
}

func pendingHasPostedMessage(input pendingInputRecord) bool {
	for _, messageTS := range input.MessageTS {
		if messageTS != "" {
			return true
		}
	}
	return false
}

func reviewDeliveryFor(input ports.SlackPendingInput, source *feature.Feature) *reviewDelivery {
	if input.Kind != ports.SlackPendingReview {
		return nil
	}
	return &reviewDelivery{input: input, source: source}
}

func (n *Notifier) waitingLine(record *featureRecord, repliesEnabled bool) string {
	n.recordMu.Lock()
	pending := append([]pendingInputRecord(nil), record.Pending...)
	n.recordMu.Unlock()
	return waitingSummary(pending, repliesEnabled)
}

func (n *Notifier) warmPendingRecords() {
	entries, err := os.ReadDir(n.stateDir)
	if err != nil {
		return
	}
	n.recordMu.Lock()
	defer n.recordMu.Unlock()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		featureID := entry.Name()
		if _, loaded := n.records[featureID]; loaded {
			continue
		}
		if _, err := os.Stat(filepath.Join(n.stateDir, featureID, recordFilename)); err != nil {
			continue
		}
		record, err := loadFeatureRecord(n.stateDir, featureID)
		if err == nil {
			n.records[featureID] = record
		}
	}
	n.refreshTrackedInputsLocked()
}

// refreshTrackedInputsLocked publishes the durable pending set as immutable
// admission state for the runtime tap. The caller must hold recordMu.
func (n *Notifier) refreshTrackedInputsLocked() {
	tracked := make(map[string]bool)
	for ownerID, record := range n.records {
		if len(record.Pending) > 0 {
			tracked[ownerID] = true
		}
		for _, item := range record.Pending {
			tracked[item.SourceFeatureID] = true
		}
	}
	n.inputMu.Lock()
	n.trackedInputs = tracked
	n.inputMu.Unlock()
}

func pendingDeliveryKey(featureID, identity, destinationKey string) string {
	return featureID + "\x00" + identity + "\x00" + destinationKey
}

func (n *Notifier) reservePendingDelivery(featureID, identity, destinationKey string) bool {
	key := pendingDeliveryKey(featureID, identity, destinationKey)
	n.inputMu.Lock()
	defer n.inputMu.Unlock()
	if _, reserved := n.pendingDeliveries[key]; reserved {
		return false
	}
	n.pendingDeliveries[key] = struct{}{}
	return true
}

func (n *Notifier) releasePendingDelivery(featureID, identity, destinationKey string) {
	if identity == "" {
		return
	}
	n.inputMu.Lock()
	delete(n.pendingDeliveries, pendingDeliveryKey(featureID, identity, destinationKey))
	n.inputMu.Unlock()
}

func (n *Notifier) pendingDeliveryEligible(
	record *featureRecord,
	featureID, identity, destinationKey string,
) bool {
	if identity == "" {
		return true
	}
	n.recordMu.Lock()
	pending := false
	for _, item := range record.Pending {
		if item.Identity == identity && item.MessageTS[destinationKey] == "" {
			pending = true
			break
		}
	}
	n.recordMu.Unlock()
	if !pending {
		return false
	}
	n.inputMu.Lock()
	_, reserved := n.pendingDeliveries[pendingDeliveryKey(featureID, identity, destinationKey)]
	n.inputMu.Unlock()
	return reserved
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
	n.refreshTrackedInputsLocked()
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
		entry.DisplayName = scrub(settings.Token, recipient.DisplayName)
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
	entry.DisplayName = scrub(settings.Token, recipient.DisplayName)
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

func (n *Notifier) reportWriteFailure(
	item workItem,
	itemKind string,
	attempts int,
	credential deliveryCredential,
	failure writeFailure,
) {
	at := n.clock.Now()
	if failure.class == deliveryFailureCredential {
		if n.reporter != nil && failure.canonical != nil {
			n.reporter.ReportSlackDeliveryFailure(at, credential.generation, *failure.canonical)
		}
	} else if !item.suppressDestinationFailure {
		n.recordDestinationFailure(item, at, credential.token, failure)
	}
	data := map[string]any{
		"destination_kind": item.kind,
		"item_kind":        itemKind,
		"failure_class":    string(failure.class),
		"slack_error":      failure.slackError,
		"error_code":       string(failure.errorCode),
		"attempts":         attempts,
	}
	if itemKind == "review_artifact" && item.reply.tag != "" {
		data["tag"] = item.reply.tag
	}
	n.reportDrop(observe.Event{
		Timestamp: at,
		EventType: "slack.delivery_failed",
		FeatureID: item.featureID,
		Data:      data,
	})
}

func (n *Notifier) recordDestinationFailure(
	item workItem,
	at time.Time,
	attemptToken string,
	failure writeFailure,
) {
	if item.featureID == "" || item.destinationKey == "" {
		return
	}
	record, err := n.recordFor(item.featureID)
	if err != nil {
		log.Printf(
			"slack-notifier: recording a %s destination failure failed: %v",
			item.kind,
			err,
		)
		return
	}
	n.recordMu.Lock()
	entry := record.Destinations[item.destinationKey]
	entry.DisplayName = scrub(attemptToken, entry.DisplayName)
	entry.recordFailure(failure.errorCode, scrub(attemptToken, failure.slackError), at)
	record.Destinations[item.destinationKey] = entry
	persistErr := n.persistRecordLocked(
		item.featureID,
		record,
		ports.SlackRecipientKind(item.kind),
	)
	n.recordMu.Unlock()
	n.logPersistError(persistErr, ports.SlackRecipientKind(item.kind))
}

func (n *Notifier) reportWriteSuccess(item workItem, credentialGeneration uint64) {
	at := n.clock.Now()
	if n.reporter != nil {
		n.reporter.ReportSlackDeliverySuccess(at, credentialGeneration)
	}
	if !item.poll {
		n.rearmResponderPoll(item.featureID, item.destinationKey)
	}
	if item.suppressDestinationFailure {
		return
	}
	if item.featureID == "" || item.destinationKey == "" {
		return
	}
	record, err := n.recordFor(item.featureID)
	if err != nil {
		return
	}
	n.recordMu.Lock()
	entry, ok := record.Destinations[item.destinationKey]
	if !ok || entry.Failure == nil {
		n.recordMu.Unlock()
		return
	}
	entry.Failure = nil
	record.Destinations[item.destinationKey] = entry
	persistErr := n.persistRecordLocked(
		item.featureID,
		record,
		ports.SlackRecipientKind(item.kind),
	)
	n.recordMu.Unlock()
	n.logPersistError(persistErr, ports.SlackRecipientKind(item.kind))
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
	onDone    func()
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
		if g.onDone != nil {
			g.onDone()
		}
	}
}
