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
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/utilskill"
)

// DescriptionChatModel is a dedicated chat model for refining PR descriptions.
// It mirrors ChatModel but uses a feature-scoped session key, intercepts the
// UpdatePRDescription tool, and emits DescriptionChatExitMsg on Esc.
type DescriptionChatModel struct {
	ChatModel
	sessionKey string
	ctx        DescriptionChatContext
	updated    bool
}

// NewDescriptionChatModel creates a description chat model primed with the
// snapshotted PR description context and a custom session key.
func NewDescriptionChatModel(
	width, height int,
	sm *session.Manager,
	workDir string,
	ctx DescriptionChatContext,
	buildSession agent.BuildSessionFunc,
	chatModel string,
	skillsDir string,
) DescriptionChatModel {
	cm := NewChatModel(width, height, sm, workDir, buildDescriptionChatSystemPrompt(ctx), buildSession, chatModel, skillsDir)
	return DescriptionChatModel{
		ChatModel:  cm,
		sessionKey: fmt.Sprintf("%s-desc-chat", ctx.FeatureID),
		ctx:        ctx,
	}
}

// Update processes key and async messages for the description chat. It
// intercepts Esc/ctrl+c, enter, and the UpdatePRDescription tool-use block
// before forwarding everything else to the embedded ChatModel.
func (m DescriptionChatModel) Update(msg tea.Msg) (DescriptionChatModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			if m.responding {
				return m, func() tea.Msg { return DescriptionChatExitMsg{} }
			}
			if strings.TrimSpace(m.input.Value()) == "" {
				return m, func() tea.Msg { return DescriptionChatExitMsg{} }
			}
			m.input.Reset()
			return m, nil

		case "ctrl+c":
			if m.responding || m.sess != nil {
				if m.sessionMgr != nil && m.sess != nil {
					_ = m.sessionMgr.StopSession(m.sessionKey)
				}
				m.responding = false
				m.sess = nil
				m.thinkingLine = ""
				if m.partialText != "" {
					m.history += m.partialText
					m.partialText = ""
				}
				m.history += "\n  [cancelled]\n"
				m.rebuildViewport()
				return m, nil
			}
			return m, func() tea.Msg { return DescriptionChatExitMsg{} }

		case "enter":
			if m.responding {
				return m, nil
			}
			question := strings.TrimSpace(m.input.Value())
			if question == "" {
				return m, nil
			}
			m.input.Reset()
			m.history += "\n" + chatUserStyle.Render("You: "+question) + "\n\n"
			if m.sessionMgr == nil {
				m.history += "  Error: no session manager available\n"
				m.rebuildViewport()
				return m, nil
			}
			m.responding = true
			m.thinkingLine = ""
			m.rebuildViewport()
			if m.sess == nil {
				m.turnCostBaseline = nil
				return m, m.startSessionCmd(question)
			}
			m.turnCostBaseline = m.sess.Cost()
			sess := m.sess
			return m, tea.Batch(
				func() tea.Msg {
					if err := sess.SendUserMessage(question); err != nil {
						return chatSendErrorMsg{err: err}
					}
					return nil
				},
				chatRecoveryTickCmd(sess, m.turnCostBaseline),
			)
		}

	case chatMsgsMsg:
		for _, sdkMsg := range msg.messages {
			if sdkMsg.Assistant != nil {
				for _, block := range sdkMsg.Assistant.Message.Content {
					if block.IsToolUse() && block.Name == "UpdatePRDescription" && !m.updated {
						var input struct {
							Title string `json:"title"`
							Body  string `json:"body"`
						}
						if err := json.Unmarshal(block.Input, &input); err == nil {
							m.updated = true
							if m.sessionMgr != nil && m.sess != nil {
								toolResult := map[string]interface{}{
									"type": "user",
									"message": map[string]interface{}{
										"role": "user",
										"content": []map[string]interface{}{
											{
												"type":        "tool_result",
												"tool_use_id": block.ID,
												"content":     "Description updated.",
											},
										},
									},
								}
								if data, err := json.Marshal(toolResult); err == nil {
									data = append(data, '\n')
									_ = m.sessionMgr.SendInput(m.sessionKey, data)
								}
							}
							return m, func() tea.Msg {
								return PublishDescriptionUpdatedMsg{title: input.Title, body: input.Body}
							}
						}
					}
				}
			}
		}
		// If we didn't intercept the tool, fall through to ChatModel.Update
	}

	cm, cmd := m.ChatModel.Update(msg)
	m.ChatModel = cm
	return m, cmd
}

// startSessionCmd launches the interactive session for the description chat
// using the feature-scoped session key and allowing the UpdatePRDescription
// tool.
func (m DescriptionChatModel) startSessionCmd(initialQuestion string) tea.Cmd {
	return func() tea.Msg {
		prompt := initialQuestion
		skillInstruction := chatSkillInstruction(m.skillsDir)
		if skillInstruction != "" {
			prompt = skillInstruction + "\n\n" + prompt
		}
		cmd, env, sessOpts, err := m.buildSession(agent.BuildSessionOpts{
			Model:           m.chatModel,
			Prompt:          prompt,
			SystemPrompt:    m.systemPrompt,
			AllowedTools:    []string{"UpdatePRDescription"},
			DisallowedTools: []string{"Edit", "Write", "NotebookEdit", "Bash"},
			WorkDir:         m.workDir,
			PermHandler:     &session.ReadOnlyHandler{},
			Phase:           utilskill.PhaseAll,
			TurnMode:        ports.TurnModeInteractive,
		})
		if err != nil {
			return chatMsgsMsg{messages: []llm.SDKMessage{
				{Type: "assistant", Assistant: &llm.AssistantMessage{
					Message: llm.ConversationMsg{
						Role:    "assistant",
						Content: []llm.ContentBlock{{Type: "text", Text: "Error starting session: " + err.Error()}},
					},
				}},
			}}
		}
		sessOpts.InitialPrompt = prompt
		sess, err := m.sessionMgr.StartSession(m.sessionKey, "", feature.PhaseResearch, cmd, m.workDir, env, sessOpts)
		if err != nil {
			return chatMsgsMsg{messages: []llm.SDKMessage{
				{Type: "assistant", Assistant: &llm.AssistantMessage{
					Message: llm.ConversationMsg{
						Role:    "assistant",
						Content: []llm.ContentBlock{{Type: "text", Text: "Error starting session: " + err.Error()}},
					},
				}},
			}}
		}
		return chatSessionStartedMsg{sess: sess}
	}
}
