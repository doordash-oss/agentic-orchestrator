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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// newFRTestFeature seeds a multi-repo feature whose RepoImpl entries are all
// at "awaiting_final_review" — the precondition for the unified Final
// Review pass. The store is the real on-disk store so AtomicPhaseStamp's
// transactional writes round-trip through Modify/Load.
func newFRTestFeature(t *testing.T, stateDir, featureID string, repoNames []string) (*feature.Store, *feature.Feature, []string) {
	t.Helper()
	store := feature.NewStore(stateDir)
	repos := make([]feature.FeatureRepo, 0, len(repoNames))
	repoPaths := make([]string, 0, len(repoNames))
	repoStates := map[string]*feature.RepoState{}
	for _, name := range repoNames {
		repoDir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatalf("mkdir repo %q: %v", name, err)
		}
		repos = append(repos, feature.FeatureRepo{
			Name:       name,
			Path:       repoDir,
			BaseBranch: "main",
		})
		repoPaths = append(repoPaths, repoDir)
		// Touched=true so the FR loop's TouchedRepos reader sees this
		// repo as part of the staged subset.
		repoStates[name] = &feature.RepoState{Touched: true}
	}
	f := &feature.Feature{
		ID:                  featureID,
		Name:                "Final Review Loop Test",
		Slug:                "fr-loop-test",
		Description:         "Feature-level Final Review test fixture",
		Status:              feature.StatusFinalReviewing,
		CurrentPhase:        feature.PhaseReview,
		ActiveRun:           1,
		RunCount:            1,
		SchemaVersion:       feature.SchemaVersionCurrent,
		Repos:               repos,
		RepoStates:          repoStates,
		ExitCriteria:        "Relevant tests pass",
		CurrentRoadmapPhase: 1,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	return store, f, repoPaths
}

// frLoopTestEnv encapsulates the per-test directory layout the new loop
// expects (state dir + per-feature subdir, scripts dir).
type frLoopTestEnv struct {
	stateDir   string
	scriptsDir string
}

func newFRLoopEnv(t *testing.T) frLoopTestEnv {
	t.Helper()
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	scriptsDir := filepath.Join(tmp, "scripts")
	for _, d := range []string{stateDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	return frLoopTestEnv{stateDir: stateDir, scriptsDir: scriptsDir}
}

// frArtifactDir returns runs/run-NNN/review/ for the FR-loop feature.
func frArtifactDir(stateDir string, f *feature.Feature) string {
	return filepath.Join(ActiveRunDir(stateDir, f), feature.PhaseReview.DirName())
}

func runFinalReviewWithFixScript(t *testing.T, fixBody func(artDir string) string) *FeatureFinalReviewResult {
	t.Helper()
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-fix-protocol", []string{"api", "web"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: keep requesting changes")+"\n"+
			testutil.JSONLSuccess+"\n")

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			fixBody(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  10,
		MaxConsecFails: 2,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    fixScript,
		}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	return result
}

func runFinalReviewWithReviewScript(t *testing.T, reviewBody func(artDir string) string) *FeatureFinalReviewResult {
	t.Helper()
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-reviewer-protocol", []string{"api", "web"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			reviewBody(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  10,
		MaxConsecFails: 2,
		BuildSession:   mockBuildSessionByModel(map[string]string{"reviewer": reviewScript}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	return result
}

func TestRunFeatureFinalReviewLoop_SessionsCarryFinalReviewPhase(t *testing.T) {
	t.Parallel()
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-session-phase", []string{"api", "web"})
	f.CurrentPhase = feature.PhaseFinalReview
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature current phase: %v", err)
	}

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		iterDir := latestFinalReviewIterationDir(t, artDir)
		switch {
		case strings.Contains(id, "-final-review-01"):
			writeFinalReviewFeedbackFile(t, iterDir, testutil.StructuredReviewFeedback("- **High**: needs final review fix", "", "CHANGES_REQUESTED"))
			touchPhaseComplete(t, iterDir)
		case strings.Contains(id, "-fix-01"):
			markFinalReviewReportPassed(t, iterDir)
			touchPhaseComplete(t, iterDir)
		case strings.Contains(id, "-final-review-02"):
			writeFinalReviewFeedbackFile(t, iterDir, testutil.StructuredReviewFeedback("", "", "APPROVED"))
			touchPhaseComplete(t, iterDir)
		default:
			t.Fatalf("unexpected session id %q", id)
		}

		sess := session.NewSession(id, featureID, phase)
		sess.SetCost(&llm.ResultMessage{Type: "result", Subtype: "success", SessionID: "mock"})
		sess.SendStatus(agentStatusSuccess)
		sess.CloseDone()
		return sess, nil
	}

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  3,
		MaxConsecFails: 3,
		BuildSession: func(BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			return []string{"mock"}, nil, &session.SessionOpts{}, nil
		},
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed; last error: %s", result.FinalStatus, result.LastError)
	}

	var got []string
	for _, call := range sm.StartSessionCalls {
		got = append(got, fmt.Sprintf("%s=%s", call.ID, call.Phase))
		if call.Phase != feature.PhaseFinalReview {
			t.Fatalf("StartSession(%q) phase = %s, want %s; all calls: %v", call.ID, call.Phase, feature.PhaseFinalReview, got)
		}
	}
	if len(sm.StartSessionCalls) != 3 {
		t.Fatalf("StartSession call count = %d, want 3; calls: %v", len(sm.StartSessionCalls), got)
	}
}

func latestFinalReviewIterationDir(t *testing.T, artDir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(artDir, "iteration-*"))
	if err != nil {
		t.Fatalf("glob final review iterations: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("no final review iterations under %s", artDir)
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}

func writeFinalReviewFeedbackFile(t *testing.T, iterDir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(iterDir, "review-feedback.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write final review feedback: %v", err)
	}
}

func touchPhaseComplete(t *testing.T, iterDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(iterDir, PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("touch %s: %v", PhaseCompleteFile, err)
	}
}

func markFinalReviewReportPassed(t *testing.T, iterDir string) {
	t.Helper()
	reportPath := filepath.Join(iterDir, "verification-report.yaml")
	report, err := ReadVerificationReport(reportPath)
	if err != nil {
		t.Fatalf("read final review verification report: %v", err)
	}
	markVerificationRowsPassed(report.Results)
	markVerificationRowsPassed(report.RequiredChecks)
	markVerificationRowsPassed(report.AdditionalChecks)
	if err := WriteVerificationReport(reportPath, *report); err != nil {
		t.Fatalf("write final review verification report: %v", err)
	}
}

func markVerificationRowsPassed(rows []VerificationCheckResult) {
	for i := range rows {
		rows[i].Status = VerificationStatusPassed
		rows[i].Evidence = "mock final review contract check passed"
	}
}

func TestPriorImplementationEvidenceContextForRunListsReferencedEvidenceArtifacts(t *testing.T) {
	runDir := t.TempDir()
	phaseDir := filepath.Join(runDir, "phase-01")
	if err := os.MkdirAll(filepath.Join(phaseDir, "plan"), 0o755); err != nil {
		t.Fatalf("mkdir plan dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, "plan", "phase-plan.md"), []byte("# Phase plan\n"), 0o644); err != nil {
		t.Fatalf("write phase plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, "testing-contract.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatalf("write testing contract: %v", err)
	}
	implementDir := filepath.Join(phaseDir, feature.PhaseImplement.DirName())
	am := NewArtifactManager(implementDir)
	iterDir, err := am.CreateIterationDir(2)
	if err != nil {
		t.Fatalf("CreateIterationDir() error = %v", err)
	}
	if err := am.WriteMeta(iterDir, IterationMeta{
		Iteration:    2,
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: ReviewApproved.String(),
	}); err != nil {
		t.Fatalf("WriteMeta() error = %v", err)
	}
	report := strings.Join([]string{
		"version: 2",
		"results:",
		"  - item_id: visual_1",
		"    name: Setup wizard",
		"    mode: visual",
		"    status: passed",
		"    evidence:",
		"      summary: Captured setup wizard",
		"      primary: screenshots/setup.png",
		"      attachments:",
		"        - screenshots/setup-detail.png",
	}, "\n")
	if err := os.WriteFile(filepath.Join(iterDir, "verification-report.yaml"), []byte(report), 0o644); err != nil {
		t.Fatalf("write verification report: %v", err)
	}

	ctx := priorImplementationEvidenceContextForRun(runDir)
	for _, want := range []string{
		filepath.Join(iterDir, "verification-report.yaml"),
		iterDir,
		filepath.Join(iterDir, "screenshots", "setup.png"),
		filepath.Join(iterDir, "screenshots", "setup-detail.png"),
	} {
		if !containsPriorEvidencePath(ctx, want) {
			t.Errorf("priorImplementationEvidenceContextForRun() missing %s: %+v", want, ctx)
		}
	}
	for _, unwanted := range []string{
		filepath.Join(phaseDir, "plan", "phase-plan.md"),
		filepath.Join(phaseDir, "testing-contract.yaml"),
	} {
		if containsPriorEvidencePath(ctx, unwanted) {
			t.Errorf("priorImplementationEvidenceContextForRun() includes phase-planning artifact %s: %+v", unwanted, ctx)
		}
	}
}

func containsPriorEvidencePath(ctx priorImplementationEvidenceContext, want string) bool {
	for _, paths := range [][]string{
		ctx.ReportPaths,
		ctx.EvidenceRootDirs,
		ctx.EvidenceArtifactPaths,
	} {
		for _, path := range paths {
			if path == want {
				return true
			}
		}
	}
	return false
}

func shellSingleQuoteForFRTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// TestRunFeatureFinalReviewLoop_ReviewApprovedAtomicallyStampsAllRepos covers
// the SUCCESS path: one feature-level reviewer session approves on iter-1;
// every repo at "awaiting_final_review" transitions atomically to
// "review_passed".
func TestRunFeatureFinalReviewLoop_ReviewApprovedAtomicallyStampsAllRepos(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-success", []string{"api", "web", "infra"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	approveScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLAssistant("Review complete: APPROVED")+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  3,
		MaxConsecFails: 3,
		BuildSession:   mockBuildSessionByModel(map[string]string{"reviewer": approveScript}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if got, want := result.Iterations, 1; got != want {
		t.Errorf("Iterations = %d, want %d", got, want)
	}
	if got, want := len(result.Repos), 3; got != want {
		t.Errorf("Repos len = %d, want %d", got, want)
	}

	// AtomicPhaseStamp must have transitioned every repo to ReviewPassed.
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"api", "web", "infra"} {
		st := loaded.RepoStates[name]
		if st == nil || !st.Touched {
			t.Errorf("repo %s = %+v, want review_passed", name, st)
		}
	}
}

// TestRunFeatureFinalReviewLoop_FinishOrViolateNudgeRecoversSameSession proves
// the FR review leg recovers within one iteration via the finish-or-violate
// nudge: the reviewer ends its first turn without the FR completion artifacts,
// the harness nudges the same live session, and the nudged turn writes the
// APPROVED feedback + phase_complete so the loop ends review_passed.
func TestRunFeatureFinalReviewLoop_FinishOrViolateNudgeRecoversSameSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-nudge", []string{"api", "web"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh", fmt.Sprintf(`%s
echo '{"type":"result","subtype":"success","session_id":"mock","total_cost_usd":0.001,"stop_reason":"end_turn"}'
while IFS= read -r _line; do
  case "$_line" in
    %s)
      %s
      echo '{"type":"result","subtype":"success","session_id":"mock","total_cost_usd":0.001,"stop_reason":"end_turn"}'
      exit 0
      ;;
  esac
done
`, testutil.JSONLInit, finishOrViolateNudgeCasePattern, testutil.WriteFinalReviewApproved(artDir)))

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:              f,
		FeatureStore:         store,
		StateDir:             env.stateDir,
		Model:                "agent",
		ReviewModel:          "reviewer",
		MaxIterations:        3,
		MaxConsecFails:       3,
		FinishOrViolateNudge: true,
		BuildSession:         mockBuildSessionByModel(map[string]string{"reviewer": reviewScript}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if got, want := result.Iterations, 1; got != want {
		t.Errorf("Iterations = %d, want %d (recovered within the first iteration)", got, want)
	}
}

func TestRunFeatureFinalReviewLoop_ReviewerRepoMutationTripsProtocolViolationAndRestores(t *testing.T) {
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-reviewer-repo-mutation", []string{"api"})
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

	readmePath := filepath.Join(repo, "README.md")
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			fmt.Sprintf("cat > %s <<'EOF'\n# Mutated by reviewer\nEOF\n", shellSingleQuoteForFRTest(readmePath))+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  1,
		MaxConsecFails: 1,
		CommandRunner:  NewExecCommandRunner(),
		BuildSession:   mockBuildSessionByModel(map[string]string{"reviewer": reviewScript}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != "protocol_violation" {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "read-only Final Review phase modified target repo api") {
		t.Fatalf("LastError missing repo-mutation violation:\n%s", result.LastError)
	}
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README after restore: %v", err)
	}
	if got, want := string(data), "# Test\n"; got != want {
		t.Fatalf("README after restore = %q, want %q", got, want)
	}
	matches, err := filepath.Glob(filepath.Join(artDir, "iteration-01", "violations", "repo-mutation-*.patch"))
	if err != nil {
		t.Fatalf("glob violation patches: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("violation patch count = %d, want 1 (%v)", len(matches), matches)
	}
	patch, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read violation patch: %v", err)
	}
	if got := string(patch); !strings.Contains(got, "README.md") || !strings.Contains(got, "# Mutated by reviewer") {
		t.Fatalf("violation patch missing reviewer mutation:\n%s", got)
	}
}

// TestRunFeatureFinalReviewLoop_ChangesRequestedThenFixApproves drives the
// inverted iteration order: review FIRST returns CHANGES_REQUESTED, fix runs
// in the same iteration, iter-2's review APPROVES. End state: every repo
// transitions to "review_passed".
func TestRunFeatureFinalReviewLoop_ChangesRequestedThenFixApproves(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-changes", []string{"api", "web"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	// Review: CHANGES_REQUESTED on iter-1, APPROVED on iter-2+.
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		fmt.Sprintf(`if [ -d "%s/iteration-02" ]; then
%s
%s
%s
%s
%s
else
%s
%s
%s
%s
%s
fi`,
			artDir,
			testutil.JSONLInit,
			testutil.WriteFinalReviewApproved(artDir),
			testutil.JSONLAssistant("APPROVED"),
			testutil.WriteFinalReviewApproved(artDir),
			testutil.JSONLSuccess,
			testutil.JSONLInit,
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: Cross-repo type signature mismatch between api and web"),
			testutil.JSONLAssistant("CHANGES_REQUESTED"),
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: Cross-repo type signature mismatch between api and web"),
			testutil.JSONLSuccess))

	// Fix agent succeeds.
	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewFixSuccessArtifacts(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

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
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", result.Iterations)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || !st.Touched {
			t.Errorf("repo %s = %+v, want review_passed", name, st)
		}
	}
}

func TestRunFeatureFinalReviewLoop_ChangesRequestedWithMalformedReportStillRunsFix(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-malformed-report-fix", []string{"api", "web"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	malformedReport := fmt.Sprintf(`for _d in "%s"/iteration-*; do :; done
cat > "$_d/verification-report.yaml" << 'REPORT'
version: 2
additional_checks:
  - name: Source comparison spot-check
    command: manual: compare translated README against English source
    mode: manual
    status: failed
REPORT
`, artDir)

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		fmt.Sprintf(`if [ -d "%s/iteration-02" ]; then
%s
%s
%s
else
%s
%s
%s
%s
fi`,
			artDir,
			testutil.JSONLInit,
			testutil.WriteFinalReviewApproved(artDir),
			testutil.JSONLSuccess,
			testutil.JSONLInit,
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: README still contains untranslated text"),
			malformedReport+testutil.TouchPhaseComplete(artDir),
			testutil.JSONLSuccess))

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		fmt.Sprintf(`%s
for _d in "%s"/iteration-*; do :; done
if grep -q 'command: manual: compare' "$_d/verification-report.yaml"; then
  echo "malformed reviewer verification report leaked to final fixer" >&2
  exit 1
fi
%s
%s
`,
			testutil.JSONLInit,
			artDir,
			testutil.WriteFinalReviewFixSuccessArtifacts(artDir),
			testutil.JSONLSuccess))

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

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
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed; last error: %s", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", result.Iterations)
	}
}

// TestRunFeatureFinalReviewLoop_FixAgentSeesAllReposInAddDir verifies that
// when the fix agent runs after CHANGES_REQUESTED, BuildSessionOpts mounts
// every Feature.Repos worktree via AdditionalDirs. Same workspace shape as
// the implementer.
func TestRunFeatureFinalReviewLoop_FixAgentSeesAllReposInAddDir(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repoPaths := newFRTestFeature(t, env.stateDir, "fr-fix-dirs", []string{"api", "web", "infra"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	// Review: CHANGES_REQUESTED then APPROVED.
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
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: needs cross-repo fix"),
			testutil.JSONLSuccess))

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewFixSuccessArtifacts(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	bs, captured := capturingBuildSessionByModel(map[string]string{
		"reviewer": reviewScript,
		"agent":    fixScript,
	})

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  3,
		MaxConsecFails: 3,
		BuildSession:   bs,
		SkillsDir:      filepath.Join(env.stateDir, "skills"),
		GuidelinesDir:  filepath.Join(env.stateDir, "guidelines"),
	}

	if _, err := RunFeatureFinalReviewLoop(cfg, sm); err != nil {
		t.Fatalf("loop: %v", err)
	}

	// Each fix-agent BuildSessionOpts must include every repo worktree as
	// an --add-dir entry.
	var fixCalls []BuildSessionOpts
	for _, opts := range *captured {
		if opts.Model == "agent" {
			fixCalls = append(fixCalls, opts)
		}
	}
	if len(fixCalls) == 0 {
		t.Fatal("fix agent BuildSession was never called")
	}
	for i, opts := range fixCalls {
		for _, want := range repoPaths {
			absWant, _ := filepath.Abs(want)
			if !sliceContains(opts.AdditionalDirs, absWant) {
				t.Errorf("fix call %d AdditionalDirs missing repo path %q (got %v)", i, absWant, opts.AdditionalDirs)
			}
		}
		for _, want := range []string{
			"## Output Roots",
			"`iteration_dir`:",
			filepath.Join(cfg.SkillsDir, "final-fix", "SKILL.md"),
			"## Completion",
		} {
			if !strings.Contains(opts.SystemPrompt, want) {
				t.Errorf("fix call %d SystemPrompt missing %q:\n%s", i, want, opts.SystemPrompt)
			}
		}
		if !opts.SystemPromptHasUsefulResources {
			t.Errorf("fix call %d SystemPromptHasUsefulResources = false, want true", i)
		}
	}
	// The reviewer call should also see every repo worktree.
	for _, opts := range *captured {
		if opts.Model != "reviewer" {
			continue
		}
		for _, want := range repoPaths {
			absWant, _ := filepath.Abs(want)
			if !sliceContains(opts.AdditionalDirs, absWant) {
				t.Errorf("reviewer AdditionalDirs missing repo path %q (got %v)", absWant, opts.AdditionalDirs)
			}
		}
		for _, want := range []string{
			"## Output Roots",
			"`iteration_dir`:",
			filepath.Join(cfg.SkillsDir, "final-review", "SKILL.md"),
			"## Completion",
		} {
			if !strings.Contains(opts.SystemPrompt, want) {
				t.Errorf("reviewer SystemPrompt missing %q:\n%s", want, opts.SystemPrompt)
			}
		}
		if !opts.SystemPromptHasUsefulResources {
			t.Errorf("reviewer SystemPromptHasUsefulResources = false, want true")
		}
		if got, want := strings.Join(opts.AgentNames, ","), strings.Join(explorationAgentNames(), ","); got != want {
			t.Errorf("reviewer AgentNames = %v, want exploration set %v", opts.AgentNames, explorationAgentNames())
		}
	}
}

// TestRunFeatureFinalReviewLoop_MaxIterationsAtomicFailureStamp covers the
// max-iterations safety rail: every iteration's reviewer says CHANGES_REQUESTED
// and the loop trips MaxIterations. AtomicPhaseStamp transitions every repo
// to "failed".
func TestRunFeatureFinalReviewLoop_MaxIterationsAtomicFailureStamp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-maxiter", []string{"api", "web"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: still wrong")+"\n"+
			testutil.JSONLSuccess+"\n")

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewFixSuccessArtifacts(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  2,
		MaxConsecFails: 5,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    fixScript,
		}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != "max_iterations" {
		t.Fatalf("FinalStatus = %q, want max_iterations", result.FinalStatus)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.LastError == "" {
			t.Errorf("repo %s = %+v, want failed", name, st)
		}
	}
}

// TestRunFeatureFinalReviewLoop_ConsecutiveFailuresSafetyRail covers the
// fix-agent safety rail: review keeps requesting changes and the fix agent
// fails (no phase_complete). The consecutive-failure counter trips and
// every repo lands at "failed".
func TestRunFeatureFinalReviewLoop_ConsecutiveFailuresSafetyRail(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-safetyrail", []string{"api", "web"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: keep failing")+"\n"+
			testutil.JSONLSuccess+"\n")

	// Fix agent fails: no phase_complete touch.
	failFixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  10,
		MaxConsecFails: 2,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    failFixScript,
		}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != "safety_rail" {
		t.Fatalf("FinalStatus = %q, want safety_rail", result.FinalStatus)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.LastError == "" {
			t.Errorf("repo %s = %+v, want failed", name, st)
		}
	}
}

func TestRunFeatureFinalReviewLoop_MissingEvidenceRunsFixAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-missing-evidence-fix", []string{"api", "web"})

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
			testutil.WriteFinalReviewChangesRequested(artDir, "- **Critical**: MISSING_EVIDENCE_REQUIREMENT behavioral: Record the create-project CLI journey."),
			testutil.JSONLSuccess))
	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewFixSuccessArtifacts(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  3,
		MaxConsecFails: 3,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    fixScript,
		}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.PlanRevisionFeedback != "" {
		t.Fatalf("PlanRevisionFeedback = %q, want empty", result.PlanRevisionFeedback)
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2 after fix-agent iteration", result.Iterations)
	}
	if _, err := os.Stat(filepath.Join(artDir, "iteration-01", "phase_complete")); err != nil {
		t.Fatalf("iteration-01 phase_complete missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(artDir, "iteration-02", "phase_complete")); err != nil {
		t.Fatalf("iteration-02 phase_complete missing: %v", err)
	}
}

func TestRunFeatureFinalReviewLoop_CoveredMissingEvidenceRunsFix(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-covered-evidence", []string{"app"})

	requirement := "Capture actual rendered README output showing the translated top section, one translated mixed-content table, and one preserved code/flags section."
	phaseDir := PhaseDir(env.stateDir, f, 1)
	planDir := filepath.Join(phaseDir, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir phase plan dir: %v", err)
	}
	planPath := filepath.Join(planDir, "phase-plan.md")
	plan := "# Phase 1\n\n### Visual Evidence\n\n- [ ] " + requirement + "\n"
	if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
		t.Fatalf("write phase plan: %v", err)
	}
	contract := CompileTestingContract(plan, planPath, "collapsed")
	if err := WriteTestingContract(filepath.Join(phaseDir, "testing-contract.yaml"), contract); err != nil {
		t.Fatalf("write phase testing contract: %v", err)
	}

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		fmt.Sprintf(`if [ -d "%s/iteration-02" ]; then
%s
%s
else
%s
%s
fi`,
			artDir,
			testutil.JSONLInit,
			testutil.WriteFinalReviewApproved(artDir)+"\n"+testutil.JSONLSuccess,
			testutil.JSONLInit,
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: MISSING_EVIDENCE_REQUIREMENT phase 1 visual: "+requirement)+"\n"+testutil.JSONLSuccess))

	fixMarker := filepath.Join(artDir, "fix-ran")
	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			fmt.Sprintf("touch %q\n", fixMarker)+
			testutil.WriteFinalReviewFixSuccessArtifacts(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  3,
		MaxConsecFails: 3,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    fixScript,
		}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed; feedback:\n%s", result.FinalStatus, result.PlanRevisionFeedback)
	}
	if _, err := os.Stat(fixMarker); err != nil {
		t.Fatalf("fix marker was not written: %v", err)
	}
}

func TestRunFeatureFinalReviewLoop_FixMissingPhaseCompleteTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithFixScript(t, func(string) string {
		return ""
	})

	if result.FinalStatus != "protocol_violation" {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "final_review_fixer @") ||
		!strings.Contains(result.LastError, "phase_complete") {
		t.Fatalf("LastError = %q, want final_review_fixer phase_complete violation", result.LastError)
	}
}

func TestRunFeatureFinalReviewLoop_FixMissingVerificationReportTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithFixScript(t, func(artDir string) string {
		return fmt.Sprintf(`for _d in "%s"/iteration-*; do :; done
rm -f "$_d/verification-report.yaml"
touch "$_d/phase_complete"`, artDir)
	})

	if result.FinalStatus != "protocol_violation" {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "final_review_fixer @") ||
		!strings.Contains(result.LastError, "verification-report.yaml") {
		t.Fatalf("LastError = %q, want final_review_fixer verification-report violation", result.LastError)
	}
}

func TestRunFeatureFinalReviewLoop_FixRejectedVerificationReportTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithFixScript(t, func(artDir string) string {
		return testutil.TouchPhaseComplete(artDir)
	})

	if result.FinalStatus != "protocol_violation" {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "final_review_fixer @") ||
		!strings.Contains(result.LastError, "not_run") {
		t.Fatalf("LastError = %q, want final_review_fixer not_run violation", result.LastError)
	}
}

func TestRunFeatureFinalReviewLoop_ReviewerMissingPhaseCompleteTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithReviewScript(t, func(artDir string) string {
		return testutil.WriteReviewApproved(artDir)
	})

	if result.FinalStatus != "protocol_violation" {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "final_reviewer @") ||
		!strings.Contains(result.LastError, "phase_complete") {
		t.Fatalf("LastError = %q, want final_reviewer phase_complete violation", result.LastError)
	}
}

func TestRunFeatureFinalReviewLoop_ReviewerMissingFeedbackTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithReviewScript(t, func(artDir string) string {
		return testutil.TouchPhaseComplete(artDir)
	})

	if result.FinalStatus != "protocol_violation" {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "final_reviewer @") ||
		!strings.Contains(result.LastError, "review-feedback.md") {
		t.Fatalf("LastError = %q, want final_reviewer review-feedback violation", result.LastError)
	}
}

func TestRunFeatureFinalReviewLoop_ReviewerMalformedVerdictTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithReviewScript(t, func(artDir string) string {
		return testutil.WriteFinalReviewMalformedVerdictLatest(artDir)
	})

	if result.FinalStatus != "protocol_violation" {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "final_reviewer @") ||
		!strings.Contains(result.LastError, "LGTM") {
		t.Fatalf("LastError = %q, want malformed verdict violation", result.LastError)
	}
}

func TestRunFeatureFinalReviewLoop_ReviewerApprovedWithoutVerificationReportPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithReviewScript(t, func(artDir string) string {
		return fmt.Sprintf(`for _d in "%s"/iteration-*; do :; done
rm -f "$_d/verification-report.yaml"
%s`, artDir, testutil.WriteFinalReviewApproved(artDir))
	})

	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
}

// TestRunFeatureFinalReviewLoop_CrashRecoveryResumesFromInterruptedIter
// verifies mid-iteration crash recovery: the harness pre-creates iteration-01
// with CHANGES_REQUESTED meta. The loop must resume at iteration-02.
func TestRunFeatureFinalReviewLoop_CrashRecoveryResumesFromInterruptedIter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-resume", []string{"api"})

	artDir := frArtifactDir(env.stateDir, f)
	iter01 := filepath.Join(artDir, "iteration-01")
	if err := os.MkdirAll(iter01, 0o755); err != nil {
		t.Fatalf("mkdir iter01: %v", err)
	}
	am := NewArtifactManager(artDir)
	if err := am.WriteMeta(iter01, IterationMeta{
		Iteration:    1,
		ReviewStatus: "changes_requested",
		StartedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("write iter01 meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(iter01, "review-feedback.md"), []byte("Old feedback"), 0o644); err != nil {
		t.Fatalf("seed prior feedback: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLAssistant("APPROVED")+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewFixSuccessArtifacts(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

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
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2 (resumed from interrupted iter-01)", result.Iterations)
	}
	if _, err := os.Stat(filepath.Join(artDir, "iteration-02")); err != nil {
		t.Errorf("iteration-02 missing — loop did not resume: %v", err)
	}
}

// TestRunFeatureFinalReviewLoop_NoStagedReposShortCircuits verifies the
// degenerate "every repo already past FR" case returns review_passed
// without launching a session. Mirrors the legacy short-circuit behavior
// for callers that defensively invoke the FR pass.
func TestRunFeatureFinalReviewLoop_NoStagedReposShortCircuits(t *testing.T) {
	env := newFRLoopEnv(t)
	store := feature.NewStore(env.stateDir)
	f := &feature.Feature{
		ID:            "fr-empty",
		Name:          "empty",
		Slug:          "empty",
		Status:        feature.StatusFinalReviewing,
		CurrentPhase:  feature.PhaseReview,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "api", Path: filepath.Join(env.stateDir, "api"), BaseBranch: "main"},
		},
		// All repos untouched — none staged for FR (the loop short-circuits
		// when TouchedRepos() is empty).
		RepoStates: map[string]*feature.RepoState{
			"api": {},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg := OrchestratorConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     env.stateDir,
	}
	result, err := RunFeatureFinalReviewLoop(cfg, nil)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Errorf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
}

// TestRunFeatureFinalReviewLoop_DoesNotPersistReviewerTestingContract verifies
// Final Review is an autonomous product review whose only reviewer-authored
// contract artifact is review-feedback.md.
func TestRunFeatureFinalReviewLoop_DoesNotPersistReviewerTestingContract(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-contract", []string{"api", "web"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	approveScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  3,
		MaxConsecFails: 3,
		BuildSession:   mockBuildSessionByModel(map[string]string{"reviewer": approveScript}),
	}

	if _, err := RunFeatureFinalReviewLoop(cfg, sm); err != nil {
		t.Fatalf("loop: %v", err)
	}

	contractPath := filepath.Join(artDir, "testing-contract.yaml")
	if _, err := os.Stat(contractPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("testing contract stat error = %v, want not exists", err)
	}
}

// TestRunMultiRepoFinalReview_RunFinalReviewFnSeam asserts the
// OrchestratorConfig.RunFinalReviewFn test seam short-circuits the FR
// dispatch — production tests can replace the engine without launching real
// sessions. Mirrors how SetRunMultiRepoFinalReviewFn wires the orchestrator-
// level seam.
func TestRunMultiRepoFinalReview_RunFinalReviewFnSeam(t *testing.T) {
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-seam", []string{"api", "web"})

	called := 0
	cfg := OrchestratorConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     env.stateDir,
		RunFinalReviewFn: func(_ OrchestratorConfig, _ ports.SessionManager) (*FeatureFinalReviewResult, error) {
			called++
			return &FeatureFinalReviewResult{FinalStatus: "review_passed", Iterations: 1, Repos: []string{"api", "web"}}, nil
		},
	}

	result, err := RunMultiRepoFinalReview(cfg, nil)
	if err != nil {
		t.Fatalf("RunMultiRepoFinalReview: %v", err)
	}
	if called != 1 {
		t.Errorf("seam called %d times, want 1", called)
	}
	if result.FinalStatus != "all_passed" {
		t.Errorf("FinalStatus = %q, want all_passed", result.FinalStatus)
	}
}

func TestRunMultiRepoFinalReview_ProtocolViolationStatusPreserved(t *testing.T) {
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-protocol-status", []string{"api", "web"})

	cfg := OrchestratorConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     env.stateDir,
		RunFinalReviewFn: func(_ OrchestratorConfig, _ ports.SessionManager) (*FeatureFinalReviewResult, error) {
			return &FeatureFinalReviewResult{
				FinalStatus: "protocol_violation",
				LastError:   "protocol violation: final_review_fixer @ /tmp/iter: verification-report.yaml is missing",
				Repos:       []string{"api", "web"},
			}, nil
		},
	}

	result, err := RunMultiRepoFinalReview(cfg, nil)
	if err != nil {
		t.Fatalf("RunMultiRepoFinalReview() error = %v", err)
	}
	if result.FinalStatus != "failed" {
		t.Fatalf("FinalStatus = %q, want failed", result.FinalStatus)
	}
	for _, repo := range []string{"api", "web"} {
		if result.RepoStatuses[repo] != "protocol_violation" {
			t.Fatalf("RepoStatuses[%q] = %q, want protocol_violation (all statuses: %#v)", repo, result.RepoStatuses[repo], result.RepoStatuses)
		}
	}
}

// sliceContains is a small helper for AdditionalDirs assertions.
func sliceContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
