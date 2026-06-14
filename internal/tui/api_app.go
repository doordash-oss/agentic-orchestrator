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
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
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
	Operations(context.Context, server.OperationQuery) (server.OperationSnapshotResponse, error)
	CreateFeature(context.Context, server.CreateFeatureRequest) (server.OperationAcceptedResponse, error)
	StartFeature(context.Context, string) (server.OperationAcceptedResponse, error)
	ResumeFeature(context.Context, string) (server.OperationAcceptedResponse, error)
	RestartFeature(context.Context, string, server.RestartFeatureRequest) (server.OperationAcceptedResponse, error)
	StopFeature(context.Context, string) (server.OperationAcceptedResponse, error)
	DeleteFeature(context.Context, string) (server.OperationAcceptedResponse, error)
	PublishFeature(context.Context, string, server.PublishFeatureRequest) (server.OperationAcceptedResponse, error)
	MergeFeature(context.Context, string) (server.OperationAcceptedResponse, error)
	RewindFeature(context.Context, string, server.RewindFeatureRequest) (server.OperationAcceptedResponse, error)
	RetryFeature(context.Context, string) (server.OperationAcceptedResponse, error)
	StartRebase(context.Context, string, server.RebaseActionRequest) (server.OperationAcceptedResponse, error)
	MarkDone(context.Context, string) (server.OperationAcceptedResponse, error)
	CleanupFeature(context.Context, string, server.CleanupActionRequest) (server.OperationAcceptedResponse, error)
	UpdateFeatureConfig(context.Context, string, server.FeatureConfigMutationRequest) (server.OperationAcceptedResponse, error)
	UpdateRuntimeConfig(context.Context, server.RuntimeConfigMutationRequest) (server.OperationAcceptedResponse, error)
	ExecuteRecovery(context.Context, server.RecoveryActionRequest) (server.OperationAcceptedResponse, error)
	NeedUserInputDecision(context.Context, string, server.NeedUserInputDecisionRequest) (server.OperationAcceptedResponse, error)
	ReviewDecision(context.Context, string, server.ReviewDecisionRequest) (server.OperationAcceptedResponse, error)
	FetchReviewComments(context.Context, string, server.ReviewCommentsFetchRequest) (server.ReviewCommentsFetchResponse, error)
	StartReviewComments(context.Context, string, server.ReviewCommentsActionRequest) (server.OperationAcceptedResponse, error)
	StartTweak(context.Context, string, server.TweakActionRequest) (server.OperationAcceptedResponse, error)
	FinishTweak(context.Context, string, server.TweakFinishRequest) (server.OperationAcceptedResponse, error)
	StartRefactor(context.Context, string, server.RefactorActionRequest) (server.OperationAcceptedResponse, error)
	RestartRefactor(context.Context, string, server.RefactorActionRequest) (server.OperationAcceptedResponse, error)
	AnswerPermission(context.Context, server.PermissionAnswerRequest) (server.OperationAcceptedResponse, error)
	SendHelp(context.Context, server.HelpAnswerRequest) (server.OperationAcceptedResponse, error)
	AnswerAskUser(context.Context, server.AskUserAnswerRequest) (server.OperationAcceptedResponse, error)
	SubscribeEvents(context.Context, server.EventSubscriptionOptions) (<-chan server.RefreshSignal, <-chan error)
	FetchRefreshSnapshot(context.Context, server.RefreshSignal) (server.RefreshSnapshot, error)
}

type APIAppOptions struct {
	Runtime         server.RuntimeIdentity
	LaunchPolicy    server.LaunchPolicy
	OwnedServer     bool
	EventOptions    server.EventSubscriptionOptions
	StopOwnedServer func(context.Context) error
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
	Operations  []APIOperationPresentation
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

type APIOperationPresentation struct {
	ID        string
	Kind      string
	FeatureID string
	Status    string
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
	client                   APIClient
	featureList              server.FeatureListResponse
	featureDetails           map[string]server.FeatureDetailResponse
	runtimeConfig            server.RuntimeConfigResponse
	catalog                  server.ModelCatalogResponse
	prompts                  server.PromptSnapshotResponse
	permissions              server.PermissionSnapshotResponse
	sessionList              server.SessionListResponse
	sessionDetails           map[string]server.SessionDetailResponse
	livePreviews             map[string]server.LivePreviewResponse
	transcripts              map[string]server.TranscriptResponse
	contents                 map[string]apiFeatureContentSnapshot
	operations               server.OperationSnapshotResponse
	recovery                 server.RecoverySnapshotResponse
	launchPolicy             server.LaunchPolicy
	snapshot                 APIAppSnapshot
	recoveryPanel            *apiRecoveryPanel
	selectedFeature          string
	width                    int
	height                   int
	ownedServer              bool
	stopOwnedServer          func(context.Context) error
	quitOwnedServerPrompt    bool
	actionConfirmActive      bool
	actionConfirmKind        string
	actionConfirmFeatureID   string
	actionConfirmFeatureName string
	tweakReviewModalActive   bool
	tweakReviewFeatureID     string
	tweakReviewFeatureName   string
	needInputPromptActive    bool
	needInputFeatureID       string
	needInputFeatureName     string
	permissionPromptActive   bool
	permissionFeatureID      string
	permissionFeatureName    string
	permissionRequest        server.ControlRequestDTO
	helpPromptActive         bool
	helpFeatureID            string
	helpFeatureName          string
	helpQuestion             string
	helpAnswerDraft          string
	askUserPromptActive      bool
	askUserFeatureID         string
	askUserFeatureName       string
	askUserQuestion          string
	askUserRequest           server.ControlRequestDTO
	askUserAnswerDraft       string
	createPrompt             *apiCreateFeaturePrompt
	reviewComments           *apiReviewCommentsPanel
	refactorPrompt           *apiRefactorPrompt
	refactorPipeline         *apiRefactorPipelinePanel
	configEditor             *apiFeatureConfigEditor
	runtimeConfigEditor      *apiRuntimeConfigEditor
	statusMessage            string
	eventCtx                 context.Context
	cancelEvents             context.CancelFunc
	signals                  <-chan server.RefreshSignal
	eventErrs                <-chan error
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

type apiOperationAcceptedMsg struct {
	kind      string
	featureID string
	accepted  server.OperationAcceptedResponse
	err       error
}

type apiOwnedServerStoppedMsg struct {
	err error
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

type apiRefactorPrompt struct {
	featureID   string
	featureName string
	repo        string
	draft       string
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

type apiCreateFeaturePrompt struct {
	nameDraft        string
	descriptionDraft string
	focus            int
	repoCursor       int
	selectedRepos    map[string]bool
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
	app := APIAppModel{
		client:          client,
		featureDetails:  map[string]server.FeatureDetailResponse{},
		sessionDetails:  map[string]server.SessionDetailResponse{},
		livePreviews:    map[string]server.LivePreviewResponse{},
		transcripts:     map[string]server.TranscriptResponse{},
		contents:        map[string]apiFeatureContentSnapshot{},
		width:           100,
		height:          30,
		ownedServer:     opts.OwnedServer,
		stopOwnedServer: opts.StopOwnedServer,
		launchPolicy:    opts.LaunchPolicy,
	}
	features, err := client.Features(ctx)
	if err != nil {
		return APIAppModel{}, fmt.Errorf("load features snapshot: %w", err)
	}
	runtimeConfig, err := client.RuntimeConfig(ctx)
	if err != nil {
		return APIAppModel{}, fmt.Errorf("load runtime config snapshot: %w", err)
	}
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
	operations, err := client.Operations(ctx, server.OperationQuery{Limit: 20})
	if err != nil {
		return APIAppModel{}, fmt.Errorf("load operation snapshot: %w", err)
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
	app.operations = operations
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
		return m, nil
	case tea.KeyPressMsg:
		return m.handleAPIKey(msg)
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
		m.configEditor = newAPIFeatureConfigEditor(msg.featureID, m.featureNameByID(msg.featureID), msg.config, apiPhaseModelCatalog(m.catalog))
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
	case apiOperationAcceptedMsg:
		if msg.err != nil {
			m.statusMessage = "Mutation failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		m.upsertAcceptedOperation(msg.kind, msg.featureID, msg.accepted)
		m.statusMessage = fmt.Sprintf("Accepted %s operation %s", msg.kind, msg.accepted.OperationID)
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
			m.statusMessage = "Server stop failed: " + firstLine(msg.err.Error())
			m.quitOwnedServerPrompt = false
			return m, nil
		}
		return m, tea.Quit
	default:
		return m, nil
	}
}

func (m APIAppModel) handleAPIKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
	if m.runtimeConfigEditor != nil {
		return m.handleAPIRuntimeConfigEditorKey(msg)
	}
	if m.recoveryPanel != nil {
		return m.handleAPIRecoveryKey(msg)
	}
	if m.configEditor != nil {
		return m.handleAPIConfigEditorKey(msg)
	}
	if m.createPrompt != nil {
		return m.handleAPICreateFeaturePromptKey(msg)
	}
	if m.reviewComments != nil {
		return m.handleAPIReviewCommentsKey(msg)
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
		m.clearActionConfirm()
		if strings.ToLower(msg.Text) == "y" {
			return m, m.selectedFeatureActionCmd(kind, featureID)
		}
		return m, nil
	}
	if msg.Code == 'r' && msg.Mod.Contains(tea.ModCtrl) {
		return m.confirmSelectedFeatureAction("feature.rewind"), nil
	}
	if msg.Code == 'f' && msg.Mod.Contains(tea.ModCtrl) {
		return m.openRefactorRestartPrompt(), nil
	}
	switch msg.Text {
	case "[":
		return m.cycleSelectedArtifact(-1)
	case "]":
		return m.cycleSelectedArtifact(1)
	}
	switch msg.Text {
	case "E":
		return m.openRuntimeConfigEditor(), nil
	case "M":
		return m.confirmSelectedFeatureAction("feature.merge"), nil
	case "R":
		return m.confirmSelectedFeatureAction("feature.retry"), nil
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
		if m.selectedFeature == "" {
			m.statusMessage = "No feature selected"
			return m, nil
		}
		m.statusMessage = "Refreshing attach/live snapshots"
		return m, m.fetchFeatureDetailCmd(m.selectedFeature)
	case "w":
		return m.openCreateFeaturePrompt(2), nil
	case "p":
		return m.confirmSelectedFeatureAction("feature.publish"), nil
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
		return m, m.fetchFeatureConfigCmd(m.selectedFeature)
	case "l":
		return m.cycleSelectedLog()
	case "g":
		return m.openReviewCommentsPreview()
	case "t":
		return m.confirmSelectedFeatureAction("feature.tweak.start"), nil
	case "b":
		return m.confirmSelectedFeatureAction("feature.rebase"), nil
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
		return m, m.primarySelectedFeatureActionCmd(m.selectedPrimaryFeatureActionKind(), m.selectedFeature)
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
	w := m.width
	if w < 60 {
		w = 60
	}
	var b strings.Builder
	b.WriteString(TitleStyle.Render(" Agentico"))
	b.WriteString("\n")
	b.WriteString(MutedStyle.Render(strings.Repeat("-", min(w, 100))))
	b.WriteString("\n")
	b.WriteString(m.renderAPIRuntimeLine())
	if workspace := m.renderAPIWorkspaceSummary(); workspace != "" {
		b.WriteString("\n")
		b.WriteString(workspace)
	}
	b.WriteString("\n\n")
	b.WriteString(m.renderAPIFeatureList())
	if m.recoveryPanel != nil {
		b.WriteString("\n")
		b.WriteString(m.renderAPIRecovery())
	}
	if m.snapshot.Detail != nil {
		b.WriteString(m.renderAPIFeatureDetail())
	}
	if m.snapshot.LivePreview != nil {
		b.WriteString("\n")
		b.WriteString(m.renderAPILivePreview())
	}
	if m.snapshot.Transcript != nil {
		b.WriteString("\n")
		b.WriteString(m.renderAPITranscript())
	}
	if m.snapshot.Content != nil {
		b.WriteString("\n")
		b.WriteString(m.renderAPIContent())
	}
	if len(m.snapshot.Sessions) > 0 {
		b.WriteString("\n")
		b.WriteString(m.renderAPISessions())
	}
	if len(m.snapshot.Operations) > 0 {
		b.WriteString("\n")
		b.WriteString(m.renderAPIOperations())
	}
	if m.statusMessage != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(colorError).Render(m.statusMessage))
		b.WriteString("\n")
	}
	if m.configEditor != nil {
		b.WriteString("\n")
		b.WriteString(m.configEditor.render(min(w-4, 96)))
		b.WriteString("\n")
	}
	if m.runtimeConfigEditor != nil {
		b.WriteString("\n")
		b.WriteString(m.runtimeConfigEditor.render(min(w-4, 96)))
		b.WriteString("\n")
	}
	if m.createPrompt != nil {
		b.WriteString("\n")
		b.WriteString(m.renderAPICreateFeaturePrompt(min(w-4, 96)))
		b.WriteString("\n")
	}
	if m.reviewComments != nil {
		b.WriteString("\n")
		b.WriteString(m.renderAPIReviewCommentsPanel(min(w-4, 96)))
		b.WriteString("\n")
	}
	if m.refactorPrompt != nil {
		b.WriteString("\n")
		b.WriteString(m.renderAPIRefactorPrompt(min(w-4, 96)))
		b.WriteString("\n")
	}
	if m.refactorPipeline != nil {
		b.WriteString("\n")
		b.WriteString(m.renderAPIRefactorPipeline(min(w-4, 96)))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(KeyHelpStyle.Render(" [n] New   [up/down] Select   [enter] Start/Resume   [v] Attach/Live   [l] Log   [[ / ]] Artifact   [i] Input   [a] Permission   [u] AskUser   [h] Chat/Help   [w] Workspace   [e] Config   [Shift+E] Runtime   [g] Review   [t] Tweak   [Shift+T] Finish Tweak   [b] Rebase   [Shift+F] Refactor   [ctrl+f] Restart refactor   [r] Restart   [ctrl+r] Rewind   [p] Publish   [Shift+M] Merge   [Shift+D] Done   [Shift+R] Retry   [s] Stop   [c] Clean   [d] Delete   [q] Quit"))
	if m.quitOwnedServerPrompt {
		b.WriteString("\n\n")
		b.WriteString(panelStyle(true).
			Width(58).
			BorderForeground(colorWarning).
			Render("Stop the server started for this TUI session?\n\n[y] Stop server and quit   [n] Leave running   [esc] Cancel"))
	}
	if m.actionConfirmActive {
		b.WriteString("\n\n")
		b.WriteString(m.renderFeatureActionConfirm())
	}
	if m.tweakReviewModalActive {
		b.WriteString("\n\n")
		b.WriteString(m.renderTweakReviewModal())
	}
	if m.needInputPromptActive {
		b.WriteString("\n\n")
		b.WriteString(m.renderNeedInputPrompt())
	}
	if m.permissionPromptActive {
		b.WriteString("\n\n")
		b.WriteString(m.renderPermissionPrompt())
	}
	if m.helpPromptActive {
		b.WriteString("\n\n")
		b.WriteString(m.renderHelpPrompt())
	}
	if m.askUserPromptActive {
		b.WriteString("\n\n")
		b.WriteString(m.renderAskUserPrompt())
	}
	return tea.NewView(b.String())
}

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
	out.Operations = append([]APIOperationPresentation(nil), out.Operations...)
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
	return m.createPrompt != nil
}

func (m *APIAppModel) ApplyRefreshSnapshot(snapshot server.RefreshSnapshot) {
	selected := m.selectedFeature
	if snapshot.Features != nil {
		m.featureList = *snapshot.Features
	}
	if snapshot.RuntimeConfig != nil {
		m.runtimeConfig = *snapshot.RuntimeConfig
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
	if snapshot.Operations != nil {
		m.operations = *snapshot.Operations
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

func (m *APIAppModel) upsertAcceptedOperation(kind, featureID string, accepted server.OperationAcceptedResponse) {
	if accepted.OperationID == "" {
		return
	}
	status := accepted.Status
	if status == "" {
		status = server.OperationStatusQueued
	}
	dto := server.OperationDTO{
		ID:     accepted.OperationID,
		Kind:   kind,
		Target: server.OperationTarget{FeatureID: featureID},
		Status: status,
	}
	for i := range m.operations.Operations {
		if m.operations.Operations[i].ID == accepted.OperationID {
			m.operations.Operations[i] = dto
			return
		}
	}
	m.operations.Operations = append([]server.OperationDTO{dto}, m.operations.Operations...)
	if len(m.operations.Operations) > 20 {
		m.operations.Operations = m.operations.Operations[:20]
	}
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
		Operations:  apiOperationPresentations(m.operations),
	}
	m.selectedFeature = selected
}

func newAPIRecoveryPanel(snapshot server.RecoverySnapshotResponse) *apiRecoveryPanel {
	actions := make(map[string]string, len(snapshot.Items))
	for _, item := range snapshot.Items {
		action := item.DefaultAction
		if action == "" {
			action = "skip"
		}
		actions[item.Key] = action
	}
	return &apiRecoveryPanel{
		snapshotID: snapshot.SnapshotID,
		items:      append([]server.RecoveryItemDTO(nil), snapshot.Items...),
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
		b.WriteString(panelStyle(i == m.recoveryPanel.cursor).Render(content))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(KeyHelpStyle.Render(" [r] Resume   [k] Kill   [s] Skip   [enter] Continue"))
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
	if m.selectedFeature == "" {
		return m
	}
	if !m.selectedActionReady(kind) {
		m.statusMessage = kind + " is unavailable"
		return m
	}
	m.actionConfirmActive = true
	m.actionConfirmKind = kind
	m.actionConfirmFeatureID = m.selectedFeature
	m.actionConfirmFeatureName = m.selectedFeatureName()
	return m
}

func (m *APIAppModel) clearActionConfirm() {
	m.actionConfirmActive = false
	m.actionConfirmKind = ""
	m.actionConfirmFeatureID = ""
	m.actionConfirmFeatureName = ""
}

func (m APIAppModel) openTweakReviewModal() APIAppModel {
	if m.selectedFeature == "" {
		return m
	}
	if !m.selectedActionReady("feature.tweak.finish") {
		m.statusMessage = "feature.tweak.finish is unavailable"
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
	m.createPrompt = nil
}

func (m APIAppModel) openCreateFeaturePrompt(focus int) APIAppModel {
	prompt := newAPICreateFeaturePrompt(m.runtimeConfig)
	if focus >= prompt.focusCount(len(m.runtimeConfig.Repos)) {
		focus = 0
	}
	prompt.focus = focus
	m.createPrompt = prompt
	m.configEditor = nil
	m.runtimeConfigEditor = nil
	m.statusMessage = ""
	return m
}

func newAPICreateFeaturePrompt(runtime server.RuntimeConfigResponse) *apiCreateFeaturePrompt {
	selected := make(map[string]bool, len(runtime.Repos))
	for _, repo := range runtime.Repos {
		if repo.Name != "" {
			selected[repo.Name] = true
		}
	}
	return &apiCreateFeaturePrompt{selectedRepos: selected}
}

func (p apiCreateFeaturePrompt) focusCount(repoCount int) int {
	if repoCount > 0 {
		return 3
	}
	return 2
}

func (p apiCreateFeaturePrompt) selectedRepoNames(repos []server.ConfigRepoDTO) []string {
	selected := make([]string, 0, len(repos))
	for _, repo := range repos {
		if repo.Name != "" && p.selectedRepos[repo.Name] {
			selected = append(selected, repo.Name)
		}
	}
	return selected
}

func (p *apiCreateFeaturePrompt) normalizeRepoCursor(repoCount int) {
	if repoCount == 0 {
		p.repoCursor = 0
		return
	}
	if p.repoCursor < 0 {
		p.repoCursor = 0
	}
	if p.repoCursor >= repoCount {
		p.repoCursor = repoCount - 1
	}
}

func (m APIAppModel) openNeedInputPrompt() APIAppModel {
	if m.selectedFeature == "" {
		m.statusMessage = "No feature selected"
		return m
	}
	if _, ok := m.selectedNeedInputGate(m.selectedFeature); !ok {
		m.statusMessage = "No need-user-input gate for selected feature"
		return m
	}
	m.needInputPromptActive = true
	m.needInputFeatureID = m.selectedFeature
	m.needInputFeatureName = m.selectedFeatureName()
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
	m.statusMessage = ""
	return m
}

func (m APIAppModel) openRuntimeConfigEditor() APIAppModel {
	m.runtimeConfigEditor = newAPIRuntimeConfigEditor(m.runtimeConfig.Defaults, apiPhaseModelCatalog(m.catalog))
	m.configEditor = nil
	m.statusMessage = ""
	return m
}

func (m APIAppModel) cycleSelectedArtifact(delta int) (APIAppModel, tea.Cmd) {
	content, ok := m.selectedContentSnapshot()
	if !ok {
		m.statusMessage = "No run content for selected feature"
		return m, nil
	}
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
		m.statusMessage = "feature.review_comments is unavailable"
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
		m.statusMessage = kind + " is unavailable"
		return m
	}
	repo, pipelines, ok := m.selectedRefactorDefaults(kind)
	if !ok {
		m.statusMessage = "refactor action is unavailable"
		return m
	}
	m.refactorPrompt = &apiRefactorPrompt{
		featureID:   m.selectedFeature,
		featureName: m.selectedFeatureName(),
		repo:        repo,
		pipelines:   pipelines,
		restart:     restart,
	}
	m.refactorPipeline = nil
	m.statusMessage = ""
	return m
}

func (m APIAppModel) selectedReviewCommentsDefaults() (string, string, []string, bool) {
	action, ok := m.selectedRawAction("feature.review_comments")
	if !ok || !action.Enabled {
		return "", "", nil, false
	}
	modes := apiReviewCommentModes(nil)
	repoRequired := false
	if ok {
		for _, input := range action.RequiredInputs {
			switch input.Name {
			case "repo":
				repoRequired = input.Required
			case "mode":
				modes = apiReviewCommentModes(input.Options)
			}
		}
	}
	repo := m.selectedReviewCommentsRepo()
	if repo == "" && repoRequired {
		return "", "", modes, false
	}
	return repo, modes[0], modes, true
}

func (m APIAppModel) selectedRefactorDefaults(kind string) (string, []feature.PipelineProfile, bool) {
	action, ok := m.selectedRawAction(kind)
	if !ok || !action.Enabled {
		return "", nil, false
	}
	pipelines := apiRefactorPipelines(nil)
	for _, input := range action.RequiredInputs {
		if input.Name == "pipeline" {
			pipelines = apiRefactorPipelines(input.Options)
			break
		}
	}
	return m.selectedRefactorRepo(), pipelines, true
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
		return m.selectedFeature != ""
	case "feature.resume":
		for _, f := range m.snapshot.Features {
			if f.ID == m.selectedFeature {
				return apiFeatureCanResume(f.Status)
			}
		}
	case "feature.publish":
		return m.selectedFeature != ""
	case "feature.merge":
		return m.selectedFeature != ""
	case "feature.restart":
		return m.selectedFeature != ""
	case "feature.retry":
		return m.selectedFeature != ""
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
		b.WriteString(fmt.Sprintf("  Log %s", content.Log.ID))
		if content.Log.Size > 0 {
			b.WriteString(fmt.Sprintf("  bytes %d-%d of %d", content.Log.Offset, min(content.Log.Offset+content.Log.Limit, content.Log.Size), content.Log.Size))
		}
		b.WriteByte('\n')
		appendIndentedText(&b, content.Log.Text)
	}
	if content.Artifact != nil {
		b.WriteString(fmt.Sprintf("  Artifact %s", content.Artifact.ID))
		if content.Artifact.Phase != "" {
			b.WriteString("  " + content.Artifact.Phase)
		}
		if content.Artifact.Size > 0 {
			b.WriteString(fmt.Sprintf("  bytes %d-%d of %d", content.Artifact.Offset, min(content.Artifact.Offset+content.Artifact.Limit, content.Artifact.Size), content.Artifact.Size))
		}
		b.WriteByte('\n')
		appendIndentedText(&b, content.Artifact.Text)
	}
	return b.String()
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

func (m APIAppModel) renderAPIOperations() string {
	var b strings.Builder
	b.WriteString("Operations\n")
	for _, op := range m.snapshot.Operations {
		label := op.Kind
		if label == "" {
			label = "operation"
		}
		b.WriteString(fmt.Sprintf("  %s  %s  %s", op.ID, label, op.Status))
		if op.FeatureID != "" {
			b.WriteString("  " + op.FeatureID)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (m APIAppModel) renderAPICreateFeaturePrompt(width int) string {
	prompt := m.createPrompt
	if prompt == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("New Feature\n\n")
	b.WriteString(apiPromptFieldLine("Name", prompt.nameDraft, prompt.focus == 0))
	b.WriteString(apiPromptFieldLine("Description", prompt.descriptionDraft, prompt.focus == 1))
	if len(m.runtimeConfig.Repos) > 0 {
		b.WriteString("\nRepos\n")
		for i, repo := range m.runtimeConfig.Repos {
			if repo.Name == "" {
				continue
			}
			cursor := "  "
			if prompt.focus == 2 && i == prompt.repoCursor {
				cursor = "> "
			}
			mark := " "
			if prompt.selectedRepos[repo.Name] {
				mark = "x"
			}
			b.WriteString(fmt.Sprintf("%s[%s] %s\n", cursor, mark, repo.Name))
		}
	}
	b.WriteString("\n")
	b.WriteString(KeyHelpStyle.Render("[tab] Field   [space] Toggle repo   [enter] Create   [esc] Cancel"))
	return panelStyle(true).Width(width).Render(b.String())
}

func apiPromptFieldLine(label, value string, focused bool) string {
	cursor := "  "
	if focused {
		cursor = "> "
	}
	if value == "" {
		value = MutedStyle.Render("(empty)")
	}
	return fmt.Sprintf("%s%s: %s\n", cursor, label, value)
}

func (m APIAppModel) renderFeatureActionConfirm() string {
	title := "Confirm " + m.actionConfirmKind
	name := m.actionConfirmFeatureName
	if name == "" {
		name = m.actionConfirmFeatureID
	}
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n\n")
	b.WriteString("  " + name + "\n\n")
	switch m.actionConfirmKind {
	case "feature.publish":
		b.WriteString(WarningStyle.Render("  This will publish the selected feature."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Review the server operation result before continuing."))
	case "feature.merge":
		b.WriteString(WarningStyle.Render("  This will merge the selected feature to the base branch."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Review the server operation result before continuing."))
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
	case "feature.rewind":
		b.WriteString(WarningStyle.Render("  This will rewind the selected feature to its current phase."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Later phase work may be replaced by the server action."))
	case "feature.rebase":
		b.WriteString(WarningStyle.Render("  This will start a rebase cycle for the selected feature."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Conflict handling and push results will be reported by the server operation."))
	case "feature.tweak.start":
		b.WriteString(WarningStyle.Render("  This will start an interactive tweak session for the selected feature."))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  Finish and review decisions will be handled through server operations."))
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
	b.WriteString("Review comments\n\n")
	b.WriteString("  Feature: " + name + "\n")
	b.WriteString("  Repo: " + panel.repo + "\n")
	b.WriteString("  Mode: " + panel.mode)
	if len(panel.modes) > 1 {
		b.WriteString(" (" + strings.Join(panel.modes, ", ") + ")")
	}
	b.WriteString("\n\n")
	if len(panel.comments) == 0 {
		b.WriteString("  No pending review comments on this PR.\n")
	} else {
		for i, comment := range panel.comments {
			b.WriteString(fmt.Sprintf("  Comment %d/%d", i+1, len(panel.comments)))
			if comment.UserLogin != "" {
				b.WriteString(" @" + comment.UserLogin)
			}
			b.WriteByte('\n')
			location := apiReviewCommentLocation(comment)
			if location != "" {
				b.WriteString("  " + location + "\n")
			}
			for _, line := range strings.Split(comment.Body, "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				b.WriteString("  " + line + "\n")
			}
			if i < len(panel.comments)-1 {
				b.WriteByte('\n')
			}
		}
	}
	b.WriteByte('\n')
	b.WriteString(KeyHelpStyle.Render(" [tab] Mode   [enter]/[a] Start   [esc]/[q] Close"))
	return panelStyle(true).
		Width(width).
		BorderForeground(colorBrand).
		Render(b.String())
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
	panel := m.refactorPipeline
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
	b.WriteString("Pipeline\n\n")
	b.WriteString("  Feature: " + name + "\n")
	if panel.repo != "" {
		b.WriteString("  Repo: " + panel.repo + "\n")
	}
	b.WriteString("\n")
	for i, option := range panel.pipelines {
		cursor := "  "
		if i == panel.cursor {
			cursor = "> "
		}
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, option))
	}
	b.WriteString("\n")
	b.WriteString(KeyHelpStyle.Render(" [left/right] Select   [enter] Start   [esc] Cancel"))
	return panelStyle(true).
		Width(width).
		BorderForeground(colorBrand).
		Render(b.String())
}

func (m APIAppModel) renderNeedInputPrompt() string {
	name := m.needInputFeatureName
	if name == "" {
		name = m.needInputFeatureID
	}
	var b strings.Builder
	b.WriteString("Need user input\n\n")
	b.WriteString("  " + name + "\n\n")
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
	if question := strings.TrimSpace(m.askUserQuestion); question != "" {
		b.WriteString("  " + firstLine(question) + "\n")
	}
	b.WriteString("\n")
	b.WriteString("  Answer: " + m.askUserAnswerDraft + "\n")
	b.WriteString("\n")
	b.WriteString(KeyHelpStyle.Render(" [enter] Send   [esc] Cancel"))
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

func (m APIAppModel) createFeatureCmd(prompt apiCreateFeaturePrompt) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		req := server.CreateFeatureRequest{
			Name:        strings.TrimSpace(prompt.nameDraft),
			Description: strings.TrimSpace(prompt.descriptionDraft),
			Repos:       prompt.selectedRepoNames(m.runtimeConfig.Repos),
			Models:      m.runtimeConfig.Defaults,
		}
		accepted, err := m.client.CreateFeature(ctx, req)
		return apiOperationAcceptedMsg{
			kind:     "feature.create",
			accepted: accepted,
			err:      err,
		}
	}
}

func (m APIAppModel) primarySelectedFeatureActionCmd(kind, featureID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		var (
			accepted server.OperationAcceptedResponse
			err      error
		)
		switch kind {
		case "feature.resume":
			accepted, err = m.client.ResumeFeature(ctx, featureID)
		case "feature.start":
			accepted, err = m.client.StartFeature(ctx, featureID)
		default:
			err = fmt.Errorf("unsupported primary feature action %s", kind)
		}
		return apiOperationAcceptedMsg{
			kind:      kind,
			featureID: featureID,
			accepted:  accepted,
			err:       err,
		}
	}
}

func (m APIAppModel) selectedFeatureActionCmd(kind, featureID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		var (
			accepted server.OperationAcceptedResponse
			err      error
		)
		switch kind {
		case "feature.publish":
			accepted, err = m.client.PublishFeature(ctx, featureID, server.PublishFeatureRequest{})
		case "feature.merge":
			accepted, err = m.client.MergeFeature(ctx, featureID)
		case "feature.restart":
			accepted, err = m.client.RestartFeature(ctx, featureID, server.RestartFeatureRequest{})
		case "feature.retry":
			accepted, err = m.client.RetryFeature(ctx, featureID)
		case "feature.mark-done":
			accepted, err = m.client.MarkDone(ctx, featureID)
		case "feature.rebase":
			accepted, err = m.client.StartRebase(ctx, featureID, server.RebaseActionRequest{Repo: m.selectedRebaseRepo(featureID)})
		case "feature.cleanup":
			accepted, err = m.client.CleanupFeature(ctx, featureID, server.CleanupActionRequest{Target: "worktrees"})
		case "feature.rewind":
			targetPhase := m.selectedFeatureCurrentPhase(featureID)
			if targetPhase == "" {
				err = errors.New("selected feature has no rewind target phase")
				break
			}
			accepted, err = m.client.RewindFeature(ctx, featureID, server.RewindFeatureRequest{TargetPhase: targetPhase})
		case "feature.tweak.start":
			accepted, err = m.client.StartTweak(ctx, featureID, server.TweakActionRequest{})
		case "feature.stop":
			accepted, err = m.client.StopFeature(ctx, featureID)
		case "feature.delete":
			accepted, err = m.client.DeleteFeature(ctx, featureID)
		default:
			err = fmt.Errorf("unsupported feature action %s", kind)
		}
		return apiOperationAcceptedMsg{
			kind:      kind,
			featureID: featureID,
			accepted:  accepted,
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
		accepted, err := m.client.FinishTweak(ctx, featureID, server.TweakFinishRequest{
			Decision:   decision,
			HadChanges: hadChanges,
		})
		return apiOperationAcceptedMsg{
			kind:      "feature.tweak.finish",
			featureID: featureID,
			accepted:  accepted,
			err:       err,
		}
	}
}

func (m APIAppModel) executeRecoveryCmd(panel apiRecoveryPanel) tea.Cmd {
	return func() tea.Msg {
		accepted, err := m.client.ExecuteRecovery(context.Background(), server.RecoveryActionRequest{
			SnapshotID: panel.snapshotID,
			Actions:    copyStringMapValues(panel.actions),
		})
		return apiOperationAcceptedMsg{kind: "recovery.execute", accepted: accepted, err: err}
	}
}

func (m APIAppModel) handleAPICreateFeaturePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	prompt := m.createPrompt
	if prompt == nil {
		return m, nil
	}
	repoCount := len(m.runtimeConfig.Repos)
	switch msg.Code {
	case tea.KeyEscape:
		m.clearCreatePrompt()
		m.statusMessage = ""
		return m, nil
	case tea.KeyTab:
		prompt.focus = (prompt.focus + 1) % prompt.focusCount(repoCount)
		prompt.normalizeRepoCursor(repoCount)
		return m, nil
	case tea.KeyEnter:
		if strings.TrimSpace(prompt.nameDraft) == "" {
			m.statusMessage = "Feature name cannot be empty"
			return m, nil
		}
		return m, m.createFeatureCmd(*prompt)
	case tea.KeyBackspace:
		switch prompt.focus {
		case 0:
			prompt.nameDraft = trimLastRuneAPI(prompt.nameDraft)
		case 1:
			prompt.descriptionDraft = trimLastRuneAPI(prompt.descriptionDraft)
		}
		return m, nil
	case tea.KeyUp:
		if prompt.focus == 2 {
			prompt.repoCursor--
			prompt.normalizeRepoCursor(repoCount)
		}
		return m, nil
	case tea.KeyDown:
		if prompt.focus == 2 {
			prompt.repoCursor++
			prompt.normalizeRepoCursor(repoCount)
		}
		return m, nil
	}
	if prompt.focus == 2 && msg.Text == " " {
		if repoCount > 0 {
			prompt.normalizeRepoCursor(repoCount)
			repo := m.runtimeConfig.Repos[prompt.repoCursor]
			if repo.Name != "" {
				prompt.selectedRepos[repo.Name] = !prompt.selectedRepos[repo.Name]
			}
		}
		return m, nil
	}
	if msg.Text != "" && !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
		switch prompt.focus {
		case 0:
			prompt.nameDraft += msg.Text
		case 1:
			prompt.descriptionDraft += msg.Text
		}
		m.statusMessage = ""
	}
	return m, nil
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
	case tea.KeyTab:
		next := *panel
		next.mode = apiNextReviewCommentMode(panel.mode, panel.modes)
		m.reviewComments = &next
		return m, nil
	case tea.KeyEnter:
		return m, m.startReviewCommentsCmd(*panel)
	}
	switch strings.ToLower(msg.Text) {
	case "a":
		return m, m.startReviewCommentsCmd(*panel)
	case "m":
		next := *panel
		next.mode = apiNextReviewCommentMode(panel.mode, panel.modes)
		m.reviewComments = &next
		return m, nil
	case "q":
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
		value := strings.TrimSpace(prompt.draft)
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
	case tea.KeyBackspace:
		runes := []rune(prompt.draft)
		if len(runes) > 0 {
			prompt.draft = string(runes[:len(runes)-1])
		}
		return m, nil
	}
	if msg.Text != "" && !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
		prompt.draft += msg.Text
		m.statusMessage = ""
	}
	return m, nil
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
	switch msg.Code {
	case tea.KeyEscape:
		m.configEditor = nil
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
		return m, m.saveFeatureConfigCmd(*editor)
	}
	switch strings.ToLower(msg.Text) {
	case "k":
		editor.move(-1)
	case "j":
		editor.move(1)
	}
	return m, nil
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
	switch msg.Code {
	case tea.KeyEscape:
		m.clearAskUserPrompt()
		return m, nil
	case tea.KeyEnter:
		answer := strings.TrimSpace(m.askUserAnswerDraft)
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
	if msg.Text != "" && !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
		m.askUserAnswerDraft += msg.Text
		m.statusMessage = ""
	}
	return m, nil
}

func (m APIAppModel) saveFeatureConfigCmd(editor apiFeatureConfigEditor) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		req := server.FeatureConfigMutationFromDTO(editor.draft)
		accepted, err := m.client.UpdateFeatureConfig(ctx, editor.featureID, req)
		return apiOperationAcceptedMsg{
			kind:      "feature.config.update",
			featureID: editor.featureID,
			accepted:  accepted,
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
		accepted, err := m.client.UpdateRuntimeConfig(ctx, server.RuntimeConfigMutationRequest{
			Defaults: config.DefaultsConfig{Models: editor.draft},
		})
		return apiOperationAcceptedMsg{
			kind:     "runtime.config.update",
			accepted: accepted,
			err:      err,
		}
	}
}

func (m APIAppModel) startReviewCommentsCmd(panel apiReviewCommentsPanel) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.eventCtx != nil {
			ctx = m.eventCtx
		}
		accepted, err := m.client.StartReviewComments(ctx, panel.featureID, server.ReviewCommentsActionRequest{
			Repo: panel.repo,
			Mode: panel.mode,
		})
		return apiOperationAcceptedMsg{
			kind:      "feature.review_comments",
			featureID: panel.featureID,
			accepted:  accepted,
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
		var (
			accepted server.OperationAcceptedResponse
			err      error
		)
		if panel.restart {
			kind = "feature.refactor.restart"
			accepted, err = m.client.RestartRefactor(ctx, panel.featureID, req)
		} else {
			accepted, err = m.client.StartRefactor(ctx, panel.featureID, req)
		}
		return apiOperationAcceptedMsg{
			kind:      kind,
			featureID: panel.featureID,
			accepted:  accepted,
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
		accepted, err := m.client.NeedUserInputDecision(ctx, featureID, server.NeedUserInputDecisionRequest{Decision: decision})
		return apiOperationAcceptedMsg{
			kind:      "feature.need_user_input.decision",
			featureID: featureID,
			accepted:  accepted,
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
		accepted, err := m.client.ReviewDecision(ctx, featureID, req)
		return apiOperationAcceptedMsg{
			kind:      "feature.review_decision",
			featureID: featureID,
			accepted:  accepted,
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
		accepted, err := m.client.AnswerPermission(ctx, server.PermissionAnswerRequest{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Decision:  decision,
		})
		return apiOperationAcceptedMsg{
			kind:      "permission.answer",
			featureID: req.FeatureID,
			accepted:  accepted,
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
		accepted, err := m.client.SendHelp(ctx, server.HelpAnswerRequest{
			FeatureID: featureID,
			Message:   answer,
		})
		return apiOperationAcceptedMsg{
			kind:      "help.send",
			featureID: featureID,
			accepted:  accepted,
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
		accepted, err := m.client.AnswerAskUser(ctx, server.AskUserAnswerRequest{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Answers:   map[string]string{question: answer},
		})
		return apiOperationAcceptedMsg{
			kind:      "ask_user.answer",
			featureID: req.FeatureID,
			accepted:  accepted,
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

func (m APIAppModel) stopOwnedServerCmd() tea.Cmd {
	return func() tea.Msg {
		if m.stopOwnedServer == nil {
			return apiOwnedServerStoppedMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return apiOwnedServerStoppedMsg{err: m.stopOwnedServer(ctx)}
	}
}

type apiFeatureConfigEditor struct {
	featureID   string
	featureName string
	draft       server.FeatureConfigDTO
	catalog     PhaseModelCatalog
	cursor      int
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

func newAPIFeatureConfigEditor(featureID, featureName string, response server.FeatureConfigResponse, catalog PhaseModelCatalog) *apiFeatureConfigEditor {
	if response.FeatureID != "" {
		featureID = response.FeatureID
	}
	return &apiFeatureConfigEditor{
		featureID:   featureID,
		featureName: featureName,
		draft:       response.Current,
		catalog:     catalog,
	}
}

func (e *apiFeatureConfigEditor) move(delta int) {
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

func (e *apiFeatureConfigEditor) cycleModel(delta int) {
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

func (e apiFeatureConfigEditor) render(width int) string {
	if width < 52 {
		width = 52
	}
	name := e.featureName
	if name == "" {
		name = e.featureID
	}
	var b strings.Builder
	b.WriteString("Feature config\n\n")
	b.WriteString("  " + name + "\n")
	if e.draft.Pipeline != "" {
		b.WriteString("  Pipeline: " + e.draft.Pipeline + "\n")
	}
	if e.draft.Inquireness != "" {
		b.WriteString("  Inquireness: " + e.draft.Inquireness + "\n")
	}
	b.WriteString("\n")
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

func (e apiFeatureConfigEditor) fields() []string {
	if len(e.catalog.Fields) > 0 {
		return e.catalog.Fields
	}
	return phaseCatalogFields
}

func (e apiFeatureConfigEditor) currentField() string {
	fields := e.fields()
	if e.cursor < 0 || e.cursor >= len(fields) {
		return ""
	}
	return fields[e.cursor]
}

func (e apiFeatureConfigEditor) modelValue(field string) string {
	switch field {
	case "Research":
		return e.draft.Models.Research
	case "Planning":
		return e.draft.Models.Planning
	case "Implementation":
		return e.draft.Models.Implementation
	case "Review":
		return e.draft.Models.Review
	case "KB Build":
		return e.draft.Models.KBBuild
	default:
		return ""
	}
}

func (e *apiFeatureConfigEditor) setModelValue(field, value string) {
	switch field {
	case "Research":
		e.draft.Models.Research = value
	case "Planning":
		e.draft.Models.Planning = value
	case "Implementation":
		e.draft.Models.Implementation = value
	case "Review":
		e.draft.Models.Review = value
	case "KB Build":
		e.draft.Models.KBBuild = value
	}
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

func trimLastRuneAPI(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return value
	}
	return string(runes[:len(runes)-1])
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

func apiOperationPresentations(resp server.OperationSnapshotResponse) []APIOperationPresentation {
	out := make([]APIOperationPresentation, 0, len(resp.Operations))
	for _, op := range resp.Operations {
		out = append(out, APIOperationPresentation{
			ID:        op.ID,
			Kind:      op.Kind,
			FeatureID: op.Target.FeatureID,
			Status:    string(op.Status),
		})
	}
	return out
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
