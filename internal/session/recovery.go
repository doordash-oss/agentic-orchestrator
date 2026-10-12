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
)

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
	// a null signal to every member of the process group; it returns nil if
	// at least one member exists, or ESRCH if the group is empty.
	err := syscall.Kill(-pgid, 0)
	return err == nil
}
