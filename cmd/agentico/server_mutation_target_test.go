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

func mutationTargetOrchestrator(sessions ports.SessionManager) *orchestrator.Orchestrator {
	return orchestrator.New(orchestrator.Deps{Sessions: sessions}, orchestrator.Hooks{})
}

type mutationTargetSessionManager struct {
	sessions []ports.SessionView
}

func (m *mutationTargetSessionManager) StartSession(string, string, feature.Phase, []string, string, []string, ...*ports.SessionOpts) (ports.SessionHandle, error) {
	return nil, nil
}
func (m *mutationTargetSessionManager) StopSession(string) error { return nil }
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
