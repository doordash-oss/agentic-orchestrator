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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/instancelock"
)

// TestHealthDeclaresExplicitCompatibility pins the explicit compatibility
// contract clients rely on: /api/v1/health must declare the API/schema
// contract, the runtime policy in force, and the server build identity —
// clients must never have to infer compatibility from the API major alone.
func TestHealthDeclaresExplicitCompatibility(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{
		Runtime: RuntimeIdentity{RuntimeDir: "/tmp/rt", StateDir: "/tmp/rt/features", Config: "/tmp/rt/config.yaml"},
		Owner:   instancelock.Owner{PID: 42, Version: "v1.2.3-test"},
		// Health is auth-exempt, so no token is needed for this request.
		AuthToken:             "secret-token-value",
		DisableHostValidation: true,
	})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	var body HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	decl := body.Compatibility
	if decl.APIVersion != APIVersion {
		t.Fatalf("compatibility.api_version = %q; want %q", decl.APIVersion, APIVersion)
	}
	if decl.SchemaVersion != CompatibilitySchemaVersion || decl.SchemaVersion < 1 {
		t.Fatalf("compatibility.schema_version = %d; want %d (>= 1)", decl.SchemaVersion, CompatibilitySchemaVersion)
	}
	if decl.MinClientSchema != CompatibilityMinClientSchema || decl.MinClientSchema < 1 {
		t.Fatalf("compatibility.min_client_schema = %d; want %d (>= 1)", decl.MinClientSchema, CompatibilityMinClientSchema)
	}
	if decl.RuntimePolicy != CompatibilityRuntimePolicy || decl.RuntimePolicy == "" {
		t.Fatalf("compatibility.runtime_policy = %q; want %q", decl.RuntimePolicy, CompatibilityRuntimePolicy)
	}
	if decl.ServerBuild.Version != "v1.2.3-test" {
		t.Fatalf("compatibility.server_build.version = %q; want owner version", decl.ServerBuild.Version)
	}

	// Health stays secret-free even with the declaration added.
	if raw := w.Body.String(); strings.Contains(raw, "secret-token-value") {
		t.Fatalf("health response leaks the auth token:\n%s", raw)
	}
}

// TestCompatibilityDeclarationFallbackBuildVersion pins that an unknown
// build version is reported as "dev" rather than an empty identity.
func TestCompatibilityDeclarationFallbackBuildVersion(t *testing.T) {
	t.Parallel()
	decl := NewCompatibilityDeclaration("")
	if decl.ServerBuild.Version != "dev" {
		t.Fatalf("fallback build version = %q; want dev", decl.ServerBuild.Version)
	}
	if decl.APIVersion != APIVersion {
		t.Fatalf("api_version = %q; want %q", decl.APIVersion, APIVersion)
	}
}
