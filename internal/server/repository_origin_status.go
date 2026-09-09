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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

const apiPathWorkspaceRepositoryOriginStatus = "/api/v1/workspace/repositories/origin-status"

func (h *apiHandler) handleWorkspaceRepositoryOriginStatusRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !h.requireTrustedMutation(w, r) {
		return
	}
	var req RepositoryOriginStatusRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if !req.Mode.Valid() || len(req.Repositories) == 0 || len(req.Repositories) > 32 {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("mode and 1-32 repository selectors are required"))
		return
	}
	refresh := make(map[string]struct{}, len(req.Refresh))
	for _, key := range req.Refresh {
		if key == "" || len(key) > maxInitializeRepoKeyLength {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("refresh keys must be non-empty and bounded"))
			return
		}
		if _, duplicate := refresh[key]; duplicate {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("refresh keys must be unique"))
			return
		}
		refresh[key] = struct{}{}
	}

	mode := git.LocalSourceMode(req.Mode)
	response := RepositoryOriginStatusResponse{Repositories: make([]RepositoryOriginStatus, 0, len(req.Repositories))}
	seen := make(map[string]struct{}, len(req.Repositories))
	for _, selector := range req.Repositories {
		if selector.RepoKey == "" || len(selector.RepoKey) > maxInitializeRepoKeyLength {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("repository key is missing or exceeds the bounded length"))
			return
		}
		key, identity, ok := h.resolveCatalogSourceSelector(selector)
		if !ok {
			writeAPIError(w, http.StatusConflict, errcat.InvalidRepository,
				errcat.WithDiagnostics("the selected repository is no longer present under the expected identity"))
			return
		}
		if _, duplicate := seen[identity.Path]; duplicate {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("repository selectors must be unique"))
			return
		}
		seen[identity.Path] = struct{}{}
		_, refreshing := refresh[key]
		response.Repositories = append(response.Repositories, h.originStatusRow(r.Context(), key, identity, mode, refreshing))
	}
	writeActionJSON(w, http.StatusOK, &response)
}

// originStatusRow resolves one repository's typed origin snapshot. Local-only
// outcomes are resolved inline; a mapped source consults (and if necessary
// schedules) the coordinator's coalesced attempt for the resolved source.
func (h *apiHandler) originStatusRow(ctx context.Context, repoKey string, identity git.RepoIdentity, mode git.LocalSourceMode, refresh bool) RepositoryOriginStatus {
	plan := git.PlanOriginCheck(ctx, identity.Path, mode, git.OriginCheckOptions{})
	row := RepositoryOriginStatus{
		RepoKey: repoKey,
		Identity: RepositoryIdentity{
			Path: identity.Path, CommonDir: identity.CommonDir,
			Device: git.FormatIdentityDevice(identity.Device), Inode: git.FormatIdentityInode(identity.Inode),
		},
		Mode: RepositoryOriginStatusMode(plan.Source.Mode),
	}
	if plan.Source.Kind == git.LocalSourceDetached {
		row.Kind = RepositoryOriginStatusKindDetached
		row.Commit = plan.Source.Commit
	} else {
		row.Kind = RepositoryOriginStatusKindBranch
		row.Branch = plan.Source.Branch
	}
	if plan.Source.Commit != "" {
		row.LocalSha = plan.Source.Commit
	}
	if plan.Mapping != nil {
		row.OriginBranch = plan.Mapping.Branch
	}
	if plan.Status != "" {
		row.Status = RepositoryOriginStatusStatus(plan.Status)
		checkedAt := time.Now().UTC()
		row.CheckedAt = &checkedAt
		attachOriginIssue(&row, plan.Status, plan.Diagnostics)
		return row
	}
	if h.originChecks == nil {
		row.Status = RepositoryOriginStatusStatus(git.OriginCheckUnknown)
		row.Issue = wireErrorPtr(errcat.New(errcat.OriginCheckUnavailable,
			errcat.WithDiagnostics("origin checks are not available on this runtime")))
		return row
	}
	completed, ok, _ := h.originChecks.ensure(identity, plan, identity.Path, refresh)
	if !ok {
		row.Status = RepositoryOriginStatusStatus(git.OriginCheckChecking)
		return row
	}
	row.applyCompleted(completed)
	return row
}

func (row *RepositoryOriginStatus) applyCompleted(completed *originCompleted) {
	row.Status = RepositoryOriginStatusStatus(git.OriginCheckUnknown)
	checkedAt := completed.checkedAt.UTC()
	row.CheckedAt = &checkedAt
	switch {
	case completed.comparison != nil:
		comparison := completed.comparison
		row.Status = RepositoryOriginStatusStatus(comparison.Status)
		row.FetchedSha = comparison.FetchedSHA
		row.LocalSha = comparison.LocalSHA
		ahead := comparison.AheadCount
		row.AheadCount = &ahead
		behind := comparison.BehindCount
		row.BehindCount = &behind
		if completed.updateEligible != nil {
			eligible := *completed.updateEligible
			row.UpdateEligible = &eligible
		}
		for _, blocker := range completed.updateBlockers {
			row.UpdateBlockers = append(row.UpdateBlockers, RepositoryOriginStatusUpdateBlockers(blocker))
		}
	case completed.absent:
		row.Status = RepositoryOriginStatusStatus(git.OriginCheckRemoteBranchMissing)
		attachOriginIssue(row, git.OriginCheckRemoteBranchMissing, "")
	default:
		row.Status = RepositoryOriginStatusStatus(git.OriginCheckUnknown)
		attachOriginIssue(row, git.OriginCheckUnknown, completed.diagnostics)
		if completed.stale != nil {
			row.StaleComparison = originComparisonWire(completed.stale)
		}
		if completed.updateEligible != nil {
			eligible := *completed.updateEligible
			row.UpdateEligible = &eligible
		}
		for _, blocker := range completed.updateBlockers {
			row.UpdateBlockers = append(row.UpdateBlockers, RepositoryOriginStatusUpdateBlockers(blocker))
		}
	}
}

func originComparisonWire(comparison *git.OriginComparison) *OriginComparison {
	if comparison == nil {
		return nil
	}
	return &OriginComparison{
		Status:       OriginComparisonStatus(comparison.Status),
		LocalSha:     comparison.LocalSHA,
		FetchedSha:   comparison.FetchedSHA,
		OriginBranch: comparison.OriginBranch,
		AheadCount:   comparison.AheadCount,
		BehindCount:  comparison.BehindCount,
		CheckedAt:    comparison.CheckedAt.UTC(),
	}
}

func wireErrorPtr(rendered errcat.Error) *Error {
	wire := wireError(rendered)
	return &wire
}

func attachOriginIssue(row *RepositoryOriginStatus, status git.OriginCheckStatus, diagnostics string) {
	switch status {
	case git.OriginCheckLocalBaseMissing:
		row.Issue = wireErrorPtr(errcat.New(errcat.LocalBaseMissing, errcat.WithDiagnostics(diagnostics)))
	case git.OriginCheckRemoteBranchMissing:
		row.Issue = wireErrorPtr(errcat.New(errcat.OriginBranchMissing))
	case git.OriginCheckUnknown:
		options := []errcat.Option{}
		if diagnostics != "" {
			options = append(options, errcat.WithDiagnostics(diagnostics))
		}
		row.Issue = wireErrorPtr(errcat.New(errcat.OriginCheckUnavailable, options...))
	}
}
