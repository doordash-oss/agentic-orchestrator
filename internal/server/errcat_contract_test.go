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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestCatalogActionReferencesAreValidFeatureActions pins the catalog's
// remediation action references to the generated feature-action enum: a typo
// in a referenced action ID can never reach the wire.
func TestCatalogActionReferencesAreValidFeatureActions(t *testing.T) {
	t.Parallel()

	invalid := 0
	for _, code := range errcat.Codes() {
		entry, ok := errcat.Lookup(code)
		if !ok {
			t.Fatalf("%s: listed by Codes but missing from catalog", code)
		}
		for _, action := range entry.Actions {
			if !FeatureAction(action).Valid() {
				t.Errorf("%s: action reference %q is not a generated FeatureAction value", code, action)
				invalid++
			}
		}
	}
	if invalid > 0 {
		t.Fatalf("%d invalid action references", invalid)
	}
}

// readinessIssueCodes are the codes the readiness projection renders as
// canonical errors on the wire. The generated readiness-issue enum is gone
// (the codes are catalog codes), so this set pins what the projection may
// emit.
var readinessIssueCodes = []errcat.Code{
	errcat.InvalidConfiguration,
	errcat.InvalidRepository,
	errcat.InvalidWorkspaceRoot,
	errcat.MissingExecutable,
	errcat.ModelsUnavailable,
	errcat.Unauthenticated,
	errcat.UnsupportedVersion,
}

// TestCatalogCoversReadinessIssueCodes pins every readiness issue code to a
// complete, blocking catalog entry so every projected issue renders authored
// title, summary, and remediation text.
func TestCatalogCoversReadinessIssueCodes(t *testing.T) {
	t.Parallel()

	for _, code := range readinessIssueCodes {
		entry, ok := errcat.Lookup(code)
		if !ok {
			t.Errorf("readiness issue code %q has no catalog entry", code)
			continue
		}
		if entry.Class != errcat.ClassBlocking {
			t.Errorf("readiness issue code %q class is %q; want blocking", code, entry.Class)
		}
		if entry.Title == "" || entry.Summary == "" {
			t.Errorf("readiness issue code %q must carry authored title and summary", code)
		}
	}
}

// TestRepresentativeRejectionsCarryCanonicalBodies decodes the canonical
// error body on a representative sample of endpoints: every non-2xx response
// carries a cataloged code, class, title, and summary in the shared envelope.
func TestRepresentativeRejectionsCarryCanonicalBodies(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := feature.NewStore(dir)
	handler := NewHandler(HandlerOptions{
		AuthToken:             testAuthToken,
		Features:              store,
		DisableHostValidation: true,
	})

	tests := []struct {
		name        string
		method      string
		path        string
		wantStatus  int
		wantCode    errcat.Code
		wantClass   errcat.Class
		wantTitle   string
		wantSummary string
	}{
		{
			name:        "unauthorized",
			method:      http.MethodGet,
			path:        apiPathFeatures,
			wantStatus:  http.StatusUnauthorized,
			wantCode:    errcat.Unauthorized,
			wantClass:   errcat.ClassBlocking,
			wantTitle:   "Unauthorized",
			wantSummary: "The request is missing a valid bearer token.",
		},
		{
			name:        "method not allowed",
			method:      http.MethodDelete,
			path:        apiPathFeatures,
			wantStatus:  http.StatusMethodNotAllowed,
			wantCode:    errcat.MethodNotAllowed,
			wantClass:   errcat.ClassBlocking,
			wantTitle:   "Method not allowed",
			wantSummary: "The HTTP method is not supported by this endpoint.",
		},
		{
			name:        "unknown route",
			method:      http.MethodGet,
			path:        "/api/v1/definitely-not-a-route",
			wantStatus:  http.StatusNotFound,
			wantCode:    errcat.NotFound,
			wantClass:   errcat.ClassBlocking,
			wantTitle:   "Not found",
			wantSummary: "Endpoint was not found.",
		},
		{
			name:        "invalid feature id",
			method:      http.MethodGet,
			path:        "/api/v1/features/" + strings.Repeat("..", 2) + "/bad",
			wantStatus:  http.StatusBadRequest,
			wantCode:    errcat.BadRequest,
			wantClass:   errcat.ClassBlocking,
			wantTitle:   "Bad request",
			wantSummary: "The request was not valid.",
		},
		{
			name:        "feature not found",
			method:      http.MethodGet,
			path:        apiPathFeatures + "/feat-404",
			wantStatus:  http.StatusNotFound,
			wantCode:    errcat.NotFound,
			wantClass:   errcat.ClassBlocking,
			wantTitle:   "Not found",
			wantSummary: `Feature "feat-404" was not found.`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.name != "unauthorized" {
				req.Header.Set("Authorization", "Bearer "+testAuthToken)
			}
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
			if body.APIVersion != APIVersion {
				t.Fatalf("api_version = %q; want %q", body.APIVersion, APIVersion)
			}
			if body.Error.Code != string(tc.wantCode) {
				t.Fatalf("code = %q; want %q", body.Error.Code, tc.wantCode)
			}
			if body.Error.Class != ErrorClass(tc.wantClass) {
				t.Fatalf("class = %q; want %q", body.Error.Class, tc.wantClass)
			}
			if body.Error.Title != tc.wantTitle || body.Error.Summary != tc.wantSummary {
				t.Fatalf("title/summary = %q/%q; want %q/%q", body.Error.Title, body.Error.Summary, tc.wantTitle, tc.wantSummary)
			}
		})
	}
}

// TestRebaseActionErrorFamilyCarriesRepositoryContext pins the rebase
// rejection family: already-up-to-date is a warning whose repositories block
// names each repo and target branch, and fetch/target-resolution failures
// name the failing repository in the block with the raw text in diagnostics.
func TestRebaseActionErrorFamilyCarriesRepositoryContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantCode   errcat.Code
		wantClass  errcat.Class
		wantRepo   string
		wantBranch string
	}{
		{
			name:       "already up to date",
			err:        &feature.RebaseAlreadyUpToDateError{Targets: []feature.RebaseRepoTarget{{Repo: "web", Target: "main", Ref: "origin/main", Publishable: true}}},
			wantCode:   errcat.RebaseAlreadyUpToDate,
			wantClass:  errcat.ClassWarning,
			wantRepo:   "web",
			wantBranch: "main",
		},
		{
			name:      "fetch failed",
			err:       &feature.RebaseFetchError{Repo: "web", Err: errors.New("dial tcp: refused")},
			wantCode:  errcat.RebaseFetchFailed,
			wantClass: errcat.ClassBlocking,
			wantRepo:  "web",
		},
		{
			name:      "target resolution failed",
			err:       &feature.RebaseTargetResolutionError{Repo: "web", Err: errors.New("no such branch")},
			wantCode:  errcat.RebaseTargetResolutionFailed,
			wantClass: errcat.ClassBlocking,
			wantRepo:  "web",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target := &preflightMutationTarget{rebaseFeatureErr: tc.err}
			handler := NewHandler(HandlerOptions{
				Mutations:             target,
				DisableHostValidation: true,
			})
			w := postTrustedAuthedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/rebase", map[string]any{})

			resp := w.Result()
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d; want 409", resp.StatusCode)
			}
			var body ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != string(tc.wantCode) {
				t.Fatalf("code = %q; want %q", body.Error.Code, tc.wantCode)
			}
			if body.Error.Class != ErrorClass(tc.wantClass) {
				t.Fatalf("class = %q; want %q", body.Error.Class, tc.wantClass)
			}
			if body.Error.Title == "" || body.Error.Summary == "" {
				t.Fatalf("title/summary must be catalog-rendered: %+v", body.Error)
			}
			if body.Error.Context == nil || len(body.Error.Context.Repositories) != 1 {
				t.Fatalf("context = %+v; want one repository block", body.Error.Context)
			}
			repo := body.Error.Context.Repositories[0]
			if repo.Name != tc.wantRepo || repo.Branch != tc.wantBranch {
				t.Fatalf("repository block = %+v; want name %q branch %q", repo, tc.wantRepo, tc.wantBranch)
			}
			if tc.name != "already up to date" && body.Error.Diagnostics == "" {
				t.Fatalf("raw failure text must stay in diagnostics: %+v", body.Error)
			}
		})
	}
}

// TestMutationFallbackErrors pins the classification floor: an error matching
// no sentinel decodes as cataloged bad_request with the raw text in
// diagnostics, and a nil error decodes as the fallback internal-error code.
func TestMutationFallbackErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   errcat.Code
		direct     bool
	}{
		{
			name:       "unmatched sentinel",
			err:        errors.New("boom: no sentinel matches"),
			wantStatus: http.StatusBadRequest,
			wantCode:   errcat.BadRequest,
		},
		{
			name:       "nil error",
			err:        nil,
			wantStatus: http.StatusInternalServerError,
			wantCode:   errcat.InternalError,
			direct:     true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.direct {
				// The nil path is the mapper's defensive floor: no route can
				// reach it, so exercise the mapper directly.
				w := httptest.NewRecorder()
				writeMutationError(w, tc.err)
				resp := w.Result()
				defer resp.Body.Close()
				if resp.StatusCode != tc.wantStatus {
					t.Fatalf("status = %d; want %d", resp.StatusCode, tc.wantStatus)
				}
				var body ErrorResponse
				if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if body.Error.Code != string(tc.wantCode) || body.Error.Class != ErrorClass(errcat.ClassBlocking) {
					t.Fatalf("error = %+v; want %q blocking", body.Error, tc.wantCode)
				}
				if body.Error.Title == "" || body.Error.Summary == "" {
					t.Fatalf("title/summary must be catalog-rendered: %+v", body.Error)
				}
				return
			}
			target := &refactorMutationTarget{err: tc.err}
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
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d; want %d", resp.StatusCode, tc.wantStatus)
			}
			var body ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != string(tc.wantCode) {
				t.Fatalf("code = %q; want %q", body.Error.Code, tc.wantCode)
			}
			if body.Error.Class != ErrorClass(errcat.ClassBlocking) {
				t.Fatalf("class = %q; want blocking", body.Error.Class)
			}
			if body.Error.Title == "" || body.Error.Summary == "" {
				t.Fatalf("title/summary must be catalog-rendered: %+v", body.Error)
			}
			if tc.err != nil && body.Error.Diagnostics != tc.err.Error() {
				t.Fatalf("diagnostics = %q; want raw text %q", body.Error.Diagnostics, tc.err.Error())
			}
		})
	}
}
