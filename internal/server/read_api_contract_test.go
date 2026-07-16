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

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// Fixture literals used repeatedly across this file's contract assertions.
const (
	// descriptionFieldKey, confidenceFieldKey, labelFieldKey, optionsFieldKey,
	// headerFieldKey, questionsFieldKey and questionFieldKey are JSON field
	// names reused across AskUserQuestion, rewind-option and
	// publish-description test fixtures.
	descriptionFieldKey = "description"
	confidenceFieldKey  = "confidence"
	labelFieldKey       = "label"
	optionsFieldKey     = "options"
	headerFieldKey      = "header"
	questionsFieldKey   = "questions"
	questionFieldKey    = "question"
	// secretTokenLiteral is a fake secret value asserted to never leak into
	// responses, alongside testRepoPath/worktreePathLiteral/secretBranchLiteral.
	secretTokenLiteral = "private-token"
	// chatLabel is the shared kind/label value for chat-prompt fixtures.
	chatLabel = "chat"
	// controlSubtypeCanUseTool is the llm.ControlRequest.Subtype value for a
	// tool-permission control request.
	controlSubtypeCanUseTool = "can_use_tool"
	// gitWorktreeAddFailedMsg is the fake LastError value used across
	// worktree-setup-failure fixtures.
	gitWorktreeAddFailedMsg = "git worktree add failed"
	// modelOpus1M is a fake extended-context model ID used across
	// ModelConfig fixtures.
	modelOpus1M = "opus[1m]"
	// artifactIDRoadmap is the fake roadmap artifact/phase ID used across
	// artifact-read fixtures in this file.
	artifactIDRoadmap = "roadmap"
	// fixtureAskSessionID is the fake AskUserQuestion session ID used across
	// ask-user fixtures in this file.
	fixtureAskSessionID = "sess-ask"
	// fixtureTaskID is the fake llm task ID used across task-event fixtures
	// in this file.
	fixtureTaskID = "task-1"
)

func TestReadAPISnapshotsRevisionAndStructuredErrors(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	handler := NewHandler(HandlerOptions{
		Runtime:               RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features:              store,
		DisableHostValidation: true,
	})

	dashboard := getJSONMap(t, handler, apiPathFeatures)
	meta := dashboard["meta"].(map[string]any)
	revision := meta["revision"].(string)
	if revision == "" {
		t.Fatal("dashboard revision is empty")
	}
	req := httptest.NewRequest(http.MethodGet, apiPathFeatures, nil)
	req.Header.Set("If-None-Match", `"`+revision+`"`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusNotModified {
		t.Fatalf("revalidated dashboard status = %d; want 304", w.Result().StatusCode)
	}
	rawDashboard := mustMarshalJSON(t, dashboard)
	for _, forbidden := range []string{testRepoPath, worktreePathLiteral, secretBranchLiteral, secretTokenLiteral, "permissions_queue"} {
		if strings.Contains(rawDashboard, forbidden) {
			t.Fatalf("dashboard leaks %q in %s", forbidden, rawDashboard)
		}
	}

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	if detail["api_version"] != APIVersion {
		t.Fatalf("detail api_version = %v; want %s", detail["api_version"], APIVersion)
	}
	if detail[entityFeature].(map[string]any)["id"] != f.ID {
		t.Fatalf("detail feature id = %v; want %s", detail[entityFeature], f.ID)
	}
	models := detail[entityFeature].(map[string]any)["models"].(map[string]any)
	if models["implementation"] != f.Models.Implementation {
		t.Fatalf("detail feature models.implementation = %v; want %s", models["implementation"], f.Models.Implementation)
	}
	rawDetail := mustMarshalJSON(t, detail)
	for _, forbidden := range []string{testRepoPath, worktreePathLiteral, secretBranchLiteral} {
		if strings.Contains(rawDetail, forbidden) {
			t.Fatalf("detail leaks storage/git field %q in %s", forbidden, rawDetail)
		}
	}

	errResp := requestJSONMap(t, handler, "/api/v1/features/../bad", http.StatusBadRequest)
	errDTO := errResp["error"].(map[string]any)
	if errDTO["code"] != "bad_request" || errDTO["status"].(float64) != http.StatusBadRequest {
		t.Fatalf("error DTO = %+v; want stable bad_request", errDTO)
	}
}

func TestFeatureAutomaticReviewReadModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mode        feature.AutomaticReviewMode
		global      bool
		wantEnabled bool
		wantSource  string
	}{
		{"default global off", feature.AutomaticReviewDefault, false, false, "global"},
		{"default global on", feature.AutomaticReviewDefault, true, true, "global"},
		{"feature enabled", feature.AutomaticReviewEnabled, false, true, "feature"},
		{"feature disabled", feature.AutomaticReviewDisabled, true, false, "feature"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, f := seedReadFeature(t)
			f.AutomaticReviewMode = feature.PersistAutomaticReviewMode(tt.mode)
			if err := store.Save(f); err != nil {
				t.Fatalf("Save: %v", err)
			}
			opts := baseReadHandlerOptions(store)
			opts.Config = &config.Config{Defaults: config.DefaultsConfig{AutomaticReviewEnabled: tt.global}}
			handler := NewHandler(opts)

			configBody := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/config")
			current := configBody["current"].(map[string]any)
			if got := current["automatic_review_mode"]; got != string(tt.mode) {
				t.Errorf("config automatic_review_mode = %v, want %q", got, tt.mode)
			}

			detailBody := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
			detail := detailBody[entityFeature].(map[string]any)
			state := detail["automatic_review"].(map[string]any)
			if got := state["mode"]; got != string(tt.mode) {
				t.Errorf("detail automatic_review.mode = %v, want %q", got, tt.mode)
			}
			if got := state["enabled"]; got != tt.wantEnabled {
				t.Errorf("detail automatic_review.enabled = %v, want %v", got, tt.wantEnabled)
			}
			if got := state["source"]; got != tt.wantSource {
				t.Errorf("detail automatic_review.source = %v, want %q", got, tt.wantSource)
			}
		})
	}
}

func TestFeatureDetailIncludesDurableSetupFailureState(t *testing.T) {
	t.Parallel()
	store := feature.NewStore(t.TempDir())
	now := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	f := &feature.Feature{
		ID:            "feat-setup-failed",
		Name:          "Setup Failed",
		Slug:          "setup-failed",
		Status:        feature.StatusFailed,
		CurrentPhase:  feature.PhasePlan,
		Created:       now,
		ActiveRun:     1,
		RunCount:      1,
		FailureType:   feature.FailureWorktreeSetup,
		LastError:     gitWorktreeAddFailedMsg,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         []feature.FeatureRepo{{Name: "repo-a", Branch: "feature/setup-failed"}},
	}
	setup := feature.NewActiveSetupState(f.Repos, nil, nil, now)
	setup.Status = feature.SetupStatusFailed
	setup.CompletedAt = &now
	setup.LatestLogPath = "/tmp/agentico/setup.log"
	setup.LastError = gitWorktreeAddFailedMsg
	task := setup.Tasks["worktree:repo-a"]
	task.Status = feature.SetupStatusFailed
	task.Path = "/tmp/worktrees/setup-failed/repo-a"
	task.LastError = gitWorktreeAddFailedMsg
	setup.Tasks[task.Key] = task
	f.SetRun(&feature.Run{RunNumber: 1, Setup: setup, FailureType: feature.FailureWorktreeSetup})
	if err := store.Save(f); err != nil {
		t.Fatalf("Save feature: %v", err)
	}
	handler := NewHandler(HandlerOptions{Features: store, DisableHostValidation: true})

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	featureBody := detail[entityFeature].(map[string]any)
	active := featureBody["active_run_detail"].(map[string]any)
	setupBody, ok := active["setup"].(map[string]any)
	if !ok {
		t.Fatalf("active_run_detail = %+v, want setup object for durable setup failure", active)
	}
	if setupBody["status"] != string(feature.SetupStatusFailed) || setupBody["last_error"] != gitWorktreeAddFailedMsg || setupBody["latest_log_path"] == "" {
		t.Fatalf("setup = %+v, want failed setup diagnostic", setupBody)
	}
	tasks := setupBody["tasks"].(map[string]any)
	worktreeTask := tasks["worktree:repo-a"].(map[string]any)
	if worktreeTask["status"] != string(feature.SetupStatusFailed) || worktreeTask["last_error"] != gitWorktreeAddFailedMsg || worktreeTask["path"] == "" {
		t.Fatalf("worktree task = %+v, want failed task diagnostic", worktreeTask)
	}
}

func TestFeatureDetailSynthesizesCycleFromRepoCycleState(t *testing.T) {
	t.Parallel()

	store, f := seedReadFeature(t)
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusCodeReady
		ff.CurrentPhase = feature.PhasePublish
		ff.SetRefactorCount(1)
		ff.ActiveCycle = nil
		ff.SetActiveCycleType("")
		ff.RepoCycles = map[string]*feature.RepoCycleState{
			repoNameSelf: {Type: feature.CycleRefactor, Status: feature.RepoCycleRunning},
		}
		return nil
	}); err != nil {
		t.Fatalf("Modify() error = %v", err)
	}
	handler := NewHandler(HandlerOptions{
		Runtime:               RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features:              store,
		DisableHostValidation: true,
	})

	list := getJSONMap(t, handler, apiPathFeatures)
	summaries := list["features"].([]any)
	if len(summaries) != 1 {
		t.Fatalf("list features len = %d, want 1", len(summaries))
	}
	summaryDTO := summaries[0].(map[string]any)
	summaryCycle, ok := summaryDTO["cycle"].(map[string]any)
	if !ok {
		t.Fatalf("summary feature cycle missing in %+v", summaryDTO)
	}
	if summaryCycle["type"] != actionRefactor || summaryCycle["status"] != feature.RepoCycleRunning || summaryCycle["count"].(float64) != 1 {
		t.Fatalf("summary feature cycle = %+v, want running refactor #1", summaryCycle)
	}

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	featureDTO := detail[entityFeature].(map[string]any)
	cycle, ok := featureDTO["cycle"].(map[string]any)
	if !ok {
		t.Fatalf("detail feature cycle missing in %+v", featureDTO)
	}
	if cycle["type"] != actionRefactor || cycle["status"] != feature.RepoCycleRunning || cycle["count"].(float64) != 1 {
		t.Fatalf("detail feature cycle = %+v, want running refactor #1", cycle)
	}
}

func TestFeatureDetailProjectsActiveFeatureRebaseOperation(t *testing.T) {
	store, f := seedReadFeature(t)
	f.ID = "feat-rebase"
	f.Status = feature.StatusCodeReady
	f.Repos = []feature.FeatureRepo{{Name: repoNameAPI}, {Name: repoNameWeb}}
	f.ActiveCycle = &feature.CycleState{Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 2}
	f.SetActiveCycleType(feature.CycleRebase)
	f.RebaseOperation = &feature.RebaseOperationState{
		Stage: feature.RebaseStageHarness,
		Repos: map[string]*feature.RebaseRepoProgress{
			repoNameAPI: {Status: feature.RebaseRepoStatusRebasing, RebaseTarget: "main"},
			repoNameWeb: {Status: feature.RebaseRepoStatusUpToDate, RebaseTarget: "main"},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	opts := baseReadHandlerOptions(store)
	opts.FeatureStore = store
	opts.Freshness = StaticFreshnessProvider(map[string]RepoFreshness{
		repoNameAPI: RepoFreshnessLocalChanges,
		repoNameWeb: RepoFreshnessInSync,
	})
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/features/feat-rebase")
	featureBody := body[entityFeature].(map[string]any)
	cycle := featureBody["cycle"].(map[string]any)
	if cycle["type"] != actionRebase || cycle["status"] != feature.RepoCycleRunning {
		t.Fatalf("cycle = %+v, want active rebase", cycle)
	}
	status := map[string]RepoStatusDTO{}
	for _, raw := range featureBody["repo_status"].([]any) {
		repo := raw.(map[string]any)
		name := repo["name"].(string)
		status[name] = RepoStatusDTO{
			Name:         name,
			Freshness:    repo["freshness"].(string),
			RebaseStatus: repo["rebase_status"].(string),
		}
	}
	if status[repoNameAPI].RebaseStatus != "rebasing" || status[repoNameAPI].Freshness != "local changes" {
		t.Fatalf("api status = %+v", status[repoNameAPI])
	}
	if status[repoNameWeb].RebaseStatus != "up_to_date" || status[repoNameWeb].Freshness != "in sync" {
		t.Fatalf("web status = %+v", status[repoNameWeb])
	}
}

func TestTranscriptDTOsAssignBlockIndexToConversationRows(t *testing.T) {
	t.Parallel()

	rows := transcriptDTOs([]llm.SDKMessage{{
		Type: roleAssistant,
		Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
			Role: roleAssistant,
			Content: []llm.ContentBlock{
				{Type: blockTypeText, Text: "first section"},
				{Type: blockTypeText, Text: "second section"},
			},
		}},
	}}, 7)

	if len(rows) != 2 {
		t.Fatalf("transcriptDTOs returned %d rows, want 2: %+v", len(rows), rows)
	}
	for i, row := range rows {
		if row.Index != 7 {
			t.Fatalf("row[%d].Index = %d, want 7", i, row.Index)
		}
		if row.BlockIndex != i {
			t.Fatalf("row[%d].BlockIndex = %d, want %d", i, row.BlockIndex, i)
		}
	}
	if rows[0].Text != "first section" || rows[1].Text != "second section" {
		t.Fatalf("transcript rows lost text order: %+v", rows)
	}
}

func TestTranscriptDTOsExposeStatusTextInPlace(t *testing.T) {
	t.Parallel()

	rows := transcriptDTOs([]llm.SDKMessage{
		{Type: roleAssistant, Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
			Role: roleAssistant, Content: []llm.ContentBlock{{Type: blockTypeText, Text: "before"}},
		}}},
		{Type: "status", Status: &llm.StatusMessage{Type: "status", Message: "Auto-approved Bash: go test ./..."}},
		{Type: roleAssistant, Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
			Role: roleAssistant, Content: []llm.ContentBlock{{Type: blockTypeToolUse, Name: toolNameBash}},
		}}},
	}, 4)

	if len(rows) != 3 {
		t.Fatalf("transcriptDTOs() returned %d rows, want 3: %+v", len(rows), rows)
	}
	status := rows[1]
	if status.Index != 5 || status.Role != roleSystem || status.Type != "status" || status.Text != "Auto-approved Bash: go test ./..." || status.Redacted {
		t.Fatalf("status row = %+v, want textual system status at index 5", status)
	}
}

func TestConfigCatalogPromptPermissionSnapshots(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	f.PendingNeedUserInputPath = filepath.Join(store.RunDir(f.ID, 1), "phase-02", targetPhaseImplement, "need-user-input.yaml")
	f.Pipeline = feature.PipelineMedium
	f.Checkpoints = feature.Checkpoints{InquiryReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true, DraftPublish: true}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	registry := llm.NewRegistry()
	registry.Register(fakeProvider{name: providerCodex, models: []string{modelGPT54, modelGPT54Mini}})
	askInput, err := json.Marshal(map[string]any{
		questionsFieldKey: []map[string]any{{
			questionFieldKey: askUserQuestionSampleText,
			optionsFieldKey: []map[string]any{{
				labelFieldKey: "A",
			}},
		}},
		"secret": secretTokenLiteral,
	})
	if err != nil {
		t.Fatalf("Marshal AskUser input: %v", err)
	}
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id: fixtureAskSessionID, featureID: f.ID, phase: feature.PhaseImplement, status: ports.SessionWaitingHelp,
			initialPrompt: "raw initial prompt with private-token",
			pending: []*llm.ControlRequestMessage{{
				Type:      transcriptTypeControlRequest,
				RequestID: "ask-1",
				Request: llm.ControlRequest{
					Subtype:  controlSubtypeCanUseTool,
					ToolName: toolNameAskUserQuestion,
					Input:    askInput,
				},
			}},
		},
		&fakeSessionView{
			id: "sess-perm", featureID: f.ID, phase: feature.PhasePlan, status: ports.SessionWaitingPermission,
			pending: []*llm.ControlRequestMessage{{
				Type:      transcriptTypeControlRequest,
				RequestID: fixturePermissionRequestID,
				Request:   llm.ControlRequest{Subtype: controlSubtypeCanUseTool, ToolName: toolNameBash, Input: json.RawMessage(`{"command":"echo private-token"}`)},
			}},
		},
	}}
	opts := baseReadHandlerOptions(store)
	opts.Config = &config.Config{
		Defaults: config.DefaultsConfig{
			Models:       config.ModelConfig{Research: modelOpus1M, Planning: modelOpus1M, Implementation: modelOpus1M, Review: modelGPT54, Utilities: modelSonnet, KBBuild: modelOpus1M},
			Inquireness:  "high",
			Pipeline:     string(feature.PipelineLarge),
			ExitCriteria: "private-token should not leak",
		},
		Repos:          map[string]config.RepoConfig{repoNameSelf: {Path: testRepoPath}},
		WorkspaceRoots: []string{"/workspace"},
	}
	opts.Registry = registry
	opts.Sessions = sessions
	handler := NewHandler(opts)

	for _, path := range []string{
		apiPathConfigRuntime,
		"/api/v1/features/" + f.ID + "/config",
		apiPathCatalogModels,
		apiPathPrompts,
		apiPathPermissions,
	} {
		body := getJSONMap(t, handler, path)
		raw := mustMarshalJSON(t, body)
		if strings.Contains(raw, secretTokenLiteral) || strings.Contains(raw, "raw initial prompt") {
			t.Fatalf("%s leaks sensitive data in %s", path, raw)
		}
		if body["api_version"] != APIVersion {
			t.Fatalf("%s api_version = %v; want %s", path, body["api_version"], APIVersion)
		}
	}
	featureCfg := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/config")
	original := featureCfg["original"].(map[string]any)
	if original["pipeline"] != string(feature.PipelineMedium) {
		t.Fatalf("feature config original pipeline = %v; want %s", original["pipeline"], feature.PipelineMedium)
	}
	if original["inquireness"] != string(f.Inquireness) {
		t.Fatalf("feature config original inquireness = %v; want %s", original["inquireness"], f.Inquireness)
	}
	checkpoints := original["checkpoints"].(map[string]any)
	if checkpoints["roadmap_review"] != true || checkpoints["phase_plan_review"] != true || checkpoints["draft_publish"] != true || checkpoints["inquiry_review"] == true {
		t.Fatalf("feature config original checkpoints = %+v; want medium-normalized persisted checkpoints", checkpoints)
	}
	prompts := getJSONMap(t, handler, apiPathPrompts)
	asks := prompts["ask_user_questions"].([]any)
	if len(asks) != 1 {
		t.Fatalf("ask_user_questions length = %d; want 1", len(asks))
	}
	ask := asks[0].(map[string]any)
	questions := ask[questionsFieldKey].([]any)
	if len(questions) != 1 {
		t.Fatalf("ask_user questions length = %d; want 1", len(questions))
	}
	question := questions[0].(map[string]any)
	if question[questionFieldKey] != askUserQuestionSampleText {
		t.Fatalf("ask_user question = %v; want Choose?", question[questionFieldKey])
	}
	options := question[optionsFieldKey].([]any)
	if len(options) != 1 || options[0].(map[string]any)[labelFieldKey] != "A" {
		t.Fatalf("ask_user options = %+v; want sanitized option A", options)
	}
	gates := prompts["need_user_inputs"].([]any)
	if len(gates) != 1 {
		t.Fatalf("need_user_inputs length = %d; want 1", len(gates))
	}
	gate := gates[0].(map[string]any)
	if gate["feature_id"] != f.ID {
		t.Fatalf("need user input feature_id = %v; want %s", gate["feature_id"], f.ID)
	}
	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	detailGate := detail[entityFeature].(map[string]any)["need_user_input"].(map[string]any)
	if detailGate["feature_id"] != f.ID {
		t.Fatalf("detail need user input feature_id = %v; want %s", detailGate["feature_id"], f.ID)
	}
}

func TestNeedUserInputGateDTOsIncludeQuestionnaireAndCycleRouting(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)

	featureGatePath := filepath.Join(store.RunDir(f.ID, 1), "phase-02", targetPhaseImplement, "iteration-03", "need-user-input.yaml")
	if err := agent.WriteNeedUserInputRecord(featureGatePath, agent.NeedUserInputRecord{
		Summary:   "Choose a persistence backend before implementation continues.",
		Iteration: 3,
		Questions: []agent.NeedUserInputQuestion{
			{Index: 1, Prompt: "Which database should implementation use?", Answer: "Postgres"},
			{Index: 2, Prompt: "Should we migrate existing data?", Answer: ""},
		},
	}); err != nil {
		t.Fatalf("WriteNeedUserInputRecord(feature gate) error = %v", err)
	}
	f.PendingNeedUserInputPath = featureGatePath
	f.CurrentIteration = 3

	cycleGatePath := filepath.Join(store.RunDir(f.ID, 1), "cycles", repoNameSelf, "iteration-02", "need-user-input.yaml")
	if err := agent.WriteNeedUserInputRecord(cycleGatePath, agent.NeedUserInputRecord{
		Summary:   "Resolve review comment policy.",
		Iteration: 2,
		Questions: []agent.NeedUserInputQuestion{
			{Index: 1, Prompt: "Reply now or leave unresolved?", Answer: "Reply now"},
		},
	}); err != nil {
		t.Fatalf("WriteNeedUserInputRecord(cycle gate) error = %v", err)
	}
	f.RepoCycles = map[string]*feature.RepoCycleState{
		repoNameSelf: {
			Type:                     feature.CycleReviewComments,
			Status:                   feature.RepoCycleNeedUserInput,
			Iteration:                2,
			PendingNeedUserInputPath: cycleGatePath,
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	handler := NewHandler(baseReadHandlerOptions(store))

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	detailGate := detail[entityFeature].(map[string]any)["need_user_input"].(map[string]any)
	if detailGate["summary"] != "Choose a persistence backend before implementation continues." {
		t.Fatalf("detail gate summary = %v", detailGate["summary"])
	}
	detailQuestions := detailGate["questions"].([]any)
	if len(detailQuestions) != 2 {
		t.Fatalf("detail gate questions length = %d; want 2", len(detailQuestions))
	}
	firstDetailQuestion := detailQuestions[0].(map[string]any)
	if firstDetailQuestion["index"] != float64(1) ||
		firstDetailQuestion["prompt"] != "Which database should implementation use?" ||
		firstDetailQuestion["answer"] != "Postgres" {
		t.Fatalf("detail gate first question = %+v", firstDetailQuestion)
	}

	prompts := getJSONMap(t, handler, apiPathPrompts)
	gates := prompts["need_user_inputs"].([]any)
	if len(gates) != 2 {
		t.Fatalf("need_user_inputs length = %d; want feature and cycle gates", len(gates))
	}
	var cycleGate map[string]any
	for _, raw := range gates {
		gate := raw.(map[string]any)
		if gate["repo_name"] == repoNameSelf && gate["cycle_type"] == string(feature.CycleReviewComments) {
			cycleGate = gate
			break
		}
	}
	if cycleGate == nil {
		t.Fatalf("need_user_inputs = %+v; want gate with repo/cycle routing", gates)
	}
	cycleQuestions := cycleGate["questions"].([]any)
	if len(cycleQuestions) != 1 || cycleQuestions[0].(map[string]any)["answer"] != "Reply now" {
		t.Fatalf("cycle gate questions = %+v", cycleQuestions)
	}
}

func TestPromptSnapshotPreservesReadableAskUserQuestionText(t *testing.T) {
	t.Parallel()

	store, f := seedReadFeature(t)
	longQuestion := "Should desktop app label names that match what is displayed on screen, including In Progress, Published, Watch, Answer, Approve, and Publish as PR, be translated into the target language or kept in English so the reader can map the README back to the live interface without losing important workflow context?"
	longLabel := "Translate visible desktop app labels too, including every status badge, button label, and action description that directly corresponds to on-screen text"
	longDescription := "Translate all prose including desktop app labels. The README is a localized document, and describing what the screen says in English breaks immersion even though the reader can still match the workflow by position, status, and surrounding context."
	input, err := json.Marshal(map[string]any{
		questionsFieldKey: []map[string]any{{
			questionFieldKey: longQuestion,
			optionsFieldKey: []map[string]any{{
				labelFieldKey:       longLabel,
				descriptionFieldKey: longDescription,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("Marshal AskUser input: %v", err)
	}
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id: fixtureAskSessionID, featureID: f.ID, phase: feature.PhaseDesign, status: ports.SessionWaitingHelp,
			pending: []*llm.ControlRequestMessage{{
				Type:      transcriptTypeControlRequest,
				RequestID: "ask-long",
				Request: llm.ControlRequest{
					Subtype:  controlSubtypeCanUseTool,
					ToolName: toolNameAskUserQuestion,
					Input:    input,
				},
			}},
		},
	}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	prompts := getJSONMap(t, handler, apiPathPrompts)
	asks := prompts["ask_user_questions"].([]any)
	if len(asks) != 1 {
		t.Fatalf("ask_user_questions length = %d; want 1", len(asks))
	}
	ask := asks[0].(map[string]any)
	if _, ok := ask["input"]; ok {
		t.Fatalf("AskUser prompt snapshot exposed raw input: %+v", ask["input"])
	}
	questions := ask[questionsFieldKey].([]any)
	if len(questions) != 1 {
		t.Fatalf("ask_user questions length = %d; want 1", len(questions))
	}
	question := questions[0].(map[string]any)
	if got := question[questionFieldKey]; got != longQuestion {
		t.Fatalf("ask_user question = %q; want full question %q", got, longQuestion)
	}
	options := question[optionsFieldKey].([]any)
	if len(options) != 1 {
		t.Fatalf("ask_user options length = %d; want 1", len(options))
	}
	option := options[0].(map[string]any)
	if got := option[labelFieldKey]; got != longLabel {
		t.Fatalf("ask_user option label = %q; want full label %q", got, longLabel)
	}
	if got := option[descriptionFieldKey]; got != longDescription {
		t.Fatalf("ask_user option description = %q; want full description %q", got, longDescription)
	}
}

func TestPromptSnapshotRecoversAskUserConfidenceFromAssistantToolUse(t *testing.T) {
	t.Parallel()

	store, f := seedReadFeature(t)
	question := "Which orthographic system should the Neapolitan translation follow?"
	optionDescriptions := []string{
		"The 400-year-old literary tradition.",
		"The most recent academically credible guide.",
		"Use Historical-Literary conventions for prose but De Blasi & Montuori 2020 for technical terms.",
	}
	strippedInput := json.RawMessage(mustMarshalJSON(t, map[string]any{
		questionsFieldKey: []map[string]any{{
			questionFieldKey: question,
			headerFieldKey:   "Orthography",
			"multi_select":   true,
			optionsFieldKey: []map[string]any{
				{labelFieldKey: "Historical-Literary (Recommended)", descriptionFieldKey: optionDescriptions[0]},
				{labelFieldKey: "De Blasi & Montuori 2020", descriptionFieldKey: optionDescriptions[1]},
				{labelFieldKey: "Hybrid", descriptionFieldKey: optionDescriptions[2]},
			},
		}},
	}))
	sourceInput := json.RawMessage(mustMarshalJSON(t, map[string]any{
		questionsFieldKey: []map[string]any{{
			questionFieldKey: question,
			headerFieldKey:   "Orthography",
			"multi_select":   true,
			optionsFieldKey: []map[string]any{
				{labelFieldKey: "Historical-Literary (Recommended)", descriptionFieldKey: optionDescriptions[0], confidenceFieldKey: 0.72},
				{labelFieldKey: "De Blasi & Montuori 2020", descriptionFieldKey: optionDescriptions[1], confidenceFieldKey: 0.21},
				{labelFieldKey: "Hybrid", descriptionFieldKey: optionDescriptions[2], confidenceFieldKey: 0.07},
			},
		}},
	}))
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id: fixtureAskSessionID, featureID: f.ID, phase: feature.PhaseDesign, status: ports.SessionWaitingHelp,
			messages: []llm.SDKMessage{{
				Type: roleAssistant,
				Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
					Role: roleAssistant,
					Content: []llm.ContentBlock{{
						Type:  blockTypeToolUse,
						ID:    "toolu-ask-1",
						Name:  toolNameAskUserQuestion,
						Input: sourceInput,
					}},
				}},
			}},
			pending: []*llm.ControlRequestMessage{{
				Type:      transcriptTypeControlRequest,
				RequestID: "ask-confidence",
				Request: llm.ControlRequest{
					Subtype:  controlSubtypeCanUseTool,
					ToolName: toolNameAskUserQuestion,
					Input:    strippedInput,
				},
			}},
		},
	}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	prompts := getJSONMap(t, handler, apiPathPrompts)
	asks := prompts["ask_user_questions"].([]any)
	if len(asks) != 1 {
		t.Fatalf("ask_user_questions length = %d; want 1", len(asks))
	}
	ask := asks[0].(map[string]any)
	if _, ok := ask["input"]; ok {
		t.Fatalf("AskUser prompt snapshot exposed raw input: %+v", ask["input"])
	}
	questions := ask[questionsFieldKey].([]any)
	if len(questions) != 1 {
		t.Fatalf("ask_user questions length = %d; want 1", len(questions))
	}
	questionDTO := questions[0].(map[string]any)
	if got := questionDTO["multi_select"]; got != true {
		t.Fatalf("ask_user multi_select = %v; want true in %+v", got, questionDTO)
	}
	options := questionDTO[optionsFieldKey].([]any)
	want := []float64{0.72, 0.21, 0.07}
	if len(options) != len(want) {
		t.Fatalf("ask_user options length = %d; want %d", len(options), len(want))
	}
	for i, wantConfidence := range want {
		option := options[i].(map[string]any)
		if got := option[confidenceFieldKey]; got != wantConfidence {
			t.Fatalf("option[%d] confidence = %v; want %.2f in %+v", i, got, wantConfidence, options)
		}
	}
}

func TestRuntimeConfigDiscoversWorkspaceRootReposOnRead(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, name := range []string{repoNameAPI, repoNameWeb, "worker"} {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatalf("mkdir git repo %s: %v", name, err)
		}
	}
	cfg := &config.Config{
		Repos:          map[string]config.RepoConfig{"explicit": {Path: "/explicit"}},
		WorkspaceRoots: []string{root},
	}
	handler := NewHandler(HandlerOptions{Config: cfg, DisableHostValidation: true})

	body := getJSONMap(t, handler, apiPathConfigRuntime)
	repos := body["repos"].([]any)
	names := make(map[string]bool, len(repos))
	for _, item := range repos {
		repo := item.(map[string]any)
		names[repo["name"].(string)] = true
	}
	for _, want := range []string{repoNameAPI, repoNameWeb, "worker", "explicit"} {
		if !names[want] {
			t.Fatalf("runtime config repos = %+v, want discovered repo %q", names, want)
		}
	}
}

func TestRuntimeConfigUsesDiscoveredPathForBlankExplicitRepo(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repoPath := filepath.Join(root, "bpfagent")
	if err := os.MkdirAll(filepath.Join(repoPath, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir git repo: %v", err)
	}
	cfg := &config.Config{
		Repos: map[string]config.RepoConfig{
			"bpfagent": {
				PipelineGates: map[string]config.Checkpoints{
					string(feature.PipelineMedium): {ManualPublish: true},
				},
			},
		},
		WorkspaceRoots: []string{root},
	}
	handler := NewHandler(HandlerOptions{Config: cfg, DisableHostValidation: true})

	body := getJSONMap(t, handler, apiPathConfigRuntime)
	repos := body["repos"].([]any)
	for _, item := range repos {
		repo := item.(map[string]any)
		if repo["name"] == "bpfagent" {
			if got := repo["path"]; got != repoPath {
				t.Fatalf("bpfagent path = %v, want discovered path %q", got, repoPath)
			}
			return
		}
	}
	t.Fatalf("runtime config repos = %+v, want bpfagent", repos)
}

func TestFeatureDetailActionCatalogStableAndRedacted(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPublished
		ff.CurrentPhase = feature.PhasePublish
		ff.RepoCycles = map[string]*feature.RepoCycleState{
			repoNameSelf: {
				Type:      feature.CycleReviewComments,
				Status:    feature.RepoCycleFailed,
				LastError: "private-token leaked in raw prompt payload",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("Modify() error = %v", err)
	}
	handler := NewHandler(baseReadHandlerOptions(store))

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	featureDTO := detail[entityFeature].(map[string]any)
	rawActions, ok := featureDTO["actions"].([]any)
	if !ok {
		t.Fatalf("detail actions missing or wrong type in %+v", featureDTO)
	}
	gotIDs := make([]string, 0, len(rawActions))
	for _, raw := range rawActions {
		action := raw.(map[string]any)
		gotIDs = append(gotIDs, action["id"].(string))
		if action["scope"].(map[string]any)["type"] != entityFeature {
			t.Fatalf("action scope = %+v; want feature scope", action["scope"])
		}
		if _, ok := action["required_inputs"].([]any); !ok {
			t.Fatalf("action %s missing required_inputs metadata", action["id"])
		}
	}
	wantIDs := []string{
		actionSetup,
		"start",
		actionPauseStop,
		actionResume,
		actionRestart,
		actionPublish,
		actionMerge,
		actionRewind,
		actionRebase,
		actionReviewComments,
		actionRefactor,
		actionRetry,
		actionMarkDone,
		actionCleanup,
		actionDelete,
	}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("action ids = %v; want %v", gotIDs, wantIDs)
	}
	actionsByID := map[string]map[string]any{}
	for _, rawAction := range rawActions {
		action := rawAction.(map[string]any)
		actionsByID[action["id"].(string)] = action
	}
	assertActionScope(t, actionsByID[actionPublish], "")
	assertActionInputNames(t, actionsByID[actionPublish])
	assertActionScope(t, actionsByID[actionMerge], "")
	assertActionInputNames(t, actionsByID[actionMerge])
	assertActionInputNames(t, actionsByID[actionRewind], "target_phase", "roadmap_phase", "upgrade_pipeline")
	assertActionInputRequired(t, actionsByID[actionRewind], "target_phase", true)
	assertActionInputRequired(t, actionsByID[actionRewind], "roadmap_phase", false)
	assertActionInputRequired(t, actionsByID[actionRewind], "upgrade_pipeline", false)
	assertActionScope(t, actionsByID[actionRebase], "")
	assertActionInputNames(t, actionsByID[actionRebase])
	assertActionScope(t, actionsByID[actionReviewComments], "required")
	assertActionInputNames(t, actionsByID[actionReviewComments], "repo", "mode")
	assertActionInputRequired(t, actionsByID[actionReviewComments], "repo", true)
	assertActionInputRequired(t, actionsByID[actionReviewComments], "mode", true)
	assertActionScope(t, actionsByID[actionRefactor], "optional")
	assertActionInputNames(t, actionsByID[actionRefactor], "repo", "prompt", "pipeline")
	assertActionInputRequired(t, actionsByID[actionRefactor], "repo", false)
	assertActionInputRequired(t, actionsByID[actionRefactor], "prompt", true)
	assertActionScope(t, actionsByID[actionCleanup], "")
	assertActionInputNames(t, actionsByID[actionCleanup], "target")
	assertActionScope(t, actionsByID[actionDelete], "")
	refactorPrompt := actionInputByName(t, actionsByID[actionRefactor], "prompt")
	if got := int(refactorPrompt["max_length"].(float64)); got != MaxActionTextBytes {
		t.Fatalf("refactor prompt max_length = %d; want %d", got, MaxActionTextBytes)
	}
	raw := mustMarshalJSON(t, rawActions)
	for _, forbidden := range []string{secretTokenLiteral, "raw prompt", testRepoPath, worktreePathLiteral} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("action catalog leaks %q in %s", forbidden, raw)
		}
	}
	var sawDisabledReason bool
	for _, rawAction := range rawActions {
		action := rawAction.(map[string]any)
		if action["enabled"] == true {
			continue
		}
		reasons := action["disabled_reasons"].([]any)
		if len(reasons) == 0 {
			t.Fatalf("disabled action %s has no disabled_reasons", action["id"])
		}
		reason := reasons[0].(map[string]any)
		if reason["code"] == "" || reason["message"] == "" {
			t.Fatalf("disabled reason = %+v; want machine-readable code and message", reason)
		}
		sawDisabledReason = true
	}
	if !sawDisabledReason {
		t.Fatalf("all actions enabled; want representative disabled reasons")
	}
}

func TestFeatureDetailActionCatalogStateMatrix(t *testing.T) {
	t.Parallel()
	publishable := true
	localOnly := false
	tests := []struct {
		name string
		f    *feature.Feature
		want map[string]struct {
			enabled      bool
			disabledCode string
		}
	}{
		{
			name: "publishable code ready",
			f:    actionCatalogTestFeature(feature.StatusCodeReady, feature.Checkpoints{}, &publishable, nil),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				actionPublish:  {enabled: true},
				actionMerge:    {disabledCode: disabledNotLocalOnly},
				actionRefactor: {disabledCode: disabledStatusNotAllowed},
			},
		},
		{
			name: "manual publish code ready",
			f:    actionCatalogTestFeature(feature.StatusCodeReady, feature.Checkpoints{ManualPublish: true}, &publishable, nil),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				actionPublish:  {disabledCode: "manual_publish_required"},
				actionRefactor: {enabled: true},
				actionMarkDone: {enabled: true},
			},
		},
		{
			name: "local only code ready",
			f:    actionCatalogTestFeature(feature.StatusCodeReady, feature.Checkpoints{}, &localOnly, nil),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				actionPublish: {disabledCode: "local_only"},
				actionMerge:   {enabled: true},
			},
		},
		{
			name: "local only created",
			f:    actionCatalogTestFeature(feature.StatusCreated, feature.Checkpoints{}, &localOnly, nil),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				actionMerge: {disabledCode: disabledStatusNotAllowed},
			},
		},
		{
			name: "medium created cannot rewind just because upgrade targets exist",
			f: func() *feature.Feature {
				f := actionCatalogTestFeature(feature.StatusCreated, feature.Checkpoints{}, &publishable, nil)
				f.Pipeline = feature.PipelineMedium
				return f
			}(),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				actionRewind: {disabledCode: "no_rewind_targets"},
			},
		},
		{
			name: "published",
			f:    actionCatalogTestFeature(feature.StatusPublished, feature.Checkpoints{}, &publishable, nil),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				actionPublish:  {disabledCode: "already_published"},
				actionMerge:    {disabledCode: disabledNotLocalOnly},
				actionRefactor: {enabled: true},
				actionCleanup:  {enabled: true},
			},
		},
		{
			name: "published active cycle",
			f: actionCatalogTestFeature(feature.StatusPublished, feature.Checkpoints{}, &publishable, map[string]*feature.RepoCycleState{
				repoNameSelf: {Type: feature.CycleReviewComments, Status: feature.RepoCycleRunning},
			}),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				actionRebase:         {disabledCode: disabledCycleActive},
				actionReviewComments: {disabledCode: disabledCycleActive},
				actionRefactor:       {disabledCode: disabledCycleActive},
			},
		},
		{
			name: "running cleanup disabled",
			f:    actionCatalogTestFeature(feature.StatusImplementing, feature.Checkpoints{}, &publishable, nil),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				actionCleanup: {disabledCode: feature.RepoCycleRunning},
				actionDelete:  {disabledCode: feature.RepoCycleRunning},
			},
		},
		{
			name: "medium plan review can still open rewind upgrade",
			f: func() *feature.Feature {
				f := actionCatalogTestFeature(feature.StatusPlanNeedsReview, feature.Checkpoints{}, &publishable, nil)
				f.Pipeline = feature.PipelineMedium
				return f
			}(),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				actionRewind: {enabled: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions := actionCatalogDTOs(tt.f)
			for id, want := range tt.want {
				got := actionDTOByID(t, actions, id)
				if got.Enabled != want.enabled {
					t.Fatalf("action %s enabled = %v; want %v", id, got.Enabled, want.enabled)
				}
				if want.enabled {
					continue
				}
				if len(got.DisabledReasons) == 0 {
					t.Fatalf("action %s disabled reasons empty; want %s", id, want.disabledCode)
				}
				if got.DisabledReasons[0].Code != want.disabledCode {
					t.Fatalf("action %s disabled code = %q; want %q", id, got.DisabledReasons[0].Code, want.disabledCode)
				}
			}
		})
	}
}

func TestFeatureDetailActionCatalogMediumPlanReviewAdvertisesPlanAndUpgradeTargets(t *testing.T) {
	t.Parallel()
	publishable := true
	f := actionCatalogTestFeature(feature.StatusPlanNeedsReview, feature.Checkpoints{}, &publishable, nil)
	f.Pipeline = feature.PipelineMedium

	rewind := actionDTOByID(t, actionCatalogDTOs(f), actionRewind)
	if !rewind.Enabled {
		t.Fatalf("rewind enabled = false; want true")
	}
	targetOptions := actionInputDTOByName(t, rewind, "target_phase").Options
	if got, want := strings.Join(targetOptions, ","), targetPhasePlan; got != want {
		t.Fatalf("target_phase options = %q; want %q", got, want)
	}
	upgradeOptions := actionInputDTOByName(t, rewind, "upgrade_pipeline").Options
	if got, want := strings.Join(upgradeOptions, ","), "large,moonshot"; got != want {
		t.Fatalf("upgrade_pipeline options = %q; want %q", got, want)
	}
}

func TestFeatureDetailActionCatalogDesignReviewExcludesUnstartedPlan(t *testing.T) {
	t.Parallel()
	publishable := true
	f := actionCatalogTestFeature(feature.StatusDesignNeedsReview, feature.Checkpoints{}, &publishable, nil)
	f.CurrentPhase = feature.PhaseDesign
	f.Pipeline = feature.PipelineMoonshot

	rewind := actionDTOByID(t, actionCatalogDTOs(f), actionRewind)
	if !rewind.Enabled {
		t.Fatalf("rewind enabled = false; want true")
	}
	targetOptions := actionInputDTOByName(t, rewind, "target_phase").Options
	if got, want := strings.Join(targetOptions, ","), "inquire,research,design"; got != want {
		t.Fatalf("target_phase options = %q; want %q", got, want)
	}
}

func TestPromptPermissionSnapshotsPreserveFIFOOrdering(t *testing.T) {
	t.Parallel()
	store, newest := seedReadFeature(t)
	oldest := cloneFeatureForReadTest(t, store, newest, "older-feature", "older feature")

	oldest.HelpQueue = []feature.HelpRequest{{
		Question: "oldest help",
		Time:     time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC),
		Pending:  true,
	}}
	newest.HelpQueue = []feature.HelpRequest{{
		Question: "newest help",
		Time:     time.Date(2026, 6, 13, 12, 5, 0, 0, time.UTC),
		Pending:  true,
	}}
	oldGatePath := filepath.Join(store.RunDir(oldest.ID, 1), "phase-02", targetPhaseImplement, "need-user-input.yaml")
	newGatePath := filepath.Join(store.RunDir(newest.ID, 1), "phase-02", targetPhaseImplement, "need-user-input.yaml")
	writeFile(t, oldGatePath, "questions: []\n")
	writeFile(t, newGatePath, "questions: []\n")
	oldGateTime := time.Date(2026, 6, 13, 12, 2, 0, 0, time.UTC)
	newGateTime := time.Date(2026, 6, 13, 12, 7, 0, 0, time.UTC)
	if err := os.Chtimes(oldGatePath, oldGateTime, oldGateTime); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", oldGatePath, err)
	}
	if err := os.Chtimes(newGatePath, newGateTime, newGateTime); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", newGatePath, err)
	}
	oldest.PendingNeedUserInputPath = oldGatePath
	newest.PendingNeedUserInputPath = newGatePath
	for _, f := range []*feature.Feature{oldest, newest} {
		if err := store.Save(f); err != nil {
			t.Fatalf("Save(%s) error = %v", f.ID, err)
		}
	}

	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id: "new-session", featureID: newest.ID, phase: feature.PhaseImplement, status: ports.SessionWaitingPermission,
			startedAt: time.Date(2026, 6, 13, 12, 10, 0, 0, time.UTC),
			pending: []*llm.ControlRequestMessage{
				pendingReadControl("new-perm", toolNameBash, `{}`),
				pendingReadControl("new-ask", toolNameAskUserQuestion, `{"questions":[{"question":"new ask"}]}`),
			},
		},
		&fakeSessionView{
			id: "old-session", featureID: oldest.ID, phase: feature.PhasePlan, status: ports.SessionWaitingHelp,
			startedAt: time.Date(2026, 6, 13, 12, 1, 0, 0, time.UTC),
			pending: []*llm.ControlRequestMessage{
				pendingReadControl("old-ask", toolNameAskUserQuestion, `{"questions":[{"question":"old ask"}]}`),
				pendingReadControl("old-perm", toolNameBash, `{}`),
			},
		},
	}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	prompts := getJSONMap(t, handler, apiPathPrompts)
	if got, want := stringFieldFromJSON(t, prompts["ask_user_questions"], requestIDKey), []string{"old-ask", "new-ask"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("prompt ask_user_questions order = %v; want %v", got, want)
	}
	if got, want := stringFieldFromJSON(t, prompts["help_queue"], questionFieldKey), []string{"oldest help", "newest help"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("prompt help_queue order = %v; want %v", got, want)
	}
	if got, want := stringFieldFromJSON(t, prompts["need_user_inputs"], "feature_id"), []string{oldest.ID, newest.ID}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("prompt need_user_inputs order = %v; want %v", got, want)
	}
	permissions := getJSONMap(t, handler, apiPathPermissions)
	if got, want := stringFieldFromJSON(t, permissions["requests"], requestIDKey), []string{"old-perm", "new-perm"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("permissions requests order = %v; want %v", got, want)
	}
	assertControlsHaveWaitingSince(t, prompts["ask_user_questions"])
	assertControlsHaveWaitingSince(t, permissions["requests"])
	assertGatesHaveWaitingSince(t, prompts["need_user_inputs"])
}

func TestPromptAndPermissionSnapshotsExposeStableWaitingSince(t *testing.T) {
	t.Parallel()

	store, f := seedReadFeature(t)
	legacyGatePath := filepath.Join(store.RunDir(f.ID, 1), "phase-02", targetPhaseImplement, "need-user-input.yaml")
	writeFile(t, legacyGatePath, "questions: []\n")
	legacyGateTime := time.Date(2026, 6, 13, 12, 2, 0, 0, time.UTC)
	if err := os.Chtimes(legacyGatePath, legacyGateTime, legacyGateTime); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", legacyGatePath, err)
	}
	f.PendingNeedUserInputPath = legacyGatePath
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(%s) error = %v", f.ID, err)
	}

	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id: "waiting-since-permission", featureID: f.ID, phase: feature.PhaseImplement, status: ports.SessionWaitingPermission,
			pending: []*llm.ControlRequestMessage{pendingReadControl("permission-waiting-since", toolNameBash, `{}`)},
		},
		&fakeSessionView{
			id: "waiting-since-question", featureID: f.ID, phase: feature.PhasePlan, status: ports.SessionWaitingHelp,
			pending: []*llm.ControlRequestMessage{pendingReadControl("question-waiting-since", toolNameAskUserQuestion, `{"questions":[{"question":"Which option?"}]}`)},
		},
	}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	firstPrompts := getJSONMap(t, handler, apiPathPrompts)
	secondPrompts := getJSONMap(t, handler, apiPathPrompts)
	firstPermissions := getJSONMap(t, handler, apiPathPermissions)
	secondPermissions := getJSONMap(t, handler, apiPathPermissions)

	firstGate := firstPrompts["need_user_inputs"].([]any)[0].(map[string]any)
	gateWaitingSince, err := time.Parse(time.RFC3339Nano, firstGate["waiting_since"].(string))
	if err != nil {
		t.Fatalf("Parse(waiting_since) error = %v", err)
	}
	if !gateWaitingSince.Equal(legacyGateTime) {
		t.Fatalf("legacy gate waiting_since = %v; want %v", gateWaitingSince, legacyGateTime)
	}
	if got, want := secondPrompts["need_user_inputs"].([]any)[0].(map[string]any)["waiting_since"], firstGate["waiting_since"]; got != want {
		t.Fatalf("gate waiting_since changed across snapshots: got %v; want %v", got, want)
	}

	assertStableControlWaitingSince(t, firstPrompts["ask_user_questions"], secondPrompts["ask_user_questions"])
	assertStableControlWaitingSince(t, firstPermissions["requests"], secondPermissions["requests"])
}

func assertStableControlWaitingSince(t *testing.T, first, second any) {
	t.Helper()
	firstRequests := first.([]any)
	secondRequests := second.([]any)
	if len(firstRequests) != 1 || len(secondRequests) != 1 {
		t.Fatalf("control request lengths = %d and %d; want 1", len(firstRequests), len(secondRequests))
	}
	firstWaitingSince := firstRequests[0].(map[string]any)["waiting_since"]
	if firstWaitingSince == nil || firstWaitingSince == "" {
		t.Fatalf("control request waiting_since = %v; want timestamp", firstWaitingSince)
	}
	if got := secondRequests[0].(map[string]any)["waiting_since"]; got != firstWaitingSince {
		t.Fatalf("control request waiting_since changed across snapshots: got %v; want %v", got, firstWaitingSince)
	}
}

func assertControlsHaveWaitingSince(t *testing.T, requests any) {
	t.Helper()
	for _, request := range requests.([]any) {
		if got := request.(map[string]any)["waiting_since"]; got == nil || got == "" {
			t.Fatalf("control request waiting_since = %v; want timestamp", got)
		}
	}
}

func assertGatesHaveWaitingSince(t *testing.T, gates any) {
	t.Helper()
	for _, gate := range gates.([]any) {
		if got := gate.(map[string]any)["waiting_since"]; got == nil || got == "" {
			t.Fatalf("need user input gate waiting_since = %v; want timestamp", got)
		}
	}
}

func TestPromptSnapshotIncludesWaitingHelpSessionWithoutControlRequest(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id:        "sess-waiting-help",
			featureID: f.ID,
			phase:     feature.PhaseDesign,
			status:    ports.SessionWaitingHelp,
			startedAt: time.Date(2026, 6, 13, 12, 3, 0, 0, time.UTC),
		},
	}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	prompts := getJSONMap(t, handler, apiPathPrompts)
	if got, want := stringFieldFromJSON(t, prompts["help_queue"], questionFieldKey), []string{agentQuestionPrompt}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("prompt help_queue = %v; want %v for WaitingHelp session without control request", got, want)
	}
	if got := stringFieldFromJSON(t, prompts["ask_user_questions"], requestIDKey); len(got) != 0 {
		t.Fatalf("prompt ask_user_questions = %v; want empty without control request", got)
	}
}

func TestPermissionSnapshotIncludesToolInputAndActionableSummary(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id: fixtureSessionID, featureID: f.ID, phase: feature.PhaseImplement, status: ports.SessionWaitingPermission,
			pending: []*llm.ControlRequestMessage{
				pendingReadControl(fixturePermissionRequestID, toolNameBash, `{"command":"go test ./internal/server"}`),
			},
		},
	}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	permissions := getJSONMap(t, handler, apiPathPermissions)
	requests := permissions["requests"].([]any)
	if len(requests) != 1 {
		t.Fatalf("permissions requests length = %d; want 1", len(requests))
	}
	request := requests[0].(map[string]any)
	if got, want := request["summary"], "go test ./internal/server"; got != want {
		t.Fatalf("permission summary = %v; want %q", got, want)
	}
	input := request["input"].(map[string]any)
	if got, want := input["command"], "go test ./internal/server"; got != want {
		t.Fatalf("permission input.command = %v; want %q", got, want)
	}
}

func TestPermissionSnapshotIncludesRememberPreview(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"command":"go test ./internal/server -run TestAPIApp"}`)
	store, f := seedReadFeature(t)
	sess := &fakeSessionView{
		id: "sess-remember", featureID: f.ID, phase: feature.PhaseImplement,
		status:         ports.SessionWaitingPermission,
		permCacheScope: repoNameSelf,
		pending: []*llm.ControlRequestMessage{{
			Type:      transcriptTypeControlRequest,
			RequestID: "perm-remember",
			Request: llm.ControlRequest{
				Subtype:  controlSubtypeCanUseTool,
				ToolName: "Bash",
				Input:    input,
			},
		}},
	}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = fakeSessionManager{views: []ports.SessionView{sess}}
	handler := NewHandler(opts)

	permissions := getJSONMap(t, handler, "/api/v1/permissions")
	requests := permissions["requests"].([]any)
	if len(requests) != 1 {
		t.Fatalf("permissions requests length = %d, want 1", len(requests))
	}
	req := requests[0].(map[string]any)
	remember := req["remember"].(map[string]any)
	if got, want := remember["pattern"], testRememberPattern; got != want {
		t.Fatalf("remember.pattern = %v, want %q", got, want)
	}
	if got, want := remember["scope"], repoNameSelf; got != want {
		t.Fatalf("remember.scope = %v, want %q", got, want)
	}
	if got, want := remember["scope_display"], "repo: agentic-orchestrator"; got != want {
		t.Fatalf("remember.scope_display = %v, want %q", got, want)
	}
}

func TestPermissionSnapshotRememberPreviewDoesNotLeakSensitiveInput(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"command":"echo private-token"}`)
	store, f := seedReadFeature(t)
	sess := &fakeSessionView{
		id: "sess-remember-secret", featureID: f.ID, phase: feature.PhaseImplement,
		status: ports.SessionWaitingPermission,
		pending: []*llm.ControlRequestMessage{{
			Type:      transcriptTypeControlRequest,
			RequestID: "perm-remember-secret",
			Request: llm.ControlRequest{
				Subtype:  controlSubtypeCanUseTool,
				ToolName: toolNameBash,
				Input:    input,
			},
		}},
	}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = fakeSessionManager{views: []ports.SessionView{sess}}
	handler := NewHandler(opts)

	permissions := getJSONMap(t, handler, apiPathPermissions)
	raw := mustMarshalJSON(t, permissions)
	if strings.Contains(raw, secretTokenLiteral) {
		t.Fatalf("permissions snapshot leaks private-token in %s", raw)
	}
	requests := permissions["requests"].([]any)
	if len(requests) != 1 {
		t.Fatalf("permissions requests length = %d, want 1", len(requests))
	}
	req := requests[0].(map[string]any)
	remember := req["remember"].(map[string]any)
	pattern, ok := remember["pattern"].(string)
	if !ok || pattern == "" {
		t.Fatalf("remember.pattern = %v, want non-empty string", remember["pattern"])
	}
	if strings.Contains(pattern, secretTokenLiteral) {
		t.Fatalf("remember.pattern leaks private-token: %q", pattern)
	}
	if got, want := pattern, "Bash(echo [redacted] *)"; got != want {
		t.Fatalf("remember.pattern = %v, want %q", got, want)
	}
}

func TestPermissionSnapshotRememberPreviewDoesNotLeakUnsafeRawInput(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		input json.RawMessage
	}{
		{name: "malformed_json", input: json.RawMessage(`{"command":"echo private-token"`)},
		{name: "non_object_json", input: json.RawMessage(`"private-token"`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, f := seedReadFeature(t)
			sess := &fakeSessionView{
				id: "sess-remember-unsafe-" + tt.name, featureID: f.ID, phase: feature.PhaseImplement,
				status: ports.SessionWaitingPermission,
				pending: []*llm.ControlRequestMessage{{
					Type:      transcriptTypeControlRequest,
					RequestID: "perm-remember-unsafe-" + tt.name,
					Request: llm.ControlRequest{
						Subtype:  controlSubtypeCanUseTool,
						ToolName: toolNameBash,
						Input:    tt.input,
					},
				}},
			}
			opts := baseReadHandlerOptions(store)
			opts.Sessions = fakeSessionManager{views: []ports.SessionView{sess}}
			handler := NewHandler(opts)

			permissions := getJSONMap(t, handler, apiPathPermissions)
			raw := mustMarshalJSON(t, permissions)
			if strings.Contains(raw, secretTokenLiteral) {
				t.Fatalf("permissions snapshot leaks private-token in %s", raw)
			}
			requests := permissions["requests"].([]any)
			if len(requests) != 1 {
				t.Fatalf("permissions requests length = %d, want 1", len(requests))
			}
			req := requests[0].(map[string]any)
			remember := req["remember"].(map[string]any)
			pattern, ok := remember["pattern"].(string)
			if !ok || pattern == "" {
				t.Fatalf("remember.pattern = %v, want non-empty string", remember["pattern"])
			}
			if strings.Contains(pattern, secretTokenLiteral) {
				t.Fatalf("remember.pattern leaks private-token: %q", pattern)
			}
			if got, want := pattern, "Bash(*)"; got != want {
				t.Fatalf("remember.pattern = %v, want %q", got, want)
			}
		})
	}
}

func TestFeatureDetailLoadsBoundedHistoricalRuns(t *testing.T) {
	t.Parallel()
	_, f := seedReadFeature(t)
	f.ActiveRun = 25
	f.RunCount = 25
	reader := &countingFeatureReader{feature: f}
	handler := NewHandler(HandlerOptions{
		Runtime:               RuntimeIdentity{RuntimeDir: runtimeDirLiteral, StateDir: "/state", Config: testRuntimeConfigPath},
		FeatureStore:          reader,
		DisableHostValidation: true,
	})

	body := getJSONMap(t, handler, "/api/v1/features/"+f.ID)

	const wantHistoryLimit = 5
	if len(reader.loadRunNumbers) > wantHistoryLimit {
		t.Fatalf("feature detail LoadRun calls = %v; want at most %d recent historical runs", reader.loadRunNumbers, wantHistoryLimit)
	}
	if got, want := intsToCSV(reader.loadRunNumbers), "20,21,22,23,24"; got != want {
		t.Fatalf("feature detail LoadRun calls = %s; want %s", got, want)
	}
	rawHistory := body[entityFeature].(map[string]any)["historical_runs"].([]any)
	gotRuns := make([]int, 0, len(rawHistory))
	for _, raw := range rawHistory {
		gotRuns = append(gotRuns, int(raw.(map[string]any)["run_number"].(float64)))
	}
	if got, want := intsToCSV(gotRuns), "20,21,22,23,24"; got != want {
		t.Fatalf("historical_runs run numbers = %s; want %s", got, want)
	}
}

func TestSessionListIncludesActiveAndRecentFeatureSessions(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	now := time.Date(2026, 6, 13, 13, 0, 0, 0, time.UTC)
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id: "sess-running", featureID: f.ID, phase: feature.PhaseImplement,
			kind: ports.KindPhase, status: ports.SessionRunning, startedAt: now.Add(-2 * time.Minute), runNumber: 3,
		},
		&fakeSessionView{
			id: "sess-completed", featureID: f.ID, phase: feature.PhasePlan,
			kind: ports.KindPhase, status: ports.SessionDone, startedAt: now.Add(-1 * time.Minute),
		},
		&fakeSessionView{
			id: "sess-failed", featureID: f.ID, phase: feature.PhaseResearch,
			kind: ports.KindPhase, status: ports.SessionFailed, startedAt: now.Add(-3 * time.Minute),
		},
	}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, apiPathSessions)
	rawSessions := body["sessions"].([]any)
	if len(rawSessions) != 3 {
		t.Fatalf("sessions length = %d; want active plus recent completed/failed sessions", len(rawSessions))
	}
	gotIDs := make([]string, 0, len(rawSessions))
	for _, raw := range rawSessions {
		gotIDs = append(gotIDs, raw.(map[string]any)["id"].(string))
	}
	wantIDs := []string{"sess-completed", "sess-running", "sess-failed"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("session order = %v; want %v", gotIDs, wantIDs)
	}
	if got := rawSessions[1].(map[string]any)["run_number"]; got != float64(3) {
		t.Fatalf("running session run_number = %v; want 3", got)
	}
}

func TestSessionListOmitsUnavailableContextPercentage(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	unavailable := -1
	opts := baseReadHandlerOptions(store)
	opts.Sessions = fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		id: "sess-restored", featureID: f.ID, phase: feature.PhaseImplement,
		kind: ports.KindPhase, status: ports.SessionRunning, contextPct: &unavailable,
	}}}

	body := getJSONMap(t, NewHandler(opts), apiPathSessions)
	session := body["sessions"].([]any)[0].(map[string]any)
	if got, present := session["context_percentage"]; present {
		t.Fatalf("context_percentage = %v; want omitted when usage is unavailable", got)
	}
}

func TestSessionListExposesChatSessionKind(t *testing.T) {
	t.Parallel()
	store, _ := seedReadFeature(t)
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id:        ChatSessionID,
			featureID: ChatSessionID,
			phase:     feature.PhaseResearch,
			kind:      ports.KindChat,
			label:     chatLabel,
			status:    ports.SessionWaitingHelp,
		},
		&fakeSessionView{
			id:        "chat-ask",
			featureID: ChatSessionID,
			phase:     feature.PhaseResearch,
			kind:      ports.KindChat,
			label:     chatLabel,
			status:    ports.SessionWaitingHelp,
			pending: []*llm.ControlRequestMessage{{
				RequestID: "ask-1",
				Request:   llm.ControlRequest{ToolName: toolNameAskUserQuestion},
			}},
		},
	}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, apiPathSessions)
	rawSessions := body["sessions"].([]any)
	if len(rawSessions) != 2 {
		t.Fatalf("sessions length = %d; want both chat sessions", len(rawSessions))
	}
	byID := map[string]map[string]any{}
	for _, raw := range rawSessions {
		session := raw.(map[string]any)
		byID[session["id"].(string)] = session
	}
	chat := byID[ChatSessionID]
	if chat["id"] != ChatSessionID || chat["feature_id"] != ChatSessionID {
		t.Fatalf("chat session identity = %+v; want stable chat identity", chat)
	}
	if chat["kind"] != chatLabel || chat[labelFieldKey] != chatLabel {
		t.Fatalf("chat session metadata = kind %v label %v; want chat/chat", chat["kind"], chat[labelFieldKey])
	}
	if chat["turn_state"] != "waiting_input" {
		t.Fatalf("chat turn_state = %v; want waiting_input", chat["turn_state"])
	}
	if got := byID["chat-ask"]["turn_state"]; got != "waiting_question" {
		t.Fatalf("chat AskUser turn_state = %v; want waiting_question", got)
	}
}

func TestSessionListUsesBoundedRecentSessionsWithoutFeatureScan(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	now := time.Date(2026, 6, 13, 13, 0, 0, 0, time.UTC)
	views := []ports.SessionView{&fakeSessionView{
		id: "sess-active-old", featureID: f.ID, phase: feature.PhaseImplement,
		kind: ports.KindPhase, status: ports.SessionRunning, startedAt: now.Add(-2 * time.Hour),
	}}
	for i := 0; i < 60; i++ {
		views = append(views, &fakeSessionView{
			id: "sess-done-" + twoDigit(i), featureID: f.ID, phase: feature.PhasePlan,
			kind: ports.KindPhase, status: ports.SessionDone, startedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	var featureSessionCalls int
	sessions := fakeSessionManager{views: views, featureSessionsCalls: &featureSessionCalls}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, apiPathSessions)

	if featureSessionCalls != 0 {
		t.Fatalf("FeatureSessions calls = %d; want 0 for bounded session list", featureSessionCalls)
	}
	rawSessions := body["sessions"].([]any)
	const wantRecentLimit = 50
	if got, want := len(rawSessions), wantRecentLimit+1; got != want {
		t.Fatalf("sessions length = %d; want %d active plus bounded recent sessions", got, want)
	}
	gotIDs := make([]string, 0, len(rawSessions))
	for _, raw := range rawSessions {
		gotIDs = append(gotIDs, raw.(map[string]any)["id"].(string))
	}
	for _, wantID := range []string{"sess-done-00", "sess-done-49", "sess-active-old"} {
		if !stringSliceContains(gotIDs, wantID) {
			t.Fatalf("session ids = %v; want %s included", gotIDs, wantID)
		}
	}
	if stringSliceContains(gotIDs, "sess-done-50") {
		t.Fatalf("session ids = %v; want sessions beyond recent limit excluded", gotIDs)
	}
}

func TestSessionDetailAndTranscriptDoNotScanFeatureSessionsOnLookupMiss(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	var featureSessionCalls int
	sessions := fakeSessionManager{
		views: []ports.SessionView{&fakeSessionView{
			id: "sess-historical", featureID: f.ID, phase: feature.PhaseImplement,
			kind: ports.KindPhase, status: ports.SessionDone,
		}},
		getSessionMiss:       true,
		featureSessionsCalls: &featureSessionCalls,
	}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	requestJSONMap(t, handler, "/api/v1/sessions/sess-historical", http.StatusNotFound)
	requestJSONMap(t, handler, "/api/v1/sessions/sess-historical/transcript", http.StatusNotFound)
	if featureSessionCalls != 0 {
		t.Fatalf("FeatureSessions calls = %d; want 0 for addressed session lookup miss", featureSessionCalls)
	}
}

func TestSessionTranscriptPreservesProtocolPromptsAndLocalUserEchoes(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	protocolPrompt := "Translate README in Neapolitan.\n" + strings.Repeat("Preserve this sentence. ", 30) + "tail marker"
	sessions := fakeSessionManager{
		views: []ports.SessionView{&fakeSessionView{
			id: "sess-local-echo", featureID: f.ID, phase: feature.PhaseImplement,
			kind: ports.KindPhase, status: ports.SessionRunning,
			messages: []llm.SDKMessage{
				{Type: roleUser, User: &llm.UserMessage{Message: llm.ConversationMsg{
					Role:    roleUser,
					Content: []llm.ContentBlock{{Type: blockTypeText, Text: protocolPrompt}},
				}}},
				{Type: roleUser, LocallyAppended: true, User: &llm.UserMessage{Message: llm.ConversationMsg{
					Role:    roleUser,
					Content: []llm.ContentBlock{{Type: blockTypeText, Text: "PostgreSQL"}},
				}}},
				{Type: roleUser, LocallyAppended: true, AutoPicked: true, AutoPickQuestion: "Which cache?", AutoPickConfidence: 0.72, User: &llm.UserMessage{Message: llm.ConversationMsg{
					Role:    roleUser,
					Content: []llm.ContentBlock{{Type: blockTypeText, Text: "Redis (Recommended)"}},
				}}},
			},
		}},
	}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/sessions/sess-local-echo/transcript")
	messages := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want protocol prompt, local echo, and auto-picked rows", len(messages))
	}
	protocol := messages[0].(map[string]any)
	if protocol[blockTypeText] != protocolPrompt || protocol["redacted"] == true || protocol["locally_appended"] == true {
		t.Fatalf("protocol user row = %+v, want visible non-local prompt", protocol)
	}
	local := messages[1].(map[string]any)
	if local[blockTypeText] != "PostgreSQL" || local["redacted"] == true || local["locally_appended"] != true {
		t.Fatalf("local user echo row = %+v, want visible locally-appended text", local)
	}
	autoPicked := messages[2].(map[string]any)
	if autoPicked[blockTypeText] != "Redis (Recommended)" || autoPicked["locally_appended"] != true || autoPicked["auto_picked"] != true || autoPicked["auto_pick_confidence"] != 0.72 || autoPicked["auto_pick_question"] != "Which cache?" {
		t.Fatalf("auto-picked user row = %+v, want visible auto-picked metadata", autoPicked)
	}
}

func TestSessionTranscriptDoesNotTruncateAssistantText(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	longAnswer := "The chat answer must stay complete for the AMA transcript.\n\n" +
		strings.Repeat("This sentence is part of the answer body and must remain visible. ", 12) +
		"tail marker"
	sessions := fakeSessionManager{
		views: []ports.SessionView{&fakeSessionView{
			id: "sess-chat-long-answer", featureID: f.ID, phase: feature.PhaseResearch,
			kind: ports.KindChat, status: ports.SessionRunning,
			messages: []llm.SDKMessage{{
				Type: roleAssistant,
				Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
					Role:    roleAssistant,
					Content: []llm.ContentBlock{{Type: blockTypeText, Text: longAnswer}},
				}},
			}},
		}},
	}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/sessions/sess-chat-long-answer/transcript")
	messages := body["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages length = %d, want assistant row", len(messages))
	}
	row := messages[0].(map[string]any)
	if row[blockTypeText] != longAnswer {
		t.Fatalf("assistant transcript text was truncated:\ngot  %q\nwant %q", row[blockTypeText], longAnswer)
	}
}

func TestLivePreviewUsesBoundedRecentSessionsWithoutFeatureScan(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	now := time.Date(2026, 6, 13, 13, 0, 0, 0, time.UTC)
	views := make([]ports.SessionView, 0, 60)
	for i := 0; i < 60; i++ {
		views = append(views, &fakeSessionView{
			id: "sess-done-" + twoDigit(i), featureID: f.ID, phase: feature.PhaseImplement,
			kind: ports.KindPhase, status: ports.SessionDone, startedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	var featureSessionCalls int
	var recentSessionLimits []int
	sessions := fakeSessionManager{
		views:                views,
		featureSessionsCalls: &featureSessionCalls,
		recentSessionLimits:  &recentSessionLimits,
	}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/live-preview")

	if featureSessionCalls != 0 {
		t.Fatalf("FeatureSessions calls = %d; want 0 for bounded live preview", featureSessionCalls)
	}
	if got, want := intsToCSV(recentSessionLimits), "50"; got != want {
		t.Fatalf("RecentSessions limits = %s; want %s", got, want)
	}
	rawSession, ok := body["session"].(map[string]any)
	if !ok {
		t.Fatalf("live preview session = %T; want object", body["session"])
	}
	if got, want := rawSession["id"], "sess-done-00"; got != want {
		t.Fatalf("live preview session id = %v; want %s", got, want)
	}
}

func TestLivePreviewIncludesExtendedTranscriptTailAndToolProgressRows(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	msgs := []llm.SDKMessage{{
		Type: transcriptTypeToolProgress,
		ToolProgress: &llm.ToolProgressMessage{
			Type:      transcriptTypeToolProgress,
			ToolUseID: "toolu-bash-1",
			ToolName:  toolNameBash,
			Data:      "private-token output must stay redacted",
		},
	}}
	for i := 1; i <= 6; i++ {
		msgs = append(msgs, llm.SDKMessage{
			Type: roleAssistant,
			Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
				Role:    roleAssistant,
				Content: []llm.ContentBlock{{Type: blockTypeText, Text: "preview line " + twoDigit(i)}},
			}},
		})
	}
	sessions := fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		id: "sess-live", featureID: f.ID, phase: feature.PhaseImplement,
		kind: ports.KindPhase, status: ports.SessionRunning, provider: providerCodex,
		messages: msgs,
	}}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	preview := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/live-preview")
	transcript := preview["transcript"].([]any)
	if len(transcript) != len(msgs) {
		t.Fatalf("live preview transcript len = %d; want %d rows (%+v)", len(transcript), len(msgs), transcript)
	}
	raw := mustMarshalJSON(t, preview)
	if !strings.Contains(raw, "preview line 01") || !strings.Contains(raw, "preview line 06") {
		t.Fatalf("live preview transcript did not include extended tail: %s", raw)
	}
	if strings.Contains(raw, secretTokenLiteral) {
		t.Fatalf("live preview transcript leaked tool progress output: %s", raw)
	}
	tool := transcript[0].(map[string]any)
	if tool["type"] != transcriptTypeToolProgress || tool["tool"] != toolNameBash || tool["redacted"] != true {
		t.Fatalf("tool progress transcript row = %+v; want redacted Bash tool_progress row", tool)
	}
}

func TestSessionTranscriptIncludesSanitizedFileChangeRows(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	msgs := []llm.SDKMessage{
		{Type: roleAssistant, Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
			Role: roleAssistant,
			Content: []llm.ContentBlock{{
				Type:  blockTypeToolUse,
				Name:  toolNameWrite,
				Input: json.RawMessage(`{"file_path":"docs/provider-notes.md","content":"# Provider notes\n\nUpdated for all providers.\n"}`),
			}},
		}}},
		{Type: transcriptTypeToolProgress, ToolProgress: &llm.ToolProgressMessage{
			Type:     transcriptTypeToolProgress,
			ToolName: toolNameBash,
			Data:     "private-token output must stay redacted",
		}},
	}
	sessions := fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		id: fixtureSessionID, featureID: f.ID, phase: feature.PhaseImplement,
		kind: ports.KindPhase, status: ports.SessionRunning, provider: providerCodex,
		messages: msgs,
	}}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/sessions/sess-1/transcript?limit=10")
	raw := mustMarshalJSON(t, body)
	for _, want := range []string{`"file_change"`, `"path":"docs/provider-notes.md"`, `"operation":"write"`, `"+ # Provider notes`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("transcript missing sanitized file change %q in %s", want, raw)
		}
	}
	if strings.Contains(raw, secretTokenLiteral) {
		t.Fatalf("transcript leaked redacted tool progress output: %s", raw)
	}
}

func TestSessionTranscriptIncludesStructuredCodexFileChangeDiffRows(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	msgs := []llm.SDKMessage{{
		Type: transcriptTypeToolProgress,
		ToolProgress: &llm.ToolProgressMessage{
			Type:      transcriptTypeToolProgress,
			ToolUseID: "call_write",
			ToolName:  toolNameWrite,
		},
		FileChanges: []llm.FileChangeEvent{{
			Path:         filepath.Join("/work/repo", "README.md"),
			Operation:    fileChangeOpUpdate,
			Detail:       "@@ -1,2 +1,2 @@\n-old\n+new\n",
			AddedLines:   1,
			RemovedLines: 1,
			HasDiffPatch: true,
		}},
	}}
	sessions := fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		id: fixtureSessionID, featureID: f.ID, phase: feature.PhaseImplement,
		kind: ports.KindPhase, status: ports.SessionRunning, provider: providerCodex,
		workDir:  "/work/repo",
		messages: msgs,
	}}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/sessions/sess-1/transcript?limit=10")
	raw := mustMarshalJSON(t, body)
	for _, want := range []string{`"file_change"`, `"path":"README.md"`, `"operation":"update"`, `"has_diff_patch":true`, `"added_lines":1`, `"removed_lines":1`, `-old`, `+new`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("transcript missing structured Codex file change %q in %s", want, raw)
		}
	}
}

func TestSessionTranscriptIncludesTaskLifecycleAndDelegationMetadata(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	msgs := []llm.SDKMessage{
		{Type: roleAssistant, Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
			Role: roleAssistant,
			Content: []llm.ContentBlock{{
				Type:  blockTypeToolUse,
				Name:  "Agent",
				Input: json.RawMessage(`{"description":"Explore KB completion handler","prompt":"Inspect KB docs and return impacted categories with private-token omitted."}`),
			}},
		}}},
		{Type: roleSystem, Subtype: transcriptTypeTaskStarted, TaskStarted: &llm.TaskStartedMessage{
			Type:        roleSystem,
			Subtype:     transcriptTypeTaskStarted,
			TaskID:      fixtureTaskID,
			Description: "inspect provider docs",
			TaskType:    "local_agent",
			Prompt:      "Read the provider docs and report every attach-view metadata gap with private-token omitted.",
		}},
		{Type: roleSystem, Subtype: transcriptTypeTaskProgress, TaskProgress: &llm.TaskProgressMessage{
			Type:         roleSystem,
			Subtype:      transcriptTypeTaskProgress,
			TaskID:       fixtureTaskID,
			Description:  "inspect provider docs",
			LastToolName: "Read",
		}},
		{Type: roleSystem, Subtype: transcriptTypeTaskNotification, TaskNotification: &llm.TaskNotificationMessage{
			Type:    roleSystem,
			Subtype: transcriptTypeTaskNotification,
			TaskID:  fixtureTaskID,
			Status:  "completed",
			Summary: "found API transcript gaps",
		}},
	}
	sessions := fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		id: fixtureSessionID, featureID: f.ID, phase: feature.PhaseImplement,
		kind: ports.KindPhase, status: ports.SessionRunning, provider: providerClaude,
		messages: msgs,
	}}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/sessions/sess-1/transcript?limit=10")
	raw := mustMarshalJSON(t, body)
	for _, want := range []string{
		`"type":"tool_use"`,
		`"tool":"Agent"`,
		`"tool_call"`,
		`"summary":"Explore KB completion handler"`,
		`"prompt":"Inspect KB docs and return impacted categories with [redacted] omitted."`,
		`"type":"task_started"`,
		`"type":"task_progress"`,
		`"type":"task_notification"`,
		`"task"`,
		`"description":"inspect provider docs"`,
		`"last_tool_name":"Read"`,
		`"status":"completed"`,
		`"summary":"found API transcript gaps"`,
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("transcript missing task/delegation metadata %q in %s", want, raw)
		}
	}
	if strings.Contains(raw, secretTokenLiteral) {
		t.Fatalf("transcript leaked unsanitized task/delegation prompt: %s", raw)
	}
}

func TestArtifactLogLivePreviewAndSessionReadsAreBoundedAndRedacted(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	runDir := store.RunDir(f.ID, 1)
	writeFile(t, filepath.Join(runDir, targetPhasePlan, "phase-plan.md"), "hello artifact content")
	writeFile(t, filepath.Join(runDir, "logs", "session.log"), "first\nsecond\nthird\n")
	msgs := []llm.SDKMessage{
		{Type: roleAssistant, Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{Role: roleAssistant, Content: []llm.ContentBlock{{Type: blockTypeText, Text: "safe text"}, {Type: blockTypeToolUse, Name: toolNameBash, Input: json.RawMessage(`{"command":"echo private-token"}`)}}}}},
		{Type: roleUser, User: &llm.UserMessage{Message: llm.ConversationMsg{Role: roleUser, Content: []llm.ContentBlock{{Type: blockTypeText, Text: "raw prompt private-token"}}}}},
	}
	sessions := fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		id: fixtureSessionID, featureID: f.ID, phase: feature.PhaseImplement, repoName: repoNameSelf,
		kind: ports.KindPhase, status: ports.SessionRunning, provider: providerCodex, model: modelGPT54,
		logPath: filepath.Join(runDir, "logs", "session.log"), messages: msgs,
		initialPrompt: "private-token initial prompt",
	}}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	list := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/artifacts")
	if got := len(list["artifacts"].([]any)); got == 0 {
		t.Fatal("artifact list is empty")
	}
	content := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/artifacts/plan?offset=6&limit=8")
	if content[blockTypeText] != "artifact" {
		t.Fatalf("artifact text slice = %q; want artifact", content[blockTypeText])
	}
	requestJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/artifacts/..%2Ffeature.yaml", http.StatusBadRequest)

	logBody := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/logs/session?offset=6&limit=6")
	if logBody[blockTypeText] != "second" {
		t.Fatalf("log text slice = %q; want second", logBody[blockTypeText])
	}
	preview := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/live-preview")
	if preview[entityFeature].(map[string]any)["id"] != f.ID {
		t.Fatalf("live preview feature = %+v; want %s", preview[entityFeature], f.ID)
	}
	detail := getJSONMap(t, handler, "/api/v1/sessions/sess-1")
	if got, want := detail["session"].(map[string]any)["initial_prompt"], "[redacted] initial prompt"; got != want {
		t.Fatalf("session detail initial_prompt = %q; want %q", got, want)
	}

	for _, path := range []string{apiPathSessions, "/api/v1/sessions/sess-1", "/api/v1/sessions/sess-1/transcript?limit=10"} {
		raw := mustMarshalJSON(t, getJSONMap(t, handler, path))
		if strings.Contains(raw, secretTokenLiteral) {
			t.Fatalf("%s leaks redacted content in %s", path, raw)
		}
	}
}

func TestArtifactReadsAllowAbsolutePathsWithinSameRun(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	runDir := store.RunDir(f.ID, 1)
	roadmapPath := filepath.Join(runDir, artifactIDRoadmap, "roadmap.md")
	writeFile(t, roadmapPath, "# Roadmap\n\nTranslate README.\n")
	f.Status = feature.StatusPlanNeedsReview
	f.CurrentPhase = feature.PhasePlan
	f.Artifacts = map[string]string{artifactIDRoadmap: roadmapPath}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	handler := NewHandler(baseReadHandlerOptions(store))

	list := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/artifacts")
	rawArtifacts := list["artifacts"].([]any)
	if len(rawArtifacts) != 1 {
		t.Fatalf("artifacts len = %d; want 1 (%+v)", len(rawArtifacts), rawArtifacts)
	}
	artifact := rawArtifacts[0].(map[string]any)
	if artifact["id"] != artifactIDRoadmap || artifact["path"] != roadmapPath || artifact["content_available"] != true {
		t.Fatalf("roadmap artifact = %+v; want available roadmap with absolute path", artifact)
	}
	if artifact["phase"] != artifactIDRoadmap {
		t.Fatalf("roadmap artifact phase = %v; want roadmap", artifact["phase"])
	}

	content := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/artifacts/roadmap?offset=2&limit=7")
	if content[blockTypeText] != "Roadmap" {
		t.Fatalf("roadmap content = %q; want Roadmap", content[blockTypeText])
	}
}

func TestRewindDescriptionReviewArtifactAndStateExposed(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	pending := feature.PhasePlan
	f.Status = feature.StatusDesignNeedsReview
	f.CurrentPhase = feature.PhaseDesign
	f.Pipeline = feature.PipelineMedium
	f.PendingReviewPhase = &pending
	f.IsRewind = true
	f.Artifacts = nil
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	writeFile(t, filepath.Join(store.BaseDir, f.ID, "description-review.md"), "initial prompt")

	handler := NewHandler(baseReadHandlerOptions(store))

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	activeRun := detail[entityFeature].(map[string]any)["active_run_detail"].(map[string]any)
	if activeRun["pending_review_phase"] != targetPhasePlan {
		t.Fatalf("pending_review_phase = %v; want plan", activeRun["pending_review_phase"])
	}
	if activeRun["is_rewind"] != true {
		t.Fatalf("is_rewind = %v; want true", activeRun["is_rewind"])
	}

	list := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/artifacts")
	rawArtifacts := list["artifacts"].([]any)
	if len(rawArtifacts) != 1 {
		t.Fatalf("artifacts len = %d; want 1 (%+v)", len(rawArtifacts), rawArtifacts)
	}
	artifact := rawArtifacts[0].(map[string]any)
	if artifact["id"] != "description-review" || artifact["content_available"] != true {
		t.Fatalf("description review artifact = %+v; want available description-review", artifact)
	}
	content := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/artifacts/description-review?offset=8&limit=6")
	if content[blockTypeText] != "prompt" {
		t.Fatalf("description-review content = %q; want prompt", content[blockTypeText])
	}
}

func TestSSEEmitsMetadataOnlyEventsAndHeartbeat(t *testing.T) {
	t.Parallel()
	eventCh := make(chan interface{}, 4)
	handler := NewHandler(HandlerOptions{Events: eventCh, DisableHostValidation: true})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v1/events?heartbeat_ms=10", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET SSE error = %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("SSE Content-Type = %q; want text/event-stream", got)
	}
	reader := bufio.NewReader(resp.Body)
	connected := readSSEBlock(t, reader, "connected")
	if !strings.Contains(connected, `"snapshot_required":true`) {
		t.Fatalf("connected event = %s; want snapshot_required", connected)
	}
	eventCh <- ports.Event{Type: ports.PhaseCompleted, FeatureID: fixtureFeatureIDAlt, Phase: feature.PhaseImplement, Error: errors.New("private-token /tmp/path")}
	updated := readSSEBlock(t, reader, sseEventLifecycleUpdated)
	if !strings.Contains(updated, `"feature_id":"feat-001"`) {
		t.Fatalf("lifecycle event = %s; want feature id", updated)
	}
	if strings.Contains(updated, secretTokenLiteral) || strings.Contains(updated, "/tmp/path") {
		t.Fatalf("SSE event leaks unsafe detail: %s", updated)
	}
	heartbeat := readSSEBlock(t, reader, sseEventHeartbeat)
	if !strings.Contains(heartbeat, "event: heartbeat") {
		t.Fatalf("heartbeat event = %s; want heartbeat", heartbeat)
	}
}

func TestSSEEmitsShutdownFromDomainEvents(t *testing.T) {
	t.Parallel()
	domainCh := make(chan ports.Event, 1)
	handler := NewHandler(HandlerOptions{DomainEvents: domainCh, DisableHostValidation: true})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v1/events?heartbeat_ms=10", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET SSE error = %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	reader := bufio.NewReader(resp.Body)
	readSSEBlock(t, reader, "connected")

	domainCh <- ports.Event{
		Type:    ports.RuntimeShutdownStarted,
		Message: "private-token /tmp/agentico-runtime/shutdown.log",
	}
	updated := readSSEBlock(t, reader, sseEventShutdownUpdated)
	if !strings.Contains(updated, `"resource":{"type":"runtime"`) {
		t.Fatalf("shutdown event = %s; want runtime resource", updated)
	}
	if !strings.Contains(updated, `"snapshot_required":true`) {
		t.Fatalf("shutdown event = %s; want snapshot_required", updated)
	}
	if strings.Contains(updated, secretTokenLiteral) || strings.Contains(updated, "/tmp/agentico-runtime") {
		t.Fatalf("shutdown event leaks unsafe detail: %s", updated)
	}
}

func TestRuntimeServerCloseEmitsShutdownNotification(t *testing.T) {
	srv, err := Start(context.Background(), Options{AuthToken: testAuthToken})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.BaseURL()+"/api/v1/events?heartbeat_ms=10", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET SSE error = %v", err)
	}
	reader := bufio.NewReader(resp.Body)
	readSSEBlock(t, reader, "connected")

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done <- srv.Close(ctx)
	}()
	updated := readSSEBlock(t, reader, sseEventShutdownUpdated)
	if !strings.Contains(updated, `"resource":{"type":"runtime"`) {
		t.Fatalf("shutdown event = %s; want runtime resource", updated)
	}
	_ = resp.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for RuntimeServer.Close")
	}
}

// baseReadHandlerOptions returns the HandlerOptions shared by most read-API
// contract tests: a fixed runtime identity over store, and host validation
// disabled. Callers set any additional fields they need on the result.
func baseReadHandlerOptions(store *feature.Store) HandlerOptions {
	return HandlerOptions{
		Runtime:               RuntimeIdentity{RuntimeDir: runtimeDirLiteral, StateDir: store.BaseDir, Config: testRuntimeConfigPath},
		Features:              store,
		DisableHostValidation: true,
	}
}

func seedReadFeature(t *testing.T) (*feature.Store, *feature.Feature) {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	f := &feature.Feature{
		ID: fixtureFeatureIDAlt, Name: "Read API", Slug: "read-api", Description: "private-token description",
		Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement, Created: now,
		Repos:       []feature.FeatureRepo{{Name: repoNameSelf, Path: testRepoPath, WorktreePath: worktreePathLiteral, Branch: secretBranchLiteral}},
		Models:      config.ModelConfig{Research: modelOpus1M, Planning: modelOpus1M, Implementation: modelOpus1M, Review: modelGPT54, Utilities: modelSonnet, KBBuild: modelOpus1M},
		Inquireness: "high", ExitCriteria: "private-token exit criteria", ActiveRun: 1, RunCount: 1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	f.CurrentIteration = 2
	f.CurrentRoadmapPhase = 1
	f.TotalRoadmapPhases = 3
	f.CurrentPhaseStatus = "implementing"
	f.Artifacts = map[string]string{targetPhasePlan: "plan/phase-plan.md"}
	f.RepoStates = map[string]*feature.RepoState{repoNameSelf: {Touched: true, PRURL: "https://github.example/pr/1"}}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return store, f
}

func cloneFeatureForReadTest(t *testing.T, store *feature.Store, base *feature.Feature, id, name string) *feature.Feature {
	t.Helper()
	f := &feature.Feature{
		ID: id, Name: name, Slug: id, Description: base.Description,
		Status: feature.StatusImplementing, CurrentPhase: base.CurrentPhase, Created: base.Created.Add(-time.Minute),
		Repos:         append([]feature.FeatureRepo(nil), base.Repos...),
		Models:        base.Models,
		Inquireness:   base.Inquireness,
		ExitCriteria:  base.ExitCriteria,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	f.CurrentIteration = base.CurrentIteration
	f.CurrentRoadmapPhase = base.CurrentRoadmapPhase
	f.TotalRoadmapPhases = base.TotalRoadmapPhases
	f.CurrentPhaseStatus = base.CurrentPhaseStatus
	f.Artifacts = map[string]string{targetPhasePlan: "plan/phase-plan.md"}
	f.RepoStates = map[string]*feature.RepoState{repoNameSelf: {Touched: true}}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(%s) error = %v", id, err)
	}
	return f
}

func pendingReadControl(requestID, toolName, input string) *llm.ControlRequestMessage {
	return &llm.ControlRequestMessage{
		Type:         transcriptTypeControlRequest,
		RequestID:    requestID,
		WaitingSince: time.Date(2026, 6, 13, 12, 3, 0, 0, time.UTC),
		Request: llm.ControlRequest{
			Subtype:  controlSubtypeCanUseTool,
			ToolName: toolName,
			Input:    json.RawMessage(input),
		},
	}
}

func getJSONMap(t *testing.T, handler http.Handler, path string) map[string]any {
	t.Helper()
	return requestJSONMap(t, handler, path, http.StatusOK)
}

func requestJSONMap(t *testing.T, handler http.Handler, path string, wantStatus int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s status = %d; want %d; body: %s", http.MethodGet, path, resp.StatusCode, wantStatus, data)
	}
	var out map[string]any
	if resp.StatusCode == http.StatusNotModified {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return out
}

func readSSEBlock(t *testing.T, r *bufio.Reader, event string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var lines []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE line: %v", err)
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			lines = append(lines, line)
		}
		block := strings.Join(lines, "\n")
		if strings.Contains(block, "event: "+event) {
			return block
		}
	}
	t.Fatalf("timed out waiting for SSE event %q", event)
	return ""
}

func mustMarshalJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return string(data)
}

func assertActionScope(t *testing.T, action map[string]any, wantRepoSelection string) {
	t.Helper()
	scope := action["scope"].(map[string]any)
	if scope["type"] != entityFeature {
		t.Fatalf("action %s scope type = %v; want %s", action["id"], scope["type"], entityFeature)
	}
	if got := stringValue(scope["repo_selection"]); got != wantRepoSelection {
		t.Fatalf("action %s repo_selection = %q; want %q", action["id"], got, wantRepoSelection)
	}
}

func assertActionInputNames(t *testing.T, action map[string]any, want ...string) {
	t.Helper()
	rawInputs := action["required_inputs"].([]any)
	got := make([]string, 0, len(rawInputs))
	for _, raw := range rawInputs {
		got = append(got, raw.(map[string]any)["name"].(string))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("action %s input names = %v; want %v", action["id"], got, want)
	}
}

func actionInputByName(t *testing.T, action map[string]any, name string) map[string]any {
	t.Helper()
	for _, raw := range action["required_inputs"].([]any) {
		input := raw.(map[string]any)
		if input["name"] == name {
			return input
		}
	}
	t.Fatalf("action %s missing input %q", action["id"], name)
	return nil
}

func actionInputDTOByName(t *testing.T, action ActionDTO, name string) ActionInputDTO {
	t.Helper()
	for _, input := range action.RequiredInputs {
		if input.Name == name {
			return input
		}
	}
	t.Fatalf("action %s missing input %q", action.ID, name)
	return ActionInputDTO{}
}

func assertActionInputRequired(t *testing.T, action map[string]any, name string, want bool) {
	t.Helper()
	input := actionInputByName(t, action, name)
	if got := input["required"]; got != want {
		t.Fatalf("action %s input %s required = %v; want %v", action["id"], name, got, want)
	}
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func actionCatalogTestFeature(status feature.Status, checkpoints feature.Checkpoints, publishable *bool, cycles map[string]*feature.RepoCycleState) *feature.Feature {
	return &feature.Feature{
		ID:          "feat-actions",
		Status:      status,
		Checkpoints: checkpoints,
		Repos: []feature.FeatureRepo{{
			Name:        repoNameSelf,
			Publishable: publishable,
		}},
		RepoCycles: cycles,
	}
}

func actionDTOByID(t *testing.T, actions []ActionDTO, id string) ActionDTO {
	t.Helper()
	for _, action := range actions {
		if action.ID == id {
			return action
		}
	}
	t.Fatalf("action catalog missing %q", id)
	return ActionDTO{}
}

func stringFieldFromJSON(t *testing.T, raw any, key string) []string {
	t.Helper()
	items := raw.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.(map[string]any)[key].(string))
	}
	return out
}

func intsToCSV(vals []int) string {
	parts := make([]string, 0, len(vals))
	for _, val := range vals {
		parts = append(parts, strconv.Itoa(val))
	}
	return strings.Join(parts, ",")
}

func stringSliceContains(vals []string, want string) bool {
	for _, val := range vals {
		if val == want {
			return true
		}
	}
	return false
}

func twoDigit(v int) string {
	if v < 10 {
		return "0" + strconv.Itoa(v)
	}
	return strconv.Itoa(v)
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

type fakeProvider struct {
	name     string
	models   []string
	catalog  []llm.ModelInfo
	toolLess bool
}

func (p fakeProvider) Name() string { return p.name }

func (p fakeProvider) MatchesModel(model string) bool {
	model = strings.TrimPrefix(model, p.name+":")
	for _, candidate := range p.AvailableModels() {
		if candidate == model {
			return true
		}
	}
	return false
}

func (p fakeProvider) DetectCLI() bool { return true }

func (p fakeProvider) AvailableModels() []string {
	if len(p.catalog) == 0 {
		return append([]string(nil), p.models...)
	}
	models := make([]string, 0, len(p.catalog))
	for _, info := range p.catalog {
		models = append(models, info.ID)
	}
	return models
}

func (p fakeProvider) BuildCommand(llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}

func (p fakeProvider) NewProtocol(llm.ProtocolOpts) llm.Protocol { return nil }

func (p fakeProvider) InstallHint() string { return "" }

func (p fakeProvider) VersionInfo() (string, error) { return "test", nil }

func (p fakeProvider) MinVersion() [3]int { return [3]int{} }

func (p fakeProvider) EnvVarsToExclude() []string { return nil }

func (p fakeProvider) SupportsNativeToollessReview() bool { return p.toolLess }

func (p fakeProvider) ModelCatalog() []llm.ModelInfo {
	if len(p.catalog) > 0 {
		return append([]llm.ModelInfo(nil), p.catalog...)
	}
	out := make([]llm.ModelInfo, 0, len(p.models))
	for _, model := range p.models {
		out = append(out, llm.ModelInfo{ID: model, DisplayName: model, ContextWindow: 200000, Category: "capable"})
	}
	return out
}

type countingFeatureReader struct {
	feature        *feature.Feature
	loadRunNumbers []int
}

func (r *countingFeatureReader) List() ([]*feature.Feature, error) {
	if r.feature == nil {
		return nil, nil
	}
	return []*feature.Feature{r.feature}, nil
}

func (r *countingFeatureReader) Load(id string) (*feature.Feature, error) {
	if r.feature == nil || r.feature.ID != id {
		return nil, os.ErrNotExist
	}
	return r.feature, nil
}

func (r *countingFeatureReader) LoadRun(_ string, runNumber int) (*feature.Run, error) {
	r.loadRunNumbers = append(r.loadRunNumbers, runNumber)
	return &feature.Run{RunNumber: runNumber}, nil
}

func (r *countingFeatureReader) RunDir(string, int) string { return "" }

type fakeSessionManager struct {
	views                []ports.SessionView
	getSessionMiss       bool
	featureSessionsCalls *int
	recentSessionLimits  *[]int
}

func (m fakeSessionManager) StartSession(string, string, feature.Phase, []string, string, []string, ...*ports.SessionOpts) (ports.SessionHandle, error) {
	return nil, nil
}

func (m fakeSessionManager) StopSession(string) error { return nil }

func (m fakeSessionManager) GetSession(id string) ports.SessionView {
	if m.getSessionMiss {
		return nil
	}
	for _, view := range m.views {
		if view.ID() == id {
			return view
		}
	}
	return nil
}

func (m fakeSessionManager) ActiveSessions() []ports.SessionView {
	var out []ports.SessionView
	for _, view := range m.views {
		if view.IsActive() {
			out = append(out, view)
		}
	}
	return out
}

func (m fakeSessionManager) RecentSessions(limit int) []ports.SessionView {
	if m.recentSessionLimits != nil {
		*m.recentSessionLimits = append(*m.recentSessionLimits, limit)
	}
	out := append([]ports.SessionView(nil), m.views...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartedAt().Equal(out[j].StartedAt()) {
			return out[i].ID() < out[j].ID()
		}
		return out[i].StartedAt().After(out[j].StartedAt())
	})
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (m fakeSessionManager) FeatureSessions(featureID string) []ports.SessionView {
	if m.featureSessionsCalls != nil {
		(*m.featureSessionsCalls)++
	}
	var out []ports.SessionView
	for _, view := range m.views {
		if view.FeatureID() == featureID {
			out = append(out, view)
		}
	}
	return out
}

func (m fakeSessionManager) SendInput(string, []byte) error { return nil }

func (m fakeSessionManager) Attach(id string) (ports.SessionView, error) {
	return m.GetSession(id), nil
}

func (m fakeSessionManager) Detach() {}

func (m fakeSessionManager) Shutdown() {}

func (m fakeSessionManager) IsShuttingDown() bool { return false }

type fakeSessionView struct {
	id             string
	featureID      string
	runNumber      int
	phase          feature.Phase
	repoName       string
	kind           ports.SessionKind
	label          string
	status         ports.SessionStatus
	startedAt      time.Time
	iteration      int
	initialPrompt  string
	provider       string
	model          string
	workDir        string
	logPath        string
	messages       []llm.SDKMessage
	log            ports.MessageLog
	pending        []*llm.ControlRequestMessage
	permCacheScope string
	contextPct     *int
}

func (s *fakeSessionView) ID() string                  { return s.id }
func (s *fakeSessionView) FeatureID() string           { return s.featureID }
func (s *fakeSessionView) RunNumber() int              { return s.runNumber }
func (s *fakeSessionView) Phase() feature.Phase        { return s.phase }
func (s *fakeSessionView) RepoName() string            { return s.repoName }
func (s *fakeSessionView) PermCacheScope() string      { return s.permCacheScope }
func (s *fakeSessionView) Kind() ports.SessionKind     { return s.kind }
func (s *fakeSessionView) Label() string               { return s.label }
func (s *fakeSessionView) Status() ports.SessionStatus { return s.status }
func (s *fakeSessionView) IsActive() bool {
	return s.status == ports.SessionRunning || s.status == ports.SessionWaitingHelp || s.status == ports.SessionWaitingPermission
}
func (s *fakeSessionView) Iteration() int { return s.iteration }
func (s *fakeSessionView) StartedAt() time.Time {
	if s.startedAt.IsZero() {
		return time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	}
	return s.startedAt
}
func (s *fakeSessionView) InitialPrompt() string            { return s.initialPrompt }
func (s *fakeSessionView) ProviderName() string             { return s.provider }
func (s *fakeSessionView) Model() string                    { return s.model }
func (s *fakeSessionView) WorkDir() string                  { return s.workDir }
func (s *fakeSessionView) EffectiveEffort() llm.EffortLevel { return "" }
func (s *fakeSessionView) EffortSource() llm.EffortSource   { return "" }
func (s *fakeSessionView) MessageLog() ports.MessageLog {
	if s.log != nil {
		return s.log
	}
	return fakeMessageLog{messages: s.messages}
}
func (s *fakeSessionView) Cost() *llm.ResultMessage { return nil }
func (s *fakeSessionView) LatestUsage() *llm.Usage  { return nil }
func (s *fakeSessionView) AccumulatedUsage() llm.Usage {
	return llm.Usage{InputTokens: 10, OutputTokens: 5}
}
func (s *fakeSessionView) LastControlRequest() *llm.ControlRequestMessage {
	if len(s.pending) == 0 {
		return nil
	}
	return s.pending[len(s.pending)-1]
}
func (s *fakeSessionView) PendingControlRequests() []*llm.ControlRequestMessage {
	return append([]*llm.ControlRequestMessage(nil), s.pending...)
}
func (s *fakeSessionView) QALog() []ports.QAPair { return nil }
func (s *fakeSessionView) LogFilePath() string   { return s.logPath }
func (s *fakeSessionView) ContextPercentage() int {
	if s.contextPct != nil {
		return *s.contextPct
	}
	return 42
}
func (s *fakeSessionView) ErrorDetail() string             { return "" }
func (s *fakeSessionView) ExitCodeDetail() string          { return "" }
func (s *fakeSessionView) LastStdoutAt() time.Time         { return s.StartedAt() }
func (s *fakeSessionView) StatusCh() <-chan string         { return nil }
func (s *fakeSessionView) AttachCh() <-chan llm.SDKMessage { return nil }
func (s *fakeSessionView) Done() <-chan struct{}           { return nil }
func (s *fakeSessionView) HasPendingAskUserQuestion() bool {
	for _, req := range s.pending {
		if req.Request.ToolName == toolNameAskUserQuestion {
			return true
		}
	}
	return false
}
func (s *fakeSessionView) SendUserMessage(string) error                { return nil }
func (s *fakeSessionView) RespondToControl(string, bool, string) error { return nil }
func (s *fakeSessionView) RespondToAskUser(string, json.RawMessage, map[string]string, map[string]llm.AskUserAnnotation) error {
	return nil
}
func (s *fakeSessionView) ClearPendingQuestion(string) {}
func (s *fakeSessionView) ResetWaitingStatus()         {}
func (s *fakeSessionView) Stop() error                 { return nil }
func (s *fakeSessionView) Interrupt() error            { return nil }
func (s *fakeSessionView) Wait()                       {}

type fakeMessageLog struct {
	messages []llm.SDKMessage
}

func (l fakeMessageLog) Append(llm.SDKMessage)                     {}
func (l fakeMessageLog) UpdateLast(llm.SDKMessage)                 {}
func (l fakeMessageLog) UpdateLastAssistantPartial(llm.SDKMessage) {}
func (l fakeMessageLog) Messages() []llm.SDKMessage {
	return append([]llm.SDKMessage(nil), l.messages...)
}
func (l fakeMessageLog) Len() int     { return len(l.messages) }
func (l fakeMessageLog) Text() string { return "" }
func (l fakeMessageLog) LastN(n int) []llm.SDKMessage {
	if n >= len(l.messages) {
		return l.Messages()
	}
	return append([]llm.SDKMessage(nil), l.messages[len(l.messages)-n:]...)
}
func (l fakeMessageLog) LastResultMessage() *llm.ResultMessage { return nil }
func (l fakeMessageLog) LastErrorDetail() string               { return "" }
func (l fakeMessageLog) AssistantText() string                 { return "" }
func (l fakeMessageLog) ToolUseBlocks() []llm.ContentBlock {
	var blocks []llm.ContentBlock
	for _, msg := range l.messages {
		if msg.Assistant == nil {
			continue
		}
		for _, block := range msg.Assistant.Message.Content {
			if block.IsToolUse() {
				blocks = append(blocks, block)
			}
		}
	}
	return blocks
}
