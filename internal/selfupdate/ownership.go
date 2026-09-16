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
	"time"

	"golang.org/x/sys/unix"
)

// ownershipSchemaVersion is the on-disk schema of OwnershipRecord.
const ownershipSchemaVersion = 1

const (
	leaseDirName     = ".agentico-selfupdate"
	receiptName      = "receipt.json"
	txDirPrefix      = "tx-"
	backupName       = "backup"
	stagedName       = "staged"
	updateLockSuffix = ".update.lock"
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
// executable.
func ReceiptPath(execPath string) string {
	return filepath.Join(LeaseDir(execPath), receiptName)
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
// (pid-suffixed O_EXCL tmp, rename, chmod repair).
func writeOwnershipRecord(execPath string, rec OwnershipRecord) error {
	if err := ensureLeaseDir(execPath); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ownership record: %w", err)
	}
	data = append(data, '\n')

	path := OwnershipRecordPath(execPath)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create ownership record temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write ownership record temp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close ownership record temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit ownership record: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("repair ownership record permissions: %w", err)
	}
	return nil
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
// the lease and the latest receipt is still pending. Every other observation
// (no lease dir, free lease, missing/unparseable/confirmed receipt) reports
// false. Only real I/O errors opening the lease file itself are returned as
// errors.
func LiveHandoff(execPath string) (bool, Receipt, error) {
	receipt, _ := ReadReceipt(ReceiptPath(execPath))
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
			return receipt.Outcome == OutcomePending, receipt, nil
		}
		return false, receipt, fmt.Errorf("probe lease: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
		return false, receipt, fmt.Errorf("release lease probe: %w", err)
	}
	return false, receipt, nil
}
