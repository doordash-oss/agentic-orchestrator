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
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// TestPairedEditorPreservesConfiguredEffort pins that the paired Review
// editor (and every `e` entry path) seeds its snapshot from the selected
// record's current per-phase effort instead of zeroing it: an unrelated
// Behavior or Gates edit must not erase configured effort when saved
// through the paired mutation.
func TestPairedEditorPreservesConfiguredEffort(t *testing.T) {
	t.Parallel()

	configuredEffort := config.EffortConfig{
		Inquiry: "low", Research: "low", Planning: "xhigh",
		Implementation: "auto", Review: "medium", Utilities: "low", KBBuild: "low",
	}
	created := time.Now()
	parentSummary := server.FeatureSummary{
		ID: "parent-effort", Name: "Effort Parent", Slug: "effort-parent",
		Status: "published", CurrentPhase: "publish",
		CreatedAt: created, Repos: []string{"repo"},
	}
	parentDetail := apiTestFeatureDetail(parentSummary)
	childDetail := server.FeatureDetailDTO{
		ID: "child-effort", Name: "Effort Child", Slug: "effort-child",
		Status: "implementing", CurrentPhase: "implement",
		ParentID: parentSummary.ID, ParentKind: "refactor",
		CreatedAt: created.Add(time.Hour), Repos: []string{"repo"},
		Effort: configuredEffort,
	}
	parentDetail.ActiveChild = &server.RelationshipChild{ID: childDetail.ID, Name: childDetail.Name}

	for _, entry := range []struct {
		name      string
		featureID string
	}{
		{name: "from_parent_row", featureID: parentSummary.ID},
		{name: "from_child_row", featureID: childDetail.ID},
	} {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeTUIAPIClient{
				features: server.FeatureListResponse{Features: []server.FeatureSummary{parentSummary}},
				detail:   server.FeatureDetailResponse{Feature: parentDetail},
				featureConfig: server.FeatureConfigResponse{
					Current: server.FeatureConfig{
						Pipeline:    "moonshot",
						Effort:      configuredEffort,
						Inquireness: "high",
					},
					Defaults: server.FeatureConfig{Pipeline: "large"},
				},
			}
			app := newTestAPIAppModel(t, client)
			defer app.Close()
			app.featureDetails[childDetail.ID] = server.FeatureDetailResponse{Feature: childDetail}

			cmd := app.fetchFeatureConfigCmd(entry.featureID)
			if cmd == nil {
				t.Fatal("fetchFeatureConfigCmd returned nil")
			}
			model, _ := app.Update(cmd())
			m := model.(APIAppModel) //nolint:errcheck // Update preserves concrete type
			if m.configEditor == nil {
				t.Fatalf("editor did not open for %s; status = %q", entry.featureID, m.statusMessage)
			}
			if got := m.configEditor.editor.Snapshot().Effort; got != configuredEffort {
				t.Fatalf("editor snapshot effort = %+v, want configured %+v", got, configuredEffort)
			}

			m.saveFeatureConfigCmd(*m.configEditor)()
			if len(client.updateFeatureConfigRequests) != 1 {
				t.Fatalf("update requests = %d, want 1", len(client.updateFeatureConfigRequests))
			}
			sent := client.updateFeatureConfigRequests[0]
			if sent.Effort != configuredEffort {
				t.Fatalf("paired mutation effort = %+v, want configured %+v", sent.Effort, configuredEffort)
			}
		})
	}
}

// TestClosedChildLeavesActiveProjection pins Phase 8's fallback behavior:
// once a child's relationship carries a close outcome it must disappear
// from the active nested rows, and selecting it is no longer valid.
func TestClosedChildLeavesActiveProjection(t *testing.T) {
	t.Parallel()

	created := time.Now()
	closedAt := created.Add(2 * time.Hour)
	parentSummary := server.FeatureSummary{
		ID: "parent-closed", Name: "Closed Parent", Slug: "closed-parent",
		Status: "published", CurrentPhase: "publish",
		CreatedAt: created, Repos: []string{"repo"},
	}
	parentDetail := apiTestFeatureDetail(parentSummary)
	closedChild := server.FeatureDetailDTO{
		ID: "child-closed", Name: "Closed Child", Slug: "closed-child",
		Status: "interrupted", CurrentPhase: "implement",
		ParentID: parentSummary.ID, ParentKind: "refactor",
		CreatedAt: created.Add(time.Hour), Repos: []string{"repo"},
		CloseOutcome: "discarded", ClosedAt: &closedAt,
	}

	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{parentSummary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			parentSummary.ID: {Feature: parentDetail},
			closedChild.ID:   {Feature: closedChild},
		},
	}

	if children := app.apiChildFeatures(); len(children) != 0 {
		t.Fatalf("apiChildFeatures() projected %d children of a closed relationship, want none", len(children))
	}
	if app.apiHasChildFeature(closedChild.ID) {
		t.Fatal("closed child still treated as a valid active selection")
	}
	dash := app.apiDashboardModel()
	for _, item := range dash.visibleItems {
		if item.kind == listItemChildFeature {
			t.Fatalf("dashboard still renders a nested child row for the closed child: %+v", item.feature)
		}
	}
	rows := app.apiDashboardFeatures()
	for _, f := range rows {
		if f.ID == closedChild.ID {
			t.Fatalf("closed child still projected as a top-level row: %+v", f)
		}
	}
}

// TestDiscardSelectionFallsBackToParent pins that discarding the selected
// active child moves selection to its parent until Phase 9 supplies
// closed-history navigation.
func TestDiscardSelectionFallsBackToParent(t *testing.T) {
	t.Parallel()

	created := time.Now()
	parentSummary := server.FeatureSummary{
		ID: "parent-discard", Name: "Discard Parent", Slug: "discard-parent",
		Status: "published", CurrentPhase: "publish",
		CreatedAt: created, Repos: []string{"repo"},
	}
	childDetail := server.FeatureDetailDTO{
		ID: "child-discard", Name: "Discard Child", Slug: "discard-child",
		Status: "implementing", CurrentPhase: "implement",
		ParentID: parentSummary.ID, ParentKind: "refactor",
		CreatedAt: created.Add(time.Hour), Repos: []string{"repo"},
	}

	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{parentSummary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			parentSummary.ID: {Feature: apiTestFeatureDetail(parentSummary)},
			childDetail.ID:   {Feature: childDetail},
		},
	}
	app.selectedFeature = childDetail.ID

	model, _ := app.Update(apiMutationResultMsg{kind: mutationKindFeatureDiscard, featureID: childDetail.ID})
	m := model.(APIAppModel) //nolint:errcheck // Update preserves concrete type
	if m.selectedFeature != parentSummary.ID {
		t.Fatalf("after discarding the selected child, selectedFeature = %q, want parent %q",
			m.selectedFeature, parentSummary.ID)
	}
}

// nonDefaultParentDetail builds a parent detail carrying deliberately
// recognizable Review axes so tests can prove they survive the refactor
// wizard and the paired editor unchanged.
func nonDefaultParentDetail(t *testing.T, id, name string) server.FeatureDetailDTO {
	t.Helper()
	created := time.Now()
	summary := server.FeatureSummary{
		ID: id, Name: name, Slug: name,
		Status: "published", CurrentPhase: "publish",
		CreatedAt: created, Repos: []string{"repo"},
	}
	detail := apiTestFeatureDetail(summary)
	detail.Actions = []server.ActionDTO{{ID: actionIDRefactor, Enabled: true}}
	detail.Models = config.ModelConfig{
		Inquiry:        "claude/opus-4-7",
		Research:       "claude/opus-4-7",
		Planning:       "codex/gpt-5-codex",
		Implementation: "codex/gpt-5-codex",
		Review:         "claude/opus-4-7",
		Utilities:      "claude/opus-4-7",
		KBBuild:        "codex/gpt-5-codex",
	}
	detail.Effort = config.EffortConfig{
		Inquiry: "low", Research: "low", Planning: "xhigh",
		Implementation: "auto", Review: "medium", Utilities: "low", KBBuild: "low",
	}
	detail.Inquireness = feature.InquirenessHigh
	detail.RiskLevel = feature.RiskHigh
	detail.ExitCriteria = "parent exit criteria: every refactor retains context"
	detail.Checkpoints = server.Checkpoints{
		InquiryReview:   true,
		ResearchReview:  true,
		DesignReview:    false,
		RoadmapReview:   false,
		PhasePlanReview: true,
		ManualPublish:   true,
	}
	return detail
}

func seedCheckpointsFromDetail(d server.FeatureDetailDTO) config.Checkpoints {
	return config.Checkpoints{
		InquiryReview:   d.Checkpoints.InquiryReview,
		ResearchReview:  d.Checkpoints.ResearchReview,
		DesignReview:    d.Checkpoints.DesignReview,
		RoadmapReview:   d.Checkpoints.RoadmapReview,
		PhasePlanReview: d.Checkpoints.PhasePlanReview,
		ManualPublish:   d.Checkpoints.ManualPublish,
	}
}

// TestRefactorWizardSeedsParentReviewAxes pins that the refactor wizard
// initializes every Review axis (models, effort, inquiry behavior, risk,
// exit criteria, gates) from the parent's current configuration instead of
// silently falling back to workspace/pipeline defaults.
func TestRefactorWizardSeedsParentReviewAxes(t *testing.T) {
	t.Parallel()

	detail := nonDefaultParentDetail(t, "parent-seed", "Seed Parent")
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{{
			ID: detail.ID, Name: detail.Name, Slug: detail.Slug,
			Status: detail.Status, CurrentPhase: detail.CurrentPhase,
			CreatedAt: detail.CreatedAt, Repos: detail.Repos,
		}}},
		detail: server.FeatureDetailResponse{Feature: detail},
	}
	app := newTestAPIAppModel(t, client)
	defer app.Close()

	m := app.openRefactorWizard()
	if m.wizard == nil {
		t.Fatalf("openRefactorWizard() did not open the wizard; status = %q", m.statusMessage)
	}
	w := m.wizard
	if w.models != detail.Models {
		t.Errorf("wizard models = %+v, want parent models %+v", w.models, detail.Models)
	}
	if w.effort != detail.Effort {
		t.Errorf("wizard effort = %+v, want parent effort %+v", w.effort, detail.Effort)
	}
	if got := w.inquirenessOptions[w.inquirenessCursor]; got != string(detail.Inquireness) {
		t.Errorf("wizard inquireness = %q, want parent %q", got, detail.Inquireness)
	}
	if got := w.riskOptions[w.riskCursor]; got != string(detail.RiskLevel) {
		t.Errorf("wizard risk = %q, want parent %q", got, detail.RiskLevel)
	}
	if w.exitCriteria != detail.ExitCriteria {
		t.Errorf("wizard exit criteria = %q, want parent %q", w.exitCriteria, detail.ExitCriteria)
	}
	for i, want := range []bool{
		detail.Checkpoints.InquiryReview,
		detail.Checkpoints.ResearchReview,
		detail.Checkpoints.DesignReview,
		detail.Checkpoints.RoadmapReview,
		detail.Checkpoints.PhasePlanReview,
		detail.Checkpoints.ManualPublish,
	} {
		if w.checkpoints[i] != want {
			t.Errorf("wizard checkpoint %d = %v, want parent %v", i, w.checkpoints[i], want)
		}
	}
}

// TestRefactorWizardPipelineCursorPreservesSeededReviewAxes pins the
// child-pipeline independence invariant: moving the pipeline cursor (or
// advancing the Pipeline step) changes only the child's pipeline choice and
// must not overwrite the Review axes seeded from the parent.
func TestRefactorWizardPipelineCursorPreservesSeededReviewAxes(t *testing.T) {
	t.Parallel()

	detail := nonDefaultParentDetail(t, "parent-seed", "Seed Parent")
	seed := RefactorWizardSeed{
		ParentID:     detail.ID,
		ParentRepos:  detail.Repos,
		Models:       detail.Models,
		Effort:       detail.Effort,
		Inquireness:  detail.Inquireness,
		RiskLevel:    detail.RiskLevel,
		ExitCriteria: detail.ExitCriteria,
		Checkpoints:  seedCheckpointsFromDetail(detail),
	}
	cat := apiPhaseModelCatalog(server.ModelCatalogResponse{
		ProviderOrder: []string{"claude"},
		ProviderModels: map[string][]server.ModelDTO{
			"claude": {{ID: "claude/sonnet-4-6"}},
		},
	})
	w := NewRefactorWizardModel(
		detail.Repos, map[string]string{"repo": "/tmp/repo"}, nil,
		config.DefaultsConfig{}, "claude",
		cat.ProviderModels, cat.ProviderOrder, cat.PhaseDefaults, cat.PhaseProviderModels,
		nil, nil, seed,
	)

	// Drive: name briefly, then What → Pipeline. Move the pipeline cursor
	// down to moonshot, back up to large, then advance to Review.
	w.nameInput.SetValue("Refactor Child")
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	w, _ = w.Update(enter)                            // name → description focus
	w, _ = w.Update(enter)                            // What → Pipeline
	w, _ = w.Update(down)                             // cursor: large → moonshot
	w, _ = w.Update(down)                             // stays at bottom
	w, _ = w.Update(tea.KeyPressMsg{Code: tea.KeyUp}) // moonshot → large
	w, _ = w.Update(enter)                            // Pipeline → Review

	if w.step != wizardStepReview {
		t.Fatalf("wizard step = %v, want Review", w.step)
	}
	if w.models != seed.Models {
		t.Errorf("pipeline cursor overwrote models: %+v, want %+v", w.models, seed.Models)
	}
	if w.effort != seed.Effort {
		t.Errorf("pipeline cursor overwrote effort: %+v, want %+v", w.effort, seed.Effort)
	}
	if got := w.inquirenessOptions[w.inquirenessCursor]; got != string(seed.Inquireness) {
		t.Errorf("pipeline cursor overwrote inquireness: %q, want %q", got, seed.Inquireness)
	}
	if got := w.riskOptions[w.riskCursor]; got != string(seed.RiskLevel) {
		t.Errorf("pipeline cursor overwrote risk: %q, want %q", got, seed.RiskLevel)
	}
	if w.exitCriteria != seed.ExitCriteria {
		t.Errorf("pipeline cursor overwrote exit criteria: %q, want %q", w.exitCriteria, seed.ExitCriteria)
	}
	for i, want := range []bool{
		seed.Checkpoints.InquiryReview,
		seed.Checkpoints.ResearchReview,
		seed.Checkpoints.DesignReview,
		seed.Checkpoints.RoadmapReview,
		seed.Checkpoints.PhasePlanReview,
		seed.Checkpoints.ManualPublish,
	} {
		if w.checkpoints[i] != want {
			t.Errorf("pipeline cursor overwrote checkpoint %d: %v, want parent %v", i, w.checkpoints[i], want)
		}
	}
}

// TestRefreshSnapshotClosedChildFallsBackToParent pins the ordered
// relationship-refresh fallback: when the selected active child closes in
// the applied bundle, selection moves to its parent even though the child's
// summary lingers in the cached feature list (selection-time detail fetches
// upsert it there) and even when another top-level feature would sort first.
func TestRefreshSnapshotClosedChildFallsBackToParent(t *testing.T) {
	t.Parallel()

	created := time.Now()
	otherSummary := server.FeatureSummary{
		ID: "other-sorted", Name: "Other Sorted", Slug: "other-sorted",
		Status: "implementing", CurrentPhase: "implement",
		CreatedAt: created.Add(-2 * time.Hour),
	}
	parentSummary := server.FeatureSummary{
		ID: "parent-refresh", Name: "Refresh Parent", Slug: "refresh-parent",
		Status: "published", CurrentPhase: "publish",
		CreatedAt: created.Add(-time.Hour), Repos: []string{"repo"},
	}
	childDetail := server.FeatureDetailDTO{
		ID: "child-refresh", Name: "Refresh Child", Slug: "refresh-child",
		Status: "created", CurrentPhase: "plan",
		ParentID: parentSummary.ID, ParentKind: "refactor",
		CreatedAt: created, Repos: []string{"repo"},
	}
	childSummary := server.FeatureSummary{
		ID: childDetail.ID, Name: childDetail.Name, Slug: childDetail.Slug,
		Status: childDetail.Status, CurrentPhase: childDetail.CurrentPhase,
		CreatedAt: childDetail.CreatedAt, Repos: childDetail.Repos,
	}
	closedAt := created.Add(2 * time.Hour)
	closedChild := childDetail
	closedChild.CloseOutcome = "completed"
	closedChild.ClosedAt = &closedAt

	app := APIAppModel{
		// The feature list still carries the child's stale summary, as a
		// selection-time detail fetch would have upserted it.
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{otherSummary, parentSummary, childSummary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			parentSummary.ID: {Feature: apiTestFeatureDetail(parentSummary)},
			childDetail.ID:   {Feature: childDetail},
		},
		selectedFeature: childDetail.ID,
	}
	app.ApplyRefreshSnapshot(server.RefreshSnapshot{
		Relationship: &server.RelationshipRefreshBundle{
			Parent: server.FeatureDetailResponse{Feature: apiTestFeatureDetail(parentSummary)},
			Child:  &server.FeatureDetailResponse{Feature: closedChild},
		},
	})
	if app.selectedFeature != parentSummary.ID {
		t.Fatalf("after the relationship bundle closed the selected child, selectedFeature = %q, want parent %q (not first-sorted %q)",
			app.selectedFeature, parentSummary.ID, otherSummary.ID)
	}
	for _, item := range app.apiDashboardModel().visibleItems {
		if item.kind == listItemChildFeature {
			t.Fatalf("dashboard still renders a nested child row for the closed child: %+v", item.feature)
		}
	}
}

// TestRefactorLaunchSubmitsSeededGatesUnfiltered pins the launch request
// itself: when the child pipeline moves to Medium, the submitted Review
// gates must still equal the parent-seeded paired configuration. Filtering
// them through the Medium profile (which hides Inquiry/Research/Design)
// would silently clear parent review gates on both persisted records.
func TestRefactorLaunchSubmitsSeededGatesUnfiltered(t *testing.T) {
	t.Parallel()

	detail := nonDefaultParentDetail(t, "parent-gates", "Gates Parent")
	seed := RefactorWizardSeed{
		ParentID:     detail.ID,
		ParentRepos:  detail.Repos,
		Models:       detail.Models,
		Effort:       detail.Effort,
		Inquireness:  detail.Inquireness,
		RiskLevel:    detail.RiskLevel,
		ExitCriteria: detail.ExitCriteria,
		Checkpoints:  seedCheckpointsFromDetail(detail),
	}
	cat := apiPhaseModelCatalog(server.ModelCatalogResponse{
		ProviderOrder: []string{"claude"},
		ProviderModels: map[string][]server.ModelDTO{
			"claude": {{ID: "claude/sonnet-4-6"}},
		},
	})
	w := NewRefactorWizardModel(
		detail.Repos, map[string]string{"repo": "/tmp/repo"}, nil,
		config.DefaultsConfig{}, "claude",
		cat.ProviderModels, cat.ProviderOrder, cat.PhaseDefaults, cat.PhaseProviderModels,
		nil, nil, seed,
	)

	// Walk What → Pipeline, move the child's pipeline cursor to Medium,
	// then advance through Review so the wizard builds its result.
	w.nameInput.SetValue("Refactor Child")
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	w, _ = w.Update(enter)                                 // name → description focus
	w, _ = w.Update(enter)                                 // What → Pipeline
	w, _ = w.Update(tea.KeyPressMsg{Code: tea.KeyUp})      // pipeline: large → medium
	w, _ = w.Update(enter)                                 // Pipeline → Review
	w, _ = w.Update(tea.KeyPressMsg{Code: 'G', Text: "G"}) // Review → build result

	result := w.result
	if result == nil {
		t.Fatal("wizard produced no result after the Review step")
	}
	if result.Pipeline != feature.PipelineMedium {
		t.Fatalf("result pipeline = %q, want %q", result.Pipeline, feature.PipelineMedium)
	}
	want := feature.Checkpoints{
		InquiryReview:   seed.Checkpoints.InquiryReview,
		ResearchReview:  seed.Checkpoints.ResearchReview,
		DesignReview:    seed.Checkpoints.DesignReview,
		RoadmapReview:   seed.Checkpoints.RoadmapReview,
		PhasePlanReview: seed.Checkpoints.PhasePlanReview,
		ManualPublish:   seed.Checkpoints.ManualPublish,
	}
	if result.Checkpoints != want {
		t.Fatalf("result checkpoints = %+v, want parent-seeded %+v (Medium must not filter paired Review gates)", result.Checkpoints, want)
	}
	if !result.Checkpoints.InquiryReview || !result.Checkpoints.ResearchReview {
		t.Fatalf("Medium projection cleared seeded gates: %+v", result.Checkpoints)
	}

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{{
			ID: detail.ID, Name: detail.Name, Slug: detail.Slug,
			Status: detail.Status, CurrentPhase: detail.CurrentPhase,
			CreatedAt: detail.CreatedAt, Repos: detail.Repos,
		}}},
		detail: server.FeatureDetailResponse{Feature: detail},
	}
	app := newTestAPIAppModel(t, client)
	defer app.Close()

	app.refactorFeatureCmd(result, detail.ID)()
	if len(client.refactorFeatureRequests) != 1 {
		t.Fatalf("refactor requests = %d, want 1", len(client.refactorFeatureRequests))
	}
	if got := client.refactorFeatureRequests[0].Checkpoints; got != want {
		t.Fatalf("submitted checkpoints = %+v, want parent-seeded %+v", got, want)
	}
	if got := client.refactorFeatureRequests[0].Pipeline; got != feature.PipelineMedium {
		t.Fatalf("submitted pipeline = %q, want independent child choice %q", got, feature.PipelineMedium)
	}
}
