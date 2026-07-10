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

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type rebaseLoopSessionManagerStub struct {
	startCalls int
}

func (s *rebaseLoopSessionManagerStub) StartSession(
	string,
	string,
	feature.Phase,
	[]string,
	string,
	[]string,
	...*ports.SessionOpts,
) (ports.SessionHandle, error) {
	s.startCalls++
	return nil, errors.New("unexpected session start")
}

func (s *rebaseLoopSessionManagerStub) StopSession(string) error                   { return nil }
func (s *rebaseLoopSessionManagerStub) GetSession(string) ports.SessionView        { return nil }
func (s *rebaseLoopSessionManagerStub) ActiveSessions() []ports.SessionView        { return nil }
func (s *rebaseLoopSessionManagerStub) RecentSessions(int) []ports.SessionView     { return nil }
func (s *rebaseLoopSessionManagerStub) FeatureSessions(string) []ports.SessionView { return nil }
func (s *rebaseLoopSessionManagerStub) SendInput(string, []byte) error             { return nil }
func (s *rebaseLoopSessionManagerStub) Attach(string) (ports.SessionView, error)   { return nil, nil }
func (s *rebaseLoopSessionManagerStub) Detach()                                    {}
func (s *rebaseLoopSessionManagerStub) Shutdown()                                  {}
func (s *rebaseLoopSessionManagerStub) IsShuttingDown() bool                       { return false }

func newRebaseTestFeature(t *testing.T, stateDir, featureID string, repoNames []string) (*feature.Store, *feature.Feature, []string) {
	t.Helper()
	return newLoopTestFeature(t, stateDir, featureID, repoNames, loopTestFeatureOptions{
		Name:         "Rebase Loop Test",
		Slug:         "rebase-loop-test",
		Description:  "Feature-level rebase cycle test fixture",
		ExitCriteria: "Rebase complete and force-pushed",
	})
}

// stubRunImplementFn returns a RunImplementFn that yields the given
// LoopResult (and optional error) without running a real session. Used
// to drive the rebase loop's outer state-machine deterministically.
func stubRunImplementFn(result *LoopResult, err error) func(ImplementConfig, ports.SessionManager) (*LoopResult, error) {
	return func(_ ImplementConfig, _ ports.SessionManager) (*LoopResult, error) {
		return result, err
	}
}

// capturingRunImplementFn records every ImplementConfig the rebase loop
// hands to the implement-loop dispatcher. Returns the captured slice for
// assertions.
func capturingRunImplementFn(result *LoopResult) (
	func(ImplementConfig, ports.SessionManager) (*LoopResult, error),
	*[]ImplementConfig,
) {
	var captured []ImplementConfig
	return func(c ImplementConfig, _ ports.SessionManager) (*LoopResult, error) {
		// Copy slices so subsequent loop mutations don't race the
		// captured copy.
		cp := c
		cp.AdditionalDirs = append([]string(nil), c.AdditionalDirs...)
		captured = append(captured, cp)
		return result, nil
	}, &captured
}

// TestRunRebaseLoop_SuccessAtomicallyStampsBehindRepos covers the SUCCESS
// path: the inner implement loop returns review_passed; the rebase loop
// stamps every behind-subset repo to "awaiting_final_review" and
// clears Feature.ActiveCycle.
func TestRunRebaseLoop_SuccessAtomicallyStampsBehindRepos(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-success", []string{"api", "web", "infra"})

	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
			{RepoName: "web", RebaseTarget: "main"},
		},
		MaxIterations:  3,
		MaxConsecFails: 3,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil),
	}

	result, err := RunRebaseLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Errorf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if got, want := result.Repos, []string{"api", "web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Repos = %v, want %v", got, want)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Behind subset stamped AwaitingFinalReview.
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.Touched == false {
			t.Errorf("repo %s = %+v, want awaiting_final_review", name, st)
		}
	}
	// Repo outside behind subset preserved its prior status.
	if st := loaded.RepoStates["infra"]; st == nil || st.PRURL == "" {
		t.Errorf("infra = %+v, want pr_ready (preserved)", st)
	}
	// ActiveCycle cleared on success.
	if loaded.ActiveCycle != nil {
		t.Errorf("ActiveCycle = %+v, want nil after success", loaded.ActiveCycle)
	}
	// RebaseCount incremented.
	if loaded.RebaseCount() != 1 {
		t.Errorf("RebaseCount = %d, want 1", loaded.RebaseCount())
	}
}

// TestRunRebaseLoop_RetryLandsAfterConflictResolutionIteration drives the
// RETRY path: iteration 1 of the inner loop encounters a rebase conflict
// (loop returns review_passed after Iterations: 2 — implementer resolved
// in iteration 2, reviewer approved). The rebase loop reports those 2
// iterations and stamps success.
func TestRunRebaseLoop_RetryLandsAfterConflictResolutionIteration(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-retry", []string{"api", "web"})

	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{
				RepoName:      "api",
				RebaseTarget:  "main",
				ConflictFiles: []string{"internal/api/handler.go"},
			},
			{RepoName: "web", RebaseTarget: "main"},
		},
		MaxIterations:  5,
		MaxConsecFails: 3,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 2}, nil),
	}

	result, err := RunRebaseLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Errorf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2 (conflict resolved on iter-2)", result.Iterations)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.Touched == false {
			t.Errorf("repo %s = %+v, want awaiting_final_review", name, st)
		}
	}

	// The conflict files context should make it into the per-repo plan
	// section so the agent uses the "rebase-already-in-progress"
	// template. The plan is at runs/run-001/rebase-1/rebase-plan.md.
	planPath := filepath.Join(ActiveRunDir(stateDir, loaded), "rebase-1", "rebase-plan.md")
	planBytes, readErr := os.ReadFile(planPath)
	if readErr != nil {
		t.Fatalf("read rebase-plan.md: %v", readErr)
	}
	plan := string(planBytes)
	if !strings.Contains(plan, "rebase already in progress") {
		t.Errorf("plan missing in-progress marker:\n%s", plan)
	}
	if !strings.Contains(plan, "internal/api/handler.go") {
		t.Errorf("plan missing conflict file reference:\n%s", plan)
	}
}

// TestRunRebaseLoop_MaxIterationsTrip exercises the safety-rail trip:
// inner loop returns max_iterations. The rebase loop stamps every staged
// repo "failed"; ActiveCycle.Status flips to "failed" so the TUI
// can surface the failure row.
func TestRunRebaseLoop_MaxIterationsTrip(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-maxiter", []string{"api", "web"})

	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
		},
		MaxIterations:  2,
		MaxConsecFails: 5,
		RunImplementFn: stubRunImplementFn(&LoopResult{
			FinalStatus: "max_iterations",
			Iterations:  2,
			LastError:   "Reviewer kept rejecting: unresolved conflict in internal/api/handler.go",
		}, nil),
	}

	result, err := RunRebaseLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}
	if result.FinalStatus != "max_iterations" {
		t.Errorf("FinalStatus = %q, want max_iterations", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "unresolved conflict") {
		t.Errorf("LastError = %q, want it to include the inner error", result.LastError)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"api"} {
		st := loaded.RepoStates[name]
		if st == nil || st.LastError == "" {
			t.Errorf("repo %s = %+v, want failed", name, st)
		}
	}
	// Repo outside behind subset preserved its prior status.
	if st := loaded.RepoStates["web"]; st == nil || st.PRURL == "" {
		t.Errorf("web = %+v, want pr_ready (preserved)", st)
	}

	if loaded.ActiveCycle == nil {
		t.Fatal("ActiveCycle = nil, want non-nil with Status=failed (TUI must surface the failure)")
	}
	if loaded.ActiveCycle.Status != feature.RepoCycleFailed {
		t.Errorf("ActiveCycle.Status = %q, want failed", loaded.ActiveCycle.Status)
	}
	if loaded.ActiveCycle.Type != feature.CycleRebase {
		t.Errorf("ActiveCycle.Type = %q, want rebase", loaded.ActiveCycle.Type)
	}
}

// TestRunRebaseLoop_SafetyRailTrip exercises the consec-fails safety rail:
// inner loop returns safety_rail. The outer loop stamps "failed".
func TestRunRebaseLoop_SafetyRailTrip(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-safety", []string{"api", "web"})

	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
			{RepoName: "web", RebaseTarget: "main"},
		},
		MaxIterations:  10,
		MaxConsecFails: 2,
		RunImplementFn: stubRunImplementFn(&LoopResult{
			FinalStatus: "safety_rail",
			Iterations:  3,
			LastError:   "no progress for 3 consecutive iterations",
		}, nil),
	}

	result, err := RunRebaseLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}
	if result.FinalStatus != "safety_rail" {
		t.Errorf("FinalStatus = %q, want safety_rail", result.FinalStatus)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.LastError == "" {
			t.Errorf("repo %s = %+v, want failed", name, st)
		}
	}
}

// TestRunRebaseLoop_DispatchErrorStampsFailure verifies that when the
// inner loop dispatcher returns an error (not a graceful loop result),
// the rebase loop still atomically stamps "failed" and surfaces
// the error to the caller.
func TestRunRebaseLoop_DispatchErrorStampsFailure(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-dispatch-err", []string{"api"})

	dispatchErr := errors.New("session manager: ports.ErrSessionShuttingDown")
	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
		},
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(nil, dispatchErr),
	}

	result, err := RunRebaseLoop(cfg, nil)
	if err == nil {
		t.Fatalf("RunRebaseLoop returned nil error, want %v", dispatchErr)
	}
	if !errors.Is(err, dispatchErr) {
		t.Errorf("err = %v, want errors.Is %v", err, dispatchErr)
	}
	if result == nil {
		t.Fatal("result = nil, want non-nil with FinalStatus=failed")
	}
	if result.FinalStatus != "failed" {
		t.Errorf("FinalStatus = %q, want failed", result.FinalStatus)
	}

	loaded, _ := store.Load(f.ID)
	if st := loaded.RepoStates["api"]; st == nil || st.LastError == "" {
		t.Errorf("api = %+v, want failed", st)
	}
}

// TestRunRebaseLoop_InterruptedPreservesState verifies that when the
// inner loop returns "interrupted" (e.g. session manager shutdown), no
// atomic stamp is written and ActiveCycle stays at running so a restart
// can resume.
func TestRunRebaseLoop_InterruptedPreservesState(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-interrupt", []string{"api"})

	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
		},
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "interrupted", Iterations: 1}, nil),
	}

	result, err := RunRebaseLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}
	if result.FinalStatus != "interrupted" {
		t.Errorf("FinalStatus = %q, want interrupted", result.FinalStatus)
	}

	loaded, _ := store.Load(f.ID)
	// Interrupted: per-repo state preserved (no atomic stamp).
	if st := loaded.RepoStates["api"]; st == nil || st.PRURL == "" {
		t.Errorf("api = %+v, want pr_ready (preserved)", st)
	}
	if loaded.ActiveCycle == nil {
		t.Fatal("ActiveCycle = nil, want preserved at Status=running for resume")
	}
	if loaded.ActiveCycle.Status != feature.RepoCycleRunning {
		t.Errorf("ActiveCycle.Status = %q, want running (preserved)", loaded.ActiveCycle.Status)
	}
}

// TestRunRebaseLoop_NeedUserInputSurfacesGate verifies that an agent-authored
// NEED_USER_INPUT handoff pauses the rebase cycle instead of stamping the
// staged repos failed.
func TestRunRebaseLoop_NeedUserInputSurfacesGate(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-nui", []string{"api", "web"})
	gatePath := filepath.Join(stateDir, "rebase-1", "iteration-01", "need-user-input.yaml")

	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
			{RepoName: "web", RebaseTarget: "main"},
		},
		MaxIterations: 3,
		RunImplementFn: stubRunImplementFn(&LoopResult{
			FinalStatus:       "need_user_input",
			Iterations:        1,
			LastError:         "Build gate needs a human decision.",
			NeedUserInputPath: gatePath,
		}, nil),
	}

	result, err := RunRebaseLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}
	if result.FinalStatus != "need_user_input" {
		t.Fatalf("FinalStatus = %q, want need_user_input", result.FinalStatus)
	}
	if result.NeedUserInputPath != gatePath {
		t.Errorf("NeedUserInputPath = %q, want %q", result.NeedUserInputPath, gatePath)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.PendingNeedUserInputPath != gatePath {
		t.Errorf("PendingNeedUserInputPath = %q, want %q", loaded.PendingNeedUserInputPath, gatePath)
	}
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.LastError != "" {
			t.Errorf("repo %s = %+v, want prior state without failure", name, st)
		}
	}
	if loaded.ActiveCycle == nil {
		t.Fatal("ActiveCycle = nil, want preserved at Status=running for gate resume")
	}
	if loaded.ActiveCycle.Status != feature.RepoCycleRunning {
		t.Errorf("ActiveCycle.Status = %q, want running", loaded.ActiveCycle.Status)
	}
}

// TestRunRebaseLoop_NoBehindReposShortCircuits verifies the no-op
// degenerate case: zero behind repos returns FinalStatus=no_op without
// touching any state.
func TestRunRebaseLoop_NoBehindReposShortCircuits(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-empty", []string{"api"})

	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos:  nil, // nothing behind
	}

	result, err := RunRebaseLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}
	if result.FinalStatus != "no_op" {
		t.Errorf("FinalStatus = %q, want no_op", result.FinalStatus)
	}

	// Nothing should change in the store — no rebase count bump, no
	// active cycle, no repo state mutation.
	loaded, _ := store.Load(f.ID)
	if loaded.RebaseCount() != 0 {
		t.Errorf("RebaseCount = %d, want 0 (no-op should not increment)", loaded.RebaseCount())
	}
	if loaded.ActiveCycle != nil {
		t.Errorf("ActiveCycle = %+v, want nil (no-op should not set)", loaded.ActiveCycle)
	}
}

// TestRunRebaseLoop_WorkspaceReposEmptyFallsBackToBehindSubset verifies
// the legacy default: when WorkspaceRepos is empty, the loop mounts only
// the behind-subset repos. Asserts via captured ImplementConfig.AdditionalDirs.
func TestRunRebaseLoop_WorkspaceReposEmptyFallsBackToBehindSubset(t *testing.T) {
	stateDir := t.TempDir()
	store, f, repoPaths := newRebaseTestFeature(t, stateDir, "rebase-subset", []string{"api", "web", "infra"})

	captureFn, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1})

	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
			{RepoName: "web", RebaseTarget: "main"},
		},
		MaxIterations:  3,
		RunImplementFn: captureFn,
	}

	if _, err := RunRebaseLoop(cfg, nil); err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}

	if len(*captured) != 1 {
		t.Fatalf("RunImplementFn called %d times, want 1", len(*captured))
	}
	got := (*captured)[0]
	dirs := got.AdditionalDirs
	apiAbs, _ := filepath.Abs(repoPaths[0])
	webAbs, _ := filepath.Abs(repoPaths[1])
	infraAbs, _ := filepath.Abs(repoPaths[2])
	if !sliceContainsAny(dirs, apiAbs) {
		t.Errorf("AdditionalDirs missing api worktree %q (got %v)", apiAbs, dirs)
	}
	if !sliceContainsAny(dirs, webAbs) {
		t.Errorf("AdditionalDirs missing web worktree %q (got %v)", webAbs, dirs)
	}
	if sliceContainsAny(dirs, infraAbs) {
		t.Errorf("AdditionalDirs unexpectedly contains infra without explicit WorkspaceRepos %q", infraAbs)
	}
}

// TestRunRebaseLoop_WorkspaceReposMountsFullFeatureContext verifies
// coordinated smart rebase can target a behind subset while mounting all
// feature repos for validation and cross-repo fixes.
func TestRunRebaseLoop_WorkspaceReposMountsFullFeatureContext(t *testing.T) {
	stateDir := t.TempDir()
	store, f, repoPaths := newRebaseTestFeature(t, stateDir, "rebase-workspace-all", []string{"api", "web", "infra"})

	captureFn, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1})

	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
			{RepoName: "web", RebaseTarget: "main"},
		},
		WorkspaceRepos: []string{"api", "web", "infra"},
		MaxIterations:  3,
		RunImplementFn: captureFn,
	}

	result, err := RunRebaseLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}
	if got, want := result.Repos, []string{"api", "web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("result Repos = %v, want stamped behind subset %v", got, want)
	}

	if len(*captured) != 1 {
		t.Fatalf("RunImplementFn called %d times, want 1", len(*captured))
	}
	dirs := (*captured)[0].AdditionalDirs
	for _, repoPath := range repoPaths {
		abs, _ := filepath.Abs(repoPath)
		if !sliceContainsAny(dirs, abs) {
			t.Errorf("AdditionalDirs missing workspace repo %q (got %v)", abs, dirs)
		}
	}
}

func TestRunRebaseLoop_SessionStartFuncRejectsInterruptedFeature(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-session-start-interrupted", []string{"api"})
	captureFn, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1})
	sm := &rebaseLoopSessionManagerStub{}

	cfg := RebaseLoopConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       stateDir,
		BehindRepos:    []RebaseRepoTarget{{RepoName: "api", RebaseTarget: "main"}},
		MaxIterations:  3,
		RunImplementFn: captureFn,
	}

	if _, err := RunRebaseLoop(cfg, sm); err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("RunImplementFn called %d times, want 1", len(*captured))
	}

	startFn := (*captured)[0].SessionStartFunc
	if startFn == nil {
		t.Fatal("SessionStartFunc is nil, want rebase-specific guard")
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusInterrupted
		return nil
	}); err != nil {
		t.Fatalf("mark interrupted: %v", err)
	}

	_, err := startFn("session-id", f.ID, feature.PhaseImplement, nil, t.TempDir(), nil)
	if !errors.Is(err, ports.ErrSessionShuttingDown) {
		t.Fatalf("SessionStartFunc error = %v, want ErrSessionShuttingDown", err)
	}
	if sm.startCalls != 0 {
		t.Fatalf("StartSession calls = %d, want 0 after interrupted guard", sm.startCalls)
	}
}

// TestRunRebaseLoop_PlanLessTestingContractEmitted verifies the loop
// persists a plan-less testing contract whose every item is a baseline
// row tagged `repo: <name>`. No plan-source items; this is the contract
// the implement loop reads to seed each iteration's verification report.
func TestRunRebaseLoop_PlanLessTestingContractEmitted(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-contract", []string{"api", "web"})

	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
			{RepoName: "web", RebaseTarget: "main"},
		},
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil),
	}

	if _, err := RunRebaseLoop(cfg, nil); err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}

	loaded, _ := store.Load(f.ID)
	contractPath := filepath.Join(ActiveRunDir(stateDir, loaded), "rebase-1", "testing-contract.yaml")
	contract, readErr := ReadTestingContract(contractPath)
	if readErr != nil {
		t.Fatalf("read contract: %v", readErr)
	}
	gotPerRepo := map[string]int{}
	for _, item := range contract.Items {
		if item.Source == testingContractPlanSource {
			t.Errorf("plan-source item leaked into plan-less rebase contract: %+v", item)
		}
		gotPerRepo[item.Repo]++
	}
	if gotPerRepo["api"] == 0 || gotPerRepo["web"] == 0 {
		t.Errorf("expected per-repo baseline rows for api+web; got %v", gotPerRepo)
	}
}

// TestRunRebaseLoop_FlatArtifactDirLayout verifies the cycle artifact
// dir flattens — `runs/run-N/rebase-N/iteration-NN/` (no per-repo
// subdir). Asserts via the ImplementConfig.ArtifactDir that the rebase
// loop hands to the inner loop.
func TestRunRebaseLoop_FlatArtifactDirLayout(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-flat", []string{"api", "web"})

	captureFn, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed"})

	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
			{RepoName: "web", RebaseTarget: "main"},
		},
		MaxIterations:  3,
		RunImplementFn: captureFn,
	}

	if _, err := RunRebaseLoop(cfg, nil); err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured = %d, want 1", len(*captured))
	}
	gotDir := (*captured)[0].ArtifactDir
	loaded, _ := store.Load(f.ID)
	wantDir := filepath.Join(ActiveRunDir(stateDir, loaded), "rebase-1")
	if gotDir != wantDir {
		t.Errorf("ArtifactDir = %q, want %q (flat layout, no per-repo subdir)", gotDir, wantDir)
	}
	// Repo subdir should NOT exist under the flat layout.
	for _, repo := range []string{"api", "web"} {
		legacyPath := filepath.Join(wantDir, repo)
		if _, statErr := os.Stat(legacyPath); statErr == nil {
			t.Errorf("legacy per-repo subdir %q exists; flat layout violated", legacyPath)
		}
	}
}

// TestRunRebaseLoop_RebaseCountIncrementsPerInvocation verifies the
// per-invocation increment: starting at 0, two successive invocations
// land at 1 and 2, with artifact dirs `rebase-1` and `rebase-2`.
func TestRunRebaseLoop_RebaseCountIncrementsPerInvocation(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-count", []string{"api"})

	// First invocation.
	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
		},
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "review_passed"}, nil),
	}
	if _, err := RunRebaseLoop(cfg, nil); err != nil {
		t.Fatalf("first RunRebaseLoop: %v", err)
	}
	loaded, _ := store.Load(f.ID)
	if loaded.RebaseCount() != 1 {
		t.Errorf("after first invocation, RebaseCount = %d, want 1", loaded.RebaseCount())
	}
	if _, statErr := os.Stat(filepath.Join(ActiveRunDir(stateDir, loaded), "rebase-1")); statErr != nil {
		t.Errorf("rebase-1 dir missing after first invocation: %v", statErr)
	}

	// Second invocation needs the feature back into a state where rebase
	// can run again — reset the staged repo to CodeReady so it remains
	// behind. (In production, after FR re-promotes the repo to CodeReady
	// the user can launch another rebase.)
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.RepoStates["api"].LastError = ""
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	loaded, _ = store.Load(f.ID)
	cfg.Feature = loaded
	if _, err := RunRebaseLoop(cfg, nil); err != nil {
		t.Fatalf("second RunRebaseLoop: %v", err)
	}
	loaded, _ = store.Load(f.ID)
	if loaded.RebaseCount() != 2 {
		t.Errorf("after second invocation, RebaseCount = %d, want 2", loaded.RebaseCount())
	}
	if _, statErr := os.Stat(filepath.Join(ActiveRunDir(stateDir, loaded), "rebase-2")); statErr != nil {
		t.Errorf("rebase-2 dir missing after second invocation: %v", statErr)
	}
}

// TestRunRebaseLoop_CrashRecoveryReusesArtifactDir verifies mid-iteration
// crash recovery: when the harness pre-creates iteration-01 under the
// flat artifact dir (simulating a prior crashed run), the rebase loop's
// inner ImplementConfig.ArtifactDir points at the same dir so
// ArtifactManager.LatestIteration() picks up where the prior run stopped.
//
// The unit-level test uses a stub RunImplementFn so we can assert the
// directory wiring without driving real session iteration semantics; the
// integration test in TestRunRebaseLoop_Integration_3RepoMixedBehind
// covers the cross-cutting recovery.
func TestRunRebaseLoop_CrashRecoveryReusesArtifactDir(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-recover", []string{"api"})

	// Bump RebaseCount to 1 so the new invocation should advance to 2 —
	// this is the realistic crash case (ActiveCycle.Count=1 from prior
	// crashed run) where the next rebase invocation increments the
	// counter again.
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.SetRebaseCount(1)
		ff.SetActiveCycleType(feature.CycleRebase)
		ff.ActiveCycle = &feature.CycleState{
			Type:   feature.CycleRebase,
			Status: feature.RepoCycleRunning,
			Count:  1,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed prior crash state: %v", err)
	}
	loaded, _ := store.Load(f.ID)

	// Pre-create iteration-01 under the NEXT rebase dir (rebase-2) so we
	// can verify the loop's inner artifact dir resolves to it. This
	// simulates a crash mid-iteration where some prior run already wrote
	// iteration-01.
	priorDir := filepath.Join(ActiveRunDir(stateDir, loaded), "rebase-2", "iteration-01")
	if err := os.MkdirAll(priorDir, 0o755); err != nil {
		t.Fatalf("seed iteration-01: %v", err)
	}

	captureFn, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 2})

	cfg := RebaseLoopConfig{
		Feature:      loaded,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
		},
		MaxIterations:  3,
		RunImplementFn: captureFn,
	}

	if _, err := RunRebaseLoop(cfg, nil); err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}

	if len(*captured) != 1 {
		t.Fatalf("captured calls = %d, want 1", len(*captured))
	}
	gotDir := (*captured)[0].ArtifactDir
	loaded, _ = store.Load(f.ID)
	wantDir := filepath.Join(ActiveRunDir(stateDir, loaded), "rebase-2")
	if gotDir != wantDir {
		t.Errorf("recovered ArtifactDir = %q, want %q (re-uses rebase-2 from prior crash)", gotDir, wantDir)
	}
	// The prior iteration-01 must still be present so
	// ArtifactManager.LatestIteration sees it during recovery.
	if _, err := os.Stat(filepath.Join(wantDir, "iteration-01")); err != nil {
		t.Errorf("iteration-01 missing: %v (recovery would not see prior iteration)", err)
	}
}

func TestRunRebaseLoop_ResumeExistingCycleReusesArtifactDir(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-resume", []string{"api"})

	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.SetRebaseCount(1)
		ff.SetActiveCycleType(feature.CycleRebase)
		ff.ActiveCycle = &feature.CycleState{
			Type:   feature.CycleRebase,
			Status: feature.RepoCycleRunning,
			Count:  1,
		}
		ff.PendingNeedUserInputPath = filepath.Join(stateDir, "gate.yaml")
		return nil
	}); err != nil {
		t.Fatalf("seed paused rebase state: %v", err)
	}
	loaded, _ := store.Load(f.ID)

	priorDir := filepath.Join(ActiveRunDir(stateDir, loaded), "rebase-1", "iteration-01")
	if err := os.MkdirAll(priorDir, 0o755); err != nil {
		t.Fatalf("seed iteration-01: %v", err)
	}

	captureFn, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 2})
	cfg := RebaseLoopConfig{
		Feature:      loaded,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
		},
		MaxIterations:       3,
		ResumeExistingCycle: true,
		RunImplementFn:      captureFn,
	}

	if _, err := RunRebaseLoop(cfg, nil); err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}

	if len(*captured) != 1 {
		t.Fatalf("captured calls = %d, want 1", len(*captured))
	}
	loaded, _ = store.Load(f.ID)
	if loaded.RebaseCount() != 1 {
		t.Fatalf("RebaseCount = %d, want 1", loaded.RebaseCount())
	}
	wantDir := filepath.Join(ActiveRunDir(stateDir, loaded), "rebase-1")
	if got := (*captured)[0].ArtifactDir; got != wantDir {
		t.Fatalf("ArtifactDir = %q, want existing cycle dir %q", got, wantDir)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "iteration-01")); err != nil {
		t.Fatalf("existing iteration-01 missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ActiveRunDir(stateDir, loaded), "rebase-2")); !os.IsNotExist(err) {
		t.Fatalf("rebase-2 exists after resume; expected no new rebase dir (stat err=%v)", err)
	}
}

// TestRunRebaseLoop_ActiveCycleSetAtEntry verifies the cycle entry stamp:
// before the inner loop runs, ActiveCycle is stamped {Type: rebase,
// Status: running} so the TUI and observers can see the active cycle.
// Use a custom RunImplementFn that loads the persisted feature mid-call
// to assert the stamp.
func TestRunRebaseLoop_ActiveCycleSetAtEntry(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-cycle-entry", []string{"api"})

	var midRunCycle *feature.CycleState
	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "api", RebaseTarget: "main"},
		},
		MaxIterations: 3,
		RunImplementFn: func(_ ImplementConfig, _ ports.SessionManager) (*LoopResult, error) {
			loaded, _ := store.Load(f.ID)
			if loaded != nil && loaded.ActiveCycle != nil {
				cp := *loaded.ActiveCycle
				midRunCycle = &cp
			}
			return &LoopResult{FinalStatus: "review_passed"}, nil
		},
	}

	if _, err := RunRebaseLoop(cfg, nil); err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}

	if midRunCycle == nil {
		t.Fatal("ActiveCycle was nil during inner loop run; stamp never landed")
	}
	if midRunCycle.Type != feature.CycleRebase {
		t.Errorf("ActiveCycle.Type = %q, want rebase", midRunCycle.Type)
	}
	if midRunCycle.Status != feature.RepoCycleRunning {
		t.Errorf("ActiveCycle.Status = %q, want running", midRunCycle.Status)
	}
}

// TestRunRebaseLoop_NilFeatureReturnsError covers defensive validation.
func TestRunRebaseLoop_NilFeatureReturnsError(t *testing.T) {
	_, err := RunRebaseLoop(RebaseLoopConfig{}, nil)
	if err == nil {
		t.Fatal("RunRebaseLoop with nil feature returned nil error")
	}
}

// TestRebasePlanMultiRepoFormatting covers the rebase plan composer for
// the multi-repo case: every behind repo gets a per-repo section, the
// repo's rebase target appears, and conflict files surface when present.
func TestRebasePlanMultiRepoFormatting(t *testing.T) {
	plan := BuildMultiRepoRebasePlan([]RebaseRepoTarget{
		{RepoName: "web", RebaseTarget: "master", PRURL: "https://github.com/o/web/pull/1"},
		{
			RepoName:      "api",
			RebaseTarget:  "main",
			PRURL:         "https://github.com/o/api/pull/2",
			ConflictFiles: []string{"internal/api/handler.go", "internal/api/router.go"},
		},
	})
	// Per-repo headings present.
	for _, want := range []string{
		"## Cycle Communication Contract",
		"This coordinated rebase covers the conflicting repos listed below",
		"All\nfeature repos are available in the workspace as validation context",
		"make\ncross-repo edits only when verification proves they are necessary",
		"do not push",
		"The orchestrator runs Final\nReview and applies publish policy after approval",
		"`progress.md`: `{phase_dir}/progress.md`",
		"`verification-report.yaml`: `{iteration_dir}/verification-report.yaml`",
		"`phase_complete`: `{iteration_dir}/phase_complete`",
		"Do not place `progress.md` under `{iteration_dir}`",
		"## Repo: `api`",
		"## Repo: `web`",
		"`api` — base `main`",
		"`web` — base `master`",
		"internal/api/handler.go",
		"internal/api/router.go",
		"rebase already in progress",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan missing %q\n%s", want, plan)
		}
	}
	// Sorted output: api appears before web in the per-repo sections.
	if idxAPI, idxWeb := strings.Index(plan, "## Repo: `api`"), strings.Index(plan, "## Repo: `web`"); idxAPI >= 0 && idxWeb >= 0 && idxAPI > idxWeb {
		t.Errorf("expected api section before web section; api=%d web=%d", idxAPI, idxWeb)
	}
	if strings.Contains(plan, "force-push") {
		t.Fatalf("plan still contains force-push instruction:\n%s", plan)
	}
}

func TestRebaseExitCriteriaForbidsPush(t *testing.T) {
	got := rebaseExitCriteria(nil)
	for _, want := range []string{
		"Every conflicted rebase target is resolved onto its base",
		"feature-level project verification commands pass across all affected repos",
		"Do not push",
		"orchestrator runs Final Review and applies publish policy after approval",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rebaseExitCriteria missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "force-pushed") || strings.Contains(got, "force-push") {
		t.Fatalf("rebaseExitCriteria still contains push language: %q", got)
	}
}

func TestRebaseSkillDocumentsStandardImplementHandoff(t *testing.T) {
	data, err := os.ReadFile(repoRootPath(t, "skills", "rebase", "SKILL.md"))
	if err != nil {
		t.Fatalf("read rebase skill: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"`progress.md`: `{phase_dir}/progress.md`",
		"`verification-report.yaml`: `{iteration_dir}/verification-report.yaml`",
		"`phase_complete`: `{iteration_dir}/phase_complete`",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("skills/rebase/SKILL.md missing %q", want)
		}
	}
	if strings.Contains(content, "at the cycle's iteration artifact dir") {
		t.Fatalf("skills/rebase/SKILL.md still says the handoff is at the iteration artifact dir")
	}
}

func TestRebaseSkillForbidsPush(t *testing.T) {
	data, err := os.ReadFile(repoRootPath(t, "skills", "rebase", "SKILL.md"))
	if err != nil {
		t.Fatalf("read rebase skill: %v", err)
	}
	content := string(data)
	for _, forbidden := range []string{
		"force-push",
		"force-pushed",
		"force-with-lease",
		"git push",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("skills/rebase/SKILL.md still contains push instruction %q", forbidden)
		}
	}
	for _, want := range []string{
		"Do not push",
		"The orchestrator runs Final Review and applies publish policy after approval",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("skills/rebase/SKILL.md missing %q", want)
		}
	}
}

// TestRebasePlanMultiRepoEmpty covers the no-behind-repos degenerate
// composer call.
func TestRebasePlanMultiRepoEmpty(t *testing.T) {
	plan := BuildMultiRepoRebasePlan(nil)
	if !strings.Contains(plan, "No behind repos") {
		t.Errorf("plan missing no-behind marker:\n%s", plan)
	}
}

// TestRebaseRepoNamesDeduplicates verifies the loop deduplicates and
// sorts repo names (used as the AtomicPhaseStamp staged subset).
func TestRebaseRepoNamesDeduplicates(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRebaseTestFeature(t, stateDir, "rebase-dedup", []string{"api", "web"})

	cfg := RebaseLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		BehindRepos: []RebaseRepoTarget{
			{RepoName: "web", RebaseTarget: "main"},
			{RepoName: "api", RebaseTarget: "main"},
			{RepoName: "web", RebaseTarget: "main"}, // duplicate
		},
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "review_passed"}, nil),
	}

	result, err := RunRebaseLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRebaseLoop: %v", err)
	}
	want := []string{"api", "web"}
	got := append([]string(nil), result.Repos...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Repos = %v, want %v (deduplicated and sorted)", got, want)
	}
}

func sliceContainsAny(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
