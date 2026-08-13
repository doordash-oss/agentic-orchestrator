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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestReviewFeedbackFetchAggregatesGroupsSortsAndFiltersAddressedIDs(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	seedReviewFeedbackAddressedIDs(t, store, f.ID, "api", []int{11})
	installReviewFeedbackFetchFakeAPI(t, false)

	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})
	w := postTrustedJSON(handler, reviewFeedbackFetchPath(f.ID), map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("fetch status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var response struct {
		APIVersion string `json:"api_version"`
		Repos      []struct {
			Repo     string                          `json:"repo"`
			PRURL    string                          `json:"pr_url"`
			Comments []feature.ReviewFeedbackComment `json:"comments"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode fetch response: %v", err)
	}
	if response.APIVersion != APIVersion {
		t.Fatalf("api_version = %q, want %q", response.APIVersion, APIVersion)
	}
	if len(response.Repos) != 2 || response.Repos[0].Repo != "api" || response.Repos[1].Repo != "web" {
		t.Fatalf("repos = %+v, want api then web; docs without PR must be skipped", response.Repos)
	}

	apiComments := response.Repos[0].Comments
	if len(apiComments) != 2 {
		t.Fatalf("api comments = %+v, want two after addressed ID 11 is filtered", apiComments)
	}
	if apiComments[0].ID != 22 || apiComments[0].Type != "issue" || apiComments[1].ID != 33 || apiComments[1].Type != "review_body" {
		t.Fatalf("api comments = %+v, want chronological issue then review body", apiComments)
	}
	for _, comment := range apiComments {
		if comment.Repo != "api" {
			t.Fatalf("api comment repo = %q, want api", comment.Repo)
		}
	}
	webComments := response.Repos[1].Comments
	if len(webComments) != 1 || webComments[0].ID != 44 || webComments[0].Repo != "web" || webComments[0].Type != "review" {
		t.Fatalf("web comments = %+v, want tagged inline comment 44", webComments)
	}
}

func TestReviewFeedbackFetchFailsAtomicallyAndNamesRepo(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	installReviewFeedbackFetchFakeAPI(t, true)

	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})
	w := postTrustedJSON(handler, reviewFeedbackFetchPath(f.ID), map[string]any{})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("fetch status = %d, want 502: %s", w.Code, w.Body.String())
	}
	var response ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Error.Code != "review_feedback_fetch_failed" || !strings.Contains(response.Error.Message, "web") {
		t.Fatalf("error = %+v, want atomic fetch failure naming web", response.Error)
	}
	if got := response.Error.Target["repo"]; got != "web" {
		t.Fatalf("error target repo = %v, want web", got)
	}
}

func TestReviewFeedbackFetchRequestRejectsRepoAndModeFields(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})

	for _, body := range []map[string]any{{"repo": "api"}, {"mode": "all"}} {
		w := postTrustedJSON(handler, reviewFeedbackFetchPath(f.ID), body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("fetch body %v status = %d, want 400: %s", body, w.Code, w.Body.String())
		}
	}
}

func seedReviewFeedbackFetchFeature(t *testing.T) (*feature.Store, *feature.Feature) {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	f := &feature.Feature{
		ID:            "parent-fetch",
		Name:          "Parent Fetch",
		Slug:          "parent-fetch",
		Status:        feature.StatusPublished,
		Created:       time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "api", Path: t.TempDir()},
			{Name: "docs", Path: t.TempDir()},
			{Name: "web", Path: t.TempDir()},
		},
		RepoStates: map[string]*feature.RepoState{
			"api":  {PRURL: "https://github.com/example/api/pull/1"},
			"docs": {},
			"web":  {PRURL: "https://github.com/example/web/pull/2"},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(parent): %v", err)
	}
	return store, f
}

func seedReviewFeedbackAddressedIDs(t *testing.T, store *feature.Store, parentID, repoName string, ids []int) {
	t.Helper()
	dir := filepath.Join(store.BaseDir, parentID, "review-feedback", repoName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(addressed IDs): %v", err)
	}
	data, err := json.Marshal(ids)
	if err != nil {
		t.Fatalf("Marshal(addressed IDs): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "addressed-ids.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile(addressed IDs): %v", err)
	}
}

// installReviewFeedbackFetchFakeAPI fakes the three PR feedback endpoints
// for both repos. With failWebInline, web's inline-comment endpoint fails
// so the aggregate fetch must surface an atomic error naming the repo.
func installReviewFeedbackFetchFakeAPI(t *testing.T, failWebInline bool) {
	t.Helper()
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/example/api/pulls/1/comments", http.StatusOK,
		`[{"id":11,"path":"old.go","line":7,"body":"addressed inline","user":{"login":"alice"},"created_at":"2026-08-02T09:00:00Z"}]`)
	fake.HandleJSON("/repos/example/api/issues/1/comments", http.StatusOK,
		`[{"id":22,"body":"issue feedback","user":{"login":"bob"},"created_at":"2026-08-02T10:00:00Z"}]`)
	fake.HandleJSON("/repos/example/api/pulls/1/reviews", http.StatusOK,
		`[{"id":33,"body":"review body","user":{"login":"carol"},"submitted_at":"2026-08-02T11:00:00Z"}]`)
	if failWebInline {
		fake.HandleJSON("/repos/example/web/pulls/2/comments", http.StatusBadGateway, `{"message":"web unavailable"}`)
	} else {
		fake.HandleJSON("/repos/example/web/pulls/2/comments", http.StatusOK,
			`[{"id":44,"path":"web.go","line":9,"body":"web inline","user":{"login":"dana"},"created_at":"2026-08-02T08:00:00Z"}]`)
	}
	fake.HandleJSON("/repos/example/web/issues/2/comments", http.StatusOK, `[]`)
	fake.HandleJSON("/repos/example/web/pulls/2/reviews", http.StatusOK, `[]`)
}

// seedActiveReviewFeedbackChild installs an active review-feedback child
// carrying the given durable launch receipt for the parent.
func seedActiveReviewFeedbackChild(t *testing.T, store *feature.Store, parentID string, receipt *feature.ReviewFeedbackLaunchReceipt) {
	t.Helper()
	child := &feature.Feature{
		ID:            "child-review-active",
		Slug:          "child-review-active",
		Status:        feature.StatusImplementing,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Parent:        &feature.ChildRelationship{ParentID: parentID, Kind: feature.ChildKindReviewFeedback, LaunchReceipt: receipt},
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("Save(child): %v", err)
	}
}

// An ordinary refresh recognizes the durable launch receipt matching the
// pending draft's revision and finishes the cleanup an interrupted launch
// could not: the consumed draft is replaced by the current authoritative
// feedback, not relaunched.
func TestReviewFeedbackFetchConvergesDraftConsumedByLaunchReceipt(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	draft := seedReviewFeedbackSelectionDraft(t, store, f.ID)
	// Commit a selection change so the consumed draft is distinguishable
	// from a fresh reconciliation.
	if err := feature.ApplyReviewFeedbackSelection(draft, map[feature.StableReviewFeedbackRef]bool{"web:review:44": false}); err != nil {
		t.Fatal(err)
	}
	draft.Revision++
	if err := store.SaveReviewFeedbackDraft(f.ID, draft, draft.Revision-1); err != nil {
		t.Fatal(err)
	}
	seedActiveReviewFeedbackChild(t, store, f.ID, &feature.ReviewFeedbackLaunchReceipt{
		DraftRevision: draft.Revision, Changed: 1, Omitted: 0, Deferred: 0,
	})
	installReviewFeedbackFetchFakeAPI(t, false)

	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})
	w := postTrustedJSON(handler, reviewFeedbackFetchPath(f.ID), map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("fetch status = %d, want 200: %s", w.Code, w.Body.String())
	}
	response := selectionResponse(t, w.Body.Bytes())
	if response.Revision != 1 {
		t.Fatalf("revision = %d, want 1: the consumed draft must be replaced by a fresh reconciliation", response.Revision)
	}
	for _, group := range response.Repos {
		for _, comment := range group.Comments {
			if !comment.Selected {
				t.Fatalf("consumed selection leaked into refreshed draft for %q", comment.StableRef)
			}
		}
	}
	kept, err := store.LoadReviewFeedbackDraft(f.ID)
	if err != nil || kept == nil {
		t.Fatalf("reload draft: %v", err)
	}
	if kept.Revision != 1 {
		t.Fatalf("persisted revision = %d, want fresh revision 1", kept.Revision)
	}
}

func TestReviewFeedbackFetchRejectsConsumedAbsenceWhileIntentIsPending(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	f.PendingChild = &feature.ChildCreationIntent{
		Kind:    feature.ChildKindReviewFeedback,
		ChildID: "child-review-active",
		LaunchReceipt: &feature.ReviewFeedbackLaunchReceipt{
			DraftRevision: 1,
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(parent with pending intent): %v", err)
	}
	seedActiveReviewFeedbackChild(t, store, f.ID, f.PendingChild.LaunchReceipt)
	installReviewFeedbackFetchFakeAPI(t, false)

	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})
	w := postTrustedJSON(handler, reviewFeedbackFetchPath(f.ID), map[string]any{})
	if w.Code != http.StatusConflict {
		t.Fatalf("fetch status = %d, want 409: %s", w.Code, w.Body.String())
	}
	var response ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode fetch response: %v", err)
	}
	if response.Error.Code != errCodeReviewFeedbackRevisionConflict {
		t.Fatalf("error code = %q, want %q", response.Error.Code, errCodeReviewFeedbackRevisionConflict)
	}
	if kept, err := store.LoadReviewFeedbackDraft(f.ID); err != nil || kept != nil {
		t.Fatalf("draft after rejected fetch = %+v (err %v), want absent", kept, err)
	}
}

// A receipt that does not match the pending draft's revision does not claim
// it: ordinary reconciliation retains the committed selections.
func TestReviewFeedbackFetchIgnoresNonMatchingReceipt(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	draft := seedReviewFeedbackSelectionDraft(t, store, f.ID)
	if err := feature.ApplyReviewFeedbackSelection(draft, map[feature.StableReviewFeedbackRef]bool{"web:review:44": false}); err != nil {
		t.Fatal(err)
	}
	draft.Revision++
	if err := store.SaveReviewFeedbackDraft(f.ID, draft, draft.Revision-1); err != nil {
		t.Fatal(err)
	}
	seedActiveReviewFeedbackChild(t, store, f.ID, &feature.ReviewFeedbackLaunchReceipt{DraftRevision: draft.Revision + 9})
	installReviewFeedbackFetchFakeAPI(t, false)

	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})
	w := postTrustedJSON(handler, reviewFeedbackFetchPath(f.ID), map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("fetch status = %d, want 200: %s", w.Code, w.Body.String())
	}
	response := selectionResponse(t, w.Body.Bytes())
	if response.Revision != int(draft.Revision)+1 {
		t.Fatalf("revision = %d, want %d (ordinary reconciliation)", response.Revision, draft.Revision+1)
	}
	for _, group := range response.Repos {
		for _, comment := range group.Comments {
			want := comment.StableRef != "web:review:44"
			if comment.Selected != want {
				t.Fatalf("refetch changed committed selection for %q", comment.StableRef)
			}
		}
	}
}
