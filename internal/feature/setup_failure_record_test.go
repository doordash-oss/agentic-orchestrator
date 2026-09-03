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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// TestManagerSetupFailureStoresCanonicalRecord pins the durable shape of a
// failing setup task: the task carries exactly one worktree_setup_failed
// record whose repositories block names the failing repo with its branch,
// whose command block points at the setup log, and whose diagnostics hold
// the raw error text; the run carries a thin record with the same code whose
// only context is the setup_task block naming the task and which carries no
// diagnostics; the setup aggregate carries no error text at all.
func TestManagerSetupFailureStoresCanonicalRecord(t *testing.T) {
	mgr := newTestManager(t)
	mgr.Worktrees = &mocks.MockWorktreeOps{
		CreateFn: func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
			return "", errors.New("git worktree add failed: branch exists")
		},
	}

	f, err := mgr.Create("Setup Failure Record", "canonical record", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RunSetup(f.ID); err == nil {
		t.Fatal("RunSetup succeeded, want worktree failure")
	}

	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Status != feature.StatusFailed {
		t.Fatalf("Status = %s, want Failed", loaded.Status)
	}
	record := loaded.FailureRecord()
	if record == nil {
		t.Fatal("FailureRecord = nil, want one thin worktree_setup_failed record")
	}
	if record.Code != errcat.WorktreeSetupFailed {
		t.Fatalf("record code = %q, want %q", record.Code, errcat.WorktreeSetupFailed)
	}
	if record.Context == nil || record.Context.SetupTask == nil || record.Context.SetupTask.Key != "worktree:test-repo" {
		t.Fatalf("record context = %+v, want only the setup_task block naming the task", record.Context)
	}
	if record.Context.Repositories != nil || record.Context.Command != nil {
		t.Fatalf("run record context = %+v, want no repository or command blocks", record.Context)
	}
	if record.Diagnostics != "" {
		t.Fatalf("run record diagnostics = %q, want none on the thin record", record.Diagnostics)
	}

	setup := loaded.Run().Setup
	if setup == nil || setup.Status != feature.SetupStatusFailed {
		t.Fatalf("setup = %+v, want failed aggregate", setup)
	}
	task := setup.Tasks["worktree:test-repo"]
	if task.Status != feature.SetupStatusFailed {
		t.Fatalf("task = %+v, want failed task", task)
	}
	if task.Error == nil || task.Error.Code != errcat.WorktreeSetupFailed {
		t.Fatalf("task record = %+v, want worktree_setup_failed", task.Error)
	}
	if !strings.Contains(task.Error.Diagnostics, "git worktree add failed: branch exists") {
		t.Fatalf("task record diagnostics = %q, want the raw error text", task.Error.Diagnostics)
	}
	if task.Error.Context == nil || len(task.Error.Context.Repositories) != 1 ||
		task.Error.Context.Repositories[0].Name != "test-repo" || task.Error.Context.Repositories[0].Branch == "" {
		t.Fatalf("task record repositories = %+v, want [test-repo] with its branch", task.Error.Context)
	}
	if task.Error.Context.Command == nil || len(task.Error.Context.Command.LogPaths) != 1 || task.Error.Context.Command.LogPaths[0] != setup.LatestLogPath {
		t.Fatalf("task record command block = %+v, want the setup log path %q", task.Error.Context.Command, setup.LatestLogPath)
	}

	if owner := loaded.FailedSetupTask(); owner == nil || owner.Key != "worktree:test-repo" {
		t.Fatalf("FailedSetupTask = %+v, want the owning worktree task", owner)
	}
}

// TestManagerRetrySetupClearsFailureRecord pins that setup retry and setup
// completion clear the run's stored failure record: a parked setup failure is
// retryable, and once the retried attempt completes the feature is left with
// no failure record at all.
func TestManagerRetrySetupClearsFailureRecord(t *testing.T) {
	mgr := newTestManager(t)
	wtDir := t.TempDir()
	fail := true
	mgr.Worktrees = &mocks.MockWorktreeOps{
		CreateFn: func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
			if fail {
				return "", errors.New("git worktree add failed: branch exists")
			}
			return filepath.Join(wtDir, featureSlug, repoName), nil
		},
	}

	f, err := mgr.Create("Setup Retry Record", "retry clears record", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RunSetup(f.ID); err == nil {
		t.Fatal("initial RunSetup succeeded, want worktree failure")
	}
	failed, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get failed feature: %v", err)
	}
	if failed.FailureCode() != errcat.WorktreeSetupFailed {
		t.Fatalf("FailureCode = %q, want %q", failed.FailureCode(), errcat.WorktreeSetupFailed)
	}

	fail = false
	if err := mgr.RetrySetup(f.ID); err != nil {
		t.Fatalf("retry setup: %v", err)
	}
	reloaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get reloaded feature: %v", err)
	}
	if reloaded.Status != feature.StatusCreated || reloaded.Run().Setup.Status != feature.SetupStatusDone {
		t.Fatalf("status/setup = %s/%s, want Created/done", reloaded.Status, reloaded.Run().Setup.Status)
	}
	if reloaded.FailureRecord() != nil {
		t.Fatalf("failure record = %+v, want cleared by setup retry and completion", reloaded.FailureRecord())
	}
	if task := reloaded.Run().Setup.Tasks["worktree:test-repo"]; task.Error != nil {
		t.Fatalf("task record = %+v, want cleared by setup retry and completion", task.Error)
	}
}

// TestManagerFailActiveSetupAbandonedBetweenTasks pins the ownerless-failure
// contract: a setup abandoned between tasks (nothing running) parks the first
// unfinished task in task order as the failed owner of a setup_interrupted
// record, with the run's thin record pointing at it.
func TestManagerFailActiveSetupAbandonedBetweenTasks(t *testing.T) {
	mgr := newTestManager(t)
	f, err := mgr.Create("Abandoned Between Tasks", "crash window", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", []string{filepath.Join(t.TempDir(), "missing.png")}, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		setup := ff.Run().Setup
		worktree := setup.Tasks["worktree:test-repo"]
		worktree.Status = feature.SetupStatusDone
		setup.Tasks[worktree.Key] = worktree
		return nil
	}); err != nil {
		t.Fatalf("seed done worktree: %v", err)
	}

	outcome, err := mgr.FailActiveSetup(f.ID, "setup was interrupted by shutdown or crash; retry setup to continue")
	if err != nil {
		t.Fatalf("FailActiveSetup: %v", err)
	}
	if !outcome.Marked {
		t.Fatal("FailActiveSetup marked = false, want true for a running setup")
	}
	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Status != feature.StatusFailed || loaded.FailureCode() != errcat.SetupInterrupted {
		t.Fatalf("status/failure = %s/%s, want Failed/%s", loaded.Status, loaded.FailureCode(), errcat.SetupInterrupted)
	}
	owner := loaded.FailedSetupTask()
	if owner == nil || owner.Key != "image:1" {
		t.Fatalf("owning task = %+v, want the first unfinished task image:1", owner)
	}
	if owner.Status != feature.SetupStatusFailed || owner.Error == nil || owner.Error.Code != errcat.SetupInterrupted {
		t.Fatalf("owning task = %+v, want failed with a setup_interrupted record", owner)
	}
	if worktree := loaded.Run().Setup.Tasks["worktree:test-repo"]; worktree.Status != feature.SetupStatusDone || worktree.Error != nil {
		t.Fatalf("done worktree task = %+v, want untouched by the failure", worktree)
	}
}

// TestManagerRunSetupLogDirectoryFailureOwnsFirstUnfinishedTask pins the
// pre-task failure contract: a setup-log creation failure before any task
// started marks the first unfinished task failed with a setup_interrupted
// record and points the run at it.
func TestManagerRunSetupLogDirectoryFailureOwnsFirstUnfinishedTask(t *testing.T) {
	mgr := newTestManager(t)
	f, err := mgr.Create("Log Dir Failure", "blocked setup dir", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	setupDir := filepath.Join(mgr.Store.RunDir(f.ID, 1), "setup")
	if err := os.WriteFile(setupDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("block setup dir: %v", err)
	}

	if err := mgr.RunSetup(f.ID); err == nil {
		t.Fatal("RunSetup succeeded, want log directory failure")
	}
	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Status != feature.StatusFailed || loaded.FailureCode() != errcat.SetupInterrupted {
		t.Fatalf("status/failure = %s/%s, want Failed/%s", loaded.Status, loaded.FailureCode(), errcat.SetupInterrupted)
	}
	owner := loaded.FailedSetupTask()
	if owner == nil || owner.Key != "worktree:test-repo" {
		t.Fatalf("owning task = %+v, want the first unfinished task worktree:test-repo", owner)
	}
	if owner.Status != feature.SetupStatusFailed || owner.Error == nil || owner.Error.Code != errcat.SetupInterrupted {
		t.Fatalf("owning task = %+v, want failed with a setup_interrupted record", owner)
	}
}

// TestManagerRetryPhaseClearsFailureRecord pins that the retry-phase mutation
// clears the run's stored failure record so the unified phase loop starts
// clean.
func TestManagerRetryPhaseClearsFailureRecord(t *testing.T) {
	mgr := newTestManager(t)
	f, err := mgr.Create("Retry Phase Record", "retry phase clears record", []string{"test-repo"}, mgr.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	record := errcat.FailureRecord{Code: errcat.InfrastructureFailure, Diagnostics: "phase runner crashed"}
	if err := mgr.MarkFailed(f.ID, record); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	failed, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get failed feature: %v", err)
	}
	if failed.FailureCode() != errcat.InfrastructureFailure {
		t.Fatalf("FailureCode = %q, want %q", failed.FailureCode(), errcat.InfrastructureFailure)
	}

	if err := mgr.RetryPhase(f.ID, []string{"test-repo"}); err != nil {
		t.Fatalf("retry phase: %v", err)
	}
	loaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get reloaded feature: %v", err)
	}
	if loaded.FailureRecord() != nil {
		t.Fatalf("failure record = %+v, want cleared by RetryPhase", loaded.FailureRecord())
	}
}
