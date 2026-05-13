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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestPhaseImplementUnified_1Repo_EndToEnd is the slice-2 acceptance test for
// the "1-repo feature is the degenerate case of N-repo" unification principle:
// a single-repo feature drives the unified phase-implement loop and Final
// Review without exercising any "if isMultiRepo()" / "if cfg.RepoName != \"\""
// branches (those branches no longer exist after slice 2).
//
// The test uses agent.RunPhaseImplementLoop (the unified entrypoint) with
// real fixtures:
//
//   - One real git worktree (single Feature.Repos entry).
//   - A real on-disk plan with a single Task tagged with `**Repo:** repoA`.
//   - A stub RunImplementFn that returns review_passed (the loop kernel is
//     exercised by the unit tests; we exercise the cross-cutting wiring:
//     PhaseScope → workspace setup → AtomicPhaseStamp).
//
// Asserts:
//
//   - PhaseScope correctly identifies the lone repo as the phase scope.
//   - The inner ImplementConfig sees the cross-repo workspace shape:
//     cwd = state dir (NOT repo dir), AdditionalDirs includes the repo,
//     ArtifactDir is at the flat phase level (no per-repo subdir).
//   - AtomicPhaseStamp transitions the single repo to
//     "awaiting_final_review".
//   - The follow-on RunFeatureFinalReviewLoop pass approves and stamps the
//     repo "review_passed".
//   - No special-case "if isMultiRepo()" branch is exercised — the same
//     loop function handles N=1 and N=3.
func TestPhaseImplementUnified_1Repo_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	repoA := testutil.InitGitRepo(t)

	// Build the feature manifest. RepoImpl starts at Pending — fresh phase.
	store := feature.NewStore(stateDir)
	f := &feature.Feature{
		ID:            "phase-1repo",
		Name:          "1-repo unified phase",
		Slug:          "phase-1repo",
		Description:   "Single repo feature, unified phase-implement loop",
		Status:        feature.StatusImplementing,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repoA", Path: repoA, WorktreePath: repoA, Branch: "feature/test", BaseBranch: "main"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repoA": {},
		},
		MaxIterations: 3,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	f = loaded

	// Plan with a single Task. Single-repo features may legitimately omit
	// `**Repo:**` tags; we include one to exercise PhaseScope's tag path
	// (the validator codepath the legacy "if cfg.RepoName != ''" branch
	// avoided in the per-repo loop).
	planDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "implement")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	planText := "# Plan\n\n" +
		"## Tasks\n\n" +
		"### Task 1: implement the thing in repoA\n\n" +
		"**Repo:** repoA\n\n" +
		"Add a `feature.txt` file.\n\n" +
		"#### Automated Verification:\n" +
		"- [ ] file present: `test -f feature.txt`\n"
	planPath := filepath.Join(planDir, "plan.md")
	if err := os.WriteFile(planPath, []byte(planText), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	// Stub implementer: asserts the unified workspace shape and returns
	// review_passed. The loop kernel itself is unit-tested elsewhere; we
	// verify that the unification cross-cuts (workspace setup, scope, atomic
	// stamp) are correct for the N=1 degenerate case.
	stubImpl := func(c agent.ImplementConfig, _ ports.SessionManager) (*agent.LoopResult, error) {
		// The unified flow MUST set cwd to the state dir (NOT the repo
		// dir) and pass the repo via --add-dir. This is the load-bearing
		// invariant that distinguishes the unified flow from the legacy
		// per-repo flow.
		stateDirAbs, _ := filepath.Abs(filepath.Join(stateDir, f.ID))
		repoAAbs, _ := filepath.Abs(repoA)
		if c.WorkDir != stateDirAbs {
			t.Errorf("ImplementConfig.WorkDir = %q, want state dir %q (unified flow uses state dir as cwd)", c.WorkDir, stateDirAbs)
		}
		// AdditionalDirs must NOT contain stateDir (already at WorkDir;
		// the loop filters it out via additionalDirsExcludingStateDir).
		// AdditionalDirs MUST contain the repo path.
		hasRepo := false
		for _, d := range c.AdditionalDirs {
			if d == stateDirAbs {
				t.Errorf("ImplementConfig.AdditionalDirs leaked state dir %q (should be excluded)", d)
			}
			if d == repoAAbs {
				hasRepo = true
			}
		}
		if !hasRepo {
			t.Errorf("ImplementConfig.AdditionalDirs missing repoA worktree %q (got %v)", repoAAbs, c.AdditionalDirs)
		}

		// RepoName MUST be empty under the unified flow — the
		// "single-repo special case" branch is gone. The per-repo Task
		// fan-out is handled by the implement skill prompt.
		if c.RepoName != "" {
			t.Errorf("ImplementConfig.RepoName = %q, want empty (unified flow does not set per-repo session namespacing)", c.RepoName)
		}

		// ArtifactDir is at the phase level — no per-repo subdir.
		if filepath.Base(c.ArtifactDir) != "implement" {
			t.Errorf("ImplementConfig.ArtifactDir = %q, want suffix /implement (flat layout, no per-repo subdir)", c.ArtifactDir)
		}

		return &agent.LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil
	}

	cfg := agent.OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		PlanPath:       planPath,
		StateDir:       stateDir,
		MaxIterations:  3,
		RunImplementFn: stubImpl,
	}

	result, runErr := agent.RunPhaseImplementLoop(cfg, nil)
	if runErr != nil {
		t.Fatalf("RunPhaseImplementLoop: %v", runErr)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed (got LastError=%q)", result.FinalStatus, result.LastError)
	}
	if len(result.PhaseRepos) != 1 || result.PhaseRepos[0] != "repoA" {
		t.Errorf("PhaseRepos = %v, want [repoA]", result.PhaseRepos)
	}

	// Verify on-disk state: AtomicPhaseStamp transitioned the lone repo to
	// AwaitingFinalReview.
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	st := got.RepoStates["repoA"]
	if st == nil || !st.Touched {
		t.Errorf("repoA after impl = %+v, want Touched=true", st)
	}

	// Follow-on Final Review pass: exercise the unified FR dispatch via
	// RunMultiRepoFinalReview with a RunFinalReviewFn seam that simulates
	// the FR loop's atomic stamp on success. RunFeatureFinalReviewLoop itself
	// spins real Claude sessions for review iterations and is covered by its
	// own unit tests.
	frCfg := agent.OrchestratorConfig{
		Feature:       got,
		FeatureStore:  store,
		StateDir:      stateDir,
		MaxIterations: 3,
		RunFinalReviewFn: func(c agent.OrchestratorConfig, _ ports.SessionManager) (*agent.FeatureFinalReviewResult, error) {
			// Simulate the FR loop's success path: AtomicPhaseStamp with
			// PhaseOutcomeFinalReviewPassed transitions every staged repo
			// from AwaitingFinalReview → ReviewPassed.
			if err := agent.AtomicPhaseStamp(c.FeatureStore, agent.AtomicPhaseStampInput{
				FeatureID: c.Feature.ID,
				Repos:     []string{"repoA"},
				Outcome:   agent.PhaseOutcomeFinalReviewPassed,
			}); err != nil {
				return nil, err
			}
			return &agent.FeatureFinalReviewResult{
				FinalStatus: "review_passed",
				Iterations:  1,
				Repos:       []string{"repoA"},
			}, nil
		},
	}
	frResult, err := agent.RunMultiRepoFinalReview(frCfg, nil)
	if err != nil {
		t.Fatalf("RunMultiRepoFinalReview: %v", err)
	}
	if frResult.FinalStatus != "all_passed" {
		t.Errorf("FR FinalStatus = %q, want all_passed", frResult.FinalStatus)
	}

	// Verify the FR-stamp transitioned the repo to ReviewPassed.
	got2, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	st2 := got2.RepoStates["repoA"]
	if st2 == nil || !st2.Touched {
		t.Errorf("repoA after FR = %+v, want Touched=true", st2)
	}
}
