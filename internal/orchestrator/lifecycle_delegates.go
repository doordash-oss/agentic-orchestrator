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
// boundary instead of allowing clients to call feature.Manager directly. The
// transitions remain in feature.Manager; the orchestrator owns the call sites
// so observer emission and cross-cutting concerns share one chokepoint.
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// AdvanceRoadmapPhase advances the current roadmap phase.
func (o *Orchestrator) AdvanceRoadmapPhase(featureID string) error {
	return o.deps.Lifecycle.AdvanceRoadmapPhase(featureID)
}

// StartRoadmapPhaseImplementation transitions the feature to implementing for
// the current roadmap phase.
func (o *Orchestrator) StartRoadmapPhaseImplementation(featureID string) error {
	return o.deps.Lifecycle.StartRoadmapPhaseImplementation(featureID)
}

// StartPlanning transitions the feature into StatusPlanning.
func (o *Orchestrator) StartPlanning(featureID string) error {
	return o.deps.Lifecycle.StartPlanning(featureID)
}

// CompletePlanning transitions the feature out of StatusPlanning into
// StatusImplementReady.
func (o *Orchestrator) CompletePlanning(featureID string) error {
	return o.deps.Lifecycle.CompletePlanning(featureID)
}

// CompleteImplementation transitions the feature out of StatusImplementing
// into StatusReviewPassed.
func (o *Orchestrator) CompleteImplementation(featureID string) error {
	return o.deps.Lifecycle.CompleteImplementation(featureID)
}

// MarkCodeReady transitions the feature into StatusCodeReady.
func (o *Orchestrator) MarkCodeReady(featureID string) error {
	return o.deps.Lifecycle.MarkCodeReady(featureID)
}

// NeedsPlanReview sets the StatusPlanNeedsReview gate.
func (o *Orchestrator) NeedsPlanReview(featureID string) error {
	return o.deps.Lifecycle.NeedsPlanReview(featureID)
}

// ClearAddressingReviews clears the AddressingReviews flag and resets the
// Review cycle pipeline.
func (o *Orchestrator) ClearAddressingReviews(featureID string) error {
	return o.deps.Lifecycle.ClearAddressingReviews(featureID)
}

// SetTotalRoadmapPhases persists the phase count after the first planning
// loop writes the roadmap artifact.
func (o *Orchestrator) SetTotalRoadmapPhases(featureID string, count int) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		f.TotalRoadmapPhases = count
		return nil
	})
}

// BumpPlanIterationsBudget bumps the MaxPlanIterations budget by `delta`,
// defaulting to agent.DefaultMaxPlanAttempts when unset. Used by the plan
// review "iterate" path.
func (o *Orchestrator) BumpPlanIterationsBudget(featureID string, delta int) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		if f.MaxPlanIterations == 0 {
			f.MaxPlanIterations = agent.DefaultMaxPlanAttempts
		}
		f.MaxPlanIterations += delta
		return nil
	})
}

// ResetPlanStatusForRoadmap resets the feature to StatusPlanReady and clears
// TotalRoadmapPhases for the roadmap "reject" path.
func (o *Orchestrator) ResetPlanStatusForRoadmap(featureID string, budgetBump int) error {
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

// RecordRoadmapRejection writes a CHANGES_REQUESTED attempt-meta plus a
// validation-feedback.md file for the latest planning attempt when the user
// rejects a roadmap. Best-effort; errors are swallowed so a retry is not
// blocked by diagnostic bookkeeping.
func (o *Orchestrator) RecordRoadmapRejection(featureID, feedback string) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return
	}

	baseDir := o.stateDir()
	if baseDir == "" {
		return
	}

	roadmapDir := agent.RoadmapDir(baseDir, f)
	if f.RefactorPrefix() != "" {
		roadmapDir = fmt.Sprintf("%s/%s/roadmap", agent.ActiveRunDir(baseDir, f), f.RefactorPrefix())
	}

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

// ParseRoadmapAndPersistCount parses the approved roadmap from disk and
// persists TotalRoadmapPhases. Best-effort; returns count 0 on any failure.
func (o *Orchestrator) ParseRoadmapAndPersistCount(featureID string) (int, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return 0, fmt.Errorf("load feature: %w", err)
	}
	roadmapPath := o.resolveArtifactPath(f, "roadmap")
	if roadmapPath == "" {
		return 0, nil
	}
	data, readErr := os.ReadFile(roadmapPath)
	if readErr != nil {
		return 0, fmt.Errorf("read roadmap: %w", readErr)
	}
	phases, parseErr := agent.ParseRoadmap(string(data))
	if parseErr != nil {
		return 0, fmt.Errorf("parse roadmap: %w", parseErr)
	}
	if len(phases) == 0 {
		return 0, nil
	}
	if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		ff.TotalRoadmapPhases = len(phases)
		return nil
	}); err != nil {
		return 0, fmt.Errorf("persist total phases: %w", err)
	}
	return len(phases), nil
}

// (PopulateExecutionPlanForPhase / PopulateLegacyExecutionPlan removed in
// SchemaVersionCurrent = 3 — the orchestrator's StartMultiRepoImplementation
// reads execution-order.yaml fresh from disk per cycle.)

// TryCompletePublish transitions the feature to StatusPublished when all
// repos have a PR URL. Fires OnFeatureCompleted + OnFeatureSummaryNeeded
// hooks when the feature actually transitions.
func (o *Orchestrator) TryCompletePublish(featureID string) (bool, error) {
	return o.tryCompleteAndEmit(featureID)
}

// MarkDone transitions the feature to StatusDone and fires
// OnFeatureSummaryNeeded so the observe summary is refreshed at the
// terminal transition. Used by explicit mark-done paths where the feature
// reached a terminal state without the orchestrator's publish pipeline.
func (o *Orchestrator) MarkDone(featureID string) error {
	if err := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
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

// MarkPublished persists the PR URL and fires the feature-completed hooks
// (FeatureCompleted event + OnFeatureCompleted + OnFeatureSummaryNeeded)
// so observer emission lives in a single chokepoint.
func (o *Orchestrator) MarkPublished(featureID, prURL string) error {
	if err := o.deps.Lifecycle.MarkPublished(featureID, prURL); err != nil {
		return err
	}
	f, fErr := o.deps.Lifecycle.Get(featureID)
	if fErr == nil && f != nil {
		o.emitEventBlocking(ports.Event{
			Type:      ports.FeatureCompleted,
			FeatureID: featureID,
			Feature:   f,
		})
		if o.hooks.OnFeatureCompleted != nil {
			o.hooks.OnFeatureCompleted(featureID, f)
		}
		if o.hooks.OnFeatureSummaryNeeded != nil {
			o.hooks.OnFeatureSummaryNeeded(featureID, f)
		}
	}
	return nil
}

// CommitRoadmapPhase commits the completed roadmap phase via the Publisher
// port (git.CommitAll under the hood) across every repo in the feature.
// Errors on individual repos are swallowed — this is a best-effort pipeline
// step whose failure should not block the lifecycle.
func (o *Orchestrator) CommitRoadmapPhase(featureID string, phase int) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	if len(f.Repos) == 0 {
		return nil
	}
	phaseName := o.lookupRoadmapPhaseName(f, phase)
	commitMsg := fmt.Sprintf("Phase %d/%d (%s)", phase, f.TotalRoadmapPhases, f.RoadmapPhaseType)
	if phaseName != "" {
		commitMsg += ": " + phaseName
	}
	commitMsg += "\n\nFeature: " + f.Slug
	for _, repo := range f.Repos {
		if repo.WorktreePath == "" {
			continue
		}
		_ = o.deps.Publisher.CommitAll(repo.WorktreePath, commitMsg)
	}
	return nil
}

// RemoveRepoCycle removes a repo cycle entry without firing failure events.
// Callers use it when a cycle restart is aborted and the stale entry must be
// cleared before another cycle can be selected.
func (o *Orchestrator) RemoveRepoCycle(featureID, repoName string) error {
	return o.deps.Lifecycle.RemoveRepoCycle(featureID, repoName)
}

// TransitionTo transitions a feature to a caller-selected status while keeping
// lifecycle mutations behind the orchestrator boundary.
func (o *Orchestrator) TransitionTo(featureID string, status feature.Status) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		return f.Transition(status)
	})
}

// ClearRepoCycles removes every RepoCycles entry on a feature without firing
// failure events. It supports cleanup of published features with active cycles
// without exposing feature.Manager mutators to clients.
func (o *Orchestrator) ClearRepoCycles(featureID string) error {
	return o.deps.Lifecycle.ClearRepoCycles(featureID)
}

// RetryPhase clears feature-level error/gate state so the unified
// phase-implement loop can re-run the active phase from iteration 1.
// Per-repo Touched flags are monotonic and intentionally preserved.
//
// Derives the phase-declared repo subset by parsing the plan's `**Repo:**`
// tags via agent.PhaseScope. Falls back to every Feature.Repos entry when
// the plan can't be read (best-effort recovery — the next orchestrator
// cycle's PhaseScope call will revalidate before launching the loop).
func (o *Orchestrator) RetryPhase(featureID string) error {
	repoNames := o.phaseDeclaredRepos(featureID)
	return o.deps.Lifecycle.RetryPhase(featureID, repoNames)
}

// FailRepoImplementation marks one repo as failed. Phase-atomic failures
// land via agent.AtomicPhaseStamp(PhaseOutcomeFailed); this delegate
// survives for cycle-cleanup callers (a single-repo-cycle failure does not
// fail the whole phase).
func (o *Orchestrator) FailRepoImplementation(featureID, repoName, errMsg string) error {
	return o.deps.Lifecycle.FailRepoImplementation(featureID, repoName, errMsg)
}

func (o *Orchestrator) RecordRebasePreflightFailure(featureID, repoName string, cause error) error {
	if cause == nil {
		return nil
	}
	msg := "rebase preflight: " + cause.Error()
	if err := o.deps.Lifecycle.FailRepoImplementation(featureID, repoName, msg); err != nil {
		return fmt.Errorf("record rebase preflight failure: %w", err)
	}
	o.emitEvent(ports.Event{
		Type:      ports.RepoStatusChanged,
		FeatureID: featureID,
		RepoName:  repoName,
		Message:   msg,
		Error:     cause,
	})
	return nil
}

// phaseDeclaredRepos returns the phase-declared repo subset by running
// agent.PhaseScope against the feature's current plan. Returns every
// Feature.Repos entry on any error.
func (o *Orchestrator) phaseDeclaredRepos(featureID string) []string {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil || f == nil {
		return nil
	}
	planPath := o.resolvePlanPath(f)
	if planPath != "" {
		if scope, err := agent.PhaseScope(f, planPath); err == nil && scope.ScopeOK() && len(scope.Repos) > 0 {
			return scope.Repos
		}
	}
	repos := make([]string, 0, len(f.Repos))
	for _, r := range f.Repos {
		repos = append(repos, r.Name)
	}
	return repos
}

// ClearPendingHelpAndPermissions resets pending help messages and permission
// queue entries without exposing Store.Modify to clients.
func (o *Orchestrator) ClearPendingHelpAndPermissions(featureID string) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
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
		return nil
	})
}

// SetDesignReady transitions a feature into StatusDesignReady without
// going through Transition (the Design slot has a non-linear entry path). Used
// by the restart/resume flows for a failed Design phase.
func (o *Orchestrator) SetDesignReady(featureID string) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		f.Status = feature.StatusDesignReady
		return nil
	})
}

// UpgradePipeline delegates to Lifecycle.UpgradePipeline for rewind flows.
func (o *Orchestrator) UpgradePipeline(featureID string, profile feature.PipelineProfile) error {
	return o.deps.Lifecycle.UpgradePipeline(featureID, profile)
}

// RewindToPhase delegates to Lifecycle.RewindToPhase. Returns the effective
// target phase and warnings computed during the rewind.
func (o *Orchestrator) RewindToPhase(featureID string, targetPhase feature.Phase) ([]string, feature.Phase, error) {
	sourceRun := o.currentRunNumber(featureID)
	warnings, effectiveTarget, err := o.deps.Lifecycle.RewindToPhase(featureID, targetPhase)
	if err != nil {
		return warnings, effectiveTarget, err
	}
	o.fireFeatureRewoundHook(featureID, feature.RewindRequest{TargetPhase: targetPhase}, effectiveTarget, sourceRun)
	o.emitEventBlocking(ports.Event{Type: ports.FeatureRewound, FeatureID: featureID, Phase: effectiveTarget})
	return warnings, effectiveTarget, nil
}

// RewindWithRequest delegates to Lifecycle.RewindWithRequest for rewind
// flows that carry additional request fields such as a roadmap phase.
func (o *Orchestrator) RewindWithRequest(featureID string, request feature.RewindRequest) ([]string, feature.Phase, error) {
	sourceRun := o.currentRunNumber(featureID)
	warnings, effectiveTarget, err := o.deps.Lifecycle.RewindWithRequest(featureID, request)
	if err != nil {
		return warnings, effectiveTarget, err
	}
	o.fireFeatureRewoundHook(featureID, request, effectiveTarget, sourceRun)
	o.emitEventBlocking(ports.Event{Type: ports.FeatureRewound, FeatureID: featureID, Phase: effectiveTarget})
	return warnings, effectiveTarget, nil
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

// CleanWorktree delegates to Lifecycle.CleanWorktree for clean-worktree actions.
func (o *Orchestrator) CleanWorktree(featureID string) error {
	return o.deps.Lifecycle.CleanWorktree(featureID)
}

// SaveFeatureSummary persists a feature summary without exposing Store.Save.
func (o *Orchestrator) SaveFeatureSummary(featureID, summary string) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		f.Summary = summary
		return nil
	})
}

// MergeFeatureLocal commits any uncommitted changes in each repo's worktree
// and merges the feature branch into its base branch locally. Errors identify
// the affected repository so clients can present a useful diagnostic.
func (o *Orchestrator) MergeFeatureLocal(featureID string) error {
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
		if baseBranch == "" && o.deps.Branch != nil {
			baseBranch, _ = o.deps.Branch.DefaultBranch(repo.Path)
		}

		// Commit any uncommitted changes before merging.
		if o.deps.Publisher != nil {
			hasChanges, hcErr := o.deps.Publisher.HasUncommittedChanges(repoPath)
			if hcErr == nil && hasChanges {
				if err := o.deps.Publisher.CommitAll(repoPath, "Final changes before merge"); err != nil {
					return fmt.Errorf("%s: commit failed: %w", repo.Name, err)
				}
			}
		}

		if o.deps.Rebaser != nil {
			if err := o.deps.Rebaser.MergeFeatureBranch(repo.Path, branch, baseBranch); err != nil {
				return fmt.Errorf("%s: %w", repo.Name, err)
			}
		}
	}

	return nil
}

// SetRepoPublished persists a successful per-repo publish without exposing
// feature.Manager mutations to clients.
func (o *Orchestrator) SetRepoPublished(featureID, repoName, prURL string) error {
	return o.deps.Lifecycle.SetRepoPublished(featureID, repoName, prURL)
}

// SetRepoPublishError records a per-repo publish failure without changing the
// repository status or exposing feature.Manager mutations to clients.
func (o *Orchestrator) SetRepoPublishError(featureID, repoName, errMsg string) error {
	return o.deps.Lifecycle.SetRepoPublishError(featureID, repoName, errMsg)
}

// RecordPublishUIFailure marks a single-repo feature failed when the publish
// UI surfaces an error already emitted by the publish pipeline. This typed
// boundary fires the FeatureFailed hook with FailureInfrastructure when there
// is no per-repository fan-out path.
func (o *Orchestrator) RecordPublishUIFailure(featureID, errMsg string) error {
	return o.markFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg)
}

// ReportMissingArtifactFailure marks a feature failed when a phase runner
// signalled success but the expected artifact file is absent. The check lives
// at the SDK/session bridge so the phase-completion handler cannot see it; this
// method routes the failure through a typed boundary instead of MarkFailed.
func (o *Orchestrator) ReportMissingArtifactFailure(featureID, errMsg string) error {
	return o.markFailedWithEvent(featureID, feature.FailureMissingArtifact, errMsg)
}

// ReportProtocolViolation marks a feature failed when a session ended without
// the universal completion marker or produced artifacts that violate its role
// contract.
func (o *Orchestrator) ReportProtocolViolation(featureID, errMsg string) error {
	return o.markFailedWithEvent(featureID, feature.FailureProtocolViolation, errMsg)
}

// CommitUncommittedForPublish commits uncommitted changes in each repository
// so publish views can show a complete commit log. Errors on individual
// repositories are swallowed because this presentation helper must not block
// the lifecycle.
func (o *Orchestrator) CommitUncommittedForPublish(featureID string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	if o.deps.Publisher == nil {
		return nil
	}
	for i := range f.Repos {
		repo := &f.Repos[i]
		workDir := repo.WorktreePath
		if workDir == "" {
			workDir = repo.Path
		}
		if workDir == "" {
			continue
		}
		hasChanges, hcErr := o.deps.Publisher.HasUncommittedChanges(workDir)
		if hcErr != nil || !hasChanges {
			continue
		}
		_ = o.deps.Publisher.CommitAll(workDir, f.Name)
	}
	return nil
}

// UpdateFeatureConfigInput carries the editable per-feature config axes
// for Orchestrator.UpdateFeatureConfig. All fields are always
// populated — callers build the input from the current feature snapshot
// plus their edits, so a zero-value field is a real "set this to empty"
// operation, not "leave alone".
type UpdateFeatureConfigInput struct {
	Models              config.ModelConfig
	Effort              config.EffortConfig
	Inquireness         feature.Inquireness
	Checkpoints         feature.Checkpoints
	InputNotifications  feature.InputNotificationsMode
	AutomaticReviewMode feature.AutomaticReviewMode
}

// ErrFeatureNotQuiescent is kept for compatibility with older callers that
// matched the former idle-only edit rejection. UpdateFeatureConfig no longer
// returns it: feature-level config edits are persisted for any feature state,
// and active sessions pick them up on the next phase or restart.
var ErrFeatureNotQuiescent = errors.New("feature is not in a quiescent state")

// UpdateFeatureConfig atomically writes the editable config axes. Same
// idiom as ApplyRefactorPipeline — Store.Modify handles locking + atomic
// write. On success, emits ports.Event{Type: FeatureConfigChanged}
// (non-blocking) and fires hooks.OnFeatureConfigChanged(before, after) so the
// observer writes a feature.config_changed audit entry.
//
// Re-entrancy: A second call with identical inputs against an already-
// updated feature re-writes the same three fields to the same values, emits a
// second audit + event, and returns nil. This is acceptable — the audit trail
// explicitly records "no semantic change" via before == after.
// Crash recovery: Store.Modify performs an atomic unique-temp + rename,
// so a crash before rename leaves feature.yaml untouched; a crash after
// rename leaves the new values on disk and the hook+event are simply not
// fired — the next startup reads the persisted values as source of truth.
func (o *Orchestrator) UpdateFeatureConfig(featureID string, input UpdateFeatureConfigInput) error {
	var before, after feature.ConfigSnapshot
	err := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
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
		f.AutomaticReviewMode = feature.PersistAutomaticReviewMode(input.AutomaticReviewMode)
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

// EnterReviewGate transitions a feature into the review-needs state for the
// given target phase and records the pending review so gate bookkeeping remains
// inside the orchestrator.
func (o *Orchestrator) EnterReviewGate(featureID string, targetPhase feature.Phase) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		enterReviewGateFeatureState(f, targetPhase)
		return nil
	})
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

// ExtendFailedPhaseBudget clears failure bookkeeping on a failed feature and
// bumps iteration budgets by caller-supplied deltas. The caller reads configured
// defaults and passes them in so the orchestrator boundary stays free of config
// plumbing. Deltas <= 0 are ignored.
func (o *Orchestrator) ExtendFailedPhaseBudget(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		if f.Status != feature.StatusFailed {
			return nil
		}
		if f.FailureType == feature.FailureMaxIterations && maxIterationsDelta > 0 {
			f.MaxIterations += maxIterationsDelta
		}
		if f.CurrentPhase == feature.PhasePlan && maxPlanIterationsDelta > 0 {
			f.MaxPlanIterations += maxPlanIterationsDelta
		}
		f.LastError = ""
		f.FailureType = ""
		return nil
	})
}

// RepoCycleRestart describes one review-comments repository cycle that a caller
// must relaunch after a restart.
type RepoCycleRestart struct {
	RepoName    string
	CycleType   feature.RepoCycleType
	PlanContent string
}

// RefactorRestart describes the single refactor cycle that needs to be
// re-launched after a restart. Refactor cycles are limited to one per feature
// so at most one instance is returned.
type RefactorRestart struct {
	RepoName string
	Prompt   string
}

// CollectAndClearRepoCycleRestarts snapshots the feature's RepoCycles map,
// reads each review-comments cycle's plan file from disk, clears the cycle
// state, and returns restart descriptors for the caller. At most one refactor
// cycle is returned because only one refactor runs at a time.
func (o *Orchestrator) CollectAndClearRepoCycleRestarts(featureID string) ([]RepoCycleRestart, *RefactorRestart, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return nil, nil, fmt.Errorf("load feature: %w", err)
	}

	type cycleSnapshot struct {
		repoName  string
		cycleType feature.RepoCycleType
		planPath  string
	}
	var cycles []cycleSnapshot
	for repoName, rc := range f.RepoCycles {
		cycles = append(cycles, cycleSnapshot{repoName, rc.Type, rc.PlanPath})
	}

	if err := o.deps.Lifecycle.ClearRepoCycles(featureID); err != nil {
		return nil, nil, fmt.Errorf("clear repo cycles: %w", err)
	}

	var restarts []RepoCycleRestart
	var refactor *RefactorRestart
	for _, c := range cycles {
		switch c.cycleType {
		case feature.CycleRefactor:
			if refactor != nil {
				// Only restart one refactor per feature — one at a time.
				continue
			}
			prompt := f.RefactorPrompt
			if prompt == "" && c.planPath != "" {
				data, _ := os.ReadFile(c.planPath)
				prompt = extractRefactorPromptFromPlan(string(data))
			}
			refactor = &RefactorRestart{
				RepoName: c.repoName,
				Prompt:   prompt,
			}
		case feature.CycleReviewComments:
			data, _ := os.ReadFile(c.planPath)
			restarts = append(restarts, RepoCycleRestart{
				RepoName:    c.repoName,
				CycleType:   c.cycleType,
				PlanContent: string(data),
			})
		}
	}

	return restarts, refactor, nil
}

// extractRefactorPromptFromPlan returns the user prompt stored in a refactor
// plan file ("# Refactor: <repoName>\n\n<prompt>\n").
func extractRefactorPromptFromPlan(content string) string {
	if content == "" {
		return ""
	}
	if idx := indexDoubleNewline(content); idx >= 0 {
		return trimSpace(content[idx+2:])
	}
	return trimSpace(content)
}

func indexDoubleNewline(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '\n' && s[i+1] == '\n' {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// GateReviewContext bundles the artifact path and worktree directory needed to
// launch a gate-review session. An empty ArtifactPath means no artifact could
// be resolved.
type GateReviewContext struct {
	ArtifactPath string
	WorkDir      string
}

// RewindReviewContext bundles the artifact path, work directory, and any
// warnings produced while resolving a rewind review. Callers may surface the
// warnings while continuing the review with a fallback artifact.
type RewindReviewContext struct {
	ArtifactPath string
	WorkDir      string
	Warnings     []string
}

// ResolveRewindReviewContext resolves the editable artifact for an already
// performed rewind. Partial Implement rewinds are routed through the selected
// roadmap phase plan recorded on the active run; full Implement rewinds retain
// the legacy global plan path.
func (o *Orchestrator) ResolveRewindReviewContext(featureID string, targetPhase feature.Phase) (RewindReviewContext, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return RewindReviewContext{}, fmt.Errorf("load feature: %w", err)
	}

	var artifactPath string
	var warnings []string
	switch targetPhase {
	case feature.PhaseInquire:
		if baseDir := o.stateDir(); baseDir != "" {
			artifactPath = filepath.Join(baseDir, featureID, "description-review.md")
		}
	case feature.PhaseResearch:
		artifactPath = o.resolveArtifactPath(f, "inquire")
	case feature.PhaseDesign:
		artifactPath = o.resolveArtifactPath(f, "research")
	case feature.PhasePlan:
		artifactPath = o.resolveArtifactPath(f, "design")
		if artifactPath == "" {
			artifactPath = o.resolveArtifactPath(f, "research")
		}
		if artifactPath == "" && f.EffectivePipeline() == feature.PipelineMedium {
			if baseDir := o.stateDir(); baseDir != "" {
				descPath := filepath.Join(baseDir, featureID, "description-review.md")
				if _, statErr := os.Stat(descPath); statErr != nil {
					_ = os.WriteFile(descPath, []byte(f.Description), 0o644)
				}
				if _, statErr := os.Stat(descPath); statErr == nil {
					artifactPath = descPath
				}
			}
		}
	case feature.PhaseImplement:
		if f.PendingRewindReviewRoadmapPhase != nil && *f.PendingRewindReviewRoadmapPhase > 0 {
			phase := *f.PendingRewindReviewRoadmapPhase
			artifactPath = o.resolveArtifactPath(f, fmt.Sprintf("phase-%d-plan", phase))
			if artifactPath == "" {
				warnings = append(warnings, fmt.Sprintf("phase %d plan artifact is missing; falling back to global plan review artifact", phase))
				artifactPath = o.resolveArtifactPath(f, "plan")
			}
		} else {
			artifactPath = o.resolveArtifactPath(f, "plan")
		}
	}

	return RewindReviewContext{
		ArtifactPath: artifactPath,
		WorkDir:      reviewWorkDir(f),
		Warnings:     warnings,
	}, nil
}

// ResolveGateReviewContext resolves the artifact path + working directory for
// a gate-review launch. Keeping this assembly in the orchestrator ensures
// clients do not own the target-phase to artifact-key mapping, which encodes
// that a phase review reads the preceding phase's approved artifact.
func (o *Orchestrator) ResolveGateReviewContext(featureID string, targetPhase feature.Phase) (GateReviewContext, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return GateReviewContext{}, fmt.Errorf("load feature: %w", err)
	}

	var artifactPath string
	switch targetPhase {
	case feature.PhaseResearch:
		artifactPath = o.resolveArtifactPath(f, "inquire")
	case feature.PhaseDesign:
		artifactPath = o.resolveArtifactPath(f, "research")
	case feature.PhasePlan:
		artifactPath = o.resolveArtifactPath(f, "design")
		if artifactPath == "" {
			artifactPath = o.resolveArtifactPath(f, "research")
		}
	case feature.PhaseImplement:
		// Roadmap features at phase 0 → roadmap artifact (initial roadmap review).
		// Roadmap features at phase N > 0 → per-phase plan artifact
		// (phase-N-plan key routes through resolvePhaseDirForKey, which is
		// refactor-aware via RefactorPrefix() and matches resolvePlanPath's
		// fallback cascade).
		// Legacy single-repo non-roadmap → generic "plan" artifact.
		switch {
		case f.TotalRoadmapPhases > 0 && f.CurrentRoadmapPhase == 0:
			artifactPath = o.resolveArtifactPath(f, "roadmap")
		case f.CurrentRoadmapPhase > 0:
			artifactPath = o.resolveArtifactPath(f, fmt.Sprintf("phase-%d-plan", f.CurrentRoadmapPhase))
		default:
			artifactPath = o.resolveArtifactPath(f, "plan")
		}
	}

	return GateReviewContext{ArtifactPath: artifactPath, WorkDir: reviewWorkDir(f)}, nil
}

func reviewWorkDir(f *feature.Feature) string {
	if f == nil || len(f.Repos) == 0 {
		return ""
	}
	workDir := f.Repos[0].WorktreePath
	if workDir == "" {
		workDir = f.Repos[0].Path
	}
	return workDir
}

// ErrFeatureBusy is returned by RestartPhase when the feature still has at
// least one active session. The typical trigger is the user pressing "r"
// while a stop is still draining sessions: the feature is already at
// StatusInterrupted (set at the head of InterruptFeature) but agents are
// still being SIGTERM'd. Without this guard, RestartPhase would call
// StopFeatureSessions alongside the stop loop, dispatch a fresh KB cycle
// the user didn't request, and chain further "r" presses kill the new
// sessions and start more, etc. Callers should treat this as a soft failure
// and surface a "wait and retry" hint rather than marking the feature
// failed.
var ErrFeatureBusy = errors.New("feature has active sessions; wait for them to finish before restarting")

// RestartAction enumerates the follow-up outcomes returned by RestartPhase.
type RestartAction int

const (
	// RestartNoOp means the orchestrator transitioned state with no follow-up.
	RestartNoOp RestartAction = iota

	// RestartDispatchPhase requires the caller to start Outcome.Phase.
	RestartDispatchPhase

	// RestartDispatchRepoCycles returns repository and refactor descriptors for
	// the caller to relaunch.
	RestartDispatchRepoCycles

	// RestartDispatchRebase resumes a retained feature-level rebase operation.
	RestartDispatchRebase
)

// RestartOutcome describes the follow-up required after RestartPhase applies
// its state transitions.
type RestartOutcome struct {
	Action            RestartAction
	Phase             feature.Phase // meaningful only for RestartDispatchPhase
	RepoCycleRestarts []RepoCycleRestart
	RefactorRestart   *RefactorRestart
}

// RestartPhase is the single orchestrator entrypoint for user-initiated phase
// restarts:
//   - Stops any active sessions (delegates to StopFeatureSessions).
//   - On Failed features, clears the failure bookkeeping and extends the
//     iteration budget by caller-supplied deltas.
//   - On Published features with RepoCycles present, collects and clears the
//     per-cycle restart descriptors and returns RestartDispatchRepoCycles.
//   - Otherwise, walks the phase+status decision tree to transition the
//     feature back to a startable status and returns RestartDispatchPhase with
//     the phase the caller should relaunch.
//
// maxIterationsDelta / maxPlanIterationsDelta are the config-derived bumps
// used when the feature's FailureType == FailureMaxIterations (or the phase
// is Plan). Pass 0 to skip the bump; callers supply configured defaults so the
// orchestrator boundary stays free of config plumbing.
func (o *Orchestrator) RestartPhase(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) (RestartOutcome, error) {
	// Refuse if any session for this feature is still active. This catches the
	// "user spammed 'r' during a stop" case: InterruptFeature is mid-loop,
	// sessions are still draining, but the feature already shows
	// StatusInterrupted because Transition runs at the head of InterruptFeature.
	// Without the guard we'd race the stop loop and dispatch a fresh phase the
	// user never asked for.
	if o.deps.Sessions != nil {
		for _, s := range o.deps.Sessions.FeatureSessions(featureID) {
			if s != nil && s.IsActive() && !isArtifactReviewSession(s) {
				return RestartOutcome{}, ErrFeatureBusy
			}
		}
	}

	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return RestartOutcome{}, fmt.Errorf("load feature: %w", err)
	}

	// Stop any active sessions before mutating state so orphaned agents do not
	// race subsequent Store.Modify writes.
	o.StopFeatureSessions(featureID)

	if f.RebaseOperation != nil && f.ActiveCycle != nil &&
		featureRebaseActiveCycleType(f) == feature.CycleRebase {
		switch f.ActiveCycle.Status {
		case feature.RepoCycleFailed, feature.RepoCycleInterrupted:
			return RestartOutcome{Action: RestartDispatchRebase}, nil
		}
	}

	// Clear failure context on restart; extend iteration caps if exhausted.
	// ExtendFailedPhaseBudget is a no-op on non-Failed features so this is
	// safe to call unconditionally, but we retain the explicit gate for clarity.
	if f.Status == feature.StatusFailed {
		_ = o.ExtendFailedPhaseBudget(featureID, maxIterationsDelta, maxPlanIterationsDelta)
	}
	phase := f.CurrentPhase

	// Published/code-ready features with repo cycles (running, failed, or
	// interrupted): clear and return per-cycle restart descriptors for the
	// caller to relaunch. Interrupted post-publish cycles first restore the
	// feature to the publishable base state so the relaunched cycle is again
	// treated as active post-publish work rather than a plain phase restart.
	if (f.Status == feature.StatusPublished || f.Status == feature.StatusCodeReady || f.Status == feature.StatusInterrupted) && len(f.RepoCycles) > 0 {
		if f.Status == feature.StatusInterrupted {
			if err := o.restoreStatusForRepoCycleRestart(featureID); err != nil {
				return RestartOutcome{}, fmt.Errorf("restore status for cycle restart: %w", err)
			}
		}
		restarts, refactor, collectErr := o.CollectAndClearRepoCycleRestarts(featureID)
		if collectErr != nil {
			return RestartOutcome{}, fmt.Errorf("collect cycles: %w", collectErr)
		}
		return RestartOutcome{
			Action:            RestartDispatchRepoCycles,
			RepoCycleRestarts: restarts,
			RefactorRestart:   refactor,
		}, nil
	}

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
			if err := o.TransitionTo(featureID, feature.StatusCreated); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhaseInquire:
			if err := o.TransitionTo(featureID, feature.StatusInquiring); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhaseResearch:
			if err := o.TransitionTo(featureID, feature.StatusResearching); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhaseDesign:
			if err := o.SetDesignReady(featureID); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhasePlan:
			if err := o.TransitionTo(featureID, feature.StatusResearching); err != nil {
				return RestartOutcome{}, err
			}
			if err := o.TransitionTo(featureID, feature.StatusPlanReady); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhaseImplement:
			if err := o.TransitionTo(featureID, feature.StatusImplementReady); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhaseReview, feature.PhaseFinalReview:
			if err := o.resetFailedFinalReviewForRestart(featureID); err != nil {
				return RestartOutcome{}, err
			}
			phase = feature.PhaseFinalReview
		case feature.PhasePublish:
			if err := o.TransitionTo(featureID, feature.StatusCodeReady); err != nil {
				return RestartOutcome{}, err
			}
		default:
			return RestartOutcome{Action: RestartNoOp}, nil
		}
	default:
		switch phase {
		case feature.PhaseKnowledgeBase, feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign, feature.PhasePlan:
			if err := o.TransitionTo(featureID, feature.StatusInterrupted); err != nil {
				return RestartOutcome{}, err
			}
		case feature.PhaseImplement:
			if err := o.TransitionTo(featureID, feature.StatusImplementReady); err != nil {
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
		f.LastError = ""
		f.FailureType = ""
		f.CurrentPhaseStatus = ""
		f.ReviewFixing = false
		f.ReviewingGate = false
		for _, r := range f.Repos {
			if st := f.RepoStates[r.Name]; st != nil {
				st.LastError = ""
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

func (o *Orchestrator) restoreStatusForRepoCycleRestart(featureID string) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		if len(f.PRURLs()) > 0 {
			f.Status = feature.StatusPublished
		} else {
			f.Status = feature.StatusCodeReady
		}
		f.CurrentPhase = feature.PhasePublish
		f.ActiveCycle = nil
		f.SetActiveCycleType("")
		f.LastError = ""
		f.FailureType = ""
		return nil
	})
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
