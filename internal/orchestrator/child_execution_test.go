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
	"os"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// queuedChild returns a child feature parked after launch (setup queued).
func queuedChild() *feature.Feature {
	return &feature.Feature{
		ID:       "child-queued",
		Status:   feature.StatusSettingUpWorktrees,
		Pipeline: feature.PipelineMedium,
		Parent:   &feature.ChildRelationship{ParentID: "parent-1", Kind: feature.ChildKindRefactor},
	}
}

// setupCompleteChild returns a child whose durable setup finished (parked at
// the ordinary post-setup Created status). It is eligible for the
// supported execution shape: Medium pipeline, exactly one repository.
func setupCompleteChild() *feature.Feature {
	return &feature.Feature{
		ID:       "child-ready",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Repos: []feature.FeatureRepo{{
			Name:         "repoA",
			Path:         "/tmp/repoA",
			WorktreePath: "/tmp/repoA-child",
			Branch:       "feature/child-ready",
			BaseBranch:   "main",
		}},
		Parent: &feature.ChildRelationship{ParentID: "parent-1", Kind: feature.ChildKindRefactor},
	}
}

// largeProfileChild returns a setup-complete child with a Large pipeline
// profile.
func largeProfileChild() *feature.Feature {
	child := setupCompleteChild()
	child.ID = "child-large"
	child.Pipeline = feature.PipelineLarge
	return child
}

// multiRepoChild returns a setup-complete Medium child spanning two
// repositories.
func multiRepoChild() *feature.Feature {
	child := setupCompleteChild()
	child.ID = "child-multi"
	child.Repos = append(child.Repos, feature.FeatureRepo{
		Name:         "repoB",
		Path:         "/tmp/repoB",
		WorktreePath: "/tmp/repoB-child",
		Branch:       "feature/child-multi",
		BaseBranch:   "main",
	})
	return child
}

// failedSetupChild returns a child whose durable setup failed (recoverable
// only via RetrySetup).
func failedSetupChild() *feature.Feature {
	f := &feature.Feature{
		ID:       "child-failed",
		Status:   feature.StatusFailed,
		Pipeline: feature.PipelineMedium,
		Parent:   &feature.ChildRelationship{ParentID: "parent-1", Kind: feature.ChildKindRefactor},
	}
	f.Run().Failure = &errcat.FailureRecord{Code: errcat.WorktreeSetupFailed}
	return f
}

// closedCompletedChild returns a settled Completed Medium single-repository
// child parked at ReviewPassed — the state a child keeps after its work
// merged into the parent. cleanupPending controls whether the disposable
// worktree path is still durable for automatic reconciliation or cleared.
func closedCompletedChild(cleanupPending bool) *feature.Feature {
	closed := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	child := setupCompleteChild()
	child.ID = "child-closed"
	child.Status = feature.StatusReviewPassed
	child.Parent.CloseOutcome = feature.ChildCloseOutcomeCompleted
	child.Parent.ClosedAt = &closed
	child.Parent.Transaction = &feature.TransactionJournal{
		Phase: feature.TransactionPhaseMerged,
		Entries: []feature.RepoTransactionEntry{{
			ParentBranch:    "main",
			ParentAnchorSHA: "aaaa1111",
			ChildHeadSHA:    "bbbb2222",
			MergeHEAD:       "cccc3333",
		}},
	}
	if !cleanupPending {
		child.Repos[0].WorktreePath = ""
	}
	return child
}

func newChildExecutionFixture(t *testing.T, child *feature.Feature) (*orchestrator.Orchestrator, *feature.Store) {
	t.Helper()
	store, mgr := newStoredFeatureFixture(t, child)
	o := orchestrator.New(orchestrator.Deps{Manager: mgr, Store: store}, orchestrator.Hooks{})
	t.Cleanup(func() { _ = o.Shutdown() })
	return o, store
}

func TestOrchestrator_ClosedChildExecutionRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		child *feature.Feature
	}{
		{"settled", closedCompletedChild(false)},
		{"cleanup pending", closedCompletedChild(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newChildExecutionFixture(t, tc.child)
			if err := o.StartFeature(tc.child.ID); !errors.Is(err, feature.ErrChildExecutionClosed) {
				t.Fatalf("StartFeature() error = %v, want closed child", err)
			}
			if _, err := o.RestartFeature(tc.child.ID, 0, 0); !errors.Is(err, feature.ErrChildExecutionClosed) {
				t.Fatalf("RestartPhase() error = %v, want closed child", err)
			}
			stored, err := store.Load(tc.child.ID)
			if err != nil || stored.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted || stored.Status != feature.StatusReviewPassed {
				t.Fatalf("closed child changed after refused execution: %+v (err %v)", stored, err)
			}
		})
	}
}

func TestOrchestrator_ChildExecutionBlocked(t *testing.T) {
	for _, tc := range []struct {
		name  string
		child *feature.Feature
	}{
		{"queued", queuedChild()},
		{"failed setup", failedSetupChild()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newChildExecutionFixture(t, tc.child)
			if err := o.StartFeature(tc.child.ID); !errors.Is(err, feature.ErrChildExecutionBlocked) {
				t.Fatalf("StartFeature() error = %v, want blocked child", err)
			}
			if _, err := o.RestartFeature(tc.child.ID, 0, 0); !errors.Is(err, feature.ErrChildExecutionBlocked) {
				t.Fatalf("RestartPhase() error = %v, want blocked child", err)
			}
			stored, err := store.Load(tc.child.ID)
			if err != nil || stored.Status != tc.child.Status {
				t.Fatalf("blocked child changed after refused execution: %+v (err %v)", stored, err)
			}
		})
	}
}

func TestOrchestrator_ChildGatePropagatesLookupErrors(t *testing.T) {
	store, mgr := newStoredFeatureFixture(t)
	o := orchestrator.New(orchestrator.Deps{Manager: mgr, Store: store}, orchestrator.Hooks{})
	for _, run := range []struct {
		name string
		fn   func() error
	}{
		{"start", func() error { return o.StartFeature("missing-child") }},
		{"restart", func() error { _, err := o.RestartFeature("missing-child", 0, 0); return err }},
	} {
		t.Run(run.name, func(t *testing.T) {
			err := run.fn()
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s error = %v, want missing feature", run.name, err)
			}
		})
	}
}

func TestOrchestrator_ChildCapabilityGate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		child *feature.Feature
	}{
		{"medium single repo", setupCompleteChild()},
		{"medium multi repo", multiRepoChild()},
		{"large single repo", largeProfileChild()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newChildExecutionFixture(t, tc.child)
			admission := workadmission.New(workadmission.Options{})
			o.SetAdmissionBoundary(admission)
			if !admission.CloseIfQuiesced() {
				t.Fatal("admission boundary did not close")
			}
			if err := o.StartFeature(tc.child.ID); err != nil {
				if _, closed := workadmission.AsClosed(err); !closed {
					t.Fatalf("StartFeature() error = %v, want dispatch refused by admission after capability check", err)
				}
			} else {
				t.Fatal("StartFeature() bypassed closed admission")
			}
			stored, err := store.Load(tc.child.ID)
			if err != nil || stored.Parent == nil || stored.Parent.CloseOutcome != "" {
				t.Fatalf("eligible child lost active relationship: %+v (err %v)", stored, err)
			}
			if stored.Status != feature.StatusPlanReady && stored.Status != feature.StatusCreated {
				t.Fatalf("eligible child status = %s, want created or plan-ready", stored.Status)
			}
		})
	}
}
