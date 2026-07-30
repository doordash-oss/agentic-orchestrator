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
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// historyChild returns a compact closed-child relationship record as the
// server's child_history projection would carry it.
func historyChild(id, name, outcome string, startedAt, closedAt time.Time, cost float64) server.RelationshipChild {
	return server.RelationshipChild{
		ID:                id,
		Name:              name,
		Kind:              "refactor",
		DisplayToken:      "refactor:" + id,
		DisplayState:      "Closed — " + outcome,
		Pipeline:          "moonshot",
		Status:            "codeready",
		Outcome:           server.RelationshipChildOutcome(strings.ToLower(outcome)),
		RelationshipState: strings.ToLower(outcome),
		StartedAt:         startedAt,
		ClosedAt:          &closedAt,
		Cost:              server.Cost{ByPhase: map[string]float64{}, TotalUSD: cost},
		Attention:         []server.RelationshipAttention{},
		CleanupWarnings:   []server.RelationshipCleanupWarning{},
	}
}

// historyParentApp builds an APIAppModel whose single parent carries the
// given closed-child history in its summary projection.
func historyParentApp(parent server.FeatureSummary, history []server.RelationshipChild) APIAppModel {
	parent.ChildHistory = history
	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{parent}},
		featureDetails: map[string]server.FeatureDetailResponse{
			parent.ID: {Feature: apiTestFeatureDetail(parent)},
		},
		refactorHistoryExpanded: map[string]bool{},
	}
	app.selectedFeature = parent.ID
	app.rebuildPresentation(parent.ID)
	return app
}

func TestRefactorHistoryGroupCollapsedByDefault(t *testing.T) {
	t.Parallel()

	now := time.Now()
	parent := server.FeatureSummary{ID: "parent-h1", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	history := []server.RelationshipChild{
		historyChild("child-new", "new-child", "Completed", now.Add(-2*time.Hour), now.Add(-time.Hour), 1.25),
		historyChild("child-old", "old-child", "Discarded", now.Add(-24*time.Hour), now.Add(-20*time.Hour), 0.50),
	}
	app := historyParentApp(parent, history)

	dash := app.apiDashboardModel()
	var groupRow, childRows int
	for _, item := range dash.visibleItems {
		switch item.kind {
		case listItemRefactorHistory:
			groupRow++
			if item.parentID != "parent-h1" || item.closedCount != 2 {
				t.Fatalf("group row = %+v, want parent parent-h1 with count 2", item)
			}
		case listItemClosedChildFeature:
			childRows++
		}
	}
	if groupRow != 1 {
		t.Fatalf("visibleItems carry %d history group rows, want exactly 1", groupRow)
	}
	if childRows != 0 {
		t.Fatalf("collapsed group rendered %d closed child rows, want 0", childRows)
	}
	// The group row sits immediately beneath the parent row.
	parentIdx, groupIdx := -1, -1
	for i, item := range dash.visibleItems {
		if item.kind == listItemFeature && item.feature != nil && item.feature.ID == "parent-h1" {
			parentIdx = i
		}
		if item.kind == listItemRefactorHistory {
			groupIdx = i
		}
	}
	if parentIdx < 0 || groupIdx != parentIdx+1 {
		t.Fatalf("group row index = %d, want directly beneath parent at %d", groupIdx, parentIdx)
	}
	if view := stripANSI(dash.View()); !strings.Contains(view, "Refactor History") || !strings.Contains(view, "(2)") {
		t.Fatalf("dashboard missing collapsed history group in:\n%s", view)
	}
}

func TestRefactorHistoryGroupExpandsNewestFirst(t *testing.T) {
	t.Parallel()

	now := time.Now()
	parent := server.FeatureSummary{ID: "parent-h2", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	history := []server.RelationshipChild{
		historyChild("child-new", "new-child", "Completed", now.Add(-2*time.Hour), now.Add(-time.Hour), 1.25),
		historyChild("child-old", "old-child", "Discarded", now.Add(-24*time.Hour), now.Add(-20*time.Hour), 0.50),
	}
	app := historyParentApp(parent, history)
	app.refactorHistoryExpanded["parent-h2"] = true

	dash := app.apiDashboardModel()
	var closed []listItem
	for _, item := range dash.visibleItems {
		if item.kind == listItemClosedChildFeature {
			closed = append(closed, item)
		}
	}
	if len(closed) != 2 {
		t.Fatalf("expanded group rendered %d closed child rows, want 2", len(closed))
	}
	for i, item := range closed {
		if i > 0 && closed[i-1].feature.ID == item.feature.ID {
			t.Fatalf("closed child %q rendered twice", item.feature.ID)
		}
	}
	if closed[0].feature.ID != "child-new" || closed[1].feature.ID != "child-old" {
		t.Fatalf("closed order = %q, %q, want authoritative newest-first child-new, child-old",
			closed[0].feature.ID, closed[1].feature.ID)
	}
	view := stripANSI(dash.View())
	for _, marker := range []string{"Completed", "Discarded", "$1.2", "$0.5"} {
		if !strings.Contains(view, marker) {
			t.Fatalf("expanded history view missing %q in:\n%s", marker, view)
		}
	}
}

func TestRefactorHistoryExpansionIndependentPerParent(t *testing.T) {
	t.Parallel()

	now := time.Now()
	parentA := server.FeatureSummary{ID: "parent-a", Name: "Parent A", Slug: "parent-a", Status: "published", CreatedAt: now.Add(-72 * time.Hour)}
	parentB := server.FeatureSummary{ID: "parent-b", Name: "Parent B", Slug: "parent-b", Status: "created", CreatedAt: now}
	parentA.ChildHistory = []server.RelationshipChild{
		historyChild("child-a1", "child-a1", "Completed", now.Add(-4*time.Hour), now.Add(-3*time.Hour), 1.00),
	}
	parentB.ChildHistory = []server.RelationshipChild{
		historyChild("child-b1", "child-b1", "Discarded", now.Add(-2*time.Hour), now.Add(-time.Hour), 2.00),
	}
	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{parentA, parentB}},
		featureDetails: map[string]server.FeatureDetailResponse{
			"parent-a": {Feature: apiTestFeatureDetail(parentA)},
			"parent-b": {Feature: apiTestFeatureDetail(parentB)},
		},
		refactorHistoryExpanded: map[string]bool{"parent-a": true},
	}
	app.selectedFeature = "parent-b"
	app.rebuildPresentation("parent-b")

	dash := app.apiDashboardModel()
	visible := map[string]int{}
	for _, item := range dash.visibleItems {
		if item.kind == listItemClosedChildFeature && item.feature != nil {
			visible[item.feature.ID]++
		}
	}
	if visible["child-a1"] != 1 {
		t.Fatalf("expanded parent-a rendered child-a1 %d times, want 1", visible["child-a1"])
	}
	if visible["child-b1"] != 0 {
		t.Fatalf("collapsed parent-b leaked child-b1 %d times, want 0", visible["child-b1"])
	}
}

func TestRefactorHistorySelectionSurvivesRefresh(t *testing.T) {
	t.Parallel()

	now := time.Now()
	parent := server.FeatureSummary{ID: "parent-h4", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	history := []server.RelationshipChild{
		historyChild("child-new", "new-child", "Completed", now.Add(-2*time.Hour), now.Add(-time.Hour), 1.25),
		historyChild("child-old", "old-child", "Discarded", now.Add(-24*time.Hour), now.Add(-20*time.Hour), 0.50),
	}
	app := historyParentApp(parent, history)
	app.refactorHistoryExpanded["parent-h4"] = true

	// Select the older closed child without a cached detail record: the
	// summary projection alone must keep the selection stable.
	app.selectedFeature = "child-old"
	app.selectedHistoryGroup = ""
	app.rebuildPresentation("child-old")

	for i := 0; i < 3; i++ {
		if app.selectedFeature != "child-old" {
			t.Fatalf("refresh %d: selectedFeature = %q, want child-old", i, app.selectedFeature)
		}
		dash := app.apiDashboardModel()
		if dash.cursor < 0 || dash.cursor >= len(dash.visibleItems) {
			t.Fatalf("refresh %d: cursor %d out of range", i, dash.cursor)
		}
		item := dash.visibleItems[dash.cursor]
		if item.kind != listItemClosedChildFeature || item.feature.ID != "child-old" {
			t.Fatalf("refresh %d: selected item = %+v, want closed child row child-old", i, item)
		}
		// Reorder the history (reordering tolerance): selection persists by ID.
		app.featureList.Features[0].ChildHistory = []server.RelationshipChild{history[1], history[0]}
		app.rebuildPresentation(app.selectedFeature)
	}
}

func TestRefactorHistoryCollapseFallsBackToParent(t *testing.T) {
	t.Parallel()

	now := time.Now()
	parent := server.FeatureSummary{ID: "parent-h5", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	history := []server.RelationshipChild{
		historyChild("child-h5", "child-h5", "Completed", now.Add(-2*time.Hour), now.Add(-time.Hour), 1.25),
	}
	app := historyParentApp(parent, history)
	app.refactorHistoryExpanded["parent-h5"] = true
	app.selectedFeature = "child-h5"
	app.rebuildPresentation("child-h5")
	if app.selectedFeature != "child-h5" {
		t.Fatalf("expanded selection = %q, want child-h5", app.selectedFeature)
	}

	// Collapse the group: selection returns deterministically to the parent.
	app.refactorHistoryExpanded["parent-h5"] = false
	app.rebuildPresentation("child-h5")
	if app.selectedFeature != "parent-h5" {
		t.Fatalf("collapsed selection = %q, want parent fallback parent-h5", app.selectedFeature)
	}
}

func TestRefactorHistoryRemovedChildFallsBackToParent(t *testing.T) {
	t.Parallel()

	now := time.Now()
	parent := server.FeatureSummary{ID: "parent-h6", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	history := []server.RelationshipChild{
		historyChild("child-h6", "child-h6", "Discarded", now.Add(-2*time.Hour), now.Add(-time.Hour), 1.25),
	}
	app := historyParentApp(parent, history)
	app.refactorHistoryExpanded["parent-h6"] = true
	app.selectedFeature = "child-h6"
	app.rebuildPresentation("child-h6")

	// The child leaves the authoritative history (e.g. cascade/removal):
	// selection falls back to the parent.
	app.featureList.Features[0].ChildHistory = nil
	app.rebuildPresentation("child-h6")
	if app.selectedFeature != "parent-h6" {
		t.Fatalf("removed-child selection = %q, want parent fallback parent-h6", app.selectedFeature)
	}
}

func TestRefactorHistoryActiveChildStaysVisibleAboveGroup(t *testing.T) {
	t.Parallel()

	now := time.Now()
	parent := server.FeatureSummary{ID: "parent-h7", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	parent.ChildHistory = []server.RelationshipChild{
		historyChild("child-h7-old", "old-child", "Completed", now.Add(-24*time.Hour), now.Add(-20*time.Hour), 0.50),
	}
	activeChild := server.FeatureDetailDTO{
		ID: "child-h7-active", Name: "active-child", Slug: "active-child",
		Status: "implementing", CurrentPhase: "implement",
		ParentID: "parent-h7", ParentKind: "refactor", Pipeline: "moonshot",
		CreatedAt: now.Add(-time.Hour),
	}
	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{parent}},
		featureDetails: map[string]server.FeatureDetailResponse{
			"parent-h7":       {Feature: apiTestFeatureDetail(parent)},
			"child-h7-active": {Feature: activeChild},
		},
		refactorHistoryExpanded: map[string]bool{},
	}
	app.selectedFeature = "parent-h7"
	app.rebuildPresentation("parent-h7")

	dash := app.apiDashboardModel()
	kinds := []listItemKind{}
	for _, item := range dash.visibleItems {
		if item.kind == listItemFeature || item.kind == listItemChildFeature || item.kind == listItemRefactorHistory {
			kinds = append(kinds, item.kind)
		}
	}
	// Order: parent row, active child row, collapsed history group.
	wantKinds := []listItemKind{listItemFeature, listItemChildFeature, listItemRefactorHistory}
	if len(kinds) < 3 || kinds[0] != wantKinds[0] || kinds[1] != wantKinds[1] || kinds[2] != wantKinds[2] {
		t.Fatalf("row order kinds = %v, want parent → active child → history group", kinds)
	}
	if view := stripANSI(dash.View()); !strings.Contains(view, "active-child") ||
		!strings.Contains(view, "Refactor History") || !strings.Contains(view, "(1)") {
		t.Fatalf("view missing active child above collapsed history group in:\n%s", view)
	}
}

func TestClosedChildDetailIsReadOnly(t *testing.T) {
	t.Parallel()

	now := time.Now()
	closedAt := now.Add(-time.Hour)
	closedDetail := server.FeatureDetailDTO{
		ID: "child-h8", Name: "child-h8", Slug: "child-h8",
		// Pipeline "medium" pins that the Rewind & Upgrade lightbulb panel,
		// which only renders for non-moonshot features, stays suppressed on
		// the read-only closed-child surface.
		Status: "codeready", CurrentPhase: "implement", Pipeline: "medium",
		ParentID: "parent-h8", ParentKind: "refactor",
		CloseOutcome: feature.ChildCloseOutcomeCompleted,
		ClosedAt:     &closedAt,
		CreatedAt:    now.Add(-2 * time.Hour),
		Actions: []server.ActionDTO{
			{ID: actionIDDiscard, Enabled: false, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}, DisabledReasons: []server.ActionDisabledReasonDTO{
				{Code: "relationship_closed", Message: "the child relationship is closed; automatic reconciliation owns any remaining cleanup"},
			}},
		},
	}
	parentH8 := server.FeatureSummary{ID: "parent-h8", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	parentH8.ChildHistory = []server.RelationshipChild{
		historyChild("child-h8", "child-h8", "Completed", now.Add(-2*time.Hour), closedAt, 1.25),
	}
	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{parentH8}},
		featureDetails: map[string]server.FeatureDetailResponse{
			"child-h8": {Feature: closedDetail},
		},
		refactorHistoryExpanded: map[string]bool{"parent-h8": true},
	}
	app.selectedFeature = "child-h8"
	app.rebuildPresentation("child-h8")

	dash := app.apiDashboardModel()
	dash.focusPanel = 1
	view := stripANSI(dash.View())
	if !strings.Contains(view, "Closed — Completed") {
		t.Fatalf("closed child detail missing Closed — Completed banner in:\n%s", view)
	}
	// No mutation copy may appear anywhere on the closed-child detail
	// surface, body included — not just the footer.
	for _, forbidden := range []string{
		"Rewind & Upgrade", "ctrl+r", "Press ctrl+r",
		"[d] Discard", "[r] Restart", "[s] Stop", "[e] Edit config", "[Shift+E] Workspace Config",
	} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("closed child detail shows mutation affordance %q in:\n%s", forbidden, view)
		}
	}
	footer := stripANSI(dash.renderFooter())
	for _, forbidden := range []string{"[d] Discard", "[d] Delete", "[r] Restart", "[s] Stop", "[ctrl+r] Rewind", "[e] Edit config", "[Shift+E] Workspace Config"} {
		if strings.Contains(footer, forbidden) {
			t.Fatalf("closed child footer shows mutation affordance %q in: %s", forbidden, footer)
		}
	}
	if !strings.Contains(footer, "[l] Logs") {
		t.Fatalf("closed child footer missing inspection controls in: %s", footer)
	}
}

// TestClosedChildDetailIsReadOnlyWithFocusedFooter pins the exact inspection
// hints shown for a selected closed child: Logs, Diff, Back — and nothing
// else actionable.
func TestClosedChildDetailIsReadOnlyWithFocusedFooter(t *testing.T) {
	t.Parallel()

	now := time.Now()
	closedAt := now.Add(-time.Hour)
	closedDetail := server.FeatureDetailDTO{
		ID: "child-h8f", Name: "child-h8f", Slug: "child-h8f",
		Status: "codeready", CurrentPhase: "implement", Pipeline: "moonshot",
		ParentID: "parent-h8f", ParentKind: "refactor",
		CloseOutcome: feature.ChildCloseOutcomeCompleted,
		ClosedAt:     &closedAt,
		CreatedAt:    now.Add(-2 * time.Hour),
	}
	parentH8f := server.FeatureSummary{ID: "parent-h8f", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	parentH8f.ChildHistory = []server.RelationshipChild{
		historyChild("child-h8f", "child-h8f", "Completed", now.Add(-2*time.Hour), closedAt, 1.25),
	}
	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{parentH8f}},
		featureDetails: map[string]server.FeatureDetailResponse{
			"child-h8f": {Feature: closedDetail},
		},
		refactorHistoryExpanded: map[string]bool{"parent-h8f": true},
	}
	app.selectedFeature = "child-h8f"
	app.rebuildPresentation("child-h8f")

	dash := app.apiDashboardModel()
	dash.focusPanel = 1
	footer := stripANSI(dash.renderFooter())
	for _, want := range []string{"[l] Logs", "[v] Diff", "[←/esc] Back"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("closed child footer missing inspection hint %q in: %s", want, footer)
		}
	}
	for _, forbidden := range []string{"[Shift+E] Workspace Config", "[d] Discard", "[Shift+F] Refactor", "[p] Publish"} {
		if strings.Contains(footer, forbidden) {
			t.Fatalf("closed child footer shows %q in: %s", forbidden, footer)
		}
	}
}

// TestClosedChildPreservedDiffOpensReadOnly pins opening the preserved diff
// snapshot for a closed child instead of rejecting the diff action.
func TestClosedChildPreservedDiffOpensReadOnly(t *testing.T) {
	t.Parallel()

	now := time.Now()
	closedAt := now.Add(-time.Hour)
	entry := historyChild("child-h8d", "child-h8d", "Completed", now.Add(-2*time.Hour), closedAt, 1.25)
	entry.DiffSummary = "diff --git a/api.go b/api.go\n+preserved line\n"
	parentH8d := server.FeatureSummary{ID: "parent-h8d", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	parentH8d.ChildHistory = []server.RelationshipChild{entry}
	app := APIAppModel{
		featureList:             server.FeatureListResponse{Features: []server.FeatureSummary{parentH8d}},
		featureDetails:          map[string]server.FeatureDetailResponse{},
		refactorHistoryExpanded: map[string]bool{"parent-h8d": true},
	}
	app.selectedFeature = "child-h8d"
	app.rebuildPresentation("child-h8d")

	updated, cmd := app.openSelectedDiff()
	if cmd != nil {
		t.Fatal("preserved diff returned an async command; preserved content is immediate")
	}
	if updated.diffReview == nil {
		t.Fatal("openSelectedDiff did not open the diff review for a closed child")
	}
	if updated.diffReviewTitle != diffReviewPreservedTitle {
		t.Fatalf("diffReviewTitle = %q, want %q", updated.diffReviewTitle, diffReviewPreservedTitle)
	}
	if !strings.Contains(ansi.Strip(updated.diffReview.View()), "preserved line") {
		t.Fatalf("preserved diff view missing preserved content in:\n%s", ansi.Strip(updated.diffReview.View()))
	}
}

// TestClosedChildWithoutPreservedDiffReportsStatus pins the status message
// shown when a closed child was closed without a preserved diff.
func TestClosedChildWithoutPreservedDiffReportsStatus(t *testing.T) {
	t.Parallel()

	now := time.Now()
	closedAt := now.Add(-time.Hour)
	parentNoDiff := server.FeatureSummary{ID: "parent-h8e", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	parentNoDiff.ChildHistory = []server.RelationshipChild{
		historyChild("child-h8e", "child-h8e", "Completed", now.Add(-2*time.Hour), closedAt, 1.25),
	}
	app := APIAppModel{
		featureList:             server.FeatureListResponse{Features: []server.FeatureSummary{parentNoDiff}},
		featureDetails:          map[string]server.FeatureDetailResponse{},
		refactorHistoryExpanded: map[string]bool{"parent-h8e": true},
	}
	app.selectedFeature = "child-h8e"
	app.rebuildPresentation("child-h8e")

	updated, _ := app.openSelectedDiff()
	if updated.diffReview != nil {
		t.Fatal("openSelectedDiff opened a review for a closed child with no preserved diff")
	}
	if updated.statusMessage != "No preserved diff for this closed child" {
		t.Fatalf("statusMessage = %q, want preserved-diff absence notice", updated.statusMessage)
	}
}

// TestSelectingClosedHistoryClearsStaleLifecycleStatus pins the stale-status
// rule: moving selection onto closed history drops lingering mutation status.
func TestSelectingClosedHistoryClearsStaleLifecycleStatus(t *testing.T) {
	t.Parallel()

	now := time.Now()
	parent := server.FeatureSummary{ID: "parent-h8s", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	parent.ChildHistory = []server.RelationshipChild{
		historyChild("child-h8s", "child-h8s", "Completed", now.Add(-2*time.Hour), now.Add(-time.Hour), 1.25),
	}
	app := historyParentApp(parent, parent.ChildHistory)
	app.refactorHistoryExpanded["parent-h8s"] = true
	app.statusMessage = "Completed Resume"

	// Cursor moves from the parent row onto the Refactor History group row.
	model, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated := model.(APIAppModel)
	if updated.selectedHistoryGroup != "parent-h8s" {
		t.Fatalf("selectedHistoryGroup = %q, want parent-h8s group row selected", updated.selectedHistoryGroup)
	}
	if updated.statusMessage != "" {
		t.Fatalf("statusMessage = %q, want cleared on entering closed history", updated.statusMessage)
	}
}

// TestClosedChildCleanupWarningsRenderInDetail pins the cleanup-warnings
// section between the read-only banner and the metadata.
func TestClosedChildCleanupWarningsRenderInDetail(t *testing.T) {
	t.Parallel()

	now := time.Now()
	closedAt := now.Add(-time.Hour)
	entry := historyChild("child-h8c", "child-h8c", "Completed", now.Add(-2*time.Hour), closedAt, 1.25)
	entry.CleanupWarnings = []server.RelationshipCleanupWarning{{Repo: "api", Message: "worktree busy"}}
	parentH8c := server.FeatureSummary{ID: "parent-h8c", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	parentH8c.ChildHistory = []server.RelationshipChild{entry}
	app := APIAppModel{
		featureList:             server.FeatureListResponse{Features: []server.FeatureSummary{parentH8c}},
		featureDetails:          map[string]server.FeatureDetailResponse{},
		refactorHistoryExpanded: map[string]bool{"parent-h8c": true},
	}
	app.selectedFeature = "child-h8c"
	app.rebuildPresentation("child-h8c")

	dash := app.apiDashboardModel()
	dash.focusPanel = 1
	view := stripANSI(dash.View())
	if !strings.Contains(view, "Cleanup warnings:") {
		t.Fatalf("closed child detail missing cleanup warnings section in:\n%s", view)
	}
	if !strings.Contains(view, "api: worktree busy") {
		t.Fatalf("closed child detail missing repo-scoped warning in:\n%s", view)
	}
}

// TestClosedChildRowsUseSubtextByDefault pins the contrast fix: required
// closed-history rows render in the subtext token, not the muted overlay.
func TestClosedChildRowsUseSubtextByDefault(t *testing.T) {
	t.Parallel()

	now := time.Now()
	closedAt := now.Add(-time.Hour)
	f := apiSynthesizedClosedChild("parent", historyChild("child-sub", "child-sub", "Completed", now.Add(-2*time.Hour), closedAt, 1.25))
	dash := DashboardModel{}
	row := dash.renderClosedChildRowCompact(f, false)
	// The pipeline badge keeps its own accent color inside the row, so pin
	// the token change structurally: the row opens the subtext run and never
	// uses the muted overlay foreground.
	subtextRun := "\x1b[38;2;166;173;200m"
	mutedRun := "\x1b[38;2;108;112;134m"
	if !strings.HasPrefix(row, "    ↳ "+subtextRun) || !strings.Contains(row, subtextRun) {
		t.Fatalf("closed child row not rendered with SubtextStyle:\n got %q", row)
	}
	if strings.Contains(row, mutedRun) {
		t.Fatalf("closed child row still uses the muted overlay token:\n got %q", row)
	}
}

func TestClosedChildDiscardShortcutShowsImmutabilityReason(t *testing.T) {
	t.Parallel()

	now := time.Now()
	closedDetail := server.FeatureDetailDTO{
		ID: "child-h9", Name: "child-h9", Slug: "child-h9",
		Status: "codeready", CurrentPhase: "implement", Pipeline: "moonshot",
		ParentID: "parent-h9", ParentKind: "refactor",
		CloseOutcome: feature.ChildCloseOutcomeDiscarded,
		CreatedAt:    now,
		Actions: []server.ActionDTO{
			{ID: actionIDDiscard, Enabled: false, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}, DisabledReasons: []server.ActionDisabledReasonDTO{
				{Code: "relationship_closed", Message: "the child relationship is closed"},
			}},
		},
	}
	parentH9 := server.FeatureSummary{ID: "parent-h9", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)}
	parentH9.ChildHistory = []server.RelationshipChild{
		historyChild("child-h9", "child-h9", "Discarded", now.Add(-2*time.Hour), now.Add(-time.Hour), 0.75),
	}
	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{parentH9}},
		featureDetails: map[string]server.FeatureDetailResponse{
			"child-h9": {Feature: closedDetail},
		},
		refactorHistoryExpanded: map[string]bool{"parent-h9": true},
	}
	app.selectedFeature = "child-h9"
	app.rebuildPresentation("child-h9")

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if cmd != nil {
		t.Fatal("Update(d) returned a command for a settled child")
	}
	updated := model.(APIAppModel)
	if updated.actionConfirmActive {
		t.Fatal("Update(d) opened a confirmation for a settled child")
	}
	if !strings.Contains(updated.statusMessage, "Discard — the child relationship is closed") {
		t.Fatalf("statusMessage = %q, want the typed relationship_closed reason", updated.statusMessage)
	}
}

func TestDiscardConfirmationRendersImpactPreview(t *testing.T) {
	t.Parallel()

	now := time.Now()
	childDetail := server.FeatureDetailDTO{
		ID: "child-h10", Name: "active-child", Slug: "active-child",
		Status: "interrupted", CurrentPhase: "implement", Pipeline: "moonshot",
		ParentID: "parent-h10", ParentKind: "refactor",
		CreatedAt: now,
		Actions: []server.ActionDTO{
			{ID: actionIDDiscard, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature},
				ImpactPreview: &server.ActionImpactPreviewDTO{
					Kind:    server.ChildDiscard,
					Subject: server.ActionImpactSubjectDTO{ID: "child-h10", Name: "active-child"},
					Categories: []server.ActionImpactCategoryDTO{
						{Key: "sessions", Label: "Sessions stopped", Items: []string{"implementation (sess-1)"}},
						{Key: "worktrees", Label: "Disposable worktrees removed", Items: []string{"/tmp/child-wt (repo api)"}},
						{Key: "branches", Label: "Ephemeral branches removed", Items: []string{"agentic/child-h10 (repo api)"}},
						{Key: "knowledge", Label: "Temporary knowledge removed", Items: []string{}},
					},
					Retained: []string{"Review configuration retained", "Child becomes immutable Discarded history"},
				}},
		},
	}
	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "parent-h10", Name: "Parent", Slug: "parent", Status: "published", CreatedAt: now.Add(-48 * time.Hour)},
		}},
		featureDetails: map[string]server.FeatureDetailResponse{
			"child-h10": {Feature: childDetail},
		},
		refactorHistoryExpanded: map[string]bool{},
	}
	app.selectedFeature = "child-h10"
	app.rebuildPresentation("child-h10")

	app = app.confirmSelectedFeatureAction(mutationKindFeatureDiscard)
	if !app.actionConfirmActive {
		t.Fatal("discard confirmation did not open for the active child")
	}
	view := stripANSI(app.renderFeatureActionConfirm())
	for _, marker := range []string{
		"active-child",
		"Sessions stopped:", "implementation (sess-1)",
		"Disposable worktrees removed:", "api … child-wt", "/tmp/child-wt",
		"Ephemeral branches removed:", "agentic/child-h10 (repo api)",
		"Temporary knowledge removed:", "None",
		"Kept: Review configuration retained",
		"Kept: Child becomes immutable Discarded history",
		"[y] Confirm", "Cancel",
	} {
		if !strings.Contains(view, marker) {
			t.Fatalf("discard confirmation missing %q in:\n%s", marker, view)
		}
	}
	// Cancellation leaves the model idle with no mutation issued.
	model, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatal("escape on discard confirmation returned a command")
	}
	if cancelled := model.(APIAppModel); cancelled.actionConfirmActive {
		t.Fatal("escape did not cancel the discard confirmation")
	}
}

func TestParentDeleteConfirmationRendersCascadePreview(t *testing.T) {
	t.Parallel()

	now := time.Now()
	parentDetail := server.FeatureDetailDTO{
		ID: "parent-h11", Name: "Parent", Slug: "parent-h11",
		Status: "published", Pipeline: "moonshot",
		CreatedAt: now.Add(-48 * time.Hour),
		Actions: []server.ActionDTO{
			{ID: actionIDDelete, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature},
				ImpactPreview: &server.ActionImpactPreviewDTO{
					Kind:    server.ParentCascadeDelete,
					Subject: server.ActionImpactSubjectDTO{ID: "parent-h11", Name: "Parent"},
					Categories: []server.ActionImpactCategoryDTO{
						{Key: "children", Label: "Child records removed", Items: []string{"active-child", "old-child"}},
						{Key: "sessions", Label: "Sessions stopped", Items: []string{"implementation (sess-9)"}},
						{Key: "worktrees", Label: "Worktrees removed", Items: []string{"/tmp/parent-wt (repo api)"}},
						{Key: "branches", Label: "Branches removed", Items: []string{}},
						{Key: "history", Label: "Relationship history removed", Items: []string{"Closed — Completed old-child"}},
						{Key: "knowledge", Label: "Knowledge removed", Items: []string{"knowledge overlay for repo api (parent parent-h11)"}},
					},
					Retained: []string{},
				}},
		},
	}
	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "parent-h11", Name: "Parent", Slug: "parent-h11", Status: "published", CreatedAt: now.Add(-48 * time.Hour)},
		}},
		featureDetails: map[string]server.FeatureDetailResponse{
			"parent-h11": {Feature: parentDetail},
		},
		refactorHistoryExpanded: map[string]bool{},
	}
	app.selectedFeature = "parent-h11"
	app.rebuildPresentation("parent-h11")

	app = app.confirmSelectedFeatureAction(mutationKindFeatureDelete)
	if !app.actionConfirmActive {
		t.Fatal("delete confirmation did not open for the parent")
	}
	view := stripANSI(app.renderFeatureActionConfirm())
	for _, marker := range []string{
		"Child records removed:", "active-child", "old-child",
		"Branches removed:", "None",
		"Relationship history removed:", "Closed — Completed old-child",
		"knowledge overlay for repo api",
		"This cannot be undone.",
	} {
		if !strings.Contains(view, marker) {
			t.Fatalf("parent delete confirmation missing %q in:\n%s", marker, view)
		}
	}
}
