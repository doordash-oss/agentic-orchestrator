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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ---------------------------------------------------------------------------
// TestOrchestrator_StartFeature_FirstPhase
// ---------------------------------------------------------------------------

func TestOrchestrator_StartFeature_FirstPhase(t *testing.T) {
	// Feature with no repos + large pipeline → KB phase skips to Inquire.
	f := &feature.Feature{
		ID:       "feat-1",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineLarge,
		Repos:    nil, // no repos → KB skipped
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	var phaseStartedCalled bool
	var startedFeatureID string
	var startedPhase feature.Phase

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{
		OnFeatureStarted: func(id string) { startedFeatureID = id },
		OnPhaseStarted: func(id string, p feature.Phase) {
			phaseStartedCalled = true
			startedPhase = p
		},
	})

	if err := o.StartFeature("feat-1"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	if startedFeatureID != "feat-1" {
		t.Errorf("OnFeatureStarted hook got feature ID %q, want %q", startedFeatureID, "feat-1")
	}

	// KB was skipped (no repos), so Inquire should have started.
	assertLifecycleCall(t, lc, "StartInquire")
	refuteLifecycleCall(t, lc, "StartKnowledgeBase")

	if !phaseStartedCalled {
		t.Error("OnPhaseStarted was not called")
	}
	if startedPhase != feature.PhaseInquire {
		t.Errorf("OnPhaseStarted phase = %v, want PhaseInquire", startedPhase)
	}

	events := drainEvents(o)
	if !hasEventType(events, ports.FeatureStarted) {
		t.Error("expected FeatureStarted event")
	}
	if hasPhaseStarted(events, feature.PhaseInquire) == nil {
		t.Error("expected PhaseStarted event for PhaseInquire")
	}
	// KB was skipped, so no PhaseStarted event for KB.
	if hasPhaseStarted(events, feature.PhaseKnowledgeBase) != nil {
		t.Error("unexpected PhaseStarted event for PhaseKnowledgeBase (should have been skipped)")
	}
}

func TestOrchestrator_StartFeatureRunsQueuedSetupBeforeFirstPhase(t *testing.T) {
	now := time.Now()
	f := &feature.Feature{
		ID:       "feat-setup-start",
		Status:   feature.StatusSettingUpWorktrees,
		Pipeline: feature.PipelineMedium,
		Repos:    []feature.FeatureRepo{{Name: repoName, Branch: "feature/setup-start"}},
	}
	f.SetRun(&feature.Run{
		RunNumber: 1,
		Setup:     feature.NewActiveSetupState(f.Repos, nil, nil, now),
	})
	lc := lifecycleForFeature(f)
	lc.RunSetupFn = func(featureID string, opts ...feature.SetupRunnerOptions) error {
		f.Status = feature.StatusCreated
		f.Run().Setup.Status = feature.SetupStatusDone
		for _, opt := range opts {
			if opt.OnEvent != nil {
				opt.OnEvent(feature.SetupEvent{Kind: feature.SetupEventCompleted, FeatureID: featureID, RunNumber: 1, Attempt: 1})
			}
		}
		return nil
	}
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature() error = %v, want queued setup then phase start", err)
	}

	assertLifecycleCall(t, lc, "RunSetup")
	assertLifecycleCall(t, lc, "StartPlanning")
	events := drainEvents(o)
	if !hasEventType(events, ports.SetupCompleted) {
		t.Fatalf("events = %+v, want setup completion before phase start", events)
	}
	if hasPhaseStarted(events, feature.PhasePlan) == nil {
		t.Fatalf("events = %+v, want plan phase start after setup", events)
	}
}

func TestOrchestrator_StartFeaturePersistsSetupFailureWithoutPhaseStart(t *testing.T) {
	now := time.Now()
	f := &feature.Feature{
		ID:       "feat-setup-fail",
		Status:   feature.StatusSettingUpWorktrees,
		Pipeline: feature.PipelineMedium,
		Repos:    []feature.FeatureRepo{{Name: repoName, Branch: "feature/setup-fail"}},
	}
	f.SetRun(&feature.Run{
		RunNumber: 1,
		Setup:     feature.NewActiveSetupState(f.Repos, nil, nil, now),
	})
	lc := lifecycleForFeature(f)
	lc.RunSetupFn = func(featureID string, opts ...feature.SetupRunnerOptions) error {
		f.Status = feature.StatusFailed
		f.FailureType = feature.FailureWorktreeSetup
		f.LastError = "git worktree add failed"
		f.Run().Setup.Status = feature.SetupStatusFailed
		f.Run().Setup.LastError = f.LastError
		return errors.New(f.LastError)
	}
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	err := o.StartFeature(f.ID)
	if err == nil || !strings.Contains(err.Error(), "git worktree add failed") {
		t.Fatalf("StartFeature() error = %v, want setup failure", err)
	}

	assertLifecycleCall(t, lc, "RunSetup")
	refuteLifecycleCall(t, lc, "StartPlanning")
	if f.Status != feature.StatusFailed || f.FailureType != feature.FailureWorktreeSetup || !strings.Contains(f.LastError, "git worktree add failed") {
		t.Fatalf("feature status/failure/error = %s/%s/%q, want persisted setup failure", f.Status, f.FailureType, f.LastError)
	}
	if f.Run().Setup == nil || f.Run().Setup.Status != feature.SetupStatusFailed || !strings.Contains(f.Run().Setup.LastError, "git worktree add failed") {
		t.Fatalf("setup = %+v, want failed setup diagnostic", f.Run().Setup)
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_StartFeature_MediumPipeline
// ---------------------------------------------------------------------------

func TestOrchestrator_StartFeature_MediumPipeline(t *testing.T) {
	f := &feature.Feature{
		ID:       "feat-exp",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Repos:    nil,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	if err := o.StartFeature("feat-exp"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	// Medium pre-transition mutates feature via Store.Modify.
	if len(fs.ModifyCalls) == 0 {
		t.Error("expected Store.Modify call for Created→PlanReady pre-transition")
	}

	assertLifecycleCall(t, lc, "StartPlanning")

	events := drainEvents(o)
	if hasPhaseStarted(events, feature.PhasePlan) == nil {
		t.Error("expected PhaseStarted event for PhasePlan")
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_StartFeature_ResumeInterrupted
// ---------------------------------------------------------------------------

func TestOrchestrator_StartFeature_ResumeInterrupted(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-resume",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseImplement,
		Artifacts:    map[string]string{"plan": "/tmp/does-not-exist-plan.md"},
		Pipeline:     feature.PipelineLarge,
	}
	// Set up a plan that actually resolves — write to a real path.
	planPath := writeTempFile(t, "plan-resume.md", "plan content")
	f.Artifacts["plan"] = planPath
	// Per SchemaVersionCurrent = 3, the orchestrator hard-fails if the
	// per-phase execution-order.yaml is missing alongside the plan.
	f.Repos = []feature.FeatureRepo{{Name: "test-repo", Path: "/tmp/test-repo"}}
	writeExecOrderNextToPlan(t, planPath, f.Repos)

	lc := lifecycleForFeature(f)
	// startImplement → StartMultiRepoImplementation requires StatusImplementing.
	lc.StartImplementationFn = func(id string) error { f.Status = feature.StatusImplementing; return nil }
	lc.InitRepoImplFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})
	// No-op engine seam: the dispatch goroutine blocks on an empty channel.
	o.SetRunMultiRepoImplFn(noopRunMultiRepoImplFn())

	if err := o.StartFeature("feat-resume"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	assertLifecycleCall(t, lc, "StartImplementation")
	refuteLifecycleCall(t, lc, "StartKnowledgeBase")

	events := drainEvents(o)
	if hasPhaseStarted(events, feature.PhaseImplement) == nil {
		t.Error("expected PhaseStarted event for PhaseImplement")
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_StartFeature_Error
// ---------------------------------------------------------------------------

func TestOrchestrator_StartFeature_Error(t *testing.T) {
	wantErr := errors.New("disk full")
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = func(id string) (*feature.Feature, error) { return nil, wantErr }

	var hookCalled bool
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     mocks.NewMockFeatureStore(),
	}, orchestrator.Hooks{
		OnFeatureStarted: func(id string) { hookCalled = true },
	})

	err := o.StartFeature("feat-err")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want wrapped %v", err, wantErr)
	}
	if hookCalled {
		t.Error("OnFeatureStarted hook should NOT fire when Get fails")
	}

	events := drainEvents(o)
	if hasEventType(events, ports.FeatureStarted) {
		t.Error("no FeatureStarted event should be emitted on error")
	}
}
