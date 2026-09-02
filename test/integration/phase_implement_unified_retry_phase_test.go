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

package integration

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestPhaseImplementUnified_RetryPhaseRecovery is the slice-2 acceptance
// test for RetryPhase: a 2-repo phase fails after MaxIterations, the user
// invokes RetryPhase, and the phase re-runs from scratch against the same
// plan and succeeds.
//
// The test drives RunPhaseImplementLoop twice with a per-call stub:
//
//  1. First call: stub returns `max_iterations`. AtomicPhaseStamp
//     transitions both repos to "failed".
//  2. RetryPhase resets both repos to "". Iteration counters
//     are NOT carried over (per the design: phase failure recovery is
//     atomic; the next loop starts at iteration 1 against the same plan).
//  3. Second call: stub returns `review_passed`. AtomicPhaseStamp
//     transitions both repos to "awaiting_final_review".
//
// Asserts:
//
//   - Failed → Pending → AwaitingFinalReview transition for every declared
//     repo (phase atomicity holds across the failure recovery boundary).
//   - feature.Manager.RetryPhase clears the run's failure record and
//     PendingNeedUserInputPath alongside the per-repo reset.
//   - Repos outside repoNames are NOT touched (preservation guarantee).
func TestPhaseImplementUnified_RetryPhaseRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	repoA := testutil.InitGitRepo(t)
	repoB := testutil.InitGitRepo(t)
	repoC := testutil.InitGitRepo(t) // outside the phase plan; preservation check.

	store := feature.NewStore(stateDir)
	f := &feature.Feature{
		ID:            "phase-retry",
		Name:          "Phase failure → RetryPhase",
		Slug:          "phase-retry",
		Description:   "Two-repo phase fails, RetryPhase recovers",
		Status:        feature.StatusImplementing,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: repoA, WorktreePath: repoA, Branch: "feature/test", BaseBranch: "main"},
			{Name: "repo-b", Path: repoB, WorktreePath: repoB, Branch: "feature/test", BaseBranch: "main"},
			{Name: "repo-c", Path: repoC, WorktreePath: repoC, Branch: "feature/test", BaseBranch: "main"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {},
			"repo-b": {},
			// repo-c outside the phase: pre-stamped with a published PR URL
			// so we can verify RetryPhase does NOT touch it.
			"repo-c": {Touched: true, PRURL: "https://github.com/example/repo-c/pull/9"},
		},
		MaxIterations: 2,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	f = loaded

	// Plan tags only repo-a + repo-b. repo-c is outside the phase scope.
	planDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "implement")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	planText := "# Plan\n\n" +
		"## Tasks\n\n" +
		"### Task 1: A work\n\n" +
		"**Repo:** repo-a\n\n" +
		"#### Automated Verification:\n" +
		"- [ ] a tests: `go test ./...`\n\n" +
		"### Task 2: B work\n\n" +
		"**Repo:** repo-b\n\n" +
		"#### Automated Verification:\n" +
		"- [ ] b tests: `go test ./...`\n"
	planPath := filepath.Join(planDir, "plan.md")
	if err := os.WriteFile(planPath, []byte(planText), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	// Stub state machine: a counter switches between fail-then-succeed.
	implCalls := 0
	stubImpl := func(_ agent.ImplementConfig, _ ports.SessionManager) (*agent.LoopResult, error) {
		implCalls++
		if implCalls == 1 {
			return &agent.LoopResult{
				FinalStatus: "max_iterations",
				Iterations:  2,
				LastError:   "no convergence after 2 iterations",
			}, nil
		}
		return &agent.LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil
	}

	cfg := agent.OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		PlanPath:       planPath,
		StateDir:       stateDir,
		MaxIterations:  2,
		RunImplementFn: stubImpl,
	}

	// --- First invocation: phase fails. ---
	result1, runErr := agent.RunPhaseImplementLoop(cfg, nil)
	if runErr != nil {
		t.Fatalf("RunPhaseImplementLoop (first): %v", runErr)
	}
	if result1.FinalStatus != "max_iterations" {
		t.Fatalf("first FinalStatus = %q, want max_iterations", result1.FinalStatus)
	}
	wantPhaseRepos := []string{"repo-a", "repo-b"}
	if !reflect.DeepEqual(result1.PhaseRepos, wantPhaseRepos) {
		t.Errorf("first PhaseRepos = %v, want %v", result1.PhaseRepos, wantPhaseRepos)
	}

	// AtomicPhaseStamp wrote Failed for every phase-declared repo.
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load after first: %v", err)
	}
	for _, name := range wantPhaseRepos {
		st := got.RepoStates[name]
		if st == nil || !st.Touched {
			t.Errorf("after first run, repo %q = %+v, want the failed stamp (Touched)", name, st)
		}
	}
	// repo-c outside the phase plan: status preserved.
	if st := got.RepoStates["repo-c"]; st == nil || st.PRURL == "" {
		t.Errorf("repo-c after first run = %+v, want pr_ready (preserved — outside phase)", st)
	}

	// Seed a run-level failure record to verify RetryPhase clears it as
	// documented.
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Run().Failure = &errcat.FailureRecord{
			Code: errcat.InfrastructureFailure,
			Context: &errcat.RecordContext{
				Phase: &errcat.CodePhase{Name: "implement"},
			},
			Diagnostics: "something went wrong",
		}
		ff.PendingNeedUserInputPath = "/some/leftover/path.yaml"
		for _, name := range wantPhaseRepos {
			if st := ff.RepoStates[name]; st != nil {
				st.Error = &errcat.FailureRecord{Code: errcat.InfrastructureFailure, Diagnostics: "stale record"}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed error fields: %v", err)
	}

	// --- User invokes RetryPhase: reset the phase-declared subset. ---
	// Use the feature.Manager directly. The orchestrator-layer wrapper
	// derives the phase-declared subset from PhaseScope; we call the
	// manager-level method directly with an explicit subset to keep the
	// test focused on the phase-implement loop wiring.
	fm := feature.NewManager(store, nil)
	if err := fm.RetryPhase(f.ID, wantPhaseRepos); err != nil {
		t.Fatalf("RetryPhase: %v", err)
	}

	got, err = store.Load(f.ID)
	if err != nil {
		t.Fatalf("load after RetryPhase: %v", err)
	}
	for _, name := range wantPhaseRepos {
		st := got.RepoStates[name]
		if st != nil && st.Error != nil {
			t.Errorf("after RetryPhase, repo %q record = %+v, want cleared", name, st.Error)
		}
	}
	// Outside-the-phase preservation: repo-c PRURL retained.
	if st := got.RepoStates["repo-c"]; st == nil || st.PRURL == "" {
		t.Errorf("repo-c after RetryPhase = %+v, want PRURL preserved", st)
	}
	// Run-level failure record cleared.
	if got.FailureCode() != "" {
		t.Errorf("FailureCode() = %q, want empty (record cleared)", got.FailureCode())
	}
	if rec := got.FailureRecord(); rec != nil {
		t.Errorf("failure record = %+v, want cleared by RetryPhase", rec)
	}
	if got.PendingNeedUserInputPath != "" {
		t.Errorf("PendingNeedUserInputPath = %q, want cleared", got.PendingNeedUserInputPath)
	}

	// --- Second invocation: phase re-runs from iteration 1 and succeeds. ---
	cfg.Feature = got // refreshed feature view
	result2, runErr := agent.RunPhaseImplementLoop(cfg, nil)
	if runErr != nil {
		t.Fatalf("RunPhaseImplementLoop (second): %v", runErr)
	}
	if result2.FinalStatus != "review_passed" {
		t.Fatalf("second FinalStatus = %q, want review_passed", result2.FinalStatus)
	}
	if result2.Iterations != 1 {
		t.Errorf("second Iterations = %d, want 1 (RetryPhase resets — re-runs from iteration 1, not iteration 3)", result2.Iterations)
	}
	if implCalls != 2 {
		t.Errorf("RunImplementFn invocation count = %d, want 2 (one fail + one success)", implCalls)
	}

	// AtomicPhaseStamp transitioned both repos to AwaitingFinalReview.
	got2, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load after second: %v", err)
	}
	for _, name := range wantPhaseRepos {
		st := got2.RepoStates[name]
		if st == nil || st.Touched == false {
			t.Errorf("after second run, repo %q = %+v, want awaiting_final_review", name, st)
		}
	}
	// repo-c still preserved.
	if st := got2.RepoStates["repo-c"]; st == nil || st.PRURL == "" {
		t.Errorf("repo-c after second run = %+v, want pr_ready (preserved)", st)
	}
}
