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

package tui

import (
	"context"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// orchestratorAPI is the TUI-local surface of *orchestrator.Orchestrator.
// The TUI stores the orchestrator as this interface so tests can supply
// hand-written doubles without importing the full orchestrator stack.
type orchestratorAPI interface {
	// --- Feature lifecycle ---------------------------------------------
	CreateFeature(name, description string, repos []string, models config.ModelConfig,
		exitCriteria, inquireness string, images []string,
		opts ...feature.CreateOptions) (*feature.Feature, error)
	InterruptFeature(featureID string) error
	InterruptAllRunning() error
	Delete(featureID string) error
	Events() <-chan ports.Event
	Done() <-chan struct{}

	// --- Phase lifecycle -----------------------------------------------
	StartFeature(featureID string) error
	HandlePhaseCompletion(featureID string, input orchestrator.PhaseCompletionInput) error
	HandleReviewDecision(featureID string, d orchestrator.ReviewDecision) error
	// ProceedFromRewindReview confirms an already-performed rewind and
	// dispatches the target phase. The TUI's rewindCmd already invoked
	// Lifecycle.RewindToPhase before opening the rewind-artifact-review;
	// this entry point performs only the post-rewind work (clear gate,
	// read description-review.md, CompletePlanning for Implement, dispatch).
	// `target` must be the effective target propagated from RewindDoneMsg.
	ProceedFromRewindReview(featureID string, target feature.Phase) error
	// HandleNeedUserInputDecision routes a feature- or cycle-scoped
	// need-user-input gate decision (resume/abort).
	HandleNeedUserInputDecision(featureID string, d orchestrator.NeedUserInputDecision) error
	Publish(featureID string) error
	StartMultiRepoImplementation(featureID string) error

	// --- Recovery ------------------------------------------------------
	ScanRecovery(ctx context.Context) ([]ports.RecoveryItem, error)
	ExecuteRecovery(ctx context.Context, items []ports.RecoveryItem, actions map[string]ports.RecoveryAction) error

	// --- Failure escape hatch ------------------------------------------
	MarkFailed(featureID, failureType, errorMsg string) error

	// --- Session walks -------------------------------------------------
	// StopFeatureSessions halts every active session owned by featureID so
	// the caller can mutate lifecycle state without racing an orphan agent.
	StopFeatureSessions(featureID string)

	// --- Tweak lifecycle -----------------------------------------------
	// Tweak is feature-level: one interactive session mounts every
	// Feature.Repos worktree, then the orchestrator commits and pushes
	// every modified repo at session end. PullRebase conflicts surface a
	// structured PublishConflictError carrying the affected repo so the
	// TUI can route into a fresh CycleRebase via RebaseRepoCycleResultMsg.
	StartTweak(featureID string) (sessionID string, err error)
	CompleteTweakCommit(featureID string) (hadChanges bool, err error)
	CompleteTweakFinish(featureID string, hadChanges bool) error
	RestoreTweakFromReview(featureID string) error
	FailTweakSession(featureID string) error

	// --- Rebase lifecycle ----------------------------------------------
	StartRebase(featureID, repoName string) error
	HandleRebaseResult(featureID, repoName string, input orchestrator.RebaseResultInput) error
	ForcePushAfterConflict(featureID, repoName string) error

	// --- Repo-cycle lifecycle ------------------------------------------
	DispatchRepoCycle(featureID, repoName string, cycleType feature.RepoCycleType, planContent string) (sessionID string, err error)
	StartRepoCycleImplement(featureID, repoName string, cycleType feature.RepoCycleType, planContent string, conflictFiles ...string) (sessionID string, err error)
	HandleRepoCycleLoopDone(featureID string, input orchestrator.RepoCycleLoopResultInput) error
	// StartCycleFinalReview launches the unified feature-level Final Review
	// for a post-publish cycle. Every Feature.Repos entry is reviewed
	// atomically against the cumulative diff.
	StartCycleFinalReview(featureID string) error
	CompleteRepoCycle(featureID, repoName string) error

	// --- Refactor cycle ------------------------------------------------
	StartRefactorCycle(featureID, repoName, prompt string) (sessionID string, err error)
	RestartRefactorCycle(featureID, repoName, prompt string) (sessionID string, err error)
	CompleteRefactorRepoCycle(featureID, repoName string) error

	// --- Lifecycle delegates -------------------------------------------
	// Thin pass-throughs that replace direct featureManager.* mutations
	// from TUI Update-dispatched handlers so all state changes go through
	// a single orchestrator chokepoint.
	TryCompletePublish(featureID string) (bool, error)
	MarkPublished(featureID, prURL string) error
	MarkDone(featureID string) error
	SetRepoPublished(featureID, repoName, prURL string) error
	SetRepoPublishError(featureID, repoName, errMsg string) error
	RecordPublishUIFailure(featureID, errMsg string) error
	ReportMissingArtifactFailure(featureID, errMsg string) error
	RemoveRepoCycle(featureID, repoName string) error
	CommitUncommittedForPublish(featureID string) error
	TransitionTo(featureID string, status feature.Status) error
	ClearRepoCycles(featureID string) error
	// RetryPhase clears feature-level error state so the unified
	// phase-implement loop can re-run the active phase from iteration 1.
	RetryPhase(featureID string) error
	ClearPendingHelpAndPermissions(featureID string) error
	SetDesignReady(featureID string) error
	MergeFeatureLocal(featureID string) error
	UpgradePipeline(featureID string, profile feature.PipelineProfile) error
	RewindToPhase(featureID string, targetPhase feature.Phase) ([]string, feature.Phase, error)
	RewindWithRequest(featureID string, request feature.RewindRequest) ([]string, feature.Phase, error)
	CleanWorktree(featureID string) error
	SaveFeatureSummary(featureID, summary string) error
	AdvanceRoadmapPhase(featureID string) error
	StartRoadmapPhaseImplementation(featureID string) error
	StartPlanning(featureID string) error
	CompletePlanning(featureID string) error
	CompleteImplementation(featureID string) error
	MarkCodeReady(featureID string) error
	NeedsPlanReview(featureID string) error
	ClearAddressingReviews(featureID string) error
	SetTotalRoadmapPhases(featureID string, count int) error
	BumpPlanIterationsBudget(featureID string, delta int) error
	ResetPlanStatusForRoadmap(featureID string, budgetBump int) error
	RecordRoadmapRejection(featureID, feedback string)
	ParseRoadmapAndPersistCount(featureID string) (int, error)
	// PopulateExecutionPlanForPhase / PopulateLegacyExecutionPlan removed in
	// SchemaVersionCurrent = 3; the per-phase execution-order.yaml is now
	// read fresh from disk by the orchestrator.
	CommitRoadmapPhase(featureID string, phase int) error

	// --- Restart / review-gate mutators --------------------------------
	// These methods keep Store.Modify ownership in the orchestrator so
	// restartPhaseCmd, applyRefactorPipelineAndStart, and triggerReviewGateCmd
	// remain thin TUI delegates.
	ApplyRefactorPipeline(featureID string, profile feature.PipelineProfile) error
	EnterReviewGate(featureID string, targetPhase feature.Phase) error
	ResetToPublishedFromTweak(featureID string) error
	ExtendFailedPhaseBudget(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) error
	CollectAndClearRepoCycleRestarts(featureID string) ([]orchestrator.RepoCycleRestart, *orchestrator.RefactorRestart, error)

	// --- Restart / review-gate dispatch --------------------------------
	// These entry points own the restart state machine and gate-review
	// artifact/workdir assembly.
	RestartPhase(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) (orchestrator.RestartOutcome, error)
	ResolveGateReviewContext(featureID string, targetPhase feature.Phase) (orchestrator.GateReviewContext, error)
	ResolveRewindReviewContext(featureID string, targetPhase feature.Phase) (orchestrator.RewindReviewContext, error)

	// --- Feature config editing ----------------------------------------
	// UpdateFeatureConfig atomically validates quiescence and writes the
	// three editable per-feature config axes under Store.mu.
	UpdateFeatureConfig(featureID string, input orchestrator.UpdateFeatureConfigInput) error
}

// Compile-time assertion: the concrete orchestrator satisfies the TUI seam.
var _ orchestratorAPI = (*orchestrator.Orchestrator)(nil)
