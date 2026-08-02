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
)

// handleRebasePreflight serves GET
// /api/v1/features/{feature_id}/rebase/preflight — a read-only, server-authored
// preview of a feature-wide rebase. It enumerates every affected repository,
// its target branch, freshness, behind state, known blocker, and the
// authoritative source revision execution will use — all without fetching,
// rebasing, or touching any external system. The desktop never reproduces the
// rebase-choice matrix and cannot submit a stale snapshot: execution rejects a
// mismatched source_revision before any side effect.
func (h *apiHandler) handleRebasePreflight(w http.ResponseWriter, r *http.Request, featureID string) {
	if h.mutations == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "mutation service unavailable", nil)
		return
	}
	resp, err := h.mutations.PreflightRebase(featureID)
	if err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	if resp.APIVersion == "" {
		resp.APIVersion = APIVersion
	}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}
