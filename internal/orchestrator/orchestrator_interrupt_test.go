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

package orchestrator_test

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ---------------------------------------------------------------------------
// TestOrchestrator_InterruptFeature
// ---------------------------------------------------------------------------

func TestOrchestrator_InterruptFeature(t *testing.T) {
	kbStatus := map[string]string{"repo-a": "building", "repo-b": "completed"}
	f := &feature.Feature{
		ID:       "feat-int",
		Status:   feature.StatusImplementing,
		Pipeline: feature.PipelineLarge,
		HelpQueue: []feature.HelpRequest{
			{Question: "q1", Pending: true},
			{Question: "q2", Pending: false},
		},
		PermissionsQueue: []feature.PermissionRequest{
			{Tool: "t1", Pending: true},
		},
		KBStatus: kbStatus,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	sm := mocks.NewMockSessionManager()
	sessions := []session.SessionView{
		mocks.NewMockSessionView("s-1", "feat-int"),
		mocks.NewMockSessionView("s-2", "feat-int"),
	}
	sm.FeatureSessionsFn = func(id string) []session.SessionView { return sessions }

	var hookFeatureID string
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Sessions:  sm,
	}, orchestrator.Hooks{
		OnFeatureInterrupted: func(id string) { hookFeatureID = id },
	})

	if err := o.InterruptFeature("feat-int"); err != nil {
		t.Fatalf("InterruptFeature: %v", err)
	}

	// Sessions stopped.
	if len(sm.StopCalls) != 2 {
		t.Errorf("StopSession calls = %d, want 2", len(sm.StopCalls))
	}

	// Pending flags cleared on feature via Modify.
	for _, h := range f.HelpQueue {
		if h.Pending {
			t.Errorf("HelpQueue entry still pending: %+v", h)
		}
	}
	for _, p := range f.PermissionsQueue {
		if p.Pending {
			t.Errorf("PermissionsQueue entry still pending: %+v", p)
		}
	}

	// KBStatus preserved (NOT cleared by InterruptFeature).
	if f.KBStatus == nil {
		t.Error("KBStatus was cleared by InterruptFeature — should be preserved")
	}
	if f.KBStatus["repo-a"] != "building" {
		t.Errorf("KBStatus[repo-a] = %q, want building", f.KBStatus["repo-a"])
	}

	// Transition called with StatusInterrupted.
	call := assertLifecycleCall(t, lc, "Transition")
	if call != nil && len(call.Args) >= 2 {
		if status, ok := call.Args[1].(feature.Status); ok {
			if status != feature.StatusInterrupted {
				t.Errorf("Transition status = %v, want StatusInterrupted", status)
			}
		}
	}

	if hookFeatureID != "feat-int" {
		t.Errorf("OnFeatureInterrupted got ID %q, want %q", hookFeatureID, "feat-int")
	}

	events := drainEvents(o)
	if !hasEventType(events, ports.FeatureInterrupted) {
		t.Error("expected FeatureInterrupted event")
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_InterruptFeature_NoSessions
// ---------------------------------------------------------------------------

func TestOrchestrator_InterruptFeature_NoSessions(t *testing.T) {
	f := &feature.Feature{ID: "feat-ns", Status: feature.StatusImplementing}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	sm := mocks.NewMockSessionManager()
	// FeatureSessionsFn default returns nil.

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Sessions:  sm,
	}, orchestrator.Hooks{})

	if err := o.InterruptFeature("feat-ns"); err != nil {
		t.Fatalf("InterruptFeature: %v", err)
	}
	if len(sm.StopCalls) != 0 {
		t.Errorf("StopSession should not be called with no sessions; got %d", len(sm.StopCalls))
	}
	assertLifecycleCall(t, lc, "Transition")
}

// ---------------------------------------------------------------------------
// TestOrchestrator_InterruptAllRunning
// ---------------------------------------------------------------------------

func TestOrchestrator_InterruptAllRunning(t *testing.T) {
	running1 := &feature.Feature{
		ID:       "running-1",
		Status:   feature.StatusImplementing,
		KBStatus: map[string]string{"r": "building"},
	}
	running2 := &feature.Feature{
		ID:       "running-2",
		Status:   feature.StatusPlanning,
		KBStatus: map[string]string{"r": "completed"},
	}
	published := &feature.Feature{
		ID:       "pub-1",
		Status:   feature.StatusPublished,
		KBStatus: map[string]string{"r": "completed"},
	}
	done := &feature.Feature{ID: "done-1", Status: feature.StatusDone}

	fs := newFeatureStore(running1, running2, published, done)
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = func(id string) (*feature.Feature, error) {
		switch id {
		case "running-1":
			return running1, nil
		case "running-2":
			return running2, nil
		case "pub-1":
			return published, nil
		case "done-1":
			return done, nil
		}
		return nil, nil
	}
	lc.TransitionFn = func(id string, to feature.Status) error {
		switch id {
		case "running-1":
			running1.Status = to
		case "running-2":
			running2.Status = to
		}
		return nil
	}

	sm := mocks.NewMockSessionManager()
	sm.FeatureSessionsFn = func(id string) []session.SessionView { return nil }

	interrupts := 0
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Sessions:  sm,
	}, orchestrator.Hooks{
		OnFeatureInterrupted: func(id string) { interrupts++ },
	})

	if err := o.InterruptAllRunning(); err != nil {
		t.Fatalf("InterruptAllRunning: %v", err)
	}

	if interrupts != 2 {
		t.Errorf("OnFeatureInterrupted calls = %d, want 2 (only running features)", interrupts)
	}

	// Running features KBStatus cleared (startup-sweep parity).
	if running1.KBStatus != nil {
		t.Errorf("running1.KBStatus should be nil after InterruptAllRunning, got %v", running1.KBStatus)
	}
	if running2.KBStatus != nil {
		t.Errorf("running2.KBStatus should be nil after InterruptAllRunning, got %v", running2.KBStatus)
	}

	// Published feature KBStatus NOT cleared.
	if published.KBStatus == nil {
		t.Error("published.KBStatus should NOT be cleared for published features")
	}

	// Done feature untouched.
	if done.Status != feature.StatusDone {
		t.Error("done feature status changed unexpectedly")
	}

	events := drainEvents(o)
	interruptEvents := 0
	for _, ev := range events {
		if ev.Type == ports.FeatureInterrupted {
			interruptEvents++
		}
	}
	if interruptEvents != 2 {
		t.Errorf("FeatureInterrupted events = %d, want 2", interruptEvents)
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_InterruptAllRunning_PublishedWithCycles
// ---------------------------------------------------------------------------

func TestOrchestrator_InterruptAllRunning_PublishedWithCycles(t *testing.T) {
	kb := map[string]string{"r": "completed"}
	published := &feature.Feature{
		ID:       "pub-cyc",
		Status:   feature.StatusPublished,
		KBStatus: kb,
		RepoCycles: map[string]*feature.RepoCycleState{
			"r1": {Type: feature.CycleRebase, Status: "running"},
			"r2": {Type: feature.CycleTweak, Status: "reviewing"},
			"r3": {Type: feature.CycleTweak, Status: "completed"},
		},
	}

	fs := newFeatureStore(published)
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = func(id string) (*feature.Feature, error) { return published, nil }

	sm := mocks.NewMockSessionManager()

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Sessions:  sm,
	}, orchestrator.Hooks{})

	if err := o.InterruptAllRunning(); err != nil {
		t.Fatalf("InterruptAllRunning: %v", err)
	}

	// running/reviewing cycles marked interrupted (resumable, not failed).
	if published.RepoCycles["r1"].Status != feature.RepoCycleInterrupted {
		t.Errorf("r1 Status = %q, want interrupted", published.RepoCycles["r1"].Status)
	}
	if published.RepoCycles["r1"].LastError != "" {
		t.Errorf("r1 LastError = %q, want empty (interrupted is not a failure)", published.RepoCycles["r1"].LastError)
	}
	if published.RepoCycles["r2"].Status != feature.RepoCycleInterrupted {
		t.Errorf("r2 Status = %q, want interrupted", published.RepoCycles["r2"].Status)
	}
	// completed cycle unchanged.
	if published.RepoCycles["r3"].Status != "completed" {
		t.Errorf("r3 status = %q, want completed (unchanged)", published.RepoCycles["r3"].Status)
	}

	// Non-interactive repo cycles represent active agent work; interruption
	// should move the feature out of the Published bucket.
	if published.Status != feature.StatusInterrupted {
		t.Errorf("published status = %v, want Interrupted", published.Status)
	}

	// KBStatus preserved.
	if published.KBStatus == nil {
		t.Error("KBStatus should be preserved for published features with cycles")
	}
}

// TestOrchestrator_InterruptAllRunning_CodeReadyWithCycles reproduces the
// shutdown scenario where the user has manual_publish=true so the feature
// stays at StatusCodeReady while a tweak/rebase cycle runs. Before the fix,
// the InterruptAllRunning sweep had no branch matching CodeReady — the
// feature's cycle and pending help banner survived the quit and reappeared
// stale on the next launch.
func TestOrchestrator_InterruptAllRunning_CodeReadyWithCycles(t *testing.T) {
	kb := map[string]string{"agentic": "completed"}
	codeReady := &feature.Feature{
		ID:       "cr-cyc",
		Status:   feature.StatusCodeReady,
		KBStatus: kb,
		RepoCycles: map[string]*feature.RepoCycleState{
			"agentic": {Type: feature.CycleTweak, Status: "running"},
		},
		ActiveCycle: &feature.CycleState{
			Type:   feature.CycleTweak,
			Status: feature.RepoCycleRunning,
			Count:  2,
		},
		HelpQueue: []feature.HelpRequest{
			{Question: "Agent has a question — attach with 'a' to respond", Pending: true},
			{Question: "earlier-resolved", Pending: false},
		},
		PermissionsQueue: []feature.PermissionRequest{
			{Tool: "Bash", Pending: true},
		},
	}

	fs := newFeatureStore(codeReady)
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = func(id string) (*feature.Feature, error) { return codeReady, nil }
	sm := mocks.NewMockSessionManager()

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Sessions:  sm,
	}, orchestrator.Hooks{})

	if err := o.InterruptAllRunning(); err != nil {
		t.Fatalf("InterruptAllRunning: %v", err)
	}

	if codeReady.RepoCycles["agentic"].Status != feature.RepoCycleInterrupted {
		t.Errorf("RepoCycles[agentic].Status = %q, want interrupted", codeReady.RepoCycles["agentic"].Status)
	}
	if codeReady.ActiveCycle == nil || codeReady.ActiveCycle.Status != feature.RepoCycleInterrupted {
		t.Errorf("ActiveCycle.Status = %v, want interrupted", codeReady.ActiveCycle)
	}
	if codeReady.ActiveCycle.LastError != "" {
		t.Errorf("ActiveCycle.LastError = %q, want empty after interrupt sweep", codeReady.ActiveCycle.LastError)
	}
	if codeReady.HelpQueue[0].Pending {
		t.Error("Pending question should be cleared after shutdown sweep")
	}
	if codeReady.HelpQueue[1].Pending {
		t.Error("Already-resolved help entry should remain non-pending")
	}
	if codeReady.PermissionsQueue[0].Pending {
		t.Error("Pending permission request should be cleared after shutdown sweep")
	}
	if codeReady.Status != feature.StatusCodeReady {
		t.Errorf("Status = %v, should remain CodeReady", codeReady.Status)
	}
	if codeReady.KBStatus == nil {
		t.Error("KBStatus should be preserved for CodeReady features with cycles")
	}
}
