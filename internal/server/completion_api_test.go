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
	"net/http"
	"net/http/httptest"
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
	return PublishFeatureResponse{FeatureID: featureID, Result: "conflict"}, t.err
}

func (t *preflightMutationTarget) CompletionPreflight(featureID string) (CompletionPreflightResponse, error) {
	t.completionPreflightID = featureID
	if t.completionPreflightErr != nil {
		return CompletionPreflightResponse{}, t.completionPreflightErr
	}
	return t.completionPreflight, nil
}

func (t *preflightMutationTarget) RepositoryDiff(featureID, repoName, filePath string) (RepositoryDiffResponse, error) {
	t.repoDiffID = featureID
	t.repoDiffName = repoName
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

func (t *preflightMutationTarget) GeneratePublishDescription(featureID string, req PublishDescriptionRequest) (PublishDescriptionResponse, error) {
	t.publishDescReq = req
	return PublishDescriptionResponse{FeatureID: featureID, Title: "Generated title", Body: "Generated body", Result: "generated"}, nil
}

func (t *preflightMutationTarget) MergeFeature(featureID string, req GuardedFeatureActionRequest) (MergeFeatureResponse, error) {
	t.mergeReq = req
	return MergeFeatureResponse{FeatureID: featureID, Result: "merged"}, nil
}

func (t *preflightMutationTarget) MarkDone(featureID string, req GuardedFeatureActionRequest) (MarkDoneResponse, error) {
	t.markDoneReq = req
	return MarkDoneResponse{FeatureID: featureID, Result: "done"}, nil
}

func (t *preflightMutationTarget) CleanupFeature(featureID string, req CleanupActionRequest) (CleanupFeatureResponse, error) {
	t.cleanupReq = req
	return CleanupFeatureResponse{FeatureID: featureID, Result: "cleaned", Target: req.Target}, nil
}

func (t *preflightMutationTarget) DeleteFeature(featureID string, req GuardedFeatureActionRequest) (DeleteFeatureResponse, error) {
	t.deleteReq = req
	return DeleteFeatureResponse{FeatureID: featureID, Status: feature.CascadeDeleteCompleted}, nil
}

func TestCompletionPreflightReturnsEligibleRepos(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{
		completionPreflight: CompletionPreflightResponse{
			FeatureID:      fixtureFeatureID,
			SourceRevision: "rev-comp-abc",
			CanMarkDone:    true,
			Repos: []CompletionPreflightRepo{
				{Repo: "repo-a", Publishable: true, Touched: true, Status: "eligible"},
				{Repo: "repo-b", Publishable: true, Touched: true, Status: "already_published", PrURL: "https://example.com/pr/1"},
				{Repo: "repo-c", Publishable: false, Touched: false, Status: "ineligible"},
			},
		},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})

	w := authedGet(handler, "/api/v1/features/"+fixtureFeatureID+"/completion/preflight")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp CompletionPreflightResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SourceRevision != "rev-comp-abc" {
		t.Fatalf("source_revision = %q; want rev-comp-abc", resp.SourceRevision)
	}
	if !resp.CanMarkDone {
		t.Fatalf("can_mark_done = false; want true")
	}
	if len(resp.Repos) != 3 {
		t.Fatalf("repos len = %d; want 3", len(resp.Repos))
	}
	if resp.Repos[1].PrURL != "https://example.com/pr/1" {
		t.Fatalf("repo-b pr_url = %q; want https://example.com/pr/1", resp.Repos[1].PrURL)
	}
	if target.completionPreflightID != fixtureFeatureID {
		t.Fatalf("preflight called with %q; want %s", target.completionPreflightID, fixtureFeatureID)
	}
}

func TestCompletionActionsPassThroughSourceRevision(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		action string
		body   map[string]any
		check  func(*testing.T, *preflightMutationTarget)
	}{
		{
			name:   "publish",
			action: actionPublish,
			body: map[string]any{
				"source_revision": "rev-publish",
				"repos":           []string{"repo-a"},
				"title":           "Publish completion",
			},
			check: func(t *testing.T, target *preflightMutationTarget) {
				t.Helper()
				if target.publishReq.SourceRevision != "rev-publish" {
					t.Fatalf("publish source_revision = %q; want rev-publish", target.publishReq.SourceRevision)
				}
			},
		},
		{
			name:   "merge",
			action: actionMerge,
			body:   map[string]any{"source_revision": "rev-merge"},
			check: func(t *testing.T, target *preflightMutationTarget) {
				t.Helper()
				if target.mergeReq.SourceRevision != "rev-merge" {
					t.Fatalf("merge source_revision = %q; want rev-merge", target.mergeReq.SourceRevision)
				}
			},
		},
		{
			name:   "mark done",
			action: actionMarkDone,
			body:   map[string]any{"source_revision": "rev-done"},
			check: func(t *testing.T, target *preflightMutationTarget) {
				t.Helper()
				if target.markDoneReq.SourceRevision != "rev-done" {
					t.Fatalf("mark-done source_revision = %q; want rev-done", target.markDoneReq.SourceRevision)
				}
			},
		},
		{
			name:   "cleanup",
			action: actionCleanup,
			body:   map[string]any{"source_revision": "rev-clean", "target": "worktrees"},
			check: func(t *testing.T, target *preflightMutationTarget) {
				t.Helper()
				if target.cleanupReq.SourceRevision != "rev-clean" || target.cleanupReq.Target != "worktrees" {
					t.Fatalf("cleanup request = %+v; want source_revision rev-clean target worktrees", target.cleanupReq)
				}
			},
		},
		{
			name:   "delete",
			action: actionDelete,
			body:   map[string]any{"source_revision": "rev-delete"},
			check: func(t *testing.T, target *preflightMutationTarget) {
				t.Helper()
				if target.deleteReq.SourceRevision != "rev-delete" {
					t.Fatalf("delete source_revision = %q; want rev-delete", target.deleteReq.SourceRevision)
				}
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := &preflightMutationTarget{}
			handler := NewHandler(HandlerOptions{
				Mutations:             target,
				AuthToken:             testAuthToken,
				DisableHostValidation: true,
			})

			w := postTrustedAuthedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/"+tc.action, tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
			}
			tc.check(t, target)
		})
	}
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
			if body.Error.Class != ErrorClass(errcat.ClassBlocking) {
				t.Fatalf("error class = %q; want %q", body.Error.Class, errcat.ClassBlocking)
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

func TestPublishDescriptionPassesOnlySelectedRepos(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	w := postTrustedAuthedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/publish/description", map[string]any{
		"repos": []string{"repo-a"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp PublishDescriptionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Title != "Generated title" || resp.Body != "Generated body" {
		t.Fatalf("response = %+v, want generated narrative", resp)
	}
	if len(target.publishDescReq.Repos) != 1 || target.publishDescReq.Repos[0] != "repo-a" {
		t.Fatalf("publish description request = %+v, want selected repo only", target.publishDescReq)
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

func TestRepositoryDiffListsChangedFiles(t *testing.T) {
	t.Parallel()
	target := &preflightMutationTarget{
		repoDiff: RepositoryDiffResponse{
			FeatureID:      fixtureFeatureID,
			Repo:           "repo-a",
			SourceRevision: "rev-diff-1",
			Files: []RepositoryDiffFile{
				{Path: "src/foo.go", Operation: "modify", AddedLines: 10, RemovedLines: 2, Fingerprint: "abc123"},
				{Path: "src/bar.go", Operation: "add", AddedLines: 50, Fingerprint: "def456"},
				{Path: "README.md", Operation: "delete", RemovedLines: 5, Fingerprint: "ghi789"},
			},
		},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})

	w := authedGet(handler, "/api/v1/features/"+fixtureFeatureID+"/repositories/repo-a/diff")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp RepositoryDiffResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Repo != "repo-a" {
		t.Fatalf("repo = %q; want repo-a", resp.Repo)
	}
	if len(resp.Files) != 3 {
		t.Fatalf("files len = %d; want 3", len(resp.Files))
	}
	if resp.Files[0].Operation != "modify" {
		t.Fatalf("files[0].operation = %q; want modify", resp.Files[0].Operation)
	}
	if target.repoDiffID != fixtureFeatureID || target.repoDiffName != "repo-a" || target.repoDiffFilePath != "" {
		t.Fatalf("diff called with id=%q repo=%q file=%q; want %s/repo-a/empty", target.repoDiffID, target.repoDiffName, target.repoDiffFilePath, fixtureFeatureID)
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
	target := &preflightMutationTarget{
		repoDiff: RepositoryDiffResponse{
			FeatureID:      fixtureFeatureID,
			Repo:           "repo-x",
			PartialFailure: "worktree not available",
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
	var resp RepositoryDiffResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PartialFailure != "worktree not available" {
		t.Fatalf("partial_failure = %q; want 'worktree not available'", resp.PartialFailure)
	}
}
