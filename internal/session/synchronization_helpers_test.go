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
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func waitForSessionStatus(t testing.TB, sess ports.SessionHandle, want SessionStatus, timeout time.Duration) {
	t.Helper()
	waitForCondition(t, timeout, func() bool {
		return sess.Status() == want
	}, "session status %v", want)
}

func waitForAssistantText(t testing.TB, sess ports.SessionHandle, want string, timeout time.Duration) {
	t.Helper()
	waitForCondition(t, timeout, func() bool {
		return strings.Contains(sess.MessageLog().AssistantText(), want)
	}, "assistant text containing %q", want)
}

func waitForCondition(t testing.TB, timeout time.Duration, cond func() bool, format string, args ...interface{}) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if cond() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for "+format, args...)
		case <-ticker.C:
		}
	}
}

func runMockSession(t testing.TB, s *Session, messages []llm.SDKMessage, onMessage func(llm.SDKMessage)) {
	t.Helper()
	s.protocol = newScriptedProtocol(messages...)

	lines := make([]string, len(messages))
	for i := range lines {
		lines[i] = "MOCK_MSG"
	}
	runSessionWithStdoutLines(t, s, lines, onMessage)
}

func runSessionWithStdoutLines(t testing.TB, s *Session, lines []string, onMessage func(llm.SDKMessage)) {
	t.Helper()

	var stdout strings.Builder
	for _, line := range lines {
		stdout.WriteString(line)
		stdout.WriteByte('\n')
	}
	s.stdout = io.NopCloser(strings.NewReader(stdout.String()))
	s.startedAt = time.Now()
	s.ensureStreamDrainer()
	s.readMessages(onMessage)
}

type scriptedProtocol struct {
	mu        sync.Mutex
	messages  []llm.SDKMessage
	msgIndex  int
	sessionID string
}

func newScriptedProtocol(messages ...llm.SDKMessage) *scriptedProtocol {
	return &scriptedProtocol{messages: messages}
}

func (p *scriptedProtocol) SetStdin(io.Writer)              {}
func (p *scriptedProtocol) Handshake(context.Context) error { return nil }
func (p *scriptedProtocol) SendUserMessage(string) error    { return nil }
func (p *scriptedProtocol) RespondToControl(string, bool, json.RawMessage, string) error {
	return nil
}
func (p *scriptedProtocol) RespondToHook(string) error { return nil }
func (p *scriptedProtocol) RespondToAskUser(string, json.RawMessage, map[string]string, map[string]llm.AskUserAnnotation) error {
	return nil
}
func (p *scriptedProtocol) Interrupt() error       { return llm.ErrNotSupported }
func (p *scriptedProtocol) TranscriptPath() string { return "" }
func (p *scriptedProtocol) Close() error           { return nil }

func (p *scriptedProtocol) SessionID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessionID
}

func (p *scriptedProtocol) ParseLine([]byte) ([]llm.SDKMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.msgIndex >= len(p.messages) {
		return nil, nil
	}
	msg := p.messages[p.msgIndex]
	if msg.Init != nil {
		p.sessionID = msg.Init.SessionID
	} else if msg.Result != nil && msg.Result.SessionID != "" {
		p.sessionID = msg.Result.SessionID
	}
	p.msgIndex++
	return []llm.SDKMessage{msg}, nil
}
