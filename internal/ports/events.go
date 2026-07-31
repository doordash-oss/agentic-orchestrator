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

package ports

import "github.com/doordash-oss/agentic-orchestrator/internal/feature"

// EventType enumerates domain events emitted by the orchestrator.
type EventType int

const (
	FeatureCreated EventType = iota
	FeatureStarted
	FeatureAdvanced
	FeatureCompleted
	FeatureFailed
	FeatureInterrupted
	// FeatureRewound fires after a successful rewind forks a fresh active run.
	// Subscribers should refresh feature detail and run-scoped artifacts because
	// ActiveRun and carried artifact maps may have changed.
	FeatureRewound
	PhaseStarted
	PhaseCompleted
	ReviewRequired
	PublishStarted
	PublishCompleted
	RepoStatusChanged
	RecoveryScanned
	RecoveryExecuted
	SessionOutput
	// FeatureConfigChanged fires after Orchestrator.UpdateFeatureConfig
	// atomically writes the three editable per-feature config axes
	// (Models, Inquireness, Checkpoints). Carries only {Type, FeatureID};
	// the typed before/after diff flows through observe.Observer.ConfigChanged,
	// which owns events.jsonl emission with run_number tagging. The TUI
	// consumes this as OrchFeatureConfigChangedMsg and reloads the feature.
	FeatureConfigChanged
	// NeedUserInputRequired fires when an implement iteration emits
	// `## Iteration State: NEED_USER_INPUT` and the orchestrator persists
	// the gate artifact. The TUI consumes it to surface a `Needs user input`
	// banner. Message carries the agent's gate summary.
	NeedUserInputRequired
	SetupStarted
	SetupProgress
	SetupCompleted
	SetupFailed
	// VerificationProgress fires after the harness durably changes the
	// ordered per-command verification status. API clients use it to refresh
	// feature detail while no agent session is active to provide refresh
	// signals of its own.
	VerificationProgress
	// RuntimeShutdownStarted fires when the orchestrator/runtime has begun a
	// graceful shutdown. SSE consumers use it as a metadata-only signal to
	// refresh authoritative REST snapshots during reconnect.
	RuntimeShutdownStarted
	// RelationshipChildCreated fires after a direct child is durably visible.
	RelationshipChildCreated
	// RelationshipIntegrationChanged fires after durable integration state changes.
	RelationshipIntegrationChanged
	// RelationshipClosed fires after the child outcome and close time are durable.
	RelationshipClosed
	// RelationshipDiscardProgress fires after durable discard progress changes.
	RelationshipDiscardProgress
	// RelationshipCascadeProgress fires after a cascade durably settles into
	// cleanup_pending or attention_required while both records remain visible.
	RelationshipCascadeProgress
	// RelationshipCascadeDeleted fires after cascade completion removed both records.
	RelationshipCascadeDeleted
)

// Event is a typed domain event emitted by the orchestrator.
type Event struct {
	Type      EventType
	FeatureID string
	// ParentID and ChildID are both required on relationship event types.
	// They identify one relationship resource whose parent/child read models
	// must refresh as a single client bundle.
	ParentID string
	ChildID  string
	Feature  *feature.Feature // non-nil for FeatureCreated
	Phase    feature.Phase    // set for phase-related events
	Error    error            // set for failure events
	Message  string           // human-readable detail

	RunNumber   int
	Attempt     int
	SetupLog    string
	SetupTask   string
	SetupKind   feature.SetupTaskKind
	SetupStatus feature.SetupStatus
	RepoName    string
	Path        string
	Branch      string
}
