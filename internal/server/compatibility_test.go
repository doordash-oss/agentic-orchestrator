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
	"os"
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

// TestCompatibilityPinsUnchangedByServerName is a regression guard: adding
// the optional server name to the health payload and discovery record is
// strictly additive and must not move any compatibility pin.
func TestCompatibilityPinsUnchangedByServerName(t *testing.T) {
	t.Parallel()
	if CompatibilitySchemaVersion != 1 {
		t.Fatalf("CompatibilitySchemaVersion = %d; want 1", CompatibilitySchemaVersion)
	}
	if CompatibilityMinClientSchema != 1 {
		t.Fatalf("CompatibilityMinClientSchema = %d; want 1", CompatibilityMinClientSchema)
	}
	if CompatibilityRuntimePolicy != "loopback-bearer-v1" {
		t.Fatalf("CompatibilityRuntimePolicy = %q; want loopback-bearer-v1", CompatibilityRuntimePolicy)
	}
	if discoverySchemaVersion != 1 {
		t.Fatalf("discoverySchemaVersion = %d; want 1", discoverySchemaVersion)
	}
}

// TestHealthCarriesOptionalServerName pins the additive, optional top-level
// "name" field on /api/v1/health: present when a name is resolved, omitted
// when it is not.
func TestHealthCarriesOptionalServerName(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{
		Runtime:               RuntimeIdentity{RuntimeDir: "/tmp/rt", StateDir: "/tmp/rt/features", Config: "/tmp/rt/config.yaml"},
		AuthToken:             "secret-token-value",
		Name:                  "frothy-macchiato",
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
	if body.Name != "frothy-macchiato" {
		t.Fatalf("health name = %q; want frothy-macchiato", body.Name)
	}

	unnamed := NewHandler(HandlerOptions{
		Runtime:               RuntimeIdentity{RuntimeDir: "/tmp/rt", StateDir: "/tmp/rt/features", Config: "/tmp/rt/config.yaml"},
		AuthToken:             "secret-token-value",
		DisableHostValidation: true,
	})
	w2 := httptest.NewRecorder()
	unnamed.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if strings.Contains(w2.Body.String(), `"name"`) {
		t.Fatalf("unnamed health response carries a name field:\n%s", w2.Body.String())
	}
}

// TestHealthAndDiscoverySkewTolerance pins both skew directions for the
// optional name: (a) payloads/records without name still parse (old-server
// case), and (b) records carrying name still satisfy consumers built for the
// pre-name contract (new-server case — Go handlers and parsers are
// non-strict).
func TestHealthAndDiscoverySkewTolerance(t *testing.T) {
	t.Parallel()

	// Old server: no name anywhere.
	var oldHealth HealthResponse
	if err := json.Unmarshal([]byte(`{"api_version":"v1","status":"ok"}`), &oldHealth); err != nil {
		t.Fatalf("decode pre-name health payload: %v", err)
	}
	if oldHealth.Name != "" {
		t.Fatalf("pre-name health payload decoded name = %q; want empty", oldHealth.Name)
	}
	var oldRecord DiscoveryRecord
	oldJSON := `{"schema_version":1,"api_version":"v1","base_url":"http://127.0.0.1:51001","start_mode":"server"}`
	if err := json.Unmarshal([]byte(oldJSON), &oldRecord); err != nil {
		t.Fatalf("decode pre-name discovery record: %v", err)
	}
	if oldRecord.Name != "" || oldRecord.SchemaVersion != 1 {
		t.Fatalf("pre-name discovery record decoded as %+v; want empty name, schema 1", oldRecord)
	}

	// New server: name rides along and existing consumers still parse.
	newJSON := `{"schema_version":1,"api_version":"v1","base_url":"http://127.0.0.1:51001","name":"frothy-macchiato","start_mode":"server"}`
	var newRecord DiscoveryRecord
	if err := json.Unmarshal([]byte(newJSON), &newRecord); err != nil {
		t.Fatalf("decode named discovery record: %v", err)
	}
	if newRecord.Name != "frothy-macchiato" {
		t.Fatalf("named discovery record name = %q; want frothy-macchiato", newRecord.Name)
	}

	// Publishing a named record round-trips through ReadDiscovery.
	runtimeDir := t.TempDir()
	rec := newDiscoveryRecord(RuntimeIdentity{RuntimeDir: runtimeDir, StateDir: runtimeDir + "/features", Config: runtimeDir + "/config.yaml"}, NewLaunchPolicy(nil, false))
	rec.Name = "frothy-macchiato"
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	loaded, err := ReadDiscovery(runtimeDir)
	if err != nil {
		t.Fatalf("ReadDiscovery() error = %v", err)
	}
	if loaded.Name != "frothy-macchiato" {
		t.Fatalf("reloaded discovery name = %q; want frothy-macchiato", loaded.Name)
	}

	// Unnamed records stay byte-clean: the name key never appears.
	rec.Name = ""
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery(unnamed) error = %v", err)
	}
	data, err := os.ReadFile(DiscoveryPath(runtimeDir))
	if err != nil {
		t.Fatalf("read unnamed discovery file: %v", err)
	}
	if strings.Contains(string(data), `"name"`) {
		t.Fatalf("unnamed discovery file carries a name key:\n%s", data)
	}
}
