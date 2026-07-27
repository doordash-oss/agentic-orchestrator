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
	subagentHeartbeatInterval time.Duration

	mu                    sync.Mutex
	tools                 map[string]watchdogTool
	awaitingTurn          watchdogTool
	liveSubagents         map[string]struct{}
	subagentWaitSince     time.Time
	nextSubagentHeartbeat time.Time
	nextToolGeneration    uint64
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
	phase       watchdogToolPhase
	id          string
	name        string
	generation  uint64
	activeCount int
}

const anonymousWatchdogToolKey = "\x00anonymous-tool"

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
		subagentHeartbeatInterval: cfg.SubagentHeartbeatInterval,
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
		w.tools = nil
		w.awaitingTurn = watchdogTool{}
		w.liveSubagents = nil
		w.subagentWaitSince = time.Time{}
		w.nextSubagentHeartbeat = time.Time{}
		w.pendingControlRequest = nil
	case msg.ControlRequest != nil:
		if watchdogShouldParkForControlRequest(msg.ControlRequest) {
			if w.pendingControlRequest == nil {
				w.pendingControlRequest = make(map[string]struct{})
			}
			w.pendingControlRequest[msg.ControlRequest.RequestID] = struct{}{}
		}
	case msg.TaskStarted != nil || msg.TaskProgress != nil || msg.TaskNotification != nil:
		w.observeTaskLifecycleLocked(msg, now)
	case msg.ToolProgress != nil:
		w.observeToolProgressLocked(*msg.ToolProgress)
	}
	if len(w.liveSubagents) > 0 && w.subagentHeartbeatInterval > 0 {
		w.nextSubagentHeartbeat = now.Add(w.subagentHeartbeatInterval)
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

func (w *sessionWatchdog) observeToolProgressLocked(progress llm.ToolProgressMessage) {
	key := strings.TrimSpace(progress.ToolUseID)
	if key == "" {
		key = anonymousWatchdogToolKey
	}
	current := w.tools[key]
	next := observeWatchdogToolProgress(current, progress)
	switch next.phase {
	case watchdogToolRunning:
		if w.tools == nil {
			w.tools = make(map[string]watchdogTool)
		}
		w.nextToolGeneration++
		next.generation = w.nextToolGeneration
		w.tools[key] = next
		w.awaitingTurn = watchdogTool{}
	case watchdogToolAwaitingTurnResult:
		if current.phase == watchdogToolRunning {
			delete(w.tools, key)
		} else if len(w.tools) > 0 {
			return
		}
		if len(w.tools) == 0 {
			w.nextToolGeneration++
			next.generation = w.nextToolGeneration
			w.awaitingTurn = next
		}
	}
}

func (w *sessionWatchdog) observeTaskLifecycleLocked(msg llm.SDKMessage, now time.Time) {
	key := ""
	live := false
	switch {
	case msg.TaskStarted != nil:
		key, live = backgroundTaskKey(msg.TaskStarted.TaskID, msg.TaskStarted.ToolUseID), true
	case msg.TaskProgress != nil:
		key, live = backgroundTaskKey(msg.TaskProgress.TaskID, msg.TaskProgress.ToolUseID), true
	case msg.TaskNotification != nil:
		key = backgroundTaskKey(msg.TaskNotification.TaskID, msg.TaskNotification.ToolUseID)
	default:
		return
	}
	if key == "" {
		return
	}
	if live {
		if w.liveSubagents == nil {
			w.liveSubagents = make(map[string]struct{})
		}
		if len(w.liveSubagents) == 0 {
			w.subagentWaitSince = now
		}
		w.liveSubagents[key] = struct{}{}
		return
	}
	delete(w.liveSubagents, key)
	if len(w.liveSubagents) == 0 {
		w.liveSubagents = nil
		w.subagentWaitSince = time.Time{}
		w.nextSubagentHeartbeat = time.Time{}
		w.lastActivityAt = now
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

		if status, ok := w.subagentHeartbeatStatus(time.Now()); ok {
			_ = w.session.appendLocalStatus(status)
		}
		if tool, timeout, ok := w.toolStall(); ok {
			if w.failTool(tool, timeout) {
				return
			}
		}
	}
}

func (w *sessionWatchdog) subagentHeartbeatStatus(now time.Time) (string, bool) {
	w.mu.Lock()
	if len(w.liveSubagents) == 0 ||
		w.subagentHeartbeatInterval <= 0 ||
		w.subagentWaitSince.IsZero() ||
		w.nextSubagentHeartbeat.IsZero() ||
		now.Before(w.nextSubagentHeartbeat) {
		w.mu.Unlock()
		return "", false
	}
	count := len(w.liveSubagents)
	waitSince := w.subagentWaitSince
	w.nextSubagentHeartbeat = now.Add(w.subagentHeartbeatInterval)
	w.mu.Unlock()

	minutes := int(now.Sub(waitSince) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	noun := "subagents"
	if count == 1 {
		noun = "subagent"
	}
	return fmt.Sprintf("Waiting for %d %s (%dm)", count, noun, minutes), true
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
	hasPendingControlRequest := len(w.pendingControlRequest) > 0
	hasLiveSubagents := len(w.liveSubagents) > 0
	tool := w.currentToolLocked()
	lastActivityAt := w.lastActivityAt
	w.mu.Unlock()
	if hasPendingControlRequest {
		w.refreshActivity()
		return watchdogTool{}, 0, false
	}
	if hasLiveSubagents {
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

func (w *sessionWatchdog) currentToolLocked() watchdogTool {
	if len(w.tools) == 0 {
		return w.awaitingTurn
	}
	key := ""
	for candidate := range w.tools {
		if key == "" || candidate < key {
			key = candidate
		}
	}
	tool := w.tools[key]
	tool.activeCount = len(w.tools)
	return tool
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
	// Clearing the selected state here also guarantees a single fire: the only
	// caller is run(), which returns as soon as this reports true.
	w.mu.Lock()
	if len(w.liveSubagents) > 0 || w.currentToolLocked() != tool {
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
	w.tools = nil
	w.awaitingTurn = watchdogTool{}
	w.mu.Unlock()

	var reason string
	if tool.phase == watchdogToolAwaitingTurnResult {
		reason = fmt.Sprintf("provider watchdog stalled awaiting turn completion after tool %s for %s (idle %s)", tool.displayName(), timeout, idleFor.Round(time.Millisecond))
	} else if tool.activeCount > 1 {
		reason = fmt.Sprintf("provider watchdog stalled with %d pending tools (including %s) for %s (idle %s)", tool.activeCount, tool.displayName(), timeout, idleFor.Round(time.Millisecond))
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
