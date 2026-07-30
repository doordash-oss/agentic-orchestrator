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
	"context"
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
	// KindChat is the interactive AMA utility session.
	KindChat
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
	case KindChat:
		return "chat"
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
// session. PendingToolIdleTimeout bounds silence while a tool is running.
// TurnCompletionIdleTimeout bounds silence after a tool reaches a terminal
// state but before the provider completes the enclosing turn.
type SessionWatchdogConfig struct {
	PendingToolIdleTimeout    time.Duration
	TurnCompletionIdleTimeout time.Duration
	PollInterval              time.Duration
	SubagentHeartbeatInterval time.Duration
}

// ToolPermissionRequest describes a pending tool-use permission check.
type ToolPermissionRequest struct {
	RequestID        string
	ToolName         string
	Input            string
	SessionID        string
	LogicalSessionID string
	FeatureID        string
	Phase            feature.Phase
	RepoName         string
	Iteration        int
	ProviderName     string // "claude", "codex", etc. — used by provider-specific guards
	// Ctx carries the session's lifecycle context so long-running permission
	// handlers (e.g. the automatic-review classifier) can be cancelled when
	// the session shuts down. Nil means no cancellation is possible; handlers
	// should fall back to context.Background().
	Ctx context.Context
	// AppendStatus synchronously appends a sanitized, provider-neutral status
	// to the owning session before a permission decision returns. It is
	// optional and best-effort; callers must not change the decision on error.
	AppendStatus func(string) error
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

// SessionBuildNotice is a local status line plus an optional operator-event
// callback published once after the subprocess starts and before handshake.
type SessionBuildNotice struct {
	Status string
	Emit   func(SessionBuildNoticeContext)
}

// SessionBuildNoticeContext identifies the owning session for an operator
// event without coupling the session package to a specific observer.
type SessionBuildNoticeContext struct {
	SessionID string
	FeatureID string
	Phase     feature.Phase
	RepoName  string
	Iteration int
}

// ProviderInitInfo carries the provider-native identity learned during the
// initialization message for a newly started session.
type ProviderInitInfo struct {
	SessionID string
	Provider  string
	Model     string
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
	ResolvedModel     string
	Protocol          llm.Protocol
	DebugSystemPrompt string
	// OnProviderInit runs synchronously after a provider initialization message
	// updates the session identity. Callers should keep it fast and best-effort.
	OnProviderInit func(ProviderInitInfo)
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
	// SupportsFinishOrViolateNudge and UsesBoundedHelperSandbox carry the
	// resolved provider's bounded-helper capabilities. The session builder sets
	// them so a bounded helper can read them off its session options without
	// re-resolving the provider (its own runner may carry no provider registry).
	SupportsFinishOrViolateNudge bool
	UsesBoundedHelperSandbox     bool
	// SupportsSessionResume reports that the resolved provider can resume a
	// prior provider-native session via BuildSessionOpts.ResumeSessionID.
	// Used by crash-resume: when a provider process dies mid-turn, the loop
	// may start a fresh process that continues the same conversation.
	SupportsSessionResume bool
	// EffectiveEffort is the resolved provider-safe effort level for this
	// session launch. Empty means no effort was resolved (utility sessions,
	// legacy callers). Set by the implementation launch path from
	// ResolveEffort before BuildSession.
	EffectiveEffort llm.EffortLevel
	// EffortSource records whether EffectiveEffort was derived from the
	// pipeline (auto) or an explicit user configuration (explicit).
	EffortSource llm.EffortSource
	// AutoReview carries the automatic-review snapshot that was captured when
	// this session was built. The implement loop copies it back into
	// BuildSessionOpts.AutoReview so crash-resume reuses the original snapshot
	// rather than the current workspace config.
	AutoReview AutoReviewSnapshot
	// SessionBuildNotices are published once after the process starts and before
	// provider handshake. They are local status records, not terminal results.
	SessionBuildNotices []SessionBuildNotice
}

// AutoReviewSnapshot bundles the automatic-review settings snapshotted when an
// original session is built. Enabled is *bool so false stays distinguishable
// from omitted (nil means read workspace defaults). Model is the reviewer
// model selector; empty means "Automatic" (resolve one deterministic eligible
// provider/model before session creation). ReviewerProvider and ReviewerModel capture the
// resolved reviewer's identity (provider name and bare model id) so crash-resume
// can reconstruct the same reviewer without re-resolving against the current
// (possibly changed) provider/catalog state. Both are empty when no reviewer
// was resolved. Defined in ports so both the agent package and session adapters
// can reference it without a circular import.
type AutoReviewSnapshot struct {
	Enabled           *bool
	Model             string
	ReviewerProvider  string
	ReviewerModel     string
	UnavailableReason string
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

	// EffectiveEffort returns the resolved provider-safe effort level for
	// this session launch. Empty for sessions that did not resolve effort.
	EffectiveEffort() llm.EffortLevel
	// EffortSource returns whether EffectiveEffort was auto-derived from the
	// pipeline or explicitly configured.
	EffortSource() llm.EffortSource

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
	RecentSessions(limit int) []SessionView
	FeatureSessions(featureID string) []SessionView
	SendInput(sessionID string, data []byte) error
	Attach(sessionID string) (SessionView, error)
	Detach()
	Shutdown()
	IsShuttingDown() bool
}
