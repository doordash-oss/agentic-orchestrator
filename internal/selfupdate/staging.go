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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// stagingRecordSchemaVersion is the on-disk schema of StagingRecord.
const stagingRecordSchemaVersion = 1

// StagingRecord is the durable ownership record written inside a transaction
// dir before any install receipt exists. It makes the directory
// self-describing: a tx dir whose record binds this executable is recognized
// owned staging that later launches may clean once abandoned; a tx dir
// created before its record became durable carries no record and is left
// untouched rather than guessed to be owned.
type StagingRecord struct {
	SchemaVersion    int       `json:"schema_version"`
	TransactionID    string    `json:"transaction_id"`
	ExecutablePath   string    `json:"executable_path"`
	ExecutableDigest string    `json:"executable_digest"`
	CreatedAt        time.Time `json:"created_at"`
}

// writeStagingRecord durably writes the staging ownership record inside
// txDir: O_EXCL create, fsync, transaction-dir sync.
func writeStagingRecord(txDir string, rec StagingRecord) error {
	rec.SchemaVersion = stagingRecordSchemaVersion
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal staging record: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(txDir, stagingRecordName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create staging record: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write staging record: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("sync staging record: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close staging record: %w", err)
	}
	if err := SyncDir(txDir); err != nil {
		return fmt.Errorf("sync transaction dir for staging record: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("repair staging record permissions: %w", err)
	}
	return nil
}

// readStagingRecord reads and validates the staging record inside txDir.
func readStagingRecord(txDir string) (StagingRecord, error) {
	data, err := os.ReadFile(filepath.Join(txDir, stagingRecordName))
	if err != nil {
		return StagingRecord{}, err
	}
	var rec StagingRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return StagingRecord{}, fmt.Errorf("parse staging record: %w", err)
	}
	if rec.SchemaVersion != stagingRecordSchemaVersion {
		return StagingRecord{}, fmt.Errorf("staging record schema version %d is not supported", rec.SchemaVersion)
	}
	if !validTransactionID(rec.TransactionID) {
		return StagingRecord{}, fmt.Errorf("staging record transaction id %q is malformed", rec.TransactionID)
	}
	return rec, nil
}

// CleanupSeams are failure-injection points for validated cleanup.
type CleanupSeams struct {
	Remove  func(path string) error
	SyncDir func(path string) error
	Now     func() time.Time
}

// recognizedStagingNames reports whether name is a transaction object this
// package creates inside a tx dir: the rollback backup, the staged
// candidate, a prepared restore copy, the staging ownership record, or one
// of the release-staging objects (the downloaded archive and the extracted
// release executable). Anything else is unexpected and must be retained.
func recognizedStagingNames(name string) bool {
	switch {
	case name == backupName, name == stagedName, name == stagingRecordName:
		return true
	case name == releaseArchiveName, name == releaseCandidateName:
		return true
	case len(name) > len(restorePrefix) && name[:len(restorePrefix)] == restorePrefix:
		return true
	default:
		return false
	}
}

// validatedOwnedFilePaths lists the recognized object paths of txDir,
// validating each present object: regular file, no symlink, no hard links,
// owned by the current effective user. A missing object is skipped
// (idempotent success after partial cleanup). The returned error carries the
// first validation failure; on failure nothing has been removed.
func validatedOwnedFilePaths(txDir string) ([]string, error) {
	entries, err := os.ReadDir(txDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read transaction dir %s: %w", txDir, err)
	}
	var paths []string
	for _, e := range entries {
		if !recognizedStagingNames(e.Name()) {
			continue
		}
		path := filepath.Join(txDir, e.Name())
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("lstat %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing unsafe cleanup: %s is a symlink", path)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing unsafe cleanup: %s is not a regular file", path)
		}
		if id := identityFromInfo(info); id.UID != os.Geteuid() {
			return nil, fmt.Errorf("refusing unsafe cleanup: %s is owned by uid %d", path, id.UID)
		}
		if st, ok := info.Sys().(*syscallStat); ok && st.Nlink != 1 {
			return nil, fmt.Errorf("refusing unsafe cleanup: %s has %d hard links", path, st.Nlink)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// txDirHasUnexpectedEntries reports whether txDir carries entries that are
// not recognized transaction objects; such entries must be retained, so the
// directory itself is never removed.
func txDirHasUnexpectedEntries(txDir string) (bool, error) {
	entries, err := os.ReadDir(txDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read transaction dir %s: %w", txDir, err)
	}
	for _, e := range entries {
		if !recognizedStagingNames(e.Name()) {
			return true, nil
		}
	}
	return false, nil
}

// cleanupTxDir removes the validated owned objects inside txDir one by one,
// then removes the directory itself only when no unexpected entry remains.
// Never recursive: a directory is never deleted merely because its name
// resembles a transaction, and unexpected entries are always retained.
func cleanupTxDir(txDir string, seams CleanupSeams) error {
	remove := os.Remove
	if seams.Remove != nil {
		remove = seams.Remove
	}
	syncDir := SyncDir
	if seams.SyncDir != nil {
		syncDir = seams.SyncDir
	}
	paths, err := validatedOwnedFilePaths(txDir)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	unexpected, err := txDirHasUnexpectedEntries(txDir)
	if err != nil {
		return err
	}
	if unexpected {
		return fmt.Errorf("transaction dir %s retains unexpected entries; refusing directory removal", txDir)
	}
	// ENOTEMPTY from a racing entry is a retained-entries outcome, not a
	// destructive one: surface it so a later launch retries.
	if err := remove(txDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", txDir, err)
	}
	if err := syncDir(filepath.Dir(txDir)); err != nil {
		return fmt.Errorf("sync lease dir after cleanup: %w", err)
	}
	return nil
}

// CleanupSettledTransaction finishes cleanup for a settled (confirmed,
// rolled back, or abandoned) transaction: only recognized, validated, owned
// objects are removed, and a partially completed cleanup retries
// idempotently — objects already gone are success, so a completed cleanup
// never requires already-deleted backup bytes. A failure changes nothing
// about the settled outcome and is retried on a later launch.
func CleanupSettledTransaction(execPath, txID string, seams CleanupSeams) error {
	if !validTransactionID(txID) {
		return fmt.Errorf("invalid transaction id %q", txID)
	}
	txDir := filepath.Join(LeaseDir(execPath), txDirPrefix+txID)
	if _, err := os.Lstat(txDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A completed cleanup is idempotent success.
			return nil
		}
		return fmt.Errorf("lstat transaction dir %s: %w", txDir, err)
	}
	if err := validateNoSymlinkComponents(txDir); err != nil {
		return err
	}
	return cleanupTxDir(txDir, seams)
}

// ReconcileStaging retries recognized cleanup for abandoned pre-install
// staging: every tx dir under the executable's lease dir whose staging
// record binds this executable and whose transaction is not the live one is
// cleaned with full validation. A dir with no staging record was created
// before its ownership became durable and is left untouched; a record that
// fails validation or binds another executable is left untouched too. The
// lease, lock, ownership-record, receipt, and suppression files are never
// candidates: only tx-* directories are considered.
func ReconcileStaging(execPath, liveTxID string, seams CleanupSeams) error {
	leaseDir := LeaseDir(execPath)
	entries, err := os.ReadDir(leaseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read lease dir %s: %w", leaseDir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || len(name) <= len(txDirPrefix) || name[:len(txDirPrefix)] != txDirPrefix {
			continue
		}
		txDir := filepath.Join(leaseDir, name)
		rec, err := readStagingRecord(txDir)
		if err != nil {
			// No durable ownership record (or an unusable one): never guess.
			continue
		}
		if rec.ExecutablePath != execPath {
			continue
		}
		if liveTxID != "" && rec.TransactionID == liveTxID {
			// Still owned by the live transaction.
			continue
		}
		if err := validateNoSymlinkComponents(txDir); err != nil {
			continue
		}
		if err := cleanupTxDir(txDir, seams); err != nil {
			return fmt.Errorf("clean abandoned staging %s: %w", txDir, err)
		}
	}
	return nil
}

// RecordSettledError persists a sanitized message into a settled receipt's
// Error field without touching outcome, phase, resolution, or attempt
// metadata: a cleanup failure is recorded truthfully while the settled
// outcome stays exactly what it was.
func RecordSettledError(execPath, receiptPath string, r Receipt, errMsg string) error {
	r.Error = SanitizeError(errMsg)
	r.UpdatedAt = time.Now()
	return writeReceiptAtomic(receiptPath, r, r.UpdatedAt)
}
