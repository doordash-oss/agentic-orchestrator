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
	"sync"
	"sync/atomic"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// itemKind is the overflow-policy attribute of a queued item. Only the
// category the item belongs to drives the eviction decision.
type itemKind int

const (
	kindProgress itemKind = iota
	kindNeedsInput
	kindProblems
	kindLifecycle
)

func (k itemKind) protected() bool { return k != kindProgress }

func (k itemKind) String() string {
	switch k {
	case kindNeedsInput:
		return "needs_input"
	case kindProblems:
		return "problems"
	case kindLifecycle:
		return "progress"
	default:
		return "progress"
	}
}

// queueItem is one lifecycle event awaiting dispatch.
type queueItem struct {
	kind        itemKind
	event       ports.Event
	recheckOnly bool
	reservation *queueReservation
}

// queueReservation keeps an accepted event inside the queue budget until all
// of its destination deliveries finish. Cancellation lets protected work
// evict Progress that has already reached a destination inbox.
type queueReservation struct {
	canceled atomic.Bool
	kind     itemKind
	event    ports.Event
}

// itemQueue is the bounded, non-blocking intake queue. Producers never
// wait: a full queue drops incoming Progress items, while a protected item
// (Needs input, Problems, lifecycle edges) evicts the oldest queued Progress item or is
// accepted over the bound when none remains, so protected categories are
// never dropped while the process is alive.
type itemQueue struct {
	mu       sync.Mutex
	items    []queueItem
	pending  []*queueReservation
	capacity int
	signal   chan struct{}
	dropped  func(item queueItem)
}

func newItemQueue(capacity int, dropped func(queueItem)) *itemQueue {
	if capacity <= 0 {
		capacity = defaultQueueCapacity
	}
	return &itemQueue{
		capacity: capacity,
		signal:   make(chan struct{}, 1),
		dropped:  dropped,
	}
}

// enqueue never blocks. It reports whether the item was accepted.
func (q *itemQueue) enqueue(item queueItem) bool {
	q.mu.Lock()
	if len(q.pending) < q.capacity {
		item.reservation = &queueReservation{kind: item.kind, event: item.event}
		q.pending = append(q.pending, item.reservation)
		q.appendLocked(item)
		q.mu.Unlock()
		return true
	}
	if item.kind.protected() {
		for i, pending := range q.pending {
			if !pending.kind.protected() {
				evicted := queueItem{
					kind:        pending.kind,
					event:       pending.event,
					reservation: pending,
				}
				pending.canceled.Store(true)
				q.removeItemLocked(pending)
				q.pending = append(q.pending[:i], q.pending[i+1:]...)
				item.reservation = &queueReservation{kind: item.kind, event: item.event}
				q.pending = append(q.pending, item.reservation)
				q.appendLocked(item)
				q.mu.Unlock()
				q.notifyDropped(evicted)
				return true
			}
		}
		// Only protected items remain: accept over the bound.
		item.reservation = &queueReservation{kind: item.kind, event: item.event}
		q.pending = append(q.pending, item.reservation)
		q.appendLocked(item)
		q.mu.Unlock()
		return true
	}
	q.mu.Unlock()
	q.notifyDropped(item)
	return false
}

// reserveProtected gives protected work discovered while dispatching an event
// its own queue-budget reservation. It applies the same eviction policy as a
// newly enqueued protected item without adding another dispatcher item.
func (q *itemQueue) reserveProtected(event ports.Event) *queueReservation {
	return q.reserveSplit(kindNeedsInput, event)
}

// reserveSplit keeps a recipient's split copy in the original overflow category.
// Only discovered Needs-input work earns a protected reservation.
func (q *itemQueue) reserveSplit(kind itemKind, event ports.Event) *queueReservation {
	q.mu.Lock()
	reservation := &queueReservation{kind: kind, event: event}
	if len(q.pending) < q.capacity {
		q.pending = append(q.pending, reservation)
		q.mu.Unlock()
		return reservation
	}
	if !kind.protected() {
		q.mu.Unlock()
		q.notifyDropped(queueItem{kind: kind, event: event})
		return nil
	}
	for i, pending := range q.pending {
		if pending.kind.protected() {
			continue
		}
		evicted := queueItem{
			kind:        pending.kind,
			event:       pending.event,
			reservation: pending,
		}
		pending.canceled.Store(true)
		q.removeItemLocked(pending)
		q.pending = append(q.pending[:i], q.pending[i+1:]...)
		q.pending = append(q.pending, reservation)
		q.mu.Unlock()
		q.notifyDropped(evicted)
		return reservation
	}
	q.pending = append(q.pending, reservation)
	q.mu.Unlock()
	return reservation
}

func (q *itemQueue) removeItemLocked(reservation *queueReservation) {
	for i, item := range q.items {
		if item.reservation == reservation {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return
		}
	}
}

func (q *itemQueue) appendLocked(item queueItem) {
	q.items = append(q.items, item)
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *itemQueue) notifyDropped(item queueItem) {
	if q.dropped != nil {
		q.dropped(item)
	}
}

// next takes the oldest queued item in arrival order.
func (q *itemQueue) next() (queueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return queueItem{}, false
	}
	item := q.items[0]
	q.items = q.items[1:]
	return item, true
}

func (q *itemQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

func (q *itemQueue) complete(item queueItem) {
	if item.reservation == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, reservation := range q.pending {
		if reservation == item.reservation {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			return
		}
	}
}
