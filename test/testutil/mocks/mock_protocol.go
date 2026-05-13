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

package mocks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// MockProtocol implements llm.Protocol by replaying a configurable message
// sequence. It does not spawn a subprocess — messages are delivered in-process.
type MockProtocol struct {
	// Messages is the sequence of llm.SDKMessage values to replay when the
	// session reads from stdout. The protocol delivers them in order.
	Messages []llm.SDKMessage

	// OnSendMessage is called when the session sends a user message.
	OnSendMessage func(text string)

	// InterruptErr is returned from Interrupt(). Defaults to nil.
	InterruptErr error

	mu             sync.Mutex
	stdin          io.Writer
	sessionID      string
	initialized    bool
	msgIndex       int
	closeCalled    bool
	interruptCalls int
}

// NewMockProtocol creates a MockProtocol with the given message sequence.
// A standard sequence is: SystemInit → AssistantMessage(s) → ResultSuccess.
func NewMockProtocol(messages ...llm.SDKMessage) *MockProtocol {
	return &MockProtocol{Messages: messages}
}

func (p *MockProtocol) SetStdin(w io.Writer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stdin = w
}

func (p *MockProtocol) Handshake(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initialized = true

	// Write a line per message to stdin so the subprocess (cat) echoes them
	// to stdout, causing readMessages to call ParseLine for each line.
	if p.stdin != nil {
		for i := range p.Messages {
			line := fmt.Sprintf("MOCK_MSG:%d\n", i)
			if _, err := p.stdin.Write([]byte(line)); err != nil {
				return fmt.Errorf("writing mock message %d: %w", i, err)
			}
		}
		// Close stdin to signal EOF so the subprocess exits cleanly.
		if closer, ok := p.stdin.(io.Closer); ok {
			closer.Close()
		}
	}
	return nil
}

func (p *MockProtocol) ParseLine(line []byte) ([]llm.SDKMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.msgIndex >= len(p.Messages) {
		return nil, nil
	}

	msg := p.Messages[p.msgIndex]
	if msg.Init != nil {
		p.sessionID = msg.Init.SessionID
	} else if msg.Result != nil && msg.Result.SessionID != "" {
		p.sessionID = msg.Result.SessionID
	}
	p.msgIndex++
	return []llm.SDKMessage{msg}, nil
}

func (p *MockProtocol) SendUserMessage(text string) error {
	if p.OnSendMessage != nil {
		p.OnSendMessage(text)
	}
	return nil
}

func (p *MockProtocol) RespondToControl(requestID string, allow bool, originalInput json.RawMessage, reason string) error {
	return nil
}

func (p *MockProtocol) RespondToHook(requestID string) error {
	return nil
}

func (p *MockProtocol) RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]llm.AskUserAnnotation) error {
	return nil
}

// Interrupt records the call. By default returns nil (successful interrupt);
// tests that want to exercise the SIGINT fallback can set p.InterruptErr to
// llm.ErrNotSupported.
func (p *MockProtocol) Interrupt() error {
	p.mu.Lock()
	p.interruptCalls++
	err := p.InterruptErr
	p.mu.Unlock()
	return err
}

// InterruptCalls returns how many times Interrupt was called.
func (p *MockProtocol) InterruptCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.interruptCalls
}

// SessionID returns the most recent session ID observed in replayed messages.
func (p *MockProtocol) SessionID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessionID
}

// TranscriptPath returns "" — mock protocol has no transcript.
func (p *MockProtocol) TranscriptPath() string { return "" }

func (p *MockProtocol) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeCalled = true
	return nil
}

// Initialized returns whether Handshake was called.
func (p *MockProtocol) Initialized() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.initialized
}

// MessageIndex returns how many messages have been delivered via ParseLine.
func (p *MockProtocol) MessageIndex() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.msgIndex
}

// --- Helper constructors for building SDKMessage sequences ---

// InitMessage returns a SystemInit SDKMessage with default values.
func InitMessage() llm.SDKMessage {
	return llm.SDKMessage{
		Type:    "system",
		Subtype: "init",
		Init: &llm.SystemInitMessage{
			SessionID: "mock-session-1",
			Model:     "mock-model",
		},
	}
}

// AssistantTextMessage returns an assistant SDKMessage with the given text.
func AssistantTextMessage(text string) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "assistant",
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Role: "assistant",
				Content: []llm.ContentBlock{
					{Type: "text", Text: text},
				},
			},
		},
	}
}

// SuccessResult returns a result/success SDKMessage.
func SuccessResult() llm.SDKMessage {
	return llm.SDKMessage{
		Type: "result",
		Result: &llm.ResultMessage{
			Type:    "result",
			Subtype: "success",
			Result:  "success",
		},
	}
}

// ErrorResult returns a result/error SDKMessage.
func ErrorResult(errMsg string) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "result",
		Result: &llm.ResultMessage{
			Type:    "result",
			Subtype: "error",
			Result:  errMsg,
			IsError: true,
		},
	}
}

// ControlRequestMsg returns a control_request SDKMessage for tool approval.
func ControlRequestMsg(requestID, toolName string) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "control_request",
		ControlRequest: &llm.ControlRequestMessage{
			Type:      "control_request",
			RequestID: requestID,
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: toolName,
			},
		},
	}
}

// StandardSequence returns init → assistant text → success result.
func StandardSequence(assistantText string) []llm.SDKMessage {
	return []llm.SDKMessage{
		InitMessage(),
		AssistantTextMessage(assistantText),
		SuccessResult(),
	}
}
