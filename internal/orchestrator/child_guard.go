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
	"fmt"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

type childRelationshipCloser interface {
	CloseChild(childID, outcome string, closedAt time.Time) error
}

func (o *Orchestrator) closeChildRelationship(childID, outcome string, closedAt time.Time) error {
	closer, ok := o.deps.Store.(childRelationshipCloser)
	if ok {
		return closer.CloseChild(childID, outcome, closedAt)
	}
	// Lightweight port doubles may not expose the concrete store primitive.
	// Preserve the same idempotent invariant in that compatibility path;
	// production wiring always uses feature.Store.CloseChild.
	return o.deps.Store.Modify(childID, func(child *feature.Feature) error {
		if !child.IsChild() {
			return fmt.Errorf("feature %s is not a child", childID)
		}
		if !child.IsActiveChild() {
			if child.Parent.CloseOutcome == outcome {
				return nil
			}
			return feature.ErrChildRelationshipClosed
		}
		child.Parent.CloseOutcome = outcome
		timestamp := closedAt
		child.Parent.ClosedAt = &timestamp
		return nil
	})
}

// MutationOperation identifies which orchestrator mutation is being guarded.
type MutationOperation string

const (
	MutationPublish       MutationOperation = "publish"
	MutationMerge         MutationOperation = "merge"
	MutationRewind        MutationOperation = "rewind"
	MutationCleanup       MutationOperation = "cleanup"
	MutationMarkDone      MutationOperation = "mark-done"
	MutationDelivery      MutationOperation = "delivery"
	MutationRestart       MutationOperation = "restart"
	MutationResume        MutationOperation = "resume"
	MutationStart         MutationOperation = "start"
	MutationStop          MutationOperation = "stop"
	MutationRetry         MutationOperation = "retry"
	MutationDelete        MutationOperation = "delete"
	MutationRefactor      MutationOperation = "refactor"
	MutationConfig        MutationOperation = "config"
	MutationDiscard       MutationOperation = "discard"
	MutationNeedUserInput MutationOperation = "need-user-input"
	// MutationReviewDecision covers a user review-gate decision (proceed/
	// iterate/rewind) that can clear gate fields and dispatch a new phase.
	// It is an ordinary execution control for a child but a parent mutation
	// that can restart the parent pipeline, so it is allowed for children
	// and locked for parents with an active child.
	MutationReviewDecision MutationOperation = "review-decision"
)

// allowedChildMutations is the set of operations permitted on an active child.
var allowedChildMutations = map[MutationOperation]bool{
	MutationStart:          true,
	MutationStop:           true,
	MutationRestart:        true,
	MutationResume:         true,
	MutationNeedUserInput:  true,
	MutationConfig:         true,
	MutationDiscard:        true,
	MutationRetry:          true,
	MutationReviewDecision: true,
}

// allowedParentMutationsWhileChildActive is the set of operations permitted
// on a parent while it has an active child or a discard intent that has not
// reached safe closure. Discard is a child action, not a parent mutation;
// it is absent here so callers cannot infer it is a valid parent operation.
var allowedParentMutationsWhileChildActive = map[MutationOperation]bool{
	MutationConfig: true,
}

// RelationshipGuard checks whether a mutation is allowed given the
// parent/child relationship state. It is the single authoritative guard used
// by orchestrator operations and REST mutations. The action catalog agrees
// with this enforcement but never serves as the sole protection.
//
// For mutations that follow the Store.Modify pattern, prefer guardedModify
// which combines the guard check and the mutation under the same store mutex,
// closing the time-of-check/time-of-use gap with concurrent child creation
// (CreateChildLocked). RelationshipGuard remains correct for call sites where
// the subsequent operation is not a simple Store.Modify.
func (o *Orchestrator) RelationshipGuard(featureID string, op MutationOperation) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("loading feature for relationship guard: %w", err)
	}
	if f == nil {
		return nil
	}
	relationshipParentID := featureID
	if f.IsChild() {
		relationshipParentID = f.Parent.ParentID
	}
	if owned, ownershipErr := o.cascadeOwnsRelationship(relationshipParentID); ownershipErr != nil {
		return ownershipErr
	} else if owned && op != MutationDelete {
		return fmt.Errorf("%w: cascade delete owns relationship %s", feature.ErrParentMutationLocked, relationshipParentID)
	}

	var activeChild *feature.Feature
	if !f.IsChild() {
		childID, err := o.activeChildID(featureID)
		if err != nil {
			return fmt.Errorf("checking active child for relationship guard: %w", err)
		}
		if childID != "" {
			activeChild, err = o.deps.Lifecycle.Get(childID)
			if err != nil {
				return fmt.Errorf("loading child for relationship guard: %w", err)
			}
		}
	}

	return relationshipGuardCheck(f, activeChild, op)
}

// relationshipGuardCheck is the core guard logic operating on pre-loaded
// features. It is shared by RelationshipGuard (which loads features from the
// lifecycle) and guardedModify (which loads them under the store mutex so the
// guard check is serialized with CreateChildLocked). activeChild is nil when
// the feature is a child or when no active child exists.
func relationshipGuardCheck(f *feature.Feature, activeChild *feature.Feature, op MutationOperation) error {
	if f == nil {
		return nil
	}

	// Child-specific restrictions.
	if f.IsChild() {
		if f.IsActiveChild() || f.IsDiscarding() {
			if !allowedChildMutations[op] {
				return fmt.Errorf("%w: %s is not permitted on child %s", feature.ErrChildMutationRestricted, op, f.ID)
			}
			return nil
		}
		// Closed child controls are presentation-only. Automatic
		// reconciliation owns any unfinished closure cleanup.
		if f.Parent != nil && f.Parent.CloseOutcome != "" {
			if op == MutationRestart || op == MutationRetry || op == MutationStart {
				return nil
			}
			return fmt.Errorf("%w: %s is not permitted on closed child %s", feature.ErrChildRelationshipClosed, op, f.ID)
		}
		return nil
	}

	// Parent-specific restrictions: check for active child or pending discard.
	if activeChild == nil {
		return nil
	}

	// An active child exists. Check for discard intent on the child.
	if activeChild.IsDiscarding() {
		// Delete is always cascade_delete_not_available while any child
		// relationship exists, including during discard — the complete
		// recoverable cascade operation is not yet available.
		if op == MutationDelete {
			return fmt.Errorf("%w: parent %s has a child %s with discard in progress", feature.ErrCascadeDeleteNotAvailable, f.ID, activeChild.ID)
		}
		if !allowedParentMutationsWhileChildActive[op] {
			return fmt.Errorf("%w: %s is not permitted while child discard is in progress", feature.ErrParentMutationLocked, op)
		}
		return nil
	}

	// Active child with no discard in progress.
	if op == MutationDelete {
		return fmt.Errorf("%w: parent %s has an active child %s", feature.ErrCascadeDeleteNotAvailable, f.ID, activeChild.ID)
	}
	if !allowedParentMutationsWhileChildActive[op] {
		return fmt.Errorf("%w: %s is not permitted while child %s is active", feature.ErrParentMutationLocked, op, activeChild.ID)
	}
	return nil
}

// guardedModify combines the relationship guard check with a Store.Modify
// under the same store mutex, closing the time-of-check/time-of-use gap with
// concurrent child creation (CreateChildLocked). When the concrete store
// supports ModifyGuarded, the guard and mutation are atomic; otherwise it
// falls back to RelationshipGuard + Store.Modify.
func (o *Orchestrator) guardedModify(
	featureID string,
	op MutationOperation,
	fn func(f *feature.Feature) error,
) error {
	type guardedModifier interface {
		ModifyGuarded(id string, guard feature.RelationshipGuardFunc, fn func(f *feature.Feature) error) error
	}
	if gm, ok := o.deps.Store.(guardedModifier); ok {
		return gm.ModifyGuarded(
			featureID,
			func(f *feature.Feature, activeChild *feature.Feature) error {
				return relationshipGuardCheck(f, activeChild, op)
			},
			fn,
		)
	}
	// Fallback for stores that do not support ModifyGuarded.
	if err := o.RelationshipGuard(featureID, op); err != nil {
		return err
	}
	return o.deps.Store.Modify(featureID, fn)
}

// WithRelationshipReadLock acquires the relationship read lock and runs fn.
// Callers that need to detect and act on the relationship state (e.g. paired
// config detection + update) must hold this lock for the entire detect-act
// window so a concurrent child creation cannot interleave.
func (o *Orchestrator) WithRelationshipReadLock(fn func() error) error {
	o.relationshipMu.RLock()
	defer o.relationshipMu.RUnlock()
	return fn()
}

// WithRelationshipWriteLock acquires the relationship write lock and runs fn.
// Child creation must hold this lock so no mutation guard can pass while a
// child is being created. The lock is released before the method returns so
// long-running post-creation work (e.g. async setup) does not block mutations.
func (o *Orchestrator) WithRelationshipWriteLock(fn func() error) error {
	o.relationshipMu.Lock()
	defer o.relationshipMu.Unlock()
	return fn()
}
