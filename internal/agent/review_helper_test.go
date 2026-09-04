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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestRunReadOnlyReviewHelper_UsesBoundedHelperArtifactHandler(t *testing.T) {
	root := t.TempDir()
	helperDir := filepath.Join(root, "review")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		t.Fatalf("mkdir helper dir: %v", err)
	}
	workDir := filepath.Join(root, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	script := testutil.WriteScript(t, root, "reviewer.sh", testutil.WriteReviewApprovedInDir(helperDir)+`
`+testutil.JSONLInit+`
`+testutil.JSONLSuccess)

	var got BuildSessionOpts
	pr := &PhaseRunner{
		StateDir:       root,
		SessionManager: session.NewManager(make(chan interface{}, 10)),
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			got = opts
			return []string{"bash", script}, nil, &ports.SessionOpts{
				PermHandler:       opts.PermHandler,
				DebugSystemPrompt: opts.SystemPrompt,
				ProviderName:      testMockIdentifier,
			}, nil
		},
	}
	t.Cleanup(func() { pr.SessionManager.Shutdown() })

	_, err := pr.RunReadOnlyReviewHelper(context.Background(), ReviewHelperConfig{
		SessionID:              "review-helper-1",
		FeatureID:              "feature-1",
		Phase:                  feature.PhaseReview,
		Model:                  "test-model",
		Prompt:                 "review the diff",
		PromptPath:             filepath.Join(helperDir, "review-prompt.md"),
		ResponsePath:           filepath.Join(helperDir, "review-output.txt"),
		FeedbackPath:           filepath.Join(helperDir, "review-feedback.md"),
		HelperIterDir:          helperDir,
		Role:                   RoleImplementationReviewCraft,
		WorkDir:                workDir,
		LogPath:                filepath.Join(helperDir, "review-output.txt"),
		SystemPromptPrefix:     "review",
		CompletionAskingClause: "Ask at most one blocking question.",
		EffortLevel:            llm.EffortMedium,
	})
	if err != nil {
		t.Fatalf("RunReadOnlyReviewHelper() error = %v", err)
	}
	if !permissionHandlerIncludesBoundedArtifacts(got.PermHandler) {
		t.Fatalf("PermHandler = %T, want bounded artifact handler", got.PermHandler)
	}
	if !strings.Contains(got.SystemPrompt, helperDir) {
		t.Fatalf("SystemPrompt missing helper dir %q:\n%s", helperDir, got.SystemPrompt)
	}
	if !strings.Contains(got.SystemPrompt, "Ask at most one blocking question.") {
		t.Fatalf("helper without a registry discarded its injected asking clause:\n%s", got.SystemPrompt)
	}
	if !strings.Contains(got.SystemPrompt, "The harness owns the durable completion receipt") {
		t.Fatalf("SystemPrompt missing harness-owned receipt rule:\n%s", got.SystemPrompt)
	}
}

func TestRunLiveRunReviewHelper_ConfiguresScratchRootsEnvAndPermissions(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	helperDir := filepath.Join(stateDir, "review", "iteration-01", "qa")
	workDir := filepath.Join(root, "worktrees", "repo")
	for _, d := range []string{helperDir, workDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	feedbackPath := filepath.Join(helperDir, "review-feedback.md")
	evidenceRoot := filepath.Join(helperDir, "evidence")
	buildCacheRoot := filepath.Join(helperDir, "build-cache")
	tempRoot := filepath.Join(helperDir, "tmp")

	sess := newUtilityTestSession()
	sess.result = &llm.ResultMessage{Type: "result", Subtype: "success", StopReason: "end_turn"}
	sm := mocks.NewMockSessionManager()
	var startEnv []string
	var startOpts *ports.SessionOpts
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		startEnv = append([]string(nil), env...)
		if len(opts) > 0 {
			startOpts = opts[0]
		}
		if err := os.WriteFile(feedbackPath, []byte(testutil.StructuredReviewFeedback("", "", "APPROVED")), 0o644); err != nil {
			t.Errorf("write feedback: %v", err)
		}
		sess.setRootIntent(validSuccessCompletionIntent())
		sess.statusCh <- "SUCCESS"
		return sess, nil
	}

	var got BuildSessionOpts
	pr := &PhaseRunner{
		StateDir:       stateDir,
		SessionManager: sm,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			got = opts
			got.WritableRoots = append([]string(nil), opts.WritableRoots...)
			return []string{"mock-reviewer"}, []string{"TMPDIR=/old", "EXISTING=1"}, &ports.SessionOpts{
				PermHandler:       opts.PermHandler,
				DebugSystemPrompt: opts.SystemPrompt,
				ProviderName:      "mock",
			}, nil
		},
	}

	result, err := pr.RunLiveRunReviewHelper(context.Background(), ReviewHelperConfig{
		SessionID:     "qa-live-run",
		FeatureID:     "feature-1",
		Phase:         feature.PhaseReview,
		Model:         "test-model",
		Prompt:        "run the app",
		PromptPath:    filepath.Join(helperDir, "review-prompt.md"),
		FeedbackPath:  feedbackPath,
		HelperIterDir: helperDir,
		Role:          RoleImplementationReviewCraft,
		WorkDir:       workDir,
		LogPath:       filepath.Join(helperDir, "review-output.txt"),
		EffortLevel:   llm.EffortMedium,
		Kind:          ports.KindValidator,
		Label:         "QA",
		ParentSpanCtx: observe.SpanContext{FeatureID: "feature-1", RunNumber: 4},
	})
	if err != nil {
		t.Fatalf("RunLiveRunReviewHelper() error = %v", err)
	}
	if result.Status != ReviewApproved {
		t.Fatalf("result.Status = %s, want APPROVED", result.Status)
	}

	for _, dir := range []string{evidenceRoot, buildCacheRoot, tempRoot} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("scratch root %s stat = %v, info=%v; want directory", dir, err, info)
		}
	}
	for _, want := range []string{feedbackPath, evidenceRoot, buildCacheRoot, tempRoot} {
		if !stringSliceContains(got.WritableRoots, want) {
			t.Fatalf("WritableRoots missing %q in %#v", want, got.WritableRoots)
		}
	}
	if got := envValue(startEnv, "TMPDIR"); got != tempRoot {
		t.Fatalf("TMPDIR = %q, want %q; env=%v", got, tempRoot, startEnv)
	}
	if got := envValue(startEnv, "GOCACHE"); got != filepath.Join(buildCacheRoot, "go-build") {
		t.Fatalf("GOCACHE = %q, want under build cache; env=%v", got, startEnv)
	}
	if got := envValue(startEnv, "GOMODCACHE"); got != filepath.Join(buildCacheRoot, "go-mod") {
		t.Fatalf("GOMODCACHE = %q, want under build cache; env=%v", got, startEnv)
	}
	if got := envValue(startEnv, "EXISTING"); got != "1" {
		t.Fatalf("EXISTING env = %q, want preserved", got)
	}

	if startOpts == nil || startOpts.PermHandler == nil {
		t.Fatal("session PermHandler was not configured")
	}
	if startOpts.RunNumber != 4 {
		t.Fatalf("RunNumber = %d, want 4 (from ParentSpanCtx); run-scoped session lists depend on it", startOpts.RunNumber)
	}
	requirePermissionDecision(t, startOpts.PermHandler, "Bash", `{"command":"npm install && npm run build > out.log"}`, "allow")
	requirePermissionDecision(t, startOpts.PermHandler, "Write", `{"file_path":"`+filepath.Join(evidenceRoot, "screenshots", "home.png")+`"}`, "allow")
	requirePermissionDecision(t, startOpts.PermHandler, "Write", `{"file_path":"`+filepath.Join(workDir, "main.go")+`"}`, "deny")
}

func TestRunReadOnlyReviewHelper_NarrowsWritableRootsToDeclaredArtifacts(t *testing.T) {
	root := t.TempDir()
	helperDir := filepath.Join(root, "attempt-01", "validate-scope")
	parentAttemptDir := filepath.Dir(helperDir)
	siblingDir := filepath.Join(root, "attempt-01", "validate-design")
	workDir := filepath.Join(root, "work")
	for _, dir := range []string{helperDir, siblingDir, workDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}

	feedbackPath := filepath.Join(helperDir, "validation-scope-feedback.md")
	notesPath := filepath.Join(helperDir, "notes.md")
	wantRoots := []string{feedbackPath, notesPath}

	script := testutil.WriteScript(t, root, "validator.sh", `cat > "`+feedbackPath+`" << 'REVIEWEOF'
`+testutil.StructuredReviewFeedback("", "", agentStatusApproved)+`REVIEWEOF
`+testutil.JSONLInit+`
`+testutil.JSONLSuccess)

	var got BuildSessionOpts
	pr := &PhaseRunner{
		StateDir:       root,
		SessionManager: session.NewManager(make(chan interface{}, 10)),
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			got = opts
			got.AdditionalDirs = append([]string(nil), opts.AdditionalDirs...)
			got.WritableRoots = append([]string(nil), opts.WritableRoots...)
			return []string{"bash", script}, nil, &ports.SessionOpts{
				PermHandler:       opts.PermHandler,
				DebugSystemPrompt: opts.SystemPrompt,
				ProviderName:      testMockIdentifier,
			}, nil
		},
	}
	t.Cleanup(func() { pr.SessionManager.Shutdown() })

	_, err := pr.RunReadOnlyReviewHelper(context.Background(), ReviewHelperConfig{
		SessionID:              "validator-helper-1",
		FeatureID:              "feature-1",
		Phase:                  feature.PhasePlan,
		Model:                  "test-model",
		Prompt:                 "validate the plan",
		PromptPath:             filepath.Join(helperDir, "validation-scope-prompt.md"),
		ResponsePath:           filepath.Join(helperDir, "validation-scope-output.txt"),
		FeedbackPath:           feedbackPath,
		HelperIterDir:          helperDir,
		Role:                   RoleValidateRoadmapScope,
		AllowedPaths:           []string{notesPath},
		WorkDir:                workDir,
		RepoName:               "repo",
		AdditionalDirs:         []string{parentAttemptDir, siblingDir},
		LogPath:                filepath.Join(helperDir, "validation-scope-output.txt"),
		SystemPromptPrefix:     "validation-scope",
		CompletionAskingClause: "Ask at most one blocking question.",
		EffortLevel:            llm.EffortMedium,
	})
	if err != nil {
		t.Fatalf("RunReadOnlyReviewHelper() error = %v", err)
	}
	if !reflect.DeepEqual(got.WritableRoots, wantRoots) {
		t.Fatalf("WritableRoots = %#v, want %#v", got.WritableRoots, wantRoots)
	}
	for _, forbidden := range []string{root, parentAttemptDir, helperDir, siblingDir} {
		if stringSliceContains(got.WritableRoots, forbidden) {
			t.Fatalf("WritableRoots includes forbidden directory %q: %#v", forbidden, got.WritableRoots)
		}
	}
}

func TestRunReadOnlyReviewHelper_MissingRootOutcomeOverridesApprovedFeedback(t *testing.T) {
	root := t.TempDir()
	helperDir := filepath.Join(root, "review")
	workDir := filepath.Join(root, "work")
	for _, dir := range []string{helperDir, workDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}

	script := testutil.WriteScript(t, root, "reviewer-no-outcome.sh",
		testutil.WriteReviewApprovedInDir(helperDir)+`
`+testutil.JSONLInit+`
echo '{"type":"result","subtype":"success","session_id":"mock","total_cost_usd":0.001,"stop_reason":"end_turn"}'`)

	pr := &PhaseRunner{
		StateDir:       root,
		SessionManager: session.NewManager(make(chan interface{}, 10)),
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			return []string{"bash", script}, nil, &ports.SessionOpts{
				PermHandler:       opts.PermHandler,
				DebugSystemPrompt: opts.SystemPrompt,
				ProviderName:      testMockIdentifier,
			}, nil
		},
	}
	t.Cleanup(func() { pr.SessionManager.Shutdown() })

	feedbackPath := filepath.Join(helperDir, "review-feedback.md")
	result, err := pr.RunReadOnlyReviewHelper(context.Background(), ReviewHelperConfig{
		SessionID:              "review-helper-no-marker",
		FeatureID:              "feature-1",
		Phase:                  feature.PhaseReview,
		Model:                  "test-model",
		Prompt:                 "review the diff",
		PromptPath:             filepath.Join(helperDir, "review-prompt.md"),
		ResponsePath:           filepath.Join(helperDir, "review-output.txt"),
		FeedbackPath:           feedbackPath,
		HelperIterDir:          helperDir,
		Role:                   RoleImplementationReviewCraft,
		WorkDir:                workDir,
		LogPath:                filepath.Join(helperDir, "review-output.txt"),
		SystemPromptPrefix:     "review",
		CompletionAskingClause: "Ask at most one blocking question.",
		EffortLevel:            llm.EffortMedium,
	})
	if err == nil {
		t.Fatal("RunReadOnlyReviewHelper() error = nil, want protocol violation")
	}
	if !isProtocolViolationError(err) {
		t.Fatalf("RunReadOnlyReviewHelper() error = %T %v, want protocol violation", err, err)
	}
	if result == nil {
		t.Fatal("RunReadOnlyReviewHelper() result = nil, want synthesized feedback")
	}
	if result.Status != ReviewChangesRequested {
		t.Fatalf("result.Status = %s, want %s", result.Status, ReviewChangesRequested)
	}
	if !strings.Contains(result.Feedback, "agentico-outcome") {
		t.Fatalf("result.Feedback missing root-outcome violation:\n%s", result.Feedback)
	}
	if strings.Contains(result.Feedback, "\nAPPROVED") {
		t.Fatalf("result.Feedback preserved approved verdict:\n%s", result.Feedback)
	}
	onDisk, err := os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatalf("read synthesized feedback: %v", err)
	}
	if string(onDisk) != result.Feedback+"\n" {
		t.Fatalf("on-disk feedback mismatch\n--- disk ---\n%s\n--- result ---\n%s", onDisk, result.Feedback)
	}
}

func permissionHandlerIncludesBoundedArtifacts(handler ports.PermissionHandler) bool {
	switch h := handler.(type) {
	case *permission.BoundedHelperArtifactHandler:
		return true
	case *permission.SessionGuardHandler:
		return permissionHandlerIncludesBoundedArtifacts(h.Inner)
	default:
		return false
	}
}

func permissionHandlerIncludesLiveRun(handler ports.PermissionHandler) bool {
	switch h := handler.(type) {
	case *permission.LiveRunReviewHandler:
		return true
	case *permission.SessionGuardHandler:
		return permissionHandlerIncludesLiveRun(h.Inner)
	default:
		return false
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, got := range values {
		if got == want {
			return true
		}
	}
	return false
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func requirePermissionDecision(t *testing.T, handler ports.PermissionHandler, toolName, input, want string) {
	t.Helper()
	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{ToolName: toolName, Input: input})
	if err != nil {
		t.Fatalf("CanUseTool(%s) error = %v", toolName, err)
	}
	if decision.Behavior != want {
		t.Fatalf("CanUseTool(%s, %s).Behavior = %q, want %q; reason=%q", toolName, input, decision.Behavior, want, decision.Reason)
	}
}

func TestReviewHelpersUseActualModelProtocol(t *testing.T) {
	t.Parallel()
	for _, live := range []bool{false, true} {
		for _, tt := range []struct {
			name, model, inheritedTool string
			wantTool                   bool
		}{
			{name: "claude planner codex reviewer", model: "gpt-6-astra", wantTool: true},
			{name: "codex planner claude reviewer", model: "opus", inheritedTool: "complete_phase"},
		} {
			t.Run(fmt.Sprintf("%s/live=%t", tt.name, live), func(t *testing.T) {
				t.Parallel()
				registry := llm.NewRegistry()
				registry.Register(&claude.Provider{})
				registry.Register(&codex.Provider{})
				root := t.TempDir()
				stop := errors.New("stop after capturing helper launch")
				var captured BuildSessionOpts
				pr := &PhaseRunner{Registry: registry, StateDir: root, BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
					captured = opts
					return nil, nil, nil, stop
				}}
				cfg := ReviewHelperConfig{
					SessionID: "review-model-protocol", Model: tt.model, Prompt: "Review repository behavior.",
					Phase: feature.PhaseReview, Role: RoleImplementationReviewCraft,
					WorkDir: root, HelperIterDir: root, FeedbackPath: filepath.Join(root, "review-feedback.md"),
					CompletionTool: tt.inheritedTool, CompletionAskingClause: "PARENT PROVIDER ASKING CONTRACT",
				}
				var err error
				if live {
					_, err = pr.RunLiveRunReviewHelper(context.Background(), cfg)
				} else {
					_, err = pr.RunReadOnlyReviewHelper(context.Background(), cfg)
				}
				if !errors.Is(err, stop) {
					t.Fatalf("helper error = %v, want capture stop", err)
				}
				if captured.Model != tt.model || !captured.CompletionProtocol {
					t.Fatalf("wrong helper launch: model=%q completion=%v", captured.Model, captured.CompletionProtocol)
				}
				if got := strings.Contains(captured.SystemPrompt, "call `complete_phase`"); got != tt.wantTool {
					t.Fatalf("structured completion=%v, want %v:\n%s", got, tt.wantTool, captured.SystemPrompt)
				}
				if got := strings.Contains(captured.SystemPrompt, "<agentico-outcome>"); got == tt.wantTool {
					t.Fatalf("wrong outcome transport:\n%s", captured.SystemPrompt)
				}
				if strings.Contains(captured.SystemPrompt, "PARENT PROVIDER ASKING CONTRACT") || !strings.Contains(captured.SystemPrompt, pr.askingQuestionsClauseForModel(tt.model)) {
					t.Fatalf("helper inherited parent question protocol:\n%s", captured.SystemPrompt)
				}
			})
		}
	}
}

func TestPlanValidatorUsesReviewProviderProtocol(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		planner, reviewer string
		wantTool          bool
	}{
		{planner: "opus", reviewer: "gpt-6-astra", wantTool: true},
		{planner: "gpt-6-astra", reviewer: "opus"},
	} {
		t.Run(tt.planner+" to "+tt.reviewer, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			registry := llm.NewRegistry()
			registry.Register(&claude.Provider{})
			registry.Register(&codex.Provider{})
			parent := &PhaseRunner{Registry: registry}
			f := &feature.Feature{ID: "mixed-provider-plan"}
			f.Models.Planning, f.Models.Review = tt.planner, tt.reviewer
			var captured BuildSessionOpts
			cfg := PlanLoopConfig{
				Registry: registry, Feature: f, StateDir: root, WorkDir: root,
				AskingClause:   parent.askingQuestionsClauseForModel(tt.planner),
				CompletionTool: parent.completionToolForModel(tt.planner),
				BuildSession: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
					captured = opts
					return nil, nil, nil, errors.New("stop after validator launch capture")
				},
			}
			_, _, _, _ = runSpecializedPlanValidation(cfg, mocks.NewMockSessionManager(), 1, root, filepath.Join(root, "plan.md"), validatorDomain{Name: "Architecture", Template: "validate-roadmap-architecture"}, observe.SpanContext{})
			if captured.Model != tt.reviewer {
				t.Fatalf("model=%q, want %q", captured.Model, tt.reviewer)
			}
			if got := strings.Contains(captured.SystemPrompt, "call `complete_phase`"); got != tt.wantTool {
				t.Fatalf("validator inherited planner completion transport:\n%s", captured.SystemPrompt)
			}
			if !strings.Contains(captured.SystemPrompt, parent.askingQuestionsClauseForModel(tt.reviewer)) {
				t.Fatalf("validator inherited planner asking contract:\n%s", captured.SystemPrompt)
			}
		})
	}
}
