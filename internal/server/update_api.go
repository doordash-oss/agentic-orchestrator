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

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
)

// handleUpdateRoute serves the authenticated, metadata-only availability
// snapshot. It never triggers a check and never mutates anything.
func (h *apiHandler) handleUpdateRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if h.updates == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	h.writeUpdateSnapshot(w, r, http.StatusOK)
}

// handleUpdateCheckRoute accepts one explicit release-availability check.
// The accepted check runs asynchronously tied to the runtime lifetime: the
// response returns promptly with the current snapshot, concurrent requests
// coalesce into one metadata worker, and a disconnecting caller never cancels
// accepted work. Off and unsupported installations are refused with 409
// without any feed traffic; a manual check inside a server-imposed retry
// deadline is refused with 429 and a retry hint, also without a request.
func (h *apiHandler) handleUpdateCheckRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if h.updates == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	// Strict empty-object decode: unknown fields reject with 400, oversized
	// or non-JSON bodies with 413/400 through the shared mutation decoder.
	var body struct{}
	if !decodeMutationJSON(w, r, &body) {
		return
	}
	opts := h.updates.options()
	switch {
	case opts.Policy == selfupdate.PolicyOff:
		// Disabled policy refuses every update mutation with 403 and the
		// disabled-policy remediation.
		writeAPIError(w, http.StatusForbidden, errcat.Forbidden,
			errcat.WithDiagnostics("updates are disabled by this server's startup policy"),
			updatesDisabledRemediation())
		return
	case !opts.Eligibility.Supported:
		writeAPIError(w, http.StatusConflict, errcat.UpdateUnsupportedInstall,
			errcat.WithParams(errcat.UpdateUnsupportedInstallParams{
				Reason:      string(opts.Eligibility.Reason),
				Remediation: opts.Eligibility.Remediation,
			}),
			errcat.WithRemediationHint(opts.Eligibility.Remediation))
		return
	case opts.Feed == nil:
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	if refusal := h.updates.retryDeadlineRefusal(); refusal != nil {
		writeJSON(w, http.StatusTooManyRequests, ErrorResponse{
			APIVersion: APIVersion,
			Error:      wireError(*refusal),
		})
		return
	}
	h.updates.requestCheck()
	h.writeUpdateSnapshot(w, r, http.StatusAccepted)
}

func (h *apiHandler) writeUpdateSnapshot(w http.ResponseWriter, r *http.Request, status int) {
	snapshot := h.updates.Snapshot()
	revision := revisionForAny(snapshot)
	response := UpdateSnapshotResponse{
		APIVersion: APIVersion,
		Meta:       h.responseMeta(revision),
		Update:     snapshot,
	}
	h.setSequenceHeader(w)
	w.Header().Set("ETag", `"`+revision+`"`)
	// Conditional-GET handling applies to reads only: a mutation response
	// (check 202, install 202, cancel 200) can never accidentally become
	// 304 through a carried If-None-Match header.
	if r.Method == http.MethodGet && revisionMatches(r, revision) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, status, response)
}

// publishUpdateEvent fires the update.updated runtime invalidation through
// the existing epoch/sequence conventions; clients re-GET the snapshot.
func (h *apiHandler) publishUpdateEvent() {
	if h.broker == nil {
		return
	}
	h.broker.publish(snapshotRequiredEventDTO(sseEventUpdateUpdated, Resource{
		Type: resourceTypeUpdate,
	}))
}
