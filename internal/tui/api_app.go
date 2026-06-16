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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

type APIClient interface {
	Features(context.Context) (server.FeatureListResponse, error)
	FeatureDetail(context.Context, string) (server.FeatureDetailResponse, error)
	RuntimeConfig(context.Context) (server.RuntimeConfigResponse, error)
	FeatureConfig(context.Context, string) (server.FeatureConfigResponse, error)
	ModelCatalog(context.Context) (server.ModelCatalogResponse, error)
	Prompts(context.Context) (server.PromptSnapshotResponse, error)
	Permissions(context.Context) (server.PermissionSnapshotResponse, error)
	Sessions(context.Context) (server.SessionListResponse, error)
	Recovery(context.Context) (server.RecoverySnapshotResponse, error)
	SessionDetail(context.Context, string) (server.SessionDetailResponse, error)
	Transcript(context.Context, string, server.CursorQuery) (server.TranscriptResponse, error)
	ArtifactList(context.Context, string, int) (server.ArtifactListResponse, error)
	ArtifactContent(context.Context, string, int, string, server.TextQuery) (server.TextContentResponse, error)
	LogContent(context.Context, string, int, string, server.TextQuery) (server.TextContentResponse, error)
	LivePreview(context.Context, string) (server.LivePreviewResponse, error)
	CreateFeature(context.Context, server.CreateFeatureRequest) (server.CreateFeatureResponse, error)
	StartFeature(context.Context, string) (server.FeatureStartResponse, error)
	ResumeFeature(context.Context, string) (server.FeatureStartResponse, error)
	RestartFeature(context.Context, string, server.RestartFeatureRequest) (server.FeatureRestartResponse, error)
	StopFeature(context.Context, string) (server.FeatureStopResponse, error)
	DeleteFeature(context.Context, string) (server.DeleteFeatureResponse, error)
	PublishFeature(context.Context, string, server.PublishFeatureRequest) (server.PublishFeatureResponse, error)
	MergeFeature(context.Context, string) (server.MergeFeatureResponse, error)
	RewindFeature(context.Context, string, server.RewindFeatureRequest) (server.RewindFeatureResponse, error)
	RetryFeature(context.Context, string) (server.RetryFeatureResponse, error)
	StartRebase(context.Context, string, server.RebaseActionRequest) (server.RebaseStartResponse, error)
	MarkDone(context.Context, string) (server.MarkDoneResponse, error)
	CleanupFeature(context.Context, string, server.CleanupActionRequest) (server.CleanupFeatureResponse, error)
	UpdateFeatureConfig(context.Context, string, server.FeatureConfigMutationRequest) (server.FeatureConfigUpdateResponse, error)
	UpdateRuntimeConfig(context.Context, server.RuntimeConfigMutationRequest) (server.RuntimeConfigUpdateResponse, error)
	ExecuteRecovery(context.Context, server.RecoveryActionRequest) (server.RecoveryActionResponse, error)
	NeedUserInputDecision(context.Context, string, server.NeedUserInputDecisionRequest) (server.NeedUserInputDecisionResponse, error)
	DraftNeedUserInputAnswers(context.Context, string, server.NeedUserInputDraftRequest) (server.NeedUserInputDraftResponse, error)
	ToggleInputNotifications(context.Context, string) (server.InputNotificationsToggleResponse, error)
	ReviewDecision(context.Context, string, server.ReviewDecisionRequest) (server.ReviewDecisionResponse, error)
	FetchReviewComments(context.Context, string, server.ReviewCommentsFetchRequest) (server.ReviewCommentsFetchResponse, error)
	StartReviewComments(context.Context, string, server.ReviewCommentsActionRequest) (server.ReviewCommentsStartResponse, error)
	StartTweak(context.Context, string, server.TweakActionRequest) (server.TweakStartResponse, error)
	FinishTweak(context.Context, string, server.TweakFinishRequest) (server.TweakFinishResponse, error)
	StartRefactor(context.Context, string, server.RefactorActionRequest) (server.RefactorStartResponse, error)
	RestartRefactor(context.Context, string, server.RefactorActionRequest) (server.RefactorRestartResponse, error)
	AnswerPermission(context.Context, server.PermissionAnswerRequest) (server.PermissionAnswerResponse, error)
	SendHelp(context.Context, server.HelpAnswerRequest) (server.HelpSendResponse, error)
	AnswerAskUser(context.Context, server.AskUserAnswerRequest) (server.AskUserAnswerResponse, error)
	Shutdown(context.Context) (server.ShutdownResponse, error)
	SubscribeEvents(context.Context, server.EventSubscriptionOptions) (<-chan server.RefreshSignal, <-chan error)
	FetchRefreshSnapshot(context.Context, server.RefreshSignal) (server.RefreshSnapshot, error)
}

type APIAppOptions struct {
	Runtime                    server.RuntimeIdentity
	LaunchPolicy               server.LaunchPolicy
	OwnedServer                bool
	EventOptions               server.EventSubscriptionOptions
	WaitForOwnedServerShutdown func(context.Context) error
	StopOwnedServer            func(context.Context) error
}

const (
	apiTranscriptPageLimit = 50
	apiContentTailLimit    = int64(4096)
)

var apiSelectableLogIDs = []string{"session", "phase", "observe"}

type APIAppSnapshot struct {
	Runtime     APIRuntimePresentation
	Features    []APIFeaturePresentation
	Detail      *APIFeatureDetailPresentation
	Sessions    []APISessionPresentation
	LivePreview *APILivePreviewPresentation
	Transcript  *APITranscriptPresentation
	Content     *APIContentPresentation
}

type APIRuntimePresentation struct {
	Providers                  []string
	DangerouslySkipPermissions bool
	OwnedServer                bool
}

type APIFeaturePresentation struct {
	ID             string
	Name           string
	Slug           string
	Status         string
	CurrentPhase   string
	ActiveRun      int
	RunCount       int
	Repos          []string
	CreatedAt      time.Time
	AttentionCount int
	Progress       server.FeatureProgress
}

type APISessionPresentation struct {
	ID           string
	FeatureID    string
	Phase        string
	Repo         string
	Kind         string
	Label        string
	Provider     string
	Model        string
	Status       string
	ContextPct   int
	CanAttach    bool
	LogAvailable bool
}

type APILivePreviewPresentation struct {
	FeatureID      string
	SessionID      string
	Activity       string
	ContextPct     int
	CostUSD        float64
	Attention      []string
	TranscriptTail []string
}

type APITranscriptPresentation struct {
	SessionID string
	Start     int
	End       int
	Total     int
	Lines     []string
}

type APIContentPresentation struct {
	FeatureID string
	RunNumber int
	Log       *APITextSnippetPresentation
	Artifact  *APIArtifactSnippetPresentation
}

type APIRecoveryPresentation struct {
	SnapshotID string
	Items      []APIRecoveryItemPresentation
}

type APIRecoveryItemPresentation struct {
	Key            string
	FeatureName    string
	RepoName       string
	Phase          string
	Iteration      int
	PID            int
	ProcessAlive   bool
	DefaultAction  string
	SelectedAction string
}

type APITextSnippetPresentation struct {
	ID        string
	Offset    int64
	Limit     int64
	Size      int64
	Text      string
	Truncated bool
}

type APIArtifactSnippetPresentation struct {
	ID        string
	Type      string
	Category  string
	Phase     string
	Offset    int64
	Limit     int64
	Size      int64
	Text      string
	Truncated bool
}

type APIFeatureDetailPresentation struct {
	ID                 string
	Name               string
	Description        string
	Summary            string
	Pipeline           string
	Repos              []APIRepoStatusPresentation
	Actions            []APIActionPresentation
	TotalCostUSD       float64
	NeedUserInputLabel string
	Failure            string
}

type APIRepoStatusPresentation struct {
	Name  string
	State string
}

type APIActionPresentation struct {
	ID     string
	Status string
	Reason string
}

type APIAppModel struct {
	client                     APIClient
	featureList                server.FeatureListResponse
	featureDetails             map[string]server.FeatureDetailResponse
	runtimeConfig              server.RuntimeConfigResponse
	catalog                    server.ModelCatalogResponse
	prompts                    server.PromptSnapshotResponse
	permissions                server.PermissionSnapshotResponse
	sessionList                server.SessionListResponse
	sessionDetails             map[string]server.SessionDetailResponse
	livePreviews               map[string]server.LivePreviewResponse
	transcripts                map[string]server.TranscriptResponse
	contents                   map[string]apiFeatureContentSnapshot
	recovery                   server.RecoverySnapshotResponse
	launchPolicy               server.LaunchPolicy
	snapshot                   APIAppSnapshot
	recoveryPanel              *apiRecoveryPanel
	selectedFeature            string
	width                      int
	height                     int
	ownedServer                bool
	waitForOwnedServerShutdown func(context.Context) error
	quitOwnedServerPrompt      bool
	actionConfirmActive        bool
	actionConfirmKind          string
	actionConfirmFeatureID     string
	actionConfirmFeatureName   string
	actionConfirmArgs          apiFeatureActionArgs
	repoActionPanel            *apiRepoActionPanel
	rewindPanel                *apiRewindPanel
	rewindPhasePicker          *apiRoadmapRewindPanel
	tweakReviewModalActive     bool
	tweakReviewFeatureID       string
	tweakReviewFeatureName     string
	needInputPromptActive      bool
	needInputFeatureID         string
	needInputFeatureName       string
	needInputGate              server.NeedInputGateDTO
	permissionPromptActive     bool
	permissionFeatureID        string
	permissionFeatureName      string
	permissionRequest          server.ControlRequestDTO
	helpPromptActive           bool
	helpFeatureID              string
	helpFeatureName            string
	helpQuestion               string
	helpAnswerDraft            string
	askUserPromptActive        bool
	askUserFeatureID           string
	askUserFeatureName         string
	askUserQuestion            string
	askUserRequest             server.ControlRequestDTO
	askUserAnswerDraft         string
	askUserOptionCursor        int
	wizard                     *WizardModel
	reviewComments             *apiReviewCommentsPanel
	refactorPrompt             *apiRefactorPrompt
	refactorPipeline           *apiRefactorPipelinePanel
	configEditor               *EditConfigModel
	runtimeConfigEditor        *apiRuntimeConfigEditor
	workspaceManager           *WorkspaceManagerModel
	wizardRuntimeConfigPending bool
	helpOverlayActive          bool
	helpOverlay                HelpOverlayModel
	resumeAllConfirmActive     bool
	resumeAllFeatureIDs        []string
	focusPanel                 int
	rightPanelMode             dashboardRightPanelMode
	contentPanelActive         bool
	textPanelActive            bool
	textPanelTitle             string
	textPanelContent           string
	statusMessage              string
	eventCtx                   context.Context
	cancelEvents               context.CancelFunc
	signals                    <-chan server.RefreshSignal
	eventErrs                  <-chan error
}

type apiRefreshSignalMsg struct {
	signal server.RefreshSignal
}

type apiRefreshSnapshotMsg struct {
	snapshot server.RefreshSnapshot
	content  *apiFeatureContentSnapshot
	err      error
}

type apiContentSelectionMsg struct {
	featureID string
	content   apiFeatureContentSnapshot
	err       error
}

type apiTextPanelMsg struct {
	title   string
	content string
}

type apiFeatureDetailMsg struct {
	featureID   string
	detail      server.FeatureDetailResponse
	livePreview *server.LivePreviewResponse
	session     *server.SessionDetailResponse
	transcript  *server.TranscriptResponse
	content     *apiFeatureContentSnapshot
	err         error
}

type apiFeatureConfigMsg struct {
	featureID string
	config    server.FeatureConfigResponse
	err       error
}

type apiReviewCommentsFetchedMsg struct {
	featureID   string
	featureName string
	repo        string
	mode        string
	modes       []string
	response    server.ReviewCommentsFetchResponse
	err         error
}

type apiEventErrorMsg struct {
	err error
}

type apiMutationResultMsg struct {
	kind      string
	featureID string
	err       error
}

type apiRuntimeConfigMutationMsg struct {
	kind            string
	config          server.RuntimeConfigResponse
	createdRepoPath string
	err             error
}

type apiOwnedServerStoppedMsg struct {
	err error
}

type apiResumeAllResultMsg struct {
	succeeded []string
	failed    []string
}

type apiRecoveryPanel struct {
	snapshotID string
	items      []server.RecoveryItemDTO
	actions    map[string]string
	cursor     int
}

type apiReviewCommentsPanel struct {
	featureID   string
	featureName string
	repo        string
	mode        string
	modes       []string
	comments    []server.ReviewCommentDTO
}

type apiFeatureActionArgs struct {
	Repo            string
	Repos           []string
	TargetPhase     string
	RoadmapPhase    int
	UpgradePipeline feature.PipelineProfile
}

type apiRepoActionPanel struct {
	featureID   string
	featureName string
	kind        string
	repos       []apiRepoActionOption
	cursor      int
	multi       bool
	selected    map[string]bool
}

type apiRepoActionOption struct {
	Name        string
	State       string
	Publishable bool
	PRURL       string
}

type apiRewindPanel struct {
	featureID   string
	featureName string
	choices     []apiRewindChoice
	cursor      int
}

type apiRewindChoice struct {
	TargetPhase     string
	Label           string
	OverridePhase   string
	UpgradePipeline feature.PipelineProfile
}

type apiRoadmapRewindPanel struct {
	featureID   string
	featureName string
	rows        []apiRoadmapRewindRow
	cursor      int
}

type apiRoadmapRewindRow struct {
	Number        int
	Total         int
	Title         string
	PhaseType     string
	Status        string
	Effect        string
	ResetBoundary string
	CurrentPhase  bool
}

type apiRefactorPrompt struct {
	featureID   string
	featureName string
	repo        string
	draft       string
	input       textarea.Model
	pipelines   []feature.PipelineProfile
	restart     bool
}

type apiRefactorPipelinePanel struct {
	featureID   string
	featureName string
	repo        string
	prompt      string
	pipelines   []feature.PipelineProfile
	cursor      int
	restart     bool
}

type apiFeatureContentSnapshot struct {
	FeatureID     string
	RunNumber     int
	LogID         string
	Log           *server.TextContentResponse
	Artifacts     server.ArtifactListResponse
	ArtifactID    string
	Artifact      *server.TextContentResponse
	ArtifactMeta  *server.ArtifactDTO
	ContentLoaded bool
}

func NewAPIAppModel(ctx context.Context, client APIClient, opts APIAppOptions) (APIAppModel, error) {
	if client == nil {
		return APIAppModel{}, errors.New("api client is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	waitForOwnedServerShutdown := opts.WaitForOwnedServerShutdown
	if waitForOwnedServerShutdown == nil {
		waitForOwnedServerShutdown = opts.StopOwnedServer
	}
	app := APIAppModel{
		client:                     client,
		featureDetails:             map[string]server.FeatureDetailResponse{},
		sessionDetails:             map[string]server.SessionDetailResponse{},
		livePreviews:               map[string]server.LivePreviewResponse{},
		transcripts:                map[string]server.TranscriptResponse{},
		contents:                   map[string]apiFeatureContentSnapshot{},
		width:                      100,
		height:                     30,
		ownedServer:                opts.OwnedServer,
		waitForOwnedServerShutdown: waitForOwnedServerShutdown,
		launchPolicy:               opts.LaunchPolicy,
	}
	features, err := client.Features(ctx)
	if err != nil {
		return APIAppModel{}, fmt.Errorf("load features snapshot: %w", err)
	}
	runtimeConfig, err := client.RuntimeConfig(ctx)
	if err != nil {
		return APIAppModel{}, fmt.Errorf("load runtime config snapshot: %w", err)
	}
	ApplyKeyboardLayout(runtimeConfig.UI.KeyboardLayout)
	catalog, err := client.ModelCatalog(ctx)
	if err != nil {
		return APIAppModel{}, fmt.Errorf("load model catalog snapshot: %w", err)
	}
	prompts, err := client.Prompts(ctx)
	if err != nil {
		return APIAppModel{}, fmt.Errorf("load prompt snapshot: %w", err)
	}
	permissions, err := client.Permissions(ctx)
	if err != nil {
		return APIAppModel{}, fmt.Errorf("load permission snapshot: %w", err)
	}
	sessions, err := client.Sessions(ctx)
	if err != nil {
		return APIAppModel{}, fmt.Errorf("load session snapshot: %w", err)
	}
	recovery, err := client.Recovery(ctx)
	if err != nil {
		return APIAppModel{}, fmt.Errorf("load recovery snapshot: %w", err)
	}
	app.featureList = features
	app.runtimeConfig = runtimeConfig
	app.catalog = catalog
	app.prompts = prompts
	app.permissions = permissions
	app.sessionList = sessions
	app.recovery = recovery
	if len(recovery.Items) > 0 {
		app.recoveryPanel = newAPIRecoveryPanel(recovery)
	}
	app.rebuildPresentation("")
	if app.selectedFeature != "" {
		detail, err := client.FeatureDetail(ctx, app.selectedFeature)
		if err != nil {
			return APIAppModel{}, fmt.Errorf("load selected feature detail snapshot: %w", err)
		}
		app.storeFeatureDetail(detail)
		preview, err := client.LivePreview(ctx, app.selectedFeature)
		if err != nil {
			return APIAppModel{}, fmt.Errorf("load selected feature live preview snapshot: %w", err)
		}
		app.storeLivePreview(app.selectedFeature, preview)
		if sessionID := apiSelectedSessionID(preview); sessionID != "" {
			session, transcript, err := loadAPITranscriptTail(ctx, client, sessionID)
			if err != nil {
				return APIAppModel{}, err
			}
			app.storeSessionDetail(session)
			app.storeTranscript(sessionID, transcript)
		}
		if content := loadAPISelectedContent(ctx, client, app.selectedFeature, detail, nil); content != nil {
			app.storeContent(*content)
		}
		app.rebuildPresentation(app.selectedFeature)
	}

	eventCtx, cancel := context.WithCancel(ctx)
	app.eventCtx = eventCtx
	app.cancelEvents = cancel
	app.signals, app.eventErrs = client.SubscribeEvents(eventCtx, opts.EventOptions)
	return app, nil
}

func (m APIAppModel) Init() tea.Cmd {
	return m.listenForAPIEvents()
}

func (m APIAppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.helpOverlayActive {
			m.helpOverlay = NewHelpOverlayModel(m.helpOverlay.context, msg.Width, msg.Height)
		}
		if m.workspaceManager != nil {
			updated, _ := m.workspaceManager.Update(msg)
			m.workspaceManager = &updated
		}
		if m.wizard != nil {
			m.wizard.SetWidth(msg.Width)
			m.wizard.height = msg.Height
			if m.wizard.IsPickerActive() || m.wizard.IsRootPickerActive() {
				updated, _ := m.wizard.Update(msg)
				m.wizard = &updated
			}
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleAPIKey(msg)
	case HelpOverlayCloseMsg:
		m.helpOverlayActive = false
		return m, nil
	case apiRefreshSignalMsg:
		return m, tea.Batch(m.fetchRefreshSnapshotCmd(msg.signal), m.listenForAPIEvents())
	case apiRefreshSnapshotMsg:
		if msg.err != nil {
			m.statusMessage = "Refresh failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		m.ApplyRefreshSnapshot(msg.snapshot)
		if msg.content != nil {
			m.storeContent(*msg.content)
			m.rebuildPresentation(m.selectedFeature)
		}
		return m, nil
	case apiContentSelectionMsg:
		if msg.err != nil {
			m.statusMessage = "Content load failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		m.storeContent(msg.content)
		m.statusMessage = ""
		m.rebuildPresentation(msg.featureID)
		return m, nil
	case apiTextPanelMsg:
		m.textPanelActive = true
		m.textPanelTitle = msg.title
		m.textPanelContent = msg.content
		m.statusMessage = ""
		return m, nil
	case apiFeatureDetailMsg:
		if msg.err != nil {
			m.statusMessage = "Detail refresh failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		m.storeFeatureDetail(msg.detail)
		m.upsertFeatureSummary(msg.detail.Feature.FeatureSummary)
		if msg.livePreview != nil {
			m.storeLivePreview(msg.featureID, *msg.livePreview)
			m.upsertFeatureSummary(msg.livePreview.Feature)
		}
		if msg.session != nil {
			m.storeSessionDetail(*msg.session)
		}
		if msg.transcript != nil && msg.session != nil {
			m.storeTranscript(msg.session.Session.ID, *msg.transcript)
		}
		if msg.content != nil {
			m.storeContent(*msg.content)
		}
		m.rebuildPresentation(msg.featureID)
		return m, nil
	case apiFeatureConfigMsg:
		if msg.err != nil {
			m.statusMessage = "Config load failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		m.configEditor = newAPIEditConfigModel(msg.featureID, m.featureNameByID(msg.featureID), msg.config, apiPhaseModelCatalog(m.catalog))
		m.statusMessage = ""
		return m, nil
	case apiReviewCommentsFetchedMsg:
		if msg.err != nil {
			m.statusMessage = "Review comments fetch failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		repo := msg.response.Repo
		if repo == "" {
			repo = msg.repo
		}
		mode := msg.response.Mode
		if mode == "" {
			mode = msg.mode
		}
		modes := apiReviewCommentModes(msg.modes)
		if !stringSliceContains(modes, mode) {
			mode = modes[0]
		}
		m.reviewComments = &apiReviewCommentsPanel{
			featureID:   msg.featureID,
			featureName: msg.featureName,
			repo:        repo,
			mode:        mode,
			modes:       modes,
			comments:    append([]server.ReviewCommentDTO(nil), msg.response.Comments...),
		}
		m.statusMessage = fmt.Sprintf("Fetched %d review comments from %s", len(msg.response.Comments), repo)
		return m, nil
	case apiEventErrorMsg:
		if msg.err != nil {
			m.statusMessage = "Event stream failed: " + firstLine(msg.err.Error())
		}
		return m, nil
	case apiMutationResultMsg:
		if msg.err != nil {
			if msg.kind == "feature.create" && msg.featureID != "" {
				m.clearCreatePrompt()
			}
			if msg.kind == "feature.config.update" && m.configEditor != nil {
				m.configEditor.saving = false
				m.configEditor.saveErr = firstLine(msg.err.Error())
				return m, nil
			}
			m.statusMessage = "Mutation failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		if msg.kind == "recovery.execute" {
			m.recoveryPanel = nil
			m.recovery = server.RecoverySnapshotResponse{}
		}
		if msg.kind == "feature.config.update" {
			m.configEditor = nil
		}
		if msg.kind == "runtime.config.update" {
			m.runtimeConfigEditor = nil
		}
		if msg.kind == "feature.need_user_input.decision" {
			m.clearNeedInputPrompt()
		}
		if msg.kind == "permission.answer" {
			m.clearPermissionPrompt()
		}
		if msg.kind == "help.send" {
			m.clearHelpPrompt()
		}
		if msg.kind == "ask_user.answer" {
			m.clearAskUserPrompt()
		}
		if msg.kind == "feature.create" {
			m.clearCreatePrompt()
		}
		if msg.kind == "feature.review_comments" {
			m.reviewComments = nil
		}
		m.statusMessage = fmt.Sprintf("Completed %s", apiMutationKindLabel(msg.kind))
		m.rebuildPresentation(m.selectedFeature)
		return m, nil
	case apiRuntimeConfigMutationMsg:
		m.wizardRuntimeConfigPending = false
		if msg.err != nil {
			m.statusMessage = "Runtime config update failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		m.runtimeConfig = msg.config
		ApplyKeyboardLayout(m.runtimeConfig.UI.KeyboardLayout)
		if m.workspaceManager != nil {
			m.workspaceManager.SetRoots(apiWorkspaceRoots(m.runtimeConfig.WorkspaceRoots))
		}
		if m.wizard != nil {
			m.refreshAPIWizardRepos(msg.createdRepoPath)
		}
		m.statusMessage = fmt.Sprintf("Completed %s", apiMutationKindLabel(msg.kind))
		m.rebuildPresentation(m.selectedFeature)
		return m, nil
	case PlanReviewDecisionMsg:
		return m, m.reviewDecisionCmd(msg.FeatureID, m.planReviewDecisionRequest(msg.FeatureID, msg.Decision))
	case RoadmapReviewDecisionMsg:
		return m, m.reviewDecisionCmd(msg.FeatureID, roadmapReviewDecisionRequest(msg))
	case GateReviewDecisionMsg:
		return m, m.reviewDecisionCmd(msg.FeatureID, server.ReviewDecisionRequest{
			Decision: msg.Decision,
			Phase:    strings.ToLower(msg.Phase.String()),
		})
	case apiOwnedServerStoppedMsg:
		if msg.err != nil {
			m.statusMessage = "Server shutdown failed: " + firstLine(msg.err.Error())
			m.quitOwnedServerPrompt = false
			return m, nil
		}
		return m, tea.Quit
	case apiResumeAllResultMsg:
		m.resumeAllConfirmActive = false
		m.resumeAllFeatureIDs = nil
		switch {
		case len(msg.succeeded) > 0 && len(msg.failed) > 0:
			m.statusMessage = fmt.Sprintf("Resumed %d feature(s); %d failed", len(msg.succeeded), len(msg.failed))
		case len(msg.succeeded) > 0:
			m.statusMessage = fmt.Sprintf("Resumed %d feature(s)", len(msg.succeeded))
		case len(msg.failed) > 0:
			m.statusMessage = "Resume all failed: " + strings.Join(msg.failed, "; ")
		default:
			m.statusMessage = "No interrupted or failed features to resume"
		}
		m.rebuildPresentation(m.selectedFeature)
		return m, nil
	default:
		if m.workspaceManager != nil {
			return m.updateAPIWorkspaceManager(msg)
		}
		if m.wizard != nil {
			return m.handleAPIWizardMsg(msg)
		}
		return m, nil
	}
}

func (m APIAppModel) handleAPIKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.helpOverlayActive {
		var cmd tea.Cmd
		m.helpOverlay, cmd = m.helpOverlay.Update(msg)
		return m, cmd
	}
	if m.quitOwnedServerPrompt {
		switch strings.ToLower(msg.Text) {
		case "y":
			return m, m.stopOwnedServerCmd()
		case "n":
			return m, tea.Quit
		}
		if msg.Code == tea.KeyEscape {
			m.quitOwnedServerPrompt = false
		}
		return m, nil
	}
	if m.workspaceManager != nil {
		return m.updateAPIWorkspaceManager(msg)
	}
	if m.runtimeConfigEditor != nil {
		return m.handleAPIRuntimeConfigEditorKey(msg)
	}
	if m.recoveryPanel != nil {
		return m.handleAPIRecoveryKey(msg)
	}
	if m.configEditor != nil {
		return m.handleAPIConfigEditorKey(msg)
	}
	if m.wizard != nil {
		return m.handleAPIWizardMsg(msg)
	}
	if m.reviewComments != nil {
		return m.handleAPIReviewCommentsKey(msg)
	}
	if m.repoActionPanel != nil {
		return m.handleAPIRepoActionKey(msg)
	}
	if m.rewindPhasePicker != nil {
		return m.handleAPIRoadmapRewindKey(msg)
	}
	if m.rewindPanel != nil {
		return m.handleAPIRewindKey(msg)
	}
	if m.refactorPipeline != nil {
		return m.handleAPIRefactorPipelineKey(msg)
	}
	if m.refactorPrompt != nil {
		return m.handleAPIRefactorPromptKey(msg)
	}
	if m.needInputPromptActive {
		switch strings.ToLower(msg.Text) {
		case "r":
			return m, m.needUserInputDecisionCmd(m.needInputFeatureID, "resume")
		case "a":
			return m, m.needUserInputDecisionCmd(m.needInputFeatureID, "abort")
		}
		if msg.Code == tea.KeyEscape {
			m.clearNeedInputPrompt()
		}
		return m, nil
	}
	if m.permissionPromptActive {
		switch strings.ToLower(msg.Text) {
		case "a":
			return m, m.permissionAnswerCmd(m.permissionRequest, "allow")
		case "d":
			return m, m.permissionAnswerCmd(m.permissionRequest, "deny")
		}
		if msg.Code == tea.KeyEscape {
			m.clearPermissionPrompt()
		}
		return m, nil
	}
	if m.helpPromptActive {
		return m.handleAPIHelpPromptKey(msg)
	}
	if m.askUserPromptActive {
		return m.handleAPIAskUserPromptKey(msg)
	}
	if m.textPanelActive && msg.Code == tea.KeyEscape {
		m.textPanelActive = false
		m.textPanelTitle = ""
		m.textPanelContent = ""
		return m, nil
	}
	if m.contentPanelActive && msg.Code == tea.KeyEscape {
		m.contentPanelActive = false
		return m, nil
	}
	if m.tweakReviewModalActive {
		featureID := m.tweakReviewFeatureID
		switch strings.ToLower(msg.Text) {
		case "y":
			m.clearTweakReviewModal()
			return m, m.finishTweakDecisionCmd(featureID, "final-review", true)
		case "n":
			m.clearTweakReviewModal()
			return m, m.finishTweakDecisionCmd(featureID, "skip-review", true)
		}
		if msg.Code == tea.KeyEscape {
			m.clearTweakReviewModal()
			return m, m.finishTweakDecisionCmd(featureID, "restore-from-review", false)
		}
		return m, nil
	}
	if m.actionConfirmActive {
		kind := m.actionConfirmKind
		featureID := m.actionConfirmFeatureID
		args := m.actionConfirmArgs
		m.clearActionConfirm()
		if strings.ToLower(msg.Text) == "y" {
			return m, m.selectedFeatureActionCmd(kind, featureID, args)
		}
		return m, nil
	}
	if m.resumeAllConfirmActive {
		if strings.ToLower(msg.Text) == "y" {
			featureIDs := append([]string(nil), m.resumeAllFeatureIDs...)
			return m, m.resumeAllCmd(featureIDs)
		}
		m.resumeAllConfirmActive = false
		m.resumeAllFeatureIDs = nil
		return m, nil
	}
	if key.Matches(msg, keys.HelpOverlay) {
		return m.transitionToAPIHelpOverlay()
	}
	if msg.Code == 'r' && msg.Mod.Contains(tea.ModCtrl) {
		return m.openRewindPanel(), nil
	}
	switch msg.Code {
	case tea.KeyTab:
		m.focusPanel = (m.focusPanel + 1) % 2
		return m, nil
	case tea.KeyRight:
		if m.selectedFeature != "" {
			m.focusPanel = 1
		}
		return m, nil
	case tea.KeyLeft, tea.KeyEscape:
		if m.focusPanel == 1 {
			m.focusPanel = 0
			return m, nil
		}
	}
	switch msg.Text {
	case "[":
		return m.cycleSelectedArtifact(-1)
	case "]":
		return m.cycleSelectedArtifact(1)
	}
	switch {
	case msg.Text == "W":
		return m.openAPIWorkspaceManager(), nil
	case msg.Text == "R":
		return m.confirmResumeAll()
	case msg.Text == "N":
		if m.selectedFeature == "" {
			m.statusMessage = "No feature selected"
			return m, nil
		}
		return m, m.toggleAPIInputNotificationsCmd(m.selectedFeature)
	case key.Matches(msg, keys.Chat):
		m.statusMessage = "Ask chat requires a REST chat endpoint"
		return m, nil
	case msg.Text == "a":
		return m.openAPIContextualAction()
	case msg.Text == "o":
		return m.showAPIOverview(), nil
	}
	switch msg.Text {
	case "E":
		return m.openRuntimeConfigEditor(), nil
	case "M":
		return m.confirmSelectedFeatureAction("feature.merge"), nil
	case "D":
		return m.confirmSelectedFeatureAction("feature.mark-done"), nil
	case "F":
		return m.openRefactorPrompt(), nil
	case "T":
		return m.openTweakReviewModal(), nil
	}
	switch strings.ToLower(msg.Text) {
	case "q":
		if m.ownedServer {
			m.quitOwnedServerPrompt = true
			return m, nil
		}
		return m, tea.Quit
	case "n":
		return m.openCreateFeaturePrompt(0), nil
	case "v":
		return m.openSelectedDiff()
	case "w":
		return m.openCreateFeaturePrompt(2), nil
	case "p":
		return m.openPublishAction(), nil
	case "r":
		return m.confirmSelectedFeatureAction("feature.restart"), nil
	case "s":
		return m.confirmSelectedFeatureAction("feature.stop"), nil
	case "d":
		return m.confirmSelectedFeatureAction("feature.delete"), nil
	case "c":
		return m.confirmSelectedFeatureAction("feature.cleanup"), nil
	case "e":
		if m.selectedFeature == "" {
			m.statusMessage = "No feature selected"
			return m, nil
		}
		if f := m.selectedAPIDashboardFeature(); f == nil || !isFeatureQuiescent(f) {
			m.statusMessage = "Config can only be edited when the feature is idle"
			return m, nil
		}
		return m, m.fetchFeatureConfigCmd(m.selectedFeature)
	case "l":
		if m.rightPanelMode == dashboardRightPanelOverview {
			m.rightPanelMode = dashboardRightPanelLivePreview
			m.statusMessage = ""
			return m, nil
		}
		return m.cycleSelectedLog()
	case "g":
		return m.openReviewCommentsPreview()
	case "t":
		return m.openRepoCycleAction("feature.tweak.start"), nil
	case "b":
		return m.openRepoCycleAction("feature.rebase"), nil
	case "i":
		return m.openNeedInputPrompt(), nil
	case "a":
		return m.openPermissionPrompt(), nil
	case "h":
		return m.openHelpPrompt(), nil
	case "u":
		return m.openAskUserPrompt(), nil
	}
	switch msg.Code {
	case tea.KeyEnter:
		if m.selectedFeature == "" {
			return m, nil
		}
		if m.focusPanel == 0 {
			m.focusPanel = 1
		}
		return m, nil
	case tea.KeyUp:
		previous := m.selectedFeature
		m.moveSelection(-1)
		if m.selectedFeature != previous {
			m.rebuildPresentation(m.selectedFeature)
			return m, m.fetchFeatureDetailCmd(m.selectedFeature)
		}
	case tea.KeyDown:
		previous := m.selectedFeature
		m.moveSelection(1)
		if m.selectedFeature != previous {
			m.rebuildPresentation(m.selectedFeature)
			return m, m.fetchFeatureDetailCmd(m.selectedFeature)
		}
	}
	return m, nil
}

func (m APIAppModel) View() tea.View {
	view := m.renderAPIDashboard()
	w := max(m.width, 80)
	h := max(m.height, 24)
	if m.recoveryPanel != nil {
		view = overlayModal(view, m.renderAPIRecovery(), w, h)
	}
	if m.configEditor != nil {
		view = overlayModal(view, m.configEditor.View(), w, h)
	}
	if m.runtimeConfigEditor != nil {
		view = overlayModal(view, m.runtimeConfigEditor.render(min(w-4, 96)), w, h)
	}
	if m.workspaceManager != nil {
		view = overlayModal(view, m.workspaceManager.View(), w, h)
		if m.workspaceManager.IsPickerActive() {
			view = overlayModal(view, m.workspaceManager.PickerView(), w, h)
		}
	}
	if m.wizard != nil {
		view = overlayModal(view, m.wizard.ViewModal(), w, h)
		if m.wizard.IsPickerActive() {
			view = overlayModal(view, m.wizard.PickerView(), w, h)
		}
		if m.wizard.IsRootPickerActive() {
			view = overlayModal(view, m.wizard.RootPickerView(), w, h)
		}
	}
	if m.reviewComments != nil {
		view = overlayModal(view, m.renderAPIReviewCommentsPanel(min(w-4, 96)), w, h)
	}
	if m.repoActionPanel != nil {
		view = overlayModal(view, m.renderAPIRepoActionPanel(min(w-4, 72)), w, h)
	}
	if m.rewindPanel != nil {
		view = overlayModal(view, m.renderAPIRewindPanel(min(w-4, 72)), w, h)
	}
	if m.rewindPhasePicker != nil {
		view = overlayModal(view, m.renderAPIRoadmapRewindPanel(min(w-4, 84)), w, h)
	}
	if m.contentPanelActive && m.snapshot.Content != nil {
		view = overlayModal(view, panelStyle(true).Width(min(w-4, 96)).Render(m.renderAPIContent()), w, h)
	}
	if m.textPanelActive {
		view = overlayModal(view, panelStyle(true).Width(min(w-4, 96)).Render(m.renderAPITextPanel()), w, h)
	}
	if m.helpOverlayActive {
		view = overlayModal(view, m.helpOverlay.View(), w, h)
	}
	if m.quitOwnedServerPrompt {
		view = overlayModal(view, panelStyle(true).
			Width(58).
			BorderForeground(colorWarning).
			Render("Stop the server started for this TUI session?\n\n[y] Stop server and quit   [n] Leave running   [esc] Cancel"), w, h)
	}
	if m.actionConfirmActive {
		view = overlayModal(view, m.renderFeatureActionConfirm(), w, h)
	}
	if m.resumeAllConfirmActive {
		view = overlayModal(view, m.renderAPIResumeAllConfirm(), w, h)
	}
	if m.tweakReviewModalActive {
		view = overlayModal(view, m.renderTweakReviewModal(), w, h)
	}
	if m.needInputPromptActive {
		view = overlayModal(view, m.renderNeedInputPrompt(), w, h)
	}
	if m.permissionPromptActive {
		view = overlayModal(view, m.renderPermissionPrompt(), w, h)
	}
	if m.helpPromptActive {
		view = overlayModal(view, m.renderHelpPrompt(), w, h)
	}
	if m.askUserPromptActive {
		view = overlayModal(view, m.renderAskUserPrompt(), w, h)
	}
	v := tea.NewView(view)
	v.AltScreen = true
	return v
}

func (m APIAppModel) renderAPIDashboard() string {
	features := m.apiDashboardFeatures()
	dashboard := NewDashboardModel(features, m.runtimeConfig.Runtime.StateDir)
	dashboard.width = max(m.width, 80)
	dashboard.height = max(m.height, 24)
	dashboard.focusPanel = m.focusPanel
	dashboard.rightPanelMode = m.rightPanelMode
	dashboard.dangerouslySkipPerms = m.snapshot.Runtime.DangerouslySkipPermissions
	dashboard.statusMessage = m.statusMessage
	if len(m.runtimeConfig.UI.CollapsedSections) > 0 {
		dashboard.SetCollapsedSections(m.runtimeConfig.UI.CollapsedSections)
	}
	dashboard.selectFeature(m.selectedFeature)
	m.applyAPIDashboardRefactorState(&dashboard)
	if preview := m.snapshot.LivePreview; preview != nil {
		if dashboard.livePreview.feature != nil && preview.CostUSD > 0 {
			dashboard.livePreview.feature.PhaseCosts = apiPhaseCosts(nil, preview.CostUSD, dashboard.livePreview.feature.CurrentPhase)
		}
		dashboard.livePreview.contextPct = preview.ContextPct
		dashboard.livePreview.session = newAPILivePreviewSession(*preview)
	}
	return dashboard.View()
}

func (m APIAppModel) applyAPIDashboardRefactorState(dashboard *DashboardModel) {
	if dashboard == nil {
		return
	}
	dashboard.preview.refactorActive = false
	dashboard.preview.refactorPipelineActive = false
	if prompt := m.refactorPrompt; prompt != nil {
		dashboard.preview.refactorActive = true
		dashboard.preview.refactorFeatureName = firstNonEmpty(prompt.featureName, prompt.featureID)
		dashboard.preview.refactorInputView = prompt.input.View()
	}
	if panel := m.refactorPipeline; panel != nil {
		dashboard.preview.refactorPipelineActive = true
		dashboard.preview.refactorPipelineView = m.renderAPIRefactorPipelineSelector()
	}
}

func (m APIAppModel) apiDashboardFeatures() []*feature.Feature {
	details := make(map[string]server.FeatureDetailDTO, len(m.featureDetails))
	for id, resp := range m.featureDetails {
		details[id] = resp.Feature
	}
	features := make([]*feature.Feature, 0, len(m.featureList.Features))
	for _, summary := range m.featureList.Features {
		detail, hasDetail := details[summary.ID]
		features = append(features, m.apiDashboardFeature(summary, detail, hasDetail))
	}
	return features
}

func (m APIAppModel) selectedAPIDashboardFeature() *feature.Feature {
	for _, f := range m.apiDashboardFeatures() {
		if f.ID == m.selectedFeature {
			return f
		}
	}
	return nil
}

func (m APIAppModel) apiDashboardFeature(summary server.FeatureSummary, detail server.FeatureDetailDTO, hasDetail bool) *feature.Feature {
	models := m.runtimeConfig.Defaults
	if hasDetail {
		summary = detail.FeatureSummary
		if detail.Models != (config.ModelConfig{}) {
			models = detail.Models
		}
	}
	f := &feature.Feature{
		ID:                  summary.ID,
		Name:                firstNonEmpty(summary.Name, summary.Slug),
		Slug:                firstNonEmpty(summary.Slug, summary.Name),
		Status:              apiFeatureStatus(summary.Status),
		CurrentPhase:        apiFeaturePhase(summary.CurrentPhase),
		Created:             summary.CreatedAt,
		ActiveRun:           summary.ActiveRun,
		RunCount:            summary.RunCount,
		Models:              models,
		CurrentIteration:    summary.Progress.CurrentIteration,
		CurrentRoadmapPhase: summary.Progress.CurrentRoadmapPhase,
		TotalRoadmapPhases:  summary.Progress.TotalRoadmapPhases,
		CurrentPhaseStatus:  summary.Progress.CurrentPhaseStatus,
		Checkpoints: feature.Checkpoints{
			InquiryReview:   summary.Checkpoints.InquiryReview,
			ResearchReview:  summary.Checkpoints.ResearchReview,
			DesignReview:    summary.Checkpoints.DesignReview,
			RoadmapReview:   summary.Checkpoints.RoadmapReview,
			PhasePlanReview: summary.Checkpoints.PhasePlanReview,
			ManualPublish:   summary.Checkpoints.ManualPublish,
		},
		RepoStates:   map[string]*feature.RepoState{},
		RepoCycles:   map[string]*feature.RepoCycleState{},
		PhaseTimings: map[string]time.Duration{},
		PhaseCosts:   map[string]float64{},
	}
	if f.ActiveRun == 0 {
		f.ActiveRun = 1
	}
	if f.RunCount == 0 {
		f.RunCount = f.ActiveRun
	}
	if hasDetail {
		f.Description = detail.Description
		f.Summary = detail.Summary
		f.Pipeline = feature.PipelineProfile(detail.Pipeline)
		f.PhaseTimings = apiPhaseTimings(detail.Timing.ByPhase)
		f.PhaseCosts = apiPhaseCosts(detail.Cost.ByPhase, detail.Cost.TotalUSD, f.CurrentPhase)
		if detail.ActiveRun != nil {
			if detail.ActiveRun.RunNumber > 0 {
				f.ActiveRun = detail.ActiveRun.RunNumber
			}
			f.CurrentIteration = firstNonZero(detail.ActiveRun.Iteration, f.CurrentIteration)
			f.CurrentRoadmapPhase = firstNonZero(detail.ActiveRun.RoadmapPhase, f.CurrentRoadmapPhase)
			f.TotalRoadmapPhases = firstNonZero(detail.ActiveRun.RoadmapTotal, f.TotalRoadmapPhases)
			f.CurrentPhaseStatus = firstNonEmpty(detail.ActiveRun.PhaseStatus, f.CurrentPhaseStatus)
			if detail.ActiveRun.CurrentPhase != "" {
				f.CurrentPhase = apiFeaturePhase(detail.ActiveRun.CurrentPhase)
			}
		}
		if detail.Failure != nil {
			f.FailureType = detail.Failure.Type
			f.LastError = detail.Failure.Message
		}
		if detail.NeedUserInput != nil && detail.NeedUserInput.Open && f.Status != feature.StatusNeedUserInput {
			f.Status = feature.StatusNeedUserInput
		}
	}
	repoStatuses := map[string]server.RepoStatusDTO{}
	if hasDetail {
		for _, repo := range detail.RepoStatus {
			repoStatuses[repo.Name] = repo
		}
	}
	for _, repoName := range summary.Repos {
		f.Repos = append(f.Repos, m.apiDashboardRepo(repoName, repoStatuses[repoName], hasDetail))
	}
	for _, repo := range detail.RepoStatus {
		if repo.Name == "" || apiHasRepo(f.Repos, repo.Name) {
			continue
		}
		f.Repos = append(f.Repos, m.apiDashboardRepo(repo.Name, repo, true))
	}
	for _, repo := range f.Repos {
		dto := repoStatuses[repo.Name]
		f.RepoStates[repo.Name] = &feature.RepoState{
			Touched:   dto.Touched,
			PRURL:     dto.PRURL,
			LastError: dto.LastError,
		}
		if dto.CycleType != "" || dto.CycleStatus != "" {
			cycle := &feature.RepoCycleState{
				Type:   feature.RepoCycleType(dto.CycleType),
				Status: dto.CycleStatus,
			}
			f.RepoCycles[repo.Name] = cycle
			if f.ActiveCycle == nil && cycle.Status != "" {
				f.ActiveCycle = &feature.CycleState{Type: cycle.Type, Status: cycle.Status, Count: cycle.Count}
			}
		}
	}
	if len(f.RepoStates) == 0 {
		f.RepoStates = nil
	}
	if len(f.RepoCycles) == 0 {
		f.RepoCycles = nil
	}
	m.applyAPIAttention(f)
	f.SetRun(&feature.Run{
		RunNumber:           f.ActiveRun,
		CurrentIteration:    f.CurrentIteration,
		CurrentRoadmapPhase: f.CurrentRoadmapPhase,
		TotalRoadmapPhases:  f.TotalRoadmapPhases,
		CurrentPhaseStatus:  f.CurrentPhaseStatus,
		RepoStates:          f.RepoStates,
		RepoCycles:          f.RepoCycles,
		ActiveCycle:         f.ActiveCycle,
		PhaseTimings:        f.PhaseTimings,
		PhaseCosts:          f.PhaseCosts,
		LastError:           f.LastError,
		FailureType:         f.FailureType,
	})
	return f
}

func (m APIAppModel) apiDashboardRepo(name string, status server.RepoStatusDTO, hasDetail bool) feature.FeatureRepo {
	repo := feature.FeatureRepo{Name: name}
	for _, cfgRepo := range m.runtimeConfig.Repos {
		if cfgRepo.Name == name {
			repo.Path = cfgRepo.Path
			repo.WorktreePath = cfgRepo.Path
			break
		}
	}
	if hasDetail {
		publishable := status.Publishable
		repo.Publishable = &publishable
	}
	return repo
}

func (m APIAppModel) applyAPIAttention(f *feature.Feature) {
	if f == nil {
		return
	}
	for _, help := range m.prompts.HelpQueue {
		if help.FeatureID == f.ID && help.Pending {
			f.HelpQueue = append(f.HelpQueue, feature.HelpRequest{
				Question: help.Question,
				Time:     help.Time,
				Pending:  true,
			})
		}
	}
	for _, ask := range m.prompts.AskUserQuestions {
		if ask.FeatureID == f.ID && isPendingControlStatus(ask.Status) {
			f.HelpQueue = append(f.HelpQueue, feature.HelpRequest{
				Question: firstNonEmpty(ask.Summary, ask.ToolName, "Agent has a question"),
				Pending:  true,
			})
		}
	}
	for _, req := range m.permissions.Requests {
		if req.FeatureID == f.ID && isPendingControlStatus(req.Status) {
			f.PermissionsQueue = append(f.PermissionsQueue, feature.PermissionRequest{
				Tool:    firstNonEmpty(req.ToolName, "tool"),
				Args:    req.Summary,
				Pending: true,
			})
		}
	}
}

func (m *DashboardModel) selectFeature(featureID string) {
	if featureID == "" {
		m.syncPreview()
		return
	}
	for i, item := range m.visibleItems {
		if item.kind == listItemFeature && item.feature != nil && item.feature.ID == featureID {
			m.cursor = i
			m.computeCursorLine()
			m.updateScrollState(0)
			m.syncPreview()
			return
		}
	}
	m.syncPreview()
}

type apiSessionView struct {
	id         string
	featureID  string
	phase      feature.Phase
	repo       string
	kind       ports.SessionKind
	label      string
	status     ports.SessionStatus
	startedAt  time.Time
	iteration  int
	provider   string
	model      string
	contextPct int
	log        ports.MessageLog
	cost       *llm.ResultMessage
}

func newAPILivePreviewSession(preview APILivePreviewPresentation) ports.SessionView {
	if preview.SessionID == "" && preview.Activity == "" && len(preview.TranscriptTail) == 0 {
		return nil
	}
	log := session.NewMessageLog()
	for _, line := range preview.TranscriptTail {
		appendAPILivePreviewText(log, line)
	}
	appendAPILivePreviewActivity(log, preview.Activity)
	var cost *llm.ResultMessage
	if preview.CostUSD > 0 {
		cost = &llm.ResultMessage{TotalCostUSD: preview.CostUSD}
	}
	return apiSessionView{
		id:         preview.SessionID,
		featureID:  preview.FeatureID,
		phase:      feature.PhaseImplement,
		kind:       ports.KindPhase,
		label:      "Live Preview",
		status:     ports.SessionRunning,
		contextPct: preview.ContextPct,
		log:        log,
		cost:       cost,
	}
}

func appendAPILivePreviewActivity(log ports.MessageLog, activity string) {
	activity = strings.TrimSpace(activity)
	if activity == "" {
		return
	}
	if tool := apiToolNameFromActivity(activity); tool != "" {
		log.Append(llm.SDKMessage{
			Type: "tool_progress",
			ToolProgress: &llm.ToolProgressMessage{
				Type:     "tool_progress",
				ToolName: tool,
			},
		})
		return
	}
	log.Append(llm.SDKMessage{
		Type:   "status",
		Status: &llm.StatusMessage{Type: "status", Message: activity},
	})
}

func appendAPILivePreviewText(log ports.MessageLog, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	log.Append(llm.SDKMessage{
		Type: "assistant",
		Assistant: &llm.AssistantMessage{
			Type: "assistant",
			Message: llm.ConversationMsg{
				Role: "assistant",
				Content: []llm.ContentBlock{{
					Type: "text",
					Text: text,
				}},
			},
		},
	})
}

func apiToolNameFromActivity(activity string) string {
	activity = strings.TrimSpace(activity)
	if !strings.HasPrefix(activity, "Using ") || !strings.HasSuffix(activity, "...") {
		return ""
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(activity, "Using "), "..."))
}

func (s apiSessionView) ID() string { return s.id }
func (s apiSessionView) FeatureID() string {
	return s.featureID
}
func (s apiSessionView) Phase() feature.Phase { return s.phase }
func (s apiSessionView) RepoName() string     { return s.repo }
func (s apiSessionView) PermCacheScope() string {
	return ""
}
func (s apiSessionView) Kind() ports.SessionKind     { return s.kind }
func (s apiSessionView) Label() string               { return s.label }
func (s apiSessionView) Status() ports.SessionStatus { return s.status }
func (s apiSessionView) IsActive() bool {
	return s.status == ports.SessionRunning || s.status == ports.SessionWaitingHelp || s.status == ports.SessionWaitingPermission
}
func (s apiSessionView) Iteration() int               { return s.iteration }
func (s apiSessionView) StartedAt() time.Time         { return s.startedAt }
func (s apiSessionView) InitialPrompt() string        { return "" }
func (s apiSessionView) ProviderName() string         { return s.provider }
func (s apiSessionView) Model() string                { return s.model }
func (s apiSessionView) WorkDir() string              { return "" }
func (s apiSessionView) MessageLog() ports.MessageLog { return s.log }
func (s apiSessionView) Cost() *llm.ResultMessage     { return s.cost }
func (s apiSessionView) LatestUsage() *llm.Usage      { return nil }
func (s apiSessionView) AccumulatedUsage() llm.Usage  { return llm.Usage{} }
func (s apiSessionView) LastControlRequest() *llm.ControlRequestMessage {
	return nil
}
func (s apiSessionView) PendingControlRequests() []*llm.ControlRequestMessage {
	return nil
}
func (s apiSessionView) QALog() []ports.QAPair { return nil }
func (s apiSessionView) LogFilePath() string   { return "" }
func (s apiSessionView) ContextPercentage() int {
	return s.contextPct
}
func (s apiSessionView) ErrorDetail() string    { return "" }
func (s apiSessionView) ExitCodeDetail() string { return "" }
func (s apiSessionView) LastStdoutAt() time.Time {
	return time.Time{}
}
func (s apiSessionView) StatusCh() <-chan string         { return nil }
func (s apiSessionView) AttachCh() <-chan llm.SDKMessage { return nil }
func (s apiSessionView) Done() <-chan struct{}           { return nil }
func (s apiSessionView) HasPendingAskUserQuestion() bool {
	return false
}
func (s apiSessionView) SendUserMessage(string) error {
	return errors.New("api live preview session is read-only")
}
func (s apiSessionView) RespondToControl(string, bool, string) error {
	return errors.New("api live preview session is read-only")
}
func (s apiSessionView) RespondToAskUser(string, json.RawMessage, map[string]string, map[string]llm.AskUserAnnotation) error {
	return errors.New("api live preview session is read-only")
}
func (s apiSessionView) ClearPendingQuestion(string) {}
func (s apiSessionView) ResetWaitingStatus()         {}
func (s apiSessionView) Stop() error {
	return errors.New("api live preview session is read-only")
}
func (s apiSessionView) Interrupt() error {
	return errors.New("api live preview session is read-only")
}
func (s apiSessionView) Wait() {}

func (m APIAppModel) Close() {
	if m.cancelEvents != nil {
		m.cancelEvents()
	}
}

func (m APIAppModel) Snapshot() APIAppSnapshot {
	out := m.snapshot
	out.Runtime.Providers = append([]string(nil), out.Runtime.Providers...)
	out.Features = append([]APIFeaturePresentation(nil), out.Features...)
	out.Sessions = append([]APISessionPresentation(nil), out.Sessions...)
	if out.LivePreview != nil {
		preview := *out.LivePreview
		preview.Attention = append([]string(nil), preview.Attention...)
		preview.TranscriptTail = append([]string(nil), preview.TranscriptTail...)
		out.LivePreview = &preview
	}
	if out.Transcript != nil {
		transcript := *out.Transcript
		transcript.Lines = append([]string(nil), transcript.Lines...)
		out.Transcript = &transcript
	}
	if out.Content != nil {
		content := *out.Content
		if content.Log != nil {
			log := *content.Log
			content.Log = &log
		}
		if content.Artifact != nil {
			artifact := *content.Artifact
			content.Artifact = &artifact
		}
		out.Content = &content
	}
	if out.Detail != nil {
		detail := *out.Detail
		detail.Repos = append([]APIRepoStatusPresentation(nil), detail.Repos...)
		detail.Actions = append([]APIActionPresentation(nil), detail.Actions...)
		out.Detail = &detail
	}
	return out
}

func (m APIAppModel) SelectedFeatureID() string {
	return m.selectedFeature
}

func (m APIAppModel) ShowingOwnedServerQuitPrompt() bool {
	return m.quitOwnedServerPrompt
}

func (m APIAppModel) ShowingFeatureActionConfirm() bool {
	return m.actionConfirmActive
}

func (m APIAppModel) ShowingTweakReviewModal() bool {
	return m.tweakReviewModalActive
}

func (m APIAppModel) ShowingNeedInputPrompt() bool {
	return m.needInputPromptActive
}

func (m APIAppModel) ShowingPermissionPrompt() bool {
	return m.permissionPromptActive
}

func (m APIAppModel) ShowingHelpPrompt() bool {
	return m.helpPromptActive
}

func (m APIAppModel) ShowingAskUserPrompt() bool {
	return m.askUserPromptActive
}

func (m APIAppModel) ShowingCreateFeaturePrompt() bool {
	return m.wizard != nil
}

func (m *APIAppModel) ApplyRefreshSnapshot(snapshot server.RefreshSnapshot) {
	selected := m.selectedFeature
	if snapshot.Features != nil {
		m.featureList = *snapshot.Features
	}
	if snapshot.RuntimeConfig != nil {
		m.runtimeConfig = *snapshot.RuntimeConfig
		ApplyKeyboardLayout(m.runtimeConfig.UI.KeyboardLayout)
	}
	if snapshot.Prompts != nil {
		m.prompts = *snapshot.Prompts
	}
	if snapshot.Permissions != nil {
		m.permissions = *snapshot.Permissions
	}
	if snapshot.Recovery != nil {
		m.recovery = *snapshot.Recovery
		m.recoveryPanel = mergeAPIRecoveryPanel(m.recoveryPanel, *snapshot.Recovery)
	}
	if snapshot.Sessions != nil {
		m.sessionList = *snapshot.Sessions
	}
	if snapshot.Feature != nil {
		m.storeFeatureDetail(*snapshot.Feature)
		m.upsertFeatureSummary(snapshot.Feature.Feature.FeatureSummary)
	}
	if snapshot.Session != nil {
		m.storeSessionDetail(*snapshot.Session)
		m.upsertSessionSummary(snapshot.Session.Session.SessionSummaryDTO)
	}
	if snapshot.Transcript != nil && snapshot.Session != nil {
		m.storeTranscript(snapshot.Session.Session.ID, *snapshot.Transcript)
	}
	if snapshot.LivePreview != nil {
		m.storeLivePreview(snapshot.LivePreview.Feature.ID, *snapshot.LivePreview)
		m.upsertFeatureSummary(snapshot.LivePreview.Feature)
	}
	m.rebuildPresentation(selected)
}

func (m *APIAppModel) storeFeatureDetail(detail server.FeatureDetailResponse) {
	if detail.Feature.ID == "" {
		return
	}
	if m.featureDetails == nil {
		m.featureDetails = map[string]server.FeatureDetailResponse{}
	}
	m.featureDetails[detail.Feature.ID] = detail
}

func (m *APIAppModel) storeSessionDetail(detail server.SessionDetailResponse) {
	if detail.Session.ID == "" {
		return
	}
	if m.sessionDetails == nil {
		m.sessionDetails = map[string]server.SessionDetailResponse{}
	}
	m.sessionDetails[detail.Session.ID] = detail
}

func (m *APIAppModel) storeLivePreview(featureID string, preview server.LivePreviewResponse) {
	if featureID == "" {
		featureID = preview.Feature.ID
	}
	if featureID == "" {
		return
	}
	if m.livePreviews == nil {
		m.livePreviews = map[string]server.LivePreviewResponse{}
	}
	m.livePreviews[featureID] = preview
}

func (m *APIAppModel) storeTranscript(sessionID string, transcript server.TranscriptResponse) {
	if sessionID == "" {
		return
	}
	if m.transcripts == nil {
		m.transcripts = map[string]server.TranscriptResponse{}
	}
	existing, ok := m.transcripts[sessionID]
	if !ok || transcript.Cursor.Start == 0 || transcript.Cursor.Start > existing.Cursor.End {
		m.transcripts[sessionID] = transcript
		return
	}
	byIndex := make(map[int]server.TranscriptMessageDTO, len(existing.Messages)+len(transcript.Messages))
	for _, msg := range existing.Messages {
		byIndex[msg.Index] = msg
	}
	for _, msg := range transcript.Messages {
		byIndex[msg.Index] = msg
	}
	merged := make([]server.TranscriptMessageDTO, 0, len(byIndex))
	for _, msg := range byIndex {
		merged = append(merged, msg)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Index < merged[j].Index
	})
	if transcript.Cursor.Total == 0 {
		transcript.Cursor.Total = existing.Cursor.Total
	}
	if existing.Cursor.Start < transcript.Cursor.Start {
		transcript.Cursor.Start = existing.Cursor.Start
	}
	if existing.Cursor.End > transcript.Cursor.End {
		transcript.Cursor.End = existing.Cursor.End
	}
	transcript.Messages = merged
	m.transcripts[sessionID] = transcript
}

func (m *APIAppModel) storeContent(content apiFeatureContentSnapshot) {
	if content.FeatureID == "" || content.RunNumber <= 0 {
		return
	}
	if m.contents == nil {
		m.contents = map[string]apiFeatureContentSnapshot{}
	}
	m.contents[content.FeatureID] = content
}

func (m *APIAppModel) rebuildPresentation(preferredFeatureID string) {
	attention := apiAttentionCounts(m.prompts, m.permissions)
	features := make([]APIFeaturePresentation, 0, len(m.featureList.Features))
	for _, dto := range m.featureList.Features {
		features = append(features, APIFeaturePresentation{
			ID:             dto.ID,
			Name:           dto.Name,
			Slug:           dto.Slug,
			Status:         dto.Status,
			CurrentPhase:   dto.CurrentPhase,
			ActiveRun:      dto.ActiveRun,
			RunCount:       dto.RunCount,
			Repos:          append([]string(nil), dto.Repos...),
			CreatedAt:      dto.CreatedAt,
			AttentionCount: attention[dto.ID],
			Progress:       dto.Progress,
		})
	}
	sort.Slice(features, func(i, j int) bool {
		if io, jo := apiFeatureSortOrder(features[i].Status), apiFeatureSortOrder(features[j].Status); io != jo {
			return io < jo
		}
		if features[i].AttentionCount != features[j].AttentionCount {
			return features[i].AttentionCount > features[j].AttentionCount
		}
		return features[i].CreatedAt.After(features[j].CreatedAt)
	})
	providers := append([]string(nil), m.runtimeConfig.Providers...)
	if len(providers) == 0 {
		providers = append(providers, m.catalog.ProviderOrder...)
	}
	selected := preferredFeatureID
	if !apiHasFeature(features, selected) {
		selected = ""
		if len(features) > 0 {
			selected = features[0].ID
		}
	}
	var detail *APIFeatureDetailPresentation
	if selected != "" {
		if resp, ok := m.featureDetails[selected]; ok {
			presentation := apiFeatureDetailPresentation(resp.Feature)
			detail = &presentation
		}
	}
	sessions := apiSessionPresentations(m.sessionList, m.sessionDetails, selected)
	var livePreview *APILivePreviewPresentation
	var transcript *APITranscriptPresentation
	var content *APIContentPresentation
	if selected != "" {
		if preview, ok := m.livePreviews[selected]; ok {
			presentation := apiLivePreviewPresentation(selected, preview)
			livePreview = &presentation
			if livePreview.SessionID != "" {
				if resp, ok := m.transcripts[livePreview.SessionID]; ok {
					presentation := apiTranscriptPresentation(livePreview.SessionID, resp)
					transcript = &presentation
				}
			}
		}
		if resp, ok := m.contents[selected]; ok {
			presentation := apiContentPresentation(resp)
			content = &presentation
		}
	}
	m.snapshot = APIAppSnapshot{
		Runtime: APIRuntimePresentation{
			Providers:                  providers,
			DangerouslySkipPermissions: m.launchPolicy.DangerouslySkipPermissions,
			OwnedServer:                m.ownedServer,
		},
		Features:    features,
		Detail:      detail,
		Sessions:    sessions,
		LivePreview: livePreview,
		Transcript:  transcript,
		Content:     content,
	}
	m.selectedFeature = selected
}

func newAPIRecoveryPanel(snapshot server.RecoverySnapshotResponse) *apiRecoveryPanel {
	actions := make(map[string]string, len(snapshot.Items))
	items := append([]server.RecoveryItemDTO(nil), snapshot.Items...)
	for i, item := range items {
		if len(item.AllowedActions) == 0 {
			if item.Tweak {
				item.AllowedActions = []string{"kill"}
			} else {
				item.AllowedActions = []string{"resume", "kill", "skip"}
			}
			items[i] = item
		}
		action := item.DefaultAction
		if action == "" {
			action = "skip"
			if item.Tweak {
				action = "kill"
			}
		}
		actions[item.Key] = action
	}
	return &apiRecoveryPanel{
		snapshotID: snapshot.SnapshotID,
		items:      items,
		actions:    actions,
	}
}

func mergeAPIRecoveryPanel(existing *apiRecoveryPanel, snapshot server.RecoverySnapshotResponse) *apiRecoveryPanel {
	if len(snapshot.Items) == 0 {
		return nil
	}
	panel := newAPIRecoveryPanel(snapshot)
	if existing == nil {
		return panel
	}
	panel.cursor = min(existing.cursor, len(panel.items)-1)
	if panel.cursor < 0 {
		panel.cursor = 0
	}
	existingActions := copyStringMapValues(existing.actions)
	for _, item := range panel.items {
		action, ok := existingActions[item.Key]
		if !ok {
			continue
		}
		for _, allowed := range item.AllowedActions {
			if allowed == action {
				panel.actions[item.Key] = action
				break
			}
		}
	}
	return panel
}

func (m APIAppModel) handleAPIRecoveryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	panel := m.recoveryPanel
	if panel == nil {
		return m, nil
	}
	switch strings.ToLower(msg.Text) {
	case "r":
		panel.setAction("resume")
		return m, nil
	case "k":
		panel.setAction("kill")
		return m, nil
	case "s":
		panel.setAction("skip")
		return m, nil
	}
	switch msg.Code {
	case tea.KeyUp:
		if panel.cursor > 0 {
			panel.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if panel.cursor < len(panel.items)-1 {
			panel.cursor++
		}
		return m, nil
	case tea.KeyEnter:
		return m, m.executeRecoveryCmd(*panel)
	}
	return m, nil
}

func (p *apiRecoveryPanel) setAction(action string) {
	if p == nil || p.cursor < 0 || p.cursor >= len(p.items) {
		return
	}
	item := p.items[p.cursor]
	for _, allowed := range item.AllowedActions {
		if allowed == action {
			p.actions[item.Key] = action
			return
		}
	}
}

func (m APIAppModel) renderAPIRecovery() string {
	if m.recoveryPanel == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(TitleStyle.Render(" Session Recovery"))
	b.WriteString("\n\n")
	for i, item := range m.recoveryPanel.items {
		cursor := "  "
		if i == m.recoveryPanel.cursor {
			cursor = SelectedRowStyle.Render("\u25b8 ")
		}
		name := firstNonEmpty(item.FeatureName, item.FeatureID)
		repoSuffix := ""
		if item.RepoName != "" {
			repoSuffix = " (" + item.RepoName + ")"
		}
		alive := MutedStyle.Render("NOT running")
		if item.ProcessAlive {
			alive = WarningStyle.Render("STILL RUNNING (orphan)")
		}
		action := apiRecoveryActionLabel(m.recoveryPanel.actions[item.Key])
		content := fmt.Sprintf("%s%d. %s (%s, iter %d)%s\n", cursor, i+1, name, item.Phase, item.Iteration, repoSuffix)
		content += fmt.Sprintf("   PID %d: %s\n", item.PID, alive)
		content += fmt.Sprintf("   Action: %s", action)
		if item.Tweak {
			content += "\n   " + MutedStyle.Render("(interactive tweak - kill only)")
		}
		b.WriteString(panelStyle(i == m.recoveryPanel.cursor).Render(content))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(KeyHelpStyle.Render(apiRecoveryFooter(m.recoveryPanel.currentAllowedActions())))
	b.WriteString("\n")
	return b.String()
}

func apiRecoveryActionLabel(action string) string {
	switch action {
	case "resume":
		return "[R]esume"
	case "kill":
		return "[K]ill"
	case "skip":
		return "[S]kip"
	default:
		return strings.TrimSpace(action)
	}
}

func (p *apiRecoveryPanel) currentAllowedActions() []string {
	if p == nil || p.cursor < 0 || p.cursor >= len(p.items) {
		return nil
	}
	return append([]string(nil), p.items[p.cursor].AllowedActions...)
}

func apiRecoveryFooter(allowed []string) string {
	parts := make([]string, 0, len(allowed)+1)
	for _, action := range allowed {
		switch action {
		case "resume":
			parts = append(parts, "[r] Resume")
		case "kill":
			parts = append(parts, "[k] Kill")
		case "skip":
			parts = append(parts, "[s] Skip")
		}
	}
	parts = append(parts, "[enter] Continue")
	return " " + strings.Join(parts, "   ")
}

func copyStringMapValues(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (m *APIAppModel) upsertFeatureSummary(summary server.FeatureSummary) {
	if summary.ID == "" {
		return
	}
	for i := range m.featureList.Features {
		if m.featureList.Features[i].ID == summary.ID {
			m.featureList.Features[i] = summary
			return
		}
	}
	m.featureList.Features = append(m.featureList.Features, summary)
}

func (m *APIAppModel) upsertSessionSummary(summary server.SessionSummaryDTO) {
	if summary.ID == "" {
		return
	}
	for i := range m.sessionList.Sessions {
		if m.sessionList.Sessions[i].ID == summary.ID {
			m.sessionList.Sessions[i] = summary
			return
		}
	}
	m.sessionList.Sessions = append([]server.SessionSummaryDTO{summary}, m.sessionList.Sessions...)
}

func (m *APIAppModel) moveSelection(delta int) {
	if delta == 0 || len(m.snapshot.Features) == 0 {
		return
	}
	idx := 0
	for i, f := range m.snapshot.Features {
		if f.ID == m.selectedFeature {
			idx = i
			break
		}
	}
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(m.snapshot.Features) {
		idx = len(m.snapshot.Features) - 1
	}
	m.selectedFeature = m.snapshot.Features[idx].ID
}

func (m APIAppModel) confirmSelectedFeatureAction(kind string) APIAppModel {
	return m.confirmSelectedFeatureActionWithArgs(kind, apiFeatureActionArgs{})
}

func (m APIAppModel) confirmSelectedFeatureActionWithArgs(kind string, args apiFeatureActionArgs) APIAppModel {
	if m.selectedFeature == "" {
		return m
	}
	if !m.selectedActionReady(kind) {
		m.statusMessage = apiMutationKindLabel(kind) + " is unavailable"
		return m
	}
	m.actionConfirmActive = true
	m.actionConfirmKind = kind
	m.actionConfirmFeatureID = m.selectedFeature
	m.actionConfirmFeatureName = m.selectedFeatureName()
	m.actionConfirmArgs = args
	return m
}

func (m *APIAppModel) clearActionConfirm() {
	m.actionConfirmActive = false
	m.actionConfirmKind = ""
	m.actionConfirmFeatureID = ""
	m.actionConfirmFeatureName = ""
	m.actionConfirmArgs = apiFeatureActionArgs{}
}

func (m APIAppModel) openTweakReviewModal() APIAppModel {
	if m.selectedFeature == "" {
		return m
	}
	if !m.selectedActionReady("feature.tweak.finish") {
		m.statusMessage = apiMutationKindLabel("feature.tweak.finish") + " is unavailable"
		return m
	}
	m.tweakReviewModalActive = true
	m.tweakReviewFeatureID = m.selectedFeature
	m.tweakReviewFeatureName = m.selectedFeatureName()
	return m
}

func (m *APIAppModel) clearTweakReviewModal() {
	m.tweakReviewModalActive = false
	m.tweakReviewFeatureID = ""
	m.tweakReviewFeatureName = ""
}

func (m *APIAppModel) clearNeedInputPrompt() {
	m.needInputPromptActive = false
	m.needInputFeatureID = ""
	m.needInputFeatureName = ""
	m.needInputGate = server.NeedInputGateDTO{}
}

func (m *APIAppModel) clearPermissionPrompt() {
	m.permissionPromptActive = false
	m.permissionFeatureID = ""
	m.permissionFeatureName = ""
	m.permissionRequest = server.ControlRequestDTO{}
}

func (m *APIAppModel) clearHelpPrompt() {
	m.helpPromptActive = false
	m.helpFeatureID = ""
	m.helpFeatureName = ""
	m.helpQuestion = ""
	m.helpAnswerDraft = ""
}

func (m *APIAppModel) clearAskUserPrompt() {
	m.askUserPromptActive = false
	m.askUserFeatureID = ""
	m.askUserFeatureName = ""
	m.askUserQuestion = ""
	m.askUserRequest = server.ControlRequestDTO{}
	m.askUserAnswerDraft = ""
}

func (m *APIAppModel) clearCreatePrompt() {
	m.wizard = nil
	m.wizardRuntimeConfigPending = false
}

func (m APIAppModel) openCreateFeaturePrompt(_ int) APIAppModel {
	wizard := m.newCreateFeatureWizard()
	if m.width > 0 {
		wizard.SetWidth(m.width)
	}
	if m.height > 0 {
		wizard.height = m.height
	}
	m.wizard = &wizard
	m.configEditor = nil
	m.runtimeConfigEditor = nil
	m.statusMessage = ""
	return m
}

func (m APIAppModel) newCreateFeatureWizard() WizardModel {
	availRepos, repoPaths, repoConfigs := apiRuntimeRepoState(m.runtimeConfig)

	existingSlugs := make(map[string]string, len(m.featureList.Features))
	for _, f := range m.featureList.Features {
		if f.Slug != "" {
			existingSlugs[f.Slug] = f.Name
		}
	}

	cat := apiPhaseModelCatalog(m.catalog)
	return NewWizardModel(
		availRepos,
		repoPaths,
		repoConfigs,
		apiWizardDefaults(m.runtimeConfig),
		"",
		cat.ProviderModels,
		cat.ProviderOrder,
		cat.PhaseDefaults,
		cat.PhaseProviderModels,
		existingSlugs,
		append([]string(nil), m.runtimeConfig.WorkspaceRoots...),
	)
}

func apiRuntimeRepoState(runtime server.RuntimeConfigResponse) ([]string, map[string]string, map[string]config.RepoConfig) {
	availRepos := make([]string, 0, len(runtime.Repos))
	repoPaths := make(map[string]string, len(runtime.Repos))
	repoConfigs := make(map[string]config.RepoConfig, len(runtime.Repos))
	for _, repo := range runtime.Repos {
		if repo.Name == "" {
			continue
		}
		availRepos = append(availRepos, repo.Name)
		repoPaths[repo.Name] = repo.Path
		repoConfigs[repo.Name] = config.RepoConfig{
			Path:          repo.Path,
			PipelineGates: copyConfigPipelineGates(repo.PipelineGates),
		}
	}
	sort.Strings(availRepos)
	return availRepos, repoPaths, repoConfigs
}

func (m *APIAppModel) refreshAPIWizardRepos(createdRepoPath string) {
	if m.wizard == nil {
		return
	}
	availRepos, repoPaths, repoConfigs := apiRuntimeRepoState(m.runtimeConfig)
	m.wizard.RefreshRepos(availRepos, repoPaths, repoConfigs)
	m.wizard.SetWorkspaceRoots(m.runtimeConfig.WorkspaceRoots)
	if createdRepoPath != "" {
		m.wizard.AutoSelectCreatedRepo(createdRepoPath, repoPaths)
		m.wizard.Advance()
	}
}

func (m APIAppModel) openAPIWorkspaceManager() APIAppModel {
	manager := NewWorkspaceManagerModel(apiWorkspaceRoots(m.runtimeConfig.WorkspaceRoots), m.width, m.height)
	m.workspaceManager = &manager
	m.configEditor = nil
	m.runtimeConfigEditor = nil
	m.statusMessage = ""
	return m
}

func apiWorkspaceRoots(paths []string) []workspaceRoot {
	roots := make([]workspaceRoot, 0, len(paths))
	for _, path := range paths {
		expanded := config.ExpandHome(path)
		roots = append(roots, workspaceRoot{
			Path:      expanded,
			RepoCount: countGitReposInDir(expanded),
			IsRepo:    isGitRepo(expanded),
		})
	}
	return roots
}

func (m APIAppModel) updateAPIWorkspaceManager(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.workspaceManager == nil {
		return m, nil
	}
	manager, cmd := m.workspaceManager.Update(msg)
	m.workspaceManager = &manager
	var cmds []tea.Cmd
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	if root := m.workspaceManager.ConsumeAddedRoot(); root != "" {
		roots := append([]string(nil), m.runtimeConfig.WorkspaceRoots...)
		if !containsRootExpanded(roots, root) {
			roots = append(roots, root)
			m.runtimeConfig.WorkspaceRoots = roots
			m.workspaceManager.SetRoots(apiWorkspaceRoots(roots))
			cmds = append(cmds, m.persistRuntimeWorkspaceRootsCmd(roots, ""))
		}
	}

	if root := m.workspaceManager.ConsumeRemovedRoot(); root != "" {
		roots := removeRoot(m.runtimeConfig.WorkspaceRoots, root)
		m.runtimeConfig.WorkspaceRoots = roots
		m.workspaceManager.SetRoots(apiWorkspaceRoots(roots))
		cmds = append(cmds, m.persistRuntimeWorkspaceRootsCmd(roots, ""))
	}

	if m.workspaceManager.IsClosed() {
		m.workspaceManager = nil
	}

	return m, tea.Batch(cmds...)
}

func (m APIAppModel) persistRuntimeWorkspaceRootsCmd(roots []string, createdRepoPath string) tea.Cmd {
	roots = append([]string(nil), roots...)
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		_, err := m.client.UpdateRuntimeConfig(ctx, server.RuntimeConfigMutationRequest{
			WorkspaceRoots: &roots,
		})
		if err != nil {
			return apiRuntimeConfigMutationMsg{kind: "runtime.config.update", createdRepoPath: createdRepoPath, err: err}
		}
		cfg, err := m.client.RuntimeConfig(ctx)
		return apiRuntimeConfigMutationMsg{
			kind:            "runtime.config.update",
			config:          cfg,
			createdRepoPath: createdRepoPath,
			err:             err,
		}
	}
}

func apiWizardDefaults(runtime server.RuntimeConfigResponse) config.DefaultsConfig {
	defaults := config.DefaultsConfig{
		Models:              runtime.FeatureDefaults.Models,
		PipelinePreferences: runtime.FeatureDefaults.PipelinePreferences,
		Inquireness:         runtime.FeatureDefaults.Inquireness,
		Pipeline:            runtime.FeatureDefaults.Pipeline,
		Checkpoints:         runtime.FeatureDefaults.Checkpoints,
	}
	if defaults.Models == (config.ModelConfig{}) {
		defaults.Models = runtime.Defaults
	}
	return defaults
}

func copyConfigPipelineGates(in map[string]config.Checkpoints) map[string]config.Checkpoints {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]config.Checkpoints, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func copyBoolMap(in map[string]bool) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (m APIAppModel) openNeedInputPrompt() APIAppModel {
	if m.selectedFeature == "" {
		m.statusMessage = "No feature selected"
		return m
	}
	gate, ok := m.selectedNeedInputGate(m.selectedFeature)
	if !ok {
		m.statusMessage = "No need-user-input gate for selected feature"
		return m
	}
	m.needInputPromptActive = true
	m.needInputFeatureID = m.selectedFeature
	m.needInputFeatureName = m.selectedFeatureName()
	m.needInputGate = gate
	m.statusMessage = ""
	return m
}

func (m APIAppModel) openPermissionPrompt() APIAppModel {
	if m.selectedFeature == "" {
		m.statusMessage = "No feature selected"
		return m
	}
	req, ok := m.selectedPendingPermission(m.selectedFeature)
	if !ok {
		m.statusMessage = "No pending permission for selected feature"
		return m
	}
	m.permissionPromptActive = true
	m.permissionFeatureID = m.selectedFeature
	m.permissionFeatureName = m.selectedFeatureName()
	m.permissionRequest = req
	m.statusMessage = ""
	return m
}

func (m APIAppModel) openHelpPrompt() APIAppModel {
	if m.selectedFeature == "" {
		m.statusMessage = "No feature selected"
		return m
	}
	help, ok := m.selectedPendingHelp(m.selectedFeature)
	if !ok {
		m.statusMessage = "No pending help request for selected feature"
		return m
	}
	m.helpPromptActive = true
	m.helpFeatureID = m.selectedFeature
	m.helpFeatureName = m.selectedFeatureName()
	m.helpQuestion = help.Question
	m.helpAnswerDraft = ""
	m.statusMessage = ""
	return m
}

func (m APIAppModel) openAskUserPrompt() APIAppModel {
	if m.selectedFeature == "" {
		m.statusMessage = "No feature selected"
		return m
	}
	req, ok := m.selectedPendingAskUser(m.selectedFeature)
	if !ok {
		m.statusMessage = "No pending AskUser question for selected feature"
		return m
	}
	question := askUserQuestionLabel(req)
	if question == "" {
		m.statusMessage = "Pending AskUser question has no displayable question"
		return m
	}
	m.askUserPromptActive = true
	m.askUserFeatureID = m.selectedFeature
	m.askUserFeatureName = m.selectedFeatureName()
	m.askUserQuestion = question
	m.askUserRequest = req
	m.askUserAnswerDraft = ""
	m.askUserOptionCursor = 0
	m.statusMessage = ""
	return m
}

func (m APIAppModel) openPublishAction() APIAppModel {
	if m.selectedFeature == "" {
		m.statusMessage = "No feature selected"
		return m
	}
	if !m.selectedActionReady("feature.publish") {
		m.statusMessage = apiMutationKindLabel("feature.publish") + " is unavailable"
		return m
	}
	repos := m.selectedRepoActionOptions("feature.publish")
	if len(repos) > 1 {
		m.repoActionPanel = newAPIRepoActionPanel(m.selectedFeature, m.selectedFeatureName(), "feature.publish", repos, true)
		m.statusMessage = ""
		return m
	}
	args := apiFeatureActionArgs{}
	if len(repos) == 1 {
		args.Repos = []string{repos[0].Name}
	}
	return m.confirmSelectedFeatureActionWithArgs("feature.publish", args)
}

func (m APIAppModel) openRepoCycleAction(kind string) APIAppModel {
	if m.selectedFeature == "" {
		m.statusMessage = "No feature selected"
		return m
	}
	if !m.selectedActionReady(kind) {
		m.statusMessage = apiMutationKindLabel(kind) + " is unavailable"
		return m
	}
	repos := m.selectedRepoActionOptions(kind)
	if len(repos) > 1 {
		m.repoActionPanel = newAPIRepoActionPanel(m.selectedFeature, m.selectedFeatureName(), kind, repos, false)
		m.statusMessage = ""
		return m
	}
	args := apiFeatureActionArgs{}
	if len(repos) == 1 {
		args.Repo = repos[0].Name
	}
	return m.confirmSelectedFeatureActionWithArgs(kind, args)
}

func (m APIAppModel) openRewindPanel() APIAppModel {
	if m.selectedFeature == "" {
		m.statusMessage = "No feature selected"
		return m
	}
	if !m.selectedActionReady("feature.rewind") {
		m.statusMessage = apiMutationKindLabel("feature.rewind") + " is unavailable"
		return m
	}
	choices := m.selectedRewindChoices()
	if len(choices) == 0 {
		m.statusMessage = "selected feature has no rewind target phase"
		return m
	}
	if len(choices) == 1 {
		choice := choices[0]
		return m.confirmSelectedFeatureActionWithArgs("feature.rewind", apiFeatureActionArgs{
			TargetPhase:     choice.TargetPhase,
			UpgradePipeline: choice.UpgradePipeline,
		})
	}
	m.rewindPanel = &apiRewindPanel{
		featureID:   m.selectedFeature,
		featureName: m.selectedFeatureName(),
		choices:     choices,
	}
	m.statusMessage = ""
	return m
}

func (m APIAppModel) openRuntimeConfigEditor() APIAppModel {
	m.runtimeConfigEditor = newAPIRuntimeConfigEditor(m.runtimeConfig.Defaults, apiPhaseModelCatalog(m.catalog))
	m.configEditor = nil
	m.statusMessage = ""
	return m
}

func (m APIAppModel) transitionToAPIHelpOverlay() (tea.Model, tea.Cmd) {
	contexts := AllHelpContexts()
	ctxName := "Dashboard"
	switch {
	case m.contentPanelActive:
		ctxName = "Logs"
	case m.recoveryPanel != nil:
		ctxName = "Recovery"
	case m.reviewComments != nil:
		ctxName = "Review Comments"
	case m.wizard != nil:
		ctxName = "Wizard"
	case m.focusPanel == 1:
		ctxName = "Detail Panel"
	}
	ctx, ok := contexts[ctxName]
	if !ok {
		ctx = contexts["Dashboard"]
	}
	m.helpOverlay = NewHelpOverlayModel(ctx, m.width, m.height)
	m.helpOverlayActive = true
	return m, nil
}

func (m APIAppModel) confirmResumeAll() (tea.Model, tea.Cmd) {
	m.resumeAllFeatureIDs = m.resumeAllCandidates()
	m.resumeAllConfirmActive = true
	return m, nil
}

func (m APIAppModel) resumeAllCandidates() []string {
	ids := make([]string, 0)
	for _, summary := range m.featureList.Features {
		if apiFeatureResumeAllKind(summary.Status) != "" {
			ids = append(ids, summary.ID)
		}
	}
	return ids
}

func apiFeatureResumeAllKind(status string) string {
	switch apiFeatureStatus(status) {
	case feature.StatusInterrupted, feature.StatusNeedUserInput:
		return "feature.resume"
	case feature.StatusFailed:
		return "feature.retry"
	default:
		return ""
	}
}

func (m APIAppModel) resumeAllCmd(featureIDs []string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		statusByFeature := map[string]string{}
		for _, summary := range m.featureList.Features {
			statusByFeature[summary.ID] = summary.Status
		}
		var result apiResumeAllResultMsg
		for _, featureID := range featureIDs {
			kind := apiFeatureResumeAllKind(statusByFeature[featureID])
			if kind == "" {
				continue
			}
			var err error
			switch kind {
			case "feature.resume":
				_, err = m.client.ResumeFeature(ctx, featureID)
			case "feature.retry":
				_, err = m.client.RetryFeature(ctx, featureID)
			}
			if err != nil {
				result.failed = append(result.failed, fmt.Sprintf("%s: %s", featureID, firstLine(err.Error())))
				continue
			}
			result.succeeded = append(result.succeeded, featureID)
		}
		return result
	}
}

func (m APIAppModel) toggleAPIInputNotificationsCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		_, err := m.client.ToggleInputNotifications(ctx, featureID)
		return apiMutationResultMsg{
			kind:      "feature.input_notifications.toggle",
			featureID: featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) openSelectedDiff() (APIAppModel, tea.Cmd) {
	f := m.selectedAPIDashboardFeature()
	if f == nil {
		m.statusMessage = "No feature selected"
		return m, nil
	}
	if f.Status != feature.StatusCodeReady || len(f.Repos) == 0 || f.Repos[0].WorktreePath == "" {
		m.statusMessage = "Diff is only available for Code Ready features with a worktree"
		return m, nil
	}
	m.textPanelActive = true
	m.textPanelTitle = fmt.Sprintf("Diff: %s", firstNonEmpty(f.Slug, f.Name, f.ID))
	m.textPanelContent = MutedStyle.Render("Loading diff...")
	return m, func() tea.Msg {
		diff, err := git.DiffSummary(f.Repos[0].WorktreePath, f.Repos[0].BaseBranch)
		if err != nil || strings.TrimSpace(diff) == "" {
			diff = MutedStyle.Render("No changes found")
		} else {
			diff = colorizeDiff(diff)
		}
		return apiTextPanelMsg{title: fmt.Sprintf("Diff: %s", firstNonEmpty(f.Slug, f.Name, f.ID)), content: diff}
	}
}

func (m APIAppModel) openAPIContextualAction() (tea.Model, tea.Cmd) {
	if m.selectedFeature == "" {
		m.statusMessage = "No feature selected"
		return m, nil
	}
	if _, ok := m.selectedPendingPermission(m.selectedFeature); ok {
		return m.openPermissionPrompt(), nil
	}
	if _, ok := m.selectedPendingAskUser(m.selectedFeature); ok {
		return m.openAskUserPrompt(), nil
	}
	if _, ok := m.selectedPendingHelp(m.selectedFeature); ok {
		return m.openHelpPrompt(), nil
	}
	if _, ok := m.selectedNeedInputGate(m.selectedFeature); ok {
		return m.openNeedInputPrompt(), nil
	}
	if m.selectedActionReady("feature.resume") {
		return m, m.primarySelectedFeatureActionCmd("feature.resume", m.selectedFeature)
	}
	if m.selectedActionReady("feature.retry") {
		return m, m.selectedFeatureActionCmd("feature.retry", m.selectedFeature)
	}
	if m.selectedActionReady("feature.start") {
		return m, m.primarySelectedFeatureActionCmd("feature.start", m.selectedFeature)
	}
	if f := m.selectedAPIDashboardFeature(); isLivePreviewEligible(f) {
		m.focusPanel = 1
		m.statusMessage = "Live preview is already visible"
		return m, nil
	}
	m.statusMessage = "No contextual action for selected feature"
	return m, nil
}

func (m APIAppModel) showAPIOverview() APIAppModel {
	if f := m.selectedAPIDashboardFeature(); isLivePreviewEligible(f) {
		m.rightPanelMode = dashboardRightPanelOverview
		m.focusPanel = 1
		m.statusMessage = ""
		return m
	}
	m.statusMessage = "Overview is only available for live features"
	return m
}

func (m APIAppModel) cycleSelectedArtifact(delta int) (APIAppModel, tea.Cmd) {
	content, ok := m.selectedContentSnapshot()
	if !ok {
		m.statusMessage = "No run content for selected feature"
		return m, nil
	}
	m.contentPanelActive = true
	artifacts := availableTextArtifacts(content.Artifacts)
	if len(artifacts) == 0 {
		m.statusMessage = "No text artifacts for selected run"
		return m, nil
	}
	if len(artifacts) == 1 {
		m.statusMessage = "Only one text artifact for selected run"
		return m, nil
	}
	artifact := cycleArtifactSelection(artifacts, content.ArtifactID, delta)
	return m, m.fetchArtifactContentCmd(content, artifact)
}

func (m APIAppModel) cycleSelectedLog() (APIAppModel, tea.Cmd) {
	content, ok := m.selectedContentSnapshot()
	if !ok {
		m.statusMessage = "No run content for selected feature"
		return m, nil
	}
	m.contentPanelActive = true
	return m, m.fetchNextLogContentCmd(content)
}

func (m APIAppModel) selectedContentSnapshot() (apiFeatureContentSnapshot, bool) {
	if m.selectedFeature == "" {
		return apiFeatureContentSnapshot{}, false
	}
	content, ok := m.contents[m.selectedFeature]
	if !ok || content.RunNumber <= 0 {
		return apiFeatureContentSnapshot{}, false
	}
	if content.LogID == "" && content.Log != nil {
		content.LogID = content.Log.ID
	}
	if content.ArtifactID == "" {
		switch {
		case content.ArtifactMeta != nil && content.ArtifactMeta.ID != "":
			content.ArtifactID = content.ArtifactMeta.ID
		case content.Artifact != nil:
			content.ArtifactID = content.Artifact.ID
		}
	}
	return content, true
}

func (m APIAppModel) openReviewCommentsPreview() (APIAppModel, tea.Cmd) {
	if m.selectedFeature == "" {
		m.statusMessage = "No feature selected"
		return m, nil
	}
	if !m.selectedActionReady("feature.review_comments") {
		m.statusMessage = apiMutationKindLabel("feature.review_comments") + " is unavailable"
		return m, nil
	}
	repos := m.selectedRepoActionOptions("feature.review_comments")
	if len(repos) > 1 {
		m.repoActionPanel = newAPIRepoActionPanel(m.selectedFeature, m.selectedFeatureName(), "feature.review_comments", repos, false)
		m.statusMessage = ""
		return m, nil
	}
	repo, mode, modes, ok := m.selectedReviewCommentsDefaults()
	if !ok {
		m.statusMessage = "review-comments requires a repo"
		return m, nil
	}
	m.reviewComments = nil
	m.statusMessage = "Fetching review comments..."
	return m, m.fetchReviewCommentsCmd(m.selectedFeature, m.selectedFeatureName(), repo, mode, modes)
}

func (m APIAppModel) openRefactorPrompt() APIAppModel {
	return m.openRefactorPromptFor(false)
}

func (m APIAppModel) openRefactorRestartPrompt() APIAppModel {
	return m.openRefactorPromptFor(true)
}

func (m APIAppModel) openRefactorPromptFor(restart bool) APIAppModel {
	if m.selectedFeature == "" {
		m.statusMessage = "No feature selected"
		return m
	}
	kind := "feature.refactor.start"
	if restart {
		kind = "feature.refactor.restart"
	}
	if !m.selectedActionReady(kind) {
		m.statusMessage = apiMutationKindLabel(kind) + " is unavailable"
		return m
	}
	repos := m.selectedRepoActionOptions(kind)
	if len(repos) > 1 {
		m.repoActionPanel = newAPIRepoActionPanel(m.selectedFeature, m.selectedFeatureName(), kind, repos, false)
		m.refactorPrompt = nil
		m.refactorPipeline = nil
		m.statusMessage = ""
		return m
	}
	repo := ""
	if len(repos) == 1 {
		repo = repos[0].Name
	}
	return m.openRefactorPromptForRepo(kind, repo, restart)
}

func (m APIAppModel) openRefactorPromptForRepo(kind, repo string, restart bool) APIAppModel {
	pipelines, ok := m.selectedRefactorPipelines(kind)
	if !ok {
		m.statusMessage = "refactor action is unavailable"
		return m
	}
	ta := newStyledTextarea()
	ta.Placeholder = "Describe the refactoring for " + repo + "..."
	ta.SetWidth(max(m.width-12, 20))
	if m.width >= 100 {
		ta.SetWidth(max((m.width/2)-10, 20))
	}
	ta.SetHeight(5)
	ta.Focus()
	m.refactorPrompt = &apiRefactorPrompt{
		featureID:   m.selectedFeature,
		featureName: m.selectedFeatureName(),
		repo:        repo,
		input:       ta,
		pipelines:   pipelines,
		restart:     restart,
	}
	m.refactorPipeline = nil
	m.statusMessage = ""
	return m
}

func newAPIRepoActionPanel(featureID, featureName, kind string, repos []apiRepoActionOption, multi bool) *apiRepoActionPanel {
	selected := map[string]bool{}
	if multi {
		for _, repo := range repos {
			if repo.Name != "" && (kind != "feature.publish" || repo.Publishable) {
				selected[repo.Name] = true
			}
		}
		if len(selected) == 0 {
			for _, repo := range repos {
				if repo.Name != "" {
					selected[repo.Name] = true
				}
			}
		}
	}
	return &apiRepoActionPanel{
		featureID:   featureID,
		featureName: featureName,
		kind:        kind,
		repos:       append([]apiRepoActionOption(nil), repos...),
		multi:       multi,
		selected:    selected,
	}
}

func (m APIAppModel) selectedRepoActionOptions(kind string) []apiRepoActionOption {
	byName := map[string]apiRepoActionOption{}
	order := []string{}
	add := func(option apiRepoActionOption) {
		option.Name = strings.TrimSpace(option.Name)
		if option.Name == "" {
			return
		}
		if _, ok := byName[option.Name]; !ok {
			order = append(order, option.Name)
		}
		byName[option.Name] = option
	}
	if detail, ok := m.featureDetails[m.selectedFeature]; ok {
		for _, repo := range detail.Feature.RepoStatus {
			stateParts := make([]string, 0, 3)
			if repo.CycleType != "" || repo.CycleStatus != "" {
				stateParts = append(stateParts, strings.Trim(strings.TrimSpace(repo.CycleType)+"/"+strings.TrimSpace(repo.CycleStatus), "/"))
			}
			if repo.LastError != "" {
				stateParts = append(stateParts, firstLine(repo.LastError))
			}
			if repo.PRURL != "" {
				stateParts = append(stateParts, apiPRNumberLabel(repo.PRURL))
			}
			add(apiRepoActionOption{
				Name:        repo.Name,
				State:       strings.Join(nonEmptyStrings(stateParts), " "),
				Publishable: repo.Publishable,
				PRURL:       repo.PRURL,
			})
		}
	}
	for _, feature := range m.featureList.Features {
		if feature.ID != m.selectedFeature {
			continue
		}
		for _, repo := range feature.Repos {
			if _, ok := byName[repo]; !ok {
				add(apiRepoActionOption{Name: repo})
			}
		}
	}
	for _, feature := range m.snapshot.Features {
		if feature.ID != m.selectedFeature {
			continue
		}
		for _, repo := range feature.Repos {
			if _, ok := byName[repo]; !ok {
				add(apiRepoActionOption{Name: repo})
			}
		}
	}
	options := make([]apiRepoActionOption, 0, len(order))
	for _, name := range order {
		options = append(options, byName[name])
	}
	if kind == "feature.review_comments" {
		publishable := options[:0]
		for _, option := range options {
			if option.Publishable {
				publishable = append(publishable, option)
			}
		}
		if len(publishable) > 0 {
			return publishable
		}
	}
	return options
}

func (m APIAppModel) selectedReviewCommentsDefaults() (string, string, []string, bool) {
	action, ok := m.selectedRawAction("feature.review_comments")
	if !ok || !action.Enabled {
		return "", "", nil, false
	}
	repoRequired := false
	if ok {
		for _, input := range action.RequiredInputs {
			switch input.Name {
			case "repo":
				repoRequired = input.Required
			}
		}
	}
	repo := m.selectedReviewCommentsRepo()
	if repo == "" && repoRequired {
		_, modes := m.selectedReviewCommentsModeDefaults()
		return "", "", modes, false
	}
	mode, modes := m.selectedReviewCommentsModeDefaults()
	return repo, mode, modes, true
}

func (m APIAppModel) selectedReviewCommentsModeDefaults() (string, []string) {
	modes := apiReviewCommentModes(nil)
	action, ok := m.selectedRawAction("feature.review_comments")
	if ok {
		for _, input := range action.RequiredInputs {
			if input.Name == "mode" {
				modes = apiReviewCommentModes(input.Options)
				break
			}
		}
	}
	return modes[0], modes
}

func (m APIAppModel) selectedRefactorPipelines(kind string) ([]feature.PipelineProfile, bool) {
	action, ok := m.selectedRawAction(kind)
	if !ok || !action.Enabled {
		return nil, false
	}
	pipelines := apiRefactorPipelines(nil)
	for _, input := range action.RequiredInputs {
		if input.Name == "pipeline" {
			pipelines = apiRefactorPipelines(input.Options)
			break
		}
	}
	return pipelines, true
}

func (m APIAppModel) selectedRawAction(kind string) (server.ActionDTO, bool) {
	detail, ok := m.featureDetails[m.selectedFeature]
	if !ok {
		return server.ActionDTO{}, false
	}
	for _, action := range detail.Feature.Actions {
		if apiActionMatchesMutationKind(action.ID, kind) {
			return action, true
		}
	}
	return server.ActionDTO{}, false
}

func (m APIAppModel) selectedReviewCommentsRepo() string {
	if detail, ok := m.featureDetails[m.selectedFeature]; ok {
		for _, repo := range detail.Feature.RepoStatus {
			if repo.Name != "" && repo.Publishable {
				return repo.Name
			}
		}
		for _, repo := range detail.Feature.RepoStatus {
			if repo.Name != "" {
				return repo.Name
			}
		}
	}
	for _, feature := range m.featureList.Features {
		if feature.ID == m.selectedFeature && len(feature.Repos) > 0 {
			return feature.Repos[0]
		}
	}
	for _, feature := range m.snapshot.Features {
		if feature.ID == m.selectedFeature && len(feature.Repos) > 0 {
			return feature.Repos[0]
		}
	}
	return ""
}

func (m APIAppModel) selectedRefactorRepo() string {
	if detail, ok := m.featureDetails[m.selectedFeature]; ok {
		for _, repo := range detail.Feature.RepoStatus {
			if strings.TrimSpace(repo.Name) != "" {
				return repo.Name
			}
		}
	}
	for _, feature := range m.featureList.Features {
		if feature.ID == m.selectedFeature && len(feature.Repos) > 0 {
			return feature.Repos[0]
		}
	}
	for _, feature := range m.snapshot.Features {
		if feature.ID == m.selectedFeature && len(feature.Repos) > 0 {
			return feature.Repos[0]
		}
	}
	return ""
}

func (m APIAppModel) selectedRebaseRepo(featureID string) string {
	if detail, ok := m.featureDetails[featureID]; ok {
		for _, repo := range detail.Feature.RepoStatus {
			if strings.TrimSpace(repo.Name) != "" && repo.Publishable {
				return repo.Name
			}
		}
		for _, repo := range detail.Feature.RepoStatus {
			if strings.TrimSpace(repo.Name) != "" {
				return repo.Name
			}
		}
	}
	for _, feature := range m.featureList.Features {
		if feature.ID == featureID && len(feature.Repos) > 0 {
			return feature.Repos[0]
		}
	}
	for _, feature := range m.snapshot.Features {
		if feature.ID == featureID && len(feature.Repos) > 0 {
			return feature.Repos[0]
		}
	}
	return ""
}

func (m APIAppModel) selectedFeatureName() string {
	return m.featureNameByID(m.selectedFeature)
}

func (m APIAppModel) featureNameByID(featureID string) string {
	if featureID == "" {
		return ""
	}
	if m.snapshot.Detail != nil && m.snapshot.Detail.ID == featureID && m.snapshot.Detail.Name != "" {
		return m.snapshot.Detail.Name
	}
	for _, f := range m.snapshot.Features {
		if f.ID == featureID {
			if f.Name != "" {
				return f.Name
			}
			if f.Slug != "" {
				return f.Slug
			}
		}
	}
	return featureID
}

func (m APIAppModel) selectedActionReady(kind string) bool {
	if kind == "feature.tweak.finish" {
		return m.selectedFeatureHasTweakCycle(m.selectedFeature)
	}
	if m.snapshot.Detail != nil {
		sawAction := false
		for _, action := range m.snapshot.Detail.Actions {
			if apiActionMatchesMutationKind(action.ID, kind) {
				sawAction = true
				return action.Status == "" || action.Status == "ready"
			}
		}
		if sawAction {
			return false
		}
	}
	switch kind {
	case "feature.start":
		status, ok := m.selectedFeatureStatus(m.selectedFeature)
		return ok && status == feature.StatusCreated
	case "feature.resume":
		status, ok := m.selectedFeatureStatus(m.selectedFeature)
		return ok && (status == feature.StatusInterrupted || status == feature.StatusNeedUserInput)
	case "feature.publish":
		return m.selectedFeature != ""
	case "feature.merge":
		return m.selectedFeature != ""
	case "feature.restart":
		return m.selectedFeature != ""
	case "feature.retry":
		status, ok := m.selectedFeatureStatus(m.selectedFeature)
		return ok && status == feature.StatusFailed
	case "feature.mark-done":
		return m.selectedFeature != ""
	case "feature.rebase":
		return m.selectedFeature != ""
	case "feature.cleanup":
		return m.selectedFeature != ""
	case "feature.review_comments":
		action, ok := m.selectedRawAction(kind)
		return ok && action.Enabled
	case "feature.tweak.start":
		return m.selectedFeature != ""
	case "feature.refactor.start":
		return m.selectedFeature != ""
	case "feature.refactor.restart":
		return m.selectedFeature != ""
	case "feature.rewind":
		return m.selectedFeatureCurrentPhase(m.selectedFeature) != ""
	case "feature.stop":
		for _, f := range m.snapshot.Features {
			if f.ID == m.selectedFeature {
				return apiFeatureCanStop(f.Status)
			}
		}
	case "feature.delete":
		return m.selectedFeature != ""
	}
	return false
}

func (m APIAppModel) selectedFeatureHasTweakCycle(featureID string) bool {
	if featureID == "" {
		return false
	}
	detail, ok := m.featureDetails[featureID]
	if !ok {
		return false
	}
	if detail.Feature.Cycle != nil && strings.EqualFold(detail.Feature.Cycle.Type, "tweak") {
		return true
	}
	for _, repo := range detail.Feature.RepoStatus {
		if strings.EqualFold(repo.CycleType, "tweak") {
			return true
		}
	}
	return false
}

func apiActionMatchesMutationKind(actionID, kind string) bool {
	if actionID == kind {
		return true
	}
	switch kind {
	case "feature.start":
		return actionID == "start"
	case "feature.resume":
		return actionID == "resume"
	case "feature.publish":
		return actionID == "publish"
	case "feature.merge":
		return actionID == "merge"
	case "feature.restart":
		return actionID == "restart"
	case "feature.retry":
		return actionID == "retry"
	case "feature.rebase":
		return actionID == "rebase"
	case "feature.mark-done":
		return actionID == "mark-done"
	case "feature.cleanup":
		return actionID == "cleanup"
	case "feature.review_comments":
		return actionID == "review-comments"
	case "feature.tweak.start":
		return actionID == "tweak"
	case "feature.tweak.finish":
		return actionID == "tweak"
	case "feature.refactor.start":
		return actionID == "refactor"
	case "feature.refactor.restart":
		return actionID == "refactor"
	case "feature.rewind":
		return actionID == "rewind"
	case "feature.stop":
		return actionID == "pause-stop" || actionID == "stop"
	case "feature.delete":
		return actionID == "delete"
	default:
		return false
	}
}

func (m APIAppModel) renderAPIRuntimeLine() string {
	providers := "none"
	if len(m.snapshot.Runtime.Providers) > 0 {
		providers = strings.Join(m.snapshot.Runtime.Providers, ", ")
	}
	ownership := "attached"
	if m.snapshot.Runtime.OwnedServer {
		ownership = "owned server"
	}
	return fmt.Sprintf("Runtime: %s   Providers: %s", ownership, providers)
}

func (m APIAppModel) renderAPIWorkspaceSummary() string {
	if len(m.runtimeConfig.Repos) == 0 {
		return MutedStyle.Render("Workspace: no configured repos")
	}
	names := make([]string, 0, len(m.runtimeConfig.Repos))
	for _, repo := range m.runtimeConfig.Repos {
		if repo.Name != "" {
			names = append(names, repo.Name)
		}
	}
	if len(names) == 0 {
		return MutedStyle.Render("Workspace: no configured repos")
	}
	if len(names) > 4 {
		names = append(names[:4], fmt.Sprintf("+%d more", len(names)-4))
	}
	return MutedStyle.Render("Workspace: " + strings.Join(names, ", "))
}

func (m APIAppModel) renderAPIFeatureList() string {
	if len(m.snapshot.Features) == 0 {
		return MutedStyle.Render("No features")
	}
	var b strings.Builder
	b.WriteString("Features\n")
	for _, f := range m.snapshot.Features {
		cursor := "  "
		if f.ID == m.selectedFeature {
			cursor = "> "
		}
		name := f.Name
		if name == "" {
			name = f.Slug
		}
		row := fmt.Sprintf("%s%-28s %-15s %-10s", cursor, truncatePlain(name, 28), f.Status, f.CurrentPhase)
		if f.AttentionCount > 0 {
			row += fmt.Sprintf("  %d attention", f.AttentionCount)
		}
		if f.ActiveRun > 0 || f.RunCount > 0 {
			row += fmt.Sprintf("  run %d/%d", f.ActiveRun, max(f.RunCount, f.ActiveRun))
		}
		b.WriteString(row)
		b.WriteByte('\n')
	}
	return b.String()
}

func (m APIAppModel) renderAPIFeatureDetail() string {
	detail := m.snapshot.Detail
	if detail == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nSelected detail\n")
	if detail.Description != "" {
		b.WriteString("  Description: " + detail.Description + "\n")
	}
	if detail.Summary != "" {
		b.WriteString("  Summary: " + detail.Summary + "\n")
	}
	if detail.Pipeline != "" {
		b.WriteString("  Pipeline: " + detail.Pipeline + "\n")
	}
	if len(detail.Repos) > 0 {
		repos := make([]string, 0, len(detail.Repos))
		for _, repo := range detail.Repos {
			label := repo.Name
			if repo.State != "" {
				label += " " + repo.State
			}
			repos = append(repos, label)
		}
		b.WriteString("  Repos: " + strings.Join(repos, ", ") + "\n")
	}
	if len(detail.Actions) > 0 {
		actions := make([]string, 0, len(detail.Actions))
		for _, action := range detail.Actions {
			label := strings.TrimSpace(action.ID + " " + action.Status)
			if action.Reason != "" {
				label += ": " + action.Reason
			}
			actions = append(actions, label)
		}
		b.WriteString("  Actions: " + strings.Join(actions, ", ") + "\n")
	}
	if detail.TotalCostUSD > 0 {
		b.WriteString(fmt.Sprintf("  Cost: $%.2f\n", detail.TotalCostUSD))
	}
	if detail.NeedUserInputLabel != "" {
		b.WriteString("  " + detail.NeedUserInputLabel + "\n")
	}
	if detail.Failure != "" {
		b.WriteString("  Failure: " + detail.Failure + "\n")
	}
	return b.String()
}

func (m APIAppModel) renderAPISessions() string {
	var b strings.Builder
	b.WriteString("Sessions\n")
	for _, sess := range m.snapshot.Sessions {
		label := firstNonEmpty(sess.Label, sess.Kind, "session")
		b.WriteString(fmt.Sprintf("  %s  %s  %s", sess.ID, label, sess.Status))
		if sess.Phase != "" {
			b.WriteString("  " + sess.Phase)
		}
		if sess.Repo != "" {
			b.WriteString("  " + sess.Repo)
		}
		if sess.ContextPct > 0 {
			b.WriteString(fmt.Sprintf("  %d%%", sess.ContextPct))
		}
		if sess.Provider != "" || sess.Model != "" {
			b.WriteString("  " + strings.TrimSpace(sess.Provider+" "+sess.Model))
		}
		var flags []string
		if sess.CanAttach {
			flags = append(flags, "attach")
		}
		if sess.LogAvailable {
			flags = append(flags, "log")
		}
		if len(flags) > 0 {
			b.WriteString("  " + strings.Join(flags, ","))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (m APIAppModel) renderAPILivePreview() string {
	preview := m.snapshot.LivePreview
	if preview == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Attach / Live Preview\n")
	if preview.Activity != "" {
		b.WriteString("  Activity: " + preview.Activity + "\n")
	}
	if preview.SessionID != "" {
		b.WriteString("  Session: " + preview.SessionID + "\n")
	}
	if preview.ContextPct >= 0 {
		b.WriteString(fmt.Sprintf("  Context: %d%%\n", preview.ContextPct))
	}
	if preview.CostUSD > 0 {
		b.WriteString(fmt.Sprintf("  Cost: $%.2f\n", preview.CostUSD))
	}
	for _, attention := range preview.Attention {
		b.WriteString("  " + attention + "\n")
	}
	for _, line := range preview.TranscriptTail {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

func (m APIAppModel) renderAPITranscript() string {
	transcript := m.snapshot.Transcript
	if transcript == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Attach Transcript\n")
	if transcript.SessionID != "" {
		b.WriteString("  Session: " + transcript.SessionID + "\n")
	}
	if transcript.Total > 0 {
		b.WriteString(fmt.Sprintf("  Messages: %d-%d of %d\n", transcript.Start, transcript.End, transcript.Total))
	}
	for _, line := range transcript.Lines {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

func (m APIAppModel) renderAPIContent() string {
	content := m.snapshot.Content
	if content == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Run Content\n")
	if content.RunNumber > 0 {
		b.WriteString(fmt.Sprintf("  Run: %d\n", content.RunNumber))
	}
	if content.Log != nil {
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("  Log %s", content.Log.ID))
		if content.Log.Size > 0 {
			b.WriteString(fmt.Sprintf("  bytes %d-%d of %d", content.Log.Offset, min(content.Log.Offset+content.Log.Limit, content.Log.Size), content.Log.Size))
		}
		if content.Log.Truncated {
			b.WriteString("  truncated")
		}
		b.WriteByte('\n')
		appendIndentedText(&b, content.Log.Text)
	}
	if content.Artifact != nil {
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("  Artifact %s", content.Artifact.ID))
		if content.Artifact.Phase != "" {
			b.WriteString("  " + content.Artifact.Phase)
		}
		if content.Artifact.Size > 0 {
			b.WriteString(fmt.Sprintf("  bytes %d-%d of %d", content.Artifact.Offset, min(content.Artifact.Offset+content.Artifact.Limit, content.Artifact.Size), content.Artifact.Size))
		}
		if content.Artifact.Truncated {
			b.WriteString("  truncated")
		}
		b.WriteByte('\n')
		appendIndentedText(&b, content.Artifact.Text)
	}
	b.WriteByte('\n')
	b.WriteString(KeyHelpStyle.Render(" [l] Next log   [[] Prev artifact   []] Next artifact   [esc] Close"))
	return b.String()
}

func (m APIAppModel) renderAPITextPanel() string {
	title := strings.TrimSpace(m.textPanelTitle)
	if title == "" {
		title = "Content"
	}
	content := strings.TrimRight(m.textPanelContent, "\n")
	if content == "" {
		content = MutedStyle.Render("No content")
	}
	return TitleStyle.Render(title) + "\n\n" + content + "\n\n" + KeyHelpStyle.Render(" [esc] Close")
}

func appendIndentedText(b *strings.Builder, text string) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b.WriteString("    " + line + "\n")
	}
}

func (m APIAppModel) renderFeatureActionConfirm() string {
	title := "Confirm " + apiMutationKindLabel(m.actionConfirmKind)
	if m.actionConfirmKind == "feature.rewind" {
		return m.renderAPIRewindConfirm()
	}
	name := m.actionConfirmFeatureName
	if name == "" {
		name = m.actionConfirmFeatureID
	}
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n\n")
	b.WriteString("  " + name + "\n\n")
	if len(m.actionConfirmArgs.Repos) > 0 {
		b.WriteString("  Repos: " + strings.Join(m.actionConfirmArgs.Repos, ", ") + "\n\n")
	} else if m.actionConfirmArgs.Repo != "" {
		b.WriteString("  Repo: " + m.actionConfirmArgs.Repo + "\n\n")
	}
	if m.actionConfirmArgs.TargetPhase != "" {
		b.WriteString("  Target phase: " + apiRewindPhaseLabel(m.actionConfirmArgs.TargetPhase) + "\n\n")
	}
	switch m.actionConfirmKind {
	case "feature.publish":
		b.WriteString(WarningStyle.Render("  This will publish the selected feature."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Review the server result before continuing."))
	case "feature.merge":
		b.WriteString(WarningStyle.Render("  This will merge the selected feature to the base branch."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Review the server result before continuing."))
	case "feature.restart":
		b.WriteString(WarningStyle.Render("  This will restart the selected feature phase."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Any active work for that phase will be replaced."))
	case "feature.retry":
		b.WriteString(WarningStyle.Render("  This will retry the failed feature phase."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Existing failure state will be replaced by the retry."))
	case "feature.mark-done":
		b.WriteString(WarningStyle.Render("  This will mark the selected feature as done."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Review the feature status before continuing."))
	case "feature.cleanup":
		b.WriteString(WarningStyle.Render("  This will clean the selected feature worktrees."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Feature state and repo-cycle history will be preserved."))
	case "feature.rebase":
		b.WriteString(WarningStyle.Render("  This will start a rebase cycle for the selected feature."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Conflict handling and push results will be reported by the server."))
	case "feature.tweak.start":
		b.WriteString(WarningStyle.Render("  This will start an interactive tweak session for the selected feature."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Finish and review decisions will be handled by the server."))
	case "feature.stop":
		b.WriteString(WarningStyle.Render("  This will interrupt the current phase."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  You can restart it later."))
	case "feature.delete":
		b.WriteString(WarningStyle.Render("  This will remove all artifacts and worktrees."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  This cannot be undone."))
	default:
		b.WriteString(WarningStyle.Render("  This will send the selected API request."))
	}
	b.WriteString("\n\n")
	b.WriteString(KeyHelpStyle.Render(" [y] Confirm   [any key] Cancel"))
	return panelStyle(true).
		Width(58).
		BorderForeground(colorWarning).
		Render(b.String())
}

func (m APIAppModel) renderAPIRewindConfirm() string {
	args := m.actionConfirmArgs
	target := args.TargetPhase
	if target == "" {
		target = m.selectedFeatureCurrentPhase(m.actionConfirmFeatureID)
	}
	phaseName := apiRewindPhaseLabel(target)
	if args.UpgradePipeline != "" {
		phaseName = "KB Build"
	}
	var c strings.Builder
	c.WriteString("\n")
	if args.UpgradePipeline != "" {
		fmt.Fprintf(&c, "  \u26a0 Upgrade to %s\n\n", args.UpgradePipeline)
		c.WriteString(WarningStyle.Render("  Pipeline will be upgraded and feature will restart from KB Build."))
		c.WriteString("\n")
		c.WriteString(WarningStyle.Render("  All progress will be lost."))
		c.WriteString("\n")
	} else if args.RoadmapPhase > 0 {
		_, total := m.selectedRoadmapProgress(m.actionConfirmFeatureID)
		if total < args.RoadmapPhase {
			total = args.RoadmapPhase
		}
		fmt.Fprintf(&c, "  \u26a0 Rewind Implement to roadmap Phase %d\n\n", args.RoadmapPhase)
		c.WriteString(WarningStyle.Render("  Keep: " + roadmapPhaseRangeLabel(1, args.RoadmapPhase-1)))
		c.WriteString("\n")
		c.WriteString(WarningStyle.Render(fmt.Sprintf("  Redo: Phase %d", args.RoadmapPhase)))
		c.WriteString("\n")
		c.WriteString(WarningStyle.Render("  Discard: " + roadmapPhaseRangeLabel(args.RoadmapPhase+1, total)))
		c.WriteString("\n")
		c.WriteString(WarningStyle.Render("  Reset boundary: " + roadmapResetBoundaryLabel(args.RoadmapPhase)))
		c.WriteString("\n")
	} else {
		fmt.Fprintf(&c, "  \u26a0 Rewind to %s\n\n", phaseName)
		c.WriteString(WarningStyle.Render("  All progress from this phase onwards will be lost:"))
		c.WriteString("\n")
		for _, phase := range feature.PhasesFromOnwards(apiFeaturePhase(target)) {
			c.WriteString(WarningStyle.Render("  - " + phase.String()))
			c.WriteString("\n")
		}
	}
	for _, line := range m.apiRewindPRWarningLines(m.actionConfirmFeatureID) {
		c.WriteString(WarningStyle.Render("  " + line))
		c.WriteString("\n")
	}
	contentBox := panelStyle(true).
		Width(60).
		BorderForeground(colorWarning).
		Render(c.String())
	contentBox = renderBorderTitle(contentBox, "Rewind Confirmation", WarningStyle)
	return contentBox + "\n" + KeyHelpStyle.Render(" [y] Confirm   [any key] Cancel")
}

func (m APIAppModel) apiRewindPRWarningLines(featureID string) []string {
	if featureID == "" {
		return nil
	}
	prs := map[string]string{}
	if detail, ok := m.featureDetails[featureID]; ok {
		for _, repo := range detail.Feature.RepoStatus {
			if repo.Name != "" && repo.PRURL != "" {
				prs[repo.Name] = repo.PRURL
			}
		}
	}
	if len(prs) == 0 {
		return nil
	}
	names := make([]string, 0, len(prs))
	for name := range prs {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := []string{"PRs that will be closed:"}
	for _, name := range names {
		lines = append(lines, fmt.Sprintf("- %s: %s", name, prs[name]))
	}
	return lines
}

func (m APIAppModel) renderAPIResumeAllConfirm() string {
	var b strings.Builder
	b.WriteString("\n")
	if len(m.resumeAllFeatureIDs) > 0 {
		b.WriteString(fmt.Sprintf("  %d interrupted/failed feature(s) will be resumed.\n", len(m.resumeAllFeatureIDs)))
	} else {
		b.WriteString(MutedStyle.Render("  No interrupted or failed features to resume."))
		b.WriteString("\n")
	}
	panelWidth := 56
	contentBox := panelStyle(true).
		Width(panelWidth).
		BorderForeground(colorInfo).
		Render(b.String())
	contentBox = renderBorderTitle(contentBox, "Resume All", lipgloss.NewStyle().Foreground(colorInfo))
	return contentBox + "\n" + KeyHelpStyle.Render(" [y] Confirm   [any key] Cancel")
}

func (m APIAppModel) renderTweakReviewModal() string {
	name := m.tweakReviewFeatureName
	if name == "" {
		name = m.tweakReviewFeatureID
	}
	var b strings.Builder
	b.WriteString(TitleStyle.Render("Final Review"))
	b.WriteString("\n\n")
	if name != "" {
		b.WriteString("  " + name + "\n\n")
	}
	b.WriteString("Changes have been committed. Run a Final Review?")
	b.WriteString("\n\n")
	b.WriteString("  [y] Yes - review and fix issues")
	b.WriteByte('\n')
	b.WriteString("  [n] No  - skip review and complete")
	b.WriteByte('\n')
	b.WriteByte('\n')
	b.WriteString(KeyHelpStyle.Render("y to review | n to skip | Esc to cancel"))
	return panelStyle(true).
		Width(58).
		BorderForeground(colorBrand).
		Render(b.String())
}

func (m APIAppModel) renderAPIReviewCommentsPanel(width int) string {
	panel := m.reviewComments
	if panel == nil {
		return ""
	}
	if width < 48 {
		width = 48
	}
	name := panel.featureName
	if name == "" {
		name = panel.featureID
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Review Comments: %s (%d)\n\n", name, len(panel.comments)))
	b.WriteString("  Repo: " + panel.repo + "\n")
	b.WriteString("\n\n")
	if len(panel.comments) == 0 {
		b.WriteString("  No pending review comments on this PR.\n")
	} else {
		for i, comment := range panel.comments {
			b.WriteString(fmt.Sprintf("  Comment %d/%d", i+1, len(panel.comments)))
			b.WriteByte('\n')
			location := apiReviewCommentLocation(comment)
			if location != "" {
				if comment.Path != "" {
					b.WriteString("  Location: " + location + "\n")
				} else {
					b.WriteString("  File: " + location + "\n")
				}
			}
			if comment.UserLogin != "" {
				b.WriteString("  Author: @" + comment.UserLogin + "\n")
			}
			for _, line := range strings.Split(comment.Body, "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				b.WriteString("  " + line + "\n")
			}
			if strings.TrimSpace(comment.DiffHunk) != "" {
				b.WriteString("  Context:\n")
				for _, line := range strings.Split(strings.TrimSpace(comment.DiffHunk), "\n") {
					b.WriteString("    " + line + "\n")
				}
			}
			if i < len(panel.comments)-1 {
				b.WriteByte('\n')
			}
		}
	}
	b.WriteByte('\n')
	b.WriteString(KeyHelpStyle.Render(" [Shift+A] Auto-address   [esc] Back   [q] Quit   [\u2191/\u2193] Scroll"))
	return panelStyle(true).
		Width(width).
		BorderForeground(colorBrand).
		Render(b.String())
}

func (m APIAppModel) renderAPIRepoActionPanel(width int) string {
	panel := m.repoActionPanel
	if panel == nil {
		return ""
	}
	if width < 52 {
		width = 52
	}
	name := panel.featureName
	if name == "" {
		name = panel.featureID
	}
	var b strings.Builder
	b.WriteString("Select repo \u2014 " + apiMutationKindLabel(panel.kind) + "\n\n")
	if name != "" {
		b.WriteString("  Feature: " + name + "\n\n")
	}
	if len(panel.repos) == 0 {
		b.WriteString("  No repos available.\n")
	} else {
		for i, repo := range panel.repos {
			cursor := "  "
			if i == panel.cursor {
				cursor = "> "
			}
			mark := ""
			if panel.multi {
				mark = "[ ] "
				if panel.selected[repo.Name] {
					mark = "[x] "
				}
			}
			line := cursor + mark + repo.Name
			status := apiRepoActionStatusForKind(panel.kind, repo)
			if status != "" {
				line += "  " + status
			}
			if i == panel.cursor {
				line = SelectedRowStyle.Render(line)
			}
			b.WriteString("  " + line + "\n")
		}
	}
	b.WriteByte('\n')
	footer := " [\u2191/\u2193] Select   [enter] Confirm   [esc] Cancel"
	if panel.multi {
		footer = " [space] Toggle   [enter] Confirm   [esc] Cancel"
	}
	b.WriteString(KeyHelpStyle.Render(footer))
	return panelStyle(true).
		Width(width).
		BorderForeground(colorBrand).
		Render(b.String())
}

func apiRepoActionStatus(repo apiRepoActionOption) string {
	return apiRepoActionStatusForKind("", repo)
}

func apiRepoActionStatusForKind(kind string, repo apiRepoActionOption) string {
	parts := make([]string, 0, 3)
	if repo.Publishable && kind == "feature.publish" {
		parts = append(parts, "publishable")
	}
	if repo.State != "" {
		parts = append(parts, repo.State)
	} else if kind != "feature.publish" {
		parts = append(parts, "idle")
	}
	if repo.PRURL != "" && !strings.Contains(repo.State, "PR #") {
		parts = append(parts, apiPRNumberLabel(repo.PRURL))
	}
	return strings.Join(nonEmptyStrings(parts), " ")
}

func (m APIAppModel) renderAPIRewindPanel(width int) string {
	panel := m.rewindPanel
	if panel == nil {
		return ""
	}
	if width < 52 {
		width = 52
	}
	name := panel.featureName
	if name == "" {
		name = panel.featureID
	}
	var b strings.Builder
	b.WriteString(TitleStyle.Render("Rewind to Phase"))
	b.WriteString("\n\n")
	if name != "" {
		b.WriteString("  Feature: " + name + "\n\n")
	}
	printedUpgradeTitle := false
	for i, choice := range panel.choices {
		if choice.UpgradePipeline != "" && !printedUpgradeTitle {
			b.WriteByte('\n')
			b.WriteString(TitleStyle.Render("Pipeline Upgrade"))
			b.WriteByte('\n')
			printedUpgradeTitle = true
		}
		cursor := "  "
		line := choice.Label
		if line == "" {
			line = apiRewindChoiceLabel(choice.TargetPhase)
		}
		if i == panel.cursor {
			cursor = "> "
			line = SelectedRowStyle.Render(line)
		}
		b.WriteString("  " + cursor + line + "\n")
	}
	b.WriteByte('\n')
	b.WriteString(KeyHelpStyle.Render("Enter to select \u00b7 Esc to cancel"))
	return panelStyle(true).
		Width(width).
		BorderForeground(colorBrand).
		Render(b.String())
}

func (m APIAppModel) renderAPIRoadmapRewindPanel(width int) string {
	panel := m.rewindPhasePicker
	if panel == nil {
		return ""
	}
	if width < 60 {
		width = 60
	}
	var b strings.Builder
	b.WriteString(TitleStyle.Render("Choose Roadmap Phase"))
	b.WriteString("\n\n")
	if panel.featureName != "" {
		b.WriteString("  Feature: " + panel.featureName + "\n\n")
	}
	for i, row := range panel.rows {
		cursor := "  "
		style := lipgloss.NewStyle()
		if i == panel.cursor {
			cursor = "> "
			style = SelectedRowStyle
		}
		header := fmt.Sprintf("%sPhase %d/%d: %s  [%s]  %s", cursor, row.Number, row.Total, row.Title, row.PhaseType, row.Status)
		if row.CurrentPhase {
			header += "  (current phase)"
		}
		b.WriteString(style.Render(header))
		b.WriteByte('\n')
		b.WriteString(MutedStyle.Render("    " + row.Effect))
		b.WriteByte('\n')
		b.WriteString(MutedStyle.Render("    Reset boundary: " + row.ResetBoundary))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(KeyHelpStyle.Render("Enter to continue \u00b7 Esc to return"))
	return panelStyle(true).
		Width(width).
		BorderForeground(colorBrand).
		Render(b.String())
}

func apiRewindChoiceLabel(phase string) string {
	if strings.EqualFold(strings.TrimSpace(phase), "inquire") || strings.EqualFold(strings.TrimSpace(phase), "inquiry") {
		return "Rewind to Start (Inquiry)"
	}
	label := apiRewindPhaseLabel(phase)
	if label == "" {
		label = strings.TrimSpace(phase)
	}
	return "Rewind to " + label
}

func apiRewindPhaseLabel(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "knowledgebase", "knowledge_base", "knowledge-base", "kb":
		return "KB Build"
	case "inquire", "inquiry":
		return "Start (Inquiry)"
	case "research":
		return "Research"
	case "design":
		return "Design"
	case "plan":
		return "Plan"
	case "implement":
		return "Implement"
	case "review":
		return "Review"
	case "finalreview", "final_review", "final-review":
		return "Final Review"
	case "publish":
		return "Publish"
	default:
		return strings.TrimSpace(phase)
	}
}

func (m APIAppModel) renderAPIRefactorPrompt(width int) string {
	prompt := m.refactorPrompt
	if prompt == nil {
		return ""
	}
	if width < 48 {
		width = 48
	}
	name := prompt.featureName
	if name == "" {
		name = prompt.featureID
	}
	title := "Refactor"
	if prompt.restart {
		title = "Restart refactor"
	}
	var b strings.Builder
	b.WriteString(title + "\n\n")
	b.WriteString("  Feature: " + name + "\n")
	if prompt.repo != "" {
		b.WriteString("  Repo: " + prompt.repo + "\n")
	}
	b.WriteString("\n")
	draft := prompt.draft
	if draft == "" {
		draft = " "
	}
	b.WriteString(panelStyle(false).Width(width - 4).Render(draft))
	b.WriteString("\n\n")
	b.WriteString(KeyHelpStyle.Render(" [ctrl+s] Submit   [esc] Cancel"))
	return panelStyle(true).
		Width(width).
		BorderForeground(colorBrand).
		Render(b.String())
}

func (m APIAppModel) renderAPIRefactorPipeline(width int) string {
	if width < 48 {
		width = 48
	}
	return panelStyle(true).
		Width(width).
		BorderForeground(colorBrand).
		Render(m.renderAPIRefactorPipelineSelector())
}

func (m APIAppModel) renderAPIRefactorPipelineSelector() string {
	panel := m.refactorPipeline
	if panel == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(TitleStyle.Render("Select Pipeline for Refactor"))
	b.WriteString("\n\n")
	parts := make([]string, 0, len(panel.pipelines))
	for i, option := range panel.pipelines {
		if i == panel.cursor {
			parts = append(parts, SelectedRowStyle.Render("\u25b8 "+string(option)))
		} else {
			parts = append(parts, "  "+string(option))
		}
	}
	b.WriteString(strings.Join(parts, "    "))
	b.WriteString("\n\n")
	selected := feature.PipelineLarge
	if len(panel.pipelines) > 0 && panel.cursor >= 0 && panel.cursor < len(panel.pipelines) {
		selected = panel.pipelines[panel.cursor]
	}
	switch selected {
	case feature.PipelineMedium:
		b.WriteString(MutedStyle.Render("Skip research - go straight to planning"))
	case feature.PipelineLarge:
		b.WriteString(MutedStyle.Render("Inquiry + research + planning"))
	case feature.PipelineMoonshot:
		b.WriteString(MutedStyle.Render("Full pipeline with all gates enabled"))
	}
	b.WriteString("\n\n")
	b.WriteString(KeyHelpStyle.Render(" [\u2190/\u2192] Navigate   [enter] Confirm   [esc] Cancel"))
	return b.String()
}

func (m APIAppModel) renderNeedInputPrompt() string {
	name := m.needInputFeatureName
	if name == "" {
		name = m.needInputFeatureID
	}
	var b strings.Builder
	b.WriteString("Need user input\n\n")
	b.WriteString("  " + name + "\n\n")
	if m.needInputGate.Scope != "" {
		b.WriteString("  Scope: " + m.needInputGate.Scope + "\n")
	}
	if m.needInputGate.Iteration > 0 {
		b.WriteString(fmt.Sprintf("  Iteration: %d\n", m.needInputGate.Iteration))
	}
	if m.needInputGate.Scope != "" || m.needInputGate.Iteration > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(WarningStyle.Render("  Resume continues from the saved answers."))
	b.WriteString("\n")
	b.WriteString(WarningStyle.Render("  Abort marks the paused gate as failed."))
	b.WriteString("\n\n")
	b.WriteString(KeyHelpStyle.Render(" [r] Resume   [a] Abort   [esc] Cancel"))
	return panelStyle(true).
		Width(58).
		BorderForeground(colorWarning).
		Render(b.String())
}

func (m APIAppModel) renderPermissionPrompt() string {
	name := m.permissionFeatureName
	if name == "" {
		name = m.permissionFeatureID
	}
	toolName := strings.TrimSpace(m.permissionRequest.ToolName)
	if toolName == "" {
		toolName = "tool"
	}
	var b strings.Builder
	b.WriteString("Permission request\n\n")
	b.WriteString("  " + name + "\n")
	if m.permissionRequest.RequestID != "" {
		b.WriteString("  Request: " + m.permissionRequest.RequestID + "\n")
	}
	if m.permissionRequest.SessionID != "" {
		b.WriteString("  Session: " + m.permissionRequest.SessionID + "\n")
	}
	if m.permissionRequest.Phase != "" {
		b.WriteString("  Phase: " + m.permissionRequest.Phase + "\n")
	}
	b.WriteString("  " + toolName)
	if m.permissionRequest.Summary != "" {
		b.WriteString(": " + firstLine(m.permissionRequest.Summary))
	}
	b.WriteString("\n\n")
	b.WriteString(WarningStyle.Render("  Allow resumes the waiting session."))
	b.WriteString("\n")
	b.WriteString(WarningStyle.Render("  Deny sends a rejection to the session."))
	b.WriteString("\n\n")
	b.WriteString(KeyHelpStyle.Render(" [a] Allow   [d] Deny   [esc] Cancel"))
	return panelStyle(true).
		Width(58).
		BorderForeground(colorWarning).
		Render(b.String())
}

func (m APIAppModel) renderHelpPrompt() string {
	name := m.helpFeatureName
	if name == "" {
		name = m.helpFeatureID
	}
	var b strings.Builder
	b.WriteString("Help request\n\n")
	b.WriteString("  " + name + "\n")
	if question := strings.TrimSpace(m.helpQuestion); question != "" {
		b.WriteString("  " + firstLine(question) + "\n")
	}
	b.WriteString("\n")
	b.WriteString("  Answer: " + m.helpAnswerDraft + "\n")
	b.WriteString("\n")
	b.WriteString(KeyHelpStyle.Render(" [enter] Send   [esc] Cancel"))
	return panelStyle(true).
		Width(58).
		BorderForeground(colorWarning).
		Render(b.String())
}

func (m APIAppModel) renderAskUserPrompt() string {
	name := m.askUserFeatureName
	if name == "" {
		name = m.askUserFeatureID
	}
	var b strings.Builder
	b.WriteString("AskUser question\n\n")
	b.WriteString("  " + name + "\n")
	if m.askUserRequest.RequestID != "" {
		b.WriteString("  Request: " + m.askUserRequest.RequestID + "\n")
	}
	if m.askUserRequest.SessionID != "" {
		b.WriteString("  Session: " + m.askUserRequest.SessionID + "\n")
	}
	if question := strings.TrimSpace(m.askUserQuestion); question != "" {
		b.WriteString("  " + firstLine(question) + "\n")
	}
	if q, ok := apiAskUserFirstQuestion(m.askUserRequest); ok && len(q.Options) > 0 {
		b.WriteByte('\n')
		for i, option := range q.Options {
			cursor := "  "
			line := fmt.Sprintf("%d. %s", i+1, option.Label)
			if option.Description != "" {
				line += " - " + option.Description
			}
			if i == m.askUserOptionCursor {
				cursor = "> "
				line = SelectedRowStyle.Render(line)
			}
			b.WriteString("  " + cursor + line + "\n")
		}
	}
	b.WriteString("\n")
	b.WriteString("  Answer: " + m.askUserAnswerDraft + "\n")
	b.WriteString("\n")
	footer := " [enter] Send   [esc] Cancel"
	if q, ok := apiAskUserFirstQuestion(m.askUserRequest); ok && len(q.Options) > 0 {
		footer = " [up/down] Option   [enter] Send   [esc] Cancel"
	}
	b.WriteString(KeyHelpStyle.Render(footer))
	return panelStyle(true).
		Width(58).
		BorderForeground(colorWarning).
		Render(b.String())
}

func (m APIAppModel) listenForAPIEvents() tea.Cmd {
	return func() tea.Msg {
		if m.signals == nil && m.eventErrs == nil {
			return nil
		}
		select {
		case signal, ok := <-m.signals:
			if !ok {
				return nil
			}
			return apiRefreshSignalMsg{signal: signal}
		case err, ok := <-m.eventErrs:
			if !ok || err == nil {
				return nil
			}
			return apiEventErrorMsg{err: err}
		case <-m.eventCtx.Done():
			return nil
		}
	}
}

func (m APIAppModel) fetchRefreshSnapshotCmd(signal server.RefreshSignal) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		snapshot, err := m.client.FetchRefreshSnapshot(ctx, signal)
		if err != nil {
			return apiRefreshSnapshotMsg{snapshot: snapshot, err: err}
		}
		var content *apiFeatureContentSnapshot
		if m.shouldRefreshSelectedContent(signal) {
			featureID := m.selectedFeature
			detail, ok := m.featureDetails[featureID]
			if snapshot.Feature != nil && snapshot.Feature.Feature.ID == featureID {
				detail = *snapshot.Feature
				ok = true
			}
			if ok {
				var previous *apiFeatureContentSnapshot
				if existing, ok := m.contents[featureID]; ok {
					previous = &existing
				}
				content = loadAPISelectedContent(ctx, m.client, featureID, detail, previous)
			}
		}
		return apiRefreshSnapshotMsg{snapshot: snapshot, content: content}
	}
}

func (m APIAppModel) fetchFeatureDetailCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		detail, err := m.client.FeatureDetail(ctx, featureID)
		if err != nil {
			return apiFeatureDetailMsg{featureID: featureID, err: err}
		}
		preview, err := m.client.LivePreview(ctx, featureID)
		if err != nil {
			return apiFeatureDetailMsg{featureID: featureID, err: err}
		}
		msg := apiFeatureDetailMsg{featureID: featureID, detail: detail, livePreview: &preview}
		if sessionID := apiSelectedSessionID(preview); sessionID != "" {
			session, transcript, err := loadAPITranscriptTail(ctx, m.client, sessionID)
			if err != nil {
				return apiFeatureDetailMsg{featureID: featureID, err: err}
			}
			msg.session = &session
			msg.transcript = &transcript
		}
		var previous *apiFeatureContentSnapshot
		if existing, ok := m.contents[featureID]; ok {
			previous = &existing
		}
		msg.content = loadAPISelectedContent(ctx, m.client, featureID, detail, previous)
		return msg
	}
}

func (m APIAppModel) fetchArtifactContentCmd(content apiFeatureContentSnapshot, artifact server.ArtifactDTO) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		query := server.TextQuery{Offset: apiContentTailOffset(artifact.Size), Limit: apiContentTailLimit}
		resp, err := m.client.ArtifactContent(ctx, content.FeatureID, content.RunNumber, artifact.ID, query)
		if err != nil {
			return apiContentSelectionMsg{
				featureID: content.FeatureID,
				err:       fmt.Errorf("load artifact %s: %w", artifact.ID, err),
			}
		}
		next := content
		artifactCopy := artifact
		next.ArtifactID = artifact.ID
		next.ArtifactMeta = &artifactCopy
		next.Artifact = &resp
		next.ContentLoaded = next.Log != nil || next.Artifact != nil
		return apiContentSelectionMsg{featureID: content.FeatureID, content: next}
	}
}

func (m APIAppModel) fetchNextLogContentCmd(content apiFeatureContentSnapshot) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		logIDs := cycleLogIDs(content.LogID)
		var lastErr error
		for _, logID := range logIDs {
			query := server.TextQuery{Limit: apiContentTailLimit}
			if logID == content.LogID && content.Log != nil {
				query.Offset = apiContentTailOffset(content.Log.Size)
			}
			resp, err := m.client.LogContent(ctx, content.FeatureID, content.RunNumber, logID, query)
			if err != nil {
				lastErr = err
				continue
			}
			next := content
			next.LogID = logID
			next.Log = &resp
			next.ContentLoaded = true
			return apiContentSelectionMsg{featureID: content.FeatureID, content: next}
		}
		if lastErr == nil {
			lastErr = errors.New("no selectable logs")
		}
		return apiContentSelectionMsg{
			featureID: content.FeatureID,
			err:       fmt.Errorf("load next run log: %w", lastErr),
		}
	}
}

func (m APIAppModel) shouldRefreshSelectedContent(signal server.RefreshSignal) bool {
	if m.selectedFeature == "" {
		return false
	}
	resource := signal.Resource
	if resource.Type == "" {
		resource = signal.Event.Resource
	}
	if resource.FeatureID != "" && resource.FeatureID != m.selectedFeature {
		return false
	}
	switch {
	case signal.Event.Kind == "log.updated":
		return true
	case resource.Type == "log" || resource.Type == "artifact":
		return true
	default:
		return false
	}
}

func (m APIAppModel) fetchFeatureConfigCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		cfg, err := m.client.FeatureConfig(ctx, featureID)
		return apiFeatureConfigMsg{featureID: featureID, config: cfg, err: err}
	}
}

func (m APIAppModel) fetchReviewCommentsCmd(featureID, featureName, repo, mode string, modes []string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		resp, err := m.client.FetchReviewComments(ctx, featureID, server.ReviewCommentsFetchRequest{Repo: repo})
		return apiReviewCommentsFetchedMsg{
			featureID:   featureID,
			featureName: featureName,
			repo:        repo,
			mode:        mode,
			modes:       modes,
			response:    resp,
			err:         err,
		}
	}
}

func (m APIAppModel) selectedPrimaryFeatureActionKind() string {
	if m.selectedActionReady("feature.resume") {
		return "feature.resume"
	}
	return "feature.start"
}

func (m APIAppModel) createFeatureCmd(result *WizardResult) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		projection := result.Pipeline.ProjectGates(result.Checkpoints, true)
		req := server.CreateFeatureRequest{
			Name:                    strings.TrimSpace(result.Name),
			Description:             strings.TrimSpace(result.Description),
			Repos:                   append([]string(nil), result.Repos...),
			Models:                  result.Models,
			ExitCriteria:            result.ExitCriteria,
			Inquireness:             result.Inquireness,
			Images:                  append([]string(nil), result.Images...),
			UseCurrentBranch:        result.UseCurrentBranch,
			UseCurrentBranchPerRepo: copyBoolMap(result.UseCurrentBranchPerRepo),
			Checkpoints:             projection.Checkpoints,
			Attachments:             append([]string(nil), result.Attachments...),
			RiskLevel:               feature.RiskLevel(result.RiskLevel),
			Pipeline:                result.Pipeline,
		}
		created, err := m.client.CreateFeature(ctx, req)
		if err != nil {
			return apiMutationResultMsg{
				kind: "feature.create",
				err:  err,
			}
		}
		if created.FeatureID == "" {
			return apiMutationResultMsg{
				kind: "feature.create",
				err:  errors.New("create feature response missing feature_id"),
			}
		}
		_, err = m.client.StartFeature(ctx, created.FeatureID)
		return apiMutationResultMsg{
			kind:      "feature.create",
			featureID: created.FeatureID,
			err:       err,
		}
	}
}

func (m APIAppModel) handleAPIWizardMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.wizard == nil {
		return m, nil
	}
	if m.wizardRuntimeConfigPending {
		if keyMsg, ok := msg.(tea.KeyPressMsg); ok && keyMsg.Code == tea.KeyEscape {
			m.clearCreatePrompt()
			m.wizardRuntimeConfigPending = false
			m.statusMessage = ""
			return m, nil
		}
		m.statusMessage = "Updating workspace configuration..."
		return m, nil
	}
	wizard, cmd := m.wizard.Update(msg)
	m.wizard = &wizard
	var cmds []tea.Cmd
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	if sideEffectCmd := m.consumeAPIWizardRuntimeConfigSideEffect(); sideEffectCmd != nil {
		cmds = append(cmds, sideEffectCmd)
	}

	if m.wizard.IsCancelled() {
		m.clearCreatePrompt()
		m.statusMessage = ""
		return m, nil
	}
	if m.wizard.IsDone() {
		if m.wizardRuntimeConfigPending {
			m.statusMessage = "Updating workspace configuration..."
			return m, tea.Batch(cmds...)
		}
		result := m.wizard.Result()
		m.clearCreatePrompt()
		if result != nil {
			return m, m.createFeatureCmd(result)
		}
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

func (m *APIAppModel) consumeAPIWizardRuntimeConfigSideEffect() tea.Cmd {
	if m.wizard == nil {
		return nil
	}
	if root := m.wizard.ConsumeBrowseRoot(); root != "" {
		roots := append([]string(nil), m.runtimeConfig.WorkspaceRoots...)
		if containsRootExpanded(roots, root) {
			m.refreshAPIWizardRepos("")
			return nil
		}
		roots = append(roots, root)
		m.runtimeConfig.WorkspaceRoots = roots
		m.refreshAPIWizardRepos("")
		m.wizardRuntimeConfigPending = true
		m.statusMessage = "Updating workspace configuration..."
		return m.persistRuntimeWorkspaceRootsCmd(roots, "")
	}
	if createdPath := m.wizard.ConsumeCreateRepoPath(); createdPath != "" {
		parentDir := filepath.Dir(createdPath)
		roots := append([]string(nil), m.runtimeConfig.WorkspaceRoots...)
		if !containsRootExpanded(roots, parentDir) {
			roots = append(roots, parentDir)
		}
		m.runtimeConfig.WorkspaceRoots = roots
		m.wizardRuntimeConfigPending = true
		m.statusMessage = "Updating workspace configuration..."
		return m.persistRuntimeWorkspaceRootsCmd(roots, createdPath)
	}
	return nil
}

func (m APIAppModel) primarySelectedFeatureActionCmd(kind, featureID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		var err error
		switch kind {
		case "feature.resume":
			_, err = m.client.ResumeFeature(ctx, featureID)
		case "feature.start":
			_, err = m.client.StartFeature(ctx, featureID)
		default:
			err = fmt.Errorf("unsupported primary feature action %s", kind)
		}
		return apiMutationResultMsg{
			kind:      kind,
			featureID: featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) selectedFeatureActionCmd(kind, featureID string, argsOpt ...apiFeatureActionArgs) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		args := apiFeatureActionArgs{}
		if len(argsOpt) > 0 {
			args = argsOpt[0]
		}
		var err error
		switch kind {
		case "feature.publish":
			_, err = m.client.PublishFeature(ctx, featureID, server.PublishFeatureRequest{Repos: append([]string(nil), args.Repos...)})
		case "feature.merge":
			_, err = m.client.MergeFeature(ctx, featureID)
		case "feature.restart":
			_, err = m.client.RestartFeature(ctx, featureID, server.RestartFeatureRequest{})
		case "feature.retry":
			_, err = m.client.RetryFeature(ctx, featureID)
		case "feature.mark-done":
			_, err = m.client.MarkDone(ctx, featureID)
		case "feature.rebase":
			repo := args.Repo
			if repo == "" {
				repo = m.selectedRebaseRepo(featureID)
			}
			_, err = m.client.StartRebase(ctx, featureID, server.RebaseActionRequest{Repo: repo})
		case "feature.cleanup":
			_, err = m.client.CleanupFeature(ctx, featureID, server.CleanupActionRequest{Target: "worktrees", Repo: args.Repo})
		case "feature.rewind":
			targetPhase := args.TargetPhase
			if targetPhase == "" {
				targetPhase = m.selectedFeatureCurrentPhase(featureID)
			}
			if targetPhase == "" {
				err = errors.New("selected feature has no rewind target phase")
				break
			}
			_, err = m.client.RewindFeature(ctx, featureID, server.RewindFeatureRequest{
				TargetPhase:     targetPhase,
				RoadmapPhase:    args.RoadmapPhase,
				UpgradePipeline: args.UpgradePipeline,
			})
		case "feature.tweak.start":
			_, err = m.client.StartTweak(ctx, featureID, server.TweakActionRequest{})
		case "feature.stop":
			_, err = m.client.StopFeature(ctx, featureID)
		case "feature.delete":
			_, err = m.client.DeleteFeature(ctx, featureID)
		default:
			err = fmt.Errorf("unsupported feature action %s", kind)
		}
		return apiMutationResultMsg{
			kind:      kind,
			featureID: featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) finishTweakDecisionCmd(featureID, decision string, hadChanges bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		_, err := m.client.FinishTweak(ctx, featureID, server.TweakFinishRequest{
			Decision:   decision,
			HadChanges: hadChanges,
		})
		return apiMutationResultMsg{
			kind:      "feature.tweak.finish",
			featureID: featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) executeRecoveryCmd(panel apiRecoveryPanel) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.ExecuteRecovery(context.Background(), server.RecoveryActionRequest{
			SnapshotID: panel.snapshotID,
			Actions:    copyStringMapValues(panel.actions),
		})
		return apiMutationResultMsg{kind: "recovery.execute", err: err}
	}
}

func (m APIAppModel) handleAPIRepoActionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	panel := m.repoActionPanel
	if panel == nil {
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEscape:
		m.repoActionPanel = nil
		m.statusMessage = ""
		return m, nil
	case tea.KeyUp:
		if panel.cursor > 0 {
			panel.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if panel.cursor < len(panel.repos)-1 {
			panel.cursor++
		}
		return m, nil
	case tea.KeySpace:
		if panel.multi && panel.cursor >= 0 && panel.cursor < len(panel.repos) {
			name := panel.repos[panel.cursor].Name
			if panel.selected == nil {
				panel.selected = map[string]bool{}
			}
			panel.selected[name] = !panel.selected[name]
		}
		return m, nil
	case tea.KeyEnter:
		return m.acceptAPIRepoAction(*panel)
	}
	switch strings.ToLower(msg.Text) {
	case "j":
		if panel.cursor < len(panel.repos)-1 {
			panel.cursor++
		}
	case "k":
		if panel.cursor > 0 {
			panel.cursor--
		}
	case " ":
		if panel.multi && panel.cursor >= 0 && panel.cursor < len(panel.repos) {
			name := panel.repos[panel.cursor].Name
			if panel.selected == nil {
				panel.selected = map[string]bool{}
			}
			panel.selected[name] = !panel.selected[name]
		}
	}
	return m, nil
}

func (m APIAppModel) acceptAPIRepoAction(panel apiRepoActionPanel) (tea.Model, tea.Cmd) {
	if len(panel.repos) == 0 {
		m.repoActionPanel = nil
		m.statusMessage = "No repos available for " + panel.kind
		return m, nil
	}
	repo := panel.repos[min(max(panel.cursor, 0), len(panel.repos)-1)].Name
	m.repoActionPanel = nil
	switch panel.kind {
	case "feature.publish":
		repos := panel.selectedRepoNames()
		if len(repos) == 0 {
			m.repoActionPanel = &panel
			m.statusMessage = "Select at least one repo to publish"
			return m, nil
		}
		m.selectedFeature = panel.featureID
		return m.confirmSelectedFeatureActionWithArgs("feature.publish", apiFeatureActionArgs{Repos: repos}), nil
	case "feature.review_comments":
		m.selectedFeature = panel.featureID
		mode, modes := m.selectedReviewCommentsModeDefaults()
		m.statusMessage = "Fetching review comments..."
		return m, m.fetchReviewCommentsCmd(panel.featureID, panel.featureName, repo, mode, modes)
	case "feature.rebase", "feature.tweak.start":
		m.selectedFeature = panel.featureID
		return m.confirmSelectedFeatureActionWithArgs(panel.kind, apiFeatureActionArgs{Repo: repo}), nil
	case "feature.refactor.start":
		m.selectedFeature = panel.featureID
		return m.openRefactorPromptForRepo(panel.kind, repo, false), nil
	case "feature.refactor.restart":
		m.selectedFeature = panel.featureID
		return m.openRefactorPromptForRepo(panel.kind, repo, true), nil
	default:
		m.statusMessage = "Unsupported repo action " + panel.kind
		return m, nil
	}
}

func (m APIAppModel) handleAPIRewindKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	panel := m.rewindPanel
	if panel == nil {
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEscape:
		m.rewindPanel = nil
		m.statusMessage = ""
		return m, nil
	case tea.KeyUp:
		if panel.cursor > 0 {
			panel.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if panel.cursor < len(panel.choices)-1 {
			panel.cursor++
		}
		return m, nil
	case tea.KeyEnter:
		if len(panel.choices) == 0 {
			m.rewindPanel = nil
			m.statusMessage = "selected feature has no rewind target phase"
			return m, nil
		}
		choice := panel.choices[min(max(panel.cursor, 0), len(panel.choices)-1)]
		m.selectedFeature = panel.featureID
		m.rewindPanel = nil
		if choice.UpgradePipeline == "" && apiFeaturePhase(choice.TargetPhase) == feature.PhaseImplement {
			if next, ok := m.newAPIRoadmapRewindPanel(panel.featureID, panel.featureName); ok {
				m.rewindPhasePicker = next
				m.statusMessage = ""
				return m, nil
			}
		}
		return m.confirmSelectedFeatureActionWithArgs("feature.rewind", apiFeatureActionArgs{
			TargetPhase:     choice.TargetPhase,
			UpgradePipeline: choice.UpgradePipeline,
		}), nil
	}
	switch strings.ToLower(msg.Text) {
	case "j":
		if panel.cursor < len(panel.choices)-1 {
			panel.cursor++
		}
	case "k":
		if panel.cursor > 0 {
			panel.cursor--
		}
	}
	return m, nil
}

func (m APIAppModel) handleAPIRoadmapRewindKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	panel := m.rewindPhasePicker
	if panel == nil {
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEscape:
		m.rewindPhasePicker = nil
		m.statusMessage = ""
		return m, nil
	case tea.KeyUp:
		if panel.cursor > 0 {
			panel.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if panel.cursor < len(panel.rows)-1 {
			panel.cursor++
		}
		return m, nil
	case tea.KeyEnter:
		if len(panel.rows) == 0 {
			m.rewindPhasePicker = nil
			m.statusMessage = "selected feature has no roadmap phases"
			return m, nil
		}
		row := panel.rows[min(max(panel.cursor, 0), len(panel.rows)-1)]
		m.selectedFeature = panel.featureID
		m.rewindPhasePicker = nil
		return m.confirmSelectedFeatureActionWithArgs("feature.rewind", apiFeatureActionArgs{
			TargetPhase:  feature.PhaseImplement.DirName(),
			RoadmapPhase: row.Number,
		}), nil
	}
	switch strings.ToLower(msg.Text) {
	case "j":
		if panel.cursor < len(panel.rows)-1 {
			panel.cursor++
		}
	case "k":
		if panel.cursor > 0 {
			panel.cursor--
		}
	}
	return m, nil
}

func (p apiRepoActionPanel) selectedRepoNames() []string {
	if !p.multi {
		if p.cursor >= 0 && p.cursor < len(p.repos) {
			return []string{p.repos[p.cursor].Name}
		}
		return nil
	}
	out := make([]string, 0, len(p.repos))
	for _, repo := range p.repos {
		if p.selected[repo.Name] {
			out = append(out, repo.Name)
		}
	}
	return out
}

func (m APIAppModel) handleAPIReviewCommentsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	panel := m.reviewComments
	if panel == nil {
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEscape:
		m.reviewComments = nil
		m.statusMessage = ""
		return m, nil
	case 'A':
		if msg.Text == "A" && len(panel.comments) > 0 {
			m.statusMessage = "Auto-addressing review comments for " + panel.repo + "..."
			return m, m.startReviewCommentsCmd(*panel)
		}
		return m, nil
	}
	if strings.ToLower(msg.Text) == "q" {
		m.reviewComments = nil
		m.statusMessage = ""
		return m, nil
	}
	return m, nil
}

func (m APIAppModel) handleAPIRefactorPromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	prompt := m.refactorPrompt
	if prompt == nil {
		return m, nil
	}
	if msg.Code == 's' && msg.Mod.Contains(tea.ModCtrl) {
		value := strings.TrimSpace(prompt.input.Value())
		if value == "" {
			m.statusMessage = "Refactor prompt cannot be empty"
			return m, nil
		}
		m.refactorPipeline = &apiRefactorPipelinePanel{
			featureID:   prompt.featureID,
			featureName: prompt.featureName,
			repo:        prompt.repo,
			prompt:      value,
			pipelines:   append([]feature.PipelineProfile(nil), prompt.pipelines...),
			cursor:      apiDefaultRefactorPipelineCursor(prompt.pipelines),
			restart:     prompt.restart,
		}
		m.refactorPrompt = nil
		m.statusMessage = ""
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEscape:
		m.refactorPrompt = nil
		m.statusMessage = ""
		return m, nil
	}
	var cmd tea.Cmd
	prompt.input, cmd = prompt.input.Update(msg)
	prompt.draft = prompt.input.Value()
	m.statusMessage = ""
	return m, cmd
}

func (m APIAppModel) handleAPIRefactorPipelineKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	panel := m.refactorPipeline
	if panel == nil {
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEscape:
		m.refactorPipeline = nil
		m.statusMessage = ""
		return m, nil
	case tea.KeyLeft:
		if panel.cursor > 0 {
			panel.cursor--
		}
		return m, nil
	case tea.KeyRight:
		if panel.cursor < len(panel.pipelines)-1 {
			panel.cursor++
		}
		return m, nil
	case tea.KeyEnter:
		m.refactorPipeline = nil
		return m, m.startRefactorCmd(*panel)
	}
	switch strings.ToLower(msg.Text) {
	case "h":
		if panel.cursor > 0 {
			panel.cursor--
		}
	case "l":
		if panel.cursor < len(panel.pipelines)-1 {
			panel.cursor++
		}
	}
	return m, nil
}

func (m APIAppModel) handleAPIConfigEditorKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	editor := m.configEditor
	if editor == nil {
		return m, nil
	}
	if editor.discardConfirm {
		switch msg.String() {
		case "y", "Y":
			m.configEditor = nil
			return m, nil
		default:
			editor.discardConfirm = false
			return m, nil
		}
	}
	switch msg.String() {
	case "esc":
		if editor.editor.HasChanges() {
			editor.discardConfirm = true
			return m, nil
		}
		m.configEditor = nil
		return m, nil
	case "enter":
		if editor.saving {
			return m, nil
		}
		editor.saving = true
		editor.saveErr = ""
		return m, m.saveFeatureConfigCmd(*editor)
	default:
		updated, cmd := editor.Update(msg)
		m.configEditor = &updated
		return m, cmd
	}
}

func (m APIAppModel) handleAPIRuntimeConfigEditorKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	editor := m.runtimeConfigEditor
	if editor == nil {
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEscape:
		m.runtimeConfigEditor = nil
		return m, nil
	case tea.KeyUp:
		editor.move(-1)
		return m, nil
	case tea.KeyDown:
		editor.move(1)
		return m, nil
	case tea.KeyTab:
		if msg.Mod.Contains(tea.ModShift) {
			editor.cycleModel(-1)
		} else {
			editor.cycleModel(1)
		}
		return m, nil
	case tea.KeyEnter:
		return m, m.saveRuntimeConfigCmd(*editor)
	}
	switch strings.ToLower(msg.Text) {
	case "k":
		editor.move(-1)
	case "j":
		editor.move(1)
	}
	return m, nil
}

func (m APIAppModel) handleAPIHelpPromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEscape:
		m.clearHelpPrompt()
		return m, nil
	case tea.KeyEnter:
		answer := strings.TrimSpace(m.helpAnswerDraft)
		if answer == "" {
			m.statusMessage = "Help answer cannot be empty"
			return m, nil
		}
		return m, m.helpAnswerCmd(m.helpFeatureID, answer)
	case tea.KeyBackspace:
		runes := []rune(m.helpAnswerDraft)
		if len(runes) > 0 {
			m.helpAnswerDraft = string(runes[:len(runes)-1])
		}
		return m, nil
	}
	if msg.Text != "" && !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
		m.helpAnswerDraft += msg.Text
		m.statusMessage = ""
	}
	return m, nil
}

func (m APIAppModel) handleAPIAskUserPromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	question, hasQuestion := apiAskUserFirstQuestion(m.askUserRequest)
	hasOptions := hasQuestion && len(question.Options) > 0
	switch msg.Code {
	case tea.KeyEscape:
		m.clearAskUserPrompt()
		return m, nil
	case tea.KeyUp:
		if hasOptions && m.askUserOptionCursor > 0 {
			m.askUserOptionCursor--
		}
		return m, nil
	case tea.KeyDown:
		if hasOptions && m.askUserOptionCursor < len(question.Options)-1 {
			m.askUserOptionCursor++
		}
		return m, nil
	case tea.KeyEnter:
		answer := strings.TrimSpace(m.askUserAnswerDraft)
		if answer == "" && hasOptions && m.askUserOptionCursor >= 0 && m.askUserOptionCursor < len(question.Options) {
			answer = strings.TrimSpace(question.Options[m.askUserOptionCursor].Label)
		}
		if answer == "" {
			m.statusMessage = "AskUser answer cannot be empty"
			return m, nil
		}
		return m, m.askUserAnswerCmd(m.askUserRequest, m.askUserQuestion, answer)
	case tea.KeyBackspace:
		runes := []rune(m.askUserAnswerDraft)
		if len(runes) > 0 {
			m.askUserAnswerDraft = string(runes[:len(runes)-1])
		}
		return m, nil
	}
	if hasOptions {
		switch strings.ToLower(msg.Text) {
		case "j":
			if m.askUserOptionCursor < len(question.Options)-1 {
				m.askUserOptionCursor++
			}
			return m, nil
		case "k":
			if m.askUserOptionCursor > 0 {
				m.askUserOptionCursor--
			}
			return m, nil
		}
		if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
			idx := int(msg.Text[0] - '1')
			if idx < len(question.Options) {
				m.askUserOptionCursor = idx
				m.askUserAnswerDraft = question.Options[idx].Label
			}
			return m, nil
		}
	}
	if msg.Text != "" && !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
		m.askUserAnswerDraft += msg.Text
		m.statusMessage = ""
	}
	return m, nil
}

func (m APIAppModel) saveFeatureConfigCmd(editor EditConfigModel) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		snap := editor.editor.Snapshot()
		_, err := m.client.UpdateFeatureConfig(ctx, editor.featureID, server.FeatureConfigMutationRequest{
			Models:      snap.Models,
			Inquireness: string(snap.Inquireness),
			Checkpoints: snap.Checkpoints,
			Pipeline:    editor.pipeline,
		})
		return apiMutationResultMsg{
			kind:      "feature.config.update",
			featureID: editor.featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) saveRuntimeConfigCmd(editor apiRuntimeConfigEditor) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		_, err := m.client.UpdateRuntimeConfig(ctx, server.RuntimeConfigMutationRequest{
			Defaults: config.DefaultsConfig{Models: editor.draft},
		})
		return apiMutationResultMsg{
			kind: "runtime.config.update",
			err:  err,
		}
	}
}

func (m APIAppModel) startReviewCommentsCmd(panel apiReviewCommentsPanel) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		_, err := m.client.StartReviewComments(ctx, panel.featureID, server.ReviewCommentsActionRequest{
			Repo:     panel.repo,
			Mode:     panel.mode,
			Comments: append([]server.ReviewCommentDTO(nil), panel.comments...),
		})
		return apiMutationResultMsg{
			kind:      "feature.review_comments",
			featureID: panel.featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) startRefactorCmd(panel apiRefactorPipelinePanel) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		pipeline := feature.PipelineLarge
		if len(panel.pipelines) > 0 && panel.cursor >= 0 && panel.cursor < len(panel.pipelines) {
			pipeline = panel.pipelines[panel.cursor]
		}
		req := server.RefactorActionRequest{
			Repo:     panel.repo,
			Prompt:   panel.prompt,
			Pipeline: pipeline,
		}
		kind := "feature.refactor.start"
		var err error
		if panel.restart {
			kind = "feature.refactor.restart"
			_, err = m.client.RestartRefactor(ctx, panel.featureID, req)
		} else {
			_, err = m.client.StartRefactor(ctx, panel.featureID, req)
		}
		return apiMutationResultMsg{
			kind:      kind,
			featureID: panel.featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) needUserInputDecisionCmd(featureID, decision string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		_, err := m.client.NeedUserInputDecision(ctx, featureID, server.NeedUserInputDecisionRequest{Decision: decision})
		return apiMutationResultMsg{
			kind:      "feature.need_user_input.decision",
			featureID: featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) reviewDecisionCmd(featureID string, req server.ReviewDecisionRequest) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		_, err := m.client.ReviewDecision(ctx, featureID, req)
		return apiMutationResultMsg{
			kind:      "feature.review_decision",
			featureID: featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) permissionAnswerCmd(req server.ControlRequestDTO, decision string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		_, err := m.client.AnswerPermission(ctx, server.PermissionAnswerRequest{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Decision:  decision,
		})
		return apiMutationResultMsg{
			kind:      "permission.answer",
			featureID: req.FeatureID,
			err:       err,
		}
	}
}

func (m APIAppModel) helpAnswerCmd(featureID, answer string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		_, err := m.client.SendHelp(ctx, server.HelpAnswerRequest{
			FeatureID: featureID,
			Message:   answer,
		})
		return apiMutationResultMsg{
			kind:      "help.send",
			featureID: featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) askUserAnswerCmd(req server.ControlRequestDTO, question, answer string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		_, err := m.client.AnswerAskUser(ctx, server.AskUserAnswerRequest{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Answers:   map[string]string{question: answer},
		})
		return apiMutationResultMsg{
			kind:      "ask_user.answer",
			featureID: req.FeatureID,
			err:       err,
		}
	}
}

func (m APIAppModel) planReviewDecisionRequest(featureID, decision string) server.ReviewDecisionRequest {
	req := server.ReviewDecisionRequest{Decision: decision}
	if progress, ok := m.featureProgress(featureID); ok {
		if progress.CurrentRoadmapPhase > 0 {
			req.PhasePlan = true
			return req
		}
	}
	if decision == "proceed" {
		req.Phase = "implement"
	}
	return req
}

func roadmapReviewDecisionRequest(msg RoadmapReviewDecisionMsg) server.ReviewDecisionRequest {
	decision := "proceed"
	if msg.Decision == "reject" {
		decision = "iterate"
	}
	return server.ReviewDecisionRequest{
		Decision: decision,
		Roadmap:  true,
		Comment:  msg.Comment,
	}
}

func (m APIAppModel) featureProgress(featureID string) (server.FeatureProgress, bool) {
	if detail, ok := m.featureDetails[featureID]; ok && detail.Feature.ID != "" {
		return detail.Feature.Progress, true
	}
	for _, summary := range m.featureList.Features {
		if summary.ID == featureID {
			return summary.Progress, true
		}
	}
	return server.FeatureProgress{}, false
}

func (m APIAppModel) selectedNeedInputGate(featureID string) (server.NeedInputGateDTO, bool) {
	if featureID == "" {
		return server.NeedInputGateDTO{}, false
	}
	if detail, ok := m.featureDetails[featureID]; ok && detail.Feature.NeedUserInput != nil && detail.Feature.NeedUserInput.Open {
		return *detail.Feature.NeedUserInput, true
	}
	for _, gate := range m.prompts.NeedUserInputs {
		if gate.FeatureID == featureID && gate.Open {
			return gate, true
		}
	}
	return server.NeedInputGateDTO{}, false
}

func (m APIAppModel) selectedPendingPermission(featureID string) (server.ControlRequestDTO, bool) {
	if featureID == "" {
		return server.ControlRequestDTO{}, false
	}
	for _, req := range m.permissions.Requests {
		if req.FeatureID == featureID && req.ToolName != "AskUserQuestion" && isPendingControlStatus(req.Status) {
			return req, true
		}
	}
	return server.ControlRequestDTO{}, false
}

func (m APIAppModel) selectedPendingHelp(featureID string) (server.HelpQueueDTO, bool) {
	if featureID == "" {
		return server.HelpQueueDTO{}, false
	}
	for _, help := range m.prompts.HelpQueue {
		if help.FeatureID == featureID && help.Pending {
			return help, true
		}
	}
	return server.HelpQueueDTO{}, false
}

func (m APIAppModel) selectedPendingAskUser(featureID string) (server.ControlRequestDTO, bool) {
	if featureID == "" {
		return server.ControlRequestDTO{}, false
	}
	for _, req := range m.prompts.AskUserQuestions {
		if req.FeatureID == featureID && req.ToolName == "AskUserQuestion" && isPendingControlStatus(req.Status) {
			return req, true
		}
	}
	return server.ControlRequestDTO{}, false
}

func askUserQuestionLabel(req server.ControlRequestDTO) string {
	if len(req.Questions) > 0 {
		question := strings.TrimSpace(req.Questions[0].Question)
		if question == "" {
			question = strings.TrimSpace(req.Questions[0].Header)
		}
		if question != "" {
			return question
		}
	}
	return strings.TrimSpace(req.Summary)
}

func apiAskUserFirstQuestion(req server.ControlRequestDTO) (server.AskUserQuestionDTO, bool) {
	if len(req.Questions) == 0 {
		return server.AskUserQuestionDTO{}, false
	}
	q := req.Questions[0]
	if strings.TrimSpace(q.Question) == "" {
		q.Question = strings.TrimSpace(q.Header)
	}
	return q, true
}

func (m APIAppModel) stopOwnedServerCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := m.client.Shutdown(ctx)
		if err == nil && m.waitForOwnedServerShutdown != nil {
			err = m.waitForOwnedServerShutdown(ctx)
		}
		return apiOwnedServerStoppedMsg{err: err}
	}
}

type apiRuntimeConfigEditor struct {
	draft   config.ModelConfig
	catalog PhaseModelCatalog
	cursor  int
}

func newAPIRuntimeConfigEditor(defaults config.ModelConfig, catalog PhaseModelCatalog) *apiRuntimeConfigEditor {
	return &apiRuntimeConfigEditor{
		draft:   defaults,
		catalog: catalog,
	}
}

func (e *apiRuntimeConfigEditor) move(delta int) {
	fields := e.fields()
	if len(fields) == 0 {
		e.cursor = 0
		return
	}
	e.cursor += delta
	if e.cursor < 0 {
		e.cursor = len(fields) - 1
	}
	if e.cursor >= len(fields) {
		e.cursor = 0
	}
}

func (e *apiRuntimeConfigEditor) cycleModel(delta int) {
	field := e.currentField()
	if field == "" {
		return
	}
	opts := e.catalog.ModelOptionsForField(field)
	if len(opts) == 0 {
		return
	}
	current := e.modelValue(field)
	idx := -1
	for i, opt := range opts {
		if opt == current {
			idx = i
			break
		}
	}
	if idx == -1 {
		if delta < 0 {
			idx = len(opts) - 1
		} else {
			idx = 0
		}
	} else {
		idx = (idx + delta + len(opts)) % len(opts)
	}
	e.setModelValue(field, opts[idx])
}

func (e apiRuntimeConfigEditor) render(width int) string {
	if width < 52 {
		width = 52
	}
	var b strings.Builder
	b.WriteString("Runtime config\n\n")
	b.WriteString("  Default models\n\n")
	for i, field := range e.fields() {
		prefix := "  "
		line := fmt.Sprintf("%-14s %s", field, apiConfigModelSummary(e.modelValue(field)))
		if i == e.cursor {
			prefix = "> "
			line = SelectedRowStyle.Render(line)
		}
		b.WriteString(prefix + line + "\n")
	}
	b.WriteString("\n")
	b.WriteString(KeyHelpStyle.Render(" [up/down] Field   [tab] Change model   [enter] Save   [esc] Cancel"))
	return panelStyle(true).Width(width).Render(b.String())
}

func (e apiRuntimeConfigEditor) fields() []string {
	if len(e.catalog.Fields) > 0 {
		return e.catalog.Fields
	}
	return phaseCatalogFields
}

func (e apiRuntimeConfigEditor) currentField() string {
	fields := e.fields()
	if e.cursor < 0 || e.cursor >= len(fields) {
		return ""
	}
	return fields[e.cursor]
}

func (e apiRuntimeConfigEditor) modelValue(field string) string {
	switch field {
	case "Research":
		return e.draft.Research
	case "Planning":
		return e.draft.Planning
	case "Implementation":
		return e.draft.Implementation
	case "Review":
		return e.draft.Review
	case "KB Build":
		return e.draft.KBBuild
	default:
		return ""
	}
}

func (e *apiRuntimeConfigEditor) setModelValue(field, value string) {
	switch field {
	case "Research":
		e.draft.Research = value
	case "Planning":
		e.draft.Planning = value
	case "Implementation":
		e.draft.Implementation = value
	case "Review":
		e.draft.Review = value
	case "KB Build":
		e.draft.KBBuild = value
	}
}

func newAPIEditConfigModel(featureID, featureName string, response server.FeatureConfigResponse, catalog PhaseModelCatalog) *EditConfigModel {
	if response.FeatureID != "" {
		featureID = response.FeatureID
	}
	if featureName == "" {
		featureName = featureID
	}
	f := apiFeatureFromConfig(featureID, featureName, response)
	model := NewEditConfigModel(f, catalog, apiFeatureConfigPublishable(response.Publish))
	return &model
}

func apiFeatureFromConfig(featureID, featureName string, response server.FeatureConfigResponse) *feature.Feature {
	current := response.Current
	pipeline := feature.PipelineProfile(current.Pipeline)
	if !pipeline.IsValid() {
		pipeline = feature.PipelineProfile(response.Defaults.Pipeline)
	}
	if !pipeline.IsValid() {
		pipeline = feature.PipelineLarge
	}
	return &feature.Feature{
		ID:          featureID,
		Name:        featureName,
		Models:      current.Models,
		Inquireness: feature.Inquireness(current.Inquireness),
		Checkpoints: feature.Checkpoints{
			InquiryReview:   current.Checkpoints.InquiryReview,
			ResearchReview:  current.Checkpoints.ResearchReview,
			DesignReview:    current.Checkpoints.DesignReview,
			RoadmapReview:   current.Checkpoints.RoadmapReview,
			PhasePlanReview: current.Checkpoints.PhasePlanReview,
			ManualPublish:   current.Checkpoints.ManualPublish,
		},
		Pipeline: pipeline,
		Repos:    apiFeatureConfigRepos(response.Publish),
	}
}

func apiFeatureConfigRepos(publish server.PublishabilityDTO) []feature.FeatureRepo {
	repos := make([]feature.FeatureRepo, 0, len(publish.Repos))
	names := make([]string, 0, len(publish.Repos))
	for name := range publish.Repos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		publishable := publish.Repos[name]
		value := publishable
		repos = append(repos, feature.FeatureRepo{Name: name, Publishable: &value})
	}
	return repos
}

func apiFeatureConfigPublishable(publish server.PublishabilityDTO) bool {
	for _, repoPublishable := range publish.Repos {
		if !repoPublishable {
			return false
		}
	}
	return true
}

func apiConfigModelSummary(value string) string {
	if value == "" {
		return "(default)"
	}
	return value
}

func apiPhaseModelCatalog(resp server.ModelCatalogResponse) PhaseModelCatalog {
	cat := PhaseModelCatalog{
		Fields:              append([]string(nil), phaseCatalogFields...),
		ProviderModels:      map[string][]string{},
		ProviderOrder:       append([]string(nil), resp.ProviderOrder...),
		PhaseDefaults:       map[string]string{},
		PhaseProviderModels: map[string]map[string][]string{},
	}
	missingProviders := map[string]bool{}
	cat.PhaseDefaults["Research"] = resp.PhaseDefaults.Research
	cat.PhaseDefaults["Planning"] = resp.PhaseDefaults.Planning
	cat.PhaseDefaults["Implementation"] = resp.PhaseDefaults.Implementation
	cat.PhaseDefaults["Review"] = resp.PhaseDefaults.Review
	cat.PhaseDefaults["KB Build"] = resp.PhaseDefaults.KBBuild
	for provider, models := range resp.ProviderModels {
		ids := make([]string, 0, len(models))
		for _, model := range models {
			if model.ID != "" {
				ids = append(ids, model.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		cat.ProviderModels[provider] = ids
		if !stringSliceContains(cat.ProviderOrder, provider) {
			missingProviders[provider] = true
		}
	}
	for field, providerModels := range resp.PhaseProviderModels {
		cat.PhaseProviderModels[field] = map[string][]string{}
		for provider, models := range providerModels {
			cat.PhaseProviderModels[field][provider] = append([]string(nil), models...)
			if !stringSliceContains(cat.ProviderOrder, provider) {
				missingProviders[provider] = true
			}
		}
	}
	missingOrder := make([]string, 0, len(missingProviders))
	for provider := range missingProviders {
		missingOrder = append(missingOrder, provider)
	}
	sort.Strings(missingOrder)
	for _, provider := range missingOrder {
		cat.ProviderOrder = append(cat.ProviderOrder, provider)
	}
	return cat
}

func stringSliceContains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func nonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func apiPRNumberLabel(prURL string) string {
	prURL = strings.TrimSpace(prURL)
	if prURL == "" {
		return ""
	}
	parts := strings.Split(strings.TrimRight(prURL, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return prURL
	}
	return "PR #" + parts[len(parts)-1]
}

func apiReviewCommentModes(values []string) []string {
	modes := make([]string, 0, len(values))
	for _, value := range values {
		mode := strings.ToLower(strings.TrimSpace(value))
		if (mode == "auto" || mode == "address_all") && !stringSliceContains(modes, mode) {
			modes = append(modes, mode)
		}
	}
	if len(modes) == 0 {
		return []string{"auto", "address_all"}
	}
	return modes
}

func apiNextReviewCommentMode(current string, modes []string) string {
	modes = apiReviewCommentModes(modes)
	for i, mode := range modes {
		if mode == current {
			return modes[(i+1)%len(modes)]
		}
	}
	return modes[0]
}

func apiRefactorPipelines(values []string) []feature.PipelineProfile {
	pipelines := make([]feature.PipelineProfile, 0, len(values))
	for _, value := range values {
		var pipeline feature.PipelineProfile
		switch strings.ToLower(strings.TrimSpace(value)) {
		case string(feature.PipelineMedium):
			pipeline = feature.PipelineMedium
		case string(feature.PipelineLarge):
			pipeline = feature.PipelineLarge
		case string(feature.PipelineMoonshot):
			pipeline = feature.PipelineMoonshot
		default:
			continue
		}
		if !pipelineSliceContains(pipelines, pipeline) {
			pipelines = append(pipelines, pipeline)
		}
	}
	if len(pipelines) == 0 {
		return []feature.PipelineProfile{feature.PipelineMedium, feature.PipelineLarge, feature.PipelineMoonshot}
	}
	return pipelines
}

func apiDefaultRefactorPipelineCursor(pipelines []feature.PipelineProfile) int {
	for i, pipeline := range pipelines {
		if pipeline == feature.PipelineLarge {
			return i
		}
	}
	return 0
}

func pipelineSliceContains(values []feature.PipelineProfile, needle feature.PipelineProfile) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func apiReviewCommentLocation(comment server.ReviewCommentDTO) string {
	if comment.Path != "" {
		if comment.Line > 0 {
			return fmt.Sprintf("%s:%d", comment.Path, comment.Line)
		}
		return comment.Path
	}
	switch strings.ToLower(strings.TrimSpace(comment.Type)) {
	case "issue":
		return "PR conversation"
	case "review":
		return "PR review"
	default:
		return strings.TrimSpace(comment.Type)
	}
}

func (m APIAppModel) selectedFeatureCurrentPhase(featureID string) string {
	if featureID == "" {
		return ""
	}
	if detail, ok := m.featureDetails[featureID]; ok {
		if phase := strings.ToLower(strings.TrimSpace(detail.Feature.CurrentPhase)); phase != "" {
			return phase
		}
	}
	for _, feature := range m.featureList.Features {
		if feature.ID == featureID {
			return strings.ToLower(strings.TrimSpace(feature.CurrentPhase))
		}
	}
	for _, feature := range m.snapshot.Features {
		if feature.ID == featureID {
			return strings.ToLower(strings.TrimSpace(feature.CurrentPhase))
		}
	}
	return ""
}

func (m APIAppModel) selectedFeatureStatus(featureID string) (feature.Status, bool) {
	if featureID == "" {
		return feature.StatusCreated, false
	}
	if detail, ok := m.featureDetails[featureID]; ok && strings.TrimSpace(detail.Feature.Status) != "" {
		return apiFeatureStatus(detail.Feature.Status), true
	}
	for _, f := range m.featureList.Features {
		if f.ID == featureID {
			return apiFeatureStatus(f.Status), true
		}
	}
	for _, f := range m.snapshot.Features {
		if f.ID == featureID {
			return apiFeatureStatus(f.Status), true
		}
	}
	return feature.StatusCreated, false
}

func (m APIAppModel) selectedRewindChoices() []apiRewindChoice {
	choices := []apiRewindChoice{}
	addPhase := func(option string) {
		option = strings.ToLower(strings.TrimSpace(option))
		if option == "" {
			return
		}
		for _, choice := range choices {
			if choice.UpgradePipeline == "" && choice.TargetPhase == option {
				return
			}
		}
		choices = append(choices, apiRewindChoice{
			TargetPhase: option,
			Label:       apiRewindChoiceLabel(option),
		})
	}
	addUpgrade := func(option string) {
		option = strings.ToLower(strings.TrimSpace(option))
		profile := feature.PipelineProfile(option)
		if profile == "" {
			return
		}
		choices = append(choices, apiRewindChoice{
			TargetPhase:     feature.PhaseInquire.DirName(),
			Label:           "Upgrade to " + string(profile) + " " + MutedStyle.Render("(rewinds to KB Build)"),
			OverridePhase:   feature.PhaseKnowledgeBase.DirName(),
			UpgradePipeline: profile,
		})
	}
	action, ok := m.selectedRawAction("feature.rewind")
	if ok {
		for _, input := range action.RequiredInputs {
			if input.Name == "target_phase" {
				for _, option := range input.Options {
					addPhase(option)
				}
			}
		}
		for _, input := range action.RequiredInputs {
			if input.Name == "upgrade_pipeline" {
				for _, option := range input.Options {
					addUpgrade(option)
				}
			}
		}
		if len(choices) > 0 {
			return choices
		}
	}
	if phase := m.selectedFeatureCurrentPhase(m.selectedFeature); phase != "" {
		addPhase(phase)
		return choices
	}
	return nil
}

func (m APIAppModel) newAPIRoadmapRewindPanel(featureID, featureName string) (*apiRoadmapRewindPanel, bool) {
	current, total := m.selectedRoadmapProgress(featureID)
	if total <= 1 {
		return nil, false
	}
	rows := make([]apiRoadmapRewindRow, 0, total)
	cursor := 0
	for phaseNum := 1; phaseNum <= total; phaseNum++ {
		row := apiRoadmapRewindRow{
			Number:        phaseNum,
			Total:         total,
			Title:         fmt.Sprintf("Phase %d", phaseNum),
			PhaseType:     fallbackRoadmapPhaseType(phaseNum, total),
			Status:        apiRoadmapPhaseStatus(current, phaseNum),
			Effect:        roadmapPhaseEffect(phaseNum, total),
			ResetBoundary: roadmapResetBoundaryLabel(phaseNum),
			CurrentPhase:  current == phaseNum,
		}
		if row.CurrentPhase {
			cursor = len(rows)
		}
		rows = append(rows, row)
	}
	if featureName == "" {
		featureName = m.featureNameByID(featureID)
	}
	return &apiRoadmapRewindPanel{
		featureID:   featureID,
		featureName: featureName,
		rows:        rows,
		cursor:      cursor,
	}, true
}

func (m APIAppModel) selectedRoadmapProgress(featureID string) (int, int) {
	if detail, ok := m.featureDetails[featureID]; ok {
		current := detail.Feature.Progress.CurrentRoadmapPhase
		total := detail.Feature.Progress.TotalRoadmapPhases
		if detail.Feature.ActiveRun != nil {
			current = firstNonZero(detail.Feature.ActiveRun.RoadmapPhase, current)
			total = firstNonZero(detail.Feature.ActiveRun.RoadmapTotal, total)
		}
		return current, total
	}
	for _, summary := range m.featureList.Features {
		if summary.ID == featureID {
			return summary.Progress.CurrentRoadmapPhase, summary.Progress.TotalRoadmapPhases
		}
	}
	return 0, 0
}

func apiRoadmapPhaseStatus(current, phaseNum int) string {
	switch {
	case current == phaseNum:
		return "current"
	case current > phaseNum:
		return "completed"
	default:
		return "pending"
	}
}

func apiAttentionCounts(prompts server.PromptSnapshotResponse, permissions server.PermissionSnapshotResponse) map[string]int {
	counts := map[string]int{}
	for _, h := range prompts.HelpQueue {
		if h.Pending && h.FeatureID != "" {
			counts[h.FeatureID]++
		}
	}
	for _, gate := range prompts.NeedUserInputs {
		if gate.Open && gate.FeatureID != "" {
			counts[gate.FeatureID]++
		}
	}
	for _, ask := range prompts.AskUserQuestions {
		if isPendingControlStatus(ask.Status) && ask.FeatureID != "" {
			counts[ask.FeatureID]++
		}
	}
	for _, req := range permissions.Requests {
		if isPendingControlStatus(req.Status) && req.FeatureID != "" {
			counts[req.FeatureID]++
		}
	}
	return counts
}

func isPendingControlStatus(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	return status == "" || status == "pending" || status == "waiting"
}

func apiFeatureSortOrder(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done":
		return 2
	case "published":
		return 1
	default:
		return 0
	}
}

func apiHasFeature(features []APIFeaturePresentation, id string) bool {
	if id == "" {
		return false
	}
	for _, f := range features {
		if f.ID == id {
			return true
		}
	}
	return false
}

func apiHasRepo(repos []feature.FeatureRepo, name string) bool {
	for _, repo := range repos {
		if repo.Name == name {
			return true
		}
	}
	return false
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func apiFeatureStatus(status string) feature.Status {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "created":
		return feature.StatusCreated
	case "researching":
		return feature.StatusResearching
	case "planready", "plan_ready", "plan-ready":
		return feature.StatusPlanReady
	case "planning":
		return feature.StatusPlanning
	case "implementready", "implement_ready", "implement-ready":
		return feature.StatusImplementReady
	case "implementing":
		return feature.StatusImplementing
	case "reviewpassed", "review_passed", "review-passed":
		return feature.StatusReviewPassed
	case "codeready", "code_ready", "code-ready", "prready", "pr_ready", "pr-ready":
		return feature.StatusCodeReady
	case "published":
		return feature.StatusPublished
	case "failed":
		return feature.StatusFailed
	case "interrupted":
		return feature.StatusInterrupted
	case "done":
		return feature.StatusDone
	case "buildingkb", "building_kb", "building-kb":
		return feature.StatusBuildingKB
	case "planneedsreview", "plan_needs_review", "plan-needs-review":
		return feature.StatusPlanNeedsReview
	case "inquiring":
		return feature.StatusInquiring
	case "inquireready", "inquire_ready", "inquire-ready":
		return feature.StatusInquireReady
	case "designready", "design_ready", "design-ready":
		return feature.StatusDesignReady
	case "designing":
		return feature.StatusDesigning
	case "promptneedsreview", "prompt_needs_review", "prompt-needs-review":
		return feature.StatusPromptNeedsReview
	case "inquiryneedsreview", "inquiry_needs_review", "inquiry-needs-review":
		return feature.StatusInquiryNeedsReview
	case "researchneedsreview", "research_needs_review", "research-needs-review":
		return feature.StatusResearchNeedsReview
	case "designneedsreview", "design_needs_review", "design-needs-review":
		return feature.StatusDesignNeedsReview
	case "reviewing":
		return feature.StatusReviewing
	case "needuserinput", "need_user_input", "need-user-input":
		return feature.StatusNeedUserInput
	case "finalreviewing", "final_reviewing", "final-reviewing":
		return feature.StatusFinalReviewing
	default:
		return feature.StatusCreated
	}
}

func apiFeaturePhase(phase string) feature.Phase {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "knowledgebase", "knowledge_base", "knowledge-base", "kb":
		return feature.PhaseKnowledgeBase
	case "inquire", "inquiry":
		return feature.PhaseInquire
	case "research":
		return feature.PhaseResearch
	case "design", "brainstorm":
		return feature.PhaseDesign
	case "plan", "planning":
		return feature.PhasePlan
	case "implement", "implementation":
		return feature.PhaseImplement
	case "review":
		return feature.PhaseReview
	case "finalreview", "final_review", "final-review":
		return feature.PhaseFinalReview
	case "publish", "publishing":
		return feature.PhasePublish
	default:
		return feature.PhaseKnowledgeBase
	}
}

func apiPhaseTimings(in map[string]int64) map[string]time.Duration {
	if len(in) == 0 {
		return map[string]time.Duration{}
	}
	out := make(map[string]time.Duration, len(in))
	for key, seconds := range in {
		if seconds <= 0 {
			continue
		}
		out[key] = time.Duration(seconds) * time.Second
	}
	return out
}

func apiPhaseCosts(byPhase map[string]float64, total float64, current feature.Phase) map[string]float64 {
	out := make(map[string]float64, len(byPhase)+1)
	for key, cost := range byPhase {
		if cost > 0 {
			out[key] = cost
		}
	}
	if len(out) == 0 && total > 0 {
		key := current.DirName()
		if key == "" {
			key = "implement"
		}
		out[key] = total
	}
	return out
}

func apiFeatureCanStop(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "researching", "planning", "implementing", "reviewing", "publishing", "running":
		return true
	default:
		return false
	}
}

func apiFeatureCanResume(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "interrupted", "needuserinput":
		return true
	default:
		return false
	}
}

func apiFeatureDetailPresentation(dto server.FeatureDetailDTO) APIFeatureDetailPresentation {
	name := dto.Name
	if name == "" {
		name = dto.Slug
	}
	out := APIFeatureDetailPresentation{
		ID:           dto.ID,
		Name:         name,
		Description:  dto.Description,
		Summary:      dto.Summary,
		Pipeline:     dto.Pipeline,
		TotalCostUSD: dto.Cost.TotalUSD,
	}
	for _, repo := range dto.RepoStatus {
		stateParts := make([]string, 0, 4)
		if repo.Touched {
			stateParts = append(stateParts, "touched")
		}
		if repo.Publishable {
			stateParts = append(stateParts, "publishable")
		}
		if repo.CycleType != "" || repo.CycleStatus != "" {
			cycle := strings.Trim(strings.TrimSpace(repo.CycleType)+"/"+strings.TrimSpace(repo.CycleStatus), "/")
			if cycle != "" {
				stateParts = append(stateParts, cycle)
			}
		}
		if repo.LastError != "" {
			stateParts = append(stateParts, firstLine(repo.LastError))
		}
		out.Repos = append(out.Repos, APIRepoStatusPresentation{
			Name:  repo.Name,
			State: strings.Join(stateParts, " "),
		})
	}
	for _, action := range dto.Actions {
		status := "ready"
		reason := ""
		if !action.Enabled {
			status = "disabled"
			if len(action.DisabledReasons) > 0 {
				reason = action.DisabledReasons[0].Message
			}
		}
		out.Actions = append(out.Actions, APIActionPresentation{
			ID:     action.ID,
			Status: status,
			Reason: reason,
		})
	}
	if dto.NeedUserInput != nil && dto.NeedUserInput.Open {
		out.NeedUserInputLabel = "Need user input"
		if dto.NeedUserInput.Iteration > 0 {
			out.NeedUserInputLabel = fmt.Sprintf("Need user input iteration %d", dto.NeedUserInput.Iteration)
		}
	}
	if dto.Failure != nil {
		out.Failure = firstLine(dto.Failure.Message)
	}
	return out
}

func apiSessionPresentations(resp server.SessionListResponse, details map[string]server.SessionDetailResponse, selectedFeatureID string) []APISessionPresentation {
	out := make([]APISessionPresentation, 0, len(resp.Sessions))
	for _, sess := range resp.Sessions {
		if selectedFeatureID != "" && sess.FeatureID != selectedFeatureID {
			continue
		}
		presentation := apiSessionPresentation(sess)
		if detail, ok := details[sess.ID]; ok {
			presentation.CanAttach = detail.Session.CanAttach
			presentation.LogAvailable = detail.Session.LogAvailable
		}
		out = append(out, presentation)
	}
	return out
}

func apiSessionPresentation(dto server.SessionSummaryDTO) APISessionPresentation {
	return APISessionPresentation{
		ID:         dto.ID,
		FeatureID:  dto.FeatureID,
		Phase:      dto.Phase,
		Repo:       dto.Repo,
		Kind:       dto.Kind,
		Label:      dto.Label,
		Provider:   dto.Provider,
		Model:      dto.Model,
		Status:     dto.Status,
		ContextPct: dto.ContextPct,
	}
}

func apiSelectedSessionID(preview server.LivePreviewResponse) string {
	if preview.Session == nil {
		return ""
	}
	return preview.Session.ID
}

func loadAPITranscriptTail(ctx context.Context, client APIClient, sessionID string) (server.SessionDetailResponse, server.TranscriptResponse, error) {
	session, err := client.SessionDetail(ctx, sessionID)
	if err != nil {
		return server.SessionDetailResponse{}, server.TranscriptResponse{}, fmt.Errorf("load selected session detail snapshot: %w", err)
	}
	end := session.Session.TranscriptCursor.End
	if end == 0 {
		end = session.Session.TranscriptCursor.Total
	}
	start := max(0, end-apiTranscriptPageLimit)
	transcript, err := client.Transcript(ctx, sessionID, server.CursorQuery{Cursor: start, Limit: apiTranscriptPageLimit})
	if err != nil {
		return server.SessionDetailResponse{}, server.TranscriptResponse{}, fmt.Errorf("load selected session transcript snapshot: %w", err)
	}
	return session, transcript, nil
}

func loadAPISelectedContent(ctx context.Context, client APIClient, featureID string, detail server.FeatureDetailResponse, previous *apiFeatureContentSnapshot) *apiFeatureContentSnapshot {
	runNumber := apiActiveRunNumber(detail.Feature)
	if featureID == "" || runNumber <= 0 {
		return nil
	}
	out := apiFeatureContentSnapshot{FeatureID: featureID, RunNumber: runNumber}
	logID := "session"
	if previous != nil && previous.RunNumber == runNumber && validSelectableLogID(previous.LogID) {
		logID = previous.LogID
	}
	logQuery := server.TextQuery{Limit: apiContentTailLimit}
	if previous != nil && previous.RunNumber == runNumber && previous.Log != nil && logID == previous.LogID {
		logQuery.Offset = apiContentTailOffset(previous.Log.Size)
	}
	out.LogID = logID
	if logContent, err := client.LogContent(ctx, featureID, runNumber, logID, logQuery); err == nil {
		out.Log = &logContent
		out.ContentLoaded = true
	}
	if artifacts, err := client.ArtifactList(ctx, featureID, runNumber); err == nil {
		out.Artifacts = artifacts
		artifactID := ""
		if previous != nil && previous.RunNumber == runNumber {
			artifactID = previous.ArtifactID
		}
		if artifact, ok := selectedAvailableTextArtifact(artifacts, artifactID); ok {
			query := server.TextQuery{Offset: apiContentTailOffset(artifact.Size), Limit: apiContentTailLimit}
			if content, err := client.ArtifactContent(ctx, featureID, runNumber, artifact.ID, query); err == nil {
				artifactCopy := artifact
				out.ArtifactID = artifact.ID
				out.ArtifactMeta = &artifactCopy
				out.Artifact = &content
				out.ContentLoaded = true
			}
		}
	}
	if !out.ContentLoaded && len(out.Artifacts.Artifacts) == 0 {
		return nil
	}
	return &out
}

func apiActiveRunNumber(dto server.FeatureDetailDTO) int {
	if dto.ActiveRun != nil && dto.ActiveRun.RunNumber > 0 {
		return dto.ActiveRun.RunNumber
	}
	return dto.FeatureSummary.ActiveRun
}

func apiContentTailOffset(size int64) int64 {
	if size <= apiContentTailLimit {
		return 0
	}
	return size - apiContentTailLimit
}

func firstAvailableTextArtifact(resp server.ArtifactListResponse) (server.ArtifactDTO, bool) {
	artifact, ok := selectedAvailableTextArtifact(resp, "")
	return artifact, ok
}

func selectedAvailableTextArtifact(resp server.ArtifactListResponse, artifactID string) (server.ArtifactDTO, bool) {
	artifacts := availableTextArtifacts(resp)
	if len(artifacts) == 0 {
		return server.ArtifactDTO{}, false
	}
	if artifactID != "" {
		for _, artifact := range artifacts {
			if artifact.ID == artifactID {
				return artifact, true
			}
		}
	}
	return artifacts[0], true
}

func availableTextArtifacts(resp server.ArtifactListResponse) []server.ArtifactDTO {
	out := make([]server.ArtifactDTO, 0, len(resp.Artifacts))
	for _, artifact := range resp.Artifacts {
		if artifact.ContentAvailable {
			out = append(out, artifact)
		}
	}
	return out
}

func cycleArtifactSelection(artifacts []server.ArtifactDTO, currentID string, delta int) server.ArtifactDTO {
	if len(artifacts) == 0 {
		return server.ArtifactDTO{}
	}
	idx := 0
	for i, artifact := range artifacts {
		if artifact.ID == currentID {
			idx = i
			break
		}
	}
	idx = (idx + delta) % len(artifacts)
	if idx < 0 {
		idx += len(artifacts)
	}
	return artifacts[idx]
}

func validSelectableLogID(logID string) bool {
	for _, known := range apiSelectableLogIDs {
		if logID == known {
			return true
		}
	}
	return false
}

func cycleLogIDs(currentID string) []string {
	if len(apiSelectableLogIDs) == 0 {
		return nil
	}
	start := -1
	for i, logID := range apiSelectableLogIDs {
		if logID == currentID {
			start = i
			break
		}
	}
	out := make([]string, 0, len(apiSelectableLogIDs))
	for i := 1; i <= len(apiSelectableLogIDs); i++ {
		out = append(out, apiSelectableLogIDs[(start+i)%len(apiSelectableLogIDs)])
	}
	return out
}

func apiLivePreviewPresentation(featureID string, dto server.LivePreviewResponse) APILivePreviewPresentation {
	if featureID == "" {
		featureID = dto.Feature.ID
	}
	out := APILivePreviewPresentation{
		FeatureID:  featureID,
		Activity:   strings.TrimSpace(dto.Activity),
		ContextPct: dto.Context.Percentage,
		CostUSD:    dto.Cost.TotalUSD,
	}
	if dto.Session != nil {
		out.SessionID = dto.Session.ID
	}
	for _, req := range dto.Attention {
		summary := strings.TrimSpace(req.Summary)
		if summary == "" {
			summary = strings.TrimSpace(req.ToolName)
		}
		if summary == "" {
			continue
		}
		tool := strings.TrimSpace(req.ToolName)
		if tool == "" {
			tool = "Attention"
		}
		out.Attention = append(out.Attention, tool+": "+firstLine(summary))
	}
	for _, msg := range dto.Transcript {
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			text = strings.TrimSpace(msg.Tool)
		}
		if text == "" {
			continue
		}
		out.TranscriptTail = append(out.TranscriptTail, firstLine(text))
	}
	return out
}

func apiTranscriptPresentation(sessionID string, dto server.TranscriptResponse) APITranscriptPresentation {
	out := APITranscriptPresentation{
		SessionID: sessionID,
		Start:     dto.Cursor.Start,
		End:       dto.Cursor.End,
		Total:     dto.Cursor.Total,
	}
	for _, msg := range dto.Messages {
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			text = strings.TrimSpace(msg.Tool)
		}
		if text == "" {
			continue
		}
		prefix := strings.TrimSpace(msg.Role)
		if prefix == "" {
			prefix = strings.TrimSpace(msg.Type)
		}
		if prefix != "" {
			text = prefix + ": " + firstLine(text)
		} else {
			text = firstLine(text)
		}
		out.Lines = append(out.Lines, text)
	}
	return out
}

func apiContentPresentation(dto apiFeatureContentSnapshot) APIContentPresentation {
	out := APIContentPresentation{
		FeatureID: dto.FeatureID,
		RunNumber: dto.RunNumber,
	}
	if dto.Log != nil {
		log := apiTextSnippetPresentation(*dto.Log)
		log.ID = firstNonEmpty(log.ID, dto.LogID)
		out.Log = &log
	}
	if dto.Artifact != nil {
		artifact := APIArtifactSnippetPresentation{
			ID:        firstNonEmpty(dto.Artifact.ID, dto.ArtifactID),
			Offset:    dto.Artifact.Offset,
			Limit:     dto.Artifact.Limit,
			Size:      dto.Artifact.Size,
			Text:      dto.Artifact.Text,
			Truncated: dto.Artifact.Truncated,
		}
		if dto.ArtifactMeta != nil {
			artifact.ID = firstNonEmpty(artifact.ID, dto.ArtifactMeta.ID)
			artifact.Type = dto.ArtifactMeta.Type
			artifact.Category = dto.ArtifactMeta.Category
			artifact.Phase = dto.ArtifactMeta.Phase
		}
		out.Artifact = &artifact
	}
	return out
}

func apiTextSnippetPresentation(dto server.TextContentResponse) APITextSnippetPresentation {
	return APITextSnippetPresentation{
		ID:        dto.ID,
		Offset:    dto.Offset,
		Limit:     dto.Limit,
		Size:      dto.Size,
		Text:      dto.Text,
		Truncated: dto.Truncated,
	}
}

func apiMutationKindLabel(kind string) string {
	switch kind {
	case "feature.create":
		return "Create Feature"
	case "feature.start":
		return "Start"
	case "feature.resume":
		return "Resume"
	case "feature.publish":
		return "Publish"
	case "feature.merge":
		return "Merge"
	case "feature.stop":
		return "Stop"
	case "feature.delete":
		return "Delete"
	case "feature.restart":
		return "Restart"
	case "feature.retry":
		return "Retry"
	case "feature.mark-done":
		return "Mark Done"
	case "feature.rebase":
		return "Rebase"
	case "feature.cleanup":
		return "Cleanup"
	case "feature.rewind":
		return "Rewind"
	case "feature.tweak.start":
		return "Tweak"
	case "feature.tweak.finish":
		return "Finish Tweak"
	case "feature.review_comments":
		return "Review Comments"
	case "feature.refactor.start":
		return "Refactor"
	case "feature.refactor.restart":
		return "Restart Refactor"
	case "feature.need_user_input.decision":
		return "Need Input Decision"
	case "feature.input_notifications.toggle":
		return "Input Alerts"
	case "feature.review_decision":
		return "Review Decision"
	case "feature.config.update":
		return "Feature Config"
	case "runtime.config.update":
		return "Runtime Config"
	case "permission.answer":
		return "Permission Answer"
	case "help.send":
		return "Help Reply"
	case "ask_user.answer":
		return "AskUser Answer"
	case "recovery.execute":
		return "Recovery"
	default:
		return strings.TrimSpace(kind)
	}
}

func truncatePlain(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if len(s) <= maxWidth {
		return s
	}
	if maxWidth == 1 {
		return s[:1]
	}
	return s[:maxWidth-1] + "..."
}
