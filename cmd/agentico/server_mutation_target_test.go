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
			assertMetadata(t, result.Metadata, map[string]string{
				"request_id": "perm-1",
				"session_id": "session-permission",
				"decision":   tc.decision,
			})
			assertMetadataDoesNotContain(t, result.Metadata, "go test ./cmd/agentico")
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
	assertMetadata(t, result.Metadata, map[string]string{
		"request_id": "ask-1",
		"session_id": "session-ask",
	})
	assertMetadataDoesNotContain(t, result.Metadata, "Postgres with read replicas", "Dark launch first")
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
	assertMetadata(t, result.Metadata, map[string]string{
		"feature_id": "feat-help",
		"session_id": "session-help",
	})
	assertMetadataDoesNotContain(t, result.Metadata, "Please use the existing migration path.")
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
	assertMetadata(t, result.Metadata, map[string]string{"feature_id": "feat-need-input"})
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
	assertMetadata(t, result.Metadata, map[string]string{"status": "updated"})
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
	assertMetadata(t, result.Metadata, map[string]string{"status": "updated"})
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
	assertMetadata(t, result.Metadata, map[string]string{"status": "unchanged"})
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
	featureID := result.Metadata["feature_id"]
	if featureID == "" {
		t.Fatalf("result metadata = %v; want feature_id", result.Metadata)
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
	if len(created.Attachments) != 1 {
		t.Fatalf("created attachments = %v; want one copied attachment", created.Attachments)
	}
	copied, err := os.ReadFile(created.Attachments[0])
	if err != nil {
		t.Fatalf("ReadFile copied attachment: %v", err)
	}
	if string(copied) != "rest attachment" {
		t.Fatalf("copied attachment = %q; want original contents", copied)
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
	assertMetadata(t, result.Metadata, map[string]string{"feature_id": f.ID})

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
	assertMetadata(t, result.Metadata, map[string]string{
		"feature_id": f.ID,
		"action":     "publish",
		"status":     "published",
	})
	assertMetadataDoesNotContain(t, result.Metadata, "https://github.com/acme/repo-a/pull/12")
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
	var opFailure serverruntime.OperationFailureError
	if !errors.As(err, &opFailure) || opFailure.OperationFailure() == nil || opFailure.OperationFailure().Code != "conflict" {
		t.Fatalf("publishAction() operation failure = %+v; want conflict metadata wrapper", opFailure.OperationFailure())
	}
	assertMetadata(t, result.Metadata, map[string]string{
		"feature_id":     f.ID,
		"action":         "publish",
		"status":         "conflict",
		"conflict":       "publish",
		"repo":           "repo-a",
		"branch":         "feature/publish-conflict",
		"rebase_target":  "main",
		"conflict_files": "0",
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
	assertMetadata(t, result.Metadata, map[string]string{
		"feature_id":      f.ID,
		"action":          "rewind",
		"target_phase":    "implement",
		"effective_phase": "implement",
		"warning_count":   "0",
	})
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
	assertMetadata(t, result.Metadata, map[string]string{
		"feature_id":       f.ID,
		"action":           "rewind",
		"target_phase":     "inquire",
		"upgrade_pipeline": "large",
		"effective_phase":  feature.PhaseKnowledgeBase.DirName(),
		"status":           "rewound",
		"warning_count":    "0",
	})
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
	assertMetadata(t, result.Metadata, map[string]string{
		"feature_id":       f.ID,
		"action":           "rewind",
		"target_phase":     "inquire",
		"upgrade_pipeline": "large",
		"status":           "failed",
		"failed_stage":     "upgrade_pipeline",
	})
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
	assertMetadata(t, result.Metadata, map[string]string{
		"feature_id": f.ID,
		"phase":      feature.PhaseImplement.String(),
		"dispatch":   "phase",
	})
	assertMetadataDoesNotContain(t, result.Metadata, "do-not-leak", planPath)
}

func TestRuntimeOperationReconcilerMapsRecoveredFeatureState(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	saveRecoveredFeature := func(id string, status feature.Status, phase feature.Phase) {
		t.Helper()
		f := &feature.Feature{
			ID:                       id,
			Name:                     id,
			Slug:                     id,
			Status:                   status,
			CurrentPhase:             phase,
			ActiveRun:                3,
			RunCount:                 3,
			CurrentRoadmapPhase:      7,
			LastError:                "raw prompt secret=do-not-leak /tmp/private-agentico",
			PendingNeedUserInputPath: "/tmp/private-agentico/need-user-input.yaml",
		}
		if err := store.Save(f); err != nil {
			t.Fatalf("Save(%s) error = %v", id, err)
		}
	}
	saveRecoveredFeature("feat-running", feature.StatusImplementing, feature.PhaseImplement)
	saveRecoveredFeature("feat-failed", feature.StatusFailed, feature.PhasePlan)
	saveRecoveredFeature("feat-interrupted", feature.StatusInterrupted, feature.PhaseResearch)

	reconcile := runtimeOperationReconciler(store)
	tests := []struct {
		name       string
		record     serverruntime.OperationRecord
		wantStatus serverruntime.OperationStatus
		wantResult map[string]string
		wantErr    string
	}{
		{
			name: "running feature means dispatched mutation succeeded",
			record: serverruntime.OperationRecord{
				Kind:   "feature.restart",
				Status: serverruntime.OperationStatusRunning,
				Target: serverruntime.OperationTarget{Type: "feature", FeatureID: "feat-running", RunNumber: 2},
			},
			wantStatus: serverruntime.OperationStatusSucceeded,
			wantResult: map[string]string{
				"feature_id":    "feat-running",
				"status":        feature.StatusImplementing.String(),
				"phase":         feature.PhaseImplement.String(),
				"run_number":    "3",
				"roadmap_phase": "7",
			},
		},
		{
			name: "queued feature mutation did not start before restart",
			record: serverruntime.OperationRecord{
				Kind:   "feature.restart",
				Status: serverruntime.OperationStatusQueued,
				Target: serverruntime.OperationTarget{Type: "feature", FeatureID: "feat-running", RunNumber: 2},
			},
			wantStatus: serverruntime.OperationStatusInterrupted,
			wantErr:    "interrupted",
		},
		{
			name: "failed feature fails stale mutation",
			record: serverruntime.OperationRecord{
				Kind:   "feature.restart",
				Status: serverruntime.OperationStatusRunning,
				Target: serverruntime.OperationTarget{Type: "feature", FeatureID: "feat-failed"},
			},
			wantStatus: serverruntime.OperationStatusFailed,
			wantErr:    "failed",
		},
		{
			name: "interrupted feature interrupts stale non-stop mutation",
			record: serverruntime.OperationRecord{
				Kind:   "feature.restart",
				Status: serverruntime.OperationStatusRunning,
				Target: serverruntime.OperationTarget{Type: "feature", FeatureID: "feat-interrupted"},
			},
			wantStatus: serverruntime.OperationStatusInterrupted,
			wantErr:    "interrupted",
		},
		{
			name: "interrupted feature completes stale stop mutation",
			record: serverruntime.OperationRecord{
				Kind:   "feature.stop",
				Status: serverruntime.OperationStatusRunning,
				Target: serverruntime.OperationTarget{Type: "feature", FeatureID: "feat-interrupted"},
			},
			wantStatus: serverruntime.OperationStatusSucceeded,
			wantResult: map[string]string{
				"feature_id": "feat-interrupted",
				"status":     feature.StatusInterrupted.String(),
				"phase":      feature.PhaseResearch.String(),
				"run_number": "3",
				"action":     "stop",
			},
		},
		{
			name: "missing feature interrupts stale mutation with public id only",
			record: serverruntime.OperationRecord{
				Kind:   "feature.start",
				Status: serverruntime.OperationStatusRunning,
				Target: serverruntime.OperationTarget{Type: "feature", FeatureID: "feat-missing"},
			},
			wantStatus: serverruntime.OperationStatusInterrupted,
			wantErr:    "interrupted",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reconcile(tt.record)
			if got.Status != tt.wantStatus {
				t.Fatalf("runtimeOperationReconciler(%s).Status = %s, want %s", tt.record.Target.FeatureID, got.Status, tt.wantStatus)
			}
			for k, want := range tt.wantResult {
				if got.Result[k] != want {
					t.Fatalf("runtimeOperationReconciler(%s).Result[%s] = %q, want %q; result=%v", tt.record.Target.FeatureID, k, got.Result[k], want, got.Result)
				}
			}
			if tt.wantErr != "" {
				if got.Error == nil || got.Error.Code != tt.wantErr {
					t.Fatalf("runtimeOperationReconciler(%s).Error = %+v, want code %q", tt.record.Target.FeatureID, got.Error, tt.wantErr)
				}
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("Marshal reconciliation: %v", err)
			}
			for _, leaked := range []string{"do-not-leak", "raw prompt", "/tmp/private-agentico"} {
				if strings.Contains(string(raw), leaked) {
					t.Fatalf("runtimeOperationReconciler leaked %q in %s", leaked, raw)
				}
			}
		})
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
	assertMetadata(t, result.Metadata, map[string]string{
		"feature_id": f.ID,
		"action":     "review-comments",
		"cycle_type": string(feature.CycleReviewComments),
		"repo":       "repo-a",
		"mode":       "address_all",
		"status":     "failed",
	})
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
	assertMetadata(t, result.Metadata, map[string]string{
		"feature_id":    f.ID,
		"action":        "review-comments",
		"cycle_type":    string(feature.CycleReviewComments),
		"repo":          "repo-a",
		"mode":          "auto",
		"source":        "provided",
		"comment_count": "1",
		"status":        "failed",
	})
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
	assertMetadata(t, result.Metadata, map[string]string{
		"feature_id":  f.ID,
		"action":      "tweak.finish",
		"decision":    "final-review",
		"had_changes": "true",
		"status":      "review_started",
	})
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
		assertMetadata(t, result.Metadata, map[string]string{
			"feature_id": f.ID,
			"action":     "cleanup",
			"target":     "cycles",
			"status":     "cleaned",
		})
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
		assertMetadata(t, result.Metadata, map[string]string{
			"feature_id": f.ID,
			"action":     "cleanup",
			"target":     "worktrees",
			"status":     "cleaned",
		})
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
		assertMetadata(t, result.Metadata, map[string]string{
			"feature_id": f.ID,
			"action":     "delete",
			"status":     "deleted",
		})
		if len(worktrees.removeCalls) != 1 || worktrees.removeCalls[0].path != "/tmp/repo-a-worktree" || !worktrees.removeCalls[0].deleteBranch {
			t.Fatalf("worktree remove calls = %+v; want one branch-deleting delete", worktrees.removeCalls)
		}
		if _, err := store.Load(f.ID); err == nil {
			t.Fatalf("Load deleted feature error = nil; want missing feature")
		}
	})
}

func TestServerMutationTargetConflictMetadataWrapsRebaseFailure(t *testing.T) {
	err := &orchestrator.RebaseConflictError{
		FeatureID:     "feat-rebase",
		RepoName:      "repo-a",
		Branch:        "feature/rebase",
		RebaseTarget:  "main",
		ConflictFiles: []string{"a.go", "b.go"},
	}
	metadata := actionMetadata("feat-rebase", "rebase")
	addConflictMetadata(metadata, err)
	wrapped := operationFailureForMetadata(err, metadata)

	var rebaseConflict *orchestrator.RebaseConflictError
	if !errors.As(wrapped, &rebaseConflict) {
		t.Fatalf("wrapped error = %T %v; want RebaseConflictError", wrapped, wrapped)
	}
	var opFailure serverruntime.OperationFailureError
	if !errors.As(wrapped, &opFailure) || opFailure.OperationFailure() == nil {
		t.Fatalf("wrapped operation failure = %+v; want OperationFailureError", opFailure.OperationFailure())
	}
	failure := opFailure.OperationFailure()
	if failure.Code != "conflict" {
		t.Fatalf("operation failure code = %q; want conflict", failure.Code)
	}
	assertMetadata(t, failure.Metadata, map[string]string{
		"feature_id":     "feat-rebase",
		"action":         "rebase",
		"status":         "conflict",
		"conflict":       "rebase",
		"repo":           "repo-a",
		"branch":         "feature/rebase",
		"rebase_target":  "main",
		"conflict_files": "2",
	})
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
	sessions  []ports.SessionView
	stopCalls []string
	onStop    func(string)
}

func (m *mutationTargetSessionManager) StartSession(string, string, feature.Phase, []string, string, []string, ...*ports.SessionOpts) (ports.SessionHandle, error) {
	return nil, nil
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

func assertMetadata(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	for k, wantValue := range want {
		if got[k] != wantValue {
			t.Fatalf("metadata[%q] = %q, want %q; metadata = %v", k, got[k], wantValue, got)
		}
	}
}

func assertMetadataDoesNotContain(t *testing.T, metadata map[string]string, banned ...string) {
	t.Helper()
	for _, v := range metadata {
		for _, b := range banned {
			if b != "" && strings.Contains(v, b) {
				t.Fatalf("metadata leaked %q in %v", b, metadata)
			}
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
