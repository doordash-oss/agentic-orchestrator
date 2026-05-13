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

package instancelock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const (
	lockFilename     = ".instance.lock"
	metadataFilename = ".instance.json"
)

// Owner is best-effort metadata about the process holding an Agentic instance
// lock. It is diagnostic only; the OS-held lock is the authority.
type Owner struct {
	PID       int       `json:"pid"`
	PGID      int       `json:"pgid,omitempty"`
	StartedAt time.Time `json:"started_at"`
	StateDir  string    `json:"state_dir"`
	Config    string    `json:"config_path"`
	Version   string    `json:"version,omitempty"`
}

// Lock represents an acquired instance lock. Call Close when the TUI exits.
type Lock struct {
	file  *os.File
	owner Owner
}

// LockPath returns the path used for the OS-held advisory lock.
func LockPath(runtimeDir string) string {
	return filepath.Join(runtimeDir, lockFilename)
}

// MetadataPath returns the path used for diagnostic owner metadata.
func MetadataPath(runtimeDir string) string {
	return filepath.Join(runtimeDir, metadataFilename)
}

// Acquire attempts to take the Agentic instance lock for runtimeDir.
// The returned bool is false when another live process currently holds the
// lock; in that case owner contains any metadata that could be read.
func Acquire(runtimeDir, stateDir, configPath, version string) (*Lock, bool, Owner, error) {
	if runtimeDir == "" {
		return nil, false, Owner{}, errors.New("runtime dir is empty")
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return nil, false, Owner{}, fmt.Errorf("create runtime dir: %w", err)
	}

	lockPath := LockPath(runtimeDir)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, Owner{}, fmt.Errorf("open instance lock: %w", err)
	}

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			owner, _ := ReadOwner(runtimeDir)
			return nil, false, owner, nil
		}
		return nil, false, Owner{}, fmt.Errorf("acquire instance lock: %w", err)
	}

	owner := Owner{
		PID:       os.Getpid(),
		PGID:      currentPGID(),
		StartedAt: time.Now(),
		StateDir:  stateDir,
		Config:    configPath,
		Version:   version,
	}
	if err := writeOwner(runtimeDir, owner); err != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
		return nil, false, Owner{}, err
	}

	return &Lock{file: f, owner: owner}, true, owner, nil
}

// Owner returns the metadata written when the lock was acquired.
func (l *Lock) Owner() Owner {
	if l == nil {
		return Owner{}
	}
	return l.owner
}

// Close releases the instance lock. The metadata file is intentionally left in
// place: it is overwritten by the next owner and ignored unless the lock is
// actively held, which avoids races with a fast successor process.
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	errUnlock := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	errClose := l.file.Close()
	l.file = nil
	return errors.Join(errUnlock, errClose)
}

// ReadOwner reads best-effort owner metadata. Missing metadata is not an error
// because the lock file itself, not this metadata, is authoritative.
func ReadOwner(runtimeDir string) (Owner, error) {
	data, err := os.ReadFile(MetadataPath(runtimeDir))
	if errors.Is(err, os.ErrNotExist) {
		return Owner{}, nil
	}
	if err != nil {
		return Owner{}, fmt.Errorf("read instance metadata: %w", err)
	}
	var owner Owner
	if err := json.Unmarshal(data, &owner); err != nil {
		return Owner{}, fmt.Errorf("parse instance metadata: %w", err)
	}
	return owner, nil
}

func writeOwner(runtimeDir string, owner Owner) error {
	data, err := json.MarshalIndent(owner, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal instance metadata: %w", err)
	}
	data = append(data, '\n')

	path := MetadataPath(runtimeDir)
	tmp := filepath.Join(runtimeDir, fmt.Sprintf(".instance-%d.json.tmp", os.Getpid()))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write instance metadata: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit instance metadata: %w", err)
	}
	return nil
}

func currentPGID() int {
	pgid, err := unix.Getpgid(0)
	if err != nil {
		return 0
	}
	return pgid
}
