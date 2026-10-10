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

package serverclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

const (
	testFeatureConfigPath = "/api/v1/features/feat-1/config"
	fixtureFeatureID      = "feat-1"
	fixtureSessionID      = "sess-1"
	testAuthToken         = "test-token"
	contentTypeJSON       = "application/json"
)

func TestClientExportsOnlyJourneyUsedMethods(t *testing.T) {
	t.Parallel()

	want := []string{
		"FetchReviewFeedback",
		"LivePreview",
		"RebaseFeature",
		"RefactorFeature",
		"ReviewFeedbackFeature",
		"SubscribeSessionOutput",
		"Transcript",
		"UpdateFeatureConfig",
		"UpdateReviewFeedbackSelection",
	}
	typeOfClient := reflect.TypeOf((*Client)(nil))
	got := make([]string, 0, typeOfClient.NumMethod())
	for i := 0; i < typeOfClient.NumMethod(); i++ {
		got = append(got, typeOfClient.Method(i).Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exported Client methods = %v, want %v", got, want)
	}
}

func TestClientFetchReviewFeedbackReturnsTypedGroups(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/features/feat-1/actions/review-feedback/fetch" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode fetch request: %v", err)
		}
		if len(req) != 0 {
			t.Fatalf("FetchReviewFeedback() request = %v, want empty object", req)
		}
		writeJSON(w, http.StatusOK, server.ReviewFeedbackFetchResponse{
			APIVersion: server.APIVersion,
			Revision:   1,
			SnapshotID: "snapshot-1",
			Repos: []server.ReviewFeedbackRepoComments{{
				Repo:  "api",
				PrURL: "https://github.com/example/api/pull/1",
				Comments: []server.ReviewFeedbackDraftComment{{
					StableRef: "api:issue:11", Selected: true,
					Repo: "api", ID: 11, Type: server.ReviewFeedbackDraftCommentType("issue"), Body: "please adjust",
				}},
			}},
		})
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	result, err := client.FetchReviewFeedback(context.Background(), fixtureFeatureID)
	if err != nil {
		t.Fatalf("FetchReviewFeedback() error = %v", err)
	}
	if len(result.Repos) != 1 || result.Repos[0].Repo != "api" || len(result.Repos[0].Comments) != 1 || result.Repos[0].Comments[0].ID != 11 {
		t.Fatalf("FetchReviewFeedback() = %+v, want typed api group with comment 11", result)
	}
}

func TestClientRetainedReadAndMutationSemantics(t *testing.T) {
	var sawReadAuth, sawMutationAuth, sawTrustedMutation bool
	automaticReviewMode := string(feature.AutomaticReviewEnabled)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/features/feat-1/live-preview":
			sawReadAuth = r.Header.Get("Authorization") == "Bearer test-token"
			writeJSON(w, http.StatusOK, server.LivePreviewResponse{
				APIVersion: server.APIVersion,
				Feature:    server.FeatureSummary{ID: fixtureFeatureID},
			})
		case "POST " + testFeatureConfigPath:
			sawMutationAuth = r.Header.Get("Authorization") == "Bearer test-token"
			sawTrustedMutation = r.Header.Get("X-Agentico-Client") == "local"
			if got := r.Header.Get("Content-Type"); got != contentTypeJSON {
				t.Fatalf("Content-Type = %q, want %q", got, contentTypeJSON)
			}
			var req server.FeatureConfigMutationRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode feature config request: %v", err)
			}
			if req.Inquireness != "high" || req.Pipeline != feature.PipelineLarge {
				t.Fatalf("feature config request = %+v, want high/large", req)
			}
			if req.AutomaticReviewMode == nil || *req.AutomaticReviewMode != automaticReviewMode {
				t.Fatalf("automatic review mode = %v, want %q", req.AutomaticReviewMode, automaticReviewMode)
			}
			writeJSON(w, http.StatusOK, server.FeatureActionResponse{
				APIVersion: server.APIVersion,
				FeatureID:  fixtureFeatureID,
				Result:     "updated",
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := NewClient(ClientOptions{BaseURL: srv.URL, Token: testAuthToken})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	preview, err := client.LivePreview(context.Background(), fixtureFeatureID)
	if err != nil {
		t.Fatalf("LivePreview() error = %v", err)
	}
	if preview.Feature.ID != fixtureFeatureID || !sawReadAuth {
		t.Fatalf("LivePreview() = %+v read_auth=%v, want feat-1 with bearer auth", preview, sawReadAuth)
	}
	updated, err := client.UpdateFeatureConfig(context.Background(), fixtureFeatureID, server.FeatureConfigMutationRequest{
		Inquireness:         "high",
		Pipeline:            feature.PipelineLarge,
		AutomaticReviewMode: &automaticReviewMode,
	})
	if err != nil {
		t.Fatalf("UpdateFeatureConfig() error = %v", err)
	}
	if updated.FeatureID != fixtureFeatureID || updated.Result != "updated" || !sawMutationAuth || !sawTrustedMutation {
		t.Fatalf("UpdateFeatureConfig() = %+v auth=%v trusted=%v, want updated authenticated trusted mutation", updated, sawMutationAuth, sawTrustedMutation)
	}
}

func TestClientRetainedMutationReturnsStructuredAPIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != testFeatureConfigPath {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		writeJSON(w, http.StatusConflict, server.ErrorResponse{
			APIVersion: server.APIVersion,
			Error: server.Error{
				Class:       server.ErrorClass(errcat.ClassBlocking),
				Code:        string(errcat.Conflict),
				Title:       "Conflict",
				Summary:     "The request conflicts with the current state of the feature.",
				Diagnostics: "feature is busy",
				Remediation: &server.ErrorRemediation{Hint: "Refresh the feature and retry."},
				Context: &server.ErrorContext{Repositories: []server.ErrorRepositoryContext{{
					Name:   "repo-a",
					Branch: "feature/feat-1",
				}}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.UpdateFeatureConfig(context.Background(), fixtureFeatureID, server.FeatureConfigMutationRequest{})
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("UpdateFeatureConfig() error = %v, want APIError", err)
	}
	if apiErr.Code != string(errcat.Conflict) || apiErr.Class != string(errcat.ClassBlocking) ||
		apiErr.Status != http.StatusConflict || apiErr.Method != http.MethodPost || apiErr.Path != testFeatureConfigPath {
		t.Fatalf("APIError = %+v, want method/path scoped blocking conflict", apiErr)
	}
	if apiErr.Title != "Conflict" || apiErr.Summary != "The request conflicts with the current state of the feature." {
		t.Fatalf("APIError title/summary = %q/%q, want catalog-authored conflict text", apiErr.Title, apiErr.Summary)
	}
	if apiErr.Diagnostics != "feature is busy" {
		t.Fatalf("APIError diagnostics = %q, want feature is busy", apiErr.Diagnostics)
	}
	if apiErr.Remediation == nil || apiErr.Remediation.Hint != "Refresh the feature and retry." {
		t.Fatalf("APIError remediation = %+v, want refresh hint", apiErr.Remediation)
	}
	if apiErr.Context == nil || len(apiErr.Context.Repositories) != 1 ||
		apiErr.Context.Repositories[0].Name != "repo-a" || apiErr.Context.Repositories[0].Branch != "feature/feat-1" {
		t.Fatalf("APIError context = %+v, want repo-a repository context", apiErr.Context)
	}
	if got, want := apiErr.Error(), "api POST "+testFeatureConfigPath+": conflict (409): Conflict"; got != want {
		t.Fatalf("APIError.Error() = %q, want %q", got, want)
	}
}

func TestClientRefactorFeatureReturnsTypedResult(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/features/feat-1/actions/refactor" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		var req server.RefactorFeatureRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode refactor request: %v", err)
		}
		if req.Name != "Rework auth" || req.Pipeline != feature.PipelineMedium {
			t.Fatalf("RefactorFeature() request = %+v, want named medium child", req)
		}
		writeJSON(w, http.StatusCreated, server.ChildLaunchResponse{
			APIVersion: server.APIVersion,
			FeatureID:  "child-1",
			ParentID:   fixtureFeatureID,
			Result:     "created",
		})
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	result, err := client.RefactorFeature(context.Background(), fixtureFeatureID, server.RefactorFeatureRequest{
		Name:     "Rework auth",
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("RefactorFeature() error = %v", err)
	}
	if result.FeatureID != "child-1" || result.ParentID != fixtureFeatureID {
		t.Fatalf("RefactorFeature() = %+v, want child-1 of feat-1", result)
	}
}

func TestClientReviewFeedbackFeatureReturnsTypedResult(t *testing.T) {
	t.Parallel()

	gate := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/features/feat-1/actions/review-feedback" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		var req server.ReviewFeedbackFeatureRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode review-feedback request: %v", err)
		}
		if req.Gate == nil || *req.Gate || req.ExpectedRevision != 7 {
			t.Fatalf("ReviewFeedbackFeature() request = %+v, want expected revision 7 and explicit false gate", req)
		}
		writeJSON(w, http.StatusCreated, server.ReviewFeedbackFeatureResponse{
			APIVersion: server.APIVersion,
			FeatureID:  "child-1",
			ParentID:   fixtureFeatureID,
			Result:     "created",
		})
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	result, err := client.ReviewFeedbackFeature(context.Background(), fixtureFeatureID, server.ReviewFeedbackFeatureRequest{
		ExpectedRevision: 7,
		Gate:             &gate,
	})
	if err != nil {
		t.Fatalf("ReviewFeedbackFeature() error = %v", err)
	}
	if result.FeatureID != "child-1" || result.ParentID != fixtureFeatureID {
		t.Fatalf("ReviewFeedbackFeature() = %+v, want child-1 of feat-1", result)
	}
}

func TestClientTranscriptContinuationUsesHandlerOffset(t *testing.T) {
	t.Parallel()

	texts := []string{"first transcript row", "second transcript row"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/sessions/"+fixtureSessionID+"/transcript" || r.URL.Query().Get("limit") != "1" {
			t.Fatalf("unexpected transcript request %s %s", r.Method, r.URL.String())
		}
		start := 0
		if offset := r.URL.Query().Get("offset"); offset != "" {
			if offset != "1" {
				t.Fatalf("offset = %q, want 1", offset)
			}
			start = 1
		}
		writeJSON(w, http.StatusOK, server.TranscriptResponse{
			APIVersion: server.APIVersion,
			Cursor:     server.Cursor{Start: start, End: start + 1, Total: len(texts)},
			Messages:   []server.TranscriptMessage{{Index: start, Role: "assistant", Type: "text", Text: texts[start]}},
		})
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	first, err := client.Transcript(context.Background(), fixtureSessionID, CursorQuery{Limit: 1})
	if err != nil {
		t.Fatalf("Transcript(first) error = %v", err)
	}
	if first.Cursor.Start != 0 || first.Cursor.End != 1 || len(first.Messages) != 1 || first.Messages[0].Text != "first transcript row" {
		t.Fatalf("Transcript(first) = cursor %+v messages %+v; want first row", first.Cursor, first.Messages)
	}
	second, err := client.Transcript(context.Background(), fixtureSessionID, CursorQuery{Cursor: first.Cursor.End, Limit: 1})
	if err != nil {
		t.Fatalf("Transcript(second) error = %v", err)
	}
	if second.Cursor.Start != 1 || second.Cursor.End != 2 || len(second.Messages) != 1 || second.Messages[0].Text != "second transcript row" {
		t.Fatalf("Transcript(second) = cursor %+v messages %+v; want continuation row", second.Cursor, second.Messages)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
