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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type waiveMutationTarget struct {
	MutationTarget
	received *[]TestingContractWaiveRequest
}

func (t waiveMutationTarget) WaiveTestingContractItems(featureID string, req TestingContractWaiveRequest) (TestingContractWaiveResponse, error) {
	if t.received != nil {
		*t.received = append(*t.received, req)
	}
	return TestingContractWaiveResponse{FeatureID: featureID, ContractRevision: 2, WaivedItems: req.ItemIDs}, nil
}

func TestTestingContractWaiveAction(t *testing.T) {
	t.Parallel()
	post := func(t *testing.T, body any, trusted bool) (*http.Response, []TestingContractWaiveRequest) {
		t.Helper()
		var received []TestingContractWaiveRequest
		handler := NewHandler(HandlerOptions{
			Mutations:             waiveMutationTarget{received: &received},
			DisableHostValidation: true,
		})
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/features/feat-1/actions/testing-contract-waive", bytes.NewReader(payload))
		req.Header.Set("Content-Type", contentTypeJSON)
		if trusted {
			req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Result(), received
	}

	t.Run("records the waiver", func(t *testing.T) {
		t.Parallel()
		resp, received := post(t, map[string]any{"item_ids": []string{"visual_1", "manual_2"}, "reason": "no signed-in Slack browser on the VM"}, true)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if len(received) != 1 || len(received[0].ItemIDs) != 2 || received[0].Reason == "" {
			t.Fatalf("received = %+v", received)
		}
		var out TestingContractWaiveResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.Result != "waived" || out.FeatureID != "feat-1" || out.ContractRevision != 2 || len(out.WaivedItems) != 2 {
			t.Fatalf("response = %+v", out)
		}
	})
	t.Run("rejects empty items and reason", func(t *testing.T) {
		t.Parallel()
		for _, body := range []map[string]any{{"item_ids": []string{}, "reason": "r"}, {"item_ids": []string{"x"}, "reason": " "}} {
			resp, received := post(t, body, true)
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest || len(received) != 0 {
				t.Fatalf("body %v: status = %d, received = %d", body, resp.StatusCode, len(received))
			}
		}
	})
	t.Run("requires the trusted client header", func(t *testing.T) {
		t.Parallel()
		resp, received := post(t, map[string]any{"item_ids": []string{"x"}, "reason": "r"}, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || len(received) != 0 {
			t.Fatalf("status = %d, received = %d; want 403 and no mutation", resp.StatusCode, len(received))
		}
	})
}
