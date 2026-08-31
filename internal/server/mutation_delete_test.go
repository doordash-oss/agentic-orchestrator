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
	"encoding/json"
	"net/http"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

type cascadeDeleteMutationTarget struct {
	preflightMutationTarget
	resp DeleteFeatureResponse
}

func (t *cascadeDeleteMutationTarget) DeleteFeature(featureID string, _ GuardedFeatureActionRequest) (DeleteFeatureResponse, error) {
	t.resp.FeatureID = featureID
	return t.resp, nil
}

func TestDeleteActionAnnotatesNonTerminalCascadeOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		resp          DeleteFeatureResponse
		wantRetryable bool
		wantDiagCode  string
	}{
		{
			name: "cleanup pending without diagnostics gains fallback",
			resp: DeleteFeatureResponse{
				OperationID: "cascade:feat", Status: feature.CascadeDeleteCleanupPending,
			},
			wantRetryable: true,
			wantDiagCode:  "cascade_cleanup_pending",
		},
		{
			name: "attention required keeps its diagnostics",
			resp: DeleteFeatureResponse{
				OperationID: "cascade:feat", Status: feature.CascadeDeleteAttentionRequired,
				Diagnostics: []CascadeDiagnostic{{Code: "external_ref_moved", Message: "moved"}},
			},
			wantRetryable: true,
			wantDiagCode:  "external_ref_moved",
		},
		{
			name: "completed stays a plain success",
			resp: DeleteFeatureResponse{
				OperationID: "cascade:feat", Status: feature.CascadeDeleteCompleted,
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := &cascadeDeleteMutationTarget{resp: tc.resp}
			handler := NewHandler(HandlerOptions{
				Mutations:             target,
				AuthToken:             testAuthToken,
				DisableHostValidation: true,
			})

			w := postTrustedAuthedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/delete", map[string]any{})
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			retryable, present := body["retryable"]
			if tc.wantRetryable != present || (present && retryable != true) {
				t.Fatalf("retryable = %v (present=%v), want %v", retryable, present, tc.wantRetryable)
			}
			if body["status"] != string(tc.resp.Status) {
				t.Fatalf("status = %v, want %q", body["status"], tc.resp.Status)
			}
			if tc.wantDiagCode == "" {
				return
			}
			diags, ok := body["diagnostics"].([]any)
			if !ok || len(diags) != 1 {
				t.Fatalf("diagnostics = %v, want one entry", body["diagnostics"])
			}
			if code := diags[0].(map[string]any)["code"]; code != tc.wantDiagCode {
				t.Fatalf("diagnostic code = %v, want %q", code, tc.wantDiagCode)
			}
		})
	}
}
