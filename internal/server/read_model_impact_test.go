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
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// actionJSONByID returns the raw JSON projection of one action from a detail
// feature body.
func actionJSONByID(t *testing.T, featureBody map[string]any, id string) map[string]any {
	t.Helper()
	for _, raw := range featureBody["actions"].([]any) {
		action := raw.(map[string]any)
		if action["id"] == id {
			return action
		}
	}
	t.Fatalf("detail actions missing %q", id)
	return nil
}

// impactCategoryItems returns the item entries of one preview category, and
// fails when the always-complete category set is missing a key.
func impactCategoryItems(t *testing.T, preview map[string]any, key string) []any {
	t.Helper()
	categories, ok := preview["categories"].([]any)
	if !ok {
		t.Fatalf("preview categories = %#v, want array", preview["categories"])
	}
	for _, raw := range categories {
		category := raw.(map[string]any)
		if category["key"] == key {
			items, ok := category["items"].([]any)
			if !ok {
				t.Fatalf("category %q items = %#v, want array (absent categories serialize empty, not omitted)", key, category["items"])
			}
			return items
		}
	}
	t.Fatalf("preview missing category %q: %+v", key, preview["categories"])
	return nil
}

func impactCategoryKeys(t *testing.T, preview map[string]any, want []string) {
	t.Helper()
	categories, ok := preview["categories"].([]any)
	if !ok {
		t.Fatalf("preview categories = %#v, want array", preview["categories"])
	}
	got := make([]string, 0, len(categories))
	for _, raw := range categories {
		category := raw.(map[string]any)
		if _, ok = category["label"].(string); !ok {
			t.Fatalf("category missing label: %+v", category)
		}
		got = append(got, category["key"].(string))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("preview category keys = %v, want %v", got, want)
	}
}

func previewRetained(t *testing.T, preview map[string]any, want []string) {
	t.Helper()
	raw, ok := preview["retained"].([]any)
	if !ok {
		t.Fatalf("preview retained = %#v, want array (empty counts must serialize, not omit)", preview["retained"])
	}
	got := make([]string, 0, len(raw))
	for _, item := range raw {
		got = append(got, item.(string))
	}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("preview retained = %v, want %v", got, want)
	}
}

func itemsContain(t *testing.T, items []any, substrings ...string) {
	t.Helper()
	for _, fragment := range substrings {
		found := false
		for _, item := range items {
			if strings.Contains(item.(string), fragment) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("items %v missing entry containing %q", items, fragment)
		}
	}
}

// TestDiscardActionCarriesStructuredImpactPreview pins the discard preview on
// an active child: every category serialized, repo-derived worktree/branch
// entries, live-session naming, and both retained statements.
func TestDiscardActionCarriesStructuredImpactPreview(t *testing.T) {
	t.Parallel()
	store, parent := seedReadFeature(t)
	child := seedReadChildFeature(t, store, parent.ID, feature.StatusCreated, feature.SetupStatusDone, "")
	child.Repos = []feature.FeatureRepo{{
		Name:         repoNameSelf,
		Path:         "/repos/" + repoNameSelf,
		WorktreePath: "/worktrees/" + child.ID + "/" + repoNameSelf,
		Branch:       "feature/rework-auth",
	}}
	if err := store.Save(child); err != nil {
		t.Fatalf("Save(child) error = %v", err)
	}
	// Seed the durable workspace state the discard preview uses as evidence
	// that a temporary knowledge workspace actually exists for this repo.
	for _, repo := range child.Repos {
		workspaceDir := feature.ChildKBWorkspaceDir(store.BaseDir, child.ID, repo.Name)
		if err := feature.SaveWorkspaceState(workspaceDir, &feature.ChildKBWorkspaceState{
			Source: feature.WorkspaceSourceCanonical,
		}); err != nil {
			t.Fatalf("SaveWorkspaceState(%s) error = %v", repo.Name, err)
		}
	}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		id:        "sess-implement-1",
		featureID: child.ID,
		label:     "Implement phase-1",
		status:    ports.SessionRunning,
		startedAt: time.Date(2026, 7, 28, 11, 0, 0, 0, time.UTC),
	}}}
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/features/"+child.ID)[entityFeature].(map[string]any)
	discard := actionJSONByID(t, body, actionDiscard)
	if discard["enabled"] != true {
		t.Fatalf("discard enabled = %v, want true for an active child", discard["enabled"])
	}
	preview, ok := discard["impact_preview"].(map[string]any)
	if !ok {
		t.Fatalf("discard action missing impact_preview: %+v", discard)
	}
	if preview["kind"] != string(ChildDiscard) {
		t.Fatalf("preview kind = %v, want %s", preview["kind"], string(ChildDiscard))
	}
	subject := preview["subject"].(map[string]any)
	if subject["id"] != child.ID || subject["name"] != "Rework auth" {
		t.Fatalf("preview subject = %+v, want child %s Rework auth", subject, child.ID)
	}
	impactCategoryKeys(t, preview, []string{"sessions", "worktrees", "branches", "knowledge"})
	itemsContain(t, impactCategoryItems(t, preview, "sessions"), "Implement phase-1", "sess-implement-1")
	itemsContain(t, impactCategoryItems(t, preview, "worktrees"), "/worktrees/"+child.ID, repoNameSelf)
	itemsContain(t, impactCategoryItems(t, preview, "branches"), "feature/rework-auth", repoNameSelf)
	itemsContain(t, impactCategoryItems(t, preview, "knowledge"), "knowledge", repoNameSelf)
	previewRetained(t, preview, []string{impactRetainedReviewConfig, impactRetainedDiscardHistory})

	for _, raw := range body["actions"].([]any) {
		action := raw.(map[string]any)
		if action["id"] == actionDiscard {
			continue
		}
		if _, present := action["impact_preview"]; present {
			t.Fatalf("action %q unexpectedly carries an impact_preview", action["id"])
		}
	}
}

// TestDiscardImpactPreviewSessionFallback pins the record-ledger session
// naming used when no live session manager is wired: sessions are projected
// from the child's durable run ledger rather than silently showing none.
func TestDiscardImpactPreviewSessionFallback(t *testing.T) {
	t.Parallel()
	store, parent := seedReadFeature(t)
	child := seedReadChildFeature(t, store, parent.ID, feature.StatusCreated, feature.SetupStatusDone, "")
	child.SessionCosts = []feature.SessionCostRecord{
		{SessionID: "sess-plan-1", PhaseKey: "phase-1-plan", CostUSD: 0.12},
		{SessionID: "sess-impl-1", PhaseKey: "phase-1-implement", CostUSD: 0.34},
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("Save(child) error = %v", err)
	}
	handler := NewHandler(baseReadHandlerOptions(store))

	body := getJSONMap(t, handler, "/api/v1/features/"+child.ID)[entityFeature].(map[string]any)
	discard := actionJSONByID(t, body, actionDiscard)
	preview, ok := discard["impact_preview"].(map[string]any)
	if !ok {
		t.Fatalf("discard action missing impact_preview: %+v", discard)
	}
	items := impactCategoryItems(t, preview, "sessions")
	if len(items) != 2 {
		t.Fatalf("fallback session items = %v, want the two recorded sessions", items)
	}
	itemsContain(t, items, "phase-1-plan", "sess-plan-1", "phase-1-implement", "sess-impl-1")
}

// TestParentDeleteCarriesCascadeImpactPreview pins the cascade delete preview
// on a parent with an active child and closed-child history: both children
// enumerated, all six categories present, relationship history named, and the
// retained array explicit (empty) rather than omitted.
func TestParentDeleteCarriesCascadeImpactPreview(t *testing.T) {
	t.Parallel()
	store, parent := seedReadFeature(t)
	active := seedReadChildFeature(t, store, parent.ID, feature.StatusImplementing, feature.SetupStatusDone, "")
	active.Name = "Active refactor"
	active.Repos = []feature.FeatureRepo{{
		Name:         repoNameSelf,
		WorktreePath: "/worktrees/" + active.ID + "/" + repoNameSelf,
		Branch:       "feature/active-refactor",
	}}
	if err := store.Save(active); err != nil {
		t.Fatalf("Save(active child) error = %v", err)
	}

	closedAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	closed := &feature.Feature{
		ID:           parent.ID + "-closed",
		Name:         "Completed refactor",
		Slug:         "completed-refactor",
		Status:       feature.StatusReviewPassed,
		CurrentPhase: feature.PhaseImplement,
		Created:      time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC),
		Repos:        []feature.FeatureRepo{{Name: repoNameSelf, Branch: "feature/completed-refactor"}},
		ActiveRun:    1,
		RunCount:     1,
		Parent: &feature.ChildRelationship{
			ParentID:     parent.ID,
			Kind:         feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeCompleted,
			ClosedAt:     &closedAt,
		},
	}
	closed.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Status: feature.SetupStatusDone}})
	if err := store.Save(closed); err != nil {
		t.Fatalf("Save(closed child) error = %v", err)
	}

	handler := NewHandler(baseReadHandlerOptions(store))
	body := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	del := actionJSONByID(t, body, actionDelete)
	if del["enabled"] != true {
		t.Fatalf("delete enabled = %v, want cascade delete available during active work", del["enabled"])
	}
	preview, ok := del["impact_preview"].(map[string]any)
	if !ok {
		t.Fatalf("delete action missing impact_preview: %+v", del)
	}
	if preview["kind"] != string(ParentCascadeDelete) {
		t.Fatalf("preview kind = %v, want %s", preview["kind"], string(ParentCascadeDelete))
	}
	subject := preview["subject"].(map[string]any)
	if subject["id"] != parent.ID || subject["name"] != parent.Name {
		t.Fatalf("preview subject = %+v, want parent %s %s", subject, parent.ID, parent.Name)
	}
	impactCategoryKeys(t, preview, []string{"children", "sessions", "worktrees", "branches", "history", "knowledge"})

	children := impactCategoryItems(t, preview, "children")
	itemsContain(t, children, "Active refactor", "Completed refactor")
	sessions := impactCategoryItems(t, preview, "sessions")
	if len(sessions) != 0 {
		t.Fatalf("preview sessions = %v, want the explicitly empty category (no live sessions, no ledger entries)", sessions)
	}
	itemsContain(t, impactCategoryItems(t, preview, "worktrees"), "/worktrees/"+active.ID, worktreePathLiteral)
	branches := impactCategoryItems(t, preview, "branches")
	itemsContain(t, branches, "feature/active-refactor", "feature/completed-refactor")
	itemsContain(t, impactCategoryItems(t, preview, "history"), "Closed — Completed Completed refactor")

	// Without durable knowledge state the knowledge category renders
	// explicitly empty: no overlay was ever promoted for this relationship.
	if knowledge := impactCategoryItems(t, preview, "knowledge"); len(knowledge) != 0 {
		t.Fatalf("preview knowledge = %v, want empty (no overlay provenance, no workspace state)", knowledge)
	}
	previewRetained(t, preview, nil)

	// Once an overlay was genuinely promoted (provenance durable) and the
	// active child's workspace was seeded (workspace state durable), the
	// knowledge category enumerates both resources.
	if err := feature.SaveOverlayProvenance(feature.ParentOverlayPath(store.BaseDir, parent.ID, repoNameSelf), &feature.OverlayProvenance{
		CanonicalCommit: "cafebabe", ParentHEAD: "deadbeef", Generation: 1,
	}); err != nil {
		t.Fatalf("SaveOverlayProvenance error = %v", err)
	}
	workspaceDir := feature.ChildKBWorkspaceDir(store.BaseDir, active.ID, repoNameSelf)
	if err := feature.SaveWorkspaceState(workspaceDir, &feature.ChildKBWorkspaceState{
		Source: feature.WorkspaceSourceCanonical,
	}); err != nil {
		t.Fatalf("SaveWorkspaceState error = %v", err)
	}

	body = getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	preview = actionJSONByID(t, body, actionDelete)["impact_preview"].(map[string]any)
	knowledge := impactCategoryItems(t, preview, "knowledge")
	itemsContain(t, knowledge, "knowledge overlay", repoNameSelf)
	itemsContain(t, knowledge, "temporary knowledge workspace", active.ID, repoNameSelf)
}

// TestCascadeKnowledgeImpactEmptyForMediumOnlyRelationship pins that a
// Medium-pipeline child (no knowledge-base phase, never promoted an overlay)
// leaves the cascade knowledge category explicitly empty rather than naming
// overlays that cannot exist.
func TestCascadeKnowledgeImpactEmptyForMediumOnlyRelationship(t *testing.T) {
	t.Parallel()
	store, parent := seedReadFeature(t)
	child := seedReadChildFeature(t, store, parent.ID, feature.StatusImplementing, feature.SetupStatusDone, "")
	child.Pipeline = feature.PipelineMedium
	if err := store.Save(child); err != nil {
		t.Fatalf("Save(child) error = %v", err)
	}
	handler := NewHandler(baseReadHandlerOptions(store))

	body := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	preview := actionJSONByID(t, body, actionDelete)["impact_preview"].(map[string]any)
	if knowledge := impactCategoryItems(t, preview, "knowledge"); len(knowledge) != 0 {
		t.Fatalf("medium-only relationship knowledge = %v, want explicitly empty", knowledge)
	}
}

// TestSingleFeatureDeleteCarriesNoImpactPreview pins that an ordinary
// child-free feature delete never receives a preview.
func TestSingleFeatureDeleteCarriesNoImpactPreview(t *testing.T) {
	t.Parallel()
	store, parent := seedReadFeature(t)
	handler := NewHandler(baseReadHandlerOptions(store))

	body := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	for _, raw := range body["actions"].([]any) {
		action := raw.(map[string]any)
		if _, present := action["impact_preview"]; present {
			t.Fatalf("action %q carries an impact_preview on a single feature", action["id"])
		}
	}
}
