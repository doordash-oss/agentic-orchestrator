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
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// Orchestration operations: each exported method below owns the relationship
// lock, admission, and dispatch order for one feature mutation, so callers
// translate requests and never sequence those concerns themselves.

// Fixed budget extension the retry operation grants a feature that failed on
// an exhausted iteration budget.
const (
	retryMaxIterationsDelta     = 10
	retryMaxPlanIterationsDelta = 2
)

// ErrNoSetupWork is returned by DispatchSetup when the feature has neither
// queued nor failed durable setup.
var ErrNoSetupWork = errors.New("no pending or failed setup work")

// ErrChildCreationUnavailable is returned by LaunchChild when the lifecycle
// dependency cannot create children.
var ErrChildCreationUnavailable = errors.New("feature manager is not available")

// ErrStaleRewindPreview matches every StaleRewindPreviewError.
var ErrStaleRewindPreview = errors.New("stale rewind preview")

// StaleRewindPreviewError rejects a rewind whose preview no longer matches
// the active run or rewind-relevant state. Its text is a redacted reason
// safe to return across the API boundary.
type StaleRewindPreviewError struct {
	Reason string
}

func (e *StaleRewindPreviewError) Error() string { return e.Reason }

// Is reports ErrStaleRewindPreview as a match.
func (e *StaleRewindPreviewError) Is(target error) bool { return target == ErrStaleRewindPreview }

// StopFeature interrupts the feature inside the relationship guard window.
func (o *Orchestrator) StopFeature(featureID string) error {
	unlock := o.lockRelationshipRead()
	defer unlock()
	if err := o.relationshipGuard(featureID, mutationStop); err != nil {
		return err
	}
	o.trace(traceTransition)
	return o.interruptFeature(featureID)
}

// RestartFeature applies the restart transition and, when the outcome calls
// for it, starts the feature without releasing the relationship lock in
// between, so no child creation can interleave. On a dispatch failure the
// returned outcome still names the phase that was being dispatched.
func (o *Orchestrator) RestartFeature(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) (RestartOutcome, error) {
	startMu := o.featureStartControl(featureID)
	startMu.Lock()
	defer startMu.Unlock()
	unlock := o.lockRelationshipRead()
	defer unlock()
	return o.restartAndDispatchLocked(featureID, maxIterationsDelta, maxPlanIterationsDelta)
}

func (o *Orchestrator) restartAndDispatchLocked(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) (RestartOutcome, error) {
	outcome, err := o.restartPhaseLocked(featureID, maxIterationsDelta, maxPlanIterationsDelta)
	if err != nil {
		return RestartOutcome{}, err
	}
	o.trace(traceTransition)
	switch outcome.Action {
	case RestartNoOp:
		return outcome, nil
	case RestartDispatchPhase:
		return outcome, o.startFeatureLocked(featureID)
	default:
		return outcome, fmt.Errorf("unknown restart action %d", outcome.Action)
	}
}

// RetryFeature retries a failed feature. A failed setup reruns its
// unfinished setup tasks synchronously (starting non-child features
// afterwards); every other feature restarts its phase, with the fixed budget
// extension when the failure was an exhausted iteration budget.
func (o *Orchestrator) RetryFeature(featureID string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		f = nil
	}
	if isFailedSetupFeature(f) {
		return o.retrySetup(featureID)
	}
	maxIterationsDelta, maxPlanIterationsDelta := retryIterationDeltas(f)
	_, err = o.RestartFeature(featureID, maxIterationsDelta, maxPlanIterationsDelta)
	return err
}

func retryIterationDeltas(f *feature.Feature) (int, int) {
	if f == nil || f.Status != feature.StatusFailed || f.FailureCode() != errcat.IterationBudgetExhausted {
		return 0, 0
	}
	return retryMaxIterationsDelta, retryMaxPlanIterationsDelta
}

// DispatchSetup runs a feature's queued setup, or the unfinished tasks of a
// failed setup, in an orchestrator-owned goroutine tracked by WaitForCycles,
// without starting orchestration. Admission is reserved before the goroutine
// launches and settled when it ends; a closed boundary refuses the dispatch.
// Setup failures are durable on the feature and reported through setup
// events, so the returned error covers only the dispatch itself.
func (o *Orchestrator) DispatchSetup(featureID string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return err
	}
	retry := isFailedSetupFeature(f)
	if !retry && !isPendingSetupFeature(f) {
		return fmt.Errorf("%w: feature %q", ErrNoSetupWork, featureID)
	}
	if err := o.admissionBeginAsync(featureID); err != nil {
		return err
	}
	o.cycleWG.Add(1)
	go func() {
		defer o.cycleWG.Done()
		defer o.admissionEndAsync(featureID)
		o.trace(traceDispatch)
		_ = o.runSetupWith(retry, featureID)
	}()
	return nil
}

// isPendingSetupFeature reports whether the feature has queued durable setup
// that has not completed yet (the state Create leaves it in with QueueSetup).
func isPendingSetupFeature(f *feature.Feature) bool {
	if f == nil || f.Status != feature.StatusSettingUpWorktrees {
		return false
	}
	setup := f.Run().Setup
	return setup != nil &&
		(setup.Status == feature.SetupStatusQueued || setup.Status == feature.SetupStatusRunning)
}

func isFailedSetupFeature(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	setup := f.Run().Setup
	return f.Status == feature.StatusFailed &&
		errcat.IsSetupFailure(f.FailureCode()) &&
		setup != nil &&
		setup.Status == feature.SetupStatusFailed
}

// ChildLaunch is the kind-tagged input of LaunchChild. Kind is one of
// feature.ChildKindRefactor, feature.ChildKindReviewFeedback, or
// feature.ChildKindRebase; only the fields of that kind are read.
type ChildLaunch struct {
	Kind string
	// Refactor is the refactor child spec.
	Refactor feature.RefactorChildSpec
	// ExpectedRevision and Gate commit the parent's pending review-feedback
	// draft.
	ExpectedRevision int64
	Gate             *bool
}

// ChildLaunchResult carries the launched child and, for review feedback,
// the draft outcome counts.
type ChildLaunchResult struct {
	Child    *feature.Feature
	Changed  int
	Omitted  int
	Deferred int
	// Replayed reports a review-feedback launch answered from its durable
	// receipt; nothing was re-announced or re-dispatched.
	Replayed bool
}

// childCreator is the lifecycle capability that durably creates children.
type childCreator interface {
	CreateRefactorChild(parentID string, spec feature.RefactorChildSpec) (*feature.Feature, error)
	LaunchReviewFeedbackChildFromDraft(parentID string, expectedRevision int64, gate *bool) (*feature.ReviewFeedbackLaunchResult, error)
	CreateRebaseChild(parentID string, spec feature.RebaseChildSpec) (*feature.Feature, error)
}

// LaunchChild creates a child of parentID and dispatches its setup. Creation
// holds the relationship write lock so no guarded mutation can pass while
// the child appears; the created event and the async setup dispatch run
// after the lock is released. A rebase launch runs its preflight first to
// build the spec.
func (o *Orchestrator) LaunchChild(parentID string, launch ChildLaunch) (ChildLaunchResult, error) {
	creator, ok := o.deps.Lifecycle.(childCreator)
	if !ok {
		return ChildLaunchResult{}, ErrChildCreationUnavailable
	}
	var create func() (ChildLaunchResult, error)
	switch launch.Kind {
	case feature.ChildKindRefactor:
		create = func() (ChildLaunchResult, error) {
			child, err := creator.CreateRefactorChild(parentID, launch.Refactor)
			return ChildLaunchResult{Child: child}, err
		}
	case feature.ChildKindReviewFeedback:
		create = func() (ChildLaunchResult, error) {
			res, err := creator.LaunchReviewFeedbackChildFromDraft(parentID, launch.ExpectedRevision, launch.Gate)
			if err != nil {
				return ChildLaunchResult{}, err
			}
			return ChildLaunchResult{
				Child: res.Child, Changed: res.Changed, Omitted: res.Omitted, Deferred: res.Deferred, Replayed: res.Replayed,
			}, nil
		}
	case feature.ChildKindRebase:
		preflight, err := o.rebaseChildPreflight(parentID)
		if err != nil {
			return ChildLaunchResult{}, err
		}
		spec := feature.RebaseChildSpec{Bases: preflight.Bases, Targets: preflight.Targets, Behind: preflight.Behind}
		create = func() (ChildLaunchResult, error) {
			child, err := creator.CreateRebaseChild(parentID, spec)
			return ChildLaunchResult{Child: child}, err
		}
	default:
		return ChildLaunchResult{}, fmt.Errorf("unknown child kind %q", launch.Kind)
	}

	unlock := o.lockRelationshipWrite()
	result, err := create()
	unlock()
	if err != nil {
		return ChildLaunchResult{}, err
	}
	if result.Replayed {
		return result, nil
	}
	o.childCreated(result.Child)
	o.runSetupAsync(result.Child.ID)
	return result, nil
}

// Delete runs the durable relationship cascade and, when it completed,
// settles the feature's admission reservation; a pending cleanup keeps the
// reservation until its retry settles.
func (o *Orchestrator) Delete(featureID string) (feature.CascadeDeleteResult, error) {
	result, err := o.deleteCascade(featureID)
	if err != nil {
		return result, err
	}
	if result.Status == feature.CascadeDeleteCompleted {
		o.trace(traceSettle)
		o.admission.settle(featureID)
	}
	return result, nil
}

// EnableFeatureAutomaticReview turns automatic Bash review on for one
// feature, keeping every other config axis. It shares the config update's
// paired routing, closed-child rejection, audit hook, and event.
func (o *Orchestrator) EnableFeatureAutomaticReview(featureID string) error {
	unlock := o.lockRelationshipRead()
	defer unlock()
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return err
	}
	mode := feature.AutomaticReviewEnabled
	_, err = o.updateFeatureConfigLocked(featureID, UpdateFeatureConfigInput{
		Models:              f.Models,
		Effort:              f.Effort,
		Inquireness:         f.Inquireness,
		Checkpoints:         f.Pipeline.NormalizeCheckpoints(f.Checkpoints, f.IsPublishable()),
		InputNotifications:  feature.NormalizeInputNotificationsMode(f.InputNotifications),
		AutomaticReviewMode: &mode,
		Pipeline:            f.Pipeline,
	})
	return err
}

// RewindInput is the rewind request plus the optional pipeline upgrade and
// the preview guard inputs. An empty SourceRevision skips the guard.
type RewindInput struct {
	Request         feature.RewindRequest
	UpgradePipeline feature.PipelineProfile
	SourceRevision  string
	SourceRunNumber int
}

// RewindResult reports a rewind's warnings, effective phase, and the active
// run before and after it.
type RewindResult struct {
	Warnings        []feature.RewindWarning
	EffectivePhase  feature.Phase
	SourceRunNumber int
	NewRunNumber    int
}

// Rewind checks the preview guard, then runs the relationship guard,
// optional pipeline upgrade, session stop, and rewind, all under one
// relationship read lock. A stale preview is rejected with
// ErrStaleRewindPreview before any side effect; historical source runs are
// rejected outright. Warnings and the effective phase are returned even
// when the rewind fails.
func (o *Orchestrator) Rewind(featureID string, input RewindInput) (RewindResult, error) {
	unlock := o.lockRelationshipRead()
	defer unlock()
	current, err := o.deps.Store.Load(featureID)
	if err != nil {
		return RewindResult{}, fmt.Errorf("loading feature for rewind guard: %w", err)
	}
	if input.SourceRevision != "" {
		if input.SourceRunNumber != 0 && input.SourceRunNumber != current.ActiveRun {
			return RewindResult{}, &StaleRewindPreviewError{Reason: "active run changed since preview"}
		}
		if feature.RewindRevision(current) != input.SourceRevision {
			return RewindResult{}, &StaleRewindPreviewError{Reason: "rewind state changed since preview"}
		}
	}
	result := RewindResult{SourceRunNumber: current.ActiveRun}
	result.Warnings, result.EffectivePhase, err = o.rewindWithUpgradeLocked(featureID, input.Request, input.UpgradePipeline)
	if err != nil {
		return result, err
	}
	if updated, loadErr := o.deps.Store.Load(featureID); loadErr == nil {
		result.NewRunNumber = updated.ActiveRun
	}
	return result, nil
}

// AnswerQueuedHelp answers the feature's first pending help-queue entry with
// message and reports whether one was pending.
func (o *Orchestrator) AnswerQueuedHelp(featureID, message string) (bool, error) {
	found := false
	err := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		for i := range f.HelpQueue {
			if !f.HelpQueue[i].Pending {
				continue
			}
			f.HelpQueue[i].Answer = message
			f.HelpQueue[i].Pending = false
			found = true
			return nil
		}
		return nil
	})
	return found, err
}

// PendingNeedUserInputGatePath returns the gate artifact of the feature's
// open need-user-input request, or an error when none is open.
func (o *Orchestrator) PendingNeedUserInputGatePath(featureID string) (string, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", err
	}
	if f.PendingNeedUserInputPath == "" {
		return "", fmt.Errorf("feature %s is not paused on a need-user-input gate", featureID)
	}
	return f.PendingNeedUserInputPath, nil
}

// Steps recorded by the operation trace, in the order an operation may take
// them.
const (
	traceLockAcquired = "lock acquired"
	traceGuard        = "guard checked"
	traceAdmission    = "admission reserved"
	traceTransition   = "transition applied"
	traceDispatch     = "dispatch requested"
	traceSettle       = "admission settled"
	traceLockReleased = "lock released"
)

// trace records one operation step for the ordering tests; production
// orchestrators leave traceStep nil.
func (o *Orchestrator) trace(step string) {
	if o.traceStep != nil {
		o.traceStep(step)
	}
}

func (o *Orchestrator) lockRelationshipRead() (unlock func()) {
	o.relationshipMu.RLock()
	o.trace(traceLockAcquired)
	return func() {
		o.trace(traceLockReleased)
		o.relationshipMu.RUnlock()
	}
}

func (o *Orchestrator) lockRelationshipWrite() (unlock func()) {
	o.relationshipMu.Lock()
	o.trace(traceLockAcquired)
	return func() {
		o.trace(traceLockReleased)
		o.relationshipMu.Unlock()
	}
}
