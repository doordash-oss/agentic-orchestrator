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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// seedHistoryFeature builds a store with one feature whose active run is
// run-001, then creates the supplied sealed runs via CreateRun. Returns the
// store and feature for handler construction.
func seedHistoryFeature(t *testing.T, sealedRuns ...*feature.Run) (*feature.Store, *feature.Feature) {
	t.Helper()
	store, f := seedReadFeature(t)
	for _, run := range sealedRuns {
		if err := store.CreateRun(f.ID, run); err != nil {
			t.Fatalf("CreateRun(%d) error = %v", run.RunNumber, err)
		}
	}
	return store, f
}

func sealedRun(t *testing.T, n int, opts ...func(*feature.Run)) *feature.Run {
	t.Helper()
	sealReason := feature.SealReasonRewind
	sealed := time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)
	started := sealed.Add(-2 * time.Hour)
	r := &feature.Run{
		RunNumber:  n,
		StartedAt:  &started,
		SealedAt:   &sealed,
		SealReason: sealReason,
		IsRewind:   true,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func runListPage(t *testing.T, handler http.Handler, featureID string, page, pageSize int) map[string]any {
	t.Helper()
	path := "/api/v1/features/" + featureID + "/runs?page=" + strconv.Itoa(page) + "&page_size=" + strconv.Itoa(pageSize)
	return getJSONMap(t, handler, path)
}

func runNumbersFromList(body map[string]any) []int {
	rawRuns := body["runs"].([]any)
	out := make([]int, 0, len(rawRuns))
	for _, raw := range rawRuns {
		out = append(out, int(raw.(map[string]any)["run_number"].(float64)))
	}
	return out
}

func TestRunListPaginatesNewestFirstAcrossMoreThanFiveRuns(t *testing.T) {
	t.Parallel()
	var sealed []*feature.Run
	for n := 2; n <= 7; n++ {
		sealed = append(sealed, sealedRun(t, n))
	}
	store, f := seedHistoryFeature(t, sealed...)
	handler := NewHandler(baseReadHandlerOptions(store))

	page1 := runListPage(t, handler, f.ID, 1, 3)
	if got, want := intsToCSV(runNumbersFromList(page1)), "7,6,5"; got != want {
		t.Fatalf("page 1 runs = %s; want %s", got, want)
	}
	if total := int(page1["total"].(float64)); total != 7 {
		t.Fatalf("total = %d; want 7", total)
	}
	if tp := int(page1["total_pages"].(float64)); tp != 3 {
		t.Fatalf("total_pages = %d; want 3", tp)
	}

	page2 := runListPage(t, handler, f.ID, 2, 3)
	if got, want := intsToCSV(runNumbersFromList(page2)), "4,3,2"; got != want {
		t.Fatalf("page 2 runs = %s; want %s", got, want)
	}
	page3 := runListPage(t, handler, f.ID, 3, 3)
	if got, want := intsToCSV(runNumbersFromList(page3)), "1"; got != want {
		t.Fatalf("page 3 runs = %s; want %s", got, want)
	}
}

func TestRunListHandlesRunNumbersAbove999AndEnumerationGaps(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t, sealedRun(t, 1000), sealedRun(t, 1001))
	handler := NewHandler(baseReadHandlerOptions(store))

	body := runListPage(t, handler, f.ID, 1, 100)
	if got, want := intsToCSV(runNumbersFromList(body)), "1001,1000,1"; got != want {
		t.Fatalf("runs = %s; want %s (newest first, run numbers above 999)", got, want)
	}

	// A run directory without a parseable name is skipped by enumeration.
	_ = os.MkdirAll(filepath.Join(store.BaseDir, f.ID, "runs", "not-a-run"), 0o755)
	body2 := runListPage(t, handler, f.ID, 1, 100)
	if got, want := intsToCSV(runNumbersFromList(body2)), "1001,1000,1"; got != want {
		t.Fatalf("runs after bogus dir = %s; want %s", got, want)
	}
}

func TestRunListSkipsRunDirectoryMissingRunYAML(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t, sealedRun(t, 2), sealedRun(t, 3))
	// Create a run-4 directory with no run.yaml (recovery gap).
	_ = os.MkdirAll(filepath.Join(store.BaseDir, f.ID, "runs", "run-004"), 0o755)
	handler := NewHandler(baseReadHandlerOptions(store))

	body := runListPage(t, handler, f.ID, 1, 100)
	// run-004 is enumerated (total includes it) but not loaded (no run.yaml).
	if total := int(body["total"].(float64)); total != 4 {
		t.Fatalf("total = %d; want 4 (gap dir enumerated)", total)
	}
	got := runNumbersFromList(body)
	// The unloadable run-004 must not appear as a fabricated entry.
	for _, n := range got {
		if n == 4 {
			t.Fatalf("run-004 with no run.yaml should not be rendered; got runs %v", got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("rendered runs = %v; want 3 loadable runs", got)
	}
}

func TestRunListRejectsInvalidPaginationBounds(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t, sealedRun(t, 2))
	handler := NewHandler(baseReadHandlerOptions(store))

	cases := []string{
		"/api/v1/features/" + f.ID + "/runs?page=0&page_size=5",
		"/api/v1/features/" + f.ID + "/runs?page=1&page_size=0",
		"/api/v1/features/" + f.ID + "/runs?page=1&page_size=999",
		"/api/v1/features/" + f.ID + "/runs?page=abc&page_size=5",
	}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d; want 400", path, w.Code)
		}
	}
}

func TestRunDetailIsRunAuthenticWithProvenance(t *testing.T) {
	t.Parallel()
	backupBranch := "feature/secret-backup"
	carriedPhases := []string{"inquire", "research"}
	sealed := sealedRun(t, 2, func(r *feature.Run) {
		r.RewindTarget = phasePtr(feature.PhaseImplement)
		roadmapPhase := 2
		r.RewindRoadmapPhase = &roadmapPhase
		r.CarriedFromRun = 1
		r.CarriedPhases = carriedPhases
		r.BackupBranches = map[string]string{repoNameSelf: backupBranch}
		r.PendingReviewPhase = phasePtr(feature.PhasePlan)
		r.PhaseTimings = map[string]time.Duration{"implement": 120 * time.Second}
		r.PhaseCosts = map[string]float64{"implement": 1.5}
	})
	store, f := seedHistoryFeature(t, sealed)
	handler := NewHandler(baseReadHandlerOptions(store))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/runs/2", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	body := decodeBodyMap(t, w.Result())
	run := body["run"].(map[string]any)

	// current_phase comes from the run's pending review phase, NOT the
	// feature's active current_phase (PhaseImplement).
	if got := run["current_phase"]; got != feature.PhasePlan.DirName() {
		t.Fatalf("current_phase = %v; want %s (run-authentic, not feature %s)", got, feature.PhasePlan.DirName(), f.CurrentPhase.DirName())
	}
	if got := run["rewind_target"]; got != feature.PhaseImplement.DirName() {
		t.Fatalf("rewind_target = %v; want %s", got, feature.PhaseImplement.DirName())
	}
	if got := int(run["rewind_roadmap_phase"].(float64)); got != 2 {
		t.Fatalf("rewind_roadmap_phase = %d; want 2", got)
	}
	if got := int(run["carried_from_run"].(float64)); got != 1 {
		t.Fatalf("carried_from_run = %d; want 1", got)
	}
	if got := run["carried_phases"].([]any); len(got) != 2 {
		t.Fatalf("carried_phases len = %d; want 2", len(got))
	}
	// backup_branch_repos exposes repo names only, never the branch value.
	repos := run["backup_branch_repos"].([]any)
	if len(repos) != 1 || repos[0] != repoNameSelf {
		t.Fatalf("backup_branch_repos = %v; want [%s]", repos, repoNameSelf)
	}
	bodyBytes := w.Body.String()
	if strings.Contains(bodyBytes, backupBranch) {
		t.Fatalf("backup branch value %q leaked into run detail response", backupBranch)
	}
	timing := run["timing"].(map[string]any)
	if got := int64(timing["total_seconds"].(float64)); got != 120 {
		t.Fatalf("timing total_seconds = %d; want 120", got)
	}
	cost := run["cost"].(map[string]any)
	if got := cost["total_usd"].(float64); got != 1.5 {
		t.Fatalf("cost total_usd = %v; want 1.5", got)
	}
}

func TestRunDetailMissingRunIsNotFound(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t)
	handler := NewHandler(baseReadHandlerOptions(store))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/runs/999", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", w.Code)
	}
}

func TestRunSessionsFilteredByRunNumber(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t, sealedRun(t, 2), sealedRun(t, 3))
	now := time.Date(2026, 6, 13, 13, 0, 0, 0, time.UTC)
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{id: "sess-run2-a", featureID: f.ID, phase: feature.PhaseImplement, kind: ports.KindPhase, status: ports.SessionDone, startedAt: now.Add(-2 * time.Minute), runNumber: 2},
		&fakeSessionView{id: "sess-run3-a", featureID: f.ID, phase: feature.PhasePlan, kind: ports.KindPhase, status: ports.SessionDone, startedAt: now.Add(-1 * time.Minute), runNumber: 3},
		&fakeSessionView{id: "sess-run2-b", featureID: f.ID, phase: feature.PhaseResearch, kind: ports.KindPhase, status: ports.SessionDone, startedAt: now.Add(-3 * time.Minute), runNumber: 2},
		&fakeSessionView{id: "sess-other", featureID: "other-feature", phase: feature.PhaseImplement, kind: ports.KindPhase, status: ports.SessionDone, startedAt: now, runNumber: 2},
	}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/runs/2/sessions", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	body := decodeBodyMap(t, w.Result())
	if got := int(body["run_number"].(float64)); got != 2 {
		t.Fatalf("run_number = %d; want 2", got)
	}
	rawSessions := body["sessions"].([]any)
	gotIDs := make([]string, 0, len(rawSessions))
	for _, raw := range rawSessions {
		gotIDs = append(gotIDs, raw.(map[string]any)["id"].(string))
	}
	// Only run-2 sessions for this feature; run-3 and other-feature excluded.
	want := map[string]bool{"sess-run2-a": true, "sess-run2-b": true}
	if len(gotIDs) != len(want) {
		t.Fatalf("run sessions = %v; want %v", gotIDs, want)
	}
	for _, id := range gotIDs {
		if !want[id] {
			t.Fatalf("unexpected run session %q; got %v", id, gotIDs)
		}
	}
}

func TestRunHistoryRoutesRejectMutationMethods(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t, sealedRun(t, 2))
	handler := NewHandler(baseReadHandlerOptions(store))

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/features/" + f.ID + "/runs"},
		{http.MethodPost, "/api/v1/features/" + f.ID + "/runs/2"},
		{http.MethodPost, "/api/v1/features/" + f.ID + "/runs/2/sessions"},
		{http.MethodDelete, "/api/v1/features/" + f.ID + "/runs/2"},
		{http.MethodPut, "/api/v1/features/" + f.ID + "/runs"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d; want 405 (run history is read-only)", tc.method, tc.path, w.Code)
		}
	}
}

func TestRunDetailInvalidRunNumberIsBadRequest(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t)
	handler := NewHandler(baseReadHandlerOptions(store))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/runs/abc", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
}

// phasePtr is a test helper returning a pointer to a Phase.
func phasePtr(p feature.Phase) *feature.Phase { return &p }

func decodeBodyMap(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode response body: %v; body: %s", err, data)
	}
	return out
}
