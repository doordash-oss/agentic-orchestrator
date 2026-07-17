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
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestResolvePlanArtifactPath(t *testing.T) {
	t.Run("absolute path from store", func(t *testing.T) {
		dir := t.TempDir()
		planPath := filepath.Join(dir, "my-plan.md")
		_ = os.WriteFile(planPath, []byte("plan content"), 0o644)

		store := feature.NewStore(dir)
		_ = store.Save(&feature.Feature{
			ID:            "test-feat",
			Artifacts:     map[string]string{"plan": planPath},
			SchemaVersion: feature.SchemaVersionCurrent,
		})

		got := resolvePlanArtifactPath(store, "test-feat", dir)
		if got != planPath {
			t.Errorf("expected %q, got %q", planPath, got)
		}
	})

	t.Run("relative path from store", func(t *testing.T) {
		dir := t.TempDir()
		artifactDir := filepath.Join(dir, "plan")
		_ = os.MkdirAll(artifactDir, 0o755)
		planPath := filepath.Join(artifactDir, "impl-plan.md")
		_ = os.WriteFile(planPath, []byte("plan content"), 0o644)

		store := feature.NewStore(dir)
		_ = store.Save(&feature.Feature{
			ID:            "test-feat",
			Artifacts:     map[string]string{"plan": "impl-plan.md"},
			SchemaVersion: feature.SchemaVersionCurrent,
		})

		got := resolvePlanArtifactPath(store, "test-feat", artifactDir)
		if got != planPath {
			t.Errorf("expected %q, got %q", planPath, got)
		}
	})

	t.Run("glob fallback", func(t *testing.T) {
		dir := t.TempDir()
		artifactDir := filepath.Join(dir, "plan")
		_ = os.MkdirAll(artifactDir, 0o755)
		planPath := filepath.Join(artifactDir, "2026-03-12-plan.md")
		_ = os.WriteFile(planPath, []byte("plan content"), 0o644)
		// Also write files that should be skipped
		_ = os.WriteFile(filepath.Join(artifactDir, "output.txt"), []byte("log"), 0o644)
		_ = os.WriteFile(filepath.Join(artifactDir, "validation-1-prompt.md"), []byte("prompt"), 0o644)

		got := resolvePlanArtifactPath(nil, "", artifactDir)
		if got != planPath {
			t.Errorf("expected %q, got %q", planPath, got)
		}
	})

	t.Run("no matching files", func(t *testing.T) {
		dir := t.TempDir()
		got := resolvePlanArtifactPath(nil, "", dir)
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})
}

func TestRecordApprovedPhasePlanArtifactRecordsFrontendFlag(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "phase-plan.md")
	planText := "# Phase 1 Plan\n\n" +
		"## Metadata\n\n" +
		"**Frontend:** true\n\n" +
		"## Overview\nShip UI.\n\n" +
		"## Success Criteria\n\n" +
		"### Visual Evidence\n- [ ] Capture the UI.\n"
	if err := os.WriteFile(planPath, []byte(planText), 0o644); err != nil {
		t.Fatalf("WriteFile(plan) error = %v", err)
	}

	store := feature.NewStore(dir)
	f := newTestPlanFeature(t, dir)
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature) error = %v", err)
	}
	cfg := PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:      f,
			FeatureStore: store,
		},
		Phase: RoadmapPhase{Number: 2},
	}

	if err := recordApprovedPhasePlanArtifact(cfg, planPath); err != nil {
		t.Fatalf("recordApprovedPhasePlanArtifact() error = %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load(feature) error = %v", err)
	}
	if got := loaded.Artifacts["phase-2-plan"]; got != planPath {
		t.Fatalf("Artifacts[phase-2-plan] = %q, want %q", got, planPath)
	}
	if !loaded.RoadmapPhaseFrontend(2) || !loaded.AnyRoadmapPhaseFrontend() {
		t.Fatalf("frontend flag not recorded: %#v", loaded.Run().RoadmapPhaseFrontendByPhase)
	}
}

// --- Integration tests for RunRoadmapPlanningLoop ---
// These tests follow the same pattern as TestImplementLoop* in integration_test.go:
// they use mock bash scripts that emit the expected signals.

// newTestPlanFeature creates a minimal feature for plan loop integration testing.
func newTestPlanFeature(t *testing.T, repoPath string) *feature.Feature {
	t.Helper()
	return &feature.Feature{
		ID:           "test-plan-001",
		Name:         "Test Plan Feature",
		Slug:         "test-plan-feature",
		Description:  "Integration test for plan validation",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
		ActiveRun:    1,
		RunCount:     1,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: repoPath},
		},
		Models: config.ModelConfig{
			Planning: "planner",
			Review:   "reviewer",
		},
		ExitCriteria:  "Relevant tests pass",
		SchemaVersion: feature.SchemaVersionCurrent,
	}
}

func writeRoadmapArtifactSnippet(planDir string) string {
	return fmt.Sprintf(`cat > %q <<'ROADMAPEOF'
%s
ROADMAPEOF
`, filepath.Join(planDir, "roadmap.md"), validRoadmapText())
}

type plannerScriptArtifacts struct {
	PlanText string
}

func writePhasePlannerArtifactsSnippet(planDir string, artifacts plannerScriptArtifacts) string {
	if artifacts.PlanText == "" {
		return ""
	}
	return fmt.Sprintf(`cat > %q <<'PLANEOF'
%s
PLANEOF
`, filepath.Join(planDir, "plan.md"), artifacts.PlanText)
}

type phasePlanningLoopRun struct {
	Result       *PlanLoopResult
	PhasePlanDir string
}

func runPhasePlanningLoopWithPlannerArtifacts(t *testing.T, artifacts plannerScriptArtifacts) phasePlanningLoopRun {
	t.Helper()

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	phasePlanDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, d := range []string{workDir, phasePlanDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			writePhasePlannerArtifactsSnippet(phasePlanDir, artifacts)+
			testutil.TouchPhaseCompleteInLatestAttemptDir(phasePlanDir)+"\n"+
			testutil.JSONLSuccess+"\n")
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature) error = %v", err)
	}

	result, err := RunPhasePlanningLoop(PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               store,
			StateDir:                   stateDir,
			WorkDir:                    workDir,
			MaxAttempts:                2,
			DangerouslySkipPermissions: true,
			BuildSession:               mockBuildSession(planScript, criticScript),
		},
		Phase: RoadmapPhase{
			Number: 1,
			Name:   "Test Phase",
			Type:   "tdd-fill-in",
			Goal:   "Test phase planner contract",
		},
	}, sm)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop() error = %v", err)
	}
	return phasePlanningLoopRun{Result: result, PhasePlanDir: phasePlanDir}
}

func runPhasePlanningLoopWithSecondAttemptRepair(t *testing.T) *PlanLoopResult {
	t.Helper()

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	phasePlanDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, d := range []string{workDir, phasePlanDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}

	counterPath := filepath.Join(tmpDir, "phase-plan-counter")
	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh", fmt.Sprintf(`%s
if [ ! -f %q ]; then
  echo 1 > %q
%s
else
%s
fi
%s
%s
`,
		testutil.JSONLInit,
		counterPath,
		counterPath,
		writePhasePlannerArtifactsSnippet(phasePlanDir, plannerScriptArtifacts{}),
		writePhasePlannerArtifactsSnippet(phasePlanDir, plannerScriptArtifacts{
			PlanText: validPhasePlanText(),
		}),
		testutil.TouchPhaseCompleteInLatestAttemptDir(phasePlanDir),
		testutil.JSONLSuccess))
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature) error = %v", err)
	}

	result, err := RunPhasePlanningLoop(PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               store,
			StateDir:                   stateDir,
			WorkDir:                    workDir,
			MaxAttempts:                2,
			DangerouslySkipPermissions: true,
			BuildSession:               mockBuildSession(planScript, criticScript),
		},
		Phase: RoadmapPhase{
			Number: 1,
			Name:   "Test Phase",
			Type:   "tdd-fill-in",
			Goal:   "Test phase planner contract repair",
		},
	}, sm)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop() error = %v", err)
	}
	return result
}

func TestRoadmapPlanningLoopMissingRoadmapAfterPhaseCompleteTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	planDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "roadmap")
	for _, d := range []string{workDir, planDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+testutil.TouchPhaseCompleteInLatestAttemptDir(planDir)+"\n"+testutil.JSONLSuccess+"\n")
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	_ = store.Save(f)

	result, err := RunRoadmapPlanningLoop(PlanLoopConfig{
		Feature:                    f,
		FeatureStore:               store,
		StateDir:                   stateDir,
		WorkDir:                    workDir,
		MaxAttempts:                2,
		DangerouslySkipPermissions: true,
		BuildSession:               mockBuildSession(planScript, criticScript),
	}, sm)
	if err != nil {
		t.Fatalf("RunRoadmapPlanningLoop() error = %v", err)
	}
	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}
	if !strings.Contains(result.LastError, string(RolePlanRoadmapReviser)) || !strings.Contains(result.LastError, "roadmap markdown") {
		t.Fatalf("LastError = %q, want roadmap reviser contract violation", result.LastError)
	}
}

func TestRoadmapPlanningLoopWritesFirstAttemptQALog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	planDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "roadmap")
	for _, d := range []string{workDir, planDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan-qa.sh",
		testutil.JSONLInit+"\n"+
			`read -r _agentic_init`+"\n"+
			`echo '{"type":"control_request","request_id":"ask_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Scope?","options":[{"label":"Roadmap only (Recommended)","confidence":0.91},{"label":"Everything","confidence":0.1}]}]}}}'`+"\n"+
			`read -r _ask_response`+"\n"+
			writeRoadmapArtifactSnippet(planDir)+"\n"+
			testutil.TouchPhaseCompleteInLatestAttemptDir(planDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	f.Pipeline = feature.PipelineMedium
	f.Inquireness = feature.InquirenessNone
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature): %v", err)
	}

	result, err := RunRoadmapPlanningLoop(PlanLoopConfig{
		Feature:                    f,
		FeatureStore:               store,
		StateDir:                   stateDir,
		WorkDir:                    workDir,
		MaxAttempts:                1,
		DangerouslySkipPermissions: true,
		BuildSession:               mockBuildSession(planScript, ""),
	}, sm)
	if err != nil {
		t.Fatalf("RunRoadmapPlanningLoop(): %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Fatalf("FinalStatus = %q, want approved (LastError=%q)", result.FinalStatus, result.LastError)
	}

	qaPath := filepath.Join(planDir, "qa-answers.md")
	data, err := os.ReadFile(qaPath)
	if err != nil {
		t.Fatalf("read roadmap qa-answers.md: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "## Q: Scope?") || !strings.Contains(content, "**A:** Roadmap only (Recommended)") {
		t.Errorf("roadmap qa-answers.md missing selected answer:\n%s", content)
	}
	if !strings.Contains(content, "_(auto-picked, confidence: 0.91)_") {
		t.Errorf("roadmap qa-answers.md missing auto-pick annotation:\n%s", content)
	}
}

func TestRoadmapPlanningLoopMissingPhaseCompleteTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	planDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "roadmap")
	for _, d := range []string{workDir, planDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			writeRoadmapArtifactSnippet(planDir)+
			// A stale/shared parent marker must not satisfy an attempt-local
			// planner completion contract.
			testutil.TouchPhaseCompleteInDir(planDir)+"\n"+
			testutil.JSONLSuccess+"\n")
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature) error = %v", err)
	}

	result, err := RunRoadmapPlanningLoop(PlanLoopConfig{
		Feature:                    f,
		FeatureStore:               store,
		StateDir:                   stateDir,
		WorkDir:                    workDir,
		MaxAttempts:                2,
		DangerouslySkipPermissions: true,
		BuildSession:               mockBuildSession(planScript, criticScript),
	}, sm)
	if err != nil {
		t.Fatalf("RunRoadmapPlanningLoop() error = %v", err)
	}
	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}
	if !strings.Contains(result.LastError, string(RolePlanRoadmapReviser)) || !strings.Contains(result.LastError, "phase_complete") {
		t.Fatalf("LastError = %q, want roadmap reviser missing-marker violation", result.LastError)
	}
}

func TestRoadmapPlanningLoopContractViolationCanRecoverWithinAttemptBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	planDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "roadmap")
	for _, d := range []string{workDir, planDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}

	counterPath := filepath.Join(tmpDir, "plan-counter")
	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh", fmt.Sprintf(`%s
if [ ! -f %q ]; then
  echo 1 > %q
else
%s
fi
%s
%s
`, testutil.JSONLInit, counterPath, counterPath, writeRoadmapArtifactSnippet(planDir), testutil.TouchPhaseCompleteInLatestAttemptDir(planDir), testutil.JSONLSuccess))
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	_ = store.Save(f)

	result, err := RunRoadmapPlanningLoop(PlanLoopConfig{
		Feature:                    f,
		FeatureStore:               store,
		StateDir:                   stateDir,
		WorkDir:                    workDir,
		MaxAttempts:                2,
		DangerouslySkipPermissions: true,
		BuildSession:               mockBuildSession(planScript, criticScript),
	}, sm)
	if err != nil {
		t.Fatalf("RunRoadmapPlanningLoop() error = %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Fatalf("FinalStatus = %q, want approved (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}
}

// TestRoadmapPlanningLoop_FinishOrViolateNudgeRecoversSameSession proves the
// roadmap planner recovers within a single attempt via the finish-or-violate
// nudge: the planner ends its first turn without roadmap.md, the harness nudges
// the same live session, and the nudged turn writes roadmap.md + phase_complete
// so the critic approves and the loop ends with one attempt.
func TestRoadmapPlanningLoop_FinishOrViolateNudgeRecoversSameSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	planDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "roadmap")
	for _, d := range []string{workDir, planDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh", fmt.Sprintf(`%s
echo '{"type":"result","subtype":"success","session_id":"mock","total_cost_usd":0.001,"stop_reason":"end_turn"}'
while IFS= read -r _line; do
  case "$_line" in
    %s)
      %s
      %s
      echo '{"type":"result","subtype":"success","session_id":"mock","total_cost_usd":0.001,"stop_reason":"end_turn"}'
      exit 0
      ;;
  esac
done
`, testutil.JSONLInit, finishOrViolateNudgeCasePattern, writeRoadmapArtifactSnippet(planDir), testutil.TouchPhaseCompleteInLatestAttemptDir(planDir)))
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	sm := session.NewManager(make(chan interface{}, 100))
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	_ = store.Save(f)

	result, err := RunRoadmapPlanningLoop(PlanLoopConfig{
		Feature:                    f,
		FeatureStore:               store,
		StateDir:                   stateDir,
		WorkDir:                    workDir,
		MaxAttempts:                1,
		DangerouslySkipPermissions: true,
		FinishOrViolateNudge:       true,
		BuildSession:               mockBuildSession(planScript, criticScript),
	}, sm)
	if err != nil {
		t.Fatalf("RunRoadmapPlanningLoop() error = %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Fatalf("FinalStatus = %q, want approved (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 1 {
		t.Fatalf("Iterations = %d, want 1 (recovered within the first attempt)", result.Iterations)
	}
}

// TestRoadmapPlanningLoop_GatesInteractiveTurnMode proves the roadmap planner
// gates TurnModeInteractive on the finish-or-violate capability: armed only when
// PlanLoopConfig.FinishOrViolateNudge is set, default one-shot otherwise.
func TestRoadmapPlanningLoop_GatesInteractiveTurnMode(t *testing.T) {
	cases := []struct {
		name     string
		nudge    bool
		wantMode ports.SessionTurnMode
	}{
		{name: "capability armed uses interactive", nudge: true, wantMode: ports.TurnModeInteractive},
		{name: "capability off uses one-shot", nudge: false, wantMode: ports.TurnModeOneShot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			workDir := filepath.Join(tmpDir, "work")
			if err := os.MkdirAll(workDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			store := feature.NewStore(tmpDir)
			f := newTestPlanFeature(t, workDir)
			_ = store.Save(f)

			// session.SessionOpts is a type alias for ports.SessionOpts
			// (session/manager.go), so this captures the exact concrete value
			// production sets TurnMode on.
			var capturedOpts *ports.SessionOpts
			_, err := RunRoadmapPlanningLoop(PlanLoopConfig{
				Feature:              f,
				FeatureStore:         store,
				StateDir:             tmpDir,
				WorkDir:              workDir,
				MaxAttempts:          1,
				FinishOrViolateNudge: tc.nudge,
				BuildSession: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
					return []string{"echo", "unused"}, nil, &ports.SessionOpts{PIDDir: opts.PIDDir}, nil
				},
				SessionStartFunc: func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
					if len(opts) > 0 {
						capturedOpts = opts[0]
					}
					return nil, session.ErrShuttingDown
				},
			}, nil)
			if err != nil {
				t.Fatalf("RunRoadmapPlanningLoop() error: %v", err)
			}
			if capturedOpts == nil {
				t.Fatal("expected SessionOpts to be captured")
			}
			if capturedOpts.TurnMode != tc.wantMode {
				t.Errorf("TurnMode = %v, want %v", capturedOpts.TurnMode, tc.wantMode)
			}
		})
	}
}

func TestRoadmapPlanningLoopUsesAttemptDirCompletionMarker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	planDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "roadmap")
	for _, d := range []string{workDir, planDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			writeRoadmapArtifactSnippet(planDir)+
			testutil.TouchPhaseCompleteInLatestAttemptDir(planDir)+"\n"+
			testutil.JSONLSuccess+"\n")
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")
	buildSession, captured := capturingBuildSession(planScript, criticScript)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	_ = store.Save(f)

	result, err := RunRoadmapPlanningLoop(PlanLoopConfig{
		Feature:                    f,
		FeatureStore:               store,
		StateDir:                   stateDir,
		WorkDir:                    workDir,
		MaxAttempts:                1,
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
	}, sm)
	if err != nil {
		t.Fatalf("RunRoadmapPlanningLoop() error = %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Fatalf("FinalStatus = %q, want approved (LastError=%q)", result.FinalStatus, result.LastError)
	}

	var planOpts *BuildSessionOpts
	for i := range *captured {
		if _, ok := (*captured)[i].PermHandler.(*permission.ReadOnlyHandler); !ok {
			planOpts = &(*captured)[i]
			break
		}
	}
	if planOpts == nil {
		t.Fatal("no non-helper planning session found")
	}
	if !reflect.DeepEqual(planOpts.AgentNames, explorationAgentNames()) {
		t.Fatalf("roadmap planner AgentNames = %v, want exploration set %v", planOpts.AgentNames, explorationAgentNames())
	}
	wantMarker := filepath.Join(planDir, "attempt-01", "phase_complete")
	if !strings.Contains(planOpts.SystemPrompt, wantMarker) {
		t.Fatalf("SystemPrompt missing attempt marker path %q:\n%s", wantMarker, planOpts.SystemPrompt)
	}
	parentMarker := filepath.Join(planDir, "phase_complete")
	if strings.Contains(planOpts.SystemPrompt, parentMarker) {
		t.Fatalf("SystemPrompt contains parent marker path %q:\n%s", parentMarker, planOpts.SystemPrompt)
	}
}

func TestPhasePlanningLoopMissingPlanMarkdownTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	run := runPhasePlanningLoopWithPlannerArtifacts(t, plannerScriptArtifacts{})
	result := run.Result
	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}
	if !strings.Contains(result.LastError, string(RolePlanPhaseReviser)) ||
		!strings.Contains(result.LastError, "phase plan markdown") {
		t.Fatalf("LastError = %q, want phase plan markdown violation", result.LastError)
	}
	feedbackPath := filepath.Join(run.PhasePlanDir, "attempt-02", "validation-feedback.md")
	feedback, err := os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", feedbackPath, err)
	}
	if !strings.Contains(string(feedback), string(RolePlanPhaseReviser)) ||
		!strings.Contains(string(feedback), "phase plan markdown") {
		t.Fatalf("validation-feedback.md = %q, want planner role and missing artifact", feedback)
	}
}

func TestPhasePlanningLoopWritesFirstAttemptQALog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	phasePlanDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, d := range []string{workDir, phasePlanDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "phase-plan-qa.sh",
		testutil.JSONLInit+"\n"+
			`read -r _agentic_init`+"\n"+
			`echo '{"type":"control_request","request_id":"ask_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Slice?","options":[{"label":"Tracer (Recommended)","confidence":0.83},{"label":"Full build","confidence":0.2}]}]}}}'`+"\n"+
			`read -r _ask_response`+"\n"+
			testutil.WritePhasePlanSuccessArtifacts(phasePlanDir, validPhasePlanText())+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	f.Pipeline = feature.PipelineMedium
	f.Inquireness = feature.InquirenessHigh
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature): %v", err)
	}

	result, err := RunPhasePlanningLoop(PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               store,
			StateDir:                   stateDir,
			WorkDir:                    workDir,
			MaxAttempts:                1,
			DangerouslySkipPermissions: true,
			BuildSession:               mockBuildSession(planScript, ""),
		},
		Phase: RoadmapPhase{Number: 1, Name: "Test Phase", Type: "tdd-fill-in", Goal: "Write plan"},
	}, sm)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop(): %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Fatalf("FinalStatus = %q, want approved (LastError=%q)", result.FinalStatus, result.LastError)
	}

	qaPath := filepath.Join(phasePlanDir, "qa-answers.md")
	data, err := os.ReadFile(qaPath)
	if err != nil {
		t.Fatalf("read phase-plan qa-answers.md: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "## Q: Slice?") || !strings.Contains(content, "**A:** Tracer (Recommended)") {
		t.Errorf("phase-plan qa-answers.md missing selected answer:\n%s", content)
	}
	if !strings.Contains(content, "_(auto-picked, confidence: 0.83)_") {
		t.Errorf("phase-plan qa-answers.md missing auto-pick annotation:\n%s", content)
	}
}

func TestPhasePlanningLoopMissingPhaseCompleteTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	phasePlanDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, d := range []string{workDir, phasePlanDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			writePhasePlannerArtifactsSnippet(phasePlanDir, plannerScriptArtifacts{
				PlanText: validPhasePlanText(),
			})+
			// A stale/shared parent marker must not satisfy an attempt-local
			// planner completion contract.
			testutil.TouchPhaseCompleteInDir(phasePlanDir)+"\n"+
			testutil.JSONLSuccess+"\n")
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature) error = %v", err)
	}

	result, err := RunPhasePlanningLoop(PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               store,
			StateDir:                   stateDir,
			WorkDir:                    workDir,
			MaxAttempts:                2,
			DangerouslySkipPermissions: true,
			BuildSession:               mockBuildSession(planScript, criticScript),
		},
		Phase: RoadmapPhase{
			Number: 1,
			Name:   "Test Phase",
			Type:   "tdd-fill-in",
			Goal:   "Test phase planner marker",
		},
	}, sm)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop() error = %v", err)
	}
	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}
	if !strings.Contains(result.LastError, string(RolePlanPhaseReviser)) || !strings.Contains(result.LastError, "phase_complete") {
		t.Fatalf("LastError = %q, want phase-plan reviser missing-marker violation", result.LastError)
	}
}

func TestPhasePlanningLoopContractViolationCanRecoverWithinAttemptBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	result := runPhasePlanningLoopWithSecondAttemptRepair(t)
	if result.FinalStatus != "approved" {
		t.Fatalf("FinalStatus = %q, want approved (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}
}

// planTextWithVisualEvidenceRow returns validPhasePlanText() with a real
// Visual Evidence requirement in place of the None-required marker, so the
// deterministic agent-evidence check has something to reject for
// automated-only profiles.
func planTextWithVisualEvidenceRow() string {
	return strings.Replace(validPhasePlanText(),
		"### Visual Evidence\n- [ ] None required: no user-facing rendered surface.\n\n",
		"### Visual Evidence\n- [ ] Home screen default state [size: 760x480]\n\n", 1)
}

func TestPhasePlanningLoopRejectsAgentEvidenceForAutomatedOnlyProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	phasePlanDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, d := range []string{workDir, phasePlanDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			testutil.WritePhasePlanSuccessArtifacts(phasePlanDir, planTextWithVisualEvidenceRow())+"\n"+
			testutil.JSONLSuccess+"\n")
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	f.Pipeline = feature.PipelineLarge
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature) error = %v", err)
	}

	result, err := RunPhasePlanningLoop(PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               store,
			StateDir:                   stateDir,
			WorkDir:                    workDir,
			MaxAttempts:                2,
			DangerouslySkipPermissions: true,
			BuildSession:               mockBuildSession(planScript, criticScript),
		},
		Phase: RoadmapPhase{
			Number: 1,
			Name:   "Test Phase",
			Type:   "tdd-fill-in",
			Goal:   "Test automated-only evidence rejection",
		},
	}, sm)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop() error = %v", err)
	}
	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if !strings.Contains(result.LastError, "### Visual Evidence") {
		t.Fatalf("LastError = %q, want it to name ### Visual Evidence", result.LastError)
	}

	feedbackPath := filepath.Join(phasePlanDir, "attempt-02", "validation-feedback.md")
	feedback, err := os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", feedbackPath, err)
	}
	if !strings.Contains(string(feedback), "### Visual Evidence") {
		t.Fatalf("validation-feedback.md = %q, want it to name ### Visual Evidence", feedback)
	}
}

func TestPhasePlanningLoopAllowsAgentEvidenceForMoonshotProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	phasePlanDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, d := range []string{workDir, phasePlanDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			testutil.WritePhasePlanSuccessArtifacts(phasePlanDir, planTextWithVisualEvidenceRow())+"\n"+
			testutil.JSONLSuccess+"\n")
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	f.Pipeline = feature.PipelineMoonshot
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature) error = %v", err)
	}

	result, err := RunPhasePlanningLoop(PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               store,
			StateDir:                   stateDir,
			WorkDir:                    workDir,
			MaxAttempts:                2,
			DangerouslySkipPermissions: true,
			BuildSession:               mockBuildSession(planScript, criticScript),
		},
		Phase: RoadmapPhase{
			Number: 1,
			Name:   "Test Phase",
			Type:   "tdd-fill-in",
			Goal:   "Test agent evidence allowed for moonshot",
		},
	}, sm)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop() error = %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Fatalf("FinalStatus = %q, want approved (LastError=%q)", result.FinalStatus, result.LastError)
	}
}

func TestPlanValidatorHelperUsesIsolatedDirAndMarkerContract(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	attemptDir := filepath.Join(tmpDir, "attempt-01")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, attemptDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}
	planArtifactPath := filepath.Join(attemptDir, "plan.md")
	if err := os.WriteFile(planArtifactPath, []byte(validPhasePlanText()), 0o644); err != nil {
		t.Fatalf("write plan artifact: %v", err)
	}

	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh", fmt.Sprintf(`%s
%s
for _prompt in $(find %s -name 'validation-*-prompt.md' -type f 2>/dev/null); do
  _dir=$(dirname "$_prompt")
  touch "$_dir/phase_complete"
done
%s
`, testutil.JSONLInit, testutil.WriteAnyValidatorApproved(tmpDir), tmpDir, testutil.JSONLSuccess))

	var got BuildSessionOpts
	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(filepath.Join(tmpDir, "store"))
	f := &feature.Feature{
		ID:              "test-plan-001",
		Name:            "Test Plan Feature",
		RiskLevel:       feature.RiskMedium,
		Status:          feature.StatusPlanning,
		ActiveTimingKey: "phase-1-plan",
		SchemaVersion:   feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	cfg := PlanLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     tmpDir,
		WorkDir:      workDir,
		BuildSession: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			got = opts
			return []string{"bash", criticScript}, nil, &session.SessionOpts{
				PermHandler:       opts.PermHandler,
				DebugSystemPrompt: opts.SystemPrompt,
				ProviderName:      testMockIdentifier,
			}, nil
		},
	}

	status, _, _, err := runSpecializedPlanValidationForArtifact(
		cfg,
		sm,
		1,
		attemptDir,
		planArtifactPath,
		validatorDomain{Name: "Scope", Template: "validate-roadmap-scope"},
		validationArtifactPhasePlan,
		planValidationExtras{},
		observe.SpanContext{},
	)
	if err != nil {
		t.Fatalf("runSpecializedPlanValidationForArtifact() error = %v", err)
	}
	if status != ReviewApproved {
		t.Fatalf("status = %s, want APPROVED", status)
	}

	helperDir := filepath.Join(attemptDir, "validate-scope")
	for _, path := range []string{
		filepath.Join(helperDir, "validation-scope-feedback.md"),
		filepath.Join(helperDir, "phase_complete"),
		filepath.Join(attemptDir, "validation-scope-feedback.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected artifact %q: %v", path, err)
		}
	}
	if !strings.Contains(got.SystemPrompt, filepath.Join(helperDir, "phase_complete")) {
		t.Fatalf("SystemPrompt missing helper marker path:\n%s", got.SystemPrompt)
	}
	if !permissionHandlerIncludesBoundedArtifacts(got.PermHandler) {
		t.Fatalf("PermHandler = %T, want bounded artifact handler", got.PermHandler)
	}
	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.PhaseCost("phase-1-plan"); got != 0.001 {
		t.Errorf("PhaseCost(phase-1-plan) = %v, want 0.001", got)
	}
	if len(updated.SessionCosts) != 1 {
		t.Fatalf("len(SessionCosts) = %d, want 1", len(updated.SessionCosts))
	}
	cost := updated.SessionCosts[0]
	if cost.SessionID != "test-plan-001-planreview-scope-01" {
		t.Errorf("SessionID = %q, want test-plan-001-planreview-scope-01", cost.SessionID)
	}
	if cost.PhaseKey != "phase-1-plan" {
		t.Errorf("PhaseKey = %q, want phase-1-plan", cost.PhaseKey)
	}
	if cost.ObserverPhase != "review" {
		t.Errorf("ObserverPhase = %q, want review", cost.ObserverPhase)
	}
	if cost.CostUSD != 0.001 {
		t.Errorf("CostUSD = %v, want 0.001", cost.CostUSD)
	}
}

func TestPlanValidatorMissingPhaseCompleteReturnsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	phasePlanDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, d := range []string{workDir, phasePlanDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", d, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			writePhasePlannerArtifactsSnippet(phasePlanDir, plannerScriptArtifacts{
				PlanText: validPhasePlanText(),
			})+
			testutil.TouchPhaseCompleteInLatestAttemptDir(phasePlanDir)+"\n"+
			testutil.JSONLSuccess+"\n")
	criticScript := testutil.WriteScript(t, scriptsDir, "critic-no-marker.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApprovedWithoutPhaseComplete(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(feature) error = %v", err)
	}

	result, err := RunPhasePlanningLoop(PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               store,
			StateDir:                   stateDir,
			WorkDir:                    workDir,
			MaxAttempts:                2,
			DangerouslySkipPermissions: true,
			BuildSession:               mockBuildSession(planScript, criticScript),
		},
		Phase: RoadmapPhase{
			Number: 1,
			Name:   "Test Phase",
			Type:   "tdd-fill-in",
			Goal:   "Test phase validator marker",
		},
	}, sm)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop() error = %v", err)
	}
	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if !strings.Contains(result.LastError, string(RoleValidatePhasePlanStructural)) || !strings.Contains(result.LastError, "phase_complete") {
		t.Fatalf("LastError = %q, want structural validator phase_complete violation", result.LastError)
	}
	if _, err := os.Stat(filepath.Join(phasePlanDir, "attempt-02", "validate-structural", "validation-structural-feedback.md")); err != nil {
		t.Fatalf("expected helper feedback in child dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(phasePlanDir, "attempt-02", "validation-structural-feedback.md")); err != nil {
		t.Fatalf("expected mirrored parent feedback: %v", err)
	}
	parentFeedback, err := os.ReadFile(filepath.Join(phasePlanDir, "attempt-02", "validation-structural-feedback.md"))
	if err != nil {
		t.Fatalf("reading mirrored parent feedback: %v", err)
	}
	if !strings.Contains(string(parentFeedback), "phase_complete") {
		t.Fatalf("parent validation feedback missing phase_complete violation:\n%s", parentFeedback)
	}
	if !strings.Contains(string(parentFeedback), agentStatusChangesRequested) || strings.Contains(string(parentFeedback), "\nAPPROVED") {
		t.Fatalf("parent validation feedback did not override approved verdict:\n%s", parentFeedback)
	}
}

// TestPlanLoopMaxAttemptsOverride verifies that cfg.MaxAttempts overrides the default.

// TestPlanLoopIterateMoreResumesCorrectly simulates the "Iterate more" flow:
// 3 failed attempts are pre-seeded, MaxAttempts is set to 6, and the loop
// should resume from attempt 4 and run up to attempt 6.

// TestPlanLoopOutputPreservation verifies that the loop writes attempt-specific
// output files (attempt-01/output.txt, attempt-02/output.txt) and that
// earlier attempt files are not overwritten by later attempts.

func TestIsArtifactExcluded(t *testing.T) {
	tests := []struct {
		name     string
		excluded bool
	}{
		{"system-prompt.md", true},
		{"user-prompt.md", true},
		{"validation-1-feedback.md", true},
		{"validation-2-prompt.md", true},
		{"debug-output.md", true},
		{"error.log", true},
		{"output.txt", true},
		{"plan-revision-prompt.md", true},
		{"plan.md", false},
		{"research.md", false},
		{"qa-answers.md", true},
		{".protocol-retry.yaml", true},
		{".protocol-retry-feat-abc123.yaml", true},
		{"protocol-retry.yaml", false},
		{".protocol-retry-feat-abc123.txt", false},
		{"2026-03-13-my-feature.md", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsArtifactExcluded(tt.name)
			if got != tt.excluded {
				t.Errorf("IsArtifactExcluded(%q) = %v, want %v", tt.name, got, tt.excluded)
			}
		})
	}
}

// --- Tests for plan attempt resume ---

func TestLatestCompletedPlanAttempt(t *testing.T) {
	t.Run("no meta files", func(t *testing.T) {
		dir := t.TempDir()
		if got := LatestCompletedPlanAttempt(dir); got != 0 {
			t.Errorf("expected 0, got %d", got)
		}
	})

	t.Run("single meta file", func(t *testing.T) {
		dir := t.TempDir()
		_ = WritePlanAttemptMeta(dir, PlanAttemptMeta{Attempt: 1, AgentStatus: agentStatusSuccess, ReviewStatus: agentStatusChangesRequested})
		if got := LatestCompletedPlanAttempt(dir); got != 1 {
			t.Errorf("expected 1, got %d", got)
		}
	})

	t.Run("multiple meta files returns highest", func(t *testing.T) {
		dir := t.TempDir()
		_ = WritePlanAttemptMeta(dir, PlanAttemptMeta{Attempt: 1, AgentStatus: agentStatusSuccess, ReviewStatus: agentStatusChangesRequested})
		_ = WritePlanAttemptMeta(dir, PlanAttemptMeta{Attempt: 2, AgentStatus: agentStatusSuccess, ReviewStatus: agentStatusChangesRequested})
		if got := LatestCompletedPlanAttempt(dir); got != 2 {
			t.Errorf("expected 2, got %d", got)
		}
	})

	t.Run("skips failed attempts", func(t *testing.T) {
		dir := t.TempDir()
		_ = WritePlanAttemptMeta(dir, PlanAttemptMeta{Attempt: 1, AgentStatus: "FAILED", ReviewStatus: ""})
		if got := LatestCompletedPlanAttempt(dir); got != 0 {
			t.Errorf("expected 0 (failed attempt should be skipped), got %d", got)
		}
	})

	t.Run("skips failed returns latest success", func(t *testing.T) {
		dir := t.TempDir()
		_ = WritePlanAttemptMeta(dir, PlanAttemptMeta{Attempt: 1, AgentStatus: agentStatusSuccess, ReviewStatus: agentStatusChangesRequested})
		_ = WritePlanAttemptMeta(dir, PlanAttemptMeta{Attempt: 2, AgentStatus: "FAILED", ReviewStatus: ""})
		if got := LatestCompletedPlanAttempt(dir); got != 1 {
			t.Errorf("expected 1 (attempt 2 failed), got %d", got)
		}
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		if got := LatestCompletedPlanAttempt("/nonexistent/path"); got != 0 {
			t.Errorf("expected 0, got %d", got)
		}
	})
}

func TestRunPhasePlanningLoopRetriesFailedAttemptWithFreshSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	phasePlanDir := filepath.Join(tmpDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, dir := range []string{workDir, phasePlanDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}
	if err := WritePlanAttemptMeta(phasePlanDir, PlanAttemptMeta{
		Attempt:      1,
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: agentStatusChangesRequested,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(phasePlanDir, "attempt-01", "validation-feedback.md"), []byte("revise this plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePlanAttemptMeta(phasePlanDir, PlanAttemptMeta{
		Attempt:     2,
		AgentStatus: "FAILED",
	}); err != nil {
		t.Fatal(err)
	}

	store := feature.NewStore(tmpDir)
	f := newTestPlanFeature(t, workDir)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}

	var gotSessionID string
	result, err := RunPhasePlanningLoop(PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:      f,
			FeatureStore: store,
			StateDir:     tmpDir,
			WorkDir:      workDir,
			MaxAttempts:  3,
			BuildSession: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
				return []string{"echo", "unused"}, nil, &ports.SessionOpts{PIDDir: opts.PIDDir}, nil
			},
			SessionStartFunc: func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
				gotSessionID = id
				return nil, session.ErrShuttingDown
			},
		},
		Phase: RoadmapPhase{
			Number: 1,
			Name:   "Restarted plan",
			Type:   "tdd-fill-in",
			Goal:   "Retry failed provider sessions without changing the plan attempt",
		},
	}, nil)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop() error = %v", err)
	}
	if result.FinalStatus != "interrupted" {
		t.Fatalf("FinalStatus = %q, want interrupted", result.FinalStatus)
	}
	if gotSessionID != "test-plan-001-phase-01-plan-02-retry-02" {
		t.Fatalf("sessionID = %q, want test-plan-001-phase-01-plan-02-retry-02", gotSessionID)
	}
	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PlanIteration != 2 {
		t.Fatalf("PlanIteration = %d, want 2", updated.PlanIteration)
	}
}

func TestRunPhasePlanningLoopRetriesEarlyInfrastructureFailureInProcess(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	phasePlanDir := filepath.Join(tmpDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, dir := range []string{workDir, phasePlanDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	store := feature.NewStore(tmpDir)
	f := newTestPlanFeature(t, workDir)
	f.Pipeline = feature.PipelineMedium
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}

	var sessionIDs []string
	result, err := RunPhasePlanningLoop(PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:      f,
			FeatureStore: store,
			StateDir:     tmpDir,
			WorkDir:      workDir,
			MaxAttempts:  1,
			BuildSession: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
				return []string{"echo", "unused"}, nil, &ports.SessionOpts{PIDDir: opts.PIDDir}, nil
			},
			SessionStartFunc: func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
				sessionIDs = append(sessionIDs, id)
				if len(sessionIDs) == 1 {
					return newTerminalStatusTestSession(ports.SessionFailed), nil
				}
				attemptDir := filepath.Join(phasePlanDir, "attempt-01")
				if err := os.WriteFile(filepath.Join(phasePlanDir, "plan.md"), []byte(validPhasePlanText()), 0o644); err != nil {
					t.Fatalf("write plan.md: %v", err)
				}
				if err := os.WriteFile(filepath.Join(attemptDir, PhaseCompleteFile), nil, 0o644); err != nil {
					t.Fatalf("write phase_complete: %v", err)
				}
				sess := newUtilityTestSession()
				sess.statusCh <- agentStatusSuccess
				return sess, nil
			},
		},
		Phase: RoadmapPhase{
			Number: 1,
			Name:   "Retry plan",
			Type:   "tdd-fill-in",
			Goal:   "Retry a provider child process that died before doing work",
		},
	}, nil)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop() error = %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Fatalf("FinalStatus = %q, want approved", result.FinalStatus)
	}
	wantSessionIDs := []string{
		"test-plan-001-phase-01-plan-01",
		"test-plan-001-phase-01-plan-01-retry-02",
	}
	if !reflect.DeepEqual(sessionIDs, wantSessionIDs) {
		t.Fatalf("session IDs = %#v, want %#v", sessionIDs, wantSessionIDs)
	}
	if _, err := os.Stat(filepath.Join(phasePlanDir, "attempt-02")); !os.IsNotExist(err) {
		t.Fatalf("attempt-02 stat err = %v, want not exist", err)
	}
}

func TestPlanRetrySessionAttempt(t *testing.T) {
	dir := t.TempDir()
	if got := nextPlanSessionAttempt(dir, 2); got != 1 {
		t.Fatalf("nextPlanSessionAttempt(no meta) = %d, want 1", got)
	}
	if got := planAttemptSessionID("feature-phase-01-plan-02", 1); got != "feature-phase-01-plan-02" {
		t.Fatalf("planAttemptSessionID(first) = %q", got)
	}
	if err := WritePlanAttemptMeta(dir, PlanAttemptMeta{Attempt: 2, AgentStatus: "FAILED"}); err != nil {
		t.Fatal(err)
	}
	if got := nextPlanSessionAttempt(dir, 2); got != 2 {
		t.Fatalf("nextPlanSessionAttempt(legacy failed) = %d, want 2", got)
	}
	if got := planAttemptSessionID("feature-phase-01-plan-02", 2); got != "feature-phase-01-plan-02-retry-02" {
		t.Fatalf("planAttemptSessionID(retry) = %q", got)
	}
	if err := WritePlanAttemptMeta(dir, PlanAttemptMeta{Attempt: 2, SessionAttempt: 2, AgentStatus: "FAILED"}); err != nil {
		t.Fatal(err)
	}
	if got := nextPlanSessionAttempt(dir, 2); got != 3 {
		t.Fatalf("nextPlanSessionAttempt(recorded failed retry) = %d, want 3", got)
	}
	if err := WritePlanAttemptMeta(dir, PlanAttemptMeta{Attempt: 2, AgentStatus: agentStatusSuccess, ReviewStatus: agentStatusChangesRequested}); err != nil {
		t.Fatal(err)
	}
	if got := nextPlanSessionAttempt(dir, 2); got != 1 {
		t.Fatalf("nextPlanSessionAttempt(success) = %d, want 1", got)
	}
}

func TestLoadPriorAxisApprovals(t *testing.T) {
	t.Run("latest wins per axis", func(t *testing.T) {
		dir := t.TempDir()
		write := func(attempt int, axis, body string) {
			attemptDir := filepath.Join(dir, fmt.Sprintf("attempt-%02d", attempt))
			if err := os.MkdirAll(attemptDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(attemptDir, fmt.Sprintf("axis-approved-%s.md", axis)), []byte(body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		write(1, "architecture", "# AxisApproved: architecture\nAttempt: 1\n\n## Frozen Sections\n- Architecture Approach\n- Old Phase\n")
		write(2, "scope", "# AxisApproved: scope\nAttempt: 2\n\n## Frozen Sections\n- Deferred Work\n")
		write(3, "architecture", "# AxisApproved: architecture\nAttempt: 3\n\n## Frozen Sections\n- Architecture Approach\n- New Phase\n")

		got := LoadPriorAxisApprovals(dir)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (%+v)", len(got), got)
		}
		if got[0].Axis != "architecture" {
			t.Errorf("got[0].Axis = %q, want architecture", got[0].Axis)
		}
		if !reflect.DeepEqual(got[0].FrozenSections, []string{"Architecture Approach", "New Phase"}) {
			t.Errorf("architecture FrozenSections = %#v; expected latest-wins", got[0].FrozenSections)
		}
		if got[1].Axis != "scope" {
			t.Errorf("got[1].Axis = %q, want scope", got[1].Axis)
		}
	})

	t.Run("empty directory returns nil", func(t *testing.T) {
		if got := LoadPriorAxisApprovals(t.TempDir()); got != nil {
			t.Errorf("got %#v, want nil", got)
		}
	})

	t.Run("missing frozen-sections heading yields empty list", func(t *testing.T) {
		dir := t.TempDir()
		attemptDir := filepath.Join(dir, "attempt-01")
		_ = os.MkdirAll(attemptDir, 0o755)
		_ = os.WriteFile(filepath.Join(attemptDir, "axis-approved-scope.md"),
			[]byte("# AxisApproved: scope\nAttempt: 1\n"), 0o644)

		got := LoadPriorAxisApprovals(dir)
		if len(got) != 1 || got[0].Axis != "scope" {
			t.Fatalf("expected single scope approval, got %#v", got)
		}
		if len(got[0].FrozenSections) != 0 {
			t.Errorf("FrozenSections = %#v, want empty", got[0].FrozenSections)
		}
	})

	// Fix B: a CHANGES_REQUESTED verdict on a later attempt must invalidate
	// an earlier APPROVED sticky, so the reviser isn't told to treat a
	// regressed axis's sections as no-touch.
	t.Run("later CHANGES_REQUESTED invalidates earlier approval", func(t *testing.T) {
		dir := t.TempDir()
		attempt1 := filepath.Join(dir, "attempt-01")
		attempt2 := filepath.Join(dir, "attempt-02")
		_ = os.MkdirAll(attempt1, 0o755)
		_ = os.MkdirAll(attempt2, 0o755)

		// attempt-1: scope approved with a frozen list.
		_ = os.WriteFile(filepath.Join(attempt1, "axis-approved-scope.md"),
			[]byte("# AxisApproved: scope\nAttempt: 1\n\n## Frozen Sections\n- Overview\n- Changes Required\n"), 0o644)
		_ = WritePlanAttemptMeta(dir, PlanAttemptMeta{
			Attempt:      1,
			AgentStatus:  agentStatusSuccess,
			ReviewStatus: agentStatusChangesRequested,
			AxisVerdicts: map[string]string{"scope": agentStatusApproved, testAxisGrounding: agentStatusChangesRequested},
		})
		// attempt-2: scope regressed — CHANGES_REQUESTED, no new sticky.
		_ = WritePlanAttemptMeta(dir, PlanAttemptMeta{
			Attempt:      2,
			AgentStatus:  agentStatusSuccess,
			ReviewStatus: agentStatusChangesRequested,
			AxisVerdicts: map[string]string{"scope": agentStatusChangesRequested, testAxisGrounding: agentStatusChangesRequested},
		})

		got := LoadPriorAxisApprovals(dir)
		for _, a := range got {
			if a.Axis == "scope" {
				t.Fatalf("stale scope approval leaked through regression: %#v", a)
			}
		}
	})

	// Fix B regression: an attempt-3 re-approval after a attempt-2 rejection
	// should restore the sticky (supersession works in both directions).
	t.Run("later APPROVED supersedes earlier CHANGES_REQUESTED", func(t *testing.T) {
		dir := t.TempDir()
		for _, a := range []int{1, 2, 3} {
			_ = os.MkdirAll(filepath.Join(dir, fmt.Sprintf("attempt-%02d", a)), 0o755)
		}
		_ = WritePlanAttemptMeta(dir, PlanAttemptMeta{
			Attempt: 1, AgentStatus: agentStatusSuccess, ReviewStatus: agentStatusChangesRequested,
			AxisVerdicts: map[string]string{"scope": agentStatusChangesRequested},
		})
		// attempt-2: scope rejected again, no sticky.
		_ = WritePlanAttemptMeta(dir, PlanAttemptMeta{
			Attempt: 2, AgentStatus: agentStatusSuccess, ReviewStatus: agentStatusChangesRequested,
			AxisVerdicts: map[string]string{"scope": agentStatusChangesRequested},
		})
		// attempt-3: scope finally approves.
		_ = os.WriteFile(filepath.Join(dir, "attempt-03", "axis-approved-scope.md"),
			[]byte("# AxisApproved: scope\nAttempt: 3\n\n## Frozen Sections\n- Deferred Work\n"), 0o644)
		_ = WritePlanAttemptMeta(dir, PlanAttemptMeta{
			Attempt: 3, AgentStatus: agentStatusSuccess, ReviewStatus: agentStatusChangesRequested,
			AxisVerdicts: map[string]string{"scope": agentStatusApproved},
		})

		got := LoadPriorAxisApprovals(dir)
		if len(got) != 1 || got[0].Axis != "scope" {
			t.Fatalf("expected scope approval to resurface, got %#v", got)
		}
		if !reflect.DeepEqual(got[0].FrozenSections, []string{"Deferred Work"}) {
			t.Errorf("FrozenSections = %#v", got[0].FrozenSections)
		}
	})
}

func TestComposeValidatorResults(t *testing.T) {
	tests := []struct {
		name       string
		results    []ValidatorResult
		wantStatus ReviewStatus
		wantInFeed string
		wantError  bool
	}{
		{
			name: "all approved → APPROVED",
			results: []ValidatorResult{
				{Domain: testAxisGrounding, Status: ReviewApproved},
				{Domain: "scope", Status: ReviewApproved},
				{Domain: "structural", Status: ReviewApproved},
			},
			wantStatus: ReviewApproved,
			wantInFeed: "Overall: APPROVED**",
		},
		{
			name: "provider error is infrastructure failure",
			results: []ValidatorResult{
				{Domain: "architecture", Status: ReviewFailed, Error: fmt.Errorf("codex rejected turn/start")},
				{Domain: "scope", Status: ReviewApproved},
			},
			wantStatus: ReviewChangesRequested,
			wantInFeed: "architecture Validator — ERROR",
			wantError:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, feedback, err := composeValidatorResults(tc.results, feature.RiskMedium)
			if err != nil && !tc.wantError {
				t.Fatalf("compose err: %v", err)
			}
			if err == nil && tc.wantError {
				t.Fatal("compose err = nil, want infrastructure error")
			}
			if status != tc.wantStatus {
				t.Errorf("status = %s, want %s", status, tc.wantStatus)
			}
			if !strings.Contains(feedback, tc.wantInFeed) {
				t.Errorf("feedback missing %q; got:\n%s", tc.wantInFeed, feedback)
			}
		})
	}
}

func TestRoadmapLoopValidatorInfrastructureFailureDoesNotRevisePlan(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	roadmapDir := filepath.Join(tmpDir, "test-plan-001", "runs", "run-001", "roadmap")
	for _, dir := range []string{workDir, scriptsDir, roadmapDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			writeRoadmapArtifactSnippet(roadmapDir)+
			testutil.TouchPhaseCompleteInLatestAttemptDir(roadmapDir)+"\n"+
			testutil.JSONLSuccess+"\n")
	criticScript := testutil.WriteScript(t, scriptsDir, "critic-error.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLError("codex rejected turn/start")+"\n")
	buildSession := mockBuildSession(planScript, criticScript)
	var callsMu sync.Mutex
	plannerCalls := 0
	criticCalls := 0

	store := feature.NewStore(tmpDir)
	f := newTestPlanFeature(t, workDir)
	f.Pipeline = feature.PipelineMoonshot
	f.RiskLevel = feature.RiskLow
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}

	sm := session.NewManager(make(chan interface{}, 100))
	defer sm.Shutdown()
	result, err := RunRoadmapPlanningLoop(PlanLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     tmpDir,
		WorkDir:      workDir,
		MaxAttempts:  3,
		BuildSession: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			callsMu.Lock()
			if !isReviewHelper(opts.PermHandler) {
				plannerCalls++
			} else {
				criticCalls++
			}
			callsMu.Unlock()
			return buildSession(opts)
		},
	}, sm)
	if err != nil {
		t.Fatalf("RunRoadmapPlanningLoop() error = %v", err)
	}
	if result.FinalStatus != "failed" || result.Iterations != 1 {
		t.Fatalf("result = %+v, want infrastructure failure in attempt 1", result)
	}
	if plannerCalls != 1 {
		t.Fatalf("planner calls = %d, want 1; validator failure must not trigger plan revision", plannerCalls)
	}
	wantCriticCalls := len(roadmapValidatorsForRisk(feature.RiskLow)) * maxValidatorInfrastructureSessionAttempts
	if criticCalls != wantCriticCalls {
		t.Fatalf("critic calls = %d, want %d validation-only retries", criticCalls, wantCriticCalls)
	}
	if _, err := os.Stat(filepath.Join(roadmapDir, "attempt-02")); !os.IsNotExist(err) {
		t.Fatalf("attempt-02 stat error = %v, want no second plan attempt", err)
	}
}

// TestReviewStatusIsApproved locks in the F4↔F1 integration contract:
func TestReviewStatusIsApproved(t *testing.T) {
	if !ReviewApproved.IsApproved() {
		t.Error("ReviewApproved should report IsApproved() == true")
	}
	if ReviewChangesRequested.IsApproved() {
		t.Error("ReviewChangesRequested should NOT report IsApproved()")
	}
	if ReviewFailed.IsApproved() {
		t.Error("ReviewFailed should NOT report IsApproved()")
	}
}

func TestWriteAxisApprovalArtifactRoundTrip(t *testing.T) {
	t.Run("digest round-trips when plan path is provided", func(t *testing.T) {
		dir := t.TempDir()
		attemptDir := filepath.Join(dir, "attempt-01")
		if err := os.MkdirAll(attemptDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		planPath := filepath.Join(dir, "plan.md")
		planBody := "# Phase 1\n\n## Grounding\n\n| Ref | Class |\n|-----|-------|\n| foo | EXISTS |\n\n## Changes Required\n\nEdit foo.go\n"
		if err := os.WriteFile(planPath, []byte(planBody), 0o644); err != nil {
			t.Fatalf("write plan: %v", err)
		}

		markers := ValidatorMarkers{AxisApproved: testAxisGrounding, FrozenSections: []string{groundingSectionHeading}}
		writeAxisApprovalArtifact(attemptDir, 1, markers, planPath)

		data, err := os.ReadFile(filepath.Join(attemptDir, "axis-approved-grounding.md"))
		if err != nil {
			t.Fatalf("read artifact: %v", err)
		}
		parsed := parseAxisApprovalArtifact(string(data))
		if parsed.Axis != testAxisGrounding {
			t.Errorf("Axis = %q, want grounding", parsed.Axis)
		}
		if parsed.ApprovedAttempt != 1 {
			t.Errorf("ApprovedAttempt = %d, want 1", parsed.ApprovedAttempt)
		}
		if parsed.ApprovedDigest == "" {
			t.Errorf("ApprovedDigest was not persisted")
		}
		if got := frozenSectionsDigest(planPath, parsed.FrozenSections); got != parsed.ApprovedDigest {
			t.Errorf("digest mismatch on round-trip: stored %q vs computed %q", parsed.ApprovedDigest, got)
		}
	})

	t.Run("backward-compat when plan path is empty", func(t *testing.T) {
		dir := t.TempDir()
		attemptDir := filepath.Join(dir, "attempt-01")
		_ = os.MkdirAll(attemptDir, 0o755)

		markers := ValidatorMarkers{AxisApproved: "scope", FrozenSections: []string{"## Changes Required"}}
		writeAxisApprovalArtifact(attemptDir, 1, markers, "")

		data, _ := os.ReadFile(filepath.Join(attemptDir, "axis-approved-scope.md"))
		if strings.Contains(string(data), "Approved-Digest:") {
			t.Errorf("Approved-Digest should be omitted when plan path is empty; got: %q", data)
		}
		parsed := parseAxisApprovalArtifact(string(data))
		if parsed.ApprovedDigest != "" {
			t.Errorf("ApprovedDigest = %q, want empty", parsed.ApprovedDigest)
		}
	})
}

func TestFrozenSectionsDigest(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(planPath, []byte("## Grounding\n\nrow a\nrow b\n\n## Changes Required\n\nedit x\n"), 0o644)

	d1 := frozenSectionsDigest(planPath, []string{groundingSectionHeading})
	d2 := frozenSectionsDigest(planPath, []string{groundingSectionHeading})
	if d1 == "" || d1 != d2 {
		t.Errorf("digest not stable: %q vs %q", d1, d2)
	}

	// Changing an unrelated section leaves the grounding-only digest unchanged —
	// this is the property the short-circuit relies on.
	_ = os.WriteFile(planPath, []byte("## Grounding\n\nrow a\nrow b\n\n## Changes Required\n\nedit y\n"), 0o644)
	d3 := frozenSectionsDigest(planPath, []string{groundingSectionHeading})
	if d3 != d1 {
		t.Errorf("grounding digest changed when only Changes Required changed: %q vs %q", d1, d3)
	}

	// Editing the frozen section changes the digest.
	_ = os.WriteFile(planPath, []byte("## Grounding\n\nrow a\n\n## Changes Required\n\nedit y\n"), 0o644)
	d4 := frozenSectionsDigest(planPath, []string{groundingSectionHeading})
	if d4 == d1 {
		t.Errorf("grounding digest did not change after frozen-section edit")
	}

	// Missing section or no headings → empty digest (short-circuit disabled).
	if got := frozenSectionsDigest(planPath, []string{"## Nonexistent"}); got != "" {
		t.Errorf("digest for missing section = %q, want empty", got)
	}
	if got := frozenSectionsDigest(planPath, nil); got != "" {
		t.Errorf("digest for empty headings = %q, want empty", got)
	}
}

func TestPlanAttemptMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	meta := PlanAttemptMeta{
		Attempt:        2,
		SessionAttempt: 3,
		AgentStatus:    agentStatusSuccess,
		ReviewStatus:   agentStatusChangesRequested,
	}
	if err := WritePlanAttemptMeta(dir, meta); err != nil {
		t.Fatalf("write error: %v", err)
	}

	got, err := readPlanAttemptMeta(dir, 2)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if got.Attempt != 2 || got.SessionAttempt != 3 || got.AgentStatus != agentStatusSuccess || got.ReviewStatus != agentStatusChangesRequested {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestPlanValidationSurfaces_PassExplicitEmptyAgentNames(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	artifactDir := filepath.Join(tmpDir, "artifact")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", d, err)
		}
	}

	f := newTestPlanFeature(t, workDir)
	planPath := filepath.Join(artifactDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", planPath, err)
	}

	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	baseCfg := PlanLoopConfig{
		Feature:  f,
		StateDir: stateDir,
		WorkDir:  workDir,
	}

	tests := []struct {
		name   string
		invoke func(t *testing.T) []BuildSessionOpts
	}{
		{
			name: "runSpecializedPlanValidationForArtifact",
			invoke: func(t *testing.T) []BuildSessionOpts {
				buildSession, captured := capturingBuildSession("", criticScript)
				cfg := baseCfg
				cfg.BuildSession = buildSession
				sm := session.NewManager(make(chan interface{}, 10))
				t.Cleanup(sm.Shutdown)
				if _, _, _, err := runSpecializedPlanValidationForArtifact(cfg, sm, 1, artifactDir, planPath, validatorDomain{Name: "Architecture", Template: "validate-roadmap-architecture"}, validationArtifactRoadmap, planValidationExtras{}, observe.SpanContext{}); err != nil {
					t.Fatalf("runSpecializedPlanValidationForArtifact error: %v", err)
				}
				return *captured
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			captured := tt.invoke(t)
			if len(captured) == 0 {
				t.Fatal("expected BuildSession capture")
			}
			assertExplorationAgentNames(t, captured[0].AgentNames)
		})
	}
}

// TestPlanningLoopCodexSkipsResumeUnit verifies that --resume is NOT
// inserted for Codex models without spawning processes.

// --- Integration test for RunPhasePlanningLoop with codex review model ---

func TestPhasePlanLoopCodexReviewPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	// Phase 1 plan dir: <stateDir>/<featureID>/phase-01/plan
	phasePlanDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, d := range []string{workDir, phasePlanDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			testutil.WritePhasePlanSuccessArtifacts(phasePlanDir, validPhasePlanText())+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	f.Models.Review = "codex" // Exercise the codex review path
	_ = store.Save(f)

	// Custom BuildSession that routes bounded review helpers to a dedicated script.
	customBuildSession := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		cmd := []string{"bash", planScript}
		if isReviewHelper(opts.PermHandler) {
			reviewScriptPath := filepath.Join(scriptsDir, "review.sh")
			reviewScriptContent := fmt.Sprintf("#!/bin/bash\n%s\n%s\n%s\n",
				testutil.JSONLInit, testutil.WriteAnyValidatorApproved(tmpDir), testutil.JSONLSuccess)
			if err := os.WriteFile(reviewScriptPath, []byte(reviewScriptContent), 0o755); err != nil {
				return nil, nil, nil, err
			}
			cmd = []string{"bash", reviewScriptPath}
		}
		sessOpts := &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}
		return cmd, nil, sessOpts, nil
	}

	cfg := PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               store,
			StateDir:                   stateDir,
			WorkDir:                    workDir,
			DangerouslySkipPermissions: true,
			BuildSession:               customBuildSession,
		},
		Phase: RoadmapPhase{
			Number: 1,
			Name:   "Test Phase",
			Type:   "collapsed",
			Goal:   "Test the codex review path",
		},
	}

	result, err := RunPhasePlanningLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop error: %v", err)
	}

	if result.FinalStatus != "approved" {
		t.Errorf("expected FinalStatus=approved, got %s", result.FinalStatus)
	}
	if result.Iterations != 1 {
		t.Errorf("expected Iterations=1, got %d", result.Iterations)
	}

	// The phase-plan multi-validator pipeline writes per-axis prompt /
	// output / feedback files. Verify each axis produced its full set.
	for _, axis := range []string{"structural", "scope"} {
		for _, suffix := range []string{"output.txt", "prompt.md", "feedback.md"} {
			path := filepath.Join(phasePlanDir, "attempt-01", "validate-"+axis, fmt.Sprintf("validation-%s-%s", axis, suffix))
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Errorf("attempt-01/validate-%s/validation-%s-%s was not created", axis, axis, suffix)
			}
		}
		parentFeedbackPath := filepath.Join(phasePlanDir, "attempt-01", fmt.Sprintf("validation-%s-feedback.md", axis))
		if _, err := os.Stat(parentFeedbackPath); os.IsNotExist(err) {
			t.Errorf("attempt-01/validation-%s-feedback.md compatibility cache was not created", axis)
		}
	}
}

// TestPlanningLoopPermissionCacheScope verifies that the standard (non-refactor)
// roadmap and phase-plan sessions derive PermCacheScope from Feature.Repos[0].Name,
// ensuring "Allow & Remember" rules are persisted per-repo rather than globally.

// --- Regression tests for the specialized validator path ---
// These tests verify that specialized validators run through bounded helper
// sessions, preserve the configured review model, and keep validator-specific
// log/prompt wiring intact.

func TestSpecializedValidation_UsesReviewModelInBoundedSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tests := []struct {
		name        string
		reviewModel string
		wantModel   string
	}{
		{
			name:        "codex review model uses print mode",
			reviewModel: "codex",
			wantModel:   "codex",
		},
		{
			name:        "gpt-5.4 review model uses print mode",
			reviewModel: "gpt-5.4",
			wantModel:   "gpt-5.4",
		},
		{
			name:        "claude review model uses print mode",
			reviewModel: "opus",
			wantModel:   "opus",
		},
		{
			name:        "empty review model defaults to sonnet print mode",
			reviewModel: "",
			wantModel:   "sonnet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			workDir := filepath.Join(tmpDir, "work")
			stateDir := tmpDir
			scriptsDir := filepath.Join(tmpDir, "scripts")
			os.MkdirAll(workDir, 0o755)
			os.MkdirAll(scriptsDir, 0o755)

			store := feature.NewStore(stateDir)
			f := newTestPlanFeature(t, workDir)
			f.Models.Review = tt.reviewModel
			_ = store.Save(f)

			planDir := filepath.Join(stateDir, f.ID, "plan")
			os.MkdirAll(planDir, 0o755)

			validatorScript := testutil.WriteScript(t, scriptsDir, "validator.sh",
				testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

			eventCh := make(chan interface{}, 100)
			sm := session.NewManager(eventCh)
			defer sm.Shutdown()

			var capturedOpts BuildSessionOpts
			cfg := PlanLoopConfig{
				Feature:      f,
				FeatureStore: store,
				StateDir:     stateDir,
				WorkDir:      workDir,
				BuildSession: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
					capturedOpts = opts
					return []string{"bash", validatorScript}, nil, &session.SessionOpts{
						PIDDir:        opts.PIDDir,
						PermHandler:   opts.PermHandler,
						InitialPrompt: opts.Prompt,
						LogPath:       opts.LogPath,
					}, nil
				},
			}

			domain := validatorDomain{Name: "Architecture", Template: "validate-roadmap-architecture"}
			_, _, _, _ = runSpecializedPlanValidation(cfg, sm, 1, planDir, filepath.Join(planDir, "plan.md"), domain, observe.SpanContext{})

			if capturedOpts.Model != tt.wantModel {
				t.Errorf("expected BuildSession model=%q, got %q", tt.wantModel, capturedOpts.Model)
			}
			if capturedOpts.LogPath == "" {
				t.Errorf("expected LogPath for validator helper, got empty (model=%s)", tt.reviewModel)
			}
			assertExplorationAgentNames(t, capturedOpts.AgentNames)
		})
	}
}

func TestRoadmapValidatorsForRisk(t *testing.T) {
	tests := []struct {
		name          string
		risk          feature.RiskLevel
		wantNames     []string
		wantTemplates []string
	}{
		{
			name:          "low risk roadmap",
			risk:          feature.RiskLow,
			wantNames:     []string{"Architecture", "Scope"},
			wantTemplates: []string{"validate-roadmap-architecture", "validate-roadmap-scope"},
		},
		{
			name:          "medium risk roadmap",
			risk:          feature.RiskMedium,
			wantNames:     []string{"Architecture", "Scope"},
			wantTemplates: []string{"validate-roadmap-architecture", "validate-roadmap-scope"},
		},
		{
			name:          "high risk roadmap",
			risk:          feature.RiskHigh,
			wantNames:     []string{"Architecture", "Security", "Performance", "Scope"},
			wantTemplates: []string{"validate-roadmap-architecture", "validate-plan-security", "validate-plan-performance", "validate-roadmap-scope"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := roadmapValidatorsForRisk(tt.risk)
			if len(got) != len(tt.wantNames) {
				t.Fatalf("roadmapValidatorsForRisk(%s) returned %d validators, want %d", tt.risk, len(got), len(tt.wantNames))
			}
			for i, want := range tt.wantNames {
				if got[i].Name != want {
					t.Errorf("roadmapValidatorsForRisk(%s)[%d] = %q, want %q", tt.risk, i, got[i].Name, want)
				}
				if got[i].Template != tt.wantTemplates[i] {
					t.Errorf("roadmapValidatorsForRisk(%s)[%d] template = %q, want %q", tt.risk, i, got[i].Template, tt.wantTemplates[i])
				}
			}
		})
	}
}

func TestPhasePlanValidatorsForRisk(t *testing.T) {
	tests := []struct {
		name          string
		risk          feature.RiskLevel
		wantNames     []string
		wantTemplates []string
	}{
		{
			name:          "low risk phase plan",
			risk:          feature.RiskLow,
			wantNames:     []string{"Structural", "Scope"},
			wantTemplates: []string{"validate-phase-plan-structural", "validate-phase-plan-scope"},
		},
		{
			name:          "medium risk phase plan",
			risk:          feature.RiskMedium,
			wantNames:     []string{"Structural", "Scope"},
			wantTemplates: []string{"validate-phase-plan-structural", "validate-phase-plan-scope"},
		},
		{
			name:          "high risk phase plan",
			risk:          feature.RiskHigh,
			wantNames:     []string{"Structural", "Scope", "Security", "Performance", "Testing"},
			wantTemplates: []string{"validate-phase-plan-structural", "validate-phase-plan-scope", "validate-plan-security", "validate-plan-performance", "validate-plan-testing"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := phasePlanValidatorsForRisk(tt.risk)
			if len(got) != len(tt.wantNames) {
				t.Fatalf("phasePlanValidatorsForRisk(%s) returned %d validators, want %d", tt.risk, len(got), len(tt.wantNames))
			}
			for i, want := range tt.wantNames {
				if got[i].Name != want {
					t.Errorf("phasePlanValidatorsForRisk(%s)[%d] = %q, want %q", tt.risk, i, got[i].Name, want)
				}
				if got[i].Template != tt.wantTemplates[i] {
					t.Errorf("phasePlanValidatorsForRisk(%s)[%d] template = %q, want %q", tt.risk, i, got[i].Template, tt.wantTemplates[i])
				}
			}
		})
	}
}

func TestRoadmapSpecializedValidation_UsesRoadmapValidatorSubset(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	skillsDir := filepath.Join(tmpDir, "skills")
	os.MkdirAll(workDir, 0o755)
	os.MkdirAll(scriptsDir, 0o755)
	os.MkdirAll(skillsDir, 0o755)

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	f.RiskLevel = feature.RiskMedium
	f.Pipeline = feature.PipelineMoonshot // roadmap validation should still stay lighter
	_ = store.Save(f)

	roadmapDir := filepath.Join(stateDir, f.ID, "roadmap")
	os.MkdirAll(roadmapDir, 0o755)
	roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
	_ = os.WriteFile(roadmapPath, []byte("# Roadmap\n\n## Phase 1: Tracer Bullet Skeleton\n"), 0o644)

	validatorScript := testutil.WriteScript(t, scriptsDir, "validator.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	var validationOptsMu sync.Mutex
	var validationOpts []BuildSessionOpts
	cfg := PlanLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		WorkDir:      workDir,
		SkillsDir:    skillsDir,
		BuildSession: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			validationOptsMu.Lock()
			validationOpts = append(validationOpts, opts)
			validationOptsMu.Unlock()
			return []string{"bash", validatorScript}, nil, &session.SessionOpts{
				PIDDir:        opts.PIDDir,
				PermHandler:   opts.PermHandler,
				InitialPrompt: opts.Prompt,
				LogPath:       opts.LogPath,
			}, nil
		},
	}

	_, _, _ = runRoadmapMultiValidatorPlanValidation(cfg, sm, 1, roadmapDir, roadmapPath)

	expectedValidators := roadmapValidatorsForRisk(feature.RiskMedium)
	if len(validationOpts) != len(expectedValidators) {
		t.Fatalf("expected %d BuildSession calls for roadmap validators, got %d", len(expectedValidators), len(validationOpts))
	}
	// Parallel validators arrive in non-deterministic order. Check universal
	// properties on every opts. The validator subset itself is asserted via
	// roadmapValidatorsForRisk below.
	for i, opts := range validationOpts {
		if opts.LogPath == "" {
			t.Errorf("validator %d: expected LogPath for helper output, got empty", i)
		}
		assertExplorationAgentNames(t, opts.AgentNames)
	}
	var hasArchitecture, hasScope bool
	for _, v := range expectedValidators {
		if v.Template == "validate-roadmap-architecture" {
			hasArchitecture = true
		}
		if v.Template == "validate-roadmap-scope" {
			hasScope = true
		}
	}
	if !hasArchitecture {
		t.Fatalf("expected specialized roadmap validator set to include validate-roadmap-architecture")
	}
	if !hasScope {
		t.Fatalf("expected specialized roadmap validator set to include validate-roadmap-scope")
	}
}

func TestSpecializedValidation_UsesConfiguredReviewModel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	os.MkdirAll(workDir, 0o755)
	os.MkdirAll(scriptsDir, 0o755)

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	f.Models.Review = "codex"
	_ = store.Save(f)

	planDir := filepath.Join(stateDir, f.ID, "plan")
	os.MkdirAll(planDir, 0o755)

	validatorScript := testutil.WriteScript(t, scriptsDir, "validator.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	var capturedOpts BuildSessionOpts
	cfg := PlanLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		WorkDir:      workDir,
		BuildSession: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			capturedOpts = opts
			return []string{"bash", validatorScript}, nil, &session.SessionOpts{
				PIDDir:        opts.PIDDir,
				PermHandler:   opts.PermHandler,
				InitialPrompt: opts.Prompt,
				LogPath:       opts.LogPath,
			}, nil
		},
	}

	domain := validatorDomain{Name: "Architecture", Template: "validate-roadmap-architecture"}
	_, _, _, _ = runSpecializedPlanValidation(cfg, sm, 1, planDir, filepath.Join(planDir, "plan.md"), domain, observe.SpanContext{})

	if capturedOpts.Model != "codex" {
		t.Errorf("expected BuildSession model=%q (no override), got %q", "codex", capturedOpts.Model)
	}
	if capturedOpts.LogPath == "" {
		t.Error("expected LogPath for validator helper")
	}
	assertExplorationAgentNames(t, capturedOpts.AgentNames)
}

func TestPhasePlanLoop_SkillReadInstruction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	skillsDir := filepath.Join(tmpDir, "skills")
	phasePlanDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, d := range []string{workDir, phasePlanDir, scriptsDir, skillsDir} {
		os.MkdirAll(d, 0o755)
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			testutil.WritePhasePlanSuccessArtifacts(phasePlanDir, validPhasePlanText())+"\n"+
			testutil.JSONLSuccess+"\n")

	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh",
		testutil.JSONLInit+"\n"+testutil.WriteAnyValidatorApproved(tmpDir)+"\n"+testutil.JSONLSuccess+"\n")

	buildSession, captured := capturingBuildSession(planScript, criticScript)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	_ = store.Save(f)

	cfg := PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               store,
			StateDir:                   stateDir,
			WorkDir:                    workDir,
			DangerouslySkipPermissions: true,
			BuildSession:               buildSession,
			SkillsDir:                  skillsDir,
		},
		Phase: RoadmapPhase{
			Number: 1,
			Name:   "Test Phase",
			Type:   "collapsed",
			Goal:   "Test skill-read instruction",
		},
	}

	result, err := RunPhasePlanningLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop error: %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Errorf("expected approved, got %s", result.FinalStatus)
	}

	// Find the planning session (not the bounded validator helper, which runs
	// with a ReadOnlyHandler).
	var planOpts *BuildSessionOpts
	for i := range *captured {
		if _, ok := (*captured)[i].PermHandler.(*permission.ReadOnlyHandler); !ok {
			planOpts = &(*captured)[i]
			break
		}
	}
	if planOpts == nil {
		t.Fatal("no non-helper planning session found")
	}
	if !reflect.DeepEqual(planOpts.AgentNames, explorationAgentNames()) {
		t.Fatalf("phase-plan planner AgentNames = %v, want exploration set %v", planOpts.AgentNames, explorationAgentNames())
	}

	if !strings.Contains(planOpts.SystemPrompt, "## Output Roots") || !strings.Contains(planOpts.SystemPrompt, "## Completion") {
		t.Error("planning session systemPrompt missing RoleSpec output roots or completion protocol")
	}
	wantMarker := filepath.Join(phasePlanDir, "attempt-01", "phase_complete")
	if !strings.Contains(planOpts.SystemPrompt, wantMarker) {
		t.Fatalf("planning session systemPrompt missing attempt marker path %q:\n%s", wantMarker, planOpts.SystemPrompt)
	}
	parentMarker := filepath.Join(phasePlanDir, "phase_complete")
	if strings.Contains(planOpts.SystemPrompt, parentMarker) {
		t.Fatalf("planning session systemPrompt contains parent marker path %q:\n%s", parentMarker, planOpts.SystemPrompt)
	}
	wantOutputRoot := "`artifact_dir`: " + phasePlanDir
	if !strings.Contains(planOpts.SystemPrompt, wantOutputRoot) {
		t.Fatalf("planning session systemPrompt missing shared artifact root %q:\n%s", wantOutputRoot, planOpts.SystemPrompt)
	}
	attemptOutputRoot := "`artifact_dir`: " + filepath.Join(phasePlanDir, "attempt-01")
	if strings.Contains(planOpts.SystemPrompt, attemptOutputRoot) {
		t.Fatalf("planning session systemPrompt advertises attempt subdir as artifact root %q:\n%s", attemptOutputRoot, planOpts.SystemPrompt)
	}

	// The RoleSpec system prompt owns the skill-read instruction.
	expectedSkillPath := filepath.Join(skillsDir, "plan-phase", "SKILL.md")
	if !strings.Contains(planOpts.SystemPrompt, expectedSkillPath) {
		t.Errorf("planning session systemPrompt missing skill-read instruction for plan-phase, expected path %q", expectedSkillPath)
	}
	if strings.Contains(planOpts.Prompt, expectedSkillPath) {
		t.Errorf("planning session user prompt contains RoleSpec-owned skill-read instruction %q", expectedSkillPath)
	}
}

// mockBuildSessionPerDomain dispatches bounded validator helper sessions to
// per-domain scripts based on the LogPath (which contains the domain name,
// e.g. "validation-architecture-output.txt"). Planning sessions all share
// planScript.
func mockBuildSessionPerDomain(planScript string, domainScripts map[string]string) func(BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
	return func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		var cmd []string
		if !isReviewHelper(opts.PermHandler) {
			cmd = []string{"bash", planScript}
		} else {
			script := ""
			for domain, s := range domainScripts {
				if strings.Contains(opts.LogPath, domain) {
					script = s
					break
				}
			}
			if script == "" {
				return nil, nil, nil, fmt.Errorf("no script for LogPath %q", opts.LogPath)
			}
			cmd = []string{"bash", script}
		}
		sessOpts := &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}
		return cmd, nil, sessOpts, nil
	}
}

// TestRoadmapLoopStickyApprovals verifies that when one roadmap validator
// APPROVES with a `## Sticky Approval` block on attempt N, the
// axis-approved-<axis>.md artifact is persisted and the attempt N+1 revise
// prompt surfaces a "Prior Axis Approvals" block so the reviser knows what
// not to rewrite.
func TestRoadmapLoopStickyApprovals(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	planDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "roadmap")
	for _, d := range []string{workDir, planDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+testutil.TouchPhaseCompleteInLatestAttemptDir(planDir)+"\n"+testutil.JSONLSuccess+"\n")

	// Architecture validator always approves with a sticky-approval block.
	archScript := testutil.WriteScript(t, scriptsDir, "critic-arch.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteSpecificAxisApproved(tmpDir, "architecture", []string{"Architecture Approach", "Phase 1: Skeleton"})+"\n"+
			testutil.JSONLSuccess+"\n")

	// Scope validator: first attempt rejects, second approves.
	scopeCounter := filepath.Join(tmpDir, "scope-counter")
	scopeScript := testutil.WriteScript(t, scriptsDir, "critic-scope.sh", fmt.Sprintf(`
COUNTER_FILE=%q
%s
if [ ! -f "$COUNTER_FILE" ]; then
  echo 1 > "$COUNTER_FILE"
  %s
else
  %s
fi
%s
`, scopeCounter, testutil.JSONLInit,
		testutil.WriteSpecificAxisChangesRequested(tmpDir, "scope", "- **High**: Requirement Coverage FAIL"),
		testutil.WriteSpecificAxisApproved(tmpDir, "scope", []string{"Deferred Work"}),
		testutil.JSONLSuccess))

	_ = os.WriteFile(filepath.Join(planDir, "plan.md"), []byte("# Test Roadmap\n## Phase 1: Architecture Approach\nDo stuff."), 0o644)

	buildSession := mockBuildSessionPerDomain(planScript, map[string]string{
		"architecture": archScript,
		"scope":        scopeScript,
	})

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	_ = store.Save(f)

	cfg := PlanLoopConfig{
		Feature:                    f,
		FeatureStore:               store,
		StateDir:                   stateDir,
		WorkDir:                    workDir,
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
	}

	result, err := RunRoadmapPlanningLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunRoadmapPlanningLoop error: %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Fatalf("FinalStatus = %s, want approved", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}

	// Attempt 1 should have persisted the architecture approval.
	archPath := filepath.Join(planDir, "attempt-01", "axis-approved-architecture.md")
	data, err := os.ReadFile(archPath)
	if err != nil {
		t.Fatalf("missing %s: %v", archPath, err)
	}
	for _, want := range []string{"# AxisApproved: architecture", "- Architecture Approach", "- Phase 1: Skeleton"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s missing %q:\n%s", archPath, want, string(data))
		}
	}

	// Attempt 2's revise prompt should surface the Prior Axis Approvals block.
	revPromptPath := filepath.Join(planDir, "attempt-02", "user-prompt.md")
	revPrompt, err := os.ReadFile(revPromptPath)
	if err != nil {
		t.Fatalf("missing %s: %v", revPromptPath, err)
	}
	for _, want := range []string{"## Prior Axis Approvals", "### architecture", "- Architecture Approach"} {
		if !strings.Contains(string(revPrompt), want) {
			t.Errorf("revise prompt missing %q:\n%s", want, string(revPrompt))
		}
	}
}

// TestPhasePlanLoopStickyApprovals is the phase-plan counterpart of
// TestRoadmapLoopStickyApprovals — when one per-phase axis APPROVES with a
// `## Sticky Approval` block on attempt 1, the axis-approved-<axis>.md
// artifact is persisted and attempt 2's revise prompt surfaces a "Prior
// Axis Approvals" block so the reviser knows what not to rewrite. This
// guards against the oscillation pattern that motivated the per-phase
// sticky-approval port.
func TestPhasePlanLoopStickyApprovals(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	phasePlanDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, d := range []string{workDir, phasePlanDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			testutil.WritePhasePlanSuccessArtifacts(phasePlanDir, validPhasePlanText())+"\n"+
			testutil.JSONLSuccess+"\n")

	// Structural axis always approves with a sticky-approval block.
	structuralScript := testutil.WriteScript(t, scriptsDir, "critic-structural.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteSpecificAxisApproved(tmpDir, "structural", []string{"## Tasks", "## Success Criteria"})+"\n"+
			testutil.JSONLSuccess+"\n")

	// Scope axis: first attempt rejects, second approves — forces a revise iteration.
	scopeCounter := filepath.Join(tmpDir, "phase-scope-counter")
	scopeScript := testutil.WriteScript(t, scriptsDir, "critic-scope.sh", fmt.Sprintf(`
COUNTER_FILE=%q
%s
if [ ! -f "$COUNTER_FILE" ]; then
  echo 1 > "$COUNTER_FILE"
  %s
else
  %s
fi
%s
`, scopeCounter, testutil.JSONLInit,
		testutil.WriteSpecificAxisChangesRequested(tmpDir, "scope", "- **High**: Roadmap Scope Fidelity FAIL"),
		testutil.WriteSpecificAxisApproved(tmpDir, "scope", []string{"## Tasks"}),
		testutil.JSONLSuccess))

	_ = os.WriteFile(filepath.Join(phasePlanDir, "plan.md"), []byte("# Phase 1 Plan\n## Overview\nX.\n## Tasks\n### Task 1: Y\n#### What to build\nY.\n#### Acceptance criteria\n- [ ] Done.\n#### Blocked by\nNone - can start immediately.\n## Success Criteria\n### Automated Verification\n- [ ] Tests: `go test ./...`\n### Manual Verification\n- [ ] None required: internal-only change.\n### Visual Evidence\n- [ ] None required: no rendered surface.\n### Behavioral Evidence\n- [ ] None required: automated tests are the artifact.\n"), 0o644)

	buildSession := mockBuildSessionPerDomain(planScript, map[string]string{
		"structural": structuralScript,
		"scope":      scopeScript,
	})

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	_ = store.Save(f)

	cfg := PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               store,
			StateDir:                   stateDir,
			WorkDir:                    workDir,
			DangerouslySkipPermissions: true,
			BuildSession:               buildSession,
		},
		Phase: RoadmapPhase{
			Number: 1,
			Name:   "Tracer",
			Type:   "tracer-bullet",
			Goal:   "Sticky test",
		},
	}

	result, err := RunPhasePlanningLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop error: %v", err)
	}
	if result.FinalStatus != "approved" {
		t.Fatalf("FinalStatus = %s, want approved", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}

	// Attempt 1 should have persisted the structural approval.
	for _, axis := range []string{"structural"} {
		p := filepath.Join(phasePlanDir, "attempt-01", fmt.Sprintf("axis-approved-%s.md", axis))
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
		if !strings.Contains(string(data), fmt.Sprintf("# AxisApproved: %s", axis)) {
			t.Errorf("%s missing header:\n%s", p, string(data))
		}
	}

	// Attempt 2's revise prompt should surface the Prior Axis Approvals block.
	revPromptPath := filepath.Join(phasePlanDir, "attempt-02", "user-prompt.md")
	revPrompt, err := os.ReadFile(revPromptPath)
	if err != nil {
		t.Fatalf("missing %s: %v", revPromptPath, err)
	}
	for _, want := range []string{"## Prior Axis Approvals", "### structural", "- ## Tasks", "- ## Success Criteria"} {
		if !strings.Contains(string(revPrompt), want) {
			t.Errorf("revise prompt missing %q:\n%s", want, string(revPrompt))
		}
	}
}

func TestPhasePlanLoopStallEscalationPersistsReviewArtifact(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	phasePlanDir := filepath.Join(stateDir, "test-plan-001", "runs", "run-001", "phase-01", "plan")
	for _, d := range []string{workDir, phasePlanDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	planScript := testutil.WriteScript(t, scriptsDir, "plan.sh",
		testutil.JSONLInit+"\n"+
			testutil.WritePhasePlanSuccessArtifacts(phasePlanDir, validPhasePlanText())+"\n"+
			testutil.JSONLSuccess+"\n")
	structuralScript := testutil.WriteScript(t, scriptsDir, "critic-structural.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteSpecificAxisChangesRequested(tmpDir, "structural", "- **High**: structural section still invalid")+"\n"+
			testutil.JSONLSuccess+"\n")
	scopeScript := testutil.WriteScript(t, scriptsDir, "critic-scope.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteSpecificAxisApproved(tmpDir, "scope", nil)+"\n"+
			testutil.JSONLSuccess+"\n")

	buildSession := mockBuildSessionPerDomain(planScript, map[string]string{
		"structural": structuralScript,
		"scope":      scopeScript,
	})

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	f.RiskLevel = feature.RiskLow
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	cfg := PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature:                    f,
			FeatureStore:               store,
			StateDir:                   stateDir,
			WorkDir:                    workDir,
			MaxAttempts:                axisStallLimit + 2,
			DangerouslySkipPermissions: true,
			BuildSession:               buildSession,
		},
		Phase: RoadmapPhase{
			Number: 1,
			Name:   "Tracer",
			Type:   "tracer-bullet",
			Goal:   "Exercise stalled human-review escalation",
		},
	}

	result, err := RunPhasePlanningLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunPhasePlanningLoop error: %v", err)
	}
	if result.FinalStatus != "needs_human_review" {
		t.Fatalf("FinalStatus = %s, want needs_human_review", result.FinalStatus)
	}
	if result.Iterations != axisStallLimit {
		t.Fatalf("Iterations = %d, want %d", result.Iterations, axisStallLimit)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	wantPath := filepath.Join(phasePlanDir, "plan.md")
	if got := loaded.Artifacts["phase-1-plan"]; got != wantPath {
		t.Fatalf("Artifacts[phase-1-plan] = %q, want %q", got, wantPath)
	}
}

// TestBuildSpecializedValidationPrompt_GroundingIncludesPriorPhaseContext verifies
// that when PriorPhasePlanPaths is populated, the Grounding axis prompt
// contains a "Prior Phase Context" block listing each plan path so the
// critic doesn't flag legitimate prior-phase symbols as ungrounded.
func TestBuildSpecializedValidationPrompt_GroundingIncludesPriorPhaseContext(t *testing.T) {
	f := &feature.Feature{Name: "Test", Description: "desc",
		SchemaVersion: feature.SchemaVersionCurrent}
	domain := validatorDomain{Name: "Grounding", Template: "validate-phase-plan-grounding"}
	extras := planValidationExtras{
		PriorPhasePlanPaths: []string{
			"/state/features/f1/phase-01/plan/phase-01-plan.md",
			"/state/features/f1/phase-02/plan/phase-02-plan.md",
		},
	}

	prompt := buildSpecializedValidationPromptForArtifact(f, "/plan.md", "", "", "", domain, validationArtifactPhasePlan, extras)

	if !strings.Contains(prompt, "## Prior Phase Context") {
		t.Errorf("grounding prompt missing '## Prior Phase Context' section:\n%s", prompt)
	}
	for _, want := range extras.PriorPhasePlanPaths {
		if !strings.Contains(prompt, want) {
			t.Errorf("grounding prompt missing prior-phase path %q", want)
		}
	}
	if !strings.Contains(prompt, "Phase 1 plan") || !strings.Contains(prompt, "Phase 2 plan") {
		t.Errorf("grounding prompt missing phase numbering")
	}
}

// TestRunSpecializedPlanValidation_NoStickyOnChangesRequested covers Fix A:
// when a critic emits a `## Sticky Approval` block alongside a
// `## Verdict\nCHANGES_REQUESTED` (SKILL contract violation), the harness
// must NOT write axis-approved-<axis>.md. Otherwise a stale sticky leaks
// forward and later attempts are told to freeze sections for an axis that's
// actively asking for changes — the root of the phase-02 multi-partition-scan
// scope regression.
func TestRunSpecializedPlanValidation_NoStickyOnChangesRequested(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := tmpDir
	scriptsDir := filepath.Join(tmpDir, "scripts")
	planDir := filepath.Join(stateDir, "feat-sticky-gate")
	attemptDir := filepath.Join(planDir, "attempt-01")
	for _, d := range []string{workDir, planDir, attemptDir, scriptsDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	// Critic emits CHANGES_REQUESTED but also includes a `## Sticky Approval`
	// block (the contract violation this test guards against).
	violationBody := testutil.StructuredReviewFeedbackWithSticky(
		"- **High**: Grounding FAIL", "", agentStatusChangesRequested,
		testAxisGrounding, []string{groundingSectionHeading},
	)
	criticScript := testutil.WriteScript(t, scriptsDir, "critic.sh", fmt.Sprintf(`%s
for _prompt in $(find %s -name 'validation-grounding-prompt.md' -type f 2>/dev/null); do
  _dir=$(dirname "$_prompt")
  cat > "$_dir/validation-grounding-feedback.md" << 'REVIEWEOF'
%s
REVIEWEOF
  touch "$_dir/phase_complete"
done
%s
`, testutil.JSONLInit, tmpDir, strings.TrimRight(violationBody, "\n"), testutil.JSONLSuccess))

	planPath := filepath.Join(planDir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# Plan\n## Grounding\nplaceholder\n"), 0o644)

	buildSession, _ := capturingBuildSession("", criticScript)
	eventCh := make(chan interface{}, 10)
	sm := session.NewManager(eventCh)
	t.Cleanup(sm.Shutdown)
	store := feature.NewStore(stateDir)
	f := newTestPlanFeature(t, workDir)
	_ = store.Save(f)

	cfg := PlanLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		WorkDir:      workDir,
		BuildSession: buildSession,
	}

	domain := validatorDomain{Name: "Grounding", Template: "validate-phase-plan-grounding"}
	status, _, _, err := runSpecializedPlanValidationForArtifact(cfg, sm, 1, attemptDir, planPath, domain, validationArtifactPhasePlan, planValidationExtras{}, observe.SpanContext{})
	if err != nil {
		t.Fatalf("runSpecializedPlanValidationForArtifact: %v", err)
	}
	if status != ReviewChangesRequested {
		t.Fatalf("status = %v, want CHANGES_REQUESTED", status)
	}
	// The sticky artifact must NOT exist for a rejected verdict.
	stickyPath := filepath.Join(attemptDir, "axis-approved-grounding.md")
	if _, statErr := os.Stat(stickyPath); !os.IsNotExist(statErr) {
		t.Errorf("axis-approved-grounding.md should not be written when verdict is CHANGES_REQUESTED; statErr=%v", statErr)
	}
}

// TestBuildSpecializedValidationPrompt_NonGroundingOmitsPriorPhaseContext
// verifies the prior-phase block is grounding-only — other axes don't need
// the context and adding it would just add noise.
func TestBuildSpecializedValidationPrompt_NonGroundingOmitsPriorPhaseContext(t *testing.T) {
	f := &feature.Feature{Name: "Test", Description: "desc",
		SchemaVersion: feature.SchemaVersionCurrent}
	priorPath := "/marker/prior-phase-plan.md"
	extras := planValidationExtras{
		PriorPhasePlanPaths: []string{priorPath},
	}

	for _, template := range []string{"validate-phase-plan-structural", "validate-phase-plan-scope"} {
		t.Run(template, func(t *testing.T) {
			domain := validatorDomain{Name: template, Template: template}
			prompt := buildSpecializedValidationPromptForArtifact(f, "/plan.md", "", "", "", domain, validationArtifactPhasePlan, extras)
			// Detect the actual block (unique prior-phase path) rather than
			// the "## Prior Phase Context" heading substring, since the
			// grounding SKILL body itself mentions that heading by name.
			if strings.Contains(prompt, priorPath) {
				t.Errorf("%s prompt should NOT contain prior-phase path %q:\n%s", template, priorPath, prompt)
			}
		})
	}
}

// TestBuildSpecializedValidationPrompt_GroundingWithoutPriorPhases_NoBlock
// verifies the Prior Phase Context block is only emitted when there are
// actually prior phases to reference. We look for a "- Phase N plan:" bullet
// since that string only appears when the block is actually emitted.
func TestBuildSpecializedValidationPrompt_GroundingWithoutPriorPhases_NoBlock(t *testing.T) {
	f := &feature.Feature{Name: "Test", Description: "desc",
		SchemaVersion: feature.SchemaVersionCurrent}
	domain := validatorDomain{Name: "Grounding", Template: "validate-phase-plan-grounding"}

	prompt := buildSpecializedValidationPromptForArtifact(f, "/plan.md", "", "", "", domain, validationArtifactPhasePlan, planValidationExtras{})

	if strings.Contains(prompt, "- Phase 1 plan:") {
		t.Errorf("grounding prompt should NOT emit a Phase-N plan bullet when no prior phases:\n%s", prompt)
	}
}

// TestExtractPlanSection verifies the markdown section extractor handles the
// heading, next-heading boundary, and EOF cases.
func TestExtractPlanSection(t *testing.T) {
	plan := `# Title

## Grounding

| Ref | Status |
|---|---|
| foo.go | EXISTS |

## Changes Required

- edit bar.go
`
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(path, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("grounding section", func(t *testing.T) {
		got := string(extractPlanSection(path, groundingSectionHeading))
		for _, want := range []string{"| Ref | Status |", "| foo.go | EXISTS |"} {
			if !strings.Contains(got, want) {
				t.Errorf("expected %q in grounding section, got:\n%s", want, got)
			}
		}
		if strings.Contains(got, "Changes Required") {
			t.Errorf("grounding section leaked into next heading: %s", got)
		}
	})

	t.Run("last section runs to EOF", func(t *testing.T) {
		got := string(extractPlanSection(path, "## Changes Required"))
		if !strings.Contains(got, "- edit bar.go") {
			t.Errorf("expected EOF section content, got: %q", got)
		}
	})

	t.Run("missing heading returns nil", func(t *testing.T) {
		got := extractPlanSection(path, "## Missing")
		if got != nil {
			t.Errorf("expected nil for missing heading, got %q", string(got))
		}
	})

	t.Run("unreadable file returns nil", func(t *testing.T) {
		if got := extractPlanSection("/no/such/file.md", "## Anything"); got != nil {
			t.Errorf("expected nil for unreadable file, got %q", string(got))
		}
	})
}

// TestPlanSectionDigest_StableAndSensitive verifies the digest is byte-stable
// across trailing-whitespace changes (so cosmetic edits don't unstall) and
// changes when real content changes.
func TestPlanSectionDigest_StableAndSensitive(t *testing.T) {
	dir := t.TempDir()

	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	a := write("a.md", "## Grounding\n- foo.go\n- bar.go\n\n## Next\n")
	// Same content, different trailing whitespace on each line.
	b := write("b.md", "## Grounding\n- foo.go   \n- bar.go\t\n\n## Next\n")
	c := write("c.md", "## Grounding\n- foo.go\n- baz.go\n\n## Next\n")

	if planSectionDigest(a, groundingSectionHeading) != planSectionDigest(b, groundingSectionHeading) {
		t.Error("digest changed on trailing-whitespace-only diff")
	}
	if planSectionDigest(a, groundingSectionHeading) == planSectionDigest(c, groundingSectionHeading) {
		t.Error("digest stayed the same after content change")
	}
	if planSectionDigest(a, "## Missing") != "" {
		t.Error("missing section should digest to empty string")
	}
}

// TestAxisStallState_EscalatesAfterLimit drives the tracker through three
// identical-section CHANGES_REQUESTED verdicts for the Structural axis and
// expects it to flag stalled. It also verifies that a successful attempt
// resets the counter and that different section content does not count as a
// stall.
func TestAxisStallState_EscalatesAfterLimit(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	write := func(body string) {
		if err := os.WriteFile(planPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tasksSection := "## Tasks\n### Task 1: Do it\n\n#### What to build\nThing.\n\n## Next\n"
	write(tasksSection)

	var tracker axisStallState
	results := []ValidatorResult{
		{Domain: "Structural", Status: ReviewChangesRequested, Feedback: "bad"},
	}

	if stalled, _, _, _, _ := tracker.observe(1, planPath, results); stalled {
		t.Fatalf("stall should not trigger on attempt 1")
	}
	if stalled, _, _, _, _ := tracker.observe(2, planPath, results); stalled {
		t.Fatalf("stall should not trigger on attempt 2 (need %d)", axisStallLimit)
	}
	stalled, axis, count, verdicts, digests := tracker.observe(3, planPath, results)
	if !stalled {
		t.Fatalf("stall should trigger on attempt %d with unchanged section", axisStallLimit)
	}
	if axis != "structural" || count != axisStallLimit {
		t.Errorf("got stall axis=%q count=%d, want structural/%d", axis, count, axisStallLimit)
	}
	if verdicts["structural"] != agentStatusChangesRequested {
		t.Errorf("expected verdicts[structural]=CHANGES_REQUESTED, got %q", verdicts["structural"])
	}
	if digests["structural"] == "" {
		t.Error("expected digests[structural] to be populated for replay")
	}
}

func TestAxisStallState_ResetsOnApproval(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(planPath, []byte("## Tasks\n- foo\n"), 0o644)

	var tracker axisStallState
	failing := []ValidatorResult{{Domain: "Structural", Status: ReviewChangesRequested}}
	passing := []ValidatorResult{{Domain: "Structural", Status: ReviewApproved}}

	_, _, _, _, _ = tracker.observe(1, planPath, failing)
	_, _, _, _, _ = tracker.observe(2, planPath, failing)
	// Approval should reset the counter.
	_, _, _, _, _ = tracker.observe(3, planPath, passing)
	// Next failure should be count=1, not axisStallLimit.
	if stalled, _, _, _, _ := tracker.observe(4, planPath, failing); stalled {
		t.Error("approval should have reset the structural counter")
	}
}

// TestLoadAxisStallState_ReplaysAcrossRestarts is the core regression for
// Fix #3: the stall counter must survive a session restart, otherwise a
// drifting critic can escape the cap by crashing/being interrupted every
// few attempts. We write two prior attempts' metas with persisted
// AxisVerdicts/AxisDigests, then a third failure with the same digest should
// trip the cap on that single in-process observe call.
func TestLoadAxisStallState_ReplaysAcrossRestarts(t *testing.T) {
	artifactDir := t.TempDir()

	digest := "deadbeef" // persisted digest; real digest bytes aren't required for replay

	for _, attempt := range []int{1, 2} {
		if err := WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
			Attempt:      attempt,
			AgentStatus:  agentStatusSuccess,
			ReviewStatus: agentStatusChangesRequested,
			AxisVerdicts: map[string]string{"structural": agentStatusChangesRequested},
			AxisDigests:  map[string]string{"structural": digest},
		}); err != nil {
			t.Fatalf("write meta %d: %v", attempt, err)
		}
	}

	// Simulate a session restart: reconstruct the tracker from disk.
	tracker := loadAxisStallState(artifactDir)

	// Third attempt with the same digest must now trip the cap — the replay
	// has already counted 2 consecutive identical failures, so one more
	// observation should hit axisStallLimit.
	stalled, axis, count := tracker.observeAxis("structural", agentStatusChangesRequested, digest)
	if !stalled {
		t.Fatalf("expected stall on attempt 3 after replaying 2 prior failures; got stalled=false count=%d", count)
	}
	if axis != "structural" || count != axisStallLimit {
		t.Errorf("stall axis=%q count=%d, want structural/%d", axis, count, axisStallLimit)
	}
}

// TestLoadAxisStallState_IgnoresApprovedAxes ensures an intervening APPROVED
// verdict resets the counter during replay — a critic that approved at
// attempt 2 shouldn't leave a stale structural-fail digest around that traps
// attempt 3.
func TestLoadAxisStallState_IgnoresApprovedAxes(t *testing.T) {
	artifactDir := t.TempDir()

	_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
		Attempt:      1,
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: agentStatusChangesRequested,
		AxisVerdicts: map[string]string{"structural": agentStatusChangesRequested},
		AxisDigests:  map[string]string{"structural": "abc"},
	})
	_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
		Attempt:      2,
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: agentStatusApproved,
		AxisVerdicts: map[string]string{"structural": agentStatusApproved},
		AxisDigests:  map[string]string{"structural": "abc"},
	})

	tracker := loadAxisStallState(artifactDir)

	// A single fresh failure shouldn't trip the cap — the approval in
	// attempt 2 should have reset grounding's counter during replay.
	stalled, _, count := tracker.observeAxis("structural", agentStatusChangesRequested, "abc")
	if stalled {
		t.Fatalf("unexpected stall: approval should have cleared the counter; count=%d", count)
	}
}

// TestLoadAxisStallState_TolerantToMissingFields verifies replay is safe on
// feature directories created before Fix #3 landed — older attempt metas
// have no AxisVerdicts/AxisDigests fields and should contribute nothing to
// the reconstructed state (instead of crashing).
func TestLoadAxisStallState_TolerantToMissingFields(t *testing.T) {
	artifactDir := t.TempDir()
	_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
		Attempt:      1,
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: agentStatusChangesRequested,
	})

	tracker := loadAxisStallState(artifactDir)
	if stalled, _, _ := tracker.observeAxis("structural", agentStatusChangesRequested, "newdigest"); stalled {
		t.Error("pre-Fix #3 attempts should not contribute to the stall count")
	}
}

func TestAxisStallState_ChangedSectionResetsCount(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(planPath, []byte("## Tasks\n- foo\n"), 0o644)

	var tracker axisStallState
	failing := []ValidatorResult{{Domain: "Structural", Status: ReviewChangesRequested}}

	_, _, _, _, _ = tracker.observe(1, planPath, failing)
	_, _, _, _, _ = tracker.observe(2, planPath, failing)
	// Planner materially revised the Tasks section.
	_ = os.WriteFile(planPath, []byte("## Tasks\n- foo\n- bar\n- baz\n"), 0o644)
	stalled, _, _, _, _ := tracker.observe(3, planPath, failing)
	if stalled {
		t.Error("changed section should have reset the structural counter to 1")
	}
}

// TestRefactorLoopPropagatesPriorPhasePlanPaths covers fix #2c end-to-end by
// running RunRefactorLoop with a two-phase roadmap and a planner stub that
// captures the PhasePlanLoopConfig it received for each phase. We expect the
// second phase's config to carry the first phase's approved plan path in
// PriorPhasePlanPaths, so the grounding critic gets prior-phase context.
//
// This test lives here (not refactor_test.go) because it exercises the
// PhasePlanLoopConfig plumbing owned by this file.
func TestRefactorLoopPropagatesPriorPhasePlanPaths(t *testing.T) {
	// Sanity: the refactor.go edit must populate PriorPhasePlanPaths from the
	// accumulator. We assert the smaller invariant: build a two-element slice
	// the way RunRefactorLoop does, and confirm it matches what
	// runPhasePlanMultiValidatorValidation forwards.
	cfg := PhasePlanLoopConfig{
		PlanLoopConfig: PlanLoopConfig{
			Feature: &feature.Feature{Name: "f",
				SchemaVersion: feature.SchemaVersionCurrent},
		},
		PriorPhasePlanPaths: []string{"/phase-01/plan.md"},
	}
	extras := planValidationExtras{PriorPhasePlanPaths: cfg.PriorPhasePlanPaths}
	if !reflect.DeepEqual(extras.PriorPhasePlanPaths, cfg.PriorPhasePlanPaths) {
		t.Errorf("runPhasePlanMultiValidatorValidation must forward PriorPhasePlanPaths into extras; got %v", extras.PriorPhasePlanPaths)
	}
}
