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
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
)

// newRepoInitHandler builds a handler with one valid workspace root and a
// fake git-init function that just creates a .git directory (so tests don't
// depend on a git binary).
func newRepoInitHandler(t *testing.T, root string) (*apiHandler, http.Handler, *[]string) {
	t.Helper()
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{root}
	var initialized []string
	api := newAPIHandler(HandlerOptions{
		Config:                cfg,
		Mutations:             &createFeatureRecorder{},
		DisableHostValidation: true,
		InitGitRepository: func(path string) error {
			initialized = append(initialized, path)
			return os.MkdirAll(filepath.Join(path, ".git"), 0o755)
		},
	})
	return api, api.routes(), &initialized
}

func decodeErrorBody(t *testing.T, w *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var body ErrorResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return body
}

func TestWorkspaceRepositoryInitCreatesDiscoverableRepository(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	api, handler, initialized := newRepoInitHandler(t, root)
	target := filepath.Join(root, "brand-new-repo")

	seqBefore := api.broker.currentSeq()
	w := postTrustedJSON(handler, "/api/v1/workspace/repositories/init", map[string]any{
		"path":    target,
		"consent": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	var body RepositoryInitResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Result != "initialized" {
		t.Fatalf("result = %q; want initialized", body.Result)
	}
	if body.Repository.Name != "brand-new-repo" {
		t.Fatalf("repository.name = %q; want brand-new-repo", body.Repository.Name)
	}
	if len(*initialized) != 1 {
		t.Fatalf("git init calls = %v; want exactly one", *initialized)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Fatalf("target not initialized: %v", err)
	}
	if seqAfter := api.broker.currentSeq(); seqAfter <= seqBefore {
		t.Fatalf("broker seq = %d (before %d); want config invalidation event", seqAfter, seqBefore)
	}

	// The new repository must be discoverable through workspace discovery.
	req := httptest.NewRequest(http.MethodGet, apiPathConfigRuntime, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime config status = %d; want 200", rec.Code)
	}
	var runtimeCfg RuntimeConfigResponse
	if err := json.NewDecoder(rec.Result().Body).Decode(&runtimeCfg); err != nil {
		t.Fatalf("decode runtime config: %v", err)
	}
	found := false
	for _, repo := range runtimeCfg.Repos {
		if repo.Name == "brand-new-repo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("runtime config repos = %+v; want brand-new-repo discovered", runtimeCfg.Repos)
	}
}

func TestWorkspaceRepositoryInitRequiresExplicitConsent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, handler, initialized := newRepoInitHandler(t, root)
	target := filepath.Join(root, "no-consent")

	w := postTrustedJSON(handler, "/api/v1/workspace/repositories/init", map[string]any{
		"path": target,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
	if body := decodeErrorBody(t, w); body.Error.Code != "consent_required" {
		t.Fatalf("error code = %q; want consent_required", body.Error.Code)
	}
	if len(*initialized) != 0 {
		t.Fatalf("git init calls = %v; want none without consent", *initialized)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target created without consent (err=%v)", err)
	}
}

func TestWorkspaceRepositoryInitRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape-link")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	_, handler, initialized := newRepoInitHandler(t, root)

	tests := []struct {
		name     string
		path     string
		wantCode string
	}{
		{name: "traversal", path: root + string(filepath.Separator) + ".." + string(filepath.Separator) + "sneaky", wantCode: "invalid_repository_path"},
		{name: "relative", path: "relative/repo", wantCode: "invalid_repository_path"},
		{name: "empty", path: "  ", wantCode: "invalid_repository_path"},
		{name: "outside root", path: filepath.Join(outside, "repo"), wantCode: "path_outside_workspace_root"},
		{name: "workspace root itself", path: root, wantCode: "path_outside_workspace_root"},
		{name: "symlink escape", path: filepath.Join(root, "escape-link", "repo"), wantCode: "path_outside_workspace_root"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := postTrustedJSON(handler, "/api/v1/workspace/repositories/init", map[string]any{
				"path":    tc.path,
				"consent": true,
			})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
			}
			if body := decodeErrorBody(t, w); body.Error.Code != tc.wantCode {
				t.Fatalf("error code = %q; want %q", body.Error.Code, tc.wantCode)
			}
		})
	}
	if len(*initialized) != 0 {
		t.Fatalf("git init calls = %v; want none for unsafe paths", *initialized)
	}
}

func TestWorkspaceRepositoryInitRejectsIncompatibleTargets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, handler, initialized := newRepoInitHandler(t, root)

	existingRepo := filepath.Join(root, "existing-repo")
	if err := os.MkdirAll(filepath.Join(existingRepo, ".git"), 0o755); err != nil {
		t.Fatalf("create existing repo: %v", err)
	}
	nonEmpty := filepath.Join(root, "occupied")
	if err := os.MkdirAll(nonEmpty, 0o755); err != nil {
		t.Fatalf("create non-empty dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmpty, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	emptyDir := filepath.Join(root, "empty-ok")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatalf("create empty dir: %v", err)
	}

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantCode   string
	}{
		{name: "already a repository", path: existingRepo, wantStatus: http.StatusConflict, wantCode: "already_repository"},
		{name: "non-empty directory", path: nonEmpty, wantStatus: http.StatusConflict, wantCode: "directory_not_empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := postTrustedJSON(handler, "/api/v1/workspace/repositories/init", map[string]any{
				"path":    tc.path,
				"consent": true,
			})
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d body=%s; want %d", w.Code, w.Body.String(), tc.wantStatus)
			}
			if body := decodeErrorBody(t, w); body.Error.Code != tc.wantCode {
				t.Fatalf("error code = %q; want %q", body.Error.Code, tc.wantCode)
			}
		})
	}
	if len(*initialized) != 0 {
		t.Fatalf("git init calls = %v; want none for incompatible targets", *initialized)
	}

	// An existing empty directory inside the root is initializable.
	w := postTrustedJSON(handler, "/api/v1/workspace/repositories/init", map[string]any{
		"path":    emptyDir,
		"consent": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("empty dir init status = %d body=%s; want 201", w.Code, w.Body.String())
	}
}

func TestWorkspaceRepositoryInitRejectsUntrustedClients(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, handler, initialized := newRepoInitHandler(t, root)
	payload, _ := json.Marshal(map[string]any{"path": filepath.Join(root, "repo"), "consent": true})

	// Missing trusted local-client header.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspace/repositories/init", bytes.NewReader(payload))
	req.Header.Set("Content-Type", contentTypeJSON)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("untrusted client status = %d; want 403", w.Code)
	}

	// Non-loopback Host is rejected when host validation is enabled.
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{root}
	strict := NewHandler(HandlerOptions{Config: cfg, Mutations: &createFeatureRecorder{}})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/workspace/repositories/init", bytes.NewReader(payload))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	req.Host = "evil.example.com"
	w = httptest.NewRecorder()
	strict.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-loopback host status = %d; want 403", w.Code)
	}
	if len(*initialized) != 0 {
		t.Fatalf("git init calls = %v; want none for untrusted clients", *initialized)
	}
}
