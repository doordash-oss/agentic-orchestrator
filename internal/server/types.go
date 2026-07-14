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

type RuntimeIdentity = serverapi.RuntimeIdentity

type LaunchPolicy = serverapi.LaunchPolicy

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
type OwnerDTO = serverapi.Owner

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

type HealthResponse = serverapi.HealthResponse

type FeatureListResponse = serverapi.FeatureListResponse

type FeatureSummary = serverapi.FeatureSummary

type FeatureProgress = serverapi.FeatureProgress

type WarningDTO = serverapi.Warning

type FeatureDetailResponse = serverapi.FeatureDetailResponse

type FeatureDetailDTO = serverapi.FeatureDetail

type ActionDTO = serverapi.Action

type ActionScopeDTO = serverapi.ActionScope

type ActionInputDTO = serverapi.ActionInput

type ActionDisabledReasonDTO = serverapi.ActionDisabledReason

type RunSummaryDTO = serverapi.RunSummary

type SetupDTO = serverapi.Setup

type SetupTaskDTO = serverapi.SetupTask

type RepoStatusDTO = serverapi.RepoStatus

type CycleDTO = serverapi.Cycle

type TimingDTO = serverapi.Timing

type CostDTO = serverapi.Cost

type ReviewGateDTO = serverapi.ReviewGate

type FailureDTO = serverapi.Failure

type NeedInputGateDTO = serverapi.NeedUserInputGate

type NeedUserInputQuestionDTO = serverapi.NeedUserInputQuestion

type RecoverySnapshotResponse = serverapi.RecoverySnapshotResponse

type RecoveryItemDTO = serverapi.RecoveryItem

type RecoveryActionRequest struct {
	SnapshotID string            `json:"snapshot_id"`
	Actions    map[string]string `json:"actions"`
}

type RuntimeConfigResponse = serverapi.RuntimeConfigResponse

type WorkspaceBrowseResponse = serverapi.WorkspaceBrowseResponse

type WorkspaceBrowseEntryDTO = serverapi.WorkspaceBrowseEntry

type ConfigRepoDTO = serverapi.ConfigRepo

type FeatureDefaultsDTO = serverapi.FeatureDefaults

type NotificationConfigDTO = serverapi.NotificationConfig

type ObservabilityDTO = serverapi.Observability

type FeatureConfigResponse = serverapi.FeatureConfigResponse

type FeatureConfigDTO = serverapi.FeatureConfig

type FeatureConfigInputNotifications = serverapi.FeatureConfigInputNotifications

type CheckpointsDTO = serverapi.Checkpoints

type PublishabilityDTO = serverapi.Publishability

type ModelCatalogResponse = serverapi.ModelCatalogResponse

type ModelDTO = serverapi.Model

type PromptSnapshotResponse = serverapi.PromptSnapshotResponse

type PermissionSnapshotResponse = serverapi.PermissionSnapshotResponse

type ControlRequestDTO = serverapi.ControlRequest

type PermissionRememberPreviewDTO = serverapi.PermissionRememberPreview

type AskUserQuestionDTO = serverapi.AskUserQuestion

type AskUserOptionDTO = serverapi.AskUserOption

type HelpQueueDTO = serverapi.HelpQueue

type ArtifactListResponse = serverapi.ArtifactListResponse

type ArtifactDTO = serverapi.Artifact

type TextContentResponse = serverapi.TextContentResponse

type LivePreviewResponse = serverapi.LivePreviewResponse

type ContextDTO = serverapi.Context

type SessionListResponse = serverapi.SessionListResponse

type SessionDetailResponse = serverapi.SessionDetailResponse

type SessionSummaryDTO = serverapi.SessionSummary

type SessionDetailDTO = serverapi.SessionDetail

type CursorDTO = serverapi.Cursor

type UsageDTO = serverapi.Usage

type TranscriptResponse = serverapi.TranscriptResponse

type SessionOutputResponse = serverapi.SessionOutputResponse

// SessionOutputChunk is one record delivered over /output/stream — a single
// row from the session's transcript (the same TranscriptMessageDTO shape and
// index space handleTranscript and the client's snapshot-refresh
// reconciliation use), not a raw log byte window.
type SessionOutputChunk = serverapi.SessionOutputChunk

type TranscriptMessageDTO = serverapi.TranscriptMessage

type ToolCallDTO = serverapi.ToolCall

type TaskDTO = serverapi.Task

type FileChangeDTO = serverapi.FileChange

type ReviewCommentsFetchResponse = serverapi.ReviewCommentsFetchResponse

type ReviewCommentDTO = serverapi.ReviewComment

type CreateFeatureResponse = serverapi.CreateFeatureResponse

type FeatureStartResponse = serverapi.FeatureStartResponse

type FeatureStopResponse = serverapi.FeatureStopResponse

type FeatureRestartResponse = serverapi.FeatureRestartResponse

type ReviewDecisionResponse = serverapi.ReviewDecisionResponse

type ReviewSessionResponse = serverapi.ReviewSessionResponse

type ReviewDraftUpdateRequest = serverapi.ReviewDraftUpdateRequest

type ReviewSessionDecisionRequest = serverapi.ReviewSessionDecisionRequest

type ReviewSessionDecisionResponse = serverapi.ReviewSessionDecisionResponse

type FeatureConfigUpdateResponse = serverapi.FeatureConfigUpdateResponse

type NeedUserInputDecisionResponse = serverapi.NeedUserInputDecisionResponse

type NeedUserInputDraftResponse = serverapi.NeedUserInputDraftResponse

type PermissionAnswerResponse = serverapi.PermissionAnswerResponse

type AskUserAnswerResponse = serverapi.AskUserAnswerResponse

type HelpSendResponse = serverapi.HelpSendResponse

type ChatStartResponse = serverapi.ChatStartResponse

type RuntimeConfigUpdateResponse = serverapi.RuntimeConfigUpdateResponse

type PublishFeatureResponse = serverapi.PublishFeatureResponse

type PublishDescriptionResponse = serverapi.PublishDescriptionResponse

type MergeFeatureResponse = serverapi.MergeFeatureResponse

type RewindFeatureResponse = serverapi.RewindFeatureResponse

type RetryFeatureResponse = serverapi.RetryFeatureResponse

type RebaseStartResponse = serverapi.RebaseStartResponse

type ReviewCommentsStartResponse = serverapi.ReviewCommentsStartResponse

type RefactorStartResponse = serverapi.RefactorStartResponse

type RefactorRestartResponse = serverapi.RefactorRestartResponse

type MarkDoneResponse = serverapi.MarkDoneResponse

type CleanupFeatureResponse = serverapi.CleanupFeatureResponse

type DeleteFeatureResponse = serverapi.DeleteFeatureResponse

type RecoveryActionResponse = serverapi.RecoveryActionResponse

type ShutdownResponse = serverapi.ShutdownResponse

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
