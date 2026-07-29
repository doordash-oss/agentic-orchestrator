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
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
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

// largeProfileChild returns a setup-complete child whose Large profile needs
// temporary child knowledge-base isolation.
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
	return &feature.Feature{
		ID:          "child-failed",
		Status:      feature.StatusFailed,
		FailureType: feature.FailureWorktreeSetup,
		Pipeline:    feature.PipelineMedium,
		Parent:      &feature.ChildRelationship{ParentID: "parent-1", Kind: feature.ChildKindRefactor},
	}
}

// closedCompletedChild returns a settled Completed Medium single-repository
// child parked at ReviewPassed — the state a child keeps after its work
// merged into the parent. cleanupPending controls whether the disposable
// worktree path is still durable (a resumable closure tail) or cleared.
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

// TestOrchestrator_ClosedChildExecutionRefused pins the settled-relationship
// half of the child execution gate: a child whose relationship closed
// (Completed, merged into the parent) can never start, resume, retry, or
// replay pipeline phases again — its disposable worktree may already be
// gone. The refusal is the stable typed ErrChildExecutionClosed, never a
// capability or setup error, and it leaves the closed record untouched. The
// only surviving execution route is Restart for a Completed child whose
// cleanup tail is genuinely resumable (covered end-to-end by the real-git
// cleanup-warning retry test); start and retry stay refused even then.
func TestOrchestrator_ClosedChildExecutionRefused(t *testing.T) {
	children := []struct {
		name string
		f    *feature.Feature
	}{
		{"settled child (cleanup finished)", closedCompletedChild(false)},
		{"closed child with resumable cleanup tail", closedCompletedChild(true)},
	}
	for _, tc := range children {
		t.Run("start/"+tc.name, func(t *testing.T) {
			lc := lifecycleForFeature(tc.f)
			store := newFeatureStore(tc.f)
			o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})
			err := o.StartFeature(tc.f.ID)
			if !errors.Is(err, feature.ErrChildExecutionClosed) {
				t.Fatalf("StartFeature() error = %v, want ErrChildExecutionClosed", err)
			}
			var capErr *feature.ChildCapabilityError
			if errors.As(err, &capErr) || errors.Is(err, feature.ErrChildExecutionBlocked) {
				t.Fatalf("StartFeature() error = %v, want the closed-relationship error, not a capability/setup block", err)
			}
			refuteLifecycleCall(t, lc, "RunSetup")
			refuteLifecycleCall(t, lc, "StartPlanning")
			refuteLifecycleCall(t, lc, "StartImplementing")
			refuteLifecycleCall(t, lc, "StartFinalReview")
			parked, loadErr := store.Load(tc.f.ID)
			if loadErr != nil {
				t.Fatalf("reload child: %v", loadErr)
			}
			if parked.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted ||
				parked.Status != feature.StatusReviewPassed {
				t.Fatalf("closed child mutated by rejected start: outcome=%q status=%s", parked.Parent.CloseOutcome, parked.Status)
			}
		})
		t.Run("retry/"+tc.name, func(t *testing.T) {
			lc := lifecycleForFeature(tc.f)
			o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(tc.f)}, orchestrator.Hooks{})
			err := o.RetryPhase(tc.f.ID)
			if !errors.Is(err, feature.ErrChildExecutionClosed) {
				t.Fatalf("RetryPhase() error = %v, want ErrChildExecutionClosed", err)
			}
			refuteLifecycleCall(t, lc, "RetryPhase")
		})
	}

	t.Run("restart/settled child replays no pipeline phases", func(t *testing.T) {
		child := closedCompletedChild(false)
		lc := lifecycleForFeature(child)
		o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(child)}, orchestrator.Hooks{})
		_, err := o.RestartPhase(child.ID, 0, 0)
		if !errors.Is(err, feature.ErrChildExecutionClosed) {
			t.Fatalf("RestartPhase() error = %v, want ErrChildExecutionClosed", err)
		}
		refuteLifecycleCall(t, lc, "MarkImplementing")
		refuteLifecycleCall(t, lc, "StartFinalReview")
	})
}

// TestOrchestrator_ChildExecutionBlocked pins the setup-state half of the
// supported child capability gate: start/restart/retry on a
// child whose setup is queued, running, or failed returns
// ErrChildExecutionBlocked and never
// routes into phase execution. Setup runs solely via RunSetup/RetrySetup.
func TestOrchestrator_ChildExecutionBlocked(t *testing.T) {
	children := []struct {
		name string
		f    *feature.Feature
	}{
		{"queued child", queuedChild()},
		{"failed-setup child", failedSetupChild()},
	}
	for _, tc := range children {
		t.Run("start/"+tc.name, func(t *testing.T) {
			lc := lifecycleForFeature(tc.f)
			o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(tc.f)}, orchestrator.Hooks{})
			err := o.StartFeature(tc.f.ID)
			if !errors.Is(err, feature.ErrChildExecutionBlocked) {
				t.Fatalf("StartFeature() error = %v, want ErrChildExecutionBlocked", err)
			}
			refuteLifecycleCall(t, lc, "RunSetup")
			refuteLifecycleCall(t, lc, "RetrySetup")
			refuteLifecycleCall(t, lc, "StartPlanning")
			refuteLifecycleCall(t, lc, "StartInquire")
		})
		t.Run("restart/"+tc.name, func(t *testing.T) {
			lc := lifecycleForFeature(tc.f)
			o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(tc.f)}, orchestrator.Hooks{})
			_, err := o.RestartPhase(tc.f.ID, 0, 0)
			if !errors.Is(err, feature.ErrChildExecutionBlocked) {
				t.Fatalf("RestartPhase() error = %v, want ErrChildExecutionBlocked", err)
			}
			refuteLifecycleCall(t, lc, "RunSetup")
		})
		t.Run("retry/"+tc.name, func(t *testing.T) {
			lc := lifecycleForFeature(tc.f)
			o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(tc.f)}, orchestrator.Hooks{})
			err := o.RetryPhase(tc.f.ID)
			if !errors.Is(err, feature.ErrChildExecutionBlocked) {
				t.Fatalf("RetryPhase() error = %v, want ErrChildExecutionBlocked", err)
			}
			refuteLifecycleCall(t, lc, "RetryPhase")
		})
	}
}

// TestOrchestrator_ChildGatePropagatesLookupErrors pins fail-closed gate
// behavior: when the feature record cannot be loaded the gate must surface
// the lookup failure rather than silently skipping the child check.
func TestOrchestrator_ChildGatePropagatesLookupErrors(t *testing.T) {
	lookupErr := errors.New("disk read failed")
	newOrchestrator := func() *orchestrator.Orchestrator {
		lc := mocks.NewMockFeatureLifecycle()
		lc.GetFn = func(string) (*feature.Feature, error) { return nil, lookupErr }
		return orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore()}, orchestrator.Hooks{})
	}

	if err := newOrchestrator().StartFeature("child-x"); err == nil ||
		!strings.Contains(err.Error(), "loading feature") ||
		errors.Is(err, feature.ErrChildExecutionBlocked) {
		t.Fatalf("StartFeature() error = %v, want propagated lookup failure", err)
	}
	if err := newOrchestrator().RetryPhase("child-x"); err == nil ||
		!strings.Contains(err.Error(), "loading feature") ||
		errors.Is(err, feature.ErrChildExecutionBlocked) {
		t.Fatalf("RetryPhase() error = %v, want propagated lookup failure", err)
	}
	if _, err := newOrchestrator().RestartPhase("child-x", 0, 0); err == nil ||
		!strings.Contains(err.Error(), "loading feature") ||
		errors.Is(err, feature.ErrChildExecutionBlocked) {
		t.Fatalf("RestartPhase() error = %v, want propagated lookup failure", err)
	}
}

// TestOrchestrator_ChildSetupRunsOnlyViaSetupEntrypoints confirms the queued
// child's setup is reachable exclusively through RunSetup/RetrySetup.
func TestOrchestrator_ChildSetupRunsOnlyViaSetupEntrypoints(t *testing.T) {
	child := queuedChild()
	lc := lifecycleForFeature(child)
	lc.RunSetupFn = func(featureID string, opts ...feature.SetupRunnerOptions) error { return nil }
	lc.RetrySetupFn = func(featureID string, opts ...feature.SetupRunnerOptions) error { return nil }
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(child)}, orchestrator.Hooks{})

	if err := o.RunSetup(child.ID); err != nil {
		t.Fatalf("RunSetup() error = %v", err)
	}
	assertLifecycleCall(t, lc, "RunSetup")
	if err := o.RetrySetup(child.ID); err != nil {
		t.Fatalf("RetrySetup() error = %v", err)
	}
	assertLifecycleCall(t, lc, "RetrySetup")
}

// TestOrchestrator_ChildCapabilityGate pins the child capability matrix:
// a setup-complete Medium child with exactly one repository passes start,
// resume/retry, and restart; Large/Moonshot children return the typed
// temporary KB-capability error, and a multi-repository child returns the
// distinct typed multi-repository error. Rejection never invalidates or
// closes the child, which stays setup-complete and active.
func TestOrchestrator_ChildCapabilityGate(t *testing.T) {
	t.Run("eligible medium single-repo child starts", func(t *testing.T) {
		child := setupCompleteChild()
		lc := lifecycleForFeature(child)
		store := newFeatureStore(child)
		o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})
		if err := o.StartFeature(child.ID); err != nil {
			t.Fatalf("StartFeature() error = %v, want nil for eligible child", err)
		}
		refuteLifecycleCall(t, lc, "RunSetup")
		refuteLifecycleCall(t, lc, "RetrySetup")
	})

	t.Run("eligible medium multi-repo child starts", func(t *testing.T) {
		child := multiRepoChild()
		lc := lifecycleForFeature(child)
		store := newFeatureStore(child)
		o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})
		if err := o.StartFeature(child.ID); err != nil {
			t.Fatalf("StartFeature() error = %v, want nil for eligible multi-repo child", err)
		}
	})

	capabilityChildren := []struct {
		name       string
		f          *feature.Feature
		wantReason string
	}{
		{"large child", largeProfileChild(), feature.ChildCapabilityProfileUnsupported},
	}
	for _, tc := range capabilityChildren {
		t.Run("start/"+tc.name, func(t *testing.T) {
			lc := lifecycleForFeature(tc.f)
			store := newFeatureStore(tc.f)
			o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})
			err := o.StartFeature(tc.f.ID)
			var capErr *feature.ChildCapabilityError
			if !errors.As(err, &capErr) || capErr.Reason != tc.wantReason {
				t.Fatalf("StartFeature() error = %v, want ChildCapabilityError reason %s", err, tc.wantReason)
			}
			refuteLifecycleCall(t, lc, "StartPlanning")
			refuteLifecycleCall(t, lc, "StartInquire")
			// The rejection is not a failure: the child remains
			// setup-complete, active, and eligible later.
			parked, loadErr := store.Load(tc.f.ID)
			if loadErr != nil {
				t.Fatalf("reload child: %v", loadErr)
			}
			if parked.Status != feature.StatusCreated || !parked.IsActiveChild() {
				t.Fatalf("child after rejection = %s active=%v, want Created and active", parked.Status, parked.IsActiveChild())
			}
		})
		t.Run("restart/"+tc.name, func(t *testing.T) {
			lc := lifecycleForFeature(tc.f)
			o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(tc.f)}, orchestrator.Hooks{})
			_, err := o.RestartPhase(tc.f.ID, 0, 0)
			var capErr *feature.ChildCapabilityError
			if !errors.As(err, &capErr) || capErr.Reason != tc.wantReason {
				t.Fatalf("RestartPhase() error = %v, want ChildCapabilityError reason %s", err, tc.wantReason)
			}
		})
		t.Run("retry/"+tc.name, func(t *testing.T) {
			lc := lifecycleForFeature(tc.f)
			o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(tc.f)}, orchestrator.Hooks{})
			err := o.RetryPhase(tc.f.ID)
			var capErr *feature.ChildCapabilityError
			if !errors.As(err, &capErr) || capErr.Reason != tc.wantReason {
				t.Fatalf("RetryPhase() error = %v, want ChildCapabilityError reason %s", err, tc.wantReason)
			}
		})
	}
}
