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
	"sync"
	"testing"
	"time"

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

	// Cleanup should be rejected.
	err = o.RelationshipGuard("guard-parent", orchestrator.MutationCleanup)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Cleanup guard: err = %v, want ErrParentMutationLocked", err)
	}

	// Start should be rejected.
	err = o.RelationshipGuard("guard-parent", orchestrator.MutationStart)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Start guard: err = %v, want ErrParentMutationLocked", err)
	}

	// Delivery (rebase, review-comments) should be rejected.
	err = o.RelationshipGuard("guard-parent", orchestrator.MutationDelivery)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Delivery guard: err = %v, want ErrParentMutationLocked", err)
	}

	// Stop should be rejected for the parent while a child is active.
	err = o.RelationshipGuard("guard-parent", orchestrator.MutationStop)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Stop guard: err = %v, want ErrParentMutationLocked", err)
	}

	// ReviewDecision should be rejected for the parent while a child is
	// active — a "proceed" can restart the parent pipeline.
	err = o.RelationshipGuard("guard-parent", orchestrator.MutationReviewDecision)
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

	// Cleanup is not allowed on a child.
	err = o.RelationshipGuard("guard-child-only", orchestrator.MutationCleanup)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child Cleanup: err = %v, want ErrChildMutationRestricted", err)
	}

	// Start is allowed on a child.
	err = o.RelationshipGuard("guard-child-only", orchestrator.MutationStart)
	if err != nil {
		t.Fatalf("Child Start: err = %v, want nil", err)
	}

	// Stop is allowed on a child (ordinary execution control).
	err = o.RelationshipGuard("guard-child-only", orchestrator.MutationStop)
	if err != nil {
		t.Fatalf("Child Stop: err = %v, want nil", err)
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

	// Delivery (rebase, review-comments) is not allowed on a child.
	err = o.RelationshipGuard("guard-child-only", orchestrator.MutationDelivery)
	if !errors.Is(err, feature.ErrChildMutationRestricted) {
		t.Fatalf("Child Delivery: err = %v, want ErrChildMutationRestricted", err)
	}

	// ReviewDecision is allowed on a child — resolving the child's own
	// review gate is an ordinary execution control.
	err = o.RelationshipGuard("guard-child-only", orchestrator.MutationReviewDecision)
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
		orchestrator.MutationDelivery,
		orchestrator.MutationStop,
	}
	for _, op := range ops {
		err := o.RelationshipGuard("free-parent", op)
		if err != nil {
			t.Fatalf("Guard for %s: err = %v, want nil", op, err)
		}
	}
}

// TestRelationshipGuardFailsClosedOnListError verifies that when
// Store.List fails, the guard propagates the error rather than treating
// it as "no active child" and allowing the parent mutation to proceed.
func TestRelationshipGuardFailsClosedOnListError(t *testing.T) {
	t.Parallel()
	parent := &feature.Feature{
		ID:       "list-err-parent",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
	}
	store := newFeatureStore(parent)
	listErr := errors.New("disk read error")
	store.ListFn = func() ([]*feature.Feature, error) {
		return nil, listErr
	}
	lc := lifecycleForFeature(parent)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	err := o.RelationshipGuard("list-err-parent", orchestrator.MutationPublish)
	if err == nil {
		t.Fatal("expected error when Store.List fails, got nil (fail-open)")
	}
	// The guard must propagate the underlying List error (fail closed)
	// rather than masking it as ErrParentMutationLocked, which would
	// imply a legitimate relationship rejection rather than an
	// infrastructure failure.
	if !errors.Is(err, listErr) {
		t.Errorf("expected error to wrap the list error, got %v", err)
	}
	if errors.Is(err, feature.ErrParentMutationLocked) {
		t.Errorf("expected the propagated list error, not ErrParentMutationLocked; got %v", err)
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
	store := newFeatureStore(parent, child)
	lc := lifecycleForFeature(parent)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	// Delete must return cascade_delete_not_available, not
	// parent_mutation_locked, even while discard is in progress.
	err := o.RelationshipGuard("discard-parent", orchestrator.MutationDelete)
	if !errors.Is(err, feature.ErrCascadeDeleteNotAvailable) {
		t.Fatalf("Delete during discard: err = %v, want ErrCascadeDeleteNotAvailable", err)
	}
	if errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Delete during discard: err = %v, should not be ErrParentMutationLocked", err)
	}

	// Other disallowed operations should still return parent_mutation_locked.
	err = o.RelationshipGuard("discard-parent", orchestrator.MutationPublish)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("Publish during discard: err = %v, want ErrParentMutationLocked", err)
	}

	err = o.RelationshipGuard("discard-parent", orchestrator.MutationMarkDone)
	if !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("MarkDone during discard: err = %v, want ErrParentMutationLocked", err)
	}

	// Config is allowed even during discard (paired Review edit).
	err = o.RelationshipGuard("discard-parent", orchestrator.MutationConfig)
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
func TestRelationshipLockSerializesMutationsWithChildCreation(t *testing.T) {
	t.Parallel()
	parent := &feature.Feature{
		ID:       "lock-parent",
		Slug:     "lock-parent",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
	}
	store := newFeatureStore(parent)
	lc := lifecycleForFeature(parent)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	// Scenario 1: Read lock blocks write lock.
	{
		readStarted := make(chan struct{})
		writeProceed := make(chan struct{})
		writeDone := make(chan struct{})

		go func() {
			_ = o.WithRelationshipReadLock(func() error {
				close(readStarted)
				<-writeProceed
				return nil
			})
		}()

		<-readStarted

		go func() {
			_ = o.WithRelationshipWriteLock(func() error {
				close(writeDone)
				return nil
			})
		}()

		select {
		case <-writeDone:
			t.Fatal("write lock acquired while read lock was held")
		case <-time.After(20 * time.Millisecond):
		}

		close(writeProceed)
		select {
		case <-writeDone:
		case <-time.After(2 * time.Second):
			t.Fatal("write lock did not acquire after read lock was released")
		}
	}

	// Scenario 2: Write lock blocks read lock.
	{
		writeStarted := make(chan struct{})
		readProceed := make(chan struct{})
		readDone := make(chan struct{})

		go func() {
			_ = o.WithRelationshipWriteLock(func() error {
				close(writeStarted)
				<-readProceed
				return nil
			})
		}()

		<-writeStarted

		go func() {
			_ = o.WithRelationshipReadLock(func() error {
				close(readDone)
				return nil
			})
		}()

		select {
		case <-readDone:
			t.Fatal("read lock acquired while write lock was held")
		case <-time.After(20 * time.Millisecond):
		}

		close(readProceed)
		select {
		case <-readDone:
		case <-time.After(2 * time.Second):
			t.Fatal("read lock did not acquire after write lock was released")
		}
	}

	// Scenario 3: Multiple read locks can proceed concurrently.
	{
		var wg sync.WaitGroup
		const concurrentReaders = 5
		wg.Add(concurrentReaders)
		for i := 0; i < concurrentReaders; i++ {
			go func() {
				defer wg.Done()
				_ = o.WithRelationshipReadLock(func() error {
					return nil
				})
			}()
		}
		wg.Wait()
	}
}
