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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func ptrTime() *time.Time {
	t := time.Now()
	return &t
}

func untrackedFinalReviewArtifactsRunner(t *testing.T, candidates []string) *mocks.MockCommandRunner {
	t.Helper()

	untracked := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		untracked[candidate] = true
	}

	runner := mocks.NewMockCommandRunner()
	runner.RunFn = func(_ context.Context, name string, args []string, _ ports.CommandOpts) ([]byte, error) {
		if name != "git" {
			t.Fatalf("CommandRunner name = %q, want git", name)
		}
		if len(args) == 0 {
			t.Fatalf("CommandRunner args empty, want git ls-files invocation")
		}
		candidate := args[len(args)-1]
		if untracked[candidate] {
			return []byte(candidate + "\n"), nil
		}
		return nil, nil
	}
	return runner
}

// TestOrchestrator_StartFeature_InterruptedFinalReview_ReDispatchesFR is the
// regression for: pressing [r] on a feature stuck at StatusInterrupted with
// CurrentPhase=PhaseFinalReview was a silent no-op. RestartPhase returns the
// dispatch action and StartFeature executes it.
// Before the fix, startPhase had no case for PhaseFinalReview and returned
// "unknown phase 8" — the error was swallowed. Now startFinalReview re-runs
// the deferred FR pass and advances through MarkCodeReady on success.
func TestOrchestrator_StartFeature_InterruptedFinalReview_ReDispatchesFR(t *testing.T) {
	unpub := false
	f := &feature.Feature{
		ID:           "feat-fr-resume",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseFinalReview,
		Repos:        []feature.FeatureRepo{{Name: "r1", Path: "/tmp/r1", Publishable: &unpub}},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
		Pipeline:  feature.PipelineLarge,
		StartedAt: ptrTime(),
	}
	lc := lifecycleForFeature(f)
	lc.MarkFinalReviewReadyFn = func(id string) error { f.Status = feature.StatusFinalReviewing; return nil }
	lc.MarkCodeReadyFn = func(id string) error { f.Status = feature.StatusCodeReady; return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	o.SetRunMultiRepoFinalReviewFn(func(
		ff *feature.Feature,
		_ ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	})

	if err := o.StartFeature("feat-fr-resume"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	assertLifecycleCall(t, lc, "MarkFinalReviewReady")
	assertLifecycleCall(t, lc, "MarkCodeReady")
}

// TestOrchestrator_OnMultiReposPassed_N1_NoLegacySingleRepoFRRouting verifies
// that the legacy `len(f.Repos) <= 1` branch in onMultiReposPassed is gone —
// N=1 features no longer call StartFinalReview or dispatch PhaseReview. They
// fall through to the multi-repo aggregation path.
func TestOrchestrator_OnMultiReposPassed_N1_NoLegacySingleRepoFRRouting(t *testing.T) {
	unpub := false
	f := &feature.Feature{
		ID:           "feat-n1-no-legacy-fr",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos:        []feature.FeatureRepo{{Name: "r1", Path: "/tmp/r1", Publishable: &unpub}},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { f.Status = feature.StatusReviewPassed; return nil }
	lc.MarkCodeReadyFn = func(id string) error { f.Status = feature.StatusCodeReady; return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-n1-no-legacy-fr", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	refuteLifecycleCall(t, lc, "StartFinalReview")
	assertLifecycleCall(t, lc, "CompleteImplementation")
	assertLifecycleCall(t, lc, "MarkCodeReady")

	events := drainEvents(o)
	if hasPhaseStarted(events, feature.PhaseReview) != nil {
		t.Error("expected NO PhaseStarted(PhaseReview) — legacy single-repo FR routing should be gone")
	}
}

func TestOrchestrator_OnMultiReposPassed_MediumRunsDeferredFinalReview(t *testing.T) {
	unpub := false
	f := &feature.Feature{
		ID:           "feat-medium-fr",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Pipeline:     feature.PipelineMedium,
		Repos:        []feature.FeatureRepo{{Name: "r1", Path: "/tmp/r1", Publishable: &unpub}},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { f.Status = feature.StatusReviewPassed; return nil }
	lc.MarkFinalReviewReadyFn = func(id string) error { f.Status = feature.StatusFinalReviewing; return nil }
	lc.MarkCodeReadyFn = func(id string) error { f.Status = feature.StatusCodeReady; return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})
	o.SetRunMultiRepoFinalReviewFn(func(
		ff *feature.Feature,
		_ ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		if ff.EffectivePipeline() != feature.PipelineMedium {
			t.Errorf("final review feature pipeline = %q, want %q", ff.EffectivePipeline(), feature.PipelineMedium)
		}
		if ff.Status != feature.StatusFinalReviewing {
			t.Errorf("final review feature status = %v, want %v", ff.Status, feature.StatusFinalReviewing)
		}
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	})

	if err := o.HandlePhaseCompletion("feat-medium-fr", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "awaiting_final_review"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "CompleteImplementation")
	assertLifecycleCall(t, lc, "MarkFinalReviewReady")
	assertLifecycleCall(t, lc, "MarkCodeReady")
	if got := lifecycleCallNames(lc); indexOf(got, "MarkFinalReviewReady") > indexOf(got, "MarkCodeReady") {
		t.Fatalf("MarkFinalReviewReady must happen before MarkCodeReady; calls: %v", got)
	}
	events := drainEvents(o)
	if !hasPhaseCompleted(events, feature.PhaseFinalReview) {
		t.Fatalf("expected PhaseCompleted(PhaseFinalReview); events: %v", events)
	}
}

func TestOrchestrator_OnMultiReposPassed_FinalReviewPlanRevisionResultFailsWithoutPlanning(t *testing.T) {
	tests := []struct {
		name     string
		pipeline feature.PipelineProfile
	}{
		{name: "medium", pipeline: feature.PipelineMedium},
		{name: "large", pipeline: feature.PipelineLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cpr := newCapturingPhaseRunner(t)

			featureID := "feat-fr-missing-evidence-" + tt.name
			roadmapPath := filepath.Join(cpr.stateDir, "roadmap.md")
			writeRoadmap(t, roadmapPath)
			unpub := false
			f := &feature.Feature{
				ID:                  featureID,
				Status:              feature.StatusImplementing,
				CurrentPhase:        feature.PhaseImplement,
				CurrentRoadmapPhase: 1,
				TotalRoadmapPhases:  1,
				ActiveRun:           1,
				Pipeline:            tt.pipeline,
				Artifacts:           map[string]string{"roadmap": roadmapPath},
				Repos:               []feature.FeatureRepo{{Name: "r1", Path: cpr.stateDir, Publishable: &unpub}},
				RepoStates: map[string]*feature.RepoState{
					"r1": {Touched: true},
				},
			}
			phasePlanDir := agent.PhasePlanDir(cpr.stateDir, f, 1)
			if err := os.MkdirAll(phasePlanDir, 0o755); err != nil {
				t.Fatalf("mkdir phase plan dir: %v", err)
			}
			if err := agent.WritePlanAttemptMeta(phasePlanDir, agent.PlanAttemptMeta{
				Attempt:      1,
				AgentStatus:  "SUCCESS",
				ReviewStatus: "APPROVED",
			}); err != nil {
				t.Fatalf("seed approved phase plan attempt: %v", err)
			}

			lc := lifecycleForFeature(f)
			lc.CompleteImplementationFn = func(id string) error { f.Status = feature.StatusReviewPassed; return nil }
			lc.MarkFinalReviewReadyFn = func(id string) error { f.Status = feature.StatusFinalReviewing; return nil }
			lc.MarkCodeReadyFn = func(id string) error {
				t.Fatalf("MarkCodeReady called after unsupported final-review plan revision")
				return nil
			}
			lc.MarkFailedFn = func(id, failureType, lastError string) error {
				if failureType != feature.FailureInfrastructure {
					t.Fatalf("failureType = %q, want %q", failureType, feature.FailureInfrastructure)
				}
				if !strings.Contains(lastError, "final review requested unsupported phase-plan revision") {
					t.Fatalf("lastError = %q, want unsupported phase-plan revision", lastError)
				}
				f.Status = feature.StatusFailed
				f.FailureType = failureType
				f.LastError = lastError
				return nil
			}
			fs := newFeatureStore(f)
			cpr.pr.FeatureStore = fs
			o := orchestrator.New(orchestrator.Deps{
				Lifecycle:   lc,
				Store:       fs,
				Sessions:    cpr.sm,
				PhaseRunner: cpr.pr,
				CmdRunner:   cpr.cmd,
			}, orchestrator.Hooks{})
			o.SetRunMultiRepoFinalReviewFn(func(ff *feature.Feature, _ ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
				if ff.EffectivePipeline() != tt.pipeline {
					t.Errorf("final review feature pipeline = %q, want %q", ff.EffectivePipeline(), tt.pipeline)
				}
				ch := make(chan *agent.OrchestratorResult, 1)
				ch <- &agent.OrchestratorResult{
					FinalStatus:          "plan_revision_required",
					PlanRevisionFeedback: "MISSING_EVIDENCE_REQUIREMENT behavioral: Record the create-project CLI journey.",
				}
				return ch, nil
			})

			if err := o.HandlePhaseCompletion(featureID, orchestrator.PhaseCompletionInput{
				Phase:           feature.PhaseImplement,
				MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "awaiting_final_review"},
			}); err == nil || !strings.Contains(err.Error(), "final review requested unsupported phase-plan revision") {
				t.Fatalf("HandlePhaseCompletion() error = %v, want unsupported phase-plan revision", err)
			}

			if f.Status != feature.StatusFailed {
				t.Fatalf("feature status = %v, want Failed after unsupported final-review plan revision", f.Status)
			}
			if captured := cpr.capturedOpts; len(captured) != 0 {
				t.Fatalf("unexpected phase-plan revision session captured: %+v", captured)
			}
			if _, err := os.Stat(filepath.Join(phasePlanDir, "attempt-01", "validation-feedback.md")); !os.IsNotExist(err) {
				t.Fatalf("final review must not write phase-plan validation feedback, stat err = %v", err)
			}
			if latest := agent.LatestCompletedPlanAttempt(phasePlanDir); latest != 1 {
				t.Fatalf("LatestCompletedPlanAttempt() = %d, want approved attempt to remain intact", latest)
			}
		})
	}
}

func TestOrchestrator_HandlePhaseCompletion_Implement_MissingEvidenceStaysOnCurrentRoadmapPhase(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	cpr.sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return nil, session.ErrShuttingDown
	}

	featureID := "feat-impl-missing-evidence-current-phase"
	roadmapPath := filepath.Join(cpr.stateDir, "roadmap.md")
	writeRoadmap(t, roadmapPath)
	unpub := false
	f := &feature.Feature{
		ID:                  featureID,
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  2,
		ActiveRun:           1,
		RunCount:            1,
		Pipeline:            feature.PipelineLarge,
		Artifacts:           map[string]string{"roadmap": roadmapPath},
		Repos:               []feature.FeatureRepo{{Name: "r1", Path: cpr.stateDir, Publishable: &unpub}},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
	}
	phase1PlanDir := agent.PhasePlanDir(cpr.stateDir, f, 1)
	phase2PlanDir := agent.PhasePlanDir(cpr.stateDir, f, 2)
	for phase, dir := range map[int]string{1: phase1PlanDir, 2: phase2PlanDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir phase %d plan dir: %v", phase, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte(fmt.Sprintf("# Phase %d plan", phase)), 0o644); err != nil {
			t.Fatalf("write phase %d plan: %v", phase, err)
		}
		if err := agent.WritePlanAttemptMeta(dir, agent.PlanAttemptMeta{
			Attempt:      1,
			AgentStatus:  "SUCCESS",
			ReviewStatus: "APPROVED",
		}); err != nil {
			t.Fatalf("seed phase %d approved attempt: %v", phase, err)
		}
	}

	lc := lifecycleForFeature(f)
	lc.StartPlanningFn = func(id string) error {
		f.Status = feature.StatusPlanning
		return nil
	}
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})
	if err := o.HandlePhaseCompletion(featureID, orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{
			FinalStatus:          "plan_revision_required",
			PlanRevisionFeedback: "MISSING_EVIDENCE_REQUIREMENT phase 1 behavioral: Record the phase-one create-project CLI journey.",
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}

	if f.CurrentRoadmapPhase != 2 {
		t.Fatalf("CurrentRoadmapPhase = %d, want current phase 2", f.CurrentRoadmapPhase)
	}
	if captured := waitForCapturedPhase(t, cpr, feature.PhasePlan, 3*time.Second); len(captured) == 0 {
		t.Fatalf("no phase-plan revision session captured; captures: %+v", cpr.capturedOpts)
	}
	data, err := os.ReadFile(filepath.Join(phase2PlanDir, "attempt-01", "validation-feedback.md"))
	if err != nil {
		t.Fatalf("read phase 2 validation feedback: %v", err)
	}
	if got := string(data); !strings.Contains(got, "MISSING_EVIDENCE_REQUIREMENT phase 1 behavioral: Record the phase-one create-project CLI journey.") {
		t.Fatalf("phase 2 validation feedback missing reviewer requirement:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(phase1PlanDir, "attempt-01", "validation-feedback.md")); !os.IsNotExist(err) {
		t.Fatalf("phase 1 validation feedback should not be overwritten for a current-phase repair, stat err = %v", err)
	}
}

// TestOrchestrator_OnMultiReposPassed_N1_AutoPublishComplete_EmitsPublishCompleted
// verifies the auto-publish path for N=1: when the per-repo publish fired
// already (via OnRepoStatusChanged eager publish) and the cross-repo join
// reaches tryCompleteAndEmit with published==true, the orchestrator emits
// PublishCompleted and fires OnPublishCompleted.
func TestOrchestrator_OnMultiReposPassed_N1_AutoPublishComplete_EmitsPublishCompleted(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-n1-pub",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos:        []feature.FeatureRepo{{Name: "r1", Path: "/tmp/r1"}},
		RepoStates: map[string]*feature.RepoState{
			"r1": {PRURL: "https://github.com/org/r1/pull/1"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { f.Status = feature.StatusReviewPassed; return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { f.Status = feature.StatusPublished; return true, nil }
	fs := newFeatureStore(f)

	var pubURLs map[string]string
	var pubID string
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnPublishCompleted: func(id string, urls map[string]string, err error) {
			pubID = id
			pubURLs = urls
		},
	})

	if err := o.HandlePhaseCompletion("feat-n1-pub", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	events := drainEvents(o)
	if !hasEventType(events, ports.PublishCompleted) {
		t.Error("expected PublishCompleted event")
	}
	if pubID != "feat-n1-pub" {
		t.Errorf("OnPublishCompleted feature ID = %q, want %q", pubID, "feat-n1-pub")
	}
	if got := pubURLs["r1"]; got != "https://github.com/org/r1/pull/1" {
		t.Errorf("OnPublishCompleted urls[r1] = %q, want PR URL", got)
	}
}

// TestOrchestrator_FeatureFinalReview_3Repo_Approves_AllReposAdvanceAndPublish
// is the slice-3 integration coverage for the unified feature-level Final
// Review: a 3-repo feature reaches FR with every repo at
// "awaiting_final_review", the FR pass approves, every repo transitions
// atomically to "review_passed", MarkCodeReady fires once for the
// feature, and per-repo auto-publish creates per-repo PRs.
func TestOrchestrator_FeatureFinalReview_3Repo_Approves_AllReposAdvanceAndPublish(t *testing.T) {
	pub := true
	f := &feature.Feature{
		ID:           "feat-3repo-fr-success",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: apiRepoName, Path: apiRepoWorkPath, Publishable: &pub},
			{Name: "web", Path: "/tmp/web", Publishable: &pub},
			{Name: "infra", Path: "/tmp/infra", Publishable: &pub},
		},
		RepoStates: map[string]*feature.RepoState{
			apiRepoName: {Touched: true},
			"web":       {Touched: true},
			"infra":     {Touched: true},
		},
		Pipeline: feature.PipelineLarge,
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { f.Status = feature.StatusReviewPassed; return nil }
	lc.MarkFinalReviewReadyFn = func(id string) error { f.Status = feature.StatusFinalReviewing; return nil }
	lc.MarkCodeReadyFn = func(id string) error { f.Status = feature.StatusCodeReady; return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) {
		// Realistic gate: only transition when the feature is parked in
		// the publish-ready window (ReviewPassed / CodeReady). The mock
		// must not preemptively set StatusPublished while FR is still
		// running, or runDeferredFinalReview's success-path
		// `Transition(StatusReviewPassed)` will trip the FSM.
		if f.Status != feature.StatusReviewPassed && f.Status != feature.StatusCodeReady {
			return false, nil
		}
		f.Status = feature.StatusPublished
		return true, nil
	}
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	// Stub the unified FR engine: approve, transition all three repos to
	// "review_passed" (simulating AtomicPhaseStamp's effect), and
	// return all_passed so the orchestrator falls through to MarkCodeReady
	// and per-repo auto-publish.
	o.SetRunMultiRepoFinalReviewFn(func(
		ff *feature.Feature,
		_ ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		go func() {
			// Atomically mark each staged repo as Touched (FR success
			// stages every repo for the publish loop downstream).
			_ = fs.Modify(ff.ID, func(target *feature.Feature) error {
				for _, r := range []string{apiRepoName, "web", "infra"} {
					if st := target.RepoStates[r]; st != nil {
						st.Touched = true
					}
				}
				return nil
			})
			ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		}()
		return ch, nil
	})

	publishedRepos := map[string]string{}
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		url := "https://github.com/org/" + repo + "/pull/1"
		publishedRepos[repo] = url
		return url, nil
	})

	if err := o.HandlePhaseCompletion("feat-3repo-fr-success", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "awaiting_final_review"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	// FR ran (MarkFinalReviewReady), then MarkCodeReady, then per-repo publish.
	assertLifecycleCall(t, lc, "MarkFinalReviewReady")
	assertLifecycleCall(t, lc, "CompleteImplementation")
	if len(publishedRepos) != 3 {
		t.Errorf("publishRepoFn calls = %d (publishedRepos=%v), want 3 (one per repo)", len(publishedRepos), publishedRepos)
	}
	for _, name := range []string{apiRepoName, "web", "infra"} {
		st := f.RepoStates[name]
		if st == nil {
			t.Errorf("RepoImpl[%q] = nil after FR", name)
			continue
		}
		if !st.Touched {
			t.Errorf("RepoStates[%q] = %+v, want Touched=true after FR", name, st)
		}
	}
}

func TestAdvanceAfterFinalReviewScrubsRootArtifactsBeforeCommitAll(t *testing.T) {
	pub := true
	candidates := []string{"phase_complete", "progress.md", "verification-report.yaml", "review-feedback.md", "meta.yaml"}
	repo := t.TempDir()
	for _, name := range candidates {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("stray\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "sub", "phase_complete"), []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write nested phase_complete: %v", err)
	}

	f := &feature.Feature{
		ID:           "feat-fr-scrub",
		Name:         "FR scrub",
		Slug:         "fr-scrub",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Pipeline:     feature.PipelineLarge,
		Checkpoints:  feature.Checkpoints{},
		Repos: []feature.FeatureRepo{{
			Name:         apiRepoName,
			Path:         repo,
			WorktreePath: repo,
			Publishable:  &pub,
			Branch:       "feature/fr-scrub",
			BaseBranch:   mainBranch,
		}},
		RepoStates: map[string]*feature.RepoState{apiRepoName: {Touched: true}},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { f.Status = feature.StatusReviewPassed; return nil }
	lc.MarkFinalReviewReadyFn = func(id string) error { f.Status = feature.StatusFinalReviewing; return nil }
	lc.SetRepoPublishedFn = func(featureID, repoName, prURL string) error {
		f.RepoStates[repoName].PRURL = prURL
		return nil
	}
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	publisher := mocks.NewMockPublisher()
	publisher.HasUncommittedChangesFn = func(worktreePath string) (bool, error) { return true, nil }
	publisher.CommitAllFn = func(worktreePath, message string) error {
		for _, name := range candidates {
			if _, err := os.Stat(filepath.Join(worktreePath, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s still exists before CommitAll: %v", name, err)
			}
		}
		if _, err := os.Stat(filepath.Join(worktreePath, "sub", "phase_complete")); err != nil {
			t.Fatalf("nested phase_complete removed, want preserved: %v", err)
		}
		return nil
	}
	publisher.DiffStatFn = func(string, string) (string, error) { return "", nil }
	publisher.CommitBodiesFn = func(string, string) (string, error) { return "", nil }
	publisher.PushFn = func(string, string) error { return nil }
	publisher.CreatePRFn = func(string, string, string, string, string, bool) (string, error) {
		return "https://github.com/org/api/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Publisher: publisher,
		CmdRunner: untrackedFinalReviewArtifactsRunner(t, candidates),
		PhaseRunner: newPublishDescriptionPhaseRunner(
			t,
			"TITLE: Final review complete\nBODY:\n## Summary\n\nVerified changes",
			false,
		),
	}, orchestrator.Hooks{})
	o.SetRunMultiRepoFinalReviewFn(func(
		ff *feature.Feature,
		_ ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	})

	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "awaiting_final_review"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}
	if got := countPublisherCalls(publisher, "CommitAll"); got != 1 {
		t.Fatalf("CommitAll calls = %d, want 1", got)
	}
}

func TestAdvanceAfterFinalReviewRoadmapFinalScrubsRootArtifactsBeforeCommitAll(t *testing.T) {
	pub := true
	candidates := []string{"phase_complete", "progress.md", "verification-report.yaml", "review-feedback.md", "meta.yaml"}
	repoA := t.TempDir()
	repoB := t.TempDir()
	writeCandidates := func() {
		t.Helper()
		for _, repo := range []string{repoA, repoB} {
			for _, name := range candidates {
				if err := os.WriteFile(filepath.Join(repo, name), []byte("stray\n"), 0o644); err != nil {
					t.Fatalf("write %s in %s: %v", name, repo, err)
				}
			}
		}
	}
	assertCandidatesRemoved := func(worktreePath string) {
		t.Helper()
		for _, name := range candidates {
			if _, err := os.Stat(filepath.Join(worktreePath, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s still exists in %s before publish CommitAll: %v", name, worktreePath, err)
			}
		}
	}

	f := &feature.Feature{
		ID:                  "feat-fr-roadmap-scrub",
		Name:                "FR roadmap scrub",
		Slug:                "fr-roadmap-scrub",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  2,
		Pipeline:            feature.PipelineLarge,
		Checkpoints:         feature.Checkpoints{},
		Repos: []feature.FeatureRepo{
			{
				Name:         apiRepoName,
				Path:         repoA,
				WorktreePath: repoA,
				Publishable:  &pub,
				Branch:       "feature/fr-roadmap-scrub-api",
				BaseBranch:   mainBranch,
			},
			{
				Name:         "web",
				Path:         repoB,
				WorktreePath: repoB,
				Publishable:  &pub,
				Branch:       "feature/fr-roadmap-scrub-web",
				BaseBranch:   mainBranch,
			},
		},
		RepoStates: map[string]*feature.RepoState{
			apiRepoName: {Touched: true},
			"web":       {Touched: true},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { f.Status = feature.StatusReviewPassed; return nil }
	lc.MarkFinalReviewReadyFn = func(id string) error { f.Status = feature.StatusFinalReviewing; return nil }
	lc.MarkCodeReadyFn = func(id string) error { f.Status = feature.StatusCodeReady; return nil }
	lc.SetRepoPublishedFn = func(featureID, repoName, prURL string) error {
		f.RepoStates[repoName].PRURL = prURL
		return nil
	}
	lc.TryCompletePublishFn = func(id string) (bool, error) { f.Status = feature.StatusPublished; return true, nil }
	fs := newFeatureStore(f)

	publisher := mocks.NewMockPublisher()
	publisher.HasUncommittedChangesFn = func(worktreePath string) (bool, error) { return true, nil }
	committed := map[string]bool{}
	phaseCommitCalls := 0
	publisher.CommitAllFn = func(worktreePath, message string) error {
		if strings.HasPrefix(message, "Phase 2/2") {
			phaseCommitCalls++
			return nil
		}
		committed[worktreePath] = true
		assertCandidatesRemoved(worktreePath)
		return nil
	}
	publisher.DiffStatFn = func(string, string) (string, error) { return "", nil }
	publisher.CommitBodiesFn = func(string, string) (string, error) { return "", nil }
	publisher.PushFn = func(string, string) error { return nil }
	publisher.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		return "https://github.com/org/" + filepath.Base(repoPath) + "/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Publisher: publisher,
		CmdRunner: untrackedFinalReviewArtifactsRunner(t, candidates),
		PhaseRunner: newPublishDescriptionPhaseRunner(
			t,
			"TITLE: Final review complete\nBODY:\n## Summary\n\nVerified changes",
			false,
		),
	}, orchestrator.Hooks{})
	o.SetRunMultiRepoFinalReviewFn(func(
		ff *feature.Feature,
		_ ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		writeCandidates()
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	})

	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "awaiting_final_review"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}
	if phaseCommitCalls != 2 {
		t.Fatalf("phase CommitAll calls = %d, want 2", phaseCommitCalls)
	}
	for _, repo := range []string{repoA, repoB} {
		if !committed[repo] {
			t.Fatalf("publish CommitAll did not run for %s", repo)
		}
	}
}

// TestOrchestrator_FeatureFinalReview_3Repo_ChangesRequested_FixApproves drives
// the orchestrator through the unified FR cycle when the reviewer requests
// changes once and the fix agent edits across multiple repos before the
// re-review approves. The FR engine returns all_passed only after the fix
// landed; the orchestrator must still fire MarkCodeReady + per-repo publish.
func TestOrchestrator_FeatureFinalReview_3Repo_ChangesRequested_FixApproves(t *testing.T) {
	pub := true
	f := &feature.Feature{
		ID:           "feat-3repo-fr-fix",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: apiRepoName, Path: apiRepoWorkPath, Publishable: &pub},
			{Name: "web", Path: "/tmp/web", Publishable: &pub},
			{Name: "infra", Path: "/tmp/infra", Publishable: &pub},
		},
		RepoStates: map[string]*feature.RepoState{
			apiRepoName: {Touched: true},
			"web":       {Touched: true},
			"infra":     {Touched: true},
		},
		Pipeline: feature.PipelineLarge,
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { f.Status = feature.StatusReviewPassed; return nil }
	lc.MarkFinalReviewReadyFn = func(id string) error { f.Status = feature.StatusFinalReviewing; return nil }
	lc.MarkCodeReadyFn = func(id string) error { f.Status = feature.StatusCodeReady; return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) {
		// Realistic gate: only allow Published transition when feature
		// is actually in the publish-ready window.
		if f.Status != feature.StatusReviewPassed && f.Status != feature.StatusCodeReady {
			return false, nil
		}
		f.Status = feature.StatusPublished
		return true, nil
	}
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	// Track that the engine reported the fix-then-approve flow under the
	// hood. From the orchestrator's perspective the FR engine returns
	// all_passed once it converges; the iteration-level fix happened inside
	// RunFeatureFinalReviewLoop. The seam below simulates that convergence
	// and asserts the harness still wires every cross-cutting bit
	// (MarkCodeReady, per-repo publishes).
	var iterations int
	o.SetRunMultiRepoFinalReviewFn(func(
		ff *feature.Feature,
		_ ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		go func() {
			// Two iterations: first CHANGES_REQUESTED + multi-repo fix,
			// second APPROVED. The engine converges and reports
			// "all_passed" — the orchestrator does not see iteration N.
			iterations = 2
			_ = fs.Modify(ff.ID, func(target *feature.Feature) error {
				for _, r := range []string{apiRepoName, "web", "infra"} {
					if st := target.RepoStates[r]; st != nil {
						st.Touched = true
					}
				}
				return nil
			})
			ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		}()
		return ch, nil
	})

	publishedRepos := map[string]string{}
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		url := "https://github.com/org/" + repo + "/pull/1"
		publishedRepos[repo] = url
		return url, nil
	})

	if err := o.HandlePhaseCompletion("feat-3repo-fr-fix", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "awaiting_final_review"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if iterations != 2 {
		t.Errorf("expected FR to converge in 2 iterations (CHANGES_REQUESTED → fix → APPROVED), got %d", iterations)
	}
	assertLifecycleCall(t, lc, "MarkFinalReviewReady")
	if len(publishedRepos) != 3 {
		t.Errorf("publishRepoFn calls = %d, want 3", len(publishedRepos))
	}
	for _, name := range []string{apiRepoName, "web", "infra"} {
		st := f.RepoStates[name]
		if st == nil {
			t.Errorf("RepoImpl[%q] = nil after FR", name)
			continue
		}
		if !st.Touched {
			t.Errorf("RepoStates[%q] = %+v, want Touched=true after FR", name, st)
		}
	}
}

func TestOrchestrator_FeatureFinalReview_FixerRepoEditsDoNotTripReadOnlyGuard(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	unpub := false
	f := &feature.Feature{
		ID:           "feat-fr-fixer-edits",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		ActiveRun:    1,
		RunCount:     1,
		Pipeline:     feature.PipelineLarge,
		Repos: []feature.FeatureRepo{{
			Name:         "docs",
			Path:         repo,
			WorktreePath: repo,
			Publishable:  &unpub,
			BaseBranch:   mainBranch,
		}},
		RepoStates: map[string]*feature.RepoState{
			"docs": {Touched: true},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { f.Status = feature.StatusReviewPassed; return nil }
	lc.MarkFinalReviewReadyFn = func(id string) error { f.Status = feature.StatusFinalReviewing; return nil }
	lc.MarkCodeReadyFn = func(id string) error { f.Status = feature.StatusCodeReady; return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		PhaseRunner: &agent.PhaseRunner{StateDir: t.TempDir()},
		CmdRunner:   agent.NewExecCommandRunner(),
	}, orchestrator.Hooks{})

	o.SetRunMultiRepoFinalReviewFn(func(
		ff *feature.Feature,
		_ ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Test\n\nFixed during final review.\n"), 0o644); err != nil {
			t.Fatalf("write final review fix: %v", err)
		}
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	})

	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "awaiting_final_review"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}
	if f.Status != feature.StatusCodeReady {
		t.Fatalf("feature status = %v, want CodeReady", f.Status)
	}
	data, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if got := string(data); !strings.Contains(got, "Fixed during final review.") {
		t.Fatalf("final review fix was not preserved:\n%s", got)
	}
}

// TestOrchestrator_FeatureFinalReview_Interrupted_DoesNotMarkFailed reproduces
// the bug where pressing Stop during deferred Final Review surfaced as
// "handle phase completion: final review interrupted" with
// failure_type=infrastructure on the feature. The InterruptFeature path drives
// StatusInterrupted; the FR engine returning FinalStatus="interrupted" must
// short-circuit the trailing MarkCodeReady / publish work and must not
// surface as a failure to surfaceDispatchCompletionError.
func TestOrchestrator_FeatureFinalReview_Interrupted_DoesNotMarkFailed(t *testing.T) {
	pub := true
	f := &feature.Feature{
		ID:           "feat-fr-interrupt",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: "payments", Path: "/tmp/payments", Publishable: &pub},
		},
		RepoStates: map[string]*feature.RepoState{
			"payments": {Touched: true},
		},
		Pipeline: feature.PipelineLarge,
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { f.Status = feature.StatusReviewPassed; return nil }
	lc.MarkFinalReviewReadyFn = func(id string) error { f.Status = feature.StatusFinalReviewing; return nil }
	// Track unexpected calls — neither should fire on the interrupt path.
	markCodeReadyCalled := 0
	markFailedCalled := 0
	lc.MarkCodeReadyFn = func(id string) error { markCodeReadyCalled++; return nil }
	lc.MarkFailedFn = func(id, failureType, lastError string) error { markFailedCalled++; return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	// Stub the FR engine to return interrupted (matching what the agent layer
	// emits when the user presses Stop and the FR session terminates early).
	o.SetRunMultiRepoFinalReviewFn(func(
		ff *feature.Feature,
		_ ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		go func() {
			// Real-world race: the InterruptFeature path transitions the
			// feature to StatusInterrupted before the FR result lands.
			_ = fs.Modify(ff.ID, func(target *feature.Feature) error {
				target.Status = feature.StatusInterrupted
				return nil
			})
			ch <- &agent.OrchestratorResult{FinalStatus: finalStatusInterrupted}
		}()
		return ch, nil
	})

	// Track FeatureFailed events — none should fire.
	failedEvents := 0
	go func() {
		for ev := range o.Events() {
			if ev.Type == ports.FeatureFailed && ev.FeatureID == f.ID {
				failedEvents++
			}
		}
	}()

	if err := o.HandlePhaseCompletion("feat-fr-interrupt", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "awaiting_final_review"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v (the interrupted FR result must not surface as an error)", err)
	}

	if markFailedCalled != 0 {
		t.Errorf("MarkFailed called %d times, want 0 — interrupted FR must not transition the feature to Failed", markFailedCalled)
	}
	if markCodeReadyCalled != 0 {
		t.Errorf("MarkCodeReady called %d times, want 0 — interrupted FR must short-circuit the trailing publish path", markCodeReadyCalled)
	}
	if f.Status != feature.StatusInterrupted {
		t.Errorf("feature Status = %s, want Interrupted (InterruptFeature owns the transition)", f.Status)
	}
}

func TestOrchestrator_FeatureFinalReview_FailedProtocolViolationPreserved(t *testing.T) {
	pub := true
	f := &feature.Feature{
		ID:           "feat-fr-protocol",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: apiRepoName, Path: apiRepoWorkPath, Publishable: &pub},
		},
		RepoStates: map[string]*feature.RepoState{
			apiRepoName: {Touched: true},
		},
		Pipeline: feature.PipelineLarge,
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { f.Status = feature.StatusReviewPassed; return nil }
	lc.MarkFinalReviewReadyFn = func(id string) error {
		f.Status = feature.StatusFinalReviewing
		f.CurrentPhase = feature.PhaseFinalReview
		return nil
	}
	var markCodeReadyCalled int
	lc.MarkCodeReadyFn = func(id string) error {
		markCodeReadyCalled++
		f.Status = feature.StatusCodeReady
		f.CurrentPhase = feature.PhasePublish
		return nil
	}
	var gotFailureType string
	var gotLastError string
	lc.MarkFailedFn = func(id, failureType, lastError string) error {
		gotFailureType = failureType
		gotLastError = lastError
		f.Status = feature.StatusFailed
		return nil
	}
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})
	o.SetRunMultiRepoFinalReviewFn(func(
		ff *feature.Feature,
		_ ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{
			FinalStatus:  "failed",
			RepoStatuses: map[string]string{apiRepoName: agent.BoundedHelperStatusProtocolViolation},
			FailedRepos:  []string{apiRepoName},
			LastError:    "protocol violation: final_review_fixer @ /tmp/iter: verification-report.yaml is missing",
		}
		return ch, nil
	})

	err := o.HandlePhaseCompletion("feat-fr-protocol", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "awaiting_final_review"},
	})
	if err == nil {
		t.Fatal("HandlePhaseCompletion() error = nil, want final review failure")
	}
	if !strings.Contains(err.Error(), "final_review_fixer") {
		t.Fatalf("HandlePhaseCompletion() error = %v, want final_review_fixer context", err)
	}

	if gotFailureType != feature.FailureProtocolViolation {
		t.Fatalf("failure type = %q, want %q", gotFailureType, feature.FailureProtocolViolation)
	}
	if !strings.Contains(gotLastError, "final_review_fixer") {
		t.Fatalf("last error = %q, want final_review_fixer context", gotLastError)
	}
	if markCodeReadyCalled != 0 {
		t.Fatalf("MarkCodeReady called %d times, want 0 after final review failure", markCodeReadyCalled)
	}
	if f.Status != feature.StatusFailed {
		t.Fatalf("Status = %s, want Failed", f.Status)
	}
	if f.CurrentPhase != feature.PhaseFinalReview {
		t.Fatalf("CurrentPhase = %s, want FinalReview", f.CurrentPhase)
	}
}
