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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
)

// TestRestartPhase_NeedUserInputGate_AbandonsGate locks in that Restart on an
// open need-user-input request abandons it: the gate pointer and pending
// attention are cleared, the feature lands on Interrupted (a startable status),
// and the phase is re-dispatched. Before the arm existed, Restart fell through
// to the NeedUserInput → ImplementReady transition, which is invalid.
func TestRestartPhase_NeedUserInputGate_AbandonsGate(t *testing.T) {
	f := &feature.Feature{
		ID:                       "feat-restart-nui",
		Status:                   feature.StatusNeedUserInput,
		CurrentPhase:             feature.PhaseImplement,
		Pipeline:                 feature.PipelineLarge,
		PendingNeedUserInputPath: "/tmp/feat-restart-nui/need-user-input.yaml",
		HelpQueue:                []feature.HelpRequest{{Question: "stale", Pending: true}},
	}
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lifecycleForFeature(f), Store: fs}, orchestrator.Hooks{})

	outcome, err := o.RestartPhase(f.ID, 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase() error = %v, want nil", err)
	}
	if outcome.Action != orchestrator.RestartDispatchPhase || outcome.Phase != feature.PhaseImplement {
		t.Errorf("outcome = %+v, want dispatch of PhaseImplement", outcome)
	}
	if f.Status != feature.StatusInterrupted {
		t.Errorf("Status = %v, want StatusInterrupted", f.Status)
	}
	if f.PendingNeedUserInputPath != "" {
		t.Errorf("PendingNeedUserInputPath = %q, want cleared", f.PendingNeedUserInputPath)
	}
	if f.HelpQueue[0].Pending {
		t.Error("pending attention should be cleared when the gate is abandoned")
	}
}

// TestStartFeature_OpenNeedUserInputGate_Refused pins the Start/Resume guard:
// NeedUserInput → Implementing is a legal transition, so without the guard the
// verb silently bypassed the gate and left the request pending but invisible.
func TestStartFeature_OpenNeedUserInputGate_Refused(t *testing.T) {
	f := &feature.Feature{
		ID:                       "feat-start-nui",
		Status:                   feature.StatusNeedUserInput,
		CurrentPhase:             feature.PhaseImplement,
		Pipeline:                 feature.PipelineLarge,
		CurrentIteration:         4,
		PendingNeedUserInputPath: "/tmp/feat-start-nui/need-user-input.yaml",
	}
	lc := lifecycleForFeature(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(f)}, orchestrator.Hooks{})

	err := o.StartFeature(f.ID)
	if !errors.Is(err, feature.ErrNeedUserInputGateOpen) {
		t.Fatalf("StartFeature() error = %v, want ErrNeedUserInputGateOpen", err)
	}
	if f.Status != feature.StatusNeedUserInput {
		t.Errorf("Status = %v, want StatusNeedUserInput (gate must stay closed)", f.Status)
	}
	if f.PendingNeedUserInputPath == "" {
		t.Error("PendingNeedUserInputPath should survive a refused start")
	}
	if f.CurrentIteration != 4 {
		t.Errorf("CurrentIteration = %d, want 4 (no iteration reset)", f.CurrentIteration)
	}
	refuteLifecycleCall(t, lc, "StartImplementation")
}

// TestStartFeature_FinalizingPhase_Refused covers the synchronous end-of-phase
// git boundary: the feature reads as StatusReviewPassed, which otherwise looks
// startable.
func TestStartFeature_FinalizingPhase_Refused(t *testing.T) {
	f := &feature.Feature{
		ID:                 "feat-start-finalizing",
		Status:             feature.StatusReviewPassed,
		CurrentPhase:       feature.PhaseImplement,
		CurrentPhaseStatus: feature.PhaseStatusFinalizing,
		Pipeline:           feature.PipelineLarge,
	}
	lc := lifecycleForFeature(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(f)}, orchestrator.Hooks{})

	if err := o.StartFeature(f.ID); !errors.Is(err, feature.ErrPhaseFinalizing) {
		t.Fatalf("StartFeature() error = %v, want ErrPhaseFinalizing", err)
	}
	if f.Status != feature.StatusReviewPassed {
		t.Errorf("Status = %v, want StatusReviewPassed", f.Status)
	}
	refuteLifecycleCall(t, lc, "StartImplementation")
}

// TestInterruptAllRunning_ClearsStaleFinalizingMarker covers a crash inside the
// git boundary: StatusReviewPassed is not running, so the interrupt arm never
// visits the feature and the marker would keep Start refused forever.
func TestInterruptAllRunning_ClearsStaleFinalizingMarker(t *testing.T) {
	f := &feature.Feature{
		ID:                 "feat-stale-finalizing",
		Status:             feature.StatusReviewPassed,
		CurrentPhase:       feature.PhaseImplement,
		CurrentPhaseStatus: feature.PhaseStatusFinalizing,
		Pipeline:           feature.PipelineLarge,
	}
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lifecycleForFeature(f),
		Store:     newFeatureStore(f),
	}, orchestrator.Hooks{})

	if err := o.InterruptAllRunning(); err != nil {
		t.Fatalf("InterruptAllRunning() error = %v", err)
	}
	if f.IsFinalizingPhase() {
		t.Errorf("CurrentPhaseStatus = %q, want stale finalizing marker cleared", f.CurrentPhaseStatus)
	}
	if f.Status != feature.StatusReviewPassed {
		t.Errorf("Status = %v, want StatusReviewPassed (sweep must not transition it)", f.Status)
	}
}
