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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

const testGateFeedback = "Mechanical exit gate violation: the creation-time target is not merged into the child branch."

// countingGate returns a PhaseExitGate that reports feedback for the first
// failFor calls and clean afterwards, plus a pointer to the call count.
func countingGate(failFor int) (func() string, *int) {
	calls := 0
	return func() string {
		calls++
		if calls <= failFor {
			return testGateFeedback
		}
		return ""
	}, &calls
}

// phaseExitGateLoopConfig builds the shared script-driven ImplementConfig the
// gate tests drive RunImplementationLoop with.
func phaseExitGateLoopConfig(t *testing.T, id string, skipReview bool) (ImplementConfig, *[]BuildSessionOpts, *session.Manager) {
	t.Helper()
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", id)
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Working...")+"\n"+
			testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	planPath := filepath.Join(artifactDir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# Plan\nImplement something"), 0o644)

	f := &feature.Feature{
		SchemaVersion: feature.SchemaVersionCurrent,
		ID:            id,
		Name:          "Phase exit gate test",
		Slug:          id,
		Description:   "Phase exit gate test",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: workDir},
		},
	}
	store := feature.NewStore(filepath.Join(tmpDir, "state"))
	_ = store.Save(f)

	buildSession, captured := capturingBuildSession(agentScript, reviewScript)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	t.Cleanup(sm.Shutdown)

	return ImplementConfig{
		Feature:                    f,
		FeatureStore:               store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		MaxIterations:              10,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               "Relevant tests pass",
		Model:                      "opus",
		ReviewModel:                "reviewer",
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
		CommandRunner:              NewExecCommandRunner(),
		SkipIterationReview:        skipReview,
	}, captured, sm
}

// TestRunImplementationLoop_PhaseExitGateFixRoundThenPass drives the
// SkipIterationReview success exit: the gate rejects the first success, the
// loop runs exactly one fix round carrying the gate feedback, and the second
// success (gate clean) completes review_passed.
func TestRunImplementationLoop_PhaseExitGateFixRoundThenPass(t *testing.T) {
	cfg, captured, sm := phaseExitGateLoopConfig(t, "test-gate-fix-001", true)
	gate, calls := countingGate(1)
	cfg.PhaseExitGate = gate

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2 (one fix round)", result.Iterations)
	}
	if *calls != 2 {
		t.Errorf("gate calls = %d, want 2 (once per success exit)", *calls)
	}
	if len(*captured) != 2 {
		t.Fatalf("BuildSession calls = %d, want 2 (implement + fix round)", len(*captured))
	}
	if !strings.Contains((*captured)[1].Prompt, testGateFeedback) {
		t.Errorf("fix round prompt does not carry the gate feedback:\n%s", (*captured)[1].Prompt)
	}
}

// TestRunImplementationLoop_PhaseExitGatePersistentFailureTripsRail proves a
// never-passing gate cannot spin: with an unchanged worktree the no-progress
// rail terminates the loop well before MaxIterations.
func TestRunImplementationLoop_PhaseExitGatePersistentFailureTripsRail(t *testing.T) {
	cfg, _, sm := phaseExitGateLoopConfig(t, "test-gate-rail-001", true)
	cfg.MaxConsecNoProgress = 2
	gate, calls := countingGate(1 << 30)
	cfg.PhaseExitGate = gate

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != "safety_rail" {
		t.Fatalf("FinalStatus = %q, want safety_rail", result.FinalStatus)
	}
	if result.Iterations >= cfg.MaxIterations {
		t.Errorf("Iterations = %d, want rail trip before MaxIterations=%d", result.Iterations, cfg.MaxIterations)
	}
	if *calls != result.Iterations {
		t.Errorf("gate calls = %d, want one per iteration (%d)", *calls, result.Iterations)
	}
}

// TestRunImplementationLoop_PhaseExitGateAfterReviewApproved drives the
// per-iteration-review success exit: the review approves, but the gate
// rejects once, forcing a fix round before the loop completes.
func TestRunImplementationLoop_PhaseExitGateAfterReviewApproved(t *testing.T) {
	cfg, captured, sm := phaseExitGateLoopConfig(t, "test-gate-review-001", false)
	gate, calls := countingGate(1)
	cfg.PhaseExitGate = gate

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2 (one fix round)", result.Iterations)
	}
	if *calls != 2 {
		t.Errorf("gate calls = %d, want 2 (once per approved review)", *calls)
	}
	found := false
	for _, opts := range *captured {
		if strings.Contains(opts.Prompt, testGateFeedback) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no session prompt carries the gate feedback")
	}
}

// TestRunImplementationLoop_PhaseExitGateNilUnchanged verifies a nil gate is
// a no-op: the first success exits review_passed exactly as before.
func TestRunImplementationLoop_PhaseExitGateNilUnchanged(t *testing.T) {
	cfg, captured, sm := phaseExitGateLoopConfig(t, "test-gate-nil-001", true)

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 1 {
		t.Fatalf("Iterations = %d, want 1", result.Iterations)
	}
	if len(*captured) != 1 {
		t.Errorf("BuildSession calls = %d, want 1", len(*captured))
	}
}
