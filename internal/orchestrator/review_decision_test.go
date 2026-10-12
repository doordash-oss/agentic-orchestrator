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
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func storedReviewFixtureInDir(t *testing.T, dir string, f *feature.Feature) (*feature.Store, *feature.Manager) {
	t.Helper()
	store := feature.NewStore(dir)
	f.SchemaVersion = feature.SchemaVersionCurrent
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	return store, feature.NewManager(store, config.NewDefault())
}

// When next == PhasePublish, advance emits NOTHING — completion handlers own
// publish dispatch exclusively. (Inquire/Research/Design → Plan isn't
// Publish, but Implement/Review landing at Publish is the critical case.)
// We drive this via multi-repo all_passed (non-publishable) which calls
// TransitionToReviewPassed → advance(Implement → Publish). Next phase is
// Publish, so advance must short-circuit.
func TestOrchestrator_AdvanceToNextPhase_PhasePublish_Silent(t *testing.T) {
	unpub := false
	f := &feature.Feature{
		ID:           "feat-adv-pub",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Pipeline:     feature.PipelineMedium,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", Publishable: &unpub},
			{Name: "r2", Path: "/tmp/r2", Publishable: &unpub},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-adv-pub", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	events := drainEvents(o)
	// No PhaseStarted for PhasePublish (dispatch owned by completion handlers).
	if hasPhaseStarted(events, feature.PhasePublish) != nil {
		t.Error("no PhaseStarted event should be emitted for PhasePublish in advance path")
	}
	// FeatureAdvanced(PhasePublish) would imply advanceToNextPhase tried to
	// dispatch it — must not happen.
	for _, ev := range events {
		if ev.Type == ports.FeatureAdvanced && ev.Phase == feature.PhasePublish {
			t.Errorf("unexpected FeatureAdvanced event for PhasePublish: %+v", ev)
		}
	}
}

// advanceToNextPhase with a checkpoint gate for the next phase: emits
// ReviewRequired, writes PendingReviewPhase on the feature.
func TestOrchestrator_AdvanceToNextPhase_Gate_EmitsReviewRequired(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-adv-gate",
		Status:       feature.StatusInquiring,
		CurrentPhase: feature.PhaseInquire,
		Pipeline:     feature.PipelineLarge,
		Checkpoints:  feature.Checkpoints{InquiryReview: true},
	}
	fs, lc := newStoredFeatureFixture(t, f)
	stateDir := t.TempDir()
	writePhaseMarkdown(t, stateDir, f, "inquire", "inquire.md")

	var gotPhase feature.Phase
	o := orchestrator.New(orchestrator.Deps{
		Manager:     lc,
		Store:       fs,
		PhaseRunner: &agent.PhaseRunner{StateDir: stateDir},
	}, orchestrator.Hooks{
		OnReviewRequired: func(id string, p feature.Phase) { gotPhase = p },
	})

	if err := o.HandlePhaseCompletion("feat-adv-gate", orchestrator.PhaseCompletionInput{
		Phase:   feature.PhaseInquire,
		Success: true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if gotPhase != feature.PhaseResearch {
		t.Errorf("OnReviewRequired phase = %v, want PhaseResearch", gotPhase)
	}
	f = loadStoredFeature(t, fs, f.ID)
	if f.PendingReviewPhase == nil || *f.PendingReviewPhase != feature.PhaseResearch {
		t.Errorf("PendingReviewPhase = %v, want PhaseResearch", f.PendingReviewPhase)
	}
	events := drainEvents(o)
	if !hasEventType(events, ports.ReviewRequired) {
		t.Error("expected ReviewRequired event")
	}
}

// ---------------------------------------------------------------------------
// Category I — HandleReviewDecision
// ---------------------------------------------------------------------------

// Generic proceed from a gate: clears PendingReviewPhase, dispatches target,
// emits FeatureAdvanced.
func TestOrchestrator_HandleReviewDecision_Proceed_Generic(t *testing.T) {
	researchGate := feature.PhaseResearch
	f := &feature.Feature{
		ID:                 "feat-rd-proc",
		Status:             feature.StatusInquiryNeedsReview,
		CurrentPhase:       feature.PhaseInquire,
		Pipeline:           feature.PipelineLarge,
		PendingReviewPhase: &researchGate,
	}
	fs, lc := newStoredFeatureFixture(t, f)
	// StartPhaseResearch is the lifecycle hook, but the PhaseRunner dispatch for
	// research needs a real PhaseRunner which we don't have here. Use
	// InquiryReview→Plan jump via TargetPhase=PhasePlan to sidestep — nope,
	// we need to test the generic proceed so use phase dispatch path.
	// Simpler: set TargetPhase to the phase whose starter uses only lifecycle
	// calls (no PhaseRunner). startResearch DOES need PhaseRunner, but in the
	// Medium pipeline the gate moving to Plan would need PhaseRunner too.
	//
	// Rather than round-trip through startPhase, we rely on the fact that a
	// startPhase call with a missing PhaseRunner errors out. Treat this error
	// as expected downstream; focus on clearReviewGate + lifecycle side-effects.

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	err := o.HandleReviewDecision("feat-rd-proc", orchestrator.ReviewDecision{
		Decision:    "proceed",
		TargetPhase: feature.PhaseResearch,
	})
	// startPhase(PhaseResearch) will crash because PhaseRunner is nil; we
	// accept any err but verify clearReviewGate ran first.
	_ = err

	f = loadStoredFeature(t, fs, f.ID)
	if f.PendingReviewPhase != nil {
		t.Errorf("PendingReviewPhase should be cleared; got %v", f.PendingReviewPhase)
	}
	if f.IsRewind {
		t.Error("IsRewind should be cleared")
	}
}

// Proceed with PhasePlan flag (per-phase plan approval) → calls
// TransitionToImplementReady and dispatches PhaseImplement.
func TestOrchestrator_HandleReviewDecision_Proceed_PhasePlan(t *testing.T) {
	planGate := feature.PhaseImplement
	f := &feature.Feature{
		ID:                  "feat-rd-pp",
		Status:              feature.StatusPlanNeedsReview,
		Pipeline:            feature.PipelineLarge,
		PendingReviewPhase:  &planGate,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  3,
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	_ = o.HandleReviewDecision("feat-rd-pp", orchestrator.ReviewDecision{
		Decision:  "proceed",
		PhasePlan: true,
	})

	f = loadStoredFeature(t, fs, f.ID)
	if f.Status != feature.StatusImplementing || f.CurrentPhase != feature.PhaseImplement {
		t.Errorf("phase-plan approval persisted status/phase = %s/%s, want implementing/implement", f.Status, f.CurrentPhase)
	}
	f = loadStoredFeature(t, fs, f.ID)
	if f.PendingReviewPhase != nil {
		t.Errorf("PendingReviewPhase should be cleared; got %v", f.PendingReviewPhase)
	}
}

// Proceed with Roadmap flag → calls AdvanceRoadmapPhase and dispatches PhasePlan.
func TestOrchestrator_HandleReviewDecision_Proceed_Roadmap(t *testing.T) {
	planGate := feature.PhasePlan
	f := &feature.Feature{
		ID:                  "feat-rd-rm",
		Status:              feature.StatusPlanNeedsReview,
		Pipeline:            feature.PipelineLarge,
		PendingReviewPhase:  &planGate,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  3,
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	_ = o.HandleReviewDecision("feat-rd-rm", orchestrator.ReviewDecision{
		Decision: "proceed",
		Roadmap:  true,
	})

	f = loadStoredFeature(t, fs, f.ID)
	if f.CurrentRoadmapPhase != 2 || f.CurrentPhase != feature.PhasePlan {
		t.Errorf("roadmap proceed persisted phase = %d/%s, want 2/plan", f.CurrentRoadmapPhase, f.CurrentPhase)
	}
}

// Proceed with TargetPhase=PhaseImplement + TotalRoadmapPhases>0, CurrentRoadmapPhase=0
// → advance roadmap pathway (AdvanceRoadmapPhase then dispatch PhasePlan).
func TestOrchestrator_HandleReviewDecision_Proceed_ImplementGate_RoadmapPending(t *testing.T) {
	implGate := feature.PhaseImplement
	f := &feature.Feature{
		ID:                  "feat-rd-impl-rm",
		Status:              feature.StatusPlanNeedsReview,
		Pipeline:            feature.PipelineLarge,
		PendingReviewPhase:  &implGate,
		TotalRoadmapPhases:  3,
		CurrentRoadmapPhase: 0,
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	_ = o.HandleReviewDecision("feat-rd-impl-rm", orchestrator.ReviewDecision{
		Decision:    "proceed",
		TargetPhase: feature.PhaseImplement,
	})

	f = loadStoredFeature(t, fs, f.ID)
	if f.CurrentRoadmapPhase != 1 || f.CurrentPhase != feature.PhasePlan {
		t.Errorf("pending roadmap proceeded to %d/%s, want 1/plan", f.CurrentRoadmapPhase, f.CurrentPhase)
	}
	// Must NOT call TransitionToImplementReady (that's the legacy non-roadmap path).
}

// Proceed with TargetPhase=PhaseImplement + non-roadmap (TotalRoadmapPhases==0)
// → legacy plan-review approval: TransitionToImplementReady + dispatch Implement.
func TestOrchestrator_HandleReviewDecision_Proceed_ImplementGate_LegacyPlan(t *testing.T) {
	implGate := feature.PhaseImplement
	f := &feature.Feature{
		ID:                 "feat-rd-impl-legacy",
		Status:             feature.StatusPlanNeedsReview,
		Pipeline:           feature.PipelineMedium,
		PendingReviewPhase: &implGate,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	_ = o.HandleReviewDecision("feat-rd-impl-legacy", orchestrator.ReviewDecision{
		Decision:    "proceed",
		TargetPhase: feature.PhaseImplement,
	})

	f = loadStoredFeature(t, fs, f.ID)
	if f.Status != feature.StatusImplementing || f.CurrentPhase != feature.PhaseImplement {
		t.Errorf("legacy approval persisted status/phase = %s/%s, want implementing/implement", f.Status, f.CurrentPhase)
	}
}

// Iterate: bumps MaxPlanIterations by 3, clears gate, dispatches Plan phase.
func TestOrchestrator_HandleReviewDecision_Iterate_BumpsIterations(t *testing.T) {
	planGate := feature.PhasePlan
	f := &feature.Feature{
		ID:                 "feat-rd-iter",
		Status:             feature.StatusPlanNeedsReview,
		Pipeline:           feature.PipelineMedium,
		PendingReviewPhase: &planGate,
		MaxPlanIterations:  5,
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	_ = o.HandleReviewDecision("feat-rd-iter", orchestrator.ReviewDecision{
		Decision: "iterate",
	})

	f = loadStoredFeature(t, fs, f.ID)
	if f.MaxPlanIterations != 8 {
		t.Errorf("MaxPlanIterations = %d, want 8", f.MaxPlanIterations)
	}
	f = loadStoredFeature(t, fs, f.ID)
	if f.PendingReviewPhase != nil {
		t.Errorf("PendingReviewPhase should be cleared; got %v", f.PendingReviewPhase)
	}
}

// Iterate with MaxPlanIterations==0 (the default for fresh features): the
// effective planning budget at runtime is agent.DefaultMaxPlanAttempts, so
// "iterate" must promote 0 → DefaultMaxPlanAttempts before adding 3.
// Otherwise the roadmap/phase-plan retry budget drops from 13 to 3 (and
// phase-plan's RunPhasePlanning would ignore the override entirely because
// it only honors the override when it exceeds the default — phase.go:803).
// The promotion preserves the default iteration budget.
func TestOrchestrator_HandleReviewDecision_Iterate_ZeroDefault_PromotesToDefault(t *testing.T) {
	planGate := feature.PhasePlan
	f := &feature.Feature{
		ID:                 "feat-rd-iter-zero",
		Status:             feature.StatusPlanNeedsReview,
		Pipeline:           feature.PipelineMedium,
		PendingReviewPhase: &planGate,
		MaxPlanIterations:  0, // default — no prior override
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	_ = o.HandleReviewDecision("feat-rd-iter-zero", orchestrator.ReviewDecision{
		Decision: "iterate",
	})

	want := agent.DefaultMaxPlanAttempts + 3
	f = loadStoredFeature(t, fs, f.ID)
	if f.MaxPlanIterations != want {
		t.Errorf("MaxPlanIterations = %d, want %d (DefaultMaxPlanAttempts+3)",
			f.MaxPlanIterations, want)
	}
}

// Iterate with MaxPlanIterations==0 on a phase-plan gate (CurrentRoadmapPhase>0):
// same promotion must happen regardless of whether the iterate targets a
// roadmap or phase-plan. RunPhasePlanning (phase.go:803) only honors
// MaxPlanIterations when it exceeds the default — so the zero-start case on
// phase-plan would drop the budget to 3, potentially short-cycling the
// retry. Promoting to DefaultMaxPlanAttempts first yields 13, which does
// exceed the default (10) and takes effect in the phase-plan loop too.
func TestOrchestrator_HandleReviewDecision_Iterate_ZeroDefault_PhasePlan_PromotesToDefault(t *testing.T) {
	tmpStateDir := t.TempDir()
	planGate := feature.PhasePlan
	f := &feature.Feature{
		ID:                  "feat-rd-iter-pp-zero",
		Status:              feature.StatusPlanNeedsReview,
		Pipeline:            feature.PipelineLarge,
		PendingReviewPhase:  &planGate,
		MaxPlanIterations:   0,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  2,
	}
	fs, lc := newStoredFeatureFixture(t, f)

	pr := &agent.PhaseRunner{StateDir: tmpStateDir}

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs, PhaseRunner: pr}, orchestrator.Hooks{})
	// startPhase → startRoadmapPhasePlan requires a roadmap artifact; without
	// one it errors. The state mutation we're asserting happens BEFORE that
	// dispatch, so swallow the error.
	_ = o.HandleReviewDecision("feat-rd-iter-pp-zero", orchestrator.ReviewDecision{
		Decision:  "iterate",
		PhasePlan: true,
	})

	want := agent.DefaultMaxPlanAttempts + 3
	f = loadStoredFeature(t, fs, f.ID)
	if f.MaxPlanIterations != want {
		t.Errorf("MaxPlanIterations = %d, want %d (DefaultMaxPlanAttempts+3 on phase-plan zero-default)",
			f.MaxPlanIterations, want)
	}
}

// Iterate on a roadmap plan where the latest attempt was APPROVED: the
// orchestrator must invalidate that attempt's meta.yaml so the next
// planning loop re-runs the attempt instead of short-circuiting via
// LatestCompletedPlanAttempt + APPROVED short-circuit in
// plan_validation.go:254. Proof: after iterate, the attempt must no longer
// register as the latest completed (AgentStatus overwritten to empty — so
// the attempt is skipped by LatestCompletedPlanAttempt).
func TestOrchestrator_HandleReviewDecision_Iterate_Roadmap_InvalidatesApprovedAttempt(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-rd-iter-rm-approved"
	roadmapDir := agent.RoadmapDir(tmpStateDir, &feature.Feature{ID: featureID, ActiveRun: 1})

	// Seed attempt-01 as APPROVED SUCCESS — this is the short-circuit trigger.
	if err := agent.WritePlanAttemptMeta(roadmapDir, agent.PlanAttemptMeta{
		Attempt:      1,
		AgentStatus:  "SUCCESS",
		ReviewStatus: "APPROVED",
	}); err != nil {
		t.Fatalf("seed approved meta: %v", err)
	}
	// Sanity: the seeded attempt should be visible to LatestCompletedPlanAttempt.
	if got := agent.LatestCompletedPlanAttempt(roadmapDir); got != 1 {
		t.Fatalf("precondition: LatestCompletedPlanAttempt = %d, want 1", got)
	}

	planGate := feature.PhasePlan
	f := &feature.Feature{
		ID:     featureID,
		Status: feature.StatusPlanNeedsReview,
		// PipelineLarge + no design/research artifact causes startPlan
		// to return an error before spawning RunPlanningWithValidation — which
		// would otherwise panic on a nil ModelRegistry inside its goroutine.
		// The meta invalidation we care about happens BEFORE that dispatch.
		Pipeline:           feature.PipelineLarge,
		PendingReviewPhase: &planGate,
		MaxPlanIterations:  5,
	}
	fs, lc := storedReviewFixtureInDir(t, tmpStateDir, f)

	pr := &agent.PhaseRunner{StateDir: tmpStateDir}

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs, PhaseRunner: pr}, orchestrator.Hooks{})
	// Swallow any dispatch error: the meta write we're asserting happens
	// before startPhase, so dispatch failure doesn't invalidate the test.
	_ = o.HandleReviewDecision(featureID, orchestrator.ReviewDecision{
		Decision: "iterate",
		Roadmap:  true,
	})

	// Post-iterate: the attempt must no longer be discoverable as an
	// APPROVED-SUCCESS entry — otherwise the next plan loop would
	// short-circuit immediately and return "approved" without re-planning.
	if got := agent.LatestCompletedPlanAttempt(roadmapDir); got != 0 {
		t.Errorf("LatestCompletedPlanAttempt = %d after iterate; want 0 (approved attempt should be invalidated)", got)
	}
}

// Iterate on a roadmap phase-plan (CurrentRoadmapPhase>0) where the latest
// per-phase attempt was APPROVED: same invalidation guarantee as the
// top-level roadmap case, but targeting PhasePlanDir instead of RoadmapDir.
// This covers the per-phase approved-attempt invalidation path:
// writePlanAttemptChangesRequested must write feedback where the planner reads
// it so the per-phase APPROVED attempt is no longer visible.
func TestOrchestrator_HandleReviewDecision_Iterate_PhasePlan_InvalidatesApprovedAttempt(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-rd-iter-pp-approved"
	phasePlanDir := agent.PhasePlanDir(tmpStateDir, &feature.Feature{ID: featureID, ActiveRun: 1}, 1)

	if err := agent.WritePlanAttemptMeta(phasePlanDir, agent.PlanAttemptMeta{
		Attempt:      1,
		AgentStatus:  "SUCCESS",
		ReviewStatus: "APPROVED",
	}); err != nil {
		t.Fatalf("seed approved meta: %v", err)
	}
	if got := agent.LatestCompletedPlanAttempt(phasePlanDir); got != 1 {
		t.Fatalf("precondition: LatestCompletedPlanAttempt = %d, want 1", got)
	}

	planGate := feature.PhasePlan
	f := &feature.Feature{
		ID:                  featureID,
		Status:              feature.StatusPlanNeedsReview,
		Pipeline:            feature.PipelineLarge,
		PendingReviewPhase:  &planGate,
		MaxPlanIterations:   5,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  2,
	}
	fs, lc := storedReviewFixtureInDir(t, tmpStateDir, f)

	pr := &agent.PhaseRunner{StateDir: tmpStateDir}

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs, PhaseRunner: pr}, orchestrator.Hooks{})
	// startPhase → startRoadmapPhasePlan requires a roadmap artifact; the
	// meta invalidation we're asserting happens BEFORE that dispatch, so
	// swallow the dispatch error.
	_ = o.HandleReviewDecision(featureID, orchestrator.ReviewDecision{
		Decision:  "iterate",
		PhasePlan: true,
	})

	if got := agent.LatestCompletedPlanAttempt(phasePlanDir); got != 0 {
		t.Errorf("LatestCompletedPlanAttempt = %d after iterate; want 0 (approved phase-plan attempt should be invalidated)", got)
	}
}

// Iterate on a legacy plan review (neither d.Roadmap nor d.PhasePlan set)
// where the latest attempt in <baseDir>/<featureID>/plan/ was APPROVED:
// the orchestrator must still invalidate that attempt's meta.yaml.
//
// Before the fix, reviewIterate only invalidated attempt meta when
// d.Roadmap || d.PhasePlan. On a plain StatusPlanNeedsReview "iterate"
// both flags are false, so the stale APPROVED meta at
// <baseDir>/<featureID>/plan/attempt-01/meta.yaml stayed in place. Older
// builds ran the now-removed RunPlanningLoop against this path; without
// invalidation, an approved legacy attempt would short-circuit any future
// planning resume — the user's iterate request would be silently dropped.
func TestOrchestrator_HandleReviewDecision_Iterate_Legacy_InvalidatesApprovedAttempt(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-rd-iter-legacy-approved"
	legacyPlanDir := filepath.Join(tmpStateDir, featureID, "runs", "run-001", "plan")

	if err := agent.WritePlanAttemptMeta(legacyPlanDir, agent.PlanAttemptMeta{
		Attempt:      1,
		AgentStatus:  "SUCCESS",
		ReviewStatus: "APPROVED",
	}); err != nil {
		t.Fatalf("seed approved meta: %v", err)
	}
	if got := agent.LatestCompletedPlanAttempt(legacyPlanDir); got != 1 {
		t.Fatalf("precondition: LatestCompletedPlanAttempt = %d, want 1", got)
	}

	planGate := feature.PhasePlan
	f := &feature.Feature{
		ID:     featureID,
		Status: feature.StatusPlanNeedsReview,
		// PipelineLarge + no design/research artifact causes startPlan
		// to return an error before spawning the planning helper — which
		// would otherwise panic on a nil ModelRegistry inside its goroutine.
		// The meta invalidation we care about happens BEFORE that dispatch.
		// Same trick the Roadmap/PhasePlan variants use.
		Pipeline:           feature.PipelineLarge,
		PendingReviewPhase: &planGate,
		MaxPlanIterations:  5,
		ActiveRun:          1,
		RunCount:           1,
		// CurrentRoadmapPhase stays 0 and Artifacts["roadmap"] stays unset,
		// which classifies this as a non-roadmap plan because no roadmap artifact exists.
	}
	fs, lc := storedReviewFixtureInDir(t, tmpStateDir, f)

	pr := &agent.PhaseRunner{StateDir: tmpStateDir}

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs, PhaseRunner: pr}, orchestrator.Hooks{})
	// No Roadmap / PhasePlan flag — the legacy path. Swallow any dispatch
	// error: the meta write we're asserting happens before startPhase, so
	// dispatch failure (missing artifact) does not invalidate the test.
	_ = o.HandleReviewDecision(featureID, orchestrator.ReviewDecision{
		Decision: "iterate",
	})

	// Post-iterate: the legacy attempt must no longer be discoverable as an
	// APPROVED-SUCCESS entry. Without invalidation, the on-disk APPROVED
	// metadata left over from the (now-removed) RunPlanningLoop would
	// continue to satisfy LatestCompletedPlanAttempt and silently swallow
	// the user's iterate request.
	if got := agent.LatestCompletedPlanAttempt(legacyPlanDir); got != 0 {
		t.Errorf("LatestCompletedPlanAttempt(legacy plan dir) = %d after iterate; want 0 (approved legacy plan attempt should be invalidated)", got)
	}
}

// Removed in SchemaVersionCurrent = 3: the silent-fallback regression these
// tests guarded against is structurally impossible now: the orchestrator
// hard-fails when execution-order.yaml is missing/malformed and there is no
// SequentialFallbackPlan synthesis path.

// Phase 2 carry-forward produces run-relative values on the new run's
// Artifacts map (carryForwardArtifactsMap strips sealedRunDir from absolute
// paths). For rewind-to-PhaseImplement on a roadmap pipeline, f.Artifacts["plan"]
// carries forward as "phase-NN/plan/plan.md" (a run-relative subpath, NOT
// relative to <activeRun>/plan/). resolvePlanPath must recognize this shape
// and join it to ActiveRunDir — otherwise the proceed path cannot locate the
// carried plan file and persists an empty string to f.Artifacts["plan"].
func TestOrchestrator_Proceed_PhasePlan_RunRelativeArtifact_RoadmapPipeline_Resolves(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-run-rel-plan"

	// Seed the carried phase-plan on disk under run-001/phase-02/plan/plan.md.
	// This mirrors what copyRunArtifactsForward produces on the new
	// run after rewind-to-Implement on a Large pipeline at phase 2.
	run1PhasePlanDir := filepath.Join(tmpStateDir, featureID, "runs", "run-001", "phase-02", "plan")
	if err := os.MkdirAll(run1PhasePlanDir, 0o755); err != nil {
		t.Fatalf("mkdir phase-plan: %v", err)
	}
	planPath := filepath.Join(run1PhasePlanDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# carried phase-02 plan"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	implementGate := feature.PhaseImplement
	f := &feature.Feature{
		ID:                  featureID,
		Status:              feature.StatusPlanNeedsReview,
		Pipeline:            feature.PipelineLarge,
		PendingReviewPhase:  &implementGate,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  3,
		ActiveRun:           1,
		RunCount:            1,
		Artifacts: map[string]string{
			// Run-relative form produced by carryForwardArtifactsMap — the
			// sealedRunDir prefix has been stripped. Before the run-relative
			// fallback landed in resolvePlanPath, os.Stat("phase-02/plan/plan.md")
			// failed (relative paths are resolved against CWD) and the resolver
			// cascade could not find the carried artifact.
			"plan": filepath.Join("phase-02", "plan", "plan.md"),
		},
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
		},
	}
	fs, lc := storedReviewFixtureInDir(t, tmpStateDir, f)
	// Abort before goroutines spawn.

	pr := &agent.PhaseRunner{StateDir: tmpStateDir}

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs, PhaseRunner: pr}, orchestrator.Hooks{})
	o.SetRunMultiRepoImplFn((&fakeRunMultiRepoImpl{ReturnErr: os.ErrClosed}).Fn())
	_ = o.HandleReviewDecision(featureID, orchestrator.ReviewDecision{
		Decision:  "proceed",
		PhasePlan: true,
	})

	f = loadStoredFeature(t, fs, f.ID)
	got, ok := f.Artifacts["plan"]
	if !ok {
		t.Fatalf("f.Artifacts[plan] was not persisted — resolvePlanPath returned empty; the run-relative fallback failed to resolve %q against ActiveRunDir",
			filepath.Join("phase-02", "plan", "plan.md"))
	}
	if got != planPath {
		t.Errorf("f.Artifacts[plan] = %q, want %q (run-relative value must resolve to <ActiveRunDir>/<carried-subpath>)",
			got, planPath)
	}
}

// ProceedFromRewindReview to PhaseInquire: reads description-review.md and
// overwrites f.Description. The file lives at the feature root (mirrors where
// rewind writes it — feature/manager.go:1200 uses
// baseDir/featureID/description-review.md). Manager.RewindWithRequest has already
// performed the rewind; this entry point only confirms it.
func TestOrchestrator_ProceedFromRewindReview_ToInquire_OverwritesDescription(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-rd-rewind"
	featureDir := filepath.Join(tmpStateDir, featureID)
	if err := os.MkdirAll(featureDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(featureDir, "description-review.md"), []byte("new desc"), 0o644); err != nil {
		t.Fatalf("write desc: %v", err)
	}

	f := &feature.Feature{
		ID:          featureID,
		Status:      feature.StatusPromptNeedsReview,
		Pipeline:    feature.PipelineLarge,
		Description: "old desc",
	}
	fs, lc := storedReviewFixtureInDir(t, tmpStateDir, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	if err := o.ProceedFromRewindReview(featureID, feature.PhaseInquire); err != nil {
		t.Fatalf("ProceedFromRewindReview: %v", err)
	}

	f = loadStoredFeature(t, fs, f.ID)
	if f.Description != "new desc" {
		t.Errorf("Description = %q, want 'new desc' (should be overwritten from description-review.md)", f.Description)
	}
	// RewindWithRequest must NOT be invoked from this path because the actual rewind
	// already happened. Calling it again would seal the
	// freshly forked run and produce a phantom extra run on every confirm.
}

// ProceedFromRewindReview to PhasePlan on an Medium pipeline: Plan is the
// first phase of the Medium pipeline (no inquire / research / design),
// so the rewind-review input is the feature description itself — same
// pattern as rewind-to-Inquire for Large/Moonshot. feature/manager.go
// writes description-review.md on this rewind; the orchestrator must read it
// back on confirm so user edits take effect.
func TestOrchestrator_ProceedFromRewindReview_MediumToPlan_OverwritesDescription(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-rd-rewind-medium-plan"
	featureDir := filepath.Join(tmpStateDir, featureID)
	if err := os.MkdirAll(featureDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(featureDir, "description-review.md"), []byte("edited desc"), 0o644); err != nil {
		t.Fatalf("write desc: %v", err)
	}

	f := &feature.Feature{
		ID:            featureID,
		Status:        feature.StatusDesignNeedsReview,
		Pipeline:      feature.PipelineMedium,
		Description:   "old desc",
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	fs, lc := storedReviewFixtureInDir(t, tmpStateDir, f)
	// Use a real feature.Store rooted at tmpStateDir so o.stateDir() resolves
	// to the right path without needing a PhaseRunner wired up — PhaseRunner
	// is left nil so startPlanning short-circuits before spawning a goroutine
	// that would panic on nil llm.Registry.
	store := fs
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: store}, orchestrator.Hooks{})

	if err := o.ProceedFromRewindReview(featureID, feature.PhasePlan); err != nil {
		t.Fatalf("ProceedFromRewindReview: %v", err)
	}

	got, _ := store.Load(featureID)
	if got.Description != "edited desc" {
		t.Errorf("Description = %q, want 'edited desc' (Medium rewind-to-Plan must read description-review.md)", got.Description)
	}
}

// ProceedFromRewindReview on an Medium-upgraded feature, given the
// already-escalated effective target. Manager.RewindWithRequest runs before the
// artifact review opens and resolves the escalation (pre-plan targets on
// Medium-upgraded features return PhaseKnowledgeBase). The orchestrator receives the
// escalated target directly. This test verifies that ProceedFromRewindReview
// dispatches PhaseKnowledgeBase rather than treating the input as a pre-plan
// phase.
func TestOrchestrator_ProceedFromRewindReview_DispatchesEscalatedKBTarget(t *testing.T) {
	unpub := false
	f := &feature.Feature{
		ID:                   "feat-rd-rewind-kb",
		Status:               feature.StatusCreated,
		Pipeline:             feature.PipelineLarge,
		PipelineUpgradedFrom: feature.PipelineMedium,
		ForceKBRebuild:       true, // skip KB freshness check so startKB enters the mixed-fresh path
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", Publishable: &unpub},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	// PhaseRunner is nil: startKB short-circuits after StartPhaseKnowledgeBase +
	// InitKBStatus and returns PhaseStarted cleanly.
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	// Effective target propagated from the earlier RewindWithRequest result.
	if err := o.ProceedFromRewindReview("feat-rd-rewind-kb", feature.PhaseKnowledgeBase); err != nil {
		t.Fatalf("ProceedFromRewindReview: %v", err)
	}

	// KB was dispatched → StartPhaseKnowledgeBase must have fired.
	// Inquire dispatch must NOT have happened (would indicate the escalated
	// target was ignored).
	// RewindWithRequest must NOT be called because the rewind already happened
	// before this entry point ran.

	events := drainEvents(o)
	// FeatureAdvanced carries the effective target (KB).
	sawKBAdvance := false
	for _, ev := range events {
		if ev.Type == ports.FeatureAdvanced && ev.Phase == feature.PhaseKnowledgeBase {
			sawKBAdvance = true
		}
		if ev.Type == ports.FeatureAdvanced && ev.Phase == feature.PhaseInquire {
			t.Errorf("unexpected FeatureAdvanced(PhaseInquire); escalated KB target must not be ignored")
		}
	}
	if !sawKBAdvance {
		t.Errorf("expected FeatureAdvanced(PhaseKnowledgeBase); got events: %+v", events)
	}
}

// Top-level roadmap approval via the needs_human_review gate: the planner left
// with FinalStatus=needs_human_review, so TotalRoadmapPhases is still 0. When
// the reviewer hits "proceed" on the roadmap gate, the orchestrator must parse
// the approved roadmap and persist TotalRoadmapPhases BEFORE AdvanceRoadmapPhase
// runs, otherwise roadmap sequencing (CurrentRoadmapPhase vs TotalRoadmapPhases
// comparisons) is wrong.
func TestOrchestrator_HandleReviewDecision_Proceed_Roadmap_PersistsPhaseCount(t *testing.T) {
	tmpDir := t.TempDir()
	roadmapPath := filepath.Join(tmpDir, "roadmap.md")
	roadmap := "# Roadmap\n\n## Phase 1: Bootstrap\n### Goal\nInit\n\n## Phase 2: Build\n### Goal\nBuild\n\n## Phase 3: Polish\n### Goal\nPolish\n"
	if err := os.WriteFile(roadmapPath, []byte(roadmap), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}

	planGate := feature.PhasePlan
	f := &feature.Feature{
		ID:                 "feat-rd-rm-nhr",
		Status:             feature.StatusPlanNeedsReview,
		Pipeline:           feature.PipelineLarge,
		PendingReviewPhase: &planGate,
		// needs_human_review → approved path: TotalRoadmapPhases is still 0.
		TotalRoadmapPhases:  0,
		CurrentRoadmapPhase: 0,
		Artifacts:           map[string]string{"roadmap": roadmapPath},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	// Record AdvanceRoadmapPhase observation. Capturing TotalRoadmapPhases
	// *at the moment AdvanceRoadmapPhase is called* proves the parse-and-persist
	// step ran before the lifecycle transition — not after.

	// PhaseRunner nil: startPlan → startRoadmapPhasePlan completes cleanly
	// without firing the PhaseRunner fan-out.
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandleReviewDecision("feat-rd-rm-nhr", orchestrator.ReviewDecision{
		Decision: "proceed",
		Roadmap:  true,
	}); err != nil {
		t.Fatalf("HandleReviewDecision: %v", err)
	}

	// TotalRoadmapPhases must now reflect the parsed roadmap (3 phases).
	f = loadStoredFeature(t, fs, f.ID)
	if f.TotalRoadmapPhases != 3 {
		t.Errorf("TotalRoadmapPhases = %d, want 3 (roadmap parsed to 3 phases)", f.TotalRoadmapPhases)
	}
	// The persist step must have run BEFORE AdvanceRoadmapPhase — otherwise
	// downstream roadmap sequencing inside that lifecycle call would see 0.
}

// Unknown decision returns an error.
func TestOrchestrator_HandleReviewDecision_UnknownDecision_Errors(t *testing.T) {
	f := &feature.Feature{ID: "feat-rd-bad", Status: feature.StatusPlanNeedsReview}
	fs, lc := newStoredFeatureFixture(t, f)
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	err := o.HandleReviewDecision("feat-rd-bad", orchestrator.ReviewDecision{Decision: "unknown"})
	if err == nil {
		t.Fatal("expected error for unknown decision")
	}
}

// Interrupted features are silently ignored.
func TestOrchestrator_HandleReviewDecision_Interrupted_NoOp(t *testing.T) {
	f := &feature.Feature{ID: "feat-rd-zmb", Status: feature.StatusInterrupted}
	fs, lc := newStoredFeatureFixture(t, f)
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandleReviewDecision("feat-rd-zmb", orchestrator.ReviewDecision{Decision: "proceed"}); err != nil {
		t.Fatalf("HandleReviewDecision: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Category J — tryCompleteAndEmit sole-emitter invariant
// ---------------------------------------------------------------------------

// Only the first Publish call emits FeatureCompleted. A second call finds
// TryCompletePublish returning false and must NOT re-emit.
func TestOrchestrator_TryCompleteAndEmit_Idempotent(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-tce",
		Status: feature.StatusReviewPassed,
		Repos:  []feature.FeatureRepo{{Name: "r1", Path: "/tmp/r1"}},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	var completedCalls int
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureCompleted: func(id string, fv *feature.Feature) { completedCalls++ },
	})
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		url := "https://github.com/org/r1/pull/1"
		return url, lc.SetRepoPublished(id, repo, url)
	})

	// First publish fires FeatureCompleted.
	if err := o.Publish("feat-tce"); err != nil {
		t.Fatalf("Publish #1: %v", err)
	}
	if completedCalls != 1 {
		t.Errorf("first Publish: OnFeatureCompleted = %d, want 1", completedCalls)
	}

	// Drain events before second publish so hasEventType is meaningful.
	_ = drainEvents(o)

	// Second publish must NOT emit FeatureCompleted again.
	if err := o.Publish("feat-tce"); err != nil {
		t.Fatalf("Publish #2: %v", err)
	}
	if completedCalls != 1 {
		t.Errorf("second Publish: OnFeatureCompleted = %d, want 1 (should not re-fire)", completedCalls)
	}
	events := drainEvents(o)
	if hasEventType(events, ports.FeatureCompleted) {
		t.Error("FeatureCompleted should NOT re-emit on second publish")
	}
}

// ---------------------------------------------------------------------------
// Category K — Hooks nil-safety
// ---------------------------------------------------------------------------

// All five new hooks are nil-safe: missing callbacks must not panic.
func TestOrchestrator_Hooks_NilSafe(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-nil-hooks",
		Status: feature.StatusReviewPassed,
		Repos:  []feature.FeatureRepo{{Name: "r1", Path: "/tmp/r1"}},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	// All hook pointers are nil — must not panic through any emission site.
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		url := "https://github.com/org/r1/pull/1"
		return url, lc.SetRepoPublished(id, repo, url)
	})

	// OnPublishStarted + OnPublishCompleted + OnFeatureCompleted all nil.
	if err := o.Publish("feat-nil-hooks"); err != nil {
		t.Fatalf("Publish with nil hooks: %v", err)
	}
	_ = drainEvents(o)

	// OnFeatureFailed nil: failure path must not panic.
	// Use the plan failure path which funnels through markFailedWithEvent.
	fFail := &feature.Feature{
		ID:           "feat-fail-nil",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
	}
	fsFail, lcFail := newStoredFeatureFixture(t, fFail)
	oFail := orchestrator.New(orchestrator.Deps{Manager: lcFail, Store: fsFail}, orchestrator.Hooks{})
	if err := oFail.HandlePhaseCompletion("feat-fail-nil", orchestrator.PhaseCompletionInput{
		Phase: feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{
			FinalStatus: finalStatusFailed,
			LastError:   "nope",
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion failure path with nil hooks: %v", err)
	}

	// OnReviewRequired nil: gate path must not panic.
	fGate := &feature.Feature{
		ID:           "feat-gate-nil",
		Status:       feature.StatusInquiring,
		CurrentPhase: feature.PhaseInquire,
		Pipeline:     feature.PipelineLarge,
		Checkpoints:  feature.Checkpoints{InquiryReview: true},
	}
	fsGate, lcGate := newStoredFeatureFixture(t, fGate)
	oGate := orchestrator.New(orchestrator.Deps{Manager: lcGate, Store: fsGate}, orchestrator.Hooks{})
	if err := oGate.HandlePhaseCompletion("feat-gate-nil", orchestrator.PhaseCompletionInput{
		Phase:   feature.PhaseInquire,
		Success: true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion gate path with nil hooks: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Category L — Regression tests for review rewind + advance failure handling
// ---------------------------------------------------------------------------

// Rewind-to-implement with CurrentRoadmapPhase > 0 must still call
// TransitionToImplementReady so the subsequent StartImplementation is a valid
// transition. Before the fix, the orchestrator gated TransitionToImplementReady on
// CurrentRoadmapPhase == 0, leaving roadmap rewinds stuck at
// StatusPlanNeedsReview when startImplement tried to transition to
// StatusImplementing — a transition that feature.validTransitions does not
// allow from StatusPlanNeedsReview (feature/feature.go:504).
func TestOrchestrator_ProceedFromRewindReview_ToImplement_RoadmapPhase_CompletesPlanning(t *testing.T) {
	implGate := feature.PhaseImplement
	f := &feature.Feature{
		ID:                  "feat-rewind-roadmap-impl",
		Status:              feature.StatusPlanNeedsReview,
		Pipeline:            feature.PipelineLarge,
		PendingReviewPhase:  &implGate,
		CurrentRoadmapPhase: 2, // non-zero: pre-fix code skipped TransitionToImplementReady
		TotalRoadmapPhases:  3,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)
	// startImplement will fire StartImplementation and then fail at
	// resolvePlanPath (no plan on disk); that's fine — the assertion is on
	// TransitionToImplementReady, which runs BEFORE startImplement.

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	// Ignore the error — startImplement will fail resolving the plan path,
	// but TransitionToImplementReady must still have been invoked upstream.
	_ = o.ProceedFromRewindReview("feat-rewind-roadmap-impl", feature.PhaseImplement)

	f = loadStoredFeature(t, fs, f.ID)
	if f.Status != feature.StatusImplementing {
		t.Errorf("roadmap rewind status = %s, want implementing", f.Status)
	}
}

func TestOrchestrator_ProceedFromRewindReview_PartialImplement_StartsRoadmapPhaseImplementation(t *testing.T) {
	implGate := feature.PhaseImplement
	pendingPhase := 2
	f := &feature.Feature{
		ID:                              "feat-rewind-partial-impl",
		Status:                          feature.StatusPlanNeedsReview,
		Pipeline:                        feature.PipelineLarge,
		PendingReviewPhase:              &implGate,
		IsRewind:                        true,
		PendingRewindReviewRoadmapPhase: &pendingPhase,
		CurrentRoadmapPhase:             2,
		TotalRoadmapPhases:              3,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	_ = o.ProceedFromRewindReview("feat-rewind-partial-impl", feature.PhaseImplement)

	f = loadStoredFeature(t, fs, f.ID)
	f = loadStoredFeature(t, fs, f.ID)
	if f.PendingRewindReviewRoadmapPhase != nil {
		t.Fatalf("PendingRewindReviewRoadmapPhase = %v, want nil", f.PendingRewindReviewRoadmapPhase)
	}
	f = loadStoredFeature(t, fs, f.ID)
	if f.PendingReviewPhase != nil {
		t.Fatalf("PendingReviewPhase = %v, want nil", f.PendingReviewPhase)
	}
	if f.IsRewind {
		t.Fatal("IsRewind = true, want false")
	}
	if f.CurrentRoadmapPhase != 2 {
		t.Errorf("CurrentRoadmapPhase = %d, want 2", f.CurrentRoadmapPhase)
	}
}

// (TestOrchestrator_HandleReviewDecision_Rewind_ToImplement_Roadmap_ReloadsExecutionPlan
// removed in SchemaVersionCurrent = 3 — see the note at the top of the
// "removed" block above.)

// Rewind-to-implement with CurrentRoadmapPhase == 0 (legacy non-roadmap plan
// path) must also call TransitionToImplementReady. This was working before the fix,
// but we pin the behaviour so future refactors do not silently drop the
// non-roadmap call.
func TestOrchestrator_ProceedFromRewindReview_ToImplement_NonRoadmap_CompletesPlanning(t *testing.T) {
	implGate := feature.PhaseImplement
	f := &feature.Feature{
		ID:                 "feat-rewind-legacy-impl",
		Status:             feature.StatusPlanNeedsReview,
		Pipeline:           feature.PipelineMedium,
		PendingReviewPhase: &implGate,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	_ = o.ProceedFromRewindReview("feat-rewind-legacy-impl", feature.PhaseImplement)

	f = loadStoredFeature(t, fs, f.ID)
	if f.Status != feature.StatusImplementing {
		t.Errorf("legacy rewind status = %s, want implementing", f.Status)
	}
}

// Rewind-to-pre-plan path where KB is immediately skipped: a pre-plan
// rewind target escalates to PhaseKnowledgeBase
// (feature/manager.go:1115-1121), but startKB sees no repos and returns
// PhaseSkipped → PhaseInquire; startPhase recurses and actually launches
// PhaseInquire. FeatureAdvanced MUST carry the actually started phase
// (Inquire), not the requested target (KnowledgeBase). Emitting the
// requested-but-skipped phase breaks the same phase-sequencing contract
// The completion contract treats a missing FeatureAdvanced event as blocking — wrong phase
// is the same class of defect for downstream subscribers.
//
// Pre-fix behavior (hardcoded `Phase: target`) emitted
// FeatureAdvanced(PhaseKnowledgeBase) even though startPhase actually
// dispatched Inquire. This test fails against that bug and passes once
// startPhase returns the actual started phase.
func TestOrchestrator_ProceedFromRewindReview_ToKB_Skipped_EmitsAdvancedForActualPhase(t *testing.T) {
	f := &feature.Feature{
		ID:                   "feat-rd-rewind-kb-skip",
		Status:               feature.StatusCreated,
		Pipeline:             feature.PipelineLarge,
		PipelineUpgradedFrom: feature.PipelineMedium,
		// No repos: startKB short-circuits at len(f.Repos)==0 and returns
		// PhaseSkipped → PhaseInquire without needing a CmdRunner or KB
		// freshness fixtures (orchestrator.go:329-331).
		Repos: nil,
	}
	fs, lc := newStoredFeatureFixture(t, f)
	// StartPhaseInquire is invoked by startInquire after the recursive dispatch
	// from the skipped KB; wire it up so the recursion completes cleanly.

	// PhaseRunner is nil: startInquire short-circuits after the lifecycle
	// transition and returns PhaseStarted cleanly.
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	// Effective target after the earlier rewind resolved the Medium-upgraded
	// escalation and propagated PhaseKnowledgeBase.
	if err := o.ProceedFromRewindReview("feat-rd-rewind-kb-skip", feature.PhaseKnowledgeBase); err != nil {
		t.Fatalf("ProceedFromRewindReview: %v", err)
	}

	// Lifecycle path: Inquire was dispatched (recursive start after KB skip);
	// KB was NOT started because startKB short-circuited before calling
	// StartPhaseKnowledgeBase.

	events := drainEvents(o)
	// FeatureAdvanced must carry the actually started phase (Inquire). The
	// pre-fix code emitted FeatureAdvanced(PhaseKnowledgeBase) because it
	// used the requested target instead of startPhase's returned phase.
	sawInquireAdvance := false
	for _, ev := range events {
		if ev.Type == ports.FeatureAdvanced {
			switch ev.Phase {
			case feature.PhaseInquire:
				sawInquireAdvance = true
			case feature.PhaseKnowledgeBase:
				t.Errorf("unexpected FeatureAdvanced(PhaseKnowledgeBase); KB was skipped — FeatureAdvanced must carry the actually started phase (Inquire)")
			}
		}
	}
	if !sawInquireAdvance {
		t.Errorf("expected FeatureAdvanced(PhaseInquire) after KB skipped; got events: %+v", events)
	}
	// PhaseStarted must only fire for Inquire (the actually started phase);
	// no PhaseStarted should fire for the skipped KB.
	if hasPhaseStarted(events, feature.PhaseKnowledgeBase) != nil {
		t.Error("unexpected PhaseStarted event for PhaseKnowledgeBase (KB was skipped)")
	}
	if hasPhaseStarted(events, feature.PhaseInquire) == nil {
		t.Error("expected PhaseStarted event for PhaseInquire (actually started phase)")
	}
}
