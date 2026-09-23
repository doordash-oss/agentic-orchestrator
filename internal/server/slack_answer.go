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

package server

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/clone"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

var _ ports.SlackAnswerPort = (*apiHandler)(nil)

type permissionAnswerLockSet struct {
	mu    sync.Mutex
	locks map[string]*permissionAnswerLock
}

type permissionAnswerLock struct {
	mu   sync.Mutex
	refs int
}

func newPermissionAnswerLockSet() *permissionAnswerLockSet {
	return &permissionAnswerLockSet{locks: make(map[string]*permissionAnswerLock)}
}

func (s *permissionAnswerLockSet) lock(requestID string) func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	if s.locks == nil {
		s.locks = make(map[string]*permissionAnswerLock)
	}
	entry := s.locks[requestID]
	if entry == nil {
		entry = &permissionAnswerLock{}
		s.locks[requestID] = entry
	}
	entry.refs++
	s.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		s.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(s.locks, requestID)
		}
		s.mu.Unlock()
	}
}

// AnswerSlackPermission submits the permission grammar exposed by Slack
// through the same mutation target used by the REST endpoint.
func (h *apiHandler) AnswerSlackPermission(answer ports.SlackPermissionAnswer) ports.SlackAnswerResult {
	if h == nil || h.mutations == nil {
		return slackAnswerFailed(errors.New("permission mutation target is unavailable"))
	}
	requestID := strings.TrimSpace(answer.RequestID)
	featureID := strings.TrimSpace(answer.SourceFeatureID)
	if requestID == "" || featureID == "" {
		return slackAnswerFailed(errors.New("request_id and source feature are required"))
	}
	if answer.Source.Kind != ports.AnswerSourceSlack {
		return slackAnswerFailed(errors.New("Slack answer source is required"))
	}
	var decision string
	switch answer.Decision {
	case ports.SlackPermissionAllowOnce:
		decision = decisionAllowOnce
	case ports.SlackPermissionDeny:
		decision = decisionDeny
	default:
		return slackAnswerFailed(fmt.Errorf("unsupported Slack permission decision %q", answer.Decision))
	}

	unlock := h.permissionAnswerLocks.lock(requestID)
	defer unlock()
	sessionID, result := h.slackPermissionSession(featureID, requestID)
	if result.Outcome != "" {
		return result
	}
	source := scrubSlackAnswerSource(answer.Source)
	_, err := h.mutations.AnswerPermission(PermissionAnswerRequest{
		SessionID: sessionID,
		RequestID: requestID,
		Decision:  decision,
		Source:    &source,
	})
	if err != nil {
		return slackPermissionFailure(err)
	}
	log.Printf(
		"slack-answer: permission accepted request=%q responder=%q",
		requestID,
		source.Responder,
	)
	return ports.SlackAnswerResult{Outcome: ports.SlackAnswerAccepted}
}

func (h *apiHandler) slackPermissionSession(featureID, requestID string) (string, ports.SlackAnswerResult) {
	if h.sessions == nil {
		return "", slackAnswerFailed(errors.New("session manager is unavailable"))
	}
	for _, session := range h.sessions.ActiveSessions() {
		if session == nil || session.FeatureID() != featureID {
			continue
		}
		for _, pending := range session.PendingControlRequests() {
			if pending == nil || pending.RequestID != requestID {
				continue
			}
			if pending.Request.ToolName == toolNameAskUserQuestion {
				return "", slackAnswerFailed(fmt.Errorf("request %s has incompatible control type", requestID))
			}
			return session.ID(), ports.SlackAnswerResult{}
		}
	}
	return "", ports.SlackAnswerResult{
		Outcome: ports.SlackAnswerNoLongerPending,
		Cause:   fmt.Errorf("%w: pending request %s not found", ErrNoLongerPending, requestID),
	}
}

// ApproveSlackReview opens or reopens the review session for the posted item,
// then submits proceed against the exact source revision the reader saw.
func (h *apiHandler) ApproveSlackReview(approval ports.SlackReviewApproval) ports.SlackAnswerResult {
	if h == nil || h.mutations == nil {
		return slackAnswerFailed(errors.New("review mutation target is unavailable"))
	}
	featureID := strings.TrimSpace(approval.SourceFeatureID)
	reviewID := strings.TrimSpace(approval.ReviewID)
	sourceRevision := strings.TrimSpace(approval.SourceRevision)
	if featureID == "" || reviewID == "" || sourceRevision == "" {
		return slackAnswerFailed(errors.New("feature, review, and source revision are required"))
	}
	if approval.Source.Kind != ports.AnswerSourceSlack {
		return slackAnswerFailed(errors.New("Slack answer source is required"))
	}
	service := h.reviewSessionService()
	session, err := service.Create(featureID)
	if err != nil {
		return slackReviewFailure(err)
	}
	if session.ReviewID != reviewID || session.SourceRevision != sourceRevision {
		return ports.SlackAnswerResult{
			Outcome: ports.SlackAnswerRevisionMoved,
			Cause:   fmt.Errorf("review source revision moved"),
		}
	}
	source := scrubSlackAnswerSource(approval.Source)
	_, err = service.SubmitDecision(featureID, reviewID, ReviewSessionDecisionRequest{
		Decision:     reviewDecisionProceed,
		BaseRevision: sourceRevision,
		Source:       &source,
	})
	if err != nil {
		return slackReviewFailure(err)
	}
	log.Printf(
		"slack-answer: review accepted feature=%q review=%q responder=%q",
		featureID,
		reviewID,
		source.Responder,
	)
	return ports.SlackAnswerResult{Outcome: ports.SlackAnswerAccepted}
}

func scrubSlackAnswerSource(source ports.AnswerSource) ports.AnswerSource {
	source.Responder = clone.RedactDiagnostics(source.Responder)
	return source
}

func slackPermissionFailure(err error) ports.SlackAnswerResult {
	if errors.Is(err, ErrNoLongerPending) {
		return ports.SlackAnswerResult{Outcome: ports.SlackAnswerNoLongerPending, Cause: err}
	}
	var conflict *ActionConflictError
	if errors.As(err, &conflict) && conflict.Code == errcat.NoLongerPending {
		return ports.SlackAnswerResult{Outcome: ports.SlackAnswerNoLongerPending, Cause: err}
	}
	return slackAnswerFailed(err)
}

func slackReviewFailure(err error) ports.SlackAnswerResult {
	if errors.Is(err, ErrNoLongerPending) {
		return ports.SlackAnswerResult{Outcome: ports.SlackAnswerNoLongerPending, Cause: err}
	}
	var conflict *ActionConflictError
	if errors.As(err, &conflict) {
		if conflict.Code == errcat.NoLongerPending {
			return ports.SlackAnswerResult{Outcome: ports.SlackAnswerNoLongerPending, Cause: err}
		}
		return ports.SlackAnswerResult{Outcome: ports.SlackAnswerRevisionMoved, Cause: err}
	}
	return slackAnswerFailed(err)
}

func slackAnswerFailed(err error) ports.SlackAnswerResult {
	return ports.SlackAnswerResult{Outcome: ports.SlackAnswerFailed, Cause: err}
}
