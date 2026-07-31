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
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestChildFeatureSelectedAfterRefactorLaunch(t *testing.T) {
	t.Parallel()

	created := time.Now()
	parentSummary := server.FeatureSummary{
		ID: "parent-001", Name: "Parent", Slug: "parent",
		Status: "published", CurrentPhase: "publish",
		CreatedAt: created,
	}
	childDetail := server.FeatureDetailDTO{
		ID: "child-001", Name: "Child", Slug: "child",
		Status: "implementing", CurrentPhase: "implement",
		ParentID: "parent-001", ParentKind: "refactor",
		Pipeline:  "moonshot",
		CreatedAt: created.Add(time.Hour),
	}

	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{parentSummary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			"parent-001": {Feature: apiTestFeatureDetail(parentSummary)},
			"child-001":  {Feature: childDetail},
		},
	}

	app.selectedFeature = "child-001"
	app.rebuildPresentation("child-001")

	if app.selectedFeature != "child-001" {
		t.Fatalf("after rebuildPresentation, selectedFeature = %q, want child-001", app.selectedFeature)
	}

	dash := app.apiDashboardModel()
	if dash.cursor < 0 || dash.cursor >= len(dash.visibleItems) {
		t.Fatalf("cursor = %d out of range [0, %d)", dash.cursor, len(dash.visibleItems))
	}
	selected := dash.visibleItems[dash.cursor]
	if selected.kind != listItemChildFeature {
		t.Fatalf("selected item kind = %v, want listItemChildFeature", selected.kind)
	}
	if selected.feature == nil || selected.feature.ID != "child-001" {
		t.Fatalf("selected child = %+v, want child-001", selected.feature)
	}
}

func TestChildFeatureRemainsSelectedThroughRefresh(t *testing.T) {
	t.Parallel()

	created := time.Now()
	parentSummary := server.FeatureSummary{
		ID: "parent-002", Name: "Parent 2", Slug: "parent-2",
		Status: "published", CurrentPhase: "publish",
		CreatedAt: created,
	}
	childDetail := server.FeatureDetailDTO{
		ID: "child-002", Name: "Child 2", Slug: "child-2",
		Status: "implementing", CurrentPhase: "implement",
		ParentID: "parent-002", ParentKind: "refactor",
		Pipeline:  "moonshot",
		CreatedAt: created.Add(time.Hour),
	}

	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{parentSummary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			"parent-002": {Feature: apiTestFeatureDetail(parentSummary)},
			"child-002":  {Feature: childDetail},
		},
	}

	app.selectedFeature = "child-002"
	app.rebuildPresentation("child-002")

	for i := 0; i < 3; i++ {
		dash := app.apiDashboardModel()
		selected := dash.visibleItems[dash.cursor]
		if selected.kind != listItemChildFeature || selected.feature == nil || selected.feature.ID != "child-002" {
			t.Fatalf("refresh %d: selected = %+v, want child-002 (listItemChildFeature)", i, selected)
		}
		app.rebuildPresentation(app.selectedFeature)
	}
}

func TestSelectedAPIDashboardFeatureReturnsChild(t *testing.T) {
	t.Parallel()

	created := time.Now()
	parentSummary := server.FeatureSummary{
		ID: "parent-003", Name: "Parent 3", Slug: "parent-3",
		Status: "published", CurrentPhase: "publish",
		CreatedAt: created,
	}
	childDetail := server.FeatureDetailDTO{
		ID: "child-003", Name: "Child 3", Slug: "child-3",
		Status: "implementing", CurrentPhase: "implement",
		ParentID: "parent-003", ParentKind: "refactor",
		Pipeline:  "moonshot",
		CreatedAt: created.Add(time.Hour),
	}

	app := APIAppModel{
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{parentSummary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			"parent-003": {Feature: apiTestFeatureDetail(parentSummary)},
			"child-003":  {Feature: childDetail},
		},
	}

	app.selectedFeature = "child-003"
	f := app.selectedAPIDashboardFeature()
	if f == nil {
		t.Fatal("selectedAPIDashboardFeature() = nil, want child feature")
	}
	if f.ID != "child-003" {
		t.Fatalf("selectedAPIDashboardFeature() ID = %q, want child-003", f.ID)
	}
	if f.Parent == nil || f.Parent.ParentID != "parent-003" {
		t.Fatalf("selected feature parent = %+v, want parent-003", f.Parent)
	}
}

func TestRefactorRemediationParsesDirtyDiagnostics(t *testing.T) {
	t.Parallel()

	apiErr := &server.APIError{
		Code:    "parent_worktrees_dirty",
		Message: "parent worktrees are dirty",
		Target: map[string]any{
			"repos": []any{
				map[string]any{
					"repo":            "payments-api",
					"path":            "/repos/payments-api",
					"staged_total":    float64(3),
					"unstaged_total":  float64(7),
					"untracked_total": float64(2),
				},
			},
		},
	}

	remediation := parseDirtyRemediation(apiErr, nil, "parent-001")
	if remediation == nil {
		t.Fatal("parseDirtyRemediation() = nil, want remediation model")
	}
	if len(remediation.repos) != 1 {
		t.Fatalf("remediation repos = %d, want 1", len(remediation.repos))
	}
	repo := remediation.repos[0]
	if repo.name != "payments-api" {
		t.Fatalf("repo name = %q, want payments-api", repo.name)
	}
	if repo.stagedTotal != 3 {
		t.Fatalf("stagedTotal = %d, want 3", repo.stagedTotal)
	}
	if repo.unstagedTotal != 7 {
		t.Fatalf("unstagedTotal = %d, want 7", repo.unstagedTotal)
	}
	if repo.untrackedTotal != 2 {
		t.Fatalf("untrackedTotal = %d, want 2", repo.untrackedTotal)
	}
}

func TestRefactorRemediationRejectsNonDirtyError(t *testing.T) {
	t.Parallel()

	apiErr := &server.APIError{
		Code:    "active_child_exists",
		Message: "parent already has an active child",
	}
	remediation := parseDirtyRemediation(apiErr, nil, "parent-001")
	if remediation != nil {
		t.Fatal("parseDirtyRemediation() = non-nil for non-dirty error")
	}
}

// TestColdStartProjectsActiveChild pins that a fresh TUI start projects the
// nested child row from the parent's ActiveChild summary without waiting for
// a relationship event: NewAPIAppModel eagerly loads each active child's
// detail so dashboard sectioning, selection, and control work immediately.
func TestColdStartProjectsActiveChild(t *testing.T) {
	t.Parallel()

	created := time.Now()
	parentSummary := server.FeatureSummary{
		ID: "parent-cold", Name: "Cold Parent", Slug: "cold-parent",
		Status: "published", CurrentPhase: "publish",
		CreatedAt: created,
		ActiveChild: &server.RelationshipChild{
			ID: "child-cold", Name: "Cold Child", Kind: "refactor",
			Status: "implementing", DisplayState: "active", RelationshipState: "active",
		},
	}
	childDetail := server.FeatureDetailDTO{
		ID: "child-cold", Name: "Cold Child", Slug: "cold-child",
		Status: "implementing", CurrentPhase: "implement",
		ParentID: "parent-cold", ParentKind: "refactor",
		CreatedAt: created.Add(time.Hour),
	}
	parentDetail := apiTestFeatureDetail(parentSummary)

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{parentSummary}},
		detailsByID: map[string]server.FeatureDetailResponse{
			"parent-cold": {Feature: parentDetail},
			"child-cold":  {Feature: childDetail},
		},
	}
	app := newTestAPIAppModel(t, client)
	defer app.Close()

	children := app.apiChildFeatures()
	child, ok := children["child-cold"]
	if !ok || child == nil {
		t.Fatalf("apiChildFeatures() = %+v, want eager cold-start child projection", children)
	}

	dash := app.apiDashboardModel()
	var parentRow, childRow int = -1, -1
	for i, item := range dash.visibleItems {
		if item.kind == listItemFeature && item.feature != nil && item.feature.ID == "parent-cold" {
			parentRow = i
		}
		if item.kind == listItemChildFeature && item.feature != nil && item.feature.ID == "child-cold" {
			childRow = i
		}
	}
	if parentRow < 0 || childRow != parentRow+1 {
		t.Fatalf("visibleItems parent=%d child=%d, want nested child immediately beneath parent", parentRow, childRow)
	}
}

// TestRefactorEntryShowsRemediationForDirtyParent pins the known-dirty entry
// path: when the server disables Refactor with the dirty_parent reason, the
// guarded entry presents the structured dirty-worktree remediation
// immediately instead of bypassing the disabled action into the wizard.
// Wizard-value preservation is reserved for the submission-time race.
func TestRefactorEntryShowsRemediationForDirtyParent(t *testing.T) {
	t.Parallel()

	created := time.Now()
	summary := server.FeatureSummary{
		ID: "parent-dirty", Name: "Dirty Parent", Slug: "dirty-parent",
		Status: "published", CurrentPhase: "publish",
		CreatedAt: created, Repos: []string{"repo"},
	}
	detail := apiTestFeatureDetail(summary)
	detail.Actions = []server.ActionDTO{
		{ID: actionIDRefactor, Enabled: false, DisabledReasons: []server.ActionDisabledReasonDTO{
			{
				Code:    disabledReasonDirtyParent,
				Message: "parent repositories must be clean before launching a refactor child",
				Target: map[string]any{
					"repos": []any{
						map[string]any{
							"repo": "repo", "path": "/tmp/repo",
							"staged_total": 1, "unstaged_total": 2, "untracked_total": 3,
						},
					},
				},
			},
		}},
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail:   server.FeatureDetailResponse{Feature: detail},
	}
	app := newTestAPIAppModel(t, client)
	defer app.Close()

	m, _ := app.openRefactorWizardIfEligible()
	if m.wizard != nil {
		t.Fatal("openRefactorWizardIfEligible() bypassed the dirty_parent guard into the wizard")
	}
	remediation := m.refactorLaunch.remediation
	if remediation == nil {
		t.Fatalf("openRefactorWizardIfEligible() did not open dirty-parent remediation; statusMessage=%q", m.statusMessage)
	}
	if remediation.wizardResult != nil {
		t.Fatal("entry-time remediation must not carry wizard values")
	}
	if remediation.parentID != summary.ID {
		t.Fatalf("remediation parent = %q, want %q", remediation.parentID, summary.ID)
	}
	if len(remediation.repos) != 1 || remediation.repos[0].name != "repo" ||
		remediation.repos[0].stagedTotal != 1 || remediation.repos[0].unstagedTotal != 2 || remediation.repos[0].untrackedTotal != 3 {
		t.Fatalf("remediation repos = %+v, want one repo entry with staged=1 unstaged=2 untracked=3", remediation.repos)
	}
	view := remediation.View(120)
	if strings.Contains(view, "preserved") {
		t.Fatalf("entry-time remediation must not claim preserved wizard values:\n%s", view)
	}
	for _, marker := range []string{"Refactor Launch Blocked", "repo", "/tmp/repo", "Staged:", "Unstaged:", "Untracked:"} {
		if !strings.Contains(view, marker) {
			t.Fatalf("entry remediation view missing %q:\n%s", marker, view)
		}
	}
}

// TestRefactorEntryRetryReevaluatesTheGuard pins the entry-time remediation
// retry: pressing r re-pulls the parent detail, and once the fresh snapshot
// shows the action enabled the wizard opens instead of the remediation
// panel; a still-dirty parent keeps the remediation with fresh diagnostics.
func TestRefactorEntryRetryReevaluatesTheGuard(t *testing.T) {
	t.Parallel()

	created := time.Now()
	summary := server.FeatureSummary{
		ID: "parent-dirty-retry", Name: "Dirty Retry Parent", Slug: "dirty-retry-parent",
		Status: "published", CurrentPhase: "publish",
		CreatedAt: created, Repos: []string{"repo"},
	}
	dirtyDetail := apiTestFeatureDetail(summary)
	dirtyDetail.Actions = []server.ActionDTO{
		{ID: actionIDRefactor, Enabled: false, DisabledReasons: []server.ActionDisabledReasonDTO{
			{Code: disabledReasonDirtyParent, Message: "parent repositories must be clean"},
		}},
	}
	cleanDetail := apiTestFeatureDetail(summary)
	cleanDetail.Actions = []server.ActionDTO{
		{ID: actionIDRefactor, Enabled: true},
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail:   server.FeatureDetailResponse{Feature: dirtyDetail},
	}
	app := newTestAPIAppModel(t, client)
	defer app.Close()

	m, _ := app.openRefactorWizardIfEligible()
	if m.refactorLaunch.remediation == nil {
		t.Fatal("dirty entry did not open remediation")
	}

	// Retry key: panel clears, entry retry is marked, and a refresh command
	// for the parent is dispatched.
	retriedModel, cmd := m.handleAPIKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	retried, ok := retriedModel.(APIAppModel)
	if !ok {
		t.Fatalf("handleAPIKey return type = %T, want APIAppModel", retriedModel)
	}
	if retried.refactorLaunch.remediation != nil {
		t.Fatal("remediation panel stayed open after entry retry key")
	}
	if retried.refactorLaunch.entryRetryParentID != summary.ID {
		t.Fatalf("entryRetryParentID = %q, want %q", retried.refactorLaunch.entryRetryParentID, summary.ID)
	}
	if cmd == nil {
		t.Fatal("entry retry did not dispatch a refresh command")
	}

	// The refreshed parent is now clean: the snapshot application must open
	// the wizard via the same guarded entry path.
	refreshed := server.FeatureDetailResponse{Feature: cleanDetail}
	m3, _ := retried.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{Feature: &refreshed}})
	m4, ok := m3.(APIAppModel)
	if !ok {
		t.Fatalf("Update return type = %T, want APIAppModel", m3)
	}
	if m4.refactorLaunch.entryRetryParentID != "" {
		t.Fatal("entry retry marker not consumed after refresh")
	}
	if m4.wizard == nil || !m4.wizard.IsRefactor() {
		t.Fatalf("clean refresh did not open the refactor wizard: wizard=%v remediation=%v status=%q",
			m4.wizard != nil, m4.refactorLaunch.remediation != nil, m4.statusMessage)
	}
}

// TestRefactorWizardStaysBlockedForOtherDisabledReasons pins that non-dirty
// disabled reasons still guard entry with the plain status line.
func TestRefactorWizardStaysBlockedForOtherDisabledReasons(t *testing.T) {
	t.Parallel()

	created := time.Now()
	summary := server.FeatureSummary{
		ID: "parent-blocked", Name: "Blocked Parent", Slug: "blocked-parent",
		Status: "published", CurrentPhase: "publish",
		CreatedAt: created, Repos: []string{"repo"},
	}
	detail := apiTestFeatureDetail(summary)
	detail.Actions = []server.ActionDTO{
		{ID: actionIDRefactor, Enabled: false, DisabledReasons: []server.ActionDisabledReasonDTO{
			{Code: "active_child", Message: "refactor already in progress"},
		}},
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail:   server.FeatureDetailResponse{Feature: detail},
	}
	app := newTestAPIAppModel(t, client)
	defer app.Close()

	m, _ := app.openRefactorWizardIfEligible()
	if m.wizard != nil {
		t.Fatal("openRefactorWizardIfEligible() opened wizard for non-dirty disabled reason")
	}
	if m.statusMessage == "" {
		t.Fatal("statusMessage empty for blocked refactor entry")
	}
}

// TestContextualActionPrefersStartOverWatch pins that a Created feature
// (e.g. a refactor child after setup) dispatches its server-enabled Start
// action from `a` instead of trapping into the watch/live-preview attach
// fallback, which would make the ordinary start action unreachable.
func TestContextualActionPrefersStartOverWatch(t *testing.T) {
	t.Parallel()

	created := time.Now()
	summary := server.FeatureSummary{
		ID: "child-start", Name: "Start Me", Slug: "start-me",
		Status: "created", CreatedAt: created, Repos: []string{"repo"},
	}
	detail := apiTestFeatureDetail(summary)
	detail.Actions = []server.ActionDTO{
		{ID: actionIDStart, Enabled: true},
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail:   server.FeatureDetailResponse{Feature: detail},
		livePreview: server.LivePreviewResponse{
			Feature: summary,
		},
	}
	app := newTestAPIAppModel(t, client)
	defer app.Close()

	model, cmd := app.openAPIContextualAction()
	m := model.(APIAppModel)
	if m.attach != nil {
		t.Fatal("openAPIContextualAction() opened watch attach for a start-ready Created feature")
	}
	if cmd == nil {
		t.Fatal("openAPIContextualAction() returned nil command, want start dispatch")
	}
	msg, ok := cmd().(apiMutationResultMsg)
	if !ok {
		t.Fatalf("start command message = %T, want apiMutationResultMsg", msg)
	}
	if msg.err != nil || msg.kind != mutationKindFeatureStart {
		t.Fatalf("start mutation result = %+v, want successful %s", msg, mutationKindFeatureStart)
	}
	if len(client.startFeatureIDs) != 1 || client.startFeatureIDs[0] != "child-start" {
		t.Fatalf("StartFeature calls = %v, want [child-start]", client.startFeatureIDs)
	}
}

// TestContextualActionPrefersResumeOverWatch pins that an Interrupted
// feature (e.g. a refactor child whose phase was stopped) dispatches its
// server-enabled Resume action from `a` instead of trapping into the
// watch/live-preview attach fallback offered by the recently stopped
// session record, which would make the ordinary resume action unreachable.
func TestContextualActionPrefersResumeOverWatch(t *testing.T) {
	t.Parallel()

	created := time.Now()
	summary := server.FeatureSummary{
		ID: "child-resume", Name: "Resume Me", Slug: "resume-me",
		Status: "interrupted", CreatedAt: created, Repos: []string{"repo"},
	}
	detail := apiTestFeatureDetail(summary)
	detail.Actions = []server.ActionDTO{
		{ID: "resume", Enabled: true},
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail:   server.FeatureDetailResponse{Feature: detail},
		livePreview: server.LivePreviewResponse{
			Feature: summary,
		},
	}
	app := newTestAPIAppModel(t, client)
	defer app.Close()

	model, cmd := app.openAPIContextualAction()
	m := model.(APIAppModel)
	if m.attach != nil {
		t.Fatal("openAPIContextualAction() opened watch attach for a resume-ready Interrupted feature")
	}
	if cmd == nil {
		t.Fatal("openAPIContextualAction() returned nil command, want resume dispatch")
	}
	msg, ok := cmd().(apiMutationResultMsg)
	if !ok {
		t.Fatalf("resume command message = %T, want apiMutationResultMsg", msg)
	}
	if msg.err != nil || msg.kind != mutationKindFeatureResume {
		t.Fatalf("resume mutation result = %+v, want successful %s", msg, mutationKindFeatureResume)
	}
	if len(client.resumeFeatureIDs) != 1 || client.resumeFeatureIDs[0] != "child-resume" {
		t.Fatalf("ResumeFeature calls = %v, want [child-resume]", client.resumeFeatureIDs)
	}
}

// TestHeaderCountsActiveChildRelationshipAsActive pins the relationship-aware
// header summary: a parent with an active refactor child counts once as
// active (never published), and the child's attention contributes once to the
// global warning total because the parent's row surfaces it as "Refactoring —
// Needs attention".
func TestHeaderCountsActiveChildRelationshipAsActive(t *testing.T) {
	t.Parallel()

	parent := &feature.Feature{
		ID: "hdr-parent", Name: "Header Parent", Slug: "header-parent",
		Status: feature.StatusPublished, CurrentPhase: feature.PhasePublish,
	}
	child := &feature.Feature{
		ID: "hdr-child", Name: "Header Child", Slug: "header-child",
		Status: feature.StatusNeedUserInput, CurrentPhase: feature.PhaseImplement,
		Parent: &feature.ChildRelationship{ParentID: "hdr-parent", Kind: "refactor"},
	}

	m := NewDashboardModel([]*feature.Feature{parent}, "")
	m.SetChildFeatures(map[string]*feature.Feature{"hdr-child": child})

	header := stripANSI(m.renderHeader(120))
	if !strings.Contains(header, "1 active") {
		t.Fatalf("header = %q, want the parent/child relationship counted once as active", header)
	}
	if strings.Contains(header, "1 published") {
		t.Fatalf("header = %q, want no published count while the parent has an active child", header)
	}
	if got := m.countNeedAttention(); got != 1 {
		t.Fatalf("countNeedAttention() = %d, want 1 from the active child's attention", got)
	}
}

// TestHeaderCountsPlainPublishedAsPublished pins the unchanged baseline: a
// published feature without an active child still counts as published.
func TestHeaderCountsPlainPublishedAsPublished(t *testing.T) {
	t.Parallel()

	plain := &feature.Feature{
		ID: "hdr-plain", Name: "Plain Published", Slug: "plain-published",
		Status: feature.StatusPublished, CurrentPhase: feature.PhasePublish,
	}
	m := NewDashboardModel([]*feature.Feature{plain}, "")
	header := stripANSI(m.renderHeader(120))
	if !strings.Contains(header, "1 published") || !strings.Contains(header, "0 active") {
		t.Fatalf("header = %q, want 0 active, 1 published for a plain published feature", header)
	}
	if got := m.countNeedAttention(); got != 0 {
		t.Fatalf("countNeedAttention() = %d, want 0", got)
	}
}
