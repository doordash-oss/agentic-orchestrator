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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// initializeFixture wires the API handler with a real workspace root and
// real bounded git behind the initialize endpoint.
//
// The tests in this file deliberately opt out of t.Parallel(): each one
// forks real git, and this machine's race-detector test binary stalls or
// crashes spawned children when many fork-heavy tests run at once (the
// documented TestProbeTimeoutKillsProcessGroup pathology). The concurrency
// semantics are still exercised — the concurrent-requests test races its
// requests against each other within the test — while the package's other
// parallel tests do not multiply the fork load underneath.
type initializeFixture struct {
	t       *testing.T
	api     *apiHandler
	handler http.Handler
	root    string
	cfg     *config.Config
}

func newInitializeFixture(t *testing.T) *initializeFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{root}
	fx := &initializeFixture{t: t, root: root, cfg: cfg}
	fx.api = newAPIHandler(HandlerOptions{
		Config:                cfg,
		Mutations:             &createFeatureRecorder{},
		DisableHostValidation: true,
	})
	fx.handler = fx.api.routes()
	return fx
}

// unbornClone builds a real unborn repository that mirrors a successful
// clone of an empty remote: an origin remote and a symbolic branch.
func (fx *initializeFixture) unbornClone(name, branch string) string {
	fx.t.Helper()
	repo := filepath.Join(fx.root, name)
	fx.git("init", "--initial-branch="+branch, repo)
	fx.git("-C", repo, "remote", "add", "origin", "http://127.0.0.1:9/remote.git")
	return repo
}

func (fx *initializeFixture) git(args ...string) string {
	fx.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		fx.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (fx *initializeFixture) gitIn(dir string, args ...string) string {
	fx.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		fx.t.Fatalf("git -C %s %v: %v: %s", dir, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// wireIdentity returns the request identity map for the repo's current
// server-resolved identity.
func (fx *initializeFixture) wireIdentity(repo string) map[string]any {
	fx.t.Helper()
	identity, ok := git.ResolveRepoIdentity(repo)
	if !ok {
		fx.t.Fatalf("resolve identity for %s", repo)
	}
	return map[string]any{
		"path":       identity.Path,
		"common_dir": identity.CommonDir,
		"device":     git.FormatIdentityDevice(identity.Device),
		"inode":      git.FormatIdentityInode(identity.Inode),
	}
}

func (fx *initializeFixture) initialize(body map[string]any) *httptest.ResponseRecorder {
	return postTrustedJSON(fx.handler, apiPathWorkspaceRepositoriesInitialize, body)
}

func (fx *initializeFixture) initializeOK(repoKey string, repo string, extra ...map[string]any) InitializeRepositoryResponse {
	fx.t.Helper()
	body := map[string]any{
		"repo_key": repoKey,
		"identity": fx.wireIdentity(repo),
		"consent":  true,
	}
	for _, e := range extra {
		for k, v := range e {
			body[k] = v
		}
	}
	w := fx.initialize(body)
	if w.Code != http.StatusOK {
		fx.t.Fatalf("initialize status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var resp InitializeRepositoryResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		fx.t.Fatalf("decode initialize response: %v", err)
	}
	return resp
}

func assertWireRepository(t *testing.T, resp InitializeRepositoryResponse, wantKey, wantRoot, repo string) {
	t.Helper()
	if resp.Result != InitializeRepositoryResponseResult("initialized") && resp.Result != InitializeRepositoryResponseResult("already_initialized") {
		t.Fatalf("result = %q", resp.Result)
	}
	got := resp.Repository
	if got.RepoKey != wantKey {
		t.Errorf("repo_key = %q; want %q", got.RepoKey, wantKey)
	}
	if got.Path != repo {
		t.Errorf("path = %q; want %q", got.Path, repo)
	}
	if !got.HasHead {
		t.Errorf("has_head = false; want true")
	}
	if got.Root != wantRoot {
		t.Errorf("root = %q; want %q", got.Root, wantRoot)
	}
	if got.Identity == nil {
		t.Fatalf("identity missing from result")
	}
	identity, ok := git.ResolveRepoIdentity(repo)
	if !ok {
		t.Fatalf("resolve identity after initialize")
	}
	if got.Identity.Path != identity.Path || got.Identity.CommonDir != identity.CommonDir ||
		got.Identity.Device != git.FormatIdentityDevice(identity.Device) ||
		got.Identity.Inode != git.FormatIdentityInode(identity.Inode) {
		t.Errorf("result identity %+v does not match fresh resolution %+v", got.Identity, identity)
	}
}

func TestWorkspaceRepositoryInitializeCreatesInitialCommit(t *testing.T) {
	fx := newInitializeFixture(t)
	repo := fx.unbornClone("seedrepo", "feature/seed")

	seqBefore := fx.api.broker.currentSeq()
	resp := fx.initializeOK("seedrepo", repo)
	if resp.Result != "initialized" {
		t.Fatalf("result = %q; want initialized", resp.Result)
	}
	assertWireRepository(t, resp, "seedrepo", fx.root, repo)

	// Real git evidence: one empty Agentico commit on the preserved
	// slash-containing branch, origin intact, nothing staged or pushed.
	if branch := fx.gitIn(repo, "symbolic-ref", "--short", "HEAD"); branch != "feature/seed" {
		t.Fatalf("branch = %q; want feature/seed", branch)
	}
	if count := fx.gitIn(repo, "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("commit count = %q; want 1", count)
	}
	if author := fx.gitIn(repo, "log", "-1", "--format=%an <%ae>"); author != "Agentico <agentico@localhost>" {
		t.Fatalf("author = %q", author)
	}
	if committer := fx.gitIn(repo, "log", "-1", "--format=%cn <%ce>"); committer != "Agentico <agentico@localhost>" {
		t.Fatalf("committer = %q", committer)
	}
	if tree := fx.gitIn(repo, "show", "-s", "--format=%T", "HEAD"); tree != fx.gitIn(repo, "mktree") {
		t.Fatalf("commit tree %q is not the empty tree", tree)
	}
	if remotes := fx.gitIn(repo, "remote"); remotes != "origin" {
		t.Fatalf("remotes = %q; want origin", remotes)
	}
	if originURL := fx.gitIn(repo, "remote", "get-url", "origin"); originURL != "http://127.0.0.1:9/remote.git" {
		t.Fatalf("origin url = %q", originURL)
	}
	if refs := fx.gitIn(repo, "for-each-ref", "--format=%(refname)", "refs/remotes"); refs != "" {
		t.Fatalf("remote-tracking refs = %q; want none (nothing fetched or pushed)", refs)
	}

	// Readiness flips to feature-ready under the same catalog key.
	snapshot := workspaceReadiness(fx.cfg)
	found := false
	for _, r := range snapshot.Repositories {
		if r.Name == "seedrepo" {
			found = true
			if !r.Valid || !r.FeatureReady {
				t.Fatalf("readiness after initialize: valid=%v feature_ready=%v", r.Valid, r.FeatureReady)
			}
		}
	}
	if !found {
		t.Fatal("seedrepo missing from readiness after initialize")
	}

	// Success published the runtime invalidation so every surface re-reads.
	if seqAfter := fx.api.broker.currentSeq(); seqAfter <= seqBefore {
		t.Fatalf("broker seq = %d (before %d); want config invalidation event", seqAfter, seqBefore)
	}
}

func TestWorkspaceRepositoryInitializePreservesExactCatalogKey(t *testing.T) {
	fx := newInitializeFixture(t)
	repo := fx.unbornClone("spaced-key-path", "main")
	fx.cfg.Repos[" spaced key "] = config.RepoConfig{Path: repo}

	resp := fx.initializeOK(" spaced key ", repo)
	if resp.Repository.RepoKey != " spaced key " {
		t.Fatalf("repo_key = %q; want exact catalog key %q", resp.Repository.RepoKey, " spaced key ")
	}
	if !git.HasHead(repo) {
		t.Fatal("repository was not initialized")
	}
}

func TestWorkspaceRepositoryInitializeRequiresExplicitConsent(t *testing.T) {
	fx := newInitializeFixture(t)
	repo := fx.unbornClone("consentless", "main")

	for _, consent := range []any{false, nil} {
		body := map[string]any{
			"repo_key": "consentless",
			"identity": fx.wireIdentity(repo),
		}
		if consent != nil {
			body["consent"] = consent
		}
		w := fx.initialize(body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("consent=%v status = %d body=%s; want 400", consent, w.Code, w.Body.String())
		}
		if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.ConsentRequired) {
			t.Fatalf("consent=%v code = %s; want consent_required", consent, body.Error.Code)
		}
	}
	if git.HasHead(repo) {
		t.Fatal("consentless initialize mutated the repository")
	}
}

func TestWorkspaceRepositoryInitializeRefusesBadSelectorsAndStaleIdentities(t *testing.T) {
	fx := newInitializeFixture(t)
	repo := fx.unbornClone("stale", "main")

	// Unknown key.
	w := fx.initialize(map[string]any{
		"repo_key": "missing",
		"identity": fx.wireIdentity(repo),
		"consent":  true,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown key status = %d body=%s; want 404", w.Code, w.Body.String())
	}
	if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.InitializeRepositoryNotFound) {
		t.Fatalf("unknown key code = %s; want initialize_repository_not_found", body.Error.Code)
	}

	// Empty key is a malformed request.
	w = fx.initialize(map[string]any{
		"repo_key": "   ",
		"identity": fx.wireIdentity(repo),
		"consent":  true,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty key status = %d; want 400", w.Code)
	}

	// Stale identity: the repository is replaced after the client read it.
	replaced := map[string]any{
		"path":       "/definitely/not/the/repo",
		"common_dir": "/definitely/not/the/repo/.git",
		"device":     "1",
		"inode":      "1",
	}
	w = fx.initialize(map[string]any{
		"repo_key": "stale",
		"identity": replaced,
		"consent":  true,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("stale identity status = %d body=%s; want 409", w.Code, w.Body.String())
	}
	if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.InitializeIdentityStale) {
		t.Fatalf("stale identity code = %s; want initialize_identity_stale", body.Error.Code)
	}

	// Path comparison mismatch.
	w = fx.initialize(map[string]any{
		"repo_key": "stale",
		"identity": fx.wireIdentity(repo),
		"path":     "/definitely/not/the/repo",
		"consent":  true,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("path mismatch status = %d; want 409", w.Code)
	}
	if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.InitializeIdentityStale) {
		t.Fatalf("path mismatch code = %s; want initialize_identity_stale", body.Error.Code)
	}

	// A matching path comparison passes.
	fx.initializeOK("stale", repo, map[string]any{"path": repo})

	if !git.HasHead(repo) {
		t.Fatal("matching selector should have initialized the repository")
	}
}

func TestWorkspaceRepositoryInitializeRefusesContentAndActiveOperations(t *testing.T) {
	fx := newInitializeFixture(t)

	untracked := fx.unbornClone("untracked", "main")
	if err := os.WriteFile(filepath.Join(untracked, "user.txt"), []byte("user content"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := fx.initialize(map[string]any{
		"repo_key": "untracked",
		"identity": fx.wireIdentity(untracked),
		"consent":  true,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("untracked status = %d body=%s; want 409", w.Code, w.Body.String())
	}
	if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.InitializeContentPresent) {
		t.Fatalf("untracked code = %s; want initialize_content_present", body.Error.Code)
	}
	if _, err := os.Stat(filepath.Join(untracked, "user.txt")); err != nil {
		t.Fatalf("user content removed on refusal: %v", err)
	}
	if git.HasHead(untracked) {
		t.Fatal("refusal created a commit")
	}

	active := fx.unbornClone("active", "main")
	if err := os.WriteFile(filepath.Join(active, ".git", "MERGE_HEAD"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w = fx.initialize(map[string]any{
		"repo_key": "active",
		"identity": fx.wireIdentity(active),
		"consent":  true,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("active operation status = %d body=%s; want 409", w.Code, w.Body.String())
	}
	if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.InitializeOperationActive) {
		t.Fatalf("active operation code = %s; want initialize_operation_in_progress", body.Error.Code)
	}
	if _, err := os.Stat(filepath.Join(active, ".git", "MERGE_HEAD")); err != nil {
		t.Fatalf("MERGE_HEAD removed on refusal: %v", err)
	}
}

func TestWorkspaceRepositoryInitializeRefreshOnlyWhenAlreadyInitialized(t *testing.T) {
	fx := newInitializeFixture(t)
	repo := fx.unbornClone("adopted", "main")
	// A competing actor (external git) initialized the clone first, and
	// the user has local changes on top.
	fx.gitIn(repo, "-c", "user.name=External", "-c", "user.email=external@example.invalid",
		"commit", "--allow-empty", "-m", "their initial commit")
	if err := os.WriteFile(filepath.Join(repo, "local.txt"), []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := fx.initializeOK("adopted", repo)
	if resp.Result != "already_initialized" {
		t.Fatalf("result = %q; want already_initialized", resp.Result)
	}
	assertWireRepository(t, resp, "adopted", fx.root, repo)
	if count := fx.gitIn(repo, "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("refresh added a commit: count = %q", count)
	}
	if _, err := os.Stat(filepath.Join(repo, "local.txt")); err != nil {
		t.Fatalf("local change removed: %v", err)
	}
}

func TestWorkspaceRepositoryInitializeConcurrentRequestsCreateOneCommit(t *testing.T) {
	fx := newInitializeFixture(t)
	repo := fx.unbornClone("raced", "main")

	const attempts = 8
	results := make([]*httptest.ResponseRecorder, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// Deliberate pacing, not waiting for an outcome: the requests
			// still overlap heavily (each takes ~100ms), but the initial
			// identity probes no longer fork eight git processes in the
			// same instant, which can stall fork/exec on a loaded machine
			// and starve the shared mutation lock's holder.
			time.Sleep(time.Duration(i) * 20 * time.Millisecond)
			results[i] = fx.initialize(map[string]any{
				"repo_key": "raced",
				"identity": fx.wireIdentity(repo),
				"consent":  true,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	initialized := 0
	for i, w := range results {
		if w.Code != http.StatusOK {
			t.Fatalf("attempt %d status = %d body=%s; want 200", i, w.Code, w.Body.String())
		}
		var resp InitializeRepositoryResponse
		if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
			t.Fatalf("attempt %d decode: %v", i, err)
		}
		switch resp.Result {
		case "initialized":
			initialized++
		case "already_initialized":
		default:
			t.Fatalf("attempt %d result = %q", i, resp.Result)
		}
	}
	if initialized != 1 {
		t.Fatalf("initialized results = %d; want exactly 1", initialized)
	}
	if count := fx.gitIn(repo, "rev-list", "--count", "HEAD"); count != "1" {
		t.Fatalf("commit count = %q; want 1", count)
	}
}

func TestWorkspaceRepositoryInitializeRefusesUntrustedClients(t *testing.T) {
	fx := newInitializeFixture(t)
	repo := fx.unbornClone("private", "main")
	payload, _ := json.Marshal(map[string]any{
		"repo_key": "private",
		"identity": fx.wireIdentity(repo),
		"consent":  true,
	})

	// Missing trusted local-client header.
	req := httptest.NewRequest(http.MethodPost, apiPathWorkspaceRepositoriesInitialize, bytes.NewReader(payload))
	req.Header.Set("Content-Type", contentTypeJSON)
	w := httptest.NewRecorder()
	fx.handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("untrusted client status = %d; want 403", w.Code)
	}

	// Non-loopback Host is rejected when host validation is enabled.
	strict := NewHandler(HandlerOptions{Config: fx.cfg, Mutations: &createFeatureRecorder{}})
	req = httptest.NewRequest(http.MethodPost, apiPathWorkspaceRepositoriesInitialize, bytes.NewReader(payload))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	req.Host = "evil.example.com"
	w = httptest.NewRecorder()
	strict.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-loopback host status = %d; want 403", w.Code)
	}
	if git.HasHead(repo) {
		t.Fatal("untrusted request mutated the repository")
	}
}

func TestWorkspaceRepositoryInitializeMissingRepositoryRefusesSafely(t *testing.T) {
	fx := newInitializeFixture(t)
	repo := fx.unbornClone("vanished", "main")
	// An explicitly registered repository (outside discovery) whose data
	// was removed after the client read the catalog.
	other := filepath.Join(fx.root, "explicit")
	w := fx.initialize(map[string]any{
		"repo_key": "explicit",
		"identity": map[string]any{
			"path":       other,
			"common_dir": filepath.Join(other, ".git"),
			"device":     "1",
			"inode":      "1",
		},
		"consent": true,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing repository status = %d body=%s; want 404", w.Code, w.Body.String())
	}
	if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.InitializeRepositoryNotFound) {
		t.Fatalf("missing repository code = %s; want initialize_repository_not_found", body.Error.Code)
	}
	if git.HasHead(repo) {
		t.Fatal("unrelated repository was mutated")
	}
}

func TestWorkspaceRepositoryInitializeInjectedFailureFailsClosed(t *testing.T) {
	fx := newInitializeFixture(t)
	repo := fx.unbornClone("failing", "main")
	fx.api.initializeGitRepository = func(ctx context.Context, dir string) (git.InitializeOutcome, error) {
		return git.InitializeOutcome{}, context.DeadlineExceeded
	}

	w := fx.initialize(map[string]any{
		"repo_key": "failing",
		"identity": fx.wireIdentity(repo),
		"consent":  true,
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("injected failure status = %d body=%s; want 503", w.Code, w.Body.String())
	}
	if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.InitializeUnavailable) {
		t.Fatalf("injected failure code = %s; want initialize_unavailable", body.Error.Code)
	}
	if git.HasHead(repo) {
		t.Fatal("failed attempt created a commit")
	}
}

func TestWorkspaceRepositoryInitializeRejectsIdentityChangedDuringOperation(t *testing.T) {
	fx := newInitializeFixture(t)
	repo := fx.unbornClone("replaced-during-operation", "main")
	expected := fx.wireIdentity(repo)
	oldGitDir := filepath.Join(fx.root, "original.git")
	fx.api.initializeGitRepository = func(ctx context.Context, dir string) (git.InitializeOutcome, error) {
		if err := os.Rename(filepath.Join(dir, ".git"), oldGitDir); err != nil {
			return git.InitializeOutcome{}, err
		}
		fx.git("init", "--initial-branch=main", dir)
		return git.InitializeOutcome{AlreadyInitialized: true}, nil
	}

	w := fx.initialize(map[string]any{
		"repo_key": "replaced-during-operation",
		"identity": expected,
		"consent":  true,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("identity changed status = %d body=%s; want 409", w.Code, w.Body.String())
	}
	if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.InitializeIdentityStale) {
		t.Fatalf("identity changed code = %s; want initialize_identity_stale", body.Error.Code)
	}
	if git.HasHead(repo) {
		t.Fatal("replacement repository was mutated")
	}
	if _, err := os.Stat(oldGitDir); err != nil {
		t.Fatalf("original git directory was not preserved: %v", err)
	}
}

func TestWorkspaceRepositoryInitializeRejectsMalformedBodies(t *testing.T) {
	fx := newInitializeFixture(t)
	repo := fx.unbornClone("strict", "main")

	// Unknown field: strict decoding refuses.
	w := fx.initialize(map[string]any{
		"repo_key":   "strict",
		"identity":   fx.wireIdentity(repo),
		"consent":    true,
		"sneak_path": "/tmp/elsewhere",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	// Oversized key.
	w = fx.initialize(map[string]any{
		"repo_key": strings.Repeat("k", maxInitializeRepoKeyLength+1),
		"identity": fx.wireIdentity(repo),
		"consent":  true,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized key status = %d; want 400", w.Code)
	}
	if git.HasHead(repo) {
		t.Fatal("malformed request mutated the repository")
	}
}
