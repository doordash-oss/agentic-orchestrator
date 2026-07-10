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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/instancelock"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/server/serverapi"
)

const APIVersion = "v1"

const discoveryFilename = ".agentico-server.json"

// ChatSessionID is the stable utility-session identity used by the AMA chat.
const ChatSessionID = "__chat__"

type RuntimeIdentity struct {
	RuntimeDir string `json:"runtime_dir"`
	StateDir   string `json:"state_dir"`
	Config     string `json:"config_path"`
}

type LaunchPolicy struct {
	Resolved                   bool     `json:"resolved"`
	Providers                  []string `json:"providers,omitempty"`
	DangerouslySkipPermissions bool     `json:"dangerously_skip_permissions"`
}

type Options struct {
	Runtime      RuntimeIdentity
	LaunchPolicy LaunchPolicy
	StartMode    string
	Owner        instancelock.Owner
	AuthToken    string
	// AllowUnauthenticated is only for tests that intentionally exercise the
	// server without discovery bootstrap. Production Start requires AuthToken.
	AllowUnauthenticated bool
	Features             FeatureLister
	FeatureStore         FeatureReader
	Freshness            RepoFreshnessProvider
	Config               *config.Config
	Registry             *llm.Registry
	Sessions             ports.SessionManager
	Events               <-chan interface{}
	DomainEvents         <-chan ports.Event
	Mutations            MutationTarget
	RequestShutdown      func()
}

type HandlerOptions struct {
	Runtime      RuntimeIdentity
	LaunchPolicy LaunchPolicy
	StartedAt    time.Time
	Owner        instancelock.Owner
	AuthToken    string
	// DisableHostValidation turns off the loopback Host-header check. Host
	// validation defaults to ON — only tests exercising something other
	// than host validation itself should set this to true.
	DisableHostValidation bool
	Features              FeatureLister
	FeatureStore          FeatureReader
	Freshness             RepoFreshnessProvider
	Config                *config.Config
	Registry              *llm.Registry
	Sessions              ports.SessionManager
	Events                <-chan interface{}
	DomainEvents          <-chan ports.Event
	Mutations             MutationTarget
	RequestShutdown       func()
}

type FeatureLister interface {
	List() ([]*feature.Feature, error)
}

type FeatureReader interface {
	FeatureLister
	Load(id string) (*feature.Feature, error)
	LoadRun(featureID string, runNumber int) (*feature.Run, error)
	RunDir(featureID string, runNumber int) string
}

type ResponseMeta = serverapi.ResponseMeta

type ErrorResponse = serverapi.ErrorResponse

type ErrorDTO = serverapi.Error

// OwnerDTO is the public process-owner metadata safe to expose through REST and
// discovery records.
type OwnerDTO struct {
	PID       int       `json:"pid"`
	PGID      int       `json:"pgid,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Version   string    `json:"version,omitempty"`
}

// OwnerDTOFromInstanceOwner drops local filesystem paths from lock owner
// metadata before it crosses public API or discovery boundaries.
func OwnerDTOFromInstanceOwner(owner instancelock.Owner) OwnerDTO {
	return OwnerDTO{
		PID:       owner.PID,
		PGID:      owner.PGID,
		StartedAt: owner.StartedAt,
		Version:   owner.Version,
	}
}

type HealthResponse struct {
	APIVersion   string          `json:"api_version"`
	Status       string          `json:"status"`
	Runtime      RuntimeIdentity `json:"runtime"`
	LaunchPolicy LaunchPolicy    `json:"launch_policy"`
	StartedAt    time.Time       `json:"started_at"`
	Owner        OwnerDTO        `json:"owner"`
	ServerTime   time.Time       `json:"server_time"`
}

type FeatureListResponse = serverapi.FeatureListResponse

type FeatureSummary = serverapi.FeatureSummary

type FeatureProgress = serverapi.FeatureProgress

type WarningDTO = serverapi.Warning

type FeatureDetailResponse struct {
	APIVersion string           `json:"api_version"`
	Meta       ResponseMeta     `json:"meta"`
	Feature    FeatureDetailDTO `json:"feature"`
}

type FeatureDetailDTO struct {
	FeatureSummary
	Description     string             `json:"description,omitempty"`
	Summary         string             `json:"summary,omitempty"`
	Pipeline        string             `json:"pipeline,omitempty"`
	Models          config.ModelConfig `json:"models"`
	ActiveRun       *RunSummaryDTO     `json:"active_run_detail,omitempty"`
	HistoricalRuns  []RunSummaryDTO    `json:"historical_runs"`
	RepoStatus      []RepoStatusDTO    `json:"repo_status"`
	Cycle           *CycleDTO          `json:"cycle,omitempty"`
	Timing          TimingDTO          `json:"timing"`
	Cost            CostDTO            `json:"cost"`
	ReviewGate      ReviewGateDTO      `json:"review_gate"`
	Failure         *FailureDTO        `json:"failure,omitempty"`
	NeedUserInput   *NeedInputGateDTO  `json:"need_user_input,omitempty"`
	Actions         []ActionDTO        `json:"actions"`
	Revision        string             `json:"revision"`
	CacheRevalidate string             `json:"cache_revalidate"`
}

type ActionDTO = serverapi.Action

type ActionScopeDTO = serverapi.ActionScope

type ActionInputDTO = serverapi.ActionInput

type ActionDisabledReasonDTO = serverapi.ActionDisabledReason

type RunSummaryDTO struct {
	RunNumber                       int        `json:"run_number"`
	StartedAt                       *time.Time `json:"started_at,omitempty"`
	SealedAt                        *time.Time `json:"sealed_at,omitempty"`
	SealReason                      string     `json:"seal_reason,omitempty"`
	CurrentPhase                    string     `json:"current_phase,omitempty"`
	PhaseStatus                     string     `json:"phase_status,omitempty"`
	Iteration                       int        `json:"iteration,omitempty"`
	RoadmapPhase                    int        `json:"roadmap_phase,omitempty"`
	RoadmapTotal                    int        `json:"roadmap_total,omitempty"`
	PendingReviewPhase              string     `json:"pending_review_phase,omitempty"`
	PendingRewindReviewRoadmapPhase int        `json:"pending_rewind_review_roadmap_phase,omitempty"`
	IsRewind                        bool       `json:"is_rewind,omitempty"`
	ArtifactCount                   int        `json:"artifact_count"`
	HasNeedUserGate                 bool       `json:"has_need_user_gate,omitempty"`
	Setup                           *SetupDTO  `json:"setup,omitempty"`
}

type SetupDTO struct {
	Status        string                  `json:"status"`
	Attempt       int                     `json:"attempt,omitempty"`
	StartedAt     *time.Time              `json:"started_at,omitempty"`
	CompletedAt   *time.Time              `json:"completed_at,omitempty"`
	LatestLogPath string                  `json:"latest_log_path,omitempty"`
	Tasks         map[string]SetupTaskDTO `json:"tasks,omitempty"`
	TaskOrder     []string                `json:"task_order,omitempty"`
	LastError     string                  `json:"last_error,omitempty"`
}

type SetupTaskDTO struct {
	Key              string     `json:"key"`
	Kind             string     `json:"kind"`
	Label            string     `json:"label,omitempty"`
	Repo             string     `json:"repo,omitempty"`
	Status           string     `json:"status"`
	Path             string     `json:"path,omitempty"`
	SourcePath       string     `json:"source_path,omitempty"`
	Branch           string     `json:"branch,omitempty"`
	StartPoint       string     `json:"start_point,omitempty"`
	UseCurrentBranch bool       `json:"use_current_branch,omitempty"`
	Attempt          int        `json:"attempt,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
}

type RepoStatusDTO = serverapi.RepoStatus

type CycleDTO = serverapi.Cycle

type TimingDTO = serverapi.Timing

type CostDTO = serverapi.Cost

type ReviewGateDTO = serverapi.ReviewGate

type FailureDTO = serverapi.Failure

type NeedInputGateDTO = serverapi.NeedUserInputGate

type RecoverySnapshotResponse struct {
	APIVersion string            `json:"api_version"`
	Meta       ResponseMeta      `json:"meta"`
	SnapshotID string            `json:"snapshot_id"`
	Items      []RecoveryItemDTO `json:"items"`
}

type RecoveryItemDTO struct {
	Key            string   `json:"key"`
	FeatureID      string   `json:"feature_id"`
	FeatureName    string   `json:"feature_name,omitempty"`
	RepoName       string   `json:"repo_name,omitempty"`
	Phase          string   `json:"phase,omitempty"`
	Iteration      int      `json:"iteration,omitempty"`
	PID            int      `json:"pid,omitempty"`
	ProcessAlive   bool     `json:"process_alive"`
	Tweak          bool     `json:"tweak,omitempty"`
	DefaultAction  string   `json:"default_action"`
	AllowedActions []string `json:"allowed_actions"`
}

type RecoveryActionRequest struct {
	SnapshotID string            `json:"snapshot_id"`
	Actions    map[string]string `json:"actions"`
}

type RuntimeConfigResponse struct {
	APIVersion      string                `json:"api_version"`
	Meta            ResponseMeta          `json:"meta"`
	Runtime         RuntimeIdentity       `json:"runtime"`
	Defaults        config.ModelConfig    `json:"model_defaults"`
	FeatureDefaults FeatureDefaultsDTO    `json:"feature_defaults"`
	Repos           []ConfigRepoDTO       `json:"repos"`
	WorkspaceRoots  []string              `json:"workspace_roots,omitempty"`
	UI              config.UIConfig       `json:"ui"`
	Notifications   NotificationConfigDTO `json:"notifications"`
	Observability   ObservabilityDTO      `json:"observability"`
	Providers       []string              `json:"providers"`
}

type WorkspaceBrowseQuery struct {
	Path       string `json:"path,omitempty"`
	ShowHidden bool   `json:"show_hidden,omitempty"`
}

type WorkspaceBrowseResponse struct {
	APIVersion     string                    `json:"api_version"`
	Meta           ResponseMeta              `json:"meta"`
	Path           string                    `json:"path"`
	IsGitRepo      bool                      `json:"is_git_repo"`
	ChildRepoCount int                       `json:"child_repo_count"`
	Entries        []WorkspaceBrowseEntryDTO `json:"entries"`
}

type WorkspaceBrowseEntryDTO struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	IsGitRepo      bool   `json:"is_git_repo"`
	ChildRepoCount int    `json:"child_repo_count,omitempty"`
}

type ConfigRepoDTO struct {
	Name          string                        `json:"name"`
	Path          string                        `json:"path,omitempty"`
	PipelineGates map[string]config.Checkpoints `json:"pipeline_gates,omitempty"`
}

type FeatureDefaultsDTO struct {
	Models              config.ModelConfig                   `json:"models"`
	PipelinePreferences map[string]config.PipelinePreference `json:"pipeline_preferences,omitempty"`
	Inquireness         string                               `json:"inquireness,omitempty"`
	Pipeline            string                               `json:"pipeline,omitempty"`
	Checkpoints         config.Checkpoints                   `json:"checkpoints"`
}

type NotificationConfigDTO struct {
	MuteFeatureInput bool `json:"mute_feature_input"`
}

type ObservabilityDTO struct {
	Events          bool   `json:"events"`
	OTelEnabled     bool   `json:"otel_enabled"`
	OTelServiceName string `json:"otel_service_name,omitempty"`
}

type FeatureConfigResponse struct {
	APIVersion string            `json:"api_version"`
	Meta       ResponseMeta      `json:"meta"`
	FeatureID  string            `json:"feature_id"`
	Current    FeatureConfigDTO  `json:"current"`
	Defaults   FeatureConfigDTO  `json:"defaults"`
	Original   FeatureConfigDTO  `json:"original"`
	Publish    PublishabilityDTO `json:"publishability"`
}

type FeatureConfigDTO struct {
	Models      config.ModelConfig `json:"models"`
	Inquireness string             `json:"inquireness"`
	Checkpoints CheckpointsDTO     `json:"checkpoints"`
	Pipeline    string             `json:"pipeline,omitempty"`
}

type CheckpointsDTO = serverapi.Checkpoints

type PublishabilityDTO struct {
	ManualPublish bool            `json:"manual_publish"`
	Repos         map[string]bool `json:"repos"`
}

type ModelCatalogResponse struct {
	APIVersion          string                         `json:"api_version"`
	Meta                ResponseMeta                   `json:"meta"`
	ProviderOrder       []string                       `json:"provider_order"`
	ProviderModels      map[string][]ModelDTO          `json:"provider_models"`
	PhaseDefaults       config.ModelConfig             `json:"phase_defaults"`
	PhaseProviderModels map[string]map[string][]string `json:"phase_provider_models"`
}

type ModelDTO struct {
	ID            string   `json:"id"`
	DisplayName   string   `json:"display_name,omitempty"`
	ContextWindow int      `json:"context_window,omitempty"`
	Aliases       []string `json:"aliases,omitempty"`
	Category      string   `json:"category,omitempty"`
}

type PromptSnapshotResponse struct {
	APIVersion       string              `json:"api_version"`
	Meta             ResponseMeta        `json:"meta"`
	AskUserQuestions []ControlRequestDTO `json:"ask_user_questions"`
	HelpQueue        []HelpQueueDTO      `json:"help_queue"`
	NeedUserInputs   []NeedInputGateDTO  `json:"need_user_inputs"`
}

type PermissionSnapshotResponse struct {
	APIVersion string              `json:"api_version"`
	Meta       ResponseMeta        `json:"meta"`
	Requests   []ControlRequestDTO `json:"requests"`
}

type ControlRequestDTO = serverapi.ControlRequest

type AskUserQuestionDTO = serverapi.AskUserQuestion

type AskUserOptionDTO = serverapi.AskUserOption

type HelpQueueDTO struct {
	FeatureID string    `json:"feature_id"`
	Question  string    `json:"question"`
	Pending   bool      `json:"pending"`
	Time      time.Time `json:"time,omitempty"`
}

type ArtifactListResponse struct {
	APIVersion string        `json:"api_version"`
	Meta       ResponseMeta  `json:"meta"`
	Artifacts  []ArtifactDTO `json:"artifacts"`
}

type ArtifactDTO struct {
	ID               string    `json:"id"`
	Type             string    `json:"type"`
	Category         string    `json:"category"`
	RunNumber        int       `json:"run_number"`
	Phase            string    `json:"phase,omitempty"`
	Iteration        int       `json:"iteration,omitempty"`
	Path             string    `json:"path,omitempty"`
	Size             int64     `json:"size,omitempty"`
	ModifiedAt       time.Time `json:"modified_at,omitempty"`
	ContentAvailable bool      `json:"content_available"`
}

type TextContentResponse struct {
	APIVersion string       `json:"api_version"`
	Meta       ResponseMeta `json:"meta"`
	ID         string       `json:"id"`
	Offset     int64        `json:"offset"`
	Limit      int64        `json:"limit"`
	Size       int64        `json:"size"`
	Text       string       `json:"text"`
	Truncated  bool         `json:"truncated"`
}

type LivePreviewResponse struct {
	APIVersion string                 `json:"api_version"`
	Meta       ResponseMeta           `json:"meta"`
	Feature    FeatureSummary         `json:"feature"`
	Session    *SessionSummaryDTO     `json:"session,omitempty"`
	Activity   string                 `json:"activity"`
	Attention  []ControlRequestDTO    `json:"attention,omitempty"`
	Context    ContextDTO             `json:"context"`
	Timing     TimingDTO              `json:"timing"`
	Cost       CostDTO                `json:"cost"`
	Transcript []TranscriptMessageDTO `json:"transcript"`
}

type ContextDTO struct {
	Percentage int `json:"percentage"`
}

type SessionListResponse = serverapi.SessionListResponse

type SessionDetailResponse struct {
	APIVersion string           `json:"api_version"`
	Meta       ResponseMeta     `json:"meta"`
	Session    SessionDetailDTO `json:"session"`
}

type SessionSummaryDTO = serverapi.SessionSummary

type SessionDetailDTO struct {
	SessionSummaryDTO
	TranscriptCursor CursorDTO           `json:"transcript_cursor"`
	PendingControls  []ControlRequestDTO `json:"pending_controls"`
	InitialPrompt    string              `json:"initial_prompt,omitempty"`
	CanAttach        bool                `json:"can_attach"`
	LogAvailable     bool                `json:"log_available"`
	SafeError        string              `json:"safe_error,omitempty"`
}

type CursorDTO = serverapi.Cursor

type UsageDTO = serverapi.Usage

type TranscriptResponse struct {
	APIVersion string                 `json:"api_version"`
	Meta       ResponseMeta           `json:"meta"`
	Cursor     CursorDTO              `json:"cursor"`
	Messages   []TranscriptMessageDTO `json:"messages"`
}

type SessionOutputResponse = serverapi.SessionOutputResponse

// SessionOutputChunk is one record delivered over /output/stream — a single
// row from the session's transcript (the same TranscriptMessageDTO shape and
// index space handleTranscript and the client's snapshot-refresh
// reconciliation use), not a raw log byte window.
type SessionOutputChunk struct {
	APIVersion string               `json:"api_version"`
	SessionID  string               `json:"session_id"`
	Index      int                  `json:"index"`
	Message    TranscriptMessageDTO `json:"message"`
	Done       bool                 `json:"done,omitempty"`
}

type TranscriptMessageDTO struct {
	Index              int            `json:"index"`
	BlockIndex         int            `json:"block_index,omitempty"`
	Role               string         `json:"role"`
	Type               string         `json:"type"`
	Text               string         `json:"text,omitempty"`
	Tool               string         `json:"tool,omitempty"`
	Status             string         `json:"status,omitempty"`
	Redacted           bool           `json:"redacted,omitempty"`
	LocallyAppended    bool           `json:"locally_appended,omitempty"`
	AutoPicked         bool           `json:"auto_picked,omitempty"`
	AutoPickQuestion   string         `json:"auto_pick_question,omitempty"`
	AutoPickConfidence float64        `json:"auto_pick_confidence,omitempty"`
	FileChange         *FileChangeDTO `json:"file_change,omitempty"`
	ToolCall           *ToolCallDTO   `json:"tool_call,omitempty"`
	Task               *TaskDTO       `json:"task,omitempty"`
}

type ToolCallDTO struct {
	Summary string `json:"summary,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

type TaskDTO struct {
	ID           string `json:"id,omitempty"`
	ToolUseID    string `json:"tool_use_id,omitempty"`
	Description  string `json:"description,omitempty"`
	TaskType     string `json:"task_type,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	LastToolName string `json:"last_tool_name,omitempty"`
	Status       string `json:"status,omitempty"`
	Summary      string `json:"summary,omitempty"`
	OutputFile   string `json:"output_file,omitempty"`
}

type FileChangeDTO struct {
	Path         string `json:"path,omitempty"`
	OldPath      string `json:"old_path,omitempty"`
	Operation    string `json:"operation,omitempty"`
	Detail       string `json:"detail,omitempty"`
	AddedLines   int    `json:"added_lines,omitempty"`
	RemovedLines int    `json:"removed_lines,omitempty"`
	HasDiffPatch bool   `json:"has_diff_patch,omitempty"`
}

type ReviewCommentsFetchResponse struct {
	APIVersion string             `json:"api_version"`
	FeatureID  string             `json:"feature_id"`
	Repo       string             `json:"repo"`
	Mode       string             `json:"mode,omitempty"`
	Comments   []ReviewCommentDTO `json:"comments"`
}

type ReviewCommentDTO struct {
	ID        int    `json:"id"`
	Type      string `json:"type,omitempty"`
	RepoName  string `json:"repo_name,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	Body      string `json:"body,omitempty"`
	UserLogin string `json:"user_login,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	DiffHunk  string `json:"diff_hunk,omitempty"`
	InReplyTo int    `json:"in_reply_to_id,omitempty"`
}

type ActionResponseMeta struct {
	APIVersion string `json:"api_version"`
}

func (m *ActionResponseMeta) setAPIVersion() {
	if m.APIVersion == "" {
		m.APIVersion = APIVersion
	}
}

type CreateFeatureResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
}

type FeatureStartResponse struct {
	ActionResponseMeta
	FeatureID  string   `json:"feature_id"`
	Result     string   `json:"result"`
	Phase      string   `json:"phase,omitempty"`
	SessionIDs []string `json:"session_ids,omitempty"`
}

type FeatureStopResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
}

type FeatureRestartResponse struct {
	ActionResponseMeta
	FeatureID      string   `json:"feature_id"`
	Result         string   `json:"result"`
	Phase          string   `json:"phase,omitempty"`
	Dispatch       string   `json:"dispatch,omitempty"`
	RepoCycleCount int      `json:"repo_cycle_count,omitempty"`
	RefactorCount  int      `json:"refactor_count,omitempty"`
	SessionIDs     []string `json:"session_ids,omitempty"`
}

type ReviewDecisionResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Decision  string `json:"decision"`
	Result    string `json:"result"`
}

type FeatureConfigUpdateResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
}

type NeedUserInputDecisionResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Decision  string `json:"decision"`
	Result    string `json:"result"`
}

type NeedUserInputDraftResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
}

type InputNotificationsToggleResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
	Muted     bool   `json:"muted"`
}

type PermissionAnswerResponse struct {
	ActionResponseMeta
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
	Result    string `json:"result"`
}

type AskUserAnswerResponse struct {
	ActionResponseMeta
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id"`
	Result    string `json:"result"`
}

type HelpSendResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
}

type ChatStartResponse struct {
	ActionResponseMeta
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
}

type RuntimeConfigUpdateResponse struct {
	ActionResponseMeta
	Result string `json:"result"`
}

type PublishFeatureResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
}

type PublishDescriptionResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Result    string `json:"result"`
}

type MergeFeatureResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
}

type RewindFeatureResponse struct {
	ActionResponseMeta
	FeatureID       string `json:"feature_id"`
	Result          string `json:"result"`
	TargetPhase     string `json:"target_phase,omitempty"`
	EffectivePhase  string `json:"effective_phase,omitempty"`
	RoadmapPhase    int    `json:"roadmap_phase,omitempty"`
	WarningCount    int    `json:"warning_count,omitempty"`
	UpgradePipeline string `json:"upgrade_pipeline,omitempty"`
}

type RetryFeatureResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
}

type RebaseStartResponse struct {
	ActionResponseMeta
	FeatureID     string   `json:"feature_id"`
	Result        string   `json:"result"`
	Repo          string   `json:"repo,omitempty"`
	CycleType     string   `json:"cycle_type"`
	RebaseTarget  string   `json:"rebase_target,omitempty"`
	ConflictFiles []string `json:"conflict_files,omitempty"`
	SessionID     string   `json:"session_id,omitempty"`
}

type ReviewCommentsStartResponse struct {
	ActionResponseMeta
	FeatureID    string `json:"feature_id"`
	Result       string `json:"result"`
	Repo         string `json:"repo"`
	Mode         string `json:"mode"`
	CycleType    string `json:"cycle_type"`
	CommentCount int    `json:"comment_count,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Source       string `json:"source,omitempty"`
}

type TweakStartResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
	CycleType string `json:"cycle_type"`
	SessionID string `json:"session_id,omitempty"`
}

type TweakFinishResponse struct {
	ActionResponseMeta
	FeatureID  string `json:"feature_id"`
	Result     string `json:"result"`
	Decision   string `json:"decision"`
	HadChanges bool   `json:"had_changes,omitempty"`
}

type RefactorStartResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
	Repo      string `json:"repo,omitempty"`
	CycleType string `json:"cycle_type"`
	Pipeline  string `json:"pipeline,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type RefactorRestartResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
	Repo      string `json:"repo,omitempty"`
	CycleType string `json:"cycle_type"`
	Pipeline  string `json:"pipeline,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type MarkDoneResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
}

type CleanupFeatureResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
	Target    string `json:"target,omitempty"`
}

type DeleteFeatureResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
	Result    string `json:"result"`
}

type RecoveryActionResponse struct {
	ActionResponseMeta
	Result string `json:"result"`
}

type ShutdownResponse struct {
	ActionResponseMeta
	Result string `json:"result"`
}

type SSEEventDTO = serverapi.SSEEvent

type ResourceDTO = serverapi.Resource

type DiscoveryRecord struct {
	SchemaVersion int             `json:"schema_version"`
	APIVersion    string          `json:"api_version"`
	BaseURL       string          `json:"base_url"`
	Epoch         string          `json:"epoch,omitempty"`
	AuthToken     string          `json:"auth_token,omitempty"`
	Runtime       RuntimeIdentity `json:"runtime"`
	LaunchPolicy  LaunchPolicy    `json:"launch_policy"`
	StartMode     string          `json:"start_mode"`
	PID           int             `json:"pid"`
	PGID          int             `json:"pgid,omitempty"`
	StartedAt     time.Time       `json:"started_at"`
	PublishedAt   time.Time       `json:"published_at"`
	Owner         OwnerDTO        `json:"owner"`
}

type DiscoveryDecision struct {
	AlreadyRunning bool
	Replace        bool
	Reason         string
	Record         DiscoveryRecord
}
