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

package tui

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestAttachHasNoFallbackTabDuringVerification(t *testing.T) {
	t.Parallel()
	const featureID = "feat-verifying"
	app := APIAppModel{
		featureDetails: map[string]server.FeatureDetailResponse{},
	}
	app.featureList = server.FeatureListResponse{Features: []server.FeatureSummary{
		{ID: featureID, Name: "Verif", Slug: "verif", Status: "implementing"},
	}}
	app.featureDetails[featureID] = server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
		ID:     featureID,
		Status: "implementing",
		ActiveRunDetail: &server.RunSummaryDTO{
			RunNumber:   1,
			PhaseStatus: "verifying",
		},
		VerificationItems: []server.VerificationItem{{Name: "Unit tests", State: "running"}},
	}}
	app.storeLivePreview(featureID, server.LivePreviewResponse{
		Feature: server.FeatureSummary{ID: featureID, Name: "Verif", Slug: "verif", Status: "implementing"},
		Session: &server.SessionSummaryDTO{ID: "old-impl", FeatureID: featureID, Phase: "Implement", Status: "done"},
		Transcript: []server.TranscriptMessageDTO{
			{Index: 1, Role: "assistant", Type: "text", Text: "stale session output"},
		},
	})

	if tabs := app.apiAttachTabsForFeature(featureID); len(tabs) != 0 {
		t.Fatalf("apiAttachTabsForFeature() = %d tabs, want none while the harness verifies (no session is running)", len(tabs))
	}
}

func TestApplyAPIFeatureDetailCopiesVerificationItems(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{ID: "feat-verif", Status: feature.StatusImplementing}
	detail := server.FeatureDetailDTO{
		VerificationItems: []server.VerificationItem{
			{Name: "Unit tests", State: "passed"},
			{Name: "Build", State: "running"},
		},
	}

	applyAPIFeatureDetail(f, detail)

	want := []feature.VerificationItemStatus{
		{Name: "Unit tests", State: "passed"},
		{Name: "Build", State: "running"},
	}
	if len(f.VerificationItems) != len(want) {
		t.Fatalf("VerificationItems = %+v, want %+v", f.VerificationItems, want)
	}
	for i := range want {
		if f.VerificationItems[i] != want[i] {
			t.Errorf("VerificationItems[%d] = %+v, want %+v", i, f.VerificationItems[i], want[i])
		}
	}
}
