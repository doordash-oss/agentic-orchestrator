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
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// Shared fixture feature-ID literals reused across this file's test tables.
// These double as generic status/kind literals in places (e.g. a session
// Status of "done"); reusing the fixture-ID constant keeps a single source
// for each short word instead of scattering raw literals.
const (
	testFeatureIDDone      = "done"
	testFeatureIDActive    = "active"
	testFeatureIDPublished = "published"
	testFeatureIDReady     = "ready"
	testFeatureIDFeat1     = "feat-1"
	testFeatureIDSetupFail = "setup-fail"
	testFeatureIDNew       = "new"
	testFeatureIDStopped   = "stopped"
	testFeatureIDQueued    = "queued"
	testFeatureIDPaused    = "paused"
)

// Shared fixture slug literals.
const (
	testFeatureSlugTranslateReadme   = "translate-readme"
	testFeatureSlugTranslateSicilian = "translate-readme-in-sicilian"
	testFeatureSlugActiveWork        = "active-work"
	testFeatureSlugFeatureOne        = "feature-one"
)

// testFeatureNameTranslateSicilian is a fixture feature name reused across
// this file's Sicilian-translate test tables.
const testFeatureNameTranslateSicilian = "Translate README in Sicilian"

// testRequestIDReq1 is a fixture AskUser/permission request ID.
const testRequestIDReq1 = "req-1"

// testRepoNameA and testRepoNameAPI are fixture repo-name literals reused
// across this file's repo-status and publish test tables.
const (
	testRepoNameA   = "repo-a"
	testRepoNameAPI = "api"
)

// Shared provider/model fixture literals.
const (
	testProviderCodex  = "codex"
	testProviderClaude = "claude"
	testModelGPT54     = "gpt-5.4"
)

// Shared status/DTO-field literals. These are plain strings distinct from
// the typed presentationStatus tokens in attach.go (statusPending etc.),
// which can't be assigned directly to the string-typed server DTO fields
// used here.
const (
	testStatusPending   = "pending"
	testStatusFailed    = "failed"
	testStatusCompleted = "completed"
)

// Shared pipeline/cycle/phase-kind literals.
const (
	testPipelineRoadmap      = "roadmap"
	testPipelineRefactor     = "refactor"
	testPipelineSizeMedium   = "medium"
	testCycleTypeRebase      = "rebase"
	testPhaseNameImplement   = "implement"
	testPhaseNameFinalReview = "Final Review"
	testPhaseKeyResearch     = "research"
)

// testActionScopeFeature is the fixture ActionScopeDTO.Type value "feature".
const testActionScopeFeature = "feature"

// testActionIDFeaturePublish is the fixture "feature.publish" action ID.
const testActionIDFeaturePublish = "feature.publish"

// Shared recovery-action-name literals.
const (
	testActionSkip = "skip"
	testActionKill = "kill"
)

// Shared session/resource fixture literals.
const (
	testSessionKindAgent        = "agent"
	testSessionIDLive           = "sess-live"
	testSessionIDImpl1          = "impl-1"
	testEventKindSessionUpdated = "session.updated"
	testResourceIDSession       = "session"
	testResourceTypeLog         = "log"
)

// Shared transcript-message-type/role literals.
const (
	testMessageTypeText    = "text"
	testMessageTypeToolUse = "tool_use"
	testMessageRoleSystem  = "system"
	testMessageRoleUser    = "user"
)

// Shared artifact-ID literals.
const (
	testArtifactIDPhase1Plan = "phase-1-plan"
	testArtifactIDPhase1Impl = "phase-1-impl"
	testArtifactIDPlan       = "plan"
	testArtifactIDDesign     = "design"
)

// Shared artifact/log server-tail-text fixtures.
const (
	testDesignTailFromServer = "design tail from server"
	testPhaseLogFromServer   = "phase log from server"
	testContentArtifactText  = "content artifact"
)

// Shared repo-freshness label literals.
const (
	testFreshnessLocalChanges = "local changes"
	testFreshnessInSync       = "in sync"
)

// Shared validator-name / validation-table-column literals.
const (
	testValidatorNameScope   = "Scope"
	testValidatorNameTesting = "Testing"
	testColumnLabelStatus    = "Status"
)

// Shared attach/dashboard UI-label literals.
const (
	testLabelLivePreview = "Live Preview"
	testLabelRunContent  = "Run Content"
)

// testShellCommandGoTest is a fixture Bash-permission command summary.
const testShellCommandGoTest = "go test ./internal/tui"

// Shared cross-file fixture literals restored here because other test files
// in this package (api_chat_adapter_test.go, attach_askuser_test.go,
// attach_test.go, chat_events_test.go, live_preview_test.go, wizard_test.go)
// reference them from this file.
const (
	testHintEnterToSelect            = "Enter to select"
	testAskRequestID                 = "ask-1"
	testAgentToolLabel               = "Agent: Explore KB completion handler"
	testAssistantTextFirstParagraph  = "First paragraph."
	testAssistantTextSecondParagraph = "Second paragraph."
	testReadyToPatchText             = "Ready to patch live preview"
	testContextPct42                 = "42%"
	testQuestionNeedInput            = "Need input?"
	testAgentDelegationPrompt        = "Read the provider docs and report every attach-view metadata gap."
	testPastedFilePath               = "/tmp/spec.pdf"
	testOptionLabelAlpha             = "Alpha"
	testOptionLabelBeta              = "Beta"
	testOptionLabelGamma             = "Gamma"
)

// testUsingBashActivity is the rendered "Using Bash..." activity line,
// reused across this file and other test files in this package.
var testUsingBashActivity = "Using " + toolNameBash + "..."

// New fixture literals introduced by the second goconst cleanup pass.
const (
	testFeatureStatusDone                = "Done"
	testFeatureNameClientCutover         = "Client cutover"
	testFeatureStatusPublished           = "Published"
	testActivityImplement                = "Implement"
	testFeatureStatusCodeReady           = "CodeReady"
	testFeatureNameTranslateReadme       = "Translate README"
	testFeatureStatusPlanning            = "Planning"
	testActivityResearch                 = "Research"
	testFeatureStatusFailed              = "Failed"
	testFeatureStatusCreated             = "Created"
	testFeatureNameActiveWork            = "Active work"
	testSessionStatusRunning             = "Running"
	testSessionIDTestingValidator        = "testing-validator"
	testFeatureNameFeatureOne            = "Feature one"
	testPromptBashPrefix                 = "$ Bash"
	testFeatureStatusInterrupted         = "Interrupted"
	testFeatureNameQueuedWork            = "Queued work"
	testFeatureNamePausedWork            = "Paused work"
	testFeatureNameFailedWork            = "Failed work"
	testSessionStatusWaitingHelp         = "WaitingHelp"
	testFeatureIDFeatCreated             = "feat-created"
	testModelCodexGPT54                  = "codex:gpt-5.4"
	testModelCodexGPT55                  = "codex:gpt-5.5"
	testPipelineSizeLarge                = "large"
	testInquirenessTargeted              = "targeted"
	testSectionLabelGates                = "Gates"
	testLabelModelsForCodex              = "Models for codex"
	testModelCodexGPT54Mini              = "codex:gpt-5.4-mini"
	testModelCodexGPT55Mini              = "codex:gpt-5.5-mini"
	testSectionLabelUtilities            = "Utilities"
	testFeatureNameBlockedWork           = "Blocked work"
	testQuestionWhichDatabase            = "Which database?"
	testDBOptionPostgres                 = "PostgreSQL"
	testActionInputNamePrompt            = "prompt"
	testInputKeyQuestions                = "questions"
	testInputKeyQuestion                 = "question"
	testDBOptionDynamoDB                 = "DynamoDB"
	testFeatureStatusPlanNeedsReview     = "PlanNeedsReview"
	testUtilityModelID                   = "test-utility"
	testFeatureStatusDesignNeedsReview   = "DesignNeedsReview"
	testArtifactPhaseDescription         = "description"
	testFeatureStatusResearchNeedsReview = "ResearchNeedsReview"
	testPipelineSizeMoonshot             = "moonshot"
	testPhaseKeyInquire                  = "inquire"
	testParamKindString                  = "string"
	testActivityRefactoring              = "Refactoring"
	testPasteTextMultiline               = "line1\nline2\nline3"
	testFeatureNamePublishedWork         = "Published work"
	testFeatureSlugPublishedWork         = "published-work"
	testFeatureIDNext                    = "next"
	testFeatureNameReadyPublish          = "Ready to publish"
	testGitBranchMain                    = "main"
	testActionInputNameUpgradePipeline   = "upgrade_pipeline"
	testArtifactIDOldPlan                = "old-plan"
	testRuntimeStateDirFeatures          = "/tmp/agentico/features"
)

// New fixture literals introduced by the third goconst cleanup pass.
const (
	testFeatureIDBlocked            = "blocked"
	testFeatureStatusImplementing   = "Implementing"
	testFeatureSlugPublishedFeature = "published-feature"
	testPermissionRequestIDPerm1    = "perm-1"
	testSectionLabelInProgress      = "IN PROGRESS"
	testSessionKindValidator        = "validator"
	testFeatureSlugQueuedWork       = "queued-work"
	testFeatureSlugPausedWork       = "paused-work"
	testFeatureSlugFailedWork       = "failed-work"
	testSectionLabelModels          = "Models"
	testSectionLabelPhases          = "Phases"
	testParamKindEnum               = "enum"
	testFeatureSlugReadyToPublish   = "ready-to-publish"
	testSessionIDOne                = "sess-1"
	testStatusNeedUserInput         = "NeedUserInput"
	testFeatureSlugClientCutover    = "client-cutover"
)

func apiTestFeatureDetail(summary server.FeatureSummary) server.FeatureDetailDTO {
	return server.FeatureDetailDTO{
		ID:           summary.ID,
		Name:         summary.Name,
		Slug:         summary.Slug,
		Status:       summary.Status,
		CurrentPhase: summary.CurrentPhase,
		Cycle:        summary.Cycle,
		ActiveRun:    summary.ActiveRun,
		RunCount:     summary.RunCount,
		Resumed:      summary.Resumed,
		ResumeCount:  summary.ResumeCount,
		Repos:        append([]string(nil), summary.Repos...),
		CreatedAt:    summary.CreatedAt,
		Checkpoints:  summary.Checkpoints,
		Progress:     summary.Progress,
		Warnings:     append([]server.WarningDTO(nil), summary.Warnings...),
	}
}

func apiTestFeatureDetailWith(summary server.FeatureSummary, overlay server.FeatureDetailDTO) server.FeatureDetailDTO {
	detail := apiTestFeatureDetail(summary)
	if overlay.Description != "" {
		detail.Description = overlay.Description
	}
	if overlay.Summary != "" {
		detail.Summary = overlay.Summary
	}
	if overlay.Pipeline != "" {
		detail.Pipeline = overlay.Pipeline
	}
	if overlay.Models != (config.ModelConfig{}) {
		detail.Models = overlay.Models
	}
	if overlay.ActiveRunDetail != nil {
		detail.ActiveRunDetail = overlay.ActiveRunDetail
	}
	if len(overlay.HistoricalRuns) > 0 {
		detail.HistoricalRuns = overlay.HistoricalRuns
	}
	if len(overlay.RepoStatus) > 0 {
		detail.RepoStatus = overlay.RepoStatus
	}
	if overlay.Cycle != nil {
		detail.Cycle = overlay.Cycle
	}
	if !reflect.DeepEqual(overlay.Timing, server.TimingDTO{}) {
		detail.Timing = overlay.Timing
	}
	if !reflect.DeepEqual(overlay.Cost, server.CostDTO{}) {
		detail.Cost = overlay.Cost
	}
	if !reflect.DeepEqual(overlay.ReviewGate, server.ReviewGateDTO{}) {
		detail.ReviewGate = overlay.ReviewGate
	}
	if overlay.Failure != nil {
		detail.Failure = overlay.Failure
	}
	if overlay.NeedUserInput != nil {
		detail.NeedUserInput = overlay.NeedUserInput
	}
	if len(overlay.Actions) > 0 {
		detail.Actions = overlay.Actions
	}
	if overlay.Revision != "" {
		detail.Revision = overlay.Revision
	}
	if overlay.CacheRevalidate != "" {
		detail.CacheRevalidate = overlay.CacheRevalidate
	}
	return detail
}

func apiTestSessionDetail(summary server.SessionSummaryDTO) server.SessionDetailDTO {
	return server.SessionDetailDTO{
		ID:         summary.ID,
		FeatureID:  summary.FeatureID,
		Phase:      summary.Phase,
		Repo:       summary.Repo,
		Kind:       summary.Kind,
		Label:      summary.Label,
		Provider:   summary.Provider,
		Model:      summary.Model,
		Status:     summary.Status,
		TurnState:  summary.TurnState,
		StartedAt:  summary.StartedAt,
		Iteration:  summary.Iteration,
		ContextPct: summary.ContextPct,
		Usage:      summary.Usage,
	}
}

func apiTestSessionDetailWith(summary server.SessionSummaryDTO, overlay server.SessionDetailDTO) server.SessionDetailDTO {
	detail := apiTestSessionDetail(summary)
	if !reflect.DeepEqual(overlay.TranscriptCursor, server.CursorDTO{}) {
		detail.TranscriptCursor = overlay.TranscriptCursor
	}
	if len(overlay.PendingControls) > 0 {
		detail.PendingControls = overlay.PendingControls
	}
	if overlay.InitialPrompt != "" {
		detail.InitialPrompt = overlay.InitialPrompt
	}
	if overlay.CanAttach {
		detail.CanAttach = overlay.CanAttach
	}
	if overlay.LogAvailable {
		detail.LogAvailable = overlay.LogAvailable
	}
	if overlay.SafeError != "" {
		detail.SafeError = overlay.SafeError
	}
	return detail
}

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
			{ID: testFeatureIDDone, Name: "Done feature", Slug: "done-feature", Status: testFeatureStatusDone, CurrentPhase: actionIDPublish, CreatedAt: created.Add(-2 * time.Hour)},
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, ActiveRun: 2, RunCount: 3, Repos: []string{testRepoNameOrchestrator}, CreatedAt: created},
			{ID: testFeatureIDPublished, Name: "Published feature", Slug: testFeatureSlugPublishedFeature, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: created.Add(-1 * time.Hour)},
		}},
		runtime: server.RuntimeConfigResponse{
			Runtime:   runtime,
			Providers: []string{testProviderCodex},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{testProviderCodex},
			ProviderModels: map[string][]server.ModelDTO{
				testProviderCodex: {{ID: testModelGPT54}},
			},
		},
		prompts: server.PromptSnapshotResponse{HelpQueue: []server.HelpQueueDTO{
			{FeatureID: testFeatureIDActive, Question: "Need a decision", Pending: true},
		}},
		permissions: server.PermissionSnapshotResponse{Requests: []server.ControlRequestDTO{
			{FeatureID: testFeatureIDActive, RequestID: testPermissionRequestIDPerm1, Status: testStatusPending, ToolName: toolNameBash, Summary: "run tests"},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Repo: testRepoNameOrchestrator, Kind: testSessionKindAgent, Label: testActivityImplement, Provider: testProviderCodex, Model: testModelGPT54, Status: featureStatusTokenRunning, ContextPct: 42},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement}, server.FeatureDetailDTO{

			Description: "Render selected feature detail from REST.",
			Pipeline:    testPipelineRoadmap,
			RepoStatus: []server.RepoStatusDTO{
				{Name: testRepoNameOrchestrator, Touched: true, Publishable: true, CycleType: testCycleTypeRebase, CycleStatus: featureStatusTokenRunning},
			},
			Actions: []server.ActionDTO{
				{ID: mutationKindFeatureStop, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
				{ID: testActionIDFeaturePublish, Enabled: false, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}, DisabledReasons: []server.ActionDisabledReasonDTO{
					{Code: "not_ready", Message: "feature is not ready to publish"},
				}},
			},
			Cost:          server.CostDTO{TotalUSD: 12.34},
			NeedUserInput: &server.NeedInputGateDTO{FeatureID: testFeatureIDActive, Open: true, Scope: testActionScopeFeature, Iteration: 9},
		})},
	}

	app, err := NewAPIAppModel(ctx, client, APIAppOptions{
		Runtime:      runtime,
		LaunchPolicy: server.LaunchPolicy{Resolved: true, Providers: []string{testProviderCodex}, DangerouslySkipPermissions: true},
	})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	if got, want := strings.Join(client.calls, ","), "Features,RuntimeConfig,ModelCatalog,Prompts,Permissions,Sessions,Recovery,FeatureDetail,LivePreview"; got != want {
		t.Fatalf("API calls = %s, want %s", got, want)
	}
	if got := strings.Join(client.detailFeatureIDs, ","); got != testFeatureIDActive {
		t.Fatalf("FeatureDetail calls = %q, want active", got)
	}
	if got := app.selectedFeature; got != testFeatureIDActive {
		t.Fatalf("selectedFeature = %q, want active", got)
	}
	if got := app.snapshot.Features[0].AttentionCount; got != 2 {
		t.Fatalf("active AttentionCount = %d, want 2", got)
	}
	if got := app.snapshot.Runtime.DangerouslySkipPermissions; !got {
		t.Fatal("Runtime.DangerouslySkipPermissions = false, want true from launch policy")
	}
	view := stripANSI(app.View().Content)
	for _, want := range []string{"Orchestrator v", dashboardFeaturesPanelTitle, testSectionLabelInProgress, "PUBLISHED", "COMPLETED", testFeatureSlugClientCutover, testFeatureSlugPublishedFeature, "done-feature", testLabelLivePreview, "Permission Request", "Bash: run tests", "$12.34", testStatusNeedUserInput} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{"Selected detail", "Attach / Live Preview", testLabelRunContent, "Operations"} {
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
		ID:           testFeatureIDReady,
		Name:         "Ready work",
		Slug:         "ready-work",
		Status:       testFeatureStatusCodeReady,
		CurrentPhase: actionIDPublish,
		Repos:        []string{testRepoNameOrchestrator},
		CreatedAt:    time.Now(),
		Checkpoints:  server.CheckpointsDTO{ManualPublish: true},
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{

			RepoStatus: []server.RepoStatusDTO{
				{Name: testRepoNameOrchestrator, Publishable: true},
			},
		})},
	}
	app := newTestAPIAppModel(t, client)

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
		ID:           testFeatureIDFeat1,
		Name:         testFeatureNameTranslateReadme,
		Slug:         testFeatureSlugTranslateReadme,
		Status:       "Designing",
		CurrentPhase: testArtifactIDDesign,
		Repos:        []string{testRepoNameOrchestrator},
		CreatedAt:    time.Now(),
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail:   server.FeatureDetailResponse{Feature: apiTestFeatureDetail(summary)},
	}
	app := newTestAPIAppModel(t, client)
	if got := app.snapshot.Features[0].AttentionCount; got != 0 {
		t.Fatalf("initial AttentionCount = %d, want 0", got)
	}

	// A refresh that fetched the prompt snapshot but then errored on a later
	// call (e.g. live-preview blocked by a resume handshake until the client
	// timed out) must still surface the pending question, not discard it.
	model, _ := app.Update(apiRefreshSnapshotMsg{
		snapshot: server.RefreshSnapshot{Prompts: &server.PromptSnapshotResponse{
			AskUserQuestions: []server.ControlRequestDTO{
				{RequestID: testRequestIDReq1, FeatureID: testFeatureIDFeat1, ToolName: toolNameAskUserQuestion, Status: testStatusPending},
			},
		}},
		err: errors.New(`send request: Get ".../features/feat-1/live-preview": context deadline exceeded`),
	})
	updated := model.(APIAppModel)
	if !strings.Contains(updated.statusMessage, "Refresh failed") {
		t.Fatalf("statusMessage = %q, want it to surface the refresh error", updated.statusMessage)
	}
	if got := updated.snapshot.Features[0].AttentionCount; got != 1 {
		t.Fatalf("AttentionCount after partial refresh = %d, want 1 (prompt snapshot applied despite error)", got)
	}
}

func TestAPIAppModelDashboardRestoresMainBranchSpinnerVisuals(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           testFeatureIDActive,
		Name:         testFeatureNameTranslateSicilian,
		Slug:         testFeatureSlugTranslateSicilian,
		Status:       testFeatureStatusPlanning,
		CurrentPhase: testArtifactIDPlan,
		Repos:        []string{testRepoNameOrchestrator},
		CreatedAt:    time.Now(),
		Progress: server.FeatureProgress{
			CurrentRoadmapPhase: 0,
			TotalRoadmapPhases:  3,
			CurrentPhaseStatus:  featureStatusTokenRunning,
		},
	}
	app := APIAppModel{
		width:          160,
		height:         40,
		focusPanel:     0,
		rightPanelMode: dashboardRightPanelOverview,
		featureList:    server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			summary.ID: {Feature: apiTestFeatureDetail(summary)},
		},
		runtimeConfig: server.RuntimeConfigResponse{
			Runtime: server.RuntimeIdentity{StateDir: testRuntimeStateDirFeatures},
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
		ID:           testFeatureIDPublished,
		Name:         "Published feature",
		Slug:         testFeatureSlugPublishedFeature,
		Status:       testFeatureStatusPublished,
		CurrentPhase: helpContextPublish,
		Repos:        []string{testRepoNameOrchestrator},
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
			summary.ID: {Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{

				Timing: server.TimingDTO{ByPhase: map[string]int64{
					testPhaseKeyResearch:     60,
					testArtifactIDPhase1Plan: 120,
					testArtifactIDPhase1Impl: 180,
					reviewCommentTypeReview:  240,
				}},
				Cost: server.CostDTO{
					TotalUSD: 7.25,
					ByPhase: map[string]float64{
						testPhaseKeyResearch:     1.25,
						testArtifactIDPhase1Plan: 2.50,
						testArtifactIDPhase1Impl: 3.00,
						reviewCommentTypeReview:  0.50,
					},
				},
			})},
		},
		runtimeConfig: server.RuntimeConfigResponse{
			Runtime: server.RuntimeIdentity{StateDir: testRuntimeStateDirFeatures},
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
	if got := dashboard.preview.feature.PhaseCost(testPhaseKeyResearch); got != 1.25 {
		t.Fatalf("dashboard preview research cost = %v, want 1.25; costs=%v", got, dashboard.preview.feature.PhaseCosts)
	}
	view := stripANSI(dashboard.View())
	for _, want := range []string{"Cost", "$7.25", testActivityResearch, "$1.25", "Phase 1 Plan", "$2.50", "Phase 1", "$3.00", testPhaseNameFinalReview, "$0.50"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API dashboard View() missing phase cost %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelDashboardShowsDerivedWorkDir(t *testing.T) {
	t.Parallel()

	runtimeDir := t.TempDir()
	stateDir := filepath.Join(runtimeDir, "features")
	workDir := filepath.Join(runtimeDir, "worktrees", feature.WorkspaceSlug(testFeatureSlugTranslateSicilian, testFeatureIDActive), testRepoNameOrchestrator)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	summary := server.FeatureSummary{
		ID:           testFeatureIDActive,
		Name:         testFeatureNameTranslateSicilian,
		Slug:         testFeatureSlugTranslateSicilian,
		Status:       testFeatureStatusPlanning,
		CurrentPhase: testArtifactIDPlan,
		Repos:        []string{testRepoNameOrchestrator},
		CreatedAt:    time.Now(),
	}
	app := APIAppModel{
		width:          160,
		height:         40,
		focusPanel:     0,
		rightPanelMode: dashboardRightPanelOverview,
		featureList:    server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			summary.ID: {Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{

				RepoStatus: []server.RepoStatusDTO{
					{Name: testRepoNameOrchestrator, Publishable: true},
				},
			})},
		},
		runtimeConfig: server.RuntimeConfigResponse{
			Runtime: server.RuntimeIdentity{StateDir: stateDir},
			Repos: []server.ConfigRepoDTO{
				{Name: testRepoNameOrchestrator, Path: "/repo/path"},
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
	for _, want := range []string{"WorkDir", "worktrees/translate-readme-", "in-sicilian-active/agentic-orchestrator"} {
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
		ID:           testFeatureIDSetupFail,
		Name:         "Setup Fail",
		Slug:         testFeatureIDSetupFail,
		Status:       testFeatureStatusFailed,
		CurrentPhase: testArtifactIDPlan,
		ActiveRun:    1,
		RunCount:     1,
		Repos:        []string{testRepoNameA},
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
			Runtime: server.RuntimeIdentity{StateDir: testRuntimeStateDirFeatures},
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
	for _, want := range []string{testSectionLabelInProgress, "Failed (worktree setup)", "Setup attempt 1", "git worktree add failed", "Worktree: repo-a", "/tmp/agentico/setup.log"} {
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
			{ID: testFeatureIDSetupFail, Name: "Setup Fail", Slug: testFeatureIDSetupFail, Status: testFeatureStatusFailed, CurrentPhase: testArtifactIDPlan, CreatedAt: time.Now()},
		}},
		detail:        detail,
		retryAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd == nil {
		t.Fatal("Update(r) returned nil command, want setup retry mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)

	if got := strings.Join(client.retryFeatureIDs, ","); got != testFeatureIDSetupFail {
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
		featureID = testFeatureIDActive
		slug      = "base-aware-diff"
		repoName  = testRepoNameOrchestrator
	)
	workDir := filepath.Join(runtimeDir, "worktrees", slug, repoName)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	runGit(t, workDir, "init", "-b", testGitBranchMain)
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
	runGit(t, workDir, "push", "-u", "origin", testGitBranchMain)
	runGit(t, workDir, "remote", "set-head", "origin", testGitBranchMain)

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
		Status:       testFeatureStatusCodeReady,
		CurrentPhase: actionIDPublish,
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
		Status:       testFeatureStatusCreated,
		CurrentPhase: testPhaseKeyResearch,
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
			summary.ID: {Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{

				Models: selected,
			})},
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

	cycle := &server.CycleDTO{Type: testPipelineRefactor, Status: featureStatusTokenRunning, Count: 1, Iteration: 1}
	summary := server.FeatureSummary{
		ID:           testFeatureIDActive,
		Name:         "Sicilian README",
		Slug:         "translate-in-sicilian",
		Status:       testFeatureStatusCodeReady,
		CurrentPhase: actionIDPublish,
		Cycle:        cycle,
		Repos:        []string{testRepoNameOrchestrator},
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
			summary.ID: {Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{

				Cycle:  cycle,
				Timing: server.TimingDTO{ByPhase: map[string]int64{testPhaseNameImplement: 300}},
				RepoStatus: []server.RepoStatusDTO{
					{Name: testRepoNameOrchestrator, Touched: true, Publishable: true, CycleType: testPipelineRefactor, CycleStatus: featureStatusTokenRunning},
				},
			})},
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
	for _, want := range []string{labelInfo, "Phase Progress", "Refactor #1", "in progress", "[l] Live Preview"} {
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

	cycle := &server.CycleDTO{Type: testCycleTypeRebase, Status: featureStatusTokenRunning, Count: 1, Iteration: 1}
	summary := server.FeatureSummary{
		ID:           testFeatureIDActive,
		Name:         "Multi Repo Rebase",
		Slug:         "multi-repo-rebase",
		Status:       testFeatureStatusCodeReady,
		CurrentPhase: actionIDPublish,
		Cycle:        cycle,
		Repos:        []string{testRepoNameAPI, testRepoNameWeb},
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
			summary.ID: {Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{

				Cycle: cycle,
				RepoStatus: []server.RepoStatusDTO{
					{Name: testRepoNameAPI, Touched: true, Publishable: true, Freshness: testFreshnessLocalChanges, RebaseStatus: "conflict", ConflictFiles: []string{"service.go"}},
					{Name: testRepoNameWeb, Touched: true, Publishable: true, Freshness: testFreshnessInSync, RebaseStatus: "up_to_date"},
				},
			})},
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
	if f.RebaseOperation == nil || f.RebaseOperation.Repos[testRepoNameAPI].Status != feature.RebaseRepoStatusConflict {
		t.Fatalf("RebaseOperation = %+v, want api conflict progress", f.RebaseOperation)
	}
	if got := f.RepoStates[testRepoNameAPI].Freshness; got != testFreshnessLocalChanges {
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
	for _, want := range []string{labelInfo, "Rebasing [1]", "Repo Status", testRepoNameAPI, "conflict: service.go", testFreshnessLocalChanges, testRepoNameWeb, testFreshnessInSync} {
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
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		runtime: server.RuntimeConfigResponse{
			Repos:     []server.ConfigRepoDTO{{Name: testRepoNameOrchestrator, Path: "/workspace/agentic-orchestrator"}},
			Providers: []string{testProviderCodex},
		},
		livePreview: server.LivePreviewResponse{
			Feature:  server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover},
			Activity: testUsingBashActivity,
			Session:  &server.SessionSummaryDTO{ID: testSessionIDLive, FeatureID: testFeatureIDActive, Label: testActivityImplement, Status: featureStatusTokenRunning},
		},
	}
	app := newTestAPIAppModel(t, client)

	view := stripANSI(app.View().Content)
	flatView := strings.Join(strings.Fields(view), " ")
	for _, want := range []string{
		"Orchestrator v",
		dashboardFeaturesPanelTitle,
		testSectionLabelInProgress,
		testFeatureSlugClientCutover,
		testLabelLivePreview,
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
	for _, notWant := range []string{"Agentico API Client", "Selected detail", "Attach / Live Preview", testLabelRunContent} {
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
	app := newTestAPIAppModel(t, client)

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

func TestAPIAppModelWelcomeRoutesDirPickerScanMessages(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, actionInputNameRepo, ".git"), 0o755); err != nil {
		t.Fatalf("create repo fixture: %v", err)
	}
	client := &fakeTUIAPIClient{
		features:                    server.FeatureListResponse{},
		runtime:                     server.RuntimeConfigResponse{},
		allowEmptyWorkspaceRoots:    true,
		updateRuntimeConfigAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter welcome) returned nil command, want picker init scans")
	}
	app = model.(APIAppModel)
	if app.welcome == nil || app.welcome.step != welcomeStepPicker {
		t.Fatalf("welcome = %+v, want picker step", app.welcome)
	}
	app.welcome.picker.setCurrentDir(root)

	model, _ = app.Update(gitRepoScanMsg{
		dir:           root,
		count:         1,
		repoDirs:      map[string]bool{actionInputNameRepo: true},
		dirRepoCounts: map[string]int{},
	})
	app = model.(APIAppModel)
	if got := app.welcome.picker.gitRepoCount; got != 1 {
		t.Fatalf("welcome picker gitRepoCount = %d, want 1", got)
	}

	model, cmd = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter selected repo) returned nil command, want runtime config mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	app = model.(APIAppModel)
	if got := client.updateRuntimeConfigRequests; len(got) != 1 || got[0].WorkspaceRoots == nil || len(*got[0].WorkspaceRoots) != 1 || (*got[0].WorkspaceRoots)[0] != filepath.Join(root, actionInputNameRepo) {
		t.Fatalf("UpdateRuntimeConfig requests = %+v, want selected repo root", got)
	}
}

func TestAPIAppModelRecoverySnapshotUsesRESTAndSubmitsAction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		recovery: server.RecoverySnapshotResponse{
			SnapshotID: "recovery-snapshot-1",
			Items: []server.RecoveryItemDTO{{
				Key:            "feat-recover:api",
				FeatureID:      "feat-recover",
				FeatureName:    "Recover me",
				RepoName:       testRepoNameAPI,
				Phase:          testPhaseNameImplement,
				Iteration:      7,
				PID:            12345,
				ProcessAlive:   true,
				DefaultAction:  testActionSkip,
				AllowedActions: []string{recoveryActionResume, testActionKill, testActionSkip},
			}},
		},
		executeRecoveryAccepted: apiTestActionResponse{Result: "executed"},
	}

	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	view := stripANSI(app.View().Content)
	for _, want := range []string{"Session Recovery", "Recover me", testRepoNameAPI, testPhaseNameImplement, "iter 7", "PID 12345", "[S]kip"} {
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
	if got := client.executeRecoveryRequests[0].Actions["feat-recover:api"]; got != recoveryActionResume {
		t.Fatalf("ExecuteRecovery action = %q, want resume", got)
	}
	if strings.Contains(stripANSI(submitted.View().Content), "Session Recovery") {
		t.Fatalf("API app View() still shows recovery panel after accepted submit:\n%s", stripANSI(submitted.View().Content))
	}
	if !strings.Contains(submitted.statusMessage, "Completed Recovery") {
		t.Fatalf("statusMessage = %q, want completed recovery status", submitted.statusMessage)
	}
}

func TestAPIAppModelSessionSnapshotRefreshUsesAPIReadModels(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Repo: testRepoNameOrchestrator, Kind: testSessionKindAgent, Label: testActivityImplement, Status: featureStatusTokenRunning, ContextPct: 10},
		}},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	initialDetailCalls := len(client.detailFeatureIDs)

	initial := app.snapshot
	if len(initial.Sessions) != 1 || initial.Sessions[0].ID != testSessionIDOne || initial.Sessions[0].ContextPct != 10 {
		t.Fatalf("initial Snapshot().Sessions = %+v, want sess-1 at 10%%", initial.Sessions)
	}

	signal := server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: testEventKindSessionUpdated},
		Resource: server.ResourceDTO{Type: testResourceIDSession, ID: testSessionIDOne, FeatureID: testFeatureIDActive},
	}
	client.refreshSnapshot = server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: apiTestSessionDetailWith(server.SessionSummaryDTO{ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Repo: testRepoNameOrchestrator, Kind: testSessionKindAgent, Label: testActivityImplement, Status: testStatusCompleted, ContextPct: 37}, server.SessionDetailDTO{

			CanAttach:    false,
			LogAvailable: true,
		})},
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
	snapshot := refreshed.snapshot
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].ID != testSessionIDOne || snapshot.Sessions[0].Status != testStatusCompleted || snapshot.Sessions[0].ContextPct != 37 || !snapshot.Sessions[0].LogAvailable {
		t.Fatalf("refreshed Snapshot().Sessions = %+v, want completed sess-1 with log at 37%%", snapshot.Sessions)
	}
}

func TestAPIAppModelAttachRefreshPrunesCompletedValidatorTab(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDImpl1, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: testSessionStatusRunning},
			{ID: testSessionIDTestingValidator, FeatureID: testFeatureIDActive, Phase: testArtifactIDPlan, Kind: testSessionKindValidator, Label: testValidatorNameTesting, Status: testSessionStatusRunning},
		}},
	}
	app := newTestAPIAppModel(t, client)

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
			{ID: testSessionIDImpl1, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: testSessionStatusRunning},
			{ID: testSessionIDTestingValidator, FeatureID: testFeatureIDActive, Phase: testArtifactIDPlan, Kind: testSessionKindValidator, Label: testValidatorNameTesting, Status: testFeatureStatusDone},
		}},
	}})
	refreshed := model.(APIAppModel)
	if refreshed.attach == nil {
		t.Fatal("attach view closed; want it to stay on remaining implementation session")
	}
	if got := len(refreshed.attach.repoTabs); got != 1 {
		t.Fatalf("attach tab count = %d, want 1 after completed validator is pruned: %+v", got, refreshed.attach.repoTabs)
	}
	if got := refreshed.attach.repoTabs[0].sess.ID(); got != testSessionIDImpl1 {
		t.Fatalf("remaining attach session = %q, want impl-1", got)
	}
	if view := stripANSI(refreshed.View().Content); strings.Contains(view, testValidatorNameTesting) || strings.Contains(view, "Plan (running)") {
		t.Fatalf("attach view still renders completed validator as running:\n%s", view)
	}
}

func TestAPIAttachTabsKeepImplementationFirstAndReviewAxesStable(t *testing.T) {
	t.Parallel()

	impl := session.NewSession("impl", testFeatureIDActive, feature.PhaseImplement)
	design := session.NewSession("design", testFeatureIDActive, feature.PhaseFinalReview)
	functionality := session.NewSession("functionality", testFeatureIDActive, feature.PhaseFinalReview)
	cleanliness := session.NewSession("cleanliness", testFeatureIDActive, feature.PhaseFinalReview)
	tabs := []repoTab{
		{repoName: "Design", label: "Design", kind: ports.KindValidator, sess: design},
		{repoName: "Cleanliness", label: "Cleanliness", kind: ports.KindValidator, sess: cleanliness},
		{repoName: "impl", kind: ports.KindPhase, sess: impl},
		{repoName: "Functionality/Evidence", label: "Functionality/Evidence", kind: ports.KindValidator, sess: functionality},
	}

	apiOrderAttachTabs(tabs)
	got := []string{tabs[0].repoName, tabs[1].label, tabs[2].label, tabs[3].label}
	want := []string{"impl", "Functionality/Evidence", "Cleanliness", "Design"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered attach tabs = %v, want %v", got, want)
	}
}

func TestAPIInitialAttachTabPrefersRunningAxisOverFailedAxis(t *testing.T) {
	t.Parallel()

	failed := session.NewSession("design", testFeatureIDActive, feature.PhaseFinalReview)
	running := session.NewSession("functionality", testFeatureIDActive, feature.PhaseFinalReview)
	tabs := []repoTab{
		{repoName: "Design", label: "Design", kind: ports.KindValidator, sess: failed, status: statusFailed},
		{repoName: "Functionality/Evidence", label: "Functionality/Evidence", kind: ports.KindValidator, sess: running, status: statusImplementing},
	}

	if got := apiInitialAttachTab(tabs); got != 1 {
		t.Fatalf("apiInitialAttachTab() = %d, want running Functionality/Evidence tab", got)
	}
}

func TestAPIAppModelAttachShowsAllKnowledgeBaseRepoSessions(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: "Knowledge Base", CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: "feat-kb-dbaccess", FeatureID: testFeatureIDActive, Phase: "Knowledge Base", Repo: "dbaccess", Kind: testSessionKindAgent, Status: testSessionStatusRunning},
			{ID: "feat-kb-taulu", FeatureID: testFeatureIDActive, Phase: "Knowledge Base", Repo: "taulu", Kind: testSessionKindAgent, Status: testSessionStatusRunning},
		}},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	attached := model.(APIAppModel)
	if attached.attach == nil {
		t.Fatal("attach view did not open")
	}
	if got := len(attached.attach.repoTabs); got != 2 {
		t.Fatalf("attach tab count = %d, want 2 for per-repo KB sessions: %+v", got, attached.attach.repoTabs)
	}
	if got := []string{attached.attach.repoTabs[0].repoName, attached.attach.repoTabs[1].repoName}; !reflect.DeepEqual(got, []string{"dbaccess", "taulu"}) {
		t.Fatalf("attach repo tabs = %v, want [dbaccess taulu]", got)
	}
}

func TestAPIAppModelAttachRefreshRestoresMissingKnowledgeBaseRepoTabs(t *testing.T) {
	t.Parallel()

	fullSessions := server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
		{ID: "feat-kb-dbaccess", FeatureID: testFeatureIDActive, Phase: "Knowledge Base", Repo: "dbaccess", Kind: testSessionKindAgent, Status: testSessionStatusRunning},
		{ID: "feat-kb-dbmesh", FeatureID: testFeatureIDActive, Phase: "Knowledge Base", Repo: "dbmesh", Kind: testSessionKindAgent, Status: testSessionStatusRunning},
		{ID: "feat-kb-taulu", FeatureID: testFeatureIDActive, Phase: "Knowledge Base", Repo: "taulu", Kind: testSessionKindAgent, Status: testSessionStatusRunning},
	}}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: "Knowledge Base", CreatedAt: time.Now()},
		}},
		sessions: fullSessions,
	}
	app := newTestAPIAppModel(t, client)
	app.sessionList = server.SessionListResponse{Sessions: fullSessions.Sessions[:1]}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init plus session refresh")
	}
	attached := model.(APIAppModel)
	if got := len(attached.attach.repoTabs); got != 1 {
		t.Fatalf("initial attach tab count = %d, want stale single tab before refresh", got)
	}

	refresh := apiTestAttachSessionsSnapshotMsg(t, cmd)
	model, _ = attached.Update(refresh)
	refreshed := model.(APIAppModel)
	if got := len(refreshed.attach.repoTabs); got != 3 {
		t.Fatalf("refreshed attach tab count = %d, want 3: %+v", got, refreshed.attach.repoTabs)
	}
	if got := []string{refreshed.attach.repoTabs[0].repoName, refreshed.attach.repoTabs[1].repoName, refreshed.attach.repoTabs[2].repoName}; !reflect.DeepEqual(got, []string{"dbaccess", "dbmesh", "taulu"}) {
		t.Fatalf("refreshed attach repo tabs = %v, want [dbaccess dbmesh taulu]", got)
	}
}

func TestAPIAppModelAttachRefreshLoadsPromptForSiblingKnowledgeBaseTabs(t *testing.T) {
	t.Parallel()

	dbaccess := server.SessionSummaryDTO{ID: "feat-kb-dbaccess", FeatureID: testFeatureIDActive, Phase: "Knowledge Base", Repo: "dbaccess", Kind: testSessionKindAgent, Status: testSessionStatusRunning}
	dbmesh := server.SessionSummaryDTO{ID: "feat-kb-dbmesh", FeatureID: testFeatureIDActive, Phase: "Knowledge Base", Repo: "dbmesh", Kind: testSessionKindAgent, Status: testSessionStatusRunning}
	taulu := server.SessionSummaryDTO{ID: "feat-kb-taulu", FeatureID: testFeatureIDActive, Phase: "Knowledge Base", Repo: "taulu", Kind: testSessionKindAgent, Status: testSessionStatusRunning}
	const dbmeshPrompt = "Map the dbmesh repository and include exact file paths."
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: "Knowledge Base", CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{dbaccess, dbmesh, taulu}},
		sessionDetailsByID: map[string]server.SessionDetailResponse{
			dbaccess.ID: {Session: apiTestSessionDetailWith(dbaccess, server.SessionDetailDTO{InitialPrompt: "Map the dbaccess repository."})},
			dbmesh.ID:   {Session: apiTestSessionDetailWith(dbmesh, server.SessionDetailDTO{InitialPrompt: dbmeshPrompt})},
			taulu.ID:    {Session: apiTestSessionDetailWith(taulu, server.SessionDetailDTO{InitialPrompt: "Map the taulu repository."})},
		},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init plus session refresh")
	}
	refresh := apiTestAttachSessionsSnapshotMsg(t, cmd)
	model, cmd = model.(APIAppModel).Update(refresh)
	detailMsgs := apiTestRefreshSnapshotMsgs(t, cmd)
	for _, msg := range detailMsgs {
		model, _ = model.(APIAppModel).Update(msg)
	}

	model, _ = model.(APIAppModel).updateAPIAttach(tea.KeyPressMsg{Code: tea.KeyTab})
	switched := model.(APIAppModel)
	if got := switched.attach.ActiveRepoName(); got != "dbmesh" {
		t.Fatalf("active repo after tab = %q, want dbmesh", got)
	}
	if view := stripANSI(switched.attach.View()); !strings.Contains(view, dbmeshPrompt) {
		t.Fatalf("dbmesh attach view missing initial prompt %q:\n%s", dbmeshPrompt, view)
	}
}

func apiTestAttachSessionsSnapshotMsg(t *testing.T, cmd tea.Cmd) apiAttachSessionsSnapshotMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("command is nil")
	}
	msg := cmd()
	if refresh, ok := msg.(apiAttachSessionsSnapshotMsg); ok {
		return refresh
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("command returned %T, want apiAttachSessionsSnapshotMsg or tea.BatchMsg", msg)
	}
	for _, child := range batch {
		if child == nil {
			continue
		}
		if refresh, ok := child().(apiAttachSessionsSnapshotMsg); ok {
			return refresh
		}
	}
	t.Fatalf("batch command did not include apiAttachSessionsSnapshotMsg: %#v", msg)
	return apiAttachSessionsSnapshotMsg{}
}

func apiTestRefreshSnapshotMsgs(t *testing.T, cmd tea.Cmd) []apiRefreshSnapshotMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("command is nil")
	}
	var out []apiRefreshSnapshotMsg
	apiTestCollectRefreshSnapshotMsgs(cmd(), &out)
	if len(out) == 0 {
		t.Fatal("command did not produce any apiRefreshSnapshotMsg")
	}
	return out
}

func apiTestCollectRefreshSnapshotMsgs(msg tea.Msg, out *[]apiRefreshSnapshotMsg) {
	switch msg := msg.(type) {
	case nil:
		return
	case apiRefreshSnapshotMsg:
		*out = append(*out, msg)
	case tea.BatchMsg:
		for _, child := range msg {
			if child != nil {
				apiTestCollectRefreshSnapshotMsgs(child(), out)
			}
		}
	}
}

// TestApplyTranscriptRowStreamThenSnapshotDoesNotDuplicate is the property
// this redesign exists to guarantee: the live-stream push path and the
// snapshot-refresh pull path both funnel through applyTranscriptRow, which
// tracks exactly one key/signature watermark. A row the stream already
// applied must be a no-op when the snapshot refresh later sees the same
// row (same index, same content) — while a different, newer row present
// in that same snapshot must still get applied.
func TestApplyTranscriptRowStreamThenSnapshotDoesNotDuplicate(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{}
	sess := newAPIAttachSession(client, apiTestSessionDetail(
		server.SessionSummaryDTO{ID: testSessionIDOne, FeatureID: testFeatureIDFeat1, Status: testSessionStatusRunning}),

		server.TranscriptResponse{}, nil)

	streamedRow := server.TranscriptMessageDTO{Index: 0, Role: roleAssistant, Type: testMessageTypeText, Text: "hello from the stream"}

	// Apply the row via the live-stream push path.
	if msg := sess.applyTranscriptRow(streamedRow); msg == nil {
		t.Fatal("applyTranscriptRow(streamedRow) = nil, want the row applied on first sight")
	}
	if got := sess.MessageLog().Len(); got != 1 {
		t.Fatalf("MessageLog().Len() = %d after stream apply, want 1", got)
	}

	newRow := server.TranscriptMessageDTO{Index: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "a newer row in the same snapshot"}

	// A snapshot refresh later observes both the already-streamed row and
	// a genuinely new one.
	messages := sess.applyAPISessionSnapshot(apiTestSessionDetail(
		server.SessionSummaryDTO{ID: testSessionIDOne, FeatureID: testFeatureIDFeat1, Status: testSessionStatusRunning}),

		&server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{streamedRow, newRow}}, nil)

	if got := sess.MessageLog().Len(); got != 2 {
		t.Fatalf("MessageLog().Len() = %d after snapshot with one duplicate + one new row, want 2 (no duplication)", got)
	}
	if len(messages) != 1 {
		t.Fatalf("applyAPISessionSnapshot returned %d messages, want 1 (only the new row, duplicate is a no-op)", len(messages))
	}
	if messages[0].Assistant == nil || len(messages[0].Assistant.Message.Content) == 0 || messages[0].Assistant.Message.Content[0].Text != newRow.Text {
		t.Fatalf("applyAPISessionSnapshot's returned message = %+v, want the new row's text %q", messages[0], newRow.Text)
	}
}

// TestApplyTranscriptRowIsRaceSafeAcrossConcurrentCallers exercises the two
// real, unsynchronized callers of applyTranscriptRow: the snapshot-refresh
// path runs on bubbletea's main Update goroutine, the live-stream path runs
// on listenLiveSessionOutputCmd's own tea.Cmd goroutine. Without s.mu
// guarding the watermark fields, concurrent calls with overlapping row
// indices can race on lastTranscriptMessage or hit a concurrent write on
// the lastTranscriptRows map (a plain map, so a genuine collision panics
// the whole process rather than just corrupting a value). Run with -race.
func TestApplyTranscriptRowIsRaceSafeAcrossConcurrentCallers(t *testing.T) {
	t.Parallel()

	sess := newAPIAttachSession(nil, apiTestSessionDetail(server.SessionSummaryDTO{ID: testSessionIDOne}), server.TranscriptResponse{}, nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		row := server.TranscriptMessageDTO{Index: i, Role: roleAssistant, Type: testMessageTypeText, Text: fmt.Sprintf("msg-%d", i)}
		wg.Add(2)
		go func(r server.TranscriptMessageDTO) {
			defer wg.Done()
			sess.applyTranscriptRow(r)
		}(row)
		go func(r server.TranscriptMessageDTO) {
			defer wg.Done()
			sess.applyTranscriptRow(r)
		}(row)
	}
	wg.Wait()
}

// TestOpenAPIAttachStartsLiveOutputFeed proves attaching to a feature's
// session starts the live output feed (SubscribeSessionOutput), and that
// it resumes from index 0 for a freshly-loaded session with no transcript
// history yet (lastTranscriptMessage's -1 sentinel).
func TestOpenAPIAttachStartsLiveOutputFeed(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDFeat1, Name: testFeatureNameFeatureOne, Slug: testFeatureSlugFeatureOne, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDOne, FeatureID: testFeatureIDFeat1, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: featureStatusTokenRunning, Provider: testProviderClaude},
		}},
	}
	app := newTestAPIAppModel(t, client)

	model, _ := app.openAPIAttachForFeature(testFeatureIDFeat1)
	updated := model.(APIAppModel)
	if updated.liveOutputCancel == nil {
		t.Fatal("openAPIAttachForFeature did not start a live output feed")
	}
	if updated.liveOutputSessionID != testSessionIDOne {
		t.Fatalf("liveOutputSessionID = %q, want sess-1", updated.liveOutputSessionID)
	}
}

func TestAPIAppModelContextualAAttachesFromOverviewAndLivePreviewWithoutSessionSummary(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           testFeatureIDActive,
		Name:         testFeatureNameActiveWork,
		Slug:         testFeatureSlugActiveWork,
		Status:       testFeatureStatusPlanning,
		CurrentPhase: testArtifactIDPlan,
		CreatedAt:    time.Now(),
	}

	for _, mode := range []dashboardRightPanelMode{dashboardRightPanelOverview, dashboardRightPanelLivePreview} {
		t.Run(fmt.Sprintf("mode-%d", mode), func(t *testing.T) {
			client := &fakeTUIAPIClient{
				features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
				livePreview: server.LivePreviewResponse{
					Feature:  summary,
					Activity: testFeatureStatusPlanning,
				},
			}
			app := newTestAPIAppModel(t, client)
			app.rightPanelMode = mode

			model, _ := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
			attached := model.(APIAppModel)
			if attached.attach == nil {
				t.Fatalf("Update(a) did not open attach view from mode %d", mode)
			}
			if attached.liveOutputSessionID != "" {
				t.Fatalf("liveOutputSessionID = %q, want no stream without a concrete session ID", attached.liveOutputSessionID)
			}
			if attached.statusMessage == "Live preview is already visible" {
				t.Fatal("Update(a) reported live preview visibility instead of attaching")
			}
		})
	}
}

// TestLiveSessionOutputFeedAppliesTranscriptRow proves the live output feed
// actually grows the attached session's MessageLog through applyTranscriptRow
// — not just that attachCh received something. Pushing onto attachCh alone
// drives attach.go's attachMsgsMsg side effects (spinner, permission
// prompts) but updateViewport renders from MessageLog(), so without
// applying the row there the transcript text would never actually appear.
func TestLiveSessionOutputFeedAppliesTranscriptRow(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDFeat1, Name: testFeatureNameFeatureOne, Slug: testFeatureSlugFeatureOne, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDOne, FeatureID: testFeatureIDFeat1, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: featureStatusTokenRunning, Provider: testProviderClaude},
		}},
		subscribeSessionOutputRecords: []server.SessionOutputRecord{
			{SessionID: testSessionIDOne, Index: 0, Message: server.TranscriptMessageDTO{Index: 0, Role: roleAssistant, Type: testMessageTypeText, Text: "hi"}},
			{SessionID: testSessionIDOne, Index: 1, Message: server.TranscriptMessageDTO{Index: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "there"}},
		},
	}
	app := newTestAPIAppModel(t, client)

	model, _ := app.openAPIAttachForFeature(testFeatureIDFeat1)
	updated := model.(APIAppModel)
	sess := updated.attachedSessionView()
	if sess == nil {
		t.Fatal("no attached *apiSessionView after openAPIAttachForFeature")
	}
	before := sess.MessageLog().Len()

	// Drive the listen loop directly, feeding each returned message back
	// through updateAPIAttach to get the re-armed command — the same
	// request/response cycle the bubbletea runtime performs.
	liveCmd := updated.listenLiveSessionOutputCmd(sess)
	for i := range client.subscribeSessionOutputRecords {
		msg := liveCmd()
		lineMsg, ok := msg.(apiSessionOutputLineMsg)
		if !ok {
			t.Fatalf("iteration %d: got %T, want apiSessionOutputLineMsg", i, msg)
		}
		var cmd tea.Cmd
		model, cmd = updated.updateAPIAttach(lineMsg)
		updated = model.(APIAppModel)
		if cmd == nil {
			t.Fatalf("iteration %d: updateAPIAttach did not re-arm the listen loop", i)
		}
		liveCmd = cmd
	}

	want := len(client.subscribeSessionOutputRecords)
	if got := sess.MessageLog().Len() - before; got != want {
		t.Fatalf("MessageLog().Len() grew by %d, want %d", got, want)
	}
}

// TestAPIAttachDetachStopsLiveOutputFeed proves detaching from the attach
// view (esc) stops the live output feed, not just closes the attach panel.
func TestAPIAttachDetachStopsLiveOutputFeed(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDFeat1, Name: testFeatureNameFeatureOne, Slug: testFeatureSlugFeatureOne, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDOne, FeatureID: testFeatureIDFeat1, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: featureStatusTokenRunning, Provider: testProviderClaude},
		}},
	}
	app := newTestAPIAppModel(t, client)

	model, _ := app.openAPIAttachForFeature(testFeatureIDFeat1)
	attached := model.(APIAppModel)
	if attached.liveOutputCancel == nil {
		t.Fatal("openAPIAttachForFeature did not start a live output feed")
	}

	model, _ = attached.updateAPIAttach(tea.KeyPressMsg{Code: tea.KeyEscape})
	detached := model.(APIAppModel)
	if detached.attach != nil {
		t.Fatal("esc did not detach the attach view")
	}
	if detached.liveOutputCancel != nil || detached.liveOutputSessionID != "" {
		t.Fatalf("live output feed still running after detach: cancel=%v sessionID=%q", detached.liveOutputCancel != nil, detached.liveOutputSessionID)
	}
}

// TestAPIAttachTabSwitchResyncsLiveOutputFeed proves switching the visible
// attach tab (tab key, multi-repo attach) restarts the live output feed
// against the newly active session rather than leaving it tailing the
// previous tab's session.
func TestAPIAttachTabSwitchResyncsLiveOutputFeed(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDImpl1, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: testSessionStatusRunning},
			{ID: testSessionIDTestingValidator, FeatureID: testFeatureIDActive, Phase: testArtifactIDPlan, Kind: testSessionKindValidator, Label: testValidatorNameTesting, Status: testSessionStatusRunning},
		}},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	attached := model.(APIAppModel)
	if attached.attach == nil || len(attached.attach.repoTabs) != 2 {
		t.Fatalf("attach tabs = %+v, want two tabs", attached.attach.repoTabs)
	}
	firstSessionID := attached.liveOutputSessionID
	if firstSessionID == "" {
		t.Fatal("openAPIAttachForFeature did not start a live output feed")
	}

	model, _ = attached.updateAPIAttach(tea.KeyPressMsg{Code: tea.KeyTab})
	switched := model.(APIAppModel)
	if switched.liveOutputSessionID == "" {
		t.Fatal("live output feed stopped entirely after tab switch")
	}
	if switched.liveOutputSessionID == firstSessionID {
		t.Fatalf("liveOutputSessionID unchanged after tab switch, still %q", firstSessionID)
	}
	sess := switched.attachedSessionView()
	if sess == nil || sess.ID() != switched.liveOutputSessionID {
		t.Fatalf("live output feed session %q does not match the now-attached session %+v", switched.liveOutputSessionID, sess)
	}
}

func TestAPIAppModelLivePreviewLoadsSelectedFeatureFromREST(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		livePreview: server.LivePreviewResponse{
			Feature:  server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing},
			Activity: testUsingBashActivity,
			Session:  &server.SessionSummaryDTO{ID: testSessionIDLive, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Label: testActivityImplement, Status: featureStatusTokenRunning},
			Context:  server.ContextDTO{Percentage: 42},
			Cost:     server.CostDTO{TotalUSD: 0.42},
			Attention: []server.ControlRequestDTO{
				{RequestID: testAskRequestID, FeatureID: testFeatureIDActive, ToolName: toolNameAskUserQuestion, Status: testStatusPending, Summary: "Pick the cutover path"},
			},
			Transcript: []server.TranscriptMessageDTO{
				{Index: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "Ready to patch live preview"},
			},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	if got := strings.Join(client.livePreviewFeatureIDs, ","); got != testFeatureIDActive {
		t.Fatalf("LivePreview calls = %q, want active", got)
	}
	view := stripANSI(app.View().Content)
	for _, want := range []string{testLabelLivePreview, testUsingBashActivity, "42%", "$0.42", "Ready to patch live preview"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelOverviewUsesLivePreviewContextPct(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           testFeatureIDActive,
		Name:         testFeatureNameClientCutover,
		Slug:         testFeatureSlugClientCutover,
		Status:       testFeatureStatusImplementing,
		CurrentPhase: testPhaseNameImplement,
		CreatedAt:    time.Now(),
		Progress: server.FeatureProgress{
			CurrentIteration: 1,
		},
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail:   server.FeatureDetailResponse{Feature: apiTestFeatureDetail(summary)},
		livePreview: server.LivePreviewResponse{
			Feature: summary,
			Session: &server.SessionSummaryDTO{
				ID:        testSessionIDLive,
				FeatureID: testFeatureIDActive,
				Phase:     testPhaseNameImplement,
				Status:    featureStatusTokenRunning,
			},
			Context: server.ContextDTO{Percentage: 42},
		},
	}
	app := newTestAPIAppModel(t, client)

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
		ID:           testFeatureIDActive,
		Name:         testFeatureNameTranslateReadme,
		Slug:         testFeatureSlugTranslateReadme,
		Status:       "FinalReviewing",
		CurrentPhase: testPhaseNameFinalReview,
		CreatedAt:    time.Now(),
		Repos:        []string{testRepoNameOrchestrator},
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{

			Pipeline:        testPipelineSizeMedium,
			ActiveRunDetail: &server.RunSummaryDTO{RunNumber: 1, CurrentPhase: testPhaseNameFinalReview},
		})},
	}
	app := newTestAPIAppModel(t, client)

	app.rightPanelMode = dashboardRightPanelOverview
	dashboard := app.apiDashboardModel()
	if got := dashboard.preview.feature.CurrentPhase; got != feature.PhaseFinalReview {
		t.Fatalf("overview CurrentPhase = %s, want Final Review", got)
	}
	view := stripANSI(dashboard.View())
	finalReviewLine := ""
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, testPhaseNameFinalReview) && !strings.Contains(line, testColumnLabelStatus) {
			finalReviewLine = line
			break
		}
	}
	if finalReviewLine == "" {
		t.Fatalf("overview missing Final Review progress row in:\n%s", view)
	}
	if strings.Contains(finalReviewLine, testStatusPending) {
		t.Fatalf("Final Review row rendered as pending: %q\nview:\n%s", finalReviewLine, view)
	}
	if !strings.Contains(finalReviewLine, "reviewing") {
		t.Fatalf("Final Review row = %q, want reviewing state\nview:\n%s", finalReviewLine, view)
	}
}

func TestAPIAppModelLivePreviewPreservesTranscriptRowsFromREST(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		sessionExtra server.SessionSummaryDTO
		transcript   []server.TranscriptMessageDTO
		want         []string
		notWant      []string
	}{
		{
			name:         testMessageTypeToolUse,
			sessionExtra: server.SessionSummaryDTO{Label: testActivityImplement},
			transcript: []server.TranscriptMessageDTO{
				{Index: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "Preparing patch"},
				{Index: 2, Role: roleAssistant, Type: testMessageTypeToolUse, Tool: toolNameBash, Redacted: true},
				{Index: 3, Role: roleAssistant, Type: testMessageTypeToolUse, Tool: toolNameAskUserQuestion, Redacted: true},
			},
			want:    []string{"Preparing patch", testPromptBashPrefix, "? AskUser:"},
			notWant: []string{"> Preparing patch", "> Bash", "> AskUserQuestion"},
		},
		{
			name:         transcriptTypeToolProgress,
			sessionExtra: server.SessionSummaryDTO{Label: testActivityImplement, Provider: testProviderCodex},
			transcript: []server.TranscriptMessageDTO{
				{Index: 1, Role: testMessageRoleSystem, Type: transcriptTypeToolProgress, Tool: toolNameBash, Redacted: true},
				{Index: 2, Role: roleAssistant, Type: testMessageTypeText, Text: "Continuing after tool use"},
			},
			want:    []string{testPromptBashPrefix, "Continuing after tool use"},
			notWant: []string{"> Bash", "> Continuing after tool use"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			session := tt.sessionExtra
			session.ID = testSessionIDLive
			session.FeatureID = testFeatureIDActive
			session.Phase = testPhaseNameImplement
			session.Status = featureStatusTokenRunning
			client := &fakeTUIAPIClient{
				features: server.FeatureListResponse{Features: []server.FeatureSummary{
					{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
				}},
				livePreview: server.LivePreviewResponse{
					Feature:    server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement},
					Session:    &session,
					Transcript: tt.transcript,
				},
			}
			app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
			if err != nil {
				t.Fatalf("NewAPIAppModel() error = %v", err)
			}

			view := stripANSI(app.View().Content)
			for _, want := range tt.want {
				if !strings.Contains(view, want) {
					t.Fatalf("API live preview missing transcript row %q in:\n%s", want, view)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(view, notWant) {
					t.Fatalf("API live preview rendered row with assistant glyph %q in:\n%s", notWant, view)
				}
			}
		})
	}
}

func TestAPITranscriptRowToSDKMessagePreservesToolProgress(t *testing.T) {
	t.Parallel()

	msg, ok := apiTranscriptRowToSDKMessage(server.TranscriptMessageDTO{
		Index:    1,
		Role:     testMessageRoleSystem,
		Type:     transcriptTypeToolProgress,
		Tool:     toolNameBash,
		Redacted: true,
	}, testSessionIDLive)
	if !ok {
		t.Fatal("apiTranscriptRowToSDKMessage(tool_progress) returned !ok")
	}
	if msg.ToolProgress == nil {
		t.Fatalf("apiTranscriptRowToSDKMessage(tool_progress) = %+v, want ToolProgress message", msg)
	}
	if msg.Assistant != nil {
		t.Fatalf("tool_progress row should not reconstruct as assistant tool use: %+v", msg.Assistant)
	}
	if msg.ToolProgress.ToolName != toolNameBash || msg.ToolProgress.SessionID != testSessionIDLive {
		t.Fatalf("ToolProgress = %+v, want Bash in sess-live", msg.ToolProgress)
	}
}

func TestAPITranscriptRowKeyIncludesBlockIndex(t *testing.T) {
	t.Parallel()

	first := server.TranscriptMessageDTO{Index: 1, BlockIndex: 0, Role: roleAssistant, Type: testMessageTypeText, Text: "first section"}
	second := server.TranscriptMessageDTO{Index: 1, BlockIndex: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "second section"}

	if apiTranscriptRowKey(first) == apiTranscriptRowKey(second) {
		t.Fatalf("same-index transcript text rows produced identical keys: %q", apiTranscriptRowKey(first))
	}
}

func TestAPITranscriptRowToSDKMessagePreservesAutoPickedUserEcho(t *testing.T) {
	t.Parallel()

	msg, ok := apiTranscriptRowToSDKMessage(server.TranscriptMessageDTO{
		Index:              1,
		Role:               testMessageRoleUser,
		Type:               testMessageTypeText,
		Text:               "Translate `README.md` in place (Recommended)",
		LocallyAppended:    true,
		AutoPicked:         true,
		AutoPickQuestion:   "Which output shape?",
		AutoPickConfidence: 0.72,
	}, testSessionIDLive)
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

func TestAPILivePreviewSessionCarriesFieldsFromREST(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		session    server.SessionSummaryDTO
		transcript []server.TranscriptMessageDTO
		check      func(t *testing.T, sess ports.SessionView)
	}{
		{
			name: "provider and model",
			session: server.SessionSummaryDTO{
				ID:       testSessionIDLive,
				Phase:    testPhaseNameImplement,
				Kind:     testSessionKindAgent,
				Provider: testProviderCodex,
				Model:    "gpt-5-codex",
			},
			transcript: []server.TranscriptMessageDTO{
				{Index: 1, Role: testMessageRoleSystem, Type: transcriptTypeToolProgress, Tool: toolNameBash, Redacted: true},
			},
			check: func(t *testing.T, sess ports.SessionView) {
				if got := sess.ProviderName(); got != testProviderCodex {
					t.Fatalf("ProviderName() = %q, want codex", got)
				}
				if got := sess.Model(); got != "gpt-5-codex" {
					t.Fatalf("Model() = %q, want gpt-5-codex", got)
				}
			},
		},
		{
			name: "phase",
			session: server.SessionSummaryDTO{
				ID:     testSessionIDLive,
				Phase:  testPhaseNameFinalReview,
				Status: featureStatusTokenRunning,
			},
			check: func(t *testing.T, sess ports.SessionView) {
				if got := sess.Phase(); got != feature.PhaseFinalReview {
					t.Fatalf("Phase() = %s, want Final Review", got)
				}
			},
		},
		{
			name: "done status",
			session: server.SessionSummaryDTO{
				ID:     testSessionIDLive,
				Status: ports.SessionDone.String(),
			},
			check: func(t *testing.T, sess ports.SessionView) {
				if got := sess.Status(); got != ports.SessionDone {
					t.Fatalf("Status() = %s, want Done", got)
				}
				if sess.IsActive() {
					t.Fatal("IsActive() = true for completed live-preview session")
				}
			},
		},
		{
			name: "kind and label",
			session: server.SessionSummaryDTO{
				ID:     "scope-validator",
				Phase:  testArtifactIDPlan,
				Kind:   testSessionKindValidator,
				Label:  testValidatorNameScope,
				Status: featureStatusTokenRunning,
			},
			check: func(t *testing.T, sess ports.SessionView) {
				if got := sess.Kind().String(); got != testSessionKindValidator {
					t.Fatalf("Kind() = %q, want validator", got)
				}
				if got := sess.Label(); got != testValidatorNameScope {
					t.Fatalf("Label() = %q, want Scope", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			presentation := apiLivePreviewPresentation(testFeatureIDActive, server.LivePreviewResponse{
				Session:    &tt.session,
				Transcript: tt.transcript,
			})
			sess := newAPILivePreviewSession(presentation)
			if sess == nil {
				t.Fatal("newAPILivePreviewSession returned nil")
			}
			tt.check(t, sess)
		})
	}
}

func TestAPILivePreviewSessionWithoutSessionSummaryIsSnapshot(t *testing.T) {
	t.Parallel()

	sess := newAPILivePreviewSession(APILivePreviewPresentation{
		FeatureID: testFeatureIDActive,
		Phase:     testPhaseNameImplement,
		Activity:  testUsingBashActivity,
	})
	if sess == nil {
		t.Fatal("newAPILivePreviewSession returned nil")
	}
	if got := sess.Status(); got != ports.SessionDone {
		t.Fatalf("Status() = %s, want Done for preview-only snapshot", got)
	}
	if sess.IsActive() {
		t.Fatal("IsActive() = true for preview-only snapshot")
	}
}

func TestAPIAppModelDashboardFeatureCarriesValidationReviewGateFromREST(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           testFeatureIDActive,
		Name:         "Validate roadmap",
		Slug:         "validate-roadmap",
		Status:       testFeatureStatusPlanning,
		CurrentPhase: testArtifactIDPlan,
		Progress: server.FeatureProgress{
			CurrentRoadmapPhase: 1,
			TotalRoadmapPhases:  3,
		},
	}
	detail := apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{

		ActiveRunDetail: &server.RunSummaryDTO{RunNumber: 1, CurrentPhase: testArtifactIDPlan, RoadmapPhase: 1, RoadmapTotal: 3},
		ReviewGate: server.ReviewGateDTO{
			ValidatingPlan: true,
			ValidatorStatuses: map[string]string{
				"Architecture":           "APPROVED",
				testValidatorNameScope:   "CHANGES_REQUESTED",
				testValidatorNameTesting: featureStatusTokenRunning,
			},
		},
	})
	f := (APIAppModel{}).apiDashboardFeature(summary, detail, true)
	if !f.ValidatingPlan {
		t.Fatal("ValidatingPlan = false, want true")
	}
	if got := f.ValidatorStatuses[testValidatorNameScope]; got != "CHANGES_REQUESTED" {
		t.Fatalf("ValidatorStatuses[Scope] = %q, want CHANGES_REQUESTED", got)
	}

	sess := validatorLivePreviewSession("scope-validator", testValidatorNameScope)
	view := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(120))
	for _, want := range []string{
		testColumnLabelStatus, "Validating Phase 1 plan",
		"Validators", "Arch ✓", "Test ⟳", "Scope ✗",
		"Current: Validating Phase 1 plan", "1 ✓", "1 ✗", "1 running", "Showing Scope",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("REST validation live preview missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelDashboardFeatureCarriesImplementationReviewGateFromREST(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           testFeatureIDActive,
		Name:         "Review implementation",
		Slug:         "review-implementation",
		Status:       testFeatureStatusImplementing,
		CurrentPhase: testPhaseNameImplement,
		Progress: server.FeatureProgress{
			CurrentRoadmapPhase: 1,
			TotalRoadmapPhases:  11,
		},
	}
	detail := apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{
		ActiveRunDetail: &server.RunSummaryDTO{
			RunNumber:    5,
			CurrentPhase: testPhaseNameImplement,
			Iteration:    3,
			RoadmapPhase: 1,
			RoadmapTotal: 11,
		},
		ReviewGate: server.ReviewGateDTO{
			ReviewingGate: true,
			ReviewFixing:  true,
			ValidatorStatuses: map[string]string{
				"Craft":                  featureStatusTokenRunning,
				"Functionality/Evidence": featureStatusTokenRunning,
				"Cleanliness":            featureStatusTokenRunning,
				"Design":                 featureStatusTokenRunning,
			},
		},
	})

	f := (APIAppModel{}).apiDashboardFeature(summary, detail, true)
	if !f.ReviewingGate {
		t.Fatal("ReviewingGate = false, want true")
	}
	if !f.ReviewFixing {
		t.Fatal("ReviewFixing = false, want true")
	}

	leftPanelStatus := stripANSI(formatStatus(f))
	if !strings.Contains(leftPanelStatus, "Reviewing [3]") {
		t.Fatalf("left-panel status = %q, want active review wording", leftPanelStatus)
	}

	overviewStatus := stripANSI(formatPhaseStatus(f))
	for _, want := range []string{"reviewing:", "Craft ⟳", "Func ⟳", "Clean ⟳", "Design ⟳"} {
		if !strings.Contains(overviewStatus, want) {
			t.Fatalf("Overview phase status missing %q in %q", want, overviewStatus)
		}
	}
}

func TestAPIAppModelTranscriptLoadsSelectedSessionFromREST(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		livePreview: server.LivePreviewResponse{
			Feature: server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing},
			Session: &server.SessionSummaryDTO{ID: testSessionIDLive, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Label: testActivityImplement, Status: featureStatusTokenRunning},
		},
		sessionDetail: server.SessionDetailResponse{Session: apiTestSessionDetailWith(server.SessionSummaryDTO{ID: testSessionIDLive, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Label: testActivityImplement, Status: featureStatusTokenRunning}, server.SessionDetailDTO{

			TranscriptCursor: server.CursorDTO{Total: 64, Start: 0, End: 64},
		})},
		transcript: server.TranscriptResponse{
			Cursor: server.CursorDTO{Total: 64, Start: 14, End: 64},
			Messages: []server.TranscriptMessageDTO{
				{Index: 62, Role: roleAssistant, Type: testMessageTypeText, Text: "Patch transcript continuation"},
				{Index: 63, Role: testMessageRoleSystem, Type: testMessageTypeToolUse, Tool: toolNameBash, Redacted: true},
			},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	if got := strings.Join(client.sessionDetailIDs, ","); got != testSessionIDLive {
		t.Fatalf("SessionDetail calls = %q, want sess-live", got)
	}
	if got := strings.Join(client.transcriptSessionIDs, ","); got != testSessionIDLive {
		t.Fatalf("Transcript calls = %q, want sess-live", got)
	}
	if len(client.transcriptQueries) != 1 {
		t.Fatalf("Transcript query count = %d, want 1", len(client.transcriptQueries))
	}
	if got := client.transcriptQueries[0]; got.Cursor != 14 || got.Limit != 50 {
		t.Fatalf("Transcript query = %+v, want cursor 14 limit 50", got)
	}
	snapshot := app.snapshot
	if snapshot.Transcript == nil || snapshot.Transcript.SessionID != testSessionIDLive {
		t.Fatalf("Snapshot().Transcript = %+v, want sess-live transcript", snapshot.Transcript)
	}
	if got := strings.Join(snapshot.Transcript.Lines, "\n"); !strings.Contains(got, "Patch transcript continuation") || !strings.Contains(got, toolNameBash) {
		t.Fatalf("Snapshot().Transcript lines = %q, want transcript continuation and Bash", got)
	}
}

func TestAPIAppModelLoadsSelectedRunContentFromREST(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing}, server.FeatureDetailDTO{

			ActiveRunDetail: &server.RunSummaryDTO{RunNumber: 3, CurrentPhase: testPhaseNameImplement, ArtifactCount: 1},
		})},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: testArtifactIDPlan, RunNumber: 3, Phase: testArtifactIDPlan, Size: apiContentTailLimit + 14, ContentAvailable: true},
		}},
		logContent: server.TextContentResponse{
			ID:     testResourceIDSession,
			Offset: 0,
			Limit:  apiContentTailLimit,
			Size:   apiContentTailLimit + 80,
			Text:   "log tail from server",
		},
		artifactContent: server.TextContentResponse{
			ID:     testArtifactIDPlan,
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

	if got := strings.Join(client.logContentIDs, ","); got != testResourceIDSession {
		t.Fatalf("LogContent IDs = %q, want session", got)
	}
	if got := client.logContentQueries[0]; got.Offset != 0 || got.Limit != apiContentTailLimit {
		t.Fatalf("LogContent query = %+v, want offset 0 limit %d", got, apiContentTailLimit)
	}
	if got := strings.Join(client.artifactListFeatureIDs, ","); got != testFeatureIDActive {
		t.Fatalf("ArtifactList feature IDs = %q, want active", got)
	}
	if got := strings.Join(client.artifactContentIDs, ","); got != testArtifactIDPlan {
		t.Fatalf("ArtifactContent IDs = %q, want plan", got)
	}
	if got := client.artifactContentQueries[0]; got.Offset != 14 || got.Limit != apiContentTailLimit {
		t.Fatalf("ArtifactContent query = %+v, want offset 14 limit %d", got, apiContentTailLimit)
	}
	snapshot := app.snapshot
	if snapshot.Content == nil || snapshot.Content.RunNumber != 3 {
		t.Fatalf("Snapshot().Content = %+v, want run 3 content", snapshot.Content)
	}
	view := stripANSI(app.View().Content)
	if strings.Contains(view, testLabelRunContent) {
		t.Fatalf("API app View() showed run content before opening the content panel:\n%s", view)
	}
	app.contentPanelActive = true
	view = stripANSI(app.View().Content)
	for _, want := range []string{testLabelRunContent, "Log session", "log tail from server", "Artifact plan", "artifact tail from server"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelContentKeysCycleArtifactsAndLogsThroughREST(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing}, server.FeatureDetailDTO{

			ActiveRunDetail: &server.RunSummaryDTO{RunNumber: 5, CurrentPhase: testPhaseNameImplement, ArtifactCount: 2},
		})},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: testArtifactIDPlan, RunNumber: 5, Phase: testArtifactIDPlan, Size: apiContentTailLimit + 10, ContentAvailable: true},
			{ID: testArtifactIDDesign, RunNumber: 5, Phase: testArtifactIDDesign, Size: apiContentTailLimit + 20, ContentAvailable: true},
		}},
		artifactContentByID: map[string]server.TextContentResponse{
			testArtifactIDPlan:   {ID: testArtifactIDPlan, Offset: 10, Limit: apiContentTailLimit, Size: apiContentTailLimit + 10, Text: "plan tail from server"},
			testArtifactIDDesign: {ID: testArtifactIDDesign, Offset: 20, Limit: apiContentTailLimit, Size: apiContentTailLimit + 20, Text: testDesignTailFromServer},
		},
		logContentByID: map[string]server.TextContentResponse{
			testResourceIDSession: {ID: testResourceIDSession, Offset: 0, Limit: apiContentTailLimit, Size: 25, Text: "session log from server"},
			logTabPhase:           {ID: logTabPhase, Offset: 0, Limit: apiContentTailLimit, Size: 20, Text: testPhaseLogFromServer},
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
	if !strings.Contains(view, "Artifact design") || !strings.Contains(view, testDesignTailFromServer) {
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
	for _, want := range []string{"Log phase", testPhaseLogFromServer, "Artifact design", testDesignTailFromServer} {
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
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing}, server.FeatureDetailDTO{

			ActiveRunDetail: &server.RunSummaryDTO{RunNumber: 7, CurrentPhase: testPhaseNameImplement, ArtifactCount: 1},
		})},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: testArtifactIDPlan, RunNumber: 7, Phase: testArtifactIDPlan, Size: 16, ContentAvailable: true},
		}},
		logContent: server.TextContentResponse{
			ID:     testResourceIDSession,
			Offset: 0,
			Limit:  apiContentTailLimit,
			Size:   21,
			Text:   "content view log tail",
		},
		artifactContent: server.TextContentResponse{
			ID:     testArtifactIDPlan,
			Offset: 0,
			Limit:  apiContentTailLimit,
			Size:   16,
			Text:   testContentArtifactText,
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

	for _, want := range []string{testLabelRunContent, "content view log tail", testContentArtifactText, "Next log"} {
		if !strings.Contains(view, want) {
			t.Fatalf("content view missing %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{dashboardFeaturesPanelTitle, testFeatureNameClientCutover, testSectionLabelInProgress} {
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
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing}, server.FeatureDetailDTO{

			ActiveRunDetail: &server.RunSummaryDTO{RunNumber: 7, CurrentPhase: testPhaseNameImplement, ArtifactCount: 1},
		})},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: testArtifactIDPlan, RunNumber: 7, Phase: testArtifactIDPlan, Size: 16, ContentAvailable: true},
		}},
		artifactContent: server.TextContentResponse{
			ID:     testArtifactIDPlan,
			Offset: 0,
			Limit:  apiContentTailLimit,
			Size:   16,
			Text:   testContentArtifactText,
		},
		logContentErrByID: map[string]error{
			testResourceIDSession: notFoundLogErr(testResourceIDSession),
			logTabPhase:           notFoundLogErr(logTabPhase),
			"observe":             notFoundLogErr("observe"),
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
	if !strings.Contains(view, testContentArtifactText) {
		t.Fatalf("content view lost artifact after missing logs:\n%s", view)
	}
}

func TestAPIAppModelContentRefreshPreservesSelectedArtifactAndLog(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing}, server.FeatureDetailDTO{

			ActiveRunDetail: &server.RunSummaryDTO{RunNumber: 6, CurrentPhase: testPhaseNameImplement, ArtifactCount: 2},
		})},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: testArtifactIDPlan, RunNumber: 6, Phase: testArtifactIDPlan, Size: apiContentTailLimit + 10, ContentAvailable: true},
			{ID: testArtifactIDDesign, RunNumber: 6, Phase: testArtifactIDDesign, Size: apiContentTailLimit + 20, ContentAvailable: true},
		}},
		artifactContentByID: map[string]server.TextContentResponse{
			testArtifactIDPlan:   {ID: testArtifactIDPlan, Offset: 10, Limit: apiContentTailLimit, Size: apiContentTailLimit + 10, Text: "plan tail from server"},
			testArtifactIDDesign: {ID: testArtifactIDDesign, Offset: 20, Limit: apiContentTailLimit, Size: apiContentTailLimit + 20, Text: testDesignTailFromServer},
		},
		logContentByID: map[string]server.TextContentResponse{
			testResourceIDSession: {ID: testResourceIDSession, Offset: 0, Limit: apiContentTailLimit, Size: apiContentTailLimit + 50, Text: "session log from server"},
			logTabPhase:           {ID: logTabPhase, Offset: 0, Limit: apiContentTailLimit, Size: apiContentTailLimit + 60, Text: testPhaseLogFromServer},
		},
		refreshSnapshot: server.RefreshSnapshot{
			Session: &server.SessionDetailResponse{Session: apiTestSessionDetail(
				server.SessionSummaryDTO{ID: testSessionIDLive, FeatureID: testFeatureIDActive, Status: featureStatusTokenRunning})},
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

	client.artifactContentByID[testArtifactIDDesign] = server.TextContentResponse{
		ID:     testArtifactIDDesign,
		Offset: 20,
		Limit:  apiContentTailLimit,
		Size:   apiContentTailLimit + 20,
		Text:   "refreshed design tail from server",
	}
	client.logContentByID[logTabPhase] = server.TextContentResponse{
		ID:     logTabPhase,
		Offset: 60,
		Limit:  apiContentTailLimit,
		Size:   apiContentTailLimit + 90,
		Text:   "refreshed phase log from server",
	}
	signal := server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: "log.resource.updated"},
		Resource: server.ResourceDTO{Type: testResourceTypeLog, ID: logTabPhase, FeatureID: testFeatureIDActive},
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
	if got := client.artifactContentQueries[2]; got.Offset != 20 || got.Limit != apiContentTailLimit {
		t.Fatalf("refresh ArtifactContent query = %+v, want offset 20 limit %d", got, apiContentTailLimit)
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
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		livePreview: server.LivePreviewResponse{
			Feature:    server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing},
			Activity:   "Thinking...",
			Session:    &server.SessionSummaryDTO{ID: testSessionIDLive, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Status: featureStatusTokenRunning},
			Context:    server.ContextDTO{Percentage: 11},
			Transcript: []server.TranscriptMessageDTO{{Index: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "Initial tail"}},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	initialDetailCalls := len(client.detailFeatureIDs)
	initialTranscriptCalls := countString(client.calls, "Transcript")

	signal := server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: testEventKindSessionUpdated},
		Resource: server.ResourceDTO{Type: testResourceIDSession, ID: testSessionIDLive, FeatureID: testFeatureIDActive},
	}
	client.refreshSnapshot = server.RefreshSnapshot{
		LivePreview: &server.LivePreviewResponse{
			Feature:  server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing},
			Activity: testUsingBashActivity,
			Session:  &server.SessionSummaryDTO{ID: testSessionIDLive, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Status: featureStatusTokenRunning},
			Context:  server.ContextDTO{Percentage: 57},
			Cost:     server.CostDTO{TotalUSD: 1.25},
			Transcript: []server.TranscriptMessageDTO{
				{Index: 2, Role: roleAssistant, Type: testMessageTypeText, Text: "Patched through REST snapshot"},
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
	for _, want := range []string{testLabelLivePreview, testUsingBashActivity, "57%", "$1.25", "Initial tail", "Patched through REST snapshot"} {
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
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		livePreview: server.LivePreviewResponse{
			Feature:    server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing},
			Activity:   "Thinking...",
			Session:    &server.SessionSummaryDTO{ID: "sess-old", FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Status: featureStatusTokenRunning},
			Transcript: []server.TranscriptMessageDTO{{Index: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "Old session tail"}},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}

	client.refreshSnapshot = server.RefreshSnapshot{
		LivePreview: &server.LivePreviewResponse{
			Feature:  server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing},
			Activity: testUsingBashActivity,
			Session:  &server.SessionSummaryDTO{ID: "sess-new", FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Status: featureStatusTokenRunning},
			Transcript: []server.TranscriptMessageDTO{
				{Index: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "New session tail"},
			},
		},
	}
	signal := server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: testEventKindSessionUpdated},
		Resource: server.ResourceDTO{Type: testResourceIDSession, ID: "sess-new", FeatureID: testFeatureIDActive},
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
	app.storeLivePreview(testFeatureIDActive, server.LivePreviewResponse{
		Feature:  server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusInterrupted},
		Activity: "Stopped",
		Session:  &server.SessionSummaryDTO{ID: "active-impl", FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Status: testFeatureIDDone, StartedAt: oldStartedAt},
		Transcript: []server.TranscriptMessageDTO{
			{Index: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "Old session first row"},
			{Index: 2, Role: roleAssistant, Type: testMessageTypeText, Text: "Old session tail"},
		},
	})
	app.storeLivePreview(testFeatureIDActive, server.LivePreviewResponse{
		Feature:  server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing},
		Activity: testUsingBashActivity,
		Session:  &server.SessionSummaryDTO{ID: "active-impl", FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Status: featureStatusTokenRunning, StartedAt: newStartedAt},
		Transcript: []server.TranscriptMessageDTO{
			{Index: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "New restarted session tail"},
		},
	})

	got := app.livePreviews[testFeatureIDActive].Transcript
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
		{Index: 10, Role: roleAssistant, Type: testMessageTypeText, Text: "I have enough context. Let me now write the research questions file."},
		{Index: 11, Role: roleAssistant, Type: testMessageTypeToolUse, Tool: toolNameBash, Redacted: true},
		{Index: 12, Role: testMessageRoleSystem, Type: "control_request", Tool: toolNameAskUserQuestion, Status: testStatusPending, Redacted: true},
	}
	incoming := []server.TranscriptMessageDTO{
		{Index: 20, Role: roleAssistant, Type: testMessageTypeText, Text: "I have enough context. Let me now write the research questions file."},
		{Index: 21, Role: roleAssistant, Type: testMessageTypeToolUse, Tool: toolNameBash, Redacted: true},
		{Index: 22, Role: testMessageRoleSystem, Type: "control_request", Tool: toolNameAskUserQuestion, Status: testStatusPending, Redacted: true},
		{Index: 23, Role: roleAssistant, Type: testMessageTypeToolUse, Tool: toolNameRead, Redacted: true},
	}

	merged := mergeLivePreviewTranscript(existing, incoming)
	if got, want := len(merged), 4; got != want {
		t.Fatalf("merged transcript len = %d, want %d: %+v", got, want, merged)
	}
	if got, want := merged[0].Text, existing[0].Text; got != want {
		t.Fatalf("merged[0].Text = %q, want %q", got, want)
	}
	if got, want := merged[1].Tool, toolNameBash; got != want {
		t.Fatalf("merged[1].Tool = %q, want %q", got, want)
	}
	if got, want := merged[2].Tool, toolNameAskUserQuestion; got != want {
		t.Fatalf("merged[2].Tool = %q, want %q", got, want)
	}
	if got, want := merged[3].Tool, toolNameRead; got != want {
		t.Fatalf("merged[3].Tool = %q, want %q", got, want)
	}
}

func TestMergeLivePreviewTranscriptReplacesUpdatedStreamingRow(t *testing.T) {
	t.Parallel()

	existing := []server.TranscriptMessageDTO{
		{Index: 1, Role: testMessageRoleSystem, Type: transcriptTypeToolProgress, Tool: toolNameBash, Redacted: true},
		{Index: 2, Role: roleAssistant, Type: testMessageTypeText, Text: "I'm using the inquire workflow now; I'll keep this"},
		{Index: 3, Role: testMessageRoleSystem, Type: transcriptTypeToolProgress, Tool: toolNameBash, Redacted: true},
	}
	incoming := []server.TranscriptMessageDTO{
		{Index: 1, Role: testMessageRoleSystem, Type: transcriptTypeToolProgress, Tool: toolNameBash, Redacted: true},
		{Index: 2, Role: roleAssistant, Type: testMessageTypeText, Text: "I'm using the inquire workflow now; I'll keep this to requirements-level clarification."},
		{Index: 3, Role: testMessageRoleSystem, Type: transcriptTypeToolProgress, Tool: toolNameBash, Redacted: true},
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

func TestAPIAppStoreTranscriptMergesFirstPageBackfillWithExistingTail(t *testing.T) {
	t.Parallel()

	app := APIAppModel{}
	app.storeTranscript(testSessionIDOne, server.TranscriptResponse{
		Cursor:   server.CursorDTO{Total: 100, Start: 50, End: 100},
		Messages: apiTestTranscriptRows(50, 100),
	})
	app.storeTranscript(testSessionIDOne, server.TranscriptResponse{
		Cursor:   server.CursorDTO{Total: 100, Start: 0, End: 50},
		Messages: apiTestTranscriptRows(0, 50),
	})

	got := app.transcripts[testSessionIDOne]
	if got.Cursor.Start != 0 || got.Cursor.End != 100 || got.Cursor.Total != 100 {
		t.Fatalf("merged cursor = %+v, want total 100 start 0 end 100", got.Cursor)
	}
	if len(got.Messages) != 100 {
		t.Fatalf("merged transcript len = %d, want 100", len(got.Messages))
	}
	if got.Messages[0].Text != "transcript row 000" || got.Messages[99].Text != "transcript row 099" {
		t.Fatalf("merged transcript endpoints = %q / %q", got.Messages[0].Text, got.Messages[99].Text)
	}
}

func TestAPIAppModelAppliesResourceTargetedRefreshSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CreatedAt: time.Now()},
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
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: time.Now()},
			{ID: testFeatureIDNew, Name: "New API feature", Slug: "new-api-feature", Status: testFeatureStatusCreated, CurrentPhase: testPhaseKeyResearch, CreatedAt: time.Now().Add(time.Second)},
		}},
		Prompts: &server.PromptSnapshotResponse{NeedUserInputs: []server.NeedInputGateDTO{
			{FeatureID: testFeatureIDNew, Open: true, Scope: testActionScopeFeature},
		}},
	})

	if got := app.selectedFeature; got != testFeatureIDActive {
		t.Fatalf("SelectedFeatureID() after refresh = %q, want active", got)
	}
	snapshot := app.snapshot
	var newFeature APIFeaturePresentation
	for _, f := range snapshot.Features {
		if f.ID == testFeatureIDNew {
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
			{ID: testFeatureIDStopped, Name: "Stopped work", Slug: "stopped-work", Status: testFeatureStatusInterrupted, CurrentPhase: testArtifactIDDesign, CreatedAt: time.Now()},
		}},
		prompts: server.PromptSnapshotResponse{
			AskUserQuestions: []server.ControlRequestDTO{
				{FeatureID: testFeatureIDStopped, RequestID: testAskRequestID, Status: testStatusPending, ToolName: toolNameAskUserQuestion, Summary: "Which path?"},
			},
			HelpQueue: []server.HelpQueueDTO{
				{FeatureID: testFeatureIDStopped, Question: "Need input?", Pending: true},
			},
		},
		permissions: server.PermissionSnapshotResponse{Requests: []server.ControlRequestDTO{
			{FeatureID: testFeatureIDStopped, RequestID: testPermissionRequestIDPerm1, Status: testStatusPending, ToolName: toolNameBash, Summary: testShellCommandGoTest},
		}},
	}
	app := newTestAPIAppModel(t, client)

	snapshot := app.snapshot
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
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
			{ID: testFeatureIDQueued, Name: testFeatureNameQueuedWork, Slug: testFeatureSlugQueuedWork, Status: testFeatureStatusCreated, CurrentPhase: testPhaseKeyResearch, CreatedAt: time.Now().Add(-time.Hour)},
		}},
		runtime:     server.RuntimeConfigResponse{Providers: []string{testProviderCodex}},
		catalog:     server.ModelCatalogResponse{},
		prompts:     server.PromptSnapshotResponse{},
		permissions: server.PermissionSnapshotResponse{},
		refreshSnapshot: server.RefreshSnapshot{
			Feature: &server.FeatureDetailResponse{Feature: apiTestFeatureDetail(
				server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: time.Now()})},
		},
	}
	app, err := NewAPIAppModel(ctx, client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	if got := app.selectedFeature; got != testFeatureIDActive {
		t.Fatalf("initial SelectedFeatureID() = %q, want active", got)
	}
	initialSessionCalls := countString(client.calls, "Sessions")
	initialTranscriptCalls := countString(client.calls, "Transcript")

	signal := server.RefreshSignal{
		Resource:         server.ResourceDTO{Type: testActionScopeFeature, ID: testFeatureIDActive, FeatureID: testFeatureIDActive},
		SnapshotRequired: true,
	}
	msg := app.fetchRefreshSnapshotCmd(signal)()
	model, _ := app.Update(msg)
	recovered := model.(APIAppModel)

	if got := recovered.selectedFeature; got != testFeatureIDActive {
		t.Fatalf("SelectedFeatureID() after reconnect snapshot = %q, want active", got)
	}
	if len(client.refreshSignals) != 1 || client.refreshSignals[0].Resource.ID != testFeatureIDActive {
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
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		runtime:     server.RuntimeConfigResponse{Providers: []string{testProviderCodex}},
		catalog:     server.ModelCatalogResponse{},
		prompts:     server.PromptSnapshotResponse{},
		permissions: server.PermissionSnapshotResponse{},
		recovery:    server.RecoverySnapshotResponse{},
		refreshSnapshot: server.RefreshSnapshot{
			Recovery: &server.RecoverySnapshotResponse{
				SnapshotID: "recovery-snapshot-2",
				Items: []server.RecoveryItemDTO{{
					Key:            "feat-1:api",
					FeatureID:      testFeatureIDActive,
					FeatureName:    testFeatureNameClientCutover,
					RepoName:       testRepoNameAPI,
					Phase:          testPhaseNameImplement,
					Iteration:      8,
					PID:            4321,
					ProcessAlive:   true,
					DefaultAction:  testActionKill,
					AllowedActions: []string{testActionKill, testActionSkip},
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
	for _, want := range []string{"Session Recovery", testFeatureNameClientCutover, "PID 4321", "[K]ill"} {
		if !strings.Contains(view, want) {
			t.Fatalf("refreshed API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelStartOrResumeSelectedFeatureUsesRESTMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		featureID     string
		buildClient   func() *fakeTUIAPIClient
		wantCompleted string
		checkCalls    func(t *testing.T, c *fakeTUIAPIClient)
	}{
		{
			name:      "start",
			featureID: testFeatureIDQueued,
			buildClient: func() *fakeTUIAPIClient {
				return &fakeTUIAPIClient{
					features: server.FeatureListResponse{Features: []server.FeatureSummary{
						{ID: testFeatureIDQueued, Name: testFeatureNameQueuedWork, Slug: testFeatureSlugQueuedWork, Status: testFeatureStatusCreated, CurrentPhase: testPhaseKeyResearch, CreatedAt: time.Now()},
					}},
					startAccepted: apiTestActionResponse{},
				}
			},
			wantCompleted: "Completed Start",
			checkCalls: func(t *testing.T, c *fakeTUIAPIClient) {
				if got := strings.Join(c.startFeatureIDs, ","); got != testFeatureIDQueued {
					t.Fatalf("StartFeature calls = %q, want queued", got)
				}
			},
		},
		{
			name:      "resume",
			featureID: testFeatureIDPaused,
			buildClient: func() *fakeTUIAPIClient {
				return &fakeTUIAPIClient{
					features: server.FeatureListResponse{Features: []server.FeatureSummary{
						{ID: testFeatureIDPaused, Name: testFeatureNamePausedWork, Slug: testFeatureSlugPausedWork, Status: testFeatureStatusInterrupted, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
					}},
					detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDPaused, Name: testFeatureNamePausedWork, Slug: testFeatureSlugPausedWork, Status: testFeatureStatusInterrupted, CurrentPhase: testPhaseNameImplement}, server.FeatureDetailDTO{

						Actions: []server.ActionDTO{
							{ID: recoveryActionResume, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
						},
					})},
					resumeAccepted: apiTestActionResponse{},
				}
			},
			wantCompleted: "Completed Resume",
			checkCalls: func(t *testing.T, c *fakeTUIAPIClient) {
				if got := strings.Join(c.resumeFeatureIDs, ","); got != testFeatureIDPaused {
					t.Fatalf("ResumeFeature calls = %q, want paused", got)
				}
				if len(c.startFeatureIDs) != 0 {
					t.Fatalf("StartFeature calls = %v, want none for resume action", c.startFeatureIDs)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := tt.buildClient()
			app := newTestAPIAppModel(t, client)

			model, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyRight})
			model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if cmd != nil {
				t.Fatal("Update(enter) returned command, want focus-only behavior")
			}
			model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
			if cmd == nil {
				t.Fatal("Update(a) returned nil command, want mutation command")
			}
			msg := cmd()
			model, _ = model.(APIAppModel).Update(msg)
			updated := model.(APIAppModel)

			tt.checkCalls(t, client)
			view := stripANSI(updated.View().Content)
			for _, want := range []string{tt.wantCompleted, tt.featureID} {
				if !strings.Contains(view, want) {
					t.Fatalf("API app View() missing %q in:\n%s", want, view)
				}
			}
		})
	}
}

func TestAPIAppModelContextualRetryUsesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testStatusFailed, Name: testFeatureNameFailedWork, Slug: testFeatureSlugFailedWork, Status: testFeatureStatusFailed, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		retryAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want retry mutation command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	retried := model.(APIAppModel)

	if got := strings.Join(client.retryFeatureIDs, ","); got != testStatusFailed {
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
			{ID: testStatusFailed, Name: testFeatureNameFailedWork, Slug: testFeatureSlugFailedWork, Status: testFeatureStatusFailed, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		retryAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	model, cmd := model.(APIAppModel).Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) in detail returned nil command, want retry mutation")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	retried := model.(APIAppModel)

	if got := strings.Join(client.retryFeatureIDs, ","); got != testStatusFailed {
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

	paused := server.FeatureSummary{ID: testFeatureIDPaused, Name: testFeatureNamePausedWork, Slug: testFeatureSlugPausedWork, Status: testFeatureStatusInterrupted, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()}
	failed := server.FeatureSummary{ID: testStatusFailed, Name: testFeatureNameFailedWork, Slug: testFeatureSlugFailedWork, Status: testFeatureStatusFailed, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now().Add(-time.Minute)}
	resumeAction := server.ActionDTO{ID: recoveryActionResume, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			paused,
			failed,
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(paused, server.FeatureDetailDTO{
			Actions: []server.ActionDTO{resumeAction},
		})},
		detailsByID: map[string]server.FeatureDetailResponse{
			testStatusFailed: {Feature: apiTestFeatureDetailWith(failed, server.FeatureDetailDTO{
				Actions: []server.ActionDTO{resumeAction},
			})},
		},
		resumeAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	focused := model.(APIAppModel)
	if focused.focusPanel != 1 {
		t.Fatalf("focusPanel after right = %d, want detail focus", focused.focusPanel)
	}
	model, cmd := focused.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	if cmd == nil {
		t.Fatal("Update(Shift+R) returned nil command, want action catalogs loaded")
	}
	model, _ = model.(APIAppModel).Update(cmd())
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

	if got := strings.Join(client.resumeFeatureIDs, ","); got != testFeatureIDPaused+","+testStatusFailed {
		t.Fatalf("ResumeFeature calls = %q, want paused,failed", got)
	}
	if got := strings.Join(client.retryFeatureIDs, ","); got != "" {
		t.Fatalf("RetryFeature calls = %q, want none", got)
	}
	if view := stripANSI(resumed.View().Content); !strings.Contains(view, "Resumed 2 feature(s)") {
		t.Fatalf("API app View() missing resume-all completed status in:\n%s", view)
	}
}

func TestAPIAppModelDashboardShortcutParity(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
	}
	app := newTestAPIAppModel(t, client)

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

	model, _ = overview.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
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

	model, cmd = closedChat.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
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
	app := newTestAPIAppModel(t, client)

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
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		startChatErr: errors.New("monthly spend limit"),
	}
	app := newTestAPIAppModel(t, client)

	failed := startAPIChatTurn(t, app, "yo")

	if failed.chat.responding {
		t.Fatal("chat remained responding after start error")
	}
	view := stripANSI(failed.View().Content)
	if !strings.Contains(view, "error starting session: monthly spend limit") {
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
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
	}
	app := newTestAPIAppModel(t, client)
	started := startAPIChatTurn(t, app, "yo")
	const errorText = "You've hit your org's monthly spend limit."

	model, _ := started.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: apiTestSessionDetailWith(server.SessionSummaryDTO{ID: chatSessionID, FeatureID: chatSessionID, Phase: testPhaseKeyResearch, Status: testStatusFailed}, server.SessionDetailDTO{

			TranscriptCursor: server.CursorDTO{Total: 2, Start: 0, End: 2},
		})},
		Transcript: &server.TranscriptResponse{
			Cursor: server.CursorDTO{Total: 2, Start: 0, End: 2},
			Messages: []server.TranscriptMessageDTO{
				{Index: 0, Role: roleAssistant, Type: testMessageTypeText, Text: errorText},
				{Index: 1, Role: testMessageRoleSystem, Type: transcriptTypeResult, Status: resultSubtypeError, Redacted: true},
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
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
	}
	app := newTestAPIAppModel(t, client)
	started := startAPIChatTurn(t, app, "yo")

	model, _ := started.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: apiTestSessionDetailWith(server.SessionSummaryDTO{ID: chatSessionID, FeatureID: chatSessionID, Phase: testPhaseKeyResearch, Status: testSessionStatusWaitingHelp}, server.SessionDetailDTO{

			TranscriptCursor: server.CursorDTO{Total: 1, Start: 0, End: 1},
		})},
		Transcript: &server.TranscriptResponse{
			Cursor: server.CursorDTO{Total: 1, Start: 0, End: 1},
			Messages: []server.TranscriptMessageDTO{
				{Index: 0, Role: roleAssistant, Type: testMessageTypeText, Text: "yo! What's up?"},
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
	model, cmd := ready.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want follow-up send command")
	}
	_, _ = model.(APIAppModel).Update(cmd())
	if got := client.helpRequests; len(got) != 1 || got[0].SessionID != chatSessionID || got[0].Message != "take two" {
		t.Fatalf("SendHelp requests = %+v, want chat follow-up message", got)
	}
}

func TestAPIAppModelChatRecoveryTickFetchesSessionSnapshot(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		refreshSnapshot: server.RefreshSnapshot{
			Session: &server.SessionDetailResponse{Session: apiTestSessionDetailWith(server.SessionSummaryDTO{
				ID:        chatSessionID,
				FeatureID: chatSessionID,
				Phase:     testPhaseKeyResearch,
				Status:    testSessionStatusWaitingHelp,
				TurnState: "waiting_input",
			}, server.SessionDetailDTO{

				TranscriptCursor: server.CursorDTO{Total: 2, Start: 0, End: 2},
			})},
			Transcript: &server.TranscriptResponse{
				Cursor: server.CursorDTO{Total: 2, Start: 0, End: 2},
				Messages: []server.TranscriptMessageDTO{
					{Index: 0, Role: roleAssistant, Type: testMessageTypeText, Text: "Recovered answer."},
					{Index: 1, Role: testMessageRoleSystem, Type: transcriptTypeResult, Status: "success", Redacted: true},
				},
			},
		},
	}
	app := newTestAPIAppModel(t, client)
	started := startAPIChatTurn(t, app, "yo")
	if !started.chat.responding || started.chat.sess == nil {
		t.Fatalf("setup: chat should be responding with a session, responding=%v sess=%#v", started.chat.responding, started.chat.sess)
	}

	model, cmd := started.Update(chatRecoveryTickMsg{sess: started.chat.sess})
	if cmd == nil {
		t.Fatal("chat recovery tick returned nil command, want snapshot fetch")
	}

	msgCh := make(chan tea.Msg, 1)
	go func() { msgCh <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-msgCh:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("chat recovery tick did not fetch the session snapshot promptly")
	}
	refresh, ok := msg.(apiRefreshSnapshotMsg)
	if !ok {
		t.Fatalf("chat recovery command returned %T, want apiRefreshSnapshotMsg", msg)
	}
	if got := client.refreshSignals; len(got) != 1 || got[0].Resource.Type != testResourceIDSession || got[0].Resource.ID != chatSessionID || got[0].Resource.FeatureID != chatSessionID {
		t.Fatalf("recovery refresh signals = %+v, want chat session-targeted refresh", got)
	}

	model, _ = model.(APIAppModel).Update(refresh)
	recovered := model.(APIAppModel)
	if recovered.chat.responding {
		t.Fatal("chat remained responding after recovery snapshot")
	}
	view := stripANSI(recovered.View().Content)
	if !strings.Contains(view, "Recovered answer.") || strings.Contains(view, "Thinking") {
		t.Fatalf("chat view did not recover from snapshot:\n%s", view)
	}
}

func TestAPIAppModelChatToolProgressOnlyWaitingHelpShowsNoAnswer(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
	}
	app := newTestAPIAppModel(t, client)
	started := startAPIChatTurn(t, app, "what is the status?")

	model, _ := started.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: apiTestSessionDetailWith(server.SessionSummaryDTO{ID: chatSessionID, FeatureID: chatSessionID, Phase: testPhaseKeyResearch, Status: testSessionStatusWaitingHelp}, server.SessionDetailDTO{

			TranscriptCursor: server.CursorDTO{Total: 1, Start: 0, End: 1},
		})},
		Transcript: &server.TranscriptResponse{
			Cursor: server.CursorDTO{Total: 1, Start: 0, End: 1},
			Messages: []server.TranscriptMessageDTO{
				{Index: 0, Role: testMessageRoleSystem, Type: transcriptTypeToolProgress, Tool: toolNameRead, Redacted: true},
			},
		},
	}})
	waiting := model.(APIAppModel)

	if waiting.chat.responding {
		t.Fatal("chat remained responding after a tool-progress-only WaitingHelp snapshot")
	}
	view := stripANSI(waiting.View().Content)
	if !strings.Contains(view, "No answer was returned") {
		t.Fatalf("chat view did not surface missing assistant response:\n%s", view)
	}
	if strings.Contains(view, "Using Read...") {
		t.Fatalf("chat kept stale live tool progress after the session became ready:\n%s", view)
	}
	if !strings.Contains(view, "[enter] Send") {
		t.Fatalf("chat did not return to send mode after the session became ready:\n%s", view)
	}
}

func TestAPIAppModelChatPendingAskUserSnapshotCanBeAnswered(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
	}
	app := newTestAPIAppModel(t, client)
	started := startAPIChatTurn(t, app, "ask me a question with 3 choices")

	model, _ := started.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: apiTestSessionDetailWith(server.SessionSummaryDTO{ID: chatSessionID, FeatureID: chatSessionID, Phase: testPhaseKeyResearch, Status: testSessionStatusWaitingHelp}, server.SessionDetailDTO{

			PendingControls: []server.ControlRequestDTO{{
				RequestID: testAskRequestID,
				SessionID: chatSessionID,
				FeatureID: chatSessionID,
				ToolName:  toolNameAskUserQuestion,
				Status:    testStatusPending,
				Questions: []server.AskUserQuestionDTO{{
					Question: testQuestionPickDirection,
					Options: []server.AskUserOptionDTO{
						{Label: testOptionLabelAlpha, Description: "First option"},
						{Label: testOptionLabelBeta, Description: "Second option"},
						{Label: testOptionLabelGamma, Description: "Third option"},
					},
				}},
			}},
		})},
		Transcript: &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
			{Index: 0, Role: roleAssistant, Type: transcriptTypeToolProgress, Tool: toolNameAskUserQuestion, Redacted: true},
		}},
	}})
	waiting := model.(APIAppModel)

	if waiting.chat.responding {
		t.Fatal("chat remained responding after pending AskUserQuestion snapshot")
	}
	view := stripANSI(waiting.View().Content)
	for _, want := range []string{testQuestionPickDirection, testOptionLabelAlpha, testOptionLabelBeta, testOptionLabelGamma, testHintEnterToSelect} {
		if !strings.Contains(view, want) {
			t.Fatalf("chat view missing %q while waiting for AskUser answer:\n%s", want, view)
		}
	}
	if strings.Contains(view, "[esc] Background") {
		t.Fatalf("chat view still rendered responding footer:\n%s", view)
	}

	// Navigate the picker down to testOptionLabelBeta and commit it — with a single
	// question this lands on the recap slot, so a second Enter submits.
	model, _ = waiting.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	after := model.(APIAppModel)
	if !after.chat.onRecapSlot() {
		t.Fatalf("expected recap slot after answering the only question, currentQuestionIdx=%d", after.chat.currentQuestionIdx)
	}
	model, cmd := after.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) on recap slot returned nil command, want AskUser answer command")
	}
	_, _ = model.(APIAppModel).Update(cmd())
	if len(client.helpRequests) != 0 {
		t.Fatalf("SendHelp requests = %+v, want none for AskUser answer", client.helpRequests)
	}
	if got := client.askUserAnswers; len(got) != 1 || got[0].RequestID != testAskRequestID || got[0].SessionID != chatSessionID || got[0].Answers[testQuestionPickDirection] != testOptionLabelBeta {
		t.Fatalf("AnswerAskUser requests = %+v, want chat answer", got)
	}
}

func TestAPIAppModelChatPromptOnlyAskUserSnapshotCanBeAnswered(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
	}
	app := newTestAPIAppModel(t, client)
	started := startAPIChatTurn(t, app, "ask me a question with 3 choices")

	model, _ := started.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: apiTestSessionDetail(
			server.SessionSummaryDTO{ID: chatSessionID, FeatureID: chatSessionID, Phase: testPhaseKeyResearch, Status: testSessionStatusRunning})},
		Transcript: &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
			{Index: 0, Role: testMessageRoleSystem, Type: transcriptTypeToolProgress, Tool: toolNameAskUserQuestion, Redacted: true},
		}},
	}})
	thinking := model.(APIAppModel)
	if !thinking.chat.responding {
		t.Fatal("setup: chat should still be responding after tool_progress snapshot")
	}

	askControl := server.ControlRequestDTO{
		RequestID: testAskRequestID,
		SessionID: chatSessionID,
		FeatureID: chatSessionID,
		ToolName:  toolNameAskUserQuestion,
		Status:    testStatusPending,
		Questions: []server.AskUserQuestionDTO{{
			Question: testQuestionPickDirection,
			Options: []server.AskUserOptionDTO{
				{Label: testOptionLabelAlpha},
				{Label: testOptionLabelBeta},
				{Label: testOptionLabelGamma},
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
	for _, want := range []string{testQuestionPickDirection, testOptionLabelAlpha, testOptionLabelBeta, testOptionLabelGamma, testHintEnterToSelect} {
		if !strings.Contains(view, want) {
			t.Fatalf("chat view missing %q after prompt-only AskUser snapshot:\n%s", want, view)
		}
	}

	// Navigate the picker down to testOptionLabelGamma and commit it — with a single
	// question this lands on the recap slot, so a second Enter submits.
	model, _ = waiting.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	after := model.(APIAppModel)
	if !after.chat.onRecapSlot() {
		t.Fatalf("expected recap slot after answering the only question, currentQuestionIdx=%d", after.chat.currentQuestionIdx)
	}
	model, cmd := after.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) on recap slot returned nil command, want AskUser answer command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	if got := client.askUserAnswers; len(got) != 1 || got[0].RequestID != testAskRequestID || got[0].Answers[testQuestionPickDirection] != testOptionLabelGamma {
		t.Fatalf("AnswerAskUser requests = %+v, want prompt-only chat answer", got)
	}

	model, _ = model.(APIAppModel).Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Prompts: &server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{askControl}},
	}})
	answered := model.(APIAppModel)
	view = stripANSI(answered.View().Content)
	occurrences := 0
	for _, turn := range answered.chat.turns {
		occurrences += strings.Count(turn.Text, testQuestionPickDirection)
	}
	if occurrences != 1 {
		t.Fatalf("AskUser prompt history count = %d, want exactly one inactive history entry: %+v", occurrences, answered.chat.turns)
	}
	answerOccurrences := 0
	for _, turn := range answered.chat.turns {
		if turn.Role == chatTurnUser {
			answerOccurrences += strings.Count(turn.Text, testOptionLabelGamma)
		}
	}
	if answerOccurrences != 1 {
		t.Fatalf("AskUser answer history count = %d, want exactly one: %+v", answerOccurrences, answered.chat.turns)
	}
	if answered.chat.hasActiveQuestion() {
		t.Fatalf("stale AskUser prompt reactivated after answer:\n%s", view)
	}
	if !answered.chat.responding {
		t.Fatalf("chat stopped waiting for assistant after stale AskUser prompt:\n%s", view)
	}
}

func TestAPIAppModelChatAcceptsTerminalPaste(t *testing.T) {
	t.Parallel()

	app := APIAppModel{width: 100, height: 30}
	app.chatReady = true
	app.chatOpen = true
	app.chat = NewAPIChatModel(app.width, 10, nil)

	model, _ := app.Update(tea.PasteMsg{Content: "line one\nline two"})
	updated := model.(APIAppModel)

	if got := updated.chat.input.Value(); got != "line one\nline two" {
		t.Fatalf("chat input after paste = %q, want pasted text", got)
	}
}

func TestAPIAppModelCreateFeatureUsesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Defaults:  config.ModelConfig{Research: testModelGPT54, Planning: testModelGPT54, Implementation: testModelGPT54, Review: testModelGPT54},
			Providers: []string{testProviderCodex},
			Repos: []server.ConfigRepoDTO{
				{Name: testRepoNameOrchestrator},
			},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{testProviderCodex},
			ProviderModels: map[string][]server.ModelDTO{
				testProviderCodex: {{ID: testModelGPT54}},
			},
			PhaseDefaults: config.ModelConfig{Research: testModelGPT54, Planning: testModelGPT54, Implementation: testModelGPT54, Review: testModelGPT54},
		},
		createAccepted: apiTestActionResponse{FeatureID: testFeatureIDFeatCreated},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd != nil {
		t.Fatal("Update(n) returned command before create submit")
	}
	creating := model.(APIAppModel)
	if creating.wizard == nil {
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
	for _, want := range []string{"Pick one or more repos", testRepoNameOrchestrator, "Browse for more"} {
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
		if len(got[0].Repos) != 1 || got[0].Repos[0] != testRepoNameOrchestrator {
			t.Fatalf("CreateFeature repos = %+v, want agentic-orchestrator selected", got[0].Repos)
		}
		if got[0].Models.Implementation != testModelGPT54 {
			t.Fatalf("CreateFeature implementation model = %q, want gpt-5.4", got[0].Models.Implementation)
		}
	}
	if got := strings.Join(client.startFeatureIDs, ","); got != testFeatureIDFeatCreated {
		t.Fatalf("StartFeature calls = %q, want feat-created auto-start after create", got)
	}
	if got := strings.Join(client.calls[len(client.calls)-2:], ","); got != "CreateFeature,StartFeature" {
		t.Fatalf("last API calls = %q, want CreateFeature,StartFeature", got)
	}
	if created.wizard != nil {
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
	makeGitRepoDir(t, rootA, testRepoNameAPI)
	makeGitRepoDir(t, rootB, testRepoNameWeb)

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			WorkspaceRoots: []string{rootA},
			Repos:          testRuntimeConfigRepos(nil, []string{rootA}),
		},
		updateRuntimeConfigAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

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
	if repoPaths[testRepoNameWeb] != filepath.Join(rootB, testRepoNameWeb) {
		t.Fatalf("runtime repos = %+v, want discovered web repo under rootB", updated.runtimeConfig.Repos)
	}
}

func TestAPIAppModelWizardBrowseRootPersistsAndRefreshesRepos(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	makeGitRepoDir(t, root, testRepoNameOrchestrator)

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Defaults:  config.ModelConfig{Research: testModelGPT54, Planning: testModelGPT54, Implementation: testModelGPT54, Review: testModelGPT54},
			Providers: []string{testProviderCodex},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{testProviderCodex},
			ProviderModels: map[string][]server.ModelDTO{
				testProviderCodex: {{ID: testModelGPT54}},
			},
			PhaseDefaults: config.ModelConfig{Research: testModelGPT54, Planning: testModelGPT54, Implementation: testModelGPT54, Review: testModelGPT54},
		},
		updateRuntimeConfigAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

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
	if _, ok := updated.wizard.repoPaths[testRepoNameOrchestrator]; !ok {
		t.Fatalf("wizard repo paths = %+v, want discovered agentic-orchestrator", updated.wizard.repoPaths)
	}
}

func TestAPIAppModelWizardCreateRepoPersistsRootRescansAndAutoSelects(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	createdPath := filepath.Join(root, "new-service")

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Defaults:       config.ModelConfig{Research: testModelGPT54, Planning: testModelGPT54, Implementation: testModelGPT54, Review: testModelGPT54},
			Providers:      []string{testProviderCodex},
			WorkspaceRoots: []string{root},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{testProviderCodex},
			ProviderModels: map[string][]server.ModelDTO{
				testProviderCodex: {{ID: testModelGPT54}},
			},
			PhaseDefaults: config.ModelConfig{Research: testModelGPT54, Planning: testModelGPT54, Implementation: testModelGPT54, Review: testModelGPT54},
		},
		updateRuntimeConfigAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

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

func newTestAPIAppModel(t *testing.T, client *fakeTUIAPIClient) APIAppModel {
	t.Helper()
	app, err := NewAPIAppModel(context.Background(), client, APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAPIAppModel() error = %v", err)
	}
	return app
}

// startAPIChatTurn opens the AMA chat panel, types message, presses enter,
// and applies the resulting chat-start command.
func startAPIChatTurn(t *testing.T, app APIAppModel, message string) APIAppModel {
	t.Helper()
	model, _ := app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	chatting := model.(APIAppModel)
	chatting.chat.input.SetValue(message)
	model, cmd := chatting.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want chat start command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	return model.(APIAppModel)
}

// walkAPIPublishReviewToConfirmation advances a publish-review model (already
// on the diff-review step) through commit log and PR description generation
// to the final publish confirmation step, returning the confirming-state
// model.
func walkAPIPublishReviewToConfirmation(t *testing.T, model APIAppModel) APIAppModel {
	t.Helper()
	m, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter on diff) returned command before commit review")
	}
	commits := m.(APIAppModel)
	m, cmd = commits.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter on commits) returned nil command, want PR description generation")
	}
	m, _ = m.(APIAppModel).Update(cmd())
	describing := m.(APIAppModel)
	m, cmd = describing.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Update(enter on PR description) returned command before final confirmation")
	}
	return m.(APIAppModel)
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
			wantKind: mutationKindFeatureMerge,
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.mergeFeatureIDs, ","); got != testFeatureIDActive {
					t.Fatalf("MergeFeature calls = %q, want active", got)
				}
			},
		},
		{
			name:     "mark done",
			key:      tea.KeyPressMsg{Code: 'D', Text: "D"},
			actionID: "mark-done",
			wantKind: mutationKindFeatureMarkDone,
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.markDoneFeatureIDs, ","); got != testFeatureIDActive {
					t.Fatalf("MarkDone calls = %q, want active", got)
				}
			},
		},
		{
			name:     testCycleTypeRebase,
			key:      tea.KeyPressMsg{Code: 'b', Text: "b"},
			actionID: testCycleTypeRebase,
			wantKind: mutationKindFeatureRebase,
			accepted: apiTestActionResponse{},
			refresh: struct {
				cycleType string
				wantLabel string
			}{cycleType: testCycleTypeRebase, wantLabel: "Rebasing"},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.startRebaseFeatureIDs, ","); got != testFeatureIDActive {
					t.Fatalf("StartRebase calls = %q, want active", got)
				}
				if got := client.startRebaseRequests; len(got) != 1 {
					t.Fatalf("StartRebase requests = %+v, want one request", got)
				}
			},
		},
		{
			name:     "cleanup worktrees",
			key:      tea.KeyPressMsg{Code: 'c', Text: "c"},
			actionID: "cleanup",
			wantKind: mutationKindFeatureCleanup,
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.cleanupFeatureIDs, ","); got != testFeatureIDActive {
					t.Fatalf("CleanupFeature calls = %q, want active", got)
				}
				if got := client.cleanupRequests; len(got) != 1 || got[0].Target != "worktrees" {
					t.Fatalf("CleanupFeature requests = %+v, want target worktrees", got)
				}
			},
		},
		{
			name:     reviewModeRewind,
			key:      tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl},
			actionID: reviewModeRewind,
			wantKind: mutationKindFeatureRewind,
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.rewindFeatureIDs, ","); got != testFeatureIDActive {
					t.Fatalf("RewindFeature calls = %q, want active", got)
				}
				if got := client.rewindRequests; len(got) != 1 || got[0].TargetPhase != testPhaseNameImplement || got[0].RoadmapPhase != 0 {
					t.Fatalf("RewindFeature requests = %+v, want target phase implement without roadmap phase", got)
				}
			},
		},
		{
			name:     "restart",
			key:      tea.KeyPressMsg{Code: 'r', Text: "r"},
			actionID: "restart",
			wantKind: mutationKindFeatureRestart,
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.restartFeatureIDs, ","); got != testFeatureIDActive {
					t.Fatalf("RestartFeature calls = %q, want active", got)
				}
			},
		},
		{
			name:     "stop",
			key:      tea.KeyPressMsg{Code: 's', Text: "s"},
			actionID: "pause-stop",
			wantKind: mutationKindFeatureStop,
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.stopFeatureIDs, ","); got != testFeatureIDActive {
					t.Fatalf("StopFeature calls = %q, want active", got)
				}
			},
		},
		{
			name:     actionIDDelete,
			key:      tea.KeyPressMsg{Code: 'd', Text: "d"},
			actionID: actionIDDelete,
			wantKind: mutationKindFeatureDelete,
			accepted: apiTestActionResponse{},
			assertCall: func(t *testing.T, client *fakeTUIAPIClient) {
				t.Helper()
				if got := strings.Join(client.deleteFeatureIDs, ","); got != testFeatureIDActive {
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
					{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, Repos: []string{testRepoNameOrchestrator}, CreatedAt: time.Now()},
				}},
				detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement}, server.FeatureDetailDTO{

					Cycle: tt.cycle,
					RepoStatus: []server.RepoStatusDTO{
						{Name: testRepoNameOrchestrator, Publishable: true},
					},
					Actions: []server.ActionDTO{
						{ID: tt.actionID, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
					},
				})},
				restartAccepted:     tt.accepted,
				stopAccepted:        tt.accepted,
				deleteAccepted:      tt.accepted,
				mergeAccepted:       tt.accepted,
				retryAccepted:       tt.accepted,
				markDoneAccepted:    tt.accepted,
				cleanupAccepted:     tt.accepted,
				rewindAccepted:      tt.accepted,
				startRebaseAccepted: tt.accepted,
			}
			app := newTestAPIAppModel(t, client)

			model, cmd := app.Update(tt.key)
			if cmd != nil {
				t.Fatalf("Update(%s) returned command before confirmation", tt.name)
			}
			confirming := model.(APIAppModel)
			wantTitle := "Confirm " + apiMutationKindLabel(tt.wantKind)
			if tt.wantKind == mutationKindFeatureRewind {
				wantTitle = "Rewind Confirmation"
			}
			if view := stripANSI(confirming.View().Content); !strings.Contains(view, wantTitle) {
				t.Fatalf("View() missing %q confirmation in:\n%s", wantTitle, view)
			}
			if len(client.mergeFeatureIDs)+len(client.retryFeatureIDs)+len(client.markDoneFeatureIDs)+len(client.cleanupFeatureIDs)+len(client.rewindFeatureIDs)+len(client.startRebaseFeatureIDs)+len(client.restartFeatureIDs)+len(client.stopFeatureIDs)+len(client.deleteFeatureIDs) != 0 {
				t.Fatalf("API action was sent before confirmation: merge=%v retry=%v markDone=%v cleanup=%v restart=%v stop=%v delete=%v rewind=%v rebase=%v", client.mergeFeatureIDs, client.retryFeatureIDs, client.markDoneFeatureIDs, client.cleanupFeatureIDs, client.restartFeatureIDs, client.stopFeatureIDs, client.deleteFeatureIDs, client.rewindFeatureIDs, client.startRebaseFeatureIDs)
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
				cycle := &server.CycleDTO{Type: tt.refresh.cycleType, Status: featureStatusTokenRunning}
				client.detail = server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusCodeReady, CurrentPhase: actionIDPublish, Cycle: cycle, CreatedAt: time.Now(), Repos: []string{testRepoNameOrchestrator}}, server.FeatureDetailDTO{

					Cycle: cycle,
					RepoStatus: []server.RepoStatusDTO{
						{Name: testRepoNameOrchestrator, Touched: true, Publishable: true, CycleType: tt.refresh.cycleType, CycleStatus: featureStatusTokenRunning},
					},
				})}
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
			if tt.wantKind == mutationKindFeatureDelete {
				if strings.Contains(view, testFeatureIDActive) {
					t.Fatalf("API app View() still shows deleted feature:\n%s", view)
				}
			} else if !strings.Contains(view, testFeatureIDActive) {
				t.Fatalf("API app View() missing %q in:\n%s", testFeatureIDActive, view)
			}
		})
	}
}

func TestAPIAppModelMaxIterationsRestartExtendsBudgetAfterConfirmation(t *testing.T) {
	t.Parallel()

	summary := server.FeatureSummary{
		ID:           testFeatureIDActive,
		Name:         testFeatureNameClientCutover,
		Slug:         testFeatureSlugClientCutover,
		Status:       testFeatureStatusFailed,
		CurrentPhase: testPhaseNameImplement,
		CreatedAt:    time.Now(),
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{
			Failure: &server.FailureDTO{Type: feature.FailureMaxIterations, Message: "reached maximum iteration count"},
			Actions: []server.ActionDTO{
				{ID: actionIDRestart, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
			},
		})},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd != nil {
		t.Fatal("Update(r) returned command before confirmation")
	}
	confirming := model.(APIAppModel)
	if view := stripANSI(confirming.View().Content); !strings.Contains(view, "add 10 more iterations") {
		t.Fatalf("max-iterations confirmation missing budget extension:\n%s", view)
	}
	if len(client.restartRequests) != 0 {
		t.Fatalf("RestartFeature requests = %+v before confirmation, want none", client.restartRequests)
	}

	model, cmd = confirming.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("Update(y) returned nil command, want restart mutation")
	}
	_ = model
	_ = cmd()

	if got := client.restartRequests; len(got) != 1 || got[0].MaxIterationsDelta != 10 || got[0].MaxPlanIterationsDelta != 2 {
		t.Fatalf("RestartFeature requests = %+v, want max_iterations_delta=10 and max_plan_iterations_delta=2", got)
	}
}

func TestAPIAppModelFeatureConfigEditorLoadsFromRESTAndSavesMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: time.Now()},
		}},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{testProviderCodex},
			ProviderModels: map[string][]server.ModelDTO{
				testProviderCodex: {
					{ID: testModelCodexGPT54},
					{ID: testModelCodexGPT55},
				},
			},
			PhaseDefaults: config.ModelConfig{
				Research: testModelCodexGPT54,
			},
			PhaseProviderModels: map[string]map[string][]string{
				testActivityResearch: {testProviderCodex: {testModelCodexGPT54, testModelCodexGPT55}},
			},
		},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish}, server.FeatureDetailDTO{

			Pipeline: testPipelineSizeLarge,
		})},
		featureConfig: server.FeatureConfigResponse{
			FeatureID: testFeatureIDActive,
			Current: server.FeatureConfigDTO{
				Models:      config.ModelConfig{Research: testModelCodexGPT54, Planning: testModelCodexGPT54, Implementation: testModelCodexGPT54, Review: testModelCodexGPT54, KBBuild: testModelCodexGPT54},
				Inquireness: testInquirenessTargeted,
				Checkpoints: server.CheckpointsDTO{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
				Pipeline:    testPipelineSizeLarge,
			},
			Defaults: server.FeatureConfigDTO{
				Models: config.ModelConfig{Research: testModelCodexGPT54},
			},
		},
		updateFeatureConfigAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if cmd == nil {
		t.Fatal("Update(e) returned nil command, want feature config fetch command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	editing := model.(APIAppModel)

	if got := strings.Join(client.featureConfigIDs, ","); got != testFeatureIDActive {
		t.Fatalf("FeatureConfig calls = %q, want active", got)
	}
	view := stripANSI(editing.View().Content)
	for _, want := range []string{"Edit Config", testFeatureNameClientCutover, testSectionLabelModels, "Behavior", testSectionLabelGates, testActivityResearch, "codex / gpt-5.4"} {
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

	if got := strings.Join(client.updateFeatureConfigIDs, ","); got != testFeatureIDActive {
		t.Fatalf("UpdateFeatureConfig calls = %q, want active", got)
	}
	if got := client.updateFeatureConfigRequests; len(got) != 1 || got[0].Models.Research != testModelCodexGPT55 || got[0].Pipeline != testPipelineSizeLarge || got[0].Inquireness != testInquirenessTargeted || !got[0].Checkpoints.RoadmapReview || !got[0].Checkpoints.PhasePlanReview || !got[0].Checkpoints.ManualPublish {
		t.Fatalf("UpdateFeatureConfig requests = %+v, want edited research model and preserved config axes", got)
	}
	view = stripANSI(saved.View().Content)
	for _, want := range []string{"Completed Feature Config", testFeatureIDActive} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelFeatureConfigEditorOpensForRunningFeature(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		featureConfig: server.FeatureConfigResponse{
			FeatureID: testFeatureIDActive,
			Current: server.FeatureConfigDTO{
				Inquireness: testInquirenessTargeted,
				Pipeline:    testPipelineSizeLarge,
			},
		},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if cmd == nil {
		t.Fatal("Update(e) returned nil command for running feature, want config fetch")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	editing := model.(APIAppModel)
	if got := strings.Join(client.featureConfigIDs, ","); got != testFeatureIDActive {
		t.Fatalf("FeatureConfig calls = %q, want active", got)
	}
	if editing.configEditor == nil {
		t.Fatal("configEditor is nil after loading running feature config")
	}
	if view := stripANSI(editing.View().Content); !strings.Contains(view, "next restart or next phase") {
		t.Fatalf("running feature config editor missing deferred-effect warning:\n%s", view)
	}
}

func TestAPIAppModelFeatureConfigEditorSavesInputAlertOverride(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: time.Now()},
		}},
		featureConfig: server.FeatureConfigResponse{
			FeatureID: testFeatureIDActive,
			Current: server.FeatureConfigDTO{
				Models:      config.ModelConfig{Research: testModelCodexGPT54},
				Inquireness: testInquirenessTargeted,
				Pipeline:    testPipelineSizeLarge,
				Checkpoints: server.CheckpointsDTO{ManualPublish: true},
			},
			Defaults: server.FeatureConfigDTO{
				InputNotifications: server.FeatureConfigInputNotifications(feature.InputNotificationsMuted),
			},
		},
		updateFeatureConfigAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if cmd == nil {
		t.Fatal("Update(e) returned nil command, want feature config fetch command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	editing := model.(APIAppModel)
	if editing.configEditor == nil {
		t.Fatal("configEditor is nil after loading feature config")
	}
	editing.configEditor.activeTab = tabBehavior
	editing.configEditor.focus = configFocusBody
	editing.configEditor.behaviorCursor = 1

	view := stripANSI(editing.View().Content)
	for _, want := range []string{"Input Alerts", "default"} {
		if !strings.Contains(view, want) {
			t.Fatalf("feature Behavior tab missing %q:\n%s", want, view)
		}
	}

	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyRight})             // default -> enabled override
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // back to tabs
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want feature config save")
	}
	_, _ = model.(APIAppModel).Update(cmd())

	if got := client.updateFeatureConfigRequests; len(got) != 1 ||
		got[0].InputNotifications != string(feature.InputNotificationsEnabled) {
		t.Fatalf("UpdateFeatureConfig requests = %+v, want explicit enabled input-alert override", got)
	}
}

func TestAPIAppModelFeatureConfigEditorSavesAutomaticReviewMode(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: time.Now()},
		}},
		featureConfig: server.FeatureConfigResponse{
			FeatureID: testFeatureIDActive,
			Current: server.FeatureConfigDTO{
				Models:              config.ModelConfig{Research: testModelCodexGPT54},
				Inquireness:         testInquirenessTargeted,
				Pipeline:            testPipelineSizeLarge,
				Checkpoints:         server.CheckpointsDTO{ManualPublish: true},
				AutomaticReviewMode: server.FeatureConfigAutomaticReviewModeDefault,
			},
		},
		updateFeatureConfigAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if cmd == nil {
		t.Fatal("Update(e) returned nil command, want feature config fetch command")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	editing := model.(APIAppModel)
	if editing.configEditor == nil {
		t.Fatal("configEditor is nil after loading feature config")
	}
	editing.configEditor.activeTab = tabBehavior
	editing.configEditor.focus = configFocusBody
	editing.configEditor.behaviorCursor = len(editing.configEditor.behaviorSettings()) - 1

	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyRight})             // default -> enabled
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // back to tabs
	model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want feature config save")
	}
	_, _ = model.(APIAppModel).Update(cmd())

	if got := client.updateFeatureConfigRequests; len(got) != 1 ||
		got[0].AutomaticReviewMode == nil ||
		*got[0].AutomaticReviewMode != string(feature.AutomaticReviewEnabled) {
		t.Fatalf("UpdateFeatureConfig requests = %+v, want enabled automatic-review override", got)
	}
}

func TestApplyAPIFeatureDetailAutomaticReview(t *testing.T) {
	t.Parallel()

	f := &feature.Feature{}
	applyAPIFeatureDetail(f, server.FeatureDetailDTO{
		AutomaticReview: server.AutomaticReviewState{
			Mode:    server.AutomaticReviewStateModeEnabled,
			Enabled: true,
			Source:  server.AutomaticReviewStateSource("feature"),
		},
	})
	if got := feature.NormalizeAutomaticReviewMode(f.AutomaticReviewMode); got != feature.AutomaticReviewEnabled {
		t.Errorf("AutomaticReviewMode = %q, want enabled", got)
	}
	if !f.AutomaticReviewEnabled {
		t.Error("AutomaticReviewEnabled = false, want true")
	}
	if f.AutomaticReviewSource != feature.AutomaticReviewSourceFeature {
		t.Errorf("AutomaticReviewSource = %q, want feature", f.AutomaticReviewSource)
	}
}

func TestAPIAppModelWorkspaceConfigEditorSavesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Notifications: server.NotificationConfigDTO{MuteFeatureInput: false},
			Defaults: config.ModelConfig{
				Inquiry:        testModelCodexGPT54,
				Research:       testModelCodexGPT54,
				Planning:       testModelCodexGPT54,
				Implementation: testModelCodexGPT54,
				Review:         testModelCodexGPT54,
				KBBuild:        testModelCodexGPT54,
			},
			FeatureDefaults: server.FeatureDefaultsDTO{
				Models: config.ModelConfig{
					Inquiry:        testModelCodexGPT54,
					Research:       testModelCodexGPT54,
					Planning:       testModelCodexGPT54,
					Implementation: testModelCodexGPT54,
					Review:         testModelCodexGPT54,
					KBBuild:        testModelCodexGPT54,
				},
				Inquireness: testPipelineSizeMedium,
				Pipeline:    testPipelineSizeLarge,
				Checkpoints: config.Checkpoints{ManualPublish: true},
			},
			Providers: []string{testProviderCodex},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{testProviderCodex},
			ProviderModels: map[string][]server.ModelDTO{
				testProviderCodex: {
					{ID: testModelCodexGPT54},
					{ID: testModelCodexGPT55},
				},
			},
			PhaseDefaults: config.ModelConfig{
				Inquiry:  testModelCodexGPT54,
				Research: testModelCodexGPT54,
			},
			PhaseProviderModels: map[string]map[string][]string{
				testActivityResearch: {testProviderCodex: {testModelCodexGPT54, testModelCodexGPT55}},
			},
		},
		updateRuntimeConfigAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'E', Text: "E"})
	if cmd != nil {
		t.Fatal("Update(E) returned command before runtime config save")
	}
	editing := model.(APIAppModel)
	view := stripANSI(editing.View().Content)
	for _, want := range []string{"Edit Config · Workspace Defaults", testSectionLabelModels, "Behavior", testSectionLabelGates, testSectionLabelPhases, testLabelModelsForCodex} {
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

	edited.configEditor.activeTab = tabBehavior
	edited.configEditor.focus = configFocusBody
	edited.configEditor.editor.rowCursor = edited.configEditor.editor.inquirenessRow()
	model, _ = edited.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // Input Alerts row
	editingAlerts := model.(APIAppModel)
	view = stripANSI(editingAlerts.View().Content)
	for _, want := range []string{"Input Alerts", "enabled"} {
		if !strings.Contains(view, want) {
			t.Fatalf("workspace Behavior tab missing %q:\n%s", want, view)
		}
	}
	model, _ = editingAlerts.Update(tea.KeyPressMsg{Code: tea.KeyRight})       // enabled -> muted
	model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // back to tabs
	edited = model.(APIAppModel)
	model, cmd = edited.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter) returned nil command, want runtime config update command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	saved := model.(APIAppModel)

	if got := client.updateRuntimeConfigRequests; len(got) != 1 ||
		got[0].Defaults.Models.Inquiry != testModelCodexGPT55 ||
		got[0].Defaults.Inquireness != "high" ||
		got[0].Defaults.Checkpoints == nil ||
		!got[0].Defaults.Checkpoints.RoadmapReview ||
		got[0].Notifications == nil ||
		!got[0].Notifications.MuteFeatureInput {
		t.Fatalf("UpdateRuntimeConfig requests = %+v, want edited models, behavior, gates, and muted input alerts", got)
	}
	if saved.configEditor != nil {
		t.Fatal("workspace config editor still open after successful save")
	}
	if saved.runtimeConfig.FeatureDefaults.Models.Inquiry != testModelCodexGPT55 {
		t.Fatalf("runtime snapshot inquiry default = %q, want reloaded codex:gpt-5.5", saved.runtimeConfig.FeatureDefaults.Models.Inquiry)
	}
	view = stripANSI(saved.View().Content)
	for _, want := range []string{"Completed Runtime Config"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelNeedInputNotificationsHonorFeatureMode(t *testing.T) {
	t.Parallel()

	type call struct {
		name   string
		reason string
	}
	var calls []call
	recordNotify := func(featureName, reason string) tea.Cmd {
		calls = append(calls, call{name: featureName, reason: reason})
		return nil
	}

	baseFeature := server.FeatureSummary{
		ID:        testFeatureIDActive,
		Name:      testFeatureNameClientCutover,
		Slug:      testFeatureSlugClientCutover,
		Status:    testFeatureStatusImplementing,
		CreatedAt: time.Now(),
	}
	gate := func(iteration int, mode feature.InputNotificationsMode) server.NeedInputGateDTO {
		return server.NeedInputGateDTO{
			FeatureID:          testFeatureIDActive,
			Open:               true,
			Scope:              "feature",
			Iteration:          iteration,
			Summary:            "Choose the rollout plan",
			InputNotifications: string(mode),
		}
	}

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{baseFeature}},
		runtime:  server.RuntimeConfigResponse{Notifications: server.NotificationConfigDTO{MuteFeatureInput: false}},
		prompts:  server.PromptSnapshotResponse{NeedUserInputs: []server.NeedInputGateDTO{gate(1, feature.InputNotificationsDefault)}},
	}
	app := newTestAPIAppModel(t, client)
	app.notifyUser = recordNotify
	if len(calls) != 0 {
		t.Fatalf("startup notifications = %+v, want none", calls)
	}

	samePrompt := client.prompts
	model, _ := app.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{Prompts: &samePrompt}})
	app = model.(APIAppModel)
	if len(calls) != 0 {
		t.Fatalf("duplicate prompt notifications = %+v, want none", calls)
	}

	defaultPrompt := server.PromptSnapshotResponse{NeedUserInputs: []server.NeedInputGateDTO{gate(2, feature.InputNotificationsDefault)}}
	model, _ = app.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{Prompts: &defaultPrompt}})
	app = model.(APIAppModel)
	if len(calls) != 1 || calls[0].name != testFeatureNameClientCutover || calls[0].reason != "Choose the rollout plan" {
		t.Fatalf("default notification calls = %+v, want one feature alert", calls)
	}

	clientMuted := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{baseFeature}},
		runtime:  server.RuntimeConfigResponse{Notifications: server.NotificationConfigDTO{MuteFeatureInput: true}},
	}
	mutedApp := newTestAPIAppModel(t, clientMuted)
	mutedApp.notifyUser = recordNotify
	before := len(calls)
	mutedDefault := server.PromptSnapshotResponse{NeedUserInputs: []server.NeedInputGateDTO{gate(3, feature.InputNotificationsDefault)}}
	model, _ = mutedApp.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{Prompts: &mutedDefault}})
	mutedApp = model.(APIAppModel)
	if len(calls) != before {
		t.Fatalf("global-muted default notification calls = %+v, want unchanged", calls)
	}

	enabledOverride := server.PromptSnapshotResponse{NeedUserInputs: []server.NeedInputGateDTO{gate(4, feature.InputNotificationsEnabled)}}
	model, _ = mutedApp.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{Prompts: &enabledOverride}})
	mutedApp = model.(APIAppModel)
	if len(calls) != before+1 {
		t.Fatalf("enabled override notification calls = %+v, want one additional alert", calls)
	}

	mutedOverride := server.PromptSnapshotResponse{NeedUserInputs: []server.NeedInputGateDTO{gate(5, feature.InputNotificationsMuted)}}
	model, _ = app.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{Prompts: &mutedOverride}})
	app = model.(APIAppModel)
	if len(calls) != before+1 {
		t.Fatalf("muted override notification calls = %+v, want unchanged", calls)
	}

	_ = app
	_ = mutedApp
}

func TestAPIAppModelShiftNNoLongerTogglesInputNotifications(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork}, server.FeatureDetailDTO{})},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'N', Text: "N"})
	if cmd != nil {
		t.Fatal("Update(Shift+N) returned command, want removed input-alert flow to ignore the key")
	}
	after := model.(APIAppModel)
	if after.statusMessage != "" {
		t.Fatalf("statusMessage after Shift+N = %q, want unchanged", after.statusMessage)
	}
}

func TestAPIAppModelWorkspaceConfigEditorIncludesUtilitiesAndDiscoveredRoleOptions(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Defaults: config.ModelConfig{
				Research:       testModelCodexGPT54,
				Planning:       testModelCodexGPT54,
				Implementation: testModelCodexGPT54,
				Review:         testModelCodexGPT54,
				Utilities:      testModelCodexGPT54Mini,
				KBBuild:        testModelCodexGPT54,
			},
			FeatureDefaults: server.FeatureDefaultsDTO{
				Models: config.ModelConfig{
					Research:       testModelCodexGPT54,
					Planning:       testModelCodexGPT54,
					Implementation: testModelCodexGPT54,
					Review:         testModelCodexGPT54,
					Utilities:      testModelCodexGPT54Mini,
					KBBuild:        testModelCodexGPT54,
				},
				Inquireness: testPipelineSizeMedium,
				Pipeline:    testPipelineSizeLarge,
			},
			Providers: []string{testProviderCodex},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{testProviderCodex},
			ProviderModels: map[string][]server.ModelDTO{
				testProviderCodex: {
					{ID: testModelCodexGPT54},
					{ID: testModelCodexGPT54Mini},
					{ID: testModelCodexGPT55Mini},
				},
			},
			PhaseDefaults: config.ModelConfig{
				Utilities: testModelCodexGPT54Mini,
			},
			PhaseProviderModels: map[string]map[string][]string{
				"chat": {testProviderCodex: {testModelCodexGPT54Mini, testModelCodexGPT55Mini}},
			},
		},
		updateRuntimeConfigAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, _ := app.Update(tea.KeyPressMsg{Code: 'E', Text: "E"})
	editing := model.(APIAppModel)
	view := stripANSI(editing.View().Content)
	for _, want := range []string{"Edit Config · Workspace Defaults", testSectionLabelPhases, testLabelModelsForCodex, testSectionLabelUtilities, "gpt-5.4-mini"} {
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
	for _, want := range []string{testSectionLabelUtilities, testLabelModelsForCodex, "gpt-5.4-mini", "gpt-5.5-mini"} {
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
	_, _ = model.(APIAppModel).Update(msg)

	if got := client.updateRuntimeConfigRequests; len(got) != 1 || got[0].Defaults.Models.Utilities != testModelCodexGPT55Mini {
		t.Fatalf("UpdateRuntimeConfig requests = %+v, want edited utilities default model", got)
	}
}

func TestAPIAppModel_ResizeThenWorkspaceConfigEffortComplete(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		runtime: server.RuntimeConfigResponse{
			Defaults: config.ModelConfig{
				Research:       testModelCodexGPT54,
				Planning:       testModelCodexGPT54,
				Implementation: testModelCodexGPT54,
				Review:         testModelCodexGPT54,
				KBBuild:        testModelCodexGPT54,
			},
			FeatureDefaults: server.FeatureDefaultsDTO{
				Pipeline: testPipelineSizeLarge,
			},
			Providers: []string{testProviderCodex},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{testProviderCodex},
			ProviderModels: map[string][]server.ModelDTO{
				testProviderCodex: {{ID: testModelCodexGPT54}},
			},
		},
	}
	app := newTestAPIAppModel(t, client)

	model, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = model.(APIAppModel)

	model, _ = app.Update(tea.KeyPressMsg{Code: 'E', Text: "E"})
	editing := model.(APIAppModel)

	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	editing = model.(APIAppModel)
	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	editing = model.(APIAppModel)
	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	editing = model.(APIAppModel)
	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	editing = model.(APIAppModel)
	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	editing = model.(APIAppModel)

	view := stripANSI(editing.View().Content)
	if !strings.Contains(view, "Auto (high)") {
		t.Errorf("complete Auto (high) not visible after resize → workspace open:\n%s", view)
	}
	for i, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 120 {
			t.Errorf("line %d width %d exceeds 120-column terminal:\n%s", i, w, line)
		}
	}
}

func TestAPIAppModel_ResizeThenFeatureConfigEffortComplete(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement}, server.FeatureDetailDTO{
			Pipeline: testPipelineSizeLarge,
		})},
		featureConfig: server.FeatureConfigResponse{
			FeatureID: testFeatureIDActive,
			Current: server.FeatureConfigDTO{
				Models:      config.ModelConfig{Research: testModelCodexGPT54, Planning: testModelCodexGPT54, Implementation: testModelCodexGPT54, Review: testModelCodexGPT54, KBBuild: testModelCodexGPT54},
				Inquireness: testInquirenessTargeted,
				Pipeline:    testPipelineSizeLarge,
			},
			Defaults: server.FeatureConfigDTO{
				Models: config.ModelConfig{Research: testModelCodexGPT54},
			},
		},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{testProviderCodex},
			ProviderModels: map[string][]server.ModelDTO{
				testProviderCodex: {{ID: testModelCodexGPT54}},
			},
		},
	}
	app := newTestAPIAppModel(t, client)

	model, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = model.(APIAppModel)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if cmd == nil {
		t.Fatal("Update(e) returned nil command, want feature config fetch command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	editing := model.(APIAppModel)

	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	editing = model.(APIAppModel)
	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	editing = model.(APIAppModel)
	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	editing = model.(APIAppModel)
	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	editing = model.(APIAppModel)
	model, _ = editing.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	editing = model.(APIAppModel)

	view := stripANSI(editing.View().Content)
	if !strings.Contains(view, "Auto (high)") {
		t.Errorf("complete Auto (high) not visible after resize → feature config open:\n%s", view)
	}
	for i, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 120 {
			t.Errorf("line %d width %d exceeds 120-column terminal:\n%s", i, w, line)
		}
	}
}

func TestAPIAppModelFeatureConfigEditorDoesNotExposeUtilities(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: time.Now()},
		}},
		catalog: server.ModelCatalogResponse{
			ProviderOrder: []string{testProviderCodex},
			ProviderModels: map[string][]server.ModelDTO{
				testProviderCodex: {
					{ID: testModelCodexGPT54},
					{ID: testModelCodexGPT54Mini},
				},
			},
			PhaseDefaults: config.ModelConfig{
				Research:  testModelCodexGPT54,
				Utilities: testModelCodexGPT54Mini,
			},
			PhaseProviderModels: map[string]map[string][]string{
				testActivityResearch: {testProviderCodex: {testModelCodexGPT54}},
				"chat":               {testProviderCodex: {testModelCodexGPT54Mini}},
			},
		},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish}, server.FeatureDetailDTO{

			Pipeline: testPipelineSizeLarge,
		})},
		featureConfig: server.FeatureConfigResponse{
			FeatureID: testFeatureIDActive,
			Current: server.FeatureConfigDTO{
				Models:      config.ModelConfig{Research: testModelCodexGPT54, Utilities: testModelCodexGPT54Mini},
				Inquireness: testInquirenessTargeted,
				Checkpoints: server.CheckpointsDTO{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
				Pipeline:    testPipelineSizeLarge,
			},
			Defaults: server.FeatureConfigDTO{
				Models:      config.ModelConfig{Research: testModelCodexGPT54},
				Inquireness: testInquirenessTargeted,
				Checkpoints: server.CheckpointsDTO{ManualPublish: true},
				Pipeline:    testPipelineSizeLarge,
			},
			Publish: server.PublishabilityDTO{ManualPublish: true, Repos: map[string]bool{testRepoNameAPI: true}},
		},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if cmd == nil {
		t.Fatal("Update(e) returned nil command, want feature config fetch")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	editing := model.(APIAppModel)
	view := stripANSI(editing.View().Content)
	if strings.Contains(view, testSectionLabelUtilities) {
		t.Fatalf("feature config editor exposed global Utilities field:\n%s", view)
	}
}

func TestAPIAppModelNeedUserInputDecisionUsesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDBlocked, Name: testFeatureNameBlockedWork, Slug: "blocked-work", Status: testStatusNeedUserInput, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDBlocked, Name: testFeatureNameBlockedWork, Slug: "blocked-work", Status: testStatusNeedUserInput, CurrentPhase: testPhaseNameImplement}, server.FeatureDetailDTO{

			NeedUserInput: &server.NeedInputGateDTO{
				FeatureID: testFeatureIDBlocked,
				Open:      true,
				Scope:     testActionScopeFeature,
				Iteration: 3,
				Summary:   "Choose a persistence backend before implementation continues.",
				Questions: []server.NeedUserInputQuestionDTO{
					{Index: 1, Prompt: testQuestionWhichDatabase, Answer: testDBOptionPostgres},
				},
			},
		})},
		needUserInputAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	if cmd != nil {
		t.Fatal("Update(i) returned command before need-user-input decision")
	}
	prompting := model.(APIAppModel)
	if prompting.artifactReview == nil || prompting.artifactReview.ReviewMode() != reviewModeNeedUserInput {
		t.Fatalf("Update(i) did not open need-user-input artifact review: %+v", prompting.artifactReview)
	}
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{"Need User Input", "Implementation needs user input", testQuestionWhichDatabase, testDBOptionPostgres, "Ctrl+D: actions menu"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	for _, removed := range []string{"Scope:", "[tab] Next question", "Ctrl+D menu"} {
		if strings.Contains(view, removed) {
			t.Fatalf("API app View() still contains removed modal copy %q in:\n%s", removed, view)
		}
	}
	if len(client.needUserInputFeatureIDs) != 0 {
		t.Fatalf("NeedUserInputDecision calls = %v before decision, want none", client.needUserInputFeatureIDs)
	}

	model, cmd = prompting.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("Ctrl+D returned command before need-user-input decision")
	}
	menuOpen := model.(APIAppModel)
	model, cmd = menuOpen.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter returned nil command, want need-user-input decision message")
	}
	decision := apiTestNeedUserInputDecisionMsg(t, cmd)
	model, cmd = model.(APIAppModel).Update(decision)
	if cmd == nil {
		t.Fatal("NeedUserInputDecisionMsg returned nil command, want REST mutation")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	decided := model.(APIAppModel)

	if got := strings.Join(client.needUserInputFeatureIDs, ","); got != testFeatureIDBlocked {
		t.Fatalf("NeedUserInputDecision calls = %q, want blocked", got)
	}
	if got := client.needUserInputRequests; len(got) != 1 || got[0].Decision != recoveryActionResume || got[0].RepoName != "" || got[0].CycleType != "" {
		t.Fatalf("NeedUserInputDecision requests = %+v, want feature-scoped resume", got)
	}
	if decided.artifactReview != nil {
		t.Fatal("need-user-input artifact review remained open after accepted decision")
	}
	if _, ok := decided.selectedNeedInputGate(testFeatureIDBlocked); ok {
		t.Fatal("resolved need-user-input gate remained selectable from cached feature detail")
	}
	model, _ = decided.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	afterAttach := model.(APIAppModel)
	if afterAttach.artifactReview != nil {
		t.Fatal("Update(a) after resolved need-user-input reopened NUI artifact review")
	}
	view = stripANSI(decided.View().Content)
	for _, want := range []string{"Completed Need Input Decision", testFeatureIDBlocked} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelContextualAOpensNeedUserInputBeforeAttach(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDBlocked, Name: testFeatureNameBlockedWork, Slug: "blocked-work", Status: testStatusNeedUserInput, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDOne, FeatureID: testFeatureIDBlocked, Phase: testPhaseNameImplement, Repo: testRepoNameOrchestrator, Kind: logTabPhase, Status: featureStatusTokenRunning},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{
			ID: testFeatureIDBlocked, Name: testFeatureNameBlockedWork, Slug: "blocked-work", Status: testStatusNeedUserInput, CurrentPhase: testPhaseNameImplement,
		}, server.FeatureDetailDTO{
			NeedUserInput: &server.NeedInputGateDTO{
				FeatureID: testFeatureIDBlocked,
				Open:      true,
				Scope:     testActionScopeFeature,
				Iteration: 1,
				Summary:   "Review the implementation output before continuing.",
				Questions: []server.NeedUserInputQuestionDTO{
					{Index: 1, Prompt: "Does the implementation satisfy the exit criteria?"},
				},
			},
		})},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd != nil {
		t.Fatal("Update(a) returned attach/session command, want local need-user-input prompt")
	}
	prompting := model.(APIAppModel)
	if prompting.artifactReview == nil || prompting.artifactReview.ReviewMode() != reviewModeNeedUserInput {
		t.Fatalf("Update(a) did not open need-user-input artifact review: %+v", prompting.artifactReview)
	}
	if prompting.attach != nil {
		t.Fatal("Update(a) attached to session instead of showing need-user-input prompt")
	}
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{"Need User Input", "Implementation needs user input", "Does the implementation satisfy the exit", "criteria?", "Ctrl+D: actions menu"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	for _, removed := range []string{"Scope:", "[tab] Next question", "Ctrl+D menu"} {
		if strings.Contains(view, removed) {
			t.Fatalf("API app View() still contains removed modal copy %q in:\n%s", removed, view)
		}
	}
}

func TestAPIAppModelNeedUserInputQuestionnaireDraftsAnswersBeforeResume(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDBlocked, Name: testFeatureNameBlockedWork, Slug: "blocked-work", Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: time.Now()},
		}},
		prompts: server.PromptSnapshotResponse{NeedUserInputs: []server.NeedInputGateDTO{
			{
				FeatureID: testFeatureIDBlocked,
				Open:      true,
				Scope:     testActionScopeFeature,
				RepoName:  testRepoNameAPI,
				CycleType: testCycleTypeRebase,
				Iteration: 4,
				Summary:   "Resolve the rebase conflict before implementation continues.",
				Questions: []server.NeedUserInputQuestionDTO{
					{Index: 1, Prompt: "Keep branch behavior or target behavior?"},
				},
			},
		}},
		detail:                     server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDBlocked, Name: testFeatureNameBlockedWork, Slug: "blocked-work", Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish}, server.FeatureDetailDTO{})},
		needUserInputAccepted:      apiTestActionResponse{},
		needUserInputDraftAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	if cmd != nil {
		t.Fatal("Update(i) returned command before need-user-input edit")
	}
	prompting := model.(APIAppModel)
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{
		"Need User Input",
		"Implementation needs user input",
		"Resolve the rebase conflict before implementation",
		"continues.",
		"Keep branch behavior or target behavior?",
		"Ctrl+D: actions menu",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	for _, removed := range []string{"Repo: " + testRepoNameAPI, "Cycle: " + testCycleTypeRebase, "[tab] Next question", "Ctrl+D menu"} {
		if strings.Contains(view, removed) {
			t.Fatalf("API app View() still contains removed modal copy %q in:\n%s", removed, view)
		}
	}

	model, cmd = prompting.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("Ctrl+D returned command before menu decision")
	}
	menuOpen := model.(APIAppModel)
	model, cmd = menuOpen.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter on incomplete resume returned command, want local block")
	}
	blocked := model.(APIAppModel)
	if len(client.needUserInputFeatureIDs) != 0 {
		t.Fatalf("NeedUserInputDecision calls = %v before all answers, want none", client.needUserInputFeatureIDs)
	}
	if view := stripANSI(blocked.View().Content); !strings.Contains(view, "Resume implementation (answer") || !strings.Contains(view, "all questions to enable)") {
		t.Fatalf("blocked need-user-input menu missing disabled resume label:\n%s", view)
	}
	model, cmd = blocked.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatal("Esc returned command while closing need-user-input menu")
	}
	editing := model.(APIAppModel)

	const answer = "Keep branch behavior"
	drafted := editing
	for _, ch := range answer {
		model, cmd = drafted.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		if cmd == nil {
			t.Fatal("typing answer returned nil command, want draft message")
		}
		drafted = apiTestApplyNeedUserInputDraftCmd(t, model.(APIAppModel), cmd)
	}
	if got := client.needUserInputDraftFeatureIDs; len(got) == 0 || got[len(got)-1] != testFeatureIDBlocked {
		t.Fatalf("DraftNeedUserInputAnswers feature IDs = %v, want final blocked draft", got)
	}
	if got := client.needUserInputDraftRequests; len(got) == 0 ||
		got[len(got)-1].RepoName != testRepoNameAPI ||
		got[len(got)-1].CycleType != testCycleTypeRebase ||
		got[len(got)-1].Answers["1"] != answer {
		t.Fatalf("DraftNeedUserInputAnswers requests = %+v, want routed answer draft", got)
	}

	model, cmd = drafted.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("Ctrl+D returned command before final menu decision")
	}
	menuOpen = model.(APIAppModel)
	model, cmd = menuOpen.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on answered resume returned nil command, want need-user-input decision message")
	}
	decision := apiTestNeedUserInputDecisionMsg(t, cmd)
	model, cmd = model.(APIAppModel).Update(decision)
	if cmd == nil {
		t.Fatal("NeedUserInputDecisionMsg returned nil command, want REST mutation")
	}
	model, _ = model.(APIAppModel).Update(cmd())
	decided := model.(APIAppModel)
	if got := client.needUserInputRequests; len(got) != 1 ||
		got[0].Decision != recoveryActionResume ||
		got[0].RepoName != testRepoNameAPI ||
		got[0].CycleType != testCycleTypeRebase {
		t.Fatalf("NeedUserInputDecision requests = %+v, want routed resume", got)
	}
	if decided.artifactReview != nil {
		t.Fatal("need-user-input artifact review remained open after accepted decision")
	}
	if _, ok := decided.selectedNeedInputGate(testFeatureIDBlocked); ok {
		t.Fatal("resolved need-user-input gate remained selectable from prompt snapshot")
	}
	model, _ = decided.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	afterAttach := model.(APIAppModel)
	if afterAttach.artifactReview != nil {
		t.Fatal("Update(a) after resolved need-user-input reopened NUI artifact review")
	}
}

func apiTestNeedUserInputDecisionMsg(t *testing.T, cmd tea.Cmd) NeedUserInputDecisionMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("command is nil")
	}
	var out []NeedUserInputDecisionMsg
	apiTestCollectNeedUserInputDecisionMsgs(cmd(), &out)
	if len(out) != 1 {
		t.Fatalf("command produced %d NeedUserInputDecisionMsg messages, want 1: %+v", len(out), out)
	}
	return out[0]
}

func apiTestCollectNeedUserInputDecisionMsgs(msg tea.Msg, out *[]NeedUserInputDecisionMsg) {
	switch msg := msg.(type) {
	case nil:
		return
	case NeedUserInputDecisionMsg:
		*out = append(*out, msg)
	case tea.BatchMsg:
		for _, child := range msg {
			if child != nil {
				apiTestCollectNeedUserInputDecisionMsgs(child(), out)
			}
		}
	}
}

func apiTestApplyNeedUserInputDraftCmd(t *testing.T, model APIAppModel, cmd tea.Cmd) APIAppModel {
	t.Helper()
	if cmd == nil {
		t.Fatal("command is nil")
	}
	var drafts []NeedUserInputDraftMsg
	apiTestCollectNeedUserInputDraftMsgs(cmd(), &drafts)
	if len(drafts) == 0 {
		t.Fatal("command did not produce a NeedUserInputDraftMsg")
	}
	for _, draft := range drafts {
		updated, restCmd := model.Update(draft)
		model = updated.(APIAppModel)
		if restCmd == nil {
			t.Fatal("NeedUserInputDraftMsg returned nil command, want REST mutation")
		}
		updated, _ = model.Update(restCmd())
		model = updated.(APIAppModel)
	}
	return model
}

func apiTestCollectNeedUserInputDraftMsgs(msg tea.Msg, out *[]NeedUserInputDraftMsg) {
	switch msg := msg.(type) {
	case nil:
		return
	case NeedUserInputDraftMsg:
		*out = append(*out, msg)
	case tea.BatchMsg:
		for _, child := range msg {
			if child != nil {
				apiTestCollectNeedUserInputDraftMsgs(child(), out)
			}
		}
	}
}

func TestAPIAppModelPermissionAnswerUsesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		permissions: server.PermissionSnapshotResponse{Requests: []server.ControlRequestDTO{
			{RequestID: testPermissionRequestIDPerm1, SessionID: testSessionIDOne, FeatureID: testFeatureIDActive, ToolName: toolNameBash, Status: testStatusPending, Summary: testShellCommandGoTest},
		}},
		permissionAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	prompting := model.(APIAppModel)
	if !apiTestShowingPermissionPrompt(prompting) {
		t.Fatal("Update(a) did not enter attach permission prompt")
	}
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{"Allow Bash?", testFeatureNameActiveWork, testShellCommandGoTest, "[y] Allow", "[n] Deny"} {
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

	if got := client.permissionAnswers; len(got) != 1 || got[0].RequestID != testPermissionRequestIDPerm1 || got[0].SessionID != testSessionIDOne || got[0].Decision != permission.DecisionAllowOnce {
		t.Fatalf("AnswerPermission requests = %+v, want perm-1/sess-1 %s", got, permission.DecisionAllowOnce)
	}
	if apiTestShowingPermissionPrompt(answered) {
		t.Fatal("permission prompt remained open after accepted answer")
	}
	view = stripANSI(answered.View().Content)
	for _, want := range []string{testFeatureNameActiveWork, "Type a message"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelHelpMessageUsesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		prompts: server.PromptSnapshotResponse{HelpQueue: []server.HelpQueueDTO{
			{FeatureID: testFeatureIDActive, Question: "Which implementation path?", Pending: true},
		}},
		helpAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if cmd != nil {
		t.Fatal("Update(h) returned command before help answer")
	}
	prompting := model.(APIAppModel)
	if !prompting.helpPromptActive {
		t.Fatal("Update(h) did not show help answer prompt")
	}
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{"Help request", testFeatureNameActiveWork, "Which implementation path?", "Answer:"} {
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

	if got := client.helpRequests; len(got) != 1 || got[0].FeatureID != testFeatureIDActive || got[0].SessionID != "" || got[0].Message != "use codex" {
		t.Fatalf("SendHelp requests = %+v, want feature-scoped message", got)
	}
	if answered.helpPromptActive {
		t.Fatal("help prompt remained open after accepted answer")
	}
	view = stripANSI(answered.View().Content)
	for _, want := range []string{"Completed Help Reply", testFeatureIDActive} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelAskUserAnswerUsesRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		prompts: server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{
			{
				RequestID: testAskRequestID,
				SessionID: testSessionIDOne,
				FeatureID: testFeatureIDActive,
				ToolName:  toolNameAskUserQuestion,
				Status:    testStatusPending,
				Summary:   testQuestionWhichDatabase,
				Questions: []server.AskUserQuestionDTO{{Question: testQuestionWhichDatabase}},
			},
		}},
		askUserAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	prompting := model.(APIAppModel)
	if !apiTestShowingAskUserPrompt(prompting) {
		t.Fatal("Update(a) did not enter attach ask-user prompt")
	}
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{testQuestionWhichDatabase, "Type your answer", "Enter"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
	if len(client.askUserAnswers) != 0 {
		t.Fatalf("AnswerAskUser calls = %v before answer, want none", client.askUserAnswers)
	}

	for _, ch := range testDBOptionPostgres {
		model, _ = prompting.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
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

	if got := client.askUserAnswers; len(got) != 1 || got[0].RequestID != testAskRequestID || got[0].SessionID != testSessionIDOne || got[0].Answers[testQuestionWhichDatabase] != testDBOptionPostgres {
		t.Fatalf("AnswerAskUser requests = %+v, want ask-1/sess-1 answer keyed by full question", got)
	}
	if apiTestShowingAskUserPrompt(answered) {
		t.Fatal("ask-user question remained active after accepted answer")
	}
	view = stripANSI(answered.View().Content)
	for _, want := range []string{"[you] PostgreSQL", testFeatureNameActiveWork} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAttachSessionRendersRestoredLocalUserTranscriptAsYou(t *testing.T) {
	t.Parallel()

	sess := newAPIAttachSession(nil, apiTestSessionDetail(
		server.SessionSummaryDTO{
			ID:        testSessionIDOne,
			FeatureID: testFeatureIDActive,
			Phase:     testPhaseNameImplement,
			Kind:      logTabPhase,
			Status:    testSessionStatusRunning,
		}),

		server.TranscriptResponse{
			Messages: []server.TranscriptMessageDTO{
				{Index: 0, Role: roleAssistant, Type: testMessageTypeText, Text: testQuestionWhichDatabase},
				{Index: 1, Role: testMessageRoleUser, Type: testMessageTypeText, Text: testDBOptionPostgres, LocallyAppended: true},
				{Index: 2, Role: roleAssistant, Type: testMessageTypeText, Text: testFeatureNameActiveWork},
			},
		}, nil)

	m := attachModelFromSession(sess, 120, 40)
	view := stripANSI(m.View())
	for _, want := range []string{"[you] PostgreSQL", testFeatureNameActiveWork} {
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
	sess := newAPIAttachSession(nil, apiTestSessionDetail(
		server.SessionSummaryDTO{
			ID:        testSessionIDOne,
			FeatureID: testFeatureIDActive,
			Phase:     testPhaseNameImplement,
			Kind:      logTabPhase,
			Status:    testSessionStatusRunning,
		}),

		transcript, nil)

	view := stripANSI(renderAttachMessages(sess.MessageLog().Messages(), filterAll, 120, nil))
	for _, want := range []string{testActionInputNamePrompt, "Translate README in Neapolitan.", "[you] Replace the existing README"} {
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
	if got, want := payload["command"], testShellCommandGoTest; got != want {
		t.Fatalf("payload[command] = %q; want %q", got, want)
	}
	if payload["summary"] != "" {
		t.Fatalf("payload[summary] = %q; want raw tool input without summary fallback", payload["summary"])
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
	sess := newAPIAttachSession(nil, apiTestSessionDetail(
		server.SessionSummaryDTO{
			ID:        testSessionIDOne,
			FeatureID: testFeatureIDActive,
			Phase:     testPhaseNameImplement,
			Kind:      logTabPhase,
			Status:    testSessionStatusRunning,
		}),

		transcript, nil)

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
	sess := newAPIAttachSession(nil, apiTestSessionDetail(
		server.SessionSummaryDTO{
			ID:        testSessionIDOne,
			FeatureID: testFeatureIDActive,
			Phase:     testPhaseNameImplement,
			Kind:      logTabPhase,
			Status:    testSessionStatusRunning,
		}),

		transcript, nil)

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
	sess := newAPIAttachSession(client, apiTestSessionDetail(
		server.SessionSummaryDTO{
			ID:        testSessionIDOne,
			FeatureID: testFeatureIDActive,
			Phase:     testPhaseNameImplement,
			Kind:      logTabPhase,
			Status:    testSessionStatusRunning,
		}),

		server.TranscriptResponse{}, nil)
	sess.MessageLog().Append(llm.SDKMessage{
		Type:            testMessageRoleUser,
		LocallyAppended: true,
		User: &llm.UserMessage{
			Message: llm.ConversationMsg{
				Role:    testMessageRoleUser,
				Content: []llm.ContentBlock{{Type: testMessageTypeText, Text: testDBOptionPostgres}},
			},
		},
	})
	if err := sess.RespondToAskUser("ask-1", json.RawMessage(`{"questions":[{"question":"Which database?"}]}`), map[string]string{
		testQuestionWhichDatabase: testDBOptionPostgres,
	}, nil); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}

	newMessages := sess.applyAPISessionSnapshot(apiTestSessionDetail(
		server.SessionSummaryDTO{
			ID:        testSessionIDOne,
			FeatureID: testFeatureIDActive,
			Phase:     testPhaseNameImplement,
			Kind:      logTabPhase,
			Status:    testSessionStatusRunning,
		}),

		&server.TranscriptResponse{
			Messages: []server.TranscriptMessageDTO{
				{Index: 0, Role: testMessageRoleUser, Type: testMessageTypeText, Text: testDBOptionPostgres, LocallyAppended: true},
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
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		prompts: server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{
			{
				RequestID: testAskRequestID,
				FeatureID: testFeatureIDActive,
				ToolName:  toolNameAskUserQuestion,
				Status:    testStatusPending,
				Summary:   displayQuestion,
				Input: map[string]any{
					testInputKeyQuestions: []any{
						map[string]any{
							testInputKeyQuestion: fullQuestion,
							"options":            []any{},
						},
					},
				},
				Questions: []server.AskUserQuestionDTO{{Question: displayQuestion}},
			},
		}},
		askUserAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, _ := app.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	prompting := model.(APIAppModel)
	if !apiTestShowingAskUserPrompt(prompting) {
		t.Fatal("Update(u) did not enter API ask-user prompt")
	}
	var cmd tea.Cmd
	for _, ch := range "Use the full input" {
		model, _ = prompting.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
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
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		prompts: server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{
			{
				RequestID: testAskRequestID,
				SessionID: testSessionIDOne,
				FeatureID: testFeatureIDActive,
				ToolName:  toolNameAskUserQuestion,
				Status:    testStatusPending,
				Summary:   testQuestionWhichDatabase,
				Questions: []server.AskUserQuestionDTO{{
					Question: testQuestionWhichDatabase,
					Options: []server.AskUserOptionDTO{
						{Label: testDBOptionPostgres, Description: "relational"},
						{Label: testDBOptionDynamoDB, Description: "managed key-value"},
					},
				}},
			},
		}},
		askUserAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, _ := app.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	prompting := model.(APIAppModel)
	view := stripANSI(prompting.View().Content)
	for _, want := range []string{testQuestionWhichDatabase, testDBOptionPostgres, "relational", testDBOptionDynamoDB, "managed key-value"} {
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
	if got := client.askUserAnswers; len(got) != 1 || got[0].Answers[testQuestionWhichDatabase] != testDBOptionDynamoDB {
		t.Fatalf("AnswerAskUser requests = %+v, want DynamoDB answer", got)
	}
}

func TestAPIAppModelChatPromptOnlyAskUserSnapshotShowsReadableLongText(t *testing.T) {
	t.Parallel()

	longQuestion := "Should TUI/UI label names that match what is displayed on screen, including In Progress, Published, Watch, Answer, Approve, and Publish as PR, be translated into the target language or kept in English so the reader can map the README back to the live interface without losing important workflow context?"
	longDescription := "Translate all prose including TUI labels. The README is a localized document, and describing what the screen says in English breaks immersion even though the reader can still match the workflow by position, status, and surrounding context."
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testArtifactIDDesign, CreatedAt: time.Now()},
		}},
	}
	app := newTestAPIAppModel(t, client)
	model, _ := app.Update(tea.WindowSizeMsg{Width: 320, Height: 60})
	app = model.(APIAppModel)
	thinking := startAPIChatTurn(t, app, "ask the full translation policy question")

	askControl := server.ControlRequestDTO{
		RequestID: "ask-long",
		SessionID: chatSessionID,
		FeatureID: chatSessionID,
		ToolName:  toolNameAskUserQuestion,
		Status:    testStatusPending,
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
		testHintEnterToSelect,
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
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: testSessionStatusRunning},
		}},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	attached := model.(APIAppModel)
	if apiTestShowingAskUserPrompt(attached) {
		t.Fatal("attach unexpectedly started with ask-user prompt")
	}

	model, _ = attached.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Prompts: &server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{
			{
				RequestID: testAskRequestID,
				SessionID: testSessionIDOne,
				FeatureID: testFeatureIDActive,
				ToolName:  toolNameAskUserQuestion,
				Status:    testStatusPending,
				Summary:   "Pick database",
				Questions: []server.AskUserQuestionDTO{{Question: "Pick database"}},
			},
		}},
	}})
	prompting := model.(APIAppModel)
	if !apiTestShowingAskUserPrompt(prompting) {
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
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testArtifactIDPlan, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testArtifactIDPlan, Kind: logTabPhase, Status: testSessionStatusWaitingHelp},
		}},
		prompts: server.PromptSnapshotResponse{AskUserQuestions: []server.ControlRequestDTO{
			{
				RequestID: testAskRequestID,
				SessionID: testSessionIDOne,
				FeatureID: testFeatureIDActive,
				ToolName:  toolNameAskUserQuestion,
				Status:    testStatusPending,
				Summary:   "Which README?",
				Questions: []server.AskUserQuestionDTO{{
					Question: "Which README?",
					Options:  []server.AskUserOptionDTO{{Label: "Root README"}},
				}},
			},
		}},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	attached := model.(APIAppModel)
	if !apiTestShowingAskUserPrompt(attached) {
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
	if apiTestShowingAskUserPrompt(answered) {
		t.Fatal("AskUser prompt remained active after answer")
	}

	model, _ = answered.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	detached := model.(APIAppModel)
	if detached.attach != nil {
		t.Fatal("Esc should detach from attach view")
	}

	model, _ = detached.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	reattached := model.(APIAppModel)
	if apiTestShowingAskUserPrompt(reattached) {
		t.Fatalf("answered AskUser prompt reactivated from cached prompts:\n%s", stripANSI(reattached.View().Content))
	}
}

func TestAPIAppModelAttachRefreshUpdatesStreamingTranscriptRow(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: testSessionStatusRunning},
		}},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	attached := model.(APIAppModel)
	partialText := "I found the workspace is a"
	fullText := "I found the workspace is a monorepo with README docs."
	firstSnapshot := server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: apiTestSessionDetail(server.SessionSummaryDTO{
			ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: testSessionStatusRunning,
		})},
		Transcript: &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
			{Index: 0, Role: roleAssistant, Type: testMessageTypeText, Text: partialText},
		}},
	}

	model, _ = attached.Update(apiRefreshSnapshotMsg{snapshot: firstSnapshot})
	attached = model.(APIAppModel)
	if view := stripANSI(attached.View().Content); !strings.Contains(view, partialText) {
		t.Fatalf("attach view missing partial transcript row:\n%s", view)
	}

	secondSnapshot := firstSnapshot
	secondSnapshot.Transcript = &server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
		{Index: 0, Role: roleAssistant, Type: testMessageTypeText, Text: fullText},
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

func TestAPIAppModelAttachBackfillsOlderTranscriptOnScrollTop(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: testSessionStatusRunning},
		}},
		transcript: server.TranscriptResponse{
			Cursor:   server.CursorDTO{Total: 100, Start: 0, End: 50},
			Messages: apiTestTranscriptRows(0, 50),
		},
	}
	app := newTestAPIAppModel(t, client)
	app.storeSessionDetail(server.SessionDetailResponse{Session: apiTestSessionDetailWith(server.SessionSummaryDTO{
		ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: testSessionStatusRunning,
	}, server.SessionDetailDTO{

		TranscriptCursor: server.CursorDTO{Total: 100, Start: 0, End: 100},
		CanAttach:        true,
	})})
	app.storeTranscript(testSessionIDOne, server.TranscriptResponse{
		Cursor:   server.CursorDTO{Total: 100, Start: 50, End: 100},
		Messages: apiTestTranscriptRows(50, 100),
	})

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Update(a) returned nil command, want attach init command")
	}
	attached := model.(APIAppModel)
	if attached.attach == nil {
		t.Fatal("attach view was not opened")
	}
	attached.attach.viewport.GotoTop()

	model, cmd = attached.Update(tea.MouseWheelMsg(tea.Mouse{Button: tea.MouseWheelUp}))
	if cmd == nil {
		t.Fatal("scrolling at the top returned nil command, want transcript backfill command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	backfilled := model.(APIAppModel)

	if len(client.transcriptQueries) == 0 {
		t.Fatal("scrolling at top did not fetch older transcript rows")
	}
	gotQuery := client.transcriptQueries[len(client.transcriptQueries)-1]
	if gotQuery.Cursor != 0 || gotQuery.Limit != apiTranscriptPageLimit {
		t.Fatalf("backfill query = %+v, want cursor 0 limit %d", gotQuery, apiTranscriptPageLimit)
	}
	view := stripANSI(backfilled.View().Content)
	if !strings.Contains(view, "transcript row 000") {
		t.Fatalf("attach view missing backfilled transcript rows:\n%s", view)
	}
	sess, ok := backfilled.attach.sess.(*apiSessionView)
	if !ok {
		t.Fatalf("attached session = %T, want *apiSessionView", backfilled.attach.sess)
	}
	text := sess.MessageLog().Text()
	if !strings.Contains(text, "transcript row 000") || !strings.Contains(text, "transcript row 050") {
		t.Fatalf("session log missing backfilled or original transcript rows:\n%s", text)
	}
	if strings.Index(text, "transcript row 000") > strings.Index(text, "transcript row 050") {
		t.Fatalf("backfilled transcript rows stored after existing rows:\n%s", text)
	}
}

func apiTestTranscriptRows(start, end int) []server.TranscriptMessageDTO {
	rows := make([]server.TranscriptMessageDTO, 0, end-start)
	for i := start; i < end; i++ {
		rows = append(rows, server.TranscriptMessageDTO{
			Index: i,
			Role:  roleAssistant,
			Type:  testMessageTypeText,
			Text:  fmt.Sprintf("transcript row %03d", i),
		})
	}
	return rows
}

func TestAPIAppModelAttachRendersSessionInitialPrompt(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: testSessionStatusRunning},
		}},
	}
	app := newTestAPIAppModel(t, client)
	app.storeSessionDetail(server.SessionDetailResponse{Session: apiTestSessionDetailWith(server.SessionSummaryDTO{
		ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: testSessionStatusRunning,
	}, server.SessionDetailDTO{

		InitialPrompt: "Implement the user-visible attach header.",
	})})
	app.storeTranscript(testSessionIDOne, server.TranscriptResponse{Messages: []server.TranscriptMessageDTO{
		{Index: 0, Role: roleAssistant, Type: testMessageTypeText, Text: "Working on it."},
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

func TestAPIAppModelAttachBackfillsInitialPromptFromRefresh(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		sessions: server.SessionListResponse{Sessions: []server.SessionSummaryDTO{
			{ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: testSessionStatusRunning},
		}},
	}
	app := newTestAPIAppModel(t, client)
	app.selectedFeature = testFeatureIDActive

	model, _ := app.openAPIAttachForFeature(testFeatureIDActive)
	attached := model.(APIAppModel)
	if attached.attach == nil {
		t.Fatal("expected attach model to open")
	}
	if view := stripANSI(attached.View().Content); strings.Contains(view, "Repository Context") {
		t.Fatalf("precondition failed: summary-only attach unexpectedly rendered prompt:\n%s", view)
	}

	model, _ = attached.Update(apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
		Session: &server.SessionDetailResponse{Session: apiTestSessionDetailWith(server.SessionSummaryDTO{
			ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Kind: logTabPhase, Status: testSessionStatusRunning,
		}, server.SessionDetailDTO{

			InitialPrompt: "# Repository Context\n\nBuild the repo knowledge base.",
			CanAttach:     true,
		})},
	}})
	view := stripANSI(model.(APIAppModel).View().Content)
	if !strings.Contains(view, "# Repository Context") {
		t.Fatalf("attach view did not backfill initial prompt from session detail refresh:\n%s", view)
	}
}

func TestAPIAppModelFetchAttachSessionDetailCmdReturnsRefreshSnapshot(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		sessionDetailsByID: map[string]server.SessionDetailResponse{
			testSessionIDOne: {Session: apiTestSessionDetailWith(server.SessionSummaryDTO{ID: testSessionIDOne, FeatureID: testFeatureIDActive, Phase: testPhaseNameImplement, Status: testSessionStatusRunning}, server.SessionDetailDTO{

				InitialPrompt:    "# Repository Context\n\nBuild the repo knowledge base.",
				TranscriptCursor: server.CursorDTO{End: 2, Total: 2},
			})},
		},
		transcriptsByID: map[string]server.TranscriptResponse{
			testSessionIDOne: {Messages: []server.TranscriptMessageDTO{
				{Index: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "Working on it."},
			}},
		},
	}
	app := newTestAPIAppModel(t, client)

	cmd := app.fetchAttachSessionDetailCmd(testSessionIDOne)
	if cmd == nil {
		t.Fatal("fetchAttachSessionDetailCmd returned nil")
	}
	msg, ok := cmd().(apiRefreshSnapshotMsg)
	if !ok {
		t.Fatalf("command returned %T, want apiRefreshSnapshotMsg", msg)
	}
	if msg.err != nil {
		t.Fatalf("command returned error: %v", msg.err)
	}
	if msg.snapshot.Session == nil || msg.snapshot.Session.Session.InitialPrompt == "" {
		t.Fatalf("snapshot missing session initial prompt: %#v", msg.snapshot.Session)
	}
	if msg.snapshot.Transcript == nil || len(msg.snapshot.Transcript.Messages) != 1 {
		t.Fatalf("snapshot missing transcript tail: %#v", msg.snapshot.Transcript)
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
			msg:  PlanReviewDecisionMsg{FeatureID: testFeatureIDActive, Decision: reviewDecisionProceed},
			detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetail(
				server.FeatureSummary{
					ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusPlanNeedsReview, CurrentPhase: testArtifactIDPlan,
					Progress: server.FeatureProgress{CurrentRoadmapPhase: 2, TotalRoadmapPhases: 3},
				})},
			wantReq: server.ReviewDecisionRequest{Decision: reviewDecisionProceed, PhasePlan: true},
		},
		{
			name: "roadmap reject iterates with comment",
			msg:  RoadmapReviewDecisionMsg{FeatureID: testFeatureIDActive, Decision: "reject", Comment: "Needs clearer slices"},
			wantReq: server.ReviewDecisionRequest{
				Decision: reviewDecisionIterate,
				Roadmap:  true,
				Comment:  "Needs clearer slices",
			},
		},
		{
			name: "gate proceeds with target phase",
			msg:  GateReviewDecisionMsg{FeatureID: testFeatureIDActive, Phase: feature.PhaseImplement, Decision: reviewDecisionProceed},
			wantReq: server.ReviewDecisionRequest{
				Decision: reviewDecisionProceed,
				Phase:    testPhaseNameImplement,
			},
		},
		{
			name: "rewind proceeds with target phase",
			msg:  RewindReviewDecisionMsg{FeatureID: testFeatureIDActive, Phase: feature.PhasePlan, Decision: reviewDecisionProceed},
			wantReq: server.ReviewDecisionRequest{
				Decision: reviewDecisionProceed,
				Phase:    testArtifactIDPlan,
				IsRewind: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.detail.Feature.ID == "" {
				tt.detail = server.FeatureDetailResponse{Feature: apiTestFeatureDetail(
					server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusPlanNeedsReview, CurrentPhase: testArtifactIDPlan})}
			}
			client := &fakeTUIAPIClient{
				features: server.FeatureListResponse{Features: []server.FeatureSummary{
					{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusPlanNeedsReview, CurrentPhase: testArtifactIDPlan, CreatedAt: time.Now()},
				}},
				detail:         tt.detail,
				reviewAccepted: apiTestActionResponse{},
			}
			app := newTestAPIAppModel(t, client)

			model, cmd := app.Update(tt.msg)
			if cmd == nil {
				t.Fatal("Update(review decision) returned nil command, want review-decision REST mutation command")
			}
			msg := cmd()
			model, _ = model.(APIAppModel).Update(msg)
			reviewed := model.(APIAppModel)

			if got := strings.Join(client.reviewFeatureIDs, ","); got != testFeatureIDActive {
				t.Fatalf("ReviewDecision feature IDs = %q, want active", got)
			}
			if len(client.reviewRequests) != 1 || client.reviewRequests[0] != tt.wantReq {
				t.Fatalf("ReviewDecision requests = %+v, want %+v", client.reviewRequests, tt.wantReq)
			}
			view := stripANSI(reviewed.View().Content)
			for _, want := range []string{"Completed Review Decision", testFeatureIDActive} {
				if !strings.Contains(view, want) {
					t.Fatalf("API app View() missing %q in:\n%s", want, view)
				}
			}
		})
	}
}

func TestAPIAppModelContextualActionOpensNeedsReviewArtifact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		buildClient    func(t *testing.T, tmp string) (*fakeTUIAPIClient, string)
		wantReviewHint string
		wantReviewMode string
		wantArtifactID string
		extraDownKey   bool
		wantDecision   string
	}{
		{
			name: "small pipeline plan roadmap review",
			buildClient: func(t *testing.T, tmp string) (*fakeTUIAPIClient, string) {
				client := &fakeTUIAPIClient{
					features: server.FeatureListResponse{Features: []server.FeatureSummary{
						{
							ID:           testFeatureIDActive,
							Name:         testFeatureNameTranslateReadme,
							Slug:         testFeatureSlugTranslateReadme,
							Status:       testFeatureStatusPlanNeedsReview,
							CurrentPhase: testArtifactIDPlan,
							ActiveRun:    1,
							CreatedAt:    time.Now(),
							Progress: server.FeatureProgress{
								CurrentRoadmapPhase: 0,
								TotalRoadmapPhases:  3,
							},
						},
					}},
					detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{
						ID:           testFeatureIDActive,
						Name:         testFeatureNameTranslateReadme,
						Slug:         testFeatureSlugTranslateReadme,
						Status:       testFeatureStatusPlanNeedsReview,
						CurrentPhase: testArtifactIDPlan,
						ActiveRun:    1,
						Progress: server.FeatureProgress{
							CurrentRoadmapPhase: 0,
							TotalRoadmapPhases:  3,
						},
					}, server.FeatureDetailDTO{

						ActiveRunDetail: &server.RunSummaryDTO{
							RunNumber:     1,
							CurrentPhase:  testArtifactIDPlan,
							RoadmapPhase:  0,
							RoadmapTotal:  3,
							ArtifactCount: 1,
						},
						Models: config.ModelConfig{Utilities: testUtilityModelID},
					})},
					reviewSession: server.ReviewSessionResponse{
						FeatureID:      testFeatureIDActive,
						ReviewID:       "review-roadmap",
						ReviewMode:     testArtifactIDPlan,
						TargetPhase:    feature.PhaseImplement.DirName(),
						RunNumber:      1,
						ArtifactID:     testPipelineRoadmap,
						Text:           "# Roadmap\n\nTranslate README.\n",
						DraftRevision:  "rev-1",
						SourceRevision: "source-rev-1",
						CanIterate:     true,
					},
				}
				return client, testPipelineRoadmap
			},
			wantReviewHint: "Roadmap needs review",
			wantReviewMode: testArtifactIDPlan,
			wantArtifactID: testPipelineRoadmap,
			extraDownKey:   true,
			wantDecision:   reviewDecisionProceed,
		},
		{
			name: "medium pipeline rewind description review",
			buildClient: func(t *testing.T, tmp string) (*fakeTUIAPIClient, string) {
				client := &fakeTUIAPIClient{
					features: server.FeatureListResponse{Features: []server.FeatureSummary{
						{
							ID:           testFeatureIDActive,
							Name:         testFeatureNameTranslateReadme,
							Slug:         testFeatureSlugTranslateReadme,
							Status:       testFeatureStatusDesignNeedsReview,
							CurrentPhase: testArtifactIDDesign,
							ActiveRun:    2,
							RunCount:     2,
							CreatedAt:    time.Now(),
						},
					}},
					detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{
						ID:           testFeatureIDActive,
						Name:         testFeatureNameTranslateReadme,
						Slug:         testFeatureSlugTranslateReadme,
						Status:       testFeatureStatusDesignNeedsReview,
						CurrentPhase: testArtifactIDDesign,
						ActiveRun:    2,
						RunCount:     2,
					}, server.FeatureDetailDTO{

						Pipeline: testPipelineSizeMedium,
						ActiveRunDetail: &server.RunSummaryDTO{
							RunNumber:          2,
							CurrentPhase:       testArtifactIDDesign,
							PendingReviewPhase: testArtifactIDPlan,
							IsRewind:           true,
							ArtifactCount:      1,
						},
						Models: config.ModelConfig{Utilities: testUtilityModelID},
					})},
					reviewSession: server.ReviewSessionResponse{
						FeatureID:      testFeatureIDActive,
						ReviewID:       "review-description",
						ReviewMode:     reviewModeRewind,
						TargetPhase:    feature.PhasePlan.DirName(),
						RunNumber:      2,
						ArtifactID:     artifactIDDescriptionReview,
						Text:           "translate readme in Sicilian",
						DraftRevision:  "rev-1",
						SourceRevision: "source-rev-1",
						CanIterate:     false,
					},
					reviewAccepted: apiTestActionResponse{},
				}
				return client, artifactIDDescriptionReview
			},
			wantReviewHint: "Rewind to Plan needs review",
			wantReviewMode: reviewModeRewind,
			wantArtifactID: artifactIDDescriptionReview,
			extraDownKey:   false,
			wantDecision:   reviewDecisionProceed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			client, _ := tt.buildClient(t, tmp)
			app := newTestAPIAppModel(t, client)

			if view := stripANSI(app.View().Content); !strings.Contains(view, tt.wantReviewHint) {
				t.Fatalf("API app View() missing review hint before action:\n%s", view)
			}

			model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
			if cmd == nil {
				t.Fatal("pressing a returned nil command, want review session create")
			}
			model, _ = model.(APIAppModel).Update(cmd())
			updated := model.(APIAppModel)

			if updated.artifactReview == nil {
				t.Fatalf("pressing a did not open artifact review; statusMessage=%q", updated.statusMessage)
			}
			if len(client.reviewSessionFeatureIDs) == 0 || client.reviewSessionFeatureIDs[len(client.reviewSessionFeatureIDs)-1] != testFeatureIDActive {
				t.Fatalf("CreateReviewSession feature IDs = %+v, want active feature", client.reviewSessionFeatureIDs)
			}
			if got := updated.artifactReview.FeatureID(); got != testFeatureIDActive {
				t.Fatalf("artifactReview.FeatureID() = %q, want active", got)
			}
			if got := updated.artifactReview.ReviewMode(); got != tt.wantReviewMode {
				t.Fatalf("artifactReview.ReviewMode() = %q, want %q", got, tt.wantReviewMode)
			}
			if got := updated.artifactReview.ArtifactID(); got != tt.wantArtifactID {
				t.Fatalf("artifactReview.ArtifactID() = %q, want %q", got, tt.wantArtifactID)
			}
			if strings.Contains(updated.statusMessage, "No contextual action") {
				t.Fatalf("statusMessage = %q, want artifact review opened", updated.statusMessage)
			}

			model, _ = updated.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
			if tt.extraDownKey {
				model, _ = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyDown})
			}
			model, cmd = model.(APIAppModel).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if cmd == nil {
				t.Fatal("artifact review proceed returned nil command")
			}
			msg := cmd()
			model, cmd = model.(APIAppModel).Update(msg)
			if cmd == nil {
				t.Fatal("review decision message returned nil REST command")
			}
			_, _ = model.(APIAppModel).Update(cmd())

			if len(client.saveReviewDraftRequests) != 1 {
				t.Fatalf("SaveReviewDraft requests = %+v, want one", client.saveReviewDraftRequests)
			}
			if len(client.submitReviewDecisionRequests) != 1 {
				t.Fatalf("SubmitReviewSessionDecision requests = %+v, want one", client.submitReviewDecisionRequests)
			}
			if got := client.submitReviewDecisionRequests[0].Decision; got != tt.wantDecision {
				t.Fatalf("review session decision = %q, want %q", got, tt.wantDecision)
			}
		})
	}
}

// newStaleRewindReviewClient builds a fakeTUIAPIClient for a feature awaiting
// rewind review, varying only status/phase/pipeline and the pending artifact.
func newStaleRewindReviewClient(status, phase, pipeline, pendingReviewPhase string, artifact server.ArtifactDTO) *fakeTUIAPIClient {
	return &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{
				ID:           testFeatureIDActive,
				Name:         testFeatureNameTranslateReadme,
				Slug:         testFeatureSlugTranslateReadme,
				Status:       status,
				CurrentPhase: phase,
				ActiveRun:    2,
				RunCount:     2,
				CreatedAt:    time.Now(),
			},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{
			ID:           testFeatureIDActive,
			Name:         testFeatureNameTranslateReadme,
			Slug:         testFeatureSlugTranslateReadme,
			Status:       status,
			CurrentPhase: phase,
			ActiveRun:    2,
			RunCount:     2,
		}, server.FeatureDetailDTO{

			Pipeline: pipeline,
			ActiveRunDetail: &server.RunSummaryDTO{
				RunNumber:          2,
				CurrentPhase:       phase,
				PendingReviewPhase: pendingReviewPhase,
				IsRewind:           true,
				ArtifactCount:      1,
			},
			Models: config.ModelConfig{Utilities: testUtilityModelID},
		})},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{artifact}},
	}
}

// newStaleRewindReviewApp builds an APIAppModel for a feature with a stale
// rewind-prompt review artifact (a roadmap left over from before the rewind),
// shared by the stale-rewind-rejection tests. It returns the app and the path
// of the stale artifact.
func newStaleRewindReviewApp(t *testing.T) (APIAppModel, string) {
	t.Helper()
	tmp := t.TempDir()
	roadmapPath := filepath.Join(tmp, "roadmap.md")
	if err := os.WriteFile(roadmapPath, []byte("# Old Roadmap\n"), 0o644); err != nil {
		t.Fatalf("write stale roadmap artifact: %v", err)
	}

	client := newStaleRewindReviewClient("PromptNeedsReview", "knowledge-base", testPipelineSizeLarge, testPhaseKeyInquire,
		server.ArtifactDTO{ID: testPipelineRoadmap, RunNumber: 2, Phase: testArtifactIDPlan, Path: roadmapPath, Size: 14, ContentAvailable: true})
	return newTestAPIAppModel(t, client), roadmapPath
}

func TestAPIAppModelContextualActionRejectsStaleRewindReviewArtifact(t *testing.T) {
	t.Parallel()

	app, _ := newStaleRewindReviewApp(t)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	updated := model.(APIAppModel)
	if updated.artifactReview != nil {
		t.Fatal("pressing a opened review synchronously; want review session create command")
	}
	if cmd == nil {
		t.Fatalf("pressing a returned nil command; want review session create, statusMessage=%q", updated.statusMessage)
	}
	if updated.statusMessage != statusMsgLoadingReviewArtifact {
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
				ID:           testFeatureIDActive,
				Name:         testFeatureNameTranslateReadme,
				Slug:         testFeatureSlugTranslateReadme,
				Status:       testFeatureStatusResearchNeedsReview,
				CurrentPhase: testPhaseKeyResearch,
				ActiveRun:    1,
				RunCount:     1,
				CreatedAt:    time.Now(),
			},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{
			ID:           testFeatureIDActive,
			Name:         testFeatureNameTranslateReadme,
			Slug:         testFeatureSlugTranslateReadme,
			Status:       testFeatureStatusResearchNeedsReview,
			CurrentPhase: testPhaseKeyResearch,
			ActiveRun:    1,
			RunCount:     1,
		}, server.FeatureDetailDTO{

			Pipeline: testPipelineSizeMoonshot,
			ActiveRunDetail: &server.RunSummaryDTO{
				RunNumber:          1,
				CurrentPhase:       testPhaseKeyResearch,
				PendingReviewPhase: testArtifactIDDesign,
				ArtifactCount:      1,
			},
			Models: config.ModelConfig{Utilities: testUtilityModelID},
		})},
		artifactList: server.ArtifactListResponse{Artifacts: []server.ArtifactDTO{
			{ID: testPhaseKeyInquire, RunNumber: 1, Phase: testPhaseKeyInquire, Path: questionsPath, Size: 12, ContentAvailable: true},
		}},
	}
	app := newTestAPIAppModel(t, client)

	if view := stripANSI(app.View().Content); !strings.Contains(view, "Research needs review") {
		t.Fatalf("API app View() should label the reviewed research artifact, not the design target:\n%s", view)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	updated := model.(APIAppModel)
	if updated.artifactReview != nil {
		t.Fatal("pressing a opened review synchronously; want review session create command")
	}
	if cmd == nil {
		t.Fatalf("pressing a returned nil command; want review session create, statusMessage=%q", updated.statusMessage)
	}
	if updated.statusMessage != statusMsgLoadingReviewArtifact {
		t.Fatalf("statusMessage = %q, want Loading review artifact", updated.statusMessage)
	}
}

func TestAPIAppModelContextualActionRejectsDetachedStaleReviewAfterRewind(t *testing.T) {
	t.Parallel()

	app, _ := newStaleRewindReviewApp(t)
	stale := NewArtifactReviewModel(server.ReviewSessionResponse{
		FeatureID:      testFeatureIDActive,
		ReviewID:       "stale-review",
		ReviewMode:     testArtifactIDPlan,
		TargetPhase:    feature.PhasePlan.DirName(),
		ArtifactID:     testPipelineRoadmap,
		Text:           "# Old Roadmap\n",
		DraftRevision:  "stale-rev",
		SourceRevision: "stale-source",
		CanIterate:     true,
	}, feature.PhasePlan, app.width, app.height)
	stale.detached = true
	app.artifactReview = &stale

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	updated := model.(APIAppModel)
	if updated.artifactReview != nil && !updated.artifactReview.Detached() {
		t.Fatalf("pressing a reattached stale %s review", updated.artifactReview.ReviewMode())
	}
	if cmd == nil {
		t.Fatalf("pressing a returned nil command; want review session create, statusMessage=%q", updated.statusMessage)
	}
	if updated.statusMessage != statusMsgLoadingReviewArtifact {
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

	client := newStaleRewindReviewClient(testFeatureStatusDesignNeedsReview, testArtifactIDDesign, testPipelineSizeMedium, testArtifactIDPlan,
		server.ArtifactDTO{ID: artifactIDDescriptionReview, RunNumber: 2, Phase: testArtifactPhaseDescription, Path: descPath, Size: 27, ContentAvailable: true})
	client.reviewSession = server.ReviewSessionResponse{
		FeatureID:      testFeatureIDActive,
		ReviewID:       "review-description",
		ReviewMode:     reviewModeRewind,
		TargetPhase:    feature.PhasePlan.DirName(),
		RunNumber:      2,
		ArtifactID:     artifactIDDescriptionReview,
		Text:           "translate readme in Sicilian",
		DraftRevision:  "rev-1",
		SourceRevision: "source-rev-1",
	}
	app := newTestAPIAppModel(t, client)
	delete(app.contents, testFeatureIDActive)
	app.rebuildPresentation(testFeatureIDActive)
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
	if got := updated.artifactReview.ReviewMode(); got != reviewModeRewind {
		t.Fatalf("artifactReview.ReviewMode() = %q, want rewind", got)
	}
	if got := updated.artifactReview.ArtifactID(); got != artifactIDDescriptionReview {
		t.Fatalf("artifactReview.ArtifactID() = %q, want %q", got, artifactIDDescriptionReview)
	}
	if got := len(client.artifactListFeatureIDs); got != initialArtifactListCalls {
		t.Fatalf("ArtifactList calls = %d, want unchanged from initial %d", got, initialArtifactListCalls)
	}
}

func TestAPIAppModelReviewCommentsPreviewAndStartUseREST(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish}, server.FeatureDetailDTO{

			RepoStatus: []server.RepoStatusDTO{
				{Name: testRepoNameOrchestrator, Publishable: true},
			},
			Actions: []server.ActionDTO{
				{
					ID:      actionIDReviewComments,
					Enabled: true,
					Scope:   server.ActionScopeDTO{Type: testActionScopeFeature, RepoSelection: "required", CycleType: actionIDReviewComments},
					RequiredInputs: []server.ActionInputDTO{
						{Name: actionInputNameRepo, Kind: testParamKindString, Required: true},
						{Name: "mode", Kind: testParamKindEnum, Required: true, Options: []string{reviewCommentsModeAuto, "address_all"}},
					},
				},
			},
		})},
		reviewCommentsResponse: server.ReviewCommentsFetchResponse{
			FeatureID: testFeatureIDActive,
			Repo:      testRepoNameOrchestrator,
			Comments: []server.ReviewCommentDTO{
				{ID: 101, Type: reviewCommentTypeReview, Path: "internal/tui/api_app.go", Line: 42, Body: "use REST DTOs here", UserLogin: "reviewer", DiffHunk: "@@ -1 +1 @@\n-old\n+new"},
			},
		},
		startReviewCommentsAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)
	model, _ := app.Update(tea.WindowSizeMsg{Width: 180, Height: 48})
	app = model.(APIAppModel)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	if cmd == nil {
		t.Fatal("Update(g) returned nil command, want review-comments fetch command")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	previewing := model.(APIAppModel)

	if got := strings.Join(client.reviewCommentsFeatureIDs, ","); got != testFeatureIDActive {
		t.Fatalf("FetchReviewComments feature IDs = %q, want active", got)
	}
	if got := client.reviewCommentsFetchRequests; len(got) != 1 || got[0].Repo != testRepoNameOrchestrator {
		t.Fatalf("FetchReviewComments requests = %+v, want agentic-orchestrator repo", got)
	}
	if len(client.startReviewCommentsFeatureIDs) != 0 {
		t.Fatalf("StartReviewComments calls = %v before preview confirmation, want none", client.startReviewCommentsFeatureIDs)
	}
	view := stripANSI(previewing.View().Content)
	if strings.Contains(view, dashboardFeaturesPanelTitle) {
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
		helpContextReviewComments,
		testFeatureNameActiveWork,
		testRepoNameOrchestrator,
		"1 pending",
		"1 included",
		"Queue",
		helpContextDetail,
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

	if got := strings.Join(client.startReviewCommentsFeatureIDs, ","); got != testFeatureIDActive {
		t.Fatalf("StartReviewComments feature IDs = %q, want active", got)
	}
	if got := client.startReviewCommentsRequests; len(got) != 1 || got[0].Repo != testRepoNameOrchestrator || got[0].Mode != reviewCommentsModeAuto || len(got[0].Comments) != 1 || got[0].Comments[0].ID != 101 || got[0].Comments[0].DiffHunk == "" {
		t.Fatalf("StartReviewComments requests = %+v, want agentic-orchestrator auto with previewed comment", got)
	}
	if cmd == nil {
		t.Fatal("review-comments mutation result returned nil command, want immediate feature detail refresh")
	}

	cycle := &server.CycleDTO{Type: actionIDReviewComments, Status: featureStatusTokenRunning}
	client.detail = server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, Cycle: cycle, CreatedAt: time.Now(), Repos: []string{testRepoNameOrchestrator}}, server.FeatureDetailDTO{

		Cycle: cycle,
		RepoStatus: []server.RepoStatusDTO{
			{Name: testRepoNameOrchestrator, Touched: true, Publishable: true, CycleType: actionIDReviewComments, CycleStatus: featureStatusTokenRunning},
		},
	})}
	msg = cmd()
	model, _ = started.Update(msg)
	started = model.(APIAppModel)

	view = stripANSI(started.View().Content)
	for _, want := range []string{"Started Review Comments", testFeatureIDActive, "Addressing Review Comments"} {
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

// newRefactorActionClient builds a fakeTUIAPIClient exposing an enabled
// feature-scoped testPipelineRefactor action on an otherwise-published feature.
func newRefactorActionClient() *fakeTUIAPIClient {
	return &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: time.Now(), Repos: []string{testRepoNameOrchestrator}},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish}, server.FeatureDetailDTO{

			RepoStatus: []server.RepoStatusDTO{
				{Name: testRepoNameOrchestrator, Publishable: true},
			},
			Actions: []server.ActionDTO{
				{
					ID:      testPipelineRefactor,
					Enabled: true,
					Scope:   server.ActionScopeDTO{Type: testActionScopeFeature},
					RequiredInputs: []server.ActionInputDTO{
						{Name: actionInputNameRepo, Kind: testParamKindString, Required: false},
						{Name: testActionInputNamePrompt, Kind: testParamKindString, Required: true, MaxLength: server.MaxActionTextBytes},
						{Name: "pipeline", Kind: testParamKindEnum, Required: false, Options: []string{testPipelineSizeMedium, testPipelineSizeLarge, testPipelineSizeMoonshot}},
					},
				},
			},
		})},
		startRefactorAccepted: apiTestActionResponse{},
	}
}

func TestAPIAppModelRefactorPromptSelectsPipelineAndStartsRESTMutation(t *testing.T) {
	t.Parallel()

	client := newRefactorActionClient()
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'F', Text: "F"})
	if cmd != nil {
		t.Fatal("Update(Shift+F) returned command before refactor prompt submit")
	}
	refactor := model.(APIAppModel)
	view := stripANSI(refactor.View().Content)
	for _, want := range []string{"Refactor", testFeatureNameActiveWork, "What changes do you want to make?", "Describe the refactoring for", "agentic-orchestrator...", "ctrl+s"} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in refactor prompt:\n%s", want, view)
		}
	}

	for _, r := range "extract transport boundary" {
		model, _ = refactor.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		refactor = model.(APIAppModel)
	}
	model, cmd = refactor.Update(tea.KeyPressMsg{Code: 's', Text: "s", Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("Update(ctrl+s) returned command before pipeline selection")
	}
	refactor = model.(APIAppModel)
	view = stripANSI(refactor.View().Content)
	for _, want := range []string{"Select Pipeline for Refactor", testPipelineSizeMedium, testPipelineSizeLarge, testPipelineSizeMoonshot, "Inquiry + research + planning", "Confirm"} { //nolint:goconst // "Confirm" here is a substring check against the composite "[enter] Confirm" key hint, coincidentally matching unrelated keybinding-description literals elsewhere
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

	if got := strings.Join(client.startRefactorFeatureIDs, ","); got != testFeatureIDActive {
		t.Fatalf("StartRefactor feature IDs = %q, want active", got)
	}
	if got := client.startRefactorRequests; len(got) != 1 || got[0].Repo != testRepoNameOrchestrator || got[0].Prompt != "extract transport boundary" || got[0].Pipeline != feature.PipelineLarge {
		t.Fatalf("StartRefactor requests = %+v, want agentic-orchestrator prompt with large pipeline", got)
	}
	if cmd == nil {
		t.Fatal("refactor mutation result returned nil command, want immediate feature detail refresh")
	}

	cycle := &server.CycleDTO{Type: testPipelineRefactor, Status: featureStatusTokenRunning}
	client.detail = server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, Cycle: cycle, CreatedAt: time.Now(), Repos: []string{testRepoNameOrchestrator}}, server.FeatureDetailDTO{

		Cycle: cycle,
		RepoStatus: []server.RepoStatusDTO{
			{Name: testRepoNameOrchestrator, Touched: true, Publishable: true, CycleType: testPipelineRefactor, CycleStatus: featureStatusTokenRunning},
		},
	})}
	msg = cmd()
	model, _ = started.Update(msg)
	refreshed := model.(APIAppModel)

	view = stripANSI(refreshed.View().Content)
	for _, want := range []string{"Started Refactor", testFeatureIDActive, testActivityRefactoring} {
		if !strings.Contains(view, want) {
			t.Fatalf("API app View() missing %q in:\n%s", want, view)
		}
	}
}

func TestAPIAppModelRefactorPromptShiftEnterAndTerminalPaste(t *testing.T) {
	t.Parallel()

	client := newRefactorActionClient()
	app := newTestAPIAppModel(t, client)

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
	if got := refactor.refactorPrompt.input.Value(); got != testPasteTextMultiline {
		t.Fatalf("refactor prompt value = %q, want %q", got, testPasteTextMultiline)
	}
}

func TestAPIAppModelRefactorPromptTracksPastedImagesAndFiles(t *testing.T) {
	t.Parallel()

	client := newRefactorActionClient()
	app := newTestAPIAppModel(t, client)

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
	_, cmd = refactor.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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

	client := newRefactorActionClient()
	app := newTestAPIAppModel(t, client)

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
			{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameClientCutover, Slug: testFeatureSlugClientCutover, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement}, server.FeatureDetailDTO{

			Actions: []server.ActionDTO{
				{ID: actionIDDelete, Enabled: false, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}, DisabledReasons: []server.ActionDisabledReasonDTO{
					{Code: featureStatusTokenRunning, Message: "delete is disabled while work is running"},
				}},
			},
		})},
		deleteAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if cmd != nil {
		t.Fatal("Update(d) returned command for disabled delete action")
	}
	updated := model.(APIAppModel)
	if updated.actionConfirmActive {
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
		cycle := &server.CycleDTO{Type: testCycleTypeRebase, Status: featureStatusTokenRunning}
		summary := server.FeatureSummary{
			ID:           testFeatureIDActive,
			Name:         testFeatureNamePublishedWork,
			Slug:         testFeatureSlugPublishedWork,
			Status:       testFeatureStatusPublished,
			CurrentPhase: actionIDPublish,
			Cycle:        cycle,
			CreatedAt:    time.Now(),
			Repos:        []string{testRepoNameAPI},
		}
		client := &fakeTUIAPIClient{
			features: server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
			detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{

				Cycle: cycle,
				RepoStatus: []server.RepoStatusDTO{
					{Name: testRepoNameAPI, Publishable: true, CycleType: testCycleTypeRebase, CycleStatus: featureStatusTokenRunning},
				},
				Actions: []server.ActionDTO{
					{ID: actionIDDelete, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
					{ID: "mark-done", Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
					{ID: reviewModeRewind, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}, RequiredInputs: []server.ActionInputDTO{
						{Name: actionInputNameTargetPhase, Kind: testParamKindEnum, Required: true, Options: []string{testPhaseNameImplement}},
					}},
				},
			})},
			deleteAccepted:   apiTestActionResponse{},
			markDoneAccepted: apiTestActionResponse{},
			rewindAccepted:   apiTestActionResponse{},
		}
		app := newTestAPIAppModel(t, client)
		return app, client
	}

	tests := []struct {
		name       string
		key        tea.KeyPressMsg
		wantStatus string
		assert     func(t *testing.T, client *fakeTUIAPIClient)
	}{
		{
			name:       actionIDDelete,
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
			name:       reviewModeRewind,
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
			if updated.actionConfirmActive {
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
	active := server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusCreated, CurrentPhase: testPhaseNameImplement, CreatedAt: created}
	next := server.FeatureSummary{ID: testFeatureIDNext, Name: "Next work", Slug: "next-work", Status: testFeatureStatusCreated, CurrentPhase: testPhaseNameImplement, CreatedAt: created.Add(-time.Minute)}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{active, next}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(active, server.FeatureDetailDTO{

			Actions: []server.ActionDTO{
				{ID: actionIDDelete, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
			},
		})},
		detailsByID: map[string]server.FeatureDetailResponse{
			testFeatureIDActive: {Feature: apiTestFeatureDetailWith(active, server.FeatureDetailDTO{

				Actions: []server.ActionDTO{
					{ID: actionIDDelete, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
				},
			})},
			testFeatureIDNext: {Feature: apiTestFeatureDetail(next)},
		},
		livePreviewsByID: map[string]server.LivePreviewResponse{
			testFeatureIDActive: {Feature: active},
			testFeatureIDNext:   {Feature: next},
		},
		deleteAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)
	if got := app.selectedFeature; got != testFeatureIDActive {
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

	if got := strings.Join(client.deleteFeatureIDs, ","); got != testFeatureIDActive {
		t.Fatalf("DeleteFeature calls = %q, want active", got)
	}
	if got := updated.selectedFeature; got != testFeatureIDNext {
		t.Fatalf("SelectedFeatureID() = %q, want next after delete", got)
	}
	view := stripANSI(updated.View().Content)
	if strings.Contains(view, testFeatureSlugActiveWork) {
		t.Fatalf("API app View() still shows deleted feature:\n%s", view)
	}
	if !strings.Contains(view, "Completed Delete") {
		t.Fatalf("API app View() missing delete completion status:\n%s", view)
	}
}

func TestAPIAppModelIgnoresStaleDetailErrorForRemovedFeature(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	active := server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusCreated, CurrentPhase: testPhaseNameImplement, CreatedAt: created}
	next := server.FeatureSummary{ID: testFeatureIDNext, Name: "Next work", Slug: "next-work", Status: testFeatureStatusCreated, CurrentPhase: testPhaseNameImplement, CreatedAt: created.Add(-time.Minute)}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{active, next}},
		detail:   server.FeatureDetailResponse{Feature: apiTestFeatureDetail(active)},
		livePreview: server.LivePreviewResponse{
			Feature: active,
		},
	}
	app := newTestAPIAppModel(t, client)
	app.ApplyRefreshSnapshot(server.RefreshSnapshot{
		Features: &server.FeatureListResponse{Features: []server.FeatureSummary{next}},
	})
	if got := app.selectedFeature; got != testFeatureIDNext {
		t.Fatalf("SelectedFeatureID() = %q, want next after refresh removed active", got)
	}

	model, _ := app.Update(apiFeatureDetailMsg{
		featureID: testFeatureIDActive,
		err:       errors.New("api GET /api/v1/features/active: not_found (404): feature not found"),
	})
	updated := model.(APIAppModel)
	if strings.Contains(updated.statusMessage, "Detail refresh failed") {
		t.Fatalf("statusMessage = %q, want stale detail error ignored", updated.statusMessage)
	}
	if got := updated.selectedFeature; got != testFeatureIDNext {
		t.Fatalf("SelectedFeatureID() = %q, want next after stale detail error", got)
	}
}

func TestAPIAppModelListRefreshClearsStalePublishedCycleDetail(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	published := server.FeatureSummary{
		ID:           testFeatureIDActive,
		Name:         testFeatureNameActiveWork,
		Slug:         testFeatureSlugActiveWork,
		Status:       testFeatureStatusPublished,
		CurrentPhase: actionIDPublish,
		Repos:        []string{testRepoNameOrchestrator},
		CreatedAt:    created,
		Progress:     server.FeatureProgress{CurrentIteration: 2},
	}
	staleDetail := server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(published, server.FeatureDetailDTO{

		Cycle: &server.CycleDTO{Type: testPipelineRefactor, Status: featureStatusTokenRunning, Count: 1},
		RepoStatus: []server.RepoStatusDTO{{
			Name:        testRepoNameOrchestrator,
			CycleType:   testPipelineRefactor,
			CycleStatus: featureStatusTokenRunning,
			Touched:     true,
		}},
	})}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{published}},
		detail:   staleDetail,
	}
	app := newTestAPIAppModel(t, client)

	app.ApplyRefreshSnapshot(server.RefreshSnapshot{
		Features: &server.FeatureListResponse{Features: []server.FeatureSummary{published}},
	})
	view := stripANSI(app.View().Content)
	if strings.Contains(view, testSectionLabelInProgress) || strings.Contains(view, testActivityRefactoring) {
		t.Fatalf("list-only refresh kept stale refactor cycle in dashboard:\n%s", view)
	}
	if !strings.Contains(view, "PUBLISHED") || !strings.Contains(view, testFeatureSlugActiveWork) {
		t.Fatalf("list-only refresh did not render feature as published:\n%s", view)
	}
}

func TestAPIAppModelPublishOpensReviewFlowBeforeRESTMutation(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDReady, Name: testFeatureNameReadyPublish, Slug: testFeatureSlugReadyToPublish, Status: testFeatureStatusCodeReady, CurrentPhase: actionIDPublish, Repos: []string{testRepoNameAPI}, CreatedAt: time.Now(), Checkpoints: server.CheckpointsDTO{ManualPublish: true}},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDReady, Name: testFeatureNameReadyPublish, Slug: testFeatureSlugReadyToPublish, Status: testFeatureStatusCodeReady, CurrentPhase: actionIDPublish, Repos: []string{testRepoNameAPI}, Checkpoints: server.CheckpointsDTO{ManualPublish: true}}, server.FeatureDetailDTO{

			RepoStatus: []server.RepoStatusDTO{
				{Name: testRepoNameAPI, Publishable: true, Touched: true},
			},
			Actions: []server.ActionDTO{
				{ID: actionIDPublish, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
			},
		})},
		runtime: server.RuntimeConfigResponse{
			Repos: []server.ConfigRepoDTO{{Name: testRepoNameAPI, Path: "/tmp/api"}},
		},
		publishDescriptionTitle: "AI: Ready to publish",
		publishDescriptionBody:  "AI generated commit summary with implementation details.",
		publishAccepted:         apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

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
	if got := client.publishDescriptionFeatureIDs; strings.Join(got, ",") != testFeatureIDReady {
		t.Fatalf("GeneratePublishDescription feature IDs = %v, want ready", got)
	}
	if got := client.publishDescriptionRequests; len(got) != 1 || got[0].FeatureName != testFeatureNameReadyPublish || strings.TrimSpace(got[0].Model) == "" {
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

	if got := strings.Join(client.publishFeatureIDs, ","); got != testFeatureIDReady {
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
		ID:            testFeatureIDReady,
		Name:          testFeatureNameReadyPublish,
		Slug:          testFeatureSlugReadyToPublish,
		Status:        feature.StatusCodeReady,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now(),
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{{
			Name:         testRepoNameAPI,
			Path:         repo,
			WorktreePath: repo,
			Branch:       "feature/ready-to-publish",
			BaseBranch:   testGitBranchMain,
		}},
	}); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDReady, Name: testFeatureNameReadyPublish, Slug: testFeatureSlugReadyToPublish, Status: testFeatureStatusCodeReady, CurrentPhase: actionIDPublish, Repos: []string{testRepoNameAPI}, CreatedAt: time.Now(), Checkpoints: server.CheckpointsDTO{ManualPublish: true}},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDReady, Name: testFeatureNameReadyPublish, Slug: testFeatureSlugReadyToPublish, Status: testFeatureStatusCodeReady, CurrentPhase: actionIDPublish, Repos: []string{testRepoNameAPI}, Checkpoints: server.CheckpointsDTO{ManualPublish: true}}, server.FeatureDetailDTO{

			RepoStatus: []server.RepoStatusDTO{
				{Name: testRepoNameAPI, Publishable: true, Touched: true},
			},
			Actions: []server.ActionDTO{
				{ID: actionIDPublish, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
			},
		})},
		runtime: server.RuntimeConfigResponse{
			Runtime: server.RuntimeIdentity{StateDir: stateDir},
			Repos:   []server.ConfigRepoDTO{{Name: testRepoNameAPI, Path: repo}},
		},
	}
	app := newTestAPIAppModel(t, client)

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
	if !strings.Contains(view, testFeatureNameReadyPublish) {
		t.Fatalf("publish commit log page is empty or missing the pre-publish commit:\n%s", view)
	}
}

func TestAPIAppModelPublishRepoSelectorSendsSelectedRepos(t *testing.T) {
	t.Parallel()

	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDReady, Name: testFeatureNameReadyPublish, Slug: testFeatureSlugReadyToPublish, Status: testFeatureStatusCodeReady, CurrentPhase: actionIDPublish, Repos: []string{testRepoNameAPI, testRepoNameWeb}, CreatedAt: time.Now(), Checkpoints: server.CheckpointsDTO{ManualPublish: true}},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDReady, Name: testFeatureNameReadyPublish, Slug: testFeatureSlugReadyToPublish, Status: testFeatureStatusCodeReady, CurrentPhase: actionIDPublish, Repos: []string{testRepoNameAPI, testRepoNameWeb}, Checkpoints: server.CheckpointsDTO{ManualPublish: true}}, server.FeatureDetailDTO{

			RepoStatus: []server.RepoStatusDTO{
				{Name: testRepoNameAPI, Publishable: true},
				{Name: testRepoNameWeb, Publishable: true},
			},
			Actions: []server.ActionDTO{
				{ID: actionIDPublish, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
			},
		})},
		publishAccepted: apiTestActionResponse{},
	}
	app := newTestAPIAppModel(t, client)

	model, cmd := app.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if cmd != nil {
		t.Fatal("Update(p) returned command before repo selection")
	}
	selecting := model.(APIAppModel)
	view := stripANSI(selecting.View().Content)
	for _, want := range []string{"Publish Feature", "Select Repository", testRepoNameAPI, testRepoNameWeb} {
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

	confirming := walkAPIPublishReviewToConfirmation(t, reviewing)
	model, cmd = confirming.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter on final confirmation) returned nil command, want publish mutation")
	}
	_, _ = model.(APIAppModel).Update(cmd())
	if got := client.publishRequests; len(got) != 1 || !slices.Equal(got[0].Repos, []string{testRepoNameWeb}) {
		t.Fatalf("PublishFeature requests = %+v, want repos [web]", got)
	}
}

func TestAPIAppModelRebaseDoesNotOpenRepoSelector(t *testing.T) {
	t.Parallel()

	client := apiRepoSelectorClient(testCycleTypeRebase)
	app := newTestAPIAppModel(t, client)

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
	if got := client.startRebaseRequests; len(got) != 1 {
		t.Fatalf("StartRebase requests = %+v, want one request", got)
	}
}

func TestAPIAppModelReviewAndRefactorRepoSelectorsUseSelectedRepo(t *testing.T) {
	t.Parallel()

	t.Run("review comments", func(t *testing.T) {
		t.Parallel()

		client := apiRepoSelectorClient(actionIDReviewComments)
		client.reviewCommentsResponse = server.ReviewCommentsFetchResponse{FeatureID: testFeatureIDActive, Repo: testRepoNameWeb}
		app := newTestAPIAppModel(t, client)

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
		if got := client.reviewCommentsFetchRequests; len(got) != 1 || got[0].Repo != testRepoNameWeb {
			t.Fatalf("FetchReviewComments requests = %+v, want repo web", got)
		}
	})

	t.Run(testPipelineRefactor, func(t *testing.T) {
		t.Parallel()

		client := apiRepoSelectorClient(testPipelineRefactor)
		app := newTestAPIAppModel(t, client)

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
		if got := client.startRefactorRequests; len(got) != 1 || got[0].Repo != testRepoNameWeb || got[0].Prompt != "split transport" {
			t.Fatalf("StartRefactor requests = %+v, want repo web prompt", got)
		}
	})
}

func TestAPIAppModelRewindPhaseSelectorUsesChosenTarget(t *testing.T) {
	t.Parallel()

	client := newRewindActionClient(reviewCommentTypeReview, []string{testArtifactIDPlan, testPhaseNameImplement}, []string{testPipelineSizeLarge}, server.FeatureProgress{})
	app := newTestAPIAppModel(t, client)

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
	if got := client.rewindRequests; len(got) != 1 || got[0].TargetPhase != testPhaseNameImplement {
		t.Fatalf("RewindFeature requests = %+v, want target implement", got)
	}
}

func TestAPIAppModelRewindPipelineUpgradeUsesUpgradeRequest(t *testing.T) {
	t.Parallel()

	client := newRewindActionClient(reviewCommentTypeReview, []string{testArtifactIDPlan, testPhaseNameImplement}, []string{testPipelineSizeLarge}, server.FeatureProgress{})
	app := newTestAPIAppModel(t, client)

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
	if got := client.rewindRequests; len(got) != 1 || got[0].TargetPhase != testPhaseKeyInquire || got[0].UpgradePipeline != feature.PipelineLarge {
		t.Fatalf("RewindFeature requests = %+v, want inquire with large upgrade", got)
	}
}

func TestAPIAppModelRewindImplementOpensRoadmapPhasePicker(t *testing.T) {
	t.Parallel()

	client := newRewindActionClient(testPhaseNameImplement, []string{testArtifactIDPlan, testPhaseNameImplement}, nil, server.FeatureProgress{CurrentRoadmapPhase: 2, TotalRoadmapPhases: 3})
	app := newTestAPIAppModel(t, client)

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
	if got := client.rewindRequests; len(got) != 1 || got[0].TargetPhase != testPhaseNameImplement || got[0].RoadmapPhase != 2 {
		t.Fatalf("RewindFeature requests = %+v, want implement roadmap phase 2", got)
	}
}

func TestAPIAppModelRewindSingleImplementTargetOpensRoadmapPhasePicker(t *testing.T) {
	t.Parallel()

	client := newRewindActionClient(testPhaseNameImplement, []string{testPhaseNameImplement}, nil, server.FeatureProgress{CurrentRoadmapPhase: 2, TotalRoadmapPhases: 3})
	app := newTestAPIAppModel(t, client)

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

	run1Detail := server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{
		ID:           testFeatureIDActive,
		Name:         testFeatureNameActiveWork,
		Slug:         testFeatureSlugActiveWork,
		Status:       testFeatureStatusImplementing,
		CurrentPhase: testPhaseNameImplement,
		ActiveRun:    1,
		RunCount:     1,
		CreatedAt:    time.Now(),
	}, server.FeatureDetailDTO{

		ActiveRunDetail: &server.RunSummaryDTO{RunNumber: 1, CurrentPhase: testPhaseNameImplement, ArtifactCount: 1},
		Actions: []server.ActionDTO{
			{ID: reviewModeRewind, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
		},
	})}
	run2Detail := server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{
		ID:           testFeatureIDActive,
		Name:         testFeatureNameActiveWork,
		Slug:         testFeatureSlugActiveWork,
		Status:       testFeatureStatusPlanNeedsReview,
		CurrentPhase: testArtifactIDDesign,
		ActiveRun:    2,
		RunCount:     2,
		CreatedAt:    run1Detail.Feature.CreatedAt,
	}, server.FeatureDetailDTO{

		ActiveRunDetail: &server.RunSummaryDTO{
			RunNumber:          2,
			CurrentPhase:       testArtifactIDDesign,
			PendingReviewPhase: testArtifactIDPlan,
			IsRewind:           true,
			ArtifactCount:      1,
		},
		Actions: []server.ActionDTO{
			{ID: reviewModeRewind, Enabled: true, Scope: server.ActionScopeDTO{Type: testActionScopeFeature}},
		},
	})}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{apiFeatureDetailSummary(run1Detail.Feature)}},
		detail:   run1Detail,
		artifactListByRun: map[int]server.ArtifactListResponse{
			1: {Artifacts: []server.ArtifactDTO{
				{ID: testArtifactIDOldPlan, RunNumber: 1, Phase: testArtifactIDPlan, Size: 18, ContentAvailable: true},
			}},
			2: {Artifacts: []server.ArtifactDTO{
				{ID: artifactIDDescriptionReview, RunNumber: 2, Phase: testArtifactPhaseDescription, Size: 24, ContentAvailable: true},
			}},
		},
		artifactContentByID: map[string]server.TextContentResponse{
			testArtifactIDOldPlan:       {ID: testArtifactIDOldPlan, Text: "old run plan artifact", Size: 18},
			artifactIDDescriptionReview: {ID: artifactIDDescriptionReview, Text: "new rewind review artifact", Size: 24},
		},
	}
	app := newTestAPIAppModel(t, client)
	if got := app.snapshot.Content; got == nil || got.RunNumber != 1 || got.Artifact == nil || got.Artifact.ID != testArtifactIDOldPlan {
		t.Fatalf("initial content = %+v, want run 1 old-plan", got)
	}
	staleReview := NewArtifactReviewModel(server.ReviewSessionResponse{
		FeatureID:      testFeatureIDActive,
		ReviewID:       "stale-review",
		ReviewMode:     testArtifactIDPlan,
		TargetPhase:    feature.PhasePlan.DirName(),
		ArtifactID:     testArtifactIDOldPlan,
		Text:           "old run plan artifact",
		DraftRevision:  "stale-rev",
		SourceRevision: "stale-source",
		CanIterate:     true,
	}, feature.PhasePlan, app.width, app.height)
	staleReview.detached = true
	app.artifactReview = &staleReview

	client.detail = run2Detail
	model, cmd := app.Update(apiMutationResultMsg{kind: mutationKindFeatureRewind, featureID: testFeatureIDActive})
	if cmd == nil {
		t.Fatal("rewind mutation result returned nil command, want immediate feature detail refresh")
	}
	afterRewind := model.(APIAppModel)
	if got, ok := afterRewind.contents[testFeatureIDActive]; ok {
		t.Fatalf("rewind mutation retained stale content = %+v, want content cleared until run 2 loads", got)
	}
	if afterRewind.artifactReview != nil {
		t.Fatal("rewind mutation retained stale artifact review")
	}

	msg := cmd()
	model, _ = afterRewind.Update(msg)
	refreshed := model.(APIAppModel)
	if got := refreshed.snapshot.Content; got == nil || got.RunNumber != 2 || got.Artifact == nil || got.Artifact.ID != artifactIDDescriptionReview {
		t.Fatalf("refreshed content = %+v, want run 2 description-review", got)
	}
	if got := refreshed.snapshot.Detail; got == nil || got.ID != testFeatureIDActive {
		t.Fatalf("refreshed detail = %+v, want active feature detail", got)
	}
}

func TestAPIAppModelSelectionFetchesSelectedFeatureDetail(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: time.Now()},
			{ID: testFeatureIDQueued, Name: testFeatureNameQueuedWork, Slug: testFeatureSlugQueuedWork, Status: testFeatureStatusCreated, CurrentPhase: testPhaseKeyResearch, CreatedAt: time.Now().Add(-time.Hour)},
		}},
		detailsByID: map[string]server.FeatureDetailResponse{
			testFeatureIDActive: {Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork}, server.FeatureDetailDTO{

				Description: "Active detail",
			})},
			testFeatureIDQueued: {Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDQueued, Name: testFeatureNameQueuedWork, Slug: testFeatureSlugQueuedWork}, server.FeatureDetailDTO{

				Description: "Queued detail from REST",
			})},
		},
		livePreviewsByID: map[string]server.LivePreviewResponse{
			testFeatureIDActive: {
				Feature:    server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork},
				Activity:   "Active preview",
				Transcript: []server.TranscriptMessageDTO{{Index: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "Active preview tail"}},
			},
			testFeatureIDQueued: {
				Feature:    server.FeatureSummary{ID: testFeatureIDQueued, Name: testFeatureNameQueuedWork, Slug: testFeatureSlugQueuedWork},
				Activity:   "Queued preview",
				Transcript: []server.TranscriptMessageDTO{{Index: 1, Role: roleAssistant, Type: testMessageTypeText, Text: "Queued preview from REST"}},
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

	if got := selected.selectedFeature; got != testFeatureIDQueued {
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
	active := server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: created}
	published := server.FeatureSummary{ID: testFeatureIDPublished, Name: testFeatureNamePublishedWork, Slug: testFeatureSlugPublishedWork, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: created.Add(-time.Minute)}
	done := server.FeatureSummary{ID: testFeatureIDDone, Name: "Done work", Slug: "done-work", Status: testFeatureStatusDone, CurrentPhase: actionIDPublish, CreatedAt: created.Add(-2 * time.Minute)}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{done, published, active}},
		detail:   server.FeatureDetailResponse{Feature: apiTestFeatureDetail(active)},
		livePreview: server.LivePreviewResponse{
			Feature: active,
		},
	}
	app := newTestAPIAppModel(t, client)

	for i := 0; i < 3; i++ {
		model, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		app = model.(APIAppModel)
	}
	if got := app.selectedFeature; got != "" {
		t.Fatalf("SelectedFeatureID() = %q, want no feature while cursor is on completed section", got)
	}
	if got := app.selectedSection; got != testStatusCompleted {
		t.Fatalf("selectedSection = %q, want completed", got)
	}

	model, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(enter on completed section) returned nil command, want runtime config persistence")
	}
	msg := cmd()
	model, _ = model.(APIAppModel).Update(msg)
	app = model.(APIAppModel)
	if !slices.Contains(app.runtimeConfig.UI.CollapsedSections, testStatusCompleted) {
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
	if got := app.selectedSection; got != testStatusCompleted {
		t.Fatalf("selectedSection after down = %q, want completed", got)
	}
	if got := app.selectedFeature; got != "" {
		t.Fatalf("SelectedFeatureID() after down = %q, want still on completed section", got)
	}
}

func TestAPIAppModelDashboardSectionCollapsePersistsThroughREST(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	active := server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusImplementing, CurrentPhase: testPhaseNameImplement, CreatedAt: created}
	published := server.FeatureSummary{ID: testFeatureIDPublished, Name: testFeatureNamePublishedWork, Slug: testFeatureSlugPublishedWork, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: created.Add(-time.Minute)}
	done := server.FeatureSummary{ID: testFeatureIDDone, Name: "Done work", Slug: "done-work", Status: testFeatureStatusDone, CurrentPhase: actionIDPublish, CreatedAt: created.Add(-2 * time.Minute)}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{done, published, active}},
		detail:   server.FeatureDetailResponse{Feature: apiTestFeatureDetail(active)},
		livePreview: server.LivePreviewResponse{
			Feature: active,
		},
	}
	app := newTestAPIAppModel(t, client)

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
	if !slices.Contains(req.UI.CollapsedSections, testStatusCompleted) {
		t.Fatalf("persisted CollapsedSections = %v, want completed", req.UI.CollapsedSections)
	}
	if !slices.Contains(app.runtimeConfig.UI.CollapsedSections, testStatusCompleted) {
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
	if got := model.(APIAppModel).quitOwnedServerPrompt; got {
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
	if got := model.(APIAppModel).quitOwnedServerPrompt; !got {
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
	if updated.quitOwnedServerPrompt {
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
	if shuttingDown.quitOwnedServerPrompt {
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

	_, cmd = updated.Update(shutdownCmd())
	if cmd == nil {
		t.Fatal("owned shutdown completion did not return quit command")
	}
}

func apiTestShowingPermissionPrompt(m APIAppModel) bool {
	return m.permissionPromptActive || (m.attach != nil && m.attach.showPermMenu)
}

func apiTestShowingAskUserPrompt(m APIAppModel) bool {
	return m.askUserPromptActive || (m.attach != nil && m.attach.HasActiveQuestion())
}

func apiRepoSelectorClient(actionID string) *fakeTUIAPIClient {
	accepted := apiTestActionResponse{}
	action := server.ActionDTO{
		ID:      actionID,
		Enabled: true,
		Scope:   server.ActionScopeDTO{Type: testActionScopeFeature, RepoSelection: "optional"},
	}
	switch actionID {
	case testCycleTypeRebase:
		action.Scope = server.ActionScopeDTO{Type: testActionScopeFeature}
	case actionIDReviewComments:
		action.Scope = server.ActionScopeDTO{Type: testActionScopeFeature, RepoSelection: "required", CycleType: actionIDReviewComments}
		action.RequiredInputs = []server.ActionInputDTO{
			{Name: actionInputNameRepo, Kind: testParamKindString, Required: true},
			{Name: "mode", Kind: testParamKindEnum, Required: true, Options: []string{reviewCommentsModeAuto, "address_all"}},
		}
	case testPipelineRefactor:
		action.RequiredInputs = []server.ActionInputDTO{
			{Name: actionInputNameRepo, Kind: testParamKindString, Required: false},
			{Name: testActionInputNamePrompt, Kind: testParamKindString, Required: true, MaxLength: server.MaxActionTextBytes},
			{Name: "pipeline", Kind: testParamKindEnum, Required: false, Options: []string{testPipelineSizeMedium, testPipelineSizeLarge, testPipelineSizeMoonshot}},
		}
	}
	client := &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{
			{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, CreatedAt: time.Now(), Repos: []string{testRepoNameAPI, testRepoNameWeb}},
		}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusPublished, CurrentPhase: actionIDPublish, Repos: []string{testRepoNameAPI, testRepoNameWeb}}, server.FeatureDetailDTO{

			RepoStatus: []server.RepoStatusDTO{
				{Name: testRepoNameAPI, Publishable: true},
				{Name: testRepoNameWeb, Publishable: true},
			},
			Actions: []server.ActionDTO{action},
		})},
		startRebaseAccepted:         accepted,
		startReviewCommentsAccepted: accepted,
		startRefactorAccepted:       accepted,
	}
	return client
}

// newRewindActionClient builds a fakeTUIAPIClient with a single active
// feature carrying a rewind action, varying only the offered target-phase
// options, the offered pipeline-upgrade options (omitted entirely when nil),
// the current phase, and roadmap progress — the dimensions the
// rewind-selector tests exercise.
func newRewindActionClient(currentPhase string, options, upgradeOptions []string, progress server.FeatureProgress) *fakeTUIAPIClient {
	inputs := []server.ActionInputDTO{
		{Name: actionInputNameTargetPhase, Kind: testParamKindEnum, Required: true, Options: options},
	}
	if len(upgradeOptions) > 0 {
		inputs = append(inputs, server.ActionInputDTO{Name: testActionInputNameUpgradePipeline, Kind: testParamKindEnum, Required: false, Options: upgradeOptions})
	}
	summary := server.FeatureSummary{ID: testFeatureIDActive, Name: testFeatureNameActiveWork, Slug: testFeatureSlugActiveWork, Status: testFeatureStatusInterrupted, CurrentPhase: currentPhase, Progress: progress}
	listSummary := summary
	listSummary.CreatedAt = time.Now()
	return &fakeTUIAPIClient{
		features: server.FeatureListResponse{Features: []server.FeatureSummary{listSummary}},
		detail: server.FeatureDetailResponse{Feature: apiTestFeatureDetailWith(summary, server.FeatureDetailDTO{

			Actions: []server.ActionDTO{
				{
					ID:             reviewModeRewind,
					Enabled:        true,
					Scope:          server.ActionScopeDTO{Type: testActionScopeFeature},
					RequiredInputs: inputs,
				},
			},
		})},
		rewindAccepted: apiTestActionResponse{},
	}
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
	calls                          []string
	features                       server.FeatureListResponse
	runtime                        server.RuntimeConfigResponse
	allowEmptyWorkspaceRoots       bool
	catalog                        server.ModelCatalogResponse
	prompts                        server.PromptSnapshotResponse
	permissions                    server.PermissionSnapshotResponse
	sessions                       server.SessionListResponse
	recovery                       server.RecoverySnapshotResponse
	livePreview                    server.LivePreviewResponse
	livePreviewsByID               map[string]server.LivePreviewResponse
	livePreviewFeatureIDs          []string
	executeRecoveryAccepted        apiTestActionResponse
	executeRecoveryErr             error
	executeRecoverySnapshotIDs     []string
	executeRecoveryRequests        []server.RecoveryActionRequest
	refreshSnapshot                server.RefreshSnapshot
	refreshSignals                 []server.RefreshSignal
	startAccepted                  apiTestActionResponse
	startErr                       error
	startFeatureIDs                []string
	createAccepted                 apiTestActionResponse
	createErr                      error
	createRequests                 []server.CreateFeatureRequest
	resumeAccepted                 apiTestActionResponse
	resumeErr                      error
	resumeFeatureIDs               []string
	restartAccepted                apiTestActionResponse
	restartErr                     error
	restartFeatureIDs              []string
	restartRequests                []server.RestartFeatureRequest
	stopAccepted                   apiTestActionResponse
	stopErr                        error
	stopFeatureIDs                 []string
	deleteAccepted                 apiTestActionResponse
	deleteErr                      error
	deleteFeatureIDs               []string
	publishAccepted                apiTestActionResponse
	publishErr                     error
	publishFeatureIDs              []string
	publishRequests                []server.PublishFeatureRequest
	publishDescriptionFeatureIDs   []string
	publishDescriptionRequests     []server.PublishDescriptionRequest
	publishDescriptionTitle        string
	publishDescriptionBody         string
	publishDescriptionErr          error
	mergeAccepted                  apiTestActionResponse
	mergeErr                       error
	mergeFeatureIDs                []string
	retryAccepted                  apiTestActionResponse
	retryErr                       error
	retryFeatureIDs                []string
	markDoneAccepted               apiTestActionResponse
	markDoneErr                    error
	markDoneFeatureIDs             []string
	cleanupAccepted                apiTestActionResponse
	cleanupErr                     error
	cleanupFeatureIDs              []string
	cleanupRequests                []server.CleanupActionRequest
	startRebaseAccepted            apiTestActionResponse
	startRebaseErr                 error
	startRebaseFeatureIDs          []string
	startRebaseRequests            []server.RebaseActionRequest
	startRefactorAccepted          apiTestActionResponse
	startRefactorErr               error
	startRefactorFeatureIDs        []string
	startRefactorRequests          []server.RefactorActionRequest
	restartRefactorAccepted        apiTestActionResponse
	restartRefactorErr             error
	restartRefactorFeatureIDs      []string
	restartRefactorRequests        []server.RefactorActionRequest
	rewindAccepted                 apiTestActionResponse
	rewindErr                      error
	rewindFeatureIDs               []string
	rewindRequests                 []server.RewindFeatureRequest
	featureConfig                  server.FeatureConfigResponse
	featureConfigErr               error
	featureConfigIDs               []string
	updateFeatureConfigAccepted    apiTestActionResponse
	updateFeatureConfigErr         error
	updateFeatureConfigIDs         []string
	updateFeatureConfigRequests    []server.FeatureConfigMutationRequest
	updateRuntimeConfigAccepted    apiTestActionResponse
	updateRuntimeConfigErr         error
	updateRuntimeConfigRequests    []server.RuntimeConfigMutationRequest
	needUserInputAccepted          apiTestActionResponse
	needUserInputErr               error
	needUserInputFeatureIDs        []string
	needUserInputRequests          []server.NeedUserInputDecisionRequest
	needUserInputDraftAccepted     apiTestActionResponse
	needUserInputDraftErr          error
	needUserInputDraftFeatureIDs   []string
	needUserInputDraftRequests     []server.NeedUserInputDraftRequest
	reviewAccepted                 apiTestActionResponse
	reviewErr                      error
	reviewFeatureIDs               []string
	reviewRequests                 []server.ReviewDecisionRequest
	reviewSession                  server.ReviewSessionResponse
	reviewSessionErr               error
	reviewSessionFeatureIDs        []string
	saveReviewDraftErr             error
	saveReviewDraftFeatureIDs      []string
	saveReviewDraftReviewIDs       []string
	saveReviewDraftRequests        []server.ReviewDraftUpdateRequest
	submitReviewDecisionErr        error
	submitReviewDecisionFeatureIDs []string
	submitReviewDecisionReviewIDs  []string
	submitReviewDecisionRequests   []server.ReviewSessionDecisionRequest
	reviewCommentsResponse         server.ReviewCommentsFetchResponse
	reviewCommentsErr              error
	reviewCommentsFeatureIDs       []string
	reviewCommentsFetchRequests    []server.ReviewCommentsFetchRequest
	startReviewCommentsAccepted    apiTestActionResponse
	startReviewCommentsErr         error
	startReviewCommentsFeatureIDs  []string
	startReviewCommentsRequests    []server.ReviewCommentsActionRequest
	permissionAccepted             apiTestActionResponse
	permissionErr                  error
	permissionAnswers              []server.PermissionAnswerRequest
	helpAccepted                   apiTestActionResponse
	helpErr                        error
	helpRequests                   []server.HelpAnswerRequest
	startChatAccepted              apiTestActionResponse
	startChatErr                   error
	startChatRequests              []server.ChatStartRequest
	askUserAccepted                apiTestActionResponse
	askUserErr                     error
	askUserAnswers                 []server.AskUserAnswerRequest
	shutdownAccepted               apiTestActionResponse
	shutdownErr                    error
	shutdownCalls                  int
	detail                         server.FeatureDetailResponse
	detailsByID                    map[string]server.FeatureDetailResponse
	detailFeatureIDs               []string
	sessionDetail                  server.SessionDetailResponse
	sessionDetailsByID             map[string]server.SessionDetailResponse
	sessionDetailIDs               []string
	transcript                     server.TranscriptResponse
	transcriptsByID                map[string]server.TranscriptResponse
	transcriptSessionIDs           []string
	transcriptQueries              []server.CursorQuery
	subscribeSessionOutputRecords  []server.SessionOutputRecord
	artifactList                   server.ArtifactListResponse
	artifactListByRun              map[int]server.ArtifactListResponse
	artifactListFeatureIDs         []string
	artifactListRunNumbers         []int
	artifactContent                server.TextContentResponse
	artifactContentByID            map[string]server.TextContentResponse
	artifactContentFeatureIDs      []string
	artifactContentRunNumbers      []int
	artifactContentIDs             []string
	artifactContentQueries         []server.TextQuery
	logContent                     server.TextContentResponse
	logContentByID                 map[string]server.TextContentResponse
	logContentErrByID              map[string]error
	logContentErr                  error
	logContentFeatureIDs           []string
	logContentRunNumbers           []int
	logContentIDs                  []string
	logContentQueries              []server.TextQuery
}

func (f *fakeTUIAPIClient) Features(context.Context) (server.FeatureListResponse, error) {
	f.calls = append(f.calls, dashboardFeaturesPanelTitle)
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

func (f *fakeTUIAPIClient) RestartFeature(_ context.Context, featureID string, req server.RestartFeatureRequest) (server.FeatureRestartResponse, error) {
	f.calls = append(f.calls, "RestartFeature")
	f.restartFeatureIDs = append(f.restartFeatureIDs, featureID)
	f.restartRequests = append(f.restartRequests, req)
	return server.FeatureRestartResponse{FeatureID: f.restartAccepted.featureID(featureID), Result: f.restartAccepted.result("restarted")}, f.restartErr
}

func (f *fakeTUIAPIClient) StopFeature(_ context.Context, featureID string) (server.FeatureStopResponse, error) {
	f.calls = append(f.calls, "StopFeature")
	f.stopFeatureIDs = append(f.stopFeatureIDs, featureID)
	return server.FeatureStopResponse{FeatureID: f.stopAccepted.featureID(featureID), Result: f.stopAccepted.result(testFeatureIDStopped)}, f.stopErr
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
	return server.PublishFeatureResponse{FeatureID: f.publishAccepted.featureID(featureID), Result: f.publishAccepted.result(testFeatureIDPublished)}, f.publishErr
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

func (f *fakeTUIAPIClient) StartRebase(_ context.Context, featureID string, req server.RebaseActionRequest) (server.RebaseStartResponse, error) {
	f.calls = append(f.calls, "StartRebase")
	f.startRebaseFeatureIDs = append(f.startRebaseFeatureIDs, featureID)
	f.startRebaseRequests = append(f.startRebaseRequests, req)
	return server.RebaseStartResponse{FeatureID: f.startRebaseAccepted.featureID(featureID), Result: f.startRebaseAccepted.result("started"), CycleType: f.startRebaseAccepted.CycleType}, f.startRebaseErr
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
	if req.Defaults.Models != nil {
		f.runtime.Defaults = server.ApplyModelConfigPatch(f.runtime.Defaults, *req.Defaults.Models)
		f.runtime.FeatureDefaults.Models = f.runtime.Defaults
	}
	if req.Defaults.Inquireness != "" {
		f.runtime.FeatureDefaults.Inquireness = req.Defaults.Inquireness
	}
	if req.Defaults.Pipeline != "" {
		f.runtime.FeatureDefaults.Pipeline = req.Defaults.Pipeline
	}
	if req.Defaults.Checkpoints != nil {
		f.runtime.FeatureDefaults.Checkpoints = *req.Defaults.Checkpoints
	}
	if req.WorkspaceRoots != nil {
		f.runtime.WorkspaceRoots = append([]string(nil), (*req.WorkspaceRoots)...)
		f.runtime.Repos = testRuntimeConfigRepos(f.runtime.Repos, f.runtime.WorkspaceRoots)
	}
	if req.UI != nil {
		f.runtime.UI = *req.UI
	}
	if req.Notifications != nil {
		f.runtime.Notifications = *req.Notifications
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

func (f *fakeTUIAPIClient) ReviewDecision(_ context.Context, featureID string, req server.ReviewDecisionRequest) (server.ReviewDecisionResponse, error) {
	f.calls = append(f.calls, "ReviewDecision")
	f.reviewFeatureIDs = append(f.reviewFeatureIDs, featureID)
	f.reviewRequests = append(f.reviewRequests, req)
	return server.ReviewDecisionResponse{FeatureID: f.reviewAccepted.featureID(featureID), Decision: req.Decision, Result: f.reviewAccepted.result("submitted")}, f.reviewErr
}

func (f *fakeTUIAPIClient) CreateReviewSession(_ context.Context, featureID string) (server.ReviewSessionResponse, error) {
	f.calls = append(f.calls, "CreateReviewSession")
	f.reviewSessionFeatureIDs = append(f.reviewSessionFeatureIDs, featureID)
	session := f.reviewSession
	if session.FeatureID == "" {
		session.FeatureID = featureID
	}
	if session.ReviewID == "" {
		session.ReviewID = "review-1"
	}
	if session.ReviewMode == "" {
		session.ReviewMode = "plan"
	}
	if session.TargetPhase == "" {
		session.TargetPhase = feature.PhaseImplement.DirName()
	}
	if session.ArtifactID == "" {
		session.ArtifactID = "plan"
	}
	if session.DraftRevision == "" {
		session.DraftRevision = "rev-1"
	}
	return session, f.reviewSessionErr
}

func (f *fakeTUIAPIClient) SaveReviewDraft(_ context.Context, featureID, reviewID string, req server.ReviewDraftUpdateRequest) (server.ReviewSessionResponse, error) {
	f.calls = append(f.calls, "SaveReviewDraft")
	f.saveReviewDraftFeatureIDs = append(f.saveReviewDraftFeatureIDs, featureID)
	f.saveReviewDraftReviewIDs = append(f.saveReviewDraftReviewIDs, reviewID)
	f.saveReviewDraftRequests = append(f.saveReviewDraftRequests, req)
	session := f.reviewSession
	session.FeatureID = featureID
	session.ReviewID = reviewID
	session.Text = req.Text
	if session.DraftRevision == "" || session.DraftRevision == req.BaseRevision {
		session.DraftRevision = req.BaseRevision + "-saved"
	}
	return session, f.saveReviewDraftErr
}

func (f *fakeTUIAPIClient) SubmitReviewSessionDecision(_ context.Context, featureID, reviewID string, req server.ReviewSessionDecisionRequest) (server.ReviewSessionDecisionResponse, error) {
	f.calls = append(f.calls, "SubmitReviewSessionDecision")
	f.submitReviewDecisionFeatureIDs = append(f.submitReviewDecisionFeatureIDs, featureID)
	f.submitReviewDecisionReviewIDs = append(f.submitReviewDecisionReviewIDs, reviewID)
	f.submitReviewDecisionRequests = append(f.submitReviewDecisionRequests, req)
	return server.ReviewSessionDecisionResponse{FeatureID: featureID, ReviewID: reviewID, Decision: req.Decision, Result: "submitted"}, f.submitReviewDecisionErr
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

func (f *fakeTUIAPIClient) SubscribeSessionOutput(context.Context, string, server.SessionOutputStreamOptions) (<-chan server.SessionOutputRecord, <-chan error) {
	records := make(chan server.SessionOutputRecord)
	errs := make(chan error, 1)
	// Feed configured records (if any) before closing, on a goroutine, so a
	// test that wants to drain them one at a time via the returned tea.Cmd
	// doesn't race an already-closed errs channel against unread buffered
	// records — mirrors the real Client's goroutine-fed channel shape.
	go func() {
		defer close(records)
		defer close(errs)
		for _, rec := range f.subscribeSessionOutputRecords {
			records <- rec
		}
	}()
	return records, errs
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
		ID:           testFeatureIDActive,
		Name:         testFeatureNameTranslateSicilian,
		Slug:         testFeatureSlugTranslateSicilian,
		Status:       testFeatureStatusPlanning,
		CurrentPhase: testArtifactIDPlan,
		Repos:        []string{testRepoNameOrchestrator},
		CreatedAt:    time.Now(),
	}
	app := APIAppModel{
		width:          100,
		height:         30,
		focusPanel:     0,
		rightPanelMode: dashboardRightPanelOverview,
		featureList:    server.FeatureListResponse{Features: []server.FeatureSummary{summary}},
		featureDetails: map[string]server.FeatureDetailResponse{
			summary.ID: {Feature: apiTestFeatureDetail(summary)},
		},
		runtimeConfig: server.RuntimeConfigResponse{
			Runtime: server.RuntimeIdentity{StateDir: testRuntimeStateDirFeatures},
		},
	}
	app.rebuildPresentation(summary.ID)

	// Docked (not fullscreen): dashboard chrome is present alongside the chat panel.
	app.chatReady = true
	app.chat = NewAPIChatModel(app.width, 10, nil)
	app.chatOpen = true
	docked := stripANSI(app.View().Content)
	if !strings.Contains(docked, testSectionLabelInProgress) {
		t.Fatalf("expected dashboard section header present while docked, got:\n%s", docked)
	}

	// Fullscreen: dashboard chrome is gone, only the chat panel renders.
	app.chat.fullscreen = true
	fullscreen := stripANSI(app.View().Content)
	if strings.Contains(fullscreen, testSectionLabelInProgress) {
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
	// yet (e.g. a streamed response arriving via refresh-snapshot
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
