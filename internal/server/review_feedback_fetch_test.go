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

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestReviewFeedbackFetchAggregatesGroupsSortsAndFiltersAddressedIDs(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	seedReviewFeedbackAddressedIDs(t, store, f.ID, "api", []int{11})
	fake := installReviewFeedbackFetchFakeAPI(t, false)

	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})
	w := postTrustedJSON(handler, reviewFeedbackFetchPath(f.ID), map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("fetch status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var response struct {
		APIVersion string `json:"api_version"`
		Repos      []struct {
			Repo         string `json:"repo"`
			PullRequests []struct {
				Position int    `json:"position"`
				Title    string `json:"title"`
				URL      string `json:"url"`
				Comments []struct {
					feature.ReviewFeedbackComment
					StableRef string `json:"stable_ref"`
					Selected  bool   `json:"selected"`
				} `json:"comments"`
			} `json:"pull_requests"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode fetch response: %v", err)
	}
	if response.APIVersion != APIVersion {
		t.Fatalf("api_version = %q, want %q", response.APIVersion, APIVersion)
	}
	if len(response.Repos) != 2 || response.Repos[0].Repo != "api" || response.Repos[1].Repo != "web" {
		t.Fatalf("repos = %+v, want api then web; docs without an open layer PR must be skipped", response.Repos)
	}

	api := response.Repos[0]
	if len(api.PullRequests) != 2 {
		t.Fatalf("api pull-request groups = %+v, want the open layers 1 and 3 only", api.PullRequests)
	}
	layer1, layer3 := api.PullRequests[0], api.PullRequests[1]
	if layer1.Position != 1 || layer1.Title != "Foundation" || layer1.URL != "https://github.com/example/api/pull/1" {
		t.Fatalf("api layer-1 group = %+v, want position 1, title Foundation, pull/1", layer1)
	}
	if layer3.Position != 3 || layer3.Title != "Extension" || layer3.URL != "https://github.com/example/api/pull/3" {
		t.Fatalf("api layer-3 group = %+v, want position 3, title Extension, pull/3", layer3)
	}
	if len(layer1.Comments) != 1 || layer1.Comments[0].ID != 22 || layer1.Comments[0].Type != "issue" {
		t.Fatalf("api layer-1 comments = %+v, want the issue comment after addressed ID 11 is filtered", layer1.Comments)
	}
	if len(layer3.Comments) != 1 || layer3.Comments[0].ID != 33 || layer3.Comments[0].Type != "review_body" {
		t.Fatalf("api layer-3 comments = %+v, want the review body", layer3.Comments)
	}
	for _, group := range append(api.PullRequests, response.Repos[1].PullRequests...) {
		for _, comment := range group.Comments {
			if comment.Repo == "" || comment.PRURL != group.URL || comment.PRNumber == 0 ||
				comment.LayerPosition != group.Position || comment.LayerTitle != group.Title {
				t.Fatalf("comment %+v lacks its PR identity (group %+v)", comment, group)
			}
		}
	}

	web := response.Repos[1]
	if len(web.PullRequests) != 1 {
		t.Fatalf("web pull-request groups = %+v, want one", web.PullRequests)
	}
	webGroup := web.PullRequests[0]
	if webGroup.Position != 1 || webGroup.Title != "Foundation" || webGroup.URL != "https://github.com/example/web/pull/2" {
		t.Fatalf("web group = %+v, want position 1, title Foundation, pull/2", webGroup)
	}
	if len(webGroup.Comments) != 1 || webGroup.Comments[0].ID != 44 || webGroup.Comments[0].Repo != "web" || webGroup.Comments[0].Type != "review" {
		t.Fatalf("web comments = %+v, want tagged inline comment 44", webGroup.Comments)
	}

	// The merged layer 2 pull request is never asked for comments; only the
	// open layers 1 and 3 are requested, plus web's single open layer.
	for _, merged := range []string{"/repos/example/api/pulls/2/comments", "/repos/example/api/issues/2/comments", "/repos/example/api/pulls/2/reviews"} {
		if got := fake.RequestCount(merged); got != 0 {
			t.Fatalf("merged layer PR endpoint %s requested %d times, want 0", merged, got)
		}
	}
	for _, open := range []string{
		"/repos/example/api/pulls/1/comments", "/repos/example/api/issues/1/comments", "/repos/example/api/pulls/1/reviews",
		"/repos/example/api/pulls/3/comments", "/repos/example/api/issues/3/comments", "/repos/example/api/pulls/3/reviews",
		"/repos/example/web/pulls/2/comments",
	} {
		if got := fake.RequestCount(open); got != 1 {
			t.Fatalf("open layer PR endpoint %s requested %d times, want 1", open, got)
		}
	}
}

func TestReviewFeedbackFetchResponseCarriesPullRequestGroupsNotRepoURL(t *testing.T) {
	store, f := seedReviewFeedbackFetchFeature(t)
	installReviewFeedbackFetchFakeAPI(t, false)

	handler := NewHandler(HandlerOptions{Features: store, FeatureStore: store, Mutations: &refactorMutationTarget{}, DisableHostValidation: true})
	w := postTrustedJSON(handler, reviewFeedbackFetchPath(f.ID), map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("fetch status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var response struct {
		Repos []map[string]any `json:"repos"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode fetch response: %v", err)
	}
	if len(response.Repos) != 2 {
		t.Fatalf("repos = %+v, want api and web only", response.Repos)
	}
	for _, repo := range response.Repos {
		if _, ok := repo["pr_url"]; ok {
			t.Fatalf("repo entry %+v still carries the removed group-level pr_url", repo)
		}
		if _, ok := repo["pull_requests"]; !ok {
			t.Fatalf("repo entry %+v lacks pull_requests", repo)
		}
	}
	api := response.Repos[0]["pull_requests"].([]any)
	if len(api) != 2 {
		t.Fatalf("api pull_requests = %+v, want two groups", api)
	}
	first := api[0].(map[string]any)
	second := api[1].(map[string]any)
	if first["position"].(float64) != 1 || second["position"].(float64) != 3 {
		t.Fatalf("api pull-request order = %+v, want positions 1 then 3", api)
	}
	for _, group := range []map[string]any{first, second} {
		for _, key := range []string{"position", "title", "url", "comments"} {
			if _, ok := group[key]; !ok {
				t.Fatalf("pull-request group %+v lacks %q", group, key)
			}
		}
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
	if response.Error.Code != string(errcat.ReviewFeedbackFetchFailed) || !strings.Contains(response.Error.Diagnostics, "web") {
		t.Fatalf("error = %+v, want atomic fetch failure naming web", response.Error)
	}
	if response.Error.Context == nil || len(response.Error.Context.Repositories) != 1 ||
		response.Error.Context.Repositories[0].Name != "web" {
		t.Fatalf("error context = %+v, want web repository context", response.Error.Context)
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
			"api":  {Touched: true},
			"docs": {},
			"web":  {Touched: true},
		},
		// api has open pull requests on layers 1 and 3 and a merged pull
		// request on layer 2; web has one open layer pull request; docs has
		// none and must be skipped entirely.
		Stack: []feature.StackLayer{
			{
				Position: 1,
				Title:    "Foundation",
				Branch:   "agentico/parent-fetch/1-foundation",
				Repos: map[string]feature.StackRepoEntry{
					"api": {PRURL: "https://github.com/example/api/pull/1", PRState: feature.StackPRStateOpen},
					"web": {PRURL: "https://github.com/example/web/pull/2", PRState: feature.StackPRStateOpen},
				},
			},
			{
				Position: 2,
				Title:    "Merged layer",
				Branch:   "agentico/parent-fetch/2-merged-layer",
				Repos: map[string]feature.StackRepoEntry{
					"api": {PRURL: "https://github.com/example/api/pull/2", PRState: feature.StackPRStateMerged},
				},
			},
			{
				Position: 3,
				Title:    "Extension",
				Branch:   "agentico/parent-fetch/3-extension",
				Repos: map[string]feature.StackRepoEntry{
					"api": {PRURL: "https://github.com/example/api/pull/3", PRState: feature.StackPRStateOpen},
				},
			},
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
// for every open layer pull request of the seeded feature: api layers 1 and
// 3 plus web's layer 1. The merged api layer 2 pull request is never
// registered — a fetch that asked for it would fail on the fake's 404. With
// failWebInline, web's inline-comment endpoint fails so the aggregate fetch
// must surface an atomic error naming the repo.
func installReviewFeedbackFetchFakeAPI(t *testing.T, failWebInline bool) *testutil.FakeGitHubAPI {
	t.Helper()
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/example/api/pulls/1/comments", http.StatusOK,
		`[{"id":11,"path":"old.go","line":7,"body":"addressed inline","user":{"login":"alice"},"created_at":"2026-08-02T09:00:00Z"}]`)
	fake.HandleJSON("/repos/example/api/issues/1/comments", http.StatusOK,
		`[{"id":22,"body":"issue feedback","user":{"login":"bob"},"created_at":"2026-08-02T10:00:00Z"}]`)
	fake.HandleJSON("/repos/example/api/pulls/1/reviews", http.StatusOK, `[]`)
	fake.HandleJSON("/repos/example/api/pulls/3/comments", http.StatusOK, `[]`)
	fake.HandleJSON("/repos/example/api/issues/3/comments", http.StatusOK, `[]`)
	fake.HandleJSON("/repos/example/api/pulls/3/reviews", http.StatusOK,
		`[{"id":33,"body":"review body","user":{"login":"carol"},"submitted_at":"2026-08-02T11:00:00Z"}]`)
	if failWebInline {
		fake.HandleJSON("/repos/example/web/pulls/2/comments", http.StatusBadGateway, `{"message":"web unavailable"}`)
	} else {
		fake.HandleJSON("/repos/example/web/pulls/2/comments", http.StatusOK,
			`[{"id":44,"path":"web.go","line":9,"body":"web inline","user":{"login":"dana"},"created_at":"2026-08-02T08:00:00Z"}]`)
	}
	fake.HandleJSON("/repos/example/web/issues/2/comments", http.StatusOK, `[]`)
	fake.HandleJSON("/repos/example/web/pulls/2/reviews", http.StatusOK, `[]`)
	return fake
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
		for _, pr := range group.PullRequests {
			for _, comment := range pr.Comments {
				if !comment.Selected {
					t.Fatalf("consumed selection leaked into refreshed draft for %q", comment.StableRef)
				}
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
	if response.Error.Code != string(errcat.ReviewFeedbackRevisionConflict) {
		t.Fatalf("error code = %q, want %q", response.Error.Code, errcat.ReviewFeedbackRevisionConflict)
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
		for _, pr := range group.PullRequests {
			for _, comment := range pr.Comments {
				want := comment.StableRef != "web:review:44"
				if comment.Selected != want {
					t.Fatalf("refetch changed committed selection for %q", comment.StableRef)
				}
			}
		}
	}
}
