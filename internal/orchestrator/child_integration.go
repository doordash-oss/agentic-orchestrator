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
	// vector, check whether the parent tips have moved. If they moved
	// cleanly and child code did not change, rebuild the candidate vector.
	// If child code changed, invalidate final review.
	if journal != nil &&
		(journal.Phase == feature.TransactionPhasePrepared || journal.Phase == feature.TransactionPhaseAttention) &&
		journal.AllCandidatesPrepared() && !journal.AllApplied() {
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
		if err := o.rollbackTransaction(child, parent, journal, -1, nil); err != nil {
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

	// Confirm every parent ref is at its candidate commit.
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
		ref := "refs/heads/" + entry.ParentBranch
		current, err := o.deps.Worktrees.RefSHA(parentRepo.Path, ref)
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
			syncErr := fmt.Errorf("syncing parent worktree for repo %s: %w", entry.Repo, err)
			// The journal's attention record and the relationship event own
			// this failure; the phase stays applied so recovery semantics
			// are unchanged and the pass remains resumable, and the child's
			// run carries no failure record until a later phase classifies
			// it.
			finding := integrationFinding{
				ctx:         repoContextFromEntry(entry),
				code:        errcat.IntegrationWorktreeSyncFailed,
				diagnostics: syncErr.Error(),
			}
			if err := o.parkIntegrationAttention(child, journal, []integrationFinding{finding}); err != nil {
				return fmt.Errorf("recording closure sync attention: %w", err)
			}
			return syncErr
		}
		if entry.PendingSync {
			entry.PendingSync = false
			if err := o.persistTransaction(childID, journal); err != nil {
				return fmt.Errorf("clearing pending worktree sync for repo %s: %w", entry.Repo, err)
			}
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
	if parent.IsPublishable() && parent.Checkpoints.AutoPublish() {
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
// child after the shared transactional merge has closed the child. For each
// repo that had selected comments it pull-rebases and pushes the parent
// branch to the existing PR, replies to every selected comment with
// type-appropriate routing, resolves inline review threads whose reply
// succeeded, and records the addressed comment IDs. Repos without selected
// comments are not pushed. Failures are terminal warnings: the tail
// attempts every step once, records per-repo failures in the entry's stored
// tail warning record, and marks itself settled regardless. The parent ends
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

	for _, repo := range parent.Repos {
		comments := commentsByRepo[repo.Name]
		if len(comments) == 0 {
			continue
		}

		// Get the merge SHA for this repo from the transaction journal.
		mergeSHA := ""
		if child.Parent.Transaction != nil {
			if entry := child.Parent.Transaction.EntryByRepo(repo.Name); entry != nil {
				mergeSHA = entry.MergeHEAD
			}
		}

		// Get the PR URL from the parent's repo state.
		repoState := parent.RepoStates[repo.Name]
		if repoState == nil || repoState.PRURL == "" {
			o.recordTransactionTailWarning(child.ID, repo.Name, "no PR URL for review-feedback tail")
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
		branch := repo.Branch

		// Pull-rebase and push the parent branch to the existing PR remote.
		if o.deps.Remote == nil {
			o.recordTransactionTailWarning(child.ID, repo.Name, "remote operations not configured")
			continue
		}
		if err := o.deps.Remote.PullRebase(worktree, branch); err != nil {
			o.recordTransactionTailWarning(child.ID, repo.Name, fmt.Sprintf("pull-rebase failed: %v", err))
			continue
		}
		if err := o.deps.Remote.Push(worktree, branch); err != nil {
			o.recordTransactionTailWarning(child.ID, repo.Name, fmt.Sprintf("push failed: %v", err))
			continue
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

		// Reply to each selected comment.
		replied := make(map[int]bool)
		for _, comment := range comments {
			if addressed[comment.ID] {
				replied[comment.ID] = true
				continue
			}
			outcome, ok := outcomes[comment.ID]
			body := feature.ReviewFeedbackReplyBody(outcome, ok, mergeSHA)
			var replyErr error
			switch comment.Type {
			case git.CommentTypeReview:
				replyErr = git.ReplyToPRComment(worktree, repoState.PRURL, comment.ID, body)
			case git.CommentTypeIssue, git.CommentTypeReviewBody:
				replyErr = git.ReplyToIssueComment(worktree, repoState.PRURL, body)
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

		// Fetch the unresolved-thread map and resolve inline threads whose
		// replies succeeded.
		threadMap, err := git.FetchReviewThreadMap(worktree, repoState.PRURL)
		if err != nil {
			o.recordTransactionTailWarning(child.ID, repo.Name, fmt.Sprintf("fetch thread map: %v", err))
			continue
		}
		for _, comment := range comments {
			if !replied[comment.ID] || comment.Type != git.CommentTypeReview {
				continue
			}
			threadNodeID, ok := threadMap[comment.ID]
			if !ok {
				continue
			}
			if err := git.ResolveReviewThread(worktree, threadNodeID); err != nil {
				o.recordTransactionTailWarning(child.ID, repo.Name, fmt.Sprintf("resolve thread for comment %d: %v", comment.ID, err))
			}
		}
	}

	// The parent ends Published whether or not any step failed.
	if err := o.deps.Lifecycle.MarkPublished(parent.ID, parent.FirstRepoPRURL()); err != nil {
		return fmt.Errorf("returning review-feedback parent to published: %w", err)
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
					entry.Tail = &errcat.FailureRecord{
						Code: errcat.ReviewFeedbackTailIncomplete,
						Context: &errcat.RecordContext{
							Repositories: []errcat.CodeRepository{{
								Name:   repoName,
								Branch: entry.ParentBranch,
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
