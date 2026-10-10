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

package session

import (
	"context"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// RecoveryAdapter owns the state directory and feature manager the recovery
// scan and execute need, and exposes them as the ports.RecoveryOperator
// interface. Context is accepted for future cancellation but is not
// consulted today.
type RecoveryAdapter struct {
	StateDir       string
	FeatureManager *feature.Manager
}

// NewRecoveryAdapter constructs a RecoveryAdapter bound to the given state
// directory and feature manager.
func NewRecoveryAdapter(stateDir string, fm *feature.Manager) *RecoveryAdapter {
	return &RecoveryAdapter{StateDir: stateDir, FeatureManager: fm}
}

// ScanForRecovery checks all features for orphaned sessions.
func (a *RecoveryAdapter) ScanForRecovery(_ context.Context) ([]ports.RecoveryItem, error) {
	pidFiles, err := findPIDFiles(a.StateDir)
	if err != nil {
		return nil, err
	}

	var items []ports.RecoveryItem
	for _, pf := range pidFiles {
		alive := isProcessGroupAlive(pf.PID)
		var f *feature.Feature
		if a.FeatureManager != nil {
			f, _ = a.FeatureManager.Store.Load(pf.FeatureID)
		}
		items = append(items, ports.RecoveryItem{
			PIDFile:      pf,
			ProcessAlive: alive,
			Feature:      f,
			RepoName:     pf.RepoName,
		})
	}

	return items, nil
}

// ExecuteRecovery applies the user's chosen action for each item.
//
// Re-entrancy / crash recovery:
//
//	(a) Idempotent on retry. Each per-item branch is a no-op when re-applied:
//	    terminateProcessGroup is gated on item.ProcessAlive (rechecked via
//	    isProcessGroupAlive); removePIDFile tolerates a missing file;
//	    fm.Transition short-circuits when the target state
//	    is already set; and the resume-metadata Store.Modify writes
//	    overwrite-with-the-same-value (SessionID + RepoName) so a re-run
//	    against the same RecoveryItem set converges.
//	(b) On crash mid-loop: each per-item action commits independently —
//	    terminateProcessGroup is synchronous, removePIDFile is filesystem-
//	    atomic, and fm.* mutations route through Store.Modify (atomic
//	    feature.yaml rewrite). A crash leaves the feature directory in a
//	    consistent partial state where the items already processed have
//	    progressed and the remaining items still match their original PID
//	    files; the next ScanRecovery pass picks up where this one stopped.
func (a *RecoveryAdapter) ExecuteRecovery(_ context.Context, items []ports.RecoveryItem, actions map[string]ports.RecoveryAction) error {
	for _, item := range items {
		action, ok := actions[ports.RecoveryActionKey(item.PIDFile.FeatureID, item.RepoName)]
		if !ok {
			action = ports.RecoverySkip
		}

		switch action {
		case ports.RecoveryKill:
			if item.ProcessAlive {
				terminateProcessGroup(item.PIDFile.PID)
			}
			// Clean up PID file after the process is confirmed dead
			if item.PIDFile.Dir != "" {
				_ = removePIDFile(item.PIDFile.Dir, item.PIDFile.RepoName)
			}
			if item.Feature != nil && a.FeatureManager != nil {
				_ = a.FeatureManager.Transition(item.Feature.ID, feature.StatusInterrupted)
			}

		case ports.RecoveryResume:
			// Kill orphaned process group first if still alive, wait for exit
			if item.ProcessAlive {
				terminateProcessGroup(item.PIDFile.PID)
			}
			// Clean up stale PID file after process is confirmed dead
			if item.PIDFile.Dir != "" {
				_ = removePIDFile(item.PIDFile.Dir, item.PIDFile.RepoName)
			}
			// Store the SessionID on the feature so the phase runner
			// can use --resume to continue the session from where it left off.
			// Skip resume metadata for review sessions — review is a sub-step
			// of the implementation loop and its session ID must not be injected
			// into the implement loop via --resume. A crashed review gate is
			// handled by restarting the full implementation phase.
			// The next start request performs the status transition.
			isReviewPhase := item.PIDFile.Phase == feature.PhaseReview.String()
			// Allow resume metadata for Final Review but not for the
			// per-iteration review gate.
			if isReviewPhase && item.Feature != nil && item.Feature.IsReviewing() {
				isReviewPhase = false // Allow resume
			}

			// The unified phase-implement loop re-runs the interrupted unit
			// (iteration N's implement, or iteration N's review) from scratch
			// with a fresh Claude session. The durable on-disk state
			// (progress.md, plan checkmarks, working tree, prior reviewer
			// feedback) is the resume scaffolding. The recovery flow no
			// longer writes resume hints.
			_ = isReviewPhase

		case ports.RecoverySkip:
			// Clean up PID file if process is dead
			if !item.ProcessAlive && item.PIDFile.Dir != "" {
				_ = removePIDFile(item.PIDFile.Dir, item.PIDFile.RepoName)
			}
		}
	}

	return nil
}
