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

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestClientFetchesTypedSnapshotsAndAcceptedMutations(t *testing.T) {
	var sawTrustedHeader bool
	var sawResumeTrustedHeader bool
	var sawRecoveryTrustedHeader bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/health":
			writeJSON(w, http.StatusOK, HealthResponse{APIVersion: APIVersion, Status: "ok"})
		case "GET /api/v1/features":
			writeJSON(w, http.StatusOK, FeatureListResponse{APIVersion: APIVersion, Features: []FeatureSummary{{ID: "feat-1", Name: "Client cutover"}}})
		case "GET /api/v1/features/feat-1":
			writeJSON(w, http.StatusOK, FeatureDetailResponse{APIVersion: APIVersion, Feature: FeatureDetailDTO{FeatureSummary: FeatureSummary{ID: "feat-1", Name: "Client cutover"}}})
		case "GET /api/v1/config/runtime":
			writeJSON(w, http.StatusOK, RuntimeConfigResponse{APIVersion: APIVersion, Runtime: RuntimeIdentity{StateDir: "/runtime/features"}})
		case "GET /api/v1/features/feat-1/config":
			writeJSON(w, http.StatusOK, FeatureConfigResponse{APIVersion: APIVersion, FeatureID: "feat-1"})
		case "GET /api/v1/catalog/models":
			writeJSON(w, http.StatusOK, ModelCatalogResponse{APIVersion: APIVersion, ProviderOrder: []string{"codex"}})
		case "GET /api/v1/prompts":
			writeJSON(w, http.StatusOK, PromptSnapshotResponse{APIVersion: APIVersion})
		case "GET /api/v1/permissions":
			writeJSON(w, http.StatusOK, PermissionSnapshotResponse{APIVersion: APIVersion})
		case "GET /api/v1/sessions":
			writeJSON(w, http.StatusOK, SessionListResponse{APIVersion: APIVersion, Sessions: []SessionSummaryDTO{{ID: "sess-1", FeatureID: "feat-1"}}})
		case "GET /api/v1/sessions/sess-1":
			writeJSON(w, http.StatusOK, SessionDetailResponse{APIVersion: APIVersion, Session: SessionDetailDTO{SessionSummaryDTO: SessionSummaryDTO{ID: "sess-1", FeatureID: "feat-1"}}})
		case "GET /api/v1/sessions/sess-1/transcript":
			if got := r.URL.Query().Get("offset"); got != "2" {
				t.Errorf("transcript offset = %q, want 2", got)
			}
			writeJSON(w, http.StatusOK, TranscriptResponse{APIVersion: APIVersion, Cursor: CursorDTO{Start: 2, End: 3}})
		case "GET /api/v1/features/feat-1/runs/1/artifacts":
			writeJSON(w, http.StatusOK, ArtifactListResponse{APIVersion: APIVersion, Artifacts: []ArtifactDTO{{ID: "plan", RunNumber: 1}}})
		case "GET /api/v1/features/feat-1/runs/1/artifacts/plan":
			if got := r.URL.Query().Get("limit"); got != "8" {
				t.Errorf("artifact limit = %q, want 8", got)
			}
			writeJSON(w, http.StatusOK, TextContentResponse{APIVersion: APIVersion, ID: "plan", Text: "phase"})
		case "GET /api/v1/features/feat-1/runs/1/logs/session":
			writeJSON(w, http.StatusOK, TextContentResponse{APIVersion: APIVersion, ID: "session", Text: "log"})
		case "GET /api/v1/features/feat-1/live-preview":
			writeJSON(w, http.StatusOK, LivePreviewResponse{APIVersion: APIVersion, Feature: FeatureSummary{ID: "feat-1"}})
		case "GET /api/v1/operations":
			writeJSON(w, http.StatusOK, OperationSnapshotResponse{APIVersion: APIVersion, Operations: []OperationDTO{{ID: "op-1", Status: OperationStatusQueued}}})
		case "GET /api/v1/recovery":
			writeJSON(w, http.StatusOK, RecoverySnapshotResponse{
				APIVersion: APIVersion,
				SnapshotID: "recovery-snapshot-1",
				Items: []RecoveryItemDTO{{
					Key:            "feat-1:api",
					FeatureID:      "feat-1",
					RepoName:       "api",
					Phase:          "implement",
					DefaultAction:  "skip",
					AllowedActions: []string{"resume", "kill", "skip"},
				}},
			})
		case "POST /api/v1/features":
			sawTrustedHeader = r.Header.Get("X-Agentico-Client") == "local"
			var req CreateFeatureRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			if req.Name != "New feature" {
				t.Errorf("create request name = %q, want New feature", req.Name)
			}
			writeJSON(w, http.StatusAccepted, OperationAcceptedResponse{APIVersion: APIVersion, OperationID: "op-create", Status: OperationStatusQueued})
		case "POST /api/v1/features/feat-1/actions/resume":
			sawResumeTrustedHeader = r.Header.Get("X-Agentico-Client") == "local"
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode resume request: %v", err)
			}
			if len(req) != 0 {
				t.Errorf("resume request = %+v, want empty JSON object", req)
			}
			writeJSON(w, http.StatusAccepted, OperationAcceptedResponse{APIVersion: APIVersion, OperationID: "op-resume", Status: OperationStatusQueued})
		case "POST /api/v1/recovery/actions":
			sawRecoveryTrustedHeader = r.Header.Get("X-Agentico-Client") == "local"
			var req RecoveryActionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode recovery request: %v", err)
			}
			if req.SnapshotID != "recovery-snapshot-1" || req.Actions["feat-1:api"] != "resume" {
				t.Errorf("recovery request = %+v, want snapshot action resume", req)
			}
			writeJSON(w, http.StatusAccepted, OperationAcceptedResponse{APIVersion: APIVersion, OperationID: "op-recovery", Status: OperationStatusQueued})
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
	if detail, err := client.FeatureDetail(ctx, "feat-1"); err != nil || detail.Feature.ID != "feat-1" {
		t.Fatalf("FeatureDetail() = %+v, %v; want feat-1", detail, err)
	}
	if _, err := client.RuntimeConfig(ctx); err != nil {
		t.Fatalf("RuntimeConfig() error = %v", err)
	}
	if _, err := client.FeatureConfig(ctx, "feat-1"); err != nil {
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
	if _, err := client.SessionDetail(ctx, "sess-1"); err != nil {
		t.Fatalf("SessionDetail() error = %v", err)
	}
	if _, err := client.Transcript(ctx, "sess-1", CursorQuery{Cursor: 2, Limit: 10}); err != nil {
		t.Fatalf("Transcript() error = %v", err)
	}
	if _, err := client.ArtifactList(ctx, "feat-1", 1); err != nil {
		t.Fatalf("ArtifactList() error = %v", err)
	}
	if _, err := client.ArtifactContent(ctx, "feat-1", 1, "plan", TextQuery{Limit: 8}); err != nil {
		t.Fatalf("ArtifactContent() error = %v", err)
	}
	if _, err := client.LogContent(ctx, "feat-1", 1, "session", TextQuery{}); err != nil {
		t.Fatalf("LogContent() error = %v", err)
	}
	if _, err := client.LivePreview(ctx, "feat-1"); err != nil {
		t.Fatalf("LivePreview() error = %v", err)
	}
	if _, err := client.Operations(ctx, OperationQuery{}); err != nil {
		t.Fatalf("Operations() error = %v", err)
	}
	recovery, err := client.Recovery(ctx)
	if err != nil {
		t.Fatalf("Recovery() error = %v", err)
	}
	if recovery.SnapshotID != "recovery-snapshot-1" || len(recovery.Items) != 1 || recovery.Items[0].Key != "feat-1:api" {
		t.Fatalf("Recovery() = %+v; want typed recovery snapshot", recovery)
	}
	accepted, err := client.CreateFeature(ctx, CreateFeatureRequest{Name: "New feature"})
	if err != nil {
		t.Fatalf("CreateFeature() error = %v", err)
	}
	if accepted.OperationID != "op-create" || !sawTrustedHeader {
		t.Fatalf("CreateFeature() = %+v trusted=%v, want accepted with trusted header", accepted, sawTrustedHeader)
	}
	accepted, err = client.ResumeFeature(ctx, "feat-1")
	if err != nil {
		t.Fatalf("ResumeFeature() error = %v", err)
	}
	if accepted.OperationID != "op-resume" || !sawResumeTrustedHeader {
		t.Fatalf("ResumeFeature() = %+v trusted=%v, want accepted with trusted header", accepted, sawResumeTrustedHeader)
	}
	accepted, err = client.ExecuteRecovery(ctx, RecoveryActionRequest{
		SnapshotID: "recovery-snapshot-1",
		Actions:    map[string]string{"feat-1:api": "resume"},
	})
	if err != nil {
		t.Fatalf("ExecuteRecovery() error = %v", err)
	}
	if accepted.OperationID != "op-recovery" || !sawRecoveryTrustedHeader {
		t.Fatalf("ExecuteRecovery() = %+v trusted=%v, want accepted with trusted header", accepted, sawRecoveryTrustedHeader)
	}
}

func TestClientFeatureConfigReadAndUpdate(t *testing.T) {
	t.Run("read_returns_structured_feature_config", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/features/feat-1/config" {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
			}
			writeJSON(w, http.StatusOK, FeatureConfigResponse{
				APIVersion: APIVersion,
				FeatureID:  "feat-1",
				Current: FeatureConfigDTO{
					Inquireness: "always",
					Pipeline:    "large",
					Checkpoints: CheckpointsDTO{ManualPublish: true},
				},
				Defaults: FeatureConfigDTO{Inquireness: "auto"},
				Original: FeatureConfigDTO{Pipeline: "medium"},
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
		config, err := client.FeatureConfig(context.Background(), "feat-1")
		if err != nil {
			t.Fatalf("FeatureConfig() error = %v", err)
		}
		if config.FeatureID != "feat-1" || config.Current.Inquireness != "always" || config.Current.Pipeline != "large" {
			t.Fatalf("FeatureConfig() = %+v, want structured current config for feat-1", config)
		}
		if !config.Current.Checkpoints.ManualPublish || !config.Publish.ManualPublish || !config.Publish.Repos["app"] {
			t.Fatalf("FeatureConfig() publish/checkpoints = %+v/%+v, want manual publish repo app", config.Current.Checkpoints, config.Publish)
		}
	})

	t.Run("update_posts_typed_request_and_returns_accepted_operation", func(t *testing.T) {
		var sawTrustedHeader bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/features/feat-1/config" {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
			}
			sawTrustedHeader = r.Header.Get("X-Agentico-Client") == "local"
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			var req FeatureConfigMutationRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode update request: %v", err)
			}
			if req.Inquireness != "always" || string(req.Pipeline) != "large" {
				t.Fatalf("update request = %+v, want inquireness always pipeline large", req)
			}
			writeJSON(w, http.StatusAccepted, OperationAcceptedResponse{
				APIVersion:  APIVersion,
				OperationID: "op-config",
				Status:      OperationStatusQueued,
			})
		}))
		defer srv.Close()

		client, err := NewClient(ClientOptions{BaseURL: srv.URL})
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		accepted, err := client.UpdateFeatureConfig(context.Background(), "feat-1", FeatureConfigMutationRequest{
			Inquireness: "always",
			Pipeline:    "large",
		})
		if err != nil {
			t.Fatalf("UpdateFeatureConfig() error = %v", err)
		}
		if accepted.OperationID != "op-config" || accepted.Status != OperationStatusQueued || !sawTrustedHeader {
			t.Fatalf("UpdateFeatureConfig() = %+v trusted=%v, want queued accepted operation with trusted header", accepted, sawTrustedHeader)
		}
	})

	t.Run("update_returns_structured_api_error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/features/feat-1/config" {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
			}
			writeJSON(w, http.StatusConflict, ErrorResponse{
				APIVersion: APIVersion,
				Error: ErrorDTO{
					Code:    "operation_conflict",
					Message: "operation already queued",
					Status:  http.StatusConflict,
					Target:  map[string]any{"feature_id": "feat-1"},
				},
			})
		}))
		defer srv.Close()

		client, err := NewClient(ClientOptions{BaseURL: srv.URL})
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		_, err = client.UpdateFeatureConfig(context.Background(), "feat-1", FeatureConfigMutationRequest{Inquireness: "always"})
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("UpdateFeatureConfig() error = %v, want APIError", err)
		}
		if apiErr.Code != "operation_conflict" || apiErr.Status != http.StatusConflict || apiErr.Method != http.MethodPost || apiErr.Path != "/api/v1/features/feat-1/config" {
			t.Fatalf("APIError = %+v, want method/path scoped conflict", apiErr)
		}
		if apiErr.Target["feature_id"] != "feat-1" {
			t.Fatalf("APIError target = %+v, want feature_id feat-1", apiErr.Target)
		}
	})
}

func TestClientReturnsStructuredErrorsMalformedResponsesAndTimeouts(t *testing.T) {
	t.Run("structured_error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusConflict, ErrorResponse{APIVersion: APIVersion, Error: ErrorDTO{Code: "conflict", Message: "busy", Status: http.StatusConflict}})
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
		if apiErr.Code != "conflict" || apiErr.Status != http.StatusConflict {
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
		{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: "first transcript row"}}}}},
		{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: "second transcript row"}}}}},
	}
	handler := NewHandler(HandlerOptions{
		Sessions: fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
			id: "sess-1", featureID: "feat-1", messages: messages,
		}}},
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	first, err := client.Transcript(context.Background(), "sess-1", CursorQuery{Limit: 1})
	if err != nil {
		t.Fatalf("Transcript(first) error = %v", err)
	}
	if first.Cursor.Start != 0 || first.Cursor.End != 1 || len(first.Messages) != 1 || first.Messages[0].Text != "first transcript row" {
		t.Fatalf("Transcript(first) = cursor %+v messages %+v; want first row", first.Cursor, first.Messages)
	}
	second, err := client.Transcript(context.Background(), "sess-1", CursorQuery{Cursor: first.Cursor.End, Limit: 1})
	if err != nil {
		t.Fatalf("Transcript(second) error = %v", err)
	}
	if second.Cursor.Start != 1 || second.Cursor.End != 2 || len(second.Messages) != 1 || second.Messages[0].Text != "second transcript row" {
		t.Fatalf("Transcript(second) = cursor %+v messages %+v; want continuation row", second.Cursor, second.Messages)
	}
}

func TestClientSSEReconnectAndSnapshotRefresh(t *testing.T) {
	var eventConnections atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			n := eventConnections.Add(1)
			if n == 1 {
				_, _ = w.Write([]byte("id: 1\nevent: connected\ndata: {\"api_version\":\"v1\",\"id\":\"1\",\"kind\":\"connected\",\"resource\":{\"type\":\"runtime\"},\"snapshot_required\":true}\n\n"))
				flusher.Flush()
				return
			}
			_, _ = w.Write([]byte("id: 2\nevent: operation.updated\ndata: {\"api_version\":\"v1\",\"id\":\"2\",\"kind\":\"operation.updated\",\"resource\":{\"type\":\"operation\",\"id\":\"op-1\",\"feature_id\":\"feat-1\"},\"snapshot_required\":true}\n\n"))
			flusher.Flush()
			<-r.Context().Done()
		case "/api/v1/operations":
			writeJSON(w, http.StatusOK, OperationSnapshotResponse{APIVersion: APIVersion, Operations: []OperationDTO{{ID: "op-1", Status: OperationStatusSucceeded}}})
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
	if first.Event.Kind != "connected" || !first.SnapshotRequired {
		t.Fatalf("first signal = %+v, want connected snapshot", first)
	}
	second := waitRefreshSignal(t, signals)
	if second.Event.Kind != "operation.updated" || second.Resource.ID != "op-1" {
		t.Fatalf("second signal = %+v, want operation update for op-1", second)
	}
	snapshot, err := client.FetchRefreshSnapshot(context.Background(), second)
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if snapshot.Operations == nil || len(snapshot.Operations.Operations) != 1 {
		t.Fatalf("FetchRefreshSnapshot() = %+v, want operation snapshot", snapshot)
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

func TestClientFetchRefreshSnapshotIncludesLivePreviewForSessionOutput(t *testing.T) {
	var sawSessionDetail bool
	var sawTranscript bool
	var sawLivePreview bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/sessions/sess-1":
			sawSessionDetail = true
			writeJSON(w, http.StatusOK, SessionDetailResponse{APIVersion: APIVersion, Session: SessionDetailDTO{
				SessionSummaryDTO: SessionSummaryDTO{ID: "sess-1", FeatureID: "feat-1"},
				TranscriptCursor:  CursorDTO{Total: 53, Start: 0, End: 53},
			}})
		case "GET /api/v1/sessions/sess-1/transcript":
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
				Messages:   []TranscriptMessageDTO{{Index: 52, Role: "assistant", Type: "text", Text: "fresh tail"}},
			})
		case "GET /api/v1/features/feat-1/live-preview":
			sawLivePreview = true
			writeJSON(w, http.StatusOK, LivePreviewResponse{APIVersion: APIVersion, Feature: FeatureSummary{ID: "feat-1"}, Activity: "Using Bash..."})
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
		Event:    SSEEventDTO{Kind: "log.updated"},
		Resource: ResourceDTO{Type: "session", ID: "sess-1", FeatureID: "feat-1"},
	})
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if !sawSessionDetail || snapshot.Session == nil || snapshot.Session.Session.ID != "sess-1" {
		t.Fatalf("FetchRefreshSnapshot() session = %+v, sawSessionDetail=%v; want sess-1 detail", snapshot.Session, sawSessionDetail)
	}
	if !sawTranscript || snapshot.Transcript == nil || len(snapshot.Transcript.Messages) != 1 || snapshot.Transcript.Messages[0].Text != "fresh tail" {
		t.Fatalf("FetchRefreshSnapshot() transcript = %+v, sawTranscript=%v; want bounded tail", snapshot.Transcript, sawTranscript)
	}
	if !sawLivePreview || snapshot.LivePreview == nil || snapshot.LivePreview.Feature.ID != "feat-1" || snapshot.LivePreview.Activity != "Using Bash..." {
		t.Fatalf("FetchRefreshSnapshot() live preview = %+v, sawLivePreview=%v; want feat-1 preview", snapshot.LivePreview, sawLivePreview)
	}
}

func TestClientFetchRefreshSnapshotIncludesRecoveryForRecoveryUpdates(t *testing.T) {
	var sawHealth bool
	var sawRecovery bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/health":
			sawHealth = true
			writeJSON(w, http.StatusOK, HealthResponse{APIVersion: APIVersion, Status: "ok"})
		case "GET /api/v1/recovery":
			sawRecovery = true
			writeJSON(w, http.StatusOK, RecoverySnapshotResponse{
				APIVersion: APIVersion,
				SnapshotID: "recovery-snapshot-2",
				Items: []RecoveryItemDTO{{
					Key:            "feat-1:api",
					FeatureID:      "feat-1",
					FeatureName:    "Client cutover",
					RepoName:       "api",
					Phase:          "implement",
					Iteration:      8,
					PID:            4321,
					ProcessAlive:   true,
					DefaultAction:  "kill",
					AllowedActions: []string{"kill", "skip"},
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
		Event:    SSEEventDTO{Kind: "recovery.updated"},
		Resource: ResourceDTO{Type: "runtime"},
	})
	if err != nil {
		t.Fatalf("FetchRefreshSnapshot() error = %v", err)
	}
	if !sawHealth || snapshot.Health == nil || snapshot.Health.Status != "ok" {
		t.Fatalf("FetchRefreshSnapshot() health = %+v, sawHealth=%v; want ok health", snapshot.Health, sawHealth)
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
