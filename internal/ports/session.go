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

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// ErrSessionShuttingDown is the canonical sentinel returned by SessionManager
// implementations when StartSession is called after shutdown has begun. Defined
// in ports so agent/orchestrator code can detect shutdown without importing a
// specific session adapter package.
var ErrSessionShuttingDown = errors.New("session manager is shutting down")

// SessionStatus enumerates session lifecycle states. Defined in ports so
// domain code can reason about sessions without importing internal/session.
type SessionStatus int

const (
	SessionRunning SessionStatus = iota
	SessionWaitingPermission
	SessionWaitingHelp
	SessionDone
	SessionFailed
)

// String returns a human-readable label for the status.
func (s SessionStatus) String() string {
	switch s {
	case SessionRunning:
		return "Running"
	case SessionWaitingPermission:
		return "WaitingPermission"
	case SessionWaitingHelp:
		return "WaitingHelp"
	case SessionDone:
		return "Done"
	case SessionFailed:
		return "Failed"
	default:
		return "SessionStatus(?)"
	}
}

// SessionKind classifies a session by its role so the TUI and observer layer
// can label, group, and filter sessions uniformly. It is purely informational —
// lifecycle behavior is unchanged.
type SessionKind int

const (
	// KindPhase is the default kind: a main phase agent session (Research,
	// Plan, Implement, Review, etc.). Assumed when no kind is specified.
	KindPhase SessionKind = iota
	// KindRepoImpl is a per-repo implementation session spawned as part of a
	// multi-repo feature.
	KindRepoImpl
	// KindValidator is a read-only plan-validation critic session (one per
	// validator domain: Architecture, Security, Performance, Testing, …).
	KindValidator
	// KindReviewHelper is a read-only code-review helper session.
	KindReviewHelper
	// KindTweak is an interactive tweak/revise session spawned from the TUI.
	KindTweak
)

// SessionTurnMode controls how a session interprets provider Result messages.
type SessionTurnMode int

const (
	// TurnModeOneShot is the default autonomous mode: a Result completes the
	// session's work and provider-specific wrapper cleanup may run.
	TurnModeOneShot SessionTurnMode = iota
	// TurnModeInteractive treats a Result as the end of one assistant turn.
	// The session remains alive for follow-up user messages until explicitly
	// stopped or until the provider process exits.
	TurnModeInteractive
)

// String returns a short human-readable identifier for the kind.
func (k SessionKind) String() string {
	switch k {
	case KindPhase:
		return "phase"
	case KindRepoImpl:
		return "repo-impl"
	case KindValidator:
		return "validator"
	case KindReviewHelper:
		return "review-helper"
	case KindTweak:
		return "tweak"
	default:
		return "unknown"
	}
}

// QAPair captures a single question-answer exchange from AskUserQuestion.
type QAPair struct {
	Question string
	Answer   string
	// Notes captures freeform text the user entered alongside the answer
	// (Claude Agent SDK annotations.notes). Empty when the user skipped notes.
	Notes string
	// AutoPicked marks answers synthesized by the session-layer grill-me
	// auto-pick policy rather than typed or selected by a human.
	AutoPicked bool
	// Confidence is the selected option's self-rated confidence when
	// AutoPicked is true.
	Confidence float64
}

// AskUserAutoPickPurpose identifies the role that emitted an AskUserQuestion
// bundle. The session policy uses this explicit purpose instead of deriving
// behavior from session IDs.
type AskUserAutoPickPurpose string

const (
	AskUserAutoPickPurposeNone             AskUserAutoPickPurpose = ""
	AskUserAutoPickPurposeInquire          AskUserAutoPickPurpose = "inquire"
	AskUserAutoPickPurposeDesign           AskUserAutoPickPurpose = "design"
	AskUserAutoPickPurposeRoadmapCreator   AskUserAutoPickPurpose = "roadmap_creator"
	AskUserAutoPickPurposePhasePlanCreator AskUserAutoPickPurpose = "phase_plan_creator"
	AskUserAutoPickPurposeResearch         AskUserAutoPickPurpose = "research"
	AskUserAutoPickPurposeImplement        AskUserAutoPickPurpose = "implement"
	AskUserAutoPickPurposeReview           AskUserAutoPickPurpose = "review"
	AskUserAutoPickPurposeKBBuild          AskUserAutoPickPurpose = "kb_build"
	AskUserAutoPickPurposeChat             AskUserAutoPickPurpose = "chat"
	AskUserAutoPickPurposeTweak            AskUserAutoPickPurpose = "tweak"
	AskUserAutoPickPurposeFinalReview      AskUserAutoPickPurpose = "final_review"
	AskUserAutoPickPurposeValidator        AskUserAutoPickPurpose = "validator"
	AskUserAutoPickPurposeRoadmapReviser   AskUserAutoPickPurpose = "roadmap_reviser"
	AskUserAutoPickPurposePhasePlanReviser AskUserAutoPickPurpose = "phase_plan_reviser"
)

// AskUserAutoPickConfig carries the narrow session-layer policy context for
// deciding whether an AskUserQuestion bundle can be answered before TUI
// routing. LoadInquireness is called for each incoming bundle so live config
// edits affect the next decision.
type AskUserAutoPickConfig struct {
	Purpose              AskUserAutoPickPurpose
	LoadInquireness      func() (feature.Inquireness, error)
	OnQuestionAutoPicked func(question, answer string, confidence float64)
}

// SessionWatchdogConfig enables provider-specific lifecycle safety rails for a
// session. The first watchdog watches for a provider that reports a pending
// tool call and then goes silent without emitting a permission request, result,
// or process exit.
type SessionWatchdogConfig struct {
	PendingToolIdleTimeout time.Duration
	PollInterval           time.Duration
}

// ToolPermissionRequest describes a pending tool-use permission check.
type ToolPermissionRequest struct {
	RequestID    string
	ToolName     string
	Input        string
	SessionID    string
	FeatureID    string
	ProviderName string // "claude", "codex", etc. — used by provider-specific guards
}

// PermissionDecision is the outcome of a permission check.
type PermissionDecision struct {
	Behavior string // "allow" | "deny" | "" (defer)
	Reason   string
}

// PermissionHandler decides whether a session may invoke a tool. Defined in
// ports so SessionOpts (also in ports) can reference it without pulling the
// session package into every consumer.
type PermissionHandler interface {
	CanUseTool(req ToolPermissionRequest) (PermissionDecision, error)
}

// SessionOpts holds optional configuration for a session start. Owned by the
// ports package so orchestrator / agent code can construct session options
// without importing internal/session.
type SessionOpts struct {
	PIDDir        string
	Iteration     int
	PermHandler   PermissionHandler
	InitialPrompt string
	// ContextWindow is the resolved model window for the session. It lets the
	// session expose an initial prompt context estimate before provider
	// telemetry arrives.
	ContextWindow     int
	LogPath           string
	StderrPath        string
	RepoName          string
	PermCacheScope    string
	ProviderName      string
	Protocol          llm.Protocol
	DebugSystemPrompt string
	// CriticalAttachSendTimeout overrides the bounded send timeout for
	// result messages forwarded to the attach channel. Zero uses the
	// production default.
	CriticalAttachSendTimeout time.Duration
	// CodexHandshakeTimeout overrides the protocol handshake timeout. Zero
	// uses the production default.
	CodexHandshakeTimeout time.Duration
	// ResultShutdownGrace overrides the post-result subprocess shutdown grace.
	// Zero uses the production default.
	ResultShutdownGrace time.Duration
	// KeepAliveOnTruncatedResult leaves stdin open when a Result indicates a
	// resumable truncated turn. Loop waiters that send an automatic
	// continuation set this so the continuation is not racing a post-Result
	// stdin shutdown.
	KeepAliveOnTruncatedResult bool
	// Kind classifies the session for TUI/observer purposes. Defaults to
	// KindPhase when the zero value is used.
	Kind SessionKind
	// TurnMode controls whether Result ends the whole session or just the
	// current assistant turn. Defaults to TurnModeOneShot.
	TurnMode SessionTurnMode
	// Label is a short context-specific sub-label (validator domain, helper
	// target, …). Empty for plain phase sessions.
	Label string
	// AskUserAutoPick enables the session-layer AskUserQuestion auto-pick
	// policy for allowlisted creator sessions. Nil preserves pass-through
	// routing for every AskUserQuestion bundle.
	AskUserAutoPick *AskUserAutoPickConfig
	// Watchdog enables generic session lifecycle watchdogs. Nil disables them.
	Watchdog *SessionWatchdogConfig
}

// MessageLog is the interface consumers use to observe a session's SDK
// message stream. It mirrors the methods on the concrete session MessageLog
// so *session.MessageLog satisfies it structurally.
type MessageLog interface {
	Append(msg llm.SDKMessage)
	UpdateLast(msg llm.SDKMessage)
	UpdateLastAssistantPartial(msg llm.SDKMessage)
	Messages() []llm.SDKMessage
	Len() int
	Text() string
	LastN(n int) []llm.SDKMessage
	LastResultMessage() *llm.ResultMessage
	LastErrorDetail() string
	AssistantText() string
	ToolUseBlocks() []llm.ContentBlock
}

// AttachConsumerRegistrar is an optional interface implemented by session
// handles that can track whether anything is actively draining AttachCh().
// Callers that block on AttachCh should register for the lifetime of that read
// loop and invoke the returned cleanup when the loop exits.
type AttachConsumerRegistrar interface {
	RegisterAttachConsumer() func()
}

// SessionView is the read-oriented interface external packages (TUI, agent)
// use to observe a session.
type SessionView interface {
	ID() string
	FeatureID() string
	Phase() feature.Phase
	RepoName() string
	PermCacheScope() string
	Kind() SessionKind
	Label() string

	Status() SessionStatus
	IsActive() bool
	Iteration() int
	StartedAt() time.Time
	InitialPrompt() string
	ProviderName() string
	Model() string
	WorkDir() string

	MessageLog() MessageLog

	Cost() *llm.ResultMessage
	LatestUsage() *llm.Usage
	AccumulatedUsage() llm.Usage
	LastControlRequest() *llm.ControlRequestMessage
	// PendingControlRequests returns every outstanding control_request in
	// arrival order. Used by callers that need to handle parallel
	// AskUserQuestion calls correctly — LastControlRequest only exposes
	// the most recently arrived one and would lose the others.
	PendingControlRequests() []*llm.ControlRequestMessage
	QALog() []QAPair
	LogFilePath() string
	ContextPercentage() int
	ErrorDetail() string
	ExitCodeDetail() string
	LastStdoutAt() time.Time

	StatusCh() <-chan string
	AttachCh() <-chan llm.SDKMessage
	Done() <-chan struct{}

	HasPendingAskUserQuestion() bool

	SendUserMessage(text string) error
	RespondToControl(requestID string, allow bool, reason string) error
	RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]llm.AskUserAnnotation) error
	ClearPendingQuestion(requestID string)
	ResetWaitingStatus()
	Stop() error
	Interrupt() error
	Wait()
}

// SessionHandle extends SessionView with the mutable lifecycle methods that
// the orchestrator and agent packages need to install logs, cleanup funcs,
// and tool-allowed callbacks.
type SessionHandle interface {
	SessionView

	SetStatus(status SessionStatus)
	SetLogFile(f *os.File)
	AddCleanupFunc(fn func())
	SetHasUnansweredQuestion(v bool)
	CloseStdin()
	SetOnToolAllowed(fn func(toolName string, input json.RawMessage))
	SetOnFileRead(fn func(read llm.FileReadEvent))
	SetOnSubagentEvent(fn func(msg llm.SDKMessage))
}

// SessionManager abstracts PTY session lifecycle.
type SessionManager interface {
	StartSession(id, featureID string, phase feature.Phase,
		command []string, workdir string, env []string,
		opts ...*SessionOpts) (SessionHandle, error)
	StopSession(id string) error
	GetSession(id string) SessionView
	ActiveSessions() []SessionView
	FeatureSessions(featureID string) []SessionView
	SendInput(sessionID string, data []byte) error
	Attach(sessionID string) (SessionView, error)
	Detach()
	Shutdown()
	IsShuttingDown() bool
}
