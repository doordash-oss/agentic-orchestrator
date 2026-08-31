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
	"os"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func (h *apiHandler) handleRecoveryRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if h.mutations == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "mutation service unavailable", nil)
		return
	}
	items, err := h.mutations.ScanRecovery(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "scan recovery", nil)
		return
	}
	dtoItems := make([]RecoveryItem, 0, len(items))
	for _, item := range items {
		dtoItems = append(dtoItems, recoveryItemDTO(item))
	}
	snapshotID := h.storeRecoverySnapshot(items, dtoItems)
	response := RecoverySnapshotResponse{
		APIVersion: APIVersion,
		Meta:       h.responseMeta(revisionForAny(dtoItems)),
		SnapshotID: snapshotID,
		Items:      dtoItems,
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *apiHandler) handleRecoveryActionRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req RecoveryActionRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	req.SnapshotID = strings.TrimSpace(req.SnapshotID)
	if req.SnapshotID == "" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "snapshot_id is required", nil)
		return
	}
	if len(req.Actions) == 0 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "actions are required", nil)
		return
	}
	items, ok := h.lookupRecoverySnapshot(req.SnapshotID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "recovery snapshot not found", nil)
		return
	}
	actions, err := decodeRecoveryActions(items, req.Actions)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", err.Error(), nil)
		return
	}
	resp, err := h.mutations.ExecuteRecovery(context.Background(), items, actions)
	if err != nil {
		writeMutationError(w, err)
		return
	}
	if resp.Result == "" {
		resp.Result = resultRecovered
	}
	writeActionJSON(w, http.StatusOK, &resp)
}

// handleRecoveryLogRoute serves GET /api/v1/recovery/logs — a bounded, redacted
// log slice for one recovery item, resolved through the snapshot identity and
// item key. The snapshot must be the latest scan (a new scan invalidates every
// prior snapshot id), the key must match an item in that snapshot, and the log
// path is never echoed back — only the bounded, sanitized text. Traversal and
// cross-item access are rejected: the key must equal an item's key exactly and
// the read target is the item's own log path, not a client-supplied path.
func (h *apiHandler) handleRecoveryLogRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	snapshotID := strings.TrimSpace(r.URL.Query().Get("snapshot_id"))
	itemKey := strings.TrimSpace(r.URL.Query().Get("key"))
	if snapshotID == "" || itemKey == "" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "snapshot_id and key are required", nil)
		return
	}
	if !safeRecoveryKey(itemKey) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid recovery key", nil)
		return
	}
	items, ok := h.lookupRecoverySnapshot(snapshotID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "recovery snapshot not found or stale", nil)
		return
	}
	var item ports.RecoveryItem
	found := false
	for _, candidate := range items {
		if ports.RecoveryActionKey(candidate.PIDFile.FeatureID, candidate.RepoName) == itemKey {
			item, found = candidate, true
			break
		}
	}
	if !found {
		writeAPIError(w, http.StatusNotFound, "not_found", "recovery item not found in snapshot", nil)
		return
	}
	logPath := strings.TrimSpace(item.PIDFile.LogPath)
	if logPath == "" {
		writeAPIError(w, http.StatusNotFound, "not_found", "log is not available for this item", nil)
		return
	}
	// The bounded-read/sanitize invariant is encoded once in writeTextFileSlice;
	// the item key is a safe, opaque identity (never a host path) and the log
	// text is sanitized so secrets and oversized content never reach the
	// client. The raw host path stays server-side.
	h.writeTextFileSlice(w, r, itemKey, logPath)
}

// safeRecoveryKey guards the recovery item key against path-traversal and
// control characters. A valid key is a feature id optionally joined to a repo
// name by a colon; both segments use the safe-action-token alphabet.
func safeRecoveryKey(key string) bool {
	if key == "" || len(key) > 512 || strings.Contains(key, "..") {
		return false
	}
	for _, part := range strings.Split(key, ":") {
		if !safeActionToken(part, false) {
			return false
		}
	}
	return true
}

const (
	recoveryActionKill = "kill"
	// recoveryActionSkip preserves the legacy spelling solely for fail-closed rejection tests.
	recoveryActionSkip = "skip"
)

func recoveryItemDTO(item ports.RecoveryItem) RecoveryItem {
	dto := RecoveryItem{
		Key:          ports.RecoveryActionKey(item.PIDFile.FeatureID, item.RepoName),
		FeatureID:    item.PIDFile.FeatureID,
		RepoName:     item.RepoName,
		Phase:        item.PIDFile.Phase,
		Iteration:    item.PIDFile.Iteration,
		PID:          item.PIDFile.PID,
		ProcessAlive: item.ProcessAlive,
		LogAvailable: recoveryLogAvailable(item),
	}
	if item.Feature != nil {
		dto.FeatureName = item.Feature.Name
	}
	dto.DefaultAction = actionResume
	dto.AllowedActions = []string{actionResume, recoveryActionKill}
	return dto
}

// recoveryLogAvailable reports whether a bounded log read is available for the
// item. A live or dead orphan session may carry a log path; the read endpoint
// revalidates existence at read time, but availability here lets the desktop
// surface the affordance only when a log exists.
func recoveryLogAvailable(item ports.RecoveryItem) bool {
	path := strings.TrimSpace(item.PIDFile.LogPath)
	if path == "" {
		return false
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return true
	}
	return false
}

func (h *apiHandler) storeRecoverySnapshot(items []ports.RecoveryItem, dtoItems []RecoveryItem) string {
	rawItems := append([]ports.RecoveryItem(nil), items...)
	snapshotID := revisionForAny(struct {
		GeneratedAt time.Time
		Items       []RecoveryItem
	}{
		GeneratedAt: time.Now().UTC(),
		Items:       dtoItems,
	})
	h.recoveryMu.Lock()
	defer h.recoveryMu.Unlock()
	h.recoverySnapshots = map[string][]ports.RecoveryItem{snapshotID: rawItems}
	return snapshotID
}

func (h *apiHandler) lookupRecoverySnapshot(snapshotID string) ([]ports.RecoveryItem, bool) {
	h.recoveryMu.Lock()
	defer h.recoveryMu.Unlock()
	items, ok := h.recoverySnapshots[snapshotID]
	if !ok {
		return nil, false
	}
	return append([]ports.RecoveryItem(nil), items...), true
}

func decodeRecoveryActions(items []ports.RecoveryItem, raw map[string]string) (map[string]ports.RecoveryAction, error) {
	allowed := make(map[string]map[string]ports.RecoveryAction, len(items))
	for _, item := range items {
		key := ports.RecoveryActionKey(item.PIDFile.FeatureID, item.RepoName)
		itemAllowed := map[string]ports.RecoveryAction{
			recoveryActionKill: ports.RecoveryKill,
			actionResume:       ports.RecoveryResume,
		}
		allowed[key] = itemAllowed
	}
	actions := make(map[string]ports.RecoveryAction, len(raw))
	for key, action := range raw {
		itemAllowed, ok := allowed[key]
		if !ok {
			return nil, errors.New("unknown recovery item " + key)
		}
		parsed, ok := itemAllowed[strings.ToLower(strings.TrimSpace(action))]
		if !ok {
			return nil, errors.New("invalid recovery action for " + key)
		}
		actions[key] = parsed
	}
	return actions, nil
}
