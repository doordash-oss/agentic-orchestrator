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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
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
		name        string
		action      string
		err         error
		wantCode    string
		wantTitle   string
		wantSummary string
	}{
		{
			name:        "resume with open gate",
			action:      "resume",
			err:         fmt.Errorf("%w: %s", feature.ErrNeedUserInputGateOpen, gatePath),
			wantCode:    string(errcat.NeedUserInputOpen),
			wantTitle:   "Waiting on user input",
			wantSummary: "The feature is waiting on an open user input request.",
		},
		{
			name:        "start with open gate",
			action:      "start",
			err:         fmt.Errorf("%w: %s", feature.ErrNeedUserInputGateOpen, gatePath),
			wantCode:    string(errcat.NeedUserInputOpen),
			wantTitle:   "Waiting on user input",
			wantSummary: "The feature is waiting on an open user input request.",
		},
		{
			name:        "start while finalizing",
			action:      "start",
			err:         feature.ErrPhaseFinalizing,
			wantCode:    string(errcat.PhaseFinalizing),
			wantTitle:   "Phase finalizing",
			wantSummary: "The feature is finalizing the current phase.",
		},
		{
			name:        "restart rejected transition",
			action:      "restart",
			err:         fmt.Errorf("restart phase: %w", transitionErr),
			wantCode:    string(errcat.InvalidTransition),
			wantTitle:   "Invalid transition",
			wantSummary: "The action is not valid in the feature's current state.",
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
			if body.Error.Class != ErrorClass(errcat.ClassBlocking) {
				t.Fatalf("error class = %q; want %q", body.Error.Class, errcat.ClassBlocking)
			}
			if body.Error.Title != tc.wantTitle || body.Error.Summary != tc.wantSummary {
				t.Fatalf("error = %+v; want title %q and summary %q", body.Error, tc.wantTitle, tc.wantSummary)
			}
		})
	}
}
