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

// TestPhaseImplementUnified_NeedUserInputMidPhaseResume is the slice-2
// acceptance test for the feature-scoped NEED_USER_INPUT gate:
//
//   - Phase iteration 2 emits a need_user_input handoff. The loop's
//     AtomicPhaseStamp(PhaseOutcomeNeedUserInput) records the gate path
//     on the feature (Feature.PendingNeedUserInputPath) WITHOUT mutating
//     per-repo status — under the unified flow the gate is feature-scoped.
//   - The user answers the gate by clearing Feature.PendingNeedUserInputPath
//     (the orchestrator's resume path does this after the user responds).
//   - The phase resumes from iteration 3 (the inner loop's
//     ArtifactManager.LatestIteration() drives the resume cursor; here we
//     simulate that effect via a stub) and lands on review_passed.
//   - AtomicPhaseStamp on the resume's success transitions every declared
//     repo to "awaiting_final_review" atomically.
//
// The test exercises the cross-cutting wiring: the gate path round-trips
// through Feature.PendingNeedUserInputPath, per-repo state is preserved
// across the pause, and the resume's atomic stamp lands.
func TestPhaseImplementUnified_NeedUserInputMidPhaseResume(t *testing.T) {
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

	store := feature.NewStore(stateDir)
	f := &feature.Feature{
		ID:            "phase-nui",
		Name:          "Phase NEED_USER_INPUT mid-phase resume",
		Slug:          "phase-nui",
		Description:   "Phase pauses at iteration 2, resumes at iteration 3",
		Status:        feature.StatusImplementing,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: repoA, WorktreePath: repoA, Branch: "feature/test", BaseBranch: "main"},
			{Name: "repo-b", Path: repoB, WorktreePath: repoB, Branch: "feature/test", BaseBranch: "main"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {},
			"repo-b": {},
		},
		MaxIterations: 5,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	f = loaded

	planDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "implement")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	planText := "# Plan\n\n" +
		"## Tasks\n\n" +
		"### Task 1: A\n\n" +
		"**Repo:** repo-a\n\n" +
		"#### Automated Verification:\n" +
		"- [ ] a tests: `go test ./...`\n\n" +
		"### Task 2: B\n\n" +
		"**Repo:** repo-b\n\n" +
		"#### Automated Verification:\n" +
		"- [ ] b tests: `go test ./...`\n"
	planPath := filepath.Join(planDir, "plan.md")
	if err := os.WriteFile(planPath, []byte(planText), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	// The gate path the inner loop "wrote" when it emitted
	// need_user_input. Real production layout puts this at
	// runs/run-NNN/implement/iteration-02/need-user-input.yaml; we
	// synthesize a real file at that path so the gate exists on disk
	// for the test's verification.
	iterDir := filepath.Join(planDir, "iteration-02")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatalf("mkdir iter: %v", err)
	}
	gatePath := filepath.Join(iterDir, "need-user-input.yaml")
	if err := os.WriteFile(gatePath, []byte("question: clarify the API contract\n"), 0o644); err != nil {
		t.Fatalf("write gate: %v", err)
	}

	// Stub state machine: invocation 1 → need_user_input @ iter 2;
	// invocation 2 → review_passed @ iter 3 (simulating the resume from
	// iteration 3, which the inner loop's ArtifactManager.LatestIteration
	// scaffolding handles in production).
	implCalls := 0
	stubImpl := func(_ agent.ImplementConfig, _ ports.SessionManager) (*agent.LoopResult, error) {
		implCalls++
		if implCalls == 1 {
			return &agent.LoopResult{
				FinalStatus:       "need_user_input",
				Iterations:        2,
				NeedUserInputPath: gatePath,
			}, nil
		}
		return &agent.LoopResult{FinalStatus: "review_passed", Iterations: 3}, nil
	}

	cfg := agent.OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		PlanPath:       planPath,
		StateDir:       stateDir,
		MaxIterations:  5,
		RunImplementFn: stubImpl,
	}

	// --- First invocation: phase pauses at iteration 2. ---
	result1, runErr := agent.RunPhaseImplementLoop(cfg, nil)
	if runErr != nil {
		t.Fatalf("RunPhaseImplementLoop (first): %v", runErr)
	}
	if result1.FinalStatus != "need_user_input" {
		t.Fatalf("first FinalStatus = %q, want need_user_input", result1.FinalStatus)
	}
	if result1.NeedUserInputPath != gatePath {
		t.Errorf("first NeedUserInputPath = %q, want %q", result1.NeedUserInputPath, gatePath)
	}
	if result1.Iterations != 2 {
		t.Errorf("first Iterations = %d, want 2", result1.Iterations)
	}

	// AtomicPhaseStamp(PhaseOutcomeNeedUserInput) recorded the gate path
	// on the feature WITHOUT mutating per-repo status (the gate is
	// feature-scoped under the unified flow).
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load after first: %v", err)
	}
	if got.PendingNeedUserInputPath != gatePath {
		t.Errorf("Feature.PendingNeedUserInputPath = %q, want %q", got.PendingNeedUserInputPath, gatePath)
	}
	for _, name := range []string{"repo-a", "repo-b"} {
		st := got.RepoStates[name]
		if st != nil && (st.Touched || st.Error != nil) {
			t.Errorf("repo %q after pause = %+v, want untouched (per-repo state untouched on need_user_input)", name, st)
		}
	}
	if _, err := os.Stat(gatePath); err != nil {
		t.Errorf("gate file missing on disk: %v", err)
	}

	// --- User answers the gate. The orchestrator's resume path clears
	// Feature.PendingNeedUserInputPath after the user responds; the gate
	// file itself can also be removed (the loop reads the user's answer
	// from a sibling artifact in production). Mirror that here. ---
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.PendingNeedUserInputPath = ""
		return nil
	}); err != nil {
		t.Fatalf("clear gate: %v", err)
	}
	if err := os.Remove(gatePath); err != nil {
		t.Fatalf("remove gate file: %v", err)
	}

	// --- Second invocation: phase resumes at iteration 3 and lands on
	// review_passed. ---
	got, err = store.Load(f.ID)
	if err != nil {
		t.Fatalf("reload after answer: %v", err)
	}
	cfg.Feature = got
	result2, runErr := agent.RunPhaseImplementLoop(cfg, nil)
	if runErr != nil {
		t.Fatalf("RunPhaseImplementLoop (second): %v", runErr)
	}
	if result2.FinalStatus != "review_passed" {
		t.Fatalf("second FinalStatus = %q, want review_passed (LastError=%q)", result2.FinalStatus, result2.LastError)
	}
	if result2.Iterations != 3 {
		t.Errorf("second Iterations = %d, want 3 (resumed from iteration 3, not restart at 1)", result2.Iterations)
	}
	if implCalls != 2 {
		t.Errorf("RunImplementFn call count = %d, want 2", implCalls)
	}

	// Resume success: AtomicPhaseStamp(PhaseOutcomeReviewPassed)
	// transitioned both repos atomically to AwaitingFinalReview.
	got2, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load after second: %v", err)
	}
	for _, name := range []string{"repo-a", "repo-b"} {
		st := got2.RepoStates[name]
		if st == nil || st.Touched == false {
			t.Errorf("after resume, repo %q = %+v, want awaiting_final_review", name, st)
		}
	}
	// Gate path should remain cleared after the successful resume.
	if got2.PendingNeedUserInputPath != "" {
		t.Errorf("PendingNeedUserInputPath = %q, want cleared after resume success", got2.PendingNeedUserInputPath)
	}
}
