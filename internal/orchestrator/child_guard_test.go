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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
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
	deps := storedFeatureDeps(t, parent, child)
	o := New(deps, Hooks{})

	// Publish should be rejected for parent with active child.
	err := o.relationshipGuard("guard-parent", mutationPublish)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Publish guard: err = %v, want ErrParentMutationLocked", err)
	}

	// Delete should return cascade_delete_not_available.
	err = o.relationshipGuard("guard-parent", mutationDelete)
	if !errors.Is(err, feature.ErrCascadeDeleteNotAvailable) {
		t.Fatalf("Delete guard: err = %v, want ErrCascadeDeleteNotAvailable", err)
	}

	// Config should be allowed (paired Review edit).
	err = o.relationshipGuard("guard-parent", mutationConfig)
	if err != nil {
		t.Fatalf("Config guard: err = %v, want nil", err)
	}

	// Restart should be rejected.
	err = o.relationshipGuard("guard-parent", mutationRestart)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Restart guard: err = %v, want ErrParentMutationLocked", err)
	}

	// MarkDone should be rejected.
	err = o.relationshipGuard("guard-parent", mutationMarkDone)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("MarkDone guard: err = %v, want ErrParentMutationLocked", err)
	}

	// Cleanup should be rejected.
	err = o.relationshipGuard("guard-parent", mutationCleanup)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Cleanup guard: err = %v, want ErrParentMutationLocked", err)
	}

	// Start should be rejected.
	err = o.relationshipGuard("guard-parent", mutationStart)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Start guard: err = %v, want ErrParentMutationLocked", err)
	}

	// Rebase delivery should be rejected.
	err = o.relationshipGuard("guard-parent", mutationDelivery)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Delivery guard: err = %v, want ErrParentMutationLocked", err)
	}

	// Stop should be rejected for the parent while a child is active.
	err = o.relationshipGuard("guard-parent", mutationStop)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Stop guard: err = %v, want ErrParentMutationLocked", err)
	}

	// ReviewDecision should be rejected for the parent while a child is
	// active — a "proceed" can restart the parent pipeline.
	err = o.relationshipGuard("guard-parent", mutationReviewDecision)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("ReviewDecision guard: err = %v, want ErrParentMutationLocked", err)
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
	deps := storedFeatureDeps(t, child)
	o := New(deps, Hooks{})

	// Publish is not allowed on a child.
	err := o.relationshipGuard("guard-child-only", mutationPublish)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child Publish: err = %v, want ErrChildMutationRestricted", err)
	}

	// Merge is not allowed on a child.
	err = o.relationshipGuard("guard-child-only", mutationMerge)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child Merge: err = %v, want ErrChildMutationRestricted", err)
	}

	// Rewind is not allowed on a child.
	err = o.relationshipGuard("guard-child-only", mutationRewind)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child Rewind: err = %v, want ErrChildMutationRestricted", err)
	}

	// MarkDone is not allowed on a child.
	err = o.relationshipGuard("guard-child-only", mutationMarkDone)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child MarkDone: err = %v, want ErrChildMutationRestricted", err)
	}

	// Delete is not allowed on a child.
	err = o.relationshipGuard("guard-child-only", mutationDelete)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child Delete: err = %v, want ErrChildMutationRestricted", err)
	}

	// Cleanup is not allowed on a child.
	err = o.relationshipGuard("guard-child-only", mutationCleanup)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child Cleanup: err = %v, want ErrChildMutationRestricted", err)
	}

	// Start is allowed on a child.
	err = o.relationshipGuard("guard-child-only", mutationStart)
	if err != nil {
		t.Fatalf("Child Start: err = %v, want nil", err)
	}

	// Stop is allowed on a child (ordinary execution control).
	err = o.relationshipGuard("guard-child-only", mutationStop)
	if err != nil {
		t.Fatalf("Child Stop: err = %v, want nil", err)
	}

	// Config is allowed on a child (paired Review edit).
	err = o.relationshipGuard("guard-child-only", mutationConfig)
	if err != nil {
		t.Fatalf("Child Config: err = %v, want nil", err)
	}

	// Discard is allowed on a child.
	err = o.relationshipGuard("guard-child-only", mutationDiscard)
	if err != nil {
		t.Fatalf("Child Discard: err = %v, want nil", err)
	}

	// Rebase delivery is not allowed on a child.
	err = o.relationshipGuard("guard-child-only", mutationDelivery)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child Delivery: err = %v, want ErrChildMutationRestricted", err)
	}

	// ReviewDecision is allowed on a child — resolving the child's own
	// review gate is an ordinary execution control.
	err = o.relationshipGuard("guard-child-only", mutationReviewDecision)
	if err != nil {
		t.Fatalf("Child ReviewDecision: err = %v, want nil", err)
	}
}

func TestRelationshipGuardParentWithoutChildAllowsAll(t *testing.T) {
	t.Parallel()
	parent := &feature.Feature{
		ID:       "free-parent",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
	}
	deps := storedFeatureDeps(t, parent)
	o := New(deps, Hooks{})

	// All operations should be allowed when no active child exists.
	ops := []mutationOperation{
		mutationPublish,
		mutationDelete,
		mutationRestart,
		mutationConfig,
		mutationMarkDone,
		mutationMerge,
		mutationRewind,
		mutationDelivery,
		mutationStop,
	}
	for _, op := range ops {
		err := o.relationshipGuard("free-parent", op)
		if err != nil {
			t.Fatalf("Guard for %s: err = %v, want nil", op, err)
		}
	}
}

// TestDetectPairedConfigTargetToleratesPartialLoad verifies that legacy
// feature records (which fail to load with a PartialLoadError) do not block
// paired config detection: the scan uses the features that did load.
func TestDetectPairedConfigTargetToleratesPartialLoad(t *testing.T) {
	t.Parallel()
	parent := &feature.Feature{
		ID:       "partial-parent",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
	}
	child := &feature.Feature{
		ID:       "partial-child",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Parent:   &feature.ChildRelationship{ParentID: "partial-parent", Kind: feature.ChildKindRefactor},
	}
	deps := storedFeatureDeps(t, parent, child)
	legacyDir := filepath.Join(deps.Store.BaseDir, "legacy-record")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "feature.yaml"), []byte("id: legacy-record\nschema_version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := New(deps, Hooks{})

	parentID, childID, paired, err := o.detectPairedConfigTarget("partial-parent")
	if err != nil {
		t.Fatalf("DetectPairedConfigTarget: err = %v, want nil", err)
	}
	if !paired || parentID != "partial-parent" || childID != "partial-child" {
		t.Fatalf("DetectPairedConfigTarget = (%q, %q, %v), want (partial-parent, partial-child, true)", parentID, childID, paired)
	}

	// The guard shares the same relationship scan and must still see the
	// active child through a partial load.
	if gErr := o.relationshipGuard("partial-parent", mutationPublish); !errors.Is(gErr, feature.ErrParentMutationLocked) {
		t.Fatalf("Publish guard: err = %v, want ErrParentMutationLocked", gErr)
	}
}

// TestRelationshipGuardDeleteDuringDiscardReturnsCascadeError verifies that
// parent Delete during an in-flight child discard returns
// ErrCascadeDeleteNotAvailable, not ErrParentMutationLocked. The phase's
// stable machine-readable conflict contract requires cascade_delete_not_available
// until the recoverable cascade delete operation exists.
func TestRelationshipGuardDeleteDuringDiscardReturnsCascadeError(t *testing.T) {
	t.Parallel()
	parent := &feature.Feature{
		ID:       "discard-parent",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
	}
	child := &feature.Feature{
		ID:       "discard-child",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Parent:   &feature.ChildRelationship{ParentID: "discard-parent", Kind: feature.ChildKindRefactor},
		DiscardIntent: &feature.DiscardIntent{
			Step: feature.DiscardStepIntentRecorded,
		},
	}
	deps := storedFeatureDeps(t, parent, child)
	o := New(deps, Hooks{})

	// Delete must return cascade_delete_not_available, not
	// parent_mutation_locked, even while discard is in progress.
	err := o.relationshipGuard("discard-parent", mutationDelete)
	if !errors.Is(err, feature.ErrCascadeDeleteNotAvailable) {
		t.Fatalf("Delete during discard: err = %v, want ErrCascadeDeleteNotAvailable", err)
	}
	if errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Delete during discard: err = %v, should not be ErrParentMutationLocked", err)
	}

	// Other disallowed operations should still return parent_mutation_locked.
	err = o.relationshipGuard("discard-parent", mutationPublish)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Publish during discard: err = %v, want ErrParentMutationLocked", err)
	}

	err = o.relationshipGuard("discard-parent", mutationMarkDone)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("MarkDone during discard: err = %v, want ErrParentMutationLocked", err)
	}

	// Config is allowed even during discard (paired Review edit).
	err = o.relationshipGuard("discard-parent", mutationConfig)
	if err != nil {
		t.Fatalf("Config during discard: err = %v, want nil", err)
	}
}

// TestRelationshipLockSerializesMutationsWithChildCreation verifies that the
// relationship read lock (held by mutation guards) and the write lock (held by
// child creation) are mutually exclusive. A child creation attempt must block
// while a mutation is in progress, and a mutation must block while a child is
// being created. This closes the time-of-check/time-of-use gap where a
// standalone RelationshipGuard read could pass, then CreateChildLocked creates
// a child, then the mutation lands on a parent that now has an active child.
