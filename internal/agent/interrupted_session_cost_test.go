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
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

type runNumberTestSession struct {
	*utilityTestSession
	runNumber int
}

func (s *runNumberTestSession) RunNumber() int { return s.runNumber }

func sealAndForkForTest(t *testing.T, store *feature.Store, id string) {
	t.Helper()
	_, err := store.SealAndForkRun(id,
		func(oldRun *feature.Run) error {
			now := time.Now()
			oldRun.SealedAt = &now
			oldRun.SealReason = feature.SealReasonRewind
			return nil
		},
		func(oldRun *feature.Run) (*feature.Run, error) {
			return &feature.Run{RunNumber: oldRun.RunNumber + 1, Committing: true, ActiveTimingKey: "design"}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("SealAndForkRun: %v", err)
	}
}

func seedInterruptFeature(t *testing.T, store *feature.Store, id string) *feature.Feature {
	t.Helper()
	f := &feature.Feature{
		ID:              id,
		Name:            id,
		Slug:            id,
		Status:          feature.StatusImplementing,
		CurrentPhase:    feature.PhaseImplement,
		ActiveTimingKey: "phase-1-impl",
		ActiveRun:       1,
		RunCount:        1,
		SchemaVersion:   feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestImplementLoop_InterruptedSessionRecordsCostInItsRun(t *testing.T) {
	tests := []struct {
		name   string
		rewind bool
	}{
		{name: "pause keeps the active run"},
		{name: "rewind lands in the sealed run", rewind: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			featureID := "interrupt-cost-001"
			artifactDir, stateDir, workDir, observeDir := crashResumeTestDirs(t, featureID)
			cfg := newCrashResumeLoopConfig(t, artifactDir, stateDir, workDir, observeDir, featureID)
			store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
			f := seedInterruptFeature(t, store, featureID)
			f.Repos = cfg.Feature.Repos
			cfg.Feature = f
			cfg.FeatureStore = store
			cfg.BuildSession = func(BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
				return []string{"mock-agent"}, nil, &session.SessionOpts{}, nil
			}
			cfg.SessionStartFunc = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
				sess := &runNumberTestSession{utilityTestSession: newUtilityTestSession(), runNumber: opts[0].RunNumber}
				sess.AccumulatedUsageVal = llm.Usage{InputTokens: 100, OutputTokens: 10, CostUSD: 0.098}
				setFeatureStatus(t, store, featureID, feature.StatusInterrupted)
				if tt.rewind {
					sealAndForkForTest(t, store, featureID)
				}
				sess.StatusChVal <- agentStatusFailed
				return sess, nil
			}

			result, err := runImplementationLoop(cfg, nil)
			if err != nil {
				t.Fatalf("runImplementationLoop: %v", err)
			}
			if result.FinalStatus != "interrupted" {
				t.Fatalf("FinalStatus = %q, want interrupted", result.FinalStatus)
			}

			run1, err := store.LoadRun(featureID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(run1.SessionCosts) != 1 || run1.SessionCosts[0].PhaseKey != "phase-1-impl" {
				t.Fatalf("run 1 session costs = %+v, want one phase-1-impl row", run1.SessionCosts)
			}
			if got := run1.PhaseCosts["phase-1-impl"]; got != 0.098 {
				t.Errorf("run 1 PhaseCosts[phase-1-impl] = %v, want 0.098", got)
			}
			if tt.rewind {
				run2, err := store.LoadRun(featureID, 2)
				if err != nil {
					t.Fatal(err)
				}
				if len(run2.SessionCosts) != 0 {
					t.Errorf("run 2 session costs = %+v, want none", run2.SessionCosts)
				}
			}

			ended := filterEventsByType(readObserveEvents(t, observeDir, featureID), "session.ended")
			if len(ended) != 1 || ended[0].Status != "interrupted" {
				t.Fatalf("session.ended = %+v, want one interrupted event", ended)
			}
		})
	}
}

func TestAccumulateSessionCostToFeature_TargetsSessionRun(t *testing.T) {
	tests := []struct {
		name       string
		runNumber  int
		rewind     bool
		wantRun    int
		wantKey    string
		wantNoneIn int
	}{
		{name: "unset run uses the active run", runNumber: 0, wantRun: 1, wantKey: "phase-1-impl"},
		{name: "active run records normally", runNumber: 1, wantRun: 1, wantKey: "phase-1-impl"},
		{name: "unset run after rewind uses the new run", runNumber: 0, rewind: true, wantRun: 2, wantKey: "design", wantNoneIn: 1},
		{name: "sealed run keeps its session", runNumber: 1, rewind: true, wantRun: 1, wantKey: "phase-1-impl", wantNoneIn: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := feature.NewStore(t.TempDir())
			f := seedInterruptFeature(t, store, "run-target")
			if tt.rewind {
				sealAndForkForTest(t, store, f.ID)
			}
			err := accumulateSessionCostToFeature(store, f.ID, "implement", sessionCost{TotalCostUSD: 0.5}, sessionCostMetadata{
				SessionID: "s1",
				RunNumber: tt.runNumber,
			})
			if err != nil {
				t.Fatal(err)
			}
			run, err := store.LoadRun(f.ID, tt.wantRun)
			if err != nil {
				t.Fatal(err)
			}
			if len(run.SessionCosts) != 1 || run.SessionCosts[0].PhaseKey != tt.wantKey || run.PhaseCosts[tt.wantKey] != 0.5 {
				t.Fatalf("run %d costs = %+v / %+v, want one %s row of 0.5", tt.wantRun, run.SessionCosts, run.PhaseCosts, tt.wantKey)
			}
			if tt.wantNoneIn > 0 {
				other, err := store.LoadRun(f.ID, tt.wantNoneIn)
				if err != nil {
					t.Fatal(err)
				}
				if len(other.SessionCosts) != 0 {
					t.Errorf("run %d session costs = %+v, want none", tt.wantNoneIn, other.SessionCosts)
				}
			}
		})
	}
}
