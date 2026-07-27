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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// testRepoPath is a fake repo path used across redaction tests in this file
// and read_api_contract_test.go to assert secrets never leak into responses.
const testRepoPath = "/repo/path"

// Shared test-only fixture literals used across this file, client_test.go,
// sse_test.go, discovery_test.go, read_api_contract_test.go and
// openapi_contract_test.go.
const (
	// worktreePathLiteral is a fake worktree path asserted to never leak.
	worktreePathLiteral = "/worktree/path"
	// runtimeDirLiteral is the fake RuntimeIdentity.RuntimeDir used in tests.
	runtimeDirLiteral = "/runtime"
	// repoNameSelf is the fake name for this repo's own entry in test fixtures.
	repoNameSelf = "agentic-orchestrator"
	// repoNameAPI and repoNameWeb are fake secondary repo names used in
	// multi-repo fixtures.
	repoNameAPI = "api"
	repoNameWeb = "web"
	// contentTypeJSON is the expected JSON Content-Type response header value.
	contentTypeJSON = "application/json"
	// askUserQuestionSampleText is a sample AskUserQuestion prompt used across
	// question-related fixtures.
	askUserQuestionSampleText = "Choose?"
	// testRuntimeConfigPath is the fake RuntimeIdentity.Config path used
	// across most fixtures in this package's tests.
	testRuntimeConfigPath = "/runtime/config.yaml"
	// fixtureFeatureID is the standard fake feature ID used throughout this
	// package's HTTP-level tests.
	fixtureFeatureID = "feat-1"
	// fixtureFeatureIDAlt is a second fake feature ID used where a test needs
	// to distinguish it from fixtureFeatureID.
	fixtureFeatureIDAlt = "feat-001"
	// providerCodex and providerClaude are fake LLM provider name literals
	// used across launch-policy and catalog fixtures.
	providerCodex  = "codex"
	providerClaude = "claude"
	// secretBranchLiteral is a fake branch name asserted to never leak into
	// responses.
	secretBranchLiteral = "feature/secret"
	// modelGPT54, modelGPT54Mini and modelSonnet are fake catalog model ID
	// literals used across model-catalog fixtures.
	modelGPT54     = "gpt-5.4"
	modelGPT54Mini = "gpt-5.4-mini"
	modelSonnet    = "sonnet"
	// decisionKey is the PermissionAnswerRequest JSON body map key for the
	// decision field, used across permission-answer fixtures.
	decisionKey = "decision"
	// testRememberPattern is a fake permission remember_pattern value used
	// across remember-scope fixtures.
	testRememberPattern = "Bash(go test *)"
	// rememberPatternKey is the PermissionAnswerRequest JSON body map key for
	// the remember_pattern field, used across remember-scope fixtures.
	rememberPatternKey = "remember_pattern"
	// fixturePermissionRequestID is the standard fake PermissionRequest.ID
	// used throughout this package's HTTP-level tests.
	fixturePermissionRequestID = "perm-1"
	// fixtureSessionID is the standard fake session ID used throughout this
	// package's HTTP-level tests.
	fixtureSessionID = "sess-1"
	// testAuthToken is the standard fake bearer token used throughout this
	// package's HTTP-level tests.
	testAuthToken = "test-token"
	// requestIDKey is the PermissionAnswerRequest JSON body map key for the
	// request_id field, used across permission-answer fixtures.
	requestIDKey = "request_id"
)

func TestFeatureListDTOShapeAndNoAuthentication(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)
	f := &feature.Feature{
		ID:                  fixtureFeatureIDAlt,
		Name:                "Feature One",
		Slug:                "feature-one",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		Created:             created,
		ActiveRun:           2,
		RunCount:            3,
		CurrentIteration:    4,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  5,
		Checkpoints:         feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: repoNameSelf, Path: testRepoPath, WorktreePath: worktreePathLiteral, Branch: secretBranchLiteral},
		},
	}
	handler := NewHandler(HandlerOptions{
		Runtime: RuntimeIdentity{RuntimeDir: runtimeDirLiteral, StateDir: testRuntimeStateDir, Config: testRuntimeConfigPath},
		Features: featureListerFunc(func() ([]*feature.Feature, error) {
			return []*feature.Feature{f}, nil
		}),
		StartedAt:             created,
		DisableHostValidation: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/features", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q; want JSON", got)
	}
	var body FeatureListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.APIVersion != APIVersion {
		t.Fatalf("api_version = %q; want %q", body.APIVersion, APIVersion)
	}
	if len(body.Features) != 1 {
		t.Fatalf("features length = %d; want 1", len(body.Features))
	}
	got := body.Features[0]
	if got.ID != f.ID || got.Name != f.Name || got.Slug != f.Slug {
		t.Fatalf("summary identity = %+v; want feature identity", got)
	}
	if got.Status != "Implementing" || got.CurrentPhase != "Implement" {
		t.Fatalf("summary status/phase = %q/%q; want Implementing/Implement", got.Status, got.CurrentPhase)
	}
	if got.ActiveRun != 2 || got.RunCount != 3 || got.Progress.CurrentIteration != 4 {
		t.Fatalf("summary progress = %+v; want run/progress fields", got)
	}
	if !got.Checkpoints.ManualPublish {
		t.Fatalf("summary checkpoints = %+v; want manual_publish=true", got.Checkpoints)
	}
	if len(got.Repos) != 1 || got.Repos[0] != repoNameSelf {
		t.Fatalf("summary repos = %v; want repo names only", got.Repos)
	}
	raw := w.Body.String()
	for _, forbidden := range []string{testRepoPath, worktreePathLiteral, secretBranchLiteral, "models", "exit_criteria", "permissions_queue"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("response leaks internal field %q in:\n%s", forbidden, raw)
		}
	}
}

func TestSnapshotResponsesExposeAsOfSequence(t *testing.T) {
	t.Parallel()

	handler := newAPIHandler(HandlerOptions{
		Runtime: RuntimeIdentity{RuntimeDir: runtimeDirLiteral, StateDir: testRuntimeStateDir, Config: testRuntimeConfigPath},
		Features: featureListerFunc(func() ([]*feature.Feature, error) {
			return nil, nil
		}),
		DisableHostValidation: true,
	})
	handler.broker.publish(SSEEventDTO{Kind: testEventKindFeatureState, Resource: ResourceDTO{Type: entityFeature, ID: fixtureFeatureID}})

	w := httptest.NewRecorder()
	handler.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features", nil))

	resp := w.Result()
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Agentico-Seq"); got != "1" {
		t.Fatalf("X-Agentico-Seq = %q, want 1", got)
	}
	var body FeatureListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Meta.AsOfSeq != 1 {
		t.Fatalf("meta.as_of_seq = %d, want 1", body.Meta.AsOfSeq)
	}
}

func TestPermissionAnswerRejectsLegacyAndMissingRememberScope(t *testing.T) {
	t.Parallel()

	handler := NewHandler(HandlerOptions{
		Mutations:             permissionAnswerMutationTarget{},
		DisableHostValidation: true,
	})
	tests := []struct {
		name        string
		body        map[string]string
		wantMessage string
	}{
		{
			name:        "legacy allow decision",
			body:        map[string]string{requestIDKey: fixturePermissionRequestID, decisionKey: "allow"},
			wantMessage: errMessageInvalidDecision,
		},
		{
			name:        "uppercase allow once decision",
			body:        map[string]string{requestIDKey: fixturePermissionRequestID, decisionKey: "ALLOW_ONCE"},
			wantMessage: errMessageInvalidDecision,
		},
		{
			name:        "whitespace allow once decision",
			body:        map[string]string{requestIDKey: fixturePermissionRequestID, decisionKey: " allow_once "},
			wantMessage: errMessageInvalidDecision,
		},
		{
			name:        "allow remember without scope",
			body:        map[string]string{requestIDKey: fixturePermissionRequestID, decisionKey: decisionAllowRemember, rememberPatternKey: testRememberPattern},
			wantMessage: "remember_scope is required for allow_remember",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, apiPathPermissionsAnswer, bytes.NewReader(payload))
			req.Header.Set("Content-Type", contentTypeJSON)
			req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			resp := w.Result()
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d; want 400", resp.StatusCode)
			}
			var body ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != errCodeBadRequest || body.Error.Message != tc.wantMessage {
				t.Fatalf("error = %+v, want bad_request %q", body.Error, tc.wantMessage)
			}
		})
	}
}

func TestPermissionAnswerAcceptsExplicitDecisions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		body                map[string]string
		wantDecision        string
		wantRememberPattern string
		wantRememberScope   *string
	}{
		{
			name:         "allow once",
			body:         map[string]string{requestIDKey: fixturePermissionRequestID, decisionKey: decisionAllowOnce},
			wantDecision: decisionAllowOnce,
		},
		{
			name:         decisionDeny,
			body:         map[string]string{requestIDKey: fixturePermissionRequestID, decisionKey: decisionDeny},
			wantDecision: decisionDeny,
		},
		{
			name:                "allow remember with scope",
			body:                map[string]string{requestIDKey: fixturePermissionRequestID, decisionKey: decisionAllowRemember, rememberPatternKey: testRememberPattern, "remember_scope": repoNameSelf},
			wantDecision:        decisionAllowRemember,
			wantRememberPattern: testRememberPattern,
			wantRememberScope:   stringPtr(repoNameSelf),
		},
		{
			name:                "allow remember with empty global scope",
			body:                map[string]string{requestIDKey: fixturePermissionRequestID, decisionKey: decisionAllowRemember, rememberPatternKey: testRememberPattern, "remember_scope": ""},
			wantDecision:        decisionAllowRemember,
			wantRememberPattern: testRememberPattern,
			wantRememberScope:   stringPtr(""),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var received []PermissionAnswerRequest
			handler := NewHandler(HandlerOptions{
				Mutations:             permissionAnswerMutationTarget{received: &received},
				DisableHostValidation: true,
			})
			payload, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, apiPathPermissionsAnswer, bytes.NewReader(payload))
			req.Header.Set("Content-Type", contentTypeJSON)
			req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			resp := w.Result()
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d; want 200", resp.StatusCode)
			}
			if len(received) != 1 {
				t.Fatalf("received %d permission answers, want 1", len(received))
			}
			got := received[0]
			if got.Decision != tc.wantDecision || got.RememberPattern != tc.wantRememberPattern {
				t.Fatalf("request = %+v, want decision %q remember pattern %q", got, tc.wantDecision, tc.wantRememberPattern)
			}
			switch {
			case tc.wantRememberScope == nil && got.RememberScope != nil:
				t.Fatalf("remember scope = %q, want nil", *got.RememberScope)
			case tc.wantRememberScope != nil && got.RememberScope == nil:
				t.Fatalf("remember scope = nil, want %q", *tc.wantRememberScope)
			case tc.wantRememberScope != nil && *got.RememberScope != *tc.wantRememberScope:
				t.Fatalf("remember scope = %q, want %q", *got.RememberScope, *tc.wantRememberScope)
			}
		})
	}
}

func stringPtr(value string) *string {
	return &value
}

type permissionAnswerMutationTarget struct {
	MutationTarget
	received *[]PermissionAnswerRequest
}

func (t permissionAnswerMutationTarget) AnswerPermission(req PermissionAnswerRequest) (PermissionAnswerResponse, error) {
	if t.received != nil {
		*t.received = append(*t.received, req)
	}
	return PermissionAnswerResponse{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Decision:  req.Decision,
		Result:    resultAnswered,
	}, nil
}

func TestFeatureListEmptyRuntimeAndPartialWarnings(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{
		Runtime: RuntimeIdentity{RuntimeDir: runtimeDirLiteral, StateDir: testRuntimeStateDir, Config: testRuntimeConfigPath},
		Features: featureListerFunc(func() ([]*feature.Feature, error) {
			return nil, nil
		}),
		DisableHostValidation: true,
	})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features", nil))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("empty status = %d; want 200", w.Result().StatusCode)
	}
	var empty FeatureListResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&empty); err != nil {
		t.Fatalf("decode empty response: %v", err)
	}
	if len(empty.Features) != 0 || len(empty.Warnings) != 0 {
		t.Fatalf("empty response = %+v; want empty features and warnings", empty)
	}

	partial := &feature.PartialLoadError{Warnings: []feature.LoadWarning{
		{ID: "bad-001", Err: errors.New("parsing feature file: duplicate key")},
	}}
	handler = NewHandler(HandlerOptions{
		Runtime: RuntimeIdentity{RuntimeDir: runtimeDirLiteral, StateDir: testRuntimeStateDir, Config: testRuntimeConfigPath},
		Features: featureListerFunc(func() ([]*feature.Feature, error) {
			return []*feature.Feature{{ID: "good-001", Name: "Good", Slug: "good", Status: feature.StatusCreated, ActiveRun: 1, RunCount: 1}}, partial
		}),
		DisableHostValidation: true,
	})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features", nil))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("partial status = %d; want 200", w.Result().StatusCode)
	}
	var body FeatureListResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode partial response: %v", err)
	}
	if len(body.Features) != 1 || body.Features[0].ID != "good-001" {
		t.Fatalf("partial features = %+v; want good feature", body.Features)
	}
	if len(body.Warnings) != 1 || body.Warnings[0].FeatureID != "bad-001" || body.Warnings[0].Code != "partial_load" {
		t.Fatalf("partial warnings = %+v; want structured partial load warning", body.Warnings)
	}
}

func TestFeatureListDoesNotMutateStorageSchemas(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := feature.NewStore(dir)
	f := &feature.Feature{
		ID:            "readonly-001",
		Name:          "Read Only",
		Slug:          "read-only",
		Status:        feature.StatusCreated,
		CurrentPhase:  feature.PhaseResearch,
		Created:       time.Now().UTC().Truncate(time.Second),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	featurePath := filepath.Join(dir, f.ID, "feature.yaml")
	runPath := filepath.Join(dir, f.ID, "runs", feature.RunDirName(1), "run.yaml")
	beforeFeature, err := os.ReadFile(featurePath)
	if err != nil {
		t.Fatalf("ReadFile(feature before): %v", err)
	}
	beforeRun, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("ReadFile(run before): %v", err)
	}

	handler := NewHandler(HandlerOptions{
		Runtime:               RuntimeIdentity{RuntimeDir: filepath.Dir(dir), StateDir: dir, Config: filepath.Join(filepath.Dir(dir), "config.yaml")},
		Features:              store,
		DisableHostValidation: true,
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features", nil))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Result().StatusCode)
	}
	afterFeature, err := os.ReadFile(featurePath)
	if err != nil {
		t.Fatalf("ReadFile(feature after): %v", err)
	}
	afterRun, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("ReadFile(run after): %v", err)
	}
	if !bytes.Equal(beforeFeature, afterFeature) {
		t.Fatalf("feature.yaml mutated by read-only endpoint\nbefore:\n%s\nafter:\n%s", beforeFeature, afterFeature)
	}
	if !bytes.Equal(beforeRun, afterRun) {
		t.Fatalf("run.yaml mutated by read-only endpoint\nbefore:\n%s\nafter:\n%s", beforeRun, afterRun)
	}
}

func TestModelCatalogIncludesChatUtilityEligibility(t *testing.T) {
	t.Parallel()

	reg := llm.NewRegistry()
	reg.Register(fakeProvider{
		name: providerCodex,
		catalog: []llm.ModelInfo{
			{ID: modelGPT54, Category: "capable"},
			{ID: modelGPT54Mini, Category: "balanced"},
		},
	})
	handler := NewHandler(HandlerOptions{Registry: reg, DisableHostValidation: true})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/models", nil))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Result().StatusCode)
	}
	var body ModelCatalogResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	chatModels := body.PhaseProviderModels[string(llm.PhaseChat)][providerCodex]
	if len(chatModels) != 1 || chatModels[0] != modelGPT54Mini {
		t.Fatalf("chat utility models = %+v, want discovered balanced utility model", chatModels)
	}
}

func TestModelCatalogIncludesDedicatedAutomaticReviewEligibility(t *testing.T) {
	t.Parallel()

	reg := llm.NewRegistry()
	reg.Register(fakeProvider{
		name:     "claude",
		toolLess: true,
		catalog: []llm.ModelInfo{
			{ID: "haiku", Category: "cheap"},
			{ID: "opus", Category: "capable"},
		},
	})
	reg.Register(fakeProvider{
		name:     "opencode",
		toolLess: false,
		catalog:  []llm.ModelInfo{{ID: "anthropic/haiku", Category: "cheap"}},
	})
	handler := NewHandler(HandlerOptions{Registry: reg, DisableHostValidation: true})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/models", nil))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Result().StatusCode)
	}
	var body ModelCatalogResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	groups := body.PhaseProviderModels[string(llm.PhaseAutomaticReview)]
	if got, want := groups["claude"], []string{"haiku", "opus"}; !slices.Equal(got, want) {
		t.Fatalf("automatic-review Claude models = %v, want %v", got, want)
	}
	if _, ok := groups["opencode"]; ok {
		t.Fatalf("automatic-review catalog includes non-tool-less provider: %v", groups)
	}
}

type featureListerFunc func() ([]*feature.Feature, error)

func (f featureListerFunc) List() ([]*feature.Feature, error) {
	if f == nil {
		return nil, nil
	}
	return f()
}

func jsonResponse(v any) (*http.Response, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(v); err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body.Bytes())),
		Header:     make(http.Header),
	}, nil
}
