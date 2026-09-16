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
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// defaultUpdateRand draws a uniform value in [0, n) for schedule jitter.
func defaultUpdateRand(n int64) int64 {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return 0
	}
	return v.Int64()
}

// Statuses the availability coordinator emits in this phase. downloading,
// verified, scheduled, draining, and restarting belong to the later
// installation slice; quiescing is reserved and never emitted.
const (
	updateStatusIdle        = "idle"
	updateStatusChecking    = "checking"
	updateStatusUpToDate    = "up_to_date"
	updateStatusAvailable   = "available"
	updateStatusConfirmed   = "confirmed"
	updateStatusFailed      = "failed"
	updateStatusUnsupported = "unsupported"
	updateStatusDisabled    = "disabled"
)

// Retry backoff bounds for transient check failures: one minute doubling to
// a thirty-minute cap, reset by any success.
const (
	updateBackoffBase = time.Minute
	updateBackoffCap  = 30 * time.Minute
)

// FeedChecker performs one metadata-only release check. *selfupdate.FeedClient
// satisfies it; tests inject deterministic fakes.
type FeedChecker interface {
	LatestStable(ctx context.Context) (selfupdate.ReleaseSelection, error)
}

// UpdateObserver receives server.update observations. *observe.Observer
// satisfies it once ServerUpdate is installed there.
type UpdateObserver interface {
	ServerUpdate(from, to, result string)
}

// UpdateOptions carries the resolved startup inputs of the availability
// coordinator: effective policy, validated settings, eligibility, the
// validated recovery outcome of this launch (if any), and the injected
// clock/jitter/log seams for deterministic tests.
type UpdateOptions struct {
	// Policy is the effective startup policy. Empty means no policy was
	// configured for this handler, which resolves safe: disabled.
	Policy selfupdate.Policy
	// Settings are the validated startup settings (channel, interval,
	// strategy, window).
	Settings selfupdate.StartupSettings
	// CurrentVersion is the running build version.
	CurrentVersion string
	// Eligibility is the classification of the running installation.
	Eligibility selfupdate.Eligibility
	// ExecPath is the canonical executable path used for suppression and
	// receipt reads. Empty disables those durable reads.
	ExecPath string
	// StartupReceipt is the validated recovery/handoff receipt of this
	// launch. Nil means the coordinator reads the durable latest receipt
	// best-effort instead.
	StartupReceipt *selfupdate.Receipt
	// Feed performs metadata checks. Nil means no checks can run.
	Feed FeedChecker
	// Stager runs the signed-release candidate pipeline for one pinned
	// version. Nil means installs cannot stage (refused as unavailable).
	Stager ReleaseStager
	// Install owns the process-level replacement work (update lock,
	// transaction begin, final drain/commit/exec). Nil means installs
	// cannot run.
	Install InstallLifecycle
	// Admission is the runtime work-admission boundary installs gate on.
	// Nil means installs cannot run.
	Admission InstallAdmission
	// Activity snapshots the observed work state for the published
	// active-work summary: activity counts, whether detection failed, and
	// the held reservation count.
	Activity func(ctx context.Context) (activity workadmission.Activity, detectionFailed bool, pendingAdmissions int)
	// Now is the injectable clock; nil uses time.Now.
	Now func() time.Time
	// Rand draws a uniform value in [0, n) for schedule jitter; nil uses
	// crypto-quality randomness from math/rand/v2.
	Rand func(n int64) int64
	// Log receives one sanitized "update:" line per visible transition; nil
	// discards.
	Log func(line string)
	// Observer receives server.update observations; nil discards.
	Observer UpdateObserver
}

// updateClock is the deterministic-time seam: Now reads the clock and After
// returns a channel that fires after the given delay (fake clocks fire it on
// Advance).
type updateClock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type realUpdateClock struct{}

func (realUpdateClock) Now() time.Time { return time.Now().UTC() }

func (realUpdateClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

// updateState is the mutable availability snapshot guarded by the
// coordinator mutex.
type updateState struct {
	status         string
	lastCheckAt    *time.Time
	lastSuccessAt  *time.Time
	nextCheckAt    *time.Time
	retryNotBefore *time.Time
	latest         *selfupdate.ReleaseSelection
	wireError      *Error
	receipt        *selfupdate.Receipt
	backoffAttempt int
}

// updateCoordinator owns release-availability state: the snapshot, the
// single in-flight check worker shared by initial, periodic, and explicit
// triggers, the periodic scheduler, retry deadlines, and event/log/
// observation publication. All network work happens outside the mutex so
// reads stay responsive; generation fencing drops stale completions and
// forbids post-shutdown updates.
type updateCoordinator struct {
	opts    UpdateOptions
	clock   updateClock
	rand    func(n int64) int64
	publish func()

	mu    sync.Mutex
	state updateState
	// install is the single accepted install operation; nil when none is
	// active. Guarded by mu.
	install *installOperation
	// activitySummary caches the last advisory activity read for the
	// published snapshot; refreshed outside the mutex by refreshActivity.
	activitySummary UpdateActiveWorkSummary
	started         bool
	stopped         bool
	generation      uint64
	lastRev         string
	// cancel releases the loop's derived context so shutdown cancels
	// in-flight metadata work; nil before start.
	cancel context.CancelFunc

	wake   chan struct{}
	stopCh chan struct{}
	done   chan struct{}
}

func newUpdateCoordinator(opts UpdateOptions) *updateCoordinator {
	if opts.Policy == "" {
		opts.Policy = selfupdate.PolicyOff
	}
	if opts.Settings.CheckInterval <= 0 {
		opts.Settings.CheckInterval = selfupdate.DefaultCheckInterval
	}
	if opts.Settings.Strategy == "" {
		opts.Settings.Strategy = selfupdate.DefaultStrategy
	}
	if opts.Settings.Channel == "" {
		opts.Settings.Channel = selfupdate.DefaultChannel
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Rand == nil {
		opts.Rand = defaultUpdateRand
	}
	c := &updateCoordinator{
		opts:   opts,
		clock:  realUpdateClock{},
		rand:   opts.Rand,
		wake:   make(chan struct{}, 1),
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
	c.state = c.initialState()
	return c
}

// initialState derives the startup snapshot: off wins for visible status;
// then unsupported; then the validated recovery outcome of this launch or
// the durable latest receipt.
func (c *updateCoordinator) initialState() updateState {
	st := updateState{status: updateStatusIdle}
	st.receipt = c.resolveStartupReceipt()
	switch {
	case c.opts.Policy == selfupdate.PolicyOff:
		st.status = updateStatusDisabled
	case !c.opts.Eligibility.Supported:
		st.status = updateStatusUnsupported
	case st.receipt != nil:
		switch st.receipt.Outcome {
		case selfupdate.OutcomeRolledBack:
			st.status = updateStatusFailed
			wireErr := wireError(c.rolledBackError(*st.receipt))
			st.wireError = &wireErr
		case selfupdate.OutcomeConfirmed:
			st.status = updateStatusConfirmed
		}
	}
	return st
}

// resolveStartupReceipt prefers this launch's validated receipt and otherwise
// reads the durable latest receipt best-effort, logging (sanitized) when the
// store is unreadable so an unreadable history is never silently treated as
// an empty successful one.
func (c *updateCoordinator) resolveStartupReceipt() *selfupdate.Receipt {
	if c.opts.StartupReceipt != nil {
		return c.opts.StartupReceipt
	}
	if c.opts.ExecPath == "" {
		return nil
	}
	receipt, found, err := selfupdate.ReadLatestReceipt(c.opts.ExecPath)
	if err != nil {
		c.logf("receipt history unreadable: %s", selfupdate.SanitizeError(err.Error()))
		return nil
	}
	if !found {
		return nil
	}
	return &receipt
}

func (c *updateCoordinator) checksEnabled() bool {
	return c.opts.Policy == selfupdate.PolicyNotify &&
		c.opts.Eligibility.Supported &&
		c.opts.Feed != nil
}

// start launches the scheduler after health/discovery startup. It never
// blocks readiness: the initial check runs asynchronously inside the loop.
func (c *updateCoordinator) start(ctx context.Context) {
	c.mu.Lock()
	if c.started || c.stopped || !c.checksEnabled() {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.mu.Unlock()
	loopCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	// A concurrent shutdown may have fenced the coordinator between the two
	// critical sections; undo the start in that case.
	if c.stopped {
		c.mu.Unlock()
		cancel()
		return
	}
	c.cancel = cancel
	c.mu.Unlock()
	go c.run(loopCtx)
}

// shutdown cancels workers and timers, waits for the loop to exit within the
// caller's deadline, and fences any late completion via the generation
// counter. It never publishes a post-shutdown snapshot change. An install
// operation that already entered its final drain owns shutdown and recovery
// and is left in control; one that has not is cancelled and joined.
func (c *updateCoordinator) shutdown(ctx context.Context) {
	c.shutdownInstall(ctx)
	c.mu.Lock()
	wasStarted := c.started
	if c.stopped {
		c.mu.Unlock()
		if !wasStarted {
			return
		}
		select {
		case <-c.done:
		case <-ctx.Done():
		}
		return
	}
	c.stopped = true
	c.generation++
	c.state.nextCheckAt = nil
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	close(c.stopCh)
	if !wasStarted {
		return
	}
	select {
	case <-c.done:
	case <-ctx.Done():
	}
}

// run is the scheduler loop: an initial check, then periodic checks at the
// configured cadence with bounded jitter, plus explicit triggers that bypass
// periodic waiting. The loop is the single check worker: signals arriving
// while a check runs coalesce into the pending wake slot.
func (c *updateCoordinator) run(ctx context.Context) {
	defer close(c.done)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.performCheck(ctx, "initial")
	for {
		// A wake that arrived while the last check ran is satisfied by it:
		// drain it so coalesced requests never queue a second worker.
		c.drainWake()
		next, has := c.nextDeadline()
		if !has {
			select {
			case <-ctx.Done():
				return
			case <-c.stopCh:
				return
			case <-c.wake:
				c.performCheck(ctx, "explicit")
			}
			continue
		}
		if d := next.Sub(c.clock.Now()); d > 0 {
			select {
			case <-ctx.Done():
				return
			case <-c.stopCh:
				return
			case <-c.wake:
				c.performCheck(ctx, "explicit")
				continue
			case <-c.clock.After(d):
			}
		}
		c.performCheck(ctx, "periodic")
	}
}

// drainWake absorbs one pending wake signal.
func (c *updateCoordinator) drainWake() {
	select {
	case <-c.wake:
	default:
	}
}

func (c *updateCoordinator) nextDeadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.nextCheckAt == nil {
		return time.Time{}, false
	}
	return *c.state.nextCheckAt, true
}

// retryDeadlineRefusal returns the rendered refusal for an explicit check
// inside a server-imposed retry deadline, or nil when no deadline is in
// force. The refused check makes no request.
func (c *updateCoordinator) retryDeadlineRefusal() *errcat.Error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.retryNotBefore == nil || c.clock.Now().After(*c.state.retryNotBefore) {
		return nil
	}
	until := c.state.retryNotBefore.UTC().Format(time.RFC3339)
	rendered := errcat.New(errcat.UpdateCheckFailed,
		errcat.WithParams(errcat.UpdateCheckParams{
			Reason: fmt.Sprintf("manual update checks are paused until %s (server-imposed retry deadline)", until),
		}),
		errcat.WithRemediationHint(fmt.Sprintf("Retry after %s; the deadline is imposed by the release feed and local settings cannot shorten it.", until)))
	return &rendered
}

// requestCheck signals the scheduler worker. A signal arriving while a
// check runs coalesces into the same worker; the request never blocks on
// network I/O.
func (c *updateCoordinator) requestCheck() {
	c.mu.Lock()
	stopped := c.stopped
	c.mu.Unlock()
	if stopped {
		return
	}
	select {
	case c.wake <- struct{}{}:
	default: // a wake is already pending: coalesce
	}
}

// performCheck runs one metadata check. Network work happens outside the
// mutex; the completion is applied only when the generation still matches,
// so shutdown and restart fencing drop stale results.
func (c *updateCoordinator) performCheck(ctx context.Context, trigger string) {
	c.mu.Lock()
	// Absorb any pending wake: this check satisfies it, so signals arriving
	// while a check runs coalesce instead of queueing a second worker.
	select {
	case <-c.wake:
	default:
	}
	if c.stopped {
		c.mu.Unlock()
		return
	}
	if trigger == "explicit" && c.state.retryNotBefore != nil && c.clock.Now().Before(*c.state.retryNotBefore) {
		// A deadline may have been imposed after the caller validated; an
		// explicit trigger never bypasses it.
		c.mu.Unlock()
		return
	}
	generation := c.generation
	prevStatus := c.state.status
	changed := c.setStatusLocked(updateStatusChecking)
	c.mu.Unlock()
	if changed {
		c.emitTransition(prevStatus, updateStatusChecking, "check_started:"+trigger)
	}

	sel, err := c.fetch(ctx)

	c.mu.Lock()
	if c.stopped || generation != c.generation {
		c.mu.Unlock()
		return
	}
	result := c.applyCheckResultLocked(sel, err)
	newStatus := c.state.status
	revision := c.commitRevisionLocked()
	c.mu.Unlock()
	if revision != "" {
		c.emitTransition(updateStatusChecking, newStatus, result)
	}
}

// fetch performs the metadata lookup outside the coordinator mutex.
func (c *updateCoordinator) fetch(ctx context.Context) (selfupdate.ReleaseSelection, error) {
	if c.opts.Feed == nil {
		return selfupdate.ReleaseSelection{}, fmt.Errorf("no release feed configured")
	}
	return c.opts.Feed.LatestStable(ctx)
}

// applyCheckResultLocked folds one finished check into the snapshot. A
// failed refresh retains the prior latest-release metadata and its
// last-success timestamp; a success resets the retry floor and backoff and
// schedules the next periodic check with bounded jitter.
func (c *updateCoordinator) applyCheckResultLocked(sel selfupdate.ReleaseSelection, err error) string {
	now := c.clock.Now()
	st := &c.state
	st.lastCheckAt = &now

	if err != nil {
		st.status = updateStatusFailed
		wireErr := wireError(errcat.New(errcat.UpdateCheckFailed,
			errcat.WithParams(errcat.UpdateCheckParams{Reason: selfupdate.SanitizeError(err.Error())})))
		st.wireError = &wireErr
		st.backoffAttempt++
		backoff := updateBackoffBase << (st.backoffAttempt - 1)
		if backoff > updateBackoffCap || backoff <= 0 {
			backoff = updateBackoffCap
		}
		next := now.Add(backoff)
		if notBefore, retry := selfupdate.IsFeedRetryError(err); retry {
			st.retryNotBefore = &notBefore
			// A server-imposed deadline is a floor local backoff cannot
			// shorten.
			if notBefore.After(next) {
				next = notBefore
			}
		} else {
			st.retryNotBefore = nil
		}
		st.nextCheckAt = &next
		return "check_failed:update_check_failed"
	}

	// Suppression is durable state independent of discovery: an unreadable
	// store fails the check rather than risking a wrong "available", and a
	// successful check never rewrites or clears it.
	if c.opts.ExecPath != "" {
		lookup, lookupErr := selfupdate.LookupSuppression(c.opts.ExecPath, sel.Version)
		if lookupErr != nil {
			st.status = updateStatusFailed
			wireErr := wireError(errcat.New(errcat.UpdateCheckFailed,
				errcat.WithParams(errcat.UpdateCheckParams{
					Reason: selfupdate.SanitizeError(fmt.Sprintf("suppression store unreadable: %v", lookupErr)),
				})))
			st.wireError = &wireErr
			st.backoffAttempt++
			backoff := updateBackoffBase << (st.backoffAttempt - 1)
			if backoff > updateBackoffCap || backoff <= 0 {
				backoff = updateBackoffCap
			}
			next := now.Add(backoff)
			st.nextCheckAt = &next
			return "check_failed:update_check_failed"
		}
		st.retryNotBefore = nil
		st.backoffAttempt = 0
		st.latest = &sel
		st.lastSuccessAt = &now
		if lookup.Suppressed {
			// The suppressed newest release stays visible as latest_version
			// while the durable rollback outcome explains why it is not
			// available.
			st.status = updateStatusFailed
			wireErr := wireError(c.rolledBackErrorFor(sel))
			st.wireError = &wireErr
			next := now.Add(c.jitteredInterval())
			st.nextCheckAt = &next
			return "check_failed:update_rolled_back"
		}
	} else {
		st.retryNotBefore = nil
		st.backoffAttempt = 0
		st.latest = &sel
		st.lastSuccessAt = &now
	}

	current := selfupdate.NormalizeVersion(c.opts.CurrentVersion)
	status := updateStatusUpToDate
	if sel.Version != current {
		if cmp, ordered := selfupdate.CompareReleaseVersions(current, sel.Version); ordered && cmp < 0 {
			status = updateStatusAvailable
		}
	}
	st.status = status
	st.wireError = nil
	next := now.Add(c.jitteredInterval())
	st.nextCheckAt = &next
	if status == updateStatusAvailable {
		return "check_success:available"
	}
	return "check_success:up_to_date"
}

// commitRevisionLocked recomputes the snapshot revision and returns it when
// the visible snapshot changed since the last commit (startup counts as the
// first commit without an event).
func (c *updateCoordinator) commitRevisionLocked() string {
	rev := revisionForAny(c.snapshotLocked())
	if rev == c.lastRev {
		return ""
	}
	c.lastRev = rev
	return rev
}

// jitteredInterval draws the next periodic delay: the configured interval
// with jitter bounded by selfupdate.JitterDelay, clamped so realized delays
// stay positive even for very short intervals.
func (c *updateCoordinator) jitteredInterval() time.Duration {
	interval := c.opts.Settings.CheckInterval
	if interval <= 0 {
		return selfupdate.DefaultCheckInterval
	}
	magnitude := selfupdate.JitterDelay(interval)
	if magnitude <= 0 {
		return interval
	}
	offset := c.rand(int64(2*magnitude)+1) - int64(magnitude)
	delay := interval + time.Duration(offset)
	if delay <= 0 {
		return interval
	}
	return delay
}

func (c *updateCoordinator) setStatusLocked(status string) bool {
	if c.state.status == status {
		return false
	}
	c.state.status = status
	return true
}

// rolledBackError renders the canonical update_rolled_back projection of a
// validated rollback receipt.
func (c *updateCoordinator) rolledBackError(receipt selfupdate.Receipt) errcat.Error {
	return errcat.New(errcat.UpdateRolledBack,
		errcat.WithParams(errcat.UpdateRolledBackParams{
			FromVersion: receipt.FromVersion,
			ToVersion:   receipt.ToVersion,
		}),
		errcat.WithDiagnostics(selfupdate.SanitizeError(receipt.Error)))
}

// rolledBackErrorFor renders the suppressed-newest projection: the newest
// release is the historically rolled-back target, so the durable outcome —
// not a fresh install failure — explains the failed state.
func (c *updateCoordinator) rolledBackErrorFor(sel selfupdate.ReleaseSelection) errcat.Error {
	from, to := c.opts.CurrentVersion, sel.Version
	if c.state.receipt != nil {
		from, to = c.state.receipt.FromVersion, c.state.receipt.ToVersion
	}
	return errcat.New(errcat.UpdateRolledBack,
		errcat.WithParams(errcat.UpdateRolledBackParams{FromVersion: from, ToVersion: to}),
		errcat.WithDiagnostics(selfupdate.SanitizeError(
			fmt.Sprintf("newest release %s is suppressed by a rolled-back update", sel.Version))))
}

// confirmReceipt settles an adopted handoff: the public snapshot exposes the
// confirmed outcome and its receipt. No-op when shutdown already fenced the
// coordinator or the receipt is already projected.
func (c *updateCoordinator) confirmReceipt(receipt selfupdate.Receipt) {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	if c.state.receipt != nil && c.state.receipt.TransactionID == receipt.TransactionID &&
		c.state.receipt.Outcome == receipt.Outcome {
		c.mu.Unlock()
		return
	}
	prevStatus := c.state.status
	c.state.receipt = &receipt
	if c.state.status == updateStatusIdle || c.state.status == updateStatusChecking {
		c.state.status = updateStatusConfirmed
	}
	revision := c.commitRevisionLocked()
	newStatus := c.state.status
	c.mu.Unlock()
	if revision != "" {
		c.emitTransition(prevStatus, newStatus, "handoff_confirmed")
	}
}

// emitTransition publishes one visible snapshot change: the SSE
// invalidation, one sanitized update: log line, and one server.update
// observation. Observer or sink failures never stop discovery or the server.
func (c *updateCoordinator) emitTransition(from, to, result string) {
	if c.publish != nil {
		c.publish()
	}
	c.logf("%s -> %s (%s)", from, to, result)
	if c.opts.Observer != nil {
		c.opts.Observer.ServerUpdate(from, to, result)
	}
}

func (c *updateCoordinator) logf(format string, args ...any) {
	if c.opts.Log == nil {
		return
	}
	c.opts.Log("update: " + selfupdate.SanitizeError(fmt.Sprintf(format, args...)))
}

// snapshotLocked builds the generated wire snapshot from the current state.
// Callers must hold the mutex. An active install operation takes precedence
// over the availability status and contributes the pinned target, waiting
// method, and verified-candidate fields; scheduled_for is explicitly null
// because an idle wait has no predicted deadline.
func (c *updateCoordinator) snapshotLocked() UpdateSnapshot {
	opts := c.opts
	status := UpdateSnapshotStatus(c.state.status)
	snap := UpdateSnapshot{
		Status:               status,
		Policy:               UpdateSnapshotPolicy(opts.Policy),
		Channel:              UpdateSnapshotChannel(opts.Settings.Channel),
		Strategy:             UpdateSnapshotStrategy(opts.Settings.Strategy),
		CurrentVersion:       opts.CurrentVersion,
		Installation:         UpdateSnapshotInstallation(opts.Eligibility.Install),
		Signature:            Unverified,
		CheckIntervalSeconds: int(opts.Settings.CheckInterval.Seconds()),
	}
	if op := c.install; op != nil {
		snap.Status = UpdateSnapshotStatus(op.status)
		method := UpdateSnapshotMethod(op.when)
		snap.Method = &method
		stop := op.stopActiveWork
		snap.StopActiveWork = &stop
		target := op.target.Version
		snap.TargetVersion = &target
		if op.candidate != nil {
			snap.Signature = Verified
		}
		if op.targetContract != nil {
			snap.TargetContract = &UpdateTargetContract{
				// The wire contract is the public projection of the verified
				// envelope contract; api_version renders as its decimal
				// string form.
				APIVersion:      strconv.Itoa(op.targetContract.APIVersion),
				SchemaVersion:   op.targetContract.SchemaVersion,
				MinClientSchema: op.targetContract.MinClientSchema,
			}
		}
		if op.wireError != nil {
			snap.Error = op.wireError
		}
	}
	// scheduled_for is explicitly null: an idle install waits without a
	// predicted deadline, and an immediate install schedules nothing.
	snap.ScheduledFor = nil
	if !opts.Eligibility.Supported {
		reason := UpdateSnapshotUnsupportedReason(opts.Eligibility.Reason)
		snap.UnsupportedReason = &reason
		remediation := opts.Eligibility.Remediation
		snap.Remediation = &remediation
	}
	if c.state.latest != nil {
		latest := c.state.latest.Version
		snap.LatestVersion = &latest
		if releaseURL := c.state.latest.ReleaseURL; releaseURL != "" {
			snap.LatestReleaseURL = &releaseURL
		}
	}
	if c.state.lastCheckAt != nil {
		t := c.state.lastCheckAt.UTC()
		snap.LastCheckAt = &t
	}
	if c.state.lastSuccessAt != nil {
		t := c.state.lastSuccessAt.UTC()
		snap.LastSuccessAt = &t
	}
	if c.state.nextCheckAt != nil && c.checksEnabled() {
		t := c.state.nextCheckAt.UTC()
		snap.NextCheckAt = &t
	}
	if c.state.retryNotBefore != nil {
		t := c.state.retryNotBefore.UTC()
		snap.RetryNotBefore = &t
	}
	if c.state.wireError != nil && snap.Error == nil {
		snap.Error = c.state.wireError
	}
	if c.state.receipt != nil {
		snap.Receipt = publicUpdateReceipt(*c.state.receipt)
	}
	// The activity summary is a cached advisory read: detection runs
	// outside this mutex, refreshed on every snapshot request. Activity
	// changes alone do not bump the snapshot revision.
	snap.ActiveWorkSummary = c.activitySummary
	return snap
}

// Snapshot returns the current wire snapshot. The activity summary is
// refreshed outside the mutex first so slow discovery never stalls reads.
func (c *updateCoordinator) Snapshot() UpdateSnapshot {
	c.refreshActivity()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

// options exposes the resolved inputs for the request handlers.
func (c *updateCoordinator) options() UpdateOptions {
	return c.opts
}

// publicUpdateReceipt projects a durable receipt onto the public model:
// versions, outcome, times, and sanitized error only — never transaction
// paths, descriptors, process environment, credentials, or raw receipt
// internals.
func publicUpdateReceipt(receipt selfupdate.Receipt) *UpdatePublicReceipt {
	public := &UpdatePublicReceipt{
		Outcome:     UpdatePublicReceiptOutcome(receipt.Outcome),
		FromVersion: receipt.FromVersion,
		ToVersion:   receipt.ToVersion,
	}
	settled := receipt.ResolvedAt
	if settled.IsZero() {
		settled = receipt.UpdatedAt
	}
	if !settled.IsZero() {
		t := settled.UTC()
		public.CompletedAt = &t
	}
	if msg := selfupdate.SanitizeError(strings.TrimSpace(receipt.Error)); msg != "" {
		public.Error = &msg
	}
	return public
}
