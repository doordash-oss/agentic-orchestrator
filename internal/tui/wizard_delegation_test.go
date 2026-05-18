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
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// Phase 3 delegation tests. These exercise the wizard's per-axis delegation
// block (Models/Inquireness/Checkpoints) that routes keystrokes through the
// shared ConfigEditorModel while preserving the wizard's manually-set flags,
// Inquireness revert-on-esc semantics, and clamp-at-bounds navigation.

func newWizardAtReviewForDelegation(t *testing.T, providerModels map[string][]string, providerOrder []string, phaseModels map[string]map[string][]string) WizardModel {
	t.Helper()
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
			KBBuild:        "opus",
		},
	}
	m := NewWizardModel(nil, nil, nil, defaults, "", providerModels, providerOrder, nil, phaseModels, nil, nil)
	m.nameInput.SetValue("feat")
	m, _ = m.advance() // What → Where
	m.selectedRepos["r"] = true
	m, _ = m.advance() // Where → Pipeline
	m.pipelineCursor = 2
	m.applyPipelineDefaults()
	m, _ = m.advance() // Pipeline → Review
	return m
}

// -- Models axis --

func TestWizardReviewDelegation_ModelsCycleRoundTrip(t *testing.T) {
	providerModels := map[string][]string{
		"claude": {"opus", "sonnet"},
		"codex":  {"gpt-5.4"},
	}
	m := newWizardAtReviewForDelegation(t, providerModels, []string{"claude", "codex"}, nil)

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.summaryEditing {
		t.Fatal("expected summaryEditing=true after Enter on Models")
	}

	first := m.models.Research
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	second := m.models.Research
	if second == first {
		t.Fatalf("Tab did not cycle Research: %q == %q", second, first)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	third := m.models.Research
	if third == second {
		t.Errorf("second Tab did not cycle Research: %q == %q", third, second)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if got := m.models.Research; got != second {
		t.Errorf("Shift+Tab did not cycle back: got %q, want %q", got, second)
	}

	if !m.modelsManuallySet {
		t.Error("expected modelsManuallySet=true after cycling")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.summaryEditing {
		t.Error("expected summaryEditing=false after Esc")
	}
	if got := m.models.Research; got != second {
		t.Errorf("cycled value lost after Esc: got %q, want %q", got, second)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	r := m.Result()
	if r == nil {
		t.Fatal("expected non-nil Result")
	}
	if r.Models.Research != second {
		t.Errorf("WizardResult.Models.Research = %q, want %q", r.Models.Research, second)
	}
}

func TestWizardReviewDelegation_ModelsUpDownClampedAtBounds(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Up at modelCursor=0: stays at 0
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.modelCursor != 0 {
		t.Errorf("Up at modelCursor=0: got %d, want 0", m.modelCursor)
	}

	// Walk down to 4 and beyond: stays at 4
	for i := 0; i < 10; i++ {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.modelCursor != 4 {
		t.Errorf("Down × 10 at top: got modelCursor=%d, want 4 (clamped)", m.modelCursor)
	}
}

func TestWizardReviewDelegation_ModelsSubRowCycle(t *testing.T) {
	providerModels := map[string][]string{
		"claude": {"opus", "sonnet"},
	}
	m := newWizardAtReviewForDelegation(t, providerModels, []string{"claude"}, nil)
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Navigate to Implementation (row 2)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modelCursor != 2 {
		t.Fatalf("modelCursor = %d, want 2", m.modelCursor)
	}
	origImpl := m.models.Implementation
	origResearch := m.models.Research
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.models.Implementation == origImpl {
		t.Error("Tab on Implementation row did not cycle Implementation")
	}
	if m.models.Research != origResearch {
		t.Error("Tab on Implementation row accidentally changed Research")
	}
}

// -- Inquireness axis --

func TestWizardReviewDelegation_InquirenessRightLeft(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.summaryCursor = summaryFieldInquireness
	if m.inquirenessCursor != 1 {
		t.Fatalf("expected initial inquirenessCursor=1 (medium), got %d", m.inquirenessCursor)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.inquirenessCursor != 2 {
		t.Errorf("Right from medium: got %d, want 2 (high)", m.inquirenessCursor)
	}
	if !m.inquirenessManuallySet {
		t.Error("expected inquirenessManuallySet=true after Right")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.inquirenessCursor != 1 {
		t.Errorf("Left from high: got %d, want 1 (medium)", m.inquirenessCursor)
	}
}

func TestWizardReviewDelegation_InquirenessEscReverts(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.summaryCursor = summaryFieldInquireness
	origCursor := m.inquirenessCursor
	origManual := m.inquirenessManuallySet

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.inquirenessCursor == origCursor {
		t.Fatal("expected cursor to advance before Esc")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.inquirenessCursor != origCursor {
		t.Errorf("Esc did not revert cursor: got %d, want %d", m.inquirenessCursor, origCursor)
	}
	if m.inquirenessManuallySet != origManual {
		t.Errorf("Esc did not revert manuallySet: got %v, want %v", m.inquirenessManuallySet, origManual)
	}
}

// -- Checkpoints axis --

func TestWizardReviewDelegation_CheckpointsSpaceToggles(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	orig := m.checkpoints[0]
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if m.checkpoints[0] == orig {
		t.Error("Space did not toggle checkpoint[0]")
	}
	if !m.checkpointsManuallySet {
		t.Error("expected checkpointsManuallySet=true after Space")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if m.checkpoints[0] != orig {
		t.Error("second Space did not toggle back")
	}
}

func TestWizardReviewDelegation_CheckpointsTabTogglesNotJumps(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	orig := m.checkpoints[0]
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.checkpoints[0] == orig {
		t.Error("Tab on Checkpoints did not toggle checkpoint[0] (wizard reshape failed)")
	}
	if m.checkpointsCursor != 0 {
		t.Errorf("Tab on Checkpoints moved sub-cursor: got %d, want 0", m.checkpointsCursor)
	}
	if !m.checkpointsManuallySet {
		t.Error("expected checkpointsManuallySet=true after Tab")
	}
}

func TestWizardReviewDelegation_CheckpointsUpDownClampedAtBounds(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.checkpointsCursor != 0 {
		t.Errorf("Up at checkpointsCursor=0: got %d, want 0", m.checkpointsCursor)
	}
	for i := 0; i < 10; i++ {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.checkpointsCursor != 4 {
		t.Errorf("Down × 10 (publishable=true): got %d, want 4 (clamped)", m.checkpointsCursor)
	}
}

func TestWizardReviewDelegation_CheckpointsManualPublishHidden_Parity(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.provisionalPublishable = false
	initManualPublish := m.checkpoints[4]
	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	for i := 0; i < 10; i++ {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.checkpointsCursor != 3 {
		t.Errorf("Down × 10 (publishable=false): got %d, want 3 (row 4 hidden)", m.checkpointsCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // collapse
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	r := m.Result()
	if r == nil {
		t.Fatal("expected non-nil Result")
	}
	if r.Checkpoints.ManualPublish != initManualPublish {
		t.Errorf("WizardResult.Checkpoints.ManualPublish was force-flipped: got %v, want %v (init)",
			r.Checkpoints.ManualPublish, initManualPublish)
	}
}

func TestWizardReviewDelegation_GateRoundTripAllThreeAxes(t *testing.T) {
	providerModels := map[string][]string{"claude": {"opus", "sonnet"}}
	m := newWizardAtReviewForDelegation(t, providerModels, []string{"claude"}, nil)

	// Cycle Models.Research
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	cycledResearch := m.models.Research
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // collapse

	// Advance Inquireness
	m.summaryCursor = summaryFieldInquireness
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	inquirenessVal := m.inquirenessOptions[m.inquirenessCursor]
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // collapse

	// Toggle the first visible thorough gate (InquiryReview).
	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // collapse

	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	r := m.Result()
	if r == nil {
		t.Fatal("expected non-nil Result")
	}
	if r.Models.Research != cycledResearch {
		t.Errorf("Models.Research = %q, want %q", r.Models.Research, cycledResearch)
	}
	if r.Inquireness != inquirenessVal {
		t.Errorf("Inquireness = %q, want %q", r.Inquireness, inquirenessVal)
	}
	if !r.Checkpoints.InquiryReview {
		t.Errorf("Checkpoints.InquiryReview = %v, want true after toggling the visible thorough gate", r.Checkpoints.InquiryReview)
	}
}

func TestWizardReviewDelegation_NonConfigRowsUntouched(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)

	// Risk: editing left/right cycle the risk pill and should NOT invoke
	// the configEditor (its `original` should stay at zero-value).
	m.summaryCursor = summaryFieldRisk
	origRisk := m.riskCursor
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.riskCursor == origRisk {
		t.Error("Risk Right did not advance cursor")
	}
	if (m.configEditor.original != feature.ConfigSnapshot{}) {
		t.Error("Risk editing should not construct configEditor (original != zero)")
	}
}

// -- Catalog parity --

func TestWizardConfigEditorCatalogParity(t *testing.T) {
	reg := llm.NewRegistry()
	cat := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})

	m := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "",
		cat.ProviderModels, cat.ProviderOrder, cat.PhaseDefaults, cat.PhaseProviderModels,
		nil, nil)

	if !reflect.DeepEqual(m.configCatalog.ProviderModels, cat.ProviderModels) {
		t.Errorf("configCatalog.ProviderModels diverged from BuildPhaseModelCatalog output")
	}
	if !reflect.DeepEqual(m.configCatalog.ProviderOrder, cat.ProviderOrder) {
		t.Errorf("configCatalog.ProviderOrder diverged")
	}
	if !reflect.DeepEqual(m.configCatalog.PhaseDefaults, cat.PhaseDefaults) {
		t.Errorf("configCatalog.PhaseDefaults diverged")
	}
	if !reflect.DeepEqual(m.configCatalog.PhaseProviderModels, cat.PhaseProviderModels) {
		t.Errorf("configCatalog.PhaseProviderModels diverged")
	}
	if !reflect.DeepEqual(m.configCatalog.Fields, cat.Fields) {
		t.Errorf("configCatalog.Fields diverged: got %v, want %v",
			m.configCatalog.Fields, cat.Fields)
	}
}

func TestWizardAndOverlayShareCatalogStructure(t *testing.T) {
	reg := llm.NewRegistry()
	catOverlay := BuildPhaseModelCatalog(reg, config.DefaultsConfig{})
	mw := NewWizardModel(nil, nil, nil, config.DefaultsConfig{}, "",
		catOverlay.ProviderModels, catOverlay.ProviderOrder, catOverlay.PhaseDefaults, catOverlay.PhaseProviderModels,
		nil, nil)

	if !reflect.DeepEqual(mw.configCatalog.Fields, catOverlay.Fields) {
		t.Errorf("wizard and overlay Fields differ: %v vs %v",
			mw.configCatalog.Fields, catOverlay.Fields)
	}
	if !reflect.DeepEqual(mw.configCatalog.PhaseProviderModels, catOverlay.PhaseProviderModels) {
		t.Errorf("wizard and overlay PhaseProviderModels differ")
	}
}

// -- Rendering substring checks --

func TestWizardReviewDelegation_ModelsEditorRendersAssignmentsAndChoices(t *testing.T) {
	providerModels := map[string][]string{
		"claude": {"claude:opus", "claude:sonnet"},
		"codex":  {"codex:gpt-5.4"},
	}
	defaults := config.DefaultsConfig{
		Models: config.ModelConfig{
			Research:       "claude:opus",
			Planning:       "claude:opus",
			Implementation: "claude:opus",
			Review:         "codex:gpt-5.4",
			KBBuild:        "claude:sonnet",
		},
	}
	m := NewWizardModel(nil, nil, nil, defaults, "",
		providerModels, []string{"claude", "codex"}, nil, nil, nil, nil)

	m.nameInput.SetValue("feat")
	m, _ = m.advance()
	m.selectedRepos["r"] = true
	m, _ = m.advance()
	m, _ = m.advance()
	m.width = 160
	m.height = 40

	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := m.View()
	needles := []string{
		"Model Selection",
		"Research",
		"Planning",
		"Implementation",
		"Review",
		"KB Build",
		"Choices for Research",
	}
	for _, needle := range needles {
		if !strings.Contains(view, needle) {
			t.Errorf("rendered Models editor missing %q", needle)
		}
	}
}

func TestWizardReviewDelegation_CheckpointsEditorRendersGates(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.width = 140
	m.height = 40
	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := m.View()
	for _, needle := range []string{"Gates", "Inquiry Review", "Research Review", "Design Review", "Plan Review"} {
		if !strings.Contains(view, needle) {
			t.Errorf("rendered Checkpoints editor missing %q", needle)
		}
	}
	if !strings.Contains(view, "Manual Publish") {
		t.Error("Manual Publish row should appear when provisionalPublishable=true")
	}
}

func TestWizardReviewDelegation_InquirenessEditorRendersPills(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.width = 140
	m.height = 40
	m.summaryCursor = summaryFieldInquireness
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := m.View()
	for _, needle := range []string{"Inquiry", "none", "medium", "high"} {
		if !strings.Contains(view, needle) {
			t.Errorf("rendered Inquireness editor missing %q", needle)
		}
	}
}

// -- ManualPublish hidden invariant guard (separate Test prefix to match verification) --

func TestWizardManualPublishHidden_WhenNotPublishable(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.provisionalPublishable = false
	m.width = 120
	m.height = 40

	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	view := m.View()
	if strings.Contains(view, "Manual Publish") {
		t.Error("Manual Publish row should be hidden when !provisionalPublishable")
	}
}
