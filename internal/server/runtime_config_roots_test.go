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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

// runtimeConfigRecorder records RuntimeConfig mutation calls so tests can
// assert nothing was persisted when validation rejects the request.
type runtimeConfigRecorder struct {
	MutationTarget
	calls atomic.Int64
}

func (r *runtimeConfigRecorder) RuntimeConfig(req RuntimeConfigMutationRequest) (RuntimeConfigUpdateResponse, error) {
	r.calls.Add(1)
	return RuntimeConfigUpdateResponse{Result: resultUpdated}, nil
}

func newRuntimeConfigHandler(t *testing.T, recorder *runtimeConfigRecorder) http.Handler {
	t.Helper()
	cfg := config.NewDefault()
	api := newAPIHandler(HandlerOptions{
		Config:                cfg,
		Mutations:             recorder,
		DisableHostValidation: true,
	})
	return api.routes()
}

func patchTrustedJSON(handler http.Handler, path string, body any) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestPatchRuntimeConfigAcceptsValidWorkspaceRoots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	second := t.TempDir()
	recorder := &runtimeConfigRecorder{}
	handler := newRuntimeConfigHandler(t, recorder)

	w := patchTrustedJSON(handler, apiPathConfigRuntime, map[string]any{
		"workspace_roots": []string{root, second},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if got := recorder.calls.Load(); got != 1 {
		t.Fatalf("RuntimeConfig calls = %d; want one authoritative mutation", got)
	}
}

func TestPatchRuntimeConfigRejectsInvalidWorkspaceRoots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fileRoot := filepath.Join(root, "a-file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o644); err != nil {
		t.Fatalf("create file root: %v", err)
	}
	nonexistent := filepath.Join(root, "does-not-exist")

	tests := []struct {
		name        string
		roots       []string
		wantPaths   []string
		wantReasons []string
	}{
		{
			name:        "nonexistent root",
			roots:       []string{nonexistent},
			wantPaths:   []string{nonexistent},
			wantReasons: []string{"path does not exist"},
		},
		{
			name:        "file is not a directory",
			roots:       []string{fileRoot},
			wantPaths:   []string{fileRoot},
			wantReasons: []string{"path is not a directory"},
		},
		{
			name:        "mixed valid and invalid roots",
			roots:       []string{root, nonexistent, fileRoot},
			wantPaths:   []string{nonexistent, fileRoot},
			wantReasons: []string{"path does not exist", "path is not a directory"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &runtimeConfigRecorder{}
			handler := newRuntimeConfigHandler(t, recorder)

			w := patchTrustedJSON(handler, apiPathConfigRuntime, map[string]any{
				"workspace_roots": tc.roots,
			})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
			}
			body := decodeErrorBody(t, w)
			if body.Error.Code != string(errcat.InvalidWorkspaceRoot) {
				t.Fatalf("error code = %q; want %q", body.Error.Code, errcat.InvalidWorkspaceRoot)
			}
			// The summary names every rejected path; the raw reasons travel
			// as diagnostics.
			wantSummary := "Some workspace roots do not resolve to existing directories: " + strings.Join(tc.wantPaths, "; ") + "."
			if body.Error.Summary != wantSummary {
				t.Fatalf("summary = %q; want %q", body.Error.Summary, wantSummary)
			}
			rejected := make([]string, len(tc.wantPaths))
			for i, path := range tc.wantPaths {
				rejected[i] = fmt.Sprintf("%s (%s)", path, tc.wantReasons[i])
			}
			if wantDiagnostics := "rejected workspace roots: " + strings.Join(rejected, "; "); body.Error.Diagnostics != wantDiagnostics {
				t.Fatalf("diagnostics = %q; want %q", body.Error.Diagnostics, wantDiagnostics)
			}
			// Rejection happens before the mutation target, so nothing is
			// persisted.
			if got := recorder.calls.Load(); got != 0 {
				t.Fatalf("RuntimeConfig calls = %d; want none for invalid roots", got)
			}
		})
	}
}

func TestPutRuntimeConfigRejectsInvalidWorkspaceRoots(t *testing.T) {
	t.Parallel()
	recorder := &runtimeConfigRecorder{}
	handler := newRuntimeConfigHandler(t, recorder)

	payload, _ := json.Marshal(map[string]any{
		"workspace_roots": []string{filepath.Join(t.TempDir(), "missing")},
	})
	req := httptest.NewRequest(http.MethodPut, apiPathConfigRuntime, bytes.NewReader(payload))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.InvalidWorkspaceRoot) {
		t.Fatalf("error code = %q; want %q", body.Error.Code, errcat.InvalidWorkspaceRoot)
	}
	if got := recorder.calls.Load(); got != 0 {
		t.Fatalf("RuntimeConfig calls = %d; want none for invalid roots", got)
	}
}
