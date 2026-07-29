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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// DiscardChild implements the durable, idempotent child discard state machine.
// It persists discard intent before requesting any stop, stops and joins
// every child session and phase helper, resolves pending attention, establishes
// integration safety (Task 5), closes the relationship with outcome Discarded,
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
	if child.Parent.CloseOutcome == feature.ChildCloseOutcomeCompleted {
		return fmt.Errorf("child %s already completed; cannot discard", childID)
	}
	if child.Parent.CloseOutcome == feature.ChildCloseOutcomeDiscarded {
		// Already discarded — resume from the durable step.
		return o.resumeDiscard(childID)
	}

	// Step 1: Record durable intent if not already present.
	if child.DiscardIntent == nil {
		if err := o.deps.Store.Modify(childID, func(f *feature.Feature) error {
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

	// Step 4: Resolve pending attention.
	if step == feature.DiscardStepSessionsQuiesced {
		o.resolveChildAttention(childID)
		if err := o.setDiscardStep(childID, feature.DiscardStepAttentionResolved); err != nil {
			return err
		}
		step = feature.DiscardStepAttentionResolved
	}

	// Step 5: Integration safety (Task 5).
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
		if err := o.deps.Store.Modify(childID, func(f *feature.Feature) error {
			if f.Parent.CloseOutcome != "" {
				return nil
			}
			f.Parent.CloseOutcome = feature.ChildCloseOutcomeDiscarded
			f.Parent.ClosedAt = &now
			f.DiscardIntent.ClosedAt = &now
			f.DiscardIntent.Step = feature.DiscardStepClosed
			f.LastError = ""
			f.FailureType = ""
			return nil
		}); err != nil {
			return fmt.Errorf("closing child as discarded: %w", err)
		}
		o.emitEvent(ports.Event{
			Type:             ports.RepoStatusChanged,
			FeatureID:        childID,
			RelatedFeatureID: child.Parent.ParentID,
			Message:          "refactor child discarded",
		})
		step = feature.DiscardStepClosed
	}

	// Step 7: Cleanup tail (disposable worktrees, ephemeral branches).
	if step == feature.DiscardStepClosed {
		child, err = o.deps.Lifecycle.Get(childID)
		if err != nil {
			return fmt.Errorf("reload child for cleanup: %w", err)
		}
		o.cleanupChildResourcesPerRepo(child)
		if err := o.setDiscardStep(childID, feature.DiscardStepCleanupDone); err != nil {
			return err
		}
	}

	return nil
}

// setDiscardStep durably records the discard step.
func (o *Orchestrator) setDiscardStep(childID string, step feature.DiscardStep) error {
	return o.deps.Store.Modify(childID, func(f *feature.Feature) error {
		if f.DiscardIntent == nil {
			return fmt.Errorf("discard intent missing")
		}
		f.DiscardIntent.Step = step
		return nil
	})
}

// resolveChildAttention settles pending questions, permissions, help, gates,
// and input markers so no prompt or completion callback can mutate the child
// after discard.
func (o *Orchestrator) resolveChildAttention(childID string) {
	// Clear permission queue entries for this feature.
	if o.deps.Store != nil {
		_ = o.deps.Store.Modify(childID, func(f *feature.Feature) error {
			f.PermissionsQueue = nil
			f.HelpQueue = nil
			f.PendingNeedUserInputPath = ""
			f.PendingReviewPhase = nil
			f.PendingRewindReviewRoadmapPhase = nil
			return nil
		})
	}
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
	cas, ok := o.deps.Worktrees.(refCASOperator)
	if !ok {
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
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		if entry.ApplyState != feature.RepoApplyApplied {
			continue
		}
		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			entry.Diagnostics = fmt.Sprintf("parent no longer has repository %s", entry.Repo)
			allSafe = false
			continue
		}
		ref := "refs/heads/" + entry.ParentBranch
		current, err := cas.RefSHA(parentRepo.Path, ref)
		if err != nil {
			entry.Diagnostics = fmt.Sprintf("reading ref %s: %v", ref, err)
			allSafe = false
			continue
		}
		entry.ObservedSHA = current

		if current == entry.CandidateSHA {
			// Ref still at candidate — CAS rollback to anchor.
			if err := cas.UpdateRef(parentRepo.Path, ref, entry.CandidateSHA, entry.ParentAnchorSHA); err != nil {
				entry.Diagnostics = fmt.Sprintf("rollback CAS failed for %s: %v", ref, err)
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
				entry.Diagnostics = fmt.Sprintf("syncing parent worktree for %s after rollback: %v", entry.Repo, err)
				allSafe = false
				continue
			}
		} else if current == entry.ParentAnchorSHA {
			// Already rolled back (possibly externally).
			entry.ApplyState = feature.RepoApplyRolledBack
		} else {
			// Externally moved — cannot overwrite.
			entry.Diagnostics = fmt.Sprintf("ref %s externally moved: anchor %s candidate %s observed %s",
				ref, entry.ParentAnchorSHA, entry.CandidateSHA, current)
			allSafe = false
		}
	}

	// Persist the updated journal.
	if journal.Phase == feature.TransactionPhaseAttention || journal.AnyApplied() {
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
