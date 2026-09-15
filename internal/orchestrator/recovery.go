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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
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
// moved state as integration attention. Restack-journal reconciliation runs
// after integration reconciliation and before promotion reconciliation so
// session recovery never relaunches over an unfinished Final Review fix
// relocation: every feature holding restack journal entries gets each entry
// classified against its journaled old and new SHAs — landed transactions
// are finished, never-landed ones dropped with a relocation warning, and
// externally moved ones preserved with the same warning. Reconciliation
// errors follow the existing fail-closed startup ordering so session
// recovery never acts on an unreconciled transaction.
func (o *Orchestrator) ScanRecovery(ctx context.Context) ([]ports.RecoveryItem, error) {
	if o.deps.Recovery == nil {
		return nil, errors.New("recovery operator not configured")
	}
	// Reconcile any orphan run directories before scanning for recovery items.
	// Recovery decisions (resume/kill/skip) must observe a consistent run set;
	// otherwise a stale committing:true run or run_number > ActiveRun leftover
	// could steer the desktop app toward a run that will be deleted a moment later.
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
	// Reconcile interrupted restack journals before promotion reconciliation
	// and ordinary session recovery: a Final Review fix relocation whose
	// compare-and-swap ref transaction was interrupted mid-landing is
	// finished, dropped, or preserved with a warning here.
	if err := o.ReconcileRestackJournal(ctx); err != nil {
		return nil, fmt.Errorf("reconcile restack journals: %w", err)
	}
	// Reconcile pending promotion journals after integration so a merged
	// child with an unfinished promotion can be recovered before ordinary
	// session recovery observes or relaunches sessions.
	if err := o.ReconcilePromotions(); err != nil {
		return nil, fmt.Errorf("reconcile promotions: %w", err)
	}
	items, err := o.deps.Recovery.ScanForRecovery(ctx)
	if err != nil {
		return nil, fmt.Errorf("scan for recovery: %w", err)
	}
	items = o.dropSupervisedSessions(items)
	o.emitEvent(ports.Event{
		Type:    ports.RecoveryScanned,
		Message: fmt.Sprintf("%d items", len(items)),
	})
	if o.hooks.OnRecoveryScanned != nil {
		o.hooks.OnRecoveryScanned(items)
	}
	return items, nil
}

// dropSupervisedSessions removes scan hits whose PID file belongs to a live
// session this process still supervises. Those are healthy runs, not orphans;
// offering resume/kill for them would terminate the run.
func (o *Orchestrator) dropSupervisedSessions(items []ports.RecoveryItem) []ports.RecoveryItem {
	if o.deps.Sessions == nil || len(items) == 0 {
		return items
	}
	live := make(map[string]bool)
	for _, view := range o.deps.Sessions.ActiveSessions() {
		if view.IsActive() {
			live[view.ID()] = true
		}
	}
	if len(live) == 0 {
		return items
	}
	kept := make([]ports.RecoveryItem, 0, len(items))
	for _, item := range items {
		if item.PIDFile.ManagerID != "" && live[item.PIDFile.ManagerID] {
			continue
		}
		kept = append(kept, item)
	}
	return kept
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
//   - Rebase pass-through candidate: confirm only when the no-op entry was
//     durably applied; otherwise leave retryable.
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

	if o.deps.Worktrees == nil {
		return fmt.Errorf("ref CAS operations not configured for reconciliation")
	}

	parent, err := o.deps.Lifecycle.Get(f.Parent.ParentID)
	if err != nil {
		return fmt.Errorf("load parent %s: %w", f.Parent.ParentID, err)
	}
	if parent == nil {
		return fmt.Errorf("parent %s not found", f.Parent.ParentID)
	}

	// Classify every listed ref of every entry against its journaled anchor
	// and candidate SHAs. Every unclassifiable condition becomes a finding
	// for the stored attention record; a ref race or a missing repository
	// park exactly as the transaction boundary would.
	allAtCandidate := true
	anyApplied := false
	findings := []integrationFinding{}
	anyRolledBack := false
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			findings = append(findings, entryFinding(entry, errcat.IntegrationRepositoryMissing,
				fmt.Sprintf("parent no longer has repository %s", entry.Repo)))
			continue
		}

		// Read and classify every listed ref. An entry is at-candidate only
		// when every ref sits at its candidate, at-anchor only when every
		// ref sits at its anchor; anything mixed is a race.
		entryAllAtCandidate := len(entry.Refs) > 0
		entryAllAtAnchor := len(entry.Refs) > 0
		entryRaced := false
		for j := range entry.Refs {
			ref := &entry.Refs[j]
			refName := "refs/heads/" + ref.Branch
			current, err := o.deps.Worktrees.RefSHA(parentRepo.Path, refName)
			if err != nil {
				findings = append(findings, entryFinding(entry, errcat.IntegrationCandidateFailed,
					fmt.Sprintf("reading ref %s: %v", refName, err)))
				entryRaced = true
				entryAllAtCandidate = false
				entryAllAtAnchor = false
				continue
			}
			ref.ObservedSHA = current
			switch ref.Classify(current) {
			case feature.RefAtCandidate:
				entryAllAtAnchor = false
			case feature.RefAtAnchor:
				entryAllAtCandidate = false
			default:
				findings = append(findings, entryFinding(entry, errcat.IntegrationRefRace,
					fmt.Sprintf("ref %s externally moved: anchor %s candidate %s observed %s",
						refName, ref.AnchorSHA, ref.CandidateSHA, current)))
				entryRaced = true
				entryAllAtCandidate = false
				entryAllAtAnchor = false
			}
		}
		if entryRaced || len(entry.Refs) == 0 {
			allAtCandidate = false
			continue
		}

		// A pass-through entry's candidates equal its anchors; its refs
		// classify as both. Only a durably applied pass-through counts as
		// applied — otherwise the transaction stays retryable.
		if entry.IsPassThrough() {
			if entry.ApplyState != feature.RepoApplyApplied {
				allAtCandidate = false
			} else {
				anyApplied = true
			}
			continue
		}

		switch {
		case entryAllAtCandidate:
			entry.ApplyState = feature.RepoApplyApplied
			anyApplied = true
		case entryAllAtAnchor:
			// Every ref is at its old SHA.
			switch {
			case entry.ApplyState == feature.RepoApplyApplied &&
				journal.Phase == feature.TransactionPhaseRollingBack:
				// The rollback transaction restored the refs but the state
				// was not persisted before the crash. Mark it rolled back
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
				// Was applied but a ref moved back — external reset.
				findings = append(findings, entryFinding(entry, errcat.IntegrationRefRace,
					fmt.Sprintf("refs of %s were applied but regressed to their old SHAs", entry.Repo)))
			}
			allAtCandidate = false
		}
	}

	if len(findings) > 0 {
		journal.Phase = feature.TransactionPhaseAttention
		return o.parkIntegrationAttention(f, journal, findings)
	}

	if allAtCandidate && anyApplied {
		journal.Phase = feature.TransactionPhaseApplied
		if err := o.persistTransaction(f.ID, journal); err != nil {
			return fmt.Errorf("recording reconciled applied: %w", err)
		}
		return o.closeTransactionAfterApply(f.ID, f.Parent.ParentID)
	}

	if anyApplied && !allAtCandidate {
		return o.rollbackTransaction(f, parent, journal, -1, integrationFinding{})
	}

	// If some entries were rolled back during a partially completed
	// rollback, continue the rollback for remaining applied entries.
	if anyRolledBack && journal.Phase == feature.TransactionPhaseRollingBack {
		return o.rollbackTransaction(f, parent, journal, -1, integrationFinding{})
	}

	return nil
}

// restackJournalLifecycle is the restack-journal capability the recovery pass
// needs from the feature lifecycle. Satisfied by *feature.Manager; a
// lifecycle without the capability skips the pass, mirroring the
// optional-capability assertions the other reconciliation passes use.
type restackJournalLifecycle interface {
	SetRestackJournalEntry(featureID, repository string, entry feature.RestackJournalEntry) error
	ApplyRestackJournalEntry(featureID, repository string) error
	SetRestackJournalPendingSync(featureID, repository string, pending bool) error
	DropRestackJournalEntry(featureID, repository string) error
}

// ReconcileRestackJournal is the idempotent startup reconciliation pass for
// restack journals. It runs after integration reconciliation and before
// promotion reconciliation so ordinary session recovery never relaunches a
// session over an unfinished Final Review fix relocation.
//
// For each feature holding restack journal entries, it classifies every
// entry's refs against their journaled old and new SHAs:
//   - All refs at their new SHAs: the transaction landed. The remap is
//     persisted and the entry removed (skipped when the entry is already
//     applied or synced, whose remap a prior scan or the landing flow
//     persisted), the repository's worktree is hard-reset to the rewritten
//     chain's top, and a failed reset re-writes a prepared entry as applied
//     with pending sync so the next scan retries.
//   - All refs at their old SHAs: the transaction never landed. The entry
//     is dropped and the fix-relocated-above-layer warning is emitted.
//   - Anything else, including a ref that cannot be read or a repository
//     the feature no longer records: external movement. Refs and entry are
//     preserved and the same warning is emitted with the observed state as
//     diagnostics.
//
// Reconciliation errors follow the existing fail-closed startup ordering.
func (o *Orchestrator) ReconcileRestackJournal(ctx context.Context) error {
	if o.deps.Store == nil {
		return nil
	}
	lifecycle, ok := o.deps.Lifecycle.(restackJournalLifecycle)
	if !ok {
		return nil
	}
	features, listErr := o.deps.Store.List()
	var partialIDs []string
	if listErr != nil {
		var ple *feature.PartialLoadError
		if !errors.As(listErr, &ple) {
			return fmt.Errorf("list features: %w", listErr)
		}
		for _, w := range ple.Warnings {
			partialIDs = append(partialIDs, w.ID)
		}
	}
	var errs []error
	for _, f := range features {
		if err := o.reconcileOneRestackJournal(f, lifecycle); err != nil {
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
		if err := o.reconcileOneRestackJournal(f, lifecycle); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// reconcileOneRestackJournal reconciles a single feature's restack journal.
// Entries are copied before iteration because the lifecycle operations
// reload and rewrite the run underneath the listed feature.
func (o *Orchestrator) reconcileOneRestackJournal(f *feature.Feature, lifecycle restackJournalLifecycle) error {
	if f == nil || len(f.Run().RestackJournal) == 0 {
		return nil
	}
	if o.deps.Worktrees == nil {
		return fmt.Errorf("ref reads for restack reconciliation are not configured")
	}
	entries := append([]feature.RestackJournalEntry(nil), f.Run().RestackJournal...)
	for _, entry := range entries {
		if err := o.reconcileOneRestackEntry(f, entry, lifecycle); err != nil {
			return err
		}
	}
	return nil
}

// reconcileOneRestackEntry classifies one restack journal entry against the
// repository's refs and finishes, drops, or preserves it as documented on
// ReconcileRestackJournal. refs/heads/* are shared across the worktrees of
// one repository, so the repository's worktree path resolves every listed
// ref.
func (o *Orchestrator) reconcileOneRestackEntry(f *feature.Feature, entry feature.RestackJournalEntry, lifecycle restackJournalLifecycle) error {
	repo := featureRepoByName(f, entry.Repository)
	if repo == nil {
		o.emitRestackRelocationWarning(f, entry, fmt.Sprintf("repository %q is no longer recorded on the feature, so the restack landing was not reconciled", entry.Repository))
		return nil
	}
	worktree := repo.WorktreePath
	if worktree == "" {
		worktree = repo.Path
	}
	allAtNew, allAtOld := true, true
	var observed []string
	for _, update := range entry.Updates {
		current, err := o.deps.Worktrees.RefSHA(worktree, update.Ref)
		if err != nil {
			o.emitRestackRelocationWarning(f, entry, fmt.Sprintf("reading ref %s: %v", update.Ref, err))
			return nil
		}
		observed = append(observed, fmt.Sprintf("%s=%s", update.Ref, current))
		switch current {
		case update.NewSHA:
			// At its new SHA: the update landed. A no-op update whose old
			// and new SHAs match leaves the ref at both, so it still
			// counts as at its old SHA too.
			if update.NewSHA != update.OldSHA {
				allAtOld = false
			}
		case update.OldSHA:
			allAtNew = false
		default:
			allAtNew = false
			allAtOld = false
		}
	}
	switch {
	case allAtNew:
		return o.finishRestackLanding(f, entry, worktree, lifecycle)
	case allAtOld:
		if err := lifecycle.DropRestackJournalEntry(f.ID, entry.Repository); err != nil {
			return fmt.Errorf("dropping never-landed restack journal entry for %s: %w", entry.Repository, err)
		}
		o.emitRestackRelocationWarning(f, entry, "the restack transaction never landed; the final review fixes remain in the top layer")
		return nil
	default:
		o.emitRestackRelocationWarning(f, entry, fmt.Sprintf("refs moved externally while the restack landing was in flight: %s", strings.Join(observed, ", ")))
		return nil
	}
}

// finishRestackLanding finishes one landed restack transaction: persist the
// remap and remove the entry (unless the entry is already applied or
// synced), hard-reset the repository's worktree to the rewritten chain's
// top, and drop the entry. A failed reset re-writes a prepared entry as
// applied with pending sync so the next scan retries; an entry whose remap
// was already persisted is left carrying its pending-sync flag.
func (o *Orchestrator) finishRestackLanding(f *feature.Feature, entry feature.RestackJournalEntry, worktree string, lifecycle restackJournalLifecycle) error {
	remapPersisted := entry.State == feature.RestackJournalApplied || entry.State == feature.RestackJournalSynced
	if !remapPersisted {
		if err := lifecycle.ApplyRestackJournalEntry(f.ID, entry.Repository); err != nil {
			return fmt.Errorf("applying restack journal entry for %s: %w", entry.Repository, err)
		}
	}
	if entry.NewTopSHA != "" {
		if err := o.deps.Worktrees.ResetToCommit(worktree, entry.NewTopSHA); err != nil {
			if remapPersisted {
				// The entry already records its applied state and pending
				// sync flag; leave them set for the next scan to retry.
				return nil
			}
			pending := entry
			pending.State = feature.RestackJournalApplied
			pending.PendingSync = true
			if err := lifecycle.SetRestackJournalEntry(f.ID, entry.Repository, pending); err != nil {
				return fmt.Errorf("marking restack journal entry for %s pending sync: %w", entry.Repository, err)
			}
			return nil
		}
	}
	if err := lifecycle.DropRestackJournalEntry(f.ID, entry.Repository); err != nil {
		return fmt.Errorf("dropping restack journal entry for %s: %w", entry.Repository, err)
	}
	o.emitEvent(ports.Event{
		Type:      ports.RepoStatusChanged,
		FeatureID: f.ID,
		RepoName:  entry.Repository,
		Branch:    topStackLayer(f.Run().Stack).Branch,
		Message:   fmt.Sprintf("recovery finished the restack landing; worktree reset to %s", entry.NewTopSHA),
	})
	return nil
}

// emitRestackRelocationWarning emits the per-repository warning event for a
// restack journal entry whose landing left the final review fixes above the
// layer the fix round asked to relocate into: the repositories block names
// the entry's repository on the top layer's branch, and the params name the
// requested layer and the top layer the fixes actually remain in.
func (o *Orchestrator) emitRestackRelocationWarning(f *feature.Feature, entry feature.RestackJournalEntry, diagnostic string) {
	o.emitFixRelocatedWarning(f, entry.Repository, entry.RequestedLayer, topStackLayer(f.Run().Stack).Position, diagnostic)
}

// topStackLayer returns the stack layer with the highest position — the
// layer final review fixes remain in when no relocation lands — or the zero
// layer when the stack is empty.
func topStackLayer(stack []feature.StackLayer) feature.StackLayer {
	var top feature.StackLayer
	for _, layer := range stack {
		if layer.Position > top.Position {
			top = layer
		}
	}
	return top
}
