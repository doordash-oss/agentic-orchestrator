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

package agent

import (
	"errors"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// inMemoryFeatureStore is a tiny store fake that lets MarkRunning's
// Modify+Load round-trip observe the same feature object. The real
// MockFeatureStore's ModifyFn / LoadFn are independent — sharing them
// here keeps the deep-module tests legible without dragging in the
// orchestrator package's featureStore helper.
type inMemoryFeatureStore struct {
	*mocks.MockFeatureStore
	mu sync.Mutex
	f  *feature.Feature
}

func newInMemoryStore(f *feature.Feature) *inMemoryFeatureStore {
	s := &inMemoryFeatureStore{
		MockFeatureStore: mocks.NewMockFeatureStore(),
		f:                f,
	}
	s.LoadFn = func(id string) (*feature.Feature, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.f == nil || s.f.ID != id {
			return nil, errors.New("feature not found")
		}
		return s.f, nil
	}
	s.ModifyFn = func(id string, fn func(*feature.Feature) error) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.f == nil || s.f.ID != id {
			return errors.New("feature not found")
		}
		return fn(s.f)
	}
	return s
}

// newFeatureForTweak builds a multi-repo feature suitable for tweak tests.
// Each repo has a worktree path so HasUncommittedChanges / CommitAll /
// PullRebase / Push see distinct workdirs.
func newFeatureForTweak(repos ...string) *feature.Feature {
	f := &feature.Feature{
		ID:         "feat-tweak",
		Slug:       "tweak-feat",
		Status:     feature.StatusPublished,
		ActiveRun:  1,
		RunCount:   1,
		Repos:      make([]feature.FeatureRepo, 0, len(repos)),
		RepoStates: make(map[string]*feature.RepoState, len(repos)),
	}
	for _, r := range repos {
		f.Repos = append(f.Repos, feature.FeatureRepo{
			Name:         r,
			Path:         "/tmp/" + r,
			WorktreePath: "/tmp/wt/" + r,
			Branch:       "feature/tweak-feat",
		})
		f.RepoStates[r] = &feature.RepoState{
			Touched: true, PRURL: "https://github.com/org/" + r + "/pull/1",
		}
	}
	return f
}

// TestTweakSession_MarkRunning_StampsActiveCycleAndIncrementsCount
// asserts that MarkRunning pre-bumps TweakCount and stamps
// Feature.ActiveCycle = {Type: tweak, Status: running, Count: TweakCount}.
// The deep module owns the lifecycle stamp so the orchestrator does not
// have to re-implement it.
func TestTweakSession_MarkRunning_StampsActiveCycleAndIncrementsCount(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI)
	store := newInMemoryStore(f)
	ts := &TweakSession{Store: store}

	updated, err := ts.MarkRunning(f.ID)
	if err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if updated == nil {
		t.Fatal("MarkRunning: nil feature")
	}
	if updated.TweakCount() != 1 {
		t.Errorf("TweakCount = %d, want 1", updated.TweakCount())
	}
	if updated.ActiveCycle == nil {
		t.Fatal("ActiveCycle = nil, want non-nil")
	}
	if updated.ActiveCycle.Type != feature.CycleTweak {
		t.Errorf("ActiveCycle.Type = %q, want %q", updated.ActiveCycle.Type, feature.CycleTweak)
	}
	if updated.ActiveCycle.Status != feature.RepoCycleRunning {
		t.Errorf("ActiveCycle.Status = %q, want %q", updated.ActiveCycle.Status, feature.RepoCycleRunning)
	}
	if updated.ActiveCycle.Count != 1 {
		t.Errorf("ActiveCycle.Count = %d, want 1", updated.ActiveCycle.Count)
	}
}

// TestTweakSession_MarkRunning_Idempotent verifies that re-entrant
// MarkRunning on an already-running tweak does NOT double-bump
// TweakCount. Idempotency matters because the orchestrator's StartTweak
// can be retried if the dispatch fails partway through.
func TestTweakSession_MarkRunning_Idempotent(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI)
	store := newInMemoryStore(f)
	ts := &TweakSession{Store: store}

	if _, err := ts.MarkRunning(f.ID); err != nil {
		t.Fatalf("first MarkRunning: %v", err)
	}
	if _, err := ts.MarkRunning(f.ID); err != nil {
		t.Fatalf("second MarkRunning: %v", err)
	}
	if got := f.TweakCount(); got != 1 {
		t.Errorf("TweakCount after second MarkRunning = %d, want 1 (idempotent)", got)
	}
}

// TestTweakSession_MarkInterrupted_TransitionsStatus covers the
// crash-recovery path: when the harness sees Status: running with no
// live session, MarkInterrupted transitions the entry to "interrupted"
// so the TUI can offer "resume tweak" → fresh session. The entry is NOT
// cleared (ClearActiveCycle owns that).
func TestTweakSession_MarkInterrupted_TransitionsStatus(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI)
	f.ActiveCycle = &feature.CycleState{
		Type:   feature.CycleTweak,
		Status: feature.RepoCycleRunning,
		Count:  1,
	}
	store := newInMemoryStore(f)
	ts := &TweakSession{Store: store}

	if err := ts.MarkInterrupted(f.ID); err != nil {
		t.Fatalf("MarkInterrupted: %v", err)
	}
	if f.ActiveCycle == nil {
		t.Fatal("ActiveCycle = nil, want preserved with interrupted status")
	}
	if f.ActiveCycle.Status != "interrupted" {
		t.Errorf("ActiveCycle.Status = %q, want \"interrupted\"", f.ActiveCycle.Status)
	}
	if f.ActiveCycle.Type != feature.CycleTweak {
		t.Errorf("ActiveCycle.Type = %q, want %q (entry must remain)", f.ActiveCycle.Type, feature.CycleTweak)
	}
}

// TestTweakSession_ClearActiveCycle_WipesEntry — the success path:
// after a clean PushAll, ClearActiveCycle removes Feature.ActiveCycle so
// the feature returns to its steady published state.
func TestTweakSession_ClearActiveCycle_WipesEntry(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI)
	f.SetTweakCount(1)
	f.SetActiveCycleType(feature.CycleTweak)
	f.ActiveCycle = &feature.CycleState{
		Type:   feature.CycleTweak,
		Status: feature.RepoCycleRunning,
		Count:  1,
	}
	store := newInMemoryStore(f)
	ts := &TweakSession{Store: store}

	if err := ts.ClearActiveCycle(f.ID); err != nil {
		t.Fatalf("ClearActiveCycle: %v", err)
	}
	if f.ActiveCycle != nil {
		t.Errorf("ActiveCycle = %+v, want nil", f.ActiveCycle)
	}
	// TweakCount survives — the next tweak's artifact dir enumerates from
	// the prior count.
	if got := f.TweakCount(); got != 1 {
		t.Errorf("TweakCount after clear = %d, want 1 (count must persist for artifact enumeration)", got)
	}
}

// TestTweakSession_CommitAll_NoChanges_NoOp asserts the clean-end-with-
// no-changes path: every repo's HasUncommittedChanges returns false, no
// CommitAll is invoked, and the returned hadChanges map is empty.
// Acceptance criterion: "clean end with no changes".
func TestTweakSession_CommitAll_NoChanges_NoOp(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI, "backend", "frontend")
	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(string) (bool, error) { return false, nil }
	ts := &TweakSession{Publisher: pub}

	hadChanges, err := ts.CommitAll(f)
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if len(hadChanges) != 0 {
		t.Errorf("hadChanges = %+v, want empty (no repo had uncommitted changes)", hadChanges)
	}
	for _, c := range pub.Calls {
		if c.Method == "CommitAll" {
			t.Errorf("Publisher.CommitAll fired %d times, want 0", len(pub.Calls))
			break
		}
	}
}

// TestTweakSession_CommitAll_OneRepo_CommitsThatRepoOnly covers the
// "clean end with changes in one repo" acceptance criterion. Only the
// dirty repo gets a CommitAll; clean repos are left alone.
func TestTweakSession_CommitAll_OneRepo_CommitsThatRepoOnly(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI, "backend", "frontend")
	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(worktree string) (bool, error) {
		return worktree == "/tmp/wt/backend", nil
	}
	committedRepos := map[string]bool{}
	pub.CommitAllFn = func(worktree, _ string) error {
		switch worktree {
		case "/tmp/wt/api":
			committedRepos[testRepoNameAPI] = true
		case "/tmp/wt/backend":
			committedRepos["backend"] = true
		case "/tmp/wt/frontend":
			committedRepos["frontend"] = true
		}
		return nil
	}
	ts := &TweakSession{Publisher: pub}

	hadChanges, err := ts.CommitAll(f)
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if !hadChanges["backend"] {
		t.Errorf("hadChanges[backend] = false, want true")
	}
	if hadChanges[testRepoNameAPI] {
		t.Errorf("hadChanges[api] = true, want false (api was clean)")
	}
	if hadChanges["frontend"] {
		t.Errorf("hadChanges[frontend] = true, want false (frontend was clean)")
	}
	if !committedRepos["backend"] {
		t.Error("Publisher.CommitAll was not called against backend's worktree")
	}
	if committedRepos[testRepoNameAPI] {
		t.Error("Publisher.CommitAll fired against clean repo api")
	}
	if committedRepos["frontend"] {
		t.Error("Publisher.CommitAll fired against clean repo frontend")
	}
}

// TestTweakSession_CommitAll_MultipleRepos covers the
// "clean end with changes in multiple repos" acceptance criterion. Two
// of three repos have uncommitted changes; both get CommitAll called;
// the third (clean) is skipped.
func TestTweakSession_CommitAll_MultipleRepos(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI, "backend", "frontend")
	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(worktree string) (bool, error) {
		// api + backend are dirty; frontend is clean.
		return worktree != "/tmp/wt/frontend", nil
	}
	ts := &TweakSession{Publisher: pub}

	hadChanges, err := ts.CommitAll(f)
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if !hadChanges[testRepoNameAPI] || !hadChanges["backend"] {
		t.Errorf("hadChanges = %+v, want api+backend true", hadChanges)
	}
	if hadChanges["frontend"] {
		t.Errorf("hadChanges[frontend] = true, want false")
	}

	commitCalls := 0
	for _, c := range pub.Calls {
		if c.Method == "CommitAll" {
			commitCalls++
		}
	}
	if commitCalls != 2 {
		t.Errorf("Publisher.CommitAll calls = %d, want 2 (api + backend)", commitCalls)
	}
}

// TestTweakSession_PushAll_NoModifiedRepos_NoOp ensures PushAll is a
// no-op when CommitAll reported zero modified repos. PullRebase and
// Push must not fire — there is nothing to land.
func TestTweakSession_PushAll_NoModifiedRepos_NoOp(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI, "backend")
	pub := mocks.NewMockPublisher()
	reb := mocks.NewMockRebaseOperator()
	ts := &TweakSession{Publisher: pub, Rebaser: reb}

	if err := ts.PushAll(f, map[string]bool{}); err != nil {
		t.Errorf("PushAll: %v, want nil", err)
	}
	for _, c := range pub.Calls {
		if c.Method == "Push" {
			t.Errorf("Publisher.Push fired despite empty modifiedRepos: %+v", c)
		}
	}
	for _, c := range reb.Calls {
		if c.Method == "PullRebase" {
			t.Errorf("Rebaser.PullRebase fired despite empty modifiedRepos: %+v", c)
		}
	}
}

// TestTweakSession_PushAll_AllRepos_PullsAndPushes covers the success
// half of "clean end with changes in multiple repos": PushAll runs
// PullRebase + Push for every modified repo and returns nil.
func TestTweakSession_PushAll_AllRepos_PullsAndPushes(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI, "backend")
	pub := mocks.NewMockPublisher()
	reb := mocks.NewMockRebaseOperator()
	reb.PullRebaseFn = func(string, string) ports.PullRebaseResult {
		return ports.PullRebaseResult{Outcome: ports.PullRebaseSuccess}
	}
	ts := &TweakSession{Publisher: pub, Rebaser: reb}

	if err := ts.PushAll(f, map[string]bool{testRepoNameAPI: true, "backend": true}); err != nil {
		t.Errorf("PushAll: %v, want nil", err)
	}

	pullCalls := 0
	for _, c := range reb.Calls {
		if c.Method == "PullRebase" {
			pullCalls++
		}
	}
	if pullCalls != 2 {
		t.Errorf("Rebaser.PullRebase calls = %d, want 2 (one per modified repo)", pullCalls)
	}

	pushCalls := 0
	for _, c := range pub.Calls {
		if c.Method == "Push" {
			pushCalls++
		}
	}
	if pushCalls != 2 {
		t.Errorf("Publisher.Push calls = %d, want 2 (one per modified repo)", pushCalls)
	}
}

// TestTweakSession_PushAll_PullRebaseConflict_OneRepo_SurfacesConflict
// covers the "pull-rebase conflict in one repo surfaces
// PublishConflictError" acceptance criterion. The conflicted repo's push
// is short-circuited (we do NOT push the pre-rebase head); the clean
// repo continues and pushes successfully. The returned error is a
// *FeatureTweakPushError whose Conflicts slice carries the affected
// repo's branch + RebaseTarget.
func TestTweakSession_PushAll_PullRebaseConflict_OneRepo_SurfacesConflict(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI, "backend")
	pub := mocks.NewMockPublisher()
	reb := mocks.NewMockRebaseOperator()
	reb.PullRebaseFn = func(worktree, _ string) ports.PullRebaseResult {
		if worktree == "/tmp/wt/api" {
			return ports.PullRebaseResult{Outcome: ports.PullRebaseConflict}
		}
		return ports.PullRebaseResult{Outcome: ports.PullRebaseSuccess}
	}
	resolveCalls := 0
	ts := &TweakSession{
		Publisher: pub,
		Rebaser:   reb,
		ResolveRebaseTarget: func(_ *feature.Feature, repo *feature.FeatureRepo) string {
			resolveCalls++
			if repo.Name != testRepoNameAPI {
				t.Errorf("ResolveRebaseTarget called against %q, want \"api\"", repo.Name)
			}
			return testRebaseTargetMaster
		},
	}

	err := ts.PushAll(f, map[string]bool{testRepoNameAPI: true, "backend": true})
	if err == nil {
		t.Fatal("PushAll: expected *FeatureTweakPushError, got nil")
	}
	var ftpe *FeatureTweakPushError
	if !errors.As(err, &ftpe) {
		t.Fatalf("PushAll error = %v (%T), want errors.As(*FeatureTweakPushError)", err, err)
	}
	if len(ftpe.Conflicts) != 1 {
		t.Fatalf("ftpe.Conflicts = %d entries, want 1", len(ftpe.Conflicts))
	}
	if ftpe.Conflicts[0].RepoName != testRepoNameAPI {
		t.Errorf("Conflicts[0].RepoName = %q, want %q", ftpe.Conflicts[0].RepoName, testRepoNameAPI)
	}
	if ftpe.Conflicts[0].Branch != "feature/tweak-feat" {
		t.Errorf("Conflicts[0].Branch = %q, want %q", ftpe.Conflicts[0].Branch, "feature/tweak-feat")
	}
	if ftpe.Conflicts[0].RebaseTarget != testRebaseTargetMaster {
		t.Errorf("Conflicts[0].RebaseTarget = %q, want \"master\" (PR base must drive recovery rebase)", ftpe.Conflicts[0].RebaseTarget)
	}
	if resolveCalls != 1 {
		t.Errorf("ResolveRebaseTarget calls = %d, want 1", resolveCalls)
	}

	// The conflicted repo's push must NOT have fired; the clean repo's
	// push must have fired (siblings continue under the unified flow).
	pushedTo := map[string]bool{}
	for _, c := range pub.Calls {
		if c.Method == "Push" {
			if wt, ok := c.Args[0].(string); ok {
				pushedTo[wt] = true
			}
		}
	}
	if pushedTo["/tmp/wt/api"] {
		t.Error("Publisher.Push fired for the conflicted repo (should be short-circuited)")
	}
	if !pushedTo["/tmp/wt/backend"] {
		t.Error("Publisher.Push did NOT fire for the clean repo (siblings should continue)")
	}
}

// TestTweakSession_PushAll_DeterministicConflictOrder ensures the
// FeatureTweakPushError surfaces conflicts in alphabetical order (by
// repo name), so the orchestrator's "first conflict" routing is
// deterministic regardless of map iteration order.
func TestTweakSession_PushAll_DeterministicConflictOrder(t *testing.T) {
	f := newFeatureForTweak("zeta", "alpha", "mu")
	reb := mocks.NewMockRebaseOperator()
	reb.PullRebaseFn = func(string, string) ports.PullRebaseResult {
		return ports.PullRebaseResult{Outcome: ports.PullRebaseConflict}
	}
	ts := &TweakSession{
		Publisher: mocks.NewMockPublisher(),
		Rebaser:   reb,
		ResolveRebaseTarget: func(_ *feature.Feature, _ *feature.FeatureRepo) string {
			return testRebaseTargetMaster
		},
	}

	err := ts.PushAll(f, map[string]bool{"zeta": true, "alpha": true, "mu": true})
	if err == nil {
		t.Fatal("PushAll: expected error, got nil")
	}
	var ftpe *FeatureTweakPushError
	if !errors.As(err, &ftpe) {
		t.Fatalf("PushAll error type = %T, want *FeatureTweakPushError", err)
	}
	if len(ftpe.Conflicts) != 3 {
		t.Fatalf("Conflicts = %d, want 3", len(ftpe.Conflicts))
	}
	want := []string{"alpha", "mu", "zeta"}
	for i, c := range ftpe.Conflicts {
		if c.RepoName != want[i] {
			t.Errorf("Conflicts[%d].RepoName = %q, want %q (alphabetical)", i, c.RepoName, want[i])
		}
	}
}

// TestTweakSession_PushAll_PushFailure_RecordsAsFailure covers a non-
// conflict push error: the failure lands in FeatureTweakPushError.Failures
// rather than Conflicts, so the orchestrator does not route through the
// rebase UX for a transient push error.
func TestTweakSession_PushAll_PushFailure_RecordsAsFailure(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI)
	pub := mocks.NewMockPublisher()
	pub.PushFn = func(string, string) error { return errors.New("auth refused") }
	reb := mocks.NewMockRebaseOperator()
	reb.PullRebaseFn = func(string, string) ports.PullRebaseResult {
		return ports.PullRebaseResult{Outcome: ports.PullRebaseSuccess}
	}
	ts := &TweakSession{Publisher: pub, Rebaser: reb}

	err := ts.PushAll(f, map[string]bool{testRepoNameAPI: true})
	if err == nil {
		t.Fatal("PushAll: expected error, got nil")
	}
	var ftpe *FeatureTweakPushError
	if !errors.As(err, &ftpe) {
		t.Fatalf("PushAll error type = %T, want *FeatureTweakPushError", err)
	}
	if len(ftpe.Conflicts) != 0 {
		t.Errorf("Conflicts = %d, want 0 (push failure is NOT a conflict)", len(ftpe.Conflicts))
	}
	if len(ftpe.Failures) != 1 {
		t.Fatalf("Failures = %d, want 1", len(ftpe.Failures))
	}
	if ftpe.Failures[0].RepoName != testRepoNameAPI {
		t.Errorf("Failures[0].RepoName = %q, want \"api\"", ftpe.Failures[0].RepoName)
	}
}

// TestTweakSession_SessionDieMidTweak_TransitionsToInterrupted is the
// "session-die-mid-tweak transitions to interrupted" acceptance
// criterion. The deep module's MarkInterrupted is the entry point for
// crash recovery — when the harness sees Feature.ActiveCycle.Status
// "running" with no live session, it calls this to expose "resume tweak".
//
// The test simulates the lifecycle: MarkRunning stamps the entry, the
// session dies (no explicit MarkFailed/ClearActiveCycle), and crash
// recovery calls MarkInterrupted. The cycle entry remains so the TUI
// can offer resume.
func TestTweakSession_SessionDieMidTweak_TransitionsToInterrupted(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI, "backend")
	store := newInMemoryStore(f)
	ts := &TweakSession{Store: store}

	// Step 1: orchestrator stamps ActiveCycle running.
	if _, err := ts.MarkRunning(f.ID); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if f.ActiveCycle == nil || f.ActiveCycle.Status != feature.RepoCycleRunning {
		t.Fatalf("after MarkRunning: ActiveCycle = %+v, want running", f.ActiveCycle)
	}

	// Step 2: session dies — process exits without success. Crash
	// recovery sees Status: running with no live session.
	if err := ts.MarkInterrupted(f.ID); err != nil {
		t.Fatalf("MarkInterrupted: %v", err)
	}

	// Step 3: assert the entry transitioned to "interrupted" but is
	// preserved (TUI needs it to offer "resume tweak").
	if f.ActiveCycle == nil {
		t.Fatal("ActiveCycle = nil after MarkInterrupted; TUI cannot offer \"resume tweak\"")
	}
	if f.ActiveCycle.Status != "interrupted" {
		t.Errorf("ActiveCycle.Status = %q, want \"interrupted\"", f.ActiveCycle.Status)
	}
	if f.ActiveCycle.Type != feature.CycleTweak {
		t.Errorf("ActiveCycle.Type = %q, want %q (entry must remain so TUI can resume)", f.ActiveCycle.Type, feature.CycleTweak)
	}

	// Step 4: a fresh session starts via MarkRunning. The deep module
	// re-stamps Status: running and bumps TweakCount (the existing entry
	// is "interrupted", not "running", so the idempotency short-circuit
	// does NOT fire).
	if _, err := ts.MarkRunning(f.ID); err != nil {
		t.Fatalf("MarkRunning after interrupt: %v", err)
	}
	if f.TweakCount() != 2 {
		t.Errorf("TweakCount after resume = %d, want 2 (resume = fresh session)", f.TweakCount())
	}
	if f.ActiveCycle.Status != feature.RepoCycleRunning {
		t.Errorf("ActiveCycle.Status after resume = %q, want %q", f.ActiveCycle.Status, feature.RepoCycleRunning)
	}
}

// TestTweakSession_FullLifecycle_CleanEnd_NoChanges is the integration
// test for the "clean end with no changes" acceptance criterion across
// the full deep-module API: MarkRunning → CommitAll (no changes) →
// PushAll (no-op) → ClearActiveCycle. No commit, no push.
func TestTweakSession_FullLifecycle_CleanEnd_NoChanges(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI, "backend")
	store := newInMemoryStore(f)
	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(string) (bool, error) { return false, nil }
	reb := mocks.NewMockRebaseOperator()
	ts := &TweakSession{Store: store, Publisher: pub, Rebaser: reb}

	if _, err := ts.MarkRunning(f.ID); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	hadChanges, err := ts.CommitAll(f)
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if len(hadChanges) != 0 {
		t.Errorf("hadChanges = %+v, want empty", hadChanges)
	}
	if err := ts.PushAll(f, hadChanges); err != nil {
		t.Errorf("PushAll: %v, want nil", err)
	}
	if err := ts.ClearActiveCycle(f.ID); err != nil {
		t.Fatalf("ClearActiveCycle: %v", err)
	}
	if f.ActiveCycle != nil {
		t.Errorf("ActiveCycle = %+v after clean lifecycle, want nil", f.ActiveCycle)
	}

	// No push, no commit fired during the lifecycle.
	for _, c := range pub.Calls {
		if c.Method == "Push" || c.Method == "CommitAll" {
			t.Errorf("Publisher.%s fired during clean lifecycle: %+v", c.Method, c)
		}
	}
}

// TestTweakSession_FullLifecycle_CleanEnd_OneRepo wires a happy-path
// "clean end with changes in one repo": MarkRunning → CommitAll
// (one repo dirty) → PushAll (one repo) → ClearActiveCycle. Exactly one
// CommitAll, one PullRebase, one Push.
func TestTweakSession_FullLifecycle_CleanEnd_OneRepo(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI, "backend")
	store := newInMemoryStore(f)
	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(worktree string) (bool, error) {
		return worktree == "/tmp/wt/api", nil
	}
	reb := mocks.NewMockRebaseOperator()
	reb.PullRebaseFn = func(string, string) ports.PullRebaseResult {
		return ports.PullRebaseResult{Outcome: ports.PullRebaseSuccess}
	}
	ts := &TweakSession{Store: store, Publisher: pub, Rebaser: reb}

	if _, err := ts.MarkRunning(f.ID); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	hadChanges, err := ts.CommitAll(f)
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if !hadChanges[testRepoNameAPI] || hadChanges["backend"] {
		t.Errorf("hadChanges = %+v, want only api dirty", hadChanges)
	}
	if err := ts.PushAll(f, hadChanges); err != nil {
		t.Errorf("PushAll: %v, want nil", err)
	}
	if err := ts.ClearActiveCycle(f.ID); err != nil {
		t.Fatalf("ClearActiveCycle: %v", err)
	}

	commitCalls := 0
	pushCalls := 0
	for _, c := range pub.Calls {
		switch c.Method {
		case "CommitAll":
			commitCalls++
		case "Push":
			pushCalls++
		}
	}
	if commitCalls != 1 {
		t.Errorf("CommitAll calls = %d, want 1", commitCalls)
	}
	if pushCalls != 1 {
		t.Errorf("Push calls = %d, want 1", pushCalls)
	}

	pullCalls := 0
	for _, c := range reb.Calls {
		if c.Method == "PullRebase" {
			pullCalls++
		}
	}
	if pullCalls != 1 {
		t.Errorf("PullRebase calls = %d, want 1", pullCalls)
	}
}

// TestTweakSession_FullLifecycle_CleanEnd_MultipleRepos wires a happy-
// path "clean end with changes in multiple repos": MarkRunning →
// CommitAll (two of three dirty) → PushAll (two repos pulled+pushed) →
// ClearActiveCycle. Two CommitAll, two PullRebase, two Push.
func TestTweakSession_FullLifecycle_CleanEnd_MultipleRepos(t *testing.T) {
	f := newFeatureForTweak(testRepoNameAPI, "backend", "frontend")
	store := newInMemoryStore(f)
	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(worktree string) (bool, error) {
		return worktree != "/tmp/wt/frontend", nil
	}
	reb := mocks.NewMockRebaseOperator()
	reb.PullRebaseFn = func(string, string) ports.PullRebaseResult {
		return ports.PullRebaseResult{Outcome: ports.PullRebaseSuccess}
	}
	ts := &TweakSession{Store: store, Publisher: pub, Rebaser: reb}

	if _, err := ts.MarkRunning(f.ID); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	hadChanges, err := ts.CommitAll(f)
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if !hadChanges[testRepoNameAPI] || !hadChanges["backend"] {
		t.Errorf("hadChanges = %+v, want api+backend true", hadChanges)
	}
	if hadChanges["frontend"] {
		t.Errorf("hadChanges[frontend] = true, want false (frontend was clean)")
	}
	if err := ts.PushAll(f, hadChanges); err != nil {
		t.Errorf("PushAll: %v, want nil", err)
	}
	if err := ts.ClearActiveCycle(f.ID); err != nil {
		t.Fatalf("ClearActiveCycle: %v", err)
	}

	commitCalls := 0
	pushCalls := 0
	for _, c := range pub.Calls {
		switch c.Method {
		case "CommitAll":
			commitCalls++
		case "Push":
			pushCalls++
		}
	}
	if commitCalls != 2 {
		t.Errorf("CommitAll calls = %d, want 2 (api + backend)", commitCalls)
	}
	if pushCalls != 2 {
		t.Errorf("Push calls = %d, want 2 (api + backend)", pushCalls)
	}

	pullCalls := 0
	for _, c := range reb.Calls {
		if c.Method == "PullRebase" {
			pullCalls++
		}
	}
	if pullCalls != 2 {
		t.Errorf("PullRebase calls = %d, want 2 (api + backend)", pullCalls)
	}
}
