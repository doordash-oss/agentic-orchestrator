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
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/instancelock"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func NewHandler(opts HandlerOptions) http.Handler {
	return newAPIHandler(opts).routes()
}

type apiHandler struct {
	runtime               RuntimeIdentity
	policy                LaunchPolicy
	startedAt             time.Time
	owner                 instancelock.Owner
	authToken             string
	features              FeatureLister
	store                 FeatureReader
	freshness             RepoFreshnessProvider
	cfg                   *config.Config
	registry              *llm.Registry
	sessions              ports.SessionManager
	broker                *eventBroker
	mutations             MutationTarget
	requestShutdown       func()
	disableHostValidation bool
	initGitRepository     func(path string) error

	// resourceSvc is the cached resource service, created once in
	// newAPIHandler so the per-resource mutex in resourceLockSet survives
	// across HTTP requests instead of being reconstructed every call.
	resourceSvc *resourceService

	recoveryMu         sync.Mutex
	recoverySnapshots  map[string][]ports.RecoveryItem
	reviewSessionLocks *reviewSessionLockSet

	// readinessMu guards the cached provider readiness probe results served
	// by /api/v1/readiness and refreshed by /api/v1/readiness/refresh.
	readinessMu       sync.Mutex
	providerReadiness []ProviderReadiness
	readinessProbedAt time.Time
}

func newAPIHandler(opts HandlerOptions) *apiHandler {
	startedAt := opts.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	store := opts.FeatureStore
	if store == nil {
		if reader, ok := opts.Features.(FeatureReader); ok {
			store = reader
		}
	}
	features := opts.Features
	if features == nil && store != nil {
		features = store
	}
	handler := &apiHandler{
		runtime:               opts.Runtime,
		policy:                opts.LaunchPolicy,
		startedAt:             startedAt,
		owner:                 opts.Owner,
		authToken:             opts.AuthToken,
		features:              features,
		store:                 store,
		freshness:             opts.Freshness,
		cfg:                   opts.Config,
		registry:              opts.Registry,
		sessions:              opts.Sessions,
		broker:                newEventBroker(opts.Events, opts.DomainEvents),
		mutations:             opts.Mutations,
		requestShutdown:       opts.RequestShutdown,
		disableHostValidation: opts.DisableHostValidation,
		initGitRepository:     opts.InitGitRepository,
		reviewSessionLocks:    newReviewSessionLockSet(),
	}
	handler.resourceSvc = newResourceService(store, handler.configOrDefault, opts.Registry, opts.Mutations, opts.Runtime)
	return handler
}

// topLevelRoute is one top-level mux registration. topLevelServerRoutes is
// the single source of truth routes() builds the mux from — a route-parity
// test (openapi_contract_test.go) reads this same slice so a pattern added
// here without a matching OpenAPI path (or vice versa) fails a test instead
// of silently drifting.
type topLevelRoute struct {
	pattern string
	handler func(*apiHandler) http.HandlerFunc
}

const (
	apiPathHealth           = "/api/v1/health"
	apiPathFeatures         = "/api/v1/features"
	apiPathConfigRuntime    = "/api/v1/config/runtime"
	apiPathCatalogModels    = "/api/v1/catalog/models"
	apiPathReadiness        = "/api/v1/readiness"
	apiPathReadinessRefresh = "/api/v1/readiness/refresh"
	apiPathPrompts          = "/api/v1/prompts"
	apiPathPermissions      = "/api/v1/permissions"
	apiPathSessions         = "/api/v1/sessions"
	apiPathRecovery         = "/api/v1/recovery"
	apiPathRecoveryActions  = "/api/v1/recovery/actions"
	apiPathRecoveryLogs     = "/api/v1/recovery/logs"
	apiPathShutdown         = "/api/v1/shutdown"
	apiPathEvents           = "/api/v1/events"
	apiPathResources        = "/api/v1/resources"
)

// routeSegmentConfig is the feature sub-route segment for the per-feature
// config endpoint, shared between the route matcher and the mutation
// preflight allow-list.
const routeSegmentConfig = "config"

// entityFeature is the resource/entity-type name for a feature. It's used as
// a generic-error-message noun and as the DTO discriminator for "feature"
// scoped resources (ResourceDTO.Type, ActionScopeDTO.Type, etc).
const entityFeature = "feature"

// resourceTypeSession and resourceTypeRuntime are the other ResourceDTO.Type
// discriminator values, alongside entityFeature.
const (
	resourceTypeSession = "session"
	resourceTypeRuntime = "runtime"
)

var topLevelServerRoutes = []topLevelRoute{
	{apiPathHealth, func(h *apiHandler) http.HandlerFunc { return methodHandler(h.handleHealth) }},
	{apiPathFeatures, func(h *apiHandler) http.HandlerFunc { return h.handleFeaturesRoot }},
	{apiPathFeatures + "/", func(h *apiHandler) http.HandlerFunc { return h.handleFeatureRoutes }},
	{apiPathConfigRuntime, func(h *apiHandler) http.HandlerFunc { return h.handleRuntimeConfigRoute }},
	{apiPathCatalogModels, func(h *apiHandler) http.HandlerFunc { return methodHandler(h.handleModelCatalog) }},
	{apiPathReadiness, func(h *apiHandler) http.HandlerFunc { return methodHandler(h.handleReadiness) }},
	{apiPathReadinessRefresh, func(h *apiHandler) http.HandlerFunc { return h.handleReadinessRefreshRoute }},
	{apiPathWorkspaceRepositoriesInit, func(h *apiHandler) http.HandlerFunc { return h.handleWorkspaceRepositoryInitRoute }},
	{apiPathPrompts, func(h *apiHandler) http.HandlerFunc { return methodHandler(h.handlePrompts) }},
	{apiPathPrompts + "/", func(h *apiHandler) http.HandlerFunc { return h.handlePromptMutationRoutes }},
	{apiPathPermissions, func(h *apiHandler) http.HandlerFunc { return methodHandler(h.handlePermissions) }},
	{apiPathPermissions + "/", func(h *apiHandler) http.HandlerFunc { return h.handlePermissionMutationRoutes }},
	{apiPathSessions, func(h *apiHandler) http.HandlerFunc { return methodHandler(h.handleSessionList) }},
	{apiPathSessions + "/", func(h *apiHandler) http.HandlerFunc { return methodHandler(h.handleSessionRoutes) }},
	{apiPathRecovery, func(h *apiHandler) http.HandlerFunc { return h.handleRecoveryRoute }},
	{apiPathRecoveryActions, func(h *apiHandler) http.HandlerFunc { return h.handleRecoveryActionRoute }},
	{apiPathRecoveryLogs, func(h *apiHandler) http.HandlerFunc { return h.handleRecoveryLogRoute }},
	{apiPathShutdown, func(h *apiHandler) http.HandlerFunc { return h.handleShutdownMutationRoute }},
	{apiPathEvents, func(h *apiHandler) http.HandlerFunc { return methodHandler(h.handleEvents) }},
	{apiPathResources, func(h *apiHandler) http.HandlerFunc { return h.handleResourceRoutes }},
	{apiPathResources + "/", func(h *apiHandler) http.HandlerFunc { return h.handleResourceRoutes }},
}

func (h *apiHandler) routes() http.Handler {
	mux := http.NewServeMux()
	for _, route := range topLevelServerRoutes {
		mux.HandleFunc(route.pattern, route.handler(h))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.rejectInvalidHost(w, r) {
			return
		}
		if h.handleMutationPreflight(w, r) {
			return
		}
		if h.applyMutationCORS(w, r) {
			return
		}
		if h.rejectUnauthorized(w, r) {
			return
		}
		h.setSequenceHeader(w)
		escaped := r.URL.EscapedPath()
		if strings.HasPrefix(escaped, apiPathFeatures+"/") {
			h.handleFeatureRoutes(w, r)
			return
		}
		if strings.HasPrefix(escaped, apiPathSessions+"/") {
			methodHandler(h.handleSessionRoutes)(w, r)
			return
		}
		if strings.HasPrefix(escaped, apiPathResources) {
			h.handleResourceRoutes(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (h *apiHandler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		APIVersion:    APIVersion,
		Status:        "ok",
		Runtime:       h.runtime,
		LaunchPolicy:  h.policy,
		StartedAt:     h.startedAt,
		Owner:         OwnerDTOFromInstanceOwner(h.owner),
		ServerTime:    time.Now().UTC(),
		Compatibility: NewCompatibilityDeclaration(h.owner.Version),
	})
}

func (h *apiHandler) handleFeatureList(w http.ResponseWriter, r *http.Request) {
	features, warnings, err := listFeatures(h.features)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "list features", nil)
		return
	}
	summaries := make([]FeatureSummary, 0, len(features))
	for _, f := range features {
		summary := summarizeFeature(f)
		summary.Warnings = append(summary.Warnings, effortDriftWarnings(f, h.registry)...)
		summaries = append(summaries, summary)
	}
	revision := revisionForAny(struct {
		Features []FeatureSummary
		Warnings []WarningDTO
	}{Features: summaries, Warnings: warnings})
	h.writeRevisionedJSON(w, r, revision, FeatureListResponse{
		APIVersion: APIVersion,
		Meta:       h.responseMeta(revision),
		Features:   summaries,
		Warnings:   warnings,
	})
}

func (h *apiHandler) handleFeatureRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, apiPathFeatures+"/"))
	if invalidPathParts(parts) || len(parts) == 0 || !validEntityID(parts[0]) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid feature id", nil)
		return
	}
	featureID := parts[0]
	if len(parts) > 1 && parts[1] == "reviews" {
		h.handleReviewSessionRoute(w, r, featureID, parts[2:])
		return
	}
	if len(parts) == 3 && parts[2] == "preflight" {
		switch parts[1] {
		case "rebase":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
				return
			}
			h.handleRebasePreflight(w, r, featureID)
			return
		case "refactor":
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", "POST")
				writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
				return
			}
			h.handleRefactorPreflight(w, r, featureID)
			return
		}
	}
	if h.handleFeatureMutationRoute(w, r, featureID, parts[1:]) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	switch {
	case len(parts) == 1:
		h.handleFeatureDetail(w, r, featureID)
	case len(parts) == 2 && parts[1] == routeSegmentConfig:
		h.handleFeatureConfig(w, r, featureID)
	case len(parts) == 2 && parts[1] == "live-preview":
		h.handleLivePreview(w, r, featureID)
	case len(parts) == 3 && parts[1] == "rewind" && parts[2] == "preview":
		h.handleRewindPreview(w, r, featureID)
	case len(parts) >= 2 && parts[1] == "runs":
		h.handleRunsRoute(w, r, featureID, parts[2:])
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found", nil)
	}
}

// handleRunsRoute dispatches the run-scoped history surface under
// /api/v1/features/{feature_id}/runs. All branches are GET-only: the
// caller (handleFeatureRoutes) rejects non-GET methods before reaching
// here, so run history exposes no mutation operation.
func (h *apiHandler) handleRunsRoute(w http.ResponseWriter, r *http.Request, featureID string, parts []string) {
	switch {
	case len(parts) == 0:
		h.handleRunList(w, r, featureID)
	case len(parts) == 1:
		runNumber, ok := parseRunNumber(parts[0])
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid run number", map[string]any{"feature_id": featureID})
			return
		}
		h.handleRunDetail(w, r, featureID, runNumber)
	case len(parts) >= 2:
		runNumber, ok := parseRunNumber(parts[0])
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid run number", map[string]any{"feature_id": featureID})
			return
		}
		h.handleRunRoute(w, r, featureID, runNumber, parts[1:])
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found", nil)
	}
}

func (h *apiHandler) handleRunRoute(w http.ResponseWriter, r *http.Request, featureID string, runNumber int, parts []string) {
	switch {
	case len(parts) == 1 && parts[0] == "artifacts":
		h.handleArtifactList(w, r, featureID, runNumber)
	case len(parts) == 2 && parts[0] == "artifacts":
		h.handleArtifactContent(w, r, featureID, runNumber, parts[1])
	case len(parts) == 2 && parts[0] == "logs":
		h.handleLogContent(w, r, featureID, runNumber, parts[1])
	case len(parts) == 1 && parts[0] == "sessions":
		h.handleRunSessions(w, r, featureID, runNumber)
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found", nil)
	}
}

func (h *apiHandler) handleFeatureDetail(w http.ResponseWriter, r *http.Request, featureID string) {
	f, err := h.loadFeature(featureID)
	if err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	detail := h.featureDetailDTO(f)
	revision := revisionForAny(detail)
	detail.Revision = revision
	detail.CacheRevalidate = "etag"
	h.writeRevisionedJSON(w, r, revision, FeatureDetailResponse{
		APIVersion: APIVersion,
		Meta:       h.responseMeta(revision),
		Feature:    detail,
	})
}

func (h *apiHandler) handleSessionRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, apiPathSessions+"/"))
	if invalidPathParts(parts) || len(parts) == 0 || !validEntityID(parts[0]) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid session id", nil)
		return
	}
	switch {
	case len(parts) == 1:
		h.handleSessionDetail(w, r, parts[0])
	case len(parts) == 2 && parts[1] == "transcript":
		h.handleTranscript(w, r, parts[0])
	case len(parts) == 3 && parts[1] == "output" && parts[2] == "stream":
		h.handleSessionOutputStream(w, r, parts[0])
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found", nil)
	}
}

func (h *apiHandler) loadFeature(featureID string) (*feature.Feature, error) {
	if h.store == nil {
		return nil, os.ErrNotExist
	}
	return h.store.Load(featureID)
}

func (h *apiHandler) featureAndRun(featureID string, runNumber int) (*feature.Feature, *feature.Run, error) {
	f, err := h.loadFeature(featureID)
	if err != nil {
		return nil, nil, err
	}
	run, err := h.store.LoadRun(featureID, runNumber)
	if err != nil {
		return nil, nil, err
	}
	return f, run, nil
}

func methodHandler(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
			return
		}
		fn(w, r)
	}
}

// requireMethod writes a 405 and returns false if r.Method doesn't match
// method, mirroring methodHandler for single-branch call sites that can't
// use the wrapper form.
func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	return false
}

func listFeatures(lister FeatureLister) ([]*feature.Feature, []WarningDTO, error) {
	if lister == nil {
		return nil, nil, nil
	}
	features, err := lister.List()
	if err == nil {
		return features, nil, nil
	}
	var partial *feature.PartialLoadError
	if !errors.As(err, &partial) {
		return nil, nil, err
	}
	warnings := make([]WarningDTO, 0, len(partial.Warnings))
	for _, w := range partial.Warnings {
		warnings = append(warnings, WarningDTO{
			Code:      "partial_load",
			FeatureID: w.ID,
			Message:   "feature could not be loaded",
		})
	}
	return features, warnings, nil
}

func summarizeFeature(f *feature.Feature) FeatureSummary {
	if f == nil {
		return FeatureSummary{}
	}
	repos := make([]string, 0, len(f.Repos))
	for _, repo := range f.Repos {
		repos = append(repos, repo.Name)
	}
	return FeatureSummary{
		ID:           f.ID,
		Name:         f.Name,
		Slug:         f.Slug,
		Status:       f.Status.String(),
		CurrentPhase: f.CurrentPhase.String(),
		Cycle:        activeCycleDTO(f),
		ActiveRun:    f.ActiveRun,
		RunCount:     f.RunCount,
		Repos:        repos,
		CreatedAt:    f.Created,
		Checkpoints:  checkpointsDTO(f.Checkpoints),
		Progress: FeatureProgress{
			CurrentIteration:    f.CurrentIteration,
			CurrentRoadmapPhase: f.CurrentRoadmapPhase,
			TotalRoadmapPhases:  f.TotalRoadmapPhases,
			CurrentPhaseStatus:  f.CurrentPhaseStatus,
		},
	}
}

func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func invalidPathParts(parts []string) bool {
	for _, part := range parts {
		if part == "." || part == ".." || strings.ContainsAny(part, `\`) {
			return true
		}
	}
	return false
}

func parseRunNumber(raw string) (int, bool) {
	n, err := strconv.Atoi(raw)
	return n, err == nil && n > 0
}

func validEntityID(id string) bool {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func writeStoreError(w http.ResponseWriter, err error, id string) {
	target := map[string]any{entityFeature + "_id": id}
	if errors.Is(err, os.ErrNotExist) {
		writeAPIError(w, http.StatusNotFound, "not_found", entityFeature+" not found", target)
		return
	}
	writeAPIError(w, http.StatusInternalServerError, "internal_error", "read "+entityFeature, target)
}

func (h *apiHandler) responseMeta(revision string) ResponseMeta {
	asOfSeq := h.currentSeq()
	return ResponseMeta{Revision: revision, GeneratedAt: time.Now().UTC(), AsOfSeq: asOfSeq}
}

func (h *apiHandler) writeRevisionedJSON(w http.ResponseWriter, r *http.Request, revision string, v any) {
	h.setSequenceHeader(w)
	if revision != "" {
		w.Header().Set("ETag", `"`+revision+`"`)
		if revisionMatches(r, revision) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *apiHandler) setSequenceHeader(w http.ResponseWriter) {
	w.Header().Set("X-Agentico-Seq", strconv.FormatUint(h.currentSeq(), 10))
}

func (h *apiHandler) currentSeq() uint64 {
	if h == nil || h.broker == nil {
		return 0
	}
	return h.broker.currentSeq()
}

func (h *apiHandler) rejectUnauthorized(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.authToken == "" || !authRequiredPath(r.URL.EscapedPath()) {
		return false
	}
	if h.authorized(r) {
		return false
	}
	writeAPIError(w, http.StatusUnauthorized, "unauthorized", http.StatusText(http.StatusUnauthorized), nil)
	return true
}

func (h *apiHandler) rejectInvalidHost(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.disableHostValidation {
		return false
	}
	if isAllowedLoopbackHost(r.Host) {
		return false
	}
	writeAPIError(w, http.StatusForbidden, "forbidden", "invalid host", nil)
	return true
}

func isAllowedLoopbackHost(raw string) bool {
	host := strings.TrimSpace(raw)
	if host == "" {
		return false
	}
	if split, _, err := net.SplitHostPort(host); err == nil {
		host = split
	} else if strings.HasPrefix(host, "[") && strings.Contains(host, "]") {
		end := strings.Index(host, "]")
		host = host[1:end]
	} else if strings.Count(host, ":") > 0 {
		return false
	}
	host = strings.Trim(host, "[]")
	switch strings.ToLower(host) {
	case "127.0.0.1", hostLocalhost, "::1":
		return true
	default:
		return false
	}
}

func (h *apiHandler) authorized(r *http.Request) bool {
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		if bearer := strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")); bearer != "" && constantTimeEqual(bearer, h.authToken) {
			return true
		}
	}
	if sseTokenFallbackAllowed(r.URL.EscapedPath()) {
		return constantTimeEqual(r.URL.Query().Get("access_token"), h.authToken)
	}
	return false
}

func authRequiredPath(path string) bool {
	// /api/v1/health is liveness-only (status, runtime identity, launch
	// policy, start time, owner — no secrets) and must be reachable without
	// a token: PrepareDiscovery probes it with the *discovery record's*
	// token, and a stale/rotated token there must not make a genuinely
	// healthy, running server look dead and invite a Replace attempt
	// against it (see discoveryHealth in discovery.go).
	return strings.HasPrefix(path, "/api/v1/") && path != apiPathHealth
}

// sseTokenFallbackAllowed reports whether the given path may authenticate via
// the access_token query parameter instead of an Authorization header.
//
// Any request/access logging added to this server must avoid writing raw URLs
// for these paths because the access_token query parameter is a bearer
// credential and must never be persisted to a log, trace span, or crash report.
func sseTokenFallbackAllowed(path string) bool {
	return path == apiPathEvents || strings.HasSuffix(path, "/output/stream")
}

func constantTimeEqual(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func revisionMatches(r *http.Request, revision string) bool {
	if r.URL.Query().Get("revision") == revision {
		return true
	}
	for _, part := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		if strings.Trim(strings.TrimSpace(part), `"`) == revision {
			return true
		}
	}
	return false
}

func writeAPIError(w http.ResponseWriter, status int, code, message string, target map[string]any) {
	writeJSON(w, status, ErrorResponse{
		APIVersion: APIVersion,
		Error: ErrorDTO{
			Code:    code,
			Message: message,
			Status:  status,
			Target:  target,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
