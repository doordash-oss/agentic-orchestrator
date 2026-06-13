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

package integration

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestReadModelSSESnapshotCoherence(t *testing.T) {
	store, f := integrationReadStore(t)
	eventCh := make(chan interface{}, 8)
	srv := httptest.NewServer(serverruntime.NewHandler(serverruntime.HandlerOptions{
		Runtime:  serverruntime.RuntimeIdentity{RuntimeDir: t.TempDir(), StateDir: store.BaseDir},
		Features: store, FeatureStore: store, Events: eventCh,
	}))
	t.Cleanup(srv.Close)

	before := getIntegrationJSON(t, srv.URL+"/api/v1/features/"+f.ID)
	beforeRevision := before["meta"].(map[string]any)["revision"].(string)
	resp, reader := openIntegrationSSE(t, srv)
	defer resp.Body.Close()
	readIntegrationSSEBlock(t, reader, "connected")

	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.CurrentIteration = 2
		ff.CurrentPhaseStatus = "reviewing"
		return nil
	}); err != nil {
		t.Fatalf("Modify() error = %v", err)
	}
	eventCh <- ports.Event{Type: ports.PhaseCompleted, FeatureID: f.ID, Phase: feature.PhaseImplement}
	block := readIntegrationSSEBlock(t, reader, "lifecycle.updated")
	if strings.Contains(block, store.BaseDir) {
		t.Fatalf("SSE leaked state dir in %s", block)
	}

	after := getIntegrationJSON(t, srv.URL+"/api/v1/features/"+f.ID)
	afterRevision := after["meta"].(map[string]any)["revision"].(string)
	if afterRevision == beforeRevision {
		t.Fatalf("detail revision did not change after store mutation: %s", afterRevision)
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/features/"+f.ID, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("If-None-Match", `"`+afterRevision+`"`)
	revalidated, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("revalidate detail: %v", err)
	}
	defer revalidated.Body.Close()
	if revalidated.StatusCode != http.StatusNotModified {
		body, _ := io.ReadAll(revalidated.Body)
		t.Fatalf("revalidate status = %d; want 304; body: %s", revalidated.StatusCode, body)
	}
}

func TestReadModelSSEBackpressureConcurrentPolling(t *testing.T) {
	store, f := integrationReadStore(t)
	eventCh := make(chan interface{}, 256)
	srv := httptest.NewServer(serverruntime.NewHandler(serverruntime.HandlerOptions{
		Runtime:  serverruntime.RuntimeIdentity{RuntimeDir: t.TempDir(), StateDir: store.BaseDir},
		Features: store, FeatureStore: store, Events: eventCh,
	}))
	t.Cleanup(srv.Close)
	resp, reader := openIntegrationSSE(t, srv)
	defer resp.Body.Close()
	readIntegrationSSEBlock(t, reader, "connected")

	for i := 0; i < 64; i++ {
		eventCh <- ports.Event{Type: ports.SessionOutput, FeatureID: f.ID, Phase: feature.PhaseImplement}
	}
	body := getIntegrationJSON(t, srv.URL+"/api/v1/features")
	revision := body["meta"].(map[string]any)["revision"].(string)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/features", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("If-None-Match", `"`+revision+`"`)
	revalidated, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("concurrent revalidate: %v", err)
	}
	defer revalidated.Body.Close()
	if revalidated.StatusCode != http.StatusNotModified {
		t.Fatalf("concurrent revalidate status = %d; want 304", revalidated.StatusCode)
	}
	if block := readIntegrationSSEBlock(t, reader, "session.updated"); !strings.Contains(block, `"snapshot_required":true`) {
		t.Fatalf("session event = %s; want snapshot_required", block)
	}
}

func integrationReadStore(t *testing.T) (*feature.Store, *feature.Feature) {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	f := &feature.Feature{
		ID: "feat-sse", Name: "SSE", Slug: "sse", Status: feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement, Created: time.Now().UTC().Truncate(time.Second),
		Repos:     []feature.FeatureRepo{{Name: "agentic-orchestrator"}},
		Models:    config.ModelConfig{Implementation: "opus[1m]"},
		ActiveRun: 1, RunCount: 1, SchemaVersion: feature.SchemaVersionCurrent,
	}
	f.CurrentIteration = 1
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return store, f
}

func getIntegrationJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s status = %d; body: %s", url, resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return out
}

func openIntegrationSSE(t *testing.T, srv *httptest.Server) (*http.Response, *bufio.Reader) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + "/api/v1/events?heartbeat_ms=10")
	if err != nil {
		t.Fatalf("GET SSE: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d; want 200", resp.StatusCode)
	}
	return resp, bufio.NewReader(resp.Body)
}

func readIntegrationSSEBlock(t *testing.T, r *bufio.Reader, event string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var lines []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE: %v", err)
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			lines = append(lines, line)
		}
		block := strings.Join(lines, "\n")
		if strings.Contains(block, "event: "+event) {
			return block
		}
	}
	t.Fatalf("timed out waiting for SSE event %q", event)
	return ""
}
