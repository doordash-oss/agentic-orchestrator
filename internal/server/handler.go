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
	"errors"
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
	runtime         RuntimeIdentity
	policy          LaunchPolicy
	startedAt       time.Time
	owner           instancelock.Owner
	features        FeatureLister
	store           FeatureReader
	cfg             *config.Config
	registry        *llm.Registry
	sessions        ports.SessionManager
	broker          *eventBroker
	operations      *OperationRegistry
	mutations       *mutationExecutor
	requestShutdown func()

	recoveryMu        sync.Mutex
	recoverySnapshots map[string][]ports.RecoveryItem
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
		runtime:         opts.Runtime,
		policy:          opts.LaunchPolicy,
		startedAt:       startedAt,
		owner:           opts.Owner,
		features:        features,
		store:           store,
		cfg:             opts.Config,
		registry:        opts.Registry,
		sessions:        opts.Sessions,
		broker:          newEventBroker(opts.Events, opts.DomainEvents),
		operations:      opts.Operations,
		requestShutdown: opts.RequestShutdown,
	}
	if opts.Operations != nil && opts.Mutations != nil {
		handler.mutations = newMutationExecutor(opts.Operations, opts.Mutations, opts.MutationLimits, handler.publishOperationUpdate)
	}
	return handler
}

func (h *apiHandler) routes() http.Handler {
	return h.routesWithMCP(true)
}

func (h *apiHandler) restRoutes() http.Handler {
	return h.routesWithMCP(false)
}

func (h *apiHandler) routesWithMCP(includeMCP bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", methodHandler(http.MethodGet, h.handleHealth))
	mux.HandleFunc("/api/v1/features", h.handleFeaturesRoot)
	mux.HandleFunc("/api/v1/features/", h.handleFeatureRoutes)
	mux.HandleFunc("/api/v1/config/runtime", h.handleRuntimeConfigRoute)
	mux.HandleFunc("/api/v1/catalog/models", methodHandler(http.MethodGet, h.handleModelCatalog))
	mux.HandleFunc("/api/v1/prompts", methodHandler(http.MethodGet, h.handlePrompts))
	mux.HandleFunc("/api/v1/prompts/", h.handlePromptMutationRoutes)
	mux.HandleFunc("/api/v1/permissions", methodHandler(http.MethodGet, h.handlePermissions))
	mux.HandleFunc("/api/v1/permissions/", h.handlePermissionMutationRoutes)
	mux.HandleFunc("/api/v1/sessions", methodHandler(http.MethodGet, h.handleSessionList))
	mux.HandleFunc("/api/v1/sessions/", methodHandler(http.MethodGet, h.handleSessionRoutes))
	mux.HandleFunc("/api/v1/operations", methodHandler(http.MethodGet, h.handleOperations))
	mux.HandleFunc("/api/v1/recovery", h.handleRecoveryRoute)
	mux.HandleFunc("/api/v1/recovery/actions", h.handleRecoveryActionRoute)
	mux.HandleFunc("/api/v1/shutdown", h.handleShutdownMutationRoute)
	mux.HandleFunc("/api/v1/events", methodHandler(http.MethodGet, h.handleEvents))
	if includeMCP {
		mux.Handle(MCPEndpointPath, h.mcpHTTPHandler())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.handleMutationPreflight(w, r) {
			return
		}
		if h.applyMutationCORS(w, r) {
			return
		}
		escaped := r.URL.EscapedPath()
		if strings.HasPrefix(escaped, "/api/v1/features/") {
			h.handleFeatureRoutes(w, r)
			return
		}
		if strings.HasPrefix(escaped, "/api/v1/sessions/") {
			methodHandler(http.MethodGet, h.handleSessionRoutes)(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (h *apiHandler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		APIVersion:   APIVersion,
		Status:       "ok",
		Runtime:      h.runtime,
		LaunchPolicy: h.policy,
		StartedAt:    h.startedAt,
		Owner:        OwnerDTOFromInstanceOwner(h.owner),
		ServerTime:   time.Now().UTC(),
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
		summaries = append(summaries, summarizeFeature(f))
	}
	revision := revisionForAny(struct {
		Features []FeatureSummary
		Warnings []WarningDTO
	}{Features: summaries, Warnings: warnings})
	writeRevisionedJSON(w, r, http.StatusOK, revision, FeatureListResponse{
		APIVersion: APIVersion,
		Meta:       responseMeta(revision),
		Features:   summaries,
		Warnings:   warnings,
	})
}

func (h *apiHandler) handleFeatureRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1/features/"))
	if invalidPathParts(parts) || len(parts) == 0 || !validEntityID(parts[0]) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid feature id", nil)
		return
	}
	featureID := parts[0]
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
	case len(parts) == 2 && parts[1] == "config":
		h.handleFeatureConfig(w, r, featureID)
	case len(parts) == 2 && parts[1] == "live-preview":
		h.handleLivePreview(w, r, featureID)
	case len(parts) >= 4 && parts[1] == "runs":
		runNumber, ok := parseRunNumber(parts[2])
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid run number", map[string]any{"feature_id": featureID})
			return
		}
		h.handleRunRoute(w, r, featureID, runNumber, parts[3:])
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
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found", nil)
	}
}

func (h *apiHandler) handleFeatureDetail(w http.ResponseWriter, r *http.Request, featureID string) {
	f, err := h.loadFeature(featureID)
	if err != nil {
		writeStoreError(w, err, "feature", featureID)
		return
	}
	detail := h.featureDetailDTO(f)
	revision := revisionForAny(detail)
	detail.Revision = revision
	detail.CacheRevalidate = "etag"
	writeRevisionedJSON(w, r, http.StatusOK, revision, FeatureDetailResponse{
		APIVersion: APIVersion,
		Meta:       responseMeta(revision),
		Feature:    detail,
	})
}

func (h *apiHandler) handleSessionRoutes(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/"))
	if invalidPathParts(parts) || len(parts) == 0 || !validEntityID(parts[0]) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid session id", nil)
		return
	}
	switch {
	case len(parts) == 1:
		h.handleSessionDetail(w, r, parts[0])
	case len(parts) == 2 && parts[1] == "transcript":
		h.handleTranscript(w, r, parts[0])
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

func methodHandler(method string, fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
			return
		}
		fn(w, r)
	}
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

func writeStoreError(w http.ResponseWriter, err error, entity, id string) {
	target := map[string]any{entity + "_id": id}
	if errors.Is(err, os.ErrNotExist) {
		writeAPIError(w, http.StatusNotFound, "not_found", entity+" not found", target)
		return
	}
	writeAPIError(w, http.StatusInternalServerError, "internal_error", "read "+entity, target)
}

func responseMeta(revision string) ResponseMeta {
	return ResponseMeta{Revision: revision, GeneratedAt: time.Now().UTC()}
}

func writeRevisionedJSON(w http.ResponseWriter, r *http.Request, status int, revision string, v any) {
	if revision != "" {
		w.Header().Set("ETag", `"`+revision+`"`)
		if revisionMatches(r, revision) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	writeJSON(w, status, v)
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
