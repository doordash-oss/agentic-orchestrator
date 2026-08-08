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
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// stateErrorMutationTarget returns the same state-based rejection from every
// lifecycle verb so the classification can be asserted per route.
type stateErrorMutationTarget struct {
	MutationTarget
	err error
}

func (t *stateErrorMutationTarget) StartFeature(string) (FeatureStartResponse, error) {
	return FeatureStartResponse{}, t.err
}

func (t *stateErrorMutationTarget) ResumeFeature(string) (FeatureStartResponse, error) {
	return FeatureStartResponse{}, t.err
}

func (t *stateErrorMutationTarget) RestartFeature(string, RestartFeatureRequest) (FeatureRestartResponse, error) {
	return FeatureRestartResponse{}, t.err
}

// TestLifecycleActionsClassifyStateRejections pins state-based rejections to
// 409 with their own machine codes. Reporting them as 400 bad_request made
// clients ask the user to correct input they never typed.
func TestLifecycleActionsClassifyStateRejections(t *testing.T) {
	t.Parallel()
	gatePath := "/state/feat/run-1/need-user-input.yaml"
	transitionErr := (&feature.Feature{Status: feature.StatusNeedUserInput}).
		Transition(feature.StatusImplementReady)

	tests := []struct {
		name     string
		action   string
		err      error
		wantCode string
		wantMsg  string
	}{
		{
			name:     "resume with open gate",
			action:   "resume",
			err:      fmt.Errorf("%w: %s", feature.ErrNeedUserInputGateOpen, gatePath),
			wantCode: errCodeNeedUserInputOpen,
			wantMsg:  gatePath,
		},
		{
			name:     "start with open gate",
			action:   "start",
			err:      fmt.Errorf("%w: %s", feature.ErrNeedUserInputGateOpen, gatePath),
			wantCode: errCodeNeedUserInputOpen,
			wantMsg:  gatePath,
		},
		{
			name:     "start while finalizing",
			action:   "start",
			err:      feature.ErrPhaseFinalizing,
			wantCode: errCodePhaseFinalizing,
			wantMsg:  "finalizing",
		},
		{
			name:     "restart rejected transition",
			action:   "restart",
			err:      fmt.Errorf("restart phase: %w", transitionErr),
			wantCode: errCodeInvalidTransition,
			wantMsg:  "invalid transition from NeedUserInput",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler := NewHandler(HandlerOptions{
				Mutations:             &stateErrorMutationTarget{err: tc.err},
				DisableHostValidation: true,
			})

			w := postTrustedJSON(handler,
				"/api/v1/features/"+fixtureFeatureID+"/actions/"+tc.action, map[string]any{})
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d body=%s; want 409", w.Code, w.Body.String())
			}
			body := decodeErrorBody(t, w)
			if body.Error.Code != tc.wantCode {
				t.Fatalf("error code = %q; want %q", body.Error.Code, tc.wantCode)
			}
			if !strings.Contains(body.Error.Message, tc.wantMsg) {
				t.Fatalf("error message = %q; want it to name %q", body.Error.Message, tc.wantMsg)
			}
		})
	}
}
