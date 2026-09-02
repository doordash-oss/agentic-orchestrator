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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"gopkg.in/yaml.v3"
)

func TestGeneratedOpenAPIIsCurrent(t *testing.T) {
	cmd := exec.Command("go", "run", "../../tools/openapi-generate", "--check")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenAPI generated code drift:\n%s\n%v", out, err)
	}
}

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
	RequestBody map[string]any             `yaml:"requestBody"`
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
	for _, schema := range []string{"ResponseMeta", "SSEEvent", "Resource", "SessionOutputChunk", "ErrorResponse"} {
		if _, ok := spec.Components.Schemas[schema]; !ok {
			t.Fatalf("components.schemas.%s missing", schema)
		}
	}
	assertSchemaProperties(t, spec, "ResponseMeta", "revision", "generated_at", "as_of_seq")
	assertSchemaProperties(t, spec, "SSEEvent", "seq", "epoch", "kind", "resource", "resource_version", "snapshot_required")
	assertSchemaProperties(t, spec, "Resource", "type", "id", "feature_id", "phase")
	assertSchemaProperties(t, spec, "NeedUserInputGate", "verification")
	assertSchemaProperties(t, spec, "NeedUserInputVerification", "blockers", "allowed_actions")
	assertSchemaProperties(
		t, spec, "NeedUserInputVerificationBlocker",
		"item_id", "name", "repo_name", "command", "reason", "capabilities", "remediation",
	)
	// The canonical error envelope: ErrorResponse wraps the catalog-rendered
	// Error, which carries the stable code, severity class, and authored
	// title/summary plus optional typed context and raw diagnostics.
	assertSchemaProperties(t, spec, "ErrorResponse", "api_version", "error")
	assertSchemaProperties(
		t, spec, "Error",
		"code", "class", "title", "summary", "diagnostics", "context", "remediation",
	)
	assertSchemaProperties(
		t, spec, "ErrorRepositoryContext",
		"name", "branch", "conflict_files", "dirty_files", "parent_anchor_sha",
		"expected_ref_sha", "child_head_sha", "candidate_sha", "merge_head", "observed_sha",
	)
	errorProps := schemaProperties(spec.Components.Schemas["Error"])
	for _, forbidden := range []string{"message", "status", "target"} {
		if errorProps[forbidden] {
			t.Fatalf("Error schema must not carry legacy property %q", forbidden)
		}
	}
}

// TestIntegrationAttentionSchemasCollapsedToCanonicalError pins the OpenAPI
// shape of the integration-attention single owner: the transaction journal's
// and the relationship child summaries' attention fields reference the
// canonical Error component, the legacy relationship-attention and
// dirty-diagnostics schemas and the per-entry attention duplicates are gone,
// and the typed pending-sync flag is declared.
func TestIntegrationAttentionSchemasCollapsedToCanonicalError(t *testing.T) {
	spec := loadOpenAPISpec(t)
	for _, schema := range []string{"RelationshipAttention", "ChildDirtyDiagnostics"} {
		if _, ok := spec.Components.Schemas[schema]; ok {
			t.Fatalf("components.schemas.%s must be deleted; attention is the canonical Error", schema)
		}
	}
	assertSchemaProperties(t, spec, "TransactionJournal", "phase", "attention", "entries")
	assertSchemaProperties(t, spec, "RelationshipChildSummary", "attention")
	// The detail schema composes the summary through allOf, so its own
	// property set carries only the diff body.
	assertSchemaProperties(t, spec, "RelationshipChild", "diff_summary")
	assertSchemaProperties(t, spec, "RepoTransactionEntry", "pending_sync")
	entryProps := schemaProperties(spec.Components.Schemas["RepoTransactionEntry"])
	for _, forbidden := range removedEntryWireKeys {
		if entryProps[forbidden] {
			t.Fatalf("RepoTransactionEntry must not carry removed property %q", forbidden)
		}
	}
}

// TestRefactorRequestSchemaOmitsRepoSelection pins the refactor contract:
// repository and base-branch selection are inherited from the parent and must
// never re-enter the refactor request schema.
func TestRefactorRequestSchemaOmitsRepoSelection(t *testing.T) {
	spec := loadOpenAPISpec(t)
	schema, ok := spec.Components.Schemas["RefactorFeatureRequest"]
	if !ok {
		t.Fatal("components.schemas.RefactorFeatureRequest missing")
	}
	props := schemaProperties(schema)
	for _, required := range []string{"name", "description", "images", "attachments", "pipeline", "checkpoints", "effort", "models", "risk_level", "exit_criteria", "inquireness"} {
		if !props[required] {
			t.Fatalf("RefactorFeatureRequest missing property %s", required)
		}
	}
	for _, forbidden := range []string{"repos", "repo", "branches", "branch", "base_branch", "use_current_branch", "use_current_branch_per_repo"} {
		if props[forbidden] {
			t.Fatalf("RefactorFeatureRequest must not accept repository/branch selection; found %q", forbidden)
		}
	}
}

// apiPathRefactorAction is the served URL of the refactor launch action.
const apiPathRefactorAction = "/api/v1/features/{feature_id}/actions/refactor"

const apiPathReviewFeedbackFetch = "/api/v1/features/{feature_id}/actions/review-feedback/fetch"
const apiPathReviewFeedbackSelection = "/api/v1/features/{feature_id}/actions/review-feedback/selection"

const apiPathReviewFeedbackAction = "/api/v1/features/{feature_id}/actions/review-feedback"

// TestRefactorOperationBindsTypedSchemas pins the statically documented
// refactor operation: the request body must reference RefactorFeatureRequest
// (so oapi-codegen emits the typed Go binding) and the success/typed error
// responses must be declared.
func TestRefactorOperationBindsTypedSchemas(t *testing.T) {
	spec := loadOpenAPISpec(t)
	op := lookupOpenAPIOperation(t, spec, http.MethodPost, apiPathRefactorAction)
	if op.OperationID != "refactorFeature" {
		t.Fatalf("operationId = %q, want refactorFeature", op.OperationID)
	}
	schemaRef := nestedYAMLRef(t, op.RequestBody, "content", "application/json", "schema")
	if schemaRef != "#/components/schemas/RefactorFeatureRequest" {
		t.Fatalf("refactorFeature requestBody schema = %q, want RefactorFeatureRequest", schemaRef)
	}
	resp201 := declaredOpenAPIResponse(t, op, "201")
	respSchemaRef := nestedYAMLRef(t, responseContentMap(t, resp201), "schema")
	if respSchemaRef != "#/components/schemas/RefactorFeatureResponse" {
		t.Fatalf("refactorFeature 201 schema = %q, want RefactorFeatureResponse", respSchemaRef)
	}
	declaredOpenAPIResponse(t, op, "404")
	declaredOpenAPIResponse(t, op, "409")
}

func TestReviewFeedbackFetchOperationBindsTypedSchemas(t *testing.T) {
	spec := loadOpenAPISpec(t)
	op := lookupOpenAPIOperation(t, spec, http.MethodPost, apiPathReviewFeedbackFetch)
	if op.OperationID != "fetchReviewFeedback" {
		t.Fatalf("operationId = %q, want fetchReviewFeedback", op.OperationID)
	}
	schemaRef := nestedYAMLRef(t, op.RequestBody, "content", "application/json", "schema")
	if schemaRef != "#/components/schemas/ReviewFeedbackFetchRequest" {
		t.Fatalf("fetchReviewFeedback requestBody schema = %q, want ReviewFeedbackFetchRequest", schemaRef)
	}
	resp200 := declaredOpenAPIResponse(t, op, "200")
	respSchemaRef := nestedYAMLRef(t, responseContentMap(t, resp200), "schema")
	if respSchemaRef != "#/components/schemas/ReviewFeedbackFetchResponse" {
		t.Fatalf("fetchReviewFeedback 200 schema = %q, want ReviewFeedbackFetchResponse", respSchemaRef)
	}
	declaredOpenAPIResponse(t, op, "400")
	declaredOpenAPIResponse(t, op, "404")
	declaredOpenAPIResponse(t, op, "502")
}

func TestReviewFeedbackLaunchOperationBindsTypedSchemas(t *testing.T) {
	spec := loadOpenAPISpec(t)
	op := lookupOpenAPIOperation(t, spec, http.MethodPost, apiPathReviewFeedbackAction)
	if op.OperationID != "reviewFeedbackFeature" {
		t.Fatalf("operationId = %q, want reviewFeedbackFeature", op.OperationID)
	}
	schemaRef := nestedYAMLRef(t, op.RequestBody, "content", "application/json", "schema")
	if schemaRef != "#/components/schemas/ReviewFeedbackFeatureRequest" {
		t.Fatalf("reviewFeedbackFeature requestBody schema = %q, want ReviewFeedbackFeatureRequest", schemaRef)
	}
	resp201 := declaredOpenAPIResponse(t, op, "201")
	respSchemaRef := nestedYAMLRef(t, responseContentMap(t, resp201), "schema")
	if respSchemaRef != "#/components/schemas/ReviewFeedbackFeatureResponse" {
		t.Fatalf("reviewFeedbackFeature 201 schema = %q, want ReviewFeedbackFeatureResponse", respSchemaRef)
	}
	declaredOpenAPIResponse(t, op, "400")
	declaredOpenAPIResponse(t, op, "404")
	declaredOpenAPIResponse(t, op, "409")

	schema, ok := spec.Components.Schemas["ReviewFeedbackFeatureRequest"]
	if !ok {
		t.Fatal("components.schemas.ReviewFeedbackFeatureRequest missing")
	}
	props := schemaProperties(schema)
	for _, required := range []string{"expected_revision", "gate"} {
		if !props[required] {
			t.Fatalf("ReviewFeedbackFeatureRequest missing property %q", required)
		}
	}
	// The launch request stays constant-size: only the expected draft
	// revision and gate travel; comment bodies and hunks are resolved
	// server-side from the durable draft.
	for _, forbidden := range []string{"repo", "mode", "comments", "body", "diff_hunk"} {
		if props[forbidden] {
			t.Fatalf("ReviewFeedbackFeatureRequest must stay constant-size without %q", forbidden)
		}
	}

	selSchema, ok := spec.Components.Schemas["ReviewFeedbackSelectionRequest"]
	if !ok {
		t.Fatal("components.schemas.ReviewFeedbackSelectionRequest missing")
	}
	selProps := schemaProperties(selSchema)
	for _, required := range []string{"expected_revision", "updates"} {
		if !selProps[required] {
			t.Fatalf("ReviewFeedbackSelectionRequest missing property %q", required)
		}
	}
	selUpdate, ok := spec.Components.Schemas["ReviewFeedbackSelectionUpdate"]
	if !ok {
		t.Fatal("components.schemas.ReviewFeedbackSelectionUpdate missing")
	}
	updateProps := schemaProperties(selUpdate)
	for _, required := range []string{"stable_ref", "selected"} {
		if !updateProps[required] {
			t.Fatalf("ReviewFeedbackSelectionUpdate missing property %q", required)
		}
	}
	for _, forbidden := range []string{"repo", "id", "type", "path", "body", "diff_hunk", "author", "created_at"} {
		if updateProps[forbidden] {
			t.Fatalf("ReviewFeedbackSelectionUpdate is reference-only and must not carry %q", forbidden)
		}
	}
	selOp := lookupOpenAPIOperation(t, spec, http.MethodPost, apiPathReviewFeedbackSelection)
	if selOp.OperationID != "updateReviewFeedbackSelection" {
		t.Fatalf("selection operationId = %q, want updateReviewFeedbackSelection", selOp.OperationID)
	}
	declaredOpenAPIResponse(t, selOp, "200")
	declaredOpenAPIResponse(t, selOp, "409")
}

// TestGeneratedRefactorSurfaceIsTyped pins the generated Go surface: the
// generated RefactorFeatureRequest carries every contracted field (compile
// time), the generated action enum includes refactor, and no hand-maintained
// duplicate redefines the type.
func TestGeneratedRefactorSurfaceIsTyped(t *testing.T) {
	if FeatureActionRefactor != FeatureAction(actionRefactor) || !FeatureActionRefactor.Valid() {
		t.Fatalf("generated action enum must include %q", actionRefactor)
	}
	req := RefactorFeatureRequest{
		Name:         "Rework auth",
		Description:  "desc",
		Images:       []string{"img"},
		Attachments:  []string{"att"},
		ExitCriteria: "done",
		Inquireness:  feature.InquirenessMedium,
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal generated RefactorFeatureRequest: %v", err)
	}
	var roundTrip RefactorFeatureRequest
	if err := json.Unmarshal(body, &roundTrip); err != nil {
		t.Fatalf("unmarshal generated RefactorFeatureRequest: %v", err)
	}
	if roundTrip.Name != req.Name || roundTrip.Description != req.Description || roundTrip.Inquireness != req.Inquireness {
		t.Fatalf("round trip mismatch: %+v vs %+v", roundTrip, req)
	}
}

func nestedYAMLRef(t *testing.T, node map[string]any, keys ...string) string {
	t.Helper()
	current := any(node)
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("missing intermediate key %q on the way to schema $ref", key)
		}
		current, ok = m[key]
		if !ok {
			t.Fatalf("missing key %q on the way to schema $ref", key)
		}
	}
	m, ok := current.(map[string]any)
	if !ok {
		t.Fatal("schema node is not an object")
	}
	ref, _ := m["$ref"].(string)
	return ref
}

func responseContentMap(t *testing.T, resp openAPIResponse) map[string]any {
	t.Helper()
	content, ok := resp.Content["application/json"].(map[string]any)
	if !ok {
		t.Fatalf("response %q missing application/json content", resp.Description)
	}
	return content
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
	case path == apiPathCatalogModels:
		return apiPathCatalogModels
	case path == apiPathCatalogRefresh:
		return apiPathCatalogRefresh
	case path == apiPathReadiness:
		return apiPathReadiness
	case path == apiPathReadinessRefresh:
		return apiPathReadinessRefresh
	case path == apiPathWorkspaceRepositoriesInit:
		return apiPathWorkspaceRepositoriesInit
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
	case path == apiPathRecoveryLogs:
		return apiPathRecoveryLogs
	case path == apiPathEvents:
		return apiPathEvents
	case path == apiPathUploads:
		return apiPathUploads
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
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/reviews"},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/reviews", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/reviews/{review_id}/validate", mutation: true},
		{method: "put", path: "/api/v1/features/{feature_id}/reviews/{review_id}/draft", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/reviews/{review_id}/decision", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/actions/{action}", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/actions/refactor", mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/actions/rebase", mutation: true},
		{method: httpMethodPost, path: apiPathReviewFeedbackAction, mutation: true},
		{method: httpMethodPost, path: apiPathReviewFeedbackFetch, mutation: true},
		{method: httpMethodPost, path: apiPathReviewFeedbackSelection, mutation: true},
		{method: httpMethodPost, path: "/api/v1/features/{feature_id}/actions/{action}/{subaction}", mutation: true},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/rewind/preview"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/completion/preflight"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/repositories/{repo_name}/diff"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/repositories/{repo_name}/path"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/runs"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/runs/{run_number}"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/runs/{run_number}/sessions"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/runs/{run_number}/artifacts"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/runs/{run_number}/artifacts/{artifact_id}"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/runs/{run_number}/logs/{log_id}"},
		{method: httpMethodGet, path: "/api/v1/features/{feature_id}/runs/{run_number}/logs"},
		{method: httpMethodGet, path: apiPathConfigRuntime},
		{method: "patch", path: apiPathConfigRuntime, mutation: true},
		{method: "put", path: apiPathConfigRuntime, mutation: true},
		{method: httpMethodGet, path: apiPathCatalogModels},
		{method: httpMethodPost, path: apiPathCatalogRefresh, mutation: true},
		{method: httpMethodGet, path: apiPathReadiness},
		{method: httpMethodPost, path: apiPathReadinessRefresh, mutation: true},
		{method: httpMethodPost, path: apiPathWorkspaceRepositoriesInit, mutation: true},
		{method: httpMethodGet, path: apiPathPrompts},
		{method: httpMethodPost, path: "/api/v1/prompts/ask-user/answer", mutation: true},
		{method: httpMethodPost, path: "/api/v1/prompts/help/send", mutation: true},
		{method: httpMethodPost, path: "/api/v1/prompts/chat/start", mutation: true},
		{method: httpMethodPost, path: "/api/v1/prompts/chat/end", mutation: true},
		{method: httpMethodGet, path: apiPathPermissions},
		{method: httpMethodPost, path: apiPathPermissionsAnswer, mutation: true},
		{method: httpMethodGet, path: apiPathSessions},
		{method: httpMethodGet, path: "/api/v1/sessions/{session_id}"},
		{method: httpMethodGet, path: "/api/v1/sessions/{session_id}/transcript"},
		{method: httpMethodGet, path: "/api/v1/sessions/{session_id}/output/stream", sse: true},
		{method: httpMethodGet, path: apiPathRecovery},
		{method: httpMethodPost, path: apiPathRecoveryActions, mutation: true},
		{method: httpMethodGet, path: apiPathRecoveryLogs},
		{method: httpMethodGet, path: apiPathEvents, sse: true},
		{method: httpMethodPost, path: apiPathUploads, mutation: true},
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
