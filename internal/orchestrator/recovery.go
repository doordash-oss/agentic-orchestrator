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
	"context"
	"errors"
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// ScanRecovery scans for orphaned PTY sessions that may need recovery and
// returns the items the caller should prompt on. Before scanning, invokes
// Store.CleanupOrphanRuns per-feature so recovery decisions always observe a
// reconciled run set. Cleanup errors suppress the subsequent scan call.
// Emits ports.RecoveryScanned on success (even when zero items are found)
// and fires the Hooks.OnRecoveryScanned callback with the full item slice
// (downstream observers fan out per-feature from this list).
//
// Re-entrancy: CleanupOrphanRuns is idempotent; a second call after success
// sees no orphans and is a no-op. Crash recovery: if the process crashes
// mid-cleanup, the next startup's cleanup pass reconciles any still-present
// orphans before scan proceeds.
//
// Integration reconciliation runs after discard-intent reconciliation so
// a child with a durable discard intent is rolled back and closed before
// ordinary integration can mark the journal applied or publish the parent.
// It then classifies every transaction journal target ref against its
// journaled old and candidate SHAs, finishes fully applied transactions,
// rolls back provable partials, and preserves unclassifiable externally
// moved state as integration attention. Reconciliation errors follow the
// existing fail-closed startup ordering so session recovery never acts on
// an unreconciled transaction.
func (o *Orchestrator) ScanRecovery(ctx context.Context) ([]ports.RecoveryItem, error) {
	if o.deps.Recovery == nil {
		return nil, errors.New("recovery operator not configured")
	}
	// Reconcile any orphan run directories before scanning for recovery items.
	// Recovery decisions (resume/kill/skip) must observe a consistent run set;
	// otherwise a stale committing:true run or run_number > ActiveRun leftover
	// could steer the TUI toward a run that will be deleted a moment later.
	if o.deps.Store != nil {
		if err := o.cleanupOrphanRuns(); err != nil {
			return nil, fmt.Errorf("cleanup orphan runs: %w", err)
		}
	}
	// Roll interrupted child creations forward before abandoned-setup
	// reconciliation: a rebuilt child is left in SettingUpWorktrees with a
	// running setup intent, which the pass below then marks retryable.
	if reconciler, ok := o.deps.Store.(interface {
		ReconcilePendingChildCreations() ([]string, error)
	}); ok {
		if _, err := reconciler.ReconcilePendingChildCreations(); err != nil {
			return nil, fmt.Errorf("reconcile pending child creations: %w", err)
		}
	}
	if reconciler, ok := o.deps.Lifecycle.(interface {
		ReconcileAbandonedSetups() ([]string, error)
	}); ok {
		ids, err := reconciler.ReconcileAbandonedSetups()
		if err != nil {
			return nil, fmt.Errorf("reconcile abandoned setup: %w", err)
		}
		for _, id := range ids {
			o.emitSetupReconciled(id)
		}
	}
	// Child creation and abandoned setup establish the complete durable
	// relationship first. Cascade then takes exclusive ownership before
	// paired config, discard, integration, closure cleanup, or ordinary
	// session recovery can advance either record.
	if err := o.ReconcileCascadeDeletes(); err != nil {
		return nil, fmt.Errorf("reconcile cascade deletes: %w", err)
	}
	// Reconcile interrupted paired config updates before integration
	// transactions so both records converge before any integration work.
	if reconciler, ok := o.deps.Store.(interface {
		ReconcilePendingConfigUpdates() ([]string, error)
	}); ok {
		if _, err := reconciler.ReconcilePendingConfigUpdates(); err != nil {
			return nil, fmt.Errorf("reconcile pending config updates: %w", err)
		}
	}
	// Reconcile discard intents before integration transactions so a
	// child with a durable discard intent is rolled back, closed, and
	// cleaned up before ordinary integration reconciliation can mark the
	// journal applied and close the child as completed. Discard must
	// converge through rollback/closure/cleanup in that order so
	// integration never observes a child that should already be discarded.
	if err := o.ReconcileDiscardIntents(); err != nil {
		return nil, fmt.Errorf("reconcile discard intents: %w", err)
	}
	// Reconcile interrupted integration transactions before ordinary session
	// recovery observes or relaunches sessions.
	if o.deps.Store != nil {
		if err := o.ReconcileIntegrationTransactions(); err != nil {
			return nil, fmt.Errorf("reconcile integration transactions: %w", err)
		}
	}
	items, err := o.deps.Recovery.ScanForRecovery(ctx)
	if err != nil {
		return nil, fmt.Errorf("scan for recovery: %w", err)
	}
	o.emitEvent(ports.Event{
		Type:    ports.RecoveryScanned,
		Message: fmt.Sprintf("%d items", len(items)),
	})
	if o.hooks.OnRecoveryScanned != nil {
		o.hooks.OnRecoveryScanned(items)
	}
	return items, nil
}

func (o *Orchestrator) emitSetupReconciled(featureID string) {
	ev := feature.SetupEvent{
		Kind:      feature.SetupEventFailed,
		FeatureID: featureID,
		Error:     "setup was interrupted by shutdown or crash; retry setup to continue",
	}
	if o.deps.Store != nil {
		if f, err := o.deps.Store.Load(featureID); err == nil && f != nil {
			ev.RunNumber = f.ActiveRun
			if setup := f.Run().Setup; setup != nil {
				ev.Attempt = setup.Attempt
				ev.LogPath = setup.LatestLogPath
			}
		}
	}
	o.emitSetupEvent(ev)
}

// cleanupOrphanRuns enumerates all features (including those surfaced via
// PartialLoadError, since those are frequently the ones most in need of
// cleanup) and invokes Store.CleanupOrphanRuns per feature. Per-feature
// errors are aggregated via errors.Join and returned. The caller holds the
// nil-Store guard.
func (o *Orchestrator) cleanupOrphanRuns() error {
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
		if _, err := o.deps.Store.CleanupOrphanRuns(f.ID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.ID, err))
		}
	}
	for _, id := range partialIDs {
		if _, err := o.deps.Store.CleanupOrphanRuns(id); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// ExecuteRecovery dispatches a batch of recovery actions. For each item with
// an entry in the actions map, emits ports.RecoveryExecuted and fires
// Hooks.OnRecoveryAction. Items whose action is RecoveryResume trigger a
// re-dispatch of the feature's current phase via startPhase. Phase
// re-dispatch errors are collected and returned via errors.Join but do not
// suppress event emission.
func (o *Orchestrator) ExecuteRecovery(
	ctx context.Context,
	items []ports.RecoveryItem,
	actions map[string]ports.RecoveryAction,
) error {
	if o.deps.Recovery == nil {
		return errors.New("recovery operator not configured")
	}
	if err := o.deps.Recovery.ExecuteRecovery(ctx, items, actions); err != nil {
		return fmt.Errorf("execute recovery: %w", err)
	}

	for _, item := range items {
		featureID := ""
		if item.Feature != nil {
			featureID = item.Feature.ID
		}
		key := ports.RecoveryActionKey(featureID, item.RepoName)
		action, ok := actions[key]
		if !ok {
			continue
		}
		actionStr := recoveryActionString(action)
		o.emitEvent(ports.Event{
			Type:      ports.RecoveryExecuted,
			FeatureID: featureID,
			Message:   fmt.Sprintf("%s:%s", item.RepoName, actionStr),
		})
		if o.hooks.OnRecoveryAction != nil {
			o.hooks.OnRecoveryAction(featureID, item.RepoName, actionStr)
		}
	}

	// Build dedup map for relaunch: a multi-repo feature may have several
	// items, but we want to re-dispatch its current phase at most once.
	resumedFeatures := make(map[string]feature.Phase)
	var resumedOrder []string
	for _, item := range items {
		if item.Feature == nil {
			continue
		}
		key := ports.RecoveryActionKey(item.Feature.ID, item.RepoName)
		action, ok := actions[key]
		if !ok || action != ports.RecoveryResume {
			continue
		}
		if _, exists := resumedFeatures[item.Feature.ID]; !exists {
			resumedFeatures[item.Feature.ID] = item.Feature.CurrentPhase
			resumedOrder = append(resumedOrder, item.Feature.ID)
		}
	}
	var relaunchErrs []error
	for _, fid := range resumedOrder {
		phase := resumedFeatures[fid]
		if _, _, err := o.startPhase(fid, phase); err != nil {
			relaunchErrs = append(relaunchErrs, fmt.Errorf("relaunch %s phase %s: %w", fid, phase, err))
		}
	}
	if len(relaunchErrs) > 0 {
		return errors.Join(relaunchErrs...)
	}
	return nil
}

// recoveryActionString returns the lowercase action label used for events
// and hook payloads.
func recoveryActionString(action ports.RecoveryAction) string {
	switch action {
	case ports.RecoveryResume:
		return "resume"
	case ports.RecoveryKill:
		return "kill"
	case ports.RecoverySkip:
		return "skip"
	default:
		return fmt.Sprintf("unknown(%d)", int(action))
	}
}

// ReconcileIntegrationTransactions is the idempotent startup reconciliation
// pass for integration journals. It runs after existing durable record/setup
// cleanup that materializes valid children and before ordinary session
// recovery observes or relaunches sessions.
//
// For each child feature with a transaction journal, it classifies every
// target ref against its journaled old and candidate SHAs:
//   - Prepared-but-unapplied: leave retryable without moving refs.
//   - All refs already at candidates: finish the durable transition once.
//   - Provable partial apply: conditionally roll back.
//   - Partially completed rollback: resume from durable and observed state.
//   - Any ref matching neither old nor candidate: preserve as attention.
//
// Reconciliation errors follow the existing fail-closed startup ordering so
// session recovery never acts on an unreconciled transaction.
func (o *Orchestrator) ReconcileIntegrationTransactions() error {
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
		if err := o.reconcileOneIntegration(f); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.ID, err))
		}
	}
	for _, id := range partialIDs {
		f, err := o.deps.Store.Load(id)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: load: %w", id, err))
			continue
		}
		if f == nil {
			continue
		}
		if err := o.reconcileOneIntegration(f); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// reconcileOneIntegration reconciles a single feature's integration journal.
func (o *Orchestrator) reconcileOneIntegration(f *feature.Feature) error {
	if f == nil || !f.IsChild() {
		return nil
	}
	if owned, err := o.cascadeOwnsRelationship(f.Parent.ParentID); err != nil {
		return err
	} else if owned {
		return nil
	}
	// A child with a durable discard intent is owned by the discard
	// flow. ReconcileDiscardIntents (which runs before this pass in
	// ScanRecovery) resumes the discard through rollback/closure/cleanup.
	// Integration reconciliation must not close or publish a child that
	// has a discard intent — even if the journal is all-at-candidate —
	// because the discard path converges through a different outcome.
	if f.IsDiscarding() {
		return nil
	}
	journal := f.Parent.Transaction
	if journal == nil || journal.Phase == "" {
		return nil
	}

	// A preparing journal was interrupted during candidate staging.
	// Leave it retryable — runTransactionIntegration resumes preparation
	// on restart by re-preparing from scratch (parent refs are untouched
	// during preparation).
	if journal.Phase == feature.TransactionPhasePreparing {
		return nil
	}

	// A merged journal with an active child means closure was interrupted
	// (e.g. a crash after the merged write in the old code path). Finish
	// the closure so the child is not permanently stranded.
	if journal.Phase == feature.TransactionPhaseMerged {
		if f.IsActiveChild() {
			return o.closeTransactionAfterApply(f.ID, f.Parent.ParentID)
		}
		// Already closed — settle the impermanent closure tail.
		return o.settleChildClosureTail(f.ID, f.Parent.ParentID)
	}

	cas, ok := o.deps.Worktrees.(refCASOperator)
	if !ok {
		return fmt.Errorf("ref CAS operations not configured for reconciliation")
	}

	parent, err := o.deps.Lifecycle.Get(f.Parent.ParentID)
	if err != nil {
		return fmt.Errorf("load parent %s: %w", f.Parent.ParentID, err)
	}
	if parent == nil {
		return fmt.Errorf("parent %s not found", f.Parent.ParentID)
	}

	// Classify each ref against journaled old and candidate SHAs.
	allAtCandidate := true
	anyApplied := false
	anyUnclassifiable := false
	anyRolledBack := false
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			entry.Diagnostics = fmt.Sprintf("parent no longer has repository %s", entry.Repo)
			anyUnclassifiable = true
			continue
		}
		ref := "refs/heads/" + entry.ParentBranch
		current, err := cas.RefSHA(parentRepo.Path, ref)
		if err != nil {
			entry.Diagnostics = fmt.Sprintf("reading ref %s: %v", ref, err)
			anyUnclassifiable = true
			continue
		}
		entry.ObservedSHA = current
		switch {
		case current == entry.CandidateSHA:
			entry.ApplyState = feature.RepoApplyApplied
			anyApplied = true
		case current == entry.ParentAnchorSHA:
			// Ref is at the old SHA.
			switch {
			case entry.ApplyState == feature.RepoApplyApplied &&
				journal.Phase == feature.TransactionPhaseRollingBack:
				// The rollback CAS restored the ref but the state was
				// not persisted before the crash. Mark it rolled back
				// and continue the rollback.
				entry.ApplyState = feature.RepoApplyRolledBack
				anyRolledBack = true
			case entry.ApplyState == feature.RepoApplyRolledBack:
				// The entry was durably marked rolled_back before a
				// crash interrupted the aggregate phase write. The
				// rollback must still be completed (or resumed for
				// remaining applied entries).
				anyRolledBack = true
			case entry.ApplyState == feature.RepoApplyApplied:
				// Was applied but ref moved back — external reset.
				anyUnclassifiable = true
				entry.Diagnostics = fmt.Sprintf("ref %s was applied but regressed to old SHA", ref)
			}
		default:
			anyUnclassifiable = true
			entry.Diagnostics = fmt.Sprintf("ref %s externally moved: old %s candidate %s observed %s",
				ref, entry.ParentAnchorSHA, entry.CandidateSHA, current)
		}
		if entry.ApplyState != feature.RepoApplyApplied {
			allAtCandidate = false
		}
	}

	if anyUnclassifiable {
		journal.Phase = feature.TransactionPhaseAttention
		journal.Attention = transactionAttentionSummary(journal)
		return o.persistTransaction(f.ID, journal)
	}

	if allAtCandidate && anyApplied {
		journal.Phase = feature.TransactionPhaseApplied
		if err := o.persistTransaction(f.ID, journal); err != nil {
			return fmt.Errorf("recording reconciled applied: %w", err)
		}
		return o.closeTransactionAfterApply(f.ID, f.Parent.ParentID)
	}

	if anyApplied && !allAtCandidate {
		return o.rollbackTransaction(f, parent, journal, -1)
	}

	// If some entries were rolled back during a partially completed
	// rollback, continue the rollback for remaining applied entries.
	if anyRolledBack && journal.Phase == feature.TransactionPhaseRollingBack {
		return o.rollbackTransaction(f, parent, journal, -1)
	}

	return nil
}
