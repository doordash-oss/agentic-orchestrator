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
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
)

func (m ChatModel) hasActivePermission() bool {
	return strings.TrimSpace(m.pendingPermRequestID) != ""
}

func (m ChatModel) renderPermissionPrompt() (body, footer string) {
	toolName := strings.TrimSpace(m.pendingPermToolName)
	if toolName == "" {
		toolName = "Tool"
	}
	detail := strings.TrimSpace(m.pendingPermSummary)
	if detail == "" {
		detail = strings.TrimSpace(formatPermissionDetail(toolName, m.pendingPermInput))
	}

	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("Permission request"))
	b.WriteString("\n\n")
	if detail != "" {
		fmt.Fprintf(&b, "%s: %s", toolName, firstLine(detail))
	} else {
		b.WriteString(toolName)
	}
	if m.pendingPermRemember != nil && strings.TrimSpace(m.pendingPermRemember.Pattern) != "" {
		fmt.Fprintf(&b, "\nRemember: %s", m.pendingPermRemember.Pattern)
	}

	footer = "[y] Allow once | [n] Deny | [esc] Close"
	if m.pendingPermRemember != nil {
		footer = "[y] Allow once | [A] Allow & remember | [n] Deny | [esc] Close"
	}
	return b.String(), KeyHelpStyle.Render(footer)
}

func (m ChatModel) submitPermissionDecision(decision string) (ChatModel, tea.Cmd) {
	requestID := m.pendingPermRequestID
	sess := m.sess
	m.clearPendingPermission()
	m.responding = true
	m.rebuildViewport()
	m = m.resize(m.width, m.height)

	if requestID == "" || sess == nil {
		return m, nil
	}
	m.turnCostBaseline = sess.Cost()
	if m.answeredPermRequestIDs == nil {
		m.answeredPermRequestIDs = make(map[string]struct{})
	}
	m.answeredPermRequestIDs[requestID] = struct{}{}

	sendCmd := func() tea.Msg {
		if responder, ok := sess.(explicitPermissionResponder); ok {
			if err := responder.RespondToPermissionDecision(requestID, decision, "", ""); err != nil {
				return chatSendErrorMsg{err: err}
			}
			return nil
		}
		allow := decision == permission.DecisionAllowOnce || decision == permission.DecisionAllowRemember
		reason := ""
		if !allow {
			reason = "denied by user"
		}
		if err := sess.RespondToControl(requestID, allow, reason); err != nil {
			return chatSendErrorMsg{err: err}
		}
		return nil
	}
	return m, func() tea.Msg {
		if msg := sendCmd(); msg != nil {
			return msg
		}
		return chatRecoveryTickMsg{sess: sess, baseline: m.turnCostBaseline}
	}
}

func (m *ChatModel) clearPendingPermission() {
	m.pendingPermRequestID = ""
	m.pendingPermToolName = ""
	m.pendingPermSummary = ""
	m.pendingPermInput = nil
	m.pendingPermRemember = nil
}

func permissionInputRaw(input map[string]interface{}) json.RawMessage {
	if len(input) == 0 {
		return nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	return data
}
