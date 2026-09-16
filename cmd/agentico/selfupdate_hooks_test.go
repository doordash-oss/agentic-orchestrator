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

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
)

// writeHooksTestExecutable writes an exact-mode executable file, defeating
// the process umask the way the selfupdate package's own tests do.
func writeHooksTestExecutable(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return path
}

// TestCheckLiveHandoffScenarios exercises the launch gate end to end against
// a real installed-copy fixture: a live handoff (held lease plus pending
// receipt) rejects a competing launch, while a free lease or a confirmed
// receipt lets it through.
func TestCheckLiveHandoffScenarios(t *testing.T) {
	dir := t.TempDir()
	installedPath := writeHooksTestExecutable(t, dir, "agentico", "installed-v1")
	exec, err := selfupdate.CaptureExecutableAt(installedPath)
	if err != nil {
		t.Fatalf("capture installed executable: %v", err)
	}
	candidatePath := writeHooksTestExecutable(t, dir, "candidate", "candidate-v2")
	candidateDigest, err := selfupdate.DigestFile(candidatePath)
	if err != nil {
		t.Fatalf("digest candidate: %v", err)
	}
	runtimeDir := t.TempDir()
	record := selfupdate.OwnershipRecord{
		RuntimeDir:       runtimeDir,
		StateDir:         filepath.Join(runtimeDir, "features"),
		Config:           filepath.Join(runtimeDir, "config.yaml"),
		PID:              os.Getpid(),
		Version:          "test-version",
		StartedAt:        time.Now(),
		ExecutablePath:   exec.Path,
		ExecutableDigest: exec.Digest,
		ExecutableIno:    exec.ID.Ino,
	}
	opts := selfupdate.BeginOptions{
		RuntimeDir:      runtimeDir,
		StateDir:        filepath.Join(runtimeDir, "features"),
		Config:          filepath.Join(runtimeDir, "config.yaml"),
		PID:             os.Getpid(),
		FromVersion:     "1.0.0",
		ToVersion:       "2.0.0",
		CandidatePath:   candidatePath,
		CandidateDigest: candidateDigest,
		Bind: selfupdate.BindEndpoint{
			Host:         "127.0.0.1",
			Port:         54321,
			AdvertiseURL: "http://127.0.0.1:54321",
			Policy:       "loopback",
		},
		ReceiptDest: selfupdate.ReceiptPath(exec.Path),
	}

	// An empty exec path never probes.
	if err := checkLiveHandoff(""); err != nil {
		t.Fatalf("checkLiveHandoff(\"\") = %v; want nil", err)
	}
	// A never-leased executable has no lease file to probe: no handoff.
	if err := checkLiveHandoff(exec.Path); err != nil {
		t.Fatalf("checkLiveHandoff(unleased) = %v; want nil", err)
	}

	// Pending receipt behind a held lease: a competing launch is rejected
	// with the transaction and phase named.
	lease, err := selfupdate.AcquireLease(exec, record)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	tx, err := selfupdate.Begin(exec, opts, selfupdate.TxSeams{})
	if err != nil {
		_ = lease.Close()
		t.Fatalf("Begin: %v", err)
	}
	txid := tx.Receipt().TransactionID
	liveErr := checkLiveHandoff(exec.Path)
	if liveErr == nil {
		_ = lease.Close()
		t.Fatal("checkLiveHandoff() = nil during a live handoff; want rejection")
	}
	var held *liveHandoffError
	if !errors.As(liveErr, &held) {
		_ = lease.Close()
		t.Fatalf("checkLiveHandoff() = %T; want *liveHandoffError", liveErr)
	}
	if held.receipt.TransactionID != txid {
		_ = lease.Close()
		t.Fatalf("rejected transaction = %q; want %q", held.receipt.TransactionID, txid)
	}
	if !strings.Contains(held.Error(), txid) || !strings.Contains(held.Error(), string(selfupdate.PhaseBackupReady)) {
		_ = lease.Close()
		t.Fatalf("liveHandoffError message = %q; want transaction %q and phase named", held.Error(), txid)
	}

	// Free lease: the same pending receipt no longer blocks a launch.
	if err := lease.Close(); err != nil {
		t.Fatalf("close lease: %v", err)
	}
	if err := checkLiveHandoff(exec.Path); err != nil {
		t.Fatalf("checkLiveHandoff() with a free lease = %v; want nil", err)
	}

	// Confirmed receipt behind a held lease: the handoff completed.
	lease, err = selfupdate.AcquireLease(exec, record)
	if err != nil {
		t.Fatalf("re-acquire lease: %v", err)
	}
	defer func() { _ = lease.Close() }()
	if _, err := selfupdate.ConfirmTransaction(exec.Path, txid); err != nil {
		t.Fatalf("ConfirmTransaction: %v", err)
	}
	if err := checkLiveHandoff(exec.Path); err != nil {
		t.Fatalf("checkLiveHandoff() with a confirmed receipt = %v; want nil", err)
	}
}
