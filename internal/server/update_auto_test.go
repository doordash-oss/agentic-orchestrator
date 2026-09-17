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

package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
)

func autoUpdateOptions() UpdateOptions {
	opts := eligibleUpdateOptions()
	opts.Policy = selfupdate.PolicyAuto
	opts.Settings.Policy = selfupdate.PolicyAuto
	return opts
}

func installOpAuto(c *updateCoordinator) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.install == nil {
		return false, false
	}
	return true, c.install.auto
}

func TestAutoPolicyInstallsDiscoveredReleaseWhenIdle(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, autoUpdateOptions())
	idle := make(chan struct{})
	fixture.admission.setWaitIdleGate(idle, 1)
	fixture.lifecycle.setReplaceBlock(make(chan struct{}))
	fixture.discoverLatest(t)

	waitInstallCond(t, 5*time.Second, func() bool {
		return installOpStatus(fixture.handler.updates) == updateStatusScheduled
	}, "auto policy must schedule an idle install of the discovered release")
	if present, auto := installOpAuto(fixture.handler.updates); !present || !auto {
		t.Fatalf("operation present=%v auto=%v, want an automatic operation", present, auto)
	}
	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
	snapshot := decodeUpdateSnapshot(t, w.Result())
	if snapshot.Policy != UpdateSnapshotPolicy("auto") {
		t.Fatalf("policy = %q, want auto", snapshot.Policy)
	}
	if snapshot.Method == nil || *snapshot.Method != UpdateSnapshotMethodIdle {
		t.Fatalf("method = %v, want idle", snapshot.Method)
	}
	if snapshot.StopActiveWork == nil || *snapshot.StopActiveWork {
		t.Fatalf("stop_active_work = %v, want false", snapshot.StopActiveWork)
	}
	if snapshot.TargetVersion == nil || *snapshot.TargetVersion != "2.0.0" {
		t.Fatalf("target_version = %v, want 2.0.0", snapshot.TargetVersion)
	}
	if snapshot.ScheduledFor != nil {
		t.Fatalf("scheduled_for = %v, want null without a window", snapshot.ScheduledFor)
	}
	if fixture.lifecycle.replaceCallsN() != 0 {
		t.Fatal("replace must wait for idle")
	}

	close(idle)
	waitInstallCond(t, 5*time.Second, func() bool {
		return fixture.lifecycle.replaceCallsN() == 1
	}, "the automatic install must replace once the runtime is idle")
	waitInstallCond(t, 5*time.Second, func() bool {
		return installOpStatus(fixture.handler.updates) == updateStatusRestarting
	}, "the automatic install must reach restarting")
}

func TestAutoPolicyCancelledTargetWaitsForNewerRelease(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, autoUpdateOptions())
	fixture.stager.setBlock(make(chan struct{}))
	fixture.discoverLatest(t)
	waitInstallCond(t, 5*time.Second, func() bool {
		present, _ := installOpAuto(fixture.handler.updates)
		return present
	}, "auto policy must schedule the discovered release")

	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodDelete, []byte(`{}`)))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", w.Result().StatusCode)
	}
	fixture.discoverLatest(t)
	if present, _ := installOpAuto(fixture.handler.updates); present {
		t.Fatal("a cancelled automatic target must not be rescheduled for the same version")
	}

	fixture.feed.mu.Lock()
	fixture.feed.selection = selfupdate.ReleaseSelection{Version: "2.1.0", TagName: "v2.1.0"}
	fixture.feed.mu.Unlock()
	fixture.discoverLatest(t)
	waitInstallCond(t, 5*time.Second, func() bool {
		present, _ := installOpAuto(fixture.handler.updates)
		return present
	}, "a newer release must be scheduled again")
	fixture.handler.updates.mu.Lock()
	target := fixture.handler.updates.install.target.Version
	fixture.handler.updates.mu.Unlock()
	if target != "2.1.0" {
		t.Fatalf("target = %q, want 2.1.0", target)
	}
}

func TestAutoPolicyHonorsMaintenanceWindow(t *testing.T) {
	t.Parallel()
	clock := newFakeUpdateClock()
	now := clock.Now().Local()
	open := now.Add(2 * time.Hour)
	end := now.Add(4 * time.Hour)
	opts := autoUpdateOptions()
	opts.Settings.Window = fmt.Sprintf("%02d:%02d-%02d:%02d", open.Hour(), open.Minute(), end.Hour(), end.Minute())
	fixture := newInstallAPIFixture(t, opts)
	fixture.handler.updates.clock = clock
	fixture.discoverLatest(t)

	waitInstallCond(t, 5*time.Second, func() bool {
		return installOpStatus(fixture.handler.updates) == updateStatusScheduled
	}, "the automatic install must park as scheduled outside the window")
	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
	snapshot := decodeUpdateSnapshot(t, w.Result())
	if snapshot.ScheduledFor == nil || !snapshot.ScheduledFor.Equal(open) {
		t.Fatalf("scheduled_for = %v, want %v", snapshot.ScheduledFor, open)
	}
	if fixture.lifecycle.replaceCallsN() != 0 {
		t.Fatal("replace must wait for the window")
	}

	clock.Advance(2 * time.Hour)
	waitInstallCond(t, 5*time.Second, func() bool {
		return fixture.lifecycle.replaceCallsN() == 1
	}, "the automatic install must replace once the window opens")
}

func TestInstallRefusesIncompatibleServerContract(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
	fixture.stager.contract = &selfupdate.ServerContract{APIVersion: 1, SchemaVersion: CompatibilitySchemaVersion - 1, MinClientSchema: 0}
	fixture.discoverLatest(t)

	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
	if w.Result().StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Result().StatusCode)
	}
	waitInstallCond(t, 5*time.Second, func() bool {
		return installOpCleared(fixture.handler.updates)
	}, "the incompatible target must settle as failed")
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
	snapshot := decodeUpdateSnapshot(t, w.Result())
	if snapshot.Status != UpdateSnapshotStatusFailed || snapshot.Error == nil {
		t.Fatalf("status = %q error = %v, want failed with an error", snapshot.Status, snapshot.Error)
	}
	if !strings.Contains(snapshot.Error.Summary, "schema series") {
		t.Fatalf("error summary = %q, want the contract reason", snapshot.Error.Summary)
	}
	if fixture.lifecycle.beginCallsN() != 0 {
		t.Fatal("an incompatible contract must never begin a transaction")
	}
}
