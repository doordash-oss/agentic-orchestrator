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

package main

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type fakeSlackAnswerPort struct {
	permission ports.SlackPermissionAnswer
	review     ports.SlackReviewApproval
}

func (p *fakeSlackAnswerPort) AnswerSlackPermission(answer ports.SlackPermissionAnswer) ports.SlackAnswerResult {
	p.permission = answer
	return ports.SlackAnswerResult{Outcome: ports.SlackAnswerAccepted}
}

func (p *fakeSlackAnswerPort) ApproveSlackReview(approval ports.SlackReviewApproval) ports.SlackAnswerResult {
	p.review = approval
	return ports.SlackAnswerResult{Outcome: ports.SlackAnswerRevisionMoved}
}

func TestSlackAnswerRelayBindsAfterConstruction(t *testing.T) {
	relay := &slackAnswerRelay{}
	before := relay.AnswerSlackPermission(ports.SlackPermissionAnswer{RequestID: "permission-1"})
	if before.Outcome != ports.SlackAnswerFailed || before.Cause == nil {
		t.Fatalf("unbound permission result = %+v; want failed with cause", before)
	}
	target := &fakeSlackAnswerPort{}
	relay.bind(target)
	permission := ports.SlackPermissionAnswer{
		RequestID:       "permission-1",
		SourceFeatureID: "feature-1",
		Decision:        ports.SlackPermissionAllowOnce,
	}
	if got := relay.AnswerSlackPermission(permission); got.Outcome != ports.SlackAnswerAccepted {
		t.Fatalf("bound permission result = %+v; want accepted", got)
	}
	if target.permission != permission {
		t.Fatalf("forwarded permission = %+v; want %+v", target.permission, permission)
	}

	review := ports.SlackReviewApproval{
		SourceFeatureID: "feature-1",
		ReviewID:        "review-1",
		SourceRevision:  "revision-1",
	}
	if got := relay.ApproveSlackReview(review); got.Outcome != ports.SlackAnswerRevisionMoved {
		t.Fatalf("bound review result = %+v; want revision moved", got)
	}
	if target.review != review {
		t.Fatalf("forwarded review = %+v; want %+v", target.review, review)
	}
}
