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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// admitFixture wires a work-admission coordinator into a standard fixture.
// The coordinator is attached after construction like the other retargetable
// fixture knobs; no work has been admitted yet when it returns.
func admitFixture(t *testing.T, script func(h *fakeHandle)) (*serviceFixture, *workadmission.Coordinator) {
	t.Helper()
	fx := newServiceFixture(t, script)
	coord := workadmission.New(workadmission.Options{})
	fx.svc.opts.Admission = coord
	return fx, coord
}

// cloneHeld asserts every held reservation belongs to the clone category and
// returns the total.
func cloneHeld(t *testing.T, coord *workadmission.Coordinator) int {
	t.Helper()
	total, per := coord.Held()
	if n := per[workadmission.CategoryClone]; n != total {
		t.Fatalf("held = %d total but only %d clone reservations, want every reservation in the clone category", total, n)
	}
	return total
}

// Admission releases after publishing the terminal record, under the operation
// lock. Wait for that critical section before asserting the settled reservation.
func waitForAdmissionSettlement(fx *serviceFixture, id string, state State) {
	fx.t.Helper()
	fx.waitForState(id, state)
	op := fx.svc.op(id)
	op.mu.Lock()
	op.mu.Unlock()
}

func TestAdmissionReservationHeldFromStartUntilTerminalSettle(t *testing.T) {
	t.Parallel()
	spawnGate := make(chan struct{})
	fx, coord := admitFixture(t, succeedScript)
	fx.runner.setOnStart(func(spec RunSpec) { <-spawnGate })

	rec := fx.start("key-admit")
	if n := cloneHeld(t, coord); n != 1 {
		t.Fatalf("held = %d after start, want 1", n)
	}
	close(spawnGate)
	waitForAdmissionSettlement(fx, rec.ID, StateSucceeded)
	if n := cloneHeld(t, coord); n != 0 {
		t.Fatalf("held = %d after terminal settle, want 0", n)
	}
}

func TestAdmissionReservationReleasesOnFailedSettle(t *testing.T) {
	t.Parallel()
	fx, coord := admitFixture(t, failAuthScript)
	rec := fx.start("key-admit-fail")
	waitForAdmissionSettlement(fx, rec.ID, StateFailed)
	if n := cloneHeld(t, coord); n != 0 {
		t.Fatalf("held = %d after failed settle, want 0", n)
	}
}

func TestAdmissionHeldThroughCleanupPendingUntilReconciliationResolves(t *testing.T) {
	t.Parallel()
	fx, coord := admitFixture(t, hangScript)
	rec := fx.start("key-admit-pending")
	fx.waitForState(rec.ID, StateRunning)

	// A root that stops being clone-eligible makes owned cleanup unprovable
	// deterministically: the attempt must stay cleanup-pending.
	if err := os.MkdirAll(filepath.Join(fx.root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.Cancel(rec.ID); err != nil {
		t.Fatal(err)
	}
	pending := fx.waitForState(rec.ID, StateCleanupPending)
	if pending.PendingOutcome != StateCancelled {
		t.Fatalf("pending outcome = %s, want cancelled", pending.PendingOutcome)
	}
	if n := cloneHeld(t, coord); n != 1 {
		t.Fatalf("held = %d while cleanup pending, want 1 (cleanup has not settled)", n)
	}
	if got := fx.svc.ActiveWork(); got != 1 {
		t.Fatalf("ActiveWork = %d while cleanup pending, want 1", got)
	}

	// The scripted worker's recorded pid is not a real process tree; drop
	// it as a crash before the process bookkeeping settled would, so the
	// cleanup retry below is deterministic.
	snap, err := fx.svc.Snapshot(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	snap.Process = nil
	if err := fx.svc.save(&snap); err != nil {
		t.Fatal(err)
	}

	// Eligibility restored: cleanup reconciliation finally succeeds and the
	// reservation releases with the terminal settle.
	if err := os.RemoveAll(filepath.Join(fx.root, ".git")); err != nil {
		t.Fatal(err)
	}
	resolved, err := fx.svc.Cleanup(rec.ID)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if resolved.State != StateCancelled {
		t.Fatalf("resolved state = %s, want cancelled", resolved.State)
	}
	if n := cloneHeld(t, coord); n != 0 {
		t.Fatalf("held = %d after cleanup reconciliation resolved, want 0", n)
	}
	if got := fx.svc.ActiveWork(); got != 0 {
		t.Fatalf("ActiveWork = %d after cleanup reconciliation, want 0", got)
	}
	// A re-driven cleanup cannot double-release or re-hold.
	if _, err := fx.svc.Cleanup(rec.ID); err != nil {
		t.Fatal(err)
	}
	if n := cloneHeld(t, coord); n != 0 {
		t.Fatalf("held = %d after repeated cleanup, want 0", n)
	}
}

func TestAdmissionValidationFailureReleasesReservation(t *testing.T) {
	t.Parallel()
	fx, coord := admitFixture(t, succeedScript)
	if err := os.MkdirAll(filepath.Join(fx.root, "taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		input func() StartInput
	}{
		{"invalid remote", func() StartInput {
			in := fx.startInput("key-bad-remote")
			in.Remote = "/srv/git/widget.git"
			return in
		}},
		{"separator destination", func() StartInput {
			in := fx.startInput("key-bad-dest")
			in.Destination = "sub/dir"
			return in
		}},
		{"empty idempotency key", func() StartInput {
			return fx.startInput("")
		}},
		{"ineligible root", func() StartInput {
			in := fx.startInput("key-bad-root")
			in.Root = "/nonexistent-agentico-root"
			return in
		}},
		{"occupied destination", func() StartInput {
			in := fx.startInput("key-occupied")
			in.Destination = "taken"
			return in
		}},
	}
	// Sequential: each request fully settles (including its deferred
	// release) before the next observes the coordinator.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := fx.svc.Start(context.Background(), tc.input()); err == nil {
				t.Fatal("invalid request accepted")
			}
			if n := cloneHeld(t, coord); n != 0 {
				t.Fatalf("held = %d after rejected request, want 0", n)
			}
		})
	}
}

func TestAdmissionReplayDoesNotLeakOrDoubleHold(t *testing.T) {
	t.Parallel()
	fx, coord := admitFixture(t, succeedScript)
	rec := fx.start("key-replay")
	waitForAdmissionSettlement(fx, rec.ID, StateSucceeded)
	if n := cloneHeld(t, coord); n != 0 {
		t.Fatalf("held = %d after settle, want 0", n)
	}

	// Same key, same input: the retained record replays; the request
	// releases its own reservation and never doubles onto the operation.
	again, err := fx.svc.Start(context.Background(), fx.startInput("key-replay"))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != rec.ID {
		t.Fatalf("replay created %s, want %s", again.ID, rec.ID)
	}
	if n := cloneHeld(t, coord); n != 0 {
		t.Fatalf("held = %d after replay, want 0", n)
	}
	if len(fx.runner.handles()) != 1 {
		t.Fatalf("replay spawned another clone: %d handles", len(fx.runner.handles()))
	}

	// Same key, changed input: the conflict releases too.
	changed := fx.startInput("key-replay")
	changed.Destination = "elsewhere"
	if _, err := fx.svc.Start(context.Background(), changed); err == nil {
		t.Fatal("changed-input reuse accepted")
	}
	if n := cloneHeld(t, coord); n != 0 {
		t.Fatalf("held = %d after changed-input conflict, want 0", n)
	}

	// A replay of a still-live operation holds nothing extra: the live
	// operation already owns its one reservation.
	fxLive, coordLive := admitFixture(t, hangScript)
	live := fxLive.start("key-live")
	fxLive.waitForState(live.ID, StateRunning)
	replayedLive, err := fxLive.svc.Start(context.Background(), fxLive.startInput("key-live"))
	if err != nil {
		t.Fatal(err)
	}
	if replayedLive.ID != live.ID {
		t.Fatalf("live replay created %s, want %s", replayedLive.ID, live.ID)
	}
	if n := cloneHeld(t, coordLive); n != 1 {
		t.Fatalf("held = %d after live replay, want 1 (the live operation's own)", n)
	}
	if _, err := fxLive.svc.Cancel(live.ID); err != nil {
		t.Fatal(err)
	}
	fxLive.waitForState(live.ID, StateCancelled)
	if n := cloneHeld(t, coordLive); n != 0 {
		t.Fatalf("held = %d after live settle, want 0", n)
	}
}

func TestAdmissionCreateHeldUntilSynchronousSettle(t *testing.T) {
	t.Parallel()
	fx, coord := admitFixture(t, succeedScript)
	release := make(chan struct{})
	fx.createGit = blockingCreateGit(release)
	createDone := make(chan struct{})
	go func() {
		defer close(createDone)
		rec, err := fx.svc.Create(context.Background(), fx.createInput("key-admit-create", "fresh"))
		if err != nil {
			fx.t.Errorf("Create: %v", err)
			return
		}
		if rec.State != StateSucceeded {
			fx.t.Errorf("blocked create ended as %s", rec.State)
		}
	}()
	waitForCreateRunning(t, fx, "fresh")
	if n := cloneHeld(t, coord); n != 1 {
		t.Fatalf("held = %d while create runs, want 1", n)
	}
	close(release)
	<-createDone
	if n := cloneHeld(t, coord); n != 0 {
		t.Fatalf("held = %d after create settled, want 0", n)
	}
}

func TestStartRefusedWhenAdmissionClosed(t *testing.T) {
	t.Parallel()
	fx, coord := admitFixture(t, succeedScript)
	if !coord.CloseIfQuiesced() {
		t.Fatal("admission did not close while idle")
	}

	_, err := fx.svc.Start(context.Background(), fx.startInput("key-closed"))
	closed, ok := workadmission.AsClosed(err)
	if !ok {
		t.Fatalf("start error = %v, want *workadmission.ClosedError", err)
	}
	if closed.Category != workadmission.CategoryClone {
		t.Errorf("closed category = %s, want %s", closed.Category, workadmission.CategoryClone)
	}

	// The create boundary refuses through the same category.
	_, err = fx.svc.Create(context.Background(), fx.createInput("key-closed-create", "fresh"))
	if _, ok := workadmission.AsClosed(err); !ok {
		t.Fatalf("create error = %v, want *workadmission.ClosedError", err)
	}
	if n := cloneHeld(t, coord); n != 0 {
		t.Fatalf("held = %d after refusals, want 0", n)
	}

	// Reopening admits work again, and it settles normally.
	coord.Open()
	rec := fx.start("key-reopened")
	waitForAdmissionSettlement(fx, rec.ID, StateSucceeded)
	if n := cloneHeld(t, coord); n != 0 {
		t.Fatalf("held = %d after reopened start settled, want 0", n)
	}
}

func TestRecoverRunsWhenAdmissionClosedAndSweepWaits(t *testing.T) {
	t.Parallel()
	fx := newCrashFixture(t)
	coord := workadmission.New(workadmission.Options{})
	fx.svc.opts.Admission = coord
	running := fx.craftRecord(t, "clone-adm-run", "k-adm-run", "widget", StateRunning, nil, false)
	stale := fx.craftRecord(t, "clone-adm-old", "k-adm-old", "stale", StateFailed, nil, false)
	stale.ResolvedAt = time.Now().UTC().Add(-(RetentionWindow + time.Hour))
	if err := fx.svc.save(&stale); err != nil {
		t.Fatal(err)
	}
	if got := fx.svc.ActiveWork(); got != 1 {
		t.Fatalf("ActiveWork = %d with one crafted running record, want 1", got)
	}

	if !coord.CloseIfQuiesced() {
		t.Fatal("admission did not close while idle")
	}

	// Startup reconciliation owns the durable records: it runs even when
	// acquisition is refused, recovering unfinished work truthfully.
	if err := fx.svc.Recover(); err != nil {
		t.Fatalf("Recover under closed admission: %v", err)
	}
	final, err := fx.svc.Snapshot(running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != StateInterrupted {
		t.Fatalf("recovered state = %s, want interrupted", final.State)
	}
	if got := fx.svc.ActiveWork(); got != 0 {
		t.Fatalf("ActiveWork = %d after recovery, want 0", got)
	}

	// The periodic sweep skips itself behind a closed boundary and waits
	// for a later tick.
	if got := fx.svc.Sweep(); got != nil {
		t.Fatalf("Sweep pruned %v behind a closed boundary, want none", got)
	}
	if _, err := fx.svc.Snapshot(stale.ID); err != nil {
		t.Fatalf("prunable record removed while sweep skipped: %v", err)
	}

	// Once admission reopens, the periodic pass runs again.
	coord.Open()
	if got := fx.svc.Sweep(); len(got) != 1 || got[0] != stale.ID {
		t.Fatalf("Sweep after reopen = %v, want [%s]", got, stale.ID)
	}
	if _, err := fx.svc.Snapshot(stale.ID); err == nil {
		t.Fatal("stale record not pruned after reopen")
	}
}

func TestActiveWorkCountsActiveAndCleanupPendingNotTerminal(t *testing.T) {
	t.Parallel()
	fx, _ := admitFixture(t, hangScript)
	if got := fx.svc.ActiveWork(); got != 0 {
		t.Fatalf("ActiveWork = %d on an idle service, want 0", got)
	}
	rec := fx.start("key-count")
	fx.waitForState(rec.ID, StateRunning)
	if got := fx.svc.ActiveWork(); got != 1 {
		t.Fatalf("ActiveWork = %d while running, want 1", got)
	}
	if _, err := fx.svc.Cancel(rec.ID); err != nil {
		t.Fatal(err)
	}
	fx.waitForState(rec.ID, StateCancelled)
	// The terminal record is retained in the store yet never counts.
	if got := fx.svc.ActiveWork(); got != 0 {
		t.Fatalf("ActiveWork = %d after terminal settle, want 0", got)
	}

	fxDone := newServiceFixture(t, succeedScript)
	done := fxDone.start("key-count-done")
	fxDone.waitForState(done.ID, StateSucceeded)
	if got := fxDone.svc.ActiveWork(); got != 0 {
		t.Fatalf("ActiveWork = %d with a retained succeeded record, want 0", got)
	}
}
