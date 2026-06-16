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
)

const APIVersion = "v1"

const discoveryFilename = ".agentico-server.json"

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
	Runtime         RuntimeIdentity
	LaunchPolicy    LaunchPolicy
	StartMode       string
	Owner           instancelock.Owner
	Features        FeatureLister
	FeatureStore    FeatureReader
	Config          *config.Config
	Registry        *llm.Registry
	Sessions        ports.SessionManager
	Events          <-chan interface{}
	DomainEvents    <-chan ports.Event
	Mutations       MutationTarget
	RequestShutdown func()
}

type HandlerOptions struct {
	Runtime         RuntimeIdentity
	LaunchPolicy    LaunchPolicy
	StartedAt       time.Time
	Owner           instancelock.Owner
	Features        FeatureLister
	FeatureStore    FeatureReader
	Config          *config.Config
	Registry        *llm.Registry
	Sessions        ports.SessionManager
	Events          <-chan interface{}
	DomainEvents    <-chan ports.Event
	Mutations       MutationTarget
	RequestShutdown func()
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

type ResponseMeta struct {
	Revision    string    `json:"revision"`
	GeneratedAt time.Time `json:"generated_at"`
	CacheHit    bool      `json:"cache_hit,omitempty"`
}

type ErrorResponse struct {
	APIVersion string   `json:"api_version"`
	Error      ErrorDTO `json:"error"`
}

type ErrorDTO struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Status  int            `json:"status"`
	Target  map[string]any `json:"target,omitempty"`
}

// OwnerDTO is the public process-owner metadata safe to expose through REST,
// MCP, and discovery records.
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

type FeatureListResponse struct {
	APIVersion string           `json:"api_version"`
	Meta       ResponseMeta     `json:"meta"`
	Features   []FeatureSummary `json:"features"`
	Warnings   []WarningDTO     `json:"warnings,omitempty"`
}

type FeatureSummary struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Slug         string          `json:"slug"`
	Status       string          `json:"status"`
	CurrentPhase string          `json:"current_phase"`
	ActiveRun    int             `json:"active_run"`
	RunCount     int             `json:"run_count"`
	Repos        []string        `json:"repos"`
	CreatedAt    time.Time       `json:"created_at"`
	Checkpoints  CheckpointsDTO  `json:"checkpoints"`
	Progress     FeatureProgress `json:"progress"`
	Warnings     []WarningDTO    `json:"warnings,omitempty"`
}

type FeatureProgress struct {
	CurrentIteration    int    `json:"current_iteration,omitempty"`
	CurrentRoadmapPhase int    `json:"current_roadmap_phase,omitempty"`
	TotalRoadmapPhases  int    `json:"total_roadmap_phases,omitempty"`
	CurrentPhaseStatus  string `json:"current_phase_status,omitempty"`
}

type WarningDTO struct {
	Code      string `json:"code"`
	FeatureID string `json:"feature_id,omitempty"`
	Message   string `json:"message"`
}

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

type ActionDTO struct {
	ID              string                    `json:"id"`
	Enabled         bool                      `json:"enabled"`
	Scope           ActionScopeDTO            `json:"scope"`
	RequiredInputs  []ActionInputDTO          `json:"required_inputs"`
	DisabledReasons []ActionDisabledReasonDTO `json:"disabled_reasons,omitempty"`
}

type ActionScopeDTO struct {
	Type          string `json:"type"`
	RepoSelection string `json:"repo_selection,omitempty"`
	CycleType     string `json:"cycle_type,omitempty"`
}

type ActionInputDTO struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Required  bool     `json:"required"`
	Options   []string `json:"options,omitempty"`
	MaxLength int      `json:"max_length,omitempty"`
}

type ActionDisabledReasonDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RunSummaryDTO struct {
	RunNumber       int        `json:"run_number"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	SealedAt        *time.Time `json:"sealed_at,omitempty"`
	SealReason      string     `json:"seal_reason,omitempty"`
	CurrentPhase    string     `json:"current_phase,omitempty"`
	PhaseStatus     string     `json:"phase_status,omitempty"`
	Iteration       int        `json:"iteration,omitempty"`
	RoadmapPhase    int        `json:"roadmap_phase,omitempty"`
	RoadmapTotal    int        `json:"roadmap_total,omitempty"`
	ArtifactCount   int        `json:"artifact_count"`
	HasNeedUserGate bool       `json:"has_need_user_gate,omitempty"`
}

type RepoStatusDTO struct {
	Name        string `json:"name"`
	Touched     bool   `json:"touched"`
	PRURL       string `json:"pr_url,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	Publishable bool   `json:"publishable"`
	CycleType   string `json:"cycle_type,omitempty"`
	CycleStatus string `json:"cycle_status,omitempty"`
}

type CycleDTO struct {
	Type      string `json:"type,omitempty"`
	Status    string `json:"status,omitempty"`
	Count     int    `json:"count,omitempty"`
	Iteration int    `json:"iteration,omitempty"`
}

type TimingDTO struct {
	TotalSeconds int64            `json:"total_seconds"`
	ByPhase      map[string]int64 `json:"by_phase"`
}

type CostDTO struct {
	TotalUSD float64            `json:"total_usd"`
	ByPhase  map[string]float64 `json:"by_phase"`
}

type ReviewGateDTO struct {
	ReviewingGate     bool              `json:"reviewing_gate"`
	ReviewFixing      bool              `json:"review_fixing"`
	ValidatingPlan    bool              `json:"validating_plan"`
	ValidatorStatuses map[string]string `json:"validator_statuses,omitempty"`
}

type FailureDTO struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
}

type NeedInputGateDTO struct {
	FeatureID string `json:"feature_id,omitempty"`
	Open      bool   `json:"open"`
	Scope     string `json:"scope,omitempty"`
	Iteration int    `json:"iteration,omitempty"`
}

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

func FeatureConfigMutationFromDTO(dto FeatureConfigDTO) FeatureConfigMutationRequest {
	return FeatureConfigMutationRequest{
		Models:      dto.Models,
		Inquireness: dto.Inquireness,
		Checkpoints: feature.Checkpoints{
			InquiryReview:   dto.Checkpoints.InquiryReview,
			ResearchReview:  dto.Checkpoints.ResearchReview,
			DesignReview:    dto.Checkpoints.DesignReview,
			RoadmapReview:   dto.Checkpoints.RoadmapReview,
			PhasePlanReview: dto.Checkpoints.PhasePlanReview,
			ManualPublish:   dto.Checkpoints.ManualPublish,
		},
		Pipeline: feature.PipelineProfile(dto.Pipeline),
	}
}

type CheckpointsDTO struct {
	InquiryReview   bool `json:"inquiry_review"`
	ResearchReview  bool `json:"research_review"`
	DesignReview    bool `json:"design_review"`
	RoadmapReview   bool `json:"roadmap_review"`
	PhasePlanReview bool `json:"phase_plan_review"`
	ManualPublish   bool `json:"manual_publish"`
	DraftPublish    bool `json:"draft_publish"`
}

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

type ControlRequestDTO struct {
	RequestID string               `json:"request_id"`
	SessionID string               `json:"session_id,omitempty"`
	FeatureID string               `json:"feature_id,omitempty"`
	Phase     string               `json:"phase,omitempty"`
	ToolName  string               `json:"tool_name"`
	Status    string               `json:"status"`
	Summary   string               `json:"summary,omitempty"`
	Questions []AskUserQuestionDTO `json:"questions,omitempty"`
}

type AskUserQuestionDTO struct {
	Question    string             `json:"question,omitempty"`
	Header      string             `json:"header,omitempty"`
	MultiSelect bool               `json:"multi_select,omitempty"`
	Options     []AskUserOptionDTO `json:"options,omitempty"`
}

type AskUserOptionDTO struct {
	Label       string   `json:"label,omitempty"`
	Description string   `json:"description,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
}

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

type SessionListResponse struct {
	APIVersion string              `json:"api_version"`
	Meta       ResponseMeta        `json:"meta"`
	Sessions   []SessionSummaryDTO `json:"sessions"`
}

type SessionDetailResponse struct {
	APIVersion string           `json:"api_version"`
	Meta       ResponseMeta     `json:"meta"`
	Session    SessionDetailDTO `json:"session"`
}

type SessionSummaryDTO struct {
	ID         string    `json:"id"`
	FeatureID  string    `json:"feature_id"`
	Phase      string    `json:"phase"`
	Repo       string    `json:"repo,omitempty"`
	Kind       string    `json:"kind"`
	Label      string    `json:"label,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	Model      string    `json:"model,omitempty"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	Iteration  int       `json:"iteration,omitempty"`
	ContextPct int       `json:"context_percentage,omitempty"`
	Usage      UsageDTO  `json:"usage"`
}

type SessionDetailDTO struct {
	SessionSummaryDTO
	TranscriptCursor CursorDTO           `json:"transcript_cursor"`
	PendingControls  []ControlRequestDTO `json:"pending_controls"`
	CanAttach        bool                `json:"can_attach"`
	LogAvailable     bool                `json:"log_available"`
	SafeError        string              `json:"safe_error,omitempty"`
}

type CursorDTO struct {
	Total int `json:"total"`
	Start int `json:"start"`
	End   int `json:"end"`
}

type UsageDTO struct {
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

type TranscriptResponse struct {
	APIVersion string                 `json:"api_version"`
	Meta       ResponseMeta           `json:"meta"`
	Cursor     CursorDTO              `json:"cursor"`
	Messages   []TranscriptMessageDTO `json:"messages"`
}

type TranscriptMessageDTO struct {
	Index    int    `json:"index"`
	Role     string `json:"role"`
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Status   string `json:"status,omitempty"`
	Redacted bool   `json:"redacted,omitempty"`
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

type RuntimeConfigUpdateResponse struct {
	ActionResponseMeta
	Result string `json:"result"`
}

type PublishFeatureResponse struct {
	ActionResponseMeta
	FeatureID string `json:"feature_id"`
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

type SSEEventDTO struct {
	APIVersion       string      `json:"api_version"`
	ID               string      `json:"id"`
	Kind             string      `json:"kind"`
	At               time.Time   `json:"at"`
	Resource         ResourceDTO `json:"resource"`
	Revision         string      `json:"revision,omitempty"`
	SnapshotRequired bool        `json:"snapshot_required"`
	Summary          string      `json:"summary,omitempty"`
}

type ResourceDTO struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	FeatureID string `json:"feature_id,omitempty"`
	Phase     string `json:"phase,omitempty"`
}

type DiscoveryRecord struct {
	SchemaVersion int             `json:"schema_version"`
	APIVersion    string          `json:"api_version"`
	BaseURL       string          `json:"base_url"`
	MCP           MCPMetadata     `json:"mcp"`
	Runtime       RuntimeIdentity `json:"runtime"`
	LaunchPolicy  LaunchPolicy    `json:"launch_policy"`
	StartMode     string          `json:"start_mode"`
	PID           int             `json:"pid"`
	PGID          int             `json:"pgid,omitempty"`
	StartedAt     time.Time       `json:"started_at"`
	PublishedAt   time.Time       `json:"published_at"`
	Owner         OwnerDTO        `json:"owner"`
}

type MCPMetadata struct {
	Transport      string `json:"transport"`
	Path           string `json:"path"`
	Endpoint       string `json:"endpoint"`
	RESTAPIVersion string `json:"rest_api_version"`
}

type DiscoveryDecision struct {
	AlreadyRunning bool
	Replace        bool
	Reason         string
	Record         DiscoveryRecord
}
