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
	// ListenAddr overrides the TCP bind address (normalized via
	// ResolveListenAddr). Empty keeps the default ephemeral 127.0.0.1 bind.
	ListenAddr string
	// RuntimePolicy overrides the declared runtime policy selected from the
	// resolved listen address. Empty keeps the bind-driven selection. Only
	// tests exercise this; production derives the policy from ListenAddr.
	RuntimePolicy string
	// Name is the resolved server display name surfaced in the startup
	// line, health payload, and discovery record.
	Name string
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
	// PersistProviderModelCatalog writes a successfully discovered provider
	// catalog before the server installs it in memory. Nil keeps live refreshes
	// in memory only.
	PersistProviderModelCatalog func(llm.LLMProvider, []llm.ModelInfo) error
	// InitGitRepository overrides the git-init implementation used by the
	// workspace repository-initialization endpoint. Nil means the default
	// git adapter (internal/git.InitRepository).
	InitGitRepository func(path string) error
	// Worktrees inspects parent worktrees so a dirty refactor entry can
	// attach the same structured diagnostics the launch-time error carries.
	// Nil is tolerated: the dirty_parent disabled reason then ships without
	// a diagnostics target.
	Worktrees feature.WorktreeOps
}

type HandlerOptions struct {
	Runtime      RuntimeIdentity
	LaunchPolicy LaunchPolicy
	StartedAt    time.Time
	Owner        instancelock.Owner
	AuthToken    string
	// Name is the resolved server display name reported by /api/v1/health.
	Name string
	// DisableHostValidation turns off the Host-header check. Host validation
	// defaults to ON — only tests exercising something other than host
	// validation itself should set this to true.
	DisableHostValidation bool
	// RuntimePolicy is the server's declared runtime policy (loopback or
	// network). Empty defaults to the loopback policy, which enforces the
	// loopback-only Host-header rule.
	RuntimePolicy               string
	Features                    FeatureLister
	FeatureStore                FeatureReader
	Freshness                   RepoFreshnessProvider
	Config                      *config.Config
	Registry                    *llm.Registry
	Sessions                    ports.SessionManager
	Events                      <-chan interface{}
	DomainEvents                <-chan ports.Event
	Mutations                   MutationTarget
	PersistProviderModelCatalog func(llm.LLMProvider, []llm.ModelInfo) error
	// InitGitRepository overrides the git-init implementation used by the
	// workspace repository-initialization endpoint. Nil means the default
	// git adapter (internal/git.InitRepository).
	InitGitRepository func(path string) error
	Worktrees         feature.WorktreeOps
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

type RelationshipReader interface {
	RelationshipChildren(parentID string) (*feature.RelationshipChildren, error)
}

// BulkRelationshipReader resolves every parent's children in one store pass
// so list-shaped endpoints avoid a per-parent directory rescan.
type BulkRelationshipReader interface {
	AllRelationshipChildren() (map[string]*feature.RelationshipChildren, error)
}

// OwnerFromInstanceOwner drops local filesystem paths from lock owner
// metadata before it crosses public API or discovery boundaries.
func OwnerFromInstanceOwner(owner instancelock.Owner) Owner {
	return Owner{
		PID:       owner.PID,
		PGID:      owner.PGID,
		StartedAt: owner.StartedAt,
		Version:   owner.Version,
	}
}

type RecoveryActionRequest struct {
	SnapshotID string            `json:"snapshot_id"`
	Actions    map[string]string `json:"actions"`
}

type DiscoveryRecord struct {
	SchemaVersion int             `json:"schema_version"`
	APIVersion    string          `json:"api_version"`
	BaseURL       string          `json:"base_url"`
	Epoch         string          `json:"epoch,omitempty"`
	AuthToken     string          `json:"auth_token,omitempty"`
	Name          string          `json:"name,omitempty"`
	Runtime       RuntimeIdentity `json:"runtime"`
	LaunchPolicy  LaunchPolicy    `json:"launch_policy"`
	StartMode     string          `json:"start_mode"`
	PID           int             `json:"pid"`
	PGID          int             `json:"pgid,omitempty"`
	StartedAt     time.Time       `json:"started_at"`
	PublishedAt   time.Time       `json:"published_at"`
	Owner         Owner           `json:"owner"`
}

type DiscoveryDecision struct {
	AlreadyRunning bool
	Replace        bool
	Reason         string
	Record         DiscoveryRecord
}
