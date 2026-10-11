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
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// Steps the recording fakes add to the operation trace.
const (
	stepPhaseStarted = "phase started"
	stepChildCreated = "child created"
	stepSetupRun     = "setup run"
	stepSetupRetried = "setup retried"
	stepRewound      = "rewound"
)

var errStopDispatch = errors.New("dispatch observed")

// opFixture runs one orchestrator over a recording lifecycle and an
// in-memory store. Every lock, guard, admission, transition, and dispatch
// step the operations take lands in steps in order, together with the
// fakes' own steps.
type opFixture struct {
	t     *testing.T
	o     *Orchestrator
	lc    *mocks.MockFeatureLifecycle
	store *gateTestFeatureStore

	mu    sync.Mutex
	steps []string
}

func newOpFixture(t *testing.T, features ...*feature.Feature) *opFixture {
	t.Helper()
	fx := &opFixture{t: t, store: newGateFeatureStore(features...)}
	fx.store.ListFn = func() ([]*feature.Feature, error) {
		fx.store.mu.Lock()
		defer fx.store.mu.Unlock()
		list := make([]*feature.Feature, 0, len(fx.store.features))
		for _, f := range fx.store.features {
			list = append(list, f)
		}
		return list, nil
	}
	fx.lc = mocks.NewMockFeatureLifecycle()
	fx.lc.GetFn = fx.store.Load
	fx.lc.TransitionFn = func(id string, to feature.Status) error {
		return fx.store.Modify(id, func(f *feature.Feature) error {
			f.Status = to
			return nil
		})
	}
	fx.lc.StartInquireFn = func(string) error {
		fx.record(stepPhaseStarted)
		return errStopDispatch
	}
	fx.lc.RunSetupFn = func(string, ...feature.SetupRunnerOptions) error {
		fx.record(stepSetupRun)
		return nil
	}
	fx.lc.RetrySetupFn = func(string, ...feature.SetupRunnerOptions) error {
		fx.record(stepSetupRetried)
		return nil
	}
	fx.lc.RewindWithRequestFn = func(string, feature.RewindRequest) ([]feature.RewindWarning, feature.Phase, error) {
		fx.record(stepRewound)
		return nil, feature.PhaseInquire, nil
	}
	fx.o = fx.newOrchestrator(fx.lc)
	return fx
}

func (fx *opFixture) newOrchestrator(lc ports.FeatureLifecycle) *Orchestrator {
	o := New(Deps{Lifecycle: lc, Store: fx.store}, Hooks{})
	o.traceStep = fx.record
	fx.t.Cleanup(func() {
		o.WaitForCycles()
		_ = o.Shutdown()
	})
	return o
}

func (fx *opFixture) record(step string) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	fx.steps = append(fx.steps, step)
}

func (fx *opFixture) recorded() []string {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]string(nil), fx.steps...)
}

func (fx *opFixture) reset() {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	fx.steps = nil
}

func (fx *opFixture) assertSteps(want ...string) {
	fx.t.Helper()
	if got := fx.recorded(); !reflect.DeepEqual(got, want) {
		fx.t.Fatalf("steps =\n  %q\nwant\n  %q", got, want)
	}
}

// closeAdmission installs an admission boundary that refuses new work.
func (fx *opFixture) closeAdmission() {
	coord := workadmission.New(workadmission.Options{})
	fx.o.SetAdmissionBoundary(coord)
	if !coord.CloseIfQuiesced() {
		fx.t.Fatal("admission boundary did not close")
	}
}

func opFeature(id string, status feature.Status, phase feature.Phase) *feature.Feature {
	return &feature.Feature{
		ID: id, Slug: id, Status: status, CurrentPhase: phase,
		Pipeline: feature.PipelineLarge, ActiveRun: 1, RunCount: 1,
	}
}

// opParentWithChild returns a parent and its active, setup-complete child.
func opParentWithChild() (*feature.Feature, *feature.Feature) {
	parent := opFeature("op-parent", feature.StatusInterrupted, feature.PhaseInquire)
	child := opFeature("op-child", feature.StatusCreated, feature.PhasePlan)
	child.Pipeline = feature.PipelineMedium
	child.Parent = &feature.ChildRelationship{ParentID: parent.ID, Kind: feature.ChildKindRefactor}
	return parent, child
}

func setupFeature(id string, status feature.SetupStatus) *feature.Feature {
	f := opFeature(id, feature.StatusSettingUpWorktrees, feature.PhaseKnowledgeBase)
	f.Run().Setup = &feature.SetupState{Status: status, Attempt: 1}
	if status == feature.SetupStatusFailed {
		f.Status = feature.StatusFailed
		f.Run().Failure = &errcat.FailureRecord{Code: errcat.WorktreeSetupFailed}
	}
	return f
}

func TestStopFeatureOrdering(t *testing.T) {
	tests := []struct {
		name      string
		features  func() []*feature.Feature
		target    string
		wantErr   error
		wantSteps []string
		wantFinal feature.Status
	}{
		{
			name: "interrupts inside the guard window",
			features: func() []*feature.Feature {
				return []*feature.Feature{opFeature("op-1", feature.StatusImplementing, feature.PhaseImplement)}
			},
			target:    "op-1",
			wantSteps: []string{traceLockAcquired, traceGuard, traceTransition, traceLockReleased},
			wantFinal: feature.StatusInterrupted,
		},
		{
			name: "failed guard exits before interrupting",
			features: func() []*feature.Feature {
				parent, child := opParentWithChild()
				parent.Status = feature.StatusImplementing
				return []*feature.Feature{parent, child}
			},
			target:    "op-parent",
			wantErr:   feature.ErrParentMutationLocked,
			wantSteps: []string{traceLockAcquired, traceGuard, traceLockReleased},
			wantFinal: feature.StatusImplementing,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newOpFixture(t, tt.features()...)
			err := fx.o.StopFeature(tt.target)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("StopFeature() error = %v, want %v", err, tt.wantErr)
			}
			fx.assertSteps(tt.wantSteps...)
			if f, _ := fx.store.Load(tt.target); f.Status != tt.wantFinal {
				t.Fatalf("status = %s, want %s", f.Status, tt.wantFinal)
			}
		})
	}
}

func TestRestartFeatureOrdering(t *testing.T) {
	tests := []struct {
		name        string
		features    func() []*feature.Feature
		target      string
		busySession bool
		wantErr     error
		wantAction  RestartAction
		wantSteps   []string
	}{
		{
			name: "dispatches the phase before releasing the lock",
			features: func() []*feature.Feature {
				return []*feature.Feature{opFeature("op-1", feature.StatusInterrupted, feature.PhaseInquire)}
			},
			target:     "op-1",
			wantErr:    errStopDispatch,
			wantAction: RestartDispatchPhase,
			wantSteps: []string{
				traceLockAcquired, traceGuard, traceTransition,
				traceDispatch, traceGuard, stepPhaseStarted, traceLockReleased,
			},
		},
		{
			name: "failed guard exits before the transition",
			features: func() []*feature.Feature {
				parent, child := opParentWithChild()
				return []*feature.Feature{parent, child}
			},
			target:    "op-parent",
			wantErr:   feature.ErrParentMutationLocked,
			wantSteps: []string{traceLockAcquired, traceGuard, traceLockReleased},
		},
		{
			name: "busy feature exits before the transition",
			features: func() []*feature.Feature {
				return []*feature.Feature{opFeature("op-1", feature.StatusInterrupted, feature.PhaseInquire)}
			},
			target:      "op-1",
			busySession: true,
			wantErr:     ErrFeatureBusy,
			wantSteps:   []string{traceLockAcquired, traceGuard, traceLockReleased},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newOpFixture(t, tt.features()...)
			if tt.busySession {
				sessions := mocks.NewMockSessionManager()
				sessions.FeatureSessionsFn = func(string) []ports.SessionView {
					v := mocks.NewMockSessionView("busy", tt.target)
					v.IsActiveVal = true
					return []ports.SessionView{v}
				}
				fx.o.deps.Sessions = sessions
			}
			outcome, err := fx.o.RestartFeature(tt.target, 0, 0)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("RestartFeature() error = %v, want %v", err, tt.wantErr)
			}
			if outcome.Action != tt.wantAction {
				t.Fatalf("outcome = %+v, want action %d", outcome, tt.wantAction)
			}
			fx.assertSteps(tt.wantSteps...)
		})
	}
}

// TestRestartFeatureHoldsLockThroughDispatch proves a child creation cannot
// interleave between the restart transition and the phase start.
func TestRestartFeatureHoldsLockThroughDispatch(t *testing.T) {
	fx := newOpFixture(t, opFeature("op-1", feature.StatusInterrupted, feature.PhaseInquire))
	inDispatch := make(chan struct{})
	release := make(chan struct{})
	fx.lc.StartInquireFn = func(string) error {
		close(inDispatch)
		<-release
		return errStopDispatch
	}
	creator := &blockingChildCreator{fx: fx}
	o := fx.newOrchestrator(childCreatorLifecycle{MockFeatureLifecycle: fx.lc, creator: creator})

	restartDone := make(chan error, 1)
	go func() {
		_, err := o.RestartFeature("op-1", 0, 0)
		restartDone <- err
	}()
	<-inDispatch

	launchDone := make(chan error, 1)
	go func() {
		_, err := o.LaunchChild("op-1", ChildLaunch{Kind: feature.ChildKindRefactor})
		launchDone <- err
	}()
	select {
	case err := <-launchDone:
		t.Fatalf("child launch finished (err %v) while the restart dispatch held the lock", err)
	case <-time.After(50 * time.Millisecond):
	}
	if creator.calls() != 0 {
		t.Fatal("child created while the restart dispatch held the lock")
	}

	close(release)
	if err := <-restartDone; !errors.Is(err, errStopDispatch) {
		t.Fatalf("RestartFeature() error = %v, want the dispatch result", err)
	}
	if err := <-launchDone; err != nil {
		t.Fatalf("LaunchChild() error = %v", err)
	}
	if creator.calls() != 1 {
		t.Fatalf("child creations = %d, want 1 after the restart released the lock", creator.calls())
	}
}

func TestRetryFeatureRouting(t *testing.T) {
	budgetFailed := func(phase feature.Phase, code errcat.Code) *feature.Feature {
		f := opFeature("op-1", feature.StatusFailed, phase)
		f.MaxIterations = 10
		f.MaxPlanIterations = 3
		f.Run().Failure = &errcat.FailureRecord{Code: code}
		return f
	}
	// Restarted cases dispatch into a closed admission boundary so the
	// phase start is refused right after the dispatch is requested.
	tests := []struct {
		name          string
		feature       *feature.Feature
		wantSteps     []string
		wantMaxIter   int
		wantMaxPlan   int
		wantRestarted bool
	}{
		{
			name: "failed setup reruns setup without the restart",
			feature: func() *feature.Feature {
				f := setupFeature("op-1", feature.SetupStatusFailed)
				f.Parent = &feature.ChildRelationship{ParentID: "absent-parent", Kind: feature.ChildKindRefactor}
				return f
			}(),
			wantSteps: []string{stepSetupRetried},
		},
		{
			name:          "exhausted budget restarts with the fixed extension",
			feature:       budgetFailed(feature.PhaseInquire, errcat.IterationBudgetExhausted),
			wantRestarted: true,
			wantMaxIter:   10 + retryMaxIterationsDelta,
			wantMaxPlan:   3,
		},
		{
			name:          "plan failure on an exhausted budget extends the plan budget",
			feature:       budgetFailed(feature.PhasePlan, errcat.IterationBudgetExhausted),
			wantRestarted: true,
			wantMaxIter:   10 + retryMaxIterationsDelta,
			wantMaxPlan:   3 + retryMaxPlanIterationsDelta,
		},
		{
			name:          "other failures restart without an extension",
			feature:       budgetFailed(feature.PhaseInquire, errcat.InfrastructureFailure),
			wantRestarted: true,
			wantMaxIter:   10,
			wantMaxPlan:   3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newOpFixture(t, tt.feature)
			if tt.wantRestarted {
				fx.closeAdmission()
			}
			err := fx.o.RetryFeature("op-1")
			if !tt.wantRestarted {
				if err != nil {
					t.Fatalf("RetryFeature() error = %v", err)
				}
				fx.assertSteps(tt.wantSteps...)
				return
			}
			if _, closed := workadmission.AsClosed(err); !closed {
				t.Fatalf("RetryFeature() error = %v, want the refused phase dispatch", err)
			}
			fx.assertSteps(traceLockAcquired, traceGuard, traceTransition, traceDispatch, traceGuard, traceLockReleased)
			f, _ := fx.store.Load("op-1")
			if f.MaxIterations != tt.wantMaxIter || f.MaxPlanIterations != tt.wantMaxPlan {
				t.Fatalf("budgets = %d/%d, want %d/%d", f.MaxIterations, f.MaxPlanIterations, tt.wantMaxIter, tt.wantMaxPlan)
			}
		})
	}
}

func TestRetryIterationDeltas(t *testing.T) {
	t.Parallel()

	failedWithRecord := func(code errcat.Code) *feature.Feature {
		f := &feature.Feature{Status: feature.StatusFailed, CurrentPhase: feature.PhasePlan}
		f.Run().Failure = &errcat.FailureRecord{Code: code}
		return f
	}

	tests := []struct {
		name     string
		feature  *feature.Feature
		wantMax  int
		wantPlan int
	}{
		{
			name:     "iteration budget exhausted",
			feature:  failedWithRecord(errcat.IterationBudgetExhausted),
			wantMax:  10,
			wantPlan: 2,
		},
		{
			name:    "worktree setup failure routes to setup retry without deltas",
			feature: failedWithRecord(errcat.WorktreeSetupFailed),
		},
		{
			name:    "other failure",
			feature: failedWithRecord(errcat.InfrastructureFailure),
		},
		{
			name: "failure record on active feature",
			feature: func() *feature.Feature {
				f := &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement}
				f.Run().Failure = &errcat.FailureRecord{Code: errcat.IterationBudgetExhausted}
				return f
			}(),
		},
		{name: "missing feature"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotMax, gotPlan := retryIterationDeltas(tt.feature)
			if gotMax != tt.wantMax || gotPlan != tt.wantPlan {
				t.Fatalf("retryIterationDeltas() = (%d, %d), want (%d, %d)", gotMax, gotPlan, tt.wantMax, tt.wantPlan)
			}
		})
	}
}

func TestDispatchSetupOrdering(t *testing.T) {
	tests := []struct {
		name           string
		feature        *feature.Feature
		closeAdmission bool
		wantErr        error
		wantClosed     bool
		wantSteps      []string
	}{
		{
			name:      "queued setup runs after admission is reserved",
			feature:   setupFeature("op-1", feature.SetupStatusQueued),
			wantSteps: []string{traceAdmission, traceDispatch, stepSetupRun, traceSettle},
		},
		{
			name:      "failed setup reruns only unfinished tasks",
			feature:   setupFeature("op-1", feature.SetupStatusFailed),
			wantSteps: []string{traceAdmission, traceDispatch, stepSetupRetried, traceSettle},
		},
		{
			name:    "no setup work is refused",
			feature: opFeature("op-1", feature.StatusCreated, feature.PhaseKnowledgeBase),
			wantErr: ErrNoSetupWork,
		},
		{
			name:           "refused admission never launches",
			feature:        setupFeature("op-1", feature.SetupStatusQueued),
			closeAdmission: true,
			wantClosed:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newOpFixture(t, tt.feature)
			if tt.closeAdmission {
				fx.closeAdmission()
			}
			err := fx.o.DispatchSetup("op-1")
			fx.o.WaitForCycles()
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("DispatchSetup() error = %v, want %v", err, tt.wantErr)
			}
			if _, closed := workadmission.AsClosed(err); closed != tt.wantClosed {
				t.Fatalf("DispatchSetup() error = %v, want closed=%v", err, tt.wantClosed)
			}
			if tt.wantErr == nil && !tt.wantClosed && err != nil {
				t.Fatalf("DispatchSetup() error = %v", err)
			}
			fx.assertSteps(tt.wantSteps...)
		})
	}
}

// blockingChildCreator records child creations; when gate is set, creation
// waits on it while the caller holds the relationship write lock.
type blockingChildCreator struct {
	fx       *opFixture
	gate     chan struct{}
	creating chan struct{}
	replay   bool
	mu       sync.Mutex
	n        int
}

func (c *blockingChildCreator) create(parentID string) *feature.Feature {
	c.fx.record(stepChildCreated)
	if c.gate != nil {
		close(c.creating)
		<-c.gate
	}
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	child := opFeature("op-new-child", feature.StatusSettingUpWorktrees, feature.PhaseKnowledgeBase)
	child.Parent = &feature.ChildRelationship{ParentID: parentID, Kind: feature.ChildKindRefactor}
	c.fx.store.mu.Lock()
	c.fx.store.features[child.ID] = child
	c.fx.store.mu.Unlock()
	return child
}

func (c *blockingChildCreator) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

type childCreatorLifecycle struct {
	*mocks.MockFeatureLifecycle
	creator *blockingChildCreator
}

func (l childCreatorLifecycle) CreateRefactorChild(parentID string, _ feature.RefactorChildSpec) (*feature.Feature, error) {
	return l.creator.create(parentID), nil
}

func (l childCreatorLifecycle) LaunchReviewFeedbackChildFromDraft(parentID string, _ int64, _ *bool) (*feature.ReviewFeedbackLaunchResult, error) {
	return &feature.ReviewFeedbackLaunchResult{Child: l.creator.create(parentID), Changed: 2, Replayed: l.creator.replay}, nil
}

func (l childCreatorLifecycle) CreateRebaseChild(parentID string, _ feature.RebaseChildSpec) (*feature.Feature, error) {
	return l.creator.create(parentID), nil
}

func TestLaunchChildOrdering(t *testing.T) {
	launched := []string{
		traceLockAcquired, stepChildCreated, traceLockReleased,
		traceAdmission, traceDispatch, stepSetupRun, traceSettle,
	}
	tests := []struct {
		name        string
		launch      ChildLaunch
		replay      bool
		noCreator   bool
		wantErr     error
		wantSteps   []string
		wantCreated bool
	}{
		{
			name:        "refactor creates under the lock, then announces and dispatches setup",
			launch:      ChildLaunch{Kind: feature.ChildKindRefactor},
			wantSteps:   launched,
			wantCreated: true,
		},
		{
			name:        "review feedback creates under the lock, then announces and dispatches setup",
			launch:      ChildLaunch{Kind: feature.ChildKindReviewFeedback, ExpectedRevision: 3},
			wantSteps:   launched,
			wantCreated: true,
		},
		{
			name:      "review feedback replay skips the announcement and setup",
			launch:    ChildLaunch{Kind: feature.ChildKindReviewFeedback},
			replay:    true,
			wantSteps: []string{traceLockAcquired, stepChildCreated, traceLockReleased},
		},
		{
			name:    "rebase runs its preflight before taking the lock",
			launch:  ChildLaunch{Kind: feature.ChildKindRebase},
			wantErr: feature.ErrRefactorParentNotFound,
		},
		{
			name:      "lifecycle without child creation is refused",
			launch:    ChildLaunch{Kind: feature.ChildKindRefactor},
			noCreator: true,
			wantErr:   ErrChildCreationUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newOpFixture(t, opFeature("op-1", feature.StatusPublished, feature.PhasePublish))
			creator := &blockingChildCreator{fx: fx, replay: tt.replay}
			o := fx.o
			if !tt.noCreator {
				o = fx.newOrchestrator(childCreatorLifecycle{MockFeatureLifecycle: fx.lc, creator: creator})
			}
			parentID := "op-1"
			if tt.launch.Kind == feature.ChildKindRebase {
				fx.store.LoadFn = func(string) (*feature.Feature, error) { return nil, os.ErrNotExist }
			}
			result, err := o.LaunchChild(parentID, tt.launch)
			o.WaitForCycles()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("LaunchChild() error = %v, want %v", err, tt.wantErr)
			}
			fx.assertSteps(tt.wantSteps...)
			if tt.wantErr != nil {
				return
			}
			if result.Child == nil || result.Child.ID != "op-new-child" || result.Replayed != tt.replay {
				t.Fatalf("result = %+v, want the created child", result)
			}
			created := false
			for _, ev := range drainOpEvents(o) {
				if ev.Type == ports.RelationshipChildCreated && ev.ChildID == "op-new-child" {
					created = true
				}
			}
			if created != tt.wantCreated {
				t.Fatalf("child-created event = %v, want %v", created, tt.wantCreated)
			}
		})
	}
}

func drainOpEvents(o *Orchestrator) []ports.Event {
	var events []ports.Event
	for {
		select {
		case ev := <-o.Events():
			events = append(events, ev)
		default:
			return events
		}
	}
}

// TestRelationshipLockSerializesOperationsWithChildCreation asserts the
// lock at the operation level: a child launch holding the write lock blocks
// stop, restart, and config update until creation finishes, and a stop
// holding the read lock blocks a child launch.
func TestRelationshipLockSerializesOperationsWithChildCreation(t *testing.T) {
	mutations := map[string]func(o *Orchestrator) error{
		"stop": func(o *Orchestrator) error { return o.StopFeature("op-1") },
		"restart": func(o *Orchestrator) error {
			_, err := o.RestartFeature("op-1", 0, 0)
			return err
		},
		"config update": func(o *Orchestrator) error {
			_, err := o.UpdateFeatureConfig("op-1", UpdateFeatureConfigInput{})
			return err
		},
	}
	for name, mutate := range mutations {
		t.Run("child launch blocks "+name, func(t *testing.T) {
			fx := newOpFixture(t, opFeature("op-1", feature.StatusInterrupted, feature.PhaseInquire))
			creator := &blockingChildCreator{fx: fx, gate: make(chan struct{}), creating: make(chan struct{})}
			o := fx.newOrchestrator(childCreatorLifecycle{MockFeatureLifecycle: fx.lc, creator: creator})

			launchDone := make(chan error, 1)
			go func() {
				_, err := o.LaunchChild("op-1", ChildLaunch{Kind: feature.ChildKindRefactor})
				launchDone <- err
			}()
			<-creator.creating

			mutated := make(chan struct{})
			go func() {
				_ = mutate(o)
				close(mutated)
			}()
			select {
			case <-mutated:
				t.Fatalf("%s finished while child creation held the write lock", name)
			case <-time.After(50 * time.Millisecond):
			}
			close(creator.gate)
			if err := <-launchDone; err != nil {
				t.Fatalf("LaunchChild() error = %v", err)
			}
			select {
			case <-mutated:
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not proceed after child creation released the lock", name)
			}
		})
	}

	t.Run("stop blocks child launch", func(t *testing.T) {
		fx := newOpFixture(t, opFeature("op-1", feature.StatusImplementing, feature.PhaseImplement))
		inStop := make(chan struct{})
		release := make(chan struct{})
		fx.lc.TransitionFn = func(string, feature.Status) error {
			close(inStop)
			<-release
			return nil
		}
		creator := &blockingChildCreator{fx: fx}
		o := fx.newOrchestrator(childCreatorLifecycle{MockFeatureLifecycle: fx.lc, creator: creator})

		stopDone := make(chan error, 1)
		go func() { stopDone <- o.StopFeature("op-1") }()
		<-inStop

		launchDone := make(chan error, 1)
		go func() {
			_, err := o.LaunchChild("op-1", ChildLaunch{Kind: feature.ChildKindRefactor})
			launchDone <- err
		}()
		select {
		case <-launchDone:
			t.Fatal("child launch finished while stop held the read lock")
		case <-time.After(50 * time.Millisecond):
		}
		if creator.calls() != 0 {
			t.Fatal("child created while stop held the read lock")
		}
		close(release)
		if err := <-stopDone; err != nil {
			t.Fatalf("StopFeature() error = %v", err)
		}
		if err := <-launchDone; err != nil {
			t.Fatalf("LaunchChild() error = %v", err)
		}
	})
}

func TestDeleteSettlesOnlyCompletedCascade(t *testing.T) {
	tests := []struct {
		name      string
		deleteErr error
		wantSteps []string
	}{
		{name: "completed cascade settles admission", wantSteps: []string{traceSettle}},
		{name: "failed cascade keeps the reservation", deleteErr: errors.New("delete failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newOpFixture(t, opFeature("op-1", feature.StatusDone, feature.PhasePublish))
			fx.lc.DeleteFn = func(string) error { return tt.deleteErr }
			fx.lc.GetFn = func(string) (*feature.Feature, error) { return nil, errors.New("not loaded") }
			result, err := fx.o.Delete("op-1")
			if !errors.Is(err, tt.deleteErr) {
				t.Fatalf("Delete() error = %v, want %v", err, tt.deleteErr)
			}
			if tt.deleteErr == nil && result.Status != feature.CascadeDeleteCompleted {
				t.Fatalf("Delete() result = %+v, want completed", result)
			}
			fx.assertSteps(tt.wantSteps...)
		})
	}
}

func TestDiscardChildSettlesAfterDiscard(t *testing.T) {
	t.Run("refused discard keeps the reservation", func(t *testing.T) {
		fx := newOpFixture(t, opFeature("op-1", feature.StatusImplementing, feature.PhaseImplement))
		if err := fx.o.DiscardChild("op-1"); err == nil {
			t.Fatal("DiscardChild() on a non-child error = nil")
		}
		fx.assertSteps()
	})
	t.Run("settles after the discard state machine returns", func(t *testing.T) {
		if testing.Short() {
			t.Skip("real-git discard")
		}
		cfx := newChildIntegrationFixture(t, feature.StatusPublished, true)
		o := cfx.orchestrator()
		var steps []string
		var mu sync.Mutex
		o.traceStep = func(step string) {
			mu.Lock()
			defer mu.Unlock()
			steps = append(steps, step)
		}
		if err := o.DiscardChild(cfx.child.ID); err != nil {
			t.Fatalf("DiscardChild() error = %v", err)
		}
		_, child := cfx.reload()
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeDiscarded {
			t.Fatalf("close outcome = %q, want discarded", child.Parent.CloseOutcome)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(steps) == 0 || steps[len(steps)-1] != traceSettle {
			t.Fatalf("steps = %q, want admission settled last", steps)
		}
	})
}
