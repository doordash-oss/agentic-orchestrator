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

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	gitpkg "github.com/doordash-oss/agentic-orchestrator/internal/git"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// These regressions pin the Phase 7 origin-check contract at the
// source-acceptance boundary: an origin check is evidence about its recorded
// SHAs and never a transaction against feature creation. A behind, failed, or
// superseded check must not move the feature start off the accepted local
// commit, and submission must never fetch.

func TestAcceptedLocalStartSurvivesBehindOriginCheck(t *testing.T) {
	runtimeDir := t.TempDir()
	repoPath, bare := originAcceptanceRemoteFixture(t, runtimeDir, testRepoAName)
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
	store, manager, handler := newOriginCheckAcceptanceRuntime(t, cfg, runtimeDir)

	row := awaitOriginStatusRow(t, handler, originStatusRequest(t, testRepoAName, repoPath))
	if row.Status != serverruntime.RepositoryOriginStatusStatusBehind {
		t.Fatalf("origin check status = %q; want behind", row.Status)
	}
	remoteSha := mutationGitOutput(t, bare, "rev-parse", "refs/heads/main")
	if row.FetchedSha != remoteSha {
		t.Fatalf("fetched sha = %q; want remote main %q", row.FetchedSha, remoteSha)
	}
	if row.BehindCount == nil || *row.BehindCount != 2 {
		t.Fatalf("behind count = %v; want 2", row.BehindCount)
	}

	expected := mutationRepositorySource(t, testRepoAName, repoPath, gitpkg.LocalSourceModeDefault)
	if expected.ObservedSha == remoteSha {
		t.Fatal("fixture did not place the local source behind origin")
	}
	rec := postCreateFeatureREST(t, handler, serverruntime.CreateFeatureRequest{
		Name:              "Continue behind origin",
		Repos:             []string{testRepoAName},
		RepositorySources: []serverruntime.RepositorySource{expected},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/features status = %d, want %d; body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var response serverruntime.CreateFeatureResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal create response: %v", err)
	}
	if err := manager.RunSetup(response.FeatureID); err != nil {
		t.Fatalf("RunSetup(%q) error = %v", response.FeatureID, err)
	}
	created, err := store.Load(response.FeatureID)
	if err != nil {
		t.Fatalf("Load(%q) error = %v", response.FeatureID, err)
	}
	repo := created.Repos[0]
	if got := mutationGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD"); got != expected.ObservedSha {
		t.Fatalf("worktree HEAD = %q; want accepted local %q, not fetched origin %q", got, expected.ObservedSha, remoteSha)
	}
	task := created.Run().Setup.Tasks["worktree:"+testRepoAName]
	if task.StartPoint != expected.ObservedSha || task.ExactSHA != expected.ObservedSha {
		t.Fatalf("setup pin = start_point:%q exact_sha:%q; want accepted local %q", task.StartPoint, task.ExactSHA, expected.ObservedSha)
	}
}

func TestOriginCheckFailureDoesNotBlockCreationAtAcceptedLocalCommit(t *testing.T) {
	runtimeDir := t.TempDir()
	repoPath, _ := originAcceptanceRemoteFixture(t, runtimeDir, testRepoAName)
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
	store, manager, handler := newOriginCheckAcceptanceRuntime(t, cfg, runtimeDir)

	// Break origin access while the local source stays valid: the check must
	// return a warning snapshot, and creation must continue from the local
	// commit without an acknowledgement gate.
	mutationGitOutput(t, repoPath, "remote", "set-url", "origin", filepath.Join(runtimeDir, "missing-remote"))

	row := awaitOriginStatusRow(t, handler, originStatusRequest(t, testRepoAName, repoPath))
	if row.Status != serverruntime.RepositoryOriginStatusStatusUnknown {
		t.Fatalf("origin check status = %q; want unknown", row.Status)
	}
	if row.Issue == nil || row.Issue.Code != "origin_check_unavailable" {
		t.Fatalf("origin check issue = %+v; want origin_check_unavailable", row.Issue)
	}

	expected := mutationRepositorySource(t, testRepoAName, repoPath, gitpkg.LocalSourceModeDefault)
	rec := postCreateFeatureREST(t, handler, serverruntime.CreateFeatureRequest{
		Name:              "Continue after failed origin check",
		Repos:             []string{testRepoAName},
		RepositorySources: []serverruntime.RepositorySource{expected},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/features status = %d, want %d; body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var response serverruntime.CreateFeatureResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal create response: %v", err)
	}
	if err := manager.RunSetup(response.FeatureID); err != nil {
		t.Fatalf("RunSetup(%q) error = %v", response.FeatureID, err)
	}
	created, err := store.Load(response.FeatureID)
	if err != nil {
		t.Fatalf("Load(%q) error = %v", response.FeatureID, err)
	}
	if got := mutationGitOutput(t, created.Repos[0].WorktreePath, "rev-parse", "HEAD"); got != expected.ObservedSha {
		t.Fatalf("worktree HEAD = %q; want accepted local %q despite the failed check", got, expected.ObservedSha)
	}
}

func TestCreationUsesAcceptedLocalCommitWithoutFetchingNewerOrigin(t *testing.T) {
	runtimeDir := t.TempDir()
	repoPath, bare := originAcceptanceRemoteFixture(t, runtimeDir, testRepoAName)
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
	store, manager, handler := newOriginCheckAcceptanceRuntime(t, cfg, runtimeDir)

	row := awaitOriginStatusRow(t, handler, originStatusRequest(t, testRepoAName, repoPath))
	checkedSha := row.FetchedSha
	if row.Status != serverruntime.RepositoryOriginStatusStatusBehind || checkedSha == "" {
		t.Fatalf("origin check row = %#v; want a completed behind comparison", row)
	}

	// Origin moves again after the completed check. Submission must use the
	// accepted local commit, never the newer origin data, and must not fetch.
	writer := filepath.Join(runtimeDir, "later-writer")
	mutationGitOutput(t, runtimeDir, "clone", bare, writer)
	mutationGitOutput(t, writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote three")
	mutationGitOutput(t, writer, "push", "origin", "main")
	newerRemoteSha := mutationGitOutput(t, bare, "rev-parse", "refs/heads/main")
	if newerRemoteSha == checkedSha {
		t.Fatal("origin did not advance after the completed check")
	}

	expected := mutationRepositorySource(t, testRepoAName, repoPath, gitpkg.LocalSourceModeDefault)
	rec := postCreateFeatureREST(t, handler, serverruntime.CreateFeatureRequest{
		Name:              "Continue with newer origin data",
		Repos:             []string{testRepoAName},
		RepositorySources: []serverruntime.RepositorySource{expected},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/features status = %d, want %d; body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var response serverruntime.CreateFeatureResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal create response: %v", err)
	}
	if err := manager.RunSetup(response.FeatureID); err != nil {
		t.Fatalf("RunSetup(%q) error = %v", response.FeatureID, err)
	}
	created, err := store.Load(response.FeatureID)
	if err != nil {
		t.Fatalf("Load(%q) error = %v", response.FeatureID, err)
	}
	if got := mutationGitOutput(t, created.Repos[0].WorktreePath, "rev-parse", "HEAD"); got != expected.ObservedSha {
		t.Fatalf("worktree HEAD = %q; want accepted local %q, not newer origin %q", got, expected.ObservedSha, newerRemoteSha)
	}
	if got := mutationGitOutput(t, repoPath, "rev-parse", "refs/remotes/origin/main"); got != checkedSha {
		t.Fatalf("tracking ref = %q; want the checked %q — submission must not fetch", got, checkedSha)
	}
}

// originAcceptanceRemoteFixture builds a repository whose local main is two
// commits behind its bare origin, mirroring the server-side origin-status
// fixture with the mutation-test git helpers.
func originAcceptanceRemoteFixture(t *testing.T, runtimeDir, name string) (repo, bare string) {
	t.Helper()
	repo = filepath.Join(runtimeDir, name)
	initMutationGitRepo(t, repo)
	bare = filepath.Join(runtimeDir, name+"-origin.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", bare, err)
	}
	mutationGitOutput(t, bare, "init", "--bare")
	mutationGitOutput(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	mutationGitOutput(t, repo, "remote", "add", "origin", bare)
	mutationGitOutput(t, bare, "fetch", repo, "main:refs/heads/main")
	mutationGitOutput(t, repo, "fetch", "origin")
	mutationGitOutput(t, repo, "branch", "--set-upstream-to=origin/main", "main")

	writer := filepath.Join(runtimeDir, name+"-writer")
	mutationGitOutput(t, runtimeDir, "clone", bare, writer)
	mutationGitOutput(t, writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote one")
	mutationGitOutput(t, writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote two")
	mutationGitOutput(t, writer, "push", "origin", "main")
	return repo, bare
}

func newOriginCheckAcceptanceRuntime(t *testing.T, cfg *config.Config, runtimeDir string) (*feature.Store, *feature.Manager, http.Handler) {
	t.Helper()
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	manager.Worktrees = gitpkg.NewWorktreeManager(filepath.Join(runtimeDir, "worktrees"))
	target := newRESTCreateFeatureTarget(store, manager, cfg, filepath.Join(runtimeDir, "config.yaml"))
	handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		DisableHostValidation: true,
		Features:              store,
		FeatureStore:          store,
		Config:                cfg,
		Mutations:             &target,
	})
	return store, manager, handler
}

func originStatusRequest(t testing.TB, repoKey, repoPath string) serverruntime.RepositoryOriginStatusRequest {
	t.Helper()
	identity, ok := gitpkg.ResolveRepoIdentity(repoPath)
	if !ok {
		t.Fatalf("ResolveRepoIdentity(%q) failed", repoPath)
	}
	return serverruntime.RepositoryOriginStatusRequest{
		Mode: serverruntime.RepositoryOriginStatusRequestModeDefault,
		Repositories: []serverruntime.RepositorySourceSelector{{
			RepoKey: repoKey,
			Identity: serverruntime.RepositoryIdentity{
				Path:      identity.Path,
				CommonDir: identity.CommonDir,
				Device:    gitpkg.FormatIdentityDevice(identity.Device),
				Inode:     gitpkg.FormatIdentityInode(identity.Inode),
			},
		}},
	}
}

func awaitOriginStatusRow(t *testing.T, handler http.Handler, request serverruntime.RepositoryOriginStatusRequest) serverruntime.RepositoryOriginStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("Marshal origin-status request: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspace/repositories/origin-status", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Agentico-Client", "local")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST origin-status status = %d; want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		var response serverruntime.RepositoryOriginStatusResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("Unmarshal origin-status response: %v", err)
		}
		if len(response.Repositories) != 1 {
			t.Fatalf("origin-status rows = %d; want 1", len(response.Repositories))
		}
		row := response.Repositories[0]
		if row.Status != serverruntime.RepositoryOriginStatusStatusChecking || time.Now().After(deadline) {
			return row
		}
		time.Sleep(25 * time.Millisecond)
	}
}
