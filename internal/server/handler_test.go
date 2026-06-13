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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestFeatureListDTOShapeAndNoAuthentication(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)
	f := &feature.Feature{
		ID:                  "feat-001",
		Name:                "Feature One",
		Slug:                "feature-one",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		Created:             created,
		ActiveRun:           2,
		RunCount:            3,
		CurrentIteration:    4,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  5,
		Repos: []feature.FeatureRepo{
			{Name: "agentic-orchestrator", Path: "/repo/path", WorktreePath: "/worktree/path", Branch: "feature/secret"},
		},
	}
	handler := NewHandler(HandlerOptions{
		Runtime: RuntimeIdentity{RuntimeDir: "/runtime", StateDir: "/runtime/features", Config: "/runtime/config.yaml"},
		Features: featureListerFunc(func() ([]*feature.Feature, error) {
			return []*feature.Feature{f}, nil
		}),
		StartedAt: created,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/features", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q; want JSON", got)
	}
	var body FeatureListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.APIVersion != APIVersion {
		t.Fatalf("api_version = %q; want %q", body.APIVersion, APIVersion)
	}
	if len(body.Features) != 1 {
		t.Fatalf("features length = %d; want 1", len(body.Features))
	}
	got := body.Features[0]
	if got.ID != f.ID || got.Name != f.Name || got.Slug != f.Slug {
		t.Fatalf("summary identity = %+v; want feature identity", got)
	}
	if got.Status != "Implementing" || got.CurrentPhase != "Implement" {
		t.Fatalf("summary status/phase = %q/%q; want Implementing/Implement", got.Status, got.CurrentPhase)
	}
	if got.ActiveRun != 2 || got.RunCount != 3 || got.Progress.CurrentIteration != 4 {
		t.Fatalf("summary progress = %+v; want run/progress fields", got)
	}
	if len(got.Repos) != 1 || got.Repos[0] != "agentic-orchestrator" {
		t.Fatalf("summary repos = %v; want repo names only", got.Repos)
	}
	raw := w.Body.String()
	for _, forbidden := range []string{"/repo/path", "/worktree/path", "feature/secret", "models", "exit_criteria", "permissions_queue"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("response leaks internal field %q in:\n%s", forbidden, raw)
		}
	}
}

func TestFeatureListEmptyRuntimeAndPartialWarnings(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{
		Runtime: RuntimeIdentity{RuntimeDir: "/runtime", StateDir: "/runtime/features", Config: "/runtime/config.yaml"},
		Features: featureListerFunc(func() ([]*feature.Feature, error) {
			return nil, nil
		}),
	})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features", nil))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("empty status = %d; want 200", w.Result().StatusCode)
	}
	var empty FeatureListResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&empty); err != nil {
		t.Fatalf("decode empty response: %v", err)
	}
	if len(empty.Features) != 0 || len(empty.Warnings) != 0 {
		t.Fatalf("empty response = %+v; want empty features and warnings", empty)
	}

	partial := &feature.PartialLoadError{Warnings: []feature.LoadWarning{
		{ID: "bad-001", Err: errors.New("parsing feature file: duplicate key")},
	}}
	handler = NewHandler(HandlerOptions{
		Runtime: RuntimeIdentity{RuntimeDir: "/runtime", StateDir: "/runtime/features", Config: "/runtime/config.yaml"},
		Features: featureListerFunc(func() ([]*feature.Feature, error) {
			return []*feature.Feature{{ID: "good-001", Name: "Good", Slug: "good", Status: feature.StatusCreated, ActiveRun: 1, RunCount: 1}}, partial
		}),
	})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features", nil))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("partial status = %d; want 200", w.Result().StatusCode)
	}
	var body FeatureListResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode partial response: %v", err)
	}
	if len(body.Features) != 1 || body.Features[0].ID != "good-001" {
		t.Fatalf("partial features = %+v; want good feature", body.Features)
	}
	if len(body.Warnings) != 1 || body.Warnings[0].FeatureID != "bad-001" || body.Warnings[0].Code != "partial_load" {
		t.Fatalf("partial warnings = %+v; want structured partial load warning", body.Warnings)
	}
}

func TestFeatureListDoesNotMutateStorageSchemas(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := feature.NewStore(dir)
	f := &feature.Feature{
		ID:            "readonly-001",
		Name:          "Read Only",
		Slug:          "read-only",
		Status:        feature.StatusCreated,
		CurrentPhase:  feature.PhaseResearch,
		Created:       time.Now().UTC().Truncate(time.Second),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	featurePath := filepath.Join(dir, f.ID, "feature.yaml")
	runPath := filepath.Join(dir, f.ID, "runs", feature.RunDirName(1), "run.yaml")
	beforeFeature, err := os.ReadFile(featurePath)
	if err != nil {
		t.Fatalf("ReadFile(feature before): %v", err)
	}
	beforeRun, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("ReadFile(run before): %v", err)
	}

	handler := NewHandler(HandlerOptions{
		Runtime:  RuntimeIdentity{RuntimeDir: filepath.Dir(dir), StateDir: dir, Config: filepath.Join(filepath.Dir(dir), "config.yaml")},
		Features: store,
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features", nil))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Result().StatusCode)
	}
	afterFeature, err := os.ReadFile(featurePath)
	if err != nil {
		t.Fatalf("ReadFile(feature after): %v", err)
	}
	afterRun, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("ReadFile(run after): %v", err)
	}
	if !bytes.Equal(beforeFeature, afterFeature) {
		t.Fatalf("feature.yaml mutated by read-only endpoint\nbefore:\n%s\nafter:\n%s", beforeFeature, afterFeature)
	}
	if !bytes.Equal(beforeRun, afterRun) {
		t.Fatalf("run.yaml mutated by read-only endpoint\nbefore:\n%s\nafter:\n%s", beforeRun, afterRun)
	}
}

type featureListerFunc func() ([]*feature.Feature, error)

func (f featureListerFunc) List() ([]*feature.Feature, error) {
	if f == nil {
		return nil, nil
	}
	return f()
}

func jsonResponse(status int, v any) (*http.Response, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(v); err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: status,
		Body:       ioNopCloser{Reader: bytes.NewReader(body.Bytes())},
		Header:     make(http.Header),
	}, nil
}

type ioNopCloser struct {
	*bytes.Reader
}

func (ioNopCloser) Close() error { return nil }
