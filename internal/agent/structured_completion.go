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

// resolvePhaseCompletion keeps tool transport separate from the existing
// artifact commit boundary. Rejection is returned to the pending tool call;
// it never starts a new user turn or invents a successful outcome.
func resolvePhaseCompletion(sess ports.SessionView, request llm.PhaseCompletionRequest, commit func(llm.CompletionIntent) ([]ProtocolViolation, error)) (bool, []ProtocolViolation, error) {
	responder, ok := sess.(ports.PhaseCompletionRequester)
	if !ok {
		return false, nil, fmt.Errorf("session does not support structured completion")
	}
	var violations []ProtocolViolation
	switch {
	case !request.Intent.Valid():
		violations = []ProtocolViolation{{Artifact: "complete_phase", Reason: "invalid completion request"}}
	case hasPendingRootQuestion(sess):
		violations = []ProtocolViolation{{Artifact: "complete_phase", Reason: "a user question is still awaiting an answer"}}
	case liveBackgroundTasks(sess) > 0:
		violations = []ProtocolViolation{{Artifact: "complete_phase", Reason: "delegated tasks are still running"}}
	case commit == nil:
		err := fmt.Errorf("harness completion committer is not configured")
		_ = responder.RespondToPhaseCompletion(request.RequestID, false, err.Error())
		return false, nil, err
	default:
		var err error
		violations, err = commit(request.Intent)
		if err != nil {
			_ = responder.RespondToPhaseCompletion(request.RequestID, false, err.Error())
			return false, nil, err
		}
	}
	accepted := len(violations) == 0
	message := "Phase completion accepted."
	if !accepted {
		var reasons []string
		for _, v := range violations {
			reasons = append(reasons, v.Artifact+": "+v.Reason)
		}
		message = strings.Join(reasons, "\n")
	}
	if err := responder.RespondToPhaseCompletion(request.RequestID, accepted, message); err != nil {
		if accepted {
			// The durable receipt is authoritative even if the provider exits
			// before receiving its acknowledgment. Never report a committed
			// phase as failed merely because the transport closed.
			log.Printf("session %s: phase committed but completion acknowledgment failed: %v", sess.ID(), err)
			return true, nil, nil
		}
		return false, violations, fmt.Errorf("responding to phase completion: %w", err)
	}
	return accepted, violations, nil
}
