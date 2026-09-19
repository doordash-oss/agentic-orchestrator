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
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func newProductionInstallTransactionFixture(t *testing.T, seams selfupdate.FileOps) *productionInstallTransaction {
	t.Helper()
	dir := t.TempDir()
	path := writeHooksTestExecutable(t, dir, "agentico", "installed-v1")
	exec, err := selfupdate.CaptureExecutableAt(path)
	if err != nil {
		t.Fatal(err)
	}
	candidate := writeHooksTestExecutable(t, dir, "candidate", "candidate-v2")
	digest, err := selfupdate.DigestFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir := t.TempDir()
	stateDir, configPath := filepath.Join(runtimeDir, "features"), filepath.Join(runtimeDir, "config.yaml")
	lease, err := selfupdate.AcquireLease(exec, selfupdate.OwnershipRecord{
		RuntimeDir:       runtimeDir,
		StateDir:         stateDir,
		Config:           configPath,
		PID:              os.Getpid(),
		Version:          "1.0.0",
		StartedAt:        time.Now(),
		ExecutablePath:   exec.Path,
		ExecutableDigest: exec.Digest,
		ExecutableIno:    exec.ID.Ino,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	tx, err := selfupdate.Begin(exec, selfupdate.BeginOptions{
		RuntimeDir:      runtimeDir,
		StateDir:        stateDir,
		Config:          configPath,
		PID:             os.Getpid(),
		FromVersion:     "1.0.0",
		ToVersion:       "2.0.0",
		CandidatePath:   candidate,
		CandidateDigest: digest,
		Bind:            selfupdate.BindEndpoint{Host: "127.0.0.1", Port: 54321, AdvertiseURL: "http://127.0.0.1:54321", Policy: "loopback"},
	}, seams)
	if err != nil {
		t.Fatal(err)
	}
	boot := &runtimeBootstrap{
		selfUpdateExec: exec,
		updateLease:    lease,
		runtime:        serverruntime.RuntimeIdentity{RuntimeDir: runtimeDir, StateDir: stateDir, Config: configPath},
		sessionManager: session.NewManager(make(chan interface{}, 100)),
	}
	t.Cleanup(boot.sessionManager.Shutdown)
	return &productionInstallTransaction{run: &serverRun{boot: boot}, tx: tx}
}

func TestProductionInstallCancelRetriesSettlementAfterRepair(t *testing.T) {
	tx := newProductionInstallTransactionFixture(t, selfupdate.FileOps{})
	path := selfupdate.ReceiptPath(tx.run.boot.selfUpdateExec.Path)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := tx.Cancel(); err == nil {
		t.Fatal("expected first attempt to fail")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := tx.Cancel(); err != nil {
		t.Fatalf("cancel after repairing filesystem still fails: %v", err)
	}
}

// Production replacement must release the coordinator's lock before either
// recovery path reacquires it. A failed exec is intercepted to keep the test
// process alive after verifying the recovery guard and restored bytes.
func TestProductionInstallRecoveryReleasesGuardedLock(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		kind := selfupdate.RecoveryKindRestart
		if replaced {
			kind = selfupdate.RecoveryKindRestore
		}
		t.Run(kind, func(t *testing.T) {
			tx := newProductionInstallTransactionFixture(t, selfupdate.FileOps{
				Rename: func(from, to string) error {
					if replaced {
						if err := os.Rename(from, to); err != nil {
							return err
						}
					}
					return errors.New("injected commit failure")
				},
			})
			lifecycle := &productionInstallLifecycle{exitCode: make(chan int, 1)}
			lifecycle.setRun(tx.run)
			release, ok := lifecycle.AcquireUpdateLock()
			if !ok {
				t.Fatal("acquire lock")
			}
			defer release()
			old := selfUpdateRecoveryExecFn
			t.Cleanup(func() { selfUpdateRecoveryExecFn = old })
			called := false
			selfUpdateRecoveryExecFn = func(path string, _ []string, env []string) error {
				called = true
				guard, present, err := selfupdate.ParseRecoveryGuardEnv(env)
				if err != nil || !present || guard.Kind != kind {
					t.Errorf("recovery guard = %+v, present=%v, error=%v", guard, present, err)
				}
				if path != tx.run.boot.selfUpdateExec.Path {
					t.Errorf("recovery exec path = %s", path)
				}
				return errors.New("exec intercepted")
			}
			stderr := captureStderrFn(func() { _ = lifecycle.Replace(tx, release, nil, nil) })
			if !called || strings.Contains(stderr, "runtime update lock held") {
				t.Fatalf("recovery never reaches exec while caller owns update lock: %s", stderr)
			}
			digest, err := selfupdate.DigestFile(tx.run.boot.selfUpdateExec.Path)
			if err != nil || digest != tx.tx.Receipt().OldDigest {
				t.Fatalf("recovered executable digest = %s, error = %v", digest, err)
			}
		})
	}
}

// Cleanup can remove recognized objects before discovering an unexpected
// entry. Retrying must finish cleanup without trying to settle the already
// abandoned receipt again or requiring the deleted backup bytes.
func TestProductionInstallCancelRetriesPartialCleanup(t *testing.T) {
	tx := newProductionInstallTransactionFixture(t, selfupdate.FileOps{})
	unexpected := filepath.Join(tx.tx.TxDir(), "retained-entry")
	if err := os.WriteFile(unexpected, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := tx.Cancel(); err == nil || !strings.Contains(err.Error(), "unexpected entries") {
		t.Fatalf("first cleanup error = %v", err)
	}
	receiptPath := selfupdate.ReceiptPath(tx.run.boot.selfUpdateExec.Path)
	before, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tx.tx.Receipt().BackupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup should have been cleaned: %v", err)
	}
	if err := os.Remove(unexpected); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := tx.Cancel(); err != nil {
			t.Fatalf("retry cleanup: %v", err)
		}
	}
	after, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("retry rewrote settled receipt")
	}
	if _, err := os.Stat(tx.tx.TxDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transaction dir remains: %v", err)
	}
}
