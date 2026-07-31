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

package feature

import (
	"time"
)

// DiscardStep records the durable progress of a child discard operation.
// Each step is persisted before the operation advances so startup
// reconciliation resumes from the last durable step.
type DiscardStep string

const (
	// DiscardStepIntentRecorded: discard intent is durable on the child.
	DiscardStepIntentRecorded DiscardStep = "intent_recorded"
	// DiscardStepSessionsStopping: session stop has been requested.
	DiscardStepSessionsStopping DiscardStep = "sessions_stopping"
	// DiscardStepSessionsQuiesced: all sessions and helpers have joined.
	DiscardStepSessionsQuiesced DiscardStep = "sessions_quiesced"
	// DiscardStepAttentionResolved: pending questions, permissions, help,
	// gates, and input markers have been settled.
	DiscardStepAttentionResolved DiscardStep = "attention_resolved"
	// DiscardStepRefsSafe: integration safety is established — no parent
	// ref contains a child candidate, or provable refs were rolled back.
	DiscardStepRefsSafe DiscardStep = "refs_safe"
	// DiscardStepClosed: the relationship is closed with outcome Discarded.
	DiscardStepClosed DiscardStep = "closed"
	// DiscardStepCleanupDone: disposable cleanup tail completed.
	DiscardStepCleanupDone DiscardStep = "cleanup_done"
)

// DiscardIntent is the durable record of an in-flight child discard. It
// persists the discard step so startup reconciliation can resume from
// the last durable point. Stored on Feature.DiscardIntent.
type DiscardIntent struct {
	// RequestedAt is when the discard was first requested.
	RequestedAt time.Time `yaml:"requested_at"`
	// Step is the last durably completed step.
	Step DiscardStep `yaml:"step"`
	// ClosedAt is the relationship close timestamp, set when the child
	// is closed as Discarded. Empty until closure is durable.
	ClosedAt *time.Time `yaml:"closed_at,omitempty"`
}

// IsDiscarding reports whether the child has an active discard intent that
// has not reached safe closure.
func (f *Feature) IsDiscarding() bool {
	return f != nil && f.IsChild() && f.DiscardIntent != nil &&
		f.DiscardIntent.Step != DiscardStepCleanupDone
}

// DiscardClosureTailPending reports whether a discarded child's cleanup tail
// is still unfinished (worktree paths or cleanup warnings remain).
func (f *Feature) DiscardClosureTailPending() bool {
	if f == nil || !f.IsChild() {
		return false
	}
	if f.Parent == nil || f.Parent.CloseOutcome != ChildCloseOutcomeDiscarded {
		return false
	}
	if f.DiscardIntent != nil && f.DiscardIntent.Step != DiscardStepCleanupDone {
		return true
	}
	return f.AnyChildWorktreePending() || f.hasUnsettledDiscardCleanupWarning()
}

// hasUnsettledDiscardCleanupWarning checks for cleanup warnings on a
// discarded child's transaction journal entries.
func (f *Feature) hasUnsettledDiscardCleanupWarning() bool {
	if f.Parent == nil || f.Parent.Transaction == nil {
		return false
	}
	for i := range f.Parent.Transaction.Entries {
		if f.Parent.Transaction.Entries[i].CleanupWarning != "" {
			return true
		}
	}
	return false
}
