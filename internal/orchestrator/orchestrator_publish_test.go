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
	"errors"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// ---------------------------------------------------------------------------
// TestOrchestrator_StartPublish_NotPublishable
// ---------------------------------------------------------------------------
//
// If the feature is not publishable, startPublish returns PhaseNoOp and no
// hooks/events fire. publishFn is NOT called.
func TestOrchestrator_StartPublish_NotPublishable(t *testing.T) {
	unpub := false
	f := &feature.Feature{
		ID:           "feat-np",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhasePublish,
		Pipeline:     feature.PipelineMedium,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", Publishable: &unpub},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	var hookCalled bool
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{
		OnPhaseStarted: func(id string, p feature.Phase) { hookCalled = true },
	})

	publishCalls := 0
	o.SetPublishFn(func(id string) error {
		publishCalls++
		return nil
	})

	if err := o.StartFeature("feat-np"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}
	if publishCalls != 0 {
		t.Errorf("publishFn should NOT be called; got %d calls", publishCalls)
	}
	if hookCalled {
		t.Error("OnPhaseStarted should NOT fire for no-op publish")
	}
	events := drainEvents(o)
	if hasPhaseStarted(events, feature.PhasePublish) != nil {
		t.Error("no PhaseStarted event should be emitted for not-publishable publish")
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_StartPublish_AutoPublishDisabled
// ---------------------------------------------------------------------------

func TestOrchestrator_StartPublish_AutoPublishDisabled(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-mp",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhasePublish,
		Pipeline:     feature.PipelineMedium,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
		},
		Checkpoints: feature.Checkpoints{ManualPublish: true}, // AutoPublish=false
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	var hookCalled bool
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{
		OnPhaseStarted: func(id string, p feature.Phase) { hookCalled = true },
	})

	publishCalls := 0
	o.SetPublishFn(func(id string) error {
		publishCalls++
		return nil
	})

	if err := o.StartFeature("feat-mp"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}
	if publishCalls != 0 {
		t.Errorf("publishFn should NOT be called; got %d calls", publishCalls)
	}
	if hookCalled {
		t.Error("OnPhaseStarted should NOT fire for no-op publish")
	}
	events := drainEvents(o)
	if hasPhaseStarted(events, feature.PhasePublish) != nil {
		t.Error("no PhaseStarted event should be emitted for auto-publish-disabled publish")
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_StartPublish_AutoPublishEnabled
// ---------------------------------------------------------------------------
//
// When publishable + auto-publish, startPublish delegates to publishFn.
// We use SetPublishFn to confirm the dispatch happens without invoking the
// full publish path.
func TestOrchestrator_StartPublish_AutoPublishEnabled(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-auto",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhasePublish,
		Pipeline:     feature.PipelineMedium,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
		},
		// Checkpoints zero-value → AutoPublish()==true.
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	var hookCalled bool
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{
		OnPhaseStarted: func(id string, p feature.Phase) { hookCalled = true },
	})

	publishCalls := 0
	var publishedID string
	o.SetPublishFn(func(id string) error {
		publishCalls++
		publishedID = id
		return nil
	})

	if err := o.StartFeature("feat-auto"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	if publishCalls != 1 {
		t.Errorf("publishFn should be called exactly once; got %d", publishCalls)
	}
	if publishedID != "feat-auto" {
		t.Errorf("publishFn called with ID %q, want %q", publishedID, "feat-auto")
	}
	if !hookCalled {
		t.Error("OnPhaseStarted SHOULD fire (publish dispatched successfully)")
	}
	events := drainEvents(o)
	if hasPhaseStarted(events, feature.PhasePublish) == nil {
		t.Error("expected PhaseStarted event for PhasePublish")
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_StartPublish_PublishError
// ---------------------------------------------------------------------------
//
// When publishFn returns an error, startPhase propagates it and does NOT emit
// a PhaseStarted event.
func TestOrchestrator_StartPublish_PublishError(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-err",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhasePublish,
		Pipeline:     feature.PipelineMedium,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	var hookCalled bool
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{
		OnPhaseStarted: func(id string, p feature.Phase) { hookCalled = true },
	})

	wantErr := errors.New("publish boom")
	o.SetPublishFn(func(id string) error { return wantErr })

	err := o.StartFeature("feat-err")
	if err == nil {
		t.Fatal("expected error from publishFn, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want wrapping %v", err, wantErr)
	}

	if hookCalled {
		t.Error("OnPhaseStarted should NOT fire on publish error")
	}
	events := drainEvents(o)
	// FeatureStarted may be present (fires before dispatch); PhaseStarted must not.
	for _, ev := range events {
		if ev.Type == ports.PhaseStarted {
			t.Errorf("unexpected PhaseStarted event on publish error: %v", ev)
		}
	}
}
