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

	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"

	tea "charm.land/bubbletea/v2"
)

// testCatalog returns a deterministic 3-provider catalog covering all 5 phase
// roles with 2–3 model choices each. Tests that need predictable cycling
// consume this catalog instead of a live *llm.Registry.
func testCatalog() PhaseModelCatalog {
	cat := PhaseModelCatalog{
		Fields:        []string{"Research", "Planning", "Implementation", "Review", "KB Build"},
		ProviderOrder: []string{"claude", "codex"},
		ProviderModels: map[string][]string{
			"claude": {"claude/sonnet-4-6", "claude/opus-4-7"},
			"codex":  {"codex/gpt-5-codex"},
		},
		PhaseDefaults: map[string]string{
			"Research":       "claude/sonnet-4-6",
			"Planning":       "claude/opus-4-7",
			"Implementation": "claude/sonnet-4-6",
			"Review":         "claude/sonnet-4-6",
			"KB Build":       "claude/sonnet-4-6",
		},
		PhaseProviderModels: map[string]map[string][]string{
			"Research":       {"claude": {"claude/sonnet-4-6", "claude/opus-4-7"}, "codex": {"codex/gpt-5-codex"}},
			"Planning":       {"claude": {"claude/sonnet-4-6", "claude/opus-4-7"}, "codex": {"codex/gpt-5-codex"}},
			"Implementation": {"claude": {"claude/sonnet-4-6", "claude/opus-4-7"}, "codex": {"codex/gpt-5-codex"}},
			"Review":         {"claude": {"claude/sonnet-4-6", "claude/opus-4-7"}, "codex": {"codex/gpt-5-codex"}},
			"KB Build":       {"claude": {"claude/sonnet-4-6", "claude/opus-4-7"}, "codex": {"codex/gpt-5-codex"}},
		},
	}
	return cat
}

// newEditor seeds a ConfigEditorModel backed by testCatalog().
func newEditor(f *feature.Feature, provisionalPublishable bool) ConfigEditorModel {
	if f == nil {
		f = &feature.Feature{}
	}
	return NewConfigEditorModel(f, testCatalog(), provisionalPublishable)
}

func checkpointRowForGate(t *testing.T, e ConfigEditorModel, gate feature.GateIndex) int {
	t.Helper()
	fields := e.visibleCheckpointFields()
	for i, field := range fields {
		if field.Gate == gate {
			return e.checkpointsStart() + i
		}
	}
	t.Fatalf("gate %v not visible in editor", gate)
	return 0
}

func checkpointViewContainsLabel(view, label string) bool {
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		row := strings.TrimSpace(line)
		row = strings.TrimPrefix(row, "▸ ")
		switch {
		case strings.HasPrefix(row, "[x] "):
			row = strings.TrimPrefix(row, "[x] ")
		case strings.HasPrefix(row, "[ ] "):
			row = strings.TrimPrefix(row, "[ ] ")
		default:
			continue
		}
		row = strings.TrimLeft(row, " ")
		if row == label || strings.HasPrefix(row, label+" ") {
			return true
		}
	}
	return false
}

func TestConfigEditor_RowCursor_FlatWalk(t *testing.T) {
	t.Parallel()
	e := newEditor(nil, true) // 5 Models + 1 Inquireness + 5 Checkpoints = rows 0..10
	if e.rowCursor != 0 {
		t.Fatalf("initial rowCursor = %d, want 0", e.rowCursor)
	}
	for i := 0; i < 10; i++ {
		e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if e.rowCursor != 10 {
		t.Errorf("after 10 downs: rowCursor = %d, want 10", e.rowCursor)
	}
	// One more down wraps to 0.
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if e.rowCursor != 0 {
		t.Errorf("after wrap-around down: rowCursor = %d, want 0", e.rowCursor)
	}
	// Up from 0 wraps to 10.
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if e.rowCursor != 10 {
		t.Errorf("up from 0 should wrap to 10, got %d", e.rowCursor)
	}
	for i := 0; i < 10; i++ {
		e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	if e.rowCursor != 0 {
		t.Errorf("after 10 ups from 10: rowCursor = %d, want 0", e.rowCursor)
	}
}

func TestConfigEditor_RowCursor_FlatWalk_4Checkpoints(t *testing.T) {
	t.Parallel()
	e := newEditor(nil, false) // ManualPublish hidden → 5 + 1 + 4 = rows 0..9
	if e.lastRow() != 9 {
		t.Fatalf("lastRow = %d, want 9", e.lastRow())
	}
	for i := 0; i < 9; i++ {
		e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if e.rowCursor != 9 {
		t.Errorf("after 9 downs: rowCursor = %d, want 9", e.rowCursor)
	}
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if e.rowCursor != 0 {
		t.Errorf("wrap-around: rowCursor = %d, want 0", e.rowCursor)
	}
}

func TestConfigEditor_Tab_CyclesModelsValueOnModelsRow(t *testing.T) {
	t.Parallel()
	fields := []struct {
		row    int
		getter func(feature.ConfigSnapshot) string
	}{
		{0, func(s feature.ConfigSnapshot) string { return s.Models.Research }},
		{1, func(s feature.ConfigSnapshot) string { return s.Models.Planning }},
		{2, func(s feature.ConfigSnapshot) string { return s.Models.Implementation }},
		{3, func(s feature.ConfigSnapshot) string { return s.Models.Review }},
		{4, func(s feature.ConfigSnapshot) string { return s.Models.KBBuild }},
	}
	// Seed each Models field to a known valid value so the reversibility
	// assertion exercises normal cycling (not stale-model preservation).
	seed := config.ModelConfig{
		Research:       "claude/sonnet-4-6",
		Planning:       "claude/sonnet-4-6",
		Implementation: "claude/sonnet-4-6",
		Review:         "claude/sonnet-4-6",
		KBBuild:        "claude/sonnet-4-6",
	}
	for _, tt := range fields {
		e := newEditor(&feature.Feature{Models: seed}, true)
		e.rowCursor = tt.row
		origRow := e.rowCursor
		before := tt.getter(e.Snapshot())

		e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		if after := tt.getter(e.Snapshot()); after == before {
			t.Errorf("row %d: tab did not advance model (before=%q, after=%q)", tt.row, before, after)
		}
		if e.rowCursor != origRow {
			t.Errorf("row %d: tab should not move the cursor, got %d", tt.row, e.rowCursor)
		}

		// shift+tab reverses back to the original value.
		e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		if after := tt.getter(e.Snapshot()); after != before {
			t.Errorf("row %d: shift+tab should undo tab (want %q, got %q)", tt.row, before, after)
		}
	}
}

func TestConfigEditor_Tab_JumpsEditorsFromNonModelsRow(t *testing.T) {
	t.Parallel()
	e := newEditor(nil, true)
	inq := e.inquirenessRow()
	cpStart := e.checkpointsStart()

	// From Inquireness, tab → first Checkpoint row.
	e.rowCursor = inq
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if e.rowCursor != cpStart {
		t.Errorf("tab from Inquireness: got %d, want %d", e.rowCursor, cpStart)
	}

	// From first Checkpoint row, tab wraps to first Models row (0).
	e.rowCursor = cpStart
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if e.rowCursor != 0 {
		t.Errorf("tab from Checkpoint: got %d, want 0", e.rowCursor)
	}

	// From Inquireness, shift+tab → last Models row (inq - 1).
	e.rowCursor = inq
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if e.rowCursor != inq-1 {
		t.Errorf("shift+tab from Inquireness: got %d, want %d", e.rowCursor, inq-1)
	}

	// From first Checkpoint, shift+tab → Inquireness.
	e.rowCursor = cpStart
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if e.rowCursor != inq {
		t.Errorf("shift+tab from Checkpoint: got %d, want %d", e.rowCursor, inq)
	}
}

func TestConfigEditor_ModelsCycleForward(t *testing.T) {
	t.Parallel()
	e := newEditor(nil, true)
	opts := e.catalog.ModelOptionsForField("Research")
	if len(opts) < 2 {
		t.Fatalf("catalog has too few Research options: %v", opts)
	}
	e.rowCursor = 0
	// Seed to the first option.
	e.models.Research = opts[0]
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := e.Snapshot().Models.Research; got != opts[1] {
		t.Errorf("→ from %q: got %q, want %q", opts[0], got, opts[1])
	}
	// Cycle to the last option and verify wrap-to-first.
	e.models.Research = opts[len(opts)-1]
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := e.Snapshot().Models.Research; got != opts[0] {
		t.Errorf("→ wrap: got %q, want %q", got, opts[0])
	}
}

func TestConfigEditor_ModelsCycleBackward(t *testing.T) {
	t.Parallel()
	e := newEditor(nil, true)
	opts := e.catalog.ModelOptionsForField("Research")
	e.rowCursor = 0
	e.models.Research = opts[0]
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got := e.Snapshot().Models.Research; got != opts[len(opts)-1] {
		t.Errorf("← from first: got %q, want %q", got, opts[len(opts)-1])
	}
}

func TestConfigEditor_InquirenessCycle(t *testing.T) {
	t.Parallel()
	e := newEditor(&feature.Feature{Inquireness: feature.InquirenessMedium}, true)
	e.rowCursor = e.inquirenessRow()

	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := e.Snapshot().Inquireness; got != feature.InquirenessHigh {
		t.Errorf("medium → high: got %q", got)
	}
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := e.Snapshot().Inquireness; got != feature.InquirenessNone {
		t.Errorf("high → none (wrap): got %q", got)
	}
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got := e.Snapshot().Inquireness; got != feature.InquirenessHigh {
		t.Errorf("none ← (wrap): got %q", got)
	}
}

func TestConfigEditor_InquirenessCopyDescribesHarnessSurfacing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		inquireness feature.Inquireness
		want        string
	}{
		{"none", feature.InquirenessNone, "Harness keeps planning questions hidden unless manual input is required"},
		{"medium", feature.InquirenessMedium, "Harness surfaces key planning questions"},
		{"high", feature.InquirenessHigh, "Harness surfaces more planning questions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEditor(&feature.Feature{Inquireness: tt.inquireness}, true)
			e.rowCursor = e.inquirenessRow()
			view := e.renderInquirenessBox()
			if !strings.Contains(view, tt.want) {
				t.Fatalf("renderInquirenessBox() missing %q:\n%s", tt.want, view)
			}
			for _, forbidden := range []string{
				"Agent works autonomously",
				"Agent pauses at key decisions",
				"Agent asks before every major step",
			} {
				if strings.Contains(view, forbidden) {
					t.Fatalf("renderInquirenessBox() still contains agent-owned copy %q:\n%s", forbidden, view)
				}
			}
		})
	}
}

func TestConfigEditor_CheckpointsToggle(t *testing.T) {
	t.Parallel()
	type check struct {
		name string
		gate feature.GateIndex
		get  func(feature.Checkpoints) bool
	}
	toggles := []check{
		{"InquiryReview", feature.GateInquiryReview, func(c feature.Checkpoints) bool { return c.InquiryReview }},
		{"ResearchReview", feature.GateResearchReview, func(c feature.Checkpoints) bool { return c.ResearchReview }},
		{"DesignReview", feature.GateDesignReview, func(c feature.Checkpoints) bool { return c.DesignReview }},
		{"RoadmapReview", feature.GateRoadmapReview, func(c feature.Checkpoints) bool { return c.RoadmapReview }},
	}
	for _, tc := range toggles {
		t.Run(tc.name, func(t *testing.T) {
			e := newEditor(&feature.Feature{Pipeline: feature.PipelineMoonshot}, true)
			e.rowCursor = checkpointRowForGate(t, e, tc.gate)
			before := tc.get(e.Snapshot().Checkpoints)
			e, _ = e.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
			if after := tc.get(e.Snapshot().Checkpoints); after == before {
				t.Errorf("space did not toggle %s", tc.name)
			}
			e, _ = e.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
			if after := tc.get(e.Snapshot().Checkpoints); after != before {
				t.Errorf("second space did not restore %s", tc.name)
			}
		})
	}
}

func TestConfigEditor_CheckpointsToggle_ManualPublishHidden(t *testing.T) {
	t.Parallel()
	e := newEditor(&feature.Feature{Pipeline: feature.PipelineMoonshot}, false) // ManualPublish hidden
	e.rowCursor = checkpointRowForGate(t, e, feature.GateRoadmapReview)
	before := e.Snapshot().Checkpoints
	e, _ = e.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	after := e.Snapshot().Checkpoints
	if after.RoadmapReview == before.RoadmapReview {
		t.Errorf("space on last visible row did not toggle RoadmapReview")
	}
	// ManualPublish remains forced on regardless of internal state.
	if !after.ManualPublish {
		t.Errorf("ManualPublish should be forced true when publishable=false, got %v", after.ManualPublish)
	}
}

func TestConfigEditor_CheckpointRowsFollowPipelineProfile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		feature     *feature.Feature
		publishable bool
		wantRows    []string
		wantHidden  []string
		wantCP      feature.Checkpoints
	}{
		{
			name: "medium shows roadmap phase-plan and manual publish",
			feature: &feature.Feature{
				Pipeline: feature.PipelineMedium,
				Checkpoints: feature.Checkpoints{
					RoadmapReview:   true,
					PhasePlanReview: true,
					ManualPublish:   true,
				},
			},
			publishable: true,
			wantRows:    []string{"Roadmap Review", "Phase Plan Review", "Manual Publish"},
			wantHidden:  []string{"Inquiry Review", "Research Review", "Design Review", "Plan Review"},
			wantCP:      feature.Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
		},
		{
			name: "unpublished large hides manual publish row but keeps every review gate",
			feature: &feature.Feature{
				Pipeline: feature.PipelineLarge,
				Checkpoints: feature.Checkpoints{
					DesignReview:    true,
					RoadmapReview:   true,
					PhasePlanReview: true,
					ManualPublish:   false,
				},
			},
			publishable: false,
			wantRows:    []string{"Inquiry Review", "Research Review", "Design Review", "Roadmap Review", "Phase Plan Review"},
			wantHidden:  []string{"Manual Publish", "Plan Review"},
			wantCP:      feature.Checkpoints{DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEditor(tt.feature, tt.publishable)
			view := e.renderCheckpointsBox(120)
			for _, label := range tt.wantRows {
				if !checkpointViewContainsLabel(view, label) {
					t.Fatalf("renderCheckpointsBox missing %q\n%s", label, view)
				}
			}
			for _, label := range tt.wantHidden {
				if checkpointViewContainsLabel(view, label) {
					t.Fatalf("renderCheckpointsBox unexpectedly contained %q\n%s", label, view)
				}
			}
			if got := e.Snapshot().Checkpoints; got != tt.wantCP {
				t.Fatalf("Snapshot checkpoints = %+v, want %+v", got, tt.wantCP)
			}
		})
	}
}

func TestConfigEditor_PhasePlanReviewVisibilityFollowsRoadmapReview(t *testing.T) {
	t.Parallel()

	e := newEditor(&feature.Feature{
		Pipeline:    feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	}, true)

	view := e.renderCheckpointsBox(120)
	if strings.Contains(view, "Phase Plan Review") {
		t.Fatalf("phase plan review should be hidden when both planning gates are off:\n%s", view)
	}

	e.rowCursor = checkpointRowForGate(t, e, feature.GateRoadmapReview)
	e, _ = e.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	cp := e.Snapshot().Checkpoints
	if !cp.RoadmapReview || !cp.PhasePlanReview {
		t.Fatalf("turning roadmap review on should restore phase plan review on, got %+v", cp)
	}

	e, _ = e.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	cp = e.Snapshot().Checkpoints
	if cp.RoadmapReview || cp.PhasePlanReview {
		t.Fatalf("turning roadmap review off should turn phase plan review off, got %+v", cp)
	}
}

func TestConfigEditor_PhasePlanOnlyManualConfigIsVisible(t *testing.T) {
	t.Parallel()

	e := newEditor(&feature.Feature{
		Pipeline: feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{
			PhasePlanReview: true,
			ManualPublish:   true,
		},
	}, true)

	view := e.renderCheckpointsBox(120)
	if !strings.Contains(view, "Phase Plan Review") {
		t.Fatalf("manual phase-plan-only config should be visible:\n%s", view)
	}
}

func TestConfigEditor_PhasePlanOnlyUnpublishedToggleClampsCursor(t *testing.T) {
	t.Parallel()

	e := newEditor(&feature.Feature{
		Pipeline: feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{
			PhasePlanReview: true,
		},
	}, false)

	e.rowCursor = checkpointRowForGate(t, e, feature.GatePhasePlanReview)
	e, _ = e.Update(tea.KeyPressMsg{Code: ' ', Text: " "})

	if e.rowCursor > e.lastRow() {
		t.Fatalf("rowCursor should be clamped after hiding phase plan review: got rowCursor=%d lastRow=%d", e.rowCursor, e.lastRow())
	}
	if want := checkpointRowForGate(t, e, feature.GateRoadmapReview); e.rowCursor != want {
		t.Fatalf("rowCursor should land on roadmap review after phase plan row hides: got %d, want %d", e.rowCursor, want)
	}
}

func TestConfigEditor_StaleModelPreservation(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Models: config.ModelConfig{Research: "retired/model"},
	}
	e := newEditor(f, true)
	// Pre-cycle: stale value is preserved exactly.
	if got := e.Snapshot().Models.Research; got != "retired/model" {
		t.Errorf("stale value dropped at construction: got %q", got)
	}
	// Renderer marks it (unavailable).
	view := e.renderModelsBox(120)
	if !strings.Contains(view, "(unavailable)") {
		t.Errorf("renderer missing (unavailable) marker; view:\n%s", view)
	}
	// Forward cycle lands on the first eligible, dropping the stale value.
	e.rowCursor = 0
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	opts := e.catalog.ModelOptionsForField("Research")
	if got := e.Snapshot().Models.Research; got != opts[0] {
		t.Errorf("forward cycle from stale: got %q, want first eligible %q", got, opts[0])
	}
}

func TestConfigEditor_HasChanges(t *testing.T) {
	t.Parallel()
	e := newEditor(&feature.Feature{Inquireness: feature.InquirenessMedium}, true)
	if e.HasChanges() {
		t.Fatal("fresh editor should have no changes")
	}
	// Inquireness flip then back.
	e.rowCursor = e.inquirenessRow()
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if !e.HasChanges() {
		t.Error("expected HasChanges=true after Inquireness flip")
	}
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if e.HasChanges() {
		t.Error("expected HasChanges=false after Inquireness cycle back")
	}
	// Checkpoint toggle.
	e.rowCursor = e.checkpointsStart()
	e, _ = e.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if !e.HasChanges() {
		t.Error("expected HasChanges=true after checkpoint toggle")
	}
	e, _ = e.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if e.HasChanges() {
		t.Error("expected HasChanges=false after checkpoint toggle back")
	}
}

func TestConfigEditor_ChangeCounters(t *testing.T) {
	t.Parallel()
	e := newEditor(&feature.Feature{Inquireness: feature.InquirenessMedium}, true)
	// Cycle Research and Planning rows forward.
	e.rowCursor = 0
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	e.rowCursor = 1
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := e.ModelsChangeCount(); got != 2 {
		t.Errorf("ModelsChangeCount = %d, want 2", got)
	}
	if e.CheckpointsChangeCount() != 0 {
		t.Errorf("CheckpointsChangeCount = %d, want 0", e.CheckpointsChangeCount())
	}
	if e.InquirenessChanged() {
		t.Error("InquirenessChanged should be false here")
	}

	// Flip Inquireness and toggle one checkpoint.
	e.rowCursor = e.inquirenessRow()
	e, _ = e.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	e.rowCursor = e.checkpointsStart()
	e, _ = e.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if !e.InquirenessChanged() {
		t.Error("InquirenessChanged should be true after flip")
	}
	if got := e.CheckpointsChangeCount(); got != 1 {
		t.Errorf("CheckpointsChangeCount = %d, want 1", got)
	}
}

func TestConfigEditor_Snapshot_ManualPublishForced(t *testing.T) {
	t.Parallel()
	// provisionalPublishable=false → ManualPublish forced true regardless of
	// the internal Checkpoints struct.
	e := newEditor(nil, false)
	e.checkpoints.ManualPublish = false
	if !e.Snapshot().Checkpoints.ManualPublish {
		t.Error("Snapshot should force ManualPublish=true when !provisionalPublishable")
	}

	// provisionalPublishable=true → pass through.
	e2 := newEditor(nil, true)
	e2.checkpoints.ManualPublish = false
	if e2.Snapshot().Checkpoints.ManualPublish {
		t.Error("Snapshot should not force ManualPublish=true when provisionalPublishable=true")
	}
}
