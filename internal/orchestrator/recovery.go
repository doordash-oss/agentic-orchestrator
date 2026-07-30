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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
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
	// could steer the TUI toward a run that will be deleted a moment later.
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
		claim, dispatch := o.claimRecoveryResume(fid, phase)
		if !dispatch {
			continue
		}
		_, started, err := o.startPhase(fid, phase)
		if err != nil {
			if claim != nil {
				_ = claim.Release(time.Now())
			}
			relaunchErrs = append(relaunchErrs, fmt.Errorf("relaunch %s phase %s: %w", fid, phase, err))
			continue
		}
		if claim != nil {
			if started {
				claim.DispatchStarted()
			} else {
				_ = claim.Release(time.Now())
			}
		}
	}
	if len(relaunchErrs) > 0 {
		return errors.Join(relaunchErrs...)
	}
	return nil
}

// claimRecoveryResume stamps the durable continuation intent for an eligible
// interrupted implementation unit. Any lookup, eligibility, or claim failure
// deliberately degrades to the existing fresh relaunch path.
func (o *Orchestrator) claimRecoveryResume(featureID string, phase feature.Phase) (*agent.ResumeClaim, bool) {
	if o.deps.Lifecycle == nil || o.deps.PhaseRunner == nil {
		return nil, true
	}
	current, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return nil, true
	}
	coordinator := o.resumeCoordinatorForFeature(current)
	if coordinator == nil {
		return nil, true
	}
	if current.CurrentPhase != phase {
		return nil, true
	}
	model := resumeModelForFeature(o.deps.PhaseRunner, current)
	claim, eligibility, err := coordinator.Claim(
		featureID,
		current,
		model,
		o.deps.PhaseRunner.Registry,
		time.Now(),
	)
	if err != nil {
		if errors.Is(err, agent.ErrResumeAlreadyClaimed) {
			return nil, false
		}
		coordinator.MarkFreshFallback("claim_error", time.Now())
		return nil, true
	}
	if !eligibility.Eligible {
		coordinator.MarkFreshFallback(string(eligibility.Reason), time.Now())
		return nil, true
	}
	return claim, true
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
