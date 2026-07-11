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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// turnStateX are normalizedTurnState() wire tokens for
// SessionDetailDTO.TurnState.
const (
	turnStateRunning           = "running"
	turnStateWaitingInput      = "waiting_input"
	turnStateWaitingQuestion   = "waiting_question"
	turnStateWaitingPermission = "waiting_permission"
	turnStateCompleted         = "completed"
	turnStateFailed            = "failed"
)

type apiChatSnapshotInput struct {
	Session                *apiSessionView
	Detail                 server.SessionDetailDTO
	Transcript             *server.TranscriptResponse
	Controls               []server.ControlRequestDTO
	WasResponding          bool
	HasInProgressAgentText bool
}

const apiChatNoAnswerText = "No answer was returned before the chat session became ready for the next message."

// sessionErrorFallbackText is shown when a failed session has no SafeError set.
const sessionErrorFallbackText = "Session error"

func apiChatEventsFromSnapshot(in apiChatSnapshotInput) []chatEvent {
	if in.Session == nil {
		return nil
	}
	controls := in.Controls
	if len(controls) == 0 {
		controls = in.Detail.PendingControls
	}
	in.Session.applyAPIChatSessionState(in.Detail, controls)

	events := in.Session.apiChatTranscriptEvents(in.Transcript)
	for _, event := range apiChatPendingQuestionEvents(controls) {
		if !chatEventsContainPendingQuestion(events, event.RequestID) {
			events = append(events, event)
		}
	}
	if in.WasResponding && apiChatSnapshotFailed(in.Detail) && !chatEventsEndTurn(events) {
		events = append(events, chatEvent{Kind: chatEventError, Text: apiChatFailureText(in.Detail)})
	}
	if in.WasResponding && apiChatSnapshotReadyForNextMessage(in.Detail, controls) && !chatEventsEndTurn(events) {
		if !in.HasInProgressAgentText && !chatEventsHaveAssistantText(events) {
			events = append(events, chatEvent{Kind: chatEventAssistantText, Text: apiChatNoAnswerText})
		}
		events = append(events, chatEvent{Kind: chatEventCompleted})
	}
	return events
}

func (s *apiSessionView) applyAPIChatSessionState(detail server.SessionDetailDTO, controls []server.ControlRequestDTO) {
	s.applyAPISessionState(detail, controls, apiSessionStateUpdate{useTurnState: true, forceInitialPrompt: true})
}

func (s *apiSessionView) apiChatTranscriptEvents(transcript *server.TranscriptResponse) []chatEvent {
	if transcript == nil {
		return nil
	}
	if s.lastTranscriptRows == nil {
		s.lastTranscriptRows = map[string]string{}
	}
	var events []chatEvent
	for _, row := range transcript.Messages {
		if row.Index < s.lastTranscriptMessage {
			continue
		}
		msg, hasMsg := apiTranscriptRowToSDKMessage(row, s.id)
		event, hasEvent := apiChatEventFromTranscriptRow(row)
		if !hasMsg && !hasEvent {
			continue
		}
		key := apiTranscriptRowKey(row)
		signature := apiTranscriptRowSignature(row)
		if apiVisibleUserTranscriptRow(row) && apiHasLocalUserEcho(s.log, row.Text) {
			s.rememberAPIChatTranscriptRow(row, key, signature)
			continue
		}
		if row.Index > s.lastTranscriptMessage {
			if hasMsg {
				s.log.Append(msg)
			}
			if hasEvent {
				events = append(events, event)
			}
			s.lastTranscriptMessage = row.Index
			s.lastTranscriptRows = map[string]string{key: signature}
			s.lastTranscriptTailKey = key
			continue
		}
		if previous, ok := s.lastTranscriptRows[key]; ok {
			if s.updateKnownAPIChatTranscriptRow(key, signature, previous, msg, hasMsg) && hasEvent {
				events = append(events, event)
			}
			continue
		}
		if hasMsg {
			s.log.Append(msg)
		}
		s.lastTranscriptRows[key] = signature
		s.lastTranscriptTailKey = key
		if hasEvent {
			events = append(events, event)
		}
	}
	return collapseAPIChatErrorEvents(events)
}

// updateKnownAPIChatTranscriptRow updates the log entry for a transcript row
// that was already seen. It returns false (and does nothing else) when the
// row's signature is unchanged; otherwise it applies the new message to the
// log and records the new signature, returning true.
func (s *apiSessionView) updateKnownAPIChatTranscriptRow(key, signature, previous string, msg llm.SDKMessage, hasMsg bool) bool {
	if previous == signature {
		return false
	}
	if hasMsg {
		if key == s.lastTranscriptTailKey && apiCanUpdateLastTranscriptLogMessage(s.log) {
			s.log.UpdateLast(msg)
		} else {
			s.log.Append(msg)
			s.lastTranscriptTailKey = key
		}
	}
	s.lastTranscriptRows[key] = signature
	return true
}

func (s *apiSessionView) rememberAPIChatTranscriptRow(row server.TranscriptMessageDTO, key, signature string) {
	if row.Index > s.lastTranscriptMessage {
		s.lastTranscriptMessage = row.Index
		s.lastTranscriptRows = map[string]string{}
	}
	if row.Index == s.lastTranscriptMessage {
		s.lastTranscriptRows[key] = signature
		s.lastTranscriptTailKey = key
	}
}

func apiChatEventFromTranscriptRow(row server.TranscriptMessageDTO) (chatEvent, bool) {
	switch row.Type {
	case blockTypeText:
		text := strings.TrimSpace(row.Text)
		if text == "" {
			return chatEvent{}, false
		}
		if row.Role == roleUser {
			return chatEvent{
				Kind:       chatEventUserText,
				Text:       text,
				AutoPicked: row.AutoPicked,
				Confidence: row.AutoPickConfidence,
			}, true
		}
		return chatEvent{Kind: chatEventAssistantText, Text: text}, true
	case blockTypeToolUse, transcriptTypeToolProgress:
		if row.Tool == "" {
			return chatEvent{}, false
		}
		return chatEvent{Kind: chatEventToolActivity, ToolName: row.Tool}, true
	case transcriptTypeTaskStarted, transcriptTypeTaskProgress:
		if row.Task == nil {
			return chatEvent{}, false
		}
		return chatEvent{Kind: chatEventToolActivity, Text: apiChatTaskActivityText(row.Task)}, true
	case transcriptTypeTaskNotification:
		if row.Task == nil {
			return chatEvent{}, false
		}
		return chatEvent{Kind: chatEventToolActivity, Text: apiChatTaskActivityText(row.Task)}, true
	case transcriptTypeResult:
		if apiTranscriptResultIsError(row.Status) {
			text := strings.TrimSpace(row.Text)
			if text == "" {
				text = sessionErrorFallbackText
			}
			return chatEvent{Kind: chatEventError, Text: text}, true
		}
		return chatEvent{Kind: chatEventCompleted}, true
	case transcriptTypeStatus:
		if strings.TrimSpace(row.Text) == "" {
			return chatEvent{}, false
		}
		return chatEvent{Kind: chatEventToolActivity, Text: row.Text}, true
	default:
		return chatEvent{}, false
	}
}

func collapseAPIChatErrorEvents(events []chatEvent) []chatEvent {
	var lastAssistantText string
	for i := range events {
		switch events[i].Kind {
		case chatEventAssistantText:
			if strings.TrimSpace(events[i].Text) != "" {
				lastAssistantText = events[i].Text
			}
		case chatEventError:
			if events[i].Text == sessionErrorFallbackText && strings.TrimSpace(lastAssistantText) != "" {
				events[i].Text = lastAssistantText
			}
		}
	}
	return events
}

func apiChatTaskActivityText(task *server.TaskDTO) string {
	if task == nil {
		return ""
	}
	if strings.TrimSpace(task.LastToolName) != "" {
		return "Using " + task.LastToolName + "..."
	}
	if strings.TrimSpace(task.Description) != "" {
		return task.Description
	}
	if strings.TrimSpace(task.Summary) != "" {
		return task.Summary
	}
	return "Task running..."
}

func apiTranscriptResultIsError(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case resultSubtypeError, turnStateFailed, "failure", "max_budget":
		return true
	default:
		return false
	}
}

func apiChatPendingQuestionEvents(controls []server.ControlRequestDTO) []chatEvent {
	var events []chatEvent
	for _, req := range controls {
		if req.ToolName != toolNameAskUserQuestion || !isPendingControlStatus(req.Status) {
			continue
		}
		raw := apiControlRequestInput(req)
		questions := parseAskUserQuestions(raw)
		if len(questions) == 0 {
			continue
		}
		events = append(events, chatEvent{
			Kind:      chatEventPendingQuestion,
			RequestID: req.RequestID,
			Raw:       raw,
			Questions: questions,
		})
	}
	return events
}

func apiChatSnapshotReadyForNextMessage(detail server.SessionDetailDTO, controls []server.ControlRequestDTO) bool {
	if len(controls) > 0 {
		return false
	}
	switch normalizedTurnState(detail.TurnState) {
	case turnStateWaitingInput:
		return true
	case turnStateWaitingQuestion, turnStateWaitingPermission, turnStateRunning, turnStateCompleted, turnStateFailed:
		return false
	default:
		return apiSessionStatus(detail.Status) == ports.SessionWaitingHelp
	}
}

func apiChatSnapshotFailed(detail server.SessionDetailDTO) bool {
	if normalizedTurnState(detail.TurnState) == turnStateFailed {
		return true
	}
	return apiSessionStatus(detail.Status) == ports.SessionFailed
}

func apiChatFailureText(detail server.SessionDetailDTO) string {
	if text := strings.TrimSpace(detail.SafeError); text != "" {
		return text
	}
	return sessionErrorFallbackText
}

func apiSessionStatusFromTurnState(turnState string) (ports.SessionStatus, bool) {
	switch normalizedTurnState(turnState) {
	case turnStateRunning:
		return ports.SessionRunning, true
	case turnStateWaitingInput, turnStateWaitingQuestion:
		return ports.SessionWaitingHelp, true
	case turnStateWaitingPermission:
		return ports.SessionWaitingPermission, true
	case turnStateCompleted:
		return ports.SessionDone, true
	case turnStateFailed:
		return ports.SessionFailed, true
	default:
		return ports.SessionRunning, false
	}
}

func normalizedTurnState(turnState string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(turnState), "-", "_"))
}

func chatEventsEndTurn(events []chatEvent) bool {
	for _, event := range events {
		if event.Kind == chatEventCompleted || event.Kind == chatEventError || event.Kind == chatEventPendingQuestion {
			return true
		}
	}
	return false
}

func chatEventsHaveAssistantText(events []chatEvent) bool {
	for _, event := range events {
		if event.Kind == chatEventAssistantText && strings.TrimSpace(event.Text) != "" {
			return true
		}
	}
	return false
}

func chatEventsContainPendingQuestion(events []chatEvent, requestID string) bool {
	for _, event := range events {
		if event.Kind == chatEventPendingQuestion && (requestID == "" || event.RequestID == requestID) {
			return true
		}
	}
	return false
}
