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

package orchestrator_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
	"go.uber.org/fx"
	"gopkg.in/yaml.v3"
)

func newHookStore(t *testing.T, f *feature.Feature) *feature.Store {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	if f != nil {
		f.SchemaVersion = feature.SchemaVersionCurrent
		if err := store.Save(f); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// writeClaudeSettings writes a minimal .claude/settings.json into repoPath so
// ImportRepoSettings has rules to consume.
func writeClaudeSettings(t *testing.T, repoPath string, allow []string) {
	t.Helper()
	dir := filepath.Join(repoPath, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	settings := struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}{}
	settings.Permissions.Allow = allow
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
}

// newTestObserver returns an enabled observer rooted at dir, built through
// the fx module (the production construction path). The emitter writes JSONL
// to dir/<featureID>/events.jsonl. The returned stop function runs the
// module's shutdown lifecycle once.
func newTestObserver(t *testing.T, dir string) (*observe.Observer, func()) {
	t.Helper()
	cfg := config.NewDefault()
	var obs *observe.Observer
	app := fx.New(
		fx.Supply(cfg, fx.Annotate(dir, fx.ResultTags(`name:"stateDir"`))),
		observe.Module,
		fx.Populate(&obs),
		fx.NopLogger,
	)
	if err := app.Err(); err != nil {
		t.Fatalf("build observer: %v", err)
	}
	var stopped bool
	stop := func() {
		if !stopped {
			stopped = true
			if err := app.Stop(context.Background()); err != nil {
				t.Errorf("stop observer: %v", err)
			}
		}
	}
	t.Cleanup(stop)
	return obs, stop
}

// readEvents returns all JSONL events from the given path. The file may not
// exist yet when the observer has not flushed; returns an empty slice.
func readEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []map[string]any
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		out = append(out, m)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

// containsEventType reports whether any event has the given EventType.
func containsEventType(events []map[string]any, t string) bool {
	for _, e := range events {
		if e["event_type"] == t {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// T12. OnFeatureCreated fires permission import for each repo.
// ---------------------------------------------------------------------------

// assertImportedRules decodes the frozen on-disk permission scope file
// (<scope>.json with a rules array) and requires at least minRules rules.
func assertImportedRules(t *testing.T, path string, minRules int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read permission scope file %s: %v", path, err)
	}
	var persisted struct {
		Rules []struct {
			ToolPattern string `json:"tool_pattern"`
			Effect      string `json:"effect"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("decode permission scope file %s: %v", path, err)
	}
	if len(persisted.Rules) < minRules {
		t.Errorf("%s: expected at least %d imported rules, got %d", path, minRules, len(persisted.Rules))
	}
}

func TestBuildHooks_OnFeatureCreated_FiresPermissionImport(t *testing.T) {
	tmp := t.TempDir()
	var store *permission.Store
	app := fx.New(
		fx.Supply(fx.Annotate(filepath.Join(tmp, "state"), fx.ResultTags(`name:"stateDir"`))),
		permission.Module,
		fx.Populate(&store),
		fx.NopLogger,
	)
	if err := app.Err(); err != nil {
		t.Fatalf("build permission store: %v", err)
	}
	permStoreDir := filepath.Join(tmp, "permissions")

	repo1 := filepath.Join(tmp, "repo1")
	repo2 := filepath.Join(tmp, "repo2")
	writeClaudeSettings(t, repo1, []string{"Bash(ls *)"})
	writeClaudeSettings(t, repo2, []string{"Edit(*)"})

	f := &feature.Feature{
		ID: "f1",
		Repos: []feature.FeatureRepo{
			{Name: "repo1", Path: repo1},
			{Name: "repo2", Path: repo2},
		},
	}

	h := orchestrator.BuildHooks(nil, store, newHookStore(t, nil))
	if h.OnFeatureCreated == nil {
		t.Fatal("OnFeatureCreated hook is nil")
	}
	h.OnFeatureCreated(f)

	assertImportedRules(t, filepath.Join(permStoreDir, "repo1.json"), 1)
	assertImportedRules(t, filepath.Join(permStoreDir, "repo2.json"), 1)
}

// ---------------------------------------------------------------------------
// T13. OnFeatureStarted emits feature.started via observer.
// ---------------------------------------------------------------------------

func TestBuildHooks_OnFeatureStarted_FiresObserver(t *testing.T) {
	tmp := t.TempDir()
	obs, stop := newTestObserver(t, tmp)
	defer stop()

	if err := os.MkdirAll(filepath.Join(tmp, "f1"), 0o755); err != nil {
		t.Fatalf("mkdir feature dir: %v", err)
	}

	f := &feature.Feature{
		ID:   "f1",
		Name: "My feature",
		Repos: []feature.FeatureRepo{
			{Name: "repoA", Path: "/tmp/repoA"},
		},
	}
	fs := newHookStore(t, f)

	h := orchestrator.BuildHooks(obs, nil, fs)
	if h.OnFeatureStarted == nil {
		t.Fatal("OnFeatureStarted hook is nil")
	}
	h.OnFeatureStarted("f1")

	// Force the emitter to flush by shutting the observer down. The defer
	// above will call it again but Shutdown is idempotent.
	stop()
	events := readEvents(t, filepath.Join(tmp, "f1", "events.jsonl"))
	if !containsEventType(events, "feature.started") {
		t.Errorf("expected feature.started event, got events: %+v", events)
	}
}

// ---------------------------------------------------------------------------
// T14. Populated hooks emit their expected events.
// ---------------------------------------------------------------------------

func TestBuildHooks_PopulatedHooksEmit(t *testing.T) {
	tmp := t.TempDir()
	obs, stop := newTestObserver(t, tmp)
	defer stop()

	if err := os.MkdirAll(filepath.Join(tmp, "f2"), 0o755); err != nil {
		t.Fatalf("mkdir feature dir: %v", err)
	}

	f := &feature.Feature{
		ID:           "f2",
		Name:         "Emit hooks",
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: "r", Path: "/tmp/r"},
		},
	}
	fs := newHookStore(t, f)

	h := orchestrator.BuildHooks(obs, nil, fs)

	// Each populated hook should emit a distinct event type.
	cases := []struct {
		name       string
		invoke     func()
		wantEvType string
	}{
		{
			name:       "OnFeatureStarted",
			invoke:     func() { h.OnFeatureStarted("f2") },
			wantEvType: "feature.started",
		},
		{
			name:       "OnFeatureInterrupted",
			invoke:     func() { h.OnFeatureInterrupted("f2") },
			wantEvType: "feature.interrupted",
		},
		{
			name:       "OnFeatureCompleted",
			invoke:     func() { h.OnFeatureCompleted("f2", f) },
			wantEvType: "feature.completed",
		},
		{
			name:       "OnFeatureFailed",
			invoke:     func() { h.OnFeatureFailed("f2", errcat.SessionCrashed, errcat.ClassBlocking, "boom") },
			wantEvType: "feature.failed",
		},
		{
			name:       "OnPhaseStarted",
			invoke:     func() { h.OnPhaseStarted("f2", feature.PhaseImplement) },
			wantEvType: "phase.started",
		},
		{
			name:       "OnPhaseCompleted",
			invoke:     func() { h.OnPhaseCompleted("f2", feature.PhaseImplement, nil) },
			wantEvType: "phase.completed",
		},
		{
			name:       "OnRecoveryAction",
			invoke:     func() { h.OnRecoveryAction("f2", "", "resume") },
			wantEvType: "recovery.action",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.invoke()
		})
	}

	// Flush and check events.
	stop()
	events := readEvents(t, filepath.Join(tmp, "f2", "events.jsonl"))
	for _, tc := range cases {
		if !containsEventType(events, tc.wantEvType) {
			t.Errorf("missing expected event %q after %s", tc.wantEvType, tc.name)
		}
	}
}

// ---------------------------------------------------------------------------
// T15. All hook fields populated with observability and permissions disabled.
// ---------------------------------------------------------------------------

func TestBuildHooks_AllFieldsPopulated_AndNilSafe(t *testing.T) {
	// Every field must be non-nil and safely invokable with optional
	// observability and permission collaborators disabled.
	h := orchestrator.BuildHooks(nil, nil, newHookStore(t, nil))

	v := reflect.ValueOf(h)
	typ := v.Type()
	for i := 0; i < v.NumField(); i++ {
		fv := v.Field(i)
		if fv.Kind() != reflect.Func {
			continue
		}
		if fv.IsNil() {
			t.Errorf("Hooks.%s is nil — BuildHooks must populate every field", typ.Field(i).Name)
		}
	}

	// Invoke every hook with zero-ish arguments. None should panic; none
	// should crash on nil observer/permission store.
	f := &feature.Feature{ID: "nil-safe", Repos: []feature.FeatureRepo{{Name: "r"}}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("hook invocation panicked: %v", r)
		}
	}()

	h.OnFeatureCreated(f)
	h.OnFeatureStarted("x")
	h.OnFeatureInterrupted("x")
	h.OnFeatureCompleted("x", f)
	h.OnFeatureFailed("x", errcat.SessionCrashed, errcat.ClassBlocking, "e")
	h.OnPhaseStarted("x", feature.PhaseImplement)
	h.OnPhaseCompleted("x", feature.PhaseImplement, nil)
	h.OnRecoveryScanned([]ports.RecoveryItem{})
	h.OnRecoveryAction("x", "", "resume")
	h.OnReviewRequired("x", feature.PhaseImplement)
	h.OnPublishStarted("x")
	h.OnPublishCompleted("x", map[string]string{}, nil)
	h.OnFeatureSummaryNeeded("x", f)
	h.OnFeatureConfigChanged("x", feature.ConfigSnapshot{}, feature.ConfigSnapshot{})
}

// TestBuildHooks_OnFeatureConfigChanged_FiresObserver verifies that the hook
// invokes Observer.ConfigChanged, which writes a feature.config_changed line
// to events.jsonl with the before/after snapshots.
func TestBuildHooks_OnFeatureConfigChanged_FiresObserver(t *testing.T) {
	tmp := t.TempDir()
	obs, stop := newTestObserver(t, tmp)
	defer stop()

	if err := os.MkdirAll(filepath.Join(tmp, "f1"), 0o755); err != nil {
		t.Fatalf("mkdir feature dir: %v", err)
	}

	f := &feature.Feature{
		ID:   "f1",
		Name: "My feature",
	}
	fs := newHookStore(t, f)

	h := orchestrator.BuildHooks(obs, nil, fs)
	if h.OnFeatureConfigChanged == nil {
		t.Fatal("OnFeatureConfigChanged hook is nil")
	}
	before := feature.ConfigSnapshot{Inquireness: feature.InquirenessMedium}
	after := feature.ConfigSnapshot{Inquireness: feature.InquirenessHigh}
	h.OnFeatureConfigChanged("f1", before, after)

	stop()
	events := readEvents(t, filepath.Join(tmp, "f1", "events.jsonl"))
	if !containsEventType(events, "feature.config_changed") {
		t.Errorf("expected feature.config_changed event, got events: %+v", events)
	}
}

func TestBuildHooks_OnFeatureRewound_FiresObserver(t *testing.T) {
	tmp := t.TempDir()
	featureID := "rewind-hook-feat"
	if err := os.MkdirAll(filepath.Join(tmp, featureID), 0o755); err != nil {
		t.Fatalf("mkdir feature dir: %v", err)
	}
	obs, _ := newTestObserver(t, tmp)
	fs := newHookStore(t, &feature.Feature{
		ID:                 featureID,
		Name:               "rewind hook",
		TraceID:            "1234567890abcdef1234567890abcdef",
		FeatureSpanID:      "1234567890abcdef",
		ActiveRun:          2,
		TotalRoadmapPhases: 4,
	})
	h := orchestrator.BuildHooks(obs, nil, fs)
	h.OnFeatureRewound(featureID, feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 2,
	}, feature.PhaseImplement, 1, 2)

	events := readEvents(t, filepath.Join(tmp, featureID, "events.jsonl"))
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1: %+v", len(events), events)
	}
	if events[0]["event_type"] != "feature.rewound" {
		t.Fatalf("event_type = %v, want feature.rewound", events[0]["event_type"])
	}
	if events[0]["run_number"] != float64(2) {
		t.Errorf("run_number = %v, want 2", events[0]["run_number"])
	}
	data, ok := events[0]["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %T, want map", events[0]["data"])
	}
	if data["roadmap_phase"] != float64(2) {
		t.Errorf("data.roadmap_phase = %v, want 2", data["roadmap_phase"])
	}
	if data["source_run"] != float64(1) || data["new_run"] != float64(2) {
		t.Errorf("data source/new run = %v/%v, want 1/2", data["source_run"], data["new_run"])
	}
}

// TestBuildHooks_OnFeatureSummaryNeeded_BuildsInputFromRunFailureRecord
// verifies that OnFeatureSummaryNeeded renders the run's stored canonical
// failure record into the summary input: a failed feature's
// observe-summary.yaml carries the record's catalog code and class, while a
// healthy feature's summary leaves both empty.
// featureSummaryMirror decodes the frozen observe-summary.yaml feature block
// (observe's summary artifact shape) for assertions in this package's tests.
type featureSummaryMirror struct {
	Feature struct {
		ID         string `yaml:"id"`
		ErrorCode  string `yaml:"error_code,omitempty"`
		ErrorClass string `yaml:"error_class,omitempty"`
	} `yaml:"feature"`
}

func TestBuildHooks_OnFeatureSummaryNeeded_BuildsInputFromRunFailureRecord(t *testing.T) {
	writeSummary := func(t *testing.T, f *feature.Feature) featureSummaryMirror {
		t.Helper()
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, f.ID), 0o755); err != nil {
			t.Fatalf("mkdir feature dir: %v", err)
		}
		obs, stop := newTestObserver(t, tmp)
		defer stop()

		fs := newHookStore(t, f)

		h := orchestrator.BuildHooks(obs, nil, fs, tmp)
		if h.OnFeatureSummaryNeeded == nil {
			t.Fatal("OnFeatureSummaryNeeded hook is nil")
		}
		h.OnFeatureSummaryNeeded(f.ID, f)
		stop()

		data, err := os.ReadFile(filepath.Join(tmp, f.ID, "observe-summary.yaml"))
		if err != nil {
			t.Fatalf("read observe-summary.yaml: %v", err)
		}
		var summary featureSummaryMirror
		if err := yaml.Unmarshal(data, &summary); err != nil {
			t.Fatalf("unmarshal observe-summary.yaml: %v\n%s", err, data)
		}
		return summary
	}

	t.Run("failed feature carries record code and class", func(t *testing.T) {
		f := &feature.Feature{
			ID:           "fsum-failed",
			Name:         "Summary failure",
			Status:       feature.StatusFailed,
			CurrentPhase: feature.PhaseImplement,
			Repos:        []feature.FeatureRepo{{Name: "r", Path: "/tmp/r"}},
		}
		f.Run().Failure = &errcat.FailureRecord{
			Code:        errcat.IterationBudgetExhausted,
			Context:     &errcat.RecordContext{Phase: &errcat.CodePhase{Name: feature.PhaseImplement.FailureName()}},
			Diagnostics: "iteration cap",
		}

		summary := writeSummary(t, f)
		if summary.Feature.ErrorCode != string(errcat.IterationBudgetExhausted) {
			t.Errorf("error_code = %q, want %q", summary.Feature.ErrorCode, errcat.IterationBudgetExhausted)
		}
		if summary.Feature.ErrorClass != string(errcat.ClassBlocking) {
			t.Errorf("error_class = %q, want %q", summary.Feature.ErrorClass, errcat.ClassBlocking)
		}
	})

	t.Run("healthy feature leaves error fields empty", func(t *testing.T) {
		f := &feature.Feature{
			ID:           "fsum-healthy",
			Name:         "Summary healthy",
			Status:       feature.StatusDone,
			CurrentPhase: feature.PhasePublish,
			Repos:        []feature.FeatureRepo{{Name: "r", Path: "/tmp/r"}},
		}

		summary := writeSummary(t, f)
		if summary.Feature.ErrorCode != "" {
			t.Errorf("error_code = %q, want empty for a healthy feature", summary.Feature.ErrorCode)
		}
		if summary.Feature.ErrorClass != "" {
			t.Errorf("error_class = %q, want empty for a healthy feature", summary.Feature.ErrorClass)
		}
	})
}

// ---------------------------------------------------------------------------
// T10. Orchestrator.Delete — stops sessions then calls lifecycle.
// ---------------------------------------------------------------------------

func TestOrchestrator_Delete_StopsSessionsAndDeletesStoredFeature(t *testing.T) {
	f := &feature.Feature{ID: "f1", Name: "Delete me"}
	store, manager := newStoredFeatureFixture(t, f)
	sessions := mocks.NewMockSessionManager()
	sessions.FeatureSessionsFn = func(featureID string) []ports.SessionView {
		return []ports.SessionView{
			mocks.NewMockSessionView("s1", featureID),
			mocks.NewMockSessionView("s2", featureID),
		}
	}
	o := orchestrator.New(orchestrator.Deps{Manager: manager, Store: store, Sessions: sessions}, orchestrator.Hooks{})
	defer o.Shutdown()

	if _, err := o.Delete(f.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(sessions.StopCalls) != 2 {
		t.Errorf("StopSession calls = %d, want 2", len(sessions.StopCalls))
	}
	seen := map[string]bool{}
	for _, id := range sessions.StopCalls {
		seen[id] = true
	}
	if !seen["s1"] || !seen["s2"] {
		t.Errorf("StopSession ids = %v, want both s1 and s2", sessions.StopCalls)
	}
	if _, err := store.Load(f.ID); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load after delete = %v, want not exist", err)
	}
}
