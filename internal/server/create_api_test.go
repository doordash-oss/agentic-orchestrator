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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/clone"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// createServerFixture wires a real clone service (real bounded git) behind
// the API handler, so create/init journeys exercise the true server path.
type createServerFixture struct {
	t       *testing.T
	api     *apiHandler
	handler http.Handler
	root    string
	cfg     *config.Config
	// release, when non-nil, gates the real repository creation: create
	// requests stay in flight (holding their destination reservation)
	// until it closes.
	release chan struct{}
}

func newCreateServerFixture(t *testing.T) *createServerFixture {
	t.Helper()
	return newCreateServerFixtureGated(t, false)
}

func newCreateServerFixtureGated(t *testing.T, gated bool) *createServerFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{root}
	fx := &createServerFixture{t: t, root: root, cfg: cfg}
	opts := clone.Options{
		StateDir: t.TempDir(),
		Config:   func() *config.Config { return cfg },
	}
	if gated {
		fx.release = make(chan struct{})
		release := fx.release
		opts.CreateGit = func(ctx context.Context, dir string) error {
			<-release
			return git.CreateRepository(ctx, dir)
		}
	}
	// Wire SSE hooks exactly as the handler wires its default service: an
	// injected service would otherwise publish no invalidations.
	var apiRef *apiHandler
	opts.Hooks = clone.Hooks{
		OperationChanged: func(kind, operationID string) { apiRef.publishOperationEvent(kind, operationID) },
		WorkspaceChanged: func() { apiRef.publishCloneWorkspaceEvent() },
	}
	svc, err := clone.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx)
	})
	fx.api = newAPIHandler(HandlerOptions{
		Config:                cfg,
		Mutations:             &createFeatureRecorder{},
		DisableHostValidation: true,
		Clones:                svc,
	})
	apiRef = fx.api
	fx.handler = fx.api.routes()
	return fx
}

func (fx *createServerFixture) create(body map[string]any) *httptest.ResponseRecorder {
	return postTrustedJSON(fx.handler, apiPathWorkspaceRepositoriesCreate, body)
}

func (fx *createServerFixture) createOK(destination, key string) CreateRepositoryResponse {
	fx.t.Helper()
	w := fx.create(map[string]any{
		"root_path":       fx.root,
		"destination":     destination,
		"idempotency_key": key,
		"consent":         true,
	})
	if w.Code != http.StatusCreated {
		fx.t.Fatalf("create status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var resp CreateRepositoryResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		fx.t.Fatalf("decode create response: %v", err)
	}
	return resp
}

func TestWorkspaceRepositoryCreateRequiresExplicitConsent(t *testing.T) {
	t.Parallel()
	fx := newCreateServerFixture(t)

	for _, consent := range []any{false, nil} {
		body := map[string]any{
			"root_path":       fx.root,
			"destination":     "consentless",
			"idempotency_key": "consent-key",
		}
		if consent != nil {
			body["consent"] = consent
		}
		w := fx.create(body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("consent=%v status = %d body=%s; want 400", consent, w.Code, w.Body.String())
		}
		errBody := decodeErrorBody(t, w)
		if errBody.Error.Code != string(errcat.ConsentRequired) {
			t.Errorf("consent=%v code = %s; want consent_required", consent, errBody.Error.Code)
		}
	}
	// Absent consent is rejected before any filesystem mutation.
	if _, err := os.Lstat(filepath.Join(fx.root, "consentless")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("consentless create mutated the filesystem: %v", err)
	}
}

func TestWorkspaceRepositoryCreatePublishesResult(t *testing.T) {
	t.Parallel()
	fx := newCreateServerFixture(t)

	seqBefore := fx.api.broker.currentSeq()
	resp := fx.createOK("fresh", "create-1")
	if resp.Result != "created" {
		t.Fatalf("result = %q; want created", resp.Result)
	}
	repo := resp.Repository
	dest := filepath.Join(fx.root, "fresh")
	if repo.RepoKey != "fresh" || repo.Path != dest || !repo.HasHead || repo.Root != fx.root {
		t.Errorf("repository result = %+v", repo)
	}
	// The response identity is the server-resolved identity of the
	// published repository and matches the readiness catalog entry, which
	// reports the repository feature-ready.
	if repo.Identity == nil {
		t.Fatal("created repository carries no identity")
	}
	snapshot := workspaceReadiness(fx.cfg)
	found := false
	for _, entry := range snapshot.Repositories {
		if entry.Name != "fresh" {
			continue
		}
		found = true
		if entry.Identity == nil ||
			entry.Identity.Path != repo.Identity.Path ||
			entry.Identity.CommonDir != repo.Identity.CommonDir ||
			entry.Identity.Device != repo.Identity.Device ||
			entry.Identity.Inode != repo.Identity.Inode {
			t.Errorf("readiness identity = %+v, response identity = %+v", entry.Identity, repo.Identity)
		}
		if !entry.FeatureReady {
			t.Error("created repository is not feature-ready in readiness")
		}
	}
	if !found {
		t.Fatal("created repository missing from readiness")
	}
	// Publication fired the runtime invalidation.
	if seqAfter := fx.api.broker.currentSeq(); seqAfter <= seqBefore {
		t.Fatalf("broker seq = %d (before %d); want config invalidation event", seqAfter, seqBefore)
	}
	assertServerCreatedRepository(t, dest)
}

func TestWorkspaceRepositoryCreateReplayAndConflicts(t *testing.T) {
	t.Parallel()
	fx := newCreateServerFixture(t)

	first := fx.createOK("replayed", "create-1")
	// Same key, same input: the retained result replays.
	again := fx.createOK("replayed", "create-1")
	if again.Repository.RepoKey != first.Repository.RepoKey ||
		again.Repository.Identity == nil || first.Repository.Identity == nil ||
		*again.Repository.Identity != *first.Repository.Identity {
		t.Errorf("replay result = %+v, want the retained %+v", again.Repository, first.Repository)
	}

	// Different key, same destination: exists.
	w := fx.create(map[string]any{
		"root_path":       fx.root,
		"destination":     "replayed",
		"idempotency_key": "create-2",
		"consent":         true,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate destination status = %d body=%s; want 409", w.Code, w.Body.String())
	}
	if code := decodeErrorBody(t, w).Error.Code; code != string(errcat.CloneDestinationExists) {
		t.Errorf("duplicate destination code = %s; want clone_destination_exists", code)
	}

	// Invalid child names are rejected with their canonical code.
	w = fx.create(map[string]any{
		"root_path":       fx.root,
		"destination":     "bad/name",
		"idempotency_key": "create-3",
		"consent":         true,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid destination status = %d; want 400", w.Code)
	}
	if code := decodeErrorBody(t, w).Error.Code; code != string(errcat.CloneDestinationInvalid) {
		t.Errorf("invalid destination code = %s; want clone_destination_invalid", code)
	}

	// Unconfigured roots are rejected.
	w = fx.create(map[string]any{
		"root_path":       "/not/configured",
		"destination":     "anywhere",
		"idempotency_key": "create-4",
		"consent":         true,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unconfigured root status = %d; want 400", w.Code)
	}
	if code := decodeErrorBody(t, w).Error.Code; code != string(errcat.CloneRootIneligible) {
		t.Errorf("unconfigured root code = %s; want clone_root_ineligible", code)
	}

	// Untrusted mutations (missing client header) are refused.
	payload, _ := json.Marshal(map[string]any{
		"root_path":       fx.root,
		"destination":     "untrusted",
		"idempotency_key": "create-5",
		"consent":         true,
	})
	req := httptest.NewRequest(http.MethodPost, apiPathWorkspaceRepositoriesCreate, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", contentTypeJSON)
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("untrusted create status = %d; want 401/403", rec.Code)
	}
}

func TestWorkspaceRepositoryCreateCompetesWithLegacyInit(t *testing.T) {
	t.Parallel()
	fx := newCreateServerFixture(t)

	// A legacy initialization of an explicitly chosen folder keeps working
	// beside Create and is never converted into the new-child policy.
	plain := filepath.Join(fx.root, "chosen-empty")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	w := postTrustedJSON(fx.handler, apiPathWorkspaceRepositoriesInit, map[string]any{
		"path":    plain,
		"consent": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("legacy init status = %d body=%s; want 201", w.Code, w.Body.String())
	}

	// Create cannot publish over the initialized repository.
	w = fx.create(map[string]any{
		"root_path":       fx.root,
		"destination":     "chosen-empty",
		"idempotency_key": "vs-init-1",
		"consent":         true,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("create over initialized repository status = %d; want 409", w.Code)
	}
	if _, err := os.Stat(filepath.Join(plain, ".git", "HEAD")); err != nil {
		t.Fatalf("legacy-initialized repository was damaged: %v", err)
	}

	// And the reverse order: a published create makes the same target an
	// existing repository for a later legacy init, which refuses without
	// deleting anything.
	created := fx.createOK("made-by-create", "vs-init-2")
	w = postTrustedJSON(fx.handler, apiPathWorkspaceRepositoriesInit, map[string]any{
		"path":    filepath.Join(fx.root, "made-by-create"),
		"consent": true,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("legacy init over created repository status = %d body=%s; want 409", w.Code, w.Body.String())
	}
	if code := decodeErrorBody(t, w).Error.Code; code != string(errcat.AlreadyRepository) {
		t.Errorf("legacy init over created repository code = %s; want already_repository", code)
	}
	if _, err := os.Stat(filepath.Join(created.Repository.Path, ".git", "HEAD")); err != nil {
		t.Fatalf("created repository was damaged by the refused init: %v", err)
	}
}

func TestWorkspaceRepositoryCreateAndInitRaceNeverDestroysData(t *testing.T) {
	t.Parallel()
	// A create held in flight (its staging hidden, destination reserved)
	// races a legacy init on the same child path: exactly one side wins,
	// the loser fails cleanly, and neither can delete the other's data.
	fx := newCreateServerFixtureGated(t, true)

	target := filepath.Join(fx.root, "contested")
	createDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		createDone <- fx.create(map[string]any{
			"root_path":       fx.root,
			"destination":     "contested",
			"idempotency_key": "race-1",
			"consent":         true,
		})
	}()
	// Wait for the in-flight create's hidden staging to appear.
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := os.ReadDir(fx.root)
		if err != nil {
			t.Fatal(err)
		}
		staged := false
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".agentico-create-") {
				staged = true
			}
		}
		if staged {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("in-flight create staging never appeared")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// The legacy init validates a free target and wins the destination
	// while the create is still staged.
	initRec := postTrustedJSON(fx.handler, apiPathWorkspaceRepositoriesInit, map[string]any{
		"path":    target,
		"consent": true,
	})
	close(fx.release)
	createRec := <-createDone

	if initRec.Code != http.StatusCreated {
		t.Fatalf("legacy init status = %d body=%s; want 201 (init won the race)", initRec.Code, initRec.Body.String())
	}
	if createRec.Code != http.StatusConflict {
		t.Fatalf("create status = %d body=%s; want 409 (destination occupied at publication)", createRec.Code, createRec.Body.String())
	}
	// The winner's data is intact and the loser left no residue.
	if _, err := os.Stat(filepath.Join(target, ".git", "HEAD")); err != nil {
		t.Fatalf("winning legacy init data was damaged: %v", err)
	}
	entries, err := os.ReadDir(fx.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".agentico-") {
			t.Errorf("losing create left staging residue: %s", e.Name())
		}
	}
}

// assertServerCreatedRepository proves the promised repository shape with
// real git, independent of the service layer's own assertions.
func assertServerCreatedRepository(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := run("symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("initial branch = %q; want main", got)
	}
	if got := run("rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("commit count = %q; want 1", got)
	}
	if got := run("log", "-1", "--format=%an <%ae>"); got != "Agentico <agentico@localhost>" {
		t.Errorf("author = %q; want Agentico <agentico@localhost>", got)
	}
	if got := run("remote"); got != "" {
		t.Errorf("remotes = %q; want none", got)
	}
}
