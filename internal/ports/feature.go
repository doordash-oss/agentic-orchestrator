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

package ports

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// FeatureStore abstracts filesystem-backed feature persistence.
// Satisfied by: *feature.Store
type FeatureStore interface {
	Save(f *feature.Feature) error
	Load(id string) (*feature.Feature, error)
	Modify(id string, fn func(f *feature.Feature) error) error
	List() ([]*feature.Feature, error)
	Delete(id string) error

	// Run persistence. Added in Phase 1 (runs-first state layout).
	CreateRun(featureID string, r *feature.Run) error
	LoadRun(featureID string, runNumber int) (*feature.Run, error)
	SaveRun(featureID string, r *feature.Run) error
	SealAndForkRun(
		featureID string,
		seal func(oldRun *feature.Run) error,
		fork func(oldRun *feature.Run) (*feature.Run, error),
		populate func(oldRun, newRun *feature.Run) error,
	) (*feature.Feature, error)

	// CleanupOrphanRuns removes run directories for a feature that are stale
	// from a crashed rewind: run_number > ActiveRun, or loadable run.yaml
	// with Committing:true. Preserves sealed runs with run_number <= ActiveRun.
	// When deletions cause max(run_number on disk) < ActiveRun, rolls
	// ActiveRun and RunCount back to max(run_number on disk) and rewrites
	// feature.yaml atomically. Returns the sorted list of deleted run numbers.
	// Added in Phase 3.
	CleanupOrphanRuns(id string) ([]int, error)
}

// FeatureLifecycle abstracts feature lifecycle coordination.
// Satisfied by: *feature.Manager
type FeatureLifecycle interface {
	// CRUD / Query
	Create(name, description string, repos []string, models config.ModelConfig,
		exitCriteria, inquireness string, images []string,
		opts ...feature.CreateOptions) (*feature.Feature, error)
	SlugExists(slug string) (string, error)
	Get(id string) (*feature.Feature, error)
	List() ([]*feature.Feature, error)
	Delete(featureID string) error
	Transition(id string, to feature.Status) error

	// Knowledge Base phase
	StartKnowledgeBase(featureID string) error
	CompleteKnowledgeBase(featureID string) error
	InitKBStatus(featureID string) error
	MarkRepoKBCompleted(featureID, repoName string) error
	MarkRepoKBFailed(featureID, repoName, errMsg string) error
	AllKBsCompleted(featureID string) (bool, error)

	// Interactive phases (inquire, research, brainstorm)
	StartInquire(featureID string) error
	CompleteInquire(featureID string) error
	StartBrainstorm(featureID string) error
	CompleteBrainstorm(featureID string) error
	StartResearch(featureID string) error
	CompleteResearch(featureID string) error

	// Plan phase
	StartPlanning(featureID string) error
	CompletePlanning(featureID string) error
	NeedsPlanReview(featureID string) error

	// Implement phase
	StartImplementation(featureID string) error
	UpdateIteration(featureID string, iteration int) error
	CompleteImplementation(featureID string) error

	// Publish / completion
	MarkCodeReady(featureID string) error
	// MarkFinalReviewReady transitions the feature into the deferred
	// end-of-feature Final Review pass: Status becomes StatusFinalReviewing
	// and CurrentPhase becomes PhaseFinalReview. The orchestrator calls this
	// before invoking RunMultiRepoFinalReview after the last roadmap-phase's
	// implement returns all_passed.
	MarkFinalReviewReady(featureID string) error
	MarkPublished(featureID, prURL string) error
	MarkDone(featureID string) error
	ReturnToPublished(featureID string) error

	// Post-publish cycles
	CompleteRefactor(featureID string) error
	StartAddressingReviews(featureID string) error
	ClearAddressingReviews(featureID string) error

	// Per-repo cycles
	StartRepoCycle(featureID, repoName string, cycleType feature.RepoCycleType) error
	CompleteRepoCycle(featureID, repoName string) error
	RemoveRepoCycle(featureID, repoName string) error
	FailRepoCycle(featureID, repoName, errMsg string) error
	MarkRepoCycleReviewing(featureID, repoName string) error
	HasActiveRepoCycles(featureID string) (bool, error)
	ClearRepoCycles(featureID string) error
	SetRepoCyclePlanPath(featureID, repoName, planPath string) error

	// Roadmap phases
	AdvanceRoadmapPhase(featureID string) error
	StartRoadmapPhaseImplementation(featureID string) error
	CompleteRoadmap(featureID string) error
	RecordRoadmapPhaseCommitAnchors(featureID string, phase int, anchors map[string]string) error

	// Worktree management
	RecreateWorktree(featureID string) error
	CleanWorktree(featureID string) error
	EnsureWorktree(featureID string) error

	// Failure / restart
	MarkFailed(featureID, failureType, lastError string) error
	RestartFromBeginning(featureID string) error

	// Rewind / pipeline
	RewindToPhase(featureID string, targetPhase feature.Phase) ([]string, feature.Phase, error)
	RewindWithRequest(featureID string, request feature.RewindRequest) ([]string, feature.Phase, error)
	UpgradePipeline(featureID string, newProfile feature.PipelineProfile) error

	// Per-repo implementation state. The unified flow tracks per-repo
	// signal in RepoStates (Touched, PRURL, LastError); orchestration
	// readers consume it via Feature.AllReposPublished / Feature.TouchedRepos.
	InitRepoImpl(featureID string) error
	SetRepoPublished(featureID, repoName, prURL string) error
	SetRepoPublishError(featureID, repoName, errMsg string) error
	TryCompletePublish(featureID string) (bool, error)
	// RetryPhase clears feature-level error/gate state so the unified
	// phase-implement loop can re-run the active phase from iteration 1.
	// Per-repo Touched flags are monotonic and intentionally preserved.
	RetryPhase(featureID string, repoNames []string) error
	// FailRepoImplementation records a failure error on one repo's state.
	// Phase-atomic failures land via agent.AtomicPhaseStamp(PhaseOutcomeFailed);
	// this helper survives for cycle-cleanup callers.
	FailRepoImplementation(featureID, repoName, errMsg string) error
}
