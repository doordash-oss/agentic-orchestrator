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

type observedEvent struct {
	eventType string
	traceID   string
	spanID    string
	sessionID string
	repoName  string
	iteration int
	data      map[string]string
}

type recordingObserver struct {
	events []observedEvent
}

func (o *recordingObserver) PermissionRequested(sc ObservabilityContext, sessionID, repoName string, iteration int, toolName, toolInput string) {
	o.events = append(o.events, observedEvent{
		eventType: "permission.requested",
		traceID:   sc.TraceID,
		spanID:    sc.SpanID,
		sessionID: sessionID,
		repoName:  repoName,
		iteration: iteration,
		data: map[string]string{
			"tool_name":  toolName,
			"tool_input": toolInput,
		},
	})
}

func (o *recordingObserver) PermissionResolved(sc ObservabilityContext, sessionID, repoName string, iteration int, toolName, decision string) {
	o.events = append(o.events, observedEvent{
		eventType: "permission.resolved",
		traceID:   sc.TraceID,
		spanID:    sc.SpanID,
		sessionID: sessionID,
		repoName:  repoName,
		iteration: iteration,
		data: map[string]string{
			"tool_name": toolName,
			"decision":  decision,
		},
	})
}

func (o *recordingObserver) QuestionAsked(sc ObservabilityContext, sessionID, repoName string, iteration int, question string) {
	o.events = append(o.events, observedEvent{
		eventType: "question.asked",
		traceID:   sc.TraceID,
		spanID:    sc.SpanID,
		sessionID: sessionID,
		repoName:  repoName,
		iteration: iteration,
		data: map[string]string{
			"question": question,
		},
	})
}

func (o *recordingObserver) QuestionAnswered(sc ObservabilityContext, sessionID, repoName string, iteration int, question, answer string) {
	o.events = append(o.events, observedEvent{
		eventType: "question.answered",
		traceID:   sc.TraceID,
		spanID:    sc.SpanID,
		sessionID: sessionID,
		repoName:  repoName,
		iteration: iteration,
		data: map[string]string{
			"question": question,
			"answer":   answer,
		},
	})
}

func (o *recordingObserver) event(eventType string) (observedEvent, bool) {
	for _, event := range o.events {
		if event.eventType == eventType {
			return event, true
		}
	}
	return observedEvent{}, false
}
