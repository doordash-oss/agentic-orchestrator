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

// preflightMutationTarget is a test double that records preflight and action
// calls so the handler tests can assert routing and response shape without a
// live orchestrator.
type preflightMutationTarget struct {
	MutationTarget
	rebaseFeatureID        string
	rebaseFeatureErr       error
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

func (t *preflightMutationTarget) RebaseFeature(featureID string, _ RebaseFeatureRequest) (RebaseFeatureResponse, error) {
	t.rebaseFeatureID = featureID
	if t.rebaseFeatureErr != nil {
		return RebaseFeatureResponse{ParentID: featureID, Result: "failed"}, t.rebaseFeatureErr
	}
	return RebaseFeatureResponse{FeatureID: "rebase-child", ParentID: featureID, Result: "created"}, nil
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

func TestRebaseActionAcceptsLegacyBody(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	w := postTrustedAuthedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/rebase", map[string]any{
		"source_revision": "rev-old",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	if target.rebaseFeatureID != fixtureFeatureID {
		t.Fatalf("RebaseFeature called with %q; want %s", target.rebaseFeatureID, fixtureFeatureID)
	}
}
