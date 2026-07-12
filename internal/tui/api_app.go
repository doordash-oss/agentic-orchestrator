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
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
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
	GeneratePublishDescription(context.Context, string, server.PublishDescriptionRequest) (server.PublishDescriptionResponse, error)
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
	StartChat(context.Context, server.ChatStartRequest) (server.ChatStartResponse, error)
	AnswerAskUser(context.Context, server.AskUserAnswerRequest) (server.AskUserAnswerResponse, error)
	Shutdown(context.Context) (server.ShutdownResponse, error)
	SubscribeEvents(context.Context, server.EventSubscriptionOptions) (<-chan server.RefreshSignal, <-chan error)
	FetchRefreshSnapshot(context.Context, server.RefreshSignal) (server.RefreshSnapshot, error)
	SubscribeSessionOutput(context.Context, string, server.SessionOutputStreamOptions) (<-chan server.SessionOutputRecord, <-chan error)
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

const (
	statusMsgNoFeatureSelected       = "No feature selected"
	statusMsgNoActiveSessions        = "No active sessions to watch."
	statusMsgLoadingReviewArtifact   = "Loading review artifact"
	statusMsgReviewArtifactLoading   = "Review artifact is still loading"
	statusMsgUpdatingWorkspaceConfig = "Updating workspace configuration..."
)

// phaseLabelKBBuild is the display label for the knowledge-base build phase,
// distinct from feature.PhaseKnowledgeBase.String() ("Knowledge Base").
const phaseLabelKBBuild = "KB Build"

// mutationKindAskUserAnswer is the apiMutationResultMsg.kind for AskUser
// answer submissions.
const mutationKindAskUserAnswer = "ask_user.answer"

// controlRequestSubtypeCanUseTool is the ControlRequest.Subtype reported for
// permission ("can I use this tool?") requests.
const controlRequestSubtypeCanUseTool = "can_use_tool"

// reviewCommentsModeAuto and reviewCommentsModeAddressAll are the
// review-comments action modes, mirroring server.reviewCommentsModeAuto and
// its "address_all" sibling.
const (
	reviewCommentsModeAuto       = "auto"
	reviewCommentsModeAddressAll = "address_all"
)

// logTabSession and logTabPhase are log-tab identifiers in
// apiSelectableLogIDs, mirroring the server's per-run logs (session log,
// logs/phase.log).
const (
	logTabSession = "session"
	logTabPhase   = "phase"
)

// resourceTypeSession is the server.ResourceDTO.Type value for a session
// resource, mirroring the server's own (unexported) resourceTypeSession.
const resourceTypeSession = "session"

// mutationKindFeatureX are apiMutationResultMsg.kind values for feature
// lifecycle mutations, mirroring the server's action/mutation route kinds.
const (
	mutationKindFeaturePublish               = "feature.publish"
	mutationKindFeatureCreate                = "feature.create"
	mutationKindFeatureConfigUpdate          = "feature.config.update"
	mutationKindRecoveryExecute              = "recovery.execute"
	mutationKindRuntimeConfigUpdate          = "runtime.config.update"
	mutationKindFeatureNeedUserInputDecision = "feature.need_user_input.decision"
	mutationKindPermissionAnswer             = "permission.answer"
	mutationKindHelpSend                     = "help.send"
	mutationKindFeatureReviewComments        = "feature.review_comments"
	mutationKindFeatureDelete                = "feature.delete"
	mutationKindFeatureRewind                = "feature.rewind"
	mutationKindFeatureResume                = "feature.resume"
	mutationKindFeatureRetry                 = "feature.retry"
	mutationKindFeatureRefactorStart         = "feature.refactor.start"
	mutationKindFeatureRefactorRestart       = "feature.refactor.restart"
	mutationKindFeatureTweakFinish           = "feature.tweak.finish"
	mutationKindFeatureStart                 = "feature.start"
	mutationKindFeatureMerge                 = "feature.merge"
	mutationKindFeatureRestart               = "feature.restart"
	mutationKindFeatureMarkDone              = "feature.mark-done"
	mutationKindFeatureRebase                = "feature.rebase"
	mutationKindFeatureCleanup               = "feature.cleanup"
	mutationKindFeatureTweakStart            = "feature.tweak.start"
	mutationKindFeatureStop                  = "feature.stop"
)

// actionIDX are the short action-verb IDs server.ActionDTO.ID reports,
// matched against mutationKind* by apiActionMatchesMutationKind.
const (
	actionIDStart          = "start"
	actionIDRetry          = "retry"
	actionIDPublish        = "publish"
	actionIDMerge          = "merge"
	actionIDRestart        = "restart"
	actionIDRebase         = "rebase"
	actionIDMarkDone       = "mark-done"
	actionIDCleanup        = "cleanup"
	actionIDReviewComments = "review-comments"
	actionIDTweak          = "tweak"
	actionIDRefactor       = "refactor"
	actionIDRewind         = "rewind"
	actionIDStop           = "stop"
	actionIDDelete         = "delete"
)

// actionInputNameX are server.ActionDTO.RequiredInputs[*].Name values.
const (
	actionInputNameRepo        = "repo"
	actionInputNameMode        = "mode"
	actionInputNamePipeline    = "pipeline"
	actionInputNameTargetPhase = "target_phase"
)

// actionStatusReady is the server.ActionDTO.Status value for an action that
// is currently available to invoke.
const actionStatusReady = "ready"

// sseEventKindSessionUpdated is the SSEEventDTO.Kind synthesized for locally
// generated session refresh signals, mirroring the server's own
// sseEventSessionUpdated.
const sseEventKindSessionUpdated = "session.updated"

// blockTypeText, blockTypeToolUse, roleUser and roleSystem mirror the
// server's own (unexported) TranscriptMessageDTO.Type / role constants of
// the same name for the equivalent llm.SDKMessage fields.
const (
	blockTypeText    = "text"
	blockTypeToolUse = "tool_use"
	roleUser         = "user"
	roleSystem       = "system"
)

// transcriptTypeX mirror the server's own (unexported) TranscriptMessageDTO
// system-row Type values of the same name.
const (
	transcriptTypeToolProgress     = "tool_progress"
	transcriptTypeStatus           = "status"
	transcriptTypeTaskStarted      = "task_started"
	transcriptTypeTaskProgress     = "task_progress"
	transcriptTypeTaskNotification = "task_notification"
	transcriptTypeResult           = "result"
)

// apiSessionStatusX are the normalized apiSessionStatus() wire tokens for
// session status values reported outside of TurnState.
const (
	apiSessionStatusWaitingPermission = "waiting_permission"
	apiSessionStatusDone              = "done"
	apiSessionStatusCompleted         = "completed"
	apiSessionStatusSuccess           = "success"
	apiSessionStatusError             = "error"
)

// recoveryActionKill and recoveryActionResume are RecoveryItemDTO
// AllowedActions/DefaultAction values, mirroring the server's own
// (unexported) recoveryActionKill.
const (
	recoveryActionKill   = "kill"
	recoveryActionResume = "resume"
	recoveryActionSkip   = "skip"
)

// prStatusPublished and prStatusFailed are publishRepoEntry.PRStatus values
// for a repo whose PR has been opened / whose publish attempt errored.
const (
	prStatusPublished = "published"
	prStatusFailed    = "failed"
)

// reviewModeRewind and reviewModeGate are apiReviewTarget's non-default
// ArtifactReviewModel review-mode tokens (the default is "plan").
const (
	reviewModeRewind = "rewind"
	reviewModeGate   = "gate"
)

// artifactIDDescriptionReview, artifactIDPrompt and artifactIDRoadmap are
// reviewArtifactIDs()/apiPublishPlanText() artifact IDs that have no
// corresponding feature.Phase (unlike the phase-named artifacts, which reuse
// feature.PhaseX.DirName()).
const (
	artifactIDDescriptionReview = "description-review"
	artifactIDPrompt            = "prompt"
	artifactIDRoadmap           = "roadmap"
)

// phaseAliasFinalReview is a hyphenated spelling accepted by
// apiRewindPhaseLabel alongside "finalreview" / "final_review".
const phaseAliasFinalReview = "final-review"

// resourceTypeLog is the server.ResourceDTO.Type value for a run's log
// stream, mirroring the server's own (unexported) resourceTypeSession
// naming.
const resourceTypeLog = "log"

// reviewDecisionProceed and reviewDecisionIterate are
// server.ReviewDecisionRequest.Decision values.
const (
	reviewDecisionProceed = "proceed"
	reviewDecisionIterate = "iterate"
)

// featureStatusTokenX are lowercase wire tokens accepted by
// apiFeatureStatus/apiFeatureCanStop for feature status values that have no
// dedicated feature.Status wire alias elsewhere in this file.
const (
	featureStatusTokenCreated  = "created"
	featureStatusTokenPlanning = "planning"
	featureStatusTokenRunning  = "running"
)

var apiSelectableLogIDs = []string{logTabSession, logTabPhase, "observe"}

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
	Phase          string
	Kind           string
	Label          string
	Provider       string
	Model          string
	Activity       string
	ContextPct     int
	CostUSD        float64
	Attention      []string
	TranscriptRows []server.TranscriptMessageDTO
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
	chat                       ChatModel
	chatReady                  bool
	chatOpen                   bool
	livePreviews               map[string]server.LivePreviewResponse
	transcripts                map[string]server.TranscriptResponse
	contents                   map[string]apiFeatureContentSnapshot
	recovery                   server.RecoverySnapshotResponse
	launchPolicy               server.LaunchPolicy
	snapshot                   APIAppSnapshot
	recoveryPanel              *apiRecoveryPanel
	selectedFeature            string
	selectedSection            string
	width                      int
	height                     int
	spinner                    spinner.Model
	ownedServer                bool
	waitForOwnedServerShutdown func(context.Context) error
	quitOwnedServerPrompt      bool
	ownedServerShutdownPending bool
	actionConfirmActive        bool
	actionConfirmKind          string
	actionConfirmFeatureID     string
	actionConfirmFeatureName   string
	actionConfirmArgs          apiFeatureActionArgs
	publish                    *PublishModel
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
	welcome                    *WelcomeModel
	welcomeSkipped             bool
	attach                     *AttachModel
	artifactReview             *ArtifactReviewModel
	wizard                     *WizardModel
	reviewComments             *apiReviewCommentsPanel
	refactorPrompt             *apiRefactorPrompt
	refactorPipeline           *apiRefactorPipelinePanel
	configEditor               *EditConfigModel
	workspaceManager           *WorkspaceManagerModel
	wizardRuntimeConfigPending bool
	helpOverlayActive          bool
	helpOverlay                HelpOverlayModel
	resumeAllConfirmActive     bool
	resumeAllFeatureIDs        []string
	focusPanel                 int
	rightPanelMode             dashboardRightPanelMode
	contentPanelActive         bool
	contentViewport            *reviewViewportModel
	diffReview                 *reviewViewportModel
	textPanelActive            bool
	textPanelTitle             string
	textPanelContent           string
	statusMessage              string
	eventCtx                   context.Context
	cancelEvents               context.CancelFunc
	signals                    <-chan server.RefreshSignal
	eventErrs                  <-chan error
	liveOutputCancel           context.CancelFunc
	liveOutputSessionID        string
	liveOutputRecords          <-chan server.SessionOutputRecord
	liveOutputErrs             <-chan error
	transcriptBackfills        map[string]bool
}

type apiRefreshSignalMsg struct {
	signal server.RefreshSignal
}

// apiSessionOutputLineMsg re-arms the live output listen loop (see
// listenLiveSessionOutputCmd) after one record was reconciled through
// applyTranscriptRow. The record itself has already been applied by the
// time this is delivered — this msg carries no payload beyond which
// session it's for.
type apiSessionOutputLineMsg struct {
	sessionID string
}

// apiSessionOutputDoneMsg signals that the live output feed for sessionID
// ended (channel closed or a stream error) and should be stopped.
type apiSessionOutputDoneMsg struct {
	sessionID string
}

type apiTranscriptBackfillMsg struct {
	sessionID  string
	before     int
	transcript server.TranscriptResponse
	err        error
}

type apiRefreshSnapshotMsg struct {
	snapshot server.RefreshSnapshot
	content  *apiFeatureContentSnapshot
	err      error
}

type apiAttachSessionsSnapshotMsg struct {
	featureID    string
	sessions     server.SessionListResponse
	err          error
	openIfClosed bool
}

type apiContentSelectionMsg struct {
	featureID string
	content   apiFeatureContentSnapshot
	status    string
	err       error
}

type apiTextPanelMsg struct {
	title   string
	content string
}

type apiDiffReviewMsg struct {
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

type apiReviewArtifactMsg struct {
	featureID string
	detail    server.FeatureDetailResponse
	artifact  server.ArtifactDTO
	err       error
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
	requestID string
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
	browser     reviewCommentsBrowserModel
}

type apiFeatureActionArgs struct {
	Repo            string
	Repos           []string
	Title           string
	Body            string
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
	featureID     string
	featureName   string
	repo          string
	draft         string
	input         SimpleTextarea
	pipelines     []feature.PipelineProfile
	restart       bool
	canPaste      bool
	imageTempDir  string
	attachTempDir string
	imageCounter  int
	images        []string
	attachments   []string
	attachNames   []string
}

type apiRefactorPipelinePanel struct {
	featureID   string
	featureName string
	repo        string
	prompt      string
	images      []string
	attachments []string
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
		transcriptBackfills:        map[string]bool{},
		width:                      100,
		height:                     30,
		spinner:                    newAPIAppSpinner(),
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
	} else if len(runtimeConfig.WorkspaceRoots) == 0 {
		welcome := NewWelcomeModel()
		app.welcome = &welcome
	}
	app.rebuildPresentation("")
	if app.selectedFeature != "" {
		if err := app.loadSelectedFeatureState(ctx, client); err != nil {
			return APIAppModel{}, err
		}
	}

	eventCtx, cancel := context.WithCancel(ctx)
	app.eventCtx = eventCtx
	app.cancelEvents = cancel
	app.signals, app.eventErrs = client.SubscribeEvents(eventCtx, opts.EventOptions)
	return app, nil
}

// loadSelectedFeatureState loads and stores the detail, live preview, transcript,
// and content snapshots for the currently selected feature.
func (app *APIAppModel) loadSelectedFeatureState(ctx context.Context, client APIClient) error {
	detail, err := client.FeatureDetail(ctx, app.selectedFeature)
	if err != nil {
		return fmt.Errorf("load selected feature detail snapshot: %w", err)
	}
	app.storeFeatureDetail(detail)
	preview, err := client.LivePreview(ctx, app.selectedFeature)
	if err != nil {
		return fmt.Errorf("load selected feature live preview snapshot: %w", err)
	}
	app.storeLivePreview(app.selectedFeature, preview)
	if sessionID := apiSelectedSessionID(preview); sessionID != "" {
		session, transcript, err := loadAPITranscriptTail(ctx, client, sessionID)
		if err != nil {
			return err
		}
		app.storeSessionDetail(session)
		app.storeTranscript(sessionID, transcript)
	}
	if content := loadAPISelectedContent(ctx, client, app.selectedFeature, detail, nil); content != nil {
		app.storeContent(*content)
	}
	app.rebuildPresentation(app.selectedFeature)
	return nil
}

func (m APIAppModel) Init() tea.Cmd {
	return tea.Batch(m.listenForAPIEvents(), m.spinner.Tick)
}

func (m APIAppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(spinner.TickMsg); ok {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.attach != nil && m.attach.thinkingLine != "" {
			m.attach.spinnerView = m.spinner.View()
		}
		if m.publish != nil {
			m.publish.spinnerView = m.spinnerView()
		}
		if m.chatReady && m.chat.responding {
			m.chat.spinnerView = m.spinner.View()
			m.chat.rebuildViewport()
		}
		return m, cmd
	}
	if m.chatReady && apiChatOwnsMsg(msg) {
		return m.updateAPIChat(msg)
	}
	if m.artifactReview != nil && !m.artifactReview.Detached() && apiArtifactReviewOwnsMsg(msg) {
		return m.updateAPIArtifactReview(msg)
	}
	if m.attach != nil {
		return m.updateAPIAttach(msg)
	}
	if m.chatOpen {
		if _, ok := msg.(tea.PasteMsg); ok {
			return m.updateAPIChat(msg)
		}
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.publish != nil {
			updated, _ := m.publish.Update(msg)
			m.publish = &updated
		}
		if m.diffReview != nil {
			m.diffReview.Resize(msg.Width, msg.Height)
		}
		if m.contentViewport != nil {
			m.contentViewport.Resize(msg.Width, msg.Height)
		}
		if m.chatOpen {
			if m.chat.fullscreen {
				m.chat = m.chat.resize(msg.Width, msg.Height)
			} else {
				m.chat = m.chat.resize(msg.Width, m.chat.chatPanelHeight(msg.Height))
			}
		}
		if m.welcome != nil {
			welcome, _ := m.welcome.Update(msg)
			m.welcome = &welcome
		}
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
		if m.ownedServerShutdownPending {
			return m, nil
		}
		return m, tea.Batch(m.fetchRefreshSnapshotCmd(msg.signal), m.listenForAPIEvents())
	case apiRefreshSnapshotMsg:
		if m.ownedServerShutdownPending && msg.err != nil {
			return m, nil
		}
		if msg.err != nil {
			// Surface the error but still apply whatever was fetched before it.
			// FetchRefreshSnapshot fills the snapshot incrementally and returns
			// the partial result alongside the error, so discarding it would drop
			// good data — e.g. a prompt snapshot fetched just before a slow
			// live-preview call timed out, which would otherwise leave the help
			// badge stale until the next successful refresh.
			m.statusMessage = "Refresh failed: " + firstLine(msg.err.Error())
		}
		m.ApplyRefreshSnapshot(msg.snapshot)
		if msg.content != nil {
			m.storeContent(*msg.content)
			m.rebuildPresentation(m.selectedFeature)
			m.syncAPIContentViewport()
		}
		m = m.applyAPIChatRefreshSnapshot(msg.snapshot)
		return m, m.apiChatRecoveryCmdIfResponding()
	case apiAttachSessionsSnapshotMsg:
		return m.applyAPIAttachSessionsSnapshot(msg)
	case apiContentSelectionMsg:
		if msg.err != nil {
			m.statusMessage = "Content load failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		m.storeContent(msg.content)
		m.statusMessage = msg.status
		m.rebuildPresentation(msg.featureID)
		m.syncAPIContentViewport()
		return m, nil
	case apiTextPanelMsg:
		m.textPanelActive = true
		m.textPanelTitle = msg.title
		m.textPanelContent = msg.content
		m.statusMessage = ""
		return m, nil
	case apiDiffReviewMsg:
		if m.diffReview == nil {
			vp := newReviewViewportModel(m.width, m.height, "")
			m.diffReview = &vp
		}
		m.diffReview.SetContent(msg.content)
		m.diffReview.GotoTop()
		m.statusMessage = ""
		return m, nil
	case apiFeatureDetailMsg:
		if msg.featureID != "" && msg.featureID != m.selectedFeature {
			return m, nil
		}
		if msg.err != nil {
			m.statusMessage = "Detail refresh failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		m.storeFeatureDetail(msg.detail)
		m.upsertFeatureSummary(apiFeatureDetailSummary(msg.detail.Feature))
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
	case apiReviewArtifactMsg:
		if msg.featureID != "" && msg.featureID != m.selectedFeature {
			return m, nil
		}
		if msg.err != nil {
			m.statusMessage = "Review artifact load failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		m.storeFeatureDetail(msg.detail)
		summary := apiFeatureDetailSummary(msg.detail.Feature)
		m.upsertFeatureSummary(summary)
		f := m.apiDashboardFeature(summary, msg.detail.Feature, true)
		m.rebuildPresentation(msg.featureID)
		return m.openAPIReviewModel(f, msg.artifact)
	case apiFeatureConfigMsg:
		if msg.err != nil {
			m.statusMessage = "Config load failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		m.configEditor = newAPIEditConfigModel(msg.featureID, m.featureNameByID(msg.featureID), msg.config, apiFeatureModelCatalog(m.catalog))
		if f := m.selectedAPIDashboardFeature(); f != nil && f.ID == msg.featureID {
			m.configEditor.deferredEffectWarning = featureConfigChangesDeferred(f)
		}
		m.statusMessage = ""
		return m, nil
	case publishDescGeneratedMsg:
		if m.publish == nil {
			return m, nil
		}
		updated, cmd := m.publish.Update(msg)
		m.publish = &updated
		return m, cmd
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
		comments := append([]server.ReviewCommentDTO(nil), msg.response.Comments...)
		m.reviewComments = &apiReviewCommentsPanel{
			featureID:   msg.featureID,
			featureName: msg.featureName,
			repo:        repo,
			mode:        mode,
			modes:       modes,
			comments:    comments,
			browser:     newReviewCommentsBrowserModel(msg.featureName, repo, reviewCommentItemsFromDTO(comments), m.width, m.height),
		}
		m.statusMessage = fmt.Sprintf("Fetched %d review comments from %s", len(msg.response.Comments), repo)
		return m, nil
	case apiEventErrorMsg:
		if m.ownedServerShutdownPending {
			return m, nil
		}
		if msg.err != nil {
			m.statusMessage = "Event stream failed: " + firstLine(msg.err.Error())
		}
		return m, nil
	case apiMutationResultMsg:
		if msg.err != nil {
			if msg.kind == mutationKindFeaturePublish {
				m.publish = nil
			}
			if msg.kind == mutationKindFeatureCreate && msg.featureID != "" {
				m.clearCreatePrompt()
			}
			if msg.kind == mutationKindFeatureConfigUpdate && m.configEditor != nil {
				m.configEditor.saving = false
				m.configEditor.saveErr = firstLine(msg.err.Error())
				return m, nil
			}
			m.statusMessage = "Mutation failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		if msg.kind == mutationKindRecoveryExecute {
			m.recoveryPanel = nil
			m.recovery = server.RecoverySnapshotResponse{}
		}
		if msg.kind == mutationKindFeatureConfigUpdate {
			m.configEditor = nil
		}
		if msg.kind == mutationKindRuntimeConfigUpdate {
			m.configEditor = nil
		}
		if msg.requestID != "" {
			m.clearResolvedControlRequest(msg.requestID)
		}
		if msg.kind == mutationKindFeatureNeedUserInputDecision {
			m.clearNeedInputPrompt()
		}
		if msg.kind == mutationKindPermissionAnswer {
			m.clearPermissionPrompt()
		}
		if msg.kind == mutationKindHelpSend {
			m.clearHelpPrompt()
		}
		if msg.kind == mutationKindAskUserAnswer {
			m.clearAskUserPrompt()
		}
		if msg.kind == mutationKindFeatureCreate {
			m.clearCreatePrompt()
		}
		if msg.kind == mutationKindFeatureReviewComments {
			m.reviewComments = nil
		}
		if msg.kind == mutationKindFeatureDelete {
			m.removeFeatureState(msg.featureID)
		}
		if msg.kind == mutationKindFeaturePublish {
			m.publish = nil
		}
		m.statusMessage = apiMutationSuccessMessage(msg.kind)
		if msg.kind == mutationKindFeatureRewind && msg.featureID != "" {
			delete(m.contents, msg.featureID)
			if m.artifactReview != nil && m.artifactReview.FeatureID() == msg.featureID {
				m.artifactReview = nil
			}
			m.rebuildPresentation(m.selectedFeature)
			return m, m.fetchFeatureDetailCmd(msg.featureID)
		}
		if apiMutationRefreshesFeatureDetail(msg.kind) && msg.featureID != "" {
			m.rebuildPresentation(m.selectedFeature)
			return m, m.fetchFeatureDetailCmd(msg.featureID)
		}
		m.rebuildPresentation(m.selectedFeature)
		return m, nil
	case apiRuntimeConfigMutationMsg:
		m.wizardRuntimeConfigPending = false
		if msg.err != nil {
			if m.configEditor != nil && m.configEditor.isWorkspace {
				m.configEditor.saving = false
				m.configEditor.saveErr = firstLine(msg.err.Error())
				return m, nil
			}
			m.statusMessage = "Runtime config update failed: " + firstLine(msg.err.Error())
			return m, nil
		}
		m.runtimeConfig = msg.config
		ApplyKeyboardLayout(m.runtimeConfig.UI.KeyboardLayout)
		if m.configEditor != nil && m.configEditor.isWorkspace {
			m.configEditor = nil
		}
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
	case RewindReviewDecisionMsg:
		return m, m.reviewDecisionCmd(msg.FeatureID, server.ReviewDecisionRequest{
			Decision: msg.Decision,
			Phase:    msg.Phase.DirName(),
			IsRewind: true,
		})
	case apiOwnedServerStoppedMsg:
		if msg.err != nil {
			m.ownedServerShutdownPending = false
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
		if m.welcome != nil {
			return m.updateAPIWelcome(msg)
		}
		if m.refactorPrompt != nil {
			return m.handleAPIRefactorPromptMsg(msg)
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
			m.quitOwnedServerPrompt = false
			m.ownedServerShutdownPending = true
			m.statusMessage = "Stopping server..."
			if m.cancelEvents != nil {
				m.cancelEvents()
			}
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
	if m.chatOpen {
		return m.updateAPIChat(msg)
	}
	if m.welcome != nil {
		return m.updateAPIWelcome(msg)
	}
	if m.contentPanelActive && msg.Code == tea.KeyEscape {
		m.closeAPIContentView()
		return m, nil
	}
	if m.diffReview != nil {
		return m.handleAPIDiffReviewKey(msg)
	}
	if m.publish != nil {
		return m.handleAPIPublishKey(msg)
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
			return m, m.permissionAnswerCmd(m.permissionRequest, "allow_once")
		case "r":
			if m.permissionRequest.Remember != nil {
				return m, m.permissionAnswerCmd(m.permissionRequest, permission.DecisionAllowRemember)
			}
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
	if m.contentPanelActive {
		return m.handleAPIContentKey(msg)
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
		if !m.requireSelectedFeature() {
			return m, nil
		}
		return m, m.toggleAPIInputNotificationsCmd(m.selectedFeature)
	case key.Matches(msg, keys.Chat):
		return m.transitionToAPIChat()
	case msg.Text == "a":
		return m.openAPIContextualAction()
	case msg.Text == "o":
		return m.showAPIOverview(), nil
	}
	switch msg.Text {
	case "E":
		return m.openRuntimeConfigEditor(), nil
	case "M":
		return m.confirmSelectedFeatureAction(mutationKindFeatureMerge), nil
	case "D":
		return m.confirmSelectedFeatureAction(mutationKindFeatureMarkDone), nil
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
		if canRetrySetup(m.selectedAPIDashboardFeature()) {
			return m, m.selectedFeatureActionCmd(mutationKindFeatureRetry, m.selectedFeature)
		}
		return m.confirmSelectedFeatureAction(mutationKindFeatureRestart), nil
	case "s":
		return m.confirmSelectedFeatureAction(mutationKindFeatureStop), nil
	case "d":
		return m.confirmSelectedFeatureAction(mutationKindFeatureDelete), nil
	case "c":
		return m.confirmSelectedFeatureAction(mutationKindFeatureCleanup), nil
	case "e":
		if !m.requireSelectedFeature() {
			return m, nil
		}
		if f := m.selectedAPIDashboardFeature(); !canEditFeatureConfig(f) {
			m.statusMessage = statusMsgNoFeatureSelected
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
		return m.openRepoCycleAction(mutationKindFeatureTweakStart), nil
	case "b":
		return m.confirmSelectedFeatureAction(mutationKindFeatureRebase), nil
	case "i":
		return m.openNeedInputPrompt(), nil
	case "a":
		return m.openPermissionPrompt(), nil
	case "h":
		return m.openHelpPrompt(), nil
	case "u":
		if m.selectedFeature != "" && m.hasAPIAttachableSession(m.selectedFeature) {
			return m.openAPIAttachForFeature(m.selectedFeature)
		}
		return m.openAskUserPrompt(), nil
	}
	switch msg.Code {
	case tea.KeyEnter, tea.KeyUp, tea.KeyDown:
		return m.handleAPIDashboardListKey(msg)
	}
	return m, nil
}

func (m APIAppModel) updateAPIAttach(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case apiRefreshSignalMsg:
		if m.ownedServerShutdownPending {
			return m, nil
		}
		return m, tea.Batch(m.fetchRefreshSnapshotCmd(msg.signal), m.listenForAPIEvents())
	case apiRefreshSnapshotMsg:
		if m.ownedServerShutdownPending && msg.err != nil {
			return m, nil
		}
		if msg.err != nil {
			return m, nil
		}
		m.ApplyRefreshSnapshot(msg.snapshot)
		if msg.content != nil {
			m.storeContent(*msg.content)
		}
		var cmd tea.Cmd
		m, cmd = m.applyAPIAttachRefreshSnapshot(msg.snapshot)
		return m, cmd
	case apiAttachSessionsSnapshotMsg:
		return m.applyAPIAttachSessionsSnapshot(msg)
	case apiEventErrorMsg:
		return m, nil
	case HelpResolvedMsg:
		if msg.RequestID != "" {
			m.clearResolvedControlRequest(msg.RequestID)
			m.rebuildPresentation(m.selectedFeature)
		}
		if msg.FeatureID != "" {
			return m, m.fetchFeatureDetailCmd(msg.FeatureID)
		}
		return m, nil
	case apiSessionOutputLineMsg:
		if msg.sessionID != m.liveOutputSessionID {
			return m, nil
		}
		sess := m.attachedSessionView()
		if sess == nil || sess.ID() != m.liveOutputSessionID {
			m.stopLiveSessionOutput()
			return m, nil
		}
		return m, m.listenLiveSessionOutputCmd(sess)
	case apiSessionOutputDoneMsg:
		if msg.sessionID == m.liveOutputSessionID {
			m.stopLiveSessionOutput()
		}
		return m, nil
	case apiTranscriptBackfillMsg:
		if m.transcriptBackfills != nil {
			delete(m.transcriptBackfills, msg.sessionID)
		}
		if msg.err != nil {
			return m, nil
		}
		m.storeTranscript(msg.sessionID, msg.transcript)
		active := m.attachedSessionView()
		if active == nil || active.ID() != msg.sessionID || m.attach == nil {
			return m, nil
		}
		wasAtTop := m.attach.viewport.AtTop()
		oldOffset := m.attach.viewport.YOffset()
		oldTotal := m.attach.viewport.TotalLineCount()
		if len(active.applyTranscriptBackfill(msg.transcript)) == 0 {
			return m, nil
		}
		m.attach.updateViewport()
		if wasAtTop {
			m.attach.viewport.GotoTop()
		} else if delta := m.attach.viewport.TotalLineCount() - oldTotal; delta > 0 {
			m.attach.viewport.SetYOffset(oldOffset + delta)
		}
		return m, nil
	}

	updated, cmd := m.attach.Update(msg)
	m.attach = &updated
	if updated.Detached() || updated.Done() {
		m.attach = nil
		m.stopLiveSessionOutput()
		if m.selectedFeature != "" {
			return m, tea.Batch(cmd, m.fetchFeatureDetailCmd(m.selectedFeature))
		}
		return m, cmd
	}
	liveCmd := m.syncLiveSessionOutputForAttach()
	backfillCmd := m.maybeStartAttachTranscriptBackfill(msg)
	return m, tea.Batch(cmd, liveCmd, backfillCmd)
}

func apiArtifactReviewOwnsMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyPressMsg,
		tea.WindowSizeMsg,
		tea.PasteMsg,
		artifactReviewSessionStartedMsg,
		artifactReviewMsgsMsg,
		artifactReviewDoneMsg,
		artifactReviewStartErrorMsg,
		artifactReviewSendErrorMsg:
		return true
	default:
		return false
	}
}

func (m APIAppModel) updateAPIArtifactReview(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case artifactReviewSessionStartedMsg:
		if m.artifactReview != nil && msg.sess != nil &&
			msg.generation == m.artifactReview.sessionGeneration &&
			!m.artifactReview.sessionStarted {
			updated, cmd := m.artifactReview.handleSessionStarted(msg.sess)
			m.artifactReview = &updated
			return m, cmd
		}
		if msg.sess != nil {
			_ = msg.sess.Stop()
		}
		return m, nil
	}

	updated, cmd := m.artifactReview.Update(msg)
	m.artifactReview = &updated
	if updated.Detached() {
		if updated.Decided() {
			updated.StopSession()
			m.artifactReview = &updated
		}
		return m, cmd
	}
	return m, cmd
}

func apiChatOwnsMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case chatSessionStartedMsg, chatMsgsMsg, chatDoneMsg, chatSendErrorMsg, chatRecoveryTickMsg, ChatExitMsg:
		return true
	default:
		return false
	}
}

func (m APIAppModel) updateAPIChat(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(ChatExitMsg); ok {
		m.chatOpen = false
		m.chat.fullscreen = false
		return m, nil
	}
	if tick, ok := msg.(chatRecoveryTickMsg); ok && !m.chat.pollSession && tick.sess == m.chat.sess && m.chat.responding && m.chat.sess != nil {
		return m, m.fetchRefreshSnapshotCmd(apiChatRecoveryRefreshSignal(m.chat.sess))
	}
	updated, cmd := m.chat.Update(msg)
	m.chat = updated
	// Re-sync docked/fullscreen dimensions after every message, not just on
	// a fullscreen flip — chatPanelHeight depends on turns/responding, which
	// change on ordinary messages (a streamed reply, a new turn) with no
	// tea.WindowSizeMsg involved. Without this, the panel's rendered height
	// stays stuck at whatever it was when last resized while the dashboard's
	// own layout math (which calls chatPanelHeight fresh every render)
	// drifts out of sync with it.
	if m.chat.fullscreen {
		m.chat = m.chat.resize(m.width, m.height)
	} else {
		m.chat = m.chat.resize(m.width, m.chat.chatPanelHeight(m.height))
	}
	if cmd != nil {
		return m, cmd
	}
	if _, ok := msg.(chatSessionStartedMsg); ok {
		return m, m.apiChatRecoveryCmdIfResponding()
	}
	return m, nil
}

func (m APIAppModel) apiChatRecoveryCmdIfResponding() tea.Cmd {
	if !m.chatReady || m.chat.pollSession || !m.chat.responding || m.chat.sess == nil {
		return nil
	}
	return chatRecoveryTickCmd(m.chat.sess, nil)
}

func apiChatRecoveryRefreshSignal(sess session.SessionView) server.RefreshSignal {
	if sess == nil {
		return server.RefreshSignal{}
	}
	resource := server.ResourceDTO{
		Type:      resourceTypeSession,
		ID:        sess.ID(),
		FeatureID: sess.FeatureID(),
		Phase:     sess.Phase().String(),
	}
	return server.RefreshSignal{
		Event: server.SSEEventDTO{
			Kind:     sseEventKindSessionUpdated,
			Resource: resource,
		},
		Resource: resource,
	}
}

func (m APIAppModel) transitionToAPIChat() (tea.Model, tea.Cmd) {
	chatH := m.chat.chatPanelHeight(m.height)
	if !m.chatReady {
		m.chat = NewAPIChatModel(m.width, chatH, m.client)
		m.chatReady = true
	} else {
		m.chat = m.chat.resize(m.width, chatH)
		m.chat.input.Focus()
	}
	m.chatOpen = true
	m.statusMessage = ""
	return m, textarea.Blink
}

func (m APIAppModel) View() tea.View {
	if m.artifactReview != nil && !m.artifactReview.Detached() {
		return apiAltView(m.artifactReview.View())
	}
	if m.attach != nil {
		return apiAltView(m.attach.View())
	}
	if m.welcome != nil {
		return apiAltView(m.welcome.View())
	}
	if m.publish != nil {
		return apiAltView(m.publish.View())
	}
	if m.diffReview != nil {
		return apiAltView(renderReviewViewportScreen(
			m.width,
			diffReviewTitle,
			"",
			diffReviewTitle,
			*m.diffReview,
			" [esc] Close   [↑/↓] Scroll",
		))
	}
	if m.reviewComments != nil {
		view := m.renderAPIReviewCommentsPanel(max(m.width-2, 80))
		if m.helpOverlayActive {
			w := max(m.width, 80)
			h := max(m.height, 24)
			view = overlayModal(view, m.helpOverlay.View(), w, h)
		}
		return apiAltView(view)
	}
	if m.contentPanelActive && m.snapshot.Content != nil {
		view := m.renderAPIContentScreen()
		if m.helpOverlayActive {
			w := max(m.width, 80)
			h := max(m.height, 24)
			view = overlayModal(view, m.helpOverlay.View(), w, h)
		}
		return apiAltView(view)
	}
	var view string
	if m.chatOpen && m.chat.fullscreen {
		view = m.chat.View()
	} else {
		view = m.renderAPIDashboard()
		if m.chatOpen {
			view += m.chat.View()
		} else if m.chatReady && m.chat.responding {
			view += lipgloss.NewStyle().Foreground(colorBrand).Render("  * Chat thinking... press " + ChatKeyHint() + " to view")
		}
	}
	w := max(m.width, 80)
	h := max(m.height, 24)
	if m.recoveryPanel != nil {
		view = overlayModal(view, m.renderAPIRecovery(), w, h)
	}
	if m.configEditor != nil {
		view = overlayModal(view, m.configEditor.View(), w, h)
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
	if m.repoActionPanel != nil {
		view = overlayModal(view, m.renderAPIRepoActionPanel(min(w-4, 72)), w, h)
	}
	if m.rewindPanel != nil {
		view = overlayModal(view, m.renderAPIRewindPanel(min(w-4, 72)), w, h)
	}
	if m.rewindPhasePicker != nil {
		view = overlayModal(view, m.renderAPIRoadmapRewindPanel(min(w-4, 84)), w, h)
	}
	if m.textPanelActive {
		view = overlayModal(view, panelStyle(true).Width(min(w-4, 96)).Render(m.renderAPITextPanel()), w, h)
	}
	if m.helpOverlayActive {
		view = overlayModal(view, m.helpOverlay.View(), w, h)
	}
	if m.quitOwnedServerPrompt {
		view = overlayModal(view, m.renderOwnedServerQuitPrompt(), w, h)
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
	return apiAltView(view)
}

func apiAltView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m APIAppModel) renderAPIDashboard() string {
	dashboard := m.apiDashboardModel()
	return dashboard.View()
}

func (m APIAppModel) apiDashboardModel() DashboardModel {
	features := m.apiDashboardFeatures()
	dashboard := NewDashboardModel(features, m.runtimeConfig.Runtime.StateDir)
	dashboard.width = max(m.width, 80)
	dashboard.height = max(m.height, 24)
	if m.chatOpen {
		dashboard.height = max(m.height-m.chat.chatPanelHeight(m.height), 6)
	} else if m.chatReady && m.chat.responding {
		dashboard.height = max(m.height-1, 6)
	}
	dashboard.focusPanel = m.focusPanel
	dashboard.rightPanelMode = m.rightPanelMode
	dashboard.dangerouslySkipPerms = m.snapshot.Runtime.DangerouslySkipPermissions
	dashboard.statusMessage = m.statusMessage
	if m.welcomeSkipped {
		dashboard.SetWelcomeSkipped()
	}
	if len(m.runtimeConfig.UI.CollapsedSections) > 0 {
		dashboard.SetCollapsedSections(m.runtimeConfig.UI.CollapsedSections)
	}
	switch {
	case m.selectedFeature != "":
		dashboard.selectFeature(m.selectedFeature)
	case m.selectedSection != "":
		dashboard.selectSection(m.selectedSection)
	default:
		dashboard.syncPreview()
	}
	m.applyAPIDashboardRefactorState(&dashboard)
	spinnerView := m.spinnerView()
	dashboard.spinnerView = spinnerView
	dashboard.preview.spinnerView = spinnerView
	dashboard.livePreview.spinnerView = spinnerView
	if preview := m.snapshot.LivePreview; preview != nil {
		if dashboard.livePreview.feature != nil && preview.CostUSD > 0 {
			liveFeature := *dashboard.livePreview.feature
			liveFeature.PhaseCosts = apiPhaseCosts(nil, preview.CostUSD, liveFeature.CurrentPhase)
			dashboard.livePreview.feature = &liveFeature
		}
		dashboard.preview.contextPct = preview.ContextPct
		dashboard.livePreview.contextPct = preview.ContextPct
		dashboard.livePreview.session = newAPILivePreviewSession(*preview)
	}
	return dashboard
}

func newAPIAppSpinner() spinner.Model {
	return spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(lipgloss.NewStyle().Foreground(colorInfo)))
}

func (m APIAppModel) spinnerView() string {
	if view := m.spinner.View(); view != "" && view != "(error)" {
		return view
	}
	return newAPIAppSpinner().View()
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
	return m.apiDashboardFeatureByID(m.selectedFeature)
}

func (m APIAppModel) apiDashboardFeatureByID(featureID string) *feature.Feature {
	for _, f := range m.apiDashboardFeatures() {
		if f.ID == featureID {
			return f
		}
	}
	return nil
}

// applyAPIFeatureDetail merges feature detail fields (description, timings,
// costs, active run info, failure, and need-input state) into f.
func applyAPIFeatureDetail(f *feature.Feature, detail server.FeatureDetailDTO) {
	f.Description = detail.Description
	f.Summary = detail.Summary
	f.Pipeline = feature.PipelineProfile(detail.Pipeline)
	f.PhaseTimings = apiPhaseTimings(detail.Timing.ByPhase)
	f.PhaseCosts = apiPhaseCosts(detail.Cost.ByPhase, detail.Cost.TotalUSD, f.CurrentPhase)
	f.ValidatingPlan = detail.ReviewGate.ValidatingPlan
	f.ValidatorStatuses = copyStringMapValues(detail.ReviewGate.ValidatorStatuses)
	applyAPIActiveRunDetail(f, detail.ActiveRunDetail)
	if detail.Failure != nil {
		f.FailureType = detail.Failure.Type
		f.LastError = detail.Failure.Message
	}
	if detail.NeedUserInput != nil && detail.NeedUserInput.Open && f.Status != feature.StatusNeedUserInput {
		f.Status = feature.StatusNeedUserInput
	}
}

// applyAPIActiveRunDetail merges active-run fields from run into f. No-op if run is nil.
func applyAPIActiveRunDetail(f *feature.Feature, run *server.RunSummaryDTO) {
	if run == nil {
		return
	}
	if run.RunNumber > 0 {
		f.ActiveRun = run.RunNumber
	}
	f.CurrentIteration = firstNonZero(run.Iteration, f.CurrentIteration)
	f.CurrentRoadmapPhase = firstNonZero(run.RoadmapPhase, f.CurrentRoadmapPhase)
	f.TotalRoadmapPhases = firstNonZero(run.RoadmapTotal, f.TotalRoadmapPhases)
	f.CurrentPhaseStatus = firstNonEmpty(run.PhaseStatus, f.CurrentPhaseStatus)
	if run.CurrentPhase != "" {
		f.CurrentPhase = apiFeaturePhase(run.CurrentPhase)
	}
	if run.PendingReviewPhase != "" {
		phase := apiFeaturePhase(run.PendingReviewPhase)
		f.PendingReviewPhase = &phase
	}
	if run.PendingRewindReviewRoadmapPhase > 0 {
		roadmapPhase := run.PendingRewindReviewRoadmapPhase
		f.PendingRewindReviewRoadmapPhase = &roadmapPhase
	}
	f.IsRewind = run.IsRewind
}

func (m APIAppModel) apiDashboardFeature(summary server.FeatureSummary, detail server.FeatureDetailDTO, hasDetail bool) *feature.Feature {
	models := m.runtimeConfig.Defaults
	summaryCycle := summary.Cycle
	if hasDetail {
		summary = mergeAPIFeatureSummary(summary, apiFeatureDetailSummary(detail))
		summary.Cycle = summaryCycle
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
		applyAPIFeatureDetail(f, detail)
	}
	activeCycleType := feature.RepoCycleType("")
	rebaseCount := 0
	tweakCount := 0
	refactorCount := 0
	reviewCommentsCount := 0
	if summary.Cycle != nil && (summary.Cycle.Type != "" || summary.Cycle.Status != "") {
		cycleType := feature.RepoCycleType(summary.Cycle.Type)
		cycleCount := summary.Cycle.Count
		if cycleCount <= 0 && cycleType != "" {
			cycleCount = 1
		}
		f.ActiveCycle = &feature.CycleState{
			Type:      cycleType,
			Status:    summary.Cycle.Status,
			Count:     cycleCount,
			Iteration: summary.Cycle.Iteration,
		}
		activeCycleType = cycleType
		switch cycleType {
		case feature.CycleRebase:
			rebaseCount = cycleCount
		case feature.CycleTweak:
			tweakCount = cycleCount
		case feature.CycleRefactor:
			refactorCount = cycleCount
		case feature.CycleReviewComments:
			reviewCommentsCount = cycleCount
		}
	}
	repoStatuses := map[string]server.RepoStatusDTO{}
	if hasDetail {
		for _, repo := range detail.RepoStatus {
			repoStatuses[repo.Name] = repo
		}
	}
	for _, repoName := range summary.Repos {
		f.Repos = append(f.Repos, m.apiDashboardRepo(repoName, repoStatuses[repoName], hasDetail, f.Slug, f.ID))
	}
	for _, repo := range detail.RepoStatus {
		if repo.Name == "" || apiHasRepo(f.Repos, repo.Name) {
			continue
		}
		f.Repos = append(f.Repos, m.apiDashboardRepo(repo.Name, repo, true, f.Slug, f.ID))
	}
	for _, repo := range f.Repos {
		dto := repoStatuses[repo.Name]
		f.RepoStates[repo.Name] = &feature.RepoState{
			Touched:   dto.Touched,
			PRURL:     dto.PRURL,
			LastError: dto.LastError,
			Freshness: dto.Freshness,
		}
		if summary.Cycle != nil && (dto.CycleType != "" || dto.CycleStatus != "") {
			cycleType := feature.RepoCycleType(dto.CycleType)
			cycle := &feature.RepoCycleState{
				Type:   cycleType,
				Status: dto.CycleStatus,
			}
			if f.ActiveCycle != nil && f.ActiveCycle.Type == cycleType {
				cycle.Count = f.ActiveCycle.Count
				cycle.Iteration = f.ActiveCycle.Iteration
			}
			f.RepoCycles[repo.Name] = cycle
			if f.ActiveCycle == nil && cycle.Status != "" {
				f.ActiveCycle = &feature.CycleState{Type: cycle.Type, Status: cycle.Status, Count: cycle.Count}
			}
			if activeCycleType == "" && cycle.Status != "" {
				activeCycleType = cycle.Type
			}
		}
	}
	if hasDetail {
		for _, dto := range detail.RepoStatus {
			if dto.Name == "" || dto.RebaseStatus == "" {
				continue
			}
			if f.RebaseOperation == nil {
				f.RebaseOperation = &feature.RebaseOperationState{
					Stage: feature.RebaseStageHarness,
					Repos: map[string]*feature.RebaseRepoProgress{},
				}
			}
			f.RebaseOperation.Repos[dto.Name] = &feature.RebaseRepoProgress{
				Status:        feature.RebaseRepoStatus(dto.RebaseStatus),
				RebaseTarget:  dto.RebaseTarget,
				ConflictFiles: append([]string(nil), dto.ConflictFiles...),
				LastError:     dto.LastError,
			}
		}
	}
	if len(f.RepoStates) == 0 {
		f.RepoStates = nil
	}
	if len(f.RepoCycles) == 0 {
		f.RepoCycles = nil
	}
	var setup *feature.SetupState
	if hasDetail && detail.ActiveRunDetail != nil {
		setup = apiSetupState(detail.ActiveRunDetail.Setup)
	}
	m.applyAPIAttention(f)
	f.SetRun(&feature.Run{
		RunNumber:                       f.ActiveRun,
		CurrentIteration:                f.CurrentIteration,
		CurrentRoadmapPhase:             f.CurrentRoadmapPhase,
		TotalRoadmapPhases:              f.TotalRoadmapPhases,
		CurrentPhaseStatus:              f.CurrentPhaseStatus,
		PendingReviewPhase:              f.PendingReviewPhase,
		PendingRewindReviewRoadmapPhase: f.PendingRewindReviewRoadmapPhase,
		IsRewind:                        f.IsRewind,
		ActiveCycleType:                 activeCycleType,
		RebaseCount:                     rebaseCount,
		TweakCount:                      tweakCount,
		RefactorCount:                   refactorCount,
		ReviewCommentsCount:             reviewCommentsCount,
		RepoStates:                      f.RepoStates,
		RepoCycles:                      f.RepoCycles,
		ActiveCycle:                     f.ActiveCycle,
		RebaseOperation:                 f.RebaseOperation,
		ValidatingPlan:                  f.ValidatingPlan,
		ValidatorStatuses:               f.ValidatorStatuses,
		PhaseTimings:                    f.PhaseTimings,
		PhaseCosts:                      f.PhaseCosts,
		LastError:                       f.LastError,
		FailureType:                     f.FailureType,
		Setup:                           setup,
	})
	return f
}

func apiSetupState(dto *server.SetupDTO) *feature.SetupState {
	if dto == nil {
		return nil
	}
	tasks := make(map[string]feature.SetupTask, len(dto.Tasks))
	for key, task := range dto.Tasks {
		tasks[key] = apiSetupTask(task)
	}
	return &feature.SetupState{
		Status:        feature.SetupStatus(dto.Status),
		Attempt:       dto.Attempt,
		StartedAt:     dto.StartedAt,
		CompletedAt:   dto.CompletedAt,
		LatestLogPath: dto.LatestLogPath,
		Tasks:         tasks,
		TaskOrder:     append([]string(nil), dto.TaskOrder...),
		LastError:     dto.LastError,
	}
}

func apiSetupTask(dto server.SetupTaskDTO) feature.SetupTask {
	return feature.SetupTask{
		Key:              dto.Key,
		Kind:             feature.SetupTaskKind(dto.Kind),
		Label:            dto.Label,
		Repo:             dto.Repo,
		Status:           feature.SetupStatus(dto.Status),
		Path:             dto.Path,
		SourcePath:       dto.SourcePath,
		Branch:           dto.Branch,
		StartPoint:       dto.StartPoint,
		UseCurrentBranch: dto.UseCurrentBranch,
		Attempt:          dto.Attempt,
		StartedAt:        dto.StartedAt,
		EndedAt:          dto.EndedAt,
		LastError:        dto.LastError,
	}
}

func mergeAPIFeatureSummary(base, overlay server.FeatureSummary) server.FeatureSummary {
	if overlay.ID != "" {
		base.ID = overlay.ID
	}
	if overlay.Name != "" {
		base.Name = overlay.Name
	}
	if overlay.Slug != "" {
		base.Slug = overlay.Slug
	}
	if overlay.Status != "" {
		base.Status = overlay.Status
	}
	if overlay.CurrentPhase != "" {
		base.CurrentPhase = overlay.CurrentPhase
	}
	if overlay.Cycle != nil {
		base.Cycle = overlay.Cycle
	}
	if overlay.ActiveRun != 0 {
		base.ActiveRun = overlay.ActiveRun
	}
	if overlay.RunCount != 0 {
		base.RunCount = overlay.RunCount
	}
	if len(overlay.Repos) > 0 {
		base.Repos = append([]string(nil), overlay.Repos...)
	}
	if !overlay.CreatedAt.IsZero() {
		base.CreatedAt = overlay.CreatedAt
	}
	if !reflect.DeepEqual(overlay.Checkpoints, server.CheckpointsDTO{}) {
		base.Checkpoints = overlay.Checkpoints
	}
	if !reflect.DeepEqual(overlay.Progress, server.FeatureProgress{}) {
		base.Progress = overlay.Progress
	}
	if len(overlay.Warnings) > 0 {
		base.Warnings = append([]server.WarningDTO(nil), overlay.Warnings...)
	}
	return base
}

func apiFeatureDetailSummary(detail server.FeatureDetailDTO) server.FeatureSummary {
	return server.FeatureSummary{
		ID:           detail.ID,
		Name:         detail.Name,
		Slug:         detail.Slug,
		Status:       detail.Status,
		CurrentPhase: detail.CurrentPhase,
		Cycle:        detail.Cycle,
		ActiveRun:    detail.ActiveRun,
		RunCount:     detail.RunCount,
		Repos:        append([]string(nil), detail.Repos...),
		CreatedAt:    detail.CreatedAt,
		Checkpoints:  detail.Checkpoints,
		Progress:     detail.Progress,
		Warnings:     append([]server.WarningDTO(nil), detail.Warnings...),
	}
}

func (m APIAppModel) apiDashboardRepo(name string, status server.RepoStatusDTO, hasDetail bool, featureSlug, featureID string) feature.FeatureRepo {
	repo := feature.FeatureRepo{Name: name}
	for _, cfgRepo := range m.runtimeConfig.Repos {
		if cfgRepo.Name == name {
			repo.Path = cfgRepo.Path
			break
		}
	}
	repo.WorktreePath = m.apiDashboardWorktreePath(featureSlug, featureID, name)
	if hasDetail {
		publishable := status.Publishable
		repo.Publishable = &publishable
	}
	return repo
}

func (m APIAppModel) apiDashboardWorktreePath(featureSlug, featureID, repoName string) string {
	if featureSlug == "" || repoName == "" || m.runtimeConfig.Runtime.StateDir == "" {
		return ""
	}
	for _, slug := range []string{feature.WorkspaceSlug(featureSlug, featureID), featureSlug} {
		candidate := filepath.Join(filepath.Dir(m.runtimeConfig.Runtime.StateDir), "worktrees", slug, repoName)
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

func (m APIAppModel) apiSessionWorkDir(featureID, repoName string) string {
	summary, ok := m.apiFeatureSummary(featureID)
	if !ok {
		return ""
	}
	if repoName == "" && len(summary.Repos) == 1 {
		repoName = summary.Repos[0]
	}
	if repoName == "" {
		return ""
	}
	return m.apiDashboardWorktreePath(summary.Slug, summary.ID, repoName)
}

func (m APIAppModel) apiFeatureSummary(featureID string) (server.FeatureSummary, bool) {
	for _, summary := range m.featureList.Features {
		if summary.ID == featureID {
			return summary, true
		}
	}
	if detail, ok := m.featureDetails[featureID]; ok && detail.Feature.ID != "" {
		return apiFeatureDetailSummary(detail.Feature), true
	}
	return server.FeatureSummary{}, false
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

func (m *DashboardModel) findAndSelect(predicate func(listItem) bool) bool {
	for i, item := range m.visibleItems {
		if predicate(item) {
			m.cursor = i
			m.computeCursorLine()
			m.updateScrollState(0)
			m.syncPreview()
			return true
		}
	}
	m.syncPreview()
	return false
}

func (m *DashboardModel) selectFeature(featureID string) bool {
	if featureID == "" {
		m.syncPreview()
		return false
	}
	return m.findAndSelect(func(item listItem) bool {
		return item.kind == listItemFeature && item.feature != nil && item.feature.ID == featureID
	})
}

func (m *DashboardModel) selectSection(section string) bool {
	if section == "" {
		m.syncPreview()
		return false
	}
	return m.findAndSelect(func(item listItem) bool {
		return item.kind == listItemSectionHeader && item.section == section
	})
}

type apiSessionView struct {
	id                     string
	featureID              string
	phase                  feature.Phase
	repo                   string
	kind                   ports.SessionKind
	label                  string
	status                 ports.SessionStatus
	startedAt              time.Time
	iteration              int
	provider               string
	model                  string
	workDir                string
	initialPrompt          string
	contextPct             int
	log                    *session.MessageLog
	cost                   *llm.ResultMessage
	client                 APIClient
	statusCh               chan string
	attachCh               chan llm.SDKMessage
	doneCh                 chan struct{}
	pending                []*llm.ControlRequestMessage
	rememberPreviews       map[string]server.PermissionRememberPreviewDTO
	lastTranscriptMessage  int
	firstTranscriptMessage int
	lastTranscriptRows     map[string]string
	lastTranscriptTailKey  string
	mu                     sync.Mutex
}

// appendAPITranscriptRows appends convertible rows to log and returns the
// min/max row index seen, or -1/-1 if none were appended.
func appendAPITranscriptRows(log *session.MessageLog, rows []server.TranscriptMessageDTO, sessionID string) (firstIndex, lastIndex int) {
	firstIndex, lastIndex = -1, -1
	for _, row := range rows {
		msg, ok := apiTranscriptRowToSDKMessage(row, sessionID)
		if !ok {
			continue
		}
		log.Append(msg)
		if firstIndex == -1 || row.Index < firstIndex {
			firstIndex = row.Index
		}
		if row.Index > lastIndex {
			lastIndex = row.Index
		}
	}
	return firstIndex, lastIndex
}

func newAPILivePreviewSession(preview APILivePreviewPresentation) ports.SessionView {
	if preview.SessionID == "" && preview.Activity == "" && len(preview.TranscriptRows) == 0 && len(preview.TranscriptTail) == 0 {
		return nil
	}
	log := session.NewMessageLog()
	firstIndex := -1
	lastIndex := -1
	if len(preview.TranscriptRows) > 0 {
		firstIndex, lastIndex = appendAPITranscriptRows(log, preview.TranscriptRows, preview.SessionID)
	} else {
		for _, line := range preview.TranscriptTail {
			appendAPILivePreviewText(log, line)
		}
	}
	appendAPILivePreviewActivity(log, preview.Activity)
	var cost *llm.ResultMessage
	if preview.CostUSD > 0 {
		cost = &llm.ResultMessage{TotalCostUSD: preview.CostUSD}
	}
	return &apiSessionView{
		id:                     preview.SessionID,
		featureID:              preview.FeatureID,
		phase:                  apiLivePreviewPhase(preview.Phase),
		kind:                   apiSessionKind(preview.Kind),
		label:                  firstNonEmpty(preview.Label, "Live Preview"),
		status:                 ports.SessionRunning,
		provider:               preview.Provider,
		model:                  preview.Model,
		contextPct:             preview.ContextPct,
		log:                    log,
		cost:                   cost,
		statusCh:               make(chan string, 8),
		attachCh:               make(chan llm.SDKMessage, 64),
		doneCh:                 make(chan struct{}),
		lastTranscriptMessage:  lastIndex,
		firstTranscriptMessage: firstIndex,
		lastTranscriptRows:     map[string]string{},
	}
}

func apiLivePreviewPhase(phase string) feature.Phase {
	if strings.TrimSpace(phase) == "" {
		return feature.PhaseImplement
	}
	return apiFeaturePhase(phase)
}

func appendAPILivePreviewActivity(log ports.MessageLog, activity string) {
	activity = strings.TrimSpace(activity)
	if activity == "" {
		return
	}
	if tool := apiToolNameFromActivity(activity); tool != "" {
		log.Append(llm.SDKMessage{
			Type: transcriptTypeToolProgress,
			ToolProgress: &llm.ToolProgressMessage{
				Type:     transcriptTypeToolProgress,
				ToolName: tool,
			},
		})
		return
	}
	log.Append(llm.SDKMessage{
		Type:   transcriptTypeStatus,
		Status: &llm.StatusMessage{Type: transcriptTypeStatus, Message: activity},
	})
}

func appendAPILivePreviewText(log ports.MessageLog, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	log.Append(llm.SDKMessage{
		Type: roleAssistant,
		Assistant: &llm.AssistantMessage{
			Type: roleAssistant,
			Message: llm.ConversationMsg{
				Role: roleAssistant,
				Content: []llm.ContentBlock{{
					Type: blockTypeText,
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

func (s *apiSessionView) ID() string { return s.id }
func (s *apiSessionView) FeatureID() string {
	return s.featureID
}
func (s *apiSessionView) Phase() feature.Phase { return s.phase }
func (s *apiSessionView) RepoName() string     { return s.repo }
func (s *apiSessionView) PermCacheScope() string {
	return s.repo
}
func (s *apiSessionView) Kind() ports.SessionKind { return s.kind }
func (s *apiSessionView) Label() string           { return s.label }
func (s *apiSessionView) Status() ports.SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}
func (s *apiSessionView) IsActive() bool {
	status := s.Status()
	return status == ports.SessionRunning || status == ports.SessionWaitingHelp || status == ports.SessionWaitingPermission
}
func (s *apiSessionView) Iteration() int               { return s.iteration }
func (s *apiSessionView) StartedAt() time.Time         { return s.startedAt }
func (s *apiSessionView) InitialPrompt() string        { return s.initialPrompt }
func (s *apiSessionView) ProviderName() string         { return s.provider }
func (s *apiSessionView) Model() string                { return s.model }
func (s *apiSessionView) WorkDir() string              { return s.workDir }
func (s *apiSessionView) MessageLog() ports.MessageLog { return s.log }
func (s *apiSessionView) Cost() *llm.ResultMessage     { return s.cost }
func (s *apiSessionView) LatestUsage() *llm.Usage      { return nil }
func (s *apiSessionView) AccumulatedUsage() llm.Usage  { return llm.Usage{} }
func (s *apiSessionView) LastControlRequest() *llm.ControlRequestMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	return cloneControlRequestMessage(s.pending[len(s.pending)-1])
}
func (s *apiSessionView) PendingControlRequests() []*llm.ControlRequestMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*llm.ControlRequestMessage, 0, len(s.pending))
	for _, req := range s.pending {
		out = append(out, cloneControlRequestMessage(req))
	}
	return out
}
func (s *apiSessionView) QALog() []ports.QAPair { return nil }
func (s *apiSessionView) LogFilePath() string   { return "" }
func (s *apiSessionView) ContextPercentage() int {
	return s.contextPct
}
func (s *apiSessionView) ErrorDetail() string    { return "" }
func (s *apiSessionView) ExitCodeDetail() string { return "" }
func (s *apiSessionView) LastStdoutAt() time.Time {
	return time.Time{}
}
func (s *apiSessionView) StatusCh() <-chan string         { return s.statusCh }
func (s *apiSessionView) AttachCh() <-chan llm.SDKMessage { return s.attachCh }
func (s *apiSessionView) Done() <-chan struct{}           { return s.doneCh }
func (s *apiSessionView) HasPendingAskUserQuestion() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, req := range s.pending {
		if req != nil && req.Request.ToolName == toolNameAskUserQuestion {
			return true
		}
	}
	return false
}
func (s *apiSessionView) SendUserMessage(text string) error {
	if s.client == nil {
		return errors.New("api session client is not available")
	}
	_, err := s.client.SendHelp(context.Background(), server.HelpAnswerRequest{
		FeatureID: s.featureID,
		SessionID: s.id,
		Message:   text,
	})
	return err
}
func (s *apiSessionView) RespondToControl(requestID string, allow bool, _ string) error {
	if s.client == nil {
		return errors.New("api session client is not available")
	}
	decision := "deny"
	if allow {
		decision = "allow_once"
	}
	return s.RespondToPermissionDecision(requestID, decision, "", "")
}

func (s *apiSessionView) RespondToPermissionDecision(requestID, decision, rememberPattern, rememberScope string) error {
	if s.client == nil {
		return errors.New("api session client is not available")
	}
	answer := server.PermissionAnswerRequest{
		RequestID: requestID,
		SessionID: s.id,
		Decision:  decision,
	}
	if decision == permission.DecisionAllowRemember {
		if preview, ok := s.rememberPreview(requestID); ok {
			rememberPattern = preview.Pattern
			rememberScope = preview.Scope
		}
		answer.RememberPattern = rememberPattern
		answer.RememberScope = &rememberScope
	}
	_, err := s.client.AnswerPermission(context.Background(), answer)
	return err
}

func (s *apiSessionView) rememberPreview(requestID string) (server.PermissionRememberPreviewDTO, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rememberPreviews == nil {
		return server.PermissionRememberPreviewDTO{}, false
	}
	preview, ok := s.rememberPreviews[requestID]
	return preview, ok
}

func (s *apiSessionView) RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, _ map[string]llm.AskUserAnnotation) error {
	if s.client == nil {
		return errors.New("api session client is not available")
	}
	_, err := s.client.AnswerAskUser(context.Background(), server.AskUserAnswerRequest{
		RequestID: requestID,
		SessionID: s.id,
		Answers:   answers,
	})
	if err != nil {
		return err
	}
	s.appendLocalAskUserAnswerEchoes(questions, answers)
	return nil
}

func (s *apiSessionView) appendLocalAskUserAnswerEchoes(questions json.RawMessage, answers map[string]string) {
	if s.log == nil || len(answers) == 0 {
		return
	}
	for _, question := range apiAskUserAnswerKeysInPresentedOrder(questions, answers) {
		answer := answers[question]
		if strings.TrimSpace(answer) == "" || apiHasLocalUserEcho(s.log, answer) {
			continue
		}
		s.log.Append(llm.SDKMessage{
			Type:            roleUser,
			LocallyAppended: true,
			User: &llm.UserMessage{
				Message: llm.ConversationMsg{
					Role:    roleUser,
					Content: []llm.ContentBlock{{Type: blockTypeText, Text: answer}},
				},
			},
		})
	}
}

func apiAskUserAnswerKeysInPresentedOrder(questions json.RawMessage, answers map[string]string) []string {
	seen := map[string]bool{}
	var keys []string
	for _, q := range parseAskUserQuestions(questions) {
		if q.Question == "" || seen[q.Question] {
			continue
		}
		if _, ok := answers[q.Question]; ok {
			keys = append(keys, q.Question)
			seen[q.Question] = true
		}
	}
	var remaining []string
	for key := range answers {
		if !seen[key] {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	return append(keys, remaining...)
}

func (s *apiSessionView) ClearPendingQuestion(requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = slices.DeleteFunc(s.pending, func(req *llm.ControlRequestMessage) bool {
		return req != nil && req.RequestID == requestID
	})
}
func (s *apiSessionView) ResetWaitingStatus() {
	s.mu.Lock()
	s.status = ports.SessionRunning
	s.mu.Unlock()
}
func (s *apiSessionView) Stop() error {
	if s.client == nil {
		return errors.New("api session client is not available")
	}
	_, err := s.client.StopFeature(context.Background(), s.featureID)
	return err
}
func (s *apiSessionView) Interrupt() error { return s.Stop() }
func (s *apiSessionView) Wait()            {}

func (m APIAppModel) apiAttachTabsForFeature(featureID string) []repoTab {
	controlsBySession := m.apiPendingControlsBySession(featureID)
	summariesByID := map[string]server.SessionSummaryDTO{}
	var order []string
	addSummary := func(summary server.SessionSummaryDTO) {
		if summary.FeatureID != featureID || summary.ID == "" {
			return
		}
		if !apiSessionDTOIsActive(summary) && len(controlsBySession[summary.ID]) == 0 {
			return
		}
		if _, ok := summariesByID[summary.ID]; !ok {
			order = append(order, summary.ID)
		}
		summariesByID[summary.ID] = summary
	}
	for _, summary := range m.sessionList.Sessions {
		addSummary(summary)
	}
	for sessionID, controls := range controlsBySession {
		if sessionID == "" || len(controls) == 0 {
			continue
		}
		if _, ok := summariesByID[sessionID]; !ok {
			addSummary(apiSessionSummaryFromControl(featureID, controls[0]))
		}
	}
	if len(order) == 0 {
		preview, havePreview := m.livePreviews[featureID]
		if havePreview && preview.Session != nil {
			addSummary(*preview.Session)
		}
		if havePreview && len(order) == 0 && isLivePreviewEligible(m.apiDashboardFeatureByID(featureID)) {
			if tab, ok := apiLivePreviewFallbackTab(featureID, preview); ok {
				return []repoTab{tab}
			}
		}
	}

	tabs := make([]repoTab, 0, len(order))
	for _, sessionID := range order {
		summary := summariesByID[sessionID]
		detail := apiSessionDetailFromSummary(summary)
		detail.CanAttach = apiSessionDTOIsActive(summary)
		if cached, ok := m.sessionDetails[sessionID]; ok {
			detail = cached.Session
			if detail.ID == "" {
				detail = apiSessionDetailFromSummary(summary)
			}
		}
		transcript := server.TranscriptResponse{}
		if cached, ok := m.transcripts[sessionID]; ok {
			transcript = cached
		}
		controls := controlsBySession[sessionID]
		if len(controls) == 0 {
			controls = detail.PendingControls
		}
		sess := newAPIAttachSession(m.client, detail, transcript, controls, m.apiSessionWorkDir(featureID, detail.Repo))
		repoName := firstNonEmpty(sess.RepoName(), sess.Label(), sess.ID())
		if repoName == "" {
			repoName = sessionID
		}
		tabs = append(tabs, repoTab{
			repoName: repoName,
			label:    sess.Label(),
			kind:     sess.Kind(),
			sess:     sess,
			status:   apiAttachTabStatus(sess),
		})
	}
	return tabs
}

func apiLivePreviewFallbackTab(featureID string, preview server.LivePreviewResponse) (repoTab, bool) {
	presentation := apiLivePreviewPresentation(featureID, preview)
	sess := newAPILivePreviewSession(presentation)
	if sess == nil {
		return repoTab{}, false
	}
	repoName := firstNonEmpty(sess.RepoName(), sess.Label(), sess.ID(), "live-preview")
	return repoTab{
		repoName: repoName,
		label:    sess.Label(),
		kind:     sess.Kind(),
		sess:     sess,
		status:   apiAttachTabStatus(sess),
	}, true
}

func (m APIAppModel) apiPendingControlsBySession(featureID string) map[string][]server.ControlRequestDTO {
	out := map[string][]server.ControlRequestDTO{}
	add := func(req server.ControlRequestDTO) {
		if req.FeatureID != featureID || !isPendingControlStatus(req.Status) {
			return
		}
		out[req.SessionID] = append(out[req.SessionID], req)
	}
	for _, req := range m.prompts.AskUserQuestions {
		add(req)
	}
	for _, req := range m.permissions.Requests {
		add(req)
	}
	for _, detail := range m.sessionDetails {
		if detail.Session.FeatureID != featureID {
			continue
		}
		for _, req := range detail.Session.PendingControls {
			add(req)
		}
	}
	return out
}

func apiInitialAttachTab(tabs []repoTab) int {
	for i, tab := range tabs {
		if tab.sess != nil && firstPendingPermissionControlRequest(tab.sess) != nil {
			return i
		}
	}
	for i, tab := range tabs {
		if tab.sess != nil && firstPendingAskUserControlRequest(tab.sess) != nil {
			return i
		}
	}
	return resolveInitialTab(tabs, "")
}

func apiAttachTabStatus(sess session.SessionView) presentationStatus {
	if sess == nil {
		return statusPending
	}
	switch sess.Status() {
	case ports.SessionWaitingHelp, ports.SessionWaitingPermission:
		return statusWaiting
	case ports.SessionDone:
		return statusReviewPassed
	case ports.SessionFailed:
		return statusFailed
	default:
		return statusImplementing
	}
}

func newAPIAttachSession(client APIClient, detail server.SessionDetailDTO, transcript server.TranscriptResponse, controls []server.ControlRequestDTO, workDir ...string) *apiSessionView {
	log := session.NewMessageLog()
	firstIndex := -1
	lastIndex := -1
	lastRows := map[string]string{}
	lastTailKey := ""
	for _, row := range transcript.Messages {
		if msg, ok := apiTranscriptRowToSDKMessage(row, detail.ID); ok {
			log.Append(msg)
			if firstIndex == -1 || row.Index < firstIndex {
				firstIndex = row.Index
			}
			if row.Index > lastIndex {
				lastIndex = row.Index
				lastRows = map[string]string{}
			}
			if row.Index == lastIndex {
				key := apiTranscriptRowKey(row)
				lastRows[key] = apiTranscriptRowSignature(row)
				lastTailKey = key
			}
		}
	}
	status := apiSessionStatus(detail.Status)
	pending := apiControlRequestMessages(controls)
	if len(pending) > 0 {
		if firstPendingAskUserControlRequest(&apiSessionView{pending: pending}) != nil {
			status = ports.SessionWaitingHelp
		} else {
			status = ports.SessionWaitingPermission
		}
	}
	return &apiSessionView{
		id:                     detail.ID,
		featureID:              detail.FeatureID,
		phase:                  apiFeaturePhase(detail.Phase),
		repo:                   detail.Repo,
		kind:                   apiSessionKind(detail.Kind),
		label:                  detail.Label,
		status:                 status,
		startedAt:              detail.StartedAt,
		iteration:              detail.Iteration,
		provider:               detail.Provider,
		model:                  detail.Model,
		workDir:                firstNonEmpty(workDir...),
		initialPrompt:          detail.InitialPrompt,
		contextPct:             detail.ContextPct,
		log:                    log,
		cost:                   &llm.ResultMessage{TotalCostUSD: detail.Usage.CostUSD},
		client:                 client,
		statusCh:               make(chan string, 8),
		attachCh:               make(chan llm.SDKMessage, 64),
		doneCh:                 make(chan struct{}),
		pending:                pending,
		rememberPreviews:       apiRememberPreviewsByRequest(controls),
		lastTranscriptMessage:  lastIndex,
		firstTranscriptMessage: firstIndex,
		lastTranscriptRows:     lastRows,
		lastTranscriptTailKey:  lastTailKey,
	}
}

func newAPIChatSession(client APIClient, sessionID string) *apiSessionView {
	return &apiSessionView{
		id:                     sessionID,
		featureID:              chatSessionID,
		phase:                  feature.PhaseResearch,
		kind:                   ports.KindChat,
		label:                  ports.KindChat.String(),
		status:                 ports.SessionRunning,
		startedAt:              time.Now(),
		log:                    session.NewMessageLog(),
		client:                 client,
		statusCh:               make(chan string, 8),
		attachCh:               make(chan llm.SDKMessage, 64),
		doneCh:                 make(chan struct{}),
		lastTranscriptMessage:  -1,
		firstTranscriptMessage: -1,
		lastTranscriptRows:     map[string]string{},
	}
}

func apiSessionSummaryFromControl(featureID string, req server.ControlRequestDTO) server.SessionSummaryDTO {
	status := session.SessionWaitingPermission.String()
	if req.ToolName == toolNameAskUserQuestion {
		status = session.SessionWaitingHelp.String()
	}
	return server.SessionSummaryDTO{
		ID:        req.SessionID,
		FeatureID: featureID,
		Phase:     req.Phase,
		Kind:      "phase",
		Status:    status,
	}
}

func apiSessionDTOIsActive(summary server.SessionSummaryDTO) bool {
	status := apiSessionStatus(summary.Status)
	return status == ports.SessionRunning || status == ports.SessionWaitingHelp || status == ports.SessionWaitingPermission
}

func apiSessionDetailFromSummary(summary server.SessionSummaryDTO) server.SessionDetailDTO {
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

func apiSessionSummaryFromDetail(detail server.SessionDetailDTO) server.SessionSummaryDTO {
	return server.SessionSummaryDTO{
		ID:         detail.ID,
		FeatureID:  detail.FeatureID,
		Phase:      detail.Phase,
		Repo:       detail.Repo,
		Kind:       detail.Kind,
		Label:      detail.Label,
		Provider:   detail.Provider,
		Model:      detail.Model,
		Status:     detail.Status,
		TurnState:  detail.TurnState,
		StartedAt:  detail.StartedAt,
		Iteration:  detail.Iteration,
		ContextPct: detail.ContextPct,
		Usage:      detail.Usage,
	}
}

func apiSessionStatus(status string) ports.SessionStatus {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(status), "-", "_")) {
	case "waitingpermission", apiSessionStatusWaitingPermission:
		return ports.SessionWaitingPermission
	case "waitinghelp", "waiting_help", "waiting":
		return ports.SessionWaitingHelp
	case apiSessionStatusDone, apiSessionStatusCompleted, apiSessionStatusSuccess:
		return ports.SessionDone
	case string(statusFailed), apiSessionStatusError:
		return ports.SessionFailed
	default:
		return ports.SessionRunning
	}
}

func apiSessionKind(kind string) ports.SessionKind {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(kind), "-", "_")) {
	case "repo_impl":
		return ports.KindRepoImpl
	case ports.KindValidator.String():
		return ports.KindValidator
	case "review_helper":
		return ports.KindReviewHelper
	case ports.KindTweak.String():
		return ports.KindTweak
	case ports.KindChat.String():
		return ports.KindChat
	default:
		return ports.KindPhase
	}
}

func apiControlRequestMessages(requests []server.ControlRequestDTO) []*llm.ControlRequestMessage {
	out := make([]*llm.ControlRequestMessage, 0, len(requests))
	seen := map[string]bool{}
	for _, req := range requests {
		if req.RequestID == "" || seen[req.RequestID] {
			continue
		}
		seen[req.RequestID] = true
		out = append(out, apiControlRequestMessage(req))
	}
	return out
}

func apiRememberPreviewsByRequest(requests []server.ControlRequestDTO) map[string]server.PermissionRememberPreviewDTO {
	previews := make(map[string]server.PermissionRememberPreviewDTO)
	for _, req := range requests {
		if req.RequestID == "" || req.Remember == nil {
			continue
		}
		previews[req.RequestID] = *req.Remember
	}
	if len(previews) == 0 {
		return nil
	}
	return previews
}

func apiControlRequestMessage(req server.ControlRequestDTO) *llm.ControlRequestMessage {
	return &llm.ControlRequestMessage{
		Type:      msgTypeControlRequest,
		RequestID: req.RequestID,
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: req.ToolName,
			Input:    apiControlRequestInput(req),
		},
	}
}

func apiControlRequestInput(req server.ControlRequestDTO) json.RawMessage {
	if req.ToolName == toolNameAskUserQuestion {
		if len(req.Input) > 0 {
			if data, err := json.Marshal(req.Input); err == nil {
				return data
			}
		}
		type option struct {
			Label       string   `json:"label,omitempty"`
			Description string   `json:"description,omitempty"`
			Confidence  *float64 `json:"confidence,omitempty"`
		}
		type question struct {
			Question    string   `json:"question,omitempty"`
			Header      string   `json:"header,omitempty"`
			MultiSelect bool     `json:"multiSelect,omitempty"`
			Options     []option `json:"options,omitempty"`
		}
		envelope := struct {
			Questions []question `json:"questions"`
		}{}
		for _, q := range req.Questions {
			converted := question{
				Question:    q.Question,
				Header:      q.Header,
				MultiSelect: q.MultiSelect,
			}
			for _, opt := range q.Options {
				converted.Options = append(converted.Options, option{
					Label:       opt.Label,
					Description: opt.Description,
					Confidence:  opt.Confidence,
				})
			}
			envelope.Questions = append(envelope.Questions, converted)
		}
		data, _ := json.Marshal(envelope)
		return data
	}
	if len(req.Input) > 0 {
		if data, err := json.Marshal(req.Input); err == nil {
			return data
		}
	}
	data, _ := json.Marshal(map[string]string{"summary": req.Summary})
	return data
}

func cloneControlRequestMessage(req *llm.ControlRequestMessage) *llm.ControlRequestMessage {
	if req == nil {
		return nil
	}
	clone := *req
	clone.Request.Input = append(json.RawMessage(nil), req.Request.Input...)
	return &clone
}

func apiTranscriptRowToSDKMessage(row server.TranscriptMessageDTO, sessionID string) (llm.SDKMessage, bool) {
	switch row.Type {
	case blockTypeText:
		text := strings.TrimSpace(row.Text)
		if text == "" {
			return llm.SDKMessage{}, false
		}
		if row.Role == roleUser {
			return apiTranscriptMessageWithFileChange(llm.SDKMessage{
				Type:               roleUser,
				LocallyAppended:    row.LocallyAppended,
				AutoPicked:         row.AutoPicked,
				AutoPickQuestion:   row.AutoPickQuestion,
				AutoPickConfidence: row.AutoPickConfidence,
				User: &llm.UserMessage{
					Type:      roleUser,
					SessionID: sessionID,
					Message: llm.ConversationMsg{
						Role:    roleUser,
						Content: []llm.ContentBlock{{Type: blockTypeText, Text: text}},
					},
				},
			}, row), true
		}
		return apiTranscriptMessageWithFileChange(llm.SDKMessage{
			Type: roleAssistant,
			Assistant: &llm.AssistantMessage{
				Type:      roleAssistant,
				SessionID: sessionID,
				Message: llm.ConversationMsg{
					Role:    roleAssistant,
					Content: []llm.ContentBlock{{Type: blockTypeText, Text: text}},
				},
			},
		}, row), true
	case blockTypeToolUse:
		if row.Tool == "" {
			return llm.SDKMessage{}, false
		}
		block := llm.ContentBlock{Type: blockTypeToolUse, Name: row.Tool}
		if input := apiTranscriptToolCallInput(row.ToolCall); len(input) > 0 {
			block.Input = input
		}
		return apiTranscriptMessageWithFileChange(llm.SDKMessage{
			Type: roleAssistant,
			Assistant: &llm.AssistantMessage{
				Type:      roleAssistant,
				SessionID: sessionID,
				Message: llm.ConversationMsg{
					Role:    roleAssistant,
					Content: []llm.ContentBlock{block},
				},
			},
		}, row), true
	case transcriptTypeTaskStarted:
		if row.Task == nil {
			return llm.SDKMessage{}, false
		}
		return apiTranscriptMessageWithFileChange(llm.SDKMessage{
			Type:    roleSystem,
			Subtype: transcriptTypeTaskStarted,
			TaskStarted: &llm.TaskStartedMessage{
				Type:        roleSystem,
				Subtype:     transcriptTypeTaskStarted,
				TaskID:      row.Task.ID,
				ToolUseID:   row.Task.ToolUseID,
				Description: row.Task.Description,
				TaskType:    row.Task.TaskType,
				Prompt:      row.Task.Prompt,
				SessionID:   sessionID,
			},
		}, row), true
	case transcriptTypeTaskProgress:
		if row.Task == nil {
			return llm.SDKMessage{}, false
		}
		return apiTranscriptMessageWithFileChange(llm.SDKMessage{
			Type:    roleSystem,
			Subtype: transcriptTypeTaskProgress,
			TaskProgress: &llm.TaskProgressMessage{
				Type:         roleSystem,
				Subtype:      transcriptTypeTaskProgress,
				TaskID:       row.Task.ID,
				ToolUseID:    row.Task.ToolUseID,
				Description:  row.Task.Description,
				LastToolName: row.Task.LastToolName,
				SessionID:    sessionID,
			},
		}, row), true
	case transcriptTypeTaskNotification:
		if row.Task == nil {
			return llm.SDKMessage{}, false
		}
		return apiTranscriptMessageWithFileChange(llm.SDKMessage{
			Type:    roleSystem,
			Subtype: transcriptTypeTaskNotification,
			TaskNotification: &llm.TaskNotificationMessage{
				Type:       roleSystem,
				Subtype:    transcriptTypeTaskNotification,
				TaskID:     row.Task.ID,
				ToolUseID:  row.Task.ToolUseID,
				Status:     firstNonEmpty(row.Task.Status, row.Status),
				Summary:    row.Task.Summary,
				OutputFile: row.Task.OutputFile,
				SessionID:  sessionID,
			},
		}, row), true
	case transcriptTypeToolProgress:
		if row.Tool == "" {
			return llm.SDKMessage{}, false
		}
		return apiTranscriptMessageWithFileChange(llm.SDKMessage{
			Type: transcriptTypeToolProgress,
			ToolProgress: &llm.ToolProgressMessage{
				Type:      transcriptTypeToolProgress,
				ToolName:  row.Tool,
				Data:      row.Text,
				SessionID: sessionID,
			},
		}, row), true
	case transcriptTypeResult:
		subtype := firstNonEmpty(row.Status, "success")
		return apiTranscriptMessageWithFileChange(llm.SDKMessage{
			Type:   transcriptTypeResult,
			Result: &llm.ResultMessage{Type: transcriptTypeResult, Subtype: subtype, SessionID: sessionID},
		}, row), true
	case transcriptTypeStatus:
		return apiTranscriptMessageWithFileChange(llm.SDKMessage{
			Type:   transcriptTypeStatus,
			Status: &llm.StatusMessage{Type: transcriptTypeStatus, SessionID: sessionID, Message: row.Text},
		}, row), true
	default:
		return llm.SDKMessage{}, false
	}
}

func apiTranscriptToolCallInput(call *server.ToolCallDTO) json.RawMessage {
	if call == nil {
		return nil
	}
	payload := make(map[string]string)
	if strings.TrimSpace(call.Summary) != "" {
		payload["description"] = call.Summary
	}
	if strings.TrimSpace(call.Prompt) != "" {
		payload["prompt"] = call.Prompt
	}
	if len(payload) == 0 {
		return nil
	}
	data, _ := json.Marshal(payload)
	return data
}

func apiTranscriptRowKey(row server.TranscriptMessageDTO) string {
	fileChangeKey := ""
	if row.FileChange != nil {
		fileChangeKey = fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%t", row.FileChange.Path, row.FileChange.OldPath, row.FileChange.Operation, row.FileChange.Detail, row.FileChange.AddedLines, row.FileChange.RemovedLines, row.FileChange.HasDiffPatch)
	}
	toolCallKey := ""
	if row.ToolCall != nil {
		toolCallKey = fmt.Sprintf("%s\x00%s", row.ToolCall.Summary, row.ToolCall.Prompt)
	}
	taskKey := ""
	if row.Task != nil {
		taskKey = fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", row.Task.ID, row.Task.ToolUseID, row.Task.Description, row.Task.TaskType, row.Task.Prompt, row.Task.LastToolName, row.Task.Status, row.Task.Summary, row.Task.OutputFile)
	}
	return fmt.Sprintf("%d\x00%d\x00%s\x00%s\x00%s\x00%s\x00%t\x00%t\x00%t\x00%s\x00%.6f\x00%s\x00%s\x00%s", row.Index, row.BlockIndex, row.Role, row.Type, row.Tool, row.Status, row.Redacted, row.LocallyAppended, row.AutoPicked, row.AutoPickQuestion, row.AutoPickConfidence, fileChangeKey, toolCallKey, taskKey)
}

func apiTranscriptRowSignature(row server.TranscriptMessageDTO) string {
	return apiTranscriptRowKey(row) + "\x00" + row.Text
}

func apiTranscriptMessageWithFileChange(msg llm.SDKMessage, row server.TranscriptMessageDTO) llm.SDKMessage {
	if row.FileChange == nil || strings.TrimSpace(row.FileChange.Path) == "" {
		return msg
	}
	msg.FileChanges = []llm.FileChangeEvent{{
		Path:         row.FileChange.Path,
		OldPath:      row.FileChange.OldPath,
		Operation:    row.FileChange.Operation,
		Detail:       row.FileChange.Detail,
		AddedLines:   row.FileChange.AddedLines,
		RemovedLines: row.FileChange.RemovedLines,
		HasDiffPatch: row.FileChange.HasDiffPatch,
	}}
	return msg
}

func apiVisibleUserTranscriptRow(row server.TranscriptMessageDTO) bool {
	return row.Role == roleUser && row.Type == blockTypeText && row.LocallyAppended && strings.TrimSpace(row.Text) != ""
}

func apiHasLocalUserEcho(log ports.MessageLog, text string) bool {
	if log == nil {
		return false
	}
	want := strings.TrimSpace(text)
	if want == "" {
		return false
	}
	for _, msg := range log.Messages() {
		if !msg.LocallyAppended || msg.User == nil {
			continue
		}
		for _, block := range msg.User.Message.Content {
			if block.IsText() && strings.TrimSpace(block.Text) == want {
				return true
			}
		}
	}
	return false
}

func apiCanUpdateLastTranscriptLogMessage(log ports.MessageLog) bool {
	if log == nil {
		return false
	}
	messages := log.Messages()
	if len(messages) == 0 {
		return true
	}
	return !messages[len(messages)-1].LocallyAppended
}

func (m APIAppModel) applyAPIAttachRefreshSnapshot(snapshot server.RefreshSnapshot) (APIAppModel, tea.Cmd) {
	if m.attach == nil {
		return m, nil
	}
	active, ok := m.attach.sess.(*apiSessionView)
	if !ok || active == nil {
		return m, nil
	}
	var cmds []tea.Cmd
	detail := apiSessionDetailFromSummary(server.SessionSummaryDTO{
		ID:         active.ID(),
		FeatureID:  active.FeatureID(),
		Phase:      active.Phase().String(),
		Repo:       active.RepoName(),
		Kind:       active.Kind().String(),
		Label:      active.Label(),
		Status:     active.Status().String(),
		StartedAt:  active.StartedAt(),
		Iteration:  active.Iteration(),
		ContextPct: active.ContextPercentage(),
	})
	var transcript *server.TranscriptResponse
	updateActive := true
	if snapshot.Session != nil {
		if snapshot.Session.Session.ID != active.ID() {
			updateActive = false
		} else {
			detail = snapshot.Session.Session
			transcript = snapshot.Transcript
		}
	}
	if updateActive {
		var updateCmd tea.Cmd
		m, updateCmd = m.applyAPIActiveAttachUpdate(active, detail, transcript)
		if updateCmd != nil {
			cmds = append(cmds, updateCmd)
		}
	}
	var rebuildCmd tea.Cmd
	m, rebuildCmd = m.rebuildAPIAttachTabs()
	if rebuildCmd != nil {
		cmds = append(cmds, rebuildCmd)
	}
	return m, tea.Batch(cmds...)
}

func (m APIAppModel) applyAPIAttachSessionsSnapshot(msg apiAttachSessionsSnapshotMsg) (APIAppModel, tea.Cmd) {
	if msg.err != nil {
		m.statusMessage = "Session refresh failed: " + firstLine(msg.err.Error())
		return m, nil
	}
	m.ApplyRefreshSnapshot(server.RefreshSnapshot{Sessions: &msg.sessions})
	if m.attach != nil && m.attach.featureID == msg.featureID {
		var cmds []tea.Cmd
		var rebuildCmd tea.Cmd
		m, rebuildCmd = m.rebuildAPIAttachTabs()
		if rebuildCmd != nil {
			cmds = append(cmds, rebuildCmd)
		}
		if detailCmd := m.fetchAttachMissingSessionDetailsCmd(msg.featureID); detailCmd != nil {
			cmds = append(cmds, detailCmd)
		}
		return m, tea.Batch(cmds...)
	}
	if msg.openIfClosed {
		model, cmd := m.openAPIAttachForFeatureFromCache(msg.featureID, false)
		if updated, ok := model.(APIAppModel); ok {
			return updated, cmd
		}
		return m, cmd
	}
	return m, nil
}

// applyAPIActiveAttachUpdate applies a refreshed session detail/transcript to the
// active attach session, updating attach messages, pending questions/permissions,
// and the viewport as needed.
func (m APIAppModel) applyAPIActiveAttachUpdate(active *apiSessionView, detail server.SessionDetailDTO, transcript *server.TranscriptResponse) (APIAppModel, tea.Cmd) {
	controls := detail.PendingControls
	if len(controls) == 0 {
		if bySession := m.apiPendingControlsBySession(active.FeatureID()); len(bySession) > 0 {
			controls = bySession[active.ID()]
		}
	}
	initialPromptBefore := active.InitialPrompt()
	newMessages := active.applyAPISessionSnapshot(detail, transcript, controls)
	initialPromptChanged := active.InitialPrompt() != initialPromptBefore
	var cmd tea.Cmd
	if len(newMessages) > 0 {
		updated, updateCmd := m.attach.Update(attachMsgsMsg{generation: m.attach.tabGeneration, messages: newMessages})
		m.attach = &updated
		cmd = updateCmd
	}
	switch {
	case len(controls) > 0:
		m.attach.restorePendingAskUserQuestions(active)
		if !m.attach.HasActiveQuestion() {
			m.attach.restorePendingPermission(active)
		}
		m.attach.updateViewport()
	case initialPromptChanged:
		m.attach.updateViewport()
	}
	return m, cmd
}

func (m APIAppModel) rebuildAPIAttachTabs() (APIAppModel, tea.Cmd) {
	if m.attach == nil {
		return m, nil
	}
	next := m.apiAttachTabsForFeature(m.attach.featureID)
	if len(next) == 0 {
		m.attach = nil
		m.statusMessage = statusMsgNoActiveSessions
		m.stopLiveSessionOutput()
		return m, nil
	}
	if !m.attach.rebuildTabs(next) {
		return m, nil
	}
	idx := m.attach.activeTabIdx
	if idx < 0 || idx >= len(m.attach.repoTabs) || m.attach.repoTabs[idx].sess == nil {
		return m, nil
	}
	var cmd tea.Cmd
	updated, cmd := m.attach.switchToTab(idx)
	m.attach = &updated
	liveCmd := m.syncLiveSessionOutputForAttach()
	return m, tea.Batch(cmd, liveCmd)
}

func (m APIAppModel) applyAPIChatRefreshSnapshot(snapshot server.RefreshSnapshot) APIAppModel {
	if !m.chatReady || m.chat.sess == nil {
		return m
	}
	active, ok := m.chat.sess.(*apiSessionView)
	if !ok || active == nil {
		return m
	}
	var detail server.SessionDetailDTO
	var transcript *server.TranscriptResponse
	if snapshot.Session != nil {
		if snapshot.Session.Session.ID != active.ID() {
			return m
		}
		detail = snapshot.Session.Session
		transcript = snapshot.Transcript
	} else {
		controls := m.apiPendingControlsBySession(active.FeatureID())[active.ID()]
		if len(controls) == 0 {
			return m
		}
		detail = apiSessionDetailFromSummary(server.SessionSummaryDTO{
			ID:        active.ID(),
			FeatureID: active.FeatureID(),
			Phase:     active.Phase().String(),
			Status:    active.Status().String(),
		})
		detail.PendingControls = controls
	}
	controls := detail.PendingControls
	if len(controls) == 0 {
		if bySession := m.apiPendingControlsBySession(active.FeatureID()); len(bySession) > 0 {
			controls = bySession[active.ID()]
			detail.PendingControls = controls
		}
	}
	events := apiChatEventsFromSnapshot(apiChatSnapshotInput{
		Session:                active,
		Detail:                 detail,
		Transcript:             transcript,
		Controls:               controls,
		WasResponding:          m.chat.responding,
		HasInProgressAgentText: m.chat.hasInProgressAgentText(),
	})
	if len(events) == 0 {
		return m
	}
	m.chat = m.chat.ApplyEvents(events)
	if m.chat.fullscreen {
		m.chat = m.chat.resize(m.width, m.height)
	} else {
		m.chat = m.chat.resize(m.width, m.chat.chatPanelHeight(m.height))
	}
	return m
}

// apiSessionStateUpdate captures the caller-specific deltas layered on top of
// applyAPISessionState's shared status/contextPct/pending handling.
type apiSessionStateUpdate struct {
	// useTurnState allows detail.TurnState to override the status derived
	// from detail.Status. Used by the live-chat-adapter path only.
	useTurnState bool
	// forceInitialPrompt applies detail.InitialPrompt unconditionally, even
	// when empty. Used by the live-chat-adapter path; the REST snapshot path
	// only applies InitialPrompt when it is non-empty.
	forceInitialPrompt bool
	// setRememberPreviews applies apiRememberPreviewsByRequest(controls).
	// Used by the REST snapshot path only.
	setRememberPreviews bool
}

// applyAPISessionState updates status, contextPct, initialPrompt, and pending
// controls shared by the REST snapshot-refresh path (applyAPISessionSnapshot)
// and the live-chat-adapter path (applyAPIChatSessionState). opt controls the
// small behavioral differences between the two callers.
func (s *apiSessionView) applyAPISessionState(detail server.SessionDetailDTO, controls []server.ControlRequestDTO, opt apiSessionStateUpdate) {
	pending := apiControlRequestMessages(controls)
	hasAskUser := false
	for _, req := range pending {
		if req != nil && req.Request.ToolName == toolNameAskUserQuestion {
			hasAskUser = true
			break
		}
	}
	s.mu.Lock()
	s.status = apiSessionStatus(detail.Status)
	if opt.useTurnState {
		if status, ok := apiSessionStatusFromTurnState(detail.TurnState); ok {
			s.status = status
		}
	}
	s.contextPct = detail.ContextPct
	if opt.forceInitialPrompt || detail.InitialPrompt != "" {
		s.initialPrompt = detail.InitialPrompt
	}
	s.pending = pending
	if opt.setRememberPreviews {
		s.rememberPreviews = apiRememberPreviewsByRequest(controls)
	}
	if len(s.pending) > 0 {
		if hasAskUser {
			s.status = ports.SessionWaitingHelp
		} else {
			s.status = ports.SessionWaitingPermission
		}
	}
	s.mu.Unlock()
}

func (s *apiSessionView) applyAPISessionSnapshot(detail server.SessionDetailDTO, transcript *server.TranscriptResponse, controls []server.ControlRequestDTO) []llm.SDKMessage {
	s.applyAPISessionState(detail, controls, apiSessionStateUpdate{setRememberPreviews: true})
	if transcript == nil {
		return nil
	}
	var messages []llm.SDKMessage
	for _, row := range transcript.Messages {
		if msg := s.applyTranscriptRow(row); msg != nil {
			messages = append(messages, *msg)
		}
	}
	return messages
}

// applyTranscriptRow reconciles one transcript row against the session's
// message log using the same key/signature watermark applyAPISessionSnapshot
// already tracked before this refactor. Returns the SDKMessage that was
// appended or updated, or nil if the row was a no-op (already applied with
// an identical signature, or a local-user-echo row). Shared by the
// snapshot-refresh pull path and the live-stream push path so both use
// exactly one dedup mechanism.
//
// The snapshot-refresh path runs synchronously on bubbletea's main Update
// goroutine, but the live-stream path runs inside listenLiveSessionOutputCmd's
// returned closure — its own tea.Cmd goroutine — so the two callers are not
// otherwise serialized against each other. s.mu (already used to guard
// status/contextPct/pending) also guards the watermark fields mutated below.
// s.log's own methods take their own internal mutex, so calling them while
// holding s.mu is safe.
func (s *apiSessionView) applyTranscriptRow(row server.TranscriptMessageDTO) *llm.SDKMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row.Index < s.lastTranscriptMessage {
		return nil
	}
	if s.lastTranscriptRows == nil {
		s.lastTranscriptRows = map[string]string{}
	}
	msg, ok := apiTranscriptRowToSDKMessage(row, s.id)
	if !ok {
		return nil
	}
	key := apiTranscriptRowKey(row)
	signature := apiTranscriptRowSignature(row)
	if apiVisibleUserTranscriptRow(row) && apiHasLocalUserEcho(s.log, row.Text) {
		if row.Index > s.lastTranscriptMessage {
			s.lastTranscriptMessage = row.Index
			s.lastTranscriptRows = map[string]string{}
		}
		if row.Index == s.lastTranscriptMessage {
			s.lastTranscriptRows[key] = signature
			s.lastTranscriptTailKey = key
		}
		return nil
	}
	if row.Index > s.lastTranscriptMessage {
		s.log.Append(msg)
		if s.firstTranscriptMessage == -1 || row.Index < s.firstTranscriptMessage {
			s.firstTranscriptMessage = row.Index
		}
		s.lastTranscriptMessage = row.Index
		s.lastTranscriptRows = map[string]string{key: signature}
		s.lastTranscriptTailKey = key
		return &msg
	}
	if previous, ok := s.lastTranscriptRows[key]; ok {
		if previous == signature {
			return nil
		}
		if key == s.lastTranscriptTailKey && apiCanUpdateLastTranscriptLogMessage(s.log) {
			s.log.UpdateLast(msg)
		} else {
			s.log.Append(msg)
			s.lastTranscriptTailKey = key
		}
		s.lastTranscriptRows[key] = signature
		return &msg
	}
	s.log.Append(msg)
	if s.firstTranscriptMessage == -1 || row.Index < s.firstTranscriptMessage {
		s.firstTranscriptMessage = row.Index
	}
	s.lastTranscriptRows[key] = signature
	s.lastTranscriptTailKey = key
	return &msg
}

func (s *apiSessionView) firstLoadedTranscriptIndex() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstTranscriptMessage
}

func (s *apiSessionView) applyTranscriptBackfill(transcript server.TranscriptResponse) []llm.SDKMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.log == nil {
		s.log = session.NewMessageLog()
	}
	limit := s.firstTranscriptMessage
	if limit < 0 {
		limit = transcript.Cursor.End
	}
	var messages []llm.SDKMessage
	firstLoaded := s.firstTranscriptMessage
	for _, row := range transcript.Messages {
		if limit >= 0 && row.Index >= limit {
			continue
		}
		msg, ok := apiTranscriptRowToSDKMessage(row, s.id)
		if !ok {
			continue
		}
		messages = append(messages, msg)
		if firstLoaded == -1 || row.Index < firstLoaded {
			firstLoaded = row.Index
		}
	}
	if len(messages) > 0 {
		s.log.Prepend(messages)
	}
	if transcript.Cursor.Start >= 0 && (firstLoaded == -1 || transcript.Cursor.Start < firstLoaded) {
		firstLoaded = transcript.Cursor.Start
	}
	s.firstTranscriptMessage = firstLoaded
	return messages
}

func (m APIAppModel) Close() {
	if m.cancelEvents != nil {
		m.cancelEvents()
	}
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
		m.upsertFeatureSummary(apiFeatureDetailSummary(snapshot.Feature.Feature))
	}
	if snapshot.Session != nil {
		m.storeSessionDetail(*snapshot.Session)
		m.upsertSessionSummary(apiSessionSummaryFromDetail(snapshot.Session.Session))
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
	featureID := detail.Feature.ID
	newRun := apiActiveRunNumber(detail.Feature)
	if existing, ok := m.featureDetails[featureID]; ok {
		oldRun := apiActiveRunNumber(existing.Feature)
		if oldRun > 0 && newRun > 0 && oldRun != newRun {
			delete(m.contents, featureID)
		}
	} else if content, ok := m.contents[featureID]; ok && content.RunNumber > 0 && newRun > 0 && content.RunNumber != newRun {
		delete(m.contents, featureID)
	}
	m.featureDetails[featureID] = detail
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
	if existing, ok := m.livePreviews[featureID]; ok && sameLivePreviewSessionInstance(existing, preview) {
		preview.Transcript = mergeLivePreviewTranscript(existing.Transcript, preview.Transcript)
	}
	m.livePreviews[featureID] = preview
}

func livePreviewResponseSessionID(preview server.LivePreviewResponse) string {
	if preview.Session == nil {
		return ""
	}
	return strings.TrimSpace(preview.Session.ID)
}

func sameLivePreviewSessionInstance(existing, incoming server.LivePreviewResponse) bool {
	existingID := livePreviewResponseSessionID(existing)
	if existingID == "" || existingID != livePreviewResponseSessionID(incoming) {
		return false
	}
	if existing.Session != nil && incoming.Session != nil &&
		!existing.Session.StartedAt.IsZero() && !incoming.Session.StartedAt.IsZero() {
		return existing.Session.StartedAt.Equal(incoming.Session.StartedAt)
	}
	return true
}

func mergeLivePreviewTranscript(existing, incoming []server.TranscriptMessageDTO) []server.TranscriptMessageDTO {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}
	if len(existing) == 0 {
		return trimLivePreviewTranscript(incoming)
	}
	if len(incoming) == 0 {
		return trimLivePreviewTranscript(existing)
	}
	overlap := livePreviewTranscriptOverlap(existing, incoming)
	merged := make([]server.TranscriptMessageDTO, 0, len(existing)+len(incoming)-overlap)
	merged = append(merged, existing[:len(existing)-overlap]...)
	merged = append(merged, incoming...)
	return trimLivePreviewTranscript(merged)
}

func livePreviewTranscriptOverlap(existing, incoming []server.TranscriptMessageDTO) int {
	maxOverlap := min(len(existing), len(incoming))
	for overlap := maxOverlap; overlap > 0; overlap-- {
		matches := true
		existingStart := len(existing) - overlap
		for i := 0; i < overlap; i++ {
			if !livePreviewTranscriptRowsOverlap(existing[existingStart+i], incoming[i]) {
				matches = false
				break
			}
		}
		if matches {
			return overlap
		}
	}
	return 0
}

func livePreviewTranscriptRowsOverlap(existing, incoming server.TranscriptMessageDTO) bool {
	if livePreviewTranscriptContentKey(existing) == livePreviewTranscriptContentKey(incoming) {
		return true
	}
	return apiTranscriptRowKey(existing) == apiTranscriptRowKey(incoming)
}

func livePreviewTranscriptContentKey(row server.TranscriptMessageDTO) string {
	row.Index = 0
	return apiTranscriptRowSignature(row)
}

func trimLivePreviewTranscript(rows []server.TranscriptMessageDTO) []server.TranscriptMessageDTO {
	if len(rows) > livePreviewTranscriptMessageLimit {
		rows = rows[len(rows)-livePreviewTranscriptMessageLimit:]
	}
	return rows
}

func (m *APIAppModel) storeTranscript(sessionID string, transcript server.TranscriptResponse) {
	if sessionID == "" {
		return
	}
	if m.transcripts == nil {
		m.transcripts = map[string]server.TranscriptResponse{}
	}
	existing, ok := m.transcripts[sessionID]
	if !ok || transcriptResponseCoversFullRange(transcript) || transcript.Cursor.Start > existing.Cursor.End {
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

func transcriptResponseCoversFullRange(transcript server.TranscriptResponse) bool {
	return transcript.Cursor.Start == 0 && transcript.Cursor.Total > 0 && transcript.Cursor.End >= transcript.Cursor.Total
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

// apiLivePreviewAndTranscript returns the live preview presentation for the
// selected feature and, if its session has a cached transcript, the
// transcript presentation for that session.
func (m APIAppModel) apiLivePreviewAndTranscript(selected string) (*APILivePreviewPresentation, *APITranscriptPresentation) {
	preview, ok := m.livePreviews[selected]
	if !ok {
		return nil, nil
	}
	presentation := apiLivePreviewPresentation(selected, preview)
	livePreview := &presentation
	var transcript *APITranscriptPresentation
	resp, ok := m.transcripts[livePreview.SessionID]
	if livePreview.SessionID != "" && ok {
		tpresentation := apiTranscriptPresentation(livePreview.SessionID, resp)
		transcript = &tpresentation
	}
	return livePreview, transcript
}

func (m *APIAppModel) rebuildPresentation(preferredFeatureID string) {
	attention := apiAttentionCounts(m.featureList.Features, m.prompts, m.permissions)
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
		if len(features) > 0 && m.selectedSection == "" {
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
		livePreview, transcript = m.apiLivePreviewAndTranscript(selected)
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
	if selected != "" {
		m.selectedSection = ""
	}
}

func newAPIRecoveryPanel(snapshot server.RecoverySnapshotResponse) *apiRecoveryPanel {
	actions := make(map[string]string, len(snapshot.Items))
	items := append([]server.RecoveryItemDTO(nil), snapshot.Items...)
	for i, item := range items {
		if len(item.AllowedActions) == 0 {
			if item.Tweak {
				item.AllowedActions = []string{recoveryActionKill}
			} else {
				item.AllowedActions = []string{recoveryActionResume, recoveryActionKill, recoveryActionSkip}
			}
			items[i] = item
		}
		action := item.DefaultAction
		if action == "" {
			action = recoveryActionSkip
			if item.Tweak {
				action = recoveryActionKill
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

// moveCursor shifts *cursor by delta, clamped to [0, length).
func moveCursor(cursor *int, delta, length int) {
	next := *cursor + delta
	if next >= 0 && next < length {
		*cursor = next
	}
}

func (m APIAppModel) handleAPIRecoveryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	panel := m.recoveryPanel
	if panel == nil {
		return m, nil
	}
	switch strings.ToLower(msg.Text) {
	case "r":
		panel.setAction(recoveryActionResume)
		return m, nil
	case "k":
		panel.setAction(recoveryActionKill)
		return m, nil
	case "s":
		panel.setAction(recoveryActionSkip)
		return m, nil
	}
	switch msg.Code {
	case tea.KeyUp:
		moveCursor(&panel.cursor, -1, len(panel.items))
		return m, nil
	case tea.KeyDown:
		moveCursor(&panel.cursor, 1, len(panel.items))
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

const (
	recoveryLabelResume = "[R]esume"
	recoveryLabelKill   = "[K]ill"
	recoveryLabelSkip   = "[S]kip"
)

func apiRecoveryActionLabel(action string) string {
	switch action {
	case recoveryActionResume:
		return recoveryLabelResume
	case recoveryActionKill:
		return recoveryLabelKill
	case recoveryActionSkip:
		return recoveryLabelSkip
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
		case recoveryActionResume:
			parts = append(parts, "[r] Resume")
		case recoveryActionKill:
			parts = append(parts, "[k] Kill")
		case recoveryActionSkip:
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

func (m *APIAppModel) removeFeatureState(featureID string) {
	if featureID == "" {
		return
	}
	m.featureList.Features = slices.DeleteFunc(m.featureList.Features, func(summary server.FeatureSummary) bool {
		return summary.ID == featureID
	})
	delete(m.featureDetails, featureID)
	delete(m.livePreviews, featureID)
	delete(m.contents, featureID)

	removedSessionIDs := map[string]struct{}{}
	m.sessionList.Sessions = slices.DeleteFunc(m.sessionList.Sessions, func(summary server.SessionSummaryDTO) bool {
		if summary.FeatureID != featureID {
			return false
		}
		if summary.ID != "" {
			removedSessionIDs[summary.ID] = struct{}{}
		}
		return true
	})
	for sessionID := range removedSessionIDs {
		delete(m.sessionDetails, sessionID)
		delete(m.transcripts, sessionID)
	}

	m.prompts.AskUserQuestions = slices.DeleteFunc(m.prompts.AskUserQuestions, func(req server.ControlRequestDTO) bool {
		return req.FeatureID == featureID
	})
	m.prompts.HelpQueue = slices.DeleteFunc(m.prompts.HelpQueue, func(item server.HelpQueueDTO) bool {
		return item.FeatureID == featureID
	})
	m.prompts.NeedUserInputs = slices.DeleteFunc(m.prompts.NeedUserInputs, func(gate server.NeedInputGateDTO) bool {
		return gate.FeatureID == featureID
	})
	m.permissions.Requests = slices.DeleteFunc(m.permissions.Requests, func(req server.ControlRequestDTO) bool {
		return req.FeatureID == featureID
	})
	m.recovery.Items = slices.DeleteFunc(m.recovery.Items, func(item server.RecoveryItemDTO) bool {
		return item.FeatureID == featureID
	})
	if m.recoveryPanel != nil {
		m.recoveryPanel = mergeAPIRecoveryPanel(m.recoveryPanel, m.recovery)
	}
	if m.reviewComments != nil && m.reviewComments.featureID == featureID {
		m.reviewComments = nil
	}
	if m.configEditor != nil && m.configEditor.featureID == featureID {
		m.configEditor = nil
	}
	if m.refactorPrompt != nil && m.refactorPrompt.featureID == featureID {
		m.refactorPrompt = nil
	}
	if m.refactorPipeline != nil && m.refactorPipeline.featureID == featureID {
		m.refactorPipeline = nil
	}
	if m.needInputFeatureID == featureID {
		m.clearNeedInputPrompt()
	}
	if m.permissionFeatureID == featureID {
		m.clearPermissionPrompt()
	}
	if m.helpFeatureID == featureID {
		m.clearHelpPrompt()
	}
	if m.askUserFeatureID == featureID {
		m.clearAskUserPrompt()
	}
	if m.actionConfirmFeatureID == featureID {
		m.clearActionConfirm()
	}
	if m.tweakReviewFeatureID == featureID {
		m.clearTweakReviewModal()
	}
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

func (m APIAppModel) handleAPIDashboardListKey(msg tea.KeyPressMsg) (APIAppModel, tea.Cmd) {
	previousFeature := m.selectedFeature
	previousCollapsed := append([]string(nil), m.runtimeConfig.UI.CollapsedSections...)
	dashboard := m.apiDashboardModel()

	switch msg.Code {
	case tea.KeyUp:
		if dashboard.focusPanel == 1 {
			dashboard.MoveToAdjacentFeature(-1)
		} else {
			dashboard, _ = dashboard.Update(msg)
		}
	case tea.KeyDown:
		if dashboard.focusPanel == 1 {
			dashboard.MoveToAdjacentFeature(1)
		} else {
			dashboard, _ = dashboard.Update(msg)
		}
	case tea.KeyEnter:
		dashboard, _ = dashboard.Update(msg)
		if dashboard.ConsumeWantNewFeature() {
			return m.openCreateFeaturePrompt(0), nil
		}
	default:
		return m, nil
	}

	m = m.applyAPIDashboardListState(dashboard)
	m.rebuildPresentation(m.selectedFeature)
	var cmds []tea.Cmd
	if msg.Code == tea.KeyEnter && !slices.Equal(previousCollapsed, m.runtimeConfig.UI.CollapsedSections) {
		cmds = append(cmds, m.persistRuntimeUICmd(m.runtimeConfig.UI))
	}
	if m.selectedFeature != "" && m.selectedFeature != previousFeature {
		cmds = append(cmds, m.fetchFeatureDetailCmd(m.selectedFeature))
	}
	return m, tea.Batch(cmds...)
}

func (m APIAppModel) applyAPIDashboardListState(dashboard DashboardModel) APIAppModel {
	m.focusPanel = dashboard.focusPanel
	m.rightPanelMode = dashboard.rightPanelMode
	if f := dashboard.SelectedFeature(); f != nil {
		m.selectedFeature = f.ID
		m.selectedSection = ""
	} else {
		m.selectedFeature = ""
		m.selectedSection = dashboard.SelectedSection()
	}
	m.runtimeConfig.UI.CollapsedSections = dashboard.CollapsedSectionsList()
	return m
}

func (m APIAppModel) updateAPIWelcome(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.welcome == nil {
		return m, nil
	}
	welcome, cmd := m.welcome.Update(msg)
	m.welcome = &welcome
	var cmds []tea.Cmd
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	if root := m.welcome.ConsumePendingRoot(); root != "" {
		roots := append([]string(nil), m.runtimeConfig.WorkspaceRoots...)
		if !containsRootExpanded(roots, root) {
			roots = append(roots, root)
			m.runtimeConfig.WorkspaceRoots = roots
			cmds = append(cmds, m.persistRuntimeWorkspaceRootsCmd(roots, ""))
		}
	}

	if m.welcome.IsDone() {
		if m.welcome.IsCancelled() {
			m.welcomeSkipped = true
			m.statusMessage = "You can add workspace roots later by pressing W"
		} else {
			m.statusMessage = ""
		}
		m.welcome = nil
		m.rebuildPresentation(m.selectedFeature)
	}
	return m, tea.Batch(cmds...)
}

func (m APIAppModel) confirmSelectedFeatureAction(kind string) APIAppModel {
	return m.confirmSelectedFeatureActionWithArgs(kind, apiFeatureActionArgs{})
}

func (m APIAppModel) confirmSelectedFeatureActionWithArgs(kind string, args apiFeatureActionArgs) APIAppModel {
	if m.selectedFeature == "" {
		return m
	}
	if blocked, message := m.selectedFeatureActionLocallyBlocked(kind); blocked {
		m.statusMessage = message
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
	if !m.selectedActionReady(mutationKindFeatureTweakFinish) {
		m.statusMessage = apiMutationKindLabel(mutationKindFeatureTweakFinish) + " is unavailable"
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

func (m *APIAppModel) clearResolvedControlRequest(requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	m.prompts.AskUserQuestions = slices.DeleteFunc(m.prompts.AskUserQuestions, func(req server.ControlRequestDTO) bool {
		return req.RequestID == requestID
	})
	m.permissions.Requests = slices.DeleteFunc(m.permissions.Requests, func(req server.ControlRequestDTO) bool {
		return req.RequestID == requestID
	})
	for id, detail := range m.sessionDetails {
		detail.Session.PendingControls = slices.DeleteFunc(detail.Session.PendingControls, func(req server.ControlRequestDTO) bool {
			return req.RequestID == requestID
		})
		m.sessionDetails[id] = detail
	}
	if m.permissionRequest.RequestID == requestID {
		m.clearPermissionPrompt()
	}
	if m.askUserRequest.RequestID == requestID {
		m.clearAskUserPrompt()
	}
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

func (m APIAppModel) apiCtx() context.Context {
	ctx := context.Background()
	if m.eventCtx != nil {
		ctx = m.eventCtx
	}
	return ctx
}

func (m APIAppModel) persistRuntimeConfigCmd(req server.RuntimeConfigMutationRequest, createdRepoPath string) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		_, err := m.client.UpdateRuntimeConfig(ctx, req)
		if err != nil {
			return apiRuntimeConfigMutationMsg{kind: mutationKindRuntimeConfigUpdate, createdRepoPath: createdRepoPath, err: err}
		}
		cfg, err := m.client.RuntimeConfig(ctx)
		return apiRuntimeConfigMutationMsg{
			kind:            mutationKindRuntimeConfigUpdate,
			config:          cfg,
			createdRepoPath: createdRepoPath,
			err:             err,
		}
	}
}

func (m APIAppModel) persistRuntimeWorkspaceRootsCmd(roots []string, createdRepoPath string) tea.Cmd {
	roots = append([]string(nil), roots...)
	return m.persistRuntimeConfigCmd(server.RuntimeConfigMutationRequest{WorkspaceRoots: &roots}, createdRepoPath)
}

func (m APIAppModel) persistRuntimeUICmd(ui config.UIConfig) tea.Cmd {
	return m.persistRuntimeConfigCmd(server.RuntimeConfigMutationRequest{UI: &ui}, "")
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

// requireSelectedFeature reports whether a feature is currently selected. If
// not, it sets the standard "no feature selected" status message so the
// caller can bail out with that message already in place.
func (m *APIAppModel) requireSelectedFeature() bool {
	if m.selectedFeature != "" {
		return true
	}
	m.statusMessage = statusMsgNoFeatureSelected
	return false
}

func (m APIAppModel) openNeedInputPrompt() APIAppModel {
	if !m.requireSelectedFeature() {
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
	if !m.requireSelectedFeature() {
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
	if !m.requireSelectedFeature() {
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
	if !m.requireSelectedFeature() {
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
	if !m.requireSelectedFeature() {
		return m
	}
	f := m.selectedAPIDashboardFeature()
	if f == nil || f.Status != feature.StatusCodeReady || f.Checkpoints.AutoPublish() || !f.IsPublishable() {
		m.statusMessage = apiMutationKindLabel(mutationKindFeaturePublish) + " is unavailable"
		return m
	}
	if saved, ok := m.loadSavedFeatureForDiff(f.ID); ok {
		f = apiMergeSavedPublishFeature(f, saved)
	}
	apiCommitSingleRepoForPublishPreview(f)
	repos := apiPublishRepoEntries(f)
	if len(repos) == 0 {
		m.statusMessage = "Publish is unavailable: no repos configured"
		return m
	}
	planText := apiPublishPlanText(f)
	publish := NewPublishModel(f, repos, planText, f.Models.Planning, m.width, m.height)
	publish.prCtx = apiPublishPRContext(f, publish.worktreeDir, publish.baseBranch, planText)
	publish.runDesc = func(ctx context.Context, model string, prCtx agent.PRContext) (string, string, error) {
		return m.runAPIPublishDescription(ctx, f.ID, model, prCtx)
	}
	publish.publishable = f.IsPublishable()
	publish.spinnerView = m.spinnerView()
	m.publish = &publish
	m.statusMessage = ""
	return m
}

func (m APIAppModel) handleAPIPublishKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.publish == nil {
		return m, nil
	}
	if m.publish.step == publishStepConfirm && key.Matches(msg, keys.Enter) {
		args := apiFeatureActionArgs{
			Title: strings.TrimSpace(m.publish.prTitle),
			Body:  strings.TrimSpace(m.publish.prBody),
		}
		if m.publish.hasRepoSelect && strings.TrimSpace(m.publish.repoName) != "" {
			args.Repos = []string{m.publish.repoName}
		}
		m.publish.step = publishStepExecute
		return m, m.selectedFeatureActionCmd(mutationKindFeaturePublish, m.publish.featureID, args)
	}

	updated, cmd := m.publish.Update(msg)
	if updated.IsDone() {
		m.publish = nil
		return m, nil
	}
	m.publish = &updated
	return m, cmd
}

func (m APIAppModel) runAPIPublishDescription(ctx context.Context, featureID, model string, prCtx agent.PRContext) (string, string, error) {
	resp, err := m.client.GeneratePublishDescription(ctx, featureID, server.PublishDescriptionRequest{
		Model:              model,
		FeatureName:        prCtx.FeatureName,
		FeatureDescription: prCtx.FeatureDescription,
		Roadmap:            prCtx.Roadmap,
		CommitBodies:       prCtx.CommitBodies,
		DiffStat:           prCtx.DiffStat,
	})
	title := strings.TrimSpace(resp.Title)
	body := strings.TrimSpace(resp.Body)
	if title == "" || body == "" {
		fallbackTitle, fallbackBody := agent.BuildPRDescriptionFallback(prCtx)
		if title == "" {
			title = fallbackTitle
		}
		if body == "" {
			body = fallbackBody
		}
	}
	return title, body, err
}

func (m APIAppModel) openRepoCycleAction(kind string) APIAppModel {
	if !m.requireSelectedFeature() {
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
	if !m.requireSelectedFeature() {
		return m
	}
	if blocked, message := m.selectedFeatureActionLocallyBlocked(mutationKindFeatureRewind); blocked {
		m.statusMessage = message
		return m
	}
	if !m.selectedActionReady(mutationKindFeatureRewind) {
		m.statusMessage = apiMutationKindLabel(mutationKindFeatureRewind) + " is unavailable"
		return m
	}
	choices := m.selectedRewindChoices()
	if len(choices) == 0 {
		m.statusMessage = "selected feature has no rewind target phase"
		return m
	}
	if len(choices) == 1 {
		choice := choices[0]
		if choice.UpgradePipeline == "" && apiFeaturePhase(choice.TargetPhase) == feature.PhaseImplement {
			if next, ok := m.newAPIRoadmapRewindPanel(m.selectedFeature, m.selectedFeatureName()); ok {
				m.rewindPhasePicker = next
				m.statusMessage = ""
				return m
			}
		}
		return m.confirmSelectedFeatureActionWithArgs(mutationKindFeatureRewind, apiFeatureActionArgs{
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
	cfg := &config.Config{Defaults: apiWizardDefaults(m.runtimeConfig)}
	editor := NewWorkspaceEditConfigModel(cfg, apiPhaseModelCatalog(m.catalog))
	m.configEditor = &editor
	m.statusMessage = ""
	return m
}

func (m APIAppModel) transitionToAPIHelpOverlay() (tea.Model, tea.Cmd) {
	contexts := AllHelpContexts()
	ctxName := helpContextDashboard
	switch {
	case m.contentPanelActive:
		ctxName = helpContextLogs
	case m.recoveryPanel != nil:
		ctxName = helpContextRecovery
	case m.reviewComments != nil:
		ctxName = helpContextReviewComments
	case m.wizard != nil:
		ctxName = helpContextWizard
	case m.focusPanel == 1:
		ctxName = helpContextDetailPanel
	}
	ctx, ok := contexts[ctxName]
	if !ok {
		ctx = contexts[helpContextDashboard]
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
		return mutationKindFeatureResume
	case feature.StatusFailed:
		return mutationKindFeatureRetry
	default:
		return ""
	}
}

func (m APIAppModel) resumeAllCmd(featureIDs []string) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
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
			case mutationKindFeatureResume:
				_, err = m.client.ResumeFeature(ctx, featureID)
			case mutationKindFeatureRetry:
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
		ctx := m.apiCtx()
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
		m.statusMessage = statusMsgNoFeatureSelected
		return m, nil
	}
	repo, ok := m.selectedDiffRepo(f)
	if f.Status != feature.StatusCodeReady || !ok || repo.WorktreePath == "" {
		m.statusMessage = "Diff is only available for Code Ready features with a worktree"
		return m, nil
	}
	vp := newReviewViewportModel(m.width, m.height, MutedStyle.Render("Loading diff..."))
	m.diffReview = &vp
	m.textPanelActive = false
	m.textPanelTitle = ""
	m.textPanelContent = ""
	return m, func() tea.Msg {
		diff, err := git.DiffSummary(repo.WorktreePath, repo.BaseBranch)
		if err != nil || strings.TrimSpace(diff) == "" {
			diff = MutedStyle.Render("No changes found")
		} else {
			diff = colorizeDiff(diff)
		}
		return apiDiffReviewMsg{content: diff}
	}
}

func (m APIAppModel) handleAPIDiffReviewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.diffReview == nil {
		return m, nil
	}
	if msg.Code == tea.KeyEscape {
		m.diffReview = nil
		return m, nil
	}
	updated, cmd := m.diffReview.Update(msg)
	m.diffReview = &updated
	return m, cmd
}

func (m APIAppModel) selectedDiffRepo(f *feature.Feature) (feature.FeatureRepo, bool) {
	if f == nil || len(f.Repos) == 0 {
		return feature.FeatureRepo{}, false
	}
	repo := f.Repos[0]
	if saved, ok := m.loadSavedFeatureForDiff(f.ID); ok {
		if savedRepo, ok := featureRepoByName(saved.Repos, repo.Name); ok {
			repo = mergeFeatureRepoForDiff(repo, savedRepo)
		}
	}
	return repo, true
}

func (m APIAppModel) loadSavedFeatureForDiff(featureID string) (*feature.Feature, bool) {
	stateDir := strings.TrimSpace(m.runtimeConfig.Runtime.StateDir)
	if featureID == "" || stateDir == "" {
		return nil, false
	}
	f, err := feature.NewStore(stateDir).Load(featureID)
	if err != nil {
		return nil, false
	}
	return f, true
}

func featureRepoByName(repos []feature.FeatureRepo, name string) (feature.FeatureRepo, bool) {
	if name != "" {
		for _, repo := range repos {
			if repo.Name == name {
				return repo, true
			}
		}
	}
	if len(repos) == 0 {
		return feature.FeatureRepo{}, false
	}
	return repos[0], true
}

func mergeFeatureRepoForDiff(base, overlay feature.FeatureRepo) feature.FeatureRepo {
	if overlay.Name != "" {
		base.Name = overlay.Name
	}
	if overlay.Path != "" {
		base.Path = overlay.Path
	}
	if overlay.WorktreePath != "" {
		base.WorktreePath = overlay.WorktreePath
	}
	if overlay.Branch != "" {
		base.Branch = overlay.Branch
	}
	if overlay.BaseBranch != "" {
		base.BaseBranch = overlay.BaseBranch
	}
	if overlay.Publishable != nil {
		base.Publishable = overlay.Publishable
	}
	return base
}

func apiMergeSavedPublishFeature(base, saved *feature.Feature) *feature.Feature {
	if base == nil {
		return saved
	}
	merged := *base
	merged.Repos = append([]feature.FeatureRepo(nil), base.Repos...)
	merged.RepoStates = copyPointerMap(base.RepoStates)
	merged.RepoCycles = copyPointerMap(base.RepoCycles)
	if saved == nil {
		return &merged
	}
	if merged.Description == "" {
		merged.Description = saved.Description
	}
	if merged.Summary == "" {
		merged.Summary = saved.Summary
	}
	if merged.Models == (config.ModelConfig{}) {
		merged.Models = saved.Models
	}
	if len(saved.Artifacts) > 0 {
		merged.Artifacts = copyStringMapValues(saved.Artifacts)
	}
	if len(merged.Repos) == 0 {
		merged.Repos = append([]feature.FeatureRepo(nil), saved.Repos...)
	} else {
		for i := range merged.Repos {
			if savedRepo, ok := featureRepoByName(saved.Repos, merged.Repos[i].Name); ok {
				merged.Repos[i] = mergeFeatureRepoForDiff(merged.Repos[i], savedRepo)
			}
		}
	}
	if len(saved.RepoStates) > 0 {
		merged.RepoStates = copyPointerMap(saved.RepoStates)
	}
	return &merged
}

func apiPublishRepoEntries(f *feature.Feature) []publishRepoEntry {
	if f == nil {
		return nil
	}
	entries := make([]publishRepoEntry, 0, len(f.Repos))
	for _, repo := range f.Repos {
		worktreeDir := repo.WorktreePath
		if worktreeDir == "" {
			worktreeDir = repo.Path
		}
		branch := repo.Branch
		if branch == "" && f.Slug != "" {
			branch = "feature/" + f.Slug
		}
		entry := publishRepoEntry{
			Name:        repo.Name,
			Branch:      branch,
			WorktreeDir: worktreeDir,
			RepoPath:    repo.Path,
			BaseBranch:  repo.BaseBranch,
			PRStatus:    string(statusPending),
		}
		if state := f.RepoStates[repo.Name]; state != nil {
			if state.PRURL != "" {
				entry.PRStatus = prStatusPublished
				entry.PRURL = state.PRURL
			}
			if state.LastError != "" {
				entry.PRStatus = prStatusFailed
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

func apiCommitSingleRepoForPublishPreview(f *feature.Feature) {
	if f == nil || len(f.Repos) != 1 {
		return
	}
	// Match the legacy single-repo publish flow: commit dirty work before
	// building the review model so Diff Review and Commit Log preview the PR.
	repo := f.Repos[0]
	workDir := repo.WorktreePath
	if workDir == "" {
		workDir = repo.Path
	}
	if workDir == "" || !git.HasUncommittedChanges(workDir) {
		return
	}
	_ = git.CommitAll(workDir, f.Name)
}

func apiPublishPlanText(f *feature.Feature) string {
	if f == nil {
		return ""
	}
	for _, key := range []string{artifactIDRoadmap, feature.PhasePlan.DirName()} {
		path := strings.TrimSpace(f.Artifacts[key])
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}

func apiPublishPRContext(f *feature.Feature, worktreeDir, baseBranch, planText string) agent.PRContext {
	if f == nil {
		return agent.PRContext{Roadmap: planText}
	}
	prCtx := agent.PRContext{
		FeatureName:        f.Name,
		FeatureDescription: f.Description,
		Roadmap:            planText,
	}
	if worktreeDir != "" {
		if bodies, err := git.CommitBodies(worktreeDir, baseBranch); err == nil {
			prCtx.CommitBodies = bodies
		}
		if stat, err := git.DiffStat(worktreeDir, baseBranch); err == nil {
			prCtx.DiffStat = stat
		}
	}
	return prCtx
}

func copyPointerMap[T any](in map[string]*T) map[string]*T {
	if len(in) == 0 {
		return map[string]*T{}
	}
	out := make(map[string]*T, len(in))
	for name, state := range in {
		if state == nil {
			continue
		}
		copyState := *state
		out[name] = &copyState
	}
	return out
}

func (m APIAppModel) hasAPIAttachableSession(featureID string) bool {
	return len(m.apiAttachTabsForFeature(featureID)) > 0
}

func (m APIAppModel) openAPIAttachForFeature(featureID string) (tea.Model, tea.Cmd) {
	return m.openAPIAttachForFeatureFromCache(featureID, true)
}

func (m APIAppModel) openAPIAttachForFeatureFromCache(featureID string, refreshSessions bool) (tea.Model, tea.Cmd) {
	tabs := m.apiAttachTabsForFeature(featureID)
	if len(tabs) == 0 {
		if refreshSessions {
			m.statusMessage = "Refreshing sessions..."
			return m, m.fetchAttachSessionsCmd(featureID, true)
		}
		m.statusMessage = statusMsgNoActiveSessions
		return m, nil
	}
	initialIdx := apiInitialAttachTab(tabs)
	if initialIdx < 0 {
		m.statusMessage = statusMsgNoActiveSessions
		return m, nil
	}
	attach := NewAttachModel(tabs, initialIdx, featureID, m.width, m.height)
	if f := m.selectedAPIDashboardFeature(); f != nil && f.ID == featureID {
		attach.featureName = f.Name
		attach.activeRun = f.ActiveRun
	}
	m.attach = &attach
	m.statusMessage = ""
	var liveCmd tea.Cmd
	var detailCmd tea.Cmd
	activeSessionID := ""
	if sess, ok := tabs[initialIdx].sess.(*apiSessionView); ok {
		activeSessionID = sess.ID()
		liveCmd = m.startLiveSessionOutput(sess)
		detailCmd = m.fetchAttachSessionDetailCmd(sess.ID())
	}
	var sessionsCmd tea.Cmd
	var missingDetailCmd tea.Cmd
	if refreshSessions {
		sessionsCmd = m.fetchAttachSessionsCmd(featureID, false)
	} else {
		missingDetailCmd = m.fetchAttachMissingSessionDetailsCmd(featureID, activeSessionID)
	}
	return m, tea.Batch(sessionsCmd, attach.Init(), liveCmd, detailCmd, missingDetailCmd)
}

func (m APIAppModel) openAPIContextualAction() (tea.Model, tea.Cmd) {
	if !m.requireSelectedFeature() {
		return m, nil
	}
	if f := m.selectedAPIDashboardFeature(); f != nil && f.Status.IsNeedsReview() {
		return m.openAPIReviewAttention(f)
	}
	if m.hasAPIAttachableSession(m.selectedFeature) {
		return m.openAPIAttachForFeature(m.selectedFeature)
	}
	if _, ok := m.selectedNeedInputGate(m.selectedFeature); ok {
		return m.openNeedInputPrompt(), nil
	}
	if m.selectedActionReady(mutationKindFeatureResume) {
		return m, m.primarySelectedFeatureActionCmd(mutationKindFeatureResume, m.selectedFeature)
	}
	if m.selectedActionReady(mutationKindFeatureRetry) {
		return m, m.selectedFeatureActionCmd(mutationKindFeatureRetry, m.selectedFeature)
	}
	if m.selectedActionReady(mutationKindFeatureStart) {
		return m, m.primarySelectedFeatureActionCmd(mutationKindFeatureStart, m.selectedFeature)
	}
	if f := m.selectedAPIDashboardFeature(); isLivePreviewEligible(f) {
		m.focusPanel = 1
		m.statusMessage = "Live preview is already visible"
		return m, nil
	}
	m.statusMessage = "No contextual action for selected feature"
	return m, nil
}

func (m APIAppModel) openAPIReviewAttention(f *feature.Feature) (tea.Model, tea.Cmd) {
	if f == nil || !f.Status.IsNeedsReview() {
		return m, nil
	}
	if m.artifactReview != nil &&
		m.artifactReview.FeatureID() == f.ID &&
		m.artifactReview.Detached() &&
		!m.artifactReview.Decided() {
		if artifactReviewMatchesFeature(m.artifactReview, f) {
			cmd := m.artifactReview.Reattach()
			m.statusMessage = ""
			return m, cmd
		}
		m.artifactReview = nil
	}
	artifact, ok, reason := m.reviewArtifactForFeature(f)
	if !ok {
		m.statusMessage = statusMsgLoadingReviewArtifact
		if reason != "" && reason != statusMsgReviewArtifactLoading {
			m.statusMessage = reason
		}
		return m, m.fetchReviewArtifactCmd(f.ID)
	}
	return m.openAPIReviewModel(f, artifact)
}

func (m APIAppModel) openAPIReviewModel(f *feature.Feature, artifact server.ArtifactDTO) (tea.Model, tea.Cmd) {
	if f == nil {
		return m, nil
	}
	reviewMode, rewindPhase := apiReviewTarget(f)
	model := NewArtifactReviewModel(artifact.Path, f.ID, reviewMode, rewindPhase, m.width, m.height, nil, "", nil)
	model.utilityModel = m.apiUtilityModelForFeature(f.ID)
	m.artifactReview = &model
	m.statusMessage = ""
	return m, model.editor.Focus()
}

func artifactReviewMatchesFeature(review *ArtifactReviewModel, f *feature.Feature) bool {
	if review == nil || f == nil {
		return false
	}
	reviewMode, rewindPhase := apiReviewTarget(f)
	return review.FeatureID() == f.ID &&
		review.ReviewMode() == reviewMode &&
		review.rewindPhase == rewindPhase
}

func apiReviewTarget(f *feature.Feature) (string, feature.Phase) {
	reviewMode := feature.PhasePlan.DirName()
	rewindPhase := feature.PhasePlan
	if f != nil && f.PendingReviewPhase != nil {
		rewindPhase = *f.PendingReviewPhase
		if f.IsRewind {
			reviewMode = reviewModeRewind
		} else {
			reviewMode = reviewModeGate
		}
	}
	return reviewMode, rewindPhase
}

func (m APIAppModel) reviewArtifactForFeature(f *feature.Feature) (server.ArtifactDTO, bool, string) {
	content, ok := m.selectedContentSnapshot()
	if !ok || len(content.Artifacts.Artifacts) == 0 {
		return server.ArtifactDTO{}, false, statusMsgReviewArtifactLoading
	}
	return selectReviewArtifact(f, content.Artifacts)
}

func selectReviewArtifact(f *feature.Feature, resp server.ArtifactListResponse) (server.ArtifactDTO, bool, string) {
	if len(resp.Artifacts) == 0 {
		return server.ArtifactDTO{}, false, "Review artifact is unavailable"
	}
	preferred := reviewArtifactIDs(f)
	artifacts := availableTextArtifacts(resp)
	for _, id := range preferred {
		for _, artifact := range artifacts {
			if artifact.ID == id && strings.TrimSpace(artifact.Path) != "" {
				return artifact, true, ""
			}
		}
	}
	if reviewRequiresPreferredArtifact(f) {
		return server.ArtifactDTO{}, false, statusMsgReviewArtifactLoading
	}
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.Path) != "" {
			return artifact, true, ""
		}
	}
	return server.ArtifactDTO{}, false, "Review artifact path is unavailable"
}

func reviewRequiresPreferredArtifact(f *feature.Feature) bool {
	return f != nil && f.Status.IsNeedsReview() && len(reviewArtifactIDs(f)) > 0
}

func reviewArtifactIDs(f *feature.Feature) []string {
	if f == nil {
		return nil
	}
	if f.IsRewind && f.PendingReviewPhase != nil {
		switch *f.PendingReviewPhase {
		case feature.PhaseInquire:
			return []string{artifactIDDescriptionReview, artifactIDPrompt}
		case feature.PhasePlan:
			if f.EffectivePipeline() == feature.PipelineMedium {
				return []string{artifactIDDescriptionReview, artifactIDPrompt}
			}
			return []string{feature.PhaseDesign.DirName(), feature.PhaseResearch.DirName()}
		case feature.PhaseResearch:
			return []string{feature.PhaseInquire.DirName()}
		case feature.PhaseDesign:
			return []string{feature.PhaseResearch.DirName()}
		case feature.PhaseImplement:
			if f.PendingRewindReviewRoadmapPhase != nil && *f.PendingRewindReviewRoadmapPhase > 0 {
				return []string{fmt.Sprintf("phase-%d-plan", *f.PendingRewindReviewRoadmapPhase), feature.PhasePlan.DirName()}
			}
			return []string{feature.PhasePlan.DirName(), artifactIDRoadmap}
		}
	}
	switch f.Status {
	case feature.StatusPlanNeedsReview:
		ids := make([]string, 0, 3)
		switch {
		case f.CurrentRoadmapPhase == 0:
			ids = append(ids, artifactIDRoadmap)
		case f.CurrentRoadmapPhase > 0:
			ids = append(ids, fmt.Sprintf("phase-%d-plan", f.CurrentRoadmapPhase))
		}
		return append(ids, feature.PhasePlan.DirName())
	case feature.StatusInquiryNeedsReview:
		return []string{feature.PhaseInquire.DirName()}
	case feature.StatusResearchNeedsReview:
		return []string{feature.PhaseResearch.DirName()}
	case feature.StatusDesignNeedsReview:
		return []string{feature.PhaseDesign.DirName()}
	case feature.StatusPromptNeedsReview:
		return []string{artifactIDPrompt}
	default:
		return []string{feature.PhasePlan.DirName(), artifactIDRoadmap}
	}
}

func (m APIAppModel) apiUtilityModelForFeature(featureID string) string {
	if detail, ok := m.featureDetails[featureID]; ok && strings.TrimSpace(detail.Feature.Models.Utilities) != "" {
		return detail.Feature.Models.Utilities
	}
	return m.runtimeConfig.Defaults.Utilities
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
	m.openAPIContentView()
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
	m.openAPIContentView()
	return m, m.fetchNextLogContentCmd(content)
}

func (m *APIAppModel) openAPIContentView() {
	m.contentPanelActive = true
	m.syncAPIContentViewport()
	if m.contentViewport != nil {
		m.contentViewport.GotoTop()
	}
}

func (m *APIAppModel) closeAPIContentView() {
	m.contentPanelActive = false
	m.contentViewport = nil
}

func (m APIAppModel) handleAPIContentKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Back):
		m.closeAPIContentView()
		return m, nil
	case key.Matches(msg, keys.ViewLogs):
		return m.cycleSelectedLog()
	case msg.Text == "[":
		return m.cycleSelectedArtifact(-1)
	case msg.Text == "]":
		return m.cycleSelectedArtifact(1)
	}
	if m.contentViewport == nil {
		m.openAPIContentView()
	}
	if m.contentViewport == nil {
		return m, nil
	}
	updated, cmd := m.contentViewport.Update(msg)
	m.contentViewport = &updated
	return m, cmd
}

func (m *APIAppModel) syncAPIContentViewport() {
	if !m.contentPanelActive || m.snapshot.Content == nil {
		return
	}
	if m.contentViewport == nil {
		vp := newReviewViewportModel(m.width, m.height, "")
		m.contentViewport = &vp
	} else {
		m.contentViewport.Resize(m.width, m.height)
	}
	m.contentViewport.SetContent(m.renderAPIContentBody())
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
	if !m.requireSelectedFeature() {
		return m, nil
	}
	if !m.selectedActionReady(mutationKindFeatureReviewComments) {
		m.statusMessage = apiMutationKindLabel(mutationKindFeatureReviewComments) + " is unavailable"
		return m, nil
	}
	repos := m.selectedRepoActionOptions(mutationKindFeatureReviewComments)
	if len(repos) > 1 {
		m.repoActionPanel = newAPIRepoActionPanel(m.selectedFeature, m.selectedFeatureName(), mutationKindFeatureReviewComments, repos, false)
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

func (m APIAppModel) openRefactorPromptFor(restart bool) APIAppModel {
	if !m.requireSelectedFeature() {
		return m
	}
	kind := mutationKindFeatureRefactorStart
	if restart {
		kind = mutationKindFeatureRefactorRestart
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
	ta := NewSimpleTextarea()
	ta.Placeholder = "Describe the refactoring for " + repo + "..."
	ta.ShowFocusedHint = true
	ta.SetWidth(max(m.width-12, 20))
	if m.width >= 100 {
		ta.SetWidth(max((m.width/2)-10, 20))
	}
	ta.SetHeight(5)
	_ = ta.Focus()
	canPaste := canPasteClipboardImage()
	var imageTempDir string
	var attachTempDir string
	if canPaste {
		imageTempDir, _ = os.MkdirTemp("", "agentic-refactor-images-*")
		attachTempDir, _ = os.MkdirTemp("", "agentic-refactor-attach-*")
	}
	m.refactorPrompt = &apiRefactorPrompt{
		featureID:     m.selectedFeature,
		featureName:   m.selectedFeatureName(),
		repo:          repo,
		input:         ta,
		pipelines:     pipelines,
		restart:       restart,
		canPaste:      canPaste,
		imageTempDir:  imageTempDir,
		attachTempDir: attachTempDir,
	}
	m.refactorPipeline = nil
	m.statusMessage = ""
	return m
}

func newAPIRepoActionPanel(featureID, featureName, kind string, repos []apiRepoActionOption, multi bool) *apiRepoActionPanel {
	selected := map[string]bool{}
	if multi {
		for _, repo := range repos {
			if repo.Name != "" && (kind != mutationKindFeaturePublish || repo.Publishable) {
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
	for _, repo := range m.selectedFeatureRepoNames() {
		if _, ok := byName[repo]; !ok {
			add(apiRepoActionOption{Name: repo})
		}
	}
	options := make([]apiRepoActionOption, 0, len(order))
	for _, name := range order {
		options = append(options, byName[name])
	}
	if kind == mutationKindFeatureReviewComments {
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

func findActionInput(action server.ActionDTO, name string) (server.ActionInputDTO, bool) {
	for _, input := range action.RequiredInputs {
		if input.Name == name {
			return input, true
		}
	}
	return server.ActionInputDTO{}, false
}

func (m APIAppModel) selectedReviewCommentsDefaults() (string, string, []string, bool) {
	action, ok := m.selectedRawAction(mutationKindFeatureReviewComments)
	if !ok || !action.Enabled {
		return "", "", nil, false
	}
	repoRequired := false
	if input, found := findActionInput(action, actionInputNameRepo); found {
		repoRequired = input.Required
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
	if action, ok := m.selectedRawAction(mutationKindFeatureReviewComments); ok {
		if input, found := findActionInput(action, actionInputNameMode); found {
			modes = apiReviewCommentModes(input.Options)
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
	if input, found := findActionInput(action, actionInputNamePipeline); found {
		pipelines = apiRefactorPipelines(input.Options)
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
	if repos := m.selectedFeatureRepoNames(); len(repos) > 0 {
		return repos[0]
	}
	return ""
}

// selectedFeatureRepoNames returns the repo names declared on the currently
// selected feature, looking first in the cached feature list and falling
// back to the last full snapshot.
func (m APIAppModel) selectedFeatureRepoNames() []string {
	for _, feature := range m.featureList.Features {
		if feature.ID == m.selectedFeature {
			return feature.Repos
		}
	}
	for _, feature := range m.snapshot.Features {
		if feature.ID == m.selectedFeature {
			return feature.Repos
		}
	}
	return nil
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
	if kind == mutationKindFeatureTweakFinish {
		return m.selectedFeatureHasTweakCycle(m.selectedFeature)
	}
	if m.snapshot.Detail != nil {
		for _, action := range m.snapshot.Detail.Actions {
			if apiActionMatchesMutationKind(action.ID, kind) {
				return action.Status == "" || action.Status == actionStatusReady
			}
		}
	}
	switch kind {
	case mutationKindFeatureStart:
		status, ok := m.selectedFeatureStatus(m.selectedFeature)
		return ok && status == feature.StatusCreated
	case mutationKindFeatureResume:
		status, ok := m.selectedFeatureStatus(m.selectedFeature)
		return ok && (status == feature.StatusInterrupted || status == feature.StatusNeedUserInput)
	case mutationKindFeaturePublish, mutationKindFeatureMerge, mutationKindFeatureRestart,
		mutationKindFeatureMarkDone, mutationKindFeatureRebase, mutationKindFeatureCleanup,
		mutationKindFeatureTweakStart, mutationKindFeatureRefactorStart, mutationKindFeatureRefactorRestart,
		mutationKindFeatureDelete:
		return m.selectedFeature != ""
	case mutationKindFeatureRetry:
		status, ok := m.selectedFeatureStatus(m.selectedFeature)
		return ok && status == feature.StatusFailed
	case mutationKindFeatureReviewComments:
		action, ok := m.selectedRawAction(kind)
		return ok && action.Enabled
	case mutationKindFeatureRewind:
		return m.selectedFeatureCurrentPhase(m.selectedFeature) != ""
	case mutationKindFeatureStop:
		for _, f := range m.snapshot.Features {
			if f.ID == m.selectedFeature {
				return apiFeatureCanStop(f.Status)
			}
		}
	}
	return false
}

func (m APIAppModel) selectedFeatureActionLocallyBlocked(kind string) (bool, string) {
	f := m.selectedAPIDashboardFeature()
	if f == nil || !hasActiveRepoCycles(f) {
		return false, ""
	}
	switch kind {
	case mutationKindFeatureDelete:
		return true, "Stop active repo cycles before deleting"
	case mutationKindFeatureRewind:
		return true, "Stop active repo cycles before rewinding"
	case mutationKindFeatureMarkDone:
		return true, "Cannot mark done while repo cycles are active"
	default:
		return false, ""
	}
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
	case mutationKindFeatureStart:
		return actionID == actionIDStart
	case mutationKindFeatureResume:
		return actionID == recoveryActionResume
	case mutationKindFeaturePublish:
		return actionID == actionIDPublish
	case mutationKindFeatureMerge:
		return actionID == actionIDMerge
	case mutationKindFeatureRestart:
		return actionID == actionIDRestart
	case mutationKindFeatureRetry:
		return actionID == actionIDRetry
	case mutationKindFeatureRebase:
		return actionID == actionIDRebase
	case mutationKindFeatureMarkDone:
		return actionID == actionIDMarkDone
	case mutationKindFeatureCleanup:
		return actionID == actionIDCleanup
	case mutationKindFeatureReviewComments:
		return actionID == actionIDReviewComments
	case mutationKindFeatureTweakStart:
		return actionID == actionIDTweak
	case mutationKindFeatureTweakFinish:
		return actionID == actionIDTweak
	case mutationKindFeatureRefactorStart:
		return actionID == actionIDRefactor
	case mutationKindFeatureRefactorRestart:
		return actionID == actionIDRefactor
	case mutationKindFeatureRewind:
		return actionID == actionIDRewind
	case mutationKindFeatureStop:
		return actionID == "pause-stop" || actionID == actionIDStop
	case mutationKindFeatureDelete:
		return actionID == actionIDDelete
	default:
		return false
	}
}

func (m APIAppModel) renderAPIContentScreen() string {
	vp := m.contentViewport
	if vp == nil {
		temp := newReviewViewportModel(m.width, m.height, m.renderAPIContentBody())
		vp = &temp
	}
	w := m.width
	if w < 40 {
		w = 80
	}

	var b strings.Builder
	box := renderReviewViewportBox(w-2, "Run Content", *vp)
	b.WriteString(" " + strings.ReplaceAll(box, "\n", "\n "))
	b.WriteString("\n")
	b.WriteString(renderReviewViewportScrollPercent(*vp))
	b.WriteString("\n")
	b.WriteString(KeyHelpStyle.Render(apiContentFooter() + "   [↑/↓] Scroll"))
	b.WriteString("\n")
	return b.String()
}

func apiContentFooter() string {
	return " [l] Next log   [[] Prev artifact   []] Next artifact   [esc] Close"
}

func (m APIAppModel) renderAPIContentBody() string {
	content := m.snapshot.Content
	if content == nil {
		return ""
	}
	var b strings.Builder
	if content.RunNumber > 0 {
		fmt.Fprintf(&b, "  Run: %d\n", content.RunNumber)
	}
	if content.Log != nil {
		b.WriteString("\n")
		fmt.Fprintf(&b, "  Log %s", content.Log.ID)
		if content.Log.Size > 0 {
			fmt.Fprintf(&b, "  bytes %d-%d of %d", content.Log.Offset, min(content.Log.Offset+content.Log.Limit, content.Log.Size), content.Log.Size)
		}
		if content.Log.Truncated {
			b.WriteString("  truncated")
		}
		b.WriteByte('\n')
		appendIndentedText(&b, content.Log.Text)
	}
	if content.Artifact != nil {
		b.WriteString("\n")
		fmt.Fprintf(&b, "  Artifact %s", content.Artifact.ID)
		if content.Artifact.Phase != "" {
			b.WriteString("  " + content.Artifact.Phase)
		}
		if content.Artifact.Size > 0 {
			fmt.Fprintf(&b, "  bytes %d-%d of %d", content.Artifact.Offset, min(content.Artifact.Offset+content.Artifact.Limit, content.Artifact.Size), content.Artifact.Size)
		}
		if content.Artifact.Truncated {
			b.WriteString("  truncated")
		}
		b.WriteByte('\n')
		appendIndentedText(&b, content.Artifact.Text)
	}
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

func (m APIAppModel) renderOwnedServerQuitPrompt() string {
	keyStyle := lipgloss.NewStyle().Bold(true).Foreground(colorWarning)
	actionStyle := lipgloss.NewStyle().Bold(true).Foreground(colorText)
	footerStyle := lipgloss.NewStyle().Foreground(colorOverlay)

	var b strings.Builder
	b.WriteString("The server belongs to this window.")
	b.WriteString("\n\n")
	b.WriteString("  " + keyStyle.Render("[ y ]") + " " + actionStyle.Render("Stop server + quit") + "\n")
	b.WriteString("        " + MutedStyle.Render("clean shutdown") + "\n\n")
	b.WriteString("  " + keyStyle.Render("[ n ]") + " " + actionStyle.Render("Keep server running") + "\n")
	b.WriteString("        " + MutedStyle.Render("detach this TUI only") + "\n\n")
	b.WriteString("        " + footerStyle.Render("esc cancel"))

	box := renderBoxPanel(58, colorWarning, b.String())
	return renderBorderTitle(box, "Session Exit", WarningStyle)
}

// renderBoxPanel wraps body in an active-styled panel of the given width and
// border color.
func renderBoxPanel(width int, color compat.AdaptiveColor, body string) string {
	return panelStyle(true).
		Width(width).
		BorderForeground(color).
		Render(body)
}

func (m APIAppModel) renderFeatureActionConfirm() string {
	title := "Confirm " + apiMutationKindLabel(m.actionConfirmKind)
	if m.actionConfirmKind == mutationKindFeatureRewind {
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
	if lines, ok := apiFeatureActionConfirmWarnings[m.actionConfirmKind]; ok {
		b.WriteString(WarningStyle.Render("  " + lines[0]))
		b.WriteString("\n")
		b.WriteString(WarningStyle.Render("  " + lines[1]))
	} else {
		b.WriteString(WarningStyle.Render("  This will send the selected API request."))
	}
	b.WriteString("\n\n")
	b.WriteString(KeyHelpStyle.Render(" [y] Confirm   [any key] Cancel"))
	return renderBoxPanel(58, colorWarning, b.String())
}

var apiFeatureActionConfirmWarnings = map[string][2]string{
	mutationKindFeaturePublish:    {"This will publish the selected feature.", "Review the server result before continuing."},
	mutationKindFeatureMerge:      {"This will merge the selected feature to the base branch.", "Review the server result before continuing."},
	mutationKindFeatureRestart:    {"This will restart the selected feature phase.", "Any active work for that phase will be replaced."},
	mutationKindFeatureRetry:      {"This will retry the failed feature phase.", "Existing failure state will be replaced by the retry."},
	mutationKindFeatureMarkDone:   {"This will mark the selected feature as done.", "Review the feature status before continuing."},
	mutationKindFeatureCleanup:    {"This will clean the selected feature worktrees.", "Feature state and repo-cycle history will be preserved."},
	mutationKindFeatureRebase:     {"This will start a rebase cycle for the selected feature.", "Conflict handling and push results will be reported by the server."},
	mutationKindFeatureTweakStart: {"This will start an interactive tweak session for the selected feature.", "Finish and review decisions will be handled by the server."},
	mutationKindFeatureStop:       {"This will interrupt the current phase.", "You can restart it later."},
	mutationKindFeatureDelete:     {"This will remove all artifacts and worktrees.", "This cannot be undone."},
}

func (m APIAppModel) renderAPIRewindConfirm() string {
	args := m.actionConfirmArgs
	target := args.TargetPhase
	if target == "" {
		target = m.selectedFeatureCurrentPhase(m.actionConfirmFeatureID)
	}
	phaseName := apiRewindPhaseLabel(target)
	if args.UpgradePipeline != "" {
		phaseName = apiRewindPhaseLabel("knowledgebase")
	}
	var c strings.Builder
	c.WriteString("\n")
	switch {
	case args.UpgradePipeline != "":
		fmt.Fprintf(&c, "  \u26a0 Upgrade to %s\n\n", args.UpgradePipeline)
		c.WriteString(WarningStyle.Render("  Pipeline will be upgraded and feature will restart from KB Build."))
		c.WriteString("\n")
		c.WriteString(WarningStyle.Render("  All progress will be lost."))
		c.WriteString("\n")
	case args.RoadmapPhase > 0:
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
	default:
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
	contentBox := renderBoxPanel(60, colorWarning, c.String())
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
		fmt.Fprintf(&b, "  %d interrupted/failed feature(s) will be resumed.\n", len(m.resumeAllFeatureIDs))
	} else {
		b.WriteString(MutedStyle.Render("  No interrupted or failed features to resume."))
		b.WriteString("\n")
	}
	panelWidth := 56
	contentBox := renderBoxPanel(panelWidth, colorInfo, b.String())
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
	return renderBoxPanel(58, colorBrand, b.String())
}

func (m APIAppModel) renderAPIReviewCommentsPanel(width int) string {
	panel := m.reviewComments
	if panel == nil {
		return ""
	}
	panel.browser.resize(width, max(m.height, 18))
	return panel.browser.View()
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
			b.WriteString("  " + apiRepoActionLine(panel, i, repo) + "\n")
		}
	}
	b.WriteByte('\n')
	footer := " [\u2191/\u2193] Select   [enter] Confirm   [esc] Cancel"
	if panel.multi {
		footer = " [space] Toggle   [enter] Confirm   [esc] Cancel"
	}
	b.WriteString(KeyHelpStyle.Render(footer))
	return renderBoxPanel(width, colorBrand, b.String())
}

// apiRepoActionLine renders a single repo row for the repo action panel,
// including cursor, selection mark, and status suffix.
func apiRepoActionLine(panel *apiRepoActionPanel, i int, repo apiRepoActionOption) string {
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
	if status := apiRepoActionStatusForKind(panel.kind, repo); status != "" {
		line += "  " + status
	}
	if i == panel.cursor {
		line = SelectedRowStyle.Render(line)
	}
	return line
}

func apiRepoActionStatusForKind(kind string, repo apiRepoActionOption) string {
	parts := make([]string, 0, 3)
	if repo.Publishable && kind == mutationKindFeaturePublish {
		parts = append(parts, "publishable")
	}
	if repo.State != "" {
		parts = append(parts, repo.State)
	} else if kind != mutationKindFeaturePublish {
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
	return renderBoxPanel(width, colorBrand, b.String())
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
	return renderBoxPanel(width, colorBrand, b.String())
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
	case feature.PhaseKnowledgeBase.DirName(), "knowledge_base", "knowledge-base", "kb":
		return phaseLabelKBBuild
	case feature.PhaseInquire.DirName(), "inquiry":
		return "Start (Inquiry)"
	case feature.PhaseResearch.DirName():
		return feature.PhaseResearch.String()
	case feature.PhaseDesign.DirName():
		return feature.PhaseDesign.String()
	case feature.PhasePlan.DirName():
		return feature.PhasePlan.String()
	case feature.PhaseImplement.DirName():
		return feature.PhaseImplement.String()
	case feature.PhaseReview.DirName():
		return feature.PhaseReview.String()
	case "finalreview", "final_review", phaseAliasFinalReview:
		return feature.PhaseFinalReview.String()
	case feature.PhasePublish.DirName():
		return feature.PhasePublish.String()
	default:
		return strings.TrimSpace(phase)
	}
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
		fmt.Fprintf(&b, "  Iteration: %d\n", m.needInputGate.Iteration)
	}
	if m.needInputGate.Scope != "" || m.needInputGate.Iteration > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(WarningStyle.Render("  Resume continues from the saved answers."))
	b.WriteString("\n")
	b.WriteString(WarningStyle.Render("  Abort marks the paused gate as failed."))
	b.WriteString("\n\n")
	b.WriteString(KeyHelpStyle.Render(" [r] Resume   [a] Abort   [esc] Cancel"))
	return renderBoxPanel(58, colorWarning, b.String())
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
	b.WriteString(WarningStyle.Render("  Allow Once resumes the waiting session."))
	b.WriteString("\n")
	if remember := m.permissionRequest.Remember; remember != nil {
		b.WriteString(WarningStyle.Render("  Allow & Remember stores " + remember.Pattern + " for " + remember.ScopeDisplay + "."))
		b.WriteString("\n")
	}
	b.WriteString(WarningStyle.Render("  Deny sends a rejection to the session."))
	b.WriteString("\n\n")
	if m.permissionRequest.Remember != nil {
		b.WriteString(KeyHelpStyle.Render(" [a] Allow Once   [r] Allow & Remember   [d] Deny   [esc] Cancel"))
	} else {
		b.WriteString(KeyHelpStyle.Render(" [a] Allow Once   [d] Deny   [esc] Cancel"))
	}
	return renderBoxPanel(58, colorWarning, b.String())
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
	return renderBoxPanel(58, colorWarning, b.String())
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
	return renderBoxPanel(58, colorWarning, b.String())
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

// startLiveSessionOutput begins tailing sess's structured transcript-record
// stream, reconciling each record through sess.applyTranscriptRow — the
// same per-row dedup the snapshot-refresh path uses — so the existing
// AttachModel render loop (drainAndPollAttachChCmd/pollAttachCh) picks
// applied messages up with no further wiring. Any previously running feed
// is stopped first — only one session's output is tailed at a time,
// matching the single visible attach tab.
func (m *APIAppModel) startLiveSessionOutput(sess *apiSessionView) tea.Cmd {
	m.stopLiveSessionOutput()
	if sess == nil || m.client == nil || !sess.IsActive() || strings.TrimSpace(sess.ID()) == "" {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.liveOutputCancel = cancel
	m.liveOutputSessionID = sess.ID()
	// Resume from the row after whatever the transcript snapshot already
	// loaded (sess.lastTranscriptMessage), not from index 0 — avoids
	// re-streaming the whole history apiSessionView already has. The
	// sentinel value -1 (no rows applied yet) naturally resolves to 0 here.
	m.liveOutputRecords, m.liveOutputErrs = m.client.SubscribeSessionOutput(ctx, sess.ID(), server.SessionOutputStreamOptions{AfterIndex: sess.lastTranscriptMessage + 1})
	return m.listenLiveSessionOutputCmd(sess)
}

// stopLiveSessionOutput cancels any in-flight live output subscription and
// clears its bookkeeping. Safe to call when no feed is running.
func (m *APIAppModel) stopLiveSessionOutput() {
	if m.liveOutputCancel != nil {
		m.liveOutputCancel()
	}
	m.liveOutputCancel = nil
	m.liveOutputSessionID = ""
	m.liveOutputRecords = nil
	m.liveOutputErrs = nil
}

// listenLiveSessionOutputCmd blocks for the next record or error on the
// channels captured when the feed started, reconciles it through
// sess.applyTranscriptRow, and forwards the resulting message (if any) onto
// sess.attachCh — mirroring the non-blocking select/default pattern
// session/manager.go uses for local sessions so a slow consumer can't stall
// the reconciliation loop.
func (m APIAppModel) listenLiveSessionOutputCmd(sess *apiSessionView) tea.Cmd {
	records, errs := m.liveOutputRecords, m.liveOutputErrs
	sessionID := sess.ID()
	return func() tea.Msg {
		select {
		case rec, ok := <-records:
			if !ok {
				return apiSessionOutputDoneMsg{sessionID: sessionID}
			}
			if msg := sess.applyTranscriptRow(rec.Message); msg != nil {
				select {
				case sess.attachCh <- *msg:
				default:
				}
			}
			return apiSessionOutputLineMsg{sessionID: sessionID}
		case _, ok := <-errs:
			if !ok {
				return apiSessionOutputDoneMsg{sessionID: sessionID}
			}
			// Reconnect-on-error is left to a future task; for now a stream
			// error just ends this feed like a closed channel would.
			return apiSessionOutputDoneMsg{sessionID: sessionID}
		}
	}
}

// attachedSessionView returns the *apiSessionView AttachModel is currently
// polling — its sess field, the same pointer switchToTab installs and
// drainAndPollAttachChCmd/pollAttachCh drain — or nil if there is no attach
// view or its active session isn't a live API session.
//
// This is deliberately not repoTabs[activeTabIdx].sess: apiAttachTabsForFeature
// rebuilds every repoTab's *apiSessionView (with a fresh, empty attachCh)
// on every refresh snapshot, but AttachModel.sess only repoints at one of
// those fresh objects when switchToTab actually runs (see
// rebuildAPIAttachTabs). Writing to a repoTabs pointer that AttachModel
// isn't (yet) polling would silently drop every applied message.
func (m *APIAppModel) attachedSessionView() *apiSessionView {
	if m.attach == nil {
		return nil
	}
	sess, _ := m.attach.sess.(*apiSessionView)
	return sess
}

// syncLiveSessionOutputForAttach reconciles the live output feed with
// AttachModel's current sess: stopped if the attach view closed, restarted
// against the new session if it changed. This is called after any point
// where AttachModel's own Update (tab/shift+tab key handling) or
// api_app.go's tab-rebuild logic may have repointed sess, since neither of
// those call sites knows about the live feed.
func (m *APIAppModel) syncLiveSessionOutputForAttach() tea.Cmd {
	if m.attach == nil {
		m.stopLiveSessionOutput()
		return nil
	}
	sess := m.attachedSessionView()
	if sess != nil && sess.ID() == m.liveOutputSessionID {
		return nil
	}
	return m.startLiveSessionOutput(sess)
}

func apiAttachBackfillScrollTrigger(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		return msg.Mouse().Button == tea.MouseWheelUp
	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", keyPgUp:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func (m *APIAppModel) maybeStartAttachTranscriptBackfill(msg tea.Msg) tea.Cmd {
	if !apiAttachBackfillScrollTrigger(msg) || m.attach == nil || !m.attach.viewport.AtTop() || m.client == nil {
		return nil
	}
	sess := m.attachedSessionView()
	if sess == nil {
		return nil
	}
	before := sess.firstLoadedTranscriptIndex()
	if before <= 0 {
		return nil
	}
	if m.transcriptBackfills == nil {
		m.transcriptBackfills = map[string]bool{}
	}
	if m.transcriptBackfills[sess.ID()] {
		return nil
	}
	m.transcriptBackfills[sess.ID()] = true
	return m.fetchAttachTranscriptBackfillCmd(sess.ID(), before)
}

func (m APIAppModel) fetchAttachTranscriptBackfillCmd(sessionID string, before int) tea.Cmd {
	start := max(0, before-apiTranscriptPageLimit)
	limit := before - start
	if sessionID == "" || limit <= 0 || m.client == nil {
		return nil
	}
	ctx := m.apiCtx()
	return func() tea.Msg {
		transcript, err := m.client.Transcript(ctx, sessionID, server.CursorQuery{Cursor: start, Limit: limit})
		return apiTranscriptBackfillMsg{sessionID: sessionID, before: before, transcript: transcript, err: err}
	}
}

func (m APIAppModel) fetchRefreshSnapshotCmd(signal server.RefreshSignal) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
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

func (m APIAppModel) fetchAttachSessionsCmd(featureID string, openIfClosed bool) tea.Cmd {
	if featureID == "" || m.client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx := m.apiCtx()
		sessions, err := m.client.Sessions(ctx)
		return apiAttachSessionsSnapshotMsg{
			featureID:    featureID,
			sessions:     sessions,
			err:          err,
			openIfClosed: openIfClosed,
		}
	}
}

func (m APIAppModel) fetchAttachMissingSessionDetailsCmd(featureID string, skipSessionIDs ...string) tea.Cmd {
	if featureID == "" || m.client == nil {
		return nil
	}
	skip := make(map[string]bool, len(skipSessionIDs))
	for _, id := range skipSessionIDs {
		if id != "" {
			skip[id] = true
		}
	}
	seen := map[string]bool{}
	var cmds []tea.Cmd
	for _, tab := range m.apiAttachTabsForFeature(featureID) {
		sess, ok := tab.sess.(*apiSessionView)
		if !ok || sess == nil {
			continue
		}
		sessionID := sess.ID()
		if sessionID == "" || skip[sessionID] || seen[sessionID] {
			continue
		}
		seen[sessionID] = true
		if _, ok := m.sessionDetails[sessionID]; ok {
			continue
		}
		if cmd := m.fetchAttachSessionDetailCmd(sessionID); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

func (m APIAppModel) fetchAttachSessionDetailCmd(sessionID string) tea.Cmd {
	if sessionID == "" || m.client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx := m.apiCtx()
		session, transcript, err := loadAPITranscriptTail(ctx, m.client, sessionID)
		if err != nil {
			return apiRefreshSnapshotMsg{err: err}
		}
		return apiRefreshSnapshotMsg{snapshot: server.RefreshSnapshot{
			Session:    &session,
			Transcript: &transcript,
		}}
	}
}

func (m APIAppModel) fetchFeatureDetailCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
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

func (m APIAppModel) fetchReviewArtifactCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
		detail, err := m.client.FeatureDetail(ctx, featureID)
		if err != nil {
			return apiReviewArtifactMsg{featureID: featureID, err: err}
		}
		runNumber := apiActiveRunNumber(detail.Feature)
		if runNumber <= 0 {
			return apiReviewArtifactMsg{featureID: featureID, err: fmt.Errorf("active run is unavailable")}
		}
		artifacts, err := m.client.ArtifactList(ctx, featureID, runNumber)
		if err != nil {
			return apiReviewArtifactMsg{featureID: featureID, err: err}
		}
		f := m.apiDashboardFeature(apiFeatureDetailSummary(detail.Feature), detail.Feature, true)
		artifact, ok, reason := selectReviewArtifact(f, artifacts)
		if !ok {
			if reason == "" {
				reason = "Review artifact is unavailable"
			}
			return apiReviewArtifactMsg{featureID: featureID, err: errors.New(reason)}
		}
		return apiReviewArtifactMsg{featureID: featureID, detail: detail, artifact: artifact}
	}
}

func (m APIAppModel) fetchArtifactContentCmd(content apiFeatureContentSnapshot, artifact server.ArtifactDTO) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
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
		ctx := m.apiCtx()
		logIDs := cycleLogIDs(content.LogID)
		var lastErr error
		sawMissingLog := false
		for _, logID := range logIDs {
			query := server.TextQuery{Limit: apiContentTailLimit}
			if logID == content.LogID && content.Log != nil {
				query.Offset = apiContentTailOffset(content.Log.Size)
			}
			resp, err := m.client.LogContent(ctx, content.FeatureID, content.RunNumber, logID, query)
			if err != nil {
				if apiMissingRunLogError(err) {
					sawMissingLog = true
					continue
				}
				lastErr = err
				continue
			}
			next := content
			next.LogID = logID
			next.Log = &resp
			next.ContentLoaded = true
			return apiContentSelectionMsg{featureID: content.FeatureID, content: next}
		}
		if sawMissingLog && lastErr == nil {
			return apiContentSelectionMsg{
				featureID: content.FeatureID,
				content:   content,
				status:    "No run logs available for selected run",
			}
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

func apiMissingRunLogError(err error) bool {
	var apiErr *server.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Status == http.StatusNotFound && apiErr.Code == "not_found"
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
	switch resource.Type {
	case resourceTypeLog, "artifact":
		return true
	default:
		return false
	}
}

func (m APIAppModel) fetchFeatureConfigCmd(featureID string) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
		cfg, err := m.client.FeatureConfig(ctx, featureID)
		return apiFeatureConfigMsg{featureID: featureID, config: cfg, err: err}
	}
}

func (m APIAppModel) fetchReviewCommentsCmd(featureID, featureName, repo, mode string, modes []string) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
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

func (m APIAppModel) createFeatureCmd(result *WizardResult) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
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
				kind: mutationKindFeatureCreate,
				err:  err,
			}
		}
		if created.FeatureID == "" {
			return apiMutationResultMsg{
				kind: mutationKindFeatureCreate,
				err:  errors.New("create feature response missing feature_id"),
			}
		}
		_, err = m.client.StartFeature(ctx, created.FeatureID)
		return apiMutationResultMsg{
			kind:      mutationKindFeatureCreate,
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
		m.statusMessage = statusMsgUpdatingWorkspaceConfig
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
			m.statusMessage = statusMsgUpdatingWorkspaceConfig
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
		m.statusMessage = statusMsgUpdatingWorkspaceConfig
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
		m.statusMessage = statusMsgUpdatingWorkspaceConfig
		return m.persistRuntimeWorkspaceRootsCmd(roots, createdPath)
	}
	return nil
}

func (m APIAppModel) primarySelectedFeatureActionCmd(kind, featureID string) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
		var err error
		switch kind {
		case mutationKindFeatureResume:
			_, err = m.client.ResumeFeature(ctx, featureID)
		case mutationKindFeatureStart:
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
		ctx := m.apiCtx()
		args := apiFeatureActionArgs{}
		if len(argsOpt) > 0 {
			args = argsOpt[0]
		}
		var err error
		switch kind {
		case mutationKindFeaturePublish:
			_, err = m.client.PublishFeature(ctx, featureID, server.PublishFeatureRequest{
				Repos: append([]string(nil), args.Repos...),
				Title: args.Title,
				Body:  args.Body,
			})
		case mutationKindFeatureMerge:
			_, err = m.client.MergeFeature(ctx, featureID)
		case mutationKindFeatureRestart:
			_, err = m.client.RestartFeature(ctx, featureID, server.RestartFeatureRequest{})
		case mutationKindFeatureRetry:
			_, err = m.client.RetryFeature(ctx, featureID)
		case mutationKindFeatureMarkDone:
			_, err = m.client.MarkDone(ctx, featureID)
		case mutationKindFeatureRebase:
			_, err = m.client.StartRebase(ctx, featureID, server.RebaseActionRequest{})
		case mutationKindFeatureCleanup:
			_, err = m.client.CleanupFeature(ctx, featureID, server.CleanupActionRequest{Target: "worktrees"})
		case mutationKindFeatureRewind:
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
		case mutationKindFeatureTweakStart:
			_, err = m.client.StartTweak(ctx, featureID, server.TweakActionRequest{})
		case mutationKindFeatureStop:
			_, err = m.client.StopFeature(ctx, featureID)
		case mutationKindFeatureDelete:
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
		ctx := m.apiCtx()
		_, err := m.client.FinishTweak(ctx, featureID, server.TweakFinishRequest{
			Decision:   decision,
			HadChanges: hadChanges,
		})
		return apiMutationResultMsg{
			kind:      mutationKindFeatureTweakFinish,
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
		return apiMutationResultMsg{kind: mutationKindRecoveryExecute, err: err}
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
		moveCursor(&panel.cursor, -1, len(panel.repos))
		return m, nil
	case tea.KeyDown:
		moveCursor(&panel.cursor, 1, len(panel.repos))
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
		moveCursor(&panel.cursor, 1, len(panel.repos))
	case "k":
		moveCursor(&panel.cursor, -1, len(panel.repos))
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
	case mutationKindFeaturePublish:
		repos := panel.selectedRepoNames()
		if len(repos) == 0 {
			m.repoActionPanel = &panel
			m.statusMessage = "Select at least one repo to publish"
			return m, nil
		}
		m.selectedFeature = panel.featureID
		return m.confirmSelectedFeatureActionWithArgs(mutationKindFeaturePublish, apiFeatureActionArgs{Repos: repos}), nil
	case mutationKindFeatureReviewComments:
		m.selectedFeature = panel.featureID
		mode, modes := m.selectedReviewCommentsModeDefaults()
		m.statusMessage = "Fetching review comments..."
		return m, m.fetchReviewCommentsCmd(panel.featureID, panel.featureName, repo, mode, modes)
	case mutationKindFeatureTweakStart:
		m.selectedFeature = panel.featureID
		return m.confirmSelectedFeatureActionWithArgs(panel.kind, apiFeatureActionArgs{Repo: repo}), nil
	case mutationKindFeatureRefactorStart:
		m.selectedFeature = panel.featureID
		return m.openRefactorPromptForRepo(panel.kind, repo, false), nil
	case mutationKindFeatureRefactorRestart:
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
		moveCursor(&panel.cursor, -1, len(panel.choices))
		return m, nil
	case tea.KeyDown:
		moveCursor(&panel.cursor, 1, len(panel.choices))
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
		return m.confirmSelectedFeatureActionWithArgs(mutationKindFeatureRewind, apiFeatureActionArgs{
			TargetPhase:     choice.TargetPhase,
			UpgradePipeline: choice.UpgradePipeline,
		}), nil
	}
	switch strings.ToLower(msg.Text) {
	case "j":
		moveCursor(&panel.cursor, 1, len(panel.choices))
	case "k":
		moveCursor(&panel.cursor, -1, len(panel.choices))
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
		moveCursor(&panel.cursor, -1, len(panel.rows))
		return m, nil
	case tea.KeyDown:
		moveCursor(&panel.cursor, 1, len(panel.rows))
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
		return m.confirmSelectedFeatureActionWithArgs(mutationKindFeatureRewind, apiFeatureActionArgs{
			TargetPhase:  feature.PhaseImplement.DirName(),
			RoadmapPhase: row.Number,
		}), nil
	}
	switch strings.ToLower(msg.Text) {
	case "j":
		moveCursor(&panel.cursor, 1, len(panel.rows))
	case "k":
		moveCursor(&panel.cursor, -1, len(panel.rows))
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
		if panel.browser.filtering || panel.browser.filter != "" {
			panel.browser, _ = panel.browser.Update(msg)
			m.reviewComments = panel
			return m, nil
		}
		m.reviewComments = nil
		m.statusMessage = ""
		return m, nil
	case tea.KeyEnter:
		included := panel.includedComments()
		if len(included) == 0 {
			panel.browser.status = "No comments included. Press space to include one, or Shift+A to address all."
			m.reviewComments = panel
			return m, nil
		}
		m.statusMessage = "Addressing included review comments for " + panel.repo + "..."
		return m, m.startReviewCommentsCmd(panel.withComments(included))
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
	updated, cmd := panel.browser.Update(msg)
	panel.browser = updated
	m.reviewComments = panel
	if cmd != nil {
		return m, cmd
	}
	return m, nil
}

func (p apiReviewCommentsPanel) includedComments() []server.ReviewCommentDTO {
	out := make([]server.ReviewCommentDTO, 0, len(p.comments))
	for _, comment := range p.comments {
		if p.browser.included[comment.ID] {
			out = append(out, comment)
		}
	}
	return out
}

func (p apiReviewCommentsPanel) withComments(comments []server.ReviewCommentDTO) apiReviewCommentsPanel {
	p.comments = append([]server.ReviewCommentDTO(nil), comments...)
	p.browser = newReviewCommentsBrowserModel(p.featureName, p.repo, reviewCommentItemsFromDTO(p.comments), p.browser.width, p.browser.height)
	return p
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
			images:      append([]string(nil), prompt.images...),
			attachments: append([]string(nil), prompt.attachments...),
			pipelines:   append([]feature.PipelineProfile(nil), prompt.pipelines...),
			cursor:      apiDefaultRefactorPipelineCursor(prompt.pipelines),
			restart:     prompt.restart,
		}
		m.refactorPrompt = nil
		m.statusMessage = ""
		return m, nil
	}
	if msg.Code == tea.KeyEscape {
		m.refactorPrompt = nil
		m.statusMessage = ""
		return m, nil
	}
	if msg.Code == 'v' && msg.Mod.Contains(tea.ModCtrl) && prompt.canPaste {
		imgDir := prompt.imageTempDir
		attachDir := prompt.attachTempDir
		nextIdx := prompt.imageCounter + 1
		return m, func() tea.Msg {
			path, err := saveClipboardImage(imgDir, nextIdx)
			if err == nil {
				return ImagePastedMsg{Path: path}
			}
			paths, names, ferr := saveClipboardFiles(attachDir)
			if ferr == nil && len(paths) > 0 {
				return FilesPastedMsg{Paths: paths, Names: names}
			}
			text, terr := getClipboardText()
			if terr == nil && text != "" {
				return TextPastedMsg{Text: text}
			}
			return ImagePasteFailedMsg{}
		}
	}
	var cmd tea.Cmd
	prompt.input, cmd = prompt.input.Update(msg)
	prompt.draft = prompt.input.Value()
	m.statusMessage = ""
	return m, cmd
}

func (m APIAppModel) handleAPIRefactorPromptMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	prompt := m.refactorPrompt
	if prompt == nil {
		return m, nil
	}
	switch msg := msg.(type) {
	case ImagePastedMsg:
		prompt.imageCounter++
		prompt.images = append(prompt.images, msg.Path)
		prompt.input.InsertString(fmt.Sprintf("[Image #%d]", len(prompt.images)))
	case ImagePasteFailedMsg:
	case TextPastedMsg:
		prompt.input.InsertString(msg.Text)
	case FilesPastedMsg:
		prompt.attachments = append(prompt.attachments, msg.Paths...)
		prompt.attachNames = append(prompt.attachNames, msg.Names...)
		for _, name := range msg.Names {
			prompt.input.InsertString(fmt.Sprintf("[%s]", name))
		}
	case tea.PasteMsg:
		var cmd tea.Cmd
		prompt.input, cmd = prompt.input.Update(msg)
		prompt.draft = prompt.input.Value()
		m.statusMessage = ""
		return m, cmd
	default:
		return m, nil
	}
	prompt.draft = prompt.input.Value()
	m.statusMessage = ""
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
		moveCursor(&panel.cursor, -1, len(panel.pipelines))
		return m, nil
	case tea.KeyRight:
		moveCursor(&panel.cursor, 1, len(panel.pipelines))
		return m, nil
	case tea.KeyEnter:
		m.refactorPipeline = nil
		return m, m.startRefactorCmd(*panel)
	}
	switch strings.ToLower(msg.Text) {
	case "h":
		moveCursor(&panel.cursor, -1, len(panel.pipelines))
	case "l":
		moveCursor(&panel.cursor, 1, len(panel.pipelines))
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
	if editor.activeTab == tabModels && editor.editor.ModelFilteringActive() {
		updated, cmd := editor.Update(msg)
		m.configEditor = &updated
		return m, cmd
	}
	switch msg.String() {
	case keyEsc:
		if editor.editor.HasChanges() {
			editor.discardConfirm = true
			return m, nil
		}
		m.configEditor = nil
		return m, nil
	case keyEnter:
		if editor.EnterIsLocal() {
			updated, cmd := editor.Update(msg)
			m.configEditor = &updated
			return m, cmd
		}
		if editor.saving {
			return m, nil
		}
		editor.saving = true
		editor.saveErr = ""
		if editor.isWorkspace {
			return m, m.saveWorkspaceConfigCmd(*editor)
		}
		return m, m.saveFeatureConfigCmd(*editor)
	default:
		updated, cmd := editor.Update(msg)
		m.configEditor = &updated
		return m, cmd
	}
}

// backspaceRune removes the last rune from s, if any.
func backspaceRune(s string) string {
	runes := []rune(s)
	if len(runes) > 0 {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
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
		m.helpAnswerDraft = backspaceRune(m.helpAnswerDraft)
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
		questionKey := apiAskUserAnswerQuestionKey(m.askUserRequest, m.askUserQuestion)
		return m, m.askUserAnswerCmd(m.askUserRequest, questionKey, answer)
	case tea.KeyBackspace:
		m.askUserAnswerDraft = backspaceRune(m.askUserAnswerDraft)
		return m, nil
	}
	if hasOptions {
		var handled bool
		m, handled = m.handleAPIAskUserOptionShortcut(msg, question)
		if handled {
			return m, nil
		}
	}
	if msg.Text != "" && !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
		m.askUserAnswerDraft += msg.Text
		m.statusMessage = ""
	}
	return m, nil
}

// handleAPIAskUserOptionShortcut handles j/k cursor movement and 1-9 quick-select
// shortcuts for an ask-user question with options. Returns handled=false if msg
// did not match a shortcut.
func (m APIAppModel) handleAPIAskUserOptionShortcut(msg tea.KeyPressMsg, question server.AskUserQuestionDTO) (APIAppModel, bool) {
	switch strings.ToLower(msg.Text) {
	case "j":
		if m.askUserOptionCursor < len(question.Options)-1 {
			m.askUserOptionCursor++
		}
		return m, true
	case "k":
		if m.askUserOptionCursor > 0 {
			m.askUserOptionCursor--
		}
		return m, true
	}
	if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
		idx := int(msg.Text[0] - '1')
		if idx < len(question.Options) {
			m.askUserOptionCursor = idx
			m.askUserAnswerDraft = question.Options[idx].Label
		}
		return m, true
	}
	return m, false
}

func (m APIAppModel) saveFeatureConfigCmd(editor EditConfigModel) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
		snap := editor.editor.Snapshot()
		_, err := m.client.UpdateFeatureConfig(ctx, editor.featureID, server.FeatureConfigMutationRequest{
			Models:      snap.Models,
			Inquireness: string(snap.Inquireness),
			Checkpoints: snap.Checkpoints,
			Pipeline:    editor.pipeline,
		})
		return apiMutationResultMsg{
			kind:      mutationKindFeatureConfigUpdate,
			featureID: editor.featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) saveWorkspaceConfigCmd(editor EditConfigModel) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
		snap := editor.editor.Snapshot()
		_, err := m.client.UpdateRuntimeConfig(ctx, server.RuntimeConfigMutationRequest{
			Defaults: config.DefaultsConfig{
				Models:      snap.Models,
				Inquireness: string(snap.Inquireness),
				Checkpoints: feature.FeatureCheckpointsToConfig(snap.Checkpoints),
				Pipeline:    string(editor.pipeline),
			},
		})
		if err != nil {
			return apiRuntimeConfigMutationMsg{kind: mutationKindRuntimeConfigUpdate, err: err}
		}
		cfg, err := m.client.RuntimeConfig(ctx)
		return apiRuntimeConfigMutationMsg{kind: mutationKindRuntimeConfigUpdate, config: cfg, err: err}
	}
}

func (m APIAppModel) startReviewCommentsCmd(panel apiReviewCommentsPanel) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
		_, err := m.client.StartReviewComments(ctx, panel.featureID, server.ReviewCommentsActionRequest{
			Repo:     panel.repo,
			Mode:     panel.mode,
			Comments: append([]server.ReviewCommentDTO(nil), panel.comments...),
		})
		return apiMutationResultMsg{
			kind:      mutationKindFeatureReviewComments,
			featureID: panel.featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) startRefactorCmd(panel apiRefactorPipelinePanel) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
		pipeline := feature.PipelineLarge
		if len(panel.pipelines) > 0 && panel.cursor >= 0 && panel.cursor < len(panel.pipelines) {
			pipeline = panel.pipelines[panel.cursor]
		}
		req := server.RefactorActionRequest{
			Repo:        panel.repo,
			Prompt:      panel.prompt,
			Images:      append([]string(nil), panel.images...),
			Attachments: append([]string(nil), panel.attachments...),
			Pipeline:    pipeline,
		}
		kind := mutationKindFeatureRefactorStart
		var err error
		if panel.restart {
			kind = mutationKindFeatureRefactorRestart
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
		ctx := m.apiCtx()
		_, err := m.client.NeedUserInputDecision(ctx, featureID, server.NeedUserInputDecisionRequest{Decision: decision})
		return apiMutationResultMsg{
			kind:      mutationKindFeatureNeedUserInputDecision,
			featureID: featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) reviewDecisionCmd(featureID string, req server.ReviewDecisionRequest) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
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
		ctx := m.apiCtx()
		answer := server.PermissionAnswerRequest{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Decision:  decision,
		}
		if decision == permission.DecisionAllowRemember && req.Remember != nil {
			answer.RememberPattern = req.Remember.Pattern
			scope := req.Remember.Scope
			answer.RememberScope = &scope
		}
		_, err := m.client.AnswerPermission(ctx, answer)
		return apiMutationResultMsg{
			kind:      mutationKindPermissionAnswer,
			featureID: req.FeatureID,
			requestID: req.RequestID,
			err:       err,
		}
	}
}

func (m APIAppModel) helpAnswerCmd(featureID, answer string) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
		_, err := m.client.SendHelp(ctx, server.HelpAnswerRequest{
			FeatureID: featureID,
			Message:   answer,
		})
		return apiMutationResultMsg{
			kind:      mutationKindHelpSend,
			featureID: featureID,
			err:       err,
		}
	}
}

func (m APIAppModel) askUserAnswerCmd(req server.ControlRequestDTO, question, answer string) tea.Cmd {
	return func() tea.Msg {
		ctx := m.apiCtx()
		_, err := m.client.AnswerAskUser(ctx, server.AskUserAnswerRequest{
			RequestID: req.RequestID,
			SessionID: req.SessionID,
			Answers:   map[string]string{question: answer},
		})
		return apiMutationResultMsg{
			kind:      mutationKindAskUserAnswer,
			featureID: req.FeatureID,
			requestID: req.RequestID,
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
		if progress.CurrentRoadmapPhase == 0 && progress.TotalRoadmapPhases > 0 {
			req.Roadmap = true
			return req
		}
	}
	if m.featureHasArtifact(featureID, "roadmap") {
		req.Roadmap = true
		return req
	}
	if decision == reviewDecisionProceed {
		req.Phase = feature.PhaseImplement.DirName()
	}
	return req
}

func roadmapReviewDecisionRequest(msg RoadmapReviewDecisionMsg) server.ReviewDecisionRequest {
	decision := reviewDecisionProceed
	if msg.Decision == "reject" {
		decision = reviewDecisionIterate
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

func (m APIAppModel) featureHasArtifact(featureID, artifactID string) bool {
	if featureID == "" || artifactID == "" {
		return false
	}
	content, ok := m.contents[featureID]
	if !ok {
		return false
	}
	for _, artifact := range content.Artifacts.Artifacts {
		if artifact.ID == artifactID {
			return true
		}
	}
	return false
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
		if req.FeatureID == featureID && req.ToolName != toolNameAskUserQuestion && isPendingControlStatus(req.Status) {
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
		if req.FeatureID == featureID && req.ToolName == toolNameAskUserQuestion && isPendingControlStatus(req.Status) {
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

func apiAskUserAnswerQuestionKey(req server.ControlRequestDTO, fallback string) string {
	if len(req.Input) == 0 {
		return strings.TrimSpace(fallback)
	}
	data, err := json.Marshal(req.Input)
	if err != nil {
		return strings.TrimSpace(fallback)
	}
	questions := parseAskUserQuestions(data)
	if len(questions) == 0 {
		return strings.TrimSpace(fallback)
	}
	question := strings.TrimSpace(questions[0].RawQuestion)
	if question == "" {
		question = strings.TrimSpace(questions[0].Question)
	}
	if question == "" {
		question = strings.TrimSpace(questions[0].Header)
	}
	if question != "" {
		return question
	}
	return strings.TrimSpace(fallback)
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

func apiPhaseModelCatalog(resp server.ModelCatalogResponse) PhaseModelCatalog {
	cat := PhaseModelCatalog{
		Fields:              append([]string(nil), globalModelFields...),
		ProviderModels:      map[string][]string{},
		ProviderOrder:       append([]string(nil), resp.ProviderOrder...),
		PhaseDefaults:       map[string]string{},
		PhaseProviderModels: map[string]map[string][]string{},
	}
	missingProviders := map[string]bool{}
	cat.PhaseDefaults["Clarify"] = resp.PhaseDefaults.Inquiry
	cat.PhaseDefaults["Research"] = resp.PhaseDefaults.Research
	cat.PhaseDefaults["Planning"] = resp.PhaseDefaults.Planning
	cat.PhaseDefaults["Implementation"] = resp.PhaseDefaults.Implementation
	cat.PhaseDefaults["Review"] = resp.PhaseDefaults.Review
	cat.PhaseDefaults["Utilities"] = resp.PhaseDefaults.Utilities
	cat.PhaseDefaults[phaseLabelKBBuild] = resp.PhaseDefaults.KBBuild
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
		displayField := normalizeModelCatalogField(field)
		cat.PhaseProviderModels[displayField] = map[string][]string{}
		for provider, models := range providerModels {
			cat.PhaseProviderModels[displayField][provider] = append([]string(nil), models...)
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
	cat.ProviderOrder = append(cat.ProviderOrder, missingOrder...)
	return cat
}

func apiFeatureModelCatalog(resp server.ModelCatalogResponse) PhaseModelCatalog {
	cat := apiPhaseModelCatalog(resp)
	cat.Fields = append([]string(nil), phaseCatalogFields...)
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
		if (mode == reviewCommentsModeAuto || mode == reviewCommentsModeAddressAll) && !stringSliceContains(modes, mode) {
			modes = append(modes, mode)
		}
	}
	if len(modes) == 0 {
		return []string{reviewCommentsModeAuto, reviewCommentsModeAddressAll}
	}
	return modes
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
			Label:       m.apiRewindChoiceLabel(option),
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
	action, ok := m.selectedRawAction(mutationKindFeatureRewind)
	if ok {
		for _, input := range action.RequiredInputs {
			if input.Name == actionInputNameTargetPhase {
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

func (m APIAppModel) apiRewindChoiceLabel(phase string) string {
	if apiFeaturePhase(phase) == feature.PhaseImplement {
		if _, total := m.selectedRoadmapProgress(m.selectedFeature); total > 1 {
			return "Choose Implement roadmap phase"
		}
	}
	return apiRewindChoiceLabel(phase)
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
		if detail.Feature.ActiveRunDetail != nil {
			current = firstNonZero(detail.Feature.ActiveRunDetail.RoadmapPhase, current)
			total = firstNonZero(detail.Feature.ActiveRunDetail.RoadmapTotal, total)
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

// roadmapPhaseStatusCurrent, roadmapPhaseStatusCompleted and
// roadmapPhaseStatusPending are apiRoadmapPhaseStatus() display tokens for a
// roadmap phase's progress relative to the active phase.
const (
	roadmapPhaseStatusCurrent   = "current"
	roadmapPhaseStatusCompleted = "completed"
	roadmapPhaseStatusPending   = "pending"
)

func apiRoadmapPhaseStatus(current, phaseNum int) string {
	switch {
	case current == phaseNum:
		return roadmapPhaseStatusCurrent
	case current > phaseNum:
		return roadmapPhaseStatusCompleted
	default:
		return roadmapPhaseStatusPending
	}
}

func apiAttentionCounts(features []server.FeatureSummary, prompts server.PromptSnapshotResponse, permissions server.PermissionSnapshotResponse) map[string]int {
	counts := map[string]int{}
	suppressed := apiSuppressedAttentionFeatures(features)
	shouldCount := func(featureID string) bool {
		return featureID != "" && !suppressed[featureID]
	}
	for _, h := range prompts.HelpQueue {
		if h.Pending && shouldCount(h.FeatureID) {
			counts[h.FeatureID]++
		}
	}
	for _, gate := range prompts.NeedUserInputs {
		if gate.Open && shouldCount(gate.FeatureID) {
			counts[gate.FeatureID]++
		}
	}
	for _, ask := range prompts.AskUserQuestions {
		if isPendingControlStatus(ask.Status) && shouldCount(ask.FeatureID) {
			counts[ask.FeatureID]++
		}
	}
	for _, req := range permissions.Requests {
		if isPendingControlStatus(req.Status) && shouldCount(req.FeatureID) {
			counts[req.FeatureID]++
		}
	}
	return counts
}

func apiSuppressedAttentionFeatures(features []server.FeatureSummary) map[string]bool {
	suppressed := make(map[string]bool)
	for _, summary := range features {
		if summary.ID == "" {
			continue
		}
		if apiFeatureStatus(summary.Status) == feature.StatusInterrupted {
			suppressed[summary.ID] = true
		}
	}
	return suppressed
}

func isPendingControlStatus(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	return status == "" || status == string(statusPending) || status == "waiting"
}

func apiFeatureSortOrder(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case apiSessionStatusDone:
		return 2
	case prStatusPublished:
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
	case featureStatusTokenCreated:
		return feature.StatusCreated
	case "researching":
		return feature.StatusResearching
	case "planready", "plan_ready", "plan-ready":
		return feature.StatusPlanReady
	case featureStatusTokenPlanning:
		return feature.StatusPlanning
	case "implementready", "implement_ready", "implement-ready":
		return feature.StatusImplementReady
	case string(statusImplementing):
		return feature.StatusImplementing
	case "reviewpassed", "review_passed", "review-passed":
		return feature.StatusReviewPassed
	case "codeready", "code_ready", "code-ready", "prready", "pr_ready", "pr-ready":
		return feature.StatusCodeReady
	case prStatusPublished:
		return feature.StatusPublished
	case string(statusFailed):
		return feature.StatusFailed
	case "interrupted":
		return feature.StatusInterrupted
	case apiSessionStatusDone:
		return feature.StatusDone
	case "buildingkb", "building_kb", "building-kb":
		return feature.StatusBuildingKB
	case "settingupworktrees", "setting_up_worktrees", "setting-up-worktrees":
		return feature.StatusSettingUpWorktrees
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
	case string(statusReviewing):
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
	case feature.PhaseKnowledgeBase.DirName(), "knowledge_base", "knowledge-base", "knowledge base", "kb":
		return feature.PhaseKnowledgeBase
	case feature.PhaseInquire.DirName(), "inquiry":
		return feature.PhaseInquire
	case feature.PhaseResearch.DirName():
		return feature.PhaseResearch
	case feature.PhaseDesign.DirName(), "brainstorm":
		return feature.PhaseDesign
	case feature.PhasePlan.DirName(), featureStatusTokenPlanning:
		return feature.PhasePlan
	case feature.PhaseImplement.DirName(), "implementation":
		return feature.PhaseImplement
	case feature.PhaseReview.DirName():
		return feature.PhaseReview
	case "finalreview", "final_review", phaseAliasFinalReview, "final review":
		return feature.PhaseFinalReview
	case feature.PhasePublish.DirName(), "publishing":
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
			key = feature.PhaseImplement.DirName()
		}
		out[key] = total
	}
	return out
}

func apiFeatureCanStop(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "researching", featureStatusTokenPlanning, string(statusImplementing), string(statusReviewing), "publishing", featureStatusTokenRunning, "settingupworktrees", "setting_up_worktrees", "setting-up-worktrees":
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
		if repo.RebaseStatus != "" {
			stateParts = append(stateParts, "rebase/"+repo.RebaseStatus)
		}
		if repo.Freshness != "" && repo.Freshness != git.FreshnessUnknown {
			stateParts = append(stateParts, repo.Freshness)
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
	logID := logTabSession
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
	if dto.ActiveRunDetail != nil && dto.ActiveRunDetail.RunNumber > 0 {
		return dto.ActiveRunDetail.RunNumber
	}
	return dto.ActiveRun
}

func apiContentTailOffset(size int64) int64 {
	if size <= apiContentTailLimit {
		return 0
	}
	return size - apiContentTailLimit
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
		Phase:      dto.Feature.CurrentPhase,
		Activity:   strings.TrimSpace(dto.Activity),
		ContextPct: dto.Context.Percentage,
		CostUSD:    dto.Cost.TotalUSD,
	}
	if dto.Session != nil {
		out.SessionID = dto.Session.ID
		out.Phase = firstNonEmpty(dto.Session.Phase, out.Phase)
		out.Kind = dto.Session.Kind
		out.Label = dto.Session.Label
		out.Provider = dto.Session.Provider
		out.Model = dto.Session.Model
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
		out.TranscriptRows = append(out.TranscriptRows, msg)
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
	case mutationKindFeatureCreate:
		return "Create Feature"
	case mutationKindFeatureStart:
		return "Start"
	case mutationKindFeatureResume:
		return "Resume"
	case mutationKindFeaturePublish:
		return "Publish" //nolint:goconst // action-verb label, coincidentally matches unrelated phase/help-context literals
	case mutationKindFeatureMerge:
		return "Merge"
	case mutationKindFeatureStop:
		return "Stop"
	case mutationKindFeatureDelete:
		return "Delete"
	case mutationKindFeatureRestart:
		return "Restart"
	case mutationKindFeatureRetry:
		return "Retry"
	case mutationKindFeatureMarkDone:
		return "Mark Done"
	case mutationKindFeatureRebase:
		return "Rebase"
	case mutationKindFeatureCleanup:
		return "Cleanup"
	case mutationKindFeatureRewind:
		return "Rewind" //nolint:goconst // action-verb label, unrelated to other domains sharing this word
	case mutationKindFeatureTweakStart:
		return "Tweak"
	case mutationKindFeatureTweakFinish:
		return "Finish Tweak"
	case mutationKindFeatureReviewComments:
		return helpContextReviewComments
	case mutationKindFeatureRefactorStart:
		return "Refactor"
	case mutationKindFeatureRefactorRestart:
		return "Restart Refactor"
	case mutationKindFeatureNeedUserInputDecision:
		return "Need Input Decision"
	case "feature.input_notifications.toggle":
		return "Input Alerts"
	case "feature.review_decision":
		return "Review Decision"
	case mutationKindFeatureConfigUpdate:
		return "Feature Config"
	case mutationKindRuntimeConfigUpdate:
		return "Runtime Config"
	case mutationKindPermissionAnswer:
		return "Permission Answer"
	case mutationKindHelpSend:
		return "Help Reply"
	case mutationKindAskUserAnswer:
		return "AskUser Answer"
	case mutationKindRecoveryExecute:
		return helpContextRecovery
	default:
		return strings.TrimSpace(kind)
	}
}

func apiMutationSuccessMessage(kind string) string {
	switch kind {
	case mutationKindFeatureRefactorRestart:
		return "Restarted Refactor"
	case mutationKindFeatureRebase, mutationKindFeatureTweakStart, mutationKindFeatureReviewComments, mutationKindFeatureRefactorStart:
		return "Started " + apiMutationKindLabel(kind)
	default:
		return "Completed " + apiMutationKindLabel(kind)
	}
}

func apiMutationRefreshesFeatureDetail(kind string) bool {
	switch kind {
	case mutationKindFeatureRebase,
		mutationKindFeatureTweakStart,
		mutationKindFeatureReviewComments,
		mutationKindFeatureRefactorStart,
		mutationKindFeatureRefactorRestart:
		return true
	default:
		return false
	}
}
