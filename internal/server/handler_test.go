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
	"fmt"
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

// relationshipCountingStore counts relationship scans so list-shaped
// endpoints can prove they resolve every parent's children in one pass
// instead of rescanning the store per parent.
type relationshipCountingStore struct {
	features       []*feature.Feature
	perParentCalls int
	bulkCalls      int
}

func (s *relationshipCountingStore) List() ([]*feature.Feature, error) { return s.features, nil }
func (s *relationshipCountingStore) Load(id string) (*feature.Feature, error) {
	for _, f := range s.features {
		if f.ID == id {
			return f, nil
		}
	}
	return nil, os.ErrNotExist
}
func (s *relationshipCountingStore) LoadRun(string, int) (*feature.Run, error) {
	return &feature.Run{}, nil
}
func (s *relationshipCountingStore) RunDir(string, int) string      { return "" }
func (s *relationshipCountingStore) ListRuns(string) ([]int, error) { return nil, nil }

func (s *relationshipCountingStore) classify() map[string]*feature.RelationshipChildren {
	all := map[string]*feature.RelationshipChildren{}
	for _, f := range s.features {
		if !f.IsChild() {
			continue
		}
		children := all[f.Parent.ParentID]
		if children == nil {
			children = &feature.RelationshipChildren{}
			all[f.Parent.ParentID] = children
		}
		if f.IsActiveChild() {
			children.Active = f
		} else {
			children.Closed = append(children.Closed, f)
		}
	}
	return all
}

func (s *relationshipCountingStore) RelationshipChildren(parentID string) (*feature.RelationshipChildren, error) {
	s.perParentCalls++
	children := s.classify()[parentID]
	if children == nil {
		children = &feature.RelationshipChildren{}
	}
	return children, nil
}

func (s *relationshipCountingStore) AllRelationshipChildren() (map[string]*feature.RelationshipChildren, error) {
	s.bulkCalls++
	return s.classify(), nil
}

func TestFeatureListResolvesRelationshipsInOneScan(t *testing.T) {
	t.Parallel()

	store := &relationshipCountingStore{features: []*feature.Feature{
		{ID: "parent-1", Name: "Parent One", Status: feature.StatusPublished, CurrentPhase: feature.PhasePublish},
		{ID: "parent-2", Name: "Parent Two", Status: feature.StatusPublished, CurrentPhase: feature.PhasePublish},
		{
			ID:           "child-1",
			Name:         "Pass",
			Status:       feature.StatusDesigning,
			CurrentPhase: feature.PhaseDesign,
			Parent:       &feature.ChildRelationship{ParentID: "parent-1", Kind: feature.ChildKindRefactor},
		},
	}}
	handler := NewHandler(HandlerOptions{
		Runtime:               RuntimeIdentity{RuntimeDir: runtimeDirLiteral, StateDir: testRuntimeStateDir, Config: testRuntimeConfigPath},
		FeatureStore:          store,
		DisableHostValidation: true,
	})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features", nil))
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	var body FeatureListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Features) != 2 {
		t.Fatalf("features length = %d; want 2 parents", len(body.Features))
	}
	var parentOne *FeatureSummary
	for i := range body.Features {
		if body.Features[i].ID == "parent-1" {
			parentOne = &body.Features[i]
		}
	}
	if parentOne == nil || parentOne.ActiveChild == nil || parentOne.ActiveChild.ID != "child-1" {
		t.Fatalf("parent-1 active child = %+v; want child-1", parentOne)
	}
	if store.bulkCalls != 1 || store.perParentCalls != 0 {
		t.Fatalf("relationship scans: bulk = %d, per-parent = %d; want one bulk scan and no per-parent scans", store.bulkCalls, store.perParentCalls)
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
	handler.broker.publish(SSEEvent{Kind: testEventKindFeatureState, Resource: Resource{Type: entityFeature, ID: fixtureFeatureID}})

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

// TestRefactorActionIsKnownAndUnknownActionsStayUnknown verifies that the
// restored refactor action routes to the mutation target while action strings
// without a handler (including refactor subactions) still hit the generic
// method-not-allowed response.
func TestRefactorActionIsKnownAndUnknownActionsStayUnknown(t *testing.T) {
	t.Parallel()

	t.Run("refactor is a known action", func(t *testing.T) {
		t.Parallel()
		target := &refactorMutationTarget{
			err: fmt.Errorf("%w: feat-1", feature.ErrRefactorParentNotFound),
		}
		handler := NewHandler(HandlerOptions{
			Mutations:             target,
			DisableHostValidation: true,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/features/feat-1/actions/refactor", bytes.NewReader([]byte(`{"name":"Rework auth"}`)))
		req.Header.Set("Content-Type", contentTypeJSON)
		req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		resp := w.Result()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d; want 404 for a missing refactor parent", resp.StatusCode)
		}
		var body ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Error.Code != errCodeRefactorParentNotFound {
			t.Fatalf("error code = %q; want %q", body.Error.Code, errCodeRefactorParentNotFound)
		}
	})

	for _, path := range []string{
		"/api/v1/features/feat-1/actions/refactor/restart",
		"/api/v1/features/feat-1/actions/unrecognized-action",
	} {
		t.Run(strings.TrimPrefix(path, "/api/v1/features/feat-1/actions/"), func(t *testing.T) {
			t.Parallel()

			handler := NewHandler(HandlerOptions{
				Mutations:             &refactorMutationTarget{},
				DisableHostValidation: true,
			})
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{}`)))
			req.Header.Set("Content-Type", contentTypeJSON)
			req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			resp := w.Result()
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d; want generic unknown-action response 405", resp.StatusCode)
			}
			var body ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != "method_not_allowed" {
				t.Fatalf("error code = %q; want method_not_allowed", body.Error.Code)
			}
		})
	}
}

// TestRefactorActionReturnsCreated verifies the typed wizard brief reaches
// the mutation target and that a successful launch returns 201 with the
// child identifier.
func TestRefactorActionReturnsCreated(t *testing.T) {
	t.Parallel()

	target := &refactorMutationTarget{
		resp: RefactorFeatureResponse{FeatureID: "child-1", ParentID: "feat-1", Result: resultCreated},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		DisableHostValidation: true,
	})
	body := `{
		"name": " Rework auth ",
		"description": "split the auth package",
		"images": ["~/shots/login.png"],
		"attachments": ["~/docs/auth.md"],
		"pipeline": "large",
		"checkpoints": {"inquiry_review": true},
		"effort": {"planning": "high"},
		"models": {"planning": "opus"},
		"risk_level": "low",
		"exit_criteria": "build passes",
		"inquireness": "medium"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/features/feat-1/actions/refactor", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; want 201; body = %s", resp.StatusCode, raw)
	}
	var out RefactorFeatureResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.FeatureID != "child-1" || out.ParentID != "feat-1" || out.Result != resultCreated {
		t.Fatalf("response = %+v; want feature_id child-1 parent_id feat-1 result created", out)
	}
	if len(target.received) != 1 {
		t.Fatalf("mutation target received %d requests, want 1", len(target.received))
	}
	got := target.received[0]
	if got.Name != "Rework auth" {
		t.Fatalf("name = %q; want trimmed %q", got.Name, "Rework auth")
	}
	if got.Pipeline != feature.PipelineLarge || got.RiskLevel != feature.RiskLow {
		t.Fatalf("pipeline/risk = %q/%q; want large/low", got.Pipeline, got.RiskLevel)
	}
	if got.Description != "split the auth package" || got.ExitCriteria != "build passes" {
		t.Fatalf("description/exit criteria = %q/%q", got.Description, got.ExitCriteria)
	}
	if len(got.Images) != 1 || len(got.Attachments) != 1 {
		t.Fatalf("images/attachments = %v/%v; want one each", got.Images, got.Attachments)
	}
	if !got.Checkpoints.InquiryReview {
		t.Fatalf("checkpoints = %+v; want inquiry_review", got.Checkpoints)
	}
	if got.Effort.Planning != "high" || got.Models.Planning != "opus" {
		t.Fatalf("effort/models planning = %q/%q; want high/opus", got.Effort.Planning, got.Models.Planning)
	}
	if got.Inquireness != feature.InquirenessMedium {
		t.Fatalf("inquireness = %q; want medium", got.Inquireness)
	}
}

// TestRefactorActionRejectsInvalidInquireness verifies the handler validates
// the inquiry behavior before the mutation target is invoked.
func TestRefactorActionRejectsInvalidInquireness(t *testing.T) {
	t.Parallel()

	target := &refactorMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		DisableHostValidation: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/features/feat-1/actions/refactor",
		bytes.NewReader([]byte(`{"name":"Rework auth","inquireness":"chatty"}`)))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", resp.StatusCode)
	}
	if len(target.received) != 0 {
		t.Fatalf("mutation target invoked with invalid inquireness")
	}
}

// TestRefactorActionRequiresName verifies the wizard brief must carry a
// child name; the mutation target is never invoked without one.
func TestRefactorActionRequiresName(t *testing.T) {
	t.Parallel()

	target := &refactorMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		DisableHostValidation: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/features/feat-1/actions/refactor", bytes.NewReader([]byte(`{"description":"no name"}`)))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", resp.StatusCode)
	}
	if len(target.received) != 0 {
		t.Fatalf("mutation target invoked without a name")
	}
}

// TestRefactorActionTypedErrorCodes pins the stable machine codes of every
// typed child-launch failure and of the child execution gate.
func TestRefactorActionTypedErrorCodes(t *testing.T) {
	t.Parallel()

	dirtyErr := &feature.ParentWorktreesDirtyError{Repos: []feature.RepoDirtyDiagnostics{{
		Repo:           repoNameSelf,
		Path:           testRepoPath,
		Staged:         []string{"staged.go"},
		Unstaged:       []string{"unstaged.go"},
		Untracked:      []string{"new.go"},
		StagedTotal:    1,
		UnstagedTotal:  1,
		UntrackedTotal: 1,
	}}}
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantTarget map[string]any
	}{
		{
			name:       "parent not found",
			err:        fmt.Errorf("%w: feat-1", feature.ErrRefactorParentNotFound),
			wantStatus: http.StatusNotFound,
			wantCode:   errCodeRefactorParentNotFound,
		},
		{
			name:       "parent is child",
			err:        fmt.Errorf("%w: feat-1", feature.ErrRefactorParentIsChild),
			wantStatus: http.StatusConflict,
			wantCode:   errCodeRefactorParentIsChild,
		},
		{
			name:       "parent status ineligible",
			err:        fmt.Errorf("%w: feat-1 is creating", feature.ErrRefactorParentStatusIneligible),
			wantStatus: http.StatusConflict,
			wantCode:   errCodeRefactorParentStatusIneligible,
		},
		{
			name:       "active child exists",
			err:        &feature.ActiveChildExistsError{ParentID: "feat-1", ChildID: "child-9"},
			wantStatus: http.StatusConflict,
			wantCode:   errCodeActiveChildExists,
			wantTarget: map[string]any{"parent_id": "feat-1", "child_id": "child-9"},
		},
		{
			name:       "parent worktrees dirty",
			err:        dirtyErr,
			wantStatus: http.StatusConflict,
			wantCode:   errCodeParentWorktreesDirty,
		},
		{
			name:       "child execution blocked",
			err:        fmt.Errorf("%w: child-9", feature.ErrChildExecutionBlocked),
			wantStatus: http.StatusConflict,
			wantCode:   errCodeChildExecutionBlocked,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			handler := NewHandler(HandlerOptions{
				Mutations:             &refactorMutationTarget{err: tc.err},
				DisableHostValidation: true,
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/features/feat-1/actions/refactor", bytes.NewReader([]byte(`{"name":"Rework auth"}`)))
			req.Header.Set("Content-Type", contentTypeJSON)
			req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			resp := w.Result()
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d; want %d", resp.StatusCode, tc.wantStatus)
			}
			var body ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != tc.wantCode {
				t.Fatalf("error code = %q; want %q", body.Error.Code, tc.wantCode)
			}
			if body.Error.Status != tc.wantStatus {
				t.Fatalf("error status = %d; want %d", body.Error.Status, tc.wantStatus)
			}
			for key, want := range tc.wantTarget {
				if got := body.Error.Target[key]; got != want {
					t.Fatalf("target[%q] = %v; want %v", key, got, want)
				}
			}
		})
	}
}

// TestRefactorActionDirtyErrorCarriesDiagnostics verifies the dirty-worktree
// rejection serializes the captured per-repository diagnostics into the error
// target payload.
func TestRefactorActionDirtyErrorCarriesDiagnostics(t *testing.T) {
	t.Parallel()

	target := &refactorMutationTarget{
		err: &feature.ParentWorktreesDirtyError{Repos: []feature.RepoDirtyDiagnostics{{
			Repo:           repoNameSelf,
			Path:           testRepoPath,
			Staged:         []string{"staged.go"},
			Unstaged:       []string{"unstaged.go"},
			Untracked:      []string{"new.go"},
			StagedTotal:    3,
			UnstagedTotal:  2,
			UntrackedTotal: 1,
		}}},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		DisableHostValidation: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/features/feat-1/actions/refactor", bytes.NewReader([]byte(`{"name":"Rework auth"}`)))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d; want 409", resp.StatusCode)
	}
	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != errCodeParentWorktreesDirty {
		t.Fatalf("error code = %q; want %q", body.Error.Code, errCodeParentWorktreesDirty)
	}
	repos, ok := body.Error.Target["repos"].([]any)
	if !ok || len(repos) != 1 {
		t.Fatalf("target.repos = %v; want one repository", body.Error.Target["repos"])
	}
	repo := repos[0].(map[string]any)
	if repo["repo"] != repoNameSelf || repo["path"] != testRepoPath {
		t.Fatalf("repo identity = %v/%v; want %s/%s", repo["repo"], repo["path"], repoNameSelf, testRepoPath)
	}
	if got := repo["staged_total"]; got != float64(3) {
		t.Fatalf("staged_total = %v; want 3", got)
	}
	if got := repo["staged"].([]any); len(got) != 1 || got[0] != "staged.go" {
		t.Fatalf("staged = %v; want [staged.go]", got)
	}
	if got := repo["untracked"].([]any); len(got) != 1 || got[0] != "new.go" {
		t.Fatalf("untracked = %v; want [new.go]", got)
	}
}

// TestStartResumeActionChildExecutionBlocked verifies the child
// execution capability gate surfaces through the start AND resume mutation
// routes as the stable 409 child_execution_blocked conflict (never a
// "started" success), regardless of the child's setup state.
func TestStartResumeActionChildExecutionBlocked(t *testing.T) {
	t.Parallel()

	for _, action := range []string{actionStart, actionResume} {
		for _, setupState := range []string{"queued", "setup-complete"} {
			t.Run(action+"/"+setupState, func(t *testing.T) {
				t.Parallel()
				target := &startBlockedMutationTarget{
					err: fmt.Errorf("%w: child-9", feature.ErrChildExecutionBlocked),
				}
				handler := NewHandler(HandlerOptions{
					Mutations:             target,
					DisableHostValidation: true,
				})
				req := httptest.NewRequest(http.MethodPost, "/api/v1/features/child-9/actions/"+action, bytes.NewReader([]byte(`{}`)))
				req.Header.Set("Content-Type", contentTypeJSON)
				req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
				w := httptest.NewRecorder()

				handler.ServeHTTP(w, req)

				resp := w.Result()
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusConflict {
					t.Fatalf("status = %d; want 409", resp.StatusCode)
				}
				var body ErrorResponse
				if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if body.Error.Code != errCodeChildExecutionBlocked {
					t.Fatalf("error code = %q; want %q", body.Error.Code, errCodeChildExecutionBlocked)
				}
			})
		}
	}
}

type startBlockedMutationTarget struct {
	MutationTarget
	err error
}

func (t *startBlockedMutationTarget) StartFeature(string) (FeatureStartResponse, error) {
	return FeatureStartResponse{}, t.err
}

func (t *startBlockedMutationTarget) ResumeFeature(string) (FeatureStartResponse, error) {
	return FeatureStartResponse{}, t.err
}

type deleteBlockedMutationTarget struct {
	MutationTarget
	err error
}

func (t *deleteBlockedMutationTarget) DeleteFeature(string, GuardedFeatureActionRequest) (DeleteFeatureResponse, error) {
	return DeleteFeatureResponse{}, t.err
}

type refactorMutationTarget struct {
	MutationTarget
	received []RefactorFeatureRequest
	resp     RefactorFeatureResponse
	err      error
}

func (t *refactorMutationTarget) RefactorFeature(featureID string, req RefactorFeatureRequest) (RefactorFeatureResponse, error) {
	t.received = append(t.received, req)
	return t.resp, t.err
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

// TestStartActionChildCapabilityErrors verifies the typed child
// capability rejections surface through the action route as distinct
// stable 409 machine codes carrying the feature/profile/repository
// context.
func TestStartActionChildCapabilityErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		err      error
		wantCode string
	}{
		{
			name:     "blocked child",
			err:      fmt.Errorf("%w: child-9", feature.ErrChildExecutionBlocked),
			wantCode: errCodeChildExecutionBlocked,
		},
		{
			name:     "settled closed child",
			err:      fmt.Errorf("%w: child-9", feature.ErrChildExecutionClosed),
			wantCode: errCodeChildRelationshipClosed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := &startBlockedMutationTarget{err: tc.err}
			handler := NewHandler(HandlerOptions{
				Mutations:             target,
				DisableHostValidation: true,
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/features/child-9/actions/start", bytes.NewReader([]byte(`{}`)))
			req.Header.Set("Content-Type", contentTypeJSON)
			req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			resp := w.Result()
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d; want 409", resp.StatusCode)
			}
			var body ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != tc.wantCode {
				t.Fatalf("error code = %q; want %q", body.Error.Code, tc.wantCode)
			}
		})
	}
}

func TestDeleteActionClosedChildReturnsRelationshipConflict(t *testing.T) {
	t.Parallel()

	handler := NewHandler(HandlerOptions{
		Mutations: &deleteBlockedMutationTarget{
			err: fmt.Errorf("%w: delete is not permitted", feature.ErrChildRelationshipClosed),
		},
		DisableHostValidation: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/features/child-9/actions/delete", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != errCodeChildRelationshipClosed {
		t.Fatalf("error code = %q, want %q", body.Error.Code, errCodeChildRelationshipClosed)
	}
}
