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

package clone

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
)

// crashFixture builds a service over records crafted to look like a crash
// survivor: no live workers, durable records and staging exactly as they
// would have been left mid-flight.
type crashFixture struct {
	*serviceFixture
}

func newCrashFixture(t *testing.T) *crashFixture {
	t.Helper()
	fx := &crashFixture{newServiceFixture(t, succeedScript)}
	// The fixture's service must not own live workers: rebuild it without
	// spawning so records are crafted by hand below.
	return fx
}

// craftRecord writes a record (and its staging with a valid ownership
// marker) directly into the store directory, as a crash would have left
// it, and returns it.
func (fx *crashFixture) craftRecord(t *testing.T, id, key, dest string, state State, process *ProcessInfo, cancelRequested bool) Record {
	t.Helper()
	stagingRel := stagingName(id)
	stagingPath := filepath.Join(fx.root, stagingRel)
	if err := os.MkdirAll(stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	handle, _, err := OpenRootDir(fx.root)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	nonce := newNonce()
	_, idty, err := OpenRootDir(fx.root)
	if err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(ownershipMarker{
		OperationID: id, Nonce: nonce,
		RootDevice: idty.Device, RootInode: idty.Inode,
		CreatedAt: time.Now().UTC(),
	})
	if err := os.WriteFile(filepath.Join(stagingPath, ownershipMarkerName), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rec := Record{
		ID: id, IdempotencyKey: key, InputFingerprint: "fp-" + key,
		RemoteURL: "https://example.com/acme/widget.git",
		RootPath:  fx.root, RootResolved: fx.root,
		RootDevice: idty.Device, RootInode: idty.Inode,
		Destination: dest, DestinationPath: filepath.Join(fx.root, dest),
		StagingPath: stagingPath, OwnershipNonce: nonce,
		State: state, Stage: StageTransferring,
		CancelRequested: cancelRequested,
		Process:         process,
		CreatedAt:       now, UpdatedAt: now,
	}
	if err := fx.svc.save(&rec); err != nil {
		t.Fatalf("craft record: %v", err)
	}
	return rec
}

// spawnSurvivor spawns a real long-lived child process in its own process
// group and returns its recorded process info. The child is reaped
// asynchronously so a terminated survivor disappears instead of turning
// into a zombie that still answers kill(pid, 0).
func spawnSurvivor(t *testing.T) *ProcessInfo {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn survivor: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	})
	return &ProcessInfo{
		Pid:           cmd.Process.Pid,
		Pgid:          cmd.Process.Pid,
		StartIdentity: processStartIdentity(cmd.Process.Pid),
		StartedAt:     time.Now().UTC(),
	}
}

func TestRecoverInterruptsUnfinishedWorkWithoutRespawning(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	rec := fx.craftRecord(t, "clone-running", "k-run", "widget", StateRunning, nil, false)

	if err := fx.svc.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	final, _ := fx.svc.Snapshot(rec.ID)
	if final.State != StateInterrupted {
		t.Fatalf("state = %s, want interrupted", final.State)
	}
	if _, err := os.Lstat(final.StagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("interrupted attempt kept staging: %v", err)
	}
	if final.ResolvedAt.IsZero() {
		t.Error("interrupted record not resolved")
	}
	// No new clone was launched.
	if len(fx.runner.handles()) != 0 {
		t.Errorf("recovery spawned a clone: %d handles", len(fx.runner.handles()))
	}
	// Repeated startup reconciliation is idempotent.
	if err := fx.svc.Recover(); err != nil {
		t.Fatalf("second Recover: %v", err)
	}
	again, _ := fx.svc.Snapshot(rec.ID)
	if again.State != StateInterrupted || !again.ResolvedAt.Equal(final.ResolvedAt) {
		t.Errorf("second reconciliation changed the record: %+v", again)
	}
}

func TestRecoverKeepsExplicitCancellationDistinguishable(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	rec := fx.craftRecord(t, "clone-cancelling", "k-cancel", "widget", StateCancelling, nil, true)

	if err := fx.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	final, _ := fx.svc.Snapshot(rec.ID)
	if final.State != StateCancelled {
		t.Fatalf("state = %s, want cancelled (not interrupted)", final.State)
	}
}

func TestRecoverAcceptedBeforeSpawn(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	rec := fx.craftRecord(t, "clone-accepted", "k-accepted", "widget", StateAccepted, nil, false)
	if err := fx.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	final, _ := fx.svc.Snapshot(rec.ID)
	if final.State != StateInterrupted {
		t.Fatalf("state = %s, want interrupted", final.State)
	}
}

func TestRecoverRecognizesPublicationAfterCrash(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	// A crash after atomic publication but before recording success: the
	// destination holds our publication evidence.
	dest := filepath.Join(fx.root, "widget")
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := fx.craftRecord(t, "clone-finalizing", "k-fin", "widget", StateFinalizing, nil, false)
	pubMarker, _ := json.Marshal(publicationMarker{OperationID: rec.ID, Nonce: rec.OwnershipNonce, PublishedAt: time.Now().UTC()})
	if err := os.WriteFile(filepath.Join(dest, ".git", publicationMarkerName), pubMarker, 0o600); err != nil {
		t.Fatal(err)
	}
	// Staging is already gone (renamed away).
	_ = os.RemoveAll(rec.StagingPath)

	if err := fx.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	final, _ := fx.svc.Snapshot(rec.ID)
	if final.State != StateSucceeded {
		t.Fatalf("state = %s, want succeeded from publication evidence", final.State)
	}
	if final.Published == nil || final.Published.RepoKey != "widget" {
		t.Errorf("publication identity = %+v", final.Published)
	}
}

func TestRecoverDoesNotClaimUnrelatedDestinationAsSuccess(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	// An unrelated repository merely occupying the destination: no marker
	// with our nonce.
	dest := filepath.Join(fx.root, "widget")
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := fx.craftRecord(t, "clone-finalizing-foreign", "k-foreign", "widget", StateFinalizing, nil, false)
	_ = os.RemoveAll(rec.StagingPath)

	if err := fx.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	final, _ := fx.svc.Snapshot(rec.ID)
	if final.State == StateSucceeded {
		t.Fatal("unrelated destination repository reported as this attempt's success")
	}
	if final.State != StateInterrupted && final.State != StateCleanupPending {
		t.Fatalf("state = %s, want interrupted or cleanup-pending", final.State)
	}
	// The unrelated repository was never a cleanup target.
	if _, err := os.Stat(filepath.Join(dest, ".git", "HEAD")); err != nil {
		t.Errorf("unrelated destination damaged: %v", err)
	}
}

func TestRecoverTerminatesVerifiedSurvivingProcess(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	survivor := spawnSurvivor(t)
	rec := fx.craftRecord(t, "clone-survivor", "k-survivor", "widget", StateRunning, survivor, false)

	if err := fx.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	if IsProcessAlive(survivor.Pid) {
		t.Error("verified surviving process was not terminated")
	}
	final, _ := fx.svc.Snapshot(rec.ID)
	if final.State != StateInterrupted {
		t.Fatalf("state = %s, want interrupted", final.State)
	}
}

func TestRecoverRefusesToSignalOnPIDReuse(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	survivor := spawnSurvivor(t)
	// A record whose recorded start identity does not match the live
	// process: PID reuse. Nothing may be signaled or deleted.
	stale := &ProcessInfo{Pid: survivor.Pid, Pgid: survivor.Pgid, StartIdentity: "reused-identity", StartedAt: time.Now().UTC()}
	rec := fx.craftRecord(t, "clone-reused", "k-reused", "widget", StateRunning, stale, false)

	if err := fx.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	if !IsProcessAlive(survivor.Pid) {
		t.Error("unverifiable process was signaled despite PID-reuse uncertainty")
	}
	final, _ := fx.svc.Snapshot(rec.ID)
	if final.State != StateCleanupPending {
		t.Fatalf("state = %s, want cleanup-pending", final.State)
	}
	if !strings.Contains(final.CleanupIssue, "process") {
		t.Errorf("cleanup issue = %q, want process uncertainty reason", final.CleanupIssue)
	}
	// Staging is retained (unresolved) and the reservation survives.
	if _, err := os.Lstat(final.StagingPath); err != nil {
		t.Errorf("staging deleted under uncertainty: %v", err)
	}
}

func TestRecoverRootSwapKeepsChangedOwnershipUntouched(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	rec := fx.craftRecord(t, "clone-swap", "k-swap", "widget", StateRunning, nil, false)
	// Replace the configured root path with a symlink to a different tree
	// after the record was created: identity no longer matches.
	other := t.TempDir()
	moved := filepath.Join(fx.root + ".moved")
	if err := os.Rename(fx.root, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(moved, fx.root) })
	if err := os.Symlink(other, fx.root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(fx.root) })

	if err := fx.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	final, _ := fx.svc.Snapshot(rec.ID)
	if final.State != StateCleanupPending {
		t.Fatalf("state = %s, want cleanup-pending after root identity change", final.State)
	}
	// The moved real root (with our staging inside) was never touched.
	if _, err := os.Lstat(filepath.Join(moved, stagingName(rec.ID))); err != nil {
		t.Errorf("changed-ownership staging deleted: %v", err)
	}
}

func TestRecoverCleanupPendingResolvesThroughSameBoundary(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	rec := fx.craftRecord(t, "clone-pending", "k-pending", "widget", StateCleanupPending, nil, false)
	pending := rec
	pending.PendingOutcome = StateFailed
	if err := fx.svc.save(&pending); err != nil {
		t.Fatal(err)
	}

	if err := fx.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	final, _ := fx.svc.Snapshot(rec.ID)
	if final.State != StateFailed {
		t.Fatalf("state = %s, want failed after startup cleanup reconciliation", final.State)
	}
	if _, err := os.Lstat(final.StagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging not removed: %v", err)
	}
}

func TestRecoverLateSuccessThroughCleanupEndpoint(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	rec := fx.craftRecord(t, "clone-late", "k-late", "widget", StateCleanupPending, nil, false)
	pending := rec
	pending.PendingOutcome = StateFailed
	if err := fx.svc.save(&pending); err != nil {
		t.Fatal(err)
	}
	// The attempt actually published before the failure was recorded: the
	// destination carries our publication evidence.
	dest := filepath.Join(fx.root, "widget")
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	pubMarker, _ := json.Marshal(publicationMarker{OperationID: rec.ID, Nonce: rec.OwnershipNonce, PublishedAt: time.Now().UTC()})
	if err := os.WriteFile(filepath.Join(dest, ".git", publicationMarkerName), pubMarker, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(rec.StagingPath)

	resolved, err := fx.svc.Cleanup(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != StateSucceeded {
		t.Fatalf("late success not recognized: state = %s", resolved.State)
	}
}

func TestRecoverPersistenceFailureLeavesTruthfulUnresolvedRecord(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	rec := fx.craftRecord(t, "clone-persist", "k-persist", "widget", StateRunning, nil, false)
	storeDir := filepath.Join(fx.stateDir, StoreDirName)
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })

	if err := fx.svc.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	final, _ := fx.svc.Snapshot(rec.ID)
	if final.State == StateInterrupted || final.State == StateSucceeded {
		t.Fatalf("recovery claimed %s despite persistence failure", final.State)
	}
	// Restore write access and reconcile again: it converges.
	_ = os.Chmod(storeDir, 0o700)
	if err := fx.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	after, _ := fx.svc.Snapshot(rec.ID)
	if after.State != StateInterrupted {
		t.Errorf("state = %s after retry, want interrupted", after.State)
	}
}

func TestSameKeyReplayAcrossRestartReturnsSucceededOperation(t *testing.T) {
	t.Parallel()
	fx := newServiceFixture(t, succeedScript)
	rec := fx.start("key-restart")
	fx.waitForState(rec.ID, StateSucceeded)

	// Simulate a restart: a fresh service over the same state dir.
	svc2, err := New(Options{
		StateDir: fx.stateDir,
		Config:   func() *config.Config { return fx.cfg },
		Runner:   newFakeRunner(succeedScript),
		Hooks:    Hooks{},
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := svc2.Start(context.Background(), fx.startInput("key-restart"))
	if err != nil {
		t.Fatalf("same-key replay after restart: %v", err)
	}
	if replayed.ID != rec.ID || replayed.State != StateSucceeded {
		t.Errorf("replay = %+v, want the retained succeeded operation", replayed)
	}
}

func TestCancelOnRecoveredOperationWithoutWorker(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	survivor := spawnSurvivor(t)
	rec := fx.craftRecord(t, "clone-recovered-cancel", "k-rc", "widget", StateRunning, survivor, false)

	cancelling, err := fx.svc.Cancel(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelling.State != StateCancelling {
		t.Fatalf("state = %s, want cancelling", cancelling.State)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		final, _ := fx.svc.Snapshot(rec.ID)
		if final.State == StateCancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered cancel never finished; state %s", final.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if IsProcessAlive(survivor.Pid) {
		t.Error("survivor still alive after recovered cancellation")
	}
}
