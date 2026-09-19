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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// beginRecoveryTx runs a real Begin (and optionally Commit) so the durable
// on-disk state is exactly what production recovery consumes.
func beginRecoveryTx(t *testing.T, commit bool) (*txFixture, *Transaction) {
	t.Helper()
	f := newTxFixture(t)
	tx, err := Begin(f.exec, f.opts, FileOps{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if commit {
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}
	return f, tx
}

// crashInsideCommit rewinds the durable receipt to backup-ready after a real
// Commit, reproducing a crash between the replacement rename and the receipt
// phase advance: candidate bytes installed, receipt still backup-ready.
func crashInsideCommit(t *testing.T, f *txFixture) Receipt {
	t.Helper()
	r, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	r.Phase = PhaseBackupReady
	if err := WriteReceiptDurable(ReceiptPath(f.exec.Path), r); err != nil {
		t.Fatalf("rewrite receipt: %v", err)
	}
	return r
}

func holdLease(t *testing.T, f *txFixture) *Lease {
	t.Helper()
	lease, err := AcquireLease(f.exec, testRecordFor(f.exec, f.runtimeDir))
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	return lease
}

func requireRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected unsafe-record refusal, got nil error")
	}
	if !IsUnsafeRecovery(err) {
		t.Fatalf("error = %v, want *UnsafeRecoveryError", err)
	}
}

func TestInspectRecoveryNoReceipt(t *testing.T) {
	exec := installExecutable(t, "v1")
	plan, err := InspectRecovery(exec, t.TempDir())
	if err != nil {
		t.Fatalf("InspectRecovery without a receipt: %v", err)
	}
	if plan.Action != RecoveryActionNone {
		t.Fatalf("action = %s, want none", plan.Action)
	}
}

func TestInspectRecoverySettledReceiptsAreNone(t *testing.T) {
	tests := []struct {
		name   string
		commit bool
		mutate func(r *Receipt)
	}{
		{"confirmed", true, func(r *Receipt) { r.Outcome = OutcomeConfirmed }},
		{"rolled back", true, func(r *Receipt) {
			r.Outcome = OutcomeRolledBack
			r.Phase = PhaseRollbackAttempted
		}},
		{"abandoned resolution", false, func(r *Receipt) {
			r.Resolution = ResolutionAbandonedPreReplacement
			r.InstallFailed = true
		}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f, _ := beginRecoveryTx(t, tc.commit)
			r, err := ReadReceipt(ReceiptPath(f.exec.Path))
			if err != nil {
				t.Fatalf("ReadReceipt: %v", err)
			}
			tc.mutate(&r)
			if err := WriteReceiptDurable(ReceiptPath(f.exec.Path), r); err != nil {
				t.Fatalf("rewrite receipt: %v", err)
			}
			plan, err := InspectRecovery(f.exec, f.runtimeDir)
			if err != nil {
				t.Fatalf("InspectRecovery settled receipt: %v", err)
			}
			if plan.Action != RecoveryActionNone {
				t.Fatalf("action = %s, want none", plan.Action)
			}
			if plan.ReceiptPath != ReceiptPath(f.exec.Path) {
				t.Fatalf("plan receipt path = %s, want %s", plan.ReceiptPath, ReceiptPath(f.exec.Path))
			}
			if plan.Receipt.TransactionID != r.TransactionID {
				t.Fatalf("plan receipt txid = %s, want %s", plan.Receipt.TransactionID, r.TransactionID)
			}
		})
	}
}

func TestInspectRecoveryPendingDecisions(t *testing.T) {
	t.Run("backup-ready original installed abandons", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, false)
		plan, err := InspectRecovery(f.exec, f.runtimeDir)
		if err != nil {
			t.Fatalf("InspectRecovery: %v", err)
		}
		if plan.Action != RecoveryActionAbandon {
			t.Fatalf("action = %s, want abandon", plan.Action)
		}
		if plan.ReceiptPath != ReceiptPath(f.exec.Path) {
			t.Fatalf("plan receipt path = %s, want keyed %s", plan.ReceiptPath, ReceiptPath(f.exec.Path))
		}
	})
	t.Run("backup-ready candidate installed restores after crash inside commit", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, true)
		crashInsideCommit(t, f)
		plan, err := InspectRecovery(f.exec, f.runtimeDir)
		if err != nil {
			t.Fatalf("InspectRecovery: %v", err)
		}
		if plan.Action != RecoveryActionRestore {
			t.Fatalf("action = %s, want restore", plan.Action)
		}
	})
	t.Run("replacement-complete candidate installed restores", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, true)
		plan, err := InspectRecovery(f.exec, f.runtimeDir)
		if err != nil {
			t.Fatalf("InspectRecovery: %v", err)
		}
		if plan.Action != RecoveryActionRestore {
			t.Fatalf("action = %s, want restore", plan.Action)
		}
	})
}

func TestInspectRecoveryRefusesUnsafeRecords(t *testing.T) {
	t.Run("wrong runtime dir", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, false)
		_, err := InspectRecovery(f.exec, t.TempDir())
		requireRefusal(t, err)
	})
	t.Run("keyed receipt binds another executable", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		if err := ensureLeaseDir(exec.Path); err != nil {
			t.Fatalf("ensureLeaseDir: %v", err)
		}
		foreign := Receipt{
			SchemaVersion:  receiptSchemaVersion,
			TransactionID:  testTxID,
			ExecutablePath: "/elsewhere/bin/agentico",
			Outcome:        OutcomePending,
			Phase:          PhaseBackupReady,
		}
		if err := WriteReceiptDurable(ReceiptPath(exec.Path), foreign); err != nil {
			t.Fatalf("write foreign receipt: %v", err)
		}
		_, err := InspectRecovery(exec, t.TempDir())
		requireRefusal(t, err)
	})
	t.Run("corrupt keyed receipt json", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		if err := ensureLeaseDir(exec.Path); err != nil {
			t.Fatalf("ensureLeaseDir: %v", err)
		}
		if err := os.WriteFile(ReceiptPath(exec.Path), []byte("{not json"), 0o600); err != nil {
			t.Fatalf("write garbage receipt: %v", err)
		}
		_, err := InspectRecovery(exec, t.TempDir())
		requireRefusal(t, err)
	})
	t.Run("unsupported schema version", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, false)
		r, err := ReadReceipt(ReceiptPath(f.exec.Path))
		if err != nil {
			t.Fatalf("ReadReceipt: %v", err)
		}
		r.SchemaVersion = 2
		if err := WriteReceiptDurable(ReceiptPath(f.exec.Path), r); err != nil {
			t.Fatalf("rewrite receipt: %v", err)
		}
		_, err = InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
	t.Run("illegal phase outcome combinations", func(t *testing.T) {
		tests := []struct {
			name   string
			commit bool
			mutate func(r *Receipt)
		}{
			{"pending rollback-attempted", true, func(r *Receipt) {
				r.Outcome = OutcomePending
				r.Phase = PhaseRollbackAttempted
			}},
			{"confirmed backup-ready", false, func(r *Receipt) {
				r.Outcome = OutcomeConfirmed
			}},
			{"rolled back replacement-complete", true, func(r *Receipt) {
				r.Outcome = OutcomeRolledBack
			}},
			{"confirmed with resolution", true, func(r *Receipt) {
				r.Outcome = OutcomeConfirmed
				r.Resolution = ResolutionAbandonedPreReplacement
			}},
		}
		for _, tc := range tests {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				f, _ := beginRecoveryTx(t, tc.commit)
				r, err := ReadReceipt(ReceiptPath(f.exec.Path))
				if err != nil {
					t.Fatalf("ReadReceipt: %v", err)
				}
				tc.mutate(&r)
				if err := WriteReceiptDurable(ReceiptPath(f.exec.Path), r); err != nil {
					t.Fatalf("rewrite receipt: %v", err)
				}
				_, err = InspectRecovery(f.exec, f.runtimeDir)
				requireRefusal(t, err)
			})
		}
	})
	t.Run("receipt mode 0644", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, false)
		if err := os.Chmod(ReceiptPath(f.exec.Path), 0o644); err != nil {
			t.Fatalf("chmod receipt: %v", err)
		}
		_, err := InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
	t.Run("receipt owned by another uid", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, false)
		if err := os.Chown(ReceiptPath(f.exec.Path), os.Geteuid()+1, -1); err != nil {
			t.Skipf("cannot hand the receipt to another uid as euid %d: %v", os.Geteuid(), err)
		}
		_, err := InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
	t.Run("symlinked lease dir component", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, false)
		leaseDir := LeaseDir(f.exec.Path)
		real := leaseDir + ".real"
		if err := os.Rename(leaseDir, real); err != nil {
			t.Fatalf("rename lease dir: %v", err)
		}
		if err := os.Symlink(filepath.Base(real), leaseDir); err != nil {
			t.Fatalf("symlink lease dir: %v", err)
		}
		_, err := InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
	t.Run("backup path outside tx dir", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, false)
		r, err := ReadReceipt(ReceiptPath(f.exec.Path))
		if err != nil {
			t.Fatalf("ReadReceipt: %v", err)
		}
		r.BackupPath = filepath.Join(t.TempDir(), "elsewhere-backup")
		if err := WriteReceiptDurable(ReceiptPath(f.exec.Path), r); err != nil {
			t.Fatalf("rewrite receipt: %v", err)
		}
		_, err = InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
	t.Run("restore with missing backup", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, true)
		r := crashInsideCommit(t, f)
		if err := os.Remove(r.BackupPath); err != nil {
			t.Fatalf("remove backup: %v", err)
		}
		_, err := InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
	t.Run("restore with backup digest mismatch", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, true)
		r := crashInsideCommit(t, f)
		data, err := os.ReadFile(r.BackupPath)
		if err != nil {
			t.Fatalf("read backup: %v", err)
		}
		data[len(data)-1] ^= 0xff
		if err := os.WriteFile(r.BackupPath, data, 0o600); err != nil {
			t.Fatalf("tamper backup bytes: %v", err)
		}
		_, err = InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
	t.Run("restore with backup identity mismatch", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, true)
		r := crashInsideCommit(t, f)
		data, err := os.ReadFile(r.BackupPath)
		if err != nil {
			t.Fatalf("read backup: %v", err)
		}
		tmp := writeExecutableFile(t, filepath.Dir(r.BackupPath), ".backup-swap", 0o600, data)
		if err := os.Rename(tmp, r.BackupPath); err != nil {
			t.Fatalf("swap backup inode: %v", err)
		}
		_, err = InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
	t.Run("restore with hard-linked backup", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, true)
		r := crashInsideCommit(t, f)
		if err := os.Link(r.BackupPath, filepath.Join(t.TempDir(), "backup-link")); err != nil {
			t.Fatalf("hard-link backup: %v", err)
		}
		_, err := InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
}

func TestInspectRecoveryRefusesExternallyReplacedInstalled(t *testing.T) {
	t.Run("replacement-complete different bytes", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, true)
		replaceFile(t, f.installedPath, "externally-replaced-after-commit")
		_, err := InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
	t.Run("replacement-complete candidate bytes on unexpected inode", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, true)
		tmp := writeExecutableFile(t, filepath.Dir(f.installedPath), ".ext-tmp", 0o755, []byte("candidate-v2-bytes"))
		if err := os.Rename(tmp, f.installedPath); err != nil {
			t.Fatalf("rename external copy over installed: %v", err)
		}
		_, err := InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
	t.Run("backup-ready unknown bytes", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, false)
		replaceFile(t, f.installedPath, "unknown-bytes")
		_, err := InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
}

func TestInspectRecoveryRestoreAttemptRecorded(t *testing.T) {
	t.Run("restored copy installed finishes rollback", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, true)
		lease := holdLease(t, f)
		plan, err := InspectRecovery(f.exec, f.runtimeDir)
		if err != nil {
			t.Fatalf("InspectRecovery: %v", err)
		}
		if plan.Action != RecoveryActionRestore {
			t.Fatalf("action = %s, want restore", plan.Action)
		}
		if _, err := RestorePrevious(f.exec, lease, plan, "target failed", FileOps{}); err != nil {
			t.Fatalf("RestorePrevious: %v", err)
		}
		// The crash boundary: the attempt was durable and the rename
		// landed, but rolled_back never persisted.
		crashed, err := ReadReceipt(plan.ReceiptPath)
		if err != nil {
			t.Fatalf("ReadReceipt: %v", err)
		}
		crashed.Outcome = OutcomePending
		crashed.Phase = PhaseReplacementComplete
		if err := WriteReceiptDurable(plan.ReceiptPath, crashed); err != nil {
			t.Fatalf("rewrite receipt: %v", err)
		}
		again, err := InspectRecovery(f.exec, f.runtimeDir)
		if err != nil {
			t.Fatalf("InspectRecovery after rewind: %v", err)
		}
		if again.Action != RecoveryActionFinishRollback {
			t.Fatalf("action = %s, want finish-rollback", again.Action)
		}
	})
	t.Run("candidate still installed restores", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, true)
		r, err := ReadReceipt(ReceiptPath(f.exec.Path))
		if err != nil {
			t.Fatalf("ReadReceipt: %v", err)
		}
		r.RecoveryAttemptID = "11111111111111111111111111111111"
		r.RecoveryKind = RecoveryKindRestore
		if err := WriteReceiptDurable(ReceiptPath(f.exec.Path), r); err != nil {
			t.Fatalf("rewrite receipt: %v", err)
		}
		plan, err := InspectRecovery(f.exec, f.runtimeDir)
		if err != nil {
			t.Fatalf("InspectRecovery: %v", err)
		}
		if plan.Action != RecoveryActionRestore {
			t.Fatalf("action = %s, want restore", plan.Action)
		}
	})
	t.Run("neither restore copy nor candidate matches refuses", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, true)
		r, err := ReadReceipt(ReceiptPath(f.exec.Path))
		if err != nil {
			t.Fatalf("ReadReceipt: %v", err)
		}
		r.RecoveryAttemptID = "11111111111111111111111111111111"
		r.RecoveryKind = RecoveryKindRestore
		if err := WriteReceiptDurable(ReceiptPath(f.exec.Path), r); err != nil {
			t.Fatalf("rewrite receipt: %v", err)
		}
		replaceFile(t, f.installedPath, "externally-replaced-mid-recovery")
		_, err = InspectRecovery(f.exec, f.runtimeDir)
		requireRefusal(t, err)
	})
}

func TestInspectRecoveryRestartAttemptRecorded(t *testing.T) {
	f, _ := beginRecoveryTx(t, false)
	lease := holdLease(t, f)
	plan, err := InspectRecovery(f.exec, f.runtimeDir)
	if err != nil {
		t.Fatalf("InspectRecovery: %v", err)
	}
	if plan.Action != RecoveryActionAbandon {
		t.Fatalf("action = %s, want abandon before restart marking", plan.Action)
	}
	if _, err := MarkRecoveryRestart(f.exec, lease, plan, "aborted shutdown", FileOps{}); err != nil {
		t.Fatalf("MarkRecoveryRestart: %v", err)
	}
	again, err := InspectRecovery(f.exec, f.runtimeDir)
	if err != nil {
		t.Fatalf("InspectRecovery after restart marking: %v", err)
	}
	if again.Action != RecoveryActionAbandon {
		t.Fatalf("action = %s, want abandon", again.Action)
	}
	replaceFile(t, f.installedPath, "externally-replaced-after-restart")
	_, err = InspectRecovery(f.exec, f.runtimeDir)
	requireRefusal(t, err)
}

func TestRestorePreviousFullJourney(t *testing.T) {
	f, _ := beginRecoveryTx(t, true)
	lease := holdLease(t, f)
	plan, err := InspectRecovery(f.exec, f.runtimeDir)
	if err != nil {
		t.Fatalf("InspectRecovery: %v", err)
	}
	if plan.Action != RecoveryActionRestore {
		t.Fatalf("action = %s, want restore", plan.Action)
	}

	restored, err := RestorePrevious(f.exec, lease, plan, "target failed health wait", FileOps{})
	if err != nil {
		t.Fatalf("RestorePrevious: %v", err)
	}
	if restored.Outcome != OutcomeRolledBack || restored.Phase != PhaseRollbackAttempted {
		t.Fatalf("restored receipt = %s/%s, want rolled_back/rollback-attempted", restored.Outcome, restored.Phase)
	}
	if restored.RecoveryAttemptID == "" || restored.RecoveryKind != RecoveryKindRestore || restored.RecoveryAttemptBy != os.Getpid() || restored.RecoveryStartedAt.IsZero() {
		t.Fatalf("attempt metadata incomplete: %+v", restored)
	}
	if restored.RestorePath == "" || restored.RestoreID == nil {
		t.Fatalf("restore copy not recorded: %+v", restored)
	}
	if filepath.Dir(restored.RestorePath) != filepath.Dir(restored.BackupPath) || !strings.HasPrefix(filepath.Base(restored.RestorePath), restorePrefix) {
		t.Fatalf("restore path %s is not a prepared copy in the tx dir", restored.RestorePath)
	}

	installedID, err := StatFile(f.installedPath)
	if err != nil {
		t.Fatalf("stat installed: %v", err)
	}
	if got := mustDigest(t, f.installedPath); got != f.exec.Digest {
		t.Fatalf("installed digest = %s, want previous build digest %s", got, f.exec.Digest)
	}
	if installedID.Mode != f.exec.ID.Mode {
		t.Fatalf("installed mode = %o, want original %o", installedID.Mode, f.exec.ID.Mode)
	}
	if installedID.Dev != restored.RestoreID.Dev || installedID.Ino != restored.RestoreID.Ino || installedID.Size != restored.RestoreID.Size {
		t.Fatalf("installed identity %+v is not the prepared restore copy %+v", installedID, *restored.RestoreID)
	}

	onDisk, err := ReadReceipt(plan.ReceiptPath)
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if onDisk.Outcome != OutcomeRolledBack || onDisk.Phase != PhaseRollbackAttempted || onDisk.RecoveryAttemptID != restored.RecoveryAttemptID {
		t.Fatalf("on-disk receipt = %s/%s, want rolled_back/rollback-attempted", onDisk.Outcome, onDisk.Phase)
	}

	lookup, err := LookupSuppression(f.exec.Path, f.opts.ToVersion)
	if err != nil || !lookup.Suppressed {
		t.Fatalf("LookupSuppression(%s) = (%v, %v), want suppressed", f.opts.ToVersion, lookup.Suppressed, err)
	}
	other, err := LookupSuppression(f.exec.Path, "3.0.0")
	if err != nil || other.Suppressed {
		t.Fatalf("LookupSuppression(3.0.0) = (%v, %v), want not suppressed", other.Suppressed, err)
	}

	if _, err := os.Stat(restored.BackupPath); err != nil {
		t.Fatalf("backup must survive until cleanup: %v", err)
	}
}

func TestRestorePreviousReusesPreparedRestoreCopy(t *testing.T) {
	f, _ := beginRecoveryTx(t, true)
	lease := holdLease(t, f)
	plan, err := InspectRecovery(f.exec, f.runtimeDir)
	if err != nil {
		t.Fatalf("InspectRecovery: %v", err)
	}

	boom := errors.New("rename boom")
	if _, err := RestorePrevious(f.exec, lease, plan, "interrupted", FileOps{Rename: func(string, string) error { return boom }}); !errors.Is(err, boom) {
		t.Fatalf("interrupted RestorePrevious error = %v, want rename boom", err)
	}
	interrupted, err := ReadReceipt(plan.ReceiptPath)
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if interrupted.RecoveryAttemptID == "" || interrupted.RecoveryKind != RecoveryKindRestore || interrupted.RestorePath == "" || interrupted.RestoreID == nil {
		t.Fatalf("write-ahead attempt not durable after interruption: %+v", interrupted)
	}
	if got := mustDigest(t, f.installedPath); got != f.candidateDigest {
		t.Fatalf("installed digest = %s, want candidate still installed %s", got, f.candidateDigest)
	}
	preparedID, err := StatFile(interrupted.RestorePath)
	if err != nil {
		t.Fatalf("stat prepared restore copy: %v", err)
	}
	if countRestoreCopies(t, filepath.Dir(interrupted.RestorePath)) != 1 {
		t.Fatalf("want exactly one prepared restore copy after the interrupted attempt")
	}

	resumed, err := InspectRecovery(f.exec, f.runtimeDir)
	if err != nil {
		t.Fatalf("InspectRecovery after interruption: %v", err)
	}
	if resumed.Action != RecoveryActionRestore {
		t.Fatalf("action = %s, want restore", resumed.Action)
	}
	final, err := RestorePrevious(f.exec, lease, resumed, "resumed", FileOps{})
	if err != nil {
		t.Fatalf("Resume RestorePrevious: %v", err)
	}
	if final.Outcome != OutcomeRolledBack || final.RestorePath != interrupted.RestorePath {
		t.Fatalf("final receipt = %s with restore path %s, want rolled_back reusing %s", final.Outcome, final.RestorePath, interrupted.RestorePath)
	}
	installedID, err := StatFile(f.installedPath)
	if err != nil {
		t.Fatalf("stat installed: %v", err)
	}
	if installedID.Ino != preparedID.Ino {
		t.Fatalf("installed inode %d is not the reused prepared copy %d", installedID.Ino, preparedID.Ino)
	}
	if _, err := os.Stat(interrupted.RestorePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared copy must be renamed away, stat error = %v", err)
	}
}

func countRestoreCopies(t *testing.T, txDir string) int {
	t.Helper()
	entries, err := os.ReadDir(txDir)
	if err != nil {
		t.Fatalf("read tx dir: %v", err)
	}
	count := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), restorePrefix) {
			count++
		}
	}
	return count
}

func TestRestorePreviousFinishRollbackOnlyCompletesBookkeeping(t *testing.T) {
	f, _ := beginRecoveryTx(t, true)
	lease := holdLease(t, f)
	plan, err := InspectRecovery(f.exec, f.runtimeDir)
	if err != nil {
		t.Fatalf("InspectRecovery: %v", err)
	}
	if _, err := RestorePrevious(f.exec, lease, plan, "target failed", FileOps{}); err != nil {
		t.Fatalf("RestorePrevious: %v", err)
	}
	crashed, err := ReadReceipt(plan.ReceiptPath)
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	crashed.Outcome = OutcomePending
	crashed.Phase = PhaseReplacementComplete
	if err := WriteReceiptDurable(plan.ReceiptPath, crashed); err != nil {
		t.Fatalf("rewrite receipt: %v", err)
	}

	resumed, err := InspectRecovery(f.exec, f.runtimeDir)
	if err != nil {
		t.Fatalf("InspectRecovery: %v", err)
	}
	if resumed.Action != RecoveryActionFinishRollback {
		t.Fatalf("action = %s, want finish-rollback", resumed.Action)
	}
	before, err := os.Stat(f.installedPath)
	if err != nil {
		t.Fatalf("stat installed: %v", err)
	}
	beforeID, err := StatFile(f.installedPath)
	if err != nil {
		t.Fatalf("StatFile installed: %v", err)
	}

	final, err := RestorePrevious(f.exec, lease, resumed, "resumed finish", FileOps{})
	if err != nil {
		t.Fatalf("RestorePrevious finish-rollback: %v", err)
	}
	if final.Outcome != OutcomeRolledBack || final.Phase != PhaseRollbackAttempted {
		t.Fatalf("final receipt = %s/%s, want rolled_back/rollback-attempted", final.Outcome, final.Phase)
	}
	after, err := os.Stat(f.installedPath)
	if err != nil {
		t.Fatalf("stat installed: %v", err)
	}
	afterID, err := StatFile(f.installedPath)
	if err != nil {
		t.Fatalf("StatFile installed: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("installed mtime changed on finish-rollback: %s -> %s", before.ModTime(), after.ModTime())
	}
	if afterID.Dev != beforeID.Dev || afterID.Ino != beforeID.Ino || afterID.Size != beforeID.Size {
		t.Fatalf("installed identity changed on finish-rollback: %+v -> %+v", beforeID, afterID)
	}
	onDisk, err := ReadReceipt(plan.ReceiptPath)
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if onDisk.Outcome != OutcomeRolledBack {
		t.Fatalf("on-disk receipt outcome = %s, want rolled_back", onDisk.Outcome)
	}
}

func TestMarkRecoveryRestartJourney(t *testing.T) {
	f, _ := beginRecoveryTx(t, false)
	lease := holdLease(t, f)
	plan, err := InspectRecovery(f.exec, f.runtimeDir)
	if err != nil {
		t.Fatalf("InspectRecovery: %v", err)
	}
	if plan.Action != RecoveryActionAbandon {
		t.Fatalf("action = %s, want abandon", plan.Action)
	}
	marked, err := MarkRecoveryRestart(f.exec, lease, plan, "aborted shutdown", FileOps{})
	if err != nil {
		t.Fatalf("MarkRecoveryRestart: %v", err)
	}
	if marked.RecoveryAttemptID == "" || marked.RecoveryKind != RecoveryKindRestart || marked.RecoveryAttemptBy != os.Getpid() || marked.RecoveryStartedAt.IsZero() {
		t.Fatalf("restart attempt metadata incomplete: %+v", marked)
	}
	if marked.Outcome != OutcomePending || marked.Phase != PhaseBackupReady {
		t.Fatalf("restart marking advanced receipt = %s/%s, want pending/backup-ready", marked.Outcome, marked.Phase)
	}
	assertInstalledIntact(t, f)
	onDisk, err := ReadReceipt(plan.ReceiptPath)
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if onDisk.RecoveryAttemptID != marked.RecoveryAttemptID || onDisk.RecoveryKind != RecoveryKindRestart {
		t.Fatalf("on-disk restart attempt = %+v", onDisk)
	}
}

func TestMarkRecoveryRestartRequiresBackupReadyOriginal(t *testing.T) {
	f, _ := beginRecoveryTx(t, true)
	lease := holdLease(t, f)
	plan, err := InspectRecovery(f.exec, f.runtimeDir)
	if err != nil {
		t.Fatalf("InspectRecovery: %v", err)
	}
	_, err = MarkRecoveryRestart(f.exec, lease, plan, "not a restart", FileOps{})
	requireRefusal(t, err)
}

func TestResolveAbandonedSettlesPreReplacement(t *testing.T) {
	f, _ := beginRecoveryTx(t, false)
	lease := holdLease(t, f)
	plan, err := InspectRecovery(f.exec, f.runtimeDir)
	if err != nil {
		t.Fatalf("InspectRecovery: %v", err)
	}
	if plan.Action != RecoveryActionAbandon {
		t.Fatalf("action = %s, want abandon", plan.Action)
	}
	settled, err := ResolveAbandoned(f.exec, lease, plan, "  installation failed before replacement  ", FileOps{})
	if err != nil {
		t.Fatalf("ResolveAbandoned: %v", err)
	}
	if settled.Outcome != OutcomePending {
		t.Fatalf("outcome = %s, want pending: the vocabulary must not grow", settled.Outcome)
	}
	if settled.Resolution != ResolutionAbandonedPreReplacement || !settled.InstallFailed || settled.ResolvedAt.IsZero() {
		t.Fatalf("settlement metadata incomplete: %+v", settled)
	}
	if settled.Error != "installation failed before replacement" {
		t.Fatalf("settled error = %q, want sanitized reason", settled.Error)
	}
	assertInstalledIntact(t, f)
	if _, err := os.Stat(settled.BackupPath); err != nil {
		t.Fatalf("ResolveAbandoned must remove nothing: %v", err)
	}
	onDisk, err := ReadReceipt(plan.ReceiptPath)
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if onDisk.Resolution != ResolutionAbandonedPreReplacement || !onDisk.InstallFailed {
		t.Fatalf("on-disk settlement = %+v", onDisk)
	}
	lookup, err := LookupSuppression(f.exec.Path, f.opts.ToVersion)
	if err != nil || lookup.Suppressed {
		t.Fatalf("LookupSuppression(%s) = (%v, %v), want not suppressed: an installation failure never suppresses", f.opts.ToVersion, lookup.Suppressed, err)
	}
	after, err := InspectRecovery(f.exec, f.runtimeDir)
	if err != nil {
		t.Fatalf("InspectRecovery after settlement: %v", err)
	}
	if after.Action != RecoveryActionNone {
		t.Fatalf("action after settlement = %s, want none", after.Action)
	}
}

func TestRecoveryMutationsRequireHeldLease(t *testing.T) {
	f, _ := beginRecoveryTx(t, false)
	plan, err := InspectRecovery(f.exec, f.runtimeDir)
	if err != nil {
		t.Fatalf("InspectRecovery: %v", err)
	}
	mutations := map[string]func() error{
		"resolve abandoned": func() error {
			_, err := ResolveAbandoned(f.exec, nil, plan, "reason", FileOps{})
			return err
		},
		"restore previous": func() error {
			_, err := RestorePrevious(f.exec, nil, plan, "reason", FileOps{})
			return err
		},
		"mark restart": func() error {
			_, err := MarkRecoveryRestart(f.exec, nil, plan, "reason", FileOps{})
			return err
		},
	}
	for name, call := range mutations {
		if err := call(); !IsUnsafeRecovery(err) {
			t.Fatalf("%s without a held lease = %v, want refusal", name, err)
		}
	}
}

func twoExecsInOneDir(t *testing.T) (Executable, Executable) {
	t.Helper()
	dir := t.TempDir()
	a := writeExecutableFile(t, dir, "agentico-a", 0o755, []byte("copy-a-bytes"))
	b := writeExecutableFile(t, dir, "agentico-b", 0o755, []byte("copy-b-bytes"))
	execA, err := CaptureExecutableAt(a)
	if err != nil {
		t.Fatalf("capture exec A: %v", err)
	}
	execB, err := CaptureExecutableAt(b)
	if err != nil {
		t.Fatalf("capture exec B: %v", err)
	}
	return execA, execB
}

func beginOptsForExec(t *testing.T, exec Executable, runtimeDir string) BeginOptions {
	t.Helper()
	candidatePath := writeExecutableFile(t, t.TempDir(), "candidate", 0o755, []byte("candidate-for-"+filepath.Base(exec.Path)))
	return BeginOptions{
		RuntimeDir:      runtimeDir,
		StateDir:        filepath.Join(runtimeDir, "features"),
		Config:          filepath.Join(runtimeDir, "config.yaml"),
		PID:             os.Getpid(),
		FromVersion:     "1.0.0",
		ToVersion:       "2.0.0",
		CandidatePath:   candidatePath,
		CandidateDigest: mustDigest(t, candidatePath),
	}
}

func TestTwoExecutablesInOneDirectoryRecoverIndependently(t *testing.T) {
	t.Run("keyed receipt of one executable is invisible to the other", func(t *testing.T) {
		execA, execB := twoExecsInOneDir(t)
		runtimeA := t.TempDir()
		opts := beginOptsForExec(t, execA, runtimeA)
		if _, err := Begin(execA, opts, FileOps{}); err != nil {
			t.Fatalf("Begin on A: %v", err)
		}
		plan, err := InspectRecovery(execB, t.TempDir())
		if err != nil {
			t.Fatalf("InspectRecovery on B: %v", err)
		}
		if plan.Action != RecoveryActionNone {
			t.Fatalf("B action = %s, want none: A's keyed receipt is not B's", plan.Action)
		}
		if _, found, err := ReadLatestReceipt(execB.Path); err != nil || found {
			t.Fatalf("ReadLatestReceipt(B) = (%v, %v), want not found without error", found, err)
		}
	})
}
