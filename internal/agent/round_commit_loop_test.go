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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// recordingRoundCommitHook captures every round completion event the loops
// emit. It deliberately performs no git work: these tests assert the loops'
// reporting contract, not the orchestrator's commit behavior.
func recordingRoundCommitHook(t *testing.T) (RoundCommitHook, *[]RoundCommitInput) {
	t.Helper()
	inputs := &[]RoundCommitInput{}
	return func(input RoundCommitInput) error {
		*inputs = append(*inputs, input)
		return nil
	}, inputs
}

// TestRunImplementationLoop_RoundCommitHook_ImplementThenFixRound drives the
// canonical fix cycle: iteration 1 implements (review requests changes),
// iteration 2 addresses the feedback (review approves). The hook must fire
// once per round, right after each session ends, with the agreed round
// identity: first implement round unlabeled, the feedback-driven round a
// fix round with the per-phase counter.
func TestRunImplementationLoop_RoundCommitHook_ImplementThenFixRound(t *testing.T) {
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

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", `
PROGRESS_FILE="`+progressFile+`"
ITER=$(($(cat "$PROGRESS_FILE" 2>/dev/null | wc -l) + 1))
echo "iteration $ITER" >> "$PROGRESS_FILE"
`+testutil.JSONLInit+`
`+testutil.WriteImplementSuccessArtifacts(artifactDir)+`
`+testutil.JSONLSuccess+`
`)

	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh", `
for _d in "`+artifactDir+`"/iteration-*; do :; done
`+testutil.JSONLInit+`
if [ "$(basename "$_d")" = "iteration-01" ]; then
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
	f.CurrentRoadmapPhase = 1
	f.TotalRoadmapPhases = 2
	f.RoadmapPhaseType = "tracer-bullet"

	hook, inputs := recordingRoundCommitHook(t)

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
		RoundCommitHook:     hook,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}

	if len(*inputs) != 2 {
		t.Fatalf("round commit hook fired %d times, want 2; inputs: %+v", len(*inputs), *inputs)
	}
	first, second := (*inputs)[0], (*inputs)[1]

	if first.Kind != RoundCommitImplement {
		t.Errorf("round 1 Kind = %q, want implement", first.Kind)
	}
	if !first.FirstImplementCommit {
		t.Error("round 1 must be the phase's first (unlabeled) implementation commit")
	}
	if first.Iteration != 1 {
		t.Errorf("round 1 Iteration = %d, want 1", first.Iteration)
	}
	if first.PhaseNumber != 1 || first.TotalPhases != 2 || first.PhaseType != "tracer-bullet" {
		t.Errorf("round 1 phase identity = %d/%d %q, want 1/2 tracer-bullet", first.PhaseNumber, first.TotalPhases, first.PhaseType)
	}
	if first.Repos["test-repo"] != workDir {
		t.Errorf("round 1 Repos[test-repo] = %q, want %q", first.Repos["test-repo"], workDir)
	}

	if second.Kind != RoundCommitFix {
		t.Errorf("round 2 Kind = %q, want fix", second.Kind)
	}
	if second.FixNumber != 1 {
		t.Errorf("round 2 FixNumber = %d, want 1", second.FixNumber)
	}
	if second.FirstImplementCommit {
		t.Error("round 2 must not be labeled as the first implementation commit")
	}
	if second.Iteration != 2 {
		t.Errorf("round 2 Iteration = %d, want 2", second.Iteration)
	}
}

// TestRunImplementationLoop_RoundCommitHook_SecondFixRoundNumbersPerPhase
// verifies the separate per-phase fix counter: two consecutive fix rounds
// addressing two distinct CHANGES_REQUESTED reviews are fix rounds 1 and 2.
func TestRunImplementationLoop_RoundCommitHook_SecondFixRoundNumbersPerPhase(t *testing.T) {
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

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", `
PROGRESS_FILE="`+progressFile+`"
ITER=$(($(cat "$PROGRESS_FILE" 2>/dev/null | wc -l) + 1))
echo "iteration $ITER" >> "$PROGRESS_FILE"
`+testutil.JSONLInit+`
`+testutil.WriteImplementSuccessArtifacts(artifactDir)+`
`+testutil.JSONLSuccess+`
`)

	// Review requests changes on iterations 1 and 2, approves on 3.
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh", `
for _d in "`+artifactDir+`"/iteration-*; do :; done
`+testutil.JSONLInit+`
if [ "$(basename "$_d")" = "iteration-03" ]; then
    `+testutil.WriteReviewApproved(artifactDir)+`
else
    `+testutil.WriteReviewChangesRequested(artifactDir, "- **High**: Still not right")+`
fi
`+testutil.JSONLSuccess+`
`)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	hook, inputs := recordingRoundCommitHook(t)

	planPath := writePlanFile(t, artifactDir, "Implement with two fix rounds")

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
		RoundCommitHook:     hook,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if len(*inputs) != 3 {
		t.Fatalf("round commit hook fired %d times, want 3; inputs: %+v", len(*inputs), *inputs)
	}
	if got := (*inputs)[1].FixNumber; got != 1 {
		t.Errorf("round 2 FixNumber = %d, want 1", got)
	}
	if got := (*inputs)[2].FixNumber; got != 2 {
		t.Errorf("round 3 FixNumber = %d, want 2", got)
	}
	if (*inputs)[2].Kind != RoundCommitFix {
		t.Errorf("round 3 Kind = %q, want fix", (*inputs)[2].Kind)
	}
}

// TestRunImplementationLoop_RoundCommitHook_ErrorFailsLoop: a round whose
// changes cannot be committed must fail the loop loudly instead of
// continuing to the review gate over uncommitted state.
func TestRunImplementationLoop_RoundCommitHook_ErrorFailsLoop(t *testing.T) {
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

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", `
echo "iteration 1" >> "`+progressFile+`"
`+testutil.JSONLInit+`
`+testutil.WriteImplementSuccessArtifacts(artifactDir)+`
`+testutil.JSONLSuccess+`
`)

	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)

	planPath := writePlanFile(t, artifactDir, "Implement something")

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
		RoundCommitHook: func(input RoundCommitInput) error {
			return fmt.Errorf("simulated commit failure")
		},
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err == nil {
		t.Fatalf("expected loop error on round commit failure; got result %+v", result)
	}
	if !strings.Contains(err.Error(), "committing implementation round 1") {
		t.Errorf("error = %v, want round commit failure surfaced", err)
	}

	// The session reached semantic completion before the commit failed; its
	// iteration record must be persisted so crash recovery and the
	// meta-scan round trackers see the same history the live run produced.
	metaPath := filepath.Join(artifactDir, "iteration-01", "meta.yaml")
	data, readErr := os.ReadFile(metaPath)
	if readErr != nil {
		t.Fatalf("read iteration meta after round commit failure: %v", readErr)
	}
	if !strings.Contains(string(data), "agent_status: SUCCESS") {
		t.Errorf("iteration meta = %s, want agent_status SUCCESS (semantic completion recorded)", data)
	}
}

// TestRunFeatureFinalReviewLoop_RoundCommitHook_FiresPerFixRound drives the
// inverted FR order (review → fix → review) with a real git worktree and
// asserts the fix round reports the feature-level fix counter.
func TestRunFeatureFinalReviewLoop_RoundCommitHook_FiresPerFixRound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-round-commit", []string{testRepoNameAPI})
	repo := testutil.InitGitRepo(t)
	f.Repos[0].Path = repo
	f.Repos[0].WorktreePath = repo
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature with git repo: %v", err)
	}

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		fmt.Sprintf(`if [ -d "%s/iteration-02" ]; then
%s
%s
%s
else
%s
%s
%s
fi`,
			artDir,
			testutil.JSONLInit,
			testutil.WriteFinalReviewApproved(artDir),
			testutil.JSONLSuccess,
			testutil.JSONLInit,
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: needs a fix"),
			testutil.JSONLSuccess))

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	hook, inputs := recordingRoundCommitHook(t)

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    fixScript,
		}),
		RoundCommitHook: hook,
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if len(*inputs) != 1 {
		t.Fatalf("round commit hook fired %d times, want 1; inputs: %+v", len(*inputs), *inputs)
	}
	got := (*inputs)[0]
	if got.Kind != RoundCommitFinalReviewFix {
		t.Errorf("Kind = %q, want final_review_fix", got.Kind)
	}
	if got.FixNumber != 1 {
		t.Errorf("FixNumber = %d, want 1", got.FixNumber)
	}
	if got.Iteration != 1 {
		t.Errorf("Iteration = %d, want 1", got.Iteration)
	}
	if got.Repos[testRepoNameAPI] != repo {
		t.Errorf("Repos[api] = %q, want %q", got.Repos[testRepoNameAPI], repo)
	}
}

// TestRunFeatureFinalReviewLoop_DirtyWorktreeAtApprovalFailsLoudly: with
// per-round commits wired, an approval over a dirty worktree is a fixer bug.
// The loop must fail the feature instead of sweeping the changes.
func TestRunFeatureFinalReviewLoop_DirtyWorktreeAtApprovalFailsLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-dirty-approval", []string{testRepoNameAPI})
	repo := testutil.InitGitRepo(t)
	f.Repos[0].Path = repo
	f.Repos[0].WorktreePath = repo
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature with git repo: %v", err)
	}

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		fmt.Sprintf(`if [ -d "%s/iteration-02" ]; then
%s
%s
%s
else
%s
%s
%s
fi`,
			artDir,
			testutil.JSONLInit,
			testutil.WriteFinalReviewApproved(artDir),
			testutil.JSONLSuccess,
			testutil.JSONLInit,
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: needs a fix"),
			testutil.JSONLSuccess))

	// The fixer edits the repo but the (recording, non-committing) hook
	// leaves the worktree dirty — simulating a fix round whose commit
	// never landed.
	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		fmt.Sprintf("%s\ncat > %s <<'EOF'\nfix applied\nEOF\n%s\n",
			testutil.JSONLInit,
			filepath.Join(repo, "fix.txt"),
			testutil.JSONLSuccess))

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	hook, _ := recordingRoundCommitHook(t)

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    fixScript,
		}),
		RoundCommitHook: hook,
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != "failed" {
		t.Fatalf("FinalStatus = %q, want failed", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, testRepoNameAPI) || !strings.Contains(result.LastError, "uncommitted changes") {
		t.Errorf("LastError = %q, want dirty-worktree failure naming repo %q", result.LastError, testRepoNameAPI)
	}
}

// TestRunFeatureFinalReviewLoop_DirtyWorktreeAtApprovalToleratesRootArtifacts:
// a stranded untracked progress.md at the repo root is a known orchestration
// artifact (the publish path scrubs it) and must not fail an otherwise clean
// approval.
func TestRunFeatureFinalReviewLoop_DirtyWorktreeAtApprovalToleratesRootArtifacts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-stray-artifact", []string{testRepoNameAPI})
	repo := testutil.InitGitRepo(t)
	f.Repos[0].Path = repo
	f.Repos[0].WorktreePath = repo
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature with git repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "progress.md"), []byte("stray\n"), 0o644); err != nil {
		t.Fatalf("write stray artifact: %v", err)
	}

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:         f,
		FeatureStore:    store,
		StateDir:        env.stateDir,
		Model:           "agent",
		ReviewModel:     "reviewer",
		MaxIterations:   1,
		MaxConsecFails:  3,
		BuildSession:    mockBuildSessionByModel(map[string]string{"reviewer": reviewScript}),
		RoundCommitHook: func(input RoundCommitInput) error { return nil },
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed (LastError=%q)", result.FinalStatus, result.LastError)
	}
}

// TestRunImplementationLoop_RoundCommitHook_ProtocolViolationsCountFixRounds
// pins the live-counter/meta-scan invariant: implementer protocol violations
// write meta.ReviewStatus=CHANGES_REQUESTED and arm reviewer feedback, so
// each one must advance the live fix-round counter exactly like a
// crash-resume meta scan would. Two violations make the following round fix
// round 2, not a repeated fix round 1.
func TestRunImplementationLoop_RoundCommitHook_ProtocolViolationsCountFixRounds(t *testing.T) {
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

	// Iterations 1-2 emit success without writing the required artifacts
	// (implementer contract violation); iteration 3 writes them.
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", `
if [ -d "`+artifactDir+`/iteration-03" ]; then
`+testutil.JSONLInit+`
`+testutil.WriteImplementSuccessArtifacts(artifactDir)+`
`+testutil.JSONLSuccess+`
else
`+testutil.JSONLInit+`
`+testutil.JSONLSuccess+`
fi
`)

	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh", `
for _d in "`+artifactDir+`"/iteration-*; do :; done
`+testutil.JSONLInit+`
if [ "$(basename "$_d")" = "iteration-03" ]; then
    `+testutil.WriteReviewApproved(artifactDir)+`
else
    `+testutil.WriteReviewChangesRequested(artifactDir, "- **High**: missing artifacts")+`
fi
`+testutil.JSONLSuccess+`
`)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	hook, inputs := recordingRoundCommitHook(t)

	planPath := writePlanFile(t, artifactDir, "Violate twice then fix")

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
		RoundCommitHook:     hook,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed (LastError=%q)", result.FinalStatus, result.LastError)
	}

	// Protocol-violation rounds do not commit; only iteration 3's fix round
	// fires the hook, and it must be fix round 2 (two CHANGES_REQUESTED
	// metas precede it).
	if len(*inputs) != 1 {
		t.Fatalf("round commit hook fired %d times, want 1; inputs: %+v", len(*inputs), *inputs)
	}
	got := (*inputs)[0]
	if got.Kind != RoundCommitFix {
		t.Errorf("Kind = %q, want fix", got.Kind)
	}
	if got.FixNumber != 2 {
		t.Errorf("FixNumber = %d, want 2 (two protocol-violation CHANGES_REQUESTED iterations precede it)", got.FixNumber)
	}

	// The persisted metas must agree with the live counter: a crash resume
	// scanning them seeds the same fix number.
	am := NewArtifactManager(artifactDir)
	tr := newImplementRoundCommitTracker(am, artifactDir, 3)
	if tr.changesRequested != 2 {
		t.Errorf("meta-scan changesRequested = %d, want 2 (live/meta divergence)", tr.changesRequested)
	}
}

// TestRunFeatureFinalReviewLoop_RoundCommitHook_FailedFixRoundStillCommits:
// a FAILED fix round's partial edits must not strand the worktree dirty —
// FR has no later absorbing round, so the fix-round commit fires whatever
// the session outcome. The follow-up review approves over the committed
// state instead of tripping the approval dirty-check.
func TestRunFeatureFinalReviewLoop_RoundCommitHook_FailedFixRoundStillCommits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-failed-fix-commit", []string{testRepoNameAPI})
	repo := testutil.InitGitRepo(t)
	f.Repos[0].Path = repo
	f.Repos[0].WorktreePath = repo
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature with git repo: %v", err)
	}

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	// Iteration 1's fixer applies a real edit but dies before completing
	// (FAILED); iteration 2's review approves the (committed) partial fix.
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		fmt.Sprintf(`if [ -d "%s/iteration-02" ]; then
%s
%s
%s
else
%s
%s
%s
fi`,
			artDir,
			testutil.JSONLInit,
			testutil.WriteFinalReviewApproved(artDir),
			testutil.JSONLSuccess,
			testutil.JSONLInit,
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: needs a fix"),
			testutil.JSONLSuccess))

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		fmt.Sprintf(`%s
cat > %s <<'EOF'
partial fix
EOF
exit 1
`,
			testutil.JSONLInit,
			filepath.Join(repo, "fix.txt")))

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	// The hook commits the failed fixer's partial work, mirroring the
	// orchestrator's behavior.
	hook := func(input RoundCommitInput) error {
		for _, path := range input.Repos {
			if path == "" {
				continue
			}
			if _, err := git.CommitAllAndGetHead(path, "Final review fix 1 (address review feedback)"); err != nil {
				return err
			}
		}
		return nil
	}

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    fixScript,
		}),
		RoundCommitHook: hook,
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if _, err := os.Stat(filepath.Join(repo, "fix.txt")); err != nil {
		t.Errorf("partial fix edit missing; the failed fix round's work disappeared: %v", err)
	}
}

// TestNewImplementRoundCommitTracker_RecoveryFromMetas seeds the tracker from
// persisted iteration metas so a crash-resumed loop keeps the round labeling
// of the interrupted run.
func TestNewImplementRoundCommitTracker_RecoveryFromMetas(t *testing.T) {
	artifactDir := t.TempDir()
	am := NewArtifactManager(artifactDir)

	writeMeta := func(iter int, agentStatus, reviewStatus string) {
		t.Helper()
		iterDir := filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", iter))
		if err := os.MkdirAll(iterDir, 0o755); err != nil {
			t.Fatalf("mkdir iteration dir: %v", err)
		}
		if err := am.WriteMeta(iterDir, IterationMeta{Iteration: iter, AgentStatus: agentStatus, ReviewStatus: reviewStatus}); err != nil {
			t.Fatalf("write meta: %v", err)
		}
	}
	// Phase history: implement (1), fix (2, changes_requested review at 1),
	// failed fix attempt (3), fix (4, second changes_requested at 3... no —
	// review at 2 approved nothing yet). Realistic sequence:
	//   1: SUCCESS / CHANGES_REQUESTED  → fix round 1 follows
	//   2: FAILED  / (empty)             → fix round 1 failed, no commit
	//   3: SUCCESS / CHANGES_REQUESTED   → fix round 2 follows
	//   4: SUCCESS / (pending review)
	writeMeta(1, agentStatusSuccess, agentStatusChangesRequested)
	writeMeta(2, agentStatusFailed, "")
	writeMeta(3, agentStatusSuccess, agentStatusChangesRequested)
	writeMeta(4, agentStatusSuccess, "")

	tr := newImplementRoundCommitTracker(am, artifactDir, 4)
	if tr.changesRequested != 2 {
		t.Errorf("changesRequested = %d, want 2", tr.changesRequested)
	}
	if !tr.semanticSessions {
		t.Error("semanticSessions = false, want true after a SUCCESS round")
	}

	// A phase with only a FAILED round: the next round is still the
	// unlabeled first implementation commit.
	emptyDir := t.TempDir()
	am2 := NewArtifactManager(emptyDir)
	iterDir := filepath.Join(emptyDir, "iteration-01")
	os.MkdirAll(iterDir, 0o755)
	_ = am2.WriteMeta(iterDir, IterationMeta{Iteration: 1, AgentStatus: agentStatusFailed})
	tr2 := newImplementRoundCommitTracker(am2, emptyDir, 1)
	if tr2.semanticSessions {
		t.Error("semanticSessions = true, want false after only a FAILED round")
	}
	if tr2.changesRequested != 0 {
		t.Errorf("changesRequested = %d, want 0", tr2.changesRequested)
	}
}

// TestNewFRRoundCommitTracker_RecoveryFromMetas seeds the FR fix counter from
// lowercase "changes_requested" review statuses.
func TestNewFRRoundCommitTracker_RecoveryFromMetas(t *testing.T) {
	artifactDir := t.TempDir()
	am := NewArtifactManager(artifactDir)
	for iter, reviewStatus := range map[int]string{1: "changes_requested", 2: "changes_requested", 3: "approved"} {
		iterDir := filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", iter))
		if err := os.MkdirAll(iterDir, 0o755); err != nil {
			t.Fatalf("mkdir iteration dir: %v", err)
		}
		if err := am.WriteMeta(iterDir, IterationMeta{Iteration: iter, ReviewStatus: reviewStatus}); err != nil {
			t.Fatalf("write meta: %v", err)
		}
	}
	tr := newFRRoundCommitTracker(am, artifactDir, 3)
	if tr.changesRequested != 2 {
		t.Errorf("changesRequested = %d, want 2", tr.changesRequested)
	}
}
