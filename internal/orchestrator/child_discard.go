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

// DiscardChild implements the durable, idempotent child discard state machine.
// It persists discard intent before requesting any stop, stops and joins
// every child session and phase helper, resolves pending attention, establishes
// integration safety, closes the relationship with outcome Discarded,
// and then enters the retryable cleanup tail. Repeated or concurrent requests
// converge on the same outcome and close timestamp.
func (o *Orchestrator) DiscardChild(childID string) error {
	child, err := o.deps.Lifecycle.Get(childID)
	if err != nil {
		return fmt.Errorf("load child: %w", err)
	}
	if child == nil || !child.IsChild() {
		return fmt.Errorf("feature %s is not a child", childID)
	}
	if !child.IsActiveChild() {
		return fmt.Errorf("%w: discard is not permitted on closed child %s", feature.ErrChildRelationshipClosed, childID)
	}
	if owned, err := o.cascadeOwnsRelationship(child.Parent.ParentID); err != nil {
		return err
	} else if owned {
		return fmt.Errorf("%w: parent %s cascade owns child %s", feature.ErrParentMutationLocked, child.Parent.ParentID, childID)
	}
	// Step 1: Record durable intent if not already present. The write lock
	// serializes with any in-flight integration (RunChildIntegration holds
	// the read lock) so a discard request cannot interleave with an
	// integration that is already in progress.
	if child.DiscardIntent == nil {
		if err := o.WithRelationshipWriteLock(func() error {
			return o.deps.Store.Modify(childID, func(f *feature.Feature) error {
				if f.DiscardIntent != nil {
					return nil
				}
				if f.Parent.CloseOutcome != "" {
					return fmt.Errorf("child already closed: %s", f.Parent.CloseOutcome)
				}
				f.DiscardIntent = &feature.DiscardIntent{
					RequestedAt: time.Now(),
					Step:        feature.DiscardStepIntentRecorded,
				}
				return nil
			})
		}); err != nil {
			return fmt.Errorf("recording discard intent: %w", err)
		}
	}

	return o.resumeDiscard(childID)
}

// resumeDiscard continues the discard from the last durable step.
func (o *Orchestrator) resumeDiscard(childID string) error {
	child, err := o.deps.Lifecycle.Get(childID)
	if err != nil {
		return fmt.Errorf("load child for discard: %w", err)
	}
	if child == nil || child.DiscardIntent == nil {
		return nil
	}

	step := child.DiscardIntent.Step

	// Already fully done.
	if step == feature.DiscardStepCleanupDone {
		return nil
	}

	// Step 2: Stop sessions.
	if step == feature.DiscardStepIntentRecorded {
		o.StopFeatureSessions(childID)
		if err := o.setDiscardStep(childID, feature.DiscardStepSessionsStopping); err != nil {
			return err
		}
		step = feature.DiscardStepSessionsStopping
	}

	// Step 3: Wait for sessions to quiesce.
	if step == feature.DiscardStepSessionsStopping {
		if o.deps.Sessions != nil {
			for _, s := range o.deps.Sessions.FeatureSessions(childID) {
				if s != nil && s.IsActive() {
					// Sessions are still draining; the caller can retry.
					return fmt.Errorf("sessions for %s are still draining", childID)
				}
			}
		}
		if err := o.setDiscardStep(childID, feature.DiscardStepSessionsQuiesced); err != nil {
			return err
		}
		step = feature.DiscardStepSessionsQuiesced
	}

	// Step 4: Resolve pending attention. Only advance the durable
	// step once the attention-clearing save has succeeded, otherwise
	// the durable step name would overstate reality and leave prompts
	// uncleared while the child continues toward Discarded.
	if step == feature.DiscardStepSessionsQuiesced {
		if err := o.resolveChildAttention(childID); err != nil {
			return fmt.Errorf("resolving child attention during discard: %w", err)
		}
		if err := o.setDiscardStep(childID, feature.DiscardStepAttentionResolved); err != nil {
			return err
		}
		step = feature.DiscardStepAttentionResolved
	}

	// Step 5: Integration safety — classify parent refs and roll back
	// provably child-applied candidates.
	if step == feature.DiscardStepAttentionResolved {
		safe, err := o.ensureDiscardRefSafety(childID)
		if err != nil {
			return err
		}
		if !safe {
			// Ref safety cannot be proven — keep the child active with
			// discard intent and precise attention diagnostics.
			return fmt.Errorf("discard ref safety not established for child %s; child remains active", childID)
		}
		if err := o.setDiscardStep(childID, feature.DiscardStepRefsSafe); err != nil {
			return err
		}
		step = feature.DiscardStepRefsSafe
	}

	// Step 6: Close the relationship with outcome Discarded.
	if step == feature.DiscardStepRefsSafe {
		now := time.Now()
		if err := o.closeChildRelationship(childID, feature.ChildCloseOutcomeDiscarded, now); err != nil {
			return fmt.Errorf("closing child as discarded: %w", err)
		}
		if err := o.deps.Store.Modify(childID, func(f *feature.Feature) error {
			f.DiscardIntent.ClosedAt = &now
			f.DiscardIntent.Step = feature.DiscardStepClosed
			f.Run().Failure = nil
			return nil
		}); err != nil {
			return fmt.Errorf("closing child as discarded: %w", err)
		}
		o.emitEvent(ports.Event{
			Type:      ports.RelationshipClosed,
			FeatureID: childID,
			ParentID:  child.Parent.ParentID,
			ChildID:   childID,
			Message:   "refactor child discarded",
		})
		step = feature.DiscardStepClosed
	}

	// Step 7: Cleanup tail (disposable worktrees, ephemeral branches).
	if step == feature.DiscardStepClosed {
		child, err = o.deps.Lifecycle.Get(childID)
		if err != nil {
			return fmt.Errorf("reload child for cleanup: %w", err)
		}
		// Preserve the child's diff before cleanup removes the disposable
		// worktrees. Best-effort: capture failures never block the discard.
		o.preserveChildDiffSummary(childID)
		warnings := o.cleanupChildResourcesPerRepo(child)

		// Remove disposable KB workspaces for discarded children. Discarded
		// children never modify a parent overlay; their workspaces, markers,
		// and locks are removed idempotently after session quiescence.
		baseDir := o.stateDir()
		if baseDir != "" && child.EffectivePipeline().HasPhase(feature.PhaseKnowledgeBase) {
			for _, repo := range child.Repos {
				workspaceDir := feature.ChildKBWorkspaceDir(baseDir, childID, repo.Name)
				if err := agent.RemoveWorkspace(workspaceDir); err != nil {
					warnings[repo.Name] = fmt.Sprintf("removing discarded child KB workspace for %s: %v", repo.Name, err)
				}
			}
		}
		// Delete any pending promotion journal (discarded children never
		// promote). Release the journal's recorded overlay locks first so a
		// discarded child with an interrupted promotion cannot strand a
		// stable lock for every later child of this parent.
		if store, ok := o.deps.Store.(promotionStore); ok {
			if journal, jerr := store.LoadPromotion(childID); jerr == nil && journal != nil {
				_ = releaseJournalOverlayLocks(journal)
			}
			_ = store.DeletePromotion(childID)
		}
		// Durably record per-repo cleanup warnings on the transaction
		// journal so they remain visible and retryable after restart.
		if child.Parent.Transaction != nil {
			for _, repo := range child.Repos {
				warning := warnings[repo.Name]
				if err := o.recordTransactionCleanupWarning(childID, repo.Name, warning); err != nil {
					return fmt.Errorf("record discard cleanup warning for %s: %w", repo.Name, err)
				}
			}
		}
		// Only advance to cleanup_done when every repo's disposable
		// worktree has been removed. A failed cleanup must remain
		// durably visible (worktree path still set, warning recorded)
		// and retryable through ReconcileDiscardIntents so the parent
		// can launch a new child while the older cleanup tail retries.
		reloaded, reloadErr := o.deps.Lifecycle.Get(childID)
		if reloadErr != nil {
			return fmt.Errorf("reload child after cleanup: %w", reloadErr)
		}
		if reloaded.AnyChildWorktreePending() {
			// Cleanup incomplete — leave the step at DiscardStepClosed
			// so startup reconciliation resumes the cleanup tail.
			return fmt.Errorf("discard cleanup for child %s incomplete; %d worktree(s) still pending", childID, countPendingWorktrees(reloaded))
		}
		if err := o.setDiscardStep(childID, feature.DiscardStepCleanupDone); err != nil {
			return err
		}
	}

	return nil
}

// setDiscardStep durably records the discard step.
func (o *Orchestrator) setDiscardStep(childID string, step feature.DiscardStep) error {
	var parentID string
	var changed bool
	err := o.deps.Store.Modify(childID, func(f *feature.Feature) error {
		if f.DiscardIntent == nil {
			return fmt.Errorf("discard intent missing")
		}
		parentID = f.Parent.ParentID
		changed = f.DiscardIntent.Step != step
		f.DiscardIntent.Step = step
		return nil
	})
	if err != nil || !changed {
		return err
	}
	o.emitEvent(ports.Event{
		Type:      ports.RelationshipDiscardProgress,
		FeatureID: childID,
		ParentID:  parentID,
		ChildID:   childID,
		Message:   "discard advanced to " + string(step),
	})
	return nil
}

// resolveChildAttention settles pending questions, permissions, help, gates,
// and input markers so no prompt or completion callback can mutate the child
// after discard. It returns an error if the durable attention-clearing save
// fails so the caller does not advance the discard state machine past a
// failed update.
func (o *Orchestrator) resolveChildAttention(childID string) error {
	// Clear permission queue entries for this feature.
	if o.deps.Store == nil {
		return nil
	}
	if err := o.deps.Store.Modify(childID, func(f *feature.Feature) error {
		f.PermissionsQueue = nil
		f.HelpQueue = nil
		f.PendingNeedUserInputPath = ""
		f.PendingReviewPhase = nil
		f.PendingRewindReviewRoadmapPhase = nil
		return nil
	}); err != nil {
		return fmt.Errorf("clearing pending attention for discard: %w", err)
	}
	return nil
}

// ensureDiscardRefSafety classifies parent refs against recorded anchors and
// child candidates. For preparing/prepared integration states, no parent
// refs have been moved — safe to proceed. For applying/applied/rolling-back
// states, conditionally roll back only refs proven to contain a child
// candidate. Externally moved refs are never overwritten. Returns true when
// all parent refs are safe (no child candidate remains), false when safety
// cannot be proven.
func (o *Orchestrator) ensureDiscardRefSafety(childID string) (bool, error) {
	child, err := o.deps.Lifecycle.Get(childID)
	if err != nil {
		return false, fmt.Errorf("load child for ref safety: %w", err)
	}

	journal := child.Parent.Transaction
	if journal == nil || journal.Phase == "" {
		// No integration started — no parent refs to worry about.
		return true, nil
	}

	// Preparing or prepared: candidates staged but no parent ref advanced.
	if journal.Phase == feature.TransactionPhasePreparing ||
		journal.Phase == feature.TransactionPhasePrepared {
		return true, nil
	}

	// Attention with no applied refs: safe to proceed.
	if journal.Phase == feature.TransactionPhaseAttention && !journal.AnyApplied() {
		return true, nil
	}

	// Rolled back: all provable refs restored.
	if journal.Phase == feature.TransactionPhaseRolledBack {
		return true, nil
	}

	// Applying, applied, rolling_back, or attention with some applied refs:
	// need to roll back provably child-applied refs.
	if o.deps.Worktrees == nil {
		return false, fmt.Errorf("ref CAS operations not configured for discard safety")
	}

	parent, err := o.deps.Lifecycle.Get(child.Parent.ParentID)
	if err != nil {
		return false, fmt.Errorf("load parent for discard safety: %w", err)
	}
	if parent == nil {
		return false, fmt.Errorf("parent not found for discard safety")
	}

	allSafe := true
	inspectedRefs := false
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		// Already rolled back — nothing to do.
		if entry.ApplyState == feature.RepoApplyRolledBack {
			continue
		}
		// Entries that are not yet durably marked applied may still have
		// their ref at the candidate: the apply CAS succeeds before the
		// entry is persisted as RepoApplyApplied, so a crash between the
		// CAS and the persist leaves the ref at the candidate while the
		// entry's ApplyState is still empty or "applying". We must inspect
		// the actual ref for every entry that is not already rolled back.
		inspectedRefs = true
		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			entry.Diagnostics = fmt.Sprintf("parent no longer has repository %s", entry.Repo)
			allSafe = false
			continue
		}
		ref := "refs/heads/" + entry.ParentBranch
		current, err := o.deps.Worktrees.RefSHA(parentRepo.Path, ref)
		if err != nil {
			entry.Diagnostics = fmt.Sprintf("reading ref %s: %v", ref, err)
			allSafe = false
			continue
		}
		entry.ObservedSHA = current

		if current == entry.CandidateSHA {
			// Ref still at candidate — CAS rollback to anchor.
			if err := o.deps.Worktrees.UpdateRef(parentRepo.Path, ref, entry.CandidateSHA, entry.ParentAnchorSHA); err != nil {
				entry.Diagnostics = fmt.Sprintf("repo %s rollback CAS failed for %s: %v", entry.Repo, ref, err)
				allSafe = false
				continue
			}
			entry.ApplyState = feature.RepoApplyRolledBack
			// Sync parent worktree back to anchor.
			parentWorktree := parentRepo.WorktreePath
			if parentWorktree == "" {
				parentWorktree = parentRepo.Path
			}
			if err := o.deps.Worktrees.ResetToCommit(parentWorktree, entry.ParentAnchorSHA); err != nil {
				entry.Diagnostics = fmt.Sprintf("repo %s syncing parent worktree for %s after rollback: %v", entry.Repo, entry.Repo, err)
				allSafe = false
				continue
			}
		} else if current == entry.ParentAnchorSHA {
			// Already rolled back (possibly externally).
			entry.ApplyState = feature.RepoApplyRolledBack
		} else {
			// Externally moved — cannot overwrite.
			entry.Diagnostics = fmt.Sprintf("repo %s ref %s externally moved: anchor %s candidate %s observed %s",
				entry.Repo, ref, entry.ParentAnchorSHA, entry.CandidateSHA, current)
			allSafe = false
		}
	}

	// Persist the updated journal.
	if inspectedRefs || journal.Phase == feature.TransactionPhaseAttention {
		if allSafe {
			journal.Phase = feature.TransactionPhaseRolledBack
		} else {
			journal.Phase = feature.TransactionPhaseAttention
			journal.Attention = transactionAttentionSummary(journal)
		}
	}
	if err := o.persistTransaction(childID, journal); err != nil {
		return false, fmt.Errorf("persisting discard ref safety: %w", err)
	}

	return allSafe, nil
}

// countPendingWorktrees returns the number of child repos whose disposable
// worktree path is still recorded, meaning per-repo cleanup has not durably
// completed.
func countPendingWorktrees(f *feature.Feature) int {
	if f == nil {
		return 0
	}
	n := 0
	for i := range f.Repos {
		if f.Repos[i].WorktreePath != "" {
			n++
		}
	}
	return n
}

// ReconcileDiscardIntents processes discard intents at startup before
// ordinary session recovery. It resumes interrupted discards from the durable
// step.
func (o *Orchestrator) ReconcileDiscardIntents() error {
	if o.deps.Store == nil {
		return nil
	}
	features, listErr := o.deps.Store.List()
	var partialIDs []string
	if listErr != nil {
		var ple *feature.PartialLoadError
		if errors.As(listErr, &ple) {
			for _, w := range ple.Warnings {
				partialIDs = append(partialIDs, w.ID)
			}
		} else {
			return fmt.Errorf("list features: %w", listErr)
		}
	}
	var errs []error
	for _, f := range features {
		if f != nil && f.IsChild() && f.DiscardIntent != nil && f.DiscardIntent.Step != feature.DiscardStepCleanupDone {
			if owned, err := o.cascadeOwnsRelationship(f.Parent.ParentID); err != nil {
				errs = append(errs, fmt.Errorf("discard reconcile %s ownership: %w", f.ID, err))
				continue
			} else if owned {
				continue
			}
			if err := o.resumeDiscard(f.ID); err != nil {
				errs = append(errs, fmt.Errorf("discard reconcile %s: %w", f.ID, err))
			}
		}
	}
	for _, id := range partialIDs {
		f, err := o.deps.Store.Load(id)
		if err != nil {
			errs = append(errs, fmt.Errorf("discard reconcile %s: load: %w", id, err))
			continue
		}
		if f != nil && f.IsChild() && f.DiscardIntent != nil && f.DiscardIntent.Step != feature.DiscardStepCleanupDone {
			if owned, ownershipErr := o.cascadeOwnsRelationship(f.Parent.ParentID); ownershipErr != nil {
				errs = append(errs, fmt.Errorf("discard reconcile %s ownership: %w", id, ownershipErr))
				continue
			} else if owned {
				continue
			}
			if err := o.resumeDiscard(id); err != nil {
				errs = append(errs, fmt.Errorf("discard reconcile %s: %w", id, err))
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
