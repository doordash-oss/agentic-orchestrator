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

package orchestrator

import (
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestEmitEventCapturesLifecycleFeatureSnapshot(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	if err := store.Save(&feature.Feature{
		SchemaVersion:       feature.SchemaVersionCurrent,
		ID:                  "F-1",
		Name:                "Snapshot",
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  2,
		PhaseTimings:        map[string]time.Duration{"phase-1-impl": time.Minute},
		PhaseCosts:          map[string]float64{"phase-1-impl": 1.25},
	}); err != nil {
		t.Fatalf("seed feature: %v", err)
	}
	orchestrator := New(Deps{Store: store}, Hooks{})

	orchestrator.emitEvent(ports.Event{
		Type: ports.PhaseCompleted, FeatureID: "F-1", Phase: feature.PhaseImplement,
	})
	if err := store.Modify("F-1", func(f *feature.Feature) error {
		f.CurrentRoadmapPhase = 2
		f.CurrentPhase = feature.PhasePlan
		return nil
	}); err != nil {
		t.Fatalf("advance stored feature: %v", err)
	}

	event := <-orchestrator.Events()
	if event.Feature == nil {
		t.Fatal("lifecycle event has no emission-time feature snapshot")
	}
	if got := event.Feature.CurrentRoadmapPhase; got != 1 {
		t.Fatalf("snapshot roadmap phase = %d; want 1", got)
	}
	if got := event.Feature.PhaseTimings["phase-1-impl"]; got != time.Minute {
		t.Fatalf("snapshot phase timing = %s; want 1m", got)
	}
}
