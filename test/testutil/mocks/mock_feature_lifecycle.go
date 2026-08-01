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

package mocks

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// MockCall records a single method invocation on a mock.
type MockCall struct {
	Method string
	Args   []any
}

// MockFeatureLifecycle is a custom mock for ports.FeatureLifecycle.
// Commonly-tested methods have dedicated Fn overrides; all others record
// a MockCall and return DefaultError.
type MockFeatureLifecycle struct {
	CreateFn func(name, description string, repos []string, models config.ModelConfig,
		exitCriteria, inquireness string, images []string,
		opts ...feature.CreateOptions) (*feature.Feature, error)
	GetFn                      func(id string) (*feature.Feature, error)
	ListFn                     func() ([]*feature.Feature, error)
	DeleteFn                   func(featureID string) error
	TransitionFn               func(id string, to feature.Status) error
	RunSetupFn                 func(featureID string, opts ...feature.SetupRunnerOptions) error
	RetrySetupFn               func(featureID string, opts ...feature.SetupRunnerOptions) error
	ReconcileAbandonedSetupsFn func() ([]string, error)
	FailActiveSetupFn          func(featureID, message string) (bool, error)

	// Phase-start hooks — tests that need to mimic real lifecycle behavior
	// (status transitions) can install these. Called after the mock records
	// the invocation.
	StartKnowledgeBaseFn  func(featureID string) error
	StartInquireFn        func(featureID string) error
	StartDesignFn         func(featureID string) error
	StartResearchFn       func(featureID string) error
	StartPlanningFn       func(featureID string) error
	StartImplementationFn func(featureID string) error

	// Phase-completion hooks — allow tests to control side-effects and
	// status transitions when completion handlers run.
	CompleteKnowledgeBaseFn  func(featureID string) error
	CompleteInquireFn        func(featureID string) error
	CompleteResearchFn       func(featureID string) error
	CompleteDesignFn         func(featureID string) error
	CompletePlanningFn       func(featureID string) error
	CompleteImplementationFn func(featureID string) error

	// KB per-repo hooks.
	InitKBStatusFn        func(featureID string) error
	MarkRepoKBCompletedFn func(featureID, repoName string) error
	MarkRepoKBFailedFn    func(featureID, repoName, errMsg string) error
	AllKBsCompletedFn     func(featureID string) (bool, error)

	// Plan / review hooks.
	NeedsPlanReviewFn                func(featureID string) error
	StartAddressingReviewsFn         func(featureID string) error
	ClearAddressingReviewsFn         func(featureID string) error
	StartFeatureRebaseOperationFn    func(featureID string) error
	MarkFeatureRebaseStageFn         func(featureID string, stage feature.RebaseStage) error
	UpdateFeatureRebaseRepoFn        func(featureID, repoName string, status feature.RebaseRepoStatus, progress feature.RebaseRepoProgress) error
	FailFeatureRebaseCycleFn         func(featureID, errMsg string) error
	MarkFeatureRebaseNeedUserInputFn func(featureID, gatePath string, iteration int, summary string) error
	ClearFeatureRebaseOperationFn    func(featureID string) error

	// Roadmap hooks.
	AdvanceRoadmapPhaseFn             func(featureID string) error
	StartRoadmapPhaseImplementationFn func(featureID string) error
	RecordRoadmapPhaseCommitAnchorsFn func(featureID string, phase int, anchors map[string]string) error

	// Publish / completion hooks.
	MarkCodeReadyFn        func(featureID string) error
	MarkFinalReviewReadyFn func(featureID string) error
	MarkPublishedFn        func(featureID, prURL string) error
	MarkFailedFn           func(featureID, failureType, lastError string) error
	SetRepoPublishedFn     func(featureID, repoName, prURL string) error
	SetRepoPublishErrorFn  func(featureID, repoName, errMsg string) error
	TryCompletePublishFn   func(featureID string) (bool, error)
	InitRepoImplFn         func(featureID string) error

	// Per-repo cycle hook — tests that need to mimic the real
	// CompleteRepoCycle side-effect (delete RepoCycles[repoName]) install
	// this. Called after the mock records the invocation.
	CompleteRepoCycleFn func(featureID, repoName string) error

	// FailRepoCycleFn lets tests mimic the real FailRepoCycle side-effect
	// (set Status=RepoCycleFailed, clear PendingNeedUserInputPath, clear
	// RefactorPrompt for refactor cycles). Called after the mock records
	// the invocation.
	FailRepoCycleFn func(featureID, repoName, errMsg string) error

	// FailRepoImplementationFn lets tests intercept the per-repo
	// implementation failure path. The plan parameter was dropped in
	// SchemaVersionCurrent = 4.
	FailRepoImplementationFn func(featureID, repoName, errMsg string) error

	// RetryPhaseFn lets tests intercept the unified phase-retry path that
	// replaces the old per-repo RetryRepo (deleted in SchemaVersionCurrent = 4).
	RetryPhaseFn func(featureID string, repoNames []string) error

	// Rewind hook.
	RewindToPhaseFn     func(featureID string, targetPhase feature.Phase) ([]string, feature.Phase, error)
	RewindWithRequestFn func(featureID string, request feature.RewindRequest) ([]string, feature.Phase, error)

	DefaultError error
	Calls        []MockCall
}

// NewMockFeatureLifecycle returns an empty MockFeatureLifecycle.
func NewMockFeatureLifecycle() *MockFeatureLifecycle {
	return &MockFeatureLifecycle{}
}

func (m *MockFeatureLifecycle) record(method string, args ...any) {
	m.Calls = append(m.Calls, MockCall{Method: method, Args: args})
}

// ---------------------------------------------------------------------------
// CRUD / Query
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) Create(name, description string, repos []string, models config.ModelConfig,
	exitCriteria, inquireness string, images []string,
	opts ...feature.CreateOptions) (*feature.Feature, error) {
	m.record("Create", name, description, repos, models, exitCriteria, inquireness, images, opts)
	if m.CreateFn != nil {
		return m.CreateFn(name, description, repos, models, exitCriteria, inquireness, images, opts...)
	}
	return nil, m.DefaultError
}

func (m *MockFeatureLifecycle) SlugExists(slug string) (string, error) {
	m.record("SlugExists", slug)
	return "", m.DefaultError
}

func (m *MockFeatureLifecycle) Get(id string) (*feature.Feature, error) {
	m.record("Get", id)
	if m.GetFn != nil {
		return m.GetFn(id)
	}
	return nil, m.DefaultError
}

func (m *MockFeatureLifecycle) List() ([]*feature.Feature, error) {
	m.record("List")
	if m.ListFn != nil {
		return m.ListFn()
	}
	return nil, m.DefaultError
}

func (m *MockFeatureLifecycle) Delete(featureID string) error {
	m.record("Delete", featureID)
	if m.DeleteFn != nil {
		return m.DeleteFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) Transition(id string, to feature.Status) error {
	m.record("Transition", id, to)
	if m.TransitionFn != nil {
		return m.TransitionFn(id, to)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) RunSetup(featureID string, opts ...feature.SetupRunnerOptions) error {
	m.record("RunSetup", featureID, opts)
	if m.RunSetupFn != nil {
		return m.RunSetupFn(featureID, opts...)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) RetrySetup(featureID string, opts ...feature.SetupRunnerOptions) error {
	m.record("RetrySetup", featureID, opts)
	if m.RetrySetupFn != nil {
		return m.RetrySetupFn(featureID, opts...)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) FailActiveSetup(featureID, message string) (bool, error) {
	m.record("FailActiveSetup", featureID, message)
	if m.FailActiveSetupFn != nil {
		return m.FailActiveSetupFn(featureID, message)
	}
	return false, m.DefaultError
}

func (m *MockFeatureLifecycle) ReconcileAbandonedSetups() ([]string, error) {
	m.record("ReconcileAbandonedSetups")
	if m.ReconcileAbandonedSetupsFn != nil {
		return m.ReconcileAbandonedSetupsFn()
	}
	return nil, m.DefaultError
}

// ---------------------------------------------------------------------------
// Knowledge Base phase
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) StartKnowledgeBase(featureID string) error {
	m.record("StartKnowledgeBase", featureID)
	if m.StartKnowledgeBaseFn != nil {
		return m.StartKnowledgeBaseFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) CompleteKnowledgeBase(featureID string) error {
	m.record("CompleteKnowledgeBase", featureID)
	if m.CompleteKnowledgeBaseFn != nil {
		return m.CompleteKnowledgeBaseFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) InitKBStatus(featureID string) error {
	m.record("InitKBStatus", featureID)
	if m.InitKBStatusFn != nil {
		return m.InitKBStatusFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) MarkRepoKBCompleted(featureID, repoName string) error {
	m.record("MarkRepoKBCompleted", featureID, repoName)
	if m.MarkRepoKBCompletedFn != nil {
		return m.MarkRepoKBCompletedFn(featureID, repoName)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) MarkRepoKBFailed(featureID, repoName, errMsg string) error {
	m.record("MarkRepoKBFailed", featureID, repoName, errMsg)
	if m.MarkRepoKBFailedFn != nil {
		return m.MarkRepoKBFailedFn(featureID, repoName, errMsg)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) AllKBsCompleted(featureID string) (bool, error) {
	m.record("AllKBsCompleted", featureID)
	if m.AllKBsCompletedFn != nil {
		return m.AllKBsCompletedFn(featureID)
	}
	return false, m.DefaultError
}

// ---------------------------------------------------------------------------
// Interactive phases (inquire, design, research)
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) StartInquire(featureID string) error {
	m.record("StartInquire", featureID)
	if m.StartInquireFn != nil {
		return m.StartInquireFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) CompleteInquire(featureID string) error {
	m.record("CompleteInquire", featureID)
	if m.CompleteInquireFn != nil {
		return m.CompleteInquireFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) StartDesign(featureID string) error {
	m.record("StartDesign", featureID)
	if m.StartDesignFn != nil {
		return m.StartDesignFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) CompleteDesign(featureID string) error {
	m.record("CompleteDesign", featureID)
	if m.CompleteDesignFn != nil {
		return m.CompleteDesignFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) StartResearch(featureID string) error {
	m.record("StartResearch", featureID)
	if m.StartResearchFn != nil {
		return m.StartResearchFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) CompleteResearch(featureID string) error {
	m.record("CompleteResearch", featureID)
	if m.CompleteResearchFn != nil {
		return m.CompleteResearchFn(featureID)
	}
	return m.DefaultError
}

// ---------------------------------------------------------------------------
// Plan phase
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) StartPlanning(featureID string) error {
	m.record("StartPlanning", featureID)
	if m.StartPlanningFn != nil {
		return m.StartPlanningFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) CompletePlanning(featureID string) error {
	m.record("CompletePlanning", featureID)
	if m.CompletePlanningFn != nil {
		return m.CompletePlanningFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) NeedsPlanReview(featureID string) error {
	m.record("NeedsPlanReview", featureID)
	if m.NeedsPlanReviewFn != nil {
		return m.NeedsPlanReviewFn(featureID)
	}
	return m.DefaultError
}

// ---------------------------------------------------------------------------
// Implement phase
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) StartImplementation(featureID string) error {
	m.record("StartImplementation", featureID)
	if m.StartImplementationFn != nil {
		return m.StartImplementationFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) UpdateIteration(featureID string, iteration int) error {
	m.record("UpdateIteration", featureID, iteration)
	return m.DefaultError
}

func (m *MockFeatureLifecycle) CompleteImplementation(featureID string) error {
	m.record("CompleteImplementation", featureID)
	if m.CompleteImplementationFn != nil {
		return m.CompleteImplementationFn(featureID)
	}
	return m.DefaultError
}

// ---------------------------------------------------------------------------
// Publish / completion
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) MarkCodeReady(featureID string) error {
	m.record("MarkCodeReady", featureID)
	if m.MarkCodeReadyFn != nil {
		return m.MarkCodeReadyFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) MarkFinalReviewReady(featureID string) error {
	m.record("MarkFinalReviewReady", featureID)
	if m.MarkFinalReviewReadyFn != nil {
		return m.MarkFinalReviewReadyFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) MarkPublished(featureID, prURL string) error {
	m.record("MarkPublished", featureID, prURL)
	if m.MarkPublishedFn != nil {
		return m.MarkPublishedFn(featureID, prURL)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) MarkDone(featureID string) error {
	m.record("MarkDone", featureID)
	return m.DefaultError
}

func (m *MockFeatureLifecycle) ReturnToPublished(featureID string) error {
	m.record("ReturnToPublished", featureID)
	return m.DefaultError
}

// ---------------------------------------------------------------------------
// Post-publish cycles
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) StartAddressingReviews(featureID string) error {
	m.record("StartAddressingReviews", featureID)
	if m.StartAddressingReviewsFn != nil {
		return m.StartAddressingReviewsFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) ClearAddressingReviews(featureID string) error {
	m.record("ClearAddressingReviews", featureID)
	if m.ClearAddressingReviewsFn != nil {
		return m.ClearAddressingReviewsFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) StartFeatureRebaseOperation(featureID string) error {
	m.record("StartFeatureRebaseOperation", featureID)
	if m.StartFeatureRebaseOperationFn != nil {
		return m.StartFeatureRebaseOperationFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) MarkFeatureRebaseStage(featureID string, stage feature.RebaseStage) error {
	m.record("MarkFeatureRebaseStage", featureID, stage)
	if m.MarkFeatureRebaseStageFn != nil {
		return m.MarkFeatureRebaseStageFn(featureID, stage)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) UpdateFeatureRebaseRepo(featureID, repoName string, status feature.RebaseRepoStatus, progress feature.RebaseRepoProgress) error {
	m.record("UpdateFeatureRebaseRepo", featureID, repoName, status, progress)
	if m.UpdateFeatureRebaseRepoFn != nil {
		return m.UpdateFeatureRebaseRepoFn(featureID, repoName, status, progress)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) FailFeatureRebaseCycle(featureID, errMsg string) error {
	m.record("FailFeatureRebaseCycle", featureID, errMsg)
	if m.FailFeatureRebaseCycleFn != nil {
		return m.FailFeatureRebaseCycleFn(featureID, errMsg)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) MarkFeatureRebaseNeedUserInput(featureID, gatePath string, iteration int, summary string) error {
	m.record("MarkFeatureRebaseNeedUserInput", featureID, gatePath, iteration, summary)
	if m.MarkFeatureRebaseNeedUserInputFn != nil {
		return m.MarkFeatureRebaseNeedUserInputFn(featureID, gatePath, iteration, summary)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) ClearFeatureRebaseOperation(featureID string) error {
	m.record("ClearFeatureRebaseOperation", featureID)
	if m.ClearFeatureRebaseOperationFn != nil {
		return m.ClearFeatureRebaseOperationFn(featureID)
	}
	return m.DefaultError
}

// ---------------------------------------------------------------------------
// Per-repo cycles
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) StartRepoCycle(featureID, repoName string, cycleType feature.RepoCycleType) error {
	m.record("StartRepoCycle", featureID, repoName, cycleType)
	return m.DefaultError
}

func (m *MockFeatureLifecycle) CompleteRepoCycle(featureID, repoName string) error {
	m.record("CompleteRepoCycle", featureID, repoName)
	if m.CompleteRepoCycleFn != nil {
		return m.CompleteRepoCycleFn(featureID, repoName)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) RemoveRepoCycle(featureID, repoName string) error {
	m.record("RemoveRepoCycle", featureID, repoName)
	return m.DefaultError
}

func (m *MockFeatureLifecycle) FailRepoCycle(featureID, repoName, errMsg string) error {
	m.record("FailRepoCycle", featureID, repoName, errMsg)
	if m.FailRepoCycleFn != nil {
		return m.FailRepoCycleFn(featureID, repoName, errMsg)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) FailRepoImplementation(featureID, repoName, errMsg string) error {
	m.record("FailRepoImplementation", featureID, repoName, errMsg)
	if m.FailRepoImplementationFn != nil {
		return m.FailRepoImplementationFn(featureID, repoName, errMsg)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) MarkRepoCycleReviewing(featureID, repoName string) error {
	m.record("MarkRepoCycleReviewing", featureID, repoName)
	return m.DefaultError
}

func (m *MockFeatureLifecycle) HasActiveRepoCycles(featureID string) (bool, error) {
	m.record("HasActiveRepoCycles", featureID)
	return false, m.DefaultError
}

func (m *MockFeatureLifecycle) ClearRepoCycles(featureID string) error {
	m.record("ClearRepoCycles", featureID)
	return m.DefaultError
}

func (m *MockFeatureLifecycle) SetRepoCyclePlanPath(featureID, repoName, planPath string) error {
	m.record("SetRepoCyclePlanPath", featureID, repoName, planPath)
	return m.DefaultError
}

// ---------------------------------------------------------------------------
// Roadmap phases
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) AdvanceRoadmapPhase(featureID string) error {
	m.record("AdvanceRoadmapPhase", featureID)
	if m.AdvanceRoadmapPhaseFn != nil {
		return m.AdvanceRoadmapPhaseFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) StartRoadmapPhaseImplementation(featureID string) error {
	m.record("StartRoadmapPhaseImplementation", featureID)
	if m.StartRoadmapPhaseImplementationFn != nil {
		return m.StartRoadmapPhaseImplementationFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) CompleteRoadmap(featureID string) error {
	m.record("CompleteRoadmap", featureID)
	return m.DefaultError
}

func (m *MockFeatureLifecycle) RecordRoadmapPhaseCommitAnchors(featureID string, phase int, anchors map[string]string) error {
	m.record("RecordRoadmapPhaseCommitAnchors", featureID, phase, anchors)
	if m.RecordRoadmapPhaseCommitAnchorsFn != nil {
		return m.RecordRoadmapPhaseCommitAnchorsFn(featureID, phase, anchors)
	}
	return m.DefaultError
}

// ---------------------------------------------------------------------------
// Worktree management
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) RecreateWorktree(featureID string) error {
	m.record("RecreateWorktree", featureID)
	return m.DefaultError
}

func (m *MockFeatureLifecycle) CleanWorktree(featureID string) error {
	m.record("CleanWorktree", featureID)
	return m.DefaultError
}

func (m *MockFeatureLifecycle) EnsureWorktree(featureID string) error {
	m.record("EnsureWorktree", featureID)
	return m.DefaultError
}

// ---------------------------------------------------------------------------
// Failure / restart
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) MarkFailed(featureID, failureType, lastError string) error {
	m.record("MarkFailed", featureID, failureType, lastError)
	if m.MarkFailedFn != nil {
		return m.MarkFailedFn(featureID, failureType, lastError)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) RestartFromBeginning(featureID string) error {
	m.record("RestartFromBeginning", featureID)
	return m.DefaultError
}

// ---------------------------------------------------------------------------
// Rewind / pipeline
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) RewindToPhase(featureID string, targetPhase feature.Phase) ([]string, feature.Phase, error) {
	m.record("RewindToPhase", featureID, targetPhase)
	if m.RewindToPhaseFn != nil {
		return m.RewindToPhaseFn(featureID, targetPhase)
	}
	return nil, 0, m.DefaultError
}

func (m *MockFeatureLifecycle) RewindWithRequest(featureID string, request feature.RewindRequest) ([]string, feature.Phase, error) {
	m.record("RewindWithRequest", featureID, request)
	if m.RewindWithRequestFn != nil {
		return m.RewindWithRequestFn(featureID, request)
	}
	return nil, 0, m.DefaultError
}

func (m *MockFeatureLifecycle) UpgradePipeline(featureID string, newProfile feature.PipelineProfile) error {
	m.record("UpgradePipeline", featureID, newProfile)
	return m.DefaultError
}

// ---------------------------------------------------------------------------
// Multi-repo implementation state
// ---------------------------------------------------------------------------

func (m *MockFeatureLifecycle) InitRepoImpl(featureID string) error {
	m.record("InitRepoImpl", featureID)
	if m.InitRepoImplFn != nil {
		return m.InitRepoImplFn(featureID)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) SetRepoPublished(featureID, repoName, prURL string) error {
	m.record("SetRepoPublished", featureID, repoName, prURL)
	if m.SetRepoPublishedFn != nil {
		return m.SetRepoPublishedFn(featureID, repoName, prURL)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) SetRepoPublishError(featureID, repoName, errMsg string) error {
	m.record("SetRepoPublishError", featureID, repoName, errMsg)
	if m.SetRepoPublishErrorFn != nil {
		return m.SetRepoPublishErrorFn(featureID, repoName, errMsg)
	}
	return m.DefaultError
}

func (m *MockFeatureLifecycle) TryCompletePublish(featureID string) (bool, error) {
	m.record("TryCompletePublish", featureID)
	if m.TryCompletePublishFn != nil {
		return m.TryCompletePublishFn(featureID)
	}
	return false, m.DefaultError
}

func (m *MockFeatureLifecycle) RetryPhase(featureID string, repoNames []string) error {
	m.record("RetryPhase", featureID, repoNames)
	if m.RetryPhaseFn != nil {
		return m.RetryPhaseFn(featureID, repoNames)
	}
	return m.DefaultError
}
