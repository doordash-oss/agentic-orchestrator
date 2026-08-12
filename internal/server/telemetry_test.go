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
	"time"
)

type httpMetricCapture struct {
	routes   []string
	statuses []int
}

func (c *httpMetricCapture) ObserveHTTPRequest(route, method string, status int, duration time.Duration, requestBytes, responseBytes int64) {
	c.routes = append(c.routes, method+" "+route)
	c.statuses = append(c.statuses, status)
}

func TestHTTPMetricsNormalizesIDsAndExcludesStreamingRoutes(t *testing.T) {
	capture := &httpMetricCapture{}
	handler := withHTTPMetrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}), capture)
	for _, target := range []string{
		"/api/v1/features/secret-id/actions/publish",
		"/api/v1/features/secret-id/repositories/private-repo/diff",
		"/api/v1/features/secret-id/runs/42/artifacts/private-artifact",
		"/api/v1/features/secret-id/not-a-route/private-value",
		"/private/request/path",
		"/api/v1/health",
		"/api/v1/events",
		"/api/v1/sessions/session-secret/output/stream",
	} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, target, nil))
	}
	want := []string{
		"POST /api/v1/features/{feature_id}/actions/{action}",
		"POST /api/v1/features/{feature_id}/repositories/{repo_name}/diff",
		"POST /api/v1/features/{feature_id}/runs/{run_number}/artifacts/{artifact_id}",
		"POST /api/v1/features/{feature_id}/{other}",
		"POST /{other}",
	}
	if len(capture.routes) != len(want) {
		t.Fatalf("routes=%v", capture.routes)
	}
	for i := range want {
		if capture.routes[i] != want[i] || capture.statuses[i] != http.StatusCreated {
			t.Fatalf("routes=%v statuses=%v", capture.routes, capture.statuses)
		}
	}
}
