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
	"errors"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"

	tea "charm.land/bubbletea/v2"
)

// ptrFalse returns a pointer to false for seeding FeatureRepo.Publishable.
func ptrFalse() *bool { f := false; return &f }

// TestEditConfigOverlay_InquirenessCycle verifies the Inquireness axis
// cycles through none/medium/high on right/left and wraps at both ends.
func TestEditConfigOverlay_InquirenessCycle(t *testing.T) {
	f := &feature.Feature{
		ID:          "f1",
		Name:        "test",
		Inquireness: feature.InquirenessMedium,
	}
	cat := BuildPhaseModelCatalog(nil, config.DefaultsConfig{})
	editor := NewConfigEditorModel(f, cat, true)
	// Move cursor off the default Models row 0 onto the Inquireness row.
	editor.rowCursor = editor.inquirenessRow()

	// medium -> right -> high
	editor, _ = editor.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := editor.Snapshot().Inquireness; got != feature.InquirenessHigh {
		t.Errorf("after right from medium: got %q, want high", got)
	}
	// high -> right wraps to none
	editor, _ = editor.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := editor.Snapshot().Inquireness; got != feature.InquirenessNone {
		t.Errorf("after right from high (wrap): got %q, want none", got)
	}
	// none -> left wraps to high
	editor, _ = editor.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got := editor.Snapshot().Inquireness; got != feature.InquirenessHigh {
		t.Errorf("after left from none (wrap): got %q, want high", got)
	}
}

func TestEditConfigBehaviorPane_InquirenessCopyDescribesHarnessSurfacing(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{ID: "f1", Name: "copy", Inquireness: feature.InquirenessMedium}
	app = withOverlay(t, app, f)
	view := app.editConfig.renderBehaviorPane()

	if !strings.Contains(view, "Harness surfaces key planning questions") {
		t.Fatalf("renderBehaviorPane() missing harness-owned copy:\n%s", view)
	}
	for _, forbidden := range []string{
		"Agent works autonomously",
		"Agent pauses at key decisions",
		"Agent asks before every major step",
	} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("renderBehaviorPane() still contains agent-owned copy %q:\n%s", forbidden, view)
		}
	}
}

// TestAppModel_EditConfigResultMsg_OnSuccessClosesOverlay verifies the
// authoritative close path: a successful editConfigResultMsg zeroes the
// overlay state and does not depend on OrchFeatureConfigChangedMsg arriving.
func TestAppModel_EditConfigResultMsg_OnSuccessClosesOverlay(t *testing.T) {
	app, _ := newTestAppModel(t)
	app.editConfigActive = true
	app.editConfig = EditConfigModel{featureID: "f1", saving: true}

	updated, _ := app.Update(editConfigResultMsg{featureID: "f1", err: nil})
	got := updated.(AppModel)
	if got.editConfigActive {
		t.Error("editConfigActive should be false after successful save")
	}
	if got.editConfig.featureID != "" {
		t.Errorf("editConfig.featureID should be zeroed, got %q", got.editConfig.featureID)
	}
}

// TestAppModel_EditConfigResultMsg_OnErrorKeepsOverlayOpen verifies that on
// save error the overlay stays open with the error banner populated.
func TestAppModel_EditConfigResultMsg_OnErrorKeepsOverlayOpen(t *testing.T) {
	app, _ := newTestAppModel(t)
	app.editConfigActive = true
	app.editConfig = EditConfigModel{featureID: "f1", saving: true}

	errIn := errors.New("update feature f1 config: " + orchestrator.ErrFeatureNotQuiescent.Error())
	updated, _ := app.Update(editConfigResultMsg{featureID: "f1", err: errIn})
	got := updated.(AppModel)
	if !got.editConfigActive {
		t.Error("editConfigActive should remain true after save error")
	}
	if got.editConfig.saving {
		t.Error("editConfig.saving should be cleared after save error")
	}
	if got.editConfig.saveErr == "" {
		t.Error("editConfig.saveErr should be set after save error")
	}
}

// TestIsFeatureQuiescent covers the predicate that gates the `e` key.
func TestIsFeatureQuiescent(t *testing.T) {
	tests := []struct {
		name string
		f    *feature.Feature
		want bool
	}{
		{"nil", nil, false},
		{"failed (quiescent)", &feature.Feature{Status: feature.StatusFailed}, true},
		{"created (quiescent)", &feature.Feature{Status: feature.StatusCreated}, true},
		{"code-ready (quiescent)", &feature.Feature{Status: feature.StatusCodeReady}, true},
		{"implementing (running)", &feature.Feature{Status: feature.StatusImplementing}, false},
		{"plan-needs-review", &feature.Feature{Status: feature.StatusPlanNeedsReview}, false},
		{
			"active repo cycle",
			&feature.Feature{
				Status: feature.StatusPublished,
				RepoCycles: map[string]*feature.RepoCycleState{
					"a": {Status: "running"},
				},
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFeatureQuiescent(tt.f); got != tt.want {
				t.Errorf("isFeatureQuiescent = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- Phase 2 overlay behavior ---

// withOverlay seeds an AppModel with an active overlay for the given feature.
// Returns the app and the helper-built catalog for further assertions.
func withOverlay(t *testing.T, app AppModel, f *feature.Feature) AppModel {
	t.Helper()
	cat := BuildPhaseModelCatalog(app.registry, app.featureManager.Config.Defaults)
	app.editConfig = NewEditConfigModel(f, cat, f.IsPublishable())
	app.editConfigActive = true
	return app
}

func TestEditConfig_EscOnClean_ClosesImmediately(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{ID: "f1", Name: "clean", Inquireness: feature.InquirenessMedium}
	app = withOverlay(t, app, f)

	updated, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	got := updated.(AppModel)
	if got.editConfigActive {
		t.Error("overlay should close on esc when no changes")
	}
	if got.editConfig.featureID != "" {
		t.Errorf("editConfig should be zeroed, got featureID %q", got.editConfig.featureID)
	}
	if cmd != nil {
		t.Error("no command should dispatch on clean esc")
	}
}

func TestEditConfig_EscOnDirty_EntersDiscardConfirm(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{ID: "f1", Name: "dirty", Inquireness: feature.InquirenessMedium}
	app = withOverlay(t, app, f)
	// Simulate a prior edit: cycle inquireness.
	app.editConfig.editor.inquireness = feature.InquirenessHigh

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	got := updated.(AppModel)
	if !got.editConfigActive {
		t.Error("overlay should remain open on dirty esc")
	}
	if !got.editConfig.discardConfirm {
		t.Error("discardConfirm should be true after dirty esc")
	}
}

func TestEditConfig_DiscardConfirm_Yes_Closes(t *testing.T) {
	orch := newFakeOrch()
	app, _ := newTestAppModel(t)
	app.orchestrator = orch
	f := &feature.Feature{ID: "f1", Name: "dirty", Inquireness: feature.InquirenessMedium}
	app = withOverlay(t, app, f)
	app.editConfig.editor.inquireness = feature.InquirenessHigh
	app.editConfig.discardConfirm = true

	updated, _ := app.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	got := updated.(AppModel)
	if got.editConfigActive {
		t.Error("overlay should close after y confirm")
	}
	if n := len(orch.updateFeatureConfigArgs); n != 0 {
		t.Errorf("expected no save to dispatch, got %d", n)
	}
}

func TestEditConfig_DiscardConfirm_No_ReturnsToEditing(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{ID: "f1", Name: "dirty", Inquireness: feature.InquirenessMedium}
	app = withOverlay(t, app, f)
	app.editConfig.editor.inquireness = feature.InquirenessHigh
	app.editConfig.discardConfirm = true

	updated, _ := app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	got := updated.(AppModel)
	if !got.editConfigActive {
		t.Error("overlay should stay open after n")
	}
	if got.editConfig.discardConfirm {
		t.Error("discardConfirm should be cleared after n")
	}
	if got.editConfig.editor.inquireness != feature.InquirenessHigh {
		t.Error("edit state should remain intact after n")
	}
}

func TestEditConfig_DiscardConfirm_AnyKey_ReturnsToEditing(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{ID: "f1", Name: "dirty", Inquireness: feature.InquirenessMedium}
	app = withOverlay(t, app, f)
	app.editConfig.editor.inquireness = feature.InquirenessHigh
	app.editConfig.discardConfirm = true

	updated, _ := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	got := updated.(AppModel)
	if got.editConfig.discardConfirm {
		t.Error("discardConfirm should be cleared after non-y key")
	}
	if got.editConfig.editor.inquireness != feature.InquirenessHigh {
		t.Error("edit state should remain intact after non-y key")
	}
}

func TestEditConfig_EnterDispatchesSaveWithAllAxes(t *testing.T) {
	orch := newFakeOrch()
	app, _ := newTestAppModel(t)
	app.orchestrator = orch
	f := &feature.Feature{
		ID:          "f1",
		Name:        "multi",
		Inquireness: feature.InquirenessMedium,
		Models:      config.ModelConfig{Research: "r"},
	}
	app = withOverlay(t, app, f)
	// Dirty all three axes.
	app.editConfig.editor.inquireness = feature.InquirenessHigh
	app.editConfig.editor.models.Research = "new-research"
	app.editConfig.editor.checkpoints.PlanReview = true

	updated, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(AppModel)
	if !got.editConfig.saving {
		t.Error("editConfig.saving should be true after enter")
	}
	if cmd == nil {
		t.Fatal("enter should dispatch a save command")
	}
	_ = cmd() // fire the command synchronously

	if len(orch.updateFeatureConfigArgs) != 1 {
		t.Fatalf("expected 1 save dispatch, got %d", len(orch.updateFeatureConfigArgs))
	}
	call := orch.updateFeatureConfigArgs[0]
	if call.FeatureID != "f1" {
		t.Errorf("featureID = %q, want f1", call.FeatureID)
	}
	if call.Input.Inquireness != feature.InquirenessHigh {
		t.Errorf("Inquireness = %q, want high", call.Input.Inquireness)
	}
	if call.Input.Models.Research != "new-research" {
		t.Errorf("Models.Research = %q, want new-research", call.Input.Models.Research)
	}
	if !call.Input.Checkpoints.PlanReview {
		t.Error("Checkpoints.PlanReview should be true")
	}
}

func TestEditConfig_EnterDispatchesManualPublishForced(t *testing.T) {
	orch := newFakeOrch()
	app, _ := newTestAppModel(t)
	app.orchestrator = orch
	f := &feature.Feature{
		ID:          "f1",
		Name:        "no-publish",
		Inquireness: feature.InquirenessMedium,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Publishable: ptrFalse()},
		},
	}
	app = withOverlay(t, app, f)
	// Change a non-checkpoints axis so the save has something to differ;
	// critically, leave ManualPublish at its internal false value.
	app.editConfig.editor.models.Research = "new-r"

	_, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should dispatch a save command")
	}
	_ = cmd()
	if len(orch.updateFeatureConfigArgs) != 1 {
		t.Fatalf("expected 1 save, got %d", len(orch.updateFeatureConfigArgs))
	}
	if !orch.updateFeatureConfigArgs[0].Input.Checkpoints.ManualPublish {
		t.Error("ManualPublish should be forced true when feature is not publishable")
	}
}

func TestEditConfig_DiffSummaryFooter(t *testing.T) {
	app, _ := newTestAppModel(t)
	cases := []struct {
		name   string
		mutate func(m *EditConfigModel)
		want   []string
	}{
		{
			name:   "no changes",
			mutate: func(m *EditConfigModel) {},
			want:   []string{"No changes"},
		},
		{
			name: "inquireness only",
			mutate: func(m *EditConfigModel) {
				m.editor.inquireness = feature.InquirenessHigh
			},
			want: []string{"Models: 0 changes", "Gates: 0 changes", "Inquiry: changed"},
		},
		{
			name: "models one",
			mutate: func(m *EditConfigModel) {
				m.editor.models.Research = "different"
			},
			want: []string{"Models: 1 change", "Gates: 0 changes", "Inquiry: unchanged"},
		},
		{
			name: "multi-axis",
			mutate: func(m *EditConfigModel) {
				m.editor.models.Research = "r2"
				m.editor.models.Planning = "p2"
				m.editor.checkpoints.PlanReview = true
				m.editor.inquireness = feature.InquirenessNone
			},
			want: []string{"Models: 2 changes", "Gates: 1 change", "Inquiry: changed"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &feature.Feature{
				ID:          "f1",
				Name:        "sum",
				Inquireness: feature.InquirenessMedium,
			}
			app2 := withOverlay(t, app, f)
			tc.mutate(&app2.editConfig)
			view := app2.editConfig.View()
			for _, frag := range tc.want {
				if !strings.Contains(view, frag) {
					t.Errorf("view missing %q\nview:\n%s", frag, view)
				}
			}
		})
	}
}

func TestEditConfig_StaleModel_RendersUnavailable(t *testing.T) {
	app, _ := newTestAppModel(t)
	cat := BuildPhaseModelCatalog(app.registry, app.featureManager.Config.Defaults)
	// Force a stale value that cannot be in the catalog.
	f := &feature.Feature{
		ID:     "f1",
		Name:   "stale",
		Models: config.ModelConfig{Research: "this-model-does-not-exist-xyz"},
	}
	app.editConfig = NewEditConfigModel(f, cat, true)
	view := app.editConfig.View()
	if !strings.Contains(view, "(unavailable)") {
		t.Errorf("expected (unavailable) in view for stale model; view:\n%s", view)
	}
}

// TestEditConfig_TabCyclesSegmentedTabs asserts the segmented-tab
// navigation grammar: tab/shift+tab cycle the active tab (Models →
// Behavior → Gates) and snap the editor's cursor to the first row of the
// new tab so per-row keys (←/→/space) always target something visible.
func TestEditConfig_TabCyclesSegmentedTabs(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{ID: "f1", Name: "tab", Inquireness: feature.InquirenessMedium}
	app = withOverlay(t, app, f)
	if app.editConfig.activeTab != tabModels {
		t.Fatalf("initial activeTab = %v, want tabModels", app.editConfig.activeTab)
	}

	// tab: Models → Behavior, cursor snaps to inquirenessRow.
	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	got := updated.(AppModel)
	if got.editConfig.activeTab != tabBehavior {
		t.Errorf("after tab: activeTab = %v, want tabBehavior", got.editConfig.activeTab)
	}
	if want := got.editConfig.editor.inquirenessRow(); got.editConfig.editor.rowCursor != want {
		t.Errorf("after tab: rowCursor = %d, want %d (inquirenessRow)", got.editConfig.editor.rowCursor, want)
	}

	// tab: Behavior → Gates, cursor snaps to first checkpoint row.
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	got = updated.(AppModel)
	if got.editConfig.activeTab != tabGates {
		t.Errorf("after second tab: activeTab = %v, want tabGates", got.editConfig.activeTab)
	}
	if want := got.editConfig.editor.checkpointsStart(); got.editConfig.editor.rowCursor != want {
		t.Errorf("after second tab: rowCursor = %d, want %d (first checkpoint)", got.editConfig.editor.rowCursor, want)
	}

	// tab: Gates → Models (wrap), cursor snaps to row 0.
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	got = updated.(AppModel)
	if got.editConfig.activeTab != tabModels {
		t.Errorf("after third tab: activeTab = %v, want tabModels (wrap)", got.editConfig.activeTab)
	}
	if got.editConfig.editor.rowCursor != 0 {
		t.Errorf("after third tab: rowCursor = %d, want 0", got.editConfig.editor.rowCursor)
	}

	// shift+tab from Models wraps backward to Gates.
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	got = updated.(AppModel)
	if got.editConfig.activeTab != tabGates {
		t.Errorf("after shift+tab: activeTab = %v, want tabGates (wrap back)", got.editConfig.activeTab)
	}
}

// TestEditConfig_UpDownWrapsWithinActiveTab asserts ↑/↓ are scoped to
// the active tab's row range and wrap inside it — the flat-walk cursor
// can no longer cross tab boundaries from the overlay's nav keys.
func TestEditConfig_UpDownWrapsWithinActiveTab(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{ID: "f1", Name: "walk", Inquireness: feature.InquirenessMedium}
	app = withOverlay(t, app, f)

	// Models tab: cursor starts at 0. Up wraps to last Models row.
	modelsLast := app.editConfig.editor.modelsCount() - 1
	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got := updated.(AppModel)
	if got.editConfig.editor.rowCursor != modelsLast {
		t.Errorf("up-wrap in Models tab: rowCursor = %d, want %d", got.editConfig.editor.rowCursor, modelsLast)
	}
	// Down from last Models row wraps back to 0, not across to Behavior.
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(AppModel)
	if got.editConfig.editor.rowCursor != 0 {
		t.Errorf("down-wrap in Models tab: rowCursor = %d, want 0", got.editConfig.editor.rowCursor)
	}

	// Jump to Gates, walk rows there.
	got.editConfig.activeTab = tabGates
	got.editConfig.editor.rowCursor = got.editConfig.editor.checkpointsStart()
	start := got.editConfig.editor.checkpointsStart()
	last := got.editConfig.editor.lastRow()
	// Up from first checkpoint wraps to last.
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got = updated.(AppModel)
	if got.editConfig.editor.rowCursor != last {
		t.Errorf("up-wrap in Gates tab: rowCursor = %d, want %d", got.editConfig.editor.rowCursor, last)
	}
	// Down from last wraps to start, not past into another tab.
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(AppModel)
	if got.editConfig.editor.rowCursor != start {
		t.Errorf("down-wrap in Gates tab: rowCursor = %d, want %d", got.editConfig.editor.rowCursor, start)
	}
}

func TestEditConfig_CatalogBuiltAtModalOpen(t *testing.T) {
	// Right-panel dispatch path. Seed the dashboard, focus the right panel,
	// and drive the handler directly.
	app, fm := newTestAppModel(t)
	f, err := fm.Create("catalog-test", "desc", nil, config.ModelConfig{}, "", "medium", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	app.currentView = ViewDashboard
	app.dashboard.SetFeatures([]*feature.Feature{f})
	app.dashboard.MoveToAdjacentFeature(1)
	if app.dashboard.SelectedFeature() == nil {
		app.dashboard.MoveToAdjacentFeature(-1)
	}
	if app.dashboard.SelectedFeature() == nil {
		t.Skip("dashboard selection did not resolve to a feature — layout dependent")
	}
	app.dashboard.focusPanel = 1

	updated, _ := app.updateDashboardRightPanel(tea.KeyPressMsg{Code: 'e', Text: "e"})
	got := updated.(AppModel)
	if !got.editConfigActive {
		t.Fatal("right-panel `e` should open the overlay for a quiescent feature")
	}
	if n := len(got.editConfig.editor.catalog.Fields); n != 5 {
		t.Errorf("catalog.Fields = %d entries, want 5", n)
	}

	// Detail-view dispatch path.
	app2, fm2 := newTestAppModel(t)
	f2, err := fm2.Create("detail-test", "desc", nil, config.ModelConfig{}, "", "medium", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	app2.currentView = ViewDetail
	app2.detail = NewDetailModel(f2, fm2.Store.BaseDir)
	updated2, _ := app2.updateDetail(tea.KeyPressMsg{Code: 'e', Text: "e"})
	got2 := updated2.(AppModel)
	if !got2.editConfigActive {
		t.Fatal("detail-view `e` should open the overlay for a quiescent feature")
	}
	if n := len(got2.editConfig.editor.catalog.Fields); n != 5 {
		t.Errorf("catalog.Fields = %d entries, want 5", n)
	}
}
