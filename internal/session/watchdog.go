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
	turnCompletionIdleTimeout time.Duration
	pollInterval              time.Duration

	mu                    sync.Mutex
	tool                  watchdogTool
	pendingControlRequest map[string]struct{}
	lastActivityAt        time.Time

	startOnce sync.Once
}

type watchdogToolPhase uint8

const (
	watchdogToolInactive watchdogToolPhase = iota
	watchdogToolRunning
	watchdogToolAwaitingTurnResult
)

type watchdogTool struct {
	phase watchdogToolPhase
	id    string
	name  string
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
		w.tool = watchdogTool{}
		w.pendingControlRequest = nil
	case msg.ControlRequest != nil:
		if watchdogShouldParkForControlRequest(msg.ControlRequest) {
			if w.pendingControlRequest == nil {
				w.pendingControlRequest = make(map[string]struct{})
			}
			w.pendingControlRequest[msg.ControlRequest.RequestID] = struct{}{}
		}
	case msg.ToolProgress != nil:
		w.tool = observeWatchdogToolProgress(w.tool, *msg.ToolProgress)
	}
	w.mu.Unlock()
}

func watchdogShouldParkForControlRequest(req *llm.ControlRequestMessage) bool {
	if req == nil {
		return false
	}
	return req.Request.Subtype != "hook_callback"
}

func (w *sessionWatchdog) ResolveControlRequest(requestID string) {
	if w == nil || requestID == "" {
		return
	}
	w.mu.Lock()
	delete(w.pendingControlRequest, requestID)
	if len(w.pendingControlRequest) == 0 {
		w.pendingControlRequest = nil
	}
	w.lastActivityAt = time.Now()
	w.mu.Unlock()
}

func (w *sessionWatchdog) ClearControlRequests() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.pendingControlRequest = nil
	w.lastActivityAt = time.Now()
	w.mu.Unlock()
}

func observeWatchdogToolProgress(current watchdogTool, progress llm.ToolProgressMessage) watchdogTool {
	data := strings.TrimSpace(progress.Data)
	id := strings.TrimSpace(progress.ToolUseID)
	name := strings.TrimSpace(progress.ToolName)
	if isWatchdogPendingToolData(data) {
		return watchdogTool{
			phase: watchdogToolRunning,
			id:    id,
			name:  name,
		}
	}
	if !isWatchdogTerminalToolData(data) {
		return current
	}
	if current.phase == watchdogToolRunning && id != "" && current.id != "" && id != current.id {
		return current
	}
	if id == "" {
		id = current.id
	}
	if name == "" {
		name = current.name
	}
	return watchdogTool{
		phase: watchdogToolAwaitingTurnResult,
		id:    id,
		name:  name,
	}
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

		if tool, timeout, ok := w.toolStall(); ok {
			if w.failTool(tool, timeout) {
				return
			}
		}
	}
}

func (w *sessionWatchdog) toolStall() (watchdogTool, time.Duration, bool) {
	if len(w.session.PendingControlRequests()) > 0 || w.session.HasPendingAskUserQuestion() {
		w.refreshActivity()
		return watchdogTool{}, 0, false
	}
	status := w.session.Status()
	if status == SessionWaitingHelp {
		w.refreshActivity()
		return watchdogTool{}, 0, false
	}
	if status == SessionDone || status == SessionFailed {
		return watchdogTool{}, 0, false
	}

	w.mu.Lock()
	tool := w.tool
	hasPendingControlRequest := len(w.pendingControlRequest) > 0
	lastActivityAt := w.lastActivityAt
	w.mu.Unlock()
	if hasPendingControlRequest {
		w.refreshActivity()
		return watchdogTool{}, 0, false
	}
	timeout := w.timeoutFor(tool.phase)
	if timeout <= 0 {
		return watchdogTool{}, 0, false
	}
	if stdoutAt := w.session.LastStdoutAt(); stdoutAt.After(lastActivityAt) {
		lastActivityAt = stdoutAt
	}
	idleFor := time.Since(lastActivityAt)
	return tool, timeout, idleFor >= timeout
}

func (w *sessionWatchdog) refreshActivity() {
	w.mu.Lock()
	w.lastActivityAt = time.Now()
	w.mu.Unlock()
}

func (w *sessionWatchdog) timeoutFor(phase watchdogToolPhase) time.Duration {
	switch phase {
	case watchdogToolRunning:
		return w.pendingToolIdleTimeout
	case watchdogToolAwaitingTurnResult:
		return w.turnCompletionIdleTimeout
	default:
		return 0
	}
}

func (w *sessionWatchdog) failTool(tool watchdogTool, timeout time.Duration) bool {
	// Observe and the poller race at the timeout boundary. Linearize the
	// decision under the watchdog lock: if a Result or any stdout arrived after
	// the poll snapshot, that activity wins and the watchdog keeps waiting.
	// Clearing w.tool here also guarantees a single fire: the only caller is
	// run(), which returns as soon as this reports true.
	w.mu.Lock()
	if w.tool != tool {
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
	w.tool = watchdogTool{}
	w.mu.Unlock()

	var reason string
	if tool.phase == watchdogToolAwaitingTurnResult {
		reason = fmt.Sprintf("provider watchdog stalled awaiting turn completion after tool %s for %s (idle %s)", tool.displayName(), timeout, idleFor.Round(time.Millisecond))
	} else {
		reason = fmt.Sprintf("provider watchdog stalled with pending tool %s for %s (idle %s)", tool.displayName(), timeout, idleFor.Round(time.Millisecond))
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
