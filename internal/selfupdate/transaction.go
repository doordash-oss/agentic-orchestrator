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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// FileOps supplies filesystem and clock overrides shared by installation and
// recovery. Nil fields use the real implementation.
type FileOps struct {
	Copy         func(dst, src string, mode uint32) error
	SyncFile     func(path string) error
	SyncDir      func(path string) error
	WriteReceipt func(path string, r Receipt) error
	Rename       func(oldpath, newpath string) error
	Now          func() time.Time
}

func (s FileOps) copy(dst, src string, mode uint32) error {
	if s.Copy != nil {
		return s.Copy(dst, src, mode)
	}
	return CopyFile(dst, src, mode)
}

func (s FileOps) syncFile(path string) error {
	if s.SyncFile != nil {
		return s.SyncFile(path)
	}
	return SyncFile(path)
}

func (s FileOps) syncDir(path string) error {
	if s.SyncDir != nil {
		return s.SyncDir(path)
	}
	return SyncDir(path)
}

func (s FileOps) rename(oldpath, newpath string) error {
	if s.Rename != nil {
		return s.Rename(oldpath, newpath)
	}
	return os.Rename(oldpath, newpath)
}

func (s FileOps) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s FileOps) writeReceipt(path string, receipt Receipt) error {
	if s.WriteReceipt != nil {
		return s.WriteReceipt(path, receipt)
	}
	return writeReceiptAtomic(path, receipt, receipt.UpdatedAt)
}

// BeginOptions carries the runtime identity and candidate inputs for one
// transaction.
type BeginOptions struct {
	RuntimeDir, StateDir, Config string
	PID, PGID                    int
	FromVersion, ToVersion       string
	CandidatePath                string
	CandidateDigest              string
	Bind                         BindEndpoint
}

// Transaction is a prepared, durable replacement of one installed executable.
// Every step before Commit leaves the installed bytes untouched.
type Transaction struct {
	exec    Executable
	opts    BeginOptions
	seams   FileOps
	txDir   string
	receipt Receipt
	// releaseProvenance, when set by BeginVerifiedRelease, re-runs the full
	// release trust revalidation immediately before the replacement rename.
	releaseProvenance *releaseProvenance
}

// stageCopy persists a fresh copy and its directory entry before recording identity.
func (s FileOps) stageCopy(dst, src string, mode uint32) (FileIdentity, error) {
	if err := s.copy(dst, src, mode); err != nil {
		return FileIdentity{}, err
	}
	if err := s.syncFile(dst); err != nil {
		return FileIdentity{}, err
	}
	if err := s.syncDir(filepath.Dir(dst)); err != nil {
		return FileIdentity{}, err
	}
	return StatFile(dst)
}

func restoreInstalledModeAndOwner(path string, r Receipt) error {
	if err := os.Chmod(path, os.FileMode(r.OriginalMode)); err != nil {
		return fmt.Errorf("restore installed mode: %w", err)
	}
	// Never elevate or take ownership we did not start with.
	if r.OriginalUID == os.Geteuid() && r.OriginalGID == os.Getegid() {
		if err := os.Chown(path, r.OriginalUID, r.OriginalGID); err != nil {
			return fmt.Errorf("restore installed ownership: %w", err)
		}
	}
	return nil
}

// CopyFile copies src into a newly created dst with exactly the requested
// permission bits. It never hard-links, never removes the source, and never
// chmods a link: dst is created O_EXCL and is always a fresh regular file.
func CopyFile(dst, src string, mode uint32) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open copy source %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(mode))
	if err != nil {
		return fmt.Errorf("create copy dest %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	// Explicit chmod: the O_CREAT mode is umask-masked and the staged mode
	// must match the original executable exactly for commit verification.
	if err := out.Chmod(os.FileMode(mode)); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("chmod copy dest %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("close copy dest %s: %w", dst, err)
	}
	return nil
}

// SyncFile flushes a file's bytes to stable storage.
func SyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s for sync: %w", path, err)
	}
	defer f.Close()
	if err := unix.Fsync(int(f.Fd())); err != nil {
		return fmt.Errorf("fsync %s: %w", path, err)
	}
	return nil
}

// SyncDir flushes a directory's entries to stable storage.
func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open dir %s for sync: %w", path, err)
	}
	defer f.Close()
	if err := unix.Fsync(int(f.Fd())); err != nil {
		return fmt.Errorf("fsync dir %s: %w", path, err)
	}
	return nil
}

// VerifyCandidate validates a locally built candidate before any
// installed-path mutation: a regular file (not a symlink) owned by the
// current effective user, executable, non-empty, with exactly the expected
// sha256 digest. This is a local-build check, not release verification.
func VerifyCandidate(path, digest string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("lstat candidate: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("candidate %s is not a regular file", path)
	}
	if info.Size() <= 0 {
		return fmt.Errorf("candidate %s is empty", path)
	}
	id := identityFromInfo(info)
	if id.UID != os.Geteuid() {
		return fmt.Errorf("candidate %s is not owned by the current effective user", path)
	}
	if id.Mode&0o111 == 0 {
		return fmt.Errorf("candidate %s is not executable", path)
	}
	actual, err := DigestFile(path)
	if err != nil {
		return err
	}
	if actual != digest {
		return fmt.Errorf("candidate digest %s does not match expected %s", actual, digest)
	}
	return nil
}

func newTransactionID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate transaction id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Begin prepares a durable replacement without touching the installed bytes:
// it re-checks the installed identity, verifies the candidate, creates the
// private transaction dir, copies and syncs a rollback backup (0600) and a
// staged candidate (original mode), and persists a pending backup-ready
// receipt. Any failure leaves the installed executable exactly as it was.
func Begin(exec Executable, opts BeginOptions, seams FileOps) (*Transaction, error) {
	if opts.CandidatePath == "" {
		return nil, errors.New("candidate path is empty")
	}
	if opts.CandidateDigest == "" {
		return nil, errors.New("candidate digest is empty")
	}
	matches, err := exec.PathStillMatches()
	if err != nil {
		return nil, err
	}
	if !matches {
		return nil, fmt.Errorf("installed executable %s no longer matches captured identity", exec.Path)
	}
	if err := VerifyCandidate(opts.CandidatePath, opts.CandidateDigest); err != nil {
		return nil, err
	}
	t := &Transaction{exec: exec, opts: opts, seams: seams}
	if err := ensureLeaseDir(exec.Path); err != nil {
		return nil, err
	}
	txID, err := newTransactionID()
	if err != nil {
		return nil, err
	}
	t.txDir = filepath.Join(LeaseDir(exec.Path), txDirPrefix+txID)
	if err := os.Mkdir(t.txDir, 0o700); err != nil {
		return nil, fmt.Errorf("create transaction dir: %w", err)
	}
	if err := os.Chmod(t.txDir, 0o700); err != nil {
		return nil, fmt.Errorf("repair transaction dir permissions: %w", err)
	}
	if err := t.seams.syncDir(LeaseDir(exec.Path)); err != nil {
		return nil, err
	}
	// Durable staging ownership precedes every staged byte and the install
	// receipt: a dir whose record never became durable is never guessed to
	// be owned by a later launch, and a dir whose record did is recognized
	// abandoned staging once its transaction is settled or gone. The stamp
	// is captured once so the staging record and the receipt share it.
	startedAt := t.seams.now()
	if err := writeStagingRecord(t.txDir, StagingRecord{
		TransactionID:    txID,
		ExecutablePath:   exec.Path,
		ExecutableDigest: exec.Digest,
		CreatedAt:        startedAt,
	}); err != nil {
		return nil, err
	}

	backupPath := filepath.Join(t.txDir, backupName)
	backupID, err := t.seams.stageCopy(backupPath, exec.Path, 0o600)
	if err != nil {
		return nil, err
	}

	stagingPath := filepath.Join(t.txDir, stagedName)
	stagingID, err := t.seams.stageCopy(stagingPath, opts.CandidatePath, exec.ID.Mode)
	if err != nil {
		return nil, err
	}

	t.receipt = Receipt{
		SchemaVersion:  receiptSchemaVersion,
		TransactionID:  txID,
		RuntimeDir:     opts.RuntimeDir,
		StateDir:       opts.StateDir,
		Config:         opts.Config,
		PID:            opts.PID,
		PGID:           opts.PGID,
		FromVersion:    opts.FromVersion,
		ToVersion:      opts.ToVersion,
		ExecutablePath: exec.Path,
		ExecutableID:   exec.ID,
		OriginalMode:   exec.ID.Mode,
		OriginalUID:    exec.ID.UID,
		OriginalGID:    exec.ID.GID,
		OldDigest:      exec.Digest,
		NewDigest:      opts.CandidateDigest,
		BackupPath:     backupPath,
		BackupID:       backupID,
		StagingPath:    stagingPath,
		StagingID:      stagingID,
		Bind:           opts.Bind,
		StartedAt:      startedAt,
		UpdatedAt:      startedAt,
		Outcome:        OutcomePending,
		Phase:          PhaseBackupReady,
	}
	if err := t.seams.writeReceipt(ReceiptPath(t.exec.Path), t.receipt); err != nil {
		return nil, err
	}
	return t, nil
}

// Receipt returns the transaction's current receipt state.
func (t *Transaction) Receipt() Receipt {
	return t.receipt
}

// TxDir returns the private transaction directory.
func (t *Transaction) TxDir() string {
	return t.txDir
}

// VerifyInstalledUnchanged asserts the installed path still carries the
// original inode and bytes recorded at Begin.
func (t *Transaction) VerifyInstalledUnchanged() error {
	id, err := StatFile(t.exec.Path)
	if err != nil {
		return err
	}
	if !id.SameFile(t.receipt.ExecutableID) {
		return fmt.Errorf("installed executable %s identity changed since transaction start", t.exec.Path)
	}
	digest, err := DigestFile(t.exec.Path)
	if err != nil {
		return err
	}
	if digest != t.receipt.OldDigest {
		return fmt.Errorf("installed executable %s bytes changed since transaction start", t.exec.Path)
	}
	return nil
}

// Commit atomically replaces the installed executable with the staged
// candidate. The staged file is re-verified (identity, digest, mode,
// containment in the transaction dir) and the installed file re-checked
// before the rename; the rename is synced and permissions/ownership restored
// without elevation before the receipt phase advances. A release-backed
// transaction additionally re-runs its full trust revalidation — retained
// manifest signature and asset/digest binding, target eligibility and
// suppression, and link safety on the staging path — immediately before the
// rename, without refetching or re-probing.
func (t *Transaction) Commit() error {
	if err := t.VerifyInstalledUnchanged(); err != nil {
		return err
	}
	if t.releaseProvenance != nil {
		if err := revalidateReleaseProvenance(t.exec, t.releaseProvenance.candidate, t.releaseProvenance.stageOpts); err != nil {
			return fmt.Errorf("commit-boundary revalidation: %w", err)
		}
		if err := validateNoSymlinkComponents(t.receipt.StagingPath); err != nil {
			return fmt.Errorf("commit-boundary link safety: %w", err)
		}
		if t.receipt.ToVersion != t.releaseProvenance.candidate.release.Resolved().Version {
			return fmt.Errorf("commit-boundary target %q does not match the verified release %q", t.receipt.ToVersion, t.releaseProvenance.candidate.release.Resolved().Version)
		}
	}
	stagingID, err := StatFile(t.receipt.StagingPath)
	if err != nil {
		return err
	}
	if !stagingID.SameFile(t.receipt.StagingID) {
		return fmt.Errorf("staged candidate %s changed since preparation", t.receipt.StagingPath)
	}
	if filepath.Dir(t.receipt.StagingPath) != t.txDir {
		return fmt.Errorf("staged candidate %s is outside transaction dir %s", t.receipt.StagingPath, t.txDir)
	}
	digest, err := DigestFile(t.receipt.StagingPath)
	if err != nil {
		return err
	}
	if digest != t.receipt.NewDigest {
		return fmt.Errorf("staged candidate digest %s does not match expected %s", digest, t.receipt.NewDigest)
	}
	if stagingID.Mode != t.receipt.OriginalMode {
		return fmt.Errorf("staged candidate mode %o does not match original %o", stagingID.Mode, t.receipt.OriginalMode)
	}

	if err := t.seams.rename(t.receipt.StagingPath, t.exec.Path); err != nil {
		return err
	}
	// Write-ahead honesty: the receipt phase only advances to
	// replacement-complete after the rename is durable. A failure of this
	// directory sync returns an error while the receipt still says
	// backup-ready, even though the installed bytes were replaced: the
	// durable record must never claim a phase that is not yet on disk.
	if err := t.seams.syncDir(filepath.Dir(t.exec.Path)); err != nil {
		return err
	}
	if err := restoreInstalledModeAndOwner(t.exec.Path, t.receipt); err != nil {
		return err
	}

	t.receipt.Phase = PhaseReplacementComplete
	t.receipt.UpdatedAt = t.seams.now()
	return t.seams.writeReceipt(ReceiptPath(t.exec.Path), t.receipt)
}

// RecordError persists a sanitized error message into the receipt without
// advancing phase or outcome: an errored transaction keeps its truthful
// progress marker.
func (t *Transaction) RecordError(errMsg string, secrets ...string) error {
	t.receipt.Error = SanitizeError(errMsg, secrets...)
	t.receipt.UpdatedAt = t.seams.now()
	return t.seams.writeReceipt(ReceiptPath(t.exec.Path), t.receipt)
}

// Confirm marks the transaction confirmed. Callers must only confirm after
// the replacement image has fully started and published its presence.
func (t *Transaction) Confirm() error {
	if t.receipt.Phase != PhaseReplacementComplete {
		return fmt.Errorf("cannot confirm transaction in phase %q", t.receipt.Phase)
	}
	t.receipt.Outcome = OutcomeConfirmed
	t.receipt.UpdatedAt = t.seams.now()
	return t.seams.writeReceipt(ReceiptPath(t.exec.Path), t.receipt)
}

// validTransactionID reports whether txID has the exact shape newTransactionID
// mints (16-byte lowercase hex). The standalone confirm/cleanup helpers key a
// filesystem path on the id, so anything that could smuggle path separators
// or traversal segments is rejected outright.
func validTransactionID(txID string) bool {
	if len(txID) != 32 {
		return false
	}
	for i := 0; i < len(txID); i++ {
		c := txID[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ConfirmTransaction durably records outcome=confirmed for the latest
// receipt of the executable when it still matches txID and is pending. The
// receipt is stored only at the executable-specific path.
// Mismatched or advanced receipts are an error — confirmation is write-once,
// so a caller can never confirm another process's transaction or re-confirm
// a settled one.
func ConfirmTransaction(execPath, txID string) (Receipt, error) {
	if !validTransactionID(txID) {
		return Receipt{}, fmt.Errorf("invalid transaction id %q", txID)
	}
	r, found, err := ReadLatestReceipt(execPath)
	if err != nil || !found {
		if err == nil {
			err = fmt.Errorf("no receipt binds to executable %s", execPath)
		}
		return Receipt{}, err
	}
	path := ReceiptPath(execPath)
	if r.TransactionID != txID {
		return Receipt{}, fmt.Errorf("latest receipt transaction %q does not match %q", r.TransactionID, txID)
	}
	if !r.ActionablePending() {
		return Receipt{}, fmt.Errorf("receipt outcome %q (resolution %q) is not actionable pending", r.Outcome, r.Resolution)
	}
	if r.Phase != PhaseReplacementComplete {
		return Receipt{}, fmt.Errorf("receipt phase %q is not replacement-complete", r.Phase)
	}
	r.Outcome = OutcomeConfirmed
	r.UpdatedAt = time.Now()
	if err := writeReceiptAtomic(path, r, r.UpdatedAt); err != nil {
		return Receipt{}, err
	}

	return r, nil
}
