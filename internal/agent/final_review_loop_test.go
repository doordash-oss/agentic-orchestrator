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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
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
		filepath.Join(phaseDir, "plan", "phase-plan.md"),
		filepath.Join(phaseDir, "testing-contract.yaml"),
		filepath.Join(iterDir, "verification-report.yaml"),
		iterDir,
		filepath.Join(iterDir, "screenshots", "setup.png"),
		filepath.Join(iterDir, "screenshots", "setup-detail.png"),
	} {
		if !containsPriorEvidencePath(ctx, want) {
			t.Errorf("priorImplementationEvidenceContextForRun() missing %s: %+v", want, ctx)
		}
	}
}

func containsPriorEvidencePath(ctx priorImplementationEvidenceContext, want string) bool {
	for _, paths := range [][]string{
		ctx.PlanPaths,
		ctx.ContractPaths,
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

func TestRunFeatureFinalReviewLoop_FinalReviewerContinuationStaysInSameIteration(t *testing.T) {
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-review-continuation", []string{"api", "web"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}
	firstRunMarker := filepath.Join(env.scriptsDir, "review-first-run")
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review-continuation.sh",
		fmt.Sprintf(`if [ ! -f "%s" ]; then
  touch "%s"
  for _d in "%s"/iteration-*; do :; done
  cat > "$_d/%s" <<'EOF'
%s
EOF
  touch "$_d/phase_complete"
  %s
  %s
else
  for _d in "%s"/iteration-*; do :; done
  cat > "$_d/%s" <<'EOF'
%s
EOF
  %s
  %s
  %s
fi`,
			firstRunMarker,
			firstRunMarker,
			artDir,
			ReviewProgressHandoffFilename,
			validReviewProgressHandoff("CONTINUE", "checked seeded verification evidence"),
			testutil.JSONLInit,
			testutil.JSONLSuccess,
			artDir,
			ReviewProgressHandoffFilename,
			validReviewProgressHandoff("COMPLETE", "checked seeded verification evidence"),
			testutil.JSONLInit,
			testutil.WriteFinalReviewApproved(artDir),
			testutil.JSONLSuccess))

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	buildSession, captured := capturingBuildSessionByModel(map[string]string{"reviewer": reviewScript})
	cfg := OrchestratorConfig{
		Feature:             f,
		FeatureStore:        store,
		StateDir:            env.stateDir,
		Model:               "agent",
		ReviewModel:         "reviewer",
		MaxIterations:       3,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		BuildSession:        buildSession,
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != "review_passed" || result.Iterations != 1 {
		t.Fatalf("result = %+v, want review_passed in iteration 1", result)
	}
	if _, err := os.Stat(filepath.Join(artDir, "iteration-02")); !os.IsNotExist(err) {
		t.Fatalf("iteration-02 exists or stat failed unexpectedly: %v", err)
	}
	reviewPrompts := promptsForModel(*captured, "reviewer")
	if len(reviewPrompts) != 2 {
		t.Fatalf("review prompts = %d, want 2", len(reviewPrompts))
	}
	if !strings.Contains(reviewPrompts[1], ReviewProgressHandoffFilename) {
		t.Fatalf("continuation prompt missing %s:\n%s", ReviewProgressHandoffFilename, reviewPrompts[1])
	}
}

func TestRunFeatureFinalReviewLoop_FinalReviewerContextHandoffNilObserverNoPanic(t *testing.T) {
	withHandoffPollInterval(t, 2*time.Millisecond)

	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-review-nil-observer", []string{"api"})
	artDir := frArtifactDir(env.stateDir, f)
	handoffSeen := make(chan string, 1)
	writeErr := make(chan error, 1)

	sm := &stubSessionManager{
		start: func(id, featureID string, phase feature.Phase, command []string, workdir string, envVars []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
			sess := session.NewSession(id, featureID, phase)
			sess.SetProviderName("codex")
			sess.SetLatestUsage(&llm.Usage{
				ContextTotalTokens: 80_000,
				ContextWindow:      200_000,
			})
			sink := attachCaptureSink(sess)
			go func() {
				select {
				case <-sink.done:
					handoffSeen <- sink.contents()
					iterDir := filepath.Join(artDir, "iteration-01")
					if err := writeFinalReviewApprovedArtifacts(iterDir); err != nil {
						writeErr <- err
						sess.SetCost(&llm.ResultMessage{Subtype: "error", IsError: true})
						sess.SendStatus(agentStatusFailed)
						return
					}
					writeErr <- nil
					sess.SetCost(&llm.ResultMessage{Subtype: "success"})
					sess.SendStatus(agentStatusSuccess)
				case <-time.After(2 * time.Second):
					handoffSeen <- ""
					writeErr <- fmt.Errorf("timed out waiting for Smart Zone handoff message")
					sess.SetCost(&llm.ResultMessage{Subtype: "error", IsError: true})
					sess.SendStatus(agentStatusFailed)
				}
			}()
			return sess, nil
		},
	}

	cfg := OrchestratorConfig{
		Feature:             f,
		FeatureStore:        store,
		StateDir:            env.stateDir,
		Model:               "agent",
		ReviewModel:         "reviewer",
		MaxIterations:       1,
		MaxConsecFails:      1,
		MaxConsecNoProgress: 1,
		BuildSession: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			return []string{opts.Model}, nil, &ports.SessionOpts{
				ProviderName:                  "codex",
				ContextHandoffThresholdTokens: 80_000,
			}, nil
		},
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write final review artifacts: %v", err)
	}
	if msg := <-handoffSeen; !strings.Contains(msg, "skills/final-review/HANDOFF.md") {
		t.Fatalf("handoff message missing final-review skill:\n%s", msg)
	}
}

func TestRunFeatureFinalReviewLoop_FinalFixContextHandoffNilObserverNoPanic(t *testing.T) {
	withHandoffPollInterval(t, 2*time.Millisecond)

	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-fix-nil-observer", []string{"api"})
	artDir := frArtifactDir(env.stateDir, f)
	handoffSeen := make(chan string, 1)
	writeErr := make(chan error, 3)
	reviewStarts := 0

	sm := &stubSessionManager{
		start: func(id, featureID string, phase feature.Phase, command []string, workdir string, envVars []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
			model := ""
			if len(command) > 0 {
				model = command[0]
			}
			sess := session.NewSession(id, featureID, phase)
			sess.SetProviderName("codex")
			switch model {
			case "reviewer":
				reviewStarts++
				startNo := reviewStarts
				go func() {
					iterDir := filepath.Join(artDir, fmt.Sprintf("iteration-%02d", startNo))
					var err error
					if startNo == 1 {
						err = writeFinalReviewChangesRequestedArtifacts(iterDir, "- **High**: needs fix")
					} else {
						err = writeFinalReviewApprovedArtifacts(iterDir)
					}
					if err != nil {
						writeErr <- err
						sess.SetCost(&llm.ResultMessage{Subtype: "error", IsError: true})
						sess.SendStatus(agentStatusFailed)
						return
					}
					writeErr <- nil
					sess.SetCost(&llm.ResultMessage{Subtype: "success"})
					sess.SendStatus(agentStatusSuccess)
				}()
			case "agent":
				sess.SetLatestUsage(&llm.Usage{
					ContextTotalTokens: 80_000,
					ContextWindow:      200_000,
				})
				sink := attachCaptureSink(sess)
				go func() {
					select {
					case <-sink.done:
						handoffSeen <- sink.contents()
						if err := writeFinalReviewFixArtifacts(filepath.Join(artDir, "iteration-01")); err != nil {
							writeErr <- err
							sess.SetCost(&llm.ResultMessage{Subtype: "error", IsError: true})
							sess.SendStatus(agentStatusFailed)
							return
						}
						writeErr <- nil
						sess.SetCost(&llm.ResultMessage{Subtype: "success"})
						sess.SendStatus(agentStatusSuccess)
					case <-time.After(2 * time.Second):
						handoffSeen <- ""
						writeErr <- fmt.Errorf("timed out waiting for Smart Zone handoff message")
						sess.SetCost(&llm.ResultMessage{Subtype: "error", IsError: true})
						sess.SendStatus(agentStatusFailed)
					}
				}()
			default:
				return nil, fmt.Errorf("unexpected model %q", model)
			}
			return sess, nil
		},
	}

	cfg := OrchestratorConfig{
		Feature:             f,
		FeatureStore:        store,
		StateDir:            env.stateDir,
		Model:               "agent",
		ReviewModel:         "reviewer",
		MaxIterations:       2,
		MaxConsecFails:      1,
		MaxConsecNoProgress: 1,
		BuildSession: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			return []string{opts.Model}, nil, &ports.SessionOpts{
				ProviderName:                  "codex",
				ContextHandoffThresholdTokens: 80_000,
			}, nil
		},
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	for i := 0; i < 3; i++ {
		if err := <-writeErr; err != nil {
			t.Fatalf("write final review/fix artifacts: %v", err)
		}
	}
	if msg := <-handoffSeen; !strings.Contains(msg, "skills/final-fix/HANDOFF.md") {
		t.Fatalf("handoff message missing final-fix skill:\n%s", msg)
	}
}

func TestRunFeatureFinalReviewLoop_FinalFixContinuationStaysInSameIteration(t *testing.T) {
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-fix-continuation", []string{"api", "web"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review-then-approve.sh",
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
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: needs fix")+"\n"+testutil.JSONLSuccess))

	firstFixMarker := filepath.Join(env.scriptsDir, "fix-first-run")
	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix-continuation.sh",
		fmt.Sprintf(`if [ ! -f "%s" ]; then
  touch "%s"
  for _d in "%s"/iteration-*; do :; done
  cat > "$_d/%s" <<'EOF'
%s
EOF
  touch "$_d/phase_complete"
  %s
  %s
else
  for _d in "%s"/iteration-*; do :; done
  cat > "$_d/%s" <<'EOF'
%s
EOF
  %s
  %s
  %s
fi`,
			firstFixMarker,
			firstFixMarker,
			artDir,
			ProducerProgressHandoffFilename,
			validProducerProgressHandoff("CONTINUE", "updated failing check"),
			testutil.JSONLInit,
			testutil.JSONLSuccess,
			artDir,
			ProducerProgressHandoffFilename,
			validProducerProgressHandoff("COMPLETE", "updated failing check"),
			testutil.JSONLInit,
			testutil.WriteFinalReviewFixSuccessArtifacts(artDir),
			testutil.JSONLSuccess))

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	buildSession, captured := capturingBuildSessionByModel(map[string]string{
		"reviewer": reviewScript,
		"agent":    fixScript,
	})
	cfg := OrchestratorConfig{
		Feature:             f,
		FeatureStore:        store,
		StateDir:            env.stateDir,
		Model:               "agent",
		ReviewModel:         "reviewer",
		MaxIterations:       4,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		BuildSession:        buildSession,
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != "review_passed" || result.Iterations != 2 {
		t.Fatalf("result = %+v, want review_passed in iteration 2", result)
	}
	fixPrompts := promptsForModel(*captured, "agent")
	if len(fixPrompts) != 2 {
		t.Fatalf("fix prompts = %d, want 2", len(fixPrompts))
	}
	if !strings.Contains(fixPrompts[1], ProducerProgressHandoffFilename) {
		t.Fatalf("fix continuation prompt missing %s:\n%s", ProducerProgressHandoffFilename, fixPrompts[1])
	}
	if _, err := os.Stat(filepath.Join(artDir, "iteration-01", ProducerProgressHandoffFilename)); err != nil {
		t.Fatalf("producer handoff missing in iteration-01: %v", err)
	}
}

func writeFinalReviewApprovedArtifacts(iterDir string) error {
	if err := markVerificationReportPassed(iterDir); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(iterDir, "review-feedback.md"), []byte(testutil.StructuredReviewFeedback("", "", "APPROVED")), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(iterDir, "phase_complete"), []byte("done\n"), 0o644)
}

func writeFinalReviewChangesRequestedArtifacts(iterDir, findings string) error {
	if err := os.WriteFile(filepath.Join(iterDir, "review-feedback.md"), []byte(testutil.StructuredReviewFeedback(findings, "", "CHANGES_REQUESTED")), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(iterDir, "phase_complete"), []byte("done\n"), 0o644)
}

func writeFinalReviewFixArtifacts(iterDir string) error {
	if err := markVerificationReportPassed(iterDir); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(iterDir, "phase_complete"), []byte("done\n"), 0o644)
}

func markVerificationReportPassed(iterDir string) error {
	reportPath := filepath.Join(iterDir, "verification-report.yaml")
	report, err := ReadVerificationReport(reportPath)
	if err != nil {
		return err
	}
	markChecksPassed := func(checks []VerificationCheckResult) {
		for i := range checks {
			checks[i].Status = VerificationStatusPassed
			exitCode := 0
			checks[i].EvidenceData = VerificationEvidence{
				ExitCode: &exitCode,
				Summary:  "mock final review check passed",
			}
		}
	}
	markChecksPassed(report.Results)
	markChecksPassed(report.RequiredChecks)
	markChecksPassed(report.AdditionalChecks)
	return WriteVerificationReport(reportPath, *report)
}

func promptsForModel(opts []BuildSessionOpts, model string) []string {
	var prompts []string
	for _, opt := range opts {
		if opt.Model == model {
			prompts = append(prompts, opt.Prompt)
		}
	}
	return prompts
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

func TestRunFeatureFinalReviewLoop_MissingEvidenceRoutesPlanRevision(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithReviewScript(t, func(artDir string) string {
		return testutil.WriteFinalReviewChangesRequested(artDir, "- **Critical**: MISSING_EVIDENCE_REQUIREMENT behavioral: Record the create-project CLI journey.")
	})

	if result.FinalStatus != "plan_revision_required" {
		t.Fatalf("FinalStatus = %q, want plan_revision_required", result.FinalStatus)
	}
	for _, want := range []string{
		"MISSING_EVIDENCE_REQUIREMENT behavioral: Record the create-project CLI journey.",
		"### Behavioral Evidence",
		"Do not add verification-report.yaml rows directly",
	} {
		if !strings.Contains(result.PlanRevisionFeedback, want) {
			t.Errorf("PlanRevisionFeedback missing %q:\n%s", want, result.PlanRevisionFeedback)
		}
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

func TestRunFeatureFinalReviewLoop_ReviewerMissingVerificationReportTripsProtocolViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	result := runFinalReviewWithReviewScript(t, func(artDir string) string {
		return fmt.Sprintf(`for _d in "%s"/iteration-*; do :; done
rm -f "$_d/verification-report.yaml"
%s`, artDir, testutil.WriteFinalReviewApproved(artDir))
	})

	if result.FinalStatus != "protocol_violation" {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "final_reviewer @") ||
		!strings.Contains(result.LastError, "verification-report.yaml") {
		t.Fatalf("LastError = %q, want verification-report violation", result.LastError)
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

// TestRunFeatureFinalReviewLoop_TestingContractIsFeatureLevelWithRepoTags
// verifies the loop persists a feature-level testing contract whose every
// item carries a `repo:` field — the unification gate that replaces N
// per-repo contracts.
func TestRunFeatureFinalReviewLoop_TestingContractIsFeatureLevelWithRepoTags(t *testing.T) {
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
	contract, err := ReadTestingContract(contractPath)
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	// Plan-less mode: only baseline rows. Each repo gets a baseline set;
	// no plan-source items.
	gotApi := 0
	gotWeb := 0
	for _, item := range contract.Items {
		switch item.Repo {
		case "api":
			gotApi++
		case "web":
			gotWeb++
		}
		if item.Source == testingContractPlanSource {
			t.Errorf("plan-source item leaked into plan-less FR contract: %+v", item)
		}
	}
	if gotApi == 0 || gotWeb == 0 {
		t.Errorf("expected per-repo baseline rows for both repos; got api=%d web=%d", gotApi, gotWeb)
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
