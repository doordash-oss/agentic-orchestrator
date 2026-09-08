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
	"net/http"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/workspace"
)

// apiPathWorkspaceRepositoriesInitialize is the bounded explicit
// initialization route for an existing unborn clone: one empty local
// initial commit, preserving origin, branch and every existing file.
const apiPathWorkspaceRepositoriesInitialize = "/api/v1/workspace/repositories/initialize"

// maxInitializeRepoKeyLength bounds the repository selector key. Catalog
// keys can contain slashes; the bound exists so a runaway key cannot bloat
// the mutation body budget.
const maxInitializeRepoKeyLength = 512

// handleWorkspaceRepositoryInitializeRoute serves POST
// /api/v1/workspace/repositories/initialize. The server resolves the
// structured repository selector against its own current authorized
// catalog and revalidates the expected identity before any mutation:
// renderer-supplied keys, paths and identity fields are comparisons, never
// independent filesystem authority. The git operation itself is bounded,
// serialized with the shared per-path mutation guard, and never
// reinitializes, republishes or pushes.
func (h *apiHandler) handleWorkspaceRepositoryInitializeRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req InitializeRepositorySchema
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if !req.Consent {
		writeAPIError(w, http.StatusBadRequest, errcat.ConsentRequired)
		return
	}
	repoKey := req.RepoKey
	if strings.TrimSpace(repoKey) == "" {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("repository key is required"))
		return
	}
	if len(repoKey) > maxInitializeRepoKeyLength {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("repository key exceeds the bounded length"))
		return
	}

	entry, ok := h.currentCatalogRepository(repoKey)
	if !ok {
		writeAPIError(w, http.StatusNotFound, errcat.InitializeRepositoryNotFound,
			errcat.WithDiagnostics("no repository with this key exists in the current catalog"))
		return
	}
	expanded := workspace.ExpandHome(entry.Path)
	if !workspace.IsGitRepo(expanded) {
		writeAPIError(w, http.StatusNotFound, errcat.InitializeRepositoryNotFound,
			errcat.WithDiagnostics("the repository under this key is no longer present"))
		return
	}
	identity, ok := git.ResolveRepoIdentity(expanded)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.InitializeUnavailable,
			errcat.WithDiagnostics("the repository's identity could not be resolved"))
		return
	}
	if !sameWireIdentity(req.Identity, identity) {
		writeAPIError(w, http.StatusConflict, errcat.InitializeIdentityStale,
			errcat.WithDiagnostics("the repository under this key does not match the expected identity"))
		return
	}
	if req.Path != "" {
		// A renderer-supplied path is only a comparison against the
		// server-resolved canonical path, never filesystem authority.
		resolved, err := resolveSymlinkedPrefix(workspace.ExpandHome(req.Path))
		if err != nil || resolved != identity.Path {
			writeAPIError(w, http.StatusConflict, errcat.InitializeIdentityStale,
				errcat.WithDiagnostics("the expected repository path does not match the server-resolved repository"))
			return
		}
	}

	var outcome git.InitializeOutcome
	var err error
	if h.initializeGitRepository != nil {
		outcome, err = h.initializeGitRepository(r.Context(), expanded)
	} else {
		outcome, err = git.InitializeRepositoryAtIdentity(r.Context(), expanded, identity)
	}
	if err != nil {
		h.writeInitializeError(w, err)
		return
	}

	// The repository is re-read after the mutation: the reported key and
	// identity come from the refreshed catalog and a fresh identity
	// resolution, never from the request.
	freshIdentity, ok := git.ResolveRepoIdentity(expanded)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.InitializeUnavailable,
			errcat.WithDiagnostics("the initialized repository's identity could not be re-resolved"))
		return
	}
	if !freshIdentity.Equal(identity) {
		writeAPIError(w, http.StatusConflict, errcat.InitializeIdentityStale,
			errcat.WithDiagnostics("the repository identity changed while initialization was in progress"))
		return
	}
	repo, ok := h.refreshedCatalogRepository(repoKey, freshIdentity)
	if !ok {
		writeAPIError(w, http.StatusConflict, errcat.InitializeIdentityStale,
			errcat.WithDiagnostics("the initialized repository is no longer present in the current catalog"))
		return
	}
	repo.Root = h.containingRootOrDefault(freshIdentity.Path)
	hasHead := git.HasHead(expanded)
	if !hasHead {
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError,
			errcat.WithDiagnostics("initialization reported success but HEAD does not resolve"))
		return
	}
	result := InitializeRepositoryResponseResult("initialized")
	if outcome.AlreadyInitialized {
		result = InitializeRepositoryResponseResult("already_initialized")
	}
	resp := InitializeRepositoryResponse{
		Result: result,
		Repository: InitializeRepositoryResult{
			RepoKey: repo.Name,
			Path:    repo.Path,
			HasHead: true,
			Root:    repo.Root,
			Identity: &RepositoryIdentity{
				Path:      freshIdentity.Path,
				CommonDir: freshIdentity.CommonDir,
				Device:    git.FormatIdentityDevice(freshIdentity.Device),
				Inode:     git.FormatIdentityInode(freshIdentity.Inode),
			},
		},
	}
	// Success (created or refresh-only) changed readiness: every desktop
	// surface re-reads through the established runtime invalidation.
	h.publishCloneWorkspaceEvent()
	writeActionJSON(w, http.StatusOK, &resp)
}

// currentCatalogRepository resolves a repository selector against the
// connected server's current authorized catalog (configured plus
// discovered repositories). Only the server's own resolution authorizes
// the mutation target.
func (h *apiHandler) currentCatalogRepository(repoKey string) (config.RepoConfig, bool) {
	allRepos := config.AllRepos(runtimeConfigRepoSnapshot(h.configOrDefault()))
	entry, ok := allRepos[repoKey]
	if !ok || strings.TrimSpace(entry.Path) == "" {
		return config.RepoConfig{}, false
	}
	return entry, true
}

// refreshedCatalogRepository finds the repository's current catalog key by
// server-resolved identity. The request's exact key wins while it remains a
// valid alias; if discovery renamed the key during the operation, the stable
// identity locates the new collision-safe key instead.
func (h *apiHandler) refreshedCatalogRepository(preferredKey string, identity git.RepoIdentity) (WorkspaceRepository, bool) {
	allRepos := config.AllRepos(runtimeConfigRepoSnapshot(h.configOrDefault()))
	keys := make([]string, 0, len(allRepos))
	if _, ok := allRepos[preferredKey]; ok {
		keys = append(keys, preferredKey)
	}
	remaining := make([]string, 0, len(allRepos))
	for key := range allRepos {
		if key != preferredKey {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	keys = append(keys, remaining...)
	for _, key := range keys {
		candidate, ok := git.ResolveRepoIdentity(workspace.ExpandHome(allRepos[key].Path))
		if ok && candidate.Equal(identity) {
			return WorkspaceRepository{Name: key, Path: candidate.Path}, true
		}
	}
	return WorkspaceRepository{}, false
}

// containingRootOrDefault returns the configured workspace root containing
// the resolved repository path, or the empty string for an explicitly
// registered repository that lives outside every configured root.
func (h *apiHandler) containingRootOrDefault(resolvedPath string) string {
	root, ok := h.containingWorkspaceRoot(resolvedPath)
	if !ok {
		return ""
	}
	return root
}

// sameWireIdentity compares a request's expected identity with a fresh
// server-side resolution. Every field must match: a repository replaced at
// the same path has a new device/inode identity and must never be
// initialized through a stale selector.
func sameWireIdentity(expected RepositoryIdentity, resolved git.RepoIdentity) bool {
	return expected.Path == resolved.Path &&
		expected.CommonDir == resolved.CommonDir &&
		expected.Device == git.FormatIdentityDevice(resolved.Device) &&
		expected.Inode == git.FormatIdentityInode(resolved.Inode)
}

// writeInitializeError maps the git adapter's distinguished eligibility
// and probe failures onto canonical refusal codes. Unknown failures fail
// closed as unavailability with bounded diagnostics.
func (h *apiHandler) writeInitializeError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, git.ErrInitializeContentPresent):
		writeAPIError(w, http.StatusConflict, errcat.InitializeContentPresent)
	case errors.Is(err, git.ErrInitializeOperationActive):
		writeAPIError(w, http.StatusConflict, errcat.InitializeOperationActive)
	case errors.Is(err, git.ErrInitializeIdentityChanged):
		writeAPIError(w, http.StatusConflict, errcat.InitializeIdentityStale,
			errcat.WithDiagnostics(boundInitializeDiagnostics(err)))
	case errors.Is(err, git.ErrInitializeNotARepository):
		writeAPIError(w, http.StatusNotFound, errcat.InitializeRepositoryNotFound,
			errcat.WithDiagnostics(boundInitializeDiagnostics(err)))
	default:
		writeAPIError(w, http.StatusServiceUnavailable, errcat.InitializeUnavailable,
			errcat.WithDiagnostics(boundInitializeDiagnostics(err)))
	}
	return false
}

// initializeDiagnosticsBound caps the raw adapter detail carried onto the
// wire; the git layer already bounds command output, this is the backstop.
const initializeDiagnosticsBound = 400

func boundInitializeDiagnostics(err error) string {
	detail := strings.TrimSpace(err.Error())
	if len(detail) > initializeDiagnosticsBound {
		detail = detail[:initializeDiagnosticsBound]
	}
	return detail
}
