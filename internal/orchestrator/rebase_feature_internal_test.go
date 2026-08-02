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
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
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

func TestResolveRebaseTargetUsesConcreteGitDefaultBranch(t *testing.T) {
	repoPath := initGitWorktree(t, "rebase-default")
	runCompletionGit(t, repoPath, "branch", "-m", "trunk")
	repo := &feature.FeatureRepo{Name: repoAName, Path: repoPath}
	f := &feature.Feature{Repos: []feature.FeatureRepo{*repo}}

	got := New(Deps{}, Hooks{}).resolveRebaseTarget(f, repo)
	if got != "trunk" {
		t.Fatalf("resolveRebaseTarget() = %q; want concrete git default branch trunk", got)
	}
}

func writeRebaseVerificationGate(t *testing.T, stateRoot, featureID, summary string) string {
	t.Helper()
	contractPath := filepath.Join(stateRoot, featureID, "testing-contract.yaml")
	if err := agent.WriteTestingContract(contractPath, agent.TestingContract{
		Version:  1,
		Revision: 1,
		Items: []agent.TestingContractItem{{
			ID: "rebase-capability",
			Policy: agent.TestingContractItemPolicy{
				Required:    true,
				AllowWaiver: true,
			},
		}},
	}); err != nil {
		t.Fatalf("WriteTestingContract: %v", err)
	}
	rec := agent.SynthesizeVerificationNeedUserInputGate(contractPath, 1, []string{"rebase-capability"}, 2)
	rec.Summary = summary
	rec.Questions[0].Answer = agent.NeedUserVerificationRetryAfterAuth
	gatePath := filepath.Join(stateRoot, featureID, "rebase-iteration-02", agent.NeedUserInputArtifactName)
	if err := agent.WriteNeedUserInputRecord(gatePath, rec); err != nil {
		t.Fatalf("WriteNeedUserInputRecord: %v", err)
	}
	return gatePath
}

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
	publisher *mocks.MockRemoteOps
	remotes   map[string]string
	featureID string
	feature   *feature.Feature
}

func newFeatureRebaseHarnessFixture(t *testing.T, status feature.Status, repoNames []string) *featureRebaseHarnessFixture {
	t.Helper()

	store := feature.NewStore(t.TempDir())
	manager := feature.NewManager(store, config.NewDefault())
	publisher := mocks.NewMockRemoteOps()

	repos := make([]feature.FeatureRepo, 0, len(repoNames))
	repoStates := make(map[string]*feature.RepoState, len(repoNames))
	remotes := make(map[string]string, len(repoNames))
	for _, name := range repoNames {
		worktree, remote := testutil.InitPublishReadyGitRepo(t)
		testutil.CreateBranch(t, worktree, "feature/feature-rebase")
		remotes[name] = remote
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
		orch:      New(Deps{Lifecycle: manager, Store: store, Remote: publisher}, Hooks{}),
		publisher: publisher,
		remotes:   remotes,
		featureID: f.ID,
		feature:   f,
	}
}

func (f *featureRebaseHarnessFixture) repo(name string) feature.FeatureRepo {
	f.t.Helper()
	for _, repo := range f.feature.Repos {
		if repo.Name == name {
			return repo
		}
	}
	f.t.Fatalf("repo %q not found", name)
	return feature.FeatureRepo{}
}

func (f *featureRebaseHarnessFixture) advanceBase(name, path, content string) {
	f.t.Helper()
	repo := f.repo(name)
	runCompletionGit(f.t, repo.WorktreePath, "checkout", mainBranch)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repo.WorktreePath, path)), 0o755); err != nil {
		f.t.Fatalf("create base fixture directory: %v", err)
	}
	testutil.CommitFile(f.t, repo.WorktreePath, path, content, "advance base")
	testutil.SimulatePush(f.t, repo.WorktreePath, f.remotes[name], mainBranch, mainBranch)
	runCompletionGit(f.t, repo.WorktreePath, "checkout", repo.Branch)
}

func (f *featureRebaseHarnessFixture) conflictWithBase(name, path string) {
	f.t.Helper()
	repo := f.repo(name)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repo.WorktreePath, path)), 0o755); err != nil {
		f.t.Fatalf("create feature fixture directory: %v", err)
	}
	testutil.CommitFile(f.t, repo.WorktreePath, path, "feature\n", "feature change")
	f.advanceBase(name, path, "base\n")
}

func (f *featureRebaseHarnessFixture) breakOrigin(name string) {
	f.t.Helper()
	repo := f.repo(name)
	runCompletionGit(f.t, repo.WorktreePath, "remote", "set-url", "origin", filepath.Join(f.t.TempDir(), "missing.git"))
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

func rebaseFinalReviewResult(result *agent.FeatureFinalReviewResult) chan *agent.FeatureFinalReviewResult {
	ch := make(chan *agent.FeatureFinalReviewResult, 1)
	ch <- result
	return ch
}

func (f *featureRebaseHarnessFixture) setFinalReviewResult(result *agent.FeatureFinalReviewResult, before func()) *int {
	f.t.Helper()
	calls := 0
	f.orch.SetRunFeatureCycleFinalReviewFn(func(*feature.Feature) (chan *agent.FeatureFinalReviewResult, error) {
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
	return f.setFinalReviewResult(&agent.FeatureFinalReviewResult{FinalStatus: reviewStatusPassed}, before)
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

func drainCycleProgressEvents(orch *Orchestrator) []ports.Event {
	var events []ports.Event
	for {
		select {
		case event := <-orch.Events():
			if event.Type == ports.CycleProgress {
				events = append(events, event)
			}
		default:
			return events
		}
	}
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
	fixture.orch.SetRunFeatureCycleFinalReviewFn(func(*feature.Feature) (chan *agent.FeatureFinalReviewResult, error) {
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
	if got := countRebaseMockCalls(fixture.publisher.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
	events := drainCycleProgressEvents(fixture.orch)
	if len(events) < 8 {
		t.Fatalf("CycleProgress events = %d, want start + three updates per repo + success", len(events))
	}
	repoEvents := map[string]int{}
	for _, event := range events {
		repoEvents[event.RepoName]++
	}
	if repoEvents[apiRepoName] < 3 || repoEvents[webRepoName] < 3 {
		t.Fatalf("per-repo CycleProgress events = %+v, want at least three for api and web", repoEvents)
	}
}

func TestStartFeatureRebaseHarnessFailurePersistsRepoErrorAndSkipsSmartAgent(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusCodeReady, []string{apiRepoName, webRepoName})
	fixture.breakOrigin(apiRepoName)
	fixture.orch.runRebaseLoopFn = func(agent.RebaseLoopConfig, ports.SessionManager) (*agent.RebaseLoopResult, error) {
		t.Fatal("smart rebase agent should not start after harness failure")
		return nil, nil
	}
	fixture.orch.SetRunFeatureCycleFinalReviewFn(func(*feature.Feature) (chan *agent.FeatureFinalReviewResult, error) {
		t.Fatal("Final Review should not start after harness failure")
		return nil, nil
	})

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	fixture.wait()

	loaded := fixture.load()
	if got := loaded.RepoStates[apiRepoName].LastError; !strings.Contains(got, "fetch failed") {
		t.Fatalf("RepoStates[api].LastError = %q, want fetch failure", got)
	}
	if loaded.ActiveCycle == nil || loaded.ActiveCycle.Status != feature.RepoCycleFailed ||
		!strings.Contains(loaded.ActiveCycle.LastError, "fetch failed") {
		t.Fatalf("ActiveCycle = %+v, want retained fetch failure", loaded.ActiveCycle)
	}
	if loaded.RebaseOperation == nil || loaded.RebaseOperation.Stage != feature.RebaseStageHarness {
		t.Fatalf("RebaseOperation = %+v, want retained harness operation", loaded.RebaseOperation)
	}
	if got := countRebaseMockCalls(fixture.publisher.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
	var sawFailureEvent bool
	for _, event := range drainCycleProgressEvents(fixture.orch) {
		if event.Error != nil && strings.Contains(event.Error.Error(), "fetch failed") {
			sawFailureEvent = true
		}
	}
	if !sawFailureEvent {
		t.Fatal("CycleProgress did not expose the retained harness failure")
	}
}

func TestStartFeatureRebaseHarnessMixedConflictAndFailurePreservesOperation(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusCodeReady, []string{apiRepoName, webRepoName})
	fixture.conflictWithBase(apiRepoName, "conflicted.go")
	fixture.breakOrigin(webRepoName)
	fixture.orch.runRebaseLoopFn = func(agent.RebaseLoopConfig, ports.SessionManager) (*agent.RebaseLoopResult, error) {
		t.Fatal("smart rebase agent should not start for mixed harness conflict and failure")
		return nil, nil
	}
	fixture.orch.SetRunFeatureCycleFinalReviewFn(func(*feature.Feature) (chan *agent.FeatureFinalReviewResult, error) {
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
	if loaded.ActiveCycle.Status != feature.RepoCycleFailed ||
		!strings.Contains(loaded.ActiveCycle.LastError, "fetch failed") {
		t.Fatalf("ActiveCycle = %+v, want terminal mixed harness failure", loaded.ActiveCycle)
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
	if !strings.Contains(webProgress.LastError, "fetch failed") {
		t.Fatalf("web progress LastError = %q, want fetch failure", webProgress.LastError)
	}
	if got := loaded.RepoStates[webRepoName].LastError; !strings.Contains(got, "fetch failed") {
		t.Fatalf("RepoStates[web].LastError = %q, want fetch failure", got)
	}
	if got := countRebaseMockCalls(fixture.publisher.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
}

func TestStartFeatureRebaseCleanChangeRunsFinalReviewBeforeAutoPush(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName, webRepoName})
	apiPath := fixture.feature.Repos[0].WorktreePath
	fixture.advanceBase(apiRepoName, "base.txt", "advance\n")
	fixture.orch.runRebaseLoopFn = func(agent.RebaseLoopConfig, ports.SessionManager) (*agent.RebaseLoopResult, error) {
		t.Fatal("smart rebase agent should not start for clean harness change")
		return nil, nil
	}
	finalReviewCalls := fixture.setFinalReviewPassed(func() {
		if got := countRebaseMockCalls(fixture.publisher.Calls, "ForcePush"); got != 0 {
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
	pushes := forcePushCalls(fixture.publisher.Calls)
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
	fixture.advanceBase(apiRepoName, "base.txt", "advance\n")
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
	if got := countRebaseMockCalls(fixture.publisher.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
}

func TestStartFeatureRebaseUnsupportedFinalReviewPlanRevisionRetainsFailedCycle(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName})
	fixture.feature.Pipeline = feature.PipelineMedium
	fixture.saveFeature()
	fixture.advanceBase(apiRepoName, "base.txt", "advance\n")
	fixture.setFinalReviewResult(&agent.FeatureFinalReviewResult{
		FinalStatus:          finalStatusPlanRevisionRequired,
		PlanRevisionFeedback: "MISSING_EVIDENCE_REQUIREMENT behavioral: Cover the clean rebase plan revision path.",
	}, nil)

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	fixture.wait()

	loaded := fixture.load()
	if loaded.Status != feature.StatusPublished {
		t.Fatalf("Status = %q, want %q", loaded.Status, feature.StatusPublished)
	}
	if loaded.LastError != "" || loaded.FailureType != "" {
		t.Fatalf("feature failure fields = %q/%q, want untouched", loaded.LastError, loaded.FailureType)
	}
	if loaded.RebaseOperation == nil || loaded.RebaseOperation.Stage != feature.RebaseStageFinalReview {
		t.Fatalf("RebaseOperation = %+v, want retained final review operation", loaded.RebaseOperation)
	}
	if loaded.ActiveCycle == nil || loaded.ActiveCycle.Status != feature.RepoCycleFailed ||
		!strings.Contains(loaded.ActiveCycle.LastError, "plan_revision_required") {
		t.Fatalf("ActiveCycle = %+v, want retained failed final review", loaded.ActiveCycle)
	}
	if got := loaded.RepoStates[apiRepoName].LastError; !strings.Contains(got, "rebase final review failed") {
		t.Fatalf("RepoStates[api].LastError = %q, want rebase final review failure", got)
	}
	if got := countRebaseMockCalls(fixture.publisher.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
}

func TestStartFeatureRebaseInterruptedInFlightHarnessDoesNotRunFinalReviewOrPush(t *testing.T) {
	const waitTimeout = 10 * time.Second

	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName, webRepoName})
	apiPath := fixture.feature.Repos[0].WorktreePath
	webPath := fixture.feature.Repos[1].WorktreePath
	webHeadBefore, err := git.CurrentHeadSHA(webPath)
	if err != nil {
		t.Fatalf("web HEAD before rebase: %v", err)
	}
	blocked := make(chan struct{})
	finalReviewCalled := make(chan struct{}, 1)
	testutil.CommitFile(t, apiPath, "api-feature.txt", "feature\n", "feature commit before blocking rebase")
	fixture.advanceBase(apiRepoName, "api-base.txt", "advance\n")
	fixture.advanceBase(webRepoName, "web-base.txt", "advance\n")
	startedPath := filepath.Join(t.TempDir(), "pre-rebase-started")
	releasePath := filepath.Join(t.TempDir(), "pre-rebase-release")
	hookPath := filepath.Join(apiPath, ".git", "hooks", "pre-rebase")
	hook := "#!/bin/sh\n: > '" + startedPath + "'\nwhile [ ! -f '" + releasePath + "' ]; do sleep 0.01; done\n"
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil {
		t.Fatalf("write pre-rebase hook: %v", err)
	}
	go func() {
		for {
			if _, err := os.Stat(startedPath); err == nil {
				close(blocked)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	fixture.orch.SetRunFeatureCycleFinalReviewFn(func(*feature.Feature) (chan *agent.FeatureFinalReviewResult, error) {
		finalReviewCalled <- struct{}{}
		return rebaseFinalReviewResult(&agent.FeatureFinalReviewResult{FinalStatus: reviewStatusPassed}), nil
	})

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	select {
	case <-blocked:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for harness rebase to block")
	}
	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- fixture.orch.InterruptFeature(fixture.featureID)
	}()
	deadline := time.After(waitTimeout)
	for !fixture.orch.featureRebaseStopRequested(fixture.featureID) {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for interrupt request to be recorded")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	if err := os.WriteFile(releasePath, []byte("release\n"), 0o644); err != nil {
		t.Fatalf("release pre-rebase hook: %v", err)
	}
	select {
	case err := <-interruptDone:
		if err != nil {
			t.Fatalf("InterruptFeature: %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for interrupt to finish after harness release")
	}
	fixture.wait()

	select {
	case <-finalReviewCalled:
		t.Fatal("Final Review ran after interrupted rebase harness completed")
	default:
	}
	if got := countRebaseMockCalls(fixture.publisher.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
	webHeadAfter, err := git.CurrentHeadSHA(webPath)
	if err != nil {
		t.Fatalf("web HEAD after interrupt: %v", err)
	}
	if webHeadAfter != webHeadBefore {
		t.Fatalf("web HEAD = %s, want unchanged %s because second repo must not rebase", webHeadAfter, webHeadBefore)
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
	if loaded.ActivePhaseStart != nil {
		t.Fatal("ActivePhaseStart is still running after rebase interrupt")
	}
	if loaded.PhaseTimings["rebase-1"] <= 0 {
		t.Fatalf("PhaseTimings[rebase-1] = %s, want banked active time", loaded.PhaseTimings["rebase-1"])
	}
}

func TestResumeFeatureRebaseRestoresInterruptedSmartCycle(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName})
	fixture.orch.deps.PhaseRunner = &agent.PhaseRunner{StateDir: fixture.store.BaseDir}
	if err := fixture.manager.StartFeatureRebaseOperation(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebaseOperation: %v", err)
	}
	if err := fixture.manager.UpdateFeatureRebaseRepo(
		fixture.featureID,
		apiRepoName,
		feature.RebaseRepoStatusConflict,
		feature.RebaseRepoProgress{
			RebaseTarget:  mainBranch,
			ConflictFiles: []string{"internal/api/conflicted.go"},
		},
	); err != nil {
		t.Fatalf("UpdateFeatureRebaseRepo: %v", err)
	}
	if err := fixture.manager.MarkFeatureRebaseStage(fixture.featureID, feature.RebaseStageSmartRebase); err != nil {
		t.Fatalf("MarkFeatureRebaseStage: %v", err)
	}
	if err := fixture.store.Modify(fixture.featureID, func(f *feature.Feature) error {
		f.Status = feature.StatusInterrupted
		f.CurrentPhase = feature.PhasePublish
		f.ActiveCycle.Status = feature.RepoCycleInterrupted
		return nil
	}); err != nil {
		t.Fatalf("interrupt feature: %v", err)
	}

	var smartCalls int
	fixture.orch.runRebaseLoopFn = func(cfg agent.RebaseLoopConfig, _ ports.SessionManager) (*agent.RebaseLoopResult, error) {
		smartCalls++
		if !cfg.ResumeExistingCycle {
			t.Fatal("ResumeExistingCycle = false, want existing cycle artifacts reused")
		}
		if got, want := cfg.BehindRepos, []agent.RebaseRepoTarget{{
			RepoName:      apiRepoName,
			RebaseTarget:  mainBranch,
			ConflictFiles: []string{"internal/api/conflicted.go"},
			PRURL:         "https://github.example/api/pull/1",
		}}; !reflect.DeepEqual(got, want) {
			t.Fatalf("BehindRepos = %#v, want %#v", got, want)
		}
		return &agent.RebaseLoopResult{
			FinalStatus: reviewStatusPassed,
			Repos:       []string{apiRepoName},
		}, nil
	}
	fixture.setFinalReviewPassed(nil)

	resumer, ok := any(fixture.orch).(interface {
		ResumeFeatureRebase(string) error
	})
	if !ok {
		t.Fatal("Orchestrator does not implement ResumeFeatureRebase")
	}
	if err := resumer.ResumeFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("ResumeFeatureRebase: %v", err)
	}
	fixture.wait()

	if smartCalls != 1 {
		t.Fatalf("smart rebase calls = %d, want 1", smartCalls)
	}
	loaded := fixture.load()
	if loaded.Status != feature.StatusPublished {
		t.Fatalf("Status = %s, want Published after resumed cycle completes", loaded.Status)
	}
	if loaded.RebaseOperation != nil {
		t.Fatalf("RebaseOperation = %+v, want nil after resumed cycle completes", loaded.RebaseOperation)
	}
}

func TestResumeNeedUserInputResumesFeatureRebaseCycle(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName})
	fixture.orch.deps.PhaseRunner = &agent.PhaseRunner{StateDir: fixture.store.BaseDir}
	if err := fixture.manager.StartFeatureRebaseOperation(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebaseOperation: %v", err)
	}
	if err := fixture.manager.UpdateFeatureRebaseRepo(
		fixture.featureID,
		apiRepoName,
		feature.RebaseRepoStatusConflict,
		feature.RebaseRepoProgress{
			RebaseTarget:  mainBranch,
			ConflictFiles: []string{"internal/api/conflicted.go"},
		},
	); err != nil {
		t.Fatalf("UpdateFeatureRebaseRepo: %v", err)
	}
	if err := fixture.manager.MarkFeatureRebaseStage(fixture.featureID, feature.RebaseStageSmartRebase); err != nil {
		t.Fatalf("MarkFeatureRebaseStage: %v", err)
	}
	gatePath := writeRebaseVerificationGate(t, fixture.store.BaseDir, fixture.featureID, "Choose the conflict resolution.")
	if err := fixture.manager.MarkFeatureRebaseNeedUserInput(
		fixture.featureID,
		gatePath,
		2,
		"Choose the conflict resolution.",
	); err != nil {
		t.Fatalf("MarkFeatureRebaseNeedUserInput: %v", err)
	}

	var smartCalls int
	fixture.orch.runRebaseLoopFn = func(cfg agent.RebaseLoopConfig, _ ports.SessionManager) (*agent.RebaseLoopResult, error) {
		smartCalls++
		if !cfg.ResumeExistingCycle {
			t.Fatal("ResumeExistingCycle = false, want existing smart-rebase loop")
		}
		return &agent.RebaseLoopResult{FinalStatus: reviewStatusPassed, Repos: []string{apiRepoName}}, nil
	}
	fixture.setFinalReviewPassed(nil)

	if err := fixture.orch.ResumeNeedUserInput(fixture.featureID, NeedUserInputResume{}); err != nil {
		t.Fatalf("ResumeNeedUserInput: %v", err)
	}
	fixture.wait()

	if smartCalls != 1 {
		t.Fatalf("smart rebase calls = %d, want 1", smartCalls)
	}
	loaded := fixture.load()
	if loaded.Status != feature.StatusPublished {
		t.Fatalf("Status = %q, want Published", loaded.Status)
	}
	if loaded.ActiveCycle != nil || loaded.RebaseOperation != nil {
		t.Fatalf("rebase state = cycle %+v operation %+v, want cleared success", loaded.ActiveCycle, loaded.RebaseOperation)
	}
}

func TestResumeNeedUserInputKeepsRebaseGateWhenRelaunchCannotDispatch(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName})
	if err := fixture.manager.StartFeatureRebaseOperation(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebaseOperation: %v", err)
	}
	if err := fixture.manager.MarkFeatureRebaseStage(fixture.featureID, feature.RebaseStageSmartRebase); err != nil {
		t.Fatalf("MarkFeatureRebaseStage: %v", err)
	}
	gatePath := writeRebaseVerificationGate(t, fixture.store.BaseDir, fixture.featureID, "Choose the conflict resolution.")
	if err := fixture.manager.MarkFeatureRebaseNeedUserInput(
		fixture.featureID,
		gatePath,
		2,
		"Choose the conflict resolution.",
	); err != nil {
		t.Fatalf("MarkFeatureRebaseNeedUserInput: %v", err)
	}

	err := fixture.orch.ResumeNeedUserInput(fixture.featureID, NeedUserInputResume{})
	if err == nil || !strings.Contains(err.Error(), "phase runner not configured") {
		t.Fatalf("ResumeNeedUserInput error = %v, want dispatch configuration failure", err)
	}
	loaded := fixture.load()
	if loaded.ActiveCycle == nil || loaded.ActiveCycle.Status != feature.RepoCycleNeedUserInput {
		t.Fatalf("ActiveCycle = %+v, want input gate retained", loaded.ActiveCycle)
	}
	if loaded.ActiveCycle.PendingNeedUserInputPath != gatePath {
		t.Fatalf("PendingNeedUserInputPath = %q, want %q", loaded.ActiveCycle.PendingNeedUserInputPath, gatePath)
	}
}

func TestRetryFeatureRebaseFromPublishSkipsFinalReview(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName})
	api := fixture.feature.Repos[0]
	if err := fixture.manager.StartFeatureRebaseOperation(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebaseOperation: %v", err)
	}
	if err := fixture.manager.UpdateFeatureRebaseRepo(
		fixture.featureID,
		apiRepoName,
		feature.RebaseRepoStatusChanged,
		feature.RebaseRepoProgress{
			RebaseTarget: originMainBranch,
			Changed:      true,
		},
	); err != nil {
		t.Fatalf("UpdateFeatureRebaseRepo: %v", err)
	}
	if err := fixture.manager.MarkFeatureRebaseStage(fixture.featureID, feature.RebaseStagePublish); err != nil {
		t.Fatalf("MarkFeatureRebaseStage: %v", err)
	}
	if err := fixture.manager.FailFeatureRebaseCycle(fixture.featureID, "transient push failure"); err != nil {
		t.Fatalf("FailFeatureRebaseCycle: %v", err)
	}
	fixture.orch.SetRunFeatureCycleFinalReviewFn(func(*feature.Feature) (chan *agent.FeatureFinalReviewResult, error) {
		t.Fatal("Final Review must not repeat when retrying the publish stage")
		return nil, nil
	})

	if err := fixture.orch.RetryFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("RetryFeatureRebase: %v", err)
	}
	fixture.wait()

	pushes := forcePushCalls(fixture.publisher.Calls)
	if len(pushes) != 1 || pushes[0].Args[0] != api.WorktreePath {
		t.Fatalf("ForcePush calls = %+v, want one publish retry for %q", pushes, api.WorktreePath)
	}
	loaded := fixture.load()
	if loaded.Status != feature.StatusPublished {
		t.Fatalf("Status = %q, want Published", loaded.Status)
	}
	if loaded.ActiveCycle != nil || loaded.RebaseOperation != nil {
		t.Fatalf("rebase state = cycle %+v operation %+v, want cleared success", loaded.ActiveCycle, loaded.RebaseOperation)
	}
}

func TestRetryFeatureRebaseHarnessPreservesConflictAndRechecksFailedRepo(t *testing.T) {
	fixture := newFeatureRebaseHarnessFixture(t, feature.StatusPublished, []string{apiRepoName, webRepoName})
	fixture.orch.deps.PhaseRunner = &agent.PhaseRunner{StateDir: fixture.store.BaseDir}
	if err := fixture.manager.StartFeatureRebaseOperation(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebaseOperation: %v", err)
	}
	if err := fixture.manager.UpdateFeatureRebaseRepo(
		fixture.featureID,
		apiRepoName,
		feature.RebaseRepoStatusConflict,
		feature.RebaseRepoProgress{
			RebaseTarget:  mainBranch,
			ConflictFiles: []string{"api/conflicted.go"},
		},
	); err != nil {
		t.Fatalf("UpdateFeatureRebaseRepo(api): %v", err)
	}
	if err := fixture.manager.UpdateFeatureRebaseRepo(
		fixture.featureID,
		webRepoName,
		feature.RebaseRepoStatusFailed,
		feature.RebaseRepoProgress{LastError: "temporary fetch failure"},
	); err != nil {
		t.Fatalf("UpdateFeatureRebaseRepo(web): %v", err)
	}
	if err := fixture.manager.FailFeatureRebaseCycle(fixture.featureID, "web: temporary fetch failure"); err != nil {
		t.Fatalf("FailFeatureRebaseCycle: %v", err)
	}

	// If the preserved conflict repo were rechecked, its broken origin would
	// fail the retry. A successful retry therefore proves only the failed web
	// repo was fetched and re-evaluated.
	fixture.breakOrigin(apiRepoName)
	fixture.orch.runRebaseLoopFn = func(cfg agent.RebaseLoopConfig, _ ports.SessionManager) (*agent.RebaseLoopResult, error) {
		if got, want := cfg.BehindRepos, []agent.RebaseRepoTarget{{
			RepoName:      apiRepoName,
			RebaseTarget:  mainBranch,
			ConflictFiles: []string{"api/conflicted.go"},
			PRURL:         "https://github.example/api/pull/1",
		}}; !reflect.DeepEqual(got, want) {
			t.Fatalf("BehindRepos = %#v, want %#v", got, want)
		}
		return &agent.RebaseLoopResult{FinalStatus: reviewStatusPassed, Repos: []string{apiRepoName}}, nil
	}
	fixture.setFinalReviewPassed(nil)

	if err := fixture.orch.RetryFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("RetryFeatureRebase: %v", err)
	}
	fixture.wait()

	loaded := fixture.load()
	if loaded.ActiveCycle != nil || loaded.RebaseOperation != nil {
		t.Fatalf("rebase state = cycle %+v operation %+v, want cleared success", loaded.ActiveCycle, loaded.RebaseOperation)
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
	fixture.orch = New(Deps{Lifecycle: lifecycle, Store: fixture.store, Remote: fixture.publisher}, Hooks{})
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

	if got := countRebaseMockCalls(fixture.publisher.Calls, "ForcePush"); got != 0 {
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
	fixture.conflictWithBase(apiRepoName, "internal/api/conflicted.go")
	fixture.advanceBase("worker", "worker-base.txt", "advance\n")
	fixture.orch.runRebaseLoopFn = func(cfg agent.RebaseLoopConfig, _ ports.SessionManager) (*agent.RebaseLoopResult, error) {
		smartCalls++
		events = append(events, "smart")
		if got := countRebaseMockCalls(fixture.publisher.Calls, "ForcePush"); got != 0 {
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
		if got := countRebaseMockCalls(fixture.publisher.Calls, "ForcePush"); got != 0 {
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
	pushes := forcePushCalls(fixture.publisher.Calls)
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
	fixture.conflictWithBase(apiRepoName, "api.go")

	fingerprints := map[string]string{
		apiPath: "api-before",
		webPath: "web-before",
	}
	fixture.orch.worktreeFingerprintFn = func(worktreePath string) (string, error) {
		return fingerprints[worktreePath], nil
	}
	webHeadBefore, err := git.CurrentHeadSHA(webPath)
	if err != nil {
		t.Fatalf("web HEAD before smart rebase: %v", err)
	}
	fixture.orch.runRebaseLoopFn = func(cfg agent.RebaseLoopConfig, _ ports.SessionManager) (*agent.RebaseLoopResult, error) {
		if got, want := cfg.WorkspaceRepos, []string{apiRepoName, webRepoName}; !reflect.DeepEqual(got, want) {
			t.Fatalf("WorkspaceRepos = %v, want %v", got, want)
		}
		fingerprints[webPath] = "web-after-smart-agent-edit"
		if err := os.WriteFile(filepath.Join(webPath, "smart-edit.txt"), []byte("edited\n"), 0o644); err != nil {
			t.Fatalf("write smart rebase context edit: %v", err)
		}
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

	pushes := forcePushCalls(fixture.publisher.Calls)
	if len(pushes) != 2 {
		t.Fatalf("ForcePush calls = %d, want api conflict target and edited web context repo", len(pushes))
	}
	if pushes[0].Args[0] != apiPath || pushes[1].Args[0] != webPath {
		t.Fatalf("ForcePush order/worktrees = %+v, want api then web", pushes)
	}
	webHeadAfter, err := git.CurrentHeadSHA(webPath)
	if err != nil {
		t.Fatalf("web HEAD after smart rebase: %v", err)
	}
	if webHeadAfter == webHeadBefore || git.HasUncommittedChanges(webPath) {
		t.Fatalf("web smart edit was not committed before push: head %s -> %s dirty=%v", webHeadBefore, webHeadAfter, git.HasUncommittedChanges(webPath))
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
	repo := fixture.repo("local")
	runCompletionGit(t, repo.WorktreePath, "checkout", mainBranch)
	testutil.CommitFile(t, repo.WorktreePath, "local-base.txt", "advance\n", "advance local base")
	runCompletionGit(t, repo.WorktreePath, "checkout", repo.Branch)
	finalReviewCalls := fixture.setFinalReviewPassed(nil)

	if err := fixture.orch.StartFeatureRebase(fixture.featureID); err != nil {
		t.Fatalf("StartFeatureRebase: %v", err)
	}
	fixture.wait()

	if *finalReviewCalls != 1 {
		t.Fatalf("Final Review calls = %d, want 1", *finalReviewCalls)
	}
	loaded := fixture.load()
	if got := loaded.Repos[0].BaseBranch; got != mainBranch {
		t.Fatalf("base branch = %q, want main", got)
	}
	if got := countRebaseMockCalls(fixture.publisher.Calls, "ForcePush"); got != 0 {
		t.Fatalf("ForcePush calls = %d, want 0", got)
	}
}
