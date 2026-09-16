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

package orchestrator

import (
	"errors"
	"io/fs"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// featureAdmission is one feature's held admission reservation plus the
// count of scheduled-but-not-yet-launched asynchronous continuations. The
// reservation is acquired before any work launches (phase dispatch or
// background setup) and released only when the feature's owned work has
// settled: no continuation is scheduled, no session of the feature is
// active, and the durable status is neither running nor finalizing. This
// covers the windows a session list or status projection alone cannot see:
// the pre-launch handshake, phase finalization, automatic phase advance,
// and child integration/publish tails.
type featureAdmission struct {
	reservation   *workadmission.Reservation
	continuations int
}

// featureAdmissions tracks per-feature admission reservations against one
// shared work-admission coordinator. A nil coordinator (tests, runtimes
// without the boundary) makes every operation a no-op that always succeeds.
type featureAdmissions struct {
	coordinator *workadmission.Coordinator

	mu       sync.Mutex
	features map[string]*featureAdmission
}

func newFeatureAdmissions(coordinator *workadmission.Coordinator) *featureAdmissions {
	if coordinator == nil {
		return nil
	}
	return &featureAdmissions{coordinator: coordinator, features: make(map[string]*featureAdmission)}
}

// launch admits one phase dispatch. When the feature already holds a
// reservation the launch is a transfer: a scheduled asynchronous
// continuation consumes its credit, and a synchronous continuation (the
// completion handler advancing within its own stack) simply keeps the
// reservation. Otherwise a fresh reservation is acquired before the phase
// starter dispatches any work; a closed boundary refuses the launch.
func (a *featureAdmissions) launch(featureID string) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if fa := a.features[featureID]; fa != nil {
		if fa.continuations > 0 {
			fa.continuations--
		}
		return nil
	}
	res, err := a.coordinator.Acquire(workadmission.CategoryFeature)
	if err != nil {
		return err
	}
	a.features[featureID] = &featureAdmission{reservation: res}
	return nil
}

// beginAsync reserves one scheduled asynchronous continuation: background
// setup, the deferred-final-review tail, or a server-dispatched restart.
// The reservation is held from before the goroutine launches, so no idle
// window exists between the scheduling decision and the continuation's own
// launch. A closed boundary refuses the scheduling.
func (a *featureAdmissions) beginAsync(featureID string) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if fa := a.features[featureID]; fa != nil {
		fa.continuations++
		return nil
	}
	res, err := a.coordinator.Acquire(workadmission.CategoryFeature)
	if err != nil {
		return err
	}
	a.features[featureID] = &featureAdmission{reservation: res, continuations: 1}
	return nil
}

// cancelAsync withdraws one scheduled asynchronous continuation that will
// never launch (its dispatch failed), then settles if quiet.
func (a *featureAdmissions) cancelAsync(featureID string, ownsWork func() bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	fa := a.features[featureID]
	if fa != nil && fa.continuations > 0 {
		fa.continuations--
	}
	quiet := fa == nil || (fa.continuations == 0 && !ownsWork())
	if quiet && fa != nil {
		delete(a.features, featureID)
	}
	a.mu.Unlock()
	if quiet && fa != nil {
		fa.reservation.Release()
	}
}

// settleIfQuiet releases the feature's reservation exactly once its owned
// work has settled: no scheduled continuation, no active session, and a
// durable status that is neither running nor finalizing. Unknown feature
// state fails closed and keeps the reservation.
func (a *featureAdmissions) settleIfQuiet(featureID string, ownsWork func() bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	fa := a.features[featureID]
	if fa == nil {
		a.mu.Unlock()
		return
	}
	if fa.continuations > 0 || ownsWork() {
		a.mu.Unlock()
		return
	}
	delete(a.features, featureID)
	a.mu.Unlock()
	fa.reservation.Release()
}

// settle releases the feature's reservation unconditionally: the terminal
// paths (delete/discard cleanup) own this decision.
func (a *featureAdmissions) settle(featureID string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	fa := a.features[featureID]
	delete(a.features, featureID)
	a.mu.Unlock()
	if fa != nil {
		fa.reservation.Release()
	}
}

// admissionLaunch admits one phase dispatch for the feature.
func (o *Orchestrator) admissionLaunch(featureID string) error {
	return o.admission.launch(featureID)
}

// admissionBeginAsync reserves one scheduled asynchronous continuation.
func (o *Orchestrator) admissionBeginAsync(featureID string) error {
	return o.admission.beginAsync(featureID)
}

// admissionOwnsWork reports whether the feature still owns observable work:
// an active session, a running durable status, or an in-flight synchronous
// phase boundary (finalizing).
func (o *Orchestrator) admissionOwnsWork(featureID string) bool {
	if o.deps.Sessions != nil {
		for _, sess := range o.deps.Sessions.FeatureSessions(featureID) {
			if sess.IsActive() {
				return true
			}
		}
	}
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The feature no longer exists (deleted): nothing left to own.
			return false
		}
		// Unknown state never releases: an unreadable feature must not
		// manufacture a false idle window.
		return true
	}
	if f == nil {
		return true
	}
	return f.Status.IsRunning() || f.IsFinalizingPhase()
}

// admissionSettleIfQuiet is the settle funnel for every completion,
// interruption, and async-tail path.
func (o *Orchestrator) admissionSettleIfQuiet(featureID string) {
	o.admission.settleIfQuiet(featureID, func() bool { return o.admissionOwnsWork(featureID) })
}

// admissionCancelAsync withdraws a scheduled continuation whose dispatch
// failed, then settles if quiet.
func (o *Orchestrator) admissionCancelAsync(featureID string) {
	o.admission.cancelAsync(featureID, func() bool { return o.admissionOwnsWork(featureID) })
}

// SetAdmissionBoundary installs the runtime work-admission boundary on an
// orchestrator constructed without one (the fx graph builds the coordinator
// outside the orchestrator's own module). It must run before the server
// starts serving; a second call replaces nothing once set.
func (o *Orchestrator) SetAdmissionBoundary(coordinator *workadmission.Coordinator) {
	if coordinator == nil || o.admission != nil {
		return
	}
	o.admission = newFeatureAdmissions(coordinator)
}

// PrepareAsyncWork reserves admission for server-owned background work
// (durable feature setup, restart dispatch) before it is scheduled. A
// closed boundary refuses the scheduling with workadmission.ClosedError.
func (o *Orchestrator) PrepareAsyncWork(featureID string) error {
	return o.admissionBeginAsync(featureID)
}

// SettleAsyncWork settles one unit of server-owned background work after
// it finishes; the release applies only when the feature owns no other
// work.
func (o *Orchestrator) SettleAsyncWork(featureID string) {
	o.admissionSettleIfQuiet(featureID)
}

// SettleFeatureWork releases the feature's admission reservation
// unconditionally; the terminal cleanup paths (delete, discard) call it
// once their state machine has settled.
func (o *Orchestrator) SettleFeatureWork(featureID string) {
	o.admission.settle(featureID)
}
