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
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// Statuses the install operation emits. They take precedence over the
// availability statuses while an operation is active.
const (
	updateStatusDownloading = "downloading"
	updateStatusVerified    = "verified"
	updateStatusScheduled   = "scheduled"
	updateStatusDraining    = "draining"
	updateStatusRestarting  = "restarting"
)

// install waiting methods.
const (
	updateInstallWhenNow  = "now"
	updateInstallWhenIdle = "idle"
)

// installRetryAfterSeconds bounds how long cancellation and shutdown wait
// for in-flight install work to settle.
const installSettleTimeout = 30 * time.Second

// installResponseDeadline bounds how long the final drain waits for the
// accepting request's response to complete before shutdown begins.
const installResponseDeadline = 10 * time.Second

// InstallAdmission is the work-boundary surface the install coordinator
// drives; *workadmission.Coordinator satisfies it.
type InstallAdmission interface {
	WaitForIdle(ctx context.Context) error
	CloseIfQuiesced() bool
	Open()
	Closed() bool
	Detect(ctx context.Context) (workadmission.Activity, error)
	Held() (int, map[workadmission.Category]int)
}

// ReleaseStager runs the signed-release candidate pipeline for one pinned
// version: resolve, verify, authenticated staging, the isolated probe, and
// admission. The cmd layer implements it over the existing selfupdate
// pipeline; progress reports classify failures and drive statuses.
type ReleaseStager interface {
	// StageCandidate stages the pinned release version. progress reports
	// the pipeline stage ("resolve", "download", "verify", "probe",
	// "admit") and is called with the stage active when an error surfaces,
	// so the caller can classify the canonical failure. cleanup settles the
	// owned release staging idempotently; it is nil only when staging never
	// created anything.
	StageCandidate(ctx context.Context, version string, progress func(stage string)) (candidate selfupdate.VerifiedCandidate, contract *selfupdate.ServerContract, cleanup func() error, err error)
}

// InstallTransaction is one prepared durable replacement transaction.
type InstallTransaction interface {
	// ID is the durable transaction identifier.
	ID() string
	// Cancel settles a pre-replacement transaction consistently: the
	// pending receipt resolves as abandoned pre-replacement (never a
	// failed boot, never rollback suppression) and validated owned
	// resources are cleaned. Idempotent.
	Cancel() error
}

// InstallLifecycle owns the process-level replacement work the runtime
// server cannot perform itself. The cmd layer implements it with the same
// drain→commit→exec sequence the test driver journeys exercise.
type InstallLifecycle interface {
	// AcquireUpdateLock takes the per-runtime update transaction lock; the
	// guarded commit section holds it. The returned release drops it.
	AcquireUpdateLock() (release func(), acquired bool)
	// Begin prepares the durable transaction — revalidated provenance,
	// owner-only backup, pending write-ahead receipt — while HTTP is still
	// available.
	Begin(candidate selfupdate.VerifiedCandidate) (InstallTransaction, error)
	// Replace runs the final drain, commit, and exec. notify publishes
	// lifecycle statuses (draining, restarting); responseDone must complete
	// before shutdown begins. On success it never returns. When the old
	// build keeps serving (a drain-stage abort), it returns the sanitized
	// reason; recovery paths re-exec and never return.
	Replace(handle InstallTransaction, notify func(status string), responseDone <-chan struct{}) error
}

// installRefusal is one typed install-request refusal for the handler.
type installRefusal struct {
	status int
	code   errcat.Code
	opts   []errcat.Option
}

func (r *installRefusal) Error() string {
	return string(r.code)
}

// installAcceptance reports what requestInstall did: coalesced means an
// equivalent request returned the existing operation.
type installAcceptance struct {
	coalesced bool
}

// installOperation is the single accepted install operation. All field
// access is guarded by the updateCoordinator mutex unless noted.
type installOperation struct {
	when           string
	stopActiveWork bool
	target         selfupdate.ReleaseSelection
	targetContract *selfupdate.ServerContract
	status         string
	candidate      *selfupdate.VerifiedCandidate
	tx             InstallTransaction
	cleanup        func() error
	wireError      *Error

	// generation fences stale worker transitions: it advances on
	// cancellation and coordinator shutdown.
	generation    uint64
	cancelling    bool
	drainEntered  bool
	cleanupFailed bool

	cancel context.CancelFunc
	done   chan struct{}
	// responseDone is closed by the accepting handler after its 202 is
	// written; the final drain waits for it before shutdown begins.
	responseDone chan struct{}
	// cleanupMu serializes idempotent resource cleanup between the worker
	// and a cancellation retry so no two cleanup routines ever overlap.
	cleanupMu sync.Mutex
}

// installRequest is the validated wire request.
type installRequest struct {
	consent        bool
	when           string
	stopActiveWork bool
	version        string // empty means "pin the discovered latest"
}

// requestInstall accepts, coalesces, or refuses one consented install
// request. Refusals carry the transport status and canonical code; the
// caller renders them. The accepted operation belongs to the runtime: a
// caller disconnect never cancels it.
func (c *updateCoordinator) requestInstall(req installRequest, responseDone chan struct{}) (installAcceptance, *installRefusal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return installAcceptance{}, &installRefusal{status: http.StatusServiceUnavailable, code: errcat.Unavailable}
	}
	if op := c.install; op != nil {
		if op.cleanupFailed {
			return installAcceptance{}, &installRefusal{
				status: http.StatusConflict,
				code:   errcat.UpdateInstallFailed,
				opts: []errcat.Option{
					errcat.WithDiagnostics("the previous install operation's cleanup failed; retry DELETE /api/v1/update/install to clean its owned resources before requesting a new install"),
				},
			}
		}
		// Equivalent requests — including retries after latest_version
		// moved — return the existing operation and never launch a second
		// worker. A change to the waiting method, stop permission, or the
		// pinned target is a conflict that requires cancellation first.
		equivalent := req.when == op.when && req.stopActiveWork == op.stopActiveWork &&
			(req.version == "" || req.version == op.target.Version)
		if !equivalent {
			return installAcceptance{}, &installRefusal{
				status: http.StatusConflict,
				code:   errcat.Conflict,
				opts: []errcat.Option{
					errcat.WithDiagnostics("an install operation is already active; cancel it (DELETE /api/v1/update/install) to change the target, waiting method, or stop permission"),
				},
			}
		}
		return installAcceptance{coalesced: true}, nil
	}

	// Pin the last discovered eligible stable release atomically with
	// acceptance. Without a discovered release there is nothing to install.
	latest := c.state.latest
	if latest == nil {
		return installAcceptance{}, &installRefusal{
			status: http.StatusConflict,
			code:   errcat.Conflict,
			opts: []errcat.Option{
				errcat.WithDiagnostics("no release has been discovered yet; run a check (POST /api/v1/update/check) first"),
			},
		}
	}
	if req.version != "" && req.version != latest.Version {
		return installAcceptance{}, &installRefusal{
			status: http.StatusConflict,
			code:   errcat.Conflict,
			opts: []errcat.Option{
				errcat.WithDiagnostics(fmt.Sprintf("target version %q is not the discovered latest stable release %q", req.version, latest.Version)),
			},
		}
	}
	current := selfupdate.NormalizeVersion(c.opts.CurrentVersion)
	if cmp, ordered := selfupdate.CompareReleaseVersions(current, latest.Version); !ordered || cmp >= 0 {
		return installAcceptance{}, &installRefusal{
			status: http.StatusConflict,
			code:   errcat.Conflict,
			opts: []errcat.Option{
				errcat.WithDiagnostics(fmt.Sprintf("the discovered release %s is not newer than the installed %s", latest.Version, current)),
			},
		}
	}
	// Suppression lookup failure blocks admission rather than assuming
	// eligibility; a suppressed target refuses with the durable rollback
	// outcome.
	if c.opts.ExecPath != "" {
		lookup, lookupErr := selfupdate.LookupSuppression(c.opts.ExecPath, latest.Version)
		if lookupErr != nil {
			return installAcceptance{}, &installRefusal{
				status: http.StatusConflict,
				code:   errcat.UpdateCheckFailed,
				opts: []errcat.Option{
					errcat.WithParams(errcat.UpdateCheckParams{Reason: "suppression store unreadable: refusing to admit an install target"}),
				},
			}
		}
		if lookup.Suppressed {
			return installAcceptance{}, &installRefusal{
				status: http.StatusConflict,
				code:   errcat.UpdateRolledBack,
				opts: []errcat.Option{
					errcat.WithParams(errcat.UpdateRolledBackParams{FromVersion: current, ToVersion: latest.Version}),
					errcat.WithDiagnostics("the discovered release was rolled back by a failed install and is suppressed"),
				},
			}
		}
	}

	op := &installOperation{
		when:         req.when,
		target:       *latest,
		status:       updateStatusDownloading,
		generation:   c.generation,
		cancel:       nil,
		done:         make(chan struct{}),
		responseDone: responseDone,
	}
	ctx, cancel := context.WithCancel(context.Background())
	op.cancel = cancel
	c.install = op
	c.mu.Unlock()
	go c.runInstall(op, ctx)
	c.mu.Lock()
	return installAcceptance{}, nil
}

// cancelResult reports the cancellation outcome.
type cancelResult int

const (
	cancelNone cancelResult = iota
	cancelCompleted
	cancelCleanupFailed
	cancelDrainRefused
	cancelTimedOut
)

// cancelInstall is the sole cancellation/cleanup owner for an accepted
// operation, serialized with install and ordinary shutdown. It is
// idempotent when no operation exists.
func (c *updateCoordinator) cancelInstall(ctx context.Context) (cancelResult, *installRefusal) {
	c.mu.Lock()
	op := c.install
	if op == nil {
		c.mu.Unlock()
		return cancelNone, nil
	}
	if op.drainEntered {
		// At or after drain entry the operation finishes installation or
		// recovery; cancellation returns the canonical conflict.
		c.mu.Unlock()
		return cancelDrainRefused, &installRefusal{
			status: http.StatusConflict,
			code:   errcat.UpdateInProgress,
			opts:   []errcat.Option{errcat.WithDiagnostics("the installation is draining; cancellation is no longer available")},
		}
	}
	if !op.cancelling {
		op.cancelling = true
		op.generation++
		cancel := op.cancel
		done := op.done
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if err := waitInstallSettled(ctx, done); err != nil {
			return cancelTimedOut, &installRefusal{
				status: http.StatusServiceUnavailable,
				code:   errcat.UpdateInProgress,
				opts:   []errcat.Option{errcat.WithDiagnostics("the cancelled install did not settle in time; retry cancellation")},
			}
		}
	} else {
		done := op.done
		c.mu.Unlock()
		if err := waitInstallSettled(ctx, done); err != nil {
			return cancelTimedOut, &installRefusal{
				status: http.StatusServiceUnavailable,
				code:   errcat.UpdateInProgress,
				opts:   []errcat.Option{errcat.WithDiagnostics("the cancelled install did not settle in time; retry cancellation")},
			}
		}
	}

	// The worker settled. A retained operation means its cleanup failed:
	// this repeated DELETE owns the idempotent retry.
	c.mu.Lock()
	op = c.install
	if op == nil {
		c.mu.Unlock()
		return cancelCompleted, nil
	}
	if !op.cleanupFailed {
		// The worker cleared the operation itself.
		c.mu.Unlock()
		return cancelCompleted, nil
	}
	c.mu.Unlock()
	if err := cleanupInstallResources(op); err != nil {
		return cancelCleanupFailed, &installRefusal{
			status: http.StatusConflict,
			code:   errcat.UpdateInstallFailed,
			opts: []errcat.Option{
				errcat.WithDiagnostics(selfupdate.SanitizeError(err.Error())),
				errcat.WithRemediationHint("The install operation keeps ownership of its resources until cleanup succeeds; retry DELETE /api/v1/update/install."),
			},
		}
	}
	c.mu.Lock()
	if c.install == op {
		c.install = nil
	}
	c.mu.Unlock()
	c.admissionOpen()
	return cancelCompleted, nil
}

// waitInstallSettled waits for one operation's worker to settle within the
// bounded deadline or the caller's context.
func waitInstallSettled(ctx context.Context, done chan struct{}) error {
	timeout := time.NewTimer(installSettleTimeout)
	defer timeout.Stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timeout.C:
		return fmt.Errorf("install worker did not settle within %s", installSettleTimeout)
	}
}

// cleanupInstallResources performs the validated idempotent cleanup of one
// operation's owned resources: a begun transaction settles consistently
// (abandoned pre-replacement, never rollback suppression) and release
// staging is cleaned. Serialized per operation so the worker and a
// cancellation retry never overlap.
func cleanupInstallResources(op *installOperation) error {
	op.cleanupMu.Lock()
	defer op.cleanupMu.Unlock()
	var firstErr error
	if op.tx != nil {
		if err := op.tx.Cancel(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if op.cleanup != nil {
		if err := op.cleanup(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// admissionOpen reopens the work boundary after an aborted or cancelled
// install. Ordinary work admission resumes; refused work-start requests
// during the closure have already failed and are not replayed.
func (c *updateCoordinator) admissionOpen() {
	if c.opts.Admission != nil {
		c.opts.Admission.Open()
	}
}

// applyInstallStatus records one worker status transition when the worker
// still owns the operation, and publishes the visible change. result feeds
// the sanitized log and observation.
func (c *updateCoordinator) applyInstallStatus(op *installOperation, status, result string) {
	c.mu.Lock()
	stale := c.install != op || op.cancelling
	if !stale && op.drainEntered && status != updateStatusDraining && status != updateStatusRestarting {
		stale = true
	}
	if stale {
		c.mu.Unlock()
		return
	}
	prev := op.status
	op.status = status
	revision := c.commitRevisionLocked()
	c.mu.Unlock()
	if revision != "" {
		c.emitTransition(prev, status, result)
	}
}

// failInstall settles one failed install: the canonical failure becomes the
// public snapshot error, owned resources settle through validated cleanup,
// admission reopens, and the operation clears so a later install requires
// fresh consent.
func (c *updateCoordinator) failInstall(op *installOperation, code errcat.Code, result string, opts ...errcat.Option) {
	wireErr := wireError(errcat.New(code, opts...))
	c.mu.Lock()
	if c.install != op {
		c.mu.Unlock()
		return
	}
	op.wireError = &wireErr
	op.status = updateStatusFailed
	// Fold the failure into the availability state so it stays visible
	// after the operation clears: a failed install requires fresh consent.
	c.state.status = updateStatusFailed
	c.state.wireError = &wireErr
	c.mu.Unlock()
	if code == errcat.UpdateBlockedActiveWork {
		// A blocked immediate attempt never closed admission; nothing to
		// reopen and no owned resources to keep.
		c.clearInstallOp(op)
		return
	}
	if err := cleanupInstallResources(op); err != nil {
		c.mu.Lock()
		if c.install == op {
			op.cleanupFailed = true
		}
		c.mu.Unlock()
		c.admissionOpen()
		c.applySettledFailureLog(op, result+":cleanup_failed")
		return
	}
	c.clearInstallOp(op)
	c.admissionOpen()
	c.logInstallFailure(op, result)
}

// clearInstallOp removes the settled operation, keeping its failure visible
// through the availability state.
func (c *updateCoordinator) clearInstallOp(op *installOperation) {
	c.mu.Lock()
	if c.install == op {
		c.install = nil
	}
	c.mu.Unlock()
}

func (c *updateCoordinator) logInstallFailure(op *installOperation, result string) {
	c.mu.Lock()
	revision := c.commitRevisionLocked()
	c.mu.Unlock()
	if revision != "" {
		c.emitTransition(updateStatusFailed, c.snapshotStatusLocked(), result)
	}
}

func (c *updateCoordinator) applySettledFailureLog(op *installOperation, result string) {
	c.logInstallFailure(op, result)
}

// snapshotStatusLocked returns the effective visible status.
func (c *updateCoordinator) snapshotStatusLocked() string {
	if c.install != nil {
		return c.install.status
	}
	return c.state.status
}

// runInstall is the single install worker. All slow work — network,
// verification, probe, filesystem, cleanup — happens outside the
// coordinator mutex; generation and pointer identity fence stale results.
func (c *updateCoordinator) runInstall(op *installOperation, ctx context.Context) {
	defer close(op.done)
	defer func() {
		if op.cancel != nil {
			op.cancel()
		}
	}()

	stager := c.opts.Stager
	lifecycle := c.opts.Install
	admission := c.opts.Admission
	if stager == nil || lifecycle == nil || admission == nil {
		c.failInstall(op, errcat.UpdateInstallFailed, "install_unsupported",
			errcat.WithDiagnostics("this runtime cannot install updates"))
		return
	}

	c.applyInstallStatus(op, updateStatusDownloading, "install_started:"+op.when)
	lastStage := "resolve"
	candidate, contract, cleanup, err := stager.StageCandidate(ctx, op.target.Version, func(stage string) { lastStage = stage })
	if err != nil {
		// The pipeline cleans its own owned staging on failure; the
		// coordinator retains the idempotent cleanup for the failure
		// settle and any later cancellation retry.
		c.mu.Lock()
		op.cleanup = cleanup
		c.mu.Unlock()
		if ctx.Err() != nil {
			c.settleCancelled(op, cleanup)
			return
		}
		code := errcat.UpdateInstallFailed
		switch lastStage {
		case "resolve", "download":
			code = errcat.UpdateDownloadFailed
		case "verify", "probe", "admit":
			code = errcat.UpdateSignatureFailed
		}
		c.failInstall(op, code, "install_failed:"+lastStage,
			errcat.WithParams(errcat.UpdateTargetFailureParams{
				Version: op.target.Version,
				Reason:  selfupdate.SanitizeError(err.Error()),
			}))
		return
	}
	c.mu.Lock()
	op.candidate = &candidate
	op.cleanup = cleanup
	if contract != nil {
		op.targetContract = contract
	}
	c.mu.Unlock()
	c.applyInstallStatus(op, updateStatusVerified, "install_verified")

	if op.when == updateInstallWhenIdle {
		c.applyInstallStatus(op, updateStatusScheduled, "install_scheduled")
	} else {
		// when=now refuses active or uncertain work before commitment;
		// stop_active_work is accepted but never stops work in this phase.
		if blockers := c.installBlockers(admission); blockers != nil {
			c.failInstall(op, errcat.UpdateBlockedActiveWork, "install_blocked_active_work", blockers...)
			return
		}
		if !admission.CloseIfQuiesced() {
			blockers := c.installBlockers(admission)
			if blockers == nil {
				blockers = []errcat.Option{errcat.WithParams(errcat.UpdateBlockedActiveWorkParams{PendingAdmissions: 1})}
			}
			c.failInstall(op, errcat.UpdateBlockedActiveWork, "install_blocked_active_work", blockers...)
			return
		}
	}

	// The guarded commit section: wait for idle, close the boundary, then
	// hold the per-runtime update transaction lock while rechecking. A
	// blocker under the lock reopens admission and returns an idle
	// operation to scheduled without consuming fresh consent or
	// retargeting; an immediate operation aborts as blocked. This is the
	// boundary Phase 6 extends with explicit session stopping — Phase 5
	// never stops active work.
	for {
		if err := c.awaitIdleForInstall(ctx, op, admission); err != nil {
			if ctx.Err() != nil {
				c.settleCancelled(op, cleanup)
				return
			}
			// Uncertain detection blocks progress without inventing a
			// deadline: stay scheduled, reevaluate after the bounded wait.
			c.logInstallWait(op, err)
			if !sleepInstallWait(ctx) {
				c.settleCancelled(op, cleanup)
				return
			}
			continue
		}
		release, acquired := lifecycle.AcquireUpdateLock()
		if !acquired {
			c.admissionOpen()
			c.failInstall(op, errcat.UpdateInstallFailed, "install_failed:update_lock",
				errcat.WithParams(errcat.UpdateTargetFailureParams{
					Version: op.target.Version,
					Reason:  "another update operation holds this runtime's update lock",
				}))
			return
		}
		blockers := c.installBlockers(admission)
		if blockers != nil || !c.installHeldNone(admission) {
			release()
			c.admissionOpen()
			if blockers == nil {
				blockers = []errcat.Option{errcat.WithParams(errcat.UpdateBlockedActiveWorkParams{PendingAdmissions: 1})}
			}
			if op.when == updateInstallWhenNow {
				c.failInstall(op, errcat.UpdateBlockedActiveWork, "install_blocked_active_work", blockers...)
				return
			}
			c.applyInstallStatus(op, updateStatusScheduled, "install_rescheduled")
			continue
		}
		handle, err := lifecycle.Begin(candidate)
		if err != nil {
			release()
			c.admissionOpen()
			c.failInstall(op, errcat.UpdateInstallFailed, "install_failed:begin",
				errcat.WithParams(errcat.UpdateTargetFailureParams{
					Version: op.target.Version,
					Reason:  selfupdate.SanitizeError(err.Error()),
				}))
			return
		}
		c.mu.Lock()
		op.tx = handle
		cancelled := op.cancelling || c.stopped
		c.mu.Unlock()
		if cancelled {
			// Cancellation wins until final drain begins: the prepared
			// pre-replacement receipt settles consistently and owned
			// resources are cleaned.
			release()
			c.settleCancelled(op, cleanup)
			return
		}

		// From here the operation owns shutdown: cancellation and ordinary
		// shutdown defer to the installation or its recovery.
		c.mu.Lock()
		op.drainEntered = true
		c.mu.Unlock()
		c.applyInstallStatus(op, updateStatusDraining, "install_draining")

		// The accepted response completes before shutdown begins.
		waitResponseDone(op.responseDone)

		replaceErr := lifecycle.Replace(handle, func(status string) {
			if status == updateStatusRestarting {
				c.mu.Lock()
				if c.install == op {
					op.status = updateStatusRestarting
				}
				c.mu.Unlock()
			}
		}, op.responseDone)
		// Replace never returns on success or recovery-exec paths.
		// Returning means the old build kept serving: release the guarded
		// section, reopen admission, and settle the truthful failure; a
		// later attempt requires fresh consent.
		release()
		c.admissionOpen()
		reason := "the replacement returned without executing the target"
		if replaceErr != nil {
			reason = selfupdate.SanitizeError(replaceErr.Error())
		}
		c.failInstall(op, errcat.UpdateInstallFailed, "install_failed:drain",
			errcat.WithParams(errcat.UpdateTargetFailureParams{
				Version: op.target.Version,
				Reason:  reason,
			}))
		return
	}
}

// sleepInstallWait paces one reevaluation after blocked progress. It
// returns false when the operation's context ended.
func sleepInstallWait(ctx context.Context) bool {
	select {
	case <-time.After(time.Second):
		return true
	case <-ctx.Done():
		return false
	}
}

// waitResponseDone waits for the accepting response to complete within the
// bounded deadline. A disconnecting caller never cancels the operation.
func waitResponseDone(responseDone <-chan struct{}) {
	if responseDone == nil {
		return
	}
	timeout := time.NewTimer(installResponseDeadline)
	defer timeout.Stop()
	select {
	case <-responseDone:
	case <-timeout.C:
	}
}

// awaitIdleForInstall closes the admission boundary once the runtime is
// observed idle: activity events and the bounded fallback poll drive
// reevaluation, and the atomic closure re-checks the reservation count
// under the boundary's own mutex. When work races in first, the caller
// keeps waiting. An already-closed boundary is idle by construction.
func (c *updateCoordinator) awaitIdleForInstall(ctx context.Context, op *installOperation, admission InstallAdmission) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if admission.Closed() {
			return nil
		}
		if err := admission.WaitForIdle(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Detection failure or uncertainty: block progress without a
			// deadline, reevaluating on the next poll.
			return err
		}
		if admission.CloseIfQuiesced() {
			return nil
		}
		// Work raced in before closure: keep waiting. The waiter's next
		// evaluation observes it.
	}
}

// installBlockers reports the canonical active-work blockers for an
// immediate install, or nil when the runtime is observably idle. Failed or
// incomplete detection always blocks.
func (c *updateCoordinator) installBlockers(admission InstallAdmission) []errcat.Option {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	activity, err := admission.Detect(ctx)
	if err != nil {
		return []errcat.Option{errcat.WithParams(errcat.UpdateBlockedActiveWorkParams{DetectionFailed: true})}
	}
	if held, _ := admission.Held(); held > 0 || activity.Busy() {
		return []errcat.Option{errcat.WithParams(errcat.UpdateBlockedActiveWorkParams{
			Features:          activity.Features,
			ChatActive:        activity.ChatActive,
			Clones:            activity.Clones,
			Uploads:           activity.Uploads,
			OriginChecks:      activity.OriginChecks,
			RepositoryWork:    activity.RepositoryWork,
			PendingAdmissions: held,
		})}
	}
	return nil
}

// installHeldNone reports whether no admission reservations are held.
func (c *updateCoordinator) installHeldNone(admission InstallAdmission) bool {
	held, _ := admission.Held()
	return held == 0
}

// settleCancelled settles one cancelled operation: validated idempotent
// cleanup of owned resources, ownership release on success, retention on
// failure so a repeated cancellation retries.
func (c *updateCoordinator) settleCancelled(op *installOperation, cleanup func() error) {
	c.mu.Lock()
	op.cleanup = cleanup
	c.mu.Unlock()
	if err := cleanupInstallResources(op); err != nil {
		c.mu.Lock()
		if c.install == op {
			op.cleanupFailed = true
		}
		c.mu.Unlock()
		c.admissionOpen()
		return
	}
	c.clearInstallOp(op)
	c.admissionOpen()
}

// logInstallWait records one bounded wait-reevaluation without changing the
// visible state.
func (c *updateCoordinator) logInstallWait(op *installOperation, err error) {
	c.logf("install wait reevaluates: %s", selfupdate.SanitizeError(err.Error()))
}

// shutdownInstall cancels and joins an active install worker during
// ordinary shutdown. Once the operation entered its final drain it owns
// shutdown and recovery; ordinary shutdown defers to it.
func (c *updateCoordinator) shutdownInstall(ctx context.Context) {
	c.mu.Lock()
	op := c.install
	if op == nil || op.drainEntered {
		c.mu.Unlock()
		return
	}
	if !op.cancelling {
		op.cancelling = true
		op.generation++
	}
	cancel := op.cancel
	done := op.done
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
	// Leftover owned staging reconciles on the next ordinary launch through
	// the existing validated stale-staging recovery; consent is forgotten
	// because it was never durable.
}

// refreshActivity refreshes the cached advisory activity summary outside
// the coordinator mutex so slow detection never stalls snapshot reads.
func (c *updateCoordinator) refreshActivity() {
	if c.opts.Activity == nil {
		return
	}
	summary := c.activeWorkSummary()
	c.mu.Lock()
	c.activitySummary = summary
	c.mu.Unlock()
}

// activeWorkSummary builds the published active-work summary from the
// injected activity probe. A nil probe serves the static empty summary
// (runtimes without the boundary), and failed detection reports
// detection_failed with the counts it could not complete.
func (c *updateCoordinator) activeWorkSummary() UpdateActiveWorkSummary {
	summary := UpdateActiveWorkSummary{}
	if c.opts.Activity == nil {
		return summary
	}
	activity, detectionFailed, pending := c.opts.Activity(context.Background())
	summary.FeatureCount = activity.Features
	summary.ChatActive = activity.ChatActive
	summary.CloneCount = activity.Clones
	summary.UploadCount = activity.Uploads
	summary.OriginCheckCount = activity.OriginChecks
	summary.PendingAdmissions = pending
	summary.DetectionFailed = detectionFailed
	// parked_count stays zero and quiescing_since stays absent in this
	// phase: no checkpoint parking or quiescing exists.
	return summary
}

// updatesDisabledRemediation is the disabled-policy remediation hint shared
// by every update mutation refusal.
func updatesDisabledRemediation() errcat.Option {
	return errcat.WithRemediationHint(
		"Updates are disabled by this server's startup policy (--updates=off or server.updates.policy: off). Restart the server with updates enabled to use update mutations.")
}

// requireUpdatesMutationRefusal writes the shared refusal for update
// mutations when updates are disabled by policy: 403 forbidden with the
// disabled-policy remediation. It returns whether the response was written.
func (h *apiHandler) refuseUpdatesMutationDisabled(w http.ResponseWriter) bool {
	if h.updates == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return true
	}
	if h.updates.options().Policy != selfupdate.PolicyOff {
		return false
	}
	writeAPIError(w, http.StatusForbidden, errcat.Forbidden,
		errcat.WithDiagnostics("updates are disabled by this server's startup policy"),
		updatesDisabledRemediation())
	return true
}

// refuseUpdatesMutationUnsupported writes the ineligible-install refusal
// shared by install mutations.
func (h *apiHandler) refuseUpdatesMutationUnsupported(w http.ResponseWriter) bool {
	opts := h.updates.options()
	if opts.Eligibility.Supported {
		return false
	}
	writeAPIError(w, http.StatusConflict, errcat.UpdateUnsupportedInstall,
		errcat.WithParams(errcat.UpdateUnsupportedInstallParams{
			Reason:      string(opts.Eligibility.Reason),
			Remediation: opts.Eligibility.Remediation,
		}),
		errcat.WithRemediationHint(opts.Eligibility.Remediation))
	return true
}

// installRouteMaxLength bounds the explicit version selector.
const installRouteMaxLength = 64

// handleUpdateInstallRoute serves POST (install) and DELETE (cancel) on
// /api/v1/update/install.
func (h *apiHandler) handleUpdateInstallRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.handleUpdateInstallPost(w, r)
	case http.MethodDelete:
		h.handleUpdateInstallDelete(w, r)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
	}
}

// handleUpdateInstallPost accepts one consented install request. The
// accepted operation belongs to the runtime: the 202 returns promptly and
// a disconnecting caller never cancels accepted work.
func (h *apiHandler) handleUpdateInstallPost(w http.ResponseWriter, r *http.Request) {
	if h.updates == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req UpdateInstallRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if !req.Consent {
		writeAPIError(w, http.StatusBadRequest, errcat.UpdateConsentRequired,
			errcat.WithDiagnostics("installing a release requires explicit consent: true"))
		return
	}
	when := string(req.When)
	if when != updateInstallWhenNow && when != updateInstallWhenIdle {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("when must be \"now\" or \"idle\""))
		return
	}
	stopActiveWork := false
	if req.StopActiveWork != nil {
		stopActiveWork = *req.StopActiveWork
		if when != updateInstallWhenNow {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("stop_active_work is valid only with when: now"))
			return
		}
	}
	version := ""
	if req.Version != nil {
		version = strings.TrimSpace(*req.Version)
		if version == "" || len(version) > installRouteMaxLength {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("version must be a non-empty bounded release version"))
			return
		}
	}
	if h.refuseUpdatesMutationDisabled(w) {
		return
	}
	if h.refuseUpdatesMutationUnsupported(w) {
		return
	}
	opts := h.updates.options()
	if opts.Feed == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	if opts.Stager == nil || opts.Install == nil || opts.Admission == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable,
			errcat.WithDiagnostics("this runtime cannot install updates"))
		return
	}
	// An immediate install refuses active or uncertain work before staging;
	// stop permission is accepted but never stops work in this phase.
	if when == updateInstallWhenNow {
		if blockers := h.installRequestBlockers(r.Context()); blockers != nil {
			writeAPIError(w, http.StatusConflict, errcat.UpdateBlockedActiveWork, blockers...)
			return
		}
	}
	responseDone := make(chan struct{})
	_, refusal := h.updates.requestInstall(installRequest{
		consent:        req.Consent,
		when:           when,
		stopActiveWork: stopActiveWork,
		version:        version,
	}, responseDone)
	if refusal != nil {
		writeAPIError(w, refusal.status, refusal.code, refusal.opts...)
		return
	}
	// The accepted 202 completes before any shutdown the operation may
	// later begin: the response write closes the coordination channel.
	h.writeUpdateSnapshot(w, r, http.StatusAccepted)
	close(responseDone)
}

// installRequestBlockers reports the request-time active-work blockers for
// an immediate install, or nil when the runtime is observably idle.
func (h *apiHandler) installRequestBlockers(ctx context.Context) []errcat.Option {
	activity, detectionFailed, detectionErr := h.admissionActivitySnapshot(ctx)
	pending := h.admissionPendingCount()
	if detectionFailed || detectionErr != nil {
		return []errcat.Option{errcat.WithParams(errcat.UpdateBlockedActiveWorkParams{DetectionFailed: true})}
	}
	if pending > 0 || activity.Busy() {
		return []errcat.Option{errcat.WithParams(errcat.UpdateBlockedActiveWorkParams{
			Features:          activity.Features,
			ChatActive:        activity.ChatActive,
			Clones:            activity.Clones,
			Uploads:           activity.Uploads,
			OriginChecks:      activity.OriginChecks,
			RepositoryWork:    activity.RepositoryWork,
			PendingAdmissions: pending,
		})}
	}
	return nil
}

// handleUpdateInstallDelete cancels the accepted operation: it is the sole
// cancellation and cleanup owner before final drain, idempotent when no
// operation exists, and a cleanup-retry channel after a failed cleanup.
func (h *apiHandler) handleUpdateInstallDelete(w http.ResponseWriter, r *http.Request) {
	if h.updates == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	// Cancel documents and requires an empty JSON object like every other
	// bodyless mutation.
	var body struct{}
	if !decodeMutationJSON(w, r, &body) {
		return
	}
	if h.refuseUpdatesMutationDisabled(w) {
		return
	}
	result, refusal := h.updates.cancelInstall(r.Context())
	switch result {
	case cancelNone, cancelCompleted:
		h.writeUpdateSnapshot(w, r, http.StatusOK)
		return
	case cancelDrainRefused, cancelTimedOut, cancelCleanupFailed:
		writeAPIError(w, refusal.status, refusal.code, refusal.opts...)
		return
	default:
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError)
		return
	}
}
