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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	gitpkg "github.com/doordash-oss/agentic-orchestrator/internal/git"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestServerMutationTargetCreateFeatureWithoutDisplayedSourcesCapturesExactLocalCommit(t *testing.T) {
	runtimeDir := t.TempDir()
	repoPath := filepath.Join(runtimeDir, testRepoAName)
	initMutationGitRepo(t, repoPath)
	wantCommit := mutationGitOutput(t, repoPath, "rev-parse", "HEAD")

	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	target := newRESTCreateFeatureTarget(store, manager, cfg, filepath.Join(runtimeDir, "config.yaml"))

	result, err := target.CreateFeature(serverruntime.CreateFeatureRequest{
		Name:  "Capture local source",
		Repos: []string{testRepoAName},
	})
	if err != nil {
		t.Fatalf("CreateFeature() error = %v", err)
	}
	created, err := store.Load(result.FeatureID)
	if err != nil {
		t.Fatalf("Load(%q) error = %v", result.FeatureID, err)
	}
	if len(created.Repos) != 1 || created.Repos[0].Source == nil {
		t.Fatalf("created repositories = %+v; want accepted local source", created.Repos)
	}
	if got := created.Repos[0].Source.Commit; got != wantCommit {
		t.Fatalf("accepted source commit = %q, want %q", got, wantCommit)
	}
	task := created.Run().Setup.Tasks["worktree:"+testRepoAName]
	if task.StartPoint != wantCommit || task.ExactSHA != wantCommit {
		t.Fatalf("queued worktree pin = start_point:%q exact_sha:%q, want %q", task.StartPoint, task.ExactSHA, wantCommit)
	}
}

func TestCreateFeatureRESTRejectsChangedBranchWithRefreshedSource(t *testing.T) {
	runtimeDir := t.TempDir()
	repoPath := filepath.Join(runtimeDir, testRepoAName)
	initMutationGitRepo(t, repoPath)
	expected := mutationRepositorySource(t, testRepoAName, repoPath, gitpkg.LocalSourceModeCurrent)

	mutationGitOutput(t, repoPath, "checkout", "-b", "review/changed-source")
	mutationGitOutput(t, repoPath, "commit", "--allow-empty", "-m", "move source")
	refreshedCommit := mutationGitOutput(t, repoPath, "rev-parse", "HEAD")

	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
	store, handler := newCreateFeatureRESTHandler(runtimeDir, cfg)

	rec := postCreateFeatureREST(t, handler, serverruntime.CreateFeatureRequest{
		Name:                    "Reject changed source",
		Repos:                   []string{testRepoAName},
		UseCurrentBranch:        true,
		UseCurrentBranchPerRepo: map[string]bool{testRepoAName: true},
		RepositorySources:       []serverruntime.RepositorySource{expected},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /api/v1/features status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var response serverruntime.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal error response: %v", err)
	}
	if response.Error.Code != string(errcat.LocalSourceStale) || response.Error.Class != serverruntime.ErrorClassNeedsAction {
		t.Fatalf("error = %+v, want %q needs_action", response.Error, errcat.LocalSourceStale)
	}
	if response.Error.Context == nil || len(response.Error.Context.Repositories) != 1 {
		t.Fatalf("error context = %+v; want refreshed repository source", response.Error.Context)
	}
	got := response.Error.Context.Repositories[0]
	if got.Name != testRepoAName || got.Branch != "review/changed-source" || got.ObservedSha != refreshedCommit {
		t.Fatalf("refreshed repository context = %+v; want repo=%q branch=%q sha=%q", got, testRepoAName, "review/changed-source", refreshedCommit)
	}
	features, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(features) != 0 {
		t.Fatalf("persisted features = %d, want zero after stale refusal", len(features))
	}
}

func TestCreateFeatureRESTReconcilesCatalogRenameByRepositoryIdentity(t *testing.T) {
	runtimeDir := t.TempDir()
	repoPath := filepath.Join(runtimeDir, "renamed-repo")
	initMutationGitRepo(t, repoPath)
	expected := mutationRepositorySource(t, "old-key", repoPath, gitpkg.LocalSourceModeDefault)

	cfg := config.NewDefault()
	cfg.Repos["new-key"] = config.RepoConfig{Path: repoPath}
	store, handler := newCreateFeatureRESTHandler(runtimeDir, cfg)
	rec := postCreateFeatureREST(t, handler, serverruntime.CreateFeatureRequest{
		Name:              "Accept catalog rename",
		Repos:             []string{"old-key"},
		RepositorySources: []serverruntime.RepositorySource{expected},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/features status = %d, want %d; body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var response serverruntime.CreateFeatureResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal create response: %v", err)
	}
	created, err := store.Load(response.FeatureID)
	if err != nil {
		t.Fatalf("Load(%q) error = %v", response.FeatureID, err)
	}
	if len(created.Repos) != 1 || created.Repos[0].Name != "new-key" || created.Repos[0].Source == nil {
		t.Fatalf("created repositories = %+v; want identity-preserving rename to new-key", created.Repos)
	}
	if got := created.Run().Setup.Tasks["worktree:new-key"].ExactSHA; got != expected.ObservedSha {
		t.Fatalf("renamed repository exact SHA = %q, want %q", got, expected.ObservedSha)
	}
}

func TestCreateFeatureRESTRejectsReplacementForEntireMultiRepositoryDraft(t *testing.T) {
	runtimeDir := t.TempDir()
	repoAPath := filepath.Join(runtimeDir, testRepoAName)
	repoBPath := filepath.Join(runtimeDir, testRepoBName)
	initMutationGitRepo(t, repoAPath)
	initMutationGitRepo(t, repoBPath)
	expectedA := mutationRepositorySource(t, testRepoAName, repoAPath, gitpkg.LocalSourceModeDefault)
	expectedB := mutationRepositorySource(t, testRepoBName, repoBPath, gitpkg.LocalSourceModeDefault)

	replacementPath := filepath.Join(runtimeDir, "replacement-b")
	initMutationGitRepo(t, replacementPath)
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoAPath}
	cfg.Repos[testRepoBName] = config.RepoConfig{Path: replacementPath}
	store, handler := newCreateFeatureRESTHandler(runtimeDir, cfg)
	rec := postCreateFeatureREST(t, handler, serverruntime.CreateFeatureRequest{
		Name:              "Reject partial draft",
		Repos:             []string{testRepoAName, testRepoBName},
		RepositorySources: []serverruntime.RepositorySource{expectedA, expectedB},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /api/v1/features status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var response serverruntime.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal replacement response: %v", err)
	}
	if response.Error.Code != string(errcat.LocalSourceStale) {
		t.Fatalf("replacement error code = %q, want %q", response.Error.Code, errcat.LocalSourceStale)
	}
	features, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(features) != 0 {
		t.Fatalf("persisted features = %d, want zero after multi-repository refusal", len(features))
	}
}

func TestCreateFeatureRESTRejectsIncompleteDisplayedSourceSetBeforePersistence(t *testing.T) {
	runtimeDir := t.TempDir()
	repoAPath := filepath.Join(runtimeDir, testRepoAName)
	repoBPath := filepath.Join(runtimeDir, testRepoBName)
	initMutationGitRepo(t, repoAPath)
	initMutationGitRepo(t, repoBPath)
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoAPath}
	cfg.Repos[testRepoBName] = config.RepoConfig{Path: repoBPath}
	store, handler := newCreateFeatureRESTHandler(runtimeDir, cfg)
	rec := postCreateFeatureREST(t, handler, serverruntime.CreateFeatureRequest{
		Name:  "Reject incomplete expectations",
		Repos: []string{testRepoAName, testRepoBName},
		RepositorySources: []serverruntime.RepositorySource{
			mutationRepositorySource(t, testRepoAName, repoAPath, gitpkg.LocalSourceModeDefault),
		},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/v1/features status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	features, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(features) != 0 {
		t.Fatalf("persisted features = %d, want zero for incomplete source set", len(features))
	}
}

func TestQueuedAcceptedCommitSurvivesSetupFailureRestartAndRetry(t *testing.T) {
	runtimeDir := t.TempDir()
	repoPath := filepath.Join(runtimeDir, testRepoAName)
	initMutationGitRepo(t, repoPath)
	expected := mutationRepositorySource(t, testRepoAName, repoPath, gitpkg.LocalSourceModeDefault)

	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	target := newRESTCreateFeatureTarget(store, manager, cfg, filepath.Join(runtimeDir, "config.yaml"))
	result, err := target.CreateFeature(serverruntime.CreateFeatureRequest{
		Name:              "Retry accepted commit",
		Repos:             []string{testRepoAName},
		RepositorySources: []serverruntime.RepositorySource{expected},
	})
	if err != nil {
		t.Fatalf("CreateFeature() error = %v", err)
	}

	blockedBase := filepath.Join(runtimeDir, "blocked-worktrees")
	if err := os.WriteFile(blockedBase, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", blockedBase, err)
	}
	manager.Worktrees = gitpkg.NewWorktreeManager(blockedBase)
	if err := manager.RunSetup(result.FeatureID); err == nil {
		t.Fatal("RunSetup() error = nil, want initial worktree failure")
	}

	mutationGitOutput(t, repoPath, "commit", "--allow-empty", "-m", "advance after acceptance")
	advancedCommit := mutationGitOutput(t, repoPath, "rev-parse", "HEAD")
	if advancedCommit == expected.ObservedSha {
		t.Fatal("source branch did not advance")
	}

	restartedStore := feature.NewStore(filepath.Join(runtimeDir, "features"))
	restartedManager := feature.NewManager(restartedStore, cfg)
	restartedManager.Worktrees = gitpkg.NewWorktreeManager(filepath.Join(runtimeDir, "worktrees"))
	if err := restartedManager.RetrySetup(result.FeatureID); err != nil {
		t.Fatalf("RetrySetup() after restart error = %v", err)
	}
	created, err := restartedStore.Load(result.FeatureID)
	if err != nil {
		t.Fatalf("Load(%q) after retry error = %v", result.FeatureID, err)
	}
	repo := created.Repos[0]
	if got := mutationGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD"); got != expected.ObservedSha {
		t.Fatalf("retried worktree HEAD = %q, want persisted accepted commit %q", got, expected.ObservedSha)
	}
	task := created.Run().Setup.Tasks["worktree:"+testRepoAName]
	if task.StartPoint != expected.ObservedSha || task.ExactSHA != expected.ObservedSha {
		t.Fatalf("retried setup pin = start_point:%q exact_sha:%q, want %q", task.StartPoint, task.ExactSHA, expected.ObservedSha)
	}
}

func TestSynchronousCreationUsesAcceptedCommitWithoutChangingOriginalCheckout(t *testing.T) {
	runtimeDir := t.TempDir()
	repoPath := filepath.Join(runtimeDir, testRepoAName)
	initMutationGitRepo(t, repoPath)
	mutationGitOutput(t, repoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	mutationGitOutput(t, repoPath, "checkout", "-b", "topic/local-work")
	mutationGitOutput(t, repoPath, "commit", "--allow-empty", "-m", "topic source")
	acceptedCommit := mutationGitOutput(t, repoPath, "rev-parse", "HEAD")
	untouchedPath := filepath.Join(repoPath, "untouched.txt")
	if err := os.WriteFile(untouchedPath, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", untouchedPath, err)
	}

	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	manager.Worktrees = gitpkg.NewWorktreeManager(filepath.Join(runtimeDir, "worktrees"))
	created, err := manager.Create("Synchronous exact source", "", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
		UseCurrentBranch: true,
		PinLocalSources:  true,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	repo := created.Repos[0]
	if repo.Source == nil || repo.Source.Commit != acceptedCommit {
		t.Fatalf("accepted source = %+v, want commit %q", repo.Source, acceptedCommit)
	}
	if repo.BaseBranch != "main" {
		t.Fatalf("publication base branch = %q, want main", repo.BaseBranch)
	}
	if got := mutationGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD"); got != acceptedCommit {
		t.Fatalf("synchronous worktree HEAD = %q, want %q", got, acceptedCommit)
	}
	if got := mutationGitOutput(t, repoPath, "branch", "--show-current"); got != "topic/local-work" {
		t.Fatalf("original branch = %q, want topic/local-work", got)
	}
	contents, err := os.ReadFile(untouchedPath)
	if err != nil || string(contents) != "keep me\n" {
		t.Fatalf("original file = %q, %v; want untouched contents", contents, err)
	}
	if got := mutationGitOutput(t, repoPath, "status", "--porcelain"); got != "?? untouched.txt" {
		t.Fatalf("original checkout status = %q, want unchanged untracked file", got)
	}
}

func TestCreateFeatureRESTIdempotentReplayKeepsAcceptedPinAfterBranchMoves(t *testing.T) {
	runtimeDir := t.TempDir()
	repoPath := filepath.Join(runtimeDir, testRepoAName)
	initMutationGitRepo(t, repoPath)
	expected := mutationRepositorySource(t, testRepoAName, repoPath, gitpkg.LocalSourceModeDefault)
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
	store, handler := newCreateFeatureRESTHandler(runtimeDir, cfg)
	request := serverruntime.CreateFeatureRequest{
		Name:              "Replay accepted source",
		Repos:             []string{testRepoAName},
		RepositorySources: []serverruntime.RepositorySource{expected},
		IdempotencyKey:    "accepted-source-replay",
	}
	first := postCreateFeatureREST(t, handler, request)
	if first.Code != http.StatusCreated {
		t.Fatalf("first POST status = %d, want %d; body=%s", first.Code, http.StatusCreated, first.Body.String())
	}
	var firstResponse serverruntime.CreateFeatureResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstResponse); err != nil {
		t.Fatalf("Unmarshal first response: %v", err)
	}

	mutationGitOutput(t, repoPath, "commit", "--allow-empty", "-m", "advance after response")
	second := postCreateFeatureREST(t, handler, request)
	if second.Code != http.StatusCreated {
		t.Fatalf("replay POST status = %d, want %d; body=%s", second.Code, http.StatusCreated, second.Body.String())
	}
	var secondResponse serverruntime.CreateFeatureResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondResponse); err != nil {
		t.Fatalf("Unmarshal replay response: %v", err)
	}
	if secondResponse.FeatureID != firstResponse.FeatureID {
		t.Fatalf("replay feature ID = %q, want %q", secondResponse.FeatureID, firstResponse.FeatureID)
	}
	features, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(features) != 1 {
		t.Fatalf("persisted features = %d, want one idempotent creation", len(features))
	}
	task := features[0].Run().Setup.Tasks["worktree:"+testRepoAName]
	if task.ExactSHA != expected.ObservedSha {
		t.Fatalf("replayed exact SHA = %q, want original accepted %q", task.ExactSHA, expected.ObservedSha)
	}
}

func mutationRepositorySource(t testing.TB, key, repoPath string, mode gitpkg.LocalSourceMode) serverruntime.RepositorySource {
	t.Helper()
	identity, ok := gitpkg.ResolveRepoIdentity(repoPath)
	if !ok {
		t.Fatalf("ResolveRepoIdentity(%q) failed", repoPath)
	}
	source, err := gitpkg.InspectLocalSource(t.Context(), repoPath, mode)
	if err != nil {
		t.Fatalf("InspectLocalSource(%q, %q) error = %v", repoPath, mode, err)
	}
	kind := serverruntime.RepositorySourceKindBranch
	if source.Kind == gitpkg.LocalSourceDetached {
		kind = serverruntime.RepositorySourceKindDetached
	}
	return serverruntime.RepositorySource{
		RepoKey: key,
		Identity: serverruntime.RepositoryIdentity{
			Path:      identity.Path,
			CommonDir: identity.CommonDir,
			Device:    gitpkg.FormatIdentityDevice(identity.Device),
			Inode:     gitpkg.FormatIdentityInode(identity.Inode),
			BirthTime: identity.BirthTime,
		},
		Mode: serverruntime.RepositorySourceMode(source.Mode), Kind: kind,
		Branch: source.Branch, ObservedSha: source.Commit,
	}
}

func postCreateFeatureREST(t testing.TB, handler http.Handler, request serverruntime.CreateFeatureRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal create request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/features", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func newCreateFeatureRESTHandler(runtimeDir string, cfg *config.Config) (*feature.Store, http.Handler) {
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	target := newRESTCreateFeatureTarget(store, manager, cfg, filepath.Join(runtimeDir, "config.yaml"))
	handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		DisableHostValidation: true,
		Features:              store,
		FeatureStore:          store,
		Config:                cfg,
		Mutations:             &target,
	})
	return store, handler
}

func mutationGitOutput(t testing.TB, repoPath string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out))
}
