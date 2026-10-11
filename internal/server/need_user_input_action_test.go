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
	"net/http"
	"testing"
)

type needUserInputResumeTarget struct {
	MutationTarget
	resumeCalls int
}

func (t *needUserInputResumeTarget) ResumeNeedUserInput(featureID string) (NeedUserInputResumeResponse, error) {
	t.resumeCalls++
	return NeedUserInputResumeResponse{FeatureID: featureID, Result: "resumed"}, nil
}

func TestNeedUserInputActionRejectsRetiredAbortPayload(t *testing.T) {
	t.Parallel()
	target := &needUserInputResumeTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		DisableHostValidation: true,
	})

	w := postTrustedJSON(handler, "/api/v1/features/feat-resume/actions/need-user-input", map[string]any{
		"decision": "abort",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	if target.resumeCalls != 0 {
		t.Fatalf("resume calls = %d; want zero for retired abort payload", target.resumeCalls)
	}
}
