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
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

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
	if err := o.guardedModify(featureID, MutationMarkDone, func(f *feature.Feature) error {
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

// CommitRoadmapPhase commits the completed roadmap phase across every repo in
// the feature.
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
		_ = git.CommitAll(repo.WorktreePath, commitMsg)
	}
	return nil
}

// TransitionTo transitions a feature to a caller-selected status while keeping
// lifecycle mutations behind the orchestrator boundary.
func (o *Orchestrator) TransitionTo(featureID string, status feature.Status) error {
	return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		return f.Transition(status)
	})
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
	o.relationshipMu.RLock()
	defer o.relationshipMu.RUnlock()
	if err := o.RelationshipGuard(featureID, MutationRetry); err != nil {
		return err
	}
	if err := o.checkChildExecution(featureID); err != nil {
		return err
	}
	repoNames := o.phaseDeclaredRepos(featureID)
	return o.deps.Lifecycle.RetryPhase(featureID, repoNames)
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

// RewindToPhase delegates to Lifecycle.RewindToPhase. Returns the effective
// target phase and warnings computed during the rewind.
func (o *Orchestrator) RewindToPhase(featureID string, targetPhase feature.Phase) ([]string, feature.Phase, error) {
	o.relationshipMu.RLock()
	defer o.relationshipMu.RUnlock()
	if err := o.RelationshipGuard(featureID, MutationRewind); err != nil {
		return nil, targetPhase, err
	}
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
	o.relationshipMu.RLock()
	defer o.relationshipMu.RUnlock()
	return o.rewindWithRequestLocked(featureID, request)
}

// rewindWithRequestLocked is the lock-held rewind entry point. Callers must
// hold the relationship read lock so the guard check and the rewind execute
// atomically with any preceding mutations (e.g. pipeline upgrade).
func (o *Orchestrator) rewindWithRequestLocked(featureID string, request feature.RewindRequest) ([]string, feature.Phase, error) {
	if err := o.RelationshipGuard(featureID, MutationRewind); err != nil {
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

// RewindWithUpgrade performs an optional pipeline upgrade, session stop, and
// rewind as one atomic relationship-guarded operation. The relationship guard
// check, pipeline upgrade, session stop, and rewind all execute under the
// same read lock so a concurrent refactor launch cannot interleave a child
// creation between the guard check and the mutations. Callers that need a
// plain rewind (no pipeline upgrade) should use RewindWithRequest.
func (o *Orchestrator) RewindWithUpgrade(featureID string, request feature.RewindRequest, upgradePipeline feature.PipelineProfile) ([]string, feature.Phase, error) {
	o.relationshipMu.RLock()
	defer o.relationshipMu.RUnlock()
	if err := o.RelationshipGuard(featureID, MutationRewind); err != nil {
		return nil, 0, err
	}
	if upgradePipeline != "" {
		if err := o.deps.Lifecycle.UpgradePipeline(featureID, upgradePipeline); err != nil {
			return nil, 0, err
		}
	}
	o.StopFeatureSessions(featureID)
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
	if err := o.RelationshipGuard(featureID, MutationCleanup); err != nil {
		return err
	}
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
// and merges the feature branch into its base branch locally, then marks the
// feature Done unless it already is. Errors identify the affected repository
// so clients can present a useful diagnostic.
func (o *Orchestrator) MergeFeatureLocal(featureID string) error {
	o.relationshipMu.RLock()
	defer o.relationshipMu.RUnlock()
	if err := o.RelationshipGuard(featureID, MutationMerge); err != nil {
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

// ReportProtocolViolation marks a feature failed when a root turn violates the
// completion protocol or produces artifacts that violate its role contract.
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
	for i := range f.Repos {
		repo := &f.Repos[i]
		workDir := repo.WorktreePath
		if workDir == "" {
			workDir = repo.Path
		}
		if workDir == "" {
			continue
		}
		if !git.HasUncommittedChanges(workDir) {
			continue
		}
		_ = git.CommitAll(workDir, f.Name)
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

// UpdateFeatureConfig atomically writes the editable config axes.
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
func (o *Orchestrator) UpdateFeatureConfig(featureID string, input UpdateFeatureConfigInput) error {
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
		// (phase-N-plan key routes through resolvePhaseDirForKey and
		// matches resolvePlanPath's fallback cascade).
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
)

// RestartOutcome describes the follow-up required after RestartPhase applies
// its state transitions.
type RestartOutcome struct {
	Action RestartAction
	Phase  feature.Phase // meaningful only for RestartDispatchPhase
}

// RestartPhase is the single orchestrator entrypoint for user-initiated phase
// restarts:
//   - Stops any active sessions (delegates to StopFeatureSessions).
//   - On Failed features, clears the failure bookkeeping and extends the
//     iteration budget by caller-supplied deltas.
//   - Otherwise, walks the phase+status decision tree to transition the
//     feature back to a startable status and returns RestartDispatchPhase with
//     the phase the caller should relaunch.
//
// maxIterationsDelta / maxPlanIterationsDelta are the config-derived bumps
// used when the feature's FailureType == FailureMaxIterations (or the phase
// is Plan). Pass 0 to skip the bump; callers supply configured defaults so the
// orchestrator boundary stays free of config plumbing.
func (o *Orchestrator) RestartPhase(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) (RestartOutcome, error) {
	// The relationship guard rejects parent restart while a child is active.
	// For children, checkChildExecution enforces active relationship and
	// execution capability before any state changes.
	o.relationshipMu.RLock()
	defer o.relationshipMu.RUnlock()
	if err := o.RelationshipGuard(featureID, MutationRestart); err != nil {
		return RestartOutcome{}, err
	}
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

	if err := o.checkChildExecution(featureID); err != nil {
		return RestartOutcome{}, err
	}
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return RestartOutcome{}, fmt.Errorf("load feature: %w", err)
	}

	// Stop any active sessions before mutating state so orphaned agents do not
	// race subsequent Store.Modify writes.
	o.StopFeatureSessions(featureID)

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
		// StatusReviewPassed + CurrentPhase=PhaseFinalReview. RestartPhase
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
	// ExtendFailedPhaseBudget is a no-op on non-Failed features so this is
	// safe to call unconditionally, but we retain the explicit gate for clarity.
	if f.Status == feature.StatusFailed {
		_ = o.ExtendFailedPhaseBudget(featureID, maxIterationsDelta, maxPlanIterationsDelta)
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
