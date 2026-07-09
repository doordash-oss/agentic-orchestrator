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
	"net/http/httptest"
	"testing"
)

func TestBearerTokenRequiredForAPIReads(t *testing.T) {
	t.Parallel()

	handler := NewHandler(HandlerOptions{AuthToken: "test-token"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want 401", w.Result().StatusCode)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("valid token status = %d, want 200", w.Result().StatusCode)
	}
}

func TestBearerTokenAcceptedForSSEQueryFallback(t *testing.T) {
	t.Parallel()

	handler := NewHandler(HandlerOptions{AuthToken: "test-token"})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/events?access_token=test-token&heartbeat_ms=1000")
	if err != nil {
		t.Fatalf("GET SSE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE query token status = %d, want 200", resp.StatusCode)
	}
}
