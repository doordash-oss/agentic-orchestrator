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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// handleRewindPreview serves GET
// /api/v1/features/{feature_id}/rewind/preview — a read-only, server-authored
// preview of a rewind. It accepts the proposed phase, optional roadmap phase,
// and optional pipeline upgrade, and returns the authoritative source run and
// revision, valid hierarchical options, effective target after escalation,
// carry-forward set/provenance, PR and worktree consequences, backup behavior,
// and validation findings — all without mutating, sealing, forking, or touching
// any external system. The desktop never reproduces the rewind-choice matrix.
func (h *apiHandler) handleRewindPreview(w http.ResponseWriter, r *http.Request, featureID string) {
	f, err := h.loadFeature(featureID)
	if err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	targetRaw := strings.TrimSpace(r.URL.Query().Get("target_phase"))
	if targetRaw == "" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "target_phase is required", map[string]any{"feature_id": featureID})
		return
	}
	targetPhase, ok := parsePhaseDirName(targetRaw)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid target_phase", map[string]any{"feature_id": featureID, "target_phase": targetRaw})
		return
	}
	request := feature.RewindRequest{TargetPhase: targetPhase}
	if raw := r.URL.Query().Get("roadmap_phase"); raw != "" {
		rp, ok := parseIntQuery(r, "roadmap_phase", 0)
		if !ok || rp < 1 {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid roadmap_phase", map[string]any{"feature_id": featureID})
			return
		}
		request.RoadmapPhase = rp
	}
	var upgrade feature.PipelineProfile
	if up := strings.TrimSpace(r.URL.Query().Get("upgrade_pipeline")); up != "" {
		upgrade = feature.PipelineProfile(up)
	}

	sealedRunDir := ""
	if h.store != nil {
		sealedRunDir = h.store.RunDir(featureID, f.ActiveRun)
	}
	result := feature.RewindPreviewForFeature(f, sealedRunDir, request, upgrade)

	resp := rewindPreviewResponseFromResult(result)
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

// rewindPreviewResponseFromResult maps the feature-layer preview result into
// the generated response DTO. Phase/profile values are serialized as dir/name
// strings; the source revision is carried verbatim so execution can detect a
// stale preview.
func rewindPreviewResponseFromResult(result feature.RewindPreviewResult) RewindPreviewResponse {
	resp := RewindPreviewResponse{
		APIVersion:         APIVersion,
		Eligible:           result.Eligible,
		SourceRunNumber:    result.SourceRunNumber,
		SourceRevision:     result.SourceRevision,
		TargetPhase:        phaseDirNameOrEmpty(result.TargetPhase),
		EffectivePhase:     phaseDirNameOrEmpty(result.EffectivePhase),
		RoadmapPhase:       result.RoadmapPhase,
		UpgradePipeline:    string(result.UpgradePipeline),
		CarriedPhases:      append([]string(nil), result.CarriedPhases...),
		CarriedFromRun:     result.CarriedFromRun,
		BackupBranchRepos:  append([]string(nil), result.BackupBranchRepos...),
		ValidRoadmapPhases: append([]int(nil), result.ValidRoadmapPhases...),
		ValidationFindings: append([]string(nil), result.ValidationFindings...),
	}
	for _, opt := range result.UpgradePipelineOptions {
		resp.UpgradePipelineOptions = append(resp.UpgradePipelineOptions, string(opt))
	}
	for _, c := range result.ValidPhases {
		resp.ValidPhases = append(resp.ValidPhases, RewindChoice{
			Phase:         c.Phase.DirName(),
			EscalatesTo:   string(c.EscalatesTo),
			OverridePhase: phaseDirNameOrEmpty(c.OverridePhase),
		})
	}
	for _, p := range result.PRConsequences {
		resp.PrConsequences = append(resp.PrConsequences, RewindPRConsequence{Repo: p.Repo, PrURL: p.PRURL})
	}
	for _, wc := range result.WorktreeConsequences {
		resp.WorktreeConsequences = append(resp.WorktreeConsequences, RewindWorktreeConsequence{
			Repo:      wc.Repo,
			ResetKind: RewindWorktreeConsequenceResetKind(wc.ResetKind),
		})
	}
	return resp
}

func phaseDirNameOrEmpty(p feature.Phase) string {
	if p == 0 {
		return ""
	}
	return p.DirName()
}

func parsePhaseDirName(name string) (feature.Phase, bool) {
	return feature.ParsePhaseDirName(name)
}
