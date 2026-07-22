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
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// SDKEventMsg carries a structured SDK message from a session to the desktop app.
//
// Consumers SHOULD read FeatureID and Phase directly. Never re-derive
// identity from SessionID — the session manager captures both values at
// emission time, and string-parsing the SessionID is brittle (every new
// session role would otherwise need an extra case in the desktop app's parser,
// and an omitted case silently routes the event to the default phase
// and a bogus feature directory).
type SDKEventMsg struct {
	SessionID string
	FeatureID string
	Phase     feature.Phase
	StartedAt time.Time
	Message   llm.SDKMessage
	// RecordCount is the session's transcript record count
	// (len(s.MessageLog().Messages())) at the moment this event was
	// emitted — the same index space session_model.go's /output/stream and
	// /transcript endpoints use.
	RecordCount int
}

// SessionStartedMsg signals that a session has been registered and is visible
// through the session list/read APIs.
//
// Consumers SHOULD read FeatureID and Phase directly. Never re-derive
// identity from SessionID — see SDKEventMsg for rationale.
type SessionStartedMsg struct {
	SessionID string
	FeatureID string
	Phase     feature.Phase
	StartedAt time.Time
	Status    SessionStatus
}

// SessionDoneMsg signals that a session has ended.
//
// Consumers SHOULD read FeatureID and Phase directly. Never re-derive
// identity from SessionID — see SDKEventMsg for rationale.
type SessionDoneMsg struct {
	SessionID string
	FeatureID string
	Phase     feature.Phase
	StartedAt time.Time
	Status    SessionStatus
}

// assertEmissionIdentity logs a warning if an event being emitted has an
// empty FeatureID or Phase-name. It never panics — the warning is the
// forcing function that surfaces a regression (a new callsite that forgot
// to thread identity through) without breaking production traffic.
func assertEmissionIdentity(sessionID, featureID string, phase feature.Phase) {
	if featureID == "" || phase.String() == "" {
		log.Printf("session-manager: emit with empty identity — sessionID=%q featureID=%q phase=%q",
			sessionID, featureID, phase.String())
	}
}

// ErrShuttingDown is returned by StartSession when the manager is shutting down.
// Aliased to ports.ErrSessionShuttingDown so callers can use either package's
// sentinel with errors.Is.
var ErrShuttingDown = ports.ErrSessionShuttingDown

// codexHandshakeTimeout is the default maximum time to wait for each step of the
// Codex JSON-RPC handshake (initialize response, thread/start response).
var codexHandshakeTimeout = 30 * time.Second

type Manager struct {
	sessions           map[string]*Session
	attached           *Session
	mu                 sync.RWMutex
	eventCh            chan interface{}
	stoppingCh         chan struct{} // closed when Shutdown begins
	attachDropReporter AttachDropReporter
}

func NewManager(eventCh chan interface{}) *Manager {
	return &Manager{
		sessions:   make(map[string]*Session),
		eventCh:    eventCh,
		stoppingCh: make(chan struct{}),
	}
}

// NewRecoveringManager constructs a manager and restores live sessions whose
// durable process and transcript metadata survived an abrupt server exit.
func NewRecoveringManager(eventCh chan interface{}, stateDir string) *Manager {
	m := NewManager(eventCh)
	if stateDir != "" {
		if err := m.restoreLiveSessions(stateDir); err != nil {
			log.Printf("session-manager: restore live sessions: %v", err)
		}
	}
	return m
}

// SetAttachDropReporter installs an AttachDropReporter on the Manager.
// Subsequent sessions started via StartSession will forward attachCh
// drop events (critical-message timeouts) to this reporter. Safe to
// call on a nil Manager and before/after sessions are started.
func (m *Manager) SetAttachDropReporter(r AttachDropReporter) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.attachDropReporter = r
	// Propagate to any sessions already running so in-flight long
	// sessions also emit the metric. Safe even if the reporter was
	// installed before any sessions started.
	for _, s := range m.sessions {
		s.SetAttachDropReporter(r)
	}
	m.mu.Unlock()
}

// SessionOpts aliases the canonical port type. Session keeps the alias so
// existing callers can construct options via session.SessionOpts{...}.
type SessionOpts = ports.SessionOpts

func (m *Manager) StartSession(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*SessionOpts) (SessionHandle, error) {
	// Refuse new sessions after Shutdown has been called. stoppingCh is set at
	// construction, so this read needs no lock; it is re-checked under the
	// registration lock below to close the race with a Shutdown that fires
	// while this session is mid-handshake.
	select {
	case <-m.stoppingCh:
		return nil, ErrShuttingDown
	default:
	}

	// The subprocess spawn and protocol handshake below can take many seconds
	// (up to codexHandshakeTimeout). They run WITHOUT the manager lock so they
	// never block readers — ActiveSessions feeds live-preview/prompts/sessions,
	// and stalling those during a resume is what makes the desktop app's refresh time
	// out. The session is registered (made visible to readers) only once it is
	// fully started below.
	s := NewSession(id, featureID, phase)
	if len(opts) > 0 && opts[0] != nil {
		s.pidDir = opts[0].PIDDir
		s.runNumber = opts[0].RunNumber
		s.iteration = opts[0].Iteration
		if opts[0].PermHandler != nil {
			s.permHandler = opts[0].PermHandler
		}
		s.initialPrompt = opts[0].InitialPrompt
		s.seedInitialPromptContextEstimate(opts[0].InitialPrompt, opts[0].ContextWindow)
		s.repoName = opts[0].RepoName
		s.permCacheScope = opts[0].PermCacheScope
		s.providerName = opts[0].ProviderName
		if opts[0].Protocol != nil {
			s.protocol = opts[0].Protocol
		}
		if opts[0].CriticalAttachSendTimeout > 0 {
			s.criticalAttachSendTimeout = opts[0].CriticalAttachSendTimeout
		}
		if opts[0].ResultShutdownGrace > 0 {
			s.resultShutdownGrace = opts[0].ResultShutdownGrace
		}
		s.stderrPath = opts[0].StderrPath
		s.keepAliveOnTruncatedResult = opts[0].KeepAliveOnTruncatedResult
		s.kind = opts[0].Kind
		s.turnMode = opts[0].TurnMode
		s.label = opts[0].Label
		s.effectiveEffort = opts[0].EffectiveEffort
		s.effortSource = opts[0].EffortSource
		s.askUserAutoPick = opts[0].AskUserAutoPick
		s.watchdog = newSessionWatchdog(s, opts[0].Watchdog)
		if s.pidDir != "" {
			sum := sha256.Sum256([]byte(id))
			s.transcriptPath = filepath.Join(s.pidDir, fmt.Sprintf("session-transcript-%x.jsonl", sum[:8]))
			if err := os.MkdirAll(s.pidDir, 0o755); err != nil {
				return nil, fmt.Errorf("creating transcript directory: %w", err)
			}
			if err := os.Remove(s.transcriptPath); err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("resetting transcript: %w", err)
			}
		}
		// Set log file before Start() so the read goroutine can write from
		// the first line. This avoids a race with fast sessions (codex)
		// where readMessages exits before SetLogFile is called.
		if opts[0].LogPath != "" {
			if f, err := os.Create(opts[0].LogPath); err == nil {
				s.SetLogFile(f)
			}
		}
	}

	onMessage := func(msg llm.SDKMessage) {
		m.handleSessionMessage(s, id, featureID, phase, msg)
	}

	if err := s.Start(command, workdir, env, onMessage); err != nil {
		return nil, fmt.Errorf("starting session: %w", err)
	}
	if len(opts) > 0 && opts[0] != nil {
		noticeCtx := ports.SessionBuildNoticeContext{
			SessionID: id,
			FeatureID: featureID,
			Phase:     phase,
			RepoName:  opts[0].RepoName,
			Iteration: opts[0].Iteration,
		}
		for _, notice := range opts[0].SessionBuildNotices {
			if strings.TrimSpace(notice.Status) != "" {
				_ = s.appendLocalStatus(notice.Status)
			}
			if notice.Emit != nil {
				notice.Emit(noticeCtx)
			}
		}
	}

	// Give the protocol access to stdin for writing.
	if s.protocol != nil {
		s.protocol.SetStdin(s.stdin)
	}

	// Protocol handshake.
	if s.protocol != nil {
		// Delegate handshake to the protocol (Claude: initialize + send prompt,
		// Codex: initialize → thread/start → turn/start).
		handshakeTimeout := codexHandshakeTimeout
		if len(opts) > 0 && opts[0] != nil && opts[0].CodexHandshakeTimeout > 0 {
			handshakeTimeout = opts[0].CodexHandshakeTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
		defer cancel()
		if err := s.protocol.Handshake(ctx); err != nil {
			s.Stop()
			return nil, fmt.Errorf("protocol handshake: %w", err)
		}
	} else {
		// Legacy path (no protocol set): Claude interactive.
		//
		// A fast session can exit right after emitting its result without ever
		// reading stdin (common with mock scripts in tests), closing the pipe
		// before these writes land. That EPIPE/closed-pipe error is benign: the
		// child already finished, so there is nothing left to hand off to.
		if err := s.SendInitialize(); err != nil && !isChildExitedWriteError(err) {
			_ = s.Stop()
			return nil, fmt.Errorf("sending initialize handshake: %w", err)
		}
		if len(opts) > 0 && opts[0] != nil && opts[0].InitialPrompt != "" {
			if err := s.SendUserMessage(opts[0].InitialPrompt); err != nil && !isChildExitedWriteError(err) {
				_ = s.Stop()
				return nil, fmt.Errorf("sending initial prompt: %w", err)
			}
		}
	}

	// Register the fully-started session under the lock, re-checking shutdown
	// (it may have fired during the handshake) so we never leave a
	// started-but-untracked session behind. The attach-drop reporter is applied
	// here so it reflects whatever is installed when the session becomes visible.
	m.mu.Lock()
	select {
	case <-m.stoppingCh:
		m.mu.Unlock()
		_ = s.Stop()
		return nil, ErrShuttingDown
	default:
	}
	if m.attachDropReporter != nil {
		s.SetAttachDropReporter(m.attachDropReporter)
	}
	m.sessions[id] = s
	m.mu.Unlock()

	if m.eventCh != nil {
		assertEmissionIdentity(id, featureID, phase)
		startedMsg := SessionStartedMsg{
			SessionID: id,
			FeatureID: featureID,
			Phase:     phase,
			StartedAt: s.StartedAt(),
			Status:    s.Status(),
		}
		select {
		case m.eventCh <- startedMsg:
		default:
			// SessionStartedMsg is an invalidation hint for UI discovery.
			// Dropping it under backpressure is acceptable: output/done events
			// and manual snapshots still converge, while StartSession remains
			// independent of dashboard consumers.
		}
	}

	// Monitor for completion
	go func() {
		s.Wait()
		if m.eventCh != nil {
			assertEmissionIdentity(id, featureID, phase)
			doneMsg := SessionDoneMsg{
				SessionID: id,
				FeatureID: featureID,
				Phase:     phase,
				StartedAt: s.StartedAt(),
				Status:    s.Status(),
			}
			// SessionDoneMsg is critical — use blocking send since it's a
			// one-time event and must not be dropped.
			m.eventCh <- doneMsg
		}
	}()

	return s, nil
}

func (m *Manager) restoreLiveSessions(stateDir string) error {
	pidFiles, err := FindPIDFiles(stateDir)
	if err != nil {
		return err
	}
	for _, pf := range pidFiles {
		if pf.ManagerID == "" || pf.FeatureID == "" || pf.Transcript == "" || !isProcessRunning(pf.PID) {
			continue
		}
		transcriptPath, ok := pathWithin(stateDir, pf.Transcript)
		if !ok {
			log.Printf("session-manager: skip session %q with transcript outside state directory", pf.ManagerID)
			continue
		}
		phase, ok := persistedPhase(pf.Phase)
		if !ok {
			log.Printf("session-manager: skip session %q with unknown phase %q", pf.ManagerID, pf.Phase)
			continue
		}
		messages, err := readPersistedTranscript(transcriptPath)
		if err != nil {
			log.Printf("session-manager: skip session %q: %v", pf.ManagerID, err)
			continue
		}
		s := NewSession(pf.ManagerID, pf.FeatureID, phase)
		s.runNumber = pf.RunNumber
		s.iteration = pf.Iteration
		s.repoName = pf.RepoName
		s.workDir = pf.WorkDir
		s.startedAt = pf.StartedAt
		s.providerName = pf.Provider
		s.kind = pf.Kind
		s.label = pf.Label
		s.pidDir = pf.Dir
		s.transcriptPath = transcriptPath
		s.recoveredPID = pf.PID
		for _, msg := range messages {
			s.messageLog.Append(msg)
		}
		m.sessions[s.id] = s
	}
	return nil
}

func pathWithin(root, candidate string) (string, bool) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return candidateAbs, true
}

func persistedPhase(name string) (feature.Phase, bool) {
	for _, phase := range []feature.Phase{
		feature.PhaseResearch, feature.PhasePlan, feature.PhaseImplement,
		feature.PhasePublish, feature.PhaseReview, feature.PhaseKnowledgeBase,
		feature.PhaseInquire, feature.PhaseDesign, feature.PhaseFinalReview,
	} {
		if phase.String() == name {
			return phase, true
		}
	}
	return 0, false
}

func readPersistedTranscript(path string) ([]llm.SDKMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening transcript: %w", err)
	}
	defer f.Close()

	var messages []llm.SDKMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var record persistedTranscriptRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			log.Printf("session-manager: skip malformed transcript row in %s: %v", path, err)
			continue
		}
		msg := record.Message.sdkMessage()
		switch {
		case record.Index == len(messages):
			messages = append(messages, msg)
		case record.Index >= 0 && record.Index < len(messages):
			messages[record.Index] = msg
		default:
			log.Printf("session-manager: skip out-of-order transcript row %d in %s", record.Index, path)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading transcript: %w", err)
	}
	return messages, nil
}

// isChildExitedWriteError reports whether a stdin write failed because the
// child process already closed its end of the pipe (it exited). Both EPIPE and
// io.ErrClosedPipe surface this race.
func isChildExitedWriteError(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, io.ErrClosedPipe)
}

func (m *Manager) handleSessionMessage(s *Session, id, featureID string, phase feature.Phase, msg llm.SDKMessage) {
	// Route messages to session status.
	s.mu.Lock()
	switch {
	case msg.ControlRequest != nil:
		// Hook callbacks are handled inline by handleControlRequest — don't
		// change status for them.
		if msg.ControlRequest.Request.Subtype == "hook_callback" {
			// no status change
		} else if msg.ControlRequest.Request.ToolName == "AskUserQuestion" {
			s.status = SessionWaitingHelp
			s.hasUnansweredQuestion = true
		} else {
			// Other tool permissions (Bash, etc.)
			s.status = SessionWaitingPermission
		}
	case msg.Result != nil:
		interactive := s.turnMode == ports.TurnModeInteractive
		// Result received — turn completed. Preserve WaitingHelp when an
		// AskUserQuestion is still outstanding. Interactive sessions also
		// return to WaitingHelp after an unblocked turn because the user is
		// expected to drive the next message.
		//
		// SendUserMessage resets WaitingHelp to Running on the next turn.
		if s.status == SessionWaitingHelp {
			if s.hasUnansweredQuestion || s.hasPendingAskUserQuestionLocked() {
				break
			}
			if interactive {
				s.status = SessionWaitingHelp
			} else {
				s.status = SessionRunning
			}
		} else if s.status == SessionWaitingPermission && len(s.pendingControlRequests) == 0 {
			if interactive {
				s.status = SessionWaitingHelp
			} else {
				s.status = SessionRunning
			}
		} else if interactive {
			s.status = SessionWaitingHelp
		}
	case msg.Assistant != nil:
		// An assistant message means the agent moved past the permission
		// prompt — but only if every control request was actually answered.
		// Codex emits thread/tokenUsage/updated (parsed as an assistant
		// partial) immediately after an approval request, which would
		// incorrectly clobber SessionWaitingPermission before the user can
		// respond.
		if s.status == SessionWaitingPermission && len(s.pendingControlRequests) == 0 {
			s.status = SessionRunning
		}
	}
	s.mu.Unlock()

	// Forward to desktop app event channel (non-blocking to avoid goroutine accumulation).
	if m.eventCh != nil {
		assertEmissionIdentity(id, featureID, phase)
		sdkEvt := SDKEventMsg{
			SessionID:   id,
			FeatureID:   featureID,
			Phase:       phase,
			StartedAt:   s.StartedAt(),
			Message:     msg,
			RecordCount: s.MessageLog().Len(),
		}
		select {
		case m.eventCh <- sdkEvt:
		default:
			// Channel full — drop the event. The desktop app will catch up on the
			// next message. This prevents unbounded goroutine accumulation
			// under high-volume partial-message streaming.
		}
	}
}

func (m *Manager) StopSession(id string) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session %s not found", id)
	}
	return s.Stop()
}

func (m *Manager) GetSession(id string) SessionView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.sessions[id]
	if s == nil {
		return nil
	}
	return s
}

func (m *Manager) ActiveSessions() []SessionView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []SessionView
	for _, s := range m.sessions {
		if s.IsActive() {
			result = append(result, s)
		}
	}
	return result
}

// RecentSessions returns the most recently started sessions across features,
// bounded by limit.
func (m *Manager) RecentSessions(limit int) []SessionView {
	if limit <= 0 {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]SessionView, 0, min(limit, len(m.sessions)))
	for _, s := range m.sessions {
		result = append(result, s)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].StartedAt().Equal(result[j].StartedAt()) {
			return result[i].ID() < result[j].ID()
		}
		return result[i].StartedAt().After(result[j].StartedAt())
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

// FeatureSessions returns all sessions (including completed ones) for a feature,
// sorted with the most recently started session first.
func (m *Manager) FeatureSessions(featureID string) []SessionView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []SessionView
	for _, s := range m.sessions {
		if s.featureID == featureID {
			result = append(result, s)
		}
	}
	// Sort by StartedAt descending (most recent first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartedAt().After(result[j].StartedAt())
	})
	return result
}

func (m *Manager) SendInput(sessionID string, data []byte) error {
	m.mu.RLock()
	s, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	return s.SendInput(data)
}

func (m *Manager) Attach(sessionID string) (SessionView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	m.attached = s
	return s, nil
}

// RegisterTestSession inserts a pre-built session into the manager for testing.
// It must only be used in tests.
func (m *Manager) RegisterTestSession(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.id] = s
}

func (m *Manager) Detach() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.attached != nil {
		m.attached.SetAttached(false, nil)
		m.attached = nil
	}
}

func (m *Manager) Shutdown() {
	// Signal that no new sessions should be created.
	select {
	case <-m.stoppingCh:
		// Already closed
	default:
		close(m.stoppingCh)
	}

	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.RUnlock()

	for _, s := range sessions {
		_ = s.Stop()
	}
}

// IsShuttingDown reports whether Shutdown has been initiated.
func (m *Manager) IsShuttingDown() bool {
	if m == nil {
		return false
	}
	select {
	case <-m.stoppingCh:
		return true
	default:
		return false
	}
}
