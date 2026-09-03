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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// ---------------------------------------------------------------------------
// Acceptance tests for the canonical failure-record classification. Every
// terminal path must store exactly one errcat.FailureRecord on the active
// run, built through the failure_classify.go helpers: the expected catalog
// code, a phase block naming the failing phase (iteration when the loop
// result knows it), a repositories block listing the failed repositories for
// multi-repo outcomes, and raw diagnostics equal to the former last-error
// message.
// ---------------------------------------------------------------------------

// failureRecordFixture wires a real feature.Store-backed lifecycle mock whose
// MarkFailed persists the canonical failure record, so tests can assert the
// durable run state after a terminal path.
type failureRecordFixture struct {
	o     *orchestrator.Orchestrator
	store *feature.Store
	f     *feature.Feature
}

func newFailureRecordFixture(t *testing.T, f *feature.Feature) *failureRecordFixture {
	t.Helper()
	stateDir := t.TempDir()
	if f.ActiveRun == 0 {
		f.ActiveRun = 1
		f.RunCount = 1
	}
	if f.SchemaVersion == 0 {
		f.SchemaVersion = feature.SchemaVersionCurrent
	}
	store := feature.NewStore(stateDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	lc := lifecycleForFeature(f)
	lc.GetFn = func(id string) (*feature.Feature, error) { return store.Load(id) }
	lc.MarkFailedFn = func(id string, failure errcat.FailureRecord) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusFailed
			rec := failure
			ff.Run().Failure = &rec
			return nil
		})
	}
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})
	return &failureRecordFixture{o: o, store: store, f: f}
}

// requireStoredFailureRecord reloads the feature and returns its one stored
// failure record, failing the test when the terminal transition or the record
// itself is missing.
func requireStoredFailureRecord(t *testing.T, store *feature.Store, featureID string) *errcat.FailureRecord {
	t.Helper()
	f, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if f.Status != feature.StatusFailed {
		t.Fatalf("status = %s, want Failed after terminal path", f.Status)
	}
	rec := f.FailureRecord()
	if rec == nil {
		t.Fatalf("run failure record = nil, want exactly one stored record")
	}
	return rec
}

// requireRecordShape asserts the record's code, phase block name, and
// diagnostics.
func requireRecordShape(t *testing.T, rec *errcat.FailureRecord, wantCode errcat.Code, wantPhase, wantDiagnostics string) {
	t.Helper()
	if rec.Code != wantCode {
		t.Errorf("record code = %q, want %q", rec.Code, wantCode)
	}
	if rec.Context == nil || rec.Context.Phase == nil {
		t.Fatalf("record context = %+v, want phase block naming %q", rec.Context, wantPhase)
	}
	if rec.Context.Phase.Name != wantPhase {
		t.Errorf("record phase block name = %q, want %q", rec.Context.Phase.Name, wantPhase)
	}
	if rec.Diagnostics != wantDiagnostics {
		t.Errorf("record diagnostics = %q, want %q", rec.Diagnostics, wantDiagnostics)
	}
}

// requireRecordRepos asserts the record's repositories block equals want, in
// order.
func requireRecordRepos(t *testing.T, rec *errcat.FailureRecord, want ...string) {
	t.Helper()
	if rec.Context == nil {
		t.Fatalf("record context = nil, want repositories %v", want)
	}
	got := make([]string, 0, len(rec.Context.Repositories))
	for _, r := range rec.Context.Repositories {
		got = append(got, r.Name)
	}
	if len(got) != len(want) {
		t.Fatalf("record repositories = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record repositories[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Single-shot session failures
// ---------------------------------------------------------------------------

// A single-shot session failure without an explicit code defaults to
// session_crashed, scoped to the failing phase.
func TestFailureRecord_SingleShotSessionFailure_DefaultsToSessionCrashed(t *testing.T) {
	fx := newFailureRecordFixture(t, &feature.Feature{
		ID:           "fr-inquire-crash",
		Status:       feature.StatusInquiring,
		CurrentPhase: feature.PhaseInquire,
		Pipeline:     feature.PipelineLarge,
	})

	if err := fx.o.HandlePhaseCompletion(fx.f.ID, orchestrator.PhaseCompletionInput{
		Phase:       feature.PhaseInquire,
		Success:     false,
		ErrorDetail: "session died mid-turn",
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)
	requireRecordShape(t, rec, errcat.SessionCrashed, feature.PhaseInquire.FailureName(), "Inquire phase failed: session died mid-turn")
}

// An explicit FailureCode on the completion input is honored instead of the
// session_crashed default.
func TestFailureRecord_SingleShotFailure_HonorsInputFailureCode(t *testing.T) {
	fx := newFailureRecordFixture(t, &feature.Feature{
		ID:           "fr-inquire-code",
		Status:       feature.StatusInquiring,
		CurrentPhase: feature.PhaseInquire,
		Pipeline:     feature.PipelineLarge,
	})

	if err := fx.o.HandlePhaseCompletion(fx.f.ID, orchestrator.PhaseCompletionInput{
		Phase:       feature.PhaseInquire,
		Success:     false,
		ErrorDetail: "turn violated the completion protocol",
		FailureCode: errcat.ProtocolViolation,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)
	requireRecordShape(t, rec, errcat.ProtocolViolation, feature.PhaseInquire.FailureName(), "Inquire phase failed: turn violated the completion protocol")
}

// A KB session failure scopes the record to the per-repo KB phase: the
// repositories block names the repo whose session crashed.
func TestFailureRecord_KBSessionFailure_ScopesRecordToRepo(t *testing.T) {
	fx := newFailureRecordFixture(t, &feature.Feature{
		ID:           "frkb-crash",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos:        []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
	})

	if err := fx.o.HandlePhaseCompletion(fx.f.ID, orchestrator.PhaseCompletionInput{
		Phase:       feature.PhaseKnowledgeBase,
		SessionID:   "frkb-crash-kb-repo-a",
		Success:     false,
		ErrorDetail: "kb session exploded",
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)
	requireRecordShape(t, rec, errcat.SessionCrashed, feature.PhaseKnowledgeBase.FailureName(), "kb session exploded")
	requireRecordRepos(t, rec, repoName)
}

// ---------------------------------------------------------------------------
// Plan loop failures
// ---------------------------------------------------------------------------

func TestFailureRecord_PlanLoop_NilResult(t *testing.T) {
	fx := newFailureRecordFixture(t, &feature.Feature{
		ID:           "fr-plan-nil",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
		Pipeline:     feature.PipelineMedium,
	})

	if err := fx.o.HandlePhaseCompletion(fx.f.ID, orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: nil,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)
	requireRecordShape(t, rec, errcat.InfrastructureFailure, feature.PhasePlan.FailureName(), "plan loop returned no result")
}

func TestFailureRecord_PlanLoop_Failed_RecordsIteration(t *testing.T) {
	fx := newFailureRecordFixture(t, &feature.Feature{
		ID:           "fr-plan-failed",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
		Pipeline:     feature.PipelineMedium,
	})

	if err := fx.o.HandlePhaseCompletion(fx.f.ID, orchestrator.PhaseCompletionInput{
		Phase: feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{
			FinalStatus: finalStatusFailed,
			LastError:   "validator exploded",
			Iterations:  3,
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)
	requireRecordShape(t, rec, errcat.InfrastructureFailure, feature.PhasePlan.FailureName(), "validator exploded")
	if rec.Context.Phase.Iteration != 3 {
		t.Errorf("record phase iteration = %d, want 3", rec.Context.Phase.Iteration)
	}
}

func TestFailureRecord_PlanLoop_ProtocolViolation(t *testing.T) {
	fx := newFailureRecordFixture(t, &feature.Feature{
		ID:           "fr-plan-protocol",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
		Pipeline:     feature.PipelineMedium,
	})

	if err := fx.o.HandlePhaseCompletion(fx.f.ID, orchestrator.PhaseCompletionInput{
		Phase: feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{
			FinalStatus: "protocol_violation",
			LastError:   "protocol violation: plan_phase_planner @ /tmp/attempt-02: plan markdown is missing",
			Iterations:  2,
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)
	requireRecordShape(t, rec, errcat.ProtocolViolation, feature.PhasePlan.FailureName(), "protocol violation: plan_phase_planner @ /tmp/attempt-02: plan markdown is missing")
	if rec.Context.Phase.Iteration != 2 {
		t.Errorf("record phase iteration = %d, want 2", rec.Context.Phase.Iteration)
	}
}

func TestFailureRecord_PlanLoop_UnknownStatus(t *testing.T) {
	fx := newFailureRecordFixture(t, &feature.Feature{
		ID:           "fr-plan-unknown",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
		Pipeline:     feature.PipelineMedium,
	})

	if err := fx.o.HandlePhaseCompletion(fx.f.ID, orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "martian"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)
	requireRecordShape(t, rec, errcat.InfrastructureFailure, feature.PhasePlan.FailureName(), `unknown plan FinalStatus "martian"`)
}

// ---------------------------------------------------------------------------
// Implement loop failures
// ---------------------------------------------------------------------------

// An implement completion carrying neither result pointer is an
// infrastructure failure scoped to the implement phase.
func TestFailureRecord_ImplementLoop_MissingResult(t *testing.T) {
	fx := newFailureRecordFixture(t, &feature.Feature{
		ID:           "fr-impl-missing",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Pipeline:     feature.PipelineMedium,
	})

	if err := fx.o.HandlePhaseCompletion(fx.f.ID, orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)
	requireRecordShape(t, rec, errcat.InfrastructureFailure, feature.PhaseImplement.FailureName(), "implement completion missing multi-repo result")
}

// A multi-repo "failed" outcome preserves the per-repo classification: a
// protocol violation anywhere wins, then an iteration cap, then a safety
// rail, defaulting to infrastructure. The repositories block must equal the
// failed repositories and the phase block carries the live iteration.
func TestFailureRecord_MultiRepoFailed_PreservesRepoClassification(t *testing.T) {
	cases := []struct {
		name         string
		repoStatuses map[string]string
		wantCode     errcat.Code
	}{
		{
			name:         "iteration exhaustion",
			repoStatuses: map[string]string{repoName: "max_iterations", repoNameB: finalStatusFailed},
			wantCode:     errcat.IterationBudgetExhausted,
		},
		{
			name:         "protocol violation wins over other statuses",
			repoStatuses: map[string]string{repoName: finalStatusFailed, repoNameB: "protocol_violation"},
			wantCode:     errcat.ProtocolViolation,
		},
		{
			name:         "safety rail",
			repoStatuses: map[string]string{repoName: "safety_rail"},
			wantCode:     errcat.SafetyRailTripped,
		},
		{
			name:         "infrastructure default",
			repoStatuses: map[string]string{repoName: finalStatusFailed},
			wantCode:     errcat.InfrastructureFailure,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFailureRecordFixture(t, &feature.Feature{
				ID:               "fr-multi-" + tc.name,
				Status:           feature.StatusImplementing,
				CurrentPhase:     feature.PhaseImplement,
				Pipeline:         feature.PipelineMedium,
				CurrentIteration: 4,
				Repos: []feature.FeatureRepo{
					{Name: repoName, Path: repoAPath},
					{Name: repoNameB, Path: repoBPath},
				},
			})

			if err := fx.o.HandlePhaseCompletion(fx.f.ID, orchestrator.PhaseCompletionInput{
				Phase: feature.PhaseImplement,
				MultiRepoResult: &agent.OrchestratorResult{
					FinalStatus:  finalStatusFailed,
					LastError:    "impl blew up",
					RepoStatuses: tc.repoStatuses,
					FailedRepos:  []string{repoName, repoNameB},
				},
			}); err != nil {
				t.Fatalf("HandlePhaseCompletion: %v", err)
			}

			rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)
			requireRecordShape(t, rec, tc.wantCode, feature.PhaseImplement.FailureName(), "impl blew up")
			requireRecordRepos(t, rec, repoName, repoNameB)
			if rec.Context.Phase.Iteration != 4 {
				t.Errorf("record phase iteration = %d, want the live iteration 4", rec.Context.Phase.Iteration)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Deferred Final Review failures
// ---------------------------------------------------------------------------

// newFinalReviewFixture stages an implementing feature whose touched repos
// await the deferred end-of-feature Final Review pass.
func newFinalReviewFixture(t *testing.T, featureID string) *failureRecordFixture {
	t.Helper()
	pub := true
	return newFailureRecordFixture(t, &feature.Feature{
		ID:               featureID,
		Status:           feature.StatusImplementing,
		CurrentPhase:     feature.PhaseImplement,
		Pipeline:         feature.PipelineLarge,
		CurrentIteration: 2,
		Repos: []feature.FeatureRepo{
			{Name: apiRepoName, Path: apiRepoWorkPath, Publishable: &pub},
		},
		RepoStates: map[string]*feature.RepoState{
			apiRepoName: {Touched: true},
		},
	})
}

func TestFailureRecord_FinalReview_DispatchFailures(t *testing.T) {
	cases := []struct {
		name            string
		installFRFn     func(o *orchestrator.Orchestrator)
		wantDiagnostics string
	}{
		{
			name:            "no phase runner configured",
			installFRFn:     func(o *orchestrator.Orchestrator) {},
			wantDiagnostics: "dispatch final review: phase runner not configured",
		},
		{
			name: "engine dispatch errors",
			installFRFn: func(o *orchestrator.Orchestrator) {
				o.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
					return nil, errors.New("engine wedged")
				})
			},
			wantDiagnostics: "dispatch final review: engine wedged",
		},
		{
			name: "engine returns no result channel",
			installFRFn: func(o *orchestrator.Orchestrator) {
				o.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
					return nil, nil
				})
			},
			wantDiagnostics: "dispatch final review returned no result channel",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFinalReviewFixture(t, "fr-dispatch-"+tc.name)
			tc.installFRFn(fx.o)

			err := fx.o.HandlePhaseCompletion(fx.f.ID, orchestrator.PhaseCompletionInput{
				Phase:           feature.PhaseImplement,
				MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "awaiting_final_review"},
			})
			if err == nil {
				t.Fatal("HandlePhaseCompletion() error = nil, want final review failure")
			}

			rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)
			requireRecordShape(t, rec, errcat.InfrastructureFailure, feature.PhaseFinalReview.FailureName(), tc.wantDiagnostics)
		})
	}
}

func TestFailureRecord_FinalReview_Failed(t *testing.T) {
	fx := newFinalReviewFixture(t, "fr-failed")
	fx.o.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{
			FinalStatus:  finalStatusFailed,
			LastError:    "final review blew up",
			RepoStatuses: map[string]string{apiRepoName: "protocol_violation"},
			FailedRepos:  []string{apiRepoName},
		}
		return ch, nil
	})

	err := fx.o.HandlePhaseCompletion(fx.f.ID, orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "awaiting_final_review"},
	})
	if err == nil {
		t.Fatal("HandlePhaseCompletion() error = nil, want final review failure")
	}

	rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)
	requireRecordShape(t, rec, errcat.ProtocolViolation, feature.PhaseFinalReview.FailureName(), "final review blew up")
	requireRecordRepos(t, rec, apiRepoName)
	if rec.Context.Phase.Iteration != 2 {
		t.Errorf("record phase iteration = %d, want the live iteration 2", rec.Context.Phase.Iteration)
	}
}

// ---------------------------------------------------------------------------
// Typed delegate boundaries
// ---------------------------------------------------------------------------

func TestFailureRecord_Delegates_ClassifyTypedBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		invoke   func(o *orchestrator.Orchestrator, featureID string) error
		wantCode errcat.Code
	}{
		{
			name: "publish UI failure is infrastructure",
			invoke: func(o *orchestrator.Orchestrator, featureID string) error {
				return o.RecordPublishUIFailure(featureID, "publish UI surfaced an error")
			},
			wantCode: errcat.InfrastructureFailure,
		},
		{
			name: "missing artifact is artifact_missing",
			invoke: func(o *orchestrator.Orchestrator, featureID string) error {
				return o.ReportMissingArtifactFailure(featureID, "expected artifact absent")
			},
			wantCode: errcat.ArtifactMissing,
		},
		{
			name: "protocol violation is protocol_violation",
			invoke: func(o *orchestrator.Orchestrator, featureID string) error {
				return o.ReportProtocolViolation(featureID, "root turn violated the protocol")
			},
			wantCode: errcat.ProtocolViolation,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFailureRecordFixture(t, &feature.Feature{
				ID:           "fr-delegate-" + tc.name,
				Status:       feature.StatusImplementing,
				CurrentPhase: feature.PhaseImplement,
				Pipeline:     feature.PipelineMedium,
			})

			if err := tc.invoke(fx.o, fx.f.ID); err != nil {
				t.Fatalf("delegate: %v", err)
			}

			rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)
			if rec.Code != tc.wantCode {
				t.Errorf("record code = %q, want %q", rec.Code, tc.wantCode)
			}
			// delegateFailureRecord names the feature's current phase.
			if rec.Context == nil || rec.Context.Phase == nil || rec.Context.Phase.Name != feature.PhaseImplement.FailureName() {
				t.Errorf("record phase block = %+v, want phase %q", rec.Context, feature.PhaseImplement.FailureName())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Restart gating on the stored record
// ---------------------------------------------------------------------------

// The iteration budget is extended only when the stored record's code is
// iteration_budget_exhausted; every other code clears the record without
// touching the budget.
func TestFailureRecord_Restart_BudgetExtensionGatedOnIterationCode(t *testing.T) {
	t.Run("iteration budget exhausted extends budget and clears record", func(t *testing.T) {
		fx := newFailureRecordFixture(t, &feature.Feature{
			ID:            "fr-restart-iter",
			Status:        feature.StatusFailed,
			CurrentPhase:  feature.PhaseImplement,
			Pipeline:      feature.PipelineMedium,
			MaxIterations: 20,
		})
		if err := fx.store.Modify(fx.f.ID, func(ff *feature.Feature) error {
			ff.Run().Failure = &errcat.FailureRecord{
				Code:        errcat.IterationBudgetExhausted,
				Context:     &errcat.RecordContext{Phase: &errcat.CodePhase{Name: feature.PhaseImplement.FailureName()}},
				Diagnostics: "iteration cap",
			}
			return nil
		}); err != nil {
			t.Fatalf("seed failure record: %v", err)
		}

		if err := fx.o.ExtendFailedPhaseBudget(fx.f.ID, 10, 0); err != nil {
			t.Fatalf("ExtendFailedPhaseBudget: %v", err)
		}
		f, err := fx.store.Load(fx.f.ID)
		if err != nil {
			t.Fatalf("load feature: %v", err)
		}
		if f.MaxIterations != 30 {
			t.Errorf("MaxIterations = %d, want 30 for iteration_budget_exhausted", f.MaxIterations)
		}
		if rec := f.FailureRecord(); rec != nil {
			t.Errorf("failure record = %+v, want cleared by restart", rec)
		}
	})

	t.Run("other failure codes clear the record without extending the budget", func(t *testing.T) {
		fx := newFailureRecordFixture(t, &feature.Feature{
			ID:            "fr-restart-crash",
			Status:        feature.StatusFailed,
			CurrentPhase:  feature.PhaseImplement,
			Pipeline:      feature.PipelineMedium,
			MaxIterations: 20,
		})
		if err := fx.store.Modify(fx.f.ID, func(ff *feature.Feature) error {
			ff.Run().Failure = &errcat.FailureRecord{Code: errcat.SessionCrashed, Diagnostics: "session died"}
			return nil
		}); err != nil {
			t.Fatalf("seed failure record: %v", err)
		}

		if err := fx.o.ExtendFailedPhaseBudget(fx.f.ID, 10, 0); err != nil {
			t.Fatalf("ExtendFailedPhaseBudget: %v", err)
		}
		f, err := fx.store.Load(fx.f.ID)
		if err != nil {
			t.Fatalf("load feature: %v", err)
		}
		if f.MaxIterations != 20 {
			t.Errorf("MaxIterations = %d, want unchanged 20 for session_crashed", f.MaxIterations)
		}
		if rec := f.FailureRecord(); rec != nil {
			t.Errorf("failure record = %+v, want cleared by restart", rec)
		}
	})
}

// ---------------------------------------------------------------------------
// Domain event contract
// ---------------------------------------------------------------------------

// The FeatureFailed domain event carries a rendered CanonicalError whose code
// and class match the stored record, plus the raw diagnostics as Message.
func TestFailureRecord_FeatureFailedEvent_CarriesCanonicalError(t *testing.T) {
	fx := newFailureRecordFixture(t, &feature.Feature{
		ID:           "fr-event",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
		Pipeline:     feature.PipelineMedium,
	})

	if err := fx.o.HandlePhaseCompletion(fx.f.ID, orchestrator.PhaseCompletionInput{
		Phase: feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{
			FinalStatus: finalStatusFailed,
			LastError:   "validator exploded",
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	rec := requireStoredFailureRecord(t, fx.store, fx.f.ID)

	events := drainEvents(fx.o)
	var failed *ports.Event
	for i := range events {
		if events[i].Type == ports.FeatureFailed {
			failed = &events[i]
			break
		}
	}
	if failed == nil {
		t.Fatalf("events = %+v, want FeatureFailed", events)
	}
	if failed.Message != rec.Diagnostics {
		t.Errorf("FeatureFailed.Message = %q, want record diagnostics %q", failed.Message, rec.Diagnostics)
	}
	if failed.CanonicalError == nil {
		t.Fatalf("FeatureFailed.CanonicalError = nil, want rendered canonical error")
	}
	if failed.CanonicalError.Code != rec.Code {
		t.Errorf("CanonicalError.Code = %q, want record code %q", failed.CanonicalError.Code, rec.Code)
	}
	wantClass := errcat.ClassBlocking
	if entry, ok := errcat.Lookup(rec.Code); ok {
		wantClass = entry.Class
	}
	if failed.CanonicalError.Class != wantClass {
		t.Errorf("CanonicalError.Class = %q, want catalog class %q", failed.CanonicalError.Class, wantClass)
	}
}

// ---------------------------------------------------------------------------
// Legacy error.log removal
// ---------------------------------------------------------------------------

// A terminal failure must not leave a legacy error.log anywhere under the
// feature's active run directory: the canonical failure record owns the
// durable failure state now.
func TestFailureRecord_TerminalFailureWritesNoErrorLog(t *testing.T) {
	stateDir := t.TempDir()
	f := &feature.Feature{
		ID:            "fr-no-errorlog",
		Status:        feature.StatusInquiring,
		CurrentPhase:  feature.PhaseInquire,
		Pipeline:      feature.PipelineLarge,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	store := feature.NewStore(stateDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	lc := lifecycleForFeature(f)
	lc.GetFn = func(id string) (*feature.Feature, error) { return store.Load(id) }
	lc.MarkFailedFn = func(id string, failure errcat.FailureRecord) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusFailed
			rec := failure
			ff.Run().Failure = &rec
			return nil
		})
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       store,
		PhaseRunner: &agent.PhaseRunner{StateDir: stateDir},
	}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:       feature.PhaseInquire,
		Success:     false,
		ErrorDetail: "session died mid-turn",
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if rec := requireStoredFailureRecord(t, store, f.ID); rec.Code != errcat.SessionCrashed {
		t.Fatalf("record code = %q, want session_crashed", rec.Code)
	}

	featureDir := filepath.Join(stateDir, f.ID)
	if _, err := os.Stat(featureDir); err != nil {
		t.Fatalf("stat feature dir: %v", err)
	}
	found := []string{}
	err := filepath.Walk(featureDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() && info.Name() == "error.log" {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk feature dir: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("error.log files found under run dir: %v, want none", found)
	}
}
