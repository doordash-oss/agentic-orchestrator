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
	"syscall"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// ScanForRecovery checks all features for orphaned sessions.
func ScanForRecovery(featuresDir string, fm *feature.Manager) ([]ports.RecoveryItem, error) {
	pidFiles, err := FindPIDFiles(featuresDir)
	if err != nil {
		return nil, err
	}

	var items []ports.RecoveryItem
	for _, pf := range pidFiles {
		alive := isProcessGroupAlive(pf.PID)
		var f *feature.Feature
		if fm != nil {
			f, _ = fm.Get(pf.FeatureID)
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

// terminateProcessGroup kills an orphaned process and its entire process group,
// mirroring Session.Stop() behavior. Sessions are started with Setpgid: true, so
// PGID == PID. Sends SIGTERM to the group, polls for exit (up to 5s), then
// escalates to SIGKILL if needed.
func terminateProcessGroup(pid int) {
	pgid := pid // Setpgid: true → PGID equals PID

	// SIGTERM the entire process group so child processes are also signaled.
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	// Poll for process exit (up to 5s).
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			// Escalate: SIGKILL the entire process group.
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			// Brief wait for SIGKILL to take effect, then try to reap.
			time.Sleep(200 * time.Millisecond)
			// Best-effort reap if we happen to be the parent.
			var ws syscall.WaitStatus
			_, _ = syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
			return
		case <-ticker.C:
			if !isProcessGroupAlive(pid) {
				return
			}
		}
	}
}

// isProcessGroupAlive checks if ANY process in the group is still alive.
// It first attempts to reap the group leader (in case we are the parent and
// it's a zombie), then uses kill(-pgid, 0) to probe for surviving members.
// This ensures that child processes that outlive their leader are not missed.
func isProcessGroupAlive(pgid int) bool {
	// Try non-blocking waitpid to reap the group leader if we are the parent.
	// This removes the leader's zombie entry so the subsequent group-alive
	// check does not get a false positive from a zombie leader while all
	// real children have already exited.
	var ws syscall.WaitStatus
	_, _ = syscall.Wait4(pgid, &ws, syscall.WNOHANG, nil)

	// Check if ANY process in the group is still alive. kill(-pgid, 0) sends
	// a null signal to every member of the process group; it returns nil if at
	// least one member exists, or ESRCH if the group is empty.
	err := syscall.Kill(-pgid, 0)
	return err == nil
}

// ExecuteRecovery applies the user's chosen action for each item.
//
// Re-entrancy / crash recovery:
//
//	(a) Idempotent on retry. Each per-item branch is a no-op when re-applied:
//	    terminateProcessGroup is gated on item.ProcessAlive (rechecked via
//	    isProcessGroupAlive); RemovePIDFile tolerates a missing file;
//	    fm.Transition / fm.FailRepoCycle short-circuit when the target state
//	    is already set; and the resume-metadata Store.Modify writes
//	    overwrite-with-the-same-value (SessionID + RepoName) so a re-run
//	    against the same RecoveryItem set converges.
//	(b) On crash mid-loop: each per-item action commits independently —
//	    terminateProcessGroup is synchronous, RemovePIDFile is filesystem-
//	    atomic, and fm.* mutations route through Store.Modify (atomic
//	    feature.yaml rewrite). A crash leaves the feature directory in a
//	    consistent partial state where the items already processed have
//	    progressed and the remaining items still match their original PID
//	    files; the next ScanRecovery pass picks up where this one stopped.
func ExecuteRecovery(items []ports.RecoveryItem, actions map[string]ports.RecoveryAction, fm *feature.Manager) error {
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
				_ = RemovePIDFile(item.PIDFile.Dir, item.PIDFile.RepoName)
			}
			if item.Feature != nil && fm != nil {
				_ = fm.Transition(item.Feature.ID, feature.StatusInterrupted)
			}

		case ports.RecoveryResume:
			// Kill orphaned process group first if still alive, wait for exit
			if item.ProcessAlive {
				terminateProcessGroup(item.PIDFile.PID)
			}
			// Clean up stale PID file after process is confirmed dead
			if item.PIDFile.Dir != "" {
				_ = RemovePIDFile(item.PIDFile.Dir, item.PIDFile.RepoName)
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
				_ = RemovePIDFile(item.PIDFile.Dir, item.PIDFile.RepoName)
			}
		}
	}

	return nil
}

// cleanupStalePIDFiles removes PID files for processes that are no longer running.
func cleanupStalePIDFiles(featuresDir string) error {
	pidFiles, err := FindPIDFiles(featuresDir)
	if err != nil {
		return err
	}

	for _, pf := range pidFiles {
		if !isProcessGroupAlive(pf.PID) && pf.Dir != "" {
			_ = RemovePIDFile(pf.Dir, pf.RepoName)
		}
	}

	return nil
}
