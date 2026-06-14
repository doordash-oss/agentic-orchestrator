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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestOperationRegistryPersistsPaginatesFiltersAndRedacts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registry, err := NewOperationRegistry(OperationRegistryOptions{Dir: filepath.Join(dir, "operations"), DefaultLimit: 2, MaxLimit: 3})
	if err != nil {
		t.Fatalf("NewOperationRegistry() error = %v", err)
	}

	var ids []string
	for _, seed := range []OperationRecord{
		{Kind: "feature.create", Target: OperationTarget{Type: "runtime"}, Result: map[string]string{"feature_id": "feat-created"}},
		{Kind: "feature.start", Target: OperationTarget{Type: "feature", FeatureID: "feat-a"}},
		{Kind: "feature.stop", Target: OperationTarget{Type: "feature", FeatureID: "feat-b"}, Error: &OperationError{Code: "failed", Message: "private-token /tmp/raw.log"}},
		{Kind: "feature.config.update", Target: OperationTarget{Type: "feature", FeatureID: "feat-a"}, Result: map[string]string{"payload": "token=private-token"}},
	} {
		rec, err := registry.Create(seed.Kind, seed.Target)
		if err != nil {
			t.Fatalf("Create(%s) error = %v", seed.Kind, err)
		}
		ids = append(ids, rec.ID)
		if seed.Error != nil {
			if err := registry.Complete(rec.ID, OperationStatusFailed, nil, seed.Error); err != nil {
				t.Fatalf("Complete failed op: %v", err)
			}
		} else if err := registry.Complete(rec.ID, OperationStatusSucceeded, seed.Result, nil); err != nil {
			t.Fatalf("Complete succeeded op: %v", err)
		}
	}
	running, err := registry.Create("feature.start", OperationTarget{Type: "feature", FeatureID: "feat-stale"})
	if err != nil {
		t.Fatalf("Create running error = %v", err)
	}
	if err := registry.UpdateStatus(running.ID, OperationStatusRunning); err != nil {
		t.Fatalf("UpdateStatus running: %v", err)
	}

	rawFiles, err := os.ReadDir(filepath.Join(dir, "operations"))
	if err != nil {
		t.Fatalf("ReadDir(operations): %v", err)
	}
	for _, entry := range rawFiles {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "operations", entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", entry.Name(), err)
		}
		raw := string(data)
		if strings.Contains(raw, "private-token") || strings.Contains(raw, "/tmp/raw.log") {
			t.Fatalf("operation record %s leaked unsafe data:\n%s", entry.Name(), raw)
		}
	}

	restarted, err := NewOperationRegistry(OperationRegistryOptions{Dir: filepath.Join(dir, "operations"), DefaultLimit: 2, MaxLimit: 3})
	if err != nil {
		t.Fatalf("restart NewOperationRegistry() error = %v", err)
	}
	if got, wantMax := len(restarted.records), 3; got > wantMax {
		t.Fatalf("restart hydrated %d operation records; want at most %d for stale active plus default page window", got, wantMax)
	}
	page, err := restarted.List(OperationListOptions{})
	if err != nil {
		t.Fatalf("List(default) error = %v", err)
	}
	if len(page.Operations) != 2 {
		t.Fatalf("default page length = %d; want bounded default 2", len(page.Operations))
	}
	if page.NextCursor == "" {
		t.Fatalf("default page next cursor is empty; want cursor for remaining history")
	}
	if page.Operations[0].ID != running.ID || page.Operations[0].Status != OperationStatusInterrupted {
		t.Fatalf("stale operation = %+v; want newest interrupted record %s", page.Operations[0], running.ID)
	}

	filtered, err := restarted.List(OperationListOptions{FeatureID: "feat-a", Kind: "feature.config.update", Limit: 10})
	if err != nil {
		t.Fatalf("List(filtered) error = %v", err)
	}
	if len(filtered.Operations) != 1 || filtered.Operations[0].Kind != "feature.config.update" || filtered.Operations[0].Target.FeatureID != "feat-a" {
		t.Fatalf("filtered operations = %+v; want feature config update for feat-a", filtered.Operations)
	}

	cursorPage, err := restarted.List(OperationListOptions{Cursor: page.NextCursor, Limit: 3})
	if err != nil {
		t.Fatalf("List(cursor) error = %v", err)
	}
	if len(cursorPage.Operations) == 0 {
		t.Fatalf("cursor page empty; want older terminal history including %v", ids)
	}
}

func TestOperationRegistryUsesMetadataIndexWithoutHydratingUnrelatedRecords(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	operationsDir := filepath.Join(dir, "operations")
	if err := os.MkdirAll(operationsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(operations): %v", err)
	}
	index := `schema_version: 1
operations:
    - id: op-005
      kind: feature.start
      target:
        type: feature
        feature_id: feat-stale
      requested_at: 2026-01-01T00:00:06Z
      updated_at: 2026-01-01T00:00:06Z
      status: running
    - id: op-004
      kind: feature.config.update
      target:
        type: feature
        feature_id: feat-target
      requested_at: 2026-01-01T00:00:04Z
      updated_at: 2026-01-01T00:00:05Z
      completed_at: 2026-01-01T00:00:05Z
      status: succeeded
      result:
        feature_id: feat-target
        status: updated
    - id: op-003
      kind: feature.stop
      target:
        type: feature
        feature_id: feat-other
      requested_at: 2026-01-01T00:00:03Z
      updated_at: 2026-01-01T00:00:04Z
      completed_at: 2026-01-01T00:00:04Z
      status: failed
      error:
        code: failed
        message: operation failed
    - id: op-002
      kind: feature.start
      target:
        type: feature
        feature_id: feat-other
      requested_at: 2026-01-01T00:00:02Z
      updated_at: 2026-01-01T00:00:03Z
      completed_at: 2026-01-01T00:00:03Z
      status: succeeded
    - id: op-001
      kind: feature.start
      target:
        type: feature
        feature_id: feat-target
      requested_at: 2026-01-01T00:00:01Z
      updated_at: 2026-01-01T00:00:02Z
      completed_at: 2026-01-01T00:00:02Z
      status: succeeded
`
	if err := os.WriteFile(filepath.Join(operationsDir, "index.yml"), []byte(index), 0o644); err != nil {
		t.Fatalf("WriteFile(index.yml): %v", err)
	}
	staleRecord := `schema_version: 1
id: op-005
kind: feature.start
target:
    type: feature
    feature_id: feat-stale
requested_at: 2026-01-01T00:00:06Z
updated_at: 2026-01-01T00:00:06Z
status: running
`
	if err := os.WriteFile(filepath.Join(operationsDir, "op-005.yaml"), []byte(staleRecord), 0o644); err != nil {
		t.Fatalf("WriteFile(stale record): %v", err)
	}
	if err := os.WriteFile(filepath.Join(operationsDir, "op-003.yaml"), []byte(":\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(corrupt record): %v", err)
	}

	registry, err := NewOperationRegistry(OperationRegistryOptions{Dir: operationsDir, DefaultLimit: 1, MaxLimit: 10})
	if err != nil {
		t.Fatalf("NewOperationRegistry() error = %v", err)
	}
	if got := len(registry.records); got != 0 {
		t.Fatalf("restart hydrated %d full operation records; want no terminal record hydration from compact metadata index", got)
	}
	page, err := registry.List(OperationListOptions{FeatureID: "feat-target", Kind: "feature.config.update", Limit: 10})
	if err != nil {
		t.Fatalf("List(filtered) error = %v", err)
	}
	if len(page.Operations) != 1 || page.Operations[0].ID != "op-004" {
		t.Fatalf("filtered operations = %+v; want indexed op-004 only", page.Operations)
	}
	stale, err := registry.List(OperationListOptions{FeatureID: "feat-stale", State: OperationStatusInterrupted, Limit: 10})
	if err != nil {
		t.Fatalf("List(stale) error = %v", err)
	}
	if len(stale.Operations) != 1 || stale.Operations[0].ID != "op-005" || stale.Operations[0].Error == nil {
		t.Fatalf("stale operations = %+v; want indexed op-005 marked interrupted", stale.Operations)
	}
	defaultPage, err := registry.List(OperationListOptions{})
	if err != nil {
		t.Fatalf("List(default) error = %v", err)
	}
	if len(defaultPage.Operations) != 1 || defaultPage.Operations[0].ID != "op-005" || defaultPage.Operations[0].Status != OperationStatusInterrupted || defaultPage.NextCursor == "" {
		t.Fatalf("default page = %+v cursor %q; want bounded indexed first page with next cursor", defaultPage.Operations, defaultPage.NextCursor)
	}
}

func TestMutationAdmissionCreatesOperationsAndRejectsInvalidBeforeHistory(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	mutations := &fakeMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  mutations,
	})

	invalid := requestJSONMap(t, handler, http.MethodPost, "/api/v1/features", strings.NewReader(`{"description":"missing name"}`), http.StatusBadRequest)
	if invalid["error"].(map[string]any)["code"] != "bad_request" {
		t.Fatalf("invalid DTO response = %+v; want bad_request", invalid)
	}
	assertOperationCount(t, handler, 0)

	accepted := mutationJSONMap(t, handler, http.MethodPost, "/api/v1/features", `{
		"name":"REST Feature",
		"description":"desc",
		"repos":["agentic-orchestrator"],
		"models":{"research":"claude:opus","planning":"claude:sonnet","implementation":"codex:gpt-5.4","review":"claude:haiku","utilities":"claude:haiku","kb_build":"claude:opus"},
		"exit_criteria":"all acceptance checks pass",
		"inquireness":"high",
		"images":["/tmp/image-1.png"],
		"attachments":["/tmp/spec.md"],
		"use_current_branch":true,
		"use_current_branch_per_repo":{"agentic-orchestrator":true},
		"risk_level":"high",
		"pipeline":"medium",
		"checkpoints":{"inquiry_review":true,"roadmap_review":true,"phase_plan_review":true,"manual_publish":false}
	}`, "", "")
	opID := accepted["operation_id"].(string)
	if opID == "" {
		t.Fatalf("operation_id is empty in %+v", accepted)
	}
	op := waitForOperationStatus(t, handler, opID, OperationStatusSucceeded)
	if len(mutations.createCalls) != 1 || mutations.createCalls[0].Name != "REST Feature" {
		t.Fatalf("create calls = %+v; want one REST Feature call", mutations.createCalls)
	}
	createReq := mutations.createCalls[0]
	if createReq.Models.KBBuild != "claude:opus" || createReq.Models.Implementation != "codex:gpt-5.4" {
		t.Fatalf("create models = %+v; want decoded snake_case model fields", createReq.Models)
	}
	if createReq.ExitCriteria != "all acceptance checks pass" || createReq.Inquireness != "high" {
		t.Fatalf("create exit/inquireness = %q/%q; want REST values", createReq.ExitCriteria, createReq.Inquireness)
	}
	if !createReq.UseCurrentBranch || !createReq.UseCurrentBranchPerRepo["agentic-orchestrator"] {
		t.Fatalf("create current-branch choices = global %v per-repo %v; want true/true", createReq.UseCurrentBranch, createReq.UseCurrentBranchPerRepo)
	}
	if createReq.Pipeline != feature.PipelineMedium || createReq.RiskLevel != feature.RiskHigh {
		t.Fatalf("create pipeline/risk = %s/%s; want medium/high", createReq.Pipeline, createReq.RiskLevel)
	}
	if !createReq.Checkpoints.InquiryReview || !createReq.Checkpoints.RoadmapReview || !createReq.Checkpoints.PhasePlanReview || createReq.Checkpoints.ManualPublish {
		t.Fatalf("create checkpoints = %+v; want decoded review gates with manual publish false", createReq.Checkpoints)
	}
	if len(createReq.Images) != 1 || createReq.Images[0] != "/tmp/image-1.png" || len(createReq.Attachments) != 1 || createReq.Attachments[0] != "/tmp/spec.md" {
		t.Fatalf("create assets = images %v attachments %v; want forwarded REST assets", createReq.Images, createReq.Attachments)
	}
	result := op["result"].(map[string]any)
	if result["feature_id"] != "created-001" {
		t.Fatalf("operation result = %+v; want created feature id", result)
	}
}

func TestCreateFeatureMutationReturnsBeforeCreateWorkCompletes(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	mutations := &fakeMutationTarget{createHook: func() {
		close(started)
		<-release
	}}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  mutations,
	})

	done := make(chan map[string]any, 1)
	go func() {
		done <- mutationJSONMap(t, handler, http.MethodPost, "/api/v1/features", `{"name":"Async Feature"}`, "", "")
	}()

	<-started
	var accepted map[string]any
	select {
	case accepted = <-done:
	case <-time.After(100 * time.Millisecond):
		close(release)
		<-done
		t.Fatalf("POST /api/v1/features waited for CreateFeature work; want accepted response before create completes")
	}
	opID := accepted["operation_id"].(string)
	if opID == "" {
		t.Fatalf("operation_id is empty in %+v", accepted)
	}
	close(release)
	waitForOperationStatus(t, handler, opID, OperationStatusSucceeded)
}

func TestMutationTrustBoundaryAndRequestSizeRunBeforeOperationCreation(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  &fakeMutationTarget{},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/features", strings.NewReader(`{"name":"x","description":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusForbidden {
		t.Fatalf("missing trusted-client header status = %d; want 403", w.Result().StatusCode)
	}
	assertOperationCount(t, handler, 0)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/features", strings.NewReader(`{"name":"x","description":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	req.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusForbidden {
		t.Fatalf("non-loopback origin status = %d; want 403", w.Result().StatusCode)
	}
	assertOperationCount(t, handler, 0)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/features", strings.NewReader(`{"name":"x","description":"y"}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Agentico-Client", "local")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("simple content type status = %d; want 415", w.Result().StatusCode)
	}
	assertOperationCount(t, handler, 0)

	tooLarge := strings.Repeat("x", MaxMutationBodyBytes+1)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/features", strings.NewReader(tooLarge))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	req.Header.Set("Origin", "http://127.0.0.1:1234")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d; want 413", w.Result().StatusCode)
	}
	assertOperationCount(t, handler, 0)

	accepted := mutationJSONMap(t, handler, http.MethodPost, "/api/v1/features", `{"name":"ok","description":"ok"}`, "http://localhost:3000", "")
	waitForOperationStatus(t, handler, accepted["operation_id"].(string), OperationStatusSucceeded)
	assertOperationCount(t, handler, 1)
}

func TestMutationPreflightAllowsLoopbackOriginWithRequiredHeaders(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  &fakeMutationTarget{},
	})

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/features", nil)
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type, x-agentico-client")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d; want 204", resp.StatusCode)
	}
	assertAccessControlHeaders(t, resp.Header, map[string]string{
		"Access-Control-Allow-Origin":  "http://127.0.0.1:5173",
		"Access-Control-Allow-Methods": http.MethodPost,
		"Access-Control-Allow-Headers": "Content-Type, X-Agentico-Client",
	})
	assertOperationCount(t, handler, 0)
}

func TestMutationCORSActualResponsesForLoopbackOrigin(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  &fakeMutationTarget{},
	})

	rejected := httptest.NewRequest(http.MethodPost, "/api/v1/features", strings.NewReader(`{"name":"missing client"}`))
	rejected.Header.Set("Content-Type", "application/json")
	rejected.Header.Set("Origin", "http://localhost:5173")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, rejected)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing trusted client status = %d; want 403", resp.StatusCode)
	}
	assertAccessControlHeaders(t, resp.Header, map[string]string{
		"Access-Control-Allow-Origin": "http://localhost:5173",
	})
	assertOperationCount(t, handler, 0)

	accepted := httptest.NewRequest(http.MethodPost, "/api/v1/features", strings.NewReader(`{"name":"cors accepted"}`))
	accepted.Header.Set("Content-Type", "application/json")
	accepted.Header.Set("X-Agentico-Client", "local")
	accepted.Header.Set("Origin", "http://localhost:5173")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, accepted)
	resp = w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("accepted mutation status = %d; want 202; body: %s", resp.StatusCode, data)
	}
	assertAccessControlHeaders(t, resp.Header, map[string]string{
		"Access-Control-Allow-Origin": "http://localhost:5173",
	})
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode accepted mutation response: %v", err)
	}
	waitForOperationStatus(t, handler, body["operation_id"].(string), OperationStatusSucceeded)
	assertOperationCount(t, handler, 1)
}

func TestMutationCORSRejectsNonLoopbackAndDoesNotOpenReadRoutes(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  &fakeMutationTarget{},
	})

	preflight := httptest.NewRequest(http.MethodOptions, "/api/v1/features", nil)
	preflight.Header.Set("Origin", "https://evil.example")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	preflight.Header.Set("Access-Control-Request-Headers", "content-type, x-agentico-client")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, preflight)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-loopback preflight status = %d; want 403", resp.StatusCode)
	}
	assertNoAccessControlHeaders(t, resp.Header)
	assertOperationCount(t, handler, 0)

	mutation := httptest.NewRequest(http.MethodPost, "/api/v1/features", strings.NewReader(`{"name":"evil"}`))
	mutation.Header.Set("Content-Type", "application/json")
	mutation.Header.Set("X-Agentico-Client", "local")
	mutation.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, mutation)
	resp = w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-loopback mutation status = %d; want 403", resp.StatusCode)
	}
	assertNoAccessControlHeaders(t, resp.Header)
	assertOperationCount(t, handler, 0)

	read := httptest.NewRequest(http.MethodGet, "/api/v1/features", nil)
	read.Header.Set("Origin", "http://localhost:5173")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, read)
	resp = w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read route status = %d; want 200", resp.StatusCode)
	}
	assertNoAccessControlHeaders(t, resp.Header)

	srv := httptest.NewServer(handler)
	defer srv.Close()
	sseReq, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/events?heartbeat_ms=10000", nil)
	if err != nil {
		t.Fatalf("NewRequest SSE: %v", err)
	}
	sseReq.Header.Set("Origin", "http://localhost:5173")
	sseResp, err := srv.Client().Do(sseReq)
	if err != nil {
		t.Fatalf("GET SSE: %v", err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d; want 200", sseResp.StatusCode)
	}
	assertNoAccessControlHeaders(t, sseResp.Header)
}

func TestMutationLanesConflictBackpressureAndOperationSSE(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	mutations := &fakeMutationTarget{startHook: func() {
		close(started)
		<-release
	}}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  mutations,
		MutationLimits: MutationLimits{
			FeatureQueue:   0,
			RuntimeQueue:   0,
			AggregateQueue: 1,
		},
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := srv.Client()
	sseReq, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/events?heartbeat_ms=10", nil)
	if err != nil {
		t.Fatalf("NewRequest SSE: %v", err)
	}
	sseResp, err := client.Do(sseReq)
	if err != nil {
		t.Fatalf("GET SSE: %v", err)
	}
	defer sseResp.Body.Close()
	reader := bufioReader(sseResp.Body)
	readSSEBlock(t, reader, "connected")

	first := postMutationHTTP(t, client, srv.URL+"/api/v1/features/feat-1/start", `{}`)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first start status = %d; want 202", first.StatusCode)
	}
	firstBody := decodeHTTPJSONMap(t, first)
	firstOpID := firstBody["operation_id"].(string)
	<-started

	conflict := postMutationHTTP(t, client, srv.URL+"/api/v1/features/feat-1/start", `{}`)
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("same-feature conflict status = %d; want 409", conflict.StatusCode)
	}
	conflictBody := decodeHTTPJSONMap(t, conflict)
	conflictID := conflictBody["operation_id"].(string)
	op := waitForOperationStatus(t, handler, conflictID, OperationStatusRejected)
	if op["status"] != string(OperationStatusRejected) {
		t.Fatalf("conflict operation = %+v; want rejected", op)
	}

	backpressure := postMutationHTTP(t, client, srv.URL+"/api/v1/features/feat-2/start", `{}`)
	if backpressure.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("aggregate backpressure status = %d; want 429", backpressure.StatusCode)
	}
	assertOperationCount(t, handler, 2)

	close(release)
	waitForOperationStatus(t, handler, firstOpID, OperationStatusSucceeded)
	block := readSSEBlock(t, reader, "operation.updated")
	if strings.Contains(block, "private-token") || !strings.Contains(block, firstOpID) {
		t.Fatalf("operation SSE block = %s; want metadata-only event with operation id", block)
	}
}

func TestMutationFeatureLaneQueuesWithinCapAndRejectsExcessBeforeHistory(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	mutations := &fakeMutationTarget{startHook: func() {
		started <- struct{}{}
		<-release
	}}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  mutations,
		MutationLimits: MutationLimits{
			FeatureQueue:   1,
			RuntimeQueue:   0,
			AggregateQueue: 10,
		},
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := srv.Client()

	first := postMutationHTTP(t, client, srv.URL+"/api/v1/features/feat-queue/start", `{}`)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first start status = %d; want 202", first.StatusCode)
	}
	firstID := decodeHTTPJSONMap(t, first)["operation_id"].(string)
	<-started

	second := postMutationHTTP(t, client, srv.URL+"/api/v1/features/feat-queue/stop", `{}`)
	if second.StatusCode != http.StatusAccepted {
		t.Fatalf("queued stop status = %d; want 202", second.StatusCode)
	}
	secondID := decodeHTTPJSONMap(t, second)["operation_id"].(string)
	secondQueued := waitForOperationStatus(t, handler, secondID, OperationStatusQueued)
	if secondQueued["status"] != string(OperationStatusQueued) {
		t.Fatalf("second operation = %+v; want queued", secondQueued)
	}
	select {
	case <-started:
		t.Fatalf("queued operation started before active feature-lane work finished")
	case <-time.After(25 * time.Millisecond):
	}

	excess := postMutationHTTP(t, client, srv.URL+"/api/v1/features/feat-queue/restart", `{}`)
	if excess.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("excess feature-lane waiter status = %d; want 429", excess.StatusCode)
	}
	assertOperationCount(t, handler, 2)

	close(release)
	waitForOperationStatus(t, handler, firstID, OperationStatusSucceeded)
	waitForOperationStatus(t, handler, secondID, OperationStatusSucceeded)
}

func TestMutationRuntimeLaneQueuesWithinCapAndRejectsExcessBeforeHistory(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	mutations := &fakeMutationTarget{
		runtimeConfigHook: func() {
			started <- struct{}{}
			<-release
		},
		featureConfigHook: func() {
			started <- struct{}{}
			<-release
		},
	}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  mutations,
		MutationLimits: MutationLimits{
			FeatureQueue:   0,
			RuntimeQueue:   1,
			AggregateQueue: 10,
		},
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := srv.Client()

	first := patchMutationHTTP(t, client, srv.URL+"/api/v1/config/runtime", `{"defaults":{"models":{"research":"opus"}}}`)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first runtime config status = %d; want 202", first.StatusCode)
	}
	firstID := decodeHTTPJSONMap(t, first)["operation_id"].(string)
	<-started

	second := postMutationHTTP(t, client, srv.URL+"/api/v1/features/feat-runtime/config", `{"models":{"planning":"sonnet"},"pipeline":"medium"}`)
	if second.StatusCode != http.StatusAccepted {
		t.Fatalf("queued feature config status = %d; want 202", second.StatusCode)
	}
	secondID := decodeHTTPJSONMap(t, second)["operation_id"].(string)
	waitForOperationStatus(t, handler, secondID, OperationStatusQueued)
	select {
	case <-started:
		t.Fatalf("feature config operation started while runtime-lane config work was active")
	case <-time.After(25 * time.Millisecond):
	}

	excess := patchMutationHTTP(t, client, srv.URL+"/api/v1/config/runtime", `{"defaults":{"models":{"implementation":"haiku"}}}`)
	if excess.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("excess runtime-lane waiter status = %d; want 429", excess.StatusCode)
	}
	assertOperationCount(t, handler, 2)

	close(release)
	waitForOperationStatus(t, handler, firstID, OperationStatusSucceeded)
	waitForOperationStatus(t, handler, secondID, OperationStatusSucceeded)
}

func TestSessionMutationsDoNotBlockBehindActiveRuntimeLane(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	mutations := &fakeMutationTarget{
		runtimeConfigHook: func() {
			close(started)
			<-release
		},
	}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  mutations,
		MutationLimits: MutationLimits{
			FeatureQueue:   0,
			RuntimeQueue:   3,
			AggregateQueue: 10,
		},
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := srv.Client()

	runtime := patchMutationHTTP(t, client, srv.URL+"/api/v1/config/runtime", `{"defaults":{"models":{"research":"opus"}}}`)
	if runtime.StatusCode != http.StatusAccepted {
		t.Fatalf("runtime config status = %d; want 202", runtime.StatusCode)
	}
	runtimeID := decodeHTTPJSONMap(t, runtime)["operation_id"].(string)
	<-started

	sessionRequests := []struct {
		name string
		path string
		body string
	}{
		{name: "permission", path: "/api/v1/permissions/answer", body: `{"request_id":"perm-1","session_id":"sess-1","decision":"allow"}`},
		{name: "ask_user", path: "/api/v1/prompts/ask-user/answer", body: `{"request_id":"ask-1","session_id":"sess-2","answers":{"Question?":"answer"}}`},
		{name: "help", path: "/api/v1/prompts/help/send", body: `{"feature_id":"feat-help","session_id":"sess-3","message":"continue"}`},
	}
	for _, tt := range sessionRequests {
		t.Run(tt.name, func(t *testing.T) {
			resp := postMutationHTTP(t, client, srv.URL+tt.path, tt.body)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("%s status = %d; want 202", tt.path, resp.StatusCode)
			}
			opID := decodeHTTPJSONMap(t, resp)["operation_id"].(string)
			waitForOperationStatus(t, handler, opID, OperationStatusSucceeded)
		})
	}

	if runtimeOp := getOperationByID(t, handler, runtimeID); runtimeOp["status"] != string(OperationStatusRunning) {
		t.Fatalf("runtime operation status = %v; want running while session operations complete", runtimeOp["status"])
	}
	close(release)
	waitForOperationStatus(t, handler, runtimeID, OperationStatusSucceeded)
}

func TestRuntimeServerCloseInterruptsActiveAndQueuedOperations(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	operations := mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations"))
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	mutations := &fakeMutationTarget{startHook: func() {
		close(started)
		<-release
	}}
	srv, err := Start(context.Background(), Options{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: operations,
		Mutations:  mutations,
		MutationLimits: MutationLimits{
			FeatureQueue:   1,
			RuntimeQueue:   0,
			AggregateQueue: 10,
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer releaseOnce.Do(func() { close(release) })

	client := http.DefaultClient
	first := postMutationHTTP(t, client, srv.BaseURL()+"/api/v1/features/feat-shutdown/start", `{}`)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first start status = %d; want 202", first.StatusCode)
	}
	firstID := decodeHTTPJSONMap(t, first)["operation_id"].(string)
	<-started

	second := postMutationHTTP(t, client, srv.BaseURL()+"/api/v1/features/feat-shutdown/stop", `{}`)
	if second.StatusCode != http.StatusAccepted {
		t.Fatalf("queued stop status = %d; want 202", second.StatusCode)
	}
	secondID := decodeHTTPJSONMap(t, second)["operation_id"].(string)
	waitForRegistryOperationStatus(t, operations, secondID, OperationStatusQueued)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	active := waitForRegistryOperationStatus(t, operations, firstID, OperationStatusInterrupted)
	queued := waitForRegistryOperationStatus(t, operations, secondID, OperationStatusInterrupted)
	if active.Error == nil || active.Error.Code != "interrupted" {
		t.Fatalf("active operation after shutdown = %+v; want interrupted error", active)
	}
	if queued.Error == nil || queued.Error.Code != "interrupted" {
		t.Fatalf("queued operation after shutdown = %+v; want interrupted error", queued)
	}

	releaseOnce.Do(func() { close(release) })
	time.Sleep(25 * time.Millisecond)
	active = waitForRegistryOperationStatus(t, operations, firstID, OperationStatusInterrupted)
	if active.Status != OperationStatusInterrupted {
		t.Fatalf("active operation status after worker return = %s; want interrupted", active.Status)
	}
}

func TestOperationSSEMultiClientConflictAndReconnectSnapshotCoherence(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	mutations := &fakeMutationTarget{startHook: func() {
		close(started)
		<-release
	}}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  mutations,
		MutationLimits: MutationLimits{
			FeatureQueue:   0,
			RuntimeQueue:   1,
			AggregateQueue: 10,
		},
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := srv.Client()

	respA, readerA := openOperationSSE(t, client, srv.URL)
	defer respA.Body.Close()
	respB, readerB := openOperationSSE(t, client, srv.URL)
	defer respB.Body.Close()
	readSSEBlock(t, readerA, "connected")
	readSSEBlock(t, readerB, "connected")

	first := postMutationHTTP(t, client, srv.URL+"/api/v1/features/feat-sse/start", `{}`)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first start status = %d; want 202", first.StatusCode)
	}
	firstID := decodeHTTPJSONMap(t, first)["operation_id"].(string)
	<-started

	conflict := postMutationHTTP(t, client, srv.URL+"/api/v1/features/feat-sse/start", `{}`)
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("conflict start status = %d; want 409", conflict.StatusCode)
	}
	conflictID := decodeHTTPJSONMap(t, conflict)["operation_id"].(string)
	readSSEBlockContaining(t, readerA, "operation.updated", conflictID)
	readSSEBlockContaining(t, readerB, "operation.updated", conflictID)

	_ = respB.Body.Close()
	close(release)
	waitForOperationStatus(t, handler, firstID, OperationStatusSucceeded)

	runtime := patchMutationHTTP(t, client, srv.URL+"/api/v1/config/runtime", `{"defaults":{"models":{"research":"opus"}}}`)
	if runtime.StatusCode != http.StatusAccepted {
		t.Fatalf("runtime config status = %d; want 202", runtime.StatusCode)
	}
	runtimeID := decodeHTTPJSONMap(t, runtime)["operation_id"].(string)
	waitForOperationStatus(t, handler, runtimeID, OperationStatusSucceeded)

	reconnected, reconnectedReader := openOperationSSE(t, client, srv.URL)
	defer reconnected.Body.Close()
	readSSEBlock(t, reconnectedReader, "connected")
	snapshot := getJSONMap(t, handler, "/api/v1/operations?limit=20")
	ops := snapshot["operations"].([]any)
	if !operationSnapshotContains(ops, conflictID, OperationStatusRejected) || !operationSnapshotContains(ops, runtimeID, OperationStatusSucceeded) {
		t.Fatalf("operation snapshot after reconnect = %+v; want conflict %s and runtime %s", ops, conflictID, runtimeID)
	}
}

func TestPromptPermissionHelpAndRuntimeMutationRoutesCreateSafeOperations(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	mutations := &fakeMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  mutations,
	})

	permission := mutationJSONMap(t, handler, http.MethodPost, "/api/v1/permissions/answer", `{"request_id":"perm-1","session_id":"sess-1","decision":"allow"}`, "", "")
	permissionOp := waitForOperationStatus(t, handler, permission["operation_id"].(string), OperationStatusSucceeded)
	if got := permissionOp["result"].(map[string]any)["decision"]; got != "allow" {
		t.Fatalf("permission operation result decision = %v; want allow", got)
	}

	ask := mutationJSONMap(t, handler, http.MethodPost, "/api/v1/prompts/ask-user/answer", `{"request_id":"ask-1","session_id":"sess-1","answers":{"Question?":"secret-answer"}}`, "", "")
	askOp := waitForOperationStatus(t, handler, ask["operation_id"].(string), OperationStatusSucceeded)
	if result := askOp["result"].(map[string]any); result["request_id"] != "ask-1" || result["session_id"] != "sess-1" {
		t.Fatalf("ask-user operation result = %+v; want request/session ids", result)
	}

	help := mutationJSONMap(t, handler, http.MethodPost, "/api/v1/prompts/help/send", `{"feature_id":"feat-help","session_id":"sess-help","message":"raw initial prompt secret-answer"}`, "", "")
	helpOp := waitForOperationStatus(t, handler, help["operation_id"].(string), OperationStatusSucceeded)
	if result := helpOp["result"].(map[string]any); result["session_id"] != "sess-help" || result["status"] != "sent" {
		t.Fatalf("help operation result = %+v; want sent session metadata", result)
	}

	runtime := mutationJSONMap(t, handler, http.MethodPatch, "/api/v1/config/runtime", `{"defaults":{"models":{"research":"opus"}}}`, "", "")
	runtimeOp := waitForOperationStatus(t, handler, runtime["operation_id"].(string), OperationStatusSucceeded)
	if result := runtimeOp["result"].(map[string]any); result["kind"] != "runtime.config" || result["status"] != "updated" {
		t.Fatalf("runtime config operation result = %+v; want safe update metadata", result)
	}

	if len(mutations.permissionCalls) != 1 || len(mutations.askCalls) != 1 || len(mutations.helpCalls) != 1 || len(mutations.runtimeConfigCalls) != 1 {
		t.Fatalf("mutation calls = permission:%d ask:%d help:%d runtime:%d; want one each",
			len(mutations.permissionCalls), len(mutations.askCalls), len(mutations.helpCalls), len(mutations.runtimeConfigCalls))
	}
	body := getJSONMap(t, handler, "/api/v1/operations?limit=20")
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal operations body: %v", err)
	}
	if strings.Contains(string(encoded), "secret-answer") || strings.Contains(string(encoded), "raw initial prompt") {
		t.Fatalf("operation snapshot leaked raw prompt or answer: %s", encoded)
	}
}

func TestFeatureLifecycleMutationRoutesCreateSafeOperations(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	mutations := &fakeMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Runtime:    RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: filepath.Join(filepath.Dir(stateDir), "config.yaml")},
		Operations: mustOperationRegistry(t, filepath.Join(filepath.Dir(stateDir), "operations")),
		Mutations:  mutations,
	})

	requests := []struct {
		name string
		path string
		body string
	}{
		{name: "start", path: "/api/v1/features/feat-life/start", body: `{}`},
		{name: "stop", path: "/api/v1/features/feat-life/stop", body: `{}`},
		{name: "restart", path: "/api/v1/features/feat-life/restart", body: `{"max_iterations_delta":1}`},
		{name: "review", path: "/api/v1/features/feat-life/review-decision", body: `{"decision":"proceed","phase":"plan"}`},
		{name: "config", path: "/api/v1/features/feat-life/config", body: `{"inquireness":"low"}`},
		{name: "need_user_input", path: "/api/v1/features/feat-life/need-user-input", body: `{"decision":"abort"}`},
		{name: "need_user_input_draft", path: "/api/v1/features/feat-life/need-user-input-draft", body: `{"answers":{"Question?":"safe answer"}}`},
	}
	for _, tt := range requests {
		t.Run(tt.name, func(t *testing.T) {
			resp := mutationJSONMap(t, handler, http.MethodPost, tt.path, tt.body, "", "")
			waitForOperationStatus(t, handler, resp["operation_id"].(string), OperationStatusSucceeded)
		})
	}

	if len(mutations.startCalls) != 1 || len(mutations.stopCalls) != 1 || len(mutations.restartCalls) != 1 ||
		len(mutations.reviewCalls) != 1 || len(mutations.featureConfigCalls) != 1 ||
		len(mutations.needDecisionCalls) != 1 || len(mutations.needDraftCalls) != 1 {
		t.Fatalf("lifecycle calls = start:%d stop:%d restart:%d review:%d config:%d need:%d draft:%d; want one each",
			len(mutations.startCalls), len(mutations.stopCalls), len(mutations.restartCalls), len(mutations.reviewCalls),
			len(mutations.featureConfigCalls), len(mutations.needDecisionCalls), len(mutations.needDraftCalls))
	}
}

func mustOperationRegistry(t *testing.T, dir string) *OperationRegistry {
	t.Helper()
	registry, err := NewOperationRegistry(OperationRegistryOptions{Dir: dir, DefaultLimit: 50, MaxLimit: 200})
	if err != nil {
		t.Fatalf("NewOperationRegistry() error = %v", err)
	}
	return registry
}

func mutationJSONMap(t *testing.T, handler http.Handler, method, path, body, origin, trusted string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", firstNonEmpty(trusted, "local"))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s status = %d; want 202; body: %s", method, path, resp.StatusCode, data)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode mutation response: %v", err)
	}
	return out
}

func assertOperationCount(t *testing.T, handler http.Handler, want int) {
	t.Helper()
	body := getJSONMap(t, handler, "/api/v1/operations?limit=200")
	ops := body["operations"].([]any)
	if len(ops) != want {
		t.Fatalf("operation count = %d; want %d; body=%+v", len(ops), want, body)
	}
}

func assertAccessControlHeaders(t *testing.T, header http.Header, want map[string]string) {
	t.Helper()
	for name, value := range want {
		if got := header.Get(name); got != value {
			t.Fatalf("%s = %q; want %q", name, got, value)
		}
	}
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			if _, ok := want[name]; !ok {
				t.Fatalf("unexpected CORS header %s = %q", name, header.Get(name))
			}
		}
	}
}

func assertNoAccessControlHeaders(t *testing.T, header http.Header) {
	t.Helper()
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			t.Fatalf("unexpected CORS header %s = %q", name, header.Get(name))
		}
	}
}

func waitForOperationStatus(t *testing.T, handler http.Handler, opID string, want OperationStatus) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body := getJSONMap(t, handler, "/api/v1/operations?limit=200")
		for _, raw := range body["operations"].([]any) {
			op := raw.(map[string]any)
			if op["id"] == opID && op["status"] == string(want) {
				return op
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("operation %s did not reach status %s", opID, want)
	return nil
}

func getOperationByID(t *testing.T, handler http.Handler, opID string) map[string]any {
	t.Helper()
	body := getJSONMap(t, handler, "/api/v1/operations?limit=200")
	for _, raw := range body["operations"].([]any) {
		op := raw.(map[string]any)
		if op["id"] == opID {
			return op
		}
	}
	t.Fatalf("operation %s not found in %+v", opID, body)
	return nil
}

func waitForRegistryOperationStatus(t *testing.T, registry *OperationRegistry, opID string, want OperationStatus) OperationDTO {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		page, err := registry.List(OperationListOptions{Limit: 200})
		if err != nil {
			t.Fatalf("List operations: %v", err)
		}
		for _, op := range page.Operations {
			if op.ID == opID && op.Status == want {
				return op
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("operation %s did not reach registry status %s", opID, want)
	return OperationDTO{}
}

func postMutationHTTP(t *testing.T, client *http.Client, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest(%s): %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func patchMutationHTTP(t *testing.T, client *http.Client, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest(%s): %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", url, err)
	}
	return resp
}

func decodeHTTPJSONMap(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode HTTP response: %v", err)
	}
	return out
}

func bufioReader(r io.Reader) *bufio.Reader {
	return bufio.NewReader(r)
}

func openOperationSSE(t *testing.T, client *http.Client, baseURL string) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/events?heartbeat_ms=10", nil)
	if err != nil {
		t.Fatalf("NewRequest SSE: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET SSE: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d; want 200", resp.StatusCode)
	}
	return resp, bufioReader(resp.Body)
}

func readSSEBlockContaining(t *testing.T, r *bufio.Reader, event, needle string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		block := readSSEBlock(t, r, event)
		if strings.Contains(block, needle) {
			return block
		}
	}
	t.Fatalf("timed out waiting for SSE event %q containing %q", event, needle)
	return ""
}

func operationSnapshotContains(ops []any, id string, status OperationStatus) bool {
	for _, raw := range ops {
		op := raw.(map[string]any)
		if op["id"] == id && op["status"] == string(status) {
			return true
		}
	}
	return false
}

type fakeMutationTarget struct {
	mu                 sync.Mutex
	createCalls        []CreateFeatureRequest
	startCalls         []string
	stopCalls          []string
	restartCalls       []RestartFeatureRequest
	reviewCalls        []ReviewDecisionRequest
	featureConfigCalls []FeatureConfigMutationRequest
	needDecisionCalls  []NeedUserInputDecisionRequest
	needDraftCalls     []NeedUserInputDraftRequest
	permissionCalls    []PermissionAnswerRequest
	askCalls           []AskUserAnswerRequest
	helpCalls          []HelpAnswerRequest
	runtimeConfigCalls []RuntimeConfigMutationRequest
	startHook          func()
	createHook         func()
	runtimeConfigHook  func()
	featureConfigHook  func()
}

func (f *fakeMutationTarget) CreateFeature(req CreateFeatureRequest) (OperationResult, error) {
	f.mu.Lock()
	f.createCalls = append(f.createCalls, req)
	f.mu.Unlock()
	if f.createHook != nil {
		f.createHook()
	}
	return OperationResult{Metadata: map[string]string{"feature_id": "created-001"}}, nil
}

func (f *fakeMutationTarget) StartFeature(featureID string) (OperationResult, error) {
	f.mu.Lock()
	f.startCalls = append(f.startCalls, featureID)
	f.mu.Unlock()
	if f.startHook != nil {
		f.startHook()
	}
	return OperationResult{Metadata: map[string]string{"feature_id": featureID}}, nil
}

func (f *fakeMutationTarget) StopFeature(featureID string) (OperationResult, error) {
	f.mu.Lock()
	f.stopCalls = append(f.stopCalls, featureID)
	f.mu.Unlock()
	return OperationResult{Metadata: map[string]string{"feature_id": featureID}}, nil
}

func (f *fakeMutationTarget) RestartFeature(featureID string, req RestartFeatureRequest) (OperationResult, error) {
	f.mu.Lock()
	f.restartCalls = append(f.restartCalls, req)
	f.mu.Unlock()
	return OperationResult{Metadata: map[string]string{"feature_id": featureID}}, nil
}

func (f *fakeMutationTarget) ReviewDecision(featureID string, req ReviewDecisionRequest) (OperationResult, error) {
	f.mu.Lock()
	f.reviewCalls = append(f.reviewCalls, req)
	f.mu.Unlock()
	return OperationResult{Metadata: map[string]string{"feature_id": featureID}}, nil
}

func (f *fakeMutationTarget) UpdateFeatureConfig(featureID string, req FeatureConfigMutationRequest) (OperationResult, error) {
	f.mu.Lock()
	f.featureConfigCalls = append(f.featureConfigCalls, req)
	f.mu.Unlock()
	if f.featureConfigHook != nil {
		f.featureConfigHook()
	}
	return OperationResult{Metadata: map[string]string{"feature_id": featureID}}, nil
}

func (f *fakeMutationTarget) NeedUserInputDecision(featureID string, req NeedUserInputDecisionRequest) (OperationResult, error) {
	f.mu.Lock()
	f.needDecisionCalls = append(f.needDecisionCalls, req)
	f.mu.Unlock()
	return OperationResult{Metadata: map[string]string{"feature_id": featureID}}, nil
}

func (f *fakeMutationTarget) DraftNeedUserInputAnswers(featureID string, req NeedUserInputDraftRequest) (OperationResult, error) {
	f.mu.Lock()
	f.needDraftCalls = append(f.needDraftCalls, req)
	f.mu.Unlock()
	return OperationResult{Metadata: map[string]string{"feature_id": featureID}}, nil
}

func (f *fakeMutationTarget) AnswerPermission(req PermissionAnswerRequest) (OperationResult, error) {
	f.mu.Lock()
	f.permissionCalls = append(f.permissionCalls, req)
	f.mu.Unlock()
	return OperationResult{Metadata: map[string]string{"session_id": req.SessionID, "request_id": req.RequestID, "decision": req.Decision}}, nil
}

func (f *fakeMutationTarget) AnswerAskUser(req AskUserAnswerRequest) (OperationResult, error) {
	f.mu.Lock()
	f.askCalls = append(f.askCalls, req)
	f.mu.Unlock()
	return OperationResult{Metadata: map[string]string{"session_id": req.SessionID, "request_id": req.RequestID, "decision": "answered"}}, nil
}

func (f *fakeMutationTarget) SendHelp(req HelpAnswerRequest) (OperationResult, error) {
	f.mu.Lock()
	f.helpCalls = append(f.helpCalls, req)
	f.mu.Unlock()
	return OperationResult{Metadata: map[string]string{"feature_id": req.FeatureID, "session_id": req.SessionID, "status": "sent"}}, nil
}

func (f *fakeMutationTarget) RuntimeConfig(req RuntimeConfigMutationRequest) (OperationResult, error) {
	f.mu.Lock()
	f.runtimeConfigCalls = append(f.runtimeConfigCalls, req)
	f.mu.Unlock()
	if f.runtimeConfigHook != nil {
		f.runtimeConfigHook()
	}
	return OperationResult{Metadata: map[string]string{"kind": "runtime.config", "status": "updated"}}, nil
}

var _ = bytes.Buffer{}
var _ = config.ModelConfig{}
var _ = feature.Checkpoints{}
