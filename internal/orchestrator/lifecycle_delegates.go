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

// Package orchestrator exposes feature-lifecycle operations through a stable
// boundary instead of allowing clients to call feature.Manager directly.
// The orchestrator is the mutation chokepoint: several delegates
// intentionally enforce relationship guards and other cross-cutting behavior
// before delegating to the underlying ports.FeatureLifecycle call. The
// transitions remain in feature.Manager; the orchestrator owns the call sites
// so observer emission, relationship guards, and cross-cutting concerns share
// one chokepoint.
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// resetPlanStatusForRoadmap resets the feature to StatusPlanReady and clears
// TotalRoadmapPhases for the roadmap "reject" path.
func (o *Orchestrator) resetPlanStatusForRoadmap(featureID string, budgetBump int) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		f.Status = feature.StatusPlanReady
		f.TotalRoadmapPhases = 0
		if f.MaxPlanIterations == 0 {
			f.MaxPlanIterations = agent.DefaultMaxPlanAttempts
		}
		f.MaxPlanIterations += budgetBump
		return nil
	})
}

// recordRoadmapRejection writes a CHANGES_REQUESTED attempt-meta plus a
// validation-feedback.md file for the latest planning attempt when the user
// rejects a roadmap. Best-effort; errors are swallowed so a retry is not
// blocked by diagnostic bookkeeping.
func (o *Orchestrator) recordRoadmapRejection(featureID, feedback string) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return
	}

	baseDir := o.stateDir()
	if baseDir == "" {
		return
	}

	roadmapDir := agent.RoadmapDir(baseDir, f)

	latestAttempt := agent.LatestCompletedPlanAttempt(roadmapDir)
	if latestAttempt <= 0 {
		return
	}
	if feedback == "" {
		feedback = "Roadmap rejected by reviewer."
	}
	_ = agent.WritePlanAttemptMeta(roadmapDir, agent.PlanAttemptMeta{
		Attempt:      latestAttempt,
		ReviewStatus: "CHANGES_REQUESTED",
	})
	feedbackPath := fmt.Sprintf("%s/attempt-%02d/validation-feedback.md", roadmapDir, latestAttempt)
	_ = os.WriteFile(feedbackPath, []byte(feedback), 0o644)
}

// (PopulateExecutionPlanForPhase / PopulateLegacyExecutionPlan removed in
// SchemaVersionCurrent = 3 — the orchestrator's startMultiRepoImplementation
// reads execution-order.yaml fresh from disk per cycle.)

// MarkDone transitions the feature to StatusDone and fires
// OnFeatureSummaryNeeded so the observe summary is refreshed at the
// terminal transition. Used by explicit mark-done paths where the feature
// reached a terminal state without the orchestrator's publish pipeline.
func (o *Orchestrator) MarkDone(featureID string) error {
	if err := o.guardedModify(featureID, mutationMarkDone, func(f *feature.Feature) error {
		return f.Transition(feature.StatusDone)
	}); err != nil {
		return err
	}
	if o.hooks.OnFeatureSummaryNeeded != nil {
		if f, err := o.deps.Lifecycle.Get(featureID); err == nil {
			o.hooks.OnFeatureSummaryNeeded(featureID, f)
		} else {
			o.hooks.OnFeatureSummaryNeeded(featureID, nil)
		}
	}
	return nil
}

// transitionTo transitions a feature to a caller-selected status while keeping
// lifecycle mutations behind the orchestrator boundary.
func (o *Orchestrator) transitionTo(featureID string, status feature.Status) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		return f.Transition(status)
	})
}

// setDesignReady transitions a feature into StatusDesignReady without
// going through Transition (the Design slot has a non-linear entry path). Used
// by the restart/resume flows for a failed Design phase.
func (o *Orchestrator) setDesignReady(featureID string) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		f.Status = feature.StatusDesignReady
		return nil
	})
}

// rewindWithRequestLocked is the lock-held rewind entry point. Callers must
// hold the relationship read lock so the guard check and the rewind execute
// atomically with any preceding mutations (e.g. pipeline upgrade).
func (o *Orchestrator) rewindWithRequestLocked(featureID string, request feature.RewindRequest) ([]feature.RewindWarning, feature.Phase, error) {
	if err := o.relationshipGuard(featureID, mutationRewind); err != nil {
		return nil, 0, err
	}
	sourceRun := o.currentRunNumber(featureID)
	warnings, effectiveTarget, err := o.deps.Lifecycle.RewindWithRequest(featureID, request)
	if err != nil {
		return warnings, effectiveTarget, err
	}
	o.fireFeatureRewoundHook(featureID, request, effectiveTarget, sourceRun)
	o.emitEventBlocking(ports.Event{Type: ports.FeatureRewound, FeatureID: featureID, Phase: effectiveTarget})
	return warnings, effectiveTarget, nil
}

// rewindWithUpgradeLocked is the upgrade-and-rewind body; callers hold the
// relationship read lock.
func (o *Orchestrator) rewindWithUpgradeLocked(featureID string, request feature.RewindRequest, upgradePipeline feature.PipelineProfile) ([]feature.RewindWarning, feature.Phase, error) {
	if err := o.relationshipGuard(featureID, mutationRewind); err != nil {
		return nil, 0, err
	}
	if upgradePipeline != "" {
		if err := o.deps.Lifecycle.UpgradePipeline(featureID, upgradePipeline); err != nil {
			return nil, 0, err
		}
	}
	o.stopFeatureSessions(featureID)
	return o.rewindWithRequestLocked(featureID, request)
}

func (o *Orchestrator) currentRunNumber(featureID string) int {
	if o == nil || o.deps.Store == nil {
		return 0
	}
	f, err := o.deps.Store.Load(featureID)
	if err != nil || f == nil {
		return 0
	}
	return f.ActiveRun
}

func (o *Orchestrator) fireFeatureRewoundHook(featureID string, request feature.RewindRequest, effectiveTarget feature.Phase, sourceRun int) {
	if o == nil || o.hooks.OnFeatureRewound == nil {
		return
	}
	newRun := 0
	if o.deps.Store != nil {
		if f, err := o.deps.Store.Load(featureID); err == nil && f != nil {
			newRun = f.ActiveRun
		}
	}
	o.hooks.OnFeatureRewound(featureID, request, effectiveTarget, sourceRun, newRun)
}

// CleanWorktree delegates to Lifecycle.CleanWorktree for clean-worktree
// actions. The relationship guard ensures cleanup is never performed on an
// active child or while a parent has an active child.
func (o *Orchestrator) CleanWorktree(featureID string) error {
	o.relationshipMu.RLock()
	defer o.relationshipMu.RUnlock()
	if err := o.relationshipGuard(featureID, mutationCleanup); err != nil {
		return err
	}
	return o.deps.Lifecycle.CleanWorktree(featureID)
}

// MergeFeatureLocal commits any uncommitted changes in each repo's worktree
// and merges the feature branch into its base branch locally, then marks the
// feature Done unless it already is. Errors identify the affected repository
// so clients can present a useful diagnostic.
func (o *Orchestrator) MergeFeatureLocal(featureID string) error {
	o.relationshipMu.RLock()
	defer o.relationshipMu.RUnlock()
	if err := o.relationshipGuard(featureID, mutationMerge); err != nil {
		return err
	}
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	if f.IsPublishable() {
		return fmt.Errorf("merge-local is only for non-publishable features")
	}

	for i := range f.Repos {
		repo := &f.Repos[i]
		repoPath := repo.WorktreePath
		if repoPath == "" {
			repoPath = repo.Path
		}
		branch := repo.Branch
		if branch == "" {
			branch = "feature/" + f.Slug
		}
		baseBranch := repo.BaseBranch
		if baseBranch == "" {
			baseBranch = git.DefaultBranch(repo.Path)
		}

		// Commit any uncommitted changes before merging.
		if git.HasUncommittedChanges(repoPath) {
			if err := git.CommitAll(repoPath, "Final changes before merge"); err != nil {
				return fmt.Errorf("%s: commit failed: %w", repo.Name, err)
			}
		}

		if err := git.MergeFeatureBranch(repo.Path, branch, baseBranch); err != nil {
			return fmt.Errorf("%s: %w", repo.Name, err)
		}
	}

	// A feature merged again after reaching Done has nothing left to transition.
	if f.Status == feature.StatusDone {
		return nil
	}
	return o.deps.Lifecycle.MarkDone(featureID)
}

// UpdateFeatureConfigInput carries the editable per-feature config axes
// for Orchestrator.UpdateFeatureConfig. All fields are always
// populated — callers build the input from the current feature snapshot
// plus their edits, so a zero-value field is a real "set this to empty"
// operation, not "leave alone".
type UpdateFeatureConfigInput struct {
	Models             config.ModelConfig
	Effort             config.EffortConfig
	Inquireness        feature.Inquireness
	Checkpoints        feature.Checkpoints
	InputNotifications feature.InputNotificationsMode
	// AutomaticReviewMode nil keeps the feature's current mode.
	AutomaticReviewMode *feature.AutomaticReviewMode
	// Pipeline is the submitted pipeline; a paired update requires it to
	// match the addressed record's pipeline.
	Pipeline feature.PipelineProfile
}

// UpdateFeatureConfig writes the editable config axes and returns the
// updated feature. Under the relationship read lock it routes a parent with
// an active child, or the active child itself, through the paired update and
// every other feature through the single-record update, so a concurrent
// child creation cannot interleave between detection and the write.
func (o *Orchestrator) UpdateFeatureConfig(featureID string, input UpdateFeatureConfigInput) (*feature.Feature, error) {
	unlock := o.lockRelationshipRead()
	defer unlock()
	return o.updateFeatureConfigLocked(featureID, input)
}

func (o *Orchestrator) updateFeatureConfigLocked(featureID string, input UpdateFeatureConfigInput) (*feature.Feature, error) {
	var mode feature.AutomaticReviewMode
	if input.AutomaticReviewMode != nil {
		mode = *input.AutomaticReviewMode
	} else {
		current, err := o.deps.Store.Load(featureID)
		if err != nil {
			return nil, err
		}
		mode = feature.NormalizeAutomaticReviewMode(current.AutomaticReviewMode)
	}
	parentID, _, paired, err := o.detectPairedConfigTarget(featureID)
	if err != nil {
		return nil, fmt.Errorf("detecting paired config target: %w", err)
	}
	if paired {
		err = o.updatePairedFeatureConfig(parentID, feature.PairedConfigInput{
			Models:              input.Models,
			Effort:              input.Effort,
			Inquireness:         input.Inquireness,
			Checkpoints:         input.Checkpoints,
			InputNotifications:  input.InputNotifications,
			AutomaticReviewMode: mode,
		}, input.Pipeline, featureID)
	} else {
		err = o.updateFeatureConfigRecord(featureID, input, mode)
	}
	if err != nil {
		return nil, err
	}
	return o.deps.Store.Load(featureID)
}

// updateFeatureConfigRecord atomically writes the editable config axes.
// Store.Modify handles locking + atomic
// write. On success, emits ports.Event{Type: FeatureConfigChanged}
// (non-blocking) and fires hooks.OnFeatureConfigChanged(before, after) so the
// observer writes a feature.config_changed audit entry.
//
// Re-entrancy: A second call with identical inputs against an already-
// updated feature re-writes the same editable fields to the same values, emits a
// second audit + event, and returns nil. This is acceptable — the audit trail
// explicitly records "no semantic change" via before == after.
// Crash recovery: Store.Modify performs an atomic unique-temp + rename,
// so a crash before rename leaves feature.yaml untouched; a crash after
// rename leaves the new values on disk and the hook+event are simply not
// fired — the next startup reads the persisted values as source of truth.
func (o *Orchestrator) updateFeatureConfigRecord(featureID string, input UpdateFeatureConfigInput, mode feature.AutomaticReviewMode) error {
	var before, after feature.ConfigSnapshot
	err := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		if f.IsChild() && !f.IsActiveChild() {
			return feature.ErrChildRelationshipClosed
		}
		before = feature.ConfigSnapshot{
			Models:              f.Models,
			Effort:              f.Effort,
			Inquireness:         f.Inquireness,
			Checkpoints:         f.Checkpoints,
			InputNotifications:  feature.NormalizeInputNotificationsMode(f.InputNotifications),
			AutomaticReviewMode: feature.NormalizeAutomaticReviewMode(f.AutomaticReviewMode),
		}
		f.Models = input.Models
		f.Effort = input.Effort
		f.Inquireness = input.Inquireness
		f.Checkpoints = f.Pipeline.NormalizeCheckpoints(input.Checkpoints, f.IsPublishable())
		f.InputNotifications = feature.PersistInputNotificationsMode(input.InputNotifications)
		f.AutomaticReviewMode = feature.PersistAutomaticReviewMode(mode)
		after = feature.ConfigSnapshot{
			Models:              f.Models,
			Effort:              f.Effort,
			Inquireness:         f.Inquireness,
			Checkpoints:         f.Checkpoints,
			InputNotifications:  feature.NormalizeInputNotificationsMode(f.InputNotifications),
			AutomaticReviewMode: feature.NormalizeAutomaticReviewMode(f.AutomaticReviewMode),
		}
		return nil
	})
	if err != nil {
		return err
	}
	if o.hooks.OnFeatureConfigChanged != nil {
		o.hooks.OnFeatureConfigChanged(featureID, before, after)
	}
	o.emitEvent(ports.Event{Type: ports.FeatureConfigChanged, FeatureID: featureID})
	return nil
}

func enterReviewGateFeatureState(f *feature.Feature, targetPhase feature.Phase) {
	reviewStatus := feature.NeedsReviewForPhase(targetPhase)
	tp := targetPhase
	f.Status = reviewStatus
	f.PendingReviewPhase = &tp
	f.IsRewind = false
	clearPendingFeatureAttention(f)
}

func clearPendingFeatureAttention(f *feature.Feature) {
	if f == nil {
		return
	}
	for i := range f.HelpQueue {
		if f.HelpQueue[i].Pending {
			f.HelpQueue[i].Pending = false
		}
	}
	for i := range f.PermissionsQueue {
		if f.PermissionsQueue[i].Pending {
			f.PermissionsQueue[i].Pending = false
		}
	}
}

// extendFailedPhaseBudget clears failure bookkeeping on a failed feature and
// bumps iteration budgets by caller-supplied deltas. The caller reads configured
// defaults and passes them in so the orchestrator boundary stays free of config
// plumbing. Deltas <= 0 are ignored.
func (o *Orchestrator) extendFailedPhaseBudget(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		if f.Status != feature.StatusFailed {
			return nil
		}
		if f.FailureCode() == errcat.IterationBudgetExhausted && maxIterationsDelta > 0 {
			f.MaxIterations += maxIterationsDelta
		}
		if f.CurrentPhase == feature.PhasePlan && maxPlanIterationsDelta > 0 {
			f.MaxPlanIterations += maxPlanIterationsDelta
		}
		f.Run().Failure = nil
		return nil
	})
}

// ErrFeatureBusy is returned by RestartFeature when the feature still has at
// least one active session. The typical trigger is the user pressing "r"
// while a stop is still draining sessions: the feature is already at
// StatusInterrupted (set at the head of interruptFeature) but agents are
// still being SIGTERM'd. Without this guard, the restart would call
// stopFeatureSessions alongside the stop loop, dispatch a fresh KB cycle
// the user didn't request, and chain further "r" presses kill the new
// sessions and start more, etc. Callers should treat this as a soft failure
// and surface a "wait and retry" hint rather than marking the feature
// failed.
var ErrFeatureBusy = errors.New("feature has active sessions; wait for them to finish before restarting")

// RestartAction enumerates the follow-up outcomes of a restart transition.
type RestartAction int

const (
	// RestartNoOp means the orchestrator transitioned state with no follow-up.
	RestartNoOp RestartAction = iota

	// RestartDispatchPhase means RestartFeature starts Outcome.Phase.
	RestartDispatchPhase
)

// RestartOutcome describes the follow-up the restart transition requires after
// applying its state transitions.
type RestartOutcome struct {
	Action RestartAction
	Phase  feature.Phase // meaningful only for RestartDispatchPhase
}

// restartPhaseLocked is the restart transition behind RestartFeature;
// callers hold the relationship read lock.
//   - Stops any active sessions (delegates to stopFeatureSessions).
//   - On Failed features, clears the failure bookkeeping and extends the
//     iteration budget by caller-supplied deltas.
//   - Otherwise, walks the phase+status decision tree to transition the
//     feature back to a startable status and returns RestartDispatchPhase with
//     the phase to relaunch.
//
// maxIterationsDelta / maxPlanIterationsDelta are the bumps used when the
// run's failure code is iteration_budget_exhausted (or the phase is Plan).
// Pass 0 to skip the bump.
func (o *Orchestrator) restartPhaseLocked(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) (RestartOutcome, error) {
	// The relationship guard rejects parent restart while a child is active.
	// For children, checkChildExecution enforces active relationship and
	// execution capability before any state changes.
	if err := o.relationshipGuard(featureID, mutationRestart); err != nil {
		return RestartOutcome{}, err
	}
	// Refuse if any session for this feature is still active. This catches the
	// "user spammed 'r' during a stop" case: interruptFeature is mid-loop,
	// sessions are still draining, but the feature already shows
	// StatusInterrupted because Transition runs at the head of interruptFeature.
	// Without the guard we'd race the stop loop and dispatch a fresh phase the
	// user never asked for.
	if o.deps.Sessions != nil {
		for _, s := range o.deps.Sessions.FeatureSessions(featureID) {
			if s != nil && s.IsActive() && !isArtifactReviewSession(s) {
				return RestartOutcome{}, ErrFeatureBusy
			}
		}
	}

	if err := o.checkChildExecution(featureID); err != nil {
		return RestartOutcome{}, err
	}
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return RestartOutcome{}, fmt.Errorf("load feature: %w", err)
	}

	// Stop any active sessions before mutating state so orphaned agents do not
	// race subsequent Store.Modify writes.
	o.stopFeatureSessions(featureID)

	// An active child with resumable integration state replays the integration
	// boundary — never Plan, Implement, or an already-approved Final Review.
	// Closed cleanup tails are owned exclusively by automatic reconciliation.
	// A nil transaction at ReviewPassed@FinalReview means integration was
	// dispatched but died before its journal became durable; without this
	// route the status switch below has no arm for it and Restart is a NoOp.
	integrationCrashedPreJournal := f.IsActiveChild() && f.Parent.Transaction == nil &&
		f.Status == feature.StatusReviewPassed && f.CurrentPhase == feature.PhaseFinalReview
	if f.IntegrationResumable() || integrationCrashedPreJournal {
		if err := o.runChildIntegrationLocked(featureID); err != nil {
			return RestartOutcome{}, err
		}
		// Reload to check whether conflict resolution invalidated the
		// final-review approval and routed the child back through Final
		// Review. When the child code changed during resolution,
		// invalidateFinalReview clears the journal and sets
		// StatusReviewPassed + CurrentPhase=PhaseFinalReview. restartPhaseLocked
		// must dispatch Final Review so the pipeline reruns it without
		// replaying Plan or Implement.
		f, err = o.deps.Lifecycle.Get(featureID)
		if err != nil {
			return RestartOutcome{}, fmt.Errorf("reload after integration: %w", err)
		}
		if f.IsChild() && f.Parent.Transaction == nil &&
			f.Status == feature.StatusReviewPassed &&
			f.CurrentPhase == feature.PhaseFinalReview {
			return RestartOutcome{Action: RestartDispatchPhase, Phase: feature.PhaseFinalReview}, nil
		}
		return RestartOutcome{Action: RestartNoOp}, nil
	}

	// Clear failure context on restart; extend iteration caps if exhausted.
	// extendFailedPhaseBudget is a no-op on non-Failed features so this is
	// safe to call unconditionally, but we retain the explicit gate for clarity.
	if f.Status == feature.StatusFailed {
		_ = o.extendFailedPhaseBudget(featureID, maxIterationsDelta, maxPlanIterationsDelta)
	}
	phase := f.CurrentPhase

	if f.Status.IsNeedsReview() {
		if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			ff.Status = feature.StatusInterrupted
			ff.PendingReviewPhase = nil
			ff.PendingRewindReviewRoadmapPhase = nil
			ff.IsRewind = false
			clearPendingFeatureAttention(ff)
			return nil
		}); err != nil {
			return RestartOutcome{}, fmt.Errorf("restart review gate: %w", err)
		}
		return RestartOutcome{Action: RestartDispatchPhase, Phase: phase}, nil
	}

	// Restarting deliberately abandons an open need-user-input request: the
	// answers are discarded, the gate pointer and its attention are cleared,
	// and the phase is re-dispatched from Interrupted. (Answering the request
	// is the other, non-destructive way out.)
	if f.Status == feature.StatusNeedUserInput {
		if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			ff.Status = feature.StatusInterrupted
			ff.PendingNeedUserInputPath = ""
			clearPendingFeatureAttention(ff)
			return nil
		}); err != nil {
			return RestartOutcome{}, fmt.Errorf("restart need-user-input gate: %w", err)
		}
		return RestartOutcome{Action: RestartDispatchPhase, Phase: phase}, nil
	}

	// A crash inside the end-of-phase git boundary leaves the finalizing marker
	// persisted; Restart is the user's way out, so clear it before routing.
	if f.IsFinalizingPhase() {
		if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			if ff.IsFinalizingPhase() {
				ff.CurrentPhaseStatus = ""
			}
			return nil
		}); err != nil {
			return RestartOutcome{}, fmt.Errorf("clear finalizing marker: %w", err)
		}
	}

	// Restart the current phase: transition state to a startable status and
	// return the phase for the caller to relaunch.
	switch f.Status {
	case feature.StatusInterrupted:
		// Already in a valid state for start commands — no transition needed.
	case feature.StatusCreated:
		// Created is the canonical starting status; startKB/startInquire/etc.
		// handle the forward transition themselves. A feature can land here
		// with a non-zero CurrentPhase when an upstream wake-up path stranded
		// it (e.g. wakeKBWaiters' allFresh recursion before the startPhase
		// fix) — pressing Restart should recover it via plain re-dispatch.
	case feature.StatusFailed:
		switch phase {
		case feature.PhaseKnowledgeBase:
			if err := o.transitionTo(featureID, feature.StatusCreated); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhaseInquire:
			if err := o.transitionTo(featureID, feature.StatusInquiring); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhaseResearch:
			if err := o.transitionTo(featureID, feature.StatusResearching); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhaseDesign:
			if err := o.setDesignReady(featureID); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhasePlan:
			if err := o.transitionTo(featureID, feature.StatusResearching); err != nil {
				return RestartOutcome{}, err
			}
			if err := o.transitionTo(featureID, feature.StatusPlanReady); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhaseImplement:
			if err := o.transitionTo(featureID, feature.StatusImplementReady); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhaseReview, feature.PhaseFinalReview:
			if err := o.resetFailedFinalReviewForRestart(featureID); err != nil {
				return RestartOutcome{}, err
			}
			phase = feature.PhaseFinalReview
		case feature.PhasePublish:
			if err := o.transitionTo(featureID, feature.StatusCodeReady); err != nil {
				return RestartOutcome{}, err
			}
		default:
			return RestartOutcome{Action: RestartNoOp}, nil
		}
	default:
		switch phase {
		case feature.PhaseKnowledgeBase, feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign, feature.PhasePlan:
			if err := o.transitionTo(featureID, feature.StatusInterrupted); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhaseImplement:
			if err := o.transitionTo(featureID, feature.StatusImplementReady); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhasePublish:
			// CodeReady — allow restart to re-trigger auto-publish.
		default:
			return RestartOutcome{Action: RestartNoOp}, nil
		}
	}

	return RestartOutcome{Action: RestartDispatchPhase, Phase: phase}, nil
}

func (o *Orchestrator) resetFailedFinalReviewForRestart(featureID string) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		f.Status = feature.StatusReviewPassed
		f.CurrentPhase = feature.PhaseFinalReview
		f.Run().Failure = nil
		f.CurrentPhaseStatus = ""
		f.ReviewFixing = false
		f.ReviewingGate = false
		for _, r := range f.Repos {
			if st := f.RepoStates[r.Name]; st != nil {
				st.Error = nil
			}
		}
		clearPendingFeatureAttention(f)
		return nil
	})
}

func isArtifactReviewSession(s ports.SessionView) bool {
	if s == nil {
		return false
	}
	return strings.HasSuffix(s.ID(), "-artifact-review")
}

// lookupRoadmapPhaseName returns the friendly name of the given roadmap phase
// by parsing the roadmap artifact. Returns "" if the name cannot be resolved.
func (o *Orchestrator) lookupRoadmapPhaseName(f *feature.Feature, phase int) string {
	roadmapPath := o.resolveArtifactPath(f, "roadmap")
	if roadmapPath == "" {
		return ""
	}
	data, err := os.ReadFile(roadmapPath)
	if err != nil {
		return ""
	}
	phases, err := agent.ParseRoadmap(string(data))
	if err != nil {
		return ""
	}
	for _, p := range phases {
		if p.Number == phase {
			return p.Name
		}
	}
	return ""
}
