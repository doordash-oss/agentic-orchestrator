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

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
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
	fs, lc := newStoredFeatureFixture(t, f)

	var phaseStartedCalled bool
	var startedFeatureID string
	var startedPhase feature.Phase

	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
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
	if stored := loadStoredFeature(t, fs, f.ID); stored.Status != feature.StatusInquiring || stored.CurrentPhase != feature.PhaseInquire {
		t.Errorf("stored state = %s/%s, want Inquiring/Inquire", stored.Status, stored.CurrentPhase)
	}

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
	f := &feature.Feature{
		ID:       "feat-setup-start",
		Status:   feature.StatusSettingUpWorktrees,
		Pipeline: feature.PipelineMedium,
		Repos:    []feature.FeatureRepo{{Name: repoName, Branch: "feature/setup-start"}},
	}
	f.SetRun(&feature.Run{
		RunNumber: 1,
		Setup:     feature.NewQueuedSetupState(f.Repos, nil, nil),
	})
	fs, lc := newStoredFeatureFixture(t, f)
	worktrees := mocks.NewMockWorktreeOps()
	worktreePath := filepath.Join(t.TempDir(), "worktree")
	worktrees.ExpectedPathFn = func(_, _ string) string { return worktreePath }
	worktrees.CreateFn = func(_, _, _, _ string) (string, error) { return worktreePath, nil }
	lc.Worktrees = worktrees
	o := orchestrator.New(orchestrator.Deps{
		Manager:   lc,
		Store:     fs,
		Worktrees: worktrees,
	}, orchestrator.Hooks{})

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature() error = %v, want queued setup then phase start", err)
	}

	if stored := loadStoredFeature(t, fs, f.ID); stored.Run().Setup.Status != feature.SetupStatusDone || stored.Status != feature.StatusPlanning {
		t.Fatalf("stored setup/status = %s/%s, want done/Planning", stored.Run().Setup.Status, stored.Status)
	}
	events := drainEvents(o)
	if !hasEventType(events, ports.SetupCompleted) {
		t.Fatalf("events = %+v, want setup completion before phase start", events)
	}
	if hasPhaseStarted(events, feature.PhasePlan) == nil {
		t.Fatalf("events = %+v, want plan phase start after setup", events)
	}
}

func TestOrchestrator_StartFeaturePersistsSetupFailureWithoutPhaseStart(t *testing.T) {
	f := &feature.Feature{
		ID:       "feat-setup-fail",
		Status:   feature.StatusSettingUpWorktrees,
		Pipeline: feature.PipelineMedium,
		Repos:    []feature.FeatureRepo{{Name: repoName, Branch: "feature/setup-fail"}},
	}
	f.SetRun(&feature.Run{
		RunNumber: 1,
		Setup:     feature.NewQueuedSetupState(f.Repos, nil, nil),
	})
	fs, lc := newStoredFeatureFixture(t, f)
	worktreeKey := "worktree:" + repoName
	worktrees := mocks.NewMockWorktreeOps()
	worktrees.CreateFn = func(_, _, _, _ string) (string, error) { return "", errors.New("git worktree add failed") }
	lc.Worktrees = worktrees
	o := orchestrator.New(orchestrator.Deps{
		Manager:   lc,
		Store:     fs,
		Worktrees: worktrees,
	}, orchestrator.Hooks{})

	err := o.StartFeature(f.ID)
	if err == nil || !strings.Contains(err.Error(), "git worktree add failed") {
		t.Fatalf("StartFeature() error = %v, want setup failure", err)
	}

	f = loadStoredFeature(t, fs, f.ID)
	setupRec := f.FailureRecord()
	if f.Status != feature.StatusFailed || setupRec == nil || setupRec.Code != errcat.WorktreeSetupFailed ||
		setupRec.Context == nil || setupRec.Context.SetupTask == nil || setupRec.Context.SetupTask.Key != worktreeKey {
		t.Fatalf("feature status/failure = %s/%+v, want persisted thin setup failure pointing at the task", f.Status, setupRec)
	}
	owner := f.FailedSetupTask()
	if owner == nil || owner.Error == nil || !strings.Contains(owner.Error.Diagnostics, "git worktree add failed") {
		t.Fatalf("owning task = %+v, want failed worktree task carrying the raw diagnostics", owner)
	}
	if f.Run().Setup == nil || f.Run().Setup.Status != feature.SetupStatusFailed {
		t.Fatalf("setup = %+v, want failed setup state", f.Run().Setup)
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
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
	}, orchestrator.Hooks{})

	if err := o.StartFeature("feat-exp"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	if stored := loadStoredFeature(t, fs, f.ID); stored.Status != feature.StatusPlanning {
		t.Errorf("stored status = %s, want Planning", stored.Status)
	}

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

	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
	}, orchestrator.Hooks{})
	// No-op engine seam: the dispatch goroutine blocks on an empty channel.
	o.SetRunMultiRepoImplFn(noopRunMultiRepoImplFn())

	if err := o.StartFeature("feat-resume"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	if stored := loadStoredFeature(t, fs, f.ID); stored.Status != feature.StatusImplementing || stored.CurrentPhase != feature.PhaseImplement {
		t.Errorf("stored state = %s/%s, want Implementing/Implement", stored.Status, stored.CurrentPhase)
	}

	events := drainEvents(o)
	if hasPhaseStarted(events, feature.PhaseImplement) == nil {
		t.Error("expected PhaseStarted event for PhaseImplement")
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_StartFeature_Error
// ---------------------------------------------------------------------------

func TestOrchestrator_StartFeature_Error(t *testing.T) {
	fs, lc := newStoredFeatureFixture(t)

	var hookCalled bool
	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
	}, orchestrator.Hooks{
		OnFeatureStarted: func(id string) { hookCalled = true },
	})

	err := o.StartFeature("feat-err")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error = %v, want missing feature", err)
	}
	if hookCalled {
		t.Error("OnFeatureStarted hook should NOT fire when Get fails")
	}

	events := drainEvents(o)
	if hasEventType(events, ports.FeatureStarted) {
		t.Error("no FeatureStarted event should be emitted on error")
	}
}
