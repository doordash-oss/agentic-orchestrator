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
	"fmt"
	"net/http"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

// handleCompletionPreflight serves GET
// /api/v1/features/{feature_id}/completion/preflight — a read-only,
// server-authored preview of the feature's completion readiness. It reports
// the exact eligible repository set, already-published or completed outcomes,
// blockers, PR URLs, and an authoritative source revision that publish/merge/
// mark-done mutations can reject as stale before any side effect. The desktop
// never reproduces completion logic or reads feature files for publication
// context; it renders this preview and sends the source revision back.
func (h *apiHandler) handleCompletionPreflight(w http.ResponseWriter, r *http.Request, featureID string) {
	if h.mutations == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	resp, err := h.mutations.CompletionPreflight(featureID)
	if err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	if resp.APIVersion == "" {
		resp.APIVersion = APIVersion
	}
	revision := resp.SourceRevision
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

// handleRepositoryDiff serves GET
// /api/v1/features/{feature_id}/repositories/{repo_name}/diff — a bounded,
// lazy, read-only diff inspection for one repository. Without a file_path
// query parameter, it lists all changed files with summaries. With a
// file_path, it returns bounded diff content for that single file. It rejects
// traversal and symlink escape, imposes per-request and aggregate limits, and
// returns structured partial outcomes when inspection fails.
func (h *apiHandler) handleRepositoryDiff(w http.ResponseWriter, r *http.Request, featureID, repoName string) {
	if h.mutations == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	if !validRepoName(repoName) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics(fmt.Sprintf("invalid repo name for feature %q", featureID)))
		return
	}
	filePath := strings.TrimSpace(r.URL.Query().Get("file_path"))
	if filePath != "" && !validDiffFilePath(filePath) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics(fmt.Sprintf("invalid file path for feature %q", featureID)))
		return
	}
	resp, err := h.mutations.RepositoryDiff(featureID, repoName, filePath)
	if err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	if resp.APIVersion == "" {
		resp.APIVersion = APIVersion
	}
	if resp.Files == nil {
		resp.Files = []RepositoryDiffFile{}
	}
	revision := resp.SourceRevision
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

// handleRepositoryPath serves GET
// /api/v1/features/{feature_id}/repositories/{repo_name}/path. It is intended
// for the desktop main process only: the renderer still receives only opaque
// feature/repository identifiers and never a host path.
func (h *apiHandler) handleRepositoryPath(w http.ResponseWriter, r *http.Request, featureID, repoName string) {
	if h.mutations == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	if !validRepoName(repoName) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics(fmt.Sprintf("invalid repo name for feature %q", featureID)))
		return
	}
	resp, err := h.mutations.RepositoryPath(featureID, repoName)
	if err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	if resp.APIVersion == "" {
		resp.APIVersion = APIVersion
	}
	h.writeRevisionedJSON(w, r, "", resp)
}

// validRepoName checks that a repo name contains only safe identifier
// characters and rejects traversal attempts.
func validRepoName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	for _, c := range name {
		if c == '.' || c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

// validDiffFilePath checks that a file path is relative, contains no
// traversal, and is bounded in length. It does not resolve symlinks — the
// git layer and orchestrator enforce filesystem-level confinement.
func validDiffFilePath(path string) bool {
	if path == "" || len(path) > 512 {
		return false
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return false
	}
	if strings.Contains(path, "\\") {
		return false
	}
	return true
}
