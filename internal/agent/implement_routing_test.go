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
touch "$_d/phase_complete"
`+testutil.JSONLInit+`
`+testutil.JSONLSuccess+`
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

// TestImplementLoop_Routing_RETRY_CompleteWithBlockersEscalatesNeedUserInput
// locks in the convergence guard for a contradictory handoff: RETRY claims
// implementation is complete while required verification remains failed.
// There is no actionable next iteration, so the harness must synthesize a
// NEED_USER_INPUT gate instead of spending the remaining iteration budget.
func TestImplementLoop_Routing_RETRY_CompleteWithBlockersEscalatesNeedUserInput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-retry-blocked")
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
- implementation and evidence are complete

### Remaining from the plan
- the required integration check cannot run because Docker is unavailable

### Where I stopped
Complete for implementation; verification requires a Docker-enabled environment.

### Gotchas / blockers / in-flight decisions
- expanding scope or changing the verification contract requires a decision

## Deferrals

`+"~~~"+`yaml
deferrals: []
closed_deferrals: []
`+"~~~"+`

## Verification Report

- **Path**: $_d/verification-report.yaml
- **Summary**: 0 passed, 1 failed, 0 blocked, 0 not_run

## Iteration State

RETRY
PROGRESS_EOF
cat > "$_d/verification-report.yaml" <<'REPORT_EOF'
version: 1
required_checks:
  - name: Integration tests pass
    requirement: task test-coverage
    status: failed
    evidence: rootless Docker not found
REPORT_EOF
touch "$_d/phase_complete"
`+testutil.JSONLInit+`
`+testutil.JSONLSuccess+`
`)
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Blocked RETRY escalation test")
	result, err := RunImplementationLoop(ImplementConfig{
		Feature: f, WorkDir: workDir, PlanPath: planPath, MaxIterations: 3,
		MaxConsecFails: 3, MaxConsecNoProgress: 3, ExitCriteria: "Relevant tests pass",
		Model: "agent", ReviewModel: "reviewer", ArtifactDir: artifactDir, StateDir: stateDir,
		BuildSession: mockBuildSession(agentScript, reviewScript),
	}, sm)
	if err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if result.FinalStatus != "need_user_input" {
		t.Fatalf("FinalStatus = %q, want need_user_input (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 1 {
		t.Fatalf("Iterations = %d, want 1", result.Iterations)
	}
	if result.NeedUserInputPath == "" {
		t.Fatal("NeedUserInputPath should point at the synthesized gate")
	}
	rec, err := ReadNeedUserInputRecord(result.NeedUserInputPath)
	if err != nil {
		t.Fatalf("read synthesized gate: %v", err)
	}
	if !strings.Contains(rec.Summary, "required verification") {
		t.Errorf("gate summary = %q, want required-verification explanation", rec.Summary)
	}
	if len(rec.Questions) != 1 || !strings.Contains(rec.Questions[0].Prompt, "verification") {
		t.Fatalf("gate questions = %+v, want one verification decision", rec.Questions)
	}
	meta, err := NewArtifactManager(artifactDir).ReadMeta(filepath.Join(artifactDir, "iteration-01"))
	if err != nil {
		t.Fatalf("read iteration meta: %v", err)
	}
	if meta.AgentStatus != "NEED_USER_INPUT" || meta.ReviewStatus != "skipped_need_user_input" {
		t.Fatalf("meta statuses = %q/%q, want NEED_USER_INPUT/skipped_need_user_input", meta.AgentStatus, meta.ReviewStatus)
	}
}

func padIter(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	// Tests only run a few iterations; this helper avoids a fmt import.
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// TestImplementLoop_Routing_NEED_USER_INPUT_PausesFeature locks in the
// single-repo pause path: `## Iteration State: NEED_USER_INPUT` plus a
// `## Questions for User` section and agent-authored need-user-input.yaml
// produce a need_user_input LoopResult pointing at the parsed gate artifact.
func TestImplementLoop_Routing_NEED_USER_INPUT_PausesFeature(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-003")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		_ = os.MkdirAll(d, 0o755)
	}

	const progressNote = "Progress note should not be used as the persisted gate summary."
	const gateSummary = "Gate summary from need-user-input.yaml."
	questions := []string{
		"Should implementation target the legacy auth path or the new auth service?",
		"Is it acceptable to skip migration of historical sessions?",
	}
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteImplementNeedUserInputArtifactsWithGateSummary(artifactDir, progressNote, gateSummary, questions...)+"\n"+
			testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Routing NEED_USER_INPUT test")

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
	if result.FinalStatus != "need_user_input" {
		t.Errorf("FinalStatus = %q, want need_user_input", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, gateSummary) {
		t.Errorf("LastError = %q, want parsed gate summary %q", result.LastError, gateSummary)
	}
	if strings.Contains(result.LastError, progressNote) {
		t.Errorf("LastError = %q, should not reuse progress.md state note", result.LastError)
	}
	if result.NeedUserInputPath == "" {
		t.Fatalf("NeedUserInputPath should be set when a gate is persisted")
	}
	rec, readErr := ReadNeedUserInputRecord(result.NeedUserInputPath)
	if readErr != nil {
		t.Fatalf("read gate: %v", readErr)
	}
	if rec.Summary != gateSummary {
		t.Errorf("gate summary = %q, want %q", rec.Summary, gateSummary)
	}
	if len(rec.Questions) != len(questions) {
		t.Fatalf("gate questions = %d, want %d", len(rec.Questions), len(questions))
	}
	for i, want := range questions {
		if rec.Questions[i].Prompt != want {
			t.Errorf("gate Q%d prompt = %q, want %q", i+1, rec.Questions[i].Prompt, want)
		}
		if rec.Questions[i].Answer != "" {
			t.Errorf("gate Q%d answer should be empty before user fills it", i+1)
		}
	}
	// meta.yaml must be persisted so LatestIteration() advances past the
	// paused iteration; a resume run picks up at iteration N+1.
	am := NewArtifactManager(artifactDir)
	if got := am.LatestIteration(); got != result.Iterations {
		t.Errorf("LatestIteration = %d, want %d (paused iteration must be committed)", got, result.Iterations)
	}
}

func TestImplementLoop_Routing_NEED_USER_INPUT_MissingGateTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-missing-gate")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		_ = os.MkdirAll(d, 0o755)
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteImplementNeedUserInputWithoutGate(artifactDir, "Need a choice.", "Legacy or new auth?")+"\n"+
			testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Missing gate test")
	result, err := RunImplementationLoop(ImplementConfig{
		Feature: f, WorkDir: workDir, PlanPath: planPath, MaxIterations: 3,
		MaxConsecFails: 2, MaxConsecNoProgress: 3, ExitCriteria: "Relevant tests pass",
		Model: "agent", ReviewModel: "reviewer", ArtifactDir: artifactDir, StateDir: stateDir,
		BuildSession: mockBuildSession(agentScript, reviewScript),
	}, sm)
	if err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if !strings.Contains(result.LastError, "need-user-input.yaml") || !strings.Contains(result.LastError, "missing") {
		t.Fatalf("LastError = %q, want missing need-user-input.yaml", result.LastError)
	}
}

func TestImplementLoop_Routing_NEED_USER_INPUT_MalformedGateTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-malformed-gate")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		_ = os.MkdirAll(d, 0o755)
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteImplementNeedUserInputMalformedGate(artifactDir, "Need a choice.", "Legacy or new auth?")+"\n"+
			testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Malformed gate test")
	result, err := RunImplementationLoop(ImplementConfig{
		Feature: f, WorkDir: workDir, PlanPath: planPath, MaxIterations: 3,
		MaxConsecFails: 2, MaxConsecNoProgress: 3, ExitCriteria: "Relevant tests pass",
		Model: "agent", ReviewModel: "reviewer", ArtifactDir: artifactDir, StateDir: stateDir,
		BuildSession: mockBuildSession(agentScript, reviewScript),
	}, sm)
	if err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if !strings.Contains(result.LastError, "need-user-input.yaml") || !strings.Contains(result.LastError, "unparseable") {
		t.Fatalf("LastError = %q, want unparseable need-user-input.yaml", result.LastError)
	}
}

// TestImplementLoop_Routing_NEED_USER_INPUT_StubGateBackfilledFromProgress
// reproduces the real-world failure where the implementer emits a rich
// progress.md (state note + numbered questions) but a blank need-user-input.yaml
// stub. The persisted gate must be backfilled from progress.md so the TUI
// renders the actual summary and questions rather than an empty form.
func TestImplementLoop_Routing_NEED_USER_INPUT_StubGateBackfilledFromProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-stub-gate")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		_ = os.MkdirAll(d, 0o755)
	}

	const note = "Plan contradicts the worktree; the owner must choose an ordering."
	questions := []string{
		"Should the pre-flight run before or after the latest-release fetch?",
		"Is aborting an already-current install under an unwritable dir acceptable?",
	}
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteImplementNeedUserInputStubGate(artifactDir, note, questions...)+"\n"+
			testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Stub gate backfill test")
	result, err := RunImplementationLoop(ImplementConfig{
		Feature: f, WorkDir: workDir, PlanPath: planPath, MaxIterations: 5,
		MaxConsecFails: 3, MaxConsecNoProgress: 3, ExitCriteria: "Relevant tests pass",
		Model: "agent", ReviewModel: "reviewer", ArtifactDir: artifactDir, StateDir: stateDir,
		BuildSession: mockBuildSession(agentScript, reviewScript),
	}, sm)
	if err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if result.FinalStatus != "need_user_input" {
		t.Fatalf("FinalStatus = %q, want need_user_input (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if result.NeedUserInputPath == "" {
		t.Fatalf("NeedUserInputPath should be set when a gate is persisted")
	}
	rec, readErr := ReadNeedUserInputRecord(result.NeedUserInputPath)
	if readErr != nil {
		t.Fatalf("read gate: %v", readErr)
	}
	if rec.Summary != note {
		t.Errorf("gate summary = %q, want backfilled progress note %q", rec.Summary, note)
	}
	if len(rec.Questions) != len(questions) {
		t.Fatalf("gate questions = %d, want %d backfilled from progress.md", len(rec.Questions), len(questions))
	}
	for i, want := range questions {
		if rec.Questions[i].Prompt != want {
			t.Errorf("gate Q%d prompt = %q, want %q", i+1, rec.Questions[i].Prompt, want)
		}
		if rec.Questions[i].Index != i+1 {
			t.Errorf("gate Q%d index = %d, want %d", i+1, rec.Questions[i].Index, i+1)
		}
	}
}

// TestImplementLoop_Routing_MisplacedQuestionsStayOnProtocolViolationPath
// locks in the conditional ordering rule: even when the agent emits a
// `## Questions for User` section with valid numbered prompts, placing it
// AFTER `## Iteration State` keeps the iteration on the protocol-violation
// retry path. The deterministic gate must reject the malformed handoff
// before any need-user-input.yaml gate artifact is persisted.
func TestImplementLoop_Routing_MisplacedQuestionsStayOnProtocolViolationPath(t *testing.T) {
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
touch "$_d/phase_complete"
`+testutil.JSONLInit+`
`+testutil.JSONLSuccess+`
`)
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Misordered questions routing test")

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
		t.Fatalf("misordered questions must not open a gate: %+v", result)
	}
	feedbackPath := filepath.Join(artifactDir, "iteration-01", "review-feedback.md")
	feedback, readErr := os.ReadFile(feedbackPath)
	if readErr != nil {
		t.Fatalf("read review-feedback.md: %v", readErr)
	}
	if !strings.Contains(string(feedback), "Questions for User") {
		t.Fatalf("review-feedback.md = %s, want ordering violation", feedback)
	}
	if _, statErr := os.Stat(filepath.Join(artifactDir, "iteration-01", "need-user-input.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("misordered questions must not persist a gate artifact: stat err = %v", statErr)
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

	// Agent script only touches phase_complete; never writes progress.md.
	// The harness should reject as protocol violation, never invoke review.
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.TouchPhaseComplete(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
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
		testutil.JSONLInit+"\n"+testutil.WriteImplementProtocolViolation(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
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

func TestImplementLoop_Routing_ProtocolViolation_MalformedVerificationReport(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-005")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		_ = os.MkdirAll(d, 0o755)
	}

	malformedReport := `for _d in "` + artifactDir + `"/iteration-*; do :; done
cat > "$_d/verification-report.yaml" <<'VR_EOF'
:
  :
VR_EOF`
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			malformedReport+"\n"+
			testutil.WriteImplementProgressMd(artifactDir, agentStatusSuccess)+"\n"+
			testutil.TouchPhaseComplete(artifactDir)+"\n"+
			testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Routing malformed verification report test")

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
		t.Errorf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "protocol violation: implementer @") || !strings.Contains(result.LastError, "verification-report.yaml") {
		t.Errorf("LastError = %q, want implementer verification-report.yaml protocol violation", result.LastError)
	}
}
