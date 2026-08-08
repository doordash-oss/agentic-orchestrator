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
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type sessionWatchdog struct {
	session                   *Session
	pendingToolIdleTimeout    time.Duration
	subagentToolIdleTimeout   time.Duration
	turnCompletionIdleTimeout time.Duration
	pollInterval              time.Duration

	mu             sync.Mutex
	lifecycle      watchdogToolLifecycle
	seq            uint64
	lastActivityAt time.Time
	// exemptSince marks entry into a human-wait state (pending permission or
	// unanswered question); the interval is excluded from the idle clock
	// without discarding idle time accrued before it.
	exemptSince time.Time

	startOnce sync.Once
}

type watchdogToolPhase uint8

const (
	watchdogToolInactive watchdogToolPhase = iota
	watchdogToolRunning
	watchdogToolAwaitingTurnResult
)

type watchdogTool struct {
	id       string
	name     string
	subagent bool
	// control marks a synthetic entry armed by an answered control request
	// (AskUserQuestion): the CLI owes a tool_result, and any subsequent
	// message disarms the entry.
	control bool
	// timeout is the invocation's declared execution timeout, when the
	// provider reported one; silence up to that long is legitimate.
	timeout time.Duration
}

// watchdogToolLifecycle tracks every pending tool of the current turn, not
// just the most recent one: providers can run several tools in parallel
// (e.g. subagent tasks), and a sibling completing must not make the watchdog
// treat the turn as awaiting its result while other tools are still running.
type watchdogToolLifecycle struct {
	pending      []watchdogTool // arming order
	awaitingTurn bool
	turnTool     watchdogTool // last completed tool, for the awaiting-turn message
}

func (l watchdogToolLifecycle) phase() watchdogToolPhase {
	switch {
	case len(l.pending) > 0:
		return watchdogToolRunning
	case l.awaitingTurn:
		return watchdogToolAwaitingTurnResult
	default:
		return watchdogToolInactive
	}
}

func (l watchdogToolLifecycle) displayTool() watchdogTool {
	if n := len(l.pending); n > 0 {
		return l.pending[n-1]
	}
	return l.turnTool
}

func (l watchdogToolLifecycle) anySubagentPending() bool {
	for _, tool := range l.pending {
		if tool.subagent {
			return true
		}
	}
	return false
}

func (l watchdogToolLifecycle) maxPendingDeclaredTimeout() time.Duration {
	var longest time.Duration
	for _, tool := range l.pending {
		if tool.timeout > longest {
			longest = tool.timeout
		}
	}
	return longest
}

// watchdogSnapshot is a point-in-time view handed from the poller to the
// failure path so both agree on what they are timing.
type watchdogSnapshot struct {
	phase    watchdogToolPhase
	tool     watchdogTool
	subagent bool
	declared time.Duration
	seq      uint64
}

func newSessionWatchdog(sess *Session, cfg *ports.SessionWatchdogConfig) *sessionWatchdog {
	if sess == nil || cfg == nil || (cfg.PendingToolIdleTimeout <= 0 && cfg.TurnCompletionIdleTimeout <= 0) {
		return nil
	}
	shortestTimeout := shortestPositiveDuration(cfg.PendingToolIdleTimeout, cfg.TurnCompletionIdleTimeout)
	interval := cfg.PollInterval
	if interval <= 0 || interval > shortestTimeout {
		interval = shortestTimeout / 4
		if interval <= 0 {
			interval = shortestTimeout
		}
		if interval > time.Second {
			interval = time.Second
		}
	}
	return &sessionWatchdog{
		session:                   sess,
		pendingToolIdleTimeout:    cfg.PendingToolIdleTimeout,
		subagentToolIdleTimeout:   cfg.SubagentToolIdleTimeout,
		turnCompletionIdleTimeout: cfg.TurnCompletionIdleTimeout,
		pollInterval:              interval,
		lastActivityAt:            time.Now(),
	}
}

func shortestPositiveDuration(a, b time.Duration) time.Duration {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

func (w *sessionWatchdog) Start() {
	if w == nil {
		return
	}
	w.startOnce.Do(func() {
		go w.run()
	})
}

func (w *sessionWatchdog) Observe(msg llm.SDKMessage) {
	if w == nil {
		return
	}
	now := time.Now()
	w.mu.Lock()
	w.lastActivityAt = now
	switch {
	case msg.Result != nil:
		w.lifecycle = watchdogToolLifecycle{}
		w.seq++
	case msg.ToolProgress != nil:
		w.lifecycle = w.lifecycle.observe(*msg.ToolProgress)
		w.seq++
	case msg.Assistant != nil || msg.User != nil:
		// Any post-answer message proves the CLI consumed a control
		// response, so synthetic pending windows disarm.
		if cleared, changed := w.lifecycle.clearControlTools(); changed {
			w.lifecycle = cleared
			w.seq++
		}
	}
	w.mu.Unlock()
}

// ResolveControlRequest starts a fresh idle window after a permission or
// question response. Pending-request ownership lives on Session; the watchdog
// only needs the activity boundary. An answered AskUserQuestion additionally
// arms a synthetic pending window: the CLI owes a tool_result for the
// question, and a turn that produces no further output is stalled. Any
// subsequent message disarms the window (Observe).
func (w *sessionWatchdog) ResolveControlRequest(requestID, toolName string) {
	if w == nil || requestID == "" {
		return
	}
	w.mu.Lock()
	w.lastActivityAt = time.Now()
	if toolName == "AskUserQuestion" {
		l := w.lifecycle.armTool(requestID, toolName, 0)
		for i := range l.pending {
			if l.pending[i].id == requestID {
				l.pending[i].control = true
			}
		}
		w.lifecycle = l
		w.seq++
	}
	w.mu.Unlock()
}

// ClearControlRequests starts a fresh idle window after a bulk request reset.
// Session remains the single source of truth for which requests are pending.
func (w *sessionWatchdog) ClearControlRequests() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.lastActivityAt = time.Now()
	if cleared, changed := w.lifecycle.clearControlTools(); changed {
		w.lifecycle = cleared
		w.seq++
	}
	w.mu.Unlock()
}

// clearControlTools drops synthetic control entries without transitioning to
// the awaiting-turn phase.
func (l watchdogToolLifecycle) clearControlTools() (watchdogToolLifecycle, bool) {
	var pending []watchdogTool
	for _, tool := range l.pending {
		if tool.control {
			continue
		}
		pending = append(pending, tool)
	}
	if len(pending) == len(l.pending) {
		return l, false
	}
	l.pending = pending
	return l, true
}

func (l watchdogToolLifecycle) observe(progress llm.ToolProgressMessage) watchdogToolLifecycle {
	data := strings.TrimSpace(progress.Data)
	id := strings.TrimSpace(progress.ToolUseID)
	name := strings.TrimSpace(progress.ToolName)
	switch {
	case isWatchdogPendingToolData(data):
		return l.armTool(id, name, time.Duration(progress.TimeoutMS)*time.Millisecond)
	case isWatchdogTerminalToolData(data):
		return l.completeTool(id, name)
	default:
		return l
	}
}

func (l watchdogToolLifecycle) armTool(id, name string, timeout time.Duration) watchdogToolLifecycle {
	l.awaitingTurn = false
	l.turnTool = watchdogTool{}
	pending := append([]watchdogTool(nil), l.pending...)
	l.pending = pending
	for i := range pending {
		if pending[i].id == id {
			if name != "" {
				pending[i].name = name
			}
			// Providers rename tools to display titles on later updates;
			// the subagent marker must survive the rename.
			pending[i].subagent = pending[i].subagent || isWatchdogSubagentToolName(name)
			if timeout > 0 {
				pending[i].timeout = timeout
			}
			return l
		}
	}
	l.pending = append(pending, watchdogTool{id: id, name: name, subagent: isWatchdogSubagentToolName(name), timeout: timeout})
	return l
}

func (l watchdogToolLifecycle) completeTool(id, name string) watchdogToolLifecycle {
	pending := append([]watchdogTool(nil), l.pending...)
	idx := -1
	for i := range pending {
		if pending[i].id == id {
			idx = i
			break
		}
	}
	if idx < 0 && id == "" && len(pending) > 0 {
		// A terminal update without an id closes the most recently armed tool.
		idx = len(pending) - 1
	}
	if idx < 0 && len(pending) > 0 {
		// A terminal update for a tool that was never armed cannot clear
		// tools that are still running.
		return l
	}
	done := watchdogTool{id: id}
	if idx >= 0 {
		done = pending[idx]
		pending = append(pending[:idx], pending[idx+1:]...)
	}
	if name != "" {
		done.name = name
	}
	l.pending = pending
	if len(pending) > 0 {
		return l
	}
	l.awaitingTurn = true
	l.turnTool = done
	return l
}

// isWatchdogSubagentToolName reports whether a tool name identifies a
// subagent task. Subagents run in child sessions whose activity is not
// streamed to the parent, so long silence while one is pending is expected.
func isWatchdogSubagentToolName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "task")
}

func isWatchdogPendingToolData(data string) bool {
	for _, line := range strings.Split(data, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		// OpenCode/ACP commonly starts tools directly in in_progress. Both
		// statuses are non-terminal and must remain watchdog-eligible.
		if line == "pending" || line == "status: pending" ||
			line == "in_progress" || line == "status: in_progress" {
			return true
		}
	}
	return false
}

func isWatchdogTerminalToolData(data string) bool {
	for _, line := range strings.Split(data, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "completed" || line == "status: completed" ||
			line == "failed" || line == "status: failed" {
			return true
		}
	}
	return false
}

func (w *sessionWatchdog) run() {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.session.done:
			return
		case <-ticker.C:
		}

		if snap, timeout, ok := w.toolStall(); ok {
			if w.failTool(snap, timeout) {
				return
			}
		}
	}
}

func (w *sessionWatchdog) toolStall() (watchdogSnapshot, time.Duration, bool) {
	status := w.session.Status()
	if status == SessionDone || status == SessionFailed {
		return watchdogSnapshot{}, 0, false
	}
	// Waiting on a human (pending permission, unanswered question, or help)
	// is exempt. Mark the boundary instead of refreshing activity so the
	// exempt interval is excluded without resetting idle time accrued before
	// the state was entered.
	if len(w.session.PendingControlRequests()) > 0 || w.session.HasPendingAskUserQuestion() || status == SessionWaitingHelp {
		w.mu.Lock()
		if w.exemptSince.IsZero() {
			w.exemptSince = time.Now()
		}
		w.mu.Unlock()
		return watchdogSnapshot{}, 0, false
	}

	w.mu.Lock()
	if !w.exemptSince.IsZero() {
		now := time.Now()
		shifted := w.lastActivityAt.Add(now.Sub(w.exemptSince))
		if shifted.After(now) {
			shifted = now
		}
		w.lastActivityAt = shifted
		w.exemptSince = time.Time{}
	}
	snap := watchdogSnapshot{
		phase:    w.lifecycle.phase(),
		tool:     w.lifecycle.displayTool(),
		subagent: w.lifecycle.anySubagentPending(),
		declared: w.lifecycle.maxPendingDeclaredTimeout(),
		seq:      w.seq,
	}
	lastActivityAt := w.lastActivityAt
	w.mu.Unlock()
	timeout := w.timeoutFor(snap)
	if timeout <= 0 {
		return watchdogSnapshot{}, 0, false
	}
	if stdoutAt := w.session.LastStdoutAt(); stdoutAt.After(lastActivityAt) {
		lastActivityAt = stdoutAt
	}
	idleFor := time.Since(lastActivityAt)
	return snap, timeout, idleFor >= timeout
}

func (w *sessionWatchdog) timeoutFor(snap watchdogSnapshot) time.Duration {
	switch snap.phase {
	case watchdogToolRunning:
		// Subagent tasks are opaque: the parent session hears nothing until
		// they finish, so a pending subagent earns the longer timeout.
		timeout := w.pendingToolIdleTimeout
		if snap.subagent && w.subagentToolIdleTimeout > 0 {
			timeout = w.subagentToolIdleTimeout
		}
		// A tool that declared its own execution timeout (e.g. a long test
		// command) may legitimately stay silent for that long; the provider
		// then gets the normal idle grace to report a terminal update.
		if timeout > 0 && snap.declared > 0 {
			if declared := snap.declared + w.pendingToolIdleTimeout; declared > timeout {
				timeout = declared
			}
		}
		return timeout
	case watchdogToolAwaitingTurnResult:
		return w.turnCompletionIdleTimeout
	default:
		return 0
	}
}

func (w *sessionWatchdog) failTool(snap watchdogSnapshot, timeout time.Duration) bool {
	// Observe and the poller race at the timeout boundary. Linearize the
	// decision under the watchdog lock: if a Result, tool update, or any
	// stdout arrived after the poll snapshot, that activity wins and the
	// watchdog keeps waiting. Bumping seq here also guarantees a single
	// fire: the only caller is run(), which returns as soon as this
	// reports true.
	w.mu.Lock()
	if w.seq != snap.seq {
		w.mu.Unlock()
		return false
	}
	lastActivityAt := w.lastActivityAt
	if stdoutAt := w.session.LastStdoutAt(); stdoutAt.After(lastActivityAt) {
		lastActivityAt = stdoutAt
	}
	idleFor := time.Since(lastActivityAt)
	if idleFor < timeout {
		w.mu.Unlock()
		return false
	}
	w.lifecycle = watchdogToolLifecycle{}
	w.seq++
	w.mu.Unlock()

	var reason string
	if snap.phase == watchdogToolAwaitingTurnResult {
		reason = fmt.Sprintf("provider watchdog stalled awaiting turn completion after tool %s for %s (idle %s)", snap.tool.displayName(), timeout, idleFor.Round(time.Millisecond))
	} else {
		reason = fmt.Sprintf("provider watchdog stalled with pending tool %s for %s (idle %s)", snap.tool.displayName(), timeout, idleFor.Round(time.Millisecond))
	}
	w.session.failFromWatchdog(reason)
	return true
}

func (p watchdogTool) displayName() string {
	switch {
	case p.name != "":
		return p.name
	case p.id != "":
		return p.id
	default:
		return "tool"
	}
}

func (s *Session) failFromWatchdog(reason string) {
	result := &llm.ResultMessage{
		Type:    "result",
		Subtype: "error",
		Result:  reason,
		IsError: true,
	}
	s.messageLog.Append(llm.SDKMessage{Type: "result", Result: result})

	s.mu.Lock()
	if s.status == SessionDone || s.status == SessionFailed {
		s.mu.Unlock()
		return
	}
	s.cost = result
	s.status = SessionFailed
	stdin := s.stdin
	s.stdin = nil
	pid := 0
	grace := s.resultShutdownGraceDuration()
	if s.process != nil && s.process.Process != nil {
		pid = s.process.Process.Pid
	}
	s.mu.Unlock()

	select {
	case s.statusCh <- "FAILED":
	default:
	}
	if stdin != nil {
		_ = stdin.Close()
	}
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		go s.killProcessGroupAfterGrace(pid, grace)
	}
}

func (s *Session) killProcessGroupAfterGrace(pid int, grace time.Duration) {
	if grace <= 0 {
		grace = resultShutdownGrace
	}
	select {
	case <-s.done:
		return
	case <-time.After(grace):
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
