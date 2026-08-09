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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
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
// tool_result, so answers are aligned to labels first and free-text answers
// are injected as an extra option before being selected.
func (p *Protocol) RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]llm.AskUserAnnotation) error {
	questions, answers = alignAskUserAnswers(questions, answers)
	return p.writeJSON(llm.NewAskUserResponse(requestID, questions, answers, annotations))
}

// alignAskUserAnswers rewrites each answer to the exact option label it
// selects (recommended-suffix and display-truncation tolerant) and injects a
// free-text answer that matches no label as an option, so the CLI's label
// resolution always finds a selection. The resulting option list stays within
// Claude's required 2-4 cardinality: an optionless question receives one
// padding choice, while a full list reserves its final slot for the custom
// answer.
func alignAskUserAnswers(questions json.RawMessage, answers map[string]string) (json.RawMessage, map[string]string) {
	if len(answers) == 0 {
		return questions, answers
	}
	var root any
	if err := json.Unmarshal(questions, &root); err != nil {
		return questions, answers
	}
	list := askUserQuestionList(root)
	if len(list) == 0 {
		return questions, answers
	}

	aligned := make(map[string]string, len(answers))
	for k, v := range answers {
		aligned[k] = v
	}
	changed := false
	for _, item := range list {
		q, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text, _ := q["question"].(string)
		answer, ok := answers[text]
		if !ok || strings.TrimSpace(answer) == "" {
			continue
		}
		opts, _ := q["options"].([]any)
		if label, ok := llm.MatchAskUserOptionLabel(askUserOptionLabels(opts), answer); ok {
			if label != answer {
				aligned[text] = label
				changed = true
			}
			continue
		}
		if len(opts) == 0 {
			paddingLabel := "Other"
			if _, matchesAnswer := llm.MatchAskUserOptionLabel([]string{paddingLabel}, answer); matchesAnswer {
				paddingLabel = "Alternative answer"
			}
			opts = append(opts, map[string]any{
				"label":       paddingLabel,
				"description": "Provide a different custom answer.",
			})
		}
		if len(opts) >= 4 {
			opts = opts[:3]
		}
		q["options"] = append(opts, map[string]any{
			"label":       answer,
			"description": "User-provided custom answer.",
		})
		changed = true
	}
	if !changed {
		return questions, answers
	}
	out, err := json.Marshal(root)
	if err != nil {
		return questions, answers
	}
	return out, aligned
}

// askUserQuestionList returns the questions array from either the tool input
// envelope ({"questions":[...]}) or a bare array.
func askUserQuestionList(root any) []any {
	switch v := root.(type) {
	case map[string]any:
		list, _ := v["questions"].([]any)
		return list
	case []any:
		return v
	default:
		return nil
	}
}

func askUserOptionLabels(opts []any) []string {
	labels := make([]string, 0, len(opts))
	for _, opt := range opts {
		if m, ok := opt.(map[string]any); ok {
			if label, ok := m["label"].(string); ok {
				labels = append(labels, label)
			}
		}
	}
	return labels
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

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects",
		claudeProjectsDirName(p.opts.WorkDir),
		sid+".jsonl")
}

// claudeProjectsDirName encodes a working directory path into the directory
// name used by the Claude CLI under ~/.claude/projects/.
func claudeProjectsDirName(workDir string) string {
	return regexp.MustCompile(`[/.]`).ReplaceAllString(workDir, "-")
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
