package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func (h *apiHandler) handleResourceRoutes(w http.ResponseWriter, r *http.Request) {
	escaped := r.URL.EscapedPath()
	if !strings.HasPrefix(escaped, apiPathResources) {
		writeAPIError(w, http.StatusNotFound, "not_found", "resource not found", nil)
		return
	}

	rest := strings.TrimPrefix(escaped, apiPathResources)
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
			return
		}
		h.handleResourceCatalogue(w, r)
		return
	}

	parts := splitPath(rest)
	if len(parts) == 0 || !validEntityID(parts[0]) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid resource id", nil)
		return
	}
	resourceID := parts[0]

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			h.handleResourceRead(w, r, resourceID)
		case http.MethodPut:
			h.handleResourceWrite(w, r, resourceID)
		default:
			w.Header().Set("Allow", strings.Join([]string{http.MethodGet, http.MethodPut}, ", "))
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		}
		return
	}

	if len(parts) == 2 && parts[1] == "validate" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
			return
		}
		h.handleResourceValidate(w, r, resourceID)
		return
	}

	writeAPIError(w, http.StatusNotFound, "not_found", "resource endpoint not found", nil)
}

func (h *apiHandler) handleResourceCatalogue(w http.ResponseWriter, r *http.Request) {
	kindFilter := r.URL.Query().Get("kind")
	resp, err := h.resourceSvc.Catalogue(kindFilter)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "resource catalogue failed", nil)
		return
	}
	revision := revisionForResourceCatalogue(resp)
	h.writeRevisionedJSON(w, r, revision, resp)
}

func (h *apiHandler) handleResourceRead(w http.ResponseWriter, r *http.Request, resourceID string) {
	resp, err := h.resourceSvc.Read(resourceID)
	if err != nil {
		writeResourceError(w, err)
		return
	}
	h.writeRevisionedJSON(w, r, resp.Revision, resp)
}

func (h *apiHandler) handleResourceValidate(w http.ResponseWriter, r *http.Request, resourceID string) {
	if !h.requireTrustedJSONMutation(w, r) {
		return
	}
	var req ResourceValidateRequest
	if !decodeResourceJSON(w, r, &req) {
		return
	}
	resp, err := h.resourceSvc.Validate(resourceID, req.Text)
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

func (h *apiHandler) handleResourceWrite(w http.ResponseWriter, r *http.Request, resourceID string) {
	if !h.requireTrustedJSONMutation(w, r) {
		return
	}
	var req ResourceWriteRequest
	if !decodeResourceJSON(w, r, &req) {
		return
	}
	resp, err := h.resourceSvc.Write(resourceID, req.BaseRevision, req.Text)
	if err != nil {
		writeResourceError(w, err)
		return
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

func decodeResourceJSON(w http.ResponseWriter, r *http.Request, out interface{}) bool {
	limited := http.MaxBytesReader(w, r.Body, maxResourceBytes+4096)
	defer limited.Close()
	dec := json.NewDecoder(limited)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid JSON request", nil)
		return false
	}
	return true
}

func writeResourceError(w http.ResponseWriter, err error) {
	if err == nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "resource operation failed", nil)
		return
	}
	var notFound *ResourceNotFoundError
	if errors.As(err, &notFound) {
		writeAPIError(w, http.StatusNotFound, "not_found", notFound.Error(), nil)
		return
	}
	var validationErr *ValidationFailedError
	if errors.As(err, &validationErr) {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", validationErr.Error(), map[string]any{"findings": validationErr.Findings})
		return
	}
	var conflict *ActionConflictError
	if errors.As(err, &conflict) {
		writeAPIError(w, http.StatusConflict, errCodeConflict, conflict.Error(), conflict.Target)
		return
	}
	writeAPIError(w, http.StatusBadRequest, "bad_request", err.Error(), nil)
}

func revisionForResourceCatalogue(resp ResourceCatalogResponse) string {
	data, _ := json.Marshal(resp.Resources)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:12])
}
