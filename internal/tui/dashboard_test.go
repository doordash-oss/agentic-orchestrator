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
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// testRepoNameOrchestrator and testRepoNameWeb are fixture repo-name
// literals used across this file's repo-list/repo-state tests.
const (
	testRepoNameOrchestrator = "agentic-orchestrator"
	testRepoNameWeb          = "web"
)

func TestDashboardSelectFeature(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{ID: "feat-1", Name: "test-feature", Slug: "test-feature", Status: feature.StatusImplementing, Created: time.Now()},
		{ID: "feat-2", Name: "other-feature", Slug: "other-feature", Status: feature.StatusCodeReady, Created: time.Now().Add(-time.Hour)},
	}
	m := NewDashboardModel(features, "")

	// Cursor 0 is now the IN PROGRESS section header
	if m.SelectedSection() != "inProgress" {
		t.Errorf("expected inProgress section header at cursor 0, got section=%q feature=%s", m.SelectedSection(), m.SelectedFeatureID())
	}

	// Move down to first feature
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.SelectedFeatureID() != "feat-1" {
		t.Errorf("expected feat-1 selected after first down, got %s", m.SelectedFeatureID())
	}

	// Move down to second feature
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.SelectedFeatureID() != "feat-2" {
		t.Errorf("expected feat-2 selected after second down, got %s", m.SelectedFeatureID())
	}

	// Move up back to first feature
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.SelectedFeatureID() != "feat-1" {
		t.Errorf("expected feat-1 selected after up, got %s", m.SelectedFeatureID())
	}
}

func TestDashboardClipsLongFailureDetailLinesToTerminalWidth(t *testing.T) {
	t.Parallel()

	f := &feature.Feature{
		ID:           "feat-failed",
		Name:         "Translate in Sicilian",
		Slug:         "translate-in-sicilian",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseFinalReview,
		FailureType:  feature.FailureProtocolViolation,
		LastError:    "protocol violation: final_review_reviewer @ /Users/ivar.lazzaro/.agentic-workflow/worktrees/agentico-mcp-server/agentic-orchestrator/runs/run-001/final-review/iteration-02: dropped critical SDK message (type=result) after 5s on full attachCh",
		Repos:        []feature.FeatureRepo{{Name: testRepoNameOrchestrator}},
		RepoStates: map[string]*feature.RepoState{
			testRepoNameOrchestrator: {Touched: true, LastError: "dropped critical SDK message (type=result) after 5s on full attachCh"},
		},
	}
	m := dashboardWithSelectedFeature(f)
	m.width = 120
	m.height = 18
	m.focusPanel = 1
	m.syncPreview()

	view := m.View()
	for i, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
		if got := ansi.StringWidth(line); got > m.width {
			t.Fatalf("dashboard line %d width = %d, want <= %d; line=%q\nview:\n%s", i, got, m.width, ansi.Strip(line), ansi.Strip(view))
		}
	}
}

func TestDashboardSortFeatures(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{ID: "done", Name: "done", Slug: "done", Status: feature.StatusDone, Created: time.Now()},
		{ID: "active", Name: "active", Slug: "active", Status: feature.StatusImplementing, Created: time.Now()},
		{ID: "needs-help", Name: "needs-help", Slug: "needs-help", Status: feature.StatusImplementing, Created: time.Now(),
			HelpQueue: []feature.HelpRequest{{Pending: true}}},
	}
	m := NewDashboardModel(features, "")

	// needs-help should be first (active + needs attention)
	if m.features[0].ID != "needs-help" {
		t.Errorf("expected needs-help first, got %s", m.features[0].ID)
	}
	// active should be second
	if m.features[1].ID != "active" {
		t.Errorf("expected active second, got %s", m.features[1].ID)
	}
	// done should be last
	if m.features[2].ID != "done" {
		t.Errorf("expected done last, got %s", m.features[2].ID)
	}
}

func TestDashboardSortFeatures_ActivePublishedCycleBeforeIdlePublished(t *testing.T) {
	t.Parallel()
	now := time.Now()
	features := []*feature.Feature{
		{ID: "idle-published", Name: "idle-published", Slug: "idle-published", Status: feature.StatusPublished, Created: now},
		{
			ID:      "active-cycle",
			Name:    "active-cycle",
			Slug:    "active-cycle",
			Status:  feature.StatusPublished,
			Created: now.Add(-time.Hour),
			RepoCycles: map[string]*feature.RepoCycleState{
				"repo-a": {Type: feature.CycleReviewComments, Status: "running"},
			},
		},
	}

	m := NewDashboardModel(features, "")
	if m.features[0].ID != "active-cycle" {
		t.Fatalf("expected active published cycle to sort first, got %s", m.features[0].ID)
	}
}

func TestDashboardViewRenders(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{ID: "feat-1", Name: "test", Slug: "test-feature", Status: feature.StatusImplementing, CurrentIteration: 3},
	}
	m := NewDashboardModel(features, "")
	view := m.View()

	if view == "" {
		t.Error("expected non-empty view")
	}

	// Should contain version in header
	if !strings.Contains(view, GetVersion()) {
		t.Error("expected version in header")
	}

	// Should contain feature slug
	if !strings.Contains(view, "test-feature") {
		t.Error("expected feature slug in view")
	}
}

func TestDashboardFeatureRowsUsePipelineIconNotRiskBadge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		pipeline feature.PipelineProfile
		risk     feature.RiskLevel
		wantIcon string
	}{
		{
			name:     "medium",
			pipeline: feature.PipelineMedium,
			risk:     feature.RiskHigh,
			wantIcon: "⚡",
		},
		{
			name:     "large",
			pipeline: feature.PipelineLarge,
			risk:     feature.RiskLow,
			wantIcon: "🔬",
		},
		{
			name:     "moonshot",
			pipeline: feature.PipelineMoonshot,
			risk:     feature.RiskMedium,
			wantIcon: "🚀",
		},
		{
			name:     "empty defaults to moonshot",
			pipeline: "",
			risk:     feature.RiskLow,
			wantIcon: "🚀",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &feature.Feature{
				ID:        tt.name,
				Name:      tt.name,
				Slug:      tt.name,
				Status:    feature.StatusImplementing,
				Pipeline:  tt.pipeline,
				RiskLevel: tt.risk,
			}
			m := NewDashboardModel([]*feature.Feature{f}, "")
			row := m.renderFeatureRowCompact(f, false)
			if !strings.Contains(row, tt.wantIcon) {
				t.Fatalf("renderFeatureRowCompact() = %q, want pipeline icon %q", row, tt.wantIcon)
			}
			for _, oldBadge := range []string{"[L]", "[M]", "[H]"} {
				if strings.Contains(row, oldBadge) {
					t.Fatalf("renderFeatureRowCompact() = %q, should not include old risk badge %q", row, oldBadge)
				}
			}
		})
	}
}

func TestDashboardEmpty(t *testing.T) {
	t.Parallel()
	m := NewDashboardModel(nil, "")
	m.width = 120
	m.height = 30
	view := m.View()

	// Ghost CTA should appear instead of the old "No features yet" text
	if !strings.Contains(view, "Create your first feature") {
		t.Error("expected ghost CTA text in empty state")
	}
	if strings.Contains(view, "No features yet") {
		t.Error("old empty state message should be removed")
	}
}

func TestGhostCTAWelcomePanel(t *testing.T) {
	t.Parallel()
	m := NewDashboardModel(nil, "")
	m.width = 120
	m.height = 30
	view := m.View()

	if !strings.Contains(view, "What does Agentic Orchestrator do?") {
		t.Error("expected welcome panel title")
	}
	if !strings.Contains(view, "Examples:") {
		t.Error("expected Examples heading")
	}
	if !strings.Contains(view, "Press  n  to start.") {
		t.Error("expected hint text")
	}
}

func TestGhostCTAPreFocused(t *testing.T) {
	t.Parallel()
	m := NewDashboardModel(nil, "")
	if m.cursor != 0 {
		t.Errorf("expected cursor=0, got %d", m.cursor)
	}
	if len(m.visibleItems) != 1 || m.visibleItems[0].kind != listItemGhostCTA {
		t.Error("expected single ghost CTA item at index 0")
	}
}

func TestGhostCTAEnterSetsWantNewFeature(t *testing.T) {
	t.Parallel()
	m := NewDashboardModel(nil, "")
	m.width = 120
	m.height = 30

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.ConsumeWantNewFeature() {
		t.Error("expected ConsumeWantNewFeature to return true after Enter on ghost CTA")
	}
	if m.ConsumeWantNewFeature() {
		t.Error("expected ConsumeWantNewFeature to return false on second call (consumed)")
	}
}

func TestGhostCTADisappearsWithFeatures(t *testing.T) {
	t.Parallel()
	m := NewDashboardModel(nil, "")
	m.width = 120
	m.height = 30

	// Verify ghost CTA is present
	view := m.View()
	if !strings.Contains(view, "Create your first feature") {
		t.Error("expected ghost CTA in empty state")
	}

	// Add a feature
	m.SetFeatures([]*feature.Feature{
		{ID: "feat-1", Name: "test", Slug: "test-feature", Status: feature.StatusImplementing, Created: time.Now()},
	})
	view = m.View()
	if strings.Contains(view, "Create your first feature") {
		t.Error("ghost CTA should disappear when features exist")
	}
	if !strings.Contains(view, "test-feature") {
		t.Error("expected feature slug in view")
	}
}

func TestGhostCTASuppressedDuringCreation(t *testing.T) {
	t.Parallel()
	m := NewDashboardModel(nil, "")
	m.creatingName = "test-feature"
	m.buildVisibleItems()

	for _, item := range m.visibleItems {
		if item.kind == listItemGhostCTA {
			t.Error("ghost CTA should be suppressed during feature creation")
		}
	}
}

func TestCreatingFeatureRowRendersAtTopOfInProgressFeatures(t *testing.T) {
	t.Parallel()
	m := NewDashboardModel([]*feature.Feature{
		{ID: "feat-1", Name: "older", Slug: "older-feature", Status: feature.StatusImplementing, Created: time.Now()},
	}, "")
	m.creatingName = "creating-feature"
	m.buildVisibleItems()

	lines := strings.Split(strings.TrimRight(ansi.Strip(m.renderFeatureList()), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected creating row plus feature section, got lines=%q", lines)
	}
	if !strings.Contains(lines[0], "IN PROGRESS") {
		t.Fatalf("first line = %q, want in-progress section header", lines[0])
	}
	if !strings.Contains(lines[2], "creating-feature") {
		t.Fatalf("third line = %q, want creating feature row", lines[2])
	}
	if !strings.Contains(lines[3], "older-feature") {
		t.Fatalf("fourth line = %q, want first persisted in-progress feature", lines[3])
	}
}

func TestCreatingFeatureRowCreatesInProgressSection(t *testing.T) {
	t.Parallel()
	m := NewDashboardModel([]*feature.Feature{
		{ID: "feat-1", Name: "published", Slug: "published-feature", Status: feature.StatusPublished, Created: time.Now()},
	}, "")
	m.creatingName = "creating-feature"
	m.buildVisibleItems()

	lines := strings.Split(strings.TrimRight(ansi.Strip(m.renderFeatureList()), "\n"), "\n")
	if len(lines) < 6 {
		t.Fatalf("expected creating row plus published section, got lines=%q", lines)
	}
	if !strings.Contains(lines[0], "IN PROGRESS (1)") {
		t.Fatalf("first line = %q, want in-progress section for creating feature", lines[0])
	}
	if !strings.Contains(lines[2], "creating-feature") {
		t.Fatalf("third line = %q, want creating feature row", lines[2])
	}
	if !strings.Contains(lines[4], "PUBLISHED") {
		t.Fatalf("fifth line = %q, want published section after creating row", lines[4])
	}
}

func TestPersistedSetupFeatureRendersAsSelectableInProgressRow(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:      "feat-setup",
		Name:    "setup",
		Slug:    "setup-feature",
		Status:  feature.StatusSettingUpWorktrees,
		Created: time.Now(),
	}
	m := NewDashboardModel([]*feature.Feature{f}, "")

	if !m.SelectFeatureID(f.ID) {
		t.Fatalf("setup feature was not selectable")
	}
	if got := m.SelectedFeatureID(); got != f.ID {
		t.Fatalf("selected feature = %q, want %q", got, f.ID)
	}

	lines := strings.Split(strings.TrimRight(ansi.Strip(m.renderFeatureList()), "\n"), "\n")
	if len(lines) < 3 || !strings.Contains(lines[0], "IN PROGRESS (1)") {
		t.Fatalf("rendered lines = %q, want setup feature under in-progress", lines)
	}
	row := ansi.Strip(m.renderFeatureRowCompact(f, false))
	if !strings.Contains(row, "Setting up worktrees") {
		t.Fatalf("setup row = %q, want Setting up worktrees label", row)
	}
	if strings.Contains(row, "SettingUpWorktrees") {
		t.Fatalf("setup row = %q, should not render raw status token", row)
	}
}

func TestGhostCTAReappearsAfterDeletion(t *testing.T) {
	t.Parallel()
	m := NewDashboardModel([]*feature.Feature{
		{ID: "feat-1", Name: "test", Slug: "test-feature", Status: feature.StatusImplementing, Created: time.Now()},
	}, "")
	m.width = 120
	m.height = 30

	// Verify no ghost CTA with features
	view := m.View()
	if strings.Contains(view, "Create your first feature") {
		t.Error("ghost CTA should not appear when features exist")
	}

	// Remove all features
	m.SetFeatures([]*feature.Feature{})
	view = m.View()
	if !strings.Contains(view, "Create your first feature") {
		t.Error("ghost CTA should reappear after all features are deleted")
	}
}

func TestRenderWelcomePanel(t *testing.T) {
	t.Parallel()
	m := NewDashboardModel(nil, "")
	panel := m.renderWelcomePanel(60)

	checks := []string{
		"What does Agentic Orchestrator do?",
		"Examples:",
		"rate limiting",
		"Refactor the notification service",
		"Press  n  to start.",
	}
	for _, want := range checks {
		if !strings.Contains(panel, want) {
			t.Errorf("renderWelcomePanel missing %q", want)
		}
	}
}

func TestDashboardAttentionOnlyCountsPending(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{
			ID: "resolved", Name: "resolved", Slug: "resolved",
			Status: feature.StatusImplementing, Created: time.Now(),
			HelpQueue: []feature.HelpRequest{
				{Question: "Old question", Answer: "Answered", Pending: false},
			},
			PermissionsQueue: []feature.PermissionRequest{
				{Tool: toolNameBash, Args: "go test", Pending: false},
			},
		},
		{
			ID: "pending", Name: "pending", Slug: "pending",
			Status: feature.StatusImplementing, Created: time.Now().Add(-time.Hour),
			HelpQueue: []feature.HelpRequest{
				{Question: "New question", Pending: true},
			},
		},
		{
			ID: "clean", Name: "clean", Slug: "clean",
			Status: feature.StatusImplementing, Created: time.Now().Add(-2 * time.Hour),
		},
		{
			ID: "review", Name: "review", Slug: "review",
			Status: feature.StatusPlanNeedsReview, Created: time.Now().Add(-3 * time.Hour),
		},
		{
			ID: "need-input", Name: "need-input", Slug: "need-input",
			Status: feature.StatusNeedUserInput, Created: time.Now().Add(-4 * time.Hour),
		},
		{
			ID: "cycle-input", Name: "cycle-input", Slug: "cycle-input",
			Status: feature.StatusPublished, Created: time.Now().Add(-5 * time.Hour),
			RepoCycles: map[string]*feature.RepoCycleState{
				"api": {
					Type:                     feature.CycleTweak,
					Status:                   feature.RepoCycleNeedUserInput,
					PendingNeedUserInputPath: "/tmp/api/need-user-input.yaml",
				},
			},
		},
	}
	m := NewDashboardModel(features, "")

	count := m.countNeedAttention()
	if count != 4 {
		t.Errorf("countNeedAttention() = %d, want 4 (pending help, review, feature input, cycle input)", count)
	}

	if m.features[0].ID != "pending" {
		t.Errorf("features[0] = %s, want pending (needs attention)", m.features[0].ID)
	}
}

func TestDashboardFeatureRowUsesAwaitingGlyphForAttentionStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		f           *feature.Feature
		wantAwait   bool
		wantSpinner bool
	}{
		{
			name: "pending permission",
			f: &feature.Feature{
				ID: "permission", Name: "permission", Slug: "permission", Status: feature.StatusImplementing,
				PermissionsQueue: []feature.PermissionRequest{{Tool: toolNameBash, Pending: true}},
			},
			wantAwait: true,
		},
		{
			name: "ask user",
			f: &feature.Feature{
				ID: "ask", Name: "ask", Slug: "ask", Status: feature.StatusImplementing,
				HelpQueue: []feature.HelpRequest{{Question: "Question?", Pending: true}},
			},
			wantAwait: true,
		},
		{
			name:      "needs review",
			f:         &feature.Feature{ID: "review", Name: "review", Slug: "review", Status: feature.StatusPlanNeedsReview},
			wantAwait: true,
		},
		{
			name:      "feature need user input",
			f:         &feature.Feature{ID: "nui", Name: "nui", Slug: "nui", Status: feature.StatusNeedUserInput, PendingNeedUserInputPath: "/tmp/need-user-input.yaml"},
			wantAwait: true,
		},
		{
			name: "repo cycle need user input",
			f: &feature.Feature{
				ID: "cycle", Name: "cycle", Slug: "cycle", Status: feature.StatusPublished,
				RepoCycles: map[string]*feature.RepoCycleState{
					"api": {Type: feature.CycleTweak, Status: feature.RepoCycleNeedUserInput, PendingNeedUserInputPath: "/tmp/gate.yaml"},
				},
			},
			wantAwait: true,
		},
		{
			name:        "running",
			f:           &feature.Feature{ID: "run", Name: "run", Slug: "run", Status: feature.StatusImplementing},
			wantSpinner: true,
		},
		{
			name: "active cycle",
			f: &feature.Feature{
				ID: "active-cycle", Name: "active-cycle", Slug: "active-cycle", Status: feature.StatusPublished,
				RepoCycles: map[string]*feature.RepoCycleState{
					"api": {Type: feature.CycleTweak, Status: feature.RepoCycleRunning},
				},
			},
			wantSpinner: true,
		},
		{
			name: "done",
			f:    &feature.Feature{ID: "done", Name: "done", Slug: "done", Status: feature.StatusDone},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewDashboardModel([]*feature.Feature{tt.f}, "")
			m.spinnerView = "SPIN"
			row := m.renderFeatureRowCompact(tt.f, true)
			hasAwait := strings.Contains(row, ansi.Strip(awaitingUserGlyph()))
			hasSpinner := strings.Contains(row, "SPIN")
			if hasAwait != tt.wantAwait {
				t.Errorf("renderFeatureRowCompact(%s) awaiting glyph = %v, want %v; row=%q", tt.name, hasAwait, tt.wantAwait, row)
			}
			if hasSpinner != tt.wantSpinner {
				t.Errorf("renderFeatureRowCompact(%s) spinner = %v, want %v; row=%q", tt.name, hasSpinner, tt.wantSpinner, row)
			}
		})
	}
}

func TestDashboardAttentionBadgeNotShownForResolved(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{
			ID: "resolved-only", Name: "resolved", Slug: "resolved",
			Status: feature.StatusImplementing, Created: time.Now(),
			HelpQueue: []feature.HelpRequest{
				{Question: "Old question", Answer: "Answered", Pending: false},
			},
		},
	}
	m := NewDashboardModel(features, "")
	view := m.View()
	// Should NOT show attention badge since all help items are resolved
	if strings.Contains(view, "need attention") {
		t.Error("should not show attention badge when all items are resolved")
	}
}

func TestDashboardFooterHintsLeftPanel(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{ID: "f1", Name: "test", Slug: "test", Status: feature.StatusImplementing},
	}
	m := NewDashboardModel(features, "")
	m.width = 80
	m.focusPanel = 0 // left panel

	footer := m.renderFooter()
	if !strings.Contains(footer, "["+ChatKeyHint()+"] Ask") {
		t.Error("expected [/] Ask hint in footer when left panel focused")
	}
	if !strings.Contains(footer, "["+HelpKeyHint()+"] Help") {
		t.Error("expected [?] Help hint in footer when left panel focused")
	}
	if !strings.Contains(footer, "[Shift+E] Workspace Config") {
		t.Error("expected [Shift+E] Workspace Config hint in footer when left panel focused")
	}
}

func TestDashboardFooterHintsRightPanel(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{ID: "f1", Name: "test", Slug: "test", Status: feature.StatusImplementing},
	}
	m := NewDashboardModel(features, "")
	m.width = 80
	m.focusPanel = 1 // right panel

	footer := m.renderFooter()
	if !strings.Contains(footer, "["+ChatKeyHint()+"] Ask") {
		t.Error("expected [/] Ask hint in footer when right panel focused")
	}
	if !strings.Contains(footer, "["+HelpKeyHint()+"] Help") {
		t.Error("expected [?] Help hint in footer when right panel focused")
	}
	if !strings.Contains(footer, "[Shift+E] Workspace Config") {
		t.Error("expected [Shift+E] Workspace Config hint in footer when right panel focused")
	}
}

func TestDashboardFooterContextualActionLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		f    *feature.Feature
		want string
	}{
		{
			name: "permission",
			f: &feature.Feature{
				ID: "permission", Name: "permission", Slug: "permission", Status: feature.StatusImplementing,
				PermissionsQueue: []feature.PermissionRequest{{Tool: toolNameBash, Pending: true}},
			},
			want: "[a] Approve",
		},
		{
			name: "ask user",
			f: &feature.Feature{
				ID: "ask", Name: "ask", Slug: "ask", Status: feature.StatusImplementing,
				HelpQueue: []feature.HelpRequest{{Question: "Question?", Pending: true}},
			},
			want: "[a] Answer",
		},
		{
			name: "review",
			f:    &feature.Feature{ID: "review", Name: "review", Slug: "review", Status: feature.StatusPlanNeedsReview},
			want: "[a] Review",
		},
		{
			name: "watch",
			f:    &feature.Feature{ID: "watch", Name: "watch", Slug: "watch", Status: feature.StatusImplementing},
			want: "[a] Watch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewDashboardModel([]*feature.Feature{tt.f}, "")
			m.width = 120
			m.cursor = 1
			m.syncPreview()
			m.focusPanel = 1
			footer := ansi.Strip(m.renderFooter())
			if !strings.Contains(footer, tt.want) {
				t.Fatalf("renderFooter(%s) = %q, want %q", tt.name, footer, tt.want)
			}
			if tt.want == "[a] Approve" {
				primary := strings.Index(footer, "[a] Approve")
				quick := strings.Index(footer, "[y] Approve")
				if primary < 0 || quick < 0 || primary > quick {
					t.Fatalf("renderFooter(%s) = %q, want contextual approve before quick approve", tt.name, footer)
				}
			}
		})
	}
}

func TestDashboardFooterHelpHint(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{ID: "f1", Name: "test", Slug: "test", Status: feature.StatusImplementing},
	}
	m := NewDashboardModel(features, "")
	m.width = 80

	footer := m.renderFooter()
	if !strings.Contains(footer, "["+HelpKeyHint()+"] Help") {
		t.Error("expected [?] Help hint in footer")
	}
}

func TestFormatElapsedUsesTotalRuntime(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		PhaseTimings: map[string]time.Duration{
			"research": 5 * time.Minute,
			"plan":     10 * time.Minute,
		},
	}
	elapsed := formatElapsed(f)
	if elapsed == "" {
		t.Error("expected non-empty elapsed for feature with timing data")
	}
	if !strings.Contains(elapsed, "15m") {
		t.Errorf("expected 15m in elapsed, got %q", elapsed)
	}
}

func TestFormatElapsedFrozenForDone(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Status: feature.StatusDone,
		PhaseTimings: map[string]time.Duration{
			"research":  5 * time.Minute,
			"plan":      10 * time.Minute,
			"implement": 30 * time.Minute,
		},
	}
	elapsed := formatElapsed(f)
	if !strings.Contains(elapsed, "45m") {
		t.Errorf("expected 45m frozen time, got %q", elapsed)
	}
}

func TestFormatElapsedLegacyFallback(t *testing.T) {
	t.Parallel()
	start := time.Now().Add(-20 * time.Minute)
	f := &feature.Feature{
		StartedAt: &start,
	}
	elapsed := formatElapsed(f)
	if elapsed == "" {
		t.Error("expected non-empty elapsed for legacy feature")
	}
}

func TestFormatElapsedNoData(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{}
	if formatElapsed(f) != "" {
		t.Error("expected empty elapsed for feature with no timing data")
	}
}

func TestFormatCost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cost float64
		want string
	}{
		{0, ""},
		{-1.0, ""},
		{0.005, "$0.0050"},
		{0.55, "$0.55"},
		{1.234, "$1.23"},
		{10.5, "$10.50"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatCost(tt.cost)
			if got != tt.want {
				t.Errorf("formatCost(%v) = %q, want %q", tt.cost, got, tt.want)
			}
		})
	}
}

func TestFormatElapsedWithTimeAndCost(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		PhaseTimings: map[string]time.Duration{
			"research":  5 * time.Minute,
			"implement": 10 * time.Minute,
		},
		PhaseCosts: map[string]float64{
			"research":  0.50,
			"implement": 1.25,
		},
	}
	elapsed := formatElapsed(f)
	if elapsed == "" {
		t.Error("expected non-empty elapsed")
	}
	if !strings.Contains(elapsed, "15m") {
		t.Errorf("expected time in elapsed, got %q", elapsed)
	}
	if !strings.Contains(elapsed, "$1.75") {
		t.Errorf("expected cost in elapsed, got %q", elapsed)
	}
}

func TestFormatElapsedCostOnlyNoCost(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		PhaseTimings: map[string]time.Duration{
			"research": 5 * time.Minute,
		},
	}
	elapsed := formatElapsed(f)
	if strings.Contains(elapsed, "$") {
		t.Errorf("expected no cost in elapsed when PhaseCosts is nil, got %q", elapsed)
	}
}

func TestBuildVisibleItems(t *testing.T) {
	t.Parallel()
	now := time.Now()

	t.Run("empty features", func(t *testing.T) {
		m := NewDashboardModel(nil, "")
		// Empty state now emits a ghost CTA item
		if len(m.visibleItems) != 1 {
			t.Errorf("expected 1 visible item (ghost CTA) for empty features, got %d", len(m.visibleItems))
		}
		if len(m.visibleItems) == 1 && m.visibleItems[0].kind != listItemGhostCTA {
			t.Errorf("expected ghost CTA item, got kind=%d", m.visibleItems[0].kind)
		}
	})

	t.Run("single section", func(t *testing.T) {
		features := []*feature.Feature{
			{ID: "f1", Slug: "f1", Status: feature.StatusImplementing, Created: now},
			{ID: "f2", Slug: "f2", Status: feature.StatusImplementing, Created: now.Add(-time.Hour)},
		}
		m := NewDashboardModel(features, "")
		// Should have: section header + 2 features
		if len(m.visibleItems) != 3 {
			t.Errorf("expected 3 visible items, got %d", len(m.visibleItems))
		}
		if m.visibleItems[0].kind != listItemSectionHeader {
			t.Error("expected first item to be section header")
		}
		if m.visibleItems[0].section != "inProgress" {
			t.Errorf("expected inProgress section, got %s", m.visibleItems[0].section)
		}
	})

	t.Run("multiple sections", func(t *testing.T) {
		features := []*feature.Feature{
			{ID: "f1", Slug: "f1", Status: feature.StatusImplementing, Created: now},
			{ID: "f2", Slug: "f2", Status: feature.StatusPublished, Created: now},
			{ID: "f3", Slug: "f3", Status: feature.StatusDone, Created: now},
		}
		m := NewDashboardModel(features, "")
		// 3 section headers + 3 features = 6
		if len(m.visibleItems) != 6 {
			t.Errorf("expected 6 visible items, got %d", len(m.visibleItems))
		}
		// Verify order: inProgress header, f1, published header, f2, completed header, f3
		if m.visibleItems[0].section != "inProgress" || m.visibleItems[0].kind != listItemSectionHeader {
			t.Error("expected inProgress section header first")
		}
		if m.visibleItems[2].section != "published" || m.visibleItems[2].kind != listItemSectionHeader {
			t.Error("expected published section header third")
		}
		if m.visibleItems[4].section != "completed" || m.visibleItems[4].kind != listItemSectionHeader {
			t.Error("expected completed section header fifth")
		}
	})

	t.Run("collapsed section hides features", func(t *testing.T) {
		features := []*feature.Feature{
			{ID: "f1", Slug: "f1", Status: feature.StatusImplementing, Created: now},
			{ID: "f2", Slug: "f2", Status: feature.StatusPublished, Created: now},
		}
		m := NewDashboardModel(features, "")
		// Before collapse: 2 headers + 2 features = 4
		if len(m.visibleItems) != 4 {
			t.Errorf("expected 4 items before collapse, got %d", len(m.visibleItems))
		}
		// Collapse published
		m.collapsedSections["published"] = true
		m.buildVisibleItems()
		// After collapse: 2 headers + 1 feature (f1 only) = 3
		if len(m.visibleItems) != 3 {
			t.Errorf("expected 3 items after collapse, got %d", len(m.visibleItems))
		}
	})
}

func TestSelectedFeatureNilOnSectionHeader(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{ID: "f1", Slug: "f1", Status: feature.StatusImplementing, Created: time.Now()},
	}
	m := NewDashboardModel(features, "")
	// cursor 0 = section header
	if m.SelectedFeature() != nil {
		t.Error("expected nil SelectedFeature on section header")
	}
	if m.SelectedSection() != "inProgress" {
		t.Errorf("expected inProgress section, got %q", m.SelectedSection())
	}
}

func TestSectionCollapseToggle(t *testing.T) {
	t.Parallel()
	now := time.Now()
	features := []*feature.Feature{
		{ID: "f1", Slug: "f1", Status: feature.StatusImplementing, Created: now},
		{ID: "f2", Slug: "f2", Status: feature.StatusPublished, Created: now},
	}
	m := NewDashboardModel(features, "")

	// Navigate to published section header
	publishedIdx := -1
	for i, item := range m.visibleItems {
		if item.kind == listItemSectionHeader && item.section == "published" {
			publishedIdx = i
			break
		}
	}
	if publishedIdx == -1 {
		t.Fatal("published section header not found")
	}
	m.cursor = publishedIdx

	// Press Enter to collapse
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.collapsedSections["published"] {
		t.Error("expected published section to be collapsed after Enter")
	}

	// f2 should no longer be in visible items
	for _, item := range m.visibleItems {
		if item.kind == listItemFeature && item.feature.ID == "f2" {
			t.Error("f2 should be hidden when published section is collapsed")
		}
	}

	// Press Enter again to expand
	// Cursor may have shifted, find published header again
	for i, item := range m.visibleItems {
		if item.kind == listItemSectionHeader && item.section == "published" {
			m.cursor = i
			break
		}
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.collapsedSections["published"] {
		t.Error("expected published section to be expanded after second Enter")
	}
}

func TestInProgressSectionCannotCollapse(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{ID: "f1", Slug: "f1", Status: feature.StatusImplementing, Created: time.Now()},
	}
	m := NewDashboardModel(features, "")
	// cursor 0 = inProgress header
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.collapsedSections["inProgress"] {
		t.Error("IN PROGRESS section should not be collapsible")
	}
}

func TestScrollOffset(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var features []*feature.Feature
	for i := 0; i < 30; i++ {
		features = append(features, &feature.Feature{
			ID:      fmt.Sprintf("f%d", i),
			Slug:    fmt.Sprintf("feature-%d", i),
			Status:  feature.StatusImplementing,
			Created: now.Add(-time.Duration(i) * time.Hour),
		})
	}
	m := NewDashboardModel(features, "")
	m.width = 100
	m.height = 24

	// Set panel height so scroll state is computed during Update
	m.updateScrollState(m.effectivePanelHeight())

	// Navigate to the bottom
	for i := 0; i < len(m.visibleItems)-1; i++ {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}

	// Scroll offset should be non-zero after navigating through 30 features
	if m.scrollOffset == 0 {
		t.Error("expected non-zero scroll offset after navigating to bottom of 30-feature list")
	}

	// View should render correctly
	view := m.View()
	if view == "" {
		t.Error("expected non-empty view")
	}
}

func TestScrollIndicatorsDontOverwriteSelectedRow(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var features []*feature.Feature
	for i := 0; i < 30; i++ {
		features = append(features, &feature.Feature{
			ID:      fmt.Sprintf("f%d", i),
			Slug:    fmt.Sprintf("feature-%d", i),
			Status:  feature.StatusImplementing,
			Created: now.Add(-time.Duration(i) * time.Hour),
		})
	}
	m := NewDashboardModel(features, "")
	m.width = 100
	m.height = 24
	panelHeight := m.effectivePanelHeight()
	m.updateScrollState(panelHeight)

	// Navigate down until scroll kicks in
	for i := 0; i < panelHeight+2; i++ {
		if m.cursor >= len(m.visibleItems)-1 {
			break
		}
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}

	// Render the scrolled feature list
	scrolled := m.scrollFeatureList(panelHeight)
	lines := strings.Split(strings.TrimRight(scrolled, "\n"), "\n")

	// The selected item's rendered line should be present (not replaced by indicator)
	sel := m.SelectedFeature()
	if sel == nil {
		t.Fatal("expected a feature selected, got nil")
	}
	found := false
	for _, line := range lines {
		if strings.Contains(line, sel.Slug) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("selected feature %q not visible in scrolled output; indicators may be overwriting it", sel.Slug)
	}

	// If there's a ▲ indicator it should be on its own dedicated line, not replacing a feature
	if m.scrollOffset > 0 && len(lines) > 0 {
		if strings.Contains(lines[0], "▲") {
			// The indicator line should not contain any feature slug
			for _, f := range features {
				if strings.Contains(lines[0], f.Slug) {
					t.Errorf("▲ indicator line contains feature slug %q — it should be a dedicated line", f.Slug)
				}
			}
		}
	}
}

func TestScrollUpKeepsCursorVisible(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var features []*feature.Feature
	for i := 0; i < 30; i++ {
		features = append(features, &feature.Feature{
			ID:      fmt.Sprintf("f%d", i),
			Slug:    fmt.Sprintf("scroll-up-%d", i),
			Status:  feature.StatusImplementing,
			Created: now.Add(-time.Duration(i) * time.Hour),
		})
	}
	m := NewDashboardModel(features, "")
	m.width = 100
	m.height = 24
	panelHeight := m.effectivePanelHeight()
	m.updateScrollState(panelHeight)

	// Navigate down well past the panel height to trigger scrolling
	for i := 0; i < panelHeight+5; i++ {
		if m.cursor >= len(m.visibleItems)-1 {
			break
		}
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.scrollOffset == 0 {
		t.Fatal("expected scrolling to have engaged after navigating down")
	}

	// Now navigate back up, crossing the top visible boundary repeatedly
	for i := 0; i < panelHeight+5; i++ {
		if m.cursor <= 0 {
			break
		}
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})

		// After every up move, the selected feature must be visible in the rendered output
		sel := m.SelectedFeature()
		if sel == nil {
			continue // cursor on section header, skip
		}
		scrolled := m.scrollFeatureList(panelHeight)
		if !strings.Contains(scrolled, sel.Slug) {
			t.Fatalf("after scrolling up to cursor=%d (scrollOffset=%d), selected feature %q is not visible in rendered output",
				m.cursor, m.scrollOffset, sel.Slug)
		}
	}
}

func TestCollapsedSectionsList(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{ID: "f1", Slug: "f1", Status: feature.StatusImplementing, Created: time.Now()},
		{ID: "f2", Slug: "f2", Status: feature.StatusPublished, Created: time.Now()},
		{ID: "f3", Slug: "f3", Status: feature.StatusDone, Created: time.Now()},
	}
	m := NewDashboardModel(features, "")
	m.collapsedSections["published"] = true
	m.collapsedSections["completed"] = true

	list := m.CollapsedSectionsList()
	if len(list) != 2 {
		t.Errorf("expected 2 collapsed sections, got %d", len(list))
	}
	// Should be sorted
	if list[0] != "completed" || list[1] != "published" {
		t.Errorf("expected [completed, published], got %v", list)
	}
}

func TestSetCollapsedSections(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{ID: "f1", Slug: "f1", Status: feature.StatusImplementing, Created: time.Now()},
		{ID: "f2", Slug: "f2", Status: feature.StatusPublished, Created: time.Now()},
	}
	m := NewDashboardModel(features, "")
	m.SetCollapsedSections([]string{"published"})

	if !m.collapsedSections["published"] {
		t.Error("expected published to be collapsed after SetCollapsedSections")
	}

	// Verify f2 is not in visible items
	for _, item := range m.visibleItems {
		if item.kind == listItemFeature && item.feature.ID == "f2" {
			t.Error("f2 should be hidden when published section is collapsed")
		}
	}
}

func TestInProgressAlwaysExpandedFromConfig(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{ID: "f1", Name: "active", Slug: "active", Status: feature.StatusImplementing, Created: time.Now()},
		{ID: "f2", Name: "done", Slug: "done", Status: feature.StatusPublished, Created: time.Now()},
	}
	m := NewDashboardModel(features, "")

	// Simulate loading config that (erroneously) includes "inProgress"
	m.SetCollapsedSections([]string{"inProgress", "published"})

	// inProgress must NOT be collapsed
	if m.collapsedSections["inProgress"] {
		t.Error("inProgress section must not be collapsed even when config contains it")
	}

	// published should still be collapsed
	if !m.collapsedSections["published"] {
		t.Error("published section should be collapsed")
	}

	// Verify the in-progress feature is visible
	found := false
	for _, item := range m.visibleItems {
		if item.kind == listItemFeature && item.feature.ID == "f1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("in-progress feature f1 must remain visible when inProgress is force-expanded")
	}

	// CollapsedSectionsList must exclude inProgress for self-healing
	for _, s := range m.CollapsedSectionsList() {
		if s == "inProgress" {
			t.Error("CollapsedSectionsList must not include inProgress")
		}
	}

	// Even if someone manually sets inProgress in the map, buildVisibleItems should still show it
	m.collapsedSections["inProgress"] = true
	m.buildVisibleItems()
	found = false
	for _, item := range m.visibleItems {
		if item.kind == listItemFeature && item.feature.ID == "f1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("buildVisibleItems must force inProgress expanded regardless of map state")
	}
}

func TestDashboardNoLinkedGroupPrefixes(t *testing.T) {
	t.Parallel()
	now := time.Now()
	features := []*feature.Feature{
		{ID: "f1", Name: "feature-a", Slug: "feature-a", Status: feature.StatusImplementing, Created: now},
		{ID: "f2", Name: "feature-b", Slug: "feature-b", Status: feature.StatusImplementing, Created: now.Add(-time.Hour)},
		{ID: "f3", Name: "feature-c", Slug: "feature-c", Status: feature.StatusResearching, Created: now.Add(-2 * time.Hour)},
	}
	m := NewDashboardModel(features, "")
	m.width = 120
	m.height = 40

	// Tree-drawing characters that would indicate linked-group (dependency)
	// prefixes in individual feature rows. We check rows specifically because
	// the panel borders rendered by lipgloss may legitimately use │.
	treeChars := []string{"┌─", "└─", "├─"}
	for _, item := range m.visibleItems {
		if item.kind == listItemFeature {
			row := m.renderFeatureRowCompact(item.feature, false)
			for _, tc := range treeChars {
				if strings.Contains(row, tc) {
					t.Errorf("feature row for %s contains tree-drawing prefix %q (linked-group remnant)", item.feature.ID, tc)
				}
			}
		}
	}

	// Verify the full rendered view has no dependency-tree box-drawing characters.
	view := m.View()
	for _, tc := range treeChars {
		if strings.Contains(view, tc) {
			t.Errorf("dashboard view contains tree-drawing character %q (linked-group prefix remnant)", tc)
		}
	}
}

func TestDashboardFooterHidesPublishHintsForUnpublished(t *testing.T) {
	t.Parallel()
	falseBool := false
	// Test StatusCodeReady — [p] should be hidden
	f := &feature.Feature{
		ID:          "f1",
		Status:      feature.StatusCodeReady,
		Repos:       []feature.FeatureRepo{{Name: "r", Publishable: &falseBool}},
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	}
	m := NewDashboardModel([]*feature.Feature{f}, "")
	m.width = 80
	m.focusPanel = 1
	m.cursor = 1 // point at feature row (cursor 0 is section header)
	footer := m.renderFooter()
	if strings.Contains(footer, "[p]") {
		t.Error("expected [p] hint to be hidden for unpublished feature")
	}

	// Test StatusPublished — [g] should be hidden for unpublishable feature,
	// but [b] (Rebase) is now available for all features.
	f.Status = feature.StatusPublished
	f.SetPRURL("https://github.com/org/repo/pull/1")
	m = NewDashboardModel([]*feature.Feature{f}, "")
	m.width = 80
	m.focusPanel = 1
	m.cursor = 1 // point at feature row (cursor 0 is section header)
	footer = m.renderFooter()
	if !strings.Contains(footer, "[b]") {
		t.Error("expected [b] hint to be visible for unpublishable feature (local rebase is allowed)")
	}
	if strings.Contains(footer, "[g]") {
		t.Error("expected [g] hint to be hidden for unpublishable feature")
	}
}

func TestDashboardFooterShowsPublishHintsForPublished(t *testing.T) {
	t.Parallel()
	trueBool := true
	f := &feature.Feature{
		ID:          "f1",
		Status:      feature.StatusCodeReady,
		Repos:       []feature.FeatureRepo{{Name: "r", Publishable: &trueBool}},
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	}
	m := NewDashboardModel([]*feature.Feature{f}, "")
	m.width = 80
	m.focusPanel = 1
	m.cursor = 1 // point at feature row (cursor 0 is section header)
	footer := m.renderFooter()
	if !strings.Contains(footer, "[p]") {
		t.Error("expected [p] hint to be visible for published feature")
	}
}

func TestDashboardFooterHidesPublishedHintsForActivePublishedCycle(t *testing.T) {
	t.Parallel()
	trueBool := true
	f := &feature.Feature{
		ID:     "f1",
		Status: feature.StatusPublished,
		Repos: []feature.FeatureRepo{{
			Name:         "r",
			WorktreePath: "/tmp/worktrees/r",
			Publishable:  &trueBool,
		}},
		RepoCycles: map[string]*feature.RepoCycleState{
			"r": {Type: feature.CycleReviewComments, Status: "running"},
		},
	}
	f.SetPRURL("https://github.com/org/repo/pull/1")
	m := NewDashboardModel([]*feature.Feature{f}, "")
	m.width = 80
	m.focusPanel = 1
	m.cursor = 1

	footer := m.renderFooter()
	if !strings.Contains(footer, "[a] Watch") {
		t.Fatal("expected [a] Watch hint for active published cycle")
	}
	for _, unwanted := range []string{"[t] Tweak", "[Shift+F] Refactor", "[b] Rebase", "[g] Reviews", "[Shift+D] Mark done", "[c] Clean worktree"} {
		if strings.Contains(footer, unwanted) {
			t.Fatalf("did not expect %s while a published repo cycle is active", unwanted)
		}
	}
}

func TestBuildVisibleItems_ActivePublishedCycleInProgressSection(t *testing.T) {
	t.Parallel()
	now := time.Now()
	active := &feature.Feature{
		ID:      "active-cycle",
		Name:    "active-cycle",
		Slug:    "active-cycle",
		Status:  feature.StatusPublished,
		Created: now,
		RepoCycles: map[string]*feature.RepoCycleState{
			"repo-a": {Type: feature.CycleReviewComments, Status: "running"},
		},
	}
	idle := &feature.Feature{
		ID:      "idle-published",
		Name:    "idle-published",
		Slug:    "idle-published",
		Status:  feature.StatusPublished,
		Created: now.Add(-time.Hour),
	}

	m := NewDashboardModel([]*feature.Feature{active, idle}, "")

	var activeSection string
	for _, item := range m.visibleItems {
		if item.kind == listItemFeature && item.feature.ID == "active-cycle" {
			activeSection = item.section
			break
		}
	}
	if activeSection != "inProgress" {
		t.Fatalf("active published cycle section = %q, want inProgress", activeSection)
	}
	if got := m.sectionFeatureCount("inProgress"); got != 1 {
		t.Fatalf("inProgress count = %d, want 1", got)
	}
	if got := m.sectionFeatureCount("published"); got != 1 {
		t.Fatalf("published count = %d, want 1", got)
	}
}

func TestFormatStatus_Tweak_ActiveCycleType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		feature    *feature.Feature
		wantSubstr string
	}{
		{
			name: "active tweak cycle shows tweaking label",
			feature: func() *feature.Feature {
				f := &feature.Feature{
					Status:           feature.StatusImplementing,
					CurrentPhase:     feature.PhaseImplement,
					CurrentIteration: 1,
				}
				f.SetActiveCycleType(feature.CycleTweak)
				return f
			}(),
			wantSubstr: "Tweaking [1]",
		},
		{
			name: "no plan path still shows tweaking with ActiveCycleType",
			feature: func() *feature.Feature {
				f := &feature.Feature{
					Status:           feature.StatusImplementing,
					CurrentPhase:     feature.PhaseImplement,
					CurrentIteration: 2,
				}
				f.SetActiveCycleType(feature.CycleTweak)
				return f
			}(),
			wantSubstr: "Tweaking [2]",
		},
		{
			name: "non-tweak cycle does not show tweaking",
			feature: &feature.Feature{
				Status:           feature.StatusImplementing,
				CurrentPhase:     feature.PhaseImplement,
				CurrentIteration: 1,
			},
			wantSubstr: "Implementing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStatus(tt.feature)
			if !strings.Contains(got, tt.wantSubstr) {
				t.Errorf("formatStatus() = %q, want substring %q", got, tt.wantSubstr)
			}
		})
	}
}

func TestFormatStatusRefactoring(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		feature    *feature.Feature
		wantSubstr string
		wantAbsent string // if non-empty, output must NOT contain this
	}{
		{
			name: "inquiring + refactoring",
			feature: &feature.Feature{
				Status:         feature.StatusInquiring,
				RefactorPrompt: "refactor the auth module",
			},
			wantSubstr: "Refactoring: Inquiring",
		},
		{
			name: "researching + refactoring",
			feature: &feature.Feature{
				Status:         feature.StatusResearching,
				RefactorPrompt: "refactor the auth module",
			},
			wantSubstr: "Refactoring: Researching",
		},
		{
			name: "designing + refactoring",
			feature: &feature.Feature{
				Status:         feature.StatusDesigning,
				RefactorPrompt: "refactor the auth module",
			},
			wantSubstr: "Refactoring: Designing",
		},
		{
			name: "planning + refactoring + roadmap phase 0",
			feature: &feature.Feature{
				Status:              feature.StatusPlanning,
				RefactorPrompt:      "refactor the auth module",
				CurrentRoadmapPhase: 0,
			},
			wantSubstr: "Refactoring: Creating Roadmap",
		},
		{
			name: "implementing + refactoring + refactor artifact path",
			feature: &feature.Feature{
				Status:         feature.StatusImplementing,
				RefactorPrompt: "refactor the auth module",
				CurrentPhase:   feature.PhaseImplement,
				Artifacts:      map[string]string{"plan": "/some/path/refactor-1/implement/plan.md"},
			},
			wantSubstr: "Refactoring: Implementing",
		},
		{
			name: "researching + NOT refactoring",
			feature: &feature.Feature{
				Status:         feature.StatusResearching,
				RefactorPrompt: "",
			},
			wantSubstr: "Researching",
			wantAbsent: "Refactoring",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStatus(tt.feature)
			if !strings.Contains(got, tt.wantSubstr) {
				t.Errorf("formatStatus() = %q, want substring %q", got, tt.wantSubstr)
			}
			if tt.wantAbsent != "" && strings.Contains(got, tt.wantAbsent) {
				t.Errorf("formatStatus() = %q, must NOT contain %q", got, tt.wantAbsent)
			}
		})
	}
}

func TestFormatStatus_PublishedActiveReviewCommentsCycle(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Status:           feature.StatusPublished,
		CurrentIteration: 1,
		Repos:            []feature.FeatureRepo{{Name: "agentic"}},
		RepoCycles: map[string]*feature.RepoCycleState{
			"agentic": {Type: feature.CycleReviewComments, Status: "running"},
		},
	}

	got := formatStatus(f)
	if !strings.Contains(got, "Addressing Review Comments [1]") {
		t.Fatalf("formatStatus() = %q, want active review-comments cycle label", got)
	}
	if strings.Contains(got, "Published") {
		t.Errorf("formatStatus() = %q, should not fall back to Published while cycle is active", got)
	}
}

func TestDashboardStatusShowsActiveArtifactPhaseDuringProtocolRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		status      feature.Status
		current     feature.Phase
		consecutive int
		want        string
	}{
		{"inquire_first_retry", feature.StatusInquiring, feature.PhaseInquire, 1, "Inquiring"},
		{"research_second_retry", feature.StatusResearching, feature.PhaseResearch, 2, "Researching"},
		{"design_second_retry", feature.StatusDesigning, feature.PhaseDesign, 2, "Designing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{
				ID:           tt.name,
				Name:         tt.name,
				Slug:         tt.name,
				Status:       tt.status,
				CurrentPhase: tt.current,
				FailureType:  "",
				LastError:    "",
			}
			got := formatStatus(f)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("formatStatus() = %q, want active label %q for retry streak %d", got, tt.want, tt.consecutive)
			}
			if strings.Contains(got, "Failed") || strings.Contains(got, "protocol violation") {
				t.Fatalf("formatStatus() = %q, want active phase during retry streak %d", got, tt.consecutive)
			}
		})
	}
}

func TestDashboardStatusShowsBuildingKBDuringProtocolRetry(t *testing.T) {
	t.Parallel()
	for _, consecutive := range []int{1, 2} {
		t.Run(fmt.Sprintf("kb_retry_%d", consecutive), func(t *testing.T) {
			f := &feature.Feature{
				ID:           fmt.Sprintf("feat-kb-retry-%d", consecutive),
				Name:         "KB Retry",
				Slug:         "kb-retry",
				Status:       feature.StatusBuildingKB,
				CurrentPhase: feature.PhaseKnowledgeBase,
				Repos:        []feature.FeatureRepo{{Name: "repo-a"}},
				KBStatus:     map[string]string{"repo-a": "building"},
				FailureType:  "",
				LastError:    "",
			}
			got := formatStatus(f)
			if !strings.Contains(got, "Building KB") {
				t.Fatalf("formatStatus() = %q, want Building KB for retry streak %d", got, consecutive)
			}
			if strings.Contains(got, "Failed") || strings.Contains(got, "protocol violation") {
				t.Fatalf("formatStatus() = %q, want in-progress KB during retry streak %d", got, consecutive)
			}
		})
	}
}

func TestDashboardStatusShowsTerminalKBProtocolViolation(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-kb-terminal",
		Name:         "KB Terminal",
		Slug:         "kb-terminal",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseKnowledgeBase,
		FailureType:  feature.FailureProtocolViolation,
		LastError:    "protocol violation: knowledge_base_builder @ /tmp/kb: index.md: missing",
	}
	got := formatStatus(f)
	if !strings.Contains(got, "Failed") || !strings.Contains(got, "protocol violation") {
		t.Fatalf("formatStatus() = %q, want terminal protocol violation", got)
	}
	if strings.Contains(got, "Building KB") {
		t.Fatalf("formatStatus() = %q, should not render active KB after terminal failure", got)
	}
}

func TestFormatStatus_RepoPausedOnInput_StylesAsWaitingInput(t *testing.T) {
	t.Parallel()
	// Schema v3 always routes implement completion through the multi-repo
	// orchestrator, which keeps feature.Status at StatusImplementing while
	// persisting a NEED_USER_INPUT pause on RepoImpl[name].Status. The list
	// row's yellow paint must trigger off that signal too — not just the
	// permission/help queues — otherwise features silently render as plain
	// "Implementing" while waiting for the user.
	tests := []struct {
		name    string
		feature *feature.Feature
		want    string
	}{
		{
			name: "implement repo paused on input — single repo",
			feature: &feature.Feature{
				Status:                   feature.StatusImplementing,
				CurrentPhase:             feature.PhaseImplement,
				CurrentIteration:         3,
				PendingNeedUserInputPath: "/some/iteration/need-user-input.yaml",
				Repos:                    []feature.FeatureRepo{{Name: "agentic"}},
				RepoStates: map[string]*feature.RepoState{
					"agentic": {Touched: true},
				},
			},
			want: "Implementing [3] | waiting input",
		},
		{
			name: "post-publish cycle paused on input",
			feature: &feature.Feature{
				Status:           feature.StatusPublished,
				CurrentIteration: 1,
				Repos:            []feature.FeatureRepo{{Name: "agentic"}},
				RepoCycles: map[string]*feature.RepoCycleState{
					"agentic": {Type: feature.CycleReviewComments, Status: feature.RepoCycleNeedUserInput},
				},
			},
			want: "waiting input",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStatus(tt.feature)
			if !strings.Contains(got, tt.want) {
				t.Errorf("formatStatus() = %q, want substring %q", got, tt.want)
			}
		})
	}
}

func TestDashboardFeatureRow_FinalReviewingWaitingInput(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:              "final-review",
		Slug:            "final-review",
		Status:          feature.StatusFinalReviewing,
		CurrentPhase:    feature.PhaseFinalReview,
		ReviewIteration: 2,
		HelpQueue: []feature.HelpRequest{
			{Question: "Need direction", Pending: true},
		},
	}
	m := NewDashboardModel([]*feature.Feature{f}, "")

	row := m.renderFeatureRowCompact(f, false)
	if !strings.Contains(row, "Final Review: reviewing iteration 2 | waiting input") {
		t.Fatalf("renderFeatureRowCompact() = %q, want final review waiting-input label", row)
	}
	if strings.Contains(row, "? final-review") {
		t.Fatalf("renderFeatureRowCompact() = %q, should not use unknown status icon for final review", row)
	}
}

func TestActivePublishedCycleStatus_NoSuffixForSingleRepo(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Status: feature.StatusPublished,
		Repos:  []feature.FeatureRepo{{Name: "payments"}},
		RepoCycles: map[string]*feature.RepoCycleState{
			"payments": {Type: feature.CycleRebase, Status: "running"},
		},
	}
	label, _, ok := activePublishedCycleStatus(f)
	if !ok {
		t.Fatal("activePublishedCycleStatus() should report active cycle")
	}
	if strings.Contains(label, "·") {
		t.Errorf("activePublishedCycleStatus() = %q, want no `· repoName` suffix for single-repo", label)
	}
}

func TestActivePublishedCycleStatus_FeatureRebaseNoRepoSuffix(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Status:           feature.StatusCodeReady,
		CurrentIteration: 2,
		Repos:            []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}},
		ActiveCycle:      &feature.CycleState{Type: feature.CycleRebase, Status: feature.RepoCycleRunning},
	}

	label, _, ok := activePublishedCycleStatus(f)
	if !ok {
		t.Fatal("activePublishedCycleStatus() should report feature-level rebase")
	}
	if label != "Rebasing [2]" {
		t.Fatalf("activePublishedCycleStatus() = %q, want feature-level rebase label", label)
	}
	if strings.Contains(label, "·") {
		t.Fatalf("activePublishedCycleStatus() = %q, want no repo suffix for feature-level rebase", label)
	}

	got := formatStatus(f)
	if !strings.Contains(got, "Rebasing [2]") || strings.Contains(got, "Code Ready") {
		t.Fatalf("formatStatus() = %q, want active rebase instead of Code Ready", got)
	}
	detail := stripANSI(formatDetailStatus(f))
	if !strings.Contains(detail, "Rebasing [2]") || strings.Contains(detail, "Code Ready") {
		t.Fatalf("formatDetailStatus() = %q, want active rebase instead of Code Ready", detail)
	}
}

func TestActivePublishedCycleStatus_SuffixForMultiRepo(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Status: feature.StatusPublished,
		Repos:  []feature.FeatureRepo{{Name: "payments"}, {Name: "worker"}},
		RepoCycles: map[string]*feature.RepoCycleState{
			"payments": {Type: feature.CycleRebase, Status: "running"},
		},
	}
	label, _, ok := activePublishedCycleStatus(f)
	if !ok {
		t.Fatal("activePublishedCycleStatus() should report active cycle")
	}
	if !strings.Contains(label, "· payments") {
		t.Errorf("activePublishedCycleStatus() = %q, want `· payments` suffix for multi-repo", label)
	}
}
