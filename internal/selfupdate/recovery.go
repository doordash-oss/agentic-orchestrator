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
	"os"
	"path/filepath"
	"syscall"
)

// syscallStat aliases the platform stat structure for link-count checks.
type syscallStat = syscall.Stat_t

// RecoveryAction is the mandatory recovery decision for one launch.
type RecoveryAction string

const (
	// RecoveryActionNone: no transaction, or a durably settled one. Ordinary
	// startup proceeds, including serving without update ownership.
	RecoveryActionNone RecoveryAction = "none"
	// RecoveryActionAbandon: the original identity and bytes are still
	// installed and no restoration is recorded — the attempt provably never
	// replaced the executable. Settle as an installation failure, clean only
	// validated owned objects, and continue on the unchanged build.
	RecoveryActionAbandon RecoveryAction = "abandon"
	// RecoveryActionRestore: the candidate bytes are installed. Restore the
	// verified previous bytes and re-exec the installed path.
	RecoveryActionRestore RecoveryAction = "restore"
	// RecoveryActionFinishRollback: a prior restore attempt's rename already
	// landed (installed identity matches the recorded RestoreID); only the
	// rolled_back bookkeeping remains, then the installed path is executed.
	RecoveryActionFinishRollback RecoveryAction = "finish-rollback"
)

// RecoveryPlan is the validated decision for one launch, made before any
// bootstrap step and before any mutation.
type RecoveryPlan struct {
	Action      RecoveryAction
	Receipt     Receipt
	ReceiptPath string
}

// UnsafeRecoveryError reports an unsafe pending recovery record. The launch
// must refuse nonzero with this sanitized diagnostic, preserve every
// recovery input, and never follow record-supplied paths, repair
// permissions, or overwrite an externally replaced executable.
type UnsafeRecoveryError struct {
	Diagnostic string
}

func (e *UnsafeRecoveryError) Error() string { return e.Diagnostic }

// IsUnsafeRecovery reports whether err (or anything it wraps) is an unsafe
// recovery refusal.
func IsUnsafeRecovery(err error) bool {
	var unsafe *UnsafeRecoveryError
	return errors.As(err, &unsafe)
}

func refuse(format string, args ...interface{}) error {
	return &UnsafeRecoveryError{Diagnostic: fmt.Sprintf(format, args...)}
}

// validateReceiptSemantics checks the supported schema and the legal
// phase/outcome/resolution combinations before anything else: unsupported or
// self-contradictory records are unsafe and refuse startup regardless of
// settlement.
func validateReceiptSemantics(r Receipt) error {
	if r.SchemaVersion != receiptSchemaVersion {
		return refuse("receipt schema version %d is not supported (supported: %d)", r.SchemaVersion, receiptSchemaVersion)
	}
	if !validTransactionID(r.TransactionID) {
		return refuse("receipt transaction id %q is malformed", r.TransactionID)
	}
	switch r.Outcome {
	case OutcomeConfirmed:
		if r.Phase != PhaseReplacementComplete {
			return refuse("confirmed receipt carries illegal phase %q", r.Phase)
		}
		if r.Resolution != ResolutionNone {
			return refuse("confirmed receipt carries illegal resolution %q", r.Resolution)
		}
	case OutcomeRolledBack:
		if r.Phase != PhaseRollbackAttempted {
			return refuse("rolled-back receipt carries illegal phase %q", r.Phase)
		}
	case OutcomePending:
		switch r.Phase {
		case PhaseBackupReady, PhaseReplacementComplete:
		default:
			return refuse("pending receipt carries illegal phase %q", r.Phase)
		}
	default:
		return refuse("receipt carries unsupported outcome %q", r.Outcome)
	}
	if r.Phase == PhaseRollbackAttempted && r.Outcome != OutcomeRolledBack {
		return refuse("phase %q requires outcome %q", PhaseRollbackAttempted, OutcomeRolledBack)
	}
	if r.OldDigest == "" || r.NewDigest == "" || r.OldDigest == r.NewDigest {
		return refuse("receipt digests are missing or identical")
	}
	if r.OriginalMode == 0 || r.OriginalMode&0o111 == 0 {
		return refuse("receipt original mode %o is not an executable mode", r.OriginalMode)
	}
	return nil
}

// validateOwnerOnlyRegularFile asserts path is a regular file (never a
// symlink or any substituted link target), owned by the current effective
// user, with exactly the wanted permission bits. Validation is tied to the
// actual object via lstat so a swapped-in symlink can never pass as the
// record's object.
func validateOwnerOnlyRegularFile(path string, mode uint32, requireNLinkOne bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return refuse("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return refuse("%s is a symlink; refusing to follow record-supplied links", path)
	}
	if !info.Mode().IsRegular() {
		return refuse("%s is not a regular file", path)
	}
	id := identityFromInfo(info)
	if id.Mode != mode {
		return refuse("%s mode is %o; expected owner-only %o", path, id.Mode, mode)
	}
	if id.UID != os.Geteuid() {
		return refuse("%s is owned by uid %d; expected current effective uid %d", path, id.UID, os.Geteuid())
	}
	if requireNLinkOne && info.Sys() != nil {
		if st, ok := info.Sys().(*syscallStat); ok && st.Nlink != 1 {
			return refuse("%s has %d hard links; refusing a substituted object", path, st.Nlink)
		}
	}
	return nil
}

// validateNoSymlinkComponents asserts that no component of path — from the
// root down to the final element — is a symlink, so substitution between
// check and mutation cannot redirect recovery through a rewritten parent.
func validateNoSymlinkComponents(path string) error {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return refuse("recovery path %s is not absolute", path)
	}
	cur := string(filepath.Separator)
	components := splitPathComponents(cleaned)
	for _, comp := range components {
		cur = filepath.Join(cur, comp)
		info, err := os.Lstat(cur)
		if err != nil {
			return refuse("lstat component %s: %v", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return refuse("component %s of %s is a symlink", cur, path)
		}
	}
	return nil
}

// splitPathComponents splits an absolute cleaned path into its components.
func splitPathComponents(path string) []string {
	var out []string
	for {
		dir, base := filepath.Split(path)
		if base != "" {
			out = append([]string{base}, out...)
		}
		if dir == string(filepath.Separator) || dir == "" {
			return out
		}
		path = filepath.Clean(dir)
		if path == string(filepath.Separator) {
			return out
		}
	}
}

// validateRecoveryStorage validates the lease directory, the receipt file,
// and the transaction layout named by the receipt: canonical containment
// under the executable's lease dir, the exact tx-<id>/backup|staged names,
// and owner-only storage. Nothing is repaired and no record-supplied path is
// followed; every mismatch refuses.
func validateRecoveryStorage(execPath string, r Receipt) error {
	if err := validateNoSymlinkComponents(LeaseDir(execPath)); err != nil {
		return err
	}
	if err := validateOwnerOnlyDirectory(LeaseDir(execPath), 0o700); err != nil {
		return err
	}
	if err := validateOwnerOnlyRegularFile(ReceiptPath(execPath), 0o600, false); err != nil {
		return err
	}
	txDir := filepath.Join(LeaseDir(execPath), txDirPrefix+r.TransactionID)
	if r.BackupPath != filepath.Join(txDir, backupName) {
		return refuse("receipt backup path %s is outside the transaction dir %s", r.BackupPath, txDir)
	}
	if r.StagingPath != filepath.Join(txDir, stagedName) {
		return refuse("receipt staging path %s is outside the transaction dir %s", r.StagingPath, txDir)
	}
	if r.RestorePath != "" && filepath.Dir(r.RestorePath) != txDir {
		return refuse("receipt restore path %s is outside the transaction dir %s", r.RestorePath, txDir)
	}
	return nil
}

// validateOwnerOnlyDirectory asserts path is a real directory (not a
// symlink) owned by the current effective user with exactly the wanted mode.
func validateOwnerOnlyDirectory(path string, mode uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return refuse("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return refuse("%s is a symlink; refusing to follow record-supplied links", path)
	}
	if !info.IsDir() {
		return refuse("%s is not a directory", path)
	}
	id := identityFromInfo(info)
	if id.Mode != mode {
		return refuse("%s mode is %o; expected %o", path, id.Mode, mode)
	}
	if id.UID != os.Geteuid() {
		return refuse("%s is owned by uid %d; expected current effective uid %d", path, id.UID, os.Geteuid())
	}
	return nil
}

// InspectRecovery validates the installation's durable transaction state and
// decides the mandatory recovery action for this launch. It performs no
// mutation and repairs nothing. A nil error with RecoveryActionNone means
// ordinary startup proceeds. A non-nil error is an unsafe-record refusal:
// the caller must exit nonzero with the sanitized diagnostic and leave every
// input untouched. Stale PID metadata is never consulted: the executable's
// actual identity and bytes, the durable phase, and (for liveness) the lease
// flock are the only authorities.
func InspectRecovery(exec Executable, runtimeDir string) (RecoveryPlan, error) {
	r, found, err := ReadLatestReceipt(exec.Path)
	if err != nil {
		return RecoveryPlan{}, err
	}
	if !found {
		// No transaction: ordinary startup, including serving without update
		// ownership.
		return RecoveryPlan{Action: RecoveryActionNone}, nil
	}
	if err := validateReceiptSemantics(r); err != nil {
		return RecoveryPlan{}, err
	}
	if r.Settled() {
		return RecoveryPlan{Action: RecoveryActionNone, Receipt: r, ReceiptPath: ReceiptPath(exec.Path)}, nil
	}

	// Installation, runtime, and transaction membership: a different runtime
	// can never take unresolved recovery ownership of this executable's
	// transaction, and the record must bind to this exact executable.
	if r.RuntimeDir != runtimeDir {
		return RecoveryPlan{}, refuse("pending transaction %s belongs to runtime %s; runtime %s cannot take unresolved recovery ownership", r.TransactionID, r.RuntimeDir, runtimeDir)
	}
	if err := validateRecoveryStorage(exec.Path, r); err != nil {
		return RecoveryPlan{}, err
	}

	installedID, err := StatFile(exec.Path)
	if err != nil {
		return RecoveryPlan{}, refuse("stat installed executable %s: %v", exec.Path, err)
	}
	digest, err := DigestFile(exec.Path)
	if err != nil {
		return RecoveryPlan{}, refuse("digest installed executable %s: %v", exec.Path, err)
	}
	plan := RecoveryPlan{Receipt: r, ReceiptPath: ReceiptPath(exec.Path)}

	if r.RecoveryAttemptID != "" {
		switch r.RecoveryKind {
		case RecoveryKindRestore:
			switch {
			case r.RestoreID != nil && installedID.SameFile(*r.RestoreID) && digest == r.OldDigest:
				plan.Action = RecoveryActionFinishRollback
				return plan, nil
			case digest == r.NewDigest && installedID.SameFile(r.StagingID):
				plan.Action = RecoveryActionRestore
				return plan, validateBackupForRestore(exec.Path, r)
			default:
				return RecoveryPlan{}, refuse("installed executable %s matches neither the interrupted restore copy nor the recorded candidate", exec.Path)
			}
		case RecoveryKindRestart:
			if installedID.SameFile(r.ExecutableID) && digest == r.OldDigest {
				plan.Action = RecoveryActionAbandon
				return plan, nil
			}
			return RecoveryPlan{}, refuse("restart attempt recorded but installed executable %s no longer matches the original identity", exec.Path)
		default:
			return RecoveryPlan{}, refuse("receipt carries unsupported recovery kind %q", r.RecoveryKind)
		}
	}

	switch r.Phase {
	case PhaseBackupReady:
		switch {
		case installedID.SameFile(r.ExecutableID) && digest == r.OldDigest:
			plan.Action = RecoveryActionAbandon
			return plan, nil
		case digest == r.NewDigest && installedID.SameFile(r.StagingID):
			// The rename landed but the receipt never advanced (crash inside
			// Commit): candidate bytes installed still require restoration.
			plan.Action = RecoveryActionRestore
			return plan, validateBackupForRestore(exec.Path, r)
		default:
			return RecoveryPlan{}, refuse("installed executable %s matches neither the original nor the staged candidate identity", exec.Path)
		}
	case PhaseReplacementComplete:
		if digest == r.NewDigest && installedID.SameFile(r.StagingID) {
			plan.Action = RecoveryActionRestore
			return plan, validateBackupForRestore(exec.Path, r)
		}
		// Old bytes back at the installed path without a recorded restore
		// attempt — including a matching-digest file on an unexpected inode —
		// cannot authorize anything: refuse.
		return RecoveryPlan{}, refuse("receipt is replacement-complete but installed executable %s does not match the staged candidate identity", exec.Path)
	default:
		return RecoveryPlan{}, refuse("pending receipt carries illegal phase %q", r.Phase)
	}
}

// validateBackupForRestore asserts the rollback backup is present and is the
// exact recorded object: identity, digest, owner-only mode, no links. A
// missing backup on a restore path is a recovery error, never a quiet
// success.
// lstatOwnedRegular resolves path without following symlinks and requires a
// singly linked regular file: the no-follow contract every restore source
// shares.
func lstatOwnedRegular(path string) (FileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return FileIdentity{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return FileIdentity{}, fmt.Errorf("%s is not a regular file", path)
	}
	if st, ok := info.Sys().(*syscallStat); ok && st.Nlink != 1 {
		return FileIdentity{}, fmt.Errorf("%s has %d hard links", path, st.Nlink)
	}
	return identityFromInfo(info), nil
}

func validateBackupForRestore(execPath string, r Receipt) error {
	info, err := os.Lstat(r.BackupPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return refuse("rollback backup %s is missing; restoration cannot proceed", r.BackupPath)
		}
		return refuse("lstat rollback backup %s: %v", r.BackupPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return refuse("rollback backup %s is not a regular file", r.BackupPath)
	}
	id := identityFromInfo(info)
	if !id.SameFile(r.BackupID) {
		return refuse("rollback backup %s does not match the recorded identity", r.BackupPath)
	}
	if id.Mode != 0o600 {
		return refuse("rollback backup %s mode is %o; expected 0600", r.BackupPath, id.Mode)
	}
	if id.UID != os.Geteuid() {
		return refuse("rollback backup %s is owned by uid %d; expected current effective uid %d", r.BackupPath, id.UID, os.Geteuid())
	}
	if st, ok := info.Sys().(*syscallStat); ok && st.Nlink != 1 {
		return refuse("rollback backup %s has %d hard links", r.BackupPath, st.Nlink)
	}
	digest, err := DigestFile(r.BackupPath)
	if err != nil {
		return refuse("digest rollback backup %s: %v", r.BackupPath, err)
	}
	if digest != r.OldDigest {
		return refuse("rollback backup %s digest does not match the recorded original digest", r.BackupPath)
	}
	return nil
}

// validateRecoveryLease asserts the caller holds continuous binary ownership
// for the executable and runtime named by the receipt before any mutation.
func validateRecoveryLease(execPath string, lease *Lease, r Receipt) error {
	if lease == nil {
		return refuse("recovery mutation requires continuous binary ownership")
	}
	if lease.Record().ExecutablePath != execPath {
		return refuse("held lease binds executable %s; recovery targets %s", lease.Record().ExecutablePath, execPath)
	}
	if lease.Record().RuntimeDir != r.RuntimeDir {
		return refuse("held lease binds runtime %s; pending transaction belongs to %s", lease.Record().RuntimeDir, r.RuntimeDir)
	}
	return nil
}

// reacquireDecision re-reads the latest receipt after the lease is held and
// asserts the plan still applies: re-validation is tied to the actual
// objects used, so substitution between inspection and mutation cannot
// redirect recovery.
func reacquireDecision(exec Executable, plan RecoveryPlan) (Receipt, error) {
	r, found, err := ReadLatestReceipt(exec.Path)
	if err != nil || !found {
		if err == nil {
			err = refuse("pending transaction %s disappeared before recovery mutation", plan.Receipt.TransactionID)
		}
		return Receipt{}, err
	}
	if r.TransactionID != plan.Receipt.TransactionID || !r.ActionablePending() {
		return Receipt{}, refuse("transaction %s changed state before recovery mutation", plan.Receipt.TransactionID)
	}
	if err := validateReceiptSemantics(r); err != nil {
		return Receipt{}, err
	}
	if err := validateRecoveryStorage(exec.Path, r); err != nil {
		return Receipt{}, err
	}
	return r, nil
}

// newRecoveryAttemptID mints a write-ahead recovery attempt identity.
func newRecoveryAttemptID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate recovery attempt id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// ResolveAbandoned durably settles a validated pre-replacement abandonment:
// the original identity and bytes are still installed and no restoration is
// recorded, so the attempt provably never replaced the executable. The
// settlement keeps the pending/confirmed/rolled_back outcome vocabulary and
// distinguishes itself through explicit resolution metadata, records the
// installation failure, and never suppresses the target. The installed build
// continues in service unchanged. Only validated owned objects may later be
// cleaned; this function removes nothing.
func ResolveAbandoned(exec Executable, lease *Lease, plan RecoveryPlan, reason string, seams FileOps) (Receipt, error) {
	if err := validateRecoveryLease(exec.Path, lease, plan.Receipt); err != nil {
		return Receipt{}, err
	}
	r, err := reacquireDecision(exec, plan)
	if err != nil {
		return Receipt{}, err
	}
	installedID, err := StatFile(exec.Path)
	if err != nil {
		return Receipt{}, refuse("stat installed executable %s: %v", exec.Path, err)
	}
	if !installedID.SameFile(r.ExecutableID) {
		return Receipt{}, refuse("installed executable %s changed identity before abandonment", exec.Path)
	}
	digest, err := DigestFile(exec.Path)
	if err != nil || digest != r.OldDigest {
		return Receipt{}, refuse("installed executable %s bytes changed before abandonment", exec.Path)
	}

	now := seams.now()
	r.Resolution = ResolutionAbandonedPreReplacement
	r.InstallFailed = true
	r.ResolvedAt = now
	r.UpdatedAt = now
	r.Error = SanitizeError(reason)
	if err := seams.writeReceipt(ReceiptPath(exec.Path), r); err != nil {
		return Receipt{}, err
	}
	return r, nil
}

// RestorePrevious performs the validated restoration transaction for a
// candidate-installed receipt: prepare a separate synced restore copy of the
// verified backup bytes, persist the write-ahead recovery-attempt identity
// (including the restore copy's identity) before any rename, atomically
// rename the copy onto the installed path with the original mode and
// ownership, sync, and only then persist outcome=rolled_back with
// phase=rollback-attempted. When a prior attempt's rename already landed
// (installed identity matches the recorded RestoreID) only the bookkeeping
// is completed. The installed path stays continuously present, the private
// backup stays usable through recovery, and the installed path — never the
// backup path — is the one to execute afterwards. Exact-target suppression
// is recorded best-effort after rolled_back is durable; boot-time
// reconciliation re-writes it if that write failed.
func RestorePrevious(exec Executable, lease *Lease, plan RecoveryPlan, reason string, seams FileOps) (Receipt, error) {
	if err := validateRecoveryLease(exec.Path, lease, plan.Receipt); err != nil {
		return Receipt{}, err
	}
	r, err := reacquireDecision(exec, plan)
	if err != nil {
		return Receipt{}, err
	}

	installedID, err := StatFile(exec.Path)
	if err != nil {
		return Receipt{}, refuse("stat installed executable %s: %v", exec.Path, err)
	}
	digest, err := DigestFile(exec.Path)
	if err != nil {
		return Receipt{}, refuse("digest installed executable %s: %v", exec.Path, err)
	}

	restoreAlreadyLanded := r.RestoreID != nil && installedID.SameFile(*r.RestoreID) && digest == r.OldDigest
	if !restoreAlreadyLanded {
		candidateInstalled := digest == r.NewDigest && installedID.SameFile(r.StagingID)
		if !candidateInstalled {
			return Receipt{}, refuse("installed executable %s no longer matches the recorded candidate identity", exec.Path)
		}
		if err := validateBackupForRestore(exec.Path, r); err != nil {
			return Receipt{}, err
		}
	}

	txDir := filepath.Join(LeaseDir(exec.Path), txDirPrefix+r.TransactionID)

	reuseRestore := false
	if restoreAlreadyLanded {
		reuseRestore = true
	} else if r.RestorePath != "" && r.RestoreID != nil {
		// A prior attempt prepared a restore copy before dying: reuse it only
		// when it is still the exact recorded, digest-verified object.
		if id, err := lstatOwnedRegular(r.RestorePath); err == nil &&
			id.SameFile(*r.RestoreID) {
			if d, derr := DigestFile(r.RestorePath); derr == nil && d == r.OldDigest {
				reuseRestore = true
			}
		}
	}

	attemptID := r.RecoveryAttemptID
	restorePath := r.RestorePath
	var restoreID FileIdentity
	if reuseRestore {
		if restoreAlreadyLanded {
			// The rename already happened; only the rolled_back bookkeeping
			// remains. Do not touch the installed path again.
			return completeRollback(exec, plan, r, reason, seams)
		}
		id, err := lstatOwnedRegular(restorePath)
		if err != nil {
			return Receipt{}, refuse("stat prepared restore copy %s: %v", restorePath, err)
		}
		if id.Mode != r.OriginalMode {
			return Receipt{}, refuse("prepared restore copy %s mode is %o; expected %o", restorePath, id.Mode, r.OriginalMode)
		}
		restoreID = id
		if attemptID == "" {
			attemptID, err = newRecoveryAttemptID()
			if err != nil {
				return Receipt{}, err
			}
		}
	} else {
		attemptID, err = newRecoveryAttemptID()
		if err != nil {
			return Receipt{}, err
		}
		restorePath = filepath.Join(txDir, restorePrefix+attemptID)
		id, err := seams.stageCopy(restorePath, r.BackupPath, r.OriginalMode)
		if err != nil {
			return Receipt{}, fmt.Errorf("prepare restore copy %s: %w", restorePath, err)
		}
		if id.Mode != r.OriginalMode {
			return Receipt{}, refuse("restore copy %s mode is %o; expected %o", restorePath, id.Mode, r.OriginalMode)
		}
		// The copied bytes are re-verified against the recorded original
		// digest so no substitution between backup validation and the copy
		// can redirect restoration.
		copied, err := DigestFile(restorePath)
		if err != nil || copied != r.OldDigest {
			return Receipt{}, refuse("restore copy %s does not carry the recorded original bytes", restorePath)
		}
		restoreID = id
	}

	// Write-ahead honesty: the attempt identity and the restore copy's
	// identity are durable before the rename, so an interruption at any
	// later boundary is reconcilable by identity.
	now := seams.now()
	r.RecoveryAttemptID = attemptID
	r.RecoveryKind = RecoveryKindRestore
	r.RecoveryAttemptBy = os.Getpid()
	r.RecoveryStartedAt = now
	r.RestorePath = restorePath
	r.RestoreID = &restoreID
	r.UpdatedAt = now
	if err := seams.writeReceipt(ReceiptPath(exec.Path), r); err != nil {
		return Receipt{}, err
	}

	if err := seams.rename(restorePath, exec.Path); err != nil {
		return Receipt{}, fmt.Errorf("restore rename: %w", err)
	}
	if err := seams.syncDir(filepath.Dir(exec.Path)); err != nil {
		// The rename may or may not be durable; the recorded RestoreID makes
		// either state reconcilable on the next launch.
		return Receipt{}, fmt.Errorf("sync installed dir after restore: %w", err)
	}
	if err := restoreInstalledModeAndOwner(exec.Path, r); err != nil {
		return Receipt{}, err
	}

	return completeRollback(exec, plan, r, reason, seams)
}

// completeRollback persists outcome=rolled_back with phase=rollback-attempted
// after the restoration is fact, then records exact-target suppression
// best-effort (boot-time reconciliation repairs a failed suppression write).
func completeRollback(exec Executable, plan RecoveryPlan, r Receipt, reason string, seams FileOps) (Receipt, error) {
	now := seams.now()
	r.Outcome = OutcomeRolledBack
	r.Phase = PhaseRollbackAttempted
	r.Error = SanitizeError(reason)
	r.UpdatedAt = now
	if err := seams.writeReceipt(ReceiptPath(exec.Path), r); err != nil {
		return Receipt{}, err
	}
	// Best-effort: rolled_back is already durable; a failed suppression
	// write must never erase it, and ReconcileSuppression re-writes it on the
	// next launch of any build that understands it.
	_ = SuppressTarget(exec.Path, SuppressedTarget{
		Version:       r.ToVersion,
		Digest:        r.NewDigest,
		TransactionID: r.TransactionID,
		SuppressedAt:  now,
	})
	return r, nil
}

// MarkRecoveryRestart persists the write-ahead restart attempt for a pending
// pre-replacement receipt before the unchanged build is re-executed after an
// aborted shutdown: the executable bytes never changed, this is an
// installation failure being tracked, not a rollback.
func MarkRecoveryRestart(exec Executable, lease *Lease, plan RecoveryPlan, reason string, seams FileOps) (Receipt, error) {
	if err := validateRecoveryLease(exec.Path, lease, plan.Receipt); err != nil {
		return Receipt{}, err
	}
	r, err := reacquireDecision(exec, plan)
	if err != nil {
		return Receipt{}, err
	}
	installedID, err := StatFile(exec.Path)
	if err != nil {
		return Receipt{}, refuse("stat installed executable %s: %v", exec.Path, err)
	}
	if !installedID.SameFile(r.ExecutableID) {
		return Receipt{}, refuse("installed executable %s changed identity before restart", exec.Path)
	}
	digest, err := DigestFile(exec.Path)
	if err != nil || digest != r.OldDigest {
		return Receipt{}, refuse("installed executable %s bytes changed before restart", exec.Path)
	}
	if r.Phase != PhaseBackupReady {
		return Receipt{}, refuse("restart attempt requires a backup-ready receipt, not %q", r.Phase)
	}
	now := seams.now()
	attemptID := r.RecoveryAttemptID
	if attemptID == "" {
		attemptID, err = newRecoveryAttemptID()
		if err != nil {
			return Receipt{}, err
		}
	}
	r.RecoveryAttemptID = attemptID
	r.RecoveryKind = RecoveryKindRestart
	r.RecoveryAttemptBy = os.Getpid()
	r.RecoveryStartedAt = now
	r.Error = SanitizeError(reason)
	r.UpdatedAt = now
	if err := seams.writeReceipt(ReceiptPath(exec.Path), r); err != nil {
		return Receipt{}, err
	}
	return r, nil
}
