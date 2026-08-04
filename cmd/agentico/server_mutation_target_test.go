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

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/utilskill"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// Test-fixture literals reused across several cases in this file.
const (
	testFeaturePermissionID = "feat-permission"
	testFeatureHelpID       = "feat-help"
	testAskRequestID        = "ask-1"
	testWorkspaceDir        = "/workspace"
	testRepoAWorktreePath   = "/tmp/repo-a-worktree"

	wireTypeControlRequest = "control_request"
	wireSubtypeCanUseTool  = "can_use_tool"

	decisionAllowRemember     = "allow_remember"
	rememberPatternBashGoTest = "Bash(go test *)"

	labelUseFullInput = "Use the full input (Recommended)"

	testResearchModelNew       = "new-research"
	testImplementationModelNew = "new-implementation"
	testPlanningModelDefault   = "default-planning"

	testInquirenessHigh = "high"
	testInquirenessNone = "none"

	testModelCodexGPT54     = "codex:gpt-5.4"
	testModelClaudeSonnet   = "claude:sonnet"
	testModelCodexGPT54Mini = "codex:gpt-5.4-mini"

	testSessionPermissionID = "session-permission"
	testPermRequestID       = "perm-1"
	testRepoAName           = "repo-a"
	testRepoBName           = "repo-b"
	testSessionAskID        = "session-ask"
	testSessionHelpID       = "session-help"
	testReviewerLogin       = "reviewer"
)

func TestServerMutationTargetAnswerPermissionRespondsToPendingControlRequest(t *testing.T) {
	for _, tc := range []struct {
		name      string
		decision  string
		wantAllow bool
	}{
		{name: "allow once", decision: "allow_once", wantAllow: true},
		{name: "deny", decision: "deny", wantAllow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := json.RawMessage(`{"command":"go test ./cmd/agentico"}`)
			sess := &mutationTargetSessionView{
				id:        testSessionPermissionID,
				featureID: testFeaturePermissionID,
				phase:     feature.PhaseImplement,
				status:    ports.SessionWaitingPermission,
				active:    true,
				pending: []*llm.ControlRequestMessage{{
					Type:      wireTypeControlRequest,
					RequestID: testPermRequestID,
					Request: llm.ControlRequest{
						Subtype:  wireSubtypeCanUseTool,
						ToolName: toolNameBash,
						Input:    input,
					},
				}},
			}
			sessions := &mutationTargetSessionManager{sessions: []ports.SessionView{sess}}
			target := serverMutationTarget{
				orch:     mutationTargetOrchestrator(sessions),
				sessions: sessions,
			}

			result, err := target.AnswerPermission(serverruntime.PermissionAnswerRequest{
				RequestID: testPermRequestID,
				SessionID: testSessionPermissionID,
				Decision:  tc.decision,
			})
			if err != nil {
				t.Fatalf("AnswerPermission() error = %v", err)
			}

			if len(sess.controlCalls) != 1 {
				t.Fatalf("RespondToControl calls = %d, want 1", len(sess.controlCalls))
			}
			call := sess.controlCalls[0]
			if call.requestID != testPermRequestID || call.allow != tc.wantAllow {
				t.Fatalf("RespondToControl call = %+v, want request perm-1 allow=%v", call, tc.wantAllow)
			}
			if !jsonEqual(call.originalInput, input) {
				t.Fatalf("RespondToControl original input = %s, want %s", call.originalInput, input)
			}
			if result.RequestID != testPermRequestID || result.SessionID != testSessionPermissionID || result.Decision != tc.decision || result.Result != resultAnswered {
				t.Fatalf("AnswerPermission() result = %+v; want request/session/decision answer", result)
			}
			assertJSONDoesNotContain(t, result, "go test ./cmd/agentico")
		})
	}
}

func TestServerMutationTargetAnswerPermissionRejectsLegacyAllow(t *testing.T) {
	input := json.RawMessage(`{"command":"go test ./cmd/agentico"}`)
	sess := &mutationTargetSessionView{
		id:        testSessionPermissionID,
		featureID: testFeaturePermissionID,
		phase:     feature.PhaseImplement,
		status:    ports.SessionWaitingPermission,
		active:    true,
		pending: []*llm.ControlRequestMessage{{
			Type:      wireTypeControlRequest,
			RequestID: testPermRequestID,
			Request: llm.ControlRequest{
				Subtype:  wireSubtypeCanUseTool,
				ToolName: toolNameBash,
				Input:    input,
			},
		}},
	}
	sessions := &mutationTargetSessionManager{sessions: []ports.SessionView{sess}}
	target := serverMutationTarget{
		orch:     mutationTargetOrchestrator(sessions),
		sessions: sessions,
	}

	_, err := target.AnswerPermission(serverruntime.PermissionAnswerRequest{
		RequestID: testPermRequestID,
		SessionID: testSessionPermissionID,
		Decision:  "allow",
	})
	if err == nil {
		t.Fatal("AnswerPermission() error = nil, want legacy allow rejected")
	}
	if len(sess.controlCalls) != 0 {
		t.Fatalf("RespondToControl calls = %d, want none", len(sess.controlCalls))
	}
}

func TestServerMutationTargetAnswerPermissionAllowRememberPersistsBeforeAnswer(t *testing.T) {
	input := json.RawMessage(`{"command":"go test ./cmd/agentico"}`)
	permDir := t.TempDir()
	cache := permission.NewCache(permission.NewStore(permDir))
	sess := &mutationTargetSessionView{
		id:             testSessionPermissionID,
		featureID:      testFeaturePermissionID,
		phase:          feature.PhaseImplement,
		status:         ports.SessionWaitingPermission,
		active:         true,
		permCacheScope: testRepoAName,
		pending: []*llm.ControlRequestMessage{{
			Type:      wireTypeControlRequest,
			RequestID: testPermRequestID,
			Request: llm.ControlRequest{
				Subtype:  wireSubtypeCanUseTool,
				ToolName: toolNameBash,
				Input:    input,
			},
		}},
		onRespondControl: func() error {
			if _, ok := cache.Check(toolNameBash, string(input), testRepoAName); !ok {
				t.Fatal("rule was not persisted before answering")
			}
			return nil
		},
	}
	sessions := &mutationTargetSessionManager{sessions: []ports.SessionView{sess}}
	target := serverMutationTarget{
		orch:            mutationTargetOrchestrator(sessions),
		sessions:        sessions,
		permissionCache: cache,
	}
	scope := testRepoAName

	result, err := target.AnswerPermission(serverruntime.PermissionAnswerRequest{
		RequestID:       testPermRequestID,
		SessionID:       testSessionPermissionID,
		Decision:        decisionAllowRemember,
		RememberPattern: rememberPatternBashGoTest,
		RememberScope:   &scope,
	})
	if err != nil {
		t.Fatalf("AnswerPermission() error = %v", err)
	}
	if len(sess.controlCalls) != 1 || !sess.controlCalls[0].allow {
		t.Fatalf("RespondToControl calls = %+v, want one allow", sess.controlCalls)
	}
	if result.Decision != decisionAllowRemember || result.Result != resultAnswered {
		t.Fatalf("AnswerPermission() result = %+v, want remembered answer", result)
	}
	if _, ok := cache.Check(toolNameBash, string(input), testRepoAName); !ok {
		t.Fatal("remembered rule missing after answer")
	}
	auditRaw, err := os.ReadFile(filepath.Join(permDir, "remember-audit.jsonl"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if !strings.Contains(string(auditRaw), `"result":"success"`) {
		t.Fatalf("audit log = %s, want success event", auditRaw)
	}
}

func TestServerMutationTargetAnswerPermissionAllowRememberPersistenceFailureDoesNotAnswer(t *testing.T) {
	input := json.RawMessage(`{"command":"go test ./cmd/agentico"}`)
	permDir := filepath.Join(t.TempDir(), "permissions")
	if err := os.WriteFile(permDir, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write blocking permission path: %v", err)
	}
	cache := permission.NewCache(permission.NewStore(permDir))
	sess := &mutationTargetSessionView{
		id:        testSessionPermissionID,
		featureID: testFeaturePermissionID,
		phase:     feature.PhaseImplement,
		status:    ports.SessionWaitingPermission,
		active:    true,
		pending: []*llm.ControlRequestMessage{{
			Type:      wireTypeControlRequest,
			RequestID: testPermRequestID,
			Request: llm.ControlRequest{
				Subtype:  wireSubtypeCanUseTool,
				ToolName: toolNameBash,
				Input:    input,
			},
		}},
	}
	sessions := &mutationTargetSessionManager{sessions: []ports.SessionView{sess}}
	target := serverMutationTarget{
		orch:            mutationTargetOrchestrator(sessions),
		sessions:        sessions,
		permissionCache: cache,
	}
	scope := testRepoAName

	_, err := target.AnswerPermission(serverruntime.PermissionAnswerRequest{
		RequestID:       testPermRequestID,
		SessionID:       testSessionPermissionID,
		Decision:        decisionAllowRemember,
		RememberPattern: rememberPatternBashGoTest,
		RememberScope:   &scope,
	})
	if err == nil {
		t.Fatal("AnswerPermission() error = nil, want persistence failure")
	}
	if len(sess.controlCalls) != 0 {
		t.Fatalf("RespondToControl calls = %+v, want none", sess.controlCalls)
	}
}

func TestServerMutationTargetAnswerPermissionAllowRememberDuplicateReturnsAlreadyExisted(t *testing.T) {
	input := json.RawMessage(`{"command":"go test ./cmd/agentico"}`)
	permDir := t.TempDir()
	cache := permission.NewCache(permission.NewStore(permDir))
	if _, err := cache.RememberAllowPattern(rememberPatternBashGoTest, testRepoAName); err != nil {
		t.Fatalf("seed remembered rule: %v", err)
	}
	sess := &mutationTargetSessionView{
		id:        testSessionPermissionID,
		featureID: testFeaturePermissionID,
		phase:     feature.PhaseImplement,
		status:    ports.SessionWaitingPermission,
		active:    true,
		pending: []*llm.ControlRequestMessage{{
			Type:      wireTypeControlRequest,
			RequestID: testPermRequestID,
			Request: llm.ControlRequest{
				Subtype:  wireSubtypeCanUseTool,
				ToolName: toolNameBash,
				Input:    input,
			},
		}},
	}
	sessions := &mutationTargetSessionManager{sessions: []ports.SessionView{sess}}
	target := serverMutationTarget{
		orch:            mutationTargetOrchestrator(sessions),
		sessions:        sessions,
		permissionCache: cache,
	}
	scope := testRepoAName

	result, err := target.AnswerPermission(serverruntime.PermissionAnswerRequest{
		RequestID:       testPermRequestID,
		SessionID:       testSessionPermissionID,
		Decision:        decisionAllowRemember,
		RememberPattern: rememberPatternBashGoTest,
		RememberScope:   &scope,
	})
	if err != nil {
		t.Fatalf("AnswerPermission() error = %v", err)
	}
	if !result.AlreadyExisted {
		t.Fatal("AlreadyExisted = false, want true")
	}
	if _, err := os.Stat(filepath.Join(permDir, "remember-audit.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("audit stat error = %v, want duplicate to skip audit", err)
	}
}

func TestServerMutationTargetAnswerAskUserRespondsWithOriginalInputAndSafeMetadata(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"Which DB?"},{"question":"Rollout plan?"}]}`)
	answers := map[string]string{
		"Which DB?":     "Postgres with read replicas",
		"Rollout plan?": "Dark launch first",
	}
	sess := &mutationTargetSessionView{
		id:        testSessionAskID,
		featureID: "feat-ask",
		phase:     feature.PhaseInquire,
		status:    ports.SessionWaitingHelp,
		active:    true,
		pending: []*llm.ControlRequestMessage{{
			Type:      wireTypeControlRequest,
			RequestID: testAskRequestID,
			Request: llm.ControlRequest{
				Subtype:  wireSubtypeCanUseTool,
				ToolName: toolNameAskUserQuestion,
				Input:    input,
			},
		}},
	}
	sessions := &mutationTargetSessionManager{sessions: []ports.SessionView{sess}}
	target := serverMutationTarget{
		orch:     mutationTargetOrchestrator(sessions),
		sessions: sessions,
	}

	result, err := target.AnswerAskUser(serverruntime.AskUserAnswerRequest{
		RequestID: testAskRequestID,
		SessionID: testSessionAskID,
		Answers:   answers,
	})
	if err != nil {
		t.Fatalf("AnswerAskUser() error = %v", err)
	}

	if len(sess.askCalls) != 1 {
		t.Fatalf("RespondToAskUser calls = %d, want 1", len(sess.askCalls))
	}
	call := sess.askCalls[0]
	if call.requestID != testAskRequestID {
		t.Fatalf("RespondToAskUser requestID = %q, want ask-1", call.requestID)
	}
	if !jsonEqual(call.questions, input) {
		t.Fatalf("RespondToAskUser questions = %s, want original %s", call.questions, input)
	}
	if !reflect.DeepEqual(call.answers, answers) {
		t.Fatalf("RespondToAskUser answers = %v, want %v", call.answers, answers)
	}
	if result.RequestID != testAskRequestID || result.SessionID != testSessionAskID || result.Result != resultAnswered {
		t.Fatalf("AnswerAskUser() result = %+v; want request/session answer", result)
	}
	assertJSONDoesNotContain(t, result, "Postgres with read replicas", "Dark launch first")
}

func TestServerMutationTargetAnswerAskUserNormalizesTruncatedQuestionKey(t *testing.T) {
	fullQuestion := "Which persistence strategy should the orchestrator use when an AskUserQuestion contains enough detail that the read API truncates the display projection, but the provider still requires the exact original question text as the answer-map key?"
	truncatedQuestion := fullQuestion[:180] + "..."
	input, err := json.Marshal(map[string]any{
		"questions": []map[string]any{{
			"question": fullQuestion,
			"options": []map[string]string{{
				"label": labelUseFullInput,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	sess := &mutationTargetSessionView{
		id:        testSessionAskID,
		featureID: "feat-ask",
		phase:     feature.PhaseInquire,
		status:    ports.SessionWaitingHelp,
		active:    true,
		pending: []*llm.ControlRequestMessage{{
			Type:      wireTypeControlRequest,
			RequestID: testAskRequestID,
			Request: llm.ControlRequest{
				Subtype:  wireSubtypeCanUseTool,
				ToolName: toolNameAskUserQuestion,
				Input:    input,
			},
		}},
	}
	sessions := &mutationTargetSessionManager{sessions: []ports.SessionView{sess}}
	target := serverMutationTarget{
		orch:     mutationTargetOrchestrator(sessions),
		sessions: sessions,
	}

	_, err = target.AnswerAskUser(serverruntime.AskUserAnswerRequest{
		RequestID: testAskRequestID,
		SessionID: testSessionAskID,
		Answers: map[string]string{
			truncatedQuestion: labelUseFullInput,
		},
	})
	if err != nil {
		t.Fatalf("AnswerAskUser() error = %v", err)
	}

	if len(sess.askCalls) != 1 {
		t.Fatalf("RespondToAskUser calls = %d, want 1", len(sess.askCalls))
	}
	call := sess.askCalls[0]
	if got := call.answers[fullQuestion]; got != labelUseFullInput {
		t.Fatalf("RespondToAskUser answers[%q] = %q; want selected answer in %v", fullQuestion, got, call.answers)
	}
	if _, ok := call.answers[truncatedQuestion]; ok {
		t.Fatalf("RespondToAskUser kept truncated question key %q in %v", truncatedQuestion, call.answers)
	}
}

// TestServerMutationTargetStartFeatureBlocksChildren exercises the start /
// resume backend path (serverMutationTarget.StartFeature fronts both routes)
// end-to-end through the real orchestrator and store: a child whose setup is
// queued, running, or failed returns ErrChildExecutionBlocked and never
// reports "started", while a setup-complete large-profile child is eligible
// to start.
func TestServerMutationTargetStartFeatureBlocksChildren(t *testing.T) {
	newChildTarget := func(t *testing.T, mutate func(*feature.Feature)) (serverMutationTarget, string) {
		t.Helper()
		runtimeDir := t.TempDir()
		cfg := config.NewDefault()
		cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
		store := feature.NewStore(filepath.Join(runtimeDir, "features"))
		manager := feature.NewManager(store, cfg)
		child := &feature.Feature{
			ID:            "child-blocked",
			Slug:          "child-blocked",
			Status:        feature.StatusCreated,
			ActiveRun:     1,
			RunCount:      1,
			Pipeline:      feature.PipelineMedium,
			SchemaVersion: feature.SchemaVersionCurrent,
			Repos: []feature.FeatureRepo{{
				Name:       testRepoAName,
				Path:       filepath.Join(runtimeDir, testRepoAName),
				Branch:     "feature/child-blocked",
				BaseBranch: "main",
			}},
			Parent: &feature.ChildRelationship{ParentID: "p-1", Kind: feature.ChildKindRefactor},
		}
		mutate(child)
		if err := store.Save(child); err != nil {
			t.Fatalf("save child: %v", err)
		}
		orch := orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})
		return serverMutationTarget{orch: orch, store: store}, child.ID
	}

	t.Run("queued child", func(t *testing.T) {
		target, childID := newChildTarget(t, func(f *feature.Feature) {
			f.Status = feature.StatusSettingUpWorktrees
		})
		resp, err := target.StartFeature(childID)
		if !errors.Is(err, feature.ErrChildExecutionBlocked) {
			t.Fatalf("StartFeature() error = %v; want ErrChildExecutionBlocked", err)
		}
		if resp.Result == resultStarted {
			t.Fatalf("StartFeature() response = %+v; must not report started for a child mid-setup", resp)
		}
	})

	t.Run("failed-setup child", func(t *testing.T) {
		target, childID := newChildTarget(t, func(f *feature.Feature) {
			f.Status = feature.StatusFailed
			// SetRun syncs run->shadows, so stamp shadow fields after it.
			f.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Status: feature.SetupStatusFailed}})
			f.FailureType = feature.FailureWorktreeSetup
		})
		resp, err := target.StartFeature(childID)
		if !errors.Is(err, feature.ErrChildExecutionBlocked) {
			t.Fatalf("StartFeature() error = %v; want ErrChildExecutionBlocked", err)
		}
		if resp.Result == resultStarted {
			t.Fatalf("StartFeature() response = %+v; must not report started for a failed-setup child", resp)
		}
	})

	t.Run("large-profile child is eligible to start", func(t *testing.T) {
		target, childID := newChildTarget(t, func(f *feature.Feature) {
			f.Pipeline = feature.PipelineLarge
		})
		resp, err := target.StartFeature(childID)
		if err != nil {
			t.Fatalf("StartFeature() error = %v; want nil for eligible large-profile child", err)
		}
		if resp.Result != resultStarted {
			t.Fatalf("StartFeature() response = %+v; want started for eligible large-profile child", resp)
		}
	})
}

// TestServerMutationTargetRefactorFeatureMapsBriefToSpec verifies the typed
// wizard brief maps onto a RefactorChildSpec and that the response carries
// the child identifier; setup dispatch stays asynchronous and is not driven
// by this unit-level target (orch is nil).
func TestServerMutationTargetRefactorFeatureMapsBriefToSpec(t *testing.T) {
	creator := &fakeRefactorChildCreator{child: &feature.Feature{ID: "child-1"}}
	target := serverMutationTarget{childCreator: creator}

	resp, err := target.RefactorFeature("parent-1", serverruntime.RefactorFeatureRequest{
		Name:         "Rework auth",
		Description:  "split the auth package",
		Images:       []string{"~/shots/login.png"},
		Attachments:  []string{"~/docs/auth.md"},
		Pipeline:     feature.PipelineLarge,
		Checkpoints:  feature.Checkpoints{InquiryReview: true},
		Effort:       config.EffortConfig{Planning: "high"},
		Models:       config.ModelConfig{Planning: testModelClaudeSonnet},
		RiskLevel:    feature.RiskLow,
		ExitCriteria: "build passes",
		Inquireness:  feature.InquirenessHigh,
	})
	if err != nil {
		t.Fatalf("RefactorFeature() error = %v", err)
	}
	if resp.FeatureID != "child-1" || resp.ParentID != "parent-1" || resp.Result != resultCreated {
		t.Fatalf("RefactorFeature() response = %+v; want child-1 under parent-1 created", resp)
	}
	if creator.parentID != "parent-1" {
		t.Fatalf("CreateRefactorChild parentID = %q, want parent-1", creator.parentID)
	}
	spec := creator.spec
	if spec.Name != "Rework auth" || spec.Description != "split the auth package" {
		t.Fatalf("spec name/description = %q/%q", spec.Name, spec.Description)
	}
	if spec.Pipeline != feature.PipelineLarge || spec.RiskLevel != feature.RiskLow {
		t.Fatalf("spec pipeline/risk = %q/%q, want large/low", spec.Pipeline, spec.RiskLevel)
	}
	if !spec.Checkpoints.InquiryReview {
		t.Fatalf("spec checkpoints = %+v, want inquiry review", spec.Checkpoints)
	}
	if spec.Effort.Planning != "high" || spec.Models.Planning != testModelClaudeSonnet {
		t.Fatalf("spec effort/models planning = %q/%q", spec.Effort.Planning, spec.Models.Planning)
	}
	if spec.ExitCriteria != "build passes" || spec.Inquireness != feature.InquirenessHigh {
		t.Fatalf("spec exit criteria/inquireness = %q/%q", spec.ExitCriteria, spec.Inquireness)
	}
	if len(spec.Images) != 1 || len(spec.Attachments) != 1 {
		t.Fatalf("spec images/attachments = %v/%v, want one each", spec.Images, spec.Attachments)
	}
}

// TestServerMutationTargetRefactorFeatureInheritsEmptyPipeline verifies an
// empty pipeline brief leaves the spec pipeline unset so the child inherits
// the parent profile.
func TestServerMutationTargetRefactorFeatureInheritsEmptyPipeline(t *testing.T) {
	creator := &fakeRefactorChildCreator{child: &feature.Feature{ID: "child-2"}}
	target := serverMutationTarget{childCreator: creator}

	if _, err := target.RefactorFeature("parent-1", serverruntime.RefactorFeatureRequest{Name: "Rework auth"}); err != nil {
		t.Fatalf("RefactorFeature() error = %v", err)
	}
	if creator.spec.Pipeline != "" {
		t.Fatalf("spec pipeline = %q; want empty so the child inherits", creator.spec.Pipeline)
	}
}

func TestServerMutationTargetRefactorFeatureSurfacesLaunchErrors(t *testing.T) {
	creator := &fakeRefactorChildCreator{err: &feature.ParentWorktreesDirtyError{
		Repos: []feature.RepoDirtyDiagnostics{{Repo: testRepoAName}},
	}}
	target := serverMutationTarget{childCreator: creator}

	_, err := target.RefactorFeature("parent-1", serverruntime.RefactorFeatureRequest{Name: "Rework auth"})
	var dirty *feature.ParentWorktreesDirtyError
	if !errors.As(err, &dirty) {
		t.Fatalf("RefactorFeature() error = %v, want ParentWorktreesDirtyError", err)
	}
}

type fakeRefactorChildCreator struct {
	parentID string
	spec     feature.RefactorChildSpec
	child    *feature.Feature
	err      error
}

func (f *fakeRefactorChildCreator) CreateRefactorChild(parentID string, spec feature.RefactorChildSpec) (*feature.Feature, error) {
	f.parentID = parentID
	f.spec = spec
	return f.child, f.err
}

func TestServerMutationTargetReviewFeedbackFeaturePreservesPayloadAndGatePresence(t *testing.T) {
	t.Parallel()

	explicitFalse := false
	tests := []struct {
		name string
		gate *bool
	}{
		{name: "omitted gate inherits", gate: nil},
		{name: "explicit false overrides", gate: &explicitFalse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			creator := &fakeReviewFeedbackChildCreator{child: &feature.Feature{ID: "child-review"}}
			target := serverMutationTarget{reviewFeedbackCreator: creator}
			comments := []feature.ReviewFeedbackComment{{
				Repo: "api", ID: 17, Type: "review", Path: "handler.go", Line: 42,
				Author: "alice", Body: "handle this", DiffHunk: "@@ -41 +41,2 @@", InReplyTo: 9,
			}}

			resp, err := target.ReviewFeedbackFeature("parent-1", serverruntime.ReviewFeedbackFeatureRequest{
				Comments: comments,
				Gate:     tt.gate,
			})
			if err != nil {
				t.Fatalf("ReviewFeedbackFeature() error = %v", err)
			}
			if resp.FeatureID != "child-review" || resp.ParentID != "parent-1" || resp.Result != resultCreated {
				t.Fatalf("ReviewFeedbackFeature() = %+v; want created child reference", resp)
			}
			if creator.parentID != "parent-1" || !reflect.DeepEqual(creator.spec.Comments, comments) {
				t.Fatalf("CreateReviewFeedbackChild args = parent:%q comments:%+v", creator.parentID, creator.spec.Comments)
			}
			if creator.spec.GateEnabled != tt.gate {
				t.Fatalf("gate pointer = %p; want %p", creator.spec.GateEnabled, tt.gate)
			}
		})
	}
}

type fakeReviewFeedbackChildCreator struct {
	parentID string
	spec     feature.ReviewFeedbackChildSpec
	child    *feature.Feature
	err      error
}

func (f *fakeReviewFeedbackChildCreator) CreateReviewFeedbackChild(parentID string, spec feature.ReviewFeedbackChildSpec) (*feature.Feature, error) {
	f.parentID = parentID
	f.spec = spec
	return f.child, f.err
}

func TestServerMutationTargetSendHelpSendsUserMessageToAddressedActiveSession(t *testing.T) {
	sess := &mutationTargetSessionView{
		id:        testSessionHelpID,
		featureID: testFeatureHelpID,
		phase:     feature.PhaseImplement,
		status:    ports.SessionWaitingHelp,
		active:    true,
	}
	sessions := &mutationTargetSessionManager{sessions: []ports.SessionView{sess}}
	target := serverMutationTarget{
		orch:     mutationTargetOrchestrator(sessions),
		sessions: sessions,
	}

	result, err := target.SendHelp(serverruntime.HelpAnswerRequest{
		FeatureID: testFeatureHelpID,
		SessionID: testSessionHelpID,
		Message:   "Please use the existing migration path.",
	})
	if err != nil {
		t.Fatalf("SendHelp() error = %v", err)
	}

	if !reflect.DeepEqual(sess.sentMessages, []string{"Please use the existing migration path."}) {
		t.Fatalf("SendUserMessage calls = %v, want addressed help text", sess.sentMessages)
	}
	if result.FeatureID != testFeatureHelpID || result.SessionID != testSessionHelpID || result.Result != resultSent {
		t.Fatalf("SendHelp() result = %+v; want feature/session sent", result)
	}
	assertJSONDoesNotContain(t, result, "Please use the existing migration path.")
}

func TestServerMutationTargetSendHelpAnswersFeatureHelpQueueWhenNoSessionIsActive(t *testing.T) {
	store, _, f := newMutationTestFeature(t, "help queue via REST", feature.CreateOptions{}, feature.StatusImplementing, feature.PhaseImplement)
	requestedAt := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.HelpQueue = []feature.HelpRequest{{
			Question: "Which packaged evidence path should continue?",
			Time:     requestedAt,
			Pending:  true,
		}}
		return nil
	}); err != nil {
		t.Fatalf("seed help queue: %v", err)
	}
	target := serverMutationTarget{store: store}

	result, err := target.SendHelp(serverruntime.HelpAnswerRequest{
		FeatureID: f.ID,
		Message:   "Continue from the feature cockpit.",
	})
	if err != nil {
		t.Fatalf("SendHelp() error = %v", err)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if len(loaded.HelpQueue) != 1 {
		t.Fatalf("HelpQueue length = %d, want 1", len(loaded.HelpQueue))
	}
	if loaded.HelpQueue[0].Pending {
		t.Fatal("HelpQueue[0].Pending = true, want false")
	}
	if got := loaded.HelpQueue[0].Answer; got != "Continue from the feature cockpit." {
		t.Fatalf("HelpQueue[0].Answer = %q, want cockpit reply", got)
	}
	if result.FeatureID != f.ID || result.SessionID != "" || result.Result != resultSent {
		t.Fatalf("SendHelp() result = %+v; want feature-scoped sent", result)
	}
	assertJSONDoesNotContain(t, result, "Continue from the feature cockpit.")
}

func TestServerMutationTargetStartChatStartsInteractiveUtilitySessionWithoutSubagents(t *testing.T) {
	runtimeRoot := t.TempDir()
	stateDir := filepath.Join(runtimeRoot, "features")
	skillsDir := filepath.Join(runtimeRoot, "skills")
	configPath := filepath.Join(runtimeRoot, "config.yaml")
	var captured []agent.BuildSessionOpts
	phaseRunner := &agent.PhaseRunner{
		StateDir:  stateDir,
		SkillsDir: skillsDir,
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			captured = append(captured, opts)
			return []string{"agent"}, []string{"AGENT_TEST=1"}, &ports.SessionOpts{}, nil
		},
	}
	cfg := config.NewDefault()
	cfg.Defaults.Models.Utilities = "cheap-chat"
	sessions := &mutationTargetSessionManager{}
	target := serverMutationTarget{
		cfg:          cfg,
		configPath:   configPath,
		sessions:     sessions,
		phaseRunner:  phaseRunner,
		workspaceDir: testWorkspaceDir,
	}

	result, err := target.StartChat(serverruntime.ChatStartRequest{Message: "What is running?"})
	if err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}

	if result.SessionID != serverChatSessionID || result.Result != resultStarted {
		t.Fatalf("StartChat() result = %+v, want chat session started", result)
	}
	if len(captured) != 1 {
		t.Fatalf("BuildSession calls = %d, want 1", len(captured))
	}
	build := captured[0]
	if build.Model != "cheap-chat" || build.WorkDir != testWorkspaceDir || build.Phase != utilskill.PhaseAll || build.TurnMode != ports.TurnModeInteractive {
		t.Fatalf("BuildSession opts = %+v, want utility interactive chat in workspace", build)
	}
	if !build.Interactive {
		t.Fatalf("BuildSession Interactive = false, want true so text-parsed AskUserQuestion providers skip the whole picker-synthesis pipeline for AMA")
	}
	if build.EffortLevel != llm.EffortLow {
		t.Fatalf("BuildSession EffortLevel = %q, want low for AMA utility chat", build.EffortLevel)
	}
	if !reflect.DeepEqual(build.DisallowedTools, []string{"Task"}) {
		t.Fatalf("BuildSession DisallowedTools = %v, want only Task disabled for AMA", build.DisallowedTools)
	}
	if _, ok := build.PermHandler.(*permission.AMAHandler); !ok {
		t.Fatalf("BuildSession PermHandler = %T, want *permission.AMAHandler", build.PermHandler)
	}
	for _, want := range []string{
		"Agentic Orchestrator Expert Assistant",
		"Answer directly whenever the user's request is clear enough",
		filepath.Join(skillsDir, chatName, "SKILL.md"),
		"Runtime root: `" + runtimeRoot + "`",
		"Feature state directory: `" + stateDir + "`",
		"Config file: `" + configPath + "`",
		"Workspace: `" + testWorkspaceDir + "`",
		"Do not substitute the default paths from the user guide",
	} {
		if !strings.Contains(build.SystemPrompt, want) {
			t.Fatalf("BuildSession SystemPrompt missing %q:\n%s", want, build.SystemPrompt)
		}
	}
	if !strings.Contains(build.Prompt, "What is running?") || !strings.Contains(build.Prompt, filepath.Join(skillsDir, chatName, "SKILL.md")) {
		t.Fatalf("BuildSession prompt = %q, want chat skill instruction and user message", build.Prompt)
	}
	if len(sessions.startCalls) != 1 {
		t.Fatalf("StartSession calls = %d, want 1", len(sessions.startCalls))
	}
	start := sessions.startCalls[0]
	if start.id != serverChatSessionID || start.featureID != serverChatSessionID || start.phase != feature.PhaseResearch || start.workdir != testWorkspaceDir {
		t.Fatalf("StartSession call = %+v, want chat utility identity and research session in workspace", start)
	}
	if start.opts == nil || start.opts.Kind != ports.KindChat || start.opts.TurnMode != ports.TurnModeInteractive || start.opts.Label != chatName || start.opts.InitialPrompt != "What is running?" {
		t.Fatalf("StartSession opts = %+v, want chat-kind interactive session with the user-visible prompt", start.opts)
	}
	if start.opts.StderrPath != filepath.Join(stateDir, chatName, "stderr.log") {
		t.Fatalf("StartSession StderrPath = %q, want chat stderr capture", start.opts.StderrPath)
	}
	assertJSONDoesNotContain(t, result, "What is running?")
}

func TestChatMessageWithImagesAddsInspectableLocalPaths(t *testing.T) {
	got := chatMessageWithImages("What is shown?", []string{"/tmp/screenshot one.png", "/tmp/detail.png"})
	for _, want := range []string{
		"What is shown?",
		"Attached images (inspect these local files):",
		`"/tmp/screenshot one.png"`,
		`"/tmp/detail.png"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("chatMessageWithImages() missing %q:\n%s", want, got)
		}
	}
	if plain := chatMessageWithImages("No image", nil); plain != "No image" {
		t.Fatalf("chatMessageWithImages() without images = %q, want unchanged message", plain)
	}
}

func TestServerMutationTargetEndChatStopsOnlySingletonChat(t *testing.T) {
	sessions := &mutationTargetSessionManager{
		sessions: []ports.SessionView{
			&mutationTargetSessionView{id: "feature-session", featureID: "feature-1", status: ports.SessionRunning, active: true},
			&mutationTargetSessionView{id: serverChatSessionID, featureID: serverChatSessionID, status: ports.SessionRunning, active: true},
		},
	}
	target := serverMutationTarget{sessions: sessions}

	result, err := target.EndChat()
	if err != nil {
		t.Fatalf("EndChat() error = %v", err)
	}

	if result.SessionID != serverChatSessionID || result.Result != "ended" {
		t.Fatalf("EndChat() = %+v, want singleton chat ended", result)
	}
	if !reflect.DeepEqual(sessions.stopCalls, []string{serverChatSessionID}) {
		t.Fatalf("StopSession calls = %v, want only singleton chat", sessions.stopCalls)
	}
}

func TestServerMutationTargetEndChatIsIdempotentWhenInactive(t *testing.T) {
	sessions := &mutationTargetSessionManager{
		sessions: []ports.SessionView{
			&mutationTargetSessionView{id: serverChatSessionID, featureID: serverChatSessionID, status: ports.SessionDone, active: false},
		},
	}
	target := serverMutationTarget{sessions: sessions}

	result, err := target.EndChat()
	if err != nil {
		t.Fatalf("EndChat() error = %v", err)
	}

	if result.SessionID != serverChatSessionID || result.Result != "not_active" {
		t.Fatalf("EndChat() = %+v, want not_active singleton chat", result)
	}
	if len(sessions.stopCalls) != 0 {
		t.Fatalf("StopSession calls = %v, want none", sessions.stopCalls)
	}
}

func TestServerMutationTargetDraftNeedUserInputAnswersUpdatesPendingArtifactByPromptAndIndex(t *testing.T) {
	gatePath := filepath.Join(t.TempDir(), agent.NeedUserInputArtifactName)
	original := agent.NeedUserInputRecord{
		Summary:   "Implementation is blocked on product choices.",
		Iteration: 3,
		Questions: []agent.NeedUserInputQuestion{
			{Index: 1, Prompt: "Which database should back search?"},
			{Prompt: "How should rollout be staged?"},
		},
	}
	if err := agent.WriteNeedUserInputRecord(gatePath, original); err != nil {
		t.Fatalf("WriteNeedUserInputRecord() error = %v", err)
	}

	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	manager := feature.NewManager(store, cfg)
	f := &feature.Feature{
		ID:                       "feat-need-input",
		Name:                     "Need input",
		Slug:                     "need-input",
		Status:                   feature.StatusNeedUserInput,
		PendingNeedUserInputPath: gatePath,
		SchemaVersion:            feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save feature error = %v", err)
	}
	target := serverMutationTarget{
		orch:  orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		cfg:   cfg,
		store: store,
	}

	result, err := target.DraftNeedUserInputAnswers("feat-need-input", serverruntime.NeedUserInputDraftRequest{
		Answers: map[string]string{
			"Which database should back search?": "Use Postgres first.",
			"2":                                  "Start with one internal workspace.",
		},
	})
	if err != nil {
		t.Fatalf("DraftNeedUserInputAnswers() error = %v", err)
	}

	updated, err := agent.ReadNeedUserInputRecord(gatePath)
	if err != nil {
		t.Fatalf("ReadNeedUserInputRecord() error = %v", err)
	}
	if updated.Summary != original.Summary {
		t.Fatalf("Summary = %q, want unchanged %q", updated.Summary, original.Summary)
	}
	if updated.Iteration != original.Iteration {
		t.Fatalf("Iteration = %d, want unchanged %d", updated.Iteration, original.Iteration)
	}
	if updated.Questions[0].Prompt != original.Questions[0].Prompt || updated.Questions[1].Prompt != original.Questions[1].Prompt {
		t.Fatalf("Prompts changed: got %+v, want %+v", updated.Questions, original.Questions)
	}
	if updated.Questions[0].Answer != "Use Postgres first." || updated.Questions[1].Answer != "Start with one internal workspace." {
		t.Fatalf("Answers = %+v, want prompt and index updates", updated.Questions)
	}
	if result.FeatureID != "feat-need-input" || result.Result != "drafted" {
		t.Fatalf("DraftNeedUserInputAnswers() result = %+v; want drafted feature", result)
	}
}

func TestServerMutationTargetRuntimeConfigPersistsAllowedDefaultsChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, ".agentic-orchestrator", defaultConfigBasename)
	cfg := config.NewDefault()
	cfg.Defaults.Models.Research = "old-research"
	cfg.Defaults.Models.Implementation = "old-implementation"
	cfg.Defaults.MaxIterations = 3
	cfg.Defaults.Inquireness = "none"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config error = %v", err)
	}
	target := serverMutationTarget{cfg: cfg, configPath: configPath}

	checkpoints := config.Checkpoints{
		RoadmapReview:   true,
		PhasePlanReview: true,
	}
	result, err := target.RuntimeConfig(serverruntime.RuntimeConfigMutationRequest{
		Defaults: serverruntime.RuntimeDefaultsMutation{
			Models: &serverruntime.ModelConfigPatch{
				Research:       testResearchModelNew,
				Implementation: testImplementationModelNew,
			},
			Inquireness:   testInquirenessHigh,
			MaxIterations: 8,
			Checkpoints:   &checkpoints,
		},
	})
	if err != nil {
		t.Fatalf("RuntimeConfig() error = %v", err)
	}

	if cfg.Defaults.Models.Research != testResearchModelNew {
		t.Fatalf("in-memory research model = %q, want new-research", cfg.Defaults.Models.Research)
	}
	if cfg.Defaults.Models.Implementation != testImplementationModelNew {
		t.Fatalf("in-memory implementation model = %q, want new-implementation", cfg.Defaults.Models.Implementation)
	}
	if cfg.Defaults.MaxIterations != 8 || cfg.Defaults.Inquireness != testInquirenessHigh || !cfg.Defaults.Checkpoints.RoadmapReview || !cfg.Defaults.Checkpoints.PhasePlanReview {
		t.Fatalf("in-memory defaults = %+v, want requested changes", cfg.Defaults)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load config error = %v", err)
	}
	if loaded.Defaults.Models.Research != testResearchModelNew ||
		loaded.Defaults.Models.Implementation != testImplementationModelNew ||
		loaded.Defaults.MaxIterations != 8 ||
		loaded.Defaults.Inquireness != testInquirenessHigh ||
		!loaded.Defaults.Checkpoints.RoadmapReview ||
		!loaded.Defaults.Checkpoints.PhasePlanReview {
		t.Fatalf("persisted defaults = %+v, want requested changes", loaded.Defaults)
	}
	if result.Result != resultUpdated {
		t.Fatalf("RuntimeConfig() result = %+v; want updated", result)
	}
}

func TestServerMutationTargetRuntimeConfigCanDisableAllCheckpoints(t *testing.T) {
	runtimeDir := t.TempDir()
	configPath := filepath.Join(runtimeDir, "config.yaml")
	cfg := config.NewDefault()
	cfg.Defaults.Checkpoints = config.Checkpoints{
		InquiryReview:   true,
		ResearchReview:  true,
		DesignReview:    true,
		RoadmapReview:   true,
		PhasePlanReview: true,
		ManualPublish:   true,
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config error = %v", err)
	}
	target := serverMutationTarget{cfg: cfg, configPath: configPath}
	disabled := config.Checkpoints{}

	result, err := target.RuntimeConfig(serverruntime.RuntimeConfigMutationRequest{
		Defaults: serverruntime.RuntimeDefaultsMutation{Checkpoints: &disabled},
	})
	if err != nil {
		t.Fatalf("RuntimeConfig() error = %v", err)
	}
	if cfg.Defaults.Checkpoints != disabled {
		t.Fatalf("in-memory checkpoints = %+v, want all disabled", cfg.Defaults.Checkpoints)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load config error = %v", err)
	}
	if checkpointsEnabled(loaded.Defaults.Checkpoints) {
		t.Fatalf("persisted checkpoints = %+v, want all disabled", loaded.Defaults.Checkpoints)
	}
	if result.Result != resultUpdated {
		t.Fatalf("RuntimeConfig() result = %+v; want updated", result)
	}
}

func checkpointsEnabled(checkpoints config.Checkpoints) bool {
	return checkpoints.InquiryReview ||
		checkpoints.ResearchReview ||
		checkpoints.DesignReview ||
		checkpoints.RoadmapReview ||
		checkpoints.PhasePlanReview ||
		checkpoints.ManualPublish ||
		checkpoints.DraftPublish
}

func TestServerMutationTargetRuntimeConfigPersistsWorkspaceRootsAndDiscoversRepos(t *testing.T) {
	runtimeDir := t.TempDir()
	configPath := filepath.Join(runtimeDir, "config.yaml")
	root := filepath.Join(runtimeDir, "workspace")
	if err := os.MkdirAll(filepath.Join(root, "api", ".git"), 0o755); err != nil {
		t.Fatalf("create repo fixture: %v", err)
	}
	cfg := config.NewDefault()
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config error = %v", err)
	}
	target := serverMutationTarget{cfg: cfg, configPath: configPath}
	roots := []string{root}

	result, err := target.RuntimeConfig(serverruntime.RuntimeConfigMutationRequest{
		WorkspaceRoots: &roots,
	})
	if err != nil {
		t.Fatalf("RuntimeConfig() error = %v", err)
	}

	if len(cfg.WorkspaceRoots) != 1 || cfg.WorkspaceRoots[0] != root {
		t.Fatalf("in-memory workspace roots = %+v, want %q", cfg.WorkspaceRoots, root)
	}
	if got := cfg.DiscoveredRepos["api"].Path; got != filepath.Join(root, "api") {
		t.Fatalf("discovered api path = %q, want repo under workspace root", got)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load config error = %v", err)
	}
	if len(loaded.WorkspaceRoots) != 1 || loaded.WorkspaceRoots[0] != root {
		t.Fatalf("persisted workspace roots = %+v, want %q", loaded.WorkspaceRoots, root)
	}
	if result.Result != resultUpdated {
		t.Fatalf("RuntimeConfig() result = %+v; want updated", result)
	}
}

func TestServerMutationTargetRuntimeConfigRediscoverReposWhenWorkspaceRootsUnchanged(t *testing.T) {
	runtimeDir := t.TempDir()
	configPath := filepath.Join(runtimeDir, "config.yaml")
	root := filepath.Join(runtimeDir, "workspace")
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{root}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "new-service", ".git"), 0o755); err != nil {
		t.Fatalf("create repo fixture: %v", err)
	}
	target := serverMutationTarget{cfg: cfg, configPath: configPath}
	roots := []string{root}

	result, err := target.RuntimeConfig(serverruntime.RuntimeConfigMutationRequest{
		WorkspaceRoots: &roots,
	})
	if err != nil {
		t.Fatalf("RuntimeConfig() error = %v", err)
	}

	if got := cfg.DiscoveredRepos["new-service"].Path; got != filepath.Join(root, "new-service") {
		t.Fatalf("discovered new-service path = %q, want repo under unchanged workspace root", got)
	}
	if result.Result != "unchanged" {
		t.Fatalf("RuntimeConfig() result = %+v; want unchanged", result)
	}
}

func TestServerMutationTargetCreateFeaturePersistsSelectedRESTOptions(t *testing.T) {
	runtimeDir := t.TempDir()
	configPath := filepath.Join(runtimeDir, "config.yaml")
	stateDir := filepath.Join(runtimeDir, "features")
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
	cfg.Defaults.Models = config.ModelConfig{
		Research:       "default-research",
		Planning:       testPlanningModelDefault,
		Implementation: "default-implementation",
		Review:         "default-review",
		Utilities:      "default-utilities",
		KBBuild:        "default-kb",
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config error = %v", err)
	}
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, cfg)
	target := newRESTCreateFeatureTarget(store, manager, cfg, configPath)
	attachment := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(attachment, []byte("rest attachment"), 0o644); err != nil {
		t.Fatalf("WriteFile attachment: %v", err)
	}

	result, err := target.CreateFeature(serverruntime.CreateFeatureRequest{
		Name:         "REST durable options",
		Description:  "create via REST",
		Repos:        []string{testRepoAName},
		ExitCriteria: "all acceptance checks pass",
		Inquireness:  testInquirenessNone,
		Models: config.ModelConfig{
			Research:       "claude:opus",
			Implementation: testModelCodexGPT54,
			KBBuild:        "claude:haiku",
		},
		Attachments:             []string{attachment},
		UseCurrentBranch:        true,
		UseCurrentBranchPerRepo: map[string]bool{testRepoAName: true},
		RiskLevel:               feature.RiskHigh,
		Pipeline:                feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{
			InquiryReview:   true,
			RoadmapReview:   true,
			PhasePlanReview: true,
			ManualPublish:   false,
		},
	})
	if err != nil {
		t.Fatalf("CreateFeature() error = %v", err)
	}
	featureID := result.FeatureID
	if featureID == "" {
		t.Fatalf("result = %+v; want feature_id", result)
	}

	created, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("Load created feature: %v", err)
	}
	if created.Description != "create via REST" || created.ExitCriteria != "all acceptance checks pass" || created.Inquireness != feature.InquirenessNone {
		t.Fatalf("created feature text config = desc:%q exit:%q inq:%q", created.Description, created.ExitCriteria, created.Inquireness)
	}
	if created.Models.Research != "claude:opus" || created.Models.Planning != testPlanningModelDefault ||
		created.Models.Implementation != testModelCodexGPT54 || created.Models.KBBuild != "claude:haiku" {
		t.Fatalf("created feature models = %+v; want REST selections over defaults", created.Models)
	}
	if created.Pipeline != feature.PipelineMedium || created.CurrentPhase != feature.PhasePlan || created.RiskLevel != feature.RiskHigh {
		t.Fatalf("created feature pipeline/current/risk = %s/%s/%s; want medium/plan/high", created.Pipeline, created.CurrentPhase, created.RiskLevel)
	}
	wantCheckpoints := feature.PipelineMedium.ProjectGates(feature.Checkpoints{
		InquiryReview:   true,
		RoadmapReview:   true,
		PhasePlanReview: true,
		ManualPublish:   false,
	}, true).Checkpoints
	if created.Checkpoints != wantCheckpoints {
		t.Fatalf("created checkpoints = %+v, want normalized %+v", created.Checkpoints, wantCheckpoints)
	}
	if created.Status != feature.StatusSettingUpWorktrees {
		t.Fatalf("created status = %s; want SettingUpWorktrees", created.Status)
	}
	if len(created.Attachments) != 0 {
		t.Fatalf("created attachments = %v; want attachment queued for setup, not copied during create", created.Attachments)
	}
	setup := created.Run().Setup
	if setup == nil || setup.Status != feature.SetupStatusRunning {
		t.Fatalf("created setup = %+v; want active setup state", setup)
	}
	attachmentTask := setup.Tasks["attachment:1"]
	if attachmentTask.Kind != feature.SetupTaskAttachment || attachmentTask.Status != feature.SetupStatusQueued || attachmentTask.SourcePath != attachment {
		t.Fatalf("attachment setup task = %+v; want queued task from REST attachment", attachmentTask)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	pref := loaded.Defaults.PipelinePreferences["medium"]
	if pref.Models.Implementation != testModelCodexGPT54 || pref.Models.Planning != testPlanningModelDefault || pref.Inquireness != testInquirenessNone {
		t.Fatalf("persisted pipeline preference = %+v; want REST model/inquireness selections", pref)
	}
	gates := loaded.Repos[testRepoAName].PipelineGates["medium"]
	if gates.InquiryReview || !gates.RoadmapReview || !gates.PhasePlanReview || gates.ManualPublish {
		t.Fatalf("persisted repo gates = %+v; want normalized medium gates with manual publish false", gates)
	}
}

func TestServerMutationTargetCreateFeatureResolvesBlankExplicitRepoFromWorkspaceRoots(t *testing.T) {
	runtimeDir := t.TempDir()
	configPath := filepath.Join(runtimeDir, "config.yaml")
	stateDir := filepath.Join(runtimeDir, "features")
	workspaceRoot := filepath.Join(runtimeDir, "workspace")
	repoPath := filepath.Join(workspaceRoot, "bpfagent")
	if err := os.MkdirAll(filepath.Join(repoPath, ".git"), 0o755); err != nil {
		t.Fatalf("create repo fixture: %v", err)
	}
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{workspaceRoot}
	cfg.Repos["bpfagent"] = config.RepoConfig{
		PipelineGates: map[string]config.Checkpoints{
			"medium": {ManualPublish: true},
		},
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config error = %v", err)
	}
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, cfg)
	target := newRESTCreateFeatureTarget(store, manager, cfg, configPath)

	result, err := target.CreateFeature(serverruntime.CreateFeatureRequest{
		Name:     "Cassandra Probe",
		Repos:    []string{"bpfagent"},
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("CreateFeature() error = %v", err)
	}

	created, err := store.Load(result.FeatureID)
	if err != nil {
		t.Fatalf("Load created feature: %v", err)
	}
	if got := created.Repos[0].Path; got != repoPath {
		t.Fatalf("created repo path = %q, want discovered path %q", got, repoPath)
	}
}

func TestServerMutationTargetCreateFeatureQueuesSetupWithoutWorktreeSideEffects(t *testing.T) {
	runtimeDir := t.TempDir()
	configPath := filepath.Join(runtimeDir, "config.yaml")
	stateDir := filepath.Join(runtimeDir, "features")
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config error = %v", err)
	}
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, cfg)
	worktrees := mocks.NewMockWorktreeOps()
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		return "", errors.New("worktree creation should be deferred to setup")
	}
	manager.Worktrees = worktrees
	target := newRESTCreateFeatureTarget(store, manager, cfg, configPath)

	result, err := target.CreateFeature(serverruntime.CreateFeatureRequest{
		Name:     "REST queued setup",
		Repos:    []string{testRepoAName},
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("CreateFeature() error = %v, want queued setup without worktree side effect", err)
	}

	created, err := store.Load(result.FeatureID)
	if err != nil {
		t.Fatalf("Load created feature: %v", err)
	}
	if created.Status != feature.StatusSettingUpWorktrees {
		t.Fatalf("created status = %s, want SettingUpWorktrees", created.Status)
	}
	setup := created.Run().Setup
	if setup == nil || setup.Status != feature.SetupStatusRunning {
		t.Fatalf("created setup = %+v, want active setup state", setup)
	}
	if len(worktrees.Calls) != 0 {
		t.Fatalf("worktree calls = %+v, want none during feature_create", worktrees.Calls)
	}
}

func TestServerMutationTargetSetupFeatureCompletesToStartableStateWithoutStarting(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	worktrees := mocks.NewMockWorktreeOps()
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		return filepath.Join(runtimeDir, "worktrees", featureSlug, repoName), nil
	}
	manager.Worktrees = worktrees

	started := 0
	target := serverMutationTarget{
		orch: orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{
			OnFeatureStarted: func(string) { started++ },
		}),
		store:         store,
		dispatchAsync: func(fn func()) { fn() },
	}
	f, err := manager.Create("Setup via REST action", "desc", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
		QueueSetup: true,
		Pipeline:   feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}

	result, err := target.SetupFeature(f.ID)
	if err != nil {
		t.Fatalf("SetupFeature() error = %v", err)
	}
	if result.Result != resultSetupStarted || result.FeatureID != f.ID {
		t.Fatalf("SetupFeature() = %+v; want setup_started", result)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if updated.Status != feature.StatusCreated {
		t.Fatalf("status = %s; want Created (Start enabled, not started)", updated.Status)
	}
	setup := updated.Run().Setup
	if setup == nil || setup.Status != feature.SetupStatusDone {
		t.Fatalf("setup = %+v; want done", setup)
	}
	if started != 0 {
		t.Fatalf("OnFeatureStarted fired %d times; want 0 — setup must not start orchestration", started)
	}

	// A second setup dispatch has nothing to do and reports a conflict.
	if _, err := target.SetupFeature(f.ID); err == nil {
		t.Fatal("SetupFeature() on completed setup error = nil; want conflict")
	}
}

func TestServerMutationTargetSetupFeatureRetriesOnlyUnfinishedWorkWithoutStarting(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
	cfg.Repos[testRepoBName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoBName)}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	failRepoB := true
	creates := 0
	worktrees := mocks.NewMockWorktreeOps()
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		creates++
		if repoName == testRepoBName && failRepoB {
			return "", errors.New("transient checkout failure")
		}
		return filepath.Join(runtimeDir, "worktrees", featureSlug, repoName), nil
	}
	manager.Worktrees = worktrees

	started := 0
	target := serverMutationTarget{
		orch: orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{
			OnFeatureStarted: func(string) { started++ },
		}),
		store:         store,
		dispatchAsync: func(fn func()) { fn() },
	}
	f, err := manager.Create("Retry setup via REST action", "desc", []string{testRepoAName, testRepoBName}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
		QueueSetup: true,
		Pipeline:   feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}

	if _, err := target.SetupFeature(f.ID); err != nil {
		t.Fatalf("SetupFeature() dispatch error = %v; want dispatch success with durable failure", err)
	}
	failed, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load failed feature: %v", err)
	}
	if failed.Status != feature.StatusFailed || failed.FailureType != feature.FailureWorktreeSetup {
		t.Fatalf("failed feature = %s/%s; want Failed/worktree_setup with preserved state", failed.Status, failed.FailureType)
	}
	if failedSetup := failed.Run().Setup; failedSetup == nil || failedSetup.LastError == "" {
		t.Fatalf("failed setup = %+v; want durable last_error", failedSetup)
	}
	createsAfterFailure := creates

	failRepoB = false
	result, err := target.SetupFeature(f.ID)
	if err != nil {
		t.Fatalf("SetupFeature() retry error = %v", err)
	}
	if result.Result != resultSetupStarted {
		t.Fatalf("retry result = %+v; want setup_started", result)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load retried feature: %v", err)
	}
	if updated.Status != feature.StatusCreated {
		t.Fatalf("status = %s; want Created after retry without start", updated.Status)
	}
	setup := updated.Run().Setup
	if setup == nil || setup.Status != feature.SetupStatusDone || setup.Attempt != 2 {
		t.Fatalf("setup = %+v; want done on attempt 2", setup)
	}
	if creates-createsAfterFailure != 1 {
		t.Fatalf("retry worktree creates = %d; want only the previously failed task", creates-createsAfterFailure)
	}
	if started != 0 {
		t.Fatalf("OnFeatureStarted fired %d times; want 0 for setup retry", started)
	}
}

func TestServerMutationTargetRetryFeatureRoutesSetupFailureToSetupRetry(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	failWorktree := true
	worktrees := mocks.NewMockWorktreeOps()
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		if failWorktree {
			return "", errors.New("repo checkout missing")
		}
		return filepath.Join(runtimeDir, "worktrees", featureSlug, repoName), nil
	}
	manager.Worktrees = worktrees
	f, err := manager.Create("Retry setup via REST", "desc", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
		QueueSetup: true,
		Pipeline:   feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := manager.RunSetup(f.ID); err == nil {
		t.Fatal("RunSetup() error = nil, want initial setup failure")
	}
	failWorktree = false
	target := serverMutationTarget{
		orch:  orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		store: store,
	}

	result, err := target.RetryFeature(f.ID)
	if err != nil {
		t.Fatalf("RetryFeature() error = %v, want setup retry then first phase start", err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load retried feature: %v", err)
	}
	if result.Result != resultRetried || result.FeatureID != f.ID {
		t.Fatalf("RetryFeature() result = %+v, want retried feature", result)
	}
	if updated.Status != feature.StatusPlanning || updated.CurrentPhase != feature.PhasePlan {
		t.Fatalf("feature status/phase = %s/%s, want Planning/Plan after setup retry start", updated.Status, updated.CurrentPhase)
	}
	setup := updated.Run().Setup
	if setup == nil || setup.Status != feature.SetupStatusDone || setup.Attempt != 2 {
		t.Fatalf("setup = %+v, want done setup on retry attempt 2", setup)
	}
}

func TestServerMutationTargetRetryFeatureDispatchesFailedPhase(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("retry phase via REST", "desc", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	planPath := filepath.Join(runtimeDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n## Tasks\n\n**Repo:** repo-a\n\n- Retry the implementation.\n"), 0o644); err != nil {
		t.Fatalf("WriteFile plan: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusFailed
		ff.CurrentPhase = feature.PhaseImplement
		ff.FailureType = feature.FailureMaxIterations
		ff.MaxIterations = 10
		ff.MaxPlanIterations = 3
		ff.LastError = "no progress for 3 consecutive iterations"
		ff.Artifacts = map[string]string{"plan": planPath}
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}

	var dispatched []string
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: manager,
		Store:     store,
	}, orchestrator.Hooks{})
	orch.SetRunMultiRepoImplFn(func(f *feature.Feature, planPath string, _ ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		dispatched = append(dispatched, f.ID+":"+planPath)
		ch := make(chan *agent.OrchestratorResult)
		close(ch)
		return ch, nil
	})
	target := serverMutationTarget{orch: orch, store: store}

	result, err := target.RetryFeature(f.ID)
	if err != nil {
		t.Fatalf("RetryFeature() error = %v", err)
	}
	if len(dispatched) != 1 || dispatched[0] != f.ID+":"+planPath {
		t.Fatalf("phase dispatches = %v, want one implementation dispatch with plan path", dispatched)
	}
	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load retried feature: %v", err)
	}
	if updated.Status != feature.StatusImplementing || updated.CurrentPhase != feature.PhaseImplement {
		t.Fatalf("retried feature status/phase = %s/%s, want Implementing/Implement", updated.Status, updated.CurrentPhase)
	}
	if updated.MaxIterations != 20 {
		t.Fatalf("MaxIterations = %d, want 20", updated.MaxIterations)
	}
	if updated.MaxPlanIterations != 3 {
		t.Fatalf("MaxPlanIterations = %d, want unchanged 3 outside Plan", updated.MaxPlanIterations)
	}
	if result.FeatureID != f.ID || result.Result != resultRetried {
		t.Fatalf("RetryFeature() result = %+v, want retried feature", result)
	}
}

func TestRetryFeatureIterationDeltas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		feature  *feature.Feature
		wantMax  int
		wantPlan int
	}{
		{
			name: "max iterations",
			feature: &feature.Feature{
				Status:       feature.StatusFailed,
				FailureType:  feature.FailureMaxIterations,
				CurrentPhase: feature.PhasePlan,
			},
			wantMax:  10,
			wantPlan: 2,
		},
		{
			name: "other failure",
			feature: &feature.Feature{
				Status:       feature.StatusFailed,
				FailureType:  feature.FailureInfrastructure,
				CurrentPhase: feature.PhasePlan,
			},
		},
		{
			name: "stale failure type on active feature",
			feature: &feature.Feature{
				Status:       feature.StatusImplementing,
				FailureType:  feature.FailureMaxIterations,
				CurrentPhase: feature.PhaseImplement,
			},
		},
		{name: "missing feature"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotMax, gotPlan := retryFeatureIterationDeltas(tt.feature)
			if gotMax != tt.wantMax || gotPlan != tt.wantPlan {
				t.Fatalf("retryFeatureIterationDeltas() = (%d, %d), want (%d, %d)", gotMax, gotPlan, tt.wantMax, tt.wantPlan)
			}
		})
	}
}

func TestServerMutationTargetReviewDecisionRewindProceedsFromExistingRewind(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("rewind review via REST", "old desc", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{Pipeline: feature.PipelineMedium})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	pending := feature.PhasePlan
	if err := store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusDesignNeedsReview
		f.CurrentPhase = feature.PhaseDesign
		f.PendingReviewPhase = &pending
		f.IsRewind = true
		return nil
	}); err != nil {
		t.Fatalf("prepare rewind review: %v", err)
	}
	writePath := filepath.Join(store.BaseDir, f.ID, "description-review.md")
	if err := os.WriteFile(writePath, []byte("edited desc"), 0o644); err != nil {
		t.Fatalf("write description review: %v", err)
	}
	target := serverMutationTarget{
		orch:  orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		store: store,
	}

	if err := target.ReviewDecision(f.ID, serverruntime.ReviewDecisionRequest{
		Decision: "proceed",
		Phase:    phaseNamePlan,
		IsRewind: true,
	}); err != nil {
		t.Fatalf("ReviewDecision() error = %v", err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if updated.Description != "edited desc" {
		t.Fatalf("Description = %q; want edited desc", updated.Description)
	}
	if updated.IsRewind || updated.PendingReviewPhase != nil {
		t.Fatalf("rewind gate = is_rewind:%v pending:%v; want cleared", updated.IsRewind, updated.PendingReviewPhase)
	}
	if updated.Status != feature.StatusPlanning {
		t.Fatalf("Status = %s; want Planning", updated.Status)
	}
}

func TestServerMutationTargetUpdateFeatureConfigPersistsRuntimePreferences(t *testing.T) {
	runtimeDir := t.TempDir()
	configPath := filepath.Join(runtimeDir, "config.yaml")
	stateDir := filepath.Join(runtimeDir, "features")
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config error = %v", err)
	}
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("config via REST", "desc", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
		Pipeline:    feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(f *feature.Feature) error {
		publishable := true
		for i := range f.Repos {
			f.Repos[i].Publishable = &publishable
		}
		return nil
	}); err != nil {
		t.Fatalf("mark publishable: %v", err)
	}
	legacyProviderDir := filepath.Join(stateDir, "opencode", "managed-session")
	if err := os.MkdirAll(legacyProviderDir, 0o755); err != nil {
		t.Fatalf("create legacy provider state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyProviderDir, "opencode.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write legacy provider state: %v", err)
	}
	target := serverMutationTarget{
		orch:       orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		cfg:        cfg,
		configPath: configPath,
		store:      store,
	}
	automaticReviewMode := string(feature.AutomaticReviewEnabled)

	result, err := target.UpdateFeatureConfig(f.ID, serverruntime.FeatureConfigMutationRequest{
		Models: config.ModelConfig{
			Implementation: testModelClaudeSonnet,
			Review:         testModelCodexGPT54Mini,
		},
		Inquireness: testInquirenessHigh,
		Pipeline:    feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{
			InquiryReview:   true,
			DesignReview:    true,
			RoadmapReview:   true,
			PhasePlanReview: true,
			ManualPublish:   false,
		},
		InputNotifications:  string(feature.InputNotificationsMuted),
		AutomaticReviewMode: &automaticReviewMode,
	})
	if err != nil {
		t.Fatalf("UpdateFeatureConfig() error = %v", err)
	}
	if result.FeatureID != f.ID || result.Result != resultUpdated {
		t.Fatalf("UpdateFeatureConfig() result = %+v; want updated feature", result)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load updated feature: %v", err)
	}
	wantCheckpoints := feature.Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: false}
	if updated.Checkpoints != wantCheckpoints {
		t.Fatalf("updated feature checkpoints = %+v, want %+v", updated.Checkpoints, wantCheckpoints)
	}
	if updated.Models.Implementation != testModelClaudeSonnet || updated.Models.Review != testModelCodexGPT54Mini || updated.Inquireness != feature.InquirenessHigh {
		t.Fatalf("updated feature config = models:%+v inq:%q; want REST edit", updated.Models, updated.Inquireness)
	}
	if updated.InputNotifications != feature.InputNotificationsMuted {
		t.Fatalf("updated InputNotifications = %q, want muted override", updated.InputNotifications)
	}
	if got := feature.NormalizeAutomaticReviewMode(updated.AutomaticReviewMode); got != feature.AutomaticReviewEnabled {
		t.Fatalf("updated AutomaticReviewMode = %q, want enabled", got)
	}

	if _, err := target.UpdateFeatureConfig(f.ID, serverruntime.FeatureConfigMutationRequest{
		Models:             updated.Models,
		Effort:             updated.Effort,
		Inquireness:        string(updated.Inquireness),
		Pipeline:           updated.Pipeline,
		Checkpoints:        updated.Checkpoints,
		InputNotifications: string(updated.InputNotifications),
	}); err != nil {
		t.Fatalf("UpdateFeatureConfig() preserving omitted automatic review mode: %v", err)
	}
	preserved, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load preserved feature: %v", err)
	}
	if got := feature.NormalizeAutomaticReviewMode(preserved.AutomaticReviewMode); got != feature.AutomaticReviewEnabled {
		t.Fatalf("AutomaticReviewMode after omitted update = %q, want enabled", got)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	pref := loaded.Defaults.PipelinePreferences["medium"]
	if pref.Models.Implementation != testModelClaudeSonnet || pref.Models.Review != testModelCodexGPT54Mini || pref.Inquireness != testInquirenessHigh {
		t.Fatalf("persisted preference = %+v; want updated feature config", pref)
	}
	gates := loaded.Repos[testRepoAName].PipelineGates["medium"]
	if gates.InquiryReview || gates.DesignReview || !gates.RoadmapReview || !gates.PhasePlanReview || gates.ManualPublish {
		t.Fatalf("persisted repo gates = %+v; want normalized medium gates", gates)
	}
}

func TestServerMutationTargetClosedChildConfigReturnsRelationshipClosed(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	cfg := config.NewDefault()
	closedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	child := &feature.Feature{
		ID:            "closed-child",
		Name:          "Closed child",
		Slug:          "closed-child",
		Status:        feature.StatusDone,
		Pipeline:      feature.PipelineMedium,
		Models:        config.ModelConfig{Research: "old-research"},
		Inquireness:   feature.InquirenessMedium,
		Checkpoints:   feature.Checkpoints{RoadmapReview: true},
		SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID:     "parent",
			Kind:         feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeCompleted,
			ClosedAt:     &closedAt,
		},
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("Save closed child: %v", err)
	}
	manager := feature.NewManager(store, cfg)
	target := serverMutationTarget{
		orch:  orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		cfg:   cfg,
		store: store,
	}
	handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		DisableHostValidation: true,
		Features:              store,
		FeatureStore:          store,
		Config:                cfg,
		Mutations:             &target,
	})
	body, err := json.Marshal(serverruntime.FeatureConfigMutationRequest{
		Models:      config.ModelConfig{Research: "new-research"},
		Inquireness: testInquirenessHigh,
		Pipeline:    feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	})
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/features/"+child.ID+"/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var response serverruntime.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if response.Error.Code != "relationship_closed" {
		t.Fatalf("error code = %q, want relationship_closed", response.Error.Code)
	}
	loaded, err := store.Load(child.ID)
	if err != nil {
		t.Fatalf("Load closed child: %v", err)
	}
	if loaded.Models.Research != "old-research" || loaded.Inquireness != feature.InquirenessMedium || loaded.Checkpoints != (feature.Checkpoints{RoadmapReview: true}) {
		t.Fatalf("closed child config mutated: models=%+v inquireness=%q checkpoints=%+v", loaded.Models, loaded.Inquireness, loaded.Checkpoints)
	}
}

func TestServerMutationTargetPublishActionPublishesFeatureAndReturnsSafeMetadata(t *testing.T) {
	target, manager, store, f := newPublishActionTarget(t)
	target.orch.SetPublishRepoFn(func(featureID, repoName string) (string, error) {
		if featureID != f.ID || repoName != testRepoAName {
			t.Fatalf("publish repo call = %s/%s, want %s/repo-a", featureID, repoName, f.ID)
		}
		prURL := "https://github.com/acme/repo-a/pull/12"
		if err := manager.SetRepoPublished(featureID, repoName, prURL); err != nil {
			return "", err
		}
		return prURL, nil
	})

	result, err := target.PublishFeature(f.ID, serverruntime.PublishFeatureRequest{Repos: []string{testRepoAName}})
	if err != nil {
		t.Fatalf("publishAction() error = %v", err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if updated.Status != feature.StatusPublished {
		t.Fatalf("feature status = %s, want Published", updated.Status)
	}
	if result.FeatureID != f.ID || result.Result != "published" {
		t.Fatalf("PublishFeature() result = %+v; want published feature", result)
	}
	assertJSONDoesNotContain(t, result, "https://github.com/acme/repo-a/pull/12")
}

func TestServerMutationTargetPublishActionRecoversFailedPublish(t *testing.T) {
	target, manager, store, f := newPublishActionTarget(t)
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusFailed
		ff.LastError = "commit failed: index.lock already exists"
		ff.FailureType = feature.FailureInfrastructure
		return nil
	}); err != nil {
		t.Fatalf("mark feature failed: %v", err)
	}
	target.orch.SetPublishRepoFn(func(featureID, repoName string) (string, error) {
		prURL := "https://github.com/acme/repo-a/pull/13"
		if err := manager.SetRepoPublished(featureID, repoName, prURL); err != nil {
			return "", err
		}
		return prURL, nil
	})

	result, err := target.PublishFeature(f.ID, serverruntime.PublishFeatureRequest{Repos: []string{testRepoAName}})
	if err != nil {
		t.Fatalf("PublishFeature() error = %v", err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if updated.Status != feature.StatusPublished {
		t.Fatalf("feature status = %s, want Published", updated.Status)
	}
	if updated.LastError != "" || updated.FailureType != "" {
		t.Fatalf("terminal failure = (%q, %q), want cleared", updated.LastError, updated.FailureType)
	}
	if result.FeatureID != f.ID || result.Result != "published" {
		t.Fatalf("PublishFeature() result = %+v; want published feature", result)
	}
}

func TestServerMutationTargetPublishActionPreservesConflictRoutingMetadata(t *testing.T) {
	target, _, _, f := newPublishActionTarget(t)
	target.orch.SetPublishRepoFn(func(featureID, repoName string) (string, error) {
		return "", &orchestrator.PublishConflictError{
			RepoName:     repoName,
			Branch:       "feature/publish-conflict",
			RebaseTarget: "main",
		}
	})

	result, err := target.PublishFeature(f.ID, serverruntime.PublishFeatureRequest{})
	if err == nil {
		t.Fatal("publishAction() error = nil, want publish conflict")
	}
	var conflict *orchestrator.PublishConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("publishAction() error = %T %v, want PublishConflictError", err, err)
	}
	var actionConflict *serverruntime.ActionConflictError
	if !errors.As(err, &actionConflict) {
		t.Fatalf("publishAction() error = %T %v; want ActionConflictError", err, err)
	}
	if result.FeatureID != f.ID || result.Result != resultConflict {
		t.Fatalf("PublishFeature() result = %+v; want conflict feature", result)
	}
	assertTarget(t, actionConflict.Target, map[string]any{
		resultConflict:  phaseNamePublish,
		repoConflictKey: testRepoAName,
		"branch":        "feature/publish-conflict",
		"rebase_target": "main",
	})
}

func TestServerMutationTargetCompletionActionsRejectStaleSourceRevision(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*serverMutationTarget, string, string) (string, error)
	}{
		{
			name: "publish",
			run: func(target *serverMutationTarget, featureID, staleRevision string) (string, error) {
				result, err := target.PublishFeature(featureID, serverruntime.PublishFeatureRequest{
					SourceRevision: staleRevision,
					Repos:          []string{testRepoAName},
					Title:          "Publish completion",
				})
				return result.Result, err
			},
		},
		{
			name: "merge",
			run: func(target *serverMutationTarget, featureID, staleRevision string) (string, error) {
				result, err := target.MergeFeature(featureID, serverruntime.GuardedFeatureActionRequest{SourceRevision: staleRevision})
				return result.Result, err
			},
		},
		{
			name: "mark done",
			run: func(target *serverMutationTarget, featureID, staleRevision string) (string, error) {
				result, err := target.MarkDone(featureID, serverruntime.GuardedFeatureActionRequest{SourceRevision: staleRevision})
				return result.Result, err
			},
		},
		{
			name: "cleanup",
			run: func(target *serverMutationTarget, featureID, staleRevision string) (string, error) {
				result, err := target.CleanupFeature(featureID, serverruntime.CleanupActionRequest{
					SourceRevision: staleRevision,
					Target:         cleanupTargetWorktrees,
				})
				return result.Result, err
			},
		},
		{
			name: "delete",
			run: func(target *serverMutationTarget, featureID, staleRevision string) (string, error) {
				result, err := target.DeleteFeature(featureID, serverruntime.GuardedFeatureActionRequest{SourceRevision: staleRevision})
				if err != nil {
					return resultFailed, err
				}
				return string(result.Status), err
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			target, _, store, f := newPublishActionTarget(t)
			revision, err := target.orch.CompletionPreflightSourceRevision(f.ID)
			if err != nil {
				t.Fatalf("CompletionPreflightSourceRevision: %v", err)
			}
			if revision == "" {
				t.Fatal("source revision is empty")
			}
			if err := os.WriteFile(filepath.Join(f.Repos[0].Path, "drift.txt"), []byte("changed\n"), 0o644); err != nil {
				t.Fatalf("write drift file: %v", err)
			}

			result, err := tc.run(&target, f.ID, revision)
			if err == nil {
				t.Fatal("completion action error = nil; want stale preflight conflict")
			}
			var conflict *serverruntime.ActionConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("completion action error = %T %v; want ActionConflictError", err, err)
			}
			if result != resultFailed {
				t.Fatalf("result = %q; want %q", result, resultFailed)
			}
			if conflict.Target["reason"] != "stale_preflight" {
				t.Fatalf("conflict target = %+v; want stale_preflight reason", conflict.Target)
			}
			if _, err := store.Load(f.ID); err != nil {
				t.Fatalf("feature was mutated or deleted despite stale preflight: %v", err)
			}
		})
	}
}

func TestServerMutationTargetRewindActionReturnsEffectiveTargetMetadata(t *testing.T) {
	store, manager, f := newMutationTestFeature(t, "rewind via REST", feature.CreateOptions{
		Pipeline: feature.PipelineMedium,
	}, feature.StatusCodeReady, feature.PhasePublish)
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.RepoStates = map[string]*feature.RepoState{testRepoAName: {Touched: true}}
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
	target := serverMutationTarget{
		orch:  orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		store: store,
	}

	result, err := target.RewindFeature(f.ID, serverruntime.RewindFeatureRequest{TargetPhase: phaseNameImplement})
	if err != nil {
		t.Fatalf("rewindAction() error = %v", err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if updated.Status != feature.StatusPlanNeedsReview || !updated.IsRewind {
		t.Fatalf("rewound feature status/is_rewind = %s/%v, want PlanNeedsReview/true", updated.Status, updated.IsRewind)
	}
	if result.FeatureID != f.ID || result.TargetPhase != phaseNameImplement || result.EffectivePhase != phaseNameImplement || result.WarningCount != 0 {
		t.Fatalf("RewindFeature() result = %+v; want effective implement rewind", result)
	}
}

func TestServerMutationTargetRewindActionStopsSessionsBeforeRewind(t *testing.T) {
	store, manager, f := newMutationTestFeature(t, "rewind stops sessions", feature.CreateOptions{
		Pipeline: feature.PipelineMedium,
	}, feature.StatusImplementing, feature.PhaseImplement)
	sessions := &mutationTargetSessionManager{
		sessions: []ports.SessionView{&mutationTargetSessionView{
			id:        "session-rewind",
			featureID: f.ID,
			phase:     feature.PhaseImplement,
			status:    ports.SessionRunning,
			active:    true,
		}},
	}
	var statusAtStop feature.Status
	sessions.onStop = func(string) {
		loaded, err := store.Load(f.ID)
		if err != nil {
			t.Fatalf("Load during StopSession: %v", err)
		}
		statusAtStop = loaded.Status
	}
	target := serverMutationTarget{
		orch:  orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store, Sessions: sessions}, orchestrator.Hooks{}),
		store: store,
	}

	if _, err := target.RewindFeature(f.ID, serverruntime.RewindFeatureRequest{TargetPhase: phaseNamePlan}); err != nil {
		t.Fatalf("rewindAction() error = %v", err)
	}

	if got := strings.Join(sessions.stopCalls, ","); got != "session-rewind" {
		t.Fatalf("StopSession calls = %q, want session-rewind", got)
	}
	if statusAtStop != feature.StatusImplementing {
		t.Fatalf("feature status during StopSession = %s, want %s before rewind mutation", statusAtStop, feature.StatusImplementing)
	}
}

func TestServerMutationTargetRewindActionUpgradePipelineBranch(t *testing.T) {
	store, manager, f := newMutationTestFeature(t, "upgrade rewind via REST", feature.CreateOptions{
		Pipeline: feature.PipelineMedium,
	}, feature.StatusImplementing, feature.PhaseImplement)
	sessions := &mutationTargetSessionManager{
		sessions: []ports.SessionView{&mutationTargetSessionView{
			id:        "session-upgrade-rewind",
			featureID: f.ID,
			phase:     feature.PhaseImplement,
			status:    ports.SessionRunning,
			active:    true,
		}},
	}
	target := serverMutationTarget{
		orch:  orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store, Sessions: sessions}, orchestrator.Hooks{}),
		store: store,
	}

	result, err := target.RewindFeature(f.ID, serverruntime.RewindFeatureRequest{
		TargetPhase:     phaseNameInquire,
		UpgradePipeline: feature.PipelineLarge,
	})
	if err != nil {
		t.Fatalf("rewindAction() error = %v", err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if updated.Pipeline != feature.PipelineLarge || updated.CurrentPhase != feature.PhaseKnowledgeBase {
		t.Fatalf("feature pipeline/current phase = %s/%s, want large/knowledge base", updated.Pipeline, updated.CurrentPhase)
	}
	if got := strings.Join(sessions.stopCalls, ","); got != "session-upgrade-rewind" {
		t.Fatalf("StopSession calls = %q, want session-upgrade-rewind", got)
	}
	if result.FeatureID != f.ID || result.TargetPhase != phaseNameInquire || result.UpgradePipeline != "large" || result.EffectivePhase != feature.PhaseKnowledgeBase.DirName() || result.Result != "rewound" {
		t.Fatalf("RewindFeature() result = %+v; want large KB rewind", result)
	}
}

func TestServerMutationTargetRewindActionUpgradePipelineFailureMetadata(t *testing.T) {
	store, manager, f := newMutationTestFeature(t, "failed upgrade rewind via REST", feature.CreateOptions{
		Pipeline: feature.PipelineMoonshot,
	}, feature.StatusImplementing, feature.PhaseImplement)
	sessions := &mutationTargetSessionManager{
		sessions: []ports.SessionView{&mutationTargetSessionView{
			id:        "session-failed-upgrade",
			featureID: f.ID,
			phase:     feature.PhaseImplement,
			status:    ports.SessionRunning,
			active:    true,
		}},
	}
	target := serverMutationTarget{
		orch:  orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store, Sessions: sessions}, orchestrator.Hooks{}),
		store: store,
	}

	result, err := target.RewindFeature(f.ID, serverruntime.RewindFeatureRequest{
		TargetPhase:     phaseNameInquire,
		UpgradePipeline: feature.PipelineLarge,
	})
	if err == nil {
		t.Fatal("rewindAction() error = nil, want upgrade failure")
	}

	if len(sessions.stopCalls) != 0 {
		t.Fatalf("StopSession calls = %v, want none when upgrade fails", sessions.stopCalls)
	}
	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if updated.Pipeline != feature.PipelineMoonshot || updated.Status != feature.StatusImplementing {
		t.Fatalf("feature pipeline/status = %s/%s, want unchanged moonshot/implementing", updated.Pipeline, updated.Status)
	}
	if result.FeatureID != f.ID || result.TargetPhase != phaseNameInquire || result.UpgradePipeline != "large" || result.Result != resultFailed {
		t.Fatalf("RewindFeature() failure result = %+v; want failed upgrade response", result)
	}
}

func TestServerMutationTargetRestartFeatureDispatchesPhaseWork(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("restart via REST", "desc", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	planPath := filepath.Join(runtimeDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n## Tasks\n\n**Repo:** repo-a\n\n- Restart the implementation.\n"), 0o644); err != nil {
		t.Fatalf("WriteFile plan: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusFailed
		ff.CurrentPhase = feature.PhaseImplement
		ff.LastError = "previous worker died with secret=do-not-leak"
		ff.Artifacts = map[string]string{"plan": planPath}
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
	var dispatched []string
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: manager,
		Store:     store,
	}, orchestrator.Hooks{})
	orch.SetRunMultiRepoImplFn(func(f *feature.Feature, planPath string, _ ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		dispatched = append(dispatched, f.ID+":"+planPath)
		ch := make(chan *agent.OrchestratorResult)
		close(ch)
		return ch, nil
	})
	target := serverMutationTarget{orch: orch, store: store}

	result, err := target.RestartFeature(f.ID, serverruntime.RestartFeatureRequest{})
	if err != nil {
		t.Fatalf("RestartFeature() error = %v", err)
	}

	if len(dispatched) != 1 || dispatched[0] != f.ID+":"+planPath {
		t.Fatalf("phase dispatches = %v, want one implementation dispatch with plan path", dispatched)
	}
	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load restarted feature: %v", err)
	}
	if updated.Status != feature.StatusImplementing || updated.CurrentPhase != feature.PhaseImplement {
		t.Fatalf("restarted feature status/phase = %s/%s, want Implementing/Implement", updated.Status, updated.CurrentPhase)
	}
	if result.FeatureID != f.ID || result.Phase != feature.PhaseImplement.String() || result.Dispatch != "phase" {
		t.Fatalf("RestartFeature() result = %+v; want phase dispatch", result)
	}
	assertJSONDoesNotContain(t, result, "do-not-leak", planPath)
}

func TestServerMutationTargetCleanupAndDeleteActionsMutateFeatureState(t *testing.T) {
	t.Run("cleanup worktrees", func(t *testing.T) {
		target, store, f, worktrees := newCleanupActionTarget(t)

		result, err := target.CleanupFeature(f.ID, serverruntime.CleanupActionRequest{Target: cleanupTargetWorktrees})
		if err != nil {
			t.Fatalf("CleanupFeature(worktrees) error = %v", err)
		}
		if result.FeatureID != f.ID || result.Target != cleanupTargetWorktrees || result.Result != resultCleaned {
			t.Fatalf("CleanupFeature(worktrees) result = %+v; want cleaned worktrees", result)
		}
		calls := mockCallsByMethod(worktrees.Calls, "Remove")
		if len(calls) != 1 || calls[0].Args[0] != testRepoAWorktreePath || calls[0].Args[1] != false {
			t.Fatalf("worktree remove calls = %+v; want one non-branch-deleting cleanup", calls)
		}
		updated, err := store.Load(f.ID)
		if err != nil {
			t.Fatalf("Load feature after cleanup: %v", err)
		}
		if updated.Repos[0].WorktreePath != "" {
			t.Fatalf("WorktreePath after cleanup = %q; want cleared", updated.Repos[0].WorktreePath)
		}
	})

	t.Run("cleanup cycles target is unknown", func(t *testing.T) {
		target, _, f, worktrees := newCleanupActionTarget(t)
		worktrees.Calls = nil

		result, err := target.CleanupFeature(f.ID, serverruntime.CleanupActionRequest{Target: "cycles"})
		if err == nil {
			t.Fatalf("CleanupFeature(cycles) error = nil; want unknown target error")
		}
		if result.FeatureID != f.ID || result.Target != "cycles" || result.Result != resultFailed {
			t.Fatalf("CleanupFeature(cycles) result = %+v; want failed cycles", result)
		}
		if !strings.Contains(err.Error(), "unknown cleanup target") {
			t.Fatalf("CleanupFeature(cycles) error = %v; want unknown cleanup target", err)
		}
		if len(worktrees.Calls) != 0 {
			t.Fatalf("worktree calls = %+v; want none for unknown target", worktrees.Calls)
		}
	})

	t.Run("delete", func(t *testing.T) {
		target, store, f, worktrees := newCleanupActionTarget(t)

		result, err := target.DeleteFeature(f.ID, serverruntime.GuardedFeatureActionRequest{})
		if err != nil {
			t.Fatalf("DeleteFeature() error = %v", err)
		}
		if result.FeatureID != f.ID || result.Status != feature.CascadeDeleteCompleted {
			t.Fatalf("DeleteFeature() result = %+v; want deleted feature", result)
		}
		calls := mockCallsByMethod(worktrees.Calls, "RemoveRef")
		if len(calls) != 2 || calls[0].Args[0] != testRepoAWorktreePath || calls[1].Args[0] != testRepoAWorktreePath {
			t.Fatalf("worktree RemoveRef calls = %+v; want durable worktree and branch cleanup", calls)
		}
		if _, err := store.Load(f.ID); err == nil {
			t.Fatalf("Load deleted feature error = nil; want missing feature")
		}
	})
}

// newMutationTestFeature builds a store/manager with testRepoAName registered,
// creates a feature with createOpts, and sets its status/phase via store.Modify.
func newMutationTestFeature(t *testing.T, name string, createOpts feature.CreateOptions, status feature.Status, phase feature.Phase) (*feature.Store, *feature.Manager, *feature.Feature) {
	t.Helper()
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create(name, "desc", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil, createOpts)
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = status
		ff.CurrentPhase = phase
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
	return store, manager, f
}

// newRESTCreateFeatureTarget builds the serverMutationTarget shared by the
// CreateFeature-via-REST tests.
func newRESTCreateFeatureTarget(store *feature.Store, manager *feature.Manager, cfg *config.Config, configPath string) serverMutationTarget {
	return serverMutationTarget{
		orch:       orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		cfg:        cfg,
		configPath: configPath,
		store:      store,
	}
}

func newPublishActionTarget(t *testing.T) (serverMutationTarget, *feature.Manager, *feature.Store, *feature.Feature) {
	t.Helper()
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	repoPath := filepath.Join(runtimeDir, testRepoAName)
	initMutationGitRepo(t, repoPath)
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("publish via REST", "desc", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		publishable := true
		ff.Status = feature.StatusCodeReady
		ff.CurrentPhase = feature.PhasePublish
		for i := range ff.Repos {
			ff.Repos[i].Publishable = &publishable
			ff.Repos[i].Branch = "feature/publish-via-rest"
		}
		ff.RepoStates = map[string]*feature.RepoState{testRepoAName: {Touched: true}}
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
	orch := orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})
	return serverMutationTarget{orch: orch, store: store}, manager, store, f
}

func initMutationGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir git repo: %v", err)
	}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s in %s: %s: %v", strings.Join(args, " "), dir, strings.TrimSpace(string(out)), err)
		}
	}
}

func newCleanupActionTarget(t *testing.T) (serverMutationTarget, *feature.Store, *feature.Feature, *mocks.MockWorktreeOps) {
	t.Helper()
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos[testRepoAName] = config.RepoConfig{Path: filepath.Join(runtimeDir, testRepoAName)}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	worktrees := mocks.NewMockWorktreeOps()
	manager.Worktrees = worktrees
	f, err := manager.Create("cleanup via REST", "desc", []string{testRepoAName}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPublished
		ff.CurrentPhase = feature.PhasePublish
		ff.Repos[0].WorktreePath = testRepoAWorktreePath
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load prepared feature: %v", err)
	}
	orch := orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store, Worktrees: worktrees}, orchestrator.Hooks{})
	return serverMutationTarget{orch: orch, store: store}, store, loaded, worktrees
}

func mockCallsByMethod(calls []mocks.MockCall, method string) []mocks.MockCall {
	var matching []mocks.MockCall
	for _, call := range calls {
		if call.Method == method {
			matching = append(matching, call)
		}
	}
	return matching
}

func mutationTargetOrchestrator(sessions ports.SessionManager) *orchestrator.Orchestrator {
	return orchestrator.New(orchestrator.Deps{Sessions: sessions}, orchestrator.Hooks{})
}

type mutationTargetSessionManager struct {
	sessions   []ports.SessionView
	stopCalls  []string
	startCalls []mutationTargetStartSessionCall
	onStop     func(string)
}

type mutationTargetStartSessionCall struct {
	id        string
	featureID string
	phase     feature.Phase
	command   []string
	workdir   string
	env       []string
	opts      *ports.SessionOpts
}

func (m *mutationTargetSessionManager) StartSession(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
	var opt *ports.SessionOpts
	if len(opts) > 0 {
		opt = opts[0]
	}
	m.startCalls = append(m.startCalls, mutationTargetStartSessionCall{
		id:        id,
		featureID: featureID,
		phase:     phase,
		command:   append([]string(nil), command...),
		workdir:   workdir,
		env:       append([]string(nil), env...),
		opts:      opt,
	})
	sess := &mutationTargetSessionView{id: id, featureID: featureID, phase: phase, status: ports.SessionRunning, active: true}
	m.sessions = append(m.sessions, sess)
	return sess, nil
}
func (m *mutationTargetSessionManager) StopSession(id string) error {
	m.stopCalls = append(m.stopCalls, id)
	if m.onStop != nil {
		m.onStop(id)
	}
	return nil
}
func (m *mutationTargetSessionManager) GetSession(id string) ports.SessionView {
	for _, s := range m.sessions {
		if s != nil && s.ID() == id {
			return s
		}
	}
	return nil
}
func (m *mutationTargetSessionManager) ActiveSessions() []ports.SessionView {
	var out []ports.SessionView
	for _, s := range m.sessions {
		if s != nil && s.IsActive() {
			out = append(out, s)
		}
	}
	return out
}
func (m *mutationTargetSessionManager) RecentSessions(int) []ports.SessionView { return m.sessions }
func (m *mutationTargetSessionManager) FeatureSessions(featureID string) []ports.SessionView {
	var out []ports.SessionView
	for _, s := range m.sessions {
		if s != nil && s.FeatureID() == featureID {
			out = append(out, s)
		}
	}
	return out
}
func (m *mutationTargetSessionManager) SendInput(string, []byte) error { return nil }
func (m *mutationTargetSessionManager) Attach(id string) (ports.SessionView, error) {
	return m.GetSession(id), nil
}
func (m *mutationTargetSessionManager) Detach()              {}
func (m *mutationTargetSessionManager) Shutdown()            {}
func (m *mutationTargetSessionManager) IsShuttingDown() bool { return false }

type mutationTargetSessionView struct {
	id               string
	featureID        string
	phase            feature.Phase
	status           ports.SessionStatus
	active           bool
	permCacheScope   string
	pending          []*llm.ControlRequestMessage
	sentMessages     []string
	controlCalls     []mutationTargetControlCall
	askCalls         []mutationTargetAskUserCall
	onRespondControl func() error
}

type mutationTargetControlCall struct {
	requestID     string
	allow         bool
	reason        string
	originalInput json.RawMessage
}

type mutationTargetAskUserCall struct {
	requestID string
	questions json.RawMessage
	answers   map[string]string
}

func (s *mutationTargetSessionView) ID() string                       { return s.id }
func (s *mutationTargetSessionView) FeatureID() string                { return s.featureID }
func (s *mutationTargetSessionView) Phase() feature.Phase             { return s.phase }
func (s *mutationTargetSessionView) RepoName() string                 { return "" }
func (s *mutationTargetSessionView) PermCacheScope() string           { return s.permCacheScope }
func (s *mutationTargetSessionView) Kind() ports.SessionKind          { return ports.KindPhase }
func (s *mutationTargetSessionView) Label() string                    { return "" }
func (s *mutationTargetSessionView) Status() ports.SessionStatus      { return s.status }
func (s *mutationTargetSessionView) IsActive() bool                   { return s.active }
func (s *mutationTargetSessionView) Iteration() int                   { return 0 }
func (s *mutationTargetSessionView) StartedAt() time.Time             { return time.Time{} }
func (s *mutationTargetSessionView) InitialPrompt() string            { return "" }
func (s *mutationTargetSessionView) ProviderName() string             { return "" }
func (s *mutationTargetSessionView) Model() string                    { return "" }
func (s *mutationTargetSessionView) WorkDir() string                  { return "" }
func (s *mutationTargetSessionView) EffectiveEffort() llm.EffortLevel { return "" }
func (s *mutationTargetSessionView) EffortSource() llm.EffortSource   { return "" }
func (s *mutationTargetSessionView) MessageLog() ports.MessageLog     { return nil }
func (s *mutationTargetSessionView) Cost() *llm.ResultMessage         { return nil }
func (s *mutationTargetSessionView) LatestUsage() *llm.Usage          { return nil }
func (s *mutationTargetSessionView) AccumulatedUsage() llm.Usage      { return llm.Usage{} }
func (s *mutationTargetSessionView) LastControlRequest() *llm.ControlRequestMessage {
	if len(s.pending) == 0 {
		return nil
	}
	return s.pending[len(s.pending)-1]
}
func (s *mutationTargetSessionView) PendingControlRequests() []*llm.ControlRequestMessage {
	out := make([]*llm.ControlRequestMessage, len(s.pending))
	copy(out, s.pending)
	return out
}
func (s *mutationTargetSessionView) QALog() []ports.QAPair           { return nil }
func (s *mutationTargetSessionView) LogFilePath() string             { return "" }
func (s *mutationTargetSessionView) ContextPercentage() int          { return 0 }
func (s *mutationTargetSessionView) ErrorDetail() string             { return "" }
func (s *mutationTargetSessionView) ExitCodeDetail() string          { return "" }
func (s *mutationTargetSessionView) LastStdoutAt() time.Time         { return time.Time{} }
func (s *mutationTargetSessionView) StatusCh() <-chan string         { return nil }
func (s *mutationTargetSessionView) AttachCh() <-chan llm.SDKMessage { return nil }
func (s *mutationTargetSessionView) Done() <-chan struct{}           { return nil }
func (s *mutationTargetSessionView) HasPendingAskUserQuestion() bool {
	for _, pending := range s.pending {
		if pending != nil && pending.Request.ToolName == toolNameAskUserQuestion {
			return true
		}
	}
	return false
}
func (s *mutationTargetSessionView) HasPendingRootAskUserQuestion() bool {
	return s.HasPendingAskUserQuestion()
}
func (s *mutationTargetSessionView) RootCompletionIntent() llm.CompletionIntent {
	return llm.CompletionIntent{}
}
func (s *mutationTargetSessionView) LiveBackgroundTaskCount() int       { return 0 }
func (s *mutationTargetSessionView) TaskActivities() []llm.TaskActivity { return nil }
func (s *mutationTargetSessionView) SendUserMessage(text string) error {
	s.sentMessages = append(s.sentMessages, text)
	return nil
}
func (s *mutationTargetSessionView) RespondToControl(requestID string, allow bool, reason string) error {
	if s.onRespondControl != nil {
		if err := s.onRespondControl(); err != nil {
			return err
		}
	}
	var original json.RawMessage
	for _, pending := range s.pending {
		if pending != nil && pending.RequestID == requestID {
			original = append(json.RawMessage(nil), pending.Request.Input...)
			break
		}
	}
	s.controlCalls = append(s.controlCalls, mutationTargetControlCall{
		requestID:     requestID,
		allow:         allow,
		reason:        reason,
		originalInput: original,
	})
	return nil
}
func (s *mutationTargetSessionView) RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, _ map[string]llm.AskUserAnnotation) error {
	copied := make(map[string]string, len(answers))
	for k, v := range answers {
		copied[k] = v
	}
	s.askCalls = append(s.askCalls, mutationTargetAskUserCall{
		requestID: requestID,
		questions: append(json.RawMessage(nil), questions...),
		answers:   copied,
	})
	return nil
}
func (s *mutationTargetSessionView) ClearPendingQuestion(requestID string) {
	for i, pending := range s.pending {
		if pending != nil && pending.RequestID == requestID {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			return
		}
	}
}
func (s *mutationTargetSessionView) ResetWaitingStatus() {}
func (s *mutationTargetSessionView) Stop() error         { return nil }
func (s *mutationTargetSessionView) Interrupt() error    { return nil }
func (s *mutationTargetSessionView) Wait()               {}
func (s *mutationTargetSessionView) SetStatus(status ports.SessionStatus) {
	s.status = status
}
func (s *mutationTargetSessionView) SetLogFile(*os.File)                            {}
func (s *mutationTargetSessionView) AddCleanupFunc(func())                          {}
func (s *mutationTargetSessionView) SetHasUnansweredQuestion(bool)                  {}
func (s *mutationTargetSessionView) CloseStdin()                                    {}
func (s *mutationTargetSessionView) SetOnToolAllowed(func(string, json.RawMessage)) {}
func (s *mutationTargetSessionView) SetOnFileRead(func(llm.FileReadEvent))          {}
func (s *mutationTargetSessionView) SetOnSubagentEvent(func(llm.SDKMessage))        {}

func assertJSONDoesNotContain(t *testing.T, value any, banned ...string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal(%T): %v", value, err)
	}
	for _, b := range banned {
		if b != "" && strings.Contains(string(raw), b) {
			t.Fatalf("response leaked %q in %s", b, raw)
		}
	}
}

func assertTarget(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	for k, wantValue := range want {
		if got[k] != wantValue {
			t.Fatalf("target[%q] = %#v, want %#v; target = %#v", k, got[k], wantValue, got)
		}
	}
}

func jsonEqual(a, b json.RawMessage) bool {
	var av any
	var bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}
