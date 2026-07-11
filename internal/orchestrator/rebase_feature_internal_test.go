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
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// Fixture repo/branch names shared across this file's rebase harness tests.
const (
	mainBranch       = "main"
	originMainBranch = "origin/main"
	apiRepoName      = "api"
	webRepoName      = "web"
	agenticRepoName  = "agentic"
	masterBranch     = "master"
	repoAName        = "repo-a"
	repoAPath        = "/tmp/repo-a"
)

type blockingGetLifecycle struct {
	*feature.Manager
	blockAt  int32
	gets     atomic.Int32
	started  chan struct{}
	released chan struct{}
}

func (l *blockingGetLifecycle) Get(id string) (*feature.Feature, error) {
	if l.gets.Add(1) == l.blockAt {
		close(l.started)
		<-l.released
	}
	return l.Manager.Get(id)
}

type featureRebaseHarnessFixture struct {
	t         *testing.T
	store     *feature.Store
	manager   *feature.Manager
	orch      *Orchestrator
	rebaser   *mocks.MockRebaseOperator
	publisher *mocks.MockPublisher
	featureID string
	feature   *feature.Feature
}

func newFeatureRebaseHarnessFixture(t *testing.T, status feature.Status, repoNames []string) *featureRebaseHarnessFixture {
	t.Helper()

	store := feature.NewStore(t.TempDir())
	manager := feature.NewManager(store, config.NewDefault())
	rebaser := mocks.NewMockRebaseOperator()
	publisher := mocks.NewMockPublisher()
	rebaser.FetchFn = func(string) error { return nil }
	rebaser.IsBehindRemoteFn = func(string, string) (bool, error) { return false, nil }
	rebaser.IsBehindLocalFn = func(string, string) (bool, error) { return false, nil }
	publisher.HasUncommittedChangesFn = func(string) (bool, error) { return false, nil }

	repos := make([]feature.FeatureRepo, 0, len(repoNames))
	repoStates := make(map[string]*feature.RepoState, len(repoNames))
	for _, name := range repoNames {
		worktree := t.TempDir()
		repos = append(repos, feature.FeatureRepo{
			Name:         name,
			Path:         worktree,
			WorktreePath: worktree,
			Branch:       "feature/feature-rebase",
			BaseBranch:   mainBranch,
		})
		repoStates[name] = &feature.RepoState{
			Touched: true,
			PRURL:   "https://github.example/" + name + "/pull/1",
		}
	}

	f := &feature.Feature{
		ID:            "feat-feature-rebase",
		Name:          "Feature Rebase",
		Slug:          "feature-rebase",
		Status:        status,
		SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun:     1,
		RunCount:      1,
		Repos:         repos,
		RepoStates:    repoStates,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	return &featureRebaseHarnessFixture{
		t:         t,
		store:     store,
		manager:   manager,
		orch:      New(Deps{Lifecycle: manager, Store: store, Rebaser: rebaser, Publisher: publisher}, Hooks{}),
		rebaser:   rebaser,
		publisher: publisher,
		featureID: f.ID,
		feature:   f,
	}
}

func (f *featureRebaseHarnessFixture) wait() {
	f.t.Helper()
	f.orch.WaitForCycles()
}

func (f *featureRebaseHarnessFixture) load() *feature.Feature {
	f.t.Helper()
	loaded, err := f.manager.Get(f.featureID)
	if err != nil {
		f.t.Fatalf("load feature: %v", err)
	}
	return loaded
}

func (f *featureRebaseHarnessFixture) saveFeature() {
	f.t.Helper()
	if err := f.store.Save(f.feature); err != nil {
		f.t.Fatalf("save feature: %v", err)
	}
}

func countRebaseMockCalls(calls []mocks.MockCall, method string) int {
	count := 0
	for _, call := range calls {
		if call.Method == method {
			count++
		}
	}
	return count
}

func rebaseFinalReviewResult(result *agent.OrchestratorResult) chan *agent.OrchestratorResult {
	ch := make(chan *agent.OrchestratorResult, 1)
	ch <- result
	return ch
}

func (f *featureRebaseHarnessFixture) setFinalReviewResult(result *agent.OrchestratorResult, before func()) *int {
	f.t.Helper()
	calls := 0
	f.orch.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		calls++
		if before != nil {
			before()
		}
		return rebaseFinalReviewResult(result), nil
	})
	return &calls
}

func (f *featureRebaseHarnessFixture) setFinalReviewPassed(before func()) *int {
	f.t.Helper()
	return f.setFinalReviewResult(&agent.OrchestratorResult{FinalStatus: finalStatusAllPassed}, before)
}

func forcePushCalls(calls []mocks.MockCall) []mocks.MockCall {
	var out []mocks.MockCall
	for _, call := range calls {
		if call.Method == "ForcePush" {
			out = append(out, call)
		}
	}
	return out
}

func publisherCalls(calls []mocks.MockCall, method string) []mocks.MockCall {
	var out []mocks.MockCall
	for _, call := range calls {
		if call.Method == method {
			out = append(out, call)
		}
	}
	return out
}

func TestFeatureRebaseSessionStartFuncBlocksInterruptUntilStartReturns(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusCodeReady, []string{apiRepoName})
	if err := fixture.manager.StartFeatureRebaseOperation(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebaseOperation: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(
		string,
		string,
		feature.Phase,
		[]string,
		string,
		[]string,
		...*ports.SessionOpts,
	) (ports.SessionHandle, error) {
		close(entered)
		<-release
		return nil, nil
	}
	fixture.orch.deps.Sessions = sm
	startFn := fixture.orch.featureRebaseSessionStartFunc(fixture.load())
	if startFn == nil {
		t.Fatal("featureRebaseSessionStartFunc = nil, want guarded starter")
	}

	startDone := make(chan error, 1)
	go func() {
		_, err := startFn("rebase-session", fixture.featureID, feature.PhaseImplement, nil, t.TempDir(), nil)
		startDone <- err
	}()
	<-entered

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- fixture.orch.InterruptFeature(fixture.featureID)
	}()

	deadline := time.After(time.Second)
	for !fixture.orch.featureRebaseStopRequested(fixture.featureID) {
		select {
		case <-deadline:
			t.Fatal("InterruptFeature did not request feature rebase stop")
		default:
			runtime.Gosched()
		}
	}
	select {
	case err := <-interruptDone:
		t.Fatalf("InterruptFeature returned before guarded StartSession completed: %v", err)
	default:
	}

	close(release)
	if err := <-startDone; err != nil {
		t.Fatalf("SessionStartFunc returned error: %v", err)
	}
	if err := <-interruptDone; err != nil {
		t.Fatalf("InterruptFeature: %v", err)
	}
	if got := len(sm.StartSessionCalls); got != 1 {
		t.Fatalf("StartSession calls = %d, want 1", got)
	}
}

func TestStartFeatureRebaseHarnessNoOpClearsOperationWithoutSmartAgentOrFinalReview(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusCodeReady, []string{apiRepoName, webRepoName})
	fixture.orch.runRebaseLoopFn = func(agent.RebaseLoopConfig, ports.SessionManager) (*agent.RebaseLoopResult, error) {
		t.Fatal("smart rebase agent should not start for no-op harness")
		return nil, nil
	}
	fixture.orch.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		t.Fatal("Final Review should not start for no-op harness")
		return nil, nil
	})

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	fixture.wait()

	loaded := fixture.load()
	if loaded.ActiveCycle != nil {
		t.Fatalf("ActiveCycle = %+v, want nil", loaded.ActiveCycle)
	}
	if loaded.RebaseOperation != nil {
		t.Fatalf("RebaseOperation = %+v, want nil", loaded.RebaseOperation)
	}
	if got := countRebaseMockCalls(fixture.rebaser.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
}

func TestStartFeatureRebaseHarnessFailurePersistsRepoErrorAndSkipsSmartAgent(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusCodeReady, []string{apiRepoName, webRepoName})
	apiPath := fixture.feature.Repos[0].WorktreePath
	fixture.rebaser.FetchFn = func(worktreePath string) error {
		if worktreePath == apiPath {
			return errors.New("fetch denied")
		}
		return nil
	}
	fixture.orch.runRebaseLoopFn = func(agent.RebaseLoopConfig, ports.SessionManager) (*agent.RebaseLoopResult, error) {
		t.Fatal("smart rebase agent should not start after harness failure")
		return nil, nil
	}
	fixture.orch.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		t.Fatal("Final Review should not start after harness failure")
		return nil, nil
	})

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	fixture.wait()

	loaded := fixture.load()
	if got := loaded.RepoStates[apiRepoName].LastError; !strings.Contains(got, "fetch denied") {
		t.Fatalf("RepoStates[api].LastError = %q, want fetch denied", got)
	}
	if loaded.ActiveCycle != nil {
		t.Fatalf("ActiveCycle = %+v, want nil", loaded.ActiveCycle)
	}
	if loaded.RebaseOperation != nil {
		t.Fatalf("RebaseOperation = %+v, want nil", loaded.RebaseOperation)
	}
	if got := countRebaseMockCalls(fixture.rebaser.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
}

func TestStartFeatureRebaseHarnessMixedConflictAndFailurePreservesOperation(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusCodeReady, []string{apiRepoName, webRepoName})
	apiPath := fixture.feature.Repos[0].WorktreePath
	webPath := fixture.feature.Repos[1].WorktreePath
	fixture.rebaser.FetchFn = func(worktreePath string) error {
		if worktreePath == webPath {
			return errors.New("fetch denied")
		}
		return nil
	}
	fixture.rebaser.IsBehindRemoteFn = func(string, string) (bool, error) { return true, nil }
	fixture.rebaser.RebaseOntoFn = func(worktreePath, target string) ports.RebaseResult {
		if worktreePath != apiPath {
			t.Fatalf("RebaseOnto worktree = %q, want only api path %q", worktreePath, apiPath)
		}
		if target != originMainBranch {
			t.Fatalf("RebaseOnto target = %q, want origin/main", target)
		}
		return ports.RebaseResult{
			Outcome:       ports.RebaseConflict,
			ConflictFiles: []string{"conflicted.go"},
		}
	}
	fixture.orch.runRebaseLoopFn = func(agent.RebaseLoopConfig, ports.SessionManager) (*agent.RebaseLoopResult, error) {
		t.Fatal("smart rebase agent should not start for mixed harness conflict and failure")
		return nil, nil
	}
	fixture.orch.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		t.Fatal("Final Review should not start for mixed harness conflict and failure")
		return nil, nil
	})

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	fixture.wait()

	loaded := fixture.load()
	if loaded.ActiveCycle == nil {
		t.Fatal("ActiveCycle = nil, want preserved rebase cycle")
	}
	if loaded.RebaseOperation == nil {
		t.Fatal("RebaseOperation = nil, want preserved mixed harness outcomes")
	}
	if loaded.RebaseOperation.Stage != feature.RebaseStageHarness {
		t.Fatalf("RebaseOperation.Stage = %q, want %q", loaded.RebaseOperation.Stage, feature.RebaseStageHarness)
	}
	apiProgress := loaded.RebaseOperation.Repos[apiRepoName]
	if apiProgress == nil {
		t.Fatal("RebaseOperation.Repos[api] missing")
	}
	if apiProgress.Status != feature.RebaseRepoStatusConflict {
		t.Fatalf("api status = %q, want %q", apiProgress.Status, feature.RebaseRepoStatusConflict)
	}
	if got := strings.Join(apiProgress.ConflictFiles, ","); got != "conflicted.go" {
		t.Fatalf("api conflict files = %q, want conflicted.go", got)
	}
	webProgress := loaded.RebaseOperation.Repos[webRepoName]
	if webProgress == nil {
		t.Fatal("RebaseOperation.Repos[web] missing")
	}
	if webProgress.Status != feature.RebaseRepoStatusFailed {
		t.Fatalf("web status = %q, want %q", webProgress.Status, feature.RebaseRepoStatusFailed)
	}
	if !strings.Contains(webProgress.LastError, "fetch denied") {
		t.Fatalf("web progress LastError = %q, want fetch denied", webProgress.LastError)
	}
	if got := loaded.RepoStates[webRepoName].LastError; !strings.Contains(got, "fetch denied") {
		t.Fatalf("RepoStates[web].LastError = %q, want fetch denied", got)
	}
	if got := countRebaseMockCalls(fixture.rebaser.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
}

func TestStartFeatureRebaseCleanChangeRunsFinalReviewBeforeAutoPush(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName, webRepoName})
	apiPath := fixture.feature.Repos[0].WorktreePath
	fixture.rebaser.IsBehindRemoteFn = func(worktreePath, _ string) (bool, error) {
		return worktreePath == apiPath, nil
	}
	fixture.rebaser.RebaseOntoFn = func(worktreePath, target string) ports.RebaseResult {
		if worktreePath != apiPath {
			t.Fatalf("RebaseOnto worktree = %q, want only api path %q", worktreePath, apiPath)
		}
		if target != originMainBranch {
			t.Fatalf("RebaseOnto target = %q, want origin/main", target)
		}
		return ports.RebaseResult{Outcome: ports.RebaseSuccess}
	}
	fixture.orch.runRebaseLoopFn = func(agent.RebaseLoopConfig, ports.SessionManager) (*agent.RebaseLoopResult, error) {
		t.Fatal("smart rebase agent should not start for clean harness change")
		return nil, nil
	}
	finalReviewCalls := fixture.setFinalReviewPassed(func() {
		if got := countRebaseMockCalls(fixture.rebaser.Calls, "ForcePush"); got != 0 {
			t.Fatalf("ForcePush calls before Final Review = %d, want 0", got)
		}
	})

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	fixture.wait()

	if *finalReviewCalls != 1 {
		t.Fatalf("Final Review calls = %d, want 1", *finalReviewCalls)
	}
	pushes := forcePushCalls(fixture.rebaser.Calls)
	if len(pushes) != 1 {
		t.Fatalf("ForcePush calls = %d, want 1", len(pushes))
	}
	if pushes[0].Args[0] != apiPath {
		t.Fatalf("ForcePush worktree = %v, want %q", pushes[0].Args[0], apiPath)
	}
	if pushes[0].Args[1] != "feature/feature-rebase" {
		t.Fatalf("ForcePush branch = %v, want feature/feature-rebase", pushes[0].Args[1])
	}
	loaded := fixture.load()
	if loaded.Status != feature.StatusPublished {
		t.Fatalf("Status = %q, want %q", loaded.Status, feature.StatusPublished)
	}
	if loaded.ActiveCycle != nil {
		t.Fatalf("ActiveCycle = %+v, want nil", loaded.ActiveCycle)
	}
	if loaded.RebaseOperation != nil {
		t.Fatalf("RebaseOperation = %+v, want nil", loaded.RebaseOperation)
	}
	if got := loaded.RepoStates[apiRepoName].LastError; got != "" {
		t.Fatalf("RepoStates[api].LastError = %q, want empty", got)
	}
}

func TestStartFeatureRebaseManualPublishLeavesCodeReadyWithLocalChanges(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusCodeReady, []string{apiRepoName})
	fixture.feature.Checkpoints.ManualPublish = true
	fixture.saveFeature()
	fixture.rebaser.IsBehindRemoteFn = func(string, string) (bool, error) { return true, nil }
	fixture.rebaser.RebaseOntoFn = func(_, target string) ports.RebaseResult {
		if target != originMainBranch {
			t.Fatalf("RebaseOnto target = %q, want origin/main", target)
		}
		return ports.RebaseResult{Outcome: ports.RebaseSuccess}
	}
	fixture.orch.runRebaseLoopFn = func(agent.RebaseLoopConfig, ports.SessionManager) (*agent.RebaseLoopResult, error) {
		t.Fatal("smart rebase agent should not start for changed harness outcome")
		return nil, nil
	}
	finalReviewCalls := fixture.setFinalReviewPassed(nil)

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	fixture.wait()

	if *finalReviewCalls != 1 {
		t.Fatalf("Final Review calls = %d, want 1", *finalReviewCalls)
	}
	loaded := fixture.load()
	if loaded.Status != feature.StatusCodeReady {
		t.Fatalf("Status = %q, want %q", loaded.Status, feature.StatusCodeReady)
	}
	if loaded.ActiveCycle != nil {
		t.Fatalf("ActiveCycle = %+v, want nil", loaded.ActiveCycle)
	}
	if loaded.RebaseOperation != nil {
		t.Fatalf("RebaseOperation = %+v, want nil", loaded.RebaseOperation)
	}
	if got := loaded.RepoStates[apiRepoName].LastError; got != "" {
		t.Fatalf("RepoStates[api].LastError = %q, want empty", got)
	}
	if got := countRebaseMockCalls(fixture.rebaser.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
}

func TestStartFeatureRebaseUnsupportedFinalReviewPlanRevisionClearsTransientRebaseState(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName})
	fixture.feature.Pipeline = feature.PipelineMedium
	fixture.saveFeature()
	fixture.rebaser.IsBehindRemoteFn = func(string, string) (bool, error) { return true, nil }
	fixture.rebaser.RebaseOntoFn = func(_, target string) ports.RebaseResult {
		if target != originMainBranch {
			t.Fatalf("RebaseOnto target = %q, want origin/main", target)
		}
		return ports.RebaseResult{Outcome: ports.RebaseSuccess}
	}
	fixture.setFinalReviewResult(&agent.OrchestratorResult{
		FinalStatus:          finalStatusPlanRevisionRequired,
		PlanRevisionFeedback: "MISSING_EVIDENCE_REQUIREMENT behavioral: Cover the clean rebase plan revision path.",
	}, nil)

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	fixture.wait()

	loaded := fixture.load()
	if loaded.Status != feature.StatusFailed {
		t.Fatalf("Status = %q, want %q", loaded.Status, feature.StatusFailed)
	}
	if !strings.Contains(loaded.LastError, "final review requested unsupported phase-plan revision") {
		t.Fatalf("LastError = %q, want unsupported final review plan revision", loaded.LastError)
	}
	if loaded.FailureType != feature.FailureInfrastructure {
		t.Fatalf("FailureType = %q, want %q", loaded.FailureType, feature.FailureInfrastructure)
	}
	if loaded.RebaseOperation != nil {
		t.Fatalf("RebaseOperation = %+v, want nil after final review failure", loaded.RebaseOperation)
	}
	if loaded.ActiveCycle != nil {
		t.Fatalf("ActiveCycle = %+v, want nil after final review failure", loaded.ActiveCycle)
	}
	if loaded.ActiveCycleType() == feature.CycleRebase {
		t.Fatalf("ActiveCycleType = %q, want non-rebase after final review failure", loaded.ActiveCycleType())
	}
	if got := loaded.RepoStates[apiRepoName].LastError; !strings.Contains(got, "rebase final review failed") {
		t.Fatalf("RepoStates[api].LastError = %q, want rebase final review failure", got)
	}
	if got := countRebaseMockCalls(fixture.rebaser.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
}

func TestStartFeatureRebaseInterruptedInFlightHarnessDoesNotRunFinalReviewOrPush(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName, webRepoName})
	apiPath := fixture.feature.Repos[0].WorktreePath
	webPath := fixture.feature.Repos[1].WorktreePath
	blocked := make(chan struct{})
	release := make(chan struct{})
	webRebaseStarted := make(chan struct{}, 1)
	finalReviewCalled := make(chan struct{}, 1)
	fixture.rebaser.IsBehindRemoteFn = func(string, string) (bool, error) { return true, nil }
	fixture.rebaser.RebaseOntoFn = func(worktreePath, target string) ports.RebaseResult {
		if target != originMainBranch {
			t.Fatalf("RebaseOnto target = %q, want origin/main", target)
		}
		switch worktreePath {
		case apiPath:
			close(blocked)
			<-release
		case webPath:
			webRebaseStarted <- struct{}{}
		default:
			t.Fatalf("RebaseOnto worktree = %q, want api or web", worktreePath)
		}
		return ports.RebaseResult{Outcome: ports.RebaseSuccess}
	}
	fixture.orch.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		finalReviewCalled <- struct{}{}
		return rebaseFinalReviewResult(&agent.OrchestratorResult{FinalStatus: finalStatusAllPassed}), nil
	})

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for harness rebase to block")
	}
	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- fixture.orch.InterruptFeature(fixture.featureID)
	}()
	deadline := time.After(2 * time.Second)
	for !fixture.orch.featureRebaseStopRequested(fixture.featureID) {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for interrupt request to be recorded")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	close(release)
	select {
	case err := <-interruptDone:
		if err != nil {
			t.Fatalf("InterruptFeature: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for interrupt to finish after harness release")
	}
	fixture.wait()

	select {
	case <-finalReviewCalled:
		t.Fatal("Final Review ran after interrupted rebase harness completed")
	default:
	}
	if got := countRebaseMockCalls(fixture.rebaser.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
	select {
	case <-webRebaseStarted:
		t.Fatal("second repo rebase started after feature-level rebase was interrupted")
	default:
	}
	loaded := fixture.load()
	if loaded.Status != feature.StatusInterrupted {
		t.Fatalf("Status after harness completion = %q, want %q", loaded.Status, feature.StatusInterrupted)
	}
	if progress := loaded.RebaseOperation.Repos[apiRepoName]; progress == nil {
		t.Fatal("RebaseOperation.Repos[api] missing")
	} else if progress.Status == feature.RebaseRepoStatusChanged {
		t.Fatalf("api progress = %+v, want no final changed progress after interrupt", progress)
	}
	if progress := loaded.RebaseOperation.Repos[webRepoName]; progress != nil {
		t.Fatalf("web progress = %+v, want no progress after interrupt", progress)
	}
	if loaded.ActiveCycle != nil {
		switch loaded.ActiveCycle.Status {
		case feature.RepoCycleRunning, feature.RepoCycleReviewing:
			t.Fatalf("ActiveCycle revived after interrupt: %+v", loaded.ActiveCycle)
		}
	}
}

func TestRunRebaseFinalReviewAndPublishPolicyInterruptedAfterFinalReviewDoesNotPushOrAdvance(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName})
	api := fixture.feature.Repos[0]
	if err := fixture.manager.StartFeatureRebaseOperation(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebaseOperation: %v", err)
	}
	lifecycle := &blockingGetLifecycle{
		Manager:  fixture.manager,
		blockAt:  3,
		started:  make(chan struct{}),
		released: make(chan struct{}),
	}
	fixture.orch = New(Deps{Lifecycle: lifecycle, Store: fixture.store, Rebaser: fixture.rebaser}, Hooks{})
	fixture.setFinalReviewPassed(nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		fixture.orch.runRebaseFinalReviewAndPublishPolicy(fixture.featureID, []HarnessRebaseRepoOutcome{{
			RepoName:     api.Name,
			WorktreePath: api.WorktreePath,
			Branch:       api.Branch,
			Status:       feature.RebaseRepoStatusChanged,
			RebaseTarget: originMainBranch,
			Changed:      true,
			Publishable:  true,
		}})
	}()

	select {
	case <-lifecycle.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for post-final-review continuation check")
	}
	if err := fixture.store.Modify(fixture.featureID, func(f *feature.Feature) error {
		f.Status = feature.StatusInterrupted
		if f.ActiveCycle == nil {
			return errors.New("ActiveCycle = nil, want active rebase cycle before interrupt")
		}
		f.ActiveCycle.Status = feature.RepoCycleInterrupted
		return nil
	}); err != nil {
		t.Fatalf("mark interrupted: %v", err)
	}
	close(lifecycle.released)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for rebase final review policy to return")
	}

	if got := countRebaseMockCalls(fixture.rebaser.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
	loaded := fixture.load()
	if loaded.Status != feature.StatusInterrupted {
		t.Fatalf("Status = %q, want %q", loaded.Status, feature.StatusInterrupted)
	}
	if loaded.ActiveCycle != nil {
		switch loaded.ActiveCycle.Status {
		case feature.RepoCycleRunning, feature.RepoCycleReviewing:
			t.Fatalf("ActiveCycle revived after interrupt: %+v", loaded.ActiveCycle)
		}
	}
}

func TestStartFeatureRebaseWaitsForAllHarnessReposThenRunsOneSmartRebase(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName, webRepoName, "worker"})
	fixture.orch.deps.PhaseRunner = &agent.PhaseRunner{StateDir: fixture.store.BaseDir}
	apiPath := fixture.feature.Repos[0].WorktreePath
	workerPath := fixture.feature.Repos[2].WorktreePath
	var smartCalls int
	var events []string

	fixture.rebaser.IsBehindRemoteFn = func(worktreePath, _ string) (bool, error) {
		return worktreePath != fixture.feature.Repos[1].WorktreePath, nil
	}
	fixture.rebaser.RebaseOntoFn = func(worktreePath, target string) ports.RebaseResult {
		if target != originMainBranch {
			t.Fatalf("RebaseOnto target = %q, want origin/main", target)
		}
		switch worktreePath {
		case apiPath:
			return ports.RebaseResult{
				Outcome:       ports.RebaseConflict,
				ConflictFiles: []string{"internal/api/conflicted.go"},
			}
		case workerPath:
			return ports.RebaseResult{Outcome: ports.RebaseSuccess}
		default:
			t.Fatalf("RebaseOnto worktree = %q, want api or worker", worktreePath)
			return ports.RebaseResult{}
		}
	}
	fixture.orch.runRebaseLoopFn = func(cfg agent.RebaseLoopConfig, _ ports.SessionManager) (*agent.RebaseLoopResult, error) {
		smartCalls++
		events = append(events, "smart")
		if got := countRebaseMockCalls(fixture.rebaser.Calls, "ForcePush"); got != 0 {
			t.Fatalf("ForcePush calls before smart rebase = %d, want 0", got)
		}
		if got, want := cfg.BehindRepos, []agent.RebaseRepoTarget{{
			RepoName:      apiRepoName,
			RebaseTarget:  mainBranch,
			ConflictFiles: []string{"internal/api/conflicted.go"},
			PRURL:         "https://github.example/api/pull/1",
		}}; !reflect.DeepEqual(got, want) {
			t.Fatalf("BehindRepos = %#v, want %#v", got, want)
		}
		if got, want := cfg.WorkspaceRepos, []string{apiRepoName, webRepoName, "worker"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("WorkspaceRepos = %v, want %v", got, want)
		}
		loaded := fixture.load()
		if loaded.RebaseOperation == nil || loaded.RebaseOperation.Stage != feature.RebaseStageSmartRebase {
			t.Fatalf("RebaseOperation = %+v, want SmartRebase stage before smart loop", loaded.RebaseOperation)
		}
		return &agent.RebaseLoopResult{
			FinalStatus: reviewStatusPassed,
			Repos:       []string{apiRepoName},
		}, nil
	}
	finalReviewCalls := fixture.setFinalReviewPassed(func() {
		events = append(events, "final-review")
		if got := countRebaseMockCalls(fixture.rebaser.Calls, "ForcePush"); got != 0 {
			t.Fatalf("ForcePush calls before Final Review = %d, want 0", got)
		}
	})

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	fixture.wait()

	if smartCalls != 1 {
		t.Fatalf("smart rebase calls = %d, want 1", smartCalls)
	}
	if *finalReviewCalls != 1 {
		t.Fatalf("Final Review calls = %d, want 1", *finalReviewCalls)
	}
	if got, want := events, []string{"smart", "final-review"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	pushes := forcePushCalls(fixture.rebaser.Calls)
	if len(pushes) != 2 {
		t.Fatalf("ForcePush calls = %d, want worker+api after Final Review", len(pushes))
	}
	if pushes[0].Args[0] != workerPath || pushes[1].Args[0] != apiPath {
		t.Fatalf("ForcePush order/worktrees = %+v, want worker then api", pushes)
	}
	loaded := fixture.load()
	if loaded.RebaseOperation != nil {
		t.Fatalf("RebaseOperation = %+v, want nil after successful Final Review/publish policy", loaded.RebaseOperation)
	}
}

func TestStartFeatureRebaseSmartRebasePublishesEditedContextRepo(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName, webRepoName})
	fixture.orch.deps.PhaseRunner = &agent.PhaseRunner{StateDir: fixture.store.BaseDir}
	apiPath := fixture.feature.Repos[0].WorktreePath
	webPath := fixture.feature.Repos[1].WorktreePath
	fixture.feature.RepoStates[webRepoName].Touched = false
	fixture.saveFeature()

	fingerprints := map[string]string{
		apiPath: "api-before",
		webPath: "web-before",
	}
	fixture.orch.worktreeFingerprintFn = func(worktreePath string) (string, error) {
		return fingerprints[worktreePath], nil
	}
	fixture.rebaser.IsBehindRemoteFn = func(worktreePath, _ string) (bool, error) {
		return worktreePath == apiPath, nil
	}
	fixture.rebaser.RebaseOntoFn = func(worktreePath, _ string) ports.RebaseResult {
		if worktreePath != apiPath {
			t.Fatalf("RebaseOnto worktree = %q, want only api conflict target", worktreePath)
		}
		return ports.RebaseResult{
			Outcome:       ports.RebaseConflict,
			ConflictFiles: []string{"api.go"},
		}
	}
	fixture.publisher.HasUncommittedChangesFn = func(worktreePath string) (bool, error) {
		return worktreePath == webPath, nil
	}
	fixture.orch.runRebaseLoopFn = func(cfg agent.RebaseLoopConfig, _ ports.SessionManager) (*agent.RebaseLoopResult, error) {
		if got, want := cfg.WorkspaceRepos, []string{apiRepoName, webRepoName}; !reflect.DeepEqual(got, want) {
			t.Fatalf("WorkspaceRepos = %v, want %v", got, want)
		}
		fingerprints[webPath] = "web-after-smart-agent-edit"
		return &agent.RebaseLoopResult{
			FinalStatus: reviewStatusPassed,
			Repos:       []string{apiRepoName},
		}, nil
	}
	fixture.setFinalReviewPassed(nil)

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	fixture.wait()

	pushes := forcePushCalls(fixture.rebaser.Calls)
	if len(pushes) != 2 {
		t.Fatalf("ForcePush calls = %d, want api conflict target and edited web context repo", len(pushes))
	}
	if pushes[0].Args[0] != apiPath || pushes[1].Args[0] != webPath {
		t.Fatalf("ForcePush order/worktrees = %+v, want api then web", pushes)
	}
	if got := len(publisherCalls(fixture.publisher.Calls, "CommitAll")); got != 1 {
		t.Fatalf("CommitAll calls = %d, want dirty web context repo committed before push", got)
	}
	if calls := publisherCalls(fixture.publisher.Calls, "CommitAll"); len(calls) != 1 || calls[0].Args[0] != webPath {
		t.Fatalf("CommitAll calls = %+v, want web worktree only", calls)
	}
	loaded := fixture.load()
	if !loaded.RepoStates[webRepoName].Touched {
		t.Fatalf("web RepoState.Touched = false, want stamped after smart rebase context edit")
	}
	if loaded.RebaseOperation != nil {
		t.Fatalf("RebaseOperation = %+v, want nil after successful publish policy", loaded.RebaseOperation)
	}
}

func TestStartFeatureRebaseHarnessUnpublishableRepoRebasesAgainstLocalTarget(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusCodeReady, []string{"local"})
	publishable := false
	fixture.feature.Repos[0].Publishable = &publishable
	fixture.feature.RepoStates["local"].PRURL = ""
	fixture.saveFeature()

	var rebaseTarget string
	fixture.rebaser.IsBehindLocalFn = func(string, string) (bool, error) { return true, nil }
	fixture.rebaser.RebaseOntoFn = func(_, target string) ports.RebaseResult {
		rebaseTarget = target
		return ports.RebaseResult{Outcome: ports.RebaseSuccess}
	}
	finalReviewCalls := fixture.setFinalReviewPassed(nil)

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	fixture.wait()

	if *finalReviewCalls != 1 {
		t.Fatalf("Final Review calls = %d, want 1", *finalReviewCalls)
	}
	if rebaseTarget != mainBranch {
		t.Fatalf("RebaseOnto target = %q, want main", rebaseTarget)
	}
	if got := countRebaseMockCalls(fixture.rebaser.Calls, "Fetch"); got != 0 {
		t.Fatalf("Fetch calls = %d, want 0", got)
	}
	if got := countRebaseMockCalls(fixture.rebaser.Calls, "IsBehindRemote"); got != 0 {
		t.Fatalf("IsBehindRemote calls = %d, want 0", got)
	}
	if got := countRebaseMockCalls(fixture.rebaser.Calls, "IsBehindLocal"); got != 1 {
		t.Fatalf("IsBehindLocal calls = %d, want 1", got)
	}
	if got := countRebaseMockCalls(fixture.rebaser.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
}

func TestHandleFeatureRebaseDone_PrePRCodeReadyResumesPublish(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	mgr := feature.NewManager(store, cfg)

	const featureID = "feat-pre-pr-rebase"
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Pre PR Rebase",
		Slug:          "pre-pr-rebase",
		Status:        feature.StatusCodeReady,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: agenticRepoName, Path: agenticRepoPath, Branch: "feature/pre-pr-rebase"},
		},
		RepoStates: map[string]*feature.RepoState{
			agenticRepoName: {Touched: true},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			agenticRepoName: {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	var publishCalls int
	o.SetPublishFn(func(id string) error {
		publishCalls++
		if id != featureID {
			t.Fatalf("publish id = %q, want %q", id, featureID)
		}
		return nil
	})

	o.handleFeatureRebaseDone(featureID,
		[]agent.RebaseRepoTarget{{RepoName: agenticRepoName, RebaseTarget: masterBranch}},
		&agent.RebaseLoopResult{FinalStatus: reviewStatusPassed, Repos: []string{agenticRepoName}},
		nil,
	)

	if publishCalls != 1 {
		t.Fatalf("publish calls = %d, want 1", publishCalls)
	}
	got, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if got.RepoCycles != nil {
		t.Fatalf("RepoCycles = %+v, want cleared before publish resume", got.RepoCycles)
	}
}

func TestHandleFeatureRebaseDone_ManualPublishDoesNotResumePublish(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	mgr := feature.NewManager(store, cfg)

	const featureID = "feat-manual-publish-rebase"
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Manual Publish Rebase",
		Slug:          "manual-publish-rebase",
		Status:        feature.StatusCodeReady,
		SchemaVersion: feature.SchemaVersionCurrent,
		Checkpoints:   feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: agenticRepoName, Path: agenticRepoPath, Branch: "feature/manual-publish-rebase"},
		},
		RepoStates: map[string]*feature.RepoState{
			agenticRepoName: {Touched: true},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			agenticRepoName: {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	o.SetPublishFn(func(id string) error {
		t.Fatalf("publish should not resume for manual publish feature %q", id)
		return nil
	})

	o.handleFeatureRebaseDone(featureID,
		[]agent.RebaseRepoTarget{{RepoName: agenticRepoName, RebaseTarget: masterBranch}},
		&agent.RebaseLoopResult{FinalStatus: reviewStatusPassed, Repos: []string{agenticRepoName}},
		nil,
	)
}

func TestResolveBehindSubset_SkipsUpToDateHintWithoutConflictFiles(t *testing.T) {
	rebaser := mocks.NewMockRebaseOperator()
	rebaser.IsBehindRemoteFn = func(string, string) (bool, error) {
		return false, nil
	}
	o := New(Deps{Rebaser: rebaser}, Hooks{})

	f := &feature.Feature{
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{
				Name:         agenticRepoName,
				Path:         agenticRepoPath,
				WorktreePath: agenticRepoPath,
				Branch:       "feature/up-to-date",
				BaseBranch:   mainBranch,
			},
		},
	}

	got := o.resolveBehindSubset(f, agenticRepoName, "", nil)

	if len(got) != 0 {
		t.Fatalf("resolveBehindSubset returned %v, want no targets for an up-to-date non-conflict hint", got)
	}
}

func TestHandleFeatureRebaseDone_NeedUserInputPausesCycle(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	mgr := feature.NewManager(store, cfg)
	gatePath := filepath.Join(t.TempDir(), "need-user-input.yaml")

	const featureID = "feat-rebase-nui"
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Rebase Need User Input",
		Slug:          "rebase-need-user-input",
		Status:        feature.StatusPublished,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: agenticRepoName, Path: agenticRepoPath, Branch: "feature/rebase-need-user-input"},
		},
		RepoStates: map[string]*feature.RepoState{
			agenticRepoName: {Touched: true, PRURL: "https://github.com/example/agentic/pull/1"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			agenticRepoName: {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	o.handleFeatureRebaseDone(featureID,
		[]agent.RebaseRepoTarget{{RepoName: agenticRepoName, RebaseTarget: masterBranch}},
		&agent.RebaseLoopResult{
			FinalStatus:       "need_user_input",
			LastError:         "Build gate needs a human decision.",
			NeedUserInputPath: gatePath,
			Repos:             []string{agenticRepoName},
		},
		nil,
	)

	got, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	rc := got.RepoCycles[agenticRepoName]
	if rc == nil {
		t.Fatal("RepoCycles[agentic] missing")
	}
	if rc.Status != feature.RepoCycleNeedUserInput {
		t.Fatalf("RepoCycles[agentic].Status = %q, want %q", rc.Status, feature.RepoCycleNeedUserInput)
	}
	if rc.PendingNeedUserInputPath != gatePath {
		t.Errorf("PendingNeedUserInputPath = %q, want %q", rc.PendingNeedUserInputPath, gatePath)
	}
	if rc.LastError != "Build gate needs a human decision." {
		t.Errorf("LastError = %q", rc.LastError)
	}
	if st := got.RepoStates[agenticRepoName]; st == nil || st.LastError != "" {
		t.Errorf("RepoStates[agentic] = %+v, want no failure", st)
	}
}

func TestHandleFeatureRebaseDone_NeedUserInputClearFeatureGateErrorFailsCycle(t *testing.T) {
	const featureID = "feat-rebase-nui-clear-fails"
	gatePath := filepath.Join(t.TempDir(), "need-user-input.yaml")

	store := mocks.NewMockFeatureStore()
	modifyCalls := 0
	store.ModifyFn = func(_ string, fn func(*feature.Feature) error) error {
		modifyCalls++
		if modifyCalls == 1 {
			return fn(&feature.Feature{
				RepoCycles: map[string]*feature.RepoCycleState{
					agenticRepoName: {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
				},
				PendingNeedUserInputPath: gatePath,
			})
		}
		return errors.New("store write failed")
	}
	lifecycle := mocks.NewMockFeatureLifecycle()

	o := New(Deps{Lifecycle: lifecycle, Store: store}, Hooks{})
	o.handleFeatureRebaseDone(featureID,
		[]agent.RebaseRepoTarget{{RepoName: agenticRepoName, RebaseTarget: mainBranch}},
		&agent.RebaseLoopResult{
			FinalStatus:       "need_user_input",
			Iterations:        1,
			LastError:         "Build gate needs a human decision.",
			NeedUserInputPath: gatePath,
			Repos:             []string{agenticRepoName},
		},
		nil,
	)

	var failCall *mocks.MockCall
	for i := range lifecycle.Calls {
		if lifecycle.Calls[i].Method == "FailRepoCycle" {
			failCall = &lifecycle.Calls[i]
			break
		}
	}
	if failCall == nil {
		t.Fatal("FailRepoCycle was not called")
	}
	if got := failCall.Args[1]; got != agenticRepoName {
		t.Fatalf("FailRepoCycle repo = %v, want agentic", got)
	}
	msg, _ := failCall.Args[2].(string)
	if !strings.Contains(msg, "rebase: clear stale feature-level need-user-input gate") ||
		!strings.Contains(msg, "store write failed") {
		t.Fatalf("FailRepoCycle message = %q, want stale-gate persistence error context", msg)
	}
}

func TestHandleNeedUserInputDecision_RebaseResumeRestartsUnifiedLoop(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	mgr := feature.NewManager(store, config.NewDefault())
	gatePath := writeAnsweredRebaseGate(t, "Resolve generated-file policy.", "Regenerate or preserve generated files?")

	const featureID = "feat-rebase-nui-resume"
	apiDir := t.TempDir()
	webDir := t.TempDir()
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Rebase Need User Input Resume",
		Slug:          "rebase-need-user-input-resume",
		Status:        feature.StatusPublished,
		SchemaVersion: feature.SchemaVersionCurrent,
		MaxIterations: 4,
		Repos: []feature.FeatureRepo{
			{Name: apiRepoName, Path: apiDir, WorktreePath: apiDir, Branch: "feature/rebase-nui", BaseBranch: mainBranch},
			{Name: webRepoName, Path: webDir, WorktreePath: webDir, Branch: "feature/rebase-nui", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			apiRepoName: {Touched: true, PRURL: "https://github.com/example/api/pull/1"},
			webRepoName: {Touched: true, PRURL: "https://github.com/example/web/pull/2"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			apiRepoName: {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
			webRepoName: {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		},
		ActiveCycle:              &feature.CycleState{Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		PendingNeedUserInputPath: gatePath,
	}
	f.SetRebaseCount(1)
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{
		Lifecycle:   mgr,
		Store:       store,
		PhaseRunner: agent.NewPhaseRunner(nil, store, stateDir),
	}, Hooks{})

	behind := []agent.RebaseRepoTarget{
		{RepoName: apiRepoName, RebaseTarget: mainBranch},
		{RepoName: webRepoName, RebaseTarget: mainBranch},
	}
	o.handleFeatureRebaseDone(featureID, behind, &agent.RebaseLoopResult{
		FinalStatus:       "need_user_input",
		Iterations:        1,
		LastError:         "Resolve generated-file policy.",
		NeedUserInputPath: gatePath,
		Repos:             []string{apiRepoName, webRepoName},
	}, nil)

	paused, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load paused feature: %v", err)
	}
	if paused.Status != feature.StatusPublished {
		t.Fatalf("Status = %q, want published", paused.Status)
	}
	if paused.PendingNeedUserInputPath != "" {
		t.Fatalf("feature-level PendingNeedUserInputPath = %q, want cleared for cycle-scoped gate", paused.PendingNeedUserInputPath)
	}
	for _, name := range []string{apiRepoName, webRepoName} {
		rc := paused.RepoCycles[name]
		if rc == nil {
			t.Fatalf("RepoCycles[%s] missing", name)
		}
		if rc.Status != feature.RepoCycleNeedUserInput {
			t.Fatalf("RepoCycles[%s].Status = %q, want need_user_input", name, rc.Status)
		}
		if rc.PendingNeedUserInputPath != gatePath {
			t.Fatalf("RepoCycles[%s].PendingNeedUserInputPath = %q, want %q", name, rc.PendingNeedUserInputPath, gatePath)
		}
	}

	captured := make(chan agent.RebaseLoopConfig, 1)
	o.runRebaseLoopFn = func(cfg agent.RebaseLoopConfig, _ ports.SessionManager) (*agent.RebaseLoopResult, error) {
		cfgCopy := cfg
		cfgCopy.BehindRepos = append([]agent.RebaseRepoTarget(nil), cfg.BehindRepos...)
		if cfg.Feature != nil {
			featureCopy := *cfg.Feature
			featureCopy.RepoCycles = make(map[string]*feature.RepoCycleState, len(cfg.Feature.RepoCycles))
			for name, rc := range cfg.Feature.RepoCycles {
				if rc == nil {
					continue
				}
				rcCopy := *rc
				featureCopy.RepoCycles[name] = &rcCopy
			}
			cfgCopy.Feature = &featureCopy
		}
		captured <- cfgCopy
		return &agent.RebaseLoopResult{
			FinalStatus: reviewStatusPassed,
			Repos:       []string{apiRepoName, webRepoName},
		}, nil
	}

	if err := o.HandleNeedUserInputDecision(featureID, NeedUserInputDecision{
		Decision:  "resume",
		RepoName:  apiRepoName,
		CycleType: feature.CycleRebase,
	}); err != nil {
		t.Fatalf("HandleNeedUserInputDecision resume: %v", err)
	}
	o.WaitForCycles()

	var cfg agent.RebaseLoopConfig
	select {
	case cfg = <-captured:
	default:
		t.Fatal("rebase loop was not relaunched")
	}
	if !cfg.ResumeExistingCycle {
		t.Fatal("ResumeExistingCycle = false, want true")
	}
	if cfg.Feature == nil || cfg.Feature.RebaseCount() != 1 {
		t.Fatalf("captured feature RebaseCount = %v, want 1", cfg.Feature)
	}
	if got := rebaseTargetNames(cfg.BehindRepos); !reflect.DeepEqual(got, []string{apiRepoName, webRepoName}) {
		t.Fatalf("BehindRepos = %v, want [api web]", got)
	}
	for _, name := range []string{apiRepoName, webRepoName} {
		rc := cfg.Feature.RepoCycles[name]
		if rc == nil {
			t.Fatalf("captured RepoCycles[%s] missing", name)
		}
		if rc.Status != feature.RepoCycleRunning {
			t.Fatalf("captured RepoCycles[%s].Status = %q, want running", name, rc.Status)
		}
		if rc.PendingNeedUserInputPath != "" {
			t.Fatalf("captured RepoCycles[%s].PendingNeedUserInputPath = %q, want cleared", name, rc.PendingNeedUserInputPath)
		}
	}

	done, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load resumed feature: %v", err)
	}
	if done.PendingNeedUserInputPath != "" {
		t.Fatalf("feature-level PendingNeedUserInputPath after resume = %q, want cleared", done.PendingNeedUserInputPath)
	}
	if len(done.RepoCycles) != 0 {
		t.Fatalf("RepoCycles after successful resumed rebase = %+v, want cleared", done.RepoCycles)
	}
}

func TestHandleNeedUserInputDecision_RebaseAbortFailsSharedGateCycles(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	mgr := feature.NewManager(store, config.NewDefault())
	gatePath := writeAnsweredRebaseGate(t, "User rejected generated-file policy.", "Regenerate or preserve generated files?")

	const featureID = "feat-rebase-nui-abort"
	f := &feature.Feature{
		ID:                       featureID,
		Name:                     "Rebase Need User Input Abort",
		Slug:                     "rebase-need-user-input-abort",
		Status:                   feature.StatusPublished,
		SchemaVersion:            feature.SchemaVersionCurrent,
		PendingNeedUserInputPath: gatePath,
		Repos: []feature.FeatureRepo{
			{Name: apiRepoName, Path: t.TempDir(), BaseBranch: mainBranch},
			{Name: webRepoName, Path: t.TempDir(), BaseBranch: mainBranch},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			apiRepoName: {
				Type:                     feature.CycleRebase,
				Status:                   feature.RepoCycleNeedUserInput,
				Count:                    1,
				PendingNeedUserInputPath: gatePath,
			},
			webRepoName: {
				Type:                     feature.CycleRebase,
				Status:                   feature.RepoCycleNeedUserInput,
				Count:                    1,
				PendingNeedUserInputPath: gatePath,
			},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	if err := o.HandleNeedUserInputDecision(featureID, NeedUserInputDecision{
		Decision:  "abort",
		RepoName:  apiRepoName,
		CycleType: feature.CycleRebase,
	}); err != nil {
		t.Fatalf("HandleNeedUserInputDecision abort: %v", err)
	}

	got, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if got.Status != feature.StatusPublished {
		t.Fatalf("Status = %q, want published", got.Status)
	}
	if got.PendingNeedUserInputPath != "" {
		t.Fatalf("feature-level PendingNeedUserInputPath = %q, want cleared", got.PendingNeedUserInputPath)
	}
	for _, name := range []string{apiRepoName, webRepoName} {
		rc := got.RepoCycles[name]
		if rc == nil {
			t.Fatalf("RepoCycles[%s] missing", name)
		}
		if rc.Status != feature.RepoCycleFailed {
			t.Fatalf("RepoCycles[%s].Status = %q, want failed", name, rc.Status)
		}
		if rc.PendingNeedUserInputPath != "" {
			t.Fatalf("RepoCycles[%s].PendingNeedUserInputPath = %q, want cleared", name, rc.PendingNeedUserInputPath)
		}
		if rc.LastError != "User rejected generated-file policy." {
			t.Fatalf("RepoCycles[%s].LastError = %q", name, rc.LastError)
		}
	}
}

func writeAnsweredRebaseGate(t *testing.T, summary string, prompts ...string) string {
	t.Helper()
	rec := agent.NeedUserInputRecord{Summary: summary, Iteration: 1}
	for i, prompt := range prompts {
		rec.Questions = append(rec.Questions, agent.NeedUserInputQuestion{
			Index:  i + 1,
			Prompt: prompt,
			Answer: "use the generated-file policy from the feature plan",
		})
	}
	path := filepath.Join(t.TempDir(), agent.NeedUserInputArtifactName)
	if err := agent.WriteNeedUserInputRecord(path, rec); err != nil {
		t.Fatalf("write gate artifact: %v", err)
	}
	return path
}

func rebaseTargetNames(targets []agent.RebaseRepoTarget) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.RepoName)
	}
	return names
}
