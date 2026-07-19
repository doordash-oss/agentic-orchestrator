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
	"net/http"
	"net/http/httptest"
	"testing"
)

// preflightMutationTarget is a test double that records preflight and
// cycle-start calls so the handler tests can assert routing, stale-preflight
// rejection, and response shape without a live orchestrator.
type preflightMutationTarget struct {
	MutationTarget
	rebasePreflight        RebasePreflightResponse
	rebasePreflightErr     error
	rebasePreflightID      string
	refactorPreflight      RefactorPreflightResponse
	refactorPreflightErr   error
	refactorPreflightID    string
	refactorReq            RefactorPreflightRequest
	startRebaseCalls       []RebaseActionRequest
	startRebaseErr         error
	startRefactorCalls     []RefactorActionRequest
	startRefactorErr       error
	completionPreflight    CompletionPreflightResponse
	completionPreflightErr error
	completionPreflightID  string
	repoDiff               RepositoryDiffResponse
	repoDiffErr            error
	repoDiffID             string
	repoDiffName           string
	repoDiffFilePath       string
	publishReq             PublishFeatureRequest
	publishDescReq         PublishDescriptionRequest
	mergeReq               GuardedFeatureActionRequest
	markDoneReq            GuardedFeatureActionRequest
	cleanupReq             CleanupActionRequest
	deleteReq              GuardedFeatureActionRequest
}

func (t *preflightMutationTarget) PreflightRebase(featureID string) (RebasePreflightResponse, error) {
	t.rebasePreflightID = featureID
	if t.rebasePreflightErr != nil {
		return RebasePreflightResponse{}, t.rebasePreflightErr
	}
	return t.rebasePreflight, nil
}

func (t *preflightMutationTarget) PreflightRefactor(featureID string, req RefactorPreflightRequest) (RefactorPreflightResponse, error) {
	t.refactorPreflightID = featureID
	t.refactorReq = req
	if t.refactorPreflightErr != nil {
		return RefactorPreflightResponse{}, t.refactorPreflightErr
	}
	return t.refactorPreflight, nil
}

func (t *preflightMutationTarget) StartRebase(featureID string, req RebaseActionRequest) (RebaseStartResponse, error) {
	t.startRebaseCalls = append(t.startRebaseCalls, req)
	if t.startRebaseErr != nil {
		return RebaseStartResponse{FeatureID: featureID, CycleType: "rebase", Result: "failed"}, t.startRebaseErr
	}
	return RebaseStartResponse{FeatureID: featureID, CycleType: "rebase", Result: "started"}, nil
}

func (t *preflightMutationTarget) StartRefactor(featureID string, req RefactorActionRequest) (RefactorStartResponse, error) {
	t.startRefactorCalls = append(t.startRefactorCalls, req)
	if t.startRefactorErr != nil {
		return RefactorStartResponse{FeatureID: featureID, CycleType: "refactor", Result: "failed"}, t.startRefactorErr
	}
	return RefactorStartResponse{FeatureID: featureID, CycleType: "refactor", Result: "started"}, nil
}

func authedGet(handler http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testAuthToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func authedPostJSON(handler http.Handler, path string, body any) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("Authorization", "Bearer "+testAuthToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// postTrustedAuthedJSON sets both the bearer token and the trusted mutation
// header, for mutation routes under an authenticated handler.
func postTrustedAuthedJSON(handler http.Handler, path string, body any) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("Authorization", "Bearer "+testAuthToken)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestRebasePreflightReturnsServerAuthoredRepoImpact(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{
		rebasePreflight: RebasePreflightResponse{
			FeatureID:      fixtureFeatureID,
			SourceRevision: "rev-abc",
			Repos: []RebasePreflightRepo{
				{Repo: "repo-a", Target: "main", Publishable: true, Freshness: "behind", Behind: true},
				{Repo: "repo-b", Target: "main", Publishable: true, Freshness: "up_to_date", Behind: false, Blocker: ""},
			},
		},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})

	w := authedGet(handler, "/api/v1/features/"+fixtureFeatureID+"/rebase/preflight")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp RebasePreflightResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SourceRevision != "rev-abc" {
		t.Fatalf("source_revision = %q; want rev-abc", resp.SourceRevision)
	}
	if len(resp.Repos) != 2 || resp.Repos[0].Repo != "repo-a" || !resp.Repos[0].Behind {
		t.Fatalf("repos = %+v; want repo-a behind + repo-b up to date", resp.Repos)
	}
	if target.rebasePreflightID != fixtureFeatureID {
		t.Fatalf("preflight called with %q; want %s", target.rebasePreflightID, fixtureFeatureID)
	}
}

func TestRebasePreflightRejectsWrongMethod(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/features/"+fixtureFeatureID+"/rebase/preflight", nil)
	req.Header.Set("Authorization", "Bearer "+testAuthToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

func TestRefactorPreflightResolvesAllScope(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{
		refactorPreflight: RefactorPreflightResponse{
			FeatureID:      fixtureFeatureID,
			SourceRevision: "rev-xyz",
			Scope:          "all",
			Repos:          []string{"repo-a", "repo-b"},
			Prompt:         "rename foo to bar",
		},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})

	w := authedPostJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/refactor/preflight", map[string]any{
		"prompt": "rename foo to bar",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp RefactorPreflightResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Scope != "all" || len(resp.Repos) != 2 {
		t.Fatalf("scope/repos = %+v; want all with 2 repos", resp)
	}
	if target.refactorReq.Prompt != "rename foo to bar" {
		t.Fatalf("prompt = %q; want forwarded", target.refactorReq.Prompt)
	}
}

func TestRefactorPreflightRejectsEmptyPrompt(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	w := authedPostJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/refactor/preflight", map[string]any{
		"prompt": "   ",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
}

func TestRebaseStartRejectsStalePreflight(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{
		startRebaseErr: &ActionConflictError{Message: "stale rebase preflight"},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	w := postTrustedAuthedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/rebase", map[string]any{
		"source_revision": "rev-old",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s; want 409", w.Code, w.Body.String())
	}
	if len(target.startRebaseCalls) != 1 || target.startRebaseCalls[0].SourceRevision != "rev-old" {
		t.Fatalf("start calls = %+v; want one carrying source_revision rev-old", target.startRebaseCalls)
	}
}

func TestRefactorStartPassesThroughSourceRevision(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	w := postTrustedAuthedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/refactor", map[string]any{
		"prompt":          "rename foo to bar",
		"repo":            "repo-a",
		"source_revision": "rev-xyz",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(target.startRefactorCalls) != 1 || target.startRefactorCalls[0].SourceRevision != "rev-xyz" {
		t.Fatalf("start calls = %+v; want one carrying source_revision rev-xyz", target.startRefactorCalls)
	}
}
