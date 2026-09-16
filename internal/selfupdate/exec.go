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

	"golang.org/x/sys/unix"
)

// HandoffEnvVar carries the handoff metadata through the exec boundary.
const HandoffEnvVar = "AGENTICO_SELFUPDATE_HANDOFF"

// handoffSchemaVersion is the schema of HandoffMetadata.
const handoffSchemaVersion = 1

// HandoffMetadata is the only private state the new image needs to adopt the
// inherited lease. It carries a token path, never a token, and never the
// launch environment.
type HandoffMetadata struct {
	SchemaVersion  int          `json:"schema_version"`
	TransactionID  string       `json:"transaction_id"`
	RuntimeDir     string       `json:"runtime_dir"`
	StateDir       string       `json:"state_dir"`
	Config         string       `json:"config_path"`
	FromPID        int          `json:"from_pid"` // exec preserves PID: the new image must still be FromPID
	LeaseFD        int          `json:"lease_fd"`
	LeasePath      string       `json:"lease_path"`
	ExecutablePath string       `json:"executable_path"`
	NewDigest      string       `json:"new_digest"`
	FromVersion    string       `json:"from_version"`
	ToVersion      string       `json:"to_version"`
	Bind           BindEndpoint `json:"bind_endpoint"`
	ReceiptPath    string       `json:"receipt_path"`
	AuthTokenPath  string       `json:"auth_token_path"`
}

// EncodeHandoff marshals handoff metadata to its JSON form.
func EncodeHandoff(m HandoffMetadata) string {
	data, _ := json.Marshal(m)
	return string(data)
}

// ParseHandoff parses handoff metadata from its JSON form.
func ParseHandoff(value string) (HandoffMetadata, error) {
	var m HandoffMetadata
	if err := json.Unmarshal([]byte(value), &m); err != nil {
		return HandoffMetadata{}, fmt.Errorf("parse handoff metadata: %w", err)
	}
	return m, nil
}

// ParseHandoffEnv finds and parses the handoff entry in an environment
// slice. A present-but-unparseable entry reports false: a bad handoff must
// fail closed.
func ParseHandoffEnv(env []string) (HandoffMetadata, bool) {
	prefix := HandoffEnvVar + "="
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			continue
		}
		m, err := ParseHandoff(strings.TrimPrefix(entry, prefix))
		if err != nil {
			return HandoffMetadata{}, false
		}
		return m, true
	}
	return HandoffMetadata{}, false
}

// ClearCLOEXEC marks fd inheritable across exec. Only the binary lease fd
// may ever be marked this way, and only inside the final handoff window.
func ClearCLOEXEC(fd int) error {
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
		return fmt.Errorf("clear CLOEXEC on fd %d: %w", fd, err)
	}
	return nil
}

// SetCLOEXEC marks fd close-on-exec again.
func SetCLOEXEC(fd int) error {
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		return fmt.Errorf("set CLOEXEC on fd %d: %w", fd, err)
	}
	return nil
}

// IsCLOEXEC reports the close-on-exec state of fd.
func IsCLOEXEC(fd int) (bool, error) {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return false, fmt.Errorf("inspect fd %d flags: %w", fd, err)
	}
	return flags&unix.FD_CLOEXEC != 0, nil
}

// ExecFunc is the exec seam, replaceable in tests.
type ExecFunc func(path string, argv, env []string) error

// SysExec is the real exec(2).
var SysExec ExecFunc = func(path string, argv, env []string) error {
	return unix.Exec(path, argv, env)
}

// ExecReplace performs the final exec onto the newly installed binary. Only
// the lease fd is made inheritable; every other descriptor keeps its
// close-on-exec state. If exec returns an error, the lease fd is made
// close-on-exec again before returning so no later child inherits it.
func ExecReplace(installedPath string, argv []string, env []string, extraEnvEntry string, leaseFD int, execFn ExecFunc) error {
	if execFn == nil {
		execFn = SysExec
	}
	if err := ClearCLOEXEC(leaseFD); err != nil {
		return err
	}
	execEnv := env
	if extraEnvEntry != "" {
		// Copy: never mutate (or alias into) the caller's environment slice.
		execEnv = append(append([]string(nil), env...), extraEnvEntry)
	}
	if err := execFn(installedPath, argv, execEnv); err != nil {
		_ = SetCLOEXEC(leaseFD)
		return err
	}
	return nil
}

// AdoptHandoff validates and adopts an inherited handoff in the new image.
// Every identity in the metadata must match the durable receipt, the current
// process (exec preserves PID), and the freshly captured installed
// executable; the descriptor must really reference the lease file. Only then
// is the lease adopted (flock continuity via the inherited open file
// description) and close-on-exec restored before general bootstrap can
// spawn children. An arbitrary descriptor or record cannot authorize
// adoption.
func AdoptHandoff(m HandoffMetadata, installedExec Executable) (*Lease, Receipt, error) {
	if m.SchemaVersion != handoffSchemaVersion {
		return nil, Receipt{}, fmt.Errorf("handoff schema version %d is not supported", m.SchemaVersion)
	}
	receipt, err := ReadReceipt(m.ReceiptPath)
	if err != nil {
		return nil, Receipt{}, err
	}
	if receipt.SchemaVersion != receiptSchemaVersion {
		return nil, Receipt{}, fmt.Errorf("receipt schema version %d is not supported", receipt.SchemaVersion)
	}
	if receipt.TransactionID != m.TransactionID {
		return nil, Receipt{}, fmt.Errorf("receipt transaction id %q does not match handoff %q", receipt.TransactionID, m.TransactionID)
	}
	if receipt.RuntimeDir != m.RuntimeDir || receipt.StateDir != m.StateDir || receipt.Config != m.Config {
		return nil, Receipt{}, fmt.Errorf("receipt runtime identity does not match handoff metadata")
	}
	if receipt.PID != m.FromPID || m.FromPID != os.Getpid() {
		return nil, Receipt{}, fmt.Errorf("handoff pid %d does not match receipt pid %d or current pid %d", m.FromPID, receipt.PID, os.Getpid())
	}
	if receipt.ExecutablePath != m.ExecutablePath || m.ExecutablePath != installedExec.Path {
		return nil, Receipt{}, fmt.Errorf("handoff executable path does not match receipt %q or installed %q", receipt.ExecutablePath, installedExec.Path)
	}
	if receipt.NewDigest != m.NewDigest || m.NewDigest != installedExec.Digest {
		return nil, Receipt{}, fmt.Errorf("handoff digest does not match receipt %q or installed %q", receipt.NewDigest, installedExec.Digest)
	}
	if receipt.Phase != PhaseReplacementComplete {
		return nil, Receipt{}, fmt.Errorf("receipt phase %q is not %q", receipt.Phase, PhaseReplacementComplete)
	}
	if receipt.Outcome != OutcomePending {
		return nil, Receipt{}, fmt.Errorf("receipt outcome %q is not %q", receipt.Outcome, OutcomePending)
	}
	if receipt.FromVersion != m.FromVersion || receipt.ToVersion != m.ToVersion {
		return nil, Receipt{}, fmt.Errorf("receipt versions %q->%q do not match handoff %q->%q", receipt.FromVersion, receipt.ToVersion, m.FromVersion, m.ToVersion)
	}

	if _, err := unix.FcntlInt(uintptr(m.LeaseFD), unix.F_GETFD, 0); err != nil {
		return nil, Receipt{}, fmt.Errorf("handoff lease fd %d is not open: %w", m.LeaseFD, err)
	}
	var fst unix.Stat_t
	if err := unix.Fstat(m.LeaseFD, &fst); err != nil {
		return nil, Receipt{}, fmt.Errorf("fstat handoff lease fd: %w", err)
	}
	leaseID, err := StatFile(m.LeasePath)
	if err != nil {
		return nil, Receipt{}, err
	}
	if uint64(fst.Dev) != leaseID.Dev || uint64(fst.Ino) != leaseID.Ino {
		return nil, Receipt{}, fmt.Errorf("handoff lease fd does not reference lease file %s", m.LeasePath)
	}

	rec := OwnershipRecord{
		SchemaVersion:    ownershipSchemaVersion,
		RuntimeDir:       receipt.RuntimeDir,
		StateDir:         receipt.StateDir,
		Config:           receipt.Config,
		PID:              os.Getpid(),
		PGID:             receipt.PGID,
		Version:          m.ToVersion,
		StartedAt:        receipt.StartedAt,
		ExecutablePath:   receipt.ExecutablePath,
		ExecutableDigest: installedExec.Digest,
		ExecutableIno:    installedExec.ID.Ino,
		TransactionID:    m.TransactionID,
	}
	lease, err := AdoptLease(m.LeaseFD, rec)
	if err != nil {
		return nil, Receipt{}, err
	}
	if err := SetCLOEXEC(m.LeaseFD); err != nil {
		_ = lease.Close()
		return nil, Receipt{}, err
	}
	return lease, receipt, nil
}
