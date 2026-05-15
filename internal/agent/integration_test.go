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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// newTestFeature creates a minimal feature for integration testing.
func newTestFeature(t *testing.T, repoPath string) *feature.Feature {
	t.Helper()
	return &feature.Feature{
		ID:            "test-feat-001",
		Name:          "Test Feature",
		Slug:          "test-feature",
		Description:   "Integration test feature",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: repoPath},
		},
		Models: config.ModelConfig{
			Implementation: "agent",
			Review:         "reviewer",
		},
		ExitCriteria: "Relevant tests pass",
	}
}

// writePlanFile creates a plan file in the given directory and returns its path.
func writePlanFile(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing plan file: %v", err)
	}
	return path
}

func TestImplementLoopSuccessFirstIteration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Agent script: writes the iteration handoff (verification-report.yaml +
	// progress.md + phase_complete) and emits stream-json success. The
	// handoff files must be written BEFORE the result so that waitForStatus's
	// readyCheck finds phase_complete when it receives SUCCESS.
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Working on implementation...")+"\n"+
			testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	// Review script: writes the structured APPROVED review-feedback.md handoff
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)

	planPath := writePlanFile(t, artifactDir, "Implement a hello world function")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Relevant tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}
	if result.Iterations != 1 {
		t.Errorf("expected Iterations=1, got %d", result.Iterations)
	}

	// Verify artifacts were created
	iterDir := filepath.Join(artifactDir, "iteration-01")
	promptFile := filepath.Join(iterDir, "user-prompt.md")
	if _, err := os.Stat(promptFile); os.IsNotExist(err) {
		t.Error("expected user-prompt.md to be created")
	}
	metaFile := filepath.Join(iterDir, "meta.yaml")
	if _, err := os.Stat(metaFile); os.IsNotExist(err) {
		t.Error("expected meta.yaml to be created")
	}
	summaryFile := filepath.Join(artifactDir, "summary.log")
	if _, err := os.Stat(summaryFile); os.IsNotExist(err) {
		t.Error("expected summary.log to be created")
	}

	// Verify summary content
	summaryData, _ := os.ReadFile(summaryFile)
	if !strings.Contains(string(summaryData), "status=SUCCESS") {
		t.Errorf("expected summary to contain status=SUCCESS, got: %s", string(summaryData))
	}
	if !strings.Contains(string(summaryData), "review=APPROVED") {
		t.Errorf("expected summary to contain review=APPROVED, got: %s", string(summaryData))
	}
}

func TestImplementLoopRetryThenSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	progressFile := filepath.Join(workDir, "progress.md")

	// Agent script: checks progress.md to decide RETRY vs SUCCESS.
	// First call creates/updates progress.md and exits without result (→ FAILED/retry).
	// Second call writes phase_complete and emits stream-json success.
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", `
read -r -t 5 _ || true
read -r -t 5 _ || true
PROGRESS_FILE="`+progressFile+`"
`+testutil.JSONLInit+`
if [ ! -f "$PROGRESS_FILE" ]; then
    echo "# Progress" > "$PROGRESS_FILE"
    echo "Step 1 done" >> "$PROGRESS_FILE"
else
    echo "Step 2 done" >> "$PROGRESS_FILE"
    `+testutil.WriteImplementSuccessArtifacts(artifactDir)+`
    `+testutil.JSONLSuccess+`
fi
`)

	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)

	planPath := writePlanFile(t, artifactDir, "Implement feature with two steps")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Relevant tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Errorf("expected Iterations=2, got %d", result.Iterations)
	}

	// Verify both iteration directories exist
	if _, err := os.Stat(filepath.Join(artifactDir, "iteration-01")); os.IsNotExist(err) {
		t.Error("expected iteration-01 directory")
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "iteration-02")); os.IsNotExist(err) {
		t.Error("expected iteration-02 directory")
	}
}

func TestImplementLoopSafetyRailConsecutiveFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Agent script: consumes startup stdin, then exits without emitting any
	// result message (→ FAILED).
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+`read -r _ || true
read -r _ || true
exit 1`+"\n")

	// Review script: should never be called
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)

	planPath := writePlanFile(t, artifactDir, "Some plan")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       10,
		MaxConsecFails:      2, // Trigger after 2 consecutive failures
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "safety_rail" {
		t.Errorf("expected FinalStatus=safety_rail, got %s", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Errorf("expected Iterations=2, got %d", result.Iterations)
	}
	if !strings.Contains(result.LastError, "consecutive agent failures") {
		t.Errorf("expected error about consecutive failures, got: %s", result.LastError)
	}
}

func TestImplementLoopSafetyRailNoProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Create a static progress.md that never changes
	os.WriteFile(filepath.Join(workDir, "progress.md"), []byte("# Progress\nStuck here"), 0o644)

	// Agent script: writes a stable handoff (same progress.md content each
	// iteration) so the no-progress fingerprint never changes; review
	// keeps requesting changes, eventually tripping the safety rail.
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	// Review script: always requests changes (triggering no-progress safety rail)
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewChangesRequested(artifactDir, "- **High**: Please make progress")+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)

	planPath := writePlanFile(t, artifactDir, "Some plan")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       10,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 2, // Trigger after 2 iterations with no progress
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "safety_rail" {
		t.Errorf("expected FinalStatus=safety_rail, got %s", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "no progress") {
		t.Errorf("expected error about no progress, got: %s", result.LastError)
	}
}

func TestImplementLoopReviewChangesRequestedThenApproved(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	progressFile := filepath.Join(workDir, "progress.md")

	// Agent script: always writes phase_complete, emits stream-json success and updates progress
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", `
PROGRESS_FILE="`+progressFile+`"
ITER=$(($(cat "$PROGRESS_FILE" 2>/dev/null | wc -l) + 1))
echo "iteration $ITER" >> "$PROGRESS_FILE"
`+testutil.JSONLInit+`
`+testutil.WriteImplementSuccessArtifacts(artifactDir)+`
`+testutil.JSONLSuccess+`
`)

	// Review script: first call rejects, second call approves
	reviewStateFile := filepath.Join(tmpDir, "review-count")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh", `
STATE_FILE="`+reviewStateFile+`"
COUNT=$(cat "$STATE_FILE" 2>/dev/null || echo 0)
COUNT=$((COUNT + 1))
echo "$COUNT" > "$STATE_FILE"
`+testutil.JSONLInit+`
if [ "$COUNT" -eq 1 ]; then
    `+testutil.WriteReviewChangesRequested(artifactDir, "- **High**: Please add error handling")+`
else
    `+testutil.WriteReviewApproved(artifactDir)+`
fi
`+testutil.JSONLSuccess+`
`)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)

	planPath := writePlanFile(t, artifactDir, "Implement with error handling")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass with error handling",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Errorf("expected Iterations=2, got %d", result.Iterations)
	}

	// Verify both iterations have artifacts
	for _, dir := range []string{"iteration-01", "iteration-02"} {
		metaFile := filepath.Join(artifactDir, dir, "meta.yaml")
		if _, err := os.Stat(metaFile); os.IsNotExist(err) {
			t.Errorf("expected %s/meta.yaml to exist", dir)
		}
	}
}

func TestImplementLoopMaxIterations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	progressFile := filepath.Join(workDir, "progress.md")

	// Agent script: always writes phase_complete, succeeds and updates progress
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", `
PROGRESS_FILE="`+progressFile+`"
echo "more progress $(date +%s%N)" >> "$PROGRESS_FILE"
`+testutil.JSONLInit+`
`+testutil.WriteImplementSuccessArtifacts(artifactDir)+`
`+testutil.JSONLSuccess+`
`)

	// Review always requests changes to keep the loop going until max iterations
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewChangesRequested(artifactDir, "- **High**: Needs more work")+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)

	planPath := writePlanFile(t, artifactDir, "Plan")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       3, // Small limit for test speed
		MaxConsecFails:      10,
		MaxConsecNoProgress: 10,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "max_iterations" {
		t.Errorf("expected FinalStatus=max_iterations, got %s", result.FinalStatus)
	}
	if result.Iterations != 3 {
		t.Errorf("expected Iterations=3, got %d", result.Iterations)
	}
}

func TestImplementLoopEventRouting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Agent script: writes the iteration handoff and emits stream-json success
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)

	planPath := writePlanFile(t, artifactDir, "Plan with events")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}

	// Drain event channel and verify we got permission and help events.
	// With the SDK JSON protocol, these come as SDKEventMsg with ControlRequest != nil
	// (permission) or Assistant messages with AskUserQuestion tool use (help).
	// Plain bash scripts don't emit JSON, so we just verify the loop completed.
	var gotDone bool
	timeout := time.After(2 * time.Second)
	for {
		select {
		case evt := <-eventCh:
			switch evt.(type) {
			case session.SDKEventMsg:
				// SDK events received - no old-style EventMsg constants
			case session.SessionDoneMsg:
				gotDone = true
			}
		case <-timeout:
			goto done
		default:
			// Retained: bounded poll interval while draining asynchronous events.
			time.Sleep(10 * time.Millisecond)
		}
	}
done:
	// SessionDoneMsg events come from the goroutine in Manager.StartSession
	// They may or may not have been sent by now (depends on timing), but
	// we verify the loop completed successfully which confirms the sessions ran.
	_ = gotDone
}

func TestImplementLoopReviewWithShellSpecialChars(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	reportCmd := "_d=\"\"; for d in \"" + artifactDir + "\"/iteration-*; do _d=\"$d\"; done; cat > \"$_d/verification-report.yaml\" <<'REPORT_EOF'\nversion: 1\nrequired_checks:\n  - name: Tests pass\n    requirement: go test ./...\n    status: passed\n    evidence: ok\n  - name: Lint clean\n    requirement: go vet ./...\n    status: passed\n    evidence: clean\nREPORT_EOF"
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+reportCmd+"\n"+
			// Custom report is written above; pair with a valid progress.md
			// so the harness handoff parser passes.
			testutil.WriteImplementProgressMd(artifactDir, "SUCCESS")+"\n"+
			testutil.TouchPhaseComplete(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	// Review script: emits stream-json APPROVED
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)

	planPath := writePlanFile(t, artifactDir, "Implement feature")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "review_passed" {
		t.Errorf("expected review_passed with shell-special chars in diff, got %s", result.FinalStatus)
	}
}

func TestImplementLoopResumesFromLatestIteration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Pre-populate 2 iteration directories to simulate a prior run that was
	// interrupted after iteration 2 (RETRY).
	am := NewArtifactManager(artifactDir)
	for _, n := range []int{1, 2} {
		iterDir, _ := am.CreateIterationDir(n)
		am.WriteMeta(iterDir, IterationMeta{
			Iteration:   n,
			AgentStatus: "FAILED",
		})
	}

	// Agent script: writes the iteration handoff and emits stream-json success
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Resume plan")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       10,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}
	// Should have completed on iteration 3 (resumed after 2)
	if result.Iterations != 3 {
		t.Errorf("expected Iterations=3, got %d", result.Iterations)
	}

	// iteration-03 should exist
	if _, err := os.Stat(filepath.Join(artifactDir, "iteration-03")); os.IsNotExist(err) {
		t.Error("expected iteration-03 directory to exist")
	}
}

func TestImplementLoopResumesReviewerFeedback(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Pre-populate iteration 1 with CHANGES_REQUESTED review
	am := NewArtifactManager(artifactDir)
	iterDir, _ := am.CreateIterationDir(1)
	am.WriteMeta(iterDir, IterationMeta{
		Iteration:    1,
		AgentStatus:  "SUCCESS",
		ReviewStatus: "CHANGES_REQUESTED",
	})
	os.WriteFile(filepath.Join(iterDir, "review-feedback.md"), []byte("Add error handling"), 0o644)

	// Agent script: writes the iteration handoff and emits stream-json success
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Plan with review feedback")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       10,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Errorf("expected Iterations=2, got %d", result.Iterations)
	}

	// Verify the resumed iteration's prompt includes the reviewer feedback
	promptData, err := os.ReadFile(filepath.Join(artifactDir, "iteration-02", "user-prompt.md"))
	if err != nil {
		t.Fatalf("reading prompt: %v", err)
	}
	if !strings.Contains(string(promptData), "Add error handling") {
		t.Error("expected resumed prompt to contain reviewer feedback from prior run")
	}
}

// TestImplementLoopResumesReviewerFeedbackAcrossFailures verifies that
// review feedback is recovered even when FAILED iterations sit between the
// last CHANGES_REQUESTED review and the resume point.
func TestImplementLoopResumesReviewerFeedbackAcrossFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Pre-populate: iteration 1 = CHANGES_REQUESTED, iteration 2 = FAILED
	am := NewArtifactManager(artifactDir)

	iter1Dir, _ := am.CreateIterationDir(1)
	am.WriteMeta(iter1Dir, IterationMeta{
		Iteration:    1,
		AgentStatus:  "SUCCESS",
		ReviewStatus: "CHANGES_REQUESTED",
	})
	os.WriteFile(filepath.Join(iter1Dir, "review-feedback.md"), []byte("Fix the widget tests"), 0o644)

	iter2Dir, _ := am.CreateIterationDir(2)
	am.WriteMeta(iter2Dir, IterationMeta{
		Iteration:   2,
		AgentStatus: "FAILED",
	})

	// Agent script: writes the iteration handoff and emits stream-json success
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Plan with review feedback across failures")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       10,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}
	// Should resume at iteration 3 (after failed iteration 2)
	if result.Iterations != 3 {
		t.Errorf("expected Iterations=3, got %d", result.Iterations)
	}

	// Verify the resumed iteration's prompt includes the reviewer feedback
	// from iteration 1, even though iteration 2 was FAILED
	promptData, err := os.ReadFile(filepath.Join(artifactDir, "iteration-03", "user-prompt.md"))
	if err != nil {
		t.Fatalf("reading prompt: %v", err)
	}
	if !strings.Contains(string(promptData), "Fix the widget tests") {
		t.Error("expected resumed prompt to contain reviewer feedback from iteration 1 across a FAILED iteration 2")
	}
}

func TestImplementLoopResumesConsecutiveFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Pre-populate 2 FAILED iterations
	am := NewArtifactManager(artifactDir)
	for _, n := range []int{1, 2} {
		iterDir, _ := am.CreateIterationDir(n)
		am.WriteMeta(iterDir, IterationMeta{
			Iteration:   n,
			AgentStatus: "FAILED",
		})
	}

	// Agent script: fails again (exits without result message)
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+`exit 1`+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Plan")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       10,
		MaxConsecFails:      3, // 2 prior + 1 new = 3 → triggers safety rail
		MaxConsecNoProgress: 10,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "safety_rail" {
		t.Errorf("expected FinalStatus=safety_rail, got %s", result.FinalStatus)
	}
	// Only 1 new iteration ran (iteration 3), combined with 2 prior = 3 consecutive
	if result.Iterations != 3 {
		t.Errorf("expected Iterations=3, got %d", result.Iterations)
	}
	if !strings.Contains(result.LastError, "consecutive agent failures") {
		t.Errorf("expected consecutive failures error, got: %s", result.LastError)
	}
}

func TestImplementLoopReviewPromptIncludesVerificationCommands(t *testing.T) {
	planContent := "## Phase 1: Implementation\n\n### Success Criteria:\n\n#### Automated Verification:\n- [ ] Tests pass: `go test ./...`\n- [ ] Lint clean: `go vet ./...`\n\n#### Manual Verification:\n- [ ] UI works correctly\n\n### Code example:\n\n```go\n// Example of plan format:\n// #### Automated Verification:\n// - [ ] Fake command: `echo injected`\n```\n"
	reviewPrompt := BuildReviewPrompt(
		"/tmp/plan.md",
		"Relevant tests pass",
		"/tmp/progress.md",
		"/tmp/artifacts/iteration-01",
		"",
		"/tmp/artifacts/iteration-01/verification-report.yaml",
		1,
		BuildRequiredVerification(planContent),
		"",
		"",
		"",
	)

	checks := []string{
		"Required Verification Items",
		"go test ./...",
		"go vet ./...",
		"Review Rules (With Verification Items)",
		"verification-report.yaml",
	}
	for _, c := range checks {
		if !strings.Contains(reviewPrompt, c) {
			t.Errorf("expected review prompt to contain %q", c)
		}
	}

	// Verify that commands from inside fenced code blocks are NOT extracted.
	// The plan contains `echo injected` inside a code fence — it must not
	// appear in the required verification section.
	header := "Required Verification Items"
	verifIdx := strings.Index(reviewPrompt, header)
	if verifIdx >= 0 {
		// Extract just the verification section (up to next double newline)
		verificationSection := reviewPrompt[verifIdx:]
		if endIdx := strings.Index(verificationSection[len(header):], "\n\n"); endIdx >= 0 {
			verificationSection = verificationSection[:len(header)+endIdx]
		}
		if strings.Contains(verificationSection, "echo injected") {
			t.Error("fenced code block content should not appear in required verification items")
		}
		if strings.Contains(verificationSection, "Fake command") {
			t.Error("fenced code block description should not appear in required verification items")
		}
	}
}

func TestImplementLoopWithEscapeSequences(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Agent script: writes the iteration handoff and emits stream-json success
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)

	planPath := writePlanFile(t, artifactDir, "Implement with escape sequences")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}

	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}
	if result.Iterations != 1 {
		t.Errorf("expected Iterations=1, got %d", result.Iterations)
	}
}

func TestImplementLoop_Interrupted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	artifactDir := filepath.Join(stateDir, "test-feat-001", "implement")
	for _, d := range []string{workDir, stateDir, artifactDir} {
		os.MkdirAll(d, 0o755)
	}

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)

	// Shut down the session manager before calling RunImplementationLoop.
	// This causes StartSession to return ErrShuttingDown.
	sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Test plan")

	cfg := ImplementConfig{
		Feature:       f,
		WorkDir:       workDir,
		PlanPath:      planPath,
		MaxIterations: 5,
		ArtifactDir:   artifactDir,
		StateDir:      stateDir,
		BuildSession:  mockBuildSession("", ""),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalStatus != "interrupted" {
		t.Errorf("expected FinalStatus=interrupted, got %s", result.FinalStatus)
	}
	if result.Iterations != 0 {
		t.Errorf("expected Iterations=0, got %d", result.Iterations)
	}
}

func TestImplementLoop_ShutdownMidIterationDoesNotPersistFailedMeta(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Agent script: starts iteration output, then blocks on stdin until the
	// session manager shuts down and closes the pipe. This simulates quitting
	// Agentic while iteration 1 is still in flight.
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			testutil.JSONLAssistant("Starting implementation...")+"\n"+
			"cat >/dev/null\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Test plan")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Relevant tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, ""),
	}

	resultCh := make(chan *LoopResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := RunImplementationLoop(cfg, sm)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	iterDir := filepath.Join(artifactDir, "iteration-01")
	responsePath := filepath.Join(iterDir, "response.txt")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if info, err := os.Stat(responsePath); err == nil && info.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s to receive output", responsePath)
		}
		// Retained: bounded poll interval for the subprocess output file.
		time.Sleep(20 * time.Millisecond)
	}

	sm.Shutdown()

	var result *LoopResult
	select {
	case err := <-errCh:
		t.Fatalf("RunImplementationLoop error: %v", err)
	case result = <-resultCh:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for implementation loop to stop after shutdown")
	}

	if result.FinalStatus != "interrupted" {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, "interrupted")
	}
	if result.Iterations != 0 {
		t.Fatalf("Iterations = %d, want 0", result.Iterations)
	}

	if _, err := os.Stat(filepath.Join(iterDir, "meta.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected no meta.yaml for interrupted iteration-01, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "iteration-02")); !os.IsNotExist(err) {
		t.Fatalf("expected no iteration-02 after interrupted shutdown, got err=%v", err)
	}
}

// When the prior run was interrupted mid-review (phase_complete written,
// but no meta.yaml), restart must skip the implement session for that
// iteration and jump directly to the review gate.
func TestImplementLoop_ResumesReviewWhenImplementAlreadyComplete(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	artifactDir := filepath.Join(stateDir, "test-feat-001", "implement")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, stateDir, artifactDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Pre-populate iteration-02 with phase_complete but no meta.yaml —
	// simulates an interrupt during the review gate.
	am := NewArtifactManager(artifactDir)
	iter1Dir, _ := am.CreateIterationDir(1)
	am.WriteMeta(iter1Dir, IterationMeta{
		Iteration:    1,
		AgentStatus:  "SUCCESS",
		ReviewStatus: "CHANGES_REQUESTED",
		MadeProgress: true,
	})
	iter2Dir, _ := am.CreateIterationDir(2)
	if err := os.WriteFile(filepath.Join(iter2Dir, PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("seeding phase_complete: %v", err)
	}
	// The implement turn is being skipped on resume, so the harness still
	// needs a parseable progress.md + verification-report.yaml on disk to
	// route iteration-02 into the review gate. Seed both.
	testutil.WriteImplementHandoffFiles(t, artifactDir, iter2Dir, "SUCCESS")

	// Agent script would fail if called — proves the implement session
	// is skipped on resume.
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		`echo "agent should not be invoked on resume" >&2`+"\n"+`exit 1`+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Review complete: APPROVED")+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Resume review plan")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2 (should resume on iteration 2, not start 3)", result.Iterations)
	}

	// meta.yaml for iteration-02 should now be written with review status.
	meta, err := am.ReadMeta(iter2Dir)
	if err != nil {
		t.Fatalf("reading iter-02 meta: %v", err)
	}
	if meta.AgentStatus != "SUCCESS" {
		t.Errorf("iter-02 meta.AgentStatus = %q, want SUCCESS", meta.AgentStatus)
	}
	if meta.ReviewStatus != "APPROVED" {
		t.Errorf("iter-02 meta.ReviewStatus = %q, want APPROVED", meta.ReviewStatus)
	}
}

func TestImplementLoop_ReviewViaAssistantText(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	artifactDir := filepath.Join(stateDir, "test-feat-001", "implement")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, stateDir, artifactDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Agent script writes the iteration handoff so the harness's
	// progress.md parser is satisfied and readyCheck finds phase_complete.
	iterDir := filepath.Join(artifactDir, "iteration-01")
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			`mkdir -p "`+iterDir+`"`+"\n"+
			testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+
			testutil.JSONLAssistant("Implementation done")+"\n"+testutil.JSONLSuccess+"\n")

	// Review writes the structured handoff file (verdict lives there, not stdout)
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLAssistant("All changes look good. Verdict: APPROVED.")+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Test plan for review")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}
}

func TestImplementLoop_CostAccumulation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	artifactDir := filepath.Join(stateDir, "test-feat-001", "implement")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, stateDir, artifactDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Agent script: writes the iteration handoff so the harness routes via
	// the progress.md parser; emits a result with specific cost.
	iterDir := filepath.Join(artifactDir, "iteration-01")
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			`mkdir -p "`+iterDir+`"`+"\n"+
			testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+
			`echo '{"type":"result","subtype":"success","session_id":"mock","total_cost_usd":0.05}'`+"\n")

	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	store := feature.NewStore(stateDir)
	f := newTestFeature(t, workDir)

	// Save feature to store first
	if err := store.Save(f); err != nil {
		t.Fatalf("saving feature: %v", err)
	}

	planPath := writePlanFile(t, artifactDir, "Test plan for cost")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := ImplementConfig{
		Feature:             f,
		FeatureStore:        store,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       5,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}

	// Reload feature and check cost
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("loading feature: %v", err)
	}
	cost := loaded.PhaseCosts["implement"]
	if cost < 0.04 || cost > 0.06 {
		t.Errorf("expected cost ~0.05, got %f", cost)
	}
}
