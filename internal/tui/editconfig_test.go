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
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
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
	app.editConfig.focus = configFocusPhaseList
	view := app.editConfig.View()
	if !strings.Contains(view, "(unavailable)") {
		t.Errorf("expected (unavailable) in view for stale model; view:\n%s", view)
	}
}

func TestEditConfig_ModelFilterEscLeavesEditorOpen(t *testing.T) {
	reg := openCodeWinningRegistry()
	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})
	f := &feature.Feature{ID: "feat", Name: "Feature", Models: config.ModelConfig{Research: "opencode:anthropic/claude-sonnet-4-5[200K]"}}
	m := NewEditConfigModel(f, cat, true)
	m.editor.activeModelCell = modelCellModel

	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if cmd != nil {
		t.Fatal("unexpected command from filter start")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.editor.ModelFilteringActive() {
		t.Fatal("filter still active after esc")
	}
}

func TestAppModel_EditConfigModelFilterEnterLeavesOverlayOpen(t *testing.T) {
	app, _ := newTestAppModel(t)
	reg := openCodeWinningRegistry()
	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})
	f := &feature.Feature{
		ID:     "feat",
		Name:   "Feature",
		Models: config.ModelConfig{Research: "opencode:anthropic/claude-sonnet-4-5[200K]"},
	}
	app.editConfig = NewEditConfigModel(f, cat, true)
	app.editConfigActive = true
	app.editConfig.activeTab = tabModels
	app.editConfig.editor.activeModelCell = modelCellModel
	app.editConfig.focus = configFocusModelList

	var cmd tea.Cmd
	updated, cmd := app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if cmd != nil {
		t.Fatal("unexpected command from filter start")
	}
	got := updated.(AppModel)
	if !got.editConfigActive {
		t.Fatal("editConfigActive = false after filter start")
	}
	if !got.editConfig.editor.ModelFilteringActive() {
		t.Fatal("filter not active after slash")
	}

	for _, r := range "gpt" {
		updated, cmd = got.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		if cmd != nil {
			t.Fatalf("unexpected command while typing filter %q", r)
		}
		got = updated.(AppModel)
	}
	updated, cmd = got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("unexpected save command from filter enter")
	}
	got = updated.(AppModel)
	if !got.editConfigActive {
		t.Fatal("editConfigActive = false, want overlay to remain open after filter enter")
	}
	if got.editConfig.editor.ModelFilteringActive() {
		t.Fatal("filter still active after enter")
	}
	if got.editConfig.editor.models.Research != "opencode:openai/gpt-5" {
		t.Fatalf("Research model = %q, want opencode:openai/gpt-5", got.editConfig.editor.models.Research)
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

func TestEditConfig_ModelTabFocusStartsOnTabsThenDownEntersPhaseList(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{ID: "f1", Name: "focus"}
	app = withOverlay(t, app, f)

	if got := app.editConfig.focus; got != configFocusTabs {
		t.Fatalf("initial focus = %v, want tabs", got)
	}
	view := app.editConfig.View()
	if !strings.Contains(view, "▸ Models") {
		t.Fatalf("initial view missing focused Models tab arrow:\n%s", view)
	}

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	got := updated.(AppModel)
	if got.editConfig.activeTab != tabBehavior {
		t.Fatalf("tab while tabs focused activeTab = %v, want Behavior", got.editConfig.activeTab)
	}
	if got.editConfig.focus != configFocusTabs {
		t.Fatalf("tab while tabs focused focus = %v, want tabs", got.editConfig.focus)
	}

	app = withOverlay(t, app, f)
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(AppModel)
	if got.editConfig.focus != configFocusPhaseList {
		t.Fatalf("down from tabs focus = %v, want phase list", got.editConfig.focus)
	}
	if got.editConfig.editor.rowCursor != 0 {
		t.Fatalf("down from tabs rowCursor = %d, want first phase", got.editConfig.editor.rowCursor)
	}
}

func TestEditConfig_ModelTabPhaseAndPickerKeyboard(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{ID: "f1", Name: "picker", Models: config.ModelConfig{Research: "claude:sonnet[200K]"}}
	app = withOverlay(t, app, f)

	var updated tea.Model
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got := updated.(AppModel)
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(AppModel)
	if got.editConfig.editor.rowCursor != 1 {
		t.Fatalf("down in phase list rowCursor = %d, want Planning row", got.editConfig.editor.rowCursor)
	}
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got = updated.(AppModel)
	if got.editConfig.editor.rowCursor != 0 {
		t.Fatalf("up in phase list rowCursor = %d, want Research row", got.editConfig.editor.rowCursor)
	}
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got = updated.(AppModel)
	if got.editConfig.focus != configFocusTabs {
		t.Fatalf("up from first phase focus = %v, want tabs", got.editConfig.focus)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(AppModel)
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	got = updated.(AppModel)
	if got.editConfig.focus != configFocusAgentList {
		t.Fatalf("right from phase focus = %v, want agent list", got.editConfig.focus)
	}
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	got = updated.(AppModel)
	if got.editConfig.focus != configFocusModelList {
		t.Fatalf("right from agent list focus = %v, want model list", got.editConfig.focus)
	}
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got = updated.(AppModel)
	if got.editConfig.focus != configFocusPhaseList {
		t.Fatalf("enter from model list focus = %v, want phase list", got.editConfig.focus)
	}
	if got.editConfig.saving {
		t.Fatal("enter from model list should not dispatch save")
	}
}

func TestEditConfig_ModelWorkspaceRendersRightSidePickerWithoutIDAndWithCompactContext(t *testing.T) {
	reg := llm.NewRegistry()
	reg.Register(&phaseCatalogStubProvider{
		name:   "opencode",
		models: []string{"anthropic/claude-sonnet-4-5[200K]", "openai/gpt-5.4[1M]"},
		catalog: []llm.ModelInfo{
			{ID: "anthropic/claude-sonnet-4-5[200K]", DisplayName: "Claude Sonnet 4.5", ContextWindow: 200_000, Category: "balanced"},
			{ID: "openai/gpt-5.4[1M]", DisplayName: "GPT-5.4", ContextWindow: 1_000_000, Category: "capable"},
		},
	})
	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})
	f := &feature.Feature{ID: "feat", Name: "Feature", Models: config.ModelConfig{Research: "anthropic/claude-sonnet-4-5[200K]"}}
	m := NewEditConfigModel(f, cat, true)
	m.focus = configFocusPhaseList

	view := m.View()
	for _, want := range []string{"Agents", "Models for opencode", "Research (opencode/", "200K"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	for _, forbidden := range []string{"ID      ", "context 200000", "context 1000000"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("view contained %q, want no raw ID/context detail:\n%s", forbidden, view)
		}
	}
	if !strings.Contains(view, "1M") {
		t.Fatalf("view missing compact 1M context label:\n%s", view)
	}
}

func TestEditConfig_ModelWorkspaceAlwaysShowsThreePanelsAndOneFocusCursor(t *testing.T) {
	reg := llm.NewRegistry()
	reg.Register(&phaseCatalogStubProvider{
		name:   "opencode",
		models: []string{"ollama/gemma4:26b-256k[262K]", "ollama/gemma4:31b-256k[262K]"},
		catalog: []llm.ModelInfo{
			{ID: "ollama/gemma4:26b-256k[262K]", DisplayName: "Gemma 4 26B 256k (Local) (262K)", ContextWindow: 262_144, Category: "balanced"},
			{ID: "ollama/gemma4:31b-256k[262K]", DisplayName: "Gemma 4 31B Dense 256k (Local) (262K)", ContextWindow: 262_144, Category: "balanced"},
		},
	})
	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})
	f := &feature.Feature{
		ID:     "feat",
		Name:   "Feature",
		Models: config.ModelConfig{Research: "ollama/gemma4:26b-256k[262K]"},
	}

	for _, tc := range []struct {
		name  string
		focus configFocusZone
	}{
		{"tabs", configFocusTabs},
		{"phase", configFocusPhaseList},
		{"agent", configFocusAgentList},
		{"model", configFocusModelList},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewEditConfigModel(f, cat, true)
			m.focus = tc.focus
			view := m.View()
			for _, want := range []string{"Phases", "Agents", "Models for opencode"} {
				if !strings.Contains(view, want) {
					t.Fatalf("focus %v view missing stable panel %q:\n%s", tc.focus, want, view)
				}
			}
			if got := strings.Count(view, "▸"); got != 1 {
				t.Fatalf("focus %v rendered %d focus cursors, want exactly one:\n%s", tc.focus, got, view)
			}
			if strings.Contains(view, "\n(262K") {
				t.Fatalf("phase label wrapped context onto its own line:\n%s", view)
			}
		})
	}
}

func TestEditConfig_AllTabsUseStableThreePanelWorkspace(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{ID: "f1", Name: "stable", Inquireness: feature.InquirenessMedium}
	app = withOverlay(t, app, f)

	lineCounts := map[configTab]int{}
	for _, tab := range []configTab{tabModels, tabBehavior, tabGates} {
		m := app.editConfig
		m.activeTab = tab
		m.focus = configFocusTabs
		view := stripANSI(m.View())
		lineCounts[tab] = len(strings.Split(strings.TrimRight(view, "\n"), "\n"))
		if got := strings.Count(view, "┌"); got < 3 {
			t.Fatalf("tab %v rendered %d workspace panels, want at least 3:\n%s", tab, got, view)
		}
	}

	if lineCounts[tabBehavior] != lineCounts[tabModels] {
		t.Fatalf("Behavior line count = %d, want Models count %d", lineCounts[tabBehavior], lineCounts[tabModels])
	}
	if lineCounts[tabGates] != lineCounts[tabModels] {
		t.Fatalf("Gates line count = %d, want Models count %d", lineCounts[tabGates], lineCounts[tabModels])
	}
}

func TestEditConfig_GatesStatePanelIsOnOffPicker(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{
		ID:       "f1",
		Name:     "gates",
		Pipeline: feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{
			PlanReview:    true,
			ManualPublish: true,
		},
	}
	app = withOverlay(t, app, f)
	app.editConfig.activeTab = tabGates
	app.editConfig.focus = configFocusTabs

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got := updated.(AppModel)
	if got.editConfig.focus != configFocusGateList {
		t.Fatalf("down into Gates focus = %v, want gate list", got.editConfig.focus)
	}

	view := stripANSI(got.editConfig.View())
	for _, want := range []string{"State", "on", "off", "Pause after planning before implementation"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Gates view missing %q:\n%s", want, view)
		}
	}
	if strings.Count(view, "Plan Review") != 1 {
		t.Fatalf("Details panel should not repeat Plan Review; view:\n%s", view)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	got = updated.(AppModel)
	if got.editConfig.focus != configFocusGateState {
		t.Fatalf("right from gate list focus = %v, want gate state", got.editConfig.focus)
	}
	view = stripANSI(got.editConfig.View())
	if !strings.Contains(view, "▸ on") {
		t.Fatalf("state picker did not focus selected on row:\n%s", view)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(AppModel)
	if got.editConfig.editor.checkpoints.PlanReview {
		t.Fatal("down in state picker should set Plan Review off")
	}
	view = stripANSI(got.editConfig.View())
	if !strings.Contains(view, "▸ off") {
		t.Fatalf("state picker did not focus off row after change:\n%s", view)
	}
}

func TestEditConfig_BehaviorValuesUseVerticalNavigation(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{ID: "f1", Name: "behavior", Inquireness: feature.InquirenessMedium}
	app = withOverlay(t, app, f)
	app.editConfig.activeTab = tabBehavior
	app.editConfig.focus = configFocusTabs

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got := updated.(AppModel)
	if got.editConfig.focus != configFocusBody {
		t.Fatalf("down into Behavior focus = %v, want body", got.editConfig.focus)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(AppModel)
	if got.editConfig.editor.inquireness != feature.InquirenessHigh {
		t.Fatalf("down in Behavior selected %q, want %q", got.editConfig.editor.inquireness, feature.InquirenessHigh)
	}
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got = updated.(AppModel)
	if got.editConfig.editor.inquireness != feature.InquirenessMedium {
		t.Fatalf("up in Behavior selected %q, want %q", got.editConfig.editor.inquireness, feature.InquirenessMedium)
	}

	view := stripANSI(got.editConfig.View())
	for _, want := range []string{"Selected", "medium", "Effect", "Harness surfaces key planning questions", "↑↓ choose"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Behavior view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "←→ choose") {
		t.Fatalf("Behavior hints still advertise horizontal value selection:\n%s", view)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got = updated.(AppModel)
	if got.editConfig.focus != configFocusTabs {
		t.Fatalf("enter in Behavior focus = %v, want tabs", got.editConfig.focus)
	}
	if got.editConfig.saving {
		t.Fatal("enter in Behavior should not save")
	}
}

func TestEditConfig_UpDownLeavesModelPhasesThroughTabsAndClampsBodyRows(t *testing.T) {
	app, _ := newTestAppModel(t)
	f := &feature.Feature{ID: "f1", Name: "walk", Inquireness: feature.InquirenessMedium}
	app = withOverlay(t, app, f)

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got := updated.(AppModel)
	if got.editConfig.focus != configFocusPhaseList {
		t.Fatalf("down from tabs focus = %v, want phase list", got.editConfig.focus)
	}
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	got = updated.(AppModel)
	if got.editConfig.focus != configFocusTabs {
		t.Errorf("up from first model phase focus = %v, want tabs", got.editConfig.focus)
	}

	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	got = updated.(AppModel)
	if got.editConfig.activeTab != tabBehavior {
		t.Fatalf("tab from tabs activeTab = %v, want Behavior", got.editConfig.activeTab)
	}
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(AppModel)
	if got.editConfig.focus != configFocusBody {
		t.Fatalf("down into Behavior focus = %v, want body", got.editConfig.focus)
	}
	updated, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got = updated.(AppModel)
	if got.editConfig.editor.inquireness != feature.InquirenessHigh {
		t.Errorf("down in Behavior selected %q, want %q", got.editConfig.editor.inquireness, feature.InquirenessHigh)
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
