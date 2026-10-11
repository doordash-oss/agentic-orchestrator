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

package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/claudeconfig"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/askuser"
)

// interruptSeq gives each interrupt control_request a unique ID so the CLI
// can correlate responses.
var interruptSeq atomic.Uint64

// Protocol implements llm.Protocol for the Claude Code CLI JSON streaming protocol.
type Protocol struct {
	opts          llm.ProtocolOpts
	stdin         io.Writer
	mu            sync.Mutex
	sessionID     string // captured from init message
	contextWindow int    // provider-derived fallback, refined by result.modelUsage
}

// NewProtocol creates a new Claude protocol handler.
func NewProtocol(opts llm.ProtocolOpts) *Protocol {
	return &Protocol{
		opts:          opts,
		contextWindow: opts.ContextWindow,
	}
}

func (p *Protocol) SetStdin(w io.Writer) {
	p.mu.Lock()
	p.stdin = w
	p.mu.Unlock()
}

// Handshake sends the SDK initialize request and the initial user message.
func (p *Protocol) Handshake(ctx context.Context) error {
	if err := p.writeJSON(llm.NewInitializeRequest()); err != nil {
		return fmt.Errorf("sending initialize: %w", err)
	}
	if p.opts.InitialPrompt != "" {
		if err := p.SendUserMessage(p.opts.InitialPrompt); err != nil {
			return fmt.Errorf("sending initial prompt: %w", err)
		}
	}
	return nil
}

// ParseLine unmarshals a JSON line from stdout into SDKMessages.
func (p *Protocol) ParseLine(line []byte) ([]llm.SDKMessage, error) {
	var msg llm.SDKMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		// Wrap raw text in synthetic assistant message
		msg = llm.SDKMessage{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{
				Type: "assistant",
				Message: llm.ConversationMsg{
					Role: "assistant",
					Content: []llm.ContentBlock{
						{Type: "text", Text: string(line)},
					},
				},
			},
		}
	}

	// Capture session ID from init message
	if msg.Init != nil && msg.Init.SessionID != "" {
		p.mu.Lock()
		p.sessionID = msg.Init.SessionID
		p.mu.Unlock()
	}

	if msg.Result != nil {
		p.mu.Lock()
		for _, usage := range msg.Result.ModelUsage {
			if usage.ContextWindow > 0 {
				p.contextWindow = usage.ContextWindow
				break
			}
		}
		p.mu.Unlock()
	}

	if msg.Assistant != nil && msg.Assistant.Message.Usage != nil {
		p.mu.Lock()
		if p.contextWindow > 0 && msg.Assistant.Message.Usage.ContextWindow == 0 {
			msg.Assistant.Message.Usage.ContextWindow = p.contextWindow
		}
		p.mu.Unlock()
	}

	msg.OccurredAt = time.Now().UTC()
	msg.Origin = llm.EventOrigin{Kind: llm.EventOriginRoot}
	switch {
	case msg.TaskStarted != nil:
		msg.Origin = llm.EventOrigin{
			Kind:           llm.EventOriginTask,
			TaskID:         msg.TaskStarted.TaskID,
			ChildSessionID: msg.TaskStarted.SessionID,
		}
	case msg.TaskProgress != nil:
		msg.Origin = llm.EventOrigin{
			Kind:           llm.EventOriginTask,
			TaskID:         msg.TaskProgress.TaskID,
			ChildSessionID: msg.TaskProgress.SessionID,
		}
	case msg.TaskNotification != nil:
		msg.Origin = llm.EventOrigin{
			Kind:           llm.EventOriginTask,
			TaskID:         msg.TaskNotification.TaskID,
			ChildSessionID: msg.TaskNotification.SessionID,
		}
	case msg.ControlRequest != nil && msg.ControlRequest.Request.AgentID != "" && p.opts.Interactive:
		// A permission a sub-agent raises names the sub-agent. Only
		// human-driven chat routes it as the sub-agent's own request;
		// orchestrated phases keep treating it as the root agent's.
		agentID := msg.ControlRequest.Request.AgentID
		msg.Origin = llm.EventOrigin{
			Kind:           llm.EventOriginTask,
			TaskID:         agentID,
			ChildSessionID: agentID,
		}
	}
	if msg.ControlRequest != nil {
		msg.ControlRequest.Origin = msg.Origin
	}

	return []llm.SDKMessage{msg}, nil
}

// SendUserMessage sends a user message via JSON stdin.
func (p *Protocol) SendUserMessage(text string) error {
	return p.writeJSON(llm.NewUserInput(text))
}

// RespondToControl sends a control response (allow/deny) for a tool permission request.
func (p *Protocol) RespondToControl(requestID string, allow bool, originalInput json.RawMessage, reason string) error {
	if allow {
		return p.writeJSON(llm.NewAllowResponse(requestID, originalInput))
	}
	return p.writeJSON(llm.NewDenyResponse(requestID, reason))
}

// RespondToHook sends a hook continue response for PreToolUse callbacks.
func (p *Protocol) RespondToHook(requestID string) error {
	return p.writeJSON(llm.NewHookContinueResponse(requestID))
}

// RespondToAskUser sends a control response for an AskUserQuestion tool use.
// The CLI resolves each answer against the question's option labels; an
// answer matching no label selects nothing and the turn never receives a
// tool_result, so a selected answer is sent as its labels verbatim and a
// free-text answer is injected as an extra option before being selected.
func (p *Protocol) RespondToAskUser(requestID string, resolved askuser.Resolved) error {
	bundle, answers := presentAskUserAnswers(resolved)
	return p.writeJSON(llm.NewAskUserResponse(requestID, bundle, answers))
}

// presentAskUserAnswers returns the bundle to echo and the answers keyed by
// question text. A selected answer is its labels verbatim, joined with ", "
// for multi-select; a free-text answer is injected as an option so the CLI's
// label resolution always finds a selection. The resulting option list stays
// within Claude's required 2-4 cardinality: an optionless question receives
// one padding choice, while a full list reserves its final slot for the
// custom answer.
func presentAskUserAnswers(resolved askuser.Resolved) (askuser.Bundle, map[string]string) {
	bundle := askuser.Bundle{Questions: append([]askuser.Question(nil), resolved.Bundle.Questions...)}
	answers := make(map[string]string, len(resolved.Answers))
	for _, answer := range resolved.Answers {
		if answer.Selected {
			answers[answer.Question] = strings.Join(answer.Labels, ", ")
			continue
		}
		answers[answer.Question] = answer.Raw
		i := answer.Index - 1
		if i < 0 || i >= len(bundle.Questions) {
			continue
		}
		q := &bundle.Questions[i]
		opts := append([]askuser.Option(nil), q.Options...)
		if len(opts) == 0 {
			paddingLabel := "Other"
			if strings.TrimSpace(answer.Raw) == paddingLabel {
				paddingLabel = "Alternative answer"
			}
			opts = append(opts, askuser.Option{
				Label:       paddingLabel,
				Description: "Provide a different custom answer.",
			})
		}
		if len(opts) >= 4 {
			opts = opts[:3]
		}
		q.Options = append(opts, askuser.Option{
			Label:       answer.Raw,
			Description: "User-provided custom answer.",
		})
	}
	return bundle, answers
}

// Interrupt sends a control_request with subtype "interrupt" to cancel the
// current turn. The SDK protocol drops any pending tool work and emits a
// result message; the session stays alive for the next SendUserMessage.
func (p *Protocol) Interrupt() error {
	req := llm.ControlRequestMessage{
		Type:      "control_request",
		RequestID: fmt.Sprintf("agentic-interrupt-%d", interruptSeq.Add(1)),
		Request:   llm.ControlRequest{Subtype: "interrupt"},
	}
	return p.writeJSON(req)
}

// SessionID returns the session ID captured from the init message.
func (p *Protocol) SessionID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessionID
}

// TranscriptPath returns the path to the Claude CLI's transcript JSONL file.
func (p *Protocol) TranscriptPath() string {
	p.mu.Lock()
	sid := p.sessionID
	p.mu.Unlock()

	if sid == "" || p.opts.WorkDir == "" {
		return ""
	}

	configDir, err := claudeconfig.DefaultDir()
	if err != nil {
		return ""
	}
	return filepath.Join(claudeconfig.ProjectsDir(configDir, p.opts.WorkDir), sid+".jsonl")
}

func (p *Protocol) Close() error {
	return nil
}

func (p *Protocol) writeJSON(v interface{}) error {
	p.mu.Lock()
	w := p.stdin
	p.mu.Unlock()

	if w == nil {
		return fmt.Errorf("claude protocol: stdin not set")
	}

	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}
