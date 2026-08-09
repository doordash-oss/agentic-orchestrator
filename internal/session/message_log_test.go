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
	"strings"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestMessageLog_AppendAndMessages(t *testing.T) {
	log := NewMessageLog()
	if log.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", log.Len())
	}

	msg1 := llm.SDKMessage{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{SessionID: "s1", Model: "opus"}}
	msg2 := llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
		Role:    "assistant",
		Content: []llm.ContentBlock{{Type: "text", Text: "Hello"}},
	}}}
	log.Append(msg1)
	log.Append(msg2)

	if log.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", log.Len())
	}

	msgs := log.Messages()
	if len(msgs) != 2 {
		t.Fatalf("Messages() len = %d, want 2", len(msgs))
	}
	if msgs[0].Type != "system" {
		t.Errorf("msgs[0].Type = %q, want system", msgs[0].Type)
	}
	if msgs[1].Type != "assistant" {
		t.Errorf("msgs[1].Type = %q, want assistant", msgs[1].Type)
	}
}

func TestMessageLog_Messages_ReturnsCopy(t *testing.T) {
	log := NewMessageLog()
	log.Append(llm.SDKMessage{Type: "status"})
	msgs := log.Messages()
	msgs[0].Type = "modified"
	// Original should be unaffected.
	if log.Messages()[0].Type != "status" {
		t.Error("Messages() did not return a copy")
	}
}

func TestMessageLog_UpdateLast(t *testing.T) {
	log := NewMessageLog()

	// UpdateLast on empty log appends.
	log.UpdateLast(llm.SDKMessage{Type: "assistant"})
	if log.Len() != 1 {
		t.Fatalf("Len() = %d after UpdateLast on empty, want 1", log.Len())
	}

	// UpdateLast replaces.
	log.UpdateLast(llm.SDKMessage{Type: "assistant", Subtype: "updated"})
	if log.Len() != 1 {
		t.Fatalf("Len() = %d after UpdateLast, want 1", log.Len())
	}
	if log.Messages()[0].Subtype != "updated" {
		t.Errorf("Subtype = %q, want updated", log.Messages()[0].Subtype)
	}
}

func TestMessageLog_LastN(t *testing.T) {
	log := NewMessageLog()
	for i := 0; i < 5; i++ {
		log.Append(llm.SDKMessage{Type: "status", Subtype: string(rune('A' + i))})
	}

	last3 := log.LastN(3)
	if len(last3) != 3 {
		t.Fatalf("LastN(3) len = %d, want 3", len(last3))
	}
	if last3[0].Subtype != "C" {
		t.Errorf("last3[0].Subtype = %q, want C", last3[0].Subtype)
	}

	// Requesting more than available returns all.
	all := log.LastN(100)
	if len(all) != 5 {
		t.Fatalf("LastN(100) len = %d, want 5", len(all))
	}
}

func TestMessageLog_LastResultMessage(t *testing.T) {
	log := NewMessageLog()
	if log.LastResultMessage() != nil {
		t.Error("LastResultMessage() on empty should be nil")
	}

	log.Append(llm.SDKMessage{Type: "assistant"})
	log.Append(llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{Subtype: "success", TotalCostUSD: 0.01}})
	log.Append(llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{Subtype: "error", TotalCostUSD: 0.02}})

	r := log.LastResultMessage()
	if r == nil {
		t.Fatal("LastResultMessage() is nil")
	}
	if r.Subtype != "error" {
		t.Errorf("subtype = %q, want error", r.Subtype)
	}
	if r.TotalCostUSD != 0.02 {
		t.Errorf("cost = %f, want 0.02", r.TotalCostUSD)
	}
}

func TestMessageLog_LastErrorDetail(t *testing.T) {
	t.Run("empty log", func(t *testing.T) {
		log := NewMessageLog()
		if got := log.LastErrorDetail(); got != "" {
			t.Errorf("LastErrorDetail() on empty = %q, want empty", got)
		}
	})

	t.Run("result error with text", func(t *testing.T) {
		log := NewMessageLog()
		log.Append(llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{Subtype: "error", Result: "context too large"}})
		if got := log.LastErrorDetail(); got != "context too large" {
			t.Errorf("LastErrorDetail() = %q, want %q", got, "context too large")
		}
	})

	t.Run("result error without text", func(t *testing.T) {
		log := NewMessageLog()
		log.Append(llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{Subtype: "error"}})
		if got := log.LastErrorDetail(); got != "API error" {
			t.Errorf("LastErrorDetail() = %q, want %q", got, "API error")
		}
	})

	t.Run("result max_turns", func(t *testing.T) {
		log := NewMessageLog()
		log.Append(llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{Subtype: "max_turns"}})
		if got := log.LastErrorDetail(); got != "max turns reached" {
			t.Errorf("LastErrorDetail() = %q, want %q", got, "max turns reached")
		}
	})

	t.Run("no result falls back to last assistant text", func(t *testing.T) {
		log := NewMessageLog()
		log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "normal reply"}}},
		}})
		log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "Request too large (max 20MB). Try with a smaller file."}}},
		}})
		got := log.LastErrorDetail()
		if got != "Request too large (max 20MB). Try with a smaller file." {
			t.Errorf("LastErrorDetail() = %q, want request too large message", got)
		}
	})

	t.Run("long text truncated", func(t *testing.T) {
		log := NewMessageLog()
		long := strings.Repeat("x", 300)
		log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: long}}},
		}})
		got := log.LastErrorDetail()
		if len(got) > 210 {
			t.Errorf("LastErrorDetail() len = %d, should be truncated", len(got))
		}
		if !strings.HasSuffix(got, "…") {
			t.Error("truncated text should end with ellipsis")
		}
	})

	t.Run("success result returns empty", func(t *testing.T) {
		log := NewMessageLog()
		log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "all good"}}},
		}})
		log.Append(llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{Subtype: "success"}})
		if got := log.LastErrorDetail(); got != "" {
			t.Errorf("LastErrorDetail() on success = %q, want empty", got)
		}
	})
}

func TestMessageLog_AssistantText(t *testing.T) {
	log := NewMessageLog()
	log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
		Message: llm.ConversationMsg{
			Content: []llm.ContentBlock{
				{Type: "text", Text: "Hello"},
				{Type: "tool_use", Name: "Bash"},
				{Type: "text", Text: "World"},
			},
		},
	}})
	log.Append(llm.SDKMessage{Type: "user"})
	log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
		Message: llm.ConversationMsg{
			Content: []llm.ContentBlock{
				{Type: "text", Text: "Done"},
			},
		},
	}})

	text := log.AssistantText()
	if !strings.Contains(text, "Hello") || !strings.Contains(text, "World") || !strings.Contains(text, "Done") {
		t.Errorf("AssistantText() = %q, want to contain Hello, World, Done", text)
	}
	if strings.Contains(text, "Bash") {
		t.Error("AssistantText() should not contain tool use names")
	}
}

func TestMessageLog_ToolUseBlocks(t *testing.T) {
	log := NewMessageLog()
	log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
		Message: llm.ConversationMsg{
			Content: []llm.ContentBlock{
				{Type: "text", Text: "Let me check"},
				{Type: "tool_use", ID: "tu_1", Name: "Bash"},
				{Type: "tool_use", ID: "tu_2", Name: "Read"},
			},
		},
	}})
	log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
		Message: llm.ConversationMsg{
			Content: []llm.ContentBlock{
				{Type: "tool_use", ID: "tu_3", Name: "Write"},
			},
		},
	}})

	blocks := log.ToolUseBlocks()
	if len(blocks) != 3 {
		t.Fatalf("ToolUseBlocks() len = %d, want 3", len(blocks))
	}
	names := []string{blocks[0].Name, blocks[1].Name, blocks[2].Name}
	if names[0] != "Bash" || names[1] != "Read" || names[2] != "Write" {
		t.Errorf("tool names = %v, want [Bash Read Write]", names)
	}
}

func TestMessageLog_Text(t *testing.T) {
	log := NewMessageLog()
	log.Append(llm.SDKMessage{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{SessionID: "s1", Model: "opus"}})
	log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
		Message: llm.ConversationMsg{
			Content: []llm.ContentBlock{{Type: "text", Text: "Hello"}},
		},
	}})
	log.Append(llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{Subtype: "success", TotalCostUSD: 0.05}})

	text := log.Text()
	if !strings.Contains(text, "[init]") {
		t.Error("Text() missing [init]")
	}
	if !strings.Contains(text, "[assistant] Hello") {
		t.Error("Text() missing [assistant] Hello")
	}
	if !strings.Contains(text, "[result] success") {
		t.Error("Text() missing [result]")
	}
	if !strings.Contains(text, "$0.0500") {
		t.Error("Text() missing cost")
	}
}

func TestMessageLog_Text_ToolProgress(t *testing.T) {
	log := NewMessageLog()
	log.Append(llm.SDKMessage{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{
		ToolName: "Bash",
		Data:     "PASS ok",
	}})
	text := log.Text()
	if !strings.Contains(text, "[progress] Bash: PASS ok") {
		t.Errorf("Text() = %q, missing tool progress", text)
	}
}

func TestMessageLog_Text_ControlRequestCanUseToolRendersAsToolUse(t *testing.T) {
	log := NewMessageLog()
	log.Append(llm.SDKMessage{
		Type: "control_request",
		ControlRequest: &llm.ControlRequestMessage{
			RequestID: "req-1",
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "AskUserQuestion",
			},
		},
	})

	text := log.Text()
	if !strings.Contains(text, "[tool_use] AskUserQuestion") {
		t.Errorf("Text() = %q, missing tool_use rendering", text)
	}
	if strings.Contains(text, "[permission]") {
		t.Errorf("Text() = %q, should not render can_use_tool as permission", text)
	}
}

func TestMessageLog_Text_DedupesConsecutiveAssistantQuestion(t *testing.T) {
	log := NewMessageLog()
	question := "What exact version should Agentic be bumped to?"
	log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
		Message: llm.ConversationMsg{
			Content: []llm.ContentBlock{{Type: "text", Text: question}},
		},
	}})
	log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
		Message: llm.ConversationMsg{
			Content: []llm.ContentBlock{{Type: "text", Text: question}},
		},
	}})

	text := log.Text()
	if strings.Count(text, "[assistant] "+question) != 1 {
		t.Fatalf("expected duplicate assistant question to be rendered once, got: %s", text)
	}
}

func TestMessageLog_Text_ThinkingTruncation(t *testing.T) {
	log := NewMessageLog()
	longThinking := strings.Repeat("x", 300)
	log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
		Message: llm.ConversationMsg{
			Content: []llm.ContentBlock{{Type: "thinking", Thinking: longThinking}},
		},
	}})
	text := log.Text()
	if !strings.Contains(text, "[thinking]") {
		t.Error("Text() missing [thinking]")
	}
	if !strings.Contains(text, "...") {
		t.Error("Text() should truncate long thinking blocks")
	}
}

func TestMessageLog_UpdateLastAssistantPartial(t *testing.T) {
	t.Run("replaces when last is assistant partial", func(t *testing.T) {
		log := NewMessageLog()
		log.Append(llm.SDKMessage{Type: "assistant", Subtype: "partial", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "He"}}},
		}})
		log.UpdateLastAssistantPartial(llm.SDKMessage{Type: "assistant", Subtype: "partial", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "Hello"}}},
		}})
		if log.Len() != 1 {
			t.Fatalf("Len() = %d, want 1", log.Len())
		}
		if log.Messages()[0].Assistant.Message.Content[0].Text != "Hello" {
			t.Error("partial was not updated in place")
		}
	})

	t.Run("appends when last is not assistant partial", func(t *testing.T) {
		log := NewMessageLog()
		log.Append(llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{Subtype: "success"}})
		log.UpdateLastAssistantPartial(llm.SDKMessage{Type: "assistant", Subtype: "partial", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "New"}}},
		}})
		if log.Len() != 2 {
			t.Fatalf("Len() = %d, want 2 (should append, not overwrite)", log.Len())
		}
		if log.Messages()[0].Type != "result" {
			t.Error("first message was overwritten")
		}
	})

	t.Run("appends on empty log", func(t *testing.T) {
		log := NewMessageLog()
		log.UpdateLastAssistantPartial(llm.SDKMessage{Type: "assistant", Subtype: "partial"})
		if log.Len() != 1 {
			t.Fatalf("Len() = %d, want 1", log.Len())
		}
	})

	// Regression: Codex emits accumulated agentMessage text in every partial,
	// and `item/commandExecution/outputDelta` notifications arrive as
	// tool_progress events between successive text deltas while Bash is
	// running. Without coalescing across passive notifications each new
	// partial would be appended as a fresh entry, leaving stacked
	// growing-prefix copies of the same paragraph in the attach view.
	t.Run("coalesces partial across interleaved tool_progress", func(t *testing.T) {
		log := NewMessageLog()
		partial := func(text string) llm.SDKMessage {
			return llm.SDKMessage{Type: "assistant", Subtype: "partial", Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: text}}},
			}}
		}
		toolProgress := func(data string) llm.SDKMessage {
			return llm.SDKMessage{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{
				Type: "tool_progress", ToolName: "Bash", Data: data,
			}}
		}
		log.UpdateLastAssistantPartial(partial("The main binary"))
		log.UpdateLastAssistantPartial(partial("The main binary is flag-only"))
		log.Append(toolProgress("running rg ..."))
		log.UpdateLastAssistantPartial(partial("The main binary is flag-only, then starts the server"))
		log.Append(toolProgress("more output"))
		log.UpdateLastAssistantPartial(partial("The main binary is flag-only, then starts the server. orchestrator actions."))

		msgs := log.Messages()
		var partials []string
		for _, m := range msgs {
			if m.Assistant != nil && m.Subtype == "partial" {
				partials = append(partials, m.Assistant.Message.Content[0].Text)
			}
		}
		if len(partials) != 1 {
			t.Fatalf("want exactly 1 in-flight partial after coalescing, got %d: %v", len(partials), partials)
		}
		if got, want := partials[0], "The main binary is flag-only, then starts the server. orchestrator actions."; got != want {
			t.Errorf("partial text = %q, want %q", got, want)
		}
	})

	t.Run("does not replace thinking partial with later text", func(t *testing.T) {
		log := NewMessageLog()
		log.UpdateLastAssistantPartial(llm.SDKMessage{Type: "assistant", Subtype: "partial", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "thinking", Thinking: "checking"}}},
		}})
		log.Append(llm.SDKMessage{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{ToolName: "Read", Data: "running"}})
		log.UpdateLastAssistantPartial(llm.SDKMessage{Type: "assistant", Subtype: "partial", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "visible text"}}},
		}})

		msgs := log.Messages()
		hasThinking := false
		hasText := false
		for _, m := range msgs {
			if m.Assistant == nil {
				continue
			}
			for _, block := range m.Assistant.Message.Content {
				hasThinking = hasThinking || block.IsThinking()
				hasText = hasText || (block.IsText() && block.Text == "visible text")
			}
		}
		if !hasThinking || !hasText {
			t.Fatalf("messages lost distinct thinking/text partials: %+v", msgs)
		}
	})

	t.Run("final assistant replaces coalesced partial across tool_progress", func(t *testing.T) {
		log := NewMessageLog()
		log.UpdateLastAssistantPartial(llm.SDKMessage{Type: "assistant", Subtype: "partial", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "draft"}}},
		}})
		log.Append(llm.SDKMessage{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{ToolName: "Bash"}})
		log.UpdateLastAssistantPartial(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "final"}}},
		}})
		msgs := log.Messages()
		assistants := 0
		for _, m := range msgs {
			if m.Assistant != nil {
				assistants++
				if m.Subtype == "partial" {
					t.Errorf("stale partial survived final coalesce: %+v", m)
				}
				if got := m.Assistant.Message.Content[0].Text; got != "final" {
					t.Errorf("assistant text = %q, want %q", got, "final")
				}
			}
		}
		if assistants != 1 {
			t.Errorf("want 1 assistant entry, got %d", assistants)
		}
	})

	t.Run("partial appended after finalized assistant boundary", func(t *testing.T) {
		log := NewMessageLog()
		log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "turn1 final"}}},
		}})
		log.Append(llm.SDKMessage{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{ToolName: "Bash"}})
		log.UpdateLastAssistantPartial(llm.SDKMessage{Type: "assistant", Subtype: "partial", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "turn2 partial"}}},
		}})
		if log.Len() != 3 {
			t.Fatalf("Len() = %d, want 3 (finalized assistant must close the partial window)", log.Len())
		}
		if log.Messages()[0].Assistant.Message.Content[0].Text != "turn1 final" {
			t.Error("turn-1 finalized assistant was overwritten across the boundary")
		}
	})

	t.Run("partial appended after user message boundary", func(t *testing.T) {
		log := NewMessageLog()
		log.UpdateLastAssistantPartial(llm.SDKMessage{Type: "assistant", Subtype: "partial", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "stale"}}},
		}})
		log.Append(llm.SDKMessage{Type: "user", User: &llm.UserMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "new prompt"}}},
		}})
		log.UpdateLastAssistantPartial(llm.SDKMessage{Type: "assistant", Subtype: "partial", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "new partial"}}},
		}})
		if log.Len() != 3 {
			t.Fatalf("Len() = %d, want 3 (user message must close the partial window)", log.Len())
		}
		if log.Messages()[0].Assistant.Message.Content[0].Text != "stale" {
			t.Error("pre-user partial was overwritten across the user boundary")
		}
	})
}

func TestMessageLog_ConcurrentAccess(t *testing.T) {
	log := NewMessageLog()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log.Append(llm.SDKMessage{Type: "status"})
		}()
	}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = log.Messages()
			_ = log.Text()
			_ = log.Len()
			_ = log.LastResultMessage()
		}()
	}
	wg.Wait()
	if log.Len() != 100 {
		t.Errorf("Len() = %d after concurrent writes, want 100", log.Len())
	}
}
