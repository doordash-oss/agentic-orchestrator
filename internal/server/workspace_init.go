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
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/workspace"
)

// apiPathWorkspaceRepositoriesInit is the bounded repository-initialization
// mutation route used by first-launch onboarding.
const apiPathWorkspaceRepositoriesInit = "/api/v1/workspace/repositories/init"

// handleWorkspaceRepositoryInitRoute serves POST
// /api/v1/workspace/repositories/init. The desktop app supplies the native
// directory choice; the server owns git detection, path confinement and
// initialization. Requirements enforced here:
//   - trusted loopback mutation (bearer auth + X-Agentico-Client, loopback
//     host/origin) via the shared middleware and requireTrustedMutation
//   - an explicit consent flag
//   - an absolute, traversal-free target that resolves (through symlinks)
//     to a location strictly inside a configured workspace root
//   - the target is not already a git repository and, when it exists, is an
//     empty directory
func (h *apiHandler) handleWorkspaceRepositoryInitRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req RepositoryInitSchema
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if !req.Consent {
		writeAPIError(w, http.StatusBadRequest, errcat.ConsentRequired)
		return
	}
	target, root, ok := h.validateRepoInitTarget(w, req.Path)
	if !ok {
		return
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError, errcat.WithDiagnostics("create repository directory"))
		return
	}
	initRepo := h.initGitRepository
	if initRepo == nil {
		initRepo = git.InitRepository
	}
	if err := initRepo(target); err != nil {
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError, errcat.WithDiagnostics("initialize repository"))
		return
	}
	repo := h.discoveredWorkspaceRepository(target)
	repo.Root = root
	if h.broker != nil {
		h.broker.publish(snapshotRequiredEventDTO(sseEventConfigUpdated, Resource{Type: resourceTypeRuntime}))
	}
	resp := RepositoryInitResponse{Result: "initialized", Repository: repo}
	writeActionJSON(w, http.StatusCreated, &resp)
}

// validateRepoInitTarget resolves and confines the requested target path.
// On success it returns the fully resolved target and the configured
// workspace root (as configured, for display) that contains it.
func (h *apiHandler) validateRepoInitTarget(w http.ResponseWriter, raw string) (target, root string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		writeAPIError(w, http.StatusBadRequest, errcat.InvalidRepositoryPath,
			errcat.WithParams(errcat.PathParams{Path: raw}), errcat.WithDiagnostics("path is required"))
		return "", "", false
	}
	if containsTraversalSegment(raw) {
		writeAPIError(w, http.StatusBadRequest, errcat.InvalidRepositoryPath,
			errcat.WithParams(errcat.PathParams{Path: raw}), errcat.WithDiagnostics("path may not contain traversal segments"))
		return "", "", false
	}
	expanded := workspace.ExpandHome(raw)
	if !filepath.IsAbs(expanded) {
		writeAPIError(w, http.StatusBadRequest, errcat.InvalidRepositoryPath,
			errcat.WithParams(errcat.PathParams{Path: raw}), errcat.WithDiagnostics("path must be absolute"))
		return "", "", false
	}
	resolved, err := resolveSymlinkedPrefix(filepath.Clean(expanded))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, errcat.InvalidRepositoryPath,
			errcat.WithParams(errcat.PathParams{Path: raw}), errcat.WithDiagnostics("path could not be resolved"))
		return "", "", false
	}
	root, ok = h.containingWorkspaceRoot(resolved)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, errcat.PathOutsideWorkspaceRoot,
			errcat.WithParams(errcat.PathParams{Path: raw}))
		return "", "", false
	}
	info, err := os.Stat(resolved)
	switch {
	case err == nil && !info.IsDir():
		writeAPIError(w, http.StatusConflict, errcat.InvalidRepositoryPath,
			errcat.WithParams(errcat.PathParams{Path: raw}), errcat.WithDiagnostics("path exists and is not a directory"))
		return "", "", false
	case err == nil && workspace.IsGitRepo(resolved):
		writeAPIError(w, http.StatusConflict, errcat.AlreadyRepository,
			errcat.WithParams(errcat.PathParams{Path: raw}))
		return "", "", false
	case err == nil:
		entries, readErr := os.ReadDir(resolved)
		if readErr != nil {
			writeAPIError(w, http.StatusInternalServerError, errcat.InternalError, errcat.WithDiagnostics("inspect repository directory"))
			return "", "", false
		}
		if len(entries) > 0 {
			writeAPIError(w, http.StatusConflict, errcat.DirectoryNotEmpty,
				errcat.WithParams(errcat.PathParams{Path: raw}))
			return "", "", false
		}
	case !errors.Is(err, fs.ErrNotExist):
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError, errcat.WithDiagnostics("inspect repository directory"))
		return "", "", false
	}
	return resolved, root, true
}

// containsTraversalSegment reports whether the raw request path carries a
// ".." path segment. Traversal is rejected outright (before any cleaning)
// so the request is never silently rewritten.
func containsTraversalSegment(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

// resolveSymlinkedPrefix resolves symlinks in the deepest existing ancestor
// of path and rejoins the (not yet existing) remainder, so containment checks
// always run against the real filesystem location.
func resolveSymlinkedPrefix(path string) (string, error) {
	var remainder []string
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if len(remainder) == 0 {
				return resolved, nil
			}
			return filepath.Join(append([]string{resolved}, remainder...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		remainder = append([]string{filepath.Base(current)}, remainder...)
		current = parent
	}
}

// containingWorkspaceRoot returns the configured workspace root whose
// resolved location strictly contains resolvedTarget. The workspace root
// itself is not an initializable target.
func (h *apiHandler) containingWorkspaceRoot(resolvedTarget string) (string, bool) {
	cfg := h.configOrDefault()
	for _, configured := range cfg.WorkspaceRoots {
		expanded, err := filepath.Abs(workspace.ExpandHome(configured))
		if err != nil {
			continue
		}
		resolvedRoot, err := filepath.EvalSymlinks(expanded)
		if err != nil {
			continue
		}
		if resolvedTarget != resolvedRoot &&
			strings.HasPrefix(resolvedTarget, resolvedRoot+string(filepath.Separator)) {
			return configured, true
		}
	}
	return "", false
}

// discoveredWorkspaceRepository maps the initialized target back to the
// collision-safe repository key that workspace discovery will use for it.
func (h *apiHandler) discoveredWorkspaceRepository(resolvedPath string) WorkspaceRepository {
	repo := WorkspaceRepository{Name: filepath.Base(resolvedPath), Path: resolvedPath}
	snapshot := runtimeConfigRepoSnapshot(h.configOrDefault())
	for name, entry := range config.AllRepos(snapshot) {
		entryResolved, err := filepath.EvalSymlinks(workspace.ExpandHome(entry.Path))
		if err != nil {
			continue
		}
		if entryResolved == resolvedPath {
			repo.Name = name
			break
		}
	}
	return repo
}
