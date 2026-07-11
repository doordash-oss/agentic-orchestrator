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
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// httpMethodGet and httpMethodPost are the lowercase "get"/"post" method
// literals reused across the documented-route table below.
const (
	httpMethodGet  = "get"
	httpMethodPost = "post"
)

type openAPISpec struct {
	OpenAPI string `yaml:"openapi"`
	Info    struct {
		Version string `yaml:"version"`
	} `yaml:"info"`
	Security   []map[string][]string                  `yaml:"security"`
	Paths      map[string]map[string]openAPIOperation `yaml:"paths"`
	Components struct {
		SecuritySchemes map[string]map[string]any  `yaml:"securitySchemes"`
		Responses       map[string]openAPIResponse `yaml:"responses"`
		Schemas         map[string]any             `yaml:"schemas"`
	} `yaml:"components"`
}

type openAPIOperation struct {
	OperationID string                     `yaml:"operationId"`
	Security    []map[string][]string      `yaml:"security"`
	Parameters  []openAPIParameter         `yaml:"parameters"`
	Responses   map[string]openAPIResponse `yaml:"responses"`
}

type openAPIParameter struct {
	Ref      string `yaml:"$ref"`
	Name     string `yaml:"name"`
	In       string `yaml:"in"`
	Required bool   `yaml:"required"`
}

type openAPIResponse struct {
	Ref         string         `yaml:"$ref"`
	Description string         `yaml:"description"`
	Content     map[string]any `yaml:"content"`
}

type documentedRoute struct {
	method   string
	path     string
	mutation bool
	sse      bool
}

func TestOpenAPISpecCoversServerRoutes(t *testing.T) {
	spec := loadOpenAPISpec(t)
	if spec.OpenAPI != "3.1.0" {
		t.Fatalf("openapi version = %q, want 3.1.0", spec.OpenAPI)
	}
	if spec.Info.Version != APIVersion {
		t.Fatalf("info.version = %q, want server API version %q", spec.Info.Version, APIVersion)
	}
	if _, ok := spec.Components.SecuritySchemes["bearerAuth"]; !ok {
		t.Fatal("securitySchemes.bearerAuth missing")
	}
	if _, ok := spec.Components.SecuritySchemes["sseAccessToken"]; !ok {
		t.Fatal("securitySchemes.sseAccessToken missing")
	}

	expected := make(map[string]documentedRoute)
	for _, route := range documentedServerRoutes() {
		expected[routeKey(route.method, route.path)] = route
	}
	seenOperationIDs := map[string]string{}
	for path, methods := range spec.Paths {
		if strings.Contains(strings.ToLower(path), "mcp") {
			t.Fatalf("OpenAPI path %q documents removed MCP surface", path)
		}
		for method, op := range methods {
			if !isHTTPMethod(method) {
				continue
			}
			key := routeKey(method, path)
			route, ok := expected[key]
			if !ok {
				t.Fatalf("OpenAPI documents unexpected route %s %s", strings.ToUpper(method), path)
			}
			delete(expected, key)
			if op.OperationID == "" {
				t.Fatalf("%s %s missing operationId", strings.ToUpper(method), path)
			}
			if prior := seenOperationIDs[op.OperationID]; prior != "" {
				t.Fatalf("operationId %q is reused by %s and %s %s", op.OperationID, prior, strings.ToUpper(method), path)
			}
			seenOperationIDs[op.OperationID] = strings.ToUpper(method) + " " + path
			// /api/v1/health is liveness-only and intentionally exempt from
			// bearer auth (see authRequiredPath in handler.go) so that a
			// stale discovery token can't make a live server look dead.
			if path != apiPathHealth && !hasSecurity(effectiveSecurity(spec, op), "bearerAuth") {
				t.Fatalf("%s %s does not require bearerAuth", strings.ToUpper(method), path)
			}
			if route.sse && !hasSecurity(effectiveSecurity(spec, op), "sseAccessToken") {
				t.Fatalf("%s %s does not document SSE access_token fallback", strings.ToUpper(method), path)
			}
			if route.mutation && !hasTrustedMutationHeader(op) {
				t.Fatalf("%s %s missing X-Agentico-Client mutation header parameter", strings.ToUpper(method), path)
			}
		}
	}
	if len(expected) > 0 {
		missing := make([]string, 0, len(expected))
		for _, route := range expected {
			missing = append(missing, strings.ToUpper(route.method)+" "+route.path)
		}
		t.Fatalf("OpenAPI missing server routes: %s", strings.Join(missing, ", "))
	}
}

func TestOpenAPIDeclaresHardeningSchemas(t *testing.T) {
	spec := loadOpenAPISpec(t)
	for _, schema := range []string{"ResponseMeta", "SSEEvent", "Resource", "SessionOutputResponse", "SessionOutputChunk", "ErrorResponse"} {
		if _, ok := spec.Components.Schemas[schema]; !ok {
			t.Fatalf("components.schemas.%s missing", schema)
		}
	}
	assertSchemaProperties(t, spec, "ResponseMeta", "revision", "generated_at", "as_of_seq")
	assertSchemaProperties(t, spec, "SSEEvent", "seq", "epoch", "kind", "resource", "resource_version", "snapshot_required")
	assertSchemaProperties(t, spec, "Resource", "type", "id", "feature_id", "phase")
	assertSchemaProperties(t, spec, "SessionOutputResponse", "session_id", "offset", "next_offset", "size", "data", "truncated", "done")
}

func TestOpenAPIRepresentativeResponsesAreDeclared(t *testing.T) {
	spec := loadOpenAPISpec(t)
	handler := NewHandler(HandlerOptions{AuthToken: testAuthToken, DisableHostValidation: true})
	cases := []struct {
		name        string
		method      string
		specPath    string
		requestPath string
		auth        bool
		wantStatus  int
		wantType    string
	}{
		{
			name:        "authorized health",
			method:      http.MethodGet,
			specPath:    apiPathHealth,
			requestPath: apiPathHealth,
			auth:        true,
			wantStatus:  http.StatusOK,
			wantType:    contentTypeJSON,
		},
		{
			name:        "authorized feature list",
			method:      http.MethodGet,
			specPath:    apiPathFeatures,
			requestPath: apiPathFeatures,
			auth:        true,
			wantStatus:  http.StatusOK,
			wantType:    contentTypeJSON,
		},
		{
			name:        "missing token",
			method:      http.MethodGet,
			specPath:    apiPathFeatures,
			requestPath: apiPathFeatures,
			wantStatus:  http.StatusUnauthorized,
			wantType:    contentTypeJSON,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.requestPath, nil)
			if tc.auth {
				req.Header.Set("Authorization", "Bearer test-token")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			resp := w.Result()
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			op := lookupOpenAPIOperation(t, spec, tc.method, tc.specPath)
			declared := declaredOpenAPIResponse(t, op, strconv.Itoa(tc.wantStatus))
			if len(responseContentTypes(spec, declared)) > 0 && !declaresContentType(spec, declared, tc.wantType) {
				t.Fatalf("%s %s status %d does not declare %s content", tc.method, tc.specPath, tc.wantStatus, tc.wantType)
			}
			if tc.wantType != "" && !strings.HasPrefix(resp.Header.Get("Content-Type"), tc.wantType) {
				t.Fatalf("Content-Type = %q, want %s", resp.Header.Get("Content-Type"), tc.wantType)
			}
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode JSON response: %v", err)
			}
			if body["api_version"] != APIVersion {
				t.Fatalf("api_version = %v, want %s", body["api_version"], APIVersion)
			}
		})
	}
}

func TestTopLevelRoutesCoverAllDocumentedPrefixes(t *testing.T) {
	registered := map[string]bool{}
	for _, r := range topLevelServerRoutes {
		registered[r.pattern] = true
	}
	for _, route := range documentedServerRoutes() {
		prefix := topLevelPatternForPath(route.path)
		if !registered[prefix] {
			t.Errorf("documented route %s %s has no top-level mux registration for pattern %q", strings.ToUpper(route.method), route.path, prefix)
		}
	}

	// Reverse direction: every registered top-level pattern must be reachable
	// by at least one documented path — otherwise a route added to
	// topLevelServerRoutes without a matching OpenAPI entry passes silently.
	documentedPrefixes := map[string]bool{}
	for _, route := range documentedServerRoutes() {
		documentedPrefixes[topLevelPatternForPath(route.path)] = true
	}
	for _, r := range topLevelServerRoutes {
		if !documentedPrefixes[r.pattern] {
			t.Errorf("registered mux pattern %q has no documented OpenAPI route mapping to it", r.pattern)
		}
	}
}

// topLevelPatternForPath maps an expanded OpenAPI path to the mux pattern
// that would serve it, mirroring routes()'s registration granularity.
func topLevelPatternForPath(path string) string {
	switch {
	case path == apiPathHealth:
		return apiPathHealth
	case path == apiPathFeatures:
		return apiPathFeatures
	case strings.HasPrefix(path, "/api/v1/features/"):
		return "/api/v1/features/"
	case strings.HasPrefix(path, apiPathConfigRuntime):
		return apiPathConfigRuntime
	case path == apiPathWorkspaceBrowse:
		return apiPathWorkspaceBrowse
	case path == apiPathCatalogModels:
		return apiPathCatalogModels
	case path == apiPathPrompts:
		return apiPathPrompts
	case strings.HasPrefix(path, "/api/v1/prompts/"):
		return "/api/v1/prompts/"
	case path == apiPathPermissions:
		return apiPathPermissions
	case strings.HasPrefix(path, "/api/v1/permissions/"):
		return "/api/v1/permissions/"
	case path == apiPathSessions:
		return apiPathSessions
	case strings.HasPrefix(path, "/api/v1/sessions/"):
		return "/api/v1/sessions/"
	case path == apiPathRecovery:
		return apiPathRecovery
	case path == apiPathRecoveryActions:
		return apiPathRecoveryActions
	case path == apiPathShutdown:
		return apiPathShutdown
	case path == apiPathEvents:
		return apiPathEvents
	default:
		return path
	}
}

func loadOpenAPISpec(t *testing.T) openAPISpec {
	t.Helper()
	data, err := osReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	var spec openAPISpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse api/openapi.yaml: %v", err)
	}
	if len(spec.Paths) == 0 {
		t.Fatal("api/openapi.yaml has no paths")
	}
	return spec
}

var osReadFile = os.ReadFile

func documentedServerRoutes() []documentedRoute {
	return []documentedRoute{
		{method: httpMethodGet, path: apiPathHealth},
		{method: httpMethodGet, path: apiPathFeatures},
		{method: httpMethodPost, path: apiPathFeatures, mutation: true},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/config"},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/config", mutation: true},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/live-preview"},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/start", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/resume", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/stop", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/interrupt", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/restart", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/review-decision", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/need-user-input", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/need-user-input-draft", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/input-notifications", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/actions/{action}", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/actions/{action}/{subaction}", mutation: true},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/runs/{run_number}/artifacts"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/runs/{run_number}/artifacts/{artifact_id}"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/runs/{run_number}/logs/{log_id}"},
		{method: httpMethodGet, path: apiPathConfigRuntime},
		{method: "patch", path: apiPathConfigRuntime, mutation: true},
		{method: "put", path: apiPathConfigRuntime, mutation: true},
		{method: httpMethodGet, path: apiPathWorkspaceBrowse},
		{method: httpMethodGet, path: apiPathCatalogModels},
		{method: httpMethodGet, path: apiPathPrompts},
		{method: httpMethodPost, path: "/api/v1/prompts/ask-user/answer", mutation: true},
		{method: httpMethodPost, path: "/api/v1/prompts/help/send", mutation: true},
		{method: httpMethodPost, path: "/api/v1/prompts/chat/start", mutation: true},
		{method: httpMethodGet, path: apiPathPermissions},
		{method: httpMethodPost, path: apiPathPermissionsAnswer, mutation: true},
		{method: httpMethodGet, path: apiPathSessions},
		{method: httpMethodGet, path: "/api/v1/sessions/{session_id}"},
		{method: httpMethodGet, path: "/api/v1/sessions/{session_id}/transcript"},
		{method: httpMethodGet, path: "/api/v1/sessions/{session_id}/output"},
		{method: httpMethodGet, path: "/api/v1/sessions/{session_id}/output/stream", sse: true},
		{method: httpMethodGet, path: apiPathRecovery},
		{method: httpMethodPost, path: apiPathRecoveryActions, mutation: true},
		{method: httpMethodPost, path: apiPathShutdown, mutation: true},
		{method: httpMethodGet, path: apiPathEvents, sse: true},
	}
}

func routeKey(method, path string) string {
	return strings.ToLower(method) + " " + path
}

func isHTTPMethod(method string) bool {
	switch strings.ToLower(method) {
	case httpMethodGet, "post", "patch", "put", "delete", "head", "options", "trace":
		return true
	default:
		return false
	}
}

func effectiveSecurity(spec openAPISpec, op openAPIOperation) []map[string][]string {
	if len(op.Security) > 0 {
		return op.Security
	}
	return spec.Security
}

func hasSecurity(security []map[string][]string, name string) bool {
	for _, alternative := range security {
		if _, ok := alternative[name]; ok {
			return true
		}
	}
	return false
}

func hasTrustedMutationHeader(op openAPIOperation) bool {
	for _, parameter := range op.Parameters {
		if parameter.Ref == "#/components/parameters/TrustedMutationHeader" {
			return true
		}
		if parameter.Name == "X-Agentico-Client" && parameter.In == "header" && parameter.Required {
			return true
		}
	}
	return false
}

func lookupOpenAPIOperation(t *testing.T, spec openAPISpec, method, path string) openAPIOperation {
	t.Helper()
	methods, ok := spec.Paths[path]
	if !ok {
		t.Fatalf("OpenAPI path %s missing", path)
	}
	op, ok := methods[strings.ToLower(method)]
	if !ok {
		t.Fatalf("OpenAPI operation %s %s missing", method, path)
	}
	return op
}

func declaredOpenAPIResponse(t *testing.T, op openAPIOperation, status string) openAPIResponse {
	t.Helper()
	resp, ok := op.Responses[status]
	if !ok {
		t.Fatalf("operation %s missing response %s", op.OperationID, status)
	}
	return resp
}

func responseContentTypes(spec openAPISpec, resp openAPIResponse) []string {
	resp = resolveOpenAPIResponse(spec, resp)
	types := make([]string, 0, len(resp.Content))
	for contentType := range resp.Content {
		types = append(types, contentType)
	}
	return types
}

func declaresContentType(spec openAPISpec, resp openAPIResponse, contentType string) bool {
	for _, got := range responseContentTypes(spec, resp) {
		if got == contentType {
			return true
		}
	}
	return false
}

func resolveOpenAPIResponse(spec openAPISpec, resp openAPIResponse) openAPIResponse {
	if strings.HasPrefix(resp.Ref, "#/components/responses/") {
		name := strings.TrimPrefix(resp.Ref, "#/components/responses/")
		if resolved, ok := spec.Components.Responses[name]; ok {
			return resolved
		}
	}
	return resp
}

func assertSchemaProperties(t *testing.T, spec openAPISpec, schema string, props ...string) {
	t.Helper()
	found := schemaProperties(spec.Components.Schemas[schema])
	for _, prop := range props {
		if !found[prop] {
			t.Fatalf("schema %s missing property %s", schema, prop)
		}
	}
}

func schemaProperties(node any) map[string]bool {
	props := map[string]bool{}
	collectSchemaProperties(node, props)
	return props
}

func collectSchemaProperties(node any, props map[string]bool) {
	switch value := node.(type) {
	case map[string]any:
		if rawProps, ok := value["properties"].(map[string]any); ok {
			for name := range rawProps {
				props[name] = true
			}
		}
		for _, key := range []string{"allOf", "anyOf", "oneOf"} {
			if items, ok := value[key].([]any); ok {
				for _, item := range items {
					collectSchemaProperties(item, props)
				}
			}
		}
	case map[any]any:
		converted := make(map[string]any, len(value))
		for key, item := range value {
			if keyString, ok := key.(string); ok {
				converted[keyString] = item
			}
		}
		collectSchemaProperties(converted, props)
	}
}
