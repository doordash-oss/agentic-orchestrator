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
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// Phase 3 delegation tests. These exercise the wizard's per-axis delegation
// block (Models/Inquireness/Checkpoints) that routes keystrokes through the
// shared ConfigEditorModel while preserving the wizard's manually-set flags,
// Inquireness revert-on-esc semantics, and clamp-at-bounds navigation.

func newWizardAtReviewForDelegation(t *testing.T, providerModels map[string][]string, providerOrder []string, phaseModels map[string]map[string][]string) WizardModel {
	t.Helper()
	defaults := config.NewDefault().Defaults
	defaults.Models = config.ModelConfig{
		Inquiry:        "opus",
		Research:       "opus",
		Planning:       "opus",
		Implementation: "opus",
		Review:         "opus",
		KBBuild:        "opus",
	}
	defaults.Inquireness = "medium"
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
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // Clarify -> Research
	if !m.summaryEditing {
		t.Fatal("expected summaryEditing=true after Enter on Models")
	}

	first := m.models.Research
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.configEditor.activeModelCell; got != modelCellAgent {
		t.Fatalf("Right activeModelCell = %v, want modelCellAgent", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.configEditor.activeModelCell; got != modelCellModel {
		t.Fatalf("second Right activeModelCell = %v, want modelCellModel", got)
	}
	if got := m.models.Research; got != first {
		t.Fatalf("panel focus changed Research: got %q, want %q", got, first)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	second := m.models.Research
	if second == first {
		t.Fatalf("Down on Model panel did not cycle Research: %q == %q", second, first)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	firstSelection := "claude:" + first
	if got := m.models.Research; got != firstSelection {
		t.Errorf("Up on Model panel did not cycle back: got %q, want %q", got, firstSelection)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if got := m.configEditor.activeModelCell; got != modelCellAgent {
		t.Errorf("Shift+Tab activeModelCell = %v, want modelCellAgent", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	providerValue := m.models.Research
	if providerValue == firstSelection {
		t.Errorf("Down on Agent panel did not select a different provider model: got %q", providerValue)
	}

	if !m.modelsManuallySet {
		t.Error("expected modelsManuallySet=true after changing model/provider")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.summaryEditing {
		t.Error("expected summaryEditing=false after Esc")
	}
	if got := m.models.Research; got != providerValue {
		t.Errorf("changed value lost after Esc: got %q, want %q", got, providerValue)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	r := m.Result()
	if r == nil {
		t.Fatal("expected non-nil Result")
	}
	if r.Models.Research != providerValue {
		t.Errorf("WizardResult.Models.Research = %q, want %q", r.Models.Research, providerValue)
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

	// Walk down to 5 and beyond: stays at 5
	for i := 0; i < 10; i++ {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.modelCursor != 5 {
		t.Errorf("Down × 10 at top: got modelCursor=%d, want 5 (clamped)", m.modelCursor)
	}
}

func TestWizardReviewDelegation_ModelsSubRowCycle(t *testing.T) {
	providerModels := map[string][]string{
		"claude": {"opus", "sonnet"},
	}
	m := newWizardAtReviewForDelegation(t, providerModels, []string{"claude"}, nil)
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Navigate to Implementation (row 3)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modelCursor != 3 {
		t.Fatalf("modelCursor = %d, want 3", m.modelCursor)
	}
	origImpl := m.models.Implementation
	origResearch := m.models.Research
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.configEditor.activeModelCell; got != modelCellModel {
		t.Fatalf("second Right activeModelCell = %v, want modelCellModel", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.models.Implementation == origImpl {
		t.Error("Down on Implementation model panel did not cycle Implementation")
	}
	if m.models.Research != origResearch {
		t.Error("Implementation model edit accidentally changed Research")
	}
}

func TestWizardReviewDelegation_ModelSelectionPanelsUseVerticalSelection(t *testing.T) {
	providerModels := map[string][]string{
		"claude":  {"opus", "sonnet"},
		"gateway": {"glm-5p2", "gemma4"},
	}
	m := newWizardAtReviewForDelegation(t, providerModels, []string{"claude", "gateway"}, nil)
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // Clarify -> Research

	if got := m.wizardModelFocus(); got != configFocusPhaseList {
		t.Fatalf("initial model editor focus = %v, want phase list", got)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.wizardModelFocus(); got != configFocusAgentList {
		t.Fatalf("right from phase focus = %v, want agent list", got)
	}
	m.syncConfigEditorFromWizard()
	beforeCursor := m.modelCursor
	beforeAgent := m.configEditor.agentValueForField("Research")
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.syncConfigEditorFromWizard()
	if m.modelCursor != beforeCursor {
		t.Fatalf("down in agent panel moved phase cursor to %d, want %d", m.modelCursor, beforeCursor)
	}
	if got := m.configEditor.agentValueForField("Research"); got == beforeAgent {
		t.Fatalf("down in agent panel kept agent %q, want next agent", got)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.wizardModelFocus(); got != configFocusModelList {
		t.Fatalf("right from agent focus = %v, want model list", got)
	}
	beforeCursor = m.modelCursor
	beforeModel := m.models.Research
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modelCursor != beforeCursor {
		t.Fatalf("down in model panel moved phase cursor to %d, want %d", m.modelCursor, beforeCursor)
	}
	if got := m.models.Research; got == beforeModel {
		t.Fatalf("down in model panel kept model %q, want next model", got)
	}
}

func TestWizardReviewDelegation_ModelSelectionEnterReturnsNestedPanelsToPhases(t *testing.T) {
	providerModels := map[string][]string{
		"claude":  {"opus", "sonnet"},
		"gateway": {"glm-5p2", "gemma4"},
	}
	m := newWizardAtReviewForDelegation(t, providerModels, []string{"claude", "gateway"}, nil)
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.wizardModelFocus(); got != configFocusAgentList {
		t.Fatalf("right from phase focus = %v, want agent list", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.summaryEditing {
		t.Fatal("enter from agent list closed model editing")
	}
	if got := m.wizardModelFocus(); got != configFocusPhaseList {
		t.Fatalf("enter from agent list focus = %v, want phase list", got)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.wizardModelFocus(); got != configFocusModelList {
		t.Fatalf("second right from phase focus = %v, want model list", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.summaryEditing {
		t.Fatal("enter from model list closed model editing")
	}
	if got := m.wizardModelFocus(); got != configFocusPhaseList {
		t.Fatalf("enter from model list focus = %v, want phase list", got)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.summaryEditing {
		t.Fatal("enter from phase list kept model editing open")
	}
}

func TestWizardReviewDelegation_ModelSelectionViewFitsReviewWidth(t *testing.T) {
	const terminalWidth = 160
	providerModels := map[string][]string{
		"gateway": {
			"ollama/gemma4:26b-256k[262K]",
			"ollama/gemma4:31b-256k[262K]",
			"portkey/@fireworks/accounts/fireworks/models/glm-5p2[1M]",
		},
	}
	m := newWizardAtReviewForDelegation(t, providerModels, []string{"gateway"}, nil)
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.width = terminalWidth
	m.height = 40

	view := stripANSI(m.View())
	for i, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > terminalWidth {
			t.Fatalf("line %d width = %d, want <= %d:\n%s", i+1, w, terminalWidth, line)
		}
	}
}

func TestWizardReviewDelegation_ModelSelectionViewUsesShortModelNames(t *testing.T) {
	providerModels := map[string][]string{
		"gateway": {
			"ollama/gemma4:26b-256k[262K]",
			"portkey/@fireworks/accounts/fireworks/models/glm-5p2[1M]",
		},
	}
	m := newWizardAtReviewForDelegation(t, providerModels, []string{"gateway"}, nil)
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.width = 160
	m.height = 40

	view := stripANSI(m.View())
	if strings.Contains(view, "portkey/@fireworks/accounts/fireworks/models") {
		t.Fatalf("model selection rendered routed model ID, want compact model name:\n%s", view)
	}
	if !strings.Contains(view, "glm-5p2[1M]") {
		t.Fatalf("model selection missing compact model name glm-5p2[1M]:\n%s", view)
	}
}

func TestWizardReviewDelegation_ModelSummaryUsesShortModelNames(t *testing.T) {
	const routedModel = "gateway:portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]"
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.models = config.ModelConfig{
		Research:       routedModel,
		Planning:       routedModel,
		Implementation: routedModel,
		Review:         routedModel,
		KBBuild:        routedModel,
	}
	m.summaryCursor = summaryFieldRisk
	m.summaryEditing = false
	m.width = 180
	m.height = 40

	view := stripANSI(m.View())
	if strings.Contains(view, "portkey/@fireworks/accounts/fireworks/models") {
		t.Fatalf("review summary rendered routed model ID, want compact model name:\n%s", view)
	}
	if !strings.Contains(view, "R:gateway:glm-5p2[1.04M]") {
		t.Fatalf("review summary missing compact model summary:\n%s", view)
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

	orig := m.checkpoints[checkpointInquiryReview]
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if m.checkpoints[checkpointInquiryReview] == orig {
		t.Error("Space did not toggle InquiryReview checkpoint")
	}
	if !m.checkpointsManuallySet {
		t.Error("expected checkpointsManuallySet=true after Space")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if m.checkpoints[checkpointInquiryReview] != orig {
		t.Error("second Space did not toggle back")
	}
}

func TestWizardReviewDelegation_CheckpointsTabTogglesNotJumps(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	orig := m.checkpoints[checkpointInquiryReview]
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.checkpoints[checkpointInquiryReview] == orig {
		t.Error("Tab on Checkpoints did not toggle InquiryReview checkpoint (wizard reshape failed)")
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
	if m.checkpointsCursor != checkpointManualPublish {
		t.Errorf("Down × 10 (publishable=true): got %d, want %d (clamped)", m.checkpointsCursor, checkpointManualPublish)
	}
}

func TestWizardReviewDelegation_CheckpointsManualPublishHidden_Parity(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.provisionalPublishable = false
	initManualPublish := m.checkpoints[checkpointManualPublish]
	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	for i := 0; i < 10; i++ {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.checkpointsCursor != checkpointPhasePlanReview {
		t.Errorf("Down × 10 (publishable=false): got %d, want %d (manual publish row hidden)", m.checkpointsCursor, checkpointPhasePlanReview)
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
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // agent panel -> phase panel
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
	if r.Checkpoints.InquiryReview {
		t.Errorf("Checkpoints.InquiryReview = %v, want false after toggling the visible thorough gate", r.Checkpoints.InquiryReview)
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
		"Phase",
		"Model",
		"Research",
		"Planning",
		"Implementation",
		"Review",
		"KB Build",
	}
	for _, needle := range needles {
		if !strings.Contains(view, needle) {
			t.Errorf("rendered Models editor missing %q", needle)
		}
	}
	if strings.Contains(view, "Choices for Research") {
		t.Errorf("rendered Models editor still uses old Choices copy:\n%s", view)
	}
}

func TestWizardReviewDelegation_ModelFilterEnterDoesNotCloseEditing(t *testing.T) {
	const filteredPlanning = "portkey/@fireworks/accounts/fireworks/models/glm-5p2"
	m := newWizardAtReviewForDelegation(t,
		map[string][]string{"gateway": {"vendor/sonnet[200K]", filteredPlanning}},
		[]string{"gateway"},
		map[string]map[string][]string{"Planning": {"gateway": {"vendor/sonnet[200K]", filteredPlanning}}},
	)
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.modelCursor != 2 {
		t.Fatalf("modelCursor = %d, want Planning row 2", m.modelCursor)
	}
	beforePlanning := m.models.Planning

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.configEditor.activeModelCell; got != modelCellModel {
		t.Fatalf("activeModelCell = %v, want modelCellModel", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.summaryEditing {
		t.Fatal("summaryEditing = false, want still editing after filter enter")
	}
	if m.configEditor.ModelFilteringActive() {
		t.Fatal("filter still active after enter, want accepted")
	}
	if got := m.models.Planning; got != filteredPlanning {
		t.Fatalf("Planning model = %q, want filtered model %q (before %q)", got, filteredPlanning, beforePlanning)
	}
	if !m.modelsManuallySet {
		t.Fatal("modelsManuallySet = false, want true after accepting filtered model")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.wizardModelFocus(); got != configFocusPhaseList {
		t.Fatalf("enter from model list focus = %v, want phase list", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	result := m.Result()
	if result == nil {
		t.Fatal("Result() = nil")
	}
	if got := result.Models.Planning; got != filteredPlanning {
		t.Fatalf("WizardResult.Models.Planning = %q, want %q", got, filteredPlanning)
	}
}

func TestWizardReviewDelegation_CheckpointsEditorRendersGates(t *testing.T) {
	m := newWizardAtReviewForDelegation(t, nil, nil, nil)
	m.width = 140
	m.height = 40
	m.summaryCursor = summaryFieldCheckpoints
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := m.View()
	for _, needle := range []string{"Gates", "Inquiry Review", "Research Review", "Design Review", "Roadmap Review", "Phase Plan Review"} {
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

func TestWizardReviewDelegation_EffortCellNavigation(t *testing.T) {
	providerModels := map[string][]string{
		"claude": {"opus", "sonnet"},
	}
	m := newWizardAtReviewForDelegation(t, providerModels, []string{"claude"}, nil)
	m.summaryCursor = summaryFieldModels
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // Clarify -> Research

	if got := m.wizardModelFocus(); got != configFocusPhaseList {
		t.Fatalf("initial focus = %v, want phase list", got)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.wizardModelFocus(); got != configFocusAgentList {
		t.Fatalf("right from phase = %v, want agent list", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.wizardModelFocus(); got != configFocusModelList {
		t.Fatalf("right from agent = %v, want model list", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := m.configEditor.activeModelCell; got != modelCellEffort {
		t.Fatalf("right from model activeModelCell = %v, want modelCellEffort", got)
	}
	if got := m.wizardModelFocus(); got != configFocusEffortList {
		t.Fatalf("right from model focus = %v, want effort list", got)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got := m.configEditor.activeModelCell; got != modelCellModel {
		t.Fatalf("left from effort activeModelCell = %v, want modelCellModel", got)
	}
	if got := m.wizardModelFocus(); got != configFocusModelList {
		t.Fatalf("left from effort focus = %v, want model list", got)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if got := m.configEditor.activeModelCell; got != modelCellAgent {
		t.Fatalf("shift+tab from model activeModelCell = %v, want modelCellAgent", got)
	}
}

func TestWizardReviewDelegation_EffortPaneVisibleAtConstrainedWidths(t *testing.T) {
	providerModels := map[string][]string{
		"claude": {"opus", "sonnet"},
	}
	for _, termWidth := range []int{120, 80} {
		t.Run(fmt.Sprintf("terminal_%d", termWidth), func(t *testing.T) {
			m := newWizardAtReviewForDelegation(t, providerModels, []string{"claude"}, nil)
			m.width = termWidth
			m.height = 40

			m.summaryCursor = summaryFieldModels
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // Clarify -> Research

			// Navigate Phase -> Agent -> Model -> Effort.
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
			if got := m.wizardModelFocus(); got != configFocusEffortList {
				t.Fatalf("focus = %v, want configFocusEffortList", got)
			}

			panelWidth := m.wizardPanelWidth()
			inputBoxWidth := panelWidth - 6
			contentWidth := reviewEditorContentWidth(inputBoxWidth - 4)
			rendered := m.configEditor.renderModelsWorkspaceWithFocusWidth(m.wizardModelFocus(), contentWidth)

			// Assert every rendered line fits within the content width.
			for i, line := range strings.Split(rendered, "\n") {
				stripped := ansi.Strip(line)
				if w := lipgloss.Width(stripped); w > contentWidth {
					t.Errorf("line %d width %d exceeds content width %d at %d-column terminal:\n%s", i, w, contentWidth, termWidth, stripped)
				}
			}

			snapshot := ansi.Strip(rendered)
			t.Logf("\n--- wizard rendered at %d cols (content %d) ---\n%s", termWidth, contentWidth, snapshot)

			if !strings.Contains(snapshot, "Effort") {
				t.Errorf("Effort title not visible in wizard at %d-column terminal", termWidth)
			}
			if !strings.Contains(snapshot, "Auto (high)") {
				t.Errorf("complete Auto (high) value not visible in wizard at %d-column terminal", termWidth)
			}
		})
	}
}
