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
	"os/exec"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// TestOrchestrator_EnterReviewGate_SetsStatusAndPendingPhase
// ---------------------------------------------------------------------------
// EnterReviewGate flips the status to the NeedsReview variant and records the
// target phase so clients
// can render the review editor on a later tick.
// ---------------------------------------------------------------------------

func TestOrchestrator_Rewind_FiresAuditHookAfterSuccess(t *testing.T) {
	f := &feature.Feature{ID: "feat-rewind", ActiveRun: 1}
	lc := lifecycleForFeature(f)
	lc.RewindWithRequestFn = func(featureID string, request feature.RewindRequest) ([]feature.RewindWarning, feature.Phase, error) {
		f.ActiveRun = 2
		return []feature.RewindWarning{{
			Kind: feature.RewindWarningBackupBranch,
			Repo: "repo-a",
			Err:  errors.New("backup warning"),
		}}, feature.PhaseImplement, nil
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

	result, err := o.Rewind("feat-rewind", orchestrator.RewindInput{Request: feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 2,
	}})
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	warnings, effective := result.Warnings, result.EffectivePhase
	if result.SourceRunNumber != 1 || result.NewRunNumber != 2 {
		t.Fatalf("result source/new run = %d/%d, want 1/2", result.SourceRunNumber, result.NewRunNumber)
	}
	if len(warnings) != 1 || warnings[0].Kind != feature.RewindWarningBackupBranch || warnings[0].Repo != "repo-a" {
		t.Fatalf("warnings = %+v, want one repo-a backup-branch warning", warnings)
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
		t.Fatal("Rewind emitted no domain event, want FeatureRewound")
	}
}

// TestOrchestrator_ExtendFailedPhaseBudget_BumpsAndClears
// ---------------------------------------------------------------------------
// ExtendFailedPhaseBudget extends MaxIterations when the run's failure record
// code is iteration_budget_exhausted and MaxPlanIterations when the current phase is Plan.
// It always clears the stored failure record so restart can proceed.
// ---------------------------------------------------------------------------

func TestOrchestrator_ExtendFailedPhaseBudget_BumpsAndClears(t *testing.T) {
	f := &feature.Feature{
		ID:                "feat-1",
		Status:            feature.StatusFailed,
		CurrentPhase:      feature.PhasePlan,
		MaxIterations:     20,
		MaxPlanIterations: 3,
	}
	f.Run().Failure = &errcat.FailureRecord{
		Code:        errcat.IterationBudgetExhausted,
		Context:     &errcat.RecordContext{Phase: &errcat.CodePhase{Name: feature.PhasePlan.FailureName()}},
		Diagnostics: "iteration cap",
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
	if rec := f.FailureRecord(); rec != nil {
		t.Errorf("failure record not cleared: %+v", rec)
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

func TestOrchestrator_RestartPhase_FailedPlan_ExtendsBudgetAndDispatches(t *testing.T) {
	f := &feature.Feature{
		ID:                "feat-1",
		Status:            feature.StatusFailed,
		CurrentPhase:      feature.PhasePlan,
		MaxIterations:     20,
		MaxPlanIterations: 3,
	}
	f.Run().Failure = &errcat.FailureRecord{
		Code:        errcat.IterationBudgetExhausted,
		Context:     &errcat.RecordContext{Phase: &errcat.CodePhase{Name: feature.PhasePlan.FailureName()}},
		Diagnostics: "iteration cap",
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
	if rec := f.FailureRecord(); rec != nil {
		t.Errorf("failure record should be cleared: %+v", rec)
	}
}

func TestOrchestrator_RestartPhase_FailedFinalReview_DispatchesFinalReview(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-fr",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseFinalReview,
		Repos:        []feature.FeatureRepo{{Name: agenticRepoName}},
		RepoStates: map[string]*feature.RepoState{
			agenticRepoName: {
				Touched: true,
				Error:   &errcat.FailureRecord{Code: errcat.ProtocolViolation, Diagnostics: "final_review_reviewer @ /tmp/iter: invalid report"},
			},
		},
	}
	f.Run().Failure = &errcat.FailureRecord{
		Code:        errcat.ProtocolViolation,
		Context:     &errcat.RecordContext{Phase: &errcat.CodePhase{Name: feature.PhaseFinalReview.FailureName()}},
		Diagnostics: "protocol violation: final_review_reviewer @ /tmp/iter: invalid report",
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
	if rec := f.FailureRecord(); rec != nil {
		t.Fatalf("failure record should be cleared: %+v", rec)
	}
	if st := f.RepoStates[agenticRepoName]; st == nil || st.Error != nil {
		t.Fatalf("RepoStates[agentic] = %+v, want its failure record cleared", st)
	}
}

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

// TestOrchestrator_ResolveGateReviewContext_RoadmapPhaseZero_ReturnsRoadmap
// ---------------------------------------------------------------------------
// For a roadmap feature whose current roadmap phase is zero, gate-review
// opens the roadmap artifact — this is the "initial roadmap review" gate.
// ---------------------------------------------------------------------------

// TestOrchestrator_ResolveGateReviewContext_PhaseResearch_ReturnsInquireArtifact
// ---------------------------------------------------------------------------
// Gate-review for PhaseResearch reads the inquire artifact (the prior phase).
// ---------------------------------------------------------------------------

// TestOrchestrator_ResolveGateReviewContext_PhaseImplement_RoadmapPhase_ReturnsPhasePlan
// ---------------------------------------------------------------------------
// Regression for iteration-13 reviewer finding: a PhaseImplement gate on a
// roadmap feature whose CurrentRoadmapPhase > 0 must resolve the per-phase
// plan (<state>/<featureID>/phase-NN/plan/*.md), not the generic "plan"
// artifact. Roadmap phase plans are written under phase-NN/plan by
// RunPhasePlanningLoop (see internal/agent/plan_validation.go); the production
// resolver uses the same phase-%d-plan key.
// ---------------------------------------------------------------------------

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
	repoPath := testutil.InitGitRepo(t)
	rename := exec.Command("git", "branch", "-m", "trunk")
	rename.Dir = repoPath
	rename.Env = testutil.GitTestEnv()
	if out, err := rename.CombinedOutput(); err != nil {
		t.Fatalf("rename default branch: %s: %v", strings.TrimSpace(string(out)), err)
	}
	testutil.CreateBranch(t, repoPath, "feature/local-merge")
	testutil.CommitFile(t, repoPath, "feature.txt", "merged\n", "feature change")
	checkout := exec.Command("git", "checkout", "trunk")
	checkout.Dir = repoPath
	checkout.Env = testutil.GitTestEnv()
	if out, err := checkout.CombinedOutput(); err != nil {
		t.Fatalf("checkout trunk: %s: %v", out, err)
	}
	f := &feature.Feature{
		ID:     "feat-local-merge",
		Slug:   "local-merge",
		Status: feature.StatusPublished,
		Repos: []feature.FeatureRepo{{
			Name:         "repo-a",
			Path:         repoPath,
			WorktreePath: repoPath,
			Branch:       "feature/local-merge",
			Publishable:  &notPublishable,
		}},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	if err := o.MergeFeatureLocal("feat-local-merge"); err != nil {
		t.Fatalf("MergeFeatureLocal: %v", err)
	}

	assertLifecycleCall(t, lc, "MarkDone")
	cmd := exec.Command("git", "log", "--format=%s", "-2")
	cmd.Dir = repoPath
	cmd.Env = testutil.GitTestEnv()
	if out, err := cmd.Output(); err != nil || !strings.Contains(string(out), "feature change") {
		t.Fatalf("merged history = %q, err=%v", out, err)
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

// A feature already Done can merge later work without attempting a second
// MarkDone: StatusDone has no outgoing transitions, so the call would fail.
func TestOrchestrator_MergeFeatureLocal_AtDoneSkipsMarkDone(t *testing.T) {
	notPublishable := false
	repoPath := testutil.InitGitRepo(t)
	rename := exec.Command("git", "branch", "-m", "trunk")
	rename.Dir = repoPath
	rename.Env = testutil.GitTestEnv()
	if out, err := rename.CombinedOutput(); err != nil {
		t.Fatalf("rename default branch: %s: %v", strings.TrimSpace(string(out)), err)
	}
	testutil.CreateBranch(t, repoPath, "feature/late-merge")
	testutil.CommitFile(t, repoPath, "late.txt", "late\n", "late change")
	checkout := exec.Command("git", "checkout", "trunk")
	checkout.Dir = repoPath
	checkout.Env = testutil.GitTestEnv()
	if out, err := checkout.CombinedOutput(); err != nil {
		t.Fatalf("checkout trunk: %s: %v", out, err)
	}
	f := &feature.Feature{
		ID:     "feat-late-merge",
		Slug:   "late-merge",
		Status: feature.StatusDone,
		Repos: []feature.FeatureRepo{{
			Name:         "repo-a",
			Path:         repoPath,
			WorktreePath: repoPath,
			Branch:       "feature/late-merge",
			Publishable:  &notPublishable,
		}},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.MergeFeatureLocal("feat-late-merge"); err != nil {
		t.Fatalf("MergeFeatureLocal: %v", err)
	}

	for _, c := range lc.Calls {
		if c.Method == "MarkDone" {
			t.Error("MarkDone was called on an already-Done feature")
		}
	}
	log := exec.Command("git", "log", "--format=%s", "-2")
	log.Dir = repoPath
	log.Env = testutil.GitTestEnv()
	if out, err := log.Output(); err != nil || !strings.Contains(string(out), "late change") {
		t.Fatalf("merged history = %q, err = %v", out, err)
	}
}
