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
	"sort"
	"strconv"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

const (
	defaultRunListPageSize = 20
	maxRunListPageSize     = 100
)

// handleRunList serves GET /api/v1/features/{feature_id}/runs — a bounded,
// newest-first, paginated catalogue of every run on disk for a feature. It
// enumerates run directories (not ActiveRun/RunCount) so it survives
// active-run gaps left by crash recovery and reports run numbers above 999
// without lexicographic truncation. Each entry is built from that run's own
// run.yaml so no historical field is borrowed from the active run.
func (h *apiHandler) handleRunList(w http.ResponseWriter, r *http.Request, featureID string) {
	if _, err := h.loadFeature(featureID); err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	page, ok := parseIntQuery(r, "page", 1)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid page", map[string]any{"feature_id": featureID})
		return
	}
	pageSize, ok := parseIntQuery(r, "page_size", defaultRunListPageSize)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid page_size", map[string]any{"feature_id": featureID})
		return
	}
	if page < 1 || pageSize < 1 || pageSize > maxRunListPageSize {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid pagination bounds", map[string]any{"feature_id": featureID})
		return
	}
	runNumbers, err := h.store.ListRuns(featureID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "list runs", map[string]any{"feature_id": featureID})
		return
	}
	total := len(runNumbers)
	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	if page > totalPages && totalPages > 0 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "page out of range", map[string]any{"feature_id": featureID})
		return
	}
	// Newest first: walk the ascending enumeration backwards, then slice.
	start := (page - 1) * pageSize
	end := start + pageSize
	if end > total {
		end = total
	}
	pageNumbers := make([]int, 0, end-start)
	for i := len(runNumbers) - 1 - start; i >= 0 && len(pageNumbers) < (end-start); i-- {
		pageNumbers = append(pageNumbers, runNumbers[i])
	}
	runs := make([]RunSummaryDTO, 0, len(pageNumbers))
	for _, n := range pageNumbers {
		run, err := h.store.LoadRun(featureID, n)
		if err != nil {
			// A run vanished between enumeration and load (recovery gap).
			// Skip it rather than fabricating or crashing the page.
			continue
		}
		runs = append(runs, runSummaryFromRun(run))
	}
	resp := RunListResponse{
		APIVersion: APIVersion,
		Page:       page,
		PageSize:   pageSize,
		Runs:       runs,
		Total:      total,
		TotalPages: totalPages,
	}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

// handleRunDetail serves GET /api/v1/features/{feature_id}/runs/{run_number}
// — the run-authentic detail for one run, including seal metadata,
// carry-forward provenance, redacted backup-branch repo names, and run-level
// timing/cost. No field is borrowed from the active run.
func (h *apiHandler) handleRunDetail(w http.ResponseWriter, r *http.Request, featureID string, runNumber int) {
	_, run, err := h.featureAndRun(featureID, runNumber)
	if err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	detail := runDetailFromRun(run)
	detail.Cost = h.runCostWithActiveSessions(featureID, run)
	revision := revisionForAny(detail)
	h.writeRevisionedJSON(w, r, revision, RunDetailResponse{
		APIVersion: APIVersion,
		Meta:       h.responseMeta(revision),
		Run:        detail,
	})
}

// handleRunSessions serves GET
// /api/v1/features/{feature_id}/runs/{run_number}/sessions — the sessions
// belonging to one run, filtered by feature and run number. Historical
// sessions are listed via bounded reads only; this endpoint never opens a
// live output subscription.
func (h *apiHandler) handleRunSessions(w http.ResponseWriter, r *http.Request, featureID string, runNumber int) {
	if _, err := h.loadFeature(featureID); err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	sessions := h.allSessions()
	summaries := make([]SessionSummaryDTO, 0, len(sessions))
	for _, sess := range sessions {
		if sess == nil || sess.FeatureID() != featureID {
			continue
		}
		summary := sessionSummaryDTO(sess)
		if summary.RunNumber != runNumber {
			continue
		}
		summaries = append(summaries, summary)
	}
	revision := revisionForAny(summaries)
	h.writeRevisionedJSON(w, r, revision, RunSessionListResponse{
		APIVersion: APIVersion,
		Meta:       h.responseMeta(revision),
		RunNumber:  runNumber,
		Sessions:   summaries,
	})
}

// runSummaryFromRun builds a RunSummaryDTO from run-level fields only. The
// current_phase label is derived from the run's pending review phase (if any)
// rather than the live feature, so a historical run never borrows the active
// run's phase.
func runSummaryFromRun(run *feature.Run) RunSummaryDTO {
	if run == nil {
		return RunSummaryDTO{}
	}
	dto := RunSummaryDTO{
		RunNumber:       run.RunNumber,
		StartedAt:       run.StartedAt,
		SealedAt:        run.SealedAt,
		SealReason:      string(run.SealReason),
		CurrentPhase:    runPhaseLabel(run),
		PhaseStatus:     run.CurrentPhaseStatus,
		Iteration:       run.CurrentIteration,
		RoadmapPhase:    run.CurrentRoadmapPhase,
		RoadmapTotal:    run.TotalRoadmapPhases,
		ArtifactCount:   len(run.Artifacts),
		HasNeedUserGate: run.PendingNeedUserInputPath != "",
		Setup:           setupDTO(run.Setup),
	}
	if run.PendingReviewPhase != nil {
		dto.PendingReviewPhase = run.PendingReviewPhase.DirName()
	}
	if run.PendingRewindReviewRoadmapPhase != nil {
		dto.PendingRewindReviewRoadmapPhase = *run.PendingRewindReviewRoadmapPhase
	}
	dto.IsRewind = run.IsRewind
	return dto
}

// runDetailFromRun extends the run-authentic summary with seal/provenance
// fields, redacted backup-branch repo names, and run-level timing/cost.
func runDetailFromRun(run *feature.Run) RunDetail {
	summary := runSummaryFromRun(run)
	dto := RunDetail{
		ArtifactCount:                   summary.ArtifactCount,
		CurrentPhase:                    summary.CurrentPhase,
		HasNeedUserGate:                 summary.HasNeedUserGate,
		IsRewind:                        summary.IsRewind,
		Iteration:                       summary.Iteration,
		PendingReviewPhase:              summary.PendingReviewPhase,
		PendingRewindReviewRoadmapPhase: summary.PendingRewindReviewRoadmapPhase,
		PhaseStatus:                     summary.PhaseStatus,
		RoadmapPhase:                    summary.RoadmapPhase,
		RoadmapTotal:                    summary.RoadmapTotal,
		RunNumber:                       summary.RunNumber,
		SealReason:                      summary.SealReason,
		SealedAt:                        summary.SealedAt,
		Setup:                           summary.Setup,
		StartedAt:                       summary.StartedAt,
		Timing:                          runTimingDTO(run),
		Cost:                            runCostDTO(run),
	}
	if run != nil {
		dto.Committing = run.Committing
		if run.RewindTarget != nil {
			dto.RewindTarget = run.RewindTarget.DirName()
		}
		if run.RewindRoadmapPhase != nil {
			dto.RewindRoadmapPhase = *run.RewindRoadmapPhase
		}
		if run.CarriedFromRun > 0 {
			dto.CarriedFromRun = run.CarriedFromRun
		}
		if len(run.CarriedPhases) > 0 {
			dto.CarriedPhases = append([]string(nil), run.CarriedPhases...)
		}
		dto.BackupBranchRepos = runBackupBranchRepos(run)
	}
	return dto
}

// runPhaseLabel derives a run-authentic phase label from the run's pending
// review phase. A sealed run does not store its own current phase, so an
// empty label is returned rather than borrowing the active run's phase.
func runPhaseLabel(run *feature.Run) string {
	if run == nil || run.PendingReviewPhase == nil {
		return ""
	}
	return run.PendingReviewPhase.DirName()
}

// runBackupBranchRepos returns the sorted repo names that have a recorded
// backup branch for this run. Branch names are redacted: only the repo names
// are exposed, never the branch values.
func runBackupBranchRepos(run *feature.Run) []string {
	if run == nil || len(run.BackupBranches) == 0 {
		return nil
	}
	repos := make([]string, 0, len(run.BackupBranches))
	for name := range run.BackupBranches {
		repos = append(repos, name)
	}
	sort.Strings(repos)
	return repos
}

func runTimingDTO(run *feature.Run) TimingDTO {
	if run == nil {
		return TimingDTO{}
	}
	byPhase := make(map[string]int64, len(run.PhaseTimings))
	var total int64
	for phase, dur := range run.PhaseTimings {
		seconds := int64(dur.Seconds())
		byPhase[phase] = seconds
		total += seconds
	}
	if run.ActiveTimingKey != "" && run.ActivePhaseStart != nil {
		activeSeconds := int64(run.PhaseRuntime(run.ActiveTimingKey).Seconds())
		total += activeSeconds - byPhase[run.ActiveTimingKey]
		byPhase[run.ActiveTimingKey] = activeSeconds
	}
	return TimingDTO{TotalSeconds: total, ByPhase: byPhase}
}

func runCostDTO(run *feature.Run) CostDTO {
	if run == nil {
		return CostDTO{}
	}
	byPhase := make(map[string]float64, len(run.PhaseCosts))
	var total float64
	for phase, cost := range run.PhaseCosts {
		byPhase[phase] = cost
		total += cost
	}
	return CostDTO{TotalUSD: total, ByPhase: byPhase}
}

// runCostWithActiveSessions overlays provider-reported cumulative cost for
// sessions that are still active in this run. Completed session costs are
// already in Run.PhaseCosts, so excluding them prevents double counting.
func (h *apiHandler) runCostWithActiveSessions(featureID string, run *feature.Run) CostDTO {
	cost := runCostDTO(run)
	if h.sessions == nil || run == nil {
		return cost
	}
	phaseKey := strings.TrimSpace(run.ActiveTimingKey)
	if phaseKey == "" {
		return cost
	}
	for _, sess := range h.sessions.ActiveSessions() {
		if sess == nil || sess.FeatureID() != featureID || sessionRunNumber(sess) != run.RunNumber {
			continue
		}
		runningCost := sess.AccumulatedUsage().CostUSD
		if result := sess.Cost(); result != nil && result.TotalCostUSD > runningCost {
			runningCost = result.TotalCostUSD
		}
		if runningCost <= 0 {
			continue
		}
		cost.ByPhase[phaseKey] += runningCost
		cost.TotalUSD += runningCost
	}
	return cost
}

func parseIntQuery(r *http.Request, name string, fallback int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return n, true
}
