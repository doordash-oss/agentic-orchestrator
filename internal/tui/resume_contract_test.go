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
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestAPIAppModelFailedPrimaryActionFollowsResumeCatalog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		resume           server.ActionDTO
		wantMutation     string
		wantActionHint   string
		wantResumeReason string
	}{
		{
			name:           "enabled_resume_is_primary",
			resume:         server.ActionDTO{ID: recoveryActionResume, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
			wantMutation:   mutationKindFeatureResume,
			wantActionHint: "[a] Resume",
		},
		{
			name: "disabled_resume_surfaces_server_reason_and_retries",
			resume: server.ActionDTO{
				ID:      recoveryActionResume,
				Enabled: false,
				Scope:   server.ActionScopeDTO{Type: testActionScopeFeature},
				DisabledReasons: []server.ActionDisabledReasonDTO{
					{Code: "model_changed", Message: "recorded model no longer matches configured model"},
				},
			},
			wantMutation:     mutationKindFeatureRetry,
			wantActionHint:   "[a] Retry",
			wantResumeReason: "recorded model no longer matches configured model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := server.FeatureSummary{
				ID:           testStatusFailed,
				Name:         testFeatureNameFailedWork,
				Slug:         testFeatureSlugFailedWork,
				Status:       testFeatureStatusFailed,
				CurrentPhase: testPhaseNameImplement,
				CreatedAt:    time.Now(),
			}
			client := &fakeTUIAPIClient{
				features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
				detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{
					Actions: []server.ActionDTO{
						tt.resume,
						{ID: actionIDRetry, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
					},
				})},
			}
			app := newTestAPIAppModel(t, client)

			view := stripANSI(app.View().Content)
			if !strings.Contains(view, tt.wantActionHint) {
				t.Fatalf("View() missing primary action hint %q:\n%s", tt.wantActionHint, view)
			}
			if tt.wantResumeReason != "" {
				action, ok := apiActionByID(app.featureDetails[testStatusFailed].Feature.Actions, recoveryActionResume)
				if !ok || len(action.DisabledReasons) == 0 || action.DisabledReasons[0].Message != tt.wantResumeReason {
					t.Fatalf("cached Resume disabled reason = %+v, want %q", action.DisabledReasons, tt.wantResumeReason)
				}
				for _, part := range []string{"recorded model no longer", "matches configured model"} {
					if !strings.Contains(view, part) {
						t.Fatalf("View() missing server resume disabled reason fragment %q:\n%s", part, view)
					}
				}
			}

			model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
			if cmd == nil {
				t.Fatal("Update(a) returned nil command, want catalog-selected mutation")
			}
			model, _ = model.(APIAppModel).Update(cmd())
			switch tt.wantMutation {
			case mutationKindFeatureResume:
				if got := client.resumeFeatureIDs; !slices.Equal(got, []string{testStatusFailed}) {
					t.Fatalf("ResumeFeature calls = %v, want [%s]", got, testStatusFailed)
				}
				if len(client.retryFeatureIDs) != 0 {
					t.Fatalf("RetryFeature calls = %v, want none", client.retryFeatureIDs)
				}
			case mutationKindFeatureRetry:
				if got := client.retryFeatureIDs; !slices.Equal(got, []string{testStatusFailed}) {
					t.Fatalf("RetryFeature calls = %v, want [%s]", got, testStatusFailed)
				}
				if len(client.resumeFeatureIDs) != 0 {
					t.Fatalf("ResumeFeature calls = %v, want none", client.resumeFeatureIDs)
				}
			}
		})
	}
}

func TestAPIAppModelRendersResumedIndicatorFromSummaryAndDetail(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           testStatusFailed,
		Name:         testFeatureNameFailedWork,
		Slug:         testFeatureSlugFailedWork,
		Status:       testFeatureStatusFailed,
		CurrentPhase: testPhaseNameImplement,
		CreatedAt:    time.Now(),
		Resumed:      true,
		ResumeCount:  3,
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{
			Resumed:     true,
			ResumeCount: 3,
		})},
	}
	app := newTestAPIAppModel(t, client)

	view := stripANSI(app.View().Content)
	if got := strings.Count(view, "Resumed ×3"); got < 2 {
		t.Fatalf("View() resumed indicator count = %d, want summary and detail indicators:\n%s", got, view)
	}
}

func TestAPIAppModelFailedResumePrimaryKeepsRetryReachable(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           testStatusFailed,
		Name:         testFeatureNameFailedWork,
		Slug:         testFeatureSlugFailedWork,
		Status:       testFeatureStatusFailed,
		CurrentPhase: testPhaseNameImplement,
		CreatedAt:    time.Now(),
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{
			Failure: &server.FailureDTO{Type: "other", Message: "failed"},
			Actions: []server.ActionDTO{
				{ID: recoveryActionResume, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
				{ID: actionIDRetry, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
			},
		})},
	}
	app := newTestAPIAppModel(t, client)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 108, Height: 52})
	app = resized.(APIAppModel)

	view := stripANSI(app.View().Content)
	normalizedView := strings.Join(strings.Fields(strings.ReplaceAll(view, "│", " ")), " ")
	if !strings.Contains(normalizedView, "[r] Retry") ||
		!strings.Contains(normalizedView, "press [r] to retry") ||
		!strings.Contains(normalizedView, "Press [r] to retry the failed phase or [ctrl+r] to rewind.") ||
		strings.Contains(normalizedView, "[r] Restart") ||
		strings.Contains(normalizedView, "press [r] to restart") ||
		strings.Contains(normalizedView, "Press [r] to restart") {
		t.Fatalf("View() does not use Retry consistently:\n%s", view)
	}
	app.focusPanel = 1
	helpModel, _ := app.transitionToAPIHelpOverlay()
	helpView := stripANSI(helpModel.(APIAppModel).View().Content)
	if !strings.Contains(helpView, "Retry failed phase") || strings.Contains(helpView, "Restart phase") {
		t.Fatalf("help does not use Retry consistently:\n%s", helpView)
	}
	model, cmd := app.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd == nil {
		t.Fatal("Update(r) returned nil command, want Retry mutation")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	if got := client.retryFeatureIDs; !slices.Equal(got, []string{testStatusFailed}) {
		t.Fatalf("RetryFeature calls = %v, want [%s]", got, testStatusFailed)
	}
	if len(client.restartFeatureIDs) != 0 {
		t.Fatalf("RestartFeature calls = %v, want none", client.restartFeatureIDs)
	}
}

func TestAPIAppModelResumeAllUsesOnlyCatalogEnabledResume(t *testing.T) {
	t.Parallel()

	summaries := []server.FeatureSummary{
		{ID: "interrupted", Name: "Interrupted", Slug: "interrupted", Status: testFeatureStatusInterrupted, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		{ID: "failed-enabled", Name: "Failed enabled", Slug: "failed-enabled", Status: testFeatureStatusFailed, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now().Add(-time.Minute)},
		{ID: "failed-disabled", Name: "Failed disabled", Slug: "failed-disabled", Status: testFeatureStatusFailed, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now().Add(-2 * time.Minute)},
	}
	details := make(map[string]server.FeatureDetailResponse, len(summaries))
	for _, summary := range summaries {
		enabled := summary.ID != "failed-disabled"
		action := server.ActionDTO{
			ID:      recoveryActionResume,
			Enabled: enabled,
			Scope:   server.ActionScopeDTO{Type: testActionScopeFeature},
		}
		if !enabled {
			action.DisabledReasons = []server.ActionDisabledReasonDTO{{Code: "session_rejected", Message: "session previously rejected"}}
		}
		details[summary.ID] = server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{
			Actions: []server.ActionDTO{
				action,
				{ID: actionIDRetry, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
			},
		})}
	}
	client := &fakeTUIAPIClient{
		features:    server.FeatureListResponse{Features: summaries},
		detail:      details[summaries[0].ID],
		detailsByID: details,
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	if cmd == nil {
		t.Fatal("Update(Shift+R) returned nil command, want missing action catalogs loaded")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	confirming := model.(APIAppModel)
	if !confirming.resumeAllConfirmActive {
		t.Fatal("resume-all catalog load did not open confirmation")
	}

	model, cmd = confirming.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("Update(y) returned nil command, want resume-all mutation command")
	}
	model, _ = model.(APIAppModel).Update(cmd())

	if got, want := client.resumeFeatureIDs, []string{"interrupted", "failed-enabled"}; !slices.Equal(got, want) {
		t.Fatalf("ResumeFeature calls = %v, want %v", got, want)
	}
	if len(client.retryFeatureIDs) != 0 {
		t.Fatalf("RetryFeature calls = %v, want none", client.retryFeatureIDs)
	}
}
