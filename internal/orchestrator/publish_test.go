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
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
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
	lc := lifecycleForFeature(f)
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	var publishedCount int
	var publishedFeatureID string
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureCompleted: func(id string, fv *feature.Feature) {
			publishedCount++
			publishedFeatureID = id
		},
	})

	publishRepoCalls := make(map[string]int)
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		publishRepoCalls[repo]++
		return "https://github.com/org/" + repo + "/pull/1", nil
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

	assertLifecycleCall(t, lc, "TryCompletePublish")
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
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

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
	lc := lifecycleForFeature(f)
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

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
	lc := lifecycleForFeature(f)
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	calls := make(map[string]int)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})
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
	lc := lifecycleForFeature(f)
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	var calls int
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})
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
	lc := lifecycleForFeature(f)
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

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
	lc := lifecycleForFeature(f)
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

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
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})
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
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Remote:    pub,
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

	assertLifecycleCall(t, lc, "SetRepoPublished")
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
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishErrorFn = func(id, repo, msg string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Remote:    pub,
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

	assertLifecycleCall(t, lc, "SetRepoPublishError")
	// Push and CreatePR should NOT have been called because we bailed on rebase.
	for _, c := range pub.Calls {
		if c.Method == "Push" || c.Method == "CreatePR" {
			t.Errorf("%s should not have been called after rebase conflict", c.Method)
		}
	}
}

func TestOrchestrator_PublishRepo_ManualCodeReadyUsesForcePushWithLease(t *testing.T) {
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
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error {
		t.Fatalf("manual publish after a rebase must not plain-push %s from %s", branch, path)
		return nil
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		return "https://github.com/org/r1/pull/1", nil
	}

	pub.ForcePushFn = func(worktreePath, branch string) error { return nil }

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Remote:    pub,
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-manual-publish-rebased", orchestrator.PublishOptions{
		Title: "Publish repo",
		Body:  "Verified body",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if got := countPublisherCalls(pub, "ForcePush"); got != 1 {
		t.Fatalf("RemoteOps.ForcePush calls = %d, want 1", got)
	}
	if got := countPublisherCalls(pub, "CreatePR"); got != 1 {
		t.Fatalf("Publisher.CreatePR calls = %d, want 1", got)
	}
	assertLifecycleCall(t, lc, "SetRepoPublished")
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
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }

	var gotTitle, gotBody string
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotTitle, gotBody = title, body
		return "https://github.com/org/r1/pull/1", nil
	}

	pr := newPublishDescriptionPhaseRunner(t, "TITLE: Session Title\nBODY:\n## Summary\n\nGenerated body", false)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
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

func TestOrchestrator_PublishRepo_FailsAndLogsDescriptionGenerationErrors(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-fallback",
		Name:   "cool-feature",
		Slug:   "cool-feature",
		Status: feature.StatusReviewPassed,
		Models: config.ModelConfig{Planning: "sonnet"},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: wtR1Path, BaseBranch: mainBranch},
		},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }

	createPRCalls := 0
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		createPRCalls++
		return "https://github.com/org/r1/pull/1", nil
	}

	pr := newPublishDescriptionPhaseRunner(t, "", true)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
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

	logPath := filepath.Join(agent.ActiveRunDir(pr.StateDir, f), "publish", "error.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", logPath, err)
	}
	if !strings.Contains(string(data), "description generation: generating description:") {
		t.Errorf("error log = %q, want description generation context", string(data))
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
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	pr := newPublishDescriptionPhaseRunner(t, "TITLE: Generated selected title\nBODY:\nGenerated selected body", false)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
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
	pub := mocks.NewMockRemoteOps()
	pr := newPublishDescriptionPhaseRunner(t, "", true)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lifecycleForFeature(f),
		Store:       newFeatureStore(f),
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
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }

	var gotDraft bool
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotDraft = draft
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Remote:    pub,
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
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }

	var gotDraft bool
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotDraft = draft
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Remote:    pub,
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

type publishDescriptionSessionHandle struct {
	id          string
	featureID   string
	phase       feature.Phase
	done        chan struct{}
	statusCh    chan string
	attachCh    chan llm.SDKMessage
	msgLog      *session.MessageLog
	result      *llm.ResultMessage
	lastControl *llm.ControlRequestMessage
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

func newPublishDescriptionSessionHandle() *publishDescriptionSessionHandle {
	return &publishDescriptionSessionHandle{
		done:     make(chan struct{}),
		statusCh: make(chan string, 1),
		attachCh: make(chan llm.SDKMessage, 1),
		msgLog:   session.NewMessageLog(),
	}
}

func (s *publishDescriptionSessionHandle) ID() string              { return s.id }
func (s *publishDescriptionSessionHandle) FeatureID() string       { return s.featureID }
func (s *publishDescriptionSessionHandle) Phase() feature.Phase    { return s.phase }
func (s *publishDescriptionSessionHandle) RepoName() string        { return "" }
func (s *publishDescriptionSessionHandle) PermCacheScope() string  { return "" }
func (s *publishDescriptionSessionHandle) Kind() ports.SessionKind { return ports.KindPhase }
func (s *publishDescriptionSessionHandle) Label() string           { return "" }
func (s *publishDescriptionSessionHandle) Status() session.SessionStatus {
	return session.SessionRunning
}
func (s *publishDescriptionSessionHandle) IsActive() bool                   { return true }
func (s *publishDescriptionSessionHandle) Iteration() int                   { return 0 }
func (s *publishDescriptionSessionHandle) StartedAt() time.Time             { return time.Time{} }
func (s *publishDescriptionSessionHandle) WaitingSince() time.Time          { return time.Time{} }
func (s *publishDescriptionSessionHandle) InitialPrompt() string            { return "" }
func (s *publishDescriptionSessionHandle) ProviderName() string             { return "" }
func (s *publishDescriptionSessionHandle) Model() string                    { return "" }
func (s *publishDescriptionSessionHandle) WorkDir() string                  { return "" }
func (s *publishDescriptionSessionHandle) EffectiveEffort() llm.EffortLevel { return "" }
func (s *publishDescriptionSessionHandle) EffortSource() llm.EffortSource   { return "" }
func (s *publishDescriptionSessionHandle) MessageLog() ports.MessageLog {
	return s.msgLog
}
func (s *publishDescriptionSessionHandle) Cost() *llm.ResultMessage { return s.result }
func (s *publishDescriptionSessionHandle) LatestUsage() *llm.Usage  { return nil }
func (s *publishDescriptionSessionHandle) AccumulatedUsage() llm.Usage {
	return llm.Usage{}
}
func (s *publishDescriptionSessionHandle) LastControlRequest() *llm.ControlRequestMessage {
	return s.lastControl
}
func (s *publishDescriptionSessionHandle) PendingControlRequests() []*llm.ControlRequestMessage {
	if s.lastControl == nil {
		return nil
	}
	return []*llm.ControlRequestMessage{s.lastControl}
}
func (s *publishDescriptionSessionHandle) QALog() []session.QAPair         { return nil }
func (s *publishDescriptionSessionHandle) LogFilePath() string             { return "" }
func (s *publishDescriptionSessionHandle) ContextPercentage() int          { return 0 }
func (s *publishDescriptionSessionHandle) ErrorDetail() string             { return "" }
func (s *publishDescriptionSessionHandle) ExitCodeDetail() string          { return "" }
func (s *publishDescriptionSessionHandle) LastStdoutAt() time.Time         { return time.Time{} }
func (s *publishDescriptionSessionHandle) StatusCh() <-chan string         { return s.statusCh }
func (s *publishDescriptionSessionHandle) AttachCh() <-chan llm.SDKMessage { return s.attachCh }
func (s *publishDescriptionSessionHandle) Done() <-chan struct{}           { return s.done }
func (s *publishDescriptionSessionHandle) HasPendingAskUserQuestion() bool {
	return false
}
func (s *publishDescriptionSessionHandle) HasPendingRootAskUserQuestion() bool {
	return false
}
func (s *publishDescriptionSessionHandle) RootCompletionIntent() llm.CompletionIntent {
	return llm.CompletionIntent{}
}
func (s *publishDescriptionSessionHandle) LiveBackgroundTaskCount() int { return 0 }
func (s *publishDescriptionSessionHandle) TaskActivities() []llm.TaskActivity {
	return nil
}
func (s *publishDescriptionSessionHandle) SendUserMessage(text string) error { return nil }
func (s *publishDescriptionSessionHandle) RespondToControl(requestID string, allow bool, reason string) error {
	return nil
}
func (s *publishDescriptionSessionHandle) RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]llm.AskUserAnnotation) error {
	return nil
}
func (s *publishDescriptionSessionHandle) ClearPendingQuestion(requestID string)  {}
func (s *publishDescriptionSessionHandle) ResetWaitingStatus()                    {}
func (s *publishDescriptionSessionHandle) Stop() error                            { return nil }
func (s *publishDescriptionSessionHandle) Interrupt() error                       { return nil }
func (s *publishDescriptionSessionHandle) Wait()                                  {}
func (s *publishDescriptionSessionHandle) SetStatus(status session.SessionStatus) {}
func (s *publishDescriptionSessionHandle) SetLogFile(f *os.File)                  {}
func (s *publishDescriptionSessionHandle) AddCleanupFunc(fn func())               {}
func (s *publishDescriptionSessionHandle) SetHasUnansweredQuestion(v bool)        {}
func (s *publishDescriptionSessionHandle) CloseStdin()                            {}
func (s *publishDescriptionSessionHandle) SetOnToolAllowed(fn func(toolName string, input json.RawMessage)) {
}
func (s *publishDescriptionSessionHandle) SetOnFileRead(fn func(read llm.FileReadEvent))  {}
func (s *publishDescriptionSessionHandle) SetOnSubagentEvent(fn func(msg llm.SDKMessage)) {}

var _ ports.SessionHandle = (*publishDescriptionSessionHandle)(nil)

func newPublishDescriptionPhaseRunner(t *testing.T, output string, permissionFailure bool) *agent.PhaseRunner {
	t.Helper()

	newSession := func() *publishDescriptionSessionHandle {
		sess := newPublishDescriptionSessionHandle()
		if output != "" {
			sess.msgLog.Append(mocks.AssistantTextMessage(output))
			sess.result = &llm.ResultMessage{
				Type:       "result",
				Subtype:    "success",
				Result:     "done",
				StopReason: "end_turn",
			}
			sess.statusCh <- "SUCCESS"
		} else if permissionFailure {
			req := mocks.ControlRequestMsg("perm-1", "Bash").ControlRequest
			sess.lastControl = req
			sess.attachCh <- llm.SDKMessage{Type: "control_request", ControlRequest: req}
		}
		return sess
	}

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		sess := newSession()
		sess.id = id
		sess.featureID = featureID
		sess.phase = phase
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

// A repository whose remote branch is an ancestor of HEAD is fast-forwarded.
// No PR is created and no description is generated — with a nil PhaseRunner,
// any attempt to generate one would fail the publish.
func TestOrchestrator_Republish_FastForwardPushesWithoutCreatePR(t *testing.T) {
	repoPath := newRepublishRepo(t, "feature/republish")
	testutil.CommitFile(t, repoPath, "later.txt", "later\n", "later pass")
	f := republishFeature("feat-republish-ff", repoPath, "feature/republish")
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Remote: pub}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-republish-ff", orchestrator.PublishOptions{
		Repos: []string{"r1"},
	}); err != nil {
		t.Fatalf("PublishWithOptions: %v", err)
	}

	if got := countPublisherCalls(pub, "Push"); got != 1 {
		t.Errorf("Push calls = %d; want 1", got)
	}
	for _, method := range []string{"ForcePush", "CreatePR"} {
		if got := countPublisherCalls(pub, method); got != 0 {
			t.Errorf("%s calls = %d; want 0", method, got)
		}
	}
	call := assertLifecycleCall(t, lc, "SetRepoPublished")
	if call == nil {
		t.FailNow()
	}
}

// A locally rewritten branch cannot fast-forward, so the republish uses a
// lease push instead of clobbering blindly.
func TestOrchestrator_Republish_RewriteUsesLeasePush(t *testing.T) {
	repoPath := newRepublishRepo(t, "feature/republish-rewrite")
	runPublishGit(t, repoPath, "reset", "--hard", "HEAD~1")
	testutil.CommitFile(t, repoPath, "rewritten.txt", "rewritten\n", "rewritten pass")
	f := republishFeature("feat-republish-rw", repoPath, "feature/republish-rewrite")
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.ForcePushFn = func(path, branch string) error { return nil }

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Remote: pub}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-republish-rw", orchestrator.PublishOptions{
		Repos: []string{"r1"},
	}); err != nil {
		t.Fatalf("PublishWithOptions: %v", err)
	}

	if got := countPublisherCalls(pub, "ForcePush"); got != 1 {
		t.Errorf("ForcePush calls = %d; want 1", got)
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
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Remote: pub}, orchestrator.Hooks{})

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
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishErrorFn = func(id, repo, msg string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return errors.New("remote rejected") }

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Remote: pub}, orchestrator.Hooks{})

	err := o.PublishWithOptions("feat-republish-fail", orchestrator.PublishOptions{
		Repos: []string{"r1"},
	})
	if err == nil {
		t.Fatal("expected an error from the failing push, got nil")
	}
	if !strings.Contains(err.Error(), "remote rejected") {
		t.Errorf("error = %q; want it to mention the injected failure", err.Error())
	}
	if got := countPublisherCalls(pub, "Push"); got != 1 {
		t.Errorf("Push calls = %d; want 1", got)
	}
	assertLifecycleCall(t, lc, "SetRepoPublishError")
}

// leaseTestRemoteOps keeps the push path genuinely unmocked — git.ForcePush is
// the code under test — while answering the PR-state lookup locally so the
// test never reaches the network.
type leaseTestRemoteOps struct{}

func (leaseTestRemoteOps) Push(path, branch string) error      { return git.Push(path, branch) }
func (leaseTestRemoteOps) ForcePush(path, branch string) error { return git.ForcePush(path, branch) }
func (leaseTestRemoteOps) PullRebase(path, branch string) error {
	return git.PullRebase(path, branch).Err
}
func (leaseTestRemoteOps) CreatePR(string, string, string, string, string, bool) (string, error) {
	return "", errors.New("CreatePR is not used by this test")
}
func (leaseTestRemoteOps) PRBaseBranch(string, string) string { return "" }
func (leaseTestRemoteOps) PRState(string, string) (string, error) {
	return "", errors.New("state lookup unavailable in test")
}

// A caller-side fetch before the lease push would refresh the stale tracking
// ref to the true remote tip right before --force-with-lease compares
// against it, making the lease pass and silently discard the second clone's
// commit. This test uses leaseTestRemoteOps, which delegates pushes to the
// real git package, driving an unmocked push through pushRepublish while
// answering the PR-state lookup locally so the test never reaches the
// network.
func TestOrchestrator_Republish_LeaseRejectsWhenRemoteMovedUnfetched(t *testing.T) {
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
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishErrorFn = func(id, repo, msg string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Remote: leaseTestRemoteOps{}}, orchestrator.Hooks{})

	err := o.PublishWithOptions("feat-republish-lease", orchestrator.PublishOptions{
		Repos: []string{"r1"},
	})
	if err == nil {
		t.Fatal("PublishWithOptions() = nil, want an error: the remote moved and this repo never fetched")
	}
	assertLifecycleCall(t, lc, "SetRepoPublishError")
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
			lc := lifecycleForFeature(f)
			lc.SetRepoPublishErrorFn = func(id, repo, msg string) error { return nil }
			lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
			fs := newFeatureStore(f)

			pub := mocks.NewMockRemoteOps()
			pub.PRStateFn = func(repoPath, prURL string) (string, error) { return state, nil }

			o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Remote: pub}, orchestrator.Hooks{})

			err := o.PublishWithOptions("feat-republish-"+state, orchestrator.PublishOptions{
				Repos: []string{"r1"},
			})
			if err == nil {
				t.Fatal("PublishWithOptions() = nil, want an error: the pull request is not open")
			}
			if !strings.Contains(err.Error(), state) {
				t.Errorf("error = %q; want it to name the pull-request state %q", err.Error(), state)
			}
			for _, method := range []string{"Push", "ForcePush", "CreatePR"} {
				if got := countPublisherCalls(pub, method); got != 0 {
					t.Errorf("%s calls = %d; want 0 — nothing may be pushed", method, got)
				}
			}
			assertLifecycleCall(t, lc, "SetRepoPublishError")
		})
	}
}

// An unavailable state lookup must not block a legitimate republish.
func TestOrchestrator_Republish_ProceedsWhenPRStateIndeterminate(t *testing.T) {
	repoPath := newRepublishRepo(t, "feature/republish-unknown")
	testutil.CommitFile(t, repoPath, "later.txt", "later\n", "later pass")
	f := republishFeature("feat-republish-unknown", repoPath, "feature/republish-unknown")
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error { return nil }
	pub.PRStateFn = func(repoPath, prURL string) (string, error) {
		return "", errors.New("api unreachable")
	}

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Remote: pub}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-republish-unknown", orchestrator.PublishOptions{
		Repos: []string{"r1"},
	}); err != nil {
		t.Fatalf("PublishWithOptions: %v", err)
	}
	if got := countPublisherCalls(pub, "Push"); got != 1 {
		t.Errorf("Push calls = %d; want 1", got)
	}
}
