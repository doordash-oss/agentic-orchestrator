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
)

func TestRelationshipGuardParentWithActiveChild(t *testing.T) {
	t.Parallel()
	parent := &feature.Feature{
		ID:       "guard-parent",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
	}
	child := &feature.Feature{
		ID:       "guard-child",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Parent:   &feature.ChildRelationship{ParentID: "guard-parent", Kind: feature.ChildKindRefactor},
	}
	store := newFeatureStore(parent, child)
	lc := lifecycleForFeature(parent)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	// Publish should be rejected for parent with active child.
	err := o.RelationshipGuard("guard-parent", orchestrator.MutationPublish)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Publish guard: err = %v, want ErrParentMutationLocked", err)
	}

	// Delete should return cascade_delete_not_available.
	err = o.RelationshipGuard("guard-parent", orchestrator.MutationDelete)
	if !errors.Is(err, feature.ErrCascadeDeleteNotAvailable) {
		t.Fatalf("Delete guard: err = %v, want ErrCascadeDeleteNotAvailable", err)
	}

	// Config should be allowed (paired Review edit).
	err = o.RelationshipGuard("guard-parent", orchestrator.MutationConfig)
	if err != nil {
		t.Fatalf("Config guard: err = %v, want nil", err)
	}

	// Restart should be rejected.
	err = o.RelationshipGuard("guard-parent", orchestrator.MutationRestart)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Restart guard: err = %v, want ErrParentMutationLocked", err)
	}

	// MarkDone should be rejected.
	err = o.RelationshipGuard("guard-parent", orchestrator.MutationMarkDone)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("MarkDone guard: err = %v, want ErrParentMutationLocked", err)
	}
}

func TestRelationshipGuardChildRestrictions(t *testing.T) {
	t.Parallel()
	child := &feature.Feature{
		ID:       "guard-child-only",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Parent:   &feature.ChildRelationship{ParentID: "guard-parent2", Kind: feature.ChildKindRefactor},
	}
	store := newFeatureStore(child)
	lc := lifecycleForFeature(child)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	// Publish is not allowed on a child.
	err := o.RelationshipGuard("guard-child-only", orchestrator.MutationPublish)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child Publish: err = %v, want ErrChildMutationRestricted", err)
	}

	// Merge is not allowed on a child.
	err = o.RelationshipGuard("guard-child-only", orchestrator.MutationMerge)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child Merge: err = %v, want ErrChildMutationRestricted", err)
	}

	// Rewind is not allowed on a child.
	err = o.RelationshipGuard("guard-child-only", orchestrator.MutationRewind)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child Rewind: err = %v, want ErrChildMutationRestricted", err)
	}

	// MarkDone is not allowed on a child.
	err = o.RelationshipGuard("guard-child-only", orchestrator.MutationMarkDone)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child MarkDone: err = %v, want ErrChildMutationRestricted", err)
	}

	// Delete is not allowed on a child.
	err = o.RelationshipGuard("guard-child-only", orchestrator.MutationDelete)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child Delete: err = %v, want ErrChildMutationRestricted", err)
	}

	// Start is allowed on a child.
	err = o.RelationshipGuard("guard-child-only", orchestrator.MutationStart)
	if err != nil {
		t.Fatalf("Child Start: err = %v, want nil", err)
	}

	// Config is allowed on a child (paired Review edit).
	err = o.RelationshipGuard("guard-child-only", orchestrator.MutationConfig)
	if err != nil {
		t.Fatalf("Child Config: err = %v, want nil", err)
	}

	// Discard is allowed on a child.
	err = o.RelationshipGuard("guard-child-only", orchestrator.MutationDiscard)
	if err != nil {
		t.Fatalf("Child Discard: err = %v, want nil", err)
	}
}

func TestRelationshipGuardParentWithoutChildAllowsAll(t *testing.T) {
	t.Parallel()
	parent := &feature.Feature{
		ID:       "free-parent",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
	}
	store := newFeatureStore(parent)
	lc := lifecycleForFeature(parent)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	// All operations should be allowed when no active child exists.
	ops := []orchestrator.MutationOperation{
		orchestrator.MutationPublish,
		orchestrator.MutationDelete,
		orchestrator.MutationRestart,
		orchestrator.MutationConfig,
		orchestrator.MutationMarkDone,
		orchestrator.MutationMerge,
		orchestrator.MutationRewind,
	}
	for _, op := range ops {
		err := o.RelationshipGuard("free-parent", op)
		if err != nil {
			t.Fatalf("Guard for %s: err = %v, want nil", op, err)
		}
	}
}
