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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleReceipt(now time.Time) Receipt {
	return Receipt{
		SchemaVersion:  receiptSchemaVersion,
		TransactionID:  "0123456789abcdef0123456789abcdef",
		RuntimeDir:     "/runtime",
		StateDir:       "/runtime/features",
		Config:         "/runtime/config.yaml",
		PID:            4242,
		PGID:           4242,
		FromVersion:    "1.0.0",
		ToVersion:      "2.0.0",
		ExecutablePath: "/runtime/bin/agentico",
		ExecutableID: FileIdentity{
			Dev: 1, Ino: 2, Mode: 0o755, UID: 501, GID: 20, Size: 128,
		},
		OriginalMode: 0o755,
		OriginalUID:  501,
		OriginalGID:  20,
		OldDigest:    "aaaa",
		NewDigest:    "bbbb",
		BackupPath:   "/runtime/bin/.agentico-selfupdate/tx-x/backup",
		BackupID:     FileIdentity{Dev: 1, Ino: 3, Mode: 0o600, UID: 501, GID: 20, Size: 128},
		StagingPath:  "/runtime/bin/.agentico-selfupdate/tx-x/staged",
		StagingID:    FileIdentity{Dev: 1, Ino: 4, Mode: 0o755, UID: 501, GID: 20, Size: 256},
		Bind: BindEndpoint{
			Host:         "127.0.0.1",
			Port:         54321,
			Wildcard:     false,
			AdvertiseURL: "http://127.0.0.1:54321",
			Policy:       "loopback",
		},
		StartedAt: now.Add(-time.Minute),
		UpdatedAt: now,
		Error:     "",
		Outcome:   OutcomePending,
		Phase:     PhaseBackupReady,
	}
}

func assertReceiptEqual(t *testing.T, got, want Receipt) {
	t.Helper()
	if got.SchemaVersion != want.SchemaVersion ||
		got.TransactionID != want.TransactionID ||
		got.RuntimeDir != want.RuntimeDir ||
		got.StateDir != want.StateDir ||
		got.Config != want.Config ||
		got.PID != want.PID ||
		got.PGID != want.PGID ||
		got.FromVersion != want.FromVersion ||
		got.ToVersion != want.ToVersion ||
		got.ExecutablePath != want.ExecutablePath ||
		got.ExecutableID != want.ExecutableID ||
		got.OriginalMode != want.OriginalMode ||
		got.OriginalUID != want.OriginalUID ||
		got.OriginalGID != want.OriginalGID ||
		got.OldDigest != want.OldDigest ||
		got.NewDigest != want.NewDigest ||
		got.BackupPath != want.BackupPath ||
		got.BackupID != want.BackupID ||
		got.StagingPath != want.StagingPath ||
		got.StagingID != want.StagingID ||
		got.Bind != want.Bind ||
		got.Error != want.Error ||
		got.Outcome != want.Outcome ||
		got.Phase != want.Phase {
		t.Fatalf("receipt mismatch:\ngot  %+v\nwant %+v", got, want)
	}
	if !got.StartedAt.Equal(want.StartedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("timestamps mismatch: got %s/%s want %s/%s", got.StartedAt, got.UpdatedAt, want.StartedAt, want.UpdatedAt)
	}
}

func TestWriteReceiptAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "receipt.json")
	now := time.Now()
	r := sampleReceipt(now)

	if err := writeReceiptAtomic(path, r, now); err != nil {
		t.Fatalf("writeReceiptAtomic: %v", err)
	}
	got, err := ReadReceipt(path)
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	assertReceiptEqual(t, got, r)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat receipt: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %o, want 600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("receipt dir must contain only the receipt, got %v (%v)", entries, err)
	}

	// Phase/outcome updates overwrite in place and still leave one file.
	updated := r
	updated.Phase = PhaseReplacementComplete
	updated.Outcome = OutcomeConfirmed
	if err := writeReceiptAtomic(path, updated, now); err != nil {
		t.Fatalf("writeReceiptAtomic update: %v", err)
	}
	got, err = ReadReceipt(path)
	if err != nil {
		t.Fatalf("ReadReceipt update: %v", err)
	}
	if got.Phase != PhaseReplacementComplete || got.Outcome != OutcomeConfirmed {
		t.Fatalf("updated receipt = %s/%s, want replacement-complete/confirmed", got.Phase, got.Outcome)
	}
	entries, err = os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("receipt dir must contain only the receipt after update, got %v (%v)", entries, err)
	}
}

func TestReadReceiptMissing(t *testing.T) {
	if _, err := ReadReceipt(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("ReadReceipt on missing file: expected error")
	}
}

func TestOutcomeAndPhaseConstantStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		got  string
		want string
	}{
		{string(OutcomePending), "pending"},
		{string(OutcomeConfirmed), "confirmed"},
		{string(OutcomeRolledBack), "rolled_back"},
		{string(PhaseBackupReady), "backup-ready"},
		{string(PhaseReplacementComplete), "replacement-complete"},
		{string(PhaseRollbackAttempted), "rollback-attempted"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("constant = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestSanitizeError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		msg     string
		secrets []string
		want    string
	}{
		{"trims", "  boom  ", nil, "boom"},
		{"redacts secret", "failed with token abc123", []string{"abc123"}, "failed with token [REDACTED]"},
		{"redacts all occurrences", "abc and abc", []string{"abc"}, "[REDACTED] and [REDACTED]"},
		{"ignores empty secret", "nothing", []string{"", "   "}, "nothing"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SanitizeError(tc.msg, tc.secrets...); got != tc.want {
				t.Fatalf("SanitizeError = %q, want %q", got, tc.want)
			}
		})
	}
	if got := SanitizeError(strings.Repeat("a", 2048)); len(got) != maxErrorMessage {
		t.Fatalf("SanitizeError length = %d, want %d", len(got), maxErrorMessage)
	}
}

func TestWriteReceiptDurableRecoveryFieldsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	now := time.Now().UTC()
	r := sampleReceipt(now)
	r.RecoveryAttemptID = "abcdef1234567890abcdef1234567890"
	r.RecoveryKind = RecoveryKindRestore
	r.RecoveryAttemptBy = 4242
	r.RecoveryStartedAt = now.Add(-time.Minute)
	r.RestorePath = "/runtime/bin/.agentico-selfupdate/tx-x/restore-abc"
	restoreID := FileIdentity{Dev: 9, Ino: 10, Mode: 0o755, UID: 501, GID: 20, Size: 128}
	r.RestoreID = &restoreID
	r.Resolution = ResolutionAbandonedPreReplacement
	r.ResolvedAt = now
	r.InstallFailed = true

	if err := WriteReceiptDurable(path, r); err != nil {
		t.Fatalf("WriteReceiptDurable: %v", err)
	}
	got, err := ReadReceipt(path)
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	assertReceiptEqual(t, got, r)
	if got.RecoveryAttemptID != r.RecoveryAttemptID || got.RecoveryKind != r.RecoveryKind || got.RecoveryAttemptBy != r.RecoveryAttemptBy {
		t.Fatalf("attempt metadata round trip: %+v", got)
	}
	if !got.RecoveryStartedAt.Equal(r.RecoveryStartedAt) || !got.ResolvedAt.Equal(r.ResolvedAt) {
		t.Fatalf("attempt timestamps = %s/%s, want %s/%s", got.RecoveryStartedAt, got.ResolvedAt, r.RecoveryStartedAt, r.ResolvedAt)
	}
	if got.RestorePath != r.RestorePath || got.RestoreID == nil || *got.RestoreID != restoreID {
		t.Fatalf("restore copy identity round trip: %s %+v", got.RestorePath, got.RestoreID)
	}
	if got.Resolution != r.Resolution || got.InstallFailed != r.InstallFailed {
		t.Fatalf("settlement round trip: %q %v", got.Resolution, got.InstallFailed)
	}
	if !got.Settled() || got.ActionablePending() {
		t.Fatalf("pending+resolution receipt must be settled and not actionable")
	}
}

func TestReceiptPhaseOneRecordUnmarshalsAdditively(t *testing.T) {
	// A Phase 1-shaped record: none of the recovery fields exist on disk.
	phase1 := `{
  "schema_version": 1,
  "transaction_id": "0123456789abcdef0123456789abcdef",
  "runtime_dir": "/runtime",
  "executable_path": "/runtime/bin/agentico",
  "old_digest": "aaaa",
  "new_digest": "bbbb",
  "original_mode": 493,
  "outcome": "pending",
  "phase": "backup-ready"
}`
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, []byte(phase1), 0o600); err != nil {
		t.Fatalf("write phase 1 receipt: %v", err)
	}
	r, err := ReadReceipt(path)
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if r.SchemaVersion != receiptSchemaVersion || r.Outcome != OutcomePending || r.Phase != PhaseBackupReady {
		t.Fatalf("phase 1 receipt = %d %s/%s", r.SchemaVersion, r.Outcome, r.Phase)
	}
	if r.RecoveryAttemptID != "" || r.RecoveryKind != "" || r.RecoveryAttemptBy != 0 || r.RestorePath != "" || r.RestoreID != nil || r.Resolution != ResolutionNone || r.InstallFailed {
		t.Fatalf("recovery fields must unmarshal to zero values: %+v", r)
	}
	if !r.RecoveryStartedAt.IsZero() || !r.ResolvedAt.IsZero() {
		t.Fatalf("recovery timestamps must be zero: %s %s", r.RecoveryStartedAt, r.ResolvedAt)
	}
	if !r.ActionablePending() || r.Settled() {
		t.Fatal("phase 1 pending record must be actionable pending")
	}
}

func TestReceiptSettledAndActionablePendingMatrix(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name       string
		mutate     func(r *Receipt)
		settled    bool
		actionable bool
	}{
		{"pending unresolved", func(r *Receipt) {}, false, true},
		{"pending abandoned", func(r *Receipt) { r.Resolution = ResolutionAbandonedPreReplacement }, true, false},
		{"confirmed", func(r *Receipt) { r.Outcome = OutcomeConfirmed }, true, false},
		{"rolled back", func(r *Receipt) { r.Outcome = OutcomeRolledBack }, true, false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := sampleReceipt(now)
			tc.mutate(&r)
			if r.Settled() != tc.settled {
				t.Fatalf("Settled() = %v, want %v", r.Settled(), tc.settled)
			}
			if r.ActionablePending() != tc.actionable {
				t.Fatalf("ActionablePending() = %v, want %v", r.ActionablePending(), tc.actionable)
			}
		})
	}
}
