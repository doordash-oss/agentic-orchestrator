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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// seedClosedChildren persists count closed refactor children under parentID,
// each carrying diffSummary, and returns their ids newest-first.
func seedClosedChildren(t *testing.T, store *feature.Store, parentID string, count int, diffSummary string) []string {
	t.Helper()
	ids := make([]string, 0, count)
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		closedAt := base.Add(-time.Duration(i) * time.Hour)
		child := &feature.Feature{
			ID:     fmt.Sprintf("%s-closed-%02d", parentID, i),
			Name:   fmt.Sprintf("Closed %02d", i),
			Slug:   fmt.Sprintf("closed-%02d", i),
			Status: feature.StatusReviewPassed, CurrentPhase: feature.PhaseImplement,
			Created:   closedAt.Add(-time.Hour),
			Repos:     []feature.FeatureRepo{{Name: repoNameSelf}},
			ActiveRun: 1, RunCount: 1, SchemaVersion: feature.SchemaVersionCurrent,
			Parent: &feature.ChildRelationship{
				ParentID: parentID, Kind: feature.ChildKindRefactor,
				CloseOutcome: feature.ChildCloseOutcomeCompleted, ClosedAt: &closedAt,
				DiffSummary: diffSummary,
			},
		}
		child.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Status: feature.SetupStatusDone}})
		if err := store.Save(child); err != nil {
			t.Fatalf("Save(%s) error = %v", child.ID, err)
		}
		ids = append(ids, child.ID)
	}
	return ids
}

// TestFeatureListOmitsClosedChildDiffBodies pins the list projection: closed
// children contribute no diff body at all, only the has_diff_summary flag, so
// a parent's accumulated diffs cannot inflate the list payload.
func TestFeatureListOmitsClosedChildDiffBodies(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	diffSummary := "Repository: " + repoNameSelf + "\n" + strings.Repeat("+line\n", 4096)
	seedClosedChildren(t, store, parent.ID, 2, diffSummary)

	handler := NewHandler(baseReadHandlerOptions(store))
	req := httptest.NewRequest(http.MethodGet, apiPathFeatures, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	raw := w.Body.Bytes()
	if strings.Contains(string(raw), `"diff_summary"`) {
		t.Fatalf("list payload carries a diff_summary field; want the body confined to the detail route")
	}
	if len(raw) > 64*1024 {
		t.Fatalf("list payload = %d bytes for two closed children; want the diff bodies excluded", len(raw))
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode list response error = %v", err)
	}
	summary := body["features"].([]any)[0].(map[string]any)
	for _, entry := range childHistoryByID(t, summary) {
		if got, ok := entry["diff_summary"]; ok {
			t.Fatalf("list child_history diff_summary = %#v, want omitted", got)
		}
		if entry["has_diff_summary"] != true {
			t.Fatalf("list child_history has_diff_summary = %#v, want true", entry["has_diff_summary"])
		}
	}
}

// TestFeatureListBoundsChildHistory proves the list history length cannot grow
// with the number of closed children: it carries the newest
// listChildHistoryLimit entries plus the true total and a truncation flag.
func TestFeatureListBoundsChildHistory(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	ids := seedClosedChildren(t, store, parent.ID, listChildHistoryLimit+3, "")

	handler := NewHandler(baseReadHandlerOptions(store))
	summary := getJSONMap(t, handler, apiPathFeatures)["features"].([]any)[0].(map[string]any)

	history := summary["child_history"].([]any)
	if len(history) != listChildHistoryLimit {
		t.Fatalf("list child_history length = %d, want %d", len(history), listChildHistoryLimit)
	}
	if got := summary["child_history_total"]; got != float64(len(ids)) {
		t.Fatalf("child_history_total = %#v, want %d", got, len(ids))
	}
	if summary["child_history_truncated"] != true {
		t.Fatalf("child_history_truncated = %#v, want true", summary["child_history_truncated"])
	}
	// seedClosedChildren closes each successive child an hour earlier, so the
	// bound must keep the newest entries.
	for i, raw := range history {
		if got := raw.(map[string]any)["id"]; got != ids[i] {
			t.Fatalf("child_history[%d] id = %#v, want newest-first %q", i, got, ids[i])
		}
	}

	detail := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	if got := len(detail["child_history"].([]any)); got != len(ids) {
		t.Fatalf("detail child_history length = %d, want the complete history of %d", got, len(ids))
	}
	if detail["child_history_truncated"] != nil {
		t.Fatalf("detail child_history_truncated = %#v, want unset", detail["child_history_truncated"])
	}
}

// TestRefactorEntryBlockedWhenCleanlinessIndeterminate pins the fail-closed
// gate: a cleanliness probe that times out (or otherwise yields no report)
// must never pass as a clean worktree, since these actions rewrite the
// parent's worktrees.
func TestRefactorEntryBlockedWhenCleanlinessIndeterminate(t *testing.T) {
	t.Parallel()

	for name, worktrees := range map[string]*readModelCleanlinessWorktrees{
		"timeout":     {err: git.ErrProbeTimeout},
		"nil report":  {},
		"probe error": {err: fmt.Errorf("git exploded")},
	} {
		worktrees := worktrees
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, parent := seedReadFeature(t)
			parent.Status = feature.StatusPublished
			if err := store.Save(parent); err != nil {
				t.Fatalf("Save(parent) error = %v", err)
			}
			opts := baseReadHandlerOptions(store)
			opts.Worktrees = worktrees
			handler := NewHandler(opts)

			body := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
			for _, id := range []string{actionRefactor, actionReviewFeedback, actionRebase} {
				action := actionJSONByID(t, body, id)
				if action["enabled"] != false {
					t.Fatalf("%s enabled = %v, want blocked on indeterminate cleanliness", id, action["enabled"])
				}
				reasons := action["disabled_reasons"].([]any)
				if len(reasons) == 0 || reasons[0].(map[string]any)["code"] != "worktree_state_unknown" {
					t.Fatalf("%s disabled reasons = %+v, want worktree_state_unknown", id, reasons)
				}
			}
		})
	}
}

// TestRefactorEntryBlockedWhenFreshnessUnknown covers the same fail-closed
// rule on the coarser fallback path taken without the worktree capability.
func TestRefactorEntryBlockedWhenFreshnessUnknown(t *testing.T) {
	t.Parallel()

	store, parent := seedReadFeature(t)
	parent.Status = feature.StatusPublished
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}
	opts := baseReadHandlerOptions(store)
	opts.Freshness = StaticFreshnessProvider{repoNameSelf: RepoFreshnessUnknown}
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/features/"+parent.ID)[entityFeature].(map[string]any)
	action := actionJSONByID(t, body, actionRefactor)
	if action["enabled"] != false {
		t.Fatalf("refactor enabled = %v, want blocked on unknown freshness", action["enabled"])
	}
	reasons := action["disabled_reasons"].([]any)
	if len(reasons) == 0 || reasons[0].(map[string]any)["code"] != "worktree_state_unknown" {
		t.Fatalf("refactor disabled reasons = %+v, want worktree_state_unknown", reasons)
	}
}
