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
	"slices"
	"sort"
	"strings"
	"sync"
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
	// Touched=true (via OmitPRURL, no PR URL needed) so the FR loop's
	// TouchedRepos reader sees each repo as part of the staged subset.
	return newLoopTestFeature(t, stateDir, featureID, repoNames, loopTestFeatureOptions{
		Name:                "Final Review Loop Test",
		Slug:                "fr-loop-test",
		Description:         "Feature-level Final Review test fixture",
		ExitCriteria:        "Relevant tests pass",
		Status:              feature.StatusFinalReviewing,
		CurrentPhase:        feature.PhaseReview,
		CurrentRoadmapPhase: 1,
		OmitPRURL:           true,
	})
}

// frLoopTestEnv encapsulates the per-test directory layout the new loop
// expects (state dir + per-feature subdir, scripts dir).
type frLoopTestEnv struct {
	stateDir   string
	scriptsDir string
}

type lockedMockSessionManager struct {
	*mocks.MockSessionManager
	startMu sync.Mutex
}

func (m *lockedMockSessionManager) StartSession(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	return m.MockSessionManager.StartSession(id, featureID, phase, command, workdir, env, opts...)
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

func runFinalReviewWithFixScript(t *testing.T, fixBody func(artDir string) string, resultScript string) *FeatureFinalReviewResult {
	t.Helper()
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-fix-protocol", []string{testRepoNameAPI, testRepoNameWeb})

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
			resultScript+"\n")

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

	result, err := runFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	return result
}

func runFinalReviewWithReviewScript(t *testing.T, reviewBody func(artDir string) string) *FeatureFinalReviewResult {
	t.Helper()
	return runFinalReviewWithReviewScriptAndResult(t, reviewBody, testutil.JSONLSuccess)
}

func runFinalReviewWithReviewScriptAndResult(t *testing.T, reviewBody func(artDir string) string, resultScript string) *FeatureFinalReviewResult {
	t.Helper()
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-reviewer-protocol", []string{testRepoNameAPI, testRepoNameWeb})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			reviewBody(artDir)+"\n"+
			resultScript+"\n")

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

	result, err := runFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	return result
}

func TestRunFeatureFinalReviewLoop_SessionsCarryFinalReviewPhase(t *testing.T) {
	t.Parallel()
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-session-phase", []string{testRepoNameAPI, testRepoNameWeb})
	f.CurrentPhase = feature.PhaseFinalReview
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature current phase: %v", err)
	}

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	sm := &lockedMockSessionManager{MockSessionManager: mocks.NewMockSessionManager()}
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		iterDir := latestFinalReviewIterationDir(t, artDir)
		switch {
		case strings.Contains(id, "-fix-01"):
			// The fix session only emits its root outcome.
		case strings.HasSuffix(id, "-01"), strings.HasSuffix(id, "-02"):
			iteration := 1
			feedback := testutil.StructuredReviewFeedback("- **High**: needs final review fix", "", agentStatusChangesRequested)
			if strings.HasSuffix(id, "-02") {
				iteration = 2
				feedback = testutil.StructuredReviewFeedback("", "", agentStatusApproved)
			}
			suffix := fmt.Sprintf("-%02d", iteration)
			axisSlug := strings.TrimSuffix(strings.TrimPrefix(id, f.ID+"-"), suffix)
			axisDir := filepath.Join(iterDir, axisSlug)
			writeFinalReviewFeedbackFile(t, axisDir, feedback)
		default:
			t.Fatalf("unexpected session id %q", id)
		}

		sess := newUtilityTestSession()
		sess.IDVal = id
		sess.FeatureIDVal = featureID
		sess.PhaseVal = phase
		sess.setRootIntent(validSuccessCompletionIntent())
		sess.CostVal = &llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, SessionID: testMockIdentifier, StopReason: "end_turn"}
		sess.StatusChVal <- agentStatusSuccess
		close(sess.DoneChVal)
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
			return []string{testMockIdentifier}, nil, &session.SessionOpts{}, nil
		},
	}

	result, err := runFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed; last error: %s", result.FinalStatus, result.LastError)
	}

	var got []string
	for _, call := range sm.StartSessionCalls {
		got = append(got, fmt.Sprintf("%s=%s", call.ID, call.Phase))
		if call.Phase != feature.PhaseFinalReview {
			t.Fatalf("StartSession(%q) phase = %s, want %s; all calls: %v", call.ID, call.Phase, feature.PhaseFinalReview, got)
		}
	}
	if len(sm.StartSessionCalls) != 7 {
		t.Fatalf("StartSession call count = %d, want 7 (three axes, fix, three re-review axes); calls: %v", len(sm.StartSessionCalls), got)
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
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-success", []string{testRepoNameAPI, testRepoNameWeb, testRepoNameInfra})

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

	result, err := runFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
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
	for _, name := range []string{testRepoNameAPI, testRepoNameWeb, testRepoNameInfra} {
		st := loaded.RepoStates[name]
		if st == nil || !st.Touched {
			t.Errorf("repo %s = %+v, want review_passed", name, st)
		}
	}
}

// TestRunFeatureFinalReviewLoop_FinishOrViolateNudgeRecoversSameSession proves
// the FR review leg recovers within one iteration via the finish-or-violate
// nudge: the reviewer ends its first turn without a root outcome, the harness
// nudges the same live session, and the nudged turn writes APPROVED feedback
// plus a structured outcome so the loop ends review_passed.
func TestRunFeatureFinalReviewLoop_FinishOrViolateNudgeRecoversSameSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-nudge", []string{testRepoNameAPI, testRepoNameWeb})

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
      %s
      exit 0
      ;;
  esac
done
`, testutil.JSONLInit, finishOrViolateNudgeCasePattern, testutil.WriteFinalReviewApproved(artDir), testutil.JSONLSuccess))

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
		BuildSession: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			command, env, sessOpts, err := mockBuildSessionByModel(map[string]string{"reviewer": reviewScript})(opts)
			if sessOpts != nil {
			}
			return command, env, sessOpts, err
		},
	}

	result, err := runFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if got, want := result.Iterations, 1; got != want {
		t.Errorf("Iterations = %d, want %d (recovered within the first iteration)", got, want)
	}
}

func TestRunFeatureFinalReviewLoop_ReviewerRepoMutationTripsProtocolViolationAndRestores(t *testing.T) {
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-reviewer-repo-mutation", []string{testRepoNameAPI})
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

	result, err := runFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
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
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-changes", []string{testRepoNameAPI, testRepoNameWeb})

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
			testutil.JSONLAssistant(agentStatusApproved),
			testutil.WriteFinalReviewApproved(artDir),
			testutil.JSONLSuccess,
			testutil.JSONLInit,
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: Cross-repo type signature mismatch between api and web"),
			testutil.JSONLAssistant(agentStatusChangesRequested),
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: Cross-repo type signature mismatch between api and web"),
			testutil.JSONLSuccess))

	// Fix agent succeeds.
	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
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

	result, err := runFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", result.Iterations)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{testRepoNameAPI, testRepoNameWeb} {
		st := loaded.RepoStates[name]
		if st == nil || !st.Touched {
			t.Errorf("repo %s = %+v, want review_passed", name, st)
		}
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
	store, f, repoPaths := newFRTestFeature(t, env.stateDir, "fr-fix-dirs", []string{testRepoNameAPI, testRepoNameWeb, testRepoNameInfra})

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

	if _, err := runFeatureFinalReviewLoop(cfg, sm); err != nil {
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
			"`helper_dir`:",
			filepath.Join(cfg.SkillsDir, "review-implementation-"),
			"## Completion",
		} {
			if !strings.Contains(opts.SystemPrompt, want) {
				t.Errorf("reviewer SystemPrompt missing %q:\n%s", want, opts.SystemPrompt)
			}
		}
		if got, want := strings.Join(opts.AgentNames, ","), strings.Join(explorationAgentNames(), ","); got != want {
			t.Errorf("reviewer AgentNames = %v, want exploration set %v", opts.AgentNames, explorationAgentNames())
		}
	}
}

func TestRunFeatureFinalReviewLoop_FinalGateRunsAxesAndAggregates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-multi-axis", []string{"api", "web"})
	f.Pipeline = feature.PipelineMedium
	f.TraceID = "trace-fr-multi-axis"
	// Real repositories, so later rounds can name their fix delta.
	for i := range f.Repos {
		repo := testutil.InitGitRepo(t)
		f.Repos[i].Path, f.Repos[i].WorktreePath = repo, repo
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}
	observeDir := filepath.Join(env.stateDir, "observe")
	if err := os.MkdirAll(filepath.Join(observeDir, f.ID), 0o755); err != nil {
		t.Fatalf("mkdir observe feature dir: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review-axes.sh",
		testutil.JSONLInit+"\n"+
			writeFinalAxisFeedbackScript(artDir, true)+"\n"+
			testutil.JSONLSuccess+"\n")
	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			frCommitChange(f.Repos[0].Path)+"\n"+
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
		Observer:       observerFromModule(t, true, observeDir, "test"),
	}

	result, err := runFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed; LastError=%q", result.FinalStatus, result.LastError)
	}
	// QA requests changes in round 1; round 2 reruns only QA; round 3 is the
	// one-time confirmation of the Craft and Cleanliness approvals that
	// predate the fix.
	if result.Iterations != 3 {
		t.Fatalf("Iterations = %d, want 3 (changes requested, follow-up, confirmation)", result.Iterations)
	}
	for iteration, axes := range map[string][]string{
		"iteration-01": {"craft", "cleanliness", "qa"},
		"iteration-02": {"qa"},
		"iteration-03": {"craft", "cleanliness"},
	} {
		for _, slug := range []string{"craft", "cleanliness", "qa"} {
			_, err := os.Stat(filepath.Join(artDir, iteration, slug, "review-prompt.md"))
			if ran := err == nil; ran != slices.Contains(axes, slug) {
				t.Fatalf("%s %s ran = %v, want %v", iteration, slug, ran, slices.Contains(axes, slug))
			}
		}
	}
	followUp, err := os.ReadFile(filepath.Join(artDir, "iteration-02", "qa", "review-prompt.md"))
	if err != nil {
		t.Fatalf("read follow-up prompt: %v", err)
	}
	for _, want := range []string{
		"## Round Scope: Follow-Up",
		filepath.Join(artDir, "iteration-01", "qa", "review-feedback.md"),
		"Rerun the checks behind your open findings on the current commit.",
	} {
		if !strings.Contains(string(followUp), want) {
			t.Fatalf("follow-up prompt missing %q:\n%s", want, followUp)
		}
	}
	confirm, err := os.ReadFile(filepath.Join(artDir, "iteration-03", "craft", "review-prompt.md"))
	if err != nil {
		t.Fatalf("read confirmation prompt: %v", err)
	}
	if !strings.Contains(string(confirm), "## Round Scope: Confirmation") {
		t.Fatalf("confirmation prompt missing round scope:\n%s", confirm)
	}

	for _, axisSlug := range []string{"craft", "cleanliness", "qa"} {
		path := filepath.Join(artDir, "iteration-01", axisSlug, "review-feedback.md")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("axis feedback %s missing: %v", path, err)
		}
	}

	aggregatePath := filepath.Join(artDir, "iteration-01", "review-feedback.md")
	aggregate, err := os.ReadFile(aggregatePath)
	if err != nil {
		t.Fatalf("read aggregate feedback: %v", err)
	}
	for _, want := range []string{
		"### Craft",
		"### Cleanliness",
		"### QA",
		"feature fails launched smoke journey",
		"## Verdict\nCHANGES_REQUESTED",
	} {
		if !strings.Contains(string(aggregate), want) {
			t.Fatalf("aggregate feedback missing %q in:\n%s", want, aggregate)
		}
	}

	reviewerCalls := 0
	for _, opts := range *captured {
		if opts.Model != "reviewer" {
			continue
		}
		reviewerCalls++
		if strings.Contains(opts.SystemPrompt, "/skills/final-") {
			t.Fatalf("reviewer system prompt used a final-review family skill instead of an axis skill:\n%s", opts.SystemPrompt)
		}
		if !strings.Contains(opts.SystemPrompt, "/skills/review-implementation-") {
			t.Fatalf("reviewer system prompt missing implementation-review axis skill:\n%s", opts.SystemPrompt)
		}
	}
	if reviewerCalls != 6 {
		t.Fatalf("reviewer BuildSession calls = %d, want 6 (three axes, then QA, then two confirmations)", reviewerCalls)
	}

	events := readObserveEvents(t, observeDir, f.ID)
	validationStarted := filterEventsByType(events, "validation.started")
	if len(validationStarted) != 3 {
		t.Fatalf("validation.started count = %d, want 3", len(validationStarted))
	}
	for i, want := range []float64{3, 1, 2} {
		if validationStarted[i].Phase != "final_review" || validationStarted[i].Data["validator_count"] != want {
			t.Fatalf("validation.started[%d] = %+v, want final_review with validator_count=%v", i, validationStarted[i], want)
		}
	}
	validationCompleted := filterEventsByType(events, "validation.completed")
	if len(validationCompleted) != 3 {
		t.Fatalf("validation.completed count = %d, want 3", len(validationCompleted))
	}
	for i, want := range []string{"CHANGES_REQUESTED", "APPROVED", "APPROVED"} {
		if validationCompleted[i].Phase != "final_review" || validationCompleted[i].Status != want {
			t.Fatalf("validation.completed[%d] = %+v, want final_review %s", i, validationCompleted[i], want)
		}
	}
	started := validatorNamesByEvent(events, "validator.started")
	wantNames := []string{
		"Cleanliness", "Cleanliness",
		"Craft", "Craft",
		"QA", "QA",
	}
	if !slices.Equal(started, wantNames) {
		t.Fatalf("validator.started names = %v, want %v", started, wantNames)
	}
}

func TestRunFeatureFinalReviewLoop_QAAxisUsesLiveRunPosture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-qa-live-run", []string{"app"})
	f.Pipeline = feature.PipelineMedium
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review-axes.sh",
		testutil.JSONLInit+"\n"+
			writeFinalAxisFeedbackScript(artDir, false)+"\n"+
			testutil.JSONLSuccess+"\n")
	bs, captured := capturingBuildSessionByModel(map[string]string{"reviewer": reviewScript})

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	result, err := runFeatureFinalReviewLoop(OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		ReviewModel:    "reviewer",
		MaxIterations:  1,
		MaxConsecFails: 3,
		BuildSession:   bs,
		SkillsDir:      filepath.Join(env.stateDir, "skills"),
		GuidelinesDir:  filepath.Join(env.stateDir, "guidelines"),
		Observer:       observerFromModule(t, false, "", "test"),
	}, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed; LastError=%q", result.FinalStatus, result.LastError)
	}

	var liveRun, readOnly int
	var qaOpts BuildSessionOpts
	for _, opts := range *captured {
		if opts.Model != "reviewer" {
			continue
		}
		if permissionHandlerIncludesLiveRun(opts.PermHandler) {
			liveRun++
			qaOpts = opts
		}
		if permissionHandlerIncludesBoundedArtifacts(opts.PermHandler) {
			readOnly++
		}
	}
	if liveRun != 1 {
		t.Fatalf("live-run review BuildSession calls = %d, want exactly one QA axis; captured=%d", liveRun, len(*captured))
	}
	if readOnly != 2 {
		t.Fatalf("read-only review BuildSession calls = %d, want Craft and Cleanliness", readOnly)
	}
	if !strings.Contains(qaOpts.Prompt, "Axis under review: QA") {
		t.Fatalf("QA prompt missing axis label:\n%s", qaOpts.Prompt)
	}
	for _, want := range []string{
		filepath.Join(artDir, "iteration-01", "qa", "review-feedback.md"),
		filepath.Join(artDir, "iteration-01", "qa", "evidence"),
		filepath.Join(artDir, "iteration-01", "qa", "build-cache"),
		filepath.Join(artDir, "iteration-01", "qa", "tmp"),
	} {
		if !sliceContains(qaOpts.WritableRoots, want) {
			t.Fatalf("QA WritableRoots missing %q in %#v", want, qaOpts.WritableRoots)
		}
	}
	if receiptPath := filepath.Join(artDir, "iteration-01", "qa", PhaseCompleteFile); sliceContains(qaOpts.WritableRoots, receiptPath) {
		t.Fatalf("QA WritableRoots includes harness-owned receipt %q", receiptPath)
	}
	requirePermissionDecision(t, qaOpts.PermHandler, "Bash", `{"command":"npm install && npm run build > out.log"}`, "allow")
	requirePermissionDecision(t, qaOpts.PermHandler, "Write", `{"file_path":"`+filepath.Join(artDir, "iteration-01", "qa", "evidence", "home.png")+`"}`, "allow")
	requirePermissionDecision(t, qaOpts.PermHandler, "Write", `{"file_path":"`+filepath.Join(f.Repos[0].Path, "main.go")+`"}`, "deny")
}

func TestRunFeatureFinalReviewLoop_FrontendDesignAxisUsesLiveRunPosture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-design-live-run", []string{"app"})
	f.Pipeline = feature.PipelineLarge
	f.SetRoadmapPhaseFrontend(1, true)
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review-frontend-axes.sh",
		testutil.JSONLInit+"\n"+
			writeFinalAxisFeedbackScript(artDir, false)+"\n"+
			testutil.JSONLSuccess+"\n")
	bs, captured := capturingBuildSessionByModel(map[string]string{"reviewer": reviewScript})

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	result, err := runFeatureFinalReviewLoop(OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		ReviewModel:    "reviewer",
		MaxIterations:  1,
		MaxConsecFails: 3,
		BuildSession:   bs,
		SkillsDir:      filepath.Join(env.stateDir, "skills"),
		GuidelinesDir:  filepath.Join(env.stateDir, "guidelines"),
		Observer:       observerFromModule(t, false, "", "test"),
	}, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed; LastError=%q", result.FinalStatus, result.LastError)
	}

	var liveRun, readOnly int
	var designOpts BuildSessionOpts
	for _, opts := range *captured {
		if opts.Model != "reviewer" {
			continue
		}
		if permissionHandlerIncludesLiveRun(opts.PermHandler) {
			liveRun++
			if strings.Contains(opts.Prompt, "Axis under review: Design") {
				designOpts = opts
			}
		}
		if permissionHandlerIncludesBoundedArtifacts(opts.PermHandler) {
			readOnly++
		}
	}
	if liveRun != 2 {
		t.Fatalf("live-run review BuildSession calls = %d, want QA and Design axes; captured=%d", liveRun, len(*captured))
	}
	if readOnly != 2 {
		t.Fatalf("read-only review BuildSession calls = %d, want Craft and Cleanliness", readOnly)
	}
	if !strings.Contains(designOpts.Prompt, "Axis under review: Design") {
		t.Fatalf("Design prompt missing axis label:\n%s", designOpts.Prompt)
	}
	for _, want := range []string{
		filepath.Join(artDir, "iteration-01", "design", "review-feedback.md"),
		filepath.Join(artDir, "iteration-01", "design", "evidence"),
		filepath.Join(artDir, "iteration-01", "design", "build-cache"),
		filepath.Join(artDir, "iteration-01", "design", "tmp"),
	} {
		if !sliceContains(designOpts.WritableRoots, want) {
			t.Fatalf("Design WritableRoots missing %q in %#v", want, designOpts.WritableRoots)
		}
	}
	if sliceContains(designOpts.WritableRoots, filepath.Join(artDir, "iteration-01", "design", PhaseCompleteFile)) {
		t.Fatalf("Design WritableRoots grants model access to the harness receipt: %#v", designOpts.WritableRoots)
	}
	requirePermissionDecision(t, designOpts.PermHandler, "Bash", `{"command":"npm install && npm run build > out.log"}`, "allow")
	requirePermissionDecision(t, designOpts.PermHandler, "Write", `{"file_path":"`+filepath.Join(artDir, "iteration-01", "design", "evidence", "home.png")+`"}`, "allow")
	requirePermissionDecision(t, designOpts.PermHandler, "Write", `{"file_path":"`+filepath.Join(f.Repos[0].Path, "main.go")+`"}`, "deny")
}

// writeFinalAxisFeedbackScript writes feedback via tmp+mv: the parallel axis
// sessions all scan every axis dir, so a plain `cat > "$_fb"` can expose a
// partially written file to a concurrent writer or the harness's parser.
func writeFinalAxisFeedbackScript(artDir string, qaRequestsChangesFirstIteration bool) string {
	qaChanges := "0"
	if qaRequestsChangesFirstIteration {
		qaChanges = "1"
	}
	return fmt.Sprintf(`for _prompt in $(find "%s" -mindepth 3 -maxdepth 3 -name review-prompt.md -type f 2>/dev/null); do
  _dir=$(dirname "$_prompt")
  _axis=$(basename "$_dir")
  _iter=$(basename "$(dirname "$_dir")")
  _fb="$_dir/review-feedback.md"
  if [ -f "$_fb" ]; then continue; fi
  _verdict="APPROVED"
  _findings="- (none)"
  if [ "%s" = "1" ] && [ "$_iter" = "iteration-01" ] && [ "$_axis" = "qa" ]; then
    _verdict="CHANGES_REQUESTED"
    _findings="- **Critical**: feature fails launched smoke journey"
  fi
  _tmp="$_fb.tmp.$$"
  cat > "$_tmp" << REVIEWEOF
## Findings
$_findings

## Suggestions
- (none)

## Verdict
$_verdict
REVIEWEOF
  mv "$_tmp" "$_fb"
done`, artDir, qaChanges)
}

func removeFinalAxisFeedback(artDir string) string {
	return fmt.Sprintf(`for _prompt in $(find "%s" -mindepth 3 -maxdepth 3 -name review-prompt.md -type f 2>/dev/null); do
  _dir=$(dirname "$_prompt")
  rm -f "$_dir/review-feedback.md"
done`, artDir)
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
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-maxiter", []string{testRepoNameAPI, testRepoNameWeb})

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

	result, err := runFeatureFinalReviewLoop(cfg, sm)
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
	for _, name := range []string{testRepoNameAPI, testRepoNameWeb} {
		st := loaded.RepoStates[name]
		if st == nil || !st.Touched {
			t.Errorf("repo %s = %+v, want the failed stamp (Touched)", name, st)
		}
	}
}

// TestRunFeatureFinalReviewLoop_ConsecutiveFailuresSafetyRail covers the
// fix-agent safety rail: review keeps requesting changes and the fix agent
// fails without a valid root outcome. The consecutive-failure counter trips
// and every repo lands at "failed".
func TestRunFeatureFinalReviewLoop_ConsecutiveFailuresSafetyRail(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-safetyrail", []string{testRepoNameAPI, testRepoNameWeb})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: keep failing")+"\n"+
			testutil.JSONLSuccess+"\n")

	// Fix agent fails without emitting a root outcome.
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

	result, err := runFeatureFinalReviewLoop(cfg, sm)
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
	for _, name := range []string{testRepoNameAPI, testRepoNameWeb} {
		st := loaded.RepoStates[name]
		if st == nil || !st.Touched {
			t.Errorf("repo %s = %+v, want the failed stamp (Touched)", name, st)
		}
	}
}

func TestRunFeatureFinalReviewLoop_FixMissingRootOutcomeTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithFixScript(t, func(string) string { return "" }, testutil.JSONLResult(""))

	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "final_review_fixer @") ||
		!strings.Contains(result.LastError, "agentico-outcome") {
		t.Fatalf("LastError = %q, want final_review_fixer root-outcome violation", result.LastError)
	}
}

func TestRunFeatureFinalReviewLoop_ReviewerMissingRootOutcomeTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithReviewScriptAndResult(t, testutil.WriteReviewApproved, testutil.JSONLResult(""))

	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "implementation_review_") ||
		!strings.Contains(result.LastError, "agentico-outcome") {
		t.Fatalf("LastError = %q, want implementation review axis root-outcome violation", result.LastError)
	}
}

func TestRunFeatureFinalReviewLoop_ReviewerMissingFeedbackTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithReviewScript(t, func(artDir string) string {
		return removeFinalAxisFeedback(artDir)
	})

	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "implementation_review_") ||
		!strings.Contains(result.LastError, "review-feedback.md") {
		t.Fatalf("LastError = %q, want implementation review axis review-feedback violation", result.LastError)
	}
}

func TestRunFeatureFinalReviewLoop_ReviewerMalformedVerdictTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithReviewScript(t, func(artDir string) string {
		return testutil.WriteFinalReviewMalformedVerdictLatest(artDir)
	})

	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "implementation_review_") ||
		!strings.Contains(result.LastError, "LGTM") {
		t.Fatalf("LastError = %q, want malformed verdict violation", result.LastError)
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
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-resume", []string{testRepoNameAPI})

	artDir := frArtifactDir(env.stateDir, f)
	iter01 := filepath.Join(artDir, "iteration-01")
	if err := os.MkdirAll(iter01, 0o755); err != nil {
		t.Fatalf("mkdir iter01: %v", err)
	}
	am := newArtifactManager(artDir)
	if err := am.writeMeta(iter01, iterationMeta{
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
			testutil.JSONLAssistant(agentStatusApproved)+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
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

	result, err := runFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
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
			{Name: testRepoNameAPI, Path: filepath.Join(env.stateDir, testRepoNameAPI), BaseBranch: defaultTestBranch},
		},
		// All repos untouched — none staged for FR (the loop short-circuits
		// when TouchedRepos() is empty).
		RepoStates: map[string]*feature.RepoState{
			testRepoNameAPI: {},
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
	result, err := runFeatureFinalReviewLoop(cfg, nil)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Errorf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
}

// TestRunMultiRepoFinalReview_RunFinalReviewFnSeam asserts the
// OrchestratorConfig.RunFinalReviewFn test seam short-circuits the FR
// dispatch — production tests can replace the engine without launching real
// sessions. Mirrors how SetRunMultiRepoFinalReviewFn wires the orchestrator-
// level seam.
func TestRunMultiRepoFinalReview_RunFinalReviewFnSeam(t *testing.T) {
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-seam", []string{testRepoNameAPI, testRepoNameWeb})

	called := 0
	cfg := OrchestratorConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     env.stateDir,
		RunFinalReviewFn: func(_ OrchestratorConfig, _ ports.SessionManager) (*FeatureFinalReviewResult, error) {
			called++
			return &FeatureFinalReviewResult{FinalStatus: finalStatusReviewPassed, Iterations: 1, Repos: []string{testRepoNameAPI, testRepoNameWeb}}, nil
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
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-protocol-status", []string{testRepoNameAPI, testRepoNameWeb})

	cfg := OrchestratorConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     env.stateDir,
		RunFinalReviewFn: func(_ OrchestratorConfig, _ ports.SessionManager) (*FeatureFinalReviewResult, error) {
			return &FeatureFinalReviewResult{
				FinalStatus: BoundedHelperStatusProtocolViolation,
				LastError:   "protocol violation: final_review_fixer @ /tmp/iter: review-feedback.md is missing",
				Repos:       []string{testRepoNameAPI, testRepoNameWeb},
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
	for _, repo := range []string{testRepoNameAPI, testRepoNameWeb} {
		if result.RepoStatuses[repo] != BoundedHelperStatusProtocolViolation {
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
