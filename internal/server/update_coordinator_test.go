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
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
)

// fakeUpdateFeed is the deterministic metadata-feed seam: it counts calls,
// can block until released, and returns a canned selection or error.
type fakeUpdateFeed struct {
	mu        sync.Mutex
	callsN    int
	block     chan struct{}
	selection selfupdate.ReleaseSelection
	err       error
}

func (f *fakeUpdateFeed) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callsN
}

func (f *fakeUpdateFeed) LatestStable(ctx context.Context) (selfupdate.ReleaseSelection, error) {
	f.mu.Lock()
	f.callsN++
	block := f.block
	selection, err := f.selection, f.err
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return selfupdate.ReleaseSelection{}, ctx.Err()
		}
	}
	return selection, err
}

func waitForFeedCalls(t *testing.T, feed *fakeUpdateFeed, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if feed.calls() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("feed calls = %d, want %d", feed.calls(), want)
}

// fakeUpdateClock is the deterministic clock: Advance fires After channels
// whose deadlines have passed.
type fakeUpdateClock struct {
	mu     sync.Mutex
	now    time.Time
	wakers []fakeWaker
}

type fakeWaker struct {
	at time.Time
	ch chan time.Time
}

func newFakeUpdateClock() *fakeUpdateClock {
	return &fakeUpdateClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeUpdateClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeUpdateClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.wakers = append(c.wakers, fakeWaker{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeUpdateClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []fakeWaker
	remaining := c.wakers[:0]
	for _, w := range c.wakers {
		if !w.at.After(c.now) {
			due = append(due, w)
			continue
		}
		remaining = append(remaining, w)
	}
	c.wakers = remaining
	c.mu.Unlock()
	for _, w := range due {
		select {
		case w.ch <- c.now:
		default:
		}
	}
}

// transitionRecorder captures emitTransition outputs.
type transitionRecorder struct {
	mu       sync.Mutex
	logs     []string
	events   []string
	observed []observedUpdate
}

type observedUpdate struct{ from, to, result string }

func (r *transitionRecorder) log(line string) {
	r.mu.Lock()
	r.logs = append(r.logs, line)
	r.mu.Unlock()
}

func (r *transitionRecorder) ServerUpdate(from, to, result string) {
	r.mu.Lock()
	r.observed = append(r.observed, observedUpdate{from, to, result})
	r.mu.Unlock()
}

func (r *transitionRecorder) event() {
	r.mu.Lock()
	r.events = append(r.events, "update.updated")
	r.mu.Unlock()
}

func (r *transitionRecorder) eventCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *transitionRecorder) snapshot() ([]string, []string, []observedUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	logs := append([]string(nil), r.logs...)
	events := append([]string(nil), r.events...)
	observed := append([]observedUpdate(nil), r.observed...)
	return logs, events, observed
}

// newTestCoordinator builds a coordinator on the fake clock with the given
// feed, ready to start.
func newTestCoordinator(t *testing.T, opts UpdateOptions) (*updateCoordinator, *fakeUpdateClock, *fakeUpdateFeed, *transitionRecorder) {
	t.Helper()
	clock := newFakeUpdateClock()
	recorder := &transitionRecorder{}
	opts.Now = clock.Now
	opts.Rand = func(int64) int64 { return 0 }
	opts.Log = recorder.log
	opts.Observer = recorder
	feed := &fakeUpdateFeed{selection: selfupdate.ReleaseSelection{Version: "2.0.0", TagName: "v2.0.0", ReleaseURL: "https://github.com/doordash-oss/agentic-orchestrator/releases/tag/v2.0.0"}}
	opts.Feed = feed
	if opts.Settings.CheckInterval <= 0 {
		opts.Settings.CheckInterval = time.Hour
	}
	if opts.CurrentVersion == "" {
		opts.CurrentVersion = "1.0.0"
	}
	if opts.Eligibility.Install == "" {
		opts.Eligibility = selfupdate.Eligibility{Supported: true, Install: selfupdate.InstallTarball}
	}
	if opts.Policy == "" {
		opts.Policy = selfupdate.PolicyNotify
	}
	if opts.Settings.Strategy == "" {
		opts.Settings.Strategy = "quiesce"
	}
	coordinator := newUpdateCoordinator(opts)
	coordinator.clock = clock
	coordinator.publish = recorder.event
	return coordinator, clock, feed, recorder
}

func waitStatus(t *testing.T, c *updateCoordinator, status string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.Snapshot().Status == UpdateSnapshotStatus(status) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("status = %q, want %q", c.Snapshot().Status, status)
}

func TestCoordinatorInitialCheckRunsAfterStartWithoutBlockingIt(t *testing.T) {
	t.Parallel()
	coordinator, _, feed, _ := newTestCoordinator(t, UpdateOptions{})
	feed.mu.Lock()
	feed.block = make(chan struct{})
	feed.mu.Unlock()

	// start returns before the (blocked) initial check finishes; the
	// snapshot shows checking while the worker is busy.
	coordinator.start(context.Background())
	waitStatus(t, coordinator, updateStatusChecking)
	close(feed.block)
	waitStatus(t, coordinator, updateStatusAvailable)
	snapshot := coordinator.Snapshot()
	if snapshot.LatestVersion == nil || *snapshot.LatestVersion != "2.0.0" {
		t.Fatalf("latest_version = %v", snapshot.LatestVersion)
	}
	if snapshot.LastSuccessAt == nil || snapshot.LastCheckAt == nil {
		t.Fatal("last_success_at/last_check_at missing")
	}
	if snapshot.NextCheckAt == nil {
		t.Fatal("next_check_at missing after a successful check")
	}
	if snapshot.LatestReleaseURL == nil || !strings.Contains(*snapshot.LatestReleaseURL, "github.com") {
		t.Fatalf("latest_release_url = %v", snapshot.LatestReleaseURL)
	}
	coordinator.shutdown(context.Background())
}

func TestCoordinatorPeriodicChecksFollowInterval(t *testing.T) {
	t.Parallel()
	coordinator, clock, feed, _ := newTestCoordinator(t, UpdateOptions{})
	coordinator.start(context.Background())
	waitForFeedCalls(t, feed, 1)
	waitStatus(t, coordinator, updateStatusAvailable)

	// Advancing less than the interval does nothing; reaching it triggers
	// the next periodic check.
	clock.Advance(30 * time.Minute)
	if got := feed.calls(); got != 1 {
		t.Fatalf("feed calls after partial advance = %d, want 1", got)
	}
	clock.Advance(45 * time.Minute) // past the 1h interval (jitter drew 0)
	waitForFeedCalls(t, feed, 2)
	waitStatus(t, coordinator, updateStatusAvailable)
	coordinator.shutdown(context.Background())
}

func TestCoordinatorShortIntervalsKeepPositiveDelays(t *testing.T) {
	t.Parallel()
	coordinator, clock, feed, recorder := newTestCoordinator(t, UpdateOptions{})
	coordinator.opts.Settings.CheckInterval = 2 * time.Second
	// Max jitter draw: the band is [interval - 0.5s, interval + 0.5s].
	coordinator.rand = func(n int64) int64 { return n - 1 }
	coordinator.start(context.Background())
	waitForFeedCalls(t, feed, 1)
	waitStatus(t, coordinator, updateStatusAvailable)
	snapshot := coordinator.Snapshot()
	if snapshot.NextCheckAt == nil {
		t.Fatal("next_check_at missing")
	}
	delay := snapshot.NextCheckAt.Sub(clock.Now())
	if delay <= 0 {
		t.Fatalf("realized delay = %v, must stay positive", delay)
	}
	if delay > 3*time.Second {
		t.Fatalf("realized delay = %v, outside the jitter band", delay)
	}
	_ = recorder
	coordinator.shutdown(context.Background())
}

func TestCoordinatorExplicitCheckCoalescesIntoOneWorker(t *testing.T) {
	t.Parallel()
	coordinator, _, feed, _ := newTestCoordinator(t, UpdateOptions{})
	feed.mu.Lock()
	feed.block = make(chan struct{})
	feed.mu.Unlock()
	coordinator.start(context.Background())
	waitStatus(t, coordinator, updateStatusChecking)

	// Several explicit requests while the initial check is running coalesce
	// into the same worker: no queued backlog runs afterwards.
	for range 5 {
		coordinator.requestCheck()
	}
	close(feed.block)
	waitStatus(t, coordinator, updateStatusAvailable)
	// Give any wrongly-queued extra check a moment to surface.
	time.Sleep(50 * time.Millisecond)
	if got := feed.calls(); got != 1 {
		t.Fatalf("feed calls = %d, want 1 (coalesced)", got)
	}
	coordinator.shutdown(context.Background())
}

func TestCoordinatorExplicitCheckBypassesPeriodicWait(t *testing.T) {
	t.Parallel()
	coordinator, _, feed, _ := newTestCoordinator(t, UpdateOptions{})
	coordinator.start(context.Background())
	waitForFeedCalls(t, feed, 1)
	waitStatus(t, coordinator, updateStatusAvailable)

	// Long interval ahead, but an explicit request runs immediately.
	coordinator.requestCheck()
	waitForFeedCalls(t, feed, 2)
	coordinator.shutdown(context.Background())
}

func TestCoordinatorFailedRefreshRetainsLastSuccessfulMetadata(t *testing.T) {
	t.Parallel()
	coordinator, _, feed, _ := newTestCoordinator(t, UpdateOptions{})
	coordinator.start(context.Background())
	waitForFeedCalls(t, feed, 1)
	waitStatus(t, coordinator, updateStatusAvailable)
	successSnapshot := coordinator.Snapshot()

	feed.mu.Lock()
	feed.err = fmt.Errorf("connection reset by peer")
	feed.mu.Unlock()
	coordinator.requestCheck()
	waitStatus(t, coordinator, updateStatusFailed)

	snapshot := coordinator.Snapshot()
	if snapshot.LatestVersion == nil || *snapshot.LatestVersion != *successSnapshot.LatestVersion {
		t.Fatalf("latest_version = %v, want retained %v", snapshot.LatestVersion, successSnapshot.LatestVersion)
	}
	if snapshot.LastSuccessAt == nil || !snapshot.LastSuccessAt.Equal(*successSnapshot.LastSuccessAt) {
		t.Fatalf("last_success_at = %v, want retained %v", snapshot.LastSuccessAt, successSnapshot.LastSuccessAt)
	}
	if snapshot.Error == nil || snapshot.Error.Code != "update_check_failed" {
		t.Fatalf("error = %+v, want update_check_failed", snapshot.Error)
	}
	if snapshot.NextCheckAt == nil {
		t.Fatal("failed refresh must publish the next retry")
	}
	// A failed refresh with no prior success keeps metadata unknown.
	freshCoordinator, _, freshFeed, _ := newTestCoordinator(t, UpdateOptions{})
	freshFeed.err = fmt.Errorf("boom")
	freshCoordinator.start(context.Background())
	waitStatus(t, freshCoordinator, updateStatusFailed)
	fresh := freshCoordinator.Snapshot()
	if fresh.LatestVersion != nil || fresh.LastSuccessAt != nil {
		t.Fatalf("no-prior-success failure must keep metadata unknown: %+v", fresh)
	}
	coordinator.shutdown(context.Background())
	freshCoordinator.shutdown(context.Background())
}

func TestCoordinatorRetryBackoffBoundedAndResetBySuccess(t *testing.T) {
	t.Parallel()
	coordinator, clock, feed, _ := newTestCoordinator(t, UpdateOptions{})
	feed.mu.Lock()
	feed.err = fmt.Errorf("transient")
	feed.mu.Unlock()
	coordinator.start(context.Background())
	waitStatus(t, coordinator, updateStatusFailed)

	firstRetry := coordinator.Snapshot().NextCheckAt.Sub(clock.Now())
	if firstRetry != updateBackoffBase {
		t.Fatalf("first backoff = %v, want %v", firstRetry, updateBackoffBase)
	}
	// The retry deadline is local (not server-imposed): explicit checks
	// bypass it.
	if refusal := coordinator.retryDeadlineRefusal(); refusal != nil {
		t.Fatalf("local backoff must not refuse manual checks: %v", refusal)
	}
	clock.Advance(updateBackoffBase)
	waitForFeedCalls(t, feed, 2)
	waitStatus(t, coordinator, updateStatusFailed)
	secondRetry := coordinator.Snapshot().NextCheckAt.Sub(clock.Now())
	if secondRetry != 2*updateBackoffBase {
		t.Fatalf("second backoff = %v, want %v", secondRetry, 2*updateBackoffBase)
	}
	if secondRetry > updateBackoffCap {
		t.Fatalf("backoff %v exceeded the cap", secondRetry)
	}

	// A success resets the cadence to the configured interval.
	feed.mu.Lock()
	feed.err = nil
	feed.mu.Unlock()
	clock.Advance(2 * updateBackoffBase)
	waitForFeedCalls(t, feed, 3)
	waitStatus(t, coordinator, updateStatusAvailable)
	next := coordinator.Snapshot().NextCheckAt.Sub(clock.Now())
	if next < time.Hour-updateBackoffCap || next > time.Hour+updateBackoffCap {
		t.Fatalf("post-success next check = %v, want the configured cadence", next)
	}
	coordinator.shutdown(context.Background())
}

func TestCoordinatorServerImposedDeadlineIsAFloor(t *testing.T) {
	t.Parallel()
	coordinator, clock, feed, _ := newTestCoordinator(t, UpdateOptions{})
	notBefore := clock.Now().Add(2 * time.Hour)
	feed.mu.Lock()
	feed.err = &selfupdate.FeedError{Reason: "rate limited", Retry: true, NotBefore: notBefore}
	feed.mu.Unlock()
	coordinator.start(context.Background())
	waitStatus(t, coordinator, updateStatusFailed)

	snapshot := coordinator.Snapshot()
	if snapshot.RetryNotBefore == nil || !snapshot.RetryNotBefore.Equal(notBefore) {
		t.Fatalf("retry_not_before = %v, want %v", snapshot.RetryNotBefore, notBefore)
	}
	// Local backoff (1 minute) cannot shorten the two-hour floor.
	if snapshot.NextCheckAt.Before(notBefore) {
		t.Fatalf("next_check_at %v shortened the server deadline %v", snapshot.NextCheckAt, notBefore)
	}
	// Manual checks inside the deadline are refused with a hint, without a
	// request.
	calls := feed.calls()
	if refusal := coordinator.retryDeadlineRefusal(); refusal == nil {
		t.Fatal("manual check inside the deadline must be refused")
	} else if refusal.Code != "update_check_failed" {
		t.Fatalf("refusal code = %q", refusal.Code)
	}
	coordinator.requestCheck()
	time.Sleep(50 * time.Millisecond)
	if feed.calls() != calls {
		t.Fatalf("feed calls = %d, want %d (no request inside the deadline)", feed.calls(), calls)
	}
	// Past the deadline, periodic cadence resumes.
	clock.Advance(2*time.Hour + time.Minute)
	waitForFeedCalls(t, feed, calls+1)
	coordinator.shutdown(context.Background())
}

func TestCoordinatorShutdownCancelsAndFences(t *testing.T) {
	t.Parallel()
	coordinator, _, feed, recorder := newTestCoordinator(t, UpdateOptions{})
	feed.mu.Lock()
	feed.block = make(chan struct{})
	feed.mu.Unlock()
	coordinator.start(context.Background())
	waitStatus(t, coordinator, updateStatusChecking)
	eventsBefore := recorder.eventCount()

	// Shutdown while the worker is blocked: it returns within the deadline,
	// the worker is cancelled, and the late completion is dropped.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		coordinator.shutdown(shutdownCtx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not drain the worker")
	}
	close(feed.block)
	time.Sleep(100 * time.Millisecond)
	if recorder.eventCount() != eventsBefore {
		t.Fatal("stale completion published a post-shutdown update")
	}
	snapshot := coordinator.Snapshot()
	if snapshot.Status != updateStatusChecking {
		t.Fatalf("post-shutdown status = %q, want the fenced checking state", snapshot.Status)
	}
	if snapshot.NextCheckAt != nil {
		t.Fatal("shutdown must clear the scheduled next check")
	}
	// A post-shutdown explicit request is a no-op.
	coordinator.requestCheck()
	time.Sleep(50 * time.Millisecond)
	if feed.calls() != 1 {
		t.Fatalf("feed calls = %d, want 1", feed.calls())
	}
}

func TestCoordinatorStartupReceipts(t *testing.T) {
	t.Parallel()
	rolledBack := selfupdate.Receipt{
		Outcome:     selfupdate.OutcomeRolledBack,
		FromVersion: "1.0.0",
		ToVersion:   "2.0.0",
		Error:       "target failed health wait",
		ResolvedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	coordinator, _, _, _ := newTestCoordinator(t, UpdateOptions{StartupReceipt: &rolledBack})
	snapshot := coordinator.Snapshot()
	if snapshot.Status != updateStatusFailed {
		t.Fatalf("status = %q, want failed for a validated rollback", snapshot.Status)
	}
	if snapshot.Error == nil || snapshot.Error.Code != "update_rolled_back" {
		t.Fatalf("error = %+v, want update_rolled_back", snapshot.Error)
	}
	if snapshot.Receipt == nil || snapshot.Receipt.Outcome != UpdatePublicReceiptOutcomeRolledBack {
		t.Fatalf("receipt = %+v", snapshot.Receipt)
	}
	if snapshot.Receipt.FromVersion != "1.0.0" || snapshot.Receipt.ToVersion != "2.0.0" {
		t.Fatalf("receipt versions = %q/%q", snapshot.Receipt.FromVersion, snapshot.Receipt.ToVersion)
	}

	confirmed := selfupdate.Receipt{Outcome: selfupdate.OutcomeConfirmed, FromVersion: "1.0.0", ToVersion: "2.0.0", ResolvedAt: time.Now()}
	coordinator2, _, _, _ := newTestCoordinator(t, UpdateOptions{StartupReceipt: &confirmed})
	snapshot = coordinator2.Snapshot()
	if snapshot.Status != updateStatusConfirmed {
		t.Fatalf("status = %q, want confirmed", snapshot.Status)
	}
	if snapshot.Receipt == nil || snapshot.Receipt.Outcome != UpdatePublicReceiptOutcomeConfirmed {
		t.Fatalf("receipt = %+v", snapshot.Receipt)
	}

	// An adopted pending handoff starts idle with the pending receipt, and
	// settles to confirmed through confirmReceipt.
	pending := selfupdate.Receipt{Outcome: selfupdate.OutcomePending, FromVersion: "1.0.0", ToVersion: "2.0.0"}
	coordinator3, _, _, recorder := newTestCoordinator(t, UpdateOptions{StartupReceipt: &pending})
	if snapshot := coordinator3.Snapshot(); snapshot.Status != updateStatusIdle {
		t.Fatalf("status = %q, want idle while unconfirmed", snapshot.Status)
	}
	confirmedCopy := pending
	confirmedCopy.Outcome = selfupdate.OutcomeConfirmed
	coordinator3.confirmReceipt(confirmedCopy)
	if snapshot := coordinator3.Snapshot(); snapshot.Status != updateStatusConfirmed {
		t.Fatalf("status = %q, want confirmed after handoff confirmation", snapshot.Status)
	}
	logs, _, observed := recorder.snapshot()
	if len(logs) == 0 || !strings.HasPrefix(logs[len(logs)-1], "update: ") {
		t.Fatalf("log lines = %v", logs)
	}
	if len(observed) == 0 {
		t.Fatal("handoff confirmation must observe server.update")
	}
	last := observed[len(observed)-1]
	if last.to != updateStatusConfirmed || last.result != "handoff_confirmed" {
		t.Fatalf("observation = %+v", last)
	}
}

func TestCoordinatorDurableSuppressionSurvivesChecks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binary := dir + "/agentico"
	if err := writeFileTest(binary, "#!/bin/sh\n", 0o755); err != nil {
		t.Fatal(err)
	}
	rolledBack := selfupdate.Receipt{
		TransactionID: strings.Repeat("a", 32),
		Outcome:       selfupdate.OutcomeRolledBack,
		FromVersion:   "1.0.0",
		ToVersion:     "2.0.0",
		Error:         "target failed",
	}
	if err := selfupdate.SuppressTarget(binary, selfupdate.SuppressedTarget{
		Version: "2.0.0", TransactionID: rolledBack.TransactionID, SuppressedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	opts := UpdateOptions{CurrentVersion: "1.0.0", ExecPath: binary}
	coordinator, _, feed, _ := newTestCoordinator(t, opts)
	feed.mu.Lock()
	feed.selection = selfupdate.ReleaseSelection{Version: "2.0.0", TagName: "v2.0.0"}
	feed.mu.Unlock()
	coordinator.start(context.Background())
	waitStatus(t, coordinator, updateStatusFailed)

	// The suppressed newest release stays visible with the durable rollback
	// outcome explaining it.
	snapshot := coordinator.Snapshot()
	if snapshot.LatestVersion == nil || *snapshot.LatestVersion != "2.0.0" {
		t.Fatalf("latest_version = %v, want the suppressed release to stay visible", snapshot.LatestVersion)
	}
	if snapshot.Error == nil || snapshot.Error.Code != "update_rolled_back" {
		t.Fatalf("error = %+v, want update_rolled_back", snapshot.Error)
	}

	// A newer unsuppressed release becomes available while the exact-version
	// suppression and its receipt stay intact.
	feed.mu.Lock()
	feed.selection = selfupdate.ReleaseSelection{Version: "3.0.0", TagName: "v3.0.0"}
	feed.mu.Unlock()
	coordinator.requestCheck()
	waitStatus(t, coordinator, updateStatusAvailable)
	snapshot = coordinator.Snapshot()
	if snapshot.LatestVersion == nil || *snapshot.LatestVersion != "3.0.0" {
		t.Fatalf("latest_version = %v, want 3.0.0", snapshot.LatestVersion)
	}
	lookup, err := selfupdate.LookupSuppression(binary, "2.0.0")
	if err != nil || !lookup.Suppressed {
		t.Fatalf("suppression was rewritten by a successful check: %+v %v", lookup, err)
	}

	// A transient failure reports update_check_failed without deleting the
	// durable rollback information.
	feed.mu.Lock()
	feed.err = fmt.Errorf("transient network failure")
	feed.mu.Unlock()
	coordinator.requestCheck()
	waitStatus(t, coordinator, updateStatusFailed)
	snapshot = coordinator.Snapshot()
	if snapshot.Error == nil || snapshot.Error.Code != "update_check_failed" {
		t.Fatalf("error = %+v, want update_check_failed", snapshot.Error)
	}
	if snapshot.LatestVersion == nil || *snapshot.LatestVersion != "3.0.0" {
		t.Fatalf("latest_version = %v, want retained discovery", snapshot.LatestVersion)
	}
	coordinator.shutdown(context.Background())
}

func TestCoordinatorSuppressionStoreUnreadableFailsCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binary := dir + "/agentico"
	if err := writeFileTest(binary, "#!/bin/sh\n", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(selfupdate.LeaseDir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	suppressionPath := selfupdate.SuppressionPath(binary)
	if err := writeFileTest(suppressionPath, "{not json", 0o600); err != nil {
		t.Fatal(err)
	}
	coordinator, _, feed, _ := newTestCoordinator(t, UpdateOptions{ExecPath: binary})
	coordinator.start(context.Background())
	waitStatus(t, coordinator, updateStatusFailed)
	snapshot := coordinator.Snapshot()
	if snapshot.Error == nil || snapshot.Error.Code != "update_check_failed" {
		t.Fatalf("error = %+v, want update_check_failed for an unreadable suppression store", snapshot.Error)
	}
	if snapshot.LatestVersion != nil {
		t.Fatalf("latest_version = %v, want unknown", snapshot.LatestVersion)
	}
	_ = feed
	coordinator.shutdown(context.Background())
}

func TestCoordinatorNoEventsForNoOpReads(t *testing.T) {
	t.Parallel()
	coordinator, _, _, recorder := newTestCoordinator(t, UpdateOptions{})
	before := recorder.eventCount()
	snapshot := coordinator.Snapshot()
	_ = snapshot
	if recorder.eventCount() != before {
		t.Fatal("reads must not invent transitions")
	}
	// A repeated identical check result republishes only the changed
	// revision; the checking→available cycle is two transitions.
	coordinator.start(context.Background())
	waitStatus(t, coordinator, updateStatusAvailable)
	_, events, _ := recorder.snapshot()
	if len(events) < 2 {
		t.Fatalf("events = %d, want at least checking + completion", len(events))
	}
	coordinator.shutdown(context.Background())
	_, events, _ = recorder.snapshot()
	// Shutdown publishes nothing.
	afterShutdown := len(events)
	time.Sleep(50 * time.Millisecond)
	_, events, _ = recorder.snapshot()
	if len(events) != afterShutdown {
		t.Fatal("shutdown invented a transition")
	}
}

func TestCoordinatorSanitizesErrorText(t *testing.T) {
	t.Parallel()
	coordinator, _, feed, recorder := newTestCoordinator(t, UpdateOptions{})
	// The coordinator only ever surfaces error text it constructed itself
	// (statuses, deadlines, sanitized receipt reasons) — never raw upstream
	// bodies — and caps it through SanitizeError.
	huge := strings.Repeat("x", 4096)
	feed.mu.Lock()
	feed.err = fmt.Errorf("feed returned status 502 Bad Gateway %s", huge)
	feed.mu.Unlock()
	coordinator.start(context.Background())
	waitStatus(t, coordinator, updateStatusFailed)
	snapshot := coordinator.Snapshot()
	if snapshot.Error == nil {
		t.Fatal("error missing")
	}
	if len(snapshot.Error.Summary) > 2048 {
		t.Fatalf("error summary exceeded the sanitized message cap: %d bytes", len(snapshot.Error.Summary))
	}
	if strings.Contains(snapshot.Error.Summary, "sekrit") || strings.Contains(snapshot.Error.Summary, "Bearer ") {
		t.Fatalf("credential-shaped text reached the snapshot error: %q", snapshot.Error.Summary)
	}
	logs, _, _ := recorder.snapshot()
	for _, line := range logs {
		if len(line) > 2048 {
			t.Fatalf("update: log line exceeded the sanitized cap: %d bytes", len(line))
		}
		if strings.Contains(line, "Bearer ") {
			t.Fatalf("credential-shaped text reached an update: log line: %q", line)
		}
	}
	coordinator.shutdown(context.Background())
}

func writeFileTest(path, content string, mode uint32) error {
	return os.WriteFile(path, []byte(content), fs.FileMode(mode))
}

func TestCoordinatorDisabledAndUnsupportedNeverSchedule(t *testing.T) {
	t.Parallel()
	// Off: no timer, no feed traffic, forbidden mutations (handler level).
	offCoordinator, clock, offFeed, _ := newTestCoordinator(t, UpdateOptions{Policy: selfupdate.PolicyOff})
	offCoordinator.start(context.Background())
	time.Sleep(50 * time.Millisecond)
	if offFeed.calls() != 0 {
		t.Fatal("off policy must make no feed requests")
	}
	clock.Advance(24 * time.Hour)
	time.Sleep(50 * time.Millisecond)
	if offFeed.calls() != 0 {
		t.Fatal("off policy must never schedule a check")
	}
	if snapshot := offCoordinator.Snapshot(); snapshot.Status != updateStatusDisabled {
		t.Fatalf("status = %q, want disabled", snapshot.Status)
	}
	offCoordinator.shutdown(context.Background())

	// Enabled but ineligible: unsupported, no feed traffic.
	unsupported, _, unsupportedFeed, _ := newTestCoordinator(t, UpdateOptions{
		Eligibility: selfupdate.Eligibility{Supported: false, Install: selfupdate.InstallHomebrew, Reason: selfupdate.UnsupportedHomebrew, Remediation: "brew"},
	})
	unsupported.start(context.Background())
	time.Sleep(50 * time.Millisecond)
	if unsupportedFeed.calls() != 0 {
		t.Fatal("unsupported installation must make no feed requests")
	}
	if snapshot := unsupported.Snapshot(); snapshot.Status != updateStatusUnsupported {
		t.Fatalf("status = %q, want unsupported", snapshot.Status)
	}
	unsupported.shutdown(context.Background())
}
