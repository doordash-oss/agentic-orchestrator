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
	"strings"
	"time"
)

// receiptSchemaVersion is the on-disk schema of Receipt.
const receiptSchemaVersion = 1

// Outcome is the durable result of a transaction.
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

// Resolution is the explicit settlement marker that distinguishes a settled
// transaction from an actionable pending one while the outcome vocabulary
// stays pending/confirmed/rolled_back. An abandoned pre-replacement attempt
// keeps Outcome=pending and carries ResolutionAbandonedPreReplacement: it is
// settled (never actionable again) and reports an installation failure.
type Resolution string

const (
	ResolutionNone                    Resolution = ""
	ResolutionAbandonedPreReplacement Resolution = "abandoned_pre_replacement"
)

// RecoveryKind names what a persisted write-ahead recovery attempt intends:
// restoring the previous build's bytes, or re-executing the unchanged build
// after an aborted shutdown.
const (
	RecoveryKindRestore = "restore"
	RecoveryKindRestart = "restart"
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
	// RecoveryAttemptID is the write-ahead identity of an in-flight recovery
	// attempt, persisted before any restoration rename or unchanged-binary
	// re-exec so an interrupted recovery can be reconciled by identity.
	RecoveryAttemptID string `json:"recovery_attempt_id,omitempty"`
	// RecoveryKind is "restore" or "restart" (see RecoveryKind* constants).
	RecoveryKind string `json:"recovery_kind,omitempty"`
	// RecoveryAttemptBy is the pid that recorded the attempt.
	RecoveryAttemptBy int `json:"recovery_attempt_by,omitempty"`
	// RecoveryStartedAt stamps the attempt.
	RecoveryStartedAt time.Time `json:"recovery_started_at,omitempty"`
	// RestorePath and RestoreID identify the separately prepared restore copy
	// for a restore attempt; the installed path's identity matching RestoreID
	// after a crash proves the restoration rename already happened.
	RestorePath string        `json:"restore_path,omitempty"`
	RestoreID   *FileIdentity `json:"restore_id,omitempty"`
	// Resolution settles an abandoned transaction (see Resolution*).
	Resolution Resolution `json:"resolution,omitempty"`
	// ResolvedAt stamps the settlement.
	ResolvedAt time.Time `json:"resolved_at,omitempty"`
	// InstallFailed marks that the update attempt failed before the candidate
	// ever became the installed build: an installation failure, never a
	// binary rollback, and never a reason to suppress the target.
	InstallFailed bool `json:"install_failed,omitempty"`
}

// Settled reports whether the transaction is durably settled and therefore
// not actionable: confirmed, rolled back, or abandoned through an explicit
// resolution marker.
func (r Receipt) Settled() bool {
	if r.Outcome != OutcomePending {
		return true
	}
	return r.Resolution != ResolutionNone
}

// ActionablePending reports whether the receipt is a pending, unresolved
// transaction that some launch must resolve.
func (r Receipt) ActionablePending() bool {
	return r.Outcome == OutcomePending && r.Resolution == ResolutionNone
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
	return writeFileAtomic(path, data, true)
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

// WriteReceiptDurable durably writes r to path with the same atomic
// guarantees as every internal receipt write (tmp + fsync + rename + dir
// sync). It exists so a deliberate driver build can wrap the real write in
// failure seams and deterministic barriers; production code never needs it.
func WriteReceiptDurable(path string, r Receipt) error {
	return writeReceiptAtomic(path, r, r.UpdatedAt)
}
