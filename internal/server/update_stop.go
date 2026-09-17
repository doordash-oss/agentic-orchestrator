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
	"sort"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// updateStopWorkBudget bounds the authorized stopping interval: stop
// dispatch and completion confirmation share one deadline that starts
// immediately before the first dispatch. Individual calls, multiple
// sessions, and repeated observations never reset it.
const updateStopWorkBudget = 10 * time.Second

// installStopConfirmPoll paces confirmation observations while previously
// admitted work settles.
const installStopConfirmPoll = 200 * time.Millisecond

// InstallStopper performs the authorized interruption of feature and chat
// work for an immediate install. The handler implements it over the same
// mutation surface the REST pause-stop and chat-end actions use; tests
// inject deterministic fakes through UpdateOptions.Stopper.
type InstallStopper interface {
	// StoppableFeatures lists feature identities matching the enabled
	// pause-stop projection (running or need-user-input features and active
	// children), deepest children first so parent/child relationship guards
	// pass. A listing error means detection failed.
	StoppableFeatures(ctx context.Context) ([]string, error)
	// ChatActive reports whether the singleton chat session is active under
	// the current active-chat semantics.
	ChatActive() bool
	// StopFeature interrupts one feature through the guarded mutation,
	// preserving the usual interrupted-state and pending-question/permission
	// cleanup. An error aborts the installation.
	StopFeature(ctx context.Context, featureID string) error
	// EndChat ends the singleton chat session; idempotent when it is not
	// active. An error aborts the installation.
	EndChat(ctx context.Context) error
}

// handlerInstallStopper is the production InstallStopper: the pause-stop
// projection over the feature store, current active-chat semantics over the
// session manager, and dispatch through the trusted mutation surface.
type handlerInstallStopper struct {
	handler *apiHandler
}

// StoppableFeatures walks the same projection that enables the pause-stop
// action and counts detector activity: running or need-user-input features,
// skipping children whose relationship is closed. Children are ordered
// before their parents (deepest first) so stopping them satisfies the
// parent/child relationship guards.
func (s handlerInstallStopper) StoppableFeatures(context.Context) ([]string, error) {
	h := s.handler
	if h.features == nil {
		return nil, nil
	}
	features, err := h.features.List()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*feature.Feature, len(features))
	for _, f := range features {
		if f != nil {
			byID[f.ID] = f
		}
	}
	depth := func(f *feature.Feature) int {
		depth := 0
		for f != nil && f.IsChild() {
			depth++
			f = byID[f.Parent.ParentID]
		}
		return depth
	}
	type stopTarget struct {
		id    string
		depth int
	}
	var targets []stopTarget
	for _, f := range features {
		if f == nil {
			continue
		}
		if f.IsChild() && !f.IsActiveChild() {
			// A closed relationship hands the child to automatic
			// reconciliation; that work is covered by its reservations.
			continue
		}
		if f.Status.IsRunning() || f.Status == feature.StatusNeedUserInput {
			targets = append(targets, stopTarget{id: f.ID, depth: depth(f)})
		}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		return targets[i].depth > targets[j].depth
	})
	ids := make([]string, len(targets))
	for i, target := range targets {
		ids[i] = target.id
	}
	return ids, nil
}

// ChatActive mirrors the chat activity detector: the singleton session is
// active from launch through registration and while it parks between turns.
func (s handlerInstallStopper) ChatActive() bool {
	h := s.handler
	if h.sessions == nil {
		return false
	}
	sess := h.sessions.GetSession(ChatSessionID)
	return sess != nil && sess.IsActive()
}

// StopFeature dispatches the same guarded interruption the REST pause-stop
// action performs.
func (s handlerInstallStopper) StopFeature(_ context.Context, featureID string) error {
	if s.handler.mutations == nil {
		return fmt.Errorf("no stop surface available for feature %s", featureID)
	}
	_, err := s.handler.mutations.StopFeature(featureID)
	return err
}

// EndChat dispatches the same singleton chat-end mutation the REST action
// performs. The stopper only dispatches while it owns closed admission, so
// no newly created chat session can reuse the identity underneath it.
func (s handlerInstallStopper) EndChat(_ context.Context) error {
	if s.handler.mutations == nil {
		return fmt.Errorf("no stop surface available for the chat session")
	}
	_, err := s.handler.mutations.EndChat()
	return err
}

// installStopFailure is one authorized-stopping failure: the sanitized
// result context and the canonical blocker options for the public error.
type installStopFailure struct {
	result string
	opts   []errcat.Option
}

// admissionDetectBudget bounds one blocker observation's activity detection.
const admissionDetectBudget = 5 * time.Second

// detectAdmissionActivity observes the work state through the admission
// boundary within the shared detection budget. ok is false when detection
// failed: uncertainty never reads as zero activity.
func detectAdmissionActivity(admission InstallAdmission) (activity workadmission.Activity, held int, perCategory map[workadmission.Category]int, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), admissionDetectBudget)
	defer cancel()
	activity, err := admission.Detect(ctx)
	if err != nil {
		return workadmission.Activity{}, 0, nil, false
	}
	held, perCategory = admission.Held()
	return activity, held, perCategory, true
}

// detectionFailedBlockers reports the canonical blockers when activity
// detection failed or is incomplete: uncertainty never reads as zero
// activity.
func detectionFailedBlockers() []errcat.Option {
	return []errcat.Option{errcat.WithParams(errcat.UpdateBlockedActiveWorkParams{DetectionFailed: true})}
}

// admissionRaceBlockers reports the canonical blockers when an atomic
// admission decision lost a race the observation cannot see — a reservation
// won the closure race or was held under the update lock yet settled before
// detection: one pending admission is the truthful minimal snapshot.
func admissionRaceBlockers() []errcat.Option {
	return []errcat.Option{errcat.WithParams(errcat.UpdateBlockedActiveWorkParams{PendingAdmissions: 1})}
}

// activeWorkParams renders one observed work state as the canonical
// update_blocked_active_work params snapshot.
func activeWorkParams(activity workadmission.Activity, pending int) errcat.UpdateBlockedActiveWorkParams {
	return errcat.UpdateBlockedActiveWorkParams{
		Features:          activity.Features,
		ChatActive:        activity.ChatActive,
		Clones:            activity.Clones,
		Uploads:           activity.Uploads,
		OriginChecks:      activity.OriginChecks,
		RepositoryWork:    activity.RepositoryWork,
		PendingAdmissions: pending,
	}
}

// blockedActiveWorkOptions renders the canonical update_blocked_active_work
// blocker options for one observed work state, or nil when the observation
// does not block. Without stop permission any observed activity or held
// reservation blocks with the full activity snapshot. With stop permission
// only protected repository activity blocks with the activity snapshot, and
// held reservations outside the stoppable feature/chat categories block
// with the pending-admission count and the protected category names;
// feature and chat activity alone never blocks — the accepted permission
// authorizes stopping exactly that work.
func blockedActiveWorkOptions(activity workadmission.Activity, held int, perCategory map[workadmission.Category]int, stopPermitted bool) []errcat.Option {
	if !stopPermitted {
		if held > 0 || activity.Busy() {
			return []errcat.Option{errcat.WithParams(activeWorkParams(activity, held))}
		}
		return nil
	}
	if activity.ProtectedBusy() {
		return []errcat.Option{errcat.WithParams(activeWorkParams(activity, 0))}
	}
	if held > 0 {
		if cats := protectedHeldCategories(perCategory); cats != "" {
			return []errcat.Option{
				errcat.WithParams(errcat.UpdateBlockedActiveWorkParams{PendingAdmissions: held}),
				errcat.WithDiagnostics("pending admissions in protected or unknown categories: " + cats),
			}
		}
	}
	return nil
}

// installStopBlockers reports the blockers an explicit-stop immediate
// install refuses on: repository activity (clones, uploads, origin checks,
// other repository work), reservations outside the stoppable feature/chat
// categories — known protected categories and unknown categories alike —
// and failed or incomplete detection. Feature and chat activity alone
// never blocks: the accepted permission authorizes stopping it.
func (c *updateCoordinator) installStopBlockers(admission InstallAdmission) []errcat.Option {
	activity, held, perCategory, ok := detectAdmissionActivity(admission)
	if !ok {
		return detectionFailedBlockers()
	}
	return blockedActiveWorkOptions(activity, held, perCategory, true)
}

// stoppableAdmissionCategories are the only reservation categories an
// explicit-stop install may proceed past: their work is authorized to be
// interrupted, and it settles through its own stop and completion paths
// while admission is closed.
var stoppableAdmissionCategories = []workadmission.Category{
	workadmission.CategoryFeature,
	workadmission.CategoryChat,
}

// protectedHeldCategories describes held reservation categories outside the
// stoppable set, or empty when only stoppable categories are held. Category
// labels are internal tokens, safe for sanitized diagnostics.
func protectedHeldCategories(perCategory map[workadmission.Category]int) string {
	stoppable := make(map[workadmission.Category]bool, len(stoppableAdmissionCategories))
	for _, cat := range stoppableAdmissionCategories {
		stoppable[cat] = true
	}
	var parts []string
	for cat, n := range perCategory {
		if n > 0 && !stoppable[cat] {
			parts = append(parts, fmt.Sprintf("%s=%d", cat, n))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// stopTimeoutFailure is the shared deadline-expired outcome: the sanitized
// timeout context plus the remaining blocker snapshot (or detection
// uncertainty when detection itself fails).
func (c *updateCoordinator) stopTimeoutFailure(admission InstallAdmission) *installStopFailure {
	return &installStopFailure{
		result: "install_stop_failed:timeout",
		opts: append(c.stopBlockersNow(admission),
			errcat.WithDiagnostics("the stop deadline expired before all authorized work settled")),
	}
}

// stopBlockersNow snapshots the remaining blockers for a stop failure's
// canonical params: current activity counts, held reservations, or
// detection uncertainty when detection itself fails. The snapshot renders
// even a quiet observation — the zero counts are the truthful answer to
// what remains.
func (c *updateCoordinator) stopBlockersNow(admission InstallAdmission) []errcat.Option {
	activity, held, _, ok := detectAdmissionActivity(admission)
	if !ok {
		return detectionFailedBlockers()
	}
	if blockers := blockedActiveWorkOptions(activity, held, nil, false); blockers != nil {
		return blockers
	}
	return []errcat.Option{errcat.WithParams(activeWorkParams(activity, held))}
}

// dispatchAndConfirmStops runs the authorized stopping interval for one
// immediate install that already owns closed admission. One deadline —
// starting immediately before the first dispatch — bounds every dispatch
// and every confirmation observation; nothing resets it. Dispatches are
// synchronous and individually bounded by the existing session termination
// semantics, so no goroutine is ever abandoned mid-stop. Confirmation
// checks actual session completion through the activity detectors and the
// full admission reservation count: a feature reporting interrupted, a
// successful stop return, or a closed gate is insufficient by itself.
// Previously admitted work that becomes visible during stopping is
// accounted for by dispatching stops for it within the same budget;
// reservations in categories that cannot be stopped block the install.
func (c *updateCoordinator) dispatchAndConfirmStops(op *installOperation, admission InstallAdmission, stopper InstallStopper) *installStopFailure {
	budget := c.opts.StopWorkTimeout
	if budget <= 0 {
		budget = updateStopWorkBudget
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	stoppedFeatures := make(map[string]bool)
	chatStopped := false
	for {
		if err := ctx.Err(); err != nil {
			return c.stopTimeoutFailure(admission)
		}
		ids, err := stopper.StoppableFeatures(ctx)
		if err != nil {
			return &installStopFailure{
				result: "install_stop_failed:detection",
				opts: append(detectionFailedBlockers(),
					errcat.WithDiagnostics("stop-set discovery failed: "+selfupdate.SanitizeError(err.Error()))),
			}
		}
		for _, id := range ids {
			if stoppedFeatures[id] {
				continue
			}
			if ctx.Err() != nil {
				return c.stopTimeoutFailure(admission)
			}
			if hook := c.opts.StopFeatureHook; hook != nil {
				// Test-only failure injection in front of the real guarded
				// dispatch; production leaves the hook nil.
				if err := hook(id); err != nil {
					return &installStopFailure{
						result: "install_stop_failed:stop",
						opts: append(c.stopBlockersNow(admission),
							errcat.WithDiagnostics("stopping feature failed: "+selfupdate.SanitizeError(err.Error()))),
					}
				}
			}
			if err := stopper.StopFeature(ctx, id); err != nil {
				return &installStopFailure{
					result: "install_stop_failed:stop",
					opts: append(c.stopBlockersNow(admission),
						errcat.WithDiagnostics("stopping feature failed: "+selfupdate.SanitizeError(err.Error()))),
				}
			}
			stoppedFeatures[id] = true
		}
		if stopper.ChatActive() && !chatStopped {
			if ctx.Err() != nil {
				return c.stopTimeoutFailure(admission)
			}
			if err := stopper.EndChat(ctx); err != nil {
				return &installStopFailure{
					result: "install_stop_failed:stop",
					opts: append(c.stopBlockersNow(admission),
						errcat.WithDiagnostics("ending the chat session failed: "+selfupdate.SanitizeError(err.Error()))),
				}
			}
			chatStopped = true
		}

		// Confirmation: observed activity and every admission reservation
		// must be settled, including provider handshakes, setup,
		// finalization, and phase-advance tails.
		activity, detectErr := admission.Detect(ctx)
		if detectErr != nil {
			return &installStopFailure{
				result: "install_stop_failed:detection",
				opts: append(detectionFailedBlockers(),
					errcat.WithDiagnostics("completion detection failed: "+selfupdate.SanitizeError(detectErr.Error()))),
			}
		}
		held, perCategory := admission.Held()
		if held > 0 {
			if cats := protectedHeldCategories(perCategory); cats != "" {
				// Work that cannot be identified as stoppable feature or
				// chat work blocks installation even though a stop may
				// already have succeeded.
				return &installStopFailure{
					result: "install_stop_failed:unidentified_work",
					opts: []errcat.Option{
						errcat.WithParams(errcat.UpdateBlockedActiveWorkParams{PendingAdmissions: held}),
						errcat.WithDiagnostics("reservations in protected or unknown categories remain: " + cats),
					},
				}
			}
		}
		if !activity.Busy() && held == 0 {
			if ctx.Err() != nil {
				// The budget expired while the last dispatch or
				// observation was settling: confirmation did not happen
				// inside the deadline, so the attempt times out.
				return c.stopTimeoutFailure(admission)
			}
			return nil
		}
		// Previously admitted work is still settling; observe again within
		// the same budget. Repeated observations never extend it.
		select {
		case <-ctx.Done():
		case <-time.After(installStopConfirmPoll):
		}
	}
}
