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

package orchestrator_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// TestOrchestrator_ApplyRefactorPipeline_SetsProfileAndCheckpoints
// ---------------------------------------------------------------------------
// ApplyRefactorPipeline replaces the TUI's Store.Modify for
// applyRefactorPipelineAndStart. Unlike UpgradePipeline it permits any
// profile (including downgrade) because refactor resets the cycle.
// ---------------------------------------------------------------------------

func TestOrchestrator_ApplyRefactorPipeline_SetsProfileAndCheckpoints(t *testing.T) {
	f := &feature.Feature{
		ID:       "feat-1",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineLarge,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	if err := o.ApplyRefactorPipeline("feat-1", feature.PipelineMedium); err != nil {
		t.Fatalf("ApplyRefactorPipeline: %v", err)
	}

	if f.Pipeline != feature.PipelineMedium {
		t.Errorf("Pipeline = %v, want PipelineMedium", f.Pipeline)
	}
	// Medium disables auto-publish by default; Checkpoints should reflect the
	// profile-specific defaults.
	defaults := feature.DefaultCheckpointsForProfile(feature.PipelineMedium)
	if f.Checkpoints != defaults {
		t.Errorf("Checkpoints = %+v, want %+v", f.Checkpoints, defaults)
	}
}

// TestOrchestrator_EnterReviewGate_SetsStatusAndPendingPhase
// ---------------------------------------------------------------------------
// EnterReviewGate replaces triggerReviewGateCmd's Store.Modify. It flips the
// status to the NeedsReview variant and records the target phase so the UI
// can render the review editor on a later tick.
// ---------------------------------------------------------------------------

func TestOrchestrator_EnterReviewGate_SetsStatusAndPendingPhase(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-1",
		Status: feature.StatusImplementReady,
		HelpQueue: []feature.HelpRequest{
			{Question: "stale question", Pending: true},
		},
		PermissionsQueue: []feature.PermissionRequest{
			{Tool: "Bash", Pending: true},
		},
		IsRewind: true, // Should be cleared by EnterReviewGate.
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	if err := o.EnterReviewGate("feat-1", feature.PhaseImplement); err != nil {
		t.Fatalf("EnterReviewGate: %v", err)
	}

	wantStatus := feature.NeedsReviewForPhase(feature.PhaseImplement)
	if f.Status != wantStatus {
		t.Errorf("Status = %v, want %v", f.Status, wantStatus)
	}
	if f.PendingReviewPhase == nil || *f.PendingReviewPhase != feature.PhaseImplement {
		t.Errorf("PendingReviewPhase = %v, want PhaseImplement", f.PendingReviewPhase)
	}
	if f.IsRewind {
		t.Errorf("IsRewind should be cleared after entering a review gate")
	}
	if f.HelpQueue[0].Pending {
		t.Errorf("HelpQueue pending flag should be cleared after entering a review gate")
	}
	if f.PermissionsQueue[0].Pending {
		t.Errorf("PermissionsQueue pending flag should be cleared after entering a review gate")
	}
}

func TestOrchestrator_RewindWithRequest_FiresAuditHookAfterSuccess(t *testing.T) {
	f := &feature.Feature{ID: "feat-rewind", ActiveRun: 1}
	lc := lifecycleForFeature(f)
	lc.RewindWithRequestFn = func(featureID string, request feature.RewindRequest) ([]string, feature.Phase, error) {
		f.ActiveRun = 2
		return []string{"backup warning"}, feature.PhaseImplement, nil
	}
	fs := newFeatureStore(f)

	var gotFeatureID string
	var gotRequest feature.RewindRequest
	var gotEffective feature.Phase
	var gotSourceRun, gotNewRun int
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{
		OnFeatureRewound: func(featureID string, request feature.RewindRequest, effectiveTarget feature.Phase, sourceRun, newRun int) {
			gotFeatureID = featureID
			gotRequest = request
			gotEffective = effectiveTarget
			gotSourceRun = sourceRun
			gotNewRun = newRun
		},
	})

	warnings, effective, err := o.RewindWithRequest("feat-rewind", feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 2,
	})
	if err != nil {
		t.Fatalf("RewindWithRequest: %v", err)
	}
	if len(warnings) != 1 || warnings[0] != "backup warning" {
		t.Fatalf("warnings = %v, want backup warning", warnings)
	}
	if effective != feature.PhaseImplement {
		t.Fatalf("effective = %v, want PhaseImplement", effective)
	}
	if gotFeatureID != "feat-rewind" {
		t.Errorf("hook featureID = %q, want feat-rewind", gotFeatureID)
	}
	if gotRequest.RoadmapPhase != 2 || gotRequest.TargetPhase != feature.PhaseImplement {
		t.Errorf("hook request = %+v, want implement phase 2", gotRequest)
	}
	if gotEffective != feature.PhaseImplement {
		t.Errorf("hook effective = %v, want PhaseImplement", gotEffective)
	}
	if gotSourceRun != 1 || gotNewRun != 2 {
		t.Errorf("hook source/new run = %d/%d, want 1/2", gotSourceRun, gotNewRun)
	}
	select {
	case ev := <-o.Events():
		if ev.Type != ports.FeatureRewound || ev.FeatureID != "feat-rewind" || ev.Phase != feature.PhaseImplement {
			t.Fatalf("event = %+v, want FeatureRewound for feat-rewind implement", ev)
		}
	default:
		t.Fatal("RewindWithRequest emitted no domain event, want FeatureRewound")
	}
}

// TestOrchestrator_ResetToPublishedFromTweak_PublishedBranch
// ---------------------------------------------------------------------------
// ResetToPublishedFromTweak restores a feature's pre-tweak state. For a
// feature with a PR URL, Status flips to Published; for one without, it flips
// to CodeReady. ActiveCycleType + failure fields are cleared unconditionally.
// ---------------------------------------------------------------------------

func TestOrchestrator_ResetToPublishedFromTweak_PublishedBranch(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-1",
		Status: feature.StatusFailed,
		Repos:  []feature.FeatureRepo{{Name: "repo-a"}},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://example.com/pr/1"},
		},
		LastError:    "boom",
		FailureType:  feature.FailureInfrastructure,
		CurrentPhase: feature.PhaseImplement,
	}
	f.SetActiveCycleType(feature.CycleTweak)
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	if err := o.ResetToPublishedFromTweak("feat-1"); err != nil {
		t.Fatalf("ResetToPublishedFromTweak: %v", err)
	}

	if f.Status != feature.StatusPublished {
		t.Errorf("Status = %v, want StatusPublished (feature has PRURL)", f.Status)
	}
	if f.CurrentPhase != feature.PhasePublish {
		t.Errorf("CurrentPhase = %v, want PhasePublish", f.CurrentPhase)
	}
	if f.ActiveCycleType() != "" {
		t.Errorf("ActiveCycleType = %v, want empty", f.ActiveCycleType())
	}
	if f.LastError != "" || f.FailureType != "" {
		t.Errorf("failure fields not cleared: LastError=%q FailureType=%q", f.LastError, f.FailureType)
	}
}

func TestOrchestrator_ResetToPublishedFromTweak_CodeReadyBranch(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-1",
		Status: feature.StatusInterrupted,
	}
	f.SetActiveCycleType(feature.CycleTweak)
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	if err := o.ResetToPublishedFromTweak("feat-1"); err != nil {
		t.Fatalf("ResetToPublishedFromTweak: %v", err)
	}

	if f.Status != feature.StatusCodeReady {
		t.Errorf("Status = %v, want StatusCodeReady (no PRURL)", f.Status)
	}
}

// TestOrchestrator_ExtendFailedPhaseBudget_BumpsAndClears
// ---------------------------------------------------------------------------
// ExtendFailedPhaseBudget extends MaxIterations when the failure type is
// FailureMaxIterations and MaxPlanIterations when the current phase is Plan.
// It always clears LastError / FailureType so restart can proceed.
// ---------------------------------------------------------------------------

func TestOrchestrator_ExtendFailedPhaseBudget_BumpsAndClears(t *testing.T) {
	f := &feature.Feature{
		ID:                "feat-1",
		Status:            feature.StatusFailed,
		FailureType:       feature.FailureMaxIterations,
		CurrentPhase:      feature.PhasePlan,
		MaxIterations:     20,
		MaxPlanIterations: 3,
		LastError:         "iteration cap",
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	if err := o.ExtendFailedPhaseBudget("feat-1", 10, 2); err != nil {
		t.Fatalf("ExtendFailedPhaseBudget: %v", err)
	}

	if f.MaxIterations != 30 {
		t.Errorf("MaxIterations = %d, want 30", f.MaxIterations)
	}
	if f.MaxPlanIterations != 5 {
		t.Errorf("MaxPlanIterations = %d, want 5", f.MaxPlanIterations)
	}
	if f.LastError != "" || f.FailureType != "" {
		t.Errorf("failure fields not cleared: LastError=%q FailureType=%q", f.LastError, f.FailureType)
	}
}

func TestOrchestrator_ExtendFailedPhaseBudget_NotFailedIsNoOp(t *testing.T) {
	f := &feature.Feature{
		ID:            "feat-1",
		Status:        feature.StatusInterrupted,
		MaxIterations: 20,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	if err := o.ExtendFailedPhaseBudget("feat-1", 10, 2); err != nil {
		t.Fatalf("ExtendFailedPhaseBudget: %v", err)
	}

	if f.MaxIterations != 20 {
		t.Errorf("MaxIterations should not change for non-Failed feature, got %d", f.MaxIterations)
	}
}

// TestOrchestrator_CollectAndClearRepoCycleRestarts_ReadsPlansAndClears
// ---------------------------------------------------------------------------
// CollectAndClearRepoCycleRestarts snapshots the feature's RepoCycles map,
// reads each cycle's plan file from disk, clears the cycle state via
// Lifecycle.ClearRepoCycles, and returns restart descriptors the TUI dispatches.
// ---------------------------------------------------------------------------

func TestOrchestrator_CollectAndClearRepoCycleRestarts_ReadsPlansAndClears(t *testing.T) {
	tmp := t.TempDir()
	reviewPath := filepath.Join(tmp, "review-plan.md")
	refactorPath := filepath.Join(tmp, "refactor-plan.md")
	if err := os.WriteFile(reviewPath, []byte("review content"), 0o644); err != nil {
		t.Fatalf("write review plan: %v", err)
	}
	if err := os.WriteFile(refactorPath, []byte("# Refactor: repo-b\n\nmove packages around"), 0o644); err != nil {
		t.Fatalf("write refactor plan: %v", err)
	}

	f := &feature.Feature{
		ID:     "feat-1",
		Status: feature.StatusPublished,
		RepoCycles: map[string]*feature.RepoCycleState{
			"repo-a": {Type: feature.CycleReviewComments, PlanPath: reviewPath},
			"repo-b": {Type: feature.CycleRefactor, PlanPath: refactorPath},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	restarts, refactor, err := o.CollectAndClearRepoCycleRestarts("feat-1")
	if err != nil {
		t.Fatalf("CollectAndClearRepoCycleRestarts: %v", err)
	}
	assertLifecycleCall(t, lc, "ClearRepoCycles")
	if len(restarts) != 1 {
		t.Fatalf("expected 1 non-refactor restart, got %d", len(restarts))
	}
	if restarts[0].RepoName != "repo-a" || restarts[0].CycleType != feature.CycleReviewComments {
		t.Errorf("unexpected restart descriptor: %+v", restarts[0])
	}
	if restarts[0].PlanContent != "review content" {
		t.Errorf("restart PlanContent = %q, want %q", restarts[0].PlanContent, "review content")
	}

	if refactor == nil {
		t.Fatal("expected refactor restart, got nil")
	}
	if refactor.RepoName != "repo-b" {
		t.Errorf("refactor RepoName = %q, want repo-b", refactor.RepoName)
	}
	if refactor.Prompt != "move packages around" {
		t.Errorf("refactor Prompt = %q, want %q", refactor.Prompt, "move packages around")
	}
}

func TestOrchestrator_CollectAndClearRepoCycleRestarts_UsesPersistedRefactorPromptWhenPlanPathMissing(t *testing.T) {
	f := &feature.Feature{
		ID:             "feat-1",
		Status:         feature.StatusInterrupted,
		RefactorPrompt: "keep restart prompt",
		RepoCycles: map[string]*feature.RepoCycleState{
			"repo-b": {Type: feature.CycleRefactor, Status: feature.RepoCycleInterrupted},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	restarts, refactor, err := o.CollectAndClearRepoCycleRestarts("feat-1")
	if err != nil {
		t.Fatalf("CollectAndClearRepoCycleRestarts: %v", err)
	}
	if len(restarts) != 0 {
		t.Fatalf("expected no non-refactor restarts, got %d", len(restarts))
	}
	if refactor == nil {
		t.Fatal("expected refactor restart, got nil")
	}
	if refactor.RepoName != "repo-b" {
		t.Errorf("refactor RepoName = %q, want repo-b", refactor.RepoName)
	}
	if refactor.Prompt != "keep restart prompt" {
		t.Errorf("refactor Prompt = %q, want %q", refactor.Prompt, "keep restart prompt")
	}
}

func TestOrchestrator_CollectAndClearRepoCycleRestarts_SkipsTweakCycles(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-1",
		Status: feature.StatusPublished,
		RepoCycles: map[string]*feature.RepoCycleState{
			"repo-a": {Type: feature.CycleTweak, PlanPath: ""},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	restarts, refactor, err := o.CollectAndClearRepoCycleRestarts("feat-1")
	if err != nil {
		t.Fatalf("CollectAndClearRepoCycleRestarts: %v", err)
	}
	assertLifecycleCall(t, lc, "ClearRepoCycles")
	if len(restarts) != 0 {
		t.Errorf("tweak cycles must not produce restart descriptors; got %d", len(restarts))
	}
	if refactor != nil {
		t.Errorf("tweak cycles must not produce a refactor descriptor; got %+v", refactor)
	}
}

// ---------------------------------------------------------------------------
// Iteration 13 additions: RestartPhase + ResolveGateReviewContext
// ---------------------------------------------------------------------------

// TestOrchestrator_RestartPhase_TweakCycle_ResetsWithoutDispatch
// ---------------------------------------------------------------------------
// When a feature has an active tweak cycle, RestartPhase routes through
// ResetToPublishedFromTweak (force-set pre-tweak state) and returns
// RestartNoOp so the TUI just refreshes — no StartPhaseMsg is emitted.
// ---------------------------------------------------------------------------

func TestOrchestrator_RestartPhase_TweakCycle_ResetsWithoutDispatch(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-1",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos:        []feature.FeatureRepo{{Name: "repo-a"}},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://example.com/pr/1"},
		},
		LastError:   "boom",
		FailureType: feature.FailureInfrastructure,
	}
	f.SetActiveCycleType(feature.CycleTweak)
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-1", 10, 2)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartNoOp {
		t.Errorf("Action = %v, want RestartNoOp", outcome.Action)
	}
	if f.ActiveCycleType() != "" {
		t.Errorf("ActiveCycleType should be cleared, got %q", f.ActiveCycleType())
	}
	if f.Status != feature.StatusPublished {
		t.Errorf("Status = %v, want Published (PRURL set)", f.Status)
	}
	if f.LastError != "" || f.FailureType != "" {
		t.Errorf("failure fields should be cleared: LastError=%q FailureType=%q", f.LastError, f.FailureType)
	}
}

// TestOrchestrator_RestartPhase_FailedPlan_ExtendsBudgetAndDispatches
// ---------------------------------------------------------------------------
// A Failed feature at PhasePlan walks status back to PlanReady (via two
// Transition hops) and returns RestartDispatchPhase{Phase: Plan}. The
// iteration budget deltas are applied by ExtendFailedPhaseBudget.
// ---------------------------------------------------------------------------

func TestOrchestrator_RestartPhase_FailedPlan_ExtendsBudgetAndDispatches(t *testing.T) {
	f := &feature.Feature{
		ID:                "feat-1",
		Status:            feature.StatusFailed,
		CurrentPhase:      feature.PhasePlan,
		FailureType:       feature.FailureMaxIterations,
		MaxIterations:     20,
		MaxPlanIterations: 3,
		LastError:         "iteration cap",
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-1", 10, 2)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchPhase {
		t.Fatalf("Action = %v, want RestartDispatchPhase", outcome.Action)
	}
	if outcome.Phase != feature.PhasePlan {
		t.Errorf("Phase = %v, want PhasePlan", outcome.Phase)
	}
	if f.Status != feature.StatusPlanReady {
		t.Errorf("Status = %v, want PlanReady", f.Status)
	}
	if f.MaxIterations != 30 {
		t.Errorf("MaxIterations = %d, want 30 (bumped by delta=10)", f.MaxIterations)
	}
	if f.MaxPlanIterations != 5 {
		t.Errorf("MaxPlanIterations = %d, want 5 (bumped by delta=2)", f.MaxPlanIterations)
	}
	if f.LastError != "" || f.FailureType != "" {
		t.Errorf("failure fields should be cleared: LastError=%q FailureType=%q", f.LastError, f.FailureType)
	}
}

func TestOrchestrator_RestartPhase_FailedFinalReview_DispatchesFinalReview(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-fr",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseFinalReview,
		FailureType:  feature.FailureProtocolViolation,
		LastError:    "protocol violation: final_review_reviewer @ /tmp/iter: invalid report",
		Repos:        []feature.FeatureRepo{{Name: "agentic"}},
		RepoStates: map[string]*feature.RepoState{
			"agentic": {
				Touched:   true,
				LastError: "protocol violation: final_review_reviewer @ /tmp/iter: invalid report",
			},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-fr", 10, 2)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchPhase {
		t.Fatalf("Action = %v, want RestartDispatchPhase", outcome.Action)
	}
	if outcome.Phase != feature.PhaseFinalReview {
		t.Fatalf("Phase = %v, want PhaseFinalReview", outcome.Phase)
	}
	if f.Status != feature.StatusReviewPassed {
		t.Fatalf("Status = %v, want ReviewPassed so startFinalReview can re-enter", f.Status)
	}
	if f.CurrentPhase != feature.PhaseFinalReview {
		t.Fatalf("CurrentPhase = %v, want PhaseFinalReview", f.CurrentPhase)
	}
	if f.LastError != "" || f.FailureType != "" {
		t.Fatalf("failure fields should be cleared: LastError=%q FailureType=%q", f.LastError, f.FailureType)
	}
	if st := f.RepoStates["agentic"]; st == nil || st.LastError != "" {
		t.Fatalf("RepoStates[agentic] = %+v, want LastError cleared", st)
	}
}

// TestOrchestrator_RestartPhase_PublishedWithRepoCycles_ReturnsRestartList
// ---------------------------------------------------------------------------
// For a Published feature with RepoCycles, RestartPhase clears the cycles
// and returns RestartDispatchRepoCycles with per-cycle descriptors the TUI
// must fan out.
// ---------------------------------------------------------------------------

func TestOrchestrator_RestartPhase_PublishedWithRepoCycles_ReturnsRestartList(t *testing.T) {
	tmp := t.TempDir()
	planPath := filepath.Join(tmp, "review-plan.md")
	if err := os.WriteFile(planPath, []byte("review plan"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	f := &feature.Feature{
		ID:     "feat-1",
		Status: feature.StatusPublished,
		RepoCycles: map[string]*feature.RepoCycleState{
			"repo-a": {Type: feature.CycleReviewComments, PlanPath: planPath},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-1", 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchRepoCycles {
		t.Fatalf("Action = %v, want RestartDispatchRepoCycles", outcome.Action)
	}
	if len(outcome.RepoCycleRestarts) != 1 {
		t.Fatalf("expected 1 repo-cycle restart, got %d", len(outcome.RepoCycleRestarts))
	}
	if outcome.RepoCycleRestarts[0].RepoName != "repo-a" {
		t.Errorf("RepoName = %q, want repo-a", outcome.RepoCycleRestarts[0].RepoName)
	}
	if outcome.RepoCycleRestarts[0].PlanContent != "review plan" {
		t.Errorf("PlanContent = %q, want %q", outcome.RepoCycleRestarts[0].PlanContent, "review plan")
	}
	assertLifecycleCall(t, lc, "ClearRepoCycles")
}

func TestOrchestrator_RestartPhase_InterruptedWithRepoCycles_ReturnsRestartList(t *testing.T) {
	tmp := t.TempDir()
	planPath := filepath.Join(tmp, "rebase-plan.md")
	if err := os.WriteFile(planPath, []byte("rebase plan"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	f := &feature.Feature{
		ID:           "feat-1",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhasePublish,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/example/repo/pull/1"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"repo-a": {Type: feature.CycleRebase, Status: feature.RepoCycleInterrupted, PlanPath: planPath},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-1", 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchRepoCycles {
		t.Fatalf("Action = %v, want RestartDispatchRepoCycles", outcome.Action)
	}
	if len(outcome.RepoCycleRestarts) != 1 {
		t.Fatalf("expected 1 repo-cycle restart, got %d", len(outcome.RepoCycleRestarts))
	}
	if outcome.RepoCycleRestarts[0].RepoName != "repo-a" {
		t.Errorf("RepoName = %q, want repo-a", outcome.RepoCycleRestarts[0].RepoName)
	}
	if outcome.RepoCycleRestarts[0].PlanContent != "rebase plan" {
		t.Errorf("PlanContent = %q, want %q", outcome.RepoCycleRestarts[0].PlanContent, "rebase plan")
	}
	if f.Status != feature.StatusPublished {
		t.Errorf("Status after restart preparation = %v, want Published", f.Status)
	}
	assertLifecycleCall(t, lc, "ClearRepoCycles")
}

// TestOrchestrator_RestartPhase_RunningResearch_TransitionsToInterrupted
// ---------------------------------------------------------------------------
// A feature actively running the research phase moves to StatusInterrupted
// so the next StartFeature dispatch can re-enter the phase cleanly.
// ---------------------------------------------------------------------------

func TestOrchestrator_RestartPhase_RunningResearch_TransitionsToInterrupted(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-1",
		Status:       feature.StatusResearching,
		CurrentPhase: feature.PhaseResearch,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-1", 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchPhase {
		t.Fatalf("Action = %v, want RestartDispatchPhase", outcome.Action)
	}
	if outcome.Phase != feature.PhaseResearch {
		t.Errorf("Phase = %v, want PhaseResearch", outcome.Phase)
	}
	if f.Status != feature.StatusInterrupted {
		t.Errorf("Status = %v, want Interrupted", f.Status)
	}
}

func TestOrchestrator_RestartPhase_NeedsReviewRestartsCompletedPhase(t *testing.T) {
	target := feature.PhaseResearch
	f := &feature.Feature{
		ID:                 "feat-1",
		Status:             feature.StatusInquiryNeedsReview,
		CurrentPhase:       feature.PhaseInquire,
		PendingReviewPhase: &target,
		HelpQueue: []feature.HelpRequest{
			{Question: "Agent is waiting for input — press 'a' to answer", Pending: true},
		},
		PermissionsQueue: []feature.PermissionRequest{
			{Tool: "Bash", Pending: true},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-1", 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchPhase {
		t.Fatalf("Action = %v, want RestartDispatchPhase", outcome.Action)
	}
	if outcome.Phase != feature.PhaseInquire {
		t.Errorf("Phase = %v, want PhaseInquire", outcome.Phase)
	}
	if f.Status != feature.StatusInterrupted {
		t.Errorf("Status = %v, want Interrupted", f.Status)
	}
	if f.PendingReviewPhase != nil {
		t.Errorf("PendingReviewPhase = %v, want nil", f.PendingReviewPhase)
	}
	for _, h := range f.HelpQueue {
		if h.Pending {
			t.Fatalf("HelpQueue still has pending entry: %+v", h)
		}
	}
	for _, p := range f.PermissionsQueue {
		if p.Pending {
			t.Fatalf("PermissionsQueue still has pending entry: %+v", p)
		}
	}
}

func TestOrchestrator_RestartPhase_FailedSingleShotPhase_TransitionsToSamePhaseStartableStatus(t *testing.T) {
	tests := []struct {
		name       string
		phase      feature.Phase
		wantStatus feature.Status
	}{
		{
			name:       "failed inquire restarts inquire",
			phase:      feature.PhaseInquire,
			wantStatus: feature.StatusInquiring,
		},
		{
			name:       "failed research restarts research",
			phase:      feature.PhaseResearch,
			wantStatus: feature.StatusResearching,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{
				ID:           "feat-1",
				Status:       feature.StatusFailed,
				CurrentPhase: tt.phase,
			}
			lc := lifecycleForFeature(f)
			fs := newFeatureStore(f)

			o := orchestrator.New(orchestrator.Deps{
				Lifecycle: lc,
				Store:     fs,
			}, orchestrator.Hooks{})

			outcome, err := o.RestartPhase("feat-1", 0, 0)
			if err != nil {
				t.Fatalf("RestartPhase: %v", err)
			}
			if outcome.Action != orchestrator.RestartDispatchPhase {
				t.Fatalf("Action = %v, want RestartDispatchPhase", outcome.Action)
			}
			if outcome.Phase != tt.phase {
				t.Errorf("Phase = %v, want %v", outcome.Phase, tt.phase)
			}
			if f.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %v", f.Status, tt.wantStatus)
			}
		})
	}
}

// TestOrchestrator_RestartPhase_InterruptedFeature_KeepsStatus
// ---------------------------------------------------------------------------
// A feature already at StatusInterrupted needs no transition — StartFeature
// handles Interrupted → working-state. RestartPhase returns RestartDispatchPhase
// without any additional Transition call.
// ---------------------------------------------------------------------------

func TestOrchestrator_RestartPhase_InterruptedFeature_KeepsStatus(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-1",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseImplement,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-1", 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchPhase {
		t.Fatalf("Action = %v, want RestartDispatchPhase", outcome.Action)
	}
	if outcome.Phase != feature.PhaseImplement {
		t.Errorf("Phase = %v, want PhaseImplement", outcome.Phase)
	}
	if f.Status != feature.StatusInterrupted {
		t.Errorf("Status = %v, want Interrupted (unchanged)", f.Status)
	}
}

// TestOrchestrator_RestartPhase_CreatedFeature_DispatchesWithoutTransition
// ---------------------------------------------------------------------------
// Regression: a feature stranded in StatusCreated with a non-zero CurrentPhase
// (e.g. wakeKBWaiters' allFresh path before the startPhase fix dropped the
// PhaseSkipped recursion) must be recoverable via Restart. Pre-fix the default
// branch attempted Created → Interrupted, which is not in
// validTransitions[StatusCreated], so RestartPhase errored out and the TUI
// silently swallowed the failure — pressing 'r' did nothing.
// ---------------------------------------------------------------------------

func TestOrchestrator_RestartPhase_CreatedFeature_DispatchesWithoutTransition(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-1",
		Status:       feature.StatusCreated,
		CurrentPhase: feature.PhaseKnowledgeBase,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-1", 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchPhase {
		t.Fatalf("Action = %v, want RestartDispatchPhase", outcome.Action)
	}
	if outcome.Phase != feature.PhaseKnowledgeBase {
		t.Errorf("Phase = %v, want PhaseKnowledgeBase", outcome.Phase)
	}
	if f.Status != feature.StatusCreated {
		t.Errorf("Status = %v, want Created (unchanged — startKB owns the forward transition)", f.Status)
	}
}

// TestOrchestrator_ResolveGateReviewContext_PhaseImplement_ReturnsPlan
// ---------------------------------------------------------------------------
// Gate-review for PhaseImplement resolves the plan artifact on non-roadmap
// features and routes through the orchestrator's path-resolution helpers.
// ---------------------------------------------------------------------------

func TestOrchestrator_ResolveGateReviewContext_PhaseImplement_ReturnsPlan(t *testing.T) {
	tmp := t.TempDir()
	planDir := filepath.Join(tmp, "feat-1", "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	planPath := filepath.Join(planDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	f := &feature.Feature{
		ID:           "feat-1",
		Status:       feature.StatusImplementReady,
		CurrentPhase: feature.PhaseImplement,
		Artifacts:    map[string]string{"plan": planPath},
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", WorktreePath: "/tmp/repo-a-worktree", Path: "/tmp/repo-a"},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	// Use the feature.Store seam so stateDir() resolves for the orchestrator's
	// path-resolution helpers. Test uses the lifecycle-backed store here.
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     feature.NewStore(tmp),
	}, orchestrator.Hooks{})
	_ = fs // unused here; keeps the helper import exercised.

	ctx, err := o.ResolveGateReviewContext("feat-1", feature.PhaseImplement)
	if err != nil {
		t.Fatalf("ResolveGateReviewContext: %v", err)
	}
	if ctx.ArtifactPath != planPath {
		t.Errorf("ArtifactPath = %q, want %q", ctx.ArtifactPath, planPath)
	}
	if ctx.WorkDir != "/tmp/repo-a-worktree" {
		t.Errorf("WorkDir = %q, want /tmp/repo-a-worktree", ctx.WorkDir)
	}
}

// TestOrchestrator_ResolveGateReviewContext_RoadmapPhaseZero_ReturnsRoadmap
// ---------------------------------------------------------------------------
// For a roadmap feature whose current roadmap phase is zero, gate-review
// opens the roadmap artifact — this is the "initial roadmap review" gate.
// ---------------------------------------------------------------------------

func TestOrchestrator_ResolveGateReviewContext_RoadmapPhaseZero_ReturnsRoadmap(t *testing.T) {
	tmp := t.TempDir()
	roadmapDir := filepath.Join(tmp, "feat-1", "roadmap")
	if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir roadmap: %v", err)
	}
	roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
	if err := os.WriteFile(roadmapPath, []byte("# Roadmap"), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}

	f := &feature.Feature{
		ID:                  "feat-1",
		Status:              feature.StatusImplementReady,
		CurrentPhase:        feature.PhaseImplement,
		TotalRoadmapPhases:  3,
		CurrentRoadmapPhase: 0,
		Artifacts:           map[string]string{"roadmap": roadmapPath},
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a"},
		},
	}
	lc := lifecycleForFeature(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     feature.NewStore(tmp),
	}, orchestrator.Hooks{})

	ctx, err := o.ResolveGateReviewContext("feat-1", feature.PhaseImplement)
	if err != nil {
		t.Fatalf("ResolveGateReviewContext: %v", err)
	}
	if ctx.ArtifactPath != roadmapPath {
		t.Errorf("ArtifactPath = %q, want roadmap path %q", ctx.ArtifactPath, roadmapPath)
	}
	// No WorktreePath, so WorkDir falls back to Path.
	if ctx.WorkDir != "/tmp/repo-a" {
		t.Errorf("WorkDir = %q, want /tmp/repo-a", ctx.WorkDir)
	}
}

// TestOrchestrator_ResolveGateReviewContext_PhaseResearch_ReturnsInquireArtifact
// ---------------------------------------------------------------------------
// Gate-review for PhaseResearch reads the inquire artifact (the prior phase).
// ---------------------------------------------------------------------------

func TestOrchestrator_ResolveGateReviewContext_PhaseResearch_ReturnsInquireArtifact(t *testing.T) {
	tmp := t.TempDir()
	inquireDir := filepath.Join(tmp, "feat-1", "inquire")
	if err := os.MkdirAll(inquireDir, 0o755); err != nil {
		t.Fatalf("mkdir inquire: %v", err)
	}
	inquirePath := filepath.Join(inquireDir, "inquire.md")
	if err := os.WriteFile(inquirePath, []byte("# Inquire"), 0o644); err != nil {
		t.Fatalf("write inquire: %v", err)
	}

	f := &feature.Feature{
		ID:           "feat-1",
		Status:       feature.StatusResearching,
		CurrentPhase: feature.PhaseResearch,
		Artifacts:    map[string]string{"inquire": inquirePath},
	}
	lc := lifecycleForFeature(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     feature.NewStore(tmp),
	}, orchestrator.Hooks{})

	ctx, err := o.ResolveGateReviewContext("feat-1", feature.PhaseResearch)
	if err != nil {
		t.Fatalf("ResolveGateReviewContext: %v", err)
	}
	if ctx.ArtifactPath != inquirePath {
		t.Errorf("ArtifactPath = %q, want inquire path", ctx.ArtifactPath)
	}
}

// TestOrchestrator_ResolveGateReviewContext_PhaseImplement_RoadmapPhase_ReturnsPhasePlan
// ---------------------------------------------------------------------------
// Regression for iteration-13 reviewer finding: a PhaseImplement gate on a
// roadmap feature whose CurrentRoadmapPhase > 0 must resolve the per-phase
// plan (<state>/<featureID>/phase-NN/plan/*.md), not the generic "plan"
// artifact. Roadmap phase plans are written under phase-NN/plan by
// RunPhasePlanningLoop (see internal/agent/plan_validation.go) and the TUI's
// startPlanReviewSessionCmd uses the same phase-%d-plan key.
// ---------------------------------------------------------------------------

func TestOrchestrator_ResolveGateReviewContext_PhaseImplement_RoadmapPhase_ReturnsPhasePlan(t *testing.T) {
	tmp := t.TempDir()

	// Roadmap phase 2 plan lives at <state>/<featureID>/runs/run-001/phase-02/plan/*.md.
	phasePlanDir := filepath.Join(tmp, "feat-1", "runs", "run-001", "phase-02", "plan")
	if err := os.MkdirAll(phasePlanDir, 0o755); err != nil {
		t.Fatalf("mkdir phase-02/plan: %v", err)
	}
	phasePlanPath := filepath.Join(phasePlanDir, "2026-04-19-phase-02-feature.md")
	if err := os.WriteFile(phasePlanPath, []byte("# Phase 02 plan"), 0o644); err != nil {
		t.Fatalf("write phase-02 plan: %v", err)
	}

	// Seed a distractor generic "plan" artifact so a regression to the old
	// behavior (resolving "plan") would surface as a wrong path.
	genericPlanDir := filepath.Join(tmp, "feat-1", "runs", "run-001", "plan")
	if err := os.MkdirAll(genericPlanDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	genericPlanPath := filepath.Join(genericPlanDir, "plan.md")
	if err := os.WriteFile(genericPlanPath, []byte("# Legacy plan"), 0o644); err != nil {
		t.Fatalf("write legacy plan: %v", err)
	}

	f := &feature.Feature{
		ID:                  "feat-1",
		Status:              feature.StatusImplementReady,
		CurrentPhase:        feature.PhaseImplement,
		TotalRoadmapPhases:  3,
		CurrentRoadmapPhase: 2,
		ActiveRun:           1,
		RunCount:            1,
		// Artifacts intentionally empty for the phase-plan key so the resolver
		// exercises the phase-dir glob fallback (mirrors the real-world state:
		// RunPhasePlanningLoop writes the file but does not persist Artifacts[
		// "phase-N-plan"] on every code path).
		Artifacts: map[string]string{"plan": genericPlanPath},
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", WorktreePath: "/tmp/repo-a-worktree", Path: "/tmp/repo-a"},
		},
	}
	lc := lifecycleForFeature(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     feature.NewStore(tmp),
	}, orchestrator.Hooks{})

	ctx, err := o.ResolveGateReviewContext("feat-1", feature.PhaseImplement)
	if err != nil {
		t.Fatalf("ResolveGateReviewContext: %v", err)
	}
	if ctx.ArtifactPath != phasePlanPath {
		t.Errorf("ArtifactPath = %q, want phase-02 plan %q", ctx.ArtifactPath, phasePlanPath)
	}
	if ctx.ArtifactPath == genericPlanPath {
		t.Error("ArtifactPath fell back to generic plan artifact; expected phase-specific plan")
	}
	if ctx.WorkDir != "/tmp/repo-a-worktree" {
		t.Errorf("WorkDir = %q, want /tmp/repo-a-worktree", ctx.WorkDir)
	}
}

// TestOrchestrator_ResolveGateReviewContext_PhaseImplement_RefactorRoadmapPhase_ReturnsRefactorPhasePlan
// ---------------------------------------------------------------------------
// Regression for iteration-13 reviewer finding: when a roadmap feature is
// inside an active refactor cycle, the per-phase plan lives under
// <state>/<featureID>/refactor-N/phase-NN/plan/*.md. ResolveGateReviewContext
// must route through resolvePhaseDirForKey → phasePlanDirForFeature so
// RefactorPrefix() is honored. This is the "phase/refactor-aware path logic"
// the reviewer explicitly called out.
// ---------------------------------------------------------------------------

// TestRestartPhase_PublishedWithRepoCycles_DispatchesRepoCycleRestarts
// ---------------------------------------------------------------------------
// Regression test: RestartPhase must continue to route a Published feature
// with len(f.RepoCycles) > 0 through the cycle-restart branch
// (lifecycle_delegates.go:943) — produces RestartDispatchRepoCycles with
// per-cycle descriptors. This branch is preserved unchanged by the
// PhaseReview-branch removal.
func TestRestartPhase_PublishedWithRepoCycles_DispatchesRepoCycleRestarts(t *testing.T) {
	tmp := t.TempDir()
	planPath := filepath.Join(tmp, "review-plan.md")
	if err := os.WriteFile(planPath, []byte("review-comments plan"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	f := &feature.Feature{
		ID:           "feat-pubcycle",
		Status:       feature.StatusPublished,
		CurrentPhase: feature.PhasePublish,
		RepoCycles: map[string]*feature.RepoCycleState{
			"repo-a": {Type: feature.CycleReviewComments, PlanPath: planPath, Status: "failed"},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-pubcycle", 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchRepoCycles {
		t.Fatalf("Action = %v, want RestartDispatchRepoCycles", outcome.Action)
	}
	if len(outcome.RepoCycleRestarts) != 1 {
		t.Fatalf("expected 1 repo-cycle restart, got %d", len(outcome.RepoCycleRestarts))
	}
	if outcome.RepoCycleRestarts[0].RepoName != "repo-a" {
		t.Errorf("RepoName = %q, want repo-a", outcome.RepoCycleRestarts[0].RepoName)
	}
}

func TestOrchestrator_ResolveGateReviewContext_PhaseImplement_RefactorRoadmapPhase_ReturnsRefactorPhasePlan(t *testing.T) {
	tmp := t.TempDir()

	// Refactor-scoped roadmap phase 3 plan:
	// <state>/<featureID>/runs/run-001/refactor-1/phase-03/plan/*.md.
	refactorPhaseDir := filepath.Join(tmp, "feat-1", "runs", "run-001", "refactor-1", "phase-03", "plan")
	if err := os.MkdirAll(refactorPhaseDir, 0o755); err != nil {
		t.Fatalf("mkdir refactor-1/phase-03/plan: %v", err)
	}
	refactorPlanPath := filepath.Join(refactorPhaseDir, "2026-04-19-phase-03-refactor.md")
	if err := os.WriteFile(refactorPlanPath, []byte("# Refactor phase 03 plan"), 0o644); err != nil {
		t.Fatalf("write refactor phase plan: %v", err)
	}

	// Seed a distractor non-refactor phase-03/plan so a regression that drops
	// RefactorPrefix() would surface as a wrong path.
	unscopedPhaseDir := filepath.Join(tmp, "feat-1", "runs", "run-001", "phase-03", "plan")
	if err := os.MkdirAll(unscopedPhaseDir, 0o755); err != nil {
		t.Fatalf("mkdir phase-03/plan: %v", err)
	}
	unscopedPlanPath := filepath.Join(unscopedPhaseDir, "2026-04-18-phase-03-feature.md")
	if err := os.WriteFile(unscopedPlanPath, []byte("# Non-refactor phase 03 plan"), 0o644); err != nil {
		t.Fatalf("write unscoped phase plan: %v", err)
	}

	f := &feature.Feature{
		ID:                  "feat-1",
		Status:              feature.StatusImplementReady,
		CurrentPhase:        feature.PhaseImplement,
		TotalRoadmapPhases:  3,
		CurrentRoadmapPhase: 3,
		ActiveRun:           1,
		RunCount:            1,
		// Refactor fields set so RefactorPrefix() returns "refactor-1".
		RefactorPrompt: "Restructure the thing",
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", WorktreePath: "/tmp/repo-a-worktree", Path: "/tmp/repo-a"},
		},
	}
	f.SetRefactorCount(1)
	if prefix := f.RefactorPrefix(); prefix != "refactor-1" {
		t.Fatalf("RefactorPrefix() = %q, want refactor-1 (test setup invariant)", prefix)
	}
	lc := lifecycleForFeature(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     feature.NewStore(tmp),
	}, orchestrator.Hooks{})

	ctx, err := o.ResolveGateReviewContext("feat-1", feature.PhaseImplement)
	if err != nil {
		t.Fatalf("ResolveGateReviewContext: %v", err)
	}
	if ctx.ArtifactPath != refactorPlanPath {
		t.Errorf("ArtifactPath = %q, want refactor-scoped phase plan %q",
			ctx.ArtifactPath, refactorPlanPath)
	}
	if ctx.ArtifactPath == unscopedPlanPath {
		t.Error("ArtifactPath resolved to non-refactor phase plan; RefactorPrefix() was ignored")
	}
}

func TestOrchestrator_ResolveRewindReviewContext_PartialImplementReturnsPendingPhasePlan(t *testing.T) {
	tmp := t.TempDir()
	phasePlanDir := filepath.Join(tmp, "feat-partial", "runs", "run-002", "phase-02", "plan")
	if err := os.MkdirAll(phasePlanDir, 0o755); err != nil {
		t.Fatalf("mkdir phase plan: %v", err)
	}
	phasePlanPath := filepath.Join(phasePlanDir, "phase-plan.md")
	if err := os.WriteFile(phasePlanPath, []byte("# Phase 2 plan"), 0o644); err != nil {
		t.Fatalf("write phase plan: %v", err)
	}
	globalPlanDir := filepath.Join(tmp, "feat-partial", "runs", "run-002", "plan")
	if err := os.MkdirAll(globalPlanDir, 0o755); err != nil {
		t.Fatalf("mkdir global plan: %v", err)
	}
	globalPlanPath := filepath.Join(globalPlanDir, "plan.md")
	if err := os.WriteFile(globalPlanPath, []byte("# Global plan"), 0o644); err != nil {
		t.Fatalf("write global plan: %v", err)
	}

	pendingPhase := 2
	f := &feature.Feature{
		ID:                              "feat-partial",
		Status:                          feature.StatusPlanNeedsReview,
		CurrentPhase:                    feature.PhasePlan,
		Pipeline:                        feature.PipelineLarge,
		CurrentRoadmapPhase:             2,
		TotalRoadmapPhases:              3,
		PendingRewindReviewRoadmapPhase: &pendingPhase,
		ActiveRun:                       2,
		RunCount:                        2,
		Artifacts: map[string]string{
			"plan":         globalPlanPath,
			"phase-2-plan": phasePlanPath,
		},
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", WorktreePath: "/tmp/repo-a-worktree", Path: "/tmp/repo-a"},
		},
	}
	lc := lifecycleForFeature(f)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     feature.NewStore(tmp),
	}, orchestrator.Hooks{})

	ctx, err := o.ResolveRewindReviewContext("feat-partial", feature.PhaseImplement)
	if err != nil {
		t.Fatalf("ResolveRewindReviewContext: %v", err)
	}
	if ctx.ArtifactPath != phasePlanPath {
		t.Errorf("ArtifactPath = %q, want phase plan %q", ctx.ArtifactPath, phasePlanPath)
	}
	if ctx.ArtifactPath == globalPlanPath {
		t.Error("ArtifactPath fell back to global plan despite pending partial phase marker")
	}
	if len(ctx.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", ctx.Warnings)
	}
	if ctx.WorkDir != "/tmp/repo-a-worktree" {
		t.Errorf("WorkDir = %q, want /tmp/repo-a-worktree", ctx.WorkDir)
	}
}

func TestOrchestrator_ResolveRewindReviewContext_PartialImplementMissingPhasePlanWarnsAndFallsBack(t *testing.T) {
	tmp := t.TempDir()
	globalPlanDir := filepath.Join(tmp, "feat-partial", "runs", "run-002", "plan")
	if err := os.MkdirAll(globalPlanDir, 0o755); err != nil {
		t.Fatalf("mkdir global plan: %v", err)
	}
	globalPlanPath := filepath.Join(globalPlanDir, "plan.md")
	if err := os.WriteFile(globalPlanPath, []byte("# Global plan"), 0o644); err != nil {
		t.Fatalf("write global plan: %v", err)
	}

	pendingPhase := 2
	f := &feature.Feature{
		ID:                              "feat-partial",
		Status:                          feature.StatusPlanNeedsReview,
		CurrentPhase:                    feature.PhasePlan,
		Pipeline:                        feature.PipelineLarge,
		CurrentRoadmapPhase:             2,
		TotalRoadmapPhases:              3,
		PendingRewindReviewRoadmapPhase: &pendingPhase,
		ActiveRun:                       2,
		RunCount:                        2,
		Artifacts:                       map[string]string{"plan": globalPlanPath},
	}
	lc := lifecycleForFeature(f)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     feature.NewStore(tmp),
	}, orchestrator.Hooks{})

	ctx, err := o.ResolveRewindReviewContext("feat-partial", feature.PhaseImplement)
	if err != nil {
		t.Fatalf("ResolveRewindReviewContext: %v", err)
	}
	if ctx.ArtifactPath != globalPlanPath {
		t.Errorf("ArtifactPath = %q, want fallback global plan %q", ctx.ArtifactPath, globalPlanPath)
	}
	if len(ctx.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want one warning", ctx.Warnings)
	}
	if !strings.Contains(ctx.Warnings[0], "phase 2 plan") || !strings.Contains(ctx.Warnings[0], "falling back") {
		t.Errorf("Warnings[0] = %q, want phase-plan fallback warning", ctx.Warnings[0])
	}
}

// TestOrchestrator_RestartPhase_RejectsWhileSessionsActive
// ---------------------------------------------------------------------------
// Regression for the "press s,y then spam r" bug. After InterruptFeature has
// run its head-of-loop Transition(StatusInterrupted) but before its
// StopSession loop has finished, the feature shows StatusInterrupted while
// agent processes are still being SIGTERM'd. A racing RestartPhase used to
// read that half-state, call StopFeatureSessions alongside the stop loop,
// and dispatch a fresh KB phase the user never asked for; subsequent "r"
// presses then killed the new sessions and started more, etc. The
// busy-guard makes RestartPhase return ErrFeatureBusy whenever any session
// for the feature is still active, so the only side effect of an "r" press
// during a stop is the TUI surfacing a "wait" hint.
// ---------------------------------------------------------------------------
func TestOrchestrator_RestartPhase_RejectsWhileSessionsActive(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-busy",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseKnowledgeBase,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	sm := mocks.NewMockSessionManager()
	// One session still active for this feature — the case where Stop is mid-flight.
	sm.FeatureSessionsFn = func(id string) []session.SessionView {
		return []session.SessionView{mocks.NewMockSessionView("s-1", "feat-busy")}
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Sessions:  sm,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-busy", 10, 2)
	if !errors.Is(err, orchestrator.ErrFeatureBusy) {
		t.Fatalf("RestartPhase err = %v, want ErrFeatureBusy", err)
	}
	if outcome.Action != 0 {
		t.Errorf("Outcome.Action = %v, want zero (call should bail before dispatch)", outcome.Action)
	}
	// Status must NOT have been mutated — RestartPhase bailed before any transitions.
	if f.Status != feature.StatusInterrupted {
		t.Errorf("Status mutated to %v; busy-guard must not transition", f.Status)
	}
	// StopSession must NOT have been called — the in-flight InterruptFeature
	// owns that loop; RestartPhase doubling up would race it.
	if len(sm.StopCalls) != 0 {
		t.Errorf("StopSession called %d times; busy-guard must not stop sessions", len(sm.StopCalls))
	}
}

func TestOrchestrator_RestartPhase_AllowsArtifactReviewSession(t *testing.T) {
	target := feature.PhaseResearch
	f := &feature.Feature{
		ID:                 "feat-review-busy",
		Status:             feature.StatusInquiryNeedsReview,
		CurrentPhase:       feature.PhaseInquire,
		PendingReviewPhase: &target,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	sm := mocks.NewMockSessionManager()
	reviewSess := mocks.NewMockSessionView("feat-review-busy-artifact-review", "feat-review-busy")
	sm.FeatureSessionsFn = func(id string) []session.SessionView {
		return []session.SessionView{reviewSess}
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Sessions:  sm,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-review-busy", 10, 2)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchPhase {
		t.Fatalf("Action = %v, want RestartDispatchPhase", outcome.Action)
	}
	if outcome.Phase != feature.PhaseInquire {
		t.Errorf("Phase = %v, want PhaseInquire", outcome.Phase)
	}
	if f.Status != feature.StatusInterrupted {
		t.Errorf("Status = %v, want Interrupted", f.Status)
	}
	if len(sm.StopCalls) != 1 || sm.StopCalls[0] != "feat-review-busy-artifact-review" {
		t.Errorf("StopCalls = %v, want artifact review session stopped during restart cleanup", sm.StopCalls)
	}
}

// TestOrchestrator_RestartPhase_ProceedsWhenSessionsInactive
// ---------------------------------------------------------------------------
// Sanity check that the busy-guard only catches *active* sessions: the
// normal restart-of-a-failed-feature path (sessions exist in the manager
// map but ended in SessionFailed/SessionDone) must still proceed. Without
// this we'd silently break "press r to retry a failed phase".
// ---------------------------------------------------------------------------
func TestOrchestrator_RestartPhase_ProceedsWhenSessionsInactive(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-failed",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseKnowledgeBase,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	sm := mocks.NewMockSessionManager()
	deadSession := mocks.NewMockSessionView("s-dead", "feat-failed")
	deadSession.IsActiveVal = false
	deadSession.StatusVal = session.SessionFailed
	sm.FeatureSessionsFn = func(id string) []session.SessionView {
		return []session.SessionView{deadSession}
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Sessions:  sm,
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase("feat-failed", 10, 2)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchPhase {
		t.Errorf("Action = %v, want RestartDispatchPhase", outcome.Action)
	}
	if outcome.Phase != feature.PhaseKnowledgeBase {
		t.Errorf("Phase = %v, want PhaseKnowledgeBase", outcome.Phase)
	}
}
