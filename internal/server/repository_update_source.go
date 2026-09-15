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
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// apiPathWorkspaceRepositoryUpdateSource is the bounded Update-from-origin
// route: one catalog-authorized selected branch advanced only by an
// expected-old-value compare-and-swap.
const apiPathWorkspaceRepositoryUpdateSource = "/api/v1/workspace/repositories/update-source"

// defaultUpdateSourceDeadline bounds one update attempt end to end:
// common-directory coordination waits, network work, local validation, and
// mutation completion. The desktop client allowance exceeds it.
const defaultUpdateSourceDeadline = 2 * time.Minute

// updateSourceDiagnosticsBound caps raw git detail carried on the wire; the
// git layer already bounds and redacts command output.
const updateSourceDiagnosticsBound = 400

// handleWorkspaceRepositoryUpdateSourceRoute serves POST
// /api/v1/workspace/repositories/update-source. The server resolves the
// structured repository selector against its own current authorized catalog
// and revalidates the expected identity under canonical common-directory
// coordination before any mutation: renderer-supplied keys, paths, refs, and
// SHAs are comparisons, never independent filesystem or revision authority.
// Stale expectations return a typed result with a freshly resolved status;
// missing or replaced repositories require reselection. The mutation is
// ref-only: no checkout, reset, rebase, merge, force, stash, clean, staging,
// push, or hook execution, and Git ref/index locks are never deleted.
func (h *apiHandler) handleWorkspaceRepositoryUpdateSourceRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !h.requireTrustedMutation(w, r) {
		return
	}
	var req RepositoryUpdateSourceRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if !req.Mode.Valid() {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("a valid shared branch mode is required"))
		return
	}
	if strings.TrimSpace(req.RepoKey) == "" || len(req.RepoKey) > maxInitializeRepoKeyLength {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("repository key is missing or exceeds the bounded length"))
		return
	}
	if strings.TrimSpace(req.Branch) == "" || len(req.Branch) > 512 ||
		strings.TrimSpace(req.OriginBranch) == "" || len(req.OriginBranch) > 512 {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("expected branch and origin branch are required and bounded"))
		return
	}
	if !validWireCommitSHA(req.ExpectedLocalSha) || !validWireCommitSHA(req.ExpectedOriginSha) || !validWireCommitSHA(req.CheckoutHeadSha) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("expected local, origin, and checkout HEAD SHAs must be full hex commit ids"))
		return
	}
	if req.CheckoutHeadRef == "" || len(req.CheckoutHeadRef) > 512 ||
		(req.CheckoutHeadRef != "detached" && !strings.HasPrefix(req.CheckoutHeadRef, "refs/heads/")) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("observed checkout HEAD reference must be a full branch ref or detached"))
		return
	}

	key, identity, ok := h.resolveCatalogSourceSelector(RepositorySourceSelector{RepoKey: req.RepoKey, Identity: req.Identity})
	if !ok {
		writeAPIError(w, http.StatusConflict, errcat.InvalidRepository,
			errcat.WithDiagnostics("the selected repository is no longer present under the expected identity"))
		return
	}

	// The attempt's lifetime is registered before any coordination wait so
	// reconciliation reads and feature acceptance can wait it out: an
	// admitted attempt that is still queued, running, or not yet reaped is
	// never settled by a read of its old local SHA.
	releaseAttempt := h.sourceUpdates.begin(identity)
	defer releaseAttempt()

	deadline := h.updateSourceDeadline
	if deadline <= 0 {
		deadline = defaultUpdateSourceDeadline
	}
	ctx, cancel := context.WithTimeout(r.Context(), deadline)
	defer cancel()

	// The update shares the canonical common-directory boundary with origin
	// checks, feature acceptance, and setup: coordination waits, network
	// work, validation, and the mutation all fit inside the deadline.
	unlock, locked := git.LockRepositoryUntil(ctx, identity.Path)
	if !locked {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.SourceUpdateUnavailable,
			errcat.WithDiagnostics("the update timed out waiting for repository coordination"))
		return
	}
	defer unlock()

	// Identity is re-resolved under coordination: a repository replaced at
	// the same path must never be mutated through a stale selector.
	freshIdentity, ok := git.ResolveRepoIdentity(identity.Path)
	if !ok || !freshIdentity.Equal(identity) {
		writeAPIError(w, http.StatusConflict, errcat.InvalidRepository,
			errcat.WithDiagnostics("the repository was replaced or moved while the update was starting"))
		return
	}

	outcome, err := git.UpdateSourceFromOrigin(ctx, identity.Path, git.SourceUpdateExpectation{
		Mode:              git.LocalSourceMode(req.Mode),
		Branch:            req.Branch,
		OriginBranch:      req.OriginBranch,
		ExpectedLocalSHA:  req.ExpectedLocalSha,
		ExpectedOriginSHA: req.ExpectedOriginSha,
		CheckoutHeadRef:   req.CheckoutHeadRef,
		CheckoutHeadSHA:   req.CheckoutHeadSha,
	}, h.updateSourceOptions)
	if err != nil {
		var unavailable *git.SourceUpdateUnavailableError
		diagnostics := err.Error()
		if errors.As(err, &unavailable) {
			diagnostics = unavailable.Diagnostics
		}
		writeAPIError(w, http.StatusServiceUnavailable, errcat.SourceUpdateUnavailable,
			errcat.WithDiagnostics(boundUpdateSourceDiagnostics(diagnostics)))
		return
	}
	writeActionJSON(w, http.StatusOK, h.updateSourceResponse(key, freshIdentity, req, outcome))
}

// updateSourceResponse projects one typed update outcome onto the wire. Every
// identity, key, branch, mapping, and SHA comes from the server's fresh
// resolution; when a terminal plan state leaves the current selection
// unresolved, the request's own mode, branch, and mapping identify the
// refused request instead of inventing a resolution.
func (h *apiHandler) updateSourceResponse(repoKey string, identity git.RepoIdentity, req RepositoryUpdateSourceRequest, outcome git.SourceUpdateOutcome) *RepositoryUpdateSourceResponse {
	var mode RepositoryUpdateSourceResponseMode
	if outcome.Plan.Source.Mode != "" {
		mode = RepositoryUpdateSourceResponseMode(outcome.Plan.Source.Mode)
	} else {
		mode = RepositoryUpdateSourceResponseMode(req.Mode)
	}
	branch := req.Branch
	originBranch := req.OriginBranch
	if outcome.Plan.Source.Branch != "" {
		branch = outcome.Plan.Source.Branch
	}
	if outcome.Plan.Mapping != nil {
		originBranch = outcome.Plan.Mapping.Branch
	}
	resp := &RepositoryUpdateSourceResponse{
		Result:       RepositoryUpdateSourceResponseResult(outcome.Result),
		RepoKey:      repoKey,
		Identity:     RepositoryIdentity{Path: identity.Path, CommonDir: identity.CommonDir, Device: git.FormatIdentityDevice(identity.Device), Inode: git.FormatIdentityInode(identity.Inode), BirthTime: identity.BirthTime},
		Mode:         mode,
		Branch:       branch,
		OriginBranch: originBranch,
	}
	if outcome.FetchedSHA != "" {
		fetched := outcome.FetchedSHA
		resp.FetchedSha = &fetched
	}
	switch outcome.Result {
	case git.SourceUpdateUpdated:
		previous := outcome.PreviousSHA
		local := outcome.LocalSHA
		resp.PreviousSha = &previous
		resp.LocalSha = &local
	case git.SourceUpdateAlreadyUpToDate:
		local := outcome.LocalSHA
		resp.LocalSha = &local
	case git.SourceUpdateStale:
		resp.Reason = RepositoryUpdateSourceResponseReason(outcome.Reason)
		status := h.updateStaleStatusRow(repoKey, identity, req, outcome)
		resp.Status = &status
	}
	return resp
}

// updateStaleStatusRow builds the freshly resolved status snapshot carried by
// a stale refusal. It reports the current selection and, when proved, a
// fresh comparison; unprovable fetches never present old counts as current.
// An unresolved terminal plan reports the request's own mode and branch: the
// current selection is exactly "missing", never an invented resolution.
func (h *apiHandler) updateStaleStatusRow(repoKey string, identity git.RepoIdentity, req RepositoryUpdateSourceRequest, outcome git.SourceUpdateOutcome) RepositoryOriginStatus {
	plan := outcome.Plan
	row := RepositoryOriginStatus{
		RepoKey: repoKey,
		Identity: RepositoryIdentity{
			Path: identity.Path, CommonDir: identity.CommonDir,
			Device: git.FormatIdentityDevice(identity.Device), Inode: git.FormatIdentityInode(identity.Inode),
			BirthTime: identity.BirthTime,
		},
		Mode:   RepositoryOriginStatusMode(req.Mode),
		Branch: req.Branch,
	}
	if plan.Source.Mode != "" {
		row.Mode = RepositoryOriginStatusMode(plan.Source.Mode)
	}
	if plan.Source.Kind == git.LocalSourceDetached {
		row.Kind = RepositoryOriginStatusKindDetached
		row.Branch = ""
		row.Commit = plan.Source.Commit
	} else {
		row.Kind = RepositoryOriginStatusKindBranch
		if plan.Source.Branch != "" {
			row.Branch = plan.Source.Branch
		}
	}
	if plan.Source.Commit != "" {
		row.LocalSha = plan.Source.Commit
	}
	if plan.Mapping != nil {
		row.OriginBranch = plan.Mapping.Branch
	}
	checkedAt := time.Now().UTC()
	row.CheckedAt = &checkedAt
	switch {
	case plan.Status != "":
		row.Status = RepositoryOriginStatusStatus(plan.Status)
		attachOriginIssue(&row, plan.Status, plan.Diagnostics)
	case outcome.Comparison != nil:
		comparison := outcome.Comparison
		row.Status = RepositoryOriginStatusStatus(comparison.Status)
		row.LocalSha = comparison.LocalSHA
		row.FetchedSha = comparison.FetchedSHA
		ahead := comparison.AheadCount
		row.AheadCount = &ahead
		behind := comparison.BehindCount
		row.BehindCount = &behind
	case outcome.RemoteBranchMissing:
		row.Status = RepositoryOriginStatusStatus(git.OriginCheckRemoteBranchMissing)
		attachOriginIssue(&row, git.OriginCheckRemoteBranchMissing, "")
	default:
		row.Status = RepositoryOriginStatusStatus(git.OriginCheckUnknown)
		attachOriginIssue(&row, git.OriginCheckUnknown, "the fresh origin comparison could not be proved during the update attempt")
	}
	notEligible := false
	row.UpdateEligible = &notEligible
	for _, blocker := range outcome.Blockers {
		row.UpdateBlockers = append(row.UpdateBlockers, RepositoryOriginStatusUpdateBlockers(blocker))
	}
	return row
}

// validWireCommitSHA accepts exactly a full 40- or 64-character hex commit
// id, so malformed expectations fail as bad requests instead of reaching Git.
func validWireCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func boundUpdateSourceDiagnostics(detail string) string {
	detail = strings.TrimSpace(detail)
	if len(detail) > updateSourceDiagnosticsBound {
		detail = detail[:updateSourceDiagnosticsBound]
	}
	return detail
}
