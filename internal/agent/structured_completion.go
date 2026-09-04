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

package agent

import (
	"fmt"
	"log"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func phaseCompletionRequests(sess ports.SessionView) <-chan llm.PhaseCompletionRequest {
	if s, ok := sess.(ports.PhaseCompletionRequester); ok && s.UsesStructuredCompletion() {
		return s.PhaseCompletionRequests()
	}
	return nil
}

// phaseCompletionResolution is the harness decision on one complete_phase call.
type phaseCompletionResolution struct {
	// Accepted reports that the outcome was committed.
	Accepted bool
	// Deferred reports a rejection caused by harness state the model must wait
	// out (an unanswered question, live delegated tasks) rather than an
	// artifact defect. Deferred rejections do not consume the nudge budget.
	Deferred bool
	// Violations are the artifact defects behind a non-deferred rejection.
	Violations []ProtocolViolation
}

// resolvePhaseCompletion keeps tool transport separate from the existing
// artifact commit boundary. Rejection is returned to the pending tool call;
// it never starts a new user turn or invents a successful outcome.
func resolvePhaseCompletion(sess ports.SessionView, request llm.PhaseCompletionRequest, commit func(llm.CompletionIntent) ([]ProtocolViolation, error)) (phaseCompletionResolution, error) {
	responder, ok := sess.(ports.PhaseCompletionRequester)
	if !ok {
		return phaseCompletionResolution{}, fmt.Errorf("session does not support structured completion")
	}
	var resolution phaseCompletionResolution
	tool := llm.CompletePhaseToolName
	switch {
	case !request.Intent.Valid():
		resolution.Violations = []ProtocolViolation{{Artifact: tool, Reason: "invalid completion request"}}
	case hasPendingRootQuestion(sess):
		resolution.Deferred = true
		resolution.Violations = []ProtocolViolation{{Artifact: tool, Reason: "a user question is still awaiting an answer; wait for it before retrying"}}
	case liveBackgroundTasks(sess) > 0:
		resolution.Deferred = true
		resolution.Violations = []ProtocolViolation{{Artifact: tool, Reason: "delegated tasks are still running; wait for them to finish before retrying"}}
	case commit == nil:
		err := fmt.Errorf("harness completion committer is not configured")
		_ = responder.RespondToPhaseCompletion(request.RequestID, false, err.Error())
		return phaseCompletionResolution{}, err
	default:
		violations, err := commit(request.Intent)
		if err != nil {
			_ = responder.RespondToPhaseCompletion(request.RequestID, false, err.Error())
			return phaseCompletionResolution{}, err
		}
		resolution.Violations = violations
	}
	resolution.Accepted = len(resolution.Violations) == 0
	message := "Phase completion accepted."
	if !resolution.Accepted {
		var reasons []string
		for _, v := range resolution.Violations {
			reasons = append(reasons, v.Artifact+": "+v.Reason)
		}
		message = strings.Join(reasons, "\n")
	}
	if err := responder.RespondToPhaseCompletion(request.RequestID, resolution.Accepted, message); err != nil {
		if resolution.Accepted {
			// The durable receipt is authoritative even if the provider exits
			// before receiving its acknowledgment. Never report a committed
			// phase as failed merely because the transport closed.
			log.Printf("session %s: phase committed but completion acknowledgment failed: %v", sess.ID(), err)
			return phaseCompletionResolution{Accepted: true}, nil
		}
		return resolution, fmt.Errorf("responding to phase completion: %w", err)
	}
	return resolution, nil
}
