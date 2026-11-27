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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestImplementLoopInterruptEmitsPhaseInterrupted(t *testing.T) {
	tests := []struct {
		name      string
		status    feature.Status
		wantEvent string
		notEvent  string
	}{
		{name: "user interrupt", status: feature.StatusInterrupted, wantEvent: "phase.interrupted", notEvent: "phase.failed"},
		{name: "feature failed elsewhere", status: feature.StatusFailed, wantEvent: "phase.failed", notEvent: "phase.interrupted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observeDir := t.TempDir()
			featureID := "test-observe-interrupt-001"
			if err := os.MkdirAll(filepath.Join(observeDir, featureID), 0o755); err != nil {
				t.Fatal(err)
			}
			obs := observerFromModule(t, true, observeDir, "agentic")

			stateDir := t.TempDir()
			store := feature.NewStore(stateDir)
			repoDir := t.TempDir()
			f := &feature.Feature{
				ID:            featureID,
				Name:          "Test Interrupt Observe",
				Slug:          "test-interrupt-observe",
				Status:        tt.status,
				CurrentPhase:  feature.PhaseImplement,
				TraceID:       "trace-test-interrupt-001",
				Repos:         []feature.FeatureRepo{{Name: "agentic", Path: repoDir}},
				ExitCriteria:  "test criteria",
				ActiveRun:     1,
				RunCount:      1,
				SchemaVersion: feature.SchemaVersionCurrent,
			}
			if err := store.Save(f); err != nil {
				t.Fatal(err)
			}
			artifactDir := filepath.Join(stateDir, featureID, "phase-impl", "implement", "agentic")
			if err := os.MkdirAll(artifactDir, 0o755); err != nil {
				t.Fatal(err)
			}
			scriptDir := t.TempDir()
			agentScript := testutil.WriteScript(t, scriptDir, "agent.sh", testutil.JSONLInit+"\nexit 1\n")
			reviewScript := testutil.WriteScript(t, scriptDir, "review.sh", testutil.JSONLInit+"\nexit 1\n")
			sm := session.NewManager(make(chan any, 100))
			defer sm.Shutdown()

			result, err := runImplementationLoop(ImplementConfig{
				Feature:             f,
				FeatureStore:        store,
				WorkDir:             repoDir,
				PlanPath:            writePlanFile(t, artifactDir, "Some plan"),
				StateDir:            filepath.Join(stateDir, featureID),
				ArtifactDir:         artifactDir,
				RepoName:            "agentic",
				MaxIterations:       3,
				MaxConsecFails:      2,
				MaxConsecNoProgress: 3,
				BuildSession:        mockBuildSessionStillCrashingOnResume(agentScript, reviewScript),
				ExitCriteria:        "test criteria",
				Observer:            obs,
			}, sm)
			if err != nil {
				t.Fatalf("runImplementationLoop error: %v", err)
			}
			if result.FinalStatus != "interrupted" {
				t.Fatalf("FinalStatus = %q, want interrupted", result.FinalStatus)
			}

			events := readObserveEvents(t, observeDir, featureID)
			if got := filterEventsByType(events, tt.wantEvent); len(got) != 1 {
				t.Errorf("got %d %s events, want 1", len(got), tt.wantEvent)
			}
			if got := filterEventsByType(events, tt.notEvent); len(got) != 0 {
				t.Errorf("got %d %s events, want 0", len(got), tt.notEvent)
			}
		})
	}
}

func TestPlanningLoopInterruptEmitsPhaseInterrupted(t *testing.T) {
	tests := []struct {
		name      string
		status    feature.Status
		buildErr  error
		wantEvent string
		notEvent  string
	}{
		{name: "feature interrupted", status: feature.StatusInterrupted, wantEvent: "phase.interrupted", notEvent: "phase.failed"},
		{name: "cancelled session", status: feature.StatusPlanning, buildErr: context.Canceled, wantEvent: "phase.interrupted", notEvent: "phase.failed"},
		{name: "genuine error", status: feature.StatusPlanning, buildErr: errors.New("provider unavailable"), wantEvent: "phase.failed", notEvent: "phase.interrupted"},
		{name: "feature failed elsewhere", status: feature.StatusFailed, wantEvent: "phase.failed", notEvent: "phase.interrupted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			workDir := filepath.Join(tmpDir, "work")
			if err := os.MkdirAll(workDir, 0o755); err != nil {
				t.Fatal(err)
			}
			store := feature.NewStore(tmpDir)
			f := newTestPlanFeature(t, workDir)
			f.Status = tt.status
			if err := store.Save(f); err != nil {
				t.Fatal(err)
			}
			observeDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(observeDir, f.ID), 0o755); err != nil {
				t.Fatal(err)
			}
			obs := observerFromModule(t, true, observeDir, "agentic")

			_, _ = runPlanningLoop(planLoopConfig{
				Feature:      f,
				FeatureStore: store,
				StateDir:     tmpDir,
				WorkDir:      workDir,
				MaxAttempts:  1,
				Observer:     obs,
				BuildSession: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
					return nil, nil, nil, fmt.Errorf("build session: %w", tt.buildErr)
				},
			}, phasePlanKind("", RoadmapPhase{Number: 1, Name: "Plan", Type: "tdd-fill-in", Goal: "Plan the phase"}, nil), nil)

			events := readObserveEvents(t, observeDir, f.ID)
			if got := filterEventsByType(events, tt.wantEvent); len(got) != 1 {
				t.Errorf("got %d %s events, want 1", len(got), tt.wantEvent)
			}
			if got := filterEventsByType(events, tt.notEvent); len(got) != 0 {
				t.Errorf("got %d %s events, want 0", len(got), tt.notEvent)
			}
		})
	}
}
