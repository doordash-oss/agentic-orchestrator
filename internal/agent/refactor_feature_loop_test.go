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
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func newRefactorTestFeature(t *testing.T, stateDir, featureID string, repoNames []string) (*feature.Store, *feature.Feature, []string) {
	t.Helper()
	return newLoopTestFeature(t, stateDir, featureID, repoNames, loopTestFeatureOptions{
		Name:           "Refactor Loop Test",
		Slug:           "refactor-loop-test",
		Description:    "Feature-level refactor cycle test fixture",
		ExitCriteria:   "Refactor complete",
		RefactorPrompt: "extract-shared-config",
	})
}

// stubRefactorPlanFn writes a synthetic refactor-plan.md with the supplied
// content and returns its absolute path. Used to exercise PhaseScope-driven
// staged-subset behavior without launching a real refactor-plan session.
func stubRefactorPlanFn(content string) func(string) (string, error) {
	return func(stagedDir string) (string, error) {
		if err := os.MkdirAll(stagedDir, 0o755); err != nil {
			return "", err
		}
		path := filepath.Join(stagedDir, "refactor-plan.md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return "", err
		}
		return path, nil
	}
}

const refactorPlanCrossRepo = "# Refactor: extract shared config\n" +
	"\n" +
	"## Tasks\n" +
	"\n" +
	"### Task 1: extract config module to repo-a\n" +
	"\n" +
	"**Repo:** api\n" +
	"\n" +
	"Move `Config` struct from internal/server/config.go into a new package `internal/config`.\n" +
	"\n" +
	"#### Automated Verification:\n" +
	"- [ ] api unit tests pass: `cd api && go test ./internal/config/...`\n" +
	"\n" +
	"### Task 2: update web to use the new config package\n" +
	"\n" +
	"**Repo:** web\n" +
	"\n" +
	"Replace local config struct with the shared one from repo-a.\n" +
	"\n" +
	"#### Automated Verification:\n" +
	"- [ ] web tests pass: `cd web && pnpm test`\n"

const refactorPlanSingleRepo = "# Refactor: tighten api error handling\n" +
	"\n" +
	"## Tasks\n" +
	"\n" +
	"### Task 1: introduce sentinel error in api\n" +
	"\n" +
	"**Repo:** api\n" +
	"\n" +
	"Replace string-comparison error checks with a typed sentinel.\n" +
	"\n" +
	"#### Automated Verification:\n" +
	"- [ ] api unit tests pass: `cd api && go test ./...`\n"

// TestRunRefactorFeatureLoop_SuccessAtomicallyStampsStagedRepos covers the
// SUCCESS path: refactor-plan tags two repos, the inner implement loop
// returns review_passed, and AtomicPhaseStamp transitions both staged
// repos to "awaiting_final_review".
func TestRunRefactorFeatureLoop_SuccessAtomicallyStampsStagedRepos(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-success", []string{"api", "web", "infra"})

	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "extract shared config",
		MaxIterations:     3,
		MaxConsecFails:    3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanCrossRepo),
		RunImplementFn:    stubRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil),
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
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
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.Touched == false {
			t.Errorf("repo %s = %+v, want awaiting_final_review", name, st)
		}
	}
	// Repo NOT in the plan-staged subset preserves its prior status.
	if st := loaded.RepoStates["infra"]; st == nil || st.PRURL == "" {
		t.Errorf("infra = %+v, want pr_ready (preserved — outside plan)", st)
	}
	// ActiveCycle cleared on success.
	if loaded.ActiveCycle != nil {
		t.Errorf("ActiveCycle = %+v, want nil after success", loaded.ActiveCycle)
	}
	// RefactorCount incremented.
	if loaded.RefactorCount() != 1 {
		t.Errorf("RefactorCount = %d, want 1", loaded.RefactorCount())
	}
}

// TestRunRefactorFeatureLoop_RetryLandsAfterIteration drives the RETRY
// path: the inner loop returns review_passed after Iterations: 2 (the
// implementer revised based on iter-1 reviewer feedback). The refactor
// loop reports those 2 iterations and stamps success.
func TestRunRefactorFeatureLoop_RetryLandsAfterIteration(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-retry", []string{"api", "web"})

	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "extract shared config",
		MaxIterations:     5,
		MaxConsecFails:    3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanCrossRepo),
		RunImplementFn:    stubRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 2}, nil),
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", result.Iterations)
	}
}

// TestRunRefactorFeatureLoop_MaxIterationsTrip exercises the safety-rail
// trip: inner loop returns max_iterations. The outer loop stamps every
// staged repo "failed"; ActiveCycle.Status flips to "failed".
func TestRunRefactorFeatureLoop_MaxIterationsTrip(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-maxiter", []string{"api", "web"})

	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "extract shared config",
		MaxIterations:     2,
		MaxConsecFails:    5,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanCrossRepo),
		RunImplementFn: stubRunImplementFn(&LoopResult{
			FinalStatus: "max_iterations",
			Iterations:  2,
			LastError:   "Reviewer kept rejecting",
		}, nil),
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}
	if result.FinalStatus != "max_iterations" {
		t.Errorf("FinalStatus = %q, want max_iterations", result.FinalStatus)
	}

	loaded, _ := store.Load(f.ID)
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.LastError == "" {
			t.Errorf("repo %s = %+v, want failed", name, st)
		}
	}
	if loaded.ActiveCycle == nil {
		t.Fatal("ActiveCycle = nil, want non-nil with Status=failed")
	}
	if loaded.ActiveCycle.Status != feature.RepoCycleFailed {
		t.Errorf("ActiveCycle.Status = %q, want failed", loaded.ActiveCycle.Status)
	}
	if loaded.ActiveCycle.Type != feature.CycleRefactor {
		t.Errorf("ActiveCycle.Type = %q, want refactor", loaded.ActiveCycle.Type)
	}
}

// TestRunRefactorFeatureLoop_SafetyRailTrip exercises the consec-fails
// rail: inner loop returns safety_rail. Stamps "failed".
func TestRunRefactorFeatureLoop_SafetyRailTrip(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-safety", []string{"api", "web"})

	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "extract shared config",
		MaxIterations:     10,
		MaxConsecFails:    2,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanCrossRepo),
		RunImplementFn: stubRunImplementFn(&LoopResult{
			FinalStatus: "safety_rail",
			Iterations:  3,
			LastError:   "consecutive-failure rail tripped",
		}, nil),
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}
	if result.FinalStatus != "safety_rail" {
		t.Errorf("FinalStatus = %q, want safety_rail", result.FinalStatus)
	}

	loaded, _ := store.Load(f.ID)
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.LastError == "" {
			t.Errorf("repo %s = %+v, want failed", name, st)
		}
	}
}

// TestRunRefactorFeatureLoop_DispatchErrorStampsFailure verifies that when
// the inner loop dispatcher returns an error, the refactor loop stamps
// "failed" and surfaces the error to the caller.
func TestRunRefactorFeatureLoop_DispatchErrorStampsFailure(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-dispatch-err", []string{"api"})

	dispatchErr := errors.New("session manager: shutting down")
	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "tighten api error handling",
		MaxIterations:     3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanSingleRepo),
		RunImplementFn:    stubRunImplementFn(nil, dispatchErr),
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err == nil {
		t.Fatalf("RunRefactorFeatureLoop returned nil error, want %v", dispatchErr)
	}
	if !errors.Is(err, dispatchErr) {
		t.Errorf("err = %v, want errors.Is %v", err, dispatchErr)
	}
	if result == nil || result.FinalStatus != "failed" {
		t.Errorf("result = %+v, want FinalStatus=failed", result)
	}

	loaded, _ := store.Load(f.ID)
	if st := loaded.RepoStates["api"]; st == nil || st.LastError == "" {
		t.Errorf("api = %+v, want failed", st)
	}
}

// TestRunRefactorFeatureLoop_InterruptedPreservesState verifies that when
// the inner loop returns "interrupted" no atomic stamp is written and
// ActiveCycle stays at running so a restart can resume.
func TestRunRefactorFeatureLoop_InterruptedPreservesState(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-interrupt", []string{"api"})

	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "tighten api error handling",
		MaxIterations:     3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanSingleRepo),
		RunImplementFn:    stubRunImplementFn(&LoopResult{FinalStatus: "interrupted", Iterations: 1}, nil),
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}
	if result.FinalStatus != "interrupted" {
		t.Errorf("FinalStatus = %q, want interrupted", result.FinalStatus)
	}

	loaded, _ := store.Load(f.ID)
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

// TestRunRefactorFeatureLoop_NeedUserInputSurfacesGate covers the gate
// path: the inner loop returns need_user_input with a gate path; the
// outer loop persists the gate and surfaces it on the result.
func TestRunRefactorFeatureLoop_NeedUserInputSurfacesGate(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-niu", []string{"api"})

	gatePath := filepath.Join(stateDir, "refactor-1", "iteration-01", "need-user-input.md")
	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "tighten api error handling",
		MaxIterations:     3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanSingleRepo),
		RunImplementFn: stubRunImplementFn(&LoopResult{
			FinalStatus:       "need_user_input",
			Iterations:        2,
			NeedUserInputPath: gatePath,
		}, nil),
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}
	if result.FinalStatus != "need_user_input" {
		t.Errorf("FinalStatus = %q, want need_user_input", result.FinalStatus)
	}
	if result.NeedUserInputPath != gatePath {
		t.Errorf("NeedUserInputPath = %q, want %q", result.NeedUserInputPath, gatePath)
	}
}

// TestRunRefactorFeatureLoop_FlatArtifactDirLayout verifies the flat
// layout — refactor-N/ (no per-repo subdir) — by inspecting the inner
// ImplementConfig.ArtifactDir.
func TestRunRefactorFeatureLoop_FlatArtifactDirLayout(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-flat", []string{"api", "web"})

	captureFn, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed"})

	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "extract shared config",
		MaxIterations:     3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanCrossRepo),
		RunImplementFn:    captureFn,
	}

	if _, err := RunRefactorFeatureLoop(cfg, nil); err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured = %d, want 1", len(*captured))
	}
	gotDir := (*captured)[0].ArtifactDir
	loaded, _ := store.Load(f.ID)
	wantDir := filepath.Join(ActiveRunDir(stateDir, loaded), "refactor-1")
	if gotDir != wantDir {
		t.Errorf("ArtifactDir = %q, want %q (flat layout, no per-repo subdir)", gotDir, wantDir)
	}
	for _, repo := range []string{"api", "web"} {
		legacyPath := filepath.Join(wantDir, repo)
		if _, statErr := os.Stat(legacyPath); statErr == nil {
			t.Errorf("legacy per-repo subdir %q exists; flat layout violated", legacyPath)
		}
	}
}

// TestRunRefactorFeatureLoop_FullWorkspaceMounted verifies the unified
// workspace mounts every Feature.Repos worktree (cross-repo edits are
// first-class). Asserts via the captured ImplementConfig.AdditionalDirs.
func TestRunRefactorFeatureLoop_FullWorkspaceMounted(t *testing.T) {
	stateDir := t.TempDir()
	store, f, repoPaths := newRefactorTestFeature(t, stateDir, "refactor-workspace", []string{"api", "web", "infra"})

	captureFn, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1})

	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "extract shared config",
		MaxIterations:     3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanCrossRepo),
		RunImplementFn:    captureFn,
	}

	if _, err := RunRefactorFeatureLoop(cfg, nil); err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("RunImplementFn called %d times, want 1", len(*captured))
	}
	dirs := (*captured)[0].AdditionalDirs
	apiAbs, _ := filepath.Abs(repoPaths[0])
	webAbs, _ := filepath.Abs(repoPaths[1])
	infraAbs, _ := filepath.Abs(repoPaths[2])
	for _, want := range []string{apiAbs, webAbs, infraAbs} {
		if !sliceContainsAny(dirs, want) {
			t.Errorf("AdditionalDirs missing worktree %q (got %v) — refactors must mount every Feature.Repos worktree", want, dirs)
		}
	}
}

// TestRunRefactorFeatureLoop_PlannedTestingContractEmitted verifies the
// loop persists a *planned* (not plan-less) testing contract whose items
// include both per-repo baseline rows AND plan-source rows tagged `repo:`.
func TestRunRefactorFeatureLoop_PlannedTestingContractEmitted(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-contract", []string{"api", "web"})

	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "extract shared config",
		MaxIterations:     3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanCrossRepo),
		RunImplementFn:    stubRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil),
	}

	if _, err := RunRefactorFeatureLoop(cfg, nil); err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}

	loaded, _ := store.Load(f.ID)
	contractPath := filepath.Join(ActiveRunDir(stateDir, loaded), "refactor-1", "testing-contract.yaml")
	contract, readErr := ReadTestingContract(contractPath)
	if readErr != nil {
		t.Fatalf("read contract: %v", readErr)
	}

	gotPerRepo := map[string]map[string]int{} // repo → source → count
	for _, item := range contract.Items {
		if gotPerRepo[item.Repo] == nil {
			gotPerRepo[item.Repo] = map[string]int{}
		}
		gotPerRepo[item.Repo][item.Source]++
	}
	for _, repo := range []string{"api", "web"} {
		if gotPerRepo[repo]["baseline"] == 0 {
			t.Errorf("repo %s missing baseline rows; got %v", repo, gotPerRepo[repo])
		}
		if gotPerRepo[repo]["plan"] == 0 {
			t.Errorf("repo %s missing plan-source rows (planned mode should include them); got %v", repo, gotPerRepo[repo])
		}
	}
}

// TestRunRefactorFeatureLoop_CrossRepoTaskDispatch is the headline
// regression for cross-repo dispatch: a plan with one Task tagged Repo: api and
// another tagged Repo: web produces both repos in the staged subset and lands
// cross-repo edits in one iteration.
func TestRunRefactorFeatureLoop_CrossRepoTaskDispatch(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-cross-repo", []string{"api", "web", "shared"})

	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "extract shared config",
		MaxIterations:     3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanCrossRepo),
		RunImplementFn:    stubRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil),
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}

	// The staged subset is exactly the two tagged repos — sorted.
	if got, want := result.Repos, []string{"api", "web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Repos = %v, want %v (cross-repo plan must stage exactly the tagged repos)", got, want)
	}

	// Both tagged repos transition; the third (shared) preserves its
	// prior status.
	loaded, _ := store.Load(f.ID)
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.Touched == false {
			t.Errorf("repo %s = %+v, want awaiting_final_review", name, st)
		}
	}
	if st := loaded.RepoStates["shared"]; st == nil || st.PRURL == "" {
		t.Errorf("shared = %+v, want pr_ready (preserved — outside plan)", st)
	}
}

// TestRunRefactorFeatureLoop_CrashRecoveryReusesArtifactDir verifies
// mid-iteration crash recovery: when the harness pre-creates iteration-01
// under the flat artifact dir (simulating a prior crashed run), the
// refactor loop's inner ImplementConfig.ArtifactDir points at the same
// dir so ArtifactManager.LatestIteration() picks up where the prior run
// stopped.
func TestRunRefactorFeatureLoop_CrashRecoveryReusesArtifactDir(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-recover", []string{"api"})

	// Bump RefactorCount to 1 + stamp ActiveCycle so the new invocation
	// adopts that count rather than starting from scratch. Pre-create
	// iteration-01 under refactor-1 to simulate a prior crashed run.
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.SetRefactorCount(1)
		ff.SetActiveCycleType(feature.CycleRefactor)
		ff.ActiveCycle = &feature.CycleState{
			Type:   feature.CycleRefactor,
			Status: feature.RepoCycleRunning,
			Count:  1,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed prior crash state: %v", err)
	}
	loaded, _ := store.Load(f.ID)

	priorDir := filepath.Join(ActiveRunDir(stateDir, loaded), "refactor-1", "iteration-01")
	if err := os.MkdirAll(priorDir, 0o755); err != nil {
		t.Fatalf("seed iteration-01: %v", err)
	}

	captureFn, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 2})

	cfg := RefactorFeatureLoopConfig{
		Feature:           loaded,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "tighten api error handling",
		MaxIterations:     3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanSingleRepo),
		RunImplementFn:    captureFn,
	}

	if _, err := RunRefactorFeatureLoop(cfg, nil); err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured calls = %d, want 1", len(*captured))
	}
	gotDir := (*captured)[0].ArtifactDir
	loaded, _ = store.Load(f.ID)
	wantDir := filepath.Join(ActiveRunDir(stateDir, loaded), "refactor-1")
	if gotDir != wantDir {
		t.Errorf("recovered ArtifactDir = %q, want %q (re-uses refactor-1 from prior crash)", gotDir, wantDir)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "iteration-01")); err != nil {
		t.Errorf("iteration-01 missing: %v (recovery would not see prior iteration)", err)
	}
}

// TestRunRefactorFeatureLoop_ActiveCycleSetAtEntry verifies the cycle
// entry stamp lands BEFORE the inner loop runs, so the TUI and observers
// see ActiveCycle = {Type: refactor, Status: running} mid-flight.
func TestRunRefactorFeatureLoop_ActiveCycleSetAtEntry(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-cycle-entry", []string{"api"})

	var midRunCycle *feature.CycleState
	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "tighten api error handling",
		MaxIterations:     3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanSingleRepo),
		RunImplementFn: func(_ ImplementConfig, _ ports.SessionManager) (*LoopResult, error) {
			loaded, _ := store.Load(f.ID)
			if loaded != nil && loaded.ActiveCycle != nil {
				cp := *loaded.ActiveCycle
				midRunCycle = &cp
			}
			return &LoopResult{FinalStatus: "review_passed"}, nil
		},
	}

	if _, err := RunRefactorFeatureLoop(cfg, nil); err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}

	if midRunCycle == nil {
		t.Fatal("ActiveCycle was nil during inner loop run; stamp never landed")
	}
	if midRunCycle.Type != feature.CycleRefactor {
		t.Errorf("ActiveCycle.Type = %q, want refactor", midRunCycle.Type)
	}
	if midRunCycle.Status != feature.RepoCycleRunning {
		t.Errorf("ActiveCycle.Status = %q, want running", midRunCycle.Status)
	}
}

// TestRunRefactorFeatureLoop_NilFeatureReturnsError covers defensive
// validation.
func TestRunRefactorFeatureLoop_NilFeatureReturnsError(t *testing.T) {
	_, err := RunRefactorFeatureLoop(RefactorFeatureLoopConfig{}, nil)
	if err == nil {
		t.Fatal("RunRefactorFeatureLoop with nil feature returned nil error")
	}
}

// TestRunRefactorFeatureLoop_EmptyPromptReturnsError covers the case
// where neither cfg.Prompt nor Feature.RefactorPrompt is set.
func TestRunRefactorFeatureLoop_EmptyPromptReturnsError(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-empty-prompt", []string{"api"})
	// Clear the prompt.
	_ = store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.RefactorPrompt = ""
		return nil
	})
	loaded, _ := store.Load(f.ID)

	_, err := RunRefactorFeatureLoop(RefactorFeatureLoopConfig{
		Feature:      loaded,
		FeatureStore: store,
		StateDir:     stateDir,
		// Prompt and feature.RefactorPrompt both empty.
	}, nil)
	if err == nil {
		t.Fatal("RunRefactorFeatureLoop returned nil error for empty prompt")
	}
}

// TestRunRefactorFeatureLoop_PlanStepFailureSurfaces verifies the plan
// step failure path: when RunRefactorPlanFn returns an error, the loop
// surfaces it as FinalStatus=failed and stamps ActiveCycle.Status=failed.
func TestRunRefactorFeatureLoop_PlanStepFailureSurfaces(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-plan-err", []string{"api"})

	planErr := errors.New("refactor-plan session crashed")
	cfg := RefactorFeatureLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		Prompt:       "tighten api error handling",
		RunRefactorPlanFn: func(string) (string, error) {
			return "", planErr
		},
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err == nil {
		t.Fatalf("expected non-nil error from plan failure, got %+v", result)
	}
	if !errors.Is(err, planErr) {
		t.Errorf("err = %v, want errors.Is %v", err, planErr)
	}
	if result.FinalStatus != "failed" {
		t.Errorf("FinalStatus = %q, want failed", result.FinalStatus)
	}

	loaded, _ := store.Load(f.ID)
	if loaded.ActiveCycle == nil || loaded.ActiveCycle.Status != feature.RepoCycleFailed {
		t.Errorf("ActiveCycle = %+v, want non-nil with Status=failed", loaded.ActiveCycle)
	}
}

func TestRunRefactorFeatureLoop_PlanStepProtocolViolationTripsAfterBudget(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-plan-protocol", []string{"api"})
	calls := 0

	cfg := RefactorFeatureLoopConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       stateDir,
		Prompt:         "tighten api error handling",
		MaxConsecFails: 2,
		RunRefactorPlanFn: func(stagedDir string) (string, error) {
			calls++
			return "", newProtocolViolationError(RoleRefactorPlanStep, stagedDir, []ProtocolViolation{{
				Artifact: "refactor-plan.md",
				Reason:   "refactor-plan.md is missing",
			}})
		},
		RunImplementFn: func(ImplementConfig, ports.SessionManager) (*LoopResult, error) {
			t.Fatal("RunImplementFn must not run when refactor plan contract never validates")
			return nil, nil
		},
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRefactorFeatureLoop() error = %v, want nil protocol result", err)
	}
	if calls != 2 {
		t.Fatalf("plan calls = %d, want 2", calls)
	}
	if result.FinalStatus != "protocol_violation" {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "refactor_plan_step") ||
		!strings.Contains(result.LastError, "refactor-plan.md") {
		t.Fatalf("LastError = %q, want refactor plan protocol violation", result.LastError)
	}

	loaded, _ := store.Load(f.ID)
	if loaded.ActiveCycle == nil || loaded.ActiveCycle.Status != feature.RepoCycleFailed {
		t.Fatalf("ActiveCycle = %+v, want failed", loaded.ActiveCycle)
	}
}

func TestRunRefactorFeatureLoop_PlanStepProtocolViolationPreservesPlanBetweenAttempts(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-plan-stale", []string{"api"})
	calls := 0

	cfg := RefactorFeatureLoopConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       stateDir,
		Prompt:         "tighten api error handling",
		MaxConsecFails: 2,
		RunRefactorPlanFn: func(stagedDir string) (string, error) {
			calls++
			planPath := filepath.Join(stagedDir, "refactor-plan.md")
			switch calls {
			case 1:
				if err := os.MkdirAll(stagedDir, 0o755); err != nil {
					return "", err
				}
				if err := os.WriteFile(planPath, []byte(validRefactorPlanText()), 0o644); err != nil {
					return "", err
				}
				return "", newProtocolViolationError(RoleRefactorPlanStep, stagedDir, []ProtocolViolation{{
					Artifact: PhaseCompleteFile,
					Reason:   "SDK reported success but phase_complete was not present",
				}})
			case 2:
				if err := os.WriteFile(filepath.Join(stagedDir, PhaseCompleteFile), nil, 0o644); err != nil {
					return "", err
				}
				outcome, violations, validateErr := Validate(feature.PhasePlan, RoleRefactorPlanStep, stagedDir)
				if validateErr != nil {
					return "", validateErr
				}
				if !outcome.OK {
					return "", newProtocolViolationError(RoleRefactorPlanStep, stagedDir, violations)
				}
				return outcome.PlanMarkdownPath, nil
			default:
				t.Fatalf("unexpected plan attempt %d", calls)
				return "", nil
			}
		},
		RunImplementFn: func(ImplementConfig, ports.SessionManager) (*LoopResult, error) {
			return &LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil
		},
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRefactorFeatureLoop() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("plan calls = %d, want 2", calls)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if _, err := os.Stat(filepath.Join(result.ArtifactDir, "refactor-plan.md")); err != nil {
		t.Fatalf("refactor-plan.md stat error = %v, want preserved between attempts", err)
	}
}

func TestRunRefactorFeatureLoop_PlanStepProtocolViolationCanRecover(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-plan-recover", []string{"api"})
	calls := 0

	cfg := RefactorFeatureLoopConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       stateDir,
		Prompt:         "tighten api error handling",
		MaxConsecFails: 2,
		RunRefactorPlanFn: func(stagedDir string) (string, error) {
			calls++
			if calls == 1 {
				return "", newProtocolViolationError(RoleRefactorPlanStep, stagedDir, []ProtocolViolation{{
					Artifact: "refactor-plan.md",
					Reason:   "refactor-plan.md is missing",
				}})
			}
			return stubRefactorPlanFn(refactorPlanSingleRepo)(stagedDir)
		},
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil),
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRefactorFeatureLoop() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("plan calls = %d, want 2", calls)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
}

func TestRunRefactorPlanStep_ProvisionsExplorationSubagents(t *testing.T) {
	stateDir := t.TempDir()
	artifactDir := filepath.Join(stateDir, "runs", "run-001", "refactor-1")
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-plan-agents", []string{"api"})
	sm := session.NewManager(make(chan interface{}, 10))
	defer sm.Shutdown()

	in := refactorPlanStepTestInput(t, store, f, stateDir, artifactDir, "")
	var captured BuildSessionOpts
	in.BuildSession = func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		captured = opts
		return nil, nil, nil, fmt.Errorf("test: stop after capture")
	}

	if _, err := runRefactorPlanStep(in, sm); err == nil {
		t.Fatal("runRefactorPlanStep() error = nil, want error from capturing BuildSession")
	}
	if !reflect.DeepEqual(captured.AgentNames, explorationAgentNames()) {
		t.Fatalf("refactor-plan AgentNames = %v, want exploration set %v", captured.AgentNames, explorationAgentNames())
	}
}

func TestRunRefactorPlanStep_MissingPlanAfterPhaseCompleteReturnsProtocolViolation(t *testing.T) {
	stateDir := t.TempDir()
	artifactDir := filepath.Join(stateDir, "runs", "run-001", "refactor-1")
	scriptsDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-plan-missing", []string{"api"})

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			testutil.TouchPhaseCompleteInDir(artifactDir)+"\n"+
			testutil.JSONLSuccess+"\n")
	sm := session.NewManager(make(chan interface{}, 10))
	defer sm.Shutdown()

	_, err := runRefactorPlanStep(refactorPlanStepTestInput(t, store, f, stateDir, artifactDir, planScript), sm)
	if err == nil {
		t.Fatal("runRefactorPlanStep() error = nil, want protocol violation")
	}
	if !strings.Contains(err.Error(), "protocol violation: refactor_plan_step @") ||
		!strings.Contains(err.Error(), "refactor-plan.md") {
		t.Fatalf("error = %q, want refactor plan protocol violation", err)
	}
}

func TestRunRefactorPlanStep_MissingPhaseCompleteReturnsProtocolViolation(t *testing.T) {
	stateDir := t.TempDir()
	artifactDir := filepath.Join(stateDir, "runs", "run-001", "refactor-1")
	scriptsDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-plan-no-marker", []string{"api"})
	planPath := filepath.Join(artifactDir, "refactor-plan.md")

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			fmt.Sprintf("mkdir -p %q\ncat > %q <<'PLAN_EOF'\n%s\nPLAN_EOF\n", artifactDir, planPath, validRefactorPlanText())+
			testutil.JSONLSuccess+"\n")
	sm := session.NewManager(make(chan interface{}, 10))
	defer sm.Shutdown()

	_, err := runRefactorPlanStep(refactorPlanStepTestInput(t, store, f, stateDir, artifactDir, planScript), sm)
	if err == nil {
		t.Fatal("runRefactorPlanStep() error = nil, want protocol violation")
	}
	if !strings.Contains(err.Error(), "protocol violation: refactor_plan_step @") ||
		!strings.Contains(err.Error(), "phase_complete") {
		t.Fatalf("error = %q, want missing phase_complete protocol violation", err)
	}
}

func TestRunRefactorPlanStep_StalePlanWithFreshMarkerReturnsPlan(t *testing.T) {
	stateDir := t.TempDir()
	artifactDir := filepath.Join(stateDir, "runs", "run-001", "refactor-1")
	scriptsDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-plan-stale-marker", []string{"api"})
	stalePlanPath := filepath.Join(artifactDir, "refactor-plan.md")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := os.WriteFile(stalePlanPath, []byte(validRefactorPlanText()), 0o644); err != nil {
		t.Fatalf("write stale refactor plan: %v", err)
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			testutil.TouchPhaseCompleteInDir(artifactDir)+"\n"+
			testutil.JSONLSuccess+"\n")
	sm := session.NewManager(make(chan interface{}, 10))
	defer sm.Shutdown()

	got, err := runRefactorPlanStep(refactorPlanStepTestInput(t, store, f, stateDir, artifactDir, planScript), sm)
	if err != nil {
		t.Fatalf("runRefactorPlanStep() error = %v", err)
	}
	if got != stalePlanPath {
		t.Fatalf("plan path = %q, want %q", got, stalePlanPath)
	}
	if _, statErr := os.Stat(stalePlanPath); statErr != nil {
		t.Fatalf("stale refactor-plan.md stat error = %v, want preserved", statErr)
	}
}

// TestRunRefactorPlanStep_FinishOrViolateNudgeRecoversSameSession proves the
// refactor-plan step recovers via the finish-or-violate nudge: the first turn
// ends without refactor-plan.md + phase_complete, the harness nudges the same
// live session, and the nudged turn writes both so the step returns the plan
// path without a protocol violation.
func TestRunRefactorPlanStep_FinishOrViolateNudgeRecoversSameSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	stateDir := t.TempDir()
	artifactDir := filepath.Join(stateDir, "runs", "run-001", "refactor-1")
	scriptsDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-plan-nudge", []string{"api"})
	planPath := filepath.Join(artifactDir, "refactor-plan.md")

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh", fmt.Sprintf(`%s
echo '{"type":"result","subtype":"success","session_id":"mock","total_cost_usd":0.001,"stop_reason":"end_turn"}'
while IFS= read -r _line; do
  case "$_line" in
    %s)
      mkdir -p %q
      cat > %q <<'PLAN_EOF'
%s
PLAN_EOF
      %s
      echo '{"type":"result","subtype":"success","session_id":"mock","total_cost_usd":0.001,"stop_reason":"end_turn"}'
      exit 0
      ;;
  esac
done
`, testutil.JSONLInit, finishOrViolateNudgeCasePattern, artifactDir, planPath, validRefactorPlanText(), testutil.TouchPhaseCompleteInDir(artifactDir)))

	sm := session.NewManager(make(chan interface{}, 10))
	defer sm.Shutdown()

	in := refactorPlanStepTestInput(t, store, f, stateDir, artifactDir, planScript)
	in.FinishOrViolateNudge = true

	got, err := runRefactorPlanStep(in, sm)
	if err != nil {
		t.Fatalf("runRefactorPlanStep() error = %v", err)
	}
	if got != planPath {
		t.Fatalf("plan path = %q, want %q", got, planPath)
	}
}

func refactorPlanStepTestInput(t *testing.T, store ports.FeatureStore, f *feature.Feature, stateDir, artifactDir, script string) refactorPlanStepInput {
	t.Helper()
	repoPaths := map[string]string{}
	for _, repo := range f.Repos {
		repoPaths[repo.Name] = repo.Path
	}
	return refactorPlanStepInput{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		ArtifactDir:  artifactDir,
		Workspace: WorkspaceSetup{
			Cwd:       filepath.Join(stateDir, f.ID),
			RepoPaths: repoPaths,
		},
		Prompt:         "tighten api error handling",
		PlanningModel:  "planner",
		ImplementModel: "agent",
		BuildSession:   mockBuildSession(script, ""),
	}
}

// TestRunRefactorFeatureLoop_RefactorCountIncrementsPerInvocation
// verifies the per-invocation increment: starting at 0, two successive
// invocations land at 1 and 2 with artifact dirs `refactor-1` and
// `refactor-2`.
func TestRunRefactorFeatureLoop_RefactorCountIncrementsPerInvocation(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-count", []string{"api"})

	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "tighten api error handling",
		MaxIterations:     3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanSingleRepo),
		RunImplementFn:    stubRunImplementFn(&LoopResult{FinalStatus: "review_passed"}, nil),
	}
	if _, err := RunRefactorFeatureLoop(cfg, nil); err != nil {
		t.Fatalf("first invocation: %v", err)
	}
	loaded, _ := store.Load(f.ID)
	if loaded.RefactorCount() != 1 {
		t.Errorf("after first invocation, RefactorCount = %d, want 1", loaded.RefactorCount())
	}
	if _, statErr := os.Stat(filepath.Join(ActiveRunDir(stateDir, loaded), "refactor-1")); statErr != nil {
		t.Errorf("refactor-1 dir missing after first invocation: %v", statErr)
	}

	// Reset for second invocation.
	_ = store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.RepoStates["api"].LastError = ""
		ff.RefactorPrompt = "tighten api error handling"
		// Bump count to simulate the orchestrator-level pre-bump.
		ff.SetRefactorCount(2)
		return nil
	})
	loaded, _ = store.Load(f.ID)
	cfg.Feature = loaded
	if _, err := RunRefactorFeatureLoop(cfg, nil); err != nil {
		t.Fatalf("second invocation: %v", err)
	}
	loaded, _ = store.Load(f.ID)
	if loaded.RefactorCount() != 2 {
		t.Errorf("after second invocation, RefactorCount = %d, want 2", loaded.RefactorCount())
	}
	if _, statErr := os.Stat(filepath.Join(ActiveRunDir(stateDir, loaded), "refactor-2")); statErr != nil {
		t.Errorf("refactor-2 dir missing after second invocation: %v", statErr)
	}
}

// TestRunRefactorFeatureLoop_PlanScopedRepoNamesAreSorted exercises the
// staged-subset ordering invariant: regardless of plan-Task ordering or
// duplicate tags, the loop result's Repos slice is deduplicated and
// sorted (so the AtomicPhaseStamp staged subset is deterministic).
func TestRunRefactorFeatureLoop_PlanScopedRepoNamesAreSorted(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-sorted", []string{"alpha", "bravo", "charlie"})

	plan := `# Refactor

## Tasks

### Task 1

**Repo:** charlie

Touch charlie.

### Task 2

**Repo:** alpha

Touch alpha.

### Task 3

**Repo:** charlie

Also touch charlie (duplicate tag).
`
	cfg := RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "shuffle",
		MaxIterations:     3,
		RunRefactorPlanFn: stubRefactorPlanFn(plan),
		RunImplementFn:    stubRunImplementFn(&LoopResult{FinalStatus: "review_passed"}, nil),
	}

	result, err := RunRefactorFeatureLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}
	got := append([]string(nil), result.Repos...)
	sort.Strings(got) // double-belt: ensure already sorted
	want := []string{"alpha", "charlie"}
	if !reflect.DeepEqual(result.Repos, want) {
		t.Errorf("Repos = %v, want %v (deduplicated + sorted)", result.Repos, want)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("post-sort = %v (already-sorted invariant violated)", got)
	}
}

// TestBuildRefactorPlanPromptLeavesOutputRulesToSkill verifies the
// refactor-plan prompt carries only invocation arguments. Output format
// and completion protocol live in the RoleSpec system prompt and refactor
// SKILL.md.
func TestBuildRefactorPlanPromptLeavesOutputRulesToSkill(t *testing.T) {
	f := &feature.Feature{
		ID:          "test",
		Name:        "Test",
		Slug:        "test",
		Description: "test desc",
		Repos: []feature.FeatureRepo{
			{Name: "api", Path: "/tmp/api"},
			{Name: "web", Path: "/tmp/web"},
		},
	}
	ws := WorkspaceSetup{
		Cwd: "/tmp/state",
		RepoPaths: map[string]string{
			"api": "/tmp/api",
			"web": "/tmp/web",
		},
	}
	prompt := buildRefactorPlanPrompt(f, ws, "extract config", "/skills", "/guidelines", nil)
	for _, want := range []string{
		"## Refactor Request",
		"extract config",
		"## Feature Context",
		"test desc",
		"## Workspace",
		"`api`",
		"`web`",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{
		"**Repo:** <name>",
		"refactor-plan.md",
		"phase_complete",
		"/skills/refactor/SKILL.md",
		"/guidelines",
		"# Useful Resources",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("prompt contains RoleSpec/SKILL-owned content %q\n%s", forbidden, prompt)
		}
	}
}

// TestRefactorFeature_PromptStashedAtCycleEntry ensures the loop
// persists the user-supplied prompt onto the feature so a crashed/
// interrupted run can be resumed by simply re-launching against the
// same feature record. Asserts via an explicit Load after entry.
func TestRefactorFeature_PromptStashedAtCycleEntry(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newRefactorTestFeature(t, stateDir, "refactor-stash", []string{"api"})
	// Clear any pre-existing prompt to prove the loop stashes it.
	_ = store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.RefactorPrompt = ""
		return nil
	})
	loaded, _ := store.Load(f.ID)

	prompt := "introduce typed errors"
	cfg := RefactorFeatureLoopConfig{
		Feature:           loaded,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            prompt,
		MaxIterations:     3,
		RunRefactorPlanFn: stubRefactorPlanFn(refactorPlanSingleRepo),
		RunImplementFn: func(_ ImplementConfig, _ ports.SessionManager) (*LoopResult, error) {
			midRun, _ := store.Load(f.ID)
			if midRun.RefactorPrompt != prompt {
				t.Errorf("mid-run RefactorPrompt = %q, want %q", midRun.RefactorPrompt, prompt)
			}
			return &LoopResult{FinalStatus: "review_passed"}, nil
		},
	}

	if _, err := RunRefactorFeatureLoop(cfg, nil); err != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", err)
	}
}
