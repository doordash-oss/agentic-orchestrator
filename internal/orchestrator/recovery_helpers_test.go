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
	"context"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// ---------------------------------------------------------------------------
// fakeRecoveryOp — records calls, returns pre-seeded items / error.
// Satisfies ports.RecoveryOperator.
// ---------------------------------------------------------------------------

type fakeRecoveryOp struct {
	mu sync.Mutex

	// ScanFn lets tests inject scan behavior.
	ScanFn func(ctx context.Context) ([]ports.RecoveryItem, error)
	// ExecFn lets tests inject execute behavior.
	ExecFn func(ctx context.Context, items []ports.RecoveryItem, actions map[string]ports.RecoveryAction) error

	// Call logs.
	ScanCalls int
	ExecCalls []fakeExecCall

	// Seeded values used when Fn overrides are nil.
	Items   []ports.RecoveryItem
	ScanErr error
	ExecErr error
}

type fakeExecCall struct {
	Items   []ports.RecoveryItem
	Actions map[string]ports.RecoveryAction
}

func (f *fakeRecoveryOp) ScanForRecovery(ctx context.Context) ([]ports.RecoveryItem, error) {
	f.mu.Lock()
	f.ScanCalls++
	f.mu.Unlock()
	if f.ScanFn != nil {
		return f.ScanFn(ctx)
	}
	return f.Items, f.ScanErr
}

func (f *fakeRecoveryOp) ExecuteRecovery(ctx context.Context, items []ports.RecoveryItem, actions map[string]ports.RecoveryAction) error {
	f.mu.Lock()
	f.ExecCalls = append(f.ExecCalls, fakeExecCall{Items: items, Actions: actions})
	f.mu.Unlock()
	if f.ExecFn != nil {
		return f.ExecFn(ctx, items, actions)
	}
	return f.ExecErr
}

// ---------------------------------------------------------------------------
// fakeRecoveryItems — concise constructor for recovery items used by tests.
// ---------------------------------------------------------------------------

type itemSpec struct {
	FeatureID    string
	RepoName     string
	ProcessAlive bool
	CurrentPhase feature.Phase
	CycleType    feature.RepoCycleType
	Status       feature.Status
	NoFeature    bool // when true, Feature field is left nil
	Pipeline     feature.PipelineProfile
}

func fakeRecoveryItems(specs ...itemSpec) []ports.RecoveryItem {
	items := make([]ports.RecoveryItem, 0, len(specs))
	for _, s := range specs {
		var f *feature.Feature
		if !s.NoFeature {
			f = &feature.Feature{
				ID:           s.FeatureID,
				CurrentPhase: s.CurrentPhase,
				Status:       s.Status,
				Pipeline:     s.Pipeline,
			}
			f.SetActiveCycleType(s.CycleType)
		}
		items = append(items, ports.RecoveryItem{
			PIDFile:      session.PIDFile{PID: 12345, FeatureID: s.FeatureID, RepoName: s.RepoName},
			ProcessAlive: s.ProcessAlive,
			Feature:      f,
			RepoName:     s.RepoName,
		})
	}
	return items
}

// ---------------------------------------------------------------------------
// fakeRunMultiRepoImpl — spy for the runMultiRepoImplFn seam. Returns a
// pre-seeded buffered channel; exposes every argument the orchestrator
// forwarded so tests can assert parity with feature state.
// ---------------------------------------------------------------------------

// runMultiRepoCall captures every invocation of the runMultiRepoImplFn seam.
// The unified phase-implement loop derives its repo set from PhaseScope and
// re-runs interrupted units from scratch.
type runMultiRepoCall struct {
	Feature  *feature.Feature
	PlanPath string
	KBInfos  []agent.KBInfo
}

type fakeRunMultiRepoImpl struct {
	mu sync.Mutex

	// Calls captures every invocation.
	Calls []runMultiRepoCall

	// Return values. If ReturnErr is set, the seam returns (nil, ReturnErr).
	// Otherwise, returns a channel. If ChannelFactory is set it is called to
	// build the channel; otherwise a buffered(1) channel is constructed and
	// TerminalResult is sent synchronously before return (if non-nil).
	ReturnErr      error
	ChannelFactory func() chan *agent.OrchestratorResult
	TerminalResult *agent.OrchestratorResult
}

// Fn returns the function to install with SetRunMultiRepoImplFn.
func (s *fakeRunMultiRepoImpl) Fn() func(
	f *feature.Feature,
	planPath string,
	kbInfos ...agent.KBInfo,
) (chan *agent.OrchestratorResult, error) {
	return func(
		f *feature.Feature,
		planPath string,
		kbInfos ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		s.mu.Lock()
		s.Calls = append(s.Calls, runMultiRepoCall{
			Feature:  f,
			PlanPath: planPath,
			KBInfos:  append([]agent.KBInfo(nil), kbInfos...),
		})
		s.mu.Unlock()
		if s.ReturnErr != nil {
			return nil, s.ReturnErr
		}
		var ch chan *agent.OrchestratorResult
		if s.ChannelFactory != nil {
			ch = s.ChannelFactory()
		} else {
			ch = make(chan *agent.OrchestratorResult, 1)
			if s.TerminalResult != nil {
				ch <- s.TerminalResult
			}
		}
		return ch, nil
	}
}

// numCalls returns the number of times the spy was invoked.
func (s *fakeRunMultiRepoImpl) numCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Calls)
}

// noopRunMultiRepoImplFn returns a seam installer that returns a buffered(1)
// channel with no value written — so the dispatch goroutine blocks forever
// without emitting anything. Useful for tests that only need to verify the
// pre-engine dispatch path (transitions, PhaseStarted event) without driving
// a full cycle-terminal result.
func noopRunMultiRepoImplFn() func(
	f *feature.Feature,
	planPath string,
	kbInfos ...agent.KBInfo,
) (chan *agent.OrchestratorResult, error) {
	return func(
		f *feature.Feature,
		planPath string,
		kbInfos ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		return make(chan *agent.OrchestratorResult, 1), nil
	}
}

// ---------------------------------------------------------------------------
// waitForEvent collects events until one matching `match` is observed or
// the deadline elapses. Returns the matched event, or nil on timeout.
// Intended for end-to-end tests that drive an async pipeline.
// ---------------------------------------------------------------------------

func waitForEvent(ch <-chan ports.Event, match func(ports.Event) bool, deadline time.Duration) *ports.Event {
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if match(ev) {
				return &ev
			}
		case <-timer.C:
			return nil
		}
	}
}

// collectEventsFor collects every event observed on ch until `deadline`
// elapses, then returns the slice. Used when a test needs to assert on
// the full post-trigger event set (e.g., "no FeatureCompleted emitted").
func collectEventsFor(ch <-chan ports.Event, deadline time.Duration) []ports.Event {
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	var events []ports.Event
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-timer.C:
			return events
		}
	}
}
