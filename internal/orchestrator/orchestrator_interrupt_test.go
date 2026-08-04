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
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// kbStatusBuilding is the fixture KBStatus value shared by this file's tests.
const kbStatusBuilding = "building"

func findInterruptLifecycleCall(lc *mocks.MockFeatureLifecycle, method string) *mocks.MockCall {
	for i := range lc.Calls {
		if lc.Calls[i].Method == method {
			return &lc.Calls[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// TestOrchestrator_InterruptFeature
// ---------------------------------------------------------------------------

func TestOrchestrator_InterruptFeature(t *testing.T) {
	kbStatus := map[string]string{repoName: kbStatusBuilding, repoNameB: kbStatusCompleted}
	f := &feature.Feature{
		ID:                       "feat-int",
		Status:                   feature.StatusNeedUserInput,
		Pipeline:                 feature.PipelineLarge,
		PendingNeedUserInputPath: "/tmp/feat-int-need-user-input.yaml",
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
	sessions := []ports.SessionView{
		mocks.NewMockSessionView("s-1", "feat-int"),
		mocks.NewMockSessionView("s-2", "feat-int"),
	}
	sm.FeatureSessionsFn = func(id string) []ports.SessionView { return sessions }

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
	if f.PendingNeedUserInputPath != "" {
		t.Errorf("PendingNeedUserInputPath = %q, want empty after Stop", f.PendingNeedUserInputPath)
	}

	// KBStatus preserved (NOT cleared by InterruptFeature).
	if f.KBStatus == nil {
		t.Error("KBStatus was cleared by InterruptFeature — should be preserved")
	}
	if f.KBStatus[repoName] != kbStatusBuilding {
		t.Errorf("KBStatus[repo-a] = %q, want building", f.KBStatus[repoName])
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

func TestOrchestrator_InterruptFeature_SettledFeatureIsIdempotent(t *testing.T) {
	for _, status := range []feature.Status{
		feature.StatusCodeReady,
		feature.StatusPublished,
		feature.StatusDone,
		feature.StatusFailed,
		feature.StatusInterrupted,
	} {
		t.Run(status.String(), func(t *testing.T) {
			f := &feature.Feature{
				ID:     "feat-settled",
				Status: status,
			}
			lc := lifecycleForFeature(f)
			fs := newFeatureStore(f)
			o := orchestrator.New(orchestrator.Deps{
				Lifecycle: lc,
				Store:     fs,
				Sessions:  mocks.NewMockSessionManager(),
			}, orchestrator.Hooks{})

			if err := o.InterruptFeature(f.ID); err != nil {
				t.Fatalf("InterruptFeature: %v", err)
			}
			if f.Status != status {
				t.Fatalf("status = %s, want unchanged %s", f.Status, status)
			}
			if call := findInterruptLifecycleCall(lc, "Transition"); call != nil {
				t.Fatalf("Transition called for settled feature: %+v", call)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_InterruptAllRunning
// ---------------------------------------------------------------------------

func TestOrchestrator_InterruptAllRunning(t *testing.T) {
	running1 := &feature.Feature{
		ID:       "running-1",
		Status:   feature.StatusImplementing,
		KBStatus: map[string]string{"r": kbStatusBuilding},
	}
	running2 := &feature.Feature{
		ID:       "running-2",
		Status:   feature.StatusPlanning,
		KBStatus: map[string]string{"r": kbStatusCompleted},
	}
	published := &feature.Feature{
		ID:       "pub-1",
		Status:   feature.StatusPublished,
		KBStatus: map[string]string{"r": kbStatusCompleted},
	}
	done := &feature.Feature{ID: "done-1", Status: feature.StatusDone}
	setup := &feature.Feature{ID: "setup-1", Status: feature.StatusSettingUpWorktrees}

	fs := newFeatureStore(running1, running2, published, done, setup)
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
		case "setup-1":
			return setup, nil
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
	sm.FeatureSessionsFn = func(id string) []ports.SessionView { return nil }

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
	if setup.Status != feature.StatusSettingUpWorktrees {
		t.Errorf("setup feature status = %s, want SettingUpWorktrees", setup.Status)
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
