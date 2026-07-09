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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/utilskill"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestServerMutationTargetAnswerPermissionRespondsToPendingControlRequest(t *testing.T) {
	for _, tc := range []struct {
		name      string
		decision  string
		wantAllow bool
	}{
		{name: "allow", decision: "allow", wantAllow: true},
		{name: "deny", decision: "deny", wantAllow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := json.RawMessage(`{"command":"go test ./cmd/agentico"}`)
			sess := &mutationTargetSessionView{
				id:        "session-permission",
				featureID: "feat-permission",
				phase:     feature.PhaseImplement,
				status:    ports.SessionWaitingPermission,
				active:    true,
				pending: []*llm.ControlRequestMessage{{
					Type:      "control_request",
					RequestID: "perm-1",
					Request: llm.ControlRequest{
						Subtype:  "can_use_tool",
						ToolName: "Bash",
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
				RequestID: "perm-1",
				SessionID: "session-permission",
				Decision:  tc.decision,
			})
			if err != nil {
				t.Fatalf("AnswerPermission() error = %v", err)
			}

			if len(sess.controlCalls) != 1 {
				t.Fatalf("RespondToControl calls = %d, want 1", len(sess.controlCalls))
			}
			call := sess.controlCalls[0]
			if call.requestID != "perm-1" || call.allow != tc.wantAllow {
				t.Fatalf("RespondToControl call = %+v, want request perm-1 allow=%v", call, tc.wantAllow)
			}
			if !jsonEqual(call.originalInput, input) {
				t.Fatalf("RespondToControl original input = %s, want %s", call.originalInput, input)
			}
			if result.RequestID != "perm-1" || result.SessionID != "session-permission" || result.Decision != tc.decision || result.Result != "answered" {
				t.Fatalf("AnswerPermission() result = %+v; want request/session/decision answer", result)
			}
			assertJSONDoesNotContain(t, result, "go test ./cmd/agentico")
		})
	}
}

func TestServerMutationTargetAnswerAskUserRespondsWithOriginalInputAndSafeMetadata(t *testing.T) {
	input := json.RawMessage(`{"questions":[{"question":"Which DB?"},{"question":"Rollout plan?"}]}`)
	answers := map[string]string{
		"Which DB?":     "Postgres with read replicas",
		"Rollout plan?": "Dark launch first",
	}
	sess := &mutationTargetSessionView{
		id:        "session-ask",
		featureID: "feat-ask",
		phase:     feature.PhaseInquire,
		status:    ports.SessionWaitingHelp,
		active:    true,
		pending: []*llm.ControlRequestMessage{{
			Type:      "control_request",
			RequestID: "ask-1",
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "AskUserQuestion",
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
		RequestID: "ask-1",
		SessionID: "session-ask",
		Answers:   answers,
	})
	if err != nil {
		t.Fatalf("AnswerAskUser() error = %v", err)
	}

	if len(sess.askCalls) != 1 {
		t.Fatalf("RespondToAskUser calls = %d, want 1", len(sess.askCalls))
	}
	call := sess.askCalls[0]
	if call.requestID != "ask-1" {
		t.Fatalf("RespondToAskUser requestID = %q, want ask-1", call.requestID)
	}
	if !jsonEqual(call.questions, input) {
		t.Fatalf("RespondToAskUser questions = %s, want original %s", call.questions, input)
	}
	if !reflect.DeepEqual(call.answers, answers) {
		t.Fatalf("RespondToAskUser answers = %v, want %v", call.answers, answers)
	}
	if result.RequestID != "ask-1" || result.SessionID != "session-ask" || result.Result != "answered" {
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
				"label": "Use the full input (Recommended)",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	sess := &mutationTargetSessionView{
		id:        "session-ask",
		featureID: "feat-ask",
		phase:     feature.PhaseInquire,
		status:    ports.SessionWaitingHelp,
		active:    true,
		pending: []*llm.ControlRequestMessage{{
			Type:      "control_request",
			RequestID: "ask-1",
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "AskUserQuestion",
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
		RequestID: "ask-1",
		SessionID: "session-ask",
		Answers: map[string]string{
			truncatedQuestion: "Use the full input (Recommended)",
		},
	})
	if err != nil {
		t.Fatalf("AnswerAskUser() error = %v", err)
	}

	if len(sess.askCalls) != 1 {
		t.Fatalf("RespondToAskUser calls = %d, want 1", len(sess.askCalls))
	}
	call := sess.askCalls[0]
	if got := call.answers[fullQuestion]; got != "Use the full input (Recommended)" {
		t.Fatalf("RespondToAskUser answers[%q] = %q; want selected answer in %v", fullQuestion, got, call.answers)
	}
	if _, ok := call.answers[truncatedQuestion]; ok {
		t.Fatalf("RespondToAskUser kept truncated question key %q in %v", truncatedQuestion, call.answers)
	}
}

func TestServerMutationTargetStartRebaseStartsFeatureRebasePromptly(t *testing.T) {
	starter := &fakeFeatureRebaseStarter{}
	target := serverMutationTarget{rebaseStarter: starter}

	resp, err := target.StartRebase("feat-rebase", serverruntime.RebaseActionRequest{})
	if err != nil {
		t.Fatalf("StartRebase error = %v", err)
	}
	if resp.FeatureID != "feat-rebase" || resp.Result != "started" || resp.CycleType != string(feature.CycleRebase) {
		t.Fatalf("StartRebase response = %+v, want started feature rebase", resp)
	}
	if resp.Repo != "" || resp.RebaseTarget != "" || len(resp.ConflictFiles) != 0 || resp.SessionID != "" {
		t.Fatalf("StartRebase response leaked repo/conflict fields: %+v", resp)
	}
	if got := strings.Join(starter.featureIDs, ","); got != "feat-rebase" {
		t.Fatalf("StartFeatureRebase calls = %q, want feat-rebase", got)
	}
}

func TestServerMutationTargetStartRebaseRejectsInternalInputs(t *testing.T) {
	for _, req := range []serverruntime.RebaseActionRequest{
		{Repo: "agentic"},
		{RebaseTarget: "main"},
		{ConflictFiles: []string{"internal/server/mutation.go"}},
		{ConflictFiles: []string{}},
	} {
		starter := &fakeFeatureRebaseStarter{}
		target := serverMutationTarget{rebaseStarter: starter}

		resp, err := target.StartRebase("feat-rebase", req)
		if err == nil || !strings.Contains(err.Error(), "rebase is feature-scoped") {
			t.Fatalf("StartRebase(%+v) error = %v, want feature-scoped rejection", req, err)
		}
		if resp.Result != "failed" {
			t.Fatalf("StartRebase(%+v) result = %q, want failed", req, resp.Result)
		}
		if len(starter.featureIDs) != 0 {
			t.Fatalf("StartFeatureRebase called for rejected request: %v", starter.featureIDs)
		}
	}
}

type fakeFeatureRebaseStarter struct {
	featureIDs []string
	err        error
}

func (f *fakeFeatureRebaseStarter) StartFeatureRebase(featureID string) error {
	f.featureIDs = append(f.featureIDs, featureID)
	return f.err
}

func TestServerMutationTargetSendHelpSendsUserMessageToAddressedActiveSession(t *testing.T) {
	sess := &mutationTargetSessionView{
		id:        "session-help",
		featureID: "feat-help",
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
		FeatureID: "feat-help",
		SessionID: "session-help",
		Message:   "Please use the existing migration path.",
	})
	if err != nil {
		t.Fatalf("SendHelp() error = %v", err)
	}

	if !reflect.DeepEqual(sess.sentMessages, []string{"Please use the existing migration path."}) {
		t.Fatalf("SendUserMessage calls = %v, want addressed help text", sess.sentMessages)
	}
	if result.FeatureID != "feat-help" || result.SessionID != "session-help" || result.Result != "sent" {
		t.Fatalf("SendHelp() result = %+v; want feature/session sent", result)
	}
	assertJSONDoesNotContain(t, result, "Please use the existing migration path.")
}

func TestServerMutationTargetStartChatStartsReadOnlyInteractiveUtilitySession(t *testing.T) {
	stateDir := t.TempDir()
	skillsDir := filepath.Join(stateDir, "skills")
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
		sessions:     sessions,
		phaseRunner:  phaseRunner,
		workspaceDir: "/workspace",
	}

	result, err := target.StartChat(serverruntime.ChatStartRequest{Message: "What is running?"})
	if err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}

	if result.SessionID != serverChatSessionID || result.Result != "started" {
		t.Fatalf("StartChat() result = %+v, want chat session started", result)
	}
	if len(captured) != 1 {
		t.Fatalf("BuildSession calls = %d, want 1", len(captured))
	}
	build := captured[0]
	if build.Model != "cheap-chat" || build.WorkDir != "/workspace" || build.Phase != utilskill.PhaseAll || build.TurnMode != ports.TurnModeInteractive {
		t.Fatalf("BuildSession opts = %+v, want utility interactive chat in workspace", build)
	}
	if build.EffortLevel != llm.EffortLow {
		t.Fatalf("BuildSession EffortLevel = %q, want low for AMA utility chat", build.EffortLevel)
	}
	for _, tool := range []string{"Edit", "Write", "NotebookEdit", "Bash"} {
		if !slices.Contains(build.DisallowedTools, tool) {
			t.Fatalf("BuildSession DisallowedTools = %v, missing %s", build.DisallowedTools, tool)
		}
	}
	if !strings.Contains(build.Prompt, "What is running?") || !strings.Contains(build.Prompt, filepath.Join(skillsDir, "chat", "SKILL.md")) {
		t.Fatalf("BuildSession prompt = %q, want chat skill instruction and user message", build.Prompt)
	}
	if len(sessions.startCalls) != 1 {
		t.Fatalf("StartSession calls = %d, want 1", len(sessions.startCalls))
	}
	start := sessions.startCalls[0]
	if start.id != serverChatSessionID || start.featureID != serverChatSessionID || start.phase != feature.PhaseResearch || start.workdir != "/workspace" {
		t.Fatalf("StartSession call = %+v, want chat utility identity and research session in workspace", start)
	}
	if start.opts == nil || start.opts.Kind != ports.KindChat || start.opts.TurnMode != ports.TurnModeInteractive || start.opts.Label != "chat" || start.opts.InitialPrompt != build.Prompt {
		t.Fatalf("StartSession opts = %+v, want chat-kind interactive session with initial prompt", start.opts)
	}
	if start.opts.StderrPath != filepath.Join(stateDir, "chat", "stderr.log") {
		t.Fatalf("StartSession StderrPath = %q, want chat stderr capture", start.opts.StderrPath)
	}
	assertJSONDoesNotContain(t, result, "What is running?")
}

func TestServerMutationTargetDraftNeedUserInputAnswersUpdatesPendingArtifactByPromptAndIndex(t *testing.T) {
	gatePath := filepath.Join(t.TempDir(), agent.NeedUserInputArtifactName)
	original := agent.NeedUserInputRecord{
		Summary:   "Implementation is blocked on product choices.",
		Iteration: 3,
		Questions: []agent.NeedUserInputQuestion{
			{Index: 1, Prompt: "Which database should back search?"},
			{Index: 2, Prompt: "How should rollout be staged?"},
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
	cfg.Defaults.Inquireness = "low"
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config error = %v", err)
	}
	target := serverMutationTarget{cfg: cfg, configPath: configPath}

	result, err := target.RuntimeConfig(serverruntime.RuntimeConfigMutationRequest{
		Defaults: config.DefaultsConfig{
			Models: config.ModelConfig{
				Research:       "new-research",
				Implementation: "new-implementation",
			},
			Inquireness:   "high",
			MaxIterations: 8,
			Checkpoints: config.Checkpoints{
				RoadmapReview:   true,
				PhasePlanReview: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("RuntimeConfig() error = %v", err)
	}

	if cfg.Defaults.Models.Research != "new-research" {
		t.Fatalf("in-memory research model = %q, want new-research", cfg.Defaults.Models.Research)
	}
	if cfg.Defaults.Models.Implementation != "new-implementation" {
		t.Fatalf("in-memory implementation model = %q, want new-implementation", cfg.Defaults.Models.Implementation)
	}
	if cfg.Defaults.MaxIterations != 8 || cfg.Defaults.Inquireness != "high" || !cfg.Defaults.Checkpoints.RoadmapReview || !cfg.Defaults.Checkpoints.PhasePlanReview {
		t.Fatalf("in-memory defaults = %+v, want requested changes", cfg.Defaults)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load config error = %v", err)
	}
	if loaded.Defaults.Models.Research != "new-research" ||
		loaded.Defaults.Models.Implementation != "new-implementation" ||
		loaded.Defaults.MaxIterations != 8 ||
		loaded.Defaults.Inquireness != "high" ||
		!loaded.Defaults.Checkpoints.RoadmapReview ||
		!loaded.Defaults.Checkpoints.PhasePlanReview {
		t.Fatalf("persisted defaults = %+v, want requested changes", loaded.Defaults)
	}
	if result.Result != "updated" {
		t.Fatalf("RuntimeConfig() result = %+v; want updated", result)
	}
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
	if result.Result != "updated" {
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
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	cfg.Defaults.Models = config.ModelConfig{
		Research:       "default-research",
		Planning:       "default-planning",
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
	target := serverMutationTarget{
		orch:       orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		cfg:        cfg,
		configPath: configPath,
		store:      store,
	}
	attachment := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(attachment, []byte("rest attachment"), 0o644); err != nil {
		t.Fatalf("WriteFile attachment: %v", err)
	}

	result, err := target.CreateFeature(serverruntime.CreateFeatureRequest{
		Name:         "REST durable options",
		Description:  "create via REST",
		Repos:        []string{"repo-a"},
		ExitCriteria: "all acceptance checks pass",
		Inquireness:  "none",
		Models: config.ModelConfig{
			Research:       "claude:opus",
			Implementation: "codex:gpt-5.4",
			KBBuild:        "claude:haiku",
		},
		Attachments:             []string{attachment},
		UseCurrentBranch:        true,
		UseCurrentBranchPerRepo: map[string]bool{"repo-a": true},
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
	if created.Models.Research != "claude:opus" || created.Models.Planning != "default-planning" ||
		created.Models.Implementation != "codex:gpt-5.4" || created.Models.KBBuild != "claude:haiku" {
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
	if pref.Models.Implementation != "codex:gpt-5.4" || pref.Models.Planning != "default-planning" || pref.Inquireness != "none" {
		t.Fatalf("persisted pipeline preference = %+v; want REST model/inquireness selections", pref)
	}
	gates := loaded.Repos["repo-a"].PipelineGates["medium"]
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
	target := serverMutationTarget{
		orch:       orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		cfg:        cfg,
		configPath: configPath,
		store:      store,
	}

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
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config error = %v", err)
	}
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, cfg)
	worktrees := mocks.NewMockWorktreeOperator()
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		return "", errors.New("worktree creation should be deferred to setup")
	}
	manager.Worktrees = worktrees
	target := serverMutationTarget{
		orch:       orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		cfg:        cfg,
		configPath: configPath,
		store:      store,
	}

	result, err := target.CreateFeature(serverruntime.CreateFeatureRequest{
		Name:     "REST queued setup",
		Repos:    []string{"repo-a"},
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

func TestServerMutationTargetRetryFeatureRoutesSetupFailureToSetupRetry(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	failWorktree := true
	worktrees := mocks.NewMockWorktreeOperator()
	worktrees.CreateFn = func(repoPath, featureSlug, repoName, startPoint string) (string, error) {
		if failWorktree {
			return "", errors.New("repo checkout missing")
		}
		return filepath.Join(runtimeDir, "worktrees", featureSlug, repoName), nil
	}
	manager.Worktrees = worktrees
	f, err := manager.Create("Retry setup via REST", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
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
	if result.Result != "retried" || result.FeatureID != f.ID {
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

func TestServerMutationTargetRetryFeatureRestartsFailedPhase(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("Retry failed phase via REST", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusFailed
		f.CurrentPhase = feature.PhasePlan
		f.FailureType = feature.FailureInfrastructure
		f.LastError = "previous planning failure"
		return nil
	}); err != nil {
		t.Fatalf("prepare failed feature: %v", err)
	}

	target := serverMutationTarget{
		orch:  orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		store: store,
	}
	result, err := target.RetryFeature(f.ID)
	if err != nil {
		t.Fatalf("RetryFeature() error = %v, want failed phase restarted", err)
	}
	if result.Result != "retried" || result.FeatureID != f.ID {
		t.Fatalf("RetryFeature() result = %+v, want retried feature", result)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load retried feature: %v", err)
	}
	if updated.Status != feature.StatusPlanning || updated.CurrentPhase != feature.PhasePlan {
		t.Fatalf("feature status/phase = %s/%s, want Planning/Plan after retry dispatch", updated.Status, updated.CurrentPhase)
	}
	if updated.FailureType != "" || updated.LastError != "" {
		t.Fatalf("failure fields = %q/%q, want cleared", updated.FailureType, updated.LastError)
	}
}

func TestServerMutationTargetReviewDecisionRewindProceedsFromExistingRewind(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("rewind review via REST", "old desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{Pipeline: feature.PipelineMedium})
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

	if _, err := target.ReviewDecision(f.ID, serverruntime.ReviewDecisionRequest{
		Decision: "proceed",
		Phase:    "plan",
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
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config error = %v", err)
	}
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("config via REST", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
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
	target := serverMutationTarget{
		orch:       orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		cfg:        cfg,
		configPath: configPath,
		store:      store,
	}

	result, err := target.UpdateFeatureConfig(f.ID, serverruntime.FeatureConfigMutationRequest{
		Models: config.ModelConfig{
			Implementation: "claude:sonnet",
			Review:         "codex:gpt-5.4-mini",
		},
		Inquireness: "high",
		Pipeline:    feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{
			InquiryReview:   true,
			DesignReview:    true,
			RoadmapReview:   true,
			PhasePlanReview: true,
			ManualPublish:   false,
		},
	})
	if err != nil {
		t.Fatalf("UpdateFeatureConfig() error = %v", err)
	}
	if result.FeatureID != f.ID || result.Result != "updated" {
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
	if updated.Models.Implementation != "claude:sonnet" || updated.Models.Review != "codex:gpt-5.4-mini" || updated.Inquireness != feature.InquirenessHigh {
		t.Fatalf("updated feature config = models:%+v inq:%q; want REST edit", updated.Models, updated.Inquireness)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	pref := loaded.Defaults.PipelinePreferences["medium"]
	if pref.Models.Implementation != "claude:sonnet" || pref.Models.Review != "codex:gpt-5.4-mini" || pref.Inquireness != "high" {
		t.Fatalf("persisted preference = %+v; want updated feature config", pref)
	}
	gates := loaded.Repos["repo-a"].PipelineGates["medium"]
	if gates.InquiryReview || gates.DesignReview || !gates.RoadmapReview || !gates.PhasePlanReview || gates.ManualPublish {
		t.Fatalf("persisted repo gates = %+v; want normalized medium gates", gates)
	}
}

func TestServerMutationTargetPublishActionPublishesFeatureAndReturnsSafeMetadata(t *testing.T) {
	target, manager, store, f := newPublishActionTarget(t)
	target.orch.SetPublishRepoFn(func(featureID, repoName string) (string, error) {
		if featureID != f.ID || repoName != "repo-a" {
			t.Fatalf("publish repo call = %s/%s, want %s/repo-a", featureID, repoName, f.ID)
		}
		prURL := "https://github.com/acme/repo-a/pull/12"
		if err := manager.SetRepoPublished(featureID, repoName, prURL); err != nil {
			return "", err
		}
		return prURL, nil
	})

	result, err := target.PublishFeature(f.ID, serverruntime.PublishFeatureRequest{Repos: []string{"repo-a"}})
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
	if result.FeatureID != f.ID || result.Result != "conflict" {
		t.Fatalf("PublishFeature() result = %+v; want conflict feature", result)
	}
	assertTarget(t, actionConflict.Target, map[string]any{
		"conflict":      "publish",
		"repo":          "repo-a",
		"branch":        "feature/publish-conflict",
		"rebase_target": "main",
	})
}

func TestServerMutationTargetRewindActionReturnsEffectiveTargetMetadata(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("rewind via REST", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusCodeReady
		ff.CurrentPhase = feature.PhasePublish
		ff.RepoStates = map[string]*feature.RepoState{"repo-a": {Touched: true}}
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
	target := serverMutationTarget{
		orch:  orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{}),
		store: store,
	}

	result, err := target.RewindFeature(f.ID, serverruntime.RewindFeatureRequest{TargetPhase: "implement"})
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
	if result.FeatureID != f.ID || result.TargetPhase != "implement" || result.EffectivePhase != "implement" || result.WarningCount != 0 {
		t.Fatalf("RewindFeature() result = %+v; want effective implement rewind", result)
	}
}

func TestServerMutationTargetRewindActionStopsSessionsBeforeRewind(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("rewind stops sessions", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
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

	if _, err := target.RewindFeature(f.ID, serverruntime.RewindFeatureRequest{TargetPhase: "plan"}); err != nil {
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
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("upgrade rewind via REST", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
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
		TargetPhase:     "inquire",
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
	if result.FeatureID != f.ID || result.TargetPhase != "inquire" || result.UpgradePipeline != "large" || result.EffectivePhase != feature.PhaseKnowledgeBase.DirName() || result.Result != "rewound" {
		t.Fatalf("RewindFeature() result = %+v; want large KB rewind", result)
	}
}

func TestServerMutationTargetRewindActionUpgradePipelineFailureMetadata(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("failed upgrade rewind via REST", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
		Pipeline: feature.PipelineMoonshot,
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
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
		TargetPhase:     "inquire",
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
	if result.FeatureID != f.ID || result.TargetPhase != "inquire" || result.UpgradePipeline != "large" || result.Result != "failed" {
		t.Fatalf("RewindFeature() failure result = %+v; want failed upgrade response", result)
	}
}

func TestServerMutationTargetRestartFeatureDispatchesPhaseWork(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("restart via REST", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil)
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

func TestServerMutationTargetStartRefactorPersistsImagesAndAttachments(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("refactor with evidence", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPublished
		ff.CurrentPhase = feature.PhasePublish
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}

	img := filepath.Join(runtimeDir, "clip.png")
	doc := filepath.Join(runtimeDir, "spec.pdf")
	if err := os.WriteFile(img, []byte("image bytes"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if err := os.WriteFile(doc, []byte("spec bytes"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: manager,
		Store:     store,
		PhaseRunner: &agent.PhaseRunner{
			StateDir: store.BaseDir,
			BuildSessionFn: func(agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
				return nil, nil, nil, errors.New("stop after synchronous refactor setup")
			},
		},
	}, orchestrator.Hooks{})
	t.Cleanup(orch.WaitForCycles)
	target := serverMutationTarget{orch: orch, store: store}

	if _, err := target.StartRefactor(f.ID, serverruntime.RefactorActionRequest{
		Repo:        "repo-a",
		Prompt:      "use the attached evidence",
		Images:      []string{img},
		Attachments: []string{doc},
	}); err != nil {
		t.Fatalf("StartRefactor() error = %v", err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if len(updated.Images) != 1 {
		t.Fatalf("Images = %v, want one copied refactor image", updated.Images)
	}
	if data, err := os.ReadFile(updated.Images[0]); err != nil || string(data) != "image bytes" {
		t.Fatalf("copied image data = %q, err=%v; want image bytes", string(data), err)
	}
	if len(updated.Attachments) != 1 {
		t.Fatalf("Attachments = %v, want one copied refactor attachment", updated.Attachments)
	}
	if data, err := os.ReadFile(updated.Attachments[0]); err != nil || string(data) != "spec bytes" {
		t.Fatalf("copied attachment data = %q, err=%v; want spec bytes", string(data), err)
	}
	if !strings.Contains(updated.Images[0], filepath.Join(f.ID, "images")) {
		t.Fatalf("image path = %q, want copied under feature images directory", updated.Images[0])
	}
	if !strings.Contains(updated.Attachments[0], filepath.Join(f.ID, "attachments")) {
		t.Fatalf("attachment path = %q, want copied under feature attachments directory", updated.Attachments[0])
	}
}

func TestServerMutationTargetStartRefactorDoesNotPersistRequestedPipeline(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("moonshot refactor", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
		Pipeline:    feature.PipelineMoonshot,
		Checkpoints: feature.DefaultCheckpointsForProfile(feature.PipelineMoonshot),
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	originalCheckpoints := f.Checkpoints
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPublished
		ff.CurrentPhase = feature.PhasePublish
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}

	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: manager,
		Store:     store,
		PhaseRunner: &agent.PhaseRunner{
			StateDir: store.BaseDir,
			BuildSessionFn: func(agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
				return nil, nil, nil, errors.New("stop after synchronous refactor setup")
			},
		},
	}, orchestrator.Hooks{})
	t.Cleanup(orch.WaitForCycles)
	target := serverMutationTarget{orch: orch, store: store}

	if _, err := target.StartRefactor(f.ID, serverruntime.RefactorActionRequest{
		Repo:     "repo-a",
		Prompt:   "make the small follow-up change",
		Pipeline: feature.PipelineMedium,
	}); err != nil {
		t.Fatalf("StartRefactor() error = %v", err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if updated.Pipeline != feature.PipelineMoonshot {
		t.Fatalf("Pipeline = %s, want original moonshot after medium refactor request", updated.Pipeline)
	}
	if updated.Checkpoints != originalCheckpoints {
		t.Fatalf("Checkpoints = %+v, want original %+v after medium refactor request", updated.Checkpoints, originalCheckpoints)
	}
}

func TestServerMutationTargetStartRefactorUsesRequestedPipelineEffort(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("moonshot feature", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
		Pipeline: feature.PipelineMoonshot,
	})
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPublished
		ff.CurrentPhase = feature.PhasePublish
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}

	effortCh := make(chan llm.EffortLevel, 1)
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: manager,
		Store:     store,
		PhaseRunner: &agent.PhaseRunner{
			StateDir: store.BaseDir,
			BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
				select {
				case effortCh <- opts.EffortLevel:
				default:
				}
				return nil, nil, nil, errors.New("stop after capturing refactor effort")
			},
		},
	}, orchestrator.Hooks{})
	t.Cleanup(orch.WaitForCycles)
	target := serverMutationTarget{orch: orch, store: store}

	if _, err := target.StartRefactor(f.ID, serverruntime.RefactorActionRequest{
		Repo:     "repo-a",
		Prompt:   "make the small follow-up change",
		Pipeline: feature.PipelineMedium,
	}); err != nil {
		t.Fatalf("StartRefactor() error = %v", err)
	}

	select {
	case got := <-effortCh:
		if got != llm.EffortMedium {
			t.Fatalf("refactor effort = %s, want medium from request pipeline", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for refactor session build")
	}
}

func TestServerMutationTargetReviewCommentsFetchFiltersAndStartStages(t *testing.T) {
	target, store, f := newReviewCommentsActionTarget(t)
	if err := agent.SaveAddressedIDsForRepo(store.BaseDir, f, "repo-a", []int{100}); err != nil {
		t.Fatalf("SaveAddressedIDsForRepo: %v", err)
	}

	resp, err := target.FetchReviewComments(f.ID, serverruntime.ReviewCommentsFetchRequest{Repo: "repo-a"})
	if err != nil {
		t.Fatalf("FetchReviewComments() error = %v", err)
	}
	if len(resp.Comments) != 1 || resp.Comments[0].ID != 101 || resp.Comments[0].RepoName != "repo-a" {
		t.Fatalf("FetchReviewComments() comments = %+v; want only unaddressed repo-tagged comment 101", resp.Comments)
	}
	if _, err := agent.LoadReviewCommentsForRepo(store.BaseDir, f, "repo-a"); err == nil {
		t.Fatalf("FetchReviewComments staged comments; want fetch to remain read-only")
	}

	result, err := target.StartReviewComments(f.ID, serverruntime.ReviewCommentsActionRequest{Repo: "repo-a", Mode: "address_all"})
	if err == nil {
		t.Fatal("StartReviewComments() error = nil; want dispatch error from nil phase runner after staging")
	}
	if result.FeatureID != f.ID || result.Repo != "repo-a" || result.Mode != "address_all" || result.CycleType != string(feature.CycleReviewComments) || result.Result != "failed" {
		t.Fatalf("StartReviewComments() result = %+v; want failed staged review-comments", result)
	}
	staged, err := agent.LoadReviewCommentsForRepo(store.BaseDir, f, "repo-a")
	if err != nil {
		t.Fatalf("LoadReviewCommentsForRepo: %v", err)
	}
	if staged.Mode != "address_all" || len(staged.Comments) != 1 || staged.Comments[0].ID != 101 || staged.Comments[0].RepoName != "repo-a" {
		t.Fatalf("staged review comments = %+v; want address_all with comment 101", staged)
	}
}

func TestServerMutationTargetReviewCommentsStartUsesProvidedPreviewedComments(t *testing.T) {
	target, store, f := newReviewCommentsActionTarget(t)
	reviewer := target.reviewer.(*fakeReviewCommentOperator)
	reviewer.fetchCalls = 0

	result, err := target.StartReviewComments(f.ID, serverruntime.ReviewCommentsActionRequest{
		Repo: "repo-a",
		Mode: "auto",
		Comments: []serverruntime.ReviewCommentDTO{{
			ID:        202,
			Type:      ports.CommentTypeReview,
			RepoName:  "repo-a",
			Path:      "internal/tui/api_app.go",
			Line:      42,
			Body:      "use the previewed set",
			UserLogin: "reviewer",
			DiffHunk:  "@@ -1 +1 @@\n-old\n+new",
		}},
	})
	if err == nil {
		t.Fatal("StartReviewComments() error = nil; want dispatch error from nil phase runner after staging")
	}
	if reviewer.fetchCalls != 0 {
		t.Fatalf("FetchPRComments calls = %d; want 0 when comments are provided by preview", reviewer.fetchCalls)
	}
	if result.FeatureID != f.ID || result.Repo != "repo-a" || result.Mode != "auto" || result.Source != "provided" || result.CommentCount != 1 || result.Result != "failed" {
		t.Fatalf("StartReviewComments() result = %+v; want failed provided preview", result)
	}
	staged, err := agent.LoadReviewCommentsForRepo(store.BaseDir, f, "repo-a")
	if err != nil {
		t.Fatalf("LoadReviewCommentsForRepo: %v", err)
	}
	if staged.Mode != "auto" || len(staged.Comments) != 1 || staged.Comments[0].ID != 202 || staged.Comments[0].DiffHunk == "" || staged.Comments[0].User.Login != "reviewer" {
		t.Fatalf("staged review comments = %+v; want provided previewed comment 202", staged)
	}
}

func TestServerMutationTargetFinishTweakFinalReviewStartsCycleReview(t *testing.T) {
	target, orch, publisher, f := newTweakFinishActionTarget(t, true)

	result, err := target.FinishTweak(f.ID, serverruntime.TweakFinishRequest{Decision: "final-review", HadChanges: true})
	if err != nil {
		t.Fatalf("FinishTweak(final-review) error = %v", err)
	}
	if result.FeatureID != f.ID || result.Decision != "final-review" || !result.HadChanges || result.Result != "review_started" {
		t.Fatalf("FinishTweak() result = %+v; want review_started final-review", result)
	}
	if got := countMockCalls(publisher.Calls, "CommitAll"); got != 0 {
		t.Fatalf("CommitAll calls = %d; want 0 because final-review follows the commit decision", got)
	}

	orch.WaitForCycles()
	select {
	case ev := <-orch.Events():
		if ev.Type != ports.TweakReviewApproved || ev.FeatureID != f.ID {
			t.Fatalf("event = %+v; want TweakReviewApproved for %s", ev, f.ID)
		}
	default:
		t.Fatalf("orchestrator emitted no tweak review approval event")
	}
}

func TestServerMutationTargetCleanupAndDeleteActionsMutateFeatureState(t *testing.T) {
	t.Run("cleanup cycles", func(t *testing.T) {
		target, store, f, _ := newCleanupActionTarget(t)

		result, err := target.CleanupFeature(f.ID, serverruntime.CleanupActionRequest{Target: "cycles"})
		if err != nil {
			t.Fatalf("CleanupFeature(cycles) error = %v", err)
		}
		if result.FeatureID != f.ID || result.Target != "cycles" || result.Result != "cleaned" {
			t.Fatalf("CleanupFeature(cycles) result = %+v; want cleaned cycles", result)
		}
		updated, err := store.Load(f.ID)
		if err != nil {
			t.Fatalf("Load feature after cleanup: %v", err)
		}
		if len(updated.RepoCycles) != 0 {
			t.Fatalf("RepoCycles after cleanup = %+v; want cleared", updated.RepoCycles)
		}
	})

	t.Run("cleanup worktrees", func(t *testing.T) {
		target, store, f, worktrees := newCleanupActionTarget(t)

		result, err := target.CleanupFeature(f.ID, serverruntime.CleanupActionRequest{Target: "worktrees"})
		if err != nil {
			t.Fatalf("CleanupFeature(worktrees) error = %v", err)
		}
		if result.FeatureID != f.ID || result.Target != "worktrees" || result.Result != "cleaned" {
			t.Fatalf("CleanupFeature(worktrees) result = %+v; want cleaned worktrees", result)
		}
		if len(worktrees.removeCalls) != 1 || worktrees.removeCalls[0].path != "/tmp/repo-a-worktree" || worktrees.removeCalls[0].deleteBranch {
			t.Fatalf("worktree remove calls = %+v; want one non-branch-deleting cleanup", worktrees.removeCalls)
		}
		updated, err := store.Load(f.ID)
		if err != nil {
			t.Fatalf("Load feature after cleanup: %v", err)
		}
		if updated.Repos[0].WorktreePath != "" {
			t.Fatalf("WorktreePath after cleanup = %q; want cleared", updated.Repos[0].WorktreePath)
		}
	})

	t.Run("delete", func(t *testing.T) {
		target, store, f, worktrees := newCleanupActionTarget(t)

		result, err := target.DeleteFeature(f.ID)
		if err != nil {
			t.Fatalf("DeleteFeature() error = %v", err)
		}
		if result.FeatureID != f.ID || result.Result != "deleted" {
			t.Fatalf("DeleteFeature() result = %+v; want deleted feature", result)
		}
		if len(worktrees.removeCalls) != 1 || worktrees.removeCalls[0].path != "/tmp/repo-a-worktree" || !worktrees.removeCalls[0].deleteBranch {
			t.Fatalf("worktree remove calls = %+v; want one branch-deleting delete", worktrees.removeCalls)
		}
		if _, err := store.Load(f.ID); err == nil {
			t.Fatalf("Load deleted feature error = nil; want missing feature")
		}
	})
}

func TestServerMutationTargetActionConflictErrorWrapsRebaseFailure(t *testing.T) {
	err := &orchestrator.RebaseConflictError{
		FeatureID:     "feat-rebase",
		RepoName:      "repo-a",
		Branch:        "feature/rebase",
		RebaseTarget:  "main",
		ConflictFiles: []string{"a.go", "b.go"},
	}
	wrapped := actionConflictError(err)

	var rebaseConflict *orchestrator.RebaseConflictError
	if !errors.As(wrapped, &rebaseConflict) {
		t.Fatalf("wrapped error = %T %v; want RebaseConflictError", wrapped, wrapped)
	}
	var actionConflict *serverruntime.ActionConflictError
	if !errors.As(wrapped, &actionConflict) {
		t.Fatalf("wrapped error = %T %v; want ActionConflictError", wrapped, wrapped)
	}
	assertTarget(t, actionConflict.Target, map[string]any{
		"conflict":      "rebase",
		"repo":          "repo-a",
		"branch":        "feature/rebase",
		"rebase_target": "main",
	})
	files, ok := actionConflict.Target["conflict_files"].([]string)
	if !ok || !reflect.DeepEqual(files, []string{"a.go", "b.go"}) {
		t.Fatalf("conflict_files = %#v; want a.go,b.go", actionConflict.Target["conflict_files"])
	}
}

func newPublishActionTarget(t *testing.T) (serverMutationTarget, *feature.Manager, *feature.Store, *feature.Feature) {
	t.Helper()
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("publish via REST", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil)
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
		ff.RepoStates = map[string]*feature.RepoState{"repo-a": {Touched: true}}
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
	orch := orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})
	return serverMutationTarget{orch: orch, store: store}, manager, store, f
}

func newReviewCommentsActionTarget(t *testing.T) (serverMutationTarget, *feature.Store, *feature.Feature) {
	t.Helper()
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("review comments via REST", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		publishable := true
		ff.Status = feature.StatusPublished
		ff.CurrentPhase = feature.PhasePublish
		for i := range ff.Repos {
			ff.Repos[i].Publishable = &publishable
		}
		ff.RepoStates = map[string]*feature.RepoState{"repo-a": {Touched: true, PRURL: "https://github.com/acme/repo-a/pull/12"}}
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load prepared feature: %v", err)
	}
	reviewer := &fakeReviewCommentOperator{comments: []ports.ReviewComment{
		reviewComment(100, "already addressed"),
		reviewComment(101, "please fix"),
	}}
	orch := orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})
	return serverMutationTarget{orch: orch, store: store, reviewer: reviewer}, store, loaded
}

func newTweakFinishActionTarget(t *testing.T, dirty bool) (serverMutationTarget, *orchestrator.Orchestrator, *mocks.MockPublisher, *feature.Feature) {
	t.Helper()
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("tweak finish via REST", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPublished
		ff.CurrentPhase = feature.PhasePublish
		ff.Repos[0].WorktreePath = filepath.Join(runtimeDir, "repo-a-worktree")
		ff.Repos[0].Branch = "feature/tweak-finish"
		ff.ActiveCycle = &feature.CycleState{Type: feature.CycleTweak, Status: feature.RepoCycleRunning, Count: 1}
		ff.RepoCycles = map[string]*feature.RepoCycleState{
			"repo-a": {Type: feature.CycleTweak, Status: feature.RepoCycleRunning, Count: 1},
		}
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load prepared feature: %v", err)
	}
	publisher := mocks.NewMockPublisher()
	publisher.HasUncommittedChangesFn = func(string) (bool, error) { return dirty, nil }
	pr := &agent.PhaseRunner{
		FeatureStore: store,
		StateDir:     store.BaseDir,
		RunFinalReviewFn: func(agent.OrchestratorConfig, ports.SessionManager) (*agent.FeatureFinalReviewResult, error) {
			return &agent.FeatureFinalReviewResult{FinalStatus: "review_passed"}, nil
		},
	}
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   manager,
		Store:       store,
		Publisher:   publisher,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})
	return serverMutationTarget{orch: orch, store: store}, orch, publisher, loaded
}

func newCleanupActionTarget(t *testing.T) (serverMutationTarget, *feature.Store, *feature.Feature, *fakeWorktreeOperator) {
	t.Helper()
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	worktrees := &fakeWorktreeOperator{}
	manager.Worktrees = worktrees
	f, err := manager.Create("cleanup via REST", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPublished
		ff.CurrentPhase = feature.PhasePublish
		ff.Repos[0].WorktreePath = "/tmp/repo-a-worktree"
		ff.RepoCycles = map[string]*feature.RepoCycleState{
			"repo-a": {Type: feature.CycleRebase, Status: feature.RepoCycleFailed},
		}
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load prepared feature: %v", err)
	}
	orch := orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})
	return serverMutationTarget{orch: orch, store: store}, store, loaded, worktrees
}

func countMockCalls(calls []mocks.MockCall, method string) int {
	count := 0
	for _, call := range calls {
		if call.Method == method {
			count++
		}
	}
	return count
}

type fakeReviewCommentOperator struct {
	comments   []ports.ReviewComment
	fetchCalls int
}

func (f *fakeReviewCommentOperator) FetchPRComments(string, string) ([]ports.ReviewComment, error) {
	f.fetchCalls++
	out := make([]ports.ReviewComment, len(f.comments))
	copy(out, f.comments)
	return out, nil
}

func (f *fakeReviewCommentOperator) ReplyToPRComment(string, string, int, string) error {
	return nil
}

func (f *fakeReviewCommentOperator) ReplyToIssueComment(string, string, string) error {
	return nil
}

func (f *fakeReviewCommentOperator) FetchReviewThreadMap(string, string) (map[int]string, error) {
	return nil, nil
}

func (f *fakeReviewCommentOperator) ResolveReviewThread(string, string) error {
	return nil
}

func (f *fakeReviewCommentOperator) LatestCommitSHA(string) (string, error) {
	return "", nil
}

func reviewComment(id int, body string) ports.ReviewComment {
	comment := ports.ReviewComment{
		ID:        id,
		Path:      "main.go",
		Line:      12,
		Body:      body,
		CreatedAt: "2026-06-13T12:00:00Z",
		Type:      ports.CommentTypeReview,
	}
	comment.User.Login = "reviewer"
	return comment
}

type fakeWorktreeOperator struct {
	removeCalls []fakeWorktreeRemoveCall
}

type fakeWorktreeRemoveCall struct {
	path         string
	deleteBranch bool
}

func (f *fakeWorktreeOperator) Create(string, string, string, string) (string, error) {
	return "", nil
}

func (f *fakeWorktreeOperator) Remove(path string, deleteBranch bool) error {
	f.removeCalls = append(f.removeCalls, fakeWorktreeRemoveCall{path: path, deleteBranch: deleteBranch})
	return nil
}

func (f *fakeWorktreeOperator) ResetToBase(string, string) error {
	return nil
}

func (f *fakeWorktreeOperator) ResetToBaseLocal(string, string) error {
	return nil
}

func (f *fakeWorktreeOperator) ResetToCommit(string, string) error {
	return nil
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
	id           string
	featureID    string
	phase        feature.Phase
	status       ports.SessionStatus
	active       bool
	pending      []*llm.ControlRequestMessage
	sentMessages []string
	controlCalls []mutationTargetControlCall
	askCalls     []mutationTargetAskUserCall
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

func (s *mutationTargetSessionView) ID() string                   { return s.id }
func (s *mutationTargetSessionView) FeatureID() string            { return s.featureID }
func (s *mutationTargetSessionView) Phase() feature.Phase         { return s.phase }
func (s *mutationTargetSessionView) RepoName() string             { return "" }
func (s *mutationTargetSessionView) PermCacheScope() string       { return "" }
func (s *mutationTargetSessionView) Kind() ports.SessionKind      { return ports.KindPhase }
func (s *mutationTargetSessionView) Label() string                { return "" }
func (s *mutationTargetSessionView) Status() ports.SessionStatus  { return s.status }
func (s *mutationTargetSessionView) IsActive() bool               { return s.active }
func (s *mutationTargetSessionView) Iteration() int               { return 0 }
func (s *mutationTargetSessionView) StartedAt() time.Time         { return time.Time{} }
func (s *mutationTargetSessionView) InitialPrompt() string        { return "" }
func (s *mutationTargetSessionView) ProviderName() string         { return "" }
func (s *mutationTargetSessionView) Model() string                { return "" }
func (s *mutationTargetSessionView) WorkDir() string              { return "" }
func (s *mutationTargetSessionView) MessageLog() ports.MessageLog { return nil }
func (s *mutationTargetSessionView) Cost() *llm.ResultMessage     { return nil }
func (s *mutationTargetSessionView) LatestUsage() *llm.Usage      { return nil }
func (s *mutationTargetSessionView) AccumulatedUsage() llm.Usage  { return llm.Usage{} }
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
		if pending != nil && pending.Request.ToolName == "AskUserQuestion" {
			return true
		}
	}
	return false
}
func (s *mutationTargetSessionView) SendUserMessage(text string) error {
	s.sentMessages = append(s.sentMessages, text)
	return nil
}
func (s *mutationTargetSessionView) RespondToControl(requestID string, allow bool, reason string) error {
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
