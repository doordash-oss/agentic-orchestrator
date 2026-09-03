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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

const repoStatusPRCreateDiagnostics = "creating pull request: POST /repos/org/repo-a/pulls: 502 Bad Gateway " +
	"with a diagnostics tail well past the safe-display bound so the projection must bound it " +
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TestFeatureDetailRepoStatusCarriesCanonicalError pins the repository-status
// projection: a repository carrying a stored publish-failure record renders
// it through the catalog as the canonical error object — needs_action class,
// catalog title and summary naming the repository, the repositories block,
// the publish action reference, and bounded raw diagnostics — with no
// last_error key anywhere in the body.
func TestFeatureDetailRepoStatusCarriesCanonicalError(t *testing.T) {
	t.Parallel()
	store := feature.NewStore(t.TempDir())
	f := &feature.Feature{
		ID:            "feat-repo-error",
		Name:          "Repo Publish Failed",
		Slug:          "repo-publish-failed",
		Status:        feature.StatusCodeReady,
		CurrentPhase:  feature.PhasePublish,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         []feature.FeatureRepo{{Name: "repo-a", Branch: "agentico/my-feature"}},
	}
	f.RepoStates = map[string]*feature.RepoState{
		"repo-a": {
			Touched: true,
			Error: &errcat.FailureRecord{
				Code: errcat.PublishPullRequestFailed,
				Context: &errcat.RecordContext{
					Repositories: []errcat.CodeRepository{{Name: "repo-a", Branch: "agentico/my-feature"}},
				},
				Diagnostics: repoStatusPRCreateDiagnostics,
			},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save feature: %v", err)
	}
	handler := NewHandler(HandlerOptions{Features: store, DisableHostValidation: true})

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	if path := findJSONKey(detail, "last_error"); path != "" {
		t.Fatalf("feature detail carries a last_error key at %s; repository text is gone", path)
	}
	featureBody := detail[entityFeature].(map[string]any)
	repos, ok := featureBody["repo_status"].([]any)
	if !ok || len(repos) != 1 {
		t.Fatalf("repo_status = %#v, want one repository", featureBody["repo_status"])
	}
	repo := repos[0].(map[string]any)
	if repo["name"] != "repo-a" || repo["pr_url"] != nil {
		t.Fatalf("repo status = %#v, want repo-a with no pull request", repo)
	}

	repoError, ok := repo["error"].(map[string]any)
	if !ok {
		t.Fatalf("repo status error = %#v, want canonical error object", repo["error"])
	}
	if repoError["code"] != string(errcat.PublishPullRequestFailed) {
		t.Fatalf("repo error code = %v, want %q", repoError["code"], errcat.PublishPullRequestFailed)
	}
	if repoError["class"] != "needs_action" {
		t.Fatalf("repo error class = %v, want needs_action", repoError["class"])
	}
	if repoError["title"] != "Pull-request creation failed" {
		t.Fatalf("repo error title = %v, want catalog title", repoError["title"])
	}
	if repoError["summary"] != `Creating the pull request for repository "repo-a" failed.` {
		t.Fatalf("repo error summary = %v, want catalog summary naming the repository", repoError["summary"])
	}
	errorContext, ok := repoError["context"].(map[string]any)
	if !ok {
		t.Fatalf("repo error context = %#v, want context blocks", repoError["context"])
	}
	contextRepos, ok := errorContext["repositories"].([]any)
	if !ok || len(contextRepos) != 1 {
		t.Fatalf("repo error repositories = %#v, want one repository", errorContext["repositories"])
	}
	contextRepo := contextRepos[0].(map[string]any)
	if contextRepo["name"] != "repo-a" || contextRepo["branch"] != "agentico/my-feature" {
		t.Fatalf("repo error repository = %#v, want repo-a on its feature branch", contextRepo)
	}
	remediation, ok := repoError["remediation"].(map[string]any)
	if !ok {
		t.Fatalf("repo error remediation = %#v, want remediation block", repoError["remediation"])
	}
	actions, ok := remediation["actions"].([]any)
	if !ok || len(actions) != 1 || actions[0] != "publish" {
		t.Fatalf("repo error remediation actions = %#v, want [publish]", remediation["actions"])
	}
	diagnostics, _ := repoError["diagnostics"].(string)
	if len(diagnostics) == 0 || len(diagnostics) >= len(repoStatusPRCreateDiagnostics) {
		t.Fatalf("repo error diagnostics = %q (len %d), want the bounded raw text", diagnostics, len(diagnostics))
	}
}

// TestFeatureDetailRepoStatusOmitsErrorWithoutRecord pins the projection for
// a healthy repository: no error object on the row.
func TestFeatureDetailRepoStatusOmitsErrorWithoutRecord(t *testing.T) {
	t.Parallel()
	store := feature.NewStore(t.TempDir())
	f := &feature.Feature{
		ID:            "feat-repo-clean",
		Name:          "Repo Publish Clean",
		Slug:          "repo-publish-clean",
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         []feature.FeatureRepo{{Name: "repo-a", Branch: "agentico/my-feature"}},
	}
	f.RepoStates = map[string]*feature.RepoState{
		"repo-a": {Touched: true, PRURL: "https://github.example/org/repo-a/pull/1"},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save feature: %v", err)
	}
	handler := NewHandler(HandlerOptions{Features: store, DisableHostValidation: true})

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	featureBody := detail[entityFeature].(map[string]any)
	repos, ok := featureBody["repo_status"].([]any)
	if !ok || len(repos) != 1 {
		t.Fatalf("repo_status = %#v, want one repository", featureBody["repo_status"])
	}
	repo := repos[0].(map[string]any)
	if repo["error"] != nil {
		t.Fatalf("repo status error = %#v, want none for a healthy repository", repo["error"])
	}
}
