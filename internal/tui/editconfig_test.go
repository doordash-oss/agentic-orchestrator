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
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestEditConfigOverlay_InquirenessCycle(t *testing.T) {
	f := &feature.Feature{
		ID:          "f1",
		Name:        "test",
		Inquireness: feature.InquirenessMedium,
	}
	cat := BuildPhaseModelCatalog(nil, config.DefaultsConfig{})
	editor := NewConfigEditorModel(f, cat, true)
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

func TestIsFeatureQuiescent(t *testing.T) {
	tests := []struct {
		name string
		f    *feature.Feature
		want bool
	}{
		{"nil", nil, false},
		{"failed", &feature.Feature{Status: feature.StatusFailed}, true},
		{"created", &feature.Feature{Status: feature.StatusCreated}, true},
		{"code-ready", &feature.Feature{Status: feature.StatusCodeReady}, true},
		{"implementing", &feature.Feature{Status: feature.StatusImplementing}, false}, //nolint:goconst // test case label coincidentally matches an unrelated presentationStatus value
		{"plan-needs-review", &feature.Feature{Status: feature.StatusPlanNeedsReview}, false},
		{
			"active repo cycle",
			&feature.Feature{
				Status: feature.StatusPublished,
				RepoCycles: map[string]*feature.RepoCycleState{
					"a": {Status: feature.RepoCycleRunning},
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
	return NewEditConfigModel(f, BuildPhaseModelCatalog(nil, config.DefaultsConfig{}), true)
}
