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
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// OrchFeatureCreatedMsg is emitted when the orchestrator creates a feature.
type OrchFeatureCreatedMsg struct {
	FeatureID string
	Feature   *feature.Feature
}

// OrchFeatureStartedMsg is emitted when the orchestrator starts a feature.
type OrchFeatureStartedMsg struct {
	FeatureID string
}

// OrchFeatureAdvancedMsg is emitted when a feature advances to a new phase.
type OrchFeatureAdvancedMsg struct {
	FeatureID string
	Phase     feature.Phase
}

// OrchFeatureCompletedMsg is emitted when a feature reaches Done.
type OrchFeatureCompletedMsg struct {
	FeatureID string
}

// OrchFeatureFailedMsg is emitted when a feature transitions to Failed.
type OrchFeatureFailedMsg struct {
	FeatureID string
	Message   string
	Error     error
}

// OrchFeatureInterruptedMsg is emitted when a feature is interrupted.
type OrchFeatureInterruptedMsg struct {
	FeatureID string
}

// OrchPhaseStartedMsg is emitted when a phase begins.
type OrchPhaseStartedMsg struct {
	FeatureID string
	Phase     feature.Phase
}

// OrchPhaseCompletedMsg is emitted when a phase completes.
type OrchPhaseCompletedMsg struct {
	FeatureID string
	Phase     feature.Phase
	Error     error
}

// OrchReviewRequiredMsg is emitted when a review gate blocks progress.
type OrchReviewRequiredMsg struct {
	FeatureID string
	Phase     feature.Phase
}

// OrchPublishStartedMsg is emitted when publish begins.
type OrchPublishStartedMsg struct {
	FeatureID string
}

// OrchPublishCompletedMsg is emitted when publish completes.
type OrchPublishCompletedMsg struct {
	FeatureID string
	Error     error
}

// OrchRepoStatusChangedMsg is emitted when a per-repo implementation status
// changes. RepoName is carried in Message for parity with ports.Event.
type OrchRepoStatusChangedMsg struct {
	FeatureID string
	RepoName  string
}

// OrchRecoveryScannedMsg is emitted after a recovery scan completes.
type OrchRecoveryScannedMsg struct {
	Message string
}

// OrchRecoveryExecutedMsg is emitted after a recovery action runs.
type OrchRecoveryExecutedMsg struct {
	FeatureID string
	Message   string
}

// OrchTweakReviewApprovedMsg is emitted when the feature-level Final Review
// session for a tweak cycle lands an APPROVED verdict on the cumulative
// diff across every Feature.Repos. The TUI routes this to
// completeTweakFinishCmd so the tweak finishes (commit review fixes,
// rebase/push every modified repo, MarkCodeReady / ReturnToPublished,
// clear ActiveCycleType). Rebase conflicts during the final push land on
// RebaseResultMsg via completeTweakFinishCmd's conflict routing.
//
// RepoName is unused because the finish command is feature-level, but remains
// on the message surface for compatibility with legacy fixtures.
type OrchTweakReviewApprovedMsg struct {
	FeatureID string
	RepoName  string
}

// OrchFeatureConfigChangedMsg is emitted after Orchestrator.UpdateFeatureConfig
// successfully writes the three editable per-feature config axes. Carries
// only FeatureID — the TUI reloads the feature from the store to pick up the
// new values (feature.yaml is the source of truth). Typed before/after diffs
// flow through observe.Observer.ConfigChanged, not through this msg.
//
// This is a SECONDARY broadcast (non-blocking, droppable on channel
// saturation). The authoritative overlay-close signal is editConfigResultMsg
// returned by saveConfigCmd.
type OrchFeatureConfigChangedMsg struct {
	FeatureID string
}
