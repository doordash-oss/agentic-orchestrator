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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// childWithSetup returns a child whose durable setup is in the given state.
func childWithSetup(setupStatus feature.SetupStatus) *feature.Feature {
	f := &feature.Feature{
		ID:        "child-async",
		Status:    feature.StatusSettingUpWorktrees,
		Pipeline:  feature.PipelineMedium,
		Parent:    &feature.ChildRelationship{ParentID: "parent-1", Kind: feature.ChildKindRefactor},
		ActiveRun: 1,
		RunCount:  1,
	}
	f.Run().Setup = &feature.SetupState{
		Status:        setupStatus,
		Attempt:       1,
		LatestLogPath: "/logs/attempt-01-output.txt",
	}
	if setupStatus == feature.SetupStatusFailed {
		f.Status = feature.StatusFailed
		f.Run().Failure = &errcat.FailureRecord{Code: errcat.WorktreeSetupFailed}
	}
	return f
}

// TestRunSetupAsyncRecordsAndSignalsEarlySetupFailure pins the orchestrator's
// ownership of asynchronous setup: an early RunSetup error that returns
// before the runner could durably fail the setup or emit a failure event
// must still leave the child durably retryable (FailActiveSetup) and produce
// exactly one parent-correlated SetupFailed signal carrying the canonical
// error of the record FailActiveSetup stored.
func TestRunSetupAsyncRecordsAndSignalsEarlySetupFailure(t *testing.T) {
	earlyErr := errors.New("persisting initial setup transition failed")
	lc := lifecycleForFeature(childWithSetup(feature.SetupStatusRunning))
	lc.RunSetupFn = func(string, ...feature.SetupRunnerOptions) error { return earlyErr }
	failed := make(chan string, 1)
	lc.FailActiveSetupFn = func(featureID, message string) (feature.SetupFailureOutcome, error) {
		if featureID != "child-async" {
			t.Errorf("FailActiveSetup featureID = %q, want child-async", featureID)
		}
		failed <- message
		return feature.SetupFailureOutcome{
			Marked:     true,
			Owner:      feature.SetupTask{Key: "worktree:repo", Kind: feature.SetupTaskWorktree, Label: "Worktree: repo"},
			TaskRecord: &errcat.FailureRecord{Code: errcat.SetupInterrupted, Diagnostics: message},
			RunRecord:  &errcat.FailureRecord{Code: errcat.SetupInterrupted},
		}, nil
	}
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore()}, orchestrator.Hooks{})
	t.Cleanup(func() { _ = o.Shutdown() })

	o.RunSetupAsync("child-async")
	o.WaitForCycles()

	select {
	case msg := <-failed:
		if !strings.Contains(msg, earlyErr.Error()) {
			t.Fatalf("FailActiveSetup message = %q, want original error %q", msg, earlyErr)
		}
	default:
		t.Fatal("FailActiveSetup was not called; early setup error left no durable record")
	}

	var failure *ports.Event
	for {
		select {
		case ev := <-o.Events():
			if ev.Type == ports.SetupFailed {
				if failure != nil {
					t.Fatalf("double SetupFailed emission: %+v then %+v", *failure, ev)
				}
				e := ev
				failure = &e
			}
		default:
			goto drained
		}
	}
drained:
	if failure == nil {
		t.Fatal("no SetupFailed event emitted for the early setup error")
	}
	if failure.FeatureID != "child-async" {
		t.Errorf("SetupFailed FeatureID = %q, want child-async", failure.FeatureID)
	}
	if failure.ParentID != "parent-1" || failure.ChildID != "child-async" {
		t.Errorf("SetupFailed relationship = parent %q child %q, want parent-1/child-async", failure.ParentID, failure.ChildID)
	}
	if failure.Error == nil || !strings.Contains(failure.Error.Error(), earlyErr.Error()) {
		t.Errorf("SetupFailed Error = %v, want the terminal setup error", failure.Error)
	}
	if failure.CanonicalError == nil || failure.CanonicalError.Code != errcat.SetupInterrupted {
		t.Errorf("SetupFailed CanonicalError = %+v, want the setup_interrupted record of the parked setup", failure.CanonicalError)
	}
}

// TestRunSetupAsyncDoesNotDoubleSignalRunnerRecordedFailure pins that when
// RunSetup already durably failed the setup (and emitted its own task-level
// SetupFailed carrying the task's stored record), the async wrapper neither
// clobbers the record nor emits a second failure signal.
func TestRunSetupAsyncDoesNotDoubleSignalRunnerRecordedFailure(t *testing.T) {
	lc := lifecycleForFeature(childWithSetup(feature.SetupStatusFailed))
	taskRecord := &errcat.FailureRecord{
		Code:        errcat.WorktreeSetupFailed,
		Context:     &errcat.RecordContext{Repositories: []errcat.CodeRepository{{Name: "repo"}}},
		Diagnostics: "git worktree add failed",
	}
	lc.RunSetupFn = func(_ string, opts ...feature.SetupRunnerOptions) error {
		opts[0].OnEvent(feature.SetupEvent{
			Kind:      feature.SetupEventFailed,
			FeatureID: "child-async",
			TaskKey:   "worktree:repo",
			Error:     "git worktree add failed",
			Failure:   taskRecord,
		})
		return errors.New("worktree task failed")
	}
	lc.FailActiveSetupFn = func(string, string) (feature.SetupFailureOutcome, error) {
		return feature.SetupFailureOutcome{}, nil
	}
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore()}, orchestrator.Hooks{})
	t.Cleanup(func() { _ = o.Shutdown() })

	o.RunSetupAsync("child-async")
	o.WaitForCycles()

	var failure *ports.Event
	for {
		select {
		case ev := <-o.Events():
			if ev.Type == ports.SetupFailed {
				if failure != nil {
					t.Fatalf("duplicate SetupFailed emitted after runner-recorded failure: %+v", ev)
				}
				e := ev
				failure = &e
			}
		default:
			if failure == nil {
				t.Fatal("runner-recorded SetupFailed event was not emitted")
			}
			if failure.CanonicalError == nil || failure.CanonicalError.Code != errcat.WorktreeSetupFailed ||
				!strings.Contains(failure.CanonicalError.Summary, "repo") {
				t.Fatalf("SetupFailed CanonicalError = %+v, want the failed task's record rendered through the catalog", failure.CanonicalError)
			}
			return
		}
	}
}

// TestRunSetupAsyncSignalsPostSetupFailure pins that a RunSetup error
// arriving after setup already completed (a post-setup start failure) is not
// mistaken for a runner-recorded failure: the wrapper still emits the
// correlated failure signal instead of dropping it.
func TestRunSetupAsyncSignalsPostSetupFailure(t *testing.T) {
	startErr := errors.New("pipeline start failed after setup")
	done := childWithSetup(feature.SetupStatusDone)
	done.Status = feature.StatusCreated
	lc := lifecycleForFeature(done)
	lc.RunSetupFn = func(string, ...feature.SetupRunnerOptions) error { return startErr }
	lc.FailActiveSetupFn = func(string, string) (feature.SetupFailureOutcome, error) {
		return feature.SetupFailureOutcome{}, nil
	}
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore()}, orchestrator.Hooks{})
	t.Cleanup(func() { _ = o.Shutdown() })

	o.RunSetupAsync("child-async")
	o.WaitForCycles()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-o.Events():
			if ev.Type == ports.SetupFailed {
				if ev.ParentID != "parent-1" || ev.ChildID != "child-async" {
					t.Fatalf("SetupFailed relationship = parent %q child %q, want parent-1/child-async", ev.ParentID, ev.ChildID)
				}
				return
			}
		case <-deadline:
			t.Fatal("post-setup failure produced no SetupFailed signal")
		}
	}
}
