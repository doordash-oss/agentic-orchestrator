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
)

type chatEventKind int

const (
	chatEventAssistantText chatEventKind = iota
	chatEventUserText
	chatEventToolActivity
	chatEventPendingQuestion
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
				text = "Session error"
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
	m.finalizeInProgressTurn()
	m.activateQuestions(questions, event.RequestID, event.Raw)
	m.responding = false
	m.thinkingLine = ""
	if m.sess != nil {
		m.turnCostBaseline = m.sess.Cost()
	}
}
