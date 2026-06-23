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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// These tests prove OpenCode participates in the two high-risk orchestration
// loops — the feature-level final-review loop and the PR review-comments repair
// loop — through the SAME contracts as Claude and Codex: review verdicts come
// from validated artifacts, fixes must create the required verification
// outputs, terminal failures stay terminal, and the model selection routes
// through the loop unchanged. They are fake-session / fake-dispatch based: the
// final-review tests drive a mock bash session (no `opencode` CLI, gated behind
// !testing.Short like their Claude/Codex siblings), and the review-comments
// tests drive the loop's RunImplementFn seam directly. None require GitHub,
// OpenCode credentials, or network access.

const (
	openCodeReviewerModel = "opencode:anthropic/claude-sonnet-4-5"
	openCodeFixerModel    = "opencode:anthropic/claude-haiku-4-5"
)

// assertAllOpenCode fails unless every captured BuildSessionOpts carried an
// explicit "opencode:" model selection — i.e. the loop routed OpenCode through
// the session builder for every role it dispatched.
func assertAllOpenCode(t *testing.T, captured []BuildSessionOpts) {
	t.Helper()
	if len(captured) == 0 {
		t.Fatal("no BuildSession calls captured; loop never dispatched a session")
	}
	for i, opts := range captured {
		if !strings.HasPrefix(opts.Model, "opencode:") {
			t.Fatalf("captured[%d].Model = %q, want an opencode: selection", i, opts.Model)
		}
	}
}

// TestRunFeatureFinalReviewLoop_OpenCodeApprovesOnlyWithValidArtifacts proves an
// OpenCode-backed final-review session approves a feature only when the
// final-review artifacts and marker validate: the reviewer writes a valid
// APPROVED feedback + verification report, and every staged repo transitions
// atomically to review_passed. The captured models confirm the OpenCode
// selection routed through the normal session builder.
func TestRunFeatureFinalReviewLoop_OpenCodeApprovesOnlyWithValidArtifacts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-opencode-approve", []string{"api", "web"})

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

	bs, captured := capturingBuildSessionByModel(map[string]string{openCodeReviewerModel: approveScript})
	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          openCodeFixerModel,
		ReviewModel:    openCodeReviewerModel,
		MaxIterations:  3,
		MaxConsecFails: 3,
		BuildSession:   bs,
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", result.Iterations)
	}
	assertAllOpenCode(t, *captured)

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"api", "web"} {
		if st := loaded.RepoStates[name]; st == nil || !st.Touched {
			t.Errorf("repo %s = %+v, want review_passed", name, st)
		}
	}
}

// TestRunFeatureFinalReviewLoop_OpenCodeFixMustCreateVerificationReport proves an
// OpenCode-backed fix session that runs after CHANGES_REQUESTED must satisfy the
// same verification-report contract as other providers: a fix that touches
// phase_complete but omits verification-report.yaml trips a protocol violation
// naming the fixer role and the missing artifact, and the feature does not
// advance. Distinct reviewer/fixer OpenCode selections confirm both roles route
// through the normal session builder.
func TestRunFeatureFinalReviewLoop_OpenCodeFixMustCreateVerificationReport(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _ := newFRTestFeature(t, env.stateDir, "fr-opencode-fix", []string{"api", "web"})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	// Reviewer always requests changes so the fixer runs in the same iteration.
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: keep requesting changes")+"\n"+
			testutil.JSONLSuccess+"\n")

	// Fixer writes phase_complete but deletes the verification report.
	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			`for _d in "`+artDir+`"/iteration-*; do :; done
rm -f "$_d/verification-report.yaml"
touch "$_d/phase_complete"`+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	bs, captured := capturingBuildSessionByModel(map[string]string{
		openCodeReviewerModel: reviewScript,
		openCodeFixerModel:    fixScript,
	})
	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          openCodeFixerModel,
		ReviewModel:    openCodeReviewerModel,
		MaxIterations:  10,
		MaxConsecFails: 2,
		BuildSession:   bs,
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != "protocol_violation" {
		t.Fatalf("FinalStatus = %q, want protocol_violation", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "final_review_fixer @") ||
		!strings.Contains(result.LastError, "verification-report.yaml") {
		t.Fatalf("LastError = %q, want final_review_fixer verification-report violation", result.LastError)
	}
	assertAllOpenCode(t, *captured)
}

// TestRunReviewCommentsLoop_OpenCodeModelsRouteThroughLoop proves the
// review-comments repair loop forwards the OpenCode implementation and review
// model selections into the inner implementation loop unchanged, and stamps the
// staged repos on a passing review. The captured ImplementConfig is the routing
// boundary: cfg.Model/cfg.ReviewModel must reach it verbatim.
func TestRunReviewCommentsLoop_OpenCodeModelsRouteThroughLoop(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-opencode-success", []string{"api", "web"})

	runImpl, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1})
	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 101, 102),
			makeReviewCommentTarget("web", "https://github.com/example/web/pull/1", 201),
		},
		Model:          openCodeFixerModel,
		ReviewModel:    openCodeReviewerModel,
		MaxIterations:  3,
		RunImplementFn: runImpl,
	}

	result, err := RunReviewCommentsLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Errorf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if len(*captured) == 0 {
		t.Fatal("inner implementation loop was never dispatched")
	}
	implCfg := (*captured)[0]
	if implCfg.Model != openCodeFixerModel {
		t.Errorf("ImplementConfig.Model = %q, want %q", implCfg.Model, openCodeFixerModel)
	}
	if implCfg.ReviewModel != openCodeReviewerModel {
		t.Errorf("ImplementConfig.ReviewModel = %q, want %q", implCfg.ReviewModel, openCodeReviewerModel)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"api", "web"} {
		if st := loaded.RepoStates[name]; st == nil || !st.Touched {
			t.Errorf("repo %s = %+v, want awaiting_final_review", name, st)
		}
	}
	if loaded.ActiveCycle != nil {
		t.Errorf("ActiveCycle = %+v, want nil after success", loaded.ActiveCycle)
	}
}

// TestRunReviewCommentsLoop_OpenCodeNeedUserInputSurfacesGate proves the
// review-comments loop preserves need-user-input pause semantics for an
// OpenCode-backed cycle: an ambiguous decision pauses the cycle (gate persisted,
// staged repos not failed, ActiveCycle kept running for resume) rather than
// advancing the feature.
func TestRunReviewCommentsLoop_OpenCodeNeedUserInputSurfacesGate(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-opencode-nui", []string{"api", "web"})
	gatePath := filepath.Join(stateDir, "review-comments-1", "iteration-01", "need-user-input.yaml")

	runImpl, captured := capturingRunImplementFn(&LoopResult{
		FinalStatus:       "need_user_input",
		Iterations:        1,
		LastError:         "Reviewer request conflicts with product decision.",
		NeedUserInputPath: gatePath,
	})
	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 1),
			makeReviewCommentTarget("web", "https://github.com/example/web/pull/1", 2),
		},
		Model:          openCodeFixerModel,
		ReviewModel:    openCodeReviewerModel,
		MaxIterations:  3,
		RunImplementFn: runImpl,
	}

	result, err := RunReviewCommentsLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if result.FinalStatus != "need_user_input" {
		t.Fatalf("FinalStatus = %q, want need_user_input", result.FinalStatus)
	}
	if result.NeedUserInputPath != gatePath {
		t.Errorf("NeedUserInputPath = %q, want %q", result.NeedUserInputPath, gatePath)
	}
	if implCfg := (*captured)[0]; implCfg.ReviewModel != openCodeReviewerModel {
		t.Errorf("ImplementConfig.ReviewModel = %q, want %q", implCfg.ReviewModel, openCodeReviewerModel)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.PendingNeedUserInputPath != gatePath {
		t.Errorf("PendingNeedUserInputPath = %q, want %q", loaded.PendingNeedUserInputPath, gatePath)
	}
	for _, name := range []string{"api", "web"} {
		if st := loaded.RepoStates[name]; st == nil || st.LastError != "" {
			t.Errorf("repo %s = %+v, want prior state without failure", name, st)
		}
	}
	if loaded.ActiveCycle == nil || loaded.ActiveCycle.Status != feature.RepoCycleRunning {
		t.Fatalf("ActiveCycle = %+v, want preserved at Status=running for gate resume", loaded.ActiveCycle)
	}
}

// TestRunReviewCommentsLoop_OpenCodeDispatchErrorStaysTerminal proves a terminal
// dispatch failure in an OpenCode-backed review-comments cycle remains terminal:
// the loop surfaces the error, reports FinalStatus=failed, and stamps the staged
// repo failed — provider success text can never launder a terminal failure into
// a pass.
func TestRunReviewCommentsLoop_OpenCodeDispatchErrorStaysTerminal(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-opencode-dispatch", []string{"api"})

	dispatchErr := errors.New("session manager: ports.ErrSessionShuttingDown")
	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 7),
		},
		Model:          openCodeFixerModel,
		ReviewModel:    openCodeReviewerModel,
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(nil, dispatchErr),
	}

	result, err := RunReviewCommentsLoop(cfg, nil)
	if err == nil || !errors.Is(err, dispatchErr) {
		t.Fatalf("err = %v, want errors.Is %v", err, dispatchErr)
	}
	if result == nil || result.FinalStatus != "failed" {
		t.Fatalf("result = %+v, want FinalStatus failed", result)
	}

	loaded, _ := store.Load(f.ID)
	if st := loaded.RepoStates["api"]; st == nil || st.LastError == "" {
		t.Errorf("api = %+v, want failed", st)
	}
}
