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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

type codedPublishConflictTarget struct {
	*preflightMutationTarget
	err error
}

func (t *codedPublishConflictTarget) PublishFeature(featureID string, req PublishFeatureRequest) (PublishFeatureResponse, error) {
	t.publishReq = req
	return PublishFeatureResponse{}, t.err
}

func (t *preflightMutationTarget) CompletionPreflight(featureID string) (CompletionPreflightResponse, error) {
	if t.completionPreflightErr != nil {
		return CompletionPreflightResponse{}, t.completionPreflightErr
	}
	return CompletionPreflightResponse{FeatureID: featureID}, nil
}

func (t *preflightMutationTarget) RepositoryDiff(featureID, repoName, filePath string) (RepositoryDiffResponse, error) {
	t.repoDiffFilePath = filePath
	if t.repoDiffErr != nil {
		return RepositoryDiffResponse{}, t.repoDiffErr
	}
	return t.repoDiff, nil
}

func (t *preflightMutationTarget) RepositoryPath(featureID, repoName string) (RepositoryPathResponse, error) {
	return RepositoryPathResponse{FeatureID: featureID, Repo: repoName, Path: "/tmp/repo-a"}, nil
}

func (t *preflightMutationTarget) PublishFeature(featureID string, req PublishFeatureRequest) (PublishFeatureResponse, error) {
	t.publishReq = req
	return PublishFeatureResponse{FeatureID: featureID, Result: "published"}, nil
}

func (t *preflightMutationTarget) CleanupFeature(featureID string, req CleanupActionRequest) (CleanupFeatureResponse, error) {
	t.cleanupReq = req
	return CleanupFeatureResponse{FeatureID: featureID, Result: "cleaned", Target: req.Target}, nil
}

func TestPublishActionReturnsCodedRemoteSafetyConflicts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		code        errcat.Code
		detail      string
		options     []errcat.Option
		wantSummary string
		wantRepo    string
		wantBranch  string
	}{
		{
			name:   "remote diverged",
			code:   errcat.PublishRemoteDiverged,
			detail: "pull-request branch contains remote work that is not in this workspace",
			options: []errcat.Option{
				errcat.WithParams(errcat.PublishRepoParams{
					Repo: "repo-a", Branch: "feature/remote-diverged", RemoteOnlyCommits: 2,
				}),
				errcat.WithRepositories(errcat.CodeRepository{Name: "repo-a", Branch: "feature/remote-diverged"}),
			},
			wantSummary: `The pull-request branch for "repo-a" contains 2 remote commits that are not in this workspace.`,
			wantRepo:    "repo-a",
			wantBranch:  "feature/remote-diverged",
		},
		{
			name:   "remote changed",
			code:   errcat.PublishRemoteChanged,
			detail: "pull-request branch changed while Agentico was publishing",
			options: []errcat.Option{
				errcat.WithParams(errcat.PublishRepoParams{Repo: "repo-a", Branch: "feature/remote-changed"}),
				errcat.WithRepositories(errcat.CodeRepository{Name: "repo-a", Branch: "feature/remote-changed"}),
			},
			wantSummary: `The pull-request branch for "repo-a" changed while Agentico was publishing.`,
			wantRepo:    "repo-a",
			wantBranch:  "feature/remote-changed",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := &codedPublishConflictTarget{
				preflightMutationTarget: &preflightMutationTarget{},
				err: &ActionConflictError{
					Code:    tc.code,
					Detail:  tc.detail,
					Options: tc.options,
				},
			}
			handler := NewHandler(HandlerOptions{
				Mutations:             target,
				AuthToken:             testAuthToken,
				DisableHostValidation: true,
			})

			recorder := postTrustedAuthedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/"+actionPublish, map[string]any{
				"repos": []string{"repo-a"},
			})
			var body ErrorResponse
			if err := json.NewDecoder(recorder.Result().Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if got := body.Error.Code; got != string(tc.code) {
				t.Fatalf("error code = %q; want %q", got, tc.code)
			}
			if got := recorder.Code; got != http.StatusConflict {
				t.Fatalf("status = %d; want %d", got, http.StatusConflict)
			}
			if body.Error.Class != ErrorClass(errcat.ClassNeedsAction) {
				t.Fatalf("error class = %q; want %q", body.Error.Class, errcat.ClassNeedsAction)
			}
			if body.Error.Summary != tc.wantSummary {
				t.Fatalf("error summary = %q; want %q", body.Error.Summary, tc.wantSummary)
			}
			if body.Error.Diagnostics != tc.detail {
				t.Fatalf("error diagnostics = %q; want %q", body.Error.Diagnostics, tc.detail)
			}
			if body.Error.Context == nil || len(body.Error.Context.Repositories) != 1 {
				t.Fatalf("error context = %+v; want one repository", body.Error.Context)
			}
			repo := body.Error.Context.Repositories[0]
			if repo.Name != tc.wantRepo || repo.Branch != tc.wantBranch {
				t.Fatalf("error context repository = %+v; want %s@%s", repo, tc.wantRepo, tc.wantBranch)
			}
		})
	}
}

func TestCleanupActionRejectsCycleTarget(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})

	w := postTrustedAuthedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/"+actionCleanup, map[string]any{
		"target": "cycles",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	if target.cleanupReq.Target != "" {
		t.Fatalf("cleanup request reached mutation target = %+v; want validation rejection", target.cleanupReq)
	}
	var resp ErrorResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error.Code != string(errcat.BadRequest) || resp.Error.Diagnostics != "cleanup target is invalid" {
		t.Fatalf("error = %+v; want bad_request cleanup target is invalid", resp.Error)
	}
}

func TestCompletionPreflightRejectsWrongMethod(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/features/"+fixtureFeatureID+"/completion/preflight", nil)
	req.Header.Set("Authorization", "Bearer "+testAuthToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

func TestRepositoryDiffFetchesSingleFileContent(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{
		repoDiff: RepositoryDiffResponse{
			FeatureID:     fixtureFeatureID,
			Repo:          "repo-a",
			FileDiff:      "diff --git a/src/foo.go b/src/foo.go\n@@ -1,3 +1,4 @@\n+new line",
			FileTruncated: false,
		},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})

	w := authedGet(handler, "/api/v1/features/"+fixtureFeatureID+"/repositories/repo-a/diff?file_path=src/foo.go")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp RepositoryDiffResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.FileDiff == "" {
		t.Fatalf("file_diff is empty; want content")
	}
	if resp.Files == nil {
		t.Fatalf("files = nil; want empty array for single-file responses")
	}
	if len(resp.Files) != 0 {
		t.Fatalf("files len = %d; want 0 for single-file response", len(resp.Files))
	}
	if target.repoDiffFilePath != "src/foo.go" {
		t.Fatalf("file_path = %q; want src/foo.go", target.repoDiffFilePath)
	}
}

func TestRepositoryDiffRejectsTraversalPath(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})

	w := authedGet(handler, "/api/v1/features/"+fixtureFeatureID+"/repositories/repo-a/diff?file_path=../../etc/passwd")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 for traversal", w.Code)
	}
}

func TestRepositoryDiffRejectsInvalidRepoName(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})

	w := authedGet(handler, "/api/v1/features/"+fixtureFeatureID+"/repositories/../foo/diff")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 for invalid repo name", w.Code)
	}
}

func TestRepositoryDiffRejectsWrongMethod(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/features/"+fixtureFeatureID+"/repositories/repo-a/diff", nil)
	req.Header.Set("Authorization", "Bearer "+testAuthToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

func TestRepositoryDiffReportsPartialFailure(t *testing.T) {
	t.Parallel()
	rendered := wireError(errcat.New(
		errcat.RepositoryWorktreeUnavailable,
		errcat.WithRepositories(errcat.CodeRepository{Name: "repo-x"}),
		errcat.WithParams(errcat.WarningRepoParams{
			Repositories: []errcat.CodeRepository{{Name: "repo-x"}},
		}),
	))
	target := &preflightMutationTarget{
		repoDiff: RepositoryDiffResponse{
			FeatureID: fixtureFeatureID,
			Repo:      "repo-x",
			Error:     &rendered,
		},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})

	w := authedGet(handler, "/api/v1/features/"+fixtureFeatureID+"/repositories/repo-x/diff")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 for partial failure", w.Code)
	}
	body, err := io.ReadAll(w.Result().Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), "partial_failure") {
		t.Fatalf("body still carries a partial_failure field: %s", body)
	}
	var resp RepositoryDiffResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != string(errcat.RepositoryWorktreeUnavailable) {
		t.Fatalf("error = %+v; want repository_worktree_unavailable", resp.Error)
	}
	if resp.Error.Class != ErrorClass(errcat.ClassWarning) {
		t.Fatalf("error class = %q; want warning", resp.Error.Class)
	}
	if resp.Error.Context == nil || len(resp.Error.Context.Repositories) != 1 ||
		resp.Error.Context.Repositories[0].Name != "repo-x" {
		t.Fatalf("error repositories block = %+v; want repo-x", resp.Error.Context)
	}
}

func TestRepositoryDiffUnknownRepoReturnsNotFoundEnvelope(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{
		repoDiffErr: fmt.Errorf("repository %q: %w", "repo-x", feature.ErrRepositoryNotFound),
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})

	w := authedGet(handler, "/api/v1/features/"+fixtureFeatureID+"/repositories/repo-x/diff")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 for unknown repository", w.Code)
	}
	var resp ErrorResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error.Code != string(errcat.NotFound) {
		t.Fatalf("error code = %q; want not_found", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Summary, "repo-x") {
		t.Fatalf("summary = %q; want it to name the repository", resp.Error.Summary)
	}
}
