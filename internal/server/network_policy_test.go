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
	"net/http/httptest"
	"testing"
)

// TestNetworkPolicyRuntimePolicyInHealth pins the declared compatibility
// policy per bind mode: network binds declare network-bearer-v1 and the
// default (loopback) keeps loopback-bearer-v1.
func TestNetworkPolicyRuntimePolicyInHealth(t *testing.T) {
	t.Parallel()
	runtimePolicyFromHealth := func(t *testing.T, opts HandlerOptions) string {
		t.Helper()
		handler := NewHandler(opts)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
		resp := w.Result()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("health status = %d; want 200", resp.StatusCode)
		}
		var body HealthResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode health: %v", err)
		}
		return body.Compatibility.RuntimePolicy
	}
	if got := runtimePolicyFromHealth(t, HandlerOptions{AuthToken: "tok", DisableHostValidation: true, RuntimePolicy: CompatibilityNetworkRuntimePolicy}); got != CompatibilityNetworkRuntimePolicy {
		t.Fatalf("network bind runtime_policy = %q; want %q", got, CompatibilityNetworkRuntimePolicy)
	}
	if got := runtimePolicyFromHealth(t, HandlerOptions{AuthToken: "tok", DisableHostValidation: true}); got != CompatibilityRuntimePolicy {
		t.Fatalf("loopback bind runtime_policy = %q; want %q", got, CompatibilityRuntimePolicy)
	}
}

// TestNetworkPolicyAcceptsArbitraryHost pins the network-policy Host rule:
// any Host header is accepted because the bearer token is the real auth.
func TestNetworkPolicyAcceptsArbitraryHost(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{AuthToken: testAuthToken, RuntimePolicy: CompatibilityNetworkRuntimePolicy})
	for _, host := range []string{"attacker.example", "10.1.2.3:8080", "desktop.lan:9090"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Host = host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Result().StatusCode != http.StatusOK {
			t.Fatalf("network-policy status for Host %q = %d; want 200", host, w.Result().StatusCode)
		}
	}
}

// TestNetworkPolicyKeepsOriginAndAuthGuards pins the unchanged parts of the
// request-guard matrix under the network policy: non-loopback browser Origins
// are still rejected, loopback Origins still echo, and bearer auth plus the
// mutation client header are still enforced.
func TestNetworkPolicyKeepsOriginAndAuthGuards(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{
		AuthToken:     testAuthToken,
		Mutations:     permissionAnswerMutationTarget{},
		RuntimePolicy: CompatibilityNetworkRuntimePolicy,
	})
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// Non-loopback Origin remains rejected on the mutation preflight.
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/permissions/answer", nil)
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	if status := serve(req).Result().StatusCode; status != http.StatusForbidden {
		t.Fatalf("preflight with non-loopback Origin status = %d; want 403", status)
	}

	// Loopback Origin echo is unchanged.
	req = httptest.NewRequest(http.MethodOptions, "/api/v1/permissions/answer", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "Content-Type, X-Agentico-Client")
	resp := serve(req).Result()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight with loopback Origin status = %d; want 204", resp.StatusCode)
	}
	if allow := resp.Header.Get("Access-Control-Allow-Origin"); allow != "http://localhost:3000" {
		t.Fatalf("Access-Control-Allow-Origin = %q; want the loopback echo", allow)
	}

	// Bearer auth still gates mutations.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/permissions/answer", nil)
	if status := serve(req).Result().StatusCode; status != http.StatusUnauthorized {
		t.Fatalf("mutation without bearer status = %d; want 401", status)
	}

	// The mutation client header is still enforced past the bearer check.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/permissions/answer", nil)
	req.Header.Set("Authorization", "Bearer "+testAuthToken)
	if status := serve(req).Result().StatusCode; status != http.StatusForbidden {
		t.Fatalf("mutation without client header status = %d; want 403", status)
	}
}

// TestLoopbackPolicyRequestGuardPostureRegression pins the flagless
// (loopback-policy) request-guard posture so this phase's wiring cannot move
// it: bad Host 403, loopback Host passes host validation, missing bearer 401,
// non-loopback Origin 403.
func TestLoopbackPolicyRequestGuardPostureRegression(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{
		AuthToken: testAuthToken,
		Mutations: permissionAnswerMutationTarget{},
	})
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Host = "attacker.example"
	if status := serve(req).Result().StatusCode; status != http.StatusForbidden {
		t.Fatalf("loopback policy bad-Host status = %d; want 403", status)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/features", nil)
	req.Host = "127.0.0.1:8080"
	if status := serve(req).Result().StatusCode; status != http.StatusUnauthorized {
		t.Fatalf("loopback policy missing-bearer status = %d; want 401", status)
	}

	req = httptest.NewRequest(http.MethodOptions, "/api/v1/permissions/answer", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	if status := serve(req).Result().StatusCode; status != http.StatusForbidden {
		t.Fatalf("loopback policy bad-Origin status = %d; want 403", status)
	}
}
