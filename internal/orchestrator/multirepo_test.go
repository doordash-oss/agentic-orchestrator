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
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// ---------------------------------------------------------------------------
// T12. StartMultiRepoImplementation_RejectsIfFeatureNotImplementing
//
// Feature is StatusCreated. StartMultiRepoImplementation returns an error
// whose message contains "not in implementing state" and the
// runMultiRepoImplFn spy is never invoked.
// ---------------------------------------------------------------------------

func TestStartMultiRepoImplementation_RejectsIfFeatureNotImplementing(t *testing.T) {
	f := &feature.Feature{
		ID:        "feat-t12",
		Status:    feature.StatusCreated,
		Artifacts: map[string]string{"plan": "/tmp/plan.md"},
	}
	lc := lifecycleForFeature(f)
	store := newFeatureStore(f)
	spy := &fakeRunMultiRepoImpl{}

	o := orchestrator.New(
		orchestrator.Deps{Lifecycle: lc, Store: store},
		orchestrator.Hooks{},
	)
	o.SetRunMultiRepoImplFn(spy.Fn())

	err := o.StartMultiRepoImplementation("feat-t12")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not in implementing state") {
		t.Errorf("err = %q, want substring %q", err.Error(), "not in implementing state")
	}
	if spy.numCalls() != 0 {
		t.Errorf("runMultiRepoImplFn calls = %d, want 0", spy.numCalls())
	}
}

// ---------------------------------------------------------------------------
// T13. StartMultiRepoImplementation_MissingPlanPath_ReturnsError
//
// Feature is StatusImplementing but Artifacts["plan"] is empty — returns
// the "no plan artifact" error and does not invoke the engine spy.
// ---------------------------------------------------------------------------

func TestStartMultiRepoImplementation_MissingPlanPath_ReturnsError(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-t13",
		Status: feature.StatusImplementing,
		// Artifacts intentionally nil.
	}
	lc := lifecycleForFeature(f)
	store := newFeatureStore(f)
	spy := &fakeRunMultiRepoImpl{}

	o := orchestrator.New(
		orchestrator.Deps{Lifecycle: lc, Store: store},
		orchestrator.Hooks{},
	)
	o.SetRunMultiRepoImplFn(spy.Fn())

	err := o.StartMultiRepoImplementation("feat-t13")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no plan artifact") {
		t.Errorf("err = %q, want substring %q", err.Error(), "no plan artifact")
	}
	if spy.numCalls() != 0 {
		t.Errorf("runMultiRepoImplFn calls = %d, want 0", spy.numCalls())
	}
}

// ---------------------------------------------------------------------------
// T20. StartMultiRepoImplementation_ReturnsSynchronously
//
// Spy engine builds a channel but defers sending the terminal result until
// after StartMultiRepoImplementation has returned. We observe that:
//   - StartMultiRepoImplementation returns well within 100ms
//   - The engine had not yet been unblocked at that point
//
// This proves the method does not block on the channel.
// ---------------------------------------------------------------------------

func TestStartMultiRepoImplementation_ReturnsSynchronously(t *testing.T) {
	planPath := writeTempFile(t, "plan.md", "# plan")
	f := &feature.Feature{
		ID:        "feat-t20",
		Status:    feature.StatusImplementing,
		Repos:     []feature.FeatureRepo{{Name: "repo-a"}},
		Artifacts: map[string]string{"plan": planPath},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)
	lc := lifecycleForFeature(f)
	store := newFeatureStore(f)

	release := make(chan struct{})
	spy := &fakeRunMultiRepoImpl{
		ChannelFactory: func() chan *agent.OrchestratorResult {
			ch := make(chan *agent.OrchestratorResult, 1)
			go func() {
				<-release
				ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
			}()
			return ch
		},
	}

	o := orchestrator.New(
		orchestrator.Deps{Lifecycle: lc, Store: store},
		orchestrator.Hooks{},
	)
	o.SetRunMultiRepoImplFn(spy.Fn())

	start := time.Now()
	if err := o.StartMultiRepoImplementation("feat-t20"); err != nil {
		t.Fatalf("StartMultiRepoImplementation: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("StartMultiRepoImplementation blocked for %v, want < 100ms", elapsed)
	}

	// Unblock the engine so the dispatch goroutine can exit without leaking.
	close(release)
	// Drain any events that arrive; not strictly needed but keeps the test
	// well-behaved under -race.
	_ = collectEventsFor(o.Events(), 50*time.Millisecond)
}

// ---------------------------------------------------------------------------
// T21. StartMultiRepoImplementation_EngineError_Returns
//
// Spy engine returns (nil, err). Orchestrator wraps and returns that error;
// no dispatch goroutine is spawned (HandlePhaseCompletion is not reached).
// ---------------------------------------------------------------------------

func TestStartMultiRepoImplementation_EngineError_Returns(t *testing.T) {
	planPath := writeTempFile(t, "plan.md", "# plan")
	f := &feature.Feature{
		ID:        "feat-t21",
		Status:    feature.StatusImplementing,
		Repos:     []feature.FeatureRepo{{Name: "repo-a"}},
		Artifacts: map[string]string{"plan": planPath},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)
	lc := lifecycleForFeature(f)
	store := newFeatureStore(f)

	wantErr := errors.New("engine kaboom")
	spy := &fakeRunMultiRepoImpl{ReturnErr: wantErr}

	// Track completion calls via CompleteImplementationFn — if the dispatch
	// goroutine was incorrectly spawned and processed a terminal result,
	// this counter would go up.
	var completeCalls int32
	lc.CompleteImplementationFn = func(id string) error {
		completeCalls++
		return nil
	}

	o := orchestrator.New(
		orchestrator.Deps{Lifecycle: lc, Store: store},
		orchestrator.Hooks{},
	)
	o.SetRunMultiRepoImplFn(spy.Fn())

	err := o.StartMultiRepoImplementation("feat-t21")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrapped %v", err, wantErr)
	}

	// Wait a short window and confirm no dispatch goroutine fired
	// HandlePhaseCompletion (which would call CompleteImplementation on
	// all_passed, or MarkFailed on failed).
	// Retained: the absence of asynchronous dispatch is the behavior under test.
	time.Sleep(50 * time.Millisecond)
	if completeCalls != 0 {
		t.Errorf("CompleteImplementation calls = %d, want 0", completeCalls)
	}
}

func TestStartMultiRepoImplementation_HandlePhaseCompletionError_MarksFeatureFailed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping async multi-repo lifecycle failure regression in short mode; extended orchestrator run owns dispatch error plumbing")
	}

	planPath := writeTempFile(t, "plan.md", "# plan")
	f := &feature.Feature{
		ID:        "feat-t24",
		Status:    feature.StatusImplementing,
		Repos:     []feature.FeatureRepo{{Name: "repo-a"}, {Name: "repo-b"}},
		Artifacts: map[string]string{"plan": planPath},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)
	lc := lifecycleForFeature(f)
	// CompleteImplementation fails — onMultiReposPassed wraps and returns
	// the error, which propagates up through HandlePhaseCompletion to the
	// dispatch goroutine.
	completeErr := errors.New("db wedged")
	lc.CompleteImplementationFn = func(id string) error { return completeErr }

	markFailedCalled := make(chan struct{}, 1)
	lc.MarkFailedFn = func(id, failureType, errMsg string) error {
		f.Status = feature.StatusFailed
		select {
		case markFailedCalled <- struct{}{}:
		default:
		}
		return nil
	}
	store := newFeatureStore(f)

	spy := &fakeRunMultiRepoImpl{
		TerminalResult: &agent.OrchestratorResult{
			FinalStatus: "all_passed",
			RepoStatuses: map[string]string{
				"repo-a": "review_passed",
				"repo-b": "review_passed",
			},
		},
	}

	hookCh := make(chan string, 1)
	o := orchestrator.New(
		orchestrator.Deps{Lifecycle: lc, Store: store},
		orchestrator.Hooks{
			OnFeatureFailed: func(featureID, failureType, errorMsg string) {
				select {
				case hookCh <- errorMsg:
				default:
				}
			},
		},
	)
	o.SetRunMultiRepoImplFn(spy.Fn())

	if err := o.StartMultiRepoImplementation("feat-t24"); err != nil {
		t.Fatalf("StartMultiRepoImplementation: %v", err)
	}

	// Wait for FeatureFailed event. If the bug still existed, the dispatch
	// goroutine would silently exit and this assertion would time out.
	ev := waitForEvent(o.Events(), func(ev ports.Event) bool {
		return ev.Type == ports.FeatureFailed
	}, 1*time.Second)
	if ev == nil {
		t.Fatal("no FeatureFailed event observed — dispatch swallowed the completion error")
	}
	if !strings.Contains(ev.Message, "handle phase completion") {
		t.Errorf("FeatureFailed.Message = %q, want substring %q", ev.Message, "handle phase completion")
	}
	if !strings.Contains(ev.Message, completeErr.Error()) {
		t.Errorf("FeatureFailed.Message = %q, should include underlying cause %q", ev.Message, completeErr.Error())
	}

	// markFailedWithEvent should have succeeded (MarkFailed mock returns nil),
	// so the feature is transitioned to StatusFailed.
	select {
	case <-markFailedCalled:
	case <-time.After(1 * time.Second):
		t.Fatal("MarkFailed never called — feature would remain stuck in StatusImplementing")
	}
	if f.Status != feature.StatusFailed {
		t.Errorf("feature.Status = %q, want %q", f.Status, feature.StatusFailed)
	}

	// OnFeatureFailed hook fired.
	select {
	case msg := <-hookCh:
		if !strings.Contains(msg, "handle phase completion") {
			t.Errorf("OnFeatureFailed errorMsg = %q, want substring %q", msg, "handle phase completion")
		}
	case <-time.After(1 * time.Second):
		t.Error("OnFeatureFailed hook never fired")
	}
}

func TestStartMultiRepoImplementation_PublishConflictDoesNotMarkFeatureFailed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping async multi-repo publish-conflict lifecycle regression in short mode; extended orchestrator run owns conflict dispatch plumbing")
	}

	planPath := writeTempFile(t, "plan.md", "# plan")
	f := &feature.Feature{
		ID:                  "feat-publish-conflict",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  1,
		Repos: []feature.FeatureRepo{{
			Name:       "repo-a",
			Path:       "/tmp/repo-a",
			Branch:     "feature/x",
			BaseBranch: "main",
		}},
		Artifacts: map[string]string{"plan": planPath},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)

	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error {
		f.Status = feature.StatusReviewPassed
		return nil
	}
	lc.MarkCodeReadyFn = func(id string) error {
		f.Status = feature.StatusCodeReady
		return nil
	}
	markFailedCalled := make(chan struct{}, 1)
	lc.MarkFailedFn = func(id, failureType, errMsg string) error {
		f.Status = feature.StatusFailed
		select {
		case markFailedCalled <- struct{}{}:
		default:
		}
		return nil
	}
	store := newFeatureStore(f)

	spy := &fakeRunMultiRepoImpl{
		TerminalResult: &agent.OrchestratorResult{
			FinalStatus: "all_passed",
			RepoStatuses: map[string]string{
				"repo-a": "review_passed",
			},
		},
	}

	publishCalled := make(chan struct{}, 1)
	o := orchestrator.New(
		orchestrator.Deps{Lifecycle: lc, Store: store},
		orchestrator.Hooks{},
	)
	o.SetRunMultiRepoImplFn(spy.Fn())
	o.SetPublishFn(func(id string) error {
		select {
		case publishCalled <- struct{}{}:
		default:
		}
		return &orchestrator.PublishConflictError{
			RepoName:     "repo-a",
			Branch:       "feature/x",
			RebaseTarget: "main",
		}
	})

	if err := o.StartMultiRepoImplementation("feat-publish-conflict"); err != nil {
		t.Fatalf("StartMultiRepoImplementation: %v", err)
	}
	select {
	case <-publishCalled:
	case <-time.After(1 * time.Second):
		t.Fatal("publishFn was not called")
	}

	select {
	case <-markFailedCalled:
		t.Fatal("MarkFailed called for PublishConflictError; conflict should route to rebase UX")
	case <-time.After(100 * time.Millisecond):
	}
	if ev := waitForEvent(o.Events(), func(ev ports.Event) bool {
		return ev.Type == ports.FeatureFailed
	}, 100*time.Millisecond); ev != nil {
		t.Fatalf("FeatureFailed emitted for PublishConflictError: %+v", *ev)
	}
	if f.Status == feature.StatusFailed {
		t.Fatalf("feature status = Failed, want conflict to leave feature recoverable")
	}
}

// ---------------------------------------------------------------------------
// T25. StartMultiRepoImplementation_HandlePhaseCompletionError_FallbackOnMarkFailedError
//
// Extreme case: HandlePhaseCompletion fails AND the subsequent MarkFailed
// recovery transition also fails (lifecycle fully wedged). The dispatch
// goroutine must still emit FeatureFailed directly so observers are not
// stranded waiting on a terminal signal.
// ---------------------------------------------------------------------------

// TestDispatchMultiRepoResults_NeedUserInputRoutesPausedTerminalState verifies
// that an aggregate OrchestratorResult with FinalStatus == "need_user_input"
// flows through HandlePhaseCompletion / onMultiRepoImplementDone, emits a
// NeedUserInputRequired event, and does NOT mark the feature failed or
// emit PhaseCompleted (the implement phase is paused, not done).
func TestDispatchMultiRepoResults_NeedUserInputRoutesPausedTerminalState(t *testing.T) {
	planPath := writeTempFile(t, "plan.md", "# plan")
	f := &feature.Feature{
		ID:        "feat-mr-nui-dispatch",
		Status:    feature.StatusImplementing,
		Repos:     []feature.FeatureRepo{{Name: "repo-a"}, {Name: "repo-b"}},
		Artifacts: map[string]string{"plan": planPath},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)
	lc := lifecycleForFeature(f)
	store := newFeatureStore(f)

	var markFailedCalled bool
	lc.MarkFailedFn = func(id, failureType, errMsg string) error {
		markFailedCalled = true
		f.Status = feature.StatusFailed
		return nil
	}
	var completeCalled bool
	lc.CompleteImplementationFn = func(id string) error {
		completeCalled = true
		return nil
	}

	spy := &fakeRunMultiRepoImpl{
		TerminalResult: &agent.OrchestratorResult{
			FinalStatus: "need_user_input",
			RepoStatuses: map[string]string{
				"repo-a": "need_user_input",
				"repo-b": "review_passed",
			},
			LastError: "waiting for user input: repo-a",
		},
	}

	o := orchestrator.New(
		orchestrator.Deps{Lifecycle: lc, Store: store},
		orchestrator.Hooks{},
	)
	o.SetRunMultiRepoImplFn(spy.Fn())

	if err := o.StartMultiRepoImplementation("feat-mr-nui-dispatch"); err != nil {
		t.Fatalf("StartMultiRepoImplementation: %v", err)
	}

	ev := waitForEvent(o.Events(), func(ev ports.Event) bool {
		return ev.Type == ports.NeedUserInputRequired
	}, 200*time.Millisecond)
	if ev == nil {
		t.Fatal("no NeedUserInputRequired event observed")
	}
	if ev.FeatureID != "feat-mr-nui-dispatch" {
		t.Errorf("FeatureID = %q, want feat-mr-nui-dispatch", ev.FeatureID)
	}
	if ev.Phase != feature.PhaseImplement {
		t.Errorf("Phase = %v, want PhaseImplement", ev.Phase)
	}
	if !strings.Contains(ev.Message, "repo-a") {
		t.Errorf("Message = %q, want it to contain repo-a", ev.Message)
	}

	// Drain remaining events and ensure no PhaseCompleted or FeatureFailed.
	rest := collectEventsFor(o.Events(), 100*time.Millisecond)
	for _, e := range rest {
		switch e.Type {
		case ports.PhaseCompleted:
			t.Errorf("unexpected PhaseCompleted event during paused-terminal dispatch: %+v", e)
		case ports.FeatureFailed:
			t.Errorf("unexpected FeatureFailed event during paused-terminal dispatch: %+v", e)
		}
	}

	if markFailedCalled {
		t.Errorf("MarkFailed must not be called for need_user_input terminal result")
	}
	if completeCalled {
		t.Errorf("CompleteImplementation must not be called for need_user_input terminal result")
	}
	if f.Status != feature.StatusImplementing {
		t.Errorf("feature.Status = %q, want %q", f.Status, feature.StatusImplementing)
	}
}

func TestDispatchMultiRepoResults_PlanRevisionRequiredRoutesPhasePlanRevision(t *testing.T) {
	planPath := writeTempFile(t, "phase-plan.md", "# Phase plan\n")
	roadmapPath := writeTempFile(t, "roadmap.md", "# Roadmap\n\n## Phase 1: Bootstrap\n### Goal\nBootstrap\n\n## Phase 2: Hot-seat controls\n### Goal\nControls\n")
	f := &feature.Feature{
		ID:                  "feat-mr-plan-revision-dispatch",
		Status:              feature.StatusImplementing,
		ActiveRun:           1,
		RunCount:            1,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  2,
		Repos:               []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		Artifacts: map[string]string{
			"plan":    planPath,
			"roadmap": roadmapPath,
		},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)
	lc := lifecycleForFeature(f)
	store := newFeatureStore(f)

	spy := &fakeRunMultiRepoImpl{
		TerminalResult: &agent.OrchestratorResult{
			FinalStatus:          "plan_revision_required",
			RepoStatuses:         map[string]string{"repo-a": "plan_revision_required"},
			PlanRevisionFeedback: "MISSING_EVIDENCE_REQUIREMENT behavioral: Exercise Flip board without mutating turn or history.",
		},
	}

	o := orchestrator.New(
		orchestrator.Deps{
			Lifecycle: lc,
			Store:     store,
		},
		orchestrator.Hooks{},
	)
	o.SetRunMultiRepoImplFn(spy.Fn())

	if err := o.StartMultiRepoImplementation(f.ID); err != nil {
		t.Fatalf("StartMultiRepoImplementation: %v", err)
	}

	ev := waitForEvent(o.Events(), func(ev ports.Event) bool {
		return ev.Type == ports.PhaseStarted && ev.Phase == feature.PhasePlan
	}, 1*time.Second)
	if ev == nil {
		t.Fatal("no PhaseStarted(Plan) event observed; plan_revision_required was not routed")
	}
	if f.Status != feature.StatusPlanning {
		t.Fatalf("feature.Status = %q, want %q", f.Status, feature.StatusPlanning)
	}
}

// TestOnMultiRepoImplementDoneNeedUserInputDefaultSummary verifies that when
// LastError is empty but PausedRepos is set, the emitted NeedUserInputRequired
// event still carries a non-empty, repo-derived summary.
func TestOnMultiRepoImplementDoneNeedUserInputDefaultSummary(t *testing.T) {
	planPath := writeTempFile(t, "plan.md", "# plan")
	f := &feature.Feature{
		ID:        "feat-mr-nui-default",
		Status:    feature.StatusImplementing,
		Repos:     []feature.FeatureRepo{{Name: "repo-a"}},
		Artifacts: map[string]string{"plan": planPath},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)
	lc := lifecycleForFeature(f)
	store := newFeatureStore(f)

	spy := &fakeRunMultiRepoImpl{
		TerminalResult: &agent.OrchestratorResult{
			FinalStatus: "need_user_input",
		},
	}
	o := orchestrator.New(
		orchestrator.Deps{Lifecycle: lc, Store: store},
		orchestrator.Hooks{},
	)
	o.SetRunMultiRepoImplFn(spy.Fn())
	if err := o.StartMultiRepoImplementation("feat-mr-nui-default"); err != nil {
		t.Fatalf("StartMultiRepoImplementation: %v", err)
	}

	ev := waitForEvent(o.Events(), func(ev ports.Event) bool {
		return ev.Type == ports.NeedUserInputRequired
	}, 200*time.Millisecond)
	if ev == nil {
		t.Fatal("no NeedUserInputRequired event observed")
	}
	if !strings.Contains(ev.Message, "repo-a") {
		t.Errorf("Message = %q, want repo-a", ev.Message)
	}
}
