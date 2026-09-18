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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func conflictResolutionTestInput() roles.ConflictResolutionUserInput {
	return roles.ConflictResolutionUserInput{
		FeatureName:        "Rebase the payment stack",
		FeatureDescription: "Replay the stack onto the latest main.",
		LayerPosition:      2,
		LayerTitle:         "Wire the refund callback",
		RoadmapPhase:       3,
		TargetBranch:       "main",
		TargetSHA:          "1111111111111111111111111111111111111111",
		CommitMessage:      "fix: handle partial refunds\n\nRefunds under a dollar round to zero.",
		CommitPatch:        "--- a/internal/refund.go\n+++ b/internal/refund.go\n@@ -1,3 +1,4 @@",
		ConflictFiles:      []string{"internal/refund.go", "internal/refund_test.go"},
		UpstreamDiff:       "--- a/internal/refund.go\n+++ b/internal/refund.go\n@@ -2,2 +2,3 @@",
		Feedback:           "Refund test still contains a conflict marker.",
	}
}

func TestBuildConflictResolutionPromptRendersStructuredContext(t *testing.T) {
	got := roles.BuildConflictResolutionPrompt(conflictResolutionTestInput())

	for _, want := range []string{
		"**Rebase the payment stack**",
		"Replay the stack onto the latest main.",
		"layer 2: Wire the refund callback",
		"roadmap phase 3",
		"branch `main`",
		"`1111111111111111111111111111111111111111`",
		"fix: handle partial refunds",
		"Refunds under a dollar round to zero.",
		"--- a/internal/refund.go\n+++ b/internal/refund.go\n@@ -1,3 +1,4 @@",
		"- internal/refund.go",
		"- internal/refund_test.go",
		"@@ -2,2 +2,3 @@",
		"## Previous Attempt Feedback",
		"Refund test still contains a conflict marker.",
		"Never run git or any other mutating command",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildConflictResolutionPrompt() missing %q in:\n%s", want, got)
		}
	}

	noFeedback := conflictResolutionTestInput()
	noFeedback.Feedback = ""
	got = roles.BuildConflictResolutionPrompt(noFeedback)
	if strings.Contains(got, "## Previous Attempt Feedback") {
		t.Fatalf("BuildConflictResolutionPrompt() rendered a feedback section for empty feedback:\n%s", got)
	}
}

func TestBuildConflictResolverSystemPromptFromRoleSpec(t *testing.T) {
	attemptDir := filepath.Join(t.TempDir(), "conflict-attempt-01")
	got := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:         ConflictResolverRoleSpec(),
		IterationDir: attemptDir,
		SkillsDir:    "/skills",
	})

	for _, want := range []string{
		"`attempt_dir`: " + attemptDir,
		"The harness owns the durable completion receipt",
		"<agentico-outcome>",
		"/skills/resolve-rebase-conflict/SKILL.md",
		"Sub-agents are available.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildRoleSystemPrompt(conflict resolver) missing %q in:\n%s", want, got)
		}
	}
	// The role must edit the conflicted source files, so the generic
	// builder's read-only-outside-roots clause must stay off, and a role
	// with no iteration state must never see the retry outcome.
	for _, unwanted := range []string{
		"ABSOLUTE: write only inside the output roots above.",
		"This is a read-only phase for target repositories.",
		`"status":"retry"`,
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("BuildRoleSystemPrompt(conflict resolver) contains %q:\n%s", unwanted, got)
		}
	}
}

// newConflictResolutionTestRunner wires a PhaseRunner whose session manager
// and BuildSession are test doubles. The StartSession double mirrors the
// production manager by creating the session log at opts.LogPath, and every
// captured BuildSessionOpts entry is returned for wiring assertions.
func newConflictResolutionTestRunner(t *testing.T, sess ports.SessionHandle) (*PhaseRunner, *[]BuildSessionOpts) {
	t.Helper()

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		if len(opts) > 0 && opts[0] != nil && opts[0].LogPath != "" {
			if err := os.WriteFile(opts[0].LogPath, []byte("resolved conflicts\n"), 0o644); err != nil {
				t.Fatalf("writing session log: %v", err)
			}
		}
		return sess, nil
	}

	captured := &[]BuildSessionOpts{}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}
	pr.BuildSessionFn = func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		*captured = append(*captured, opts)
		return []string{testMockIdentifier}, nil, &ports.SessionOpts{LogPath: opts.LogPath}, nil
	}
	return pr, captured
}

func conflictResolutionTestRequest(workDir, attemptDir string, timeout time.Duration) ConflictResolutionRequest {
	return ConflictResolutionRequest{
		FeatureID:     "feat-1",
		SessionID:     "feat-1-rebase-conflict-01",
		RunNumber:     2,
		RepoName:      "repo-a",
		WorkDir:       workDir,
		AttemptDir:    attemptDir,
		Prompt:        "Resolve the conflicted files listed below.",
		SystemPrompt:  "You resolve rebase conflicts.",
		Model:         "test-model",
		ConflictPaths: []string{"internal/refund.go", "internal/refund_test.go"},
		Timeout:       timeout,
	}
}

func TestRunConflictResolution_CompletesAndWritesAttemptArtifacts(t *testing.T) {
	sess := newUtilityTestSession()
	sess.id = "feat-1-rebase-conflict-01"
	sess.result = &llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn}
	sess.setRootIntent(validSuccessCompletionIntent())
	sess.statusCh <- agentStatusSuccess
	close(sess.done)

	pr, captured := newConflictResolutionTestRunner(t, sess)
	workDir := t.TempDir()
	attemptDir := t.TempDir()

	result, err := pr.RunConflictResolution(context.Background(), conflictResolutionTestRequest(workDir, attemptDir, 2*time.Second))
	if err != nil {
		t.Fatalf("RunConflictResolution() error = %v", err)
	}
	if result == nil || result.Status != ConflictResolutionCompleted {
		t.Fatalf("result = %+v (err %v), want status %q", result, err, ConflictResolutionCompleted)
	}
	if result.Reason != "" {
		t.Errorf("completed result Reason = %q, want empty", result.Reason)
	}

	for _, name := range []string{"user-prompt.md", "system-prompt.md", "output.txt", PhaseCompleteFile} {
		if _, err := os.Stat(filepath.Join(attemptDir, name)); err != nil {
			t.Fatalf("attempt dir missing %s after a completed run: %v", name, err)
		}
	}
	promptBytes, err := os.ReadFile(filepath.Join(attemptDir, "user-prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(promptBytes) != "Resolve the conflicted files listed below." {
		t.Errorf("user-prompt.md = %q, want the rendered user prompt", promptBytes)
	}
	receipt, err := ReadCompletionReceipt(attemptDir)
	if err != nil {
		t.Fatalf("ReadCompletionReceipt() error = %v", err)
	}
	if receipt.Phase != feature.PhaseImplement.DirName() || receipt.Role != RoleResolveRebaseConflict {
		t.Fatalf("receipt = phase %q role %q, want implement %q", receipt.Phase, receipt.Role, RoleResolveRebaseConflict)
	}

	if len(*captured) != 1 {
		t.Fatalf("BuildSession calls = %d, want 1", len(*captured))
	}
	opts := (*captured)[0]
	if opts.Phase != feature.PhaseImplement {
		t.Errorf("BuildSessionOpts.Phase = %v, want implement", opts.Phase)
	}
	if !opts.CompletionProtocol {
		t.Error("BuildSessionOpts.CompletionProtocol = false, want true (semantic completion)")
	}
	wantRoots := []string{filepath.Join(workDir, "internal/refund.go"), filepath.Join(workDir, "internal/refund_test.go")}
	if !slices.Equal(opts.WritableRoots, wantRoots) {
		t.Errorf("BuildSessionOpts.WritableRoots = %v, want %v", opts.WritableRoots, wantRoots)
	}
	guard, ok := opts.PermHandler.(*permission.SessionGuardHandler)
	if !ok {
		t.Fatalf("BuildSessionOpts.PermHandler = %T, want a Guarded-wrapped ConflictResolverHandler", opts.PermHandler)
	}
	conflictHandler, ok := guard.Inner.(*permission.ConflictResolverHandler)
	if !ok {
		t.Fatalf("Guarded inner handler = %T, want *permission.ConflictResolverHandler", guard.Inner)
	}
	if conflictHandler.WorkDir != workDir || !slices.Equal(conflictHandler.ConflictPaths, []string{"internal/refund.go", "internal/refund_test.go"}) {
		t.Errorf("ConflictResolverHandler = workdir %q paths %v, want the request's scope", conflictHandler.WorkDir, conflictHandler.ConflictPaths)
	}
}

func TestRunConflictResolution_FailedTerminalStatesReportReasons(t *testing.T) {
	tests := []struct {
		name       string
		build      func() ports.SessionHandle
		wantReason string
	}{
		{
			name: "times out",
			build: func() ports.SessionHandle {
				return newUtilityTestSession()
			},
			wantReason: "session timed out",
		},
		{
			name: "asks the user",
			build: func() ports.SessionHandle {
				sess := newUtilityTestSession()
				sess.attachCh <- askUserControlRequest("ask-1")
				return sess
			},
			wantReason: "session asked the user for input",
		},
		{
			name: "requests a denied permission",
			build: func() ports.SessionHandle {
				sess := newUtilityTestSession()
				sess.attachCh <- mocks.ControlRequestMsg("perm-1", "Bash")
				return sess
			},
			wantReason: "session requested a denied tool permission",
		},
		{
			name: "ends without a completion outcome",
			build: func() ports.SessionHandle {
				sess := newUtilityTestSession()
				sess.result = &llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn}
				close(sess.done)
				return sess
			},
			wantReason: "session ended without completing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr, _ := newConflictResolutionTestRunner(t, tt.build())
			result, err := pr.RunConflictResolution(context.Background(), conflictResolutionTestRequest(t.TempDir(), t.TempDir(), 2*time.Second))
			if err != nil {
				t.Fatalf("RunConflictResolution() error = %v, want the failure reported through the result", err)
			}
			if result == nil {
				t.Fatal("RunConflictResolution() result = nil, want a failed result")
			}
			if result.Status != ConflictResolutionFailed {
				t.Fatalf("Status = %q, want %q", result.Status, ConflictResolutionFailed)
			}
			if result.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %q", result.Reason, tt.wantReason)
			}
		})
	}
}

func TestRunConflictResolution_ValidatesRequiredFields(t *testing.T) {
	pr, _ := newConflictResolutionTestRunner(t, newUtilityTestSession())

	base := conflictResolutionTestRequest(t.TempDir(), t.TempDir(), time.Second)
	cases := []struct {
		name   string
		mutate func(*ConflictResolutionRequest)
	}{
		{"missing session id", func(r *ConflictResolutionRequest) { r.SessionID = "" }},
		{"missing model", func(r *ConflictResolutionRequest) { r.Model = "" }},
		{"missing work dir", func(r *ConflictResolutionRequest) { r.WorkDir = "" }},
		{"missing attempt dir", func(r *ConflictResolutionRequest) { r.AttemptDir = "" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req := base
			tt.mutate(&req)
			result, err := pr.RunConflictResolution(context.Background(), req)
			if err == nil {
				t.Fatal("RunConflictResolution() error = nil, want validation error")
			}
			if result != nil {
				t.Fatalf("result = %+v, want nil", result)
			}
		})
	}
}
