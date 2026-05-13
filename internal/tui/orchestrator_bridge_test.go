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
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// ---------------------------------------------------------------------------
// Fake orchestrator implementing the TUI-local orchestratorAPI interface.
// ---------------------------------------------------------------------------

type createCall struct {
	name         string
	description  string
	repos        []string
	models       config.ModelConfig
	exitCriteria string
	inquireness  string
	images       []string
	opts         []feature.CreateOptions
}

type fakeOrch struct {
	createCalls    []createCall
	interruptIDs   []string
	deleteIDs      []string
	interruptAllRc int
	events         chan ports.Event
	done           chan struct{}

	createReturn *feature.Feature
	createErr    error

	// Phase 8 tracking — simple call recording.
	startFeatureIDs           []string
	handlePhaseCompletionArgs []struct {
		FeatureID string
		Input     orchestrator.PhaseCompletionInput
	}
	handleReviewDecisionArgs []struct {
		FeatureID string
		D         orchestrator.ReviewDecision
	}
	publishIDs             []string
	onRepoStatusArgs       []struct{ FeatureID, RepoName string }
	multiRepoImplIDs       []string
	scanRecoveryCalls      int
	executeRecoveryCalls   int
	executeRecoveryActs    []map[string]ports.RecoveryAction
	markFailedCalls        []struct{ FeatureID, Type, Msg string }
	stopFeatureSessionsIDs []string
	scanRecoveryReturn     []ports.RecoveryItem
	scanRecoveryErr        error
	executeRecoveryErr     error
	publishErr             error
	startFeatureErr        error
	handlePhaseCompleteErr error
	handleReviewDecErr     error

	// Tweak
	startTweakSessionIDs         []string
	startTweakRepoCycleArgs      []struct{ FeatureID, RepoName string }
	completeTweakCommitIDs       []string
	completeTweakMultiCommitArgs []struct{ FeatureID, RepoName string }
	completeTweakFinishArgs      []struct {
		FeatureID  string
		HadChanges bool
	}
	completeTweakMultiFinishArgs []struct {
		FeatureID, RepoName string
		HadChanges          bool
	}
	restoreTweakFromReviewIDs       []string
	restoreTweakMultiFromReviewArgs []struct{ FeatureID, RepoName string }
	restoreTweakFeatureIDs          []string
	tweakSessionReturn              string
	tweakCommitReturn               bool
	tweakErr                        error

	// Rebase
	startRebaseIDs           []string
	startRebaseRepoCycleArgs []struct{ FeatureID, RepoName string }
	handleRebaseResultArgs   []struct {
		FeatureID string
		Input     orchestrator.RebaseResultInput
	}
	handleRebaseRepoCycleResultArgs []struct {
		FeatureID, RepoName string
		Input               orchestrator.RebaseResultInput
	}
	startRebaseImplementationIDs []string
	completeRebaseIDs            []string
	forcePushArgs                []struct{ FeatureID, RepoName string }
	resumeAfterConflictArgs      []struct{ FeatureID, RepoName string }
	completeRebaseAutoPublish    bool
	rebaseErr                    error

	// Review comments
	startReviewCommentsArgs []struct {
		FeatureID string
		Comments  []ports.ReviewComment
		Mode      string
	}

	// Cycles
	dispatchRepoCycleArgs []struct {
		FeatureID, RepoName string
		CycleType           feature.RepoCycleType
		PlanContent         string
	}
	startRepoCycleImplementArgs []struct {
		FeatureID, RepoName string
		CycleType           feature.RepoCycleType
		PlanContent         string
	}
	startRepoCycleImplementConflictFiles [][]string
	handleRepoCycleLoopDoneArgs          []struct {
		FeatureID string
		Input     orchestrator.RepoCycleLoopResultInput
	}
	startCycleFinalReviewArgs []struct{ FeatureID string }
	completeRepoCycleArgs     []struct{ FeatureID, RepoName string }
	repoCycleSessionReturn    string
	repoCycleErr              error

	// Refactor
	startRefactorCycleArgs        []struct{ FeatureID, RepoName, Prompt string }
	restartRefactorCycleArgs      []struct{ FeatureID, RepoName, Prompt string }
	completeRefactorRepoCycleArgs []struct{ FeatureID, RepoName string }
	refactorSessionReturn         string
	refactorErr                   error

	// Lifecycle delegates (Phase 8 iter 3).
	lifecycleCalls      []string
	lifecycleErr        error
	parsedRoadmapPhases int
	publishPublished    bool

	// Phase 8 iter 13 — restart + gate-review consolidation.
	restartPhaseArgs []struct {
		FeatureID                                  string
		MaxIterationsDelta, MaxPlanIterationsDelta int
	}
	restartPhaseReturn    orchestrator.RestartOutcome
	resolveGateReviewArgs []struct {
		FeatureID   string
		TargetPhase feature.Phase
	}
	resolveGateReviewReturn orchestrator.GateReviewContext
	resolveRewindReviewArgs []struct {
		FeatureID   string
		TargetPhase feature.Phase
	}
	resolveRewindReviewReturn orchestrator.RewindReviewContext

	// Records UpdateFeatureConfig calls for edit-config overlay tests.
	updateFeatureConfigArgs []struct {
		FeatureID string
		Input     orchestrator.UpdateFeatureConfigInput
	}
	updateFeatureConfigErr error
}

func newFakeOrch() *fakeOrch {
	return &fakeOrch{
		events:       make(chan ports.Event, 16),
		done:         make(chan struct{}),
		createReturn: &feature.Feature{ID: "fake"},
	}
}

func (f *fakeOrch) CreateFeature(name, description string, repos []string, models config.ModelConfig,
	exitCriteria, inquireness string, images []string,
	opts ...feature.CreateOptions) (*feature.Feature, error) {
	f.createCalls = append(f.createCalls, createCall{
		name:         name,
		description:  description,
		repos:        append([]string(nil), repos...),
		models:       models,
		exitCriteria: exitCriteria,
		inquireness:  inquireness,
		images:       append([]string(nil), images...),
		opts:         append([]feature.CreateOptions(nil), opts...),
	})
	return f.createReturn, f.createErr
}

func (f *fakeOrch) InterruptFeature(id string) error {
	f.interruptIDs = append(f.interruptIDs, id)
	return nil
}

func (f *fakeOrch) InterruptAllRunning() error {
	f.interruptAllRc++
	return nil
}

func (f *fakeOrch) Delete(id string) error {
	f.deleteIDs = append(f.deleteIDs, id)
	return nil
}

func (f *fakeOrch) Events() <-chan ports.Event { return f.events }
func (f *fakeOrch) Done() <-chan struct{}      { return f.done }

// --- Fake Orchestrator Methods ----------------------------------------------

func (f *fakeOrch) StartFeature(featureID string) error {
	f.startFeatureIDs = append(f.startFeatureIDs, featureID)
	return f.startFeatureErr
}

func (f *fakeOrch) HandlePhaseCompletion(featureID string, input orchestrator.PhaseCompletionInput) error {
	f.handlePhaseCompletionArgs = append(f.handlePhaseCompletionArgs, struct {
		FeatureID string
		Input     orchestrator.PhaseCompletionInput
	}{featureID, input})
	return f.handlePhaseCompleteErr
}

func (f *fakeOrch) HandleReviewDecision(featureID string, d orchestrator.ReviewDecision) error {
	f.handleReviewDecisionArgs = append(f.handleReviewDecisionArgs, struct {
		FeatureID string
		D         orchestrator.ReviewDecision
	}{featureID, d})
	return f.handleReviewDecErr
}

func (f *fakeOrch) HandleNeedUserInputDecision(_ string, _ orchestrator.NeedUserInputDecision) error {
	return nil
}

func (f *fakeOrch) Publish(featureID string) error {
	f.publishIDs = append(f.publishIDs, featureID)
	return f.publishErr
}

func (f *fakeOrch) OnRepoStatusChanged(featureID, repoName string, _ string, _ error) error {
	f.onRepoStatusArgs = append(f.onRepoStatusArgs, struct{ FeatureID, RepoName string }{featureID, repoName})
	return nil
}

func (f *fakeOrch) StartMultiRepoImplementation(featureID string) error {
	f.multiRepoImplIDs = append(f.multiRepoImplIDs, featureID)
	return nil
}

func (f *fakeOrch) ScanRecovery(_ context.Context) ([]ports.RecoveryItem, error) {
	f.scanRecoveryCalls++
	return f.scanRecoveryReturn, f.scanRecoveryErr
}

func (f *fakeOrch) ExecuteRecovery(_ context.Context, _ []ports.RecoveryItem, actions map[string]ports.RecoveryAction) error {
	f.executeRecoveryCalls++
	f.executeRecoveryActs = append(f.executeRecoveryActs, actions)
	return f.executeRecoveryErr
}

func (f *fakeOrch) MarkFailed(featureID, failureType, errorMsg string) error {
	f.markFailedCalls = append(f.markFailedCalls, struct{ FeatureID, Type, Msg string }{featureID, failureType, errorMsg})
	return nil
}

func (f *fakeOrch) StopFeatureSessions(featureID string) {
	f.stopFeatureSessionsIDs = append(f.stopFeatureSessionsIDs, featureID)
}

// Tweak --------------------------------------------------------------------

func (f *fakeOrch) StartTweak(featureID string) (string, error) {
	f.startTweakRepoCycleArgs = append(f.startTweakRepoCycleArgs, struct{ FeatureID, RepoName string }{featureID, ""})
	return f.tweakSessionReturn, f.tweakErr
}

func (f *fakeOrch) CompleteTweakCommit(featureID string) (bool, error) {
	f.completeTweakMultiCommitArgs = append(f.completeTweakMultiCommitArgs, struct{ FeatureID, RepoName string }{featureID, ""})
	return f.tweakCommitReturn, f.tweakErr
}

func (f *fakeOrch) CompleteTweakFinish(featureID string, hadChanges bool) error {
	f.completeTweakMultiFinishArgs = append(f.completeTweakMultiFinishArgs, struct {
		FeatureID, RepoName string
		HadChanges          bool
	}{featureID, "", hadChanges})
	return f.tweakErr
}

func (f *fakeOrch) RestoreTweakFromReview(featureID string) error {
	f.restoreTweakMultiFromReviewArgs = append(f.restoreTweakMultiFromReviewArgs, struct{ FeatureID, RepoName string }{featureID, ""})
	return f.tweakErr
}

// Rebase -------------------------------------------------------------------

func (f *fakeOrch) StartRebase(featureID, repoName string) error {
	f.startRebaseRepoCycleArgs = append(f.startRebaseRepoCycleArgs, struct{ FeatureID, RepoName string }{featureID, repoName})
	return f.rebaseErr
}

func (f *fakeOrch) HandleRebaseResult(featureID, repoName string, input orchestrator.RebaseResultInput) error {
	f.handleRebaseRepoCycleResultArgs = append(f.handleRebaseRepoCycleResultArgs, struct {
		FeatureID, RepoName string
		Input               orchestrator.RebaseResultInput
	}{featureID, repoName, input})
	return f.rebaseErr
}

func (f *fakeOrch) ForcePushAfterConflict(featureID, repoName string) error {
	f.forcePushArgs = append(f.forcePushArgs, struct{ FeatureID, RepoName string }{featureID, repoName})
	return f.rebaseErr
}

// Repo cycles --------------------------------------------------------------

func (f *fakeOrch) DispatchRepoCycle(featureID, repoName string, cycleType feature.RepoCycleType, planContent string) (string, error) {
	f.dispatchRepoCycleArgs = append(f.dispatchRepoCycleArgs, struct {
		FeatureID, RepoName string
		CycleType           feature.RepoCycleType
		PlanContent         string
	}{featureID, repoName, cycleType, planContent})
	return f.repoCycleSessionReturn, f.repoCycleErr
}

func (f *fakeOrch) StartRepoCycleImplement(featureID, repoName string, cycleType feature.RepoCycleType, planContent string, conflictFiles ...string) (string, error) {
	args := struct {
		FeatureID, RepoName string
		CycleType           feature.RepoCycleType
		PlanContent         string
		ConflictFiles       []string
	}{featureID, repoName, cycleType, planContent, append([]string(nil), conflictFiles...)}
	f.startRepoCycleImplementArgs = append(f.startRepoCycleImplementArgs, struct {
		FeatureID, RepoName string
		CycleType           feature.RepoCycleType
		PlanContent         string
	}{args.FeatureID, args.RepoName, args.CycleType, args.PlanContent})
	f.startRepoCycleImplementConflictFiles = append(f.startRepoCycleImplementConflictFiles, args.ConflictFiles)
	return f.repoCycleSessionReturn, f.repoCycleErr
}

func (f *fakeOrch) HandleRepoCycleLoopDone(featureID string, input orchestrator.RepoCycleLoopResultInput) error {
	f.handleRepoCycleLoopDoneArgs = append(f.handleRepoCycleLoopDoneArgs, struct {
		FeatureID string
		Input     orchestrator.RepoCycleLoopResultInput
	}{featureID, input})
	return f.repoCycleErr
}

func (f *fakeOrch) StartCycleFinalReview(featureID string) error {
	f.startCycleFinalReviewArgs = append(f.startCycleFinalReviewArgs, struct{ FeatureID string }{featureID})
	return f.repoCycleErr
}

func (f *fakeOrch) CompleteRepoCycle(featureID, repoName string) error {
	f.completeRepoCycleArgs = append(f.completeRepoCycleArgs, struct{ FeatureID, RepoName string }{featureID, repoName})
	return f.repoCycleErr
}

// Refactor -----------------------------------------------------------------

func (f *fakeOrch) StartRefactorCycle(featureID, repoName, prompt string) (string, error) {
	f.startRefactorCycleArgs = append(f.startRefactorCycleArgs, struct{ FeatureID, RepoName, Prompt string }{featureID, repoName, prompt})
	return f.refactorSessionReturn, f.refactorErr
}

func (f *fakeOrch) RestartRefactorCycle(featureID, repoName, prompt string) (string, error) {
	f.restartRefactorCycleArgs = append(f.restartRefactorCycleArgs, struct{ FeatureID, RepoName, Prompt string }{featureID, repoName, prompt})
	return f.refactorSessionReturn, f.refactorErr
}

func (f *fakeOrch) CompleteRefactorRepoCycle(featureID, repoName string) error {
	f.completeRefactorRepoCycleArgs = append(f.completeRefactorRepoCycleArgs, struct{ FeatureID, RepoName string }{featureID, repoName})
	return f.refactorErr
}

// --- Lifecycle delegates (Phase 8 iter 3) --------------------------------
// Thin stubs for the state-mutation passthroughs; tests that care about the
// call-count or error routing set the corresponding field on fakeOrch.

func (f *fakeOrch) TryCompletePublish(featureID string) (bool, error) {
	f.lifecycleCalls = append(f.lifecycleCalls, "TryCompletePublish:"+featureID)
	return f.publishPublished, f.lifecycleErr
}
func (f *fakeOrch) MarkPublished(featureID, prURL string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "MarkPublished:"+featureID+":"+prURL)
	return f.lifecycleErr
}
func (f *fakeOrch) MarkDone(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "MarkDone:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) AdvanceRoadmapPhase(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "AdvanceRoadmapPhase:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) StartRoadmapPhaseImplementation(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "StartRoadmapPhaseImplementation:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) StartPlanning(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "StartPlanning:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) CompletePlanning(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "CompletePlanning:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) CompleteImplementation(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "CompleteImplementation:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) MarkCodeReady(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "MarkCodeReady:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) NeedsPlanReview(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "NeedsPlanReview:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) ClearAddressingReviews(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "ClearAddressingReviews:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) SetTotalRoadmapPhases(featureID string, count int) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("SetTotalRoadmapPhases:%s:%d", featureID, count))
	return f.lifecycleErr
}
func (f *fakeOrch) BumpPlanIterationsBudget(featureID string, delta int) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("BumpPlanIterationsBudget:%s:%d", featureID, delta))
	return f.lifecycleErr
}
func (f *fakeOrch) ResetPlanStatusForRoadmap(featureID string, budgetBump int) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("ResetPlanStatusForRoadmap:%s:%d", featureID, budgetBump))
	return f.lifecycleErr
}
func (f *fakeOrch) RecordRoadmapRejection(featureID, feedback string) {
	f.lifecycleCalls = append(f.lifecycleCalls, "RecordRoadmapRejection:"+featureID+":"+feedback)
}
func (f *fakeOrch) ParseRoadmapAndPersistCount(featureID string) (int, error) {
	f.lifecycleCalls = append(f.lifecycleCalls, "ParseRoadmapAndPersistCount:"+featureID)
	return f.parsedRoadmapPhases, f.lifecycleErr
}
func (f *fakeOrch) CommitRoadmapPhase(featureID string, phase int) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("CommitRoadmapPhase:%s:%d", featureID, phase))
	return f.lifecycleErr
}
func (f *fakeOrch) SetRepoPublished(featureID, repoName, prURL string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("SetRepoPublished:%s:%s:%s", featureID, repoName, prURL))
	return f.lifecycleErr
}
func (f *fakeOrch) SetRepoPublishError(featureID, repoName, errMsg string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("SetRepoPublishError:%s:%s:%s", featureID, repoName, errMsg))
	return f.lifecycleErr
}
func (f *fakeOrch) RecordPublishUIFailure(featureID, errMsg string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("RecordPublishUIFailure:%s:%s", featureID, errMsg))
	return f.lifecycleErr
}
func (f *fakeOrch) ReportMissingArtifactFailure(featureID, errMsg string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("ReportMissingArtifactFailure:%s:%s", featureID, errMsg))
	return f.lifecycleErr
}
func (f *fakeOrch) RemoveRepoCycle(featureID, repoName string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("RemoveRepoCycle:%s:%s", featureID, repoName))
	return f.lifecycleErr
}
func (f *fakeOrch) CommitUncommittedForPublish(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "CommitUncommittedForPublish:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) FailTweakSession(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("FailTweakSession:%s", featureID))
	return f.lifecycleErr
}
func (f *fakeOrch) TransitionTo(featureID string, status feature.Status) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("TransitionTo:%s:%s", featureID, status))
	return f.lifecycleErr
}
func (f *fakeOrch) ClearRepoCycles(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "ClearRepoCycles:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) RetryPhase(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "RetryPhase:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) ClearPendingHelpAndPermissions(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "ClearPendingHelpAndPermissions:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) SetBrainstormReady(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "SetBrainstormReady:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) MergeFeatureLocal(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "MergeFeatureLocal:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) UpgradePipeline(featureID string, profile feature.PipelineProfile) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("UpgradePipeline:%s:%s", featureID, profile))
	return f.lifecycleErr
}
func (f *fakeOrch) RewindToPhase(featureID string, targetPhase feature.Phase) ([]string, feature.Phase, error) {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("RewindToPhase:%s:%s", featureID, targetPhase.DirName()))
	return nil, targetPhase, f.lifecycleErr
}
func (f *fakeOrch) RewindWithRequest(featureID string, request feature.RewindRequest) ([]string, feature.Phase, error) {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("RewindWithRequest:%s:%s:%d", featureID, request.TargetPhase.DirName(), request.RoadmapPhase))
	return nil, request.TargetPhase, f.lifecycleErr
}
func (f *fakeOrch) ProceedFromRewindReview(featureID string, target feature.Phase) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("ProceedFromRewindReview:%s:%s", featureID, target.DirName()))
	return f.lifecycleErr
}
func (f *fakeOrch) CleanWorktree(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "CleanWorktree:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) SaveFeatureSummary(featureID, summary string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("SaveFeatureSummary:%s:%s", featureID, summary))
	return f.lifecycleErr
}
func (f *fakeOrch) ApplyRefactorPipeline(featureID string, profile feature.PipelineProfile) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("ApplyRefactorPipeline:%s:%s", featureID, profile))
	return f.lifecycleErr
}
func (f *fakeOrch) EnterReviewGate(featureID string, targetPhase feature.Phase) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("EnterReviewGate:%s:%s", featureID, targetPhase))
	return f.lifecycleErr
}
func (f *fakeOrch) ResetToPublishedFromTweak(featureID string) error {
	f.lifecycleCalls = append(f.lifecycleCalls, "ResetToPublishedFromTweak:"+featureID)
	return f.lifecycleErr
}
func (f *fakeOrch) ExtendFailedPhaseBudget(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) error {
	f.lifecycleCalls = append(f.lifecycleCalls, fmt.Sprintf("ExtendFailedPhaseBudget:%s:%d:%d", featureID, maxIterationsDelta, maxPlanIterationsDelta))
	return f.lifecycleErr
}
func (f *fakeOrch) CollectAndClearRepoCycleRestarts(featureID string) ([]orchestrator.RepoCycleRestart, *orchestrator.RefactorRestart, error) {
	f.lifecycleCalls = append(f.lifecycleCalls, "CollectAndClearRepoCycleRestarts:"+featureID)
	return nil, nil, f.lifecycleErr
}

// --- Phase 8 iter 13 consolidation ---------------------------------

// restartPhaseReturn configures the outcome RestartPhase returns. Tests that
// don't set this get RestartNoOp, the same behaviour as "nothing to dispatch".
func (f *fakeOrch) RestartPhase(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) (orchestrator.RestartOutcome, error) {
	f.restartPhaseArgs = append(f.restartPhaseArgs, struct {
		FeatureID                                  string
		MaxIterationsDelta, MaxPlanIterationsDelta int
	}{featureID, maxIterationsDelta, maxPlanIterationsDelta})
	return f.restartPhaseReturn, f.lifecycleErr
}

func (f *fakeOrch) ResolveGateReviewContext(featureID string, targetPhase feature.Phase) (orchestrator.GateReviewContext, error) {
	f.resolveGateReviewArgs = append(f.resolveGateReviewArgs, struct {
		FeatureID   string
		TargetPhase feature.Phase
	}{featureID, targetPhase})
	return f.resolveGateReviewReturn, f.lifecycleErr
}

func (f *fakeOrch) ResolveRewindReviewContext(featureID string, targetPhase feature.Phase) (orchestrator.RewindReviewContext, error) {
	f.resolveRewindReviewArgs = append(f.resolveRewindReviewArgs, struct {
		FeatureID   string
		TargetPhase feature.Phase
	}{featureID, targetPhase})
	return f.resolveRewindReviewReturn, f.lifecycleErr
}

// UpdateFeatureConfig records the call and returns the configured err.
func (f *fakeOrch) UpdateFeatureConfig(featureID string, input orchestrator.UpdateFeatureConfigInput) error {
	f.updateFeatureConfigArgs = append(f.updateFeatureConfigArgs, struct {
		FeatureID string
		Input     orchestrator.UpdateFeatureConfigInput
	}{featureID, input})
	return f.updateFeatureConfigErr
}

// Compile-time assertion: *fakeOrch implements orchestratorAPI.
var _ orchestratorAPI = (*fakeOrch)(nil)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// allBridgedEventTypes enumerates every ports.EventType that
// listenForOrchestratorEvents should translate into a typed TUI message.
// SessionOutput is intentionally absent — it flows through eventCh, not the
// orchestrator event bus.
var allBridgedEventTypes = []ports.EventType{
	ports.FeatureCreated,
	ports.FeatureStarted,
	ports.FeatureAdvanced,
	ports.FeatureCompleted,
	ports.FeatureFailed,
	ports.FeatureInterrupted,
	ports.PhaseStarted,
	ports.PhaseCompleted,
	ports.ReviewRequired,
	ports.PublishStarted,
	ports.PublishCompleted,
	ports.RepoStatusChanged,
	ports.RecoveryScanned,
	ports.RecoveryExecuted,
	ports.TweakReviewApproved,
	ports.FeatureConfigChanged,
}

// expectedMsgType returns the TUI message type produced by orchEventToMsg for
// a given ports.EventType, as a Go type name string (for assertion error
// messages).
func expectedMsgTypeName(et ports.EventType) string {
	switch et {
	case ports.FeatureCreated:
		return "OrchFeatureCreatedMsg"
	case ports.FeatureStarted:
		return "OrchFeatureStartedMsg"
	case ports.FeatureAdvanced:
		return "OrchFeatureAdvancedMsg"
	case ports.FeatureCompleted:
		return "OrchFeatureCompletedMsg"
	case ports.FeatureFailed:
		return "OrchFeatureFailedMsg"
	case ports.FeatureInterrupted:
		return "OrchFeatureInterruptedMsg"
	case ports.PhaseStarted:
		return "OrchPhaseStartedMsg"
	case ports.PhaseCompleted:
		return "OrchPhaseCompletedMsg"
	case ports.ReviewRequired:
		return "OrchReviewRequiredMsg"
	case ports.PublishStarted:
		return "OrchPublishStartedMsg"
	case ports.PublishCompleted:
		return "OrchPublishCompletedMsg"
	case ports.RepoStatusChanged:
		return "OrchRepoStatusChangedMsg"
	case ports.RecoveryScanned:
		return "OrchRecoveryScannedMsg"
	case ports.RecoveryExecuted:
		return "OrchRecoveryExecutedMsg"
	case ports.TweakReviewApproved:
		return "OrchTweakReviewApprovedMsg"
	case ports.FeatureConfigChanged:
		return "OrchFeatureConfigChangedMsg"
	}
	return ""
}

// ---------------------------------------------------------------------------
// T7. listenForOrchestratorEvents translates every ports.EventType (except
// SessionOutput) into the correct OrchXxxMsg.
// ---------------------------------------------------------------------------

func TestTUI_ListenForOrchestratorEvents_TranslatesAllEventTypes(t *testing.T) {
	for _, et := range allBridgedEventTypes {
		name := expectedMsgTypeName(et)
		t.Run(name, func(t *testing.T) {
			msg := orchEventToMsg(ports.Event{Type: et, FeatureID: "f1"})
			if msg == nil {
				t.Fatalf("orchEventToMsg returned nil for %d", et)
			}
			gotName := typeName(msg)
			if gotName != name {
				t.Errorf("event %d: got %s, want %s", et, gotName, name)
			}
		})
	}

	// SessionOutput is intentionally unmapped.
	t.Run("SessionOutput_returns_nil", func(t *testing.T) {
		msg := orchEventToMsg(ports.Event{Type: ports.SessionOutput})
		if msg != nil {
			t.Errorf("orchEventToMsg(SessionOutput) = %T, want nil", msg)
		}
	})
}

// typeName returns the Go type name of v without a package qualifier.
func typeName(v any) string {
	if v == nil {
		return "nil"
	}
	switch v.(type) {
	case OrchFeatureCreatedMsg:
		return "OrchFeatureCreatedMsg"
	case OrchFeatureStartedMsg:
		return "OrchFeatureStartedMsg"
	case OrchFeatureAdvancedMsg:
		return "OrchFeatureAdvancedMsg"
	case OrchFeatureCompletedMsg:
		return "OrchFeatureCompletedMsg"
	case OrchFeatureFailedMsg:
		return "OrchFeatureFailedMsg"
	case OrchFeatureInterruptedMsg:
		return "OrchFeatureInterruptedMsg"
	case OrchPhaseStartedMsg:
		return "OrchPhaseStartedMsg"
	case OrchPhaseCompletedMsg:
		return "OrchPhaseCompletedMsg"
	case OrchReviewRequiredMsg:
		return "OrchReviewRequiredMsg"
	case OrchPublishStartedMsg:
		return "OrchPublishStartedMsg"
	case OrchPublishCompletedMsg:
		return "OrchPublishCompletedMsg"
	case OrchRepoStatusChangedMsg:
		return "OrchRepoStatusChangedMsg"
	case OrchRecoveryScannedMsg:
		return "OrchRecoveryScannedMsg"
	case OrchRecoveryExecutedMsg:
		return "OrchRecoveryExecutedMsg"
	case OrchTweakReviewApprovedMsg:
		return "OrchTweakReviewApprovedMsg"
	case OrchFeatureConfigChangedMsg:
		return "OrchFeatureConfigChangedMsg"
	}
	return "<unknown>"
}

// ---------------------------------------------------------------------------
// T7 (shutdown sub-case): listener returns nil when Done closes.
// ---------------------------------------------------------------------------

func TestTUI_ListenForOrchestratorEvents_ReturnsNilOnDone(t *testing.T) {
	orch := newFakeOrch()
	app := AppModel{orchestrator: orch}
	cmd := app.listenForOrchestratorEvents()
	if cmd == nil {
		t.Fatal("listenForOrchestratorEvents returned nil cmd")
	}

	close(orch.done)

	result := make(chan any, 1)
	go func() { result <- cmd() }()

	select {
	case v := <-result:
		if v != nil {
			t.Errorf("listener returned %T after Done closed, want nil", v)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("listener did not return within 500ms after Done closure")
	}
}

// ---------------------------------------------------------------------------
// T7 (event path): listener returns typed msg when an event arrives.
// ---------------------------------------------------------------------------

func TestTUI_ListenForOrchestratorEvents_ReceivesEvent(t *testing.T) {
	orch := newFakeOrch()
	app := AppModel{orchestrator: orch}
	cmd := app.listenForOrchestratorEvents()

	orch.events <- ports.Event{Type: ports.FeatureStarted, FeatureID: "fx"}

	result := make(chan any, 1)
	go func() { result <- cmd() }()

	select {
	case v := <-result:
		msg, ok := v.(OrchFeatureStartedMsg)
		if !ok {
			t.Fatalf("got %T, want OrchFeatureStartedMsg", v)
		}
		if msg.FeatureID != "fx" {
			t.Errorf("FeatureID = %q, want %q", msg.FeatureID, "fx")
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("listener did not fire within 500ms")
	}
}

// Silence unused-field warnings on fakeOrch fields touched only in later tests.
var _ = feature.Phase(0)

// ---------------------------------------------------------------------------
// T1. createFeatureCmd delegates to orchestrator.CreateFeature.
// ---------------------------------------------------------------------------

func TestTUI_CreateFeatureCmd_DelegatesToOrchestrator(t *testing.T) {
	orch := newFakeOrch()
	orch.createReturn = &feature.Feature{ID: "feat-1"}

	fm, _ := newTestAppModel(t)
	fm.orchestrator = orch

	result := &WizardResult{
		Name:         "my feature",
		Description:  "desc",
		Repos:        []string{"repo1"},
		ExitCriteria: "done",
		Inquireness:  "skim",
	}

	cmd := fm.createFeatureCmd(result)
	if cmd == nil {
		t.Fatal("createFeatureCmd returned nil cmd")
	}
	msg := cmd()
	if _, ok := msg.(FeatureCreatedMsg); !ok {
		t.Errorf("got %T, want FeatureCreatedMsg", msg)
	}

	if len(orch.createCalls) != 1 {
		t.Fatalf("orchestrator.CreateFeature calls = %d, want 1", len(orch.createCalls))
	}
	got := orch.createCalls[0]
	if got.name != "my feature" {
		t.Errorf("name = %q, want %q", got.name, "my feature")
	}
	if got.description != "desc" {
		t.Errorf("description = %q, want %q", got.description, "desc")
	}
	if len(got.repos) != 1 || got.repos[0] != "repo1" {
		t.Errorf("repos = %v, want [repo1]", got.repos)
	}
}

func TestTUI_CreateFeatureCmd_DelegatesCompleteWizardResultToOrchestrator(t *testing.T) {
	orch := newFakeOrch()
	orch.createReturn = &feature.Feature{ID: "feat-full"}

	app, _ := newTestAppModel(t)
	app.orchestrator = orch

	result := &WizardResult{
		Name:        "complete feature",
		Description: "full payload",
		Images:      []string{"/tmp/image-1.png"},
		Attachments: []string{"/tmp/spec.md"},
		Repos:       []string{"api", "web"},
		Models: config.ModelConfig{
			Research:       "claude:opus",
			Planning:       "claude:opus",
			Implementation: "claude:sonnet",
			Review:         "codex:gpt-5.4",
			KBBuild:        "claude:haiku",
		},
		ExitCriteria:     "all acceptance checks pass",
		Inquireness:      "high",
		RiskLevel:        "high",
		Pipeline:         feature.PipelineMedium,
		UseCurrentBranch: true,
		Checkpoints: feature.Checkpoints{
			InquiryReview: true,
			PlanReview:    true,
			ManualPublish: true,
		},
	}

	msg := app.createFeatureCmd(result)()
	created, ok := msg.(FeatureCreatedMsg)
	if !ok {
		t.Fatalf("createFeatureCmd() = %T, want FeatureCreatedMsg", msg)
	}
	if created.Err != nil {
		t.Fatalf("FeatureCreatedMsg.Err = %v, want nil", created.Err)
	}
	if len(orch.createCalls) != 1 {
		t.Fatalf("orchestrator.CreateFeature calls = %d, want 1", len(orch.createCalls))
	}
	got := orch.createCalls[0]
	if got.name != result.Name || got.description != result.Description {
		t.Fatalf("create call name/description = %q/%q, want %q/%q", got.name, got.description, result.Name, result.Description)
	}
	if !slices.Equal(got.repos, result.Repos) {
		t.Fatalf("repos = %v, want %v", got.repos, result.Repos)
	}
	if got.models != result.Models {
		t.Fatalf("models = %+v, want %+v", got.models, result.Models)
	}
	if got.exitCriteria != result.ExitCriteria || got.inquireness != result.Inquireness {
		t.Fatalf("exit/inquireness = %q/%q, want %q/%q", got.exitCriteria, got.inquireness, result.ExitCriteria, result.Inquireness)
	}
	if !slices.Equal(got.images, result.Images) {
		t.Fatalf("images = %v, want %v", got.images, result.Images)
	}
	if len(got.opts) != 1 {
		t.Fatalf("CreateOptions count = %d, want 1", len(got.opts))
	}
	opt := got.opts[0]
	wantCheckpoints := result.Pipeline.ProjectGates(result.Checkpoints, true).Checkpoints
	if opt.Checkpoints != wantCheckpoints {
		t.Fatalf("Checkpoints = %+v, want %+v", opt.Checkpoints, wantCheckpoints)
	}
	if !opt.UseCurrentBranch {
		t.Fatal("UseCurrentBranch = false, want true")
	}
	if opt.RiskLevel != feature.RiskHigh {
		t.Fatalf("RiskLevel = %q, want high", opt.RiskLevel)
	}
	if opt.Pipeline != feature.PipelineMedium {
		t.Fatalf("Pipeline = %s, want medium", opt.Pipeline)
	}
	if !slices.Equal(opt.Attachments, result.Attachments) {
		t.Fatalf("Attachments = %v, want %v", opt.Attachments, result.Attachments)
	}
}

// ---------------------------------------------------------------------------
// T2. stopFeatureCmd delegates to orchestrator.InterruptFeature for
//     non-Published features.
// ---------------------------------------------------------------------------

func TestTUI_StopFeatureCmd_DelegatesToOrchestrator(t *testing.T) {
	t.Run("non_published_delegates", func(t *testing.T) {
		app, fm := newTestAppModel(t)
		orch := newFakeOrch()
		app.orchestrator = orch
		app.sessionManager = session.NewManager(nil)

		f, err := fm.Create("stoppable", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
			ff.Status = feature.StatusResearching
			return nil
		}); err != nil {
			t.Fatalf("Modify: %v", err)
		}

		msg := app.stopFeatureCmd(f.ID)()
		if _, ok := msg.(RefreshFeaturesMsg); !ok {
			t.Errorf("got %T, want RefreshFeaturesMsg", msg)
		}

		if len(orch.interruptIDs) != 1 || orch.interruptIDs[0] != f.ID {
			t.Errorf("orchestrator.InterruptFeature calls = %v, want [%s]", orch.interruptIDs, f.ID)
		}
	})

	t.Run("published_with_active_cycles_short_circuits", func(t *testing.T) {
		app, fm := newTestAppModel(t)
		orch := newFakeOrch()
		app.orchestrator = orch
		app.sessionManager = session.NewManager(nil)

		f, err := fm.Create("published", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
			ff.Status = feature.StatusPublished
			if ff.RepoCycles == nil {
				ff.RepoCycles = make(map[string]*feature.RepoCycleState)
			}
			ff.RepoCycles["test-repo"] = &feature.RepoCycleState{
				Status: "running",
			}
			return nil
		}); err != nil {
			t.Fatalf("Modify: %v", err)
		}

		_ = app.stopFeatureCmd(f.ID)()

		// Published-with-cycles must skip InterruptFeature to preserve the
		// Published state. ClearRepoCycles is the expected path.
		if len(orch.interruptIDs) != 0 {
			t.Errorf("InterruptFeature called on Published feature: %v", orch.interruptIDs)
		}
		got, err := fm.Get(f.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != feature.StatusPublished {
			t.Errorf("Status after stop = %v, want StatusPublished", got.Status)
		}
	})
}

// ---------------------------------------------------------------------------
// T3. deleteFeatureCmd delegates to orchestrator.Delete.
// ---------------------------------------------------------------------------

func TestTUI_DeleteFeatureCmd_DelegatesToOrchestrator(t *testing.T) {
	app, fm := newTestAppModel(t)
	orch := newFakeOrch()
	app.orchestrator = orch
	app.sessionManager = session.NewManager(nil)

	f, err := fm.Create("deletable", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	msg := app.deleteFeatureCmd(f.ID)()
	done, ok := msg.(DeleteFeatureDoneMsg)
	if !ok {
		t.Fatalf("got %T, want DeleteFeatureDoneMsg", msg)
	}
	if done.FeatureID != f.ID {
		t.Errorf("FeatureID = %q, want %q", done.FeatureID, f.ID)
	}
	if len(orch.deleteIDs) != 1 || orch.deleteIDs[0] != f.ID {
		t.Errorf("orchestrator.Delete calls = %v, want [%s]", orch.deleteIDs, f.ID)
	}
}

// ---------------------------------------------------------------------------
// T11. Startup sweep: NewAppModel delegates to orchestrator.InterruptAllRunning
//      only when there are no recovery items, which transitions running
//      features and preserves Published + cycles.
// ---------------------------------------------------------------------------

func TestTUI_StartupSweep_DelegatesToOrchestratorWhenNoRecoveryItems(t *testing.T) {
	app, fm := newTestAppModel(t)
	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Build a real orchestrator so InterruptAllRunning exercises the real
	// state transitions.
	orch := newTestOrchestrator(fm, sm)

	// Seed: three features in running statuses + one Published-with-cycles.
	running, err := fm.Create("running", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create running: %v", err)
	}
	if err := fm.Store.Modify(running.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusResearching
		return nil
	}); err != nil {
		t.Fatalf("Modify running: %v", err)
	}

	planning, err := fm.Create("planning", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create planning: %v", err)
	}
	if err := fm.Store.Modify(planning.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusPlanning
		return nil
	}); err != nil {
		t.Fatalf("Modify planning: %v", err)
	}

	reviewing, err := fm.Create("reviewing", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create reviewing: %v", err)
	}
	if err := fm.Store.Modify(reviewing.ID, func(f *feature.Feature) error {
		// Modern Final Review shape: feature-level Status stays
		// Implementing; per-repo state flips to FinalReviewing.
		f.Status = feature.StatusImplementing
		if f.RepoStates == nil {
			f.RepoStates = map[string]*feature.RepoState{}
		}
		f.RepoStates["test-repo"] = &feature.RepoState{Touched: true}
		return nil
	}); err != nil {
		t.Fatalf("Modify reviewing: %v", err)
	}

	published, err := fm.Create("published", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create published: %v", err)
	}
	if err := fm.Store.Modify(published.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusPublished
		if f.RepoCycles == nil {
			f.RepoCycles = make(map[string]*feature.RepoCycleState)
		}
		f.RepoCycles["test-repo"] = &feature.RepoCycleState{Status: "running"}
		return nil
	}); err != nil {
		t.Fatalf("Modify published: %v", err)
	}

	// NewAppModel triggers the startup sweep via orchestrator.InterruptAllRunning
	// because ScanRecovery returned no recoverable sessions.
	eventCh := make(chan interface{}, 16)
	_, err = NewAppModel(fm, sm, orch, permission.NewCache(nil), eventCh)
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}

	// Running-state features transition to Interrupted.
	for _, id := range []string{running.ID, planning.ID, reviewing.ID} {
		f, err := fm.Get(id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if f.Status != feature.StatusInterrupted {
			t.Errorf("%s status = %v, want StatusInterrupted", id, f.Status)
		}
	}

	// Published feature retains its status; its stale cycles become "interrupted".
	pub, err := fm.Get(published.ID)
	if err != nil {
		t.Fatalf("Get published: %v", err)
	}
	if pub.Status != feature.StatusPublished {
		t.Errorf("published status = %v, want StatusPublished", pub.Status)
	}
	if rc := pub.RepoCycles["test-repo"]; rc == nil || rc.Status != feature.RepoCycleInterrupted {
		t.Errorf("published cycle status = %+v, want interrupted", rc)
	}
}

// TestTUI_StartupSweep_DelegatesToOrchestrator_FakeOrch checks that NewAppModel
// actually calls InterruptAllRunning on the provided orchestrator when recovery
// is empty (not that it re-implements the sweep inline).
func TestTUI_StartupSweep_DelegatesToOrchestrator_FakeOrch_NoRecoveryItems(t *testing.T) {
	app, fm := newTestAppModel(t)
	orch := newFakeOrch()
	app.sessionManager = session.NewManager(nil)

	// Seed one feature so there's something to sweep.
	_, err := fm.Create("x", "y", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	eventCh := make(chan interface{}, 16)
	_, err = NewAppModel(fm, app.sessionManager, orch, permission.NewCache(nil), eventCh)
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}
	if orch.interruptAllRc != 1 {
		t.Errorf("InterruptAllRunning calls = %d, want 1", orch.interruptAllRc)
	}
}

func TestTUI_StartupRecoveryItemsDoNotInterruptRunningFeatures(t *testing.T) {
	app, fm := newTestAppModel(t)
	orch := newFakeOrch()
	app.sessionManager = session.NewManager(nil)

	running, err := fm.Create("running recovery", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create running: %v", err)
	}
	if err := fm.Store.Modify(running.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		return nil
	}); err != nil {
		t.Fatalf("Modify running: %v", err)
	}
	running, err = fm.Get(running.ID)
	if err != nil {
		t.Fatalf("Get running: %v", err)
	}

	orch.scanRecoveryReturn = []ports.RecoveryItem{{
		PIDFile: ports.PIDFile{
			PID:       12345,
			FeatureID: running.ID,
			Phase:     feature.PhaseImplement.String(),
			Iteration: 2,
		},
		ProcessAlive: true,
		Feature:      running,
	}}

	eventCh := make(chan interface{}, 16)
	newApp, err := NewAppModel(fm, app.sessionManager, orch, permission.NewCache(nil), eventCh)
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}

	if orch.scanRecoveryCalls != 1 {
		t.Errorf("ScanRecovery calls = %d, want 1", orch.scanRecoveryCalls)
	}
	if orch.interruptAllRc != 0 {
		t.Errorf("InterruptAllRunning calls = %d, want 0", orch.interruptAllRc)
	}
	if newApp.currentView != ViewRecovery {
		t.Errorf("currentView = %v, want ViewRecovery", newApp.currentView)
	}
	updated, err := fm.Get(running.ID)
	if err != nil {
		t.Fatalf("Get updated running: %v", err)
	}
	if updated.Status != feature.StatusImplementing {
		t.Errorf("status = %v, want %v", updated.Status, feature.StatusImplementing)
	}
}

func TestTUI_StartupRecoveryScanErrorDoesNotInterruptRunningFeatures(t *testing.T) {
	app, fm := newTestAppModel(t)
	orch := newFakeOrch()
	orch.scanRecoveryErr = errors.New("scan failed")
	app.sessionManager = session.NewManager(nil)

	running, err := fm.Create("running scan error", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create running: %v", err)
	}
	if err := fm.Store.Modify(running.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		return nil
	}); err != nil {
		t.Fatalf("Modify running: %v", err)
	}

	eventCh := make(chan interface{}, 16)
	_, err = NewAppModel(fm, app.sessionManager, orch, permission.NewCache(nil), eventCh)
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}

	if orch.scanRecoveryCalls != 1 {
		t.Errorf("ScanRecovery calls = %d, want 1", orch.scanRecoveryCalls)
	}
	if orch.interruptAllRc != 0 {
		t.Errorf("InterruptAllRunning calls = %d, want 0", orch.interruptAllRc)
	}
	updated, err := fm.Get(running.ID)
	if err != nil {
		t.Fatalf("Get updated running: %v", err)
	}
	if updated.Status != feature.StatusImplementing {
		t.Errorf("status = %v, want %v", updated.Status, feature.StatusImplementing)
	}
}

func TestTUI_OrchPublishCompletedConflictRoutesToRebaseUX(t *testing.T) {
	const featureID = "feat-publish-conflict"
	const repoName = "repo-a"
	const branch = "feature/publish-conflict"
	const rebaseTarget = "main"

	app, _ := newTestAppModel(t)
	orch := newFakeOrch()
	app.orchestrator = orch

	_, cmd := app.Update(OrchPublishCompletedMsg{
		FeatureID: featureID,
		Error: &orchestrator.PublishConflictError{
			RepoName:     repoName,
			Branch:       branch,
			RebaseTarget: rebaseTarget,
		},
	})
	if cmd == nil {
		t.Fatal("OrchPublishCompletedMsg conflict returned nil cmd (no rebase cycle dispatched)")
	}
	_ = cmd()

	if len(orch.startRepoCycleImplementArgs) != 1 {
		t.Fatalf("StartRepoCycleImplement calls = %d, want 1", len(orch.startRepoCycleImplementArgs))
	}
	call := orch.startRepoCycleImplementArgs[0]
	if call.FeatureID != featureID || call.RepoName != repoName || call.CycleType != feature.CycleRebase {
		t.Fatalf("StartRepoCycleImplement args = %+v, want feature=%q repo=%q cycle=%q", call, featureID, repoName, feature.CycleRebase)
	}
	if call.PlanContent != rebaseTarget {
		t.Errorf("rebase target = %q, want %q", call.PlanContent, rebaseTarget)
	}
}

func TestTUI_OrchPublishCompletedConflictExitsPublishView(t *testing.T) {
	const featureID = "feat-publish-conflict"
	const repoName = "repo-a"
	const rebaseTarget = "main"

	app, _ := newTestAppModel(t)
	app.orchestrator = newFakeOrch()
	app.publish = newTestPublishModel(featureID, "diff", "log", "title", "body", 120, 40)
	app.publish.step = publishStepExecute
	app.currentView = ViewPublish

	result, _ := app.Update(OrchPublishCompletedMsg{
		FeatureID: featureID,
		Error: &orchestrator.PublishConflictError{
			RepoName:     repoName,
			RebaseTarget: rebaseTarget,
		},
	})
	updated := result.(AppModel)

	if updated.publish.step == publishStepExecute {
		t.Fatal("publish conflict left the publish model in publishStepExecute")
	}
	if updated.currentView == ViewPublish {
		t.Fatal("publish conflict left the user on the Publish view")
	}
}

func TestTUI_PublishCmdDoesNotSynthesizeDuplicateCompletion(t *testing.T) {
	const featureID = "feat-publish-once"

	app, fm := newTestAppModel(t)
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Publish Once",
		Slug:          "publish-once",
		Status:        feature.StatusCodeReady,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: "/tmp/test-repo"},
		},
		RepoStates: map[string]*feature.RepoState{
			"test-repo": {Touched: true},
		},
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	orch := newFakeOrch()
	orch.publishErr = &orchestrator.PublishConflictError{RepoName: "test-repo", RebaseTarget: "main"}
	app.orchestrator = orch

	cmd := app.publishCmd(featureID)
	if cmd == nil {
		t.Fatal("publishCmd returned nil")
	}
	msg := cmd()

	if len(orch.publishIDs) != 1 || orch.publishIDs[0] != featureID {
		t.Fatalf("Publish calls = %+v, want exactly %q", orch.publishIDs, featureID)
	}
	if _, ok := msg.(OrchPublishCompletedMsg); ok {
		t.Fatal("publishCmd synthesized OrchPublishCompletedMsg; Publish already emits completion")
	}
	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("publishCmd msg = %T, want RefreshFeaturesMsg", msg)
	}
}

// TestTUI_CompleteTweakFinishCmd_RoutesPullRebaseConflictToRebaseUX is the
// regression test for the unified N=1 tweak-review path. After a tweak cycle
// is FR-approved, OrchTweakReviewApprovedMsg dispatches completeTweakFinishCmd.
// When the orchestrator surfaces a *PublishConflictError (PullRebase conflict
// on the post-FR push), the TUI must route the user into the rebase-resolution
// UX — emitting RebaseRepoCycleResultMsg{HasConflict:true, RebaseTarget:<PR base>}
// so handleRebaseRepoCycleResult spawns a fresh CycleRebase. Returning a bare
// RefreshFeaturesMsg here regresses the published single-repo flow that on
// master surfaced the conflict directly, leaving the user with a silent
// dashboard refresh and a failed cycle. The forwarded RebaseTarget must be
// the orchestrator-computed PR base (e.g. "master"), NOT the feature branch
// — passing the feature branch would point the rebase plan at the wrong
// ref and produce wrong recovery instructions.
func TestTUI_CompleteTweakFinishCmd_RoutesPullRebaseConflictToRebaseUX(t *testing.T) {
	const featureID = "feat-tweak-conflict"
	const repoName = "repo-a"
	const branch = "feature/tweak-conflict"
	const rebaseTarget = "master"

	app, _ := newTestAppModel(t)
	orch := newFakeOrch()
	orch.tweakErr = &orchestrator.PublishConflictError{
		RepoName:     repoName,
		Branch:       branch,
		RebaseTarget: rebaseTarget,
	}
	app.orchestrator = orch

	cmd := app.completeTweakFinishCmd(featureID, repoName, true)
	if cmd == nil {
		t.Fatal("completeTweakFinishCmd returned nil cmd")
	}
	msg := cmd()

	got, ok := msg.(RebaseRepoCycleResultMsg)
	if !ok {
		t.Fatalf("got %T, want RebaseRepoCycleResultMsg (silent refresh would regress the rebase-resolution UX)", msg)
	}
	if got.FeatureID != featureID {
		t.Errorf("FeatureID = %q, want %q", got.FeatureID, featureID)
	}
	if got.RepoName != repoName {
		t.Errorf("RepoName = %q, want %q", got.RepoName, repoName)
	}
	if !got.HasConflict {
		t.Errorf("HasConflict = false, want true")
	}
	if got.RebaseTarget != rebaseTarget {
		t.Errorf("RebaseTarget = %q, want %q (must be the PR base, not the feature branch)", got.RebaseTarget, rebaseTarget)
	}
	if got.RebaseTarget == branch {
		t.Errorf("RebaseTarget = %q is the feature branch — recovery rebase would target origin/<feature> instead of origin/master", got.RebaseTarget)
	}

	// And the orchestrator was actually called with the feature-level
	// signature (FeatureID, hadChanges). repoName is no longer threaded into
	// CompleteTweakFinish; the orchestrator fans out across every
	// Feature.Repos worktree internally.
	if len(orch.completeTweakMultiFinishArgs) != 1 {
		t.Fatalf("CompleteTweakFinish call count = %d, want 1", len(orch.completeTweakMultiFinishArgs))
	}
	call := orch.completeTweakMultiFinishArgs[0]
	if call.FeatureID != featureID || !call.HadChanges {
		t.Errorf("CompleteTweakFinish args = %+v, want {%q, _, true}", call, featureID)
	}
}

// TestTUI_HandleRebaseRepoCycleResult_ConflictSpawnsRebaseCycle verifies the
// downstream half of the conflict-routing chain: when handleRebaseRepoCycleResult
// receives a HasConflict result (as produced above), it dispatches
// startRepoCycleImplementCmd with CycleRebase + the rebase target so the
// orchestrator spawns a fresh per-repo rebase cycle. Together with the test
// above this proves the full unified-path conflict flow lands the user in the
// rebase-resolution UX instead of a silent refresh.
func TestTUI_HandleRebaseRepoCycleResult_ConflictSpawnsRebaseCycle(t *testing.T) {
	const featureID = "feat-tweak-conflict"
	const repoName = "repo-a"
	const branch = "feature/tweak-conflict"

	app, _ := newTestAppModel(t)
	orch := newFakeOrch()
	app.orchestrator = orch

	_, cmd := app.handleRebaseRepoCycleResult(RebaseRepoCycleResultMsg{
		FeatureID:    featureID,
		RepoName:     repoName,
		HasConflict:  true,
		RebaseTarget: branch,
	})
	if cmd == nil {
		t.Fatal("handleRebaseRepoCycleResult HasConflict returned nil cmd (no rebase cycle dispatched)")
	}

	// Drive the cmd so the fakeOrch records the StartRepoCycleImplement call.
	_ = cmd()

	if len(orch.startRepoCycleImplementArgs) != 1 {
		t.Fatalf("StartRepoCycleImplement call count = %d, want 1", len(orch.startRepoCycleImplementArgs))
	}
	call := orch.startRepoCycleImplementArgs[0]
	if call.FeatureID != featureID {
		t.Errorf("FeatureID = %q, want %q", call.FeatureID, featureID)
	}
	if call.RepoName != repoName {
		t.Errorf("RepoName = %q, want %q", call.RepoName, repoName)
	}
	if call.CycleType != feature.CycleRebase {
		t.Errorf("CycleType = %v, want CycleRebase", call.CycleType)
	}
	if call.PlanContent != branch {
		t.Errorf("PlanContent (rebase target) = %q, want %q", call.PlanContent, branch)
	}
}
