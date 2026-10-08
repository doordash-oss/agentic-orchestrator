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

package e2e

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// idleInstallFeed always discovers one newer stable release.
type idleInstallFeed struct{}

func (idleInstallFeed) LatestStable(context.Context) (selfupdate.ReleaseSelection, error) {
	return selfupdate.ReleaseSelection{Version: "2.0.0", TagName: "v2.0.0"}, nil
}

// idleInstallStager stages a candidate at once, with nothing to clean.
type idleInstallStager struct{}

func (idleInstallStager) StageCandidate(_ context.Context, _ string, progress func(string)) (selfupdate.VerifiedCandidate, *selfupdate.ServerContract, func() error, error) {
	for _, stage := range []string{"resolve", "download", "verify", "probe", "admit"} {
		progress(stage)
	}
	return selfupdate.VerifiedCandidate{}, &selfupdate.ServerContract{APIVersion: 2, SchemaVersion: 3, MinClientSchema: 1}, func() error { return nil }, nil
}

type idleInstallTx struct{}

func (idleInstallTx) ID() string    { return "tx-idle-install" }
func (idleInstallTx) Cancel() error { return nil }

// idleInstallLifecycle stands in for the process-level replacement: its
// final drain runs the server's shutdown of the supervisor, as the real
// drain does before the commit's exec, then reports the commit and parks
// like an exec that never returns.
type idleInstallLifecycle struct {
	shutdown  func() error
	committed chan struct{}
	release   chan struct{}
}

func (l *idleInstallLifecycle) AcquireUpdateLock() (func(), bool) { return func() {}, true }

func (l *idleInstallLifecycle) Begin(selfupdate.VerifiedCandidate) (server.InstallTransaction, error) {
	return idleInstallTx{}, nil
}

func (l *idleInstallLifecycle) Replace(_ server.InstallTransaction, _ func(), notify func(string), _ <-chan struct{}) error {
	notify("restarting")
	_ = l.shutdown()
	close(l.committed)
	<-l.release
	return errors.New("the test ended before the replacement exec")
}

// update reads the update snapshot through the live handler.
func (h *supervisorHarness) update() server.UpdateSnapshot {
	h.t.Helper()
	var resp server.UpdateSnapshotResponse
	h.do(http.MethodGet, "/api/v1/update", nil, http.StatusOK, &resp)
	return resp.Update
}

// TestSupervisorIdleInstallProceedsWhileWaitingOnPermission is the
// install-when-idle journey through the live handler and fake Claude: a
// supervisor turn waiting on a permission is active work but does not hold
// up an idle install. The install commits, the server's shutdown resolves
// the open request as interrupted, and the relaunched server shows the
// conversation paused with the interrupted marker and no second
// resolution.
func TestSupervisorIdleInstallProceedsWhileWaitingOnPermission(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	lifecycle := &idleInstallLifecycle{committed: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(lifecycle.release) })
	h.updates = &server.UpdateOptions{
		Policy:         selfupdate.PolicyNotify,
		Settings:       selfupdate.StartupSettings{Policy: selfupdate.PolicyNotify, Channel: "stable", CheckInterval: 6 * time.Hour, Strategy: "idle"},
		CurrentVersion: "1.0.0",
		Eligibility:    selfupdate.Eligibility{Supported: true, Install: selfupdate.InstallTarball},
		Feed:           idleInstallFeed{},
		Stager:         idleInstallStager{},
		Install:        lifecycle,
		Admission:      h.admission,
	}
	// Serve through the runtime server, whose update scheduler runs the
	// check and the install worker.
	h.restart()
	lifecycle.shutdown = h.coord.Close

	h.chooseSettings()
	h.send("please "+testutil.FakeSupervisorPermBash, "w1")
	waiting := h.waitLifecycle(server.SupervisorLifecycleWaitingPermission)
	if len(waiting.PendingRequests) != 1 {
		t.Fatalf("pending = %+v", waiting.PendingRequests)
	}
	requestID := waiting.PendingRequests[0].RequestID
	if summary := h.update().ActiveWorkSummary; !summary.SupervisorActive || !summary.SupervisorWaiting {
		t.Fatalf("update summary while waiting = %+v, want supervisor_active and supervisor_waiting", summary)
	}

	stream := h.openStream("")

	h.do(http.MethodPost, "/api/v1/update/check", map[string]any{}, http.StatusAccepted, nil)
	deadline := time.Now().Add(10 * time.Second)
	for h.update().LatestVersion == nil {
		if time.Now().After(deadline) {
			t.Fatal("the check never discovered the release")
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.do(http.MethodPost, "/api/v1/update/install", map[string]any{"consent": true, "when": "idle"}, http.StatusAccepted, nil)
	select {
	case <-lifecycle.committed:
	case <-time.After(20 * time.Second):
		t.Fatalf("the idle install never committed while the supervisor waited; update = %+v", h.update())
	}

	// The server's own shutdown resolved the open request, ahead of the
	// stopped state, before the replacement booted.
	for _, ev := range stream.until("shutdown resolution", func(ev sseEvent) bool {
		rec := ev.data.Record
		return rec != nil && rec.Request != nil && rec.Request.RequestID == requestID &&
			rec.Request.Stage == server.SupervisorRequestStageResolved
	}) {
		if isState(server.SupervisorLifecycleStopped)(ev) {
			t.Fatal("stopped was published before the open request was resolved")
		}
	}

	// Relaunch over the same state, as the replacement binary boots.
	h.stopServer()
	h.updates = nil
	h.admission = workadmission.New(workadmission.Options{})
	h.start()
	st := h.state()
	if st.Lifecycle != server.SupervisorLifecycleStopped || st.LastTurnOutcome != server.SupervisorTurnOutcomeInterrupted ||
		st.InterruptedBy != server.SupervisorInterruptedByShutdown || len(st.PendingRequests) != 0 {
		t.Fatalf("state after relaunch = %+v, want paused after restart", st)
	}
	page := h.transcript("?limit=500")
	var resolved []server.SupervisorRecord
	for _, rec := range page.Items {
		if rec.Request != nil && rec.Request.RequestID == requestID && rec.Request.Stage == server.SupervisorRequestStageResolved {
			resolved = append(resolved, rec)
		}
	}
	if len(resolved) != 1 || resolved[0].Request.Outcome != server.SupervisorRequestOutcomeInterrupted {
		t.Fatalf("resolutions of %s = %+v, want exactly one interrupted", requestID, resolved)
	}
	markers := markerRecords(page, server.SupervisorMarkerInterrupted)
	if len(markers) != 1 || markers[0].Marker.Text != "Interrupted before restart" || markers[0].TurnID != resolved[0].TurnID {
		t.Fatalf("interrupted markers = %+v, want one on the cut turn %q", markers, resolved[0].TurnID)
	}
	if markers[0].Seq < resolved[0].Seq {
		t.Fatalf("the boot marker #%d precedes the shutdown resolution #%d", markers[0].Seq, resolved[0].Seq)
	}
}
