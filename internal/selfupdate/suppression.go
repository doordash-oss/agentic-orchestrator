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

// suppressionSchemaVersion is the on-disk schema of suppressionState.
const suppressionSchemaVersion = 1

// suppressedTarget records one exact target version suppressed for an
// executable after an actual rollback. Suppression is separate from any
// transient check or operation status: it survives process restarts, receipt
// cleanup, and later transaction writes, and nothing in this phase can clear
// it — a later coordinator phase requires fresh explicit consent.
type suppressedTarget struct {
	Version       string    `json:"version"`
	Digest        string    `json:"digest,omitempty"`
	TransactionID string    `json:"transaction_id"`
	SuppressedAt  time.Time `json:"suppressed_at"`
}

// suppressionState is the durable, per-executable suppression store.
type suppressionState struct {
	SchemaVersion int                `json:"schema_version"`
	Targets       []suppressedTarget `json:"targets"`
}

// suppressionPath returns the per-executable suppression store path. Like
// the lease and receipt, it is keyed by the canonical executable path so
// independent executables in one directory never share suppression state.
func suppressionPath(execPath string) string {
	return filepath.Join(leaseDir(execPath), "suppression-"+leaseKey(execPath)+".json")
}

// writeSuppressionState durably writes the state with owner-only
// permissions (O_EXCL tmp, fsync, rename, dir sync, chmod repair).
func writeSuppressionState(execPath string, st suppressionState) error {
	if err := ensureLeaseDir(execPath); err != nil {
		return err
	}
	st.SchemaVersion = suppressionSchemaVersion
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal suppression state: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(suppressionPath(execPath), data, true)
}

// readSuppressionState reads and validates the executable's suppression
// store. An unsafe store (wrong schema, wrong owner or mode, symlinked,
// unreadable) is an error: suppression gates future installation decisions,
// so a corrupted store must surface, never silently reset. A missing store
// is an empty one.
func readSuppressionState(execPath string) (suppressionState, error) {
	path := suppressionPath(execPath)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return suppressionState{SchemaVersion: suppressionSchemaVersion}, nil
		}
		return suppressionState{}, fmt.Errorf("stat suppression store: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return suppressionState{}, fmt.Errorf("suppression store %s is not a regular file", path)
	}
	id := identityFromInfo(info)
	if id.Mode != 0o600 {
		return suppressionState{}, fmt.Errorf("suppression store %s mode is %o; expected 0600", path, id.Mode)
	}
	if id.UID != os.Geteuid() {
		return suppressionState{}, fmt.Errorf("suppression store %s is owned by uid %d", path, id.UID)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return suppressionState{}, fmt.Errorf("read suppression store: %w", err)
	}
	var st suppressionState
	if err := json.Unmarshal(data, &st); err != nil {
		return suppressionState{}, fmt.Errorf("parse suppression store: %w", err)
	}
	if st.SchemaVersion != suppressionSchemaVersion {
		return suppressionState{}, fmt.Errorf("suppression store schema version %d is not supported", st.SchemaVersion)
	}
	return st, nil
}

// suppressTarget durably records exact-version suppression for one target.
// Recording the same version again is idempotent (the earliest suppression
// is kept — the first actual rollback decided it). A newer eligible version
// is never affected: suppression matches the exact version string only.
func suppressTarget(execPath string, target suppressedTarget) error {
	if target.Version == "" || !validTransactionID(target.TransactionID) {
		return fmt.Errorf("suppressed target is malformed")
	}
	st, err := readSuppressionState(execPath)
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
	Target     suppressedTarget
}

// LookupSuppression reports whether the exact version is suppressed for the
// executable. Only exact version-string equality suppresses: a newer
// eligible version is not suppressed.
func LookupSuppression(execPath, version string) (SuppressionLookup, error) {
	st, err := readSuppressionState(execPath)
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

// ReconcileSuppression re-asserts the durable suppression for a rolled-back
// receipt: the outcome already happened, so a failed best-effort suppression
// write at rollback time must never erase it, and the next launch repairs
// the store. Safe to call on every boot; idempotent.
func ReconcileSuppression(execPath string, r Receipt) error {
	if r.Outcome != OutcomeRolledBack {
		return nil
	}
	return suppressTarget(execPath, suppressedTarget{
		Version:       r.ToVersion,
		Digest:        r.NewDigest,
		TransactionID: r.TransactionID,
		SuppressedAt:  r.UpdatedAt,
	})
}
