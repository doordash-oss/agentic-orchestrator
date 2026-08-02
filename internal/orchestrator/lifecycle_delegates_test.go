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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// TestOrchestrator_EnterReviewGate_SetsStatusAndPendingPhase
// ---------------------------------------------------------------------------
// EnterReviewGate flips the status to the NeedsReview variant and records the
// target phase so clients
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
// Lifecycle.ClearRepoCycles, and returns restart descriptors for the caller.
// ---------------------------------------------------------------------------

func TestOrchestrator_CollectAndClearRepoCycleRestarts_ReadsPlansAndClears(t *testing.T) {
	tmp := t.TempDir()
	reviewPath := filepath.Join(tmp, "review-plan.md")
	if err := os.WriteFile(reviewPath, []byte("review content"), 0o644); err != nil {
		t.Fatalf("write review plan: %v", err)
	}

	f := &feature.Feature{
		ID:     "feat-1",
		Status: feature.StatusPublished,
		RepoCycles: map[string]*feature.RepoCycleState{
			repoName: {Type: feature.CycleReviewComments, PlanPath: reviewPath},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	restarts, err := o.CollectAndClearRepoCycleRestarts("feat-1")
	if err != nil {
		t.Fatalf("CollectAndClearRepoCycleRestarts: %v", err)
	}
	assertLifecycleCall(t, lc, "ClearRepoCycles")
	if len(restarts) != 1 {
		t.Fatalf("expected 1 restart, got %d", len(restarts))
	}
	if restarts[0].RepoName != repoName || restarts[0].CycleType != feature.CycleReviewComments {
		t.Errorf("unexpected restart descriptor: %+v", restarts[0])
	}
	if restarts[0].PlanContent != "review content" {
		t.Errorf("restart PlanContent = %q, want %q", restarts[0].PlanContent, "review content")
	}
}

// ---------------------------------------------------------------------------
// Iteration 13 additions: RestartPhase + ResolveGateReviewContext
// ---------------------------------------------------------------------------

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
		Repos:        []feature.FeatureRepo{{Name: agenticRepoName}},
		RepoStates: map[string]*feature.RepoState{
			agenticRepoName: {
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
	if st := f.RepoStates[agenticRepoName]; st == nil || st.LastError != "" {
		t.Fatalf("RepoStates[agentic] = %+v, want LastError cleared", st)
	}
}

// TestOrchestrator_RestartPhase_PublishedWithRepoCycles_ReturnsRestartList
// ---------------------------------------------------------------------------
// For a Published feature with RepoCycles, RestartPhase clears the cycles
// and returns RestartDispatchRepoCycles with per-cycle descriptors for the
// caller to relaunch.
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
			repoName: {Type: feature.CycleReviewComments, PlanPath: planPath},
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
	if outcome.RepoCycleRestarts[0].RepoName != repoName {
		t.Errorf("RepoName = %q, want repo-a", outcome.RepoCycleRestarts[0].RepoName)
	}
	if outcome.RepoCycleRestarts[0].PlanContent != "review plan" {
		t.Errorf("PlanContent = %q, want %q", outcome.RepoCycleRestarts[0].PlanContent, "review plan")
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
// validTransitions[StatusCreated], so RestartPhase returned an error and the
// restart did not occur.
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
			{Name: repoName, WorktreePath: repoAWorktreePath, Path: repoAPath},
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
	if ctx.WorkDir != repoAWorktreePath {
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
			{Name: repoName, Path: repoAPath},
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
	if ctx.WorkDir != repoAPath {
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
// RunPhasePlanningLoop (see internal/agent/plan_validation.go); the production
// resolver uses the same phase-%d-plan key.
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
			{Name: repoName, WorktreePath: repoAWorktreePath, Path: repoAPath},
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
	if ctx.WorkDir != repoAWorktreePath {
		t.Errorf("WorkDir = %q, want /tmp/repo-a-worktree", ctx.WorkDir)
	}
}

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
			repoName: {Type: feature.CycleReviewComments, PlanPath: planPath, Status: feature.RepoCycleFailed},
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
	if outcome.RepoCycleRestarts[0].RepoName != repoName {
		t.Errorf("RepoName = %q, want repo-a", outcome.RepoCycleRestarts[0].RepoName)
	}
}

func TestRestartPhase_PublishedWithFailedFeatureRebase_DispatchesRebaseRetry(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-failed-rebase",
		Status:       feature.StatusPublished,
		CurrentPhase: feature.PhasePublish,
		ActiveCycle: &feature.CycleState{
			Type:   feature.CycleRebase,
			Status: feature.RepoCycleFailed,
			Count:  2,
		},
		RebaseOperation: &feature.RebaseOperationState{Stage: feature.RebaseStageFinalReview},
	}
	f.SetActiveCycleType(feature.CycleRebase)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lifecycleForFeature(f),
		Store:     newFeatureStore(f),
	}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase(f.ID, 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase: %v", err)
	}
	if outcome.Action != orchestrator.RestartDispatchRebase {
		t.Fatalf("Action = %v, want RestartDispatchRebase", outcome.Action)
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
			{Name: repoName, WorktreePath: repoAWorktreePath, Path: repoAPath},
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
		t.Error("ArtifactPath fell back to global plan despite a pending partial roadmap phase")
	}
	if len(ctx.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", ctx.Warnings)
	}
	if ctx.WorkDir != repoAWorktreePath {
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
// for the feature is still active, so the caller can surface a wait hint
// without starting another phase.
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
	sm.FeatureSessionsFn = func(id string) []ports.SessionView {
		return []ports.SessionView{mocks.NewMockSessionView("s-1", "feat-busy")}
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
	sm.FeatureSessionsFn = func(id string) []ports.SessionView {
		return []ports.SessionView{reviewSess}
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
	sm.FeatureSessionsFn = func(id string) []ports.SessionView {
		return []ports.SessionView{deadSession}
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

func TestOrchestrator_MergeFeatureLocal_MarksDone(t *testing.T) {
	notPublishable := false
	repoPath := t.TempDir()
	if out, err := exec.Command("git", "init", "--initial-branch=trunk", repoPath).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", strings.TrimSpace(string(out)), err)
	}
	f := &feature.Feature{
		ID:     "feat-local-merge",
		Slug:   "local-merge",
		Status: feature.StatusPublished,
		Repos: []feature.FeatureRepo{{
			Name:         "repo-a",
			Path:         repoPath,
			WorktreePath: "/worktree/a",
			Branch:       "feature/local-merge",
			Publishable:  &notPublishable,
		}},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(worktreePath string) (bool, error) { return true, nil }
	pub.CommitAllFn = func(worktreePath, message string) error { return nil }
	rebaser := mocks.NewMockRebaseOperator()
	var mergedBase string
	rebaser.MergeFeatureBranchFn = func(repoPath, featureBranch, baseBranch string) error {
		mergedBase = baseBranch
		return nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Publisher: pub,
		Rebaser:   rebaser,
	}, orchestrator.Hooks{})

	if err := o.MergeFeatureLocal("feat-local-merge"); err != nil {
		t.Fatalf("MergeFeatureLocal: %v", err)
	}

	assertLifecycleCall(t, lc, "MarkDone")
	if got := len(rebaser.Calls); got != 1 {
		t.Fatalf("rebaser calls = %d; want 1", got)
	}
	if rebaser.Calls[0].Method != "MergeFeatureBranch" {
		t.Fatalf("rebaser call = %s; want MergeFeatureBranch", rebaser.Calls[0].Method)
	}
	if mergedBase != "trunk" {
		t.Fatalf("MergeFeatureBranch base = %q; want concrete git default branch trunk", mergedBase)
	}
	if got := len(pub.Calls); got != 2 {
		t.Fatalf("publisher calls = %d; want HasUncommittedChanges + CommitAll", got)
	}
}

func TestOrchestrator_MarkDone_IsExplicitCompletionAction(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-mark-done",
		Status: feature.StatusPublished,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	var summaryNeeded bool
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{
		OnFeatureSummaryNeeded: func(featureID string, f *feature.Feature) {
			summaryNeeded = true
		},
	})

	if err := o.MarkDone("feat-mark-done"); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	if f.Status != feature.StatusDone {
		t.Fatalf("Status = %s; want %s", f.Status, feature.StatusDone)
	}
	if !summaryNeeded {
		t.Fatal("OnFeatureSummaryNeeded was not called")
	}
}
