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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	testFeatureConfigPath = "/api/v1/features/feat-1/config"
	testRuntimeStateDir   = "/runtime/features"
)

func TestClientExportsOnlyJourneyUsedMethods(t *testing.T) {
	t.Parallel()

	want := []string{
		"LivePreview",
		"RefactorFeature",
		"SubscribeSessionOutput",
		"Transcript",
		"UpdateFeatureConfig",
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

func TestClientRetainedReadAndMutationSemantics(t *testing.T) {
	var sawReadAuth, sawMutationAuth, sawTrustedMutation bool
	automaticReviewMode := string(feature.AutomaticReviewEnabled)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/features/feat-1/live-preview":
			sawReadAuth = r.Header.Get("Authorization") == "Bearer test-token"
			writeJSON(w, http.StatusOK, LivePreviewResponse{
				APIVersion: APIVersion,
				Feature:    FeatureSummary{ID: fixtureFeatureID},
			})
		case "POST " + testFeatureConfigPath:
			sawMutationAuth = r.Header.Get("Authorization") == "Bearer test-token"
			sawTrustedMutation = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
			if got := r.Header.Get("Content-Type"); got != contentTypeJSON {
				t.Fatalf("Content-Type = %q, want %q", got, contentTypeJSON)
			}
			var req FeatureConfigMutationRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode feature config request: %v", err)
			}
			if req.Inquireness != "high" || req.Pipeline != feature.PipelineLarge {
				t.Fatalf("feature config request = %+v, want high/large", req)
			}
			if req.AutomaticReviewMode == nil || *req.AutomaticReviewMode != automaticReviewMode {
				t.Fatalf("automatic review mode = %v, want %q", req.AutomaticReviewMode, automaticReviewMode)
			}
			writeJSON(w, http.StatusOK, FeatureConfigUpdateResponse{
				APIVersion: APIVersion,
				FeatureID:  fixtureFeatureID,
				Result:     resultUpdated,
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
	updated, err := client.UpdateFeatureConfig(context.Background(), fixtureFeatureID, FeatureConfigMutationRequest{
		Inquireness:         "high",
		Pipeline:            feature.PipelineLarge,
		AutomaticReviewMode: &automaticReviewMode,
	})
	if err != nil {
		t.Fatalf("UpdateFeatureConfig() error = %v", err)
	}
	if updated.FeatureID != fixtureFeatureID || updated.Result != resultUpdated || !sawMutationAuth || !sawTrustedMutation {
		t.Fatalf("UpdateFeatureConfig() = %+v auth=%v trusted=%v, want updated authenticated trusted mutation", updated, sawMutationAuth, sawTrustedMutation)
	}
}

func TestClientRetainedMutationReturnsStructuredAPIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != testFeatureConfigPath {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		writeJSON(w, http.StatusConflict, ErrorResponse{
			APIVersion: APIVersion,
			Error: ErrorDTO{
				Code:    errCodeConflict,
				Message: "feature is busy",
				Status:  http.StatusConflict,
				Target:  map[string]any{"feature_id": fixtureFeatureID},
			},
		})
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.UpdateFeatureConfig(context.Background(), fixtureFeatureID, FeatureConfigMutationRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("UpdateFeatureConfig() error = %v, want APIError", err)
	}
	if apiErr.Code != errCodeConflict || apiErr.Status != http.StatusConflict || apiErr.Method != http.MethodPost || apiErr.Path != testFeatureConfigPath {
		t.Fatalf("APIError = %+v, want method/path scoped conflict", apiErr)
	}
	if apiErr.Target["feature_id"] != fixtureFeatureID {
		t.Fatalf("APIError target = %+v, want feature_id %q", apiErr.Target, fixtureFeatureID)
	}
}

func TestClientRefactorFeatureReturnsTypedResult(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/features/feat-1/actions/refactor" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		var req RefactorFeatureRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode refactor request: %v", err)
		}
		if req.Name != "Rework auth" || req.Pipeline != feature.PipelineMedium {
			t.Fatalf("RefactorFeature() request = %+v, want named medium child", req)
		}
		writeJSON(w, http.StatusCreated, RefactorFeatureResponse{
			APIVersion: APIVersion,
			FeatureID:  "child-1",
			ParentID:   fixtureFeatureID,
			Result:     resultCreated,
		})
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	result, err := client.RefactorFeature(context.Background(), fixtureFeatureID, RefactorFeatureRequest{
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

func TestClientTranscriptContinuationUsesHandlerOffset(t *testing.T) {
	t.Parallel()

	messages := []llm.SDKMessage{
		{Type: roleAssistant, Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{Role: roleAssistant, Content: []llm.ContentBlock{{Type: blockTypeText, Text: "first transcript row"}}}}},
		{Type: roleAssistant, Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{Role: roleAssistant, Content: []llm.ContentBlock{{Type: blockTypeText, Text: "second transcript row"}}}}},
	}
	handler := NewHandler(HandlerOptions{
		Sessions: fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
			id: fixtureSessionID, featureID: fixtureFeatureID, messages: messages,
		}}},
		DisableHostValidation: true,
	})
	srv := httptest.NewServer(handler)
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

// StaticFreshnessProvider is a test-only RepoFreshnessProvider backed by a
// fixed repo-name-to-freshness map, used by server read-model tests.
type StaticFreshnessProvider map[string]RepoFreshness

func (p StaticFreshnessProvider) Freshness(_ *feature.Feature, repo feature.FeatureRepo) RepoFreshness {
	if status, ok := p[repo.Name]; ok {
		return status
	}
	return RepoFreshnessUnknown
}
