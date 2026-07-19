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
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
	"gopkg.in/yaml.v3"
)

// apiRepoName is the fixture repo name shared by rebase/cycle tests below.
const apiRepoName = "api"

func newTestManager(t *testing.T) *feature.Manager {
	t.Helper()
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	cfg.Repos["test-repo"] = config.RepoConfig{
		Path: "/tmp/test-repo",
	}
	mgr := feature.NewManager(store, cfg)
	mgr.Branches = newMockBranches(false)
	mgr.PRs = mocks.NewMockPublisher()
	return mgr
}

func newMockBranches(hasRemote bool) *mocks.MockBranchOperator {
	branches := mocks.NewMockBranchOperator()
	branches.DefaultBranchFn = func(repoPath string) (string, error) {
		return "main", nil
	}
	branches.HasOriginRemoteFn = func(repoPath string) (bool, error) {
		return hasRemote, nil
	}
	branches.BranchNameFn = func(featureSlug string) string {
		return git.BranchName(featureSlug)
	}
	return branches
}

func skipShortFeatureRegression(t *testing.T, reason string) {
	t.Helper()
	if testing.Short() {
		t.Skip(reason)
	}
}

func TestManagerCreate(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Test Feature", "A description", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if f.Slug != "test-feature" {
		t.Errorf("slug = %q, want %q", f.Slug, "test-feature")
	}
	if f.Status != feature.StatusCreated {
		t.Errorf("status = %v, want %v", f.Status, feature.StatusCreated)
	}
}

// TestManagerCreateStampsSchemaVersionAndPrePopulatesRun verifies that fresh
// features arrive at Implement with SchemaVersion stamped to the current
// version and the active run pre-populated with one RepoImpl entry per repo,
// so the orchestrator's lazy InitRepoImpl path becomes idempotent for fresh
// features. The per-phase ExecutionPlan is no longer persisted (per
// SchemaVersionCurrent = 3); it is read fresh from disk per orchestrator
// cycle, so this test no longer asserts plan pre-population.
func TestManagerCreateStampsSchemaVersionAndPrePopulatesRun(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	t.Run("single repo", func(t *testing.T) {
		mgr := newTestManager(t)
		f, err := mgr.Create("Single", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if f.SchemaVersion != feature.SchemaVersionCurrent {
			t.Errorf("SchemaVersion = %d, want %d", f.SchemaVersion, feature.SchemaVersionCurrent)
		}
		if got, want := len(f.RepoStates), 1; got != want {
			t.Fatalf("len(RepoStates) = %d, want %d", got, want)
		}
		state, ok := f.RepoStates["test-repo"]
		if !ok || state == nil {
			t.Fatalf("RepoStates[test-repo] missing")
		}
		if state.Touched {
			t.Errorf("RepoStates[test-repo].Touched = true, want false (fresh feature)")
		}
	})

	t.Run("multi repo", func(t *testing.T) {
		dir := t.TempDir()
		store := feature.NewStore(dir)
		cfg := config.NewDefault()
		cfg.Repos["repo-a"] = config.RepoConfig{Path: "/tmp/repo-a"}
		cfg.Repos["repo-b"] = config.RepoConfig{Path: "/tmp/repo-b"}
		mgr := feature.NewManager(store, cfg)
		mgr.Branches = newMockBranches(false)
		mgr.PRs = mocks.NewMockPublisher()

		f, err := mgr.Create("Multi", "desc", []string{"repo-a", "repo-b"}, mgr.Config.Defaults.Models, "", "", nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if f.SchemaVersion != feature.SchemaVersionCurrent {
			t.Errorf("SchemaVersion = %d, want %d", f.SchemaVersion, feature.SchemaVersionCurrent)
		}
		if got, want := len(f.RepoStates), 2; got != want {
			t.Fatalf("len(RepoImpl) = %d, want %d", got, want)
		}
		for _, name := range []string{"repo-a", "repo-b"} {
			state, ok := f.RepoStates[name]
			if !ok || state == nil {
				t.Fatalf("RepoStates[%s] missing", name)
			}
			if state.Touched {
				t.Errorf("RepoStates[%s].Touched = true, want false (fresh feature)", name)
			}
		}
	})
}

func TestManagerLifecycleTransitions(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Lifecycle Test", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Start research
	if err := mgr.StartResearch(f.ID); err != nil {
		t.Fatalf("start research: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusResearching {
		t.Errorf("status = %v, want Researching", f.Status)
	}
	if f.CurrentPhase != feature.PhaseResearch {
		t.Errorf("phase = %v, want Research", f.CurrentPhase)
	}

	// Complete research
	if err := mgr.CompleteResearch(f.ID); err != nil {
		t.Fatalf("complete research: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusDesignReady {
		t.Errorf("status = %v, want DesignReady", f.Status)
	}

	// Start and complete design
	if err := mgr.StartDesign(f.ID); err != nil {
		t.Fatalf("start design: %v", err)
	}
	if err := mgr.CompleteDesign(f.ID); err != nil {
		t.Fatalf("complete design: %v", err)
	}

	// Start planning
	if err := mgr.StartPlanning(f.ID); err != nil {
		t.Fatalf("start planning: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusPlanning {
		t.Errorf("status = %v, want Planning", f.Status)
	}
	if f.CurrentPhase != feature.PhasePlan {
		t.Errorf("phase = %v, want Plan", f.CurrentPhase)
	}

	// Complete planning
	if err := mgr.CompletePlanning(f.ID); err != nil {
		t.Fatalf("complete planning: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusImplementReady {
		t.Errorf("status = %v, want ImplementReady", f.Status)
	}

	// Start implementation
	if err := mgr.StartImplementation(f.ID); err != nil {
		t.Fatalf("start implementation: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusImplementing {
		t.Errorf("status = %v, want Implementing", f.Status)
	}
	if f.CurrentIteration != 1 {
		t.Errorf("iteration = %d, want 1", f.CurrentIteration)
	}
	if f.CurrentPhase != feature.PhaseImplement {
		t.Errorf("phase = %v, want Implement", f.CurrentPhase)
	}
}

func TestManagerUpdateIteration(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, _ := mgr.Create("Iter Test", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	_ = mgr.StartResearch(f.ID)
	_ = mgr.CompleteResearch(f.ID)
	_ = mgr.StartDesign(f.ID)
	_ = mgr.CompleteDesign(f.ID)
	_ = mgr.StartPlanning(f.ID)
	_ = mgr.CompletePlanning(f.ID)
	_ = mgr.StartImplementation(f.ID)

	if err := mgr.UpdateIteration(f.ID, 5); err != nil {
		t.Fatalf("update iteration: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.CurrentIteration != 5 {
		t.Errorf("iteration = %d, want 5", f.CurrentIteration)
	}
}

func TestManagerCreateWithWorktree(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	storeDir := t.TempDir()
	wtDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["test-repo"] = config.RepoConfig{
		Path: "/repos/test-repo",
	}

	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = &mocks.MockWorktreeOperator{
		CreateFn: func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
			return filepath.Join(wtDir, featureSlug, repoName), nil
		},
	}
	mgr.Branches = newMockBranches(false)
	mgr.PRs = mocks.NewMockPublisher()

	f, err := mgr.Create("Worktree Feature", "test worktree", []string{"test-repo"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if len(f.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(f.Repos))
	}

	repo := f.Repos[0]
	if repo.WorktreePath == "" {
		t.Error("expected WorktreePath to be set")
	}
	if repo.Branch == "" {
		t.Error("expected Branch to be set")
	}
	expectedBranch := git.BranchName(f.WorkspaceSlug())
	if repo.Branch != expectedBranch {
		t.Errorf("branch = %q, want %q", repo.Branch, expectedBranch)
	}
	expectedWT := filepath.Join(wtDir, f.WorkspaceSlug(), "test-repo")
	if repo.WorktreePath != expectedWT {
		t.Errorf("worktree path = %q, want %q", repo.WorktreePath, expectedWT)
	}
}

func TestManagerCreateQueuesActiveSetupWithoutWorktreeSideEffects(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	storeDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["test-repo"] = config.RepoConfig{Path: "/repos/test-repo"}

	mgr := feature.NewManager(store, cfg)
	mgr.Branches = newMockBranches(false)
	worktrees := mocks.NewMockWorktreeOperator()
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		return "", fmt.Errorf("worktree side effect should not run while setup is only queued")
	}
	mgr.Worktrees = worktrees
	mgr.PRs = mocks.NewMockPublisher()

	f, err := mgr.Create("Worktree Feature", "test worktree", []string{"test-repo"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if len(worktrees.Calls) != 0 {
		t.Fatalf("worktree create calls = %d, want 0", len(worktrees.Calls))
	}
	if f.Status != feature.StatusSettingUpWorktrees {
		t.Errorf("created status = %v, want %v", f.Status, feature.StatusSettingUpWorktrees)
	}
	features, err := store.List()
	if err != nil {
		t.Fatalf("listing persisted features: %v", err)
	}
	if len(features) != 1 {
		t.Fatalf("persisted features = %d, want 1", len(features))
	}
	persisted := features[0]
	wantBranch := git.BranchName(persisted.WorkspaceSlug())
	if got := persisted.Repos[0].Branch; got != wantBranch {
		t.Fatalf("persisted branch = %q, want %q", got, wantBranch)
	}
	setup := persisted.Run().Setup
	if setup == nil {
		t.Fatal("persisted run setup state is nil")
	}
	if setup.Status != feature.SetupStatusRunning {
		t.Fatalf("persisted setup status = %q, want %q", setup.Status, feature.SetupStatusRunning)
	}
	task, ok := setup.Tasks["worktree:test-repo"]
	if !ok {
		t.Fatal("persisted setup task worktree:test-repo missing")
	}
	if task.Kind != feature.SetupTaskWorktree || task.Status != feature.SetupStatusQueued || task.Branch != wantBranch {
		t.Fatalf("persisted setup task = %+v, want queued worktree task on %s", task, wantBranch)
	}
	if persisted.StartedAt != nil {
		t.Fatal("persisted setup feature started first phase")
	}
}

func TestManagerCreateQueuedSetupDeduplicatesUpstreamBranchBeforeSave(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	storeDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["test-repo"] = config.RepoConfig{Path: "/repos/test-repo"}

	mgr := feature.NewManager(store, cfg)
	branches := newMockBranches(true)
	conflicted := false
	branches.BranchExistsOnRemoteFn = func(repoPath, branch string) (bool, error) {
		if !conflicted && strings.HasPrefix(branch, "feature/upstream-conflict-") {
			conflicted = true
			return true, nil
		}
		return false, nil
	}
	mgr.Branches = branches
	worktrees := mocks.NewMockWorktreeOperator()
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		return "", fmt.Errorf("worktree side effect should not run while setup is only queued")
	}
	mgr.Worktrees = worktrees
	mgr.PRs = mocks.NewMockPublisher()

	f, err := mgr.Create("Upstream Conflict", "test", []string{"test-repo"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if f.Slug == "upstream-conflict" {
		t.Fatal("slug should have been modified to avoid upstream conflict")
	}
	if !strings.HasPrefix(f.Slug, "upstream-conflict-") {
		t.Fatalf("slug = %q, want upstream-conflict-*", f.Slug)
	}
	if len(worktrees.Calls) != 0 {
		t.Fatalf("worktree create calls = %d, want 0", len(worktrees.Calls))
	}

	persisted, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load persisted feature: %v", err)
	}
	wantBranch := git.BranchName(f.WorkspaceSlug())
	if persisted.Repos[0].Branch != wantBranch {
		t.Fatalf("persisted branch = %q, want %q", persisted.Repos[0].Branch, wantBranch)
	}
	task := persisted.Run().Setup.Tasks["worktree:test-repo"]
	if task.Branch != wantBranch {
		t.Fatalf("setup task branch = %q, want %q", task.Branch, wantBranch)
	}
}

func TestManagerRunSetupUsesFeatureIDQualifiedBranchWhenPlainSlugBranchIsCheckedOut(t *testing.T) {
	repoDir := testutil.InitGitRepo(t)
	occupiedBranch := "feature/setup-local-conflict"
	occupiedPath := filepath.Join(t.TempDir(), "occupied")
	cmd := exec.Command("git", "-C", repoDir, "worktree", "add", occupiedPath, "-b", occupiedBranch, "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("occupy plain slug branch: %s: %v", strings.TrimSpace(string(out)), err)
	}

	storeDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["test-repo"] = config.RepoConfig{Path: repoDir}
	mgr := feature.NewManager(store, cfg)
	mgr.Branches = &git.BranchAdapter{}
	mgr.Worktrees = git.NewWorktreeManager(t.TempDir())
	mgr.PRs = mocks.NewMockPublisher()

	f, err := mgr.Create("Setup Local Conflict", "test", []string{"test-repo"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RunSetup(f.ID); err != nil {
		t.Fatalf("run setup: %v", err)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	gotBranch := loaded.Repos[0].Branch
	if gotBranch == occupiedBranch {
		t.Fatalf("branch = %q, want ID-qualified branch distinct from occupied plain slug branch", gotBranch)
	}
	wantBranch := git.BranchName(loaded.WorkspaceSlug())
	if gotBranch != wantBranch {
		t.Fatalf("branch = %q, want %q", gotBranch, wantBranch)
	}
	if _, err := os.Stat(loaded.Repos[0].WorktreePath); err != nil {
		t.Fatalf("qualified worktree path missing: %v", err)
	}
}

func TestManagerRunSetupCompletesQueuedSetupAndCopiesAssets(t *testing.T) {
	storeDir := t.TempDir()
	wtDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: "/repos/repo-a"}
	cfg.Repos["repo-b"] = config.RepoConfig{Path: "/repos/repo-b"}
	mgr := feature.NewManager(store, cfg)
	mgr.Branches = newMockBranches(false)
	worktrees := mocks.NewMockWorktreeOperator()
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		return filepath.Join(wtDir, featureSlug, repoName), nil
	}
	mgr.Worktrees = worktrees
	mgr.PRs = mocks.NewMockPublisher()

	tmpDir := t.TempDir()
	imagePath := filepath.Join(tmpDir, "screenshot.png")
	if err := os.WriteFile(imagePath, []byte("png bytes"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	attachmentPath := filepath.Join(tmpDir, "notes.md")
	if err := os.WriteFile(attachmentPath, []byte("notes bytes"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	f, err := mgr.Create("Setup Complete", "copy assets", []string{"repo-a", "repo-b"}, cfg.Defaults.Models, "", "", []string{imagePath}, feature.CreateOptions{
		QueueSetup:  true,
		Attachments: []string{attachmentPath},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var events []feature.SetupEvent
	if err := mgr.RunSetup(f.ID, feature.SetupRunnerOptions{OnEvent: func(ev feature.SetupEvent) {
		events = append(events, ev)
	}}); err != nil {
		t.Fatalf("run setup: %v", err)
	}

	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Status != feature.StatusCreated {
		t.Fatalf("Status = %s, want Created", loaded.Status)
	}
	setup := loaded.Run().Setup
	if setup == nil || setup.Status != feature.SetupStatusDone {
		t.Fatalf("setup = %+v, want done setup", setup)
	}
	if loaded.ActiveRun != 1 || loaded.RunCount != 1 {
		t.Fatalf("run identity = active %d count %d, want active 1 count 1", loaded.ActiveRun, loaded.RunCount)
	}
	for _, key := range []string{"worktree:repo-a", "worktree:repo-b", "image:1", "attachment:1"} {
		task := setup.Tasks[key]
		if task.Status != feature.SetupStatusDone {
			t.Fatalf("task %s status = %s, want done", key, task.Status)
		}
		if task.Path == "" {
			t.Fatalf("task %s path is empty", key)
		}
	}
	for _, repo := range loaded.Repos {
		wantPath := filepath.Join(wtDir, loaded.WorkspaceSlug(), repo.Name)
		if repo.WorktreePath != wantPath {
			t.Fatalf("%s worktree path = %q, want %q", repo.Name, repo.WorktreePath, wantPath)
		}
		if repo.Branch != git.BranchName(loaded.WorkspaceSlug()) {
			t.Fatalf("%s branch = %q, want %q", repo.Name, repo.Branch, git.BranchName(loaded.WorkspaceSlug()))
		}
	}
	if got, want := len(loaded.Images), 1; got != want {
		t.Fatalf("images = %d, want %d", got, want)
	}
	if data, err := os.ReadFile(loaded.Images[0]); err != nil || string(data) != "png bytes" {
		t.Fatalf("copied image content = %q, err=%v", string(data), err)
	}
	if got, want := len(loaded.Attachments), 1; got != want {
		t.Fatalf("attachments = %d, want %d", got, want)
	}
	if data, err := os.ReadFile(loaded.Attachments[0]); err != nil || string(data) != "notes bytes" {
		t.Fatalf("copied attachment content = %q, err=%v", string(data), err)
	}
	logBytes, err := os.ReadFile(setup.LatestLogPath)
	if err != nil {
		t.Fatalf("read setup log: %v", err)
	}
	logText := string(logBytes)
	wantOrder := []string{"task worktree:repo-a started", "task worktree:repo-b started", "task image:1 started", "task attachment:1 started", "setup attempt 1 completed"}
	last := -1
	for _, marker := range wantOrder {
		idx := strings.Index(logText, marker)
		if idx < 0 {
			t.Fatalf("setup log missing %q:\n%s", marker, logText)
		}
		if idx <= last {
			t.Fatalf("setup log marker %q out of order:\n%s", marker, logText)
		}
		last = idx
	}
	if len(events) == 0 || events[0].Kind != feature.SetupEventStarted || events[len(events)-1].Kind != feature.SetupEventCompleted {
		t.Fatalf("events = %+v, want started...completed", events)
	}
	for _, ev := range events {
		if ev.RunNumber != 1 || ev.Attempt != 1 {
			t.Fatalf("event = %+v, want run 1 attempt 1", ev)
		}
	}
}

func TestManagerRunSetupFailurePersistsDiagnosticsAndLog(t *testing.T) {
	mgr := newTestManager(t)
	worktrees := mocks.NewMockWorktreeOperator()
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		return "", errors.New("git worktree add failed: branch exists")
	}
	mgr.Worktrees = worktrees

	f, err := mgr.Create("Setup Failure", "fail worktree", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	err = mgr.RunSetup(f.ID)
	if err == nil {
		t.Fatal("RunSetup succeeded, want failure")
	}

	loaded, loadErr := mgr.Get(f.ID)
	if loadErr != nil {
		t.Fatalf("get: %v", loadErr)
	}
	if loaded.ID != f.ID || loaded.Slug != f.Slug || loaded.ActiveRun != 1 || loaded.RunCount != 1 {
		t.Fatalf("feature identity changed after failure: id=%q slug=%q active=%d count=%d", loaded.ID, loaded.Slug, loaded.ActiveRun, loaded.RunCount)
	}
	if loaded.Status != feature.StatusFailed || loaded.FailureType != feature.FailureWorktreeSetup {
		t.Fatalf("status/failure = %s/%s, want Failed/%s", loaded.Status, loaded.FailureType, feature.FailureWorktreeSetup)
	}
	if !strings.Contains(loaded.LastError, "git worktree add failed") {
		t.Fatalf("LastError = %q, want worktree diagnostic", loaded.LastError)
	}
	setup := loaded.Run().Setup
	if setup == nil || setup.Status != feature.SetupStatusFailed || setup.LatestLogPath == "" {
		t.Fatalf("setup = %+v, want failed setup with latest log", setup)
	}
	task := setup.Tasks["worktree:test-repo"]
	if task.Status != feature.SetupStatusFailed || !strings.Contains(task.LastError, "git worktree add failed") {
		t.Fatalf("task = %+v, want failed task diagnostic", task)
	}
	logBytes, err := os.ReadFile(setup.LatestLogPath)
	if err != nil {
		t.Fatalf("read setup log: %v", err)
	}
	if logText := string(logBytes); !strings.Contains(logText, "setup failed on worktree:test-repo") || !strings.Contains(logText, "git worktree add failed") {
		t.Fatalf("setup log missing failure diagnostic:\n%s", logText)
	}
}

func TestManagerRunSetupImageFailurePreservesCompletedWorktree(t *testing.T) {
	wtDir := t.TempDir()
	mgr := newTestManager(t)
	mgr.Worktrees = &mocks.MockWorktreeOperator{
		CreateFn: func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
			return filepath.Join(wtDir, featureSlug, repoName), nil
		},
	}

	f, err := mgr.Create("Setup Image Failure", "missing image", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", []string{filepath.Join(t.TempDir(), "missing.png")}, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	err = mgr.RunSetup(f.ID)
	if err == nil {
		t.Fatal("RunSetup succeeded, want missing image failure")
	}

	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Status != feature.StatusFailed || loaded.FailureType != feature.FailureWorktreeSetup {
		t.Fatalf("status/failure = %s/%s, want Failed/%s", loaded.Status, loaded.FailureType, feature.FailureWorktreeSetup)
	}
	setup := loaded.Run().Setup
	if setup.Tasks["worktree:test-repo"].Status != feature.SetupStatusDone {
		t.Fatalf("worktree task = %+v, want completed before image failure", setup.Tasks["worktree:test-repo"])
	}
	if setup.Tasks["image:1"].Status != feature.SetupStatusFailed {
		t.Fatalf("image task = %+v, want failed", setup.Tasks["image:1"])
	}
	if loaded.Repos[0].WorktreePath == "" {
		t.Fatal("canonical worktree path was not preserved after later setup failure")
	}
}

func TestManagerRetrySetupSkipsDoneTasksAndCompletesOriginalRun(t *testing.T) {
	storeDir := t.TempDir()
	wtDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: "/repos/repo-a"}
	cfg.Repos["repo-b"] = config.RepoConfig{Path: "/repos/repo-b"}
	mgr := feature.NewManager(store, cfg)
	mgr.Branches = newMockBranches(false)
	worktrees := mocks.NewMockWorktreeOperator()
	failRepoB := true
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		if repoName == "repo-b" && failRepoB {
			return "", errors.New("repo-b checkout failed")
		}
		return filepath.Join(wtDir, featureSlug, repoName), nil
	}
	mgr.Worktrees = worktrees
	mgr.PRs = mocks.NewMockPublisher()

	f, err := mgr.Create("Setup Retry", "retry missing repo", []string{"repo-a", "repo-b"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RunSetup(f.ID); err == nil {
		t.Fatal("initial RunSetup succeeded, want repo-b failure")
	}
	failed, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	firstLog := failed.Run().Setup.LatestLogPath
	if failed.Run().Setup.Tasks["worktree:repo-a"].Status != feature.SetupStatusDone {
		t.Fatalf("repo-a task = %+v, want done", failed.Run().Setup.Tasks["worktree:repo-a"])
	}

	failRepoB = false
	if err := mgr.RetrySetup(f.ID); err != nil {
		t.Fatalf("retry setup: %v", err)
	}
	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	setup := loaded.Run().Setup
	if loaded.ID != f.ID || loaded.Slug != f.Slug || loaded.ActiveRun != 1 || loaded.RunCount != 1 {
		t.Fatalf("feature/run identity changed after retry: id=%q slug=%q active=%d count=%d", loaded.ID, loaded.Slug, loaded.ActiveRun, loaded.RunCount)
	}
	if loaded.Status != feature.StatusCreated || setup.Status != feature.SetupStatusDone || setup.Attempt != 2 {
		t.Fatalf("status/setup/attempt = %s/%s/%d, want Created/done/2", loaded.Status, setup.Status, setup.Attempt)
	}
	if setup.Tasks["worktree:repo-a"].Attempt != 1 {
		t.Fatalf("repo-a attempt = %d, want preserved attempt 1", setup.Tasks["worktree:repo-a"].Attempt)
	}
	if setup.Tasks["worktree:repo-b"].Attempt != 2 {
		t.Fatalf("repo-b attempt = %d, want retry attempt 2", setup.Tasks["worktree:repo-b"].Attempt)
	}
	if setup.LatestLogPath == firstLog {
		t.Fatal("retry did not create a new latest attempt log")
	}
	if _, err := os.Stat(firstLog); err != nil {
		t.Fatalf("first attempt log not preserved: %v", err)
	}
	var repoACreates int
	for _, call := range worktrees.Calls {
		if call.Method == "Create" && len(call.Args) >= 3 && call.Args[2] == "repo-a" {
			repoACreates++
		}
	}
	if repoACreates != 1 {
		t.Fatalf("repo-a create calls = %d, want 1; calls=%+v", repoACreates, worktrees.Calls)
	}
}

func TestManagerRetrySetupRefusesDuplicateActiveRunner(t *testing.T) {
	mgr := newTestManager(t)
	wtDir := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	mgr.Worktrees = &mocks.MockWorktreeOperator{
		CreateFn: func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
			close(started)
			<-release
			return filepath.Join(wtDir, featureSlug, repoName), nil
		},
	}
	f, err := mgr.Create("Setup Duplicate", "duplicate retry", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- mgr.RunSetup(f.ID) }()
	<-started

	err = mgr.RetrySetup(f.ID)
	if !errors.Is(err, feature.ErrSetupAlreadyRunning) {
		t.Fatalf("RetrySetup while RunSetup active = %v, want ErrSetupAlreadyRunning", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("background RunSetup: %v", err)
	}
}

func TestManagerRetrySetupFailsOnExpectedWorktreeBranchMismatch(t *testing.T) {
	mgr := newTestManager(t)
	conflictPath := filepath.Join(t.TempDir(), "setup-feature", "test-repo")
	if err := os.MkdirAll(conflictPath, 0o755); err != nil {
		t.Fatalf("mkdir conflict path: %v", err)
	}
	worktrees := mocks.NewMockWorktreeOperator()
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		t.Fatalf("Create called despite persisted path conflict")
		return "", nil
	}
	mgr.Worktrees = worktrees
	branches := newMockBranches(false)
	branches.CurrentBranchFn = func(repoPath string) (string, error) {
		return "feature/someone-else", nil
	}
	mgr.Branches = branches
	f, err := mgr.Create("Setup Branch Mismatch", "conflict", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		setup := ff.Run().Setup
		setup.Status = feature.SetupStatusFailed
		task := setup.Tasks["worktree:test-repo"]
		task.Status = feature.SetupStatusFailed
		task.Path = conflictPath
		setup.Tasks[task.Key] = task
		ff.Status = feature.StatusFailed
		ff.FailureType = feature.FailureWorktreeSetup
		return nil
	}); err != nil {
		t.Fatalf("seed failed setup: %v", err)
	}

	err = mgr.RetrySetup(f.ID)
	if err == nil {
		t.Fatal("RetrySetup succeeded, want branch mismatch failure")
	}
	if !strings.Contains(err.Error(), "want") || !strings.Contains(err.Error(), "someone-else") {
		t.Fatalf("RetrySetup error = %v, want branch mismatch diagnostic", err)
	}
	if _, statErr := os.Stat(conflictPath); statErr != nil {
		t.Fatalf("conflict path was removed: %v", statErr)
	}
	for _, call := range worktrees.Calls {
		if call.Method == "Remove" {
			t.Fatalf("unexpected destructive cleanup during retry: %+v", worktrees.Calls)
		}
	}
}

func TestManagerRetrySetupReusesExpectedWorktreeWhenTaskPathWasNotPersisted(t *testing.T) {
	mgr := newTestManager(t)
	expectedBase := t.TempDir()
	expectedPath := filepath.Join(expectedBase, "setup-crash-window", "test-repo")
	if err := os.MkdirAll(expectedPath, 0o755); err != nil {
		t.Fatalf("mkdir expected path: %v", err)
	}
	worktrees := mocks.NewMockWorktreeOperator()
	worktrees.ExpectedPathFn = func(featureSlug, repoName string) string {
		return expectedPath
	}
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		t.Fatalf("Create called even though expected worktree path exists")
		return "", nil
	}
	mgr.Worktrees = worktrees

	var expectedBranch string
	branches := newMockBranches(false)
	branches.CurrentBranchFn = func(repoPath string) (string, error) {
		if repoPath != expectedPath {
			t.Fatalf("CurrentBranch path = %q, want %q", repoPath, expectedPath)
		}
		return expectedBranch, nil
	}
	mgr.Branches = branches

	f, err := mgr.Create("Setup Crash Window", "reattach expected path", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	expectedBranch = git.BranchName(f.WorkspaceSlug())
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		setup := ff.Run().Setup
		setup.Status = feature.SetupStatusFailed
		task := setup.Tasks["worktree:test-repo"]
		task.Status = feature.SetupStatusFailed
		task.Path = ""
		task.LastError = "process exited before setup state saved"
		setup.Tasks[task.Key] = task
		ff.Status = feature.StatusFailed
		ff.FailureType = feature.FailureWorktreeSetup
		ff.LastError = task.LastError
		return nil
	}); err != nil {
		t.Fatalf("seed failed setup: %v", err)
	}

	if err := mgr.RetrySetup(f.ID); err != nil {
		t.Fatalf("retry setup: %v", err)
	}
	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Status != feature.StatusCreated {
		t.Fatalf("Status = %s, want Created", loaded.Status)
	}
	task := loaded.Run().Setup.Tasks["worktree:test-repo"]
	if task.Status != feature.SetupStatusDone || task.Path != expectedPath {
		t.Fatalf("task = %+v, want done with expected path %s", task, expectedPath)
	}
	if loaded.Repos[0].WorktreePath != expectedPath {
		t.Fatalf("canonical worktree = %q, want %q", loaded.Repos[0].WorktreePath, expectedPath)
	}
	for _, call := range worktrees.Calls {
		if call.Method == "Create" {
			t.Fatalf("unexpected Create call while reusing expected path: %+v", worktrees.Calls)
		}
	}
}

func TestManagerDeleteRemovesSetupTaskWorktreePaths(t *testing.T) {
	mgr := newTestManager(t)
	removed := make(map[string]bool)
	mgr.Worktrees = &mocks.MockWorktreeOperator{
		RemoveFn: func(worktreePath string, deleteBranch bool) error {
			if !deleteBranch {
				t.Fatalf("deleteBranch = false for %s, want true", worktreePath)
			}
			removed[worktreePath] = true
			return nil
		},
	}
	f, err := mgr.Create("Setup Delete", "cleanup partial setup", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setupPath := filepath.Join(t.TempDir(), "setup-delete", "test-repo")
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		task := ff.Run().Setup.Tasks["worktree:test-repo"]
		task.Path = setupPath
		task.Status = feature.SetupStatusFailed
		ff.Run().Setup.Tasks[task.Key] = task
		ff.Run().Setup.Status = feature.SetupStatusFailed
		ff.Status = feature.StatusFailed
		ff.FailureType = feature.FailureWorktreeSetup
		return nil
	}); err != nil {
		t.Fatalf("seed setup task path: %v", err)
	}

	if err := mgr.Delete(f.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !removed[setupPath] {
		t.Fatalf("removed paths = %v, want setup task path %s", removed, setupPath)
	}
}

func TestManagerReconcileAbandonedSetupsMarksFailed(t *testing.T) {
	mgr := newTestManager(t)
	f, err := mgr.Create("Setup Reconcile", "stale active setup", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	logPath := filepath.Join(mgr.Store.RunDir(f.ID, 1), "setup", "attempt-01-output.txt")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("partial setup log"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		setup := ff.Run().Setup
		setup.LatestLogPath = logPath
		task := setup.Tasks["worktree:test-repo"]
		task.Status = feature.SetupStatusRunning
		task.Path = "/tmp/worktrees/setup-reconcile/test-repo"
		setup.Tasks[task.Key] = task
		return nil
	}); err != nil {
		t.Fatalf("seed running task: %v", err)
	}

	reconciled, err := mgr.ReconcileAbandonedSetups()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(reconciled) != 1 || reconciled[0] != f.ID {
		t.Fatalf("reconciled = %v, want [%s]", reconciled, f.ID)
	}
	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Status != feature.StatusFailed || loaded.FailureType != feature.FailureWorktreeSetup {
		t.Fatalf("status/failure = %s/%s, want Failed/%s", loaded.Status, loaded.FailureType, feature.FailureWorktreeSetup)
	}
	setup := loaded.Run().Setup
	if setup.Status != feature.SetupStatusFailed || !strings.Contains(setup.LastError, "interrupted by shutdown or crash") {
		t.Fatalf("setup = %+v, want failed crash diagnostic", setup)
	}
	if setup.LatestLogPath != logPath {
		t.Fatalf("latest log = %q, want preserved %q", setup.LatestLogPath, logPath)
	}
	task := setup.Tasks["worktree:test-repo"]
	if task.Status != feature.SetupStatusFailed || task.Path == "" || task.Branch == "" {
		t.Fatalf("task = %+v, want failed task preserving path and branch", task)
	}
}

func TestManagerCreateWithoutWorktree(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	// Without WorktreeManager set, repos should have empty WorktreePath/Branch
	mgr := newTestManager(t)
	f, err := mgr.Create("No WT Feature", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(f.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(f.Repos))
	}
	if f.Repos[0].WorktreePath != "" {
		t.Errorf("expected empty WorktreePath, got %q", f.Repos[0].WorktreePath)
	}
	if f.Repos[0].Branch != "" {
		t.Errorf("expected empty Branch, got %q", f.Repos[0].Branch)
	}
}

func TestManagerPhaseProgression(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Phase Test", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Initial phase should be KnowledgeBase
	if f.CurrentPhase != feature.PhaseKnowledgeBase {
		t.Errorf("initial phase = %v, want Knowledge Base", f.CurrentPhase)
	}

	// StartKnowledgeBase + CompleteKnowledgeBase → ready for research
	if err := mgr.StartKnowledgeBase(f.ID); err != nil {
		t.Fatalf("start KB: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.CurrentPhase != feature.PhaseKnowledgeBase {
		t.Errorf("after StartKnowledgeBase: phase = %v, want Knowledge Base", f.CurrentPhase)
	}
	_ = mgr.CompleteKnowledgeBase(f.ID)

	// StartResearch should set CurrentPhase to Research
	if err := mgr.StartResearch(f.ID); err != nil {
		t.Fatalf("start research: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.CurrentPhase != feature.PhaseResearch {
		t.Errorf("after StartResearch: phase = %v, want Research", f.CurrentPhase)
	}

	// CompleteResearch → StartDesign → CompleteDesign → StartPlanning
	_ = mgr.CompleteResearch(f.ID)
	if err := mgr.StartDesign(f.ID); err != nil {
		t.Fatalf("start design: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.CurrentPhase != feature.PhaseDesign {
		t.Errorf("after StartDesign: phase = %v, want Design", f.CurrentPhase)
	}
	_ = mgr.CompleteDesign(f.ID)
	if err := mgr.StartPlanning(f.ID); err != nil {
		t.Fatalf("start planning: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.CurrentPhase != feature.PhasePlan {
		t.Errorf("after StartPlanning: phase = %v, want Plan", f.CurrentPhase)
	}

	// CompletePlanning + StartImplementation should set CurrentPhase to Implement
	_ = mgr.CompletePlanning(f.ID)
	if err := mgr.StartImplementation(f.ID); err != nil {
		t.Fatalf("start implementation: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.CurrentPhase != feature.PhaseImplement {
		t.Errorf("after StartImplementation: phase = %v, want Implement", f.CurrentPhase)
	}

	// CompleteImplementation + MarkCodeReady should set CurrentPhase to Publish
	_ = mgr.CompleteImplementation(f.ID)
	if err := mgr.MarkCodeReady(f.ID); err != nil {
		t.Fatalf("mark pr ready: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.CurrentPhase != feature.PhasePublish {
		t.Errorf("after MarkCodeReady: phase = %v, want Publish", f.CurrentPhase)
	}

	// MarkPublished should keep CurrentPhase at Publish
	if err := mgr.MarkPublished(f.ID, "https://github.com/test/pr/1"); err != nil {
		t.Fatalf("mark published: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.CurrentPhase != feature.PhasePublish {
		t.Errorf("after MarkPublished: phase = %v, want Publish", f.CurrentPhase)
	}
}

func TestMarkFinalReviewReadyTracksReviewRuntime(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Final Review Timing", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusReviewPassed
		ff.CurrentPhase = feature.PhaseImplement
		return nil
	}); err != nil {
		t.Fatalf("seed review passed: %v", err)
	}

	if err := mgr.MarkFinalReviewReady(f.ID); err != nil {
		t.Fatalf("MarkFinalReviewReady: %v", err)
	}
	got, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ActiveTimingKey != "review" {
		t.Fatalf("ActiveTimingKey = %q, want review", got.ActiveTimingKey)
	}
	if got.ActivePhaseStart == nil {
		t.Fatal("ActivePhaseStart = nil, want final review timer started")
	}

	past := time.Now().Add(-4 * time.Minute)
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.ActivePhaseStart = &past
		return nil
	}); err != nil {
		t.Fatalf("backdate ActivePhaseStart: %v", err)
	}
	if err := mgr.Transition(f.ID, feature.StatusReviewPassed); err != nil {
		t.Fatalf("finish final review: %v", err)
	}
	got, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get after finish: %v", err)
	}
	if got.ActivePhaseStart != nil {
		t.Fatal("ActivePhaseStart should be nil after leaving final review")
	}
	if d := got.PhaseRuntime("review"); d < 4*time.Minute {
		t.Fatalf("PhaseRuntime(review) = %v, want at least 4m", d)
	}
}

func TestManagerMarkPublished(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, _ := mgr.Create("Publish Test", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)

	// The test config has no origin remote, so Create initialises repos as
	// non-publishable. Force publishable=true to exercise the empty-URL guard.
	makePublishable(t, mgr, f.ID)

	// Advance through full lifecycle to PRReady
	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)
	_ = mgr.Transition(f.ID, feature.StatusCodeReady)

	// Mark published — empty URL is rejected for publishable features
	if err := mgr.MarkPublished(f.ID, ""); err == nil {
		t.Fatalf("MarkPublished with empty URL on publishable feature should fail")
	}

	// Publishable + non-empty URL succeeds
	if err := mgr.MarkPublished(f.ID, "https://github.com/test/pr/1"); err != nil {
		t.Fatalf("mark published: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusPublished {
		t.Errorf("status = %v, want Published", f.Status)
	}
	if f.PRURL() != "https://github.com/test/pr/1" {
		t.Errorf("PRURL = %q, want PR URL preserved", f.PRURL())
	}
}

// makePublishable is a test helper that marks all repos of a feature as
// publishable. Necessary because newTestManager's Config has no origin remote
// so Create initialises repos as non-publishable.
func makePublishable(t *testing.T, mgr *feature.Manager, featureID string) {
	t.Helper()
	err := mgr.Store.Modify(featureID, func(ff *feature.Feature) error {
		yes := true
		for i := range ff.Repos {
			ff.Repos[i].Publishable = &yes
		}
		return nil
	})
	if err != nil {
		t.Fatalf("makePublishable: %v", err)
	}
}

func TestManagerMarkFailed(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, _ := mgr.Create("Fail Test", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)

	if err := mgr.MarkFailed(f.ID, feature.FailureSafetyRail, "test error"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusFailed {
		t.Errorf("status = %v, want Failed", f.Status)
	}
	if f.FailureType != feature.FailureSafetyRail {
		t.Errorf("failure type = %q, want %q", f.FailureType, feature.FailureSafetyRail)
	}
	if f.LastError != "test error" {
		t.Errorf("last error = %q, want %q", f.LastError, "test error")
	}
}

func TestManagerMarkCodeReadyRefusesTerminalFailure(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Failed Final Review", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusFailed
		ff.CurrentPhase = feature.PhaseFinalReview
		ff.LastError = "protocol violation: final_review_reviewer @ /tmp/iter: invalid report"
		ff.FailureType = feature.FailureProtocolViolation
		return nil
	}); err != nil {
		t.Fatalf("seed failed final review: %v", err)
	}

	if err := mgr.MarkCodeReady(f.ID); err == nil {
		t.Fatal("MarkCodeReady succeeded, want terminal failure guard")
	}
	got, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != feature.StatusFailed {
		t.Fatalf("Status = %s, want Failed", got.Status)
	}
	if got.CurrentPhase != feature.PhaseFinalReview {
		t.Fatalf("CurrentPhase = %s, want FinalReview", got.CurrentPhase)
	}
	if got.FailureType != feature.FailureProtocolViolation {
		t.Fatalf("FailureType = %q, want %q", got.FailureType, feature.FailureProtocolViolation)
	}
}

func TestManagerCreateWithImages(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)

	// Create temp image files
	tmpDir := t.TempDir()
	img1 := filepath.Join(tmpDir, "screenshot1.png")
	img2 := filepath.Join(tmpDir, "screenshot2.png")
	if err := os.WriteFile(img1, []byte("fake png 1"), 0o644); err != nil {
		t.Fatalf("write temp image: %v", err)
	}
	if err := os.WriteFile(img2, []byte("fake png 2"), 0o644); err != nil {
		t.Fatalf("write temp image: %v", err)
	}

	f, err := mgr.Create("Image Feature", "has images", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", []string{img1, img2})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Verify images were copied
	if len(f.Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(f.Images))
	}

	for i, imgPath := range f.Images {
		data, err := os.ReadFile(imgPath)
		if err != nil {
			t.Fatalf("reading copied image %d: %v", i+1, err)
		}
		expected := fmt.Sprintf("fake png %d", i+1)
		if string(data) != expected {
			t.Errorf("image %d content = %q, want %q", i+1, string(data), expected)
		}
	}

	// Verify feature round-trips through YAML with images
	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if len(loaded.Images) != 2 {
		t.Errorf("loaded images count = %d, want 2", len(loaded.Images))
	}
}

// TestManagerStartRebaseFromReviewPassed verifies that StartRebase works when the
// feature is in ReviewPassed status (e.g., pull-rebase conflict during review-comments
// after CompleteImplementation has already marked the feature ReviewPassed).
func TestManagerReturnToPublished(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, _ := mgr.Create("Return Published Test", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)

	// Advance to PRReady
	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)
	_ = mgr.Transition(f.ID, feature.StatusCodeReady)

	// First publish
	_ = mgr.MarkPublished(f.ID, "https://github.com/test/pr/1")

	// Start a rebase-like cycle by direct transition (StartRebase is no longer available).
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)
	_ = mgr.Transition(f.ID, feature.StatusCodeReady)

	// Return to published
	if err := mgr.ReturnToPublished(f.ID); err != nil {
		t.Fatalf("return to published: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusPublished {
		t.Errorf("status = %v, want Published", f.Status)
	}
	if f.CurrentPhase != feature.PhasePublish {
		t.Errorf("phase = %v, want Publish", f.CurrentPhase)
	}
	// PR URL should still be preserved
	if f.PRURL() != "https://github.com/test/pr/1" {
		t.Errorf("PR URL = %q, want preserved", f.PRURL())
	}
}

// TestMarkPublished_NonPublishableEmptyURLAccepted confirms the guard
// only applies to publishable features; non-publishable features may
// legitimately Transition to StatusPublished with no PR URL.
func TestMarkPublished_NonPublishableEmptyURLAccepted(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, _ := mgr.Create("NoRemote Test", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)

	// Flip the repo to non-publishable.
	_ = mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		no := false
		for i := range ff.Repos {
			ff.Repos[i].Publishable = &no
		}
		return nil
	})

	// Advance through lifecycle.
	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)
	_ = mgr.Transition(f.ID, feature.StatusCodeReady)

	if err := mgr.MarkPublished(f.ID, ""); err != nil {
		t.Fatalf("non-publishable MarkPublished with empty URL should succeed: %v", err)
	}
	got, _ := mgr.Get(f.ID)
	if got.Status != feature.StatusPublished {
		t.Errorf("status = %v, want Published", got.Status)
	}
	if got.PRURL() != "" {
		t.Errorf("PRURL = %q, want empty", got.PRURL())
	}
}

// TestReturnToPublished_PublishableNoURLRejected ensures the guard blocks the
// transition when a publishable feature has no PR URL anywhere. Without the
// guard, a rebase-cycle completion on a first-publish-conflict path could land
// the feature at StatusPublished with no PR.
func TestReturnToPublished_PublishableNoURLRejected(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, _ := mgr.Create("Return Published No URL", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	makePublishable(t, mgr, f.ID)

	// Walk into StatusCodeReady without ever calling MarkPublished, so PRURL
	// stays empty at both feature and per-repo level.
	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)
	_ = mgr.Transition(f.ID, feature.StatusCodeReady)

	if err := mgr.ReturnToPublished(f.ID); err == nil {
		t.Fatal("ReturnToPublished on publishable feature with no PR URL must fail")
	}
	got, _ := mgr.Get(f.ID)
	if got.Status == feature.StatusPublished {
		t.Errorf("feature transitioned to Published without a PR URL: status = %v", got.Status)
	}
}

// TestReturnToPublished_RepoLevelURLAccepted confirms a per-repo PR URL
// satisfies the guard even when the top-level f.PRURL is empty (the normal
// state after MarkPublished because FirstRepoPRURL is the per-repo slot).
func TestReturnToPublished_RepoLevelURLAccepted(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, _ := mgr.Create("Return Published Repo URL", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	makePublishable(t, mgr, f.ID)

	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)
	_ = mgr.Transition(f.ID, feature.StatusCodeReady)

	// Seed a per-repo PR URL (simulates publishRepo's SetRepoPublished) but
	// leave f.PRURL empty.
	_ = mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		if ff.RepoStates == nil {
			ff.RepoStates = map[string]*feature.RepoState{}
		}
		ff.RepoStates["test-repo"] = &feature.RepoState{
			Touched: true, PRURL: "https://github.com/test/pr/42",
		}
		return nil
	})
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)
	_ = mgr.Transition(f.ID, feature.StatusCodeReady)

	if err := mgr.ReturnToPublished(f.ID); err != nil {
		t.Fatalf("ReturnToPublished with per-repo PR URL must succeed: %v", err)
	}
	got, _ := mgr.Get(f.ID)
	if got.Status != feature.StatusPublished {
		t.Errorf("status = %v, want Published", got.Status)
	}
}

// TestReturnToPublished_NonPublishableNoURL preserves the non-publishable
// branch: no PR is ever expected, so the guard does not fire.
func TestReturnToPublished_NonPublishableNoURL(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, _ := mgr.Create("NoRemote Return", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)

	_ = mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		no := false
		for i := range ff.Repos {
			ff.Repos[i].Publishable = &no
		}
		return nil
	})

	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)
	_ = mgr.Transition(f.ID, feature.StatusCodeReady)

	if err := mgr.ReturnToPublished(f.ID); err != nil {
		t.Fatalf("non-publishable ReturnToPublished must succeed with no URL: %v", err)
	}
}

func TestManagerRecreateWorktree(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	storeDir := t.TempDir()
	wtDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["test-repo"] = config.RepoConfig{
		Path: "/repos/test-repo",
	}
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = &mocks.MockWorktreeOperator{
		CreateFn: func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
			return filepath.Join(wtDir, featureSlug, repoName), nil
		},
	}
	mgr.Branches = newMockBranches(false)
	mgr.PRs = mocks.NewMockPublisher()

	// Create feature with worktree
	f, err := mgr.Create("Recreate WT Test", "test", []string{"test-repo"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if f.Repos[0].WorktreePath == "" {
		t.Fatal("expected worktree path after create")
	}

	originalWT := f.Repos[0].WorktreePath

	// Clean the worktree
	if err := mgr.CleanWorktree(f.ID); err != nil {
		t.Fatalf("clean worktree: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Repos[0].WorktreePath != "" {
		t.Errorf("expected empty worktree path after clean, got %q", f.Repos[0].WorktreePath)
	}

	// Recreate
	if err := mgr.RecreateWorktree(f.ID); err != nil {
		t.Fatalf("recreate worktree: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Repos[0].WorktreePath == "" {
		t.Error("expected worktree path after recreate")
	}
	if f.Repos[0].WorktreePath != originalWT {
		t.Errorf("recreated path = %q, want %q", f.Repos[0].WorktreePath, originalWT)
	}
}

func TestManagerFullRebaseCycle(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, _ := mgr.Create("Full Rebase Cycle", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)

	// Full lifecycle to Published
	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)
	_ = mgr.Transition(f.ID, feature.StatusCodeReady)
	_ = mgr.MarkPublished(f.ID, "https://github.com/test/pr/1")

	// Rebase cycle: Published -> ImplementReady -> Implementing -> ReviewPassed -> PRReady -> Published.
	// Manager.StartRebase is no longer available; emulate the transition directly.
	_ = mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		if err := ff.Transition(feature.StatusImplementReady); err != nil {
			return err
		}
		ff.SetRebaseCount(ff.RebaseCount() + 1)
		ff.SetActiveCycleType(feature.CycleRebase)
		ff.CurrentPhase = feature.PhaseImplement
		return nil
	})
	_ = mgr.StartImplementation(f.ID)

	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusImplementing {
		t.Fatalf("status = %v, want Implementing", f.Status)
	}

	_ = mgr.CompleteImplementation(f.ID)
	_ = mgr.MarkCodeReady(f.ID)
	_ = mgr.ReturnToPublished(f.ID)

	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusPublished {
		t.Errorf("status = %v, want Published", f.Status)
	}
	if f.RebaseCount() != 1 {
		t.Errorf("rebase count = %d, want 1", f.RebaseCount())
	}

	// Can still go to Done
	if err := mgr.Transition(f.ID, feature.StatusDone); err != nil {
		t.Fatalf("transition to done: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusDone {
		t.Errorf("status = %v, want Done", f.Status)
	}
}

func TestManagerCreateWithAutoPublish(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)

	// Create with AutoPublish enabled
	f, err := mgr.Create("Auto Publish Feature", "test auto-publish", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil, feature.CreateOptions{Checkpoints: feature.Checkpoints{ManualPublish: false}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !f.Checkpoints.AutoPublish() {
		t.Error("expected AutoPublish to be true")
	}

	// Verify it persists through YAML round-trip
	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if !loaded.Checkpoints.AutoPublish() {
		t.Error("expected AutoPublish to be true after reload")
	}
}

func TestManagerCreateWithoutAutoPublish(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)

	// Create without AutoPublish (default)
	f, err := mgr.Create("Manual Publish Feature", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if f.Checkpoints.ManualPublish {
		t.Error("expected ManualPublish to be false by default")
	}
}

func TestManagerCreateNoImages(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("No Image Feature", "no images", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(f.Images) != 0 {
		t.Errorf("expected 0 images, got %d", len(f.Images))
	}

	// Verify YAML doesn't contain images key (omitempty)
	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if len(loaded.Images) != 0 {
		t.Errorf("loaded images = %v, want empty", loaded.Images)
	}
}

func TestManagerCreateWithAttachments(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)

	// Create temp attachment files
	tmpDir := t.TempDir()
	doc1 := filepath.Join(tmpDir, "spec.pdf")
	doc2 := filepath.Join(tmpDir, "notes.txt")
	os.WriteFile(doc1, []byte("fake pdf content"), 0o644)
	os.WriteFile(doc2, []byte("some notes"), 0o644)

	opts := feature.CreateOptions{Attachments: []string{doc1, doc2}}
	f, err := mgr.Create("Attach Feature", "has attachments", []string{"test-repo"},
		mgr.Config.Defaults.Models, "", "", nil, opts)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if len(f.Attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(f.Attachments))
	}

	// Verify files were copied with original names
	for i, path := range f.Attachments {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading attachment %d: %v", i+1, err)
		}
		if len(data) == 0 {
			t.Errorf("attachment %d is empty", i+1)
		}
		if !strings.Contains(path, "attachments/") {
			t.Errorf("attachment %d path %q does not contain 'attachments/'", i+1, path)
		}
	}

	// Verify round-trip through YAML
	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if len(loaded.Attachments) != 2 {
		t.Errorf("loaded attachments count = %d, want 2", len(loaded.Attachments))
	}
}

func TestManagerCreateNoAttachments(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("No Attach", "no attachments", []string{"test-repo"},
		mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(f.Attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(f.Attachments))
	}
}

func TestManagerCreateDeduplicatesUpstreamBranch(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	storeDir := t.TempDir()
	wtDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["test-repo"] = config.RepoConfig{Path: "/repos/test-repo"}

	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = &mocks.MockWorktreeOperator{
		CreateFn: func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
			return filepath.Join(wtDir, featureSlug, repoName), nil
		},
	}
	branches := newMockBranches(true)
	conflicted := false
	branches.BranchExistsOnRemoteFn = func(repoPath, branch string) (bool, error) {
		if !conflicted && strings.HasPrefix(branch, "feature/upstream-conflict-") {
			conflicted = true
			return true, nil
		}
		return false, nil
	}
	mgr.Branches = branches
	mgr.PRs = mocks.NewMockPublisher()

	f, err := mgr.Create("Upstream Conflict", "test", []string{"test-repo"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if f.Slug == "upstream-conflict" {
		t.Error("slug should have been modified to avoid upstream conflict")
	}
	if !strings.HasPrefix(f.Slug, "upstream-conflict-") {
		t.Errorf("slug should start with 'upstream-conflict-', got %q", f.Slug)
	}
	suffix := strings.TrimPrefix(f.Slug, "upstream-conflict-")
	if len(suffix) != 4 {
		t.Errorf("suffix length = %d, want 4 hex chars", len(suffix))
	}

	expectedBranch := git.BranchName(f.WorkspaceSlug())
	if f.Repos[0].Branch != expectedBranch {
		t.Errorf("branch = %q, want %q", f.Repos[0].Branch, expectedBranch)
	}
}

func TestManagerCreateNoSuffixWhenNoUpstreamConflict(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	storeDir := t.TempDir()
	wtDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["test-repo"] = config.RepoConfig{Path: "/repos/test-repo"}

	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = &mocks.MockWorktreeOperator{
		CreateFn: func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
			return filepath.Join(wtDir, featureSlug, repoName), nil
		},
	}
	mgr.Branches = newMockBranches(true)
	mgr.PRs = mocks.NewMockPublisher()

	f, err := mgr.Create("No Conflict Feature", "test", []string{"test-repo"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if f.Slug != "no-conflict-feature" {
		t.Errorf("slug = %q, want %q (no suffix expected)", f.Slug, "no-conflict-feature")
	}
}

func TestSlugExists(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)

	// Create a feature
	_, err := mgr.Create("My Feature", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	t.Run("matching slug returns name", func(t *testing.T) {
		name, err := mgr.SlugExists("my-feature")
		if err != nil {
			t.Fatalf("SlugExists: %v", err)
		}
		if name != "My Feature" {
			t.Errorf("SlugExists = %q, want %q", name, "My Feature")
		}
	})

	t.Run("non-matching slug returns empty", func(t *testing.T) {
		name, err := mgr.SlugExists("other-feature")
		if err != nil {
			t.Fatalf("SlugExists: %v", err)
		}
		if name != "" {
			t.Errorf("SlugExists = %q, want empty", name)
		}
	})
}

func TestCreateDuplicateSlug(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)

	_, err := mgr.Create("Test Feature", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	t.Run("exact duplicate name", func(t *testing.T) {
		_, err := mgr.Create("Test Feature", "desc2", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
		if err == nil {
			t.Fatal("expected error for duplicate slug, got nil")
		}
		if !errors.Is(err, feature.ErrDuplicateSlug) {
			t.Errorf("expected ErrDuplicateSlug, got: %v", err)
		}
	})

	t.Run("same slug different casing", func(t *testing.T) {
		_, err := mgr.Create("test feature", "desc3", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
		if err == nil {
			t.Fatal("expected error for duplicate slug, got nil")
		}
		if !errors.Is(err, feature.ErrDuplicateSlug) {
			t.Errorf("expected ErrDuplicateSlug, got: %v", err)
		}
	})

	t.Run("unique name succeeds", func(t *testing.T) {
		_, err := mgr.Create("Unique Feature", "desc4", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
		if err != nil {
			t.Fatalf("create unique: %v", err)
		}
	})
}

func TestRewindToPhase_StatusMapping(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	tests := []struct {
		name        string
		targetPhase feature.Phase
		wantStatus  feature.Status
		wantPhase   feature.Phase
	}{
		{"rewind to inquire", feature.PhaseInquire, feature.StatusPromptNeedsReview, feature.PhaseKnowledgeBase},
		{"rewind to research", feature.PhaseResearch, feature.StatusInquiryNeedsReview, feature.PhaseInquire},
		{"rewind to design", feature.PhaseDesign, feature.StatusResearchNeedsReview, feature.PhaseResearch},
		{"rewind to plan", feature.PhasePlan, feature.StatusDesignNeedsReview, feature.PhaseDesign},
		{"rewind to implement", feature.PhaseImplement, feature.StatusPlanNeedsReview, feature.PhasePlan},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if testing.Short() && tt.name != "rewind to research" {
				t.Skip("extended rewind status matrix; short mode keeps one representative target")
			}
			mgr := newTestManager(t)
			f, err := mgr.Create("Rewind Test", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			// Advance to Implementing via direct status set
			_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
				f.Status = feature.StatusImplementing
				f.CurrentPhase = feature.PhaseImplement
				return nil
			})
			_, _, err = mgr.RewindToPhase(f.ID, tt.targetPhase)
			if err != nil {
				t.Fatalf("RewindToPhase: %v", err)
			}
			got, _ := mgr.Get(f.ID)
			if got.Status != tt.wantStatus {
				t.Errorf("status = %v, want %v", got.Status, tt.wantStatus)
			}
			if got.CurrentPhase != tt.wantPhase {
				t.Errorf("phase = %v, want %v", got.CurrentPhase, tt.wantPhase)
			}
			if got.PendingReviewPhase == nil {
				t.Error("PendingReviewPhase should be set after rewind")
			} else if *got.PendingReviewPhase != tt.targetPhase {
				t.Errorf("PendingReviewPhase = %v, want %v", *got.PendingReviewPhase, tt.targetPhase)
			}
		})
	}
}

// TestRewindToPhase_ArtifactsPreservedInSealedRun verifies the Phase 2
// seal+fork semantics: rewind DOES NOT destroy artifacts. All phase dirs
// from the active run live on under `runs/run-001/`, and `runs/run-002/`
// receives a deep copy of each phase dir that `carryForwardDirs` lists for
// the target. Rewinding to PhaseResearch carries only `inquire/` forward.
func TestRewindToPhase_ArtifactsPreservedInSealedRun(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind artifact-preservation variant; canonical carry-forward remains in short mode")
	mgr := newTestManager(t)
	f, err := mgr.Create("Artifact Cleanup", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		return nil
	})

	// Seed phase directories under run-001 (the active run).
	baseDir := mgr.Store.BaseDir
	run1Dir := filepath.Join(baseDir, f.ID, "runs", "run-001")
	for _, phase := range []feature.Phase{feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign, feature.PhasePlan, feature.PhaseImplement} {
		dir := filepath.Join(run1Dir, phase.DirName())
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", phase.DirName(), err)
		}
		os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("test"), 0o644)
	}

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhaseResearch); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}

	// Post-rewind: the sealed run-001 keeps ALL its phase dirs intact.
	for _, phase := range []feature.Phase{feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign, feature.PhasePlan, feature.PhaseImplement} {
		p := filepath.Join(run1Dir, phase.DirName(), "marker.txt")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("sealed run-001/%s/marker.txt missing: %v (seal+fork must not destroy artifacts)", phase.DirName(), err)
		}
	}
	// run-002 exists. For target=PhaseResearch, only `inquire/` is carried.
	run2Dir := filepath.Join(baseDir, f.ID, "runs", "run-002")
	if _, err := os.Stat(filepath.Join(run2Dir, "run.yaml")); err != nil {
		t.Fatalf("run-002/run.yaml missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(run2Dir, "inquire", "marker.txt")); err != nil {
		t.Errorf("run-002/inquire/marker.txt should exist (carried for PhaseResearch target): %v", err)
	}
	for _, phase := range []feature.Phase{feature.PhaseResearch, feature.PhaseDesign, feature.PhasePlan, feature.PhaseImplement} {
		if _, err := os.Stat(filepath.Join(run2Dir, phase.DirName())); !os.IsNotExist(err) {
			t.Errorf("run-002/%s should not exist (not carried for PhaseResearch target)", phase.DirName())
		}
	}
	// Feature metadata reflects the new active run.
	got, _ := mgr.Get(f.ID)
	if got.ActiveRun != 2 || got.RunCount != 2 {
		t.Errorf("ActiveRun/RunCount = %d/%d, want 2/2", got.ActiveRun, got.RunCount)
	}
}

// TestRewindToPhase_ArtifactMap_ForwardCarriesCorrectSubset_SealedPreserved
// verifies that after seal+fork, the new run's Artifacts map carries
// exactly the key subset that the target permits (per the carry-forward
// matrix), while the sealed run retains its full map on disk.
func TestRewindToPhase_ArtifactMap_ForwardCarriesCorrectSubset_SealedPreserved(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind artifact-map subset variant; canonical carry-forward remains in short mode")
	mgr := newTestManager(t)
	f, err := mgr.Create("Artifact Map", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.Artifacts = map[string]string{
			"inquire":   "/path/to/inquire.md",
			"research":  "/path/to/research.md",
			"design":    "/path/to/design.md",
			"plan":      "/path/to/plan.md",
			"implement": "/path/to/impl.md",
		}
		return nil
	})

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhaseDesign); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	got, _ := mgr.Get(f.ID)
	// New active run (run-002) carries exactly {inquire, research} per the matrix.
	wantKeys := map[string]bool{"inquire": true, "research": true}
	if len(got.Artifacts) != len(wantKeys) {
		t.Errorf("new run Artifacts len = %d, want %d: %v", len(got.Artifacts), len(wantKeys), got.Artifacts)
	}
	for k := range wantKeys {
		if _, ok := got.Artifacts[k]; !ok {
			t.Errorf("new run Artifacts missing carried key %q", k)
		}
	}
	for _, disallowed := range []string{"design", "plan", "implement"} {
		if _, ok := got.Artifacts[disallowed]; ok {
			t.Errorf("new run Artifacts should NOT contain %q (not carried for PhaseDesign)", disallowed)
		}
	}
	// Sealed run-001 retains its full Artifacts map on disk.
	sealedRun, loadErr := mgr.Store.LoadRun(f.ID, 1)
	if loadErr != nil {
		t.Fatalf("LoadRun(1): %v", loadErr)
	}
	if sealedRun.SealedAt == nil {
		t.Fatalf("run-001 should be sealed")
	}
	for _, key := range []string{"inquire", "research", "design", "plan", "implement"} {
		if _, ok := sealedRun.Artifacts[key]; !ok {
			t.Errorf("sealed run-001 lost artifact %q (must be preserved)", key)
		}
	}
}

// TestRewindToPhase_TimingsCostsCarriedForKeptPhases_Run001Preserved verifies
// that seal+fork carries timings/costs of the phases whose outputs survive the
// rewind, drops the ledger entries for redone phases, and keeps the sealed
// run's full history intact for provenance.
func TestRewindToPhase_TimingsCostsCarriedForKeptPhases_Run001Preserved(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind timing/cost provenance variant")
	mgr := newTestManager(t)
	f, err := mgr.Create("Timing Clear", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.PhaseTimings = map[string]time.Duration{
			"inquire":   1 * time.Minute,
			"research":  2 * time.Minute,
			"plan":      3 * time.Minute,
			"implement": 4 * time.Minute,
			"rebase-1":  5 * time.Minute,
		}
		f.PhaseCosts = map[string]float64{
			"inquire":   0.10,
			"research":  0.20,
			"plan":      0.30,
			"implement": 0.40,
		}
		return nil
	})

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	got, _ := mgr.Get(f.ID)
	// Rewind to PhasePlan keeps inquire/research outputs, so their ledger
	// entries carry; plan/implement/rebase-1 are redone and start at zero.
	wantTimings := map[string]time.Duration{"inquire": 1 * time.Minute, "research": 2 * time.Minute}
	if !maps.Equal(got.PhaseTimings, wantTimings) {
		t.Errorf("new run PhaseTimings = %v, want %v", got.PhaseTimings, wantTimings)
	}
	wantCosts := map[string]float64{"inquire": 0.10, "research": 0.20}
	if !maps.Equal(got.PhaseCosts, wantCosts) {
		t.Errorf("new run PhaseCosts = %v, want %v", got.PhaseCosts, wantCosts)
	}
	// Sealed run-001 retains ALL timings/costs as immutable history.
	sealed, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	for _, key := range []string{"inquire", "research", "plan", "implement", "rebase-1"} {
		if _, ok := sealed.PhaseTimings[key]; !ok {
			t.Errorf("sealed run-001 lost timing %q (must be preserved)", key)
		}
	}
	for _, key := range []string{"inquire", "research", "plan", "implement"} {
		if _, ok := sealed.PhaseCosts[key]; !ok {
			t.Errorf("sealed run-001 lost cost %q (must be preserved)", key)
		}
	}
}

func TestRewindToPhase_PRURLCleared(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind PR-close regression; lower-level PR URL clearing remains covered elsewhere")
	mgr := newTestManager(t)
	prs := mocks.NewMockPublisher()
	prs.ClosePRFn = func(prURL string) error {
		return errors.New("close failed")
	}
	mgr.PRs = prs
	f, err := mgr.Create("PR Clear", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusPublished
		f.CurrentPhase = feature.PhasePublish
		// Per-repo PR URL is the only source of truth post-cutover.
		if f.RepoStates == nil {
			f.RepoStates = map[string]*feature.RepoState{}
		}
		f.RepoStates["test-repo"] = &feature.RepoState{
			Touched: true, PRURL: "https://github.com/org/repo/pull/123",
		}
		// Ensure the feature is publishable so ClosePR is attempted
		for i := range f.Repos {
			f.Repos[i].Publishable = nil
		}
		return nil
	})

	warns, _, err := mgr.RewindToPhase(f.ID, feature.PhaseResearch)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	// Should have a warning about PR close failure
	foundPRWarn := false
	for _, w := range warns {
		if strings.Contains(w, "failed to close PR") {
			foundPRWarn = true
			break
		}
	}
	if !foundPRWarn {
		t.Errorf("expected warning about PR close failure, got warnings: %v", warns)
	}
	got, _ := mgr.Get(f.ID)
	if got.PRURL() != "" {
		t.Errorf("PRURL should be cleared, got %q", got.PRURL())
	}
}

func TestRewindToPhase_BackupBranchWarning(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind backup-branch warning regression")
	mgr := newTestManager(t)
	branches := newMockBranches(false)
	branches.CreateBackupBranchFn = func(worktreePath, slug string) (string, error) {
		return "", errors.New("backup failed")
	}
	mgr.Branches = branches
	f, err := mgr.Create("Backup Warn", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.Repos = []feature.FeatureRepo{{
			Path:         "/tmp/test-repo",
			WorktreePath: "/nonexistent/worktree/path",
			BaseBranch:   "main",
		}}
		return nil
	})

	warns, _, err := mgr.RewindToPhase(f.ID, feature.PhaseResearch)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	// Should have a warning about backup branch failure
	foundBackupWarn := false
	for _, w := range warns {
		if strings.Contains(w, "failed to create backup branch") {
			foundBackupWarn = true
			break
		}
	}
	if !foundBackupWarn {
		t.Errorf("expected warning about backup branch failure, got warnings: %v", warns)
	}
	// Strengthen: sealed run records an empty BackupBranches map because the
	// sole repo failed. Only successful branch names survive into the map.
	sealedRun, loadErr := mgr.Store.LoadRun(f.ID, 1)
	if loadErr != nil {
		t.Fatalf("LoadRun(1): %v", loadErr)
	}
	if len(sealedRun.BackupBranches) != 0 {
		t.Errorf("BackupBranches should be empty when all repos fail, got %v", sealedRun.BackupBranches)
	}
}

func TestRewindablePhases(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	tests := []struct {
		name       string
		status     feature.Status
		phase      feature.Phase
		wantPhases []feature.Phase
	}{
		{
			name:       "created has no rewind targets",
			status:     feature.StatusCreated,
			phase:      feature.PhaseKnowledgeBase,
			wantPhases: nil,
		},
		{
			name:       "inquiring allows rewind to inquire",
			status:     feature.StatusInquiring,
			phase:      feature.PhaseInquire,
			wantPhases: []feature.Phase{feature.PhaseInquire},
		},
		{
			name:       "researching allows inquire and research",
			status:     feature.StatusResearching,
			phase:      feature.PhaseResearch,
			wantPhases: []feature.Phase{feature.PhaseInquire, feature.PhaseResearch},
		},
		{
			name:       "design review allows through design",
			status:     feature.StatusDesignNeedsReview,
			phase:      feature.PhaseDesign,
			wantPhases: []feature.Phase{feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign},
		},
		{
			name:       "implementing allows all phases",
			status:     feature.StatusImplementing,
			phase:      feature.PhaseImplement,
			wantPhases: []feature.Phase{feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign, feature.PhasePlan, feature.PhaseImplement},
		},
		{
			name:       "done allows all phases",
			status:     feature.StatusDone,
			phase:      feature.PhasePublish,
			wantPhases: []feature.Phase{feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign, feature.PhasePlan, feature.PhaseImplement},
		},
		{
			name:       "published allows all phases",
			status:     feature.StatusPublished,
			phase:      feature.PhasePublish,
			wantPhases: []feature.Phase{feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign, feature.PhasePlan, feature.PhaseImplement},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{
				Status:       tt.status,
				CurrentPhase: tt.phase,
			}
			got := feature.RewindablePhases(f)
			if len(got) != len(tt.wantPhases) {
				t.Fatalf("RewindablePhases returned %d phases, want %d: %v", len(got), len(tt.wantPhases), got)
			}
			for i, p := range got {
				if p != tt.wantPhases[i] {
					t.Errorf("phase[%d] = %v, want %v", i, p, tt.wantPhases[i])
				}
			}
		})
	}
}

func TestRewindToPhase_FieldsZeroed(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind field-reset matrix")
	mgr := newTestManager(t)
	f, err := mgr.Create("Fields Zero", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Now()
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.CurrentIteration = 5
		f.PlanIteration = 3
		f.MaxPlanIterations = 10
		f.ValidatingPlan = true
		f.ReviewingGate = true
		f.SetRebaseCount(2)
		f.LastError = "some error"
		f.FailureType = "test"
		f.ActivePhaseStart = &now
		f.ActiveTimingKey = "implement"
		f.SetAddressingReviews(true)
		return nil
	})

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	got, _ := mgr.Get(f.ID)
	if got.CurrentIteration != 0 {
		t.Errorf("CurrentIteration = %d, want 0", got.CurrentIteration)
	}
	if got.PlanIteration != 0 {
		t.Errorf("PlanIteration = %d, want 0", got.PlanIteration)
	}
	if got.ValidatingPlan {
		t.Error("ValidatingPlan should be false")
	}
	if got.ReviewingGate {
		t.Error("ReviewingGate should be false")
	}
	if got.RebaseCount() != 0 {
		t.Errorf("RebaseCount = %d, want 0", got.RebaseCount())
	}
	if got.LastError != "" {
		t.Errorf("LastError = %q, want empty", got.LastError)
	}
	if got.FailureType != "" {
		t.Errorf("FailureType = %q, want empty", got.FailureType)
	}
	if got.ActivePhaseStart != nil {
		t.Error("ActivePhaseStart should be nil")
	}
	if got.ActiveTimingKey != "" {
		t.Errorf("ActiveTimingKey = %q, want empty", got.ActiveTimingKey)
	}
	if got.AddressingReviews() {
		t.Error("AddressingReviews should be false")
	}
}

func TestRewindToPhase_InvalidTargetFromCurrentState(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	tests := []struct {
		name       string
		status     feature.Status
		phase      feature.Phase
		target     feature.Phase
		wantErrSub string
	}{
		{
			name:       "cannot rewind to Implement from StatusCreated",
			status:     feature.StatusCreated,
			phase:      feature.PhaseKnowledgeBase,
			target:     feature.PhaseImplement,
			wantErrSub: "cannot rewind",
		},
		{
			name:       "cannot rewind to Plan from StatusInquiring",
			status:     feature.StatusInquiring,
			phase:      feature.PhaseInquire,
			target:     feature.PhasePlan,
			wantErrSub: "cannot rewind",
		},
		{
			name:       "cannot rewind to KB phase",
			status:     feature.StatusImplementing,
			phase:      feature.PhaseImplement,
			target:     feature.PhaseKnowledgeBase,
			wantErrSub: "cannot rewind to knowledge base",
		},
		{
			name:       "cannot rewind to Design from StatusDesignReady",
			status:     feature.StatusDesignReady,
			phase:      feature.PhaseResearch,
			target:     feature.PhaseDesign, // Design hasn't completed yet, so not rewindable
			wantErrSub: "cannot rewind",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newTestManager(t)
			f, err := mgr.Create("Test", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
				f.Status = tc.status
				f.CurrentPhase = tc.phase
				return nil
			})

			_, _, err = mgr.RewindToPhase(f.ID, tc.target)
			if tc.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrSub)
				}
				if !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Errorf("error should contain %q, got: %v", tc.wantErrSub, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestAdvanceRoadmapPhase(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Roadmap Test", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get to ReviewPassed so AdvanceRoadmapPhase can transition to Planning
	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)

	// Set roadmap context and pre-populate RepoImpl. ExecutionPlan was
	// removed in SchemaVersionCurrent = 3 — the per-phase plan is read fresh
	// from disk per orchestrator cycle.
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.TotalRoadmapPhases = 3
		f.RepoStates = map[string]*feature.RepoState{"repo-a": {}}
		return nil
	})

	// Advance to phase 1
	if err := mgr.AdvanceRoadmapPhase(f.ID); err != nil {
		t.Fatalf("AdvanceRoadmapPhase: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusPlanning {
		t.Errorf("status = %v, want Planning", f.Status)
	}
	if f.CurrentRoadmapPhase != 1 {
		t.Errorf("CurrentRoadmapPhase = %d, want 1", f.CurrentRoadmapPhase)
	}
	if f.RoadmapPhaseType != "tracer-bullet" {
		t.Errorf("RoadmapPhaseType = %q, want %q", f.RoadmapPhaseType, "tracer-bullet")
	}
	// RepoImpl now persists across phases; the pre-populated entry should
	// still be present after AdvanceRoadmapPhase.
	if len(f.RepoStates) != 1 {
		t.Errorf("RepoImpl entry count = %d, want 1 (RepoImpl persists across phases)", len(f.RepoStates))
	}
	if f.CurrentPhase != feature.PhasePlan {
		t.Errorf("CurrentPhase = %v, want Plan", f.CurrentPhase)
	}
	if f.CurrentIteration != 0 {
		t.Errorf("CurrentIteration = %d, want 0", f.CurrentIteration)
	}
	if f.ActivePhaseStart == nil {
		t.Error("expected ActivePhaseStart to be set")
	}
	if f.ActiveTimingKey != "phase-1-plan" {
		t.Errorf("ActiveTimingKey = %q, want %q", f.ActiveTimingKey, "phase-1-plan")
	}
}

func TestAdvanceRoadmapPhaseTDDFillIn(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("TDD Test", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get to ReviewPassed with phase 1 already set
	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)

	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.TotalRoadmapPhases = 3
		f.CurrentRoadmapPhase = 1
		f.RoadmapPhaseType = "tracer-bullet"
		return nil
	})

	if err := mgr.AdvanceRoadmapPhase(f.ID); err != nil {
		t.Fatalf("AdvanceRoadmapPhase: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.CurrentRoadmapPhase != 2 {
		t.Errorf("CurrentRoadmapPhase = %d, want 2", f.CurrentRoadmapPhase)
	}
	if f.RoadmapPhaseType != "tdd-fill-in" {
		t.Errorf("RoadmapPhaseType = %q, want %q", f.RoadmapPhaseType, "tdd-fill-in")
	}
}

func TestAdvanceRoadmapPhaseCollapsed(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Collapsed Test", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)

	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.TotalRoadmapPhases = 1
		return nil
	})

	if err := mgr.AdvanceRoadmapPhase(f.ID); err != nil {
		t.Fatalf("AdvanceRoadmapPhase: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.RoadmapPhaseType != "collapsed" {
		t.Errorf("RoadmapPhaseType = %q, want %q", f.RoadmapPhaseType, "collapsed")
	}
}

func TestStartRoadmapPhaseImplementation(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Phase Impl Test", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)

	if err := mgr.StartRoadmapPhaseImplementation(f.ID); err != nil {
		t.Fatalf("StartRoadmapPhaseImplementation: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusImplementReady {
		t.Errorf("status = %v, want ImplementReady", f.Status)
	}
}

func TestCompleteRoadmap(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Complete Roadmap", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)

	if err := mgr.CompleteRoadmap(f.ID); err != nil {
		t.Fatalf("CompleteRoadmap: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusCodeReady {
		t.Errorf("status = %v, want PRReady", f.Status)
	}
	if f.CurrentPhase != feature.PhasePublish {
		t.Errorf("CurrentPhase = %v, want Publish", f.CurrentPhase)
	}
}

func TestRestartFromBeginningClearsRoadmapFields(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended restart rewind field-reset regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("Reset Roadmap", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Under the new seal+fork restart path, the feature must be in a
	// rewindable state (not StatusCreated) for RewindToPhase to accept it.
	// Parallel the legacy test's intent by placing it at StatusImplementing
	// with roadmap fields populated.
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.CurrentRoadmapPhase = 3
		f.TotalRoadmapPhases = 5
		f.RoadmapPhaseType = "tdd-fill-in"
		return nil
	})

	if err := mgr.RestartFromBeginning(f.ID); err != nil {
		t.Fatalf("RestartFromBeginning: %v", err)
	}

	f, _ = mgr.Get(f.ID)
	// New active run starts fresh — roadmap fields are cleared.
	if f.CurrentRoadmapPhase != 0 {
		t.Errorf("CurrentRoadmapPhase = %d, want 0", f.CurrentRoadmapPhase)
	}
	if f.TotalRoadmapPhases != 0 {
		t.Errorf("TotalRoadmapPhases = %d, want 0", f.TotalRoadmapPhases)
	}
	if f.RoadmapPhaseType != "" {
		t.Errorf("RoadmapPhaseType = %q, want empty", f.RoadmapPhaseType)
	}
	if !f.ForceKBRebuild {
		t.Error("ForceKBRebuild should be set after RestartFromBeginning")
	}
	if f.ActiveRun != 2 || f.RunCount != 2 {
		t.Errorf("ActiveRun/RunCount = %d/%d, want 2/2", f.ActiveRun, f.RunCount)
	}
}

// TestRoadmapPhaseTimingKeyTransitions verifies that timing keys transition correctly
// through AdvanceRoadmapPhase → StartRoadmapPhaseImplementation → StartImplementation.
func TestRoadmapPhaseTimingKeyTransitions(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Timing Keys", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get to ReviewPassed so AdvanceRoadmapPhase can transition to Planning
	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)

	// Set roadmap context
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.TotalRoadmapPhases = 3
		return nil
	})

	// Step 1: AdvanceRoadmapPhase → sets ActiveTimingKey = "phase-1-plan"
	if err := mgr.AdvanceRoadmapPhase(f.ID); err != nil {
		t.Fatalf("AdvanceRoadmapPhase: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.ActiveTimingKey != "phase-1-plan" {
		t.Errorf("after AdvanceRoadmapPhase: ActiveTimingKey = %q, want %q", f.ActiveTimingKey, "phase-1-plan")
	}
	if f.ActivePhaseStart == nil {
		t.Error("after AdvanceRoadmapPhase: ActivePhaseStart should be non-nil")
	}

	// Step 2: StartRoadmapPhaseImplementation → transitions to ImplementReady,
	// which accumulates plan timing via the Transition path.
	if err := mgr.StartRoadmapPhaseImplementation(f.ID); err != nil {
		t.Fatalf("StartRoadmapPhaseImplementation: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusImplementReady {
		t.Errorf("after StartRoadmapPhaseImplementation: status = %v, want ImplementReady", f.Status)
	}

	// Step 3: StartImplementation → sets ActiveTimingKey = "phase-1-impl" (NOT "phase-1-plan")
	if err := mgr.StartImplementation(f.ID); err != nil {
		t.Fatalf("StartImplementation: %v", err)
	}
	f, _ = mgr.Get(f.ID)

	// Verify the plan timing was accumulated
	planDuration := f.PhaseTimings["phase-1-plan"]
	if planDuration == 0 {
		t.Error("PhaseTimings[\"phase-1-plan\"] should have a non-zero duration")
	}

	// Verify ActiveTimingKey switched to impl
	if f.ActiveTimingKey != "phase-1-impl" {
		t.Errorf("after StartImplementation: ActiveTimingKey = %q, want %q", f.ActiveTimingKey, "phase-1-impl")
	}
}

// TestRoadmapPhasePlanTimingSurvivesInterruptRestart verifies that when a
// roadmap-phase plan is interrupted and the user restarts it, the resumed
// plan time accumulates into phase-N-plan rather than collapsing into the
// strategic "plan" bucket. Regression for a stopwatch bug where
// StartPlanning's guard only protected refactor-N keys, so phase-2-plan
// was silently overwritten with "plan" on restart.
func TestRoadmapPhasePlanTimingSurvivesInterruptRestart(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Plan Restart", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Walk to ReviewPassed so AdvanceRoadmapPhase can transition into Planning.
	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.TotalRoadmapPhases = 3
		return nil
	})

	// Advance into phase 2 by stepping through phase 1 (advance + impl + review).
	if err := mgr.AdvanceRoadmapPhase(f.ID); err != nil {
		t.Fatalf("AdvanceRoadmapPhase(1): %v", err)
	}
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)
	if err := mgr.AdvanceRoadmapPhase(f.ID); err != nil {
		t.Fatalf("AdvanceRoadmapPhase(2): %v", err)
	}

	f, _ = mgr.Get(f.ID)
	if f.ActiveTimingKey != "phase-2-plan" {
		t.Fatalf("after AdvanceRoadmapPhase(2): ActiveTimingKey = %q, want %q", f.ActiveTimingKey, "phase-2-plan")
	}
	// Backdate ActivePhaseStart so we can observe a non-zero accumulation
	// without sleeping.
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		past := time.Now().Add(-2 * time.Minute)
		f.ActivePhaseStart = &past
		return nil
	})

	// Simulate an interrupt: leaves StatusPlanning → StatusInterrupted, which
	// triggers Transition's auto-accumulate path and credits ~2m to phase-2-plan.
	if err := mgr.Transition(f.ID, feature.StatusInterrupted); err != nil {
		t.Fatalf("Transition(Interrupted): %v", err)
	}
	f, _ = mgr.Get(f.ID)
	firstAttempt := f.PhaseTimings["phase-2-plan"]
	if firstAttempt < time.Minute {
		t.Fatalf("phase-2-plan after first interrupt = %v, want >= 1m", firstAttempt)
	}
	if got := f.PhaseTimings["plan"]; got != 0 {
		t.Fatalf("plan bucket should be empty after first interrupt, got %v", got)
	}

	// Simulate the user restart path. RestartPhase routes interrupted Plan
	// back through StartPhase → StartPlanning. Skip the orchestrator wiring
	// here and call StartPlanning directly, which is the path under test.
	if err := mgr.StartPlanning(f.ID); err != nil {
		t.Fatalf("StartPlanning after interrupt: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.ActiveTimingKey != "phase-2-plan" {
		t.Fatalf("after StartPlanning restart: ActiveTimingKey = %q, want %q (regression: bucket collapsed into strategic plan)", f.ActiveTimingKey, "phase-2-plan")
	}

	// Backdate again and accumulate a second slice via another interrupt to
	// confirm the bucket continues growing under phase-2-plan, not "plan".
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		past := time.Now().Add(-3 * time.Minute)
		f.ActivePhaseStart = &past
		return nil
	})
	if err := mgr.Transition(f.ID, feature.StatusInterrupted); err != nil {
		t.Fatalf("Transition(Interrupted) #2: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if got := f.PhaseTimings["plan"]; got != 0 {
		t.Errorf("plan bucket should remain empty after restart accumulation, got %v", got)
	}
	if got := f.PhaseTimings["phase-2-plan"]; got <= firstAttempt {
		t.Errorf("phase-2-plan = %v, want > first attempt %v (restart did not credit the right bucket)", got, firstAttempt)
	}
}

func TestInitRepoImpl(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := &feature.Store{BaseDir: dir}
	mgr := &feature.Manager{
		Store:  store,
		Config: &config.Config{},
	}

	f := &feature.Feature{
		ID:   "test-init-repo-impl",
		Name: "Test",
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/a"},
			{Name: "repo-b", Path: "/tmp/b"},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := mgr.InitRepoImpl(f.ID); err != nil {
		t.Fatalf("InitRepoImpl: %v", err)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(loaded.RepoStates) != 2 {
		t.Fatalf("expected 2 repo impl entries, got %d", len(loaded.RepoStates))
	}
	for _, name := range []string{"repo-a", "repo-b"} {
		state, ok := loaded.RepoStates[name]
		if !ok {
			t.Errorf("missing repo impl entry for %q", name)
			continue
		}
		if state.Touched {
			t.Errorf("repo %q: Touched = true, want false", name)
		}
	}
}

// newMultiRepoFeature is a helper that creates a feature with multiple repos
// directly via the Store, bypassing worktree creation. Mirrors Manager.Create's
// SchemaVersion=2 stamp so the feature behaves as a fresh per-repo-shape
// fixture (not legacy single-repo).
func newMultiRepoFeature(t *testing.T, mgr *feature.Manager, repos []feature.FeatureRepo) *feature.Feature {
	t.Helper()
	f := &feature.Feature{
		ID:            feature.GenerateIDForTest(),
		Name:          "Multi Repo Test",
		Slug:          "multi-repo-test",
		Repos:         repos,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := mgr.Store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	return f
}

func TestInitKBStatus(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	repos := []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
		{Name: "repo-b", Path: "/tmp/b"},
		{Name: "repo-c", Path: "/tmp/c"},
	}
	f := newMultiRepoFeature(t, mgr, repos)

	if err := mgr.InitKBStatus(f.ID); err != nil {
		t.Fatalf("InitKBStatus: %v", err)
	}

	loaded, err := mgr.Store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(loaded.KBStatus) != 3 {
		t.Fatalf("expected 3 KBStatus entries, got %d", len(loaded.KBStatus))
	}
	for _, repo := range repos {
		status, ok := loaded.KBStatus[repo.Name]
		if !ok {
			t.Errorf("missing KBStatus entry for %q", repo.Name)
			continue
		}
		if status != "pending" {
			t.Errorf("KBStatus[%q] = %q, want %q", repo.Name, status, "pending")
		}
	}
}

func TestMarkRepoKBCompleted(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	repos := []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
		{Name: "repo-b", Path: "/tmp/b"},
	}
	f := newMultiRepoFeature(t, mgr, repos)

	// Initialize KB status first
	if err := mgr.InitKBStatus(f.ID); err != nil {
		t.Fatalf("InitKBStatus: %v", err)
	}

	t.Run("mark single repo completed", func(t *testing.T) {
		if err := mgr.MarkRepoKBCompleted(f.ID, "repo-a"); err != nil {
			t.Fatalf("MarkRepoKBCompleted: %v", err)
		}
		loaded, err := mgr.Store.Load(f.ID)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if loaded.KBStatus["repo-a"] != "completed" {
			t.Errorf("repo-a KBStatus = %q, want %q", loaded.KBStatus["repo-a"], "completed")
		}
		if loaded.KBStatus["repo-b"] != "pending" {
			t.Errorf("repo-b KBStatus = %q, want %q (should be unchanged)", loaded.KBStatus["repo-b"], "pending")
		}
	})

	t.Run("mark all repos completed", func(t *testing.T) {
		if err := mgr.MarkRepoKBCompleted(f.ID, "repo-b"); err != nil {
			t.Fatalf("MarkRepoKBCompleted: %v", err)
		}
		allDone, err := mgr.AllKBsCompleted(f.ID)
		if err != nil {
			t.Fatalf("AllKBsCompleted: %v", err)
		}
		if !allDone {
			t.Error("expected AllKBsCompleted to return true after marking all repos completed")
		}
	})

	t.Run("mark nonexistent repo does not panic", func(t *testing.T) {
		if err := mgr.MarkRepoKBCompleted(f.ID, "nonexistent-repo"); err != nil {
			t.Fatalf("MarkRepoKBCompleted for nonexistent repo: %v", err)
		}
		loaded, err := mgr.Store.Load(f.ID)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if loaded.KBStatus["nonexistent-repo"] != "completed" {
			t.Errorf("nonexistent-repo KBStatus = %q, want %q", loaded.KBStatus["nonexistent-repo"], "completed")
		}
	})
}

func TestMarkRepoKBFailed(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	repos := []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
		{Name: "repo-b", Path: "/tmp/b"},
	}
	f := newMultiRepoFeature(t, mgr, repos)

	if err := mgr.InitKBStatus(f.ID); err != nil {
		t.Fatalf("InitKBStatus: %v", err)
	}

	if err := mgr.MarkRepoKBFailed(f.ID, "repo-a", "timeout exceeded"); err != nil {
		t.Fatalf("MarkRepoKBFailed: %v", err)
	}

	loaded, err := mgr.Store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.KBStatus["repo-a"] != "failed: timeout exceeded" {
		t.Errorf("repo-a KBStatus = %q, want %q", loaded.KBStatus["repo-a"], "failed: timeout exceeded")
	}
	if loaded.KBStatus["repo-b"] != "pending" {
		t.Errorf("repo-b KBStatus = %q, want %q (should be unchanged)", loaded.KBStatus["repo-b"], "pending")
	}
}

func TestAllKBsCompleted(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	tests := []struct {
		name     string
		kbStatus map[string]string
		want     bool
	}{
		{
			name:     "nil map returns true (backward compat)",
			kbStatus: nil,
			want:     true,
		},
		{
			name:     "empty map returns true (backward compat)",
			kbStatus: map[string]string{},
			want:     true,
		},
		{
			name: "all completed returns true",
			kbStatus: map[string]string{
				"repo-a": "completed",
				"repo-b": "completed",
			},
			want: true,
		},
		{
			name: "mix of completed and pending returns false",
			kbStatus: map[string]string{
				"repo-a": "completed",
				"repo-b": "pending",
			},
			want: false,
		},
		{
			name: "any failed returns false",
			kbStatus: map[string]string{
				"repo-a": "completed",
				"repo-b": "failed: some error",
			},
			want: false,
		},
		{
			name: "any building returns false",
			kbStatus: map[string]string{
				"repo-a": "completed",
				"repo-b": "building",
			},
			want: false,
		},
		{
			name: "all pending returns false",
			kbStatus: map[string]string{
				"repo-a": "pending",
				"repo-b": "pending",
			},
			want: false,
		},
		{
			name: "single repo completed returns true",
			kbStatus: map[string]string{
				"repo-a": "completed",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			store := feature.NewStore(dir)
			mgr := &feature.Manager{Store: store, Config: &config.Config{}}

			f := &feature.Feature{
				ID:            feature.GenerateIDForTest(),
				Name:          "AllKBs Test",
				KBStatus:      tt.kbStatus,
				SchemaVersion: feature.SchemaVersionCurrent,
			}
			if err := store.Save(f); err != nil {
				t.Fatalf("save: %v", err)
			}

			got, err := mgr.AllKBsCompleted(f.ID)
			if err != nil {
				t.Fatalf("AllKBsCompleted: %v", err)
			}
			if got != tt.want {
				t.Errorf("AllKBsCompleted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetRepoPublished(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
		{Name: "repo-b", Path: "/tmp/b"},
	})

	// Initialize RepoImpl
	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.RepoStates = map[string]*feature.RepoState{
			"repo-a": {Touched: true},
			"repo-b": {Touched: true},
		}
		return nil
	})

	err := mgr.SetRepoPublished(f.ID, "repo-a", "https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatalf("SetRepoPublished: %v", err)
	}

	loaded, _ := store.Load(f.ID)
	if !loaded.RepoStates["repo-a"].Touched {
		t.Errorf("repo-a Touched = false, want true")
	}
	if loaded.RepoStates["repo-a"].PRURL != "https://github.com/org/repo/pull/1" {
		t.Errorf("repo-a PRURL = %q, want %q", loaded.RepoStates["repo-a"].PRURL, "https://github.com/org/repo/pull/1")
	}
	// repo-b should be unchanged
	if !loaded.RepoStates["repo-b"].Touched {
		t.Errorf("repo-b Touched = false, want true (unchanged)")
	}
}

func TestSetRepoPublishError(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
	})

	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.RepoStates = map[string]*feature.RepoState{
			"repo-a": {Touched: true},
		}
		return nil
	})

	err := mgr.SetRepoPublishError(f.ID, "repo-a", "push failed")
	if err != nil {
		t.Fatalf("SetRepoPublishError: %v", err)
	}

	loaded, _ := store.Load(f.ID)
	if loaded.RepoStates["repo-a"].LastError != "push failed" {
		t.Errorf("LastError = %q, want %q", loaded.RepoStates["repo-a"].LastError, "push failed")
	}
	if !loaded.RepoStates["repo-a"].Touched {
		t.Errorf("Touched changed to false, should stay true (publish error must not regress Touched)")
	}
}

// TestSetRepoPublished_ClearsLastError verifies that calling SetRepoPublished
// after a SetRepoPublishError clears the LastError field.
func TestSetRepoPublished_ClearsLastError(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
	})

	// Initialize RepoImpl
	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.RepoStates = map[string]*feature.RepoState{
			"repo-a": {Touched: true},
		}
		return nil
	})

	// Set a publish error first
	err := mgr.SetRepoPublishError(f.ID, "repo-a", "push failed")
	if err != nil {
		t.Fatalf("SetRepoPublishError: %v", err)
	}

	// Verify error is set
	loaded, _ := store.Load(f.ID)
	if loaded.RepoStates["repo-a"].LastError != "push failed" {
		t.Fatalf("LastError = %q, want %q", loaded.RepoStates["repo-a"].LastError, "push failed")
	}

	// Now publish successfully — should clear LastError
	err = mgr.SetRepoPublished(f.ID, "repo-a", "https://github.com/org/repo/pull/1")
	if err != nil {
		t.Fatalf("SetRepoPublished: %v", err)
	}

	loaded, _ = store.Load(f.ID)
	if loaded.RepoStates["repo-a"].LastError != "" {
		t.Errorf("LastError = %q, want empty string (should be cleared after successful publish)", loaded.RepoStates["repo-a"].LastError)
	}
	if loaded.RepoStates["repo-a"].PRURL != "https://github.com/org/repo/pull/1" {
		t.Errorf("PRURL = %q, want %q", loaded.RepoStates["repo-a"].PRURL, "https://github.com/org/repo/pull/1")
	}
	if !loaded.RepoStates["repo-a"].Touched {
		t.Errorf("Touched = false, want true after publish")
	}
}

func TestTryCompletePublish_AllReady(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
		{Name: "repo-b", Path: "/tmp/b"},
	})

	// Set up: feature at ReviewPassed, all repos at pr_ready with PR URLs
	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusReviewPassed
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoStates = map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/org/a/pull/1"},
			"repo-b": {Touched: true, PRURL: "https://github.com/org/b/pull/2"},
		}
		return nil
	})

	published, err := mgr.TryCompletePublish(f.ID)
	if err != nil {
		t.Fatalf("TryCompletePublish: %v", err)
	}
	if !published {
		t.Fatal("expected published = true")
	}

	loaded, _ := store.Load(f.ID)
	if loaded.Status != feature.StatusPublished {
		t.Errorf("status = %v, want %v", loaded.Status, feature.StatusPublished)
	}
	if loaded.PRURL() != "https://github.com/org/a/pull/1" {
		t.Errorf("PRURL = %q, want first repo's PR URL", loaded.PRURL())
	}
}

func TestTryCompletePublish_NotAllReady(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
		{Name: "repo-b", Path: "/tmp/b"},
	})

	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusReviewPassed
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoStates = map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/org/a/pull/1"},
			"repo-b": {Touched: true},
		}
		// Mirror the strangler-implant dual-write: repo-b is touched but
		// has no PR URL yet, so AllReposPublished must return false.
		feat.RepoStates = map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/org/a/pull/1"},
			"repo-b": {Touched: true},
		}
		return nil
	})

	published, err := mgr.TryCompletePublish(f.ID)
	if err != nil {
		t.Fatalf("TryCompletePublish: %v", err)
	}
	if published {
		t.Fatal("expected published = false when not all repos are PR ready")
	}

	loaded, _ := store.Load(f.ID)
	if loaded.Status != feature.StatusReviewPassed {
		t.Errorf("status = %v, should remain %v", loaded.Status, feature.StatusReviewPassed)
	}
}

func TestTryCompletePublish_FeatureAtPRReady(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
	})

	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusCodeReady
		feat.CurrentPhase = feature.PhasePublish
		feat.RepoStates = map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/org/a/pull/1"},
		}
		return nil
	})

	published, err := mgr.TryCompletePublish(f.ID)
	if err != nil {
		t.Fatalf("TryCompletePublish: %v", err)
	}
	if !published {
		t.Fatal("expected published = true for feature at PRReady")
	}

	loaded, _ := store.Load(f.ID)
	if loaded.Status != feature.StatusPublished {
		t.Errorf("status = %v, want %v", loaded.Status, feature.StatusPublished)
	}
}

func TestTryCompletePublish_WrongStatus(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
	})

	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusImplementing
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoStates = map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/org/a/pull/1"},
		}
		return nil
	})

	published, err := mgr.TryCompletePublish(f.ID)
	if err != nil {
		t.Fatalf("TryCompletePublish: %v", err)
	}
	if published {
		t.Fatal("expected published = false for feature at Implementing")
	}

	loaded, _ := store.Load(f.ID)
	if loaded.Status != feature.StatusImplementing {
		t.Errorf("status = %v, should remain %v", loaded.Status, feature.StatusImplementing)
	}
}

func TestRewindToPhase_ClearsRepoImpl(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind repo-state clearing regression")
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
		{Name: "repo-b", Path: "/tmp/b"},
	})

	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusImplementing
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoStates = map[string]*feature.RepoState{
			"repo-a": {},
			"repo-b": {},
		}
		return nil
	})

	// Rewind to Plan — past implement — should clear RepoImpl.
	_, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	got, _ := mgr.Get(f.ID)
	if got.RepoStates != nil {
		t.Errorf("RepoImpl should be nil after rewind past implement, got %v", got.RepoStates)
	}
}

// TestRewindToPhase_MediumPlanWritesDescriptionReview regression-guards
// the Medium-rewind-to-Plan fix. Medium pipelines have no inquire /
// research / design phases, so rewinding to Plan (the first phase of
// the Medium pipeline) previously left the feature with no artifact the
// rewind-review session could display — the TUI aborted with "no artifact
// found for the previous phase" and the user could not attach to review.
//
// Treating PhasePlan as "first phase of the pipeline" for Medium mirrors
// the PhaseInquire case for Large/Moonshot: RewindToPhase writes
// description-review.md so startRewindReviewSessionCmd has something to
// show, and the orchestrator reads it back on proceed to overwrite
// f.Description (symmetric with rewind-to-inquire).
func TestRewindToPhase_MediumPlanWritesDescriptionReview(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind description-review file regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("Medium Feature", "original description", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Pipeline = feature.PipelineMedium
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		return nil
	})

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}

	descPath := filepath.Join(mgr.Store.BaseDir, f.ID, "description-review.md")
	data, readErr := os.ReadFile(descPath)
	if readErr != nil {
		t.Fatalf("description-review.md not written for Medium rewind-to-Plan: %v", readErr)
	}
	if string(data) != "original description" {
		t.Errorf("description-review.md contents = %q, want %q", string(data), "original description")
	}
}

// TestRewindToPhase_StandardPlanDoesNotWriteDescriptionReview ensures the
// Medium-only carve-out does not leak into Large/Moonshot pipelines,
// which still have a real design/research artifact for PhasePlan rewind
// to display. Writing description-review.md here would be harmless but
// semantically misleading — it signals "Plan's input is the description"
// which is only true for Medium.
func TestRewindToPhase_LargePlanDoesNotWriteDescriptionReview(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind description-review file regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("Large Feature", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Pipeline = feature.PipelineLarge
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		return nil
	})

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}

	descPath := filepath.Join(mgr.Store.BaseDir, f.ID, "description-review.md")
	if _, err := os.Stat(descPath); err == nil {
		t.Error("description-review.md written on Large PhasePlan rewind — should only write for Medium (Plan as first phase)")
	}
}

// (TestRewindToPhase_ClearsExecutionPlan removed in SchemaVersionCurrent = 3
// — the ExecutionPlan field has been removed; the per-phase plan is now read
// fresh from disk per orchestrator cycle and there is nothing on the feature
// to clear on rewind.)

func TestRewindToPhase_ClosesPerRepoPRs(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind per-repo PR close warning regression")
	dir := t.TempDir()
	store := feature.NewStore(dir)
	prs := mocks.NewMockPublisher()
	prs.ClosePRFn = func(prURL string) error {
		return errors.New("close failed")
	}
	mgr := &feature.Manager{
		Store:    store,
		Config:   &config.Config{},
		Branches: newMockBranches(false),
		PRs:      prs,
	}

	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
		{Name: "repo-b", Path: "/tmp/b"},
	})

	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusImplementing
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoStates = map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/org/a/pull/10"},
			"repo-b": {Touched: true, PRURL: "https://github.com/org/b/pull/20"},
		}
		return nil
	})

	warns, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}

	repoAWarn := false
	repoBWarn := false
	for _, w := range warns {
		if strings.Contains(w, "repo-a") && strings.Contains(w, "failed to close PR") {
			repoAWarn = true
		}
		if strings.Contains(w, "repo-b") && strings.Contains(w, "failed to close PR") {
			repoBWarn = true
		}
	}
	if !repoAWarn {
		t.Errorf("expected warning about closing PR for repo-a, got warnings: %v", warns)
	}
	if !repoBWarn {
		t.Errorf("expected warning about closing PR for repo-b, got warnings: %v", warns)
	}
}

func TestRewindToPhase_ImplementResetsMultiRepoState(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind multi-repo reset regression")
	// Rewinding TO PhaseImplement must clear RepoImpl so the post-rewind
	// path does a full reset: re-initialises all repos via InitRepoImpl.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/a"},
		{Name: "repo-b", Path: "/tmp/b"},
	})

	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhasePublish
		feat.RepoStates = map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/org/a/pull/1"},
			"repo-b": {Touched: true, PRURL: "https://github.com/org/b/pull/2"},
		}
		return nil
	})

	_, _, err := mgr.RewindToPhase(f.ID, feature.PhaseImplement)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	got, _ := mgr.Get(f.ID)

	// Multi-repo RepoImpl must be cleared for a full restart.
	if got.RepoStates != nil {
		t.Errorf("RepoImpl should be nil after rewind TO implement, got %v", got.RepoStates)
	}
}

func TestRepoCycleMethods(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	mgr.Config.Repos["repo-b"] = config.RepoConfig{Path: "/tmp/repo-b"}
	f, err := mgr.Create("Multi-Repo Feature", "desc", []string{"test-repo", "repo-b"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Advance to Published
	for _, s := range []feature.Status{feature.StatusBuildingKB, feature.StatusCreated, feature.StatusResearching, feature.StatusPlanReady, feature.StatusPlanning, feature.StatusImplementReady, feature.StatusImplementing, feature.StatusReviewPassed, feature.StatusCodeReady, feature.StatusPublished} {
		if err := mgr.Transition(f.ID, s); err != nil {
			t.Fatalf("transition to %v: %v", s, err)
		}
	}

	t.Run("StartRepoCycle", func(t *testing.T) {
		if err := mgr.StartRepoCycle(f.ID, "test-repo", feature.CycleReviewComments); err != nil {
			t.Fatal(err)
		}
		got, _ := mgr.Get(f.ID)
		if got.Status != feature.StatusPublished {
			t.Errorf("status = %v, want Published", got.Status)
		}
		rc := got.RepoCycles["test-repo"]
		if rc == nil || rc.Type != feature.CycleReviewComments || rc.Status != "running" {
			t.Errorf("repo cycle = %+v, want running review-comments", rc)
		}
	})

	t.Run("StartRepoCycle_DuplicateBlocked", func(t *testing.T) {
		err := mgr.StartRepoCycle(f.ID, "test-repo", feature.CycleReviewComments)
		if err == nil {
			t.Error("expected error for duplicate cycle start")
		}
	})

	t.Run("StartRepoCycle_BlockedWhileReviewing", func(t *testing.T) {
		// Mark the existing running cycle as reviewing
		if err := mgr.MarkRepoCycleReviewing(f.ID, "test-repo"); err != nil {
			t.Fatalf("MarkRepoCycleReviewing: %v", err)
		}
		// Starting a new cycle on the same repo should be blocked
		err := mgr.StartRepoCycle(f.ID, "test-repo", feature.CycleReviewComments)
		if err == nil {
			t.Error("expected error for cycle start while reviewing")
		}
		// Verify the existing cycle still has status "reviewing"
		got, _ := mgr.Get(f.ID)
		rc := got.RepoCycles["test-repo"]
		if rc == nil {
			t.Fatal("expected repo cycle for test-repo, got nil")
		}
		if rc.Status != "reviewing" {
			t.Errorf("RepoCycle status = %q, want %q", rc.Status, "reviewing")
		}
		// Restore to running for subsequent subtests
		_ = mgr.CompleteRepoCycle(f.ID, "test-repo")
		_ = mgr.StartRepoCycle(f.ID, "test-repo", feature.CycleReviewComments)
	})

	t.Run("StartRepoCycle_DifferentRepo", func(t *testing.T) {
		if err := mgr.StartRepoCycle(f.ID, "repo-b", feature.CycleReviewComments); err != nil {
			t.Fatal(err)
		}
		got, _ := mgr.Get(f.ID)
		if len(got.RepoCycles) != 2 {
			t.Errorf("len(RepoCycles) = %d, want 2", len(got.RepoCycles))
		}
		if got.RepoCycles["repo-b"].Type != feature.CycleReviewComments {
			t.Errorf("repo-b cycle type = %v, want review-comments", got.RepoCycles["repo-b"].Type)
		}
	})

	t.Run("HasActiveRepoCycles", func(t *testing.T) {
		active, err := mgr.HasActiveRepoCycles(f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !active {
			t.Error("expected active cycles")
		}
	})

	t.Run("CompleteRepoCycle", func(t *testing.T) {
		if err := mgr.CompleteRepoCycle(f.ID, "test-repo"); err != nil {
			t.Fatal(err)
		}
		got, _ := mgr.Get(f.ID)
		if _, ok := got.RepoCycles["test-repo"]; ok {
			t.Error("test-repo cycle should be removed after completion")
		}
		if got.RepoCycles["repo-b"] == nil {
			t.Error("repo-b cycle should still exist")
		}
	})

	t.Run("FailRepoCycle", func(t *testing.T) {
		if err := mgr.FailRepoCycle(f.ID, "repo-b", "merge conflict"); err != nil {
			t.Fatal(err)
		}
		got, _ := mgr.Get(f.ID)
		rc := got.RepoCycles["repo-b"]
		if rc.LastError == "" || rc.LastError != "merge conflict" {
			t.Errorf("repo-b cycle = %+v, want failed with error", rc)
		}
	})

	t.Run("HasActiveRepoCycles_NoneRunning", func(t *testing.T) {
		active, err := mgr.HasActiveRepoCycles(f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if active {
			t.Error("no running cycles, but HasActiveRepoCycles returned true")
		}
	})

	t.Run("ClearRepoCycles", func(t *testing.T) {
		if err := mgr.ClearRepoCycles(f.ID); err != nil {
			t.Fatal(err)
		}
		got, _ := mgr.Get(f.ID)
		if got.RepoCycles != nil {
			t.Errorf("RepoCycles should be nil, got %v", got.RepoCycles)
		}
	})
}

func TestManagerFeatureRebaseOperationLifecycle(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	m := feature.NewManager(store, config.NewDefault())
	f := &feature.Feature{
		ID:            "feat-rebase-op",
		Name:          "Rebase Operation",
		Slug:          "rebase-operation",
		Status:        feature.StatusCodeReady,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: apiRepoName},
			{Name: "web"},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	if err := m.StartFeatureRebaseOperation(f.ID); err != nil {
		t.Fatalf("StartFeatureRebaseOperation: %v", err)
	}
	if err := m.UpdateFeatureRebaseRepo(f.ID, apiRepoName, feature.RebaseRepoStatusChecking, feature.RebaseRepoProgress{}); err != nil {
		t.Fatalf("UpdateFeatureRebaseRepo checking: %v", err)
	}
	if err := m.UpdateFeatureRebaseRepo(f.ID, apiRepoName, feature.RebaseRepoStatusChanged, feature.RebaseRepoProgress{RebaseTarget: "main", Changed: true}); err != nil {
		t.Fatalf("UpdateFeatureRebaseRepo changed: %v", err)
	}
	if err := m.UpdateFeatureRebaseRepo(f.ID, "web", feature.RebaseRepoStatusUpToDate, feature.RebaseRepoProgress{RebaseTarget: "main"}); err != nil {
		t.Fatalf("UpdateFeatureRebaseRepo up to date: %v", err)
	}

	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if got.ActiveCycle == nil || got.ActiveCycle.Type != feature.CycleRebase || got.ActiveCycle.Status != feature.RepoCycleRunning {
		t.Fatalf("ActiveCycle = %+v, want running rebase", got.ActiveCycle)
	}
	if got.RebaseOperation == nil || got.RebaseOperation.Stage != feature.RebaseStageHarness {
		t.Fatalf("RebaseOperation = %+v, want harness stage", got.RebaseOperation)
	}
	if got.RebaseOperation.Repos[apiRepoName].Status != feature.RebaseRepoStatusChanged {
		t.Fatalf("api status = %q, want changed", got.RebaseOperation.Repos[apiRepoName].Status)
	}

	if err := m.ClearFeatureRebaseOperation(f.ID); err != nil {
		t.Fatalf("ClearFeatureRebaseOperation: %v", err)
	}
	got, err = store.Load(f.ID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	if got.ActiveCycle != nil || got.ActiveCycleType() != "" || got.RebaseOperation != nil {
		t.Fatalf("cycle not cleared: ActiveCycle=%+v ActiveCycleType=%q RebaseOperation=%+v", got.ActiveCycle, got.ActiveCycleType(), got.RebaseOperation)
	}
}

func TestManagerFeatureRebaseOperationDoesNotClobberOtherCycles(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	m := feature.NewManager(store, config.NewDefault())
	f := &feature.Feature{
		ID:            "feat-rebase-op-guard",
		Name:          "Rebase Operation Guard",
		Slug:          "rebase-operation-guard",
		Status:        feature.StatusPublished,
		SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun:     1,
		RunCount:      1,
		Repos: []feature.FeatureRepo{
			{Name: apiRepoName},
		},
		ActiveCycle: &feature.CycleState{
			Type:   feature.CycleReviewComments,
			Status: feature.RepoCycleRunning,
			Count:  2,
		},
		RebaseOperation: &feature.RebaseOperationState{
			Stage: feature.RebaseStageHarness,
			Repos: map[string]*feature.RebaseRepoProgress{
				apiRepoName: {Status: feature.RebaseRepoStatusChecking},
			},
		},
	}
	f.SetActiveCycleType(feature.CycleReviewComments)
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	if err := m.StartFeatureRebaseOperation(f.ID); err == nil {
		t.Fatal("StartFeatureRebaseOperation error = nil, want non-rebase cycle guard")
	}
	assertReviewCommentsCycleIntact(t, store, f.ID)

	if err := m.MarkFeatureRebaseStage(f.ID, feature.RebaseStageFinalReview); err == nil {
		t.Fatal("MarkFeatureRebaseStage error = nil, want non-rebase cycle guard")
	}
	assertReviewCommentsCycleIntact(t, store, f.ID)

	if err := m.ClearFeatureRebaseOperation(f.ID); err != nil {
		t.Fatalf("ClearFeatureRebaseOperation: %v", err)
	}
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	if got.RebaseOperation != nil {
		t.Fatalf("RebaseOperation = %+v, want nil", got.RebaseOperation)
	}
	if got.ActiveCycle == nil || got.ActiveCycle.Type != feature.CycleReviewComments || got.ActiveCycle.Status != feature.RepoCycleRunning || got.ActiveCycle.Count != 2 {
		t.Fatalf("ActiveCycle = %+v, want original review-comments cycle", got.ActiveCycle)
	}
	if got.ActiveCycleType() != feature.CycleReviewComments {
		t.Fatalf("ActiveCycleType = %q, want %q", got.ActiveCycleType(), feature.CycleReviewComments)
	}
}

func TestManagerFeatureRebaseOperationRejectsDuplicateStart(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	m := feature.NewManager(store, config.NewDefault())
	f := &feature.Feature{
		ID:            "feat-rebase-op-duplicate",
		Name:          "Rebase Operation Duplicate",
		Slug:          "rebase-operation-duplicate",
		Status:        feature.StatusCodeReady,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: apiRepoName},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	if err := m.StartFeatureRebaseOperation(f.ID); err != nil {
		t.Fatalf("StartFeatureRebaseOperation: %v", err)
	}
	if err := m.UpdateFeatureRebaseRepo(f.ID, apiRepoName, feature.RebaseRepoStatusChanged, feature.RebaseRepoProgress{RebaseTarget: "main", Changed: true}); err != nil {
		t.Fatalf("UpdateFeatureRebaseRepo changed: %v", err)
	}
	if err := m.MarkFeatureRebaseStage(f.ID, feature.RebaseStageSmartRebase); err != nil {
		t.Fatalf("MarkFeatureRebaseStage smart rebase: %v", err)
	}

	if err := m.StartFeatureRebaseOperation(f.ID); err == nil {
		t.Fatal("duplicate StartFeatureRebaseOperation error = nil, want active rebase guard")
	}
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if got.RebaseOperation == nil {
		t.Fatal("RebaseOperation = nil, want existing operation preserved")
	}
	if got.RebaseOperation.Stage != feature.RebaseStageSmartRebase {
		t.Fatalf("RebaseOperation.Stage = %q, want %q", got.RebaseOperation.Stage, feature.RebaseStageSmartRebase)
	}
	if got.RebaseOperation.Repos[apiRepoName].Status != feature.RebaseRepoStatusChanged {
		t.Fatalf("api status = %q, want changed", got.RebaseOperation.Repos[apiRepoName].Status)
	}
	if !got.RebaseOperation.Repos[apiRepoName].Changed {
		t.Fatalf("api Changed = false, want true")
	}
}

func TestManagerFeatureRebaseOperationStageRequiresOwnership(t *testing.T) {
	t.Run("no active ownership", func(t *testing.T) {
		store := feature.NewStore(t.TempDir())
		m := feature.NewManager(store, config.NewDefault())
		f := &feature.Feature{
			ID:            "feat-rebase-op-stage-no-owner",
			Name:          "Rebase Operation Stage No Owner",
			Slug:          "rebase-operation-stage-no-owner",
			Status:        feature.StatusCodeReady,
			SchemaVersion: feature.SchemaVersionCurrent,
			Repos: []feature.FeatureRepo{
				{Name: apiRepoName},
			},
		}
		if err := store.Save(f); err != nil {
			t.Fatalf("save feature: %v", err)
		}

		err := m.MarkFeatureRebaseStage(f.ID, feature.RebaseStageFinalReview)
		if err == nil {
			t.Fatal("MarkFeatureRebaseStage error = nil, want ownership guard")
		}
		got, err := store.Load(f.ID)
		if err != nil {
			t.Fatalf("load feature: %v", err)
		}
		if got.RebaseOperation != nil {
			t.Fatalf("RebaseOperation = %+v, want nil", got.RebaseOperation)
		}
		if got.ActiveCycle != nil || got.ActiveCycleType() != "" {
			t.Fatalf("cycle state changed: ActiveCycle=%+v ActiveCycleType=%q", got.ActiveCycle, got.ActiveCycleType())
		}
	})

	t.Run("cycle type ownership recovers operation", func(t *testing.T) {
		store := feature.NewStore(t.TempDir())
		m := feature.NewManager(store, config.NewDefault())
		f := &feature.Feature{
			ID:            "feat-rebase-op-stage-cycle-type",
			Name:          "Rebase Operation Stage Cycle Type",
			Slug:          "rebase-operation-stage-cycle-type",
			Status:        feature.StatusCodeReady,
			SchemaVersion: feature.SchemaVersionCurrent,
			Repos: []feature.FeatureRepo{
				{Name: apiRepoName},
			},
		}
		f.SetActiveCycleType(feature.CycleRebase)
		if err := store.Save(f); err != nil {
			t.Fatalf("save feature: %v", err)
		}

		if err := m.MarkFeatureRebaseStage(f.ID, feature.RebaseStageFinalReview); err != nil {
			t.Fatalf("MarkFeatureRebaseStage: %v", err)
		}
		got, err := store.Load(f.ID)
		if err != nil {
			t.Fatalf("load feature: %v", err)
		}
		if got.RebaseOperation == nil || got.RebaseOperation.Stage != feature.RebaseStageFinalReview {
			t.Fatalf("RebaseOperation = %+v, want final review stage", got.RebaseOperation)
		}
		if got.ActiveCycle == nil || got.ActiveCycle.Type != feature.CycleRebase || got.ActiveCycle.Status != feature.RepoCycleReviewing {
			t.Fatalf("ActiveCycle = %+v, want reviewing rebase", got.ActiveCycle)
		}
		if got.ActiveCycleType() != feature.CycleRebase {
			t.Fatalf("ActiveCycleType = %q, want %q", got.ActiveCycleType(), feature.CycleRebase)
		}
	})

	t.Run("active cycle ownership recovers operation", func(t *testing.T) {
		store := feature.NewStore(t.TempDir())
		m := feature.NewManager(store, config.NewDefault())
		f := &feature.Feature{
			ID:            "feat-rebase-op-stage-active-cycle",
			Name:          "Rebase Operation Stage Active Cycle",
			Slug:          "rebase-operation-stage-active-cycle",
			Status:        feature.StatusCodeReady,
			SchemaVersion: feature.SchemaVersionCurrent,
			Repos: []feature.FeatureRepo{
				{Name: apiRepoName},
			},
			ActiveCycle: &feature.CycleState{
				Type:   feature.CycleRebase,
				Status: feature.RepoCycleRunning,
				Count:  1,
			},
		}
		if err := store.Save(f); err != nil {
			t.Fatalf("save feature: %v", err)
		}

		if err := m.MarkFeatureRebaseStage(f.ID, feature.RebaseStageSmartRebase); err != nil {
			t.Fatalf("MarkFeatureRebaseStage: %v", err)
		}
		got, err := store.Load(f.ID)
		if err != nil {
			t.Fatalf("load feature: %v", err)
		}
		if got.RebaseOperation == nil || got.RebaseOperation.Stage != feature.RebaseStageSmartRebase {
			t.Fatalf("RebaseOperation = %+v, want smart rebase stage", got.RebaseOperation)
		}
		if got.ActiveCycle == nil || got.ActiveCycle.Type != feature.CycleRebase || got.ActiveCycle.Status != feature.RepoCycleRunning {
			t.Fatalf("ActiveCycle = %+v, want running rebase", got.ActiveCycle)
		}
		if got.ActiveCycleType() != feature.CycleRebase {
			t.Fatalf("ActiveCycleType = %q, want %q", got.ActiveCycleType(), feature.CycleRebase)
		}
	})
}

func TestManagerFeatureRebaseOperationUpdateRequiresOwnership(t *testing.T) {
	t.Run("non-rebase cycle with stale rebase operation", func(t *testing.T) {
		store := feature.NewStore(t.TempDir())
		m := feature.NewManager(store, config.NewDefault())
		f := &feature.Feature{
			ID:            "feat-rebase-op-update-guard",
			Name:          "Rebase Operation Update Guard",
			Slug:          "rebase-operation-update-guard",
			Status:        feature.StatusPublished,
			SchemaVersion: feature.SchemaVersionCurrent,
			ActiveRun:     1,
			RunCount:      1,
			Repos: []feature.FeatureRepo{
				{Name: apiRepoName},
			},
			ActiveCycle: &feature.CycleState{
				Type:   feature.CycleReviewComments,
				Status: feature.RepoCycleRunning,
				Count:  4,
			},
			RebaseOperation: &feature.RebaseOperationState{
				Stage: feature.RebaseStageHarness,
				Repos: map[string]*feature.RebaseRepoProgress{
					apiRepoName: {Status: feature.RebaseRepoStatusChecking},
				},
			},
		}
		f.SetActiveCycleType(feature.CycleReviewComments)
		if err := store.Save(f); err != nil {
			t.Fatalf("save feature: %v", err)
		}

		err := m.UpdateFeatureRebaseRepo(f.ID, apiRepoName, feature.RebaseRepoStatusChanged, feature.RebaseRepoProgress{RebaseTarget: "main", Changed: true})
		if err == nil {
			t.Fatal("UpdateFeatureRebaseRepo error = nil, want non-rebase cycle guard")
		}
		got, err := store.Load(f.ID)
		if err != nil {
			t.Fatalf("load feature: %v", err)
		}
		if got.ActiveCycle == nil || got.ActiveCycle.Type != feature.CycleReviewComments || got.ActiveCycle.Count != 4 {
			t.Fatalf("ActiveCycle = %+v, want original review-comments cycle", got.ActiveCycle)
		}
		if got.ActiveCycleType() != feature.CycleReviewComments {
			t.Fatalf("ActiveCycleType = %q, want %q", got.ActiveCycleType(), feature.CycleReviewComments)
		}
		if got.RebaseOperation == nil || got.RebaseOperation.Stage != feature.RebaseStageHarness {
			t.Fatalf("RebaseOperation = %+v, want stale operation preserved", got.RebaseOperation)
		}
		if got.RebaseOperation.Repos[apiRepoName].Status != feature.RebaseRepoStatusChecking {
			t.Fatalf("api status = %q, want checking", got.RebaseOperation.Repos[apiRepoName].Status)
		}
	})

	t.Run("no active operation", func(t *testing.T) {
		store := feature.NewStore(t.TempDir())
		m := feature.NewManager(store, config.NewDefault())
		f := &feature.Feature{
			ID:            "feat-rebase-op-update-no-owner",
			Name:          "Rebase Operation Update No Owner",
			Slug:          "rebase-operation-update-no-owner",
			Status:        feature.StatusCodeReady,
			SchemaVersion: feature.SchemaVersionCurrent,
			Repos: []feature.FeatureRepo{
				{Name: apiRepoName},
			},
		}
		if err := store.Save(f); err != nil {
			t.Fatalf("save feature: %v", err)
		}

		err := m.UpdateFeatureRebaseRepo(f.ID, apiRepoName, feature.RebaseRepoStatusChanged, feature.RebaseRepoProgress{RebaseTarget: "main", Changed: true})
		if err == nil {
			t.Fatal("UpdateFeatureRebaseRepo error = nil, want ownership guard")
		}
		got, err := store.Load(f.ID)
		if err != nil {
			t.Fatalf("load feature: %v", err)
		}
		if got.RebaseOperation != nil {
			t.Fatalf("RebaseOperation = %+v, want nil", got.RebaseOperation)
		}
		if got.ActiveCycle != nil || got.ActiveCycleType() != "" {
			t.Fatalf("cycle state changed: ActiveCycle=%+v ActiveCycleType=%q", got.ActiveCycle, got.ActiveCycleType())
		}
	})
}

func assertReviewCommentsCycleIntact(t *testing.T, store *feature.Store, featureID string) {
	t.Helper()
	got, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if got.ActiveCycle == nil || got.ActiveCycle.Type != feature.CycleReviewComments || got.ActiveCycle.Status != feature.RepoCycleRunning || got.ActiveCycle.Count != 2 {
		t.Fatalf("ActiveCycle = %+v, want original review-comments cycle", got.ActiveCycle)
	}
	if got.ActiveCycleType() != feature.CycleReviewComments {
		t.Fatalf("ActiveCycleType = %q, want %q", got.ActiveCycleType(), feature.CycleReviewComments)
	}
	if got.RebaseOperation == nil || got.RebaseOperation.Stage != feature.RebaseStageHarness {
		t.Fatalf("RebaseOperation = %+v, want stale harness state preserved before clear", got.RebaseOperation)
	}
}

// advanceToPublished is a test helper that transitions a feature through the full
// state machine from Created to Published using direct Transition calls via Store.Modify.
func advanceToPublished(t *testing.T, store *feature.Store, featureID string) {
	t.Helper()
	transitions := []feature.Status{
		feature.StatusInquiring,
		feature.StatusInquireReady,
		feature.StatusResearching,
		feature.StatusDesignReady,
		feature.StatusDesigning,
		feature.StatusPlanReady,
		feature.StatusPlanning,
		feature.StatusImplementReady,
		feature.StatusImplementing,
		feature.StatusReviewPassed,
		feature.StatusCodeReady,
		feature.StatusPublished,
	}
	for _, next := range transitions {
		if err := store.Modify(featureID, func(f *feature.Feature) error {
			return f.Transition(next)
		}); err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
	}
}

func TestManagerCompleteRefactor(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := &feature.Feature{
		ID:            feature.GenerateIDForTest(),
		Name:          "Complete Refactor Test",
		Slug:          "complete-refactor",
		Description:   "original description",
		Status:        feature.StatusCreated,
		CurrentPhase:  feature.PhaseKnowledgeBase,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	advanceToPublished(t, store, f.ID)

	// Set up refactor state directly (Manager.StartRefactor is no longer available).
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		if err := ff.Transition(feature.StatusInquiring); err != nil {
			return err
		}
		ff.SetRefactorCount(ff.RefactorCount() + 1)
		ff.RefactorPrompt = "refactor the auth module"
		ff.SetActiveCycleType(feature.CycleRefactor)
		ff.CurrentPhase = feature.PhaseInquire
		return nil
	}); err != nil {
		t.Fatalf("set refactor state: %v", err)
	}

	// Verify refactor prompt is set
	got, _ := store.Load(f.ID)
	if got.RefactorPrompt == "" {
		t.Fatal("expected RefactorPrompt to be set after StartRefactor")
	}

	// Complete the refactor
	if err := mgr.CompleteRefactor(f.ID); err != nil {
		t.Fatalf("CompleteRefactor: %v", err)
	}

	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load after complete: %v", err)
	}
	if got.RefactorPrompt != "" {
		t.Errorf("RefactorPrompt = %q, want empty after CompleteRefactor", got.RefactorPrompt)
	}
	// RefactorCount should remain (it tracks the total number of refactors)
	if got.RefactorCount() != 1 {
		t.Errorf("RefactorCount = %d, want 1 (should not be cleared)", got.RefactorCount())
	}
}

// TestRefactorTimingAccumulatesAcrossPhases verifies that the "refactor-N"
// timing key is preserved across all phase starters (Inquire → Research →
// Design → Plan → Implement) and that elapsed time accumulates into
// PhaseTimings["refactor-1"] rather than leaking into individual phase keys.
// advanceToPRReady walks a feature through the full pipeline up to (and including) PRReady.
func advanceToPRReady(t *testing.T, store *feature.Store, featureID string) {
	t.Helper()
	transitions := []feature.Status{
		feature.StatusInquiring,
		feature.StatusInquireReady,
		feature.StatusResearching,
		feature.StatusDesignReady,
		feature.StatusDesigning,
		feature.StatusPlanReady,
		feature.StatusPlanning,
		feature.StatusImplementReady,
		feature.StatusImplementing,
		feature.StatusReviewPassed,
		feature.StatusCodeReady,
	}
	for _, next := range transitions {
		if err := store.Modify(featureID, func(f *feature.Feature) error {
			return f.Transition(next)
		}); err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
	}
}

func TestCreateMediumFeature(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("medium-feature", "test", []string{"test-repo"}, config.ModelConfig{}, "", "", nil, feature.CreateOptions{Pipeline: feature.PipelineMedium})
	if err != nil {
		t.Fatal(err)
	}
	if f.Pipeline != feature.PipelineMedium {
		t.Errorf("Pipeline = %v, want medium", f.Pipeline)
	}
	if f.CurrentPhase != feature.PhasePlan {
		t.Errorf("CurrentPhase = %v, want plan", f.CurrentPhase)
	}
}

func TestCreateMoonshotFeature(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("moonshot-feature", "test", []string{"test-repo"}, config.ModelConfig{}, "", "", nil, feature.CreateOptions{Pipeline: feature.PipelineMoonshot})
	if err != nil {
		t.Fatal(err)
	}
	if f.Pipeline != feature.PipelineMoonshot {
		t.Errorf("Pipeline = %v, want moonshot", f.Pipeline)
	}
	if f.CurrentPhase != feature.PhaseKnowledgeBase {
		t.Errorf("CurrentPhase = %v, want knowledge-base", f.CurrentPhase)
	}
}

func TestCreateDefaultPipeline(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	// No CreateOptions means empty Pipeline — should default to large (from config defaults)
	f, err := mgr.Create("default-feature", "test", []string{"test-repo"}, config.ModelConfig{}, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Pipeline != feature.PipelineLarge {
		t.Errorf("Pipeline = %v, want large", f.Pipeline)
	}
	if f.CurrentPhase != feature.PhaseKnowledgeBase {
		t.Errorf("CurrentPhase = %v, want knowledge-base", f.CurrentPhase)
	}
}

func TestCreateInvalidDefaultsPipeline(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	cfg.Defaults.Pipeline = "garbage"
	cfg.Repos["test-repo"] = config.RepoConfig{Path: "/tmp/test-repo"}
	mgr := feature.NewManager(store, cfg)

	_, err := mgr.Create("invalid-default", "test", []string{"test-repo"}, config.ModelConfig{}, "", "", nil)
	if err == nil {
		t.Fatal("expected error for invalid defaults.pipeline")
	}
	if !strings.Contains(err.Error(), "invalid defaults.pipeline") {
		t.Errorf("error should mention 'invalid defaults.pipeline', got: %v", err)
	}
}

func TestCreateEmptyDefaultsPipelineFallsBackToMoonshot(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	cfg.Defaults.Pipeline = "" // explicitly empty
	cfg.Repos["test-repo"] = config.RepoConfig{Path: "/tmp/test-repo"}
	mgr := feature.NewManager(store, cfg)

	f, err := mgr.Create("fallback-feature", "test", []string{"test-repo"}, config.ModelConfig{}, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Pipeline != feature.PipelineMoonshot {
		t.Errorf("Pipeline = %v, want moonshot (fallback)", f.Pipeline)
	}
}

func TestCreateExplicitPipelineOverridesDefault(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t) // has defaults.pipeline = "large"
	f, err := mgr.Create("explicit-feature", "test", []string{"test-repo"}, config.ModelConfig{}, "", "", nil, feature.CreateOptions{Pipeline: feature.PipelineMedium})
	if err != nil {
		t.Fatal(err)
	}
	if f.Pipeline != feature.PipelineMedium {
		t.Errorf("Pipeline = %v, want medium (explicit)", f.Pipeline)
	}
}

func TestCreateFeatureWithDiscoveredRepo(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	storeDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	// Repos is empty — the repo lives only in DiscoveredRepos.
	cfg.DiscoveredRepos = map[string]config.RepoConfig{
		"my-service": {Path: "/some/path"},
	}

	mgr := feature.NewManager(store, cfg)
	f, err := mgr.Create("Discovered Repo Feature", "uses a discovered repo", []string{"my-service"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create with discovered repo: %v", err)
	}
	if len(f.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(f.Repos))
	}
	if f.Repos[0].Name != "my-service" {
		t.Errorf("repo name = %q, want %q", f.Repos[0].Name, "my-service")
	}
	if f.Repos[0].Path != "/some/path" {
		t.Errorf("repo path = %q, want %q", f.Repos[0].Path, "/some/path")
	}
}

func TestCreateFeatureExplicitRepoStillWorks(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	storeDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["explicit-repo"] = config.RepoConfig{Path: "/explicit/path"}

	mgr := feature.NewManager(store, cfg)
	f, err := mgr.Create("Explicit Repo Feature", "uses an explicit repo", []string{"explicit-repo"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create with explicit repo: %v", err)
	}
	if len(f.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(f.Repos))
	}
	if f.Repos[0].Name != "explicit-repo" {
		t.Errorf("repo name = %q, want %q", f.Repos[0].Name, "explicit-repo")
	}
	if f.Repos[0].Path != "/explicit/path" {
		t.Errorf("repo path = %q, want %q", f.Repos[0].Path, "/explicit/path")
	}
}

func TestRewindChoicesForFeature_OnlyCurrentPipelinePhases(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	f := &feature.Feature{
		Status:   feature.StatusImplementing,
		Pipeline: feature.PipelineMedium,
	}
	choices := feature.RewindChoicesForFeature(f)
	// Medium only has Plan + Implement
	if len(choices) != 2 {
		t.Fatalf("got %d choices, want 2", len(choices))
	}
	if choices[0].Phase != feature.PhasePlan {
		t.Errorf("first choice = %v, want Plan", choices[0].Phase)
	}
	if choices[1].Phase != feature.PhaseImplement {
		t.Errorf("second choice = %v, want Implement", choices[1].Phase)
	}
	for _, c := range choices {
		if c.EscalatesTo != "" {
			t.Errorf("phase %v: EscalatesTo = %q, want empty", c.Phase, c.EscalatesTo)
		}
		if c.OverridePhase != 0 {
			t.Errorf("phase %v: OverridePhase = %v, want 0", c.Phase, c.OverridePhase)
		}
	}
}

func TestRewindChoicesForFeature_MoonshotNoEscalation(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	f := &feature.Feature{
		Status:   feature.StatusImplementing,
		Pipeline: feature.PipelineMoonshot,
	}
	choices := feature.RewindChoicesForFeature(f)
	if len(choices) != 5 {
		t.Fatalf("got %d choices, want 5", len(choices))
	}
	for _, c := range choices {
		if c.EscalatesTo != "" {
			t.Errorf("phase %v: EscalatesTo = %q, want empty", c.Phase, c.EscalatesTo)
		}
		if c.OverridePhase != 0 {
			t.Errorf("phase %v: OverridePhase = %v, want 0", c.Phase, c.OverridePhase)
		}
	}
}

func TestRewindChoicesForFeature_StandardNoEscalation(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	f := &feature.Feature{
		Status:   feature.StatusImplementing,
		Pipeline: feature.PipelineLarge,
	}
	choices := feature.RewindChoicesForFeature(f)
	if len(choices) != 5 {
		t.Fatalf("got %d choices, want 5", len(choices))
	}
	for _, c := range choices {
		if c.EscalatesTo != "" {
			t.Errorf("phase %v: EscalatesTo = %q, want empty", c.Phase, c.EscalatesTo)
		}
	}
}

func TestRewindChoicesForFeature_DesignNeedsReviewExcludesPlan(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure lifecycle helper over an isolated feature value.
	f := &feature.Feature{
		Status:       feature.StatusDesignNeedsReview,
		CurrentPhase: feature.PhaseDesign,
		Pipeline:     feature.PipelineMoonshot,
	}

	choices := feature.RewindChoicesForFeature(f)
	want := []feature.Phase{feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign}
	if len(choices) != len(want) {
		t.Fatalf("got %d choices, want %d: %v", len(choices), len(want), choices)
	}
	for i, phase := range want {
		if choices[i].Phase != phase {
			t.Fatalf("choice[%d] = %v, want %v", i, choices[i].Phase, phase)
		}
	}
}

func TestRewindChoicesForFeature_MediumAtPlanning(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	f := &feature.Feature{
		Status:   feature.StatusPlanning,
		Pipeline: feature.PipelineMedium,
	}
	choices := feature.RewindChoicesForFeature(f)
	// Medium at Planning: only Plan is rewindable (it's the only medium phase up to completedUpTo)
	if len(choices) != 1 {
		t.Fatalf("got %d choices, want 1", len(choices))
	}
	if choices[0].Phase != feature.PhasePlan {
		t.Errorf("choice = %v, want Plan", choices[0].Phase)
	}
	if choices[0].EscalatesTo != "" {
		t.Errorf("phase %v: EscalatesTo = %q, want empty", choices[0].Phase, choices[0].EscalatesTo)
	}
}

func TestRewindChoicesForFeature_MediumPlanNeedsReviewIncludesPlan(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure lifecycle helper over an isolated feature value.
	f := &feature.Feature{
		Status:   feature.StatusPlanNeedsReview,
		Pipeline: feature.PipelineMedium,
	}

	choices := feature.RewindChoicesForFeature(f)
	if len(choices) != 1 {
		t.Fatalf("got %d choices, want 1", len(choices))
	}
	if choices[0].Phase != feature.PhasePlan {
		t.Fatalf("choice = %v, want Plan", choices[0].Phase)
	}
}

func TestRewindToPhase_MediumRejectsNonMediumPhase(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Esc Test", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.Pipeline = feature.PipelineMedium
		return nil
	})

	// Rewinding to Research on medium should fail — not a valid phase
	_, _, err = mgr.RewindToPhase(f.ID, feature.PhaseResearch)
	if err == nil {
		t.Fatal("expected error rewinding medium to non-medium phase, got nil")
	}
}

func TestUpgradeThenRewind_MediumToStandard(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended pipeline-upgrade rewind regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("Upgrade Test", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.Pipeline = feature.PipelineMedium
		return nil
	})

	// Step 1: Upgrade pipeline
	if err := mgr.UpgradePipeline(f.ID, feature.PipelineLarge); err != nil {
		t.Fatalf("UpgradePipeline: %v", err)
	}

	// Step 2: Rewind to Inquire — should redirect to KB Build
	_, effective, err := mgr.RewindToPhase(f.ID, feature.PhaseInquire)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	if effective != feature.PhaseKnowledgeBase {
		t.Errorf("effectiveTarget = %v, want PhaseKnowledgeBase", effective)
	}

	got, _ := mgr.Get(f.ID)
	if got.Pipeline != feature.PipelineLarge {
		t.Errorf("Pipeline = %q, want %q", got.Pipeline, feature.PipelineLarge)
	}
	wantCP := feature.DefaultCheckpointsForProfile(feature.PipelineLarge)
	if got.Checkpoints != wantCP {
		t.Errorf("Checkpoints = %+v, want %+v", got.Checkpoints, wantCP)
	}
	if got.Status != feature.StatusCreated {
		t.Errorf("Status = %v, want StatusCreated", got.Status)
	}
	if got.CurrentPhase != feature.PhaseKnowledgeBase {
		t.Errorf("CurrentPhase = %v, want PhaseKnowledgeBase", got.CurrentPhase)
	}
	if got.PipelineUpgradedFrom != "" {
		t.Errorf("PipelineUpgradedFrom = %q, want empty (cleared by KB rewind)", got.PipelineUpgradedFrom)
	}
}

func TestRewindToPhase_NoEscalationWithinProfile(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended pipeline rewind escalation regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("No Esc", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.Pipeline = feature.PipelineMedium
		return nil
	})

	_, effective, err := mgr.RewindToPhase(f.ID, feature.PhasePlan)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	if effective != feature.PhasePlan {
		t.Errorf("effectiveTarget = %v, want PhasePlan", effective)
	}
	got, _ := mgr.Get(f.ID)
	if got.Pipeline != feature.PipelineMedium {
		t.Errorf("Pipeline = %q, want %q (should not change)", got.Pipeline, feature.PipelineMedium)
	}
	if got.PendingReviewPhase == nil || *got.PendingReviewPhase != feature.PhasePlan {
		t.Errorf("PendingReviewPhase = %v, want PhasePlan", got.PendingReviewPhase)
	}
	if !got.IsRewind {
		t.Errorf("IsRewind = false, want true")
	}
}

func TestRewindToPhase_StandardToMoonshotNotNeeded(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended pipeline rewind escalation regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("Std Rewind", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.Pipeline = feature.PipelineLarge
		return nil
	})

	_, effective, err := mgr.RewindToPhase(f.ID, feature.PhaseResearch)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	if effective != feature.PhaseResearch {
		t.Errorf("effectiveTarget = %v, want PhaseResearch", effective)
	}
	got, _ := mgr.Get(f.ID)
	if got.Pipeline != feature.PipelineLarge {
		t.Errorf("Pipeline = %q, want %q (should not change)", got.Pipeline, feature.PipelineLarge)
	}
}

func TestUpgradePipeline_MediumToStandard(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Upgrade Test", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.Pipeline = feature.PipelineMedium
		return nil
	})

	if err := mgr.UpgradePipeline(f.ID, feature.PipelineLarge); err != nil {
		t.Fatalf("UpgradePipeline: %v", err)
	}
	got, _ := mgr.Get(f.ID)
	if got.Pipeline != feature.PipelineLarge {
		t.Errorf("Pipeline = %q, want %q", got.Pipeline, feature.PipelineLarge)
	}
	wantCP := feature.DefaultCheckpointsForProfile(feature.PipelineLarge)
	if got.Checkpoints != wantCP {
		t.Errorf("Checkpoints = %+v, want %+v", got.Checkpoints, wantCP)
	}
	if got.Status != feature.StatusImplementing {
		t.Errorf("Status = %v, want StatusImplementing (unchanged)", got.Status)
	}
}

func TestUpgradePipeline_StandardToMoonshot(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Upgrade S→T", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusPlanning
		f.CurrentPhase = feature.PhasePlan
		f.Pipeline = feature.PipelineLarge
		return nil
	})

	if err := mgr.UpgradePipeline(f.ID, feature.PipelineMoonshot); err != nil {
		t.Fatalf("UpgradePipeline: %v", err)
	}
	got, _ := mgr.Get(f.ID)
	if got.Pipeline != feature.PipelineMoonshot {
		t.Errorf("Pipeline = %q, want %q", got.Pipeline, feature.PipelineMoonshot)
	}
	if got.Status != feature.StatusPlanning {
		t.Errorf("Status = %v, want StatusPlanning (unchanged)", got.Status)
	}
}

func TestUpgradePipeline_MoonshotRejectsUpgrade(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Reject", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Pipeline = feature.PipelineMoonshot
		return nil
	})

	if err := mgr.UpgradePipeline(f.ID, feature.PipelineMoonshot); err == nil {
		t.Fatal("expected error upgrading moonshot to moonshot")
	}
}

func TestUpgradePipeline_CannotDowngrade(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Downgrade", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Pipeline = feature.PipelineLarge
		return nil
	})

	if err := mgr.UpgradePipeline(f.ID, feature.PipelineMedium); err == nil {
		t.Fatal("expected error downgrading large to medium")
	}
}

func TestUpgradeThenRewind_MediumToStandard_ResearchGoesToKB(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended pipeline-upgrade rewind regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("upgrade-then-rewind", "Test upgrade then rewind", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.Pipeline = feature.PipelineMedium
		f.Checkpoints = feature.DefaultCheckpointsForProfile(feature.PipelineMedium)
		f.Repos[0].WorktreePath = ""
		f.Repos[0].Path = "/tmp/test-repo"
		return nil
	})

	// Step 1: Upgrade from medium to large
	if err := mgr.UpgradePipeline(f.ID, feature.PipelineLarge); err != nil {
		t.Fatalf("UpgradePipeline: %v", err)
	}

	// Step 2: Verify upgrade recorded the origin
	got, _ := mgr.Get(f.ID)
	if got.Pipeline != feature.PipelineLarge {
		t.Errorf("Pipeline = %q, want %q", got.Pipeline, feature.PipelineLarge)
	}
	if got.PipelineUpgradedFrom != feature.PipelineMedium {
		t.Errorf("PipelineUpgradedFrom = %q, want %q", got.PipelineUpgradedFrom, feature.PipelineMedium)
	}

	// Step 3: Rewind to Research — should escalate to KB
	_, effective, err := mgr.RewindToPhase(f.ID, feature.PhaseResearch)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	if effective != feature.PhaseKnowledgeBase {
		t.Errorf("effectiveTarget = %v, want PhaseKnowledgeBase", effective)
	}

	// Step 4: Verify final state
	got, _ = mgr.Get(f.ID)
	if got.Pipeline != feature.PipelineLarge {
		t.Errorf("Pipeline = %q, want %q (unchanged)", got.Pipeline, feature.PipelineLarge)
	}
	if got.Status != feature.StatusCreated {
		t.Errorf("Status = %v, want StatusCreated", got.Status)
	}
	if got.CurrentPhase != feature.PhaseKnowledgeBase {
		t.Errorf("CurrentPhase = %v, want PhaseKnowledgeBase", got.CurrentPhase)
	}
	if got.PendingReviewPhase != nil {
		t.Errorf("PendingReviewPhase = %v, want nil", got.PendingReviewPhase)
	}
	if got.IsRewind {
		t.Errorf("IsRewind = true, want false")
	}
	if got.PipelineUpgradedFrom != "" {
		t.Errorf("PipelineUpgradedFrom = %q, want empty (cleared after KB escalation)", got.PipelineUpgradedFrom)
	}
}

func TestUpgradeThenRewind_MediumToMoonshot_DesignGoesToKB(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended pipeline-upgrade rewind regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("upgrade-then-rewind-moonshot", "Test upgrade then rewind to moonshot", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.Pipeline = feature.PipelineMedium
		f.Checkpoints = feature.DefaultCheckpointsForProfile(feature.PipelineMedium)
		f.Repos[0].WorktreePath = ""
		f.Repos[0].Path = "/tmp/test-repo"
		return nil
	})

	// Step 1: Upgrade from medium to moonshot
	if err := mgr.UpgradePipeline(f.ID, feature.PipelineMoonshot); err != nil {
		t.Fatalf("UpgradePipeline: %v", err)
	}

	// Step 2: Verify upgrade recorded the origin
	got, _ := mgr.Get(f.ID)
	if got.Pipeline != feature.PipelineMoonshot {
		t.Errorf("Pipeline = %q, want %q", got.Pipeline, feature.PipelineMoonshot)
	}
	if got.PipelineUpgradedFrom != feature.PipelineMedium {
		t.Errorf("PipelineUpgradedFrom = %q, want %q", got.PipelineUpgradedFrom, feature.PipelineMedium)
	}

	// Step 3: Rewind to Design — should escalate to KB
	_, effective, err := mgr.RewindToPhase(f.ID, feature.PhaseDesign)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	if effective != feature.PhaseKnowledgeBase {
		t.Errorf("effectiveTarget = %v, want PhaseKnowledgeBase", effective)
	}

	// Step 4: Verify final state
	got, _ = mgr.Get(f.ID)
	if got.Pipeline != feature.PipelineMoonshot {
		t.Errorf("Pipeline = %q, want %q (unchanged)", got.Pipeline, feature.PipelineMoonshot)
	}
	if got.Status != feature.StatusCreated {
		t.Errorf("Status = %v, want StatusCreated", got.Status)
	}
	if got.CurrentPhase != feature.PhaseKnowledgeBase {
		t.Errorf("CurrentPhase = %v, want PhaseKnowledgeBase", got.CurrentPhase)
	}
	if got.PendingReviewPhase != nil {
		t.Errorf("PendingReviewPhase = %v, want nil", got.PendingReviewPhase)
	}
	if got.IsRewind {
		t.Errorf("IsRewind = true, want false")
	}
	if got.PipelineUpgradedFrom != "" {
		t.Errorf("PipelineUpgradedFrom = %q, want empty (cleared after KB escalation)", got.PipelineUpgradedFrom)
	}
}

func TestUpgradeThenRewind_StandardToMoonshot_NoKBEscalation(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended pipeline-upgrade rewind regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("upgrade-std-to-moonshot", "Test large upgrade no KB", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.Pipeline = feature.PipelineLarge
		f.Checkpoints = feature.DefaultCheckpointsForProfile(feature.PipelineLarge)
		f.Repos[0].WorktreePath = ""
		f.Repos[0].Path = "/tmp/test-repo"
		return nil
	})

	// Step 1: Upgrade from large to moonshot
	if err := mgr.UpgradePipeline(f.ID, feature.PipelineMoonshot); err != nil {
		t.Fatalf("UpgradePipeline: %v", err)
	}

	// Step 2: Verify upgrade recorded the origin as large (not medium)
	got, _ := mgr.Get(f.ID)
	if got.Pipeline != feature.PipelineMoonshot {
		t.Errorf("Pipeline = %q, want %q", got.Pipeline, feature.PipelineMoonshot)
	}
	if got.PipelineUpgradedFrom != feature.PipelineLarge {
		t.Errorf("PipelineUpgradedFrom = %q, want %q", got.PipelineUpgradedFrom, feature.PipelineLarge)
	}

	// Step 3: Rewind to Research — should NOT escalate to KB (origin was large, not medium)
	_, effective, err := mgr.RewindToPhase(f.ID, feature.PhaseResearch)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	if effective != feature.PhaseResearch {
		t.Errorf("effectiveTarget = %v, want PhaseResearch (no KB escalation)", effective)
	}

	// Step 4: Verify final state — no KB escalation path
	got, _ = mgr.Get(f.ID)
	if got.Pipeline != feature.PipelineMoonshot {
		t.Errorf("Pipeline = %q, want %q (unchanged)", got.Pipeline, feature.PipelineMoonshot)
	}
}

func TestManagerMarkDone(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, _ := mgr.Create("Done Test", "test", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)

	// Advance through lifecycle to ReviewPassed
	_ = mgr.Transition(f.ID, feature.StatusResearching)
	_ = mgr.Transition(f.ID, feature.StatusPlanReady)
	_ = mgr.Transition(f.ID, feature.StatusPlanning)
	_ = mgr.Transition(f.ID, feature.StatusImplementReady)
	_ = mgr.Transition(f.ID, feature.StatusImplementing)
	_ = mgr.Transition(f.ID, feature.StatusReviewPassed)

	if err := mgr.MarkDone(f.ID); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	f, _ = mgr.Get(f.ID)
	if f.Status != feature.StatusDone {
		t.Errorf("status = %v, want Done", f.Status)
	}
}

func initGitRepo(t *testing.T, withRemote bool) string {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
		{"git", "-C", dir, "commit", "--allow-empty", "-m", "initial"},
	}
	if withRemote {
		cmds = append(cmds, []string{"git", "-C", dir, "remote", "add", "origin", "https://example.com/repo.git"})
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git setup %v: %s: %v", args, out, err)
		}
	}
	return dir
}

func TestManagerCreateSetsPublishable(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	// Repo WITH remote => publishable = true
	t.Run("with remote", func(t *testing.T) {
		storeDir := t.TempDir()
		store := feature.NewStore(storeDir)
		cfg := config.NewDefault()
		cfg.Repos["pub-repo"] = config.RepoConfig{Path: "/repos/pub-repo"}
		mgr := feature.NewManager(store, cfg)
		mgr.Branches = newMockBranches(true)

		f, err := mgr.Create("Pub Feature", "test", []string{"pub-repo"}, cfg.Defaults.Models, "", "", nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if f.Repos[0].Publishable == nil {
			t.Fatal("expected Publishable to be non-nil")
		}
		if !*f.Repos[0].Publishable {
			t.Error("expected Publishable to be true for repo with remote")
		}
		if !f.IsPublishable() {
			t.Error("expected IsPublishable() to be true")
		}
	})

	// Repo WITHOUT remote => publishable = false
	t.Run("without remote", func(t *testing.T) {
		storeDir := t.TempDir()
		store := feature.NewStore(storeDir)
		cfg := config.NewDefault()
		cfg.Repos["nopub-repo"] = config.RepoConfig{Path: "/repos/nopub-repo"}
		mgr := feature.NewManager(store, cfg)
		mgr.Branches = newMockBranches(false)

		f, err := mgr.Create("NoPub Feature", "test", []string{"nopub-repo"}, cfg.Defaults.Models, "", "", nil)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if f.Repos[0].Publishable == nil {
			t.Fatal("expected Publishable to be non-nil")
		}
		if *f.Repos[0].Publishable {
			t.Error("expected Publishable to be false for repo without remote")
		}
		if f.IsPublishable() {
			t.Error("expected IsPublishable() to be false")
		}
	})
}

func TestRewindToPhase_SkipsClosePRForUnpublished(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind PR-close skip regression")
	dir := t.TempDir()
	store := feature.NewStore(dir)
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	publishable := false
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "local-repo", Path: "/tmp/local", Publishable: &publishable},
	})

	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhasePublish
		feat.SetPRURL("https://github.com/org/repo/pull/99")
		feat.RepoStates = map[string]*feature.RepoState{
			"local-repo": {Touched: true, PRURL: "https://github.com/org/repo/pull/100"},
		}
		return nil
	})

	warns, _, err := mgr.RewindToPhase(f.ID, feature.PhaseResearch)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	for _, w := range warns {
		if strings.Contains(w, "failed to close PR") {
			t.Errorf("ClosePR should be skipped for unpublishable feature, got warning: %s", w)
		}
	}
}

func TestRewindToPhase_ClosePRStillCalledForPublished(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind PR-close warning regression")
	dir := t.TempDir()
	store := feature.NewStore(dir)
	prs := mocks.NewMockPublisher()
	prs.ClosePRFn = func(prURL string) error {
		return errors.New("close failed")
	}
	mgr := &feature.Manager{
		Store:    store,
		Config:   &config.Config{},
		Branches: newMockBranches(false),
		PRs:      prs,
	}

	publishable := true
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "pub-repo", Path: "/tmp/pub", Publishable: &publishable},
	})

	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhasePublish
		// Per-repo PR URL is the only source of truth post-cutover.
		if feat.RepoStates == nil {
			feat.RepoStates = map[string]*feature.RepoState{}
		}
		feat.RepoStates["pub-repo"] = &feature.RepoState{
			Touched: true, PRURL: "https://github.com/org/repo/pull/42",
		}
		return nil
	})

	warns, _, err := mgr.RewindToPhase(f.ID, feature.PhaseResearch)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	foundPRWarn := false
	for _, w := range warns {
		if strings.Contains(w, "failed to close PR") {
			foundPRWarn = true
			break
		}
	}
	if !foundPRWarn {
		t.Errorf("ClosePR should be called for publishable feature; expected 'failed to close PR' warning, got: %v", warns)
	}
}

func TestRestartFromBeginning_UnpublishedUsesLocalReset(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	if testing.Short() {
		t.Skip("requires git")
	}
	repoDir := initGitRepo(t, false)
	storeDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["local-repo"] = config.RepoConfig{Path: repoDir}
	mgr := feature.NewManager(store, cfg)
	wtDir := t.TempDir()
	mgr.Worktrees = git.NewWorktreeManager(wtDir)
	mgr.Branches = &git.BranchAdapter{}
	mgr.PRs = &git.PublishAdapter{}

	publishable := false
	f, err := mgr.Create("Local Restart", "test", []string{"local-repo"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Force Publishable to false (Create may probe the repo)
	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Repos[0].Publishable = &publishable
		feat.Status = feature.StatusImplementing
		feat.CurrentPhase = feature.PhaseImplement
		return nil
	})

	// Make a commit in the worktree so HEAD diverges from baseBranch
	loaded, _ := store.Load(f.ID)
	wtPath := loaded.Repos[0].WorktreePath
	if wtPath == "" {
		t.Fatal("worktree path not set")
	}
	commitCmd := exec.Command("git", "-C", wtPath, "commit", "--allow-empty", "-m", "diverge")
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("commit in worktree: %s: %v", out, err)
	}

	// RestartFromBeginning should succeed (ResetToBase would fail due to no origin)
	if err := mgr.RestartFromBeginning(f.ID); err != nil {
		t.Fatalf("RestartFromBeginning: %v", err)
	}

	// Verify HEAD is back to baseBranch
	baseBranch := loaded.Repos[0].BaseBranch
	headCmd := exec.Command("git", "-C", wtPath, "rev-parse", "HEAD")
	headOut, _ := headCmd.Output()
	baseCmd := exec.Command("git", "-C", wtPath, "rev-parse", baseBranch)
	baseOut, _ := baseCmd.Output()
	if strings.TrimSpace(string(headOut)) != strings.TrimSpace(string(baseOut)) {
		t.Errorf("HEAD should match %s after local reset", baseBranch)
	}
}

func TestRewindToPhase_UnpublishedUsesLocalReset(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	if testing.Short() {
		t.Skip("requires git")
	}
	repoDir := initGitRepo(t, false)
	storeDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["local-repo"] = config.RepoConfig{Path: repoDir}
	mgr := feature.NewManager(store, cfg)
	wtDir := t.TempDir()
	mgr.Worktrees = git.NewWorktreeManager(wtDir)
	mgr.Branches = &git.BranchAdapter{}
	mgr.PRs = &git.PublishAdapter{}

	publishable := false
	f, err := mgr.Create("Local Rewind", "test", []string{"local-repo"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Repos[0].Publishable = &publishable
		feat.Status = feature.StatusImplementing
		feat.CurrentPhase = feature.PhaseImplement
		return nil
	})

	loaded, _ := store.Load(f.ID)
	wtPath := loaded.Repos[0].WorktreePath
	if wtPath == "" {
		t.Fatal("worktree path not set")
	}
	commitCmd := exec.Command("git", "-C", wtPath, "commit", "--allow-empty", "-m", "diverge")
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("commit in worktree: %s: %v", out, err)
	}

	warns, _, err := mgr.RewindToPhase(f.ID, feature.PhaseResearch)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	for _, w := range warns {
		if strings.Contains(w, "failed to reset worktree") {
			t.Errorf("ResetToBaseLocal should succeed for local-only repo, got warning: %s", w)
		}
	}

	baseBranch := loaded.Repos[0].BaseBranch
	headCmd := exec.Command("git", "-C", wtPath, "rev-parse", "HEAD")
	headOut, _ := headCmd.Output()
	baseCmd := exec.Command("git", "-C", wtPath, "rev-parse", baseBranch)
	baseOut, _ := baseCmd.Output()
	if strings.TrimSpace(string(headOut)) != strings.TrimSpace(string(baseOut)) {
		t.Errorf("HEAD should match %s after local reset", baseBranch)
	}
}

func TestManagerCreateSkipsBranchCheckForUnpublishedRepos(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	storeDir := t.TempDir()
	store := feature.NewStore(storeDir)
	cfg := config.NewDefault()
	cfg.Repos["local-repo"] = config.RepoConfig{Path: "/repos/local-repo"}
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = &mocks.MockWorktreeOperator{
		CreateFn: func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
			return filepath.Join(t.TempDir(), featureSlug, repoName), nil
		},
	}
	branches := newMockBranches(false)
	branches.BranchExistsOnRemoteFn = func(repoPath, branch string) (bool, error) {
		t.Fatalf("BranchExistsOnRemote(%q, %q) was called for an unpublished repo", repoPath, branch)
		return false, nil
	}
	mgr.Branches = branches
	mgr.PRs = mocks.NewMockPublisher()

	f, err := mgr.Create("Local Feature", "test", []string{"local-repo"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if f.Repos[0].Publishable == nil {
		t.Fatal("expected Publishable to be non-nil")
	}
	if *f.Repos[0].Publishable {
		t.Error("expected Publishable to be false for repo without remote")
	}
}

func TestRewindToPhase_FromReviewing_StatusMapping(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended reviewing rewind status matrix")
	tests := []struct {
		name        string
		targetPhase feature.Phase
		wantStatus  feature.Status
		wantPhase   feature.Phase
	}{
		{"to_implement", feature.PhaseImplement, feature.StatusPlanNeedsReview, feature.PhasePlan},
		{"to_plan", feature.PhasePlan, feature.StatusDesignNeedsReview, feature.PhaseDesign},
		{"to_research", feature.PhaseResearch, feature.StatusInquiryNeedsReview, feature.PhaseInquire},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newTestManager(t)
			f, err := mgr.Create("test feature", "test description", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
				f.Status = feature.StatusImplementing
				f.CurrentPhase = feature.PhaseReview
				f.ReviewIteration = 3
				return nil
			})
			_, _, err = mgr.RewindToPhase(f.ID, tt.targetPhase)
			if err != nil {
				t.Fatalf("RewindToPhase: %v", err)
			}
			got, _ := mgr.Get(f.ID)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %v", got.Status, tt.wantStatus)
			}
			if got.CurrentPhase != tt.wantPhase {
				t.Errorf("CurrentPhase = %v, want %v", got.CurrentPhase, tt.wantPhase)
			}
			if got.PendingReviewPhase == nil {
				t.Error("PendingReviewPhase should not be nil")
			} else if *got.PendingReviewPhase != tt.targetPhase {
				t.Errorf("PendingReviewPhase = %v, want %v", *got.PendingReviewPhase, tt.targetPhase)
			}
		})
	}
}

// TestRewindToPhase_FromReviewing_ReviewArtifactsPreservedInSealedRun verifies
// review artifacts from the sealed run stay on disk under `runs/run-001/`.
// The fresh run starts with no review dir.
func TestRewindToPhase_FromReviewing_ReviewArtifactsPreservedInSealedRun(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended reviewing rewind artifact-preservation regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("test feature", "test description", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseReview
		return nil
	})
	// Seed review/ under the active run (run-001).
	run1Dir := filepath.Join(mgr.Store.BaseDir, f.ID, "runs", "run-001")
	reviewDir := filepath.Join(run1Dir, "review")
	os.MkdirAll(filepath.Join(reviewDir, "iteration-01"), 0o755)
	os.WriteFile(filepath.Join(reviewDir, "iteration-01", "meta.yaml"), []byte("status: approved"), 0o644)

	_, _, err = mgr.RewindToPhase(f.ID, feature.PhaseImplement)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	// Sealed run-001 still has its review/ tree.
	if _, err := os.Stat(filepath.Join(reviewDir, "iteration-01", "meta.yaml")); err != nil {
		t.Errorf("sealed run-001/review/.../meta.yaml missing: %v", err)
	}
	// Fresh run-002 should not have a review/ dir yet.
	if _, err := os.Stat(filepath.Join(mgr.Store.BaseDir, f.ID, "runs", "run-002", "review")); !os.IsNotExist(err) {
		t.Error("run-002/review should not exist after seal+fork")
	}
}

func TestRewindToPhase_FromReviewing_ClearsReviewIteration(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended reviewing rewind field-reset regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("test feature", "test description", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseReview
		f.ReviewIteration = 5
		return nil
	})
	_, _, err = mgr.RewindToPhase(f.ID, feature.PhaseImplement)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	got, _ := mgr.Get(f.ID)
	if got.ReviewIteration != 0 {
		t.Errorf("ReviewIteration = %d, want 0 after rewind", got.ReviewIteration)
	}
}

// TestRewindToPhase_NotFromReviewing_ReviewDirPreservedInSealedRun — under
// seal+fork the prior review/ tree is left intact on the sealed run; the
// fresh run has no review/ dir at all.
func TestRewindToPhase_NotFromReviewing_ReviewDirPreservedInSealedRun(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind review-dir preservation regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("test feature", "test description", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		return nil
	})
	run1Dir := filepath.Join(mgr.Store.BaseDir, f.ID, "runs", "run-001")
	reviewDir := filepath.Join(run1Dir, "review")
	os.MkdirAll(reviewDir, 0o755)
	os.WriteFile(filepath.Join(reviewDir, "stale.txt"), []byte("stale"), 0o644)

	_, _, err = mgr.RewindToPhase(f.ID, feature.PhasePlan)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	if _, err := os.Stat(filepath.Join(reviewDir, "stale.txt")); err != nil {
		t.Errorf("sealed run-001/review/stale.txt missing: %v", err)
	}
}

// TestRewindToPhase_FromReviewing_CarriesKeptPhaseTimings — seal+fork carries
// the planning ledger (its outputs survive a rewind to implement) and drops
// the review spend; the sealed run-001 retains its full timing/cost history.
func TestRewindToPhase_FromReviewing_CarriesKeptPhaseTimings(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended reviewing rewind timing reset regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("test feature", "test description", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseReview
		f.PhaseTimings = map[string]time.Duration{
			"review": 5 * time.Minute,
			"plan":   2 * time.Minute,
		}
		f.PhaseCosts = map[string]float64{
			"review": 0.50,
			"plan":   0.25,
		}
		return nil
	})
	_, _, err = mgr.RewindToPhase(f.ID, feature.PhaseImplement)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	got, _ := mgr.Get(f.ID)
	// New active run (run-002) carries the plan ledger; review is redone.
	wantTimings := map[string]time.Duration{"plan": 2 * time.Minute}
	if !maps.Equal(got.PhaseTimings, wantTimings) {
		t.Errorf("new run PhaseTimings = %v, want %v", got.PhaseTimings, wantTimings)
	}
	wantCosts := map[string]float64{"plan": 0.25}
	if !maps.Equal(got.PhaseCosts, wantCosts) {
		t.Errorf("new run PhaseCosts = %v, want %v", got.PhaseCosts, wantCosts)
	}
	// Sealed run-001 keeps both timings/costs as history.
	sealed, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if _, ok := sealed.PhaseTimings["review"]; !ok {
		t.Error("sealed run-001 should keep review timing as history")
	}
	if _, ok := sealed.PhaseTimings["plan"]; !ok {
		t.Error("sealed run-001 should keep plan timing as history")
	}
}

func TestRewindToPhase_FromReviewing_InvalidTarget(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("test feature", "test description", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseReview
		f.Pipeline = feature.PipelineMedium
		return nil
	})
	// PhaseResearch is not in Medium pipeline, so should be rejected
	_, _, err = mgr.RewindToPhase(f.ID, feature.PhaseResearch)
	if err == nil {
		t.Fatal("expected error for invalid rewind target, got nil")
	}
}

func TestMarkRepoCycleReviewing(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	mgr.Config.Repos["repo-b"] = config.RepoConfig{Path: "/tmp/repo-b"}
	f, err := mgr.Create("Review Cycle Feature", "desc", []string{"test-repo", "repo-b"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Advance to Published so we can start repo cycles
	for _, s := range []feature.Status{feature.StatusBuildingKB, feature.StatusCreated, feature.StatusResearching, feature.StatusPlanReady, feature.StatusPlanning, feature.StatusImplementReady, feature.StatusImplementing, feature.StatusReviewPassed, feature.StatusCodeReady, feature.StatusPublished} {
		if err := mgr.Transition(f.ID, s); err != nil {
			t.Fatalf("transition to %v: %v", s, err)
		}
	}

	t.Run("Success", func(t *testing.T) {
		if err := mgr.StartRepoCycle(f.ID, "test-repo", feature.CycleReviewComments); err != nil {
			t.Fatal(err)
		}
		if err := mgr.MarkRepoCycleReviewing(f.ID, "test-repo"); err != nil {
			t.Fatalf("MarkRepoCycleReviewing: %v", err)
		}
		got, _ := mgr.Get(f.ID)
		rc := got.RepoCycles["test-repo"]
		if rc == nil {
			t.Fatal("expected repo cycle for test-repo, got nil")
		}
		if rc.Status != "reviewing" {
			t.Errorf("RepoCycle status = %q, want %q", rc.Status, "reviewing")
		}
		// Clean up: complete the cycle so it does not interfere with other subtests
		_ = mgr.CompleteRepoCycle(f.ID, "test-repo")
	})

	t.Run("ErrorNoCycles", func(t *testing.T) {
		// Ensure no active cycles exist
		_ = mgr.ClearRepoCycles(f.ID)
		err := mgr.MarkRepoCycleReviewing(f.ID, "test-repo")
		if err == nil {
			t.Fatal("expected error when no active cycles exist, got nil")
		}
	})

	t.Run("ErrorWrongRepo", func(t *testing.T) {
		// Start a cycle only for test-repo, then ask for a non-existent repo
		if err := mgr.StartRepoCycle(f.ID, "test-repo", feature.CycleReviewComments); err != nil {
			t.Fatal(err)
		}
		err := mgr.MarkRepoCycleReviewing(f.ID, "no-such-repo")
		if err == nil {
			t.Fatal("expected error for wrong repo name, got nil")
		}
		// Clean up
		_ = mgr.CompleteRepoCycle(f.ID, "test-repo")
	})
}

func TestRemoveRepoCycle(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	mgr.Config.Repos["repo-b"] = config.RepoConfig{Path: "/tmp/repo-b"}
	f, err := mgr.Create("Remove Cycle Feature", "desc", []string{"test-repo", "repo-b"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []feature.Status{feature.StatusResearching, feature.StatusPlanReady, feature.StatusPlanning, feature.StatusImplementReady, feature.StatusImplementing, feature.StatusReviewPassed, feature.StatusCodeReady, feature.StatusPublished} {
		if err := mgr.Transition(f.ID, s); err != nil {
			t.Fatalf("transition to %v: %v", s, err)
		}
	}

	// Seed two repo cycle entries via Store.Modify
	if err := mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.RepoCycles = map[string]*feature.RepoCycleState{
			apiRepoName: {Type: feature.CycleReviewComments, Status: "running"},
			"backend":   {Type: feature.CycleReviewComments, Status: "running"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Remove the "api" repo cycle — "backend" should remain.
	if err := mgr.RemoveRepoCycle(f.ID, apiRepoName); err != nil {
		t.Fatal(err)
	}
	got, _ := mgr.Get(f.ID)
	if _, ok := got.RepoCycles[apiRepoName]; ok {
		t.Error("api cycle should be removed")
	}
	if got.RepoCycles["backend"] == nil {
		t.Error("backend cycle should still exist")
	}

	// Remove "backend" — RepoCycles should become nil
	if err := mgr.RemoveRepoCycle(f.ID, "backend"); err != nil {
		t.Fatal(err)
	}
	got, _ = mgr.Get(f.ID)
	if got.RepoCycles != nil {
		t.Errorf("RepoCycles should be nil after removing last entry, got %v", got.RepoCycles)
	}
}

func TestHasActiveRepoCycles_TreatsNeedUserInputAsActive(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Paused Cycle Feature", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []feature.Status{feature.StatusBuildingKB, feature.StatusCreated, feature.StatusResearching, feature.StatusPlanReady, feature.StatusPlanning, feature.StatusImplementReady, feature.StatusImplementing, feature.StatusReviewPassed, feature.StatusCodeReady, feature.StatusPublished} {
		if err := mgr.Transition(f.ID, s); err != nil {
			t.Fatalf("transition to %v: %v", s, err)
		}
	}
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.RepoCycles = map[string]*feature.RepoCycleState{
			"test-repo": {
				Type:                     feature.CycleReviewComments,
				Status:                   feature.RepoCycleNeedUserInput,
				PendingNeedUserInputPath: "/tmp/review-comments.yaml",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed paused cycle: %v", err)
	}

	active, err := mgr.HasActiveRepoCycles(f.ID)
	if err != nil {
		t.Fatalf("HasActiveRepoCycles: %v", err)
	}
	if !active {
		t.Fatal("HasActiveRepoCycles() = false, want true for paused cycle gate")
	}
}

func TestFailRepoCycle_ClearsPausedGateAndRefactorPrompt(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Refactor Cleanup", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []feature.Status{feature.StatusBuildingKB, feature.StatusCreated, feature.StatusResearching, feature.StatusPlanReady, feature.StatusPlanning, feature.StatusImplementReady, feature.StatusImplementing, feature.StatusReviewPassed, feature.StatusCodeReady, feature.StatusPublished} {
		if err := mgr.Transition(f.ID, s); err != nil {
			t.Fatalf("transition to %v: %v", s, err)
		}
	}
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.RefactorPrompt = "extract validation"
		ff.RepoCycles = map[string]*feature.RepoCycleState{
			"test-repo": {
				Type:                     feature.CycleRefactor,
				Status:                   feature.RepoCycleNeedUserInput,
				PendingNeedUserInputPath: "/tmp/refactor.yaml",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed paused refactor: %v", err)
	}

	if err := mgr.FailRepoCycle(f.ID, "test-repo", "user aborted"); err != nil {
		t.Fatalf("FailRepoCycle: %v", err)
	}
	got, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rc := got.RepoCycles["test-repo"]
	if rc == nil {
		t.Fatal("repo cycle missing after FailRepoCycle")
	}
	if rc.Status != feature.RepoCycleFailed {
		t.Errorf("Status = %q, want %q", rc.Status, feature.RepoCycleFailed)
	}
	if rc.LastError != "user aborted" {
		t.Errorf("LastError = %q, want %q", rc.LastError, "user aborted")
	}
	if rc.PendingNeedUserInputPath != "" {
		t.Errorf("PendingNeedUserInputPath = %q, want empty", rc.PendingNeedUserInputPath)
	}
	if got.RefactorPrompt != "" {
		t.Errorf("RefactorPrompt = %q, want empty after refactor abort", got.RefactorPrompt)
	}
}

func TestHasActiveRepoCycles_ReviewingIsActive(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	mgr := newTestManager(t)
	f, err := mgr.Create("Reviewing Active Feature", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Advance to Published so we can start repo cycles
	for _, s := range []feature.Status{feature.StatusBuildingKB, feature.StatusCreated, feature.StatusResearching, feature.StatusPlanReady, feature.StatusPlanning, feature.StatusImplementReady, feature.StatusImplementing, feature.StatusReviewPassed, feature.StatusCodeReady, feature.StatusPublished} {
		if err := mgr.Transition(f.ID, s); err != nil {
			t.Fatalf("transition to %v: %v", s, err)
		}
	}

	// Start a repo cycle and mark it as reviewing
	if err := mgr.StartRepoCycle(f.ID, "test-repo", feature.CycleReviewComments); err != nil {
		t.Fatal(err)
	}
	if err := mgr.MarkRepoCycleReviewing(f.ID, "test-repo"); err != nil {
		t.Fatalf("MarkRepoCycleReviewing: %v", err)
	}

	// HasActiveRepoCycles should return true for a reviewing cycle
	active, err := mgr.HasActiveRepoCycles(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Error("expected HasActiveRepoCycles to return true when a cycle is in reviewing status")
	}
}

// InitRepoImpl must preserve existing non-pending per-repo state. Without
// this, a restart mid-implement would clobber approved repos back to pending,
// defeating the engine's resume skip-list and redoing approved work.
func TestInitRepoImpl_PreservesExistingState(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := &feature.Store{BaseDir: dir}
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := &feature.Feature{
		ID:   "test-init-preserve",
		Name: "Test",
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/a"},
			{Name: "repo-b", Path: "/tmp/b"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://example.com/a/pr/1"},
			"repo-b": {},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := mgr.InitRepoImpl(f.ID); err != nil {
		t.Fatalf("InitRepoImpl: %v", err)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.RepoStates["repo-a"].Touched {
		t.Errorf("repo-a Touched = false, want true (existing state must survive)")
	}
	if loaded.RepoStates["repo-a"].PRURL == "" {
		t.Errorf("repo-a PRURL was cleared, want preserved")
	}
	if loaded.RepoStates["repo-b"].Touched {
		t.Errorf("repo-b Touched = true, want false (existing state must survive)")
	}
}

// InitRepoImpl must prune entries for repos that are no longer part of the
// feature. Otherwise stale state would persist after repo removal.
func TestInitRepoImpl_PrunesRemovedRepos(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := &feature.Store{BaseDir: dir}
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := &feature.Feature{
		ID:            "test-init-prune",
		Name:          "Test",
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/a"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a":   {},
			"removed":  {Touched: true},
			"removed2": {LastError: "boom"},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := mgr.InitRepoImpl(f.ID); err != nil {
		t.Fatalf("InitRepoImpl: %v", err)
	}

	loaded, _ := store.Load(f.ID)
	if len(loaded.RepoStates) != 1 {
		t.Fatalf("expected 1 repo impl entry, got %d: %+v", len(loaded.RepoStates), loaded.RepoStates)
	}
	if _, ok := loaded.RepoStates["repo-a"]; !ok {
		t.Error("repo-a entry missing")
	}
	if _, ok := loaded.RepoStates["removed"]; ok {
		t.Error("removed entry should have been pruned")
	}
}

// InitRepoImpl must add missing entries as Pending without touching existing
// non-pending state.
func TestInitRepoImpl_AddsMissingEntriesAsPending(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := &feature.Store{BaseDir: dir}
	mgr := &feature.Manager{Store: store, Config: &config.Config{}}

	f := &feature.Feature{
		ID:   "test-init-mixed",
		Name: "Test",
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/a"},
			{Name: "repo-b", Path: "/tmp/b"},
			{Name: "repo-c", Path: "/tmp/c"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {Touched: true},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := mgr.InitRepoImpl(f.ID); err != nil {
		t.Fatalf("InitRepoImpl: %v", err)
	}

	loaded, _ := store.Load(f.ID)
	if len(loaded.RepoStates) != 3 {
		t.Fatalf("expected 3 repo impl entries, got %d", len(loaded.RepoStates))
	}
	if !loaded.RepoStates["repo-a"].Touched {
		t.Errorf("repo-a Touched lost: %+v", loaded.RepoStates["repo-a"])
	}
	if loaded.RepoStates["repo-b"].Touched {
		t.Errorf("repo-b should be untouched, got %+v", loaded.RepoStates["repo-b"])
	}
	if loaded.RepoStates["repo-c"].Touched {
		t.Errorf("repo-c should be untouched, got %+v", loaded.RepoStates["repo-c"])
	}
}

// seedRewindableFeature puts a freshly-created feature into
// StatusImplementing / PhaseImplement so any phase is a valid rewind target.
// Returns the feature's base run directory for seeding marker files.
func seedRewindableFeature(t *testing.T, mgr *feature.Manager) (*feature.Feature, string) {
	t.Helper()
	f, err := mgr.Create("Carry Forward", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	run1Dir := filepath.Join(mgr.Store.BaseDir, f.ID, "runs", "run-001")
	return f, run1Dir
}

// seedCarryForwardFixtures writes marker files into a full set of phase
// directories under run-001 so TestRewindToPhase_CarriesForwardCorrectPhases
// and TestRewindToPhase_ToPhaseImplement_CarriesPlanDirs can assert carry
// semantics against real on-disk content.
func seedCarryForwardFixtures(t *testing.T, run1Dir string) {
	t.Helper()
	files := map[string]string{
		filepath.Join("inquire", "marker.txt"):            "inquire",
		filepath.Join("research", "marker.txt"):           "research",
		filepath.Join("design", "marker.txt"):             "design",
		filepath.Join("plan", "plan.md"):                  "plan",
		filepath.Join("roadmap", "roadmap.md"):            "roadmap",
		filepath.Join("phase-01", "plan", "plan.md"):      "phase-01-plan",
		filepath.Join("phase-01", "implement", "impl.md"): "phase-01-impl",
		filepath.Join("phase-02", "plan", "plan.md"):      "phase-02-plan",
		filepath.Join("phase-02", "implement", "impl.md"): "phase-02-impl",
		filepath.Join("implement", "impl.md"):             "implement",
	}
	for rel, content := range files {
		full := filepath.Join(run1Dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

// TestRewindToPhase_CarriesForwardCorrectPhases verifies the static matrix:
// rewind to each rewindable target carries exactly the set of phase
// directories that carryForwardDirs declares, no more and no less.
func TestRewindToPhase_CarriesForwardCorrectPhases(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	tests := []struct {
		name           string
		target         feature.Phase
		wantCarried    []string // relative dirs expected under run-002
		wantNotCarried []string // relative dirs that MUST NOT appear under run-002
	}{
		{
			name:           "inquire carries nothing",
			target:         feature.PhaseInquire,
			wantCarried:    nil,
			wantNotCarried: []string{"inquire", "research", "design", "plan", "roadmap", "implement"},
		},
		{
			name:           "research carries inquire",
			target:         feature.PhaseResearch,
			wantCarried:    []string{"inquire"},
			wantNotCarried: []string{"research", "design", "plan", "roadmap", "implement"},
		},
		{
			name:           "design carries inquire+research",
			target:         feature.PhaseDesign,
			wantCarried:    []string{"inquire", "research"},
			wantNotCarried: []string{"design", "plan", "roadmap", "implement"},
		},
		{
			name:           "plan carries inquire+research+design",
			target:         feature.PhasePlan,
			wantCarried:    []string{"inquire", "research", "design"},
			wantNotCarried: []string{"plan", "roadmap", "implement"},
		},
		{
			name:           "implement carries prior+plan+roadmap+phase-NN/plan",
			target:         feature.PhaseImplement,
			wantCarried:    []string{"inquire", "research", "design", "plan", "roadmap", filepath.Join("phase-01", "plan"), filepath.Join("phase-02", "plan")},
			wantNotCarried: []string{"implement", filepath.Join("phase-01", "implement"), filepath.Join("phase-02", "implement")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if testing.Short() && tc.name != "implement carries prior+plan+roadmap+phase-NN/plan" {
				t.Skip("extended carry-forward matrix; short mode keeps the widest representative target")
			}
			mgr := newTestManager(t)
			f, run1Dir := seedRewindableFeature(t, mgr)
			seedCarryForwardFixtures(t, run1Dir)

			if _, _, err := mgr.RewindToPhase(f.ID, tc.target); err != nil {
				t.Fatalf("RewindToPhase(%v): %v", tc.target, err)
			}

			run2Dir := filepath.Join(mgr.Store.BaseDir, f.ID, "runs", "run-002")
			// Sealed run preserves every fixture directory.
			for _, rel := range []string{"inquire", "research", "design", "plan", "roadmap", "implement", filepath.Join("phase-01", "plan"), filepath.Join("phase-01", "implement"), filepath.Join("phase-02", "plan"), filepath.Join("phase-02", "implement")} {
				if _, err := os.Stat(filepath.Join(run1Dir, rel)); err != nil {
					t.Errorf("sealed run-001/%s missing (seal+fork must preserve artifacts): %v", rel, err)
				}
			}
			// Carried directories must exist in the new run with identical
			// file content.
			for _, rel := range tc.wantCarried {
				srcFile := firstRegularFileIn(t, filepath.Join(run1Dir, rel))
				dstFile := filepath.Join(run2Dir, rel, filepath.Base(srcFile))
				srcBytes, err := os.ReadFile(srcFile)
				if err != nil {
					t.Fatalf("read sealed %s: %v", srcFile, err)
				}
				dstBytes, err := os.ReadFile(dstFile)
				if err != nil {
					t.Errorf("run-002/%s/%s missing (should be carried): %v", rel, filepath.Base(srcFile), err)
					continue
				}
				if string(srcBytes) != string(dstBytes) {
					t.Errorf("run-002/%s content = %q, want %q", rel, dstBytes, srcBytes)
				}
			}
			// Non-carried directories must not exist in the new run.
			for _, rel := range tc.wantNotCarried {
				if _, err := os.Stat(filepath.Join(run2Dir, rel)); !os.IsNotExist(err) {
					t.Errorf("run-002/%s should NOT exist for target %v (err=%v)", rel, tc.target, err)
				}
			}

			// CarriedPhases on the new run mirrors the carried directory list.
			newRun, err := mgr.Store.LoadRun(f.ID, 2)
			if err != nil {
				t.Fatalf("LoadRun(2): %v", err)
			}
			if newRun.CarriedFromRun != 1 {
				t.Errorf("CarriedFromRun = %d, want 1", newRun.CarriedFromRun)
			}
			gotCarried := append([]string(nil), newRun.CarriedPhases...)
			sort.Strings(gotCarried)
			want := append([]string(nil), tc.wantCarried...)
			sort.Strings(want)
			if !stringSlicesEqual(gotCarried, want) {
				t.Errorf("CarriedPhases = %v, want %v", gotCarried, want)
			}
		})
	}
}

// TestRewindToPhase_ToPhaseImplement_CarriesPlanDirs specializes the
// carry-forward suite on the Implement target's dynamic phase-NN/plan
// discovery: many phase-NN directories carry, phase-NN without a plan/
// subdir does not.
func TestRewindToPhase_ToPhaseImplement_CarriesPlanDirs(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended dynamic phase-plan carry-forward regression")
	mgr := newTestManager(t)
	f, run1Dir := seedRewindableFeature(t, mgr)

	// Seed a wide range of phase-NN directories, plus one implement-only.
	for _, n := range []string{"phase-01", "phase-02", "phase-03", "phase-04", "phase-10"} {
		planPath := filepath.Join(run1Dir, n, "plan", "plan.md")
		if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", planPath, err)
		}
		if err := os.WriteFile(planPath, []byte(n+"-plan"), 0o644); err != nil {
			t.Fatalf("write %s: %v", planPath, err)
		}
		implPath := filepath.Join(run1Dir, n, "implement", "impl.md")
		if err := os.MkdirAll(filepath.Dir(implPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", implPath, err)
		}
		if err := os.WriteFile(implPath, []byte(n+"-impl"), 0o644); err != nil {
			t.Fatalf("write %s: %v", implPath, err)
		}
	}
	// phase-99 has ONLY implement/, no plan/ → not discovered.
	onlyImpl := filepath.Join(run1Dir, "phase-99", "implement", "impl.md")
	if err := os.MkdirAll(filepath.Dir(onlyImpl), 0o755); err != nil {
		t.Fatalf("mkdir phase-99: %v", err)
	}
	if err := os.WriteFile(onlyImpl, []byte("only-impl"), 0o644); err != nil {
		t.Fatalf("write phase-99: %v", err)
	}

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhaseImplement); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}

	run2Dir := filepath.Join(mgr.Store.BaseDir, f.ID, "runs", "run-002")
	for _, n := range []string{"phase-01", "phase-02", "phase-03", "phase-04", "phase-10"} {
		if _, err := os.Stat(filepath.Join(run2Dir, n, "plan", "plan.md")); err != nil {
			t.Errorf("run-002/%s/plan/plan.md missing: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(run2Dir, n, "implement")); !os.IsNotExist(err) {
			t.Errorf("run-002/%s/implement should NOT exist (err=%v)", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(run2Dir, "phase-99")); !os.IsNotExist(err) {
		t.Errorf("run-002/phase-99 should NOT exist (no plan/ subdir to discover)")
	}

	newRun, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	// CarriedPhases should contain the static set + every discovered plan dir.
	gotCarried := append([]string(nil), newRun.CarriedPhases...)
	want := []string{"inquire", "research", "design", "roadmap", "plan"}
	for _, n := range []string{"phase-01", "phase-02", "phase-03", "phase-04", "phase-10"} {
		want = append(want, filepath.Join(n, "plan"))
	}
	if !stringSlicesEqual(gotCarried, want) {
		t.Errorf("CarriedPhases = %v, want %v (static + sorted phase-NN/plan)", gotCarried, want)
	}
}

func TestRewindWithRequest_RejectsInvalidPartialBeforeSideEffects(t *testing.T) {
	tests := []struct {
		name    string
		request feature.RewindRequest
		mutate  func(*feature.Feature)
		wantErr string
	}{
		{
			name:    "roadmap phase on non-implement target",
			request: feature.RewindRequest{TargetPhase: feature.PhasePlan, RoadmapPhase: 1},
			wantErr: "roadmap phase rewind is only valid for Implement",
		},
		{
			name:    "single phase roadmap",
			request: feature.RewindRequest{TargetPhase: feature.PhaseImplement, RoadmapPhase: 1},
			mutate:  func(f *feature.Feature) { f.TotalRoadmapPhases = 1 },
			wantErr: "requires a multi-phase roadmap",
		},
		{
			name:    "out of range roadmap phase",
			request: feature.RewindRequest{TargetPhase: feature.PhaseImplement, RoadmapPhase: 4},
			wantErr: "out of range",
		},
		{
			name:    "missing previous phase anchor",
			request: feature.RewindRequest{TargetPhase: feature.PhaseImplement, RoadmapPhase: 2},
			wantErr: "missing commit anchor",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newTestManager(t)
			f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
				{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
			})
			if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
				ff.Status = feature.StatusImplementing
				ff.CurrentPhase = feature.PhaseImplement
				ff.CurrentRoadmapPhase = 3
				ff.TotalRoadmapPhases = 3
				ff.RepoStates = map[string]*feature.RepoState{
					"repo-a": {PRURL: "https://example.invalid/pr/1"},
				}
				if tc.mutate != nil {
					tc.mutate(ff)
				}
				return nil
			}); err != nil {
				t.Fatalf("modify: %v", err)
			}
			branches := mocks.NewMockBranchOperator()
			prs := mocks.NewMockPublisher()
			worktrees := mocks.NewMockWorktreeOperator()
			mgr.Branches = branches
			mgr.PRs = prs
			mgr.Worktrees = worktrees

			_, _, err := mgr.RewindWithRequest(f.ID, tc.request)
			if err == nil {
				t.Fatal("RewindWithRequest returned nil error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want contains %q", err, tc.wantErr)
			}
			got, getErr := mgr.Get(f.ID)
			if getErr != nil {
				t.Fatalf("Get: %v", getErr)
			}
			if got.Status == feature.StatusInterrupted {
				t.Fatal("feature was interrupted before validation completed")
			}
			if len(branches.Calls) != 0 {
				t.Fatalf("backup branch side effects ran before validation: %+v", branches.Calls)
			}
			if len(prs.Calls) != 0 {
				t.Fatalf("PR close side effects ran before validation: %+v", prs.Calls)
			}
			if len(worktrees.Calls) != 0 {
				t.Fatalf("worktree reset side effects ran before validation: %+v", worktrees.Calls)
			}
		})
	}
}

func TestRewindWithRequest_Phase1PartialUsesBaseResetWithoutAnchor(t *testing.T) {
	mgr := newTestManager(t)
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
	})
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.CurrentRoadmapPhase = 2
		ff.TotalRoadmapPhases = 3
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	worktrees := mocks.NewMockWorktreeOperator()
	mgr.Worktrees = worktrees
	mgr.Branches = nil
	mgr.PRs = nil

	if _, _, err := mgr.RewindWithRequest(f.ID, feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 1,
	}); err != nil {
		t.Fatalf("RewindWithRequest: %v", err)
	}
	if len(worktrees.Calls) != 1 {
		t.Fatalf("worktree calls = %+v, want one base reset", worktrees.Calls)
	}
	if worktrees.Calls[0].Method != "ResetToBase" {
		t.Fatalf("worktree call = %s, want ResetToBase", worktrees.Calls[0].Method)
	}
}

func TestRewindWithRequest_PartialPersistsPendingReviewRoadmapPhase(t *testing.T) {
	mgr := newTestManager(t)
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
	})
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.CurrentRoadmapPhase = 2
		ff.TotalRoadmapPhases = 3
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	mgr.Worktrees = mocks.NewMockWorktreeOperator()
	mgr.Branches = nil
	mgr.PRs = nil

	if _, _, err := mgr.RewindWithRequest(f.ID, feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 1,
	}); err != nil {
		t.Fatalf("RewindWithRequest: %v", err)
	}

	got, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PendingRewindReviewRoadmapPhase == nil || *got.PendingRewindReviewRoadmapPhase != 1 {
		t.Fatalf("PendingRewindReviewRoadmapPhase = %v, want 1", got.PendingRewindReviewRoadmapPhase)
	}
	if got.PendingReviewPhase == nil || *got.PendingReviewPhase != feature.PhaseImplement {
		t.Fatalf("PendingReviewPhase = %v, want PhaseImplement", got.PendingReviewPhase)
	}
	if !got.IsRewind {
		t.Fatal("IsRewind = false, want true")
	}
	if got.CurrentRoadmapPhase != 1 {
		t.Errorf("CurrentRoadmapPhase = %d, want 1", got.CurrentRoadmapPhase)
	}
	if got.TotalRoadmapPhases != 3 {
		t.Errorf("TotalRoadmapPhases = %d, want 3", got.TotalRoadmapPhases)
	}
	if got.RoadmapPhaseType != "tracer-bullet" {
		t.Errorf("RoadmapPhaseType = %q, want tracer-bullet", got.RoadmapPhaseType)
	}

	loaded, err := feature.NewStore(mgr.Store.BaseDir).Load(f.ID)
	if err != nil {
		t.Fatalf("load through fresh store: %v", err)
	}
	if loaded.PendingRewindReviewRoadmapPhase == nil || *loaded.PendingRewindReviewRoadmapPhase != 1 {
		t.Fatalf("loaded PendingRewindReviewRoadmapPhase = %v, want 1", loaded.PendingRewindReviewRoadmapPhase)
	}
	run, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	if run.PendingRewindReviewRoadmapPhase == nil || *run.PendingRewindReviewRoadmapPhase != 1 {
		t.Fatalf("run PendingRewindReviewRoadmapPhase = %v, want 1", run.PendingRewindReviewRoadmapPhase)
	}
}

func TestRewindWithRequest_Phase2PartialResetsToAnchorAndCarriesSurvivors(t *testing.T) {
	mgr := newTestManager(t)
	unpublishable := false
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
		{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", BaseBranch: "main", Publishable: &unpublishable},
	})
	run1Dir := filepath.Join(mgr.Store.BaseDir, f.ID, "runs", "run-001")
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.CurrentRoadmapPhase = 3
		ff.TotalRoadmapPhases = 3
		ff.Artifacts = map[string]string{
			"plan":              filepath.Join(run1Dir, "phase-03", "plan", "phase-plan.md"),
			"roadmap":           filepath.Join(run1Dir, "roadmap", "roadmap.md"),
			"phase-1-plan":      filepath.Join(run1Dir, "phase-01", "plan", "phase-plan.md"),
			"phase-1-implement": filepath.Join(run1Dir, "phase-01", "implement", "iteration-01", "x"),
			"phase-2-plan":      filepath.Join(run1Dir, "phase-02", "plan", "phase-plan.md"),
			"phase-2-implement": filepath.Join(run1Dir, "phase-02", "implement", "iteration-01", "x"),
			"phase-3-plan":      filepath.Join(run1Dir, "phase-03", "plan", "phase-plan.md"),
		}
		ff.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
			1: {
				"repo-a": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repo-b": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
			2: {
				"repo-a": "cccccccccccccccccccccccccccccccccccccccc",
				"repo-b": "dddddddddddddddddddddddddddddddddddddddd",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	files := map[string]string{
		filepath.Join("roadmap", "roadmap.md"):                      "roadmap",
		filepath.Join("phase-01", "plan", "phase-plan.md"):          "phase 1 plan",
		filepath.Join("phase-01", "implement", "iteration-01", "x"): "phase 1 impl",
		filepath.Join("phase-01", "testing-contract.yaml"):          "phase 1 contract",
		filepath.Join("phase-02", "plan", "phase-plan.md"):          "phase 2 plan",
		filepath.Join("phase-02", "implement", "iteration-01", "x"): "phase 2 impl",
		filepath.Join("phase-02", "testing-contract.yaml"):          "phase 2 contract",
		filepath.Join("phase-03", "plan", "phase-plan.md"):          "phase 3 plan",
		filepath.Join("phase-03", "implement", "iteration-01", "x"): "phase 3 impl",
		filepath.Join("phase-03", "testing-contract.yaml"):          "phase 3 contract",
	}
	for rel, content := range files {
		full := filepath.Join(run1Dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	worktrees := mocks.NewMockWorktreeOperator()
	mgr.Worktrees = worktrees
	mgr.Branches = nil
	mgr.PRs = nil

	if _, _, err := mgr.RewindWithRequest(f.ID, feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 2,
	}); err != nil {
		t.Fatalf("RewindWithRequest: %v", err)
	}
	wantCalls := []struct {
		path string
		sha  string
	}{
		{"/tmp/wt-a", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"/tmp/wt-b", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	if len(worktrees.Calls) != len(wantCalls) {
		t.Fatalf("worktree calls = %+v, want %d reset-to-commit calls", worktrees.Calls, len(wantCalls))
	}
	for i, want := range wantCalls {
		call := worktrees.Calls[i]
		if call.Method != "ResetToCommit" {
			t.Fatalf("call %d method = %s, want ResetToCommit", i, call.Method)
		}
		if call.Args[0] != want.path || call.Args[1] != want.sha {
			t.Fatalf("call %d args = %+v, want %s %s", i, call.Args, want.path, want.sha)
		}
	}

	run2Dir := filepath.Join(mgr.Store.BaseDir, f.ID, "runs", "run-002")
	for _, rel := range []string{
		filepath.Join("roadmap", "roadmap.md"),
		filepath.Join("phase-01", "plan", "phase-plan.md"),
		filepath.Join("phase-01", "implement", "iteration-01", "x"),
		filepath.Join("phase-01", "testing-contract.yaml"),
		filepath.Join("phase-02", "plan", "phase-plan.md"),
	} {
		if _, err := os.Stat(filepath.Join(run2Dir, rel)); err != nil {
			t.Errorf("run-002/%s missing: %v", rel, err)
		}
	}
	for _, rel := range []string{
		filepath.Join("phase-02", "implement"),
		filepath.Join("phase-02", "testing-contract.yaml"),
		filepath.Join("phase-03"),
	} {
		if _, err := os.Stat(filepath.Join(run2Dir, rel)); !os.IsNotExist(err) {
			t.Errorf("run-002/%s should not carry forward (err=%v)", rel, err)
		}
	}
	newRun, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	if len(newRun.RoadmapPhaseCommitAnchors) != 1 {
		t.Fatalf("new run anchors = %#v, want only phase 1", newRun.RoadmapPhaseCommitAnchors)
	}
	if newRun.CurrentRoadmapPhase != 2 {
		t.Errorf("new run CurrentRoadmapPhase = %d, want 2", newRun.CurrentRoadmapPhase)
	}
	if newRun.TotalRoadmapPhases != 3 {
		t.Errorf("new run TotalRoadmapPhases = %d, want 3", newRun.TotalRoadmapPhases)
	}
	if newRun.RoadmapPhaseType != "tdd-fill-in" {
		t.Errorf("new run RoadmapPhaseType = %q, want tdd-fill-in", newRun.RoadmapPhaseType)
	}
	if got, want := newRun.Artifacts["plan"], filepath.Join("phase-02", "plan", "phase-plan.md"); got != want {
		t.Errorf("new run Artifacts[plan] = %q, want %q (generic plan alias must not point at discarded phase 3)", got, want)
	}
	if got, want := newRun.Artifacts["phase-2-plan"], filepath.Join("phase-02", "plan", "phase-plan.md"); got != want {
		t.Errorf("new run Artifacts[phase-2-plan] = %q, want %q", got, want)
	}
	for _, key := range []string{"phase-2-implement", "phase-3-plan"} {
		if got, ok := newRun.Artifacts[key]; ok {
			t.Errorf("new run Artifacts[%s] = %q, want key omitted", key, got)
		}
	}
	if got := newRun.RoadmapPhaseCommitAnchors[1]["repo-a"]; got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("phase 1 repo-a anchor = %q", got)
	}
	if _, ok := newRun.RoadmapPhaseCommitAnchors[2]; ok {
		t.Errorf("phase 2 anchors carried into target phase rewind: %#v", newRun.RoadmapPhaseCommitAnchors[2])
	}
	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if sealedRun.RewindRoadmapPhase == nil || *sealedRun.RewindRoadmapPhase != 2 {
		t.Fatalf("sealed run RewindRoadmapPhase = %v, want 2", sealedRun.RewindRoadmapPhase)
	}
	if got := sealedRun.RoadmapPhaseCommitAnchors[2]["repo-b"]; got != "dddddddddddddddddddddddddddddddddddddddd" {
		t.Errorf("sealed run phase 2 repo-b anchor = %q", got)
	}
}

func TestRoadmapPhaseFrontendPersistsThroughRunShadowSync(t *testing.T) {
	mgr := newTestManager(t)
	f, err := mgr.Create("Frontend Phase", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if f.RoadmapPhaseFrontend(1) {
		t.Fatal("RoadmapPhaseFrontend(1) = true for absent phase, want false")
	}
	if f.AnyRoadmapPhaseFrontend() {
		t.Fatal("AnyRoadmapPhaseFrontend() = true with no recorded frontend phases")
	}

	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.SetRoadmapPhaseFrontend(2, true)
		ff.SetRoadmapPhaseFrontend(3, false)
		return nil
	}); err != nil {
		t.Fatalf("modify frontend flags: %v", err)
	}

	loaded, err := feature.NewStore(mgr.Store.BaseDir).Load(f.ID)
	if err != nil {
		t.Fatalf("fresh Load: %v", err)
	}
	if loaded.RoadmapPhaseFrontend(1) {
		t.Error("RoadmapPhaseFrontend(1) = true for absent phase, want false")
	}
	if !loaded.RoadmapPhaseFrontend(2) {
		t.Error("RoadmapPhaseFrontend(2) = false, want true")
	}
	if loaded.RoadmapPhaseFrontend(3) {
		t.Error("RoadmapPhaseFrontend(3) = true, want false")
	}
	if !loaded.AnyRoadmapPhaseFrontend() {
		t.Error("AnyRoadmapPhaseFrontend() = false, want true")
	}

	run, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if !run.RoadmapPhaseFrontend(2) {
		t.Error("run RoadmapPhaseFrontend(2) = false, want true")
	}
	if run.RoadmapPhaseFrontend(3) {
		t.Error("run RoadmapPhaseFrontend(3) = true, want false")
	}
}

func TestRewindWithRequest_PartialRetainsTargetPhaseFrontend(t *testing.T) {
	mgr := newTestManager(t)
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
	})
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.CurrentRoadmapPhase = 3
		ff.TotalRoadmapPhases = 3
		ff.SetRoadmapPhaseFrontend(2, true)
		ff.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
			1: {"repo-a": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			2: {"repo-a": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		}
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	mgr.Worktrees = mocks.NewMockWorktreeOperator()
	mgr.Branches = nil
	mgr.PRs = nil

	if _, _, err := mgr.RewindWithRequest(f.ID, feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 2,
	}); err != nil {
		t.Fatalf("RewindWithRequest: %v", err)
	}

	got, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.RoadmapPhaseFrontend(2) {
		t.Error("RoadmapPhaseFrontend(2) = false after rewinding to phase 2, want retained for the carried phase plan")
	}
	if !got.AnyRoadmapPhaseFrontend() {
		t.Error("AnyRoadmapPhaseFrontend() = false after retaining the frontend target phase")
	}

	run, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	if !run.RoadmapPhaseFrontend(2) {
		t.Error("run RoadmapPhaseFrontend(2) = false after rewinding to phase 2, want retained for the carried phase plan")
	}
	if !run.AnyRoadmapPhaseFrontend() {
		t.Error("run AnyRoadmapPhaseFrontend() = false after retaining the frontend target phase")
	}
}

func TestRewindWithRequest_PartialRestoresMissingTargetPhaseFrontendFromPlan(t *testing.T) {
	mgr := newTestManager(t)
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
	})
	run1Dir := filepath.Join(mgr.Store.BaseDir, f.ID, "runs", "run-001")
	planPath := filepath.Join(run1Dir, "phase-01", "plan", "phase-plan.md")
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatalf("mkdir phase plan: %v", err)
	}
	if err := os.WriteFile(planPath, []byte("## Metadata\n\n**Frontend:** true\n"), 0o644); err != nil {
		t.Fatalf("write phase plan: %v", err)
	}
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.CurrentRoadmapPhase = 2
		ff.TotalRoadmapPhases = 2
		ff.Artifacts = map[string]string{
			"plan":         planPath,
			"phase-1-plan": planPath,
		}
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	mgr.Worktrees = mocks.NewMockWorktreeOperator()
	mgr.Branches = nil
	mgr.PRs = nil

	if _, _, err := mgr.RewindWithRequest(f.ID, feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 1,
	}); err != nil {
		t.Fatalf("RewindWithRequest: %v", err)
	}

	got, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.RoadmapPhaseFrontend(1) {
		t.Error("RoadmapPhaseFrontend(1) = false, want restored from carried phase plan")
	}
}

func TestRewindToPhase_FullImplementOmitsSealedRoadmapPhase(t *testing.T) {
	mgr := newTestManager(t)
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
	})
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.CurrentRoadmapPhase = 2
		ff.TotalRoadmapPhases = 3
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	mgr.Worktrees = mocks.NewMockWorktreeOperator()
	mgr.Branches = nil
	mgr.PRs = nil

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhaseImplement); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}
	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if sealedRun.RewindRoadmapPhase != nil {
		t.Errorf("full Implement rewind RewindRoadmapPhase = %v, want nil", sealedRun.RewindRoadmapPhase)
	}
}

func TestRewindWithRequest_PartialUnpublishableSkipsPRCloseAndRecordsBackups(t *testing.T) {
	mgr := newTestManager(t)
	unpublishable := false
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
		{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", BaseBranch: "main", Publishable: &unpublishable},
	})
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.CurrentRoadmapPhase = 3
		ff.TotalRoadmapPhases = 3
		ff.Slug = "partial-side-effects"
		ff.RepoStates = map[string]*feature.RepoState{
			"repo-a": {PRURL: "https://example.invalid/repo-a/pull/1"},
			"repo-b": {PRURL: "https://example.invalid/repo-b/pull/2"},
		}
		ff.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
			1: {
				"repo-a": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repo-b": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	worktrees := mocks.NewMockWorktreeOperator()
	branches := mocks.NewMockBranchOperator()
	branches.CreateBackupBranchFn = func(worktreePath, slug string) (string, error) {
		return "feature/" + slug + "-pre-rewind-" + filepath.Base(worktreePath), nil
	}
	prs := mocks.NewMockPublisher()
	mgr.Worktrees = worktrees
	mgr.Branches = branches
	mgr.PRs = prs

	warns, _, err := mgr.RewindWithRequest(f.ID, feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 2,
	})
	if err != nil {
		t.Fatalf("RewindWithRequest: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings = %v, want none", warns)
	}
	if len(prs.Calls) != 0 {
		t.Fatalf("PR calls = %+v, want none for unpublishable feature", prs.Calls)
	}
	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if len(sealedRun.BackupBranches) != 2 {
		t.Fatalf("BackupBranches = %#v, want both repos", sealedRun.BackupBranches)
	}
	if sealedRun.BackupBranches["repo-a"] == "" || sealedRun.BackupBranches["repo-b"] == "" {
		t.Errorf("BackupBranches = %#v, want branch names for repo-a and repo-b", sealedRun.BackupBranches)
	}
}

func TestRewindWithRequest_PartialPublishableClosesPRs(t *testing.T) {
	mgr := newTestManager(t)
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
		{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", BaseBranch: "main"},
	})
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.CurrentRoadmapPhase = 3
		ff.TotalRoadmapPhases = 3
		ff.RepoStates = map[string]*feature.RepoState{
			"repo-a": {PRURL: "https://example.invalid/repo-a/pull/1"},
			"repo-b": {PRURL: "https://example.invalid/repo-b/pull/2"},
		}
		ff.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
			1: {
				"repo-a": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repo-b": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	mgr.Worktrees = mocks.NewMockWorktreeOperator()
	mgr.Branches = nil
	prs := mocks.NewMockPublisher()
	mgr.PRs = prs

	warns, _, err := mgr.RewindWithRequest(f.ID, feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 2,
	})
	if err != nil {
		t.Fatalf("RewindWithRequest: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings = %v, want none", warns)
	}
	if len(prs.Calls) != 2 {
		t.Fatalf("PR calls = %+v, want two closes", prs.Calls)
	}
}

func TestRewindWithRequest_PartialBranchFailureStillSeals(t *testing.T) {
	mgr := newTestManager(t)
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
		{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", BaseBranch: "main"},
	})
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.CurrentRoadmapPhase = 3
		ff.TotalRoadmapPhases = 3
		ff.Slug = "partial-branch-warning"
		ff.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
			1: {
				"repo-a": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repo-b": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	mgr.Worktrees = mocks.NewMockWorktreeOperator()
	branches := mocks.NewMockBranchOperator()
	branches.CreateBackupBranchFn = func(worktreePath, slug string) (string, error) {
		if worktreePath == "/tmp/wt-a" {
			return "", errors.New("boom")
		}
		return "feature/" + slug + "-repo-b", nil
	}
	mgr.Branches = branches
	mgr.PRs = nil

	warns, _, err := mgr.RewindWithRequest(f.ID, feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 2,
	})
	if err != nil {
		t.Fatalf("RewindWithRequest: %v", err)
	}
	attrCount := 0
	for _, w := range warns {
		if strings.Contains(w, "failed to create backup branch for repo-a") {
			attrCount++
		}
		if strings.Contains(w, "repo-b") {
			t.Errorf("warnings should not mention repo-b, got %q", w)
		}
	}
	if attrCount != 1 {
		t.Fatalf("repo-a backup warning count = %d, want 1 (warns=%v)", attrCount, warns)
	}
	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if len(sealedRun.BackupBranches) != 1 {
		t.Fatalf("BackupBranches = %#v, want repo-b only", sealedRun.BackupBranches)
	}
	if got := sealedRun.BackupBranches["repo-b"]; got != "feature/partial-branch-warning-repo-b" {
		t.Errorf("BackupBranches[repo-b] = %q, want feature/partial-branch-warning-repo-b", got)
	}
	if _, ok := sealedRun.BackupBranches["repo-a"]; ok {
		t.Errorf("BackupBranches contains repo-a after failed backup: %#v", sealedRun.BackupBranches)
	}
}

// TestRewindToPhase_RecordsBackupBranches verifies each successful
// CreateBackupBranch call is recorded in the sealed run's BackupBranches
// map keyed by repo name.
func TestRewindToPhase_RecordsBackupBranches(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind backup-branch recording regression")
	mgr := newTestManager(t)
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
		{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", BaseBranch: "main"},
	})
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Slug = "recording-slug"
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}

	mock := mocks.NewMockBranchOperator()
	mock.CreateBackupBranchFn = func(worktreePath, slug string) (string, error) {
		return "feature/" + slug + "-pre-rewind-" + filepath.Base(worktreePath), nil
	}
	mgr.Branches = mock

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}

	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	want := map[string]string{
		"repo-a": "feature/recording-slug-pre-rewind-wt-a",
		"repo-b": "feature/recording-slug-pre-rewind-wt-b",
	}
	if len(sealedRun.BackupBranches) != len(want) {
		t.Fatalf("BackupBranches len = %d, want %d: %v", len(sealedRun.BackupBranches), len(want), sealedRun.BackupBranches)
	}
	for k, v := range want {
		got, ok := sealedRun.BackupBranches[k]
		if !ok {
			t.Errorf("BackupBranches missing repo %q", k)
			continue
		}
		if got != v {
			t.Errorf("BackupBranches[%q] = %q, want %q", k, got, v)
		}
	}
}

// TestRewindToPhase_PartialBranchFailureStillSeals verifies that one repo
// failing CreateBackupBranch does not abort the rewind: the surviving repo
// is recorded, the failing repo produces a per-repo warning, and the fresh
// run-002 is created.
func TestRewindToPhase_PartialBranchFailureStillSeals(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind partial backup failure regression")
	mgr := newTestManager(t)
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
		{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", BaseBranch: "main"},
	})
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Slug = "partial-slug"
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}

	mock := mocks.NewMockBranchOperator()
	mock.CreateBackupBranchFn = func(worktreePath, slug string) (string, error) {
		if worktreePath == "/tmp/wt-a" {
			return "", errors.New("boom")
		}
		return "feature/" + slug + "-success", nil
	}
	mgr.Branches = mock

	warns, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan)
	if err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}

	// One warning, per-repo attribution for repo-a, nothing about repo-b.
	attrCount := 0
	for _, w := range warns {
		if strings.Contains(w, "failed to create backup branch for repo-a") {
			attrCount++
		}
		if strings.Contains(w, "repo-b") {
			t.Errorf("warnings should not mention repo-b (it succeeded), got %q", w)
		}
	}
	if attrCount != 1 {
		t.Errorf("expected exactly one repo-a warning, got %d (warns=%v)", attrCount, warns)
	}

	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if len(sealedRun.BackupBranches) != 1 {
		t.Fatalf("BackupBranches len = %d, want 1 (repo-b only): %v", len(sealedRun.BackupBranches), sealedRun.BackupBranches)
	}
	if got := sealedRun.BackupBranches["repo-b"]; got != "feature/partial-slug-success" {
		t.Errorf("BackupBranches[repo-b] = %q, want %q", got, "feature/partial-slug-success")
	}
	if _, ok := sealedRun.BackupBranches["repo-a"]; ok {
		t.Errorf("BackupBranches must NOT contain repo-a (it failed)")
	}

	// Fresh run-002 exists.
	freshRun, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	if freshRun.RunNumber != 2 || freshRun.CarriedFromRun != 1 {
		t.Errorf("run-002 RunNumber/CarriedFromRun = %d/%d, want 2/1", freshRun.RunNumber, freshRun.CarriedFromRun)
	}
}

// TestRewindToPhase_ArtifactMapCarriedForward verifies the Artifacts map is
// both key-filtered and value-normalized (to run-relative) on the new run,
// while the sealed run retains its original absolute values.
func TestRewindToPhase_ArtifactMapCarriedForward(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind artifact-map normalization regression")
	t.Run("rewind to plan carries inquire/research/design as run-relative", func(t *testing.T) {
		mgr := newTestManager(t)
		f, run1Dir := seedRewindableFeature(t, mgr)

		absInquire := filepath.Join(run1Dir, "inquire", "inquire.md")
		absResearch := filepath.Join(run1Dir, "research", "research.md")
		absDesign := filepath.Join(run1Dir, "design", "design.md")
		absPlan := filepath.Join(run1Dir, "plan", "plan.md")
		absImpl := filepath.Join(run1Dir, "implement", "impl.md")
		for _, p := range []string{absInquire, absResearch, absDesign, absPlan, absImpl} {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", p, err)
			}
			if err := os.WriteFile(p, []byte(filepath.Base(p)), 0o644); err != nil {
				t.Fatalf("write %s: %v", p, err)
			}
		}

		if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
			ff.Artifacts = map[string]string{
				"inquire":   absInquire,
				"research":  absResearch,
				"design":    absDesign,
				"plan":      absPlan,
				"implement": absImpl,
				"pr_url":    "https://github.com/o/r/pull/1",
			}
			return nil
		}); err != nil {
			t.Fatalf("modify: %v", err)
		}

		if _, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan); err != nil {
			t.Fatalf("RewindToPhase: %v", err)
		}

		got, err := mgr.Get(f.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		wantRel := map[string]string{
			"inquire":  filepath.Join("inquire", "inquire.md"),
			"research": filepath.Join("research", "research.md"),
			"design":   filepath.Join("design", "design.md"),
		}
		if len(got.Artifacts) != len(wantRel) {
			t.Errorf("new run Artifacts len = %d, want %d: %v", len(got.Artifacts), len(wantRel), got.Artifacts)
		}
		for k, wantVal := range wantRel {
			v, ok := got.Artifacts[k]
			if !ok {
				t.Errorf("new run Artifacts missing key %q", k)
				continue
			}
			if filepath.IsAbs(v) {
				t.Errorf("new run Artifacts[%q] = %q is absolute, want run-relative", k, v)
			}
			if strings.Contains(v, "run-001") {
				t.Errorf("new run Artifacts[%q] = %q still contains run-001 prefix", k, v)
			}
			if v != wantVal {
				t.Errorf("new run Artifacts[%q] = %q, want %q", k, v, wantVal)
			}
		}
		for _, disallowed := range []string{"plan", "implement", "pr_url"} {
			if _, ok := got.Artifacts[disallowed]; ok {
				t.Errorf("new run Artifacts must NOT carry %q", disallowed)
			}
		}

		// Sealed run preserves its absolute values verbatim.
		sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
		if err != nil {
			t.Fatalf("LoadRun(1): %v", err)
		}
		if got := sealedRun.Artifacts["inquire"]; got != absInquire {
			t.Errorf("sealed Artifacts[inquire] = %q, want absolute %q (sealed-run immutability)", got, absInquire)
		}
		if got := sealedRun.Artifacts["pr_url"]; got != "https://github.com/o/r/pull/1" {
			t.Errorf("sealed Artifacts[pr_url] = %q, want URL verbatim", got)
		}
	})

	t.Run("rewind to implement carries plan+roadmap+phase-N-plan", func(t *testing.T) {
		mgr := newTestManager(t)
		f, run1Dir := seedRewindableFeature(t, mgr)

		absMap := map[string]string{
			"inquire":      filepath.Join(run1Dir, "inquire", "inquire.md"),
			"research":     filepath.Join(run1Dir, "research", "research.md"),
			"design":       filepath.Join(run1Dir, "design", "design.md"),
			"plan":         filepath.Join(run1Dir, "plan", "plan.md"),
			"roadmap":      filepath.Join(run1Dir, "roadmap", "roadmap.md"),
			"phase-1-plan": filepath.Join(run1Dir, "phase-01", "plan", "plan.md"),
			"phase-2-plan": filepath.Join(run1Dir, "phase-02", "plan", "plan.md"),
			"implement":    filepath.Join(run1Dir, "implement", "impl.md"),
			"phase-1-impl": filepath.Join(run1Dir, "phase-01", "implement", "impl.md"),
			"pr_url":       "https://github.com/o/r/pull/2",
		}
		for _, v := range absMap {
			if strings.HasPrefix(v, "https://") {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(v), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", v, err)
			}
			if err := os.WriteFile(v, []byte(filepath.Base(v)), 0o644); err != nil {
				t.Fatalf("write %s: %v", v, err)
			}
		}

		if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
			ff.Artifacts = absMap
			return nil
		}); err != nil {
			t.Fatalf("modify: %v", err)
		}

		if _, _, err := mgr.RewindToPhase(f.ID, feature.PhaseImplement); err != nil {
			t.Fatalf("RewindToPhase: %v", err)
		}

		got, err := mgr.Get(f.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		wantCarried := []string{"inquire", "research", "design", "plan", "roadmap", "phase-1-plan", "phase-2-plan"}
		if len(got.Artifacts) != len(wantCarried) {
			t.Errorf("new run Artifacts len = %d, want %d: %v", len(got.Artifacts), len(wantCarried), got.Artifacts)
		}
		for _, k := range wantCarried {
			v, ok := got.Artifacts[k]
			if !ok {
				t.Errorf("new run Artifacts missing carried key %q", k)
				continue
			}
			if filepath.IsAbs(v) {
				t.Errorf("new run Artifacts[%q] = %q is absolute, want run-relative", k, v)
			}
		}
		for _, disallowed := range []string{"implement", "phase-1-impl", "pr_url"} {
			if _, ok := got.Artifacts[disallowed]; ok {
				t.Errorf("new run Artifacts must NOT carry %q", disallowed)
			}
		}
	})
}

// firstRegularFileIn returns the first regular-file path found inside `dir`
// (walking recursively). Used by carry-forward tests that know there is
// exactly one marker file per phase directory.
func firstRegularFileIn(t *testing.T, dir string) string {
	t.Helper()
	var found string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && found == "" {
			found = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if found == "" {
		t.Fatalf("no regular file under %s", dir)
	}
	return found
}

// stringSlicesEqual compares two string slices for equality.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Crash-recovery tests for RewindToPhase
// ---------------------------------------------------------------------------

// TestRewindToPhase_CrashBeforeCommitFlagCleared_CleanedUpOnStartup
// simulates a crashed rewind by hand-fabricating the on-disk state this path
// contracts against: run-001 sealed, run-002 skeleton with Committing:true,
// feature.yaml still at ActiveRun:1. Then: cleanup -> retry rewind -> assert
// the feature ends in a healthy post-rewind state. This is the end-to-end
// retry scenario from the roadmap's "re-triggering rewind from the UI
// succeeds" guarantee, exercised at the manager boundary.
func TestRewindToPhase_CrashBeforeCommitFlagCleared_CleanedUpOnStartup(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind crash-recovery regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("Crash Recovery", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Drive into a rewindable state.
	if err := mgr.StartInquire(f.ID); err != nil {
		t.Fatalf("StartInquire: %v", err)
	}
	if err := mgr.CompleteInquire(f.ID); err != nil {
		t.Fatalf("CompleteInquire: %v", err)
	}
	if err := mgr.StartResearch(f.ID); err != nil {
		t.Fatalf("StartResearch: %v", err)
	}
	if err := mgr.CompleteResearch(f.ID); err != nil {
		t.Fatalf("CompleteResearch: %v", err)
	}
	// Seed inquire/ dir on run-001 so the carry-forward copy has something
	// to carry.
	baseDir := mgr.Store.BaseDir
	run1Dir := filepath.Join(baseDir, f.ID, "runs", "run-001")
	inquireDir := filepath.Join(run1Dir, "inquire")
	if err := os.MkdirAll(inquireDir, 0o755); err != nil {
		t.Fatalf("mkdir inquire: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inquireDir, "marker.txt"), []byte("in"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Fabricate a crashed rewind on disk:
	//   1. Seal run-001 (set SealedAt, etc.).
	//   2. Write a run-002 skeleton with Committing:true (no carry-forward).
	//   3. Leave feature.yaml's ActiveRun at 1.
	run001Yaml := filepath.Join(run1Dir, "run.yaml")
	run1Loaded, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	now := time.Now()
	sealedTarget := feature.PhaseResearch
	run1Loaded.SealedAt = &now
	run1Loaded.SealReason = feature.SealReasonRewind
	run1Loaded.RewindTarget = &sealedTarget
	data, merr := marshalRun(run1Loaded)
	if merr != nil {
		t.Fatalf("marshal sealed run-001: %v", merr)
	}
	if err := os.WriteFile(run001Yaml, data, 0o644); err != nil {
		t.Fatalf("overwrite sealed run-001: %v", err)
	}
	run2Dir := filepath.Join(baseDir, f.ID, "runs", "run-002")
	if err := os.MkdirAll(run2Dir, 0o755); err != nil {
		t.Fatalf("mkdir run-002: %v", err)
	}
	skel := &feature.Run{
		RunNumber:      2,
		CarriedFromRun: 1,
		Committing:     true,
	}
	skelData, merr := marshalRun(skel)
	if merr != nil {
		t.Fatalf("marshal skeleton: %v", merr)
	}
	if err := os.WriteFile(filepath.Join(run2Dir, "run.yaml"), skelData, 0o644); err != nil {
		t.Fatalf("write skeleton: %v", err)
	}

	// Invoke cleanup directly (ScanRecovery would do this).
	deleted, cleanErr := mgr.Store.CleanupOrphanRuns(f.ID)
	if cleanErr != nil {
		t.Fatalf("CleanupOrphanRuns: %v", cleanErr)
	}
	if len(deleted) != 1 || deleted[0] != 2 {
		t.Errorf("deleted = %v, want [2]", deleted)
	}
	if _, err := os.Stat(run2Dir); !os.IsNotExist(err) {
		t.Fatalf("run-002 still exists after cleanup: %v", err)
	}

	// Re-trigger the rewind. Idempotent re-seal kicks in on run-001.
	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhaseResearch); err != nil {
		t.Fatalf("RewindToPhase after cleanup: %v", err)
	}
	got, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get after rewind: %v", err)
	}
	if got.ActiveRun != 2 || got.RunCount != 2 {
		t.Errorf("ActiveRun/RunCount = %d/%d, want 2/2", got.ActiveRun, got.RunCount)
	}

	// run-001 still sealed.
	run1Post, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1) post: %v", err)
	}
	if run1Post.SealedAt == nil {
		t.Error("run-001 SealedAt is nil after re-rewind (must be re-sealed)")
	}

	// run-002 committed (Committing:false) with expected CarriedPhases.
	run2Post, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2) post: %v", err)
	}
	if run2Post.Committing {
		t.Error("run-002 Committing=true after retry, want false")
	}
	if !containsPhase(run2Post.CarriedPhases, "inquire") {
		t.Errorf("run-002 CarriedPhases = %v, want contains 'inquire'", run2Post.CarriedPhases)
	}
	// Carry-forward copy succeeded.
	if _, err := os.Stat(filepath.Join(run2Dir, "inquire", "marker.txt")); err != nil {
		t.Errorf("run-002/inquire/marker.txt missing: %v (carry-forward must copy)", err)
	}
}

func TestRewindWithRequest_PartialCrashCleanupAllowsRetry(t *testing.T) {
	mgr := newTestManager(t)
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
	})
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.CurrentRoadmapPhase = 3
		ff.TotalRoadmapPhases = 3
		ff.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
			1: {
				"repo-a": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	mgr.Worktrees = mocks.NewMockWorktreeOperator()
	mgr.Branches = nil
	mgr.PRs = nil

	request := feature.RewindRequest{TargetPhase: feature.PhaseImplement, RoadmapPhase: 2}
	if _, _, err := mgr.RewindWithRequest(f.ID, request); err != nil {
		t.Fatalf("first RewindWithRequest: %v", err)
	}

	writeFeatureYAMLFromExternal(t, mgr.Store.BaseDir, f.ID, 1, 2)
	run2Dir := filepath.Join(mgr.Store.BaseDir, f.ID, "runs", "run-002")
	run2, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	run2.Committing = true
	data, err := marshalRun(run2)
	if err != nil {
		t.Fatalf("marshal run-002: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run2Dir, "run.yaml"), data, 0o644); err != nil {
		t.Fatalf("write committing run-002: %v", err)
	}

	deleted, err := mgr.Store.CleanupOrphanRuns(f.ID)
	if err != nil {
		t.Fatalf("CleanupOrphanRuns: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != 2 {
		t.Fatalf("deleted = %v, want [2]", deleted)
	}
	if _, err := os.Stat(run2Dir); !os.IsNotExist(err) {
		t.Fatalf("run-002 still exists after cleanup: %v", err)
	}

	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.PendingReviewPhase = nil
		ff.PendingRewindReviewRoadmapPhase = nil
		ff.IsRewind = false
		return nil
	}); err != nil {
		t.Fatalf("restore rewindable state: %v", err)
	}
	if _, _, err := mgr.RewindWithRequest(f.ID, request); err != nil {
		t.Fatalf("retry RewindWithRequest: %v", err)
	}

	got, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ActiveRun != 2 || got.RunCount != 2 {
		t.Fatalf("ActiveRun/RunCount = %d/%d, want 2/2", got.ActiveRun, got.RunCount)
	}
	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if sealedRun.RewindRoadmapPhase == nil || *sealedRun.RewindRoadmapPhase != 2 {
		t.Fatalf("sealed RewindRoadmapPhase = %v, want 2", sealedRun.RewindRoadmapPhase)
	}
	freshRun, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2) post retry: %v", err)
	}
	if freshRun.Committing {
		t.Fatal("run-002 Committing=true after retry, want false")
	}
	if freshRun.CurrentRoadmapPhase != 2 {
		t.Errorf("run-002 CurrentRoadmapPhase = %d, want 2", freshRun.CurrentRoadmapPhase)
	}
}

// TestRewindToPhase_ReSealAfterCleanup_IdempotentSealFields asserts that the
// manager's RewindToPhase can re-seal an already-sealed active run when
// cleanup has removed the orphan forward run. The re-seal must overwrite the
// old run's seal fields (RewindTarget in particular) with values from the
// second rewind.
func TestRewindToPhase_ReSealAfterCleanup_IdempotentSealFields(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	skipShortFeatureRegression(t, "extended rewind idempotent reseal regression")
	mgr := newTestManager(t)
	f, err := mgr.Create("Reseal Cleanup", "desc", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Drive to a rewindable state.
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		return nil
	})

	// First rewind: target = Inquire. After this, feature.yaml is at ActiveRun:2.
	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhaseInquire); err != nil {
		t.Fatalf("first rewind: %v", err)
	}
	mid, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get mid: %v", err)
	}
	if mid.ActiveRun != 2 {
		t.Fatalf("post first rewind ActiveRun = %d, want 2", mid.ActiveRun)
	}

	// Fabricate the "fork landed but bump didn't" state: rewrite feature.yaml
	// with ActiveRun:1, leaving run-001 sealed and run-002 on disk. The first
	// predicate (run_number > ActiveRun) catches run-002.
	writeFeatureYAMLFromExternal(t, mgr.Store.BaseDir, f.ID, 1, 2)
	// Cleanup: run-002 deleted (run_number 2 > ActiveRun 1); max_on_disk = 1;
	// feature.yaml rolled back to ActiveRun:1, RunCount:1.
	deleted, cleanErr := mgr.Store.CleanupOrphanRuns(f.ID)
	if cleanErr != nil {
		t.Fatalf("CleanupOrphanRuns: %v", cleanErr)
	}
	if len(deleted) != 1 || deleted[0] != 2 {
		t.Errorf("deleted = %v, want [2]", deleted)
	}
	// Reload to verify reconciliation landed in feature.yaml.
	postClean, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get post-clean: %v", err)
	}
	if postClean.ActiveRun != 1 || postClean.RunCount != 1 {
		t.Errorf("post-clean ActiveRun/RunCount = %d/%d, want 1/1", postClean.ActiveRun, postClean.RunCount)
	}
	if postClean.Run() == nil || !postClean.Run().IsSealed() {
		t.Fatal("post-clean active run is not sealed; want sealed run-001")
	}

	// The post-cleanup status is whatever the first rewind set (NeedsReview
	// for PhaseInquire → StatusPromptNeedsReview). Make sure it still allows
	// the next rewind target. Drive back into Implementing so the next rewind
	// target (PhaseResearch) is legal.
	_ = mgr.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.PendingReviewPhase = nil
		f.IsRewind = false
		return nil
	})

	// Second rewind: target = Research (different from first).
	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhaseResearch); err != nil {
		t.Fatalf("second rewind: %v", err)
	}

	// Run-001 re-sealed with the NEW RewindTarget.
	resealed, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1) resealed: %v", err)
	}
	if resealed.RewindTarget == nil || *resealed.RewindTarget != feature.PhaseResearch {
		t.Errorf("resealed RewindTarget = %v, want %v", resealed.RewindTarget, feature.PhaseResearch)
	}

	// feature.yaml bumped to ActiveRun:2.
	post, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get post-reseal: %v", err)
	}
	if post.ActiveRun != 2 || post.RunCount != 2 {
		t.Errorf("post-reseal ActiveRun/RunCount = %d/%d, want 2/2", post.ActiveRun, post.RunCount)
	}

	// run-002 exists with Committing:false.
	run2Post, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	if run2Post.Committing {
		t.Error("run-002 Committing=true, want false")
	}
	if run2Post.IsSealed() {
		t.Error("run-002 is sealed, want unsealed fresh run")
	}
}

// containsPhase reports whether sl contains a phase with DirName == want.
// Used by the manager-level crash tests to assert CarriedPhases membership
// in a way that matches the feature's on-disk convention.
func containsPhase(sl []string, want string) bool {
	for _, s := range sl {
		if s == want {
			return true
		}
	}
	return false
}

// marshalRun marshals a *feature.Run to YAML bytes. Used by crash-recovery
// tests that write run.yaml files directly to disk (bypassing
// Store.SaveRun's sealed-run panic).
func marshalRun(r *feature.Run) ([]byte, error) {
	return yaml.Marshal(r)
}

// writeFeatureYAMLFromExternal rewrites feature.yaml for the given feature
// with a fabricated (ActiveRun, RunCount) pair, bypassing Store.Save's
// shadow-sync side effects. Loads current feature.yaml first to preserve
// all other fields so the test state stays self-consistent. Used by the
// crash-recovery tests to fabricate states where ActiveRun has been
// "rolled back" relative to the on-disk run directories.
func writeFeatureYAMLFromExternal(t *testing.T, baseDir, id string, activeRun, runCount int) {
	t.Helper()
	path := filepath.Join(baseDir, id, "feature.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read feature.yaml: %v", err)
	}
	// Decode into a generic map so we keep every YAML field (including any
	// fields the external test package cannot reference directly), then
	// overwrite just ActiveRun / RunCount.
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal feature.yaml: %v", err)
	}
	raw["active_run"] = activeRun
	raw["run_count"] = runCount
	out, err := yaml.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal feature.yaml: %v", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write feature.yaml: %v", err)
	}
}
