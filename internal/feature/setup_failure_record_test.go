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
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// TestManagerSetupFailureStoresCanonicalRecord pins the durable shape of a
// failing setup task: the run carries exactly one worktree_setup_failed
// record whose repositories block names the failing repo, whose diagnostics
// hold the raw error text, and whose command block points at the setup log;
// the same raw text stays on the setup aggregate and task LastError strings.
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
		t.Fatal("FailureRecord = nil, want one worktree_setup_failed record")
	}
	if record.Code != errcat.WorktreeSetupFailed {
		t.Fatalf("record code = %q, want %q", record.Code, errcat.WorktreeSetupFailed)
	}
	if record.Context == nil || len(record.Context.Repositories) != 1 || record.Context.Repositories[0].Name != "test-repo" {
		t.Fatalf("record repositories = %+v, want exactly [test-repo]", record.Context)
	}

	setup := loaded.Run().Setup
	if setup == nil || setup.Status != feature.SetupStatusFailed {
		t.Fatalf("setup = %+v, want failed aggregate", setup)
	}
	if record.Diagnostics != setup.LastError {
		t.Fatalf("record diagnostics = %q, want the setup aggregate's raw error %q", record.Diagnostics, setup.LastError)
	}
	task := setup.Tasks["worktree:test-repo"]
	if task.Status != feature.SetupStatusFailed || task.LastError != record.Diagnostics {
		t.Fatalf("task = %+v, want failed task carrying the same raw error %q", task, record.Diagnostics)
	}
	if record.Context.Command == nil || len(record.Context.Command.LogPaths) != 1 || record.Context.Command.LogPaths[0] != setup.LatestLogPath {
		t.Fatalf("record command block = %+v, want the setup log path %q", record.Context.Command, setup.LatestLogPath)
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
