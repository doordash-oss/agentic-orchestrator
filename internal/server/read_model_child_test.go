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
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

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
	if activeChild["state"] != "setting_up" || activeChild["setup_status"] != string(feature.SetupStatusQueued) {
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
	// setup-retry/cleanup/delete) must serialize.
	actions := featureBody["actions"].([]any)
	ids := map[string]bool{}
	for _, raw := range actions {
		ids[raw.(map[string]any)["id"].(string)] = true
	}
	want := map[string]bool{actionStart: true, actionResume: true, actionRestart: true, actionRetry: true, actionDiscard: true, actionCleanup: true, actionDelete: true}
	if len(actions) != len(want) {
		t.Fatalf("child detail actions = %v, want %v", ids, want)
	}
	for id := range want {
		if !ids[id] {
			t.Fatalf("child detail actions = %v, missing %q", ids, id)
		}
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
		if activeChild["state"] != "setting_up" || activeChild["setup_status"] != string(feature.SetupStatusFailed) {
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
		if activeChild["state"] != "setup_complete" || activeChild["setup_status"] != string(feature.SetupStatusDone) {
			t.Fatalf("active_child = %+v, want setup_complete", activeChild)
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
		if summaryChild["state"] != "setup_complete" {
			t.Fatalf("list summary active_child = %+v, want setup_complete", summaryChild)
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

// TestEventDTOFromDomainCarriesRelatedFeatureID pins the SSE correlation
// contract: child lifecycle/setup events carry both the child id
// (feature_id) and the parent id (related_feature_id); top-level events stay
// zero-value-safe.
func TestEventDTOFromDomainCarriesRelatedFeatureID(t *testing.T) {
	t.Parallel()

	childEvent := eventDTOFromDomain(ports.Event{
		Type:             ports.SetupStarted,
		FeatureID:        "child-1",
		RelatedFeatureID: "parent-1",
	})
	if childEvent.Resource.FeatureID != "child-1" || childEvent.Resource.RelatedFeatureID != "parent-1" {
		t.Fatalf("child event resource = %+v, want feature_id + related_feature_id", childEvent.Resource)
	}

	topLevel := eventDTOFromDomain(ports.Event{Type: ports.FeatureStarted, FeatureID: "parent-1"})
	if topLevel.Resource.RelatedFeatureID != "" {
		t.Fatalf("top-level event resource = %+v, want no related_feature_id", topLevel.Resource)
	}
}
