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
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
	"gopkg.in/yaml.v3"
)

// seedHistoryFeature builds a store with one feature whose active run is
// run-001, then writes the supplied sealed run fixtures. Returns the
// store and feature for handler construction.
func seedHistoryFeature(t *testing.T, sealedRuns ...*feature.Run) (*feature.Store, *feature.Feature) {
	t.Helper()
	store, f := seedReadFeature(t)
	for _, run := range sealedRuns {
		dir := filepath.Join(store.BaseDir, f.ID, "runs", feature.RunDirName(run.RunNumber))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create run fixture directory: %v", err)
		}
		data, err := yaml.Marshal(run)
		if err != nil {
			t.Fatalf("marshal run fixture %d: %v", run.RunNumber, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "run.yaml"), data, 0o644); err != nil {
			t.Fatalf("write run fixture %d: %v", run.RunNumber, err)
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
	if got := run["current_phase"]; got != feature.PhasePlan.String() {
		t.Fatalf("current_phase = %v; want %s (run-authentic, not feature %s)", got, feature.PhasePlan, f.CurrentPhase)
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

func TestActiveRunDetailIncludesRunningSessionCostWithoutCountingCompletedSessions(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t)
	if err := store.Modify(f.ID, func(current *feature.Feature) error {
		current.PhaseCosts = map[string]float64{"research": 1.5}
		current.ActiveTimingKey = "research"
		return nil
	}); err != nil {
		t.Fatalf("Modify() error = %v", err)
	}
	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			MockSessionView: &mocks.MockSessionView{
				IDVal:        "sess-running-cost",
				FeatureIDVal: f.ID,
				PhaseVal:     feature.PhaseResearch,
				KindVal:      ports.KindPhase,
				StatusVal:    ports.SessionRunning,
			},
			runNumber: 1,
			usage:     &llm.Usage{CostUSD: 0.42},
		},
		&fakeSessionView{
			MockSessionView: &mocks.MockSessionView{
				IDVal:        "sess-completed-cost",
				FeatureIDVal: f.ID,
				PhaseVal:     feature.PhaseResearch,
				KindVal:      ports.KindPhase,
				StatusVal:    ports.SessionDone,
			},
			runNumber: 1,
			usage:     &llm.Usage{CostUSD: 1.5},
		},
	}}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = sessions
	handler := NewHandler(opts)

	body := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1")
	cost := body["run"].(map[string]any)["cost"].(map[string]any)
	if got := cost["total_usd"].(float64); got != 1.92 {
		t.Fatalf("cost total_usd = %v; want 1.92", got)
	}
	byPhase := cost["by_phase"].(map[string]any)
	if got := byPhase["research"].(float64); got != 1.92 {
		t.Fatalf("research cost = %v; want 1.92", got)
	}
}

func TestRunDetailIncludesActivePhaseTiming(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t)
	activeStart := time.Now().Add(-2 * time.Minute)
	if err := store.Modify(f.ID, func(current *feature.Feature) error {
		current.PhaseTimings = map[string]time.Duration{"inquire": 30 * time.Second}
		current.ActivePhaseStart = &activeStart
		current.ActiveTimingKey = "research"
		return nil
	}); err != nil {
		t.Fatalf("Modify() error = %v", err)
	}
	handler := NewHandler(baseReadHandlerOptions(store))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/runs/1", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	body := decodeBodyMap(t, w.Result())
	timing := body["run"].(map[string]any)["timing"].(map[string]any)
	byPhase := timing["by_phase"].(map[string]any)
	rawResearch, ok := byPhase["research"]
	if !ok {
		t.Fatalf("timing by_phase = %v; want active research entry", byPhase)
	}
	researchSeconds := int64(rawResearch.(float64))
	if researchSeconds < 120 || researchSeconds > 121 {
		t.Fatalf("active research seconds = %d; want 120..121", researchSeconds)
	}
	if totalSeconds := int64(timing["total_seconds"].(float64)); totalSeconds != 30+researchSeconds {
		t.Fatalf("timing total_seconds = %d; want %d", totalSeconds, 30+researchSeconds)
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
		&fakeSessionView{
			MockSessionView: &mocks.MockSessionView{
				IDVal:        "sess-run2-a",
				FeatureIDVal: f.ID,
				PhaseVal:     feature.PhaseImplement,
				KindVal:      ports.KindPhase,
				StatusVal:    ports.SessionDone,
				StartedAtVal: now.Add(-2 * time.Minute),
			},
			runNumber: 2,
		},
		&fakeSessionView{
			MockSessionView: &mocks.MockSessionView{
				IDVal:        "sess-run3-a",
				FeatureIDVal: f.ID,
				PhaseVal:     feature.PhasePlan,
				KindVal:      ports.KindPhase,
				StatusVal:    ports.SessionDone,
				StartedAtVal: now.Add(-1 * time.Minute),
			},
			runNumber: 3,
		},
		&fakeSessionView{
			MockSessionView: &mocks.MockSessionView{
				IDVal:        "sess-run2-b",
				FeatureIDVal: f.ID,
				PhaseVal:     feature.PhaseResearch,
				KindVal:      ports.KindPhase,
				StatusVal:    ports.SessionDone,
				StartedAtVal: now.Add(-3 * time.Minute),
			},
			runNumber: 2,
		},
		&fakeSessionView{
			MockSessionView: &mocks.MockSessionView{
				IDVal:        "sess-other",
				FeatureIDVal: "other-feature",
				PhaseVal:     feature.PhaseImplement,
				KindVal:      ports.KindPhase,
				StatusVal:    ports.SessionDone,
				StartedAtVal: now,
			},
			runNumber: 2,
		},
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

func TestRunSessionsIncludeSessionsBeyondRecentList(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t, sealedRun(t, 2))
	now := time.Date(2026, 6, 13, 13, 0, 0, 0, time.UTC)
	views := []ports.SessionView{&fakeSessionView{
		MockSessionView: &mocks.MockSessionView{
			IDVal:        "sess-run2-old",
			FeatureIDVal: f.ID,
			PhaseVal:     feature.PhasePlan,
			KindVal:      ports.KindPhase,
			StatusVal:    ports.SessionDone,
			StartedAtVal: now.Add(-time.Hour),
		},
		runNumber: 2,
	}}
	for i := range maxSessionListRecentSessions + 1 {
		views = append(views, &fakeSessionView{
			MockSessionView: &mocks.MockSessionView{
				IDVal:        fmt.Sprintf("sess-newer-%02d", i),
				FeatureIDVal: "other-feature",
				PhaseVal:     feature.PhaseImplement,
				KindVal:      ports.KindPhase,
				StatusVal:    ports.SessionDone,
				StartedAtVal: now.Add(time.Duration(i) * time.Second),
			},
			runNumber: 1,
		})
	}
	opts := baseReadHandlerOptions(store)
	opts.Sessions = fakeSessionManager{views: views}
	handler := NewHandler(opts)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/features/"+f.ID+"/runs/2/sessions", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	rawSessions := decodeBodyMap(t, w.Result())["sessions"].([]any)
	if len(rawSessions) != 1 || rawSessions[0].(map[string]any)["id"] != "sess-run2-old" {
		t.Fatalf("run sessions = %v; want only sess-run2-old", rawSessions)
	}
}

func runSessionsByID(t *testing.T, handler http.Handler, featureID string, run int) map[string]map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/features/"+featureID+"/runs/"+strconv.Itoa(run)+"/sessions", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	byID := map[string]map[string]any{}
	for _, raw := range decodeBodyMap(t, w.Result())["sessions"].([]any) {
		sess := raw.(map[string]any)
		id := sess["id"].(string)
		if _, dup := byID[id]; dup {
			t.Fatalf("session %q listed twice", id)
		}
		byID[id] = sess
	}
	return byID
}

func withSessionCosts(records ...feature.SessionCostRecord) func(*feature.Run) {
	return func(r *feature.Run) { r.SessionCosts = records }
}

func TestRunSessionsFromLedgerAfterRestart(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t, sealedRun(t, 2, withSessionCosts(
		feature.SessionCostRecord{SessionID: "f-research", PhaseKey: "research", ObserverPhase: "Research", CostUSD: 1.5, InputTokens: 10, OutputTokens: 5},
		feature.SessionCostRecord{SessionID: "f-impl", PhaseKey: "phase-1-impl", RepoName: "api", CostUSD: 2},
		feature.SessionCostRecord{PhaseKey: "plan", CostUSD: 9},
	)))
	opts := baseReadHandlerOptions(store)
	opts.Sessions = fakeSessionManager{}
	got := runSessionsByID(t, NewHandler(opts), f.ID, 2)

	if len(got) != 2 {
		t.Fatalf("sessions = %v; want f-research and f-impl", got)
	}
	research := got["f-research"]
	if research["phase"] != "Research" || research["status"] != "Done" || research["kind"] != "phase" || research["run_number"] != float64(2) {
		t.Fatalf("f-research = %v", research)
	}
	if usage := research["usage"].(map[string]any); usage["cost_usd"] != 1.5 || usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(5) {
		t.Fatalf("f-research usage = %v", usage)
	}
	if impl := got["f-impl"]; impl["phase"] != "Implement" || impl["repo"] != "api" {
		t.Fatalf("f-impl = %v", impl)
	}
}

func TestRunSessionsKeepSealedRunSessionWhenIDReused(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t, sealedRun(t, 2, withSessionCosts(
		feature.SessionCostRecord{SessionID: "f-roadmap-01", PhaseKey: "phase-1-plan", CostUSD: 3},
	)))
	opts := baseReadHandlerOptions(store)
	opts.Sessions = fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		MockSessionView: &mocks.MockSessionView{
			IDVal:        "f-roadmap-01",
			FeatureIDVal: f.ID,
			PhaseVal:     feature.PhasePlan,
			KindVal:      ports.KindPhase,
			StatusVal:    ports.SessionRunning,
		},
		runNumber: 3,
	}}}
	got := runSessionsByID(t, NewHandler(opts), f.ID, 2)

	sess, ok := got["f-roadmap-01"]
	if !ok || len(got) != 1 {
		t.Fatalf("sessions = %v; want the sealed run's f-roadmap-01", got)
	}
	if sess["status"] != "Done" || sess["phase"] != "Plan" || sess["run_number"] != float64(2) {
		t.Fatalf("f-roadmap-01 = %v; want the sealed run's ledger row", sess)
	}
}

func TestRunSessionsPreferInMemoryRowOverLedger(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t, sealedRun(t, 2, withSessionCosts(
		feature.SessionCostRecord{SessionID: "f-design", PhaseKey: "design", CostUSD: 1},
	)))
	opts := baseReadHandlerOptions(store)
	opts.Sessions = fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		MockSessionView: &mocks.MockSessionView{
			IDVal:        "f-design",
			FeatureIDVal: f.ID,
			PhaseVal:     feature.PhaseDesign,
			KindVal:      ports.KindPhase,
			StatusVal:    ports.SessionWaitingHelp,
		},
		runNumber: 2,
	}}}
	got := runSessionsByID(t, NewHandler(opts), f.ID, 2)

	if len(got) != 1 || got["f-design"]["status"] != "WaitingHelp" {
		t.Fatalf("sessions = %v; want the in-memory f-design row only", got)
	}
}

func TestRunSessionsAggregateLedgerRecords(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t, sealedRun(t, 2, withSessionCosts(
		feature.SessionCostRecord{SessionID: "f-review", PhaseKey: "review", ObserverPhase: "Final Review", CostUSD: 1.25, InputTokens: 100, OutputTokens: 10},
		feature.SessionCostRecord{SessionID: "f-review", PhaseKey: "review", ObserverPhase: "Final Review", RepoName: "web", CostUSD: 0.75, InputTokens: 50, OutputTokens: 20},
	)))
	opts := baseReadHandlerOptions(store)
	opts.Sessions = fakeSessionManager{}
	got := runSessionsByID(t, NewHandler(opts), f.ID, 2)

	sess, ok := got["f-review"]
	if !ok || len(got) != 1 {
		t.Fatalf("sessions = %v; want one f-review row", got)
	}
	if sess["phase"] != "Final Review" || sess["repo"] != "web" {
		t.Fatalf("f-review = %v", sess)
	}
	if usage := sess["usage"].(map[string]any); usage["cost_usd"] != 2.0 || usage["input_tokens"] != float64(150) || usage["output_tokens"] != float64(30) {
		t.Fatalf("f-review usage = %v", usage)
	}
}

func TestRunSessionsIncludeEarlierResumedSegments(t *testing.T) {
	t.Parallel()
	earlier := feature.SessionCostRecord{SessionID: "f-design", PhaseKey: "design", CostUSD: 0.115, InputTokens: 100, OutputTokens: 10}
	latest := feature.SessionCostRecord{SessionID: "f-design", PhaseKey: "design", CostUSD: 0.271, InputTokens: 200, OutputTokens: 20}
	other := feature.SessionCostRecord{SessionID: "f-other", PhaseKey: "design", CostUSD: 5}
	latestUsage := &llm.Usage{CostUSD: 0.271, CostSource: "provider_estimate", InputTokens: 200, OutputTokens: 20}
	cases := []struct {
		name       string
		sealed     bool
		ledger     []feature.SessionCostRecord
		usage      *llm.Usage
		exited     bool
		wantCost   float64
		wantInput  int
		wantOutput int
	}{
		{name: "ended resumed session", ledger: []feature.SessionCostRecord{earlier, other, latest}, usage: latestUsage, exited: true, wantCost: 0.386, wantInput: 300, wantOutput: 30},
		{name: "live resumed session", ledger: []feature.SessionCostRecord{earlier, other}, usage: latestUsage, wantCost: 0.386, wantInput: 300, wantOutput: 30},
		{name: "ended resumed segment without usage", ledger: []feature.SessionCostRecord{earlier}, usage: &llm.Usage{}, exited: true, wantCost: 0.115, wantInput: 100, wantOutput: 10},
		{name: "ended session never resumed", ledger: []feature.SessionCostRecord{other, latest}, usage: latestUsage, exited: true, wantCost: 0.271, wantInput: 200, wantOutput: 20},
		{name: "live session never resumed", ledger: []feature.SessionCostRecord{other}, usage: latestUsage, wantCost: 0.271, wantInput: 200, wantOutput: 20},
		{name: "sealed run session never resumed", sealed: true, ledger: []feature.SessionCostRecord{latest}, usage: latestUsage, exited: true, wantCost: 0.271, wantInput: 200, wantOutput: 20},
		{name: "sealed run resumed session", sealed: true, ledger: []feature.SessionCostRecord{earlier, latest}, usage: latestUsage, exited: true, wantCost: 0.386, wantInput: 300, wantOutput: 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runNumber := 1
			var store *feature.Store
			var f *feature.Feature
			if tc.sealed {
				runNumber = 2
				store, f = seedHistoryFeature(t, sealedRun(t, 2, withSessionCosts(tc.ledger...)))
			} else {
				store, f = seedHistoryFeature(t)
				if err := store.Modify(f.ID, func(current *feature.Feature) error {
					current.SessionCosts = tc.ledger
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			status := ports.SessionRunning
			var done chan struct{}
			if tc.exited {
				status = ports.SessionDone
				done = make(chan struct{})
				close(done)
			}
			opts := baseReadHandlerOptions(store)
			opts.Sessions = fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
				MockSessionView: &mocks.MockSessionView{
					IDVal:        "f-design",
					FeatureIDVal: f.ID,
					PhaseVal:     feature.PhaseDesign,
					KindVal:      ports.KindPhase,
					StatusVal:    status,
				},
				runNumber: runNumber,
				usage:     tc.usage,
				done:      done,
			}}}
			got := runSessionsByID(t, NewHandler(opts), f.ID, runNumber)

			usage := got["f-design"]["usage"].(map[string]any)
			if cost, _ := usage["cost_usd"].(float64); math.Abs(cost-tc.wantCost) > 1e-9 {
				t.Errorf("cost_usd = %v; want %v", cost, tc.wantCost)
			}
			if in, _ := usage["input_tokens"].(float64); int(in) != tc.wantInput {
				t.Errorf("input_tokens = %v; want %d", in, tc.wantInput)
			}
			if out, _ := usage["output_tokens"].(float64); int(out) != tc.wantOutput {
				t.Errorf("output_tokens = %v; want %d", out, tc.wantOutput)
			}
		})
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

type pricedSessionView struct {
	*fakeSessionView
	result *llm.ResultMessage
}

func (s *pricedSessionView) Cost() *llm.ResultMessage { return s.result }

func TestReconciledCostCanLowerSessionAndRunEstimates(t *testing.T) {
	t.Parallel()
	for _, amount := range []float64{0, 0.25} {
		store, f := seedHistoryFeature(t)
		if err := store.Modify(f.ID, func(current *feature.Feature) error {
			current.PhaseCosts = map[string]float64{}
			current.ActiveTimingKey = "research"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		sess := &pricedSessionView{fakeSessionView: &fakeSessionView{
			MockSessionView: &mocks.MockSessionView{
				IDVal:        "reconciled",
				FeatureIDVal: f.ID,
				PhaseVal:     feature.PhaseResearch,
				KindVal:      ports.KindPhase,
				StatusVal:    ports.SessionRunning,
			},
			runNumber: 1,
			usage:     &llm.Usage{CostUSD: amount, CostSource: "provider_estimate"},
		}, result: &llm.ResultMessage{TotalCostUSD: 2}}
		if got := sessionSummaryDTO(sess).Usage.CostUSD; got != amount {
			t.Fatalf("session retained old estimate: %v", got)
		}
		opts := baseReadHandlerOptions(store)
		opts.Sessions = fakeSessionManager{views: []ports.SessionView{sess}}
		body := getJSONMap(t, NewHandler(opts), "/api/v1/features/"+f.ID+"/runs/1")
		cost := body["run"].(map[string]any)["cost"].(map[string]any)
		if got := cost["total_usd"].(float64); got != amount {
			t.Fatalf("run retained old estimate: %v", got)
		}
	}
}

func TestSealedRunPhaseIsWhereTheRunStopped(t *testing.T) {
	t.Parallel()
	readyAt := time.Date(2026, 6, 13, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		edit func(*feature.Run)
		want string
	}{
		{name: "roadmap implement", edit: func(r *feature.Run) { r.ActiveTimingKey = "phase-1-impl" }, want: "Implement"},
		{name: "rebase cycle", edit: func(r *feature.Run) { r.ActiveTimingKey = "rebase-2" }, want: "Implement"},
		{name: "roadmap plan", edit: func(r *feature.Run) { r.ActiveTimingKey = "phase-2-plan" }, want: "Plan"},
		{name: "design", edit: func(r *feature.Run) { r.ActiveTimingKey = "design" }, want: "Design"},
		{name: "pending review wins over timing key", edit: func(r *feature.Run) {
			r.ActiveTimingKey = "research"
			r.PendingReviewPhase = phasePtr(feature.PhaseDesign)
		}, want: "Design"},
		{name: "output ready is publish", edit: func(r *feature.Run) {
			r.ActiveTimingKey = "review"
			r.OutputReadyAt = &readyAt
		}, want: "Publish"},
		{name: "no phase data", edit: func(*feature.Run) {}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, f := seedHistoryFeature(t, sealedRun(t, 2, tc.edit))
			if err := store.Modify(f.ID, func(current *feature.Feature) error {
				current.RunCount = 2
				current.CurrentPhase = feature.PhaseInquire
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			handler := NewHandler(baseReadHandlerOptions(store))

			run := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/2")["run"].(map[string]any)
			if got, _ := run["current_phase"].(string); got != tc.want {
				t.Fatalf("run detail current_phase = %v; want %q", got, tc.want)
			}
			detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)[entityFeature].(map[string]any)
			history := detail["historical_runs"].([]any)
			if len(history) != 1 {
				t.Fatalf("historical_runs = %v; want one run", history)
			}
			if got := history[0].(map[string]any)["current_phase"]; got != run["current_phase"] {
				t.Fatalf("historical current_phase = %v; want %v to match run detail", got, run["current_phase"])
			}
			if got := detail["active_run_detail"].(map[string]any)["current_phase"]; got != feature.PhaseInquire.String() {
				t.Fatalf("active run current_phase = %v; want live %s", got, feature.PhaseInquire)
			}
			active := getJSONMap(t, handler, "/api/v1/features/"+f.ID+"/runs/1")["run"].(map[string]any)
			if got := active["current_phase"]; got != feature.PhaseInquire.String() {
				t.Fatalf("active run detail current_phase = %v; want live %s", got, feature.PhaseInquire)
			}
		})
	}
}
