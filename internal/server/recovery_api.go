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
	"net/http"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func (h *apiHandler) handleRecoveryRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if h.mutations == nil || h.mutations.target == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "mutation service unavailable", nil)
		return
	}
	items, err := h.mutations.target.ScanRecovery(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "scan recovery", nil)
		return
	}
	dtoItems := make([]RecoveryItemDTO, 0, len(items))
	for _, item := range items {
		dtoItems = append(dtoItems, recoveryItemDTO(item))
	}
	snapshotID := h.storeRecoverySnapshot(items, dtoItems)
	response := RecoverySnapshotResponse{
		APIVersion: APIVersion,
		Meta:       responseMeta(revisionForAny(dtoItems)),
		SnapshotID: snapshotID,
		Items:      dtoItems,
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *apiHandler) handleRecoveryActionRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
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
	admission, accepted := h.mutations.admit("recovery.execute", OperationTarget{Type: "runtime"}, func() (OperationResult, error) {
		return h.mutations.target.ExecuteRecovery(context.Background(), items, actions)
	})
	h.writeMutationAdmission(w, admission, accepted)
}

func recoveryItemDTO(item ports.RecoveryItem) RecoveryItemDTO {
	dto := RecoveryItemDTO{
		Key:          ports.RecoveryActionKey(item.PIDFile.FeatureID, item.RepoName),
		FeatureID:    item.PIDFile.FeatureID,
		RepoName:     item.RepoName,
		Phase:        item.PIDFile.Phase,
		Iteration:    item.PIDFile.Iteration,
		PID:          item.PIDFile.PID,
		ProcessAlive: item.ProcessAlive,
		Tweak:        ports.IsRecoveryTweakSession(item),
	}
	if item.Feature != nil {
		dto.FeatureName = item.Feature.Name
	}
	if dto.Tweak {
		dto.DefaultAction = "kill"
		dto.AllowedActions = []string{"kill"}
		return dto
	}
	dto.DefaultAction = "skip"
	dto.AllowedActions = []string{"resume", "kill", "skip"}
	return dto
}

func (h *apiHandler) storeRecoverySnapshot(items []ports.RecoveryItem, dtoItems []RecoveryItemDTO) string {
	rawItems := append([]ports.RecoveryItem(nil), items...)
	snapshotID := revisionForAny(struct {
		GeneratedAt time.Time
		Items       []RecoveryItemDTO
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
		itemAllowed := map[string]ports.RecoveryAction{"kill": ports.RecoveryKill}
		if !ports.IsRecoveryTweakSession(item) {
			itemAllowed["resume"] = ports.RecoveryResume
			itemAllowed["skip"] = ports.RecoverySkip
		}
		allowed[key] = itemAllowed
	}
	actions := make(map[string]ports.RecoveryAction, len(raw))
	for key, action := range raw {
		itemAllowed, ok := allowed[key]
		if !ok {
			return nil, errBadRecoveryAction("unknown recovery item " + key)
		}
		parsed, ok := itemAllowed[strings.ToLower(strings.TrimSpace(action))]
		if !ok {
			return nil, errBadRecoveryAction("invalid recovery action for " + key)
		}
		actions[key] = parsed
	}
	return actions, nil
}

type errBadRecoveryAction string

func (e errBadRecoveryAction) Error() string { return string(e) }
