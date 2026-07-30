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

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestFeatureResumedFiresHookAndTypedDomainEvent(t *testing.T) {
	input := ports.FeatureResumedData{
		FeatureID:   "feat-resumed",
		PhaseKey:    "phase-02/implement",
		ChildKey:    "craft",
		Iteration:   3,
		RunNumber:   4,
		ResumeCount: 2,
	}
	var hooked ports.FeatureResumedData
	o := New(Deps{}, Hooks{
		OnFeatureResumed: func(got ports.FeatureResumedData) {
			hooked = got
		},
	})

	o.featureResumed(input)

	if hooked != input {
		t.Errorf("hook input = %+v, want %+v", hooked, input)
	}
	select {
	case event := <-o.Events():
		if event.Type != ports.FeatureResumed ||
			event.FeatureID != input.FeatureID ||
			event.PhaseKey != input.PhaseKey ||
			event.ChildKey != input.ChildKey ||
			event.Iteration != input.Iteration ||
			event.RunNumber != input.RunNumber ||
			event.ResumeCount != input.ResumeCount {
			t.Errorf("domain event = %+v, want typed resume identity %+v", event, input)
		}
	default:
		t.Fatal("feature resume emitted no domain event")
	}
}
