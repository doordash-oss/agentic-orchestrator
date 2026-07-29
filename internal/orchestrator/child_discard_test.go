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
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestDiscardChildRecordsIntentAndCloses(t *testing.T) {
	t.Parallel()
	child := &feature.Feature{
		ID:       "discard-child",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Repos: []feature.FeatureRepo{{
			Name:         "repo",
			Path:         "/tmp/repo",
			WorktreePath: "/tmp/repo-child",
			Branch:       "feature/child",
			BaseBranch:   "main",
		}},
		Parent: &feature.ChildRelationship{ParentID: "discard-parent", Kind: feature.ChildKindRefactor},
	}
	parent := &feature.Feature{
		ID:       "discard-parent",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
	}
	store := newFeatureStore(child, parent)

	// The lifecycle Get needs to return the right feature by ID.
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = func(id string) (*feature.Feature, error) {
		if f, ok := store.features[id]; ok {
			return f, nil
		}
		return nil, errors.New("not found")
	}

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	err := o.DiscardChild("discard-child")
	if err != nil {
		t.Fatalf("DiscardChild: %v", err)
	}

	// Verify discard intent was recorded and then closed.
	loaded, _ := store.Load("discard-child")
	if loaded.DiscardIntent == nil {
		t.Fatal("discard intent missing")
	}
	if loaded.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
		t.Fatalf("close outcome = %q, want discarded", loaded.Parent.CloseOutcome)
	}
	if loaded.Parent.ClosedAt == nil {
		t.Fatal("closed_at missing")
	}
	if loaded.DiscardIntent.Step != feature.DiscardStepCleanupDone {
		t.Fatalf("step = %q, want cleanup_done", loaded.DiscardIntent.Step)
	}
}

func TestDiscardChildIdempotent(t *testing.T) {
	t.Parallel()
	child := &feature.Feature{
		ID:       "discard-idempotent",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Repos: []feature.FeatureRepo{{
			Name:         "repo",
			Path:         "/tmp/repo",
			WorktreePath: "/tmp/repo-child",
			Branch:       "feature/child",
			BaseBranch:   "main",
		}},
		Parent: &feature.ChildRelationship{ParentID: "discard-parent2", Kind: feature.ChildKindRefactor},
	}
	parent := &feature.Feature{
		ID:       "discard-parent2",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
	}
	store := newFeatureStore(child, parent)
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = func(id string) (*feature.Feature, error) {
		if f, ok := store.features[id]; ok {
			return f, nil
		}
		return nil, errors.New("not found")
	}
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	// First discard.
	if err := o.DiscardChild("discard-idempotent"); err != nil {
		t.Fatalf("first DiscardChild: %v", err)
	}
	firstLoaded, _ := store.Load("discard-idempotent")
	firstCloseTime := firstLoaded.Parent.ClosedAt

	// Second discard should be a no-op (already done).
	if err := o.DiscardChild("discard-idempotent"); err != nil {
		t.Fatalf("second DiscardChild: %v", err)
	}
	secondLoaded, _ := store.Load("discard-idempotent")
	if secondLoaded.Parent.ClosedAt != firstCloseTime {
		t.Fatal("close timestamp changed on second discard")
	}
}

func TestDiscardChildRejectsCompletedChild(t *testing.T) {
	t.Parallel()
	child := &feature.Feature{
		ID:       "discard-completed",
		Status:   feature.StatusReviewPassed,
		Pipeline: feature.PipelineMedium,
		Parent: &feature.ChildRelationship{
			ParentID:     "discard-parent3",
			Kind:         feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeCompleted,
		},
	}
	store := newFeatureStore(child)
	lc := lifecycleForFeature(child)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	err := o.DiscardChild("discard-completed")
	if err == nil {
		t.Fatal("expected error discarding completed child")
	}
}
