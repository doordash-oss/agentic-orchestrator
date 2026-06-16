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

package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestAPIAppModelInitializesFromRESTSnapshots(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	created := time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)
	runtime := server.RuntimeIdentity{
		RuntimeDir: "/tmp/agentico-runtime",
		StateDir:   "/tmp/agentico-runtime/features",
		Config:     "/tmp/agentico-runtime/config.yaml",
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "done", Name: "Done feature", Slug: "done-feature", Status: "Done", CurrentPhase: "publish", CreatedAt: created.Add(-2 * time.Hour)},
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", ActiveRun: 2, RunCount: 3, Repos: []string{"agentic-orchestrator"}, CreatedAt: created},
			{ID: "published", Name: "Published feature", Slug: "published-feature", Status: "Published", CurrentPhase: "publish", CreatedAt: created.Add(-1 * time.Hour)},
		}},
		runtime: server.RuntimeConfigResponse{
			Runtime:   runtime,
			Providers: []string{"codex"},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{"codex"},
			ProviderModels: map[string][]server.ModelDTO{
				"codex": {{ID: "gpt-5.4"}},
			},
		},
		prompts: server.PromptSnapshotResponse{HelpQueue: []server.HelpQueueDTO{
			{FeatureID: "active", Question: "Need a decision", Pending: true},
		}},
		permissions: server.PermissionSnapshotResponse{Requests: []server.ControlRequestDTO{
			{FeatureID: "active", RequestID: "perm-1", Status: "pending", ToolName: "Bash", Summary: "run tests"},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: "sess-1", FeatureID: "active", Phase: "implement", Repo: "agentic-orchestrator", Kind: "agent", Label: "Implement", Provider: "codex", Model: "gpt-5.4", Status: "running", ContextPct: 42},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement"},
			Description:    "Render selected feature detail from REST.",
			Pipeline:       "roadmap",
			RepoStatus: []server.RepoStatusDTO{
				{Name: "agentic-orchestrator", Touched: true, Publishable: true, CycleType: "rebase", CycleStatus: "running"},
			},
			Actions: []server.ActionDTO{
				{ID: "feature.stop", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}},
				{ID: "feature.publish", Enabled: false, Scope: server.ActionScopeDTO{Type: "feature"}, DisabledReasons: []server.ActionDisabledReasonDTO{
					{Code: "not_ready", Message: "feature is not ready to publish"},
				}},
			},
			Cost:          server.CostDTO{TotalUSD: 12.34},
			NeedUserInput: &server.NeedInputGateDTO{FeatureID: "active", Open: true, Scope: "feature", Iteration: 9},
		}},
	}

	app, err := NewAPIAppModel(ctx, client, APIAppOptions{
		Runtime:      runtime,
		LaunchPolicy: server.LaunchPolicy{Resolved: true, Providers: []string{"codex"}, DangerouslySkipPermissions: true},
	})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	if got, want := strings.Join(client.calls, ","), "Features,RuntimeConfig,ModelCatalog,Prompts,Permissions,Sessions,Recovery,FeatureDetail,LivePreview"; got != want {
		t.Fatalf("API calls = %s, want %s", got, want)
	}
	if got := strings.Join(client.detailFeatureIDs, ","); got != "active" {
		t.Fatalf("FeatureDetail calls = %q, want active", got)
	}
	if got := app.SelectedFeatureID(); got != "active" {
		t.Fatalf("SelectedFeatureID() = %q, want active", got)
	}
	if got := app.Snapshot().Features[0].AttentionCount; got != 2 {
		t.Fatalf("active AttentionCount = %d, want 2", got)
	}
	if got := app.Snapshot().Runtime.DangerouslySkipPermissions; !got {
		t.Fatal("Runtime.DangerouslySkipPermissions = false, want true from launch policy")
	}
	view := stripANSI(app.View().Content)
	for _, want := range []string{"Orchestrator v", "Features", "IN PROGRESS", "PUBLISHED", "COMPLETED", "client-cutover", "published-feature", "done-feature", "Live Preview", "Permission Request", "Bash: run tests", "$12.34", "NeedUserInput"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{"Selected detail", "Attach / Live Preview", "Run Content", "Operations"} {
		if strings.Contains(view, notWant) {
			t.Fatalf("API app View() rendered reduced-client section %q in:\n%s", notWant, view)
		}
	}
	for _, leaked := range []string{runtime.RuntimeDir, runtime.StateDir, runtime.Config} {
		if leaked != "" && strings.Contains(view, leaked) {
			t.Fatalf("API app View() leaked runtime path %q in:\n%s", leaked, view)
		}
	}
}

func TestAPIAppModelDashboardKeepsManualPublishCodeReady(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           "ready",
		Name:         "Ready work",
		Slug:         "ready-work",
		Status:       "CodeReady",
		CurrentPhase: "publish",
		Repos:        []string{"agentic-orchestrator"},
		CreatedAt:    time.Now(),
		Checkpoints:  server.CheckpointsDTO{ManualPublish: true},
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: summary,
			RepoStatus: []server.RepoStatusDTO{
				{Name: "agentic-orchestrator", Publishable: true},
			},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	features := app.apiDashboardFeatures()
	if len(features) != 1 {
		t.Fatalf("apiDashboardFeatures length = %d, want 1", len(features))
	}
	f := features[0]
	if !f.Checkpoints.ManualPublish {
		t.Fatalf("dashboard feature checkpoints = %+v; want manual publish preserved", f.Checkpoints)
	}
	row := stripANSI(NewDashboardModel(features, "").renderFeatureRowCompact(f, false))
	if !strings.Contains(row, "Code Ready") {
		t.Fatalf("dashboard row = %q; want Code Ready", row)
	}
	if strings.Contains(row, "Publishing") {
		t.Fatalf("dashboard row = %q; should not show Publishing for manual publish CodeReady", row)
	}
}

func TestAPIAppModelDashboardFeatureUsesDetailModels(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           "feature-1",
		Name:         "Model override",
		Slug:         "model-override",
		Status:       "Created",
		CurrentPhase: "research",
		CreatedAt:    time.Now(),
	}
	selected := config.ModelConfig{
		Research:       "selected-research",
		Planning:       "selected-planning",
		Implementation: "selected-implementation",
		Review:         "selected-review",
		KBBuild:        "selected-kb",
	}
	app := APIAppModel{
		runtimeConfig: server.RuntimeConfigResponse{
			Defaults: config.ModelConfig{
				Research:       "default-research",
				Planning:       "default-planning",
				Implementation: "default-implementation",
				Review:         "default-review",
				KBBuild:        "default-kb",
			},
		},
		featureList: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			summary.ID: {Feature: server.FeatureDetailDTO{
				FeatureSummary: summary,
				Models:         selected,
			}},
		},
	}

	features := app.apiDashboardFeatures()
	if len(features) != 1 {
		t.Fatalf("apiDashboardFeatures length = %d, want 1", len(features))
	}
	if got := features[0].Models; got != selected {
		t.Fatalf("dashboard feature models = %+v, want detail models %+v", got, selected)
	}
}

func TestAPIAppModelAdvertisesProductionWorkflowSurface(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		runtime: server.RuntimeConfigResponse{
			Repos:     []server.ConfigRepoDTO{{Name: "agentic-orchestrator", Path: "/workspace/agentic-orchestrator"}},
			Providers: []string{"codex"},
		},
		livePreview: server.LivePreviewResponse{
			Feature:  server.FeatureSummary{ID: "active", Name: "Client cutover"},
			Activity: "Using Bash...",
			Session:  &server.SessionSummaryDTO{ID: "sess-live", FeatureID: "active", Label: "Implement", Status: "running"},
		},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	view := stripANSI(app.View().Content)
	for _, want := range []string{
		"Orchestrator v",
		"Features",
		"IN PROGRESS",
		"client-cutover",
		"Live Preview",
		"[n] New",
		"[→/enter] Focus",
		"[Shift+W] Workspaces",
		"[Shift+R] Resume All",
		"[tab] Panel",
		"Layout: US",
		"[/] Ask",
		"[?] Help",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app production surface missing %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{"Agentico API Client", "Selected detail", "Attach / Live Preview", "Run Content"} {
		if strings.Contains(view, notWant) {
			t.Fatalf("API app View() exposed reduced-client surface %q in:\n%s", notWant, view)
		}
	}
	if strings.Contains(view, "/workspace/agentic-orchestrator") {
		t.Fatalf("API app View() leaked workspace path:\n%s", view)
	}
}

func TestAPIAppModelRecoverySnapshotUsesRESTAndSubmitsAction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		recovery: server.RecoverySnapshotResponse{
			SnapshotID: "recovery-snapshot-1",
			Items: []server.RecoveryItemDTO{{
				Key:            "feat-recover:api",
				FeatureID:      "feat-recover",
				FeatureName:    "Recover me",
				RepoName:       "api",
				Phase:          "implement",
				Iteration:      7,
				PID:            12345,
				ProcessAlive:   true,
				DefaultAction:  "skip",
				AllowedActions: []string{"resume", "kill", "skip"},
			}},
		},
		executeRecoveryAccepted: apiTestActionResponse{Result: "executed"},
	}

	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	view := stripANSI(app.View().Content)
	for _, want := range []string{"Session Recovery", "Recover me", "api", "implement", "iter 7", "PID 12345", "[S]kip"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing recovery text %q in:\n%s", want, view)
		}
	}

	model, cmd := app.Update(tea.KeyPressMsg{Text: "r", Code: 'r'})
	if cmd != nil {
		t.Fatal("Update(r) returned command before recovery submit")
	}
	resumeSelected := model.(APIAppModel)
	view = stripANSI(resumeSelected.View().Content)
	if !strings.Contains(view, "[R]esume") {
		t.Fatalf("API app View() did not select recovery resume:\n%s", view)
	}

	model, cmd = resumeSelected.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want recovery execute mutation")
	}
	msg := cmd()
	model, _ = model.Update(msg)
	submitted := model.(APIAppModel)

	if got := strings.Join(client.executeRecoverySnapshotIDs, ","); got != "recovery-snapshot-1" {
		t.Fatalf("ExecuteRecovery snapshot IDs = %q, want recovery-snapshot-1", got)
	}
	if got := client.executeRecoveryRequests[0].Actions["feat-recover:api"]; got != "resume" {
		t.Fatalf("ExecuteRecovery action = %q, want resume", got)
	}
	if strings.Contains(stripANSI(submitted.View().Content), "Session Recovery") {
		t.Fatalf("API app View() still shows recovery panel after accepted submit:\n%s", stripANSI(submitted.View().Content))
	}
	if !strings.Contains(submitted.statusMessage, "Completed Recovery") {
		t.Fatalf("statusMessage = %q, want completed recovery status", submitted.statusMessage)
	}
}

func TestAPIAppModelRecoveryTweakShowsKillOnlyAffordance(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		recovery: server.RecoverySnapshotResponse{
			SnapshotID: "recovery-snapshot-tweak",
			Items: []server.RecoveryItemDTO{{
				Key:            "feat-tweak:api",
				FeatureID:      "feat-tweak",
				FeatureName:    "Tweak me",
				RepoName:       "api",
				Phase:          "implement",
				Iteration:      2,
				PID:            23456,
				ProcessAlive:   true,
				Tweak:          true,
				DefaultAction:  "kill",
				AllowedActions: []string{"kill"},
			}},
		},
		executeRecoveryAccepted: apiTestActionResponse{Result: "executed"},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	view := stripANSI(app.View().Content)
	for _, want := range []string{"Tweak me", "[K]ill", "interactive tweak - kill only", "[k] Kill", "[enter] Continue"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API recovery view missing %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{"[r] Resume", "[s] Skip"} {
		if strings.Contains(view, notWant) {
			t.Fatalf("API recovery view unexpectedly advertised %q in:\n%s", notWant, view)
		}
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd != nil {
		t.Fatal("Update(r) returned command before recovery submit")
	}
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want recovery execute")
	}
	msg := cmd()
	_, _ = model.(APIAppModel).Update(msg)
	if got := client.executeRecoveryRequests; len(got) != 1 || got[0].Actions["feat-tweak:api"] != "kill" {
		t.Fatalf("ExecuteRecovery requests = %+v, want kill action", got)
	}
}

func TestAPIAppModelSessionSnapshotRefreshUsesAPIReadModels(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: "sess-1", FeatureID: "active", Phase: "implement", Repo: "agentic-orchestrator", Kind: "agent", Label: "Implement", Status: "running", ContextPct: 10},
		}},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	initialDetailCalls := len(client.detailFeatureIDs)

	initial := app.Snapshot()
	if len(initial.Sessions) != 1 || initial.Sessions[0].ID != "sess-1" || initial.Sessions[0].ContextPct != 10 {
		t.Fatalf("initial Snapshot().Sessions = %+v, want sess-1 at 10%%", initial.Sessions)
	}

	signal := server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: "session.updated"},
		Resource: server.ResourceDTO{Type: "session", ID: "sess-1", FeatureID: "active"},
	}
	client.refreshSnapshot = server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: server.SessionDetailDTO{
			SessionSummaryDTO: server.SessionSummaryDTO{ID: "sess-1", FeatureID: "active", Phase: "implement", Repo: "agentic-orchestrator", Kind: "agent", Label: "Implement", Status: "completed", ContextPct: 37},
			CanAttach:         false,
			LogAvailable:      true,
		}},
	}
	msg := app.fetchRefreshSnapshotCmd(signal)()
	model, _ := app.Update(msg)
	refreshed := model.(APIAppModel)

	if got := strings.Join(client.calls, ","); !strings.Contains(got, "FetchRefreshSnapshot") {
		t.Fatalf("API calls = %s, want targeted session refresh through FetchRefreshSnapshot", got)
	}
	if got := len(client.detailFeatureIDs); got != initialDetailCalls {
		t.Fatalf("FeatureDetail calls after session refresh = %d, want unchanged %d", got, initialDetailCalls)
	}
	snapshot := refreshed.Snapshot()
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].ID != "sess-1" || snapshot.Sessions[0].Status != "completed" || snapshot.Sessions[0].ContextPct != 37 || !snapshot.Sessions[0].LogAvailable {
		t.Fatalf("refreshed Snapshot().Sessions = %+v, want completed sess-1 with log at 37%%", snapshot.Sessions)
	}
}

func TestAPIAppModelLivePreviewLoadsSelectedFeatureFromREST(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		livePreview: server.LivePreviewResponse{
			Feature:  server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
			Activity: "Using Bash...",
			Session:  &server.SessionSummaryDTO{ID: "sess-live", FeatureID: "active", Phase: "implement", Label: "Implement", Status: "running"},
			Context:  server.ContextDTO{Percentage: 42},
			Cost:     server.CostDTO{TotalUSD: 0.42},
			Attention: []server.ControlRequestDTO{
				{RequestID: "ask-1", FeatureID: "active", ToolName: "AskUserQuestion", Status: "pending", Summary: "Pick the cutover path"},
			},
			Transcript: []server.TranscriptMessageDTO{
				{Index: 1, Role: "assistant", Type: "text", Text: "Ready to patch live preview"},
			},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	if got := strings.Join(client.livePreviewFeatureIDs, ","); got != "active" {
		t.Fatalf("LivePreview calls = %q, want active", got)
	}
	view := stripANSI(app.View().Content)
	for _, want := range []string{"Live Preview", "Using Bash...", "42%", "$0.42", "Ready to patch live preview"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelTranscriptLoadsSelectedSessionFromREST(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		livePreview: server.LivePreviewResponse{
			Feature: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
			Session: &server.SessionSummaryDTO{ID: "sess-live", FeatureID: "active", Phase: "implement", Label: "Implement", Status: "running"},
		},
		sessionDetail: server.SessionDetailResponse{Session: server.SessionDetailDTO{
			SessionSummaryDTO: server.SessionSummaryDTO{ID: "sess-live", FeatureID: "active", Phase: "implement", Label: "Implement", Status: "running"},
			TranscriptCursor:  server.CursorDTO{Total: 64, Start: 0, End: 64},
		}},
		transcript: server.TranscriptResponse{
			Cursor: server.CursorDTO{Total: 64, Start: 14, End: 64},
			Messages: []server.TranscriptMessageDTO{
				{Index: 62, Role: "assistant", Type: "text", Text: "Patch transcript continuation"},
				{Index: 63, Role: "system", Type: "tool_use", Tool: "Bash", Redacted: true},
			},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	if got := strings.Join(client.sessionDetailIDs, ","); got != "sess-live" {
		t.Fatalf("SessionDetail calls = %q, want sess-live", got)
	}
	if got := strings.Join(client.transcriptSessionIDs, ","); got != "sess-live" {
		t.Fatalf("Transcript calls = %q, want sess-live", got)
	}
	if len(client.transcriptQueries) != 1 {
		t.Fatalf("Transcript query count = %d, want 1", len(client.transcriptQueries))
	}
	if got := client.transcriptQueries[0]; got.Cursor != 14 || got.Limit != 50 {
		t.Fatalf("Transcript query = %+v, want cursor 14 limit 50", got)
	}
	snapshot := app.Snapshot()
	if snapshot.Transcript == nil || snapshot.Transcript.SessionID != "sess-live" {
		t.Fatalf("Snapshot().Transcript = %+v, want sess-live transcript", snapshot.Transcript)
	}
	if got := strings.Join(snapshot.Transcript.Lines, "\n"); !strings.Contains(got, "Patch transcript continuation") || !strings.Contains(got, "Bash") {
		t.Fatalf("Snapshot().Transcript lines = %q, want transcript continuation and Bash", got)
	}
}

func TestAPIAppModelLoadsSelectedRunContentFromREST(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
			ActiveRun:      &server.RunSummaryDTO{RunNumber: 3, CurrentPhase: "implement", ArtifactCount: 1},
		}},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: "plan", RunNumber: 3, Phase: "plan", Size: apiContentTailLimit + 14, ContentAvailable: true},
		}},
		logContent: server.TextContentResponse{
			ID:     "session",
			Offset: 0,
			Limit:  apiContentTailLimit,
			Size:   apiContentTailLimit + 80,
			Text:   "log tail from server",
		},
		artifactContent: server.TextContentResponse{
			ID:     "plan",
			Offset: 14,
			Limit:  apiContentTailLimit,
			Size:   apiContentTailLimit + 14,
			Text:   "artifact tail from server",
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	if got := strings.Join(client.logContentIDs, ","); got != "session" {
		t.Fatalf("LogContent IDs = %q, want session", got)
	}
	if got := client.logContentQueries[0]; got.Offset != 0 || got.Limit != apiContentTailLimit {
		t.Fatalf("LogContent query = %+v, want offset 0 limit %d", got, apiContentTailLimit)
	}
	if got := strings.Join(client.artifactListFeatureIDs, ","); got != "active" {
		t.Fatalf("ArtifactList feature IDs = %q, want active", got)
	}
	if got := strings.Join(client.artifactContentIDs, ","); got != "plan" {
		t.Fatalf("ArtifactContent IDs = %q, want plan", got)
	}
	if got := client.artifactContentQueries[0]; got.Offset != 14 || got.Limit != apiContentTailLimit {
		t.Fatalf("ArtifactContent query = %+v, want offset 14 limit %d", got, apiContentTailLimit)
	}
	snapshot := app.Snapshot()
	if snapshot.Content == nil || snapshot.Content.RunNumber != 3 {
		t.Fatalf("Snapshot().Content = %+v, want run 3 content", snapshot.Content)
	}
	view := stripANSI(app.View().Content)
	if strings.Contains(view, "Run Content") {
		t.Fatalf("API app View() showed run content before opening the content panel:\n%s", view)
	}
	app.contentPanelActive = true
	view = stripANSI(app.View().Content)
	for _, want := range []string{"Run Content", "Log session", "log tail from server", "Artifact plan", "artifact tail from server"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelLogRefreshUsesBoundedContentTail(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
			ActiveRun:      &server.RunSummaryDTO{RunNumber: 4, CurrentPhase: "implement", ArtifactCount: 1},
		}},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: "plan", RunNumber: 4, Phase: "plan", Size: apiContentTailLimit + 120, ContentAvailable: true},
		}},
		logContent: server.TextContentResponse{
			ID:     "session",
			Offset: 0,
			Limit:  apiContentTailLimit,
			Size:   apiContentTailLimit + 250,
			Text:   "initial log tail",
		},
		artifactContent: server.TextContentResponse{
			ID:     "plan",
			Offset: 120,
			Limit:  apiContentTailLimit,
			Size:   apiContentTailLimit + 120,
			Text:   "initial artifact tail",
		},
		refreshSnapshot: server.RefreshSnapshot{
			Session: &server.SessionDetailResponse{Session: server.SessionDetailDTO{
				SessionSummaryDTO: server.SessionSummaryDTO{ID: "sess-live", FeatureID: "active", Status: "running"},
			}},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	client.logContent = server.TextContentResponse{
		ID:     "session",
		Offset: 250,
		Limit:  apiContentTailLimit,
		Size:   apiContentTailLimit + 375,
		Text:   "refreshed log tail",
	}
	client.artifactContent = server.TextContentResponse{
		ID:     "plan",
		Offset: 120,
		Limit:  apiContentTailLimit,
		Size:   apiContentTailLimit + 120,
		Text:   "refreshed artifact tail",
	}
	signal := server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: "log.updated"},
		Resource: server.ResourceDTO{Type: "session", ID: "sess-live", FeatureID: "active"},
	}
	msg := app.fetchRefreshSnapshotCmd(signal)()
	model, _ := app.Update(msg)
	refreshed := model.(APIAppModel)

	if got, want := len(client.logContentQueries), 2; got != want {
		t.Fatalf("LogContent query count = %d, want %d", got, want)
	}
	if got := client.logContentQueries[1]; got.Offset != 250 || got.Limit != apiContentTailLimit {
		t.Fatalf("refresh LogContent query = %+v, want offset 250 limit %d", got, apiContentTailLimit)
	}
	if got, want := len(client.artifactContentQueries), 2; got != want {
		t.Fatalf("ArtifactContent query count = %d, want %d", got, want)
	}
	if got := client.artifactContentQueries[1]; got.Offset != 120 || got.Limit != apiContentTailLimit {
		t.Fatalf("refresh ArtifactContent query = %+v, want offset 120 limit %d", got, apiContentTailLimit)
	}
	refreshed.contentPanelActive = true
	view := stripANSI(refreshed.View().Content)
	for _, want := range []string{"refreshed log tail", "refreshed artifact tail"} {
		if !strings.Contains(view, want) {
			t.Fatalf("refreshed API app View() missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "initial log tail") {
		t.Fatalf("refreshed API app View() kept stale log content:\n%s", view)
	}
}

func TestAPIAppModelContentKeysCycleArtifactsAndLogsThroughREST(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
			ActiveRun:      &server.RunSummaryDTO{RunNumber: 5, CurrentPhase: "implement", ArtifactCount: 2},
		}},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: "plan", RunNumber: 5, Phase: "plan", Size: apiContentTailLimit + 10, ContentAvailable: true},
			{ID: "design", RunNumber: 5, Phase: "design", Size: apiContentTailLimit + 20, ContentAvailable: true},
		}},
		artifactContentByID: map[string]server.TextContentResponse{
			"plan":   {ID: "plan", Offset: 10, Limit: apiContentTailLimit, Size: apiContentTailLimit + 10, Text: "plan tail from server"},
			"design": {ID: "design", Offset: 20, Limit: apiContentTailLimit, Size: apiContentTailLimit + 20, Text: "design tail from server"},
		},
		logContentByID: map[string]server.TextContentResponse{
			"session": {ID: "session", Offset: 0, Limit: apiContentTailLimit, Size: 25, Text: "session log from server"},
			"phase":   {ID: "phase", Offset: 0, Limit: apiContentTailLimit, Size: 20, Text: "phase log from server"},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	if cmd == nil {
		t.Fatal("Update(]) returned nil command, want artifact content fetch command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	switchedArtifact := model.(APIAppModel)

	if got, want := strings.Join(client.artifactContentIDs, ","), "plan,design"; got != want {
		t.Fatalf("ArtifactContent IDs = %q, want %q", got, want)
	}
	if got := client.artifactContentQueries[1]; got.Offset != 20 || got.Limit != apiContentTailLimit {
		t.Fatalf("second ArtifactContent query = %+v, want offset 20 limit %d", got, apiContentTailLimit)
	}
	view := stripANSI(switchedArtifact.View().Content)
	if !strings.Contains(view, "Artifact design") || !strings.Contains(view, "design tail from server") {
		t.Fatalf("API app View() missing selected design artifact in:\n%s", view)
	}
	if strings.Contains(view, "plan tail from server") {
		t.Fatalf("API app View() kept stale plan artifact after selection:\n%s", view)
	}

	model, cmd = switchedArtifact.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if cmd == nil {
		t.Fatal("Update(l) returned nil command, want log content fetch command")
	}
	msg = cmd()
	model, _ = model.(APIAppModel).Update(msg)
	switchedLog := model.(APIAppModel)

	if got, want := strings.Join(client.logContentIDs, ","), "session,phase"; got != want {
		t.Fatalf("LogContent IDs = %q, want %q", got, want)
	}
	if got := client.logContentQueries[1]; got.Offset != 0 || got.Limit != apiContentTailLimit {
		t.Fatalf("second LogContent query = %+v, want offset 0 limit %d", got, apiContentTailLimit)
	}
	view = stripANSI(switchedLog.View().Content)
	for _, want := range []string{"Log phase", "phase log from server", "Artifact design", "design tail from server"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "session log from server") {
		t.Fatalf("API app View() kept stale session log after selection:\n%s", view)
	}
}

func TestAPIAppModelContentRefreshPreservesSelectedArtifactAndLog(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
			ActiveRun:      &server.RunSummaryDTO{RunNumber: 6, CurrentPhase: "implement", ArtifactCount: 2},
		}},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: "plan", RunNumber: 6, Phase: "plan", Size: apiContentTailLimit + 10, ContentAvailable: true},
			{ID: "design", RunNumber: 6, Phase: "design", Size: apiContentTailLimit + 20, ContentAvailable: true},
		}},
		artifactContentByID: map[string]server.TextContentResponse{
			"plan":   {ID: "plan", Offset: 10, Limit: apiContentTailLimit, Size: apiContentTailLimit + 10, Text: "plan tail from server"},
			"design": {ID: "design", Offset: 20, Limit: apiContentTailLimit, Size: apiContentTailLimit + 20, Text: "design tail from server"},
		},
		logContentByID: map[string]server.TextContentResponse{
			"session": {ID: "session", Offset: 0, Limit: apiContentTailLimit, Size: apiContentTailLimit + 50, Text: "session log from server"},
			"phase":   {ID: "phase", Offset: 0, Limit: apiContentTailLimit, Size: apiContentTailLimit + 60, Text: "phase log from server"},
		},
		refreshSnapshot: server.RefreshSnapshot{
			Session: &server.SessionDetailResponse{Session: server.SessionDetailDTO{
				SessionSummaryDTO: server.SessionSummaryDTO{ID: "sess-live", FeatureID: "active", Status: "running"},
			}},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	model, cmd := app.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	if cmd == nil {
		t.Fatal("Update(]) returned nil command, want artifact content fetch command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	app = model.(APIAppModel)
	model, cmd = app.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if cmd == nil {
		t.Fatal("Update(l) returned nil command, want log content fetch command")
	}
	msg = cmd()
	model, _ = model.(APIAppModel).Update(msg)
	app = model.(APIAppModel)

	client.artifactContentByID["design"] = server.TextContentResponse{
		ID:     "design",
		Offset: 20,
		Limit:  apiContentTailLimit,
		Size:   apiContentTailLimit + 20,
		Text:   "refreshed design tail from server",
	}
	client.logContentByID["phase"] = server.TextContentResponse{
		ID:     "phase",
		Offset: 60,
		Limit:  apiContentTailLimit,
		Size:   apiContentTailLimit + 90,
		Text:   "refreshed phase log from server",
	}
	signal := server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: "log.updated"},
		Resource: server.ResourceDTO{Type: "log", ID: "phase", FeatureID: "active"},
	}
	msg = app.fetchRefreshSnapshotCmd(signal)()
	model, _ = app.Update(msg)
	refreshed := model.(APIAppModel)

	if got, want := strings.Join(client.logContentIDs, ","), "session,phase,phase"; got != want {
		t.Fatalf("LogContent IDs = %q, want %q", got, want)
	}
	if got := client.logContentQueries[2]; got.Offset != 60 || got.Limit != apiContentTailLimit {
		t.Fatalf("refresh LogContent query = %+v, want offset 60 limit %d", got, apiContentTailLimit)
	}
	if got, want := strings.Join(client.artifactContentIDs, ","), "plan,design,design"; got != want {
		t.Fatalf("ArtifactContent IDs = %q, want %q", got, want)
	}
	view := stripANSI(refreshed.View().Content)
	for _, want := range []string{"Log phase", "refreshed phase log from server", "Artifact design", "refreshed design tail from server"} {
		if !strings.Contains(view, want) {
			t.Fatalf("refreshed API app View() missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "plan tail from server") || strings.Contains(view, "session log from server") {
		t.Fatalf("refreshed API app View() reset selected content:\n%s", view)
	}
}

func TestAPIAppModelLivePreviewRefreshUsesBoundedAPIReadModel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		livePreview: server.LivePreviewResponse{
			Feature:    server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
			Activity:   "Thinking...",
			Session:    &server.SessionSummaryDTO{ID: "sess-live", FeatureID: "active", Phase: "implement", Status: "running"},
			Context:    server.ContextDTO{Percentage: 11},
			Transcript: []server.TranscriptMessageDTO{{Index: 1, Role: "assistant", Type: "text", Text: "Initial tail"}},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	initialDetailCalls := len(client.detailFeatureIDs)
	initialTranscriptCalls := countString(client.calls, "Transcript")

	signal := server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: "log.updated"},
		Resource: server.ResourceDTO{Type: "session", ID: "sess-live", FeatureID: "active"},
	}
	client.refreshSnapshot = server.RefreshSnapshot{
		LivePreview: &server.LivePreviewResponse{
			Feature:  server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
			Activity: "Using Bash...",
			Session:  &server.SessionSummaryDTO{ID: "sess-live", FeatureID: "active", Phase: "implement", Status: "running"},
			Context:  server.ContextDTO{Percentage: 57},
			Cost:     server.CostDTO{TotalUSD: 1.25},
			Transcript: []server.TranscriptMessageDTO{
				{Index: 2, Role: "assistant", Type: "text", Text: "Patched through REST snapshot"},
			},
		},
	}
	msg := app.fetchRefreshSnapshotCmd(signal)()
	model, _ := app.Update(msg)
	refreshed := model.(APIAppModel)

	if got := len(client.detailFeatureIDs); got != initialDetailCalls {
		t.Fatalf("FeatureDetail calls after live-preview refresh = %d, want unchanged %d", got, initialDetailCalls)
	}
	if got := countString(client.calls, "Transcript"); got != initialTranscriptCalls {
		t.Fatalf("Transcript calls after live-preview refresh = %d, want unchanged %d", got, initialTranscriptCalls)
	}
	view := stripANSI(refreshed.View().Content)
	for _, want := range []string{"Live Preview", "Using Bash...", "57%", "$1.25", "Patched through REST snapshot"} {
		if !strings.Contains(view, want) {
			t.Fatalf("refreshed API app View() missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Initial tail") {
		t.Fatalf("refreshed API app View() kept stale live-preview tail:\n%s", view)
	}
}

func TestAPIAppModelAppliesResourceTargetedRefreshSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CreatedAt: time.Now()},
		}},
		runtime:     server.RuntimeConfigResponse{},
		catalog:     server.ModelCatalogResponse{},
		prompts:     server.PromptSnapshotResponse{},
		permissions: server.PermissionSnapshotResponse{},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	app.ApplyRefreshSnapshot(server.RefreshSnapshot{
		Features: &server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Published", CurrentPhase: "publish", CreatedAt: time.Now()},
			{ID: "new", Name: "New API feature", Slug: "new-api-feature", Status: "Created", CurrentPhase: "research", CreatedAt: time.Now().Add(time.Second)},
		}},
		Prompts: &server.PromptSnapshotResponse{NeedUserInputs: []server.NeedInputGateDTO{
			{FeatureID: "new", Open: true, Scope: "feature"},
		}},
	})

	if got := app.SelectedFeatureID(); got != "active" {
		t.Fatalf("SelectedFeatureID() after refresh = %q, want active", got)
	}
	snapshot := app.Snapshot()
	var newFeature APIFeaturePresentation
	for _, f := range snapshot.Features {
		if f.ID == "new" {
			newFeature = f
			break
		}
	}
	if newFeature.ID == "" {
		t.Fatalf("refresh snapshot did not add new feature: %+v", snapshot.Features)
	}
	if newFeature.AttentionCount != 1 {
		t.Fatalf("new feature AttentionCount = %d, want 1", newFeature.AttentionCount)
	}
}

func TestAPIAppModelReconnectSnapshotRecoveryPreservesSelection(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
			{ID: "queued", Name: "Queued work", Slug: "queued-work", Status: "Created", CurrentPhase: "research", CreatedAt: time.Now().Add(-time.Hour)},
		}},
		runtime:     server.RuntimeConfigResponse{Providers: []string{"codex"}},
		catalog:     server.ModelCatalogResponse{},
		prompts:     server.PromptSnapshotResponse{},
		permissions: server.PermissionSnapshotResponse{},
		refreshSnapshot: server.RefreshSnapshot{
			Feature: &server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
				FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Published", CurrentPhase: "publish", CreatedAt: time.Now()},
			}},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	if got := app.SelectedFeatureID(); got != "active" {
		t.Fatalf("initial SelectedFeatureID() = %q, want active", got)
	}
	initialSessionCalls := countString(client.calls, "Sessions")
	initialTranscriptCalls := countString(client.calls, "Transcript")

	signal := server.RefreshSignal{
		Resource:         server.ResourceDTO{Type: "feature", ID: "active", FeatureID: "active"},
		SnapshotRequired: true,
	}
	msg := app.fetchRefreshSnapshotCmd(signal)()
	model, _ := app.Update(msg)
	recovered := model.(APIAppModel)

	if got := recovered.SelectedFeatureID(); got != "active" {
		t.Fatalf("SelectedFeatureID() after reconnect snapshot = %q, want active", got)
	}
	if len(client.refreshSignals) != 1 || client.refreshSignals[0].Resource.ID != "active" {
		t.Fatalf("refresh signals = %+v, want targeted active feature refresh", client.refreshSignals)
	}
	if got := countString(client.calls, "Sessions"); got != initialSessionCalls {
		t.Fatalf("Sessions calls after feature refresh = %d, want unchanged %d", got, initialSessionCalls)
	}
	if got := countString(client.calls, "Transcript"); got != initialTranscriptCalls {
		t.Fatalf("Transcript calls after feature refresh = %d, want unchanged %d", got, initialTranscriptCalls)
	}
}

func TestAPIAppModelRecoveryRefreshRehydratesPanel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		runtime:     server.RuntimeConfigResponse{Providers: []string{"codex"}},
		catalog:     server.ModelCatalogResponse{},
		prompts:     server.PromptSnapshotResponse{},
		permissions: server.PermissionSnapshotResponse{},
		recovery:    server.RecoverySnapshotResponse{},
		refreshSnapshot: server.RefreshSnapshot{
			Recovery: &server.RecoverySnapshotResponse{
				SnapshotID: "recovery-snapshot-2",
				Items: []server.RecoveryItemDTO{{
					Key:            "feat-1:api",
					FeatureID:      "active",
					FeatureName:    "Client cutover",
					RepoName:       "api",
					Phase:          "implement",
					Iteration:      8,
					PID:            4321,
					ProcessAlive:   true,
					DefaultAction:  "kill",
					AllowedActions: []string{"kill", "skip"},
				}},
			},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	if strings.Contains(stripANSI(app.View().Content), "Session Recovery") {
		t.Fatalf("initial API app View() unexpectedly showed recovery panel:\n%s", stripANSI(app.View().Content))
	}

	signal := server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: "recovery.updated"},
		Resource: server.ResourceDTO{Type: "runtime"},
	}
	msg := app.fetchRefreshSnapshotCmd(signal)()
	model, _ := app.Update(msg)
	refreshed := model.(APIAppModel)

	view := stripANSI(refreshed.View().Content)
	for _, want := range []string{"Session Recovery", "Client cutover", "PID 4321", "[K]ill"} {
		if !strings.Contains(view, want) {
			t.Fatalf("refreshed API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelStartSelectedFeatureUsesRESTMutation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "queued", Name: "Queued work", Slug: "queued-work", Status: "Created", CurrentPhase: "research", CreatedAt: time.Now()},
		}},
		startAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter) returned command, want focus-only behavior")
	}
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want start mutation command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	started := model.(APIAppModel)

	if got := strings.Join(client.startFeatureIDs, ","); got != "queued" {
		t.Fatalf("StartFeature calls = %q, want queued", got)
	}
	view := stripANSI(started.View().Content)
	for _, want := range []string{"Completed Start", "queued"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelResumeSelectedFeatureUsesRESTMutation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "paused", Name: "Paused work", Slug: "paused-work", Status: "Interrupted", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "paused", Name: "Paused work", Slug: "paused-work", Status: "Interrupted", CurrentPhase: "implement"},
			Actions: []server.ActionDTO{
				{ID: "resume", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}},
			},
		}},
		resumeAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter) returned command, want focus-only behavior")
	}
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want resume mutation command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	resumed := model.(APIAppModel)

	if got := strings.Join(client.resumeFeatureIDs, ","); got != "paused" {
		t.Fatalf("ResumeFeature calls = %q, want paused", got)
	}
	if len(client.startFeatureIDs) != 0 {
		t.Fatalf("StartFeature calls = %v, want none for resume action", client.startFeatureIDs)
	}
	view := stripANSI(resumed.View().Content)
	for _, want := range []string{"Completed Resume", "paused"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelContextualRetryUsesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "failed", Name: "Failed work", Slug: "failed-work", Status: "Failed", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		retryAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want retry mutation command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	retried := model.(APIAppModel)

	if got := strings.Join(client.retryFeatureIDs, ","); got != "failed" {
		t.Fatalf("RetryFeature calls = %q, want failed", got)
	}
	if view := stripANSI(retried.View().Content); !strings.Contains(view, "Completed Retry") {
		t.Fatalf("API app View() missing retry completed status in:\n%s", view)
	}
}

func TestAPIAppModelDetailContextualRetryUsesAWithoutResumeAll(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "failed", Name: "Failed work", Slug: "failed-work", Status: "Failed", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		retryAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) in detail returned nil command, want retry mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	retried := model.(APIAppModel)

	if got := strings.Join(client.retryFeatureIDs, ","); got != "failed" {
		t.Fatalf("RetryFeature calls = %q, want failed", got)
	}
	if retried.resumeAllConfirmActive {
		t.Fatal("contextual retry in detail opened resume-all confirmation")
	}
	if view := stripANSI(retried.View().Content); !strings.Contains(view, "Completed Retry") {
		t.Fatalf("API app View() missing retry completed status in:\n%s", view)
	}
}

func TestAPIAppModelResumeAllUsesRESTMutations(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "paused", Name: "Paused work", Slug: "paused-work", Status: "Interrupted", CurrentPhase: "implement", CreatedAt: time.Now()},
			{ID: "failed", Name: "Failed work", Slug: "failed-work", Status: "Failed", CurrentPhase: "implement", CreatedAt: time.Now().Add(-time.Minute)},
		}},
		resumeAccepted: apiTestActionResponse{},
		retryAccepted:  apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	focused := model.(APIAppModel)
	if focused.focusPanel != 1 {
		t.Fatalf("focusPanel after right = %d, want detail focus", focused.focusPanel)
	}
	model, cmd := focused.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	if cmd != nil {
		t.Fatal("Update(Shift+R) returned command before resume-all confirmation")
	}
	confirming := model.(APIAppModel)
	if !confirming.resumeAllConfirmActive {
		t.Fatal("Update(Shift+R) did not open resume-all confirmation")
	}
	if view := stripANSI(confirming.View().Content); !strings.Contains(view, "Resume All") {
		t.Fatalf("API app View() missing resume-all confirmation:\n%s", view)
	}

	model, cmd = confirming.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("Update(y) returned nil command, want resume-all mutation command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	resumed := model.(APIAppModel)

	if got := strings.Join(client.resumeFeatureIDs, ","); got != "paused" {
		t.Fatalf("ResumeFeature calls = %q, want paused", got)
	}
	if got := strings.Join(client.retryFeatureIDs, ","); got != "failed" {
		t.Fatalf("RetryFeature calls = %q, want failed", got)
	}
	if view := stripANSI(resumed.View().Content); !strings.Contains(view, "Resumed 2 feature(s)") {
		t.Fatalf("API app View() missing resume-all completed status in:\n%s", view)
	}
}

func TestAPIAppModelDashboardShortcutParity(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		toggleInputAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter) returned command, want focus-only behavior")
	}
	focused := model.(APIAppModel)
	if focused.focusPanel != 1 {
		t.Fatalf("focusPanel after enter = %d, want detail focus", focused.focusPanel)
	}

	model, cmd = focused.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	if cmd != nil {
		t.Fatal("Update(o) returned command, want local overview toggle")
	}
	overview := model.(APIAppModel)
	if overview.rightPanelMode != dashboardRightPanelOverview {
		t.Fatalf("rightPanelMode after o = %v, want overview", overview.rightPanelMode)
	}

	model, cmd = overview.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if cmd != nil {
		t.Fatal("Update(/) returned command without REST chat support")
	}
	chat := model.(APIAppModel)
	if !strings.Contains(chat.statusMessage, "REST chat endpoint") {
		t.Fatalf("status after / = %q, want chat unavailable status", chat.statusMessage)
	}

	model, cmd = chat.Update(tea.KeyPressMsg{Code: 'N', Text: "N"})
	if cmd == nil {
		t.Fatal("Update(Shift+N) returned nil command, want input-alert mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	toggled := model.(APIAppModel)
	if got := strings.Join(client.toggleInputFeatureIDs, ","); got != "active" {
		t.Fatalf("ToggleInputNotifications calls = %q, want active", got)
	}
	if view := stripANSI(toggled.View().Content); !strings.Contains(view, "Completed Input Alerts") {
		t.Fatalf("API app View() missing input-alert completed status in:\n%s", view)
	}

	model, cmd = toggled.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	if cmd != nil {
		t.Fatal("Update(?) returned command, want local help overlay")
	}
	helping := model.(APIAppModel)
	if !helping.helpOverlayActive {
		t.Fatal("Update(?) did not open help overlay")
	}
}

func TestAPIAppModelCreateFeatureUsesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Defaults:  config.ModelConfig{Research: "gpt-5.4", Planning: "gpt-5.4", Implementation: "gpt-5.4", Review: "gpt-5.4"},
			Providers: []string{"codex"},
			Repos: []server.ConfigRepoDTO{
				{Name: "agentic-orchestrator"},
			},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{"codex"},
			ProviderModels: map[string][]server.ModelDTO{
				"codex": {{ID: "gpt-5.4"}},
			},
			PhaseDefaults: config.ModelConfig{Research: "gpt-5.4", Planning: "gpt-5.4", Implementation: "gpt-5.4", Review: "gpt-5.4"},
		},
		createAccepted: apiTestActionResponse{FeatureID: "feat-created"},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd != nil {
		t.Fatal("Update(n) returned command before create submit")
	}
	creating := model.(APIAppModel)
	if !creating.ShowingCreateFeaturePrompt() {
		t.Fatal("Update(n) did not show create feature prompt")
	}
	view := stripANSI(creating.View().Content)
	for _, want := range []string{"New Feature", "Name", "Description ("} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"[tab] Field", "Toggle repo"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("API app View() still contains reduced create prompt text %q in:\n%s", unwanted, view)
		}
	}

	model, _ = creating.Update(tea.KeyPressMsg{Text: "API cutover regression"})
	creating = model.(APIAppModel)
	model, _ = creating.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	creating = model.(APIAppModel)
	model, _ = creating.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	creating = model.(APIAppModel)
	if creating.wizard == nil || creating.wizard.step != wizardStepWhere {
		t.Fatalf("wizard step after name/description = %+v, want where", creating.wizard)
	}
	view = stripANSI(creating.View().Content)
	for _, want := range []string{"Pick one or more repos", "agentic-orchestrator", "Browse for more"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app wizard repo step missing %q in:\n%s", want, view)
		}
	}
	model, _ = creating.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	creating = model.(APIAppModel)
	model, _ = creating.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	creating = model.(APIAppModel)
	model, _ = creating.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	creating = model.(APIAppModel)
	if creating.wizard == nil || creating.wizard.step != wizardStepReview {
		t.Fatalf("wizard step after pipeline confirm = %+v, want review", creating.wizard)
	}
	model, cmd = creating.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if cmd == nil {
		t.Fatal("Update(Go) returned nil command, want create mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	created := model.(APIAppModel)

	if got := client.createRequests; len(got) != 1 {
		t.Fatalf("CreateFeature requests = %+v, want one request", got)
	} else {
		if got[0].Name != "API cutover regression" {
			t.Fatalf("CreateFeature name = %q, want API cutover regression", got[0].Name)
		}
		if len(got[0].Repos) != 1 || got[0].Repos[0] != "agentic-orchestrator" {
			t.Fatalf("CreateFeature repos = %+v, want agentic-orchestrator selected", got[0].Repos)
		}
		if got[0].Models.Implementation != "gpt-5.4" {
			t.Fatalf("CreateFeature implementation model = %q, want gpt-5.4", got[0].Models.Implementation)
		}
	}
	if got := strings.Join(client.startFeatureIDs, ","); got != "feat-created" {
		t.Fatalf("StartFeature calls = %q, want feat-created auto-start after create", got)
	}
	if got := strings.Join(client.calls[len(client.calls)-2:], ","); got != "CreateFeature,StartFeature" {
		t.Fatalf("last API calls = %q, want CreateFeature,StartFeature", got)
	}
	if created.ShowingCreateFeaturePrompt() {
		t.Fatal("create prompt remained open after accepted create")
	}
	if !strings.Contains(created.statusMessage, "Completed Create") {
		t.Fatalf("statusMessage = %q, want create completed status", created.statusMessage)
	}
}

func TestAPIAppModelWorkspaceManagerUsesRuntimeConfigMutation(t *testing.T) {
	t.Parallel()

	rootA := t.TempDir()
	rootB := t.TempDir()
	makeGitRepoDir(t, rootA, "api")
	makeGitRepoDir(t, rootB, "web")

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			WorkspaceRoots: []string{rootA},
			Repos:          testRuntimeConfigRepos(nil, []string{rootA}),
		},
		updateRuntimeConfigAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	if cmd != nil {
		t.Fatal("Update(W) returned command before workspace mutation")
	}
	managing := model.(APIAppModel)
	if managing.workspaceManager == nil {
		t.Fatal("Update(W) did not open workspace manager")
	}
	if managing.wizard != nil {
		t.Fatal("Update(W) opened create wizard, want workspace manager")
	}
	view := stripANSI(managing.View().Content)
	if !strings.Contains(view, "Workspace Manager") {
		t.Fatalf("workspace manager view missing modal title:\n%s", view)
	}
	if len(managing.workspaceManager.roots) != 1 || managing.workspaceManager.roots[0].Path != rootA {
		t.Fatalf("workspace manager roots = %+v, want %q", managing.workspaceManager.roots, rootA)
	}

	managing.workspaceManager.addedRoot = rootB
	model, cmd = managing.Update(struct{}{})
	if cmd == nil {
		t.Fatal("workspace root add returned nil command, want runtime config mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	updated := model.(APIAppModel)

	if got := client.updateRuntimeConfigRequests; len(got) != 1 || got[0].WorkspaceRoots == nil || !containsRootExpanded(*got[0].WorkspaceRoots, rootB) {
		t.Fatalf("UpdateRuntimeConfig requests = %+v, want workspace roots including %q", got, rootB)
	}
	if !containsRootExpanded(updated.runtimeConfig.WorkspaceRoots, rootB) {
		t.Fatalf("runtime workspace roots = %+v, want %q", updated.runtimeConfig.WorkspaceRoots, rootB)
	}
	_, repoPaths, _ := apiRuntimeRepoState(updated.runtimeConfig)
	if repoPaths["web"] != filepath.Join(rootB, "web") {
		t.Fatalf("runtime repos = %+v, want discovered web repo under rootB", updated.runtimeConfig.Repos)
	}
}

func TestAPIAppModelWizardBrowseRootPersistsAndRefreshesRepos(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	makeGitRepoDir(t, root, "agentic-orchestrator")

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Defaults:  config.ModelConfig{Research: "gpt-5.4", Planning: "gpt-5.4", Implementation: "gpt-5.4", Review: "gpt-5.4"},
			Providers: []string{"codex"},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{"codex"},
			ProviderModels: map[string][]server.ModelDTO{
				"codex": {{ID: "gpt-5.4"}},
			},
			PhaseDefaults: config.ModelConfig{Research: "gpt-5.4", Planning: "gpt-5.4", Implementation: "gpt-5.4", Review: "gpt-5.4"},
		},
		updateRuntimeConfigAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	creating := model.(APIAppModel)
	creating.wizard.browseRoot = root
	model, cmd := creating.Update(struct{}{})
	if cmd == nil {
		t.Fatal("wizard browse root returned nil command, want runtime config mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	updated := model.(APIAppModel)

	if got := client.updateRuntimeConfigRequests; len(got) != 1 || got[0].WorkspaceRoots == nil || !containsRootExpanded(*got[0].WorkspaceRoots, root) {
		t.Fatalf("UpdateRuntimeConfig requests = %+v, want workspace roots including %q", got, root)
	}
	if updated.wizard == nil {
		t.Fatal("wizard closed after browse-root refresh")
	}
	if !containsRootExpanded(updated.wizard.workspaceRoots, root) {
		t.Fatalf("wizard workspace roots = %+v, want %q", updated.wizard.workspaceRoots, root)
	}
	if _, ok := updated.wizard.repoPaths["agentic-orchestrator"]; !ok {
		t.Fatalf("wizard repo paths = %+v, want discovered agentic-orchestrator", updated.wizard.repoPaths)
	}
}

func TestAPIAppModelWizardCreateRepoPersistsRootRescansAndAutoSelects(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	createdPath := filepath.Join(root, "new-service")

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Defaults:       config.ModelConfig{Research: "gpt-5.4", Planning: "gpt-5.4", Implementation: "gpt-5.4", Review: "gpt-5.4"},
			Providers:      []string{"codex"},
			WorkspaceRoots: []string{root},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{"codex"},
			ProviderModels: map[string][]server.ModelDTO{
				"codex": {{ID: "gpt-5.4"}},
			},
			PhaseDefaults: config.ModelConfig{Research: "gpt-5.4", Planning: "gpt-5.4", Implementation: "gpt-5.4", Review: "gpt-5.4"},
		},
		updateRuntimeConfigAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	creating := model.(APIAppModel)
	creating.wizard.step = wizardStepWhere
	creating.wizard.createRepoPath = createdPath
	makeGitRepoDir(t, root, "new-service")

	model, cmd := creating.Update(struct{}{})
	if cmd == nil {
		t.Fatal("wizard create repo returned nil command, want runtime config mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	updated := model.(APIAppModel)

	if got := client.updateRuntimeConfigRequests; len(got) != 1 || got[0].WorkspaceRoots == nil || len(*got[0].WorkspaceRoots) != 1 || (*got[0].WorkspaceRoots)[0] != root {
		t.Fatalf("UpdateRuntimeConfig requests = %+v, want unchanged root %q supplied for rescan", got, root)
	}
	if updated.wizard == nil {
		t.Fatal("wizard closed after create-repo refresh")
	}
	if updated.wizard.step != wizardStepPipeline {
		t.Fatalf("wizard step = %v, want pipeline after auto-select advance", updated.wizard.step)
	}
	if !updated.wizard.selectedRepos["new-service"] {
		t.Fatalf("wizard selected repos = %+v, want new-service selected", updated.wizard.selectedRepos)
	}
	if updated.wizard.repoPaths["new-service"] != createdPath {
		t.Fatalf("wizard repo path = %q, want %q", updated.wizard.repoPaths["new-service"], createdPath)
	}
}

func makeGitRepoDir(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
		t.Fatalf("create git repo fixture: %v", err)
	}
}

func TestAPIAppModelFeatureActionsConfirmBeforeRESTMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		key        tea.KeyPressMsg
		actionID   string
		wantKind   string
		accepted   apiTestActionResponse
		cycle      *server.CycleDTO
		disabled   bool
		assertCall func(t *testing.T, client *fakeTUIAPIClient)
	}{
		{
			name:     "merge",
			key:      tea.KeyPressMsg{Code: 'M', Text: "M"},
			actionID: "merge",
			wantKind: "feature.merge",
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.mergeFeatureIDs, ","); got != "active" {
					t.Fatalf("MergeFeature calls = %q, want active", got)
				}
			},
		},
		{
			name:     "mark done",
			key:      tea.KeyPressMsg{Code: 'D', Text: "D"},
			actionID: "mark-done",
			wantKind: "feature.mark-done",
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.markDoneFeatureIDs, ","); got != "active" {
					t.Fatalf("MarkDone calls = %q, want active", got)
				}
			},
		},
		{
			name:     "rebase",
			key:      tea.KeyPressMsg{Code: 'b', Text: "b"},
			actionID: "rebase",
			wantKind: "feature.rebase",
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.startRebaseFeatureIDs, ","); got != "active" {
					t.Fatalf("StartRebase calls = %q, want active", got)
				}
				if got := client.startRebaseRequests; len(got) != 1 || got[0].Repo != "agentic-orchestrator" || got[0].RebaseTarget != "" || len(got[0].ConflictFiles) != 0 {
					t.Fatalf("StartRebase requests = %+v, want repo agentic-orchestrator without target or conflict files", got)
				}
			},
		},
		{
			name:     "cleanup worktrees",
			key:      tea.KeyPressMsg{Code: 'c', Text: "c"},
			actionID: "cleanup",
			wantKind: "feature.cleanup",
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.cleanupFeatureIDs, ","); got != "active" {
					t.Fatalf("CleanupFeature calls = %q, want active", got)
				}
				if got := client.cleanupRequests; len(got) != 1 || got[0].Target != "worktrees" || got[0].Repo != "" {
					t.Fatalf("CleanupFeature requests = %+v, want target worktrees without repo", got)
				}
			},
		},
		{
			name:     "tweak start",
			key:      tea.KeyPressMsg{Code: 't', Text: "t"},
			actionID: "tweak",
			wantKind: "feature.tweak.start",
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.startTweakFeatureIDs, ","); got != "active" {
					t.Fatalf("StartTweak calls = %q, want active", got)
				}
				if got := client.startTweakRequests; len(got) != 1 {
					t.Fatalf("StartTweak requests = %+v, want one empty request", got)
				}
			},
		},
		{
			name:     "rewind",
			key:      tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl},
			actionID: "rewind",
			wantKind: "feature.rewind",
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.rewindFeatureIDs, ","); got != "active" {
					t.Fatalf("RewindFeature calls = %q, want active", got)
				}
				if got := client.rewindRequests; len(got) != 1 || got[0].TargetPhase != "implement" || got[0].RoadmapPhase != 0 {
					t.Fatalf("RewindFeature requests = %+v, want target phase implement without roadmap phase", got)
				}
			},
		},
		{
			name:     "restart",
			key:      tea.KeyPressMsg{Code: 'r', Text: "r"},
			actionID: "restart",
			wantKind: "feature.restart",
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.restartFeatureIDs, ","); got != "active" {
					t.Fatalf("RestartFeature calls = %q, want active", got)
				}
			},
		},
		{
			name:     "stop",
			key:      tea.KeyPressMsg{Code: 's', Text: "s"},
			actionID: "pause-stop",
			wantKind: "feature.stop",
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.stopFeatureIDs, ","); got != "active" {
					t.Fatalf("StopFeature calls = %q, want active", got)
				}
			},
		},
		{
			name:     "delete",
			key:      tea.KeyPressMsg{Code: 'd', Text: "d"},
			actionID: "delete",
			wantKind: "feature.delete",
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.deleteFeatureIDs, ","); got != "active" {
					t.Fatalf("DeleteFeature calls = %q, want active", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeTUIAPIClient{
				features: server.FeatureListResponse{Features: []server.FeatureSummary{
					{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", Repos: []string{"agentic-orchestrator"}, CreatedAt: time.Now()},
				}},
				detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
					FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement"},
					Cycle:          tt.cycle,
					RepoStatus: []server.RepoStatusDTO{
						{Name: "agentic-orchestrator", Publishable: true},
					},
					Actions: []server.ActionDTO{
						{ID: tt.actionID, Enabled: !tt.disabled, Scope: server.ActionScopeDTO{Type: "feature"}},
					},
				}},
				restartAccepted:     tt.accepted,
				stopAccepted:        tt.accepted,
				deleteAccepted:      tt.accepted,
				mergeAccepted:       tt.accepted,
				retryAccepted:       tt.accepted,
				markDoneAccepted:    tt.accepted,
				cleanupAccepted:     tt.accepted,
				startTweakAccepted:  tt.accepted,
				finishTweakAccepted: tt.accepted,
				rewindAccepted:      tt.accepted,
				startRebaseAccepted: tt.accepted,
			}
			app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
			if err != nil {
				t.Fatalf("NewAPIAppModel() error = %v", err)
			}

			model, cmd := app.Update(tt.key)
			if cmd != nil {
				t.Fatalf("Update(%s) returned command before confirmation", tt.name)
			}
			confirming := model.(APIAppModel)
			wantTitle := "Confirm " + apiMutationKindLabel(tt.wantKind)
			if tt.wantKind == "feature.rewind" {
				wantTitle = "Rewind Confirmation"
			}
			if view := stripANSI(confirming.View().Content); !strings.Contains(view, wantTitle) {
				t.Fatalf("View() missing %q confirmation in:\n%s", wantTitle, view)
			}
			if len(client.mergeFeatureIDs)+len(client.retryFeatureIDs)+len(client.markDoneFeatureIDs)+len(client.cleanupFeatureIDs)+len(client.startTweakFeatureIDs)+len(client.finishTweakFeatureIDs)+len(client.rewindFeatureIDs)+len(client.startRebaseFeatureIDs)+len(client.restartFeatureIDs)+len(client.stopFeatureIDs)+len(client.deleteFeatureIDs) != 0 {
				t.Fatalf("API action was sent before confirmation: merge=%v retry=%v markDone=%v cleanup=%v tweak=%v finishTweak=%v restart=%v stop=%v delete=%v rewind=%v rebase=%v", client.mergeFeatureIDs, client.retryFeatureIDs, client.markDoneFeatureIDs, client.cleanupFeatureIDs, client.startTweakFeatureIDs, client.finishTweakFeatureIDs, client.restartFeatureIDs, client.stopFeatureIDs, client.deleteFeatureIDs, client.rewindFeatureIDs, client.startRebaseFeatureIDs)
			}

			model, cmd = confirming.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
			if cmd == nil {
				t.Fatalf("Update(y) for %s returned nil command, want REST mutation command", tt.name)
			}
			msg := cmd()
			model, _ = model.(APIAppModel).Update(msg)
			accepted := model.(APIAppModel)

			tt.assertCall(t, client)
			view := stripANSI(accepted.View().Content)
			wantStatus := "Completed " + apiMutationKindLabel(tt.wantKind)
			if !strings.Contains(view, wantStatus) {
				t.Fatalf("API app View() missing %q in:\n%s", wantStatus, view)
			}
			if tt.wantKind == "feature.delete" {
				if strings.Contains(view, "active") {
					t.Fatalf("API app View() still shows deleted feature:\n%s", view)
				}
			} else if !strings.Contains(view, "active") {
				t.Fatalf("API app View() missing %q in:\n%s", "active", view)
			}
		})
	}
}

func TestAPIAppModelFinishTweakShowsFinalReviewDecisionModal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		key            tea.KeyPressMsg
		wantDecision   string
		wantHadChanges bool
	}{
		{
			name:           "review",
			key:            tea.KeyPressMsg{Code: 'y', Text: "y"},
			wantDecision:   "final-review",
			wantHadChanges: true,
		},
		{
			name:           "skip_review",
			key:            tea.KeyPressMsg{Code: 'n', Text: "n"},
			wantDecision:   "skip-review",
			wantHadChanges: true,
		},
		{
			name:         "restore",
			key:          tea.KeyPressMsg{Code: tea.KeyEscape},
			wantDecision: "restore-from-review",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeTUIAPIClient{
				features: server.FeatureListResponse{Features: []server.FeatureSummary{
					{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", Repos: []string{"agentic-orchestrator"}, CreatedAt: time.Now()},
				}},
				detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
					FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement"},
					Cycle:          &server.CycleDTO{Type: "tweak", Status: "running"},
					RepoStatus: []server.RepoStatusDTO{
						{Name: "agentic-orchestrator", CycleType: "tweak", CycleStatus: "running"},
					},
					Actions: []server.ActionDTO{
						{ID: "tweak", Enabled: false, Scope: server.ActionScopeDTO{Type: "feature"}},
					},
				}},
				finishTweakAccepted: apiTestActionResponse{Result: "finished"},
			}
			app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
			if err != nil {
				t.Fatalf("NewAPIAppModel() error = %v", err)
			}

			model, cmd := app.Update(tea.KeyPressMsg{Code: 'T', Text: "T"})
			if cmd != nil {
				t.Fatal("Update(T) returned command before final review decision")
			}
			deciding := model.(APIAppModel)
			view := stripANSI(deciding.View().Content)
			for _, want := range []string{"Final Review", "Changes have been committed. Run a Final Review?", "[y] Yes", "[n] No", "Esc to cancel"} {
				if !strings.Contains(view, want) {
					t.Fatalf("View() missing %q in:\n%s", want, view)
				}
			}
			if strings.Contains(view, "Confirm feature.tweak.finish") {
				t.Fatalf("View() showed generic tweak finish confirmation, want final review decision modal:\n%s", view)
			}
			if len(client.finishTweakFeatureIDs) != 0 {
				t.Fatalf("FinishTweak calls before modal decision = %v, want none", client.finishTweakFeatureIDs)
			}

			model, cmd = deciding.Update(tt.key)
			if cmd == nil {
				t.Fatalf("Update(%s) returned nil command, want REST mutation command", tt.name)
			}
			msg := cmd()
			model, _ = model.(APIAppModel).Update(msg)
			accepted := model.(APIAppModel)

			if got := strings.Join(client.finishTweakFeatureIDs, ","); got != "active" {
				t.Fatalf("FinishTweak calls = %q, want active", got)
			}
			if got := client.finishTweakRequests; len(got) != 1 || got[0].Decision != tt.wantDecision || got[0].HadChanges != tt.wantHadChanges {
				t.Fatalf("FinishTweak requests = %+v, want decision %q had_changes %v", got, tt.wantDecision, tt.wantHadChanges)
			}
			if !strings.Contains(accepted.statusMessage, "Completed Finish Tweak") {
				t.Fatalf("statusMessage = %q, want tweak finish completed status", accepted.statusMessage)
			}
		})
	}
}

func TestAPIAppModelFeatureConfigEditorLoadsFromRESTAndSavesMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Published", CurrentPhase: "publish", CreatedAt: time.Now()},
		}},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{"codex"},
			ProviderModels: map[string][]server.ModelDTO{
				"codex": {
					{ID: "codex:gpt-5.4"},
					{ID: "codex:gpt-5.5"},
				},
			},
			PhaseDefaults: config.ModelConfig{
				Research: "codex:gpt-5.4",
			},
			PhaseProviderModels: map[string]map[string][]string{
				"Research": {"codex": {"codex:gpt-5.4", "codex:gpt-5.5"}},
			},
		},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Published", CurrentPhase: "publish"},
			Pipeline:       "large",
		}},
		featureConfig: server.FeatureConfigResponse{
			FeatureID: "active",
			Current: server.FeatureConfigDTO{
				Models:      config.ModelConfig{Research: "codex:gpt-5.4", Planning: "codex:gpt-5.4", Implementation: "codex:gpt-5.4", Review: "codex:gpt-5.4", KBBuild: "codex:gpt-5.4"},
				Inquireness: "targeted",
				Checkpoints: server.CheckpointsDTO{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
				Pipeline:    "large",
			},
			Defaults: server.FeatureConfigDTO{
				Models: config.ModelConfig{Research: "codex:gpt-5.4"},
			},
		},
		updateFeatureConfigAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if cmd == nil {
		t.Fatal("Update(e) returned nil command, want feature config fetch command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	editing := model.(APIAppModel)

	if got := strings.Join(client.featureConfigIDs, ","); got != "active" {
		t.Fatalf("FeatureConfig calls = %q, want active", got)
	}
	view := stripANSI(editing.View().Content)
	for _, want := range []string{"Edit Config", "Client cutover", "Models", "Behavior", "Gates", "Research", "codex / gpt-5.4"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"Feature config", "[up/down] Field", "[tab] Change model"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("API app View() still contains reduced feature-config text %q in:\n%s", unwanted, view)
		}
	}

	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	edited := model.(APIAppModel)
	model, cmd = edited.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want feature config update command")
	}
	msg = cmd()
	model, _ = model.(APIAppModel).Update(msg)
	saved := model.(APIAppModel)

	if got := strings.Join(client.updateFeatureConfigIDs, ","); got != "active" {
		t.Fatalf("UpdateFeatureConfig calls = %q, want active", got)
	}
	if got := client.updateFeatureConfigRequests; len(got) != 1 || got[0].Models.Research != "codex:gpt-5.5" || got[0].Pipeline != "large" || got[0].Inquireness != "targeted" || !got[0].Checkpoints.RoadmapReview || !got[0].Checkpoints.PhasePlanReview || !got[0].Checkpoints.ManualPublish {
		t.Fatalf("UpdateFeatureConfig requests = %+v, want edited research model and preserved config axes", got)
	}
	view = stripANSI(saved.View().Content)
	for _, want := range []string{"Completed Feature Config", "active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelFeatureConfigEditorRequiresQuiescentFeature(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if cmd != nil {
		t.Fatal("Update(e) returned command for running feature, want quiescence gate")
	}
	blocked := model.(APIAppModel)
	if got := client.featureConfigIDs; len(got) != 0 {
		t.Fatalf("FeatureConfig calls = %v, want none for running feature", got)
	}
	if !strings.Contains(blocked.statusMessage, "idle") {
		t.Fatalf("statusMessage = %q, want idle gate", blocked.statusMessage)
	}
}

func TestAPIAppModelRuntimeConfigEditorSavesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Defaults: config.ModelConfig{
				Research:       "codex:gpt-5.4",
				Planning:       "codex:gpt-5.4",
				Implementation: "codex:gpt-5.4",
				Review:         "codex:gpt-5.4",
				KBBuild:        "codex:gpt-5.4",
			},
			Providers: []string{"codex"},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{"codex"},
			ProviderModels: map[string][]server.ModelDTO{
				"codex": {
					{ID: "codex:gpt-5.4"},
					{ID: "codex:gpt-5.5"},
				},
			},
			PhaseDefaults: config.ModelConfig{
				Research: "codex:gpt-5.4",
			},
			PhaseProviderModels: map[string]map[string][]string{
				"Research": {"codex": {"codex:gpt-5.4", "codex:gpt-5.5"}},
			},
		},
		updateRuntimeConfigAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'E', Text: "E"})
	if cmd != nil {
		t.Fatal("Update(E) returned command before runtime config save")
	}
	editing := model.(APIAppModel)
	view := stripANSI(editing.View().Content)
	for _, want := range []string{"Runtime config", "Default models", "Research", "codex:gpt-5.4"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	if len(client.featureConfigIDs) != 0 {
		t.Fatalf("FeatureConfig calls = %v, want none for runtime config editor", client.featureConfigIDs)
	}

	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	edited := model.(APIAppModel)
	model, cmd = edited.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want runtime config update command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	saved := model.(APIAppModel)

	if got := client.updateRuntimeConfigRequests; len(got) != 1 || got[0].Defaults.Models.Research != "codex:gpt-5.5" {
		t.Fatalf("UpdateRuntimeConfig requests = %+v, want edited research default model", got)
	}
	view = stripANSI(saved.View().Content)
	for _, want := range []string{"Completed Runtime Config"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelNeedUserInputDecisionUsesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "blocked", Name: "Blocked work", Slug: "blocked-work", Status: "NeedUserInput", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "blocked", Name: "Blocked work", Slug: "blocked-work", Status: "NeedUserInput", CurrentPhase: "implement"},
			NeedUserInput:  &server.NeedInputGateDTO{FeatureID: "blocked", Open: true, Scope: "feature", Iteration: 3},
		}},
		needUserInputAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	if cmd != nil {
		t.Fatal("Update(i) returned command before need-user-input decision")
	}
	prompting := model.(APIAppModel)
	if !prompting.ShowingNeedInputPrompt() {
		t.Fatal("Update(i) did not show need-user-input decision prompt")
	}
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{"Need user input", "Blocked work", "[r] Resume", "[a] Abort"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	if len(client.needUserInputFeatureIDs) != 0 {
		t.Fatalf("NeedUserInputDecision calls = %v before decision, want none", client.needUserInputFeatureIDs)
	}

	model, cmd = prompting.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd == nil {
		t.Fatal("Update(r) returned nil command, want need-user-input decision mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	decided := model.(APIAppModel)

	if got := strings.Join(client.needUserInputFeatureIDs, ","); got != "blocked" {
		t.Fatalf("NeedUserInputDecision calls = %q, want blocked", got)
	}
	if got := client.needUserInputRequests; len(got) != 1 || got[0].Decision != "resume" || got[0].RepoName != "" || got[0].CycleType != "" {
		t.Fatalf("NeedUserInputDecision requests = %+v, want feature-scoped resume", got)
	}
	if decided.ShowingNeedInputPrompt() {
		t.Fatal("need-user-input prompt remained open after accepted decision")
	}
	view = stripANSI(decided.View().Content)
	for _, want := range []string{"Completed Need Input Decision", "blocked"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelPermissionAnswerUsesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		permissions: server.PermissionSnapshotResponse{Requests: []server.ControlRequestDTO{
			{RequestID: "perm-1", SessionID: "sess-1", FeatureID: "active", ToolName: "Bash", Status: "pending", Summary: "go test ./internal/tui"},
		}},
		permissionAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd != nil {
		t.Fatal("Update(a) returned command before permission decision")
	}
	prompting := model.(APIAppModel)
	if !prompting.ShowingPermissionPrompt() {
		t.Fatal("Update(a) did not show permission answer prompt")
	}
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{"Permission request", "Active work", "Bash", "go test ./internal/tui", "[a] Allow", "[d] Deny"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	if len(client.permissionAnswers) != 0 {
		t.Fatalf("AnswerPermission calls = %v before decision, want none", client.permissionAnswers)
	}

	model, cmd = prompting.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want permission answer mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	answered := model.(APIAppModel)

	if got := client.permissionAnswers; len(got) != 1 || got[0].RequestID != "perm-1" || got[0].SessionID != "sess-1" || got[0].Decision != "allow" {
		t.Fatalf("AnswerPermission requests = %+v, want perm-1/sess-1 allow", got)
	}
	if answered.ShowingPermissionPrompt() {
		t.Fatal("permission prompt remained open after accepted answer")
	}
	view = stripANSI(answered.View().Content)
	for _, want := range []string{"Completed Permission Answer", "active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelHelpMessageUsesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		prompts: server.PromptSnapshotResponse{HelpQueue: []server.HelpQueueDTO{
			{FeatureID: "active", Question: "Which implementation path?", Pending: true},
		}},
		helpAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if cmd != nil {
		t.Fatal("Update(h) returned command before help answer")
	}
	prompting := model.(APIAppModel)
	if !prompting.ShowingHelpPrompt() {
		t.Fatal("Update(h) did not show help answer prompt")
	}
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{"Help request", "Active work", "Which implementation path?", "Answer:"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	if len(client.helpRequests) != 0 {
		t.Fatalf("SendHelp calls = %v before answer, want none", client.helpRequests)
	}

	for _, ch := range "use codex" {
		model, cmd = prompting.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		if cmd != nil {
			t.Fatalf("Update(%q) returned unexpected command while typing help answer", ch)
		}
		prompting = model.(APIAppModel)
	}
	model, cmd = prompting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want help send mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	answered := model.(APIAppModel)

	if got := client.helpRequests; len(got) != 1 || got[0].FeatureID != "active" || got[0].SessionID != "" || got[0].Message != "use codex" {
		t.Fatalf("SendHelp requests = %+v, want feature-scoped message", got)
	}
	if answered.ShowingHelpPrompt() {
		t.Fatal("help prompt remained open after accepted answer")
	}
	view = stripANSI(answered.View().Content)
	for _, want := range []string{"Completed Help Reply", "active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelAskUserAnswerUsesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		prompts: server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{
			{
				RequestID: "ask-1",
				SessionID: "sess-1",
				FeatureID: "active",
				ToolName:  "AskUserQuestion",
				Status:    "pending",
				Summary:   "Which database?",
				Questions: []server.AskUserQuestionDTO{{Question: "Which database?"}},
			},
		}},
		askUserAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if cmd != nil {
		t.Fatal("Update(u) returned command before ask-user answer")
	}
	prompting := model.(APIAppModel)
	if !prompting.ShowingAskUserPrompt() {
		t.Fatal("Update(u) did not show ask-user answer prompt")
	}
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{"AskUser question", "Active work", "Which database?", "Answer:"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	if len(client.askUserAnswers) != 0 {
		t.Fatalf("AnswerAskUser calls = %v before answer, want none", client.askUserAnswers)
	}

	for _, ch := range "PostgreSQL" {
		model, cmd = prompting.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		if cmd != nil {
			t.Fatalf("Update(%q) returned unexpected command while typing ask-user answer", ch)
		}
		prompting = model.(APIAppModel)
	}
	model, cmd = prompting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want ask-user answer mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	answered := model.(APIAppModel)

	if got := client.askUserAnswers; len(got) != 1 || got[0].RequestID != "ask-1" || got[0].SessionID != "sess-1" || got[0].Answers["Which database?"] != "PostgreSQL" {
		t.Fatalf("AnswerAskUser requests = %+v, want ask-1/sess-1 answer keyed by full question", got)
	}
	if answered.ShowingAskUserPrompt() {
		t.Fatal("ask-user prompt remained open after accepted answer")
	}
	view = stripANSI(answered.View().Content)
	for _, want := range []string{"Completed AskUser Answer", "active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelAskUserOptionPromptSendsSelectedOption(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		prompts: server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{
			{
				RequestID: "ask-1",
				SessionID: "sess-1",
				FeatureID: "active",
				ToolName:  "AskUserQuestion",
				Status:    "pending",
				Summary:   "Which database?",
				Questions: []server.AskUserQuestionDTO{{
					Question: "Which database?",
					Options: []server.AskUserOptionDTO{
						{Label: "PostgreSQL", Description: "relational"},
						{Label: "DynamoDB", Description: "managed key-value"},
					},
				}},
			},
		}},
		askUserAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	prompting := model.(APIAppModel)
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{"AskUser question", "Request: ask-1", "Session: sess-1", "1. PostgreSQL - relational", "2. DynamoDB - managed key-value"} {
		if !strings.Contains(view, want) {
			t.Fatalf("AskUser option prompt missing %q in:\n%s", want, view)
		}
	}

	model, _ = prompting.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want ask-user answer")
	}
	msg := cmd()
	_, _ = model.(APIAppModel).Update(msg)
	if got := client.askUserAnswers; len(got) != 1 || got[0].Answers["Which database?"] != "DynamoDB" {
		t.Fatalf("AnswerAskUser requests = %+v, want DynamoDB answer", got)
	}
}

func TestAPIAppModelReviewDecisionsUseRESTMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		msg     tea.Msg
		detail  server.FeatureDetailResponse
		wantReq server.ReviewDecisionRequest
	}{
		{
			name: "phase plan proceeds",
			msg:  PlanReviewDecisionMsg{FeatureID: "active", Decision: "proceed"},
			detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
				FeatureSummary: server.FeatureSummary{
					ID: "active", Name: "Active work", Slug: "active-work", Status: "PlanNeedsReview", CurrentPhase: "plan",
					Progress: server.FeatureProgress{CurrentRoadmapPhase: 2, TotalRoadmapPhases: 3},
				},
			}},
			wantReq: server.ReviewDecisionRequest{Decision: "proceed", PhasePlan: true},
		},
		{
			name: "roadmap reject iterates with comment",
			msg:  RoadmapReviewDecisionMsg{FeatureID: "active", Decision: "reject", Comment: "Needs clearer slices"},
			wantReq: server.ReviewDecisionRequest{
				Decision: "iterate",
				Roadmap:  true,
				Comment:  "Needs clearer slices",
			},
		},
		{
			name: "gate proceeds with target phase",
			msg:  GateReviewDecisionMsg{FeatureID: "active", Phase: feature.PhaseImplement, Decision: "proceed"},
			wantReq: server.ReviewDecisionRequest{
				Decision: "proceed",
				Phase:    "implement",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.detail.Feature.ID == "" {
				tt.detail = server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
					FeatureSummary: server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "PlanNeedsReview", CurrentPhase: "plan"},
				}}
			}
			client := &fakeTUIAPIClient{
				features: server.FeatureListResponse{Features: []server.FeatureSummary{
					{ID: "active", Name: "Active work", Slug: "active-work", Status: "PlanNeedsReview", CurrentPhase: "plan", CreatedAt: time.Now()},
				}},
				detail:         tt.detail,
				reviewAccepted: apiTestActionResponse{},
			}
			app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
			if err != nil {
				t.Fatalf("NewAPIAppModel() error = %v", err)
			}

			model, cmd := app.Update(tt.msg)
			if cmd == nil {
				t.Fatal("Update(review decision) returned nil command, want review-decision REST mutation command")
			}
			msg := cmd()
			model, _ = model.(APIAppModel).Update(msg)
			reviewed := model.(APIAppModel)

			if got := strings.Join(client.reviewFeatureIDs, ","); got != "active" {
				t.Fatalf("ReviewDecision feature IDs = %q, want active", got)
			}
			if len(client.reviewRequests) != 1 || client.reviewRequests[0] != tt.wantReq {
				t.Fatalf("ReviewDecision requests = %+v, want %+v", client.reviewRequests, tt.wantReq)
			}
			view := stripANSI(reviewed.View().Content)
			for _, want := range []string{"Completed Review Decision", "active"} {
				if !strings.Contains(view, want) {
					t.Fatalf("API app View() missing %q in:\n%s", want, view)
				}
			}
		})
	}
}

func TestAPIAppModelReviewCommentsPreviewAndStartUseREST(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Published", CurrentPhase: "publish", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Published", CurrentPhase: "publish"},
			RepoStatus: []server.RepoStatusDTO{
				{Name: "agentic-orchestrator", Publishable: true},
			},
			Actions: []server.ActionDTO{
				{
					ID:      "review-comments",
					Enabled: true,
					Scope:   server.ActionScopeDTO{Type: "feature", RepoSelection: "required", CycleType: "review-comments"},
					RequiredInputs: []server.ActionInputDTO{
						{Name: "repo", Kind: "string", Required: true},
						{Name: "mode", Kind: "enum", Required: true, Options: []string{"auto", "address_all"}},
					},
				},
			},
		}},
		reviewCommentsResponse: server.ReviewCommentsFetchResponse{
			FeatureID: "active",
			Repo:      "agentic-orchestrator",
			Comments: []server.ReviewCommentDTO{
				{ID: 101, Type: "review", Path: "internal/tui/api_app.go", Line: 42, Body: "use REST DTOs here", UserLogin: "reviewer", DiffHunk: "@@ -1 +1 @@\n-old\n+new"},
			},
		},
		startReviewCommentsAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	if cmd == nil {
		t.Fatal("Update(g) returned nil command, want review-comments fetch command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	previewing := model.(APIAppModel)

	if got := strings.Join(client.reviewCommentsFeatureIDs, ","); got != "active" {
		t.Fatalf("FetchReviewComments feature IDs = %q, want active", got)
	}
	if got := client.reviewCommentsFetchRequests; len(got) != 1 || got[0].Repo != "agentic-orchestrator" {
		t.Fatalf("FetchReviewComments requests = %+v, want agentic-orchestrator repo", got)
	}
	if len(client.startReviewCommentsFeatureIDs) != 0 {
		t.Fatalf("StartReviewComments calls = %v before preview confirmation, want none", client.startReviewCommentsFeatureIDs)
	}
	view := stripANSI(previewing.View().Content)
	for _, want := range []string{"Review Comments: Active work (1)", "agentic-orchestrator", "Shift+A", "@reviewer", "internal/tui/api_app.go:42", "use REST DTOs here", "@@ -1 +1 @@", "-old", "+new"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}

	model, cmd = previewing.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter) started review-comments; want Shift+A only")
	}
	previewing = model.(APIAppModel)
	model, cmd = previewing.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd != nil {
		t.Fatal("Update(a) started review-comments; want Shift+A only")
	}
	previewing = model.(APIAppModel)

	model, cmd = previewing.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})
	if cmd == nil {
		t.Fatal("Update(Shift+A) returned nil command, want review-comments start mutation")
	}
	msg = cmd()
	model, _ = model.(APIAppModel).Update(msg)
	started := model.(APIAppModel)

	if got := strings.Join(client.startReviewCommentsFeatureIDs, ","); got != "active" {
		t.Fatalf("StartReviewComments feature IDs = %q, want active", got)
	}
	if got := client.startReviewCommentsRequests; len(got) != 1 || got[0].Repo != "agentic-orchestrator" || got[0].Mode != "auto" || len(got[0].Comments) != 1 || got[0].Comments[0].ID != 101 || got[0].Comments[0].DiffHunk == "" {
		t.Fatalf("StartReviewComments requests = %+v, want agentic-orchestrator auto with previewed comment", got)
	}
	view = stripANSI(started.View().Content)
	for _, want := range []string{"Completed Review Comments", "active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelRefactorPromptSelectsPipelineAndStartsRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Published", CurrentPhase: "publish", CreatedAt: time.Now(), Repos: []string{"agentic-orchestrator"}},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Published", CurrentPhase: "publish"},
			RepoStatus: []server.RepoStatusDTO{
				{Name: "agentic-orchestrator", Publishable: true},
			},
			Actions: []server.ActionDTO{
				{
					ID:      "refactor",
					Enabled: true,
					Scope:   server.ActionScopeDTO{Type: "feature"},
					RequiredInputs: []server.ActionInputDTO{
						{Name: "repo", Kind: "string", Required: false},
						{Name: "prompt", Kind: "string", Required: true, MaxLength: server.MaxActionTextBytes},
						{Name: "pipeline", Kind: "enum", Required: false, Options: []string{"medium", "large", "moonshot"}},
					},
				},
			},
		}},
		startRefactorAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'F', Text: "F"})
	if cmd != nil {
		t.Fatal("Update(Shift+F) returned command before refactor prompt submit")
	}
	refactor := model.(APIAppModel)
	view := stripANSI(refactor.View().Content)
	for _, want := range []string{"Refactor", "Active work", "What changes do you want to make?", "Describe the refactoring for", "agentic-orchestrator...", "ctrl+s"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in refactor prompt:\n%s", want, view)
		}
	}

	for _, r := range "extract transport boundary" {
		model, cmd = refactor.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		refactor = model.(APIAppModel)
	}
	model, cmd = refactor.Update(tea.KeyPressMsg{Code: 's', Text: "s", Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("Update(ctrl+s) returned command before pipeline selection")
	}
	refactor = model.(APIAppModel)
	view = stripANSI(refactor.View().Content)
	for _, want := range []string{"Select Pipeline for Refactor", "medium", "large", "moonshot", "Inquiry + research + planning", "Confirm"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in refactor pipeline selector:\n%s", want, view)
		}
	}

	model, cmd = refactor.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want refactor start mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	started := model.(APIAppModel)

	if got := strings.Join(client.startRefactorFeatureIDs, ","); got != "active" {
		t.Fatalf("StartRefactor feature IDs = %q, want active", got)
	}
	if got := client.startRefactorRequests; len(got) != 1 || got[0].Repo != "agentic-orchestrator" || got[0].Prompt != "extract transport boundary" || got[0].Pipeline != feature.PipelineLarge {
		t.Fatalf("StartRefactor requests = %+v, want agentic-orchestrator prompt with large pipeline", got)
	}
	view = stripANSI(started.View().Content)
	for _, want := range []string{"Completed Refactor", "active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelRefactorRestartShortcutIsNotExposed(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Published", CurrentPhase: "publish", CreatedAt: time.Now(), Repos: []string{"agentic-orchestrator"}},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Published", CurrentPhase: "publish"},
			RepoStatus: []server.RepoStatusDTO{
				{Name: "agentic-orchestrator", Publishable: true},
			},
			Actions: []server.ActionDTO{
				{
					ID:      "refactor",
					Enabled: true,
					Scope:   server.ActionScopeDTO{Type: "feature"},
					RequiredInputs: []server.ActionInputDTO{
						{Name: "repo", Kind: "string", Required: false},
						{Name: "prompt", Kind: "string", Required: true, MaxLength: server.MaxActionTextBytes},
						{Name: "pipeline", Kind: "enum", Required: false, Options: []string{"medium", "large", "moonshot"}},
					},
				},
			},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'f', Text: "f", Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("Update(Ctrl+F) returned command; restart refactor is not a dashboard shortcut")
	}
	updated := model.(APIAppModel)
	if updated.refactorPrompt != nil || updated.refactorPipeline != nil {
		t.Fatal("Update(Ctrl+F) opened refactor restart UI; want no normal-user restart shortcut")
	}
	if len(client.restartRefactorFeatureIDs) != 0 || len(client.startRefactorFeatureIDs) != 0 {
		t.Fatalf("refactor calls start=%v restart=%v; want none", client.startRefactorFeatureIDs, client.restartRefactorFeatureIDs)
	}
}

func TestAPIAppModelFeatureActionUsesReadModelDisabledState(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement"},
			Actions: []server.ActionDTO{
				{ID: "delete", Enabled: false, Scope: server.ActionScopeDTO{Type: "feature"}, DisabledReasons: []server.ActionDisabledReasonDTO{
					{Code: "running", Message: "delete is disabled while work is running"},
				}},
			},
		}},
		deleteAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if cmd != nil {
		t.Fatal("Update(d) returned command for disabled delete action")
	}
	updated := model.(APIAppModel)
	if updated.ShowingFeatureActionConfirm() {
		t.Fatal("Update(d) showed confirmation for disabled delete action")
	}
	if len(client.deleteFeatureIDs) != 0 {
		t.Fatalf("DeleteFeature calls = %v, want none for disabled action", client.deleteFeatureIDs)
	}
	if view := stripANSI(updated.View().Content); !strings.Contains(view, "Delete is unavailable") {
		t.Fatalf("View() missing disabled-action status in:\n%s", view)
	}
}

func TestAPIAppModelDeleteEvictsFeatureBeforeRefresh(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	active := server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Created", CurrentPhase: "implement", CreatedAt: created}
	next := server.FeatureSummary{ID: "next", Name: "Next work", Slug: "next-work", Status: "Created", CurrentPhase: "implement", CreatedAt: created.Add(-time.Minute)}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{active, next}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: active,
			Actions: []server.ActionDTO{
				{ID: "delete", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}},
			},
		}},
		detailsByID: map[string]server.FeatureDetailResponse{
			"active": {Feature: server.FeatureDetailDTO{
				FeatureSummary: active,
				Actions: []server.ActionDTO{
					{ID: "delete", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}},
				},
			}},
			"next": {Feature: server.FeatureDetailDTO{FeatureSummary: next}},
		},
		livePreviewsByID: map[string]server.LivePreviewResponse{
			"active": {Feature: active},
			"next":   {Feature: next},
		},
		deleteAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	if got := app.SelectedFeatureID(); got != "active" {
		t.Fatalf("SelectedFeatureID() = %q, want active precondition", got)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if cmd != nil {
		t.Fatal("Update(d) returned command before confirmation")
	}
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("Update(y) returned nil command, want delete mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	updated := model.(APIAppModel)

	if got := strings.Join(client.deleteFeatureIDs, ","); got != "active" {
		t.Fatalf("DeleteFeature calls = %q, want active", got)
	}
	if got := updated.SelectedFeatureID(); got != "next" {
		t.Fatalf("SelectedFeatureID() = %q, want next after delete", got)
	}
	view := stripANSI(updated.View().Content)
	if strings.Contains(view, "active-work") {
		t.Fatalf("API app View() still shows deleted feature:\n%s", view)
	}
	if !strings.Contains(view, "Completed Delete") {
		t.Fatalf("API app View() missing delete completion status:\n%s", view)
	}
}

func TestAPIAppModelIgnoresStaleDetailErrorForRemovedFeature(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	active := server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Created", CurrentPhase: "implement", CreatedAt: created}
	next := server.FeatureSummary{ID: "next", Name: "Next work", Slug: "next-work", Status: "Created", CurrentPhase: "implement", CreatedAt: created.Add(-time.Minute)}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{active, next}},
		detail:   server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{FeatureSummary: active}},
		livePreview: server.LivePreviewResponse{
			Feature: active,
		},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	app.ApplyRefreshSnapshot(server.RefreshSnapshot{
		Features: &server.FeatureListResponse{Features: []server.FeatureSummary{next}},
	})
	if got := app.SelectedFeatureID(); got != "next" {
		t.Fatalf("SelectedFeatureID() = %q, want next after refresh removed active", got)
	}

	model, _ := app.Update(apiFeatureDetailMsg{
		featureID: "active",
		err:       errors.New("api GET /api/v1/features/active: not_found (404): feature not found"),
	})
	updated := model.(APIAppModel)
	if strings.Contains(updated.statusMessage, "Detail refresh failed") {
		t.Fatalf("statusMessage = %q, want stale detail error ignored", updated.statusMessage)
	}
	if got := updated.SelectedFeatureID(); got != "next" {
		t.Fatalf("SelectedFeatureID() = %q, want next after stale detail error", got)
	}
}

func TestAPIAppModelPublishConfirmsBeforeRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "ready", Name: "Ready to publish", Slug: "ready-to-publish", Status: "CodeReady", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "ready", Name: "Ready to publish", Slug: "ready-to-publish", Status: "CodeReady", CurrentPhase: "implement"},
			Actions: []server.ActionDTO{
				{ID: "publish", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}},
			},
		}},
		publishAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if cmd != nil {
		t.Fatal("Update(p) returned command before confirmation")
	}
	confirming := model.(APIAppModel)
	if view := stripANSI(confirming.View().Content); !strings.Contains(view, "Confirm Publish") {
		t.Fatalf("View() missing publish confirmation in:\n%s", view)
	}
	if len(client.publishFeatureIDs) != 0 {
		t.Fatalf("PublishFeature calls = %v before confirmation, want none", client.publishFeatureIDs)
	}

	model, cmd = confirming.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("Update(y) returned nil command, want publish mutation command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	published := model.(APIAppModel)

	if got := strings.Join(client.publishFeatureIDs, ","); got != "ready" {
		t.Fatalf("PublishFeature calls = %q, want ready", got)
	}
	view := stripANSI(published.View().Content)
	for _, want := range []string{"Completed Publish", "ready"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelPublishRepoSelectorSendsSelectedRepos(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "ready", Name: "Ready to publish", Slug: "ready-to-publish", Status: "CodeReady", CurrentPhase: "implement", Repos: []string{"api", "web"}, CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "ready", Name: "Ready to publish", Slug: "ready-to-publish", Status: "CodeReady", CurrentPhase: "implement", Repos: []string{"api", "web"}},
			RepoStatus: []server.RepoStatusDTO{
				{Name: "api", Publishable: true},
				{Name: "web", Publishable: true},
			},
			Actions: []server.ActionDTO{
				{ID: "publish", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}},
			},
		}},
		publishAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if cmd != nil {
		t.Fatal("Update(p) returned command before repo selection")
	}
	selecting := model.(APIAppModel)
	view := stripANSI(selecting.View().Content)
	for _, want := range []string{"Select repo — Publish", "[x] api", "[x] web"} {
		if !strings.Contains(view, want) {
			t.Fatalf("repo selector missing %q in:\n%s", want, view)
		}
	}

	model, _ = selecting.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	selecting = model.(APIAppModel)
	model, cmd = selecting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter) returned command before publish confirmation")
	}
	confirming := model.(APIAppModel)
	view = stripANSI(confirming.View().Content)
	for _, want := range []string{"Confirm Publish", "Repos: web"} {
		if !strings.Contains(view, want) {
			t.Fatalf("publish confirmation missing %q in:\n%s", want, view)
		}
	}

	model, cmd = confirming.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("Update(y) returned nil command, want publish mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	if got := client.publishRequests; len(got) != 1 || !slices.Equal(got[0].Repos, []string{"web"}) {
		t.Fatalf("PublishFeature requests = %+v, want repos [web]", got)
	}
}

func TestAPIAppModelRepoSelectorsRouteCycleActions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		key        tea.KeyPressMsg
		actionID   string
		wantTitle  string
		accepted   apiTestActionResponse
		assertCall func(t *testing.T, client *fakeTUIAPIClient)
	}{
		{
			name:      "rebase",
			key:       tea.KeyPressMsg{Code: 'b', Text: "b"},
			actionID:  "rebase",
			wantTitle: "Confirm Rebase",
			accepted:  apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := client.startRebaseRequests; len(got) != 1 || got[0].Repo != "web" {
					t.Fatalf("StartRebase requests = %+v, want repo web", got)
				}
			},
		},
		{
			name:      "tweak",
			key:       tea.KeyPressMsg{Code: 't', Text: "t"},
			actionID:  "tweak",
			wantTitle: "Confirm Tweak",
			accepted:  apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := len(client.startTweakRequests); got != 1 {
					t.Fatalf("StartTweak request count = %d, want 1", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := apiRepoSelectorClient(tt.actionID, tt.accepted)
			app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
			if err != nil {
				t.Fatalf("NewAPIAppModel() error = %v", err)
			}

			model, cmd := app.Update(tt.key)
			if cmd != nil {
				t.Fatal("action key returned command before repo selection")
			}
			selecting := model.(APIAppModel)
			if view := stripANSI(selecting.View().Content); !strings.Contains(view, "Select repo") || !strings.Contains(view, "api") || !strings.Contains(view, "web") {
				t.Fatalf("repo selector not shown:\n%s", view)
			}

			model, _ = selecting.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			selecting = model.(APIAppModel)
			model, cmd = selecting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if cmd != nil {
				t.Fatal("repo selection returned command before confirmation")
			}
			confirming := model.(APIAppModel)
			view := stripANSI(confirming.View().Content)
			for _, want := range []string{tt.wantTitle, "Repo: web"} {
				if !strings.Contains(view, want) {
					t.Fatalf("confirmation missing %q in:\n%s", want, view)
				}
			}

			model, cmd = confirming.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
			if cmd == nil {
				t.Fatal("confirmation returned nil command")
			}
			msg := cmd()
			_, _ = model.(APIAppModel).Update(msg)
			tt.assertCall(t, client)
		})
	}
}

func TestAPIAppModelReviewAndRefactorRepoSelectorsUseSelectedRepo(t *testing.T) {
	t.Parallel()

	t.Run("review comments", func(t *testing.T) {
		t.Parallel()

		client := apiRepoSelectorClient("review-comments", apiTestActionResponse{})
		client.reviewCommentsResponse = server.ReviewCommentsFetchResponse{FeatureID: "active", Repo: "web"}
		app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
		if err != nil {
			t.Fatalf("NewAPIAppModel() error = %v", err)
		}

		model, cmd := app.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
		if cmd != nil {
			t.Fatal("Update(g) returned command before repo selection")
		}
		model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyDown})
		model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("repo selection returned nil command, want review-comments fetch")
		}
		msg := cmd()
		_, _ = model.(APIAppModel).Update(msg)
		if got := client.reviewCommentsFetchRequests; len(got) != 1 || got[0].Repo != "web" {
			t.Fatalf("FetchReviewComments requests = %+v, want repo web", got)
		}
	})

	t.Run("refactor", func(t *testing.T) {
		t.Parallel()

		client := apiRepoSelectorClient("refactor", apiTestActionResponse{})
		app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
		if err != nil {
			t.Fatalf("NewAPIAppModel() error = %v", err)
		}

		model, cmd := app.Update(tea.KeyPressMsg{Code: 'F', Text: "F"})
		if cmd != nil {
			t.Fatal("Update(F) returned command before repo selection")
		}
		model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyDown})
		model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd != nil {
			t.Fatal("repo selection returned command before refactor prompt")
		}
		prompting := model.(APIAppModel)
		view := stripANSI(prompting.View().Content)
		if !strings.Contains(view, "Describe the refactoring for") || !strings.Contains(view, "web...") {
			t.Fatalf("refactor prompt missing selected repo:\n%s", view)
		}

		for _, r := range "split transport" {
			model, _ = prompting.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			prompting = model.(APIAppModel)
		}
		model, _ = prompting.Update(tea.KeyPressMsg{Code: 's', Text: "s", Mod: tea.ModCtrl})
		model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("pipeline selection returned nil command")
		}
		msg := cmd()
		_, _ = model.(APIAppModel).Update(msg)
		if got := client.startRefactorRequests; len(got) != 1 || got[0].Repo != "web" || got[0].Prompt != "split transport" {
			t.Fatalf("StartRefactor requests = %+v, want repo web prompt", got)
		}
	})
}

func TestAPIAppModelRewindPhaseSelectorUsesChosenTarget(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Interrupted", CurrentPhase: "review", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Interrupted", CurrentPhase: "review"},
			Actions: []server.ActionDTO{
				{
					ID:      "rewind",
					Enabled: true,
					Scope:   server.ActionScopeDTO{Type: "feature"},
					RequiredInputs: []server.ActionInputDTO{
						{Name: "target_phase", Kind: "enum", Required: true, Options: []string{"plan", "implement"}},
						{Name: "upgrade_pipeline", Kind: "enum", Required: false, Options: []string{"large"}},
					},
				},
			},
		}},
		rewindAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'r', Text: "r", Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("Update(ctrl+r) returned command before phase choice")
	}
	choosing := model.(APIAppModel)
	if view := stripANSI(choosing.View().Content); !strings.Contains(view, "Rewind to Phase") || !strings.Contains(view, "Rewind to Plan") || !strings.Contains(view, "Rewind to Implement") || !strings.Contains(view, "Pipeline Upgrade") || !strings.Contains(view, "Upgrade to large") {
		t.Fatalf("rewind phase selector missing choices:\n%s", view)
	}

	model, _ = choosing.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("phase selection returned command before confirmation")
	}
	confirming := model.(APIAppModel)
	if view := stripANSI(confirming.View().Content); !strings.Contains(view, "Rewind Confirmation") || !strings.Contains(view, "Rewind to Implement") || !strings.Contains(view, "All progress from this phase onwards will be lost") {
		t.Fatalf("rewind confirmation missing selected target:\n%s", view)
	}

	model, cmd = confirming.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("Update(y) returned nil command, want rewind mutation")
	}
	msg := cmd()
	_, _ = model.(APIAppModel).Update(msg)
	if got := client.rewindRequests; len(got) != 1 || got[0].TargetPhase != "implement" {
		t.Fatalf("RewindFeature requests = %+v, want target implement", got)
	}
}

func TestAPIAppModelRewindPipelineUpgradeUsesUpgradeRequest(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Interrupted", CurrentPhase: "review", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Interrupted", CurrentPhase: "review"},
			Actions: []server.ActionDTO{
				{
					ID:      "rewind",
					Enabled: true,
					Scope:   server.ActionScopeDTO{Type: "feature"},
					RequiredInputs: []server.ActionInputDTO{
						{Name: "target_phase", Kind: "enum", Required: true, Options: []string{"plan", "implement"}},
						{Name: "upgrade_pipeline", Kind: "enum", Required: false, Options: []string{"large"}},
					},
				},
			},
		}},
		rewindAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'r', Text: "r", Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("Update(ctrl+r) returned command before phase choice")
	}
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("upgrade selection returned command before confirmation")
	}
	confirming := model.(APIAppModel)
	if view := stripANSI(confirming.View().Content); !strings.Contains(view, "Upgrade to large") || !strings.Contains(view, "restart") || !strings.Contains(view, "KB Build") {
		t.Fatalf("rewind upgrade confirmation missing upgrade copy:\n%s", view)
	}

	model, cmd = confirming.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("Update(y) returned nil command, want rewind mutation")
	}
	msg := cmd()
	_, _ = model.(APIAppModel).Update(msg)
	if got := client.rewindRequests; len(got) != 1 || got[0].TargetPhase != "inquire" || got[0].UpgradePipeline != feature.PipelineLarge {
		t.Fatalf("RewindFeature requests = %+v, want inquire with large upgrade", got)
	}
}

func TestAPIAppModelRewindImplementOpensRoadmapPhasePicker(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Interrupted", CurrentPhase: "implement", CreatedAt: time.Now(), Progress: server.FeatureProgress{CurrentRoadmapPhase: 2, TotalRoadmapPhases: 3}},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Interrupted", CurrentPhase: "implement", Progress: server.FeatureProgress{CurrentRoadmapPhase: 2, TotalRoadmapPhases: 3}},
			Actions: []server.ActionDTO{
				{
					ID:      "rewind",
					Enabled: true,
					Scope:   server.ActionScopeDTO{Type: "feature"},
					RequiredInputs: []server.ActionInputDTO{
						{Name: "target_phase", Kind: "enum", Required: true, Options: []string{"plan", "implement"}},
					},
				},
			},
		}},
		rewindAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: 'r', Text: "r", Mod: tea.ModCtrl})
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("implement rewind selection returned command before roadmap phase picker")
	}
	picking := model.(APIAppModel)
	if view := stripANSI(picking.View().Content); !strings.Contains(view, "Choose Roadmap Phase") || !strings.Contains(view, "Phase 2/3") || !strings.Contains(view, "current phase") {
		t.Fatalf("roadmap phase picker missing expected rows:\n%s", view)
	}

	model, cmd = picking.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("roadmap phase selection returned command before confirmation")
	}
	confirming := model.(APIAppModel)
	if view := stripANSI(confirming.View().Content); !strings.Contains(view, "Rewind Implement to roadmap Phase 2") || !strings.Contains(view, "Keep: Phase 1") || !strings.Contains(view, "Discard: Phase 3") {
		t.Fatalf("roadmap rewind confirmation missing partial rewind copy:\n%s", view)
	}

	model, cmd = confirming.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("Update(y) returned nil command, want rewind mutation")
	}
	msg := cmd()
	_, _ = model.(APIAppModel).Update(msg)
	if got := client.rewindRequests; len(got) != 1 || got[0].TargetPhase != "implement" || got[0].RoadmapPhase != 2 {
		t.Fatalf("RewindFeature requests = %+v, want implement roadmap phase 2", got)
	}
}

func TestAPIAppModelSelectionFetchesSelectedFeatureDetail(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
			{ID: "queued", Name: "Queued work", Slug: "queued-work", Status: "Created", CurrentPhase: "research", CreatedAt: time.Now().Add(-time.Hour)},
		}},
		detailsByID: map[string]server.FeatureDetailResponse{
			"active": {Feature: server.FeatureDetailDTO{
				FeatureSummary: server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work"},
				Description:    "Active detail",
			}},
			"queued": {Feature: server.FeatureDetailDTO{
				FeatureSummary: server.FeatureSummary{ID: "queued", Name: "Queued work", Slug: "queued-work"},
				Description:    "Queued detail from REST",
			}},
		},
		livePreviewsByID: map[string]server.LivePreviewResponse{
			"active": {
				Feature:    server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work"},
				Activity:   "Active preview",
				Transcript: []server.TranscriptMessageDTO{{Index: 1, Role: "assistant", Type: "text", Text: "Active preview tail"}},
			},
			"queued": {
				Feature:    server.FeatureSummary{ID: "queued", Name: "Queued work", Slug: "queued-work"},
				Activity:   "Queued preview",
				Transcript: []server.TranscriptMessageDTO{{Index: 1, Role: "assistant", Type: "text", Text: "Queued preview from REST"}},
			},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if cmd == nil {
		t.Fatal("Update(down) returned nil command, want selected detail refresh command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	selected := model.(APIAppModel)

	if got := selected.SelectedFeatureID(); got != "queued" {
		t.Fatalf("SelectedFeatureID() = %q, want queued", got)
	}
	if got := strings.Join(client.detailFeatureIDs, ","); got != "active,queued" {
		t.Fatalf("FeatureDetail calls = %q, want active,queued", got)
	}
	if got := strings.Join(client.livePreviewFeatureIDs, ","); got != "active,queued" {
		t.Fatalf("LivePreview calls = %q, want active,queued", got)
	}
	view := stripANSI(selected.View().Content)
	for _, want := range []string{"Queued preview", "Queued preview from REST"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	for _, stale := range []string{"Active detail", "Active preview", "Active preview tail"} {
		if strings.Contains(view, stale) {
			t.Fatalf("API app View() kept stale selected-feature content %q in:\n%s", stale, view)
		}
	}
}

func TestAPIAppModelQuitPromptsOnlyForOwnedServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		shutdownAccepted: apiTestActionResponse{},
	}
	var fallbackCalls int
	attached, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel(attached) error = %v", err)
	}
	model, cmd := attached.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("attached Update(q) did not return quit command")
	}
	if got := model.(APIAppModel).ShowingOwnedServerQuitPrompt(); got {
		t.Fatal("attached TUI showed owned-server quit prompt")
	}
	if client.shutdownCalls != 0 {
		t.Fatalf("attached TUI shutdown calls = %d, want 0", client.shutdownCalls)
	}

	owned, err := NewAPIAppModel(ctx, client, APIAppOptions{
		OwnedServer: true,
		StopOwnedServer: func(context.Context) error {
			fallbackCalls++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewAPIAppModel(owned) error = %v", err)
	}
	model, cmd = owned.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd != nil {
		t.Fatal("owned Update(q) returned unexpected command")
	}
	if got := model.(APIAppModel).ShowingOwnedServerQuitPrompt(); !got {
		t.Fatal("owned TUI did not show owned-server quit prompt")
	}

	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("owned Update(y) returned nil command, want shutdown command")
	}
	msg := cmd()
	if client.shutdownCalls != 1 {
		t.Fatalf("owned TUI shutdown calls = %d, want 1", client.shutdownCalls)
	}
	if fallbackCalls != 1 {
		t.Fatalf("owned TUI fallback stop calls = %d, want 1 after shutdown acceptance", fallbackCalls)
	}
	_, cmd = model.(APIAppModel).Update(msg)
	if cmd == nil {
		t.Fatal("owned shutdown completion did not return quit command")
	}

	ownedNo, err := NewAPIAppModel(ctx, client, APIAppOptions{OwnedServer: true})
	if err != nil {
		t.Fatalf("NewAPIAppModel(owned no) error = %v", err)
	}
	model, _ = ownedNo.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	_, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd == nil {
		t.Fatal("owned Update(n) returned nil command, want quit command")
	}
	if client.shutdownCalls != 1 {
		t.Fatalf("owned TUI shutdown calls after n = %d, want 1", client.shutdownCalls)
	}
}

func TestAPIAppModelOwnedServerShutdownErrorKeepsTUIOpen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{shutdownErr: errors.New("connection refused")}
	owned, err := NewAPIAppModel(ctx, client, APIAppOptions{OwnedServer: true})
	if err != nil {
		t.Fatalf("NewAPIAppModel(owned) error = %v", err)
	}
	model, _ := owned.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("owned Update(y) returned nil command, want shutdown command")
	}
	model, cmd = model.(APIAppModel).Update(cmd())
	if cmd != nil {
		t.Fatal("owned shutdown error returned quit command")
	}
	updated := model.(APIAppModel)
	if updated.ShowingOwnedServerQuitPrompt() {
		t.Fatal("owned shutdown error left quit prompt visible")
	}
	if !strings.Contains(updated.statusMessage, "Server shutdown failed: connection refused") {
		t.Fatalf("statusMessage = %q, want shutdown failure", updated.statusMessage)
	}
}

func apiRepoSelectorClient(actionID string, accepted apiTestActionResponse) *fakeTUIAPIClient {
	action := server.ActionDTO{
		ID:      actionID,
		Enabled: true,
		Scope:   server.ActionScopeDTO{Type: "feature", RepoSelection: "optional"},
	}
	switch actionID {
	case "review-comments":
		action.Scope = server.ActionScopeDTO{Type: "feature", RepoSelection: "required", CycleType: "review-comments"}
		action.RequiredInputs = []server.ActionInputDTO{
			{Name: "repo", Kind: "string", Required: true},
			{Name: "mode", Kind: "enum", Required: true, Options: []string{"auto", "address_all"}},
		}
	case "refactor":
		action.RequiredInputs = []server.ActionInputDTO{
			{Name: "repo", Kind: "string", Required: false},
			{Name: "prompt", Kind: "string", Required: true, MaxLength: server.MaxActionTextBytes},
			{Name: "pipeline", Kind: "enum", Required: false, Options: []string{"medium", "large", "moonshot"}},
		}
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Published", CurrentPhase: "publish", CreatedAt: time.Now(), Repos: []string{"api", "web"}},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Published", CurrentPhase: "publish", Repos: []string{"api", "web"}},
			RepoStatus: []server.RepoStatusDTO{
				{Name: "api", Publishable: true},
				{Name: "web", Publishable: true},
			},
			Actions: []server.ActionDTO{action},
		}},
		startRebaseAccepted:         accepted,
		startTweakAccepted:          accepted,
		startReviewCommentsAccepted: accepted,
		startRefactorAccepted:       accepted,
	}
	return client
}

type apiTestActionResponse struct {
	FeatureID    string
	Result       string
	Decision     string
	SessionID    string
	RequestID    string
	Repo         string
	Mode         string
	CycleType    string
	TargetPhase  string
	RoadmapPhase int
	Muted        bool
	HadChanges   bool
}

func (r apiTestActionResponse) result(defaultResult string) string {
	if r.Result != "" {
		return r.Result
	}
	return defaultResult
}

func (r apiTestActionResponse) featureID(defaultFeatureID string) string {
	if r.FeatureID != "" {
		return r.FeatureID
	}
	return defaultFeatureID
}

type fakeTUIAPIClient struct {
	calls                         []string
	features                      server.FeatureListResponse
	runtime                       server.RuntimeConfigResponse
	catalog                       server.ModelCatalogResponse
	prompts                       server.PromptSnapshotResponse
	permissions                   server.PermissionSnapshotResponse
	sessions                      server.SessionListResponse
	recovery                      server.RecoverySnapshotResponse
	livePreview                   server.LivePreviewResponse
	livePreviewsByID              map[string]server.LivePreviewResponse
	livePreviewFeatureIDs         []string
	executeRecoveryAccepted       apiTestActionResponse
	executeRecoveryErr            error
	executeRecoverySnapshotIDs    []string
	executeRecoveryRequests       []server.RecoveryActionRequest
	refreshSnapshot               server.RefreshSnapshot
	refreshSignals                []server.RefreshSignal
	startAccepted                 apiTestActionResponse
	startErr                      error
	startFeatureIDs               []string
	createAccepted                apiTestActionResponse
	createErr                     error
	createRequests                []server.CreateFeatureRequest
	resumeAccepted                apiTestActionResponse
	resumeErr                     error
	resumeFeatureIDs              []string
	restartAccepted               apiTestActionResponse
	restartErr                    error
	restartFeatureIDs             []string
	stopAccepted                  apiTestActionResponse
	stopErr                       error
	stopFeatureIDs                []string
	deleteAccepted                apiTestActionResponse
	deleteErr                     error
	deleteFeatureIDs              []string
	publishAccepted               apiTestActionResponse
	publishErr                    error
	publishFeatureIDs             []string
	publishRequests               []server.PublishFeatureRequest
	mergeAccepted                 apiTestActionResponse
	mergeErr                      error
	mergeFeatureIDs               []string
	retryAccepted                 apiTestActionResponse
	retryErr                      error
	retryFeatureIDs               []string
	markDoneAccepted              apiTestActionResponse
	markDoneErr                   error
	markDoneFeatureIDs            []string
	cleanupAccepted               apiTestActionResponse
	cleanupErr                    error
	cleanupFeatureIDs             []string
	cleanupRequests               []server.CleanupActionRequest
	startTweakAccepted            apiTestActionResponse
	startTweakErr                 error
	startTweakFeatureIDs          []string
	startTweakRequests            []server.TweakActionRequest
	finishTweakAccepted           apiTestActionResponse
	finishTweakErr                error
	finishTweakFeatureIDs         []string
	finishTweakRequests           []server.TweakFinishRequest
	startRebaseAccepted           apiTestActionResponse
	startRebaseErr                error
	startRebaseFeatureIDs         []string
	startRebaseRequests           []server.RebaseActionRequest
	startRefactorAccepted         apiTestActionResponse
	startRefactorErr              error
	startRefactorFeatureIDs       []string
	startRefactorRequests         []server.RefactorActionRequest
	restartRefactorAccepted       apiTestActionResponse
	restartRefactorErr            error
	restartRefactorFeatureIDs     []string
	restartRefactorRequests       []server.RefactorActionRequest
	rewindAccepted                apiTestActionResponse
	rewindErr                     error
	rewindFeatureIDs              []string
	rewindRequests                []server.RewindFeatureRequest
	featureConfig                 server.FeatureConfigResponse
	featureConfigErr              error
	featureConfigIDs              []string
	updateFeatureConfigAccepted   apiTestActionResponse
	updateFeatureConfigErr        error
	updateFeatureConfigIDs        []string
	updateFeatureConfigRequests   []server.FeatureConfigMutationRequest
	updateRuntimeConfigAccepted   apiTestActionResponse
	updateRuntimeConfigErr        error
	updateRuntimeConfigRequests   []server.RuntimeConfigMutationRequest
	needUserInputAccepted         apiTestActionResponse
	needUserInputErr              error
	needUserInputFeatureIDs       []string
	needUserInputRequests         []server.NeedUserInputDecisionRequest
	needUserInputDraftAccepted    apiTestActionResponse
	needUserInputDraftErr         error
	needUserInputDraftFeatureIDs  []string
	needUserInputDraftRequests    []server.NeedUserInputDraftRequest
	toggleInputAccepted           apiTestActionResponse
	toggleInputErr                error
	toggleInputFeatureIDs         []string
	reviewAccepted                apiTestActionResponse
	reviewErr                     error
	reviewFeatureIDs              []string
	reviewRequests                []server.ReviewDecisionRequest
	reviewCommentsResponse        server.ReviewCommentsFetchResponse
	reviewCommentsErr             error
	reviewCommentsFeatureIDs      []string
	reviewCommentsFetchRequests   []server.ReviewCommentsFetchRequest
	startReviewCommentsAccepted   apiTestActionResponse
	startReviewCommentsErr        error
	startReviewCommentsFeatureIDs []string
	startReviewCommentsRequests   []server.ReviewCommentsActionRequest
	permissionAccepted            apiTestActionResponse
	permissionErr                 error
	permissionAnswers             []server.PermissionAnswerRequest
	helpAccepted                  apiTestActionResponse
	helpErr                       error
	helpRequests                  []server.HelpAnswerRequest
	askUserAccepted               apiTestActionResponse
	askUserErr                    error
	askUserAnswers                []server.AskUserAnswerRequest
	shutdownAccepted              apiTestActionResponse
	shutdownErr                   error
	shutdownCalls                 int
	detail                        server.FeatureDetailResponse
	detailsByID                   map[string]server.FeatureDetailResponse
	detailFeatureIDs              []string
	sessionDetail                 server.SessionDetailResponse
	sessionDetailsByID            map[string]server.SessionDetailResponse
	sessionDetailIDs              []string
	transcript                    server.TranscriptResponse
	transcriptsByID               map[string]server.TranscriptResponse
	transcriptSessionIDs          []string
	transcriptQueries             []server.CursorQuery
	artifactList                  server.ArtifactListResponse
	artifactListByRun             map[int]server.ArtifactListResponse
	artifactListFeatureIDs        []string
	artifactListRunNumbers        []int
	artifactContent               server.TextContentResponse
	artifactContentByID           map[string]server.TextContentResponse
	artifactContentFeatureIDs     []string
	artifactContentRunNumbers     []int
	artifactContentIDs            []string
	artifactContentQueries        []server.TextQuery
	logContent                    server.TextContentResponse
	logContentByID                map[string]server.TextContentResponse
	logContentFeatureIDs          []string
	logContentRunNumbers          []int
	logContentIDs                 []string
	logContentQueries             []server.TextQuery
}

func (f *fakeTUIAPIClient) Features(context.Context) (server.FeatureListResponse, error) {
	f.calls = append(f.calls, "Features")
	return f.features, nil
}

func (f *fakeTUIAPIClient) RuntimeConfig(context.Context) (server.RuntimeConfigResponse, error) {
	f.calls = append(f.calls, "RuntimeConfig")
	return f.runtime, nil
}

func (f *fakeTUIAPIClient) ModelCatalog(context.Context) (server.ModelCatalogResponse, error) {
	f.calls = append(f.calls, "ModelCatalog")
	return f.catalog, nil
}

func (f *fakeTUIAPIClient) Prompts(context.Context) (server.PromptSnapshotResponse, error) {
	f.calls = append(f.calls, "Prompts")
	return f.prompts, nil
}

func (f *fakeTUIAPIClient) Permissions(context.Context) (server.PermissionSnapshotResponse, error) {
	f.calls = append(f.calls, "Permissions")
	return f.permissions, nil
}

func (f *fakeTUIAPIClient) Sessions(context.Context) (server.SessionListResponse, error) {
	f.calls = append(f.calls, "Sessions")
	return f.sessions, nil
}

func (f *fakeTUIAPIClient) Recovery(context.Context) (server.RecoverySnapshotResponse, error) {
	f.calls = append(f.calls, "Recovery")
	return f.recovery, nil
}

func (f *fakeTUIAPIClient) SessionDetail(_ context.Context, sessionID string) (server.SessionDetailResponse, error) {
	f.calls = append(f.calls, "SessionDetail")
	f.sessionDetailIDs = append(f.sessionDetailIDs, sessionID)
	if detail, ok := f.sessionDetailsByID[sessionID]; ok {
		return detail, nil
	}
	if f.sessionDetail.Session.ID == "" {
		f.sessionDetail.Session.ID = sessionID
	}
	return f.sessionDetail, nil
}

func (f *fakeTUIAPIClient) Transcript(_ context.Context, sessionID string, query server.CursorQuery) (server.TranscriptResponse, error) {
	f.calls = append(f.calls, "Transcript")
	f.transcriptSessionIDs = append(f.transcriptSessionIDs, sessionID)
	f.transcriptQueries = append(f.transcriptQueries, query)
	if transcript, ok := f.transcriptsByID[sessionID]; ok {
		return transcript, nil
	}
	return f.transcript, nil
}

func (f *fakeTUIAPIClient) ArtifactList(_ context.Context, featureID string, runNumber int) (server.ArtifactListResponse, error) {
	f.calls = append(f.calls, "ArtifactList")
	f.artifactListFeatureIDs = append(f.artifactListFeatureIDs, featureID)
	f.artifactListRunNumbers = append(f.artifactListRunNumbers, runNumber)
	if artifacts, ok := f.artifactListByRun[runNumber]; ok {
		return artifacts, nil
	}
	return f.artifactList, nil
}

func (f *fakeTUIAPIClient) ArtifactContent(_ context.Context, featureID string, runNumber int, artifactID string, query server.TextQuery) (server.TextContentResponse, error) {
	f.calls = append(f.calls, "ArtifactContent")
	f.artifactContentFeatureIDs = append(f.artifactContentFeatureIDs, featureID)
	f.artifactContentRunNumbers = append(f.artifactContentRunNumbers, runNumber)
	f.artifactContentIDs = append(f.artifactContentIDs, artifactID)
	f.artifactContentQueries = append(f.artifactContentQueries, query)
	if content, ok := f.artifactContentByID[artifactID]; ok {
		return content, nil
	}
	return f.artifactContent, nil
}

func (f *fakeTUIAPIClient) LogContent(_ context.Context, featureID string, runNumber int, logID string, query server.TextQuery) (server.TextContentResponse, error) {
	f.calls = append(f.calls, "LogContent")
	f.logContentFeatureIDs = append(f.logContentFeatureIDs, featureID)
	f.logContentRunNumbers = append(f.logContentRunNumbers, runNumber)
	f.logContentIDs = append(f.logContentIDs, logID)
	f.logContentQueries = append(f.logContentQueries, query)
	if content, ok := f.logContentByID[logID]; ok {
		return content, nil
	}
	return f.logContent, nil
}

func (f *fakeTUIAPIClient) LivePreview(_ context.Context, featureID string) (server.LivePreviewResponse, error) {
	f.calls = append(f.calls, "LivePreview")
	f.livePreviewFeatureIDs = append(f.livePreviewFeatureIDs, featureID)
	if preview, ok := f.livePreviewsByID[featureID]; ok {
		return preview, nil
	}
	return f.livePreview, nil
}

func (f *fakeTUIAPIClient) FeatureDetail(_ context.Context, featureID string) (server.FeatureDetailResponse, error) {
	f.calls = append(f.calls, "FeatureDetail")
	f.detailFeatureIDs = append(f.detailFeatureIDs, featureID)
	if detail, ok := f.detailsByID[featureID]; ok {
		return detail, nil
	}
	return f.detail, nil
}

func (f *fakeTUIAPIClient) CreateFeature(_ context.Context, req server.CreateFeatureRequest) (server.CreateFeatureResponse, error) {
	f.calls = append(f.calls, "CreateFeature")
	f.createRequests = append(f.createRequests, req)
	return server.CreateFeatureResponse{FeatureID: f.createAccepted.featureID("created"), Result: f.createAccepted.result("created")}, f.createErr
}

func (f *fakeTUIAPIClient) StartFeature(_ context.Context, featureID string) (server.FeatureStartResponse, error) {
	f.calls = append(f.calls, "StartFeature")
	f.startFeatureIDs = append(f.startFeatureIDs, featureID)
	return server.FeatureStartResponse{FeatureID: f.startAccepted.featureID(featureID), Result: f.startAccepted.result("started")}, f.startErr
}

func (f *fakeTUIAPIClient) ResumeFeature(_ context.Context, featureID string) (server.FeatureStartResponse, error) {
	f.calls = append(f.calls, "ResumeFeature")
	f.resumeFeatureIDs = append(f.resumeFeatureIDs, featureID)
	return server.FeatureStartResponse{FeatureID: f.resumeAccepted.featureID(featureID), Result: f.resumeAccepted.result("resumed")}, f.resumeErr
}

func (f *fakeTUIAPIClient) RestartFeature(_ context.Context, featureID string, _ server.RestartFeatureRequest) (server.FeatureRestartResponse, error) {
	f.calls = append(f.calls, "RestartFeature")
	f.restartFeatureIDs = append(f.restartFeatureIDs, featureID)
	return server.FeatureRestartResponse{FeatureID: f.restartAccepted.featureID(featureID), Result: f.restartAccepted.result("restarted")}, f.restartErr
}

func (f *fakeTUIAPIClient) StopFeature(_ context.Context, featureID string) (server.FeatureStopResponse, error) {
	f.calls = append(f.calls, "StopFeature")
	f.stopFeatureIDs = append(f.stopFeatureIDs, featureID)
	return server.FeatureStopResponse{FeatureID: f.stopAccepted.featureID(featureID), Result: f.stopAccepted.result("stopped")}, f.stopErr
}

func (f *fakeTUIAPIClient) DeleteFeature(_ context.Context, featureID string) (server.DeleteFeatureResponse, error) {
	f.calls = append(f.calls, "DeleteFeature")
	f.deleteFeatureIDs = append(f.deleteFeatureIDs, featureID)
	return server.DeleteFeatureResponse{FeatureID: f.deleteAccepted.featureID(featureID), Result: f.deleteAccepted.result("deleted")}, f.deleteErr
}

func (f *fakeTUIAPIClient) PublishFeature(_ context.Context, featureID string, req server.PublishFeatureRequest) (server.PublishFeatureResponse, error) {
	f.calls = append(f.calls, "PublishFeature")
	f.publishFeatureIDs = append(f.publishFeatureIDs, featureID)
	f.publishRequests = append(f.publishRequests, req)
	return server.PublishFeatureResponse{FeatureID: f.publishAccepted.featureID(featureID), Result: f.publishAccepted.result("published")}, f.publishErr
}

func (f *fakeTUIAPIClient) MergeFeature(_ context.Context, featureID string) (server.MergeFeatureResponse, error) {
	f.calls = append(f.calls, "MergeFeature")
	f.mergeFeatureIDs = append(f.mergeFeatureIDs, featureID)
	return server.MergeFeatureResponse{FeatureID: f.mergeAccepted.featureID(featureID), Result: f.mergeAccepted.result("merged")}, f.mergeErr
}

func (f *fakeTUIAPIClient) RetryFeature(_ context.Context, featureID string) (server.RetryFeatureResponse, error) {
	f.calls = append(f.calls, "RetryFeature")
	f.retryFeatureIDs = append(f.retryFeatureIDs, featureID)
	return server.RetryFeatureResponse{FeatureID: f.retryAccepted.featureID(featureID), Result: f.retryAccepted.result("retried")}, f.retryErr
}

func (f *fakeTUIAPIClient) MarkDone(_ context.Context, featureID string) (server.MarkDoneResponse, error) {
	f.calls = append(f.calls, "MarkDone")
	f.markDoneFeatureIDs = append(f.markDoneFeatureIDs, featureID)
	return server.MarkDoneResponse{FeatureID: f.markDoneAccepted.featureID(featureID), Result: f.markDoneAccepted.result("marked_done")}, f.markDoneErr
}

func (f *fakeTUIAPIClient) CleanupFeature(_ context.Context, featureID string, req server.CleanupActionRequest) (server.CleanupFeatureResponse, error) {
	f.calls = append(f.calls, "CleanupFeature")
	f.cleanupFeatureIDs = append(f.cleanupFeatureIDs, featureID)
	f.cleanupRequests = append(f.cleanupRequests, req)
	return server.CleanupFeatureResponse{FeatureID: f.cleanupAccepted.featureID(featureID), Result: f.cleanupAccepted.result("cleaned"), Target: req.Target}, f.cleanupErr
}

func (f *fakeTUIAPIClient) StartTweak(_ context.Context, featureID string, req server.TweakActionRequest) (server.TweakStartResponse, error) {
	f.calls = append(f.calls, "StartTweak")
	f.startTweakFeatureIDs = append(f.startTweakFeatureIDs, featureID)
	f.startTweakRequests = append(f.startTweakRequests, req)
	return server.TweakStartResponse{FeatureID: f.startTweakAccepted.featureID(featureID), Result: f.startTweakAccepted.result("started"), CycleType: f.startTweakAccepted.CycleType}, f.startTweakErr
}

func (f *fakeTUIAPIClient) FinishTweak(_ context.Context, featureID string, req server.TweakFinishRequest) (server.TweakFinishResponse, error) {
	f.calls = append(f.calls, "FinishTweak")
	f.finishTweakFeatureIDs = append(f.finishTweakFeatureIDs, featureID)
	f.finishTweakRequests = append(f.finishTweakRequests, req)
	return server.TweakFinishResponse{FeatureID: f.finishTweakAccepted.featureID(featureID), Result: f.finishTweakAccepted.result("finished"), Decision: req.Decision, HadChanges: req.HadChanges}, f.finishTweakErr
}

func (f *fakeTUIAPIClient) StartRebase(_ context.Context, featureID string, req server.RebaseActionRequest) (server.RebaseStartResponse, error) {
	f.calls = append(f.calls, "StartRebase")
	f.startRebaseFeatureIDs = append(f.startRebaseFeatureIDs, featureID)
	f.startRebaseRequests = append(f.startRebaseRequests, req)
	return server.RebaseStartResponse{FeatureID: f.startRebaseAccepted.featureID(featureID), Result: f.startRebaseAccepted.result("started"), Repo: req.Repo, CycleType: f.startRebaseAccepted.CycleType}, f.startRebaseErr
}

func (f *fakeTUIAPIClient) StartRefactor(_ context.Context, featureID string, req server.RefactorActionRequest) (server.RefactorStartResponse, error) {
	f.calls = append(f.calls, "StartRefactor")
	f.startRefactorFeatureIDs = append(f.startRefactorFeatureIDs, featureID)
	f.startRefactorRequests = append(f.startRefactorRequests, req)
	return server.RefactorStartResponse{FeatureID: f.startRefactorAccepted.featureID(featureID), Result: f.startRefactorAccepted.result("started"), Repo: req.Repo, CycleType: f.startRefactorAccepted.CycleType, Pipeline: string(req.Pipeline)}, f.startRefactorErr
}

func (f *fakeTUIAPIClient) RestartRefactor(_ context.Context, featureID string, req server.RefactorActionRequest) (server.RefactorRestartResponse, error) {
	f.calls = append(f.calls, "RestartRefactor")
	f.restartRefactorFeatureIDs = append(f.restartRefactorFeatureIDs, featureID)
	f.restartRefactorRequests = append(f.restartRefactorRequests, req)
	return server.RefactorRestartResponse{FeatureID: f.restartRefactorAccepted.featureID(featureID), Result: f.restartRefactorAccepted.result("restarted"), Repo: req.Repo, CycleType: f.restartRefactorAccepted.CycleType, Pipeline: string(req.Pipeline)}, f.restartRefactorErr
}

func (f *fakeTUIAPIClient) RewindFeature(_ context.Context, featureID string, req server.RewindFeatureRequest) (server.RewindFeatureResponse, error) {
	f.calls = append(f.calls, "RewindFeature")
	f.rewindFeatureIDs = append(f.rewindFeatureIDs, featureID)
	f.rewindRequests = append(f.rewindRequests, req)
	return server.RewindFeatureResponse{FeatureID: f.rewindAccepted.featureID(featureID), Result: f.rewindAccepted.result("rewound"), TargetPhase: req.TargetPhase, RoadmapPhase: req.RoadmapPhase}, f.rewindErr
}

func (f *fakeTUIAPIClient) FeatureConfig(_ context.Context, featureID string) (server.FeatureConfigResponse, error) {
	f.calls = append(f.calls, "FeatureConfig")
	f.featureConfigIDs = append(f.featureConfigIDs, featureID)
	return f.featureConfig, f.featureConfigErr
}

func (f *fakeTUIAPIClient) UpdateFeatureConfig(_ context.Context, featureID string, req server.FeatureConfigMutationRequest) (server.FeatureConfigUpdateResponse, error) {
	f.calls = append(f.calls, "UpdateFeatureConfig")
	f.updateFeatureConfigIDs = append(f.updateFeatureConfigIDs, featureID)
	f.updateFeatureConfigRequests = append(f.updateFeatureConfigRequests, req)
	return server.FeatureConfigUpdateResponse{FeatureID: f.updateFeatureConfigAccepted.featureID(featureID), Result: f.updateFeatureConfigAccepted.result("updated")}, f.updateFeatureConfigErr
}

func (f *fakeTUIAPIClient) UpdateRuntimeConfig(_ context.Context, req server.RuntimeConfigMutationRequest) (server.RuntimeConfigUpdateResponse, error) {
	f.calls = append(f.calls, "UpdateRuntimeConfig")
	f.updateRuntimeConfigRequests = append(f.updateRuntimeConfigRequests, req)
	if req.WorkspaceRoots != nil {
		f.runtime.WorkspaceRoots = append([]string(nil), (*req.WorkspaceRoots)...)
		f.runtime.Repos = testRuntimeConfigRepos(f.runtime.Repos, f.runtime.WorkspaceRoots)
	}
	return server.RuntimeConfigUpdateResponse{Result: f.updateRuntimeConfigAccepted.result("updated")}, f.updateRuntimeConfigErr
}

func testRuntimeConfigRepos(existing []server.ConfigRepoDTO, workspaceRoots []string) []server.ConfigRepoDTO {
	cfg := &config.Config{
		Repos:          make(map[string]config.RepoConfig, len(existing)),
		WorkspaceRoots: append([]string(nil), workspaceRoots...),
	}
	for _, repo := range existing {
		if repo.Name == "" {
			continue
		}
		cfg.Repos[repo.Name] = config.RepoConfig{
			Path:          repo.Path,
			PipelineGates: copyConfigPipelineGates(repo.PipelineGates),
		}
	}
	config.DiscoverReposFromRoots(cfg)
	repos := make([]server.ConfigRepoDTO, 0, len(cfg.Repos)+len(cfg.DiscoveredRepos))
	for name, repo := range cfg.Repos {
		repos = append(repos, server.ConfigRepoDTO{Name: name, Path: repo.Path, PipelineGates: copyConfigPipelineGates(repo.PipelineGates)})
	}
	for name, repo := range cfg.DiscoveredRepos {
		if _, ok := cfg.Repos[name]; ok {
			continue
		}
		repos = append(repos, server.ConfigRepoDTO{Name: name, Path: repo.Path, PipelineGates: copyConfigPipelineGates(repo.PipelineGates)})
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos
}

func (f *fakeTUIAPIClient) ExecuteRecovery(_ context.Context, req server.RecoveryActionRequest) (server.RecoveryActionResponse, error) {
	f.calls = append(f.calls, "ExecuteRecovery")
	f.executeRecoverySnapshotIDs = append(f.executeRecoverySnapshotIDs, req.SnapshotID)
	f.executeRecoveryRequests = append(f.executeRecoveryRequests, req)
	return server.RecoveryActionResponse{Result: f.executeRecoveryAccepted.result("executed")}, f.executeRecoveryErr
}

func (f *fakeTUIAPIClient) NeedUserInputDecision(_ context.Context, featureID string, req server.NeedUserInputDecisionRequest) (server.NeedUserInputDecisionResponse, error) {
	f.calls = append(f.calls, "NeedUserInputDecision")
	f.needUserInputFeatureIDs = append(f.needUserInputFeatureIDs, featureID)
	f.needUserInputRequests = append(f.needUserInputRequests, req)
	return server.NeedUserInputDecisionResponse{FeatureID: f.needUserInputAccepted.featureID(featureID), Decision: req.Decision, Result: f.needUserInputAccepted.result("decided")}, f.needUserInputErr
}

func (f *fakeTUIAPIClient) DraftNeedUserInputAnswers(_ context.Context, featureID string, req server.NeedUserInputDraftRequest) (server.NeedUserInputDraftResponse, error) {
	f.calls = append(f.calls, "DraftNeedUserInputAnswers")
	f.needUserInputDraftFeatureIDs = append(f.needUserInputDraftFeatureIDs, featureID)
	f.needUserInputDraftRequests = append(f.needUserInputDraftRequests, req)
	return server.NeedUserInputDraftResponse{FeatureID: f.needUserInputDraftAccepted.featureID(featureID), Result: f.needUserInputDraftAccepted.result("drafted")}, f.needUserInputDraftErr
}

func (f *fakeTUIAPIClient) ToggleInputNotifications(_ context.Context, featureID string) (server.InputNotificationsToggleResponse, error) {
	f.calls = append(f.calls, "ToggleInputNotifications")
	f.toggleInputFeatureIDs = append(f.toggleInputFeatureIDs, featureID)
	return server.InputNotificationsToggleResponse{FeatureID: f.toggleInputAccepted.featureID(featureID), Result: f.toggleInputAccepted.result("toggled"), Muted: f.toggleInputAccepted.Muted}, f.toggleInputErr
}

func (f *fakeTUIAPIClient) ReviewDecision(_ context.Context, featureID string, req server.ReviewDecisionRequest) (server.ReviewDecisionResponse, error) {
	f.calls = append(f.calls, "ReviewDecision")
	f.reviewFeatureIDs = append(f.reviewFeatureIDs, featureID)
	f.reviewRequests = append(f.reviewRequests, req)
	return server.ReviewDecisionResponse{FeatureID: f.reviewAccepted.featureID(featureID), Decision: req.Decision, Result: f.reviewAccepted.result("submitted")}, f.reviewErr
}

func (f *fakeTUIAPIClient) FetchReviewComments(_ context.Context, featureID string, req server.ReviewCommentsFetchRequest) (server.ReviewCommentsFetchResponse, error) {
	f.calls = append(f.calls, "FetchReviewComments")
	f.reviewCommentsFeatureIDs = append(f.reviewCommentsFeatureIDs, featureID)
	f.reviewCommentsFetchRequests = append(f.reviewCommentsFetchRequests, req)
	return f.reviewCommentsResponse, f.reviewCommentsErr
}

func (f *fakeTUIAPIClient) StartReviewComments(_ context.Context, featureID string, req server.ReviewCommentsActionRequest) (server.ReviewCommentsStartResponse, error) {
	f.calls = append(f.calls, "StartReviewComments")
	f.startReviewCommentsFeatureIDs = append(f.startReviewCommentsFeatureIDs, featureID)
	f.startReviewCommentsRequests = append(f.startReviewCommentsRequests, req)
	return server.ReviewCommentsStartResponse{FeatureID: f.startReviewCommentsAccepted.featureID(featureID), Result: f.startReviewCommentsAccepted.result("started"), Repo: req.Repo, Mode: req.Mode, CycleType: f.startReviewCommentsAccepted.CycleType}, f.startReviewCommentsErr
}

func (f *fakeTUIAPIClient) AnswerPermission(_ context.Context, req server.PermissionAnswerRequest) (server.PermissionAnswerResponse, error) {
	f.calls = append(f.calls, "AnswerPermission")
	f.permissionAnswers = append(f.permissionAnswers, req)
	return server.PermissionAnswerResponse{SessionID: req.SessionID, RequestID: req.RequestID, Decision: req.Decision, Result: f.permissionAccepted.result("answered")}, f.permissionErr
}

func (f *fakeTUIAPIClient) SendHelp(_ context.Context, req server.HelpAnswerRequest) (server.HelpSendResponse, error) {
	f.calls = append(f.calls, "SendHelp")
	f.helpRequests = append(f.helpRequests, req)
	return server.HelpSendResponse{FeatureID: req.FeatureID, SessionID: req.SessionID, Result: f.helpAccepted.result("sent")}, f.helpErr
}

func (f *fakeTUIAPIClient) AnswerAskUser(_ context.Context, req server.AskUserAnswerRequest) (server.AskUserAnswerResponse, error) {
	f.calls = append(f.calls, "AnswerAskUser")
	f.askUserAnswers = append(f.askUserAnswers, req)
	return server.AskUserAnswerResponse{SessionID: req.SessionID, RequestID: req.RequestID, Result: f.askUserAccepted.result("answered")}, f.askUserErr
}

func (f *fakeTUIAPIClient) Shutdown(context.Context) (server.ShutdownResponse, error) {
	f.calls = append(f.calls, "Shutdown")
	f.shutdownCalls++
	return server.ShutdownResponse{Result: f.shutdownAccepted.result("shutdown_scheduled")}, f.shutdownErr
}

func (f *fakeTUIAPIClient) SubscribeEvents(context.Context, server.EventSubscriptionOptions) (<-chan server.RefreshSignal, <-chan error) {
	signals := make(chan server.RefreshSignal)
	errs := make(chan error)
	close(signals)
	close(errs)
	return signals, errs
}

func (f *fakeTUIAPIClient) FetchRefreshSnapshot(_ context.Context, signal server.RefreshSignal) (server.RefreshSnapshot, error) {
	f.calls = append(f.calls, "FetchRefreshSnapshot")
	f.refreshSignals = append(f.refreshSignals, signal)
	return f.refreshSnapshot, nil
}

func countString(values []string, needle string) int {
	count := 0
	for _, value := range values {
		if value == needle {
			count++
		}
	}
	return count
}
