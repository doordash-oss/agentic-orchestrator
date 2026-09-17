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
	"testing"
	"time"
)

func TestBeginWritesDurableStagingRecord(t *testing.T) {
	f, tx := beginRecoveryTx(t, false)
	rec, err := readStagingRecord(tx.TxDir())
	if err != nil {
		t.Fatalf("readStagingRecord: %v", err)
	}
	if rec.SchemaVersion != stagingRecordSchemaVersion {
		t.Fatalf("staging record schema = %d, want %d", rec.SchemaVersion, stagingRecordSchemaVersion)
	}
	if rec.TransactionID != tx.Receipt().TransactionID {
		t.Fatalf("staging record txid = %s, want %s", rec.TransactionID, tx.Receipt().TransactionID)
	}
	if rec.ExecutablePath != f.exec.Path || rec.ExecutableDigest != f.exec.Digest {
		t.Fatalf("staging record does not bind the executable: %+v", rec)
	}
	info, err := os.Stat(filepath.Join(tx.TxDir(), stagingRecordName))
	if err != nil {
		t.Fatalf("stat staging record: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("staging record mode = %o, want 600", info.Mode().Perm())
	}
}

func TestCleanupSettledTransactionRemovesRecognizedObjectsOnly(t *testing.T) {
	f, tx := beginRecoveryTx(t, true)
	txDir := tx.TxDir()
	r := tx.Receipt()
	extra := filepath.Join(txDir, "unexpected.txt")
	if err := os.WriteFile(extra, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write unexpected entry: %v", err)
	}

	err := CleanupSettledTransaction(f.exec.Path, r.TransactionID, CleanupSeams{})
	if err == nil {
		t.Fatal("cleanup with an unexpected entry = nil error, want refusal to remove the dir")
	}
	if _, err := os.Stat(extra); err != nil {
		t.Fatalf("unexpected entry must be retained: %v", err)
	}
	if _, err := os.Stat(txDir); err != nil {
		t.Fatalf("tx dir must be retained while unexpected entries remain: %v", err)
	}
	// Recognized objects are still removed individually so a later launch
	// only retries the directory removal.
	if _, err := os.Stat(r.BackupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recognized backup must be removed, stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(txDir, stagingRecordName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging record must be removed, stat error = %v", err)
	}
}

func TestCleanupSettledTransactionIdempotentAndPartial(t *testing.T) {
	f, tx := beginRecoveryTx(t, false)
	r := tx.Receipt()
	if err := os.Remove(r.BackupPath); err != nil {
		t.Fatalf("simulate earlier partial cleanup: %v", err)
	}
	if err := CleanupSettledTransaction(f.exec.Path, r.TransactionID, CleanupSeams{}); err != nil {
		t.Fatalf("cleanup after partial removal: %v", err)
	}
	if _, err := os.Stat(tx.TxDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tx dir must be removed, stat error = %v", err)
	}
	if err := CleanupSettledTransaction(f.exec.Path, r.TransactionID, CleanupSeams{}); err != nil {
		t.Fatalf("cleanup must be idempotent: %v", err)
	}
}

func TestCleanupSettledTransactionRefusesUnsafeObjects(t *testing.T) {
	t.Run("symlinked object", func(t *testing.T) {
		f, tx := beginRecoveryTx(t, false)
		r := tx.Receipt()
		target := writeExecutableFile(t, t.TempDir(), "target", 0o600, []byte("x"))
		if err := os.Symlink(target, filepath.Join(tx.TxDir(), restorePrefix+"abc")); err != nil {
			t.Fatalf("plant symlinked object: %v", err)
		}
		if err := CleanupSettledTransaction(f.exec.Path, r.TransactionID, CleanupSeams{}); err == nil {
			t.Fatal("cleanup with a symlinked object = nil error")
		}
		for _, path := range []string{r.BackupPath, r.StagingPath, tx.TxDir()} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s must be retained on refusal: %v", path, err)
			}
		}
	})
	t.Run("hard-linked object", func(t *testing.T) {
		f, tx := beginRecoveryTx(t, false)
		r := tx.Receipt()
		if err := os.Link(r.BackupPath, filepath.Join(t.TempDir(), "backup-link")); err != nil {
			t.Fatalf("hard-link backup: %v", err)
		}
		if err := CleanupSettledTransaction(f.exec.Path, r.TransactionID, CleanupSeams{}); err == nil {
			t.Fatal("cleanup with a hard-linked object = nil error")
		}
		if _, err := os.Stat(r.BackupPath); err != nil {
			t.Fatalf("hard-linked backup must be retained on refusal: %v", err)
		}
	})
}

func TestReconcileStagingScopesToOwnedAbandonedDirs(t *testing.T) {
	t.Run("abandoned owned staging is cleaned", func(t *testing.T) {
		f, tx := beginRecoveryTx(t, false)
		// The receipt never became durable: only the staging record
		// names the dir's owner.
		if err := os.Remove(ReceiptPath(f.exec.Path)); err != nil {
			t.Fatalf("remove receipt: %v", err)
		}
		if err := ReconcileStaging(f.exec.Path, "", CleanupSeams{}); err != nil {
			t.Fatalf("ReconcileStaging: %v", err)
		}
		if _, err := os.Stat(tx.TxDir()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("abandoned staging dir must be cleaned, stat error = %v", err)
		}
	})
	t.Run("live transaction is skipped", func(t *testing.T) {
		f, tx := beginRecoveryTx(t, false)
		if err := ReconcileStaging(f.exec.Path, tx.Receipt().TransactionID, CleanupSeams{}); err != nil {
			t.Fatalf("ReconcileStaging: %v", err)
		}
		if _, err := os.Stat(tx.TxDir()); err != nil {
			t.Fatalf("live transaction dir must be untouched: %v", err)
		}
	})
	t.Run("dir without a staging record is untouched", func(t *testing.T) {
		f, _ := beginRecoveryTx(t, false)
		orphan := filepath.Join(LeaseDir(f.exec.Path), txDirPrefix+"orphan")
		if err := os.Mkdir(orphan, 0o700); err != nil {
			t.Fatalf("mkdir orphan: %v", err)
		}
		mystery := filepath.Join(orphan, "mystery")
		if err := os.WriteFile(mystery, []byte("x"), 0o600); err != nil {
			t.Fatalf("write mystery: %v", err)
		}
		if err := ReconcileStaging(f.exec.Path, "", CleanupSeams{}); err != nil {
			t.Fatalf("ReconcileStaging: %v", err)
		}
		if _, err := os.Stat(mystery); err != nil {
			t.Fatalf("a dir with no durable ownership record must never be guessed owned: %v", err)
		}
	})
	t.Run("record binding another executable is untouched", func(t *testing.T) {
		f, tx := beginRecoveryTx(t, false)
		recordPath := filepath.Join(tx.TxDir(), stagingRecordName)
		if err := os.Remove(recordPath); err != nil {
			t.Fatalf("remove staging record: %v", err)
		}
		if err := writeStagingRecord(tx.TxDir(), StagingRecord{
			TransactionID:    tx.Receipt().TransactionID,
			ExecutablePath:   "/elsewhere/bin/agentico",
			ExecutableDigest: "deadbeef",
			CreatedAt:        time.Now(),
		}); err != nil {
			t.Fatalf("write foreign staging record: %v", err)
		}
		if err := ReconcileStaging(f.exec.Path, "", CleanupSeams{}); err != nil {
			t.Fatalf("ReconcileStaging: %v", err)
		}
		if _, err := os.Stat(tx.TxDir()); err != nil {
			t.Fatalf("another executable's staging must be untouched: %v", err)
		}
	})
	t.Run("invalid staging record is untouched", func(t *testing.T) {
		f, tx := beginRecoveryTx(t, false)
		if err := os.WriteFile(filepath.Join(tx.TxDir(), stagingRecordName), []byte(`{"schema_version":2}`), 0o600); err != nil {
			t.Fatalf("write invalid staging record: %v", err)
		}
		if err := ReconcileStaging(f.exec.Path, "", CleanupSeams{}); err != nil {
			t.Fatalf("ReconcileStaging: %v", err)
		}
		if _, err := os.Stat(tx.TxDir()); err != nil {
			t.Fatalf("a dir with an unusable record must be untouched: %v", err)
		}
	})
	t.Run("unrelated lease dir entries are untouched", func(t *testing.T) {
		f, tx := beginRecoveryTx(t, false)
		leaseDir := LeaseDir(f.exec.Path)
		notes := filepath.Join(leaseDir, "notes.txt")
		if err := os.WriteFile(notes, []byte("x"), 0o600); err != nil {
			t.Fatalf("write notes: %v", err)
		}
		plainDir := filepath.Join(leaseDir, "notatxdir")
		if err := os.Mkdir(plainDir, 0o700); err != nil {
			t.Fatalf("mkdir plain dir: %v", err)
		}
		if err := ReconcileStaging(f.exec.Path, tx.Receipt().TransactionID, CleanupSeams{}); err != nil {
			t.Fatalf("ReconcileStaging: %v", err)
		}
		for _, path := range []string{notes, plainDir, ReceiptPath(f.exec.Path)} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s must never be a cleanup candidate: %v", path, err)
			}
		}
	})
	t.Run("missing lease dir", func(t *testing.T) {
		exec := installExecutable(t, "v1")
		if err := ReconcileStaging(exec.Path, "", CleanupSeams{}); err != nil {
			t.Fatalf("ReconcileStaging without a lease dir: %v", err)
		}
	})
}

func TestRecordSettledErrorPreservesSettlement(t *testing.T) {
	f, tx := beginRecoveryTx(t, true)
	r := tx.Receipt()
	r.Outcome = OutcomeConfirmed
	r.UpdatedAt = time.Now().Add(-time.Hour)
	if err := WriteReceiptDurable(ReceiptPath(f.exec.Path), r); err != nil {
		t.Fatalf("write settled receipt: %v", err)
	}
	before, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}

	if err := RecordSettledError(f.exec.Path, ReceiptPath(f.exec.Path), before, "  cleanup retry pending: boom  "); err != nil {
		t.Fatalf("RecordSettledError: %v", err)
	}
	after, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt after settled error: %v", err)
	}
	if after.Error != "cleanup retry pending: boom" {
		t.Fatalf("recorded error = %q, want sanitized message", after.Error)
	}
	if after.Outcome != OutcomeConfirmed || after.Phase != before.Phase || after.Resolution != before.Resolution || after.RecoveryAttemptID != before.RecoveryAttemptID {
		t.Fatalf("settlement changed: %s/%s/%s, want %s/%s/%s", after.Outcome, after.Phase, after.Resolution, before.Outcome, before.Phase, before.Resolution)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("UpdatedAt %s must advance past %s", after.UpdatedAt, before.UpdatedAt)
	}
}
