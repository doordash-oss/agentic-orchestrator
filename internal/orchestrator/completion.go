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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// isTerminalForCompletion returns true when the feature is in a terminal
// state that completion handlers must short-circuit on. Every completion path
// uses this guard so late session results cannot overwrite a terminal state.
// A child whose relationship is closed (discarded or completed) or whose
// discard intent is in flight is also terminal so late completion callbacks
// cannot overwrite discard or closure state.
func isTerminalForCompletion(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	if f.Status == feature.StatusInterrupted || f.Status == feature.StatusFailed {
		return true
	}
	if f.IsChild() && f.Parent != nil && (f.Parent.CloseOutcome != "" || f.IsDiscarding()) {
		return true
	}
	return false
}

// errFinalReviewInterrupted signals that the deferred Final Review pass
// returned because the user pressed Stop. The InterruptFeature path drives
// the StatusInterrupted transition and emits FeatureInterrupted; the
// dispatch chain must NOT mark the feature Failed when this surfaces, and
// the caller of runDeferredFinalReview must NOT proceed to MarkCodeReady /
// auto-publish on a feature that is already at StatusInterrupted.
//
// The plan- and implement-loop interrupted branches just return nil because
// their callers do no follow-up work; runDeferredFinalReview is called from
// inside onMultiReposPassed, which has trailing MarkCodeReady / publish
// steps that would fail on an Interrupted feature, so the sentinel exists
// to give that caller an unambiguous short-circuit signal.
var errFinalReviewInterrupted = errors.New("final review interrupted")

var finalReviewRootOrchestrationArtifacts = []string{
	agent.PhaseCompleteFile,
	"progress.md",
	"verification-report.yaml",
	"review-feedback.md",
	"meta.yaml",
}

// resolveActiveCycleType consults the explicit ActiveCycleType field first,
// then falls back to persisted compatibility signals.
// Only used by completion handlers that route through cycle-aware paths.
func resolveActiveCycleType(f *feature.Feature) feature.RepoCycleType {
	if f == nil {
		return ""
	}
	if f.ActiveCycleType() != "" {
		return f.ActiveCycleType()
	}
	switch {
	case f.AddressingReviews():
		return feature.CycleReviewComments
	}
	return ""
}

// emitPhaseCompleted emits a PhaseCompleted event and fires the hook. This
// is the sole emission site for phase completion in completion handlers.
func (o *Orchestrator) emitPhaseCompleted(featureID string, phase feature.Phase, err error) {
	if o.hooks.OnPhaseCompleted != nil {
		o.hooks.OnPhaseCompleted(featureID, phase, err)
	}
	o.emitEventBlocking(ports.Event{
		Type:      ports.PhaseCompleted,
		FeatureID: featureID,
		Phase:     phase,
		Error:     err,
	})
}

func formatSingleShotProtocolViolationError(role agent.Role, dir string, violations []agent.ProtocolViolation) string {
	return agent.FormatSingleShotProtocolViolationError(role, dir, violations)
}

// markFailedWithEvent transitions the feature to StatusFailed via lifecycle
// and emits FeatureFailed. External observer concerns remain upstream of the
// orchestrator port.
//
// Fires OnFeatureSummaryNeeded at the tail (after OnFeatureFailed) so
// downstream observers can persist observe-summary.yaml for the terminal
// failure state.
func (o *Orchestrator) markFailedWithEvent(featureID, failureType, errMsg string) error {
	if err := o.deps.Lifecycle.MarkFailed(featureID, failureType, errMsg); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	o.emitEventBlocking(ports.Event{
		Type:      ports.FeatureFailed,
		FeatureID: featureID,
		Message:   errMsg,
	})
	if o.hooks.OnFeatureFailed != nil {
		o.hooks.OnFeatureFailed(featureID, failureType, errMsg)
	}
	if o.hooks.OnFeatureSummaryNeeded != nil {
		// Best-effort reload for the summary payload. A nil feature triggers
		// the hook's feature-manager fallback (see BuildHooks).
		if f, err := o.deps.Lifecycle.Get(featureID); err == nil {
			o.hooks.OnFeatureSummaryNeeded(featureID, f)
		} else {
			o.hooks.OnFeatureSummaryNeeded(featureID, nil)
		}
	}
	return nil
}

// MarkFailed is the public wrapper for markFailedWithEvent. API callers and other
// callers route terminal-failure transitions through this method so the
// FeatureFailed event / OnFeatureFailed / OnFeatureSummaryNeeded hooks fire
// from a single orchestrator-owned emission site.
func (o *Orchestrator) MarkFailed(featureID, failureType, errMsg string) error {
	return o.markFailedWithEvent(featureID, failureType, errMsg)
}

// ---------------------------------------------------------------------------
// KB completion
// ---------------------------------------------------------------------------

// onKBCompleted handles a per-repo KB completion signal.
//
// Success path:
//  1. Stale-completion guard on feature status != StatusBuildingKB.
//  2. Consume the supervisor-committed outcome and validate the KnowledgeBase
//     artifact contract.
//  3. MarkKBFresh (best-effort).
//  4. MarkRepoKBCompleted(featureID, repoName).
//  5. When AllKBsCompleted → clear ForceKBRebuild, CompleteKnowledgeBase,
//     emit PhaseCompleted, advance.
//
// Failure path:
//  1. MarkRepoKBFailed(featureID, repoName, errMsg).
//  2. Stop sibling KB sessions for this feature.
//  3. emit PhaseCompleted with err.
//  4. markFailedWithEvent(FailureSessionCrash).
func (o *Orchestrator) onKBCompleted(featureID string, input PhaseCompletionInput) error {
	repoName := agent.RepoNameFromKBSession(input.SessionID)

	if !input.Success {
		errMsg := input.ErrorDetail
		if errMsg == "" {
			errMsg = "knowledge base generation failed"
		}
		// When InterruptFeature has already transitioned the feature to
		// StatusInterrupted (user pressed Stop), the SessionDoneMsg from each
		// killed session races into onKBCompleted and would otherwise mark
		// the feature Failed with failure_type=session_crash, masking the
		// interrupt. The session "error" in that case is the agent's last
		// in-flight narration, not a real crash. Short-circuit here and let
		// InterruptFeature own the terminal transition. Still wake KB
		// waiters since session cleanup released this feature's kb.lock.
		if f, _ := o.deps.Lifecycle.Get(featureID); isTerminalForCompletion(f) {
			o.wakeKBWaiters(featureID)
			return nil
		}
		if repoName != "" {
			_ = o.deps.Lifecycle.MarkRepoKBFailed(featureID, repoName, errMsg)
		}
		// Stop any sibling sessions still running for this feature so the
		// whole KB phase aborts cleanly.
		if o.deps.Sessions != nil {
			sessions := o.deps.Sessions.FeatureSessions(featureID)
			for _, s := range sessions {
				_ = o.deps.Sessions.StopSession(s.ID())
			}
		}
		o.emitPhaseCompleted(featureID, feature.PhaseKnowledgeBase, errors.New(errMsg))
		err := o.markFailedWithEvent(featureID, feature.FailureSessionCrash, errMsg)
		// The session-cleanup func released this feature's kb.lock before
		// onKBCompleted was dispatched. Wake any waiters parked on that lock
		// regardless of whether the failure-mark itself succeeded.
		o.wakeKBWaiters(featureID)
		return err
	}

	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	// Ignore late completion signals once the phase has already advanced.
	if f.Status != feature.StatusBuildingKB {
		return nil
	}

	_, kbDir, violations, err := o.validateKBCompletionContract(f, repoName)
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		errMsg := formatSingleShotProtocolViolationError(agent.RoleKnowledgeBaseBuilder, kbDir, violations)
		if repoName != "" {
			_ = o.deps.Lifecycle.MarkRepoKBFailed(featureID, repoName, errMsg)
		}
		if o.deps.Sessions != nil {
			for _, s := range o.deps.Sessions.FeatureSessions(featureID) {
				_ = o.deps.Sessions.StopSession(s.ID())
			}
		}
		o.emitPhaseCompleted(featureID, feature.PhaseKnowledgeBase, errors.New(errMsg))
		err := o.markFailedWithEvent(featureID, feature.FailureProtocolViolation, errMsg)
		o.wakeKBWaiters(featureID)
		return err
	}

	// Mark KB fresh on disk — best-effort, failures don't block the phase.
	// For child features, mark the disposable workspace fresh instead of the
	// canonical KB.
	if repoName != "" {
		if repo, ok := findRepo(f, repoName); ok {
			if baseDir := o.stateDir(); baseDir != "" {
				repoPath := repo.Path
				if repo.WorktreePath != "" {
					repoPath = repo.WorktreePath
				}
				if f.IsChild() {
					workspaceDir := feature.ChildKBWorkspaceDir(baseDir, featureID, repo.Name)
					_ = agent.MarkWorkspaceFresh(context.Background(), o.deps.CmdRunner, workspaceDir, repoPath)
				} else {
					kbDir := agent.KBStateDir(baseDir, repo.Name)
					_ = agent.MarkKBFresh(context.Background(), o.deps.CmdRunner, kbDir, repoPath)
				}
			}
		}
		if err := o.deps.Lifecycle.MarkRepoKBCompleted(featureID, repoName); err != nil {
			return fmt.Errorf("mark repo KB completed: %w", err)
		}
		// This repo's kb.lock was released by session cleanup before
		// onKBCompleted ran. Wake any feature parked on that lock so it
		// can retry its own KB build for this repo.
		o.wakeKBWaiters(featureID)
	}

	allDone, err := o.deps.Lifecycle.AllKBsCompleted(featureID)
	if err != nil {
		return fmt.Errorf("check all KBs completed: %w", err)
	}
	if !allDone {
		return nil
	}

	if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		ff.ForceKBRebuild = false
		return nil
	}); err != nil {
		return fmt.Errorf("clear ForceKBRebuild: %w", err)
	}
	if err := o.deps.Lifecycle.CompleteKnowledgeBase(featureID); err != nil {
		return fmt.Errorf("complete knowledge base: %w", err)
	}
	o.emitPhaseCompleted(featureID, feature.PhaseKnowledgeBase, nil)
	return o.advanceToNextPhase(featureID, feature.PhaseKnowledgeBase)
}

func (o *Orchestrator) validateKBCompletionContract(f *feature.Feature, repoName string) (agent.Outcome, string, []agent.ProtocolViolation, error) {
	var violations []agent.ProtocolViolation
	var outcome agent.Outcome
	kbDir := ""

	baseDir := o.stateDir()
	switch {
	case repoName == "":
		violations = append(violations, agent.ProtocolViolation{
			Artifact: "knowledge-base",
			Reason:   "repo name could not be resolved from session id",
		})
	case baseDir == "":
		violations = append(violations, agent.ProtocolViolation{
			Artifact: "knowledge-base",
			Reason:   "state directory is empty",
		})
	default:
		if f.IsChild() {
			kbDir = feature.ChildKBWorkspaceDir(baseDir, f.ID, repoName)
		} else {
			kbDir = agent.KBStateDir(baseDir, repoName)
		}
		var contractViolations []agent.ProtocolViolation
		var err error
		outcome, contractViolations, err = agent.Validate(
			feature.PhaseKnowledgeBase,
			agent.RoleKnowledgeBaseBuilder,
			kbDir,
		)
		if err != nil {
			return agent.Outcome{}, kbDir, nil, fmt.Errorf("validating knowledge base contract: %w", err)
		}
		violations = append(violations, contractViolations...)
	}

	return outcome, kbDir, violations, nil
}

// findRepo returns the FeatureRepo with the given name.
func findRepo(f *feature.Feature, name string) (feature.FeatureRepo, bool) {
	for _, r := range f.Repos {
		if r.Name == name {
			return r, true
		}
	}
	return feature.FeatureRepo{}, false
}

// ---------------------------------------------------------------------------
// Artifact-phase completion (Inquire / Research / Design)
// ---------------------------------------------------------------------------

// onArtifactPhaseCompleted handles completion for the three interactive
// artifact phases (inquire, research, design).
//
//  1. Consume the supervisor-committed outcome and validate the
//     registry-owned markdown artifact contract for the phase output dir.
//  2. Persist the registry-selected artifact path to f.Artifacts[phase].
//  3. For Q&A-bearing planning phases, write qa-answers.md from the
//     session's QALog (best-effort).
//  4. Call the phase-specific Complete* lifecycle method.
//  5. Emit PhaseCompleted.
//  6. Advance to next phase.
func (o *Orchestrator) onArtifactPhaseCompleted(
	featureID string,
	input PhaseCompletionInput,
	phaseKey string,
	completeFn func(string) error,
) error {
	return o.onArtifactPhaseCompletedWithKey(featureID, input, phaseKey, phaseKey, completeFn)
}

// onArtifactPhaseCompletedWithKey is the canonical entry point that lets the
// dispatcher record the artifact under a different map key than the on-disk
// phase directory name.
func (o *Orchestrator) onArtifactPhaseCompletedWithKey(
	featureID string,
	input PhaseCompletionInput,
	phaseKey string,
	artifactKey string,
	completeFn func(string) error,
) error {
	if !input.Success {
		if f, _ := o.deps.Lifecycle.Get(featureID); isTerminalForCompletion(f) {
			return nil
		}
		// LastError keeps the "<Phase> phase failed: <detail>" format so
		// downstream reporters (banners, observe-summary.yaml) can attribute
		// the failure to a specific phase even when ErrorDetail is a bare
		// technical string.
		errMsg := fmt.Sprintf("%s phase session exited without success", input.Phase)
		if input.ErrorDetail != "" {
			errMsg = fmt.Sprintf("%s phase failed: %s", input.Phase, input.ErrorDetail)
		}
		o.emitPhaseCompleted(featureID, input.Phase, errors.New(errMsg))
		return o.markFailedWithEvent(featureID, feature.FailureSessionCrash, errMsg)
	}

	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	if isTerminalForCompletion(f) {
		return nil
	}

	outcome, phaseDir, violations, err := o.validateArtifactPhaseCompletionContract(input, f, phaseKey)
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		errMsg := formatSingleShotProtocolViolationError(artifactPhaseRoleMust(input.Phase), phaseDir, violations)
		o.emitPhaseCompleted(featureID, input.Phase, errors.New(errMsg))
		return o.markFailedWithEvent(featureID, feature.FailureProtocolViolation, errMsg)
	}

	if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		if ff.Artifacts == nil {
			ff.Artifacts = make(map[string]string)
		}
		ff.Artifacts[artifactKey] = outcome.PhaseArtifactPath
		return nil
	}); err != nil {
		return fmt.Errorf("record %s artifact: %w", artifactKey, err)
	}

	if artifactPhasePersistsQALog(phaseKey) {
		if phaseDir != "" && input.SessionID != "" {
			_ = o.writeQAFile(input.SessionID, phaseDir)
		}
	}

	if err := completeFn(featureID); err != nil {
		return fmt.Errorf("complete %s: %w", phaseKey, err)
	}
	o.emitPhaseCompleted(featureID, input.Phase, nil)
	return o.advanceToNextPhase(featureID, input.Phase)
}

func artifactPhaseRoleMust(phase feature.Phase) agent.Role {
	role, _ := artifactPhaseRole(phase)
	return role
}

func artifactPhasePersistsQALog(phaseKey string) bool {
	switch phaseKey {
	case "inquire", "research", "design":
		return true
	default:
		return false
	}
}

func artifactPhaseRole(phase feature.Phase) (agent.Role, bool) {
	switch phase {
	case feature.PhaseInquire:
		return agent.RoleInquirer, true
	case feature.PhaseResearch:
		return agent.RoleResearcher, true
	case feature.PhaseDesign:
		return agent.RoleDesigner, true
	default:
		return "", false
	}
}

func (o *Orchestrator) validateArtifactPhaseCompletionContract(
	input PhaseCompletionInput,
	f *feature.Feature,
	phaseKey string,
) (agent.Outcome, string, []agent.ProtocolViolation, error) {
	role, ok := artifactPhaseRole(input.Phase)
	if !ok {
		return agent.Outcome{}, "", nil, fmt.Errorf("no artifact phase role for %s", input.Phase)
	}

	baseDir := o.stateDir()
	phaseDir := ""
	if baseDir != "" {
		phaseDir = filepath.Join(agent.ActiveRunDir(baseDir, f), phaseKey)
	}
	var violations []agent.ProtocolViolation
	if baseDir == "" {
		violations = append(violations, agent.ProtocolViolation{
			Artifact: "artifact-phase",
			Reason:   "state directory is empty",
		})
		return agent.Outcome{}, phaseDir, violations, nil
	}
	outcome, contractViolations, err := agent.Validate(input.Phase, role, phaseDir)
	if err != nil {
		return agent.Outcome{}, phaseDir, nil, fmt.Errorf("validating %s contract: %w", phaseKey, err)
	}
	violations = append(violations, contractViolations...)
	if len(violations) == 0 && strings.TrimSpace(outcome.PhaseArtifactPath) == "" {
		violations = append(violations, agent.ProtocolViolation{
			Artifact: phaseKey + " markdown artifact",
			Reason:   "contract validation did not return an artifact path",
		})
	}
	return outcome, phaseDir, violations, nil
}

// ---------------------------------------------------------------------------
// Plan loop completion
// ---------------------------------------------------------------------------

// onPlanLoopDone handles the result of a plan loop, including per-phase loops.
func (o *Orchestrator) onPlanLoopDone(featureID string, result *agent.PlanLoopResult) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	if isTerminalForCompletion(f) {
		return nil
	}
	if result == nil {
		errMsg := "plan loop returned no result"
		o.emitPhaseCompleted(featureID, feature.PhasePlan, errors.New(errMsg))
		return o.markFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg)
	}
	planDir := o.planReadOnlyGuardDir(f)
	repoMutationViolations, err := agent.EnforceReadOnlyRepoMutations(context.Background(), o.deps.CmdRunner, f, feature.PhasePlan, planDir)
	if err != nil {
		return fmt.Errorf("enforce plan read-only repo guard: %w", err)
	}
	if len(repoMutationViolations) > 0 {
		role := agent.RolePlanRoadmapPlanner
		if f.CurrentRoadmapPhase > 0 {
			role = agent.RolePlanPhasePlanner
		}
		errMsg := formatSingleShotProtocolViolationError(role, planDir, repoMutationViolations)
		o.emitPhaseCompleted(featureID, feature.PhasePlan, errors.New(errMsg))
		return o.markFailedWithEvent(featureID, feature.FailureProtocolViolation, errMsg)
	}

	switch result.FinalStatus {
	case "approved":
		return o.onPlanApproved(featureID, f)
	case "needs_human_review":
		return o.onPlanNeedsReview(featureID)
	case "failed":
		errMsg := result.LastError
		if errMsg == "" {
			errMsg = "plan loop failed"
		}
		o.emitPhaseCompleted(featureID, feature.PhasePlan, errors.New(errMsg))
		return o.markFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg)
	case "protocol_violation":
		errMsg := result.LastError
		if errMsg == "" {
			errMsg = "plan loop protocol violation"
		}
		o.emitPhaseCompleted(featureID, feature.PhasePlan, errors.New(errMsg))
		return o.markFailedWithEvent(featureID, feature.FailureProtocolViolation, errMsg)
	case finalStatusInterrupted:
		// Interrupted runs are driven by InterruptFeature; nothing to do.
		return nil
	default:
		errMsg := fmt.Sprintf("unknown plan FinalStatus %q", result.FinalStatus)
		o.emitPhaseCompleted(featureID, feature.PhasePlan, errors.New(errMsg))
		return o.markFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg)
	}
}

// onPlanApproved handles the plan-approved success branch.
func (o *Orchestrator) onPlanApproved(featureID string, f *feature.Feature) error {
	// Roadmap mode: if we're approving the top-level roadmap (CurrentRoadmapPhase==0),
	// parse the roadmap, persist TotalRoadmapPhases, and either wait for review
	// or advance directly.
	//
	// All pipelines (Medium, Large, Moonshot) produce a roadmap as their
	// top-level plan artifact and advance to phase 1 from here. Even if the
	// roadmap file is missing or fails to parse, we still advance the newly
	// approved top-level roadmap so the feature lands in Planning with
	// CurrentRoadmapPhase=1.
	if f.CurrentRoadmapPhase == 0 {
		if roadmapPath := o.resolveArtifactPath(f, "roadmap"); roadmapPath != "" {
			if data, readErr := os.ReadFile(roadmapPath); readErr == nil {
				if phases, parseErr := agent.ParseRoadmap(string(data)); parseErr == nil {
					_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
						ff.TotalRoadmapPhases = len(phases)
						return nil
					})
				}
			}
		}

		// Re-load feature after Modify so subsequent logic sees fresh state.
		ff, getErr := o.deps.Lifecycle.Get(featureID)
		if getErr != nil {
			return fmt.Errorf("reload feature: %w", getErr)
		}
		if ff.Checkpoints.RoadmapReview {
			// Route through review gate.
			if err := o.deps.Lifecycle.NeedsPlanReview(featureID); err != nil {
				return fmt.Errorf("mark needs plan review: %w", err)
			}
			o.emitPhaseCompleted(featureID, feature.PhasePlan, nil)
			phase := feature.PhasePlan
			o.emitEventBlocking(ports.Event{
				Type:      ports.ReviewRequired,
				FeatureID: featureID,
				Phase:     phase,
			})
			if o.hooks.OnReviewRequired != nil {
				o.hooks.OnReviewRequired(featureID, phase)
			}
			return nil
		}
		// Auto-advance into first phase-plan.
		if err := o.deps.Lifecycle.AdvanceRoadmapPhase(featureID); err != nil {
			return fmt.Errorf("advance roadmap phase: %w", err)
		}
		o.emitPhaseCompleted(featureID, feature.PhasePlan, nil)
		startedPhase, started, err := o.startPhase(featureID, feature.PhasePlan)
		if err != nil {
			// If the roadmap artifact is absent after state advancement, preserve
			// the advanced state so the next start attempt can recover. Other
			// dispatch errors still propagate.
			if strings.Contains(err.Error(), "no roadmap artifact") {
				return nil
			}
			return err
		}
		if started {
			o.emitEvent(ports.Event{Type: ports.FeatureAdvanced, FeatureID: featureID, Phase: startedPhase})
		}
		return nil
	}

	// Per-phase plan approval (CurrentRoadmapPhase > 0). If phase-plan review
	// is enabled, we surface it as a review gate; otherwise we run
	// StartRoadmapPhaseImplementation and start implementation.
	if f.Checkpoints.PhasePlanReview {
		if err := o.deps.Lifecycle.NeedsPlanReview(featureID); err != nil {
			return fmt.Errorf("mark needs plan review: %w", err)
		}
		o.emitPhaseCompleted(featureID, feature.PhasePlan, nil)
		phase := feature.PhasePlan
		o.emitEventBlocking(ports.Event{
			Type:      ports.ReviewRequired,
			FeatureID: featureID,
			Phase:     phase,
		})
		if o.hooks.OnReviewRequired != nil {
			o.hooks.OnReviewRequired(featureID, phase)
		}
		return nil
	}

	if err := o.deps.Lifecycle.StartRoadmapPhaseImplementation(featureID); err != nil {
		return fmt.Errorf("start roadmap phase implementation: %w", err)
	}
	// The per-phase execution-order.yaml is read fresh from disk by
	// StartMultiRepoImplementation (per SchemaVersionCurrent = 3); no
	// pre-flight populate step is needed.
	o.emitPhaseCompleted(featureID, feature.PhasePlan, nil)
	startedPhase, started, err := o.startPhase(featureID, feature.PhaseImplement)
	if err != nil {
		return err
	}
	if started {
		o.emitEvent(ports.Event{Type: ports.FeatureAdvanced, FeatureID: featureID, Phase: startedPhase})
	}
	return nil
}

// onPlanNeedsReview handles a plan loop returning "needs_human_review".
// This is the validator's escalation path — it fires when the
// planner ↔ critic loop cannot converge autonomously (max attempts
// exhausted, or an axis stalled with the planner no longer changing the
// frozen section). Always raise a review gate regardless of configured
// planning review gates: those checkpoints control whether to pause for
// routine review after auto-approval, not whether the validator's
// explicit "I need a human" escalation should be honored. Failing the
// feature here would discard a working plan over an exception path the
// user can resolve in a few seconds.
func (o *Orchestrator) onPlanNeedsReview(featureID string) error {
	if err := o.deps.Lifecycle.NeedsPlanReview(featureID); err != nil {
		return fmt.Errorf("mark needs plan review: %w", err)
	}
	o.emitPhaseCompleted(featureID, feature.PhasePlan, nil)
	phase := feature.PhasePlan
	o.emitEventBlocking(ports.Event{
		Type:      ports.ReviewRequired,
		FeatureID: featureID,
		Phase:     phase,
	})
	if o.hooks.OnReviewRequired != nil {
		o.hooks.OnReviewRequired(featureID, phase)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Implement loop completion (single- and multi-repo)
// ---------------------------------------------------------------------------

// onImplementCompleted is the dispatch point for implement-phase completion.
// Multi-repo aggregate results flow through MultiRepoResult; the single-repo
// NEED_USER_INPUT pause flow still arrives as a single-repo LoopResult so the
// orchestrator can transition the feature into StatusNeedUserInput before the
// multi-repo aggregator collapses the cycle. Single-repo cycle completions
// arrive via per-repo cycle FR result channels, never through HandlePhaseCompletion.
func (o *Orchestrator) onImplementCompleted(featureID string, input PhaseCompletionInput) error {
	if input.MultiRepoResult != nil {
		return o.onMultiRepoImplementDone(featureID, input.MultiRepoResult)
	}
	if input.ImplementResult != nil && input.ImplementResult.FinalStatus == finalStatusNeedUserInput {
		return o.onSingleRepoNeedUserInput(featureID, input.ImplementResult)
	}
	errMsg := "implement completion missing multi-repo result"
	o.emitPhaseCompleted(featureID, feature.PhaseImplement, errors.New(errMsg))
	return o.markFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg)
}

// onMultiRepoImplementDone handles an agent.OrchestratorResult completion.
func (o *Orchestrator) onMultiRepoImplementDone(featureID string, result *agent.OrchestratorResult) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	// Multi-repo is stricter than single-repo: only run when StatusImplementing.
	if f.Status != feature.StatusImplementing {
		return nil
	}

	switch result.FinalStatus {
	case finalStatusAllPassed, "awaiting_final_review":
		// "all_passed" is the inline Final Review path. "awaiting_final_review"
		// means every touched repo is staged for the deferred end-of-feature
		// Final Review pass. onMultiReposPassed dispatches that review for the
		// final-or-non-roadmap path before MarkCodeReady.
		return o.onMultiReposPassed(featureID, f)
	case finalStatusInterrupted:
		// Graceful shutdown — shutdownFeatures will transition the feature to
		// StatusInterrupted. We must NOT mark the feature Failed here.
		return nil
	case finalStatusNeedUserInput:
		// Under SchemaVersionCurrent = 4 the NEED_USER_INPUT gate is
		// feature-scoped (Feature.PendingNeedUserInputPath). Persist the
		// gate path and transition the feature into StatusNeedUserInput so
		// the resume dispatcher (resumeFeatureNeedUserInput)
		// finds it paused. Do NOT emit PhaseCompleted — the phase is
		// paused, not done — and surface NeedUserInputRequired so clients can
		// open the questionnaire.
		if result.NeedUserInputPath != "" {
			if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
				ff.PendingNeedUserInputPath = result.NeedUserInputPath
				if ff.Status == feature.StatusImplementing {
					if tErr := ff.Transition(feature.StatusNeedUserInput); tErr != nil {
						return fmt.Errorf("transition to StatusNeedUserInput: %w", tErr)
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
		summary := strings.TrimSpace(result.LastError)
		if summary == "" {
			var paused []string
			for repoName, status := range result.RepoStatuses {
				if status == finalStatusNeedUserInput {
					paused = append(paused, repoName)
				}
			}
			if len(paused) == 0 {
				for _, r := range f.Repos {
					paused = append(paused, r.Name)
				}
			}
			sort.Strings(paused)
			if len(paused) > 0 {
				summary = fmt.Sprintf("implementation is waiting for user input from: %s", strings.Join(paused, ", "))
			} else {
				summary = "implementation is waiting for user input"
			}
		}
		_ = o.deps.Lifecycle.ClearAddressingReviews(featureID)
		o.emitEventBlocking(ports.Event{
			Type:      ports.NeedUserInputRequired,
			FeatureID: featureID,
			Phase:     feature.PhaseImplement,
			Message:   summary,
		})
		return nil
	case finalStatusPlanRevisionRequired:
		return o.routePlanRevision(featureID, f, result.PlanRevisionFeedback)
	case "failed":
		errMsg := result.LastError
		if errMsg == "" {
			if len(result.FailedRepos) > 0 {
				errMsg = fmt.Sprintf("multi-repo implementation failed for repos: %s", strings.Join(result.FailedRepos, ", "))
			} else {
				errMsg = "multi-repo implementation failed"
			}
		}
		// Preserve per-repo typed failures so retry/recovery paths can
		// distinguish iteration caps from hard completion-protocol defects.
		failureType := feature.FailureInfrastructure
		for _, status := range result.RepoStatuses {
			switch status {
			case "protocol_violation":
				failureType = feature.FailureProtocolViolation
			case "safety_rail":
				if failureType == feature.FailureInfrastructure {
					failureType = feature.FailureSafetyRail
				}
			case "max_iterations":
				failureType = feature.FailureMaxIterations
			}
			if failureType == feature.FailureProtocolViolation {
				break
			}
		}
		o.emitPhaseCompleted(featureID, feature.PhaseImplement, errors.New(errMsg))
		_ = o.deps.Lifecycle.ClearAddressingReviews(featureID)
		return o.markFailedWithEvent(featureID, failureType, errMsg)
	default:
		errMsg := fmt.Sprintf("unknown multi-repo FinalStatus %q", result.FinalStatus)
		o.emitPhaseCompleted(featureID, feature.PhaseImplement, errors.New(errMsg))
		return o.markFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg)
	}
}

func (o *Orchestrator) routePlanRevision(featureID string, f *feature.Feature, feedback string) error {
	if f == nil {
		var err error
		f, err = o.deps.Lifecycle.Get(featureID)
		if err != nil {
			return fmt.Errorf("load feature for plan revision: %w", err)
		}
	}
	if strings.TrimSpace(feedback) == "" {
		feedback = "## Plan Revision Required\n\nImplementation cannot resume until the phase plan is corrected.\n"
	}
	targetRoadmapPhase := f.CurrentRoadmapPhase
	if f.TotalRoadmapPhases > 0 && targetRoadmapPhase > f.TotalRoadmapPhases {
		targetRoadmapPhase = f.CurrentRoadmapPhase
	}
	feedbackAttempt, err := o.writePlanRevisionFeedback(f, feedback, targetRoadmapPhase)
	if err != nil {
		return err
	}
	if err := o.moveFeatureToPlanningForPlanRevision(featureID, targetRoadmapPhase, feedbackAttempt+1); err != nil {
		return err
	}
	startedPhase, started, err := o.startPhase(featureID, feature.PhasePlan)
	if err != nil {
		return err
	}
	if started {
		o.emitEvent(ports.Event{Type: ports.FeatureAdvanced, FeatureID: featureID, Phase: startedPhase})
	}
	return nil
}

func (o *Orchestrator) writePlanRevisionFeedback(f *feature.Feature, feedback string, roadmapPhase int) (int, error) {
	baseDir := o.stateDir()
	if baseDir == "" || f == nil {
		return 0, nil
	}
	planDir := ""
	if roadmapPhase > 0 {
		planDir = o.phasePlanDirForFeature(f, roadmapPhase)
	} else {
		planDir = filepath.Join(agent.ActiveRunDir(baseDir, f), "plan")
	}
	latestAttempt := agent.LatestCompletedPlanAttempt(planDir)
	if latestAttempt <= 0 {
		latestAttempt = 1
	}
	attemptDir := filepath.Join(planDir, fmt.Sprintf("attempt-%02d", latestAttempt))
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		return 0, fmt.Errorf("create plan revision feedback dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(feedback), 0o644); err != nil {
		return 0, fmt.Errorf("write plan revision feedback: %w", err)
	}
	if err := agent.WritePlanAttemptMeta(planDir, agent.PlanAttemptMeta{
		Attempt:      latestAttempt,
		ReviewStatus: "CHANGES_REQUESTED",
	}); err != nil {
		return 0, fmt.Errorf("write plan revision attempt meta: %w", err)
	}
	return latestAttempt, nil
}

func (o *Orchestrator) moveFeatureToPlanningForPlanRevision(featureID string, roadmapPhase, minPlanAttempts int) error {
	return o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		switch ff.Status {
		case feature.StatusPlanning:
		case feature.StatusPlanNeedsReview:
			if err := ff.Transition(feature.StatusPlanning); err != nil {
				return err
			}
		case feature.StatusImplementing:
			if err := ff.Transition(feature.StatusReviewPassed); err != nil {
				return err
			}
			if err := ff.Transition(feature.StatusPlanning); err != nil {
				return err
			}
		case feature.StatusFinalReviewing:
			if err := ff.Transition(feature.StatusReviewPassed); err != nil {
				return err
			}
			if err := ff.Transition(feature.StatusPlanning); err != nil {
				return err
			}
		case feature.StatusReviewPassed:
			if err := ff.Transition(feature.StatusPlanning); err != nil {
				return err
			}
		default:
			if err := ff.Transition(feature.StatusPlanning); err != nil {
				return err
			}
		}
		ff.CurrentPhase = feature.PhasePlan
		if roadmapPhase > 0 {
			ff.CurrentRoadmapPhase = roadmapPhase
		}
		ff.CurrentPhaseStatus = ""
		ff.CurrentIteration = 0
		ff.PlanIteration = 0
		ff.ValidatingPlan = false
		ff.ValidatorStatuses = nil
		if ff.MaxPlanIterations < minPlanAttempts {
			ff.MaxPlanIterations = minPlanAttempts
		}
		ff.PendingReviewPhase = nil
		ff.PendingRewindReviewRoadmapPhase = nil
		if ff.Artifacts != nil {
			delete(ff.Artifacts, "plan")
		}
		return nil
	})
}

// onMultiReposPassed handles the success branch of multi-repo implement.
//
// Idempotent: if the feature has already been advanced out of
// StatusImplementing (e.g. because a sibling completion trigger ran first),
// return nil so the async phase-supervisor result path is a safe no-op.
// Without this guard, a double-call would fail CompleteImplementation's
// invalid transition check, propagate as an error, and cause
// surfaceDispatchCompletionError to wrongly mark the feature Failed.
func (o *Orchestrator) onMultiReposPassed(featureID string, f *feature.Feature) error {
	if f.Status != feature.StatusImplementing {
		return nil
	}
	if err := o.deps.Lifecycle.CompleteImplementation(featureID); err != nil {
		return fmt.Errorf("complete implementation: %w", err)
	}
	o.emitPhaseCompleted(featureID, feature.PhaseImplement, nil)

	// Roadmap mid-flight: commit + advance.
	if f.CurrentRoadmapPhase > 0 && f.CurrentRoadmapPhase < f.TotalRoadmapPhases {
		anchors := o.commitRoadmapPhase(f)
		if err := o.recordRoadmapPhaseCommitAnchors(featureID, f.CurrentRoadmapPhase, anchors); err != nil {
			return err
		}
		if err := o.deps.Lifecycle.AdvanceRoadmapPhase(featureID); err != nil {
			return fmt.Errorf("advance roadmap phase: %w", err)
		}
		startedPhase, started, err := o.startPhase(featureID, feature.PhasePlan)
		if err != nil {
			return err
		}
		if started {
			o.emitEvent(ports.Event{Type: ports.FeatureAdvanced, FeatureID: featureID, Phase: startedPhase})
		}
		return nil
	}

	// Roadmap final phase: commit + fall through to review / publish.
	if f.CurrentRoadmapPhase > 0 && f.CurrentRoadmapPhase == f.TotalRoadmapPhases {
		anchors := o.commitRoadmapPhase(f)
		if err := o.recordRoadmapPhaseCommitAnchors(featureID, f.CurrentRoadmapPhase, anchors); err != nil {
			return err
		}
	}

	// Deferred end-of-feature Final Review. When the implementation pass
	// completed leaving repos staged for FR and the pipeline runs Final
	// Review, dispatch the FR pass synchronously here before marking code
	// ready or auto-publishing.
	if !f.EffectivePipeline().ShouldSkipFinalReview() && reposNeedFinalReview(f) {
		if frErr := o.runDeferredFinalReview(featureID); frErr != nil {
			if errors.Is(frErr, errFinalReviewInterrupted) {
				// User pressed Stop during Final Review. InterruptFeature
				// owns the terminal StatusInterrupted transition; do not
				// proceed to MarkCodeReady / auto-publish on a feature that
				// the user explicitly stopped.
				return nil
			}
			return frErr
		}
	}

	return o.advanceAfterFinalReview(featureID)
}

// advanceAfterFinalReview runs the MarkCodeReady / auto-publish flow that
// follows a successful Final Review. Extracted from onMultiReposPassed so the
// restart-from-interrupted-FR path (startFinalReview) can re-enter the same
// post-FR advancement after re-running the FR pass.
func (o *Orchestrator) advanceAfterFinalReview(featureID string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("reload after final review: %w", err)
	}
	if f.Status == feature.StatusFailed || f.HasTerminalFailure() {
		errMsg := strings.TrimSpace(f.LastError)
		if errMsg == "" {
			errMsg = "final review did not complete successfully"
		}
		return fmt.Errorf("final review did not complete successfully: %s", errMsg)
	}

	// Child features never deliver: a successful final review enters the
	// explicit local-integration stage instead of CodeReady, publication, or
	// any child delivery path.
	if f.IsChild() {
		return o.RunChildIntegration(featureID)
	}

	if !f.IsPublishable() || !f.Checkpoints.AutoPublish() {
		if err := o.deps.Lifecycle.MarkCodeReady(featureID); err != nil {
			return fmt.Errorf("mark code ready: %w", err)
		}
		return nil
	}

	// Non-roadmap, multi-repo auto-publish: now that every touched repo is
	// past review, try to complete the feature-level publish. If the feature
	// is not yet fully published (e.g. a repo publish failed or is still
	// pending), fall back to MarkCodeReady so startup and resume paths can
	// recover partially published features.
	//
	// When tryCompleteAndEmit reports published==true, the feature-level
	// publish has just completed as a direct consequence of this handler
	// finishing the cross-repo join. The phase-sequencing event contract
	// requires emitting PublishCompleted (and firing OnPublishCompleted) on
	// every publish-completion site, not only the Publish() pipeline.
	if f.CurrentRoadmapPhase == 0 {
		publishRepoFn := o.publishRepoFn
		if publishRepoFn == nil {
			publishRepoFn = o.publishRepo
		}
		for _, name := range f.TouchedRepos() {
			st := f.RepoStates[name]
			if st != nil && st.PRURL != "" {
				continue
			}
			if repo, ok := findRepo(f, name); ok {
				workDir := repo.WorktreePath
				if workDir == "" {
					workDir = repo.Path
				}
				if err := o.scrubFinalReviewRootArtifacts(context.Background(), workDir); err != nil {
					_ = o.deps.Lifecycle.SetRepoPublishError(featureID, name, err.Error())
					continue
				}
			}
			_, _ = publishRepoFn(featureID, name)
		}
		published, err := o.tryCompleteAndEmit(featureID)
		if err != nil {
			return err
		}
		if !published {
			if err := o.deps.Lifecycle.MarkCodeReady(featureID); err != nil {
				return fmt.Errorf("mark code ready: %w", err)
			}
			return nil
		}
		prURLs := make(map[string]string)
		if freshF, freshErr := o.deps.Lifecycle.Get(featureID); freshErr == nil && freshF != nil {
			for _, r := range freshF.Repos {
				if st := freshF.RepoStates[r.Name]; st != nil && st.PRURL != "" {
					prURLs[r.Name] = st.PRURL
				}
			}
		}
		o.emitEventBlocking(ports.Event{
			Type:      ports.PublishCompleted,
			FeatureID: featureID,
		})
		if o.hooks.OnPublishCompleted != nil {
			o.hooks.OnPublishCompleted(featureID, prURLs, nil)
		}
		return nil
	}

	// Roadmap final multi-repo auto-publish: mark code ready, then route
	// through the full Publish pipeline so PublishStarted/PublishCompleted
	// events + hooks fire and per-repo errors / conflicts propagate to
	// callers rather than being silently swallowed.
	if err := o.deps.Lifecycle.MarkCodeReady(featureID); err != nil {
		return fmt.Errorf("mark code ready: %w", err)
	}
	if err := o.scrubFinalReviewRootArtifactsForFeature(context.Background(), f); err != nil {
		return err
	}
	publishFn := o.publishFn
	if publishFn == nil {
		publishFn = o.Publish
	}
	return publishFn(featureID)
}

func (o *Orchestrator) scrubFinalReviewRootArtifactsForFeature(ctx context.Context, f *feature.Feature) error {
	if f == nil {
		return nil
	}
	for _, name := range f.TouchedRepos() {
		repo, ok := findRepo(f, name)
		if !ok {
			continue
		}
		workDir := repo.WorktreePath
		if workDir == "" {
			workDir = repo.Path
		}
		if err := o.scrubFinalReviewRootArtifacts(ctx, workDir); err != nil {
			return fmt.Errorf("scrub final review artifacts for repo %s: %w", name, err)
		}
	}
	return nil
}

func (o *Orchestrator) scrubFinalReviewRootArtifacts(ctx context.Context, workDir string) error {
	if strings.TrimSpace(workDir) == "" || o.deps.CmdRunner == nil {
		return nil
	}
	for _, name := range finalReviewRootOrchestrationArtifacts {
		path := filepath.Join(workDir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("stat final review artifact candidate %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		out, err := o.deps.CmdRunner.Run(ctx, "git",
			[]string{"-C", workDir, "ls-files", "--others", "--exclude-standard", "--", name},
			ports.CommandOpts{})
		if err != nil {
			return fmt.Errorf("checking whether %s is untracked: %w", path, err)
		}
		if strings.TrimSpace(string(out)) != name {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove untracked final review artifact %s: %w", path, err)
		}
	}
	return nil
}

// commitRoadmapPhase creates per-repo commits for a completed roadmap phase
// and returns the full HEAD SHA for each repo whose commit-or-anchor operation
// succeeded. Commit failures remain advisory for pipeline progress, but failed
// repos are omitted from the returned anchor map.
func (o *Orchestrator) commitRoadmapPhase(f *feature.Feature) map[string]string {
	if len(f.Repos) == 0 || o.deps.Publisher == nil {
		return nil
	}
	phaseName := ""
	if roadmapPath := o.resolveArtifactPath(f, "roadmap"); roadmapPath != "" {
		if data, err := os.ReadFile(roadmapPath); err == nil {
			if phases, err := agent.ParseRoadmap(string(data)); err == nil {
				for _, p := range phases {
					if p.Number == f.CurrentRoadmapPhase {
						phaseName = p.Name
						break
					}
				}
			}
		}
	}
	msg := fmt.Sprintf("Phase %d/%d (%s)", f.CurrentRoadmapPhase, f.TotalRoadmapPhases, f.RoadmapPhaseType)
	if phaseName != "" {
		msg += ": " + phaseName
	}
	msg += "\n\nFeature: " + f.Slug
	anchors := make(map[string]string)
	for _, repo := range f.Repos {
		if repo.WorktreePath == "" {
			continue
		}
		sha, err := o.deps.Publisher.CommitAllAndGetHead(repo.WorktreePath, msg)
		if err != nil || sha == "" {
			continue
		}
		anchors[repo.Name] = sha
	}
	if len(anchors) == 0 {
		return nil
	}
	return anchors
}

func (o *Orchestrator) recordRoadmapPhaseCommitAnchors(featureID string, phase int, anchors map[string]string) error {
	if len(anchors) == 0 || o.deps.Lifecycle == nil {
		return nil
	}
	if err := o.deps.Lifecycle.RecordRoadmapPhaseCommitAnchors(featureID, phase, anchors); err != nil {
		return fmt.Errorf("record roadmap phase commit anchors: %w", err)
	}
	return nil
}

// hasActiveRepoCycle returns true when any per-repo cycle state has a
// non-empty cycle Type. Per-repo cycles are driven by f.RepoCycles and must
// route through their own completion handlers.
func hasActiveRepoCycle(f *feature.Feature) bool {
	for _, c := range f.RepoCycles {
		if c != nil && c.Type != "" {
			return true
		}
	}
	return false
}

// reposNeedFinalReview returns true when at least one repo was touched by
// the implement pass and is not yet published — i.e. it is staged for the
// deferred end-of-feature Final Review. Repos with a non-empty PRURL have
// already shipped and FR is a no-op for them; if every touched repo is
// already published the FR pass is skipped.
func reposNeedFinalReview(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	for _, name := range f.TouchedRepos() {
		st := f.RepoStates[name]
		if st != nil && st.PRURL == "" {
			return true
		}
	}
	return false
}

// runDeferredFinalReview transitions the feature into StatusFinalReviewing
// and dispatches RunMultiRepoFinalReview synchronously. Blocks until the FR
// pass returns. On success leaves the feature back at StatusReviewPassed (so
// the trailing MarkCodeReady / auto-publish path in onMultiReposPassed
// remains unchanged). On failure marks the feature failed and returns the
// error so the caller short-circuits.
func (o *Orchestrator) runDeferredFinalReview(featureID string) error {
	resultCh, err := o.startDeferredFinalReview(featureID)
	if err != nil {
		return err
	}
	res, ok := <-resultCh
	return o.finishDeferredFinalReviewResult(featureID, res, ok)
}

func (o *Orchestrator) startDeferredFinalReview(featureID string) (chan *agent.OrchestratorResult, error) {
	if err := o.deps.Lifecycle.MarkFinalReviewReady(featureID); err != nil {
		return nil, fmt.Errorf("mark final review ready: %w", err)
	}
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return nil, fmt.Errorf("load feature for final review: %w", err)
	}

	runFn := o.runMultiRepoFinalReviewFn
	if runFn == nil {
		errMsg := "runMultiRepoFinalReviewFn not configured"
		o.emitPhaseCompleted(featureID, feature.PhaseFinalReview, errors.New(errMsg))
		return nil, o.markFinalReviewFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg)
	}
	resultCh, err := runFn(f, o.computeKBInfos(f)...)
	if err != nil {
		errMsg := fmt.Sprintf("dispatch final review: %v", err)
		o.emitPhaseCompleted(featureID, feature.PhaseFinalReview, errors.New(errMsg))
		return nil, o.markFinalReviewFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg)
	}
	if resultCh == nil {
		errMsg := "dispatch final review returned no result channel"
		o.emitPhaseCompleted(featureID, feature.PhaseFinalReview, errors.New(errMsg))
		return nil, o.markFinalReviewFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg)
	}
	return resultCh, nil
}

func (o *Orchestrator) finishDeferredFinalReviewResult(featureID string, res *agent.OrchestratorResult, ok bool) error {
	if !ok || res == nil {
		errMsg := "final review returned no result"
		o.emitPhaseCompleted(featureID, feature.PhaseFinalReview, errors.New(errMsg))
		return o.markFinalReviewFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg)
	}
	switch res.FinalStatus {
	case finalStatusAllPassed:
		// Roll status back to ReviewPassed so the trailing MarkCodeReady /
		// auto-publish path in onMultiReposPassed sees the same precondition
		// as before the FR detour.
		if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			return ff.Transition(feature.StatusReviewPassed)
		}); err != nil {
			return fmt.Errorf("revert to review_passed after final review: %w", err)
		}
		o.emitPhaseCompleted(featureID, feature.PhaseFinalReview, nil)
		return nil
	case finalStatusInterrupted:
		// Shutdown handler will land the feature in StatusInterrupted; nothing
		// to do here beyond surfacing the phase-completed event. Return the
		// sentinel so onMultiReposPassed and surfaceDispatchCompletionError
		// can recognise the interrupt and skip the failure path — without
		// this the trailing MarkCodeReady / auto-publish would either fail
		// on the Interrupted feature or surface as a spurious "Failed" with
		// failure_type=infrastructure.
		o.emitPhaseCompleted(featureID, feature.PhaseFinalReview, nil)
		return errFinalReviewInterrupted
	case finalStatusPlanRevisionRequired:
		errMsg := "final review requested unsupported phase-plan revision"
		o.emitPhaseCompleted(featureID, feature.PhaseFinalReview, errors.New(errMsg))
		return o.markFinalReviewFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg)
	case "failed":
		errMsg := res.LastError
		if errMsg == "" {
			if len(res.FailedRepos) > 0 {
				errMsg = fmt.Sprintf("final review failed for repos: %s", strings.Join(res.FailedRepos, ", "))
			} else {
				errMsg = "final review failed"
			}
		}
		failureType := feature.FailureInfrastructure
		for _, status := range res.RepoStatuses {
			switch status {
			case "protocol_violation":
				failureType = feature.FailureProtocolViolation
			case "max_iterations":
				failureType = feature.FailureMaxIterations
			}
			if failureType == feature.FailureProtocolViolation {
				break
			}
		}
		o.emitPhaseCompleted(featureID, feature.PhaseFinalReview, errors.New(errMsg))
		return o.markFinalReviewFailedWithEvent(featureID, failureType, errMsg)
	default:
		errMsg := fmt.Sprintf("unknown final review FinalStatus %q", res.FinalStatus)
		o.emitPhaseCompleted(featureID, feature.PhaseFinalReview, errors.New(errMsg))
		return o.markFinalReviewFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg)
	}
}

func (o *Orchestrator) markFinalReviewFailedWithEvent(featureID, failureType, errMsg string) error {
	if err := o.markFailedWithEvent(featureID, failureType, errMsg); err != nil {
		return err
	}
	return errors.New(errMsg)
}

// Completion repo status values enumerate the server-authored completion
// status per repo.
const (
	completionStatusEligible         = "eligible"
	completionStatusAlreadyPublished = "already_published"
	completionStatusCompleted        = "completed"
	completionStatusIneligible       = "ineligible"
	completionStatusUntouched        = "untouched"
	completionStatusBlocked          = "blocked"
)

// CompletionRepoResult is the per-repository slice of a completion preflight.
type CompletionRepoResult struct {
	Repo        string
	Publishable bool
	Touched     bool
	Status      string
	PRURL       string
	Blocker     string
	Freshness   string
	LastError   string
	BaseBranch  string
	Branch      string
}

// repoPublishable reports whether repo is publishable. A nil Publishable
// pointer is treated as publishable for backward compatibility with older
// feature manifests that predate the explicit flag.
func repoPublishable(repo feature.FeatureRepo) bool {
	return repo.Publishable == nil || *repo.Publishable
}

// CompletionPreflightResult is the feature-wide, side-effect-free completion
// preview. SourceRevision captures the worktree state of every repository;
// a publish/merge/mark-done mutation carrying a mismatched SourceRevision is
// rejected as stale.
type CompletionPreflightResult struct {
	FeatureID       string
	SourceRevision  string
	CanMarkDone     bool
	MarkDoneBlocker string
	Repos           []CompletionRepoResult
}

// CompletionPreflight computes a side-effect-free preview of a feature's
// completion readiness. It enumerates every repository with its completion
// status, PR URL, blockers, and freshness, and returns a source revision
// that mutations check for staleness. The worktree is never mutated.
func (o *Orchestrator) CompletionPreflight(featureID string) (CompletionPreflightResult, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return CompletionPreflightResult{}, fmt.Errorf("load feature: %w", err)
	}
	result := CompletionPreflightResult{FeatureID: featureID}
	prURLs := f.PRURLs()
	for _, repo := range f.Repos {
		state := f.RepoStates[repo.Name]
		publishable := repoPublishable(repo)
		repoResult := CompletionRepoResult{
			Repo:        repo.Name,
			Publishable: publishable,
			BaseBranch:  repo.BaseBranch,
			Branch:      repo.Branch,
		}
		if state != nil {
			repoResult.Touched = state.Touched
			repoResult.PRURL = state.PRURL
			repoResult.LastError = safeCompletionTruncate(state.LastError, 200)
		}
		if prURLs[repo.Name] != "" {
			repoResult.PRURL = prURLs[repo.Name]
		}
		repoResult.Status = completionRepoStatus(f, repo, state, publishable, repoResult.PRURL)
		freshness, blocker, _ := o.repoFreshnessAndBlocker(o.harnessRebaseOutcomeForRepo(f, repo))
		repoResult.Freshness = freshness
		repoResult.Blocker = blocker
		if repoResult.Blocker != "" {
			repoResult.Status = completionStatusBlocked
		}
		result.Repos = append(result.Repos, repoResult)
	}
	result.CanMarkDone, result.MarkDoneBlocker = completionCanMarkDone(f)
	result.SourceRevision = preflightRevision(o.collectPreflightFingerprints(f, nil))
	return result, nil
}

// CompletionPreflightSourceRevision recomputes the current completion preview
// revision so mutation adapters can reject stale renderer snapshots before any
// side effect.
func (o *Orchestrator) CompletionPreflightSourceRevision(featureID string) (string, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("load feature: %w", err)
	}
	return preflightRevision(o.collectPreflightFingerprints(f, nil)), nil
}

func completionRepoStatus(f *feature.Feature, repo feature.FeatureRepo, state *feature.RepoState, publishable bool, prURL string) string {
	if state != nil && state.Touched {
		if !publishable {
			if f.Status == feature.StatusDone {
				return completionStatusCompleted
			}
			return completionStatusEligible
		}
		if prURL != "" {
			if f.Status == feature.StatusDone {
				return completionStatusCompleted
			}
			return completionStatusAlreadyPublished
		}
		return completionStatusEligible
	}
	if !publishable {
		return completionStatusIneligible
	}
	return completionStatusUntouched
}

func completionCanMarkDone(f *feature.Feature) (bool, string) {
	if f.Status == feature.StatusDone {
		return false, "feature is already done"
	}
	if f.Status == feature.StatusImplementing || f.Status == feature.StatusPlanning || f.Status == feature.StatusInquiring {
		return false, "feature is not ready for completion"
	}
	if f.Status == feature.StatusCodeReady || f.Status == feature.StatusReviewPassed || f.Status == feature.StatusPublished {
		return true, ""
	}
	if f.Status == feature.StatusFailed {
		return false, "feature is in a failed state"
	}
	return false, "feature status does not allow mark-done"
}

// RepositoryDiffResult is the bounded, lazy diff inspection for one repository.
type RepositoryDiffResult struct {
	FeatureID       string
	Repo            string
	SourceRevision  string
	Files           []RepositoryDiffFileResult
	Truncated       bool
	FileDiff        string
	FileTruncated   bool
	FileBinary      bool
	FileUnavailable bool
	PartialFailure  string
}

// RepositoryDiffFileResult is one changed file in a repository diff.
type RepositoryDiffFileResult struct {
	Path         string
	OldPath      string
	Operation    string
	AddedLines   int
	RemovedLines int
	Binary       bool
	Fingerprint  string
}

// MaxDiffFiles is the aggregate limit on changed files returned.
const MaxDiffFiles = 500

// MaxDiffContent is the per-request limit on single-file diff content.
const MaxDiffContent = 64 * 1024

// RepositoryDiff inspects one repository's working-tree diff. Without a
// filePath, it lists all changed files with summaries. With a filePath, it
// returns bounded diff content for that single file. It never exposes raw
// host paths, credentials, or unbounded content.
func (o *Orchestrator) RepositoryDiff(featureID, repoName, filePath string) (RepositoryDiffResult, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return RepositoryDiffResult{}, fmt.Errorf("load feature: %w", err)
	}
	repo, ok := findRepo(f, repoName)
	if !ok {
		return RepositoryDiffResult{FeatureID: featureID, Repo: repoName, PartialFailure: "repository not found"}, nil
	}
	workDir := repoWorkDir(repo)
	if workDir == "" {
		return RepositoryDiffResult{FeatureID: featureID, Repo: repoName, PartialFailure: "worktree not available"}, nil
	}
	result := RepositoryDiffResult{
		FeatureID:      featureID,
		Repo:           repoName,
		SourceRevision: preflightRevision(o.collectPreflightFingerprints(f, nil)),
	}
	if filePath != "" {
		return o.repositorySingleFileDiff(result, workDir, repo.BaseBranch, filePath)
	}
	return o.repositoryFileListDiff(result, workDir, repo.BaseBranch)
}

// RepositoryWorktreePath resolves the server-owned worktree target for a
// feature repository and canonicalizes it for the desktop main process. The
// renderer never receives this value.
func (o *Orchestrator) RepositoryWorktreePath(featureID, repoName string) (string, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("load feature: %w", err)
	}
	repo, ok := findRepo(f, repoName)
	if !ok {
		return "", fmt.Errorf("repository %q not found", repoName)
	}
	workDir := repoWorkDir(repo)
	if workDir == "" {
		return "", errors.New("worktree not available")
	}
	real, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve worktree: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("stat worktree: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("worktree target is not a directory")
	}
	return real, nil
}

func (o *Orchestrator) repositoryFileListDiff(result RepositoryDiffResult, workDir, baseBranch string) (RepositoryDiffResult, error) {
	previews, err := o.diffOperator().BranchDiffPreviews(workDir, baseBranch)
	if err != nil {
		result.PartialFailure = safeCompletionTruncate(err.Error(), 200)
		return result, nil
	}
	for i, p := range previews {
		if i >= MaxDiffFiles {
			result.Truncated = true
			break
		}
		result.Files = append(result.Files, RepositoryDiffFileResult{
			Path:         p.Path,
			OldPath:      p.OldPath,
			Operation:    p.Operation,
			AddedLines:   p.AddedLines,
			RemovedLines: p.RemovedLines,
			Binary:       isBinaryPatch(p.Patch),
			Fingerprint:  p.Fingerprint,
		})
	}
	return result, nil
}

func (o *Orchestrator) repositorySingleFileDiff(result RepositoryDiffResult, workDir, baseBranch, filePath string) (RepositoryDiffResult, error) {
	preview, err := o.diffOperator().SingleFileDiffPreview(workDir, baseBranch, filePath)
	if err != nil {
		result.PartialFailure = safeCompletionTruncate(err.Error(), 200)
		return result, nil
	}
	if preview == nil {
		result.FileUnavailable = true
		return result, nil
	}
	if isBinaryPatch(preview.Patch) {
		result.FileBinary = true
		return result, nil
	}
	diff := preview.Patch
	if len(diff) > MaxDiffContent {
		diff = safeCompletionTruncate(diff, MaxDiffContent)
		result.FileTruncated = true
	}
	result.FileDiff = diff
	return result, nil
}

func (o *Orchestrator) diffOperator() ports.DiffOperator {
	if o != nil && o.deps.Differ != nil {
		return o.deps.Differ
	}
	return &git.DiffAdapter{}
}

func isBinaryPatch(patch string) bool {
	return strings.Contains(patch, "Binary files") || strings.Contains(patch, "GIT binary patch")
}

func safeCompletionTruncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.ValidString(s[:max]) {
		max--
	}
	return s[:max]
}
