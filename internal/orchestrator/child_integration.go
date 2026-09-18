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
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
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
	// vector, check whether the parent refs have moved. If they moved
	// cleanly and child code did not change, rebuild the candidate vector.
	// If child code changed, invalidate final review.
	if journal != nil &&
		(journal.Phase == feature.TransactionPhasePrepared || journal.Phase == feature.TransactionPhaseAttention) &&
		journal.AllCandidatesPrepared() && !journal.AllApplied() {
		currentRefs, err := o.transactionRefVector(parent, journal)
		if err != nil {
			return err
		}
		if transactionNeedsRebuild(journal, currentRefs) {
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
		if err := o.rollbackTransaction(child, parent, journal, -1, integrationFinding{}); err != nil {
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
	if journal == nil {
		return false, nil
	}
	for i := range child.Repos {
		childWorktree := child.Repos[i].WorktreePath
		if childWorktree == "" {
			childWorktree = child.Repos[i].Path
		}
		currentHead, err := git.CommitAllAndGetHead(childWorktree, fmt.Sprintf("Integration commit for refactor child %s repo %s", child.ID, child.Repos[i].Name))
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
		f.Run().Failure = nil
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

	// Confirm every listed parent ref is at its candidate commit — a
	// created ref must exist at its candidate; absence makes closure
	// impossible — a deleted ref must be absent — sync the worktree to the
	// entry's top, and persist the entry's remap, the merged marking of
	// every deleted ref's stack entry, and the appended layers onto the
	// parent run.
	if o.deps.Worktrees == nil {
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
		for j := range entry.Refs {
			ref := &entry.Refs[j]
			refName := "refs/heads/" + ref.Branch
			current, absent, err := o.deps.Worktrees.RefSHAOrAbsent(parentRepo.Path, refName)
			if err != nil {
				return fmt.Errorf("confirming ref %s during closure: %w", refName, err)
			}
			if ref.RefKind() == feature.RepoRefKindDelete {
				if !absent {
					return fmt.Errorf("deleted ref %s is still present at %s; expected absence; closure impossible", refName, current)
				}
				continue
			}
			if ref.RefKind() == feature.RepoRefKindCreate && absent {
				return fmt.Errorf("created ref %s is absent; expected candidate %s; closure impossible", refName, ref.CandidateSHA)
			}
			if current != ref.CandidateSHA {
				return fmt.Errorf("ref %s is at %s, expected candidate %s; closure impossible", refName, current, ref.CandidateSHA)
			}
		}
		// Ensure the parent worktree is synced to the entry's top. An entry
		// that appended layers must sit on the new top layer's branch —
		// the existing layers' refs are never rewritten, so the sync is a
		// branch switch, not a reset; an entry whose previous top was
		// deleted additionally hard-resets to the top candidate, because
		// the transaction moved the new top branch's ref underneath the
		// checkout and a crash before the post-transaction reset leaves the
		// worktree on the right branch at the wrong tree; every other entry
		// resets to the top ref's candidate. A crash between the
		// apply-progress write and the worktree sync can leave the worktree
		// at the old tree even though every ref is at its candidate. All of
		// these are idempotent when the worktree is already current.
		parentWorktree := parentRepo.WorktreePath
		if parentWorktree == "" {
			parentWorktree = parentRepo.Path
		}
		top := entry.TopRef()
		if top == nil {
			return fmt.Errorf("repo %s records no refs during closure", entry.Repo)
		}
		syncErr := error(nil)
		if entry.PreviousTop != nil {
			if current := o.deps.Worktrees.CurrentBranch(parentWorktree); current != top.Branch {
				syncErr = o.deps.Worktrees.SwitchBranch(parentWorktree, top.Branch)
			}
			if syncErr == nil && previousTopDeleted(entry) {
				syncErr = o.deps.Worktrees.ResetToCommit(parentWorktree, top.CandidateSHA)
			}
		} else {
			syncErr = o.deps.Worktrees.ResetToCommit(parentWorktree, top.CandidateSHA)
		}
		if syncErr != nil {
			wrapped := fmt.Errorf("syncing parent worktree for repo %s: %w", entry.Repo, syncErr)
			// The journal's attention record and the relationship event own
			// this failure; the phase stays applied so recovery semantics
			// are unchanged and the pass remains resumable, and the child's
			// run carries no failure record until a later phase classifies
			// it.
			finding := entryFinding(entry, errcat.IntegrationWorktreeSyncFailed, wrapped.Error())
			if err := o.parkIntegrationAttention(child, journal, []integrationFinding{finding}); err != nil {
				return fmt.Errorf("recording closure sync attention: %w", err)
			}
			return wrapped
		}
		if entry.PendingSync {
			entry.PendingSync = false
			if err := o.persistTransaction(childID, journal); err != nil {
				return fmt.Errorf("clearing pending worktree sync for repo %s: %w", entry.Repo, err)
			}
		}
	}

	// Persist every entry's remap, the merged marking of every deleted
	// ref's stack entry — the dropped layer's pull request state becomes
	// merged with its tip and last-pushed SHA cleared, keeping the
	// pull-request URL as the durable record — and, for a transaction that
	// appended layers, the appended layer definitions with per-repository
	// tips equal to the candidates — onto the parent run, all in one write.
	// Everything carries absolute SHAs or idempotent state, so the write is
	// idempotent and a crash before it is repaired by this merged-phase
	// re-entry (and the startup scan through the journal's appended-layer
	// list).
	if err := o.deps.Store.Modify(parentID, func(f *feature.Feature) error {
		for i := range journal.Entries {
			feature.ApplyTransactionRemap(f, journal.Entries[i].Remap, journal.Entries[i].Repo)
			for j := range journal.Entries[i].Refs {
				if ref := &journal.Entries[i].Refs[j]; ref.RefKind() == feature.RepoRefKindDelete {
					feature.MarkStackLayerMergedForRepo(f, journal.Entries[i].Repo, ref.Layer)
				}
			}
		}
		applyAppendedLayers(f, journal)
		return nil
	}); err != nil {
		return fmt.Errorf("applying transaction remap to parent: %w", err)
	}

	// Observability: one repository status event per repository naming the
	// new checked-out branch, in the shape the layer boundary split already
	// uses. Entries without a previous top kept the parent's branch and emit
	// nothing.
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		if entry.PreviousTop == nil {
			continue
		}
		top := entry.TopRef()
		if top == nil {
			continue
		}
		o.emitEvent(ports.Event{
			Type:          ports.RepoStatusChanged,
			FeatureID:     parentID,
			RepoName:      entry.Repo,
			Branch:        top.Branch,
			LayerPosition: top.Layer,
		})
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

	// Persist the merged phase AFTER both transitions are durable. Closure
	// succeeded, so the journal carries no attention record and no pending
	// sync flag.
	journal.Phase = feature.TransactionPhaseMerged
	journal.Attention = nil
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

	// A settled review-feedback tail is fully complete — shared cleanup work
	// and the review-feedback tail both finished. Skip entirely so historical
	// children trigger no pushes, no gh invocations, and no journal churn on
	// later startups.
	if child.Parent.Kind == feature.ChildKindReviewFeedback &&
		child.Parent.Transaction != nil && child.Parent.Transaction.TailSettled {
		return nil
	}

	// Preserve the child's diff before any cleanup removes the disposable
	// worktrees. Best-effort: capture failures never block the closure tail.
	o.preserveChildDiffSummary(childID)

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
			// The journal is the only recovery input for a stranded overlay
			// lock, so it (and the disposable workspaces it references) may
			// only be removed once "promoted and unlocked" is durable. The
			// PromoteChildKBWorkspaces call above establishes LocksReleased
			// before returning success; anything less keeps the journal for
			// ReconcilePromotions.
			if journal != nil && journal.Phase == feature.PromotionPhasePromoted && journal.LocksReleased {
				for _, repo := range child.Repos {
					workspaceDir := feature.ChildKBWorkspaceDir(baseDir, childID, repo.Name)
					if err := agent.RemoveWorkspace(workspaceDir); err != nil {
						warnings[repo.Name] = fmt.Sprintf("removing child KB workspace for %s: %v", repo.Name, err)
					}
				}
				if err := store.DeletePromotion(childID); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("deleting settled promotion journal for %s: %w", childID, err)
				}
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
		Message:   child.Parent.Kind + " child integrated and closed",
	})

	parent, err := o.deps.Lifecycle.Get(parentID)
	if err != nil {
		return fmt.Errorf("reload parent: %w", err)
	}
	if child.Parent.Kind == feature.ChildKindReviewFeedback {
		return o.reviewFeedbackIntegrationTail(child, parent)
	}
	// The rebase child's own closure tail — retarget and republish the
	// repositories whose journal entries list refs and which already carry
	// pull requests — runs once; the settled marker guards its re-entry so
	// historical children trigger no pushes and no retargets on later
	// startups. Repositories without any pull request are left to the
	// user's Publish action or the auto-publish handoff below, exactly as
	// before the rebase pass, and the parent-settled event still ends the
	// closure tail.
	if child.Parent.Kind == feature.ChildKindRebase {
		if child.Parent.Transaction == nil || !child.Parent.Transaction.TailSettled {
			if err := o.rebaseIntegrationTail(child, parent); err != nil {
				return err
			}
		}
	}
	// A parent that already reached Published needs no publish handoff:
	// every touched repository's stack is settled, and re-entering a
	// settled closure tail must not replay the stack walk (and its PR
	// state reads) for historical children on later startups. A partial
	// publish keeps the parent CodeReady, so failed repositories still
	// retry through this tail.
	if parent.IsPublishable() && parent.Checkpoints.AutoPublish() && parent.Status != feature.StatusPublished {
		if err := o.publishWithOptionsLocked(parentID, PublishOptions{}); err != nil {
			event := ports.Event{
				Type:      ports.RepoStatusChanged,
				FeatureID: parentID,
				Message:   "parent auto-publish after child integration failed: " + err.Error(),
				Error:     err,
			}
			// The failing repository's stored record owns the condition; the
			// event carries its rendered canonical error so the SSE
			// projection matches the feature-failure shape.
			if freshParent, getErr := o.deps.Lifecycle.Get(parentID); getErr == nil {
				if rendered, ok := firstFailedRepoError(freshParent); ok {
					event.CanonicalError = &rendered
				}
			}
			o.emitEvent(event)
		}
	}
	// Parent-scoped event after the tail's last mutation: clients that reload
	// the parent on the stream's final event must observe settled state,
	// including read-time gates such as the worktree-cleanliness check.
	o.emitEvent(ports.Event{
		Type:      ports.RepoStatusChanged,
		FeatureID: parentID,
		Message:   "parent settled after child closure",
	})
	return nil
}

// reviewFeedbackIntegrationTail is the real ending for a review-feedback
// child after the shared transactional merge has closed the child. Every
// repository whose journal entry lists refs is republished first, through
// the stack publish walk restricted to those repositories, so each layer
// the transaction rewrote lands on its remote with the lease on its last
// pushed SHA before any reply names a commit. Then, for each repository
// whose republish succeeded, the tail replies to every selected comment on
// the pull request it was left on — citing the relocated SHA of the newest
// child commit whose target layer equals the comment's layer (else the
// newest relocated commit) — resolves inline review threads whose reply
// succeeded, and records the addressed comment IDs. A repository whose
// republish failed records the tail-incomplete warning and posts no
// replies, leaving its comments unaddressed for a later pass; other
// repositories continue. Failures are terminal warnings: the tail attempts
// every step once, records per-repo failures in the entry's stored tail
// warning record, and marks itself settled regardless. The parent ends
// Published whether or not any step failed.
func (o *Orchestrator) reviewFeedbackIntegrationTail(child, parent *feature.Feature) error {
	// Group selected comments by repo, preserving the parent repo order.
	commentsByRepo := make(map[string][]feature.ReviewFeedbackComment)
	for _, c := range child.ReviewFeedback {
		commentsByRepo[c.Repo] = append(commentsByRepo[c.Repo], c)
	}

	// Load the outcomes artifact (tolerant: missing/malformed → empty map).
	outcomes := feature.LoadReviewFeedbackOutcomes(o.stateDir(), child)

	// The ledger store capability (load + append). Production wiring always
	// uses *feature.Store; test doubles may not implement it — in that case
	// the nil-ledger path silently skips both the load and the appends.
	ledger, _ := o.deps.Store.(reviewFeedbackLedger)

	// Republish before replying: only repositories whose journal entry
	// lists refs go through the walk, unchanged layers are no-ops, and
	// rewritten layers push with the lease on their last pushed SHA. The
	// walk runs under the relationship read lock the integration boundary
	// already holds.
	entryByRepo := make(map[string]*feature.RepoTransactionEntry)
	var republishRepos []string
	if child.Parent.Transaction != nil {
		for _, repo := range parent.Repos {
			entry := child.Parent.Transaction.EntryByRepo(repo.Name)
			if entry == nil || len(entry.Refs) == 0 {
				continue
			}
			entryByRepo[repo.Name] = entry
			republishRepos = append(republishRepos, repo.Name)
		}
	}
	republishFailed := o.republishReviewFeedbackStacks(child.ID, parent.ID, republishRepos)

	for _, repo := range parent.Repos {
		comments := commentsByRepo[repo.Name]
		if len(comments) == 0 {
			continue
		}
		// A failed republish already recorded the tail warning; its
		// comments stay unaddressed for a later pass.
		if republishFailed[repo.Name] {
			continue
		}
		entry := entryByRepo[repo.Name]
		if entry == nil {
			o.recordTransactionTailWarning(child.ID, repo.Name, "no transaction refs recorded for repository")
			continue
		}

		worktree := repo.WorktreePath
		if worktree == "" {
			worktree = repo.Path
		}
		if worktree == "" {
			o.recordTransactionTailWarning(child.ID, repo.Name, "parent repo has no worktree path")
			continue
		}

		// Resolve the per-layer reply SHAs from the landed chain in the
		// parent worktree, filtered to the journal's relocated commits.
		replyByLayer, newestRelocated := reviewFeedbackReplySHAs(worktree, entry)
		top := entry.TopRef()
		replySHA := func(comment feature.ReviewFeedbackComment) string {
			if sha := replyByLayer[comment.LayerPosition]; sha != "" {
				return sha
			}
			if newestRelocated != "" {
				return newestRelocated
			}
			// No relocated commit for this repository (a pass-through
			// entry): cite the top ref's candidate, which the republish
			// confirmed exists on the remote.
			if top != nil {
				return top.CandidateSHA
			}
			return ""
		}

		// Load addressed ledger for recovery dedup.
		var addressed map[int]bool
		if ledger != nil {
			addr, err := ledger.LoadAddressedReviewFeedbackIDs(parent.ID, repo.Name)
			if err != nil {
				o.recordTransactionTailWarning(child.ID, repo.Name, fmt.Sprintf("load addressed IDs: %v", err))
				addressed = make(map[int]bool)
			} else {
				addressed = addr
			}
		} else {
			addressed = make(map[int]bool)
		}

		// Reply to each selected comment on the pull request it was left on.
		replied := make(map[int]bool)
		for _, comment := range comments {
			if addressed[comment.ID] {
				replied[comment.ID] = true
				continue
			}
			if comment.PRURL == "" {
				o.recordTransactionTailWarning(child.ID, repo.Name, fmt.Sprintf("comment %d has no pull request", comment.ID))
				continue
			}
			outcome, ok := outcomes[comment.ID]
			body := feature.ReviewFeedbackReplyBody(outcome, ok, replySHA(comment))
			var replyErr error
			switch comment.Type {
			case git.CommentTypeReview:
				replyErr = git.ReplyToPRComment(worktree, comment.PRURL, comment.ID, body)
			case git.CommentTypeIssue, git.CommentTypeReviewBody:
				replyErr = git.ReplyToIssueComment(worktree, comment.PRURL, body)
			default:
				replyErr = fmt.Errorf("unsupported comment type %q", comment.Type)
			}
			if replyErr != nil {
				o.recordTransactionTailWarning(child.ID, repo.Name, fmt.Sprintf("reply to comment %d: %v", comment.ID, replyErr))
				continue
			}
			replied[comment.ID] = true
			// Append to the addressed ledger immediately so a resumed tail
			// skips already-replied comments.
			if ledger != nil {
				if err := ledger.AppendAddressedReviewFeedbackIDs(parent.ID, repo.Name, []int{comment.ID}); err != nil {
					o.recordTransactionTailWarning(child.ID, repo.Name, fmt.Sprintf("record addressed ID %d: %v", comment.ID, err))
				}
			}
		}

		// Fetch the unresolved-thread map once per pull request that
		// received an inline reply, and resolve those threads.
		repliedByPR := make(map[string][]feature.ReviewFeedbackComment)
		for _, comment := range comments {
			if !replied[comment.ID] || comment.Type != git.CommentTypeReview || comment.PRURL == "" {
				continue
			}
			repliedByPR[comment.PRURL] = append(repliedByPR[comment.PRURL], comment)
		}
		for _, prURL := range sortedPRURLs(repliedByPR) {
			threadMap, err := git.FetchReviewThreadMap(worktree, prURL)
			if err != nil {
				o.recordTransactionTailWarning(child.ID, repo.Name, fmt.Sprintf("fetch thread map: %v", err))
				continue
			}
			for _, comment := range repliedByPR[prURL] {
				threadNodeID, ok := threadMap[comment.ID]
				if !ok {
					continue
				}
				if err := git.ResolveReviewThread(worktree, threadNodeID); err != nil {
					o.recordTransactionTailWarning(child.ID, repo.Name, fmt.Sprintf("resolve thread for comment %d: %v", comment.ID, err))
				}
			}
		}
	}

	// The parent ends Published whether or not any step failed. The
	// republish walk may have already completed the transition, so an
	// already-published parent needs no second one.
	if freshParent, getErr := o.deps.Lifecycle.Get(parent.ID); getErr != nil || freshParent == nil || freshParent.Status != feature.StatusPublished {
		if err := o.deps.Lifecycle.MarkPublished(parent.ID); err != nil {
			return fmt.Errorf("returning review-feedback parent to published: %w", err)
		}
	}

	// Persist the durable tail-settled marker regardless of warnings. The
	// warning is terminal, and unrecorded comment IDs resurfacing in the
	// next fetch is the retry path.
	if err := o.deps.Store.Modify(child.ID, func(f *feature.Feature) error {
		if f.Parent.Transaction != nil {
			f.Parent.Transaction.TailSettled = true
		}
		return nil
	}); err != nil {
		return fmt.Errorf("persist tail-settled marker: %w", err)
	}

	return nil
}

// republishReviewFeedbackStacks runs the stack publish walk over exactly
// the named repositories and reports, per repository, whether its
// republish failed. The walk stores a canonical failure record on each
// failing repository, so classification re-reads the parent: a repository
// carrying a stored record failed, every other named repository succeeded.
// A publish error with no stored record is a pass-level failure (guard,
// load, or selection) that touched no repository, so every named
// repository is treated as failed. Each failure records the terminal
// tail-incomplete warning; the caller skips replies for failed
// repositories.
func (o *Orchestrator) republishReviewFeedbackStacks(childID, parentID string, repos []string) map[string]bool {
	failed := make(map[string]bool)
	if len(repos) == 0 {
		return failed
	}
	if o.deps.Remote == nil {
		for _, name := range repos {
			failed[name] = true
			o.recordTransactionTailWarning(childID, name, "remote operations not configured")
		}
		return failed
	}
	publishErr := o.publishWithOptionsLocked(parentID, PublishOptions{Repos: repos})
	if publishErr == nil {
		return failed
	}
	fresh, freshErr := o.deps.Lifecycle.Get(parentID)
	// A pass-level failure (guard, load, or selection error) walked no
	// repository, so no named repository carries a stored record; every
	// other shape of publish error left at least one stored record.
	passLevelFailure := freshErr != nil || fresh == nil
	if !passLevelFailure {
		passLevelFailure = true
		for _, name := range repos {
			if state, ok := fresh.RepoStates[name]; ok && state != nil && state.Error != nil {
				passLevelFailure = false
				break
			}
		}
	}
	for _, name := range repos {
		cause := ""
		if passLevelFailure {
			cause = fmt.Sprintf("republish failed: %v", publishErr)
		} else if fresh != nil && freshErr == nil {
			if state, ok := fresh.RepoStates[name]; ok && state != nil && state.Error != nil {
				cause = "republish failed: " + state.Error.Diagnostics
			}
		}
		if cause == "" {
			continue
		}
		failed[name] = true
		o.recordTransactionTailWarning(childID, name, cause)
	}
	return failed
}

// reviewFeedbackReplySHAs resolves the per-layer reply SHAs for one
// repository's journal entry from the landed chain in the parent worktree:
// the range between the lowest listed ref's anchor and the top ref's
// candidate holds exactly the relocated child commits (and the replayed
// parent commits above them), so filtering to the journal's relocated
// values and reading each commit's Stack-Layer trailer yields, per layer
// position, the newest child commit targeted at that layer, plus the
// newest relocated commit overall for comments whose layer received no
// commit. A pass-through entry (candidate equal to the lowest anchor, an
// empty range) yields no entries and callers fall back to the top
// candidate. Failures degrade to the same fallback: the reply must name a
// SHA that exists on the remote, and the republish already delivered the
// top candidate.
func reviewFeedbackReplySHAs(worktree string, entry *feature.RepoTransactionEntry) (map[int]string, string) {
	byLayer := make(map[int]string)
	top := entry.TopRef()
	if top == nil || worktree == "" || top.CandidateSHA == "" {
		return byLayer, ""
	}
	lowestAnchor := ""
	lowestLayer := 0
	for i := range entry.Refs {
		ref := &entry.Refs[i]
		if lowestAnchor == "" || ref.Layer < lowestLayer {
			lowestAnchor = ref.AnchorSHA
			lowestLayer = ref.Layer
		}
	}
	if lowestAnchor == "" || lowestAnchor == top.CandidateSHA {
		return byLayer, ""
	}
	relocated := make(map[string]bool, len(entry.Relocated))
	for _, sha := range entry.Relocated {
		relocated[sha] = true
	}
	commits, err := git.CommitsBetweenWithStackLayer(worktree, lowestAnchor, top.CandidateSHA)
	if err != nil {
		return byLayer, ""
	}
	newest := ""
	for _, commit := range commits {
		if !relocated[commit.SHA] {
			continue
		}
		if pos, err := strconv.Atoi(commit.Layer); err == nil && pos > 0 {
			byLayer[pos] = commit.SHA
		}
		newest = commit.SHA
	}
	return byLayer, newest
}

// sortedPRURLs returns the keys of a pull-request group map in sorted order
// so per-PR steps (thread maps, resolutions) run and record warnings
// deterministically.
func sortedPRURLs(groups map[string][]feature.ReviewFeedbackComment) []string {
	urls := make([]string, 0, len(groups))
	for url := range groups {
		urls = append(urls, url)
	}
	sort.Strings(urls)
	return urls
}

// recordTransactionCleanupWarning durably records the outcome of a per-repo
// cleanup pass on the transaction journal: an empty cause clears the stored
// record for that repo (cleanup finished cleanly), a non-empty cause stores
// the canonical child_cleanup_incomplete record with the repositories block
// and the raw cause as diagnostics.
func (o *Orchestrator) recordTransactionCleanupWarning(childID, repoName, cause string) error {
	return o.deps.Store.Modify(childID, func(f *feature.Feature) error {
		if f.Parent.Transaction != nil {
			for i := range f.Parent.Transaction.Entries {
				if f.Parent.Transaction.Entries[i].Repo == repoName {
					if cause == "" {
						f.Parent.Transaction.Entries[i].Cleanup = nil
					} else {
						f.Parent.Transaction.Entries[i].Cleanup = &errcat.FailureRecord{
							Code: errcat.ChildCleanupIncomplete,
							Context: &errcat.RecordContext{
								Repositories: []errcat.CodeRepository{{
									Name:   repoName,
									Branch: childRepoBranch(f, repoName),
								}},
							},
							Diagnostics: cause,
						}
					}
					return nil
				}
			}
		}
		return nil
	})
}

// childRepoBranch returns the branch recorded for repoName on the feature's
// repositories, or "" when the repository is not listed.
func childRepoBranch(f *feature.Feature, repoName string) string {
	for i := range f.Repos {
		if f.Repos[i].Name == repoName {
			return f.Repos[i].Branch
		}
	}
	return ""
}

// reviewFeedbackLedger is the store capability for reading and writing the
// durable addressed-ID ledger. Production wiring always uses *feature.Store.
type reviewFeedbackLedger interface {
	LoadAddressedReviewFeedbackIDs(parentID, repoName string) (map[int]bool, error)
	AppendAddressedReviewFeedbackIDs(parentID, repoName string, ids []int) error
}

// recordTransactionTailWarning durably records a review-feedback integration
// tail failure for a repo on the transaction journal entry's stored tail
// record. The first failure for a repository creates the
// review_feedback_tail_incomplete record with the repositories block; every
// further failure appends one raw diagnostics line. The warning is terminal —
// it never blocks the remaining comments or repos and the tail still settles.
func (o *Orchestrator) recordTransactionTailWarning(childID, repoName, cause string) {
	if err := o.deps.Store.Modify(childID, func(f *feature.Feature) error {
		if f.Parent.Transaction != nil {
			for i := range f.Parent.Transaction.Entries {
				entry := &f.Parent.Transaction.Entries[i]
				if entry.Repo != repoName {
					continue
				}
				if entry.Tail == nil {
					branch := ""
					if top := entry.TopRef(); top != nil {
						branch = top.Branch
					}
					entry.Tail = &errcat.FailureRecord{
						Code: errcat.ReviewFeedbackTailIncomplete,
						Context: &errcat.RecordContext{
							Repositories: []errcat.CodeRepository{{
								Name:   repoName,
								Branch: branch,
							}},
						},
						Diagnostics: cause,
					}
				} else if cause != "" {
					if entry.Tail.Diagnostics != "" {
						entry.Tail.Diagnostics += "\n"
					}
					entry.Tail.Diagnostics += cause
				}
				return nil
			}
		}
		return nil
	}); err != nil {
		o.emitEvent(ports.Event{
			Type:      ports.RepoStatusChanged,
			FeatureID: childID,
			Message:   fmt.Sprintf("failed to record tail warning for %s: %v", repoName, err),
		})
	}
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
		err := o.deps.Worktrees.RemoveRef(repo.WorktreePath, repo.Path, repo.Branch)
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

// childHeadSHA reads the full HEAD of a worktree through the worktree seam.
func (o *Orchestrator) childHeadSHA(worktreePath string) (string, error) {
	if o.deps.Worktrees == nil {
		return "", fmt.Errorf("child integration: exact head capture is not configured")
	}
	return o.deps.Worktrees.CurrentHeadSHA(worktreePath)
}

// closedChildDiffSetter is the store capability that persists the preserved
// diff summary on an already-closed child.
type closedChildDiffSetter interface {
	SetClosedChildDiffSummary(childID, summary string) error
}

// preserveChildDiffSummary best-effort captures and records the closed child's
// per-repository diff — stat header plus body bounded at
// feature.DiffSummaryBudget; the merge commit remains the full record —
// before cleanup removes the disposable worktrees. It
// never fails the closure: missing capabilities, empty diffs, or store errors
// simply leave the recorded summary empty.
func (o *Orchestrator) preserveChildDiffSummary(childID string) {
	setter, ok := o.deps.Store.(closedChildDiffSetter)
	if !ok {
		return
	}
	child, err := o.deps.Lifecycle.Get(childID)
	if err != nil || child == nil || child.Parent == nil {
		return
	}
	if child.Parent.DiffSummary != "" {
		return
	}
	summary := o.captureChildDiffSummary(child)
	if summary == "" {
		return
	}
	_ = setter.SetClosedChildDiffSummary(childID, feature.ComposeBoundedDiffSummary(summary))
}

// captureChildDiffSummary computes the child's preserved diff per repository
// using the same diff semantics as the publish preview. The launch-time base
// SHA anchors the diff so a completed merge does not make the child's own
// changes appear empty; multi-repo children concatenate one header-prefixed
// section per repository. Repos whose diff is empty or fails contribute
// nothing.
func (o *Orchestrator) captureChildDiffSummary(child *feature.Feature) string {
	bases := make(map[string]string, len(child.Parent.Bases))
	for _, b := range child.Parent.Bases {
		if b.SHA != "" {
			bases[b.Repo] = b.SHA
		} else if b.ParentBranch != "" {
			bases[b.Repo] = b.ParentBranch
		}
	}
	var sb strings.Builder
	for i := range child.Repos {
		repo := &child.Repos[i]
		worktree := repo.WorktreePath
		if worktree == "" {
			worktree = repo.Path
		}
		if worktree == "" {
			continue
		}
		summary, err := git.DiffSummary(worktree, bases[repo.Name])
		if err != nil || strings.TrimSpace(summary) == "" {
			continue
		}
		fmt.Fprintf(&sb, "Repository: %s\n%s\n", repo.Name, summary)
	}
	return sb.String()
}
