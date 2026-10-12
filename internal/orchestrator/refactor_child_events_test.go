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

package orchestrator

import (
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func storedFeatureDeps(t *testing.T, features ...*feature.Feature) Deps {
	t.Helper()
	return storedFeatureDepsAt(t, t.TempDir(), features...)
}

func storedFeatureDepsAt(t *testing.T, stateDir string, features ...*feature.Feature) Deps {
	t.Helper()
	store := feature.NewStore(stateDir)
	for _, f := range features {
		f.SchemaVersion = feature.SchemaVersionCurrent
		if err := store.Save(f); err != nil {
			t.Fatal(err)
		}
	}
	return Deps{Manager: feature.NewManager(store, config.NewDefault()), Store: store}
}

func drainOrchestratorEvent(t *testing.T, o *Orchestrator) ports.Event {
	t.Helper()
	select {
	case ev := <-o.Events():
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for orchestrator event")
		return ports.Event{}
	}
}

// TestChildCreatedEmitsCorrelatedEvent pins the child-creation emit
// point: the SSE-facing event carries the child id as FeatureID and the
// launch parent and child as one relationship identity.
func TestChildCreatedEmitsCorrelatedEvent(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{feature.ChildKindRefactor, feature.ChildKindReviewFeedback} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			child := &feature.Feature{
				ID:     "child-1",
				Status: feature.StatusSettingUpWorktrees,
				Parent: &feature.ChildRelationship{ParentID: "parent-1", Kind: kind},
			}
			o := New(storedFeatureDeps(t, child), Hooks{})

			o.childCreated(child)
			ev := drainOrchestratorEvent(t, o)
			if ev.Type != ports.RelationshipChildCreated || ev.FeatureID != child.ID || ev.ParentID != "parent-1" || ev.ChildID != child.ID || ev.Feature != child {
				t.Fatalf("event = %+v, want relationship-created event for child correlated to parent", ev)
			}
		})
	}

	// Nil and top-level features must not emit anything.
	o := New(storedFeatureDeps(t), Hooks{})
	o.childCreated(nil)
	o.childCreated(&feature.Feature{ID: "top-level"})
	select {
	case stray := <-o.Events():
		t.Fatalf("unexpected event for non-child input: %+v", stray)
	default:
	}
}

// TestEmitSetupEventStampsParentOnChildEvents pins the setup-lifecycle
// correlation: child setup events carry both relationship identifiers; top-level events
// stay zero-value-safe; the parent's persisted lifecycle is never touched.
func TestEmitSetupEventStampsParentOnChildEvents(t *testing.T) {
	t.Parallel()
	child := &feature.Feature{
		ID:     "child-1",
		Status: feature.StatusSettingUpWorktrees,
		Parent: &feature.ChildRelationship{ParentID: "parent-1", Kind: feature.ChildKindRefactor},
	}
	o := New(storedFeatureDeps(t, child), Hooks{})

	o.emitSetupEvent(feature.SetupEvent{Kind: feature.SetupEventStarted, FeatureID: child.ID})
	ev := drainOrchestratorEvent(t, o)
	if ev.Type != ports.SetupStarted || ev.FeatureID != child.ID || ev.ParentID != "parent-1" || ev.ChildID != child.ID {
		t.Fatalf("child setup event = %+v, want SetupStarted correlated to parent", ev)
	}
}

func TestEmitSetupEventTopLevelHasNoRelatedFeature(t *testing.T) {
	t.Parallel()
	topLevel := &feature.Feature{ID: "top-1", Status: feature.StatusSettingUpWorktrees}
	o := New(storedFeatureDeps(t, topLevel), Hooks{})

	o.emitSetupEvent(feature.SetupEvent{Kind: feature.SetupEventCompleted, FeatureID: topLevel.ID})
	ev := drainOrchestratorEvent(t, o)
	if ev.Type != ports.SetupCompleted || ev.FeatureID != topLevel.ID || ev.ParentID != "" || ev.ChildID != "" {
		t.Fatalf("top-level setup event = %+v, want no related feature id", ev)
	}
}
