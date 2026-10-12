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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ---------------------------------------------------------------------------
// Category G — Publish pipeline
// ---------------------------------------------------------------------------

func storedRepoState(t *testing.T, store *feature.Store, featureID, repo string) *feature.RepoState {
	t.Helper()
	state := loadStoredFeature(t, store, featureID).RepoStates[repo]
	if state == nil {
		t.Fatalf("repo state for %s missing", repo)
	}
	return state
}

// Publish happy path: multiple publishable repos, no conflicts, no already-
// published skips. Emits PublishStarted + PublishCompleted, fans out per-repo,
// and delegates FeatureCompleted emission to tryCompleteAndEmit (which fires
// because TryCompletePublish returns true on the first call).
func TestOrchestrator_Publish_HappyPath_MultiRepo(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-happy",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
			{Name: "r2", Path: "/tmp/r2"},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	var publishedCount int
	var publishedFeatureID string
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureCompleted: func(id string, fv *feature.Feature) {
			publishedCount++
			publishedFeatureID = id
		},
	})

	publishRepoCalls := make(map[string]int)
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		publishRepoCalls[repo]++
		url := "https://github.com/org/" + repo + "/pull/1"
		return url, lc.SetRepoPublished(id, repo, url)
	})

	if err := o.Publish("feat-pub-happy"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if publishRepoCalls["r1"] != 1 {
		t.Errorf("publishRepo calls for r1 = %d, want 1", publishRepoCalls["r1"])
	}
	if publishRepoCalls["r2"] != 1 {
		t.Errorf("publishRepo calls for r2 = %d, want 1", publishRepoCalls["r2"])
	}

	if stored := loadStoredFeature(t, fs, f.ID); stored.Status != feature.StatusPublished {
		t.Fatalf("status = %s, want Published", stored.Status)
	}
	if publishedCount != 1 {
		t.Errorf("OnFeatureCompleted fired %d times, want 1", publishedCount)
	}
	if publishedFeatureID != "feat-pub-happy" {
		t.Errorf("OnFeatureCompleted featureID = %q, want feat-pub-happy", publishedFeatureID)
	}

	events := drainEvents(o)
	if !hasEventType(events, ports.PublishStarted) {
		t.Error("expected PublishStarted event")
	}
	if !hasEventType(events, ports.PublishCompleted) {
		t.Error("expected PublishCompleted event")
	}
	if !hasEventType(events, ports.FeatureCompleted) {
		t.Error("expected FeatureCompleted event")
	}
}

// Publish is a no-op for non-publishable features (explicitly-false flag).
func TestOrchestrator_Publish_NotPublishable_NoOp(t *testing.T) {
	unpub := false
	f := &feature.Feature{
		ID:     "feat-pub-np",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", Publishable: &unpub},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	calls := 0
	o.SetPublishRepoFn(func(id, repo string) (string, error) { calls++; return "", nil })

	if err := o.Publish("feat-pub-np"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if calls != 0 {
		t.Errorf("publishRepo should not fire for non-publishable feature; got %d", calls)
	}
	events := drainEvents(o)
	if hasEventType(events, ports.PublishStarted) {
		t.Error("PublishStarted should NOT fire for non-publishable")
	}
}

// Publish skips repos that have already been published (RepoImpl[repo].PRURL set).
func TestOrchestrator_Publish_SkipsAlreadyPublished(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-skip",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
			{Name: "r2", Path: "/tmp/r2"},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {PRURL: "https://github.com/org/r1/pull/42"},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	calls := make(map[string]int)
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		calls[repo]++
		return "https://github.com/org/" + repo + "/pull/99", nil
	})

	if err := o.Publish("feat-pub-skip"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if calls["r1"] != 0 {
		t.Errorf("r1 already published; publishRepo should not be called for r1; got %d", calls["r1"])
	}
	if calls["r2"] != 1 {
		t.Errorf("r2 publishRepo should be called once; got %d", calls["r2"])
	}
}

func TestOrchestrator_Publish_SelectedAlreadyPublishedRepoRepublishes(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-republish-selected",
		Name:         "republish selected",
		Slug:         "republish-selected",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: wtR1Path, Branch: "feature/republish-selected", BaseBranch: mainBranch},
			{Name: "r2", Path: "/tmp/r2", WorktreePath: "/tmp/wt-r2", Branch: "feature/republish-selected", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true, PRURL: "https://github.com/org/r1/pull/42"},
			"r2": {Touched: true},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	calls := make(map[string]int)
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		calls[repo]++
		return "https://github.com/org/" + repo + "/pull/99", nil
	})

	if err := o.PublishWithOptions("feat-republish-selected", orchestrator.PublishOptions{Repos: []string{"r1"}}); err != nil {
		t.Fatalf("PublishWithOptions: %v", err)
	}

	if calls["r1"] != 1 {
		t.Fatalf("selected already-published repo calls = %d, want 1", calls["r1"])
	}
	if calls["r2"] != 0 {
		t.Fatalf("unselected repo calls = %d, want 0", calls["r2"])
	}
}

func TestOrchestrator_Publish_ManualCodeReadyRepublishesExistingPR(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-republish-existing",
		Name:         "republish existing",
		Slug:         "republish-existing",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: wtR1Path, Branch: "feature/republish-existing", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true, PRURL: "https://github.com/org/r1/pull/42"},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	var calls int
	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		calls++
		return "https://github.com/org/r1/pull/42", nil
	})

	if err := o.Publish("feat-republish-existing"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if calls != 1 {
		t.Fatalf("already-published manual CodeReady repo calls = %d, want 1", calls)
	}
}

// Publish surfaces *PublishConflictError on conflict; final error satisfies
// errors.Is(err, ErrPublishConflict) and errors.As extracts repo info.
func TestOrchestrator_Publish_ConflictError_SurfacedAsSentinel(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-conflict",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	wantConflict := &orchestrator.PublishConflictError{RepoName: "r1", Branch: "feature/x"}
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		return "", wantConflict
	})

	err := o.Publish("feat-pub-conflict")
	if err == nil {
		t.Fatal("expected error from conflict, got nil")
	}
	if !errors.Is(err, orchestrator.ErrPublishConflict) {
		t.Errorf("errors.Is(err, ErrPublishConflict) = false; err = %v", err)
	}
	var ce *orchestrator.PublishConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("errors.As failed: %v", err)
	}
	if ce.RepoName != "r1" || ce.Branch != "feature/x" {
		t.Errorf("conflict details lost: %+v", ce)
	}

	events := drainEvents(o)
	if !hasEventType(events, ports.PublishCompleted) {
		t.Error("expected PublishCompleted event even on conflict")
	}
}

// When both a conflict and a plain error occur in the same publish pass, the
// conflict sentinel wins (conflict gets preferential surfacing).
func TestOrchestrator_Publish_Conflict_Takes_Priority(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-mix",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
			{Name: "r2", Path: "/tmp/r2"},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})

	plainErr := errors.New("push boom")
	conflict := &orchestrator.PublishConflictError{RepoName: "r2", Branch: "feature/x"}
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		switch repo {
		case "r1":
			return "", plainErr
		case "r2":
			return "", conflict
		}
		return "", nil
	})

	err := o.Publish("feat-pub-mix")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, orchestrator.ErrPublishConflict) {
		t.Errorf("conflict should take priority; err = %v", err)
	}
}

// Publish with no repos returns an explicit error (nothing to publish is a
// caller bug, not silent success).
func TestOrchestrator_Publish_NoRepos_Errors(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-empty",
		Status: feature.StatusReviewPassed,
	}
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs}, orchestrator.Hooks{})
	err := o.Publish("feat-pub-empty")
	if err == nil {
		t.Fatal("expected error for empty-repos feature, got nil")
	}
}

// ---------------------------------------------------------------------------
// publishRepo (internal) — exercised via o.Publish with real port mocks.
// ---------------------------------------------------------------------------

// publishRepo commits uncommitted changes, skips pull-rebase when Rebaser nil,
// pushes, creates PR, records success on Lifecycle. End-to-end via o.Publish.
func TestOrchestrator_PublishRepo_EndToEnd_NoRebaser(t *testing.T) {
	repoPath := newPublishReadyBranch(t, "feature/cool-feature")
	if err := os.WriteFile(filepath.Join(repoPath, "change.txt"), []byte("change\n"), 0o644); err != nil {
		t.Fatalf("write change: %v", err)
	}
	f := &feature.Feature{
		ID:     "feat-pubrepo",
		Name:   "cool-feature",
		Slug:   "cool-feature",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/cool-feature", BaseBranch: mainBranch},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
		Remote:  pub,
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-pubrepo", orchestrator.PublishOptions{
		Title: "Publish repo",
		Body:  "Verified body",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if git.HasUncommittedChanges(repoPath) {
		t.Fatal("publish left local changes uncommitted")
	}
	for _, want := range []string{"Push", "CreatePR"} {
		if countPublisherCalls(pub, want) != 1 {
			t.Errorf("expected RemoteOps.%s once", want)
		}
	}

	if state := storedRepoState(t, fs, f.ID, "r1"); state.PRURL == "" {
		t.Fatal("repo PR URL was not persisted")
	}
}

// publishRepo surfaces a pull-rebase conflict as *PublishConflictError and
// records SetRepoPublishError on the lifecycle.
func TestOrchestrator_PublishRepo_PullRebaseConflict_Sentinel(t *testing.T) {
	repoPath, bare := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repoPath, "feature/x")
	testutil.CommitFile(t, repoPath, "conflict.txt", "local\n", "local change")
	runPublishGit(t, repoPath, "checkout", mainBranch)
	testutil.CreateBranch(t, repoPath, "remote-feature")
	testutil.CommitFile(t, repoPath, "conflict.txt", "remote\n", "remote change")
	testutil.SimulatePush(t, repoPath, bare, "remote-feature", "feature/x")
	runPublishGit(t, repoPath, "checkout", "feature/x")
	f := &feature.Feature{
		ID:     "feat-pubrepo-conflict",
		Name:   "x",
		Slug:   "x",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/x", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	pub := mocks.NewMockRemoteOps()

	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
		Remote:  pub,
	}, orchestrator.Hooks{})

	err := o.PublishWithOptions("feat-pubrepo-conflict", orchestrator.PublishOptions{
		Title: "Publish repo",
		Body:  "Verified body",
	})
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !errors.Is(err, orchestrator.ErrPublishConflict) {
		t.Errorf("errors.Is(err, ErrPublishConflict) = false; err = %v", err)
	}

	if state := storedRepoState(t, fs, f.ID, "r1"); state.Error == nil {
		t.Fatal("repo publish failure was not persisted")
	}
	// Push and CreatePR should NOT have been called because we bailed on rebase.
	for _, c := range pub.Calls {
		if c.Method == "Push" || c.Method == "CreatePR" {
			t.Errorf("%s should not have been called after rebase conflict", c.Method)
		}
	}
}

func TestOrchestrator_PublishRepo_ManualCodeReadyUsesRewrittenBranchPush(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-manual-publish-rebased",
		Name:         "manual publish rebased",
		Slug:         "manual-publish-rebased",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{
				Name:         "r1",
				Path:         "/tmp/r1",
				WorktreePath: wtR1Path,
				Branch:       "feature/manual-publish-rebased",
				BaseBranch:   mainBranch,
			},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error {
		t.Fatalf("manual publish after a rebase must not plain-push %s from %s", branch, path)
		return nil
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		return "https://github.com/org/r1/pull/1", nil
	}

	pub.PushRewrittenBranchFn = func(worktreePath, branch string) error { return nil }

	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
		Remote:  pub,
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-manual-publish-rebased", orchestrator.PublishOptions{
		Title: "Publish repo",
		Body:  "Verified body",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if got := countPublisherCalls(pub, "PushRewrittenBranch"); got != 1 {
		t.Fatalf("RemoteOps.PushRewrittenBranch calls = %d, want 1", got)
	}
	if got := countPublisherCalls(pub, "CreatePR"); got != 1 {
		t.Fatalf("Publisher.CreatePR calls = %d, want 1", got)
	}
	if state := storedRepoState(t, fs, f.ID, "r1"); state.PRURL == "" {
		t.Fatal("repo PR URL was not persisted")
	}
}

func TestOrchestrator_Publish_RewrittenBranchRemoteDiverged(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-publish-remote-diverged",
		Name:         "publish remote diverged",
		Slug:         "publish-remote-diverged",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: wtR1Path, Branch: "feature/remote-diverged", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	fs, lc := newStoredFeatureFixture(t, f)
	pub := mocks.NewMockRemoteOps()
	pub.PushRewrittenBranchFn = func(path, branch string) error {
		return &git.RewritePushError{
			Kind:              git.RewritePushRemoteDiverged,
			Branch:            branch,
			RemoteOnlyCommits: 2,
		}
	}
	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
		Remote:  pub,
	}, orchestrator.Hooks{})

	err := o.PublishWithOptions(f.ID, orchestrator.PublishOptions{
		Repos: []string{"r1"},
		Title: "Publish remote diverged",
		Body:  "Verified body",
	})
	var diverged *orchestrator.PublishRemoteDivergedError
	if !errors.As(err, &diverged) {
		t.Fatalf("error = %T %v; want PublishRemoteDivergedError", err, err)
	}
	if diverged.RepoName != "r1" || diverged.Branch != "feature/remote-diverged" || diverged.RemoteOnlyCommits != 2 {
		t.Fatalf("PublishRemoteDivergedError = %+v; want repo r1, branch feature/remote-diverged, 2 remote commits", diverged)
	}
}

func TestOrchestrator_Publish_RewrittenBranchRemoteChanged(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-publish-remote-changed",
		Name:         "publish remote changed",
		Slug:         "publish-remote-changed",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: wtR1Path, Branch: "feature/remote-changed", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	fs, lc := newStoredFeatureFixture(t, f)
	pub := mocks.NewMockRemoteOps()
	pub.PushRewrittenBranchFn = func(path, branch string) error {
		return &git.RewritePushError{Kind: git.RewritePushRemoteChanged, Branch: branch}
	}
	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
		Remote:  pub,
	}, orchestrator.Hooks{})

	err := o.PublishWithOptions(f.ID, orchestrator.PublishOptions{
		Repos: []string{"r1"},
		Title: "Publish remote changed",
		Body:  "Verified body",
	})
	var changed *orchestrator.PublishRemoteChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("error = %T %v; want PublishRemoteChangedError", err, err)
	}
	if changed.RepoName != "r1" || changed.Branch != "feature/remote-changed" {
		t.Fatalf("PublishRemoteChangedError = %+v; want repo r1, branch feature/remote-changed", changed)
	}
}

func TestOrchestrator_PublishRepo_UsesPhaseRunnerDescriptionGeneration(t *testing.T) {
	repoPath := newPublishReadyBranch(t, "feature/cool-feature")
	testutil.CommitFile(t, repoPath, "change.txt", "change\n", "publish change")
	f := &feature.Feature{
		ID:     "feat-pub-desc",
		Name:   "cool-feature",
		Slug:   "cool-feature",
		Status: feature.StatusReviewPassed,
		Models: config.ModelConfig{Planning: "sonnet"},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/cool-feature", BaseBranch: mainBranch},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }

	var gotTitle, gotBody string
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotTitle, gotBody = title, body
		return "https://github.com/org/r1/pull/1", nil
	}

	pr := newPublishDescriptionPhaseRunner(t, "TITLE: Session Title\nBODY:\n## Summary\n\nGenerated body", false)
	o := orchestrator.New(orchestrator.Deps{
		Manager:     lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	if err := o.Publish("feat-pub-desc"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if gotTitle != "Session Title" {
		t.Errorf("CreatePR title = %q, want %q", gotTitle, "Session Title")
	}
	if !strings.Contains(gotBody, "Generated body") {
		t.Errorf("CreatePR body = %q, want generated session output", gotBody)
	}
}

func TestOrchestrator_PublishRepo_DescriptionGenerationFailureStoresRecord(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-fallback",
		Name:   "cool-feature",
		Slug:   "cool-feature",
		Status: feature.StatusReviewPassed,
		Models: config.ModelConfig{Planning: "sonnet"},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: wtR1Path, BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }

	createPRCalls := 0
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		createPRCalls++
		return "https://github.com/org/r1/pull/1", nil
	}

	pr := newPublishDescriptionPhaseRunner(t, "", true)
	o := orchestrator.New(orchestrator.Deps{
		Manager:     lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	err := o.Publish("feat-pub-fallback")
	if err == nil {
		t.Fatal("Publish() error = nil, want description generation failure")
	}
	if !strings.Contains(err.Error(), "generating description") {
		t.Errorf("Publish() error = %v, want description generation context", err)
	}
	if createPRCalls != 0 {
		t.Fatalf("CreatePR calls = %d, want 0", createPRCalls)
	}

	// The repository owns the condition through its stored record; no
	// publish-scoped error log is written.
	state := storedRepoState(t, fs, f.ID, "r1")
	if state.Error == nil {
		t.Fatal("repo publish failure was not persisted")
	}
	storedRecord := *state.Error
	if storedRecord.Code != errcat.PublishDescriptionFailed {
		t.Errorf("stored record code = %q, want publish_description_failed", storedRecord.Code)
	}
	if storedRecord.Context == nil || len(storedRecord.Context.Repositories) != 1 ||
		storedRecord.Context.Repositories[0].Name != "r1" {
		t.Fatalf("stored record repositories = %+v, want r1", storedRecord.Context)
	}
	if !strings.Contains(storedRecord.Diagnostics, "generating description") {
		t.Errorf("stored record diagnostics = %q, want the raw generation failure", storedRecord.Diagnostics)
	}
	runDir := agent.ActiveRunDir(pr.StateDir, f)
	if _, statErr := os.Stat(filepath.Join(runDir, "publish", "error.log")); !os.IsNotExist(statErr) {
		t.Errorf("publish error.log exists under %s, want none (the record owns the condition)", runDir)
	}
}

func TestOrchestrator_GeneratePublishDescriptionDerivesSelectedRepoContext(t *testing.T) {
	repo1 := newPublishReadyBranch(t, "feature/selected-r1")
	testutil.CommitFile(t, repo1, "selected.txt", "selected\n", "selected repo change")
	f := &feature.Feature{
		ID:          "feat-pub-desc-selected",
		Name:        "selected-feature",
		Slug:        "selected-feature",
		Description: "Ship selected repository changes.",
		Status:      feature.StatusCodeReady,
		Models:      config.ModelConfig{Planning: "sonnet"},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repo1, WorktreePath: repo1, BaseBranch: mainBranch},
			{Name: "r2", Path: "/tmp/r2", WorktreePath: "/tmp/wt-r2", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
			"r2": {Touched: true},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)
	pr := newPublishDescriptionPhaseRunner(t, "TITLE: Generated selected title\nBODY:\nGenerated selected body", false)
	o := orchestrator.New(orchestrator.Deps{
		Manager:     lc,
		Store:       fs,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	title, body, err := o.GeneratePublishDescription("feat-pub-desc-selected", orchestrator.PublishDescriptionOptions{Repos: []string{"r1"}})
	if err != nil {
		t.Fatalf("GeneratePublishDescription: %v", err)
	}
	if title != "Generated selected title" || !strings.Contains(body, "Generated selected body") {
		t.Fatalf("generated narrative = %q / %q, want phase-runner result", title, body)
	}
	if commits, err := git.CommitBodies(repo1, mainBranch); err != nil || !strings.Contains(commits, "selected repo change") {
		t.Fatalf("selected commit context = %q, err=%v", commits, err)
	}
}

func TestOrchestrator_GeneratePublishDescriptionReturnsGenerationFailure(t *testing.T) {
	f := &feature.Feature{
		ID:          "feat-pub-desc-failure",
		Name:        "strict-description",
		Slug:        "strict-description",
		Description: "Never publish fallback prose.",
		Status:      feature.StatusCodeReady,
		Models:      config.ModelConfig{Planning: "sonnet"},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: "/tmp/wt-r1", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
	}
	fs, lc := newStoredFeatureFixture(t, f)
	pub := mocks.NewMockRemoteOps()
	pr := newPublishDescriptionPhaseRunner(t, "", true)
	o := orchestrator.New(orchestrator.Deps{
		Manager:     lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	title, body, err := o.GeneratePublishDescription(
		"feat-pub-desc-failure",
		orchestrator.PublishDescriptionOptions{Repos: []string{"r1"}},
	)
	if err == nil {
		t.Fatal("GeneratePublishDescription() error = nil, want generation failure")
	}
	if title != "" || body != "" {
		t.Errorf("GeneratePublishDescription() = %q / %q, want empty output on failure", title, body)
	}
}

// ---------------------------------------------------------------------------
// DraftPublish — draft flag threaded from feature checkpoints to CreatePR
// ---------------------------------------------------------------------------

func TestOrchestrator_PublishRepo_DraftPublish_True(t *testing.T) {
	repoPath := newPublishReadyBranch(t, "feature/draft-feature")
	f := &feature.Feature{
		ID:   "feat-draft-true",
		Name: "draft-feature",
		Slug: "draft-feature",
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/draft-feature", BaseBranch: mainBranch},
		},
		Checkpoints: feature.Checkpoints{DraftPublish: true},
	}
	fs, lc := newStoredFeatureFixture(t, f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }

	var gotDraft bool
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotDraft = draft
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
		Remote:  pub,
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-draft-true", orchestrator.PublishOptions{
		Title: "Publish repo",
		Body:  "Verified body",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !gotDraft {
		t.Error("Publisher.CreatePR should receive draft=true when feature Checkpoints.DraftPublish is true")
	}
}

func TestOrchestrator_PublishRepo_DraftPublish_False(t *testing.T) {
	repoPath := newPublishReadyBranch(t, "feature/no-draft-feature")
	f := &feature.Feature{
		ID:   "feat-draft-false",
		Name: "no-draft-feature",
		Slug: "no-draft-feature",
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/no-draft-feature", BaseBranch: mainBranch},
		},
		// DraftPublish defaults to false
	}
	fs, lc := newStoredFeatureFixture(t, f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }

	var gotDraft bool
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotDraft = draft
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
		Remote:  pub,
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-draft-false", orchestrator.PublishOptions{
		Title: "Publish repo",
		Body:  "Verified body",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if gotDraft {
		t.Error("Publisher.CreatePR should receive draft=false when feature Checkpoints.DraftPublish is false")
	}
}

// publishDescriptionSessionHandle embeds the shared mock session view; the
// publish-description flow only reads the transcript, the terminal result,
// the status channel and pending permission requests off the handle.
type publishDescriptionSessionHandle struct {
	*mocks.MockSessionView
}

func newPublishDescriptionSessionHandle() *publishDescriptionSessionHandle {
	return &publishDescriptionSessionHandle{MockSessionView: mocks.NewMockSessionView("", "")}
}

func newPublishReadyBranch(t *testing.T, branch string) string {
	t.Helper()
	repo, _ := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repo, branch)
	return repo
}

func runPublishGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = testutil.GitTestEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func newPublishDescriptionPhaseRunner(t *testing.T, output string, permissionFailure bool) *agent.PhaseRunner {
	t.Helper()

	newSession := func() *publishDescriptionSessionHandle {
		sess := newPublishDescriptionSessionHandle()
		if output != "" {
			sess.MessageLogVal.Append(mocks.AssistantTextMessage(output))
			sess.CostVal = &llm.ResultMessage{
				Type:       "result",
				Subtype:    "success",
				Result:     "done",
				StopReason: "end_turn",
			}
			sess.StatusChVal <- "SUCCESS"
		} else if permissionFailure {
			req := mocks.ControlRequestMsg("perm-1", "Bash").ControlRequest
			sess.LastControlRequestVal = req
			sess.AttachChVal <- llm.SDKMessage{Type: "control_request", ControlRequest: req}
		}
		return sess
	}

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		sess := newSession()
		sess.IDVal = id
		sess.FeatureIDVal = featureID
		sess.PhaseVal = phase
		return sess, nil
	}

	pr := &agent.PhaseRunner{
		SessionManager: sm,
		StateDir:       t.TempDir(),
	}
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		return []string{"mock"}, nil, &ports.SessionOpts{RepoName: opts.RepoName}, nil
	}
	return pr
}

// newRepublishRepo returns a worktree whose branch is already on its bare
// origin, so origin/<branch> resolves and a republish has something to compare.
func newRepublishRepo(t *testing.T, branch string) string {
	t.Helper()
	repo, bare := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repo, branch)
	testutil.CommitFile(t, repo, "first.txt", "first\n", "first commit")
	testutil.SimulatePush(t, repo, bare, branch, branch)
	return repo
}

func republishFeature(id, repoPath, branch string) *feature.Feature {
	return &feature.Feature{
		ID:           id,
		Name:         "republish",
		Slug:         "republish",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branch, BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true, PRURL: "https://github.com/org/r1/pull/1"},
		},
	}
}

// A fast-forwardable republish still routes through live remote inspection.
// The Git primitive may choose an ordinary push after inspection. No PR is
// created and no description is generated — with a nil PhaseRunner, any
// attempt to generate one would fail the publish.
func TestOrchestrator_Republish_FastForwardRoutesThroughLiveInspectionWithoutCreatePR(t *testing.T) {
	repoPath := newRepublishRepo(t, "feature/republish")
	testutil.CommitFile(t, repoPath, "later.txt", "later\n", "later pass")
	f := republishFeature("feat-republish-ff", repoPath, "feature/republish")
	fs, lc := newStoredFeatureFixture(t, f)

	pub := mocks.NewMockRemoteOps()
	pub.PushRewrittenBranchFn = func(path, branch string) error { return nil }

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs, Remote: pub}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-republish-ff", orchestrator.PublishOptions{
		Repos: []string{"r1"},
	}); err != nil {
		t.Fatalf("PublishWithOptions: %v", err)
	}

	if got := countPublisherCalls(pub, "PushRewrittenBranch"); got != 1 {
		t.Errorf("PushRewrittenBranch calls = %d; want 1", got)
	}
	for _, method := range []string{"Push", "CreatePR"} {
		if got := countPublisherCalls(pub, method); got != 0 {
			t.Errorf("%s calls = %d; want 0", method, got)
		}
	}
	if state := storedRepoState(t, fs, f.ID, "r1"); state.PRURL == "" {
		t.Fatal("repo PR URL was not persisted")
	}
}

// A locally rewritten branch cannot fast-forward, so the republish uses a
// lease push instead of clobbering blindly.
func TestOrchestrator_Republish_RewriteUsesLeasePush(t *testing.T) {
	repoPath := newRepublishRepo(t, "feature/republish-rewrite")
	runPublishGit(t, repoPath, "reset", "--hard", "HEAD~1")
	testutil.CommitFile(t, repoPath, "rewritten.txt", "rewritten\n", "rewritten pass")
	f := republishFeature("feat-republish-rw", repoPath, "feature/republish-rewrite")
	fs, lc := newStoredFeatureFixture(t, f)

	pub := mocks.NewMockRemoteOps()
	pub.PushRewrittenBranchFn = func(path, branch string) error { return nil }

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs, Remote: pub}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-republish-rw", orchestrator.PublishOptions{
		Repos: []string{"r1"},
	}); err != nil {
		t.Fatalf("PublishWithOptions: %v", err)
	}

	if got := countPublisherCalls(pub, "PushRewrittenBranch"); got != 1 {
		t.Errorf("PushRewrittenBranch calls = %d; want 1", got)
	}
	for _, method := range []string{"Push", "CreatePR"} {
		if got := countPublisherCalls(pub, method); got != 0 {
			t.Errorf("%s calls = %d; want 0", method, got)
		}
	}
}

// Uncommitted work is committed before the republish pushes.
func TestOrchestrator_Republish_CommitsUncommittedChanges(t *testing.T) {
	repoPath := newRepublishRepo(t, "feature/republish-dirty")
	if err := os.WriteFile(filepath.Join(repoPath, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	f := republishFeature("feat-republish-dirty", repoPath, "feature/republish-dirty")
	fs, lc := newStoredFeatureFixture(t, f)

	pub := mocks.NewMockRemoteOps()
	pub.PushRewrittenBranchFn = func(path, branch string) error { return nil }

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs, Remote: pub}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-republish-dirty", orchestrator.PublishOptions{
		Repos: []string{"r1"},
	}); err != nil {
		t.Fatalf("PublishWithOptions: %v", err)
	}
	if git.HasUncommittedChanges(repoPath) {
		t.Error("republish left local changes uncommitted")
	}
}

// A push failure is recorded on the repository and surfaced to the caller.
func TestOrchestrator_Republish_PushFailureRecorded(t *testing.T) {
	repoPath := newRepublishRepo(t, "feature/republish-fail")
	testutil.CommitFile(t, repoPath, "later.txt", "later\n", "later pass")
	f := republishFeature("feat-republish-fail", repoPath, "feature/republish-fail")
	fs, lc := newStoredFeatureFixture(t, f)

	pub := mocks.NewMockRemoteOps()
	pub.PushRewrittenBranchFn = func(path, branch string) error { return errors.New("remote rejected") }

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs, Remote: pub}, orchestrator.Hooks{})

	err := o.PublishWithOptions("feat-republish-fail", orchestrator.PublishOptions{
		Repos: []string{"r1"},
	})
	if err == nil {
		t.Fatal("expected an error from the failing push, got nil")
	}
	if !strings.Contains(err.Error(), "remote rejected") {
		t.Errorf("error = %q; want it to mention the injected failure", err.Error())
	}
	if got := countPublisherCalls(pub, "PushRewrittenBranch"); got != 1 {
		t.Errorf("PushRewrittenBranch calls = %d; want 1", got)
	}
	if state := storedRepoState(t, fs, f.ID, "r1"); state.Error == nil {
		t.Fatal("repo publish failure was not persisted")
	}
}

// realGitPublishRemoteOps keeps Git push behavior genuinely unmocked while
// replacing GitHub API calls so tests never reach an external service.
type realGitPublishRemoteOps struct {
	createdPRURL string
}

func (realGitPublishRemoteOps) Push(path, branch string) error { return git.Push(path, branch) }
func (realGitPublishRemoteOps) PushRewrittenBranch(path, branch string) error {
	return git.PushRewrittenBranch(path, branch)
}
func (realGitPublishRemoteOps) PullRebase(path, branch string) error {
	return git.PullRebase(path, branch).Err
}
func (o realGitPublishRemoteOps) CreatePR(string, string, string, string, string, bool) (string, error) {
	if o.createdPRURL == "" {
		return "", errors.New("CreatePR is not used by this test")
	}
	return o.createdPRURL, nil
}
func (realGitPublishRemoteOps) PRBaseBranch(string, string) string { return "" }
func (realGitPublishRemoteOps) PRState(string, string) (string, error) {
	return "", errors.New("state lookup unavailable in test")
}

// The first manual CodeReady publish has no remote pull-request branch yet.
// It still travels through PushRewrittenBranch, which must create that branch
// with an ordinary push before the orchestrator records the new PR.
func TestOrchestrator_Publish_ManualCodeReadyCreatesAbsentRemoteBranch(t *testing.T) {
	const branch = "feature/manual-first-publish"
	const prURL = "https://github.com/org/r1/pull/1"
	repoPath, bareRemote := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repoPath, branch)
	testutil.CommitFile(t, repoPath, "first.txt", "first\n", "first publish")
	// The bare origin is probed with a plain git command: the remote branch
	// must start absent so the first publish creates it.
	remoteBranchExists := func(branch string) bool {
		return exec.Command("git", "--git-dir", bareRemote, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
	}
	if remoteBranchExists(branch) {
		t.Fatalf("remote branch %s exists before first publish", branch)
	}

	f := &feature.Feature{
		ID:           "feat-manual-first-publish",
		Name:         "manual first publish",
		Slug:         "manual-first-publish",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branch, BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	fs, lc := newStoredFeatureFixture(t, f)
	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
		Remote:  realGitPublishRemoteOps{createdPRURL: prURL},
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions(f.ID, orchestrator.PublishOptions{
		Repos: []string{"r1"},
		Title: "Manual first publish",
		Body:  "Verified body",
	}); err != nil {
		t.Fatalf("PublishWithOptions() error = %v", err)
	}
	if !remoteBranchExists(branch) {
		t.Fatalf("remote branch %s does not exist after first publish", branch)
	}

	remoteTip := runPublishGitOutput(t, bareRemote, "rev-parse", "refs/heads/"+branch)
	localTip := runPublishGitOutput(t, repoPath, "rev-parse", "HEAD")
	if remoteTip != localTip {
		t.Fatalf("remote branch tip = %s; want published local HEAD %s", remoteTip, localTip)
	}
	if got := storedRepoState(t, fs, f.ID, "r1").PRURL; got != prURL {
		t.Fatalf("published PR URL = %q; want %s", got, prURL)
	}
}

// A stale tracking ref must not choose the republish transport. The live
// remote can contain a redundant merge after origin/<branch> was last updated;
// PushRewrittenBranch must inspect and prove that live merge before replacing
// it with the rewritten local head.
func TestOrchestrator_Republish_StaleTrackingRefAllowsRedundantLiveMerge(t *testing.T) {
	const branch = "feature/republish-stale-redundant-merge"
	repoPath, bareRemote := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repoPath, branch)
	featureParent := testutil.CommitFile(t, repoPath, "feature.txt", "feature\n", "feature parent")
	testutil.SimulatePush(t, repoPath, bareRemote, branch, branch)

	runPublishGit(t, repoPath, "checkout", mainBranch)
	testutil.CommitFile(t, repoPath, "main-1.txt", "main 1\n", "main 1")
	mainParent := testutil.CommitFile(t, repoPath, "main-2.txt", "main 2\n", "main 2")
	runPublishGit(t, repoPath, "checkout", branch)
	runPublishGit(t, repoPath, "merge", "--no-ff", mainParent, "-m", "remote redundant merge")
	remoteMerge := runPublishGitOutput(t, repoPath, "rev-parse", "HEAD")
	if remoteMerge == featureParent {
		t.Fatalf("remote merge = stale feature parent %s; want a distinct merge commit", featureParent)
	}
	if got := runPublishGitOutput(t, repoPath, "show", "--remerge-diff", "--format=", "--no-ext-diff", remoteMerge); got != "" {
		t.Fatalf("remote merge remerge diff = %q; want a redundant merge with no unique resolution", got)
	}

	// Advance only the live bare remote. Deliberately do not fetch it back into
	// the worktree: origin/<branch> must remain at the old feature parent.
	runPublishGit(t, bareRemote, "fetch", repoPath, "HEAD:refs/heads/"+branch)
	if got := runPublishGitOutput(t, repoPath, "rev-parse", "origin/"+branch); got != featureParent {
		t.Fatalf("tracking ref = %s; want stale feature parent %s", got, featureParent)
	}
	if got := runPublishGitOutput(t, bareRemote, "rev-parse", "refs/heads/"+branch); got != remoteMerge {
		t.Fatalf("live remote = %s; want redundant merge %s", got, remoteMerge)
	}

	// Rewrite locally without the remote merge commit, but retain both of its
	// parents in the ancestry of the new local head.
	runPublishGit(t, repoPath, "reset", "--hard", featureParent)
	runPublishGit(t, repoPath, "merge", "--no-ff", mainParent, "-m", "rewritten local merge")
	testutil.CommitFile(t, repoPath, "local-head.txt", "local head\n", "local rewritten head")
	localHead := runPublishGitOutput(t, repoPath, "rev-parse", "HEAD")
	remoteParents := strings.Fields(runPublishGitOutput(t, repoPath, "rev-list", "--parents", "-n", "1", remoteMerge))
	if len(remoteParents) != 3 {
		t.Fatalf("remote merge parents = %v; want exactly two parents", remoteParents[1:])
	}
	for _, parent := range remoteParents[1:] {
		runPublishGit(t, repoPath, "merge-base", "--is-ancestor", parent, "HEAD")
	}

	f := republishFeature("feat-republish-stale-redundant-merge", repoPath, branch)
	fs, lc := newStoredFeatureFixture(t, f)
	o := orchestrator.New(orchestrator.Deps{
		Manager: lc,
		Store:   fs,
		Remote:  realGitPublishRemoteOps{},
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions(f.ID, orchestrator.PublishOptions{Repos: []string{"r1"}}); err != nil {
		t.Fatalf("PublishWithOptions() error = %v; live redundant merge should be replaceable", err)
	}
	if got := runPublishGitOutput(t, bareRemote, "rev-parse", "refs/heads/"+branch); got != localHead {
		t.Fatalf("remote tip = %s; want rewritten local head %s", got, localHead)
	}
	if state := storedRepoState(t, fs, f.ID, "r1"); state.PRURL == "" {
		t.Fatal("repo PR URL was not persisted")
	}
}

// An unfetched remote commit is translated through the real Git rewritten-push
// implementation into an orchestrator-owned divergence error. The test uses a
// local bare remote and answers the PR-state lookup locally, so it never reaches
// an external service.
func TestOrchestrator_Republish_RemoteDivergedErrorFromRealGit(t *testing.T) {
	branch := "feature/republish-lease"
	repoPath := newRepublishRepo(t, branch)
	bareRemote := runPublishGitOutput(t, repoPath, "remote", "get-url", "origin")

	// A second clone pushes a commit this repo never fetches.
	clonePath := t.TempDir()
	runPublishGit(t, "", "clone", bareRemote, clonePath)
	runPublishGit(t, clonePath, "config", "user.email", "test@test.com")
	runPublishGit(t, clonePath, "config", "user.name", "Test")
	runPublishGit(t, clonePath, "checkout", branch)
	testutil.CommitFile(t, clonePath, "other.txt", "other\n", "other's commit")
	runPublishGit(t, clonePath, "push", "origin", branch)

	// Rewrite the branch locally without fetching, so the stale
	// origin/<branch> tracking ref is not an ancestor of HEAD.
	runPublishGit(t, repoPath, "reset", "--hard", "HEAD~1")
	testutil.CommitFile(t, repoPath, "rewritten.txt", "rewritten\n", "rewritten pass")

	f := republishFeature("feat-republish-lease", repoPath, branch)
	fs, lc := newStoredFeatureFixture(t, f)

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs, Remote: realGitPublishRemoteOps{}}, orchestrator.Hooks{})

	err := o.PublishWithOptions("feat-republish-lease", orchestrator.PublishOptions{
		Repos: []string{"r1"},
	})
	if err == nil {
		t.Fatal("PublishWithOptions() = nil, want an error: the remote moved and this repo never fetched")
	}
	var diverged *orchestrator.PublishRemoteDivergedError
	if !errors.As(err, &diverged) {
		t.Fatalf("PublishWithOptions() error = %T %v; want PublishRemoteDivergedError", err, err)
	}
	if diverged.RepoName != "r1" || diverged.Branch != branch || diverged.RemoteOnlyCommits != 2 {
		t.Fatalf("PublishRemoteDivergedError = %+v; want repo r1, branch %s, 2 remote commits", diverged, branch)
	}
	if state := storedRepoState(t, fs, f.ID, "r1"); state.Error == nil {
		t.Fatal("repo publish failure was not persisted")
	}
}

// runPublishGitOutput runs a git command in repo and returns trimmed stdout.
func runPublishGitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if repo != "" {
		cmd.Dir = repo
	}
	cmd.Env = testutil.GitTestEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// A merged pull request whose branch was deleted still leaves PRURL recorded
// and a stale origin/<branch> resolvable, so the republish would plain-push a
// branch with no pull request behind it and report success. Refuse instead.
func TestOrchestrator_Republish_RefusesWhenPRNoLongerOpen(t *testing.T) {
	for _, state := range []string{"merged", "closed"} {
		t.Run(state, func(t *testing.T) {
			repoPath := newRepublishRepo(t, "feature/republish-"+state)
			testutil.CommitFile(t, repoPath, "later.txt", "later\n", "later pass")
			f := republishFeature("feat-republish-"+state, repoPath, "feature/republish-"+state)
			fs, lc := newStoredFeatureFixture(t, f)

			pub := mocks.NewMockRemoteOps()
			pub.PRStateFn = func(repoPath, prURL string) (string, error) { return state, nil }

			o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs, Remote: pub}, orchestrator.Hooks{})

			err := o.PublishWithOptions("feat-republish-"+state, orchestrator.PublishOptions{
				Repos: []string{"r1"},
			})
			if err == nil {
				t.Fatal("PublishWithOptions() = nil, want an error: the pull request is not open")
			}
			if !strings.Contains(err.Error(), state) {
				t.Errorf("error = %q; want it to name the pull-request state %q", err.Error(), state)
			}
			for _, method := range []string{"Push", "PushRewrittenBranch", "CreatePR"} {
				if got := countPublisherCalls(pub, method); got != 0 {
					t.Errorf("%s calls = %d; want 0 — nothing may be pushed", method, got)
				}
			}
			if state := storedRepoState(t, fs, f.ID, "r1"); state.Error == nil {
				t.Fatal("repo publish failure was not persisted")
			}
		})
	}
}

// An unavailable state lookup must not block a legitimate republish.
func TestOrchestrator_Republish_ProceedsWhenPRStateIndeterminate(t *testing.T) {
	repoPath := newRepublishRepo(t, "feature/republish-unknown")
	testutil.CommitFile(t, repoPath, "later.txt", "later\n", "later pass")
	f := republishFeature("feat-republish-unknown", repoPath, "feature/republish-unknown")
	fs, lc := newStoredFeatureFixture(t, f)

	pub := mocks.NewMockRemoteOps()
	pub.PushRewrittenBranchFn = func(path, branch string) error { return nil }
	pub.PRStateFn = func(repoPath, prURL string) (string, error) {
		return "", errors.New("api unreachable")
	}

	o := orchestrator.New(orchestrator.Deps{Manager: lc, Store: fs, Remote: pub}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-republish-unknown", orchestrator.PublishOptions{
		Repos: []string{"r1"},
	}); err != nil {
		t.Fatalf("PublishWithOptions: %v", err)
	}
	if got := countPublisherCalls(pub, "PushRewrittenBranch"); got != 1 {
		t.Errorf("PushRewrittenBranch calls = %d; want 1", got)
	}
}
