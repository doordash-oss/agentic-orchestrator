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
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
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

func TestAPIAppModelRefreshErrorStillAppliesPartialSnapshot(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           "feat-1",
		Name:         "Translate README",
		Slug:         "translate-readme",
		Status:       "Designing",
		CurrentPhase: "design",
		Repos:        []string{"agentic-orchestrator"},
		CreatedAt:    time.Now(),
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail:   server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{FeatureSummary: summary}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	if got := app.Snapshot().Features[0].AttentionCount; got != 0 {
		t.Fatalf("initial AttentionCount = %d, want 0", got)
	}

	// A refresh that fetched the prompt snapshot but then errored on a later
	// call (e.g. live-preview blocked by a resume handshake until the client
	// timed out) must still surface the pending question, not discard it.
	model, _ := app.Update(apiRefreshSnapshotMsg{
		snapshot: server.RefreshSnapshot{Prompts: &server.PromptSnapshotResponse{
			AskUserQuestions: []server.ControlRequestDTO{
				{RequestID: "req-1", FeatureID: "feat-1", ToolName: "AskUserQuestion", Status: "pending"},
			},
		}},
		err: errors.New(`send request: Get ".../features/feat-1/live-preview": context deadline exceeded`),
	})
	updated := model.(APIAppModel)
	if !strings.Contains(updated.statusMessage, "Refresh failed") {
		t.Fatalf("statusMessage = %q, want it to surface the refresh error", updated.statusMessage)
	}
	if got := updated.Snapshot().Features[0].AttentionCount; got != 1 {
		t.Fatalf("AttentionCount after partial refresh = %d, want 1 (prompt snapshot applied despite error)", got)
	}
}

func TestAPIAppModelDashboardRestoresMainBranchSpinnerVisuals(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           "active",
		Name:         "Translate README in Sicilian",
		Slug:         "translate-readme-in-sicilian",
		Status:       "Planning",
		CurrentPhase: "plan",
		Repos:        []string{"agentic-orchestrator"},
		CreatedAt:    time.Now(),
		Progress: server.FeatureProgress{
			CurrentRoadmapPhase: 0,
			TotalRoadmapPhases:  3,
			CurrentPhaseStatus:  "running",
		},
	}
	app := APIAppModel{
		width:          160,
		height:         40,
		focusPanel:     0,
		rightPanelMode: dashboardRightPanelOverview,
		featureList:    server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			summary.ID: {Feature: server.FeatureDetailDTO{FeatureSummary: summary}},
		},
		runtimeConfig: server.RuntimeConfigResponse{
			Runtime: server.RuntimeIdentity{StateDir: "/tmp/agentico/features"},
		},
	}
	app.rebuildPresentation(summary.ID)

	dot := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(lipgloss.NewStyle().Foreground(colorInfo)))
	wantSpinner := stripANSI(dot.View())
	if wantSpinner == "" {
		t.Fatal("test spinner frame is empty")
	}

	view := stripANSI(app.View().Content)
	if got := strings.Count(view, wantSpinner); got < 2 {
		t.Fatalf("API dashboard spinner count = %d, want at least 2 for left row and overview phase row; spinner %q view:\n%s", got, wantSpinner, view)
	}
}

func TestAPIAppModelDashboardRendersPhaseCostsFromRESTDetail(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           "published",
		Name:         "Published feature",
		Slug:         "published-feature",
		Status:       "Published",
		CurrentPhase: "Publish",
		Repos:        []string{"agentic-orchestrator"},
		CreatedAt:    time.Now(),
		Progress: server.FeatureProgress{
			CurrentRoadmapPhase: 2,
			TotalRoadmapPhases:  2,
		},
	}
	app := APIAppModel{
		width:          160,
		height:         40,
		focusPanel:     1,
		rightPanelMode: dashboardRightPanelOverview,
		featureList:    server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			summary.ID: {Feature: server.FeatureDetailDTO{
				FeatureSummary: summary,
				Timing: server.TimingDTO{ByPhase: map[string]int64{
					"research":     60,
					"phase-1-plan": 120,
					"phase-1-impl": 180,
					"review":       240,
				}},
				Cost: server.CostDTO{
					TotalUSD: 7.25,
					ByPhase: map[string]float64{
						"research":     1.25,
						"phase-1-plan": 2.50,
						"phase-1-impl": 3.00,
						"review":       0.50,
					},
				},
			}},
		},
		runtimeConfig: server.RuntimeConfigResponse{
			Runtime: server.RuntimeIdentity{StateDir: "/tmp/agentico/features"},
		},
		livePreviews: map[string]server.LivePreviewResponse{
			summary.ID: {
				Feature: summary,
				Cost:    server.CostDTO{TotalUSD: 7.25},
			},
		},
	}
	app.rebuildPresentation(summary.ID)

	if app.snapshot.LivePreview == nil {
		t.Fatal("test setup did not produce a live-preview snapshot")
	}
	dashboard := app.apiDashboardModel()
	if dashboard.preview.feature == nil {
		t.Fatal("dashboard preview feature is nil")
	}
	if got := dashboard.preview.feature.PhaseCost("research"); got != 1.25 {
		t.Fatalf("dashboard preview research cost = %v, want 1.25; costs=%v", got, dashboard.preview.feature.PhaseCosts)
	}
	view := stripANSI(dashboard.View())
	for _, want := range []string{"Cost", "$7.25", "Research", "$1.25", "Phase 1 Plan", "$2.50", "Phase 1", "$3.00", "Final Review", "$0.50"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API dashboard View() missing phase cost %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelDashboardShowsDerivedWorkDir(t *testing.T) {
	t.Parallel()

	runtimeDir := t.TempDir()
	stateDir := filepath.Join(runtimeDir, "features")
	workDir := filepath.Join(runtimeDir, "worktrees", "translate-readme-in-sicilian", "agentic-orchestrator")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	summary := server.FeatureSummary{
		ID:           "active",
		Name:         "Translate README in Sicilian",
		Slug:         "translate-readme-in-sicilian",
		Status:       "Planning",
		CurrentPhase: "plan",
		Repos:        []string{"agentic-orchestrator"},
		CreatedAt:    time.Now(),
	}
	app := APIAppModel{
		width:          160,
		height:         40,
		focusPanel:     0,
		rightPanelMode: dashboardRightPanelOverview,
		featureList:    server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			summary.ID: {Feature: server.FeatureDetailDTO{
				FeatureSummary: summary,
				RepoStatus: []server.RepoStatusDTO{
					{Name: "agentic-orchestrator", Publishable: true},
				},
			}},
		},
		runtimeConfig: server.RuntimeConfigResponse{
			Runtime: server.RuntimeIdentity{StateDir: stateDir},
			Repos: []server.ConfigRepoDTO{
				{Name: "agentic-orchestrator", Path: "/repo/path"},
			},
		},
	}
	app.rebuildPresentation(summary.ID)

	features := app.apiDashboardFeatures()
	if len(features) != 1 || len(features[0].Repos) != 1 {
		t.Fatalf("apiDashboardFeatures repos = %+v, want one feature repo", features)
	}
	if got := features[0].Repos[0].WorktreePath; got != workDir {
		t.Fatalf("dashboard repo WorktreePath = %q, want %q", got, workDir)
	}
	view := stripANSI(app.View().Content)
	for _, want := range []string{"WorkDir", "worktrees/translate-readme-", "in-sicilian/agentic-orchestrator"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API dashboard View() missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "/repo/path") {
		t.Fatalf("API dashboard View() rendered original repo path instead of feature workdir:\n%s", view)
	}
}

func TestAPIAppModelDashboardRestoresSetupFailureFromRESTDetail(t *testing.T) {
	t.Parallel()

	var detail server.FeatureDetailResponse
	if err := json.Unmarshal([]byte(`{
		"feature": {
			"id": "setup-fail",
			"name": "Setup Fail",
			"slug": "setup-fail",
			"status": "Failed",
			"current_phase": "plan",
			"active_run": 1,
			"run_count": 1,
			"repos": ["repo-a"],
			"created_at": "2026-06-18T10:00:00Z",
			"failure": {"type": "worktree_setup", "message": "git worktree add failed"},
			"active_run_detail": {
				"run_number": 1,
				"setup": {
					"status": "failed",
					"attempt": 1,
					"latest_log_path": "/tmp/agentico/setup.log",
					"last_error": "git worktree add failed",
					"task_order": ["worktree:repo-a"],
					"tasks": {
						"worktree:repo-a": {
							"key": "worktree:repo-a",
							"kind": "worktree",
							"label": "Worktree: repo-a",
							"repo": "repo-a",
							"status": "failed",
							"path": "/tmp/agentico/worktrees/setup-fail/repo-a",
							"branch": "feature/setup-fail",
							"attempt": 1,
							"last_error": "git worktree add failed"
						}
					}
				}
			}
		}
	}`), &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	summary := server.FeatureSummary{
		ID:           "setup-fail",
		Name:         "Setup Fail",
		Slug:         "setup-fail",
		Status:       "Failed",
		CurrentPhase: "plan",
		ActiveRun:    1,
		RunCount:     1,
		Repos:        []string{"repo-a"},
		CreatedAt:    time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC),
	}
	app := APIAppModel{
		width:           160,
		height:          40,
		focusPanel:      1,
		rightPanelMode:  dashboardRightPanelOverview,
		selectedFeature: summary.ID,
		featureList:     server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			summary.ID: detail,
		},
		runtimeConfig: server.RuntimeConfigResponse{
			Runtime: server.RuntimeIdentity{StateDir: "/tmp/agentico/features"},
		},
	}
	app.rebuildPresentation(summary.ID)

	features := app.apiDashboardFeatures()
	if len(features) != 1 {
		t.Fatalf("apiDashboardFeatures length = %d, want 1", len(features))
	}
	setup := features[0].Run().Setup
	if setup == nil || setup.Status != feature.SetupStatusFailed || setup.LastError != "git worktree add failed" {
		t.Fatalf("dashboard setup = %+v, want failed setup diagnostics", setup)
	}
	if !canRetrySetup(features[0]) {
		t.Fatalf("canRetrySetup(feature) = false for failed setup: %+v", features[0])
	}

	view := stripANSI(app.View().Content)
	for _, want := range []string{"IN PROGRESS", "Failed (worktree setup)", "Setup attempt 1", "git worktree add failed", "Worktree: repo-a", "/tmp/agentico/setup.log"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API dashboard View() missing setup failure detail %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelRShortcutRetriesFailedSetup(t *testing.T) {
	t.Parallel()

	var detail server.FeatureDetailResponse
	if err := json.Unmarshal([]byte(`{
		"feature": {
			"id": "setup-fail",
			"name": "Setup Fail",
			"slug": "setup-fail",
			"status": "Failed",
			"current_phase": "plan",
			"created_at": "2026-06-18T10:00:00Z",
			"failure": {"type": "worktree_setup", "message": "git worktree add failed"},
			"active_run_detail": {
				"run_number": 1,
				"setup": {
					"status": "failed",
					"attempt": 1,
					"last_error": "git worktree add failed",
					"tasks": {
						"worktree:repo-a": {
							"key": "worktree:repo-a",
							"kind": "worktree",
							"label": "Worktree: repo-a",
							"repo": "repo-a",
							"status": "failed",
							"last_error": "git worktree add failed"
						}
					}
				}
			}
		}
	}`), &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "setup-fail", Name: "Setup Fail", Slug: "setup-fail", Status: "Failed", CurrentPhase: "plan", CreatedAt: time.Now()},
		}},
		detail:        detail,
		retryAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd == nil {
		t.Fatal("Update(r) returned nil command, want setup retry mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)

	if got := strings.Join(client.retryFeatureIDs, ","); got != "setup-fail" {
		t.Fatalf("RetryFeature calls = %q, want setup-fail", got)
	}
	if len(client.restartFeatureIDs) != 0 {
		t.Fatalf("RestartFeature calls = %v, want none for setup retry shortcut", client.restartFeatureIDs)
	}
	if view := stripANSI(model.(APIAppModel).View().Content); !strings.Contains(view, "Completed Retry") {
		t.Fatalf("API app View() missing retry completed status in:\n%s", view)
	}
}

func TestAPIAppModelDiffUsesSavedFeatureBaseBranch(t *testing.T) {
	t.Parallel()

	runGit := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(testutil.GitTestEnv(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	writeReadme := func(t *testing.T, dir, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write README: %v", err)
		}
	}

	runtimeDir := t.TempDir()
	stateDir := filepath.Join(runtimeDir, "features")
	const (
		featureID = "active"
		slug      = "base-aware-diff"
		repoName  = "agentic-orchestrator"
	)
	workDir := filepath.Join(runtimeDir, "worktrees", slug, repoName)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	runGit(t, workDir, "init", "-b", "main")
	runGit(t, workDir, "config", "user.email", "test@test.com")
	runGit(t, workDir, "config", "user.name", "Test")
	runGit(t, workDir, "config", "commit.gpgsign", "false")
	runGit(t, workDir, "config", "tag.gpgsign", "false")
	writeReadme(t, workDir, "main-base\n")
	runGit(t, workDir, "add", "README.md")
	runGit(t, workDir, "commit", "-m", "main base")

	bareDir := filepath.Join(runtimeDir, "origin.git")
	runGit(t, runtimeDir, "init", "--bare", bareDir)
	runGit(t, bareDir, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, workDir, "remote", "add", "origin", bareDir)
	runGit(t, workDir, "push", "-u", "origin", "main")
	runGit(t, workDir, "remote", "set-head", "origin", "main")

	runGit(t, workDir, "checkout", "-b", "release")
	writeReadme(t, workDir, "release-base\n")
	runGit(t, workDir, "add", "README.md")
	runGit(t, workDir, "commit", "-m", "release base")
	runGit(t, workDir, "checkout", "-b", "feature/base-aware-diff")
	writeReadme(t, workDir, "feature-change\n")
	runGit(t, workDir, "add", "README.md")
	runGit(t, workDir, "commit", "-m", "feature change")

	store := feature.NewStore(stateDir)
	if err := store.Save(&feature.Feature{
		ID:            featureID,
		Name:          "Base Aware Diff",
		Slug:          slug,
		Status:        feature.StatusCodeReady,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now(),
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{{
			Name:         repoName,
			Path:         workDir,
			WorktreePath: workDir,
			Branch:       "feature/base-aware-diff",
			BaseBranch:   "release",
		}},
	}); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	summary := server.FeatureSummary{
		ID:           featureID,
		Name:         "Base Aware Diff",
		Slug:         slug,
		Status:       "CodeReady",
		CurrentPhase: "publish",
		Repos:        []string{repoName},
		CreatedAt:    time.Now(),
	}
	app := APIAppModel{
		width:           160,
		height:          40,
		selectedFeature: featureID,
		featureList:     server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		featureDetails:  map[string]server.FeatureDetailResponse{},
		runtimeConfig: server.RuntimeConfigResponse{
			Runtime: server.RuntimeIdentity{StateDir: stateDir},
			Repos:   []server.ConfigRepoDTO{{Name: repoName, Path: workDir}},
		},
	}

	_, cmd := app.openSelectedDiff()
	if cmd == nil {
		t.Fatal("openSelectedDiff returned nil command")
	}
	rawMsg := cmd()
	msg, ok := rawMsg.(apiDiffReviewMsg)
	if !ok {
		t.Fatalf("openSelectedDiff command returned %T, want apiDiffReviewMsg", rawMsg)
	}
	content := stripANSI(msg.content)
	if !strings.Contains(content, "-release-base") || strings.Contains(content, "-main-base") {
		t.Fatalf("diff content should be based on saved feature base branch release:\n%s", content)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	if cmd == nil {
		t.Fatal("Update(v) returned nil command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	view := stripANSI(model.(APIAppModel).View().Content)
	for _, want := range []string{"Diff Review", "-release-base", "0%"} {
		if !strings.Contains(view, want) {
			t.Fatalf("diff review view missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Diff: base-aware-diff") {
		t.Fatalf("diff review rendered generic text panel title instead of review chrome:\n%s", view)
	}

	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.(APIAppModel).diffReview != nil {
		t.Fatal("Escape did not close diff review")
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

func TestAPIAppModelOverviewShowsRESTRefactorCycleSubphase(t *testing.T) {
	t.Parallel()

	cycle := &server.CycleDTO{Type: "refactor", Status: "running", Count: 1, Iteration: 1}
	summary := server.FeatureSummary{
		ID:           "active",
		Name:         "Sicilian README",
		Slug:         "translate-in-sicilian",
		Status:       "CodeReady",
		CurrentPhase: "publish",
		Cycle:        cycle,
		Repos:        []string{"agentic-orchestrator"},
		CreatedAt:    time.Now(),
	}
	app := APIAppModel{
		width:           160,
		height:          40,
		focusPanel:      1,
		rightPanelMode:  dashboardRightPanelOverview,
		selectedFeature: summary.ID,
		featureList:     server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			summary.ID: {Feature: server.FeatureDetailDTO{
				FeatureSummary: summary,
				Cycle:          cycle,
				Timing:         server.TimingDTO{ByPhase: map[string]int64{"implement": 300}},
				RepoStatus: []server.RepoStatusDTO{
					{Name: "agentic-orchestrator", Touched: true, Publishable: true, CycleType: "refactor", CycleStatus: "running"},
				},
			}},
		},
	}

	features := app.apiDashboardFeatures()
	if len(features) != 1 {
		t.Fatalf("apiDashboardFeatures length = %d, want 1", len(features))
	}
	f := features[0]
	if f.ActiveCycle == nil || f.ActiveCycle.Type != feature.CycleRefactor || f.ActiveCycle.Status != feature.RepoCycleRunning || f.ActiveCycle.Count != 1 {
		t.Fatalf("ActiveCycle = %+v, want running refactor #1", f.ActiveCycle)
	}
	if got := f.RefactorCount(); got != 1 {
		t.Fatalf("RefactorCount = %d, want 1 from REST cycle count", got)
	}
	if got := f.ActiveCycleType(); got != feature.CycleRefactor {
		t.Fatalf("ActiveCycleType = %q, want refactor", got)
	}

	view := stripANSI(app.View().Content)
	for _, want := range []string{"Info", "Phase Progress", "Refactor #1", "in progress", "[l] Live Preview"} {
		if !strings.Contains(view, want) {
			t.Fatalf("REST refactor cycle overview missing %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{"Feature ID", "Current: Refactoring", "[o] Overview"} {
		if strings.Contains(view, notWant) {
			t.Fatalf("REST refactor cycle overview contained live-preview copy %q in:\n%s", notWant, view)
		}
	}
}

func TestAPIAppModelOverviewShowsFeatureRebaseAndFreshness(t *testing.T) {
	t.Parallel()

	cycle := &server.CycleDTO{Type: "rebase", Status: "running", Count: 1, Iteration: 1}
	summary := server.FeatureSummary{
		ID:           "active",
		Name:         "Multi Repo Rebase",
		Slug:         "multi-repo-rebase",
		Status:       "CodeReady",
		CurrentPhase: "publish",
		Cycle:        cycle,
		Repos:        []string{"api", "web"},
		CreatedAt:    time.Now(),
	}
	app := APIAppModel{
		width:           160,
		height:          40,
		focusPanel:      1,
		rightPanelMode:  dashboardRightPanelOverview,
		selectedFeature: summary.ID,
		featureList:     server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			summary.ID: {Feature: server.FeatureDetailDTO{
				FeatureSummary: summary,
				Cycle:          cycle,
				RepoStatus: []server.RepoStatusDTO{
					{Name: "api", Touched: true, Publishable: true, Freshness: "local changes", RebaseStatus: "conflict", ConflictFiles: []string{"service.go"}},
					{Name: "web", Touched: true, Publishable: true, Freshness: "in sync", RebaseStatus: "up_to_date"},
				},
			}},
		},
	}

	features := app.apiDashboardFeatures()
	if len(features) != 1 {
		t.Fatalf("apiDashboardFeatures length = %d, want 1", len(features))
	}
	f := features[0]
	if f.ActiveCycle == nil || f.ActiveCycle.Type != feature.CycleRebase || f.ActiveCycle.Status != feature.RepoCycleRunning {
		t.Fatalf("ActiveCycle = %+v, want running feature-level rebase", f.ActiveCycle)
	}
	if f.RebaseOperation == nil || f.RebaseOperation.Repos["api"].Status != feature.RebaseRepoStatusConflict {
		t.Fatalf("RebaseOperation = %+v, want api conflict progress", f.RebaseOperation)
	}
	if got := f.RepoStates["api"].Freshness; got != "local changes" {
		t.Fatalf("api freshness = %q, want local changes", got)
	}
	wantSpinner := stripANSI(newAPIAppSpinner().View())
	dashboard := NewDashboardModel(features, "")
	dashboard.spinnerView = wantSpinner
	row := stripANSI(dashboard.renderFeatureRowCompact(f, false))
	if !strings.Contains(row, wantSpinner) {
		t.Fatalf("REST rebase dashboard row = %q; want spinner %q", row, wantSpinner)
	}
	if !strings.Contains(row, "Rebasing [1]") {
		t.Fatalf("REST rebase dashboard row = %q; want feature-level rebase label", row)
	}

	view := stripANSI(app.View().Content)
	for _, want := range []string{"Info", "Rebasing [1]", "Repo Status", "api", "conflict: service.go", "local changes", "web", "in sync"} {
		if !strings.Contains(view, want) {
			t.Fatalf("REST rebase overview missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Code Ready") || strings.Contains(view, "· api") {
		t.Fatalf("REST rebase overview should show feature-level rebase without repo suffix:\n%s", view)
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
	flatView := strings.Join(strings.Fields(view), " ")
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
		"[Shift+E] Workspace Config",
		"[tab] Panel",
		"Layout: US",
		"[/] Ask",
		"[?] Help",
	} {
		if !strings.Contains(flatView, want) {
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

func TestAPIAppModelShowsWelcomeWhenWorkspaceRootsEmpty(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features:                 server.FeatureListResponse{},
		runtime:                  server.RuntimeConfigResponse{},
		allowEmptyWorkspaceRoots: true,
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	view := stripANSI(app.View().Content)
	for _, want := range []string{
		"Agentic Orchestrator helps you manage AI-assisted development workflows.",
		"To get started, add a workspace directory containing your git repositories.",
		"enter select workspace directory",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("first-run API app view missing %q in:\n%s", want, view)
		}
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatal("Update(esc welcome) returned command, want local skip")
	}
	app = model.(APIAppModel)
	view = stripANSI(app.View().Content)
	if !strings.Contains(view, "You can add workspace roots later by pressing W") {
		t.Fatalf("skipped welcome view missing workspace guidance:\n%s", view)
	}
	if strings.Contains(view, "To get started, add a workspace directory") {
		t.Fatalf("welcome intro still rendered after skip:\n%s", view)
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

func TestAPIAppModelAttachRefreshPrunesCompletedValidatorTab(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: "impl-1", FeatureID: "active", Phase: "implement", Kind: "phase", Status: "Running"},
			{ID: "testing-validator", FeatureID: "active", Phase: "plan", Kind: "validator", Label: "Testing", Status: "Running"},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	attached := model.(APIAppModel)
	if attached.attach == nil || len(attached.attach.repoTabs) != 2 {
		t.Fatalf("attach tabs = %+v, want implementation plus validator tabs", attached.attach.repoTabs)
	}

	model, _ = attached.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Sessions: &server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: "impl-1", FeatureID: "active", Phase: "implement", Kind: "phase", Status: "Running"},
			{ID: "testing-validator", FeatureID: "active", Phase: "plan", Kind: "validator", Label: "Testing", Status: "Done"},
		}},
	}})
	refreshed := model.(APIAppModel)
	if refreshed.attach == nil {
		t.Fatal("attach view closed; want it to stay on remaining implementation session")
	}
	if got := len(refreshed.attach.repoTabs); got != 1 {
		t.Fatalf("attach tab count = %d, want 1 after completed validator is pruned: %+v", got, refreshed.attach.repoTabs)
	}
	if got := refreshed.attach.repoTabs[0].sess.ID(); got != "impl-1" {
		t.Fatalf("remaining attach session = %q, want impl-1", got)
	}
	if view := stripANSI(refreshed.View().Content); strings.Contains(view, "Testing") || strings.Contains(view, "Plan (running)") {
		t.Fatalf("attach view still renders completed validator as running:\n%s", view)
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

func TestAPIAppModelOverviewUsesLivePreviewContextPct(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           "active",
		Name:         "Client cutover",
		Slug:         "client-cutover",
		Status:       "Implementing",
		CurrentPhase: "implement",
		CreatedAt:    time.Now(),
		Progress: server.FeatureProgress{
			CurrentIteration: 1,
		},
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail:   server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{FeatureSummary: summary}},
		livePreview: server.LivePreviewResponse{
			Feature: summary,
			Session: &server.SessionSummaryDTO{
				ID:        "sess-live",
				FeatureID: "active",
				Phase:     "implement",
				Status:    "running",
			},
			Context: server.ContextDTO{Percentage: 42},
		},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	app.rightPanelMode = dashboardRightPanelOverview
	dashboard := app.apiDashboardModel()
	if got := dashboard.preview.contextPct; got != 42 {
		t.Fatalf("overview contextPct = %d, want live preview context 42", got)
	}
	view := stripANSI(dashboard.View())
	if !strings.Contains(view, "context window: 42%") {
		t.Fatalf("overview missing live preview context in:\n%s", view)
	}
	if strings.Contains(view, "context window: 0%") {
		t.Fatalf("overview rendered stale zero context in:\n%s", view)
	}
}

func TestAPIAppModelOverviewParsesFinalReviewPhaseFromREST(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           "active",
		Name:         "Translate README",
		Slug:         "translate-readme",
		Status:       "FinalReviewing",
		CurrentPhase: "Final Review",
		CreatedAt:    time.Now(),
		Repos:        []string{"agentic-orchestrator"},
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: summary,
			Pipeline:       "medium",
			ActiveRun:      &server.RunSummaryDTO{RunNumber: 1, CurrentPhase: "Final Review"},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	app.rightPanelMode = dashboardRightPanelOverview
	dashboard := app.apiDashboardModel()
	if got := dashboard.preview.feature.CurrentPhase; got != feature.PhaseFinalReview {
		t.Fatalf("overview CurrentPhase = %s, want Final Review", got)
	}
	view := stripANSI(dashboard.View())
	finalReviewLine := ""
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Final Review") && !strings.Contains(line, "Status") {
			finalReviewLine = line
			break
		}
	}
	if finalReviewLine == "" {
		t.Fatalf("overview missing Final Review progress row in:\n%s", view)
	}
	if strings.Contains(finalReviewLine, "pending") {
		t.Fatalf("Final Review row rendered as pending: %q\nview:\n%s", finalReviewLine, view)
	}
	if !strings.Contains(finalReviewLine, "reviewing") {
		t.Fatalf("Final Review row = %q, want reviewing state\nview:\n%s", finalReviewLine, view)
	}
}

func TestAPIAppModelLivePreviewPreservesTranscriptToolRowsFromREST(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		livePreview: server.LivePreviewResponse{
			Feature: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement"},
			Session: &server.SessionSummaryDTO{ID: "sess-live", FeatureID: "active", Phase: "implement", Label: "Implement", Status: "running"},
			Transcript: []server.TranscriptMessageDTO{
				{Index: 1, Role: "assistant", Type: "text", Text: "Preparing patch"},
				{Index: 2, Role: "assistant", Type: "tool_use", Tool: "Bash", Redacted: true},
				{Index: 3, Role: "assistant", Type: "tool_use", Tool: "AskUserQuestion", Redacted: true},
			},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	view := stripANSI(app.View().Content)
	for _, want := range []string{"Preparing patch", "$ Bash", "? AskUser:"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API live preview missing typed transcript row %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{"> Preparing patch", "> Bash", "> AskUserQuestion"} {
		if strings.Contains(view, notWant) {
			t.Fatalf("API live preview rendered tool row as assistant text %q in:\n%s", notWant, view)
		}
	}
}

func TestAPIAppModelLivePreviewPreservesToolProgressRowsFromREST(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		livePreview: server.LivePreviewResponse{
			Feature: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement"},
			Session: &server.SessionSummaryDTO{ID: "sess-live", FeatureID: "active", Phase: "implement", Label: "Implement", Provider: "codex", Status: "running"},
			Transcript: []server.TranscriptMessageDTO{
				{Index: 1, Role: "system", Type: "tool_progress", Tool: "Bash", Redacted: true},
				{Index: 2, Role: "assistant", Type: "text", Text: "Continuing after tool use"},
			},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	view := stripANSI(app.View().Content)
	for _, want := range []string{"$ Bash", "Continuing after tool use"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API live preview missing tool-progress transcript row %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{"> Bash", "> Continuing after tool use"} {
		if strings.Contains(view, notWant) {
			t.Fatalf("API live preview rendered row with assistant glyph %q in:\n%s", notWant, view)
		}
	}
}

func TestAPITranscriptRowToSDKMessagePreservesToolProgress(t *testing.T) {
	t.Parallel()

	msg, ok := apiTranscriptRowToSDKMessage(server.TranscriptMessageDTO{
		Index:    1,
		Role:     "system",
		Type:     "tool_progress",
		Tool:     "Bash",
		Redacted: true,
	}, "sess-live")
	if !ok {
		t.Fatal("apiTranscriptRowToSDKMessage(tool_progress) returned !ok")
	}
	if msg.ToolProgress == nil {
		t.Fatalf("apiTranscriptRowToSDKMessage(tool_progress) = %+v, want ToolProgress message", msg)
	}
	if msg.Assistant != nil {
		t.Fatalf("tool_progress row should not reconstruct as assistant tool use: %+v", msg.Assistant)
	}
	if msg.ToolProgress.ToolName != "Bash" || msg.ToolProgress.SessionID != "sess-live" {
		t.Fatalf("ToolProgress = %+v, want Bash in sess-live", msg.ToolProgress)
	}
}

func TestAPITranscriptRowToSDKMessagePreservesAutoPickedUserEcho(t *testing.T) {
	t.Parallel()

	msg, ok := apiTranscriptRowToSDKMessage(server.TranscriptMessageDTO{
		Index:              1,
		Role:               "user",
		Type:               "text",
		Text:               "Translate `README.md` in place (Recommended)",
		LocallyAppended:    true,
		AutoPicked:         true,
		AutoPickQuestion:   "Which output shape?",
		AutoPickConfidence: 0.72,
	}, "sess-live")
	if !ok {
		t.Fatal("apiTranscriptRowToSDKMessage(auto-picked user text) returned !ok")
	}
	if !msg.AutoPicked || msg.AutoPickQuestion != "Which output shape?" || msg.AutoPickConfidence != 0.72 {
		t.Fatalf("apiTranscriptRowToSDKMessage() = %+v, want auto-picked metadata", msg)
	}

	rendered := stripANSI(renderAttachMessages([]llm.SDKMessage{msg}, filterAll, 120, nil))
	if !strings.Contains(rendered, "[auto-picked, confidence: 0.72] Translate `README.md` in place (Recommended)") {
		t.Fatalf("rendered auto-picked row = %q", rendered)
	}
	if strings.Contains(rendered, "[you] Translate `README.md` in place (Recommended)") {
		t.Fatalf("rendered auto-picked row should not use [you] label: %q", rendered)
	}
}

func TestAPILivePreviewSessionCarriesProviderFromREST(t *testing.T) {
	t.Parallel()

	presentation := apiLivePreviewPresentation("active", server.LivePreviewResponse{
		Session: &server.SessionSummaryDTO{
			ID:       "sess-live",
			Phase:    "implement",
			Kind:     "agent",
			Provider: "codex",
			Model:    "gpt-5-codex",
		},
		Transcript: []server.TranscriptMessageDTO{
			{Index: 1, Role: "system", Type: "tool_progress", Tool: "Bash", Redacted: true},
		},
	})
	sess := newAPILivePreviewSession(presentation)
	if sess == nil {
		t.Fatal("newAPILivePreviewSession returned nil")
	}
	if got := sess.ProviderName(); got != "codex" {
		t.Fatalf("ProviderName() = %q, want codex", got)
	}
	if got := sess.Model(); got != "gpt-5-codex" {
		t.Fatalf("Model() = %q, want gpt-5-codex", got)
	}
}

func TestAPILivePreviewSessionCarriesPhaseFromREST(t *testing.T) {
	t.Parallel()

	presentation := apiLivePreviewPresentation("active", server.LivePreviewResponse{
		Session: &server.SessionSummaryDTO{
			ID:     "sess-live",
			Phase:  "Final Review",
			Status: "running",
		},
	})
	sess := newAPILivePreviewSession(presentation)
	if sess == nil {
		t.Fatal("newAPILivePreviewSession returned nil")
	}
	if got := sess.Phase(); got != feature.PhaseFinalReview {
		t.Fatalf("Phase() = %s, want Final Review", got)
	}
}

func TestAPILivePreviewSessionCarriesKindAndLabelFromREST(t *testing.T) {
	t.Parallel()

	presentation := apiLivePreviewPresentation("active", server.LivePreviewResponse{
		Session: &server.SessionSummaryDTO{
			ID:     "scope-validator",
			Phase:  "plan",
			Kind:   "validator",
			Label:  "Scope",
			Status: "running",
		},
	})
	sess := newAPILivePreviewSession(presentation)
	if sess == nil {
		t.Fatal("newAPILivePreviewSession returned nil")
	}
	if got := sess.Kind().String(); got != "validator" {
		t.Fatalf("Kind() = %q, want validator", got)
	}
	if got := sess.Label(); got != "Scope" {
		t.Fatalf("Label() = %q, want Scope", got)
	}
}

func TestAPIAppModelDashboardFeatureCarriesValidationReviewGateFromREST(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           "active",
		Name:         "Validate roadmap",
		Slug:         "validate-roadmap",
		Status:       "Planning",
		CurrentPhase: "plan",
		Progress: server.FeatureProgress{
			CurrentRoadmapPhase: 1,
			TotalRoadmapPhases:  3,
		},
	}
	detail := server.FeatureDetailDTO{
		FeatureSummary: summary,
		ActiveRun:      &server.RunSummaryDTO{RunNumber: 1, CurrentPhase: "plan", RoadmapPhase: 1, RoadmapTotal: 3},
		ReviewGate: server.ReviewGateDTO{
			ValidatingPlan: true,
			ValidatorStatuses: map[string]string{
				"Architecture": "APPROVED",
				"Scope":        "CHANGES_REQUESTED",
				"Testing":      "running",
			},
		},
	}
	f := (APIAppModel{}).apiDashboardFeature(summary, detail, true)
	if !f.ValidatingPlan {
		t.Fatal("ValidatingPlan = false, want true")
	}
	if got := f.ValidatorStatuses["Scope"]; got != "CHANGES_REQUESTED" {
		t.Fatalf("ValidatorStatuses[Scope] = %q, want CHANGES_REQUESTED", got)
	}

	sess := validatorLivePreviewSession("scope-validator", "Scope")
	view := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(120))
	for _, want := range []string{
		"Status", "Validating Phase 1 plan",
		"Validators", "Arch ✓", "Test ⟳", "Scope ✗",
		"Current: Validating Phase 1 plan", "1 ✓", "1 ✗", "1 running", "Showing Scope",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("REST validation live preview missing %q in:\n%s", want, view)
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

func TestAPIAppModelContentViewRendersFullScreen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
			ActiveRun:      &server.RunSummaryDTO{RunNumber: 7, CurrentPhase: "implement", ArtifactCount: 1},
		}},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: "plan", RunNumber: 7, Phase: "plan", Size: 16, ContentAvailable: true},
		}},
		logContent: server.TextContentResponse{
			ID:     "session",
			Offset: 0,
			Limit:  apiContentTailLimit,
			Size:   21,
			Text:   "content view log tail",
		},
		artifactContent: server.TextContentResponse{
			ID:     "plan",
			Offset: 0,
			Limit:  apiContentTailLimit,
			Size:   16,
			Text:   "content artifact",
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if cmd == nil {
		t.Fatal("Update(l) returned nil command, want log content fetch command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	view := stripANSI(model.(APIAppModel).View().Content)

	for _, want := range []string{"Run Content", "content view log tail", "content artifact", "Next log"} {
		if !strings.Contains(view, want) {
			t.Fatalf("content view missing %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{"Features", "Client cutover", "IN PROGRESS"} {
		if strings.Contains(view, notWant) {
			t.Fatalf("content view included dashboard chrome %q:\n%s", notWant, view)
		}
	}
}

func TestAPIAppModelMissingRunLogsDoNotShowRawAPIError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	notFoundLogErr := func(logID string) error {
		return &server.APIError{
			Status:  http.StatusNotFound,
			Code:    "not_found",
			Message: "content not found",
			Method:  http.MethodGet,
			Path:    "/api/v1/features/active/runs/7/logs/" + logID,
		}
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
			ActiveRun:      &server.RunSummaryDTO{RunNumber: 7, CurrentPhase: "implement", ArtifactCount: 1},
		}},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: "plan", RunNumber: 7, Phase: "plan", Size: 16, ContentAvailable: true},
		}},
		artifactContent: server.TextContentResponse{
			ID:     "plan",
			Offset: 0,
			Limit:  apiContentTailLimit,
			Size:   16,
			Text:   "content artifact",
		},
		logContentErrByID: map[string]error{
			"session": notFoundLogErr("session"),
			"phase":   notFoundLogErr("phase"),
			"observe": notFoundLogErr("observe"),
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if cmd == nil {
		t.Fatal("Update(l) returned nil command, want log content fetch command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	updated := model.(APIAppModel)

	if got := updated.statusMessage; got != "No run logs available for selected run" {
		t.Fatalf("statusMessage = %q, want friendly missing-log status", got)
	}
	if strings.Contains(updated.statusMessage, "api GET") || strings.Contains(updated.statusMessage, "not_found") {
		t.Fatalf("statusMessage leaked raw API error: %q", updated.statusMessage)
	}
	view := stripANSI(updated.View().Content)
	if !strings.Contains(view, "content artifact") {
		t.Fatalf("content view lost artifact after missing logs:\n%s", view)
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
	for _, want := range []string{"Live Preview", "Using Bash...", "57%", "$1.25", "Initial tail", "Patched through REST snapshot"} {
		if !strings.Contains(view, want) {
			t.Fatalf("refreshed API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelLivePreviewRefreshDropsCachedTailWhenSessionChanges(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		livePreview: server.LivePreviewResponse{
			Feature:    server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
			Activity:   "Thinking...",
			Session:    &server.SessionSummaryDTO{ID: "sess-old", FeatureID: "active", Phase: "implement", Status: "running"},
			Transcript: []server.TranscriptMessageDTO{{Index: 1, Role: "assistant", Type: "text", Text: "Old session tail"}},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	client.refreshSnapshot = server.RefreshSnapshot{
		LivePreview: &server.LivePreviewResponse{
			Feature:  server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
			Activity: "Using Bash...",
			Session:  &server.SessionSummaryDTO{ID: "sess-new", FeatureID: "active", Phase: "implement", Status: "running"},
			Transcript: []server.TranscriptMessageDTO{
				{Index: 1, Role: "assistant", Type: "text", Text: "New session tail"},
			},
		},
	}
	signal := server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: "log.updated"},
		Resource: server.ResourceDTO{Type: "session", ID: "sess-new", FeatureID: "active"},
	}
	msg := app.fetchRefreshSnapshotCmd(signal)()
	model, _ := app.Update(msg)
	refreshed := model.(APIAppModel)

	view := stripANSI(refreshed.View().Content)
	if !strings.Contains(view, "New session tail") {
		t.Fatalf("refreshed API app View() missing new session tail:\n%s", view)
	}
	if strings.Contains(view, "Old session tail") {
		t.Fatalf("refreshed API app View() kept old session tail after session change:\n%s", view)
	}
}

func TestAPIAppModelLivePreviewRefreshDropsCachedTailWhenSameSessionIDRestarts(t *testing.T) {
	t.Parallel()

	oldStartedAt := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	newStartedAt := oldStartedAt.Add(time.Minute)

	app := APIAppModel{}
	app.storeLivePreview("active", server.LivePreviewResponse{
		Feature:  server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Interrupted"},
		Activity: "Stopped",
		Session:  &server.SessionSummaryDTO{ID: "active-impl", FeatureID: "active", Phase: "implement", Status: "done", StartedAt: oldStartedAt},
		Transcript: []server.TranscriptMessageDTO{
			{Index: 1, Role: "assistant", Type: "text", Text: "Old session first row"},
			{Index: 2, Role: "assistant", Type: "text", Text: "Old session tail"},
		},
	})
	app.storeLivePreview("active", server.LivePreviewResponse{
		Feature:  server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing"},
		Activity: "Using Bash...",
		Session:  &server.SessionSummaryDTO{ID: "active-impl", FeatureID: "active", Phase: "implement", Status: "running", StartedAt: newStartedAt},
		Transcript: []server.TranscriptMessageDTO{
			{Index: 1, Role: "assistant", Type: "text", Text: "New restarted session tail"},
		},
	})

	got := app.livePreviews["active"].Transcript
	if len(got) != 1 {
		t.Fatalf("cached live preview transcript len = %d, want 1 after same-ID restart: %+v", len(got), got)
	}
	if got[0].Text != "New restarted session tail" {
		t.Fatalf("cached live preview transcript = %+v, want restarted session tail only", got)
	}
}

func TestMergeLivePreviewTranscriptTreatsShiftedSnapshotsAsOverlappingTail(t *testing.T) {
	t.Parallel()

	existing := []server.TranscriptMessageDTO{
		{Index: 10, Role: "assistant", Type: "text", Text: "I have enough context. Let me now write the research questions file."},
		{Index: 11, Role: "assistant", Type: "tool_use", Tool: "Bash", Redacted: true},
		{Index: 12, Role: "system", Type: "control_request", Tool: "AskUserQuestion", Status: "pending", Redacted: true},
	}
	incoming := []server.TranscriptMessageDTO{
		{Index: 20, Role: "assistant", Type: "text", Text: "I have enough context. Let me now write the research questions file."},
		{Index: 21, Role: "assistant", Type: "tool_use", Tool: "Bash", Redacted: true},
		{Index: 22, Role: "system", Type: "control_request", Tool: "AskUserQuestion", Status: "pending", Redacted: true},
		{Index: 23, Role: "assistant", Type: "tool_use", Tool: "Read", Redacted: true},
	}

	merged := mergeLivePreviewTranscript(existing, incoming)
	if got, want := len(merged), 4; got != want {
		t.Fatalf("merged transcript len = %d, want %d: %+v", got, want, merged)
	}
	if got, want := merged[0].Text, existing[0].Text; got != want {
		t.Fatalf("merged[0].Text = %q, want %q", got, want)
	}
	if got, want := merged[1].Tool, "Bash"; got != want {
		t.Fatalf("merged[1].Tool = %q, want %q", got, want)
	}
	if got, want := merged[2].Tool, "AskUserQuestion"; got != want {
		t.Fatalf("merged[2].Tool = %q, want %q", got, want)
	}
	if got, want := merged[3].Tool, "Read"; got != want {
		t.Fatalf("merged[3].Tool = %q, want %q", got, want)
	}
}

func TestMergeLivePreviewTranscriptReplacesUpdatedStreamingRow(t *testing.T) {
	t.Parallel()

	existing := []server.TranscriptMessageDTO{
		{Index: 1, Role: "system", Type: "tool_progress", Tool: "Bash", Redacted: true},
		{Index: 2, Role: "assistant", Type: "text", Text: "I'm using the inquire workflow now; I'll keep this"},
		{Index: 3, Role: "system", Type: "tool_progress", Tool: "Bash", Redacted: true},
	}
	incoming := []server.TranscriptMessageDTO{
		{Index: 1, Role: "system", Type: "tool_progress", Tool: "Bash", Redacted: true},
		{Index: 2, Role: "assistant", Type: "text", Text: "I'm using the inquire workflow now; I'll keep this to requirements-level clarification."},
		{Index: 3, Role: "system", Type: "tool_progress", Tool: "Bash", Redacted: true},
	}

	merged := mergeLivePreviewTranscript(existing, incoming)
	if got, want := len(merged), 3; got != want {
		t.Fatalf("merged transcript len = %d, want %d: %+v", got, want, merged)
	}
	if strings.Contains(merged[1].Text, "this\nI'm using") {
		t.Fatalf("merged transcript stacked streaming prefixes: %+v", merged)
	}
	if got, want := merged[1].Text, incoming[1].Text; got != want {
		t.Fatalf("merged streaming row text = %q, want latest %q", got, want)
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

func TestAPIAppModelIgnoresStaleAttentionForInterruptedFeature(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "stopped", Name: "Stopped work", Slug: "stopped-work", Status: "Interrupted", CurrentPhase: "design", CreatedAt: time.Now()},
		}},
		prompts: server.PromptSnapshotResponse{
			AskUserQuestions: []server.ControlRequestDTO{
				{FeatureID: "stopped", RequestID: "ask-1", Status: "pending", ToolName: "AskUserQuestion", Summary: "Which path?"},
			},
			HelpQueue: []server.HelpQueueDTO{
				{FeatureID: "stopped", Question: "Need input?", Pending: true},
			},
		},
		permissions: server.PermissionSnapshotResponse{Requests: []server.ControlRequestDTO{
			{FeatureID: "stopped", RequestID: "perm-1", Status: "pending", ToolName: "Bash", Summary: "go test ./internal/tui"},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	snapshot := app.Snapshot()
	if len(snapshot.Features) != 1 {
		t.Fatalf("Features len = %d, want 1", len(snapshot.Features))
	}
	if got := snapshot.Features[0].AttentionCount; got != 0 {
		t.Fatalf("interrupted feature AttentionCount = %d, want 0", got)
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
	chatting := model.(APIAppModel)
	if !chatting.chatOpen {
		t.Fatal("Update(/) did not open AMA chat")
	}
	if view := stripANSI(chatting.View().Content); !strings.Contains(view, "Ask me Anything") {
		t.Fatalf("chat view missing AMA panel:\n%s", view)
	}

	chatting.chat.input.SetValue("what is running?")
	model, cmd = chatting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want chat start mutation")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	startedChat := model.(APIAppModel)
	if got := client.startChatRequests; len(got) != 1 || got[0].Message != "what is running?" {
		t.Fatalf("StartChat requests = %+v, want first AMA message", got)
	}
	if startedChat.chat.sess == nil || startedChat.chat.sess.ID() != chatSessionID {
		t.Fatalf("chat session = %#v, want %s", startedChat.chat.sess, chatSessionID)
	}
	if got := startedChat.chat.sess.FeatureID(); got != chatSessionID {
		t.Fatalf("chat session FeatureID() = %q, want utility identity %q", got, chatSessionID)
	}

	model, cmd = startedChat.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Update(esc) returned nil command, want ChatExitMsg")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	closedChat := model.(APIAppModel)
	if closedChat.chatOpen {
		t.Fatal("ChatExitMsg did not close AMA panel")
	}

	model, cmd = closedChat.Update(tea.KeyPressMsg{Code: 'N', Text: "N"})
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

// TestAPIAppModelChatExitResetsFullscreen covers Finding 3: fullscreen must
// not survive a chat close, or the next open sizes the panel for docked mode
// while View() still renders chat-only (fullscreen) from stale state.
func TestAPIAppModelChatExitResetsFullscreen(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	chatting := model.(APIAppModel)
	chatting.chat.fullscreen = true

	model, _ = chatting.Update(ChatExitMsg{})
	closed := model.(APIAppModel)
	if closed.chatOpen {
		t.Fatal("expected ChatExitMsg to close the chat panel")
	}
	if closed.chat.fullscreen {
		t.Fatal("expected ChatExitMsg to reset fullscreen so the next open starts docked")
	}
}

func TestAPIAppModelChatStartErrorStopsRespondingAndRendersError(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		startChatErr: errors.New("monthly spend limit"),
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	chatting := model.(APIAppModel)
	chatting.chat.input.SetValue("yo")
	model, cmd := chatting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want chat start command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	failed := model.(APIAppModel)

	if failed.chat.responding {
		t.Fatal("chat remained responding after start error")
	}
	view := stripANSI(failed.View().Content)
	if !strings.Contains(view, "Error starting session: monthly spend limit") {
		t.Fatalf("chat view missing start error:\n%s", view)
	}
	if strings.Contains(view, "Thinking") || strings.Contains(view, "[esc] Background") {
		t.Fatalf("chat view still shows responding UI after start error:\n%s", view)
	}
}

func TestAPIAppModelChatRefreshRendersResultErrorAsRedResponse(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	model, _ := app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	chatting := model.(APIAppModel)
	chatting.chat.input.SetValue("yo")
	model, cmd := chatting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want chat start command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	started := model.(APIAppModel)
	const errorText = "You've hit your org's monthly spend limit."

	model, _ = started.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: server.SessionDetailDTO{
			SessionSummaryDTO: server.SessionSummaryDTO{ID: chatSessionID, FeatureID: chatSessionID, Phase: "research", Status: "failed"},
			TranscriptCursor:  server.CursorDTO{Total: 2, Start: 0, End: 2},
		}},
		Transcript: &server.TranscriptResponse{
			Cursor: server.CursorDTO{Total: 2, Start: 0, End: 2},
			Messages: []server.TranscriptMessageDTO{
				{Index: 0, Role: "assistant", Type: "text", Text: errorText},
				{Index: 1, Role: "system", Type: "result", Status: "error", Redacted: true},
			},
		},
	}})
	failed := model.(APIAppModel)

	if failed.chat.responding {
		t.Fatal("chat remained responding after transcript result error")
	}
	turns := failed.chat.turns
	if len(turns) == 0 || turns[len(turns)-1].Role != chatTurnError || turns[len(turns)-1].Text != errorText {
		t.Fatalf("expected trailing error turn with text %q, got turns: %+v", errorText, turns)
	}
	if rendered, want := renderChatTurn(turns[len(turns)-1], 80), chatAgentTagErrorStyle.Render("[agent]")+"  "+ErrorStyle.Render(errorText); rendered != want {
		t.Fatalf("renderChatTurn(error turn) = %q, want %q (error not rendered in ErrorStyle)", rendered, want)
	}
	view := stripANSI(failed.View().Content)
	if !strings.Contains(view, errorText) {
		t.Fatalf("chat view missing transcript error:\n%s", view)
	}
	if strings.Contains(view, "Refresh failed") || strings.Contains(view, "Thinking") {
		t.Fatalf("chat view rendered refresh/thinking noise after transcript error:\n%s", view)
	}
}

func TestAPIAppModelChatWaitingHelpSnapshotAllowsNextMessage(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	model, _ := app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	chatting := model.(APIAppModel)
	chatting.chat.input.SetValue("yo")
	model, cmd := chatting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want chat start command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	started := model.(APIAppModel)

	model, _ = started.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: server.SessionDetailDTO{
			SessionSummaryDTO: server.SessionSummaryDTO{ID: chatSessionID, FeatureID: chatSessionID, Phase: "research", Status: "WaitingHelp"},
			TranscriptCursor:  server.CursorDTO{Total: 1, Start: 0, End: 1},
		}},
		Transcript: &server.TranscriptResponse{
			Cursor: server.CursorDTO{Total: 1, Start: 0, End: 1},
			Messages: []server.TranscriptMessageDTO{
				{Index: 0, Role: "assistant", Type: "text", Text: "yo! What's up?"},
			},
		},
	}})
	ready := model.(APIAppModel)

	if ready.chat.responding {
		t.Fatal("chat remained responding after WaitingHelp snapshot")
	}
	view := stripANSI(ready.View().Content)
	if strings.Contains(view, "Thinking") || !strings.Contains(view, "[enter] Send") {
		t.Fatalf("chat view did not return to send mode:\n%s", view)
	}

	ready.chat.input.SetValue("take two")
	model, cmd = ready.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want follow-up send command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	if got := client.helpRequests; len(got) != 1 || got[0].SessionID != chatSessionID || got[0].Message != "take two" {
		t.Fatalf("SendHelp requests = %+v, want chat follow-up message", got)
	}
}

func TestAPIAppModelChatPendingAskUserSnapshotCanBeAnswered(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	model, _ := app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	chatting := model.(APIAppModel)
	chatting.chat.input.SetValue("ask me a question with 3 choices")
	model, cmd := chatting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want chat start command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	started := model.(APIAppModel)

	model, _ = started.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: server.SessionDetailDTO{
			SessionSummaryDTO: server.SessionSummaryDTO{ID: chatSessionID, FeatureID: chatSessionID, Phase: "research", Status: "WaitingHelp"},
			PendingControls: []server.ControlRequestDTO{{
				RequestID: "ask-1",
				SessionID: chatSessionID,
				FeatureID: chatSessionID,
				ToolName:  "AskUserQuestion",
				Status:    "pending",
				Questions: []server.AskUserQuestionDTO{{
					Question: "Pick a direction",
					Options: []server.AskUserOptionDTO{
						{Label: "Alpha", Description: "First option"},
						{Label: "Beta", Description: "Second option"},
						{Label: "Gamma", Description: "Third option"},
					},
				}},
			}},
		}},
		Transcript: &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
			{Index: 0, Role: "assistant", Type: "tool_progress", Tool: "AskUserQuestion", Redacted: true},
		}},
	}})
	waiting := model.(APIAppModel)

	if waiting.chat.responding {
		t.Fatal("chat remained responding after pending AskUserQuestion snapshot")
	}
	// With descriptions, each option takes 2 lines, so the small chat panel's
	// option window shows only the first option plus a scroll indicator.
	view := stripANSI(waiting.View().Content)
	for _, want := range []string{"Pick a direction", "Alpha", "more below", "Enter to select"} {
		if !strings.Contains(view, want) {
			t.Fatalf("chat view missing %q while waiting for AskUser answer:\n%s", want, view)
		}
	}
	if strings.Contains(view, "[esc] Background") {
		t.Fatalf("chat view still rendered responding footer:\n%s", view)
	}

	// Navigate the picker down to "Beta" and commit it — with a single
	// question this lands on the recap slot, so a second Enter submits.
	model, _ = waiting.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	after := model.(APIAppModel)
	if !after.chat.onRecapSlot() {
		t.Fatalf("expected recap slot after answering the only question, currentQuestionIdx=%d", after.chat.currentQuestionIdx)
	}
	model, cmd = after.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) on recap slot returned nil command, want AskUser answer command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	if len(client.helpRequests) != 0 {
		t.Fatalf("SendHelp requests = %+v, want none for AskUser answer", client.helpRequests)
	}
	if got := client.askUserAnswers; len(got) != 1 || got[0].RequestID != "ask-1" || got[0].SessionID != chatSessionID || got[0].Answers["Pick a direction"] != "Beta" {
		t.Fatalf("AnswerAskUser requests = %+v, want chat answer", got)
	}
}

func TestAPIAppModelChatPromptOnlyAskUserSnapshotCanBeAnswered(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	model, _ := app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	chatting := model.(APIAppModel)
	chatting.chat.input.SetValue("ask me a question with 3 choices")
	model, cmd := chatting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want chat start command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	started := model.(APIAppModel)

	model, _ = started.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: server.SessionDetailDTO{
			SessionSummaryDTO: server.SessionSummaryDTO{ID: chatSessionID, FeatureID: chatSessionID, Phase: "research", Status: "Running"},
		}},
		Transcript: &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
			{Index: 0, Role: "system", Type: "tool_progress", Tool: "AskUserQuestion", Redacted: true},
		}},
	}})
	thinking := model.(APIAppModel)
	if !thinking.chat.responding {
		t.Fatal("setup: chat should still be responding after tool_progress snapshot")
	}

	askControl := server.ControlRequestDTO{
		RequestID: "ask-1",
		SessionID: chatSessionID,
		FeatureID: chatSessionID,
		ToolName:  "AskUserQuestion",
		Status:    "pending",
		Questions: []server.AskUserQuestionDTO{{
			Question: "Pick a direction",
			Options: []server.AskUserOptionDTO{
				{Label: "Alpha"},
				{Label: "Beta"},
				{Label: "Gamma"},
			},
		}},
	}
	model, _ = thinking.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Prompts: &server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{askControl}},
	}})
	waiting := model.(APIAppModel)

	if waiting.chat.responding {
		t.Fatal("chat remained responding after prompt-only AskUserQuestion snapshot")
	}
	view := stripANSI(waiting.View().Content)
	for _, want := range []string{"Pick a direction", "Alpha", "Beta", "Gamma", "Enter to select"} {
		if !strings.Contains(view, want) {
			t.Fatalf("chat view missing %q after prompt-only AskUser snapshot:\n%s", want, view)
		}
	}

	// Navigate the picker down to "Gamma" and commit it — with a single
	// question this lands on the recap slot, so a second Enter submits.
	model, _ = waiting.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	after := model.(APIAppModel)
	if !after.chat.onRecapSlot() {
		t.Fatalf("expected recap slot after answering the only question, currentQuestionIdx=%d", after.chat.currentQuestionIdx)
	}
	model, cmd = after.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) on recap slot returned nil command, want AskUser answer command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	if got := client.askUserAnswers; len(got) != 1 || got[0].RequestID != "ask-1" || got[0].Answers["Pick a direction"] != "Gamma" {
		t.Fatalf("AnswerAskUser requests = %+v, want prompt-only chat answer", got)
	}

	model, _ = model.(APIAppModel).Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Prompts: &server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{askControl}},
	}})
	answered := model.(APIAppModel)
	view = stripANSI(answered.View().Content)
	occurrences := 0
	for _, turn := range answered.chat.turns {
		occurrences += strings.Count(turn.Text, "Pick a direction")
	}
	if occurrences != 1 {
		t.Fatalf("stale AskUser prompt was recorded %d times, want once: %+v", occurrences, answered.chat.turns)
	}
	if !answered.chat.responding {
		t.Fatalf("chat stopped waiting for assistant after stale AskUser prompt:\n%s", view)
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
		name     string
		key      tea.KeyPressMsg
		actionID string
		wantKind string
		accepted apiTestActionResponse
		cycle    *server.CycleDTO
		disabled bool
		refresh  struct {
			cycleType string
			wantLabel string
		}
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
			refresh: struct {
				cycleType string
				wantLabel string
			}{cycleType: "rebase", wantLabel: "Rebasing"},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.startRebaseFeatureIDs, ","); got != "active" {
					t.Fatalf("StartRebase calls = %q, want active", got)
				}
				if got := client.startRebaseRequests; len(got) != 1 || got[0].Repo != "" || got[0].RebaseTarget != "" || len(got[0].ConflictFiles) != 0 {
					t.Fatalf("StartRebase requests = %+v, want no repo or conflict inputs", got)
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
			refresh: struct {
				cycleType string
				wantLabel string
			}{cycleType: "tweak", wantLabel: "Tweaking"},
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
			model, cmd = model.(APIAppModel).Update(msg)
			accepted := model.(APIAppModel)

			tt.assertCall(t, client)

			if tt.refresh.wantLabel != "" {
				if cmd == nil {
					t.Fatalf("%s mutation result returned nil command, want immediate feature detail refresh", tt.name)
				}
				cycle := &server.CycleDTO{Type: tt.refresh.cycleType, Status: "running"}
				client.detail = server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
					FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "CodeReady", CurrentPhase: "publish", Cycle: cycle, CreatedAt: time.Now(), Repos: []string{"agentic-orchestrator"}},
					Cycle:          cycle,
					RepoStatus: []server.RepoStatusDTO{
						{Name: "agentic-orchestrator", Touched: true, Publishable: true, CycleType: tt.refresh.cycleType, CycleStatus: "running"},
					},
				}}
				msg = cmd()
				model, _ = accepted.Update(msg)
				accepted = model.(APIAppModel)
			}

			view := stripANSI(accepted.View().Content)
			wantStatus := "Completed " + apiMutationKindLabel(tt.wantKind)
			if tt.refresh.wantLabel != "" {
				wantStatus = "Started " + apiMutationKindLabel(tt.wantKind)
			}
			if !strings.Contains(view, wantStatus) {
				t.Fatalf("API app View() missing %q in:\n%s", wantStatus, view)
			}
			if tt.refresh.wantLabel != "" && !strings.Contains(view, tt.refresh.wantLabel) {
				t.Fatalf("API app View() missing refreshed cycle label %q in:\n%s", tt.refresh.wantLabel, view)
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

			cycle := &server.CycleDTO{Type: "tweak", Status: "running"}
			client := &fakeTUIAPIClient{
				features: server.FeatureListResponse{Features: []server.FeatureSummary{
					{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", Cycle: cycle, Repos: []string{"agentic-orchestrator"}, CreatedAt: time.Now()},
				}},
				detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
					FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", Cycle: cycle},
					Cycle:          cycle,
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

	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	editing = model.(APIAppModel)
	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	editing = model.(APIAppModel)
	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	editing = model.(APIAppModel)
	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	editing = model.(APIAppModel)
	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	edited := model.(APIAppModel)
	model, cmd = edited.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter in model picker) returned save command, want local picker close")
	}
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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

func TestAPIAppModelFeatureConfigEditorOpensForRunningFeature(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		featureConfig: server.FeatureConfigResponse{
			FeatureID: "active",
			Current: server.FeatureConfigDTO{
				Inquireness: "targeted",
				Pipeline:    "large",
			},
		},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if cmd == nil {
		t.Fatal("Update(e) returned nil command for running feature, want config fetch")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	editing := model.(APIAppModel)
	if got := strings.Join(client.featureConfigIDs, ","); got != "active" {
		t.Fatalf("FeatureConfig calls = %q, want active", got)
	}
	if editing.configEditor == nil {
		t.Fatal("configEditor is nil after loading running feature config")
	}
	if view := stripANSI(editing.View().Content); !strings.Contains(view, "next restart or next phase") {
		t.Fatalf("running feature config editor missing deferred-effect warning:\n%s", view)
	}
}

func TestAPIAppModelWorkspaceConfigEditorSavesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Defaults: config.ModelConfig{
				Inquiry:        "codex:gpt-5.4",
				Research:       "codex:gpt-5.4",
				Planning:       "codex:gpt-5.4",
				Implementation: "codex:gpt-5.4",
				Review:         "codex:gpt-5.4",
				KBBuild:        "codex:gpt-5.4",
			},
			FeatureDefaults: server.FeatureDefaultsDTO{
				Models: config.ModelConfig{
					Inquiry:        "codex:gpt-5.4",
					Research:       "codex:gpt-5.4",
					Planning:       "codex:gpt-5.4",
					Implementation: "codex:gpt-5.4",
					Review:         "codex:gpt-5.4",
					KBBuild:        "codex:gpt-5.4",
				},
				Inquireness: "medium",
				Pipeline:    "large",
				Checkpoints: config.Checkpoints{ManualPublish: true},
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
				Inquiry:  "codex:gpt-5.4",
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
	for _, want := range []string{"Edit Config · Workspace Defaults", "Models", "Behavior", "Gates", "Phases", "Agents", "Models for codex"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	if len(client.featureConfigIDs) != 0 {
		t.Fatalf("FeatureConfig calls = %v, want none for runtime config editor", client.featureConfigIDs)
	}

	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyDown})              // enter Models tab body
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyRight}) // agents
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyRight}) // models
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyDown})  // gpt-5.4 -> gpt-5.5
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter in model picker) returned save command, want local picker close")
	}
	edited := model.(APIAppModel)
	if edited.configEditor == nil {
		t.Fatal("configEditor closed before save")
	}
	edited.configEditor.editor.inquireness = feature.InquirenessHigh
	edited.configEditor.editor.checkpoints.RoadmapReview = true
	model, cmd = edited.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want runtime config update command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	saved := model.(APIAppModel)

	if got := client.updateRuntimeConfigRequests; len(got) != 1 ||
		got[0].Defaults.Models.Inquiry != "codex:gpt-5.5" ||
		got[0].Defaults.Inquireness != "high" ||
		!got[0].Defaults.Checkpoints.RoadmapReview {
		t.Fatalf("UpdateRuntimeConfig requests = %+v, want edited models, behavior, and gates", got)
	}
	if saved.configEditor != nil {
		t.Fatal("workspace config editor still open after successful save")
	}
	if saved.runtimeConfig.FeatureDefaults.Models.Inquiry != "codex:gpt-5.5" {
		t.Fatalf("runtime snapshot inquiry default = %q, want reloaded codex:gpt-5.5", saved.runtimeConfig.FeatureDefaults.Models.Inquiry)
	}
	view = stripANSI(saved.View().Content)
	for _, want := range []string{"Completed Runtime Config"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelWorkspaceConfigEditorIncludesUtilitiesAndDiscoveredRoleOptions(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Defaults: config.ModelConfig{
				Research:       "codex:gpt-5.4",
				Planning:       "codex:gpt-5.4",
				Implementation: "codex:gpt-5.4",
				Review:         "codex:gpt-5.4",
				Utilities:      "codex:gpt-5.4-mini",
				KBBuild:        "codex:gpt-5.4",
			},
			FeatureDefaults: server.FeatureDefaultsDTO{
				Models: config.ModelConfig{
					Research:       "codex:gpt-5.4",
					Planning:       "codex:gpt-5.4",
					Implementation: "codex:gpt-5.4",
					Review:         "codex:gpt-5.4",
					Utilities:      "codex:gpt-5.4-mini",
					KBBuild:        "codex:gpt-5.4",
				},
				Inquireness: "medium",
				Pipeline:    "large",
			},
			Providers: []string{"codex"},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{"codex"},
			ProviderModels: map[string][]server.ModelDTO{
				"codex": {
					{ID: "codex:gpt-5.4"},
					{ID: "codex:gpt-5.4-mini"},
					{ID: "codex:gpt-5.5-mini"},
				},
			},
			PhaseDefaults: config.ModelConfig{
				Utilities: "codex:gpt-5.4-mini",
			},
			PhaseProviderModels: map[string]map[string][]string{
				"chat": {"codex": {"codex:gpt-5.4-mini", "codex:gpt-5.5-mini"}},
			},
		},
		updateRuntimeConfigAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: 'E', Text: "E"})
	editing := model.(APIAppModel)
	view := stripANSI(editing.View().Content)
	for _, want := range []string{"Edit Config · Workspace Defaults", "Phases", "Agents", "Models for codex", "Utilities", "gpt-5.4-mini"} {
		if !strings.Contains(view, want) {
			t.Fatalf("runtime config view missing %q:\n%s", want, view)
		}
	}

	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // enter Models tab body
	for i := 0; i < 5; i++ {
		model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	editing = model.(APIAppModel)
	view = stripANSI(editing.View().Content)
	for _, want := range []string{"Utilities", "Models for codex", "gpt-5.4-mini", "gpt-5.5-mini"} {
		if !strings.Contains(view, want) {
			t.Fatalf("utilities choices missing %q:\n%s", want, view)
		}
	}

	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyRight})
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter in model picker) returned save command, want local picker close")
	}
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want runtime config update command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)

	if got := client.updateRuntimeConfigRequests; len(got) != 1 || got[0].Defaults.Models.Utilities != "codex:gpt-5.5-mini" {
		t.Fatalf("UpdateRuntimeConfig requests = %+v, want edited utilities default model", got)
	}
}

func TestAPIAppModelFeatureConfigEditorDoesNotExposeUtilities(t *testing.T) {
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
					{ID: "codex:gpt-5.4-mini"},
				},
			},
			PhaseDefaults: config.ModelConfig{
				Research:  "codex:gpt-5.4",
				Utilities: "codex:gpt-5.4-mini",
			},
			PhaseProviderModels: map[string]map[string][]string{
				"Research": {"codex": {"codex:gpt-5.4"}},
				"chat":     {"codex": {"codex:gpt-5.4-mini"}},
			},
		},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "active", Name: "Client cutover", Slug: "client-cutover", Status: "Published", CurrentPhase: "publish"},
			Pipeline:       "large",
		}},
		featureConfig: server.FeatureConfigResponse{
			FeatureID: "active",
			Current: server.FeatureConfigDTO{
				Models:      config.ModelConfig{Research: "codex:gpt-5.4", Utilities: "codex:gpt-5.4-mini"},
				Inquireness: "targeted",
				Checkpoints: server.CheckpointsDTO{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
				Pipeline:    "large",
			},
			Defaults: server.FeatureConfigDTO{
				Models:      config.ModelConfig{Research: "codex:gpt-5.4"},
				Inquireness: "targeted",
				Checkpoints: server.CheckpointsDTO{ManualPublish: true},
				Pipeline:    "large",
			},
			Publish: server.PublishabilityDTO{ManualPublish: true, Repos: map[string]bool{"api": true}},
		},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if cmd == nil {
		t.Fatal("Update(e) returned nil command, want feature config fetch")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	editing := model.(APIAppModel)
	view := stripANSI(editing.View().Content)
	if strings.Contains(view, "Utilities") {
		t.Fatalf("feature config editor exposed global Utilities field:\n%s", view)
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
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	prompting := model.(APIAppModel)
	if !prompting.ShowingPermissionPrompt() {
		t.Fatal("Update(a) did not enter attach permission prompt")
	}
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{"Allow Bash?", "Active work", "go test ./internal/tui", "[y] Allow", "[n] Deny"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	if len(client.permissionAnswers) != 0 {
		t.Fatalf("AnswerPermission calls = %v before decision, want none", client.permissionAnswers)
	}

	model, cmd = prompting.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("Update(y) returned nil command, want permission answer mutation")
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
	for _, want := range []string{"Active work", "Type a message"} {
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

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	prompting := model.(APIAppModel)
	if !prompting.ShowingAskUserPrompt() {
		t.Fatal("Update(a) did not enter attach ask-user prompt")
	}
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{"Which database?", "Type your answer", "Enter"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	if len(client.askUserAnswers) != 0 {
		t.Fatalf("AnswerAskUser calls = %v before answer, want none", client.askUserAnswers)
	}

	for _, ch := range "PostgreSQL" {
		model, cmd = prompting.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		prompting = model.(APIAppModel)
	}
	model, cmd = prompting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("first Update(enter) returned command, want review step before ask-user answer")
	}
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
		t.Fatal("ask-user question remained active after accepted answer")
	}
	view = stripANSI(answered.View().Content)
	for _, want := range []string{"[you] PostgreSQL", "Active work"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAttachSessionRendersRestoredLocalUserTranscriptAsYou(t *testing.T) {
	t.Parallel()

	sess := newAPIAttachSession(nil, server.SessionDetailDTO{
		SessionSummaryDTO: server.SessionSummaryDTO{
			ID:        "sess-1",
			FeatureID: "active",
			Phase:     "implement",
			Kind:      "phase",
			Status:    "Running",
		},
	}, server.TranscriptResponse{
		Messages: []server.TranscriptMessageDTO{
			{Index: 0, Role: "assistant", Type: "text", Text: "Which database?"},
			{Index: 1, Role: "user", Type: "text", Text: "PostgreSQL", LocallyAppended: true},
			{Index: 2, Role: "assistant", Type: "text", Text: "Active work"},
		},
	}, nil)

	m := attachModelFromSession(sess, 120, 40)
	view := stripANSI(m.View())
	for _, want := range []string{"[you] PostgreSQL", "Active work"} {
		if !strings.Contains(view, want) {
			t.Fatalf("reattached API transcript missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAttachSessionRendersProtocolUserTranscriptAsPrompt(t *testing.T) {
	t.Parallel()

	var transcript server.TranscriptResponse
	if err := json.Unmarshal([]byte(`{
		"messages": [{
			"index": 0,
			"role": "user",
			"type": "text",
			"text": "Translate README in Neapolitan."
		}, {
			"index": 1,
			"role": "user",
			"type": "text",
			"text": "Replace the existing README",
			"locally_appended": true
		}]
	}`), &transcript); err != nil {
		t.Fatalf("unmarshal transcript: %v", err)
	}
	sess := newAPIAttachSession(nil, server.SessionDetailDTO{
		SessionSummaryDTO: server.SessionSummaryDTO{
			ID:        "sess-1",
			FeatureID: "active",
			Phase:     "implement",
			Kind:      "phase",
			Status:    "Running",
		},
	}, transcript, nil)

	view := stripANSI(renderAttachMessages(sess.MessageLog().Messages(), filterAll, 120, nil))
	for _, want := range []string{"prompt", "Translate README in Neapolitan.", "[you] Replace the existing README"} {
		if !strings.Contains(view, want) {
			t.Fatalf("reattached API transcript missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "[you] Translate README in Neapolitan.") {
		t.Fatalf("protocol prompt rendered as local user echo:\n%s", view)
	}
}

func TestAPIControlRequestMessagePreservesRESTToolInput(t *testing.T) {
	t.Parallel()

	var req server.ControlRequestDTO
	if err := json.Unmarshal([]byte(`{
		"request_id": "perm-1",
		"session_id": "sess-1",
		"feature_id": "active",
		"tool_name": "Bash",
		"status": "pending",
		"summary": "Bash requested",
		"input": {"command": "go test ./internal/tui"}
	}`), &req); err != nil {
		t.Fatalf("unmarshal control request: %v", err)
	}

	msg := apiControlRequestMessage(req)
	var payload map[string]string
	if err := json.Unmarshal(msg.Request.Input, &payload); err != nil {
		t.Fatalf("json.Unmarshal(input): %v", err)
	}
	if got, want := payload["command"], "go test ./internal/tui"; got != want {
		t.Fatalf("payload[command] = %q; want %q", got, want)
	}
	if payload["summary"] != "" {
		t.Fatalf("payload[summary] = %q; want raw tool input without summary fallback", payload["summary"])
	}
}

func TestAPIControlRequestMessagePrefersRESTAskUserInput(t *testing.T) {
	t.Parallel()

	fullQuestion := "Which persistence strategy should the orchestrator use when an AskUserQuestion contains enough detail that the read API truncates the display projection, but the provider still requires the exact original question text as the answer-map key?"
	displayQuestion := fullQuestion[:180]
	req := server.ControlRequestDTO{
		RequestID: "ask-1",
		SessionID: "sess-1",
		FeatureID: "active",
		ToolName:  "AskUserQuestion",
		Status:    "pending",
		Input: map[string]any{
			"questions": []any{
				map[string]any{
					"question": fullQuestion,
					"options": []any{
						map[string]any{"label": "Full input"},
					},
				},
			},
		},
		Questions: []server.AskUserQuestionDTO{{
			Question: displayQuestion,
			Options:  []server.AskUserOptionDTO{{Label: "Display projection"}},
		}},
	}

	msg := apiControlRequestMessage(req)
	questions := parseAskUserQuestions(msg.Request.Input)
	if len(questions) != 1 {
		t.Fatalf("parseAskUserQuestions() length = %d, want 1 from REST input", len(questions))
	}
	if got := questions[0].RawQuestion; got != fullQuestion {
		t.Fatalf("AskUser question = %q; want full REST input question", got)
	}
	if got := questions[0].Options[0].Label; got != "Full input" {
		t.Fatalf("AskUser option = %q; want REST input option", got)
	}
}

func TestAPIAttachSessionRendersFileChangeTranscriptRows(t *testing.T) {
	t.Parallel()

	var transcript server.TranscriptResponse
	if err := json.Unmarshal([]byte(`{
		"messages": [{
			"index": 0,
			"role": "assistant",
			"type": "tool_use",
			"tool": "Write",
			"redacted": true,
			"file_change": {
				"path": "docs/provider-notes.md",
				"operation": "write",
				"detail": "+ # Provider notes\n+ \n+ Updated for all providers."
			}
		}]
	}`), &transcript); err != nil {
		t.Fatalf("unmarshal transcript: %v", err)
	}
	sess := newAPIAttachSession(nil, server.SessionDetailDTO{
		SessionSummaryDTO: server.SessionSummaryDTO{
			ID:        "sess-1",
			FeatureID: "active",
			Phase:     "implement",
			Kind:      "phase",
			Status:    "Running",
		},
	}, transcript, nil)

	m := attachModelFromSession(sess, 120, 40)
	view := stripANSI(m.View())
	for _, want := range []string{"Write(docs/provider-notes.md)", "+ # Provider notes", "+ Updated for all providers."} {
		if !strings.Contains(view, want) {
			t.Fatalf("reattached API transcript missing file change %q in:\n%s", want, view)
		}
	}
}

func TestAPIAttachSessionRendersTaskLifecycleAndDelegationRows(t *testing.T) {
	t.Parallel()

	var transcript server.TranscriptResponse
	if err := json.Unmarshal([]byte(`{
		"api_version": "v1",
		"cursor": {"total": 4, "start": 0, "end": 4},
		"messages": [{
			"index": 0,
			"role": "assistant",
			"type": "tool_use",
			"tool": "Agent",
			"redacted": true,
			"tool_call": {
				"summary": "Explore KB completion handler",
				"prompt": "Inspect KB docs and return the impacted categories."
			}
		}, {
			"index": 1,
			"role": "system",
			"type": "task_started",
			"redacted": true,
			"task": {
				"id": "task-1",
				"description": "inspect provider docs",
				"task_type": "local_agent",
				"prompt": "Read the provider docs and report every attach-view metadata gap."
			}
		}, {
			"index": 2,
			"role": "system",
			"type": "task_progress",
			"redacted": true,
			"task": {
				"id": "task-1",
				"description": "inspect provider docs",
				"last_tool_name": "Read"
			}
		}, {
			"index": 3,
			"role": "system",
			"type": "task_notification",
			"redacted": true,
			"task": {
				"id": "task-1",
				"status": "completed",
				"summary": "found API transcript gaps"
			}
		}]
	}`), &transcript); err != nil {
		t.Fatalf("unmarshal transcript: %v", err)
	}
	sess := newAPIAttachSession(nil, server.SessionDetailDTO{
		SessionSummaryDTO: server.SessionSummaryDTO{
			ID:        "sess-1",
			FeatureID: "active",
			Phase:     "implement",
			Kind:      "phase",
			Status:    "Running",
		},
	}, transcript, nil)

	view := stripANSI(renderAttachMessages(sess.MessageLog().Messages(), filterAll, 120, nil))
	for _, want := range []string{
		"Agent: Explore KB completion handler",
		"Inspect KB docs and return the impacted categories.",
		"Task started: inspect provider docs",
		"Read the provider docs and report every attach-view metadata gap.",
		"Task progress: inspect provider docs via Read",
		"Task completed: found API transcript gaps",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("reattached API transcript missing task/delegation row %q in:\n%s", want, view)
		}
	}
}

func TestAPIAttachRefreshDoesNotDuplicateRestoredLocalUserEcho(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{askUserAccepted: apiTestActionResponse{}}
	sess := newAPIAttachSession(client, server.SessionDetailDTO{
		SessionSummaryDTO: server.SessionSummaryDTO{
			ID:        "sess-1",
			FeatureID: "active",
			Phase:     "implement",
			Kind:      "phase",
			Status:    "Running",
		},
	}, server.TranscriptResponse{}, nil)
	sess.MessageLog().Append(llm.SDKMessage{
		Type:            "user",
		LocallyAppended: true,
		User: &llm.UserMessage{
			Message: llm.ConversationMsg{
				Role:    "user",
				Content: []llm.ContentBlock{{Type: "text", Text: "PostgreSQL"}},
			},
		},
	})
	if err := sess.RespondToAskUser("ask-1", json.RawMessage(`{"questions":[{"question":"Which database?"}]}`), map[string]string{
		"Which database?": "PostgreSQL",
	}, nil); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}

	newMessages := sess.applyAPISessionSnapshot(server.SessionDetailDTO{
		SessionSummaryDTO: server.SessionSummaryDTO{
			ID:        "sess-1",
			FeatureID: "active",
			Phase:     "implement",
			Kind:      "phase",
			Status:    "Running",
		},
	}, &server.TranscriptResponse{
		Messages: []server.TranscriptMessageDTO{
			{Index: 0, Role: "user", Type: "text", Text: "PostgreSQL", LocallyAppended: true},
		},
	}, nil)

	if len(newMessages) != 0 {
		t.Fatalf("applyAPISessionSnapshot returned %d messages, want duplicate local echo skipped", len(newMessages))
	}
	view := stripANSI(renderAttachMessages(sess.MessageLog().Messages(), filterAll, 120, nil))
	if got := strings.Count(view, "[you] PostgreSQL"); got != 1 {
		t.Fatalf("rendered [you] PostgreSQL count = %d, want 1 in:\n%s", got, view)
	}
}

func TestAPIAppModelAskUserAnswerUsesFullInputQuestionKey(t *testing.T) {
	t.Parallel()

	fullQuestion := "Which persistence strategy should the orchestrator use when an AskUserQuestion contains enough detail that the read API truncates the display projection, but the provider still requires the exact original question text as the answer-map key?"
	displayQuestion := fullQuestion[:180]
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		prompts: server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{
			{
				RequestID: "ask-1",
				FeatureID: "active",
				ToolName:  "AskUserQuestion",
				Status:    "pending",
				Summary:   displayQuestion,
				Input: map[string]any{
					"questions": []any{
						map[string]any{
							"question": fullQuestion,
							"options":  []any{},
						},
					},
				},
				Questions: []server.AskUserQuestionDTO{{Question: displayQuestion}},
			},
		}},
		askUserAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	prompting := model.(APIAppModel)
	if !prompting.ShowingAskUserPrompt() {
		t.Fatal("Update(u) did not enter API ask-user prompt")
	}
	for _, ch := range "Use the full input" {
		model, cmd = prompting.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		prompting = model.(APIAppModel)
	}
	model, cmd = prompting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want ask-user answer mutation")
	}
	_, _ = model.(APIAppModel).Update(cmd())

	if got := client.askUserAnswers; len(got) != 1 || got[0].Answers[fullQuestion] != "Use the full input" {
		t.Fatalf("AnswerAskUser requests = %+v, want answer keyed by full original question", got)
	}
	if got := client.askUserAnswers[0].Answers[displayQuestion]; got != "" {
		t.Fatalf("AnswerAskUser used truncated display question key with answer %q", got)
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
	for _, want := range []string{"Which database?", "PostgreSQL", "relational", "DynamoDB", "managed key-value"} {
		if !strings.Contains(view, want) {
			t.Fatalf("AskUser option prompt missing %q in:\n%s", want, view)
		}
	}

	model, _ = prompting.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("first Update(enter) returned command, want review step before ask-user answer")
	}
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want ask-user answer")
	}
	msg := cmd()
	_, _ = model.(APIAppModel).Update(msg)
	if got := client.askUserAnswers; len(got) != 1 || got[0].Answers["Which database?"] != "DynamoDB" {
		t.Fatalf("AnswerAskUser requests = %+v, want DynamoDB answer", got)
	}
}

func TestAPIAppModelChatPromptOnlyAskUserSnapshotShowsReadableLongText(t *testing.T) {
	t.Parallel()

	longQuestion := "Should TUI/UI label names that match what is displayed on screen, including In Progress, Published, Watch, Answer, Approve, and Publish as PR, be translated into the target language or kept in English so the reader can map the README back to the live interface without losing important workflow context?"
	longDescription := "Translate all prose including TUI labels. The README is a localized document, and describing what the screen says in English breaks immersion even though the reader can still match the workflow by position, status, and surrounding context."
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "design", CreatedAt: time.Now()},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	model, _ := app.Update(tea.WindowSizeMsg{Width: 320, Height: 60})
	app = model.(APIAppModel)
	model, _ = app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	chatting := model.(APIAppModel)
	chatting.chat.input.SetValue("ask the full translation policy question")
	model, cmd := chatting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want chat start command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	thinking := model.(APIAppModel)

	askControl := server.ControlRequestDTO{
		RequestID: "ask-long",
		SessionID: chatSessionID,
		FeatureID: chatSessionID,
		ToolName:  "AskUserQuestion",
		Status:    "pending",
		Questions: []server.AskUserQuestionDTO{{
			Question: longQuestion,
			Options: []server.AskUserOptionDTO{{
				Label:       "Translate TUI labels too (Recommended)",
				Description: longDescription,
			}},
		}},
	}
	model, _ = thinking.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Prompts: &server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{askControl}},
	}})
	waiting := model.(APIAppModel)

	if waiting.chat.responding {
		t.Fatal("chat remained responding after prompt-only AskUserQuestion snapshot")
	}
	view := stripANSI(waiting.View().Content)
	for _, want := range []string{
		"losing important workflow context?",
		"Translate TUI labels too (Recommended)",
		"Enter to select",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("chat view missing %q after long AskUser snapshot:\n%s", want, view)
		}
	}
	for _, notWant := range []string{"target languag...", "looking...", "match..."} {
		if strings.Contains(view, notWant) {
			t.Fatalf("chat view contains truncated AskUser text %q:\n%s", notWant, view)
		}
	}
}

func TestAPIAppModelAttachRefreshActivatesAskUserPrompt(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: "sess-1", FeatureID: "active", Phase: "implement", Kind: "phase", Status: "Running"},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	attached := model.(APIAppModel)
	if attached.ShowingAskUserPrompt() {
		t.Fatal("attach unexpectedly started with ask-user prompt")
	}

	model, _ = attached.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Prompts: &server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{
			{
				RequestID: "ask-1",
				SessionID: "sess-1",
				FeatureID: "active",
				ToolName:  "AskUserQuestion",
				Status:    "pending",
				Summary:   "Pick database",
				Questions: []server.AskUserQuestionDTO{{Question: "Pick database"}},
			},
		}},
	}})
	prompting := model.(APIAppModel)
	if !prompting.ShowingAskUserPrompt() {
		t.Fatal("prompt refresh did not activate attach ask-user prompt")
	}
	if view := stripANSI(prompting.View().Content); !strings.Contains(view, "Pick database") || strings.Contains(view, "AskUser question") {
		t.Fatalf("prompt refresh did not render main attach question UI:\n%s", view)
	}
}

func TestAPIAppModelAttachAskUserAnswerDoesNotReactivateCachedPrompt(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "plan", CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: "sess-1", FeatureID: "active", Phase: "plan", Kind: "phase", Status: "WaitingHelp"},
		}},
		prompts: server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{
			{
				RequestID: "ask-1",
				SessionID: "sess-1",
				FeatureID: "active",
				ToolName:  "AskUserQuestion",
				Status:    "pending",
				Summary:   "Which README?",
				Questions: []server.AskUserQuestionDTO{{
					Question: "Which README?",
					Options:  []server.AskUserOptionDTO{{Label: "Root README"}},
				}},
			},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	attached := model.(APIAppModel)
	if !attached.ShowingAskUserPrompt() {
		t.Fatal("attach should start with cached AskUser prompt")
	}

	model, cmd = attached.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("first Enter should move to recap without dispatching")
	}
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("second Enter should dispatch AskUser answer")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	answered := model.(APIAppModel)
	if answered.ShowingAskUserPrompt() {
		t.Fatal("AskUser prompt remained active after answer")
	}

	model, _ = answered.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	detached := model.(APIAppModel)
	if detached.attach != nil {
		t.Fatal("Esc should detach from attach view")
	}

	model, _ = detached.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	reattached := model.(APIAppModel)
	if reattached.ShowingAskUserPrompt() {
		t.Fatalf("answered AskUser prompt reactivated from cached prompts:\n%s", stripANSI(reattached.View().Content))
	}
}

func TestAPIAppModelAttachRefreshUpdatesStreamingTranscriptRow(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: "sess-1", FeatureID: "active", Phase: "implement", Kind: "phase", Status: "Running"},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	attached := model.(APIAppModel)
	partialText := "I found the workspace is a"
	fullText := "I found the workspace is a monorepo with README docs."
	firstSnapshot := server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: server.SessionDetailDTO{SessionSummaryDTO: server.SessionSummaryDTO{
			ID: "sess-1", FeatureID: "active", Phase: "implement", Kind: "phase", Status: "Running",
		}}},
		Transcript: &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
			{Index: 0, Role: "assistant", Type: "text", Text: partialText},
		}},
	}

	model, _ = attached.Update(apiRefreshSnapshotMsg{snapshot: firstSnapshot})
	attached = model.(APIAppModel)
	if view := stripANSI(attached.View().Content); !strings.Contains(view, partialText) {
		t.Fatalf("attach view missing partial transcript row:\n%s", view)
	}

	secondSnapshot := firstSnapshot
	secondSnapshot.Transcript = &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
		{Index: 0, Role: "assistant", Type: "text", Text: fullText},
	}}
	model, _ = attached.Update(apiRefreshSnapshotMsg{snapshot: secondSnapshot})
	updated := model.(APIAppModel)
	view := stripANSI(updated.View().Content)
	if !strings.Contains(view, "monorepo with README docs") {
		t.Fatalf("attach view did not update repeated transcript row:\n%s", view)
	}
	if count := strings.Count(view, partialText); count != 1 {
		t.Fatalf("attach view rendered repeated transcript row %d times, want 1:\n%s", count, view)
	}
}

func TestAPIAppModelAttachRendersSessionInitialPrompt(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: "sess-1", FeatureID: "active", Phase: "implement", Kind: "phase", Status: "Running"},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	app.storeSessionDetail(server.SessionDetailResponse{Session: server.SessionDetailDTO{
		SessionSummaryDTO: server.SessionSummaryDTO{
			ID: "sess-1", FeatureID: "active", Phase: "implement", Kind: "phase", Status: "Running",
		},
		InitialPrompt: "Implement the user-visible attach header.",
	}})
	app.storeTranscript("sess-1", server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
		{Index: 0, Role: "assistant", Type: "text", Text: "Working on it."},
	}})

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	view := stripANSI(model.(APIAppModel).View().Content)
	if !strings.Contains(view, "Implement the user-visible attach header.") {
		t.Fatalf("attach view missing initial prompt:\n%s", view)
	}
	if !strings.Contains(view, "Working on it.") {
		t.Fatalf("attach view missing transcript row:\n%s", view)
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
		{
			name: "rewind proceeds with target phase",
			msg:  RewindReviewDecisionMsg{FeatureID: "active", Phase: feature.PhasePlan, Decision: "proceed"},
			wantReq: server.ReviewDecisionRequest{
				Decision: "proceed",
				Phase:    "plan",
				IsRewind: true,
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

func TestAPIAppModelContextualActionOpensNeedsReviewArtifact(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	roadmapPath := filepath.Join(tmp, "roadmap.md")
	if err := os.WriteFile(roadmapPath, []byte("# Roadmap\n\nTranslate README.\n"), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{
				ID:           "active",
				Name:         "Translate README",
				Slug:         "translate-readme",
				Status:       "PlanNeedsReview",
				CurrentPhase: "plan",
				ActiveRun:    1,
				CreatedAt:    time.Now(),
				Progress: server.FeatureProgress{
					CurrentRoadmapPhase: 0,
					TotalRoadmapPhases:  3,
				},
			},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{
				ID:           "active",
				Name:         "Translate README",
				Slug:         "translate-readme",
				Status:       "PlanNeedsReview",
				CurrentPhase: "plan",
				ActiveRun:    1,
				Progress: server.FeatureProgress{
					CurrentRoadmapPhase: 0,
					TotalRoadmapPhases:  3,
				},
			},
			ActiveRun: &server.RunSummaryDTO{
				RunNumber:     1,
				CurrentPhase:  "plan",
				RoadmapPhase:  0,
				RoadmapTotal:  3,
				ArtifactCount: 1,
			},
			Models: config.ModelConfig{Utilities: "test-utility"},
		}},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: "roadmap", RunNumber: 1, Phase: "plan", Path: roadmapPath, Size: 28, ContentAvailable: true},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	if view := stripANSI(app.View().Content); !strings.Contains(view, "Roadmap needs review") {
		t.Fatalf("API app View() missing review hint before action:\n%s", view)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	updated := model.(APIAppModel)

	if updated.artifactReview == nil {
		t.Fatalf("pressing a did not open artifact review; statusMessage=%q", updated.statusMessage)
	}
	if got := updated.artifactReview.FeatureID(); got != "active" {
		t.Fatalf("artifactReview.FeatureID() = %q, want active", got)
	}
	if got := updated.artifactReview.ReviewMode(); got != "plan" {
		t.Fatalf("artifactReview.ReviewMode() = %q, want plan", got)
	}
	if got := updated.artifactReview.ArtifactPath(); got != roadmapPath {
		t.Fatalf("artifactReview.ArtifactPath() = %q, want %q", got, roadmapPath)
	}
	if strings.Contains(updated.statusMessage, "No contextual action") {
		t.Fatalf("statusMessage = %q, want artifact review opened", updated.statusMessage)
	}

	model, _ = updated.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("artifact review proceed returned nil command")
	}
	msg := cmd()
	model, cmd = model.(APIAppModel).Update(msg)
	if cmd == nil {
		t.Fatal("review decision message returned nil REST command")
	}
	model, _ = model.(APIAppModel).Update(cmd())

	wantReq := server.ReviewDecisionRequest{Decision: "proceed", Roadmap: true}
	if len(client.reviewRequests) != 1 || client.reviewRequests[0] != wantReq {
		t.Fatalf("reviewRequests = %+v, want %+v", client.reviewRequests, wantReq)
	}
}

func TestAPIAppModelContextualActionOpensMediumRewindDescriptionReview(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	descPath := filepath.Join(tmp, "description-review.md")
	if err := os.WriteFile(descPath, []byte("translate readme in Sicilian"), 0o644); err != nil {
		t.Fatalf("write description review: %v", err)
	}

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{
				ID:           "active",
				Name:         "Translate README",
				Slug:         "translate-readme",
				Status:       "DesignNeedsReview",
				CurrentPhase: "design",
				ActiveRun:    2,
				RunCount:     2,
				CreatedAt:    time.Now(),
			},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{
				ID:           "active",
				Name:         "Translate README",
				Slug:         "translate-readme",
				Status:       "DesignNeedsReview",
				CurrentPhase: "design",
				ActiveRun:    2,
				RunCount:     2,
			},
			Pipeline: "medium",
			ActiveRun: &server.RunSummaryDTO{
				RunNumber:          2,
				CurrentPhase:       "design",
				PendingReviewPhase: "plan",
				IsRewind:           true,
				ArtifactCount:      1,
			},
			Models: config.ModelConfig{Utilities: "test-utility"},
		}},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: "roadmap", RunNumber: 2, Phase: "plan", Path: filepath.Join(tmp, "roadmap.md"), Size: 1, ContentAvailable: true},
			{ID: "description-review", RunNumber: 2, Phase: "description", Path: descPath, Size: 27, ContentAvailable: true},
		}},
		reviewAccepted: apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	if view := stripANSI(app.View().Content); !strings.Contains(view, "Rewind to Plan needs review") {
		t.Fatalf("API app View() missing rewind review hint:\n%s", view)
	}

	model, _ := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	updated := model.(APIAppModel)
	if updated.artifactReview == nil {
		t.Fatalf("pressing a did not open artifact review; statusMessage=%q", updated.statusMessage)
	}
	if got := updated.artifactReview.ReviewMode(); got != "rewind" {
		t.Fatalf("artifactReview.ReviewMode() = %q, want rewind", got)
	}
	if got := updated.artifactReview.ArtifactPath(); got != descPath {
		t.Fatalf("artifactReview.ArtifactPath() = %q, want %q", got, descPath)
	}

	model, _ = updated.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("artifact review proceed returned nil command")
	}
	msg := cmd()
	model, cmd = model.(APIAppModel).Update(msg)
	if cmd == nil {
		t.Fatal("rewind review decision message returned nil REST command")
	}
	model, _ = model.(APIAppModel).Update(cmd())

	wantReq := server.ReviewDecisionRequest{Decision: "proceed", Phase: "plan", IsRewind: true}
	if len(client.reviewRequests) != 1 || client.reviewRequests[0] != wantReq {
		t.Fatalf("reviewRequests = %+v, want %+v", client.reviewRequests, wantReq)
	}
}

func TestAPIAppModelContextualActionRejectsStaleRewindReviewArtifact(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	roadmapPath := filepath.Join(tmp, "roadmap.md")
	if err := os.WriteFile(roadmapPath, []byte("# Old Roadmap\n"), 0o644); err != nil {
		t.Fatalf("write stale roadmap artifact: %v", err)
	}

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{
				ID:           "active",
				Name:         "Translate README",
				Slug:         "translate-readme",
				Status:       "PromptNeedsReview",
				CurrentPhase: "knowledge-base",
				ActiveRun:    2,
				RunCount:     2,
				CreatedAt:    time.Now(),
			},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{
				ID:           "active",
				Name:         "Translate README",
				Slug:         "translate-readme",
				Status:       "PromptNeedsReview",
				CurrentPhase: "knowledge-base",
				ActiveRun:    2,
				RunCount:     2,
			},
			Pipeline: "large",
			ActiveRun: &server.RunSummaryDTO{
				RunNumber:          2,
				CurrentPhase:       "knowledge-base",
				PendingReviewPhase: "inquire",
				IsRewind:           true,
				ArtifactCount:      1,
			},
			Models: config.ModelConfig{Utilities: "test-utility"},
		}},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: "roadmap", RunNumber: 2, Phase: "plan", Path: roadmapPath, Size: 14, ContentAvailable: true},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	updated := model.(APIAppModel)
	if updated.artifactReview != nil {
		t.Fatalf("pressing a opened stale artifact review for %q; want fetch for rewind prompt artifact", updated.artifactReview.ArtifactPath())
	}
	if cmd == nil {
		t.Fatalf("pressing a returned nil command; want review artifact refresh, statusMessage=%q", updated.statusMessage)
	}
	if updated.statusMessage != "Loading review artifact" {
		t.Fatalf("statusMessage = %q, want Loading review artifact", updated.statusMessage)
	}
}

func TestAPIAppModelContextualActionRejectsStaleNonRewindGateArtifact(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	questionsPath := filepath.Join(tmp, "questions.md")
	if err := os.WriteFile(questionsPath, []byte("# Questions\n"), 0o644); err != nil {
		t.Fatalf("write stale questions artifact: %v", err)
	}

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{
				ID:           "active",
				Name:         "Translate README",
				Slug:         "translate-readme",
				Status:       "ResearchNeedsReview",
				CurrentPhase: "research",
				ActiveRun:    1,
				RunCount:     1,
				CreatedAt:    time.Now(),
			},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{
				ID:           "active",
				Name:         "Translate README",
				Slug:         "translate-readme",
				Status:       "ResearchNeedsReview",
				CurrentPhase: "research",
				ActiveRun:    1,
				RunCount:     1,
			},
			Pipeline: "moonshot",
			ActiveRun: &server.RunSummaryDTO{
				RunNumber:          1,
				CurrentPhase:       "research",
				PendingReviewPhase: "design",
				ArtifactCount:      1,
			},
			Models: config.ModelConfig{Utilities: "test-utility"},
		}},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: "inquire", RunNumber: 1, Phase: "inquire", Path: questionsPath, Size: 12, ContentAvailable: true},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	if view := stripANSI(app.View().Content); !strings.Contains(view, "Research needs review") {
		t.Fatalf("API app View() should label the reviewed research artifact, not the design target:\n%s", view)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	updated := model.(APIAppModel)
	if updated.artifactReview != nil {
		t.Fatalf("pressing a opened stale artifact review for %q; want fetch for research artifact", updated.artifactReview.ArtifactPath())
	}
	if cmd == nil {
		t.Fatalf("pressing a returned nil command; want review artifact refresh, statusMessage=%q", updated.statusMessage)
	}
	if updated.statusMessage != "Loading review artifact" {
		t.Fatalf("statusMessage = %q, want Loading review artifact", updated.statusMessage)
	}
}

func TestAPIAppModelContextualActionRejectsDetachedStaleReviewAfterRewind(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	roadmapPath := filepath.Join(tmp, "roadmap.md")
	if err := os.WriteFile(roadmapPath, []byte("# Old Roadmap\n"), 0o644); err != nil {
		t.Fatalf("write stale roadmap artifact: %v", err)
	}

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{
				ID:           "active",
				Name:         "Translate README",
				Slug:         "translate-readme",
				Status:       "PromptNeedsReview",
				CurrentPhase: "knowledge-base",
				ActiveRun:    2,
				RunCount:     2,
				CreatedAt:    time.Now(),
			},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{
				ID:           "active",
				Name:         "Translate README",
				Slug:         "translate-readme",
				Status:       "PromptNeedsReview",
				CurrentPhase: "knowledge-base",
				ActiveRun:    2,
				RunCount:     2,
			},
			Pipeline: "large",
			ActiveRun: &server.RunSummaryDTO{
				RunNumber:          2,
				CurrentPhase:       "knowledge-base",
				PendingReviewPhase: "inquire",
				IsRewind:           true,
				ArtifactCount:      1,
			},
			Models: config.ModelConfig{Utilities: "test-utility"},
		}},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: "roadmap", RunNumber: 2, Phase: "plan", Path: roadmapPath, Size: 14, ContentAvailable: true},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	stale := NewArtifactReviewModel(roadmapPath, "active", "plan", feature.PhasePlan, app.width, app.height, nil, "", nil)
	stale.detached = true
	app.artifactReview = &stale

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	updated := model.(APIAppModel)
	if updated.artifactReview != nil && !updated.artifactReview.Detached() {
		t.Fatalf("pressing a reattached stale %s review for %q", updated.artifactReview.ReviewMode(), updated.artifactReview.ArtifactPath())
	}
	if cmd == nil {
		t.Fatalf("pressing a returned nil command; want review artifact refresh, statusMessage=%q", updated.statusMessage)
	}
	if updated.statusMessage != "Loading review artifact" {
		t.Fatalf("statusMessage = %q, want Loading review artifact", updated.statusMessage)
	}
}

func TestAPIAppModelContextualActionLoadsReviewArtifactWhenContentCacheEmpty(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	descPath := filepath.Join(tmp, "description-review.md")
	if err := os.WriteFile(descPath, []byte("translate readme in Sicilian"), 0o644); err != nil {
		t.Fatalf("write description review: %v", err)
	}

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{
				ID:           "active",
				Name:         "Translate README",
				Slug:         "translate-readme",
				Status:       "DesignNeedsReview",
				CurrentPhase: "design",
				ActiveRun:    2,
				RunCount:     2,
				CreatedAt:    time.Now(),
			},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{
				ID:           "active",
				Name:         "Translate README",
				Slug:         "translate-readme",
				Status:       "DesignNeedsReview",
				CurrentPhase: "design",
				ActiveRun:    2,
				RunCount:     2,
			},
			Pipeline: "medium",
			ActiveRun: &server.RunSummaryDTO{
				RunNumber:          2,
				CurrentPhase:       "design",
				PendingReviewPhase: "plan",
				IsRewind:           true,
				ArtifactCount:      1,
			},
			Models: config.ModelConfig{Utilities: "test-utility"},
		}},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: "description-review", RunNumber: 2, Phase: "description", Path: descPath, Size: 27, ContentAvailable: true},
		}},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	delete(app.contents, "active")
	app.rebuildPresentation("active")
	initialArtifactListCalls := len(client.artifactListFeatureIDs)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatalf("pressing a returned nil command; statusMessage=%q", model.(APIAppModel).statusMessage)
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	updated := model.(APIAppModel)

	if updated.artifactReview == nil {
		t.Fatalf("review artifact load did not open review; statusMessage=%q", updated.statusMessage)
	}
	if got := updated.artifactReview.ReviewMode(); got != "rewind" {
		t.Fatalf("artifactReview.ReviewMode() = %q, want rewind", got)
	}
	if got := updated.artifactReview.ArtifactPath(); got != descPath {
		t.Fatalf("artifactReview.ArtifactPath() = %q, want %q", got, descPath)
	}
	if got := len(client.artifactListFeatureIDs); got <= initialArtifactListCalls {
		t.Fatalf("ArtifactList calls = %d, want more than initial %d", got, initialArtifactListCalls)
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
	model, _ := app.Update(tea.WindowSizeMsg{Width: 180, Height: 48})
	app = model.(APIAppModel)

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
	if strings.Contains(view, "Features") {
		t.Fatalf("review comments preview rendered over dashboard instead of full-screen surface:\n%s", view)
	}
	if maxPlainLineWidth(view) < 130 {
		t.Fatalf("review comments preview did not use wide terminal space:\n%s", view)
	}
	viewLines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(viewLines) < 46 {
		t.Fatalf("review comments preview rendered %d lines, want at least 46 for a 48-line terminal:\n%s", len(viewLines), view)
	}
	if !strings.Contains(viewLines[len(viewLines)-1], "[Shift+A] Address all 1") {
		t.Fatalf("review comments preview footer not on final rendered line:\n%s", view)
	}
	for _, want := range []string{
		"Review Comments",
		"Active work",
		"agentic-orchestrator",
		"1 pending",
		"1 included",
		"Queue",
		"Detail",
		"@reviewer",
		"internal/tui/api_app.go:42",
		"use REST DTOs here",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"[Shift+A] Address all 1",
		"[enter] Address included 1",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}

	model, cmd = previewing.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd != nil {
		t.Fatal("Update(a) started review-comments; want Shift+A only")
	}
	previewing = model.(APIAppModel)

	_, cmd = previewing.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want included review-comments start mutation")
	}
	_ = cmd()
	if got := client.startReviewCommentsRequests; len(got) != 1 || len(got[0].Comments) != 1 || got[0].Comments[0].ID != 101 {
		t.Fatalf("StartReviewComments via enter = %+v, want one included comment 101", got)
	}
	client.startReviewCommentsFeatureIDs = nil
	client.startReviewCommentsRequests = nil

	model, cmd = previewing.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})
	if cmd == nil {
		t.Fatal("Update(Shift+A) returned nil command, want review-comments start mutation")
	}
	msg = cmd()
	model, cmd = model.(APIAppModel).Update(msg)
	started := model.(APIAppModel)

	if got := strings.Join(client.startReviewCommentsFeatureIDs, ","); got != "active" {
		t.Fatalf("StartReviewComments feature IDs = %q, want active", got)
	}
	if got := client.startReviewCommentsRequests; len(got) != 1 || got[0].Repo != "agentic-orchestrator" || got[0].Mode != "auto" || len(got[0].Comments) != 1 || got[0].Comments[0].ID != 101 || got[0].Comments[0].DiffHunk == "" {
		t.Fatalf("StartReviewComments requests = %+v, want agentic-orchestrator auto with previewed comment", got)
	}
	if cmd == nil {
		t.Fatal("review-comments mutation result returned nil command, want immediate feature detail refresh")
	}

	cycle := &server.CycleDTO{Type: "review-comments", Status: "running"}
	client.detail = server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
		FeatureSummary: server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Published", CurrentPhase: "publish", Cycle: cycle, CreatedAt: time.Now(), Repos: []string{"agentic-orchestrator"}},
		Cycle:          cycle,
		RepoStatus: []server.RepoStatusDTO{
			{Name: "agentic-orchestrator", Touched: true, Publishable: true, CycleType: "review-comments", CycleStatus: "running"},
		},
	}}
	msg = cmd()
	model, _ = started.Update(msg)
	started = model.(APIAppModel)

	view = stripANSI(started.View().Content)
	for _, want := range []string{"Started Review Comments", "active", "Addressing Review Comments"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func maxPlainLineWidth(s string) int {
	maxWidth := 0
	for _, line := range strings.Split(s, "\n") {
		if width := lipgloss.Width(line); width > maxWidth {
			maxWidth = width
		}
	}
	return maxWidth
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
	model, cmd = model.(APIAppModel).Update(msg)
	started := model.(APIAppModel)

	if got := strings.Join(client.startRefactorFeatureIDs, ","); got != "active" {
		t.Fatalf("StartRefactor feature IDs = %q, want active", got)
	}
	if got := client.startRefactorRequests; len(got) != 1 || got[0].Repo != "agentic-orchestrator" || got[0].Prompt != "extract transport boundary" || got[0].Pipeline != feature.PipelineLarge {
		t.Fatalf("StartRefactor requests = %+v, want agentic-orchestrator prompt with large pipeline", got)
	}
	if cmd == nil {
		t.Fatal("refactor mutation result returned nil command, want immediate feature detail refresh")
	}

	cycle := &server.CycleDTO{Type: "refactor", Status: "running"}
	client.detail = server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
		FeatureSummary: server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Published", CurrentPhase: "publish", Cycle: cycle, CreatedAt: time.Now(), Repos: []string{"agentic-orchestrator"}},
		Cycle:          cycle,
		RepoStatus: []server.RepoStatusDTO{
			{Name: "agentic-orchestrator", Touched: true, Publishable: true, CycleType: "refactor", CycleStatus: "running"},
		},
	}}
	msg = cmd()
	model, _ = started.Update(msg)
	refreshed := model.(APIAppModel)

	view = stripANSI(refreshed.View().Content)
	for _, want := range []string{"Started Refactor", "active", "Refactoring"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelRefactorPromptShiftEnterAndTerminalPaste(t *testing.T) {
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

	model, _ := app.Update(tea.KeyPressMsg{Code: 'F', Text: "F"})
	refactor := model.(APIAppModel)
	model, _ = refactor.Update(tea.KeyPressMsg{Text: "line1"})
	refactor = model.(APIAppModel)
	model, _ = refactor.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	refactor = model.(APIAppModel)
	model, _ = refactor.Update(tea.PasteMsg{Content: "line2\nline3"})
	refactor = model.(APIAppModel)

	if refactor.refactorPrompt == nil {
		t.Fatal("refactor prompt closed unexpectedly")
	}
	if got := refactor.refactorPrompt.input.Value(); got != "line1\nline2\nline3" {
		t.Fatalf("refactor prompt value = %q, want %q", got, "line1\nline2\nline3")
	}
}

func TestAPIAppModelRefactorPromptTracksPastedImagesAndFiles(t *testing.T) {
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

	model, _ := app.Update(tea.KeyPressMsg{Code: 'F', Text: "F"})
	refactor := model.(APIAppModel)
	model, _ = refactor.Update(ImagePastedMsg{Path: "/tmp/refactor-image.png"})
	refactor = model.(APIAppModel)
	model, _ = refactor.Update(FilesPastedMsg{Paths: []string{"/tmp/spec.pdf"}, Names: []string{"spec.pdf"}})
	refactor = model.(APIAppModel)
	model, _ = refactor.Update(TextPastedMsg{Text: " tighten the layout"})
	refactor = model.(APIAppModel)

	if refactor.refactorPrompt == nil {
		t.Fatal("refactor prompt closed unexpectedly")
	}
	if got := refactor.refactorPrompt.input.Value(); got != "[Image #1][spec.pdf] tighten the layout" {
		t.Fatalf("refactor prompt value = %q, want pasted placeholders and text", got)
	}

	model, cmd := refactor.Update(tea.KeyPressMsg{Code: 's', Text: "s", Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("Update(ctrl+s) returned command before pipeline selection")
	}
	refactor = model.(APIAppModel)
	model, cmd = refactor.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want refactor start mutation")
	}
	_ = cmd()
	if got := client.startRefactorRequests; len(got) != 1 ||
		!slices.Equal(got[0].Images, []string{"/tmp/refactor-image.png"}) ||
		!slices.Equal(got[0].Attachments, []string{"/tmp/spec.pdf"}) {
		t.Fatalf("StartRefactor requests = %+v, want image and attachment paths", got)
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

func TestAPIAppModelDestructiveActionsGuardActiveRepoCycles(t *testing.T) {
	t.Parallel()

	makeApp := func(t *testing.T) (APIAppModel, *fakeTUIAPIClient) {
		t.Helper()
		cycle := &server.CycleDTO{Type: "rebase", Status: "running"}
		summary := server.FeatureSummary{
			ID:           "active",
			Name:         "Published work",
			Slug:         "published-work",
			Status:       "Published",
			CurrentPhase: "publish",
			Cycle:        cycle,
			CreatedAt:    time.Now(),
			Repos:        []string{"api"},
		}
		client := &fakeTUIAPIClient{
			features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
			detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
				FeatureSummary: summary,
				Cycle:          cycle,
				RepoStatus: []server.RepoStatusDTO{
					{Name: "api", Publishable: true, CycleType: "rebase", CycleStatus: "running"},
				},
				Actions: []server.ActionDTO{
					{ID: "delete", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}},
					{ID: "mark-done", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}},
					{ID: "rewind", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}, RequiredInputs: []server.ActionInputDTO{
						{Name: "target_phase", Kind: "enum", Required: true, Options: []string{"implement"}},
					}},
				},
			}},
			deleteAccepted:   apiTestActionResponse{},
			markDoneAccepted: apiTestActionResponse{},
			rewindAccepted:   apiTestActionResponse{},
		}
		app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
		if err != nil {
			t.Fatalf("NewAPIAppModel() error = %v", err)
		}
		return app, client
	}

	tests := []struct {
		name       string
		key        tea.KeyPressMsg
		wantStatus string
		assert     func(t *testing.T, client *fakeTUIAPIClient)
	}{
		{
			name:       "delete",
			key:        tea.KeyPressMsg{Code: 'd', Text: "d"},
			wantStatus: "Stop active repo cycles before deleting",
			assert: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if len(client.deleteFeatureIDs) != 0 {
					t.Fatalf("DeleteFeature calls = %v, want none", client.deleteFeatureIDs)
				}
			},
		},
		{
			name:       "mark done",
			key:        tea.KeyPressMsg{Code: 'D', Text: "D"},
			wantStatus: "Cannot mark done while repo cycles are active",
			assert: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if len(client.markDoneFeatureIDs) != 0 {
					t.Fatalf("MarkDone calls = %v, want none", client.markDoneFeatureIDs)
				}
			},
		},
		{
			name:       "rewind",
			key:        tea.KeyPressMsg{Code: 'r', Text: "r", Mod: tea.ModCtrl},
			wantStatus: "Stop active repo cycles before rewinding",
			assert: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if len(client.rewindFeatureIDs) != 0 {
					t.Fatalf("RewindFeature calls = %v, want none", client.rewindFeatureIDs)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, client := makeApp(t)
			model, cmd := app.Update(tt.key)
			if cmd != nil {
				msg := cmd()
				model, _ = model.(APIAppModel).Update(msg)
			}
			updated := model.(APIAppModel)
			if updated.ShowingFeatureActionConfirm() {
				t.Fatalf("Update(%s) showed confirmation despite active repo cycle", tt.name)
			}
			view := stripANSI(updated.View().Content)
			if !strings.Contains(view, tt.wantStatus) {
				t.Fatalf("View() missing %q in:\n%s", tt.wantStatus, view)
			}
			tt.assert(t, client)
		})
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

func TestAPIAppModelListRefreshClearsStalePublishedCycleDetail(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	published := server.FeatureSummary{
		ID:           "active",
		Name:         "Active work",
		Slug:         "active-work",
		Status:       "Published",
		CurrentPhase: "publish",
		Repos:        []string{"agentic-orchestrator"},
		CreatedAt:    created,
		Progress:     server.FeatureProgress{CurrentIteration: 2},
	}
	staleDetail := server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
		FeatureSummary: published,
		Cycle:          &server.CycleDTO{Type: "refactor", Status: "running", Count: 1},
		RepoStatus: []server.RepoStatusDTO{{
			Name:        "agentic-orchestrator",
			CycleType:   "refactor",
			CycleStatus: "running",
			Touched:     true,
		}},
	}}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{published}},
		detail:   staleDetail,
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	app.ApplyRefreshSnapshot(server.RefreshSnapshot{
		Features: &server.FeatureListResponse{Features: []server.FeatureSummary{published}},
	})
	view := stripANSI(app.View().Content)
	if strings.Contains(view, "IN PROGRESS") || strings.Contains(view, "Refactoring") {
		t.Fatalf("list-only refresh kept stale refactor cycle in dashboard:\n%s", view)
	}
	if !strings.Contains(view, "PUBLISHED") || !strings.Contains(view, "active-work") {
		t.Fatalf("list-only refresh did not render feature as published:\n%s", view)
	}
}

func TestAPIAppModelPublishOpensReviewFlowBeforeRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "ready", Name: "Ready to publish", Slug: "ready-to-publish", Status: "CodeReady", CurrentPhase: "publish", Repos: []string{"api"}, CreatedAt: time.Now(), Checkpoints: server.CheckpointsDTO{ManualPublish: true}},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "ready", Name: "Ready to publish", Slug: "ready-to-publish", Status: "CodeReady", CurrentPhase: "publish", Repos: []string{"api"}, Checkpoints: server.CheckpointsDTO{ManualPublish: true}},
			RepoStatus: []server.RepoStatusDTO{
				{Name: "api", Publishable: true, Touched: true},
			},
			Actions: []server.ActionDTO{
				{ID: "publish", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}},
			},
		}},
		runtime: server.RuntimeConfigResponse{
			Repos: []server.ConfigRepoDTO{{Name: "api", Path: "/tmp/api"}},
		},
		publishDescriptionTitle: "AI: Ready to publish",
		publishDescriptionBody:  "AI generated commit summary with implementation details.",
		publishAccepted:         apiTestActionResponse{},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if cmd != nil {
		t.Fatal("Update(p) returned command before publish review")
	}
	reviewing := model.(APIAppModel)
	view := stripANSI(reviewing.View().Content)
	for _, want := range []string{"Publish Feature", "Diff Review"} {
		if !strings.Contains(view, want) {
			t.Fatalf("publish review missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Confirm Publish") {
		t.Fatalf("publish opened generic confirmation instead of review flow:\n%s", view)
	}
	if len(client.publishFeatureIDs) != 0 {
		t.Fatalf("PublishFeature calls = %v before publish review confirmation, want none", client.publishFeatureIDs)
	}

	model, cmd = reviewing.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter on diff) returned command before commit review")
	}
	commits := model.(APIAppModel)
	if view := stripANSI(commits.View().Content); !strings.Contains(view, "Commit Log") {
		t.Fatalf("publish review did not advance to commit log:\n%s", view)
	}

	model, cmd = commits.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter on commits) returned nil command, want PR description generation")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	describing := model.(APIAppModel)
	if view := stripANSI(describing.View().Content); !strings.Contains(view, "PR Description") {
		t.Fatalf("publish review did not advance to PR description:\n%s", view)
	} else if !strings.Contains(view, client.publishDescriptionBody) {
		t.Fatalf("publish PR description missing generated AI body %q in:\n%s", client.publishDescriptionBody, view)
	}
	if got := client.publishDescriptionFeatureIDs; strings.Join(got, ",") != "ready" {
		t.Fatalf("GeneratePublishDescription feature IDs = %v, want ready", got)
	}
	if got := client.publishDescriptionRequests; len(got) != 1 || got[0].FeatureName != "Ready to publish" || strings.TrimSpace(got[0].Model) == "" {
		t.Fatalf("GeneratePublishDescription requests = %+v, want publish context with model", got)
	}

	model, cmd = describing.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter on PR description) returned command before final confirmation")
	}
	confirming := model.(APIAppModel)
	if view := stripANSI(confirming.View().Content); !strings.Contains(view, "Ready to push and create PR?") {
		t.Fatalf("publish review did not show final confirmation:\n%s", view)
	}

	model, cmd = confirming.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter on final confirmation) returned nil command, want publish mutation command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	published := model.(APIAppModel)

	if got := strings.Join(client.publishFeatureIDs, ","); got != "ready" {
		t.Fatalf("PublishFeature calls = %q, want ready", got)
	}
	if got := client.publishRequests; len(got) != 1 || len(got[0].Repos) != 0 || got[0].Title != client.publishDescriptionTitle || got[0].Body != client.publishDescriptionBody {
		t.Fatalf("PublishFeature requests = %+v, want whole-feature request with generated title/body", got)
	}
	if view := stripANSI(published.View().Content); !strings.Contains(view, "Completed Publish") {
		t.Fatalf("API app View() missing completed publish status:\n%s", view)
	}
}

func TestAPIAppModelPublishCommitsSingleRepoBeforeCommitLogPreview(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	testutil.CreateBranch(t, repo, "feature/ready-to-publish")
	if err := os.WriteFile(filepath.Join(repo, "implementation.txt"), []byte("ship it\n"), 0o644); err != nil {
		t.Fatalf("write implementation change: %v", err)
	}
	stateDir := filepath.Join(t.TempDir(), "features")
	store := feature.NewStore(stateDir)
	if err := store.Save(&feature.Feature{
		ID:            "ready",
		Name:          "Ready to publish",
		Slug:          "ready-to-publish",
		Status:        feature.StatusCodeReady,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now(),
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{{
			Name:         "api",
			Path:         repo,
			WorktreePath: repo,
			Branch:       "feature/ready-to-publish",
			BaseBranch:   "main",
		}},
	}); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "ready", Name: "Ready to publish", Slug: "ready-to-publish", Status: "CodeReady", CurrentPhase: "publish", Repos: []string{"api"}, CreatedAt: time.Now(), Checkpoints: server.CheckpointsDTO{ManualPublish: true}},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "ready", Name: "Ready to publish", Slug: "ready-to-publish", Status: "CodeReady", CurrentPhase: "publish", Repos: []string{"api"}, Checkpoints: server.CheckpointsDTO{ManualPublish: true}},
			RepoStatus: []server.RepoStatusDTO{
				{Name: "api", Publishable: true, Touched: true},
			},
			Actions: []server.ActionDTO{
				{ID: "publish", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}},
			},
		}},
		runtime: server.RuntimeConfigResponse{
			Runtime: server.RuntimeIdentity{StateDir: stateDir},
			Repos:   []server.ConfigRepoDTO{{Name: "api", Path: repo}},
		},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if cmd != nil {
		t.Fatal("Update(p) returned command before publish review")
	}
	reviewing := model.(APIAppModel)

	model, cmd = reviewing.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter on diff) returned command before commit review")
	}
	commits := model.(APIAppModel)
	view := stripANSI(commits.View().Content)
	if !strings.Contains(view, "Commit Log") {
		t.Fatalf("publish review did not advance to commit log:\n%s", view)
	}
	if !strings.Contains(view, "Ready to publish") {
		t.Fatalf("publish commit log page is empty or missing the pre-publish commit:\n%s", view)
	}
}

func TestAPIAppModelPublishRepoSelectorSendsSelectedRepos(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: "ready", Name: "Ready to publish", Slug: "ready-to-publish", Status: "CodeReady", CurrentPhase: "publish", Repos: []string{"api", "web"}, CreatedAt: time.Now(), Checkpoints: server.CheckpointsDTO{ManualPublish: true}},
		}},
		detail: server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
			FeatureSummary: server.FeatureSummary{ID: "ready", Name: "Ready to publish", Slug: "ready-to-publish", Status: "CodeReady", CurrentPhase: "publish", Repos: []string{"api", "web"}, Checkpoints: server.CheckpointsDTO{ManualPublish: true}},
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
	for _, want := range []string{"Publish Feature", "Select Repository", "api", "web"} {
		if !strings.Contains(view, want) {
			t.Fatalf("repo selector missing %q in:\n%s", want, view)
		}
	}

	model, _ = selecting.Update(tea.KeyPressMsg{Code: tea.KeyDown, Text: "j"})
	selecting = model.(APIAppModel)
	model, cmd = selecting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter) returned command before diff review")
	}
	reviewing := model.(APIAppModel)
	if view := stripANSI(reviewing.View().Content); !strings.Contains(view, "Diff Review") {
		t.Fatalf("publish selector did not advance to diff review:\n%s", view)
	}

	model, cmd = reviewing.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter on diff) returned command before commit review")
	}
	commits := model.(APIAppModel)
	model, cmd = commits.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter on commits) returned nil command, want PR description generation")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	describing := model.(APIAppModel)
	model, cmd = describing.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter on PR description) returned command before final confirmation")
	}
	confirming := model.(APIAppModel)
	model, cmd = confirming.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter on final confirmation) returned nil command, want publish mutation")
	}
	model, _ = model.(APIAppModel).Update(cmd())
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

func TestAPIAppModelRebaseDoesNotOpenRepoSelector(t *testing.T) {
	t.Parallel()

	client := apiRepoSelectorClient("rebase", apiTestActionResponse{})
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	if cmd != nil {
		t.Fatal("Update(b) returned command before confirmation")
	}
	confirming := model.(APIAppModel)
	view := stripANSI(confirming.View().Content)
	if strings.Contains(view, "Select repo") {
		t.Fatalf("rebase opened repo selector:\n%s", view)
	}
	if !strings.Contains(view, "Confirm Rebase") {
		t.Fatalf("rebase confirmation not shown:\n%s", view)
	}

	model, cmd = confirming.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("confirmation returned nil command")
	}
	msg := cmd()
	_, _ = model.(APIAppModel).Update(msg)
	if got := client.startRebaseRequests; len(got) != 1 || got[0].Repo != "" || got[0].RebaseTarget != "" || len(got[0].ConflictFiles) != 0 {
		t.Fatalf("StartRebase requests = %+v, want no repo or conflict inputs", got)
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
	if view := stripANSI(model.(APIAppModel).View().Content); !strings.Contains(view, "Choose Implement roadmap phase") {
		t.Fatalf("rewind selector should advertise roadmap phase choice:\n%s", view)
	}
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

func TestAPIAppModelRewindSingleImplementTargetOpensRoadmapPhasePicker(t *testing.T) {
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
						{Name: "target_phase", Kind: "enum", Required: true, Options: []string{"implement"}},
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
		t.Fatal("Update(ctrl+r) returned command before roadmap phase picker")
	}
	picking := model.(APIAppModel)
	if view := stripANSI(picking.View().Content); !strings.Contains(view, "Choose Roadmap Phase") || !strings.Contains(view, "Phase 2/3") || !strings.Contains(view, "current phase") {
		t.Fatalf("single implement rewind target should open roadmap phase picker:\n%s", view)
	}

	model, cmd = picking.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("roadmap phase selection returned command before confirmation")
	}
	confirming := model.(APIAppModel)
	if view := stripANSI(confirming.View().Content); !strings.Contains(view, "Rewind Implement to roadmap Phase 2") {
		t.Fatalf("roadmap rewind confirmation missing selected phase:\n%s", view)
	}
}

func TestAPIAppModelRewindMutationRefreshesFeatureAndClearsStaleRunContent(t *testing.T) {
	t.Parallel()

	run1Detail := server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
		FeatureSummary: server.FeatureSummary{
			ID:           "active",
			Name:         "Active work",
			Slug:         "active-work",
			Status:       "Implementing",
			CurrentPhase: "implement",
			ActiveRun:    1,
			RunCount:     1,
			CreatedAt:    time.Now(),
		},
		ActiveRun: &server.RunSummaryDTO{RunNumber: 1, CurrentPhase: "implement", ArtifactCount: 1},
		Actions: []server.ActionDTO{
			{ID: "rewind", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}},
		},
	}}
	run2Detail := server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{
		FeatureSummary: server.FeatureSummary{
			ID:           "active",
			Name:         "Active work",
			Slug:         "active-work",
			Status:       "PlanNeedsReview",
			CurrentPhase: "design",
			ActiveRun:    2,
			RunCount:     2,
			CreatedAt:    run1Detail.Feature.CreatedAt,
		},
		ActiveRun: &server.RunSummaryDTO{
			RunNumber:          2,
			CurrentPhase:       "design",
			PendingReviewPhase: "plan",
			IsRewind:           true,
			ArtifactCount:      1,
		},
		Actions: []server.ActionDTO{
			{ID: "rewind", Enabled: true, Scope: server.ActionScopeDTO{Type: "feature"}},
		},
	}}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{run1Detail.Feature.FeatureSummary}},
		detail:   run1Detail,
		artifactListByRun: map[int]server.ArtifactListResponse{
			1: {Artifacts: []server.ArtifactDTO{
				{ID: "old-plan", RunNumber: 1, Phase: "plan", Size: 18, ContentAvailable: true},
			}},
			2: {Artifacts: []server.ArtifactDTO{
				{ID: "description-review", RunNumber: 2, Phase: "description", Size: 24, ContentAvailable: true},
			}},
		},
		artifactContentByID: map[string]server.TextContentResponse{
			"old-plan":           {ID: "old-plan", Text: "old run plan artifact", Size: 18},
			"description-review": {ID: "description-review", Text: "new rewind review artifact", Size: 24},
		},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	if got := app.snapshot.Content; got == nil || got.RunNumber != 1 || got.Artifact == nil || got.Artifact.ID != "old-plan" {
		t.Fatalf("initial content = %+v, want run 1 old-plan", got)
	}
	staleReview := NewArtifactReviewModel("/tmp/old-plan.md", "active", "plan", feature.PhasePlan, app.width, app.height, nil, "", nil)
	staleReview.detached = true
	app.artifactReview = &staleReview

	client.detail = run2Detail
	model, cmd := app.Update(apiMutationResultMsg{kind: "feature.rewind", featureID: "active"})
	if cmd == nil {
		t.Fatal("rewind mutation result returned nil command, want immediate feature detail refresh")
	}
	afterRewind := model.(APIAppModel)
	if got, ok := afterRewind.contents["active"]; ok {
		t.Fatalf("rewind mutation retained stale content = %+v, want content cleared until run 2 loads", got)
	}
	if afterRewind.artifactReview != nil {
		t.Fatalf("rewind mutation retained stale artifact review for %q", afterRewind.artifactReview.ArtifactPath())
	}

	msg := cmd()
	model, _ = afterRewind.Update(msg)
	refreshed := model.(APIAppModel)
	if got := refreshed.snapshot.Content; got == nil || got.RunNumber != 2 || got.Artifact == nil || got.Artifact.ID != "description-review" {
		t.Fatalf("refreshed content = %+v, want run 2 description-review", got)
	}
	if got := refreshed.snapshot.Detail; got == nil || got.ID != "active" {
		t.Fatalf("refreshed detail = %+v, want active feature detail", got)
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

func TestAPIAppModelDashboardNavigationCanToggleCompletedSection(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	active := server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: created}
	published := server.FeatureSummary{ID: "published", Name: "Published work", Slug: "published-work", Status: "Published", CurrentPhase: "publish", CreatedAt: created.Add(-time.Minute)}
	done := server.FeatureSummary{ID: "done", Name: "Done work", Slug: "done-work", Status: "Done", CurrentPhase: "publish", CreatedAt: created.Add(-2 * time.Minute)}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{done, published, active}},
		detail:   server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{FeatureSummary: active}},
		livePreview: server.LivePreviewResponse{
			Feature: active,
		},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	for i := 0; i < 3; i++ {
		model, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		app = model.(APIAppModel)
	}
	if got := app.SelectedFeatureID(); got != "" {
		t.Fatalf("SelectedFeatureID() = %q, want no feature while cursor is on completed section", got)
	}
	if got := app.selectedSection; got != "completed" {
		t.Fatalf("selectedSection = %q, want completed", got)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter on completed section) returned nil command, want runtime config persistence")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	app = model.(APIAppModel)
	if !slices.Contains(app.runtimeConfig.UI.CollapsedSections, "completed") {
		t.Fatalf("CollapsedSections = %v, want completed", app.runtimeConfig.UI.CollapsedSections)
	}
	view := stripANSI(app.View().Content)
	if strings.Contains(view, "done-work") {
		t.Fatalf("collapsed completed section still rendered done feature:\n%s", view)
	}

	model, cmd = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if cmd != nil {
		t.Fatal("Update(down after collapsed completed section) returned command, want no-op at list bottom")
	}
	app = model.(APIAppModel)
	if got := app.selectedSection; got != "completed" {
		t.Fatalf("selectedSection after down = %q, want completed", got)
	}
	if got := app.SelectedFeatureID(); got != "" {
		t.Fatalf("SelectedFeatureID() after down = %q, want still on completed section", got)
	}
}

func TestAPIAppModelDashboardSectionCollapsePersistsThroughREST(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	active := server.FeatureSummary{ID: "active", Name: "Active work", Slug: "active-work", Status: "Implementing", CurrentPhase: "implement", CreatedAt: created}
	published := server.FeatureSummary{ID: "published", Name: "Published work", Slug: "published-work", Status: "Published", CurrentPhase: "publish", CreatedAt: created.Add(-time.Minute)}
	done := server.FeatureSummary{ID: "done", Name: "Done work", Slug: "done-work", Status: "Done", CurrentPhase: "publish", CreatedAt: created.Add(-2 * time.Minute)}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{done, published, active}},
		detail:   server.FeatureDetailResponse{Feature: server.FeatureDetailDTO{FeatureSummary: active}},
		livePreview: server.LivePreviewResponse{
			Feature: active,
		},
	}
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	for i := 0; i < 3; i++ {
		model, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		app = model.(APIAppModel)
	}
	model, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter on completed section) returned nil command, want runtime config persistence")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	app = model.(APIAppModel)

	if len(client.updateRuntimeConfigRequests) != 1 {
		t.Fatalf("UpdateRuntimeConfig calls = %d, want 1", len(client.updateRuntimeConfigRequests))
	}
	req := client.updateRuntimeConfigRequests[0]
	if req.UI == nil {
		t.Fatalf("UpdateRuntimeConfig request UI = nil, want collapsed section persisted")
	}
	if !slices.Contains(req.UI.CollapsedSections, "completed") {
		t.Fatalf("persisted CollapsedSections = %v, want completed", req.UI.CollapsedSections)
	}
	if !slices.Contains(app.runtimeConfig.UI.CollapsedSections, "completed") {
		t.Fatalf("runtime CollapsedSections after persistence = %v, want completed", app.runtimeConfig.UI.CollapsedSections)
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
	view := stripANSI(model.(APIAppModel).View().Content)
	for _, want := range []string{
		"Session Exit",
		"The server belongs to this window.",
		"[ y ] Stop server + quit",
		"clean shutdown",
		"[ n ] Keep server running",
		"detach this TUI only",
		"esc cancel",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("owned quit prompt missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Stop the server started for this TUI session?") {
		t.Fatalf("owned quit prompt still uses old dense copy:\n%s", view)
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

func TestAPIAppModelOwnedServerShutdownSuppressesRefreshNoise(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{}
	owned, err := NewAPIAppModel(ctx, client, APIAppOptions{OwnedServer: true})
	if err != nil {
		t.Fatalf("NewAPIAppModel(owned) error = %v", err)
	}
	cancelledEvents := false
	owned.cancelEvents = func() {
		cancelledEvents = true
	}

	model, _ := owned.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("owned Update(y) returned nil command, want shutdown command")
	}
	shutdownCmd := cmd
	shuttingDown := model.(APIAppModel)
	if shuttingDown.ShowingOwnedServerQuitPrompt() {
		t.Fatal("owned Update(y) left quit prompt visible")
	}
	if !cancelledEvents {
		t.Fatal("owned Update(y) did not cancel event subscription")
	}

	model, cmd = shuttingDown.Update(apiRefreshSignalMsg{signal: server.RefreshSignal{
		Event: server.SSEEventDTO{Kind: "shutdown.updated"},
		Resource: server.ResourceDTO{
			Type: "runtime",
		},
		SnapshotRequired: true,
	}})
	if cmd != nil {
		t.Fatal("shutdown refresh signal returned command while shutdown is pending")
	}
	model, _ = model.(APIAppModel).Update(apiRefreshSnapshotMsg{err: errors.New("api GET /api/v1/health: connection refused")})
	model, _ = model.(APIAppModel).Update(apiEventErrorMsg{err: errors.New("connect event stream: connection refused")})
	updated := model.(APIAppModel)
	if strings.Contains(updated.statusMessage, "health") ||
		strings.Contains(updated.statusMessage, "Refresh failed") ||
		strings.Contains(updated.statusMessage, "Event stream failed") {
		t.Fatalf("statusMessage = %q, want shutdown noise suppressed", updated.statusMessage)
	}

	model, cmd = updated.Update(shutdownCmd())
	if cmd == nil {
		t.Fatal("owned shutdown completion did not return quit command")
	}
}

func apiRepoSelectorClient(actionID string, accepted apiTestActionResponse) *fakeTUIAPIClient {
	action := server.ActionDTO{
		ID:      actionID,
		Enabled: true,
		Scope:   server.ActionScopeDTO{Type: "feature", RepoSelection: "optional"},
	}
	switch actionID {
	case "rebase":
		action.Scope = server.ActionScopeDTO{Type: "feature"}
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
	allowEmptyWorkspaceRoots      bool
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
	publishDescriptionFeatureIDs  []string
	publishDescriptionRequests    []server.PublishDescriptionRequest
	publishDescriptionTitle       string
	publishDescriptionBody        string
	publishDescriptionErr         error
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
	startChatAccepted             apiTestActionResponse
	startChatErr                  error
	startChatRequests             []server.ChatStartRequest
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
	logContentErrByID             map[string]error
	logContentErr                 error
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
	runtime := f.runtime
	if !f.allowEmptyWorkspaceRoots && len(runtime.WorkspaceRoots) == 0 {
		runtime.WorkspaceRoots = []string{"/tmp/agentico-test-workspace"}
	}
	return runtime, nil
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
	if err, ok := f.logContentErrByID[logID]; ok {
		return server.TextContentResponse{}, err
	}
	if content, ok := f.logContentByID[logID]; ok {
		return content, nil
	}
	if f.logContentErr != nil {
		return server.TextContentResponse{}, f.logContentErr
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

func (f *fakeTUIAPIClient) GeneratePublishDescription(_ context.Context, featureID string, req server.PublishDescriptionRequest) (server.PublishDescriptionResponse, error) {
	f.calls = append(f.calls, "GeneratePublishDescription")
	f.publishDescriptionFeatureIDs = append(f.publishDescriptionFeatureIDs, featureID)
	f.publishDescriptionRequests = append(f.publishDescriptionRequests, req)
	return server.PublishDescriptionResponse{
		FeatureID: featureID,
		Title:     f.publishDescriptionTitle,
		Body:      f.publishDescriptionBody,
		Result:    "generated",
	}, f.publishDescriptionErr
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
	if req.Defaults.Models != (config.ModelConfig{}) {
		f.runtime.Defaults = req.Defaults.Models
		f.runtime.FeatureDefaults.Models = req.Defaults.Models
	}
	if req.Defaults.Inquireness != "" {
		f.runtime.FeatureDefaults.Inquireness = req.Defaults.Inquireness
	}
	if req.Defaults.Pipeline != "" {
		f.runtime.FeatureDefaults.Pipeline = req.Defaults.Pipeline
	}
	if req.Defaults.Checkpoints != (config.Checkpoints{}) {
		f.runtime.FeatureDefaults.Checkpoints = req.Defaults.Checkpoints
	}
	if req.WorkspaceRoots != nil {
		f.runtime.WorkspaceRoots = append([]string(nil), (*req.WorkspaceRoots)...)
		f.runtime.Repos = testRuntimeConfigRepos(f.runtime.Repos, f.runtime.WorkspaceRoots)
	}
	if req.UI != nil {
		f.runtime.UI = *req.UI
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

func (f *fakeTUIAPIClient) StartChat(_ context.Context, req server.ChatStartRequest) (server.ChatStartResponse, error) {
	f.calls = append(f.calls, "StartChat")
	f.startChatRequests = append(f.startChatRequests, req)
	sessionID := f.startChatAccepted.SessionID
	if sessionID == "" {
		sessionID = chatSessionID
	}
	return server.ChatStartResponse{SessionID: sessionID, Result: f.startChatAccepted.result("started")}, f.startChatErr
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

func TestAPIAppModelFullscreenChatSkipsDashboard(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           "active",
		Name:         "Translate README in Sicilian",
		Slug:         "translate-readme-in-sicilian",
		Status:       "Planning",
		CurrentPhase: "plan",
		Repos:        []string{"agentic-orchestrator"},
		CreatedAt:    time.Now(),
	}
	app := APIAppModel{
		width:          100,
		height:         30,
		focusPanel:     0,
		rightPanelMode: dashboardRightPanelOverview,
		featureList:    server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			summary.ID: {Feature: server.FeatureDetailDTO{FeatureSummary: summary}},
		},
		runtimeConfig: server.RuntimeConfigResponse{
			Runtime: server.RuntimeIdentity{StateDir: "/tmp/agentico/features"},
		},
	}
	app.rebuildPresentation(summary.ID)

	// Docked (not fullscreen): dashboard chrome is present alongside the chat panel.
	app.chatReady = true
	app.chat = NewAPIChatModel(app.width, 10, nil)
	app.chatOpen = true
	docked := stripANSI(app.View().Content)
	if !strings.Contains(docked, "IN PROGRESS") {
		t.Fatalf("expected dashboard section header present while docked, got:\n%s", docked)
	}

	// Fullscreen: dashboard chrome is gone, only the chat panel renders.
	app.chat.fullscreen = true
	fullscreen := stripANSI(app.View().Content)
	if strings.Contains(fullscreen, "IN PROGRESS") {
		t.Errorf("expected dashboard section header absent while chat is fullscreen, got:\n%s", fullscreen)
	}
	if !strings.Contains(fullscreen, "Ask me Anything") {
		t.Errorf("expected the chat panel itself still to render while fullscreen, got:\n%s", fullscreen)
	}
}

// TestAPIAppModelChatResizesAsConversationGrowsWithoutWindowResize verifies
// that the docked chat panel's rendered height stays in sync with
// chatPanelHeight as turns are added, even when no tea.WindowSizeMsg ever
// arrives. Before this fix, ChatModel.height/width were only recomputed on
// initial open, a real window resize, or the fullscreen toggle — so as a
// conversation grew during a live session, the panel kept rendering at
// whatever height it had when last opened while the dashboard's own layout
// math (which calls chatPanelHeight fresh every render) reserved a
// different amount of space, producing a visible mismatch (overflow or a
// large gap of empty space) until the user closed and reopened the chat.
func TestAPIAppModelChatResizesAsConversationGrowsWithoutWindowResize(t *testing.T) {
	t.Parallel()

	app := APIAppModel{width: 100, height: 100}
	app.chatReady = true
	app.chat = NewAPIChatModel(app.width, 8, nil) // matches the empty-state ceiling at height=100
	app.chatOpen = true

	if got := app.chat.height; got != 8 {
		t.Fatalf("test setup invalid: chat.height = %d, want 8 (empty-state ceiling)", got)
	}

	// Simulate a turn having been added without any resize having happened
	// yet (e.g. a streamed response arriving via chatMsgsMsg/refresh-snapshot
	// handling) — chatPanelHeight should now report the non-empty range.
	app.chat.turns = []chatTurn{{Role: chatTurnUser, Text: "hello"}}
	wantHeight := app.chat.chatPanelHeight(app.height)
	if wantHeight != 18 {
		t.Fatalf("test setup invalid: chatPanelHeight = %d, want 18 (non-empty ceiling at height=100)", wantHeight)
	}

	updatedModel, _ := app.updateAPIChat(chatRecoveryTickMsg{})
	updated, ok := updatedModel.(APIAppModel)
	if !ok {
		t.Fatalf("updateAPIChat returned %T, want APIAppModel", updatedModel)
	}

	if updated.chat.height != wantHeight {
		t.Fatalf("chat.height = %d after updateAPIChat, want %d (chatPanelHeight resynced to reflect the new turn without a window resize)", updated.chat.height, wantHeight)
	}
}
