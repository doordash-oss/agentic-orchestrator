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

type txFixture struct {
	runtimeDir      string
	installedPath   string
	exec            Executable
	candidatePath   string
	candidateDigest string
	opts            BeginOptions
}

func newTxFixture(t *testing.T) *txFixture {
	t.Helper()
	exec := installExecutable(t, "installed-v1-bytes")
	candidateDir := t.TempDir()
	candidatePath := writeExecutableFile(t, candidateDir, "candidate", 0o755, []byte("candidate-v2-bytes"))
	runtimeDir := t.TempDir()
	opts := BeginOptions{
		RuntimeDir:      runtimeDir,
		StateDir:        filepath.Join(runtimeDir, "features"),
		Config:          filepath.Join(runtimeDir, "config.yaml"),
		PID:             os.Getpid(),
		FromVersion:     "1.0.0",
		ToVersion:       "2.0.0",
		CandidatePath:   candidatePath,
		CandidateDigest: mustDigest(t, candidatePath),
		Bind: BindEndpoint{
			Host:         "127.0.0.1",
			Port:         54321,
			AdvertiseURL: "http://127.0.0.1:54321",
			Policy:       "loopback",
		},
	}
	return &txFixture{
		runtimeDir:      runtimeDir,
		installedPath:   exec.Path,
		exec:            exec,
		candidatePath:   candidatePath,
		candidateDigest: opts.CandidateDigest,
		opts:            opts,
	}
}

func assertInstalledIntact(t *testing.T, f *txFixture) {
	t.Helper()
	id, err := StatFile(f.installedPath)
	if err != nil {
		t.Fatalf("stat installed: %v", err)
	}
	if id.Dev != f.exec.ID.Dev || id.Ino != f.exec.ID.Ino || id.Size != f.exec.ID.Size {
		t.Fatalf("installed identity changed: %+v, want %+v", id, f.exec.ID)
	}
	if got := mustDigest(t, f.installedPath); got != f.exec.Digest {
		t.Fatalf("installed digest = %s, want %s", got, f.exec.Digest)
	}
}

func assertNoReceipt(t *testing.T, f *txFixture) {
	t.Helper()
	if _, err := os.Stat(ReceiptPath(f.exec.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt must not exist, stat error = %v", err)
	}
}

func TestBeginPreparesDurableBackupAndStaging(t *testing.T) {
	f := newTxFixture(t)
	tx, err := Begin(f.exec, f.opts, FileOps{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	assertInstalledIntact(t, f)

	receipt := tx.Receipt()
	if receipt.Outcome != OutcomePending || receipt.Phase != PhaseBackupReady {
		t.Fatalf("receipt = %s/%s, want pending/backup-ready", receipt.Outcome, receipt.Phase)
	}
	if len(receipt.TransactionID) != 32 {
		t.Fatalf("transaction id %q is not 16-byte hex", receipt.TransactionID)
	}
	if receipt.OldDigest != f.exec.Digest || receipt.NewDigest != f.candidateDigest {
		t.Fatalf("receipt digests = %s/%s", receipt.OldDigest, receipt.NewDigest)
	}
	if receipt.ExecutableID != f.exec.ID || receipt.OriginalMode != f.exec.ID.Mode {
		t.Fatalf("receipt executable identity = %+v mode %o", receipt.ExecutableID, receipt.OriginalMode)
	}

	onDisk, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if onDisk.Outcome != OutcomePending || onDisk.Phase != PhaseBackupReady || onDisk.TransactionID != receipt.TransactionID {
		t.Fatalf("on-disk receipt = %+v", onDisk)
	}

	txDirInfo, err := os.Stat(tx.TxDir())
	if err != nil {
		t.Fatalf("stat tx dir: %v", err)
	}
	if txDirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("tx dir mode = %o, want 700", txDirInfo.Mode().Perm())
	}
	if filepath.Dir(receipt.StagingPath) != tx.TxDir() || filepath.Dir(receipt.BackupPath) != tx.TxDir() {
		t.Fatalf("backup/staging not contained in tx dir: %s %s", receipt.BackupPath, receipt.StagingPath)
	}

	backupID, err := StatFile(receipt.BackupPath)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if backupID.Mode != 0o600 {
		t.Fatalf("backup mode = %o, want 600", backupID.Mode)
	}
	if got := mustDigest(t, receipt.BackupPath); got != f.exec.Digest {
		t.Fatalf("backup digest = %s, want installed digest %s", got, f.exec.Digest)
	}

	stagingID, err := StatFile(receipt.StagingPath)
	if err != nil {
		t.Fatalf("stat staging: %v", err)
	}
	if stagingID.Mode != f.exec.ID.Mode {
		t.Fatalf("staged mode = %o, want original %o", stagingID.Mode, f.exec.ID.Mode)
	}
	if got := mustDigest(t, receipt.StagingPath); got != f.candidateDigest {
		t.Fatalf("staged digest = %s, want candidate digest %s", got, f.candidateDigest)
	}
}

func TestBeginRejectsUnsafeCandidates(t *testing.T) {
	symlinkCandidate := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		target := writeExecutableFile(t, dir, "real", 0o755, []byte("real-candidate"))
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		return link
	}

	tests := []struct {
		name   string
		candFn func(t *testing.T) (path, digest string)
	}{
		{"symlink", func(t *testing.T) (string, string) {
			link := symlinkCandidate(t)
			return link, mustDigest(t, link)
		}},
		{"not executable", func(t *testing.T) (string, string) {
			path := writeExecutableFile(t, t.TempDir(), "cand", 0o644, []byte("no-exec"))
			return path, mustDigest(t, path)
		}},
		{"empty file", func(t *testing.T) (string, string) {
			path := writeExecutableFile(t, t.TempDir(), "cand", 0o755, nil)
			return path, mustDigest(t, path)
		}},
		{"wrong digest", func(t *testing.T) (string, string) {
			path := writeExecutableFile(t, t.TempDir(), "cand", 0o755, []byte("candidate-bytes"))
			return path, "0000000000000000000000000000000000000000000000000000000000000000"
		}},
		{"missing file", func(t *testing.T) (string, string) {
			return filepath.Join(t.TempDir(), "missing"), "whatever"
		}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := newTxFixture(t)
			f.opts.CandidatePath, f.opts.CandidateDigest = tc.candFn(t)
			if _, err := Begin(f.exec, f.opts, FileOps{}); err == nil {
				t.Fatal("Begin accepted unsafe candidate")
			}
			assertInstalledIntact(t, f)
			assertNoReceipt(t, f)
		})
	}
}

func TestBeginRejectsReplacedInstalled(t *testing.T) {
	f := newTxFixture(t)
	replaceFile(t, f.installedPath, "externally-replaced")
	if _, err := Begin(f.exec, f.opts, FileOps{}); err == nil {
		t.Fatal("Begin accepted a replaced installed executable")
	}
	assertNoReceipt(t, f)
}

func TestBeginFailureInjectionLeavesInstalledIntact(t *testing.T) {
	boom := errors.New("injected boom")
	tests := []struct {
		name  string
		seams FileOps
	}{
		{"copy backup", FileOps{Copy: func(string, string, uint32) error { return boom }}},
		{"sync file", FileOps{SyncFile: func(string) error { return boom }}},
		{"sync dir", FileOps{SyncDir: func(string) error { return boom }}},
		{"write receipt", FileOps{WriteReceipt: func(string, Receipt) error { return boom }}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := newTxFixture(t)
			if _, err := Begin(f.exec, f.opts, tc.seams); err == nil {
				t.Fatal("Begin unexpectedly succeeded")
			}
			assertInstalledIntact(t, f)
			assertNoReceipt(t, f)
		})
	}
}

func TestCommitReplacesInstalledAtomically(t *testing.T) {
	f := newTxFixture(t)
	tx, err := Begin(f.exec, f.opts, FileOps{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	installedID, err := StatFile(f.installedPath)
	if err != nil {
		t.Fatalf("stat installed: %v", err)
	}
	if got := mustDigest(t, f.installedPath); got != f.candidateDigest {
		t.Fatalf("installed digest = %s, want candidate %s", got, f.candidateDigest)
	}
	if installedID.Mode != f.exec.ID.Mode {
		t.Fatalf("installed mode = %o, want original %o", installedID.Mode, f.exec.ID.Mode)
	}
	if installedID.Ino == f.exec.ID.Ino {
		t.Fatal("installed inode unchanged: replacement must be a rename, not an in-place write")
	}
	if _, err := os.Stat(tx.Receipt().StagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged file must be renamed away, stat error = %v", err)
	}
	if _, err := os.Stat(tx.Receipt().BackupPath); err != nil {
		t.Fatalf("backup must survive until settled cleanup: %v", err)
	}

	receipt, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if receipt.Phase != PhaseReplacementComplete || receipt.Outcome != OutcomePending {
		t.Fatalf("receipt = %s/%s, want replacement-complete/pending", receipt.Phase, receipt.Outcome)
	}
	if !receipt.UpdatedAt.After(receipt.StartedAt) && !receipt.UpdatedAt.Equal(receipt.StartedAt) {
		t.Fatalf("UpdatedAt %s not after StartedAt %s", receipt.UpdatedAt, receipt.StartedAt)
	}
}

func TestCommitRenameFailureLeavesInstalledIntact(t *testing.T) {
	f := newTxFixture(t)
	boom := errors.New("rename boom")
	tx, err := Begin(f.exec, f.opts, FileOps{Rename: func(string, string) error { return boom }})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("Commit with failing rename: expected error")
	}
	assertInstalledIntact(t, f)
	receipt, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if receipt.Phase != PhaseBackupReady {
		t.Fatalf("receipt phase = %s, want backup-ready", receipt.Phase)
	}
}

func TestCommitSyncDirFailureAfterRenameIsHonest(t *testing.T) {
	f := newTxFixture(t)
	boom := errors.New("dir sync boom")
	// Begin syncs three directories (lease dir, tx dir twice); the fourth
	// sync call is the post-rename sync of the installed binary's dir.
	syncCalls := 0
	tx, err := Begin(f.exec, f.opts, FileOps{SyncDir: func(path string) error {
		syncCalls++
		if syncCalls > 3 {
			return boom
		}
		return SyncDir(path)
	}})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("Commit with failing post-rename dir sync: expected error")
	}

	// The installed bytes were replaced, but the durable receipt must stay
	// backup-ready: the phase only advances after the rename is synced, so
	// observers never trust a phase that is not yet on disk.
	if got := mustDigest(t, f.installedPath); got != f.candidateDigest {
		t.Fatalf("installed digest = %s, want replaced with candidate %s", got, f.candidateDigest)
	}
	receipt, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if receipt.Phase != PhaseBackupReady || receipt.Outcome != OutcomePending {
		t.Fatalf("receipt = %s/%s, want backup-ready/pending", receipt.Phase, receipt.Outcome)
	}
}

func TestCommitFailsOnTamperedStaging(t *testing.T) {
	f := newTxFixture(t)
	tx, err := Begin(f.exec, f.opts, FileOps{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	staged := tx.Receipt().StagingPath
	af, err := os.OpenFile(staged, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open staged: %v", err)
	}
	if _, err := af.WriteString("tampered"); err != nil {
		t.Fatalf("tamper staged: %v", err)
	}
	if err := af.Close(); err != nil {
		t.Fatalf("close staged: %v", err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("Commit accepted tampered staging")
	}
	assertInstalledIntact(t, f)
	receipt, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if receipt.Phase != PhaseBackupReady {
		t.Fatalf("receipt phase = %s, want backup-ready", receipt.Phase)
	}
}

func TestCommitFailsOnExternallyReplacedInstalled(t *testing.T) {
	f := newTxFixture(t)
	tx, err := Begin(f.exec, f.opts, FileOps{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	replaceFile(t, f.installedPath, "externally-replaced-between-begin-and-commit")
	if err := tx.Commit(); err == nil {
		t.Fatal("Commit accepted an externally replaced installed executable")
	}
	receipt, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if receipt.Phase != PhaseBackupReady {
		t.Fatalf("receipt phase = %s, want backup-ready", receipt.Phase)
	}
	if _, err := os.Stat(receipt.BackupPath); err != nil {
		t.Fatalf("backup must survive a failed commit: %v", err)
	}
}

func TestRecordErrorAndConfirm(t *testing.T) {
	f := newTxFixture(t)
	tx, err := Begin(f.exec, f.opts, FileOps{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := tx.RecordError("handoff failed; token abc123 rejected", "abc123"); err != nil {
		t.Fatalf("RecordError: %v", err)
	}
	receipt, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if receipt.Error != "handoff failed; token [REDACTED] rejected" {
		t.Fatalf("receipt error = %q", receipt.Error)
	}
	if receipt.Phase != PhaseReplacementComplete || receipt.Outcome != OutcomePending {
		t.Fatalf("RecordError advanced phase/outcome: %s/%s", receipt.Phase, receipt.Outcome)
	}

	if err := tx.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	receipt, err = ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if receipt.Outcome != OutcomeConfirmed || receipt.Phase != PhaseReplacementComplete {
		t.Fatalf("receipt = %s/%s, want replacement-complete/confirmed", receipt.Phase, receipt.Outcome)
	}
}

func TestTransactionReceiptTimesAdvance(t *testing.T) {
	f := newTxFixture(t)
	base := time.Now()
	steps := 0
	tx, err := Begin(f.exec, f.opts, FileOps{Now: func() time.Time {
		steps++
		return base.Add(time.Duration(steps) * time.Minute)
	}})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !tx.Receipt().StartedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("StartedAt = %s", tx.Receipt().StartedAt)
	}
	if err := tx.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !tx.Receipt().UpdatedAt.Equal(base.Add(2 * time.Minute)) {
		t.Fatalf("UpdatedAt = %s, want seam-provided time", tx.Receipt().UpdatedAt)
	}
}

func TestConfirmTransactionConfirmsPendingReceiptOnce(t *testing.T) {
	f := newTxFixture(t)
	tx, err := Begin(f.exec, f.opts, FileOps{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	txID := tx.Receipt().TransactionID

	confirmed, err := ConfirmTransaction(f.exec.Path, txID)
	if err != nil {
		t.Fatalf("ConfirmTransaction: %v", err)
	}
	if confirmed.Outcome != OutcomeConfirmed || confirmed.Phase != PhaseReplacementComplete {
		t.Fatalf("confirmed receipt = %s/%s, want replacement-complete/confirmed", confirmed.Phase, confirmed.Outcome)
	}
	onDisk, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if onDisk.Outcome != OutcomeConfirmed || onDisk.TransactionID != txID {
		t.Fatalf("on-disk receipt = %s/%s, want confirmed/%s", onDisk.Outcome, onDisk.TransactionID, txID)
	}

	// Confirmation is write-once: a second confirm of the same transaction
	// must not silently succeed against the advanced receipt.
	if _, err := ConfirmTransaction(f.exec.Path, txID); err == nil {
		t.Fatal("ConfirmTransaction on already-confirmed receipt = nil error; want write-once rejection")
	}
}

func TestConfirmTransactionRejectsMismatchedTransaction(t *testing.T) {
	f := newTxFixture(t)
	tx, err := Begin(f.exec, f.opts, FileOps{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	other := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := ConfirmTransaction(f.exec.Path, other); err == nil {
		t.Fatal("ConfirmTransaction with mismatched txID = nil error")
	}
	receipt, err := ReadReceipt(ReceiptPath(f.exec.Path))
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if receipt.Outcome != OutcomePending {
		t.Fatalf("receipt outcome = %s; a rejected confirm must leave it pending", receipt.Outcome)
	}
}

func TestConfirmTransactionRejectsInvalidTxIDShape(t *testing.T) {
	f := newTxFixture(t)
	for _, txID := range []string{"", "short", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "../../etc/passwd00000000000000"} {
		if _, err := ConfirmTransaction(f.exec.Path, txID); err == nil {
			t.Fatalf("ConfirmTransaction(%q) = nil error; want shape rejection", txID)
		}
		if err := CleanupSettledTransaction(f.exec.Path, txID, CleanupSeams{}); err == nil {
			t.Fatalf("CleanupSettledTransaction(%q) = nil error; want shape rejection", txID)
		}
	}
}

func TestCleanupSettledTransactionRemovesTxDirOnlyAndIsIdempotent(t *testing.T) {
	f := newTxFixture(t)
	// The lease/record files must be real so the assertion that they survive
	// cleanup proves scoping, not absence.
	lease, err := AcquireLease(f.exec, testRecordFor(f.exec, f.runtimeDir))
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	defer func() { _ = lease.Close() }()
	tx, err := Begin(f.exec, f.opts, FileOps{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	txID := tx.Receipt().TransactionID

	if err := CleanupSettledTransaction(f.exec.Path, txID, CleanupSeams{}); err != nil {
		t.Fatalf("CleanupSettledTransaction: %v", err)
	}
	if _, err := os.Stat(tx.TxDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tx dir must be removed, stat error = %v", err)
	}
	for _, path := range []string{LeasePath(f.exec.Path), OwnershipRecordPath(f.exec.Path), ReceiptPath(f.exec.Path)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cleanup must never remove %s: %v", path, err)
		}
	}

	// Idempotent: a second cleanup of the same transaction is a no-op.
	if err := CleanupSettledTransaction(f.exec.Path, txID, CleanupSeams{}); err != nil {
		t.Fatalf("cleanup must be idempotent: %v", err)
	}
}
