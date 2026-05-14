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

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestRoadmapPlanningLoopEmitsEvents verifies that RunRoadmapPlanningLoop emits
// the full event sequence: phase.started -> sessions -> phase.completed.
func TestRoadmapPlanningLoopEmitsEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	featureID := "obs-roadmap-events"

	observeDir := t.TempDir()
	os.MkdirAll(filepath.Join(observeDir, featureID), 0755)
	obs := observe.New(true, observeDir, false, "", false, "agentic")

	stateDir := t.TempDir()
	workDir := filepath.Join(stateDir, "work")
	scriptsDir := filepath.Join(stateDir, "scripts")
	planDir := filepath.Join(stateDir, featureID, "runs", "run-001", "roadmap")
	for _, d := range []string{workDir, planDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	_ = os.WriteFile(filepath.Join(planDir, "plan.md"), []byte("# Roadmap\n## Phase 1: Observability Skeleton\nDo stuff"), 0o644)

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\nsleep 0.2\n"+testutil.TouchPhaseCompleteInLatestAttemptDir(planDir)+"\n"+testutil.JSONLSuccess+"\n")
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\nsleep 0.1\n"+testutil.WriteAnyValidatorApproved(stateDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := &feature.Feature{
		ID:           featureID,
		Name:         "Roadmap Events Test",
		Slug:         "roadmap-events-test",
		Description:  "Integration test for roadmap observability",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
		ActiveRun:    1,
		RunCount:     1,
		TraceID:      "trace-roadmap-events-001",
		Repos:        []feature.FeatureRepo{{Name: "test-repo", Path: workDir}},
		Models:       defaultTestPlanModels(),
		ExitCriteria: "Relevant tests pass",
	}
	_ = store.Save(f)

	cfg := PlanLoopConfig{
		Feature:                    f,
		FeatureStore:               store,
		StateDir:                   stateDir,
		WorkDir:                    workDir,
		DangerouslySkipPermissions: true,
		BuildSession:               mockBuildSession(planScript, criticScript),
		Observer:                   obs,
	}

	result, err := RunRoadmapPlanningLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunRoadmapPlanningLoop error: %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Fatalf("expected FinalStatus=approved, got %s", result.FinalStatus)
	}

	events := readObserveEvents(t, observeDir, featureID)

	// Verify phase events
	phaseStarted := filterEventsByType(events, "phase.started")
	if len(phaseStarted) != 1 {
		t.Fatalf("expected 1 phase.started, got %d", len(phaseStarted))
	}
	if phaseStarted[0].Phase != "plan" {
		t.Errorf("phase.started Phase = %q, want %q", phaseStarted[0].Phase, "plan")
	}

	phaseCompleted := filterEventsByType(events, "phase.completed")
	if len(phaseCompleted) != 1 {
		t.Fatalf("expected 1 phase.completed, got %d", len(phaseCompleted))
	}
	if phaseCompleted[0].Status != "completed" {
		t.Errorf("phase.completed Status = %q, want %q", phaseCompleted[0].Status, "completed")
	}

	// Verify session events: one plan session plus one per validator axis.
	// The roadmap validator set for the default risk level is {architecture,
	// scope}, so we expect 1 + 2 = 3 sessions.
	sessionStarted := filterEventsByType(events, "session.started")
	if len(sessionStarted) != 3 {
		t.Fatalf("expected 3 session.started events (1 plan + 2 validators), got %d", len(sessionStarted))
	}
	sessionEnded := filterEventsByType(events, "session.ended")
	if len(sessionEnded) != 3 {
		t.Fatalf("expected 3 session.ended events (1 plan + 2 validators), got %d", len(sessionEnded))
	}

	// Verify SpanContext hierarchy: sessions are children of phase
	if sessionStarted[0].ParentSpanID != phaseStarted[0].SpanID {
		t.Errorf("plan session ParentSpanID = %q, want phase SpanID %q",
			sessionStarted[0].ParentSpanID, phaseStarted[0].SpanID)
	}

	// TraceID consistency
	for _, evt := range events {
		if evt.TraceID != f.TraceID {
			t.Errorf("event %q has TraceID %q, want %q", evt.EventType, evt.TraceID, f.TraceID)
		}
	}
}

// TestPhasePlanningLoopEmitsEvents verifies that RunPhasePlanningLoop emits
// the full event sequence: phase.started -> sessions -> phase.completed.
func TestPhasePlanningLoopEmitsEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	featureID := "obs-phase-plan-events"

	observeDir := t.TempDir()
	os.MkdirAll(filepath.Join(observeDir, featureID), 0755)
	obs := observe.New(true, observeDir, false, "", false, "agentic")

	stateDir := t.TempDir()
	workDir := filepath.Join(stateDir, "work")
	scriptsDir := filepath.Join(stateDir, "scripts")
	// Phase plan artifact dir: <stateDir>/<featureID>/runs/run-001/phase-01/plan/
	planDir := filepath.Join(stateDir, featureID, "runs", "run-001", "phase-01", "plan")
	for _, d := range []string{workDir, planDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	_ = os.WriteFile(filepath.Join(planDir, "plan.md"), []byte("# Phase Plan\n## Overview\nDo stuff.\n\n## Tasks\n### Task 1: Do stuff\n\n#### What to build\nDo stuff.\n\n#### Acceptance criteria\n- [ ] Stuff is done.\n\n#### Blocked by\nNone - can start immediately.\n\n## Success Criteria\n### Automated Verification\n- [ ] Tests: `go test ./...`\n\n### Manual Verification\n- [ ] None required: test plan fixture.\n\n### Visual Evidence\n- [ ] None required: no rendered surface.\n\n### Behavioral Evidence\n- [ ] None required: automated tests are the artifact.\n"), 0o644)

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\nsleep 0.2\n"+
			testutil.WritePhasePlanSuccessArtifacts(planDir, validPhasePlanText())+"\n"+
			testutil.JSONLSuccess+"\n")
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\nsleep 0.1\n"+testutil.WriteAnyValidatorApproved(stateDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := &feature.Feature{
		ID:           featureID,
		Name:         "Phase Plan Events Test",
		Slug:         "phase-plan-events-test",
		Description:  "Integration test for phase plan observability",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
		ActiveRun:    1,
		RunCount:     1,
		TraceID:      "trace-phase-plan-events-001",
		Repos:        []feature.FeatureRepo{{Name: "test-repo", Path: workDir}},
		Models:       defaultTestPlanModels(),
		ExitCriteria: "Relevant tests pass",
	}
	_ = store.Save(f)

	cfg := PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               store,
			StateDir:                   stateDir,
			WorkDir:                    workDir,
			DangerouslySkipPermissions: true,
			BuildSession:               mockBuildSession(planScript, criticScript),
			Observer:                   obs,
		},
		Phase: RoadmapPhase{Number: 1, Name: "Test Phase", Type: "collapsed"},
	}

	result, err := RunPhasePlanningLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop error: %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Fatalf("expected FinalStatus=approved, got %s", result.FinalStatus)
	}

	events := readObserveEvents(t, observeDir, featureID)

	// Verify phase events
	phaseStarted := filterEventsByType(events, "phase.started")
	if len(phaseStarted) != 1 {
		t.Fatalf("expected 1 phase.started, got %d", len(phaseStarted))
	}
	if phaseStarted[0].Phase != "plan" {
		t.Errorf("phase.started Phase = %q, want %q", phaseStarted[0].Phase, "plan")
	}

	phaseCompleted := filterEventsByType(events, "phase.completed")
	if len(phaseCompleted) != 1 {
		t.Fatalf("expected 1 phase.completed, got %d", len(phaseCompleted))
	}
	if phaseCompleted[0].Status != "completed" {
		t.Errorf("phase.completed Status = %q, want %q", phaseCompleted[0].Status, "completed")
	}

	// Verify session events: one plan session plus one per validator axis.
	// The phase-plan validator set for the default risk level is
	// {structural, scope}, so we expect 1 + 2 = 3 sessions.
	sessionStarted := filterEventsByType(events, "session.started")
	if len(sessionStarted) != 3 {
		t.Fatalf("expected 3 session.started events (1 plan + 2 validators), got %d", len(sessionStarted))
	}
	sessionEnded := filterEventsByType(events, "session.ended")
	if len(sessionEnded) != 3 {
		t.Fatalf("expected 3 session.ended events (1 plan + 2 validators), got %d", len(sessionEnded))
	}

	// Verify SpanContext hierarchy: the plan session is a direct child of
	// the phase span. Validator sessions live one level deeper (their parent
	// is the per-validator span emitted by runValidatorSet, not the phase
	// span directly), so we don't assert on their parent here.
	if sessionStarted[0].ParentSpanID != phaseStarted[0].SpanID {
		t.Errorf("plan session ParentSpanID = %q, want phase SpanID %q",
			sessionStarted[0].ParentSpanID, phaseStarted[0].SpanID)
	}

	// TraceID consistency
	for _, evt := range events {
		if evt.TraceID != f.TraceID {
			t.Errorf("event %q has TraceID %q, want %q", evt.EventType, evt.TraceID, f.TraceID)
		}
	}
}

// TestMultiValidatorEmitsValidatorSessionParentage verifies that each validator's
// review session is a child of that validator's span (validator -> session hierarchy).

// TestFailedValidatorSessionEmitsErrorStatus verifies that when a validator/critic
// session fails (process exit 1), session.ended is emitted with a non-empty Error field.

// defaultTestPlanModels returns a ModelConfig suitable for plan loop tests.
func defaultTestPlanModels() config.ModelConfig {
	return config.ModelConfig{
		Planning: "planner",
		Review:   "reviewer",
	}
}
