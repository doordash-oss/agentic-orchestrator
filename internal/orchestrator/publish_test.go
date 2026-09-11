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

// publishRepo commits uncommitted changes, pushes the single layer branch
// through the guarded layer push, creates the PR titled with the layer's
// table title, and records success on Lifecycle. End-to-end via o.Publish.
func TestOrchestrator_PublishRepo_EndToEnd_SingleLayerStack(t *testing.T) {
	repoPath := newPublishReadyBranch(t, "feature/cool-feature")
	if err := os.WriteFile(filepath.Join(repoPath, "change.txt"), []byte("change\n"), 0o644); err != nil {
		t.Fatalf("write change: %v", err)
	}
	f := &feature.Feature{
		ID:     "feat-pubrepo",
		Name:   "cool-feature",
		Slug:   "cool-feature",
		Status: feature.StatusReviewPassed,
		Stack:  singleLayerStack(1, "feature/cool-feature"),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/cool-feature", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error {
		t.Fatalf("stack publish must deliver through PushLayerBranch, not a plain push of %s", branch)
		return nil
	}
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-pubrepo", orchestrator.PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if git.HasUncommittedChanges(repoPath) {
		t.Fatal("publish left local changes uncommitted")
	}
	for _, want := range []string{"PushLayerBranch", "CreatePR"} {
		if countPublisherCalls(pub, want) != 1 {
			t.Errorf("expected RemoteOps.%s once", want)
		}
	}

	assertLifecycleCall(t, lc, "SetRepoPublished")
}

// A manual CodeReady publish delivers the layer branch through the guarded
// layer push (a plain push would clobber a rebase child's rewritten remote).
func TestOrchestrator_PublishRepo_ManualCodeReadyUsesLayerPush(t *testing.T) {
	repoPath := newPublishReadyBranch(t, "feature/manual-publish-rebased")
	testutil.CommitFile(t, repoPath, "change.txt", "change\n", "publish change")
	f := &feature.Feature{
		ID:           "feat-manual-publish-rebased",
		Name:         "manual publish rebased",
		Slug:         "manual-publish-rebased",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Stack:        singleLayerStack(1, "feature/manual-publish-rebased"),
		Repos: []feature.FeatureRepo{
			{
				Name:         "r1",
				Path:         repoPath,
				WorktreePath: repoPath,
				Branch:       "feature/manual-publish-rebased",
				BaseBranch:   mainBranch,
			},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error {
		t.Fatalf("stack publish must not plain-push %s from %s", branch, path)
		return nil
	}
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-manual-publish-rebased", orchestrator.PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if got := countPublisherCalls(pub, "PushLayerBranch"); got != 1 {
		t.Fatalf("RemoteOps.PushLayerBranch calls = %d, want 1", got)
	}
	if got := countPublisherCalls(pub, "CreatePR"); got != 1 {
		t.Fatalf("RemoteOps.CreatePR calls = %d, want 1", got)
	}
	assertLifecycleCall(t, lc, "SetRepoPublished")
}

func TestOrchestrator_Publish_LayerPushRemoteDiverged(t *testing.T) {
	repoPath := newPublishReadyBranch(t, "feature/remote-diverged")
	testutil.CommitFile(t, repoPath, "change.txt", "change\n", "publish change")
	f := &feature.Feature{
		ID:           "feat-publish-remote-diverged",
		Name:         "publish remote diverged",
		Slug:         "publish-remote-diverged",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Stack:        singleLayerStack(1, "feature/remote-diverged"),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/remote-diverged", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishErrorFn = func(id, repo string, record errcat.FailureRecord) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return "", &git.RewritePushError{
			Kind:              git.RewritePushRemoteDiverged,
			Branch:            branch,
			RemoteOnlyCommits: 2,
		}
	}
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       newFeatureStore(f),
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	err := o.PublishWithOptions(f.ID, orchestrator.PublishOptions{
		Repos: []string{"r1"},
	})
	var diverged *orchestrator.PublishRemoteDivergedError
	if !errors.As(err, &diverged) {
		t.Fatalf("error = %T %v; want PublishRemoteDivergedError", err, err)
	}
	if diverged.RepoName != "r1" || diverged.Branch != "feature/remote-diverged" || diverged.RemoteOnlyCommits != 2 {
		t.Fatalf("PublishRemoteDivergedError = %+v; want repo r1, branch feature/remote-diverged, 2 remote commits", diverged)
	}
	if diverged.LayerPosition != 1 || diverged.LayerTitle != "Single layer" {
		t.Fatalf("PublishRemoteDivergedError layer = %d (%q); want the stack layer named", diverged.LayerPosition, diverged.LayerTitle)
	}
	if got := countPublisherCalls(pub, "CreatePR"); got != 0 {
		t.Fatalf("CreatePR calls = %d, want 0 after a refused push", got)
	}
}

func TestOrchestrator_Publish_LayerPushRemoteChanged(t *testing.T) {
	repoPath := newPublishReadyBranch(t, "feature/remote-changed")
	testutil.CommitFile(t, repoPath, "change.txt", "change\n", "publish change")
	f := &feature.Feature{
		ID:           "feat-publish-remote-changed",
		Name:         "publish remote changed",
		Slug:         "publish-remote-changed",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Stack:        singleLayerStack(1, "feature/remote-changed"),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/remote-changed", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishErrorFn = func(id, repo string, record errcat.FailureRecord) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return "", &git.RewritePushError{Kind: git.RewritePushRemoteChanged, Branch: branch}
	}
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       newFeatureStore(f),
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	err := o.PublishWithOptions(f.ID, orchestrator.PublishOptions{
		Repos: []string{"r1"},
	})
	var changed *orchestrator.PublishRemoteChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("error = %T %v; want PublishRemoteChangedError", err, err)
	}
	if changed.RepoName != "r1" || changed.Branch != "feature/remote-changed" {
		t.Fatalf("PublishRemoteChangedError = %+v; want repo r1, branch feature/remote-changed", changed)
	}
	if changed.LayerPosition != 1 || changed.LayerTitle != "Single layer" {
		t.Fatalf("PublishRemoteChangedError layer = %d (%q); want the stack layer named", changed.LayerPosition, changed.LayerTitle)
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
		Stack:  singleLayerStack(1, "feature/cool-feature"),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/cool-feature", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}

	var gotTitle, gotBody string
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotTitle, gotBody = title, body
		return "https://github.com/org/r1/pull/1", nil
	}

	pr := newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	if err := o.Publish("feat-pub-desc"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// The title is the layer's roadmap table title; the body is the
	// session's generated body.
	if gotTitle != "Single layer" {
		t.Errorf("CreatePR title = %q, want the layer table title %q", gotTitle, "Single layer")
	}
	if !strings.Contains(gotBody, "Generated body") {
		t.Errorf("CreatePR body = %q, want generated session output", gotBody)
	}
}

func TestOrchestrator_PublishRepo_DescriptionGenerationFailureStoresLayerRecord(t *testing.T) {
	repoPath := newPublishReadyBranch(t, "feature/cool-feature")
	testutil.CommitFile(t, repoPath, "change.txt", "change\n", "publish change")
	f := &feature.Feature{
		ID:     "feat-pub-fallback",
		Name:   "cool-feature",
		Slug:   "cool-feature",
		Status: feature.StatusReviewPassed,
		Models: config.ModelConfig{Planning: "sonnet"},
		Stack:  singleLayerStack(1, "feature/cool-feature"),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/cool-feature", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	var storedRecord errcat.FailureRecord
	var storedRepo string
	lc.SetRepoPublishErrorFn = func(id, repo string, record errcat.FailureRecord) error {
		storedRepo = repo
		storedRecord = record
		return nil
	}
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		t.Fatal("PushLayerBranch called; a failed description session must push nothing")
		return "", nil
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		t.Fatal("CreatePR called; a failed description session creates nothing")
		return "", nil
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

	// The repository owns the condition through its stored record; the
	// record names the layer whose session failed.
	if storedRepo != "r1" {
		t.Errorf("SetRepoPublishError repo = %q, want r1", storedRepo)
	}
	if storedRecord.Code != errcat.PublishDescriptionFailed {
		t.Errorf("stored record code = %q, want publish_description_failed", storedRecord.Code)
	}
	if storedRecord.Context == nil || len(storedRecord.Context.Repositories) != 1 ||
		storedRecord.Context.Repositories[0].Name != "r1" {
		t.Fatalf("stored record repositories = %+v, want r1", storedRecord.Context)
	}
	repo := storedRecord.Context.Repositories[0]
	if repo.LayerPosition != 1 || repo.LayerTitle != "Single layer" {
		t.Errorf("stored record layer = %d (%q), want 1 (Single layer)", repo.LayerPosition, repo.LayerTitle)
	}
	if !strings.Contains(storedRecord.Diagnostics, "generating description") {
		t.Errorf("stored record diagnostics = %q, want the raw generation failure", storedRecord.Diagnostics)
	}
	runDir := agent.ActiveRunDir(pr.StateDir, f)
	if _, statErr := os.Stat(filepath.Join(runDir, "publish", "error.log")); !os.IsNotExist(statErr) {
		t.Errorf("publish error.log exists under %s, want none (the record owns the condition)", runDir)
	}
}

// ---------------------------------------------------------------------------
// DraftPublish — draft flag threaded from feature checkpoints to CreatePR
// ---------------------------------------------------------------------------

func TestOrchestrator_PublishRepo_DraftPublish_True(t *testing.T) {
	repoPath := newPublishReadyBranch(t, "feature/draft-feature")
	testutil.CommitFile(t, repoPath, "change.txt", "change\n", "publish change")
	f := &feature.Feature{
		ID:     "feat-draft-true",
		Name:   "draft-feature",
		Slug:   "draft-feature",
		Status: feature.StatusReviewPassed,
		Stack:  singleLayerStack(1, "feature/draft-feature"),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/draft-feature", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
		Checkpoints: feature.Checkpoints{DraftPublish: true},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}

	var gotDraft bool
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotDraft = draft
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-draft-true", orchestrator.PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !gotDraft {
		t.Error("RemoteOps.CreatePR should receive draft=true when feature Checkpoints.DraftPublish is true")
	}
}

func TestOrchestrator_PublishRepo_DraftPublish_False(t *testing.T) {
	repoPath := newPublishReadyBranch(t, "feature/no-draft-feature")
	testutil.CommitFile(t, repoPath, "change.txt", "change\n", "publish change")
	f := &feature.Feature{
		ID:     "feat-draft-false",
		Name:   "no-draft-feature",
		Slug:   "no-draft-feature",
		Status: feature.StatusReviewPassed,
		Stack:  singleLayerStack(1, "feature/no-draft-feature"),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/no-draft-feature", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
		},
		// DraftPublish defaults to false
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}

	var gotDraft bool
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotDraft = draft
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions("feat-draft-false", orchestrator.PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if gotDraft {
		t.Error("RemoteOps.CreatePR should receive draft=false when feature Checkpoints.DraftPublish is false")
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

// realGitPublishRemoteOps keeps Git push behavior genuinely unmocked while
// replacing GitHub API calls so tests never reach an external service.
type realGitPublishRemoteOps struct {
	createdPRURL string
}

func (realGitPublishRemoteOps) Push(path, branch string) error { return git.Push(path, branch) }
func (realGitPublishRemoteOps) PullRebase(path, branch string) error {
	return git.PullRebase(path, branch).Err
}
func (realGitPublishRemoteOps) PushLayerBranch(path, branch, localSHA, lastPushedSHA string) (string, error) {
	return git.PushLayerBranch(path, branch, localSHA, lastPushedSHA)
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
func (realGitPublishRemoteOps) GetPRBody(prURL string) (string, error) {
	return git.GetPRBody(prURL)
}
func (realGitPublishRemoteOps) UpdatePRBody(prURL, body string) error {
	return git.UpdatePRBody(prURL, body)
}

// The first manual CodeReady publish has no remote pull-request branch yet.
// It still travels through PushLayerBranch, which must create that branch
// with an ordinary push before the orchestrator records the new PR.
func TestOrchestrator_Publish_ManualCodeReadyCreatesAbsentRemoteBranch(t *testing.T) {
	const branch = "feature/manual-first-publish"
	const prURL = "https://github.com/org/r1/pull/1"
	repoPath, bareRemote := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repoPath, branch)
	testutil.CommitFile(t, repoPath, "first.txt", "first\n", "first publish")
	if git.BranchExistsOnRemote(repoPath, branch) {
		t.Fatalf("remote branch %s exists before first publish", branch)
	}

	f := &feature.Feature{
		ID:           "feat-manual-first-publish",
		Name:         "manual first publish",
		Slug:         "manual-first-publish",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Checkpoints:  feature.Checkpoints{ManualPublish: true},
		Stack:        singleLayerStack(1, branch),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branch, BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       newFeatureStore(f),
		Remote:      realGitPublishRemoteOps{createdPRURL: prURL},
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions(f.ID, orchestrator.PublishOptions{
		Repos: []string{"r1"},
	}); err != nil {
		t.Fatalf("PublishWithOptions() error = %v", err)
	}
	if !git.BranchExistsOnRemote(repoPath, branch) {
		t.Fatalf("remote branch %s does not exist after first publish", branch)
	}

	remoteTip := runPublishGitOutput(t, bareRemote, "rev-parse", "refs/heads/"+branch)
	localTip := runPublishGitOutput(t, repoPath, "rev-parse", "HEAD")
	if remoteTip != localTip {
		t.Fatalf("remote branch tip = %s; want published local HEAD %s", remoteTip, localTip)
	}
	published := assertLifecycleCall(t, lc, "SetRepoPublished")
	if published == nil {
		t.FailNow()
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
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
