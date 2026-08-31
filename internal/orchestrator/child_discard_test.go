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
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ensure ports.SessionManager is referenced
var _ = ports.SessionRunning

func TestDiscardChildRecordsIntentAndCloses(t *testing.T) {
	t.Parallel()
	child := &feature.Feature{
		ID:       "discard-child",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Repos: []feature.FeatureRepo{{
			Name:       "repo",
			Path:       "/tmp/repo",
			Branch:     "feature/child",
			BaseBranch: "main",
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

func TestDiscardChildClosesThenRejectsStaleRetry(t *testing.T) {
	t.Parallel()
	child := &feature.Feature{
		ID:       "discard-idempotent",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Repos: []feature.FeatureRepo{{
			Name:       "repo",
			Path:       "/tmp/repo",
			Branch:     "feature/child",
			BaseBranch: "main",
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

	// Automatic reconciliation, not another user mutation, owns any cleanup
	// after the relationship has closed.
	if err := o.DiscardChild("discard-idempotent"); !errors.Is(err, feature.ErrChildRelationshipClosed) {
		t.Fatalf("second DiscardChild error = %v, want ErrChildRelationshipClosed", err)
	}
	secondLoaded, _ := store.Load("discard-idempotent")
	if secondLoaded.Parent.ClosedAt != firstCloseTime {
		t.Fatal("close timestamp changed on second discard")
	}
}

func TestDiscardChildRejectsCompletedChild(t *testing.T) {
	t.Parallel()
	closedAt := time.Now()
	child := &feature.Feature{
		ID:       "discard-completed",
		Status:   feature.StatusReviewPassed,
		Pipeline: feature.PipelineMedium,
		Parent: &feature.ChildRelationship{
			ParentID:     "discard-parent3",
			Kind:         feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeCompleted,
			ClosedAt:     &closedAt,
		},
	}
	store := newFeatureStore(child)
	lc := lifecycleForFeature(child)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	err := o.DiscardChild("discard-completed")
	if !errors.Is(err, feature.ErrChildRelationshipClosed) {
		t.Fatalf("DiscardChild() error = %v, want ErrChildRelationshipClosed", err)
	}
}

func TestDiscardChildDoesNotResumeClosedDiscardCleanup(t *testing.T) {
	t.Parallel()
	closedAt := time.Now()
	child := &feature.Feature{
		ID:       "discard-closed",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Parent: &feature.ChildRelationship{
			ParentID:     "discard-parent",
			Kind:         feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeDiscarded,
			ClosedAt:     &closedAt,
		},
		DiscardIntent: &feature.DiscardIntent{
			RequestedAt: closedAt.Add(-time.Minute),
			Step:        feature.DiscardStepClosed,
			ClosedAt:    &closedAt,
		},
	}
	store := newFeatureStore(child)
	lc := lifecycleForFeature(child)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	err := o.DiscardChild(child.ID)
	if !errors.Is(err, feature.ErrChildRelationshipClosed) {
		t.Fatalf("DiscardChild() error = %v, want ErrChildRelationshipClosed", err)
	}
	loaded, loadErr := store.Load(child.ID)
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if loaded.DiscardIntent.Step != feature.DiscardStepClosed {
		t.Fatalf("discard step = %q, want unchanged closed step", loaded.DiscardIntent.Step)
	}
}

// TestDiscardChildCleanupFailureRemainsRetryable verifies that when
// worktree cleanup fails, the discard step stays at DiscardStepClosed
// (not cleanup_done) so the cleanup remains durably visible and
// retryable through ReconcileDiscardIntents. The child is still closed
// (CloseOutcome=discarded) so the parent can launch a new child.
func TestDiscardChildCleanupFailureRemainsRetryable(t *testing.T) {
	t.Parallel()
	child := &feature.Feature{
		ID:       "discard-cleanup-fail",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Repos: []feature.FeatureRepo{{
			Name:         "repo",
			Path:         "/tmp/repo",
			WorktreePath: "/tmp/repo-child",
			Branch:       "feature/child",
			BaseBranch:   "main",
		}},
		Parent: &feature.ChildRelationship{ParentID: "discard-cleanup-parent", Kind: feature.ChildKindRefactor},
	}
	parent := &feature.Feature{
		ID:       "discard-cleanup-parent",
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
	// No Worktrees dependency — cleanup will fail because the path
	// cannot be removed, leaving the worktree pending.
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	err := o.DiscardChild("discard-cleanup-fail")
	if err == nil {
		t.Fatal("expected error when cleanup fails")
	}

	loaded, _ := store.Load("discard-cleanup-fail")

	// The child must be closed as discarded (safe closure is durable).
	if loaded.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
		t.Fatalf("close outcome = %q, want discarded", loaded.Parent.CloseOutcome)
	}

	// The discard step must NOT be cleanup_done — it must remain at
	// DiscardStepClosed so the cleanup tail is retryable.
	if loaded.DiscardIntent == nil {
		t.Fatal("discard intent missing")
	}
	if loaded.DiscardIntent.Step == feature.DiscardStepCleanupDone {
		t.Fatal("step = cleanup_done, want non-cleanup-done (cleanup must remain retryable)")
	}
	if loaded.DiscardIntent.Step != feature.DiscardStepClosed {
		t.Fatalf("step = %q, want closed", loaded.DiscardIntent.Step)
	}

	// The worktree path must still be set (durably visible).
	if loaded.Repos[0].WorktreePath == "" {
		t.Fatal("worktree path cleared; want it to remain for retry")
	}

	// The child is still discarding (IsDiscarding returns true because
	// step != cleanup_done), but the relationship is closed so the
	// parent can launch a new child.
	if !loaded.IsDiscarding() {
		t.Fatal("IsDiscarding = false, want true (cleanup not done)")
	}
}

// TestDiscardChildWithActiveSession verifies that a discard request during
// an actively running child session persists intent, requests stop, waits
// while sessions drain, and only closes after quiescence.
func TestDiscardChildWithActiveSession(t *testing.T) {
	t.Parallel()
	child := &feature.Feature{
		ID:       "discard-running-child",
		Status:   feature.StatusImplementing,
		Pipeline: feature.PipelineMedium,
		Repos: []feature.FeatureRepo{{
			Name:       "repo",
			Path:       "/tmp/repo",
			Branch:     "feature/child",
			BaseBranch: "main",
		}},
		Parent: &feature.ChildRelationship{ParentID: "discard-running-parent", Kind: feature.ChildKindRefactor},
	}
	parent := &feature.Feature{
		ID:       "discard-running-parent",
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

	// Wire a mock session manager with an active session for the child.
	// The session stays active even after StopSession is called (it is
	// "draining"), simulating a real session that takes time to terminate.
	sv := mocks.NewMockSessionView("sess-active", child.ID)
	sv.IsActiveVal = true
	sv.StatusVal = ports.SessionRunning

	sm := mocks.NewMockSessionManager()
	sm.FeatureSessionsFn = func(featureID string) []ports.SessionView {
		if featureID == child.ID {
			return []ports.SessionView{sv}
		}
		return nil
	}
	sm.StopSessionFn = func(id string) error {
		// StopSession is called but the session remains "active" —
		// it is draining, not yet quiescent.
		return nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     store,
		Sessions:  sm,
	}, orchestrator.Hooks{})

	// First call: intent is recorded, StopFeatureSessions is called
	// (which invokes StopSession), step advances to SessionsStopping.
	// The quiescence check sees the session still active → returns
	// "sessions still draining".
	err := o.DiscardChild(child.ID)
	if err == nil {
		t.Fatal("expected 'sessions still draining' error on first discard call")
	}
	if !strings.Contains(err.Error(), "still draining") {
		t.Fatalf("first discard: err = %v, want 'still draining'", err)
	}

	// Verify stop was requested (StopSession was called).
	if len(sm.StopCalls) == 0 {
		t.Fatal("StopSession was not called during discard")
	}

	// Verify the discard intent was persisted and step is at SessionsStopping.
	loaded, _ := store.Load(child.ID)
	if loaded.DiscardIntent == nil {
		t.Fatal("discard intent missing after first discard call")
	}
	if loaded.DiscardIntent.Step != feature.DiscardStepSessionsStopping {
		t.Fatalf("step = %q, want %q", loaded.DiscardIntent.Step, feature.DiscardStepSessionsStopping)
	}

	// Simulate the session finishing its drain: mark it inactive.
	sv.IsActiveVal = false

	// Second call: sessions have quiesced, discard should proceed to closure.
	if err := o.DiscardChild(child.ID); err != nil {
		t.Fatalf("second DiscardChild: %v", err)
	}

	// Verify the child is closed with outcome "discarded".
	loaded, _ = store.Load(child.ID)
	if loaded.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
		t.Fatalf("close outcome = %q, want discarded", loaded.Parent.CloseOutcome)
	}
	if loaded.DiscardIntent == nil {
		t.Fatal("discard intent missing after completion")
	}
	if loaded.DiscardIntent.Step != feature.DiscardStepCleanupDone {
		t.Fatalf("step = %q, want %q", loaded.DiscardIntent.Step, feature.DiscardStepCleanupDone)
	}
}

// TestDiscardChildAttentionResolveFailureDoesNotAdvance verifies that when
// the Store.Modify call that clears pending attention fails, the discard
// state machine does NOT advance past attention resolution. The durable
// step must remain at SessionsQuiesced so a retry can re-attempt the
// attention-clearing save, and the error must be propagated to the caller
// rather than silently swallowed.
func TestDiscardChildAttentionResolveFailureDoesNotAdvance(t *testing.T) {
	t.Parallel()
	child := &feature.Feature{
		ID:       "discard-attn-fail",
		Status:   feature.StatusCreated,
		Pipeline: feature.PipelineMedium,
		Repos: []feature.FeatureRepo{{
			Name:       "repo",
			Path:       "/tmp/repo",
			Branch:     "feature/child",
			BaseBranch: "main",
		}},
		PermissionsQueue: []feature.PermissionRequest{{Tool: "pending"}},
		Parent:           &feature.ChildRelationship{ParentID: "discard-attn-parent", Kind: feature.ChildKindRefactor},
	}
	parent := &feature.Feature{
		ID:       "discard-attn-parent",
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

	// Pre-seed the discard intent at SessionsQuiesced so the discard
	// resume jumps straight to the attention-resolution step.
	child.DiscardIntent = &feature.DiscardIntent{
		RequestedAt: time.Now(),
		Step:        feature.DiscardStepSessionsQuiesced,
	}
	reviewPhase := feature.PhaseReview
	child.PendingReviewPhase = &reviewPhase

	// Inject a Modify failure only for the attention-clearing call
	// (the one that nils the queues). A sentinel error makes the
	// assertion precise.
	attnErr := errors.New("attention save disk full")
	origModify := store.ModifyFn
	store.ModifyFn = func(id string, fn func(*feature.Feature) error) error {
		if id == child.ID {
			cur := store.features[id]
			// Detect the attention-clearing callback by the fields it
			// touches: it nils PermissionsQueue. Run the callback against
			// a throwaway clone to identify it without mutating state.
			if cur != nil && cur.PermissionsQueue != nil {
				probe := *cur
				_ = fn(&probe)
				if probe.PermissionsQueue == nil {
					return attnErr
				}
			}
		}
		return origModify(id, fn)
	}

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	err := o.DiscardChild(child.ID)
	if err == nil {
		t.Fatal("expected error when attention-clearing save fails, got nil")
	}
	if !errors.Is(err, attnErr) {
		t.Fatalf("error = %v, want it to wrap attnErr %v", err, attnErr)
	}

	// The durable step must NOT have advanced past SessionsQuiesced.
	loaded, _ := store.Load(child.ID)
	if loaded.DiscardIntent == nil {
		t.Fatal("discard intent missing")
	}
	if loaded.DiscardIntent.Step != feature.DiscardStepSessionsQuiesced {
		t.Fatalf("step = %q, want %q (must not advance on save failure)",
			loaded.DiscardIntent.Step, feature.DiscardStepSessionsQuiesced)
	}
	// The pending attention must remain uncleared.
	if loaded.PermissionsQueue == nil {
		t.Fatal("PermissionsQueue was cleared despite save failure; want it retained")
	}
	if loaded.PendingReviewPhase == nil {
		t.Fatal("PendingReviewPhase was cleared despite save failure; want it retained")
	}
}
