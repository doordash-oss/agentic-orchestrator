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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// childFakeWorktrees is a minimal WorktreeOps fake that also resolves the
// exact head SHA per path.
type childFakeWorktrees struct {
	heads map[string]string
}

func (f *childFakeWorktrees) Create(repoPath, featureSlug, repoName, startPoint string) (string, error) {
	return "", nil
}
func (f *childFakeWorktrees) Remove(string, bool) error             { return nil }
func (f *childFakeWorktrees) ResetToBase(string, string) error      { return nil }
func (f *childFakeWorktrees) ResetToBaseLocal(string, string) error { return nil }
func (f *childFakeWorktrees) ResetToCommit(string, string) error    { return nil }
func (f *childFakeWorktrees) ExpectedPath(slug, repo string) string { return "" }
func (f *childFakeWorktrees) CurrentHeadSHA(p string) (string, error) {
	sha, ok := f.heads[p]
	if !ok || sha == "" {
		return "0000000000000000000000000000000000000000", nil
	}
	return sha, nil
}

func newChildTestManager(t *testing.T, heads map[string]string, clean feature.CleanlinessOps) *feature.Manager {
	t.Helper()
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = &childFakeWorktrees{heads: heads}
	mgr.Branches = newMockBranches(false)
	mgr.Cleanliness = clean
	return mgr
}

// cleanEverywhere is a CleanlinessOps fake reporting every worktree clean.
func cleanEverywhere() feature.CleanlinessOps {
	return feature.CleanlinessFunc(func(string, int) (*feature.RepoCleanliness, error) {
		return &feature.RepoCleanliness{}, nil
	})
}

func saveChildTestParent(t *testing.T, mgr *feature.Manager, f *feature.Feature) {
	t.Helper()
	if f.ActiveRun == 0 {
		f.ActiveRun = 1
	}
	if f.RunCount == 0 {
		f.RunCount = 1
	}
	if err := mgr.Store.Save(f); err != nil {
		t.Fatalf("saving parent: %v", err)
	}
}

func childTestSpec() feature.RefactorChildSpec {
	return feature.RefactorChildSpec{
		Name:        "Refactor Widget",
		Description: "rework the widget",
		Pipeline:    feature.PipelineMoonshot,
		Checkpoints: feature.Checkpoints{PhasePlanReview: true, ManualPublish: true},
		RiskLevel:   feature.RiskMedium,
	}
}

func TestCreateRefactorChildRejectsIneligibleParents(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp store and fakes isolate state.
	mgr := newChildTestManager(t, map[string]string{"/wt/repo": "aaaa"}, cleanEverywhere())

	t.Run("missing parent", func(t *testing.T) {
		_, err := mgr.CreateRefactorChild("nope", childTestSpec())
		if !errors.Is(err, feature.ErrRefactorParentNotFound) {
			t.Fatalf("err = %v, want ErrRefactorParentNotFound", err)
		}
	})

	t.Run("ineligible status", func(t *testing.T) {
		saveChildTestParent(t, mgr, &feature.Feature{ID: "p-status", Slug: "p-status", Status: feature.StatusImplementing})
		_, err := mgr.CreateRefactorChild("p-status", childTestSpec())
		if !errors.Is(err, feature.ErrRefactorParentStatusIneligible) {
			t.Fatalf("err = %v, want ErrRefactorParentStatusIneligible", err)
		}
	})

	t.Run("parent is a child", func(t *testing.T) {
		saveChildTestParent(t, mgr, &feature.Feature{
			ID:     "p-child",
			Slug:   "p-child",
			Status: feature.StatusPublished,
			Parent: &feature.ChildRelationship{ParentID: "someone", Kind: feature.ChildKindRefactor},
		})
		_, err := mgr.CreateRefactorChild("p-child", childTestSpec())
		if !errors.Is(err, feature.ErrRefactorParentIsChild) {
			t.Fatalf("err = %v, want ErrRefactorParentIsChild", err)
		}
	})

	t.Run("active child exists", func(t *testing.T) {
		saveChildTestParent(t, mgr, &feature.Feature{ID: "p-busy", Slug: "p-busy", Status: feature.StatusPublished})
		saveChildTestParent(t, mgr, &feature.Feature{
			ID:     "c-existing",
			Slug:   "c-existing",
			Status: feature.StatusCreated,
			Parent: &feature.ChildRelationship{ParentID: "p-busy", Kind: feature.ChildKindRefactor},
		})
		_, err := mgr.CreateRefactorChild("p-busy", childTestSpec())
		var conflict *feature.ActiveChildExistsError
		if !errors.As(err, &conflict) {
			t.Fatalf("err = %v, want ActiveChildExistsError", err)
		}
		if conflict.ChildID != "c-existing" {
			t.Fatalf("conflict child = %q, want c-existing", conflict.ChildID)
		}
	})

	t.Run("closed child does not block", func(t *testing.T) {
		saveChildTestParent(t, mgr, &feature.Feature{ID: "p-free", Slug: "p-free", Status: feature.StatusCodeReady})
		closedAt := time.Now()
		saveChildTestParent(t, mgr, &feature.Feature{
			ID:     "c-closed",
			Slug:   "c-closed",
			Status: feature.StatusDone,
			Parent: &feature.ChildRelationship{
				ParentID:     "p-free",
				Kind:         feature.ChildKindRefactor,
				CloseOutcome: feature.ChildCloseOutcomeCompleted,
				ClosedAt:     &closedAt,
			},
		})
		if _, err := mgr.CreateRefactorChild("p-free", childTestSpec()); err != nil {
			t.Fatalf("create with closed child: %v", err)
		}
	})
}

func TestCreateRefactorChildPersistsRelationshipAndIntent(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp store and fakes isolate state.
	heads := map[string]string{
		"/wt/repo-a": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"/wt/repo-b": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	mgr := newChildTestManager(t, heads, cleanEverywhere())
	saveChildTestParent(t, mgr, &feature.Feature{
		ID:     "parent-1",
		Slug:   "parent-1",
		Status: feature.StatusPublished,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/src/repo-a", WorktreePath: "/wt/repo-a", Branch: "feature/parent-1-x", BaseBranch: "main"},
			{Name: "repo-b", Path: "/src/repo-b", WorktreePath: "/wt/repo-b", Branch: "feature/parent-1-x", BaseBranch: "main"},
		},
		Checkpoints:  feature.Checkpoints{},
		Pipeline:     feature.PipelineMoonshot,
		Effort:       config.EffortConfig{Planning: "low"},
		ExitCriteria: "parent exit criteria",
	})
	// Submit a non-default Review configuration (effort and exit criteria
	// deliberately unset so they inherit the parent's settings).
	spec := childTestSpec()
	spec.Models = config.ModelConfig{Planning: "submitted/planning-model"}
	spec.Inquireness = feature.InquirenessHigh
	child, err := mgr.CreateRefactorChild("parent-1", spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !child.IsChild() || child.Parent.ParentID != "parent-1" || child.Parent.Kind != feature.ChildKindRefactor {
		t.Fatalf("child relationship = %+v", child.Parent)
	}
	if child.Status != feature.StatusSettingUpWorktrees {
		t.Fatalf("child status = %v, want SettingUpWorktrees", child.Status)
	}
	if child.Parent.CloseOutcome != "" {
		t.Fatalf("close outcome = %q, want empty (active)", child.Parent.CloseOutcome)
	}
	// Exact per-repo provenance.
	if got := child.BaseSHA("repo-a"); got != heads["/wt/repo-a"] {
		t.Fatalf("base sha repo-a = %q", got)
	}
	if child.Parent.Bases[0].ParentBranch != "feature/parent-1-x" {
		t.Fatalf("parent branch provenance = %+v", child.Parent.Bases[0])
	}
	// Inherits repos in order with unique child branch identities.
	if len(child.Repos) != 2 || child.Repos[0].Name != "repo-a" || child.Repos[1].Name != "repo-b" {
		t.Fatalf("repos = %+v", child.Repos)
	}
	if child.Repos[0].Branch == "" || child.Repos[0].Branch == "feature/parent-1-x" {
		t.Fatalf("child branch not unique: %q", child.Repos[0].Branch)
	}
	if child.Repos[0].WorktreePath != "" {
		t.Fatalf("child worktree path must be deferred to setup, got %q", child.Repos[0].WorktreePath)
	}
	// Queued setup tasks point at the exact captured tip.
	setup := child.Run().Setup
	if setup == nil {
		t.Fatal("setup intent missing")
	}
	task := setup.Tasks["worktree:repo-a"]
	if task.StartPoint != heads["/wt/repo-a"] || task.ExactSHA != heads["/wt/repo-a"] {
		t.Fatalf("worktree task = %+v", task)
	}

	// Relationship fields, intent state, and provenance survive save/load.
	loaded, err := mgr.Store.Load(child.ID)
	if err != nil {
		t.Fatalf("reload child: %v", err)
	}
	if loaded.Parent == nil || loaded.BaseSHA("repo-b") != heads["/wt/repo-b"] {
		t.Fatalf("reloaded relationship = %+v", loaded.Parent)
	}
	loadedTask := loaded.Run().Setup.Tasks["worktree:repo-b"]
	if loadedTask.ExactSHA != heads["/wt/repo-b"] || loadedTask.StartPoint != heads["/wt/repo-b"] {
		t.Fatalf("reloaded task = %+v", loadedTask)
	}

	// The submitted Review configuration landed on both parent and child;
	// the parent pipeline was left untouched (child pipeline independent).
	parent, err := mgr.Store.Load("parent-1")
	if err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	if parent.PendingChild != nil {
		t.Fatalf("durable intent must be cleared after successful creation")
	}
	if child.Pipeline != feature.PipelineMoonshot || parent.Pipeline != feature.PipelineMoonshot {
		t.Fatalf("pipeline child=%v parent=%v; want both moonshot", child.Pipeline, parent.Pipeline)
	}
	// Every shared Review field must be identical on the reloaded parent
	// and child — submitted values (checkpoints, models, risk, inquireness)
	// on both, inherited values (effort, exit criteria) preserving the
	// parent's settings.
	assertSharedReviewConfigMatches(t, parent, child)
	assertSharedReviewConfigMatches(t, parent, loaded)
	if !parent.Checkpoints.PhasePlanReview || !parent.Checkpoints.ManualPublish {
		t.Fatalf("parent checkpoints = %+v, want submitted gates", parent.Checkpoints)
	}
	if parent.Models.Planning != "submitted/planning-model" || parent.RiskLevel != feature.RiskMedium ||
		parent.Inquireness != feature.InquirenessHigh {
		t.Fatalf("parent submitted review fields = models %+v risk %v inquireness %v", parent.Models, parent.RiskLevel, parent.Inquireness)
	}
	if parent.Effort.Planning != "low" || parent.ExitCriteria != "parent exit criteria" {
		t.Fatalf("parent inherited review fields = effort %+v exit %q", parent.Effort, parent.ExitCriteria)
	}
}

// assertSharedReviewConfigMatches fails when the complete shared Review
// configuration (gates, models, effort, risk, exit criteria, inquiry
// behavior) differs between the reloaded parent and child records.
func assertSharedReviewConfigMatches(t *testing.T, parent, child *feature.Feature) {
	t.Helper()
	if parent.Checkpoints != child.Checkpoints {
		t.Fatalf("checkpoints parent=%+v child=%+v", parent.Checkpoints, child.Checkpoints)
	}
	if parent.Models != child.Models {
		t.Fatalf("models parent=%+v child=%+v", parent.Models, child.Models)
	}
	if parent.Effort != child.Effort {
		t.Fatalf("effort parent=%+v child=%+v", parent.Effort, child.Effort)
	}
	if parent.RiskLevel != child.RiskLevel {
		t.Fatalf("risk parent=%v child=%v", parent.RiskLevel, child.RiskLevel)
	}
	if parent.ExitCriteria != child.ExitCriteria {
		t.Fatalf("exit criteria parent=%q child=%q", parent.ExitCriteria, child.ExitCriteria)
	}
	if parent.Inquireness != child.Inquireness {
		t.Fatalf("inquireness parent=%v child=%v", parent.Inquireness, child.Inquireness)
	}
}

func TestCreateRefactorChildDirtyParentBlocksEverything(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp store and fakes isolate state.
	mgr := newChildTestManager(t, nil, feature.CleanlinessFunc(func(path string, max int) (*feature.RepoCleanliness, error) {
		if strings.Contains(path, "dirty") {
			return &feature.RepoCleanliness{
				Staged:         []string{"a.go"},
				Untracked:      []string{"tmp.txt"},
				StagedTotal:    1,
				UntrackedTotal: 1,
			}, nil
		}
		return &feature.RepoCleanliness{}, nil
	}))
	saveChildTestParent(t, mgr, &feature.Feature{
		ID:     "parent-dirty",
		Slug:   "parent-dirty",
		Status: feature.StatusPublished,
		Repos: []feature.FeatureRepo{
			{Name: "clean-repo", Path: "/src/clean", WorktreePath: "/wt/clean"},
			{Name: "dirty-repo", Path: "/src/dirty", WorktreePath: "/wt/dirty"},
		},
	})

	_, err := mgr.CreateRefactorChild("parent-dirty", childTestSpec())
	var dirtyErr *feature.ParentWorktreesDirtyError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("err = %v, want ParentWorktreesDirtyError", err)
	}
	if len(dirtyErr.Repos) != 1 || dirtyErr.Repos[0].Repo != "dirty-repo" {
		t.Fatalf("dirty diagnostics = %+v", dirtyErr.Repos)
	}
	if dirtyErr.Repos[0].StagedTotal != 1 || len(dirtyErr.Repos[0].Untracked) != 1 {
		t.Fatalf("dirty diagnostics = %+v", dirtyErr.Repos[0])
	}
	// No child state was persisted.
	features, err := mgr.Store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, f := range features {
		if f.IsChild() {
			t.Fatalf("dirty launch must not persist any child: %+v", f.Parent)
		}
	}
}

func TestCreateRefactorChildFailsWithoutCleanlinessAdapter(t *testing.T) {
	t.Parallel()
	// A nil Cleanliness adapter must fail the launch explicitly — the
	// dirty-worktree safety check is mandatory and never silently skipped.
	mgr := newChildTestManager(t, nil, nil)
	saveChildTestParent(t, mgr, &feature.Feature{ID: "parent-noclean", Slug: "parent-noclean", Status: feature.StatusPublished})

	_, err := mgr.CreateRefactorChild("parent-noclean", childTestSpec())
	if err == nil || !strings.Contains(err.Error(), "cleanliness inspection is not configured") {
		t.Fatalf("err = %v, want explicit cleanliness-not-configured failure", err)
	}
	features, lerr := mgr.Store.List()
	if lerr != nil {
		t.Fatalf("list: %v", lerr)
	}
	for _, f := range features {
		if f.IsChild() {
			t.Fatalf("failed launch must not persist any child: %+v", f.Parent)
		}
	}
}

func TestCreateRefactorChildFailsClosedOnUnreadableCandidate(t *testing.T) {
	t.Parallel()
	// Regression: the creation-integrity scan must fail closed. A stored
	// record that cannot be loaded could be an active child (the parent
	// pointer is child-owned, discovered only by loading), so the launch
	// must error instead of risking a second active child.
	mgr := newChildTestManager(t, map[string]string{"/wt/repo": "aaaa"}, cleanEverywhere())
	saveChildTestParent(t, mgr, &feature.Feature{ID: "parent-corrupt-scan", Slug: "parent-corrupt-scan", Status: feature.StatusPublished})

	// Plant an unreadable feature record alongside the parent.
	corruptDir := filepath.Join(mgr.Store.BaseDir, "c-corrupt")
	if err := os.MkdirAll(corruptDir, 0o755); err != nil {
		t.Fatalf("mkdir corrupt record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "feature.yaml"), []byte("[unterminated"), 0o644); err != nil {
		t.Fatalf("write corrupt record: %v", err)
	}

	_, err := mgr.CreateRefactorChild("parent-corrupt-scan", childTestSpec())
	if err == nil || !strings.Contains(err.Error(), "c-corrupt") {
		t.Fatalf("err = %v, want fail-closed error naming the unreadable record", err)
	}
	// No second active child was created. List keeps its partial-load
	// tolerance, so the corrupt record itself surfaces only as a warning.
	features, lerr := mgr.Store.List()
	if lerr != nil && !feature.IsPartialLoadError(lerr) {
		t.Fatalf("list: %v", lerr)
	}
	for _, f := range features {
		if f.IsChild() && f.Parent.ParentID == "parent-corrupt-scan" {
			t.Fatalf("unreadable candidate must block the launch; got child %+v", f.Parent)
		}
	}
}

func TestCreateRefactorChildConcurrentLaunchesHaveOneWinner(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp store; goroutines contend only on the store mutex.
	mgr := newChildTestManager(t, nil, cleanEverywhere())
	saveChildTestParent(t, mgr, &feature.Feature{ID: "parent-race", Slug: "parent-race", Status: feature.StatusPublished})

	const launches = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	conflicts := 0
	for i := 0; i < launches; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := mgr.CreateRefactorChild("parent-race", childTestSpec())
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
				return
			}
			var conflict *feature.ActiveChildExistsError
			if errors.As(err, &conflict) {
				conflicts++
			} else {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || conflicts != launches-1 {
		t.Fatalf("wins = %d, conflicts = %d; want 1 and %d", wins, conflicts, launches-1)
	}
	// Exactly one child on disk.
	features, err := mgr.Store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	children := 0
	for _, f := range features {
		if f.IsChild() {
			children++
		}
	}
	if children != 1 {
		t.Fatalf("children on disk = %d, want 1", children)
	}
}

// TestModifyGuardedSerializesWithCreateChildLocked verifies that the guard
// check inside ModifyGuarded and the child creation inside CreateChildLocked
// are serialized under the same store mutex. When both run concurrently, the
// invariant holds: the guard either sees the child (and rejects the mutation)
// or the mutation completes before the child is created (and the child
// creation observes the mutated parent). There is no time-of-check/time-of-use
// gap where a mutation lands while a child exists without the guard having
// seen it.
func TestModifyGuardedSerializesWithCreateChildLocked(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp store; goroutines contend only on
	// the store mutex.
	const iterations = 50
	for i := 0; i < iterations; i++ {
		mgr := newChildTestManager(t, nil, cleanEverywhere())
		parent := &feature.Feature{
			ID:       "parent-guarded",
			Slug:     "parent-guarded",
			Status:   feature.StatusPublished,
			Pipeline: feature.PipelineMoonshot,
		}
		saveChildTestParent(t, mgr, parent)

		var wg sync.WaitGroup
		wg.Add(2)

		var modifyErr error
		var childCreated bool

		// ModifyGuarded: guard rejects when activeChild != nil, otherwise
		// transitions the parent to Done.
		go func() {
			defer wg.Done()
			modifyErr = mgr.Store.ModifyGuarded("parent-guarded",
				func(f *feature.Feature, activeChild *feature.Feature) error {
					if activeChild != nil {
						return fmt.Errorf("parent has active child")
					}
					return nil
				},
				func(f *feature.Feature) error {
					return f.Transition(feature.StatusDone)
				},
			)
		}()

		// CreateChildLocked: creates a child if no active child exists and
		// the parent is still Published (eligible for child creation).
		go func() {
			defer wg.Done()
			child, err := mgr.Store.CreateChildLocked("parent-guarded",
				func(p *feature.Feature, activeChild *feature.Feature) (*feature.Feature, *feature.ChildCreationIntent, error) {
					if activeChild != nil {
						return nil, nil, fmt.Errorf("active child exists")
					}
					if p.Status != feature.StatusPublished {
						return nil, nil, fmt.Errorf("parent not eligible: %s", p.Status)
					}
					c := &feature.Feature{
						ID:     "child-guarded",
						Slug:   "child-guarded",
						Status: feature.StatusCreated,
						Parent: &feature.ChildRelationship{
							ParentID: "parent-guarded",
							Kind:     feature.ChildKindRefactor,
						},
					}
					if c.ActiveRun == 0 {
						c.ActiveRun = 1
					}
					if c.RunCount == 0 {
						c.RunCount = 1
					}
					intent := &feature.ChildCreationIntent{ChildID: c.ID}
					return c, intent, nil
				},
			)
			_ = err
			childCreated = child != nil
		}()

		wg.Wait()

		// Invariant: both cannot succeed. If ModifyGuarded's guard saw no
		// child (mutation succeeded), CreateChildLocked must have seen the
		// parent as Done (not Published) and failed. If CreateChildLocked
		// succeeded, ModifyGuarded's guard must have seen the child and
		// the mutation must have failed.
		if modifyErr == nil && childCreated {
			t.Fatalf("iteration %d: both ModifyGuarded and CreateChildLocked succeeded — serialization broken", i)
		}

		// Verify the on-disk state is consistent.
		features, err := mgr.Store.List()
		if err != nil {
			t.Fatalf("iteration %d: list: %v", i, err)
		}
		for _, f := range features {
			if f.IsChild() && f.Parent != nil && f.Parent.ParentID == "parent-guarded" {
				// A child exists — the parent must NOT be Done (the guard
				// would have blocked the transition).
				p, err := mgr.Store.Load("parent-guarded")
				if err != nil {
					t.Fatalf("iteration %d: load parent: %v", i, err)
				}
				if p.Status == feature.StatusDone {
					t.Fatalf("iteration %d: child exists but parent is Done — guard did not block", i)
				}
			}
		}
	}
}

// TestPairedConfigRearmSaveFailurePropagatesBothErrors verifies that when the
// first parent save fails, the error properly wraps the failure and the
// revert path succeeds — the re-arm error is not silently discarded.
func TestPairedConfigRearmSaveFailurePropagatesBothErrors(t *testing.T) {
	t.Parallel()
	mgr := newChildTestManager(t, nil, cleanEverywhere())
	parent := &feature.Feature{
		ID:       "p-rearm",
		Slug:     "p-rearm",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
	}
	saveChildTestParent(t, mgr, parent)

	child, err := mgr.CreateRefactorChild("p-rearm", childTestSpec())
	if err != nil {
		t.Fatalf("CreateRefactorChild: %v", err)
	}
	_ = child

	// The save sequence in UpdatePairedConfig is:
	//   1. Save child (new config) — succeeds
	//   2. Save parent (clear intent) — FAIL (FailOnCall=2)
	//   3. Save child (revert to old) — succeeds
	//   4. Save parent (revert in memory, clear intent) — succeeds
	// When all steps after the failure succeed, the error should say
	// "both records reverted to prior config" and the parent's config
	// should be back to its original value.
	hook := &feature.StoreSaveHook{
		FailOnFeatureID: "p-rearm",
		FailOnCall:      3, // Fail the parent save that clears the intent (step 3)
	}
	mgr.Store.SetSaveHook(hook)

	input := feature.PairedConfigInput{
		Models:              config.ModelConfig{Implementation: "new-model"},
		Effort:              config.EffortConfig{Planning: "high"},
		Inquireness:         feature.InquirenessHigh,
		Checkpoints:         feature.Checkpoints{PhasePlanReview: true, ManualPublish: true},
		InputNotifications:  feature.InputNotificationsEnabled,
		AutomaticReviewMode: feature.AutomaticReviewDefault,
	}
	result, err := mgr.Store.UpdatePairedConfig("p-rearm", input, feature.PipelineMoonshot, "p-rearm")
	_ = result
	if err == nil {
		t.Fatal("expected error from UpdatePairedConfig, got nil")
	}
	if !strings.Contains(err.Error(), "saving parent config") {
		t.Fatalf("error should mention parent config save failure: %v", err)
	}
	if !strings.Contains(err.Error(), "reverted") {
		t.Fatalf("error should mention revert: %v", err)
	}

	// Verify the parent's config was actually reverted (not left in a
	// split state with the child having the new config).
	mgr.Store.ResetSaveHook()
	p, err := mgr.Store.Load("p-rearm")
	if err != nil {
		t.Fatalf("loading parent: %v", err)
	}
	if p.Models.Implementation == "new-model" {
		t.Fatal("parent config should have been reverted, but still has new-model")
	}
}

// reuseWorktrees fakes the worktree adapter for setup-runner reuse checks:
// it reports fixed HEAD SHAs per path and records Create calls.
type reuseWorktrees struct {
	heads   map[string]string
	created bool
}

func (f *reuseWorktrees) Create(repoPath, featureSlug, repoName, startPoint string) (string, error) {
	f.created = true
	return "", nil
}
func (f *reuseWorktrees) Remove(string, bool) error             { return nil }
func (f *reuseWorktrees) ResetToBase(string, string) error      { return nil }
func (f *reuseWorktrees) ResetToBaseLocal(string, string) error { return nil }
func (f *reuseWorktrees) ResetToCommit(string, string) error    { return nil }
func (f *reuseWorktrees) ExpectedPath(slug, repo string) string { return "" }
func (f *reuseWorktrees) CurrentHeadSHA(p string) (string, error) {
	return f.heads[p], nil
}

func TestRunSetupValidatesExactBaseOnReuse(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and fakes isolate state.
	const sha = "dddddddddddddddddddddddddddddddddddddddd"

	newManager := func(t *testing.T) (*feature.Manager, *reuseWorktrees, string) {
		mgr := newChildTestManager(t, nil, cleanEverywhere())
		wt := &reuseWorktrees{heads: map[string]string{}}
		mgr.Worktrees = wt
		saveChildTestParent(t, mgr, &feature.Feature{
			ID:     "p-reuse",
			Slug:   "p-reuse",
			Status: feature.StatusPublished,
			Repos: []feature.FeatureRepo{
				{Name: "repo-a", Path: "/src/repo-a", WorktreePath: "/wt/repo-a", BaseBranch: "main"},
			},
		})
		wt.heads["/wt/repo-a"] = sha
		child, err := mgr.CreateRefactorChild("p-reuse", childTestSpec())
		if err != nil {
			t.Fatalf("create child: %v", err)
		}
		mgr.Branches.(*mocks.MockBranchOperator).CurrentBranchFn = func(string) (string, error) {
			return child.Repos[0].Branch, nil
		}
		// Pretend a previous attempt created the worktree at the expected path.
		wtPath := t.TempDir()
		taskKey := "worktree:repo-a"
		if err := mgr.Store.Modify(child.ID, func(f *feature.Feature) error {
			task := f.Run().Setup.Tasks[taskKey]
			task.Path = wtPath
			task.Branch = ""
			f.Run().Setup.Tasks[taskKey] = task
			return nil
		}); err != nil {
			t.Fatalf("seed task path: %v", err)
		}
		return mgr, wt, child.ID
	}

	t.Run("reuse at matching exact base", func(t *testing.T) {
		mgr, wt, childID := newManager(t)
		existing, err := mgr.Store.Load(childID)
		if err != nil {
			t.Fatal(err)
		}
		wtPath := existing.Run().Setup.Tasks["worktree:repo-a"].Path
		wt.heads[wtPath] = sha
		if err := mgr.RunSetup(childID); err != nil {
			t.Fatalf("run setup: %v", err)
		}
		if wt.created {
			t.Fatal("worktree was recreated instead of reused")
		}
		done, err := mgr.Store.Load(childID)
		if err != nil {
			t.Fatal(err)
		}
		if done.Status != feature.StatusCreated || done.Run().Setup.Status != feature.SetupStatusDone {
			t.Fatalf("status=%v setup=%v", done.Status, done.Run().Setup.Status)
		}
	})

	t.Run("mismatch fails safely", func(t *testing.T) {
		mgr, wt, childID := newManager(t)
		existing, err := mgr.Store.Load(childID)
		if err != nil {
			t.Fatal(err)
		}
		wtPath := existing.Run().Setup.Tasks["worktree:repo-a"].Path
		wt.heads[wtPath] = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		err = mgr.RunSetup(childID)
		if err == nil || !strings.Contains(err.Error(), "want exact base") {
			t.Fatalf("err = %v, want exact-base mismatch", err)
		}
		failed, ferr := mgr.Store.Load(childID)
		if ferr != nil {
			t.Fatal(ferr)
		}
		if failed.Status != feature.StatusFailed || failed.Run().Setup.Status != feature.SetupStatusFailed {
			t.Fatalf("status=%v setup=%v", failed.Status, failed.Run().Setup.Status)
		}
	})
}

func TestReconcilePendingChildCreations(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp store; reconciliation scans only that store.
	store := feature.NewStore(t.TempDir())

	parent := &feature.Feature{
		ID:           "p-intent",
		Slug:         "p-intent",
		Status:       feature.StatusPublished,
		ActiveRun:    1,
		RunCount:     1,
		Checkpoints:  feature.Checkpoints{RoadmapReview: true},
		Models:       config.ModelConfig{Review: "parent/review-model"},
		Effort:       config.EffortConfig{Implementation: "high"},
		RiskLevel:    feature.RiskMedium,
		ExitCriteria: "submitted exit criteria",
		Inquireness:  feature.InquirenessMedium,
	}
	intent := &feature.ChildCreationIntent{
		ChildID: "c-intent",
		Kind:    feature.ChildKindRefactor,
		Child: feature.Feature{
			ID:     "c-intent",
			Name:   "R",
			Slug:   "r",
			Status: feature.StatusSettingUpWorktrees,
			Repos: []feature.FeatureRepo{
				{Name: "repo-a", Path: "/src/repo-a", Branch: "feature/r-c", BaseBranch: "main"},
			},
			Parent: &feature.ChildRelationship{
				ParentID: "p-intent",
				Kind:     feature.ChildKindRefactor,
				Bases:    []feature.ChildRepoBase{{Repo: "repo-a", SHA: "cccccccccccccccccccccccccccccccccccccccc"}},
			},
			// The review configuration selected at launch, already resolved
			// identically on both records by CreateRefactorChild.
			Checkpoints:  parent.Checkpoints,
			Models:       parent.Models,
			Effort:       parent.Effort,
			RiskLevel:    parent.RiskLevel,
			ExitCriteria: parent.ExitCriteria,
			Inquireness:  parent.Inquireness,
		},
		Setup: &feature.SetupState{Status: feature.SetupStatusRunning, Attempt: 1},
	}
	parent.PendingChild = intent
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}

	// Child missing on disk → roll forward exactly once from the intent.
	got, err := store.ReconcilePendingChildCreations()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(got) != 1 || got[0] != "p-intent" {
		t.Fatalf("reconciled = %v", got)
	}
	child, err := store.Load("c-intent")
	if err != nil {
		t.Fatalf("child not rebuilt: %v", err)
	}
	if child.BaseSHA("repo-a") != "cccccccccccccccccccccccccccccccccccccccc" {
		t.Fatalf("rebuilt provenance lost: %+v", child.Parent)
	}
	if child.Run().Setup == nil || child.Run().Setup.Status != feature.SetupStatusRunning {
		t.Fatalf("rebuilt setup = %+v", child.Run().Setup)
	}
	reloaded, err := store.Load("p-intent")
	if err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	if reloaded.PendingChild != nil {
		t.Fatalf("intent not cleared")
	}
	// The shared Review configuration survived recovery identically on both
	// records.
	assertSharedReviewConfigMatches(t, reloaded, child)

	// Idempotent: second run is a no-op.
	got, err = store.ReconcilePendingChildCreations()
	if err != nil || len(got) != 0 {
		t.Fatalf("second reconcile = %v, %v", got, err)
	}

	// Intent present but child already exists → clear only.
	parent.PendingChild = intent
	if err := store.Save(parent); err != nil {
		t.Fatalf("re-save parent: %v", err)
	}
	if _, err := store.ReconcilePendingChildCreations(); err != nil {
		t.Fatalf("reconcile with existing child: %v", err)
	}
	reloaded, _ = store.Load("p-intent")
	if reloaded.PendingChild != nil {
		t.Fatalf("intent not cleared when child exists")
	}
}

func TestReconcilePendingChildCreationsToleratesBrokenParentRunState(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp store; reconciliation scans only that store.
	// The durable intent lives on the parent's feature.yaml, so an unreadable
	// active run.yaml must not silently swallow startup recovery: the intent
	// is still rolled forward and cleared.
	store := feature.NewStore(t.TempDir())
	parent := &feature.Feature{ID: "p-norun", Slug: "p-norun", Status: feature.StatusPublished, ActiveRun: 1, RunCount: 1}
	parent.PendingChild = &feature.ChildCreationIntent{
		ChildID: "c-norun",
		Kind:    feature.ChildKindRefactor,
		Child: feature.Feature{
			ID:     "c-norun",
			Name:   "R",
			Slug:   "r",
			Status: feature.StatusSettingUpWorktrees,
			Repos: []feature.FeatureRepo{
				{Name: "repo-a", Path: "/src/repo-a", Branch: "feature/r-c", BaseBranch: "main"},
			},
			Parent: &feature.ChildRelationship{ParentID: "p-norun", Kind: feature.ChildKindRefactor},
			Models: config.ModelConfig{Review: "parent/review-model"},
		},
		Setup: &feature.SetupState{Status: feature.SetupStatusRunning, Attempt: 1},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	// Break the parent's run state after the intent is durable.
	if err := os.RemoveAll(filepath.Join(store.BaseDir, "p-norun", "runs")); err != nil {
		t.Fatalf("remove parent runs: %v", err)
	}

	got, err := store.ReconcilePendingChildCreations()
	if err != nil {
		t.Fatalf("reconcile must not be gated by the parent's run state: %v", err)
	}
	if len(got) != 1 || got[0] != "p-norun" {
		t.Fatalf("reconciled = %v", got)
	}
	// The child was rebuilt from the intent, preserving its configuration.
	child, err := store.Load("c-norun")
	if err != nil {
		t.Fatalf("child not rebuilt: %v", err)
	}
	if child.Models.Review != "parent/review-model" {
		t.Fatalf("rebuilt child models = %+v", child.Models)
	}
	// The intent is cleared even though Store.Load(parent) still fails.
	data, err := os.ReadFile(filepath.Join(store.BaseDir, "p-norun", "feature.yaml"))
	if err != nil {
		t.Fatalf("read parent feature.yaml: %v", err)
	}
	if strings.Contains(string(data), "pending_child") {
		t.Fatalf("pending child intent must be cleared without the parent's run state; feature.yaml:\n%s", data)
	}
}

func TestReconcilePendingChildCreationsReportsUnreadableRecords(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp store; reconciliation scans only that store.
	// A feature record that cannot be read or parsed could be a parent with a
	// pending child intent; skipping it would let a submitted child remain
	// neither materialized nor diagnosed. It must surface as a contextual
	// load error instead.
	store := feature.NewStore(t.TempDir())
	corruptDir := filepath.Join(store.BaseDir, "p-corrupt")
	if err := os.MkdirAll(corruptDir, 0o755); err != nil {
		t.Fatalf("mkdir corrupt record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "feature.yaml"), []byte("[unterminated"), 0o644); err != nil {
		t.Fatalf("write corrupt record: %v", err)
	}

	_, err := store.ReconcilePendingChildCreations()
	if err == nil || !strings.Contains(err.Error(), "p-corrupt") {
		t.Fatalf("err = %v, want contextual error naming p-corrupt", err)
	}
}

// TestFailActiveSetupParksRunningSetupRetryable pins the durable recovery
// contract behind Orchestrator.RunSetupAsync: when a setup run fails before
// the runner could persist its own failure, marking the still-running setup
// failed must park the child in Failed/WorktreeSetup with the error recorded
// so ordinary RetrySetup takes over — and must be an idempotent no-op once
// setup reaches a terminal state.
func TestFailActiveSetupParksRunningSetupRetryable(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp store and fakes isolate state.
	const sha = "ffffffffffffffffffffffffffffffffffffffff"
	mgr := newChildTestManager(t, nil, cleanEverywhere())
	wt := &childFakeWorktrees{heads: map[string]string{"/wt/repo": sha}}
	mgr.Worktrees = wt
	saveChildTestParent(t, mgr, &feature.Feature{
		ID:     "p-failactive",
		Slug:   "p-failactive",
		Status: feature.StatusPublished,
		Repos: []feature.FeatureRepo{
			{Name: "repo", Path: "/src/repo", WorktreePath: "/wt/repo", BaseBranch: "main"},
		},
	})
	child, err := mgr.CreateRefactorChild("p-failactive", childTestSpec())
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	const cause = "reloading child after launch failed"
	marked, err := mgr.FailActiveSetup(child.ID, cause)
	if err != nil {
		t.Fatalf("FailActiveSetup() error = %v", err)
	}
	if !marked {
		t.Fatal("FailActiveSetup marked = false for a running setup, want true")
	}

	parked, err := mgr.Store.Load(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	setup := parked.Run().Setup
	if parked.Status != feature.StatusFailed || parked.FailureType != feature.FailureWorktreeSetup {
		t.Fatalf("status=%v failureType=%q, want Failed/%s", parked.Status, parked.FailureType, feature.FailureWorktreeSetup)
	}
	if setup == nil || setup.Status != feature.SetupStatusFailed || setup.LastError != cause {
		t.Fatalf("setup = %+v, want failed state with the recorded cause", setup)
	}

	// Durably retryable through the ordinary gate: the retried run skips
	// nothing it cannot validate and completes against the exact base.
	mgr.Branches.(*mocks.MockBranchOperator).CurrentBranchFn = func(string) (string, error) {
		return parked.Repos[0].Branch, nil
	}
	wtPath := t.TempDir()
	if err := mgr.Store.Modify(child.ID, func(f *feature.Feature) error {
		task := f.Run().Setup.Tasks["worktree:repo"]
		task.Path = wtPath
		f.Run().Setup.Tasks["worktree:repo"] = task
		return nil
	}); err != nil {
		t.Fatalf("seed task path: %v", err)
	}
	wt.heads[wtPath] = sha
	if err := mgr.RetrySetup(child.ID); err != nil {
		t.Fatalf("RetrySetup() on parked child error = %v, want success", err)
	}
	retried, err := mgr.Store.Load(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != feature.StatusCreated || retried.Run().Setup.Status != feature.SetupStatusDone {
		t.Fatalf("after retry status=%v setup=%v, want Created/done", retried.Status, retried.Run().Setup.Status)
	}

	// Terminal setup is untouched: no clobber, no state change.
	marked, err = mgr.FailActiveSetup(child.ID, "late failure")
	if err != nil {
		t.Fatalf("FailActiveSetup() after completion error = %v", err)
	}
	if marked {
		t.Fatal("FailActiveSetup marked = true after setup completed, want false")
	}
	final, err := mgr.Store.Load(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != feature.StatusCreated || final.Run().Setup.Status != feature.SetupStatusDone {
		t.Fatalf("terminal state changed by FailActiveSetup: status=%v setup=%v", final.Status, final.Run().Setup.Status)
	}
}

// TestChildExecutionCapability pins the supported child execution shape matrix.
func TestChildExecutionCapability(t *testing.T) {
	oneRepo := []feature.FeatureRepo{{Name: "repoA", Path: "/tmp/a"}}
	twoRepos := []feature.FeatureRepo{{Name: "repoA", Path: "/tmp/a"}, {Name: "repoB", Path: "/tmp/b"}}

	t.Run("non-child is always capable", func(t *testing.T) {
		f := &feature.Feature{ID: "top", Pipeline: feature.PipelineMoonshot, Repos: twoRepos}
		if err := f.ChildExecutionCapability(); err != nil {
			t.Fatalf("ChildExecutionCapability() = %v for a top-level feature", err)
		}
	})

	t.Run("medium single-repo child is capable", func(t *testing.T) {
		f := &feature.Feature{ID: "c", Pipeline: feature.PipelineMedium, Repos: oneRepo,
			Parent: &feature.ChildRelationship{ParentID: "p", Kind: feature.ChildKindRefactor}}
		if err := f.ChildExecutionCapability(); err != nil {
			t.Fatalf("ChildExecutionCapability() = %v, want nil", err)
		}
	})

	t.Run("large and moonshot children get the typed KB capability error", func(t *testing.T) {
		for _, profile := range []feature.PipelineProfile{feature.PipelineLarge, feature.PipelineMoonshot} {
			f := &feature.Feature{ID: "c", Pipeline: profile, Repos: oneRepo,
				Parent: &feature.ChildRelationship{ParentID: "p", Kind: feature.ChildKindRefactor}}
			err := f.ChildExecutionCapability()
			var capErr *feature.ChildCapabilityError
			if !errors.As(err, &capErr) || capErr.Reason != feature.ChildCapabilityProfileUnsupported || capErr.Profile != profile {
				t.Fatalf("profile %s: error = %v, want typed unsupported_profile", profile, err)
			}
		}
	})

	t.Run("multi-repo medium child no longer restricted by repo count", func(t *testing.T) {
		f := &feature.Feature{ID: "c", Pipeline: feature.PipelineMedium, Repos: twoRepos,
			Parent: &feature.ChildRelationship{ParentID: "p", Kind: feature.ChildKindRefactor}}
		err := f.ChildExecutionCapability()
		if err != nil {
			t.Fatalf("multi-repo medium child error = %v, want nil (repo-count restriction retired)", err)
		}
	})
}

// TestChildSetupCompleteMatrix pins the setup-state half of the gate.
func TestChildSetupCompleteMatrix(t *testing.T) {
	mk := func(status feature.Status, failureType string) *feature.Feature {
		return &feature.Feature{ID: "c", Status: status, FailureType: failureType,
			Parent: &feature.ChildRelationship{ParentID: "p", Kind: feature.ChildKindRefactor}}
	}
	for _, tc := range []struct {
		name string
		f    *feature.Feature
		want bool
	}{
		{"queued/setting-up", mk(feature.StatusSettingUpWorktrees, ""), false},
		{"failed setup", mk(feature.StatusFailed, feature.FailureWorktreeSetup), false},
		{"setup-complete Created", mk(feature.StatusCreated, ""), true},
		{"executing child", mk(feature.StatusImplementing, ""), true},
		{"pipeline-failed child", mk(feature.StatusFailed, feature.FailureSessionCrash), true},
		{"non-child", &feature.Feature{ID: "t", Status: feature.StatusCreated}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.ChildSetupComplete(); got != tc.want {
				t.Fatalf("ChildSetupComplete() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestChildIntegrationRecordPersists proves the durable integration record
// and closure fields survive a store round-trip.
func TestChildIntegrationRecordPersists(t *testing.T) {
	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	closed := time.Now().UTC().Truncate(time.Second)
	child := &feature.Feature{
		ID: "c1", Slug: "c1", Status: feature.StatusReviewPassed, Created: time.Now(),
		ActiveRun: 1, RunCount: 1, Pipeline: feature.PipelineMedium,
		Repos: []feature.FeatureRepo{{Name: "repoA", Path: "/tmp/a"}},
		Parent: &feature.ChildRelationship{
			ParentID:     "p1",
			Kind:         feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeCompleted,
			ClosedAt:     &closed,
			Transaction: &feature.TransactionJournal{
				Phase: feature.TransactionPhaseMerged,
				Entries: []feature.RepoTransactionEntry{{
					ParentBranch:    "feature/parent",
					ParentAnchorSHA: "aaaa1111",
					ChildHeadSHA:    "bbbb2222",
					MergeHEAD:       "cccc3333",
					CleanupWarning:  "worktree busy",
					Dirty: []feature.RepoDirtyDiagnostics{{
						Repo: "repoA", Path: "/tmp/a", Untracked: []string{"stray.txt"}, UntrackedTotal: 1,
					}},
				}},
			},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Load(child.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted || got.Parent.ClosedAt == nil || !got.Parent.ClosedAt.Equal(closed) {
		t.Fatalf("closure = %q %v, want completed at %v", got.Parent.CloseOutcome, got.Parent.ClosedAt, closed)
	}
	tx := got.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %+v, want merged", tx)
	}
	if len(tx.Entries) != 1 {
		t.Fatalf("transaction entries = %d, want 1", len(tx.Entries))
	}
	entry := tx.Entries[0]
	if entry.ParentBranch != "feature/parent" || entry.ParentAnchorSHA != "aaaa1111" ||
		entry.ChildHeadSHA != "bbbb2222" || entry.MergeHEAD != "cccc3333" ||
		entry.CleanupWarning != "worktree busy" {
		t.Fatalf("transaction entry = %+v, want full round-trip", entry)
	}
	if len(entry.Dirty) != 1 || entry.Dirty[0].UntrackedTotal != 1 || entry.Dirty[0].Untracked[0] != "stray.txt" {
		t.Fatalf("dirty diagnostics = %+v, want round-trip", entry.Dirty)
	}
}

// TestIntegrationResumable pins which durable integration states a Restart
// replays: any recorded phase while the relationship is open, and no state
// after closure because automatic reconciliation owns cleanup tails.
func TestIntegrationResumable(t *testing.T) {
	mk := func(closeOutcome string, tx *feature.TransactionJournal, worktree string) *feature.Feature {
		return &feature.Feature{
			ID:     "c",
			Status: feature.StatusReviewPassed,
			Repos:  []feature.FeatureRepo{{Name: "repoA", Path: "/tmp/a", WorktreePath: worktree}},
			Parent: &feature.ChildRelationship{
				ParentID:     "p",
				Kind:         feature.ChildKindRefactor,
				CloseOutcome: closeOutcome,
				Transaction:  tx,
			},
		}
	}
	merged := &feature.TransactionJournal{Phase: feature.TransactionPhaseMerged, Entries: []feature.RepoTransactionEntry{{MergeHEAD: "cccc3333"}}}
	for _, tc := range []struct {
		name string
		f    *feature.Feature
		want bool
	}{
		{"non-child", &feature.Feature{ID: "t"}, false},
		{"active without integration", mk("", nil, "/tmp/wt"), false},
		{"active phase pending", mk("", &feature.TransactionJournal{Phase: feature.TransactionPhasePreparing}, "/tmp/wt"), true},
		{"active phase attention", mk("", &feature.TransactionJournal{Phase: feature.TransactionPhaseAttention}, "/tmp/wt"), true},
		{"active phase merged", mk("", merged, "/tmp/wt"), true},
		{"closed completed settled", mk(feature.ChildCloseOutcomeCompleted, merged, ""), false},
		{"closed completed with cleanup warning", mk(feature.ChildCloseOutcomeCompleted, &feature.TransactionJournal{Phase: feature.TransactionPhaseMerged, Entries: []feature.RepoTransactionEntry{{MergeHEAD: "cccc3333", CleanupWarning: "worktree busy"}}}, ""), false},
		{"closed completed with pending worktree", mk(feature.ChildCloseOutcomeCompleted, merged, "/tmp/wt"), false},
		{"closed completed without merge head", mk(feature.ChildCloseOutcomeCompleted, &feature.TransactionJournal{Phase: feature.TransactionPhasePreparing}, ""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.IntegrationResumable(); got != tc.want {
				t.Fatalf("IntegrationResumable() = %v, want %v", got, tc.want)
			}
		})
	}
}
