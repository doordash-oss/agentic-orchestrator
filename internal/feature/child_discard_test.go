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

package feature_test

import (
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestDiscardDomainTypes(t *testing.T) {
	t.Parallel()

	t.Run("IsDiscarding true for active child with intent", func(t *testing.T) {
		f := &feature.Feature{
			Parent: &feature.ChildRelationship{ParentID: "p"},
			DiscardIntent: &feature.DiscardIntent{
				Step: feature.DiscardStepIntentRecorded,
			},
		}
		if !f.IsDiscarding() {
			t.Fatal("expected IsDiscarding true")
		}
	})

	t.Run("IsDiscarding false after cleanup done", func(t *testing.T) {
		f := &feature.Feature{
			Parent: &feature.ChildRelationship{ParentID: "p"},
			DiscardIntent: &feature.DiscardIntent{
				Step: feature.DiscardStepCleanupDone,
			},
		}
		if f.IsDiscarding() {
			t.Fatal("expected IsDiscarding false")
		}
	})

	t.Run("DiscardClosureTailPending true when worktree path remains", func(t *testing.T) {
		now := time.Now()
		f := &feature.Feature{
			Parent: &feature.ChildRelationship{
				ParentID:     "p",
				CloseOutcome: feature.ChildCloseOutcomeDiscarded,
				ClosedAt:     &now,
			},
			DiscardIntent: &feature.DiscardIntent{Step: feature.DiscardStepCleanupDone},
			Repos: []feature.FeatureRepo{
				{Name: "repo", WorktreePath: "/wt/child"},
			},
		}
		if !f.DiscardClosureTailPending() {
			t.Fatal("expected DiscardClosureTailPending true")
		}
	})

	t.Run("DiscardClosureTailPending false when all clean", func(t *testing.T) {
		now := time.Now()
		f := &feature.Feature{
			Parent: &feature.ChildRelationship{
				ParentID:     "p",
				CloseOutcome: feature.ChildCloseOutcomeDiscarded,
				ClosedAt:     &now,
			},
			DiscardIntent: &feature.DiscardIntent{Step: feature.DiscardStepCleanupDone},
			Repos: []feature.FeatureRepo{
				{Name: "repo", WorktreePath: ""},
			},
		}
		if f.DiscardClosureTailPending() {
			t.Fatal("expected DiscardClosureTailPending false")
		}
	})

	t.Run("ChildCloseOutcomeDiscarded constant", func(t *testing.T) {
		if feature.ChildCloseOutcomeDiscarded != "discarded" {
			t.Fatalf("got %q, want discarded", feature.ChildCloseOutcomeDiscarded)
		}
	})
}
