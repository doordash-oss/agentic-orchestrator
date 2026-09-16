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

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testRecordFor(exec Executable, runtimeDir string) OwnershipRecord {
	return OwnershipRecord{
		RuntimeDir:       runtimeDir,
		StateDir:         filepath.Join(runtimeDir, "features"),
		Config:           filepath.Join(runtimeDir, "config.yaml"),
		PID:              os.Getpid(),
		Version:          "test-version",
		StartedAt:        time.Now(),
		ExecutablePath:   exec.Path,
		ExecutableDigest: exec.Digest,
		ExecutableIno:    exec.ID.Ino,
	}
}

func TestAcquireLeaseContentionAndReacquire(t *testing.T) {
	exec := installExecutable(t, "v1")
	runtimeDir := t.TempDir()
	rec := testRecordFor(exec, runtimeDir)

	first, err := AcquireLease(exec, rec)
	if err != nil {
		t.Fatalf("first AcquireLease: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	_, err = AcquireLease(exec, rec)
	if err == nil {
		t.Fatal("second AcquireLease: expected contention error")
	}
	if !IsLeaseHeld(err) {
		t.Fatalf("second AcquireLease error = %v, want LeaseHeldError", err)
	}
	var held *LeaseHeldError
	if !errors.As(err, &held) {
		t.Fatalf("errors.As(LeaseHeldError) failed for %v", err)
	}
	if held.Record.PID != os.Getpid() {
		t.Fatalf("held record pid = %d, want %d", held.Record.PID, os.Getpid())
	}
	if held.Record.ExecutableDigest != exec.Digest {
		t.Fatalf("held record digest = %s, want %s", held.Record.ExecutableDigest, exec.Digest)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}
	second, err := AcquireLease(exec, rec)
	if err != nil {
		t.Fatalf("re-acquire after Close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close second: %v", err)
	}
}

func TestAcquireLeaseSeparateBinariesAcquireIndependently(t *testing.T) {
	first := installExecutable(t, "copy-one")
	second := installExecutable(t, "copy-two")

	leaseA, err := AcquireLease(first, testRecordFor(first, t.TempDir()))
	if err != nil {
		t.Fatalf("AcquireLease(first): %v", err)
	}
	t.Cleanup(func() { _ = leaseA.Close() })

	leaseB, err := AcquireLease(second, testRecordFor(second, t.TempDir()))
	if err != nil {
		t.Fatalf("AcquireLease(second) should not contend with a separate binary copy: %v", err)
	}
	t.Cleanup(func() { _ = leaseB.Close() })
}

func TestAcquireLeaseOverwritesStaleRecord(t *testing.T) {
	exec := installExecutable(t, "v1")
	runtimeDir := t.TempDir()

	stale := testRecordFor(exec, runtimeDir)
	// Far beyond any host pid range: certainly dead, no spawn needed.
	stale.PID = 1 << 22
	stale.ExecutableDigest = "deadbeef"
	stale.ExecutableIno = 0
	if ProcessAlive(stale.PID) {
		t.Fatalf("fixture pid %d unexpectedly alive", stale.PID)
	}
	if err := writeOwnershipRecord(exec.Path, stale); err != nil {
		t.Fatalf("seed stale record: %v", err)
	}

	rec := testRecordFor(exec, runtimeDir)
	lease, err := AcquireLease(exec, rec)
	if err != nil {
		t.Fatalf("AcquireLease over stale record: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close() })

	got, err := ReadOwnershipRecord(exec.Path)
	if err != nil {
		t.Fatalf("ReadOwnershipRecord: %v", err)
	}
	if got.PID != os.Getpid() || got.ExecutableDigest != exec.Digest || got.ExecutableIno != exec.ID.Ino {
		t.Fatalf("record not overwritten: %+v", got)
	}
	if err := ValidateOwnershipRecord(got, exec, runtimeDir); err != nil {
		t.Fatalf("ValidateOwnershipRecord after overwrite: %v", err)
	}
}

func TestValidateOwnershipRecordMismatch(t *testing.T) {
	exec := installExecutable(t, "v1")
	runtimeDir := t.TempDir()
	otherExec := installExecutable(t, "other")
	otherRuntimeDir := t.TempDir()

	tests := []struct {
		name   string
		mutate func(rec *OwnershipRecord)
	}{
		{"path mismatch", func(rec *OwnershipRecord) { rec.ExecutablePath = otherExec.Path }},
		{"digest mismatch", func(rec *OwnershipRecord) { rec.ExecutableDigest = "deadbeef" }},
		{"runtime mismatch", func(rec *OwnershipRecord) { rec.RuntimeDir = otherRuntimeDir }},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := OwnershipRecord{
				ExecutablePath:   exec.Path,
				ExecutableDigest: exec.Digest,
				RuntimeDir:       runtimeDir,
			}
			tc.mutate(&rec)
			if err := ValidateOwnershipRecord(rec, exec, runtimeDir); err == nil {
				t.Fatalf("ValidateOwnershipRecord accepted %s", tc.name)
			}
		})
	}
}

func TestLeaseFilesNeverUnlinkedAfterClose(t *testing.T) {
	exec := installExecutable(t, "v1")
	lease, err := AcquireLease(exec, testRecordFor(exec, t.TempDir()))
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	leasePath, recordPath := LeasePath(exec.Path), OwnershipRecordPath(exec.Path)
	if err := lease.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, path := range []string{leasePath, recordPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("lease artifact %s must survive Close: %v", path, err)
		}
	}
	info, err := os.Stat(leasePath)
	if err != nil {
		t.Fatalf("stat lease: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lease mode = %o, want 600", info.Mode().Perm())
	}
}

func TestLeaseFDIsCLOEXECByDefault(t *testing.T) {
	exec := installExecutable(t, "v1")
	lease, err := AcquireLease(exec, testRecordFor(exec, t.TempDir()))
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	ok, err := IsCLOEXEC(lease.FD())
	if err != nil {
		t.Fatalf("IsCLOEXEC: %v", err)
	}
	if !ok {
		t.Fatal("lease fd must be close-on-exec by default")
	}
	if lease.Path() != LeasePath(exec.Path) {
		t.Fatalf("lease path = %s, want %s", lease.Path(), LeasePath(exec.Path))
	}
}

func TestLeaseSetTransactionIDKeepsFields(t *testing.T) {
	exec := installExecutable(t, "v1")
	runtimeDir := t.TempDir()
	lease, err := AcquireLease(exec, testRecordFor(exec, runtimeDir))
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close() })

	if err := lease.SetTransactionID("tx-1234"); err != nil {
		t.Fatalf("SetTransactionID: %v", err)
	}
	got, err := ReadOwnershipRecord(exec.Path)
	if err != nil {
		t.Fatalf("ReadOwnershipRecord: %v", err)
	}
	if got.TransactionID != "tx-1234" {
		t.Fatalf("transaction id = %q, want tx-1234", got.TransactionID)
	}
	if got.PID != os.Getpid() || got.ExecutableDigest != exec.Digest || got.RuntimeDir != runtimeDir {
		t.Fatalf("other fields not kept: %+v", got)
	}
}

func TestAdoptLeaseRejectsClosedFD(t *testing.T) {
	exec := installExecutable(t, "v1")
	f, err := os.Create(filepath.Join(t.TempDir(), "not-a-lease"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	fd := int(f.Fd())
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	rec := testRecordFor(exec, t.TempDir())
	if _, err := AdoptLease(fd, rec); err == nil {
		t.Fatal("AdoptLease on closed fd: expected error")
	}
}

// TestChildProcessDoesNotInheritLeaseFD proves the lease descriptor's
// close-on-exec default with a real child process: the helper exits 0 only
// when probing the fd yields EBADF.
func TestChildProcessDoesNotInheritLeaseFD(t *testing.T) {
	bin := installExecutable(t, "v1")
	lease, err := AcquireLease(bin, testRecordFor(bin, t.TempDir()))
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperChildFDProbe$", "--", strconv.Itoa(lease.FD()))
	cmd.Env = append(os.Environ(), "AGENTICO_SELFUPDATE_TEST_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed (fd leaked into child?): %v\noutput: %s", err, out)
	}
}

func TestHelperChildFDProbe(t *testing.T) {
	if os.Getenv("AGENTICO_SELFUPDATE_TEST_HELPER") != "1" {
		t.Skip("helper process only")
	}
	fd, err := strconv.Atoi(os.Args[len(os.Args)-1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad fd argument: %v\n", err)
		os.Exit(2)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		fmt.Fprintf(os.Stderr, "fd %d inherited by child: %v\n", fd, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestRuntimeUpdateLockContentionAndPersistence(t *testing.T) {
	runtimeDir := t.TempDir()

	first, err := AcquireRuntimeUpdateLock(runtimeDir)
	if err != nil {
		t.Fatalf("first AcquireRuntimeUpdateLock: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	_, err = AcquireRuntimeUpdateLock(runtimeDir)
	if err == nil || !IsRuntimeUpdateLockHeld(err) {
		t.Fatalf("second AcquireRuntimeUpdateLock error = %v, want ErrRuntimeUpdateLockHeld", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(RuntimeUpdateLockPath(runtimeDir)); err != nil {
		t.Fatalf("lock file must survive Close: %v", err)
	}
	second, err := AcquireRuntimeUpdateLock(runtimeDir)
	if err != nil {
		t.Fatalf("re-acquire after Close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close second: %v", err)
	}
	info, err := os.Stat(RuntimeUpdateLockPath(runtimeDir))
	if err != nil {
		t.Fatalf("stat lock: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %o, want 600", info.Mode().Perm())
	}
}

func TestLiveHandoffScenarios(t *testing.T) {
	writeReceiptAt := func(t *testing.T, exec Executable, r Receipt) {
		t.Helper()
		if err := writeReceiptAtomic(ReceiptPath(exec.Path), r, time.Now()); err != nil {
			t.Fatalf("write receipt: %v", err)
		}
	}
	pendingReceipt := func(exec Executable) Receipt {
		return Receipt{
			SchemaVersion:  receiptSchemaVersion,
			TransactionID:  "txid",
			ExecutablePath: exec.Path,
			Outcome:        OutcomePending,
			Phase:          PhaseBackupReady,
		}
	}

	t.Run("no lease dir", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		live, _, err := LiveHandoff(exec.Path)
		if err != nil || live {
			t.Fatalf("LiveHandoff = (%v, %v), want (false, nil)", live, err)
		}
	})

	t.Run("free lease", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		lease, err := AcquireLease(exec, testRecordFor(exec, t.TempDir()))
		if err != nil {
			t.Fatalf("AcquireLease: %v", err)
		}
		writeReceiptAt(t, exec, pendingReceipt(exec))
		if err := lease.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		live, receipt, err := LiveHandoff(exec.Path)
		if err != nil || live {
			t.Fatalf("LiveHandoff = (%v, %v), want (false, nil)", live, err)
		}
		if receipt.Outcome != OutcomePending {
			t.Fatalf("receipt outcome = %q, want pending", receipt.Outcome)
		}
	})

	t.Run("held lease with pending receipt", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		lease, err := AcquireLease(exec, testRecordFor(exec, t.TempDir()))
		if err != nil {
			t.Fatalf("AcquireLease: %v", err)
		}
		t.Cleanup(func() { _ = lease.Close() })
		writeReceiptAt(t, exec, pendingReceipt(exec))
		live, receipt, err := LiveHandoff(exec.Path)
		if err != nil || !live {
			t.Fatalf("LiveHandoff = (%v, %v), want (true, nil)", live, err)
		}
		if receipt.Outcome != OutcomePending {
			t.Fatalf("receipt outcome = %q, want pending", receipt.Outcome)
		}
	})

	t.Run("held lease with confirmed receipt", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		lease, err := AcquireLease(exec, testRecordFor(exec, t.TempDir()))
		if err != nil {
			t.Fatalf("AcquireLease: %v", err)
		}
		t.Cleanup(func() { _ = lease.Close() })
		confirmed := pendingReceipt(exec)
		confirmed.Outcome = OutcomeConfirmed
		writeReceiptAt(t, exec, confirmed)
		live, _, err := LiveHandoff(exec.Path)
		if err != nil || live {
			t.Fatalf("LiveHandoff = (%v, %v), want (false, nil) for confirmed receipt", live, err)
		}
	})

	t.Run("held lease with unparseable receipt", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		lease, err := AcquireLease(exec, testRecordFor(exec, t.TempDir()))
		if err != nil {
			t.Fatalf("AcquireLease: %v", err)
		}
		t.Cleanup(func() { _ = lease.Close() })
		if err := os.WriteFile(ReceiptPath(exec.Path), []byte("{not json"), 0o600); err != nil {
			t.Fatalf("write garbage receipt: %v", err)
		}
		live, _, err := LiveHandoff(exec.Path)
		if err != nil || live {
			t.Fatalf("LiveHandoff = (%v, %v), want (false, nil) for unparseable receipt", live, err)
		}
	})
}

func TestProcessAlive(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Fatal("ProcessAlive(current pid) = false, want true")
	}
	if !ProcessAlive(1) {
		t.Fatal("ProcessAlive(1) = false, want true")
	}
	if ProcessAlive(-1) {
		t.Fatal("ProcessAlive(-1) = true, want false")
	}

	// A reaped child is definitively dead.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper run: %v", err)
	}
	if ProcessAlive(cmd.Process.Pid) {
		t.Fatalf("ProcessAlive(reaped pid %d) = true, want false", cmd.Process.Pid)
	}
}
