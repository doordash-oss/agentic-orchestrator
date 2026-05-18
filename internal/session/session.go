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

package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// SessionStatus and QAPair alias the canonical port types.
type (
	SessionStatus = ports.SessionStatus
	QAPair        = ports.QAPair
)

// Re-exported session status constants.
const (
	SessionRunning           = ports.SessionRunning
	SessionWaitingPermission = ports.SessionWaitingPermission
	SessionWaitingHelp       = ports.SessionWaitingHelp
	SessionDone              = ports.SessionDone
	SessionFailed            = ports.SessionFailed
)

// criticalAttachSendTimeout is the default bound for blocking forward of Result
// messages onto attachCh. Without it, a stuck consumer would deadlock
// readMessages indefinitely; with it, the CLI continues reading after
// the bound. 5s is long enough for any normal TUI event-loop hitch and
// short enough that a genuine deadlock surfaces quickly. Result drops
// are recoverable — the next assistant message clears the spinner.
//
// ControlRequest messages no longer share this path — they take a
// dedicated controlCh routed through forwardControlRequests, which
// blocks indefinitely until an attached consumer can receive. Dropping
// a control_request would strand the SDK subprocess, so the forwarder
// keeps it parked until either the user attaches or the session ends.
var criticalAttachSendTimeout = 5 * time.Second

// resultShutdownGrace is how long we give the subprocess to exit cleanly
// after stdin is closed in response to a result message, before sending
// SIGTERM. The same delay is then waited again before escalating to
// SIGKILL. Mirrors Stop()'s escalation; package-level var so tests can
// shrink it.
var resultShutdownGrace = 5 * time.Second

// Session manages a CLI subprocess using the JSON stdin/stdout protocol.
// Provider-specific wire protocol logic is delegated to the Protocol interface.
type Session struct {
	id             string
	featureID      string
	phase          feature.Phase
	process        *exec.Cmd
	logFile        *os.File
	status         SessionStatus
	startedAt      time.Time
	workDir        string
	repoName       string            // repo name for multi-repo features (set via SessionOpts)
	permCacheScope string            // repo scope used by CachingHandler ("" = global)
	pidDir         string            // directory for PID file management
	iteration      int               // current iteration (for implementation phase)
	initialPrompt  string            // the user prompt that started this session (for display in attach view)
	providerName   string            // "claude" or "codex" (for display/logging)
	stderrPath     string            // optional: capture stderr to this file
	kind           ports.SessionKind // informational classification; zero value = KindPhase
	turnMode       ports.SessionTurnMode
	label          string // context-specific sub-label (validator domain, helper target, …)

	// Provider protocol — handles all wire-level communication.
	// Set before Start() via SessionOpts.Protocol.
	protocol llm.Protocol

	// SDK protocol fields
	model            string      // from SystemInitMessage — e.g. "opus[1m]", "sonnet"
	latestUsage      *llm.Usage  // most recent usage from a non-partial assistant message
	accumulatedUsage llm.Usage   // cumulative usage across all messages
	messageLog       *MessageLog // structured message log
	cost             *llm.ResultMessage

	// statusCh carries the SDK-derived session lifecycle status:
	// "SUCCESS" / "API_ERROR" / "FAILED" (see resultSubtypeToStatus).
	// It does NOT carry "RETRY" — the implement loop's RETRY routing
	// is driven by parsing progress.md after the session ends, not by
	// the session subtype itself.
	statusCh     chan string
	cleanupFuncs []func() // functions to call on session exit

	// Permission handling.
	permHandler PermissionHandler
	// pendingControlRequests holds every control_request that has been
	// surfaced to the TUI but not yet responded to, in arrival order.
	// Multiple AskUserQuestion calls can be in flight concurrently when
	// the LLM issues them as parallel tool uses in a single turn; this
	// slice keeps each requestID first-class so neither the session nor
	// the TUI silently drops one. LastControlRequest() returns the most
	// recently arrived entry to preserve the historical "single-slot"
	// semantics callers depend on. Access guarded by mu.
	pendingControlRequests []*llm.ControlRequestMessage

	// AskUserQuestion tracking — set when an AskUserQuestion tool_use is
	// detected in an assistant message, cleared when the user sends a message.
	// Used by phase runners to keep sessions alive until the user answers.
	// Access via SetHasUnansweredQuestion / HasUnansweredQuestion; the
	// field is protected by mu.
	hasUnansweredQuestion bool

	// qaLog accumulates question-answer pairs from AskUserQuestion interactions.
	// Appended by RespondToAskUser, read after phase completion to persist to disk.
	qaLog []QAPair

	// onToolAllowed is called when a tool use is auto-approved by the permission
	// handler. It receives the tool name and raw JSON input. Used by the agent
	// layer to track reads of KB/skill/guideline files.
	onToolAllowed func(toolName string, input json.RawMessage)

	// onFileRead is called when a provider reports a concrete file read through
	// a provider-neutral signal. Unlike onToolAllowed, this is not tied to a
	// permission decision.
	onFileRead func(read llm.FileReadEvent)

	// onSubagentEvent is called when the CLI emits a subagent (Task tool)
	// progress or notification message — i.e., msg.TaskProgress or
	// msg.TaskNotification is non-nil. Used by the agent layer to surface
	// subagent heartbeats to events.jsonl while the main agent is blocked
	// in a Task() call. Codex does not emit these messages.
	onSubagentEvent func(msg llm.SDKMessage)

	// For attach mode: subscribers receive copies of messages
	attachCh                  chan llm.SDKMessage
	criticalAttachSendTimeout time.Duration

	// controlCh buffers ControlRequest messages on a dedicated path so a
	// burst of stream/result traffic on attachCh cannot starve them. The
	// forwarder goroutine moves entries from controlCh into attachCh so
	// downstream consumers see a single merged stream and the public
	// AttachCh() contract is preserved. Capacity is generous (1024)
	// because control requests are rare — one per AskUserQuestion or
	// permission decision — and dropping one is unrecoverable from the
	// LLM's perspective.
	controlCh chan llm.SDKMessage

	// controlForwarderDone is closed when the forwarder goroutine exits.
	// readMessages' cleanup waits on it before closing attachCh so the
	// forwarder cannot panic with a send-on-closed-channel.
	controlForwarderDone chan struct{}

	// closing is closed at the very start of readMessages cleanup so the
	// forwarder can break out of its blocking attachCh send immediately
	// when the session is shutting down — without it, a parked
	// control_request that no consumer ever picked up would hold the
	// forwarder forever. Distinct from done (which is closed at the end
	// of cleanup, after attachCh is closed) so callers waiting on Done()
	// still see a fully-shutdown session, not a partially torn-down one.
	closing chan struct{}

	// streamRing buffers high-rate stream_event (and codex stderr
	// synthetic) messages with drop-oldest semantics, so that a burst
	// of partial deltas cannot starve critical Result / ControlRequest
	// messages that share attachCh. A dedicated drainer goroutine moves
	// entries from the ring into attachCh while honoring
	// attachStreamReserve slots of headroom for critical traffic.
	streamRing *streamRing

	// drainerDone is closed when the streamRing drainer goroutine
	// exits. readMessages' cleanup waits on this before closing
	// attachCh to avoid a send-on-closed-channel panic.
	drainerDone chan struct{}

	// drainerOnce guards lazy startup of the ring drainer so that
	// sessions which never call Start (test-only constructions) do
	// not leave a dangling goroutine, while production Start paths
	// idempotently ensure the drainer is running.
	drainerOnce sync.Once

	// attachDropReporter, if non-nil, receives a structured event every
	// time a critical SDK message is dropped after the bounded blocking
	// send on attachCh times out. The interface is minimal (one method)
	// so the session package does not need to import internal/observe
	// directly; production code passes in a small adapter over
	// *observe.Observer.
	attachDropReporter AttachDropReporter

	// lastStdoutAt is the UnixNano of the most recent line read from the
	// CLI subprocess stdout. It is updated in readMessages for every
	// non-empty line — before routing, parsing, or filtering — so that the
	// TUI can treat any raw stdout activity as evidence that the agent is
	// making progress, even when the event would otherwise be dropped by
	// the protocol (unknown message types, oversized buffers, filtered
	// stream events). Read via LastStdoutAt().
	lastStdoutAt atomic.Int64

	// Internal
	stdin  io.WriteCloser
	stdout io.ReadCloser
	done   chan struct{}
	mu     sync.Mutex

	// resultShutdownStarted ensures the post-result stdin-close + signal
	// escalation runs at most once per session, even if the SDK emits
	// multiple result messages on the same turn. Set via CompareAndSwap
	// in readMessages; never reset.
	resultShutdownStarted atomic.Bool
	resultShutdownGrace   time.Duration

	// keepAliveOnTruncatedResult lets loop-managed sessions continue the same
	// provider process after a CLI-truncated turn. Normal Result cleanup still
	// runs for deliberate end_turn / error results so wrapper processes do not
	// hang.
	keepAliveOnTruncatedResult bool

	// askUserAutoPick optionally answers confidence-qualified AskUserQuestion
	// bundles before they enter pending TUI routing.
	askUserAutoPick *ports.AskUserAutoPickConfig
}

// AttachDropReporter receives a notification every time a critical SDK
// Result message is dropped because the attachCh consumer did not
// accept it within criticalAttachSendTimeout. ControlRequest messages
// take the dedicated controlCh path and are never dropped — the
// forwarder waits indefinitely for an attached consumer. The
// implementation is expected to record a metric / emit an observability
// event — agentic wires *observe.Observer through an adapter.
//
// The interface is intentionally narrow so internal/session stays free
// of a hard dependency on internal/observe. The field is optional — a
// nil reporter is the default (no metric emitted).
type AttachDropReporter interface {
	ReportAttachDrop(sessionID, featureID, phase, msgType string, timeout time.Duration)
}

// --- Accessor methods ---

func (s *Session) ID() string           { return s.id }
func (s *Session) FeatureID() string    { return s.featureID }
func (s *Session) Phase() feature.Phase { return s.phase }
func (s *Session) Process() *exec.Cmd   { return s.process }
func (s *Session) StartedAt() time.Time { return s.startedAt }
func (s *Session) WorkDir() string      { return s.workDir }
func (s *Session) RepoName() string     { return s.repoName }
func (s *Session) Kind() ports.SessionKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kind
}
func (s *Session) Label() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.label
}
func (s *Session) SetKind(k ports.SessionKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kind = k
}
func (s *Session) SetLabel(l string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.label = l
}
func (s *Session) Iteration() int        { return s.iteration }
func (s *Session) InitialPrompt() string { return s.initialPrompt }
func (s *Session) ProviderName() string  { return s.providerName }
func (s *Session) SessionID() string {
	if s.protocol == nil {
		return ""
	}
	return s.protocol.SessionID()
}
func (s *Session) PermCacheScope() string { return s.permCacheScope }
func (s *Session) Model() string          { return s.model }
func (s *Session) LatestUsage() *llm.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latestUsage == nil {
		return nil
	}
	usage := *s.latestUsage
	return &usage
}
func (s *Session) AccumulatedUsage() llm.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accumulatedUsage
}
func (s *Session) MessageLog() ports.MessageLog { return s.messageLog }
func (s *Session) Cost() *llm.ResultMessage     { return s.cost }
func (s *Session) StatusCh() <-chan string      { return s.statusCh }

// LastControlRequest returns the most recently arrived pending
// control_request, or nil if none are outstanding. Preserves the historical
// "single-slot" semantics: callers that just want "is there one to deal
// with?" still get a definitive nil/non-nil answer. Use
// PendingControlRequests for the full set when handling parallel AUQ
// calls.
func (s *Session) LastControlRequest() *llm.ControlRequestMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastControlRequestLocked()
}

// lastControlRequestLocked is the lock-held form used by methods that
// already hold s.mu. Returns the newest pending request, matching the
// historical semantics where each new control_request overwrote the
// single slot.
func (s *Session) lastControlRequestLocked() *llm.ControlRequestMessage {
	if len(s.pendingControlRequests) == 0 {
		return nil
	}
	return s.pendingControlRequests[len(s.pendingControlRequests)-1]
}

// PendingControlRequests returns a snapshot of every pending
// control_request in arrival order. Used by the TUI to surface multiple
// concurrent AskUserQuestion calls without losing any of them.
func (s *Session) PendingControlRequests() []*llm.ControlRequestMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pendingControlRequests) == 0 {
		return nil
	}
	out := make([]*llm.ControlRequestMessage, len(s.pendingControlRequests))
	copy(out, s.pendingControlRequests)
	return out
}

// recordPendingControlRequestLocked appends cr to the pending list. Caller
// must hold s.mu. If a duplicate requestID is already present the new
// entry replaces the existing one in place to keep the list dedup'd.
func (s *Session) recordPendingControlRequestLocked(cr *llm.ControlRequestMessage) {
	if cr == nil {
		return
	}
	for i, existing := range s.pendingControlRequests {
		if existing != nil && existing.RequestID == cr.RequestID {
			s.pendingControlRequests[i] = cr
			return
		}
	}
	s.pendingControlRequests = append(s.pendingControlRequests, cr)
}

// removePendingControlRequestLocked removes the entry matching requestID.
// Returns true if an entry was removed. Caller must hold s.mu.
func (s *Session) removePendingControlRequestLocked(requestID string) bool {
	for i, cr := range s.pendingControlRequests {
		if cr != nil && cr.RequestID == requestID {
			s.pendingControlRequests = append(s.pendingControlRequests[:i], s.pendingControlRequests[i+1:]...)
			return true
		}
	}
	return false
}

// findPendingControlRequestLocked returns the entry matching requestID, or
// nil. Caller must hold s.mu.
func (s *Session) findPendingControlRequestLocked(requestID string) *llm.ControlRequestMessage {
	for _, cr := range s.pendingControlRequests {
		if cr != nil && cr.RequestID == requestID {
			return cr
		}
	}
	return nil
}

// clearPendingControlRequestsLocked resets the pending list. Caller must
// hold s.mu. Used when the session resets (e.g. SendUserMessage).
func (s *Session) clearPendingControlRequestsLocked() {
	s.pendingControlRequests = nil
}

func (s *Session) QALog() []QAPair {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.qaLog) == 0 {
		return nil
	}
	out := make([]QAPair, len(s.qaLog))
	copy(out, s.qaLog)
	return out
}
func (s *Session) LogFilePath() string {
	if s.logFile == nil {
		return ""
	}
	return s.logFile.Name()
}

// LastStdoutAt returns the timestamp of the most recent non-empty line read
// from the CLI subprocess stdout. Returns the zero time until the first line
// arrives. Used by the TUI to detect ongoing agent activity independent of
// which parsed messages make it through the attachCh pipeline.
func (s *Session) LastStdoutAt() time.Time {
	ns := s.lastStdoutAt.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// --- Setter methods (for test code) ---

func (s *Session) SetModel(m string)           { s.model = m }
func (s *Session) SetProviderName(name string) { s.providerName = name }
func (s *Session) SetLatestUsage(u *llm.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u == nil {
		s.latestUsage = nil
		return
	}
	usage := *u
	s.latestUsage = &usage
}
func (s *Session) SetAccumulatedUsage(u llm.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accumulatedUsage = u
}
func (s *Session) SetCost(c *llm.ResultMessage) { s.cost = c }
func (s *Session) SetIteration(i int)           { s.iteration = i }
func (s *Session) SendStatus(status string)     { s.statusCh <- status }

// SetLastControlRequest stages a pending control_request without going
// through the full readMessages pipeline. Used by tests. Passing nil
// clears the entire pending list (preserving the historical "single
// slot" semantics). Passing a non-nil request appends it (or replaces
// the existing entry for the same requestID), so multiple successive
// calls accumulate — letting parallel-AUQ test scenarios stage two
// concurrent requests the same way production does. Production code
// should use recordPendingControlRequestLocked from inside
// readMessages.
func (s *Session) SetLastControlRequest(cr *llm.ControlRequestMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cr == nil {
		s.pendingControlRequests = nil
		return
	}
	s.recordPendingControlRequestLocked(cr)
}

func (s *Session) SetRepoName(name string)        { s.repoName = name }
func (s *Session) SetPermCacheScope(scope string) { s.permCacheScope = scope }
func (s *Session) SetProcess(cmd *exec.Cmd)       { s.process = cmd }
func (s *Session) SetAskUserAutoPickConfig(cfg *ports.AskUserAutoPickConfig) {
	s.askUserAutoPick = cfg
}

// SetStdinForTest installs a writer as the session's stdin so test code can
// exercise SendUserMessage and other outgoing paths without launching a
// subprocess. Production code should not call this — stdin is normally
// wired during Start from the process's stdin pipe.
func (s *Session) SetStdinForTest(w io.WriteCloser) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stdin = w
}

// SetAttachDropReporter installs an AttachDropReporter that receives a
// notification when a critical SDK message is dropped on attachCh
// timeout. Optional — a nil reporter means "no metric emitted". The
// log.Printf line at the drop site is unchanged; the reporter is
// additive.
func (s *Session) SetAttachDropReporter(r AttachDropReporter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attachDropReporter = r
}

func NewSession(id, featureID string, phase feature.Phase) *Session {
	return &Session{
		id:                        id,
		featureID:                 featureID,
		phase:                     phase,
		messageLog:                NewMessageLog(),
		status:                    SessionRunning,
		statusCh:                  make(chan string, 1),
		attachCh:                  make(chan llm.SDKMessage, 100),
		criticalAttachSendTimeout: criticalAttachSendTimeout,
		controlCh:                 make(chan llm.SDKMessage, 1024),
		streamRing:                newStreamRing(streamRingCap),
		drainerDone:               make(chan struct{}),
		controlForwarderDone:      make(chan struct{}),
		closing:                   make(chan struct{}),
		done:                      make(chan struct{}),
		resultShutdownGrace:       resultShutdownGrace,
	}
}

func (s *Session) setCriticalAttachSendTimeoutForTest(timeout time.Duration) {
	s.criticalAttachSendTimeout = timeout
}

func (s *Session) setResultShutdownGraceForTest(grace time.Duration) {
	s.resultShutdownGrace = grace
}

func (s *Session) criticalAttachTimeout() time.Duration {
	if s.criticalAttachSendTimeout > 0 {
		return s.criticalAttachSendTimeout
	}
	return criticalAttachSendTimeout
}

func (s *Session) resultShutdownGraceDuration() time.Duration {
	if s.resultShutdownGrace > 0 {
		return s.resultShutdownGrace
	}
	return resultShutdownGrace
}

// ensureStreamDrainer starts the ring drainer goroutine exactly once.
// Called from Start (production) and from tests that push directly to
// streamRing without launching a subprocess.
func (s *Session) ensureStreamDrainer() {
	s.drainerOnce.Do(func() {
		go s.drainAttachStream()
		go s.forwardControlRequests()
	})
}

// forwardControlRequests bridges controlCh into attachCh. It runs in
// its own goroutine so a slow attachCh consumer cannot stall the
// readMessages producer; control_requests buffer in controlCh while
// this goroutine waits for an attachCh slot.
//
// The send to attachCh is unbounded by design. A control_request is
// the SDK subprocess asking for a decision (permission or
// AskUserQuestion); dropping it would strand the subprocess waiting
// for a response that never comes. When no consumer is attached
// (headless run, or user has detached), the request stays parked in
// controlCh until either a consumer attaches or the session is
// shutting down. The TUI's attach path replays pending requests via
// PendingControlRequests(), so a request emitted while detached is
// surfaced the next time the user attaches — even if hours later.
// The session's status reflects the wait (SessionWaitingPermission
// for permission requests; SessionWaitingHelp for AskUserQuestion).
//
// Exits when controlCh is closed (signalled by readMessages cleanup)
// or s.closing is closed (shutdown). Closes controlForwarderDone on
// exit so the cleanup defer can wait before closing attachCh.
func (s *Session) forwardControlRequests() {
	defer close(s.controlForwarderDone)
	for {
		var (
			msg llm.SDKMessage
			ok  bool
		)
		select {
		case msg, ok = <-s.controlCh:
			if !ok {
				return
			}
		case <-s.closing:
			return
		}

		select {
		case s.attachCh <- msg:
		case <-s.closing:
			return
		}
	}
}

// drainAttachStream moves entries from the stream ring into attachCh
// while keeping attachStreamReserve slots free for critical messages.
// It exits when the ring is closed and empty, or when s.done closes.
// Sends use a select with a default so a filled attachCh never blocks
// the drainer: we drop stream events rather than starve critical ones.
func (s *Session) drainAttachStream() {
	defer close(s.drainerDone)

	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()

	for {
		// Once the ring is closed, the producer has stopped (readMessages
		// has exited) so no critical traffic will arrive — the reserve
		// no longer needs protecting, and we must drain unconditionally
		// or the cleanup path that waits on drainerDone will hang.
		closed := s.streamRing.isClosed()

		for {
			if !closed && cap(s.attachCh)-len(s.attachCh) <= attachStreamReserve {
				break
			}
			msg, ok := s.streamRing.Pop()
			if !ok {
				break
			}
			select {
			case s.attachCh <- msg:
			case <-s.done:
				return
			default:
				// attachCh full — drop this stream event. During
				// normal operation the reserve prevents this; during
				// shutdown (closed) the consumer may be gone entirely
				// and dropping the remainder is the correct exit.
			}
		}

		if s.streamRing.isClosedAndEmpty() {
			return
		}
		select {
		case <-s.streamRing.Notify():
		case <-poll.C:
		case <-s.done:
			return
		}
	}
}

// Start launches the subprocess and begins reading JSONL from stdout.
func (s *Session) Start(command []string, workdir string, env []string, onMessage func(llm.SDKMessage)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.workDir = workdir
	s.startedAt = time.Now()

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	// Interactive sessions discard stderr by default to prevent provider TUI
	// escape sequences from corrupting agentic's BubbleTea UI. When StderrPath
	// is set, capture stderr to a file for debugging.
	if s.stderrPath != "" {
		stderrFile, fileErr := os.Create(s.stderrPath)
		if fileErr == nil {
			cmd.Stderr = stderrFile
			defer func() { _ = stderrFile.Close() }()
		}
	} else {
		devNull, devErr := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if devErr == nil {
			cmd.Stderr = devNull
			defer func() { _ = devNull.Close() }()
		}
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	// Write PID file if directory is configured
	if s.pidDir != "" && cmd.Process != nil {
		pf := PIDFile{
			PID:       cmd.Process.Pid,
			StartedAt: s.startedAt,
			FeatureID: s.featureID,
			Phase:     s.phase.String(),
			Iteration: s.iteration,
			RepoName:  s.repoName,
		}
		if err := WritePIDFile(s.pidDir, pf); err != nil {
			_ = stdinPipe.Close()
			terminateStartedCommand(cmd)
			return fmt.Errorf("writing PID file: %w", err)
		}
	}

	s.process = cmd
	s.stdin = stdinPipe
	s.stdout = stdoutPipe

	// Start the stream-ring drainer before any producer goroutines
	// begin pushing events.
	s.ensureStreamDrainer()

	go s.readMessages(onMessage)

	return nil
}

func terminateStartedCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}

	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	}

	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	<-done
}

// readMessages is the main goroutine that reads JSONL from stdout and dispatches messages.
func (s *Session) readMessages(onMessage func(llm.SDKMessage)) {
	defer func() {
		s.mu.Lock()
		if s.logFile != nil {
			_ = s.logFile.Close()
		}
		if s.process != nil {
			_ = s.process.Wait()
			if s.process.ProcessState != nil && s.process.ProcessState.Success() {
				s.status = SessionDone
			} else if s.status != SessionDone {
				s.status = SessionFailed
			}
		}
		pidDir := s.pidDir
		repoName := s.repoName
		cleanups := make([]func(), len(s.cleanupFuncs))
		copy(cleanups, s.cleanupFuncs)
		s.mu.Unlock()
		if pidDir != "" {
			_ = RemovePIDFile(pidDir, repoName)
		}
		for _, fn := range cleanups {
			fn()
		}
		// Both the stream drainer and the control-request forwarder
		// publish into attachCh, and sending on a closed chan panics.
		// Close their upstream sources first, wait for both to exit,
		// then close attachCh. Order:
		//   1. closing — wakes the forwarder out of any in-flight
		//      bounded-blocking attachCh send so it can exit promptly.
		//   2. streamRing.Close() / close(controlCh) — signal "no more
		//      inputs" to the drainer and forwarder.
		//   3. wait for both goroutines to finish.
		//   4. close attachCh; close done.
		close(s.closing)
		s.streamRing.Close()
		close(s.controlCh)
		<-s.drainerDone
		<-s.controlForwarderDone
		close(s.attachCh)
		close(s.done)
	}()

	// Use a line-based reader instead of json.NewDecoder so that non-JSON
	// output (e.g., from codex CLI) is gracefully skipped rather than causing
	// a fatal decode error that would set SessionFailed and stop all reading.
	scanner := bufio.NewScanner(s.stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // up to 10MB per line

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Record raw stdout activity before any parsing or routing so that
		// the TUI can detect ongoing work even for lines the protocol
		// filters out (unknown types, stream_event filtering, etc.).
		s.lastStdoutAt.Store(time.Now().UnixNano())

		// Log raw line
		s.mu.Lock()
		if s.logFile != nil {
			_, _ = s.logFile.Write(line)
			_, _ = s.logFile.Write([]byte("\n"))
		}
		s.mu.Unlock()

		// Parse the line via the provider protocol.
		var msg llm.SDKMessage
		if s.protocol != nil {
			msgs, _ := s.protocol.ParseLine(line)
			if len(msgs) == 0 {
				continue // Unrecognized/internal message — skip
			}
			msg = llm.SDKMessage(msgs[0])
		} else {
			// Fallback: direct JSON unmarshal (no protocol set)
			if err := json.Unmarshal(line, &msg); err != nil {
				text := string(line)
				msg = llm.SDKMessage{
					Type: "assistant",
					Assistant: &llm.AssistantMessage{
						Message: llm.ConversationMsg{
							Role:    "assistant",
							Content: []llm.ContentBlock{{Type: "text", Text: text}},
						},
					},
				}
			}
		}

		// Handle init message to capture model
		if msg.Init != nil {
			s.mu.Lock()
			s.model = msg.Init.Model
			pidDir := s.pidDir
			s.mu.Unlock()

			// Update PID file with session ID for --resume support
			if pidDir != "" && s.protocol != nil {
				if err := s.updatePIDFileSessionID(pidDir, s.protocol.SessionID()); err != nil {
					log.Printf("session %s: update PID file with session ID: %v", s.id, err)
				}
			}
		}

		// Handle control requests: try to auto-handle synchronously.
		// If handled (hook_callback, auto-approved tool), suppress the message
		// so the TUI doesn't show a spurious permission prompt.
		if msg.ControlRequest != nil {
			if s.tryHandleControlRequest(msg) {
				// Auto-handled — don't forward to onMessage/attach/log.
				// Still log the raw JSONL (already done above).
				continue
			}
			s.mu.Lock()
			s.recordPendingControlRequestLocked(msg.ControlRequest)
			s.mu.Unlock()
		}

		// Handle result message. The statusCh signal is deferred until after
		// the message is appended to the log below, so receivers woken by
		// statusCh observe a MessageLog that already contains this result.
		if msg.Result != nil {
			s.mu.Lock()
			s.cost = msg.Result
			// Extract context window from modelUsage if available (Claude CLI).
			for _, mu := range msg.Result.ModelUsage {
				if mu.ContextWindow > 0 && s.latestUsage != nil {
					s.latestUsage.ContextWindow = mu.ContextWindow
					break
				}
			}
			// Fallback: if no usage was accumulated from streaming messages,
			// use the result's usage (covers sessions that only get a final result).
			if msg.Result.Usage != nil && s.accumulatedUsage == (llm.Usage{}) {
				s.accumulatedUsage = *msg.Result.Usage
			}
			s.mu.Unlock()
		}

		// Capture usage from any assistant message that carries it.
		// With --include-partial-messages the final accumulated usage may
		// arrive on a partial-subtype message, so we do not filter by subtype.
		if msg.Assistant != nil && msg.Assistant.Message.Usage != nil {
			s.mu.Lock()
			s.latestUsage = msg.Assistant.Message.Usage
			// Accumulate: ADD per-message usage (Claude path — each message is a delta)
			u := msg.Assistant.Message.Usage
			s.accumulatedUsage.InputTokens += u.InputTokens
			s.accumulatedUsage.OutputTokens += u.OutputTokens
			s.accumulatedUsage.CacheReadInputTokens += u.CacheReadInputTokens
			s.accumulatedUsage.CacheCreationInputTokens += u.CacheCreationInputTokens
			s.mu.Unlock()
		}

		// Accumulate from provider-synthesized usage updates (Codex path).
		// These carry cumulative totals — use SET semantics.
		if msg.UsageUpdate != nil {
			s.mu.Lock()
			s.accumulatedUsage.InputTokens = msg.UsageUpdate.InputTokens
			s.accumulatedUsage.OutputTokens = msg.UsageUpdate.OutputTokens
			if msg.UsageUpdate.CacheReadInputTokens > 0 {
				s.accumulatedUsage.CacheReadInputTokens = msg.UsageUpdate.CacheReadInputTokens
			}
			if msg.UsageUpdate.CacheCreationInputTokens > 0 {
				s.accumulatedUsage.CacheCreationInputTokens = msg.UsageUpdate.CacheCreationInputTokens
			}
			// Track context window for ContextPercentage() (Codex provides this
			// via token usage update notifications).
			if msg.UsageUpdate.ContextWindow > 0 {
				if s.latestUsage == nil {
					s.latestUsage = &llm.Usage{}
				}
				s.latestUsage.ContextWindow = msg.UsageUpdate.ContextWindow
				s.latestUsage.ContextBaseline = msg.UsageUpdate.ContextBaseline
				// Only refresh the fill snapshot when the provider sent a
				// non-zero one. If Last is empty (thread resume before first
				// turn, Codex's fill_to_context_window corruption — see
				// openai/codex#16068), keep prior values rather than falling
				// back to lifetime-cumulative InputTokens, which would pin
				// the display at 100% for mature sessions.
				if msg.UsageUpdate.ContextTotalTokens > 0 {
					s.latestUsage.ContextTotalTokens = msg.UsageUpdate.ContextTotalTokens
					s.latestUsage.ContextInputTokens = msg.UsageUpdate.ContextInputTokens
					s.latestUsage.InputTokens = msg.UsageUpdate.ContextInputTokens
				}
			}
			s.mu.Unlock()
		}

		// Notify tool use callback for non-partial assistant messages containing
		// tool_use blocks. This catches tool calls that bypass control requests
		// (e.g., --dangerously-skip-permissions). Partial messages are skipped
		// because their tool inputs may be incomplete streaming fragments.
		if msg.Assistant != nil && msg.Subtype != "partial" && s.onToolAllowed != nil {
			for _, block := range msg.Assistant.Message.Content {
				if block.IsToolUse() {
					s.onToolAllowed(block.Name, block.Input)
				}
			}
		}

		if len(msg.FileReads) > 0 && s.onFileRead != nil {
			for _, read := range msg.FileReads {
				if read.FilePath != "" {
					s.onFileRead(read)
				}
			}
		}

		// Notify subagent (Task tool) lifecycle callback. These messages
		// are the main signal that a Task() call is still alive while the
		// main agent is blocked waiting for it to return, so we surface
		// them even though they don't otherwise affect session state.
		// Codex does not emit these.
		if (msg.TaskStarted != nil || msg.TaskProgress != nil || msg.TaskNotification != nil) && s.onSubagentEvent != nil {
			s.onSubagentEvent(msg)
		}

		// Stream events (from --include-partial-messages) are low-level API
		// deltas — too numerous for the message log. Forward delta events
		// (thinking, text, input_json) to the attach channel for the UI
		// spinner; skip everything else (message_start, content_block_stop, etc.).
		if msg.Type == "stream_event" {
			if msg.StreamDeltaType != "" {
				// Drop-oldest ring: a burst of deltas evicts stale
				// frames but never blocks readMessages or consumes
				// attachCh slots that critical messages need.
				s.streamRing.Push(msg)
			}
			continue
		}

		// Append to message log. Assistant messages always go through
		// UpdateLastAssistantPartial so that a final (non-partial) assistant
		// message replaces the accumulated partial from streaming deltas
		// instead of being appended alongside it.
		if msg.Assistant != nil {
			s.messageLog.UpdateLastAssistantPartial(msg)
		} else {
			s.messageLog.Append(msg)
		}

		notifiedExternal := false

		// Result status is a completion signal for consumers that immediately
		// inspect both the message log and manager-derived session state.
		// Notify the external callback after the log append but before statusCh
		// so receivers woken by statusCh observe a complete result view.
		if msg.Result != nil {
			if onMessage != nil {
				onMessage(msg)
				notifiedExternal = true
			}

			status := resultSubtypeToStatus(msg.Result)
			select {
			case s.statusCh <- status:
			default:
			}
		}

		// Result is usually the SDK's terminal signal for a Claude wrapper
		// turn. The wrapper (cat | shell ... nativeBin) can keep `cat`
		// blocked on stdin after the native binary exits, which prevents
		// process.Wait() from returning and leaves s.done unclosed. Close
		// stdin so cat drains, and start a watchdog that escalates
		// SIGTERM/SIGKILL if the wrapper isn't gone within the grace
		// window. PID is captured here so the watchdog never has to
		// acquire s.mu, which the cleanup defer holds while blocked in
		// process.Wait().
		//
		// The Codex provider (`codex app-server`) is multi-turn: the
		// process stays alive across turns and a Result on
		// turn/completed is NOT a process-exit signal. Closing stdin
		// here would EOF the JSON-RPC channel and kill the app-server,
		// which breaks the SessionWaitingHelp branch in
		// agent.waitForStatus that needs the session alive to deliver
		// a follow-up user message. Skip the wrapper-cleanup logic for
		// codex; cleanup happens via the explicit Stop() path instead.
		// Loop-managed Claude sessions also keep stdin open for truncated
		// turns so waitForStatus can send its auto-resume message on the
		// same session. Gate on the producer's lifecycle, not just the
		// message type.
		if msg.Result != nil && s.shouldShutdownOnResult(msg.Result) && s.resultShutdownStarted.CompareAndSwap(false, true) {
			s.mu.Lock()
			stdinW := s.stdin
			s.stdin = nil
			var pid int
			if s.process != nil && s.process.Process != nil {
				pid = s.process.Process.Pid
			}
			s.mu.Unlock()

			if stdinW != nil {
				_ = stdinW.Close()
			}
			if pid > 0 {
				go s.escalateAfterResult(pid, s.resultShutdownGraceDuration())
			}
		}

		// Forward to attach channel. Result and unhandled ControlRequest
		// messages are critical — the TUI relies on them to clear the
		// "Thinking…" state and to surface permission/question prompts.
		//
		// ControlRequest takes the dedicated controlCh path (forwarder
		// goroutine bridges into attachCh with a much larger timeout)
		// because dropping one strands the SDK subprocess waiting for a
		// response, which the LLM eventually surfaces as an opaque
		// "tool errored" — see forwardControlRequests for the design
		// rationale.
		//
		// Result still uses the bounded-blocking send onto attachCh:
		// dropping a Result is recoverable (the TUI's spinner state
		// snaps back on the next assistant message), and routing
		// Results onto controlCh would couple two different timeouts
		// onto a shared queue.
		if msg.ControlRequest != nil {
			select {
			case s.controlCh <- msg:
			case <-s.done:
				// Session shutting down; drop is fine.
			}
		} else if msg.Result != nil {
			attachTimeout := s.criticalAttachTimeout()
			criticalSendTimer := time.NewTimer(attachTimeout)
			select {
			case s.attachCh <- msg:
			case <-criticalSendTimer.C:
				log.Printf("session %s: dropped critical SDK message (type=%s) after %s on full attachCh", s.id, msg.Type, attachTimeout)
				// Additive metric: surface the drop to observability so
				// dashboards / watchdogs can alert on a stuck consumer
				// without having to grep agentic.log. The log line above
				// is intentionally preserved — the reporter is additive.
				s.mu.Lock()
				reporter := s.attachDropReporter
				featureID := s.featureID
				phaseName := s.phase.String()
				s.mu.Unlock()
				if reporter != nil {
					reporter.ReportAttachDrop(s.id, featureID, phaseName, msg.Type, attachTimeout)
				}
			}
			criticalSendTimer.Stop()
		} else {
			select {
			case s.attachCh <- msg:
			default:
			}
		}

		// Notify external callback
		if onMessage != nil && !notifiedExternal {
			onMessage(msg)
		}
	}

	// If the scanner exited due to an error (e.g., token too long for a
	// >10MB line), the subprocess may still be running and blocked writing
	// to the full pipe buffer. Kill the process so cmd.Wait() can return
	// and the session reaches a terminal state instead of hanging.
	if scanner.Err() != nil {
		s.mu.Lock()
		proc := s.process
		s.mu.Unlock()
		if proc != nil && proc.Process != nil {
			_ = proc.Process.Kill()
		}
	}
}

// tryHandleControlRequest attempts to handle a control_request synchronously.
// Returns true if the request was fully handled (response sent to CLI) and the
// message should NOT be forwarded to the TUI/attach channel.
// Returns false if the request needs user interaction (AskUserQuestion, deferred
// permission) and must be surfaced to the TUI.
func (s *Session) tryHandleControlRequest(msg llm.SDKMessage) bool {
	req := msg.ControlRequest
	if req == nil {
		return false
	}

	// Hook callbacks from the initialize handshake: always auto-continue.
	if req.Request.Subtype == "hook_callback" {
		if s.protocol != nil {
			_ = s.protocol.RespondToHook(req.RequestID)
		} else {
			_ = s.writeJSON(llm.NewHookContinueResponse(req.RequestID))
		}
		return true
	}

	// Only handle can_use_tool requests from here.
	if req.Request.Subtype != "can_use_tool" {
		return false
	}

	// AskUserQuestion normally surfaces to the TUI, except for allowlisted
	// confidence-qualified creator questions that the session can answer safely.
	if req.Request.ToolName == "AskUserQuestion" {
		if s.tryAutoPickAskUser(req) {
			return true
		}
		return false
	}

	handler := s.permHandler
	if handler == nil {
		return false
	}

	var sessionID string
	if s.protocol != nil {
		sessionID = s.protocol.SessionID()
	}
	permReq := ToolPermissionRequest{
		RequestID:    req.RequestID,
		ToolName:     req.Request.ToolName,
		Input:        string(req.Request.Input),
		SessionID:    sessionID,
		FeatureID:    s.featureID,
		ProviderName: s.providerName,
	}

	decision, err := handler.CanUseTool(permReq)
	if err != nil {
		s.respondToControlViaProtocol(req.RequestID, false, nil, err.Error())
		return true
	}

	switch decision.Behavior {
	case "allow":
		s.respondToControlViaProtocol(req.RequestID, true, req.Request.Input, "")
		if s.onToolAllowed != nil {
			s.onToolAllowed(req.Request.ToolName, req.Request.Input)
		}
		return true
	case "deny":
		s.respondToControlViaProtocol(req.RequestID, false, nil, decision.Reason)
		return true
	case "":
		return false
	default:
		s.respondToControlViaProtocol(req.RequestID, true, req.Request.Input, "")
		if s.onToolAllowed != nil {
			s.onToolAllowed(req.Request.ToolName, req.Request.Input)
		}
		return true
	}
}

func (s *Session) tryAutoPickAskUser(req *llm.ControlRequestMessage) bool {
	if req == nil || s.askUserAutoPick == nil || s.askUserAutoPick.LoadInquireness == nil {
		return false
	}
	if !askUserAutoPickPurposeCanPick(s.askUserAutoPick.Purpose) {
		return false
	}
	inquireness, err := s.askUserAutoPick.LoadInquireness()
	if err != nil {
		return false
	}
	decision := decideAskUserAutoPick(req.Request.Input, askUserAutoPickDecisionContext{
		Purpose:     s.askUserAutoPick.Purpose,
		Inquireness: inquireness,
	})
	if !decision.Pickable {
		if toolUseInput, ok := s.matchingAskUserToolUseInput(req.Request.Input); ok {
			decision = decideAskUserAutoPick(toolUseInput, askUserAutoPickDecisionContext{
				Purpose:     s.askUserAutoPick.Purpose,
				Inquireness: inquireness,
			})
		}
	}
	if !decision.Pickable {
		return false
	}

	confidenceByQuestion := make(map[string]float64, len(decision.Selections))
	for _, selection := range decision.Selections {
		confidenceByQuestion[selection.Question] = selection.Confidence
	}
	if err := s.respondToAskUserAutoPicked(req.RequestID, req.Request.Input, decision.Answers, confidenceByQuestion); err != nil {
		return false
	}

	if s.askUserAutoPick.OnQuestionAutoPicked != nil {
		for _, selection := range decision.Selections {
			s.askUserAutoPick.OnQuestionAutoPicked(selection.Question, selection.Answer, selection.Confidence)
		}
	}
	return true
}

func (s *Session) matchingAskUserToolUseInput(controlInput json.RawMessage) (json.RawMessage, bool) {
	controlSignature, ok := askUserAutoPickSignature(controlInput)
	if !ok {
		return nil, false
	}
	blocks := s.messageLog.ToolUseBlocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		if block.Name != "AskUserQuestion" || len(block.Input) == 0 {
			continue
		}
		toolUseSignature, ok := askUserAutoPickSignature(block.Input)
		if ok && toolUseSignature == controlSignature {
			return block.Input, true
		}
	}
	return nil, false
}

// respondToControlViaProtocol sends a control response through the protocol or direct writeJSON.
func (s *Session) respondToControlViaProtocol(requestID string, allow bool, originalInput json.RawMessage, reason string) {
	if s.protocol != nil {
		_ = s.protocol.RespondToControl(requestID, allow, originalInput, reason)
	} else if allow {
		_ = s.writeJSON(llm.NewAllowResponse(requestID, originalInput))
	} else {
		_ = s.writeJSON(llm.NewDenyResponse(requestID, reason))
	}
}

// CloseStdin closes the session's stdin pipe, signalling EOF to the subprocess.
func (s *Session) CloseStdin() {
	s.mu.Lock()
	w := s.stdin
	s.stdin = nil
	s.mu.Unlock()
	if w != nil {
		_ = w.Close()
	}
}

// writeJSON marshals v to JSON and writes it to stdin followed by a newline.
func (s *Session) writeJSON(v interface{}) error {
	s.mu.Lock()
	w := s.stdin
	s.mu.Unlock()

	if w == nil {
		return fmt.Errorf("session stdin is closed")
	}

	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// SendUserMessage sends a user message to the session via JSON stdin.
func (s *Session) SendUserMessage(text string) error {
	s.mu.Lock()
	s.hasUnansweredQuestion = false
	s.clearPendingControlRequestsLocked()
	if s.status == SessionWaitingHelp && !s.hasUnansweredQuestion {
		s.status = SessionRunning
	}
	s.mu.Unlock()

	if s.protocol != nil {
		return s.protocol.SendUserMessage(text)
	}
	return s.writeJSON(llm.NewUserInput(text))
}

// RespondToControl sends a control response to a pending control request.
func (s *Session) RespondToControl(requestID string, allow bool, reason string) error {
	s.mu.Lock()
	var originalInput json.RawMessage
	if cr := s.findPendingControlRequestLocked(requestID); cr != nil {
		originalInput = cr.Request.Input
		s.removePendingControlRequestLocked(requestID)
	}
	s.mu.Unlock()

	if s.protocol != nil {
		return s.protocol.RespondToControl(requestID, allow, originalInput, reason)
	}

	if allow {
		return s.writeJSON(llm.NewAllowResponse(requestID, originalInput))
	}
	return s.writeJSON(llm.NewDenyResponse(requestID, reason))
}

// RespondToAskUser sends a control response that allows an AskUserQuestion
// tool use and supplies the user's answers. questions is the original JSON
// from the control request input; answers maps question text to the response;
// annotations carries optional per-question notes/preview that ride in the
// Claude Agent SDK `annotations` field of `updatedInput`.
func (s *Session) RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]llm.AskUserAnnotation) error {
	s.captureAskUserResponse(requestID, questions, answers, annotations, nil)

	if s.protocol != nil {
		return s.protocol.RespondToAskUser(requestID, questions, answers, annotations)
	}
	return s.writeJSON(llm.NewAskUserResponse(requestID, questions, answers, annotations))
}

func (s *Session) respondToAskUserAutoPicked(requestID string, questions json.RawMessage, answers map[string]string, confidenceByQuestion map[string]float64) error {
	if s.protocol != nil {
		if err := s.protocol.RespondToAskUser(requestID, questions, answers, nil); err != nil {
			return err
		}
	} else if err := s.writeJSON(llm.NewAskUserResponse(requestID, questions, answers, nil)); err != nil {
		return err
	}
	s.captureAskUserResponse(requestID, questions, answers, nil, confidenceByQuestion)
	s.appendAutoPickedAskUserMessages(questions, answers, confidenceByQuestion)
	return nil
}

func (s *Session) appendAutoPickedAskUserMessages(questions json.RawMessage, answers map[string]string, confidenceByQuestion map[string]float64) {
	if len(answers) == 0 || len(confidenceByQuestion) == 0 {
		return
	}
	for _, q := range askUserAnswerKeysInPresentedOrder(questions, answers) {
		confidence, ok := confidenceByQuestion[q]
		if !ok || answers[q] == "" {
			continue
		}
		s.messageLog.Append(llm.SDKMessage{
			Type:               "user",
			LocallyAppended:    true,
			AutoPicked:         true,
			AutoPickQuestion:   q,
			AutoPickConfidence: confidence,
			User: &llm.UserMessage{
				Message: llm.ConversationMsg{
					Role:    "user",
					Content: []llm.ContentBlock{{Type: "text", Text: answers[q]}},
				},
			},
		})
	}
}

func (s *Session) captureAskUserResponse(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]llm.AskUserAnnotation, confidenceByQuestion map[string]float64) {
	s.mu.Lock()
	s.removePendingControlRequestLocked(requestID)
	// hasUnansweredQuestion stays true while any other AskUserQuestion
	// is still outstanding — only the last response answer clears it.
	s.hasUnansweredQuestion = s.hasPendingAskUserQuestionLocked()
	if s.status == SessionWaitingHelp && !s.hasUnansweredQuestion {
		s.status = SessionRunning
	}
	keys := askUserAnswerKeysInPresentedOrder(questions, answers)
	for _, q := range keys {
		confidence, autoPicked := confidenceByQuestion[q]
		s.qaLog = append(s.qaLog, QAPair{
			Question:   q,
			Answer:     answers[q],
			Notes:      annotations[q].Notes,
			AutoPicked: autoPicked,
			Confidence: confidence,
		})
	}
	s.mu.Unlock()
}

func askUserAnswerKeysInPresentedOrder(questions json.RawMessage, answers map[string]string) []string {
	if len(answers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(answers))
	seen := make(map[string]bool, len(answers))
	appendIfAnswered := func(question string) {
		if _, ok := answers[question]; !ok || seen[question] {
			return
		}
		keys = append(keys, question)
		seen[question] = true
	}

	var bundle struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(questions, &bundle); err == nil {
		for _, q := range bundle.Questions {
			appendIfAnswered(q.Question)
		}
	}
	if len(keys) == 0 {
		var direct []struct {
			Question string `json:"question"`
		}
		if err := json.Unmarshal(questions, &direct); err == nil {
			for _, q := range direct {
				appendIfAnswered(q.Question)
			}
		}
	}

	if len(keys) < len(answers) {
		remaining := make([]string, 0, len(answers)-len(keys))
		for q := range answers {
			if !seen[q] {
				remaining = append(remaining, q)
			}
		}
		sort.Strings(remaining)
		keys = append(keys, remaining...)
	}
	return keys
}

// ClearPendingQuestion synchronously clears the AskUserQuestion control
// request state for a specific requestID. Call this before the async
// goroutine that writes the control_response, so that re-attaching
// before the write completes does not re-show the question. Other
// concurrently pending requests (e.g. parallel AUQ calls) remain in the
// pending list and stay visible to the TUI.
func (s *Session) ClearPendingQuestion(requestID string) {
	s.mu.Lock()
	s.removePendingControlRequestLocked(requestID)
	s.hasUnansweredQuestion = s.hasPendingAskUserQuestionLocked()
	if s.status == SessionWaitingHelp && !s.hasUnansweredQuestion {
		s.status = SessionRunning
	}
	s.mu.Unlock()
}

// SetHasUnansweredQuestion atomically sets hasUnansweredQuestion under the
// session mutex. Use this from any goroutine (e.g. phase runners, readyCheck
// closures) instead of writing the field directly.
func (s *Session) SetHasUnansweredQuestion(v bool) {
	s.mu.Lock()
	s.hasUnansweredQuestion = v
	s.mu.Unlock()
}

// HasUnansweredQuestion returns hasUnansweredQuestion under the session mutex.
func (s *Session) HasUnansweredQuestion() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasUnansweredQuestion
}

// HasPendingAskUserQuestion returns true when the session is currently waiting
// on at least one AskUserQuestion control request.
func (s *Session) HasPendingAskUserQuestion() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasUnansweredQuestion {
		return true
	}
	return s.hasPendingAskUserQuestionLocked()
}

// hasPendingAskUserQuestionLocked reports whether any pending
// control_request is for the AskUserQuestion tool. Caller must hold s.mu.
func (s *Session) hasPendingAskUserQuestionLocked() bool {
	for _, cr := range s.pendingControlRequests {
		if cr != nil && cr.Request.ToolName == "AskUserQuestion" {
			return true
		}
	}
	return false
}

// SendInitialize sends the SDK initialize handshake to activate the
// control_request protocol. Deprecated: use protocol.Handshake() instead.
func (s *Session) SendInitialize() error {
	return s.writeJSON(llm.NewInitializeRequest())
}

// Protocol returns the session's protocol handler.
func (s *Session) Protocol() llm.Protocol {
	return s.protocol
}

// SendInput writes raw bytes to stdin. For backward compatibility.
// New code should use SendUserMessage or RespondToControl.
func (s *Session) SendInput(data []byte) error {
	s.mu.Lock()
	w := s.stdin
	s.mu.Unlock()

	if w == nil {
		return fmt.Errorf("session stdin is closed")
	}
	_, err := w.Write(data)
	return err
}

// Interrupt cancels the agent's current turn while keeping the session
// alive for follow-up messages. Prefers the provider's protocol-level
// interrupt (Claude: control_request/interrupt); falls back to SIGINT on
// the CLI's process group when the provider returns ErrNotSupported
// (Codex). Safe to call when no turn is active — the CLI treats it as a
// no-op and replies with an interrupted-turn result.
func (s *Session) Interrupt() error {
	s.mu.Lock()
	p := s.protocol
	proc := s.process
	s.mu.Unlock()

	if p != nil {
		err := p.Interrupt()
		if err == nil {
			return nil
		}
		if !errors.Is(err, llm.ErrNotSupported) {
			return err
		}
	}

	if proc == nil || proc.Process == nil {
		return nil
	}
	pgid := proc.Process.Pid
	if err := syscall.Kill(-pgid, syscall.SIGINT); err != nil {
		return fmt.Errorf("sending SIGINT to session pgid %d: %w", pgid, err)
	}
	return nil
}

// Stop gracefully stops the session: close stdin → SIGTERM → SIGKILL.
func (s *Session) Stop() error {
	s.mu.Lock()
	proc := s.process
	w := s.stdin
	s.mu.Unlock()

	if proc == nil || proc.Process == nil {
		return nil
	}

	// Close stdin to signal EOF
	if w != nil {
		_ = w.Close()
		s.mu.Lock()
		s.stdin = nil
		s.mu.Unlock()
	}

	// Wait up to 5s for exit
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-s.done:
		return nil
	case <-timer.C:
	}

	// SIGTERM the entire process group so child processes are also signaled.
	// The process was started with Setpgid: true, so its PGID equals its PID.
	pgid := proc.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	timer2 := time.NewTimer(5 * time.Second)
	defer timer2.Stop()
	select {
	case <-s.done:
		return nil
	case <-timer2.C:
	}

	// SIGKILL the entire process group
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	<-s.done
	return nil
}

func (s *Session) Wait() {
	<-s.done
}

// shouldShutdownOnResult reports whether this session's underlying CLI should
// be torn down after a Result message. Single-shot autonomous sessions need
// wrapper cleanup (stdin close + SIGTERM/SIGKILL watchdog) to unstick a
// process.Wait() that would otherwise hang on the wrapper's `cat`.
// Multi-turn server CLIs (Codex `app-server`) and interactive Tweak sessions
// stay alive after a Result and must not be torn down here; Stop() or user EOF
// is the explicit cleanup path for them. Loop-managed Claude sessions also
// keep stdin open for truncated turns so the waiter can send its auto-resume
// continuation before any explicit Stop().
func (s *Session) shouldShutdownOnResult(result *llm.ResultMessage) bool {
	s.mu.Lock()
	name := s.providerName
	kind := s.kind
	turnMode := s.turnMode
	keepAliveOnTruncatedResult := s.keepAliveOnTruncatedResult
	s.mu.Unlock()
	if turnMode == ports.TurnModeInteractive || kind == ports.KindTweak {
		return false
	}
	if keepAliveOnTruncatedResult && result.IsTurnTruncated() {
		return false
	}
	return name != "codex"
}

// escalateAfterResult runs in a goroutine after a result message: it waits
// for the subprocess (whose group ID equals pid because Start used
// Setpgid: true) to exit on its own, then sends SIGTERM, then SIGKILL.
// Each stage is short-circuited if s.done closes (the readMessages
// cleanup defer's process.Wait() returned). The pid and grace are
// captured by the caller — the goroutine reads no shared state — so
// this never needs to acquire s.mu (which the cleanup defer holds
// while blocked in process.Wait()).
func (s *Session) escalateAfterResult(pid int, grace time.Duration) {
	select {
	case <-s.done:
		return
	case <-time.After(grace):
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-s.done:
		return
	case <-time.After(grace):
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// Done returns a channel that is closed when the session exits.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// CloseDone closes the done channel, signaling session exit.
// This is exposed for testing; production code closes done via the read loop.
func (s *Session) CloseDone() {
	select {
	case <-s.done:
		// already closed
	default:
		close(s.done)
	}
}

// SetLogFile sets the log file for writing session output.
func (s *Session) SetLogFile(f *os.File) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logFile = f
}

// logDebug writes a formatted debug message to the session's log file.
// If no log file is set, the message is silently discarded. This avoids
// writing to stderr which would corrupt the BubbleTea TUI.
func (s *Session) logDebug(format string, args ...interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logFile != nil {
		msg := fmt.Sprintf(format, args...)
		_, _ = s.logFile.Write([]byte(msg + "\n"))
	}
}

// IdleDuration returns how long since the session last produced output.
func (s *Session) IdleDuration() time.Duration {
	// JSON protocol sessions do not track idle output duration; report zero.
	return 0
}

// IsActive returns true if the session is still running or waiting for input.
func (s *Session) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status == SessionRunning || s.status == SessionWaitingPermission || s.status == SessionWaitingHelp
}

// SetStatus sets the session status under the mutex.
func (s *Session) SetStatus(status SessionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// Status returns the session status under the mutex.
func (s *Session) Status() SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// ContextPercentage returns the estimated context window usage as a percentage (0-100).
// Returns -1 if no usage data is available yet.
//
// Two provider styles are supported:
//
//   - Codex: ContextTotalTokens holds Last.TotalTokens (input + output +
//     reasoning). ContextBaseline holds the 12K fixed-overhead constant that
//     Codex subtracts from both numerator and denominator in its own
//     `/status` display, so Agentic matches Codex within rounding.
//   - Claude: cache buckets do not overlap with InputTokens, so the fill is
//     the sum of all three. No baseline subtraction is applied.
func (s *Session) ContextPercentage() int {
	s.mu.Lock()
	var usage *llm.Usage
	if s.latestUsage != nil {
		usageCopy := *s.latestUsage
		usage = &usageCopy
	}
	s.mu.Unlock()

	if usage == nil || usage.ContextWindow == 0 {
		return -1
	}

	var total int
	if usage.ContextTotalTokens > 0 {
		total = usage.ContextTotalTokens
	} else {
		total = usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	}

	baseline := usage.ContextBaseline
	used := total - baseline
	if used < 0 {
		used = 0
	}
	window := usage.ContextWindow - baseline
	if window <= 0 {
		return -1
	}
	pct := used * 100 / window
	if pct > 100 {
		pct = 100
	}
	return pct
}

// ResetWaitingStatus transitions the session out of a waiting state.
func (s *Session) ResetWaitingStatus() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == SessionWaitingPermission || s.status == SessionWaitingHelp {
		s.status = SessionRunning
	}
}

// AddCleanupFunc appends a function to be called when the session exits.
// SetOnToolAllowed registers a callback invoked when a tool use is auto-approved.
func (s *Session) SetOnToolAllowed(fn func(toolName string, input json.RawMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onToolAllowed = fn
}

// SetOnFileRead registers a callback invoked when the provider reports a file
// read through a provider-neutral signal.
func (s *Session) SetOnFileRead(fn func(read llm.FileReadEvent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onFileRead = fn
}

// SetOnSubagentEvent registers a callback invoked whenever the session
// receives a subagent (Task tool) progress or notification message. The
// callback is fire-and-forget and must not block on channel sends or I/O.
func (s *Session) SetOnSubagentEvent(fn func(msg llm.SDKMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSubagentEvent = fn
}

func (s *Session) AddCleanupFunc(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupFuncs = append(s.cleanupFuncs, fn)
}

// resultSubtypeToStatus maps a ResultMessage subtype to a status string.
// Only "success" maps to SUCCESS; "error" maps to API_ERROR; everything
// else (max_turns, max_budget, etc.) maps to FAILED because the agent
// did not complete its task.
func resultSubtypeToStatus(result *llm.ResultMessage) string {
	switch {
	case result.Subtype == "error":
		return "API_ERROR"
	case result.IsSuccess():
		return "SUCCESS"
	default:
		return "FAILED"
	}
}

// updatePIDFileSessionID re-writes the PID file to include the session ID.
func (s *Session) updatePIDFileSessionID(pidDir, sessionID string) error {
	s.mu.Lock()
	if s.process == nil || s.process.Process == nil {
		s.mu.Unlock()
		return fmt.Errorf("session process is not available")
	}
	pid := s.process.Process.Pid
	repoName := s.repoName
	s.mu.Unlock()

	pf := PIDFile{
		PID:       pid,
		StartedAt: s.startedAt,
		FeatureID: s.featureID,
		Phase:     s.phase.String(),
		Iteration: s.iteration,
		SessionID: sessionID,
		RepoName:  repoName,
	}
	return WritePIDFile(pidDir, pf)
}

// AttachCh returns the channel for receiving messages in attach mode.
func (s *Session) AttachCh() <-chan llm.SDKMessage {
	return s.attachCh
}

// SetAttached is kept for backward compatibility during migration.
// In the new architecture, attach mode uses AttachCh() instead.
func (s *Session) SetAttached(attached bool, out *os.File) {
	// No-op in JSON mode — attach mode is handled via AttachCh()
}
