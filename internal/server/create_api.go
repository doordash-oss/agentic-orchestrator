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
	"net/http"

	"github.com/doordash-oss/agentic-orchestrator/internal/clone"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

// apiPathWorkspaceRepositoriesCreate is the bounded repository-creation
// mutation route: a new child repository inside a configured workspace
// root, published atomically with one empty initial commit on main.
const apiPathWorkspaceRepositoriesCreate = "/api/v1/workspace/repositories/create"

// handleWorkspaceRepositoryCreateRoute serves POST
// /api/v1/workspace/repositories/create. The server owns root eligibility,
// child-name validity, destination reservation, owned staging and atomic
// no-replace publication — the same protections as a clone start. The
// explicit consent flag must acknowledge the initial empty commit before
// any filesystem mutation happens.
func (h *apiHandler) handleWorkspaceRepositoryCreateRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	if h.clones == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	var req CreateRepositorySchema
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if !req.Consent {
		writeAPIError(w, http.StatusBadRequest, errcat.ConsentRequired)
		return
	}
	rec, err := h.clones.Create(r.Context(), clone.CreateStartInput{
		Root:           req.RootPath,
		Destination:    req.Destination,
		IdempotencyKey: req.IdempotencyKey,
	})
	if !h.writeCloneError(w, err) {
		return
	}
	switch rec.State {
	case clone.StateSucceeded:
		if rec.Published == nil {
			writeAPIError(w, http.StatusInternalServerError, errcat.InternalError,
				errcat.WithDiagnostics("created operation lacks publication evidence"))
			return
		}
		resp := CreateRepositoryResponse{Result: "created", Repository: createRepositoryResultDTO(rec)}
		writeActionJSON(w, http.StatusCreated, &resp)
	case clone.StateCleanupPending:
		writeAPIError(w, http.StatusConflict, errcat.CloneCleanupPending,
			errcat.WithDiagnostics("the retained attempt for this request key is awaiting cleanup; retry after cleanup resolves"))
	default:
		// A replayed key whose retained attempt failed, was cancelled or
		// was interrupted: the outcome is reported truthfully and a fresh
		// request with a new key revalidates everything.
		writeAPIError(w, http.StatusConflict, errcat.CloneUnavailable,
			errcat.WithDiagnostics("the retained attempt for this request key ended as "+string(rec.State)+"; start a new request with a new key"))
	}
}

// createRepositoryResultDTO projects the durable publication evidence of a
// succeeded create onto the API model. The repository key is the actual
// collision-safe catalog key and the identity is the server-resolved
// identity of the repository that was published.
func createRepositoryResultDTO(rec clone.Record) CreateRepositoryResult {
	dto := CreateRepositoryResult{
		RepoKey: rec.Published.RepoKey,
		Path:    rec.Published.Path,
		HasHead: rec.Published.HasHead,
		Root:    rec.RootPath,
	}
	if rec.Published.Identity != nil {
		dto.Identity = &RepositoryIdentity{
			Path:      rec.Published.Identity.Path,
			CommonDir: rec.Published.Identity.CommonDir,
			Device:    rec.Published.Identity.Device,
			Inode:     rec.Published.Identity.Inode,
		}
	}
	return dto
}
