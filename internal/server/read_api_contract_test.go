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

func TestConfigCatalogPromptPermissionAndOperationSnapshots(t *testing.T) {
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
		"/api/v1/operations",
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
	ops := getJSONMap(t, handler, "/api/v1/operations")
	if got := len(ops["operations"].([]any)); got != 0 {
		t.Fatalf("operations length = %d; want empty schema-first list", got)
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
	assertActionScope(t, actionsByID["rebase"], "feature", "optional")
	assertActionInputNames(t, actionsByID["rebase"], "repo", "rebase_target", "conflict_files")
	assertActionInputRequired(t, actionsByID["rebase"], "repo", false)
	assertActionScope(t, actionsByID["review-comments"], "feature", "required")
	assertActionInputNames(t, actionsByID["review-comments"], "repo", "mode")
	assertActionInputRequired(t, actionsByID["review-comments"], "repo", true)
	assertActionInputRequired(t, actionsByID["review-comments"], "mode", true)
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

	for _, path := range []string{"/api/v1/sessions", "/api/v1/sessions/sess-1", "/api/v1/sessions/sess-1/transcript?limit=10"} {
		raw := mustMarshalJSON(t, getJSONMap(t, handler, path))
		if strings.Contains(raw, "private-token") || strings.Contains(raw, "raw prompt") {
			t.Fatalf("%s leaks redacted content in %s", path, raw)
		}
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
func (l fakeMessageLog) ToolUseBlocks() []llm.ContentBlock     { return nil }
