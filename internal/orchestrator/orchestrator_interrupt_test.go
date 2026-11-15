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

// ---------------------------------------------------------------------------
// TestOrchestrator_InterruptFeature
// ---------------------------------------------------------------------------

func TestOrchestrator_InterruptFeature(t *testing.T) {
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
	}
	fs, lc := newStoredFeatureFixture(t, f)

	sm := mocks.NewMockSessionManager()
	sessions := []ports.SessionView{
		mocks.NewMockSessionView("s-1", "feat-int"),
		mocks.NewMockSessionView("s-2", "feat-int"),
	}
	sm.FeatureSessionsFn = func(id string) []ports.SessionView { return sessions }

	var hookFeatureID string
	o := orchestrator.New(orchestrator.Deps{
		Manager:  lc,
		Store:    fs,
		Sessions: sm,
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

	// Pending flags cleared in the stored feature.
	f = loadStoredFeature(t, fs, f.ID)
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

	if f.Status != feature.StatusInterrupted {
		t.Errorf("stored status = %s, want Interrupted", f.Status)
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
	fs, lc := newStoredFeatureFixture(t, f)

	sm := mocks.NewMockSessionManager()
	// FeatureSessionsFn default returns nil.

	o := orchestrator.New(orchestrator.Deps{
		Manager:  lc,
		Store:    fs,
		Sessions: sm,
	}, orchestrator.Hooks{})

	if err := o.InterruptFeature("feat-ns"); err != nil {
		t.Fatalf("InterruptFeature: %v", err)
	}
	if len(sm.StopCalls) != 0 {
		t.Errorf("StopSession should not be called with no sessions; got %d", len(sm.StopCalls))
	}
	if stored := loadStoredFeature(t, fs, f.ID); stored.Status != feature.StatusInterrupted {
		t.Errorf("stored status = %s, want Interrupted", stored.Status)
	}
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
			fs, lc := newStoredFeatureFixture(t, f)
			o := orchestrator.New(orchestrator.Deps{
				Manager:  lc,
				Store:    fs,
				Sessions: mocks.NewMockSessionManager(),
			}, orchestrator.Hooks{})

			if err := o.InterruptFeature(f.ID); err != nil {
				t.Fatalf("InterruptFeature: %v", err)
			}
			if stored := loadStoredFeature(t, fs, f.ID); stored.Status != status {
				t.Fatalf("stored status = %s, want unchanged %s", stored.Status, status)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_InterruptAllRunning
// ---------------------------------------------------------------------------

func TestOrchestrator_InterruptAllRunning(t *testing.T) {
	running1 := &feature.Feature{
		ID:     "running-1",
		Status: feature.StatusImplementing,
	}
	running2 := &feature.Feature{
		ID:     "running-2",
		Status: feature.StatusPlanning,
	}
	published := &feature.Feature{
		ID:     "pub-1",
		Status: feature.StatusPublished,
	}
	done := &feature.Feature{ID: "done-1", Status: feature.StatusDone}
	setup := &feature.Feature{ID: "setup-1", Status: feature.StatusSettingUpWorktrees}

	fs, lc := newStoredFeatureFixture(t, running1, running2, published, done, setup)

	sm := mocks.NewMockSessionManager()
	sm.FeatureSessionsFn = func(id string) []ports.SessionView { return nil }

	interrupts := 0
	o := orchestrator.New(orchestrator.Deps{
		Manager:  lc,
		Store:    fs,
		Sessions: sm,
	}, orchestrator.Hooks{
		OnFeatureInterrupted: func(id string) { interrupts++ },
	})

	if err := o.InterruptAllRunning(); err != nil {
		t.Fatalf("InterruptAllRunning: %v", err)
	}

	if interrupts != 2 {
		t.Errorf("OnFeatureInterrupted calls = %d, want 2 (only running features)", interrupts)
	}

	done = loadStoredFeature(t, fs, done.ID)
	setup = loadStoredFeature(t, fs, setup.ID)
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
