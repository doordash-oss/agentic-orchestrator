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
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

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

// opFixture records operation ordering while keeping feature state on disk.
type opFixture struct {
	t     *testing.T
	o     *Orchestrator
	mgr   *feature.Manager
	store *feature.Store

	mu    sync.Mutex
	steps []string
}

func newOpFixture(t *testing.T, features ...*feature.Feature) *opFixture {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	for _, f := range features {
		f.SchemaVersion = feature.SchemaVersionCurrent
		if err := store.Save(f); err != nil {
			t.Fatalf("save feature %s: %v", f.ID, err)
		}
	}
	fx := &opFixture{t: t, store: store, mgr: feature.NewManager(store, config.NewDefault())}
	fx.o = fx.newOrchestrator()
	return fx
}

func (fx *opFixture) newOrchestrator() *Orchestrator {
	o := New(Deps{Manager: fx.mgr, Store: fx.store, Worktrees: mocks.NewMockWorktreeOps()}, Hooks{})
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
		SchemaVersion: feature.SchemaVersionCurrent,
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
		wantStatus  feature.Status
	}{
		{
			name: "closed admission refuses restart before phase dispatch",
			features: func() []*feature.Feature {
				return []*feature.Feature{opFeature("op-1", feature.StatusInterrupted, feature.PhaseInquire)}
			},
			target:     "op-1",
			wantAction: RestartDispatchPhase,
			wantStatus: feature.StatusInterrupted,
		},
		{
			name: "failed guard exits before the transition",
			features: func() []*feature.Feature {
				parent, child := opParentWithChild()
				return []*feature.Feature{parent, child}
			},
			target:     "op-parent",
			wantErr:    feature.ErrParentMutationLocked,
			wantSteps:  []string{traceLockAcquired, traceGuard, traceLockReleased},
			wantStatus: feature.StatusInterrupted,
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
			wantStatus:  feature.StatusInterrupted,
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
			if tt.wantErr == nil {
				fx.closeAdmission()
			}
			outcome, err := fx.o.RestartFeature(tt.target, 0, 0)
			if tt.wantErr == nil {
				if _, closed := workadmission.AsClosed(err); !closed {
					t.Fatalf("RestartFeature() error = %v, want closed admission", err)
				}
			} else if !errors.Is(err, tt.wantErr) {
				t.Fatalf("RestartFeature() error = %v, want %v", err, tt.wantErr)
			}
			if outcome.Action != tt.wantAction {
				t.Fatalf("outcome = %+v, want action %d", outcome, tt.wantAction)
			}
			if tt.wantSteps != nil {
				fx.assertSteps(tt.wantSteps...)
			}
			stored, loadErr := fx.store.Load(tt.target)
			if loadErr != nil || stored.Status != tt.wantStatus {
				t.Fatalf("persisted status = %+v (err %v), want %s", stored, loadErr, tt.wantStatus)
			}
		})
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
	// Dispatch into a closed admission boundary so the persisted retry budgets
	// can be read before an agent phase starts.
	tests := []struct {
		name        string
		feature     *feature.Feature
		wantMaxIter int
		wantMaxPlan int
	}{
		{
			name:        "exhausted budget restarts with the fixed extension",
			feature:     budgetFailed(feature.PhaseInquire, errcat.IterationBudgetExhausted),
			wantMaxIter: 10 + retryMaxIterationsDelta,
			wantMaxPlan: 3,
		},
		{
			name:        "plan failure on an exhausted budget extends the plan budget",
			feature:     budgetFailed(feature.PhasePlan, errcat.IterationBudgetExhausted),
			wantMaxIter: 10 + retryMaxIterationsDelta,
			wantMaxPlan: 3 + retryMaxPlanIterationsDelta,
		},
		{
			name:        "other failures restart without an extension",
			feature:     budgetFailed(feature.PhaseInquire, errcat.InfrastructureFailure),
			wantMaxIter: 10,
			wantMaxPlan: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newOpFixture(t, tt.feature)
			fx.closeAdmission()
			err := fx.o.RetryFeature("op-1")
			if _, closed := workadmission.AsClosed(err); !closed {
				t.Fatalf("RetryFeature() error = %v, want the refused phase dispatch", err)
			}
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
			wantSteps: []string{traceAdmission, traceDispatch, traceSettle},
		},
		{
			name:      "failed setup reruns only unfinished tasks",
			feature:   setupFeature("op-1", feature.SetupStatusFailed),
			wantSteps: []string{traceAdmission, traceDispatch, traceSettle},
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

func TestRelationshipLockSerializesChildCreationAndStop(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git child creation")
	}
	repo := testutil.InitGitRepo(t)
	childIntegrationGit(t, repo, "checkout", "-b", "feature/op-parent")
	publishable := true
	parent := opFeature("op-parent", feature.StatusPublished, feature.PhasePublish)
	parent.Name = "Operation parent"
	parent.Created = time.Now()
	parent.Repos = []feature.FeatureRepo{{
		Name: "repo", Path: repo, WorktreePath: repo,
		Branch: "feature/op-parent", BaseBranch: "main", Publishable: &publishable,
	}}
	fx := newOpFixture(t, parent)
	wm := git.NewWorktreeManager(t.TempDir())
	fx.mgr.Worktrees = wm
	fx.o.deps.Worktrees = wm

	insideSave := make(chan struct{})
	releaseSave := make(chan struct{})
	var once sync.Once
	fx.store.SetSaveInterceptor(func(f *feature.Feature) error {
		if f.Parent != nil && f.Parent.ParentID == parent.ID {
			once.Do(func() {
				close(insideSave)
				<-releaseSave
			})
		}
		return nil
	})
	launchDone := make(chan struct {
		result ChildLaunchResult
		err    error
	}, 1)
	go func() {
		result, err := fx.o.LaunchChild(parent.ID, ChildLaunch{Kind: feature.ChildKindRefactor})
		launchDone <- struct {
			result ChildLaunchResult
			err    error
		}{result, err}
	}()
	select {
	case <-insideSave:
	case <-time.After(5 * time.Second):
		close(releaseSave)
		t.Fatal("child creation never reached its durable save")
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- fx.o.StopFeature(parent.ID) }()
	select {
	case err := <-stopDone:
		t.Fatalf("stop finished while child creation held the relationship lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseSave)
	launched := <-launchDone
	fx.store.SetSaveInterceptor(nil)
	if launched.err != nil {
		t.Fatalf("LaunchChild() error = %v", launched.err)
	}
	if err := <-stopDone; !errors.Is(err, feature.ErrParentMutationLocked) {
		t.Fatalf("StopFeature() error = %v, want active-child guard", err)
	}
	stored, err := fx.store.Load(launched.result.Child.ID)
	if err != nil || stored.Parent == nil || stored.Parent.ParentID != parent.ID {
		t.Fatalf("persisted child = %+v (err %v), want parent %s", stored, err, parent.ID)
	}
}

func TestDeleteSettlesOnlyCompletedCascade(t *testing.T) {
	fx := newOpFixture(t, opFeature("op-1", feature.StatusDone, feature.PhasePublish))
	result, err := fx.o.Delete("op-1")
	if err != nil || result.Status != feature.CascadeDeleteCompleted {
		t.Fatalf("Delete() = %+v, %v; want completed", result, err)
	}
	if _, err := fx.store.Load("op-1"); err == nil {
		t.Fatal("deleted feature still loads from store")
	}
	fx.assertSteps(traceSettle)
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

func TestAutoResume_StopWhileTimerHasTakenSchedule(t *testing.T) {
	f := opFeature("quota-stop-race", feature.StatusFailed, feature.PhaseImplement)
	plan := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(plan, []byte("# Plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.Artifacts = map[string]string{"plan": plan}
	resumeAt := time.Now().Add(time.Hour)
	f.Run().AutoResume = &feature.AutoResume{Provider: "claude", ResumeAt: resumeAt}
	fx := newOpFixture(t, f)
	taken, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	fx.o.traceStep = func(step string) {
		if step == "auto-resume taken" {
			close(taken)
			<-release
		}
	}
	resultCh := make(chan *agent.OrchestratorResult, 1)
	fx.o.SetRunMultiRepoImplFn(func(*feature.Feature, string, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		return resultCh, nil
	})
	defer func() { resultCh <- &agent.OrchestratorResult{FinalStatus: "interrupted"} }()
	fired := make(chan struct{})
	go func() { fx.o.fireAutoResume(f.ID, resumeAt); close(fired) }()
	select {
	case <-taken:
	case <-time.After(5 * time.Second):
		t.Fatal("timer did not take schedule")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- fx.o.StopFeature(f.ID) }()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before the claimed retry dispatched: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("timer did not dispatch")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not finish")
	}
	got, err := fx.store.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != feature.StatusInterrupted || got.PendingAutoResume() != nil {
		t.Fatalf("after Stop: status=%s schedule=%+v", got.Status, got.PendingAutoResume())
	}
}

func TestAutoResume_ReviewFailureDiagnosticsScheduleAcrossAgentPhases(t *testing.T) {
	for phase, status := range map[feature.Phase]feature.Status{
		feature.PhaseKnowledgeBase: feature.StatusBuildingKB,
		feature.PhaseInquire:       feature.StatusInquiring,
		feature.PhaseResearch:      feature.StatusResearching,
		feature.PhaseDesign:        feature.StatusDesigning,
		feature.PhasePlan:          feature.StatusPlanning,
		feature.PhaseImplement:     feature.StatusImplementing,
		feature.PhaseReview:        feature.StatusFinalReviewing,
		feature.PhaseFinalReview:   feature.StatusFinalReviewing,
	} {
		t.Run(phase.String(), func(t *testing.T) {
			f := opFeature("quota-phase", status, phase)
			fx := newOpFixture(t, f)
			now := time.Date(2026, time.October, 30, 8, 0, 0, 0, time.UTC)
			fx.o.nowFn = func() time.Time { return now }
			failure := errcat.FailureRecord{Code: errcat.InfrastructureFailure, Diagnostics: "review did not complete: provider usage limit: helper returned an error result\nYou've hit your 5-hour limit · resets 11:10am (America/Los_Angeles)\n; resume to re-run the review"}
			if err := fx.o.markFailedWithEvent(f.ID, failure); err != nil {
				t.Fatal(err)
			}
			got, err := fx.store.Load(f.ID)
			if err != nil {
				t.Fatal(err)
			}
			pending := got.PendingAutoResume()
			if pending == nil || !fx.o.autoResumeArmed(f.ID) {
				t.Fatalf("no auto-resume for %s", phase)
			}
			if !pending.ResumeAt.Equal(time.Date(2026, time.October, 30, 18, 12, 0, 0, time.UTC)) {
				t.Fatalf("wrong resume time: %s", pending.ResumeAt)
			}
		})
	}
}
