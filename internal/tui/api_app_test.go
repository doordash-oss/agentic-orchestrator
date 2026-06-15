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
		operations: server.OperationSnapshotResponse{Operations: []server.OperationDTO{
			{ID: "op-1", Kind: "feature.start", Status: server.OperationStatusRunning, Target: server.OperationTarget{FeatureID: "active"}},
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

	if got, want := strings.Join(client.calls, ","), "Features,RuntimeConfig,ModelCatalog,Prompts,Permissions,Sessions,Operations,Recovery,FeatureDetail,LivePreview"; got != want {
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
	for _, want := range []string{"Client cutover", "Implementing", "2 attention", "codex", "sess-1", "Implement", "running", "42%", "op-1", "Render selected feature detail from REST.", "roadmap", "agentic-orchestrator", "feature.stop", "feature.publish disabled", "$12.34", "Need user input"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	for _, leaked := range []string{runtime.RuntimeDir, runtime.StateDir, runtime.Config} {
		if leaked != "" && strings.Contains(view, leaked) {
			t.Fatalf("API app View() leaked runtime path %q in:\n%s", leaked, view)
		}
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
		"Agentico",
		"Workspace: agentic-orchestrator",
		"[n] New",
		"[v] Attach/Live",
		"[h] Chat/Help",
		"[w] Workspace",
		"Attach / Live Preview",
		"Config",
		"Runtime",
		"Review",
		"Tweak",
		"Refactor",
		"Restart",
		"Rewind",
		"Publish",
		"Rebase",
		"Merge",
		"Done",
		"Retry",
		"Stop",
		"Finish Tweak",
		"Clean",
		"Delete",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app production surface missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Agentico API Client") {
		t.Fatalf("API app View() exposed reduced-client title:\n%s", view)
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
		executeRecoveryAccepted: server.OperationAcceptedResponse{
			OperationID: "op-recovery",
			Status:      server.OperationStatusQueued,
		},
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
	snapshot := submitted.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-recovery" || snapshot.Operations[0].Kind != "recovery.execute" {
		t.Fatalf("Operations after recovery execute = %+v, want accepted recovery operation", snapshot.Operations)
	}
	if strings.Contains(stripANSI(submitted.View().Content), "Session Recovery") {
		t.Fatalf("API app View() still shows recovery panel after accepted submit:\n%s", stripANSI(submitted.View().Content))
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

	initial := stripANSI(app.View().Content)
	for _, want := range []string{"Sessions", "sess-1", "Implement", "running", "10%"} {
		if !strings.Contains(initial, want) {
			t.Fatalf("initial API app View() missing %q in:\n%s", want, initial)
		}
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
	view := stripANSI(refreshed.View().Content)
	for _, want := range []string{"sess-1", "completed", "37%", "log"} {
		if !strings.Contains(view, want) {
			t.Fatalf("refreshed API app View() missing %q in:\n%s", want, view)
		}
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
	for _, want := range []string{"Live Preview", "Using Bash...", "sess-live", "Context: 42%", "Cost: $0.42", "AskUserQuestion: Pick the cutover path", "Ready to patch live preview"} {
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
	view := stripANSI(app.View().Content)
	for _, want := range []string{"Transcript", "Patch transcript continuation", "Bash"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
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
	for _, want := range []string{"Live Preview", "Using Bash...", "Context: 57%", "Cost: $1.25", "Patched through REST snapshot"} {
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
		operations:  server.OperationSnapshotResponse{},
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
		operations: server.OperationSnapshotResponse{Operations: []server.OperationDTO{
			{ID: "op-1", Kind: "feature.start", Status: server.OperationStatusRunning, Target: server.OperationTarget{FeatureID: "active"}},
		}},
		refreshSnapshot: server.RefreshSnapshot{
			Feature: &server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
				FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Published", CurrentPhase: "publish", CreatedAt: time.Now()},
			}},
			Operations: &server.OperationSnapshotResponse{Operations: []server.OperationDTO{
				{ID: "op-1", Kind: "feature.start", Status: server.OperationStatusSucceeded, Target: server.OperationTarget{FeatureID: "active"}},
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
		Resource:         server.ResourceDTO{Type: "operation", ID: "op-1", FeatureID: "active"},
		SnapshotRequired: true,
	}
	msg := app.fetchRefreshSnapshotCmd(signal)()
	model, _ := app.Update(msg)
	recovered := model.(APIAppModel)

	if got := recovered.SelectedFeatureID(); got != "active" {
		t.Fatalf("SelectedFeatureID() after reconnect snapshot = %q, want active", got)
	}
	snapshot := recovered.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].Status != string(server.OperationStatusSucceeded) {
		t.Fatalf("Operations after reconnect snapshot = %+v, want op-1 succeeded", snapshot.Operations)
	}
	if len(client.refreshSignals) != 1 || client.refreshSignals[0].Resource.ID != "op-1" {
		t.Fatalf("refresh signals = %+v, want targeted op-1 refresh", client.refreshSignals)
	}
	if got := countString(client.calls, "Sessions"); got != initialSessionCalls {
		t.Fatalf("Sessions calls after operation refresh = %d, want unchanged %d", got, initialSessionCalls)
	}
	if got := countString(client.calls, "Transcript"); got != initialTranscriptCalls {
		t.Fatalf("Transcript calls after operation refresh = %d, want unchanged %d", got, initialTranscriptCalls)
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
		operations:  server.OperationSnapshotResponse{},
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

func TestAPIAppModelStartSelectedFeatureUsesRESTMutationAndTracksOperation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "queued", Name: "Queued work", Slug: "queued-work", Status: "Created", CurrentPhase: "research", CreatedAt: time.Now()},
		}},
		startAccepted: server.OperationAcceptedResponse{
			OperationID: "op-start",
			Status:      server.OperationStatusQueued,
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want start mutation command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	started := model.(APIAppModel)

	if got := strings.Join(client.startFeatureIDs, ","); got != "queued" {
		t.Fatalf("StartFeature calls = %q, want queued", got)
	}
	snapshot := started.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-start" || snapshot.Operations[0].Kind != "feature.start" {
		t.Fatalf("Operations after start = %+v, want accepted start operation", snapshot.Operations)
	}
	view := stripANSI(started.View().Content)
	for _, want := range []string{"Accepted feature.start operation op-start", "op-start", "queued"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelResumeSelectedFeatureUsesRESTMutationAndTracksOperation(t *testing.T) {
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
		resumeAccepted: server.OperationAcceptedResponse{
			OperationID: "op-resume",
			Status:      server.OperationStatusQueued,
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want resume mutation command")
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
	snapshot := resumed.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-resume" || snapshot.Operations[0].Kind != "feature.resume" {
		t.Fatalf("Operations after resume = %+v, want accepted resume operation", snapshot.Operations)
	}
	view := stripANSI(resumed.View().Content)
	for _, want := range []string{"Accepted feature.resume operation op-resume", "op-resume", "paused"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelCreateFeatureUsesRESTMutationAndTracksOperation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Defaults: config.ModelConfig{Implementation: "gpt-5.4"},
			Repos: []server.ConfigRepoDTO{
				{Name: "agentic-orchestrator", Path: "/workspace/agentic-orchestrator"},
			},
		},
		createAccepted: server.OperationAcceptedResponse{
			OperationID: "op-create",
			Status:      server.OperationStatusQueued,
		},
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
	for _, want := range []string{"New Feature", "agentic-orchestrator", "Create"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}

	for _, ch := range "API cutover regression" {
		model, cmd = creating.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		if cmd != nil {
			t.Fatalf("Update(%q) returned unexpected command while typing create name", ch)
		}
		creating = model.(APIAppModel)
	}
	model, cmd = creating.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want create mutation")
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
	if created.ShowingCreateFeaturePrompt() {
		t.Fatal("create prompt remained open after accepted create")
	}
	snapshot := created.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-create" || snapshot.Operations[0].Kind != "feature.create" {
		t.Fatalf("Operations after create = %+v, want accepted feature.create operation", snapshot.Operations)
	}
}

func TestAPIAppModelFeatureActionsConfirmBeforeRESTMutationAndTrackOperation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		key        tea.KeyPressMsg
		actionID   string
		wantKind   string
		accepted   server.OperationAcceptedResponse
		cycle      *server.CycleDTO
		disabled   bool
		assertCall func(t *testing.T, client *fakeTUIAPIClient)
	}{
		{
			name:     "merge",
			key:      tea.KeyPressMsg{Code: 'M', Text: "M"},
			actionID: "merge",
			wantKind: "feature.merge",
			accepted: server.OperationAcceptedResponse{
				OperationID: "op-merge",
				Status:      server.OperationStatusQueued,
			},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.mergeFeatureIDs, ","); got != "active" {
					t.Fatalf("MergeFeature calls = %q, want active", got)
				}
			},
		},
		{
			name:     "retry",
			key:      tea.KeyPressMsg{Code: 'R', Text: "R"},
			actionID: "retry",
			wantKind: "feature.retry",
			accepted: server.OperationAcceptedResponse{
				OperationID: "op-retry",
				Status:      server.OperationStatusQueued,
			},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.retryFeatureIDs, ","); got != "active" {
					t.Fatalf("RetryFeature calls = %q, want active", got)
				}
			},
		},
		{
			name:     "mark done",
			key:      tea.KeyPressMsg{Code: 'D', Text: "D"},
			actionID: "mark-done",
			wantKind: "feature.mark-done",
			accepted: server.OperationAcceptedResponse{
				OperationID: "op-mark-done",
				Status:      server.OperationStatusQueued,
			},
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
			accepted: server.OperationAcceptedResponse{
				OperationID: "op-rebase",
				Status:      server.OperationStatusQueued,
			},
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
			accepted: server.OperationAcceptedResponse{
				OperationID: "op-cleanup",
				Status:      server.OperationStatusQueued,
			},
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
			accepted: server.OperationAcceptedResponse{
				OperationID: "op-tweak-start",
				Status:      server.OperationStatusQueued,
			},
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
			accepted: server.OperationAcceptedResponse{
				OperationID: "op-rewind",
				Status:      server.OperationStatusQueued,
			},
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
			accepted: server.OperationAcceptedResponse{
				OperationID: "op-restart",
				Status:      server.OperationStatusQueued,
			},
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
			accepted: server.OperationAcceptedResponse{
				OperationID: "op-stop",
				Status:      server.OperationStatusQueued,
			},
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
			accepted: server.OperationAcceptedResponse{
				OperationID: "op-delete",
				Status:      server.OperationStatusQueued,
			},
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
			if view := stripANSI(confirming.View().Content); !strings.Contains(view, "Confirm "+tt.wantKind) {
				t.Fatalf("View() missing %q confirmation in:\n%s", tt.wantKind, view)
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
			snapshot := accepted.Snapshot()
			if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != tt.accepted.OperationID || snapshot.Operations[0].Kind != tt.wantKind {
				t.Fatalf("Operations after %s = %+v, want accepted %s operation", tt.name, snapshot.Operations, tt.wantKind)
			}
			view := stripANSI(accepted.View().Content)
			for _, want := range []string{"Accepted " + tt.wantKind + " operation " + tt.accepted.OperationID, tt.accepted.OperationID, "active"} {
				if !strings.Contains(view, want) {
					t.Fatalf("API app View() missing %q in:\n%s", want, view)
				}
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
		wantOperation  string
	}{
		{
			name:           "review",
			key:            tea.KeyPressMsg{Code: 'y', Text: "y"},
			wantDecision:   "final-review",
			wantHadChanges: true,
			wantOperation:  "op-tweak-review",
		},
		{
			name:           "skip_review",
			key:            tea.KeyPressMsg{Code: 'n', Text: "n"},
			wantDecision:   "skip-review",
			wantHadChanges: true,
			wantOperation:  "op-tweak-skip",
		},
		{
			name:          "restore",
			key:           tea.KeyPressMsg{Code: tea.KeyEscape},
			wantDecision:  "restore-from-review",
			wantOperation: "op-tweak-restore",
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
				finishTweakAccepted: server.OperationAcceptedResponse{
					OperationID: tt.wantOperation,
					Status:      server.OperationStatusQueued,
				},
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
			snapshot := accepted.Snapshot()
			if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != tt.wantOperation || snapshot.Operations[0].Kind != "feature.tweak.finish" {
				t.Fatalf("Operations after %s = %+v, want accepted feature.tweak.finish operation %s", tt.name, snapshot.Operations, tt.wantOperation)
			}
		})
	}
}

func TestAPIAppModelFeatureConfigEditorLoadsFromRESTAndSavesMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
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
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement"},
			Pipeline:       "large",
		}},
		featureConfig: server.FeatureConfigResponse{
			FeatureID: "active",
			Current: server.FeatureConfigDTO{
				Models:      config.ModelConfig{Research: "codex:gpt-5.4", Planning: "codex:gpt-5.4", Implementation: "codex:gpt-5.4", Review: "codex:gpt-5.4", KBBuild: "codex:gpt-5.4"},
				Inquireness: "targeted",
				Checkpoints: server.CheckpointsDTO{PlanReview: true, ManualPublish: true},
				Pipeline:    "large",
			},
			Defaults: server.FeatureConfigDTO{
				Models: config.ModelConfig{Research: "codex:gpt-5.4"},
			},
		},
		updateFeatureConfigAccepted: server.OperationAcceptedResponse{
			OperationID: "op-config",
			Status:      server.OperationStatusQueued,
		},
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
	for _, want := range []string{"Feature config", "Client cutover", "Research", "codex:gpt-5.4", "large", "targeted"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}

	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyTab})
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
	if got := client.updateFeatureConfigRequests; len(got) != 1 || got[0].Models.Research != "codex:gpt-5.5" || got[0].Pipeline != "large" || got[0].Inquireness != "targeted" || !got[0].Checkpoints.PlanReview || !got[0].Checkpoints.ManualPublish {
		t.Fatalf("UpdateFeatureConfig requests = %+v, want edited research model and preserved config axes", got)
	}
	snapshot := saved.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-config" || snapshot.Operations[0].Kind != "feature.config.update" {
		t.Fatalf("Operations after config save = %+v, want accepted feature.config.update operation", snapshot.Operations)
	}
	view = stripANSI(saved.View().Content)
	for _, want := range []string{"Accepted feature.config.update operation op-config", "op-config", "active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelRuntimeConfigEditorSavesRESTMutationAndTracksOperation(t *testing.T) {
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
		updateRuntimeConfigAccepted: server.OperationAcceptedResponse{
			OperationID: "op-runtime-config",
			Status:      server.OperationStatusQueued,
		},
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
	snapshot := saved.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-runtime-config" || snapshot.Operations[0].Kind != "runtime.config.update" {
		t.Fatalf("Operations after runtime config save = %+v, want accepted runtime.config.update operation", snapshot.Operations)
	}
	view = stripANSI(saved.View().Content)
	for _, want := range []string{"Accepted runtime.config.update operation op-runtime-config", "op-runtime-config"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelNeedUserInputDecisionUsesRESTMutationAndTracksOperation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "blocked", Name: "Blocked work", Slug: "blocked-work", Status: "NeedUserInput", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "blocked", Name: "Blocked work", Slug: "blocked-work", Status: "NeedUserInput", CurrentPhase: "implement"},
			NeedUserInput:  &server.NeedInputGateDTO{FeatureID: "blocked", Open: true, Scope: "feature", Iteration: 3},
		}},
		needUserInputAccepted: server.OperationAcceptedResponse{
			OperationID: "op-need-input",
			Status:      server.OperationStatusQueued,
		},
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
	snapshot := decided.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-need-input" || snapshot.Operations[0].Kind != "feature.need_user_input.decision" {
		t.Fatalf("Operations after need-user-input decision = %+v, want accepted decision operation", snapshot.Operations)
	}
	view = stripANSI(decided.View().Content)
	for _, want := range []string{"Accepted feature.need_user_input.decision operation op-need-input", "op-need-input", "blocked"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelPermissionAnswerUsesRESTMutationAndTracksOperation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		permissions: server.PermissionSnapshotResponse{Requests: []server.ControlRequestDTO{
			{RequestID: "perm-1", SessionID: "sess-1", FeatureID: "active", ToolName: "Bash", Status: "pending", Summary: "go test ./internal/tui"},
		}},
		permissionAccepted: server.OperationAcceptedResponse{
			OperationID: "op-permission",
			Status:      server.OperationStatusQueued,
		},
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
	snapshot := answered.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-permission" || snapshot.Operations[0].Kind != "permission.answer" || snapshot.Operations[0].FeatureID != "active" {
		t.Fatalf("Operations after permission answer = %+v, want accepted permission answer operation", snapshot.Operations)
	}
	view = stripANSI(answered.View().Content)
	for _, want := range []string{"Accepted permission.answer operation op-permission", "op-permission", "active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelHelpMessageUsesRESTMutationAndTracksOperation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		prompts: server.PromptSnapshotResponse{HelpQueue: []server.HelpQueueDTO{
			{FeatureID: "active", Question: "Which implementation path?", Pending: true},
		}},
		helpAccepted: server.OperationAcceptedResponse{
			OperationID: "op-help",
			Status:      server.OperationStatusQueued,
		},
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
	snapshot := answered.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-help" || snapshot.Operations[0].Kind != "help.send" || snapshot.Operations[0].FeatureID != "active" {
		t.Fatalf("Operations after help answer = %+v, want accepted help.send operation", snapshot.Operations)
	}
	view = stripANSI(answered.View().Content)
	for _, want := range []string{"Accepted help.send operation op-help", "op-help", "active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelAskUserAnswerUsesRESTMutationAndTracksOperation(t *testing.T) {
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
		askUserAccepted: server.OperationAcceptedResponse{
			OperationID: "op-ask",
			Status:      server.OperationStatusQueued,
		},
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
	snapshot := answered.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-ask" || snapshot.Operations[0].Kind != "ask_user.answer" || snapshot.Operations[0].FeatureID != "active" {
		t.Fatalf("Operations after ask-user answer = %+v, want accepted ask_user.answer operation", snapshot.Operations)
	}
	view = stripANSI(answered.View().Content)
	for _, want := range []string{"Accepted ask_user.answer operation op-ask", "op-ask", "active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelReviewDecisionsUseRESTMutationAndTrackOperation(t *testing.T) {
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
				detail: tt.detail,
				reviewAccepted: server.OperationAcceptedResponse{
					OperationID: "op-review",
					Status:      server.OperationStatusQueued,
				},
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
			snapshot := reviewed.Snapshot()
			if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-review" || snapshot.Operations[0].Kind != "feature.review_decision" {
				t.Fatalf("Operations after review decision = %+v, want accepted review-decision operation", snapshot.Operations)
			}
			view := stripANSI(reviewed.View().Content)
			for _, want := range []string{"Accepted feature.review_decision operation op-review", "op-review", "active"} {
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
				{ID: 101, Type: "review", Path: "internal/tui/api_app.go", Line: 42, Body: "use REST DTOs here", UserLogin: "reviewer"},
			},
		},
		startReviewCommentsAccepted: server.OperationAcceptedResponse{
			OperationID: "op-review-comments",
			Status:      server.OperationStatusQueued,
		},
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
	for _, want := range []string{"Review comments", "Active work", "agentic-orchestrator", "Mode: auto", "address_all", "@reviewer", "internal/tui/api_app.go:42", "use REST DTOs here"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}

	model, cmd = previewing.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if cmd != nil {
		t.Fatal("Update(tab) returned command before review-comments start")
	}
	modeChanged := model.(APIAppModel)
	if view := stripANSI(modeChanged.View().Content); !strings.Contains(view, "Mode: address_all") {
		t.Fatalf("API app View() did not cycle review-comments mode:\n%s", view)
	}

	model, cmd = modeChanged.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want review-comments start mutation")
	}
	msg = cmd()
	model, _ = model.(APIAppModel).Update(msg)
	started := model.(APIAppModel)

	if got := strings.Join(client.startReviewCommentsFeatureIDs, ","); got != "active" {
		t.Fatalf("StartReviewComments feature IDs = %q, want active", got)
	}
	if got := client.startReviewCommentsRequests; len(got) != 1 || got[0].Repo != "agentic-orchestrator" || got[0].Mode != "address_all" {
		t.Fatalf("StartReviewComments requests = %+v, want agentic-orchestrator address_all", got)
	}
	snapshot := started.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-review-comments" || snapshot.Operations[0].Kind != "feature.review_comments" {
		t.Fatalf("Operations after review-comments start = %+v, want accepted review-comments operation", snapshot.Operations)
	}
	view = stripANSI(started.View().Content)
	for _, want := range []string{"Accepted feature.review_comments operation op-review-comments", "op-review-comments", "active"} {
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
		startRefactorAccepted: server.OperationAcceptedResponse{
			OperationID: "op-refactor",
			Status:      server.OperationStatusQueued,
		},
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
	for _, want := range []string{"Refactor", "Active work", "agentic-orchestrator", "ctrl+s"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in refactor prompt:\n%s", want, view)
		}
	}

	for _, r := range "extract transport boundary" {
		model, cmd = refactor.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		if cmd != nil {
			t.Fatalf("Update(%q) returned command while typing refactor prompt", r)
		}
		refactor = model.(APIAppModel)
	}
	model, cmd = refactor.Update(tea.KeyPressMsg{Code: 's', Text: "s", Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("Update(ctrl+s) returned command before pipeline selection")
	}
	refactor = model.(APIAppModel)
	view = stripANSI(refactor.View().Content)
	for _, want := range []string{"Pipeline", "medium", "> large", "moonshot"} {
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
	snapshot := started.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-refactor" || snapshot.Operations[0].Kind != "feature.refactor.start" {
		t.Fatalf("Operations after refactor start = %+v, want accepted refactor operation", snapshot.Operations)
	}
	view = stripANSI(started.View().Content)
	for _, want := range []string{"Accepted feature.refactor.start operation op-refactor", "op-refactor", "active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelRefactorRestartUsesRESTMutation(t *testing.T) {
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
		restartRefactorAccepted: server.OperationAcceptedResponse{
			OperationID: "op-refactor-restart",
			Status:      server.OperationStatusQueued,
		},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'f', Text: "f", Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("Update(Ctrl+F) returned command before refactor restart prompt submit")
	}
	refactor := model.(APIAppModel)
	view := stripANSI(refactor.View().Content)
	for _, want := range []string{"Restart refactor", "Active work", "agentic-orchestrator", "ctrl+s"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in refactor restart prompt:\n%s", want, view)
		}
	}
	if len(client.restartRefactorFeatureIDs) != 0 {
		t.Fatalf("RestartRefactor calls = %v before prompt submit, want none", client.restartRefactorFeatureIDs)
	}

	for _, r := range "retry transport split" {
		model, cmd = refactor.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		if cmd != nil {
			t.Fatalf("Update(%q) returned command while typing refactor restart prompt", r)
		}
		refactor = model.(APIAppModel)
	}
	model, cmd = refactor.Update(tea.KeyPressMsg{Code: 's', Text: "s", Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("Update(ctrl+s) returned command before restart pipeline selection")
	}
	refactor = model.(APIAppModel)
	model, cmd = refactor.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want refactor restart mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	restarted := model.(APIAppModel)

	if got := strings.Join(client.restartRefactorFeatureIDs, ","); got != "active" {
		t.Fatalf("RestartRefactor feature IDs = %q, want active", got)
	}
	if got := client.restartRefactorRequests; len(got) != 1 || got[0].Repo != "agentic-orchestrator" || got[0].Prompt != "retry transport split" || got[0].Pipeline != feature.PipelineLarge {
		t.Fatalf("RestartRefactor requests = %+v, want agentic-orchestrator prompt with large pipeline", got)
	}
	if len(client.startRefactorFeatureIDs) != 0 {
		t.Fatalf("StartRefactor calls = %v, want none for restart", client.startRefactorFeatureIDs)
	}
	snapshot := restarted.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-refactor-restart" || snapshot.Operations[0].Kind != "feature.refactor.restart" {
		t.Fatalf("Operations after refactor restart = %+v, want accepted refactor restart operation", snapshot.Operations)
	}
	view = stripANSI(restarted.View().Content)
	for _, want := range []string{"Accepted feature.refactor.restart operation op-refactor-restart", "op-refactor-restart", "active"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
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
		deleteAccepted: server.OperationAcceptedResponse{
			OperationID: "op-delete",
			Status:      server.OperationStatusQueued,
		},
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
	if view := stripANSI(updated.View().Content); !strings.Contains(view, "feature.delete is unavailable") {
		t.Fatalf("View() missing disabled-action status in:\n%s", view)
	}
}

func TestAPIAppModelPublishConfirmsBeforeRESTMutationAndTracksOperation(t *testing.T) {
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
		publishAccepted: server.OperationAcceptedResponse{
			OperationID: "op-publish",
			Status:      server.OperationStatusQueued,
		},
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
	if view := stripANSI(confirming.View().Content); !strings.Contains(view, "Confirm feature.publish") {
		t.Fatalf("View() missing feature.publish confirmation in:\n%s", view)
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
	snapshot := published.Snapshot()
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != "op-publish" || snapshot.Operations[0].Kind != "feature.publish" {
		t.Fatalf("Operations after publish = %+v, want accepted publish operation", snapshot.Operations)
	}
	view := stripANSI(published.View().Content)
	for _, want := range []string{"Accepted feature.publish operation op-publish", "op-publish", "ready"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
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
	if !strings.Contains(view, "Queued detail from REST") {
		t.Fatalf("API app View() missing queued detail in:\n%s", view)
	}
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
		shutdownAccepted: server.OperationAcceptedResponse{OperationID: "op-shutdown", Status: server.OperationStatusQueued},
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
	operations                    server.OperationSnapshotResponse
	executeRecoveryAccepted       server.OperationAcceptedResponse
	executeRecoveryErr            error
	executeRecoverySnapshotIDs    []string
	executeRecoveryRequests       []server.RecoveryActionRequest
	refreshSnapshot               server.RefreshSnapshot
	refreshSignals                []server.RefreshSignal
	startAccepted                 server.OperationAcceptedResponse
	startErr                      error
	startFeatureIDs               []string
	createAccepted                server.OperationAcceptedResponse
	createErr                     error
	createRequests                []server.CreateFeatureRequest
	resumeAccepted                server.OperationAcceptedResponse
	resumeErr                     error
	resumeFeatureIDs              []string
	restartAccepted               server.OperationAcceptedResponse
	restartErr                    error
	restartFeatureIDs             []string
	stopAccepted                  server.OperationAcceptedResponse
	stopErr                       error
	stopFeatureIDs                []string
	deleteAccepted                server.OperationAcceptedResponse
	deleteErr                     error
	deleteFeatureIDs              []string
	publishAccepted               server.OperationAcceptedResponse
	publishErr                    error
	publishFeatureIDs             []string
	mergeAccepted                 server.OperationAcceptedResponse
	mergeErr                      error
	mergeFeatureIDs               []string
	retryAccepted                 server.OperationAcceptedResponse
	retryErr                      error
	retryFeatureIDs               []string
	markDoneAccepted              server.OperationAcceptedResponse
	markDoneErr                   error
	markDoneFeatureIDs            []string
	cleanupAccepted               server.OperationAcceptedResponse
	cleanupErr                    error
	cleanupFeatureIDs             []string
	cleanupRequests               []server.CleanupActionRequest
	startTweakAccepted            server.OperationAcceptedResponse
	startTweakErr                 error
	startTweakFeatureIDs          []string
	startTweakRequests            []server.TweakActionRequest
	finishTweakAccepted           server.OperationAcceptedResponse
	finishTweakErr                error
	finishTweakFeatureIDs         []string
	finishTweakRequests           []server.TweakFinishRequest
	startRebaseAccepted           server.OperationAcceptedResponse
	startRebaseErr                error
	startRebaseFeatureIDs         []string
	startRebaseRequests           []server.RebaseActionRequest
	startRefactorAccepted         server.OperationAcceptedResponse
	startRefactorErr              error
	startRefactorFeatureIDs       []string
	startRefactorRequests         []server.RefactorActionRequest
	restartRefactorAccepted       server.OperationAcceptedResponse
	restartRefactorErr            error
	restartRefactorFeatureIDs     []string
	restartRefactorRequests       []server.RefactorActionRequest
	rewindAccepted                server.OperationAcceptedResponse
	rewindErr                     error
	rewindFeatureIDs              []string
	rewindRequests                []server.RewindFeatureRequest
	featureConfig                 server.FeatureConfigResponse
	featureConfigErr              error
	featureConfigIDs              []string
	updateFeatureConfigAccepted   server.OperationAcceptedResponse
	updateFeatureConfigErr        error
	updateFeatureConfigIDs        []string
	updateFeatureConfigRequests   []server.FeatureConfigMutationRequest
	updateRuntimeConfigAccepted   server.OperationAcceptedResponse
	updateRuntimeConfigErr        error
	updateRuntimeConfigRequests   []server.RuntimeConfigMutationRequest
	needUserInputAccepted         server.OperationAcceptedResponse
	needUserInputErr              error
	needUserInputFeatureIDs       []string
	needUserInputRequests         []server.NeedUserInputDecisionRequest
	reviewAccepted                server.OperationAcceptedResponse
	reviewErr                     error
	reviewFeatureIDs              []string
	reviewRequests                []server.ReviewDecisionRequest
	reviewCommentsResponse        server.ReviewCommentsFetchResponse
	reviewCommentsErr             error
	reviewCommentsFeatureIDs      []string
	reviewCommentsFetchRequests   []server.ReviewCommentsFetchRequest
	startReviewCommentsAccepted   server.OperationAcceptedResponse
	startReviewCommentsErr        error
	startReviewCommentsFeatureIDs []string
	startReviewCommentsRequests   []server.ReviewCommentsActionRequest
	permissionAccepted            server.OperationAcceptedResponse
	permissionErr                 error
	permissionAnswers             []server.PermissionAnswerRequest
	helpAccepted                  server.OperationAcceptedResponse
	helpErr                       error
	helpRequests                  []server.HelpAnswerRequest
	askUserAccepted               server.OperationAcceptedResponse
	askUserErr                    error
	askUserAnswers                []server.AskUserAnswerRequest
	shutdownAccepted              server.OperationAcceptedResponse
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

func (f *fakeTUIAPIClient) Operations(context.Context, server.OperationQuery) (server.OperationSnapshotResponse, error) {
	f.calls = append(f.calls, "Operations")
	return f.operations, nil
}

func (f *fakeTUIAPIClient) FeatureDetail(_ context.Context, featureID string) (server.FeatureDetailResponse, error) {
	f.calls = append(f.calls, "FeatureDetail")
	f.detailFeatureIDs = append(f.detailFeatureIDs, featureID)
	if detail, ok := f.detailsByID[featureID]; ok {
		return detail, nil
	}
	return f.detail, nil
}

func (f *fakeTUIAPIClient) CreateFeature(_ context.Context, req server.CreateFeatureRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "CreateFeature")
	f.createRequests = append(f.createRequests, req)
	return f.createAccepted, f.createErr
}

func (f *fakeTUIAPIClient) StartFeature(_ context.Context, featureID string) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "StartFeature")
	f.startFeatureIDs = append(f.startFeatureIDs, featureID)
	return f.startAccepted, f.startErr
}

func (f *fakeTUIAPIClient) ResumeFeature(_ context.Context, featureID string) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "ResumeFeature")
	f.resumeFeatureIDs = append(f.resumeFeatureIDs, featureID)
	return f.resumeAccepted, f.resumeErr
}

func (f *fakeTUIAPIClient) RestartFeature(_ context.Context, featureID string, _ server.RestartFeatureRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "RestartFeature")
	f.restartFeatureIDs = append(f.restartFeatureIDs, featureID)
	return f.restartAccepted, f.restartErr
}

func (f *fakeTUIAPIClient) StopFeature(_ context.Context, featureID string) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "StopFeature")
	f.stopFeatureIDs = append(f.stopFeatureIDs, featureID)
	return f.stopAccepted, f.stopErr
}

func (f *fakeTUIAPIClient) DeleteFeature(_ context.Context, featureID string) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "DeleteFeature")
	f.deleteFeatureIDs = append(f.deleteFeatureIDs, featureID)
	return f.deleteAccepted, f.deleteErr
}

func (f *fakeTUIAPIClient) PublishFeature(_ context.Context, featureID string, _ server.PublishFeatureRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "PublishFeature")
	f.publishFeatureIDs = append(f.publishFeatureIDs, featureID)
	return f.publishAccepted, f.publishErr
}

func (f *fakeTUIAPIClient) MergeFeature(_ context.Context, featureID string) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "MergeFeature")
	f.mergeFeatureIDs = append(f.mergeFeatureIDs, featureID)
	return f.mergeAccepted, f.mergeErr
}

func (f *fakeTUIAPIClient) RetryFeature(_ context.Context, featureID string) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "RetryFeature")
	f.retryFeatureIDs = append(f.retryFeatureIDs, featureID)
	return f.retryAccepted, f.retryErr
}

func (f *fakeTUIAPIClient) MarkDone(_ context.Context, featureID string) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "MarkDone")
	f.markDoneFeatureIDs = append(f.markDoneFeatureIDs, featureID)
	return f.markDoneAccepted, f.markDoneErr
}

func (f *fakeTUIAPIClient) CleanupFeature(_ context.Context, featureID string, req server.CleanupActionRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "CleanupFeature")
	f.cleanupFeatureIDs = append(f.cleanupFeatureIDs, featureID)
	f.cleanupRequests = append(f.cleanupRequests, req)
	return f.cleanupAccepted, f.cleanupErr
}

func (f *fakeTUIAPIClient) StartTweak(_ context.Context, featureID string, req server.TweakActionRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "StartTweak")
	f.startTweakFeatureIDs = append(f.startTweakFeatureIDs, featureID)
	f.startTweakRequests = append(f.startTweakRequests, req)
	return f.startTweakAccepted, f.startTweakErr
}

func (f *fakeTUIAPIClient) FinishTweak(_ context.Context, featureID string, req server.TweakFinishRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "FinishTweak")
	f.finishTweakFeatureIDs = append(f.finishTweakFeatureIDs, featureID)
	f.finishTweakRequests = append(f.finishTweakRequests, req)
	return f.finishTweakAccepted, f.finishTweakErr
}

func (f *fakeTUIAPIClient) StartRebase(_ context.Context, featureID string, req server.RebaseActionRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "StartRebase")
	f.startRebaseFeatureIDs = append(f.startRebaseFeatureIDs, featureID)
	f.startRebaseRequests = append(f.startRebaseRequests, req)
	return f.startRebaseAccepted, f.startRebaseErr
}

func (f *fakeTUIAPIClient) StartRefactor(_ context.Context, featureID string, req server.RefactorActionRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "StartRefactor")
	f.startRefactorFeatureIDs = append(f.startRefactorFeatureIDs, featureID)
	f.startRefactorRequests = append(f.startRefactorRequests, req)
	return f.startRefactorAccepted, f.startRefactorErr
}

func (f *fakeTUIAPIClient) RestartRefactor(_ context.Context, featureID string, req server.RefactorActionRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "RestartRefactor")
	f.restartRefactorFeatureIDs = append(f.restartRefactorFeatureIDs, featureID)
	f.restartRefactorRequests = append(f.restartRefactorRequests, req)
	return f.restartRefactorAccepted, f.restartRefactorErr
}

func (f *fakeTUIAPIClient) RewindFeature(_ context.Context, featureID string, req server.RewindFeatureRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "RewindFeature")
	f.rewindFeatureIDs = append(f.rewindFeatureIDs, featureID)
	f.rewindRequests = append(f.rewindRequests, req)
	return f.rewindAccepted, f.rewindErr
}

func (f *fakeTUIAPIClient) FeatureConfig(_ context.Context, featureID string) (server.FeatureConfigResponse, error) {
	f.calls = append(f.calls, "FeatureConfig")
	f.featureConfigIDs = append(f.featureConfigIDs, featureID)
	return f.featureConfig, f.featureConfigErr
}

func (f *fakeTUIAPIClient) UpdateFeatureConfig(_ context.Context, featureID string, req server.FeatureConfigMutationRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "UpdateFeatureConfig")
	f.updateFeatureConfigIDs = append(f.updateFeatureConfigIDs, featureID)
	f.updateFeatureConfigRequests = append(f.updateFeatureConfigRequests, req)
	return f.updateFeatureConfigAccepted, f.updateFeatureConfigErr
}

func (f *fakeTUIAPIClient) UpdateRuntimeConfig(_ context.Context, req server.RuntimeConfigMutationRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "UpdateRuntimeConfig")
	f.updateRuntimeConfigRequests = append(f.updateRuntimeConfigRequests, req)
	return f.updateRuntimeConfigAccepted, f.updateRuntimeConfigErr
}

func (f *fakeTUIAPIClient) ExecuteRecovery(_ context.Context, req server.RecoveryActionRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "ExecuteRecovery")
	f.executeRecoverySnapshotIDs = append(f.executeRecoverySnapshotIDs, req.SnapshotID)
	f.executeRecoveryRequests = append(f.executeRecoveryRequests, req)
	return f.executeRecoveryAccepted, f.executeRecoveryErr
}

func (f *fakeTUIAPIClient) NeedUserInputDecision(_ context.Context, featureID string, req server.NeedUserInputDecisionRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "NeedUserInputDecision")
	f.needUserInputFeatureIDs = append(f.needUserInputFeatureIDs, featureID)
	f.needUserInputRequests = append(f.needUserInputRequests, req)
	return f.needUserInputAccepted, f.needUserInputErr
}

func (f *fakeTUIAPIClient) ReviewDecision(_ context.Context, featureID string, req server.ReviewDecisionRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "ReviewDecision")
	f.reviewFeatureIDs = append(f.reviewFeatureIDs, featureID)
	f.reviewRequests = append(f.reviewRequests, req)
	return f.reviewAccepted, f.reviewErr
}

func (f *fakeTUIAPIClient) FetchReviewComments(_ context.Context, featureID string, req server.ReviewCommentsFetchRequest) (server.ReviewCommentsFetchResponse, error) {
	f.calls = append(f.calls, "FetchReviewComments")
	f.reviewCommentsFeatureIDs = append(f.reviewCommentsFeatureIDs, featureID)
	f.reviewCommentsFetchRequests = append(f.reviewCommentsFetchRequests, req)
	return f.reviewCommentsResponse, f.reviewCommentsErr
}

func (f *fakeTUIAPIClient) StartReviewComments(_ context.Context, featureID string, req server.ReviewCommentsActionRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "StartReviewComments")
	f.startReviewCommentsFeatureIDs = append(f.startReviewCommentsFeatureIDs, featureID)
	f.startReviewCommentsRequests = append(f.startReviewCommentsRequests, req)
	return f.startReviewCommentsAccepted, f.startReviewCommentsErr
}

func (f *fakeTUIAPIClient) AnswerPermission(_ context.Context, req server.PermissionAnswerRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "AnswerPermission")
	f.permissionAnswers = append(f.permissionAnswers, req)
	return f.permissionAccepted, f.permissionErr
}

func (f *fakeTUIAPIClient) SendHelp(_ context.Context, req server.HelpAnswerRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "SendHelp")
	f.helpRequests = append(f.helpRequests, req)
	return f.helpAccepted, f.helpErr
}

func (f *fakeTUIAPIClient) AnswerAskUser(_ context.Context, req server.AskUserAnswerRequest) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "AnswerAskUser")
	f.askUserAnswers = append(f.askUserAnswers, req)
	return f.askUserAccepted, f.askUserErr
}

func (f *fakeTUIAPIClient) Shutdown(context.Context) (server.OperationAcceptedResponse, error) {
	f.calls = append(f.calls, "Shutdown")
	f.shutdownCalls++
	return f.shutdownAccepted, f.shutdownErr
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
