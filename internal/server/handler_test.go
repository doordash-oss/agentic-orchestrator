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
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
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
		Checkpoints:         feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "agentic-orchestrator", Path: "/repo/path", WorktreePath: "/worktree/path", Branch: "feature/secret"},
		},
	}
	handler := NewHandler(HandlerOptions{
		Runtime: RuntimeIdentity{RuntimeDir: "/runtime", StateDir: "/runtime/features", Config: "/runtime/config.yaml"},
		Features: featureListerFunc(func() ([]*feature.Feature, error) {
			return []*feature.Feature{f}, nil
		}),
		StartedAt:             created,
		DisableHostValidation: true,
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
	if !got.Checkpoints.ManualPublish {
		t.Fatalf("summary checkpoints = %+v; want manual_publish=true", got.Checkpoints)
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

func TestSnapshotResponsesExposeAsOfSequence(t *testing.T) {
	t.Parallel()

	handler := newAPIHandler(HandlerOptions{
		Runtime: RuntimeIdentity{RuntimeDir: "/runtime", StateDir: "/runtime/features", Config: "/runtime/config.yaml"},
		Features: featureListerFunc(func() ([]*feature.Feature, error) {
			return nil, nil
		}),
		DisableHostValidation: true,
	})
	handler.broker.publish(SSEEventDTO{Kind: "feature.state", Resource: ResourceDTO{Type: "feature", ID: "feat-1"}})

	w := httptest.NewRecorder()
	handler.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/features", nil))

	resp := w.Result()
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Agentico-Seq"); got != "1" {
		t.Fatalf("X-Agentico-Seq = %q, want 1", got)
	}
	var body FeatureListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Meta.AsOfSeq != 1 {
		t.Fatalf("meta.as_of_seq = %d, want 1", body.Meta.AsOfSeq)
	}
}

func TestFeatureListEmptyRuntimeAndPartialWarnings(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{
		Runtime: RuntimeIdentity{RuntimeDir: "/runtime", StateDir: "/runtime/features", Config: "/runtime/config.yaml"},
		Features: featureListerFunc(func() ([]*feature.Feature, error) {
			return nil, nil
		}),
		DisableHostValidation: true,
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
		DisableHostValidation: true,
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
		Runtime:               RuntimeIdentity{RuntimeDir: filepath.Dir(dir), StateDir: dir, Config: filepath.Join(filepath.Dir(dir), "config.yaml")},
		Features:              store,
		DisableHostValidation: true,
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

func TestModelCatalogIncludesChatUtilityEligibility(t *testing.T) {
	t.Parallel()

	reg := llm.NewRegistry()
	reg.Register(&testCatalogProvider{
		name: "codex",
		catalog: []llm.ModelInfo{
			{ID: "gpt-5.4", Category: "capable"},
			{ID: "gpt-5.4-mini", Category: "balanced"},
		},
	})
	handler := NewHandler(HandlerOptions{Registry: reg, DisableHostValidation: true})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/models", nil))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Result().StatusCode)
	}
	var body ModelCatalogResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	chatModels := body.PhaseProviderModels[string(llm.PhaseChat)]["codex"]
	if len(chatModels) != 1 || chatModels[0] != "gpt-5.4-mini" {
		t.Fatalf("chat utility models = %+v, want discovered balanced utility model", chatModels)
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

type testCatalogProvider struct {
	name    string
	catalog []llm.ModelInfo
}

func (p *testCatalogProvider) Name() string { return p.name }

func (p *testCatalogProvider) MatchesModel(model string) bool {
	for _, info := range p.catalog {
		if info.ID == model || p.name+":"+info.ID == model {
			return true
		}
	}
	return false
}

func (p *testCatalogProvider) DetectCLI() bool { return true }

func (p *testCatalogProvider) AvailableModels() []string {
	models := make([]string, 0, len(p.catalog))
	for _, info := range p.catalog {
		models = append(models, info.ID)
	}
	return models
}

func (p *testCatalogProvider) BuildCommand(llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}

func (p *testCatalogProvider) NewProtocol(llm.ProtocolOpts) llm.Protocol { return nil }
func (p *testCatalogProvider) InstallHint() string                       { return "" }
func (p *testCatalogProvider) VersionInfo() (string, error)              { return "", nil }
func (p *testCatalogProvider) MinVersion() [3]int                        { return [3]int{} }
func (p *testCatalogProvider) EnvVarsToExclude() []string                { return nil }
func (p *testCatalogProvider) ModelCatalog() []llm.ModelInfo             { return p.catalog }
