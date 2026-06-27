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

package tui

import (
	"encoding/json"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestDescriptionChatModel_Esc_EmitsDescriptionChatExitMsg(t *testing.T) {
	m := DescriptionChatModel{
		ChatModel: NewChatModel(80, 24, nil, "", "", nil, "", ""),
	}
	// input is empty by default
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected a cmd from esc")
	}
	msg := cmd()
	if _, ok := msg.(DescriptionChatExitMsg); !ok {
		t.Fatalf("expected DescriptionChatExitMsg, got %T", msg)
	}
}

func TestDescriptionChatModel_Esc_ClearsInput(t *testing.T) {
	m := DescriptionChatModel{
		ChatModel: NewChatModel(80, 24, nil, "", "", nil, "", ""),
	}
	m.input.SetValue("some text")
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatal("expected nil cmd when input is non-empty")
	}
	if m.input.Value() != "" {
		t.Fatalf("expected input to be cleared, got %q", m.input.Value())
	}
}

func TestDescriptionChatModel_InterceptsUpdatePRDescriptionTool(t *testing.T) {
	input := map[string]interface{}{"title": "New Title", "body": "New Body"}
	rawInput, _ := json.Marshal(input)

	msgs := chatMsgsMsg{
		messages: []llm.SDKMessage{
			{
				Type: "assistant",
				Assistant: &llm.AssistantMessage{
					Message: llm.ConversationMsg{
						Role: "assistant",
						Content: []llm.ContentBlock{
							{Type: "text", Text: "Here's the updated description:"},
							{Type: "tool_use", ID: "tool_1", Name: "UpdatePRDescription", Input: rawInput},
						},
					},
				},
			},
		},
	}

	m := DescriptionChatModel{
		ChatModel: NewChatModel(80, 24, nil, "", "", nil, "", ""),
	}
	m, cmd := m.Update(msgs)
	if cmd == nil {
		t.Fatal("expected a cmd from intercepted tool use")
	}
	msg := cmd()
	upd, ok := msg.(PublishDescriptionUpdatedMsg)
	if !ok {
		t.Fatalf("expected PublishDescriptionUpdatedMsg, got %T", msg)
	}
	if upd.title != "New Title" {
		t.Errorf("title = %q, want 'New Title'", upd.title)
	}
	if upd.body != "New Body" {
		t.Errorf("body = %q, want 'New Body'", upd.body)
	}
	if !m.updated {
		t.Error("expected m.updated to be true")
	}
}

func TestDescriptionChatModel_SubsequentToolUse_IsNoOp(t *testing.T) {
	input := map[string]interface{}{"title": "T1", "body": "B1"}
	rawInput, _ := json.Marshal(input)

	m := DescriptionChatModel{
		ChatModel: NewChatModel(80, 24, nil, "", "", nil, "", ""),
		updated:   true,
	}

	msgs := chatMsgsMsg{
		messages: []llm.SDKMessage{
			{
				Type: "assistant",
				Assistant: &llm.AssistantMessage{
					Message: llm.ConversationMsg{
						Role: "assistant",
						Content: []llm.ContentBlock{
							{Type: "tool_use", ID: "tool_2", Name: "UpdatePRDescription", Input: rawInput},
						},
					},
				},
			},
		},
	}

	m, cmd := m.Update(msgs)
	if cmd != nil {
		t.Fatalf("expected nil cmd for second tool use, got %T", cmd())
	}
}

func TestDescriptionChatModel_ChatSessionKey_Isolated(t *testing.T) {
	m := NewDescriptionChatModel(80, 24, nil, "/tmp", DescriptionChatContext{
		FeatureID: "feat-abc",
	}, nil, "", "")
	if m.sessionKey != "feat-abc-desc-chat" {
		t.Errorf("sessionKey = %q, want 'feat-abc-desc-chat'", m.sessionKey)
	}
}

func TestDescriptionChatModel_NoGeneralChatSession(t *testing.T) {
	m := NewDescriptionChatModel(80, 24, nil, "/tmp", DescriptionChatContext{
		FeatureID: "feat-abc",
	}, nil, "", "")
	if m.sessionKey == chatSessionID {
		t.Errorf("sessionKey should not equal general chat session ID %q", chatSessionID)
	}
}

func TestDescriptionChatModel_CtrlC_StopsSession(t *testing.T) {
	mgr := session.NewManager(nil)
	m := NewDescriptionChatModel(80, 24, mgr, "/tmp", DescriptionChatContext{
		FeatureID: "feat-ctrl",
	}, nil, "", "")
	m.responding = true

	m, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatalf("expected nil cmd after ctrl+c, got %T", cmd())
	}
	if m.responding {
		t.Error("expected responding to be false after ctrl+c")
	}
}
