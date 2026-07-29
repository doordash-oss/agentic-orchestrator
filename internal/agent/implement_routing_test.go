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

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestImplementLoop_Routing_SUCCESS_RunsReviewGate locks in that an
// iteration emitting `## Iteration State: SUCCESS` falls through to the
// review gate (not directly to review_passed via SkipIterationReview).
// The mock review approves immediately.
func TestImplementLoop_Routing_SUCCESS_RunsReviewGate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		_ = os.MkdirAll(d, 0o755)
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Routing SUCCESS test")

	cfg := ImplementConfig{
		Feature: f, WorkDir: workDir, PlanPath: planPath, MaxIterations: 5,
		MaxConsecFails: 3, MaxConsecNoProgress: 3, ExitCriteria: "Relevant tests pass",
		Model: "agent", ReviewModel: "reviewer",
		ArtifactDir: artifactDir, StateDir: stateDir,
		BuildSession: mockBuildSession(agentScript, reviewScript),
	}
	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Errorf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", result.Iterations)
	}
}

// TestImplementLoop_Routing_RETRY_SkipsReview locks in that an iteration
// emitting `## Iteration State: RETRY` causes the harness to skip the
// review gate and start the next iteration with no reviewer feedback.
// The mock review script would emit APPROVED if invoked; the test asserts
// the loop reached MaxIterations (proving review never ran).
func TestImplementLoop_Routing_RETRY_SkipsReview(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-002")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		_ = os.MkdirAll(d, 0o755)
	}

	// Agent always emits RETRY. The handoff narrative changes per iteration
	// (we append the iteration number to "Completed this iteration") so the
	// no-progress fingerprint differs and the safety rail doesn't trip
	// before MaxIterations.
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", `
ITER_FILE="`+filepath.Join(tmpDir, "iter")+`"
ITER=$(cat "$ITER_FILE" 2>/dev/null || echo 0)
ITER=$((ITER+1))
echo "$ITER" > "$ITER_FILE"
for _d in "`+artifactDir+`"/iteration-*; do :; done
mkdir -p "$_d"
cat > "`+artifactDir+`/progress.md" <<PROGRESS_EOF
# Iteration Progress

## Iteration Handoff

### Completed this iteration
- chunk $ITER

### Remaining from the plan
- more

### Where I stopped
On chunk $ITER

### Gotchas / blockers / in-flight decisions

## Deferrals

`+"~~~"+`yaml
deferrals: []
closed_deferrals: []
`+"~~~"+`

## Verification Report

- **Path**: $_d/verification-report.yaml
- **Summary**: 0 passed, 0 failed, 0 blocked, 1 not_run

## Iteration State

RETRY
PROGRESS_EOF
if [ ! -f "$_d/verification-report.yaml" ]; then
  printf 'version: 1\nrequired_checks:\n  - name: Deferred check\n    requirement: go test ./...\n    status: not_run\n    evidence: pending retry iteration\n' > "$_d/verification-report.yaml"
fi
`+testutil.JSONLInit+`
`+testutil.JSONLRetry+`
`)

	// Review: would emit APPROVED if invoked. The test asserts FinalStatus
	// is max_iterations (review never reached) — proves RETRY skipped review.
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Routing RETRY test")

	cfg := ImplementConfig{
		Feature: f, WorkDir: workDir, PlanPath: planPath, MaxIterations: 3,
		MaxConsecFails: 5, MaxConsecNoProgress: 5, ExitCriteria: "Relevant tests pass",
		Model: "agent", ReviewModel: "reviewer",
		ArtifactDir: artifactDir, StateDir: stateDir,
		BuildSession: mockBuildSession(agentScript, reviewScript),
	}
	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if result.FinalStatus != "max_iterations" {
		t.Errorf("FinalStatus = %q, want max_iterations (review must not run on RETRY)", result.FinalStatus)
	}
	if result.Iterations != 3 {
		t.Errorf("Iterations = %d, want 3", result.Iterations)
	}
	// Each iteration's meta.yaml should record AgentStatus=RETRY,
	// ReviewStatus=skipped_retry.
	am := NewArtifactManager(artifactDir)
	for n := 1; n <= 3; n++ {
		iterDir := filepath.Join(artifactDir, "iteration-"+padIter(n))
		meta, err := am.ReadMeta(iterDir)
		if err != nil {
			t.Fatalf("read meta iter %d: %v", n, err)
		}
		if meta.AgentStatus != "RETRY" {
			t.Errorf("iter %d AgentStatus = %q, want RETRY", n, meta.AgentStatus)
		}
		if meta.ReviewStatus != "skipped_retry" {
			t.Errorf("iter %d ReviewStatus = %q, want skipped_retry", n, meta.ReviewStatus)
		}
	}
}

func TestImplementLoop_NextIterationUsesLatestSessionConfig(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-session-config")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, dir := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	oldModelScript := testutil.WriteScript(t, scriptsDir, "old-model.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementRetryArtifacts(artifactDir)+"\n"+testutil.JSONLRetry+"\n")
	newModelScript := testutil.WriteScript(t, scriptsDir, "new-model.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	buildSession, captured := capturingBuildSessionByModel(map[string]string{
		"old-model": oldModelScript,
		"new-model": newModelScript,
	})
	current := SessionRuntimeConfig{
		Model:           "old-model",
		EffectiveEffort: llm.EffortMedium,
		EffortSource:    llm.EffortSourceExplicit,
		AskingClause:    "old asking clause",
	}
	buildCalls := 0
	buildWithConfigChange := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		buildCalls++
		command, env, sessionOpts, err := buildSession(opts)
		if buildCalls == 1 {
			current = SessionRuntimeConfig{
				Model:           "new-model",
				EffectiveEffort: llm.EffortHigh,
				EffortSource:    llm.EffortSourceExplicit,
				AskingClause:    "new asking clause",
			}
		}
		return command, env, sessionOpts, err
	}

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := ImplementConfig{
		Feature:             newTestFeature(t, workDir),
		WorkDir:             workDir,
		PlanPath:            writePlanFile(t, artifactDir, "Session config refresh test"),
		MaxIterations:       2,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "Relevant tests pass",
		Model:               "old-model",
		ReviewModel:         "review-model",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        buildWithConfigChange,
		ResolveSessionConfig: func(role llm.PhaseRole) (SessionRuntimeConfig, error) {
			if role != llm.PhaseImplementation {
				t.Fatalf("ResolveSessionConfig role = %q, want implementation", role)
			}
			return current, nil
		},
		SkipIterationReview: true,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed || result.Iterations != 2 {
		t.Fatalf("result = %+v, want review_passed on iteration 2", result)
	}
	if len(*captured) != 2 {
		t.Fatalf("BuildSession calls = %d, want 2", len(*captured))
	}
	if (*captured)[0].Model != "old-model" || (*captured)[1].Model != "new-model" {
		t.Fatalf("BuildSession models = [%q, %q], want [old-model, new-model]", (*captured)[0].Model, (*captured)[1].Model)
	}
	if (*captured)[0].EffortLevel != llm.EffortMedium || (*captured)[1].EffortLevel != llm.EffortHigh {
		t.Fatalf("BuildSession efforts = [%q, %q], want [medium, high]", (*captured)[0].EffortLevel, (*captured)[1].EffortLevel)
	}
	if !strings.Contains((*captured)[0].SystemPrompt, "old asking clause") ||
		!strings.Contains((*captured)[1].SystemPrompt, "new asking clause") {
		t.Fatalf("system prompts did not use the session-bound asking clauses")
	}
}

func padIter(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	// Tests only run a few iterations; this helper avoids a fmt import.
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// TestImplementLoop_Routing_LegacyQuestionsStayOnProtocolViolationPath locks
// in that progress.md cannot create a user-input gate. Questions must use the
// live root AskUserQuestion control; a legacy state/section stays on the
// protocol-violation path and never persists a gate artifact.
func TestImplementLoop_Routing_LegacyQuestionsStayOnProtocolViolationPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-questions-order")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		_ = os.MkdirAll(d, 0o755)
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", `for _d in "`+artifactDir+`"/iteration-*; do :; done
mkdir -p "$_d"
cat > "`+artifactDir+`/progress.md" <<PROGRESS_EOF
# Iteration Progress

## Iteration Handoff

### Completed this iteration
- traced the blocker

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

`+"~~~"+`yaml
deferrals: []
closed_deferrals: []
`+"~~~"+`

## Verification Report

- **Path**: $_d/verification-report.yaml
- **Summary**: 0 passed, 0 failed, 0 blocked, 0 not_run

## Iteration State

NEED_USER_INPUT
Need a product choice before touching auth.

## Questions for User

1. Legacy auth path or new auth service?
PROGRESS_EOF
if [ ! -f "$_d/verification-report.yaml" ]; then
  printf 'version: 1\nrequired_checks: []\n' > "$_d/verification-report.yaml"
fi
`+testutil.JSONLInit+`
`+testutil.JSONLSuccess+`
`)
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Legacy questions routing test")

	cfg := ImplementConfig{
		Feature: f, WorkDir: workDir, PlanPath: planPath, MaxIterations: 1,
		MaxConsecFails: 3, MaxConsecNoProgress: 3, ExitCriteria: "All tests pass",
		Model: "agent", ReviewModel: "reviewer",
		ArtifactDir: artifactDir, StateDir: stateDir,
		BuildSession: mockBuildSession(agentScript, reviewScript),
	}
	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if result.FinalStatus == "need_user_input" {
		t.Fatalf("legacy progress questions must not open a gate: %+v", result)
	}
	feedbackPath := filepath.Join(artifactDir, "iteration-01", "review-feedback.md")
	feedback, readErr := os.ReadFile(feedbackPath)
	if readErr != nil {
		t.Fatalf("read review-feedback.md: %v", readErr)
	}
	if !strings.Contains(string(feedback), "Questions for User") {
		t.Fatalf("review-feedback.md = %s, want legacy question-section violation", feedback)
	}
	if _, statErr := os.Stat(filepath.Join(artifactDir, "iteration-01", "need-user-input.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("legacy progress questions must not persist a gate artifact: stat err = %v", statErr)
	}
}

// TestImplementLoop_Routing_ProtocolViolation_BypassesReview locks in
// that a malformed progress.md (e.g., missing `## Iteration State`) is
// rejected by the harness handoff parser and never reaches the review
// gate. The mock review script would emit APPROVED if invoked.
func TestImplementLoop_Routing_ProtocolViolation_BypassesReview(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-004")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		_ = os.MkdirAll(d, 0o755)
	}

	// Agent script never writes progress.md. The harness should reject the
	// artifact contract and never invoke review.
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Routing protocol-violation test")

	cfg := ImplementConfig{
		Feature: f, WorkDir: workDir, PlanPath: planPath, MaxIterations: 5,
		MaxConsecFails: 2, MaxConsecNoProgress: 5, ExitCriteria: "Relevant tests pass",
		Model: "agent", ReviewModel: "reviewer",
		ArtifactDir: artifactDir, StateDir: stateDir,
		BuildSession: mockBuildSession(agentScript, reviewScript),
	}
	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Errorf("FinalStatus = %q, want protocol_violation (reviewer must not run on protocol violations)", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "protocol violation: implementer @") || !strings.Contains(result.LastError, "progress.md") {
		t.Errorf("LastError = %q, want implementer progress.md protocol violation", result.LastError)
	}
	// Each iteration's meta should record CHANGES_REQUESTED from the
	// deterministic protocol-violation gate.
	am := NewArtifactManager(artifactDir)
	for n := 1; n <= 2; n++ {
		iterDir := filepath.Join(artifactDir, "iteration-"+padIter(n))
		meta, err := am.ReadMeta(iterDir)
		if err != nil {
			t.Fatalf("read meta iter %d: %v", n, err)
		}
		if meta.ReviewStatus != agentStatusChangesRequested {
			t.Errorf("iter %d ReviewStatus = %q, want CHANGES_REQUESTED (protocol-violation feedback)", n, meta.ReviewStatus)
		}
		// Synthesized feedback file should be present.
		fb, err := os.ReadFile(filepath.Join(iterDir, "review-feedback.md"))
		if err != nil {
			t.Fatalf("iter %d missing review-feedback.md: %v", n, err)
		}
		if !strings.Contains(string(fb), "## Verdict\nCHANGES_REQUESTED") {
			t.Errorf("iter %d feedback missing terminal ## Verdict block", n)
		}
	}
}

func TestImplementLoop_Routing_ProtocolViolation_ResumesFailureBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-protocol-resume")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		_ = os.MkdirAll(d, 0o755)
	}

	am := NewArtifactManager(artifactDir)
	for n := 1; n <= 2; n++ {
		iterDir, err := am.CreateIterationDir(n)
		if err != nil {
			t.Fatalf("create iteration %d: %v", n, err)
		}
		if err := am.WriteMeta(iterDir, IterationMeta{
			Iteration:    n,
			AgentStatus:  agentStatusProtocolViolation,
			ReviewStatus: agentStatusChangesRequested,
		}); err != nil {
			t.Fatalf("write meta iteration %d: %v", n, err)
		}
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Routing protocol-violation resume test")

	cfg := ImplementConfig{
		Feature: f, WorkDir: workDir, PlanPath: planPath, MaxIterations: 10,
		MaxConsecFails: 3, MaxConsecNoProgress: 10, ExitCriteria: "Relevant tests pass",
		Model: "agent", ReviewModel: "reviewer",
		ArtifactDir: artifactDir, StateDir: stateDir,
		BuildSession: mockBuildSession(agentScript, reviewScript),
	}
	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if result.Iterations != 3 {
		t.Fatalf("Iterations = %d, want 3 (two recovered violations plus one new violation)", result.Iterations)
	}
	if !strings.Contains(result.LastError, "protocol violation: implementer @") {
		t.Fatalf("LastError = %q, want protocol violation detail", result.LastError)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "iteration-04")); !os.IsNotExist(err) {
		t.Fatalf("iteration-04 should not run after recovered protocol-violation budget trips: stat err = %v", err)
	}
}
