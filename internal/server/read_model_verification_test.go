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

package server

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestFeatureDetailDTOCarriesVerificationItems(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:                 "feat-verif",
		Name:               "Verif",
		Slug:               "verif",
		Status:             feature.StatusImplementing,
		CurrentPhase:       feature.PhaseImplement,
		ActiveRun:          1,
		RunCount:           1,
		CurrentPhaseStatus: "verifying",
		VerificationItems: []feature.VerificationItemStatus{
			{Name: "Unit tests", State: "passed"},
			{Name: "Build", State: "running"},
		},
	}
	h := &apiHandler{}

	detail := h.featureDetailDTO(f)

	if len(detail.VerificationItems) != 2 {
		t.Fatalf("VerificationItems = %+v, want 2 ordered entries", detail.VerificationItems)
	}
	if detail.VerificationItems[0].Name != "Unit tests" || detail.VerificationItems[0].State != "passed" {
		t.Errorf("VerificationItems[0] = %+v, want {Unit tests passed}", detail.VerificationItems[0])
	}
	if detail.VerificationItems[1].Name != "Build" || detail.VerificationItems[1].State != "running" {
		t.Errorf("VerificationItems[1] = %+v, want {Build running}", detail.VerificationItems[1])
	}
}
