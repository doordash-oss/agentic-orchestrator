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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestRewindPreviewEndpointReturnsEligiblePreview(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	// Give the repo a base branch so worktree reset consequences are real.
	f.Repos[0].BaseBranch = "main"
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	handler := NewHandler(baseReadHandlerOptions(store))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/rewind/preview?target_phase=implement", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", w.Code, w.Body.String())
	}
	body := decodeBodyMap(t, w.Result())
	if eligible := body["eligible"]; eligible != true {
		t.Fatalf("eligible = %v; want true; body: %v", eligible, body)
	}
	if got := int(body["source_run_number"].(float64)); got != f.ActiveRun {
		t.Fatalf("source_run_number = %d; want %d", got, f.ActiveRun)
	}
	if rev, _ := body["source_revision"].(string); rev == "" {
		t.Fatal("source_revision is empty")
	}
	if got := body["target_phase"]; got != feature.PhaseImplement.DirName() {
		t.Fatalf("target_phase = %v; want %s", got, feature.PhaseImplement.DirName())
	}
	validPhases, _ := body["valid_phases"].([]any)
	if len(validPhases) == 0 {
		t.Fatal("valid_phases empty")
	}
	carried, _ := body["carried_phases"].([]any)
	foundPlan := false
	for _, c := range carried {
		if c == "plan" {
			foundPlan = true
		}
	}
	if !foundPlan {
		t.Fatalf("carried_phases = %v; want to include plan", carried)
	}
	prCons, _ := body["pr_consequences"].([]any)
	if len(prCons) == 0 {
		t.Fatal("pr_consequences empty for publishable feature with a PR")
	}
	wtCons, _ := body["worktree_consequences"].([]any)
	if len(wtCons) == 0 {
		t.Fatal("worktree_consequences empty with a base branch set")
	}
	backupRepos, _ := body["backup_branch_repos"].([]any)
	foundRepo := false
	for _, r := range backupRepos {
		if r == repoNameSelf {
			foundRepo = true
		}
	}
	if !foundRepo {
		t.Fatalf("backup_branch_repos = %v; want %s", backupRepos, repoNameSelf)
	}
}

func TestRewindPreviewEndpointRejectsInvalidTarget(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	handler := NewHandler(baseReadHandlerOptions(store))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/rewind/preview?target_phase=knowledge-base", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (ineligible preview is still 200)", w.Code)
	}
	body := decodeBodyMap(t, w.Result())
	if eligible := body["eligible"]; eligible == true {
		t.Fatal("eligible = true for KB target; want false")
	}
	findings, _ := body["validation_findings"].([]any)
	if len(findings) == 0 {
		t.Fatal("validation_findings empty for invalid target")
	}
}

func TestRewindPreviewEndpointRequiresTargetPhase(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	handler := NewHandler(baseReadHandlerOptions(store))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/rewind/preview", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
}

func TestRewindPreviewEndpointIsReadOnlyRejectsPost(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	handler := NewHandler(baseReadHandlerOptions(store))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/features/"+f.ID+"/rewind/preview", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405 (preview is read-only)", w.Code)
	}
}

func TestRewindPreviewEndpointCarriesSourceRevisionForGuard(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	handler := NewHandler(baseReadHandlerOptions(store))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/rewind/preview?target_phase=implement", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	body := decodeBodyMap(t, w.Result())
	rev, _ := body["source_revision"].(string)
	// The preview's source_revision must equal feature.RewindRevision of the
	// current feature, so execution's stale guard can detect drift.
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := feature.RewindRevision(loaded); rev != want {
		t.Fatalf("preview source_revision = %s; want RewindRevision %s", rev, want)
	}
}
