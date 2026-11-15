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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// requireInterruptedReason checks the park carries the loop's reason.
func requireInterruptedReason(t *testing.T, o *orchestrator.Orchestrator, want string) {
	t.Helper()
	for _, ev := range drainEvents(o) {
		if ev.Type == ports.FeatureInterrupted {
			if ev.Message != want {
				t.Fatalf("FeatureInterrupted message = %q, want %q", ev.Message, want)
			}
			return
		}
	}
	t.Fatal("no FeatureInterrupted event")
}

// A user who answers "Stop" at a Final Review handoff parks the feature the
// way a pause-stop does: Interrupted, no failure, and nothing advances to
// Publish.
func TestOrchestrator_FeatureFinalReview_StoppedAtHandoffParksInterrupted(t *testing.T) {
	pub := true
	f := &feature.Feature{
		ID:           "feat-fr-stopped",
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
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	o.SetRunMultiRepoFinalReviewFn(func(*feature.Feature) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "stopped", LastError: "The QA axis requested changes in 3 consecutive rounds."}
		return ch, nil
	})

	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "awaiting_final_review"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	stored := loadStoredFeature(t, fs, f.ID)
	if stored.Status != feature.StatusInterrupted {
		t.Fatalf("persisted status = %s, want Interrupted", stored.Status)
	}
	if stored.CurrentPhase != feature.PhaseFinalReview {
		t.Fatalf("CurrentPhase = %s, want final review so resume re-enters it", stored.CurrentPhase)
	}
	if stored.Run().Failure != nil {
		t.Fatalf("unexpected failure: %+v", stored.Run().Failure)
	}
	requireInterruptedReason(t, o, "The QA axis requested changes in 3 consecutive rounds.")
}

// A user who answers "Stop" at a per-phase review handoff parks the feature
// in its implement phase, so resuming re-enters the phase review.
func TestOrchestrator_PhaseImplement_StoppedAtHandoffParksInterrupted(t *testing.T) {
	f := &feature.Feature{
		ID:                  "feat-phase-stopped",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  2,
		Repos:               []feature.FeatureRepo{{Name: "payments", Path: "/tmp/payments"}},
		Pipeline:            feature.PipelineMoonshot,
	}
	fs, lc := newStoredFeatureFixture(t, f)
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "stopped", LastError: "The Craft axis requested changes in 3 consecutive rounds."},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	stored := loadStoredFeature(t, fs, f.ID)
	if stored.Status != feature.StatusInterrupted {
		t.Fatalf("persisted status = %s, want Interrupted", stored.Status)
	}
	if stored.CurrentPhase != feature.PhaseImplement || stored.CurrentRoadmapPhase != 1 {
		t.Fatalf("phase = %s/%d, want implement phase 1 so resume re-enters it", stored.CurrentPhase, stored.CurrentRoadmapPhase)
	}
	if stored.Run().Failure != nil {
		t.Fatalf("unexpected failure: %+v", stored.Run().Failure)
	}
	requireInterruptedReason(t, o, "The Craft axis requested changes in 3 consecutive rounds.")
}
