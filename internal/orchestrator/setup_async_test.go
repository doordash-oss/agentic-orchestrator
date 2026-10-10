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

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func activeAsyncSetupChild(t *testing.T) *feature.Feature {
	t.Helper()
	child := &feature.Feature{
		ID:        "child-async",
		Status:    feature.StatusSettingUpWorktrees,
		Pipeline:  feature.PipelineMedium,
		Parent:    &feature.ChildRelationship{ParentID: "parent-1", Kind: feature.ChildKindRefactor},
		Repos:     []feature.FeatureRepo{{Name: "repo", Path: t.TempDir(), BaseBranch: "main"}},
		ActiveRun: 1,
		RunCount:  1,
	}
	child.Run().Setup = &feature.SetupState{
		Status:  feature.SetupStatusRunning,
		Attempt: 1,
		Tasks: map[string]feature.SetupTask{
			"worktree:repo": {Key: "worktree:repo", Kind: feature.SetupTaskWorktree, Repo: "repo", Status: feature.SetupStatusQueued},
		},
		TaskOrder: []string{"worktree:repo"},
	}
	return child
}

func setupFailureEvents(o *orchestrator.Orchestrator) []ports.Event {
	var failures []ports.Event
	for {
		select {
		case ev := <-o.Events():
			if ev.Type == ports.SetupFailed {
				failures = append(failures, ev)
			}
		default:
			return failures
		}
	}
}

func TestRunSetupAsyncRecordsAndSignalsEarlySetupFailure(t *testing.T) {
	child := activeAsyncSetupChild(t)
	store, manager := newStoredFeatureFixture(t, child)
	earlyErr := errors.New("persisting initial setup transition failed")
	rejected := false
	store.SetSaveInterceptor(func(*feature.Feature) error {
		if !rejected {
			rejected = true
			return earlyErr
		}
		return nil
	})
	defer store.SetSaveInterceptor(nil)
	o := orchestrator.New(orchestrator.Deps{Manager: manager, Store: store}, orchestrator.Hooks{})
	t.Cleanup(func() { _ = o.Shutdown() })

	o.RunSetupAsync(child.ID)
	o.WaitForCycles()
	if !rejected {
		t.Fatal("initial setup write was not attempted")
	}
	stored := loadStoredFeature(t, store, child.ID)
	if stored.Status != feature.StatusFailed || stored.Run().Setup.Status != feature.SetupStatusFailed {
		t.Fatalf("stored status/setup = %s/%s, want Failed/failed", stored.Status, stored.Run().Setup.Status)
	}
	failures := setupFailureEvents(o)
	if len(failures) != 1 {
		t.Fatalf("SetupFailed events = %d, want 1", len(failures))
	}
	ev := failures[0]
	if ev.ParentID != "parent-1" || ev.ChildID != child.ID {
		t.Errorf("relationship = %q/%q, want parent-1/%s", ev.ParentID, ev.ChildID, child.ID)
	}
	if ev.Error == nil || !strings.Contains(ev.Error.Error(), earlyErr.Error()) {
		t.Errorf("event error = %v, want %v", ev.Error, earlyErr)
	}
	if ev.CanonicalError == nil || ev.CanonicalError.Code != errcat.SetupInterrupted {
		t.Errorf("canonical error = %+v, want setup_interrupted", ev.CanonicalError)
	}
}

func TestRunSetupAsyncDoesNotDoubleSignalRunnerRecordedFailure(t *testing.T) {
	child := activeAsyncSetupChild(t)
	store, manager := newStoredFeatureFixture(t, child)
	worktrees := mocks.NewMockWorktreeOps()
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		return "", errors.New("git worktree add failed")
	}
	manager.Worktrees = worktrees
	o := orchestrator.New(orchestrator.Deps{Manager: manager, Store: store, Worktrees: worktrees}, orchestrator.Hooks{})
	t.Cleanup(func() { _ = o.Shutdown() })

	o.RunSetupAsync(child.ID)
	o.WaitForCycles()
	stored := loadStoredFeature(t, store, child.ID)
	if stored.Status != feature.StatusFailed || stored.FailureCode() != errcat.WorktreeSetupFailed {
		t.Fatalf("stored status/failure = %s/%s, want Failed/worktree_setup_failed", stored.Status, stored.FailureCode())
	}
	failures := setupFailureEvents(o)
	if len(failures) != 1 {
		t.Fatalf("SetupFailed events = %d, want 1", len(failures))
	}
	if failures[0].CanonicalError == nil || failures[0].CanonicalError.Code != errcat.WorktreeSetupFailed {
		t.Errorf("canonical error = %+v, want worktree_setup_failed", failures[0].CanonicalError)
	}
}
