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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// ownershipSchemaVersion is the on-disk schema of OwnershipRecord.
const ownershipSchemaVersion = 1

const (
	leaseDirName      = ".agentico-selfupdate"
	receiptName       = "receipt.json"
	txDirPrefix       = "tx-"
	backupName        = "backup"
	stagedName        = "staged"
	restorePrefix     = "restore-"
	stagingRecordName = "staging.json"
	updateLockSuffix  = ".update.lock"
)

// OwnershipRecord binds the lease holder to a runtime, process, executable,
// and transaction identity. It is diagnostic: the OS flock is the authority,
// and this record is validated, never trusted blindly.
type OwnershipRecord struct {
	SchemaVersion    int       `json:"schema_version"`
	RuntimeDir       string    `json:"runtime_dir"`
	StateDir         string    `json:"state_dir"`
	Config           string    `json:"config_path"`
	PID              int       `json:"pid"`
	PGID             int       `json:"pgid,omitempty"`
	Version          string    `json:"version,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	ExecutablePath   string    `json:"executable_path"`
	ExecutableDigest string    `json:"executable_digest"`
	ExecutableIno    uint64    `json:"executable_ino"`
	TransactionID    string    `json:"transaction_id,omitempty"`
}

// LeaseDir returns the lease directory for a canonical executable path. It
// lives next to the installed binary so its identity is the executable path,
// which stays stable across inode replacement.
func LeaseDir(execPath string) string {
	return filepath.Join(filepath.Dir(execPath), leaseDirName)
}

// leaseKey derives the per-binary hash used in lease and record file names.
// The canonical executable path is the stable key: inode replacement must not
// orphan the lease.
func leaseKey(execPath string) string {
	sum := sha256.Sum256([]byte(execPath))
	return hex.EncodeToString(sum[:])[:16]
}

// LeasePath returns the flock file path for the executable. The file is
// owner-only (0600) and is never unlinked: unlinking would allow a racing
// process to lock a fresh inode and double-own updates.
func LeasePath(execPath string) string {
	return filepath.Join(LeaseDir(execPath), "lease-"+leaseKey(execPath)+".lock")
}

// OwnershipRecordPath returns the owner-only record path for the executable.
// Like the lease file, it is never unlinked and is overwritten by each new
// owner.
func OwnershipRecordPath(execPath string) string {
	return filepath.Join(LeaseDir(execPath), "owner-"+leaseKey(execPath)+".json")
}

// ReceiptPath returns the stable latest-transaction receipt path for the
// executable. The receipt is keyed by the executable's canonical path so
// independent executable copies in one directory never share or overwrite
// each other's transaction authority; the lease, ownership record, and
// receipt all key identically.
func ReceiptPath(execPath string) string {
	return filepath.Join(LeaseDir(execPath), "receipt-"+leaseKey(execPath)+".json")
}

// LegacyReceiptPath returns the Phase 1 receipt path: one shared receipt.json
// per lease directory. It is read-only vocabulary: existing Phase 1 records
// are recognized here, but every Phase 2 write lands at the keyed path (and,
// when settling a legacy record, at the legacy path too so historical
// binaries keep observing the settlement).
func LegacyReceiptPath(execPath string) string {
	return filepath.Join(LeaseDir(execPath), receiptName)
}

// ReadLatestReceipt reads the executable's latest receipt with Phase 1
// legacy fallback. The keyed path takes precedence whenever it exists and is
// parseable; a keyed record that does not bind to this executable is an
// unsafe mismatch, never silently skipped. When the keyed path is absent the
// legacy receipt.json is consulted and recognized only when it binds to this
// executable — a legacy record naming another executable belongs to that
// other binary and is left untouched. found is false when no record binds.
func ReadLatestReceipt(execPath string) (receipt Receipt, found bool, err error) {
	keyed := ReceiptPath(execPath)
	if r, rerr := ReadReceipt(keyed); rerr == nil {
		if r.ExecutablePath != execPath {
			return Receipt{}, false, fmt.Errorf("receipt at %s does not bind to executable %s", keyed, execPath)
		}
		return r, true, nil
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return Receipt{}, false, fmt.Errorf("read receipt %s: %w", keyed, rerr)
	}
	legacy := LegacyReceiptPath(execPath)
	r, rerr := ReadReceipt(legacy)
	if rerr != nil {
		if errors.Is(rerr, os.ErrNotExist) {
			return Receipt{}, false, nil
		}
		return Receipt{}, false, fmt.Errorf("read receipt %s: %w", legacy, rerr)
	}
	if r.ExecutablePath != execPath {
		// Another executable's Phase 1 record: not ours, untouched.
		return Receipt{}, false, nil
	}
	return r, true, nil
}

// IsTxDirPath reports whether path names a transaction directory under a
// lease dir (base name carries the tx- prefix).
func IsTxDirPath(path string) bool {
	return strings.HasPrefix(filepath.Base(path), txDirPrefix)
}

// ensureLeaseDir creates the lease directory owner-only and self-heals its
// permissions on every call, mirroring the registry publish pattern.
func ensureLeaseDir(execPath string) error {
	dir := LeaseDir(execPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create lease dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("repair lease dir permissions: %w", err)
	}
	return nil
}

// LeaseHeldError reports that another open file description holds the
// binary-scoped lease. Record carries the best-effort current ownership
// record; it is the zero value when none could be read.
type LeaseHeldError struct {
	Record OwnershipRecord
}

func (e *LeaseHeldError) Error() string {
	return fmt.Sprintf("selfupdate lease for %s held by pid %d", e.Record.ExecutablePath, e.Record.PID)
}

// IsLeaseHeld reports whether err (or anything it wraps) is lease contention.
func IsLeaseHeld(err error) bool {
	var held *LeaseHeldError
	return errors.As(err, &held)
}

// Lease is the binary-scoped lifetime lease on one canonical installed
// executable path. The flock is held for the lifetime of the owning process
// (and across exec, when the fd is inherited), serializing updates no matter
// which runtime dir the owner serves from. The underlying fd is
// close-on-exec by default so ordinary children never inherit it. The fd is
// held raw (no *os.File): an adopted lease shares the descriptor with the
// pre-exec owner, and a second os.File would attach a finalizer that can
// double-close the descriptor once the adopted owner closes it.
type Lease struct {
	fd     int
	record OwnershipRecord
	path   string
}

// AcquireLease takes the binary-scoped lease for exec. On contention it
// returns a *LeaseHeldError carrying the record readable from disk (zero
// record when unreadable); it never steals ownership from a live holder.
func AcquireLease(exec Executable, rec OwnershipRecord) (*Lease, error) {
	if err := ensureLeaseDir(exec.Path); err != nil {
		return nil, err
	}
	rec.SchemaVersion = ownershipSchemaVersion
	if rec.ExecutablePath == "" {
		rec.ExecutablePath = exec.Path
	}

	path := LeasePath(exec.Path)
	// O_CLOEXEC is load-bearing: the lease fd stays close-on-exec until the
	// final handoff window clears it explicitly via ClearCLOEXEC.
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lease file: %w", err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(fd)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			record, _ := ReadOwnershipRecord(exec.Path)
			return nil, &LeaseHeldError{Record: record}
		}
		return nil, fmt.Errorf("acquire lease: %w", err)
	}
	if err := writeOwnershipRecord(exec.Path, rec); err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
		return nil, err
	}
	return &Lease{fd: fd, record: rec, path: path}, nil
}

// FD exposes the raw lease descriptor for exec handoff control.
func (l *Lease) FD() int {
	return l.fd
}

// Record returns the ownership record written at acquisition.
func (l *Lease) Record() OwnershipRecord {
	return l.record
}

// Path returns the lease flock file path.
func (l *Lease) Path() string {
	return l.path
}

// SetTransactionID rewrites the ownership record with the given transaction
// id, keeping every other field. The in-memory record only advances when the
// atomic write succeeds.
func (l *Lease) SetTransactionID(txID string) error {
	updated := l.record
	updated.TransactionID = txID
	if err := writeOwnershipRecord(updated.ExecutablePath, updated); err != nil {
		return err
	}
	l.record = updated
	return nil
}

// Close releases the lease. It never removes any file: the lock, record, and
// receipt paths intentionally survive so the next owner overwrites them in
// place and racing unlink/recreate double-ownership is impossible.
func (l *Lease) Close() error {
	if l == nil || l.fd < 0 {
		return nil
	}
	errUnlock := unix.Flock(l.fd, unix.LOCK_UN)
	errClose := unix.Close(l.fd)
	l.fd = -1
	return errors.Join(errUnlock, errClose)
}

// AdoptLease wraps an already-open, already-flocked lease descriptor (an fd
// inherited through exec) into a Lease without re-locking or unlocking: the
// open file description carries the held lock across the exec boundary. The
// updated record is written atomically; the caller has already validated the
// descriptor. Exactly one owner must close the descriptor: across a real
// exec the pre-exec owner no longer exists, so the adopted Lease is it.
func AdoptLease(fd int, rec OwnershipRecord) (*Lease, error) {
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
		return nil, fmt.Errorf("adopt lease fd %d is not open: %w", fd, err)
	}
	rec.SchemaVersion = ownershipSchemaVersion
	if err := writeOwnershipRecord(rec.ExecutablePath, rec); err != nil {
		return nil, err
	}
	path := LeasePath(rec.ExecutablePath)
	return &Lease{fd: fd, record: rec, path: path}, nil
}

// ReadOwnershipRecord reads and parses the ownership record for the
// executable.
func ReadOwnershipRecord(execPath string) (OwnershipRecord, error) {
	data, err := os.ReadFile(OwnershipRecordPath(execPath))
	if err != nil {
		return OwnershipRecord{}, fmt.Errorf("read ownership record: %w", err)
	}
	var rec OwnershipRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return OwnershipRecord{}, fmt.Errorf("parse ownership record: %w", err)
	}
	return rec, nil
}

// ValidateOwnershipRecord rejects records that do not bind to the given
// executable and runtime. Mismatched records are treated as stale, not
// authoritative.
func ValidateOwnershipRecord(rec OwnershipRecord, exec Executable, runtimeDir string) error {
	if rec.ExecutablePath != exec.Path {
		return fmt.Errorf("ownership record executable path %q does not match %q", rec.ExecutablePath, exec.Path)
	}
	if rec.ExecutableDigest != exec.Digest {
		return fmt.Errorf("ownership record executable digest %q does not match %q", rec.ExecutableDigest, exec.Digest)
	}
	if rec.RuntimeDir != runtimeDir {
		return fmt.Errorf("ownership record runtime dir %q does not match %q", rec.RuntimeDir, runtimeDir)
	}
	return nil
}

// ProcessAlive reports whether pid refers to a live process. Signal 0 is a
// pure existence/permission probe; EPERM means the process exists but is
// owned by another user.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

// writeOwnershipRecord atomically writes rec with owner-only permissions
// (pid-suffixed O_EXCL tmp, rename, chmod repair). It deliberately skips the
// durability syncs: the record is diagnostic and the flock is the authority,
// so a crash at worst drops a stale record that the next owner rewrites on
// acquisition.
func writeOwnershipRecord(execPath string, rec OwnershipRecord) error {
	if err := ensureLeaseDir(execPath); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ownership record: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(OwnershipRecordPath(execPath), data, false)
}

// ErrRuntimeUpdateLockHeld reports contention on the per-runtime update lock.
var ErrRuntimeUpdateLockHeld = errors.New("runtime update lock held")

// IsRuntimeUpdateLockHeld reports whether err (or anything it wraps) is
// runtime update lock contention.
func IsRuntimeUpdateLockHeld(err error) bool {
	return errors.Is(err, ErrRuntimeUpdateLockHeld)
}

// RuntimeUpdateLock serializes update transactions within one runtime dir.
// It is a separate layer from the instance lock and the binary lease: the
// instance lock proves single-server liveness, this lock serializes update
// work, and the lease owns the binary for the process lifetime.
type RuntimeUpdateLock struct {
	file *os.File
}

// RuntimeUpdateLockPath returns the per-runtime update lock path. The file
// is owner-only (0600) and never unlinked.
func RuntimeUpdateLockPath(runtimeDir string) string {
	return filepath.Join(runtimeDir, updateLockSuffix)
}

// AcquireRuntimeUpdateLock takes the per-runtime update lock without
// blocking. Contention returns an error wrapping ErrRuntimeUpdateLockHeld.
func AcquireRuntimeUpdateLock(runtimeDir string) (*RuntimeUpdateLock, error) {
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return nil, fmt.Errorf("create runtime dir: %w", err)
	}
	f, err := os.OpenFile(RuntimeUpdateLockPath(runtimeDir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open runtime update lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrRuntimeUpdateLockHeld, RuntimeUpdateLockPath(runtimeDir))
		}
		return nil, fmt.Errorf("acquire runtime update lock: %w", err)
	}
	return &RuntimeUpdateLock{file: f}, nil
}

// Close releases the runtime update lock. It removes nothing.
func (l *RuntimeUpdateLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	errUnlock := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	errClose := l.file.Close()
	l.file = nil
	return errors.Join(errUnlock, errClose)
}

// LiveHandoff reports whether a live handoff is in progress for the
// executable: true only when another open file description currently holds
// the lease and the latest receipt is an actionable pending transaction. A
// settled receipt (confirmed, rolled back, or explicitly abandoned) never
// blocks a competing launch. Every other observation (no lease dir, free
// lease, missing/unparseable receipt) reports false. Only real I/O errors
// opening the lease file itself are returned as errors. Stale PID metadata
// proves nothing here: the flock is the liveness authority.
func LiveHandoff(execPath string) (bool, Receipt, error) {
	receipt, found := readReceiptForLiveHandoff(execPath)
	f, err := os.OpenFile(LeasePath(execPath), os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, receipt, nil
		}
		return false, receipt, fmt.Errorf("open lease file: %w", err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return found && receipt.ActionablePending(), receipt, nil
		}
		return false, receipt, fmt.Errorf("probe lease: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
		return false, receipt, fmt.Errorf("release lease probe: %w", err)
	}
	return false, receipt, nil
}

// readReceiptForLiveHandoff reads the best receipt for the live-handoff
// probe without failing the launch on an unreadable record: an unparseable
// receipt cannot be trusted to prove liveness either way, so the probe
// reports no receipt and the recovery inspection (which must fail closed)
// is the authority for unsafe metadata.
func readReceiptForLiveHandoff(execPath string) (Receipt, bool) {
	r, found, err := ReadLatestReceipt(execPath)
	if err != nil {
		return Receipt{}, false
	}
	return r, found
}
