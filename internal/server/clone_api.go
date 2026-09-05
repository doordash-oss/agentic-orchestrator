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
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/clone"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

// apiPathWorkspaceClone is the clone operation route prefix: POST starts a
// clone, GET lists operations, and sub-routes address one operation.
const apiPathWorkspaceClone = "/api/v1/workspace/repositories/clone"

// CloneService is the server-side clone lifecycle boundary. Handlers stay
// thin: validation, durable state and process control live in the service;
// every response is an authoritative snapshot.
type CloneService interface {
	Start(ctx context.Context, input clone.StartInput) (clone.Record, error)
	Snapshot(id string) (clone.Record, error)
	List(q clone.ListQuery) (clone.ListResult, error)
	Cancel(id string) (clone.Record, error)
	Cleanup(id string) (clone.Record, error)
	Retry(id string) (clone.Record, error)
	Recover() error
	Shutdown(ctx context.Context) error
	Sweep() []string
}

// handleWorkspaceCloneRoute serves POST (start) and GET (list) on the
// clone route root.
func (h *apiHandler) handleWorkspaceCloneRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.handleWorkspaceCloneStart(w, r)
	case http.MethodGet:
		h.handleWorkspaceCloneList(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
	}
}

// handleWorkspaceCloneOperationRoutes serves per-operation sub-routes:
// GET /{id}, POST /{id}/cancel, POST /{id}/cleanup, POST /{id}/retry.
func (h *apiHandler) handleWorkspaceCloneOperationRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, apiPathWorkspaceClone+"/"))
	if invalidPathParts(parts) || len(parts) == 0 || !validEntityID(parts[0]) {
		writeAPIError(w, http.StatusNotFound, errcat.CloneOperationNotFound)
		return
	}
	id := parts[0]
	if h.clones == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	if len(parts) == 1 {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		rec, err := h.clones.Snapshot(id)
		if !h.writeCloneError(w, err) {
			return
		}
		resp := CloneOperationResponse{APIVersion: APIVersion, Operation: cloneOperationDTO(rec)}
		h.writeRevisionedJSON(w, r, revisionForAny(resp), &resp)
		return
	}
	if len(parts) != 2 {
		writeAPIError(w, http.StatusNotFound, errcat.CloneOperationNotFound)
		return
	}
	switch parts[1] {
	case "cancel", "cleanup", "retry":
	default:
		writeAPIError(w, http.StatusNotFound, errcat.CloneOperationNotFound)
		return
	}
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var rec clone.Record
	var err error
	result := ""
	switch parts[1] {
	case "cancel":
		rec, err = h.clones.Cancel(id)
		result = "cancellation_requested"
	case "cleanup":
		rec, err = h.clones.Cleanup(id)
		result = "cleanup_retried"
	case "retry":
		rec, err = h.clones.Retry(id)
		result = "retry_started"
	}
	if !h.writeCloneError(w, err) {
		return
	}
	status := http.StatusOK
	if result == "retry_started" {
		status = http.StatusAccepted
	}
	resp := CloneActionResponse{Result: result, Operation: cloneOperationDTO(rec)}
	writeActionJSON(w, status, &resp)
}

// handleWorkspaceCloneStart serves the trusted start mutation.
func (h *apiHandler) handleWorkspaceCloneStart(w http.ResponseWriter, r *http.Request) {
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
	var req CloneStartSchema
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	input := clone.StartInput{
		Remote:         req.RemoteURL,
		Root:           req.RootPath,
		Destination:    req.Destination,
		IdempotencyKey: req.IdempotencyKey,
	}
	rec, err := h.clones.Start(r.Context(), input)
	if !h.writeCloneError(w, err) {
		return
	}
	resp := CloneActionResponse{Result: "accepted", Operation: cloneOperationDTO(rec)}
	writeActionJSON(w, http.StatusAccepted, &resp)
}

// handleWorkspaceCloneList serves the bounded listing endpoint.
func (h *apiHandler) handleWorkspaceCloneList(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if h.clones == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	query := r.URL.Query()
	limit := clone.DefaultListLimit
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > clone.MaxListLimit {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("limit must be an integer between 1 and 200"))
			return
		}
		limit = parsed
	}
	after := query.Get("after")
	if len(after) > 256 {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("after token is too long"))
		return
	}
	result, err := h.clones.List(clone.ListQuery{Limit: limit, After: after})
	if !h.writeCloneError(w, err) {
		return
	}
	operations := make([]CloneOperation, 0, len(result.Operations))
	for _, rec := range result.Operations {
		operations = append(operations, cloneOperationDTO(rec))
	}
	resp := CloneOperationListResponse{
		APIVersion:    APIVersion,
		Operations:    operations,
		NextPageToken: &result.NextToken,
	}
	h.writeRevisionedJSON(w, r, revisionForAny(resp), &resp)
}

// cloneOperationDTO projects a durable record onto the API model. The
// staging path and process identity are deliberately not exposed.
func cloneOperationDTO(rec clone.Record) CloneOperation {
	dto := CloneOperation{
		ID:              rec.ID,
		State:           CloneOperationState(rec.State),
		Stage:           rec.Stage,
		Progress:        rec.Progress,
		RemoteURL:       rec.RemoteURL,
		RootPath:        rec.RootPath,
		Destination:     rec.Destination,
		DestinationPath: rec.DestinationPath,
		IdempotencyKey:  rec.IdempotencyKey,
		CancelRequested: rec.CancelRequested,
		CleanupIssue:    rec.CleanupIssue,
		CreatedAt:       rec.CreatedAt,
		UpdatedAt:       rec.UpdatedAt,
	}
	if rec.PendingOutcome != "" {
		dto.PendingOutcome = CloneOperationPendingOutcome(rec.PendingOutcome)
	}
	if rec.CancelRequestedAt != (time.Time{}) {
		at := rec.CancelRequestedAt
		dto.CancelRequestedAt = &at
	}
	if rec.TerminalAt != (time.Time{}) {
		at := rec.TerminalAt
		dto.TerminalAt = &at
	}
	if rec.ResolvedAt != (time.Time{}) {
		at := rec.ResolvedAt
		dto.ResolvedAt = &at
	}
	if rec.Error != nil {
		wire := errcat.New(errcat.Code(rec.Error.Code), errcat.WithDiagnostics(rec.Error.Diagnostics))
		wireValue := wireError(wire)
		dto.Error = &wireValue
	}
	if rec.Published != nil {
		dto.Published = &ClonePublication{
			RepoKey:     rec.Published.RepoKey,
			Path:        rec.Published.Path,
			HasHead:     rec.Published.HasHead,
			PublishedAt: rec.Published.PublishedAt,
		}
		if rec.Published.Identity != nil {
			dto.Published.Identity = &RepositoryIdentity{
				Path:      rec.Published.Identity.Path,
				CommonDir: rec.Published.Identity.CommonDir,
				Device:    rec.Published.Identity.Device,
				Inode:     rec.Published.Identity.Inode,
			}
		}
	}
	return dto
}

// cloneErrorStatus maps service error codes onto HTTP statuses; the code
// itself maps onto the canonical error catalog.
func cloneErrcatCode(code string) (errcat.Code, int) {
	switch code {
	case clone.CodeNotFound:
		return errcat.CloneOperationNotFound, http.StatusNotFound
	case clone.CodeHistoryUnavailable:
		return errcat.CloneHistoryUnavailable, http.StatusNotFound
	case clone.CodeIdempotencyConflict:
		return errcat.CloneIdempotencyConflict, http.StatusConflict
	case clone.CodeDestinationReserved:
		return errcat.CloneDestinationReserved, http.StatusConflict
	case clone.CodeDestinationExists:
		return errcat.CloneDestinationExists, http.StatusConflict
	case clone.CodeDestinationShadowed:
		return errcat.CloneDestinationShadowed, http.StatusConflict
	case clone.ValidationRemoteInvalid, clone.ValidationRemoteTooLong,
		clone.ValidationRemoteCredentials, clone.ValidationRemoteTransport,
		clone.ValidationRemoteOptionLike:
		return errcat.CloneRemoteInvalid, http.StatusBadRequest
	case clone.ValidationDestinationInvalid, clone.ValidationDestinationTooLong,
		clone.ValidationDestinationReserved:
		return errcat.CloneDestinationInvalid, http.StatusBadRequest
	case clone.CodeRootIneligible:
		return errcat.CloneRootIneligible, http.StatusBadRequest
	case clone.CodeNotRetryable:
		return errcat.CloneNotRetryable, http.StatusConflict
	case clone.CodeUnavailable:
		return errcat.CloneUnavailable, http.StatusServiceUnavailable
	default:
		return errcat.InternalError, http.StatusInternalServerError
	}
}

// writeCloneError renders a service error or reports success.
func (h *apiHandler) writeCloneError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	var serr *clone.ServiceError
	if !errors.As(err, &serr) {
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError,
			errcat.WithDiagnostics("clone service"))
		return false
	}
	code, status := cloneErrcatCode(serr.Code)
	diagnostics := strings.TrimSpace(serr.Detail)
	if diagnostics == "" {
		writeAPIError(w, status, code)
		return false
	}
	writeAPIError(w, status, code, errcat.WithDiagnostics(clone.RedactDiagnostics(diagnostics)))
	return false
}

// publishCloneEvent fires the snapshot-required SSE invalidation for one
// clone operation. Events identify changes; snapshots stay authoritative.
func (h *apiHandler) publishCloneEvent(operationID string) {
	if h.broker == nil {
		return
	}
	h.broker.publish(snapshotRequiredEventDTO(sseEventCloneUpdated, Resource{
		Type: resourceTypeCloneOperation,
		ID:   operationID,
	}))
}

// publishCloneWorkspaceEvent fires the runtime invalidation after a
// successful publication changed workspace discovery.
func (h *apiHandler) publishCloneWorkspaceEvent() {
	if h.broker == nil {
		return
	}
	h.broker.publish(snapshotRequiredEventDTO(sseEventConfigUpdated, Resource{Type: resourceTypeRuntime}))
}
