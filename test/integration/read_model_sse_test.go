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
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
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
	block := readIntegrationSSEBlock(t, reader, "session.output.activity")
	if strings.Contains(block, `"snapshot_required":true`) {
		t.Fatalf("session output activity = %s; want non-snapshot output signal", block)
	}
	if !strings.Contains(block, `"feature_id":"`+f.ID+`"`) {
		t.Fatalf("session output activity = %s; want feature id %s", block, f.ID)
	}
}

// TestAPIChildFeatureProjectionAndSSE stands up a parent with an active
// refactor child (queued setup) and pins the relationship projections:
// children are excluded from the top-level list, parents carry active_child,
// child detail exposes the parent linkage, and SSE frames correlate child
// events to the parent via the relationship parent/child identifiers.
func TestAPIChildFeatureProjectionAndSSE(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	now := time.Now().UTC().Truncate(time.Second)
	parent := &feature.Feature{
		ID: "parent-1", Name: "Parent", Slug: "parent", Status: feature.StatusPublished,
		CurrentPhase: feature.PhasePublish, Created: now,
		Repos:     []feature.FeatureRepo{{Name: "agentic-orchestrator"}},
		ActiveRun: 1, RunCount: 1, SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}
	child := &feature.Feature{
		ID: "child-1", Name: "Rework auth", Slug: "rework-auth", Status: feature.StatusSettingUpWorktrees,
		CurrentPhase: feature.PhaseImplement, Created: now,
		Repos:     []feature.FeatureRepo{{Name: "agentic-orchestrator", Branch: "feature/rework-auth"}},
		ActiveRun: 1, RunCount: 1, SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID,
			Kind:     feature.ChildKindRefactor,
			Bases:    []feature.ChildRepoBase{{Repo: "agentic-orchestrator", SHA: "deadbeefcafe", ParentBranch: "main"}},
		},
	}
	child.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Status: feature.SetupStatusQueued}})
	if err := store.Save(child); err != nil {
		t.Fatalf("Save(child) error = %v", err)
	}

	eventCh := make(chan interface{}, 8)
	srv := httptest.NewServer(serverruntime.NewHandler(serverruntime.HandlerOptions{
		Runtime:  serverruntime.RuntimeIdentity{RuntimeDir: t.TempDir(), StateDir: store.BaseDir},
		Features: store, FeatureStore: store, Events: eventCh,
	}))
	t.Cleanup(srv.Close)

	list := getIntegrationJSON(t, srv.URL+"/api/v1/features")
	summaries := list["features"].([]any)
	if len(summaries) != 1 || summaries[0].(map[string]any)["id"] != parent.ID {
		t.Fatalf("top-level list = %+v, want only the parent", summaries)
	}
	activeChild := summaries[0].(map[string]any)["active_child"].(map[string]any)
	if activeChild["id"] != child.ID || activeChild["relationship_state"] != "setting_up" || activeChild["setup_status"] != string(feature.SetupStatusQueued) {
		t.Fatalf("parent summary active_child = %+v, want setting_up child", activeChild)
	}

	parentDetail := getIntegrationJSON(t, srv.URL+"/api/v1/features/"+parent.ID)
	if parentDetail["feature"].(map[string]any)["active_child"].(map[string]any)["id"] != child.ID {
		t.Fatalf("parent detail active_child missing in %+v", parentDetail["feature"])
	}

	childDetail := getIntegrationJSON(t, srv.URL+"/api/v1/features/"+child.ID)
	childBody := childDetail["feature"].(map[string]any)
	if childBody["parent_id"] != parent.ID || childBody["parent_kind"] != feature.ChildKindRefactor || childBody["active"] != true {
		t.Fatalf("child detail linkage = %+v, want parent %s", childBody, parent.ID)
	}
	setupBlock := childBody["active_run_detail"].(map[string]any)["setup"].(map[string]any)
	if setupBlock["status"] != string(feature.SetupStatusQueued) {
		t.Fatalf("child detail setup = %+v, want queued setup", setupBlock)
	}
	if _, ok := childBody["setup_complete"]; ok {
		t.Fatalf("child detail setup_complete present while queued: %+v", childBody)
	}

	resp, reader := openIntegrationSSE(t, srv)
	defer resp.Body.Close()
	readIntegrationSSEBlock(t, reader, "connected")
	eventCh <- ports.Event{Type: ports.RelationshipIntegrationChanged, FeatureID: child.ID, ParentID: parent.ID, ChildID: child.ID}
	block := readIntegrationSSEBlock(t, reader, "lifecycle.updated")
	if !strings.Contains(block, `"parent_id":"`+parent.ID+`"`) || !strings.Contains(block, `"child_id":"`+child.ID+`"`) {
		t.Fatalf("child SSE frame = %s; want parent_id and child_id", block)
	}
}

// TestAPIChildFeatureNeverLeaksIntoTopLevelList covers every child setup
// state: queued, failed, and setup-complete children stay out of the
// top-level list while the parent reflects the derived relationship state.
func TestAPIChildFeatureNeverLeaksIntoTopLevelList(t *testing.T) {
	cases := []struct {
		name       string
		status     feature.Status
		setup      feature.SetupStatus
		setupErr   string
		wantState  string
		wantErrMsg bool
	}{
		{"queued setup", feature.StatusSettingUpWorktrees, feature.SetupStatusQueued, "", "setting_up", false},
		{"failed setup", feature.StatusFailed, feature.SetupStatusFailed, "git worktree add failed", "setting_up", true},
		{"setup complete", feature.StatusCreated, feature.SetupStatusDone, "", "active", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := feature.NewStore(t.TempDir())
			parent := &feature.Feature{
				ID: "parent-1", Name: "Parent", Slug: "parent", Status: feature.StatusPublished,
				CurrentPhase: feature.PhasePublish, Created: time.Now().UTC().Truncate(time.Second),
				Repos:     []feature.FeatureRepo{{Name: "agentic-orchestrator"}},
				ActiveRun: 1, RunCount: 1, SchemaVersion: feature.SchemaVersionCurrent,
			}
			if err := store.Save(parent); err != nil {
				t.Fatalf("Save(parent) error = %v", err)
			}
			child := &feature.Feature{
				ID: "child-1", Name: "Rework auth", Slug: "rework-auth", Status: tc.status,
				CurrentPhase: feature.PhaseImplement, Created: time.Now().UTC().Truncate(time.Second),
				Repos:     []feature.FeatureRepo{{Name: "agentic-orchestrator", Branch: "feature/rework-auth"}},
				ActiveRun: 1, RunCount: 1, SchemaVersion: feature.SchemaVersionCurrent,
				Parent: &feature.ChildRelationship{ParentID: parent.ID, Kind: feature.ChildKindRefactor},
			}
			child.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Status: tc.setup}})
			if tc.setup == feature.SetupStatusFailed {
				// Mirror the setup runner's durable shape: the owning task
				// carries the full record and the run carries the thin record.
				key := "worktree:agentic-orchestrator"
				child.Run().Setup.Tasks = map[string]feature.SetupTask{key: {
					Key: key, Kind: feature.SetupTaskWorktree, Label: "Worktree: agentic-orchestrator",
					Repo: "agentic-orchestrator", Status: feature.SetupStatusFailed,
					Error: &errcat.FailureRecord{
						Code: errcat.WorktreeSetupFailed,
						Context: &errcat.RecordContext{
							Repositories: []errcat.CodeRepository{{Name: "agentic-orchestrator", Branch: "feature/rework-auth"}},
						},
						Diagnostics: tc.setupErr,
					},
				}}
				child.Run().Setup.TaskOrder = []string{key}
				child.Run().Failure = &errcat.FailureRecord{
					Code: errcat.WorktreeSetupFailed,
					Context: &errcat.RecordContext{SetupTask: &errcat.CodeSetupTask{
						Key: key, Kind: "worktree", Label: "Worktree: agentic-orchestrator",
					}},
				}
			}
			if err := store.Save(child); err != nil {
				t.Fatalf("Save(child) error = %v", err)
			}
			srv := httptest.NewServer(serverruntime.NewHandler(serverruntime.HandlerOptions{
				Runtime:  serverruntime.RuntimeIdentity{RuntimeDir: t.TempDir(), StateDir: store.BaseDir},
				Features: store, FeatureStore: store,
			}))
			t.Cleanup(srv.Close)

			list := getIntegrationJSON(t, srv.URL+"/api/v1/features")
			summaries := list["features"].([]any)
			if len(summaries) != 1 || summaries[0].(map[string]any)["id"] != parent.ID {
				t.Fatalf("top-level list = %+v, want only the parent", summaries)
			}
			activeChild, ok := summaries[0].(map[string]any)["active_child"].(map[string]any)
			if !ok {
				t.Fatalf("parent summary active_child missing in %+v", summaries[0])
			}
			if activeChild["relationship_state"] != tc.wantState {
				t.Fatalf("active_child.relationship_state = %v, want %s", activeChild["relationship_state"], tc.wantState)
			}
			if tc.wantErrMsg {
				// A failed child setup surfaces through setup_status alone; no
				// last_error key exists anywhere on the summary.
				if activeChild["setup_status"] != string(feature.SetupStatusFailed) {
					t.Fatalf("active_child.setup_status = %v, want failed", activeChild["setup_status"])
				}
				if _, ok := activeChild["last_error"]; ok {
					t.Fatalf("active_child = %+v, want no last_error for a failed child setup", activeChild)
				}
			}
		})
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
