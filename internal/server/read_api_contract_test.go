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

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestReadAPISnapshotsRevisionAndStructuredErrors(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features: store,
	})

	dashboard := getJSONMap(t, handler, "/api/v1/features")
	meta := dashboard["meta"].(map[string]any)
	revision := meta["revision"].(string)
	if revision == "" {
		t.Fatal("dashboard revision is empty")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/features", nil)
	req.Header.Set("If-None-Match", `"`+revision+`"`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusNotModified {
		t.Fatalf("revalidated dashboard status = %d; want 304", w.Result().StatusCode)
	}
	rawDashboard := mustMarshalJSON(t, dashboard)
	for _, forbidden := range []string{"/repo/path", "/worktree/path", "feature/secret", "private-token", "permissions_queue"} {
		if strings.Contains(rawDashboard, forbidden) {
			t.Fatalf("dashboard leaks %q in %s", forbidden, rawDashboard)
		}
	}

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	if detail["api_version"] != APIVersion {
		t.Fatalf("detail api_version = %v; want %s", detail["api_version"], APIVersion)
	}
	if detail["feature"].(map[string]any)["id"] != f.ID {
		t.Fatalf("detail feature id = %v; want %s", detail["feature"], f.ID)
	}
	models := detail["feature"].(map[string]any)["models"].(map[string]any)
	if models["implementation"] != f.Models.Implementation {
		t.Fatalf("detail feature models.implementation = %v; want %s", models["implementation"], f.Models.Implementation)
	}
	rawDetail := mustMarshalJSON(t, detail)
	for _, forbidden := range []string{"/repo/path", "/worktree/path", "feature/secret"} {
		if strings.Contains(rawDetail, forbidden) {
			t.Fatalf("detail leaks storage/git field %q in %s", forbidden, rawDetail)
		}
	}

	errResp := requestJSONMap(t, handler, http.MethodGet, "/api/v1/features/../bad", nil, http.StatusBadRequest)
	errDTO := errResp["error"].(map[string]any)
	if errDTO["code"] != "bad_request" || errDTO["status"].(float64) != http.StatusBadRequest {
		t.Fatalf("error DTO = %+v; want stable bad_request", errDTO)
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
		LastError:     "git worktree add failed",
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         []feature.FeatureRepo{{Name: "repo-a", Branch: "feature/setup-failed"}},
	}
	setup := feature.NewActiveSetupState(f.Repos, nil, nil, now)
	setup.Status = feature.SetupStatusFailed
	setup.CompletedAt = &now
	setup.LatestLogPath = "/tmp/agentico/setup.log"
	setup.LastError = "git worktree add failed"
	task := setup.Tasks["worktree:repo-a"]
	task.Status = feature.SetupStatusFailed
	task.Path = "/tmp/worktrees/setup-failed/repo-a"
	task.LastError = "git worktree add failed"
	setup.Tasks[task.Key] = task
	f.SetRun(&feature.Run{RunNumber: 1, Setup: setup, FailureType: feature.FailureWorktreeSetup})
	if err := store.Save(f); err != nil {
		t.Fatalf("Save feature: %v", err)
	}
	handler := NewHandler(HandlerOptions{Features: store})

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	featureBody := detail["feature"].(map[string]any)
	active := featureBody["active_run_detail"].(map[string]any)
	setupBody, ok := active["setup"].(map[string]any)
	if !ok {
		t.Fatalf("active_run_detail = %+v, want setup object for durable setup failure", active)
	}
	if setupBody["status"] != "failed" || setupBody["last_error"] != "git worktree add failed" || setupBody["latest_log_path"] == "" {
		t.Fatalf("setup = %+v, want failed setup diagnostic", setupBody)
	}
	tasks := setupBody["tasks"].(map[string]any)
	worktreeTask := tasks["worktree:repo-a"].(map[string]any)
	if worktreeTask["status"] != "failed" || worktreeTask["last_error"] != "git worktree add failed" || worktreeTask["path"] == "" {
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
			"agentic-orchestrator": {Type: feature.CycleRefactor, Status: feature.RepoCycleRunning},
		}
		return nil
	}); err != nil {
		t.Fatalf("Modify() error = %v", err)
	}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: filepath.Dir(store.BaseDir), StateDir: store.BaseDir, Config: filepath.Join(filepath.Dir(store.BaseDir), "config.yaml")},
		Features: store,
	})

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	featureDTO := detail["feature"].(map[string]any)
	cycle, ok := featureDTO["cycle"].(map[string]any)
	if !ok {
		t.Fatalf("detail feature cycle missing in %+v", featureDTO)
	}
	if cycle["type"] != "refactor" || cycle["status"] != "running" || cycle["count"].(float64) != 1 {
		t.Fatalf("detail feature cycle = %+v, want running refactor #1", cycle)
	}
}

func TestFeatureDetailProjectsActiveFeatureRebaseOperation(t *testing.T) {
	store, f := seedReadFeature(t)
	f.ID = "feat-rebase"
	f.Status = feature.StatusCodeReady
	f.Repos = []feature.FeatureRepo{{Name: "api"}, {Name: "web"}}
	f.ActiveCycle = &feature.CycleState{Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 2}
	f.SetActiveCycleType(feature.CycleRebase)
	f.RebaseOperation = &feature.RebaseOperationState{
		Stage: feature.RebaseStageHarness,
		Repos: map[string]*feature.RebaseRepoProgress{
			"api": {Status: feature.RebaseRepoStatusRebasing, RebaseTarget: "main"},
			"web": {Status: feature.RebaseRepoStatusUpToDate, RebaseTarget: "main"},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	handler := NewHandler(HandlerOptions{
		Runtime:      RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features:     store,
		FeatureStore: store,
		Freshness: StaticFreshnessProvider(map[string]RepoFreshness{
			"api": RepoFreshnessLocalChanges,
			"web": RepoFreshnessInSync,
		}),
	})

	body := getJSONMap(t, handler, "/api/v1/features/feat-rebase")
	featureBody := body["feature"].(map[string]any)
	cycle := featureBody["cycle"].(map[string]any)
	if cycle["type"] != "rebase" || cycle["status"] != "running" {
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
	if status["api"].RebaseStatus != "rebasing" || status["api"].Freshness != "local changes" {
		t.Fatalf("api status = %+v", status["api"])
	}
	if status["web"].RebaseStatus != "up_to_date" || status["web"].Freshness != "in sync" {
		t.Fatalf("web status = %+v", status["web"])
	}
}

func TestConfigCatalogPromptPermissionSnapshots(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	f.PendingNeedUserInputPath = filepath.Join(store.RunDir(f.ID, 1), "phase-02", "implement", "need-user-input.yaml")
	f.Pipeline = feature.PipelineMedium
	f.Checkpoints = feature.Checkpoints{InquiryReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true, DraftPublish: true}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	registry := llm.NewRegistry()
	registry.Register(fakeProvider{name: "codex", models: []string{"gpt-5.4", "gpt-5.4-mini"}})
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id: "sess-ask", featureID: f.ID, phase: feature.PhaseImplement, status: ports.SessionWaitingHelp,
			initialPrompt: "raw initial prompt with private-token",
			pending: []*llm.ControlRequestMessage{{
				Type:      "control_request",
				RequestID: "ask-1",
				Request: llm.ControlRequest{
					Subtype:  "can_use_tool",
					ToolName: "AskUserQuestion",
					Input:    json.RawMessage(`{"questions":[{"question":"Choose?","options":[{"label":"A"}]}],"secret":"private-token"}`),
				},
			}},
		},
		&fakeSessionView{
			id: "sess-perm", featureID: f.ID, phase: feature.PhasePlan, status: ports.SessionWaitingPermission,
			pending: []*llm.ControlRequestMessage{{
				Type:      "control_request",
				RequestID: "perm-1",
				Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "Bash", Input: json.RawMessage(`{"command":"echo private-token"}`)},
			}},
		},
	}}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Config: &config.Config{
			Defaults: config.DefaultsConfig{
				Models:       config.ModelConfig{Research: "opus[1m]", Planning: "opus[1m]", Implementation: "opus[1m]", Review: "gpt-5.4", Utilities: "sonnet", KBBuild: "opus[1m]"},
				Inquireness:  "high",
				Pipeline:     "large",
				ExitCriteria: "private-token should not leak",
			},
			Repos:          map[string]config.RepoConfig{"agentic-orchestrator": {Path: "/repo/path"}},
			WorkspaceRoots: []string{"/workspace"},
		},
		Registry: registry,
		Sessions: sessions,
	})

	for _, path := range []string{
		"/api/v1/config/runtime",
		"/api/v1/features/" + f.ID + "/config",
		"/api/v1/catalog/models",
		"/api/v1/prompts",
		"/api/v1/permissions",
	} {
		body := getJSONMap(t, handler, path)
		raw := mustMarshalJSON(t, body)
		if strings.Contains(raw, "private-token") || strings.Contains(raw, "raw initial prompt") {
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
	prompts := getJSONMap(t, handler, "/api/v1/prompts")
	asks := prompts["ask_user_questions"].([]any)
	if len(asks) != 1 {
		t.Fatalf("ask_user_questions length = %d; want 1", len(asks))
	}
	ask := asks[0].(map[string]any)
	questions := ask["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("ask_user questions length = %d; want 1", len(questions))
	}
	question := questions[0].(map[string]any)
	if question["question"] != "Choose?" {
		t.Fatalf("ask_user question = %v; want Choose?", question["question"])
	}
	options := question["options"].([]any)
	if len(options) != 1 || options[0].(map[string]any)["label"] != "A" {
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
	detailGate := detail["feature"].(map[string]any)["need_user_input"].(map[string]any)
	if detailGate["feature_id"] != f.ID {
		t.Fatalf("detail need user input feature_id = %v; want %s", detailGate["feature_id"], f.ID)
	}
}

func TestPromptSnapshotPreservesReadableAskUserQuestionText(t *testing.T) {
	t.Parallel()

	store, f := seedReadFeature(t)
	longQuestion := "Should TUI/UI label names that match what is displayed on screen, including In Progress, Published, Watch, Answer, Approve, and Publish as PR, be translated into the target language or kept in English so the reader can map the README back to the live interface without losing important workflow context?"
	longLabel := "Translate visible TUI labels too, including every status badge, button label, and action description that directly corresponds to on-screen text"
	longDescription := "Translate all prose including TUI labels. The README is a localized document, and describing what the screen says in English breaks immersion even though the reader can still match the workflow by position, status, and surrounding context."
	input, err := json.Marshal(map[string]any{
		"questions": []map[string]any{{
			"question": longQuestion,
			"options": []map[string]any{{
				"label":       longLabel,
				"description": longDescription,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("Marshal AskUser input: %v", err)
	}
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id: "sess-ask", featureID: f.ID, phase: feature.PhaseDesign, status: ports.SessionWaitingHelp,
			pending: []*llm.ControlRequestMessage{{
				Type:      "control_request",
				RequestID: "ask-long",
				Request: llm.ControlRequest{
					Subtype:  "can_use_tool",
					ToolName: "AskUserQuestion",
					Input:    input,
				},
			}},
		},
	}}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

	prompts := getJSONMap(t, handler, "/api/v1/prompts")
	asks := prompts["ask_user_questions"].([]any)
	if len(asks) != 1 {
		t.Fatalf("ask_user_questions length = %d; want 1", len(asks))
	}
	ask := asks[0].(map[string]any)
	if _, ok := ask["input"]; ok {
		t.Fatalf("AskUser prompt snapshot exposed raw input: %+v", ask["input"])
	}
	questions := ask["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("ask_user questions length = %d; want 1", len(questions))
	}
	question := questions[0].(map[string]any)
	if got := question["question"]; got != longQuestion {
		t.Fatalf("ask_user question = %q; want full question %q", got, longQuestion)
	}
	options := question["options"].([]any)
	if len(options) != 1 {
		t.Fatalf("ask_user options length = %d; want 1", len(options))
	}
	option := options[0].(map[string]any)
	if got := option["label"]; got != longLabel {
		t.Fatalf("ask_user option label = %q; want full label %q", got, longLabel)
	}
	if got := option["description"]; got != longDescription {
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
		"questions": []map[string]any{{
			"question":    question,
			"header":      "Orthography",
			"multiSelect": false,
			"options": []map[string]any{
				{"label": "Historical-Literary (Recommended)", "description": optionDescriptions[0]},
				{"label": "De Blasi & Montuori 2020", "description": optionDescriptions[1]},
				{"label": "Hybrid", "description": optionDescriptions[2]},
			},
		}},
	}))
	sourceInput := json.RawMessage(mustMarshalJSON(t, map[string]any{
		"questions": []map[string]any{{
			"question":    question,
			"header":      "Orthography",
			"multiSelect": false,
			"options": []map[string]any{
				{"label": "Historical-Literary (Recommended)", "description": optionDescriptions[0], "confidence": 0.72},
				{"label": "De Blasi & Montuori 2020", "description": optionDescriptions[1], "confidence": 0.21},
				{"label": "Hybrid", "description": optionDescriptions[2], "confidence": 0.07},
			},
		}},
	}))
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id: "sess-ask", featureID: f.ID, phase: feature.PhaseDesign, status: ports.SessionWaitingHelp,
			messages: []llm.SDKMessage{{
				Type: "assistant",
				Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
					Role: "assistant",
					Content: []llm.ContentBlock{{
						Type:  "tool_use",
						ID:    "toolu-ask-1",
						Name:  "AskUserQuestion",
						Input: sourceInput,
					}},
				}},
			}},
			pending: []*llm.ControlRequestMessage{{
				Type:      "control_request",
				RequestID: "ask-confidence",
				Request: llm.ControlRequest{
					Subtype:  "can_use_tool",
					ToolName: "AskUserQuestion",
					Input:    strippedInput,
				},
			}},
		},
	}}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

	prompts := getJSONMap(t, handler, "/api/v1/prompts")
	asks := prompts["ask_user_questions"].([]any)
	if len(asks) != 1 {
		t.Fatalf("ask_user_questions length = %d; want 1", len(asks))
	}
	ask := asks[0].(map[string]any)
	if _, ok := ask["input"]; ok {
		t.Fatalf("AskUser prompt snapshot exposed raw input: %+v", ask["input"])
	}
	questions := ask["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("ask_user questions length = %d; want 1", len(questions))
	}
	options := questions[0].(map[string]any)["options"].([]any)
	want := []float64{0.72, 0.21, 0.07}
	if len(options) != len(want) {
		t.Fatalf("ask_user options length = %d; want %d", len(options), len(want))
	}
	for i, wantConfidence := range want {
		option := options[i].(map[string]any)
		if got := option["confidence"]; got != wantConfidence {
			t.Fatalf("option[%d] confidence = %v; want %.2f in %+v", i, got, wantConfidence, options)
		}
	}
}

func TestRuntimeConfigDiscoversWorkspaceRootReposOnRead(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, name := range []string{"api", "web", "worker"} {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatalf("mkdir git repo %s: %v", name, err)
		}
	}
	cfg := &config.Config{
		Repos:          map[string]config.RepoConfig{"explicit": {Path: "/explicit"}},
		WorkspaceRoots: []string{root},
	}
	handler := NewHandler(HandlerOptions{Config: cfg})

	body := getJSONMap(t, handler, "/api/v1/config/runtime")
	repos := body["repos"].([]any)
	names := make(map[string]bool, len(repos))
	for _, item := range repos {
		repo := item.(map[string]any)
		names[repo["name"].(string)] = true
	}
	for _, want := range []string{"api", "web", "worker", "explicit"} {
		if !names[want] {
			t.Fatalf("runtime config repos = %+v, want discovered repo %q", names, want)
		}
	}
}

func TestFeatureDetailActionCatalogStableAndRedacted(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPublished
		ff.CurrentPhase = feature.PhasePublish
		ff.RepoCycles = map[string]*feature.RepoCycleState{
			"agentic-orchestrator": {
				Type:      feature.CycleReviewComments,
				Status:    feature.RepoCycleFailed,
				LastError: "private-token leaked in raw prompt payload",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("Modify() error = %v", err)
	}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
	})

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	featureDTO := detail["feature"].(map[string]any)
	rawActions, ok := featureDTO["actions"].([]any)
	if !ok {
		t.Fatalf("detail actions missing or wrong type in %+v", featureDTO)
	}
	gotIDs := make([]string, 0, len(rawActions))
	for _, raw := range rawActions {
		action := raw.(map[string]any)
		gotIDs = append(gotIDs, action["id"].(string))
		if action["scope"].(map[string]any)["type"] != "feature" {
			t.Fatalf("action scope = %+v; want feature scope", action["scope"])
		}
		if _, ok := action["required_inputs"].([]any); !ok {
			t.Fatalf("action %s missing required_inputs metadata", action["id"])
		}
	}
	wantIDs := []string{
		"start",
		"pause-stop",
		"resume",
		"restart",
		"publish",
		"merge",
		"rewind",
		"rebase",
		"review-comments",
		"tweak",
		"refactor",
		"retry",
		"mark-done",
		"cleanup",
		"delete",
	}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("action ids = %v; want %v", gotIDs, wantIDs)
	}
	actionsByID := map[string]map[string]any{}
	for _, rawAction := range rawActions {
		action := rawAction.(map[string]any)
		actionsByID[action["id"].(string)] = action
	}
	assertActionScope(t, actionsByID["publish"], "feature", "")
	assertActionInputNames(t, actionsByID["publish"])
	assertActionScope(t, actionsByID["merge"], "feature", "")
	assertActionInputNames(t, actionsByID["merge"])
	assertActionInputNames(t, actionsByID["rewind"], "target_phase", "roadmap_phase", "upgrade_pipeline")
	assertActionInputRequired(t, actionsByID["rewind"], "target_phase", true)
	assertActionInputRequired(t, actionsByID["rewind"], "roadmap_phase", false)
	assertActionInputRequired(t, actionsByID["rewind"], "upgrade_pipeline", false)
	assertActionScope(t, actionsByID["rebase"], "feature", "")
	assertActionInputNames(t, actionsByID["rebase"])
	assertActionScope(t, actionsByID["review-comments"], "feature", "required")
	assertActionInputNames(t, actionsByID["review-comments"], "repo", "mode")
	assertActionInputRequired(t, actionsByID["review-comments"], "repo", true)
	assertActionInputRequired(t, actionsByID["review-comments"], "mode", true)
	assertActionScope(t, actionsByID["refactor"], "feature", "optional")
	assertActionInputNames(t, actionsByID["refactor"], "repo", "prompt", "pipeline")
	assertActionInputRequired(t, actionsByID["refactor"], "repo", false)
	assertActionInputRequired(t, actionsByID["refactor"], "prompt", true)
	assertActionScope(t, actionsByID["cleanup"], "feature", "")
	assertActionInputNames(t, actionsByID["cleanup"], "target")
	assertActionScope(t, actionsByID["delete"], "feature", "")
	refactorPrompt := actionInputByName(t, actionsByID["refactor"], "prompt")
	if got := int(refactorPrompt["max_length"].(float64)); got != MaxActionTextBytes {
		t.Fatalf("refactor prompt max_length = %d; want %d", got, MaxActionTextBytes)
	}
	raw := mustMarshalJSON(t, rawActions)
	for _, forbidden := range []string{"private-token", "raw prompt", "/repo/path", "/worktree/path"} {
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

func TestActionCatalogRebaseIsFeatureScoped(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPublished
		ff.CurrentPhase = feature.PhasePublish
		return nil
	}); err != nil {
		t.Fatalf("Modify() error = %v", err)
	}
	handler := NewHandler(HandlerOptions{
		Runtime:   RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features:  store,
		Mutations: &fakeMutationTarget{},
	})

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	featureDTO := detail["feature"].(map[string]any)
	actions := featureDTO["actions"].([]any)
	actionsByID := map[string]map[string]any{}
	for _, raw := range actions {
		action := raw.(map[string]any)
		actionsByID[action["id"].(string)] = action
	}
	assertActionScope(t, actionsByID["rebase"], "feature", "")
	assertActionInputNames(t, actionsByID["rebase"])

	for _, bodyJSON := range []string{
		`{"repo":"api"}`,
		`{"repo":""}`,
		`{"rebase_target":""}`,
		`{"conflict_files":null}`,
		`{"conflict_files":[]}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/features/"+f.ID+"/actions/rebase", strings.NewReader(bodyJSON))
		req.Header.Set("X-Agentico-Client", "local")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		resp := w.Result()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			data, _ := io.ReadAll(resp.Body)
			t.Fatalf("rebase mutation %s status = %d; want 400; body: %s", bodyJSON, resp.StatusCode, data)
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode rebase error response for %s: %v", bodyJSON, err)
		}
		errDTO := body["error"].(map[string]any)
		if errDTO["code"] != "bad_request" || !strings.Contains(errDTO["message"].(string), "rebase is feature-scoped") {
			t.Fatalf("rebase mutation %s error = %+v, want feature-scoped bad_request", bodyJSON, errDTO)
		}
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
				"publish":  {enabled: true},
				"merge":    {disabledCode: "not_local_only"},
				"refactor": {disabledCode: "status_not_allowed"},
			},
		},
		{
			name: "manual publish code ready",
			f:    actionCatalogTestFeature(feature.StatusCodeReady, feature.Checkpoints{ManualPublish: true}, &publishable, nil),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				"publish":   {disabledCode: "manual_publish_required"},
				"refactor":  {enabled: true},
				"mark-done": {enabled: true},
			},
		},
		{
			name: "local only code ready",
			f:    actionCatalogTestFeature(feature.StatusCodeReady, feature.Checkpoints{}, &localOnly, nil),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				"publish": {disabledCode: "local_only"},
				"merge":   {enabled: true},
			},
		},
		{
			name: "local only created",
			f:    actionCatalogTestFeature(feature.StatusCreated, feature.Checkpoints{}, &localOnly, nil),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				"merge": {disabledCode: "status_not_allowed"},
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
				"rewind": {disabledCode: "no_rewind_targets"},
			},
		},
		{
			name: "published",
			f:    actionCatalogTestFeature(feature.StatusPublished, feature.Checkpoints{}, &publishable, nil),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				"publish":  {disabledCode: "already_published"},
				"merge":    {disabledCode: "not_local_only"},
				"refactor": {enabled: true},
				"cleanup":  {enabled: true},
			},
		},
		{
			name: "published active cycle",
			f: actionCatalogTestFeature(feature.StatusPublished, feature.Checkpoints{}, &publishable, map[string]*feature.RepoCycleState{
				"agentic-orchestrator": {Type: feature.CycleTweak, Status: feature.RepoCycleRunning},
			}),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				"rebase":          {disabledCode: "cycle_active"},
				"review-comments": {disabledCode: "cycle_active"},
				"tweak":           {disabledCode: "cycle_active"},
				"refactor":        {disabledCode: "cycle_active"},
			},
		},
		{
			name: "running cleanup disabled",
			f:    actionCatalogTestFeature(feature.StatusImplementing, feature.Checkpoints{}, &publishable, nil),
			want: map[string]struct {
				enabled      bool
				disabledCode string
			}{
				"cleanup": {disabledCode: "running"},
				"delete":  {disabledCode: "running"},
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
				"rewind": {enabled: true},
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

	rewind := actionDTOByID(t, actionCatalogDTOs(f), "rewind")
	if !rewind.Enabled {
		t.Fatalf("rewind enabled = false; want true")
	}
	targetOptions := actionInputDTOByName(t, rewind, "target_phase").Options
	if got, want := strings.Join(targetOptions, ","), "plan"; got != want {
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

	rewind := actionDTOByID(t, actionCatalogDTOs(f), "rewind")
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
	oldGatePath := filepath.Join(store.RunDir(oldest.ID, 1), "phase-02", "implement", "need-user-input.yaml")
	newGatePath := filepath.Join(store.RunDir(newest.ID, 1), "phase-02", "implement", "need-user-input.yaml")
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
				pendingReadControl("new-perm", "Bash", `{}`),
				pendingReadControl("new-ask", "AskUserQuestion", `{"questions":[{"question":"new ask"}]}`),
			},
		},
		&fakeSessionView{
			id: "old-session", featureID: oldest.ID, phase: feature.PhasePlan, status: ports.SessionWaitingHelp,
			startedAt: time.Date(2026, 6, 13, 12, 1, 0, 0, time.UTC),
			pending: []*llm.ControlRequestMessage{
				pendingReadControl("old-ask", "AskUserQuestion", `{"questions":[{"question":"old ask"}]}`),
				pendingReadControl("old-perm", "Bash", `{}`),
			},
		},
	}}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

	prompts := getJSONMap(t, handler, "/api/v1/prompts")
	if got, want := requestIDsFromJSON(t, prompts["ask_user_questions"]), []string{"old-ask", "new-ask"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("prompt ask_user_questions order = %v; want %v", got, want)
	}
	if got, want := questionsFromJSON(t, prompts["help_queue"]), []string{"oldest help", "newest help"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("prompt help_queue order = %v; want %v", got, want)
	}
	if got, want := featureIDsFromJSON(t, prompts["need_user_inputs"]), []string{oldest.ID, newest.ID}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("prompt need_user_inputs order = %v; want %v", got, want)
	}
	permissions := getJSONMap(t, handler, "/api/v1/permissions")
	if got, want := requestIDsFromJSON(t, permissions["requests"]), []string{"old-perm", "new-perm"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("permissions requests order = %v; want %v", got, want)
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
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

	prompts := getJSONMap(t, handler, "/api/v1/prompts")
	if got, want := questionsFromJSON(t, prompts["help_queue"]), []string{"Agent has a question"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("prompt help_queue = %v; want %v for WaitingHelp session without control request", got, want)
	}
	if got := requestIDsFromJSON(t, prompts["ask_user_questions"]); len(got) != 0 {
		t.Fatalf("prompt ask_user_questions = %v; want empty without control request", got)
	}
}

func TestPermissionSnapshotIncludesToolInputAndActionableSummary(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id: "sess-1", featureID: f.ID, phase: feature.PhaseImplement, status: ports.SessionWaitingPermission,
			pending: []*llm.ControlRequestMessage{
				pendingReadControl("perm-1", "Bash", `{"command":"go test ./internal/tui"}`),
			},
		},
	}}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

	permissions := getJSONMap(t, handler, "/api/v1/permissions")
	requests := permissions["requests"].([]any)
	if len(requests) != 1 {
		t.Fatalf("permissions requests length = %d; want 1", len(requests))
	}
	request := requests[0].(map[string]any)
	if got, want := request["summary"], "go test ./internal/tui"; got != want {
		t.Fatalf("permission summary = %v; want %q", got, want)
	}
	input := request["input"].(map[string]any)
	if got, want := input["command"], "go test ./internal/tui"; got != want {
		t.Fatalf("permission input.command = %v; want %q", got, want)
	}
}

func TestFeatureDetailLoadsBoundedHistoricalRuns(t *testing.T) {
	t.Parallel()
	_, f := seedReadFeature(t)
	f.ActiveRun = 25
	f.RunCount = 25
	reader := &countingFeatureReader{feature: f}
	handler := NewHandler(HandlerOptions{
		Runtime:      RuntimeIdentity{RuntimeDir: "/runtime", StateDir: "/state", Config: "/runtime/config.yaml"},
		FeatureStore: reader,
	})

	body := getJSONMap(t, handler, "/api/v1/features/"+f.ID)

	const wantHistoryLimit = 5
	if len(reader.loadRunNumbers) > wantHistoryLimit {
		t.Fatalf("feature detail LoadRun calls = %v; want at most %d recent historical runs", reader.loadRunNumbers, wantHistoryLimit)
	}
	if got, want := intsToCSV(reader.loadRunNumbers), "20,21,22,23,24"; got != want {
		t.Fatalf("feature detail LoadRun calls = %s; want %s", got, want)
	}
	rawHistory := body["feature"].(map[string]any)["historical_runs"].([]any)
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
			kind: ports.KindPhase, status: ports.SessionRunning, startedAt: now.Add(-2 * time.Minute),
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
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

	body := getJSONMap(t, handler, "/api/v1/sessions")
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
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

	body := getJSONMap(t, handler, "/api/v1/sessions")

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
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

	requestJSONMap(t, handler, http.MethodGet, "/api/v1/sessions/sess-historical", nil, http.StatusNotFound)
	requestJSONMap(t, handler, http.MethodGet, "/api/v1/sessions/sess-historical/transcript", nil, http.StatusNotFound)
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
				{Type: "user", User: &llm.UserMessage{Message: llm.ConversationMsg{
					Role:    "user",
					Content: []llm.ContentBlock{{Type: "text", Text: protocolPrompt}},
				}}},
				{Type: "user", LocallyAppended: true, User: &llm.UserMessage{Message: llm.ConversationMsg{
					Role:    "user",
					Content: []llm.ContentBlock{{Type: "text", Text: "PostgreSQL"}},
				}}},
				{Type: "user", LocallyAppended: true, AutoPicked: true, AutoPickQuestion: "Which cache?", AutoPickConfidence: 0.72, User: &llm.UserMessage{Message: llm.ConversationMsg{
					Role:    "user",
					Content: []llm.ContentBlock{{Type: "text", Text: "Redis (Recommended)"}},
				}}},
			},
		}},
	}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

	body := getJSONMap(t, handler, "/api/v1/sessions/sess-local-echo/transcript")
	messages := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want protocol prompt, local echo, and auto-picked rows", len(messages))
	}
	protocol := messages[0].(map[string]any)
	if protocol["text"] != protocolPrompt || protocol["redacted"] == true || protocol["locally_appended"] == true {
		t.Fatalf("protocol user row = %+v, want visible non-local prompt", protocol)
	}
	local := messages[1].(map[string]any)
	if local["text"] != "PostgreSQL" || local["redacted"] == true || local["locally_appended"] != true {
		t.Fatalf("local user echo row = %+v, want visible locally-appended text", local)
	}
	autoPicked := messages[2].(map[string]any)
	if autoPicked["text"] != "Redis (Recommended)" || autoPicked["locally_appended"] != true || autoPicked["auto_picked"] != true || autoPicked["auto_pick_confidence"] != 0.72 || autoPicked["auto_pick_question"] != "Which cache?" {
		t.Fatalf("auto-picked user row = %+v, want visible auto-picked metadata", autoPicked)
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
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

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
		Type: "tool_progress",
		ToolProgress: &llm.ToolProgressMessage{
			Type:      "tool_progress",
			ToolUseID: "toolu-bash-1",
			ToolName:  "Bash",
			Data:      "private-token output must stay redacted",
		},
	}}
	for i := 1; i <= 6; i++ {
		msgs = append(msgs, llm.SDKMessage{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
				Role:    "assistant",
				Content: []llm.ContentBlock{{Type: "text", Text: "preview line " + twoDigit(i)}},
			}},
		})
	}
	sessions := fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		id: "sess-live", featureID: f.ID, phase: feature.PhaseImplement,
		kind: ports.KindPhase, status: ports.SessionRunning, provider: "codex",
		messages: msgs,
	}}}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

	preview := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/live-preview")
	transcript := preview["transcript"].([]any)
	if len(transcript) != len(msgs) {
		t.Fatalf("live preview transcript len = %d; want %d rows (%+v)", len(transcript), len(msgs), transcript)
	}
	raw := mustMarshalJSON(t, preview)
	if !strings.Contains(raw, "preview line 01") || !strings.Contains(raw, "preview line 06") {
		t.Fatalf("live preview transcript did not include extended tail: %s", raw)
	}
	if strings.Contains(raw, "private-token") {
		t.Fatalf("live preview transcript leaked tool progress output: %s", raw)
	}
	tool := transcript[0].(map[string]any)
	if tool["type"] != "tool_progress" || tool["tool"] != "Bash" || tool["redacted"] != true {
		t.Fatalf("tool progress transcript row = %+v; want redacted Bash tool_progress row", tool)
	}
}

func TestSessionTranscriptIncludesSanitizedFileChangeRows(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	msgs := []llm.SDKMessage{
		{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
			Role: "assistant",
			Content: []llm.ContentBlock{{
				Type:  "tool_use",
				Name:  "Write",
				Input: json.RawMessage(`{"file_path":"docs/provider-notes.md","content":"# Provider notes\n\nUpdated for all providers.\n"}`),
			}},
		}}},
		{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{
			Type:     "tool_progress",
			ToolName: "Bash",
			Data:     "private-token output must stay redacted",
		}},
	}
	sessions := fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		id: "sess-1", featureID: f.ID, phase: feature.PhaseImplement,
		kind: ports.KindPhase, status: ports.SessionRunning, provider: "codex",
		messages: msgs,
	}}}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

	body := getJSONMap(t, handler, "/api/v1/sessions/sess-1/transcript?limit=10")
	raw := mustMarshalJSON(t, body)
	for _, want := range []string{`"file_change"`, `"path":"docs/provider-notes.md"`, `"operation":"write"`, `"+ # Provider notes`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("transcript missing sanitized file change %q in %s", want, raw)
		}
	}
	if strings.Contains(raw, "private-token") {
		t.Fatalf("transcript leaked redacted tool progress output: %s", raw)
	}
}

func TestSessionTranscriptIncludesStructuredCodexFileChangeDiffRows(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	msgs := []llm.SDKMessage{{
		Type: "tool_progress",
		ToolProgress: &llm.ToolProgressMessage{
			Type:      "tool_progress",
			ToolUseID: "call_write",
			ToolName:  "Write",
		},
		FileChanges: []llm.FileChangeEvent{{
			Path:         filepath.Join("/work/repo", "README.md"),
			Operation:    "update",
			Detail:       "@@ -1,2 +1,2 @@\n-old\n+new\n",
			AddedLines:   1,
			RemovedLines: 1,
			HasDiffPatch: true,
		}},
	}}
	sessions := fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		id: "sess-1", featureID: f.ID, phase: feature.PhaseImplement,
		kind: ports.KindPhase, status: ports.SessionRunning, provider: "codex",
		workDir:  "/work/repo",
		messages: msgs,
	}}}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

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
		{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
			Role: "assistant",
			Content: []llm.ContentBlock{{
				Type:  "tool_use",
				Name:  "Agent",
				Input: json.RawMessage(`{"description":"Explore KB completion handler","prompt":"Inspect KB docs and return impacted categories with private-token omitted."}`),
			}},
		}}},
		{Type: "system", Subtype: "task_started", TaskStarted: &llm.TaskStartedMessage{
			Type:        "system",
			Subtype:     "task_started",
			TaskID:      "task-1",
			Description: "inspect provider docs",
			TaskType:    "local_agent",
			Prompt:      "Read the provider docs and report every attach-view metadata gap with private-token omitted.",
		}},
		{Type: "system", Subtype: "task_progress", TaskProgress: &llm.TaskProgressMessage{
			Type:         "system",
			Subtype:      "task_progress",
			TaskID:       "task-1",
			Description:  "inspect provider docs",
			LastToolName: "Read",
		}},
		{Type: "system", Subtype: "task_notification", TaskNotification: &llm.TaskNotificationMessage{
			Type:    "system",
			Subtype: "task_notification",
			TaskID:  "task-1",
			Status:  "completed",
			Summary: "found API transcript gaps",
		}},
	}
	sessions := fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		id: "sess-1", featureID: f.ID, phase: feature.PhaseImplement,
		kind: ports.KindPhase, status: ports.SessionRunning, provider: "claude",
		messages: msgs,
	}}}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

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
	if strings.Contains(raw, "private-token") {
		t.Fatalf("transcript leaked unsanitized task/delegation prompt: %s", raw)
	}
}

func TestArtifactLogLivePreviewAndSessionReadsAreBoundedAndRedacted(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	runDir := store.RunDir(f.ID, 1)
	writeFile(t, filepath.Join(runDir, "plan", "phase-plan.md"), "hello artifact content")
	writeFile(t, filepath.Join(runDir, "logs", "session.log"), "first\nsecond\nthird\n")
	msgs := []llm.SDKMessage{
		{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: "safe text"}, {Type: "tool_use", Name: "Bash", Input: json.RawMessage(`{"command":"echo private-token"}`)}}}}},
		{Type: "user", User: &llm.UserMessage{Message: llm.ConversationMsg{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "raw prompt private-token"}}}}},
	}
	sessions := fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		id: "sess-1", featureID: f.ID, phase: feature.PhaseImplement, repoName: "agentic-orchestrator",
		kind: ports.KindPhase, status: ports.SessionRunning, provider: "codex", model: "gpt-5.4",
		logPath: filepath.Join(runDir, "logs", "session.log"), messages: msgs,
		initialPrompt: "private-token initial prompt",
	}}}
	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
		Sessions: sessions,
	})

	list := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/artifacts")
	if got := len(list["artifacts"].([]any)); got == 0 {
		t.Fatal("artifact list is empty")
	}
	content := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/artifacts/plan?offset=6&limit=8")
	if content["text"] != "artifact" {
		t.Fatalf("artifact text slice = %q; want artifact", content["text"])
	}
	requestJSONMap(t, handler, http.MethodGet, "/api/v1/features/"+f.ID+"/runs/1/artifacts/..%2Ffeature.yaml", nil, http.StatusBadRequest)

	logBody := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/logs/session?offset=6&limit=6")
	if logBody["text"] != "second" {
		t.Fatalf("log text slice = %q; want second", logBody["text"])
	}
	preview := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/live-preview")
	if preview["feature"].(map[string]any)["id"] != f.ID {
		t.Fatalf("live preview feature = %+v; want %s", preview["feature"], f.ID)
	}
	detail := getJSONMap(t, handler, "/api/v1/sessions/sess-1")
	if got, want := detail["session"].(map[string]any)["initial_prompt"], "[redacted] initial prompt"; got != want {
		t.Fatalf("session detail initial_prompt = %q; want %q", got, want)
	}

	for _, path := range []string{"/api/v1/sessions", "/api/v1/sessions/sess-1", "/api/v1/sessions/sess-1/transcript?limit=10"} {
		raw := mustMarshalJSON(t, getJSONMap(t, handler, path))
		if strings.Contains(raw, "private-token") {
			t.Fatalf("%s leaks redacted content in %s", path, raw)
		}
	}
}

func TestArtifactReadsAllowAbsolutePathsWithinSameRun(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	runDir := store.RunDir(f.ID, 1)
	roadmapPath := filepath.Join(runDir, "roadmap", "roadmap.md")
	writeFile(t, roadmapPath, "# Roadmap\n\nTranslate README.\n")
	f.Status = feature.StatusPlanNeedsReview
	f.CurrentPhase = feature.PhasePlan
	f.Artifacts = map[string]string{"roadmap": roadmapPath}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
	})

	list := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/artifacts")
	rawArtifacts := list["artifacts"].([]any)
	if len(rawArtifacts) != 1 {
		t.Fatalf("artifacts len = %d; want 1 (%+v)", len(rawArtifacts), rawArtifacts)
	}
	artifact := rawArtifacts[0].(map[string]any)
	if artifact["id"] != "roadmap" || artifact["path"] != roadmapPath || artifact["content_available"] != true {
		t.Fatalf("roadmap artifact = %+v; want available roadmap with absolute path", artifact)
	}
	if artifact["phase"] != "roadmap" {
		t.Fatalf("roadmap artifact phase = %v; want roadmap", artifact["phase"])
	}

	content := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1/artifacts/roadmap?offset=2&limit=7")
	if content["text"] != "Roadmap" {
		t.Fatalf("roadmap content = %q; want Roadmap", content["text"])
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

	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: "/runtime", StateDir: store.BaseDir, Config: "/runtime/config.yaml"},
		Features: store,
	})

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)
	activeRun := detail["feature"].(map[string]any)["active_run_detail"].(map[string]any)
	if activeRun["pending_review_phase"] != "plan" {
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
	if content["text"] != "prompt" {
		t.Fatalf("description-review content = %q; want prompt", content["text"])
	}
}

func TestSSEEmitsMetadataOnlyEventsAndHeartbeat(t *testing.T) {
	t.Parallel()
	eventCh := make(chan interface{}, 4)
	handler := NewHandler(HandlerOptions{Events: eventCh})
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
	eventCh <- ports.Event{Type: ports.PhaseCompleted, FeatureID: "feat-001", Phase: feature.PhaseImplement, Error: errors.New("private-token /tmp/path")}
	updated := readSSEBlock(t, reader, "lifecycle.updated")
	if !strings.Contains(updated, `"feature_id":"feat-001"`) {
		t.Fatalf("lifecycle event = %s; want feature id", updated)
	}
	if strings.Contains(updated, "private-token") || strings.Contains(updated, "/tmp/path") {
		t.Fatalf("SSE event leaks unsafe detail: %s", updated)
	}
	heartbeat := readSSEBlock(t, reader, "heartbeat")
	if !strings.Contains(heartbeat, "event: heartbeat") {
		t.Fatalf("heartbeat event = %s; want heartbeat", heartbeat)
	}
}

func TestSSEEmitsShutdownFromDomainEvents(t *testing.T) {
	t.Parallel()
	domainCh := make(chan ports.Event, 1)
	handler := NewHandler(HandlerOptions{DomainEvents: domainCh})
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
	updated := readSSEBlock(t, reader, "shutdown.updated")
	if !strings.Contains(updated, `"resource":{"type":"runtime"`) {
		t.Fatalf("shutdown event = %s; want runtime resource", updated)
	}
	if !strings.Contains(updated, `"snapshot_required":true`) {
		t.Fatalf("shutdown event = %s; want snapshot_required", updated)
	}
	if strings.Contains(updated, "private-token") || strings.Contains(updated, "/tmp/agentico-runtime") {
		t.Fatalf("shutdown event leaks unsafe detail: %s", updated)
	}
}

func TestRuntimeServerCloseEmitsShutdownNotification(t *testing.T) {
	srv, err := Start(context.Background(), Options{})
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
	updated := readSSEBlock(t, reader, "shutdown.updated")
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

func seedReadFeature(t *testing.T) (*feature.Store, *feature.Feature) {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	f := &feature.Feature{
		ID: "feat-001", Name: "Read API", Slug: "read-api", Description: "private-token description",
		Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement, Created: now,
		Repos:       []feature.FeatureRepo{{Name: "agentic-orchestrator", Path: "/repo/path", WorktreePath: "/worktree/path", Branch: "feature/secret"}},
		Models:      config.ModelConfig{Research: "opus[1m]", Planning: "opus[1m]", Implementation: "opus[1m]", Review: "gpt-5.4", Utilities: "sonnet", KBBuild: "opus[1m]"},
		Inquireness: "high", ExitCriteria: "private-token exit criteria", ActiveRun: 1, RunCount: 1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	f.CurrentIteration = 2
	f.CurrentRoadmapPhase = 1
	f.TotalRoadmapPhases = 3
	f.CurrentPhaseStatus = "implementing"
	f.Artifacts = map[string]string{"plan": "plan/phase-plan.md"}
	f.RepoStates = map[string]*feature.RepoState{"agentic-orchestrator": {Touched: true, PRURL: "https://github.example/pr/1"}}
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
	f.Artifacts = map[string]string{"plan": "plan/phase-plan.md"}
	f.RepoStates = map[string]*feature.RepoState{"agentic-orchestrator": {Touched: true}}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save(%s) error = %v", id, err)
	}
	return f
}

func pendingReadControl(requestID, toolName, input string) *llm.ControlRequestMessage {
	return &llm.ControlRequestMessage{
		Type:      "control_request",
		RequestID: requestID,
		Request: llm.ControlRequest{
			Subtype:  "can_use_tool",
			ToolName: toolName,
			Input:    json.RawMessage(input),
		},
	}
}

func getJSONMap(t *testing.T, handler http.Handler, path string) map[string]any {
	t.Helper()
	return requestJSONMap(t, handler, http.MethodGet, path, nil, http.StatusOK)
}

func requestJSONMap(t *testing.T, handler http.Handler, method, path string, body io.Reader, wantStatus int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s status = %d; want %d; body: %s", method, path, resp.StatusCode, wantStatus, data)
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

func assertActionScope(t *testing.T, action map[string]any, wantType, wantRepoSelection string) {
	t.Helper()
	scope := action["scope"].(map[string]any)
	if scope["type"] != wantType {
		t.Fatalf("action %s scope type = %v; want %s", action["id"], scope["type"], wantType)
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
			Name:        "agentic-orchestrator",
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

func requestIDsFromJSON(t *testing.T, raw any) []string {
	t.Helper()
	items := raw.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.(map[string]any)["request_id"].(string))
	}
	return out
}

func questionsFromJSON(t *testing.T, raw any) []string {
	t.Helper()
	items := raw.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.(map[string]any)["question"].(string))
	}
	return out
}

func featureIDsFromJSON(t *testing.T, raw any) []string {
	t.Helper()
	items := raw.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.(map[string]any)["feature_id"].(string))
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
	name   string
	models []string
}

func (p fakeProvider) Name() string { return p.name }

func (p fakeProvider) MatchesModel(model string) bool {
	model = strings.TrimPrefix(model, p.name+":")
	for _, candidate := range p.models {
		if candidate == model {
			return true
		}
	}
	return false
}

func (p fakeProvider) DetectCLI() bool { return true }

func (p fakeProvider) AvailableModels() []string { return append([]string(nil), p.models...) }

func (p fakeProvider) BuildCommand(llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}

func (p fakeProvider) NewProtocol(llm.ProtocolOpts) llm.Protocol { return nil }

func (p fakeProvider) InstallHint() string { return "" }

func (p fakeProvider) VersionInfo() (string, error) { return "test", nil }

func (p fakeProvider) MinVersion() [3]int { return [3]int{} }

func (p fakeProvider) EnvVarsToExclude() []string { return nil }

func (p fakeProvider) ModelCatalog() []llm.ModelInfo {
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
	id            string
	featureID     string
	phase         feature.Phase
	repoName      string
	kind          ports.SessionKind
	label         string
	status        ports.SessionStatus
	startedAt     time.Time
	iteration     int
	initialPrompt string
	provider      string
	model         string
	workDir       string
	logPath       string
	messages      []llm.SDKMessage
	pending       []*llm.ControlRequestMessage
}

func (s *fakeSessionView) ID() string                  { return s.id }
func (s *fakeSessionView) FeatureID() string           { return s.featureID }
func (s *fakeSessionView) Phase() feature.Phase        { return s.phase }
func (s *fakeSessionView) RepoName() string            { return s.repoName }
func (s *fakeSessionView) PermCacheScope() string      { return "" }
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
func (s *fakeSessionView) InitialPrompt() string        { return s.initialPrompt }
func (s *fakeSessionView) ProviderName() string         { return s.provider }
func (s *fakeSessionView) Model() string                { return s.model }
func (s *fakeSessionView) WorkDir() string              { return s.workDir }
func (s *fakeSessionView) MessageLog() ports.MessageLog { return fakeMessageLog{messages: s.messages} }
func (s *fakeSessionView) Cost() *llm.ResultMessage     { return nil }
func (s *fakeSessionView) LatestUsage() *llm.Usage      { return nil }
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
func (s *fakeSessionView) QALog() []ports.QAPair           { return nil }
func (s *fakeSessionView) LogFilePath() string             { return s.logPath }
func (s *fakeSessionView) ContextPercentage() int          { return 42 }
func (s *fakeSessionView) ErrorDetail() string             { return "" }
func (s *fakeSessionView) ExitCodeDetail() string          { return "" }
func (s *fakeSessionView) LastStdoutAt() time.Time         { return s.StartedAt() }
func (s *fakeSessionView) StatusCh() <-chan string         { return nil }
func (s *fakeSessionView) AttachCh() <-chan llm.SDKMessage { return nil }
func (s *fakeSessionView) Done() <-chan struct{}           { return nil }
func (s *fakeSessionView) HasPendingAskUserQuestion() bool {
	for _, req := range s.pending {
		if req.Request.ToolName == "AskUserQuestion" {
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
