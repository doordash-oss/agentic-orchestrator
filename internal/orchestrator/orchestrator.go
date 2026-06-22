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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// ErrNotImplemented is a legacy sentinel retained for migration-guard tests.
var ErrNotImplemented = errors.New("not implemented")

const eventChBuffer = 256

type featureRebaseControl struct {
	mu       sync.Mutex
	cond     *sync.Cond
	stopping bool
	active   int
}

func newFeatureRebaseControl() *featureRebaseControl {
	c := &featureRebaseControl{}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// Hooks contains optional callbacks fired at lifecycle points.
// Nil hooks are silently skipped.
type Hooks struct {
	OnFeatureCreated     func(f *feature.Feature)
	OnFeatureStarted     func(featureID string)
	OnFeatureInterrupted func(featureID string)
	OnSetupEvent         func(feature.SetupEvent)
	OnPhaseStarted       func(featureID string, phase feature.Phase)
	OnPhaseCompleted     func(featureID string, phase feature.Phase, err error)

	// OnRecoveryScanned fires after a successful recovery scan. Receives the
	// full list of items so downstream observers can fan out per-feature
	// events.
	OnRecoveryScanned func(items []ports.RecoveryItem)

	// OnRecoveryAction fires once per recovery item whose action was present
	// in the actions map. repoName is "" for feature-scoped items and
	// carries the repo name for multi-repo items. action is the lowercase
	// action name ("resume", "kill", "skip").
	OnRecoveryAction func(featureID, repoName, action string)

	// Terminal and publish hooks. All nil-safe.
	OnFeatureCompleted func(featureID string, f *feature.Feature)
	OnFeatureFailed    func(featureID string, failureType, errorMsg string)
	OnReviewRequired   func(featureID string, phase feature.Phase)
	OnPublishStarted   func(featureID string)
	OnPublishCompleted func(featureID string, prURLs map[string]string, err error)

	// OnFeatureSummaryNeeded fires at every terminal transition (completed,
	// failed, done) so downstream observers can persist observe-summary.yaml
	// artifacts. Fired AFTER OnFeatureCompleted/OnFeatureFailed so the summary
	// sees the final feature state.
	OnFeatureSummaryNeeded func(featureID string, f *feature.Feature)

	// OnFeatureConfigChanged fires after Orchestrator.UpdateFeatureConfig
	// successfully writes the three editable config axes. Receives the
	// feature ID plus before/after snapshots for typed audit emission.
	OnFeatureConfigChanged func(featureID string, before, after feature.ConfigSnapshot)

	// OnFeatureRewound fires after a successful rewind fork. Receives both
	// the requested target and the effective target plus source/new run
	// numbers so observers can emit a durable audit record.
	OnFeatureRewound func(featureID string, request feature.RewindRequest, effectiveTarget feature.Phase, sourceRun, newRun int)
}

// PhaseCompletionInput is a sum-type describing a phase completion. Exactly
// one of the pointer result fields is non-nil for loop-driven phases; for
// session-parser-driven phases (KB/Inquire/Research/Design) all pointer
// fields are nil and the handler uses Success + ErrorDetail + SessionID.
type PhaseCompletionInput struct {
	Phase       feature.Phase
	SessionID   string
	Success     bool
	ErrorDetail string

	PlanResult      *agent.PlanLoopResult
	ImplementResult *agent.LoopResult
	MultiRepoResult *agent.OrchestratorResult
}

// NeedUserInputDecision describes the user's choice at a need-user-input gate.
// Scope selection is derived from a combination of fields plus persisted
// state on the feature (see HandleNeedUserInputDecision):
//   - empty RepoName → feature-scoped (single-repo mainline implement)
//   - RepoName set + RepoCycleState[RepoName].Status == RepoCycleNeedUserInput
//     on the persisted feature → cycle-scoped (post-publish)
//   - otherwise → repo-scoped (multi-repo mainline implement)
type NeedUserInputDecision struct {
	// Decision is "resume" or "abort". Any other value is rejected.
	Decision string
	// RepoName, when set, identifies the repo whose gate this decision
	// targets in a multi-repo run. Empty for single-repo / feature-scoped
	// gates.
	RepoName string
	// CycleType is the post-publish cycle the UI believed it was acting on
	// when the user pressed Resume / Abort. Diagnostic only — restart
	// dispatch reads the persisted RepoCycleState to decide which launcher
	// to call.
	CycleType feature.RepoCycleType
}

// ReviewDecision describes a user decision from a review gate or menu.
// The TUI collects these via its review-editor flow and hands them to the
// orchestrator for downstream state transitions and dispatch.
type ReviewDecision struct {
	Decision    string // "proceed" | "iterate"
	TargetPhase feature.Phase
	IsRewind    bool
	PhasePlan   bool
	Roadmap     bool
	// Comment carries free-text rejection feedback from a roadmap review
	// menu. Only meaningful when Roadmap == true && Decision == "iterate"
	// (the roadmap reject path). Mirrors RoadmapReviewDecisionMsg.Comment.
	Comment string
}

// PublishConflictError signals a pull-rebase conflict during publish. Satisfies
// errors.Is(err, ErrPublishConflict) so callers can route conflicts without
// reflecting on the wrapped type.
//
// Branch is the feature branch the conflicted push was targeting.
// RebaseTarget is the PR base branch (e.g. "master", "main") that the
// follow-up rebase-resolution plan must rebase ONTO. It is computed by the
// orchestrator (via PR lookup, repo.BaseBranch, or default-branch fallback)
// so consumers do not have to re-derive it; passing the feature branch in
// its place would point the rebase plan at the wrong target.
type PublishConflictError struct {
	RepoName     string
	Branch       string
	RebaseTarget string
}

func (e *PublishConflictError) Error() string {
	return fmt.Sprintf("publish: pull-rebase conflict in repo %s on branch %s", e.RepoName, e.Branch)
}

// Is reports whether target is the publish-conflict sentinel.
func (e *PublishConflictError) Is(target error) bool {
	_, ok := target.(*PublishConflictError)
	return ok
}

// ErrPublishConflict is the sentinel used with errors.Is for publish conflicts.
var ErrPublishConflict = &PublishConflictError{}

// Deps holds all port interface dependencies for the orchestrator.
type Deps struct {
	Lifecycle   ports.FeatureLifecycle
	Store       ports.FeatureStore
	Sessions    ports.SessionManager
	Publisher   ports.Publisher
	Differ      ports.DiffOperator
	Rebaser     ports.RebaseOperator
	CrossRef    ports.CrossRefOperator
	Reviewer    ports.ReviewCommentOperator
	Worktrees   ports.WorktreeOperator
	Branch      ports.BranchOperator
	CmdRunner   ports.CommandRunner
	Recovery    ports.RecoveryOperator
	PhaseRunner *agent.PhaseRunner // concrete — not behind a port interface
}

// PhaseStartOutcome enumerates the possible results of starting a phase.
type PhaseStartOutcome int

const (
	// PhaseStarted — the starter actually launched phase work. startPhase
	// emits a PhaseStarted event and fires OnPhaseStarted.
	PhaseStarted PhaseStartOutcome = iota

	// PhaseSkipped — the phase was skipped in favor of advancing to a
	// different phase. startPhase recursively dispatches to NextPhase and
	// does NOT emit an event or fire a hook for the skipped phase.
	PhaseSkipped

	// PhaseNoOp — the dispatch was considered but no phase actually started
	// AND there is no next phase to dispatch to. startPhase returns cleanly
	// and does NOT emit an event or fire a hook. Used by startPublish when
	// the feature is not publishable or auto-publish is disabled.
	PhaseNoOp
)

// PhaseStartResult communicates whether a phase starter actually launched
// work (PhaseStarted), should skip forward to another phase (PhaseSkipped →
// NextPhase), or short-circuited with no work and no next phase (PhaseNoOp).
type PhaseStartResult struct {
	Outcome   PhaseStartOutcome
	NextPhase feature.Phase // meaningful only when Outcome == PhaseSkipped
}

// Orchestrator coordinates feature lifecycle through port interfaces.
type Orchestrator struct {
	deps    Deps
	hooks   Hooks
	eventCh chan ports.Event
	mu      sync.RWMutex

	supervisor *phaseSupervisor

	// doneCh is closed by Shutdown to signal all emitters and consumers to
	// terminate. eventCh is deliberately NEVER closed — closing it would
	// race with concurrent emitters and could panic mid-send. Emitters
	// select on <-doneCh alongside their channel send so blocked sends
	// unblock on shutdown; consumers watch Done() to stop their receive
	// loops. stopOnce guarantees Shutdown's body runs exactly once.
	doneCh   chan struct{}
	stopOnce sync.Once

	// cycleWG tracks background goroutines launched by per-repo cycle
	// dispatch (StartRepoCycleImplement, StartCycleFinalReview,
	// startFeatureRefactor). Tests that drive these methods against a
	// t.TempDir() state directory must call WaitForCycles before the
	// test returns so the goroutine's writes don't race with TempDir
	// cleanup.
	cycleWG sync.WaitGroup

	// featureRebaseControls serialize feature-level rebase continuation checks
	// with Stop/interrupt per feature. The git operations themselves are not
	// cancellable through the RebaseOperator port, so a stop request waits for
	// the currently claimed rebase/push operation on that feature to return
	// before it lands in feature state.
	featureRebaseControls sync.Map

	// publishFn is a test hook. When nil the orchestrator calls o.Publish.
	// Tests can override this to intercept publish dispatch without touching
	// the publish implementation.
	publishFn func(featureID string) error

	// publishRepoFn is a test hook. When nil Publish calls o.publishRepo.
	// Tests can override this to isolate the Publish loop logic from the
	// per-repo pipeline logic.
	publishRepoFn func(featureID, repoName string) (string, error)

	// runMultiRepoImplFn is a test seam over
	// PhaseRunner.RunMultiRepoImplementation. The default (set in New()) is
	// a thin adapter that calls o.deps.PhaseRunner.RunMultiRepoImplementation.
	// Tests override this via SetRunMultiRepoImplFn to inject fake
	// channel-returning engines.
	//
	// The plan/resumeFromRepo/resumeSessionID params dropped in
	// SchemaVersionCurrent = 4 — the unified phase-implement loop derives its
	// repo set from PhaseScope and re-runs the interrupted unit from scratch.
	runMultiRepoImplFn func(
		f *feature.Feature,
		planPath string,
		kbInfos ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error)

	// runMultiRepoFinalReviewFn is a test seam over
	// PhaseRunner.RunMultiRepoFinalReview. The default (set in New()) is a
	// thin adapter that calls o.deps.PhaseRunner.RunMultiRepoFinalReview.
	// Tests override this via SetRunMultiRepoFinalReviewFn so the deferred
	// end-of-feature FR pass can be exercised without booting real sessions.
	runMultiRepoFinalReviewFn func(
		f *feature.Feature,
		kbInfos ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error)

	// runImplementationFn is a test seam over PhaseRunner.RunImplementation
	// (single-repo implementation loop). Tests override this via
	// SetRunImplementationFn to inject fake channel-returning engines.
	runImplementationFn func(
		f *feature.Feature,
		planPath string,
		kbInfos ...agent.KBInfo,
	) (chan *agent.LoopResult, error)

	// runRebaseLoopFn is a test seam over agent.RunRebaseLoop. The default
	// launches the production unified rebase loop; tests override it to
	// verify rebase gate routing without booting agent sessions.
	runRebaseLoopFn func(agent.RebaseLoopConfig, ports.SessionManager) (*agent.RebaseLoopResult, error)

	// worktreeFingerprintFn is a test seam for detecting whether a mounted
	// smart-rebase context repo changed during the agent loop.
	worktreeFingerprintFn func(worktreePath string) (string, error)
}

// SetRunImplementationFn installs a test seam that intercepts
// PhaseRunner.RunImplementation dispatch. Intended for tests only.
func (o *Orchestrator) SetRunImplementationFn(fn func(
	f *feature.Feature,
	planPath string,
	kbInfos ...agent.KBInfo,
) (chan *agent.LoopResult, error)) {
	o.runImplementationFn = fn
}

// New creates an Orchestrator. The eventCh is a bounded buffer (256).
func New(deps Deps, hooks Hooks) *Orchestrator {
	o := &Orchestrator{
		deps:    deps,
		hooks:   hooks,
		eventCh: make(chan ports.Event, eventChBuffer),
		doneCh:  make(chan struct{}),
	}
	o.supervisor = newPhaseSupervisor(phaseSupervisorConfig{
		Completion:        o,
		Sessions:          o.deps.Sessions,
		OnCompletionError: o.surfaceDispatchCompletionError,
	})
	o.runMultiRepoImplFn = func(
		f *feature.Feature,
		planPath string,
		kbInfos ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		if o.deps.PhaseRunner == nil {
			return nil, errors.New("phase runner not configured")
		}
		return o.deps.PhaseRunner.RunMultiRepoImplementation(
			f, planPath, kbInfos...,
		)
	}
	o.runMultiRepoFinalReviewFn = func(
		f *feature.Feature,
		kbInfos ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		if o.deps.PhaseRunner == nil {
			return nil, errors.New("phase runner not configured")
		}
		return o.deps.PhaseRunner.RunMultiRepoFinalReview(f, kbInfos...)
	}
	o.runRebaseLoopFn = agent.RunRebaseLoop
	o.worktreeFingerprintFn = gitWorktreeFingerprint
	return o
}

// Events returns a read-only channel of domain events.
func (o *Orchestrator) Events() <-chan ports.Event { return o.eventCh }

// Done returns a channel that is closed when Shutdown has been invoked.
// Consumers should select on Done alongside Events() to terminate receive
// loops cleanly. The channel is never sent to — only closed.
func (o *Orchestrator) Done() <-chan struct{} { return o.doneCh }

func (o *Orchestrator) featureRebaseControl(featureID string) *featureRebaseControl {
	if v, ok := o.featureRebaseControls.Load(featureID); ok {
		if control, ok := v.(*featureRebaseControl); ok {
			return control
		}
	}
	control := newFeatureRebaseControl()
	actual, _ := o.featureRebaseControls.LoadOrStore(featureID, control)
	if stored, ok := actual.(*featureRebaseControl); ok {
		return stored
	}
	return control
}

// WaitForCycles blocks until every background goroutine launched by the
// per-repo cycle entry points (StartRepoCycleImplement,
// StartCycleFinalReview, runRefactorLoop) has returned. Production
// callers do not need this — the orchestrator drives cycles to completion
// via its event loop. It exists for tests whose state directory is a
// t.TempDir(): without synchronizing on the goroutine, TempDir cleanup
// can race with in-flight writes from the implementation/refactor loop.
func (o *Orchestrator) WaitForCycles() { o.cycleWG.Wait() }

// SetPublishFn installs a test hook that intercepts publish dispatch in place
// of o.Publish. Intended for tests only — production code leaves it unset so
// startPublish falls through to o.Publish.
func (o *Orchestrator) SetPublishFn(fn func(featureID string) error) {
	o.publishFn = fn
}

// SetPublishRepoFn installs a test hook that intercepts per-repo publish
// dispatch in place of o.publishRepo. Intended for tests only.
func (o *Orchestrator) SetPublishRepoFn(fn func(featureID, repoName string) (string, error)) {
	o.publishRepoFn = fn
}

// SetRunMultiRepoImplFn installs a test seam that intercepts
// PhaseRunner.RunMultiRepoImplementation dispatch. Intended for tests only —
// production leaves the default adapter wired in New().
func (o *Orchestrator) SetRunMultiRepoImplFn(fn func(
	f *feature.Feature,
	planPath string,
	kbInfos ...agent.KBInfo,
) (chan *agent.OrchestratorResult, error)) {
	o.runMultiRepoImplFn = fn
}

// SetRunMultiRepoFinalReviewFn installs a test seam that intercepts the
// deferred end-of-feature Final Review dispatch. Intended for tests only —
// production leaves the default adapter wired in New().
func (o *Orchestrator) SetRunMultiRepoFinalReviewFn(fn func(
	f *feature.Feature,
	kbInfos ...agent.KBInfo,
) (chan *agent.OrchestratorResult, error)) {
	o.runMultiRepoFinalReviewFn = fn
}

// CreateFeature delegates to FeatureLifecycle.Create, fires the
// OnFeatureCreated hook, and emits a FeatureCreated event.
func (o *Orchestrator) CreateFeature(
	name, description string, repos []string,
	models config.ModelConfig, exitCriteria, inquireness string,
	images []string, opts ...feature.CreateOptions,
) (*feature.Feature, error) {
	f, err := o.deps.Lifecycle.Create(
		name, description, repos, models,
		exitCriteria, inquireness, images, opts...,
	)
	if err != nil {
		return nil, fmt.Errorf("creating feature: %w", err)
	}
	if o.hooks.OnFeatureCreated != nil {
		o.hooks.OnFeatureCreated(f)
	}
	o.emitEvent(ports.Event{
		Type: ports.FeatureCreated, FeatureID: f.ID, Feature: f,
	})
	return f, nil
}

// emitEvent sends an event on the channel. Non-blocking for non-critical events.
// Also drops events when shutdown has been signalled, so late emitters do not
// enqueue work consumers will never read.
func (o *Orchestrator) emitEvent(ev ports.Event) {
	select {
	case <-o.doneCh:
		return
	default:
	}
	select {
	case o.eventCh <- ev:
	case <-o.doneCh:
	default:
		// Drop non-critical events if channel is full.
	}
}

// emitEventBlocking sends an event on the channel, blocking until the
// consumer drains it. Used for critical lifecycle signals (PhaseCompleted,
// ReviewRequired, PublishCompleted, FeatureCompleted, FeatureFailed) that the
// TUI / downstream consumers must not miss. Selects on doneCh so a full
// buffer at shutdown does not deadlock the emitter goroutine.
func (o *Orchestrator) emitEventBlocking(ev ports.Event) {
	select {
	case o.eventCh <- ev:
	case <-o.doneCh:
	}
}

func (o *Orchestrator) emitShutdownStarted() {
	ev := ports.Event{Type: ports.RuntimeShutdownStarted}
	select {
	case o.eventCh <- ev:
		return
	default:
	}
	// Do not drain eventCh to make room: queued lifecycle events are part of
	// the read/SSE contract. eventCh is never closed, so a parked sender is
	// safe and will publish shutdown as soon as a consumer drains capacity.
	go func() {
		o.eventCh <- ev
	}()
}

// StartFeature starts a feature's phase pipeline.
// Determines the correct phase from the feature's state (first phase for
// StatusCreated, CurrentPhase for resumed features), handles the
// medium-pipeline Created → PlanReady pre-transition, fires
// OnFeatureStarted, emits FeatureStarted, and delegates to startPhase.
func (o *Orchestrator) StartFeature(featureID string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("loading feature: %w", err)
	}
	if f.Status == feature.StatusSettingUpWorktrees {
		if err := o.runSetupWith(false, featureID); err != nil {
			return err
		}
		f, err = o.deps.Lifecycle.Get(featureID)
		if err != nil {
			return fmt.Errorf("loading feature after setup: %w", err)
		}
	}

	phase := f.CurrentPhase
	// For new features, fall back to the pipeline's first phase.
	//
	// For interrupted features, the CurrentPhase field carries intent — it is
	// updated by each StartXxx transition — so normally we route on it. But
	// the zero value of feature.Phase is PhaseResearch (iota=0), which means
	// a yaml with a missing `current_phase` field deserializes to PhaseResearch
	// and would incorrectly skip earlier phases (KB/Inquire) for Large and
	// Moonshot pipelines. Disambiguate via StartedAt: every StartXxx transition
	// sets it, and the startup interrupt sweep preserves it (unlike KBStatus,
	// which InterruptAllRunning clears to force a KB rebuild). If StartedAt is
	// nil, no phase ever started, so CurrentPhase==0 must be the YAML zero
	// value — route to the canonical first phase. If StartedAt is set, the
	// feature was legitimately mid-Research when interrupted — honor CurrentPhase.
	if f.Status == feature.StatusCreated {
		phase = f.EffectivePipeline().FirstPhase()
	} else if f.Status == feature.StatusInterrupted && f.CurrentPhase == 0 {
		firstPhase := f.EffectivePipeline().FirstPhase()
		if firstPhase != feature.PhaseResearch && f.StartedAt == nil {
			phase = firstPhase
		}
	}

	// Medium pipeline pre-transition: Created → PlanReady.
	if phase == feature.PhasePlan && f.Status == feature.StatusCreated {
		if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			return ff.Transition(feature.StatusPlanReady)
		}); err != nil {
			return fmt.Errorf("transitioning to PlanReady: %w", err)
		}
	}

	if o.hooks.OnFeatureStarted != nil {
		o.hooks.OnFeatureStarted(featureID)
	}
	o.emitEvent(ports.Event{Type: ports.FeatureStarted, FeatureID: featureID})

	_, _, err = o.startPhase(featureID, phase)
	return err
}

// startPhase dispatches to the phase-specific starter. On PhaseStarted it
// emits a PhaseStarted event, fires OnPhaseStarted, and returns (phase,
// true, nil) — the requested phase that actually launched work. On
// PhaseSkipped it recursively dispatches to NextPhase without emitting any
// event for the skipped phase and returns whatever the recursive call
// resolves to (actual started phase or no-op). On PhaseNoOp it returns
// (0, false, nil) so callers can distinguish "nothing started" from an
// error.
//
// Callers that also emit FeatureAdvanced MUST use the returned started
// phase rather than the originally requested one; otherwise a skip chain
// (e.g. KB fresh -> Inquire) causes FeatureAdvanced to carry the wrong phase.
// Mismatched FeatureAdvanced signals are treated as the same class of contract
// break as missing ones.
func (o *Orchestrator) startPhase(featureID string, phase feature.Phase) (feature.Phase, bool, error) {
	var (
		result PhaseStartResult
		err    error
	)
	switch phase {
	case feature.PhaseKnowledgeBase:
		result, err = o.startKB(featureID)
	case feature.PhaseInquire:
		result, err = o.startInquire(featureID)
	case feature.PhaseResearch:
		result, err = o.startResearch(featureID)
	case feature.PhaseDesign:
		result, err = o.startDesign(featureID)
	case feature.PhasePlan:
		result, err = o.startPlan(featureID)
	case feature.PhaseImplement:
		result, err = o.startImplement(featureID)
	case feature.PhasePublish:
		result, err = o.startPublish(featureID)
	case feature.PhaseFinalReview, feature.PhaseReview:
		result, err = o.startFinalReview(featureID)
	default:
		return 0, false, fmt.Errorf("unknown phase %d", phase)
	}
	if err != nil {
		return 0, false, err
	}

	switch result.Outcome {
	case PhaseStarted:
		if o.hooks.OnPhaseStarted != nil {
			o.hooks.OnPhaseStarted(featureID, phase)
		}
		o.emitEvent(ports.Event{
			Type:      ports.PhaseStarted,
			FeatureID: featureID,
			Phase:     phase,
		})
		return phase, true, nil
	case PhaseSkipped:
		return o.startPhase(featureID, result.NextPhase)
	case PhaseNoOp:
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("unknown phase start outcome %d", result.Outcome)
	}
}

// startKB orchestrates the KB phase: per-repo freshness check, conditional
// skip, per-repo fan-out with mixed-fresh handling. Mirrors app.go:5357-5399
// and app.go:5402-5426.
//
// Idempotent for recovery resume: when the feature is already StatusBuildingKB
// (a crashed KB session was recovered via RecoveryResume), StartKnowledgeBase
// and InitKBStatus are skipped. The underlying
// StatusBuildingKB → StatusBuildingKB self-transition is invalid
// (feature/feature.go:488), and re-firing InitKBStatus would clobber per-repo
// progress that completed before the crash. Matches the startImplement /
// startReview idempotency pattern.
//
// Recovery bridge for the all-fresh skip: when a crashed KB session left the
// feature in StatusBuildingKB but every repo's KB artifacts are now fresh,
// CompleteKnowledgeBase is invoked before returning PhaseSkipped. Skipping
// straight to PhaseInquire would otherwise trigger startInquire's
// Lifecycle.StartInquire, and StatusBuildingKB → StatusInquiring is not a
// valid transition (feature/feature.go:488 lists only Created/Failed/
// Interrupted as successors of BuildingKB). CompleteKnowledgeBase moves
// BuildingKB → Created, so StartInquire's subsequent Created → Inquiring
// transition stays legal.
func (o *Orchestrator) startKB(featureID string) (PhaseStartResult, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return PhaseStartResult{}, fmt.Errorf("loading feature: %w", err)
	}

	if len(f.Repos) == 0 {
		if err := o.finalizeKBForSkip(f); err != nil {
			return PhaseStartResult{}, err
		}
		return PhaseStartResult{Outcome: PhaseSkipped, NextPhase: feature.PhaseInquire}, nil
	}

	// Check freshness for all repos.
	freshness := make(map[string]bool, len(f.Repos))
	activeKBRepos := o.activeKBSessionRepos(featureID)
	allFresh := true
	baseDir := o.stateDir()
	for _, repo := range f.Repos {
		if _, active := activeKBRepos[repo.Name]; active {
			freshness[repo.Name] = false
			allFresh = false
			continue
		}
		isFresh := false
		// Without a resolved base dir, agent.KBStateDir would read from CWD;
		// treat that as "not fresh" rather than consulting stray files.
		if baseDir != "" && !f.ForceKBRebuild {
			kbDir := agent.KBStateDir(baseDir, repo.Name)
			isFresh = agent.IsKBFresh(context.Background(), o.deps.CmdRunner, kbDir, repo.Path)
		}
		freshness[repo.Name] = isFresh
		if !isFresh {
			allFresh = false
		}
	}

	if allFresh {
		// Refresh codebase indexes for all repos, then skip to Inquire.
		if o.deps.PhaseRunner != nil {
			for _, repo := range f.Repos {
				_ = o.deps.PhaseRunner.RunCodebaseIndexForRepo(repo)
			}
		}
		if err := o.finalizeKBForSkip(f); err != nil {
			return PhaseStartResult{}, err
		}
		return PhaseStartResult{Outcome: PhaseSkipped, NextPhase: feature.PhaseInquire}, nil
	}

	// Mixed case: transition, init tracking, fan out. Skip the transition and
	// per-repo-status init when already in StatusBuildingKB (recovery resume).
	if f.Status != feature.StatusBuildingKB {
		if err := o.deps.Lifecycle.StartKnowledgeBase(featureID); err != nil {
			return PhaseStartResult{}, fmt.Errorf("start knowledge base: %w", err)
		}
		if err := o.deps.Lifecycle.InitKBStatus(featureID); err != nil {
			return PhaseStartResult{}, fmt.Errorf("init KB status: %w", err)
		}
	}

	if o.deps.PhaseRunner != nil {
		for _, repo := range f.Repos {
			if sessionID, active := activeKBRepos[repo.Name]; active {
				o.superviseSingleShotPhaseSession(featureID, sessionID, feature.PhaseKnowledgeBase)
				continue
			}
			if freshness[repo.Name] {
				// Fresh repo in the mixed case: refresh codebase index,
				// immediately mark completed in per-repo tracking.
				_ = o.deps.PhaseRunner.RunCodebaseIndexForRepo(repo)
				_ = o.deps.Lifecycle.MarkRepoKBCompleted(featureID, repo.Name)
				continue
			}
			if baseDir != "" {
				kbDir := agent.KBStateDir(baseDir, repo.Name)
				if err := o.recordReadOnlyRepoBaseline(context.Background(), f, kbDir, repo.Name); err != nil {
					return PhaseStartResult{}, fmt.Errorf("record KB read-only repo baseline for %s: %w", repo.Name, err)
				}
			}
			sessionID, err := o.deps.PhaseRunner.RunKnowledgeBaseForRepo(f, repo)
			if err != nil {
				if errors.Is(err, agent.ErrKBLocked) {
					o.markKBWaiting(featureID, baseDir, repo.Name)
					return PhaseStartResult{Outcome: PhaseStarted}, nil
				}
				return PhaseStartResult{}, fmt.Errorf("run KB for repo %s: %w", repo.Name, err)
			}
			o.superviseSingleShotPhaseSession(featureID, sessionID, feature.PhaseKnowledgeBase)
		}
	}

	// Started cleanly (or already in progress) — clear any stale wait message
	// from a prior locked attempt.
	o.clearKBWaitMessage(featureID)
	return PhaseStartResult{Outcome: PhaseStarted}, nil
}

func (o *Orchestrator) activeKBSessionRepos(featureID string) map[string]string {
	active := make(map[string]string)
	if o.deps.Sessions == nil {
		return active
	}
	for _, s := range o.deps.Sessions.FeatureSessions(featureID) {
		if s == nil || !s.IsActive() || s.Phase() != feature.PhaseKnowledgeBase {
			continue
		}
		repoName := s.RepoName()
		if repoName == "" {
			repoName = agent.RepoNameFromKBSession(s.ID())
		}
		if repoName != "" {
			active[repoName] = s.ID()
		}
	}
	return active
}

func (o *Orchestrator) activePhaseSessionID(featureID string, phase feature.Phase) string {
	if o.deps.Sessions == nil {
		return ""
	}
	for _, s := range o.deps.Sessions.FeatureSessions(featureID) {
		if s == nil || !s.IsActive() || s.Phase() != phase {
			continue
		}
		return s.ID()
	}
	return ""
}

// markKBWaiting records that the feature couldn't acquire the KB lock for the
// given repo because another feature owns it. Sets KBWaitMessage so the
// dashboard can render the wait state and the attach handler can explain why
// no sessions are available. Best-effort: lookup failures fall back to a
// generic message.
func (o *Orchestrator) markKBWaiting(featureID, baseDir, repoName string) {
	owner := ""
	if baseDir != "" {
		owner = agent.ReadKBLockOwner(agent.KBStateDir(baseDir, repoName))
	}
	ownerLabel := owner
	if owner != "" && o.deps.Lifecycle != nil {
		if other, err := o.deps.Lifecycle.Get(owner); err == nil && other != nil && other.Name != "" {
			ownerLabel = other.Name
		}
	}
	msg := fmt.Sprintf("Waiting for KB build on repo %q", repoName)
	if ownerLabel != "" {
		msg = fmt.Sprintf("Waiting for KB build on repo %q by feature %q", repoName, ownerLabel)
	}
	if o.deps.Store == nil {
		return
	}
	_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		ff.KBWaitMessage = msg
		return nil
	})
}

// clearKBWaitMessage drops any KB lock-wait note. Called once startKB has
// successfully fanned out (or determined no work remains) so the dashboard
// stops advertising a wait that no longer applies.
func (o *Orchestrator) clearKBWaitMessage(featureID string) {
	if o.deps.Store == nil {
		return
	}
	_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		if ff.KBWaitMessage == "" {
			return nil
		}
		ff.KBWaitMessage = ""
		return nil
	})
}

// wakeKBWaiters re-dispatches the KB phase for every feature currently parked
// in StatusBuildingKB with a non-empty KBWaitMessage. Called from
// onKBCompleted (success and failure paths) once the lock-holder's session
// cleanup has released the kb.lock file. startKB's recovery-resume guard
// makes the re-entry idempotent: features already past BuildingKB are
// skipped, and repos whose KB completed during the previous attempt remain
// marked completed.
//
// Routes through startPhase (not startKB directly) so that a PhaseSkipped
// outcome — common when the lock holder built every repo the waiter needs,
// leaving allFresh=true on the re-check — recurses into the next phase
// (Inquire) instead of stranding the waiter in StatusCreated/PhaseKnowledgeBase
// after finalizeKBForSkip's BuildingKB → Created transition.
func (o *Orchestrator) wakeKBWaiters(skipFeatureID string) {
	if o.deps.Lifecycle == nil {
		return
	}
	features, err := o.deps.Lifecycle.List()
	if err != nil {
		// PartialLoadError carries the features that DID load alongside the
		// per-ID load warnings. Treat it as soft: a single malformed feature
		// directory must not block legitimate waiters from being woken when
		// the lock holder finishes.
		var ple *feature.PartialLoadError
		if !errors.As(err, &ple) {
			return
		}
	}
	for _, ff := range features {
		if ff == nil || ff.ID == skipFeatureID {
			continue
		}
		if ff.Status != feature.StatusBuildingKB || ff.KBWaitMessage == "" {
			continue
		}
		if _, _, err := o.startPhase(ff.ID, feature.PhaseKnowledgeBase); err != nil {
			// A wakeup failure for one waiter must not block other waiters
			// or the completing feature's advance. Surface via the event
			// stream so it's diagnosable instead of vanishing silently.
			o.emitEvent(ports.Event{
				Type:      ports.FeatureFailed,
				FeatureID: ff.ID,
				Message:   fmt.Sprintf("KB wakeup re-dispatch failed: %v", err),
			})
		}
	}
}

// finalizeKBForSkip bridges a recovered StatusBuildingKB feature out of the KB
// phase when startKB has decided to skip to Inquire (either because the feature
// has no repos or because every repo is already fresh). StatusBuildingKB →
// StatusInquiring is not listed in validTransitions (feature/feature.go:488),
// so dispatching straight to startInquire would fail with
// "invalid transition from building_kb to inquiring". CompleteKnowledgeBase
// moves the feature from StatusBuildingKB to StatusCreated, which is the
// bridge state that keeps startInquire's subsequent Created → Inquiring
// transition legal. No-op when the feature is already in any non-BuildingKB
// status (fresh features never entered BuildingKB).
func (o *Orchestrator) finalizeKBForSkip(f *feature.Feature) error {
	if f == nil || f.Status != feature.StatusBuildingKB {
		return nil
	}
	if err := o.deps.Lifecycle.CompleteKnowledgeBase(f.ID); err != nil {
		return fmt.Errorf("complete knowledge base: %w", err)
	}
	return nil
}

// startInquire starts the Inquire phase. Idempotent for recovery resume: when
// the feature is already StatusInquiring, StartInquire is skipped because the
// StatusInquiring → StatusInquiring self-transition is invalid
// (feature/feature.go:489).
func (o *Orchestrator) startInquire(featureID string) (PhaseStartResult, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return PhaseStartResult{}, fmt.Errorf("loading feature: %w", err)
	}
	if f.Status != feature.StatusInquiring {
		if err := o.deps.Lifecycle.StartInquire(featureID); err != nil {
			return PhaseStartResult{}, fmt.Errorf("start inquire: %w", err)
		}
	}
	kbInfos := o.computeKBInfos(f)
	if o.deps.PhaseRunner != nil {
		if sessionID := o.activePhaseSessionID(featureID, feature.PhaseInquire); sessionID != "" {
			o.superviseSingleShotPhaseSession(featureID, sessionID, feature.PhaseInquire)
			return PhaseStartResult{Outcome: PhaseStarted}, nil
		}
		if err := o.recordReadOnlyRepoBaseline(context.Background(), f, o.artifactReadOnlyGuardDir(f, "inquire")); err != nil {
			return PhaseStartResult{}, fmt.Errorf("record inquire read-only repo baseline: %w", err)
		}
		sessionID, err := o.deps.PhaseRunner.RunInquire(f, kbInfos...)
		if err != nil {
			return PhaseStartResult{}, fmt.Errorf("run inquire: %w", err)
		}
		o.superviseSingleShotPhaseSession(featureID, sessionID, feature.PhaseInquire)
	}
	return PhaseStartResult{Outcome: PhaseStarted}, nil
}

// startResearch starts the Research phase. Research is always driven by
// the questions artifact produced by Inquire — this is enforced here so a
// feature with no inquire artifact fails loudly instead of silently falling
// back to a description-driven prompt that leaks ticket intent into research.
//
// Idempotent for recovery resume: when the feature is already
// StatusResearching, StartResearch is skipped because the
// StatusResearching → StatusResearching self-transition is invalid
// (feature/feature.go:487).
func (o *Orchestrator) startResearch(featureID string) (PhaseStartResult, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return PhaseStartResult{}, fmt.Errorf("loading feature: %w", err)
	}
	if f.Status != feature.StatusResearching {
		if err := o.deps.Lifecycle.StartResearch(featureID); err != nil {
			return PhaseStartResult{}, fmt.Errorf("start research: %w", err)
		}
	}
	kbInfos := o.computeKBInfos(f)
	if o.deps.PhaseRunner != nil {
		if sessionID := o.activePhaseSessionID(featureID, feature.PhaseResearch); sessionID != "" {
			o.superviseSingleShotPhaseSession(featureID, sessionID, feature.PhaseResearch)
			return PhaseStartResult{Outcome: PhaseStarted}, nil
		}
		questionsPath := o.resolveArtifactPath(f, "inquire")
		if questionsPath == "" {
			return PhaseStartResult{}, errors.New("no inquire artifact found; cannot proceed to research")
		}
		if err := o.recordReadOnlyRepoBaseline(context.Background(), f, o.artifactReadOnlyGuardDir(f, "research")); err != nil {
			return PhaseStartResult{}, fmt.Errorf("record research read-only repo baseline: %w", err)
		}
		sessionID, err := o.deps.PhaseRunner.RunResearchFromQuestions(f, questionsPath, kbInfos...)
		if err != nil {
			return PhaseStartResult{}, fmt.Errorf("run research from questions: %w", err)
		}
		o.superviseSingleShotPhaseSession(featureID, sessionID, feature.PhaseResearch)
	}
	return PhaseStartResult{Outcome: PhaseStarted}, nil
}

// startDesign starts the Design phase. Fails if the research
// artifact is missing. Collects QA files from inquire/research. Idempotent
// for recovery resume: when the feature is already StatusDesigning,
// StartDesign is skipped because the
// StatusDesigning → StatusDesigning self-transition is invalid
// (feature/feature.go:492).
func (o *Orchestrator) startDesign(featureID string) (PhaseStartResult, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return PhaseStartResult{}, fmt.Errorf("loading feature: %w", err)
	}
	if f.Status != feature.StatusDesigning {
		if err := o.deps.Lifecycle.StartDesign(featureID); err != nil {
			return PhaseStartResult{}, fmt.Errorf("start design: %w", err)
		}
	}
	researchPath := o.resolveArtifactPath(f, "research")
	if researchPath == "" {
		return PhaseStartResult{}, errors.New("research phase did not produce an artifact; cannot proceed to design")
	}
	// QA file paths from inquire/research specifically (design's own qa
	// doesn't exist yet — matches app.go:5577-5582).
	var qaFilePaths []string
	baseDir := o.stateDir()
	if baseDir != "" {
		refPrefix := f.RefactorPrefix()
		runDir := agent.ActiveRunDir(baseDir, f)
		for _, phase := range []string{"inquire", "research"} {
			qaPath := filepath.Join(runDir, refPrefix, phase, "qa-answers.md")
			if _, statErr := os.Stat(qaPath); statErr == nil {
				qaFilePaths = append(qaFilePaths, qaPath)
			}
		}
	}
	kbInfos := o.computeKBInfos(f)
	if o.deps.PhaseRunner != nil {
		if sessionID := o.activePhaseSessionID(featureID, feature.PhaseDesign); sessionID != "" {
			o.superviseSingleShotPhaseSession(featureID, sessionID, feature.PhaseDesign)
			return PhaseStartResult{Outcome: PhaseStarted}, nil
		}
		if err := o.recordReadOnlyRepoBaseline(context.Background(), f, o.artifactReadOnlyGuardDir(f, "design")); err != nil {
			return PhaseStartResult{}, fmt.Errorf("record design read-only repo baseline: %w", err)
		}
		sessionID, err := o.deps.PhaseRunner.RunDesign(f, researchPath, qaFilePaths, kbInfos...)
		if err != nil {
			return PhaseStartResult{}, fmt.Errorf("run design: %w", err)
		}
		o.superviseSingleShotPhaseSession(featureID, sessionID, feature.PhaseDesign)
	}
	return PhaseStartResult{Outcome: PhaseStarted}, nil
}

// startPlan starts the Plan phase. Delegates to startRoadmapPhasePlan when
// CurrentRoadmapPhase > 0. Otherwise resolves design → research → empty
// (medium pipeline OK; other pipelines fail on missing artifact). Idempotent
// for recovery resume: when the feature is already StatusPlanning,
// StartPlanning is skipped because the StatusPlanning → StatusPlanning
// self-transition is invalid (feature/feature.go:494). Matches the
// idempotent guard in startRoadmapPhasePlan.
func (o *Orchestrator) startPlan(featureID string) (PhaseStartResult, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return PhaseStartResult{}, fmt.Errorf("loading feature: %w", err)
	}
	if f.CurrentRoadmapPhase > 0 {
		return o.startRoadmapPhasePlan(featureID, f)
	}
	if f.Status != feature.StatusPlanning {
		if err := o.deps.Lifecycle.StartPlanning(featureID); err != nil {
			return PhaseStartResult{}, fmt.Errorf("start planning: %w", err)
		}
	}

	inputArtifactPath := o.resolveArtifactPath(f, "design")
	if inputArtifactPath == "" {
		inputArtifactPath = o.resolveArtifactPath(f, "research")
	}
	if inputArtifactPath == "" && f.EffectivePipeline() != feature.PipelineMedium {
		return PhaseStartResult{}, errors.New("no design doc or research artifact found; cannot proceed to planning")
	}

	qaFilePaths := o.collectQAFilePaths(f, f.RefactorPrefix())
	kbInfos := o.computeKBInfos(f)
	if o.deps.PhaseRunner != nil {
		if err := o.recordReadOnlyRepoBaseline(context.Background(), f, o.planReadOnlyGuardDir(f)); err != nil {
			return PhaseStartResult{}, fmt.Errorf("record plan read-only repo baseline: %w", err)
		}
		resultCh, err := o.deps.PhaseRunner.RunPlanningWithValidation(f, inputArtifactPath, qaFilePaths, kbInfos...)
		if err != nil {
			return PhaseStartResult{}, fmt.Errorf("run planning: %w", err)
		}
		o.phaseSupervisor().supervisePlanLoop(featureID, resultCh)
	}
	return PhaseStartResult{Outcome: PhaseStarted}, nil
}

// startRoadmapPhasePlan starts per-phase planning for the current roadmap
// phase. Mirrors app.go:5672-5758. Conditionally transitions via
// StartPlanning only if the feature is not already StatusPlanning.
func (o *Orchestrator) startRoadmapPhasePlan(featureID string, f *feature.Feature) (PhaseStartResult, error) {
	if f.Status != feature.StatusPlanning {
		if err := o.deps.Lifecycle.StartPlanning(featureID); err != nil {
			return PhaseStartResult{}, fmt.Errorf("start planning: %w", err)
		}
		// Re-load feature after transition
		reloaded, err := o.deps.Lifecycle.Get(featureID)
		if err != nil {
			return PhaseStartResult{}, fmt.Errorf("reload feature: %w", err)
		}
		f = reloaded
	}

	roadmapPath := o.resolveArtifactPath(f, "roadmap")
	if roadmapPath == "" {
		return PhaseStartResult{}, errors.New("no roadmap artifact found")
	}

	roadmapData, readErr := os.ReadFile(roadmapPath)
	if readErr != nil {
		return PhaseStartResult{}, fmt.Errorf("read roadmap: %w", readErr)
	}
	phases, parseErr := agent.ParseRoadmap(string(roadmapData))
	if parseErr != nil {
		return PhaseStartResult{}, fmt.Errorf("parse roadmap: %w", parseErr)
	}

	var currentPhase agent.RoadmapPhase
	for _, p := range phases {
		if p.Number == f.CurrentRoadmapPhase {
			currentPhase = p
			break
		}
	}
	if currentPhase.Number == 0 {
		return PhaseStartResult{}, fmt.Errorf("phase %d not found in roadmap", f.CurrentRoadmapPhase)
	}

	qaFilePaths := o.collectQAFilePaths(f, f.RefactorPrefix())

	var priorPhasePlanPaths []string
	for i := 1; i < currentPhase.Number; i++ {
		key := fmt.Sprintf("phase-%d-plan", i)
		if path := o.resolveArtifactPath(f, key); path != "" {
			priorPhasePlanPaths = append(priorPhasePlanPaths, path)
		}
	}

	kbInfos := o.computeKBInfos(f)
	if o.deps.PhaseRunner != nil {
		if err := o.recordReadOnlyRepoBaseline(context.Background(), f, o.planReadOnlyGuardDir(f)); err != nil {
			return PhaseStartResult{}, fmt.Errorf("record phase plan read-only repo baseline: %w", err)
		}
		resultCh, err := o.deps.PhaseRunner.RunPhasePlanning(f, roadmapPath, currentPhase, qaFilePaths, priorPhasePlanPaths, kbInfos...)
		if err != nil {
			return PhaseStartResult{}, fmt.Errorf("run phase planning: %w", err)
		}
		o.phaseSupervisor().supervisePlanLoop(featureID, resultCh)
	}
	return PhaseStartResult{Outcome: PhaseStarted}, nil
}

// startImplement starts the Implementation phase. Resolves plan path through
// the cascade, initializes repo impl tracking, persists the execution plan
// fallback, and then delegates engine invocation and result routing to
// StartMultiRepoImplementation — which is the single code path for
// multi-repo implementation runs (both fresh starts and recovery relaunches).
// Mirrors app.go:5775-5907.
//
// Idempotent for recovery resume: when the feature is already
// StatusImplementing (e.g. a PTY session crashed mid-implement and recovery
// resume preserved state), StartImplementation is skipped. The underlying
// transition Implementing → Implementing is not valid
// (feature/feature.go:496), and re-firing StartImplementation would also
// reset CurrentIteration and ActivePhaseStart, clobbering state we want to
// preserve across the crash. Crash recovery re-runs the interrupted unit
// from scratch with a fresh Claude session; durable state on disk is the
// resume scaffolding.
func (o *Orchestrator) startImplement(featureID string) (PhaseStartResult, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return PhaseStartResult{}, fmt.Errorf("loading feature: %w", err)
	}

	if f.Status != feature.StatusImplementing {
		if err := o.deps.Lifecycle.StartImplementation(featureID); err != nil {
			return PhaseStartResult{}, fmt.Errorf("start implementation: %w", err)
		}
		reloaded, err := o.deps.Lifecycle.Get(featureID)
		if err != nil {
			return PhaseStartResult{}, fmt.Errorf("loading feature: %w", err)
		}
		f = reloaded
	}

	planPath := o.resolvePlanPath(f)
	if planPath == "" {
		return PhaseStartResult{}, errors.New("plan phase did not produce an artifact; cannot proceed to implementation")
	}

	// Persist the resolved plan so restarts reuse it.
	if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		if ff.Artifacts == nil {
			ff.Artifacts = make(map[string]string)
		}
		ff.Artifacts["plan"] = planPath
		return nil
	}); err != nil {
		return PhaseStartResult{}, fmt.Errorf("persist plan path: %w", err)
	}

	// Init repo impl tracking unconditionally; crash recovery re-runs the
	// interrupted unit from scratch.
	if err := o.deps.Lifecycle.InitRepoImpl(featureID); err != nil {
		return PhaseStartResult{}, fmt.Errorf("init repo impl: %w", err)
	}

	// The unified phase-implement loop derives its repo set from PhaseScope
	// (per-Task `**Repo:** <name>` tags); per-phase execution-order.yaml is
	// gone in SchemaVersionCurrent = 4.
	if err := o.StartMultiRepoImplementation(featureID); err != nil {
		return PhaseStartResult{}, fmt.Errorf("run implementation: %w", err)
	}
	return PhaseStartResult{Outcome: PhaseStarted}, nil
}

// startPublish is a thin dispatcher mirroring app.go:1197-1224. Returns
// PhaseNoOp when the feature is not publishable or auto-publish is disabled;
// delegates to o.Publish otherwise.
func (o *Orchestrator) startPublish(featureID string) (PhaseStartResult, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return PhaseStartResult{}, fmt.Errorf("loading feature: %w", err)
	}
	if !f.IsPublishable() {
		return PhaseStartResult{Outcome: PhaseNoOp}, nil
	}
	if !f.Checkpoints.AutoPublish() {
		return PhaseStartResult{Outcome: PhaseNoOp}, nil
	}
	publishFn := o.publishFn
	if publishFn == nil {
		publishFn = o.Publish
	}
	if err := publishFn(featureID); err != nil {
		return PhaseStartResult{}, err
	}
	return PhaseStartResult{Outcome: PhaseStarted}, nil
}

// startFinalReview re-enters the deferred end-of-feature Final Review pass.
// Used by the [r] restart path when the feature was interrupted while
// StatusFinalReviewing — runDeferredFinalReview's MarkFinalReviewReady
// transition is now valid from StatusInterrupted (validTransitions).
// On FR success, the post-FR advancement (MarkCodeReady or auto-publish)
// runs via advanceAfterFinalReview so the feature reaches the same terminal
// state the original onMultiReposPassed path produces.
func (o *Orchestrator) startFinalReview(featureID string) (PhaseStartResult, error) {
	if err := o.runDeferredFinalReview(featureID); err != nil {
		if errors.Is(err, errFinalReviewInterrupted) {
			return PhaseStartResult{Outcome: PhaseStarted}, nil
		}
		return PhaseStartResult{}, err
	}
	if err := o.advanceAfterFinalReview(featureID); err != nil {
		return PhaseStartResult{}, err
	}
	return PhaseStartResult{Outcome: PhaseStarted}, nil
}

// InterruptFeature stops all sessions for a feature and clears pending help
// and permission queue flags. Normal phase work and non-interactive
// post-publish repo cycles transition the feature to StatusInterrupted; tweak-
// only post-publish cycles keep their published/code-ready feature status.
// Does NOT clear KBStatus — preserve per-repo KB tracking for resume.
//
// Ordering matters: the Interrupted transition is committed BEFORE sessions
// are stopped so a racing SessionDoneMsg → onKBCompleted finds the feature
// already at StatusInterrupted and short-circuits its failure branch.
// Otherwise the stopped session's last assistant text would surface
// as failure_type=session_crash and beat FeatureInterrupted to emission.
func (o *Orchestrator) InterruptFeature(featureID string) error {
	rebaseControl := o.featureRebaseControl(featureID)
	rebaseControl.mu.Lock()
	rebaseControl.stopping = true
	for rebaseControl.active > 0 {
		rebaseControl.cond.Wait()
	}
	rebaseControl.mu.Unlock()

	featureInterrupted := false
	if f, err := o.deps.Lifecycle.Get(featureID); err == nil &&
		(f.Status == feature.StatusPublished || f.Status == feature.StatusCodeReady) &&
		hasActiveRepoCycles(f) {
		interruptFeature := hasInterruptibleRepoCycles(f)
		if err := o.interruptActiveRepoCycles(featureID, interruptFeature); err != nil {
			return fmt.Errorf("interrupt active repo cycles: %w", err)
		}
		featureInterrupted = interruptFeature
	} else {
		// Transition to interrupted FIRST so racing completion handlers
		// (onKBCompleted, onPhaseCompletedDefault, …) observe the terminal
		// state and skip their failure paths.
		if err := o.deps.Lifecycle.Transition(featureID, feature.StatusInterrupted); err != nil {
			return fmt.Errorf("transition to interrupted: %w", err)
		}
		featureInterrupted = true
	}

	// Stop any active sessions for this feature.
	if o.deps.Sessions != nil {
		sessions := o.deps.Sessions.FeatureSessions(featureID)
		for _, s := range sessions {
			_ = o.deps.Sessions.StopSession(s.ID())
		}
	}

	// Clear pending help/permission requests.
	if err := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		for i := range f.HelpQueue {
			if f.HelpQueue[i].Pending {
				f.HelpQueue[i].Pending = false
			}
		}
		for i := range f.PermissionsQueue {
			if f.PermissionsQueue[i].Pending {
				f.PermissionsQueue[i].Pending = false
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("clear pending flags: %w", err)
	}

	if featureInterrupted && o.hooks.OnFeatureInterrupted != nil {
		o.hooks.OnFeatureInterrupted(featureID)
	}
	if featureInterrupted {
		o.emitEvent(ports.Event{Type: ports.FeatureInterrupted, FeatureID: featureID})
	}
	return nil
}

// InterruptAllRunning iterates all features; running ones are interrupted
// (stopping sessions, clearing pending flags, transitioning to interrupted)
// AND additionally have KBStatus cleared to match the startup sweep.
// Non-running features (Published, CodeReady) with active non-interactive
// repo cycles (rebase/review-comments/refactor) are interrupted at the
// feature level so the dashboard keeps them in the active bucket. Interactive
// tweak-only cycles keep the pre-existing published/code-ready status and
// just have their cycle state marked interrupted. KBStatus is preserved for
// every post-publish cycle shape. CodeReady is the manual_publish=true shape
// where a tweak/rebase cycle can be in flight while the feature waits for the
// user to publish.
func (o *Orchestrator) InterruptAllRunning() error {
	features, listErr := o.deps.Store.List()
	if listErr != nil {
		// PartialLoadError carries the features that DID load alongside the
		// per-ID load warnings. Treat it as soft: process the healthy features
		// instead of bailing on the whole sweep, otherwise a single corrupt
		// feature.yaml leaves every running feature stuck in its pre-quit
		// status with pending help/permission entries on next startup. Any
		// other error (e.g. the features dir itself is unreadable) remains
		// fatal.
		var ple *feature.PartialLoadError
		if !errors.As(listErr, &ple) {
			return fmt.Errorf("list features: %w", listErr)
		}
	}

	var errs []error
	for _, f := range features {
		switch {
		case f.Status == feature.StatusSettingUpWorktrees:
			// Worktree setup is pre-phase lifecycle work, not a session-backed
			// phase. Startup reconciliation converts stale setup to Failed;
			// the interrupt sweep should not attempt an invalid Interrupted
			// transition here.
			continue
		case f.Status.IsRunning():
			if err := o.InterruptFeature(f.ID); err != nil {
				errs = append(errs, fmt.Errorf("interrupt %s: %w", f.ID, err))
				continue
			}
			// Startup-sweep parity: clear KBStatus to force full rebuild on
			// restart. InterruptFeature alone preserves KBStatus for resume;
			// the sweep layers on this additional reset.
			if err := o.deps.Store.Modify(f.ID, func(ff *feature.Feature) error {
				ff.KBStatus = nil
				return nil
			}); err != nil {
				errs = append(errs, fmt.Errorf("clear KBStatus for %s: %w", f.ID, err))
			}
		case (f.Status == feature.StatusPublished || f.Status == feature.StatusCodeReady) && hasActiveRepoCycles(f):
			if hasInterruptibleRepoCycles(f) {
				if err := o.InterruptFeature(f.ID); err != nil {
					errs = append(errs, fmt.Errorf("interrupt repo cycles for %s: %w", f.ID, err))
				}
				continue
			}
			if err := o.interruptActiveRepoCycles(f.ID, false); err != nil {
				errs = append(errs, fmt.Errorf("mark interrupted cycles for %s: %w", f.ID, err))
			}
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// hasActiveRepoCycles returns true if the feature has any running/reviewing
// post-publish cycle. Legacy cycles live in RepoCycles; feature-level rebase,
// review-comment, and refactor flows live only in ActiveCycle.
func hasActiveRepoCycles(f *feature.Feature) bool {
	if hasActiveFeatureLevelInterruptibleCycle(f) {
		return true
	}
	for _, rc := range f.RepoCycles {
		if rc != nil && (rc.Status == feature.RepoCycleRunning || rc.Status == feature.RepoCycleReviewing) {
			return true
		}
	}
	return false
}

func hasInterruptibleRepoCycles(f *feature.Feature) bool {
	if hasActiveFeatureLevelInterruptibleCycle(f) {
		return true
	}
	for _, rc := range f.RepoCycles {
		if rc == nil {
			continue
		}
		switch rc.Type {
		case feature.CycleRebase, feature.CycleReviewComments, feature.CycleRefactor:
		default:
			continue
		}
		if rc.Status == feature.RepoCycleRunning || rc.Status == feature.RepoCycleReviewing {
			return true
		}
	}
	return false
}

func hasActiveFeatureLevelInterruptibleCycle(f *feature.Feature) bool {
	if f == nil || f.ActiveCycle == nil {
		return false
	}
	if f.ActiveCycle.Status != feature.RepoCycleRunning && f.ActiveCycle.Status != feature.RepoCycleReviewing {
		return false
	}
	cycleType := f.ActiveCycle.Type
	if cycleType == "" {
		cycleType = f.ActiveCycleType()
	}
	switch cycleType {
	case feature.CycleRebase, feature.CycleReviewComments, feature.CycleRefactor:
		return true
	default:
		return false
	}
}

func (o *Orchestrator) interruptActiveRepoCycles(featureID string, interruptFeature bool) error {
	return o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		for _, rc := range ff.RepoCycles {
			if rc == nil {
				continue
			}
			if rc.Status == feature.RepoCycleRunning || rc.Status == feature.RepoCycleReviewing {
				rc.Status = feature.RepoCycleInterrupted
				rc.LastError = ""
			}
		}
		if ff.ActiveCycle != nil &&
			(ff.ActiveCycle.Status == feature.RepoCycleRunning || ff.ActiveCycle.Status == feature.RepoCycleReviewing) {
			ff.ActiveCycle.Status = feature.RepoCycleInterrupted
			ff.ActiveCycle.LastError = ""
		}
		if interruptFeature {
			ff.Status = feature.StatusInterrupted
			ff.CurrentPhase = feature.PhasePublish
			ff.LastError = ""
			ff.FailureType = ""
		}
		for i := range ff.HelpQueue {
			if ff.HelpQueue[i].Pending {
				ff.HelpQueue[i].Pending = false
			}
		}
		for i := range ff.PermissionsQueue {
			if ff.PermissionsQueue[i].Pending {
				ff.PermissionsQueue[i].Pending = false
			}
		}
		return nil
	})
}

// HandlePhaseCompletion dispatches a phase-completion result to the
// appropriate per-phase handler. Mirrors app.go:2737-2910 (the multi-message
// fanout in the TUI's Update loop).
//
// Active-cycle handling: completion handlers observing f.ActiveCycleType != ""
// take the cycle-active early-return path — minimum mutation, emit
// PhaseCompleted, return. Cycle-specific handlers own cycle coordination.
func (o *Orchestrator) HandlePhaseCompletion(featureID string, input PhaseCompletionInput) error {
	switch input.Phase {
	case feature.PhaseKnowledgeBase:
		return o.onKBCompleted(featureID, input)
	case feature.PhaseInquire:
		return o.onArtifactPhaseCompleted(featureID, input, "inquire", o.deps.Lifecycle.CompleteInquire)
	case feature.PhaseResearch:
		return o.onArtifactPhaseCompleted(featureID, input, "research", o.deps.Lifecycle.CompleteResearch)
	case feature.PhaseDesign:
		// Validate against the legacy "design" on-disk subdirectory but
		// persist the canonical Design artifact key so downstream consumers
		// resolve it through feature.Feature.DesignArtifactPath().
		return o.onArtifactPhaseCompletedWithKey(featureID, input, "design", feature.DesignArtifactKey, o.deps.Lifecycle.CompleteDesign)
	case feature.PhasePlan:
		return o.onPlanLoopDone(featureID, input.PlanResult)
	case feature.PhaseImplement:
		return o.onImplementCompleted(featureID, input)
	default:
		return fmt.Errorf("unknown phase %d", input.Phase)
	}
}

// HandleReviewDecision handles a user decision emitted from a review menu or
// gate. Routes the decision to the appropriate pre-dispatch work (phase
// transitions, rewind, execution-plan population) and then dispatches the
// target phase starter via startPhase. Emits FeatureAdvanced.
func (o *Orchestrator) HandleReviewDecision(featureID string, d ReviewDecision) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("loading feature %s: %w", featureID, err)
	}
	if f.Status == feature.StatusInterrupted || f.Status == feature.StatusFailed {
		return nil
	}

	if d.IsRewind && d.Decision == "proceed" {
		return o.ProceedFromRewindReview(featureID, d.TargetPhase)
	}

	switch d.Decision {
	case "proceed":
		return o.reviewProceed(featureID, f, d)
	case "iterate":
		return o.reviewIterate(featureID, f, d)
	default:
		return fmt.Errorf("unknown review decision %q", d.Decision)
	}
}

// reviewProceed handles "proceed" from a gate or plan-review menu. Phase
// dispatch happens internally via startPhase after lifecycle-state mutation
// and any side-effect artifact population (execution plan, roadmap phase
// count). Each branch both prepares state and dispatches the appropriate
// follow-up phase so the orchestrator owns the full unwind.
func (o *Orchestrator) reviewProceed(featureID string, f *feature.Feature, d ReviewDecision) error {
	if err := o.clearReviewGate(featureID); err != nil {
		return err
	}

	// Per-phase plan approval (roadmap phase plan): transition to ImplementReady,
	// then dispatch PhaseImplement. The per-phase execution-order.yaml is
	// read fresh from disk by StartMultiRepoImplementation; no pre-flight
	// populate step is needed (per SchemaVersionCurrent = 3).
	if d.PhasePlan {
		if err := o.deps.Lifecycle.StartRoadmapPhaseImplementation(featureID); err != nil {
			return fmt.Errorf("start roadmap phase implementation: %w", err)
		}
		// Persist the resolved plan path so subsequent implementation reuses it.
		// Mirrors startImplement's persistence (orchestrator.go:770-779). The
		// fallback cascade in resolvePlanPath is refactor-aware
		// (phasePlanDirForFeature), so refactor-scoped phase plans are picked
		// over decoy non-refactor paths.
		if reloaded, err := o.deps.Lifecycle.Get(featureID); err == nil && reloaded != nil {
			if planPath := o.resolvePlanPath(reloaded); planPath != "" {
				_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
					if ff.Artifacts == nil {
						ff.Artifacts = make(map[string]string)
					}
					ff.Artifacts["plan"] = planPath
					return nil
				})
			}
		}
		_, _, err := o.startPhase(featureID, feature.PhaseImplement)
		return err
	}

	// Roadmap-level plan approval: advance roadmap phase, then dispatch
	// PhasePlan for the newly-advanced phase.
	if d.Roadmap {
		// Parse roadmap and persist TotalRoadmapPhases before advancing. The
		// automatic approved path (onPlanApproved) does this when the planner
		// returns "approved" status, but when the planner returns
		// "needs_human_review" and the reviewer subsequently approves via the
		// gate, TotalRoadmapPhases is still 0 and downstream roadmap sequencing
		// (CurrentRoadmapPhase < TotalRoadmapPhases checks, phase-plan vs legacy
		// plan routing) would be wrong. Mirrors app.go:3167-3191.
		o.persistRoadmapPhaseCount(featureID, f)
		if err := o.deps.Lifecycle.AdvanceRoadmapPhase(featureID); err != nil {
			return fmt.Errorf("advance roadmap phase: %w", err)
		}
		_, _, err := o.startPhase(featureID, feature.PhasePlan)
		return err
	}

	// Gate dispatch. Special-case PhaseImplement: roadmap pending vs. legacy plan.
	if d.TargetPhase == feature.PhaseImplement {
		if f.TotalRoadmapPhases > 0 && f.CurrentRoadmapPhase == 0 {
			if err := o.deps.Lifecycle.AdvanceRoadmapPhase(featureID); err != nil {
				return fmt.Errorf("advance roadmap phase: %w", err)
			}
			_, _, err := o.startPhase(featureID, feature.PhasePlan)
			return err
		}
		// Non-roadmap legacy plan review approval. The legacy plan dir's
		// execution-order.yaml (if any) is read fresh by
		// StartMultiRepoImplementation; no pre-flight populate is needed.
		if err := o.deps.Lifecycle.CompletePlanning(featureID); err != nil {
			return fmt.Errorf("complete planning: %w", err)
		}
		_, _, err := o.startPhase(featureID, feature.PhaseImplement)
		return err
	}

	// Generic gate proceed: dispatch d.TargetPhase directly.
	_, _, err := o.startPhase(featureID, d.TargetPhase)
	return err
}

// reviewIterate handles "iterate" from a plan-review menu: bump
// MaxPlanIterations by 3 and re-dispatch the plan phase so the planner runs
// another attempt. The per-phase plan attempt meta write is a best-effort
// local-file operation — missing it is not fatal.
//
// When MaxPlanIterations == 0 (the default on first run), the planner uses
// agent.DefaultMaxPlanAttempts as the budget. Phase-plan runs further guard
// the override to only take effect when it exceeds the default
// (phase.go:RunPhasePlanning), so we must promote 0 to the default before
// adding 3 — otherwise the effective budget after iterate drops from the
// default (10) to just 3, and on the phase-plan path may not extend at all.
// Mirrors the TUI's promotion at app.go:3149-3155.
//
// Plan-attempt meta invalidation fires unconditionally — regardless of
// d.Roadmap / d.PhasePlan. The planner short-circuits on any APPROVED
// attempt-NN/meta.yaml it finds (plan_validation.go:254 for the roadmap loop,
// plan_validation.go:1056 for the phase-plan loop, plan_validation.go:247 for
// the legacy flat plan loop). Gating invalidation on Roadmap/PhasePlan meant
// a plain StatusPlanNeedsReview "iterate" left the stale APPROVED meta in
// place and the next plan run would just return immediately. The helper is
// a no-op when no approved attempt exists, so calling it unconditionally is
// safe for every path.
func (o *Orchestrator) reviewIterate(featureID string, f *feature.Feature, d ReviewDecision) error {
	if err := o.clearReviewGate(featureID); err != nil {
		return err
	}
	// Roadmap reject path: reset PlanStatus back to PlanReady, record rejection
	// feedback on the latest plan attempt, then dispatch PhasePlan so the
	// planner runs another attempt with the reviewer feedback in scope.
	if d.Roadmap {
		if err := o.ResetPlanStatusForRoadmap(featureID, 1); err != nil {
			return fmt.Errorf("reset plan status for roadmap reject: %w", err)
		}
		o.RecordRoadmapRejection(featureID, d.Comment)
		_, _, err := o.startPhase(featureID, feature.PhasePlan)
		return err
	}
	if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		if ff.MaxPlanIterations == 0 {
			ff.MaxPlanIterations = agent.DefaultMaxPlanAttempts
		}
		ff.MaxPlanIterations += 3
		return nil
	}); err != nil {
		return fmt.Errorf("bump MaxPlanIterations: %w", err)
	}
	o.writePlanAttemptChangesRequested(f, d.PhasePlan)
	startedPhase, started, err := o.startPhase(featureID, feature.PhasePlan)
	if err != nil {
		return err
	}
	if started {
		o.emitEvent(ports.Event{Type: ports.FeatureAdvanced, FeatureID: featureID, Phase: startedPhase})
	}
	return nil
}

// ProceedFromRewindReview confirms a rewind that has already been performed
// and dispatches the target phase. The TUI invokes Lifecycle.RewindToPhase
// directly (rewindCmd) BEFORE opening the rewind-artifact-review session, so
// by the time the user picks "Proceed with rewind" the active run is already
// the freshly forked one with PendingReviewPhase=&target and IsRewind=true.
// This method clears that gate, reads back description-review.md if the user
// edited it during the review, runs the appropriate planning-complete
// transition for rewind-to-Implement (generic CompletePlanning, or
// StartRoadmapPhaseImplementation for partial roadmap phase rewinds), and
// starts the target phase.
//
// `target` MUST be the effective target phase (post-escalation) — typically
// the value the TUI received from RewindDoneMsg.TargetPhase. Escalation
// (e.g. Medium-upgraded features escalating pre-plan rewinds to KB) was
// resolved by the original RewindToPhase call and persisted onto the new run.
//
// Replaces the previously-incorrect HandleReviewDecision({Decision:"rewind"})
// path, which redundantly called RewindToPhase a second time and produced an
// extra phantom run on every confirm.
func (o *Orchestrator) ProceedFromRewindReview(featureID string, target feature.Phase) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("loading feature %s: %w", featureID, err)
	}
	if f.Status == feature.StatusInterrupted || f.Status == feature.StatusFailed {
		return nil
	}
	pendingPartialImplementReview := target == feature.PhaseImplement && f.IsRewind && f.PendingRewindReviewRoadmapPhase != nil
	if err := o.clearReviewGate(featureID); err != nil {
		return err
	}

	// Read description-review.md and overwrite f.Description when the user
	// proceeds from a rewind-to-first-phase review. Large/Moonshot write
	// the file on PhaseInquire rewind; Medium writes it on PhasePlan rewind
	// (Plan is Medium's first phase). feature/manager.go RewindToPhase is
	// the writer for both cases.
	if target == feature.PhaseInquire ||
		(target == feature.PhasePlan && f != nil && f.EffectivePipeline() == feature.PipelineMedium) {
		if baseDir := o.stateDir(); baseDir != "" {
			descPath := filepath.Join(baseDir, featureID, "description-review.md")
			if data, err := os.ReadFile(descPath); err == nil && len(data) > 0 {
				_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
					ff.Description = string(data)
					return nil
				})
			}
		}
	}

	// Rewind-to-implement: mark planning complete. CompletePlanning must fire
	// on every rewind-to-implement path — not only the non-roadmap case.
	// After RewindToPhase, the feature's Status is
	// NeedsReviewForPhase(PhaseImplement) == StatusPlanNeedsReview
	// (feature/manager.go:1241-1242). startImplement immediately calls
	// StartImplementation (orchestrator.go:562), which transitions to
	// StatusImplementing — a transition that is invalid from
	// StatusPlanNeedsReview (feature/feature.go:504 only allows
	// {StatusPlanning, StatusImplementReady, StatusFailed}). CompletePlanning
	// transitions StatusPlanNeedsReview → StatusImplementReady first, making
	// the subsequent StartImplementation valid.
	//
	// The per-phase execution-order.yaml is read fresh from disk by
	// StartMultiRepoImplementation (per SchemaVersionCurrent = 3); no
	// pre-flight populate step is needed on rewind.
	if target == feature.PhaseImplement {
		if pendingPartialImplementReview {
			if err := o.deps.Lifecycle.StartRoadmapPhaseImplementation(featureID); err != nil {
				return fmt.Errorf("start roadmap phase implementation: %w", err)
			}
		} else {
			if err := o.deps.Lifecycle.CompletePlanning(featureID); err != nil {
				return fmt.Errorf("complete planning: %w", err)
			}
		}
	}

	// Use the started phase returned by startPhase (not the requested
	// target) for FeatureAdvanced. startPhase may chain through
	// PhaseSkipped — e.g. a rewind target of PhaseKnowledgeBase on a
	// feature whose KB is already fresh skips to PhaseInquire (startKB
	// returns PhaseSkipped → PhaseInquire). Emitting FeatureAdvanced(target)
	// in that case would broadcast the wrong phase to subscribers and break
	// the phase-sequencing contract.
	startedPhase, started, err := o.startPhase(featureID, target)
	if err != nil {
		return err
	}
	if started {
		o.emitEvent(ports.Event{Type: ports.FeatureAdvanced, FeatureID: featureID, Phase: startedPhase})
	}
	return nil
}

// clearReviewGate clears PendingReviewPhase and IsRewind. Used from both
// reviewProceed and ProceedFromRewindReview.
func (o *Orchestrator) clearReviewGate(featureID string) error {
	return o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		ff.PendingReviewPhase = nil
		ff.PendingRewindReviewRoadmapPhase = nil
		ff.IsRewind = false
		return nil
	})
}

// persistRoadmapPhaseCount resolves the roadmap artifact, parses it, and
// writes TotalRoadmapPhases into feature state. Best-effort: if the roadmap
// file can't be resolved or parsed, TotalRoadmapPhases is left untouched so
// the existing value (whatever it is) continues to drive sequencing. Mirrors
// the TUI helper behaviour in app.go:3171-3184.
func (o *Orchestrator) persistRoadmapPhaseCount(featureID string, f *feature.Feature) {
	roadmapPath := o.resolveArtifactPath(f, "roadmap")
	if roadmapPath == "" {
		return
	}
	data, readErr := os.ReadFile(roadmapPath)
	if readErr != nil {
		return
	}
	phases, parseErr := agent.ParseRoadmap(string(data))
	if parseErr != nil || len(phases) == 0 {
		return
	}
	_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		ff.TotalRoadmapPhases = len(phases)
		return nil
	})
}

// writePlanAttemptChangesRequested invalidates the latest completed plan
// attempt so the next planner run treats the artifact as rejected and starts
// a new attempt. This mirrors the TUI's roadmap-reject path
// (app.go:3283-3297): find the latest attempt via LatestCompletedPlanAttempt
// and overwrite its meta.yaml with a CHANGES_REQUESTED entry.
//
// The planner's resume logic short-circuits when the latest attempt has
// ReviewStatus == "APPROVED" (in RunRoadmapPlanningLoop and
// RunPhasePlanningLoop). Writing a new meta with empty AgentStatus causes
// LatestCompletedPlanAttempt to skip that attempt entirely, forcing a fresh
// attempt. Without this, "iterate" on an approved plan would return approved
// immediately and never re-plan.
//
// For the non-phase-plan case the helper invalidates both the roadmap/
// directory (where RunRoadmapPlanningLoop writes) and the legacy plan/
// directory left over from the removed RunPlanningLoop (features carried
// forward from older builds may still have APPROVED metadata there). Either
// directory may be empty or absent on any given feature, in which case
// LatestCompletedPlanAttempt returns 0 and that candidate is skipped.
// Covering both keeps "iterate" correct regardless of which loop produced
// the stale APPROVED attempt.
//
// Best-effort: fails silently when state dir is unresolved or no prior
// attempt has completed successfully.
func (o *Orchestrator) writePlanAttemptChangesRequested(f *feature.Feature, phasePlan bool) {
	baseDir := o.stateDir()
	if baseDir == "" {
		return
	}
	var candidates []string
	if phasePlan && f.CurrentRoadmapPhase > 0 {
		// Refactor-aware: RunPhasePlanningLoop writes attempt-NN/meta.yaml under
		// the refactor cycle's phase-plan dir when the feature has a refactor
		// prefix (plan_validation.go:1441). Targeting the non-refactor dir here
		// would leave the APPROVED attempt untouched in the real attempt tree,
		// and the next plan run would short-circuit via plan_validation.go:1056.
		candidates = append(candidates, o.phasePlanDirForFeature(f, f.CurrentRoadmapPhase))
	} else {
		// Refactor-aware roadmap dir: matches the path RunRoadmapPlanningLoop
		// reads from during a refactor cycle (mirrors TUI at app.go:3279-3282).
		roadmapDir := agent.RoadmapDir(baseDir, f)
		if f.RefactorPrefix() != "" {
			roadmapDir = filepath.Join(agent.ActiveRunDir(baseDir, f), f.RefactorPrefix(), "roadmap")
		}
		// Legacy plan dir: features carried forward from a build that ran the
		// (now-removed) RunPlanningLoop may have APPROVED metadata at
		// runs/run-NNN/plan/attempt-NN/meta.yaml. Invalidate it defensively so
		// LatestCompletedPlanAttempt won't return a stale match for those features.
		legacyPlanDir := filepath.Join(agent.ActiveRunDir(baseDir, f), "plan")
		if f.RefactorPrefix() != "" {
			legacyPlanDir = filepath.Join(agent.ActiveRunDir(baseDir, f), f.RefactorPrefix(), "plan")
		}
		candidates = append(candidates, roadmapDir, legacyPlanDir)
	}
	for _, planDir := range candidates {
		latestAttempt := agent.LatestCompletedPlanAttempt(planDir)
		if latestAttempt <= 0 {
			continue
		}
		_ = agent.WritePlanAttemptMeta(planDir, agent.PlanAttemptMeta{
			Attempt:      latestAttempt,
			ReviewStatus: "CHANGES_REQUESTED",
		})
	}
}

// advanceToNextPhase resolves the next phase from the feature's effective
// pipeline and dispatches via startPhase. Handles checkpoint gates and
// terminal-phase short-circuits. NEVER emits FeatureCompleted — that event is
// owned exclusively by tryCompleteAndEmit.
func (o *Orchestrator) advanceToNextPhase(featureID string, completedPhase feature.Phase) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("loading feature %s: %w", featureID, err)
	}

	profile := f.EffectivePipeline()
	next, hasNext := profile.NextPhase(completedPhase)
	if !hasNext {
		// Terminal phase for this profile. Emit nothing; the TUI's
		// manualPublishCmd owns the StatusDone transition for non-publishable
		// features, and Publish already owned FeatureCompleted emission for
		// publishable ones.
		return nil
	}
	if next == feature.PhasePublish {
		// Publish dispatch is owned by completion handlers, not the generic
		// next-phase pathway. Matches TUI semantics where startPhaseCmd(Publish)
		// returns nil.
		return nil
	}

	if f.Checkpoints.HasGateForPhase(next) {
		nextPhase := next
		if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			enterReviewGateFeatureState(ff, nextPhase)
			return nil
		}); err != nil {
			return fmt.Errorf("transition to review gate: %w", err)
		}
		o.emitEventBlocking(ports.Event{
			Type:      ports.ReviewRequired,
			FeatureID: featureID,
			Phase:     next,
		})
		if o.hooks.OnReviewRequired != nil {
			o.hooks.OnReviewRequired(featureID, next)
		}
		return nil
	}

	startedPhase, started, err := o.startPhase(featureID, next)
	if err != nil {
		errMsg := fmt.Sprintf("start phase %s: %v", next, err)
		if markErr := o.markFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg); markErr != nil {
			return fmt.Errorf("%w; additionally: %v", err, markErr)
		}
		return err
	}
	if started {
		o.emitEvent(ports.Event{
			Type:      ports.FeatureAdvanced,
			FeatureID: featureID,
			Phase:     startedPhase,
		})
	}
	return nil
}

// tryCompleteAndEmit is the SOLE emitter of ports.FeatureCompleted in the
// orchestrator package. It calls Lifecycle.TryCompletePublish (which is
// idempotent: second and subsequent calls return (false, nil) because the
// feature is no longer at StatusReviewPassed/StatusCodeReady) and emits
// FeatureCompleted + fires OnFeatureCompleted only on the first call that
// transitions the feature to StatusPublished.
//
// Called from exactly three sites: Publish, onRepoStatusChanged,
// onMultiRepoImplementDone. The →StatusDone transition is TUI-owned
// (manualPublishCmd) and is NOT driven by this helper.
//
// Source-scan regression Test 73a enforces this invariant.
func (o *Orchestrator) tryCompleteAndEmit(featureID string) (bool, error) {
	published, err := o.deps.Lifecycle.TryCompletePublish(featureID)
	if err != nil {
		return false, fmt.Errorf("try complete publish: %w", err)
	}
	if !published {
		return false, nil
	}
	f, getErr := o.deps.Lifecycle.Get(featureID)
	if getErr != nil {
		// The transition already happened on disk; we just can't read the
		// fresh feature. Emit the event without feature payload rather than
		// silently swallowing it.
		o.emitEventBlocking(ports.Event{Type: ports.FeatureCompleted, FeatureID: featureID})
		if o.hooks.OnFeatureCompleted != nil {
			o.hooks.OnFeatureCompleted(featureID, nil)
		}
		if o.hooks.OnFeatureSummaryNeeded != nil {
			o.hooks.OnFeatureSummaryNeeded(featureID, nil)
		}
		return true, nil
	}
	o.emitEventBlocking(ports.Event{
		Type:      ports.FeatureCompleted,
		FeatureID: featureID,
		Feature:   f,
	})
	if o.hooks.OnFeatureCompleted != nil {
		o.hooks.OnFeatureCompleted(featureID, f)
	}
	if o.hooks.OnFeatureSummaryNeeded != nil {
		o.hooks.OnFeatureSummaryNeeded(featureID, f)
	}
	return true, nil
}

// Publish runs the publish pipeline for a feature. Fans out per-repo
// publishRepo calls, aggregates results, emits PublishStarted/PublishCompleted
// events, and delegates FeatureCompleted emission to tryCompleteAndEmit.
func (o *Orchestrator) Publish(featureID string) error {
	return o.PublishWithOptions(featureID, PublishOptions{})
}

type PublishOptions struct {
	Repos []string
	Title string
	Body  string
}

// PublishWithOptions runs the publish pipeline for all repos, or for the
// selected repos when Repos is non-empty. Title and Body override generated PR
// metadata for manual publish flows that already reviewed those fields.
func (o *Orchestrator) PublishWithOptions(featureID string, opts PublishOptions) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("loading feature %s: %w", featureID, err)
	}
	if !f.IsPublishable() {
		return nil
	}
	if len(f.Repos) == 0 {
		return fmt.Errorf("publish: no repos configured for feature %s", featureID)
	}
	requestedRepos, err := publishRepoSelection(f, opts.Repos)
	if err != nil {
		return err
	}

	o.emitEvent(ports.Event{Type: ports.PublishStarted, FeatureID: featureID})
	if o.hooks.OnPublishStarted != nil {
		o.hooks.OnPublishStarted(featureID)
	}

	publishRepo := o.publishRepoFn
	if publishRepo == nil {
		publishRepo = o.publishRepo
	}

	prURLs := make(map[string]string)
	var firstErr error
	var conflictErr *PublishConflictError
	for _, repo := range f.Repos {
		if len(requestedRepos) > 0 && !requestedRepos[repo.Name] {
			continue
		}
		// Skip repos already published (sibling goroutines may have updated)
		// or untouched (no phase ever touched them, no PR to open).
		freshF, freshErr := o.deps.Lifecycle.Get(featureID)
		if freshErr == nil {
			if st, ok := freshF.RepoStates[repo.Name]; ok && st != nil {
				if st.PRURL != "" {
					prURLs[repo.Name] = st.PRURL
					continue
				}
				if !st.Touched {
					continue
				}
			}
		}

		var prURL string
		var repoErr error
		if o.publishRepoFn != nil {
			prURL, repoErr = publishRepo(featureID, repo.Name)
		} else {
			prURL, repoErr = o.publishRepoWithOptions(featureID, repo.Name, opts)
		}
		if repoErr != nil {
			var ce *PublishConflictError
			if errors.As(repoErr, &ce) {
				if conflictErr == nil {
					conflictErr = ce
				}
			} else if firstErr == nil {
				firstErr = repoErr
			}
			continue
		}
		prURLs[repo.Name] = prURL
	}

	// Delegate FeatureCompleted emission to the sole-emitter helper.
	if _, completeErr := o.tryCompleteAndEmit(featureID); completeErr != nil && firstErr == nil {
		firstErr = completeErr
	}

	// Pick the final error: conflict first, then non-conflict.
	var finalErr error
	if conflictErr != nil {
		finalErr = conflictErr
	} else if firstErr != nil {
		finalErr = firstErr
	}

	o.emitEventBlocking(ports.Event{
		Type:      ports.PublishCompleted,
		FeatureID: featureID,
		Error:     finalErr,
	})
	if o.hooks.OnPublishCompleted != nil {
		o.hooks.OnPublishCompleted(featureID, prURLs, finalErr)
	}
	return finalErr
}

func publishRepoSelection(f *feature.Feature, repos []string) (map[string]bool, error) {
	requested := map[string]bool{}
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo != "" {
			requested[repo] = true
		}
	}
	if len(requested) == 0 {
		return nil, nil
	}
	known := map[string]bool{}
	for _, repo := range f.Repos {
		known[repo.Name] = true
	}
	for repo := range requested {
		if !known[repo] {
			return nil, fmt.Errorf("publish: repo %q not found in feature %s", repo, f.ID)
		}
	}
	return requested, nil
}

// ScanRecovery / ExecuteRecovery live in recovery.go
// StartMultiRepoImplementation lives in multirepo.go

// Shutdown cleanly shuts down the orchestrator. It closes doneCh to unblock
// consumers of Events/Done and any in-flight emitters, and shuts down the
// session manager. Safe to call more than once; stopOnce guarantees the body
// runs exactly once. Never closes eventCh — consumers observe doneCh instead.
func (o *Orchestrator) Shutdown() error {
	o.stopOnce.Do(func() {
		o.emitShutdownStarted()
		close(o.doneCh)
		if o.deps.Sessions != nil {
			o.deps.Sessions.Shutdown()
		}
	})
	return nil
}

// StopFeatureSessions stops every PTY session associated with the given
// feature ID. Safe to call with nil Sessions port (no-op). Used by callers
// that need to reset session state before cascading operations (delete,
// restart, KB-failure propagation) without also triggering lifecycle
// deletion.
//
// Keeping this policy in the orchestrator prevents TUI call sites from
// duplicating session-stop rules.
func (o *Orchestrator) StopFeatureSessions(featureID string) {
	if o.deps.Sessions == nil {
		return
	}
	for _, s := range o.deps.Sessions.FeatureSessions(featureID) {
		if s == nil {
			continue
		}
		_ = o.deps.Sessions.StopSession(s.ID())
	}
}

// Delete stops any active sessions for the feature and removes it via the
// lifecycle. Synchronous (caller learns the outcome via the returned error);
// no ports.Event is emitted, matching the TUI's legacy delete semantics.
func (o *Orchestrator) Delete(featureID string) error {
	o.StopFeatureSessions(featureID)
	if err := o.deps.Lifecycle.Delete(featureID); err != nil {
		return fmt.Errorf("deleting feature: %w", err)
	}
	return nil
}
