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

func TestEditConfigOverlay_InquirenessCycle(t *testing.T) {
	f := &feature.Feature{
		ID:          "f1",
		Name:        "test",
		Inquireness: feature.InquirenessMedium,
	}
	editor := NewConfigEditorModel(f, testCatalog(), true)
	editor.rowCursor = editor.inquirenessRow()

	editor, _ = editor.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := editor.Snapshot().Inquireness; got != feature.InquirenessHigh {
		t.Errorf("after right from medium: got %q, want high", got)
	}
	editor, _ = editor.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := editor.Snapshot().Inquireness; got != feature.InquirenessNone {
		t.Errorf("after right from high: got %q, want none", got)
	}
	editor, _ = editor.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got := editor.Snapshot().Inquireness; got != feature.InquirenessHigh {
		t.Errorf("after left from none: got %q, want high", got)
	}
}

func TestEditConfigBehaviorPane_InquirenessCopyDescribesHarnessSurfacing(t *testing.T) {
	m := newTestEditConfigModel(&feature.Feature{
		ID:          "f1",
		Name:        "copy",
		Inquireness: feature.InquirenessMedium,
	})
	view := m.renderBehaviorPane()

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

func TestEditConfig_DiffSummaryFooter(t *testing.T) {
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
				m.editor.checkpoints.RoadmapReview = true
				m.editor.checkpoints.PhasePlanReview = true
				m.editor.inquireness = feature.InquirenessNone
			},
			want: []string{"Models: 2 changes", "Gates: 2 changes", "Inquiry: changed"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestEditConfigModel(&feature.Feature{
				ID:          "f1",
				Name:        "sum",
				Inquireness: feature.InquirenessMedium,
			})
			tc.mutate(&m)
			view := m.View()
			for _, frag := range tc.want {
				if !strings.Contains(view, frag) {
					t.Errorf("view missing %q\nview:\n%s", frag, view)
				}
			}
		})
	}
}

func TestEditConfig_StaleModel_RendersUnavailable(t *testing.T) {
	m := newTestEditConfigModel(&feature.Feature{
		ID:     "f1",
		Name:   "stale",
		Models: config.ModelConfig{Research: "this-model-does-not-exist-xyz"},
	})
	view := m.View()
	if !strings.Contains(view, "(unavailable)") {
		t.Errorf("expected (unavailable) in view for stale model; view:\n%s", view)
	}
}

func TestEditConfig_TabCyclesSegmentedTabs(t *testing.T) {
	m := newTestEditConfigModel(&feature.Feature{
		ID:          "f1",
		Name:        "tab", //nolint:goconst // arbitrary fixture name coincidentally matches the unrelated "tab" key-code literal used throughout production key handling
		Inquireness: feature.InquirenessMedium,
	})
	if m.activeTab != tabModels {
		t.Fatalf("initial activeTab = %v, want tabModels", m.activeTab)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.activeTab != tabBehavior {
		t.Errorf("after tab: activeTab = %v, want tabBehavior", m.activeTab)
	}
	if want := m.editor.inquirenessRow(); m.editor.rowCursor != want {
		t.Errorf("after tab: rowCursor = %d, want %d", m.editor.rowCursor, want)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.activeTab != tabGates {
		t.Errorf("after second tab: activeTab = %v, want tabGates", m.activeTab)
	}
	if want := m.editor.checkpointsStart(); m.editor.rowCursor != want {
		t.Errorf("after second tab: rowCursor = %d, want %d", m.editor.rowCursor, want)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.activeTab != tabModels {
		t.Errorf("after third tab: activeTab = %v, want tabModels", m.activeTab)
	}
	if m.editor.rowCursor != 0 {
		t.Errorf("after third tab: rowCursor = %d, want 0", m.editor.rowCursor)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.activeTab != tabGates {
		t.Errorf("after shift+tab: activeTab = %v, want tabGates", m.activeTab)
	}
}

func TestEditConfig_UpDownHonorsActiveBodyBounds(t *testing.T) {
	m := newTestEditConfigModel(&feature.Feature{
		ID:          "f1",
		Name:        "walk",
		Inquireness: feature.InquirenessMedium,
	})

	modelsLast := m.editor.modelsCount() - 1
	m.enterActiveTabBody()
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.focus != configFocusTabs || m.editor.rowCursor != 0 {
		t.Errorf("up from first Models row = focus %v row %d, want tabs row 0", m.focus, m.editor.rowCursor)
	}
	m.enterActiveTabBody()
	m.editor.rowCursor = modelsLast
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.editor.rowCursor != modelsLast {
		t.Errorf("down at last Models row: rowCursor = %d, want %d", m.editor.rowCursor, modelsLast)
	}

	m.activeTab = tabGates
	m.enterActiveTabBody()
	m.editor.rowCursor = m.editor.checkpointsStart()
	start := m.editor.checkpointsStart()
	last := m.editor.lastRow()
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.focus != configFocusTabs || m.editor.rowCursor != start {
		t.Errorf("up from first Gates row = focus %v row %d, want tabs row %d", m.focus, m.editor.rowCursor, start)
	}
	m.enterActiveTabBody()
	m.editor.rowCursor = last
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.editor.rowCursor != last {
		t.Errorf("down at last Gates row: rowCursor = %d, want %d", m.editor.rowCursor, last)
	}
}

func newTestEditConfigModel(f *feature.Feature) EditConfigModel {
	return NewEditConfigModel(f, testCatalog(), true)
}

func TestEditConfig_EffortPaneNavigation(t *testing.T) {
	m := newTestEditConfigModel(&feature.Feature{
		ID:   "f1",
		Name: "effort",
		Models: config.ModelConfig{
			Research: "claude/sonnet-4-6",
		},
	})
	m.enterActiveTabBody()
	m.editor.rowCursor = 1 // Research

	if m.focus != configFocusPhaseList {
		t.Fatalf("initial focus = %v, want phase list", m.focus)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.focus != configFocusAgentList {
		t.Fatalf("right from phase = %v, want agent list", m.focus)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.focus != configFocusModelList {
		t.Fatalf("right from agent = %v, want model list", m.focus)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.focus != configFocusEffortList {
		t.Fatalf("right from model = %v, want effort list", m.focus)
	}
	if got := m.editor.activeModelCell; got != modelCellEffort {
		t.Fatalf("activeModelCell = %v, want modelCellEffort", got)
	}

	before := m.editor.effortValueForField("Research")
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	after := m.editor.effortValueForField("Research")
	if before == after {
		t.Fatalf("down on effort panel did not cycle: before=%q after=%q", before, after)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.focus != configFocusModelList {
		t.Fatalf("left from effort = %v, want model list", m.focus)
	}
	if got := m.editor.activeModelCell; got != modelCellModel {
		t.Fatalf("activeModelCell = %v, want modelCellModel", got)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.focus != configFocusPhaseList {
		t.Fatalf("enter from model = %v, want phase list", m.focus)
	}
}

func TestEditConfig_WorkspaceRendersEffortPane(t *testing.T) {
	m := newTestEditConfigModel(&feature.Feature{
		ID:   "f1",
		Name: "render",
		Models: config.ModelConfig{
			Research: "claude/sonnet-4-6",
		},
	})
	m.enterActiveTabBody()
	m.editor.rowCursor = 1 // Research
	view := m.renderModelsWorkspace()
	if !strings.Contains(view, "Effort") {
		t.Errorf("workspace view missing Effort pane:\n%s", view)
	}
	if !strings.Contains(view, "Auto (") {
		t.Errorf("workspace view missing Auto display value:\n%s", view)
	}
}

// TestEditConfig_ModelsWorkspaceKeepsFullPhaseAssignments wide-viewport
// regression pin: with a 140-column terminal (matching the packaged 1200x800
// evidence captures) the phase list must show each row's complete
// provider-qualified assignment instead of ellipsizing it while the modal
// still has room. The unavailable-model variant pins the ("(unavailable)")
// suffix: it must render in full alongside the phase assignment rather than
// truncating into "Implementation (sonnet[2... (unavailable))" while the
// adjacent Agents/Models inspector panes carry unused space.
func TestEditConfig_ModelsWorkspaceKeepsFullPhaseAssignments(t *testing.T) {
	m := NewEditConfigModel(&feature.Feature{
		ID:       "f1",
		Name:     "wide",
		Pipeline: feature.PipelineLarge,
		Models: config.ModelConfig{
			Research:       "claude/sonnet-4-6",
			Implementation: "codex/gpt-5-codex",
			Utilities:      "sonnet[272K]", // absent from the catalog → unavailable
		},
	}, testWorkspaceCatalog(), true)
	m.width = 140
	m.editor.rowCursor = 3 // Implementation

	view := ansi.Strip(m.renderModelsWorkspace())
	for _, want := range []string{
		"Research (claude/claude/sonnet-4-6)",
		"Implementation (codex/codex/gpt-5-codex)",
		"Utilities (claude/sonnet[272K] (unavailable))",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("workspace view at 140 cols missing full assignment %q:\n%s", want, view)
		}
	}
}

func TestEditConfig_EffortPaneVisibleAtRepresentativeWidths(t *testing.T) {
	for _, termWidth := range []int{120, 80} {
		t.Run(fmt.Sprintf("terminal_%d", termWidth), func(t *testing.T) {
			m := NewEditConfigModel(&feature.Feature{
				ID:       "f1",
				Name:     "viewport",
				Pipeline: feature.PipelineLarge,
				Models: config.ModelConfig{
					Research: "claude/sonnet-4-6",
				},
			}, testWorkspaceCatalog(), true)
			m.width = termWidth
			m.enterActiveTabBody()
			m.editor.rowCursor = 1 // Research

			// Navigate Phase → Agent → Model → Effort.
			for range 3 {
				m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
			}
			if m.focus != configFocusEffortList {
				t.Fatalf("focus = %v, want configFocusEffortList", m.focus)
			}

			view := m.renderModelsWorkspace()
			contentWidth := termWidth - editConfigOverlayWidthOverhead

			// Assert every rendered line fits within the content width
			// (no overflow / clipped border).
			for i, line := range strings.Split(view, "\n") {
				stripped := ansi.Strip(line)
				if w := lipgloss.Width(stripped); w > contentWidth {
					t.Errorf("line %d width %d exceeds content width %d at %d-column terminal:\n%s", i, w, contentWidth, termWidth, stripped)
				}
			}

			// Assert the complete effort value and closing border are
			// visible (not truncated to "Auto (high" with no closing
			// parenthesis or border).
			snapshot := ansi.Strip(view)
			t.Logf("\n--- rendered at %d cols (content %d) ---\n%s", termWidth, contentWidth, snapshot)

			if !strings.Contains(snapshot, "Effort") {
				t.Errorf("Effort title not visible at %d-column terminal", termWidth)
			}
			if !strings.Contains(snapshot, "Auto (high)") {
				t.Errorf("complete Auto (high) value not visible at %d-column terminal", termWidth)
			}
		})
	}
}
