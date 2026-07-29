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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

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
	MutationRetry         MutationOperation = "retry"
	MutationDelete        MutationOperation = "delete"
	MutationRefactor      MutationOperation = "refactor"
	MutationConfig        MutationOperation = "config"
	MutationDiscard       MutationOperation = "discard"
	MutationNeedUserInput MutationOperation = "need-user-input"
)

// allowedChildMutations is the set of operations permitted on an active child.
var allowedChildMutations = map[MutationOperation]bool{
	MutationStart:         true,
	MutationRestart:       true,
	MutationResume:        true,
	MutationNeedUserInput: true,
	MutationConfig:        true,
	MutationDiscard:       true,
	MutationRetry:         true,
}

// allowedParentMutationsWhileChildActive is the set of operations permitted
// on a parent while it has an active child or a discard intent that has not
// reached safe closure.
var allowedParentMutationsWhileChildActive = map[MutationOperation]bool{
	MutationConfig:  true,
	MutationDiscard: true,
}

// RelationshipGuard checks whether a mutation is allowed given the
// parent/child relationship state. It is the single authoritative guard used
// by orchestrator operations and REST mutations. The action catalog agrees
// with this enforcement but never serves as the sole protection.
func (o *Orchestrator) RelationshipGuard(featureID string, op MutationOperation) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("loading feature for relationship guard: %w", err)
	}
	if f == nil {
		return nil
	}

	// Child-specific restrictions.
	if f.IsChild() {
		if f.IsActiveChild() || f.IsDiscarding() {
			if !allowedChildMutations[op] {
				return fmt.Errorf("%w: %s is not permitted on child %s", feature.ErrChildMutationRestricted, op, featureID)
			}
			return nil
		}
		// Closed child: restart and retry have their own execution gate
		// (checkChildExecution) that handles the closed-relationship case
		// with the typed ErrChildExecutionClosed. Let them pass through;
		// all other mutations are rejected.
		if f.Parent != nil && f.Parent.CloseOutcome != "" {
			if op == MutationRestart || op == MutationRetry {
				return nil
			}
			return fmt.Errorf("%w: %s is not permitted on closed child %s", feature.ErrChildMutationRestricted, op, featureID)
		}
		return nil
	}

	// Parent-specific restrictions: check for active child or pending discard.
	childID, _ := o.activeChildID(featureID)
	if childID == "" {
		return nil
	}

	// An active child exists. Check for discard intent on the child.
	child, err := o.deps.Lifecycle.Get(childID)
	if err != nil {
		return fmt.Errorf("loading child for relationship guard: %w", err)
	}
	if child != nil && child.IsDiscarding() {
		if !allowedParentMutationsWhileChildActive[op] {
			return fmt.Errorf("%w: %s is not permitted while child discard is in progress", feature.ErrParentMutationLocked, op)
		}
		return nil
	}

	// Active child with no discard in progress.
	if op == MutationDelete {
		return fmt.Errorf("%w: parent %s has an active child %s", feature.ErrCascadeDeleteNotAvailable, featureID, childID)
	}
	if !allowedParentMutationsWhileChildActive[op] {
		return fmt.Errorf("%w: %s is not permitted while child %s is active", feature.ErrParentMutationLocked, op, childID)
	}
	return nil
}
