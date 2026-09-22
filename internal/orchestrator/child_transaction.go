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

package orchestrator

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// This file implements the transactional multi-repository integration
// coordinator over per-layer ref lists: prepare candidates without advancing
// parent refs, apply them with one multi-ref compare-and-swap transaction per
// repository, and compensate only changes the transaction can prove it made.
// Each journal entry lists every parent-layer ref the transaction rewrites;
// the highest-position ref is the top ref the parent worktree must have
// checked out and syncs to.

// parentTopLayerPosition returns the position of the parent's highest stack
// layer, or 0 when the parent records no stack.
func parentTopLayerPosition(parent *feature.Feature) int {
	if parent == nil {
		return 0
	}
	top := 0
	for i := range parent.Stack {
		if parent.Stack[i].Position > top {
			top = parent.Stack[i].Position
		}
	}
	return top
}

// reviewFeedbackChildWithStack reports whether this is a review-feedback
// child whose parent records a delivery stack — the shape whose candidates
// are staged through the relocation ladder rather than a merge candidate.
// Review-feedback children of stackless parents and every other child kind
// keep merge-candidate (or pass-through) preparation.
func reviewFeedbackChildWithStack(child, parent *feature.Feature) bool {
	return child != nil && parent != nil &&
		child.Parent != nil &&
		child.Parent.Kind == feature.ChildKindReviewFeedback &&
		len(parent.Stack) > 0
}

// parentWorktreeOfRepo resolves a parent repository's worktree path.
func parentWorktreeOfRepo(repo *feature.FeatureRepo) string {
	if repo == nil {
		return ""
	}
	if repo.WorktreePath != "" {
		return repo.WorktreePath
	}
	return repo.Path
}

// parentTipOfRepo reads the parent worktree's checked-out tip for one
// repository, used as the relocation chain's launch-base fallback.
func parentTipOfRepo(o *Orchestrator, repo *feature.FeatureRepo) string {
	tip, err := o.childHeadSHA(parentWorktreeOfRepo(repo))
	if err != nil {
		return ""
	}
	return tip
}

// singleRefEntry builds the one-ref journal entry shape used by refactor and
// rebase children: the parent's checked-out branch with the current tip as
// anchor.
func singleRefEntry(repoName, branch string, layer int, anchor, childHead string) feature.RepoTransactionEntry {
	return feature.RepoTransactionEntry{
		Repo: repoName,
		Refs: []feature.RepoTransactionRef{{
			Branch:    branch,
			Layer:     layer,
			AnchorSHA: anchor,
		}},
		ChildHeadSHA: childHead,
		PrepState:    feature.RepoPrepPending,
	}
}

// prepareTransactionCandidates prepares a durable candidate for every
// inherited repository without changing any parent ref or worktree. It
// commits remaining child changes, validates the parent repository mapping,
// collects cleanliness diagnostics across all parent worktrees in one
// preflight, and stages the candidate vector. Each child head, the per-ref
// anchors, candidates, and preparation outcomes are persisted before
// application.
//
// The parent is locked while a pass runs, so a parent tip that moved away
// from its creation-time base — other than to a commit this transaction
// itself produced — is external drift: preparation parks at attention with
// the integration_parent_ref_drift record before any staging. A dirty
// repository, conflict, or preparation failure likewise leaves every parent
// ref unchanged and parks with its catalog code.
func (o *Orchestrator) prepareTransactionCandidates(child, parent *feature.Feature) (*feature.TransactionJournal, error) {
	if o.deps.Worktrees == nil {
		return nil, fmt.Errorf("transaction: worktree operations are not configured")
	}

	// Rebase mechanical integration gate: the single kind-specific pre-prepare
	// step. For a rebase child, re-verify the git-level exit criteria for
	// every behind repo against the creation-time persisted targets before
	// any candidate or ref is touched. Any violation parks the transaction
	// at attention with a stored record built from every violation and
	// leaves every parent ref byte-identical. Refactor and review-feedback
	// children flow through unchanged.
	if child.Parent != nil && child.Parent.Kind == feature.ChildKindRebase {
		if gate := o.rebaseIntegrationGate(child); gate != nil {
			if err := o.persistTransaction(child.ID, gate); err != nil {
				return nil, fmt.Errorf("recording rebase gate attention: %w", err)
			}
			return nil, o.emitTransactionAttention(child, gate.Attention)
		}
	}

	journal := &feature.TransactionJournal{
		Phase: feature.TransactionPhasePreparing,
	}

	// A refactor child appends its own layers onto the parent's stack; the
	// parent's top layer — needed to record each repository's previous top —
	// is resolved once up front. A parent without a stack leaves the previous
	// top unset and the append preparation below parks with the
	// candidate-failed attention naming the missing stack.
	var parentTopLayer feature.StackLayer
	parentHasStack := parent != nil && len(parent.Stack) > 0
	if parentHasStack {
		layer, ok := stackLayerByPosition(parent.Stack, parentTopLayerPosition(parent))
		if !ok {
			return nil, fmt.Errorf("transaction: parent %s records no layer for its top stack position", parent.ID)
		}
		parentTopLayer = layer
	}

	// Validate every parent repo and capture per-repo entries.
	var driftFindings []integrationFinding
	var dirtyFindings []integrationFinding
	for _, childRepo := range child.Repos {
		parentRepo := featureRepoByName(parent, childRepo.Name)
		if parentRepo == nil {
			return nil, fmt.Errorf("%w: parent %s no longer has repository %s", ErrChildIntegrationRefused, parent.ID, childRepo.Name)
		}
		if parentRepo.Path != childRepo.Path {
			return nil, fmt.Errorf("%w: repository %s path changed since launch (%s != %s)", ErrChildIntegrationRefused, childRepo.Name, parentRepo.Path, childRepo.Path)
		}

		childWorktree := childRepo.WorktreePath
		if childWorktree == "" {
			childWorktree = childRepo.Path
		}
		parentWorktree := parentRepo.WorktreePath
		if parentWorktree == "" {
			parentWorktree = parentRepo.Path
		}

		// Commit remaining child changes and capture child head.
		childHead, err := git.CommitAllAndGetHead(childWorktree, fmt.Sprintf("Integration commit for refactor child %s repo %s", child.ID, childRepo.Name))
		if err != nil {
			return nil, fmt.Errorf("commit child changes for repo %s: %w", childRepo.Name, err)
		}

		// Capture the current parent tip (anchor).
		parentTip, err := o.childHeadSHA(parentWorktree)
		if err != nil {
			return nil, fmt.Errorf("capture parent anchor for repo %s: %w", childRepo.Name, err)
		}

		// Check parent worktree cleanliness.
		report, err := o.deps.Worktrees.InspectCleanliness(parentWorktree, feature.DefaultDirtyPathLimit)
		if err != nil {
			return nil, fmt.Errorf("inspecting parent worktree %s: %w", childRepo.Name, err)
		}

		entry := singleRefEntry(childRepo.Name, parentRepo.Branch, parentTopLayerPosition(parent), parentTip, childHead)
		if refactorAppendChild(child) {
			// A refactor child appends layers instead of merging into the
			// checked-out branch: the entry starts bare — the created refs
			// and appended layer definitions are computed after the drift
			// and dirty gates park — and records the repository's previous
			// top (branch, position, tip) so drift findings carry context,
			// a drift retry at the same tip is acknowledged, and apply,
			// rollback, and discard know the worktree's target.
			entry = feature.RepoTransactionEntry{
				Repo:         childRepo.Name,
				ChildHeadSHA: childHead,
				PrepState:    feature.RepoPrepPending,
			}
			if parentHasStack {
				entry.PreviousTop = &feature.RepoTransactionPreviousTop{
					Branch: parentTopLayer.Branch,
					Layer:  parentTopLayer.Position,
					TipSHA: parentTip,
				}
			}
		} else if reviewFeedbackChildWithStack(child, parent) {
			// A review-feedback child of a stacked parent stages its
			// candidates through the relocation ladder, which records the
			// per-layer ref list itself; the drift and dirty gates below
			// still run against the checked-out parent tip.
			entry = feature.RepoTransactionEntry{
				Repo:         childRepo.Name,
				ChildHeadSHA: childHead,
				PrepState:    feature.RepoPrepPending,
			}
		}

		// A tip away from the creation-time base is external drift unless it
		// is a candidate this child's transaction produced (a resumed or
		// partially applied transaction being rebuilt) or a tip a prior drift
		// attention already reported — retrying integration at the same tip
		// is the operator's explicit acknowledgment to absorb it.
		if base := child.BaseSHA(childRepo.Name); base != "" && parentTip != base &&
			!transactionProducedSHA(child.Parent.Transaction, parentTip) &&
			!transactionAcknowledgedDrift(child.Parent.Transaction, childRepo.Name, parentTip) {
			entry.PrepState = feature.RepoPrepFailed
			finding := entryFinding(&entry, errcat.IntegrationParentRefDrift,
				fmt.Sprintf("parent branch tip moved from %s to %s while the pass was running; the parent is locked during a pass, so this usually means something wrote to the parent's checkout outside the integration transaction; parent refs were left untouched — retry integration to accept the moved tip, or reset the parent branch before retrying", base, parentTip))
			finding.ctx.ObservedSHA = parentTip
			driftFindings = append(driftFindings, finding)
		}

		if report.Dirty() {
			entry.PrepState = feature.RepoPrepFailed
			files := dirtyFileList(report.Staged, report.Unstaged, report.Untracked)
			finding := entryFinding(&entry, errcat.IntegrationParentDirty,
				fmt.Sprintf("parent worktree has uncommitted changes: %s", joinFileList(files)))
			finding.ctx.DirtyFiles = files
			dirtyFindings = append(dirtyFindings, finding)
		}

		journal.Entries = append(journal.Entries, entry)
	}

	// If any parent tip drifted, park the transaction with the drift record
	// before any staging. All parent refs are unchanged. Carry candidate
	// provenance from the prior journal across the overwrite so a ref this
	// transaction already moved is not reclassified as drift on retry.
	if len(driftFindings) > 0 {
		if prior := child.Parent.Transaction; prior != nil {
			carryPriorCandidates(journal, prior)
		}
		journal.Phase = feature.TransactionPhaseAttention
		return nil, o.parkIntegrationAttention(child, journal, driftFindings)
	}

	// If any parent is dirty, park the transaction with the dirty-parent
	// record. All parent refs are unchanged.
	if len(dirtyFindings) > 0 {
		journal.Phase = feature.TransactionPhaseAttention
		return nil, o.parkIntegrationAttention(child, journal, dirtyFindings)
	}

	// Stage candidates for every repository without advancing any parent ref.
	// A refactor child appends its layers: pure computation — created refs,
	// previous tops, appended layer definitions, ancestry and branch-name
	// checks — recorded on the journal without touching any ref. A rebase
	// child stages from its persisted restack result: rewrite refs per kept
	// layer, delete refs per dropped layer, the previous-top record when the
	// top layer is dropped, and the remap — again pure computation. Every
	// other kind stages explicit candidates: two-parent no-ff merge commits
	// created in a temporary detached worktree, or — for a rebase repo that
	// was already up to date at child creation — a pass-through candidate
	// whose SHA is the parent anchor.
	if refactorAppendChild(child) {
		ok, err := o.prepareAppendedLayerCandidates(child, parent, journal)
		if err != nil || !ok {
			// A park records the attention itself and leaves every parent
			// ref untouched; the journal is returned empty so the caller
			// stops before apply.
			return nil, err
		}
	} else {
		for i := range journal.Entries {
			entry := &journal.Entries[i]
			parentRepo := featureRepoByName(parent, entry.Repo)
			if parentRepo == nil {
				entry.PrepState = feature.RepoPrepFailed
				journal.Phase = feature.TransactionPhaseAttention
				finding := entryFinding(entry, errcat.IntegrationRepositoryMissing,
					fmt.Sprintf("parent no longer has repository %s", entry.Repo))
				return nil, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
			}
			if child.Parent.Kind == feature.ChildKindRebase && child.IsRebaseWorkRepo(entry.Repo) {
				// A rebase work repository stages from the persisted restack
				// result; a failure parks with the candidate-failed record
				// before any ref is touched.
				if finding, staged := o.prepareRebaseRestackRefs(child, parent, entry); !staged {
					entry.PrepState = feature.RepoPrepFailed
					journal.Phase = feature.TransactionPhaseAttention
					return nil, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
				}
				entry.PrepState = feature.RepoPrepPrepared
				if err := o.persistTransaction(child.ID, journal); err != nil {
					return nil, fmt.Errorf("recording restack candidates for repo %s: %w", entry.Repo, err)
				}
				continue
			}
			if reviewFeedbackChildWithStack(child, parent) {
				finding, err := o.prepareReviewFeedbackRefs(child, parent, entry, parentRepo, parentTipOfRepo(o, parentRepo))
				if err != nil {
					if !errors.Is(err, errReviewFeedbackFallbackToMerge) {
						return nil, err
					}
					// The chain cannot be linearized with the child's commits
					// (an acknowledged drift moved the top ref away from the
					// launch base) and the merge-candidate fallback is gone:
					// park with the candidate-failed record so the operator
					// resets the parent branch or relaunches the pass.
					entry.PrepState = feature.RepoPrepFailed
					journal.Phase = feature.TransactionPhaseAttention
					finding = entryFinding(entry, errcat.IntegrationCandidateFailed,
						fmt.Sprintf("repo %s's top ref moved away from the launch base, so its chain cannot be linearized with the child's commits; reset the parent branch to the launch base and retry, or discard the pass", entry.Repo))
					return nil, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
				}
				if finding.code != "" {
					entry.PrepState = feature.RepoPrepFailed
					journal.Phase = feature.TransactionPhaseAttention
					return nil, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
				}
				entry.PrepState = feature.RepoPrepPrepared
				if err := o.persistTransaction(child.ID, journal); err != nil {
					return nil, fmt.Errorf("recording relocated candidates for repo %s: %w", entry.Repo, err)
				}
				continue
			}
			if rebasePassThroughRepo(child, entry.Repo) {
				if !git.IsAncestor(parentRepo.Path, entry.ChildHeadSHA, entry.TopRef().AnchorSHA) {
					entry.PrepState = feature.RepoPrepFailed
					journal.Phase = feature.TransactionPhaseAttention
					finding := entryFinding(entry, errcat.RebaseGatePassthroughModified,
						fmt.Sprintf("rebase child modified up-to-date repo %s; only repos behind at launch may change", entry.Repo))
					return nil, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
				}
				entry.Refs[0].CandidateSHA = entry.Refs[0].AnchorSHA
				entry.PrepState = feature.RepoPrepPrepared
				if err := o.persistTransaction(child.ID, journal); err != nil {
					return nil, fmt.Errorf("recording pass-through candidate for repo %s: %w", entry.Repo, err)
				}
				continue
			}

			// A review-feedback child of a stackless parent has no chain to
			// linearize its fixes onto — and its launch validation requires
			// open layer pull requests, so a stackless parent is unreachable
			// in practice. Fail closed instead of staging a merge candidate:
			// the merge-candidate preparation path is gone.
			entry.PrepState = feature.RepoPrepFailed
			journal.Phase = feature.TransactionPhaseAttention
			finding := entryFinding(entry, errcat.IntegrationCandidateFailed,
				fmt.Sprintf("repo %s's parent records no delivery stack; the pass's changes cannot be integrated", entry.Repo))
			return nil, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
		}
	}

	journal.Phase = feature.TransactionPhasePrepared
	if err := o.persistTransaction(child.ID, journal); err != nil {
		return nil, fmt.Errorf("recording prepared transaction: %w", err)
	}
	return journal, nil
}

// carryPriorCandidates copies candidate provenance from a prior journal into
// a freshly captured one: every entry's apply state carries over, and a prior
// ref's candidate is copied onto the new ref with the same branch name.
func carryPriorCandidates(journal *feature.TransactionJournal, prior *feature.TransactionJournal) {
	for i := range journal.Entries {
		pe := prior.EntryByRepo(journal.Entries[i].Repo)
		if pe == nil {
			continue
		}
		journal.Entries[i].ApplyState = pe.ApplyState
		for j := range journal.Entries[i].Refs {
			if journal.Entries[i].Refs[j].CandidateSHA != "" {
				continue
			}
			for k := range pe.Refs {
				if pe.Refs[k].Branch == journal.Entries[i].Refs[j].Branch {
					journal.Entries[i].Refs[j].CandidateSHA = pe.Refs[k].CandidateSHA
					break
				}
			}
		}
	}
}

// previousTopDeleted reports whether the entry's recorded previous top names
// a branch whose ref update deletes it: the checked-out top layer is being
// dropped. Apply must move the parent worktree onto the new top branch
// before the ref transaction — the deleted ref is the worktree's checked-out
// branch, and deleting it in place would strand the checkout with a dangling
// HEAD — and hard-reset the worktree to the top candidate after, because the
// transaction moves the new top branch's ref underneath the checkout;
// rollback and discard recreate the deleted ref before switching back.
func previousTopDeleted(entry *feature.RepoTransactionEntry) bool {
	if entry == nil || entry.PreviousTop == nil {
		return false
	}
	for i := range entry.Refs {
		if entry.Refs[i].Branch == entry.PreviousTop.Branch &&
			entry.Refs[i].RefKind() == feature.RepoRefKindDelete {
			return true
		}
	}
	return false
}

// applyTransactionCandidates applies the fully prepared candidate vector.
// For each repository it verifies the parent worktree has the top ref's
// branch checked out, reads every listed ref, refuses with the ref-race
// attention when any ref differs from its anchor, and then moves every ref
// — rewrites to their candidates, deletes removed — in one multi-ref
// compare-and-swap transaction. An entry whose previous top is deleted
// switches the worktree onto the new top branch before the transaction and
// hard-resets it to the top candidate after. A pass-through entry only
// syncs the worktree. Ref updates are durably tracked after every repository.
// If a later repository fails, it compensates earlier repositories only when
// their refs still equal the transaction's candidate commits. External ref
// movement is never overwritten: ambiguous states remain intact and become
// integration attention.
func (o *Orchestrator) applyTransactionCandidates(child, parent *feature.Feature, journal *feature.TransactionJournal) error {
	if o.deps.Worktrees == nil {
		return fmt.Errorf("transaction: ref CAS operations are not configured")
	}
	if !journal.AllCandidatesPrepared() {
		return fmt.Errorf("transaction: cannot apply from a partially prepared journal")
	}

	journal.Phase = feature.TransactionPhaseApplying
	if err := o.persistTransaction(child.ID, journal); err != nil {
		return fmt.Errorf("recording applying phase: %w", err)
	}

	pendingWorktreeSync := false

	// Apply each entry's ref list in order. One multi-ref compare-and-swap
	// transaction per repository: every listed ref moves or none does.
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		if entry.ApplyState == feature.RepoApplyApplied {
			continue
		}
		topRef := entry.TopRef()
		if topRef == nil {
			code, diag := errcat.IntegrationCandidateFailed, fmt.Sprintf("repo %s records no refs to apply", entry.Repo)
			if journal.AnyApplied() {
				entry.ApplyState = feature.RepoApplyAttention
				return o.rollbackTransaction(child, parent, journal, i, entryFinding(entry, code, diag))
			}
			return o.parkApplyAttention(child, journal, entry, code, diag)
		}

		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			code, diag := errcat.IntegrationRepositoryMissing, fmt.Sprintf("parent no longer has repository %s", entry.Repo)
			if journal.AnyApplied() {
				entry.ApplyState = feature.RepoApplyAttention
				return o.rollbackTransaction(child, parent, journal, i, entryFinding(entry, code, diag))
			}
			return o.parkApplyAttention(child, journal, entry, code, diag)
		}

		// Verify the parent worktree has the entry's previous-top branch
		// checked out — the branch whose tip the transaction verifies —
		// falling back to the top ref's branch for entries without a
		// previous-top record (rebase and review-feedback shapes).
		parentWorktree := parentRepo.WorktreePath
		if parentWorktree == "" {
			parentWorktree = parentRepo.Path
		}
		requiredBranch := topRef.Branch
		if prev := entry.PreviousTopRef(); prev != nil {
			requiredBranch = prev.Branch
		}
		if current := o.deps.Worktrees.CurrentBranch(parentWorktree); current != requiredBranch {
			code := errcat.IntegrationParentBranchMismatch
			diag := fmt.Sprintf("parent worktree %s has branch %q checked out; integration requires the recorded parent branch %q", parentWorktree, current, requiredBranch)
			if journal.AnyApplied() {
				entry.ApplyState = feature.RepoApplyAttention
				return o.rollbackTransaction(child, parent, journal, i, entryFinding(entry, code, diag))
			}
			return o.parkApplyAttention(child, journal, entry, code, diag)
		}

		// An entry whose previous top is deleted must move the parent
		// worktree onto the new top branch BEFORE the ref transaction: the
		// deleted ref is the worktree's checked-out branch. A failed switch
		// parks before any ref moves — mirroring the branch-mismatch park —
		// so no ref of this repository changes and earlier applied
		// repositories are compensated as for any other apply failure.
		if previousTopDeleted(entry) {
			if err := o.deps.Worktrees.SwitchBranch(parentWorktree, topRef.Branch); err != nil {
				code := errcat.IntegrationWorktreeSyncFailed
				diag := fmt.Sprintf("switching parent worktree %s from the deleted previous top %q onto the new top branch %q before the transaction: %v",
					parentWorktree, entry.PreviousTop.Branch, topRef.Branch, err)
				if journal.AnyApplied() {
					entry.ApplyState = feature.RepoApplyAttention
					return o.rollbackTransaction(child, parent, journal, i, entryFinding(entry, code, diag))
				}
				return o.parkApplyAttention(child, journal, entry, code, diag)
			}
		}

		// Read every listed ref — absent-aware, so a created ref's expected
		// absence is distinguishable from a failed read — to detect external
		// movement before any update. A single racing ref refuses the whole
		// repository.
		for j := range entry.Refs {
			ref := &entry.Refs[j]
			refName := "refs/heads/" + ref.Branch
			currentSHA, absent, err := o.deps.Worktrees.RefSHAOrAbsent(parentRepo.Path, refName)
			if err != nil {
				code, diag := errcat.IntegrationCandidateFailed, fmt.Sprintf("reading ref %s: %v", refName, err)
				if journal.AnyApplied() {
					entry.ApplyState = feature.RepoApplyAttention
					return o.rollbackTransaction(child, parent, journal, i, entryFinding(entry, code, diag))
				}
				return o.parkApplyAttention(child, journal, entry, code, diag)
			}
			ref.ObservedSHA = currentSHA
			if ref.RefKind() == feature.RepoRefKindCreate {
				if !absent {
					code, diag := errcat.IntegrationRefRace, fmt.Sprintf("external race before apply: created ref %s already exists at %s", refName, currentSHA)
					if journal.AnyApplied() {
						entry.ApplyState = feature.RepoApplyAttention
						return o.rollbackTransaction(child, parent, journal, i, entryFinding(entry, code, diag))
					}
					return o.parkApplyAttention(child, journal, entry, code, diag)
				}
				continue
			}
			if ref.RefKind() == feature.RepoRefKindDelete {
				// A deleted ref must sit at its anchor: the transaction's
				// delete line is a compare-and-swap expecting the anchor, so
				// an already-absent ref — or one moved elsewhere — fails
				// closed here instead of inside the batch.
				if absent {
					code, diag := errcat.IntegrationRefRace, fmt.Sprintf("external race before apply: deleted ref %s is already absent; expected anchor %s", refName, ref.AnchorSHA)
					if journal.AnyApplied() {
						entry.ApplyState = feature.RepoApplyAttention
						return o.rollbackTransaction(child, parent, journal, i, entryFinding(entry, code, diag))
					}
					return o.parkApplyAttention(child, journal, entry, code, diag)
				}
				if currentSHA != ref.AnchorSHA {
					code, diag := errcat.IntegrationRefRace, fmt.Sprintf("external race before apply: ref %s anchor %s observed %s", refName, ref.AnchorSHA, currentSHA)
					if journal.AnyApplied() {
						entry.ApplyState = feature.RepoApplyAttention
						return o.rollbackTransaction(child, parent, journal, i, entryFinding(entry, code, diag))
					}
					return o.parkApplyAttention(child, journal, entry, code, diag)
				}
				continue
			}
			if absent {
				code, diag := errcat.IntegrationRefRace, fmt.Sprintf("external race before apply: ref %s is absent; expected anchor %s", refName, ref.AnchorSHA)
				if journal.AnyApplied() {
					entry.ApplyState = feature.RepoApplyAttention
					return o.rollbackTransaction(child, parent, journal, i, entryFinding(entry, code, diag))
				}
				return o.parkApplyAttention(child, journal, entry, code, diag)
			}
			if currentSHA != ref.AnchorSHA {
				code, diag := errcat.IntegrationRefRace, fmt.Sprintf("external race before apply: ref %s anchor %s observed %s", refName, ref.AnchorSHA, currentSHA)
				if journal.AnyApplied() {
					entry.ApplyState = feature.RepoApplyAttention
					return o.rollbackTransaction(child, parent, journal, i, entryFinding(entry, code, diag))
				}
				return o.parkApplyAttention(child, journal, entry, code, diag)
			}
		}

		// A pass-through entry rewrites no ref; apply only syncs the
		// worktree to the top ref's anchor.
		if entry.IsPassThrough() {
			if err := o.deps.Worktrees.ResetToCommit(parentWorktree, topRef.CandidateSHA); err != nil {
				code, diag := errcat.IntegrationWorktreeSyncFailed, fmt.Sprintf("syncing parent worktree for pass-through repo %s: %v", entry.Repo, err)
				if journal.AnyApplied() {
					entry.ApplyState = feature.RepoApplyAttention
					return o.rollbackTransaction(child, parent, journal, i, entryFinding(entry, code, diag))
				}
				return o.parkApplyAttention(child, journal, entry, code, diag)
			}
			entry.ApplyState = feature.RepoApplyApplied
			markEntryRefsObserved(entry)
			if err := o.persistTransaction(child.ID, journal); err != nil {
				return fmt.Errorf("recording pass-through apply progress for repo %s: %w", entry.Repo, err)
			}
			continue
		}

		// One multi-ref compare-and-swap transaction per repository: for an
		// entry that appends layers it verifies the previous top at its
		// recorded tip and creates every appended layer's ref expecting
		// absence; for every other entry it moves each ref from its anchor
		// to its candidate. Either every line applies or none does.
		updates := appendRefUpdates(entry)
		if err := o.deps.Worktrees.UpdateRefsTransaction(parentRepo.Path, updates); err != nil {
			observeEntryRefs(o, parentRepo.Path, entry)
			finding := refsUpdateFinding(entry, err)
			if journal.AnyApplied() {
				entry.ApplyState = feature.RepoApplyAttention
				return o.rollbackTransaction(child, parent, journal, i, finding)
			}
			return o.parkApplyAttention(child, journal, entry, finding.code, finding.diagnostics)
		}

		// Mark the entry as applied immediately after the transaction
		// succeeds so a crash or worktree-sync failure preserves the durable
		// ref updates and closure can finish syncing them idempotently.
		entry.ApplyState = feature.RepoApplyApplied
		markEntryRefsObserved(entry)
		if err := o.persistTransaction(child.ID, journal); err != nil {
			return fmt.Errorf("recording apply progress for repo %s: %w", entry.Repo, err)
		}

		// Sync the parent worktree to the entry's new top. An entry that
		// appends layers switches the worktree onto the new top layer's
		// branch — created by the transaction at its candidate — so the
		// existing parent layers' refs are never rewritten, and points the
		// repository record and worktree setup task at the new branch.
		// An entry whose previous top was deleted already switched the
		// worktree onto the new top branch before the transaction, which
		// then moved that branch's ref underneath the checkout; its sync is
		// a hard reset to the top candidate. Every other entry resets the
		// worktree to the top ref's candidate; the worktree was verified
		// clean during preparation, so the hard reset is safe. If the sync
		// fails, every ref is already durably at its candidate: preserve
		// the successful transaction, set the typed pending-sync flag
		// closure retries automatically, and carry on without an attention
		// record.
		if entry.PreviousTop != nil {
			syncFailed := false
			if previousTopDeleted(entry) {
				if err := o.deps.Worktrees.ResetToCommit(parentWorktree, topRef.CandidateSHA); err != nil {
					syncFailed = true
				}
			} else if err := o.deps.Worktrees.SwitchBranch(parentWorktree, topRef.Branch); err != nil {
				syncFailed = true
			}
			if syncFailed {
				entry.PendingSync = true
				pendingWorktreeSync = true
				if err := o.persistTransaction(child.ID, journal); err != nil {
					return fmt.Errorf("recording pending worktree sync for repo %s: %w", entry.Repo, err)
				}
			}
			if err := o.moveParentRepoBranch(parent.ID, entry.Repo, topRef.Branch); err != nil {
				return fmt.Errorf("recording parent branch for repo %s: %w", entry.Repo, err)
			}
			continue
		}
		if err := o.deps.Worktrees.ResetToCommit(parentWorktree, topRef.CandidateSHA); err != nil {
			entry.PendingSync = true
			pendingWorktreeSync = true
			if err := o.persistTransaction(child.ID, journal); err != nil {
				return fmt.Errorf("recording pending worktree sync for repo %s: %w", entry.Repo, err)
			}
			continue
		}
	}

	// All candidates applied successfully.
	journal.Phase = feature.TransactionPhaseApplied
	if err := o.persistTransaction(child.ID, journal); err != nil {
		return fmt.Errorf("recording applied transaction: %w", err)
	}
	if pendingWorktreeSync {
		o.emitEvent(ports.Event{
			Type:      ports.RelationshipIntegrationChanged,
			FeatureID: child.ID,
			ParentID:  child.Parent.ParentID,
			ChildID:   child.ID,
			Message:   "worktree sync pending; will retry at closure",
		})
	}
	return nil
}

// syncRolledBackEntryWorktree re-syncs an already-rolled-back entry's parent
// worktree after a crash interrupted the rollback between the ref
// transaction and the worktree step: an entry carrying a previous-top record
// switches back to the previous top branch — for an entry whose previous top
// was deleted, the rollback transaction recreated it — and every other entry
// resets to the top anchor.
func (o *Orchestrator) syncRolledBackEntryWorktree(parent *feature.Feature, entry *feature.RepoTransactionEntry, parentWorktree string) error {
	if entry.PreviousTop != nil {
		return o.deps.Worktrees.SwitchBranch(parentWorktree, entry.PreviousTop.Branch)
	}
	if top := entry.TopRef(); top != nil {
		return o.deps.Worktrees.ResetToCommit(parentWorktree, top.AnchorSHA)
	}
	return nil
}

// markEntryRefsObserved records every ref's candidate as its observed SHA
// after a successful transaction or pass-through sync.
func markEntryRefsObserved(entry *feature.RepoTransactionEntry) {
	for j := range entry.Refs {
		if entry.Refs[j].CandidateSHA != "" {
			entry.Refs[j].ObservedSHA = entry.Refs[j].CandidateSHA
		}
	}
}

// observeEntryRefs re-reads every ref of the entry after a failed
// transaction so the stored observed SHAs diagnose the race. A created ref
// that is absent — its expected pre-transaction state — records an empty
// observed SHA.
func observeEntryRefs(o *Orchestrator, repoPath string, entry *feature.RepoTransactionEntry) {
	if o.deps.Worktrees == nil {
		return
	}
	for j := range entry.Refs {
		ref := &entry.Refs[j]
		observed, absent, err := o.deps.Worktrees.RefSHAOrAbsent(repoPath, "refs/heads/"+ref.Branch)
		if err != nil {
			continue
		}
		if absent {
			ref.ObservedSHA = ""
			continue
		}
		ref.ObservedSHA = observed
	}
}

// refsUpdateFinding classifies a failed multi-ref transaction at the
// workflow boundary, including the compare-and-swap race subtype.
func refsUpdateFinding(entry *feature.RepoTransactionEntry, err error) integrationFinding {
	var casErr *git.RefCASMismatchError
	if errors.As(err, &casErr) {
		return entryFinding(entry, errcat.IntegrationRefRace,
			fmt.Sprintf("ref %s expected %s observed %s", casErr.Ref, casErr.Expected, casErr.Observed))
	}
	return entryFinding(entry, errcat.IntegrationCandidateFailed,
		fmt.Sprintf("updating refs for repo %s: %v", entry.Repo, err))
}

// rollbackTransaction conditionally restores each earlier applied
// repository's refs from their candidates to their recorded anchors without
// deleting or rewriting commits. Per repository it classifies every ref
// against its anchor and candidate: refs still at their candidate are rolled
// back together in one transaction and the worktree reset to the top anchor;
// refs already at their anchor count as rolled back; a ref anywhere else is
// an external race that marks the entry attention and leaves every ref of
// that repository untouched. The failedIndex is the index of the repo that
// failed (and is not itself rolled back); failed is its classification when
// the rollback was triggered by an apply failure, nil when resuming a durable
// rollback.
func (o *Orchestrator) rollbackTransaction(child, parent *feature.Feature, journal *feature.TransactionJournal, failedIndex int, failed integrationFinding) error {
	if o.deps.Worktrees == nil {
		return fmt.Errorf("transaction: ref CAS operations are not configured")
	}

	journal.Phase = feature.TransactionPhaseRollingBack
	if err := o.persistTransaction(child.ID, journal); err != nil {
		return fmt.Errorf("recording rollback start: %w", err)
	}

	// The repo that failed apply classifies the aggregate record; every
	// other failing repository joins the block after it.
	findings := make([]integrationFinding, 0, len(journal.Entries))
	if failed.code != "" {
		findings = append(findings, failed)
	}

	rollbackFailed := false
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		if i == failedIndex {
			continue
		}
		// An entry already marked rolled_back may still need its worktree
		// synced if a crash interrupted the rollback between the ref
		// transaction and the worktree reset. Ensure the worktree matches
		// the top anchor.
		if entry.ApplyState == feature.RepoApplyRolledBack {
			parentRepo := featureRepoByName(parent, entry.Repo)
			if parentRepo != nil {
				parentWorktree := parentRepo.WorktreePath
				if parentWorktree == "" {
					parentWorktree = parentRepo.Path
				}
				if err := o.syncRolledBackEntryWorktree(parent, entry, parentWorktree); err != nil {
					entry.ApplyState = feature.RepoApplyAttention
					rollbackFailed = true
					findings = append(findings, entryFinding(entry, errcat.IntegrationWorktreeSyncFailed,
						fmt.Sprintf("syncing parent worktree after rolled-back recovery for repo %s: %v", entry.Repo, err)))
				}
			}
			continue
		}
		if entry.ApplyState != feature.RepoApplyApplied {
			continue
		}

		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			entry.ApplyState = feature.RepoApplyAttention
			rollbackFailed = true
			findings = append(findings, entryFinding(entry, errcat.IntegrationRepositoryMissing,
				fmt.Sprintf("parent no longer has repository %s during rollback", entry.Repo)))
			continue
		}

		// Classify every ref — absent-aware, so a created ref's absence plays
		// the anchor's role — against its anchor and candidate. A racing ref
		// leaves the whole repository untouched and parks the entry.
		rollbackRefs := make([]git.RefUpdate, 0, len(entry.Refs))
		raced := false
		for j := range entry.Refs {
			ref := &entry.Refs[j]
			refName := "refs/heads/" + ref.Branch
			currentSHA, absent, err := o.deps.Worktrees.RefSHAOrAbsent(parentRepo.Path, refName)
			if err != nil {
				entry.ApplyState = feature.RepoApplyAttention
				rollbackFailed = true
				findings = append(findings, entryFinding(entry, errcat.IntegrationCandidateFailed,
					fmt.Sprintf("reading ref %s during rollback: %v", refName, err)))
				raced = true
				break
			}
			ref.ObservedSHA = currentSHA
			switch ref.Classify(currentSHA, absent) {
			case feature.RefAtCandidate:
				// A created ref still at its candidate is deleted; a deleted
				// ref still absent is recreated at its anchor; a rewrite ref
				// is restored to its anchor.
				rollbackRefs = append(rollbackRefs, rollbackRefUpdateFor(ref))
			case feature.RefAtAnchor:
				// Already rolled back — a created ref's absence and a
				// deleted ref's presence at its anchor included.
			default:
				// External process moved the ref; preserve it and record
				// attention for the whole repository.
				entry.ApplyState = feature.RepoApplyAttention
				rollbackFailed = true
				observed := currentSHA
				if absent {
					observed = "absent"
				}
				findings = append(findings, entryFinding(entry, errcat.IntegrationRefRace,
					fmt.Sprintf("external race before rollback: ref %s candidate %s observed %s", refName, ref.CandidateSHA, observed)))
				raced = true
			}
			if raced {
				break
			}
		}
		if raced {
			continue
		}

		// One transaction rolls back every ref still at its candidate:
		// created refs are deleted, deleted refs recreated, rewrite refs
		// restored.
		if len(rollbackRefs) > 0 {
			if err := o.deps.Worktrees.UpdateRefsTransaction(parentRepo.Path, rollbackRefs); err != nil {
				observeEntryRefs(o, parentRepo.Path, entry)
				findings = append(findings, refsUpdateFinding(entry, err))
				entry.ApplyState = feature.RepoApplyAttention
				rollbackFailed = true
				continue
			}
		}

		// Sync the parent worktree back: an entry that appended layers
		// switches back to the previous top branch — its ref was only ever
		// verified, never moved — and restores the repository record; an
		// entry whose previous top was deleted switches back to the branch
		// the rollback transaction just recreated at its anchor; every
		// other entry resets to the top anchor.
		parentWorktree := parentRepo.WorktreePath
		if parentWorktree == "" {
			parentWorktree = parentRepo.Path
		}
		if entry.PreviousTop != nil {
			if err := o.deps.Worktrees.SwitchBranch(parentWorktree, entry.PreviousTop.Branch); err != nil {
				entry.ApplyState = feature.RepoApplyAttention
				rollbackFailed = true
				findings = append(findings, entryFinding(entry, errcat.IntegrationWorktreeSyncFailed,
					fmt.Sprintf("switching parent worktree back to %s after rollback for repo %s: %v", entry.PreviousTop.Branch, entry.Repo, err)))
				continue
			}
			if err := o.moveParentRepoBranch(parent.ID, entry.Repo, entry.PreviousTop.Branch); err != nil {
				return fmt.Errorf("restoring parent branch record for repo %s: %w", entry.Repo, err)
			}
		} else if top := entry.TopRef(); top != nil {
			if err := o.deps.Worktrees.ResetToCommit(parentWorktree, top.AnchorSHA); err != nil {
				entry.ApplyState = feature.RepoApplyAttention
				rollbackFailed = true
				findings = append(findings, entryFinding(entry, errcat.IntegrationWorktreeSyncFailed,
					fmt.Sprintf("syncing parent worktree after rollback for repo %s: %v", entry.Repo, err)))
				continue
			}
		}

		entry.ApplyState = feature.RepoApplyRolledBack
		for j := range entry.Refs {
			entry.Refs[j].ObservedSHA = entry.Refs[j].AnchorSHA
		}
		if err := o.persistTransaction(child.ID, journal); err != nil {
			return fmt.Errorf("recording rollback progress for repo %s: %w", entry.Repo, err)
		}
	}

	// If the failed entry (skipped during rollback) or any other entry is
	// in attention state, the aggregate must be attention — not rolled_back —
	// so the externally moved ref is preserved as a precise, durable attention
	// record rather than being cleared and re-prepared from scratch.
	anyAttention := rollbackFailed
	if !anyAttention {
		for i := range journal.Entries {
			if journal.Entries[i].ApplyState == feature.RepoApplyAttention {
				anyAttention = true
				break
			}
		}
	}

	if anyAttention {
		journal.Phase = feature.TransactionPhaseAttention
		findings = appendResidualAttentionFindings(findings, journal)
		if err := o.parkIntegrationAttention(child, journal, findings); err != nil {
			return fmt.Errorf("recording rollback attention: %w", err)
		}
		return nil
	}

	// The clean rolled-back outcome records integration_rolled_back with the
	// repository that failed apply when it is known.
	journal.Phase = feature.TransactionPhaseRolledBack
	journal.Attention = rolledBackRecord(failed, journal)
	if err := o.persistTransaction(child.ID, journal); err != nil {
		return fmt.Errorf("recording rolled-back transaction: %w", err)
	}
	return o.emitTransactionAttention(child, journal.Attention)
}

// appendResidualAttentionFindings adds one finding for every entry that
// carries apply attention into the record without a re-derived
// classification (a resumed rollback interrupted before this process), so
// the repositories block lists every affected repository.
func appendResidualAttentionFindings(findings []integrationFinding, journal *feature.TransactionJournal) []integrationFinding {
	represented := make(map[string]bool, len(findings))
	for _, finding := range findings {
		represented[finding.ctx.Name] = true
	}
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		if entry.ApplyState == feature.RepoApplyAttention && !represented[entry.Repo] {
			findings = append(findings, entryFinding(entry, errcat.IntegrationCandidateFailed,
				"apply attention resumed from an interrupted rollback"))
		}
	}
	return findings
}

// rolledBackRecord builds the integration_rolled_back record for the clean
// rolled-back outcome: the repository that failed apply when known, else the
// entries that carried apply attention into a resumed rollback, else no
// repositories (the static summary applies).
func rolledBackRecord(failed integrationFinding, journal *feature.TransactionJournal) *errcat.FailureRecord {
	if failed.code != "" {
		failed.code = errcat.IntegrationRolledBack
		failed.diagnostics = "apply failed; earlier applied refs were rolled back"
		return findingsRecord([]integrationFinding{failed})
	}
	var findings []integrationFinding
	for i := range journal.Entries {
		if journal.Entries[i].ApplyState == feature.RepoApplyAttention {
			findings = append(findings, entryFinding(&journal.Entries[i], errcat.IntegrationRolledBack,
				"apply failed; earlier applied refs were rolled back"))
		}
	}
	if record := findingsRecord(findings); record != nil {
		return record
	}
	return &errcat.FailureRecord{Code: errcat.IntegrationRolledBack}
}

func rebasePassThroughRepo(child *feature.Feature, repoName string) bool {
	return child != nil &&
		child.Parent != nil &&
		child.Parent.Kind == feature.ChildKindRebase &&
		!child.IsRebaseWorkRepo(repoName)
}

// parkApplyAttention records a no-rollback apply failure: sets the entry
// attention state, aggregate attention phase, and the stored record, persists
// the journal, and emits the attention event. Used for every apply failure
// that does not require compensation of earlier applied refs.
func (o *Orchestrator) parkApplyAttention(child *feature.Feature, journal *feature.TransactionJournal, entry *feature.RepoTransactionEntry, code errcat.Code, diagnostics string) error {
	entry.ApplyState = feature.RepoApplyAttention
	journal.Phase = feature.TransactionPhaseAttention
	return o.parkIntegrationAttention(child, journal, []integrationFinding{entryFinding(entry, code, diagnostics)})
}

// persistTransaction durably records the transaction journal on the child
// feature record.
func (o *Orchestrator) persistTransaction(childID string, journal *feature.TransactionJournal) error {
	var parentID string
	var changed bool
	err := o.deps.Store.Modify(childID, func(f *feature.Feature) error {
		parentID = f.Parent.ParentID
		changed = !reflect.DeepEqual(f.Parent.Transaction, journal)
		f.Parent.Transaction = journal
		return nil
	})
	if err != nil || !changed {
		return err
	}
	o.emitEvent(ports.Event{
		Type:      ports.RelationshipIntegrationChanged,
		FeatureID: childID,
		ParentID:  parentID,
		ChildID:   childID,
		Message:   "relationship integration state changed",
	})
	return nil
}

// validateTransactionParent confirms every child repository still maps to a
// parent repository with the same path.
func validateTransactionParent(child, parent *feature.Feature) error {
	if parent == nil || parent.IsChild() {
		return fmt.Errorf("%w: parent record %s no longer matches the relationship", ErrChildIntegrationRefused, child.Parent.ParentID)
	}
	for _, childRepo := range child.Repos {
		parentRepo := featureRepoByName(parent, childRepo.Name)
		if parentRepo == nil {
			return fmt.Errorf("%w: parent %s no longer has repository %s", ErrChildIntegrationRefused, parent.ID, childRepo.Name)
		}
		if parentRepo.Path != childRepo.Path {
			return fmt.Errorf("%w: repository %s path changed since launch (%s != %s)", ErrChildIntegrationRefused, childRepo.Name, parentRepo.Path, childRepo.Path)
		}
	}
	return nil
}

// transactionRefVector reads the current SHA of every listed ref of every
// journal entry, in journal order.
func (o *Orchestrator) transactionRefVector(parent *feature.Feature, journal *feature.TransactionJournal) ([][]string, error) {
	vector := make([][]string, 0, len(journal.Entries))
	for _, entry := range journal.Entries {
		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			return nil, fmt.Errorf("parent no longer has repository %s", entry.Repo)
		}
		current := make([]string, 0, len(entry.Refs))
		for _, ref := range entry.Refs {
			sha, absent, err := o.deps.Worktrees.RefSHAOrAbsent(parentRepo.Path, "refs/heads/"+ref.Branch)
			if err != nil {
				return nil, fmt.Errorf("reading ref %s for repo %s: %w", ref.Branch, entry.Repo, err)
			}
			if absent {
				// A created ref that does not exist yet sits at its anchor:
				// absence is the empty anchor SHA.
				sha = ""
			}
			current = append(current, sha)
		}
		vector = append(vector, current)
	}
	return vector, nil
}

// transactionProducedSHA reports whether the prior persisted journal staged
// sha as a candidate on any ref, so a resumed or partially applied
// transaction is not mistaken for external parent drift.
func transactionProducedSHA(journal *feature.TransactionJournal, sha string) bool {
	if journal == nil || sha == "" {
		return false
	}
	for i := range journal.Entries {
		for j := range journal.Entries[i].Refs {
			if journal.Entries[i].Refs[j].CandidateSHA == sha {
				return true
			}
		}
	}
	return false
}

// transactionAcknowledgedDrift reports whether a prior drift attention
// record already recorded sha as the repo's moved tip: drift parks
// integration exactly once, and a retry at the unchanged tip absorbs it. Any
// further movement parks again.
func transactionAcknowledgedDrift(journal *feature.TransactionJournal, repo, sha string) bool {
	if journal == nil || sha == "" {
		return false
	}
	if journal.Attention == nil || journal.Attention.Code != errcat.IntegrationParentRefDrift {
		return false
	}
	entry := journal.EntryByRepo(repo)
	if entry == nil {
		return false
	}
	if top := entry.TopRef(); top != nil {
		return top.AnchorSHA == sha
	}
	// An append-preparation entry records no refs until staging; its
	// previous-top tip carries the observed parent tip instead.
	if prev := entry.PreviousTopRef(); prev != nil {
		return prev.TipSHA == sha
	}
	return false
}

// transactionNeedsRebuild checks whether any listed ref has changed since
// candidates were prepared, requiring a candidate rebuild.
func transactionNeedsRebuild(journal *feature.TransactionJournal, currentRefs [][]string) bool {
	if journal == nil || len(journal.Entries) != len(currentRefs) {
		return true
	}
	for i := range journal.Entries {
		if len(journal.Entries[i].Refs) != len(currentRefs[i]) {
			return true
		}
		for j := range journal.Entries[i].Refs {
			if journal.Entries[i].Refs[j].AnchorSHA != currentRefs[i][j] {
				return true
			}
		}
	}
	return false
}
