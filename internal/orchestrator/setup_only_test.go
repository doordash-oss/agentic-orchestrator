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

package orchestrator

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

const setupOnlyRepoA = "repo-a"
const setupOnlyRepoB = "repo-b"

func newSetupOnlyFixture(t *testing.T) (*feature.Store, *feature.Manager, *mocks.MockWorktreeOps, string) {
	t.Helper()
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos[setupOnlyRepoA] = config.RepoConfig{Path: filepath.Join(runtimeDir, setupOnlyRepoA)}
	cfg.Repos[setupOnlyRepoB] = config.RepoConfig{Path: filepath.Join(runtimeDir, setupOnlyRepoB)}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	worktrees := mocks.NewMockWorktreeOps()
	manager.Worktrees = worktrees
	return store, manager, worktrees, runtimeDir
}

func countWorktreeCreates(worktrees *mocks.MockWorktreeOps) int {
	count := 0
	for _, call := range worktrees.Calls {
		if call.Method == "Create" {
			count++
		}
	}
	return count
}

func TestRunSetupOnlyLeavesFeatureStartableWithoutStarting(t *testing.T) {
	store, manager, worktrees, runtimeDir := newSetupOnlyFixture(t)
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		return filepath.Join(runtimeDir, "worktrees", featureSlug, repoName), nil
	}
	f, err := manager.Create("Setup only", "desc", []string{setupOnlyRepoA}, config.NewDefault().Defaults.Models, "", "", nil, feature.CreateOptions{
		QueueSetup: true,
		Pipeline:   feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}

	started := 0
	orch := New(Deps{Lifecycle: manager, Store: store}, Hooks{
		OnFeatureStarted: func(string) { started++ },
	})

	if err := orch.RunSetupOnly(f.ID); err != nil {
		t.Fatalf("RunSetupOnly() error = %v", err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if updated.Status != feature.StatusCreated {
		t.Fatalf("status = %s; want Created (startable, not started)", updated.Status)
	}
	setup := updated.Run().Setup
	if setup == nil || setup.Status != feature.SetupStatusDone {
		t.Fatalf("setup = %+v; want done", setup)
	}
	if started != 0 {
		t.Fatalf("OnFeatureStarted fired %d times; want 0 for setup-only run", started)
	}
}

func TestRetrySetupOnlyRerunsOnlyUnfinishedTasksWithoutStarting(t *testing.T) {
	store, manager, worktrees, runtimeDir := newSetupOnlyFixture(t)
	failRepoB := true
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		if repoName == setupOnlyRepoB && failRepoB {
			return "", errors.New("transient checkout failure")
		}
		return filepath.Join(runtimeDir, "worktrees", featureSlug, repoName), nil
	}
	f, err := manager.Create("Retry setup only", "desc", []string{setupOnlyRepoA, setupOnlyRepoB}, config.NewDefault().Defaults.Models, "", "", nil, feature.CreateOptions{
		QueueSetup: true,
		Pipeline:   feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}

	started := 0
	orch := New(Deps{Lifecycle: manager, Store: store}, Hooks{
		OnFeatureStarted: func(string) { started++ },
	})

	if err := orch.RunSetupOnly(f.ID); err == nil {
		t.Fatal("RunSetupOnly() error = nil; want initial failure for repo-b")
	}
	failed, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load failed feature: %v", err)
	}
	if failed.Status != feature.StatusFailed || failed.FailureType != feature.FailureWorktreeSetup {
		t.Fatalf("failed feature = %s/%s; want Failed/worktree_setup", failed.Status, failed.FailureType)
	}
	createsAfterFirstRun := countWorktreeCreates(worktrees)

	failRepoB = false
	if err := orch.RetrySetupOnly(f.ID); err != nil {
		t.Fatalf("RetrySetupOnly() error = %v", err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load retried feature: %v", err)
	}
	if updated.Status != feature.StatusCreated {
		t.Fatalf("status = %s; want Created after setup retry without start", updated.Status)
	}
	setup := updated.Run().Setup
	if setup == nil || setup.Status != feature.SetupStatusDone || setup.Attempt != 2 {
		t.Fatalf("setup = %+v; want done on attempt 2", setup)
	}
	// Retry must rerun only the unfinished repo-b task, not redo repo-a.
	if got := countWorktreeCreates(worktrees) - createsAfterFirstRun; got != 1 {
		t.Fatalf("retry worktree creates = %d; want 1 (only the failed task)", got)
	}
	if started != 0 {
		t.Fatalf("OnFeatureStarted fired %d times; want 0 for setup-only retry", started)
	}
}
