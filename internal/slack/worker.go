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
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// Pacing honouring Slack's limits: every write to a destination, posts and
// updates alike, waits at least this long after the previous one.
const writePaceInterval = time.Second

// Coalesced card refresh: a destination sends at most one chat.update per
// second, always rendering from the freshest record state.
const updatePaceInterval = time.Second

// maxCardStaleness bounds how long a continuously-busy feature can defer
// its refresh: a burst costs one edit, but a stream of events never
// starves the card update.
const maxCardStaleness = 3 * time.Second

// retryLimit bounds transient-failure retries (429, timeouts, transport
// errors, 5xx) per write.
const retryLimit = 3

// backoffBase seeds the exponential backoff. Jitter scales each delay into
// [0.8, 1.2) of the nominal delay so growth stays strict while delays vary.
const backoffBase = 500 * time.Millisecond

// sleepChunk keeps deferred waits responsive to shutdown without a timer
// abstraction: at most this much time per wait slice.
const sleepChunk = 100 * time.Millisecond

// workItem is one destination-bound unit of delivery prepared by the
// dispatcher.
type workItem struct {
	featureID      string
	feature        *feature.Feature
	destinationKey string
	kind           string
	channelID      string
	token          string
	line           string
	needsCard      bool
	refresh        bool
}

// dirtyEntry tracks one feature whose card needs a refresh: markedAt is
// the trailing edge (the last event that touched the feature) and firstAt
// bounds the total staleness of a continuously-busy feature.
type dirtyEntry struct {
	featureID string
	markedAt  time.Time
	firstAt   time.Time
}

// destinationWorker serializes every write to one Slack channel ID so
// thread replies land in event order, paces writes, retries transient
// failures, and coalesces card refreshes.
type destinationWorker struct {
	notifier  *Notifier
	channelID string

	mu         sync.Mutex
	inbox      []workItem
	signal     chan struct{}
	dirty      []dirtyEntry
	lastWrite  time.Time
	lastUpdate time.Time
}

func (n *Notifier) workerFor(channelID string) *destinationWorker {
	n.workerMu.Lock()
	defer n.workerMu.Unlock()
	if worker, ok := n.workers[channelID]; ok {
		return worker
	}
	worker := &destinationWorker{
		notifier:  n,
		channelID: channelID,
		signal:    make(chan struct{}, 1),
	}
	n.workers[channelID] = worker
	n.workerWG.Add(1)
	go worker.run()
	return worker
}

// enqueue never blocks the dispatcher.
func (w *destinationWorker) enqueue(item workItem) {
	w.mu.Lock()
	w.inbox = append(w.inbox, item)
	w.mu.Unlock()
	select {
	case w.signal <- struct{}{}:
	default:
	}
}

func (w *destinationWorker) next() (workItem, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.inbox) == 0 {
		return workItem{}, false
	}
	item := w.inbox[0]
	w.inbox = w.inbox[1:]
	return item, true
}

func (w *destinationWorker) markDirty(featureID string) {
	now := w.notifier.clock.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range w.dirty {
		if w.dirty[i].featureID == featureID {
			w.dirty[i].markedAt = now
			return
		}
	}
	w.dirty = append(w.dirty, dirtyEntry{featureID: featureID, markedAt: now, firstAt: now})
}

func (w *destinationWorker) hasDirty() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.dirty) > 0
}

func (w *destinationWorker) run() {
	defer w.notifier.workerWG.Done()
	for {
		select {
		case <-w.notifier.stopCh:
			return
		default:
		}
		// Pending work wins over the coalesced refresh: the inbox drains
		// in order first, so a burst of events costs one edit.
		item, ok := w.next()
		if ok {
			w.handle(item)
			continue
		}
		if w.hasDirty() {
			if !w.flushDue() {
				return
			}
			continue
		}
		select {
		case <-w.notifier.stopCh:
			return
		case <-w.signal:
		}
	}
}

// handle delivers one item: ensure the root card, post the Progress reply,
// then mark the destination dirty for the coalesced refresh.
func (w *destinationWorker) handle(item workItem) {
	if item.needsCard {
		if err := w.ensureCard(item); err != nil {
			log.Printf("slack-notifier: root card for feature %s to %s destination was not delivered: %v",
				item.featureID, item.kind, err)
		}
	}
	if item.line != "" {
		if err := w.postReply(item); err != nil {
			log.Printf("slack-notifier: progress reply for feature %s to %s destination was not delivered: %v",
				item.featureID, item.kind, err)
		}
	}
	if item.refresh {
		w.markDirty(item.featureID)
	}
}

// ensureCard posts the root card once per destination; after that the
// coalesced update path keeps it current.
func (w *destinationWorker) ensureCard(item workItem) error {
	notifier := w.notifier
	record, err := notifier.recordFor(item.featureID)
	if err != nil {
		return err
	}
	notifier.recordMu.Lock()
	rootTS := record.Destinations[item.destinationKey].RootTS
	notifier.recordMu.Unlock()
	if rootTS != "" {
		return nil
	}

	client, err := notifier.newClient(item.token)
	if err != nil {
		return err
	}
	blocks, fallback := renderRootCard(notifier.resolvedServerName(), item.feature, notifier.clock.Now())
	if !w.pace() {
		return errWorkerStopped
	}
	defer w.recordWrite()
	send := func() (PostMessageResult, error) {
		return client.PostMessageRich(notifier.requestBase, PostMessageInput{
			Channel:      item.channelID,
			FallbackText: fallback,
			Blocks:       blocks,
		})
	}
	result, err := sendWithRetry(w, "root card", item, send)
	if err != nil {
		return err
	}

	notifier.recordMu.Lock()
	entry := record.Destinations[item.destinationKey]
	entry.RootTS = result.TS
	entry.ledgerAppend(result.TS)
	record.Destinations[item.destinationKey] = entry
	persistErr := persistFeatureRecord(notifier.stateDir, item.featureID, record)
	notifier.recordMu.Unlock()
	notifier.logPersistError(persistErr, ports.SlackRecipientKind(item.kind))
	notifier.emitEvent(item.featureID, "slack.root_card_updated", map[string]any{
		"destination_kind": item.kind,
		"action":           "posted",
	})
	return nil
}

// postReply sends one Progress line into the card's thread and records the
// timestamp in the ledger before the item completes.
func (w *destinationWorker) postReply(item workItem) error {
	notifier := w.notifier
	record, err := notifier.recordFor(item.featureID)
	if err != nil {
		return err
	}
	notifier.recordMu.Lock()
	rootTS := record.Destinations[item.destinationKey].RootTS
	notifier.recordMu.Unlock()
	if rootTS == "" {
		return errors.New("no root card to reply to")
	}

	client, err := notifier.newClient(item.token)
	if err != nil {
		return err
	}
	if !w.pace() {
		return errWorkerStopped
	}
	defer w.recordWrite()
	send := func() (PostMessageResult, error) {
		return client.PostMessageRich(notifier.requestBase, PostMessageInput{
			Channel:      item.channelID,
			FallbackText: item.line,
			ThreadTS:     rootTS,
		})
	}
	result, err := sendWithRetry(w, "progress reply", item, send)
	if err != nil {
		return err
	}

	notifier.recordMu.Lock()
	entry := record.Destinations[item.destinationKey]
	entry.ledgerAppend(result.TS)
	record.Destinations[item.destinationKey] = entry
	persistErr := persistFeatureRecord(notifier.stateDir, item.featureID, record)
	notifier.recordMu.Unlock()
	notifier.logPersistError(persistErr, ports.SlackRecipientKind(item.kind))
	notifier.emitEvent(item.featureID, "slack.message_posted", map[string]any{
		"destination_kind": item.kind,
		"item_kind":        "progress",
	})
	return nil
}

// flushDue drains the coalesced refresh backlog at most one update per
// second. A refresh waits for a quiescent pipeline — no queued events, no
// dispatch in flight, no pending writes — plus the trailing-edge deadline
// (one second after the event that last touched the feature, capped by the
// staleness bound), so a burst of events costs one edit. It reports false
// when shutdown aborted the wait.
func (w *destinationWorker) flushDue() bool {
	// One pass: the run loop re-enters after draining whatever arrived, so
	// a pending inbox item can never wedge the refresh loop.
	if !w.hasDirty() {
		return true
	}
	return w.flushNext()
}

func (w *destinationWorker) flushNext() bool {
	for {
		select {
		case <-w.notifier.stopCh:
			return false
		default:
		}
		if !w.inboxEmpty() {
			// New work arrived; the run loop drains it first and the
			// refresh re-defers to the trailing edge.
			return true
		}
		now := w.notifier.clock.Now()
		quiescent := w.notifier.queue.len() == 0 && !w.notifier.processing.Load()
		// The decision and the pop commit together under the lock so an
		// enqueue racing the flush decision defers it instead of leaving
		// an unhandled item behind a fired flush.
		w.mu.Lock()
		if len(w.inbox) > 0 {
			w.mu.Unlock()
			return true
		}
		if len(w.dirty) == 0 {
			w.mu.Unlock()
			return true
		}
		entry := w.dirty[0]
		deadline := earliestTime(
			entry.markedAt.Add(updatePaceInterval),
			entry.firstAt.Add(maxCardStaleness),
		)
		deadline = latestTime(deadline,
			latestTime(w.lastWrite, w.lastUpdate).Add(updatePaceInterval))
		if quiescent && !now.Before(deadline) {
			w.dirty = w.dirty[1:]
			w.mu.Unlock()
			w.flushOne(entry.featureID)
			return true
		}
		w.mu.Unlock()
		wait := deadline.Sub(now)
		if !quiescent || wait <= 0 {
			wait = sleepChunk
		}
		if wait > sleepChunk {
			wait = sleepChunk
		}
		if !w.notifier.clock.Sleep(w.notifier.requestBase, wait) {
			return false
		}
	}
}

func (w *destinationWorker) inboxEmpty() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.inbox) == 0
}

// flushOne renders one dirty feature's card from the freshest record state
// and edits the card in place.
func (w *destinationWorker) flushOne(featureID string) {
	notifier := w.notifier
	w.mu.Lock()
	w.lastUpdate = notifier.clock.Now()
	w.mu.Unlock()

	settings := notifier.settings.SlackSettings()
	if !settings.Enabled || settings.Token == "" {
		// Slack was disabled mid-run: no further request is made.
		return
	}
	f, err := notifier.store.Load(featureID)
	if err != nil || f == nil {
		log.Printf("slack-notifier: dropping card refresh for feature %s: feature cannot be loaded", featureID)
		return
	}
	record, err := notifier.recordFor(featureID)
	if err != nil {
		log.Printf("slack-notifier: dropping card refresh for feature %s: Slack record cannot be loaded: %v",
			featureID, err)
		return
	}
	_, rootTS, kind, channelID, hasDestination := w.destinationFor(record, f)
	if !hasDestination || rootTS == "" {
		return
	}
	client, err := notifier.newClient(settings.Token)
	if err != nil {
		log.Printf("slack-notifier: dropping card refresh for feature %s: %v", featureID, err)
		return
	}
	blocks, fallback := renderRootCard(notifier.resolvedServerName(), f, notifier.clock.Now())
	if !w.pace() {
		return
	}
	defer w.recordWrite()
	send := func() (struct{}, error) {
		return struct{}{}, client.UpdateMessage(notifier.requestBase, channelID, rootTS, fallback, blocks)
	}
	if _, err := sendWithRetry(w, "card update", workItem{kind: kind}, send); err != nil {
		return
	}
	notifier.emitEvent(featureID, "slack.root_card_updated", map[string]any{
		"destination_kind": kind,
		"action":           "edited",
	})
}

// destinationFor resolves the worker's channel back to the record entry
// for a feature: the destination whose cached channel ID matches.
func (w *destinationWorker) destinationFor(record *featureRecord, f *feature.Feature) (
	key, rootTS, kind, channelID string, ok bool,
) {
	notifier := w.notifier
	notifier.recordMu.Lock()
	defer notifier.recordMu.Unlock()
	for key, entry := range record.Destinations {
		if entry.ChannelID == w.channelID {
			return key, entry.RootTS, entry.Kind, entry.ChannelID, true
		}
	}
	return "", "", "", "", false
}

// pace waits until the destination may write again: at least
// writePaceInterval after the previous write completed, across posts and
// updates alike. The caller records the write's completion through
// recordWrite once the request round-trip finishes, so the next write is
// spaced from the response that Slack actually observed. pace reports
// false when shutdown aborted the wait.
func (w *destinationWorker) pace() bool {
	deadline := w.lastWrite.Add(writePaceInterval)
	if w.notifier.clock.Now().Before(deadline) {
		if !w.waitUntil(deadline) {
			return false
		}
	}
	return true
}

func (w *destinationWorker) recordWrite() {
	w.mu.Lock()
	w.lastWrite = w.notifier.clock.Now()
	w.mu.Unlock()
}

// waitUntil sleeps in bounded slices until the clock reaches the deadline,
// staying responsive to shutdown. It reports false when shutdown or request
// cancellation aborted the wait.
func (w *destinationWorker) waitUntil(deadline time.Time) bool {
	for {
		select {
		case <-w.notifier.stopCh:
			return false
		default:
		}
		remaining := deadline.Sub(w.notifier.clock.Now())
		if remaining <= 0 {
			return true
		}
		if remaining > sleepChunk {
			remaining = sleepChunk
		}
		if !w.notifier.clock.Sleep(w.notifier.requestBase, remaining) {
			return false
		}
	}
}

var errWorkerStopped = errors.New("slack notifier stopped")

// sendWithRetry runs one Slack write with the destination's retry policy:
// a 429 pauses for the returned wait (at least one second) and retries the
// same item, other transient failures retry up to three times with
// jittered exponential backoff, and a permanent Slack error drops the item
// with no retry. Every give-up is logged with the Slack error code and
// destination kind; the token never reaches the log.
func sendWithRetry[T any](
	w *destinationWorker,
	label string,
	item workItem,
	send func() (T, error),
) (T, error) {
	var zero T
	notifier := w.notifier
	attempts := 0
	for {
		result, err := send()
		if err == nil {
			return result, nil
		}
		select {
		case <-notifier.stopCh:
			return zero, err
		default:
		}

		var apiErr *APIError
		if errors.As(err, &apiErr) {
			log.Printf("slack-notifier: giving up on %s to %s destination: Slack error %s",
				label, item.kind, apiErr.SlackError)
			return zero, err
		}
		var transportErr *TransportError
		if !errors.As(err, &transportErr) {
			log.Printf("slack-notifier: giving up on %s to %s destination: %v",
				label, item.kind, err)
			return zero, err
		}

		var wait time.Duration
		switch {
		case transportErr.StatusCode == http.StatusTooManyRequests:
			attempts++
			if attempts > retryLimit {
				log.Printf("slack-notifier: giving up on %s to %s destination after %d retries: %v",
					label, item.kind, retryLimit, err)
				return zero, err
			}
			wait = maxDuration(transportErr.RetryAfter, time.Second)
		case transientStatus(transportErr.StatusCode):
			attempts++
			if attempts > retryLimit {
				log.Printf("slack-notifier: giving up on %s to %s destination after %d retries: %v",
					label, item.kind, retryLimit, err)
				return zero, err
			}
			wait = backoffDelay(notifier, attempts)
		default:
			log.Printf("slack-notifier: giving up on %s to %s destination: %v",
				label, item.kind, err)
			return zero, err
		}
		if !w.waitUntil(notifier.clock.Now().Add(wait)) {
			return zero, err
		}
	}
}

func transientStatus(status int) bool {
	return status == 0 || status == http.StatusRequestTimeout || status >= 500
}

func backoffDelay(notifier *Notifier, attempt int) time.Duration {
	nominal := backoffBase << (attempt - 1)
	fraction := 0.8
	if notifier.jitter != nil {
		fraction += 0.4 * notifier.jitter()
	}
	return time.Duration(float64(nominal) * fraction)
}

func latestTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earliestTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
