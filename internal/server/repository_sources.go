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

const apiPathWorkspaceRepositorySources = "/api/v1/workspace/repositories/sources"

func (h *apiHandler) handleWorkspaceRepositorySourcesRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !h.requireTrustedMutation(w, r) {
		return
	}
	var req RepositorySourcesRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if !req.Mode.Valid() || len(req.Repositories) == 0 || len(req.Repositories) > 32 {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("mode and 1-32 repository selectors are required"))
		return
	}
	mode := git.LocalSourceMode(req.Mode)
	response := RepositorySourcesResponse{Repositories: make([]RepositorySource, 0, len(req.Repositories))}
	seen := make(map[string]struct{}, len(req.Repositories))
	for _, selector := range req.Repositories {
		if strings.TrimSpace(selector.RepoKey) == "" || len(selector.RepoKey) > maxInitializeRepoKeyLength {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("repository key is missing or exceeds the bounded length"))
			return
		}
		key, identity, ok := h.resolveCatalogSourceSelector(selector)
		if !ok {
			writeAPIError(w, http.StatusConflict, errcat.InvalidRepository,
				errcat.WithDiagnostics("the selected repository is no longer present under the expected identity"))
			return
		}
		if _, duplicate := seen[identity.Path]; duplicate {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("repository selectors must be unique"))
			return
		}
		seen[identity.Path] = struct{}{}
		resolved, err := git.InspectLocalSource(r.Context(), identity.Path, mode)
		if err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, git.ErrLocalSourceMissing) {
				status = http.StatusConflict
			}
			writeAPIError(w, status, errcat.InvalidRepository,
				errcat.WithDiagnostics(err.Error()))
			return
		}
		kind := RepositorySourceKindBranch
		if resolved.Kind == git.LocalSourceDetached {
			kind = RepositorySourceKindDetached
		}
		response.Repositories = append(response.Repositories, RepositorySource{
			RepoKey: key,
			Identity: RepositoryIdentity{
				Path: identity.Path, CommonDir: identity.CommonDir,
				Device: git.FormatIdentityDevice(identity.Device), Inode: git.FormatIdentityInode(identity.Inode),
			},
			Mode: RepositorySourceMode(resolved.Mode), Kind: kind,
			Branch: resolved.Branch, ObservedSha: resolved.Commit,
		})
	}
	writeActionJSON(w, http.StatusOK, &response)
}

func (h *apiHandler) resolveCatalogSourceSelector(selector RepositorySourceSelector) (string, git.RepoIdentity, bool) {
	all := config.AllRepos(runtimeConfigRepoSnapshot(h.configOrDefault()))
	keys := make([]string, 0, len(all))
	for key := range all {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for i, key := range keys {
		if key == selector.RepoKey {
			keys[0], keys[i] = keys[i], keys[0]
			break
		}
	}
	for _, key := range keys {
		identity, ok := git.ResolveRepoIdentity(workspace.ExpandHome(all[key].Path))
		if ok && sameWireIdentity(selector.Identity, identity) {
			return key, identity, true
		}
	}
	return "", git.RepoIdentity{}, false
}
