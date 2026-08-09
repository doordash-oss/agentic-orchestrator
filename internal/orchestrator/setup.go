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
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type setupRunner interface {
	RunSetup(featureID string, opts ...feature.SetupRunnerOptions) error
	RetrySetup(featureID string, opts ...feature.SetupRunnerOptions) error
}

// activeSetupFailer is the lifecycle capability an async setup failure
// handler uses to park a still-running setup durably (see
// feature.Manager.FailActiveSetup).
type activeSetupFailer interface {
	FailActiveSetup(featureID, message string) (marked bool, err error)
}

// RunSetupAsync runs RunSetup in an orchestrator-owned goroutine tracked by
// WaitForCycles, so asynchronous setup (currently the refactor-child launch
// path) always has an owner: a terminal RunSetup error is both recorded
// durably and emitted as a correlated failure signal, and emit paths select
// on shutdown. A detached "go RunSetup()" can return before the runner
// durably marks setup failed or emits a failure event (reload, transition
// persistence, or completion failures), silently stranding the feature in
// SettingUpWorktrees; this method is the only sanctioned async entry point.
func (o *Orchestrator) RunSetupAsync(featureID string) {
	o.cycleWG.Add(1)
	go func() {
		defer o.cycleWG.Done()
		if err := o.RunSetup(featureID); err != nil {
			o.recordAsyncSetupFailure(featureID, err)
		}
	}()
}

// recordAsyncSetupFailure owns every terminal setup error the runner did not
// already record: it durably marks any still-running setup failed (making the
// feature retryable) and emits a blocking SetupFailed signal with the same
// parent correlation as runner-emitted setup events. When the runner already
// failed the setup durably, its task-level SetupFailed event is the record
// and nothing more is emitted.
func (o *Orchestrator) recordAsyncSetupFailure(featureID string, setupErr error) {
	msg := setupErr.Error()
	if failer, ok := o.deps.Lifecycle.(activeSetupFailer); ok {
		marked, err := failer.FailActiveSetup(featureID, msg)
		if err != nil {
			msg = fmt.Sprintf("%s (recording the setup failure durably also failed: %v)", msg, err)
		} else if !marked && o.setupFailureAlreadyRecorded(featureID) {
			// The runner durably failed the setup and already emitted a
			// task-level SetupFailed; emitting again would double the signal.
			return
		}
	}
	ev := feature.SetupEvent{Kind: feature.SetupEventFailed, FeatureID: featureID, Error: msg}
	if f, err := o.deps.Lifecycle.Get(featureID); err == nil && f.Run() != nil {
		ev.RunNumber = f.ActiveRun
		if setup := f.Run().Setup; setup != nil {
			ev.Attempt = setup.Attempt
			ev.LogPath = setup.LatestLogPath
		}
	}
	o.emitSetupEvent(ev)
}

func (o *Orchestrator) RunSetup(featureID string) error {
	if err := o.runSetupWith(false, featureID); err != nil {
		return err
	}
	return o.startFeatureAfterSetup(featureID)
}

func (o *Orchestrator) RetrySetup(featureID string) error {
	if err := o.runSetupWith(true, featureID); err != nil {
		return err
	}
	return o.startFeatureAfterSetup(featureID)
}

// startFeatureAfterSetup continues into the pipeline once setup completes —
// except for child features, which stay parked at the durable Created status
// after setup completes (initial or retried). Starting a child is an
// explicit action routed through StartFeature and its capability gate.
func (o *Orchestrator) startFeatureAfterSetup(featureID string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("loading feature after setup: %w", err)
	}
	if f.IsChild() {
		return nil
	}
	return o.StartFeature(featureID)
}

// RunSetupOnly runs the queued durable setup without starting orchestration:
// on success the feature returns to StatusCreated, a startable
// pre-orchestration state where the action catalogue enables Start but no
// planning or provider session has begun. Setup progress and failure are
// still emitted through the standard setup events.
func (o *Orchestrator) RunSetupOnly(featureID string) error {
	return o.runSetupWith(false, featureID)
}

// RetrySetupOnly reruns only the unfinished setup tasks of a failed setup,
// preserving completed task state, without starting orchestration. On
// success the feature reaches the same startable StatusCreated state as
// RunSetupOnly.
func (o *Orchestrator) RetrySetupOnly(featureID string) error {
	return o.runSetupWith(true, featureID)
}

func (o *Orchestrator) runSetupWith(retry bool, featureID string) error {
	runner, ok := o.deps.Lifecycle.(setupRunner)
	if !ok {
		return fmt.Errorf("setup runner not configured")
	}
	opt := feature.SetupRunnerOptions{
		OnEvent: func(ev feature.SetupEvent) {
			o.emitSetupEvent(ev)
		},
	}
	if retry {
		return runner.RetrySetup(featureID, opt)
	}
	return runner.RunSetup(featureID, opt)
}

// setupFailureAlreadyRecorded reports whether the durable record shows a
// runner-recorded setup failure (as opposed to a post-setup error, which has
// no durable failure yet and must still be signalled).
func (o *Orchestrator) setupFailureAlreadyRecorded(featureID string) bool {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil || f.Run() == nil {
		return false
	}
	setup := f.Run().Setup
	return f.FailureType == feature.FailureWorktreeSetup && setup != nil && setup.Status == feature.SetupStatusFailed
}

func (o *Orchestrator) emitSetupEvent(ev feature.SetupEvent) {
	if o.hooks.OnSetupEvent != nil {
		o.hooks.OnSetupEvent(ev)
	}
	pe := setupPortsEvent(ev)
	// Setup events for a child feature carry both ids so consumers can
	// correlate both projections; the lookup is cheap at setup-event rates.
	// The parent's persisted lifecycle is never touched.
	if f, err := o.deps.Lifecycle.Get(ev.FeatureID); err == nil && f.IsChild() {
		pe.ParentID = f.Parent.ParentID
		pe.ChildID = f.ID
	}
	if pe.Type == ports.SetupFailed {
		o.emitEventBlocking(pe)
		return
	}
	o.emitEvent(pe)
}

func setupPortsEvent(ev feature.SetupEvent) ports.Event {
	eventType := ports.SetupProgress
	switch ev.Kind {
	case feature.SetupEventStarted:
		eventType = ports.SetupStarted
	case feature.SetupEventCompleted:
		eventType = ports.SetupCompleted
	case feature.SetupEventFailed:
		eventType = ports.SetupFailed
	}
	return ports.Event{
		Type:        eventType,
		FeatureID:   ev.FeatureID,
		RunNumber:   ev.RunNumber,
		Attempt:     ev.Attempt,
		SetupLog:    ev.LogPath,
		SetupTask:   ev.TaskKey,
		SetupKind:   ev.TaskKind,
		SetupStatus: ev.TaskStatus,
		RepoName:    ev.Repo,
		Path:        ev.Path,
		Branch:      ev.Branch,
		Message:     string(ev.Kind),
		Error:       setupEventError(ev),
	}
}

func setupEventError(ev feature.SetupEvent) error {
	if ev.Error == "" {
		return nil
	}
	return fmt.Errorf("%s", ev.Error)
}
