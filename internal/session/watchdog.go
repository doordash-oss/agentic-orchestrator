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
	session                *Session
	pendingToolIdleTimeout time.Duration
	pollInterval           time.Duration

	mu             sync.Mutex
	pendingTool    watchdogPendingTool
	lastActivityAt time.Time

	startOnce sync.Once
	failOnce  sync.Once
}

type watchdogPendingTool struct {
	pending bool
	id      string
	name    string
}

func newSessionWatchdog(sess *Session, cfg *ports.SessionWatchdogConfig) *sessionWatchdog {
	if sess == nil || cfg == nil || cfg.PendingToolIdleTimeout <= 0 {
		return nil
	}
	interval := cfg.PollInterval
	if interval <= 0 || interval > cfg.PendingToolIdleTimeout {
		interval = cfg.PendingToolIdleTimeout / 4
		if interval <= 0 {
			interval = cfg.PendingToolIdleTimeout
		}
		if interval > time.Second {
			interval = time.Second
		}
	}
	return &sessionWatchdog{
		session:                sess,
		pendingToolIdleTimeout: cfg.PendingToolIdleTimeout,
		pollInterval:           interval,
		lastActivityAt:         time.Now(),
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
		w.pendingTool = watchdogPendingTool{}
	case msg.ToolProgress != nil:
		w.pendingTool = observeWatchdogToolProgress(w.pendingTool, *msg.ToolProgress)
	}
	w.mu.Unlock()
}

func observeWatchdogToolProgress(current watchdogPendingTool, progress llm.ToolProgressMessage) watchdogPendingTool {
	data := strings.TrimSpace(progress.Data)
	id := strings.TrimSpace(progress.ToolUseID)
	name := strings.TrimSpace(progress.ToolName)
	if isWatchdogPendingToolData(data) {
		return watchdogPendingTool{
			pending: true,
			id:      id,
			name:    name,
		}
	}
	if !current.pending {
		return watchdogPendingTool{}
	}
	if id != "" && current.id != "" && id != current.id {
		return current
	}
	return watchdogPendingTool{}
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

func (w *sessionWatchdog) run() {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.session.done:
			return
		case <-ticker.C:
		}

		if pending, idleFor, ok := w.pendingToolStall(); ok {
			w.failPendingTool(pending, idleFor)
			return
		}
	}
}

func (w *sessionWatchdog) pendingToolStall() (watchdogPendingTool, time.Duration, bool) {
	if len(w.session.PendingControlRequests()) > 0 || w.session.HasPendingAskUserQuestion() {
		return watchdogPendingTool{}, 0, false
	}
	status := w.session.Status()
	if status == SessionWaitingPermission || status == SessionWaitingHelp || status == SessionDone || status == SessionFailed {
		return watchdogPendingTool{}, 0, false
	}

	w.mu.Lock()
	pending := w.pendingTool
	lastActivityAt := w.lastActivityAt
	w.mu.Unlock()
	if !pending.pending {
		return watchdogPendingTool{}, 0, false
	}
	if stdoutAt := w.session.LastStdoutAt(); stdoutAt.After(lastActivityAt) {
		lastActivityAt = stdoutAt
	}
	idleFor := time.Since(lastActivityAt)
	return pending, idleFor, idleFor >= w.pendingToolIdleTimeout
}

func (w *sessionWatchdog) failPendingTool(pending watchdogPendingTool, idleFor time.Duration) {
	w.failOnce.Do(func() {
		reason := fmt.Sprintf("provider watchdog stalled with pending tool %s for %s (idle %s)", pending.displayName(), w.pendingToolIdleTimeout, idleFor.Round(time.Millisecond))
		w.session.failFromWatchdog(reason)
	})
}

func (p watchdogPendingTool) displayName() string {
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
