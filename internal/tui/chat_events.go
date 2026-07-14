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

	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

type chatEventKind int

const (
	chatEventAssistantText chatEventKind = iota
	chatEventUserText
	chatEventToolActivity
	chatEventPendingQuestion
	chatEventPendingPermission
	chatEventCompleted
	chatEventError
)

type chatEvent struct {
	Kind       chatEventKind
	Text       string
	Partial    bool
	ToolName   string
	RequestID  string
	Raw        json.RawMessage
	Questions  []askUserQuestion
	Remember   *server.PermissionRememberPreviewDTO
	AutoPicked bool
	Confidence float64
}

func (m ChatModel) ApplyEvents(events []chatEvent) ChatModel {
	for _, event := range events {
		switch event.Kind {
		case chatEventAssistantText:
			m.setInProgressAgentText(event.Text, event.Partial)
			m.thinkingLine = ""
		case chatEventUserText:
			m.turns = append(m.turns, chatTurn{
				Role:       chatTurnUser,
				Text:       event.Text,
				AutoPicked: event.AutoPicked,
				Confidence: event.Confidence,
			})
		case chatEventToolActivity:
			if event.Text != "" {
				m.thinkingLine = event.Text
			} else if event.ToolName != "" {
				m.thinkingLine = fmt.Sprintf("Using %s...", event.ToolName)
			}
		case chatEventPendingQuestion:
			m.applyPendingQuestionEvent(event)
		case chatEventPendingPermission:
			m.applyPendingPermissionEvent(event)
		case chatEventCompleted:
			m.finalizeInProgressTurn()
			m.responding = false
			m.thinkingLine = ""
			if m.sess != nil {
				m.turnCostBaseline = m.sess.Cost()
			}
		case chatEventError:
			m.discardInProgressTurn()
			m.responding = false
			m.thinkingLine = ""
			text := event.Text
			if text == "" {
				text = sessionErrorFallbackText
			}
			m.turns = append(m.turns, chatTurn{Role: chatTurnError, Text: text})
			if m.sess != nil {
				m.turnCostBaseline = m.sess.Cost()
			}
		}
	}
	m.syncAutoPickedTurns()
	m.rebuildViewport()
	return m
}

func (m *ChatModel) applyPendingPermissionEvent(event chatEvent) {
	if event.RequestID != "" {
		if _, alreadyAnswered := m.answeredPermRequestIDs[event.RequestID]; alreadyAnswered {
			return
		}
		if event.RequestID == m.pendingPermRequestID {
			m.responding = false
			m.thinkingLine = ""
			return
		}
	}
	if event.RequestID == "" || event.ToolName == "" || m.hasActiveQuestion() || m.hasActivePermission() {
		return
	}
	m.finalizeInProgressTurn()
	m.pendingPermRequestID = event.RequestID
	m.pendingPermToolName = event.ToolName
	m.pendingPermSummary = event.Text
	m.pendingPermInput = event.Raw
	m.pendingPermRemember = event.Remember
	m.responding = false
	m.thinkingLine = ""
	if m.sess != nil {
		m.turnCostBaseline = m.sess.Cost()
	}
	m.rebuildViewport()
	*m = m.resize(m.width, m.height)
}

func (m *ChatModel) applyPendingQuestionEvent(event chatEvent) {
	if event.RequestID != "" {
		if _, alreadyAnswered := m.answeredAskRequestIDs[event.RequestID]; alreadyAnswered {
			return
		}
		if event.RequestID == m.pendingAskRequestID {
			m.responding = false
			m.thinkingLine = ""
			return
		}
	}
	questions := event.Questions
	if len(questions) == 0 && len(event.Raw) > 0 {
		questions = parseAskUserQuestions(event.Raw)
	}
	if len(questions) == 0 || m.hasActiveQuestion() || askUserQuestionsAlreadyAutoPicked(m.sess, questions) {
		return
	}
	m.finalizeInProgressTurnBeforeQuestion(questions)
	m.activateQuestions(questions, event.RequestID, event.Raw)
	m.responding = false
	m.thinkingLine = ""
	if m.sess != nil {
		m.turnCostBaseline = m.sess.Cost()
	}
}

func (m *ChatModel) finalizeInProgressTurnBeforeQuestion(questions []askUserQuestion) {
	n := len(m.turns)
	if n == 0 || m.turns[n-1].Role != chatTurnAgent || !m.turns[n-1].InProgress {
		return
	}
	// A match at (or near) the very start of the turn means the question stem
	// leads the whole message with no separate intro (e.g. a synthesized
	// AskUserQuestion whose stem runs straight into its options). Trimming would
	// wipe the turn instead of just the trailing draft duplicate, so keep the
	// original text and let the picker render alongside it rather than in place
	// of it.
	trimmed, ok := trimStreamedAskUserDraft(m.turns[n-1].Text, questions)
	if !ok || strings.TrimSpace(trimmed) == "" {
		m.finalizeInProgressTurn()
		return
	}
	m.turns[n-1].Text = trimmed
	m.turns[n-1].InProgress = false
}

func trimStreamedAskUserDraft(text string, questions []askUserQuestion) (string, bool) {
	if strings.TrimSpace(text) == "" || len(questions) == 0 {
		return text, false
	}
	if idx, ok := firstAskUserDraftMatch(text, questions, false); ok {
		return strings.TrimSpace(text[:idx]), true
	}
	if idx, ok := firstAskUserDraftMatch(text, questions, true); ok {
		return strings.TrimSpace(text[:idx]), true
	}
	return text, false
}

func firstAskUserDraftMatch(text string, questions []askUserQuestion, includeOptions bool) (int, bool) {
	lowerText := strings.ToLower(text)
	cut := len(text)
	found := false
	for _, q := range questions {
		candidates := []string{q.Question, q.RawQuestion, q.Header}
		if includeOptions {
			for _, opt := range q.Options {
				candidates = append(candidates, opt.Label)
			}
		}
		for _, candidate := range candidates {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			idx := strings.Index(text, candidate)
			if idx < 0 {
				idx = strings.Index(lowerText, strings.ToLower(candidate))
			}
			if idx >= 0 && idx < cut {
				cut = idx
				found = true
			}
		}
	}
	return cut, found
}
