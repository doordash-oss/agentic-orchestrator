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
	"slices"
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

func TestRunImplementationLoop_GrantsOnlyActiveRunState(t *testing.T) {
	tmpDir := t.TempDir()
	featureStateDir := filepath.Join(tmpDir, "state", "feature-a")
	sealedRunDir := filepath.Join(featureStateDir, "runs", "run-004")
	activeRunDir := filepath.Join(featureStateDir, "runs", "run-005")
	artifactDir := filepath.Join(activeRunDir, "phase-01", "implement")
	repoDir := filepath.Join(tmpDir, "repo")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, dir := range []string{sealedRunDir, artifactDir, repoDir, scriptsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", dir, err)
		}
	}
	planPath := filepath.Join(activeRunDir, "phase-01", "plan", "phase-plan.md")
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(plan dir) error = %v", err)
	}
	if err := os.WriteFile(planPath, []byte("# Plan\nImplement something\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plan) error = %v", err)
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+
			testutil.JSONLSuccess+"\n")
	buildSession, captured := capturingBuildSession(agentScript, "")
	f := &feature.Feature{
		ID:        "feature-a",
		ActiveRun: 5,
		RunCount:  5,
		Repos:     []feature.FeatureRepo{{Name: "repo-a", Path: repoDir}},
	}
	sm := session.NewManager(make(chan interface{}, 100))
	defer sm.Shutdown()

	result, err := RunImplementationLoop(ImplementConfig{
		Feature:                    f,
		WorkDir:                    activeRunDir,
		PlanPath:                   planPath,
		MaxIterations:              1,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               "Implementation completes",
		Model:                      "implementer",
		ArtifactDir:                artifactDir,
		StateDir:                   featureStateDir,
		AdditionalDirs:             []string{repoDir},
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
		SkipIterationReview:        true,
	}, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, finalStatusReviewPassed)
	}
	if len(*captured) == 0 {
		t.Fatal("BuildSession was not called")
	}
	grants := (*captured)[0].AdditionalDirs
	for _, want := range []string{activeRunDir, repoDir} {
		if !slices.Contains(grants, want) {
			t.Fatalf("AdditionalDirs = %v, missing grant %q", grants, want)
		}
	}
	for _, forbidden := range []string{featureStateDir, sealedRunDir} {
		if slices.Contains(grants, forbidden) {
			t.Fatalf("AdditionalDirs = %v, must not grant predecessor scope %q", grants, forbidden)
		}
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

func TestBuildImplementPromptIncludesPlanRevisionFeedback(t *testing.T) {
	planDir := t.TempDir()
	planPath := filepath.Join(planDir, "phase-plan.md")
	if err := os.WriteFile(planPath, []byte("# Phase plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	feedback := MissingEvidencePlanRevisionFeedback([]MissingEvidenceRequirement{{
		Phase:       1,
		Kind:        testingContractVisualSource,
		Requirement: "Capture actual rendered README output.",
	}})
	if err := WritePlanAttemptMeta(planDir, PlanAttemptMeta{
		Attempt:      2,
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: ReviewChangesRequested.String(),
	}); err != nil {
		t.Fatalf("write plan attempt meta: %v", err)
	}
	feedbackPath := filepath.Join(planDir, "attempt-02", "validation-feedback.md")
	if err := os.WriteFile(feedbackPath, []byte(feedback), 0o644); err != nil {
		t.Fatalf("write validation feedback: %v", err)
	}
	if err := WritePlanAttemptMeta(planDir, PlanAttemptMeta{
		Attempt:      3,
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: ReviewApproved.String(),
	}); err != nil {
		t.Fatalf("write approved plan attempt meta: %v", err)
	}

	prompt := BuildImplementPrompt(
		planPath,
		"Relevant tests pass",
		"/tmp/progress.md",
		"/tmp/verification-report.yaml",
		"/tmp/testing-contract.yaml",
		"",
		"",
		"",
		"",
		"",
		nil,
		3,
	)

	for _, want := range []string{
		"Approved plan revision context",
		"MISSING_EVIDENCE_REQUIREMENT phase 1 visual: Capture actual rendered README output.",
		"Previous progress.md may predate this approved plan revision",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildImplementPromptSkipsStalePlanValidatorFeedback(t *testing.T) {
	planDir := t.TempDir()
	planPath := filepath.Join(planDir, "phase-plan.md")
	if err := os.WriteFile(planPath, []byte("# Phase plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	feedback := "# Multi-Validator Plan Review\n\n" +
		"**Overall: CHANGES_REQUESTED** (1/2 validators approved)\n\n" +
		"## Structural Validator -- ERROR\n\n" +
		"running Structural validation session: protocol violation: validation-structural-feedback.md: review-feedback.md missing required section \"## Suggestions\"\n"
	if err := WritePlanAttemptMeta(planDir, PlanAttemptMeta{
		Attempt:      1,
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: ReviewChangesRequested.String(),
	}); err != nil {
		t.Fatalf("write rejected plan attempt meta: %v", err)
	}
	feedbackPath := filepath.Join(planDir, "attempt-01", "validation-feedback.md")
	if err := os.WriteFile(feedbackPath, []byte(feedback), 0o644); err != nil {
		t.Fatalf("write validation feedback: %v", err)
	}
	if err := WritePlanAttemptMeta(planDir, PlanAttemptMeta{
		Attempt:      2,
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: ReviewApproved.String(),
	}); err != nil {
		t.Fatalf("write approved plan attempt meta: %v", err)
	}

	prompt := BuildImplementPrompt(
		planPath,
		"Relevant tests pass",
		"/tmp/progress.md",
		"/tmp/verification-report.yaml",
		"/tmp/testing-contract.yaml",
		"",
		"",
		"",
		"",
		"",
		nil,
		2,
	)

	for _, unexpected := range []string{
		"Approved plan revision context",
		"Multi-Validator Plan Review",
		"review-feedback.md missing required section",
	} {
		if strings.Contains(prompt, unexpected) {
			t.Fatalf("prompt included stale validator feedback %q:\n%s", unexpected, prompt)
		}
	}
}

func TestFrontendImplementationRequiresFrontendDesignAndKeepsVisualReferences(t *testing.T) {
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
		ID:                  "test-feat-001",
		Name:                "Test Feature",
		Slug:                "test-feature",
		Description:         "Frontend feature",
		Images:              []string{"/tmp/mockup.png"},
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 1,
		ActiveRun:           1,
		RunCount:            1,
		SchemaVersion:       feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "test-repo", Path: workDir},
		},
	}
	f.SetRoadmapPhaseFrontend(1, true)

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
		SkillsDir:                  filepath.Join(tmpDir, "skills"),
		DangerouslySkipPermissions: true,
		BuildSession:               buildSession,
	}, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("RunImplementationLoop() FinalStatus = %s, want review_passed", result.FinalStatus)
	}
	if len(*captured) < 2 {
		t.Fatalf("captured %d BuildSession calls, want implement and review", len(*captured))
	}

	t.Logf("frontend implement prompt:\n%s", (*captured)[0].Prompt)

	implementSystemPrompt := (*captured)[0].SystemPrompt
	for _, want := range []string{
		"## Required Skills",
		"frontend-design",
		filepath.Join(tmpDir, "skills", "frontend-design", "SKILL.md"),
		"mandatory",
	} {
		if !strings.Contains(implementSystemPrompt, want) {
			t.Errorf("implement system prompt missing required frontend skill guidance %q:\n%s", want, implementSystemPrompt)
		}
	}

	capturedPrompts := []struct {
		name   string
		prompt string
	}{
		{name: "implement", prompt: (*captured)[0].Prompt},
	}
	for i, opts := range (*captured)[1:] {
		capturedPrompts = append(capturedPrompts, struct {
			name   string
			prompt string
		}{
			name:   fmt.Sprintf("review %d", i+1),
			prompt: opts.Prompt,
		})
	}

	for _, got := range capturedPrompts {
		if !strings.Contains(got.prompt, "## Visual References") {
			t.Errorf("%s prompt missing visual references:\n%s", got.name, got.prompt)
		}
		if !strings.Contains(got.prompt, "/tmp/mockup.png") {
			t.Errorf("%s prompt missing attached image path:\n%s", got.name, got.prompt)
		}
		linePaddedPrompt := "\n" + got.prompt + "\n"
		if strings.Contains(linePaddedPrompt, "\n## Visual Evidence\n") {
			t.Errorf("%s prompt unexpectedly contains visual evidence guidance:\n%s", got.name, got.prompt)
		}
		if strings.Contains(linePaddedPrompt, "\n## Behavioral Evidence\n") {
			t.Errorf("%s prompt unexpectedly contains behavioral evidence guidance:\n%s", got.name, got.prompt)
		}
	}
}

func TestLoopResultTypes(t *testing.T) {
	result := &LoopResult{
		FinalStatus: finalStatusReviewPassed,
		Iterations:  5,
	}
	if result.FinalStatus != finalStatusReviewPassed {
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
		sess.SendStatus(agentStatusSuccess)
		sess.CloseDone()

		got := waitForStatus(sess, nil, "")
		if got != agentStatusSuccess {
			t.Errorf("waitForStatus() = %q, want SUCCESS", got)
		}
	})

	t.Run("done_with_pending_SUCCESS_ready_false_returns_MISSING_PHASE_COMPLETE", func(t *testing.T) {
		for i := 0; i < 200; i++ {
			sess := session.NewSession("test", "feat", feature.PhaseImplement)
			sess.SetCost(newEndedAfterTextResult())
			sess.SendStatus(agentStatusSuccess)
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

	t.Run("done_before_status_ready_true_returns_SUCCESS", func(t *testing.T) {
		sess := session.NewSession("test", "feat", feature.PhaseImplement)
		sess.CloseDone()

		got := waitForStatus(sess, nil, "", func() bool { return true })
		if got != agentStatusSuccess {
			t.Errorf("waitForStatus() = %q, want SUCCESS", got)
		}
	})

	t.Run("done_with_pending_FAILED_ready_true_returns_SUCCESS", func(t *testing.T) {
		sess := session.NewSession("test", "feat", feature.PhaseImplement)
		sess.SendStatus(agentStatusFailed)
		sess.CloseDone()

		got := waitForStatus(sess, nil, "", func() bool { return true })
		if got != agentStatusSuccess {
			t.Errorf("waitForStatus() = %q, want SUCCESS", got)
		}
	})

	t.Run("done_with_pending_FAILED_ready_false_returns_FAILED", func(t *testing.T) {
		sess := session.NewSession("test", "feat", feature.PhaseImplement)
		sess.SendStatus(agentStatusFailed)
		sess.CloseDone()

		got := waitForStatus(sess, nil, "", func() bool { return false })
		if got != agentStatusFailed {
			t.Errorf("waitForStatus() = %q, want FAILED", got)
		}
	})

	t.Run("done_with_pending_API_ERROR_ready_true_returns_SUCCESS", func(t *testing.T) {
		sess := session.NewSession("test", "feat", feature.PhaseImplement)
		sess.SendStatus("API_ERROR")
		sess.CloseDone()

		got := waitForStatus(sess, nil, "", func() bool { return true })
		if got != agentStatusSuccess {
			t.Errorf("waitForStatus() = %q, want SUCCESS", got)
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
			sess.SendStatus(agentStatusSuccess)
		}()

		got := waitForStatus(sess, nil, "")
		if got != agentStatusSuccess {
			t.Errorf("waitForStatus() = %q, want SUCCESS", got)
		}
	})

	t.Run("API_ERROR_then_SUCCESS", func(t *testing.T) {
		sess := session.NewSession("test", "feat", feature.PhaseImplement)
		go func() {
			sess.SendStatus("API_ERROR")
			sess.SendStatus(agentStatusSuccess)
		}()

		got := waitForStatus(sess, nil, "")
		if got != agentStatusSuccess {
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
				if result.FinalStatus != finalStatusReviewPassed {
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
	if result.FinalStatus != finalStatusReviewPassed {
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
	return &captureSink{done: make(chan struct{}, 1)}
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

// waitForWrite blocks until the sink sees a write or the timeout elapses. The
// sink retains one pending notification so callers do not race when the write
// lands immediately after the triggering action.
func (c *captureSink) waitForWrite(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for stdin write")
	}
}

func TestCaptureSinkWaitForWriteObservesAlreadyCompletedWrite(t *testing.T) {
	sink := newCaptureSink()
	if _, err := sink.Write([]byte("already written")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	sink.waitForWrite(t, 10*time.Millisecond)
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
	return &llm.ResultMessage{Subtype: testResultSuccessValue, StopReason: "tool_use"}
}

// newEndedAfterTextResult builds a ResultMessage that the classifier will
// treat as TermEndedAfterText (deliberate stop).
func newEndedAfterTextResult() *llm.ResultMessage {
	return &llm.ResultMessage{Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn}
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
			testutil.WriteImplementHandoffFiles(t, artifactDir, iterDir, agentStatusSuccess)
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
	if result.FinalStatus != finalStatusReviewPassed {
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

// crashResumeTestSession is a dead-process fake: it reports a terminal FAILED
// status and exposes a provider-native session ID that a crash-resume attempt
// can pass back via BuildSessionOpts.ResumeSessionID.
type crashResumeTestSession struct {
	*utilityTestSession
	providerSessionID string
}

func (s *crashResumeTestSession) SessionID() string { return s.providerSessionID }

func newCrashResumeLoopConfig(t *testing.T, artifactDir, stateDir, workDir, observeDir, featureID string) ImplementConfig {
	t.Helper()
	planPath := filepath.Join(artifactDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\nDo the thing.\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Crash Resume",
		Slug:          "crash-resume",
		Description:   "crash resume loop coverage",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		TraceID:       "trace-crash-resume",
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         []feature.FeatureRepo{{Name: "agentic", Path: workDir}},
	}
	return ImplementConfig{
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
		Observer:            observe.New(true, observeDir, false, "", false, "agentic"),
		SkipIterationReview: true,
	}
}

func crashResumeTestDirs(t *testing.T, featureID string) (artifactDir, stateDir, workDir, observeDir string) {
	t.Helper()
	tmpDir := t.TempDir()
	artifactDir = filepath.Join(tmpDir, "artifacts")
	stateDir = filepath.Join(tmpDir, "state")
	workDir = filepath.Join(tmpDir, "work")
	observeDir = filepath.Join(tmpDir, "observe")
	for _, dir := range []string{artifactDir, stateDir, workDir, filepath.Join(observeDir, featureID)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return artifactDir, stateDir, workDir, observeDir
}

func TestImplementLoop_CrashResumeRecoversDeadSession(t *testing.T) {
	featureID := "crash-resume-001"
	artifactDir, stateDir, workDir, observeDir := crashResumeTestDirs(t, featureID)
	cfg := newCrashResumeLoopConfig(t, artifactDir, stateDir, workDir, observeDir, featureID)
	cfg.Feature.ActiveTimingKey = "implement"
	store := feature.NewStore(filepath.Join(filepath.Dir(stateDir), "feature-store"))
	if err := store.Save(cfg.Feature); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	cfg.FeatureStore = store

	var buildOpts []BuildSessionOpts
	cfg.BuildSession = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		buildOpts = append(buildOpts, opts)
		return []string{"mock-agent"}, nil, &session.SessionOpts{
			SupportsSessionResume: true,
			ProviderName:          "codex",
			ResolvedModel:         "gpt-5.6-codex",
		}, nil
	}
	var resumedEvents []ports.FeatureResumedData
	cfg.OnFeatureResumed = func(input ports.FeatureResumedData) {
		resumedEvents = append(resumedEvents, input)
	}

	var startIDs []string
	cfg.SessionStartFunc = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (session.SessionHandle, error) {
		startIDs = append(startIDs, id)
		if len(startIDs) == 1 {
			// First process dies mid-turn without writing the marker.
			dead := &crashResumeTestSession{utilityTestSession: newUtilityTestSession(), providerSessionID: "native-123"}
			dead.result = &llm.ResultMessage{TotalCostUSD: 1}
			dead.statusCh <- agentStatusFailed
			return dead, nil
		}
		// The resumed process finishes the iteration.
		iterDir := filepath.Join(artifactDir, "iteration-01")
		testutil.WriteImplementHandoffFiles(t, artifactDir, iterDir, agentStatusSuccess)
		if err := os.WriteFile(filepath.Join(iterDir, "phase_complete"), []byte("complete\n"), 0o644); err != nil {
			t.Fatalf("write phase_complete: %v", err)
		}
		resumed := newUtilityTestSession()
		resumed.result = &llm.ResultMessage{TotalCostUSD: 2}
		resumed.statusCh <- agentStatusSuccess
		return resumed, nil
	}

	result, err := RunImplementationLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}

	if len(buildOpts) != 2 {
		t.Fatalf("BuildSession calls = %d, want 2", len(buildOpts))
	}
	if buildOpts[0].ResumeSessionID != "" {
		t.Errorf("first BuildSession ResumeSessionID = %q, want empty", buildOpts[0].ResumeSessionID)
	}
	if buildOpts[1].ResumeSessionID != "native-123" {
		t.Errorf("resume BuildSession ResumeSessionID = %q, want native-123", buildOpts[1].ResumeSessionID)
	}
	if !strings.Contains(buildOpts[1].Prompt, crashResumeMessageFragment) {
		t.Errorf("resume prompt = %q, want crash-resume message", buildOpts[1].Prompt)
	}
	wantResumePrompt := renderResumePrompt(implementResumeContext)
	if buildOpts[1].Prompt != wantResumePrompt {
		t.Errorf("resume prompt = %q, want byte-identical %q", buildOpts[1].Prompt, wantResumePrompt)
	}
	if len(startIDs) != 2 || !strings.HasSuffix(startIDs[1], "-resume") {
		t.Errorf("start session IDs = %v, want second with -resume suffix", startIDs)
	}

	meta, err := NewArtifactManager(artifactDir).ReadMeta(filepath.Join(artifactDir, "iteration-01"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if meta.AgentStatus != agentStatusSuccess {
		t.Fatalf("AgentStatus = %q, want %q", meta.AgentStatus, agentStatusSuccess)
	}
	if meta.Provider != "codex" ||
		meta.ResolvedModel != "gpt-5.6-codex" ||
		meta.ProviderSessionID != "native-123" ||
		!meta.Resumed ||
		meta.ResumeCount != 1 {
		t.Errorf("resume meta = %#v, want mirrored provider identity and one resume", meta)
	}

	record, err := ReadResumeRecord(filepath.Join(artifactDir, "iteration-01"))
	if err != nil {
		t.Fatalf("ReadResumeRecord() error = %v", err)
	}
	if record == nil {
		t.Fatal("ReadResumeRecord() = nil, want retained record")
	}
	if record.ProviderSessionID != "native-123" ||
		record.Provider != "codex" ||
		record.ResolvedModel != "gpt-5.6-codex" ||
		record.PhaseKey != "implement" ||
		record.Iteration != 1 ||
		record.RunNumber != 1 ||
		record.OrchestratorSessionID != startIDs[0] ||
		!record.Resumed ||
		record.ResumeCount != 1 ||
		!record.Completed ||
		record.CompletedAt == nil {
		t.Errorf("resume record = %#v, want completed resumed identity", record)
	}
	if len(resumedEvents) != 1 {
		t.Fatalf("FeatureResumed callbacks = %d, want 1", len(resumedEvents))
	}
	if got := resumedEvents[0]; got.FeatureID != featureID ||
		got.PhaseKey != "implement" ||
		got.Iteration != 1 ||
		got.RunNumber != 1 ||
		got.ResumeCount != 1 {
		t.Errorf("FeatureResumed callback = %#v, want complete resume identity", got)
	}
	reloaded, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("load feature costs: %v", err)
	}
	if len(reloaded.SessionCosts) != 2 {
		t.Fatalf("session cost records = %#v, want dead and resumed process", reloaded.SessionCosts)
	}
	if reloaded.SessionCosts[0].SessionID != startIDs[0] ||
		reloaded.SessionCosts[1].SessionID != startIDs[1] ||
		!strings.HasSuffix(reloaded.SessionCosts[1].SessionID, "-resume") {
		t.Errorf("session cost IDs = (%q, %q), want base and -resume suffix",
			reloaded.SessionCosts[0].SessionID, reloaded.SessionCosts[1].SessionID)
	}

	events := readObserveEvents(t, observeDir, featureID)
	if got := len(filterEventsByType(events, "session.ended")); got != 2 {
		t.Errorf("session.ended events = %d, want 2 (dead session + resumed session)", got)
	}
}

func TestImplementLoop_ManualResumeConsumesPendingIntent(t *testing.T) {
	featureID := "manual-resume-001"
	artifactDir, stateDir, workDir, observeDir := crashResumeTestDirs(t, featureID)
	cfg := newCrashResumeLoopConfig(t, artifactDir, stateDir, workDir, observeDir, featureID)
	cfg.Feature.ActiveTimingKey = "implement"
	cfg.Feature.CurrentIteration = 1

	iterDir := filepath.Join(artifactDir, "iteration-01")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatalf("mkdir iteration: %v", err)
	}
	if err := NewArtifactManager(artifactDir).WriteMeta(iterDir, IterationMeta{
		Iteration:   1,
		AgentStatus: agentStatusFailed,
	}); err != nil {
		t.Fatalf("WriteMeta() error = %v", err)
	}
	if err := WriteResumeRecord(iterDir, ResumeRecord{
		ProviderSessionID:     "native-manual-123",
		Provider:              "codex",
		ResolvedModel:         "gpt-5.6-codex",
		PhaseKey:              "implement",
		Iteration:             1,
		RunNumber:             1,
		OrchestratorSessionID: "manual-resume-001-impl-01",
		PendingResume:         true,
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}

	var buildOpts []BuildSessionOpts
	cfg.BuildSession = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		buildOpts = append(buildOpts, opts)
		return []string{"mock-agent"}, nil, &session.SessionOpts{
			SupportsSessionResume: true,
			ProviderName:          "codex",
			ResolvedModel:         "gpt-5.6-codex",
		}, nil
	}
	var resumedEvents []ports.FeatureResumedData
	cfg.OnFeatureResumed = func(input ports.FeatureResumedData) {
		resumedEvents = append(resumedEvents, input)
	}
	cfg.SessionStartFunc = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (session.SessionHandle, error) {
		testutil.WriteImplementHandoffFiles(t, artifactDir, iterDir, agentStatusSuccess)
		if err := os.WriteFile(filepath.Join(iterDir, PhaseCompleteFile), []byte("complete\n"), 0o644); err != nil {
			t.Fatalf("write phase_complete: %v", err)
		}
		resumed := &crashResumeTestSession{
			utilityTestSession: newUtilityTestSession(),
			providerSessionID:  "native-manual-123",
		}
		resumed.statusCh <- agentStatusSuccess
		return resumed, nil
	}

	result, err := RunImplementationLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, finalStatusReviewPassed)
	}
	if len(buildOpts) != 1 || buildOpts[0].ResumeSessionID != "native-manual-123" {
		t.Fatalf("BuildSession opts = %#v, want one manual provider continuation", buildOpts)
	}
	if buildOpts[0].Prompt != renderResumePrompt(implementResumeContext) {
		t.Errorf("manual resume prompt = %q, want shared resume prompt", buildOpts[0].Prompt)
	}
	record, err := ReadResumeRecord(iterDir)
	if err != nil {
		t.Fatalf("ReadResumeRecord() error = %v", err)
	}
	if record == nil || record.PendingResume || !record.Resumed || record.ResumeCount != 1 || !record.Completed {
		t.Errorf("resume record = %#v, want completed one-resume record with cleared intent", record)
	}
	if len(resumedEvents) != 1 || resumedEvents[0].ResumeCount != 1 {
		t.Errorf("FeatureResumed callbacks = %#v, want exactly one resumed event", resumedEvents)
	}
}

func TestImplementLoop_ManualResumeRejectsExpiredProviderSession(t *testing.T) {
	tests := []struct {
		provider string
		detail   string
	}{
		{provider: "claude", detail: "no conversation found for session"},
		{provider: "codex", detail: "thread/resume JSON-RPC error: thread not found"},
		{provider: "opencode", detail: "session/load failed: session expired"},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			featureID := "manual-rejection-" + test.provider
			artifactDir, stateDir, workDir, observeDir := crashResumeTestDirs(t, featureID)
			cfg := newCrashResumeLoopConfig(t, artifactDir, stateDir, workDir, observeDir, featureID)
			cfg.Feature.ActiveTimingKey = "implement"
			cfg.Feature.CurrentIteration = 1

			iterDir := filepath.Join(artifactDir, "iteration-01")
			if err := os.MkdirAll(iterDir, 0o755); err != nil {
				t.Fatalf("mkdir iteration: %v", err)
			}
			if err := NewArtifactManager(artifactDir).WriteMeta(iterDir, IterationMeta{
				Iteration:   1,
				AgentStatus: agentStatusFailed,
			}); err != nil {
				t.Fatalf("WriteMeta() error = %v", err)
			}
			if err := WriteResumeRecord(iterDir, ResumeRecord{
				ProviderSessionID:     "expired-provider-session",
				Provider:              test.provider,
				ResolvedModel:         "model",
				PhaseKey:              "implement",
				Iteration:             1,
				RunNumber:             1,
				OrchestratorSessionID: featureID + "-impl-01",
				PendingResume:         true,
				CreatedAt:             time.Now(),
				UpdatedAt:             time.Now(),
			}); err != nil {
				t.Fatalf("WriteResumeRecord() error = %v", err)
			}

			cfg.BuildSession = func(BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
				return []string{"mock-agent"}, nil, &session.SessionOpts{
					SupportsSessionResume: true,
					ProviderName:          test.provider,
					ResolvedModel:         "model",
				}, nil
			}
			cfg.SessionStartFunc = func(string, string, feature.Phase, []string, string, []string, ...*session.SessionOpts) (session.SessionHandle, error) {
				return nil, errors.New(test.detail)
			}
			resumedEvents := 0
			cfg.OnFeatureResumed = func(ports.FeatureResumedData) {
				resumedEvents++
			}

			result, err := RunImplementationLoop(cfg, nil)
			if err != nil {
				t.Fatalf("RunImplementationLoop() error = %v", err)
			}
			if result.FinalStatus != "failed" || !strings.Contains(result.LastError, "not found or has expired") {
				t.Fatalf("result = %#v, want informative resume rejection failure", result)
			}
			record, err := ReadResumeRecord(iterDir)
			if err != nil {
				t.Fatalf("ReadResumeRecord() error = %v", err)
			}
			if record == nil || !record.Rejected || record.PendingResume || record.RejectedAt == nil {
				t.Errorf("resume record = %#v, want rejected stamp and cleared intent", record)
			}
			if resumedEvents != 0 {
				t.Errorf("FeatureResumed callbacks = %d, want 0", resumedEvents)
			}
		})
	}
}

func TestImplementLoop_CrashResumeNotAttemptedWithoutCapability(t *testing.T) {
	featureID := "crash-resume-002"
	artifactDir, stateDir, workDir, observeDir := crashResumeTestDirs(t, featureID)
	cfg := newCrashResumeLoopConfig(t, artifactDir, stateDir, workDir, observeDir, featureID)

	buildCalls := 0
	resumedEvents := 0
	cfg.OnFeatureResumed = func(ports.FeatureResumedData) {
		resumedEvents++
	}
	cfg.BuildSession = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		buildCalls++
		return []string{"mock-agent"}, nil, &session.SessionOpts{SupportsSessionResume: false}, nil
	}
	cfg.SessionStartFunc = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (session.SessionHandle, error) {
		dead := &crashResumeTestSession{utilityTestSession: newUtilityTestSession(), providerSessionID: "native-123"}
		dead.statusCh <- agentStatusFailed
		return dead, nil
	}

	if _, err := RunImplementationLoop(cfg, nil); err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if buildCalls != 1 {
		t.Fatalf("BuildSession calls = %d, want 1 (no resume without capability)", buildCalls)
	}
	if resumedEvents != 0 {
		t.Errorf("FeatureResumed callbacks = %d, want 0", resumedEvents)
	}
	meta, err := NewArtifactManager(artifactDir).ReadMeta(filepath.Join(artifactDir, "iteration-01"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if meta.AgentStatus != agentStatusFailed {
		t.Fatalf("AgentStatus = %q, want FAILED", meta.AgentStatus)
	}
}

func TestImplementLoop_CrashResumeAttemptedOncePerIteration(t *testing.T) {
	featureID := "crash-resume-003"
	artifactDir, stateDir, workDir, observeDir := crashResumeTestDirs(t, featureID)
	cfg := newCrashResumeLoopConfig(t, artifactDir, stateDir, workDir, observeDir, featureID)

	buildCalls := 0
	cfg.BuildSession = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		buildCalls++
		return []string{"mock-agent"}, nil, &session.SessionOpts{SupportsSessionResume: true}, nil
	}
	cfg.SessionStartFunc = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (session.SessionHandle, error) {
		// Every process dies; the loop must resume at most once.
		dead := &crashResumeTestSession{utilityTestSession: newUtilityTestSession(), providerSessionID: "native-123"}
		dead.statusCh <- agentStatusFailed
		return dead, nil
	}

	if _, err := RunImplementationLoop(cfg, nil); err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if buildCalls != 2 {
		t.Fatalf("BuildSession calls = %d, want 2 (initial + one resume)", buildCalls)
	}
	meta, err := NewArtifactManager(artifactDir).ReadMeta(filepath.Join(artifactDir, "iteration-01"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if meta.AgentStatus != agentStatusFailed {
		t.Fatalf("AgentStatus = %q, want FAILED", meta.AgentStatus)
	}
}

func TestImplementLoop_CrashResumeNotAttemptedWithoutProviderSessionID(t *testing.T) {
	featureID := "crash-resume-empty-provider-session"
	artifactDir, stateDir, workDir, observeDir := crashResumeTestDirs(t, featureID)
	cfg := newCrashResumeLoopConfig(t, artifactDir, stateDir, workDir, observeDir, featureID)

	buildCalls := 0
	resumedEvents := 0
	cfg.BuildSession = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		buildCalls++
		return []string{"mock-agent"}, nil, &session.SessionOpts{
			SupportsSessionResume: true,
			ProviderName:          "codex",
			ResolvedModel:         "gpt-5.6-codex",
		}, nil
	}
	cfg.OnFeatureResumed = func(ports.FeatureResumedData) {
		resumedEvents++
	}
	cfg.SessionStartFunc = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (session.SessionHandle, error) {
		dead := &crashResumeTestSession{utilityTestSession: newUtilityTestSession()}
		dead.statusCh <- agentStatusFailed
		return dead, nil
	}

	if _, err := RunImplementationLoop(cfg, nil); err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if buildCalls != 1 {
		t.Fatalf("BuildSession calls = %d, want 1", buildCalls)
	}
	if resumedEvents != 0 {
		t.Errorf("FeatureResumed callbacks = %d, want 0", resumedEvents)
	}
	record, err := ReadResumeRecord(filepath.Join(artifactDir, "iteration-01"))
	if err != nil {
		t.Fatalf("ReadResumeRecord() error = %v", err)
	}
	if record == nil {
		t.Fatal("ReadResumeRecord() = nil, want coherent record without provider session ID")
	}
	if record.ProviderSessionID != "" || record.Provider != "codex" || record.ResolvedModel != "gpt-5.6-codex" {
		t.Errorf("resume record = %#v, want provider/model with empty native session ID", record)
	}
}

func TestImplementLoop_CrashResumeNotAttemptedWithPhaseComplete(t *testing.T) {
	featureID := "crash-resume-complete"
	artifactDir, stateDir, workDir, observeDir := crashResumeTestDirs(t, featureID)
	cfg := newCrashResumeLoopConfig(t, artifactDir, stateDir, workDir, observeDir, featureID)

	buildCalls := 0
	resumedEvents := 0
	cfg.BuildSession = func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		buildCalls++
		return []string{"mock-agent"}, nil, &session.SessionOpts{SupportsSessionResume: true}, nil
	}
	cfg.OnFeatureResumed = func(ports.FeatureResumedData) {
		resumedEvents++
	}
	cfg.SessionStartFunc = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (session.SessionHandle, error) {
		iterDir := filepath.Join(artifactDir, "iteration-01")
		testutil.WriteImplementHandoffFiles(t, artifactDir, iterDir, agentStatusSuccess)
		if err := os.WriteFile(filepath.Join(iterDir, PhaseCompleteFile), []byte("complete\n"), 0o644); err != nil {
			t.Fatalf("write phase_complete: %v", err)
		}
		dead := &crashResumeTestSession{utilityTestSession: newUtilityTestSession(), providerSessionID: "native-123"}
		dead.statusCh <- agentStatusFailed
		return dead, nil
	}

	result, err := RunImplementationLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, finalStatusReviewPassed)
	}
	if buildCalls != 1 {
		t.Fatalf("BuildSession calls = %d, want 1", buildCalls)
	}
	if resumedEvents != 0 {
		t.Errorf("FeatureResumed callbacks = %d, want 0", resumedEvents)
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
	sess.SendStatus(agentStatusSuccess)

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
	sess.SendStatus(agentStatusSuccess)

	select {
	case got := <-done:
		if got != agentStatusSuccess {
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
		sess.SendStatus(agentStatusSuccess)
		sink.waitForWrite(t, 2*time.Second)
	}

	if got := countContinuationMessages(sink.contents()); got != maxAutoResumeAttempts {
		t.Errorf("continuation messages = %d, want %d", got, maxAutoResumeAttempts)
	}

	// One more truncated SUCCESS beyond the cap — this one must return a
	// missing-marker status instead of sending another continuation.
	sess.SetCost(newTruncatedResult())
	sess.SendStatus(agentStatusSuccess)

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
	sess.SendStatus(agentStatusSuccess)
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
	sess.SendStatus(agentStatusSuccess)

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
	sess.SendStatus(agentStatusSuccess)
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
	sess.SendStatus(agentStatusSuccess)

	select {
	case got := <-done:
		if got != agentStatusSuccess {
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
		sess.SendStatus(agentStatusSuccess)
		sink.waitForWrite(t, 2*time.Second)
	}
	if got := countNudgeMessages(sink.contents()); got != maxFinishOrViolateNudges {
		t.Errorf("nudge messages = %d, want %d", got, maxFinishOrViolateNudges)
	}

	// One more end_turn beyond the cap — must escalate, no extra nudge.
	sess.SetCost(newEndedAfterTextResult())
	sess.SendStatus(agentStatusSuccess)

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
	sess.SendStatus(agentStatusSuccess)

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
	sess.SendStatus(agentStatusSuccess)

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
	sess.SendStatus(agentStatusSuccess)

	select {
	case got := <-done:
		if got != agentStatusSuccess {
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

	sess.SendStatus(agentStatusSuccess)
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
	sess.SendStatus(agentStatusSuccess)

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
	sess.SendStatus(agentStatusSuccess)
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

	sess.SendStatus(agentStatusSuccess)
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

	sess.SendStatus(agentStatusSuccess)
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

	sess.SendStatus(agentStatusSuccess)
	select {
	case got := <-done:
		if got != agentStatusSuccess {
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
	sess.SendStatus(agentStatusSuccess)

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
		CommandRunner:              NewExecCommandRunner(),
		SkipIterationReview:        true,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
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
// is false, the loop runs the per-axis review gate after SUCCESS.
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
		ID:              "test-noskip-001",
		Name:            "Test No Skip Review",
		Slug:            "test-no-skip-review",
		Description:     "No skip iteration review test",
		Status:          feature.StatusImplementing,
		CurrentPhase:    feature.PhaseImplement,
		ActiveTimingKey: "implement",
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
	if result.FinalStatus != finalStatusReviewPassed {
		t.Errorf("expected FinalStatus=review_passed, got %s", result.FinalStatus)
	}

	// BuildSession should have been called once for implementation and once
	// for each selected implementation-review axis.
	if len(*captured) != 4 {
		t.Errorf("expected BuildSession called 4 times (impl + 3 review axes), got %d", len(*captured))
	}
	assertExplicitEmptyAgentNames(t, (*captured)[0].AgentNames)
	assertExplorationAgentNames(t, (*captured)[1].AgentNames)

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.PhaseCost("implement"); got != 0.004 {
		t.Errorf("PhaseCost(implement) = %v, want 0.004", got)
	}
	if len(updated.SessionCosts) != 4 {
		t.Fatalf("len(SessionCosts) = %d, want 4", len(updated.SessionCosts))
	}
	implCost := updated.SessionCosts[0]
	if implCost.SessionID != "test-noskip-001-impl-01" || implCost.PhaseKey != "implement" || implCost.ObserverPhase != "implement" || implCost.CostUSD != 0.001 {
		t.Errorf("SessionCosts[0] = %+v, want implementation session cost under implement", implCost)
	}
	for i, reviewCost := range updated.SessionCosts[1:] {
		if !strings.Contains(reviewCost.SessionID, "implementation-review-") || reviewCost.PhaseKey != "implement" || reviewCost.ObserverPhase != "review" || reviewCost.CostUSD != 0.001 {
			t.Errorf("SessionCosts[%d] = %+v, want implementation review axis session cost under implement", i+1, reviewCost)
		}
	}
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
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	iterDir := filepath.Join(artifactDir, "iteration-01")
	for _, path := range []string{
		filepath.Join(iterDir, "review", "craft", "phase_complete"),
		filepath.Join(iterDir, "review", "craft", "review-feedback.md"),
		filepath.Join(iterDir, "review", "functionality-evidence", "phase_complete"),
		filepath.Join(iterDir, "review", "functionality-evidence", "review-feedback.md"),
		filepath.Join(iterDir, "review", "cleanliness", "phase_complete"),
		filepath.Join(iterDir, "review", "cleanliness", "review-feedback.md"),
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
	if result.FinalStatus != BoundedHelperStatusProtocolViolation {
		t.Fatalf("FinalStatus = %q, want protocol_violation (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if !strings.Contains(result.LastError, "implementation_review_") || !strings.Contains(result.LastError, "phase_complete") {
		t.Fatalf("LastError = %q, want implementation-review axis phase_complete violation", result.LastError)
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
	if _, err := os.Stat(filepath.Join(artifactDir, "iteration-02", "review", "craft", "review-feedback.md")); err != nil {
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
	if !strings.Contains(string(parentFeedback), ReviewChangesRequested.String()) || strings.Contains(string(parentFeedback), "\n"+ReviewApproved.String()) {
		t.Fatalf("parent review feedback did not override approved verdict:\n%s", parentFeedback)
	}
}

// mixedFailureCrash and mixedFailureDrift are the mixedFailureScript sequence
// keywords that make the mock agent CLI emit, respectively, an error line
// (simulating an agent crash) or a protocol-violation progress.md (drift).
const (
	mixedFailureCrash = "crash"
	mixedFailureDrift = "drift"
)

func TestImplementLoopMixedFailuresTripIterationDeterminesFinalStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tests := []struct {
		name       string
		sequence   []string
		wantStatus string
	}{
		{name: "contract violation trips", sequence: []string{mixedFailureDrift, mixedFailureCrash, mixedFailureDrift}, wantStatus: BoundedHelperStatusProtocolViolation},
		{name: "agent crash trips", sequence: []string{mixedFailureCrash, mixedFailureDrift, mixedFailureCrash}, wantStatus: "safety_rail"},
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
		case mixedFailureDrift:
			b.WriteString(testutil.WriteImplementProtocolViolation(artifactDir))
			b.WriteString("\n")
			b.WriteString(testutil.JSONLSuccess)
		case mixedFailureCrash:
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

func TestImplementLoopReviewInfraFailureParksForReviewOnlyResume(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateRoot := filepath.Join(tmpDir, "state")
	stateDir := filepath.Join(stateRoot, "test-review-park")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, dir := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewExecCommandRunner()
	runVerificationTestCommand(t, runner, workDir, "git init -q")
	runVerificationTestCommand(t, runner, workDir, "git config user.email test@example.com")
	runVerificationTestCommand(t, runner, workDir, "git config user.name Test")
	runVerificationTestCommand(t, runner, workDir, "git commit --allow-empty -qm base")

	planPath := filepath.Join(artifactDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("### Automated Verification\n- [ ] Check passes: `printf verified`\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.JSONLError("Unable to connect to API (ENOTFOUND)")+"\n")
	f := &feature.Feature{
		ID: "test-review-park", Name: "Review Park", Slug: "review-park",
		Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement, CurrentRoadmapPhase: 1,
		Repos: []feature.FeatureRepo{{Name: "repo", Path: workDir, WorktreePath: workDir}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	buildSession, captured := capturingBuildSession(agentScript, reviewScript)
	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := ImplementConfig{
		Feature: f, FeatureStore: store, WorkDir: workDir, PlanPath: planPath,
		MaxIterations: 3, MaxConsecFails: 3, MaxConsecNoProgress: 3,
		Model: "opus", ReviewModel: "reviewer", ArtifactDir: artifactDir, StateDir: stateDir,
		DangerouslySkipPermissions: true, BuildSession: buildSession, CommandRunner: runner,
		SkipIterationReview: false, PhaseType: "collapsed",
	}
	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != "review_error" {
		t.Fatalf("result = %+v, want review_error park when no axis produced a verdict", result)
	}
	iterDir := filepath.Join(artifactDir, "iteration-01")
	if !HasPhaseComplete(iterDir) {
		t.Fatal("phase_complete missing; review-only resume would re-run the implementer")
	}
	if _, statErr := os.Stat(filepath.Join(iterDir, "meta.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("meta.yaml written for an unreviewed iteration; resume would advance to iteration 2: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(artifactDir, "iteration-02")); !os.IsNotExist(statErr) {
		t.Fatal("iteration-02 exists; infra failure consumed an implementer iteration")
	}
	implementerSessions := len(*captured)
	axes := implementationReviewAxesForGate(implementationReviewGatePerPhase, implementationReviewAxisSelection{Profile: f.EffectivePipeline()})
	if implementerSessions != 1+len(axes) {
		t.Fatalf("BuildSession calls = %d, want one implementer + %d review axes", implementerSessions, len(axes))
	}

	// Network restored: the same review script now approves. Resume must run
	// review-only for the same iteration, reuse the already-generated
	// verification report instead of re-executing the contract, and skip
	// axes that already produced a complete verdict.
	contractPath, ok := resolveImplementationContractPath(filepath.Dir(cfg.StateDir), f, cfg.RepoName)
	if !ok {
		t.Fatal("no contract path resolved")
	}
	runsBefore := countContractEvidenceRuns(t, contractPath)
	cachedAxis := axes[0]
	cachedAxisDir := filepath.Join(iterDir, "review", implementationReviewAxisSlug(cachedAxis.Name))
	if err := os.MkdirAll(cachedAxisDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cachedFeedback := FormatStructuredReviewFeedback(cachedAxis.Name+" Implementation Review", "- (none)", "- (none)", ReviewApproved)
	if err := os.WriteFile(filepath.Join(cachedAxisDir, "review-feedback.md"), []byte(cachedFeedback), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cachedAxisDir, PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	resumed, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("resumed RunImplementationLoop() error = %v", err)
	}
	if resumed.FinalStatus != finalStatusReviewPassed || resumed.Iterations != 1 {
		t.Fatalf("resumed result = %+v, want iteration-1 review_passed", resumed)
	}
	if got := len(*captured); got != implementerSessions+len(axes)-1 {
		t.Fatalf("BuildSession calls after resume = %d, want %d (uncached review axes only)", got, implementerSessions+len(axes)-1)
	}
	if runsAfter := countContractEvidenceRuns(t, contractPath); runsAfter != runsBefore {
		t.Fatalf("evidence runs after resume = %d, want %d (verification report must be reused, not re-executed)", runsAfter, runsBefore)
	}
}

func countContractEvidenceRuns(t *testing.T, contractPath string) int {
	t.Helper()
	root := filepath.Join(filepath.Dir(contractPath), "verification-evidence")
	runs := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && strings.HasPrefix(d.Name(), "run-") {
			runs++
		}
		return nil
	})
	return runs
}

func TestPrepareImplementationTestingContractSkipsNonMoonshotRoadmapPhases(t *testing.T) {
	tmpDir := t.TempDir()
	stateRoot := filepath.Join(tmpDir, "state")
	stateDir := filepath.Join(stateRoot, "test-large-roadmap")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &feature.Feature{
		ID: "test-large-roadmap", Name: "Large Roadmap", Slug: "large-roadmap",
		Pipeline: feature.PipelineLarge, CurrentRoadmapPhase: 1,
	}
	cfg := ImplementConfig{Feature: f, StateDir: stateDir}
	planContent := "### Automated Verification\n- [ ] Check passes: `printf verified`\n"

	path, fingerprint, err := prepareImplementationTestingContract(cfg, planContent)
	if err != nil {
		t.Fatalf("prepareImplementationTestingContract: %v", err)
	}
	if path != "" || fingerprint != "" {
		t.Fatalf("expected no contract for large-profile roadmap phase, got path=%q fingerprint=%q", path, fingerprint)
	}
	if _, statErr := os.Stat(PhaseTestingContractPath(filepath.Dir(cfg.StateDir), cfg.Feature, 1)); !os.IsNotExist(statErr) {
		t.Fatalf("testing-contract.yaml must not be written for large-profile roadmap phase")
	}
}

func TestPrepareImplementationTestingContractMoonshotRoadmapPhaseStillWritesContract(t *testing.T) {
	tmpDir := t.TempDir()
	stateRoot := filepath.Join(tmpDir, "state")
	stateDir := filepath.Join(stateRoot, "test-moonshot-roadmap")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &feature.Feature{
		ID: "test-moonshot-roadmap", Name: "Moonshot Roadmap", Slug: "moonshot-roadmap",
		Pipeline: feature.PipelineMoonshot, CurrentRoadmapPhase: 1,
		Repos: []feature.FeatureRepo{{Name: "test-repo", Path: stateDir, WorktreePath: stateDir}},
	}
	runner := NewExecCommandRunner()
	runVerificationTestCommand(t, runner, stateDir, "git init -q")
	runVerificationTestCommand(t, runner, stateDir, "git config user.email test@example.com")
	runVerificationTestCommand(t, runner, stateDir, "git config user.name Test")
	if err := os.WriteFile(filepath.Join(stateDir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runVerificationTestCommand(t, runner, stateDir, "git add README.md")
	runVerificationTestCommand(t, runner, stateDir, "git commit -qm base")
	cfg := ImplementConfig{Feature: f, StateDir: stateDir, CommandRunner: runner}
	planContent := "#### Automated Verification:\n- [ ] Check passes: `printf verified`\n"

	path, fingerprint, err := prepareImplementationTestingContract(cfg, planContent)
	if err != nil {
		t.Fatalf("prepareImplementationTestingContract: %v", err)
	}
	if path == "" || fingerprint == "" {
		t.Fatalf("expected a contract for moonshot roadmap phase, got path=%q fingerprint=%q", path, fingerprint)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("expected testing-contract.yaml at %s: %v", path, statErr)
	}
	contract, err := ReadTestingContract(path)
	if err != nil {
		t.Fatalf("ReadTestingContract: %v", err)
	}
	if len(contract.Items) == 0 {
		t.Fatalf("expected written contract to have at least one item, got none")
	}
}

func TestPrepareImplementationTestingContractNonMoonshotRoadmapRemovesStaleContract(t *testing.T) {
	tmpDir := t.TempDir()
	stateRoot := filepath.Join(tmpDir, "state")
	stateDir := filepath.Join(stateRoot, "test-large-stale")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &feature.Feature{
		ID: "test-large-stale", Name: "Large Stale", Slug: "large-stale",
		Pipeline: feature.PipelineLarge, CurrentRoadmapPhase: 1,
	}
	cfg := ImplementConfig{Feature: f, StateDir: stateDir}
	contractPath := PhaseTestingContractPath(filepath.Dir(cfg.StateDir), cfg.Feature, 1)
	if err := os.MkdirAll(filepath.Dir(contractPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteTestingContract(contractPath, compileImplementationTestingContract(cfg, "### Automated Verification\n- [ ] Check passes: `printf verified`\n")); err != nil {
		t.Fatalf("seeding stale contract: %v", err)
	}

	path, fingerprint, err := prepareImplementationTestingContract(cfg, "### Automated Verification\n- [ ] Check passes: `printf verified`\n")
	if err != nil {
		t.Fatalf("prepareImplementationTestingContract: %v", err)
	}
	if path != "" || fingerprint != "" {
		t.Fatalf("expected no contract for large-profile roadmap phase, got path=%q fingerprint=%q", path, fingerprint)
	}
	if _, statErr := os.Stat(contractPath); !os.IsNotExist(statErr) {
		t.Fatalf("stale testing-contract.yaml must be removed, stat err = %v", statErr)
	}
}

func TestPrepareImplementationTestingContractLargeCycleStillWritesContract(t *testing.T) {
	tmpDir := t.TempDir()
	stateRoot := filepath.Join(tmpDir, "state")
	stateDir := filepath.Join(stateRoot, "test-large-cycle")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &feature.Feature{
		ID: "test-large-cycle", Name: "Large Cycle", Slug: "large-cycle",
		Pipeline: feature.PipelineLarge,
	}
	f.SetActiveCycleType(feature.CycleReviewComments)
	cfg := ImplementConfig{Feature: f, StateDir: stateDir, CommandRunner: NewExecCommandRunner()}
	planContent := "### Automated Verification\n- [ ] Check passes: `printf verified`\n"

	path, fingerprint, err := prepareImplementationTestingContract(cfg, planContent)
	if err != nil {
		t.Fatalf("prepareImplementationTestingContract: %v", err)
	}
	if path == "" || fingerprint == "" {
		t.Fatalf("expected a cycle contract for large-profile repo cycle, got path=%q fingerprint=%q", path, fingerprint)
	}
	wantPath := CycleTestingContractPath(filepath.Dir(cfg.StateDir), f, cfg.RepoName, feature.CycleReviewComments)
	if path != wantPath {
		t.Fatalf("path = %q, want cycle contract path %q", path, wantPath)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("expected testing-contract.yaml at %s: %v", path, statErr)
	}
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
			{Name: "test-repo", Path: workDir, WorktreePath: workDir},
		},
	}

	store := feature.NewStore(stateRoot)
	_ = store.Save(f)
	runner := NewExecCommandRunner()
	runVerificationTestCommand(t, runner, workDir, "git init -q")
	runVerificationTestCommand(t, runner, workDir, "git config user.email test@example.com")
	runVerificationTestCommand(t, runner, workDir, "git config user.name Test")
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runVerificationTestCommand(t, runner, workDir, "git add README.md")
	runVerificationTestCommand(t, runner, workDir, "git commit -qm base")

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
		CommandRunner:              runner,
		SkipIterationReview:        true,
		PhaseType:                  "tracer-bullet",
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
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
			testutil.WriteImplementProgressMd(artifactDir, agentStatusSuccess)+"\n"+
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
		ReviewChangesRequested.String(),
		"caveat", // hedge phrase from the agent's own evidence
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(strings.ToLower(feedback), strings.ToLower(s)) {
			t.Errorf("review-feedback.md missing %q; got:\n%s", s, feedback)
		}
	}
}

func TestImplementLoopHarnessCapabilityPauseKeepsSameIteration(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateRoot := filepath.Join(tmpDir, "state")
	stateDir := filepath.Join(stateRoot, "test-capability-pause")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, dir := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewExecCommandRunner()
	runVerificationTestCommand(t, runner, workDir, "git init -q")
	runVerificationTestCommand(t, runner, workDir, "git config user.email test@example.com")
	runVerificationTestCommand(t, runner, workDir, "git config user.name Test")
	runVerificationTestCommand(t, runner, workDir, "git commit --allow-empty -qm base")

	planPath := filepath.Join(artifactDir, "plan.md")
	plan := "### Automated Verification\n- [ ] Protected [agentico capability: Okta session; probe: exit 1]: `printf protected`\n"
	if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	f := &feature.Feature{
		ID: "test-capability-pause", Name: "Capability Pause", Slug: "capability-pause",
		Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement, CurrentRoadmapPhase: 1,
		Repos: []feature.FeatureRepo{{Name: "repo", Path: workDir, WorktreePath: workDir}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	buildSession, captured := capturingBuildSession(agentScript, reviewScript)
	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := ImplementConfig{
		Feature: f, FeatureStore: store, WorkDir: workDir, PlanPath: planPath,
		MaxIterations: 1, MaxConsecFails: 3, MaxConsecNoProgress: 3,
		Model: "opus", ReviewModel: "reviewer", ArtifactDir: artifactDir, StateDir: stateDir,
		DangerouslySkipPermissions: true, BuildSession: buildSession, CommandRunner: runner,
		SkipIterationReview: false, PhaseType: "collapsed",
	}
	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != "need_user_input" || result.Iterations != 1 {
		t.Fatalf("result = %+v, want same-iteration need_user_input", result)
	}
	if len(*captured) != 1 {
		t.Fatalf("BuildSession calls = %d, want implementer only (no reviewer)", len(*captured))
	}
	iterDir := filepath.Join(artifactDir, "iteration-01")
	if _, err := os.Stat(filepath.Join(iterDir, "meta.yaml")); !os.IsNotExist(err) {
		t.Fatalf("meta.yaml exists after harness pause; resume would consume an iteration: %v", err)
	}
	rec, err := ReadNeedUserInputRecord(result.NeedUserInputPath)
	if err != nil {
		t.Fatal(err)
	}
	if rec.VerificationDecision == nil || len(rec.VerificationDecision.ItemIDs) != 1 {
		t.Fatalf("gate record = %+v, want structured verification decision", rec)
	}
	rec.Questions[0].Answer = NeedUserVerificationWaive
	if err := ApplyNeedUserVerificationDecision(rec); err != nil {
		t.Fatalf("ApplyNeedUserVerificationDecision() error = %v", err)
	}
	resumed, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("resumed RunImplementationLoop() error = %v", err)
	}
	if resumed.FinalStatus != finalStatusReviewPassed || resumed.Iterations != 1 {
		t.Fatalf("resumed result = %+v, want iteration-1 review_passed", resumed)
	}
	axes := implementationReviewAxesForGate(implementationReviewGatePerPhase, implementationReviewAxisSelection{Profile: f.EffectivePipeline()})
	if len(*captured) != 1+len(axes) {
		t.Fatalf("BuildSession calls after resume = %d, want one implementer + %d review axes (no second implementer)", len(*captured), len(axes))
	}
}

func TestImplementLoopHarnessContractErrorRoutesPlanRevisionKeepsSameIteration(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateRoot := filepath.Join(tmpDir, "state")
	stateDir := filepath.Join(stateRoot, "test-contract-revision")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, dir := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewExecCommandRunner()
	runVerificationTestCommand(t, runner, workDir, "git init -q")
	runVerificationTestCommand(t, runner, workDir, "git config user.email test@example.com")
	runVerificationTestCommand(t, runner, workDir, "git config user.name Test")
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("translated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runVerificationTestCommand(t, runner, workDir, "git add README.md")
	runVerificationTestCommand(t, runner, workDir, "git commit -qm base")

	planPath := filepath.Join(artifactDir, "plan.md")
	badPlan := "### Automated Verification\n- [ ] Stable waived check: `printf stable`\n- [ ] README translated: `grep -q translated repo/README.md`\n"
	if err := os.WriteFile(planPath, []byte(badPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	f := &feature.Feature{
		ID: "test-contract-revision", Name: "Contract Revision", Slug: "contract-revision",
		Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement, CurrentRoadmapPhase: 1,
		Repos: []feature.FeatureRepo{{Name: "repo", Path: workDir, WorktreePath: workDir}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	buildSession, captured := capturingBuildSession(agentScript, reviewScript)
	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := ImplementConfig{
		Feature: f, FeatureStore: store, WorkDir: workDir, PlanPath: planPath,
		MaxIterations: 1, MaxConsecFails: 3, MaxConsecNoProgress: 3,
		Model: "opus", ReviewModel: "reviewer", ArtifactDir: artifactDir, StateDir: stateDir,
		DangerouslySkipPermissions: true, BuildSession: buildSession, CommandRunner: runner,
		SkipIterationReview: false, PhaseType: "collapsed",
	}
	contractPath, ok := resolveImplementationContractPath(filepath.Dir(cfg.StateDir), cfg.Feature, cfg.RepoName)
	if !ok {
		t.Fatal("resolveImplementationContractPath() returned ok=false")
	}
	seededContract := compileImplementationTestingContract(cfg, badPlan)
	for i := range seededContract.Items {
		if seededContract.Items[i].Command == "printf stable" {
			seededContract.Items[i].Disposition = TestingContractItemDisposition{
				Status: TestingContractDispositionWaived, Reason: "user-approved stable exception", ChangedBy: "user",
			}
		}
	}
	if err := WriteTestingContract(contractPath, seededContract); err != nil {
		t.Fatalf("seed testing contract: %v", err)
	}
	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != "plan_revision_required" || result.Iterations != 1 {
		t.Fatalf("result = %+v, want same-iteration plan_revision_required", result)
	}
	if !strings.Contains(result.PlanRevisionFeedback, "repo/README.md") || !strings.Contains(result.PlanRevisionFeedback, "README.md") {
		t.Fatalf("PlanRevisionFeedback = %q, want bad path and correction", result.PlanRevisionFeedback)
	}
	if len(*captured) != 1 {
		t.Fatalf("BuildSession calls = %d, want implementer only (no reviewer)", len(*captured))
	}
	iterDir := filepath.Join(artifactDir, "iteration-01")
	if _, err := os.Stat(filepath.Join(iterDir, "meta.yaml")); !os.IsNotExist(err) {
		t.Fatalf("meta.yaml exists after contract repair route; resume would consume an iteration: %v", err)
	}

	goodPlan := "### Automated Verification\n- [ ] Stable waived check: `printf stable`\n- [ ] README translated: `grep -q translated README.md`\n"
	if err := os.WriteFile(planPath, []byte(goodPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	resumed, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("resumed RunImplementationLoop() error = %v", err)
	}
	if resumed.FinalStatus != finalStatusReviewPassed || resumed.Iterations != 1 {
		t.Fatalf("resumed result = %+v, want iteration-1 review_passed", resumed)
	}
	axes := implementationReviewAxesForGate(implementationReviewGatePerPhase, implementationReviewAxisSelection{Profile: f.EffectivePipeline()})
	if len(*captured) != 1+len(axes) {
		t.Fatalf("BuildSession calls after resume = %d, want original implementer plus %d review axes only", len(*captured), len(axes))
	}
	reconciledContract, err := ReadTestingContract(contractPath)
	if err != nil {
		t.Fatalf("ReadTestingContract() error = %v", err)
	}
	waiverPreserved := false
	for _, item := range reconciledContract.Items {
		if item.Command == "printf stable" {
			waiverPreserved = IsTestingContractItemWaived(item)
		}
	}
	if !waiverPreserved {
		t.Fatal("stable user waiver was lost while repairing another verification command")
	}
}

func TestImplementLoopUnscopedMultiRepoVerificationRoutesPlanRevisionBeforeImplementer(t *testing.T) {
	tmpDir := t.TempDir()
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateRoot := filepath.Join(tmpDir, "state")
	stateDir := filepath.Join(stateRoot, "unscoped-multi")
	for _, dir := range []string{artifactDir, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plan := strings.Join([]string{
		"## Tasks",
		"### Task 1: API", "**Repo:** api",
		"### Task 2: Web", "**Repo:** web",
		"## Success Criteria",
		"### Automated Verification",
		"- [ ] Tests pass: `make test`",
	}, "\n")
	planPath := filepath.Join(tmpDir, "phase-plan.md")
	if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &feature.Feature{
		ID: "unscoped-multi", Name: "Unscoped Multi", Status: feature.StatusImplementing,
		Repos: []feature.FeatureRepo{{Name: "api", Path: t.TempDir()}, {Name: "web", Path: t.TempDir()}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	buildCalls := 0
	result, err := RunImplementationLoop(ImplementConfig{
		Feature: f, FeatureStore: store, PlanPath: planPath, ArtifactDir: artifactDir, StateDir: stateDir,
		MaxIterations: 1, MaxConsecFails: 3, MaxConsecNoProgress: 3,
		BuildSession: func(BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			buildCalls++
			return nil, nil, nil, errors.New("implementer must not start")
		},
	}, nil)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus != "plan_revision_required" || !strings.Contains(result.PlanRevisionFeedback, "[repo: <name>]") {
		t.Fatalf("result = %+v, want scoped plan revision", result)
	}
	if buildCalls != 0 {
		t.Fatalf("BuildSession calls = %d, want plan repaired before implementer starts", buildCalls)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "iteration-01")); !os.IsNotExist(err) {
		t.Fatalf("iteration directory exists before plan repair: %v", err)
	}
}

func TestImplementLoopSkipReviewRoutesHarnessRegressionToRetry(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	artifactDir := filepath.Join(tmpDir, "artifacts")
	stateRoot := filepath.Join(tmpDir, "state")
	stateDir := filepath.Join(stateRoot, "test-harness-regression")
	scriptsDir := filepath.Join(tmpDir, "scripts")
	for _, dir := range []string{workDir, artifactDir, stateDir, scriptsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewExecCommandRunner()
	runVerificationTestCommand(t, runner, workDir, "git init -q")
	runVerificationTestCommand(t, runner, workDir, "git config user.email test@example.com")
	runVerificationTestCommand(t, runner, workDir, "git config user.name Test")
	if err := os.WriteFile(filepath.Join(workDir, "check.sh"), []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runVerificationTestCommand(t, runner, workDir, "git add check.sh")
	runVerificationTestCommand(t, runner, workDir, "git commit -qm base")
	// The uncommitted change regresses the check: candidate fails while the
	// anchored base commit still passes.
	if err := os.WriteFile(filepath.Join(workDir, "check.sh"), []byte("#!/bin/sh\necho regressed >&2\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	planPath := filepath.Join(artifactDir, "plan.md")
	plan := "### Automated Verification\n- [ ] Check passes: `./check.sh`\n"
	if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	f := &feature.Feature{
		ID: "test-harness-regression", Name: "Harness Regression", Slug: "harness-regression",
		Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement, CurrentRoadmapPhase: 1,
		Repos: []feature.FeatureRepo{{Name: "repo", Path: workDir, WorktreePath: workDir}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	buildSession, _ := capturingBuildSession(agentScript, reviewScript)
	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := ImplementConfig{
		Feature: f, FeatureStore: store, WorkDir: workDir, PlanPath: planPath,
		MaxIterations: 1, MaxConsecFails: 3, MaxConsecNoProgress: 3,
		Model: "opus", ReviewModel: "reviewer", ArtifactDir: artifactDir, StateDir: stateDir,
		DangerouslySkipPermissions: true, BuildSession: buildSession, CommandRunner: runner,
		SkipIterationReview: true, PhaseType: "collapsed",
	}
	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop() error = %v", err)
	}
	if result.FinalStatus == finalStatusReviewPassed {
		t.Fatalf("result = %+v, want harness-detected regression to block review_passed", result)
	}
	meta, err := os.ReadFile(filepath.Join(artifactDir, "iteration-01", "meta.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), ReviewChangesRequested.String()) {
		t.Fatalf("iteration meta = %s, want review status %q", meta, ReviewChangesRequested.String())
	}
}

func TestImplementLoop_RejectsImplementerContractMutation(t *testing.T) {
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
			{Name: "test-repo", Path: workDir, WorktreePath: workDir},
		},
	}
	runner := NewExecCommandRunner()
	runVerificationTestCommand(t, runner, workDir, "git init -q")
	runVerificationTestCommand(t, runner, workDir, "git config user.email test@example.com")
	runVerificationTestCommand(t, runner, workDir, "git config user.name Test")
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runVerificationTestCommand(t, runner, workDir, "git add README.md")
	runVerificationTestCommand(t, runner, workDir, "git commit -qm base")

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
			testutil.WriteImplementProgressMd(artifactDir, agentStatusSuccess)+"\n"+
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
		CommandRunner:              runner,
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
	if !strings.Contains(feedback, "testing-contract.yaml") || !strings.Contains(feedback, "harness-owned") {
		t.Fatalf("review-feedback.md missing harness-owned contract guidance:\n%s", feedback)
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
	if result.FinalStatus != finalStatusReviewPassed {
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

// bgTaskSession is a SessionHandle double whose live-background-task count and
// stdout-activity timestamp are test-controlled, mirroring a Claude session
// that spawned background Task subagents.
type bgTaskSession struct {
	*utilityTestSession
	liveTasks    atomic.Int32
	lastStdoutNs atomic.Int64
	userMessages chan string
	stopped      atomic.Bool
}

func newBgTaskSession() *bgTaskSession {
	s := &bgTaskSession{
		utilityTestSession: newUtilityTestSession(),
		userMessages:       make(chan string, 4),
	}
	s.lastStdoutNs.Store(time.Now().UnixNano())
	return s
}

func (s *bgTaskSession) LiveBackgroundTaskCount() int { return int(s.liveTasks.Load()) }
func (s *bgTaskSession) LastStdoutAt() time.Time      { return time.Unix(0, s.lastStdoutNs.Load()) }
func (s *bgTaskSession) SendUserMessage(text string) error {
	s.userMessages <- text
	return nil
}
func (s *bgTaskSession) Stop() error {
	s.stopped.Store(true)
	return nil
}

func withBackgroundTaskPollInterval(t *testing.T, d time.Duration) {
	t.Helper()
	prev := backgroundTaskPollInterval
	backgroundTaskPollInterval = d
	t.Cleanup(func() { backgroundTaskPollInterval = prev })
}

func TestWaitForStatus_BackgroundTasks(t *testing.T) {
	t.Run("end_turn with live tasks keeps waiting without nudge or stop", func(t *testing.T) {
		withBackgroundTaskPollInterval(t, 5*time.Millisecond)

		sess := newBgTaskSession()
		sess.result = newEndedAfterTextResult()
		sess.liveTasks.Store(3)
		sess.lastStdoutNs.Store(time.Now().UnixNano())

		var ready atomic.Bool
		done := make(chan string, 1)
		go func() {
			done <- waitForStatus(sess, nil, "", func() bool { return ready.Load() })
		}()

		sess.statusCh <- agentStatusSuccess

		select {
		case got := <-done:
			t.Fatalf("waitForStatus() returned %q; want it to keep waiting on background tasks", got)
		case msg := <-sess.userMessages:
			t.Fatalf("unexpected user message %q while background tasks run", msg)
		case <-time.After(100 * time.Millisecond):
		}
		if sess.stopped.Load() {
			t.Fatal("session was stopped while background tasks were running")
		}

		// Background tasks finish; the CLI re-invokes the agent, which
		// completes normally.
		sess.liveTasks.Store(0)
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
		select {
		case msg := <-sess.userMessages:
			t.Fatalf("unexpected user message %q; completion needed no nudge", msg)
		default:
		}
	})

	t.Run("tasks finish quietly without re-invocation triggers auto-resume", func(t *testing.T) {
		withBackgroundTaskPollInterval(t, 5*time.Millisecond)

		sess := newBgTaskSession()
		sess.result = newEndedAfterTextResult()
		sess.liveTasks.Store(1)
		sess.lastStdoutNs.Store(time.Now().UnixNano())

		var ready atomic.Bool
		done := make(chan string, 1)
		go func() {
			done <- waitForStatus(sess, nil, "", func() bool { return ready.Load() })
		}()

		sess.statusCh <- agentStatusSuccess

		// Give the waiter a moment to defer, then finish the tasks with a
		// stale stdout stamp: the CLI never re-invoked the agent.
		time.Sleep(20 * time.Millisecond)
		sess.lastStdoutNs.Store(time.Now().Add(-time.Hour).UnixNano())
		sess.liveTasks.Store(0)

		select {
		case msg := <-sess.userMessages:
			if !strings.Contains(msg, "Continue where you left off") {
				t.Fatalf("SendUserMessage() = %q, want auto-resume message", msg)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for auto-resume after tasks finished")
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

	t.Run("silent live tasks wait indefinitely until terminal", func(t *testing.T) {
		withBackgroundTaskPollInterval(t, 5*time.Millisecond)

		sess := newBgTaskSession()
		sess.result = newEndedAfterTextResult()
		sess.liveTasks.Store(1)
		sess.lastStdoutNs.Store(time.Now().Add(-time.Hour).UnixNano())

		var ready atomic.Bool
		done := make(chan string, 1)
		go func() {
			done <- waitForStatus(sess, nil, "", func() bool { return ready.Load() })
		}()

		sess.statusCh <- agentStatusSuccess

		select {
		case got := <-done:
			t.Fatalf("waitForStatus() returned %q while provider still declared a live task", got)
		case msg := <-sess.userMessages:
			t.Fatalf("unexpected user message %q while provider still declared a live task", msg)
		case <-time.After(100 * time.Millisecond):
		}
		if sess.stopped.Load() {
			t.Fatal("session was stopped while provider still declared a live task")
		}

		sess.liveTasks.Store(0)
		select {
		case msg := <-sess.userMessages:
			if !strings.Contains(msg, "Continue where you left off") {
				t.Fatalf("SendUserMessage() = %q, want auto-resume after terminal task", msg)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for auto-resume after terminal task")
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

	t.Run("dead session with stale live tasks does not hang", func(t *testing.T) {
		sess := newBgTaskSession()
		sess.result = newEndedAfterTextResult()
		sess.liveTasks.Store(2)
		sess.statusCh <- agentStatusSuccess
		close(sess.done)

		done := make(chan string, 1)
		go func() {
			done <- waitForStatus(sess, nil, "", func() bool { return false })
		}()

		select {
		case got := <-done:
			if got != agentStatusMissingMarker {
				t.Fatalf("waitForStatus() = %q, want %q", got, agentStatusMissingMarker)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("waitForStatus() hung on a dead session with stale background tasks")
		}
	})
}

func TestRetryReviewFeedbackReminder(t *testing.T) {
	dir := t.TempDir()
	if got := retryReviewFeedbackReminder(dir, 0); got != "" {
		t.Errorf("retryReviewFeedbackReminder(dir, 0) = %q, want empty", got)
	}
	if got := retryReviewFeedbackReminder(dir, 2); got != "" {
		t.Errorf("retryReviewFeedbackReminder without feedback file = %q, want empty", got)
	}

	iterDir := filepath.Join(dir, "iteration-02")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(iterDir, "review-feedback.md")
	if err := os.WriteFile(path, []byte("findings"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := retryReviewFeedbackReminder(dir, 2)
	if !strings.Contains(got, path) {
		t.Errorf("retryReviewFeedbackReminder = %q, want it to reference %s", got, path)
	}
	if strings.Contains(got, "findings") {
		t.Errorf("retryReviewFeedbackReminder = %q, want a pointer without the feedback body", got)
	}
}

// TestVerificationContextCancelsOnInterruptedFeature pins the P3 fix: a
// verification run's context must cancel once the feature is interrupted, so
// a hung command cannot outlive a user interrupt by its full timeout.
func TestVerificationContextCancelsOnInterruptedFeature(t *testing.T) {
	t.Parallel()
	store := feature.NewStore(filepath.Join(t.TempDir(), "state"))
	f := &feature.Feature{
		ID:            "verify-ctx-001",
		Name:          "Verify Ctx",
		Slug:          "verify-ctx",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	ctx, cancel := verificationContext(store, f.ID)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("context cancelled before interruption")
	default:
	}

	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusInterrupted
		return nil
	}); err != nil {
		t.Fatalf("interrupt feature: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("context not cancelled within 10s of feature interruption")
	}
}
