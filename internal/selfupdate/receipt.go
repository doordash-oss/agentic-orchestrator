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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// receiptSchemaVersion is the on-disk schema of Receipt.
const receiptSchemaVersion = 1

// Outcome is the durable result of a transaction. Only pending and confirmed
// are produced in this slice; rolled_back is vocabulary for a later phase and
// must never be reported without an actually performed rollback.
type Outcome string

const (
	OutcomePending    Outcome = "pending"
	OutcomeConfirmed  Outcome = "confirmed"
	OutcomeRolledBack Outcome = "rolled_back"
)

// Phase marks how far a transaction progressed. Phase advances only after the
// corresponding mutation is durable; Outcome is the terminal verdict.
type Phase string

const (
	PhaseBackupReady         Phase = "backup-ready"
	PhaseReplacementComplete Phase = "replacement-complete"
	PhaseRollbackAttempted   Phase = "rollback-attempted"
)

// BindEndpoint records the effective bind semantics so the replacement image
// can rebind the same endpoint without rewriting launch arguments.
type BindEndpoint struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Wildcard     bool   `json:"wildcard"`
	AdvertiseURL string `json:"advertise_url"`
	Policy       string `json:"policy"`
}

// Receipt is the write-ahead record of one update transaction. It never
// contains bearer tokens or the launch environment.
type Receipt struct {
	SchemaVersion  int          `json:"schema_version"`
	TransactionID  string       `json:"transaction_id"`
	RuntimeDir     string       `json:"runtime_dir"`
	StateDir       string       `json:"state_dir"`
	Config         string       `json:"config_path"`
	PID            int          `json:"pid"`
	PGID           int          `json:"pgid,omitempty"`
	FromVersion    string       `json:"from_version"`
	ToVersion      string       `json:"to_version"`
	ExecutablePath string       `json:"executable_path"`
	ExecutableID   FileIdentity `json:"executable_id"`
	OriginalMode   uint32       `json:"original_mode"`
	OriginalUID    int          `json:"original_uid"`
	OriginalGID    int          `json:"original_gid"`
	OldDigest      string       `json:"old_digest"`
	NewDigest      string       `json:"new_digest"`
	BackupPath     string       `json:"backup_path"`
	BackupID       FileIdentity `json:"backup_id"`
	StagingPath    string       `json:"staging_path"`
	StagingID      FileIdentity `json:"staging_id"`
	Bind           BindEndpoint `json:"bind_endpoint"`
	StartedAt      time.Time    `json:"started_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	Error          string       `json:"error,omitempty"`
	Outcome        Outcome      `json:"outcome"`
	Phase          Phase        `json:"phase"`
}

// maxErrorMessage bounds the sanitized error persisted in a receipt.
const maxErrorMessage = 1024

// SanitizeError trims and redacts an error message before it is persisted:
// every provided secret substring becomes "[REDACTED]" and the result is
// truncated to 1KB.
func SanitizeError(msg string, secrets ...string) string {
	msg = strings.TrimSpace(msg)
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		msg = strings.ReplaceAll(msg, secret, "[REDACTED]")
	}
	if len(msg) > maxErrorMessage {
		msg = msg[:maxErrorMessage]
	}
	return msg
}

// writeReceiptAtomically writes r to path with owner-only permissions. The
// tmp file is fsynced before the rename so the receipt on disk is never a
// partial write, and the parent directory is fsynced so phase/outcome
// updates are durable before callers rely on them. Overwrites are expected
// on phase and outcome updates.
func writeReceiptAtomic(path string, r Receipt, now time.Time) error {
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal receipt: %w", err)
	}
	data = append(data, '\n')

	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create receipt temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write receipt temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync receipt temp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close receipt temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit receipt: %w", err)
	}
	if err := SyncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync receipt dir: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("repair receipt permissions: %w", err)
	}
	return nil
}

// ReadReceipt reads and parses the receipt at path.
func ReadReceipt(path string) (Receipt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Receipt{}, fmt.Errorf("read receipt: %w", err)
	}
	var r Receipt
	if err := json.Unmarshal(data, &r); err != nil {
		return Receipt{}, fmt.Errorf("parse receipt: %w", err)
	}
	return r, nil
}
