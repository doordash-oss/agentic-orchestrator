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

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// MessageLog is a thread-safe log of SDK messages, replacing the ring buffer.
type MessageLog struct {
	messages []llm.SDKMessage
	mu       sync.RWMutex
}

// NewMessageLog creates an empty message log.
func NewMessageLog() *MessageLog {
	return &MessageLog{}
}

// Append adds a message to the log.
func (l *MessageLog) Append(msg llm.SDKMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, msg)
}

// UpdateLast replaces the last message in the log. Used for partial message streaming.
// If the log is empty, the message is appended instead.
func (l *MessageLog) UpdateLast(msg llm.SDKMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.messages) == 0 {
		l.messages = append(l.messages, msg)
		return
	}
	l.messages[len(l.messages)-1] = msg
}

// UpdateLastAssistantPartial coalesces an incoming assistant message into the
// most recent in-flight streaming partial.
func (l *MessageLog) UpdateLastAssistantPartial(msg llm.SDKMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.messages) - 1; i >= 0; i-- {
		existing := l.messages[i]
		if existing.Assistant != nil && existing.Subtype == "partial" {
			if !assistantPartialCanReplace(existing, msg) {
				break
			}
			l.messages[i] = msg
			return
		}
		if isPartialCoalesceBoundary(existing) {
			break
		}
	}
	l.messages = append(l.messages, msg)
}

func assistantPartialCanReplace(existing, incoming llm.SDKMessage) bool {
	if existing.Assistant == nil || incoming.Assistant == nil {
		return false
	}
	existingKinds := assistantContentKinds(existing.Assistant.Message.Content)
	incomingKinds := assistantContentKinds(incoming.Assistant.Message.Content)
	if len(existingKinds) == 0 || len(incomingKinds) == 0 {
		return true
	}
	if existingKinds["thinking"] != incomingKinds["thinking"] {
		return false
	}
	for kind := range existingKinds {
		if incomingKinds[kind] {
			return true
		}
	}
	return false
}

func assistantContentKinds(blocks []llm.ContentBlock) map[string]bool {
	kinds := make(map[string]bool, len(blocks))
	for _, block := range blocks {
		switch {
		case block.IsThinking():
			kinds["thinking"] = true
		case block.IsText():
			kinds["text"] = true
		case block.IsToolUse():
			kinds["tool_use"] = true
		case block.IsToolResult():
			kinds["tool_result"] = true
		}
	}
	return kinds
}

// isPartialCoalesceBoundary reports whether msg closes the streaming-partial
// window for UpdateLastAssistantPartial. Boundary events represent real
// state transitions (turn end, new user input, permission gate, etc.) that
// must not be skipped when coalescing partials.
func isPartialCoalesceBoundary(msg llm.SDKMessage) bool {
	return msg.Assistant != nil ||
		msg.Result != nil ||
		msg.User != nil ||
		msg.Init != nil ||
		msg.ControlRequest != nil ||
		msg.Compact != nil
}

// Messages returns a copy of all messages.
func (l *MessageLog) Messages() []llm.SDKMessage {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]llm.SDKMessage, len(l.messages))
	copy(out, l.messages)
	return out
}

// Len returns the number of messages in the log.
func (l *MessageLog) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.messages)
}

// Text renders a human-readable representation of the message log.
func (l *MessageLog) Text() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return renderMessages(l.messages)
}

// LastN returns the last n messages. If n > len, returns all.
func (l *MessageLog) LastN(n int) []llm.SDKMessage {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if n >= len(l.messages) {
		out := make([]llm.SDKMessage, len(l.messages))
		copy(out, l.messages)
		return out
	}
	out := make([]llm.SDKMessage, n)
	copy(out, l.messages[len(l.messages)-n:])
	return out
}

// LastResultMessage returns the last ResultMessage, or nil if none.
func (l *MessageLog) LastResultMessage() *llm.ResultMessage {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for i := len(l.messages) - 1; i >= 0; i-- {
		if l.messages[i].Result != nil {
			return l.messages[i].Result
		}
	}
	return nil
}

// LastErrorDetail extracts a human-readable crash/error reason by scanning
// messages in reverse. It checks the result message subtype and falls back
// to the last assistant text block (which often contains the CLI error).
func (l *MessageLog) LastErrorDetail() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	// Check result message first — provides structured error info.
	for i := len(l.messages) - 1; i >= 0; i-- {
		if r := l.messages[i].Result; r != nil {
			switch r.Subtype {
			case "success":
				return ""
			case "error":
				if r.Result != "" {
					return r.Result
				}
				return "API error"
			case "max_turns":
				return "max turns reached"
			case "max_budget":
				return "max budget reached"
			}
		}
	}

	// Fall back to last assistant text — the CLI emits synthetic error
	// messages (e.g. "Request too large") as assistant text blocks.
	for i := len(l.messages) - 1; i >= 0; i-- {
		if a := l.messages[i].Assistant; a != nil {
			for j := len(a.Message.Content) - 1; j >= 0; j-- {
				block := a.Message.Content[j]
				if block.IsText() && block.Text != "" {
					text := block.Text
					if len(text) > 200 {
						text = text[:200] + "…"
					}
					return text
				}
			}
		}
	}

	return ""
}

// AssistantText returns concatenated text from all assistant messages.
func (l *MessageLog) AssistantText() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var b strings.Builder
	for _, msg := range l.messages {
		if msg.Assistant == nil {
			continue
		}
		for _, block := range msg.Assistant.Message.Content {
			if block.IsText() {
				b.WriteString(block.Text)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

// ToolUseBlocks returns all tool use content blocks from assistant messages.
func (l *MessageLog) ToolUseBlocks() []llm.ContentBlock {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var blocks []llm.ContentBlock
	for _, msg := range l.messages {
		if msg.Assistant == nil {
			continue
		}
		for _, block := range msg.Assistant.Message.Content {
			if block.IsToolUse() {
				blocks = append(blocks, block)
			}
		}
	}
	return blocks
}

// RenderMessagesPublic produces a human-readable text view of messages.
// Exported for use by attach view and tests.
func RenderMessagesPublic(msgs []llm.SDKMessage) string {
	return renderMessages(msgs)
}

// renderMessages produces a human-readable text view of messages.
func renderMessages(msgs []llm.SDKMessage) string {
	var b strings.Builder
	lastAssistantText := ""
	for _, msg := range msgs {
		switch {
		case msg.Init != nil:
			fmt.Fprintf(&b, "[init] session=%s model=%s\n", msg.Init.SessionID, msg.Init.Model)
			lastAssistantText = ""
		case msg.Assistant != nil:
			for _, block := range msg.Assistant.Message.Content {
				switch {
				case block.IsText():
					if shouldSkipDuplicateAssistantText(lastAssistantText, block.Text) {
						continue
					}
					fmt.Fprintf(&b, "[assistant] %s\n", block.Text)
					lastAssistantText = strings.TrimSpace(block.Text)
				case block.IsToolUse():
					fmt.Fprintf(&b, "[tool_use] %s\n", block.Name)
					lastAssistantText = ""
				case block.IsThinking():
					thinking := block.Thinking
					if len(thinking) > 200 {
						thinking = thinking[:200] + "..."
					}
					fmt.Fprintf(&b, "[thinking] %s\n", thinking)
					lastAssistantText = ""
				}
			}
		case msg.User != nil:
			lastAssistantText = ""
			for _, block := range msg.User.Message.Content {
				if block.IsText() {
					fmt.Fprintf(&b, "[user] %s\n", block.Text)
				} else if block.IsToolResult() {
					fmt.Fprintf(&b, "[tool_result] tool=%s\n", block.ToolUseID)
				}
			}
		case msg.Result != nil:
			lastAssistantText = ""
			fmt.Fprintf(&b, "[result] %s cost=$%.4f\n", msg.Result.Subtype, msg.Result.TotalCostUSD)
		case msg.ControlRequest != nil:
			lastAssistantText = ""
			if msg.ControlRequest.Request.Subtype == "can_use_tool" && msg.ControlRequest.Request.ToolName != "" {
				fmt.Fprintf(&b, "[tool_use] %s\n", msg.ControlRequest.Request.ToolName)
			} else {
				fmt.Fprintf(&b, "[permission] %s: %s\n", msg.ControlRequest.Request.Subtype, msg.ControlRequest.Request.ToolName)
			}
		case msg.Status != nil:
			lastAssistantText = ""
			fmt.Fprintf(&b, "[status] %s\n", msg.Status.Message)
		case msg.ToolProgress != nil:
			lastAssistantText = ""
			fmt.Fprintf(&b, "[progress] %s: %s\n", msg.ToolProgress.ToolName, msg.ToolProgress.Data)
		case msg.RateLimit != nil:
			lastAssistantText = ""
			fmt.Fprintf(&b, "[rate_limit] %s\n", msg.RateLimit.Message)
		case msg.Compact != nil:
			lastAssistantText = ""
			b.WriteString("[compact] context compacted\n")
		}
	}
	return b.String()
}

func shouldSkipDuplicateAssistantText(previous, current string) bool {
	previous = strings.TrimSpace(previous)
	current = strings.TrimSpace(current)
	return previous != "" && previous == current
}
