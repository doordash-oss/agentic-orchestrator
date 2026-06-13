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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func NewHandler(opts HandlerOptions) http.Handler {
	startedAt := opts.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", methodHandler(http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, HealthResponse{
			APIVersion: APIVersion,
			Status:     "ok",
			Runtime:    opts.Runtime,
			StartedAt:  startedAt,
			ServerTime: time.Now().UTC(),
		})
	}))
	mux.HandleFunc("/api/v1/features", methodHandler(http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
		features, warnings, err := listFeatures(opts.Features)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list features"})
			return
		}
		summaries := make([]FeatureSummary, 0, len(features))
		for _, f := range features {
			summaries = append(summaries, summarizeFeature(f))
		}
		writeJSON(w, http.StatusOK, FeatureListResponse{
			APIVersion: APIVersion,
			Features:   summaries,
			Warnings:   warnings,
		})
	}))
	return mux
}

func methodHandler(method string, fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
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
		Progress: FeatureProgress{
			CurrentIteration:    f.CurrentIteration,
			CurrentRoadmapPhase: f.CurrentRoadmapPhase,
			TotalRoadmapPhases:  f.TotalRoadmapPhases,
			CurrentPhaseStatus:  f.CurrentPhaseStatus,
		},
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
