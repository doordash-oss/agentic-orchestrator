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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// This file implements the transactional multi-repository integration
// coordinator: prepare merge candidates without advancing parent refs,
// apply them conditionally with compare-and-swap ref updates, and compensate
// only changes the transaction can prove it made.

// prepareTransactionCandidates prepares an explicit two-parent no-fast-forward
// merge candidate for every inherited repository without changing any parent
// ref or worktree. It commits remaining child changes, validates the parent
// repository mapping, collects cleanliness diagnostics across all parent
// worktrees in one preflight, and stages the candidate vector. Each child head,
// initial parent anchor, latest expected target ref, candidate commit, and
// preparation outcome is persisted before application.
//
// The parent is locked while a pass runs, so a parent tip that moved away
// from its creation-time base — other than to a commit this transaction
// itself produced — is external drift: preparation parks at attention with
// GateCodeParentDrift before any staging. A dirty repository, conflict, or
// preparation failure likewise leaves every parent ref unchanged and records
// all affected repositories as attention.
func (o *Orchestrator) prepareTransactionCandidates(child, parent *feature.Feature) (*feature.TransactionJournal, error) {
	if o.deps.Worktrees == nil {
		return nil, fmt.Errorf("transaction: worktree operations are not configured")
	}

	// Rebase mechanical integration gate: the single kind-specific pre-prepare
	// step. For a rebase child, re-verify the git-level exit criteria for
	// every behind repo against the creation-time persisted targets before
	// any candidate or ref is touched. Any violation parks the transaction
	// at attention with typed per-repo diagnostics and leaves every parent
	// ref byte-identical. Refactor and review-feedback children flow through
	// unchanged.
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

	// Validate every parent repo and capture per-repo entries.
	var allDirty []feature.RepoDirtyDiagnostics
	var driftedRepos []string
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

		entry := feature.RepoTransactionEntry{
			Repo:            childRepo.Name,
			ParentBranch:    parentRepo.Branch,
			ParentAnchorSHA: parentTip,
			ExpectedRefSHA:  parentTip,
			ChildHeadSHA:    childHead,
			PrepState:       feature.RepoPrepPending,
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
			entry.GateCode = feature.GateCodeParentDrift
			entry.Diagnostics = fmt.Sprintf("parent branch tip moved from %s to %s while the pass was running; the parent is locked during a pass, so this usually means something wrote to the parent's checkout outside the integration transaction; parent refs were left untouched — retry integration to accept the moved tip, or reset the parent branch before retrying", base, parentTip)
			driftedRepos = append(driftedRepos, childRepo.Name)
		}

		if report.Dirty() {
			entry.PrepState = feature.RepoPrepFailed
			entry.Dirty = []feature.RepoDirtyDiagnostics{{
				Repo:           childRepo.Name,
				Path:           parentWorktree,
				Staged:         report.Staged,
				Unstaged:       report.Unstaged,
				Untracked:      report.Untracked,
				StagedTotal:    report.StagedTotal,
				UnstagedTotal:  report.UnstagedTotal,
				UntrackedTotal: report.UntrackedTotal,
			}}
			allDirty = append(allDirty, entry.Dirty[0])
		}

		journal.Entries = append(journal.Entries, entry)
	}

	// If any parent tip drifted, park the transaction with aggregated drift
	// diagnostics before any staging. All parent refs are unchanged. Carry
	// candidate provenance from the prior journal across the overwrite so a
	// ref this transaction already moved is not reclassified as drift on
	// retry.
	if len(driftedRepos) > 0 {
		if prior := child.Parent.Transaction; prior != nil {
			for i := range journal.Entries {
				if pe := prior.EntryByRepo(journal.Entries[i].Repo); pe != nil && journal.Entries[i].CandidateSHA == "" {
					journal.Entries[i].CandidateSHA = pe.CandidateSHA
					journal.Entries[i].ApplyState = pe.ApplyState
				}
			}
		}
		journal.Phase = feature.TransactionPhaseAttention
		journal.Attention = "parent branch tips moved outside the integration transaction: " + strings.Join(driftedRepos, ", ")
		if err := o.persistTransaction(child.ID, journal); err != nil {
			return nil, fmt.Errorf("recording parent drift attention: %w", err)
		}
		return nil, o.emitTransactionAttention(child, journal.Attention)
	}

	// If any parent is dirty, park the transaction with aggregated dirty
	// diagnostics. All parent refs are unchanged.
	if len(allDirty) > 0 {
		journal.Phase = feature.TransactionPhaseAttention
		journal.Attention = "parent worktrees have uncommitted changes"
		if err := o.persistTransaction(child.ID, journal); err != nil {
			return nil, fmt.Errorf("recording dirty attention: %w", err)
		}
		return nil, o.emitTransactionAttention(child, "parent worktrees have uncommitted changes")
	}

	// Stage candidates for every repository without advancing any parent ref.
	// Most candidates are explicit two-parent no-ff merge commits created in a
	// temporary detached worktree. A rebase repo that was already up to date at
	// child creation is a pass-through candidate whose SHA is the parent anchor.
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			entry.PrepState = feature.RepoPrepFailed
			entry.Diagnostics = fmt.Sprintf("parent no longer has repository %s", entry.Repo)
			journal.Phase = feature.TransactionPhaseAttention
			journal.Attention = entry.Diagnostics
			if err := o.persistTransaction(child.ID, journal); err != nil {
				return nil, fmt.Errorf("recording missing repo during staging: %w", err)
			}
			return nil, o.emitTransactionAttention(child, entry.Diagnostics)
		}
		if rebasePassThroughRepo(child, entry.Repo) {
			if !git.IsAncestor(parentRepo.Path, entry.ChildHeadSHA, entry.ParentAnchorSHA) {
				entry.PrepState = feature.RepoPrepFailed
				entry.Diagnostics = fmt.Sprintf("rebase child modified up-to-date repo %s; only repos behind at launch may change", entry.Repo)
				journal.Phase = feature.TransactionPhaseAttention
				journal.Attention = entry.Diagnostics
				if err := o.persistTransaction(child.ID, journal); err != nil {
					return nil, fmt.Errorf("recording pass-through prep failure: %w", err)
				}
				return nil, o.emitTransactionAttention(child, entry.Diagnostics)
			}
			entry.CandidateSHA = entry.ParentAnchorSHA
			entry.PrepState = feature.RepoPrepPrepared
			if err := o.persistTransaction(child.ID, journal); err != nil {
				return nil, fmt.Errorf("recording pass-through candidate for repo %s: %w", entry.Repo, err)
			}
			continue
		}

		message := fmt.Sprintf("Merge %s child %s into %s (%s)", child.Parent.Kind, child.ID, entry.ParentBranch, entry.Repo)
		result, err := o.deps.Worktrees.CreateMergeCandidate(parentRepo.Path, entry.ParentAnchorSHA, entry.ChildHeadSHA, message)
		if err != nil {
			var conflictErr *git.MergeCandidateConflictError
			if errors.As(err, &conflictErr) {
				entry.PrepState = feature.RepoPrepFailed
				entry.ConflictFiles = conflictErr.ConflictFiles
				entry.Diagnostics = fmt.Sprintf("merge conflict in repo %s: %v", entry.Repo, conflictErr.ConflictFiles)
				journal.Phase = feature.TransactionPhaseAttention
				journal.Attention = entry.Diagnostics
				if err := o.persistTransaction(child.ID, journal); err != nil {
					return nil, fmt.Errorf("recording conflict attention: %w", err)
				}
				return nil, o.emitTransactionAttention(child, entry.Diagnostics)
			}
			entry.PrepState = feature.RepoPrepFailed
			entry.Diagnostics = fmt.Sprintf("merge candidate failed for repo %s: %v", entry.Repo, err)
			journal.Phase = feature.TransactionPhaseAttention
			journal.Attention = entry.Diagnostics
			if err := o.persistTransaction(child.ID, journal); err != nil {
				return nil, fmt.Errorf("recording prep failure: %w", err)
			}
			return nil, o.emitTransactionAttention(child, entry.Diagnostics)
		}
		entry.CandidateSHA = result.CandidateSHA
		entry.PrepState = feature.RepoPrepPrepared
		// Persist progress after each candidate is prepared so a crash
		// can resume from the durable state.
		if err := o.persistTransaction(child.ID, journal); err != nil {
			return nil, fmt.Errorf("recording candidate for repo %s: %w", entry.Repo, err)
		}
	}

	journal.Phase = feature.TransactionPhasePrepared
	if err := o.persistTransaction(child.ID, journal); err != nil {
		return nil, fmt.Errorf("recording prepared transaction: %w", err)
	}
	return journal, nil
}

// applyTransactionCandidates applies the fully prepared candidate vector with
// compare-and-swap ref updates against each recorded expected parent tip. It
// durably tracks progress after every repository. Rebase pass-through
// candidates are confirmed without moving refs. If a later update fails, it
// compensates earlier updates only when their refs still equal the
// transaction's candidate commits. External ref movement is never overwritten:
// ambiguous states remain intact and become integration attention.
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

	// Apply each candidate in order. Compare-and-swap: update the parent
	// branch ref to the candidate SHA only if it still equals the expected
	// old SHA.
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		if entry.ApplyState == feature.RepoApplyApplied {
			continue
		}

		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			diag := fmt.Sprintf("parent no longer has repository %s", entry.Repo)
			if journal.AnyApplied() {
				entry.Diagnostics = diag
				entry.ApplyState = feature.RepoApplyAttention
				return o.rollbackTransaction(child, parent, journal, i)
			}
			return o.parkApplyAttention(child, journal, entry, diag)
		}

		// Verify the parent worktree has the recorded branch checked out.
		parentWorktree := parentRepo.WorktreePath
		if parentWorktree == "" {
			parentWorktree = parentRepo.Path
		}
		if current := o.deps.Worktrees.CurrentBranch(parentWorktree); current != entry.ParentBranch {
			diag := fmt.Sprintf("parent worktree %s has branch %q checked out; integration requires the recorded parent branch %q", parentWorktree, current, entry.ParentBranch)
			if journal.AnyApplied() {
				entry.Diagnostics = diag
				entry.ApplyState = feature.RepoApplyAttention
				return o.rollbackTransaction(child, parent, journal, i)
			}
			return o.parkApplyAttention(child, journal, entry, diag)
		}

		ref := "refs/heads/" + entry.ParentBranch
		// Read the current ref to detect external movement.
		currentSHA, err := o.deps.Worktrees.RefSHA(parentRepo.Path, ref)
		if err != nil {
			diag := fmt.Sprintf("reading ref %s: %v", ref, err)
			if journal.AnyApplied() {
				entry.Diagnostics = diag
				entry.ApplyState = feature.RepoApplyAttention
				return o.rollbackTransaction(child, parent, journal, i)
			}
			return o.parkApplyAttention(child, journal, entry, diag)
		}
		entry.ObservedSHA = currentSHA

		// If the ref has been externally moved, do not overwrite it.
		if currentSHA != entry.ExpectedRefSHA {
			diag := fmt.Sprintf("external race before apply: ref %s expected %s observed %s", ref, entry.ExpectedRefSHA, currentSHA)
			if journal.AnyApplied() {
				entry.Diagnostics = diag
				entry.ApplyState = feature.RepoApplyAttention
				return o.rollbackTransaction(child, parent, journal, i)
			}
			return o.parkApplyAttention(child, journal, entry, diag)
		}

		if passThroughCandidate(entry) {
			if err := o.deps.Worktrees.ResetToCommit(parentWorktree, entry.CandidateSHA); err != nil {
				diag := fmt.Sprintf("syncing parent worktree for pass-through repo %s: %v", entry.Repo, err)
				if journal.AnyApplied() {
					entry.Diagnostics = diag
					entry.ApplyState = feature.RepoApplyAttention
					return o.rollbackTransaction(child, parent, journal, i)
				}
				return o.parkApplyAttention(child, journal, entry, diag)
			}
			entry.ApplyState = feature.RepoApplyApplied
			entry.MergeHEAD = entry.CandidateSHA
			entry.ObservedSHA = entry.CandidateSHA
			if err := o.persistTransaction(child.ID, journal); err != nil {
				return fmt.Errorf("recording pass-through apply progress for repo %s: %w", entry.Repo, err)
			}
			continue
		}

		// Compare-and-swap ref update.
		if err := o.deps.Worktrees.UpdateRef(parentRepo.Path, ref, entry.ExpectedRefSHA, entry.CandidateSHA); err != nil {
			observed, _ := o.deps.Worktrees.RefSHA(parentRepo.Path, ref)
			entry.ObservedSHA = observed
			var casErr *git.RefCASMismatchError
			if errors.As(err, &casErr) {
				entry.Diagnostics = fmt.Sprintf("CAS mismatch on apply: ref %s expected %s observed %s", ref, casErr.Expected, casErr.Observed)
			} else {
				entry.Diagnostics = fmt.Sprintf("apply ref update failed for repo %s: %v", entry.Repo, err)
			}
			if journal.AnyApplied() {
				entry.ApplyState = feature.RepoApplyAttention
				return o.rollbackTransaction(child, parent, journal, i)
			}
			return o.parkApplyAttention(child, journal, entry, entry.Diagnostics)
		}

		// Mark the entry as applied immediately after the CAS succeeds so a
		// crash or worktree-sync failure preserves the durable ref update and
		// closure can finish syncing it idempotently.
		entry.ApplyState = feature.RepoApplyApplied
		entry.MergeHEAD = entry.CandidateSHA
		entry.ObservedSHA = entry.CandidateSHA
		if err := o.persistTransaction(child.ID, journal); err != nil {
			return fmt.Errorf("recording apply progress for repo %s: %w", entry.Repo, err)
		}

		// Sync the parent worktree to the new ref so the merge commit is
		// visible in the working directory. The worktree was verified clean
		// during preparation, so a hard reset is safe. If this fails, the
		// ref is already durably at the candidate: preserve the successful
		// CAS and let the idempotent closure sync retry the worktree.
		parentWorktree = parentRepo.WorktreePath
		if parentWorktree == "" {
			parentWorktree = parentRepo.Path
		}
		if err := o.deps.Worktrees.ResetToCommit(parentWorktree, entry.CandidateSHA); err != nil {
			entry.Diagnostics = fmt.Sprintf("worktree sync pending after apply for repo %s: %v", entry.Repo, err)
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

// rollbackTransaction conditionally restores each earlier applied ref from
// its candidate to its recorded old SHA without deleting or rewriting commits.
// It only rolls back refs that still equal the transaction's candidate
// commits. A CAS mismatch preserves the externally moved ref and produces
// attention diagnostics. The failedIndex is the index of the repo that failed
// (and is not itself rolled back).
func (o *Orchestrator) rollbackTransaction(child, parent *feature.Feature, journal *feature.TransactionJournal, failedIndex int) error {
	if o.deps.Worktrees == nil {
		return fmt.Errorf("transaction: ref CAS operations are not configured")
	}

	journal.Phase = feature.TransactionPhaseRollingBack
	if err := o.persistTransaction(child.ID, journal); err != nil {
		return fmt.Errorf("recording rollback start: %w", err)
	}

	rollbackFailed := false
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		if i == failedIndex {
			continue
		}
		// An entry already marked rolled_back may still need its worktree
		// synced if a crash interrupted the rollback between the ref CAS
		// and the worktree reset. Ensure the worktree matches the anchor.
		if entry.ApplyState == feature.RepoApplyRolledBack {
			parentRepo := featureRepoByName(parent, entry.Repo)
			if parentRepo != nil {
				parentWorktree := parentRepo.WorktreePath
				if parentWorktree == "" {
					parentWorktree = parentRepo.Path
				}
				if err := o.deps.Worktrees.ResetToCommit(parentWorktree, entry.ParentAnchorSHA); err != nil {
					entry.Diagnostics = fmt.Sprintf("syncing parent worktree after rolled-back recovery for repo %s: %v", entry.Repo, err)
					entry.ApplyState = feature.RepoApplyAttention
					rollbackFailed = true
				}
			}
			continue
		}
		if entry.ApplyState != feature.RepoApplyApplied {
			continue
		}

		if passThroughCandidate(entry) {
			entry.ApplyState = feature.RepoApplyRolledBack
			entry.ObservedSHA = entry.ParentAnchorSHA
			if err := o.persistTransaction(child.ID, journal); err != nil {
				return fmt.Errorf("recording pass-through rollback progress for repo %s: %w", entry.Repo, err)
			}
			continue
		}

		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			entry.Diagnostics = fmt.Sprintf("parent no longer has repository %s during rollback", entry.Repo)
			entry.ApplyState = feature.RepoApplyAttention
			rollbackFailed = true
			continue
		}

		ref := "refs/heads/" + entry.ParentBranch
		// Read the current ref to check if it still equals the candidate.
		currentSHA, err := o.deps.Worktrees.RefSHA(parentRepo.Path, ref)
		if err != nil {
			entry.Diagnostics = fmt.Sprintf("reading ref %s during rollback: %v", ref, err)
			entry.ApplyState = feature.RepoApplyAttention
			rollbackFailed = true
			continue
		}
		entry.ObservedSHA = currentSHA

		// Only roll back if the ref still equals the candidate commit.
		if currentSHA != entry.CandidateSHA {
			// External process moved the ref; preserve it and record
			// attention diagnostics.
			entry.Diagnostics = fmt.Sprintf("external race before rollback: ref %s candidate %s observed %s", ref, entry.CandidateSHA, currentSHA)
			entry.ApplyState = feature.RepoApplyAttention
			rollbackFailed = true
			continue
		}

		// Compare-and-swap rollback: restore the ref from candidate to old SHA.
		if err := o.deps.Worktrees.UpdateRef(parentRepo.Path, ref, entry.CandidateSHA, entry.ParentAnchorSHA); err != nil {
			observed, _ := o.deps.Worktrees.RefSHA(parentRepo.Path, ref)
			entry.ObservedSHA = observed
			var casErr *git.RefCASMismatchError
			if errors.As(err, &casErr) {
				entry.Diagnostics = fmt.Sprintf("CAS mismatch on rollback: ref %s candidate %s observed %s", ref, casErr.Expected, casErr.Observed)
			} else {
				entry.Diagnostics = fmt.Sprintf("rollback ref update failed for repo %s: %v", entry.Repo, err)
			}
			entry.ApplyState = feature.RepoApplyAttention
			rollbackFailed = true
			continue
		}

		// Sync the parent worktree back to the old SHA.
		parentWorktree := parentRepo.WorktreePath
		if parentWorktree == "" {
			parentWorktree = parentRepo.Path
		}
		if err := o.deps.Worktrees.ResetToCommit(parentWorktree, entry.ParentAnchorSHA); err != nil {
			entry.Diagnostics = fmt.Sprintf("syncing parent worktree after rollback for repo %s: %v", entry.Repo, err)
			entry.ApplyState = feature.RepoApplyAttention
			rollbackFailed = true
			continue
		}

		entry.ApplyState = feature.RepoApplyRolledBack
		entry.ObservedSHA = entry.ParentAnchorSHA
		if err := o.persistTransaction(child.ID, journal); err != nil {
			return fmt.Errorf("recording rollback progress for repo %s: %w", entry.Repo, err)
		}
	}

	// If the failed entry (skipped during rollback) or any other entry is
	// in attention state, the aggregate must be attention — not rolled_back —
	// so the externally moved ref is preserved as a precise, durable attention
	// state rather than being cleared and re-prepared from scratch.
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
		journal.Attention = transactionAttentionSummary(journal)
		if journal.Attention == "" {
			journal.Attention = "rollback completed with external races; some refs preserved"
		}
		if err := o.persistTransaction(child.ID, journal); err != nil {
			return fmt.Errorf("recording rollback attention: %w", err)
		}
		return o.emitTransactionAttention(child, journal.Attention)
	}

	journal.Phase = feature.TransactionPhaseRolledBack
	if err := o.persistTransaction(child.ID, journal); err != nil {
		return fmt.Errorf("recording rolled-back transaction: %w", err)
	}
	return o.emitTransactionAttention(child, "transaction rolled back after apply failure")
}

func rebasePassThroughRepo(child *feature.Feature, repoName string) bool {
	return child != nil &&
		child.Parent != nil &&
		child.Parent.Kind == feature.ChildKindRebase &&
		!child.IsRebaseBehindRepo(repoName)
}

func passThroughCandidate(entry *feature.RepoTransactionEntry) bool {
	return entry != nil &&
		entry.CandidateSHA != "" &&
		entry.CandidateSHA == entry.ParentAnchorSHA
}

// parkApplyAttention records a no-rollback apply failure: sets the per-repo
// diagnostic, entry attention state, aggregate attention phase, persists the
// journal, and emits the attention event. Used for every apply failure that
// does not require compensation of earlier applied refs.
func (o *Orchestrator) parkApplyAttention(child *feature.Feature, journal *feature.TransactionJournal, entry *feature.RepoTransactionEntry, diagnostics string) error {
	entry.Diagnostics = diagnostics
	entry.ApplyState = feature.RepoApplyAttention
	journal.Phase = feature.TransactionPhaseAttention
	journal.Attention = diagnostics
	if err := o.persistTransaction(child.ID, journal); err != nil {
		return fmt.Errorf("recording apply attention: %w", err)
	}
	return o.emitTransactionAttention(child, diagnostics)
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

// emitTransactionAttention notifies consumers that the child's transaction
// is parked at a retryable attention boundary.
func (o *Orchestrator) emitTransactionAttention(child *feature.Feature, attention string) error {
	o.emitEvent(ports.Event{
		Type:      ports.RelationshipIntegrationChanged,
		FeatureID: child.ID,
		ParentID:  child.Parent.ParentID,
		ChildID:   child.ID,
		Message:   "child integration needs attention: " + attention,
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

// transactionParentTipVector reads the current parent tip for every
// repository in the journal order.
func (o *Orchestrator) transactionParentTipVector(parent *feature.Feature, journal *feature.TransactionJournal) ([]string, error) {
	tips := make([]string, 0, len(journal.Entries))
	for _, entry := range journal.Entries {
		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			return nil, fmt.Errorf("parent no longer has repository %s", entry.Repo)
		}
		parentWorktree := parentRepo.WorktreePath
		if parentWorktree == "" {
			parentWorktree = parentRepo.Path
		}
		sha, err := o.childHeadSHA(parentWorktree)
		if err != nil {
			return nil, fmt.Errorf("reading parent tip for repo %s: %w", entry.Repo, err)
		}
		tips = append(tips, sha)
	}
	return tips, nil
}

// transactionProducedSHA reports whether the prior persisted journal staged
// sha as a candidate, so a resumed or partially applied transaction is not
// mistaken for external parent drift.
func transactionProducedSHA(journal *feature.TransactionJournal, sha string) bool {
	if journal == nil || sha == "" {
		return false
	}
	for i := range journal.Entries {
		if journal.Entries[i].CandidateSHA == sha {
			return true
		}
	}
	return false
}

// transactionAcknowledgedDrift reports whether a prior drift attention
// already recorded sha as the repo's moved tip: drift parks integration
// exactly once, and a retry at the unchanged tip absorbs it. Any further
// movement parks again.
func transactionAcknowledgedDrift(journal *feature.TransactionJournal, repo, sha string) bool {
	if journal == nil || sha == "" {
		return false
	}
	entry := journal.EntryByRepo(repo)
	return entry != nil && entry.GateCode == feature.GateCodeParentDrift && entry.ParentAnchorSHA == sha
}

// transactionNeedsRebuild checks whether the parent-tip vector has changed
// since candidates were prepared, requiring a candidate rebuild.
func transactionNeedsRebuild(journal *feature.TransactionJournal, currentTips []string) bool {
	if journal == nil || len(journal.Entries) != len(currentTips) {
		return true
	}
	for i := range journal.Entries {
		if journal.Entries[i].ParentAnchorSHA != currentTips[i] {
			return true
		}
	}
	return false
}

// transactionAttentionSummary builds a human-readable summary of all
// per-repo attention conditions in the journal.
func transactionAttentionSummary(journal *feature.TransactionJournal) string {
	if journal == nil {
		return ""
	}
	var parts []string
	for i := range journal.Entries {
		e := &journal.Entries[i]
		if e.Diagnostics != "" {
			parts = append(parts, fmt.Sprintf("%s: %s", e.Repo, e.Diagnostics))
		}
	}
	return strings.Join(parts, "; ")
}
