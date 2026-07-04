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

// Package eventbus multiplexes the orchestrator's single ports.Event
// stream to multiple in-process subscribers (TUI + 0..N WebSocket
// clients) with per-subscriber buffering and drop-oldest backpressure.
//
// Design constraints (see docs/WEB_DASHBOARD_M1_DESIGN.md § 3):
//   - Publish never blocks. A slow WS client must not stall the
//     orchestrator or the TUI.
//   - Drop-oldest favours freshness over completeness. Browser clients
//     reconcile via REST after seeing a drop notice.
//   - The TUI is given a larger buffer than WS clients so it almost
//     never sees drops in practice.
package eventbus

import (
	"sync"
	"sync/atomic"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// Default per-subscriber buffer sizes. Exposed so callers (e.g. the WS
// hub) can override per-subscription, but the orchestrator and TUI use
// the defaults.
const (
	DefaultBuffer = 256
	TUIBuffer     = 1024
)

// Bus fans out ports.Event values to N subscribers. Safe for concurrent
// Publish from any goroutine and concurrent Subscribe/Cancel from any
// goroutine. The publish path takes only an RLock so a fast publisher
// is not serialised against unrelated subscribe churn.
type Bus struct {
	mu     sync.RWMutex
	subs   map[uint64]*subscriber
	nextID atomic.Uint64

	sent  atomic.Uint64
	drops atomic.Uint64
}

// New constructs an empty Bus. Subscribers attach via Subscribe.
func New() *Bus {
	return &Bus{subs: make(map[uint64]*subscriber)}
}

// Subscription is a handle returned by Subscribe. C is the receive
// channel; Cancel removes the subscriber from the bus (idempotent).
// Name is recorded for /api/healthz reporting.
type Subscription struct {
	C      <-chan ports.Event
	Name   string
	Cancel func()
	stats  func() SubscriberStats
}

// Stats returns a snapshot of this subscription's counters.
func (s *Subscription) Stats() SubscriberStats { return s.stats() }

// SubscriberStats reports per-subscriber buffer occupancy and drop
// count. Used by /api/healthz to surface slow consumers.
type SubscriberStats struct {
	Name    string `json:"name"`
	Cap     int    `json:"cap"`
	Len     int    `json:"len"`
	Dropped uint64 `json:"dropped"`
}

// BusStats is the bus-wide counters snapshot.
type BusStats struct {
	Sent        uint64            `json:"sent"`
	Dropped     uint64            `json:"dropped"`
	Subscribers []SubscriberStats `json:"subscribers"`
}

// subscriber owns its own buffered channel. The channel capacity is
// fixed at subscription time so Publish can compare len(ch)==cap(ch)
// to decide whether to drop the oldest event.
type subscriber struct {
	id      uint64
	name    string
	ch      chan ports.Event
	dropped atomic.Uint64
	cap     int
}

// Subscribe attaches a new subscriber with a buffered channel of size
// bufferSize (clamped to DefaultBuffer when <=0). The returned
// Subscription's Cancel removes it and closes the channel; calling
// Cancel more than once is safe.
func (b *Bus) Subscribe(name string, bufferSize int) *Subscription {
	if bufferSize <= 0 {
		bufferSize = DefaultBuffer
	}
	id := b.nextID.Add(1)
	s := &subscriber{
		id:   id,
		name: name,
		ch:   make(chan ports.Event, bufferSize),
		cap:  bufferSize,
	}
	b.mu.Lock()
	b.subs[id] = s
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			close(s.ch)
		})
	}
	return &Subscription{
		C:      s.ch,
		Name:   name,
		Cancel: cancel,
		stats: func() SubscriberStats {
			return SubscriberStats{
				Name:    s.name,
				Cap:     s.cap,
				Len:     len(s.ch),
				Dropped: s.dropped.Load(),
			}
		},
	}
}

// Publish delivers ev to every current subscriber. Never blocks. If a
// subscriber's buffer is full, the oldest event in its buffer is
// dropped and the new event is enqueued — slow consumers see a gap in
// their feed but never starve faster ones.
func (b *Bus) Publish(ev ports.Event) {
	b.sent.Add(1)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subs {
		select {
		case s.ch <- ev:
			// delivered
		default:
			// buffer full — drop oldest then push
			select {
			case <-s.ch:
				s.dropped.Add(1)
				b.drops.Add(1)
			default:
				// channel raced to empty between our two selects;
				// no slot to free, treat the new event as dropped
				s.dropped.Add(1)
				b.drops.Add(1)
				continue
			}
			select {
			case s.ch <- ev:
				// delivered after eviction
			default:
				// extremely unlikely: another goroutine took the slot
				// between the drain and the re-push. Count the new
				// event as dropped rather than spin.
				s.dropped.Add(1)
				b.drops.Add(1)
			}
		}
	}
}

// Stats snapshots all bus-wide and per-subscriber counters.
func (b *Bus) Stats() BusStats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	subs := make([]SubscriberStats, 0, len(b.subs))
	for _, s := range b.subs {
		subs = append(subs, SubscriberStats{
			Name:    s.name,
			Cap:     s.cap,
			Len:     len(s.ch),
			Dropped: s.dropped.Load(),
		})
	}
	return BusStats{
		Sent:        b.sent.Load(),
		Dropped:     b.drops.Load(),
		Subscribers: subs,
	}
}

// SubscriberCount returns the live subscriber count. O(1).
func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
