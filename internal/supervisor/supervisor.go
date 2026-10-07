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

// Package supervisor owns the per-server supervisor conversation: the
// committed harness settings, the durable transcript that is the
// conversation's system of record, the lifecycle read model, and the
// single-flight launch of the provider process through the session manager.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// Lifecycle is the supervisor's explicit lifecycle state.
type Lifecycle string

const (
	LifecycleStopped           Lifecycle = "stopped"
	LifecycleStarting          Lifecycle = "starting"
	LifecycleIdle              Lifecycle = "idle"
	LifecycleRunning           Lifecycle = "running"
	LifecycleWaitingPermission Lifecycle = "waiting_permission"
	LifecycleWaitingQuestion   Lifecycle = "waiting_question"
	LifecycleFailed            Lifecycle = "failed"
)

// Active reports whether the lifecycle counts as active work for update
// stop, quit and tray: a process is launching or a turn is in flight.
func (l Lifecycle) Active() bool {
	switch l {
	case LifecycleStarting, LifecycleRunning, LifecycleWaitingPermission, LifecycleWaitingQuestion:
		return true
	default:
		return false
	}
}

// inTurn reports whether a delivered message is still awaiting its result.
func (l Lifecycle) inTurn() bool {
	switch l {
	case LifecycleRunning, LifecycleWaitingPermission, LifecycleWaitingQuestion:
		return true
	default:
		return false
	}
}

// StartingStep refines LifecycleStarting.
type StartingStep string

const (
	StepLaunching StartingStep = "launching"
	StepHandshake StartingStep = "handshake"
)

// TurnOutcome is how the most recent turn ended.
type TurnOutcome string

const (
	OutcomeNone        TurnOutcome = "none"
	OutcomeCompleted   TurnOutcome = "completed"
	OutcomeInterrupted TurnOutcome = "interrupted"
	OutcomeFailed      TurnOutcome = "failed"
)

// Settings is the committed harness choice. Empty Effort means the harness
// default.
type Settings struct {
	Harness string
	Model   string
	Effort  string
}

// Complete reports whether a harness and model are chosen.
func (s Settings) Complete() bool { return s.Harness != "" && s.Model != "" }

// State is a point-in-time snapshot of the read model.
type State struct {
	ConversationID  string
	Generation      int64
	SessionID       string
	Lifecycle       Lifecycle
	StartingStep    StartingStep
	LastTurnOutcome TurnOutcome
	Settings        Settings
	EffectiveModel  string
	// PendingRequests are the surfaced control requests awaiting an answer,
	// in arrival order. Session is the session they belong to.
	PendingRequests []*llm.ControlRequestMessage
	Session         ports.SessionView
	HeadSeq         int64
	StreamEpoch     string
	// WorkDir is the provider working directory, used to relativize paths
	// during projection.
	WorkDir string
}

// SessionIDPrefix marks supervisor session-manager ids:
// __supervisor__.<conversation>.<generation>.
const SessionIDPrefix = "__supervisor__."

// FeatureID is the constant feature identity supervisor sessions register
// under.
const FeatureID = "__supervisor__"

// SessionID forms the manager id for one generation.
func SessionID(conversationID string, generation int64) string {
	return fmt.Sprintf("%s%s.%d", SessionIDPrefix, conversationID, generation)
}

// Typed refusals. The REST layer maps each to its catalog code.
var (
	ErrSettingsLocked   = errors.New("supervisor settings are locked while a process exists")
	ErrSettingsRequired = errors.New("supervisor harness and model are not chosen")
	ErrTurnActive       = errors.New("supervisor turn is active")
	ErrClosed           = errors.New("supervisor is shut down")
)

// SettingsInvalidError rejects a harness, model or effort the catalog does
// not offer.
type SettingsInvalidError struct{ Reason string }

func (e *SettingsInvalidError) Error() string { return "invalid supervisor settings: " + e.Reason }

// LaunchFailedError reports a launch or handshake failure; nothing was
// committed for the send.
type LaunchFailedError struct{ Err error }

func (e *LaunchFailedError) Error() string { return "supervisor launch failed: " + e.Err.Error() }
func (e *LaunchFailedError) Unwrap() error { return e.Err }

// LaunchRequest carries one generation's launch inputs.
type LaunchRequest struct {
	SessionID      string
	ConversationID string
	Generation     int64
	Settings       Settings
	WorkDir        string
	PIDDir         string
	LogPath        string
	StderrPath     string
	Observer       ports.SessionObserver
	// OnSpawned is called once the process started, before the protocol
	// handshake runs.
	OnSpawned func()
}

// Launcher starts one supervisor provider process and registers it with the
// session manager. It returns once the protocol handshake was written.
type Launcher interface {
	Launch(ctx context.Context, req LaunchRequest) (ports.SessionView, error)
}

// Catalog validates a settings choice against the model catalog.
type Catalog interface {
	ValidateSettings(Settings) error
}

// Options configures a Coordinator.
type Options struct {
	// StateDir is the runtime feature state dir; the supervisor keeps its
	// files under <StateDir>/supervisor.
	StateDir string
	// WorkDir is the provider working directory (configured workspace root,
	// else the state dir).
	WorkDir   string
	Catalog   Catalog
	Launcher  Launcher
	Admission *workadmission.Coordinator
	// HandshakeTimeout bounds the wait for the provider's first protocol
	// output after launch. Zero uses DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
	// InterruptGrace bounds the wait for a turn result or exit after an
	// interrupt before the process group is terminated. Zero uses
	// DefaultInterruptGrace.
	InterruptGrace time.Duration
	Now            func() time.Time
	NewID          func() string
}

const (
	DefaultHandshakeTimeout = 30 * time.Second
	DefaultInterruptGrace   = 20 * time.Second
)

// SendResult is the outcome of one accepted send.
type SendResult struct {
	Record   Record
	Launched bool
}

// EventKind classifies a live event.
type EventKind string

const (
	EventRecord  EventKind = "record"
	EventDelta   EventKind = "delta"
	EventState   EventKind = "state"
	EventRequest EventKind = "request"
)

// Delta is one non-persisted streaming text chunk.
type Delta struct {
	TurnID          string
	StreamMessageID string
	ChunkIndex      int
	Text            string
}

// Event is one live supervisor event.
type Event struct {
	Kind       EventKind
	Generation int64
	Record     *Record
	Delta      *Delta
	State      *State
	Request    *llm.ControlRequestMessage
	Session    ports.SessionView
}

// ActionResult names the outcome of interrupt and end.
type ActionResult string

const (
	ActionAccepted  ActionResult = "accepted"
	ActionEnded     ActionResult = "ended"
	ActionNotActive ActionResult = "not_active"
)
