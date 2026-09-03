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
	"net/http"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// seedReviewFeedbackSelectionDraft saves a draft with one selected and one
// unselected reference for the fetch-test feature layout (api + web + docs).
func seedReviewFeedbackSelectionDraft(t *testing.T, store *feature.Store, parentID string) *feature.ReviewFeedbackDraft {
	t.Helper()
	parent, err := store.Load(parentID)
	if err != nil {
		t.Fatal(err)
	}
	fetched := map[string][]feature.ReviewFeedbackComment{
		"api": {
			{Repo: "api", ID: 22, Type: "issue", Author: "bob", Body: "issue body", CreatedAt: "2026-08-02T09:00:00Z"},
			{Repo: "api", ID: 33, Type: "review_body", Author: "carol", Body: "review body", CreatedAt: "2026-08-02T10:00:00Z"},
		},
		"web": {
			{Repo: "web", ID: 44, Type: "review", Author: "dana", Body: "inline", CreatedAt: "2026-08-02T08:00:00Z"},
		},
	}
	draft := feature.ReconcileReviewFeedbackDraft(parent, nil, fetched)
	if err := store.SaveReviewFeedbackDraft(parentID, draft, 0); err != nil {
		t.Fatal(err)
	}
	return draft
}

func selectionResponse(t *testing.T, recorderBody []byte) struct {
	Revision int `json:"revision"`
	Repos    []struct {
		Repo     string `json:"repo"`
		Comments []struct {
			StableRef string `json:"stable_ref"`
			Selected  bool   `json:"selected"`
			CreatedAt string `json:"created_at"`
		} `json:"comments"`
	} `json:"repos"`
} {
	t.Helper()
	var response struct {
		Revision int `json:"revision"`
		Repos    []struct {
			Repo     string `json:"repo"`
			Comments []struct {
				StableRef string `json:"stable_ref"`
				Selected  bool   `json:"selected"`
				CreatedAt string `json:"created_at"`
			} `json:"comments"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(recorderBody, &response); err != nil {
		t.Fatalf("decode selection response: %v", err)
	}
	return response
}

func TestReviewFeedbackSelectionCommitsAndPersists(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	draft := seedReviewFeedbackSelectionDraft(t, store, f.ID)

	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})
	w := postTrustedJSON(handler, reviewFeedbackSelectionPath(f.ID), map[string]any{
		"expected_revision": draft.Revision,
		"updates":           []map[string]any{{"stable_ref": "web:review:44", "selected": false}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("selection status = %d: %s", w.Code, w.Body.String())
	}
	response := selectionResponse(t, w.Body.Bytes())
	if response.Revision != int(draft.Revision)+1 {
		t.Fatalf("revision = %d, want %d", response.Revision, draft.Revision+1)
	}
	// Only the committed card changed; other selections across repositories
	// stay untouched.
	for _, group := range response.Repos {
		for _, comment := range group.Comments {
			want := comment.StableRef != "web:review:44"
			if comment.Selected != want {
				t.Fatalf("comment %q selected = %v, want %v", comment.StableRef, comment.Selected, want)
			}
			if comment.CreatedAt == "" {
				t.Fatalf("comment %q lost its creation timestamp", comment.StableRef)
			}
		}
	}
	// Durable restoration: a reload reflects exactly the committed state.
	reloaded, err := store.LoadReviewFeedbackDraft(f.ID)
	if err != nil || reloaded == nil {
		t.Fatalf("reload draft: %v", err)
	}
	if reloaded.Revision != draft.Revision+1 {
		t.Fatalf("persisted revision = %d, want %d", reloaded.Revision, draft.Revision+1)
	}
}

func TestReviewFeedbackSelectionRejectsMalformedUnknownAndCrossParentRefs(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	draft := seedReviewFeedbackSelectionDraft(t, store, f.ID)
	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})

	tests := []struct {
		name     string
		ref      string
		wantCode errcat.Code
	}{
		{name: "malformed", ref: "not-a-ref", wantCode: errcat.ReviewFeedbackMalformedReference},
		{name: "unsupported type", ref: "api:gist:22", wantCode: errcat.ReviewFeedbackMalformedReference},
		{name: "unknown repository", ref: "stranger:review:1", wantCode: errcat.ReviewFeedbackUnknownReference},
		{name: "not in draft", ref: "api:issue:999999", wantCode: errcat.ReviewFeedbackUnknownReference},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := postTrustedJSON(handler, reviewFeedbackSelectionPath(f.ID), map[string]any{
				"expected_revision": draft.Revision,
				"updates":           []map[string]any{{"stable_ref": tt.ref, "selected": true}},
			})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			var body ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != string(tt.wantCode) {
				t.Fatalf("code = %q, want %q", body.Error.Code, tt.wantCode)
			}
			if !strings.Contains(body.Error.Diagnostics, tt.ref) {
				t.Fatalf("diagnostics = %q, want raw detail naming %q", body.Error.Diagnostics, tt.ref)
			}
		})
	}
}

func TestReviewFeedbackSelectionRevisionConflictIsTyped(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	draft := seedReviewFeedbackSelectionDraft(t, store, f.ID)
	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})

	w := postTrustedJSON(handler, reviewFeedbackSelectionPath(f.ID), map[string]any{
		"expected_revision": draft.Revision + 5,
		"updates":           []map[string]any{{"stable_ref": "web:review:44", "selected": false}},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	var body ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != string(errcat.ReviewFeedbackRevisionConflict) {
		t.Fatalf("code = %q, want %q", body.Error.Code, errcat.ReviewFeedbackRevisionConflict)
	}
	// No partial application: the durable draft keeps its revision.
	kept, _ := store.LoadReviewFeedbackDraft(f.ID)
	if kept.Revision != draft.Revision {
		t.Fatalf("revision = %d after conflict, want %d", kept.Revision, draft.Revision)
	}
}

func TestReviewFeedbackSelectionBoundsAndMissingDraft(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})

	w := postTrustedJSON(handler, reviewFeedbackSelectionPath(f.ID), map[string]any{
		"expected_revision": 1,
		"updates":           []map[string]any{{"stable_ref": "web:review:44", "selected": false}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing draft status = %d, want 400", w.Code)
	}
	var body ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != string(errcat.ReviewFeedbackDraftNotFound) {
		t.Fatalf("code = %q, want %q", body.Error.Code, errcat.ReviewFeedbackDraftNotFound)
	}

	draft := seedReviewFeedbackSelectionDraft(t, store, f.ID)
	over := make([]map[string]any, 0, maxReviewFeedbackSelectionUpdates+1)
	for i := 0; i <= maxReviewFeedbackSelectionUpdates; i++ {
		over = append(over, map[string]any{"stable_ref": "web:review:44", "selected": i%2 == 0})
	}
	w = postTrustedJSON(handler, reviewFeedbackSelectionPath(f.ID), map[string]any{
		"expected_revision": draft.Revision,
		"updates":           over,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized batch status = %d, want 400", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != string(errcat.ReviewFeedbackSelectionTooLarge) {
		t.Fatalf("code = %q, want %q", body.Error.Code, errcat.ReviewFeedbackSelectionTooLarge)
	}
}

// Fresh fetches reconcile against committed selections: known references
// keep the user's choice and the durable revision moves forward.
func TestReviewFeedbackFetchRetainsCommittedSelections(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	installReviewFeedbackFetchFakeAPI(t, false)
	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})

	first := postTrustedJSON(handler, reviewFeedbackFetchPath(f.ID), map[string]any{})
	if first.Code != http.StatusOK {
		t.Fatalf("first fetch status = %d: %s", first.Code, first.Body.String())
	}
	var firstView struct {
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstView); err != nil {
		t.Fatal(err)
	}
	if firstView.Revision != 1 {
		t.Fatalf("first revision = %d, want 1", firstView.Revision)
	}

	sel := postTrustedJSON(handler, reviewFeedbackSelectionPath(f.ID), map[string]any{
		"expected_revision": firstView.Revision,
		"updates":           []map[string]any{{"stable_ref": "web:review:44", "selected": false}},
	})
	if sel.Code != http.StatusOK {
		t.Fatalf("selection status = %d: %s", sel.Code, sel.Body.String())
	}

	second := postTrustedJSON(handler, reviewFeedbackFetchPath(f.ID), map[string]any{})
	if second.Code != http.StatusOK {
		t.Fatalf("second fetch status = %d: %s", second.Code, second.Body.String())
	}
	var secondView struct {
		Revision   int    `json:"revision"`
		SnapshotID string `json:"snapshot_id"`
		Repos      []struct {
			Comments []struct {
				StableRef string `json:"stable_ref"`
				Selected  bool   `json:"selected"`
			} `json:"comments"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondView); err != nil {
		t.Fatal(err)
	}
	if secondView.Revision != 3 || secondView.SnapshotID == "" {
		t.Fatalf("second view revision/snapshot = %d/%q, want 3 and an identity", secondView.Revision, secondView.SnapshotID)
	}
	for _, group := range secondView.Repos {
		for _, comment := range group.Comments {
			want := comment.StableRef != "web:review:44"
			if comment.Selected != want {
				t.Fatalf("refetch changed committed selection for %q", comment.StableRef)
			}
		}
	}
	// Re-entry restoration: the persisted draft matches the last ack.
	kept, _ := store.LoadReviewFeedbackDraft(f.ID)
	if kept == nil || kept.Revision != int64(secondView.Revision) {
		t.Fatalf("restored draft = %+v, want revision %d", kept, secondView.Revision)
	}
}
