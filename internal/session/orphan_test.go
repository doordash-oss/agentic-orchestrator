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
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// startGroupLeader starts a long sleep in its own process group, the way
// sessions start providers, and reaps it on cleanup.
func startGroupLeader(t *testing.T) (*exec.Cmd, time.Time) {
	t.Helper()
	started := time.Now()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	})
	return cmd, started
}

func TestTerminateOrphanTerminatesMatchingProcessGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd, started := startGroupLeader(t)
	if err := WritePIDFile(dir, PIDFile{PID: cmd.Process.Pid, StartedAt: started, ManagerID: "sup.1"}); err != nil {
		t.Fatal(err)
	}
	if got := TerminateOrphan(dir, "sup.1", 5*time.Second); got != OrphanTerminated {
		t.Fatalf("TerminateOrphan = %q, want %q", got, OrphanTerminated)
	}
}

func TestTerminateOrphanLeavesMismatchedIdentityAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd, _ := startGroupLeader(t)
	// A recorded start an hour before the live process started means the
	// PID was reused by an unrelated process.
	if err := WritePIDFile(dir, PIDFile{PID: cmd.Process.Pid, StartedAt: time.Now().Add(-time.Hour), ManagerID: "sup.1"}); err != nil {
		t.Fatal(err)
	}
	if got := TerminateOrphan(dir, "sup.1", time.Second); got != OrphanMismatch {
		t.Fatalf("TerminateOrphan = %q, want %q", got, OrphanMismatch)
	}
	if err := syscall.Kill(-cmd.Process.Pid, 0); err != nil {
		t.Fatalf("mismatched process was signalled: %v", err)
	}
}

func TestTerminateOrphanIgnoresOtherSessionsAndMissingFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got := TerminateOrphan(dir, "sup.1", time.Second); got != OrphanNone {
		t.Fatalf("empty dir: TerminateOrphan = %q, want none", got)
	}
	cmd, started := startGroupLeader(t)
	if err := WritePIDFile(dir, PIDFile{PID: cmd.Process.Pid, StartedAt: started, ManagerID: "sup.other"}); err != nil {
		t.Fatal(err)
	}
	if got := TerminateOrphan(dir, "sup.1", time.Second); got != OrphanNone {
		t.Fatalf("other session: TerminateOrphan = %q, want none", got)
	}
	if err := syscall.Kill(-cmd.Process.Pid, 0); err != nil {
		t.Fatalf("other session's process was signalled: %v", err)
	}
}
