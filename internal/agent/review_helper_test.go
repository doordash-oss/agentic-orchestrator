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
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
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

func TestRunReadOnlyReviewHelper_SmartZoneHandoffContinuation(t *testing.T) {
	withHandoffPollInterval(t, 2*time.Millisecond)

	root := t.TempDir()
	helperDir := filepath.Join(root, "review")
	workDir := filepath.Join(root, "work")
	for _, dir := range []string{helperDir, workDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "feature-1"), 0o755); err != nil {
		t.Fatalf("mkdir feature state: %v", err)
	}

	var (
		mu          sync.Mutex
		starts      int
		handoffText string
		captured    []BuildSessionOpts
	)
	handoffPath := filepath.Join(helperDir, ReviewProgressHandoffFilename)
	feedbackPath := filepath.Join(helperDir, "review-feedback.md")
	promptPath := filepath.Join(helperDir, "review-prompt.md")
	originalPrompt := "review the diff"
	manager := &stubSessionManager{
		start: func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
			mu.Lock()
			starts++
			startNo := starts
			mu.Unlock()

			sess := session.NewSession(id, featureID, phase)
			sess.SetProviderName("codex")
			if startNo == 1 {
				sess.SetLatestUsage(&llm.Usage{
					ContextTotalTokens: 80_000,
					ContextWindow:      200_000,
				})
				sink := attachCaptureSink(sess)
				go func() {
					select {
					case <-sink.done:
						mu.Lock()
						handoffText = sink.contents()
						mu.Unlock()
						_ = os.WriteFile(handoffPath, []byte(validReviewProgressHandoff("CONTINUE", "checked report")), 0o644)
						_ = os.WriteFile(filepath.Join(helperDir, "phase_complete"), []byte("done\n"), 0o644)
						sess.SetCost(&llm.ResultMessage{Subtype: "success"})
						sess.CloseDone()
						sess.SendStatus(agentStatusSuccess)
					case <-time.After(2 * time.Second):
						sess.SetCost(&llm.ResultMessage{Subtype: "error", IsError: true})
						sess.CloseDone()
						sess.SendStatus(agentStatusFailed)
					}
				}()
				return sess, nil
			}
			go func() {
				_ = os.WriteFile(handoffPath, []byte(validReviewProgressHandoff("COMPLETE", "checked report")), 0o644)
				_ = os.WriteFile(feedbackPath, []byte(testutil.StructuredReviewFeedback("", "", "APPROVED")), 0o644)
				_ = os.WriteFile(filepath.Join(helperDir, "phase_complete"), []byte("done\n"), 0o644)
				sess.SetCost(&llm.ResultMessage{Subtype: "success"})
				sess.CloseDone()
				sess.SendStatus(agentStatusSuccess)
			}()
			return sess, nil
		},
	}
	pr := &PhaseRunner{
		StateDir:       root,
		SessionManager: manager,
		Observer:       observe.New(true, root, false, "", false, "agentic"),
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			mu.Lock()
			captured = append(captured, opts)
			mu.Unlock()
			return []string{"mock-reviewer"}, nil, &ports.SessionOpts{
				PermHandler:                   opts.PermHandler,
				DebugSystemPrompt:             opts.SystemPrompt,
				ProviderName:                  "codex",
				ContextHandoffThresholdTokens: 80_000,
			}, nil
		},
	}

	result, err := pr.RunReadOnlyReviewHelper(context.Background(), ReviewHelperConfig{
		SessionID:               "review-helper-smart-zone",
		FeatureID:               "feature-1",
		Phase:                   feature.PhaseReview,
		Model:                   "test-model",
		Prompt:                  originalPrompt,
		PromptPath:              promptPath,
		ResponsePath:            filepath.Join(helperDir, "review-output.txt"),
		FeedbackPath:            feedbackPath,
		HelperIterDir:           helperDir,
		Role:                    RoleIterationReviewer,
		WorkDir:                 workDir,
		LogPath:                 filepath.Join(helperDir, "review-output.txt"),
		SystemPromptPrefix:      "review",
		CompletionAskingClause:  "Ask at most one blocking question.",
		EffortLevel:             llm.EffortMedium,
		EnableSmartZoneHandoff:  true,
		HandoffPath:             handoffPath,
		MaxConsecNoProgress:     3,
		MaxConsecHandoffFails:   3,
		ContextHandoffIteration: 1,
	})
	if err != nil {
		t.Fatalf("RunReadOnlyReviewHelper() error = %v", err)
	}
	if result.Status != ReviewApproved {
		t.Fatalf("result.Status = %s, want APPROVED", result.Status)
	}
	mu.Lock()
	gotStarts := starts
	gotHandoff := handoffText
	gotCaptured := append([]BuildSessionOpts(nil), captured...)
	mu.Unlock()
	if gotStarts != 2 {
		t.Fatalf("starts = %d, want 2", gotStarts)
	}
	if !strings.Contains(gotHandoff, "skills/review-implementation/HANDOFF.md") {
		t.Fatalf("handoff pointer missing review-implementation skill:\n%s", gotHandoff)
	}
	if len(gotCaptured) == 0 || !stringSliceContains(gotCaptured[0].WritableRoots, handoffPath) {
		t.Fatalf("WritableRoots missing handoff scratch %q: %#v", handoffPath, gotCaptured)
	}
	if len(gotCaptured) < 2 || !strings.Contains(gotCaptured[1].Prompt, ReviewProgressHandoffFilename) {
		t.Fatalf("continuation prompt missing %s: %#v", ReviewProgressHandoffFilename, gotCaptured)
	}
	promptData, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read original review prompt: %v", err)
	}
	if string(promptData) != originalPrompt {
		t.Fatalf("original review prompt overwritten by continuation prompt:\n%s", promptData)
	}
	continuationPromptPath := filepath.Join(helperDir, "review-prompt-c02.md")
	continuationData, err := os.ReadFile(continuationPromptPath)
	if err != nil {
		t.Fatalf("read continuation review prompt: %v", err)
	}
	if !strings.Contains(string(continuationData), ReviewProgressHandoffFilename) {
		t.Fatalf("continuation prompt file missing handoff context:\n%s", continuationData)
	}
	events, err := os.ReadFile(filepath.Join(root, "feature-1", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if !strings.Contains(string(events), `"event_type":"context.handoff_triggered"`) ||
		!strings.Contains(string(events), `"phase":"review"`) ||
		!strings.Contains(string(events), `"threshold_tokens":80000`) {
		t.Fatalf("events.jsonl missing review handoff threshold event:\n%s", events)
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

type stubSessionManager struct {
	start func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error)
}

func (m *stubSessionManager) StartSession(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
	if m.start == nil {
		return nil, fmt.Errorf("stub session manager: missing start func")
	}
	return m.start(id, featureID, phase, command, workdir, env, opts...)
}

func (m *stubSessionManager) StopSession(string) error                   { return nil }
func (m *stubSessionManager) GetSession(string) ports.SessionView        { return nil }
func (m *stubSessionManager) ActiveSessions() []ports.SessionView        { return nil }
func (m *stubSessionManager) FeatureSessions(string) []ports.SessionView { return nil }
func (m *stubSessionManager) SendInput(string, []byte) error             { return nil }
func (m *stubSessionManager) Attach(string) (ports.SessionView, error)   { return nil, nil }
func (m *stubSessionManager) Detach()                                    {}
func (m *stubSessionManager) Shutdown()                                  {}
func (m *stubSessionManager) IsShuttingDown() bool                       { return false }

func stringSliceContains(values []string, want string) bool {
	for _, got := range values {
		if got == want {
			return true
		}
	}
	return false
}
