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
	fs, lc := newStoredFeatureFixture(t, f)
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.StartFeature(f.ID); !errors.Is(err, feature.ErrPhaseFinalizing) {
		t.Fatalf("StartFeature() error = %v, want ErrPhaseFinalizing", err)
	}
	f = loadStoredFeature(t, fs, f.ID)
	if f.Status != feature.StatusReviewPassed {
		t.Errorf("Status = %v, want StatusReviewPassed", f.Status)
	}
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
	fs, lc := newStoredFeatureFixture(t, f)
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.InterruptAllRunning(); err != nil {
		t.Fatalf("InterruptAllRunning() error = %v", err)
	}
	f = loadStoredFeature(t, fs, f.ID)
	if f.IsFinalizingPhase() {
		t.Errorf("CurrentPhaseStatus = %q, want stale finalizing marker cleared", f.CurrentPhaseStatus)
	}
	if f.Status != feature.StatusReviewPassed {
		t.Errorf("Status = %v, want StatusReviewPassed (sweep must not transition it)", f.Status)
	}
}
