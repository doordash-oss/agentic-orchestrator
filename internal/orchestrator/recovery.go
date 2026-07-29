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
	// For items with a RepoName whose feature has an interrupted
	// RepoCycleState, route through the cycle restart instead of a generic
	// feature-phase restart so a repository-scoped recovery never falls
	// through to startPhase.
	cycleRepos := make(map[string][]string) // featureID → repos with interrupted cycles
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
		// Check whether this item targets an interrupted cycle. A
		// RepoCycleNeedUserInput item is deliberately NOT restarted here:
		// its gate has its own shared-gate-clearing, answer-validating,
		// single-dispatch machinery in
		// resumeRepoCycleNeedUserInput, and a per-repo
		// restartPausedRepoCycle would bypass that gate transition
		// (clearing the paused state without answers and dispatching once
		// per repo instead of once per gate). Recovery Resume for a
		// need-user-input cycle is routed through the existing
		// questionnaire resume contract, not this relaunch path. Only
		// RepoCycleInterrupted items — cycles stopped mid-flight with no
		// gate to clear — restart here.
		if item.RepoName != "" && hasInterruptedCycle(item.Feature, item.RepoName) {
			cycleRepos[item.Feature.ID] = append(cycleRepos[item.Feature.ID], item.RepoName)
		}
		// A need-user-input cycle must not fall through to a generic
		// feature-phase restart either: the feature stays Published while
		// the cycle is paused, and startPhase on a Published feature would
		// be wrong. The process-level recovery action above already ran;
		// the user answers the gate separately to resume the cycle.
		if item.RepoName != "" && hasNeedUserInputCycle(item.Feature, item.RepoName) {
			continue
		}
		if _, exists := resumedFeatures[item.Feature.ID]; !exists {
			resumedFeatures[item.Feature.ID] = item.Feature.CurrentPhase
			resumedOrder = append(resumedOrder, item.Feature.ID)
		}
	}
	var relaunchErrs []error
	for _, fid := range resumedOrder {
		if repos, ok := cycleRepos[fid]; ok && len(repos) > 0 {
			// Restart each paused cycle through its cycle-type-specific
			// seam. restartPausedRepoCycle reads the persisted
			// RepoCycleState for type/count/plan and relaunches the
			// correct loop.
			for _, repoName := range repos {
				if err := o.restartPausedRepoCycle(fid, repoName); err != nil {
					relaunchErrs = append(relaunchErrs, fmt.Errorf("restart cycle %s repo %s: %w", fid, repoName, err))
				}
			}
			continue
		}
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

// hasInterruptedCycle returns true when the feature has a RepoCycleState for
// repoName that is interrupted — a cycle stopped mid-flight with no
// need-user-input gate to clear. Recovery Resume restarts these through the
// cycle-type-specific seam. A RepoCycleNeedUserInput state is intentionally
// excluded: those gates have their own shared-gate-clearing, answer-validating
// single-dispatch path (resumeRepoCycleNeedUserInput), and a per-repo
// restart here would bypass that transition. Both the gate-resume path and
// this recovery path now encode "repos sharing a gate are one dispatch"
// identically — the gate path via reposSharingGate, this path by admitting only
// gate-less interrupted cycles.
func hasInterruptedCycle(f *feature.Feature, repoName string) bool {
	if f == nil || repoName == "" || f.RepoCycles == nil {
		return false
	}
	rc, ok := f.RepoCycles[repoName]
	if !ok || rc == nil {
		return false
	}
	return rc.Status == feature.RepoCycleInterrupted
}

// hasNeedUserInputCycle returns true when the feature has a RepoCycleState for
// repoName that is paused on a need-user-input gate. Recovery does not
// relaunch these — the gate resume contract resumes them after answers.
func hasNeedUserInputCycle(f *feature.Feature, repoName string) bool {
	if f == nil || repoName == "" || f.RepoCycles == nil {
		return false
	}
	rc, ok := f.RepoCycles[repoName]
	if !ok || rc == nil {
		return false
	}
	return rc.Status == feature.RepoCycleNeedUserInput
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
