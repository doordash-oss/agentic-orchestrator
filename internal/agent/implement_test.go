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
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"gopkg.in/yaml.v3"
)

func TestBuildImplementPrompt(t *testing.T) {
	prompt := BuildImplementPrompt(
		"/tmp/plans/plan.md",
		"Relevant tests pass",
		"/tmp/progress.md",
		"/tmp/verification-report.yaml",
		"/tmp/testing-contract.yaml",
		"Fix the auth bug",
		"Use JWT",
		"",
		"",
		"",
		[]RequiredVerificationItem{
			{Name: "Unit tests pass", Requirement: "go test ./..."},
		},
		3,
	)

	checks := []string{
		"/tmp/plans/plan.md",
		"Relevant tests pass",
		"Fix the auth bug",
		"Use JWT",
	}

	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("expected prompt to contain %q", c)
		}
	}
	for _, c := range []string{
		"/tmp/progress.md",
		"/tmp/verification-report.yaml",
		"/tmp/testing-contract.yaml",
		"/tmp/need-user-input.yaml",
		"## Handoff Contract",
		"# Useful Resources",
		"phase_complete",
		"Unit tests pass",
	} {
		if strings.Contains(prompt, c) {
			t.Errorf("prompt should not contain role-internal artifact/resource detail %q", c)
		}
	}
}

func TestBuildImplementPromptMinimal(t *testing.T) {
	prompt := BuildImplementPrompt("/tmp/plan.md", "criteria", "", "", "", "", "", "", "", "", nil, 1)
	if !strings.Contains(prompt, "/tmp/plan.md") {
		t.Error("expected plan path in minimal prompt")
	}
	if !strings.Contains(prompt, "criteria") {
		t.Error("expected exit criteria in minimal prompt")
	}
	if strings.Contains(prompt, "## Handoff Contract") {
		t.Error("Handoff Contract section should be owned by the implement skill and system prompt")
	}
}

func TestBuildImplementPrompt_TestingContractPath(t *testing.T) {
	prompt := BuildImplementPrompt(
		"/tmp/plan.md", "criteria", "/tmp/progress.md",
		"/tmp/verification-report.yaml", "/tmp/state/feat/runs/run-001/phase-02/testing-contract.yaml",
		"", "", "", "", "", nil, 1)
	if !strings.Contains(prompt, "/tmp/state/feat/runs/run-001/phase-02/testing-contract.yaml") {
		return
	}
	t.Error("testing contract path should not be inlined into the lean implement user prompt")
}

func TestImplementationPromptsIgnoreLegacyTagsButKeepVisualReferences(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Working...")+"\n"+
			testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	planPath := filepath.Join(artifactDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\nImplement something"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	f := &feature.Feature{
		ID:            "test-feat-001",
		Name:          "Test Feature",
		Slug:          "test-feature",
		Description:   "Legacy tagged feature",
		Images:        []string{"/tmp/mockup.png"},
		Tags:          []string{"frontend"},
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: workDir},
		},
	}

	store := feature.NewStore(filepath.Join(tmpDir, "state"))
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	buildSession, captured := capturingBuildSession(agentScript, reviewScript)
	sm := session.NewManager(make(chan interface{}, 100))
	defer sm.Shutdown()

	result, err := RunImplementationLoop(ImplementConfig{
		Feature:                    f,
		FeatureStore:               store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		MaxIterations:              1,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               "Relevant tests pass",
		Model:                      "implementer",
		ReviewModel:                "reviewer",
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
	}, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("RunImplementationLoop() FinalStatus = %s, want review_passed", result.FinalStatus)
	}
	if len(*captured) < 2 {
		t.Fatalf("captured %d BuildSession calls, want implement and review", len(*captured))
	}

	t.Logf("legacy tagged implement prompt:\n%s", (*captured)[0].Prompt)
	t.Logf("legacy tagged review prompt:\n%s", (*captured)[1].Prompt)

	for _, got := range []struct {
		name   string
		prompt string
	}{
		{name: "implement", prompt: (*captured)[0].Prompt},
		{name: "review", prompt: (*captured)[1].Prompt},
	} {
		if !strings.Contains(got.prompt, "## Visual References") {
			t.Errorf("%s prompt missing visual references:\n%s", got.name, got.prompt)
		}
		if !strings.Contains(got.prompt, "/tmp/mockup.png") {
			t.Errorf("%s prompt missing attached image path:\n%s", got.name, got.prompt)
		}
		if strings.Contains(got.prompt, "## Visual Evidence") {
			t.Errorf("%s prompt unexpectedly contains visual evidence guidance:\n%s", got.name, got.prompt)
		}
		if strings.Contains(got.prompt, "## Behavioral Evidence") {
			t.Errorf("%s prompt unexpectedly contains behavioral evidence guidance:\n%s", got.name, got.prompt)
		}
		requiredSkillsHeading := "Required Skills" + " For This Feature"
		if strings.Contains(got.prompt, requiredSkillsHeading) {
			t.Errorf("%s prompt unexpectedly contains required skills guidance:\n%s", got.name, got.prompt)
		}
	}
}

func TestLoopResultTypes(t *testing.T) {
	result := &LoopResult{
		FinalStatus: "review_passed",
		Iterations:  5,
	}
	if result.FinalStatus != "review_passed" {
		t.Errorf("expected review_passed, got %s", result.FinalStatus)
	}
	if result.Iterations != 5 {
		t.Errorf("expected 5 iterations, got %d", result.Iterations)
	}

	safetyResult := &LoopResult{
		FinalStatus: "safety_rail",
		Iterations:  3,
		LastError:   "no progress for 3 consecutive iterations",
	}
	if safetyResult.LastError == "" {
		t.Error("expected non-empty error for safety rail")
	}
}

func TestImplementConfigFields(t *testing.T) {
	cfg := ImplementConfig{
		MaxIterations:       10,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "tests pass",
		Model:               "sonnet",
		ReviewModel:         "codex",
		BuildSession:        mockBuildSession("", ""),
	}
	if cfg.MaxIterations != 10 {
		t.Errorf("expected 10, got %d", cfg.MaxIterations)
	}
	if cfg.ReviewModel != "codex" {
		t.Errorf("expected codex, got %s", cfg.ReviewModel)
	}
}

func TestImplementConfigSkipFieldZeroValue(t *testing.T) {
	cfg := ImplementConfig{}
	if cfg.SkipIterationReview {
		t.Error("expected SkipIterationReview zero value to be false")
	}
}

func TestBuildHelpAnswers(t *testing.T) {
	tests := []struct {
		name  string
		queue []feature.HelpRequest
		want  string
	}{
		{
			name:  "empty queue",
			queue: nil,
			want:  "",
		},
		{
			name: "pending only",
			queue: []feature.HelpRequest{
				{Question: "What auth?", Pending: true},
			},
			want: "",
		},
		{
			name: "one answered",
			queue: []feature.HelpRequest{
				{Question: "What auth?", Answer: "Use JWT", Pending: false},
			},
			want: "Q: What auth?\nA: Use JWT",
		},
		{
			name: "mixed pending and answered",
			queue: []feature.HelpRequest{
				{Question: "What auth?", Answer: "Use JWT", Pending: false},
				{Question: "Which DB?", Pending: true},
				{Question: "Port?", Answer: "8080", Pending: false},
			},
			want: "Q: What auth?\nA: Use JWT\n\nQ: Port?\nA: 8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildHelpAnswers(tt.queue)
			if got != tt.want {
				t.Errorf("buildHelpAnswers() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWaitForStatus(t *testing.T) {
	t.Run("done_before_status_returns_FAILED", func(t *testing.T) {
		sess := session.NewSession("test", "feat", feature.PhaseImplement)
		sess.CloseDone()

		got := waitForStatus(sess, nil, "")
		if got != "FAILED" {
			t.Errorf("waitForStatus() = %q, want FAILED", got)
		}
	})

	t.Run("done_with_pending_SUCCESS", func(t *testing.T) {
		sess := session.NewSession("test", "feat", feature.PhaseImplement)
		sess.SendStatus("SUCCESS")
		sess.CloseDone()

		got := waitForStatus(sess, nil, "")
		if got != "SUCCESS" {
			t.Errorf("waitForStatus() = %q, want SUCCESS", got)
		}
	})

	t.Run("done_with_pending_SUCCESS_ready_false_returns_MISSING_PHASE_COMPLETE", func(t *testing.T) {
		for i := 0; i < 200; i++ {
			sess := session.NewSession("test", "feat", feature.PhaseImplement)
			sess.SetCost(newEndedAfterTextResult())
			sess.SendStatus("SUCCESS")
			sess.CloseDone()

			got := waitForStatus(sess, nil, "", func() bool { return false })
			if got != agentStatusMissingMarker {
				t.Fatalf("waitForStatus() iteration %d = %q, want %q", i, got, agentStatusMissingMarker)
			}
		}
	})

	t.Run("done_with_result_but_no_status_ready_false_returns_MISSING_PHASE_COMPLETE", func(t *testing.T) {
		sess := session.NewSession("test", "feat", feature.PhaseImplement)
		sess.SetCost(newEndedAfterTextResult())
		sess.CloseDone()

		got := waitForStatus(sess, nil, "", func() bool { return false })
		if got != agentStatusMissingMarker {
			t.Errorf("waitForStatus() = %q, want %q", got, agentStatusMissingMarker)
		}
	})

	t.Run("done_with_pending_truncated_SUCCESS_auto_resumes", func(t *testing.T) {
		sess := newDoneFirstStatusSession()
		sess.result = newTruncatedResult()
		sess.statusCh <- agentStatusSuccess
		close(sess.done)

		var ready atomic.Bool
		done := make(chan string, 1)
		go func() {
			done <- waitForStatus(sess, nil, "", func() bool { return ready.Load() })
		}()

		select {
		case got := <-sess.userMessages:
			if !strings.Contains(got, "Continue where you left off") {
				t.Fatalf("SendUserMessage() = %q, want auto-resume message", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for auto-resume message")
		}

		select {
		case got := <-done:
			t.Fatalf("waitForStatus() returned %q; want it to keep waiting after auto-resume", got)
		default:
		}

		ready.Store(true)
		sess.statusCh <- agentStatusSuccess

		select {
		case got := <-done:
			if got != agentStatusSuccess {
				t.Fatalf("waitForStatus() = %q, want %q", got, agentStatusSuccess)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for final SUCCESS")
		}
	})

	t.Run("done_with_pending_API_ERROR_returns_FAILED", func(t *testing.T) {
		sess := session.NewSession("test", "feat", feature.PhaseImplement)
		sess.SendStatus("API_ERROR")
		sess.CloseDone()

		got := waitForStatus(sess, nil, "")
		if got != "FAILED" {
			t.Errorf("waitForStatus() = %q, want FAILED", got)
		}
	})

	t.Run("status_SUCCESS_before_done", func(t *testing.T) {
		sess := session.NewSession("test", "feat", feature.PhaseImplement)
		go func() {
			sess.SendStatus("SUCCESS")
		}()

		got := waitForStatus(sess, nil, "")
		if got != "SUCCESS" {
			t.Errorf("waitForStatus() = %q, want SUCCESS", got)
		}
	})

	t.Run("API_ERROR_then_SUCCESS", func(t *testing.T) {
		sess := session.NewSession("test", "feat", feature.PhaseImplement)
		go func() {
			sess.SendStatus("API_ERROR")
			sess.SendStatus("SUCCESS")
		}()

		got := waitForStatus(sess, nil, "")
		if got != "SUCCESS" {
			t.Errorf("waitForStatus() = %q, want SUCCESS", got)
		}
	})

	t.Run("API_ERROR_then_done_returns_FAILED", func(t *testing.T) {
		sess := session.NewSession("test", "feat", feature.PhaseImplement)
		go func() {
			sess.SendStatus("API_ERROR")
			sess.CloseDone()
		}()

		got := waitForStatus(sess, nil, "")
		if got != "FAILED" {
			t.Errorf("waitForStatus() = %q, want FAILED", got)
		}
	})
}

// TestBuildPriorUserInputAnswers covers the artifact-scanning helper that
// renders resolved gate answers into a single prompt-ready block. The
// helper must include answered iterations (every question carries a
// non-empty answer) and skip unresolved gates (any answer left blank) so
// the resumed prompt only surfaces decisions the user has actually made.
func TestBuildPriorUserInputAnswers(t *testing.T) {
	dir := t.TempDir()

	resolved := NeedUserInputRecord{
		Summary:   "Need a decision on auth direction.",
		Iteration: 2,
		Questions: []NeedUserInputQuestion{
			{Index: 1, Prompt: "Legacy auth path or new auth service?", Answer: "Use the new auth service."},
			{Index: 2, Prompt: "May we skip session migration?", Answer: "Yes, skip it in this phase."},
		},
	}
	if err := WriteNeedUserInputRecord(filepath.Join(dir, "iteration-02", "need-user-input.yaml"), resolved); err != nil {
		t.Fatalf("write resolved gate: %v", err)
	}

	unresolved := NeedUserInputRecord{
		Summary:   "Still waiting on a later answer.",
		Iteration: 3,
		Questions: []NeedUserInputQuestion{
			{Index: 1, Prompt: "Which rollout flag?", Answer: ""},
		},
	}
	if err := WriteNeedUserInputRecord(filepath.Join(dir, "iteration-03", "need-user-input.yaml"), unresolved); err != nil {
		t.Fatalf("write unresolved gate: %v", err)
	}

	got := buildPriorUserInputAnswers(dir)
	if !strings.Contains(got, "Use the new auth service.") {
		t.Fatalf("resolved answer missing from %q", got)
	}
	if !strings.Contains(got, "Yes, skip it in this phase.") {
		t.Fatalf("second resolved answer missing from %q", got)
	}
	if !strings.Contains(got, "### Iteration 2") {
		t.Fatalf("iteration label missing from %q", got)
	}
	if strings.Contains(got, "Still waiting on a later answer.") {
		t.Fatalf("unresolved gate should not be included in %q", got)
	}
	if strings.Contains(got, "Which rollout flag?") {
		t.Fatalf("unresolved prompt should not be included in %q", got)
	}
}

// TestBuildPriorUserInputAnswers_NoArtifacts confirms the helper returns
// the empty string when no iteration directories exist (or none carry a
// resolved gate). The empty string flips the prompt template's
// PriorUserInputAnswers section off.
func TestBuildPriorUserInputAnswers_NoArtifacts(t *testing.T) {
	dir := t.TempDir()
	if got := buildPriorUserInputAnswers(dir); got != "" {
		t.Fatalf("buildPriorUserInputAnswers(empty) = %q, want \"\"", got)
	}
}

// TestBuildImplementPromptWithPriorNeedUserInputAnswers proves the
// resumed implement prompt carries a dedicated `Resolved NEED_USER_INPUT`
// block beside (and separate from) the live `NEED_HELP` answers section.
// The two histories serve different recovery paths and must not collapse.
func TestBuildImplementPromptWithPriorNeedUserInputAnswers(t *testing.T) {
	prompt := BuildImplementPrompt(
		"/tmp/plan.md",
		"criteria",
		"/tmp/progress.md",
		"/tmp/verification-report.yaml",
		"",
		"",
		"Q: runtime question?\nA: runtime answer.",
		"### Iteration 2\nSummary: Need a decision on auth direction.\nQ1: Legacy auth path or new auth service?\nA1: Use the new auth service.",
		"",
		"",
		nil,
		2,
	)

	if !strings.Contains(prompt, "Resolved NEED_USER_INPUT from previous iterations") {
		t.Fatal("expected prior need-user-input section in prompt")
	}
	if !strings.Contains(prompt, "Use the new auth service.") {
		t.Fatal("expected prior gate answer body in prompt")
	}
	if !strings.Contains(prompt, "Answers to NEED_HELP questions") {
		t.Fatal("expected NEED_HELP section to remain separate")
	}
}

func TestBuildImplementPromptWithHelpAnswers(t *testing.T) {
	prompt := BuildImplementPrompt(
		"/tmp/plan.md",
		"criteria",
		"/tmp/progress.md",
		"/tmp/verification-report.yaml",
		"",
		"",
		"Q: What auth?\nA: Use JWT",
		"",
		"",
		"",
		nil,
		1,
	)
	if !strings.Contains(prompt, "Q: What auth?") {
		t.Error("expected help question in prompt")
	}
	if !strings.Contains(prompt, "A: Use JWT") {
		t.Error("expected help answer in prompt")
	}
	if !strings.Contains(prompt, "Answers to NEED_HELP questions") {
		t.Error("expected help section header in prompt")
	}
}

// writeMockCodexBinaryForImpl creates a mock "codex" shell script that
// writes phase_complete to the iteration-01 subdirectory of the artifact dir.
func writeMockCodexBinaryForImpl(t *testing.T, binDir string) string {
	t.Helper()
	mockCodex := filepath.Join(binDir, "codex")
	script := `#!/bin/bash
# Mock codex binary for implementation routing tests.

# 1. Read initialize request
read -r line

# 2. Send initialize response
echo '{"jsonrpc":"2.0","id":1,"result":{"userAgent":"codex-mock/test","codexHome":"/tmp/codex-home","platformFamily":"darwin","platformOs":"macos"}}'

# 3. Read initialized notification
read -r line

# 4. Read thread/start request
read -r line

# 5. Send thread/start response
echo '{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"thread-test-001"}}}'

# 6. Read turn/start request
read -r line

# 7. Send turn/start response
echo '{"jsonrpc":"2.0","id":3,"result":{"turn":{"id":"turn-test-001","status":"in_progress"}}}'

# 8. Write phase_complete signal to the iteration directory
if [ -n "$MOCK_PHASE_COMPLETE_DIR" ]; then
  touch "$MOCK_PHASE_COMPLETE_DIR/iteration-01/phase_complete"
fi

# 9. Send turn/completed
echo '{"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":"turn-test-001","status":"completed"}}}'
`
	if err := os.WriteFile(mockCodex, []byte(script), 0o755); err != nil {
		t.Fatalf("writing mock codex binary: %v", err)
	}
	return mockCodex
}

// TestImplementLoopCodexRouting verifies that the implementation loop routes
// to the correct provider based on cfg.Model:
//   - "gpt-5.4" → codex app-server path (InteractiveCommandBuilder NOT called)
//   - "opus"    → claude interactive path (InteractiveCommandBuilder IS called)
func TestImplementLoopCodexRouting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tests := []struct {
		name      string
		model     string
		wantCodex bool // true → codex path, false → claude path
	}{
		{
			name:      "gpt-5.4 routes to codex app-server",
			model:     "gpt-5.4",
			wantCodex: true,
		},
		{
			name:      "opus routes to claude interactive",
			model:     "opus",
			wantCodex: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			workDir := filepath.Join(tmpDir, "work")
			artifactDir := filepath.Join(tmpDir, "artifacts")
			stateDir := filepath.Join(tmpDir, "state", "test-feat-001")
			scriptsDir := filepath.Join(tmpDir, "scripts")
			mockBinDir := filepath.Join(tmpDir, "mock-bin")
			for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir, mockBinDir} {
				os.MkdirAll(d, 0o755)
			}

			// Agent script: writes the implement-iteration handoff artifacts
			// (verification-report.yaml + progress.md + phase_complete) and
			// emits stream-json success.
			agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
				testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Working...")+"\n"+
					testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

			// Review script: approves immediately
			reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
				testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

			// For the codex case, put a mock codex binary on PATH
			if tt.wantCodex {
				writeMockCodexBinaryForImpl(t, mockBinDir)
				t.Setenv("PATH", mockBinDir+":"+os.Getenv("PATH"))
				t.Setenv("MOCK_PHASE_COMPLETE_DIR", artifactDir)
			}

			eventCh := make(chan interface{}, 100)
			sm := session.NewManager(eventCh)
			defer sm.Shutdown()

			planPath := filepath.Join(artifactDir, "plan.md")
			_ = os.WriteFile(planPath, []byte("# Plan\nImplement something"), 0o644)

			f := &feature.Feature{
				ID:            "test-feat-001",
				Name:          "Test Feature",
				Slug:          "test-feature",
				Description:   "Codex routing test",
				Status:        feature.StatusImplementing,
				CurrentPhase:  feature.PhaseImplement,
				ActiveRun:     1,
				RunCount:      1,
				SchemaVersion: feature.SchemaVersionCurrent,
				Repos: []feature.FeatureRepo{
					{Name: "test-repo", Path: workDir},
				},
			}

			store := feature.NewStore(filepath.Join(tmpDir, "state"))
			_ = store.Save(f)

			// Use capturingBuildSession to verify which model is routed.
			buildSession, captured := capturingBuildSession(agentScript, reviewScript)

			cfg := ImplementConfig{
				Feature:                    f,
				FeatureStore:               store,
				WorkDir:                    workDir,
				PlanPath:                   planPath,
				MaxIterations:              1,
				MaxConsecFails:             3,
				MaxConsecNoProgress:        3,
				ExitCriteria:               "Relevant tests pass",
				Model:                      tt.model,
				ReviewModel:                "reviewer",
				ArtifactDir:                artifactDir,
				StateDir:                   stateDir,
				DangerouslySkipPermissions: true,
				BuildSession:               buildSession,
			}

			result, err := RunImplementationLoop(cfg, sm)

			if tt.wantCodex {
				if err != nil {
					t.Fatalf("unexpected error for codex model: %v", err)
				}
				// Verify BuildSession was called with the codex model
				if len(*captured) == 0 {
					t.Fatal("expected at least 1 BuildSession call")
				}
				if (*captured)[0].Model != tt.model {
					t.Errorf("expected BuildSession model=%q, got %q", tt.model, (*captured)[0].Model)
				}
				_ = result
			} else {
				if err != nil {
					t.Fatalf("unexpected error for claude model: %v", err)
				}
				// Verify BuildSession was called with the claude model
				if len(*captured) == 0 {
					t.Fatal("expected at least 1 BuildSession call")
				}
				if (*captured)[0].Model != tt.model {
					t.Errorf("expected BuildSession model=%q, got %q", tt.model, (*captured)[0].Model)
				}
				if result.FinalStatus != "review_passed" {
					t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
				}
			}
		})
	}
}

// TestImplementLoopCodexRoutingUnit verifies provider-aware routing in
// RunImplementationLoop WITHOUT spawning real processes. SessionStartFunc
// captures the command and opts, then returns ErrShuttingDown to exit the
// loop cleanly. This runs under -short.
func TestImplementLoopCodexRoutingUnit(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		dsp       bool
		wantCodex bool
	}{
		{
			name:      "gpt-5.4 routes to codex app-server",
			model:     "gpt-5.4",
			dsp:       true,
			wantCodex: true,
		},
		{
			name:      "opus routes to claude interactive",
			model:     "opus",
			dsp:       false,
			wantCodex: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			workDir := filepath.Join(tmpDir, "work")
			artifactDir := filepath.Join(tmpDir, "artifacts")
			stateDir := filepath.Join(tmpDir, "state", "test-impl-001")
			for _, d := range []string{workDir, artifactDir, stateDir} {
				os.MkdirAll(d, 0o755)
			}

			planPath := filepath.Join(artifactDir, "plan.md")
			_ = os.WriteFile(planPath, []byte("# Plan\nDo something"), 0o644)

			store := feature.NewStore(filepath.Join(tmpDir, "state"))
			f := &feature.Feature{
				ID:           "test-impl-001",
				Name:         "Test",
				Slug:         "test",
				Description:  "routing test",
				Status:       feature.StatusImplementing,
				CurrentPhase: feature.PhaseImplement,
				Repos:        []feature.FeatureRepo{{Name: "r", Path: workDir}},
			}
			_ = store.Save(f)

			var interactiveCalled bool

			var capturedCmd []string
			var capturedOpts *session.SessionOpts
			cfg := ImplementConfig{
				Feature:                    f,
				FeatureStore:               store,
				WorkDir:                    workDir,
				PlanPath:                   planPath,
				MaxIterations:              1,
				MaxConsecFails:             3,
				MaxConsecNoProgress:        3,
				ExitCriteria:               "Relevant tests pass",
				Model:                      tt.model,
				ReviewModel:                "reviewer",
				ArtifactDir:                artifactDir,
				StateDir:                   stateDir,
				DangerouslySkipPermissions: tt.dsp,
				BuildSession: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
					sessOpts := &session.SessionOpts{
						PIDDir:        opts.PIDDir,
						PermHandler:   opts.PermHandler,
						InitialPrompt: opts.Prompt,
						RepoName:      opts.RepoName,
						LogPath:       opts.LogPath,
					}
					if tt.wantCodex {
						return []string{"codex", "app-server"}, nil, sessOpts, nil
					}
					interactiveCalled = true
					return []string{"echo", "unused"}, nil, sessOpts, nil
				},
				SessionStartFunc: func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (session.SessionHandle, error) {
					capturedCmd = command
					if len(opts) > 0 {
						capturedOpts = opts[0]
					}
					return nil, session.ErrShuttingDown
				},
			}

			result, err := RunImplementationLoop(cfg, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.FinalStatus != "interrupted" {
				t.Fatalf("expected interrupted status, got %s", result.FinalStatus)
			}

			if tt.wantCodex {
				// Codex path: command should be codex app-server
				if len(capturedCmd) < 2 || capturedCmd[0] != "codex" || capturedCmd[1] != "app-server" {
					t.Errorf("expected [codex app-server ...], got %v", capturedCmd)
				}
				if interactiveCalled {
					t.Error("InteractiveCommandBuilder should not be called for codex model")
				}
				if capturedOpts == nil {
					t.Fatal("expected SessionOpts to be captured")
				}
			} else {
				// Claude path: InteractiveCommandBuilder IS called
				if !interactiveCalled {
					t.Error("InteractiveCommandBuilder should be called for claude model")
				}
				if capturedOpts == nil {
					t.Fatal("expected SessionOpts to be captured")
				}
			}
			if !capturedOpts.KeepAliveOnTruncatedResult {
				t.Error("implementation sessions must keep stdin alive for truncated-turn auto-resume")
			}
		})
	}
}

// TestImplementLoop_SessionUsesInteractiveTurnMode proves the implement loop
// gates TurnModeInteractive on the finish-or-violate capability: it is armed
// only when ImplementConfig.FinishOrViolateNudge is set and stays at the default
// one-shot mode otherwise (the Claude/Codex path).
func TestImplementLoop_SessionUsesInteractiveTurnMode(t *testing.T) {
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
			artifactDir := filepath.Join(tmpDir, "artifacts")
			stateDir := filepath.Join(tmpDir, "state", "test-impl-tm")
			for _, d := range []string{workDir, artifactDir, stateDir} {
				os.MkdirAll(d, 0o755)
			}
			planPath := filepath.Join(artifactDir, "plan.md")
			_ = os.WriteFile(planPath, []byte("# Plan\nDo something"), 0o644)

			store := feature.NewStore(filepath.Join(tmpDir, "state"))
			f := &feature.Feature{
				ID:           "test-impl-tm",
				Name:         "Test",
				Slug:         "test",
				Status:       feature.StatusImplementing,
				CurrentPhase: feature.PhaseImplement,
				Repos:        []feature.FeatureRepo{{Name: "r", Path: workDir}},
			}
			_ = store.Save(f)

			var capturedOpts *session.SessionOpts
			cfg := ImplementConfig{
				Feature:              f,
				FeatureStore:         store,
				WorkDir:              workDir,
				PlanPath:             planPath,
				MaxIterations:        1,
				MaxConsecFails:       3,
				MaxConsecNoProgress:  3,
				ExitCriteria:         "Relevant tests pass",
				Model:                "model-a",
				ReviewModel:          "reviewer",
				ArtifactDir:          artifactDir,
				StateDir:             stateDir,
				FinishOrViolateNudge: tc.nudge,
				BuildSession: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
					return []string{"echo", "unused"}, nil, &session.SessionOpts{PIDDir: opts.PIDDir}, nil
				},
				SessionStartFunc: func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (session.SessionHandle, error) {
					if len(opts) > 0 {
						capturedOpts = opts[0]
					}
					return nil, session.ErrShuttingDown
				},
			}

			if _, err := RunImplementationLoop(cfg, nil); err != nil {
				t.Fatalf("RunImplementationLoop() error: %v", err)
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

// TestImplementLoop_FinishOrViolateNudgeRecoversSameSession proves the
// end-to-end finish-or-violate flow: a single interactive session ends its
// first turn without the completion artifacts, the harness nudges the SAME live
// session, and the nudged turn writes the artifacts + phase_complete so the
// iteration succeeds without a protocol violation.
func TestImplementLoop_FinishOrViolateNudgeRecoversSameSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-fov-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	planPath := filepath.Join(artifactDir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# Plan\nDo something"), 0o644)

	// Turn 1 emits a deliberate end_turn result with NO artifacts. The script
	// then blocks reading stdin; the finish-or-violate nudge arrives as the
	// next stdin line, after which turn 2 writes the artifacts + phase_complete
	// and emits a second success result.
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", fmt.Sprintf(`%s
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
`, testutil.JSONLInit, finishOrViolateNudgeCasePattern, testutil.WriteImplementSuccessArtifacts(artifactDir)))

	f := &feature.Feature{
		ID:            "test-fov-001",
		Name:          "Test",
		Slug:          "test",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         []feature.FeatureRepo{{Name: "test-repo", Path: workDir}},
	}
	store := feature.NewStore(filepath.Join(tmpDir, "state"))
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	sm := session.NewManager(make(chan interface{}, 100))
	defer sm.Shutdown()

	result, err := RunImplementationLoop(ImplementConfig{
		Feature:                    f,
		FeatureStore:               store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		MaxIterations:              1,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               "Relevant tests pass",
		Model:                      "implementer",
		ReviewModel:                "reviewer",
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: true,
		SkipIterationReview:        true,
		FinishOrViolateNudge:       true,
		BuildSession:               mockBuildSession(agentScript, ""),
	}, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed (LastError=%q)", result.FinalStatus, result.LastError)
	}
}

// captureSink is an io.WriteCloser that accumulates writes under a mutex,
// suitable for use as the session's fake stdin in tests exercising
// SendUserMessage paths.
type captureSink struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	done chan struct{}
}

func newCaptureSink() *captureSink {
	return &captureSink{done: make(chan struct{})}
}

func (c *captureSink) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, err := c.buf.Write(p)
	// Signal each write so tests can wait for SendUserMessage synchronization
	// without racing on the buffer.
	select {
	case c.done <- struct{}{}:
	default:
	}
	return n, err
}

func (c *captureSink) Close() error { return nil }

func (c *captureSink) contents() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// waitForWrite blocks until the sink sees another write or the timeout
// elapses. Drains any queued notifications up front so callers can issue
// an action and then wait for the resulting write without racing.
func (c *captureSink) waitForWrite(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for stdin write")
	}
}

// attachCaptureSink installs a captureSink as the session's stdin. Returns
// the sink plus the pipe reader so tests can optionally drain it.
func attachCaptureSink(sess *session.Session) *captureSink {
	sink := newCaptureSink()
	sess.SetStdinForTest(sink)
	return sink
}

// newTruncatedResult builds a ResultMessage that the classifier will treat
// as TermTurnTruncated.
func newTruncatedResult() *llm.ResultMessage {
	return &llm.ResultMessage{Subtype: "success", StopReason: "tool_use"}
}

// newEndedAfterTextResult builds a ResultMessage that the classifier will
// treat as TermEndedAfterText (deliberate stop).
func newEndedAfterTextResult() *llm.ResultMessage {
	return &llm.ResultMessage{Subtype: "success", StopReason: "end_turn"}
}

// nudgeRecorderSession is a SessionHandle test double that records the messages
// sent via SendUserMessage and can be configured to return a send error.
type nudgeRecorderSession struct {
	*utilityTestSession
	sendErr  error
	messages []string
}

func newNudgeRecorderSession(sendErr error) *nudgeRecorderSession {
	return &nudgeRecorderSession{utilityTestSession: newUtilityTestSession(), sendErr: sendErr}
}

func (s *nudgeRecorderSession) SendUserMessage(text string) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	s.messages = append(s.messages, text)
	return nil
}

func TestDecideFinishOrViolate(t *testing.T) {
	cases := []struct {
		name        string
		class       llm.TerminationClass
		startNudges int
		sendErr     error
		wantNudged  bool
		wantNudges  int
		wantSends   int
	}{
		{name: "ended after text sends nudge", class: llm.TermEndedAfterText, wantNudged: true, wantNudges: 1, wantSends: 1},
		{name: "second ended after text sends nudge", class: llm.TermEndedAfterText, startNudges: 1, wantNudged: true, wantNudges: 2, wantSends: 1},
		{name: "at cap returns false without send", class: llm.TermEndedAfterText, startNudges: maxFinishOrViolateNudges, wantNudged: false, wantNudges: maxFinishOrViolateNudges, wantSends: 0},
		{name: "truncated not nudged", class: llm.TermTurnTruncated, wantNudged: false, wantNudges: 0, wantSends: 0},
		{name: "asked formal not nudged", class: llm.TermAskedFormal, wantNudged: false, wantNudges: 0, wantSends: 0},
		{name: "errored not nudged", class: llm.TermErrored, wantNudged: false, wantNudges: 0, wantSends: 0},
		{name: "refused not nudged", class: llm.TermRefused, wantNudged: false, wantNudges: 0, wantSends: 0},
		{name: "send error returns false", class: llm.TermEndedAfterText, sendErr: errSendFailed, wantNudged: false, wantNudges: 1, wantSends: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := newNudgeRecorderSession(tc.sendErr)
			nudges := tc.startNudges
			got := decideFinishOrViolate(sess, tc.class, &nudges, []string{"plan.md"})
			if got != tc.wantNudged {
				t.Errorf("decideFinishOrViolate() = %v, want %v", got, tc.wantNudged)
			}
			if nudges != tc.wantNudges {
				t.Errorf("nudges = %d, want %d", nudges, tc.wantNudges)
			}
			if len(sess.messages) != tc.wantSends {
				t.Errorf("sends = %d, want %d", len(sess.messages), tc.wantSends)
			}
		})
	}
}

var errSendFailed = errors.New("stdin closed")

func TestFormatFinishOrViolateNudge_MentionsMissingArtifacts(t *testing.T) {
	msg := formatFinishOrViolateNudge([]string{"plan.md", "verification-report.yaml"})
	if !strings.Contains(msg, "plan.md") || !strings.Contains(msg, "verification-report.yaml") {
		t.Errorf("nudge should name missing artifacts, got: %s", msg)
	}
	if !strings.Contains(msg, "phase_complete") {
		t.Errorf("nudge should reference phase_complete marker, got: %s", msg)
	}

	empty := formatFinishOrViolateNudge(nil)
	if !strings.Contains(empty, "phase_complete") {
		t.Errorf("empty nudge should reference phase_complete marker, got: %s", empty)
	}

	// Both branches must carry the shared fragment that countNudgeMessages and
	// the integration-script case patterns match on; if the prompt wording
	// drifts away from it, those detectors silently stop firing.
	if !strings.Contains(msg, finishOrViolateNudgeFragment) {
		t.Errorf("nudge should contain %q, got: %s", finishOrViolateNudgeFragment, msg)
	}
	if !strings.Contains(empty, finishOrViolateNudgeFragment) {
		t.Errorf("empty nudge should contain %q, got: %s", finishOrViolateNudgeFragment, empty)
	}
}

// countContinuationMessages reports how many times the auto-resume
// continuation text appears in the captured stdin, via a substring match on
// a stable fragment so tests are not brittle to JSON-encoding whitespace or
// field ordering.
func countContinuationMessages(body string) int {
	return strings.Count(body, "Continue where you left off")
}

// countHandoffMessages reports how many times the context-handoff text
// appears in the captured stdin. Uses a stable fragment for the same
// robustness reason as countContinuationMessages.
func countHandoffMessages(body string) int {
	return strings.Count(body, "Wind this iteration down now")
}

type doneFirstStatusSession struct {
	*utilityTestSession
	statusCalls  atomic.Int32
	userMessages chan string
}

func newDoneFirstStatusSession() *doneFirstStatusSession {
	return &doneFirstStatusSession{
		utilityTestSession: newUtilityTestSession(),
		userMessages:       make(chan string, 2),
	}
}

func (s *doneFirstStatusSession) StatusCh() <-chan string {
	if s.statusCalls.Add(1) == 1 {
		return nil
	}
	return s.statusCh
}

func (s *doneFirstStatusSession) SendUserMessage(text string) error {
	s.userMessages <- text
	return nil
}

type cleanupFailedAfterSuccessSession struct {
	*utilityTestSession
	status session.SessionStatus
}

func newCleanupFailedAfterSuccessSession() *cleanupFailedAfterSuccessSession {
	return &cleanupFailedAfterSuccessSession{
		utilityTestSession: newUtilityTestSession(),
		status:             session.SessionRunning,
	}
}

func (s *cleanupFailedAfterSuccessSession) Status() session.SessionStatus {
	return s.status
}

func (s *cleanupFailedAfterSuccessSession) Stop() error {
	s.status = session.SessionFailed
	return nil
}

// withHandoffPollInterval temporarily lowers contextHandoffPollInterval for
// a single test so the ticker fires quickly. Restores the original interval
// via t.Cleanup so subsequent tests are unaffected.
func withHandoffPollInterval(t *testing.T, d time.Duration) {
	t.Helper()
	prev := contextHandoffPollInterval
	contextHandoffPollInterval = d
	t.Cleanup(func() { contextHandoffPollInterval = prev })
}

func handoffPollHook(ch chan<- struct{}) func() {
	return func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func waitForHandoffPolls(t *testing.T, ch <-chan struct{}, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for i := 0; i < want; i++ {
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("timed out waiting for context handoff poll %d/%d", i+1, want)
		}
	}
}

func TestRunImplementationLoop_SuccessIgnoresCleanupFailedSessionStatus(t *testing.T) {
	tmpDir := t.TempDir()
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state")
	workDir := filepath.Join(tmpDir, "work")
	observeDir := filepath.Join(tmpDir, "observe")
	for _, dir := range []string{artifactDir, stateDir, workDir, observeDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	featureID := "cleanup-success-001"
	if err := os.MkdirAll(filepath.Join(observeDir, featureID), 0o755); err != nil {
		t.Fatalf("mkdir feature observe dir: %v", err)
	}
	planPath := filepath.Join(artifactDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\nDo the thing.\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	f := &feature.Feature{
		ID:            featureID,
		Name:          "Cleanup Success",
		Slug:          "cleanup-success",
		Description:   "regression for logical success with failed cleanup",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		TraceID:       "trace-cleanup-success",
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         []feature.FeatureRepo{{Name: "agentic", Path: workDir}},
	}
	sess := newCleanupFailedAfterSuccessSession()
	obs := observe.New(true, observeDir, false, "", false, "agentic")

	cfg := ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       1,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		ExitCriteria:        "handoff parses",
		Model:               "test-model",
		ReviewModel:         "review-model",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		Observer:            obs,
		SkipIterationReview: true,
		BuildSession: func(BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			return []string{"mock-agent"}, nil, &session.SessionOpts{}, nil
		},
		SessionStartFunc: func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (session.SessionHandle, error) {
			iterDir := filepath.Join(artifactDir, "iteration-01")
			testutil.WriteImplementHandoffFiles(t, artifactDir, iterDir, "SUCCESS")
			if err := os.WriteFile(filepath.Join(iterDir, "phase_complete"), []byte("complete\n"), 0o644); err != nil {
				t.Fatalf("write phase_complete: %v", err)
			}
			sess.statusCh <- agentStatusSuccess
			return sess, nil
		},
	}

	result, err := RunImplementationLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}

	meta, err := NewArtifactManager(artifactDir).ReadMeta(filepath.Join(artifactDir, "iteration-01"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if meta.AgentStatus != agentStatusSuccess {
		t.Fatalf("AgentStatus = %q, want %q", meta.AgentStatus, agentStatusSuccess)
	}
	if meta.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 for logical SUCCESS despite failed cleanup status", meta.ExitCode)
	}

	events := readObserveEvents(t, observeDir, featureID)
	sessionEnded := filterEventsByType(events, "session.ended")
	if len(sessionEnded) != 1 {
		t.Fatalf("session.ended events = %d, want 1", len(sessionEnded))
	}
	if sessionEnded[0].Status != "ok" || sessionEnded[0].Error != "" {
		t.Fatalf("session.ended = status %q error %q, want ok with no error", sessionEnded[0].Status, sessionEnded[0].Error)
	}
}

func TestWaitForStatus_TurnTruncated_AutoResumes(t *testing.T) {
	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sink := attachCaptureSink(sess)

	var ready atomic.Bool // stays false — agent never writes phase_complete

	done := make(chan string, 1)
	go func() {
		done <- waitForImplementationStatus(sess, nil, "", func() bool { return ready.Load() })
	}()

	// Simulate a CLI-truncated SUCCESS: Cost is set, StopReason=tool_use.
	sess.SetCost(newTruncatedResult())
	sess.SendStatus("SUCCESS")

	// waitForStatus should SendUserMessage (observable via stdin write)
	// rather than flipping the session to SessionWaitingHelp.
	sink.waitForWrite(t, 2*time.Second)

	if got := countContinuationMessages(sink.contents()); got != 1 {
		t.Errorf("continuation messages written = %d, want 1", got)
	}
	if got := sess.Status(); got == session.SessionWaitingHelp {
		t.Errorf("session flipped to SessionWaitingHelp on truncation; expected auto-resume kept it running")
	}

	select {
	case <-done:
		t.Fatal("waitForStatus returned; expected it to keep waiting after auto-resume")
	default:
	}

	// Let the agent now signal completion — readyCheck will be true, so
	// waitForStatus should return SUCCESS cleanly.
	ready.Store(true)
	sess.SendStatus("SUCCESS")

	select {
	case got := <-done:
		if got != "SUCCESS" {
			t.Errorf("waitForStatus() = %q, want SUCCESS", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for final SUCCESS")
	}
}

func TestWaitForStatus_TurnTruncated_RetryCapEscalates(t *testing.T) {
	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sink := attachCaptureSink(sess)

	done := make(chan string, 1)
	go func() {
		done <- waitForStatus(sess, nil, "", func() bool { return false })
	}()

	// Send exactly maxAutoResumeAttempts truncated SUCCESSes — each should
	// trigger an auto-resume message without escalating.
	for i := 0; i < maxAutoResumeAttempts; i++ {
		sess.SetCost(newTruncatedResult())
		sess.SendStatus("SUCCESS")
		sink.waitForWrite(t, 2*time.Second)
	}

	if got := countContinuationMessages(sink.contents()); got != maxAutoResumeAttempts {
		t.Errorf("continuation messages = %d, want %d", got, maxAutoResumeAttempts)
	}

	// One more truncated SUCCESS beyond the cap — this one must return a
	// missing-marker status instead of sending another continuation.
	sess.SetCost(newTruncatedResult())
	sess.SendStatus("SUCCESS")

	if got := countContinuationMessages(sink.contents()); got != maxAutoResumeAttempts {
		t.Errorf("continuation messages after cap = %d, want %d (no extra send)", got, maxAutoResumeAttempts)
	}

	select {
	case got := <-done:
		if got != agentStatusMissingMarker {
			t.Fatalf("waitForStatus() = %q, want %q", got, agentStatusMissingMarker)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waitForStatus to return")
	}
}

// TestWaitForStatus_EndedAfterText_ReturnsMissingMarker proves that, with the
// finish-or-violate capability armed (the canonical capability-positive path),
// a deliberate end_turn without the completion marker sends exactly one nudge
// on the first turn and keeps waiting on the same live session rather than
// escalating immediately. (No truncation auto-resume fires either.)
func TestWaitForStatus_EndedAfterText_ReturnsMissingMarker(t *testing.T) {
	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sink := attachCaptureSink(sess)

	done := make(chan string, 1)
	go func() {
		done <- waitForStatusDetailed(sess, nil, "", waitForStatusOptions{
			ReadyCheck:           func() bool { return false },
			FinishOrViolateNudge: true,
		}).Status
	}()

	// A deliberate end_turn SUCCESS — no truncation auto-resume should fire,
	// but the capability is armed so exactly one nudge is sent.
	sess.SetCost(newEndedAfterTextResult())
	sess.SendStatus("SUCCESS")
	sink.waitForWrite(t, 2*time.Second)

	if got := countContinuationMessages(sink.contents()); got != 0 {
		t.Errorf("unexpected continuation messages on end_turn: %d, want 0", got)
	}
	if got := countNudgeMessages(sink.contents()); got != 1 {
		t.Errorf("nudge messages on first end_turn = %d, want 1", got)
	}

	// The nudge keeps the session alive; the function must not have returned.
	select {
	case got := <-done:
		t.Fatalf("waitForStatus returned %q; expected it to keep waiting after the nudge", got)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestWaitForStatus_EndedAfterText_RetryStateNotNudged locks the
// RETRY/NEED_USER_INPUT structural guard: when the capability is armed but the
// readiness check passes (the agent wrote phase_complete + valid progress.md
// before ending its turn, as a legitimate RETRY does), the nudge block is never
// entered — the function returns SUCCESS and sends zero nudges.
func TestWaitForStatus_EndedAfterText_RetryStateNotNudged(t *testing.T) {
	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sink := attachCaptureSink(sess)

	done := make(chan string, 1)
	go func() {
		done <- waitForStatusDetailed(sess, nil, "", waitForStatusOptions{
			ReadyCheck:           func() bool { return true },
			FinishOrViolateNudge: true,
		}).Status
	}()

	// A deliberate end_turn SUCCESS with the readiness check satisfied — the
	// RETRY path. !isReady() is false, so the nudge block is unreachable.
	sess.SetCost(newEndedAfterTextResult())
	sess.SendStatus("SUCCESS")

	select {
	case got := <-done:
		if got != agentStatusSuccess {
			t.Fatalf("waitForStatus() = %q, want %q", got, agentStatusSuccess)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waitForStatus to return")
	}
	if got := countNudgeMessages(sink.contents()); got != 0 {
		t.Errorf("nudge messages = %d, want 0 (RETRY path must bypass the nudge)", got)
	}
}

// countNudgeMessages reports how many finish-or-violate nudges appear in the
// captured stdin, via the shared finishOrViolateNudgeFragment so this stays in
// sync with the prompt wording and the integration-script case patterns.
func countNudgeMessages(body string) int {
	return strings.Count(body, finishOrViolateNudgeFragment)
}

// finishOrViolateNudgeCasePattern is the shell `case` glob the integration
// scripts use to detect the nudge on stdin. Derived from the production
// fragment (spaces escaped for the shell) so a prompt-wording change keeps the
// scripts in sync automatically.
var finishOrViolateNudgeCasePattern = "*" + strings.ReplaceAll(finishOrViolateNudgeFragment, " ", `\ `) + "*"

// TestWaitForStatus_EndedAfterText_NudgesThenSucceeds proves that, with the
// finish-or-violate capability armed, a deliberate end_turn without the
// completion marker sends one nudge to the same live session and then returns
// SUCCESS cleanly once the agent finishes.
func TestWaitForStatus_EndedAfterText_NudgesThenSucceeds(t *testing.T) {
	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sink := attachCaptureSink(sess)

	var ready atomic.Bool

	done := make(chan string, 1)
	go func() {
		done <- waitForStatusDetailed(sess, nil, "", waitForStatusOptions{
			ReadyCheck:           func() bool { return ready.Load() },
			FinishOrViolateNudge: true,
			MissingArtifacts:     []string{"plan.md"},
		}).Status
	}()

	// First end_turn without the marker — should nudge, not escalate.
	sess.SetCost(newEndedAfterTextResult())
	sess.SendStatus("SUCCESS")
	sink.waitForWrite(t, 2*time.Second)

	if got := countNudgeMessages(sink.contents()); got != 1 {
		t.Errorf("nudge messages = %d, want 1", got)
	}
	select {
	case <-done:
		t.Fatal("waitForStatus returned; expected it to keep waiting after the nudge")
	default:
	}

	// Now the agent finishes — readyCheck becomes true.
	ready.Store(true)
	sess.SendStatus("SUCCESS")

	select {
	case got := <-done:
		if got != "SUCCESS" {
			t.Errorf("waitForStatus() = %q, want SUCCESS", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for final SUCCESS")
	}
}

// TestWaitForStatus_EndedAfterText_NudgeCapEscalates proves the nudge budget is
// bounded: after maxFinishOrViolateNudges nudges, a further end_turn without the
// marker returns the missing-marker status instead of nudging again.
func TestWaitForStatus_EndedAfterText_NudgeCapEscalates(t *testing.T) {
	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sink := attachCaptureSink(sess)

	done := make(chan string, 1)
	go func() {
		done <- waitForStatusDetailed(sess, nil, "", waitForStatusOptions{
			ReadyCheck:           func() bool { return false },
			FinishOrViolateNudge: true,
		}).Status
	}()

	for i := 0; i < maxFinishOrViolateNudges; i++ {
		sess.SetCost(newEndedAfterTextResult())
		sess.SendStatus("SUCCESS")
		sink.waitForWrite(t, 2*time.Second)
	}
	if got := countNudgeMessages(sink.contents()); got != maxFinishOrViolateNudges {
		t.Errorf("nudge messages = %d, want %d", got, maxFinishOrViolateNudges)
	}

	// One more end_turn beyond the cap — must escalate, no extra nudge.
	sess.SetCost(newEndedAfterTextResult())
	sess.SendStatus("SUCCESS")

	select {
	case got := <-done:
		if got != agentStatusMissingMarker {
			t.Fatalf("waitForStatus() = %q, want %q", got, agentStatusMissingMarker)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waitForStatus to return")
	}
	if got := countNudgeMessages(sink.contents()); got != maxFinishOrViolateNudges {
		t.Errorf("nudge messages after cap = %d, want %d (no extra send)", got, maxFinishOrViolateNudges)
	}
}

// TestWaitForStatus_EndedAfterText_DisabledNotNudged proves the nudge is gated:
// when the capability is not armed, a deliberate end_turn returns the
// missing-marker status with no nudge (the Claude/Codex path).
func TestWaitForStatus_EndedAfterText_DisabledNotNudged(t *testing.T) {
	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sink := attachCaptureSink(sess)

	done := make(chan string, 1)
	go func() {
		done <- waitForStatusDetailed(sess, nil, "", waitForStatusOptions{
			ReadyCheck: func() bool { return false },
		}).Status
	}()

	sess.SetCost(newEndedAfterTextResult())
	sess.SendStatus("SUCCESS")

	select {
	case got := <-done:
		if got != agentStatusMissingMarker {
			t.Fatalf("waitForStatus() = %q, want %q", got, agentStatusMissingMarker)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waitForStatus to return")
	}
	if got := countNudgeMessages(sink.contents()); got != 0 {
		t.Errorf("nudge messages = %d, want 0 when capability disabled", got)
	}
}

func TestWaitForStatus_AskedFormal_ReturnsMissingMarker(t *testing.T) {
	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sink := attachCaptureSink(sess)

	// Seed a pending AskUserQuestion control request so the classifier
	// returns TermAskedFormal even though stop_reason looks like truncation.
	sess.SetLastControlRequest(&llm.ControlRequestMessage{
		Type:      "control_request",
		RequestID: "req-ask-1",
		Request:   llm.ControlRequest{ToolName: "AskUserQuestion"},
	})

	done := make(chan string, 1)
	go func() {
		done <- waitForStatus(sess, nil, "", func() bool { return false })
	}()

	sess.SetCost(newTruncatedResult())
	sess.SendStatus("SUCCESS")

	if got := countContinuationMessages(sink.contents()); got != 0 {
		t.Errorf("continuation messages = %d, want 0 when AskUserQuestion is pending", got)
	}

	select {
	case got := <-done:
		if got != agentStatusMissingMarker {
			t.Fatalf("waitForStatus() = %q, want %q", got, agentStatusMissingMarker)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waitForStatus to return")
	}
}

// Compile-time assertion that captureSink satisfies io.WriteCloser.
var _ io.WriteCloser = (*captureSink)(nil)

func TestWaitForStatus_ContextHandoff_SendsOnceAtThreshold(t *testing.T) {
	withHandoffPollInterval(t, 2*time.Millisecond)

	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sink := attachCaptureSink(sess)

	// Seed usage at exactly the threshold — 60%, on a 1M window.
	sess.SetLatestUsage(&llm.Usage{InputTokens: 600_000, ContextWindow: 1_000_000})

	var ready atomic.Bool // stays false until the test flips it.
	polls := make(chan struct{}, 8)

	done := make(chan string, 1)
	go func() {
		done <- waitForStatusDetailed(sess, nil, "", waitForStatusOptions{
			ReadyCheck:             func() bool { return ready.Load() },
			EnableContextHandoff:   true,
			ContextHandoffPollHook: handoffPollHook(polls),
		}).Status
	}()

	// Wait for the ticker to observe the threshold and send the handoff.
	sink.waitForWrite(t, 2*time.Second)

	if got := countHandoffMessages(sink.contents()); got != 1 {
		t.Errorf("handoff messages = %d, want 1", got)
	}
	if !strings.Contains(sink.contents(), "above Agentic's 60% handoff threshold") {
		t.Errorf("handoff message should include observed threshold, got: %s", sink.contents())
	}
	if got := countContinuationMessages(sink.contents()); got != 0 {
		t.Errorf("unexpected auto-resume messages sent alongside handoff: %d", got)
	}

	// Observe several more ticker passes; the handoff must not be sent a
	// second time while the iteration is still running.
	waitForHandoffPolls(t, polls, 3, 2*time.Second)
	if got := countHandoffMessages(sink.contents()); got != 1 {
		t.Errorf("handoff messages after extra ticks = %d, want 1 (send-once semantics)", got)
	}

	// Let the agent complete cleanly.
	ready.Store(true)
	sess.SendStatus("SUCCESS")

	select {
	case got := <-done:
		if got != "SUCCESS" {
			t.Errorf("waitForStatus() = %q, want SUCCESS", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for final SUCCESS")
	}
}

func TestWaitForStatus_ContextHandoff_CodexUsesDefaultThreshold(t *testing.T) {
	withHandoffPollInterval(t, 2*time.Millisecond)

	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sess.SetProviderName("codex")
	sink := attachCaptureSink(sess)

	// Codex follows the shared 60% threshold once the window is at least 1M.
	sess.SetLatestUsage(&llm.Usage{InputTokens: 600_000, ContextWindow: 1_000_000})

	done := make(chan string, 1)
	go func() {
		done <- waitForStatusDetailed(sess, nil, "", waitForStatusOptions{
			ReadyCheck:           func() bool { return true },
			EnableContextHandoff: true,
		}).Status
	}()

	sink.waitForWrite(t, 2*time.Second)

	if got := countHandoffMessages(sink.contents()); got != 1 {
		t.Errorf("handoff messages at 60%% for codex = %d, want 1", got)
	}
	if !strings.Contains(sink.contents(), "above Agentic's 60% handoff threshold") {
		t.Errorf("codex handoff message should include 60%% threshold, got: %s", sink.contents())
	}

	sess.SendStatus("SUCCESS")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waitForStatus to return")
	}
}

func TestWaitForStatus_ContextHandoffReturnsSnapshot(t *testing.T) {
	withHandoffPollInterval(t, 2*time.Millisecond)

	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sess.SetProviderName("codex")
	sink := attachCaptureSink(sess)
	sess.SetLatestUsage(&llm.Usage{
		ContextTotalTokens: 604_800,
		ContextWindow:      1_000_000,
		ContextBaseline:    12_000,
	})

	done := make(chan waitForStatusResult, 1)
	go func() {
		done <- waitForStatusDetailed(sess, nil, "", waitForStatusOptions{
			ReadyCheck:           func() bool { return true },
			EnableContextHandoff: true,
		})
	}()

	sink.waitForWrite(t, 2*time.Second)
	sess.SendStatus("SUCCESS")

	select {
	case got := <-done:
		if got.Handoff.Pct != 60 {
			t.Errorf("Handoff.Pct = %d, want 60", got.Handoff.Pct)
		}
		if got.Handoff.TotalTokens != 604_800 {
			t.Errorf("Handoff.TotalTokens = %d, want 604800", got.Handoff.TotalTokens)
		}
		if got.Handoff.WindowTokens != 1_000_000 {
			t.Errorf("Handoff.WindowTokens = %d, want 1000000", got.Handoff.WindowTokens)
		}
		if got.Handoff.BaselineTokens != 12_000 {
			t.Errorf("Handoff.BaselineTokens = %d, want 12000", got.Handoff.BaselineTokens)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waitForStatus to return")
	}
}

func TestWaitForStatus_ContextHandoff_BelowThreshold_NoSend(t *testing.T) {
	withHandoffPollInterval(t, 2*time.Millisecond)

	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sink := attachCaptureSink(sess)

	// 40% usage — comfortably below the 60% threshold.
	sess.SetLatestUsage(&llm.Usage{InputTokens: 400_000, ContextWindow: 1_000_000})
	polls := make(chan struct{}, 8)

	done := make(chan string, 1)
	go func() {
		done <- waitForStatusDetailed(sess, nil, "", waitForStatusOptions{
			ReadyCheck:             func() bool { return true },
			EnableContextHandoff:   true,
			ContextHandoffPollHook: handoffPollHook(polls),
		}).Status
	}()

	// Observe several ticker passes below threshold.
	waitForHandoffPolls(t, polls, 3, 2*time.Second)

	if got := countHandoffMessages(sink.contents()); got != 0 {
		t.Errorf("handoff messages below threshold = %d, want 0", got)
	}

	// Clean up.
	sess.SendStatus("SUCCESS")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waitForStatus to return")
	}
}

func TestWaitForStatus_ContextHandoff_DisabledBelowOneMillionWindow(t *testing.T) {
	withHandoffPollInterval(t, 2*time.Millisecond)

	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sink := attachCaptureSink(sess)

	// 90% usage would otherwise cross the default threshold, but the
	// implementation nudge is disabled for context windows below 1M tokens.
	sess.SetLatestUsage(&llm.Usage{InputTokens: 180_000, ContextWindow: 200_000})
	polls := make(chan struct{}, 8)

	done := make(chan string, 1)
	go func() {
		done <- waitForStatusDetailed(sess, nil, "", waitForStatusOptions{
			ReadyCheck:             func() bool { return true },
			EnableContextHandoff:   true,
			ContextHandoffPollHook: handoffPollHook(polls),
		}).Status
	}()

	waitForHandoffPolls(t, polls, 3, 2*time.Second)

	if got := countHandoffMessages(sink.contents()); got != 0 {
		t.Errorf("handoff messages below 1M window = %d, want 0", got)
	}

	sess.SendStatus("SUCCESS")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waitForStatus to return")
	}
}

func TestWaitForStatus_ContextHandoff_NotSentBeforeUsageArrives(t *testing.T) {
	withHandoffPollInterval(t, 2*time.Millisecond)

	sess := session.NewSession("test", "feat", feature.PhaseImplement)
	sink := attachCaptureSink(sess)

	// latestUsage stays nil — ContextPercentage() returns -1, so the
	// threshold check must not trip.
	polls := make(chan struct{}, 8)

	done := make(chan string, 1)
	go func() {
		done <- waitForStatusDetailed(sess, nil, "", waitForStatusOptions{
			ReadyCheck:             func() bool { return true },
			EnableContextHandoff:   true,
			ContextHandoffPollHook: handoffPollHook(polls),
		}).Status
	}()

	waitForHandoffPolls(t, polls, 3, 2*time.Second)

	if got := countHandoffMessages(sink.contents()); got != 0 {
		t.Errorf("handoff messages before usage data = %d, want 0", got)
	}

	sess.SendStatus("SUCCESS")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waitForStatus to return")
	}
}

func TestParseLargeCodexCommandOutput(t *testing.T) {
	line := []byte(`{"method":"item/completed","params":{"item":{"type":"commandExecution","command":"rg -n foo","aggregatedOutput":"` + strings.Repeat("x", 21) + `","exitCode":0,"durationMs":123}}}`)
	got, ok := parseLargeCodexCommandOutput(line, 20)
	if !ok {
		t.Fatal("expected large command output to be detected")
	}
	if got.Command != "rg -n foo" {
		t.Errorf("Command = %q, want rg command", got.Command)
	}
	if got.OutputChars != 21 {
		t.Errorf("OutputChars = %d, want 21", got.OutputChars)
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", got.ExitCode)
	}
	if got.DurationMs == nil || *got.DurationMs != 123 {
		t.Errorf("DurationMs = %v, want 123", got.DurationMs)
	}
}

func TestParseLargeCodexCommandOutputIgnoresSmallAndNonCommandEvents(t *testing.T) {
	small := []byte(`{"method":"item/completed","params":{"item":{"type":"commandExecution","command":"cat file","aggregatedOutput":"short"}}}`)
	if _, ok := parseLargeCodexCommandOutput(small, 20); ok {
		t.Fatal("small output should not be reported")
	}

	assistant := []byte(`{"method":"item/completed","params":{"item":{"type":"agentMessage","text":"` + strings.Repeat("x", 30) + `"}}}`)
	if _, ok := parseLargeCodexCommandOutput(assistant, 20); ok {
		t.Fatal("non-command output should not be reported")
	}
}

func TestWaitForStatus_ContextHandoff_DisabledForSharedWaiter(t *testing.T) {
	withHandoffPollInterval(t, 2*time.Millisecond)

	sess := session.NewSession("test", "feat", feature.PhaseReview)
	sink := attachCaptureSink(sess)

	sess.SetLatestUsage(&llm.Usage{InputTokens: 120_000, ContextWindow: 200_000})

	done := make(chan string, 1)
	go func() {
		done <- waitForStatus(sess, nil, "", func() bool { return true })
	}()

	if got := countHandoffMessages(sink.contents()); got != 0 {
		t.Errorf("handoff messages from shared waiter = %d, want 0", got)
	}

	sess.SendStatus("SUCCESS")
	select {
	case got := <-done:
		if got != "SUCCESS" {
			t.Errorf("waitForStatus() = %q, want SUCCESS", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waitForStatus to return")
	}
}

func TestWaitForStatus_ReadyCheckFalse(t *testing.T) {
	sess := session.NewSession("test", "feat", feature.PhaseImplement)

	var ready atomic.Bool
	ready.Store(false)

	done := make(chan string, 1)
	go func() {
		got := waitForStatus(sess, nil, "", func() bool {
			return ready.Load()
		})
		done <- got
	}()

	// SUCCESS with a false readyCheck should return a structured
	// missing-marker status instead of parking the session for help.
	sess.SendStatus("SUCCESS")

	select {
	case got := <-done:
		if got != agentStatusMissingMarker {
			t.Errorf("waitForStatus() = %q, want %q", got, agentStatusMissingMarker)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for waitForStatus")
	}
}

// TestImplementLoopSkipIterationReview verifies that when SkipIterationReview
// is true, the loop returns "review_passed" immediately on SUCCESS without
// invoking the review gate (BuildSession called only once for implementation).
func TestImplementLoopSkipIterationReview(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-skip-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Agent script: emits SUCCESS
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Working...")+"\n"+
			testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	// Review script: should NOT be called when SkipIterationReview is true
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	planPath := filepath.Join(artifactDir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# Plan\nImplement something"), 0o644)

	f := &feature.Feature{
		ID:           "test-skip-001",
		Name:         "Test Skip Review",
		Slug:         "test-skip-review",
		Description:  "Skip iteration review test",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: workDir},
		},
	}

	store := feature.NewStore(filepath.Join(tmpDir, "state"))
	_ = store.Save(f)

	buildSession, captured := capturingBuildSession(agentScript, reviewScript)

	cfg := ImplementConfig{
		Feature:                    f,
		FeatureStore:               store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		MaxIterations:              3,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               "Relevant tests pass",
		Model:                      "opus",
		ReviewModel:                "reviewer",
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
		SkipIterationReview:        true,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}
	if result.Iterations != 1 {
		t.Errorf("expected Iterations=1, got %d", result.Iterations)
	}

	// BuildSession should have been called exactly once (implementation only,
	// no review gate).
	if len(*captured) != 1 {
		t.Errorf("expected BuildSession called 1 time (impl only), got %d", len(*captured))
	}
}

// TestImplementLoopNoSkipIterationReview verifies that when SkipIterationReview
// is false, the loop runs the per-iteration review gate after SUCCESS
// (BuildSession called at least twice: once for implementation, once for review).
func TestImplementLoopNoSkipIterationReview(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-noskip-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Agent script: emits SUCCESS
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Working...")+"\n"+
			testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	// Review script: approves immediately
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	planPath := filepath.Join(artifactDir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# Plan\nImplement something"), 0o644)

	f := &feature.Feature{
		ID:           "test-noskip-001",
		Name:         "Test No Skip Review",
		Slug:         "test-no-skip-review",
		Description:  "No skip iteration review test",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: workDir},
		},
	}

	store := feature.NewStore(filepath.Join(tmpDir, "state"))
	_ = store.Save(f)

	buildSession, captured := capturingBuildSession(agentScript, reviewScript)

	cfg := ImplementConfig{
		Feature:                    f,
		FeatureStore:               store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		MaxIterations:              3,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               "Relevant tests pass",
		Model:                      "opus",
		ReviewModel:                "reviewer",
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
		SkipIterationReview:        false,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}

	// BuildSession should have been called at least twice: once for
	// implementation and once for the review gate.
	if len(*captured) < 2 {
		t.Errorf("expected BuildSession called at least 2 times (impl + review), got %d", len(*captured))
	}
	assertExplicitEmptyAgentNames(t, (*captured)[0].AgentNames)
	assertExplorationAgentNames(t, (*captured)[1].AgentNames)
}

func TestImplementLoopReviewHelperUsesChildDirAndMarker(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-review-child-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	planPath := filepath.Join(artifactDir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# Plan\nImplement something"), 0o644)

	f := newTestFeature(t, workDir)
	f.ID = "test-review-child-001"
	store := feature.NewStore(filepath.Join(tmpDir, "state"))
	_ = store.Save(f)

	result, err := RunImplementationLoop(ImplementConfig{
		Feature:                    f,
		FeatureStore:               store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		MaxIterations:              3,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               "Relevant tests pass",
		Model:                      "agent",
		ReviewModel:                "reviewer",
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: true,
		BuildSession:               mockBuildSession(agentScript, reviewScript),
	}, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	iterDir := filepath.Join(artifactDir, "iteration-01")
	for _, path := range []string{
		filepath.Join(iterDir, "review", "phase_complete"),
		filepath.Join(iterDir, "review", "review-feedback.md"),
		filepath.Join(iterDir, "review-feedback.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected review artifact %q: %v", path, err)
		}
	}
}

func TestImplementLoopReviewHelperMissingPhaseCompleteCountsConsecutiveFailure(t *testing.T) {
	// Extended-regression owner: review-helper protocol violations tripping the consecutive-failure rail.
	if testing.Short() {
		t.Skip("skipping in short mode: extended regression covers review-helper protocol violations tripping the consecutive-failure rail")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-review-marker-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review-no-marker.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApprovedWithoutPhaseComplete(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	planPath := filepath.Join(artifactDir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# Plan\nImplement something"), 0o644)

	f := newTestFeature(t, workDir)
	f.ID = "test-review-marker-001"
	store := feature.NewStore(filepath.Join(tmpDir, "state"))
	_ = store.Save(f)

	result, err := RunImplementationLoop(ImplementConfig{
		Feature:                    f,
		FeatureStore:               store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		MaxIterations:              5,
		MaxConsecFails:             2,
		MaxConsecNoProgress:        5,
		ExitCriteria:               "Relevant tests pass",
		Model:                      "agent",
		ReviewModel:                "reviewer",
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: true,
		BuildSession:               mockBuildSession(agentScript, reviewScript),
	}, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != "protocol_violation" {
		t.Fatalf("FinalStatus = %q, want protocol_violation (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if !strings.Contains(result.LastError, "iteration_reviewer") || !strings.Contains(result.LastError, "phase_complete") {
		t.Fatalf("LastError = %q, want iteration_reviewer phase_complete violation", result.LastError)
	}

	metaBytes, err := os.ReadFile(filepath.Join(artifactDir, "iteration-02", "meta.yaml"))
	if err != nil {
		t.Fatalf("reading iteration-02 meta.yaml: %v", err)
	}
	var meta IterationMeta
	if err := yaml.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("unmarshal iteration-02 meta.yaml: %v", err)
	}
	if meta.AgentStatus != agentStatusProtocolViolation {
		t.Fatalf("AgentStatus = %q, want %q", meta.AgentStatus, agentStatusProtocolViolation)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "iteration-02", "review", "review-feedback.md")); err != nil {
		t.Fatalf("expected helper review feedback in child dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "iteration-02", "review-feedback.md")); err != nil {
		t.Fatalf("expected mirrored parent review feedback: %v", err)
	}
	parentFeedback, err := os.ReadFile(filepath.Join(artifactDir, "iteration-02", "review-feedback.md"))
	if err != nil {
		t.Fatalf("reading mirrored parent review feedback: %v", err)
	}
	if !strings.Contains(string(parentFeedback), "phase_complete") {
		t.Fatalf("parent review feedback missing phase_complete violation:\n%s", parentFeedback)
	}
	if !strings.Contains(string(parentFeedback), "CHANGES_REQUESTED") || strings.Contains(string(parentFeedback), "\nAPPROVED") {
		t.Fatalf("parent review feedback did not override approved verdict:\n%s", parentFeedback)
	}
}

func TestImplementLoopMixedFailuresTripIterationDeterminesFinalStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tests := []struct {
		name       string
		sequence   []string
		wantStatus string
	}{
		{name: "contract violation trips", sequence: []string{"drift", "crash", "drift"}, wantStatus: "protocol_violation"},
		{name: "agent crash trips", sequence: []string{"crash", "drift", "crash"}, wantStatus: "safety_rail"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runImplementLoopWithMixedFailureScript(t, tt.sequence, 3)
			if result.FinalStatus != tt.wantStatus {
				t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, tt.wantStatus)
			}
			if result.Iterations != len(tt.sequence) {
				t.Fatalf("Iterations = %d, want %d", result.Iterations, len(tt.sequence))
			}
		})
	}
}

func runImplementLoopWithMixedFailureScript(t *testing.T, sequence []string, maxConsecFails int) *LoopResult {
	t.Helper()

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-mixed-failures")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh", mixedFailureScript(artifactDir, sequence))
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)
	planPath := writePlanFile(t, artifactDir, "Plan")

	result, err := RunImplementationLoop(ImplementConfig{
		Feature:             f,
		WorkDir:             workDir,
		PlanPath:            planPath,
		MaxIterations:       10,
		MaxConsecFails:      maxConsecFails,
		MaxConsecNoProgress: 10,
		ExitCriteria:        "Relevant tests pass",
		Model:               "agent",
		ReviewModel:         "reviewer",
		ArtifactDir:         artifactDir,
		StateDir:            stateDir,
		BuildSession:        mockBuildSession(agentScript, reviewScript),
	}, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	return result
}

func mixedFailureScript(artifactDir string, sequence []string) string {
	var b strings.Builder
	b.WriteString(testutil.JSONLInit)
	b.WriteString("\n")
	b.WriteString(`_counter="` + artifactDir + `/.mixed-failure-step"
_step=0
if [ -f "$_counter" ]; then
  _step=$(cat "$_counter")
fi
_step=$((_step + 1))
printf '%s' "$_step" > "$_counter"
case "$_step" in
`)
	for i, failure := range sequence {
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(")\n")
		switch failure {
		case "drift":
			b.WriteString(testutil.WriteImplementProtocolViolation(artifactDir))
			b.WriteString("\n")
			b.WriteString(testutil.JSONLSuccess)
		case "crash":
			b.WriteString(testutil.JSONLError("mock agent crash"))
		}
		b.WriteString("\n;;\n")
	}
	b.WriteString("*)\n")
	b.WriteString(testutil.JSONLError("unexpected mixed-failure script invocation"))
	b.WriteString("\n;;\n")
	b.WriteString("esac\n")
	return b.String()
}

func TestImplementLoop_WritesTestingContractForRoadmapPhase(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateRoot := filepath.Join(tmpDir, "state")
	stateDir := filepath.Join(stateRoot, "test-contract-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLAssistant("Working...")+"\n"+
			testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	planPath := filepath.Join(artifactDir, "plan.md")
	plan := "#### Automated Verification:\n- [ ] Agent package tests pass: `go test ./internal/agent/... -count=1`\n"
	_ = os.WriteFile(planPath, []byte(plan), 0o644)

	f := &feature.Feature{
		ID:                  "test-contract-001",
		Name:                "Test Contract",
		Slug:                "test-contract",
		Description:         "Writes testing contract for roadmap phase",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 1,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: workDir},
		},
	}

	store := feature.NewStore(stateRoot)
	_ = store.Save(f)

	buildSession, _ := capturingBuildSession(agentScript, reviewScript)
	cfg := ImplementConfig{
		Feature:                    f,
		FeatureStore:               store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		MaxIterations:              1,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               "Relevant tests pass",
		Model:                      "opus",
		ReviewModel:                "reviewer",
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
		SkipIterationReview:        true,
		PhaseType:                  "tracer-bullet",
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %s, want review_passed", result.FinalStatus)
	}

	contractPath := PhaseTestingContractPath(stateRoot, f, 1)
	if _, err := os.Stat(contractPath); err != nil {
		t.Fatalf("expected testing contract at %s: %v", contractPath, err)
	}
	contract, err := ReadTestingContract(contractPath)
	if err != nil {
		t.Fatalf("ReadTestingContract: %v", err)
	}
	if contract.Scope != "implementation" && contract.Scope != "phase-01" {
		t.Fatalf("unexpected contract scope %q", contract.Scope)
	}

	reportPath := filepath.Join(artifactDir, "iteration-01", "verification-report.yaml")
	report, err := ReadVerificationReport(reportPath)
	if err != nil {
		t.Fatalf("ReadVerificationReport: %v", err)
	}
	if report.Version != 2 {
		t.Fatalf("report Version = %d, want 2", report.Version)
	}
	if report.ContractPath != contractPath {
		t.Fatalf("report ContractPath = %q, want %q", report.ContractPath, contractPath)
	}
	if len(report.Results) == 0 || report.Results[0].ItemID == "" {
		t.Fatalf("expected contract-backed results with item IDs, got %+v", report.Results)
	}
}

// TestImplementLoop_IntegrityGateRejectsWithoutLLM verifies that when the
// implementation writes a verification-report.yaml whose pass claim is
// contradicted by its own evidence (hedge phrases like "CAVEAT" and
// "pre-existing bug"), the deterministic Report Integrity Gate rejects it
// before the LLM reviewer is invoked.
//
// Proof shape: MaxIterations=1, agent script writes an invalid report and
// signals SUCCESS. The loop should reach "max_iterations" (because the gate
// returns CHANGES_REQUESTED and no further iterations are allowed), and
// BuildSession should be called exactly once (implementation only — the
// review script must never run).
func TestImplementLoop_IntegrityGateRejectsWithoutLLM(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-gate-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	// Agent script: overwrites verification-report.yaml in the latest
	// iteration dir with invalid content (pass + hedge evidence),
	// touches phase_complete, then emits SUCCESS.
	badReport := `version: 1
required_checks:
  - name: Unit tests
    requirement: go test ./...
    status: passed
    evidence: |
      tests exit 0 locally. CAVEAT: pre-existing bug on macOS; orthogonal to this phase.
`
	invalidReportCmd := "_d=\"\"; for d in \"" + artifactDir + "\"/iteration-*; do _d=\"$d\"; done; cat > \"$_d/verification-report.yaml\" <<'REPORT_EOF'\n" + badReport + "REPORT_EOF"
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			invalidReportCmd+"\n"+
			// Pair the custom invalid report with a
			// minimal valid progress.md so the new harness handoff parser
			// passes and the iteration reaches the Report Integrity Gate
			// where the contradiction is rejected.
			testutil.WriteImplementProgressMd(artifactDir, "SUCCESS")+"\n"+
			testutil.TouchPhaseComplete(artifactDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	// Review script: MUST NOT run. If it does, we'd see an APPROVED result.
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	planPath := filepath.Join(artifactDir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# Plan\nImplement something"), 0o644)

	f := &feature.Feature{
		ID:           "test-gate-001",
		Name:         "Test Integrity Gate",
		Slug:         "test-integrity-gate",
		Description:  "Gate must reject hedge-phrase report without invoking LLM",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: workDir},
		},
	}

	store := feature.NewStore(filepath.Join(tmpDir, "state"))
	_ = store.Save(f)

	buildSession, captured := capturingBuildSession(agentScript, reviewScript)

	cfg := ImplementConfig{
		Feature:                    f,
		FeatureStore:               store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		MaxIterations:              1,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               "Relevant tests pass",
		Model:                      "opus",
		ReviewModel:                "reviewer",
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
		SkipIterationReview:        false,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalStatus != "max_iterations" {
		t.Errorf("expected FinalStatus=max_iterations (gate rejects, loop hits cap), got %s", result.FinalStatus)
	}

	// BuildSession must have been called exactly once — for the
	// implementation step. If the gate had let the report through, a
	// second call for the review step would also have been recorded.
	if len(*captured) != 1 {
		t.Fatalf("expected BuildSession called exactly 1 time (impl only; gate must skip LLM review), got %d", len(*captured))
	}

	// The gate must have written review-feedback.md with structured output.
	feedbackPath := filepath.Join(artifactDir, "iteration-01", "review-feedback.md")
	feedbackBytes, err := os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatalf("expected review-feedback.md at %s: %v", feedbackPath, err)
	}
	feedback := string(feedbackBytes)
	wantSubstrings := []string{
		"Report Integrity Gate",
		"CHANGES_REQUESTED",
		"caveat", // hedge phrase from the agent's own evidence
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(strings.ToLower(feedback), strings.ToLower(s)) {
			t.Errorf("review-feedback.md missing %q; got:\n%s", s, feedback)
		}
	}
}

func TestImplementLoop_IntegrityGateRejectsStaleContractRevision(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateRoot := filepath.Join(tmpDir, "state")
	stateDir := filepath.Join(stateRoot, "test-stale-contract-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		os.MkdirAll(d, 0o755)
	}

	planPath := filepath.Join(artifactDir, "plan.md")
	plan := "#### Automated Verification:\n- [ ] Agent package tests pass: `go test ./internal/agent/... -count=1`\n"
	if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	f := &feature.Feature{
		ID:                  "test-stale-contract-001",
		Name:                "Test Stale Contract Revision",
		Slug:                "test-stale-contract-revision",
		Description:         "Gate must reject stale contract revisions before bounded review",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 1,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: workDir},
		},
	}

	contractPath := PhaseTestingContractPath(stateRoot, f, 1)
	contract := CompileTestingContract(plan, planPath, "tdd-fill-in")
	revisedContract, err := ReviseTestingContract(&contract, []TestingContractChange{
		{
			ItemID:       contract.Items[len(contract.Items)-1].ID,
			ChangeReason: "tighten the contract after implementation starts",
			ChangedBy:    "implementer",
		},
	})
	if err != nil {
		t.Fatalf("ReviseTestingContract() error = %v", err)
	}
	revisedContractYAML, err := yaml.Marshal(revisedContract)
	if err != nil {
		t.Fatalf("yaml.Marshal(revisedContract) error = %v", err)
	}

	exitCode := 0
	report := BuildContractVerificationReportStub(&contract, contractPath)
	for i := range report.Results {
		report.Results[i].Status = VerificationStatusPassed
		report.Results[i].Evidence = "ok"
		report.Results[i].EvidenceData = VerificationEvidence{ExitCode: &exitCode, Summary: "ok"}
	}
	reportYAML, err := yaml.Marshal(report)
	if err != nil {
		t.Fatalf("yaml.Marshal(report) error = %v", err)
	}

	writeContractCmd := "cat > \"" + contractPath + "\" <<'CONTRACT_EOF'\n" + string(revisedContractYAML) + "CONTRACT_EOF"
	writeReportCmd := "_d=\"\"; for d in \"" + artifactDir + "\"/iteration-*; do _d=\"$d\"; done; cat > \"$_d/verification-report.yaml\" <<'REPORT_EOF'\n" + string(reportYAML) + "REPORT_EOF"
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			writeContractCmd+"\n"+
			writeReportCmd+"\n"+
			// Custom verification-report is written above; pair with a
			// valid progress.md so the harness handoff parser passes.
			testutil.WriteImplementProgressMd(artifactDir, "SUCCESS")+"\n"+
			testutil.TouchPhaseComplete(artifactDir)+"\n"+
			testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	store := feature.NewStore(stateRoot)
	_ = store.Save(f)
	buildSession, captured := capturingBuildSession(agentScript, reviewScript)
	cfg := ImplementConfig{
		Feature:                    f,
		FeatureStore:               store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		MaxIterations:              1,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               "Relevant tests pass",
		Model:                      "opus",
		ReviewModel:                "reviewer",
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
		SkipIterationReview:        false,
		PhaseType:                  "tdd-fill-in",
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != "max_iterations" {
		t.Fatalf("RunImplementationLoop() FinalStatus = %s, want max_iterations", result.FinalStatus)
	}
	if len(*captured) != 1 {
		t.Fatalf("BuildSession() calls = %d, want 1 when gate rejects before review", len(*captured))
	}

	feedbackBytes, err := os.ReadFile(filepath.Join(artifactDir, "iteration-01", "review-feedback.md"))
	if err != nil {
		t.Fatalf("os.ReadFile(review-feedback.md) error = %v", err)
	}
	feedback := string(feedbackBytes)
	if !strings.Contains(feedback, "contract revision 1") || !strings.Contains(feedback, "revision 2") {
		t.Fatalf("review-feedback.md missing stale revision guidance:\n%s", feedback)
	}
}

// TestImplementLoop_SkillReadInstruction verifies that RunImplementationLoop:
// - Sets the system prompt to the completion protocol (contains "phase_complete")
// - Prepends the skill-read instruction path to the user prompt
func TestImplementLoop_SkillReadInstruction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateDir := filepath.Join(tmpDir, "state", "test-skill-impl-001")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	skillsDir := filepath.Join(tmpDir, "skills")
	for _, d := range []string{workDir, artifactDir, stateDir, scriptsDir, skillsDir} {
		os.MkdirAll(d, 0o755)
	}

	iterDir := filepath.Join(artifactDir, "iteration-01")

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+
			testutil.JSONLSuccess+"\n")
	_ = iterDir

	buildSession, captured := capturingBuildSession(agentScript, "")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	f := newTestFeature(t, workDir)

	planPath := filepath.Join(artifactDir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# Plan\nImplement something"), 0o644)

	store := feature.NewStore(filepath.Join(tmpDir, "state"))
	_ = store.Save(f)

	cfg := ImplementConfig{
		Feature:                    f,
		FeatureStore:               store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		MaxIterations:              1,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               "Relevant tests pass",
		Model:                      "agent",
		ReviewModel:                "reviewer",
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
		SkillsDir:                  skillsDir,
		SkipIterationReview:        true,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}

	if len(*captured) == 0 {
		t.Fatal("expected at least one BuildSession call")
	}

	opts := (*captured)[0]

	// System prompt should contain completion protocol (has "phase_complete")
	if !strings.Contains(opts.SystemPrompt, "phase_complete") {
		t.Error("systemPrompt missing completion protocol marker 'phase_complete'")
	}

	// The RoleSpec-backed system prompt owns the primary skill directive.
	expectedSkillPath := filepath.Join(skillsDir, "implement", "SKILL.md")
	if !strings.Contains(opts.SystemPrompt, expectedSkillPath) {
		t.Errorf("system prompt missing skill-read instruction, expected path %q in system prompt", expectedSkillPath)
	}
	if strings.Contains(opts.Prompt, expectedSkillPath) {
		t.Errorf("user prompt should not contain RoleSpec-owned skill path %q", expectedSkillPath)
	}
}
