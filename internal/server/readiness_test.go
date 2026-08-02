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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// readinessProbeProvider wraps the shared MockProvider with a configurable
// readiness probe so tests can model unauthenticated and later-authenticated
// provider CLIs.
type readinessProbeProvider struct {
	*mocks.MockProvider
	status func() llm.ProviderReadiness
}

func (p *readinessProbeProvider) CheckReadiness(_ context.Context) llm.ProviderReadiness {
	return p.status()
}

type refreshableProvider struct {
	*readinessProbeProvider
	mu             sync.RWMutex
	catalog        []llm.ModelInfo
	discovered     []llm.ModelInfo
	discoveryError error
	discoveries    atomic.Int32
}

func (p *refreshableProvider) ModelCatalog() []llm.ModelInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]llm.ModelInfo(nil), p.catalog...)
}

func (p *refreshableProvider) SetModelCatalog(models []llm.ModelInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.catalog = append([]llm.ModelInfo(nil), models...)
}

func (p *refreshableProvider) DiscoverModelCatalog(context.Context) ([]llm.ModelInfo, error) {
	p.discoveries.Add(1)
	return append([]llm.ModelInfo(nil), p.discovered...), p.discoveryError
}

// versionGatedProvider wraps the shared MockProvider and opts into strict
// minimum-version enforcement.
type versionGatedProvider struct {
	*mocks.MockProvider
}

func (p *versionGatedProvider) EnforcesMinVersion() bool { return true }

func newReadinessRegistry(providers ...llm.LLMProvider) *llm.Registry {
	registry := llm.NewRegistry()
	for _, p := range providers {
		registry.Register(p)
	}
	return registry
}

func staticReadiness(status llm.ProviderReadiness) func() llm.ProviderReadiness {
	return func() llm.ProviderReadiness { return status }
}

func getReadinessSnapshot(t *testing.T, handler http.Handler) ReadinessResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/readiness", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/readiness status = %d body=%s; want 200", resp.StatusCode, w.Body.String())
	}
	var body ReadinessResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode readiness response: %v", err)
	}
	if body.APIVersion != APIVersion {
		t.Fatalf("api_version = %q; want %q", body.APIVersion, APIVersion)
	}
	return body
}

func providerEntry(t *testing.T, snapshot ReadinessResponse, name string) ProviderReadiness {
	t.Helper()
	for _, p := range snapshot.Providers {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("provider %q missing from readiness snapshot: %+v", name, snapshot.Providers)
	return ProviderReadiness{}
}

func postTrustedJSON(handler http.Handler, path string, body any) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestReadinessEndpointReportsProviderTaxonomyAndWorkspaceValidity(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	discoveredRepo := filepath.Join(root, "good-repo")
	if err := os.MkdirAll(filepath.Join(discoveredRepo, ".git"), 0o755); err != nil {
		t.Fatalf("create fake git repo: %v", err)
	}
	brokenRepoPath := filepath.Join(t.TempDir(), "not-a-repo")
	if err := os.MkdirAll(brokenRepoPath, 0o755); err != nil {
		t.Fatalf("create broken repo dir: %v", err)
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{root, filepath.Join(root, "does-not-exist")}
	cfg.Repos["broken"] = config.RepoConfig{Path: brokenRepoPath}

	missing := &mocks.MockProvider{ProviderName: "missing", Hint: "npm install -g missing-cli"}
	unauth := &readinessProbeProvider{
		MockProvider: &mocks.MockProvider{ProviderName: "unauth", CLIDetected: true, Hint: "npm install -g unauth-cli"},
		status: staticReadiness(llm.ProviderReadiness{
			Ready:  false,
			Detail: "unauth CLI is installed but not authenticated",
			Remedy: "Run: unauth-cli auth login",
		}),
	}
	oldver := &versionGatedProvider{
		MockProvider: &mocks.MockProvider{
			ProviderName:      "oldver",
			CLIDetected:       true,
			Hint:              "npm install -g oldver-cli",
			VersionInfoResult: "0.1.0",
			MinVer:            [3]int{1, 0, 0},
		},
	}
	ready := &readinessProbeProvider{
		MockProvider: &mocks.MockProvider{
			ProviderName:      "readyprov",
			CLIDetected:       true,
			Models:            []string{"fake-model"},
			VersionInfoResult: "9.9.9",
		},
		status: staticReadiness(llm.ProviderReadiness{Ready: true, Detail: "authenticated as user@example.com"}),
	}

	registry := newReadinessRegistry(missing, unauth, oldver, ready)
	handler := NewHandler(HandlerOptions{
		Config:                cfg,
		Registry:              registry,
		DisableHostValidation: true,
	})

	snapshot := getReadinessSnapshot(t, handler)

	if !snapshot.Ready {
		t.Fatalf("snapshot.Ready = false; want true with one usable provider: %+v", snapshot.Issues)
	}
	if snapshot.ProbedAt == nil || snapshot.ProbedAt.IsZero() {
		t.Fatalf("snapshot.ProbedAt = %v; want probe timestamp", snapshot.ProbedAt)
	}

	missingEntry := providerEntry(t, snapshot, "missing")
	if missingEntry.Installed || missingEntry.Ready || missingEntry.Issue == nil ||
		missingEntry.Issue.Code != MissingExecutable {
		t.Fatalf("missing provider entry = %+v; want missing_executable issue", missingEntry)
	}
	if missingEntry.Issue.Remedy == "" {
		t.Fatalf("missing provider remedy empty; want install hint")
	}

	unauthEntry := providerEntry(t, snapshot, "unauth")
	if !unauthEntry.Installed || unauthEntry.Ready || unauthEntry.Issue == nil ||
		unauthEntry.Issue.Code != Unauthenticated {
		t.Fatalf("unauth provider entry = %+v; want unauthenticated issue", unauthEntry)
	}
	if unauthEntry.Issue.Remedy != "Run: unauth-cli auth login" {
		t.Fatalf("unauth remedy = %q; want safe CLI remediation command", unauthEntry.Issue.Remedy)
	}

	oldverEntry := providerEntry(t, snapshot, "oldver")
	if !oldverEntry.Installed || oldverEntry.Ready || oldverEntry.Issue == nil ||
		oldverEntry.Issue.Code != UnsupportedVersion {
		t.Fatalf("oldver provider entry = %+v; want unsupported_version issue", oldverEntry)
	}

	readyEntry := providerEntry(t, snapshot, "readyprov")
	if !readyEntry.Installed || !readyEntry.Ready || readyEntry.Issue != nil {
		t.Fatalf("ready provider entry = %+v; want installed+ready with no issue", readyEntry)
	}

	if !snapshot.Models.Available {
		t.Fatalf("models = %+v; want available via ready provider", snapshot.Models)
	}
	foundModel := false
	for _, m := range snapshot.Models.Models {
		if m == "fake-model" {
			foundModel = true
		}
	}
	if !foundModel {
		t.Fatalf("models = %v; want fake-model listed", snapshot.Models.Models)
	}

	if !snapshot.Configuration.Valid {
		t.Fatalf("configuration = %+v; want valid", snapshot.Configuration)
	}

	rootsByPath := map[string]WorkspaceRootReadiness{}
	for _, r := range snapshot.Workspace.Roots {
		rootsByPath[r.Path] = r
	}
	if entry, ok := rootsByPath[root]; !ok || !entry.Valid {
		t.Fatalf("valid workspace root entry = %+v (ok=%v); want valid", rootsByPath[root], ok)
	}
	invalidRoot := rootsByPath[filepath.Join(root, "does-not-exist")]
	if invalidRoot.Valid || invalidRoot.Issue == nil || invalidRoot.Issue.Code != InvalidWorkspaceRoot {
		t.Fatalf("invalid workspace root entry = %+v; want invalid_workspace_root issue", invalidRoot)
	}

	reposByName := map[string]RepositoryReadiness{}
	for _, r := range snapshot.Workspace.Repositories {
		reposByName[r.Name] = r
	}
	if entry, ok := reposByName["good-repo"]; !ok || !entry.Valid {
		t.Fatalf("discovered repo entry = %+v (ok=%v); want valid git repo", reposByName["good-repo"], ok)
	}
	broken := reposByName["broken"]
	if broken.Valid || broken.Issue == nil || broken.Issue.Code != InvalidRepository {
		t.Fatalf("broken repo entry = %+v; want invalid_repository issue", broken)
	}

	// Probing must restrict model routing to usable providers only.
	models := registry.AvailableModels()
	if len(models) != 1 || models[0] != "fake-model" {
		t.Fatalf("registry.AvailableModels() = %v; want only the ready provider's models", models)
	}

	// Flattened issues cover every failing section.
	codes := map[ReadinessIssueCode]bool{}
	for _, issue := range snapshot.Issues {
		codes[issue.Code] = true
	}
	for _, want := range []ReadinessIssueCode{MissingExecutable, Unauthenticated, UnsupportedVersion, InvalidWorkspaceRoot, InvalidRepository} {
		if !codes[want] {
			t.Fatalf("flattened issues missing %s: %+v", want, snapshot.Issues)
		}
	}
}

func TestReadinessInvalidConfigurationReported(t *testing.T) {
	t.Parallel()

	cfg := config.NewDefault()
	cfg.Defaults.Pipeline = "bogus-profile"
	ready := &readinessProbeProvider{
		MockProvider: &mocks.MockProvider{ProviderName: "readyprov", CLIDetected: true, Models: []string{"fake-model"}},
		status:       staticReadiness(llm.ProviderReadiness{Ready: true}),
	}
	handler := NewHandler(HandlerOptions{
		Config:                cfg,
		Registry:              newReadinessRegistry(ready),
		DisableHostValidation: true,
	})

	snapshot := getReadinessSnapshot(t, handler)
	if snapshot.Ready {
		t.Fatal("snapshot.Ready = true; want false with invalid configuration")
	}
	if snapshot.Configuration.Valid || snapshot.Configuration.Issue == nil ||
		snapshot.Configuration.Issue.Code != InvalidConfiguration {
		t.Fatalf("configuration = %+v; want invalid_configuration issue", snapshot.Configuration)
	}
}

// createFeatureRecorder fails feature creation attempts unless allowed,
// recording accepted requests.
type createFeatureRecorder struct {
	MutationTarget
	created atomic.Int64
}

func TestCreateFeatureIdempotencyKeyReturnsOriginalResult(t *testing.T) {
	t.Parallel()
	target := &createFeatureRecorder{}
	handler := NewHandler(HandlerOptions{
		Config: config.NewDefault(), Mutations: target, DisableHostValidation: true,
	})
	body := map[string]any{
		"name": "one feature", "idempotency_key": "3d7fa9f5-7417-4785-9981-fbfc9988bd8f",
		"pipeline": "large", "risk_level": "high",
	}
	for range 2 {
		w := postTrustedJSON(handler, apiPathFeatures, body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create status = %d body=%s; want 201", w.Code, w.Body.String())
		}
	}
	if got := target.created.Load(); got != 1 {
		t.Fatalf("CreateFeature called %d times; want one authoritative mutation", got)
	}
}

func TestCreateFeatureReportsFieldSpecificCreationLimits(t *testing.T) {
	t.Parallel()
	target := &createFeatureRecorder{}
	handler := NewHandler(HandlerOptions{
		Config: config.NewDefault(), Mutations: target, DisableHostValidation: true,
	})
	tests := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "idempotency key",
			body: map[string]any{"name": "feature", "idempotency_key": strings.Repeat("k", 129)},
			want: "idempotency_key exceeds the 128 character limit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := postTrustedJSON(handler, apiPathFeatures, tt.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("create status = %d body=%s; want 400", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tt.want) {
				t.Errorf("create body = %s; want field-specific message %q", w.Body.String(), tt.want)
			}
		})
	}
	if got := target.created.Load(); got != 0 {
		t.Errorf("CreateFeature called %d times; want 0 for invalid input", got)
	}
}

func TestCreateFeatureRejectsUnresolvableModelOverrides(t *testing.T) {
	t.Parallel()

	target := &createFeatureRecorder{}
	ready := &readinessProbeProvider{
		MockProvider: &mocks.MockProvider{
			ProviderName: "readyprov",
			CLIDetected:  true,
			Models:       []string{"sonnet[1M]"},
			Catalog: []llm.ModelInfo{
				{ID: "sonnet[1M]", Aliases: []string{"sonnet"}},
			},
		},
		status: staticReadiness(llm.ProviderReadiness{Ready: true}),
	}
	handler := NewHandler(HandlerOptions{
		Config:                config.NewDefault(),
		Registry:              newReadinessRegistry(ready),
		Mutations:             target,
		DisableHostValidation: true,
	})

	w := postTrustedJSON(handler, apiPathFeatures, map[string]any{
		"name":   "bad model",
		"models": map[string]string{"planning": "not-a-real-model"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	body := decodeErrorBody(t, w)
	if body.Error.Code != errCodeBadRequest {
		t.Fatalf("error code = %q; want %q", body.Error.Code, errCodeBadRequest)
	}
	if !strings.Contains(body.Error.Message, "model for planning is unavailable") {
		t.Fatalf("message = %q; want unavailable planning model", body.Error.Message)
	}
	if got := target.created.Load(); got != 0 {
		t.Fatalf("CreateFeature called %d times; want 0 for invalid model", got)
	}
}

func TestCreateFeatureAcceptsContextAnnotatedModelFallback(t *testing.T) {
	t.Parallel()

	target := &createFeatureRecorder{}
	ready := &readinessProbeProvider{
		MockProvider: &mocks.MockProvider{
			ProviderName: "readyprov",
			CLIDetected:  true,
			Models:       []string{"sonnet[1M]"},
			Catalog: []llm.ModelInfo{
				{ID: "sonnet[1M]", Aliases: []string{"sonnet"}},
			},
		},
		status: staticReadiness(llm.ProviderReadiness{Ready: true}),
	}
	handler := NewHandler(HandlerOptions{
		Config:                config.NewDefault(),
		Registry:              newReadinessRegistry(ready),
		Mutations:             target,
		DisableHostValidation: true,
	})

	w := postTrustedJSON(handler, apiPathFeatures, map[string]any{
		"name":   "fallback model",
		"models": map[string]string{"kb_build": "sonnet[200K]"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	if got := target.created.Load(); got != 1 {
		t.Fatalf("CreateFeature called %d times; want 1 after fallback model", got)
	}
}

func (t *createFeatureRecorder) CreateFeature(req CreateFeatureRequest) (CreateFeatureResponse, error) {
	t.created.Add(1)
	return CreateFeatureResponse{FeatureID: fixtureFeatureID, Result: resultCreated}, nil
}

func TestCreateFeatureReturnsStructuredReadinessErrorWhenNoProviderUsable(t *testing.T) {
	t.Parallel()

	unauth := &readinessProbeProvider{
		MockProvider: &mocks.MockProvider{ProviderName: "unauth", CLIDetected: true, Models: []string{"fake-model"}},
		status: staticReadiness(llm.ProviderReadiness{
			Ready:  false,
			Detail: "not authenticated",
			Remedy: "Run: unauth-cli auth login",
		}),
	}
	target := &createFeatureRecorder{}
	handler := NewHandler(HandlerOptions{
		Config:                config.NewDefault(),
		Registry:              newReadinessRegistry(unauth),
		Mutations:             target,
		DisableHostValidation: true,
	})

	w := postTrustedJSON(handler, apiPathFeatures, map[string]string{"name": "gated feature"})
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d body=%s; want 409", resp.StatusCode, w.Body.String())
	}
	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != "not_ready" {
		t.Fatalf("error code = %q; want not_ready", body.Error.Code)
	}
	rawIssues, ok := body.Error.Target["issues"]
	if !ok {
		t.Fatalf("error target = %+v; want structured issues", body.Error.Target)
	}
	issuesJSON, _ := json.Marshal(rawIssues)
	var issues []ReadinessIssue
	if err := json.Unmarshal(issuesJSON, &issues); err != nil || len(issues) == 0 {
		t.Fatalf("issues = %s (err=%v); want non-empty readiness issues", issuesJSON, err)
	}
	sawUnauthenticated := false
	for _, issue := range issues {
		if issue.Code == Unauthenticated {
			sawUnauthenticated = true
		}
	}
	if !sawUnauthenticated {
		t.Fatalf("issues = %+v; want unauthenticated issue", issues)
	}
	if got := target.created.Load(); got != 0 {
		t.Fatalf("CreateFeature called %d times; want 0 while not ready", got)
	}
}

func TestReadinessRefreshReprobesAndUnblocksFeatureCreation(t *testing.T) {
	t.Parallel()

	var authenticated atomic.Bool
	provider := &readinessProbeProvider{
		MockProvider: &mocks.MockProvider{ProviderName: "flipper", CLIDetected: true, Models: []string{"fake-model"}},
		status: func() llm.ProviderReadiness {
			if authenticated.Load() {
				return llm.ProviderReadiness{Ready: true}
			}
			return llm.ProviderReadiness{Ready: false, Detail: "not authenticated", Remedy: "Run: flipper auth login"}
		},
	}
	target := &createFeatureRecorder{}
	apiHandler := newAPIHandler(HandlerOptions{
		Config:                config.NewDefault(),
		Registry:              newReadinessRegistry(provider),
		Mutations:             target,
		DisableHostValidation: true,
	})
	handler := apiHandler.routes()

	if snapshot := getReadinessSnapshot(t, handler); snapshot.Ready {
		t.Fatal("initial snapshot.Ready = true; want false before authentication")
	}
	if w := postTrustedJSON(handler, apiPathFeatures, map[string]string{"name": "gated"}); w.Code != http.StatusConflict {
		t.Fatalf("create while unauthenticated status = %d; want 409", w.Code)
	}

	// Plain GET must not re-probe: it reuses the cached probe result so the
	// desktop app never pays a CLI probe per poll.
	authenticated.Store(true)
	if snapshot := getReadinessSnapshot(t, handler); snapshot.Ready {
		t.Fatal("GET readiness re-probed providers; want cached not-ready snapshot until refresh")
	}

	seqBefore := apiHandler.broker.currentSeq()
	w := postTrustedJSON(handler, "/api/v1/readiness/refresh", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var refreshed ReadinessResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&refreshed); err != nil {
		t.Fatalf("decode refresh response: %v", err)
	}
	if !refreshed.Ready {
		t.Fatalf("refreshed.Ready = false; want true after authentication: %+v", refreshed.Issues)
	}
	entry := providerEntry(t, refreshed, "flipper")
	if !entry.Ready || entry.Issue != nil {
		t.Fatalf("refreshed provider entry = %+v; want ready", entry)
	}
	if seqAfter := apiHandler.broker.currentSeq(); seqAfter <= seqBefore {
		t.Fatalf("broker seq = %d (before %d); want readiness change published on event stream", seqAfter, seqBefore)
	}

	if w := postTrustedJSON(handler, apiPathFeatures, map[string]string{"name": "now allowed"}); w.Code != http.StatusCreated {
		t.Fatalf("create after refresh status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	if got := target.created.Load(); got != 1 {
		t.Fatalf("CreateFeature called %d times; want 1 after readiness refresh", got)
	}
}

func TestReadinessRefreshRequiresTrustedClientHeader(t *testing.T) {
	t.Parallel()

	handler := NewHandler(HandlerOptions{
		Config:                config.NewDefault(),
		Registry:              newReadinessRegistry(),
		Mutations:             &createFeatureRecorder{},
		DisableHostValidation: true,
	})
	payload := bytes.NewReader([]byte(`{}`))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/readiness/refresh", payload)
	req.Header.Set("Content-Type", contentTypeJSON)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("refresh without trusted header status = %d; want 403", w.Code)
	}
}

func TestProviderModelRefreshReprobesOnlySelectedProviderAndReplacesItsCatalog(t *testing.T) {
	t.Parallel()

	var claudeProbes atomic.Int32
	var codexProbes atomic.Int32
	claude := &refreshableProvider{
		readinessProbeProvider: &readinessProbeProvider{
			MockProvider: &mocks.MockProvider{
				ProviderName:      "claude",
				CLIDetected:       true,
				VersionInfoResult: "2.1.220",
			},
			status: func() llm.ProviderReadiness {
				claudeProbes.Add(1)
				return llm.ProviderReadiness{Ready: true}
			},
		},
		catalog:    []llm.ModelInfo{{ID: "claude-old", Category: "balanced"}},
		discovered: []llm.ModelInfo{{ID: "claude-new", Category: "capable"}},
	}
	codex := &refreshableProvider{
		readinessProbeProvider: &readinessProbeProvider{
			MockProvider: &mocks.MockProvider{
				ProviderName:      "codex",
				CLIDetected:       true,
				VersionInfoResult: "0.145.0",
			},
			status: func() llm.ProviderReadiness {
				codexProbes.Add(1)
				return llm.ProviderReadiness{Ready: true}
			},
		},
		catalog:    []llm.ModelInfo{{ID: "codex-current", Category: "capable"}},
		discovered: []llm.ModelInfo{{ID: "codex-unrequested", Category: "capable"}},
	}
	handler := NewHandler(HandlerOptions{
		Registry:              newReadinessRegistry(claude, codex),
		Mutations:             &createFeatureRecorder{},
		DisableHostValidation: true,
	})
	_ = getReadinessSnapshot(t, handler)

	w := postTrustedJSON(handler, "/api/v1/catalog/models/refresh", map[string]string{
		"provider": "claude",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var body struct {
		Readiness ReadinessResponse    `json:"readiness"`
		Catalog   ModelCatalogResponse `json:"catalog"`
	}
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode refresh response: %v", err)
	}
	if got := claudeProbes.Load(); got != 2 {
		t.Fatalf("Claude readiness probes = %d; want initial + selected refresh", got)
	}
	if got := codexProbes.Load(); got != 1 {
		t.Fatalf("Codex readiness probes = %d; want initial probe only", got)
	}
	if got := body.Catalog.ProviderModels["claude"]; len(got) != 1 || got[0].ID != "claude-new" {
		t.Fatalf("Claude refreshed models = %+v; want claude-new", got)
	}
	if got := body.Catalog.ProviderModels["codex"]; len(got) != 1 || got[0].ID != "codex-current" {
		t.Fatalf("Codex models = %+v; want unchanged codex-current", got)
	}
	if entry := providerEntry(t, body.Readiness, "claude"); !entry.Ready {
		t.Fatalf("Claude readiness = %+v; want ready", entry)
	}
}

func TestProviderModelRefreshFailurePreservesPreviousCatalog(t *testing.T) {
	t.Parallel()

	provider := &refreshableProvider{
		readinessProbeProvider: &readinessProbeProvider{
			MockProvider: &mocks.MockProvider{
				ProviderName:      "claude",
				CLIDetected:       true,
				VersionInfoResult: "2.1.220",
			},
			status: staticReadiness(llm.ProviderReadiness{Ready: true}),
		},
		catalog:        []llm.ModelInfo{{ID: "claude-stable", Category: "capable"}},
		discoveryError: errors.New("provider CLI failed"),
	}
	handler := NewHandler(HandlerOptions{
		Registry:              newReadinessRegistry(provider),
		Mutations:             &createFeatureRecorder{},
		DisableHostValidation: true,
	})
	_ = getReadinessSnapshot(t, handler)

	w := postTrustedJSON(handler, "/api/v1/catalog/models/refresh", map[string]string{
		"provider": "claude",
	})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("refresh status = %d body=%s; want 502", w.Code, w.Body.String())
	}
	if got := provider.ModelCatalog(); len(got) != 1 || got[0].ID != "claude-stable" {
		t.Fatalf("catalog after failed refresh = %+v; want prior claude-stable", got)
	}
}

func TestProviderModelRefreshCacheFailurePreservesPreviousCatalog(t *testing.T) {
	t.Parallel()

	provider := &refreshableProvider{
		readinessProbeProvider: &readinessProbeProvider{
			MockProvider: &mocks.MockProvider{
				ProviderName:      "claude",
				CLIDetected:       true,
				VersionInfoResult: "2.1.220",
			},
			status: staticReadiness(llm.ProviderReadiness{Ready: true}),
		},
		catalog:    []llm.ModelInfo{{ID: "claude-stable", Category: "capable"}},
		discovered: []llm.ModelInfo{{ID: "claude-new", Category: "capable"}},
	}
	handler := NewHandler(HandlerOptions{
		Registry:              newReadinessRegistry(provider),
		Mutations:             &createFeatureRecorder{},
		DisableHostValidation: true,
		PersistProviderModelCatalog: func(llm.LLMProvider, []llm.ModelInfo) error {
			return errors.New("disk full")
		},
	})
	_ = getReadinessSnapshot(t, handler)

	w := postTrustedJSON(handler, "/api/v1/catalog/models/refresh", map[string]string{
		"provider": "claude",
	})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("refresh status = %d body=%s; want 502", w.Code, w.Body.String())
	}
	if got := provider.ModelCatalog(); len(got) != 1 || got[0].ID != "claude-stable" {
		t.Fatalf("catalog after cache failure = %+v; want prior claude-stable", got)
	}
}

func TestProviderModelRefreshRejectsInvalidProviderName(t *testing.T) {
	t.Parallel()

	handler := NewHandler(HandlerOptions{
		Registry:              newReadinessRegistry(),
		Mutations:             &createFeatureRecorder{},
		DisableHostValidation: true,
	})
	w := postTrustedJSON(handler, "/api/v1/catalog/models/refresh", map[string]string{
		"provider": "../claude",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("refresh status = %d body=%s; want 400", w.Code, w.Body.String())
	}
}

func TestProviderModelRefreshHandlesUnknownUnreadyAndUnsupportedProviders(t *testing.T) {
	t.Parallel()

	unready := &refreshableProvider{
		readinessProbeProvider: &readinessProbeProvider{
			MockProvider: &mocks.MockProvider{ProviderName: "unready", CLIDetected: true},
			status: staticReadiness(llm.ProviderReadiness{
				Ready:  false,
				Detail: "not authenticated",
				Remedy: "Run unready login",
			}),
		},
		discovered: []llm.ModelInfo{{ID: "must-not-discover"}},
	}
	unsupported := &readinessProbeProvider{
		MockProvider: &mocks.MockProvider{
			ProviderName: "unsupported",
			CLIDetected:  true,
			Models:       []string{"fallback"},
		},
		status: staticReadiness(llm.ProviderReadiness{Ready: true}),
	}
	handler := NewHandler(HandlerOptions{
		Registry:              newReadinessRegistry(unready, unsupported),
		Mutations:             &createFeatureRecorder{},
		DisableHostValidation: true,
	})
	_ = getReadinessSnapshot(t, handler)

	unknown := postTrustedJSON(handler, "/api/v1/catalog/models/refresh", map[string]string{
		"provider": "unknown",
	})
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d; want 404", unknown.Code)
	}

	notReady := postTrustedJSON(handler, "/api/v1/catalog/models/refresh", map[string]string{
		"provider": "unready",
	})
	if notReady.Code != http.StatusOK {
		t.Fatalf("unready provider status = %d body=%s; want 200 readiness response", notReady.Code, notReady.Body.String())
	}
	if got := unready.discoveries.Load(); got != 0 {
		t.Fatalf("unready provider discovery calls = %d; want 0", got)
	}

	cannotRefresh := postTrustedJSON(handler, "/api/v1/catalog/models/refresh", map[string]string{
		"provider": "unsupported",
	})
	if cannotRefresh.Code != http.StatusConflict {
		t.Fatalf("unsupported provider status = %d body=%s; want 409", cannotRefresh.Code, cannotRefresh.Body.String())
	}
}
