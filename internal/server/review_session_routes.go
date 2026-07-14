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
	"os"
	"strings"
)

func (h *apiHandler) handleReviewSessionRoute(w http.ResponseWriter, r *http.Request, featureID string, parts []string) {
	switch {
	case len(parts) == 0:
		h.handleCreateReviewSession(w, r, featureID)
	case len(parts) == 2:
		if !validEntityID(parts[0]) {
			writeAPIError(w, http.StatusBadRequest, errCodeBadRequest, "invalid review id", map[string]any{"feature_id": featureID})
			return
		}
		switch parts[1] {
		case "draft":
			h.handleSaveReviewDraft(w, r, featureID, parts[0])
		case "decision":
			h.handleSubmitReviewSessionDecision(w, r, featureID, parts[0])
		default:
			writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found", nil)
		}
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found", nil)
	}
}

func (h *apiHandler) handleCreateReviewSession(w http.ResponseWriter, r *http.Request, featureID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
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
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
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

func (h *apiHandler) handleSubmitReviewSessionDecision(w http.ResponseWriter, r *http.Request, featureID, reviewID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
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
		writeAPIError(w, http.StatusForbidden, "forbidden", "trusted local client header is required", nil)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && !isLoopbackOrigin(origin) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "browser origin is not trusted", nil)
		return false
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "application/json") {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "JSON body is required", nil)
		return false
	}
	if r.ContentLength > MaxMutationBodyBytes {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "mutation body is too large", nil)
		return false
	}
	return true
}

func writeReviewSessionError(w http.ResponseWriter, err error, featureID, reviewID string) {
	if err == nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "review session failed", nil)
		return
	}
	var conflict *ActionConflictError
	if errors.As(err, &conflict) {
		writeMutationError(w, err)
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		target := map[string]any{"feature_id": featureID}
		if reviewID != "" {
			target["review_id"] = reviewID
		}
		writeAPIError(w, http.StatusNotFound, "not_found", "review session not found", target)
		return
	}
	writeMutationError(w, err)
}
