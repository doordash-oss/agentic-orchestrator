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

// Package workadmission owns the runtime work-admission boundary shared by
// HTTP entry points, orchestration, session launching, and repository work.
// Reservation acquisition and admission closure synchronize on one mutex, so
// a deterministic invariant holds: either work owns a reservation, or
// installation owns closed admission — never both. Reservations must be
// acquired before the underlying work launches and released only after that
// work settles, so no window exists where running work is invisible both to
// the reservation count and to the activity detectors.
package workadmission

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Category labels one class of admitted work. Categories exist for truthful
// diagnostics; the idle decision only needs the total reservation count.
type Category string

const (
	// CategoryFeature covers feature phases, setup, child workflows, and
	// their finalization and continuation tails.
	CategoryFeature Category = "feature"
	// CategoryChat covers the singleton chat session from launch through
	// registration.
	CategoryChat Category = "chat"
	// CategoryClone covers repository clone/create operations from queue
	// admission until the record and its cleanup settle.
	CategoryClone Category = "clone"
	// CategoryUpload covers in-flight upload staging requests and upload
	// consumption.
	CategoryUpload Category = "upload"
	// CategoryOrigin covers deduplicated origin-status attempts.
	CategoryOrigin Category = "origin"
	// CategoryRepository covers other repository mutation/setup work:
	// source updates, initialization, reconciliation, provider and Git
	// probes launched by read-shaped requests, and detached cache
	// refreshes.
	CategoryRepository Category = "repository"
)

// ClosedError reports that admission is closed for new work of a category.
// The refusal is transport-agnostic: HTTP callers render it as 503
// update_in_progress with Retry-After; internal callers treat it as a
// work-start failure.
type ClosedError struct {
	Category Category
}

func (e *ClosedError) Error() string {
	return fmt.Sprintf("admission closed for %s work: an update is in progress", e.Category)
}

// AsClosed extracts a *ClosedError, if present.
func AsClosed(err error) (*ClosedError, bool) {
	var closed *ClosedError
	if errors.As(err, &closed) {
		return closed, true
	}
	return nil, false
}

// Activity is the observed work state discovered by detectors. It is an
// advisory read of live work; reservations are the authoritative admission
// state. Detection failure must never be reported as zero activity — the
// caller treats a detection error as blocking.
type Activity struct {
	Features       int
	ChatActive     bool
	Clones         int
	Uploads        int
	OriginChecks   int
	RepositoryWork int
}

// Busy reports whether any observed activity exists.
func (a Activity) Busy() bool {
	return a.Features > 0 || a.ChatActive || a.Clones > 0 ||
		a.Uploads > 0 || a.OriginChecks > 0 || a.RepositoryWork > 0
}

// Detector discovers observed activity. Detectors run outside the
// coordinator mutex and must be safe for concurrent use. A detector error
// means detection failed: the counts are incomplete and installation must
// be blocked.
type Detector func(ctx context.Context) (Activity, error)

// Options configures a Coordinator.
type Options struct {
	// Detectors discover observed activity for the idle decision and the
	// published snapshot.
	Detectors []Detector
	// FallbackPoll bounds how long an idle waiter can go without
	// reevaluating when activity-change events are missed. Zero uses
	// DefaultFallbackPoll.
	FallbackPoll time.Duration
	// Now is the injectable clock; nil uses time.Now.
	Now func() time.Time
	// After returns a channel that fires after the given delay; nil uses
	// time.After. Fake clocks fire it deterministically on Advance.
	After func(d time.Duration) <-chan time.Time
}

// DefaultFallbackPoll bounds missed activity events between reevaluations.
const DefaultFallbackPoll = 2 * time.Second

// Coordinator is the runtime work-admission boundary. Acquisition, release,
// and closure share one mutex. Activity discovery happens outside the
// mutex; the atomic idle decision is CloseIfQuiesced, which re-checks the
// reservation count under the same mutex acquisitions synchronize on.
type Coordinator struct {
	poll  time.Duration
	now   func() time.Time
	after func(time.Duration) <-chan time.Time

	// detectors holds the registered activity detectors; atomic so
	// registration races nothing and detection never needs the state
	// mutex.
	detectors atomic.Pointer[[]Detector]

	mu     sync.Mutex
	closed bool
	held   map[Category]int
	total  int
	// change wakes idle waiters. Cap-one with non-blocking sends: a wake
	// that arrives while a waiter evaluates is satisfied by that
	// evaluation.
	change chan struct{}
}

// New creates a Coordinator.
func New(opts Options) *Coordinator {
	poll := opts.FallbackPoll
	if poll <= 0 {
		poll = DefaultFallbackPoll
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	after := opts.After
	if after == nil {
		after = time.After
	}
	detectors := opts.Detectors
	c := &Coordinator{
		poll:   poll,
		now:    now,
		after:  after,
		held:   make(map[Category]int),
		change: make(chan struct{}, 1),
	}
	c.detectors.Store(&detectors)
	return c
}

// SetDetectors installs the activity detectors on an existing coordinator.
// The same coordinator instance is shared across the runtime — HTTP entry
// points, orchestration, sessions, and repository work hold reservations
// against it — so detectors are registered by the layer that owns them once
// the handler is constructed.
func (c *Coordinator) SetDetectors(detectors []Detector) {
	c.detectors.Store(&detectors)
	c.NotifyChanged()
}

// Acquire reserves one unit of admitted work. It fails with *ClosedError
// when admission is closed; otherwise the reservation must be released
// exactly once after the owned work settles.
func (c *Coordinator) Acquire(cat Category) (*Reservation, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, &ClosedError{Category: cat}
	}
	c.held[cat]++
	c.total++
	c.mu.Unlock()
	return &Reservation{coordinator: c, category: cat}, nil
}

// Held returns the total number of held reservations and the per-category
// counts. The map is a copy.
func (c *Coordinator) Held() (int, map[Category]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	per := make(map[Category]int, len(c.held))
	for cat, n := range c.held {
		per[cat] = n
	}
	return c.total, per
}

// Closed reports whether admission is closed for new work.
func (c *Coordinator) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// CloseIfQuiesced is the atomic idle decision: it closes admission only when
// no reservations are held, under the same mutex Acquire synchronizes on. A
// reservation acquired before this call keeps it open (returns false, and
// the caller keeps waiting); work arriving after a successful close is
// refused. The single closer is the install operation.
func (c *Coordinator) CloseIfQuiesced() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.total > 0 {
		return false
	}
	c.closed = true
	return true
}

// Open reopens admission after an aborted or cancelled install. Work that
// was refused meanwhile has already failed; nothing is replayed.
func (c *Coordinator) Open() {
	c.mu.Lock()
	c.closed = false
	c.mu.Unlock()
	c.NotifyChanged()
}

// Detect runs every detector outside the coordinator mutex and merges the
// results. Any detector error fails detection: callers must treat the
// activity as unknown and block installation.
func (c *Coordinator) Detect(ctx context.Context) (Activity, error) {
	var merged Activity
	if dp := c.detectors.Load(); dp != nil {
		for _, detect := range *dp {
			if detect == nil {
				continue
			}
			activity, err := detect(ctx)
			if err != nil {
				return Activity{}, fmt.Errorf("activity detection failed: %w", err)
			}
			merged.Features += activity.Features
			merged.ChatActive = merged.ChatActive || activity.ChatActive
			merged.Clones += activity.Clones
			merged.Uploads += activity.Uploads
			merged.OriginChecks += activity.OriginChecks
			merged.RepositoryWork += activity.RepositoryWork
		}
	}
	return merged, nil
}

// IdleSnapshot combines one detection with the current reservation count.
// It is an advisory observation; the authoritative gate is CloseIfQuiesced.
func (c *Coordinator) IdleSnapshot(ctx context.Context) (Activity, int, error) {
	activity, err := c.Detect(ctx)
	if err != nil {
		return Activity{}, 0, err
	}
	total, _ := c.Held()
	return activity, total, nil
}

// WaitForIdle blocks until one observation shows no detected activity and no
// held reservations, or the context ends. Activity-change signals and a
// bounded fallback poll both trigger reevaluation, so a missed event only
// delays the decision by one poll interval. Detection errors return
// immediately: uncertainty never counts as idle.
func (c *Coordinator) WaitForIdle(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		activity, err := c.Detect(ctx)
		if err != nil {
			return err
		}
		c.mu.Lock()
		total := c.total
		c.mu.Unlock()
		if !activity.Busy() && total == 0 {
			return nil
		}
		timer := c.after(c.poll)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.change:
		case <-timer:
		}
	}
}

// NotifyChanged signals that activity may have changed, waking an idle
// waiter. Non-blocking: a signal arriving during an evaluation is satisfied
// by that evaluation.
func (c *Coordinator) NotifyChanged() {
	select {
	case c.change <- struct{}{}:
	default:
	}
}

// Reservation is one unit of admitted work. Release is idempotent and
// exactly-once.
type Reservation struct {
	once        sync.Once
	coordinator *Coordinator
	category    Category
}

// Category reports the reservation's work category.
func (r *Reservation) Category() Category {
	if r == nil {
		return ""
	}
	return r.category
}

// Release settles one unit of admitted work. Safe to call more than once;
// only the first call counts.
func (r *Reservation) Release() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.coordinator.mu.Lock()
		if n := r.coordinator.held[r.category]; n <= 1 {
			delete(r.coordinator.held, r.category)
		} else {
			r.coordinator.held[r.category] = n - 1
		}
		if r.coordinator.total > 0 {
			r.coordinator.total--
		}
		r.coordinator.mu.Unlock()
		r.coordinator.NotifyChanged()
	})
}
