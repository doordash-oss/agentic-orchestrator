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

package server

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type readModelCleanlinessWorktrees struct {
	feature.WorktreeOps
	report *git.CleanlinessReport
}

func (w *readModelCleanlinessWorktrees) InspectCleanliness(string, int) (*git.CleanlinessReport, error) {
	return w.report, nil
}

// seedReadChildFeature persists an active refactor child under parentID with
// the given durable status/setup state and returns it.
func seedReadChildFeature(t *testing.T, store *feature.Store, parentID string, status feature.Status, setupStatus feature.SetupStatus, setupErr string) *feature.Feature {
	t.Helper()
	child := &feature.Feature{
		ID: parentID + "-child", Name: "Rework auth", Slug: "rework-auth",
		Status: status, CurrentPhase: feature.PhaseImplement, Created: time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC),
		Repos:         []feature.FeatureRepo{{Name: repoNameSelf, Branch: "feature/rework-auth"}},
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID: parentID,
			Kind:     feature.ChildKindRefactor,
			Bases:    []feature.ChildRepoBase{{Repo: repoNameSelf, SHA: "deadbeefcafe", ParentBranch: "main"}},
		},
	}
	child.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Status: setupStatus, LastError: setupErr}})
	if setupStatus == feature.SetupStatusFailed {
		child.FailureType = feature.FailureWorktreeSetup
		child.LastError = setupErr
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("Save(child) error = %v", err)
	}
	return child
}

// TestChildFeatureExcludedFromTopLevelListButDetailWorks pins the projection
// split: children are reachable by id but never appear in the top-level list,
// while the parent summary carries the active child.
func TestChildFeatureExcludedFromTopLevelListButDetailWorks(t *testing.T) {
	t.Parallel()
	store, parent := seedReadFeature(t)
	child := seedReadChildFeature(t, store, parent.ID, feature.StatusSettingUpWorktrees, feature.SetupStatusQueued, "")
	handler := NewHandler(baseReadHandlerOptions(store))

	list := getJSONMap(t, handler, apiPathFeatures)
	summaries := list["features"].([]any)
	if len(summaries) != 1 {
		t.Fatalf("list features len = %d, want only the top-level parent", len(summaries))
	}
	summaryDTO := summaries[0].(map[string]any)
	if summaryDTO["id"] != parent.ID {
		t.Fatalf("list summary id = %v, want parent %s", summaryDTO["id"], parent.ID)
	}
	activeChild, ok := summaryDTO["active_child"].(map[string]any)
	if !ok {
		t.Fatalf("parent summary active_child missing in %+v", summaryDTO)
	}
	if activeChild["id"] != child.ID || activeChild["kind"] != feature.ChildKindRefactor {
		t.Fatalf("active_child = %+v, want child %s of kind refactor", activeChild, child.ID)
	}
	if activeChild["relationship_state"] != "setting_up" || activeChild["setup_status"] != string(feature.SetupStatusQueued) {
		t.Fatalf("active_child = %+v, want setting_up with queued setup", activeChild)
	}
	if activeChild["status"] != feature.StatusSettingUpWorktrees.String() {
		t.Fatalf("active_child.status = %v, want %s", activeChild["status"], feature.StatusSettingUpWorktrees.String())
	}

	detail := getJSONMap(t, handler, "/api/v1/features/"+child.ID)
	featureBody := detail[entityFeature].(map[string]any)
	if featureBody["id"] != child.ID {
		t.Fatalf("child detail id = %v, want %s", featureBody["id"], child.ID)
	}
	if featureBody["parent_id"] != parent.ID || featureBody["parent_kind"] != feature.ChildKindRefactor {
		t.Fatalf("child detail parent linkage = %+v, want parent %s refactor", featureBody, parent.ID)
	}
	if featureBody["active"] != true {
		t.Fatalf("child detail active = %v, want true for an open relationship", featureBody["active"])
	}
	bases := featureBody["bases"].([]any)
	if len(bases) != 1 {
		t.Fatalf("child detail bases = %+v, want one captured base", bases)
	}
	base := bases[0].(map[string]any)
	if base["repo"] != repoNameSelf || base["sha"] != "deadbeefcafe" || base["parent_branch"] != "main" {
		t.Fatalf("child detail base = %+v, want exact captured provenance", base)
	}
	if _, ok := featureBody["setup_complete"]; ok {
		t.Fatalf("child detail setup_complete present while setup is queued: %+v", featureBody)
	}
	// The restricted child catalog (capability-gated execution entries plus
	// setup-retry and discard) must serialize. Child cleanup and
	// single-record delete are unavailable while the relationship is active.
	actions := featureBody["actions"].([]any)
	ids := map[string]bool{}
	for _, raw := range actions {
		ids[raw.(map[string]any)["id"].(string)] = true
	}
	want := map[string]bool{actionStart: true, actionPauseStop: true, actionResume: true, actionRestart: true, actionRetry: true, actionDiscard: true}
	if len(actions) != len(want) {
		t.Fatalf("child detail actions = %v, want %v", ids, want)
	}
	for id := range want {
		if !ids[id] {
			t.Fatalf("child detail actions = %v, missing %q", ids, id)
		}
	}
}

func TestParentDetailIgnoresLegacyProviderStateInFeatureStore(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	for _, name := range []string{"opencode", "codex-home"} {
		dir := filepath.Join(store.BaseDir, name, "managed-session")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "provider.json"), []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", dir, err)
		}
	}

	handler := NewHandler(baseReadHandlerOptions(store))
	detail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)
	if got := detail[entityFeature].(map[string]any)["id"]; got != parent.ID {
		t.Fatalf("parent detail id = %v, want %s", got, parent.ID)
	}
}

// TestParentProjectionsCarryActiveChildDerivedState covers the derived-state
// matrix on both parent summary and parent detail: failed setup surfaces
// last_error and stays setting_up; done setup flips to setup_complete.
func TestParentProjectionsCarryActiveChildDerivedState(t *testing.T) {
	t.Parallel()

	t.Run("failed setup", func(t *testing.T) {
		t.Parallel()
		store, parent := seedReadFeature(t)
		seedReadChildFeature(t, store, parent.ID, feature.StatusFailed, feature.SetupStatusFailed, gitWorktreeAddFailedMsg)
		handler := NewHandler(baseReadHandlerOptions(store))

		detail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)
		activeChild := detail[entityFeature].(map[string]any)["active_child"].(map[string]any)
		if activeChild["relationship_state"] != "setting_up" || activeChild["setup_status"] != string(feature.SetupStatusFailed) {
			t.Fatalf("active_child = %+v, want setting_up with failed setup", activeChild)
		}
		if activeChild["last_error"] != gitWorktreeAddFailedMsg {
			t.Fatalf("active_child last_error = %v, want setup failure", activeChild["last_error"])
		}
	})

	t.Run("setup complete", func(t *testing.T) {
		t.Parallel()
		store, parent := seedReadFeature(t)
		seedReadChildFeature(t, store, parent.ID, feature.StatusCreated, feature.SetupStatusDone, "")
		handler := NewHandler(baseReadHandlerOptions(store))

		detail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)
		activeChild := detail[entityFeature].(map[string]any)["active_child"].(map[string]any)
		if activeChild["relationship_state"] != "active" || activeChild["setup_status"] != string(feature.SetupStatusDone) {
			t.Fatalf("active_child = %+v, want active", activeChild)
		}
		if _, ok := activeChild["last_error"]; ok {
			t.Fatalf("active_child = %+v, want no last_error for a completed setup", activeChild)
		}

		list := getJSONMap(t, handler, apiPathFeatures)
		summaries := list["features"].([]any)
		if len(summaries) != 1 {
			t.Fatalf("list features len = %d, want only the parent", len(summaries))
		}
		summaryChild := summaries[0].(map[string]any)["active_child"].(map[string]any)
		if summaryChild["relationship_state"] != "active" {
			t.Fatalf("list summary active_child = %+v, want active", summaryChild)
		}
	})

	t.Run("no child", func(t *testing.T) {
		t.Parallel()
		store, parent := seedReadFeature(t)
		handler := NewHandler(baseReadHandlerOptions(store))

		detail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)
		if _, ok := detail[entityFeature].(map[string]any)["active_child"]; ok {
			t.Fatalf("parent detail carries active_child without a child: %+v", detail[entityFeature])
		}
	})
}

func TestParentProjectionsCarryCompleteRelationshipHistory(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	active := seedReadChildFeature(t, store, parent.ID, feature.StatusImplementing, feature.SetupStatusDone, "")
	active.Name = "Active refactor"
	activeStartedAt := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	active.StartedAt = &activeStartedAt
	active.PhaseCosts = map[string]float64{"implement": 1.25}
	if err := store.Save(active); err != nil {
		t.Fatalf("Save(active child) error = %v", err)
	}

	closedAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	closed := &feature.Feature{
		ID:            parent.ID + "-closed",
		Name:          "Completed refactor",
		Slug:          "completed-refactor",
		Status:        feature.StatusReviewPassed,
		CurrentPhase:  feature.PhaseImplement,
		Created:       time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC),
		Repos:         []feature.FeatureRepo{{Name: repoNameSelf}},
		Pipeline:      feature.PipelineMedium,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID:     parent.ID,
			Kind:         feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeCompleted,
			ClosedAt:     &closedAt,
		},
	}
	closed.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Status: feature.SetupStatusDone}})
	closedStartedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	closed.StartedAt = &closedStartedAt
	closed.PhaseCosts = map[string]float64{"implement": 2.5}
	closed.Parent.Transaction = &feature.TransactionJournal{
		Phase:     feature.TransactionPhaseMerged,
		Attention: "manual inspection required",
		Entries: []feature.RepoTransactionEntry{{
			Repo:           repoNameSelf,
			CleanupWarning: "branch cleanup pending",
		}},
	}
	if err := store.Save(closed); err != nil {
		t.Fatalf("Save(closed child) error = %v", err)
	}

	handler := NewHandler(baseReadHandlerOptions(store))
	list := getJSONMap(t, handler, apiPathFeatures)
	parentSummary := list["features"].([]any)[0].(map[string]any)
	assertRelationshipProjection(t, parentSummary["active_child"], active.ID, "", "Active")

	history, ok := parentSummary["child_history"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("parent list child_history = %#v, want one closed child", parentSummary["child_history"])
	}
	assertRelationshipProjection(t, history[0], closed.ID, feature.ChildCloseOutcomeCompleted, "Closed — Completed")

	parentDetail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	if got := mustMarshalJSON(t, parentDetail["child_history"]); got != mustMarshalJSON(t, parentSummary["child_history"]) {
		t.Fatalf("parent detail history = %s, want list projection %s", got, mustMarshalJSON(t, parentSummary["child_history"]))
	}

	childDetail := getJSONMap(t, handler, "/api/v1/features/"+closed.ID)[entityFeature].(map[string]any)
	if got := mustMarshalJSON(t, childDetail["relationship"]); got != mustMarshalJSON(t, history[0]) {
		t.Fatalf("closed child relationship = %s, want parent projection %s", got, mustMarshalJSON(t, history[0]))
	}
}

// TestClosedChildSurfacesPreservedDiffSummary pins the API contract for the
// preserved close-time diff: a closed child surfaces `diff_summary`, while
// open children and children closed without a captured diff omit the field.
func TestClosedChildSurfacesPreservedDiffSummary(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	active := seedReadChildFeature(t, store, parent.ID, feature.StatusImplementing, feature.SetupStatusDone, "")

	closedAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	diffSummary := "Repository: " + repoNameSelf + "\ndiff --git a/auth.go b/auth.go\n+session rotation"
	withDiff := &feature.Feature{
		ID: parent.ID + "-closed-diff", Name: "Closed with diff", Slug: "closed-with-diff",
		Status: feature.StatusReviewPassed, CurrentPhase: feature.PhaseImplement,
		Created:   time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC),
		Repos:     []feature.FeatureRepo{{Name: repoNameSelf}},
		ActiveRun: 1, RunCount: 1, SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID, Kind: feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeCompleted, ClosedAt: &closedAt,
			DiffSummary: diffSummary,
		},
	}
	withDiff.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Status: feature.SetupStatusDone}})
	if err := store.Save(withDiff); err != nil {
		t.Fatalf("Save(withDiff) error = %v", err)
	}

	olderClosedAt := closedAt.Add(-time.Hour)
	withoutDiff := &feature.Feature{
		ID: parent.ID + "-closed-clean", Name: "Closed without diff", Slug: "closed-clean",
		Status: feature.StatusReviewPassed, CurrentPhase: feature.PhaseImplement,
		Created:   time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC),
		Repos:     []feature.FeatureRepo{{Name: repoNameSelf}},
		ActiveRun: 1, RunCount: 1, SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID, Kind: feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeDiscarded, ClosedAt: &olderClosedAt,
		},
	}
	withoutDiff.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Status: feature.SetupStatusDone}})
	if err := store.Save(withoutDiff); err != nil {
		t.Fatalf("Save(withoutDiff) error = %v", err)
	}

	handler := NewHandler(baseReadHandlerOptions(store))
	parentSummary := getJSONMap(t, handler, apiPathFeatures)["features"].([]any)[0].(map[string]any)

	if got := parentSummary["active_child"].(map[string]any)["diff_summary"]; got != nil {
		t.Fatalf("active child diff_summary = %#v, want omitted", got)
	}
	history := parentSummary["child_history"].([]any)
	seen := map[string]map[string]any{}
	for _, raw := range history {
		item := raw.(map[string]any)
		seen[item["id"].(string)] = item
	}
	if got := seen[withDiff.ID]["diff_summary"]; got != diffSummary {
		t.Fatalf("closed child diff_summary = %#v, want preserved summary", got)
	}
	if got, ok := seen[withoutDiff.ID]["diff_summary"]; ok {
		t.Fatalf("no-diff closed child diff_summary = %#v, want omitted", got)
	}
	_ = active
}

// TestClosedChildDiffSummaryBoundedInResponse proves an oversized persisted
// diff summary (a pre-fix record holding a raw multi-megabyte diff) can no
// longer inflate the feature-detail response: the DTO value is capped with a
// truncation marker.
func TestClosedChildDiffSummaryBoundedInResponse(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	closedAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	hugeDiff := "Repository: " + repoNameSelf + "\n" +
		strings.Repeat("+a persisted raw diff line from before the bound existed\n", (feature.DiffSummaryBudget*4)/57)
	closed := &feature.Feature{
		ID: parent.ID + "-closed-huge", Name: "Closed with huge diff", Slug: "closed-huge",
		Status: feature.StatusReviewPassed, CurrentPhase: feature.PhaseImplement,
		Created:   time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC),
		Repos:     []feature.FeatureRepo{{Name: repoNameSelf}},
		ActiveRun: 1, RunCount: 1, SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID, Kind: feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeCompleted, ClosedAt: &closedAt,
			DiffSummary: hugeDiff,
		},
	}
	closed.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Status: feature.SetupStatusDone}})
	if err := store.Save(closed); err != nil {
		t.Fatalf("Save(closed) error = %v", err)
	}

	handler := NewHandler(baseReadHandlerOptions(store))
	detail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	history := detail["child_history"].([]any)
	var got string
	for _, raw := range history {
		item := raw.(map[string]any)
		if item["id"] == closed.ID {
			got = item["diff_summary"].(string)
		}
	}
	if got == "" || len(got) > feature.DiffSummaryBudget {
		t.Fatalf("diff_summary length = %d, want non-empty and <= %d", len(got), feature.DiffSummaryBudget)
	}
	if !strings.HasSuffix(got, " bytes omitted]") {
		t.Fatalf("diff_summary missing truncation marker, tail = %q", got[len(got)-120:])
	}
}

func TestParentDeleteRemainsEnabledDuringActiveRelationship(t *testing.T) {
	t.Parallel()

	parent := &feature.Feature{ID: "parent-1", Status: feature.StatusImplementing}
	actions := actionCatalogDTOsWithChildGuard(parent, true)
	deleteAction := actionDTOByID(t, actions, actionDelete)
	if !deleteAction.Enabled {
		t.Fatalf("parent delete disabled = %+v, want cascade delete enabled", deleteAction.DisabledReasons)
	}
	for _, action := range actions {
		if action.ID == actionDelete {
			continue
		}
		if action.Enabled {
			t.Fatalf("parent action %q enabled during active relationship", action.ID)
		}
		if len(action.DisabledReasons) == 0 || action.DisabledReasons[0].Code != disabledParentHasActiveChild.Code {
			t.Fatalf("parent action %q reasons = %+v, want %q", action.ID, action.DisabledReasons, disabledParentHasActiveChild.Code)
		}
	}
}

func TestParentRefactorActionReportsDirtyParent(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	parent.Status = feature.StatusPublished
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}
	opts := baseReadHandlerOptions(store)
	opts.Freshness = StaticFreshnessProvider{repoNameSelf: RepoFreshnessLocalChanges}
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	for _, raw := range body["actions"].([]any) {
		action := raw.(map[string]any)
		if action["id"] != actionRefactor {
			continue
		}
		if action["enabled"] != false {
			t.Fatalf("refactor enabled = %v, want dirty-parent rejection", action["enabled"])
		}
		reasons := action["disabled_reasons"].([]any)
		if len(reasons) == 0 || reasons[0].(map[string]any)["code"] != "dirty_parent" {
			t.Fatalf("refactor disabled reasons = %+v, want dirty_parent", reasons)
		}
		return
	}
	t.Fatal("refactor action missing")
}

func TestParentRefactorDirtyReasonCarriesDiagnostics(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	parent.Status = feature.StatusPublished
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}
	opts := baseReadHandlerOptions(store)
	opts.Freshness = StaticFreshnessProvider{repoNameSelf: RepoFreshnessLocalChanges}
	opts.Worktrees = &readModelCleanlinessWorktrees{
		report: &git.CleanlinessReport{
			Staged:         []string{"staged.txt"},
			Unstaged:       []string{"unstaged.txt"},
			Untracked:      []string{"untracked-a.txt", "untracked-b.txt"},
			StagedTotal:    1,
			UnstagedTotal:  1,
			UntrackedTotal: 2,
		},
	}
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	for _, raw := range body["actions"].([]any) {
		action := raw.(map[string]any)
		if action["id"] != actionRefactor {
			continue
		}
		reasons := action["disabled_reasons"].([]any)
		if len(reasons) == 0 {
			t.Fatalf("refactor disabled reasons = %+v, want dirty_parent", reasons)
		}
		target, ok := reasons[0].(map[string]any)["target"].(map[string]any)
		if !ok {
			t.Fatalf("dirty_parent reason target = %#v, want diagnostics payload", reasons[0])
		}
		repos, ok := target["repos"].([]any)
		if !ok || len(repos) != 1 {
			t.Fatalf("dirty_parent target.repos = %#v, want one repo entry", target)
		}
		repo := repos[0].(map[string]any)
		if repo["repo"] != repoNameSelf {
			t.Fatalf("diagnostics repo = %v, want %q", repo["repo"], repoNameSelf)
		}
		for key, want := range map[string]float64{"staged_total": 1, "unstaged_total": 1, "untracked_total": 2} {
			if repo[key] != want {
				t.Fatalf("diagnostics %s = %#v, want %v (full repo entry: %#v)", key, repo[key], want, repo)
			}
		}
		return
	}
	t.Fatal("refactor action missing")
}

func TestParentRefactorDirtyReasonWithoutCleanlinessOmitsTarget(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	parent.Status = feature.StatusPublished
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}
	opts := baseReadHandlerOptions(store)
	opts.Freshness = StaticFreshnessProvider{repoNameSelf: RepoFreshnessLocalChanges}
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	for _, raw := range body["actions"].([]any) {
		action := raw.(map[string]any)
		if action["id"] != actionRefactor {
			continue
		}
		reasons := action["disabled_reasons"].([]any)
		if len(reasons) == 0 {
			t.Fatalf("refactor disabled reasons = %+v, want dirty_parent", reasons)
		}
		if target, ok := reasons[0].(map[string]any)["target"]; ok {
			t.Fatalf("dirty_parent reason target = %#v, want none without cleanliness wiring", target)
		}
		return
	}
	t.Fatal("refactor action missing")
}

func TestReviewFeedbackActionCatalogEligibility(t *testing.T) {
	t.Parallel()
	publishable := true
	base := actionCatalogTestFeature(feature.StatusPublished, feature.Checkpoints{}, &publishable)
	base.RepoStates = map[string]*feature.RepoState{
		repoNameSelf: {PRURL: "https://github.example/org/repo/pull/17"},
	}

	tests := []struct {
		name             string
		feature          *feature.Feature
		hasActiveChild   bool
		wantEnabled      bool
		wantDisabledCode string
	}{
		{name: "published parent with pull request", feature: base, wantEnabled: true},
		{
			name: "no pull request",
			feature: func() *feature.Feature {
				copy := *base
				copy.RepoStates = map[string]*feature.RepoState{repoNameSelf: {}}
				return &copy
			}(),
			wantDisabledCode: "no_pull_request",
		},
		{
			name:             "active child",
			feature:          base,
			hasActiveChild:   true,
			wantDisabledCode: "active_child_present",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := actionDTOByID(t, actionCatalogDTOsWithChildGuard(tt.feature, tt.hasActiveChild), actionReviewFeedback)
			if action.Enabled != tt.wantEnabled {
				t.Fatalf("review-feedback enabled = %v; want %v", action.Enabled, tt.wantEnabled)
			}
			if tt.wantEnabled {
				if len(action.RequiredInputs) != 0 || action.Scope.Type != entityFeature || action.Scope.RepoSelection != "" {
					t.Fatalf("review-feedback metadata = %+v; want feature scope with no catalog inputs", action)
				}
				return
			}
			if len(action.DisabledReasons) == 0 || action.DisabledReasons[0].Code != tt.wantDisabledCode {
				t.Fatalf("review-feedback disabled reasons = %+v; want %q first", action.DisabledReasons, tt.wantDisabledCode)
			}
		})
	}
}

func TestParentReviewFeedbackDirtyReasonCarriesDiagnostics(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	parent.Status = feature.StatusPublished
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}
	opts := baseReadHandlerOptions(store)
	opts.Worktrees = &readModelCleanlinessWorktrees{report: &git.CleanlinessReport{
		Unstaged:      []string{"review.go"},
		UnstagedTotal: 1,
	}}
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	for _, raw := range body["actions"].([]any) {
		action := raw.(map[string]any)
		if action["id"] != actionReviewFeedback {
			continue
		}
		if action["enabled"] != false {
			t.Fatalf("review-feedback enabled = %v; want dirty-parent rejection", action["enabled"])
		}
		reasons := action["disabled_reasons"].([]any)
		if len(reasons) == 0 || reasons[0].(map[string]any)["code"] != "dirty_parent" {
			t.Fatalf("review-feedback disabled reasons = %+v; want dirty_parent", reasons)
		}
		return
	}
	t.Fatal("review-feedback action missing")
}

func assertRelationshipProjection(t *testing.T, raw any, wantID, wantOutcome, wantDisplayPrefix string) {
	t.Helper()
	projection, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("relationship projection = %#v, want object", raw)
	}
	outcome, _ := projection["outcome"].(string)
	if projection["id"] != wantID || outcome != wantOutcome {
		t.Fatalf("relationship identity/outcome = %#v, want id=%q outcome=%q", projection, wantID, wantOutcome)
	}
	display, _ := projection["display_state"].(string)
	if !strings.HasPrefix(display, wantDisplayPrefix) {
		t.Fatalf("relationship display_state = %q, want prefix %q", display, wantDisplayPrefix)
	}
	for _, field := range []string{"display_token", "pipeline", "started_at", "cost", "integration_state", "attention", "cleanup_warnings"} {
		if _, exists := projection[field]; !exists {
			t.Fatalf("relationship projection missing %q: %#v", field, projection)
		}
	}
}

// TestChildDetailExposesSetupComplete pins the derived setup_complete flag on
// the child's own detail once its durable setup finished.
func TestChildDetailExposesSetupComplete(t *testing.T) {
	t.Parallel()
	store, parent := seedReadFeature(t)
	child := seedReadChildFeature(t, store, parent.ID, feature.StatusCreated, feature.SetupStatusDone, "")
	handler := NewHandler(baseReadHandlerOptions(store))

	detail := getJSONMap(t, handler, "/api/v1/features/"+child.ID)
	featureBody := detail[entityFeature].(map[string]any)
	if featureBody["setup_complete"] != true {
		t.Fatalf("child detail setup_complete = %v, want true", featureBody["setup_complete"])
	}
	active := featureBody["active_run_detail"].(map[string]any)
	setupBody, ok := active["setup"].(map[string]any)
	if !ok || setupBody["status"] != string(feature.SetupStatusDone) {
		t.Fatalf("child detail setup block = %+v, want done durable setup", active["setup"])
	}
}

// TestReviewFeedbackChildDetailExposesPersistedComments pins the projection
// of the persisted selected-comment artifact onto the child's own feature
// detail read model. The comments must appear in persisted order with all
// stored fields intact, and the field must be absent for refactor children
// and parents.
func TestReviewFeedbackChildDetailExposesPersistedComments(t *testing.T) {
	t.Parallel()

	comments := []feature.ReviewFeedbackComment{
		{Repo: "org/repo-a", ID: 101, Type: "review", Path: "src/a.go", Line: 42, Author: "alice", Body: "nit: use context"},
		{Repo: "org/repo-b", ID: 202, Type: "issue", Author: "bob", Body: "this breaks"},
	}

	t.Run("review-feedback child carries persisted comments in order", func(t *testing.T) {
		t.Parallel()
		store, parent := seedReadFeature(t)
		child := &feature.Feature{
			ID: parent.ID + "-rfchild", Name: "Address review feedback", Slug: "review-feedback",
			Status: feature.StatusSettingUpWorktrees, CurrentPhase: feature.PhaseImplement,
			Created:       time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC),
			Repos:         []feature.FeatureRepo{{Name: repoNameSelf, Branch: "feature/rf"}},
			ActiveRun:     1,
			RunCount:      1,
			SchemaVersion: feature.SchemaVersionCurrent,
			Parent: &feature.ChildRelationship{
				ParentID: parent.ID,
				Kind:     feature.ChildKindReviewFeedback,
				Bases:    []feature.ChildRepoBase{{Repo: repoNameSelf, SHA: "deadbeef", ParentBranch: "main"}},
			},
			ReviewFeedback: comments,
		}
		child.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Status: feature.SetupStatusQueued}})
		if err := store.Save(child); err != nil {
			t.Fatalf("Save(child) error = %v", err)
		}

		handler := NewHandler(baseReadHandlerOptions(store))
		detail := getJSONMap(t, handler, "/api/v1/features/"+child.ID)
		body := detail[entityFeature].(map[string]any)
		rawComments, ok := body["review_feedback"]
		if !ok {
			t.Fatalf("child detail missing review_feedback: keys=%v", sortedKeys(body))
		}
		list, ok := rawComments.([]any)
		if !ok || len(list) != 2 {
			t.Fatalf("review_feedback = %#v, want 2-element array", rawComments)
		}
		first := list[0].(map[string]any)
		if first["repo"] != "org/repo-a" || first["id"] != float64(101) || first["type"] != "review" || first["path"] != "src/a.go" {
			t.Fatalf("first comment = %#v, want repo-a/101/review/src/a.go", first)
		}
		second := list[1].(map[string]any)
		if second["repo"] != "org/repo-b" || second["id"] != float64(202) || second["type"] != "issue" {
			t.Fatalf("second comment = %#v, want repo-b/202/issue", second)
		}
	})

	t.Run("refactor child omits review_feedback", func(t *testing.T) {
		t.Parallel()
		store, parent := seedReadFeature(t)
		refactorChild := seedReadChildFeature(t, store, parent.ID, feature.StatusCreated, feature.SetupStatusDone, "")
		handler := NewHandler(baseReadHandlerOptions(store))
		detail := getJSONMap(t, handler, "/api/v1/features/"+refactorChild.ID)
		body := detail[entityFeature].(map[string]any)
		if _, exists := body["review_feedback"]; exists {
			t.Fatalf("refactor child detail must not carry review_feedback")
		}
	})

	t.Run("parent omits review_feedback", func(t *testing.T) {
		t.Parallel()
		store, _ := seedReadFeature(t)
		handler := NewHandler(baseReadHandlerOptions(store))
		parentID := fixtureFeatureIDAlt
		detail := getJSONMap(t, handler, "/api/v1/features/"+parentID)
		body := detail[entityFeature].(map[string]any)
		if _, exists := body["review_feedback"]; exists {
			t.Fatalf("parent detail must not carry review_feedback")
		}
	})
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestEventDTOFromDomainMapsRelationshipResource(t *testing.T) {
	t.Parallel()

	childEvent := eventDTOFromDomain(ports.Event{
		Type:     ports.RelationshipChildCreated,
		ParentID: "parent-1",
		ChildID:  "child-1",
	})
	if childEvent.Kind != sseEventLifecycleUpdated || childEvent.Resource.Type != resourceTypeRelationship {
		t.Fatalf("relationship event = %+v, want lifecycle.updated relationship resource", childEvent)
	}
	if childEvent.Resource.ParentID != "parent-1" || childEvent.Resource.ChildID != "child-1" {
		t.Fatalf("relationship resource = %+v, want parent_id + child_id", childEvent.Resource)
	}
	if childEvent.Resource.ID == "" || !childEvent.SnapshotRequired {
		t.Fatalf("relationship event = %+v, want stable id and snapshot_required", childEvent)
	}

	cascadeProgress := eventDTOFromDomain(ports.Event{
		Type:     ports.RelationshipCascadeProgress,
		ParentID: "parent-1",
		ChildID:  "child-1",
	})
	if cascadeProgress.Resource.Type != resourceTypeRelationship ||
		cascadeProgress.Resource.RelationshipDeleted ||
		!cascadeProgress.SnapshotRequired {
		t.Fatalf("cascade progress event = %+v, want retained relationship snapshot bundle", cascadeProgress)
	}

	topLevel := eventDTOFromDomain(ports.Event{Type: ports.FeatureStarted, FeatureID: "parent-1"})
	if topLevel.Resource.ParentID != "" || topLevel.Resource.ChildID != "" {
		t.Fatalf("top-level event resource = %+v, want no relationship ids", topLevel.Resource)
	}
}
