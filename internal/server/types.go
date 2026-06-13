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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/instancelock"
)

const APIVersion = "v1"

const discoveryFilename = ".agentico-server.json"

type RuntimeIdentity struct {
	RuntimeDir string `json:"runtime_dir"`
	StateDir   string `json:"state_dir"`
	Config     string `json:"config_path"`
}

type Options struct {
	Runtime   RuntimeIdentity
	StartMode string
	Owner     instancelock.Owner
	Features  FeatureLister
}

type HandlerOptions struct {
	Runtime   RuntimeIdentity
	StartedAt time.Time
	Features  FeatureLister
}

type FeatureLister interface {
	List() ([]*feature.Feature, error)
}

type HealthResponse struct {
	APIVersion string          `json:"api_version"`
	Status     string          `json:"status"`
	Runtime    RuntimeIdentity `json:"runtime"`
	StartedAt  time.Time       `json:"started_at"`
	ServerTime time.Time       `json:"server_time"`
}

type FeatureListResponse struct {
	APIVersion string           `json:"api_version"`
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
	Progress     FeatureProgress `json:"progress"`
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

type DiscoveryRecord struct {
	SchemaVersion int                `json:"schema_version"`
	APIVersion    string             `json:"api_version"`
	BaseURL       string             `json:"base_url"`
	Runtime       RuntimeIdentity    `json:"runtime"`
	StartMode     string             `json:"start_mode"`
	PID           int                `json:"pid"`
	PGID          int                `json:"pgid,omitempty"`
	StartedAt     time.Time          `json:"started_at"`
	PublishedAt   time.Time          `json:"published_at"`
	Owner         instancelock.Owner `json:"owner"`
}

type DiscoveryDecision struct {
	AlreadyRunning bool
	Replace        bool
	Reason         string
	Record         DiscoveryRecord
}
