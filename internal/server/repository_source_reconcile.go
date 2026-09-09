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

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// apiPathWorkspaceRepositoryReconcileSourceUpdate is the bounded
// settlement-read route for one uncertain Update-from-origin attempt.
const apiPathWorkspaceRepositoryReconcileSourceUpdate = "/api/v1/workspace/repositories/reconcile-source-update"

// defaultReconcileSourceDeadline bounds one reconciliation end to end. It
// exceeds the update attempt deadline so waiting out a full admitted
// attempt — including its coordination waits — plus the settlement reads
// always fits; the desktop client allowance exceeds it in turn.
const defaultReconcileSourceDeadline = 3 * time.Minute

// handleWorkspaceRepositoryReconcileSourceUpdateRoute serves POST
// /api/v1/workspace/repositories/reconcile-source-update. The request
// repeats the attempted update's displayed binding; every renderer-supplied
// value is an expectation compared against server-resolved state, never
// authority over which repository or ref is read. The handler first waits
// out the lifetime of any admitted update attempt on the resolved
// repository — including one still waiting for common-directory
// coordination — then takes that same coordination and re-checks: only a
// read under the lock with no admitted attempt is a settlement. The read is
// local-only (identity, current selection, and the requested branch's tip);
// it never fetches, never mutates, and never treats a cached comparison as
// evidence. A missing or replaced repository requires reselection.
func (h *apiHandler) handleWorkspaceRepositoryReconcileSourceUpdateRoute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !h.requireTrustedMutation(w, r) {
		return
	}
	var req RepositorySourceReconcileRequest
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
	if !validWireCommitSHA(req.ExpectedLocalSha) || !validWireCommitSHA(req.ExpectedOriginSha) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("expected local and origin SHAs must be full hex commit ids"))
		return
	}
	if req.CheckoutHeadRef != nil &&
		(*req.CheckoutHeadRef == "" || len(*req.CheckoutHeadRef) > 512 ||
			(*req.CheckoutHeadRef != "detached" && !strings.HasPrefix(*req.CheckoutHeadRef, "refs/heads/"))) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("observed checkout HEAD reference must be a full branch ref or detached"))
		return
	}
	if req.CheckoutHeadSha != nil && !validWireCommitSHA(*req.CheckoutHeadSha) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("observed checkout HEAD SHA must be a full hex commit id"))
		return
	}

	key, identity, ok := h.resolveCatalogSourceSelector(RepositorySourceSelector{RepoKey: req.RepoKey, Identity: req.Identity})
	if !ok {
		writeAPIError(w, http.StatusConflict, errcat.InvalidRepository,
			errcat.WithDiagnostics("the selected repository is no longer present under the expected identity"))
		return
	}

	deadline := h.reconcileSourceDeadline
	if deadline <= 0 {
		deadline = defaultReconcileSourceDeadline
	}
	ctx, cancel := context.WithTimeout(r.Context(), deadline)
	defer cancel()

	// Settlement: wait out every admitted attempt's lifetime, then hold the
	// canonical common-directory lock with no admitted attempt behind it.
	// An attempt admitted while the lock was being acquired is registered
	// before it waits for the lock, so the under-lock recheck observes it
	// and yields rather than reading a state it may still mutate.
	unlock, locked := h.settleForReconciliation(ctx, identity)
	if !locked {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.SourceReconcileUnavailable,
			errcat.WithDiagnostics("the reconciliation timed out waiting for repository coordination or an admitted update attempt"))
		return
	}
	defer unlock()

	// Identity is re-resolved under coordination: a replaced repository is
	// never read through a stale selector.
	freshIdentity, ok := git.ResolveRepoIdentity(identity.Path)
	if !ok || !freshIdentity.Equal(identity) {
		writeAPIError(w, http.StatusConflict, errcat.InvalidRepository,
			errcat.WithDiagnostics("the repository was replaced or moved during the reconciliation"))
		return
	}

	// An attempted update that bound the checkout HEAD to the branch itself
	// was an original-checkout update: its settlement must observe the
	// checkout before any whole-checkout completion is claimed. The branch
	// tip alone never proves the index and files advanced.
	observeCheckout := req.CheckoutHeadRef != nil && *req.CheckoutHeadRef == "refs/heads/"+req.Branch
	report, err := git.ReconcileSourceUpdateState(ctx, identity.Path, req.Branch, req.ExpectedLocalSha, req.ExpectedOriginSha, observeCheckout, git.OriginCheckOptions{})
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.SourceReconcileUnavailable,
			errcat.WithDiagnostics(boundUpdateSourceDiagnostics(err.Error())))
		return
	}
	plan := git.PlanOriginCheck(ctx, identity.Path, git.LocalSourceMode(req.Mode), git.OriginCheckOptions{})
	writeActionJSON(w, http.StatusOK, h.reconcileSourceResponse(key, freshIdentity, req, report, plan))
}

// settleForReconciliation acquires the common-directory lock only when no
// admitted update attempt is registered for the repository, yielding the
// lock to any attempt admitted meanwhile. It returns the unlock function
// and whether the settlement was reached within ctx.
func (h *apiHandler) settleForReconciliation(ctx context.Context, identity git.RepoIdentity) (func(), bool) {
	for {
		if err := h.sourceUpdates.wait(ctx, identity); err != nil {
			return nil, false
		}
		unlock, locked := git.LockRepositoryUntil(ctx, identity.Path)
		if !locked {
			return nil, false
		}
		if h.sourceUpdates.quiescent(identity) {
			return unlock, true
		}
		// An admitted attempt is queued behind this lock; let it through
		// and wait out its lifetime before reading.
		unlock()
	}
}

// reconcileSourceResponse projects one settlement observation onto the
// wire. Identity and key come from the server's fresh resolution; the mode,
// branch, and origin mapping echo the attempted update's binding, and the
// optional selection carries the freshly resolved current selection read
// under the same coordination.
func (h *apiHandler) reconcileSourceResponse(repoKey string, identity git.RepoIdentity, req RepositorySourceReconcileRequest, report git.SourceReconcileReport, plan git.OriginCheckPlan) *RepositorySourceReconcileResponse {
	resp := &RepositorySourceReconcileResponse{
		Outcome:      RepositorySourceReconcileResponseOutcome(report.State),
		RepoKey:      repoKey,
		Identity:     RepositoryIdentity{Path: identity.Path, CommonDir: identity.CommonDir, Device: git.FormatIdentityDevice(identity.Device), Inode: git.FormatIdentityInode(identity.Inode)},
		Mode:         RepositorySourceReconcileResponseMode(req.Mode),
		Branch:       req.Branch,
		OriginBranch: req.OriginBranch,
	}
	if report.LocalSHA != "" {
		local := report.LocalSHA
		resp.LocalSha = &local
	}
	if report.Checkout != nil {
		checkout := RepositorySourceReconcileCheckout{
			State: RepositorySourceReconcileCheckoutState(report.Checkout.State),
			HeadRef: report.Checkout.HeadRef,
		}
		if report.Checkout.HeadSHA != "" {
			headSHA := report.Checkout.HeadSHA
			checkout.HeadSha = &headSHA
		}
		resp.Checkout = &checkout
	}
	// The current selection is reported only when it resolves; a terminal
	// plan state (detached, missing base, no origin, other upstream) leaves
	// it absent, and the renderer's own source refresh owns what happens
	// next.
	if plan.Source.Kind != "" {
		kind := RepositorySourceKindBranch
		if plan.Source.Kind == git.LocalSourceDetached {
			kind = RepositorySourceKindDetached
		}
		selection := RepositorySource{
			RepoKey:     repoKey,
			Identity:    resp.Identity,
			Mode:        RepositorySourceMode(plan.Source.Mode),
			Kind:        kind,
			Branch:      plan.Source.Branch,
			ObservedSha: plan.Source.Commit,
		}
		resp.Selection = &selection
	}
	return resp
}
