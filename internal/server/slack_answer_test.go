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
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type slackAnswerMutationTarget struct {
	MutationTarget
	permissionRequests []PermissionAnswerRequest
	permissionErr      error
	reviewRequests     []ReviewDecisionRequest
}

func (t *slackAnswerMutationTarget) AnswerPermission(req PermissionAnswerRequest) (PermissionAnswerResponse, error) {
	t.permissionRequests = append(t.permissionRequests, req)
	if t.permissionErr != nil {
		return PermissionAnswerResponse{}, t.permissionErr
	}
	return PermissionAnswerResponse{RequestID: req.RequestID, Decision: string(req.Decision)}, nil
}

func (t *slackAnswerMutationTarget) ReviewDecision(_ string, req ReviewDecisionRequest) error {
	t.reviewRequests = append(t.reviewRequests, req)
	return nil
}

func TestSlackAnswerPortPermissionOutcomes(t *testing.T) {
	source := ports.AnswerSource{Kind: ports.AnswerSourceSlack, Responder: "Ada"}
	session := &fakeSessionView{
		id:        "session-1",
		featureID: "feature-1",
		status:    ports.SessionWaitingPermission,
		pending: []*llm.ControlRequestMessage{{
			RequestID: "permission-1",
			Request: llm.ControlRequest{
				ToolName: "Bash",
			},
		}},
	}
	target := &slackAnswerMutationTarget{}
	handler := &apiHandler{
		sessions:  fakeSessionManager{views: []ports.SessionView{session}},
		mutations: target,
	}

	got := handler.AnswerSlackPermission(ports.SlackPermissionAnswer{
		RequestID:       "permission-1",
		SourceFeatureID: "feature-1",
		Decision:        ports.SlackPermissionAllowOnce,
		Source:          source,
	})
	if got.Outcome != ports.SlackAnswerAccepted || got.Cause != nil {
		t.Fatalf("AnswerSlackPermission() = %+v; want accepted", got)
	}
	if len(target.permissionRequests) != 1 {
		t.Fatalf("permission calls = %d; want 1", len(target.permissionRequests))
	}
	req := target.permissionRequests[0]
	if req.SessionID != "session-1" || req.RequestID != "permission-1" ||
		req.Decision != decisionAllowOnce ||
		req.Source == nil || *req.Source != source {
		t.Fatalf("permission request = %+v; want session, decision, and Slack source", req)
	}

	target.permissionErr = ErrNoLongerPending
	got = handler.AnswerSlackPermission(ports.SlackPermissionAnswer{
		RequestID:       "permission-1",
		SourceFeatureID: "feature-1",
		Decision:        ports.SlackPermissionDeny,
		Source:          source,
	})
	if got.Outcome != ports.SlackAnswerNoLongerPending {
		t.Fatalf("second AnswerSlackPermission() = %+v; want no longer pending", got)
	}
}

func TestSlackAnswerPortPermissionRejectsInvalidTargets(t *testing.T) {
	tests := []struct {
		name        string
		answer      ports.SlackPermissionAnswer
		wantOutcome ports.SlackAnswerOutcome
	}{
		{
			name: "unknown request",
			answer: ports.SlackPermissionAnswer{
				RequestID:       "missing",
				SourceFeatureID: "feature-1",
				Decision:        ports.SlackPermissionAllowOnce,
				Source:          ports.AnswerSource{Kind: ports.AnswerSourceSlack},
			},
			wantOutcome: ports.SlackAnswerNoLongerPending,
		},
		{
			name: "wrong feature",
			answer: ports.SlackPermissionAnswer{
				RequestID:       "permission-1",
				SourceFeatureID: "feature-2",
				Decision:        ports.SlackPermissionAllowOnce,
				Source:          ports.AnswerSource{Kind: ports.AnswerSourceSlack},
			},
			wantOutcome: ports.SlackAnswerNoLongerPending,
		},
		{
			name: "unsupported decision",
			answer: ports.SlackPermissionAnswer{
				RequestID:       "permission-1",
				SourceFeatureID: "feature-1",
				Decision:        ports.SlackPermissionDecision("allow_remember"),
				Source:          ports.AnswerSource{Kind: ports.AnswerSourceSlack},
			},
			wantOutcome: ports.SlackAnswerFailed,
		},
	}

	session := &fakeSessionView{
		id:        "session-1",
		featureID: "feature-1",
		status:    ports.SessionWaitingPermission,
		pending: []*llm.ControlRequestMessage{{
			RequestID: "permission-1",
			Request:   llm.ControlRequest{ToolName: "Bash"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := &slackAnswerMutationTarget{}
			handler := &apiHandler{
				sessions:  fakeSessionManager{views: []ports.SessionView{session}},
				mutations: target,
			}
			got := handler.AnswerSlackPermission(test.answer)
			if got.Outcome != test.wantOutcome || got.Cause == nil {
				t.Fatalf("AnswerSlackPermission() = %+v; want %s with cause", got, test.wantOutcome)
			}
			if len(target.permissionRequests) != 0 {
				t.Fatalf("permission calls = %d; want none", len(target.permissionRequests))
			}
		})
	}
}

func TestSlackAnswerPortReviewOutcomes(t *testing.T) {
	store, f, planPath := seedReviewSessionFeature(
		t,
		feature.StatusPlanNeedsReview,
		nil,
		"plan",
		"# Plan\n",
	)
	target := &slackAnswerMutationTarget{}
	handler := &apiHandler{
		store:              store,
		mutations:          target,
		reviewSessionLocks: newReviewSessionLockSet(),
	}
	pending, err := handler.PendingSlackInputs(f.ID)
	if err != nil {
		t.Fatalf("PendingSlackInputs() error = %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending inputs = %+v; want one review", pending)
	}
	review := pending[0]
	source := ports.AnswerSource{Kind: ports.AnswerSourceSlack, Responder: "Ada"}

	got := handler.ApproveSlackReview(ports.SlackReviewApproval{
		SourceFeatureID: f.ID,
		ReviewID:        review.ReviewID,
		SourceRevision:  review.SourceRevision,
		Source:          source,
	})
	if got.Outcome != ports.SlackAnswerAccepted || got.Cause != nil {
		t.Fatalf("ApproveSlackReview() = %+v; want accepted", got)
	}
	if len(target.reviewRequests) != 1 || target.reviewRequests[0].Source == nil ||
		*target.reviewRequests[0].Source != source {
		t.Fatalf("review requests = %+v; want one request with Slack source", target.reviewRequests)
	}

	if err := os.WriteFile(planPath, []byte("# Changed\n"), 0o644); err != nil {
		t.Fatalf("change plan: %v", err)
	}
	got = handler.ApproveSlackReview(ports.SlackReviewApproval{
		SourceFeatureID: f.ID,
		ReviewID:        review.ReviewID,
		SourceRevision:  review.SourceRevision,
		Source:          source,
	})
	if got.Outcome != ports.SlackAnswerRevisionMoved {
		t.Fatalf("ApproveSlackReview() after artifact change = %+v; want revision moved", got)
	}

	if err := store.Modify(f.ID, func(current *feature.Feature) error {
		current.Status = feature.StatusImplementing
		return nil
	}); err != nil {
		t.Fatalf("close review gate: %v", err)
	}
	got = handler.ApproveSlackReview(ports.SlackReviewApproval{
		SourceFeatureID: f.ID,
		ReviewID:        review.ReviewID,
		SourceRevision:  review.SourceRevision,
		Source:          source,
	})
	if got.Outcome != ports.SlackAnswerNoLongerPending {
		t.Fatalf("ApproveSlackReview() after close = %+v; want no longer pending", got)
	}
}

func TestHandlerBindsSlackAnswerPort(t *testing.T) {
	var bound ports.SlackAnswerPort
	target := &slackAnswerMutationTarget{}
	handler := newAPIHandler(HandlerOptions{
		Mutations: target,
		BindSlackAnswerPort: func(port ports.SlackAnswerPort) {
			bound = port
		},
	})
	if bound == nil {
		t.Fatal("Slack answer port was not bound")
	}
	if bound != handler {
		t.Fatalf("bound port = %T; want handler", bound)
	}
}

func TestSlackAnswerResultClassifiesWrappedNoLongerPending(t *testing.T) {
	result := slackAnswerFailure(errors.New("outer: " + ErrNoLongerPending.Error()))
	if result.Outcome != ports.SlackAnswerFailed {
		t.Fatalf("plain text error = %+v; want failed", result)
	}
	result = slackAnswerFailure(errors.Join(errors.New("outer"), ErrNoLongerPending))
	if result.Outcome != ports.SlackAnswerNoLongerPending {
		t.Fatalf("wrapped sentinel = %+v; want no longer pending", result)
	}
}

func TestSlackAnswerPortScrubsResponderBeforeMutationAndLogging(t *testing.T) {
	const (
		token  = "xoxb-123456789012345678901234567890"
		secret = "second-secret"
	)
	hostile := "Ada " + token + " https://alice:" + secret + "@example.com/private"
	source := ports.AnswerSource{Kind: ports.AnswerSourceSlack, Responder: hostile}
	session := &fakeSessionView{
		id:        "session-1",
		featureID: "feature-1",
		pending: []*llm.ControlRequestMessage{{
			RequestID: "permission-1",
			Request:   llm.ControlRequest{ToolName: "Bash"},
		}},
	}
	target := &slackAnswerMutationTarget{}
	handler := &apiHandler{
		sessions:              fakeSessionManager{views: []ports.SessionView{session}},
		mutations:             target,
		permissionAnswerLocks: newPermissionAnswerLockSet(),
	}
	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldOutput) })

	got := handler.AnswerSlackPermission(ports.SlackPermissionAnswer{
		RequestID:       "permission-1",
		SourceFeatureID: "feature-1",
		Decision:        ports.SlackPermissionAllowOnce,
		Source:          source,
	})
	if got.Outcome != ports.SlackAnswerAccepted {
		t.Fatalf("AnswerSlackPermission() = %+v; want accepted", got)
	}
	if len(target.permissionRequests) != 1 || target.permissionRequests[0].Source == nil {
		t.Fatalf("permission requests = %+v; want one sourced request", target.permissionRequests)
	}
	responder := target.permissionRequests[0].Source.Responder
	for _, sensitive := range []string{token, secret} {
		if strings.Contains(responder, sensitive) {
			t.Fatalf("mutation responder %q leaked %q", responder, sensitive)
		}
		if strings.Contains(logs.String(), sensitive) {
			t.Fatalf("logs %q leaked %q", logs.String(), sensitive)
		}
	}
	if !strings.Contains(responder, "Ada") || !strings.Contains(logs.String(), "Ada") {
		t.Fatalf("scrubbed responder/logs = %q/%q; want non-sensitive name retained", responder, logs.String())
	}

	store, f, _ := seedReviewSessionFeature(
		t,
		feature.StatusPlanNeedsReview,
		nil,
		"plan",
		"# Plan\n",
	)
	reviewTarget := &slackAnswerMutationTarget{}
	reviewHandler := &apiHandler{
		store:              store,
		mutations:          reviewTarget,
		reviewSessionLocks: newReviewSessionLockSet(),
	}
	pending, err := reviewHandler.PendingSlackInputs(f.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingSlackInputs() = %+v, %v; want one review", pending, err)
	}
	review := reviewHandler.ApproveSlackReview(ports.SlackReviewApproval{
		SourceFeatureID: f.ID,
		ReviewID:        pending[0].ReviewID,
		SourceRevision:  pending[0].SourceRevision,
		Source:          source,
	})
	if review.Outcome != ports.SlackAnswerAccepted {
		t.Fatalf("ApproveSlackReview() = %+v; want accepted", review)
	}
	if len(reviewTarget.reviewRequests) != 1 || reviewTarget.reviewRequests[0].Source == nil {
		t.Fatalf("review requests = %+v; want one sourced request", reviewTarget.reviewRequests)
	}
	reviewResponder := reviewTarget.reviewRequests[0].Source.Responder
	for _, sensitive := range []string{token, secret} {
		if strings.Contains(reviewResponder, sensitive) {
			t.Fatalf("review responder %q leaked %q", reviewResponder, sensitive)
		}
		if strings.Contains(logs.String(), sensitive) {
			t.Fatalf("logs %q leaked %q", logs.String(), sensitive)
		}
	}
}
