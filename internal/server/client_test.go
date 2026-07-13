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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// testFeatureConfigPath is the fixture path for feat-1's config route, used
// across several client tests in this file.
const testFeatureConfigPath = "/api/v1/features/feat-1/config"

// Route and fixture literals reused across the fake-server test cases in
// this file.
const (
	routeGetHealth             = "GET /api/v1/health"
	routeGetFeatures           = "GET /api/v1/features"
	routeGetPrompts            = "GET /api/v1/prompts"
	routeGetSessions           = "GET /api/v1/sessions"
	routeGetSession1           = "GET /api/v1/sessions/sess-1"
	routeGetSession1Transcript = "GET /api/v1/sessions/sess-1/transcript"
	routeGetLivePreview        = "GET /api/v1/features/feat-1/live-preview"

	testFeatureName     = "Client cutover"
	testRuntimeStateDir = "/runtime/features"

	inquirenessAlways = "always"

	sessionStatusWaitingHelp = "WaitingHelp"
	sessionStatusDone        = "Done"
	featureStatusCodeReady   = "CodeReady"

	// fixtureRecoveryItemKey is the fake RecoveryItem.Key / recovery action
	// map key used across recovery fixtures in this file.
	fixtureRecoveryItemKey = "feat-1:api"
	// fixtureRecoverySnapshotID is the fake RecoverySnapshot.SnapshotID used
	// across recovery fixtures in this file.
	fixtureRecoverySnapshotID = "recovery-snapshot-1"
)

func TestClientFetchesTypedSnapshotsAndActionResults(t *testing.T) {
	var sawTrustedHeader bool
	var sawResumeTrustedHeader bool
	var sawRecoveryTrustedHeader bool
	var sawChatTrustedHeader bool
	var sawPublishDescriptionTrustedHeader bool
	var sawReviewCreateTrustedHeader bool
	var sawReviewSaveTrustedHeader bool
	var sawReviewDecisionTrustedHeader bool
	var sawReviewCancelTrustedHeader bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case routeGetHealth:
			writeJSON(w, http.StatusOK, HealthResponse{APIVersion: APIVersion, Status: "ok"})
		case routeGetFeatures:
			writeJSON(w, http.StatusOK, FeatureListResponse{APIVersion: APIVersion, Features: []FeatureSummary{{ID: fixtureFeatureID, Name: testFeatureName}}})
		case "GET /api/v1/features/feat-1":
			writeJSON(w, http.StatusOK, FeatureDetailResponse{APIVersion: APIVersion, Feature: testFeatureDetail(FeatureSummary{ID: fixtureFeatureID, Name: testFeatureName})})
		case "GET /api/v1/config/runtime":
			writeJSON(w, http.StatusOK, RuntimeConfigResponse{APIVersion: APIVersion, Runtime: RuntimeIdentity{StateDir: testRuntimeStateDir}})
		case "GET /api/v1/features/feat-1/config":
			writeJSON(w, http.StatusOK, FeatureConfigResponse{APIVersion: APIVersion, FeatureID: fixtureFeatureID})
		case "GET /api/v1/catalog/models":
			writeJSON(w, http.StatusOK, ModelCatalogResponse{APIVersion: APIVersion, ProviderOrder: []string{providerCodex}})
		case routeGetPrompts:
			writeJSON(w, http.StatusOK, PromptSnapshotResponse{APIVersion: APIVersion})
		case "GET /api/v1/permissions":
			writeJSON(w, http.StatusOK, PermissionSnapshotResponse{APIVersion: APIVersion})
		case routeGetSessions:
			writeJSON(w, http.StatusOK, SessionListResponse{APIVersion: APIVersion, Sessions: []SessionSummaryDTO{{ID: fixtureSessionID, FeatureID: fixtureFeatureID}}})
		case routeGetSession1:
			writeJSON(w, http.StatusOK, SessionDetailResponse{APIVersion: APIVersion, Session: testSessionDetail(SessionSummaryDTO{ID: fixtureSessionID, FeatureID: fixtureFeatureID}, CursorDTO{})})
		case routeGetSession1Transcript:
			if got := r.URL.Query().Get("offset"); got != "2" {
				t.Errorf("transcript offset = %q, want 2", got)
			}
			writeJSON(w, http.StatusOK, TranscriptResponse{APIVersion: APIVersion, Cursor: CursorDTO{Start: 2, End: 3}})
		case "GET /api/v1/features/feat-1/runs/1/artifacts":
			writeJSON(w, http.StatusOK, ArtifactListResponse{APIVersion: APIVersion, Artifacts: []ArtifactDTO{{ID: targetPhasePlan, RunNumber: 1}}})
		case "GET /api/v1/features/feat-1/runs/1/artifacts/plan":
			if got := r.URL.Query().Get("limit"); got != "8" {
				t.Errorf("artifact limit = %q, want 8", got)
			}
			writeJSON(w, http.StatusOK, TextContentResponse{APIVersion: APIVersion, ID: targetPhasePlan, Text: "phase"})
		case "GET /api/v1/features/feat-1/runs/1/logs/session":
			writeJSON(w, http.StatusOK, TextContentResponse{APIVersion: APIVersion, ID: logIDSession, Text: "log"})
		case "POST /api/v1/features/feat-1/reviews":
			sawReviewCreateTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
			writeJSON(w, http.StatusOK, ReviewSessionResponse{APIVersion: APIVersion, FeatureID: fixtureFeatureID, ReviewID: "review-1", Text: "draft", DraftRevision: "rev-1"})
		case "GET /api/v1/features/feat-1/reviews/review-1":
			writeJSON(w, http.StatusOK, ReviewSessionResponse{APIVersion: APIVersion, FeatureID: fixtureFeatureID, ReviewID: "review-1", Text: "draft", DraftRevision: "rev-1"})
		case "PUT /api/v1/features/feat-1/reviews/review-1/draft":
			sawReviewSaveTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
			var req ReviewDraftUpdateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode review draft request: %v", err)
			}
			if req.BaseRevision != "rev-1" || req.Text != "edited" {
				t.Errorf("review draft request = %+v, want rev-1 edited", req)
			}
			writeJSON(w, http.StatusOK, ReviewSessionResponse{APIVersion: APIVersion, FeatureID: fixtureFeatureID, ReviewID: "review-1", Text: "edited", DraftRevision: "rev-2"})
		case "POST /api/v1/features/feat-1/reviews/review-1/decision":
			sawReviewDecisionTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
			var req ReviewSessionDecisionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode review session decision request: %v", err)
			}
			if req.Decision != reviewDecisionProceed || req.BaseRevision != "rev-2" {
				t.Errorf("review session decision request = %+v, want proceed rev-2", req)
			}
			writeJSON(w, http.StatusOK, ReviewSessionDecisionResponse{APIVersion: APIVersion, FeatureID: fixtureFeatureID, ReviewID: "review-1", Decision: reviewDecisionProceed, Result: "submitted"})
		case "DELETE /api/v1/features/feat-1/reviews/review-1":
			sawReviewCancelTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
			writeJSON(w, http.StatusOK, ReviewSessionDecisionResponse{APIVersion: APIVersion, FeatureID: fixtureFeatureID, ReviewID: "review-1", Result: "cancelled"})
		case routeGetLivePreview:
			writeJSON(w, http.StatusOK, LivePreviewResponse{APIVersion: APIVersion, Feature: FeatureSummary{ID: fixtureFeatureID}})
		case "GET /api/v1/recovery":
			writeJSON(w, http.StatusOK, RecoverySnapshotResponse{
				APIVersion: APIVersion,
				SnapshotID: fixtureRecoverySnapshotID,
				Items: []RecoveryItemDTO{{
					Key:            fixtureRecoveryItemKey,
					FeatureID:      fixtureFeatureID,
					RepoName:       repoNameAPI,
					Phase:          targetPhaseImplement,
					DefaultAction:  recoveryActionSkip,
					AllowedActions: []string{actionResume, recoveryActionKill, recoveryActionSkip},
				}},
			})
		case "POST /api/v1/features":
			sawTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
			var req CreateFeatureRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			if req.Name != "New feature" {
				t.Errorf("create request name = %q, want New feature", req.Name)
			}
			writeJSON(w, http.StatusCreated, CreateFeatureResponse{APIVersion: APIVersion, FeatureID: "feat-created", Result: resultCreated})
		case "POST /api/v1/features/feat-1/actions/resume":
			sawResumeTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode resume request: %v", err)
			}
			if len(req) != 0 {
				t.Errorf("resume request = %+v, want empty JSON object", req)
			}
			writeJSON(w, http.StatusOK, FeatureStartResponse{APIVersion: APIVersion, FeatureID: fixtureFeatureID, Result: resultStarted})
		case "POST /api/v1/recovery/actions":
			sawRecoveryTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
			var req RecoveryActionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode recovery request: %v", err)
			}
			if req.SnapshotID != fixtureRecoverySnapshotID || req.Actions[fixtureRecoveryItemKey] != actionResume {
				t.Errorf("recovery request = %+v, want snapshot action resume", req)
			}
			writeJSON(w, http.StatusOK, RecoveryActionResponse{APIVersion: APIVersion, Result: resultRecovered})
		case "POST /api/v1/prompts/chat/start":
			sawChatTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
			var req ChatStartRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode chat request: %v", err)
			}
			if req.Message != "What is running?" {
				t.Errorf("chat request message = %q, want prompt", req.Message)
			}
			writeJSON(w, http.StatusOK, ChatStartResponse{APIVersion: APIVersion, SessionID: "chat-1", Result: resultStarted})
		case "POST /api/v1/features/feat-1/actions/publish/description":
			sawPublishDescriptionTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
			var req PublishDescriptionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode publish description request: %v", err)
			}
			if req.Model != modelSonnet || req.FeatureName != testFeatureName {
				t.Errorf("publish description request = %+v, want model and feature context", req)
			}
			writeJSON(w, http.StatusOK, PublishDescriptionResponse{APIVersion: APIVersion, FeatureID: fixtureFeatureID, Title: testFeatureName, Body: "AI body", Result: resultGenerated})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx := context.Background()
	if health, err := client.Health(ctx); err != nil || health.Status != "ok" {
		t.Fatalf("Health() = %+v, %v; want ok", health, err)
	}
	if features, err := client.Features(ctx); err != nil || len(features.Features) != 1 {
		t.Fatalf("Features() = %+v, %v; want one feature", features, err)
	}
	if detail, err := client.FeatureDetail(ctx, fixtureFeatureID); err != nil || detail.Feature.ID != fixtureFeatureID {
		t.Fatalf("FeatureDetail() = %+v, %v; want feat-1", detail, err)
	}
	if _, err := client.RuntimeConfig(ctx); err != nil {
		t.Fatalf("RuntimeConfig() error = %v", err)
	}
	if _, err := client.FeatureConfig(ctx, fixtureFeatureID); err != nil {
		t.Fatalf("FeatureConfig() error = %v", err)
	}
	if _, err := client.ModelCatalog(ctx); err != nil {
		t.Fatalf("ModelCatalog() error = %v", err)
	}
	if _, err := client.Prompts(ctx); err != nil {
		t.Fatalf("Prompts() error = %v", err)
	}
	if _, err := client.Permissions(ctx); err != nil {
		t.Fatalf("Permissions() error = %v", err)
	}
	if _, err := client.Sessions(ctx); err != nil {
		t.Fatalf("Sessions() error = %v", err)
	}
	if _, err := client.SessionDetail(ctx, fixtureSessionID); err != nil {
		t.Fatalf("SessionDetail() error = %v", err)
	}
	if _, err := client.Transcript(ctx, fixtureSessionID, CursorQuery{Cursor: 2, Limit: 10}); err != nil {
		t.Fatalf("Transcript() error = %v", err)
	}
	if _, err := client.ArtifactList(ctx, fixtureFeatureID, 1); err != nil {
		t.Fatalf("ArtifactList() error = %v", err)
	}
	if _, err := client.ArtifactContent(ctx, fixtureFeatureID, 1, targetPhasePlan, TextQuery{Limit: 8}); err != nil {
		t.Fatalf("ArtifactContent() error = %v", err)
	}
	if _, err := client.LogContent(ctx, fixtureFeatureID, 1, logIDSession, TextQuery{}); err != nil {
		t.Fatalf("LogContent() error = %v", err)
	}
	review, err := client.CreateReviewSession(ctx, fixtureFeatureID)
	if err != nil {
		t.Fatalf("CreateReviewSession() error = %v", err)
	}
	if review.ReviewID != "review-1" || !sawReviewCreateTrustedHeader {
		t.Fatalf("CreateReviewSession() = %+v trusted=%v, want review-1 trusted", review, sawReviewCreateTrustedHeader)
	}
	if _, err := client.ReviewSession(ctx, fixtureFeatureID, "review-1"); err != nil {
		t.Fatalf("ReviewSession() error = %v", err)
	}
	saved, err := client.SaveReviewDraft(ctx, fixtureFeatureID, "review-1", ReviewDraftUpdateRequest{BaseRevision: "rev-1", Text: "edited"})
	if err != nil {
		t.Fatalf("SaveReviewDraft() error = %v", err)
	}
	if saved.DraftRevision != "rev-2" || !sawReviewSaveTrustedHeader {
		t.Fatalf("SaveReviewDraft() = %+v trusted=%v, want rev-2 trusted", saved, sawReviewSaveTrustedHeader)
	}
	decided, err := client.SubmitReviewSessionDecision(ctx, fixtureFeatureID, "review-1", ReviewSessionDecisionRequest{Decision: reviewDecisionProceed, BaseRevision: "rev-2"})
	if err != nil {
		t.Fatalf("SubmitReviewSessionDecision() error = %v", err)
	}
	if decided.Result != "submitted" || !sawReviewDecisionTrustedHeader {
		t.Fatalf("SubmitReviewSessionDecision() = %+v trusted=%v, want submitted trusted", decided, sawReviewDecisionTrustedHeader)
	}
	cancelled, err := client.CancelReviewSession(ctx, fixtureFeatureID, "review-1")
	if err != nil {
		t.Fatalf("CancelReviewSession() error = %v", err)
	}
	if cancelled.Result != "cancelled" || !sawReviewCancelTrustedHeader {
		t.Fatalf("CancelReviewSession() = %+v trusted=%v, want cancelled trusted", cancelled, sawReviewCancelTrustedHeader)
	}
	if _, err := client.LivePreview(ctx, fixtureFeatureID); err != nil {
		t.Fatalf("LivePreview() error = %v", err)
	}
	recovery, err := client.Recovery(ctx)
	if err != nil {
		t.Fatalf("Recovery() error = %v", err)
	}
	if recovery.SnapshotID != fixtureRecoverySnapshotID || len(recovery.Items) != 1 || recovery.Items[0].Key != fixtureRecoveryItemKey {
		t.Fatalf("Recovery() = %+v; want typed recovery snapshot", recovery)
	}
	created, err := client.CreateFeature(ctx, CreateFeatureRequest{Name: "New feature"})
	if err != nil {
		t.Fatalf("CreateFeature() error = %v", err)
	}
	if created.FeatureID != "feat-created" || created.Result != resultCreated || !sawTrustedHeader {
		t.Fatalf("CreateFeature() = %+v trusted=%v, want created with trusted header", created, sawTrustedHeader)
	}
	resumed, err := client.ResumeFeature(ctx, fixtureFeatureID)
	if err != nil {
		t.Fatalf("ResumeFeature() error = %v", err)
	}
	if resumed.FeatureID != fixtureFeatureID || resumed.Result != resultStarted || !sawResumeTrustedHeader {
		t.Fatalf("ResumeFeature() = %+v trusted=%v, want started with trusted header", resumed, sawResumeTrustedHeader)
	}
	recovered, err := client.ExecuteRecovery(ctx, RecoveryActionRequest{
		SnapshotID: fixtureRecoverySnapshotID,
		Actions:    map[string]string{fixtureRecoveryItemKey: actionResume},
	})
	if err != nil {
		t.Fatalf("ExecuteRecovery() error = %v", err)
	}
	if recovered.Result != resultRecovered || !sawRecoveryTrustedHeader {
		t.Fatalf("ExecuteRecovery() = %+v trusted=%v, want recovered with trusted header", recovered, sawRecoveryTrustedHeader)
	}
	chat, err := client.StartChat(ctx, ChatStartRequest{Message: "What is running?"})
	if err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	if chat.SessionID != "chat-1" || chat.Result != resultStarted || !sawChatTrustedHeader {
		t.Fatalf("StartChat() = %+v trusted=%v, want chat session with trusted header", chat, sawChatTrustedHeader)
	}
	desc, err := client.GeneratePublishDescription(ctx, fixtureFeatureID, PublishDescriptionRequest{Model: modelSonnet, FeatureName: testFeatureName})
	if err != nil {
		t.Fatalf("GeneratePublishDescription() error = %v", err)
	}
	if desc.FeatureID != fixtureFeatureID || desc.Title != testFeatureName || desc.Body != "AI body" || desc.Result != resultGenerated || !sawPublishDescriptionTrustedHeader {
		t.Fatalf("GeneratePublishDescription() = %+v trusted=%v, want generated description with trusted header", desc, sawPublishDescriptionTrustedHeader)
	}
}

func TestClientAnswerPermissionSendsExplicitRememberFields(t *testing.T) {
	t.Parallel()

	var got PermissionAnswerRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != apiPathPermissionsAnswer {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeJSON(w, http.StatusOK, PermissionAnswerResponse{
			RequestID: got.RequestID,
			SessionID: got.SessionID,
			Decision:  got.Decision,
			Result:    resultAnswered,
		})
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	scope := "agentic-orchestrator"
	_, err = client.AnswerPermission(context.Background(), PermissionAnswerRequest{
		RequestID:       fixturePermissionRequestID,
		SessionID:       fixtureSessionID,
		Decision:        decisionAllowRemember,
		RememberPattern: testRememberPattern,
		RememberScope:   &scope,
	})
	if err != nil {
		t.Fatalf("AnswerPermission() error = %v", err)
	}
	if got.Decision != decisionAllowRemember || got.RememberPattern != testRememberPattern || got.RememberScope == nil || *got.RememberScope != scope {
		t.Fatalf("request = %+v, want explicit remember fields", got)
	}
}

func TestClientFeatureConfigReadAndUpdate(t *testing.T) {
	t.Run("read_returns_structured_feature_config", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != testFeatureConfigPath {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
			}
			writeJSON(w, http.StatusOK, FeatureConfigResponse{
				APIVersion: APIVersion,
				FeatureID:  fixtureFeatureID,
				Current: FeatureConfigDTO{
					Inquireness: inquirenessAlways,
					Pipeline:    string(feature.PipelineLarge),
					Checkpoints: CheckpointsDTO{ManualPublish: true},
				},
				Defaults: FeatureConfigDTO{Inquireness: "auto"},
				Original: FeatureConfigDTO{Pipeline: string(feature.PipelineMedium)},
				Publish: PublishabilityDTO{
					ManualPublish: true,
					Repos:         map[string]bool{"app": true},
				},
			})
		}))
		defer srv.Close()

		client, err := NewClient(ClientOptions{BaseURL: srv.URL})
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		config, err := client.FeatureConfig(context.Background(), fixtureFeatureID)
		if err != nil {
			t.Fatalf("FeatureConfig() error = %v", err)
		}
		if config.FeatureID != fixtureFeatureID || config.Current.Inquireness != inquirenessAlways || config.Current.Pipeline != string(feature.PipelineLarge) {
			t.Fatalf("FeatureConfig() = %+v, want structured current config for feat-1", config)
		}
		if !config.Current.Checkpoints.ManualPublish || !config.Publish.ManualPublish || !config.Publish.Repos["app"] {
			t.Fatalf("FeatureConfig() publish/checkpoints = %+v/%+v, want manual publish repo app", config.Current.Checkpoints, config.Publish)
		}
	})

	t.Run("update_posts_typed_request_and_returns_action_result", func(t *testing.T) {
		var sawTrustedHeader bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != testFeatureConfigPath {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
			}
			sawTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
			if got := r.Header.Get("Content-Type"); got != contentTypeJSON {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			var req FeatureConfigMutationRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode update request: %v", err)
			}
			if req.Inquireness != inquirenessAlways || req.Pipeline != feature.PipelineLarge {
				t.Fatalf("update request = %+v, want inquireness always pipeline large", req)
			}
			writeJSON(w, http.StatusOK, FeatureConfigUpdateResponse{
				APIVersion: APIVersion, FeatureID: fixtureFeatureID,
				Result: resultUpdated,
			})
		}))
		defer srv.Close()

		client, err := NewClient(ClientOptions{BaseURL: srv.URL})
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		updated, err := client.UpdateFeatureConfig(context.Background(), fixtureFeatureID, FeatureConfigMutationRequest{
			Inquireness: inquirenessAlways,
			Pipeline:    feature.PipelineLarge,
		})
		if err != nil {
			t.Fatalf("UpdateFeatureConfig() error = %v", err)
		}
		if updated.FeatureID != fixtureFeatureID || updated.Result != resultUpdated || !sawTrustedHeader {
			t.Fatalf("UpdateFeatureConfig() = %+v trusted=%v, want updated result with trusted header", updated, sawTrustedHeader)
		}
	})

	t.Run("update_returns_structured_api_error", func(t *testing.T) {
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
		defer srv.Close()

		client, err := NewClient(ClientOptions{BaseURL: srv.URL})
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		_, err = client.UpdateFeatureConfig(context.Background(), fixtureFeatureID, FeatureConfigMutationRequest{Inquireness: inquirenessAlways})
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("UpdateFeatureConfig() error = %v, want APIError", err)
		}
		if apiErr.Code != errCodeConflict || apiErr.Status != http.StatusConflict || apiErr.Method != http.MethodPost || apiErr.Path != testFeatureConfigPath {
			t.Fatalf("APIError = %+v, want method/path scoped conflict", apiErr)
		}
		if apiErr.Target["feature_id"] != fixtureFeatureID {
			t.Fatalf("APIError target = %+v, want feature_id feat-1", apiErr.Target)
		}
	})
}

func TestClientRuntimeConfigUpdateSendsWorkspaceRoots(t *testing.T) {
	var sawTrustedHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/config/runtime" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		sawTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
		var req RuntimeConfigMutationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode update request: %v", err)
		}
		if req.WorkspaceRoots == nil || len(*req.WorkspaceRoots) != 2 || (*req.WorkspaceRoots)[0] != "/workspace/a" || (*req.WorkspaceRoots)[1] != "/workspace/b" {
			t.Fatalf("workspace roots request = %+v, want supplied roots", req.WorkspaceRoots)
		}
		writeJSON(w, http.StatusOK, RuntimeConfigUpdateResponse{
			APIVersion: APIVersion, Result: resultUpdated,
		})
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	roots := []string{"/workspace/a", "/workspace/b"}
	updated, err := client.UpdateRuntimeConfig(context.Background(), RuntimeConfigMutationRequest{WorkspaceRoots: &roots})
	if err != nil {
		t.Fatalf("UpdateRuntimeConfig() error = %v", err)
	}
	if updated.Result != resultUpdated || !sawTrustedHeader {
		t.Fatalf("UpdateRuntimeConfig() = %+v trusted=%v, want updated trusted result", updated, sawTrustedHeader)
	}
}

func TestClientRewindFeatureSendsUpgradePipeline(t *testing.T) {
	var sawTrustedHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/features/feat-1/actions/rewind" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		sawTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
		var req RewindFeatureRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rewind request: %v", err)
		}
		if req.TargetPhase != targetPhaseInquire || req.RoadmapPhase != 2 || req.UpgradePipeline != feature.PipelineLarge {
			t.Fatalf("rewind request = %+v, want inquire roadmap phase 2 large upgrade", req)
		}
		writeJSON(w, http.StatusOK, RewindFeatureResponse{
			APIVersion: APIVersion, FeatureID: fixtureFeatureID,
			Result:          resultRewound,
			TargetPhase:     targetPhaseInquire,
			RoadmapPhase:    2,
			UpgradePipeline: string(feature.PipelineLarge),
		})
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	rewound, err := client.RewindFeature(context.Background(), fixtureFeatureID, RewindFeatureRequest{
		TargetPhase:     targetPhaseInquire,
		RoadmapPhase:    2,
		UpgradePipeline: feature.PipelineLarge,
	})
	if err != nil {
		t.Fatalf("RewindFeature() error = %v", err)
	}
	if rewound.FeatureID != fixtureFeatureID || rewound.Result != resultRewound || !sawTrustedHeader {
		t.Fatalf("RewindFeature() = %+v trusted=%v, want rewound trusted result", rewound, sawTrustedHeader)
	}
}

func TestClientShutdownPostsTrustedMutationAndReturnsResult(t *testing.T) {
	var sawTrustedHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/shutdown" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		sawTrustedHeader = r.Header.Get("X-Agentico-Client") == trustedClientHeaderValue
		if got := r.Header.Get("Content-Type"); got != contentTypeJSON {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode shutdown request: %v", err)
		}
		if len(req) != 0 {
			t.Fatalf("shutdown request = %+v, want empty JSON object", req)
		}
		writeJSON(w, http.StatusOK, ShutdownResponse{
			APIVersion: APIVersion, Result: resultShutdownScheduled,
		})
	}))
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	shutdown, err := client.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if shutdown.Result != resultShutdownScheduled || !sawTrustedHeader {
		t.Fatalf("Shutdown() = %+v trusted=%v, want shutdown_scheduled trusted result", shutdown, sawTrustedHeader)
	}
}

func TestClientReturnsStructuredErrorsMalformedResponsesAndTimeouts(t *testing.T) {
	t.Run("structured_error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusConflict, ErrorResponse{APIVersion: APIVersion, Error: ErrorDTO{Code: errCodeConflict, Message: "busy", Status: http.StatusConflict}})
		}))
		defer srv.Close()
		client, err := NewClient(ClientOptions{BaseURL: srv.URL})
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		_, err = client.Features(context.Background())
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("Features() error = %v, want APIError", err)
		}
		if apiErr.Code != errCodeConflict || apiErr.Status != http.StatusConflict {
			t.Fatalf("APIError = %+v, want conflict 409", apiErr)
		}
	})
	t.Run("malformed_json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"api_version":`))
		}))
		defer srv.Close()
		client, err := NewClient(ClientOptions{BaseURL: srv.URL})
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		if _, err := client.Features(context.Background()); err == nil || !strings.Contains(err.Error(), "decode response") {
			t.Fatalf("Features() error = %v, want decode response error", err)
		}
	})
	t.Run("bounded_timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			writeJSON(w, http.StatusOK, FeatureListResponse{APIVersion: APIVersion})
		}))
		defer srv.Close()
		client, err := NewClient(ClientOptions{BaseURL: srv.URL, Timeout: 10 * time.Millisecond})
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		if _, err := client.Features(context.Background()); err == nil {
			t.Fatal("Features() error = nil, want timeout")
		}
	})
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

func TestClientSendsBearerTokenOnReadsMutationsAndEvents(t *testing.T) {
	var sawReadAuth, sawMutationAuth, sawEventAuth atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("%s %s Authorization = %q, want bearer token", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		switch r.Method + " " + r.URL.Path {
		case routeGetFeatures:
			sawReadAuth.Store(true)
			writeJSON(w, http.StatusOK, FeatureListResponse{APIVersion: APIVersion})
		case "POST /api/v1/shutdown":
			sawMutationAuth.Store(true)
			writeJSON(w, http.StatusOK, ShutdownResponse{APIVersion: APIVersion, Result: "ok"})
		case "GET /api/v1/events":
			sawEventAuth.Store(true)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: connected\ndata: {\"api_version\":\"v1\",\"id\":\"1\",\"kind\":\"connected\",\"resource\":{\"type\":\"runtime\"},\"snapshot_required\":true}\n\n"))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	client, err := NewClient(ClientOptions{BaseURL: srv.URL, Token: testAuthToken})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Features(context.Background()); err != nil {
		t.Fatalf("Features() error = %v", err)
	}
	if _, err := client.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	signals, errs := client.SubscribeEvents(ctx, EventSubscriptionOptions{})
	_ = waitRefreshSignal(t, signals)
	cancel()
	<-errs
	if !sawReadAuth.Load() || !sawMutationAuth.Load() || !sawEventAuth.Load() {
		t.Fatalf("auth seen read=%v mutation=%v event=%v; want all true", sawReadAuth.Load(), sawMutationAuth.Load(), sawEventAuth.Load())
	}
}

func TestClientSSEReconnectAndSnapshotRefresh(t *testing.T) {
	var eventConnections atomic.Int32
	var secondAfter atomic.Value
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiPathEvents:
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			n := eventConnections.Add(1)
			if n == 1 {
				_, _ = w.Write([]byte("id: 1\nevent: connected\ndata: {\"api_version\":\"v1\",\"id\":\"1\",\"kind\":\"connected\",\"resource\":{\"type\":\"runtime\"},\"snapshot_required\":true}\n\n"))
				flusher.Flush()
				return
			}
			secondAfter.Store(r.URL.Query().Get("after"))
			_, _ = w.Write([]byte("id: 2\nevent: lifecycle.updated\ndata: {\"api_version\":\"v1\",\"id\":\"2\",\"kind\":\"lifecycle.updated\",\"resource\":{\"type\":\"feature\",\"feature_id\":\"feat-1\"},\"snapshot_required\":true}\n\n"))
			flusher.Flush()
			<-r.Context().Done()
		case "/api/v1/features/feat-1":
			writeJSON(w, http.StatusOK, FeatureDetailResponse{APIVersion: APIVersion, Feature: testFeatureDetail(FeatureSummary{ID: fixtureFeatureID})})
		case "/api/v1/features/feat-1/live-preview":
			writeJSON(w, http.StatusOK, LivePreviewResponse{APIVersion: APIVersion, Feature: FeatureSummary{ID: fixtureFeatureID}})
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	signals, errs := client.SubscribeEvents(ctx, EventSubscriptionOptions{
		HeartbeatInterval: 10 * time.Millisecond,
		ReconnectDelay:    10 * time.Millisecond,
	})
	first := waitRefreshSignal(t, signals)
	if first.Event.Kind != sseEventConnected || !first.SnapshotRequired {
		t.Fatalf("first signal = %+v, want connected snapshot", first)
	}
	second := waitRefreshSignal(t, signals)
	if second.Event.Kind != sseEventLifecycleUpdated || second.Resource.FeatureID != fixtureFeatureID {
		t.Fatalf("second signal = %+v, want feature lifecycle update for feat-1", second)
	}
	snapshot, err := client.FetchRefreshSnapshot(context.Background(), second)
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if snapshot.Feature == nil || snapshot.Feature.Feature.ID != fixtureFeatureID || snapshot.LivePreview == nil {
		t.Fatalf("FetchRefreshSnapshot() = %+v, want feature and live preview snapshots", snapshot)
	}
	if got, _ := secondAfter.Load().(string); got != "1" {
		t.Fatalf("second event connection after = %q, want 1", got)
	}
	cancel()
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("SubscribeEvents() terminal error = %v, want nil after cancel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SSE subscription shutdown")
	}
}

func TestClientFetchRefreshSnapshotIgnoresSessionOutputActivity(t *testing.T) {
	var sawSessionDetail bool
	var sawTranscript bool
	var sawLivePreview bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case routeGetSession1:
			sawSessionDetail = true
			writeJSON(w, http.StatusOK, SessionDetailResponse{APIVersion: APIVersion, Session: testSessionDetail(
				SessionSummaryDTO{ID: fixtureSessionID, FeatureID: fixtureFeatureID},
				CursorDTO{Total: 53, Start: 0, End: 53},
			)})
		case routeGetSession1Transcript:
			sawTranscript = true
			if got := r.URL.Query().Get("offset"); got != "3" {
				t.Errorf("transcript offset = %q, want 3", got)
			}
			if got := r.URL.Query().Get("limit"); got != "50" {
				t.Errorf("transcript limit = %q, want 50", got)
			}
			writeJSON(w, http.StatusOK, TranscriptResponse{
				APIVersion: APIVersion,
				Cursor:     CursorDTO{Total: 53, Start: 3, End: 53},
				Messages:   []TranscriptMessageDTO{{Index: 52, Role: roleAssistant, Type: blockTypeText, Text: "fresh tail"}},
			})
		case routeGetPrompts:
			writeJSON(w, http.StatusOK, PromptSnapshotResponse{APIVersion: APIVersion})
		case routeGetLivePreview:
			sawLivePreview = true
			writeJSON(w, http.StatusOK, LivePreviewResponse{APIVersion: APIVersion, Feature: FeatureSummary{ID: fixtureFeatureID}, Activity: "Using Bash..."})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	snapshot, err := client.FetchRefreshSnapshot(context.Background(), RefreshSignal{
		Event:    SSEEventDTO{Kind: sseEventSessionOutputActivity},
		Resource: ResourceDTO{Type: resourceTypeSession, ID: fixtureSessionID, FeatureID: fixtureFeatureID},
	})
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if sawSessionDetail || sawTranscript || sawLivePreview {
		t.Fatalf("session output activity triggered refetches session=%v transcript=%v live_preview=%v; want none", sawSessionDetail, sawTranscript, sawLivePreview)
	}
	if snapshot != (RefreshSnapshot{}) {
		t.Fatalf("FetchRefreshSnapshot() = %+v, want empty snapshot for output activity", snapshot)
	}
}

func TestClientFetchRefreshSnapshotIncludesPromptSnapshotForSessionUpdate(t *testing.T) {
	var sawSessionDetail bool
	var sawSessions bool
	var sawPrompts bool
	var sawLivePreview bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case routeGetSession1:
			sawSessionDetail = true
			writeJSON(w, http.StatusOK, SessionDetailResponse{APIVersion: APIVersion, Session: testSessionDetail(
				SessionSummaryDTO{ID: fixtureSessionID, FeatureID: fixtureFeatureID, Status: "Running"},
				CursorDTO{},
			)})
		case routeGetSessions:
			sawSessions = true
			writeJSON(w, http.StatusOK, SessionListResponse{APIVersion: APIVersion, Sessions: []SessionSummaryDTO{
				{ID: fixtureSessionID, FeatureID: fixtureFeatureID, Phase: feature.PhaseKnowledgeBase.String(), Repo: "dbaccess", Status: "Running"},
				{ID: "sess-2", FeatureID: fixtureFeatureID, Phase: feature.PhaseKnowledgeBase.String(), Repo: "taulu", Status: "Running"},
			}})
		case routeGetPrompts:
			sawPrompts = true
			writeJSON(w, http.StatusOK, PromptSnapshotResponse{APIVersion: APIVersion})
		case routeGetLivePreview:
			sawLivePreview = true
			writeJSON(w, http.StatusOK, LivePreviewResponse{APIVersion: APIVersion, Feature: FeatureSummary{ID: fixtureFeatureID}, Activity: "Working"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	snapshot, err := client.FetchRefreshSnapshot(context.Background(), RefreshSignal{
		Event:    SSEEventDTO{Kind: sseEventSessionUpdated},
		Resource: ResourceDTO{Type: resourceTypeSession, ID: fixtureSessionID, FeatureID: fixtureFeatureID},
	})
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if !sawSessionDetail || snapshot.Session == nil || snapshot.Session.Session.ID != fixtureSessionID {
		t.Fatalf("FetchRefreshSnapshot() session = %+v, sawSessionDetail=%v; want sess-1 detail", snapshot.Session, sawSessionDetail)
	}
	if !sawSessions || snapshot.Sessions == nil || len(snapshot.Sessions.Sessions) != 2 {
		t.Fatalf("FetchRefreshSnapshot() sessions = %+v, sawSessions=%v; want sibling session list", snapshot.Sessions, sawSessions)
	}
	if !sawPrompts || snapshot.Prompts == nil || len(snapshot.Prompts.AskUserQuestions) != 0 {
		t.Fatalf("FetchRefreshSnapshot() prompts = %+v, sawPrompts=%v; want empty prompt snapshot to clear stale client prompts", snapshot.Prompts, sawPrompts)
	}
	if !sawLivePreview || snapshot.LivePreview == nil || snapshot.LivePreview.Feature.ID != fixtureFeatureID {
		t.Fatalf("FetchRefreshSnapshot() live preview = %+v, sawLivePreview=%v; want feat-1 preview", snapshot.LivePreview, sawLivePreview)
	}
}

func TestClientFetchRefreshSnapshotIncludesPromptSnapshotForFeatureScopedSessionUpdate(t *testing.T) {
	var sawSessions bool
	var sawPrompts bool
	var sawLivePreview bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case routeGetSessions:
			sawSessions = true
			writeJSON(w, http.StatusOK, SessionListResponse{APIVersion: APIVersion, Sessions: []SessionSummaryDTO{
				{ID: fixtureSessionID, FeatureID: fixtureFeatureID, Status: sessionStatusWaitingHelp},
			}})
		case routeGetPrompts:
			sawPrompts = true
			writeJSON(w, http.StatusOK, PromptSnapshotResponse{APIVersion: APIVersion, HelpQueue: []HelpQueueDTO{
				{FeatureID: fixtureFeatureID, Question: agentQuestionPrompt, Pending: true},
			}})
		case routeGetLivePreview:
			sawLivePreview = true
			writeJSON(w, http.StatusOK, LivePreviewResponse{APIVersion: APIVersion, Feature: FeatureSummary{ID: fixtureFeatureID}, Activity: "Waiting"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	snapshot, err := client.FetchRefreshSnapshot(context.Background(), RefreshSignal{
		Event:    SSEEventDTO{Kind: sseEventSessionUpdated},
		Resource: ResourceDTO{Type: resourceTypeSession, FeatureID: fixtureFeatureID},
	})
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if !sawSessions || snapshot.Sessions == nil || len(snapshot.Sessions.Sessions) != 1 {
		t.Fatalf("FetchRefreshSnapshot() sessions = %+v, sawSessions=%v; want session list", snapshot.Sessions, sawSessions)
	}
	if !sawPrompts || snapshot.Prompts == nil || len(snapshot.Prompts.HelpQueue) != 1 {
		t.Fatalf("FetchRefreshSnapshot() prompts = %+v, sawPrompts=%v; want prompt snapshot for feature-scoped session update", snapshot.Prompts, sawPrompts)
	}
	if !sawLivePreview || snapshot.LivePreview == nil || snapshot.LivePreview.Feature.ID != fixtureFeatureID {
		t.Fatalf("FetchRefreshSnapshot() live preview = %+v, sawLivePreview=%v; want feat-1 preview", snapshot.LivePreview, sawLivePreview)
	}
}

func TestClientFetchRefreshSnapshotHydratesLivePreviewSessionForFeatureScopedSessionUpdate(t *testing.T) {
	var sawSessions bool
	var sawPrompts bool
	var sawLivePreview bool
	var sawSessionDetail bool
	var sawTranscript bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case routeGetSessions:
			sawSessions = true
			writeJSON(w, http.StatusOK, SessionListResponse{APIVersion: APIVersion, Sessions: []SessionSummaryDTO{
				{ID: fixtureSessionID, FeatureID: fixtureFeatureID, Status: sessionStatusWaitingHelp},
			}})
		case routeGetPrompts:
			sawPrompts = true
			writeJSON(w, http.StatusOK, PromptSnapshotResponse{APIVersion: APIVersion})
		case routeGetLivePreview:
			sawLivePreview = true
			writeJSON(w, http.StatusOK, LivePreviewResponse{
				APIVersion: APIVersion,
				Feature:    FeatureSummary{ID: fixtureFeatureID},
				Session:    &SessionSummaryDTO{ID: fixtureSessionID, FeatureID: fixtureFeatureID, Status: sessionStatusWaitingHelp},
				Activity:   "Waiting",
			})
		case routeGetSession1:
			sawSessionDetail = true
			writeJSON(w, http.StatusOK, SessionDetailResponse{APIVersion: APIVersion, Session: testSessionDetail(
				SessionSummaryDTO{ID: fixtureSessionID, FeatureID: fixtureFeatureID, Status: sessionStatusWaitingHelp},
				CursorDTO{Total: 7, Start: 0, End: 7},
			)})
		case routeGetSession1Transcript:
			sawTranscript = true
			writeJSON(w, http.StatusOK, TranscriptResponse{
				APIVersion: APIVersion,
				Cursor:     CursorDTO{Total: 7, Start: 0, End: 7},
				Messages:   []TranscriptMessageDTO{{Index: 6, Role: roleAssistant, Type: blockTypeText, Text: askUserQuestionSampleText}},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	snapshot, err := client.FetchRefreshSnapshot(context.Background(), RefreshSignal{
		Event:    SSEEventDTO{Kind: sseEventSessionUpdated},
		Resource: ResourceDTO{Type: resourceTypeSession, FeatureID: fixtureFeatureID},
	})
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if !sawSessions || snapshot.Sessions == nil {
		t.Fatalf("FetchRefreshSnapshot() sessions = %+v, sawSessions=%v; want session list", snapshot.Sessions, sawSessions)
	}
	if !sawPrompts || snapshot.Prompts == nil {
		t.Fatalf("FetchRefreshSnapshot() prompts = %+v, sawPrompts=%v; want prompt snapshot", snapshot.Prompts, sawPrompts)
	}
	if !sawLivePreview || snapshot.LivePreview == nil || snapshot.LivePreview.Session == nil || snapshot.LivePreview.Session.ID != fixtureSessionID {
		t.Fatalf("FetchRefreshSnapshot() live preview = %+v, sawLivePreview=%v; want sess-1 preview", snapshot.LivePreview, sawLivePreview)
	}
	if !sawSessionDetail || snapshot.Session == nil || snapshot.Session.Session.ID != fixtureSessionID {
		t.Fatalf("FetchRefreshSnapshot() session = %+v, sawSessionDetail=%v; want live preview session detail", snapshot.Session, sawSessionDetail)
	}
	if !sawTranscript || snapshot.Transcript == nil || len(snapshot.Transcript.Messages) != 1 || snapshot.Transcript.Messages[0].Text != askUserQuestionSampleText {
		t.Fatalf("FetchRefreshSnapshot() transcript = %+v, sawTranscript=%v; want live preview session transcript", snapshot.Transcript, sawTranscript)
	}
}

func TestClientFetchRefreshSnapshotResnapshotsPromptsOnConnected(t *testing.T) {
	var sawFeatures, sawPrompts, sawPermissions, sawSessions bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case routeGetFeatures:
			sawFeatures = true
			writeJSON(w, http.StatusOK, FeatureListResponse{APIVersion: APIVersion, Features: []FeatureSummary{
				{ID: fixtureFeatureID, Status: "Designing"},
			}})
		case routeGetPrompts:
			sawPrompts = true
			writeJSON(w, http.StatusOK, PromptSnapshotResponse{APIVersion: APIVersion, AskUserQuestions: []ControlRequestDTO{
				{RequestID: "req-1", FeatureID: fixtureFeatureID, ToolName: toolNameAskUserQuestion, Status: controlRequestStatusPending},
			}})
		case "GET /api/v1/permissions":
			sawPermissions = true
			writeJSON(w, http.StatusOK, PermissionSnapshotResponse{APIVersion: APIVersion})
		case routeGetSessions:
			sawSessions = true
			writeJSON(w, http.StatusOK, SessionListResponse{APIVersion: APIVersion, Sessions: []SessionSummaryDTO{
				{ID: "feat-1-design", FeatureID: fixtureFeatureID, Status: sessionStatusWaitingHelp},
			}})
		case routeGetHealth:
			writeJSON(w, http.StatusOK, HealthResponse{APIVersion: APIVersion})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	// The `connected` event (and snapshot_required heartbeats) signal the client
	// to re-snapshot. A session sitting idle in WaitingHelp emits no further
	// events, so this (re)connect is the only chance to pull the pending
	// question into the prompt snapshot that drives the dashboard help badge.
	snapshot, err := client.FetchRefreshSnapshot(context.Background(), RefreshSignal{
		Event:            SSEEventDTO{Kind: sseEventConnected, Resource: ResourceDTO{Type: resourceTypeRuntime}, SnapshotRequired: true},
		Resource:         ResourceDTO{Type: resourceTypeRuntime},
		SnapshotRequired: true,
	})
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if !sawPrompts || snapshot.Prompts == nil || len(snapshot.Prompts.AskUserQuestions) != 1 {
		t.Fatalf("FetchRefreshSnapshot() prompts = %+v, sawPrompts=%v; want re-snapshotted prompts on snapshot_required", snapshot.Prompts, sawPrompts)
	}
	if !sawFeatures || snapshot.Features == nil || len(snapshot.Features.Features) != 1 {
		t.Fatalf("FetchRefreshSnapshot() features = %+v, sawFeatures=%v; want re-snapshotted features", snapshot.Features, sawFeatures)
	}
	if !sawPermissions || snapshot.Permissions == nil {
		t.Fatalf("FetchRefreshSnapshot() permissions = %+v, sawPermissions=%v; want re-snapshotted permissions", snapshot.Permissions, sawPermissions)
	}
	if !sawSessions || snapshot.Sessions == nil || len(snapshot.Sessions.Sessions) != 1 {
		t.Fatalf("FetchRefreshSnapshot() sessions = %+v, sawSessions=%v; want re-snapshotted sessions", snapshot.Sessions, sawSessions)
	}
}

func TestClientFetchRefreshSnapshotIncludesFeatureForTerminalSession(t *testing.T) {
	var sawSessionDetail bool
	var sawSessions bool
	var sawLivePreview bool
	var sawFeatureDetail bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case routeGetSession1:
			sawSessionDetail = true
			writeJSON(w, http.StatusOK, SessionDetailResponse{APIVersion: APIVersion, Session: testSessionDetail(
				SessionSummaryDTO{ID: fixtureSessionID, FeatureID: fixtureFeatureID, Status: sessionStatusDone},
				CursorDTO{Total: 0, Start: 0, End: 0},
			)})
		case routeGetSessions:
			sawSessions = true
			writeJSON(w, http.StatusOK, SessionListResponse{APIVersion: APIVersion, Sessions: []SessionSummaryDTO{
				{ID: fixtureSessionID, FeatureID: fixtureFeatureID, Status: sessionStatusDone},
			}})
		case routeGetPrompts:
			writeJSON(w, http.StatusOK, PromptSnapshotResponse{APIVersion: APIVersion})
		case routeGetLivePreview:
			sawLivePreview = true
			writeJSON(w, http.StatusOK, LivePreviewResponse{
				APIVersion: APIVersion,
				Feature:    FeatureSummary{ID: fixtureFeatureID, Status: featureStatusCodeReady, CurrentPhase: actionPublish},
				Activity:   sessionStatusDone,
			})
		case "GET /api/v1/features/feat-1":
			sawFeatureDetail = true
			writeJSON(w, http.StatusOK, FeatureDetailResponse{APIVersion: APIVersion, Feature: testFeatureDetail(FeatureSummary{ID: fixtureFeatureID, Status: featureStatusCodeReady, CurrentPhase: actionPublish})})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	snapshot, err := client.FetchRefreshSnapshot(context.Background(), RefreshSignal{
		Event:    SSEEventDTO{Kind: sseEventSessionUpdated},
		Resource: ResourceDTO{Type: resourceTypeSession, ID: fixtureSessionID, FeatureID: fixtureFeatureID},
	})
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if !sawSessionDetail || snapshot.Session == nil || snapshot.Session.Session.ID != fixtureSessionID {
		t.Fatalf("FetchRefreshSnapshot() session = %+v, sawSessionDetail=%v; want sess-1 detail", snapshot.Session, sawSessionDetail)
	}
	if !sawSessions || snapshot.Sessions == nil || len(snapshot.Sessions.Sessions) != 1 {
		t.Fatalf("FetchRefreshSnapshot() sessions = %+v, sawSessions=%v; want terminal session list refresh", snapshot.Sessions, sawSessions)
	}
	if !sawLivePreview || snapshot.LivePreview == nil || snapshot.LivePreview.Feature.ID != fixtureFeatureID || snapshot.LivePreview.Activity != sessionStatusDone {
		t.Fatalf("FetchRefreshSnapshot() live preview = %+v, sawLivePreview=%v; want terminal preview", snapshot.LivePreview, sawLivePreview)
	}
	if !sawFeatureDetail || snapshot.Feature == nil || snapshot.Feature.Feature.Status != featureStatusCodeReady || snapshot.Feature.Feature.CurrentPhase != actionPublish {
		t.Fatalf("FetchRefreshSnapshot() feature = %+v, sawFeatureDetail=%v; want refreshed feature detail", snapshot.Feature, sawFeatureDetail)
	}
}

func TestClientFetchRefreshSnapshotSkipsLivePreviewForChatSession(t *testing.T) {
	var sawSessionDetail bool
	var sawTranscript bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/sessions/__chat__":
			sawSessionDetail = true
			writeJSON(w, http.StatusOK, SessionDetailResponse{APIVersion: APIVersion, Session: testSessionDetail(
				SessionSummaryDTO{ID: ChatSessionID, FeatureID: ChatSessionID},
				CursorDTO{Total: 2, Start: 0, End: 2},
			)})
		case "GET /api/v1/sessions/__chat__/transcript":
			sawTranscript = true
			writeJSON(w, http.StatusOK, TranscriptResponse{
				APIVersion: APIVersion,
				Cursor:     CursorDTO{Total: 2, Start: 0, End: 2},
				Messages: []TranscriptMessageDTO{
					{Index: 0, Role: roleAssistant, Type: blockTypeText, Text: "monthly spend limit"},
					{Index: 1, Role: roleSystem, Type: "result", Status: "error", Redacted: true},
				},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	snapshot, err := client.FetchRefreshSnapshot(context.Background(), RefreshSignal{
		Event:    SSEEventDTO{Kind: sseEventSessionUpdated},
		Resource: ResourceDTO{Type: resourceTypeSession, ID: ChatSessionID, FeatureID: ChatSessionID},
	})
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if !sawSessionDetail || snapshot.Session == nil || snapshot.Session.Session.ID != ChatSessionID {
		t.Fatalf("FetchRefreshSnapshot() session = %+v, sawSessionDetail=%v; want chat detail", snapshot.Session, sawSessionDetail)
	}
	if !sawTranscript || snapshot.Transcript == nil || len(snapshot.Transcript.Messages) != 2 {
		t.Fatalf("FetchRefreshSnapshot() transcript = %+v, sawTranscript=%v; want chat transcript", snapshot.Transcript, sawTranscript)
	}
	if snapshot.LivePreview != nil {
		t.Fatalf("FetchRefreshSnapshot() live preview = %+v, want nil for chat utility session", snapshot.LivePreview)
	}
}

func TestClientFetchRefreshSnapshotIncludesRecoveryForRecoveryUpdates(t *testing.T) {
	var sawRecovery bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/recovery":
			sawRecovery = true
			writeJSON(w, http.StatusOK, RecoverySnapshotResponse{
				APIVersion: APIVersion,
				SnapshotID: "recovery-snapshot-2",
				Items: []RecoveryItemDTO{{
					Key:            fixtureRecoveryItemKey,
					FeatureID:      fixtureFeatureID,
					FeatureName:    testFeatureName,
					RepoName:       repoNameAPI,
					Phase:          targetPhaseImplement,
					Iteration:      8,
					PID:            4321,
					ProcessAlive:   true,
					DefaultAction:  recoveryActionKill,
					AllowedActions: []string{recoveryActionKill, recoveryActionSkip},
				}},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	snapshot, err := client.FetchRefreshSnapshot(context.Background(), RefreshSignal{
		Event:    SSEEventDTO{Kind: sseEventRecoveryUpdated},
		Resource: ResourceDTO{Type: resourceTypeRuntime},
	})
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if !sawRecovery || snapshot.Recovery == nil || snapshot.Recovery.SnapshotID != "recovery-snapshot-2" || len(snapshot.Recovery.Items) != 1 {
		t.Fatalf("FetchRefreshSnapshot() recovery = %+v, sawRecovery=%v; want recovery snapshot", snapshot.Recovery, sawRecovery)
	}
}

func waitRefreshSignal(t *testing.T, signals <-chan RefreshSignal) RefreshSignal {
	t.Helper()
	select {
	case sig, ok := <-signals:
		if !ok {
			t.Fatal("refresh signal channel closed")
		}
		return sig
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for refresh signal")
	}
	return RefreshSignal{}
}

func testFeatureDetail(summary FeatureSummary) FeatureDetailDTO {
	return featureDetailFromSummary(summary)
}

func testSessionDetail(summary SessionSummaryDTO, cursor CursorDTO) SessionDetailDTO {
	detail := sessionDetailFromSummary(summary)
	detail.TranscriptCursor = cursor
	return detail
}

// StaticFreshnessProvider is a test-only RepoFreshnessProvider backed by a
// fixed repo-name-to-freshness map, used by read_api_contract_test.go.
type StaticFreshnessProvider map[string]RepoFreshness

func (p StaticFreshnessProvider) Freshness(_ *feature.Feature, repo feature.FeatureRepo) RepoFreshness {
	if status, ok := p[repo.Name]; ok {
		return status
	}
	return RepoFreshnessUnknown
}
