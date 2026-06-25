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
	"reflect"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
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
touch "`+filepath.Join(helperDir, "phase_complete")+`"
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
				ProviderName:      "mock",
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
		Role:                   RoleIterationReviewer,
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
	if !strings.Contains(got.SystemPrompt, filepath.Join(helperDir, "phase_complete")) {
		t.Fatalf("SystemPrompt missing helper phase_complete path:\n%s", got.SystemPrompt)
	}
}

func TestRunReadOnlyReviewHelper_DelegatesEditsToClient(t *testing.T) {
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
touch "`+filepath.Join(helperDir, "phase_complete")+`"
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
				ProviderName:      "mock",
			}, nil
		},
	}
	t.Cleanup(func() { pr.SessionManager.Shutdown() })

	_, err := pr.RunReadOnlyReviewHelper(context.Background(), ReviewHelperConfig{
		SessionID:              "review-helper-delegate",
		FeatureID:              "feature-1",
		Phase:                  feature.PhaseReview,
		Model:                  "test-model",
		Prompt:                 "review the diff",
		PromptPath:             filepath.Join(helperDir, "review-prompt.md"),
		ResponsePath:           filepath.Join(helperDir, "review-output.txt"),
		FeedbackPath:           filepath.Join(helperDir, "review-feedback.md"),
		HelperIterDir:          helperDir,
		Role:                   RoleIterationReviewer,
		WorkDir:                workDir,
		LogPath:                filepath.Join(helperDir, "review-output.txt"),
		SystemPromptPrefix:     "review",
		CompletionAskingClause: "Ask at most one blocking question.",
		EffortLevel:            llm.EffortMedium,
	})
	if err != nil {
		t.Fatalf("RunReadOnlyReviewHelper() error = %v", err)
	}
	// Bounded helpers must delegate edits to the client handler: OpenCode's own
	// edit glob map collapses to a catch-all deny for the helper's worktree-relative
	// artifact paths, so the helper's feedback/phase_complete writes are denied
	// before the client handler (which allows exactly those paths) is ever asked.
	if !got.DelegateEditsToClient {
		t.Fatalf("BuildSessionOpts.DelegateEditsToClient = false, want true for bounded review helper")
	}
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
	markerPath := filepath.Join(helperDir, "phase_complete")
	notesPath := filepath.Join(helperDir, "notes.md")
	wantRoots := []string{feedbackPath, markerPath, notesPath}

	script := testutil.WriteScript(t, root, "validator.sh", `cat > "`+feedbackPath+`" << 'REVIEWEOF'
`+testutil.StructuredReviewFeedback("", "", "APPROVED")+`REVIEWEOF
touch "`+markerPath+`"
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
				ProviderName:      "mock",
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

func TestRunReadOnlyReviewHelper_ProtocolViolationOverridesApprovedFeedback(t *testing.T) {
	root := t.TempDir()
	helperDir := filepath.Join(root, "review")
	workDir := filepath.Join(root, "work")
	for _, dir := range []string{helperDir, workDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}

	script := testutil.WriteScript(t, root, "reviewer-no-marker.sh",
		testutil.WriteReviewApprovedInDir(helperDir)+`
`+testutil.JSONLInit+`
`+testutil.JSONLSuccess)

	pr := &PhaseRunner{
		StateDir:       root,
		SessionManager: session.NewManager(make(chan interface{}, 10)),
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			return []string{"bash", script}, nil, &ports.SessionOpts{
				PermHandler:       opts.PermHandler,
				DebugSystemPrompt: opts.SystemPrompt,
				ProviderName:      "mock",
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
		Role:                   RoleIterationReviewer,
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
	if !strings.Contains(result.Feedback, "phase_complete") {
		t.Fatalf("result.Feedback missing phase_complete violation:\n%s", result.Feedback)
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
	case *permission.SizeGuardHandler:
		return permissionHandlerIncludesBoundedArtifacts(h.Inner)
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
