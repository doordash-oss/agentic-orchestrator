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
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// OrphanOutcome reports what TerminateOrphan did with one PID file.
type OrphanOutcome string

const (
	// OrphanNone means no PID file was found or its process is gone.
	OrphanNone OrphanOutcome = "none"
	// OrphanMismatch means a live process holds the PID but its identity
	// does not match the PID file, so it was left alone.
	OrphanMismatch OrphanOutcome = "mismatch"
	// OrphanTerminated means the recorded process group was terminated.
	OrphanTerminated OrphanOutcome = "terminated"
	// OrphanSurvived means the process group outlived the bounded wait.
	OrphanSurvived OrphanOutcome = "survived"
)

// identityStartSlack bounds how far a live process's start time may sit
// from the PID file's recorded start before the PID is treated as reused.
// StartedAt is taken immediately before exec and ps reports whole seconds.
const identityStartSlack = 5 * time.Second

// TerminateOrphan reads the PID files directly in dir and, for each whose
// manager session id is managerID and whose process is alive with a start
// time matching the recorded one, terminates its process group and waits up
// to wait for it to exit. A process is never reattached. A PID file whose
// identity does not match the live process is left alone.
func TerminateOrphan(dir, managerID string, wait time.Duration) OrphanOutcome {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return OrphanNone
	}
	outcome := OrphanNone
	for _, pf := range appendPIDFilesInDir(nil, dir, entries) {
		if pf.ManagerID != managerID || pf.PID <= 0 || !isProcessGroupAlive(pf.PID) {
			continue
		}
		if !processStartMatches(pf.PID, pf.StartedAt) {
			outcome = OrphanMismatch
			continue
		}
		if terminateProcessGroupWithin(pf.PID, wait) {
			outcome = OrphanTerminated
		} else {
			outcome = OrphanSurvived
		}
	}
	return outcome
}

// processStartMatches reports whether pid's start time lies within the slack
// of recorded, guarding against a recycled PID.
func processStartMatches(pid int, recorded time.Time) bool {
	if recorded.IsZero() {
		return false
	}
	started, ok := processStartTime(pid)
	if !ok {
		return false
	}
	delta := started.Sub(recorded)
	if delta < 0 {
		delta = -delta
	}
	// ps truncates to the second, so allow one extra second below.
	return delta <= identityStartSlack+time.Second
}

// processStartTime reads a process's start time through ps, which reports it
// the same way on macOS and Linux.
func processStartTime(pid int) (time.Time, bool) {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}, false
	}
	raw := strings.Join(strings.Fields(string(out)), " ")
	started, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", raw, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return started, true
}

// terminateProcessGroupWithin sends SIGTERM to the group, escalates to
// SIGKILL halfway through the wait, and reports whether the group exited.
func terminateProcessGroupWithin(pgid int, wait time.Duration) bool {
	if wait <= 0 {
		wait = 5 * time.Second
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	deadline := time.Now().Add(wait)
	escalate := time.Now().Add(wait / 2)
	escalated := false
	for time.Now().Before(deadline) {
		if !isProcessGroupAlive(pgid) {
			return true
		}
		if !escalated && time.Now().After(escalate) {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			escalated = true
		}
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(pgid, &ws, syscall.WNOHANG, nil)
		time.Sleep(50 * time.Millisecond)
	}
	return !isProcessGroupAlive(pgid)
}
