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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// This file owns the child execution and integration seam: a setup-complete
// Medium child runs the ordinary Plan/Implement/Review pipeline, and a
// successful final review enters an explicit local-integration stage instead
// of any child delivery path. A durable no-fast-forward merge boundary on
// the recorded parent branch closes the child, moves the parent to CodeReady,
// cleans disposable child resources, and only then evaluates the parent's
// current publish configuration — the child's Completed close and the
// parent's CodeReady transition are durable before publication begins, so a
// failed push or pull request never reopens the child or undoes the local
// merge.

// childHeadReader is the exact-HEAD lookup integration requires; it is
// satisfied by *git.WorktreeManager and asserted structurally so tests can
// substitute fakes without widening ports.WorktreeOperator.
type childHeadReader interface {
	CurrentHeadSHA(worktreePath string) (string, error)
}

// checkChildExecution is the fail-closed capability gate for child feature
// execution. Queued, setting-up, and failed-setup children stay reachable
// only through RunSetup / RetrySetup; setup-complete children must satisfy
// the supported execution shape (any active pipeline profile); a child
// whose relationship has settled can never replay pipeline phases.
// Feature-lookup failures propagate so the gate can never be silently
// skipped.
func (o *Orchestrator) checkChildExecution(featureID string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("loading feature: %w", err)
	}
	if !f.IsChild() {
		return nil
	}
	if !f.IsActiveChild() {
		return fmt.Errorf("%w: %s", feature.ErrChildExecutionClosed, featureID)
	}
	if !f.ChildSetupComplete() {
		return fmt.Errorf("%w: %s", feature.ErrChildExecutionBlocked, featureID)
	}
	return f.ChildExecutionCapability()
}

// ErrChildIntegrationRefused marks the durable refusal conditions under
// which integration never mutates the parent: closed relationship, parent
// record or repository mismatch, or a final review that is not durably
// approved.
var ErrChildIntegrationRefused = errors.New("child integration refused")

// ErrChildDiscardInProgress is returned when an integration or closure
// attempt is rejected because the child has a durable discard intent.
var ErrChildDiscardInProgress = errors.New("child discard in progress")

// RunChildIntegration carries an approved child through local integration,
// closure, cleanup, and the parent's publish decision. It acquires the
// relationship read lock for the entire integration so a concurrent
// DiscardChild (which holds the write lock to record its intent) cannot
// interleave. If a discard intent is already durable on the child, the
// integration is refused.
func (o *Orchestrator) RunChildIntegration(childID string) error {
	return o.WithRelationshipReadLock(func() error {
		return o.runChildIntegrationLocked(childID)
	})
}

// runChildIntegrationLocked is the lock-held integration entry point.
// Callers must hold the relationship read lock. RestartPhase, which already
// holds the read lock, calls this directly to avoid a recursive RLock.
func (o *Orchestrator) runChildIntegrationLocked(childID string) error {
	child, err := o.deps.Lifecycle.Get(childID)
	if err != nil {
		return fmt.Errorf("load child: %w", err)
	}
	if child == nil || !child.IsChild() {
		return fmt.Errorf("%w: feature is not a child feature", ErrChildIntegrationRefused)
	}
	if owned, ownershipErr := o.cascadeOwnsRelationship(child.Parent.ParentID); ownershipErr != nil {
		return ownershipErr
	} else if owned {
		return fmt.Errorf("%w: cascade delete owns child %s", feature.ErrParentMutationLocked, child.ID)
	}
	if child.IsDiscarding() {
		return fmt.Errorf("%w: child %s has discard in progress", ErrChildDiscardInProgress, child.ID)
	}
	if child.Parent.CloseOutcome != "" {
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			return fmt.Errorf("%w: child relationship %s already closed (%s)", ErrChildIntegrationRefused, child.ID, child.Parent.CloseOutcome)
		}
		if child.Parent.Transaction != nil && child.Parent.Transaction.Phase == feature.TransactionPhaseMerged {
			return o.settleChildClosureTail(childID, child.Parent.ParentID)
		}
		return fmt.Errorf("%w: child relationship %s already closed (%s)", ErrChildIntegrationRefused, child.ID, child.Parent.CloseOutcome)
	}
	if err := validateChildForIntegration(child); err != nil {
		return err
	}
	parent, err := o.deps.Lifecycle.Get(child.Parent.ParentID)
	if err != nil {
		return fmt.Errorf("%w: parent %s unreadable: %v", ErrChildIntegrationRefused, child.Parent.ParentID, err)
	}

	// Final KB refresh: revalidate workspace provenance and rebuild against
	// the final reviewed child HEAD before any parent ref is touched. Only a
	// complete, validated refresh vector may enter the integration path.
	if child.EffectivePipeline().HasPhase(feature.PhaseKnowledgeBase) {
		if err := o.RefreshChildKBWorkspaces(childID); err != nil {
			return fmt.Errorf("final KB refresh: %w", err)
		}
	}

	return o.runTransactionIntegration(childID, child, parent)
}

// validateChildForIntegration enforces the durable preconditions under which
// local integration may touch an open child relationship.
func validateChildForIntegration(child *feature.Feature) error {
	if len(child.Repos) == 0 {
		return fmt.Errorf("%w: child %s has no repositories", ErrChildIntegrationRefused, child.ID)
	}
	if child.Status != feature.StatusReviewPassed {
		return fmt.Errorf("%w: final review is not durably approved for child %s (status %s)", ErrChildIntegrationRefused, child.ID, child.Status)
	}
	return nil
}

func featureRepoByName(f *feature.Feature, name string) *feature.FeatureRepo {
	if f == nil {
		return nil
	}
	for i := range f.Repos {
		if f.Repos[i].Name == name {
			return &f.Repos[i]
		}
	}
	return nil
}

// runTransactionIntegration drives the transactional integration path:
// prepare candidates, apply them, and close the child. It is idempotent —
// a restart replays only the unfinished steps.
//
// When the journal is in the attention phase, the restart logic checks
// whether the child head has changed since final-review approval. If the
// child code changed, final review is invalidated and the child is routed
// back through review. If only parent tips changed, candidates are rebuilt
// while preserving the still-valid approval.
func (o *Orchestrator) runTransactionIntegration(childID string, child, parent *feature.Feature) error {
	if err := validateTransactionParent(child, parent); err != nil {
		return err
	}

	journal := child.Parent.Transaction

	// If the journal has candidates prepared against an old parent-tip
	// vector, check whether the parent tips have moved. If they moved
	// cleanly and child code did not change, rebuild the candidate vector.
	// If child code changed, invalidate final review.
	if journal != nil && journal.AllCandidatesPrepared() && !journal.AllApplied() {
		currentTips, err := o.transactionParentTipVector(parent, journal)
		if err != nil {
			return err
		}
		if transactionNeedsRebuild(journal, currentTips) {
			changed, err := o.commitAndCompareChildHeads(child, journal)
			if err != nil {
				return err
			}
			if changed {
				return o.invalidateFinalReview(childID, journal)
			}
			journal = nil
		}
	}

	// If the journal is in attention due to a conflict, check whether the
	// child head changed since the reviewed head. If so, invalidate final
	// review.
	if journal != nil && journal.Phase == feature.TransactionPhaseAttention {
		changed, err := o.commitAndCompareChildHeads(child, journal)
		if err != nil {
			return err
		}
		if changed {
			return o.invalidateFinalReview(childID, journal)
		}
	}

	// If the journal is in preparing phase, candidate staging was
	// interrupted by a crash. If all candidates are durable, advance
	// to prepared; otherwise re-prepare from scratch (safe because
	// parent refs are never touched during preparation and child
	// commits are idempotent).
	if journal != nil && journal.Phase == feature.TransactionPhasePreparing {
		if journal.AllCandidatesPrepared() {
			journal.Phase = feature.TransactionPhasePrepared
			if err := o.persistTransaction(childID, journal); err != nil {
				return fmt.Errorf("recording prepared after interrupted staging: %w", err)
			}
		} else {
			journal = nil
		}
	}

	// If the journal was rolled back, all refs are back at their anchors.
	// Clear it and re-prepare from scratch.
	if journal != nil && journal.Phase == feature.TransactionPhaseRolledBack {
		journal = nil
	}

	// If the journal was interrupted during rollback, resume it.
	// rollbackTransaction is idempotent: it skips entries already
	// rolled back (syncing their worktrees) and re-attempts only the
	// remaining applied entries. On completion the journal transitions
	// to rolled_back or attention.
	if journal != nil && journal.Phase == feature.TransactionPhaseRollingBack {
		if err := o.rollbackTransaction(child, parent, journal, -1); err != nil {
			return err
		}
		var err error
		child, err = o.deps.Lifecycle.Get(childID)
		if err != nil {
			return fmt.Errorf("reload child after rollback resume: %w", err)
		}
		journal = child.Parent.Transaction
	}

	// Step: prepare candidates (if not already prepared).
	if journal == nil || journal.Phase == "" || journal.Phase == feature.TransactionPhaseAttention {
		var err error
		journal, err = o.prepareTransactionCandidates(child, parent)
		if err != nil {
			return err
		}
		if journal == nil {
			return nil
		}
	}

	// Step: apply candidates (if prepared or an apply was interrupted
	// by a crash that left the journal in the applying phase).
	// applyTransactionCandidates is idempotent — it skips entries that
	// are already applied and re-attempts pending ones.
	if journal.Phase == feature.TransactionPhasePrepared || journal.Phase == feature.TransactionPhaseApplying {
		if err := o.applyTransactionCandidates(child, parent, journal); err != nil {
			return err
		}
		var err error
		child, err = o.deps.Lifecycle.Get(childID)
		if err != nil {
			return fmt.Errorf("reload child after apply: %w", err)
		}
		journal = child.Parent.Transaction
	}

	// Step: confirm all refs at candidates and close.
	if journal != nil && journal.Phase == feature.TransactionPhaseApplied {
		return o.closeTransactionAfterApply(childID, parent.ID)
	}

	return nil
}

// commitAndCompareChildHeads commits remaining child changes in every
// repository and returns true if any child head differs from the
// corresponding journal entry's recorded ChildHeadSHA. This detects whether
// child code changed since final-review approval, requiring review
// invalidation. An inspection failure is returned as an error so the caller
// can propagate a contextual error instead of silently treating a broken
// worktree as "unchanged."
func (o *Orchestrator) commitAndCompareChildHeads(child *feature.Feature, journal *feature.TransactionJournal) (bool, error) {
	if o.deps.Publisher == nil || journal == nil {
		return false, nil
	}
	for i := range child.Repos {
		childWorktree := child.Repos[i].WorktreePath
		if childWorktree == "" {
			childWorktree = child.Repos[i].Path
		}
		currentHead, err := o.deps.Publisher.CommitAllAndGetHead(childWorktree, fmt.Sprintf("Integration commit for refactor child %s repo %s", child.ID, child.Repos[i].Name))
		if err != nil {
			return false, fmt.Errorf("committing child changes for repo %s: %w", child.Repos[i].Name, err)
		}
		entry := journal.EntryByRepo(child.Repos[i].Name)
		if entry != nil && entry.ChildHeadSHA != "" && currentHead != entry.ChildHeadSHA {
			return true, nil
		}
	}
	return false, nil
}

// invalidateFinalReview clears the transaction journal and resets the child
// to StatusReviewPassed with CurrentPhase=PhaseFinalReview so the pipeline
// routes it back through final review — not Plan or Implement — before a
// fresh transaction can be prepared.
func (o *Orchestrator) invalidateFinalReview(childID string, journal *feature.TransactionJournal) error {
	if err := o.deps.Store.Modify(childID, func(f *feature.Feature) error {
		f.Parent.Transaction = nil
		f.Status = feature.StatusReviewPassed
		f.CurrentPhase = feature.PhaseFinalReview
		f.LastError = ""
		f.FailureType = ""
		return nil
	}); err != nil {
		return fmt.Errorf("invalidating final review: %w", err)
	}
	o.emitEvent(ports.Event{
		Type:      ports.RepoStatusChanged,
		FeatureID: childID,
		Message:   "child integration conflict changed child code; final review invalidated",
	})
	return nil
}

// closeTransactionAfterApply completes the transaction closure only after
// every parent ref is confirmed at its candidate commit. It marks the parent
// CodeReady and the child Completed, then persists the merged phase, and
// finally settles the closure tail (per-repo cleanup and publish handoff).
//
// Crash safety: the merged phase is persisted AFTER both the parent CodeReady
// and child Completed transitions are durable, so a crash before the merged
// write leaves the journal in the applied phase — startup reconciliation
// sees all refs at candidates and finishes closure exactly once.
// Idempotent: repeated entry is a no-op for finished steps.
func (o *Orchestrator) closeTransactionAfterApply(childID, parentID string) error {
	child, err := o.deps.Lifecycle.Get(childID)
	if err != nil {
		return fmt.Errorf("reload child for closure: %w", err)
	}
	if child.IsDiscarding() {
		return fmt.Errorf("%w: refusing to close child %s as completed while discard is in progress", ErrChildDiscardInProgress, childID)
	}
	journal := child.Parent.Transaction
	if journal == nil {
		return fmt.Errorf("transaction journal missing during closure")
	}

	// Confirm every parent ref is at its candidate commit.
	cas, ok := o.deps.Worktrees.(refCASOperator)
	if !ok {
		return fmt.Errorf("transaction: ref CAS operations are not configured")
	}
	parent, err := o.deps.Lifecycle.Get(parentID)
	if err != nil {
		return fmt.Errorf("reload parent for closure: %w", err)
	}
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			return fmt.Errorf("parent no longer has repository %s during closure", entry.Repo)
		}
		ref := "refs/heads/" + entry.ParentBranch
		current, err := cas.RefSHA(parentRepo.Path, ref)
		if err != nil {
			return fmt.Errorf("confirming ref %s during closure: %w", ref, err)
		}
		if current != entry.CandidateSHA {
			return fmt.Errorf("ref %s is at %s, expected candidate %s; closure impossible", ref, current, entry.CandidateSHA)
		}
		entry.MergeHEAD = entry.CandidateSHA
		// Ensure the parent worktree is synced to the candidate. A crash
		// between the apply-progress write and the worktree sync can leave
		// the worktree at the old tree even though the ref is at the
		// candidate. This is idempotent when the worktree is already current.
		parentWorktree := parentRepo.WorktreePath
		if parentWorktree == "" {
			parentWorktree = parentRepo.Path
		}
		if err := o.deps.Worktrees.ResetToCommit(parentWorktree, entry.CandidateSHA); err != nil {
			return fmt.Errorf("syncing parent worktree for repo %s during closure: %w", entry.Repo, err)
		}
	}

	// Parent → CodeReady first (failure leaves child open, retryable).
	if parent.Status != feature.StatusCodeReady {
		if err := o.deps.Lifecycle.MarkCodeReady(parentID); err != nil {
			return fmt.Errorf("mark parent code ready: %w", err)
		}
	}

	// Child → Completed.
	now := time.Now()
	if err := o.closeChildRelationship(childID, feature.ChildCloseOutcomeCompleted, now); err != nil {
		return fmt.Errorf("close child relationship: %w", err)
	}
	if err := o.deps.Store.Modify(childID, func(f *feature.Feature) error {
		f.LastError = ""
		return nil
	}); err != nil {
		return fmt.Errorf("clear child relationship error: %w", err)
	}

	// Persist the merged phase AFTER both transitions are durable.
	journal.Phase = feature.TransactionPhaseMerged
	if err := o.persistTransaction(childID, journal); err != nil {
		return fmt.Errorf("recording merged transaction: %w", err)
	}

	return o.settleChildClosureTail(childID, parentID)
}

// settleChildClosureTail is the impermanent (fully retryable) end of the
// integration boundary: cleanup disposable child resources per-repository,
// durably record the cleanup outcome, and only then hand delivery back to
// the parent's current publish configuration. It is safe to re-enter on a
// closed child — the merge and closure are never touched here. Any storage
// failure is returned so callers never observe a settled closure tail whose
// durable state was not written.
func (o *Orchestrator) settleChildClosureTail(childID, parentID string) error {
	child, err := o.deps.Lifecycle.Get(childID)
	if err != nil {
		return fmt.Errorf("reload child: %w", err)
	}

	// Promote child KB workspaces to parent overlays before cleanup. The
	// promotion is a post-close operation: a completed child never reopens.
	// If promotion fails, the workspace is preserved and promotion remains
	// pending for idempotent recovery. Cleanup and auto-publish are blocked
	// until promotion succeeds — the parent must not publish before its
	// successful child knowledge is available in the overlay.
	if child.EffectivePipeline().HasPhase(feature.PhaseKnowledgeBase) {
		if err := o.PromoteChildKBWorkspaces(childID, parentID); err != nil {
			o.emitEvent(ports.Event{
				Type:      ports.RepoStatusChanged,
				FeatureID: childID,
				ParentID:  parentID,
				ChildID:   childID,
				Message:   "child KB promotion failed: " + err.Error(),
			})
			// Promotion is pending — block cleanup and auto-publish. The
			// child stays Completed with its workspace preserved for
			// idempotent recovery by ReconcilePromotions.
			return fmt.Errorf("child KB promotion pending for %s: %w", childID, err)
		}
	}

	warnings := o.cleanupChildResourcesPerRepo(child)

	// Clean up child KB workspaces after successful promotion. If promotion
	// is still pending, the workspace is preserved for recovery.
	baseDir := o.stateDir()
	if baseDir != "" && child.EffectivePipeline().HasPhase(feature.PhaseKnowledgeBase) {
		if store, ok := o.deps.Store.(promotionStore); ok {
			journal, jerr := store.LoadPromotion(childID)
			if jerr != nil {
				return fmt.Errorf("loading promotion journal for cleanup: %w", jerr)
			}
			if journal != nil && journal.Phase == feature.PromotionPhasePromoted {
				for _, repo := range child.Repos {
					workspaceDir := feature.ChildKBWorkspaceDir(baseDir, childID, repo.Name)
					if err := agent.RemoveWorkspace(workspaceDir); err != nil {
						warnings[repo.Name] = fmt.Sprintf("removing child KB workspace for %s: %v", repo.Name, err)
					}
				}
				_ = store.DeletePromotion(childID)
			}
		}
	}

	for _, repo := range child.Repos {
		warning := warnings[repo.Name]
		if err := o.recordTransactionCleanupWarning(childID, repo.Name, warning); err != nil {
			return fmt.Errorf("record cleanup warning for %s: %w", repo.Name, err)
		}
	}
	o.emitEvent(ports.Event{
		Type:      ports.RelationshipClosed,
		FeatureID: childID,
		ParentID:  parentID,
		ChildID:   childID,
		Message:   "refactor child integrated and closed",
	})

	parent, err := o.deps.Lifecycle.Get(parentID)
	if err != nil {
		return fmt.Errorf("reload parent: %w", err)
	}
	if parent.IsPublishable() && parent.Checkpoints.AutoPublish() {
		if err := o.publishWithOptionsLocked(parentID, PublishOptions{}); err != nil {
			o.emitEvent(ports.Event{
				Type:      ports.RepoStatusChanged,
				FeatureID: parentID,
				Message:   "parent auto-publish after child integration failed: " + err.Error(),
			})
		}
	}
	return nil
}

// recordTransactionCleanupWarning durably records the outcome of a per-repo
// cleanup pass on the transaction journal (empty clears a previous warning
// for that repo).
func (o *Orchestrator) recordTransactionCleanupWarning(childID, repoName, warning string) error {
	return o.deps.Store.Modify(childID, func(f *feature.Feature) error {
		if f.Parent.Transaction != nil {
			for i := range f.Parent.Transaction.Entries {
				if f.Parent.Transaction.Entries[i].Repo == repoName {
					f.Parent.Transaction.Entries[i].CleanupWarning = warning
					return nil
				}
			}
		}
		return nil
	})
}

// worktreeRefRemover carries the recorded main-repository and branch
// identity into cleanup, so a retried removal still reaches the ephemeral
// branch even after an earlier partial removal deregistered the worktree.
type worktreeRefRemover interface {
	RemoveRef(worktreePath, mainRepo, branch string) error
}

// cleanupChildResourcesPerRepo removes the disposable child worktree and
// ephemeral branch for every repository independently. Each repo's cleanup
// is attempted separately; a failure for one repo records a warning without
// blocking cleanup of the others. Returns a map of repo name to warning
// string (empty warnings are omitted).
func (o *Orchestrator) cleanupChildResourcesPerRepo(child *feature.Feature) map[string]string {
	warnings := make(map[string]string)
	if o.deps.Worktrees == nil {
		for _, repo := range child.Repos {
			if repo.WorktreePath != "" {
				warnings[repo.Name] = "worktree cleanup is not configured"
			}
		}
		return warnings
	}
	for _, repo := range child.Repos {
		if repo.WorktreePath == "" {
			continue
		}
		var err error
		if rr, ok := o.deps.Worktrees.(worktreeRefRemover); ok {
			err = rr.RemoveRef(repo.WorktreePath, repo.Path, repo.Branch)
		} else {
			err = o.deps.Worktrees.Remove(repo.WorktreePath, true)
		}
		if err != nil {
			warnings[repo.Name] = fmt.Sprintf("removing child worktree %s: %v", repo.WorktreePath, err)
			continue
		}
		if storeErr := o.deps.Store.Modify(child.ID, func(f *feature.Feature) error {
			for i := range f.Repos {
				if f.Repos[i].Name == repo.Name {
					f.Repos[i].WorktreePath = ""
				}
			}
			return nil
		}); storeErr != nil {
			warnings[repo.Name] = fmt.Sprintf("clearing child worktree path for %s: %v", repo.Name, storeErr)
		}
	}
	return warnings
}

// childHeadSHA reads the full HEAD of a worktree through the structural
// head-reader capability; absent capability fails closed.
func (o *Orchestrator) childHeadSHA(worktreePath string) (string, error) {
	reader, ok := o.deps.Worktrees.(childHeadReader)
	if !ok {
		return "", fmt.Errorf("child integration: exact head capture is not configured")
	}
	return reader.CurrentHeadSHA(worktreePath)
}
