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

// suppressionSchemaVersion is the on-disk schema of SuppressionState.
const suppressionSchemaVersion = 1

// SuppressedTarget records one exact target version suppressed for an
// executable after an actual rollback. Suppression is separate from any
// transient check or operation status: it survives process restarts, receipt
// cleanup, and later transaction writes, and nothing in this phase can clear
// it — a later coordinator phase requires fresh explicit consent.
type SuppressedTarget struct {
	Version       string    `json:"version"`
	Digest        string    `json:"digest,omitempty"`
	TransactionID string    `json:"transaction_id"`
	SuppressedAt  time.Time `json:"suppressed_at"`
}

// SuppressionState is the durable, per-executable suppression store.
type SuppressionState struct {
	SchemaVersion int                `json:"schema_version"`
	Targets       []SuppressedTarget `json:"targets"`
}

// SuppressionPath returns the per-executable suppression store path. Like
// the lease and receipt, it is keyed by the canonical executable path so
// independent executables in one directory never share suppression state.
func SuppressionPath(execPath string) string {
	return filepath.Join(LeaseDir(execPath), "suppression-"+leaseKey(execPath)+".json")
}

// writeSuppressionState atomically writes the state with owner-only
// permissions (O_EXCL tmp, fsync, rename, dir sync, chmod repair).
func writeSuppressionState(execPath string, st SuppressionState) error {
	if err := ensureLeaseDir(execPath); err != nil {
		return err
	}
	st.SchemaVersion = suppressionSchemaVersion
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal suppression state: %w", err)
	}
	data = append(data, '\n')
	path := SuppressionPath(execPath)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create suppression temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write suppression temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync suppression temp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close suppression temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit suppression: %w", err)
	}
	if err := SyncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync suppression dir: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("repair suppression permissions: %w", err)
	}
	return nil
}

// ReadSuppressionState reads and validates the executable's suppression
// store. An unsafe store (wrong schema, wrong owner or mode, symlinked,
// unreadable) is an error: suppression gates future installation decisions,
// so a corrupted store must surface, never silently reset. A missing store
// is an empty one.
func ReadSuppressionState(execPath string) (SuppressionState, error) {
	path := SuppressionPath(execPath)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SuppressionState{SchemaVersion: suppressionSchemaVersion}, nil
		}
		return SuppressionState{}, fmt.Errorf("stat suppression store: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return SuppressionState{}, fmt.Errorf("suppression store %s is not a regular file", path)
	}
	id := identityFromInfo(info)
	if id.Mode != 0o600 {
		return SuppressionState{}, fmt.Errorf("suppression store %s mode is %o; expected 0600", path, id.Mode)
	}
	if id.UID != os.Geteuid() {
		return SuppressionState{}, fmt.Errorf("suppression store %s is owned by uid %d", path, id.UID)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SuppressionState{}, fmt.Errorf("read suppression store: %w", err)
	}
	var st SuppressionState
	if err := json.Unmarshal(data, &st); err != nil {
		return SuppressionState{}, fmt.Errorf("parse suppression store: %w", err)
	}
	if st.SchemaVersion != suppressionSchemaVersion {
		return SuppressionState{}, fmt.Errorf("suppression store schema version %d is not supported", st.SchemaVersion)
	}
	return st, nil
}

// SuppressTarget durably records exact-version suppression for one target.
// Recording the same version again is idempotent (the earliest suppression
// is kept — the first actual rollback decided it). A newer eligible version
// is never affected: suppression matches the exact version string only.
func SuppressTarget(execPath string, target SuppressedTarget) error {
	if target.Version == "" || !validTransactionID(target.TransactionID) {
		return fmt.Errorf("suppressed target is malformed")
	}
	st, err := ReadSuppressionState(execPath)
	if err != nil {
		return err
	}
	for _, existing := range st.Targets {
		if existing.Version == target.Version {
			return nil
		}
	}
	st.Targets = append(st.Targets, target)
	return writeSuppressionState(execPath, st)
}

// SuppressionLookup is the resolved answer for one version.
type SuppressionLookup struct {
	Suppressed bool
	Target     SuppressedTarget
}

// LookupSuppression reports whether the exact version is suppressed for the
// executable. Only exact version-string equality suppresses: a newer
// eligible version is not suppressed.
func LookupSuppression(execPath, version string) (SuppressionLookup, error) {
	st, err := ReadSuppressionState(execPath)
	if err != nil {
		return SuppressionLookup{}, err
	}
	for _, t := range st.Targets {
		if t.Version == version {
			return SuppressionLookup{Suppressed: true, Target: t}, nil
		}
	}
	return SuppressionLookup{}, nil
}

// SuppressedVersions lists every suppressed exact version, oldest first.
// This is the package contract the later update-check coordinator consumes:
// a suppressed version stays ineligible for automatic selection and manual
// requests alike, and a metadata-check success can never clear it.
func SuppressedVersions(execPath string) ([]SuppressedTarget, error) {
	st, err := ReadSuppressionState(execPath)
	if err != nil {
		return nil, err
	}
	return st.Targets, nil
}

// ReconcileSuppression re-asserts the durable suppression for a rolled-back
// receipt: the outcome already happened, so a failed best-effort suppression
// write at rollback time must never erase it, and the next launch repairs
// the store. Safe to call on every boot; idempotent.
func ReconcileSuppression(execPath string, r Receipt) error {
	if r.Outcome != OutcomeRolledBack {
		return nil
	}
	return SuppressTarget(execPath, SuppressedTarget{
		Version:       r.ToVersion,
		Digest:        r.NewDigest,
		TransactionID: r.TransactionID,
		SuppressedAt:  r.UpdatedAt,
	})
}
