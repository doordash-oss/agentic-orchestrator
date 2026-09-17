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

package workadmission

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// blockingDetector gates one detection on a release barrier for
// deterministic races.
type blockingDetector struct {
	enter   chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func newBlockingDetector() *blockingDetector {
	return &blockingDetector{enter: make(chan struct{}), release: make(chan struct{})}
}

func (d *blockingDetector) Detect(context.Context) (Activity, error) {
	d.calls.Add(1)
	d.enter <- struct{}{}
	<-d.release
	return Activity{}, nil
}

func TestAcquireReleaseCounts(t *testing.T) {
	t.Parallel()
	c := New(Options{})
	res, err := c.Acquire(CategoryFeature)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	total, per := c.Held()
	if total != 1 || per[CategoryFeature] != 1 {
		t.Fatalf("held after acquire: total=%d per=%v", total, per)
	}
	res.Release()
	res.Release() // idempotent
	total, per = c.Held()
	if total != 0 || len(per) != 0 {
		t.Fatalf("held after release: total=%d per=%v", total, per)
	}
}

func TestClosedRefusesAcquire(t *testing.T) {
	t.Parallel()
	c := New(Options{})
	if !c.CloseIfQuiesced() {
		t.Fatal("close on quiesced coordinator must succeed")
	}
	if c.Closed() != true {
		t.Fatal("closed flag not set")
	}
	_, err := c.Acquire(CategoryChat)
	closed, ok := AsClosed(err)
	if !ok {
		t.Fatalf("expected ClosedError, got %v", err)
	}
	if closed.Category != CategoryChat {
		t.Fatalf("category = %q", closed.Category)
	}
	// A second close attempt is a no-op returning false.
	if c.CloseIfQuiesced() {
		t.Fatal("double close must return false")
	}
	c.Open()
	if _, err := c.Acquire(CategoryChat); err != nil {
		t.Fatalf("acquire after open: %v", err)
	}
}

// TestClosureRaceNeverBoth is the deterministic invariant: with one
// acquirer and one closer racing from a barrier, every iteration ends with
// either the acquirer owning a held reservation (close observed it and
// returned false) or the closer owning closed admission (acquire refused) —
// never both. The acquirer holds its reservation until the closer's
// decision is observed, so the invariant applies at the closure instant.
func TestClosureRaceNeverBoth(t *testing.T) {
	t.Parallel()
	const iterations = 300
	c := New(Options{})
	var violations atomic.Int64
	for i := 0; i < iterations; i++ {
		c.Open()
		start := make(chan struct{})
		closerDone := make(chan bool, 1)
		acquirerDone := make(chan error, 1)
		var held *Reservation
		go func() {
			<-start
			closerDone <- c.CloseIfQuiesced()
		}()
		go func() {
			<-start
			res, err := c.Acquire(CategoryFeature)
			held = res
			acquirerDone <- err
		}()
		close(start)
		closedIt := <-closerDone
		acqErr := <-acquirerDone
		acquiredIt := acqErr == nil
		if closedIt && acquiredIt {
			violations.Add(1)
		}
		if !closedIt && !acquiredIt {
			// The closer never closed, yet acquire failed: acquire can only
			// fail behind a closed flag, which only the closer sets.
			violations.Add(1)
			t.Errorf("iteration %d: acquire failed (%v) without closure", i, acqErr)
		}
		if acquiredIt {
			if held == nil {
				t.Fatalf("iteration %d: acquired without a reservation", i)
			}
			// The reservation was visibly held across the closer's
			// decision; settle it now.
			held.Release()
		}
	}
	if v := violations.Load(); v != 0 {
		t.Fatalf("invariant violated %d times: closure and a held reservation coexisted", v)
	}
}

// TestCloseWaitsForHeldReservations proves closure refuses while work holds
// reservations and succeeds only after the work settles.
func TestCloseWaitsForHeldReservations(t *testing.T) {
	t.Parallel()
	c := New(Options{})
	res, err := c.Acquire(CategoryClone)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if c.CloseIfQuiesced() {
		t.Fatal("close must refuse while a reservation is held")
	}
	// New work is still admitted while admission is open, matching the
	// idle-wait semantics: installation waits for it too.
	extra, err := c.Acquire(CategoryOrigin)
	if err != nil {
		t.Fatalf("acquire while open: %v", err)
	}
	res.Release()
	if c.CloseIfQuiesced() {
		t.Fatal("close must refuse while the second reservation is held")
	}
	extra.Release()
	if !c.CloseIfQuiesced() {
		t.Fatal("close must succeed once reservations settle")
	}
}

// TestDetectMergesAndFailsClosed verifies detector merging and that any
// detector error fails detection instead of reporting zero activity.
func TestDetectMergesAndFailsClosed(t *testing.T) {
	t.Parallel()
	c := New(Options{Detectors: []Detector{
		func(context.Context) (Activity, error) { return Activity{Features: 2}, nil },
		func(context.Context) (Activity, error) { return Activity{ChatActive: true, Clones: 1}, nil },
	}})
	activity, err := c.Detect(context.Background())
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if activity.Features != 2 || !activity.ChatActive || activity.Clones != 1 || !activity.Busy() {
		t.Fatalf("merged activity = %+v", activity)
	}

	failing := New(Options{Detectors: []Detector{
		func(context.Context) (Activity, error) { return Activity{}, nil },
		func(context.Context) (Activity, error) { return Activity{}, errors.New("store unreadable") },
	}})
	if _, err := failing.Detect(context.Background()); err == nil {
		t.Fatal("detector error must fail detection")
	}
	if err := failing.WaitForIdle(context.Background()); err == nil {
		t.Fatal("failed detection must never count as idle")
	}
}

// TestWaitForIdleReleaseWakes proves a reservation release wakes the waiter
// without waiting for the fallback poll.
func TestWaitForIdleReleaseWakes(t *testing.T) {
	c := New(Options{
		Detectors:    []Detector{func(context.Context) (Activity, error) { return Activity{}, nil }},
		FallbackPoll: time.Hour,
	})
	res, err := c.Acquire(CategoryFeature)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- c.WaitForIdle(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("waiter returned early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	res.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait for idle: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("release did not wake the idle waiter")
	}
}

// TestWaitForIdleFallbackPollCoversMissedEvents proves the bounded fallback
// poll reevaluates even when no activity-change signal ever arrives: the
// observed activity clears without any notification and the waiter still
// concludes idle within the poll bound.
func TestWaitForIdleFallbackPollCoversMissedEvents(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	c := New(Options{
		Detectors: []Detector{func(context.Context) (Activity, error) {
			if busy.Load() {
				return Activity{Features: 1}, nil
			}
			return Activity{}, nil
		}},
		FallbackPoll: 20 * time.Millisecond,
	})
	done := make(chan error, 1)
	go func() { done <- c.WaitForIdle(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("waiter returned while busy: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	busy.Store(false) // no NotifyChanged: the event is "missed"
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait for idle: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fallback poll did not reevaluate the idle decision")
	}
}

func TestWaitForIdleContextCancel(t *testing.T) {
	c := New(Options{FallbackPoll: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.WaitForIdle(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

// TestActivityProtectedBusy pins the protected-work predicate beside Busy:
// repository activity of every class is protected, while feature and chat
// activity alone — the work an explicit-stop install may interrupt — never
// is.
func TestActivityProtectedBusy(t *testing.T) {
	t.Parallel()
	for _, activity := range []Activity{
		{Features: 3},
		{ChatActive: true},
		{Features: 1, ChatActive: true},
		{},
	} {
		if activity.ProtectedBusy() {
			t.Fatalf("activity %+v must not be protected", activity)
		}
	}
	for _, activity := range []Activity{
		{Clones: 1},
		{Uploads: 1},
		{OriginChecks: 1},
		{RepositoryWork: 1},
		{Features: 2, RepositoryWork: 1},
	} {
		if !activity.ProtectedBusy() {
			t.Fatalf("activity %+v must be protected", activity)
		}
		if !activity.Busy() {
			t.Fatalf("protected activity %+v must also be busy", activity)
		}
	}
}

// TestDetectRunsOutsideMutex proves slow discovery does not block
// acquisition: a detector blocked mid-discovery cannot stall Acquire.
func TestDetectRunsOutsideMutex(t *testing.T) {
	detector := newBlockingDetector()
	c := New(Options{Detectors: []Detector{detector.Detect}})
	done := make(chan error, 1)
	go func() {
		_, err := c.Detect(context.Background())
		done <- err
	}()
	<-detector.enter
	// While detection is blocked, Acquire must complete.
	acquired := make(chan error, 1)
	go func() {
		_, err := c.Acquire(CategoryRepository)
		acquired <- err
	}()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("acquire during detection: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquire blocked behind a slow detector")
	}
	close(detector.release)
	if err := <-done; err != nil {
		t.Fatalf("detect: %v", err)
	}
}

// TestCloseForStoppingRefusesProtectedReservations proves the stopping
// closure is refused — with admission left open — when any reservation
// outside the stoppable categories is held, while feature and chat
// reservations alone permit the closure and persist under it.
func TestCloseForStoppingRefusesProtectedReservations(t *testing.T) {
	for _, cat := range []Category{CategoryClone, CategoryUpload, CategoryOrigin, CategoryRepository, Category("unknown")} {
		c := New(Options{})
		res, err := c.Acquire(cat)
		if err != nil {
			t.Fatalf("acquire %s: %v", cat, err)
		}
		if c.CloseForStopping(CategoryFeature, CategoryChat) {
			t.Fatalf("closure with held %s reservation must be refused", cat)
		}
		if c.Closed() {
			t.Fatalf("admission must stay open after a refused stopping closure (%s)", cat)
		}
		// A refused closure leaves ordinary admission working.
		if _, err := c.Acquire(CategoryFeature); err != nil {
			t.Fatalf("acquire after refused closure: %v", err)
		}
		res.Release()
	}
}

// TestCloseForStoppingAdmitsStoppableReservations proves feature and chat
// reservations survive the stopping closure, settle through Release while
// closed, and every new reservation — stoppable or not — is refused once
// the boundary is closed.
func TestCloseForStoppingAdmitsStoppableReservations(t *testing.T) {
	c := New(Options{})
	featureRes, err := c.Acquire(CategoryFeature)
	if err != nil {
		t.Fatalf("acquire feature: %v", err)
	}
	chatRes, err := c.Acquire(CategoryChat)
	if err != nil {
		t.Fatalf("acquire chat: %v", err)
	}
	if !c.CloseForStopping(CategoryFeature, CategoryChat) {
		t.Fatal("closure with only stoppable reservations must succeed")
	}
	if !c.Closed() {
		t.Fatal("admission must be closed after a successful stopping closure")
	}
	for _, cat := range []Category{CategoryFeature, CategoryChat, CategoryClone, CategoryRepository, Category("unknown")} {
		if _, err := c.Acquire(cat); err == nil {
			t.Fatalf("acquire %s under closed admission must fail", cat)
		}
	}
	// Settling stoppable work releases its reservations while closed.
	featureRes.Release()
	chatRes.Release()
	total, _ := c.Held()
	if total != 0 {
		t.Fatalf("held after settle = %d, want 0", total)
	}
	// The boundary stays closed: closed admission alone never proves
	// inactivity, and reopening belongs to the install operation.
	if !c.Closed() {
		t.Fatal("boundary must stay closed until explicitly reopened")
	}
	c.Open()
	if c.Closed() {
		t.Fatal("Open must reopen after the stopping interval")
	}
}

// TestCloseForStoppingSynchronizesWithAcquisition proves the stopping
// closure and reservation acquisition are atomic with respect to each
// other: a protected acquire racing the closure either wins (closure
// refused) or is refused by the closed boundary — never both admitted and
// closed, and never silently dropped.
func TestCloseForStoppingSynchronizesWithAcquisition(t *testing.T) {
	for i := 0; i < 64; i++ {
		c := New(Options{})
		start := make(chan struct{})
		closureObserved := make(chan struct{}, 1)
		closureDone := make(chan bool, 1)
		acquireDone := make(chan error, 1)
		go func() {
			<-start
			closureDone <- c.CloseForStopping(CategoryFeature, CategoryChat)
		}()
		go func() {
			<-start
			res, err := c.Acquire(CategoryClone)
			if err == nil {
				// Hold the reservation until both outcomes are observed so
				// the closure cannot race past a released reservation.
				defer func() {
					<-closureObserved
					res.Release()
				}()
			}
			acquireDone <- err
		}()
		close(start)
		closed := <-closureDone
		closureObserved <- struct{}{}
		acquireErr := <-acquireDone
		if closed && acquireErr == nil {
			t.Fatal("closure cannot succeed while a protected reservation was acquired")
		}
		if !closed && acquireErr != nil {
			// The closure refused because the reservation was held; the
			// acquisition itself must have succeeded in that case.
			t.Fatalf("acquire failed (%v) while closure was refused", acquireErr)
		}
	}
}
