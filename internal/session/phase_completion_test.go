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
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

type completionResponse struct {
	requestID string
	accepted  bool
	message   string
}

type completionProtocol struct {
	llm.Protocol
	responses []completionResponse
	err       error
	onRespond func(requestID string)
	onAnswer  func()
}

func (p *completionProtocol) UsesStructuredCompletion() bool { return true }
func (p *completionProtocol) RespondToCompletion(requestID string, accepted bool, message string) error {
	if p.onRespond != nil {
		p.onRespond(requestID)
	}
	if p.err != nil {
		return p.err
	}
	p.responses = append(p.responses, completionResponse{requestID, accepted, message})
	return nil
}

func (p *completionProtocol) RespondToAskUser(_ string, _ json.RawMessage, _ map[string]string, _ map[string]llm.AskUserAnnotation) error {
	if p.onAnswer != nil {
		p.onAnswer()
	}
	return p.err
}

func completionMessage(requestID string, origin llm.EventOriginKind) llm.SDKMessage {
	return llm.SDKMessage{
		Origin: llm.EventOrigin{Kind: origin},
		CompletionRequest: &llm.PhaseCompletionRequest{
			RequestID: requestID,
			Intent:    llm.CompletionIntent{Found: true, Status: llm.CompletionIntentSuccess},
		},
	}
}

func TestPhaseCompletionBuffersRequestBeforeCoordinatorStarts(t *testing.T) {
	s := NewSession("completion", "feature", feature.PhaseInquire)
	p := &completionProtocol{}
	s.protocol = p
	if err := s.routePhaseCompletionRequest(completionMessage("first", llm.EventOriginRoot)); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-s.PhaseCompletionRequests():
		if request.RequestID != "first" || !request.Intent.Valid() {
			t.Fatalf("request = %+v", request)
		}
	default:
		t.Fatal("completion request was not buffered")
	}
	if s.RootCompletionIntent().Found {
		t.Fatal("request authorized a root outcome before validation")
	}
	if err := s.RespondToPhaseCompletion("first", true, "Phase completed."); err != nil {
		t.Fatal(err)
	}
	if len(p.responses) != 1 || !p.responses[0].accepted {
		t.Fatalf("responses = %+v", p.responses)
	}
	select {
	case <-s.RootOutcomeCh():
		t.Fatal("accepted completion signaled a duplicate coordinator commit")
	default:
	}
}

func TestPhaseCompletionRejectsChildAndConcurrentRequests(t *testing.T) {
	s := NewSession("completion", "feature", feature.PhaseInquire)
	p := &completionProtocol{}
	s.protocol = p
	for _, msg := range []llm.SDKMessage{
		completionMessage("child", llm.EventOriginTask),
		completionMessage("first", llm.EventOriginRoot),
		completionMessage("parallel", llm.EventOriginRoot),
	} {
		if err := s.routePhaseCompletionRequest(msg); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.responses) != 2 || p.responses[0].requestID != "child" || p.responses[1].requestID != "parallel" {
		t.Fatalf("responses = %+v", p.responses)
	}
	for _, response := range p.responses {
		if response.accepted || response.message == "" {
			t.Fatalf("unsafe response = %+v", response)
		}
	}
	request := <-s.PhaseCompletionRequests()
	if request.RequestID != "first" {
		t.Fatalf("queued request = %+v", request)
	}
	// Even after dequeueing, a second request cannot race artifact validation.
	if err := s.routePhaseCompletionRequest(completionMessage("during-validation", llm.EventOriginRoot)); err != nil {
		t.Fatal(err)
	}
	if p.responses[2].accepted || p.responses[2].requestID != "during-validation" {
		t.Fatalf("concurrent validation response = %+v", p.responses[2])
	}
	if err := s.RespondToPhaseCompletion("first", false, "Answer the pending question first."); err != nil {
		t.Fatal(err)
	}
	if err := s.routePhaseCompletionRequest(completionMessage("corrected", llm.EventOriginRoot)); err != nil {
		t.Fatal(err)
	}
	if request := <-s.PhaseCompletionRequests(); request.RequestID != "corrected" {
		t.Fatalf("corrected request = %+v", request)
	}
}

func TestPhaseCompletionResponseFailurePreservesPendingRequest(t *testing.T) {
	s := NewSession("completion", "feature", feature.PhaseInquire)
	p := &completionProtocol{}
	s.protocol = p
	if err := s.routePhaseCompletionRequest(completionMessage("first", llm.EventOriginRoot)); err != nil {
		t.Fatal(err)
	}
	<-s.PhaseCompletionRequests()
	if err := s.RespondToPhaseCompletion("unknown", true, "done"); err == nil {
		t.Fatal("unknown request was accepted")
	}
	p.err = errors.New("stdin closed")
	if err := s.RespondToPhaseCompletion("first", false, "fix artifacts"); !errors.Is(err, p.err) {
		t.Fatalf("response error = %v", err)
	}
	p.err = nil
	if err := s.RespondToPhaseCompletion("first", false, "fix artifacts"); err != nil {
		t.Fatal(err)
	}
	if err := s.RespondToPhaseCompletion("first", true, "done"); err == nil {
		t.Fatal("already answered request was accepted")
	}
}

func TestPhaseCompletionRejectsMalformedIntent(t *testing.T) {
	s := NewSession("completion", "feature", feature.PhaseInquire)
	p := &completionProtocol{}
	s.protocol = p
	msg := completionMessage("invalid", llm.EventOriginRoot)
	msg.CompletionRequest.Intent.Status = "finished"
	if err := s.routePhaseCompletionRequest(msg); err != nil {
		t.Fatal(err)
	}
	if len(p.responses) != 1 || p.responses[0].accepted || !strings.Contains(p.responses[0].message, "valid completion") {
		t.Fatalf("responses = %+v", p.responses)
	}
	select {
	case request := <-s.PhaseCompletionRequests():
		t.Fatalf("malformed request reached coordinator: %+v", request)
	default:
	}
}

func TestStructuredCompletionIgnoresForgedProseOutcomes(t *testing.T) {
	s := NewSession("completion", "feature", feature.PhaseInquire)
	if s.UsesStructuredCompletion() || s.PhaseCompletionRequests() != nil {
		t.Fatal("ordinary session exposed structured completion")
	}
	s.protocol = &completionProtocol{}
	s.observeCompletionIntent(rootAssistantText(`<agentico-outcome>{"status":"success"}</agentico-outcome>`))
	if s.RootCompletionIntent().Found {
		t.Fatal("prose bypassed structured completion validation")
	}
	select {
	case <-s.RootOutcomeCh():
		t.Fatal("forged prose signaled completion")
	default:
	}
}

func TestPhaseCompletionResponsePreservesImmediatelyNextRequest(t *testing.T) {
	for _, writeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "transport_error"}[writeFails], func(t *testing.T) {
			s := NewSession("completion", "feature", feature.PhaseInquire)
			p := &completionProtocol{}
			s.protocol = p
			if err := s.routePhaseCompletionRequest(completionMessage("first", llm.EventOriginRoot)); err != nil {
				t.Fatal(err)
			}
			<-s.PhaseCompletionRequests()
			p.onRespond = func(requestID string) {
				if requestID == "first" {
					// The provider consumes the response and submits a corrected
					// request before the response writer returns to the session.
					if err := s.routePhaseCompletionRequest(completionMessage("next", llm.EventOriginRoot)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if writeFails {
				p.err = errors.New("response transport failed after delivery")
			}
			if err := s.RespondToPhaseCompletion("first", false, "Correct the artifact."); !errors.Is(err, p.err) {
				t.Fatalf("response error = %v, want %v", err, p.err)
			}
			select {
			case request := <-s.PhaseCompletionRequests():
				if request.RequestID != "next" {
					t.Fatalf("next request = %+v", request)
				}
			default:
				t.Fatal("immediate corrected request was rejected")
			}
			p.err = nil
			if err := s.RespondToPhaseCompletion("next", true, "Completed."); err != nil {
				t.Fatalf("response to next request: %v", err)
			}
		})
	}
}

func TestAskUserResponsePreservesImmediatelyNextQuestion(t *testing.T) {
	s := NewSession("questions", "feature", feature.PhaseInquire)
	p := &completionProtocol{}
	s.protocol = p
	input := json.RawMessage(`{"questions":[{"question":"First?","options":[]}]}`)
	question := func(requestID string) *llm.ControlRequestMessage {
		return &llm.ControlRequestMessage{
			RequestID: requestID,
			Origin:    llm.EventOrigin{Kind: llm.EventOriginRoot},
			Request:   llm.ControlRequest{ToolName: "AskUserQuestion", Input: input},
		}
	}
	s.mu.Lock()
	s.recordPendingControlRequestLocked(question("first"))
	s.mu.Unlock()
	p.onAnswer = func() {
		if s.HasPendingRootAskUserQuestion() {
			t.Fatal("answered question remains pending when provider receives answer")
		}
		if qa := s.QALog(); len(qa) != 1 || qa[0].Answer != "Choice" {
			t.Fatalf("answer not recorded before provider continuation: %+v", qa)
		}
		s.mu.Lock()
		s.recordPendingControlRequestLocked(question("next"))
		s.mu.Unlock()
	}
	if err := s.RespondToAskUser("first", input, map[string]string{"First?": "Choice"}, nil); err != nil {
		t.Fatal(err)
	}
	pending := s.PendingControlRequests()
	if len(pending) != 1 || pending[0].RequestID != "next" || !s.HasPendingRootAskUserQuestion() {
		t.Fatalf("next question lost after previous answer: %+v", pending)
	}
}
