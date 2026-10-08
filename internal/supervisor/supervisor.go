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

// Waiting reports a turn waiting on the user: a permission or question
// answer. A waiting supervisor is active work, but it does not block an
// unattended install.
func (l Lifecycle) Waiting() bool {
	return l == LifecycleWaitingPermission || l == LifecycleWaitingQuestion
}

// BlocksIdleInstall reports whether the lifecycle holds up an unattended
// (install-when-idle) install: a process is launching or a turn is running.
// Explicit stop-and-install still ends a waiting supervisor through Active.
func (l Lifecycle) BlocksIdleInstall() bool {
	return l == LifecycleStarting || l == LifecycleRunning
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
	StepRebuilding StartingStep = "rebuilding"
	StepLaunching  StartingStep = "launching"
	StepHandshake  StartingStep = "handshake"
)

// TurnOutcome is how the most recent turn ended.
type TurnOutcome string

const (
	OutcomeNone        TurnOutcome = "none"
	OutcomeCompleted   TurnOutcome = "completed"
	OutcomeInterrupted TurnOutcome = "interrupted"
	OutcomeFailed      TurnOutcome = "failed"
)

// InterruptedBy says who cut the most recent turn; meaningful when the
// outcome is OutcomeInterrupted.
type InterruptedBy string

const (
	InterruptedByNone InterruptedBy = "none"
	// InterruptedByUser is Stop, the interrupt grace termination, or End.
	InterruptedByUser InterruptedBy = "user"
	// InterruptedByShutdown is a turn cut by a server shutdown or crash,
	// detected at the next boot.
	InterruptedByShutdown InterruptedBy = "shutdown"
)

// RequestedPermissionMode is the permission mode the supervisor asks every
// harness for.
const RequestedPermissionMode = "default"

// PermissionMode compares the requested permission mode with the one the
// running harness reported in its init message.
type PermissionMode struct {
	Requested          string
	Effective          string
	RestrictedByPolicy bool
}

// Settings is the committed harness choice. Empty Effort means the harness
// default.
type Settings struct {
	Harness string
	Model   string
	Effort  string
}

// SettingsChange merges optional fields over the committed settings.
type SettingsChange struct {
	Harness, Model, Effort *string
	RequestID              string
	ExpectedGeneration     int64
}

// PendingChange is the one durable settings change waiting for a turn or launch.
type PendingChange struct {
	RequestID   string    `json:"request_id"`
	Kind        string    `json:"kind"`
	Target      Settings  `json:"target"`
	RequestedAt time.Time `json:"requested_at"`
}

type StaleGenerationError struct{ Current int64 }

func (e *StaleGenerationError) Error() string {
	return fmt.Sprintf("stale supervisor generation: %d", e.Current)
}

type ChangePendingError struct{ RequestID string }

func (e *ChangePendingError) Error() string { return "supervisor change pending: " + e.RequestID }

var ErrPendingChangeNotFound = errors.New("supervisor pending change not found")

// Complete reports whether a harness and model are chosen.
func (s Settings) Complete() bool { return s.Harness != "" && s.Model != "" }

// State is a point-in-time snapshot of the read model.
type State struct {
	ConversationID string
	Generation     int64
	SessionID      string
	// NativeSessionID is the harness's own session id the transcript is
	// rebuilt under; empty until one is assigned or adopted.
	NativeSessionID string
	Lifecycle       Lifecycle
	StartingStep    StartingStep
	LastTurnOutcome TurnOutcome
	InterruptedBy   InterruptedBy
	Settings        Settings
	PendingChange   *PendingChange
	EffectiveModel  string
	PermissionMode  PermissionMode
	// Failure is the most recent launch failure; set only while the
	// lifecycle is failed.
	Failure *LaunchFailedError
	// PendingRequests are the surfaced control requests awaiting an answer,
	// in arrival order. Session is the session they belong to.
	PendingRequests []*llm.ControlRequestMessage
	Session         ports.SessionView
	HeadSeq         int64
	ContextUsage    *ContextUsage
	StreamEpoch     string
	// WorkDir is the provider working directory, used to relativize paths
	// during projection.
	WorkDir string
}

// ContextUsage is the live process's current context fill.
type ContextUsage struct {
	Percent      int `json:"percent"`
	UsedTokens   int `json:"used_tokens"`
	WindowTokens int `json:"window_tokens"`
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

// ClientMessageConflictError refuses a reused client message id whose text
// or hidden-context presence differs from the committed record.
type ClientMessageConflictError struct{ CommittedSeq int64 }

func (e *ClientMessageConflictError) Error() string {
	return fmt.Sprintf("supervisor client message id already committed at seq %d with different content", e.CommittedSeq)
}

// ErrHiddenContextUnsupported refuses a message carrying hidden context on
// a session that cannot deliver it, rather than dropping the context.
var ErrHiddenContextUnsupported = errors.New("supervisor session cannot carry hidden context")

// SettingsInvalidError rejects a harness, model or effort the catalog does
// not offer.
type SettingsInvalidError struct{ Reason string }

func (e *SettingsInvalidError) Error() string { return "invalid supervisor settings: " + e.Reason }

// LaunchFailedError reports a launch or handshake failure; nothing was
// committed for the send.
type LaunchFailedError struct {
	Err               error
	AttemptedSettings *Settings
}

func (e *LaunchFailedError) Error() string { return "supervisor launch failed: " + e.Err.Error() }
func (e *LaunchFailedError) Unwrap() error { return e.Err }

// LaunchRequest carries one generation's launch inputs.
type LaunchRequest struct {
	SessionID      string
	ConversationID string
	Generation     int64
	Settings       Settings
	// ResumeSessionID, when set, resumes the harness against the native
	// session rebuilt from the transcript.
	ResumeSessionID string
	// SeedHistoryPath, when set, names the rendered history a seeding
	// harness delivers to the new session before the first prompt.
	SeedHistoryPath string
	WorkDir         string
	PIDDir          string
	LogPath         string
	StderrPath      string
	Observer        ports.SessionObserver
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
	DefaultModel(string) (string, error)
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
	// Converters rebuild each harness's native session from the transcript
	// before launch, keyed by harness name. A harness without one launches
	// fresh every generation.
	Converters map[string]Converter
	// HandshakeTimeout bounds the wait for the provider's first protocol
	// output after launch. Zero uses DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
	// InterruptGrace bounds the wait for a turn result or exit after an
	// interrupt before the process group is terminated. Zero uses
	// DefaultInterruptGrace.
	InterruptGrace time.Duration
	// OrphanWait bounds the wait at boot for a previous server's surviving
	// provider process group to exit. Zero uses DefaultOrphanWait.
	OrphanWait time.Duration
	// SettingsUpdateTimeout bounds an in-place provider settings update.
	SettingsUpdateTimeout time.Duration
	// CompactionCaptureTimeout bounds native baseline collection after a boundary.
	CompactionCaptureTimeout time.Duration
	Now                      func() time.Time
	NewID                    func() string
}

const (
	DefaultHandshakeTimeout      = 30 * time.Second
	DefaultInterruptGrace        = 20 * time.Second
	DefaultOrphanWait            = 10 * time.Second
	DefaultSettingsUpdateTimeout = 10 * time.Second
)

// SendResult is the outcome of one accepted send.
type SendResult struct {
	Record   Record
	Launched bool
	// Deduplicated reports an identical resend of a committed client
	// message id; nothing was appended or delivered.
	Deduplicated bool
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
