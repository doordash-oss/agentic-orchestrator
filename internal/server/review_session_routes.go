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
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

func (h *apiHandler) handleReviewSessionRoute(w http.ResponseWriter, r *http.Request, featureID string, parts []string) {
	switch {
	case len(parts) == 0:
		if r.Method == http.MethodGet {
			h.handleReadReviewSession(w, r, featureID)
			return
		}
		h.handleCreateReviewSession(w, r, featureID)
	case len(parts) == 2:
		if !validEntityID(parts[0]) {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics(fmt.Sprintf("invalid review id for feature %q", featureID)))
			return
		}
		switch parts[1] {
		case "draft":
			h.handleSaveReviewDraft(w, r, featureID, parts[0])
		case "validate":
			h.handleValidateReviewDraft(w, r, featureID, parts[0])
		case "decision":
			h.handleSubmitReviewSessionDecision(w, r, featureID, parts[0])
		default:
			writeAPIError(w, http.StatusNotFound, errcat.NotFound, errcat.WithParams(errcat.SubjectParams{Subject: "Endpoint"}))
		}
	default:
		writeAPIError(w, http.StatusNotFound, errcat.NotFound, errcat.WithParams(errcat.SubjectParams{Subject: "Endpoint"}))
	}
}

func (h *apiHandler) handleReadReviewSession(w http.ResponseWriter, r *http.Request, featureID string) {
	resp, err := h.reviewSessionService().Read(featureID)
	if err != nil {
		writeReviewSessionError(w, err, featureID, "")
		return
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) handleCreateReviewSession(w http.ResponseWriter, r *http.Request, featureID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
		return
	}
	if !h.requireTrustedJSONMutation(w, r) {
		return
	}
	var req map[string]any
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	resp, err := h.reviewSessionService().Create(featureID)
	if err != nil {
		writeReviewSessionError(w, err, featureID, "")
		return
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) handleSaveReviewDraft(w http.ResponseWriter, r *http.Request, featureID, reviewID string) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
		return
	}
	if !h.requireTrustedJSONMutation(w, r) {
		return
	}
	var req ReviewDraftUpdateRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	resp, err := h.reviewSessionService().SaveDraft(featureID, reviewID, req)
	if err != nil {
		writeReviewSessionError(w, err, featureID, reviewID)
		return
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) handleValidateReviewDraft(w http.ResponseWriter, r *http.Request, featureID, reviewID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
		return
	}
	if !h.requireTrustedJSONMutation(w, r) {
		return
	}
	var req ReviewDraftValidationRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	resp, err := h.reviewSessionService().ValidateDraft(featureID, reviewID, req)
	if err != nil {
		writeReviewSessionError(w, err, featureID, reviewID)
		return
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) handleSubmitReviewSessionDecision(w http.ResponseWriter, r *http.Request, featureID, reviewID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
		return
	}
	if !h.requireTrustedJSONMutation(w, r) {
		return
	}
	var req ReviewSessionDecisionRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	resp, err := h.reviewSessionService().SubmitDecision(featureID, reviewID, req)
	if err != nil {
		writeReviewSessionError(w, err, featureID, reviewID)
		return
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) reviewSessionService() *reviewSessionService {
	var decider reviewDecisionFunc
	if h != nil && h.mutations != nil {
		decider = h.mutations.ReviewDecision
	}
	if h == nil {
		return newReviewSessionService(nil, decider)
	}
	return newReviewSessionService(h.store, decider, h.reviewSessionLocks)
}

func (h *apiHandler) requireTrustedJSONMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Agentico-Client") != trustedClientHeaderValue {
		writeAPIError(w, http.StatusForbidden, errcat.Forbidden, errcat.WithDiagnostics("trusted local client header is required"))
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && !isLoopbackOrigin(origin) {
		writeAPIError(w, http.StatusForbidden, errcat.Forbidden, errcat.WithDiagnostics("browser origin is not trusted"))
		return false
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "application/json") {
		writeAPIError(w, http.StatusUnsupportedMediaType, errcat.UnsupportedMediaType, errcat.WithDiagnostics("JSON body is required"))
		return false
	}
	if r.ContentLength > MaxMutationBodyBytes {
		writeAPIError(w, http.StatusRequestEntityTooLarge, errcat.RequestTooLarge, errcat.WithDiagnostics("mutation body is too large"))
		return false
	}
	return true
}

func writeReviewSessionError(w http.ResponseWriter, err error, featureID, reviewID string) {
	if err == nil {
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError, errcat.WithDiagnostics("review session failed"))
		return
	}
	var conflict *ActionConflictError
	if errors.As(err, &conflict) {
		writeMutationError(w, err)
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		params := errcat.SubjectParams{Subject: "Review session"}
		if reviewID != "" {
			params.Name = reviewID
		}
		writeAPIError(w, http.StatusNotFound, errcat.NotFound, errcat.WithParams(params))
		return
	}
	writeMutationError(w, err)
}
