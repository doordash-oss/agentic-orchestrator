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

//go:generate go run ../../tools/openapi-generate

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

// ChatSessionID is the stable utility-session identity used by the AMA chat.
const ChatSessionID = "__chat__"

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
	// PersistProviderModelCatalog writes a successfully discovered provider
	// catalog before the server installs it in memory. Nil keeps live refreshes
	// in memory only.
	PersistProviderModelCatalog func(llm.LLMProvider, []llm.ModelInfo) error
	// InitGitRepository overrides the git-init implementation used by the
	// workspace repository-initialization endpoint. Nil means the default
	// git adapter (internal/git.InitRepository).
	InitGitRepository func(path string) error
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
	DisableHostValidation       bool
	Features                    FeatureLister
	FeatureStore                FeatureReader
	Freshness                   RepoFreshnessProvider
	Config                      *config.Config
	Registry                    *llm.Registry
	Sessions                    ports.SessionManager
	Events                      <-chan interface{}
	DomainEvents                <-chan ports.Event
	Mutations                   MutationTarget
	RequestShutdown             func()
	PersistProviderModelCatalog func(llm.LLMProvider, []llm.ModelInfo) error
	// InitGitRepository overrides the git-init implementation used by the
	// workspace repository-initialization endpoint. Nil means the default
	// git adapter (internal/git.InitRepository).
	InitGitRepository func(path string) error
}

type FeatureLister interface {
	List() ([]*feature.Feature, error)
}

type FeatureReader interface {
	FeatureLister
	Load(id string) (*feature.Feature, error)
	LoadRun(featureID string, runNumber int) (*feature.Run, error)
	RunDir(featureID string, runNumber int) string
	ListRuns(featureID string) ([]int, error)
}

type ErrorDTO = Error

// OwnerDTO is the public process-owner metadata safe to expose through REST and
// discovery records.
type OwnerDTO = Owner

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

type WarningDTO = Warning

type FeatureDetailDTO = FeatureDetail

type ActionDTO = Action

type ActionScopeDTO = ActionScope

type ActionInputDTO = ActionInput

type ActionDisabledReasonDTO = ActionDisabledReason

type RunSummaryDTO = RunSummary

type SetupDTO = Setup

type SetupTaskDTO = SetupTask

type RepoStatusDTO = RepoStatus

type CycleDTO = Cycle

type TimingDTO = Timing

type CostDTO = Cost

type ReviewGateDTO = ReviewGate

type FailureDTO = Failure

type NeedInputGateDTO = NeedUserInputGate

type NeedUserInputQuestionDTO = NeedUserInputQuestion

type RecoveryItemDTO = RecoveryItem

type RecoveryActionRequest struct {
	SnapshotID string            `json:"snapshot_id"`
	Actions    map[string]string `json:"actions"`
}

type ConfigRepoDTO = ConfigRepo

type FeatureDefaultsDTO = FeatureDefaults

type NotificationConfigDTO = NotificationConfig

type ObservabilityDTO = Observability

type FeatureConfigDTO = FeatureConfig

type CheckpointsDTO = Checkpoints

type PublishabilityDTO = Publishability

type ModelDTO = Model

type ControlRequestDTO = ControlRequest

type PermissionRememberPreviewDTO = PermissionRememberPreview

type AskUserQuestionDTO = AskUserQuestion

type AskUserOptionDTO = AskUserOption

type HelpQueueDTO = HelpQueue

type ArtifactDTO = Artifact

type ContextDTO = Context

type SessionSummaryDTO = SessionSummary

type SessionDetailDTO = SessionDetail

type CursorDTO = Cursor

type UsageDTO = Usage

type TranscriptMessageDTO = TranscriptMessage

type ToolCallDTO = ToolCall

type TaskDTO = Task

type FileChangeDTO = FileChange

type ReviewCommentDTO = ReviewComment

type SSEEventDTO = SSEEvent

type ResourceDTO = Resource

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
