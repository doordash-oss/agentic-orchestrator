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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

var errTestLifecycleFailed = errors.New("lifecycle delete failed")

func errorsIs(err, target error) bool { return errors.Is(err, target) }

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

// newTestObserver returns an enabled observer rooted at dir. The emitter writes
// JSONL to dir/<featureID>/events.jsonl.
func newTestObserver(dir string) *observe.Observer {
	return observe.New(true, dir, false, "", false, "")
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

func TestBuildHooks_OnFeatureCreated_FiresPermissionImport(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "perm-store")
	store := permission.NewStore(storeDir)

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

	h := orchestrator.BuildHooks(nil, store, nil)
	if h.OnFeatureCreated == nil {
		t.Fatal("OnFeatureCreated hook is nil")
	}
	h.OnFeatureCreated(f)

	r1, err := store.Load("repo1")
	if err != nil {
		t.Fatalf("Load repo1 rules: %v", err)
	}
	r2, err := store.Load("repo2")
	if err != nil {
		t.Fatalf("Load repo2 rules: %v", err)
	}
	if len(r1) == 0 {
		t.Errorf("expected at least one rule imported for repo1, got 0")
	}
	if len(r2) == 0 {
		t.Errorf("expected at least one rule imported for repo2, got 0")
	}
}

// ---------------------------------------------------------------------------
// T13. OnFeatureStarted emits feature.started via observer.
// ---------------------------------------------------------------------------

func TestBuildHooks_OnFeatureStarted_FiresObserver(t *testing.T) {
	tmp := t.TempDir()
	obs := newTestObserver(tmp)
	defer obs.Shutdown()

	if err := os.MkdirAll(filepath.Join(tmp, "f1"), 0o755); err != nil {
		t.Fatalf("mkdir feature dir: %v", err)
	}

	fs := mocks.NewMockFeatureStore()
	f := &feature.Feature{
		ID:   "f1",
		Name: "My feature",
		Repos: []feature.FeatureRepo{
			{Name: "repoA", Path: "/tmp/repoA"},
		},
	}
	fs.LoadFn = func(id string) (*feature.Feature, error) {
		return f, nil
	}

	h := orchestrator.BuildHooks(obs, nil, fs)
	if h.OnFeatureStarted == nil {
		t.Fatal("OnFeatureStarted hook is nil")
	}
	h.OnFeatureStarted("f1")

	// Force the emitter to flush by shutting the observer down. The defer
	// above will call it again but Shutdown is idempotent.
	obs.Shutdown()
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
	obs := newTestObserver(tmp)
	defer obs.Shutdown()

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
	fs := mocks.NewMockFeatureStore()
	fs.LoadFn = func(id string) (*feature.Feature, error) { return f, nil }

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
			invoke:     func() { h.OnFeatureFailed("f2", "execution_failed", "boom") },
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
	obs.Shutdown()
	events := readEvents(t, filepath.Join(tmp, "f2", "events.jsonl"))
	for _, tc := range cases {
		if !containsEventType(events, tc.wantEvType) {
			t.Errorf("missing expected event %q after %s", tc.wantEvType, tc.name)
		}
	}
}

// ---------------------------------------------------------------------------
// T15. All hook fields populated and nil-safe.
// ---------------------------------------------------------------------------

func TestBuildHooks_AllFieldsPopulated_AndNilSafe(t *testing.T) {
	// Build with nil collaborators. Every field must be non-nil (no
	// accidental omission) and every hook must be safely invokable.
	h := orchestrator.BuildHooks(nil, nil, nil)

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
	h.OnFeatureFailed("x", "t", "e")
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
	obs := newTestObserver(tmp)
	defer obs.Shutdown()

	if err := os.MkdirAll(filepath.Join(tmp, "f1"), 0o755); err != nil {
		t.Fatalf("mkdir feature dir: %v", err)
	}

	fs := mocks.NewMockFeatureStore()
	f := &feature.Feature{
		ID:   "f1",
		Name: "My feature",
	}
	fs.LoadFn = func(id string) (*feature.Feature, error) {
		return f, nil
	}

	h := orchestrator.BuildHooks(obs, nil, fs)
	if h.OnFeatureConfigChanged == nil {
		t.Fatal("OnFeatureConfigChanged hook is nil")
	}
	before := feature.ConfigSnapshot{Inquireness: feature.InquirenessMedium}
	after := feature.ConfigSnapshot{Inquireness: feature.InquirenessHigh}
	h.OnFeatureConfigChanged("f1", before, after)

	obs.Shutdown()
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
	obs := newTestObserver(tmp)
	fs := mocks.NewMockFeatureStore()
	fs.LoadFn = func(id string) (*feature.Feature, error) {
		if id != featureID {
			return nil, nil
		}
		return &feature.Feature{
			ID:                 featureID,
			Name:               "rewind hook",
			TraceID:            "1234567890abcdef1234567890abcdef",
			FeatureSpanID:      "1234567890abcdef",
			ActiveRun:          2,
			TotalRoadmapPhases: 4,
		}, nil
	}
	fs.LoadRunFn = func(id string, runNumber int) (*feature.Run, error) {
		switch runNumber {
		case 1:
			return &feature.Run{RunNumber: 1, BackupBranches: map[string]string{repoName: "feature/hook-backup"}}, nil
		case 2:
			return &feature.Run{RunNumber: 2, CarriedPhases: []string{"roadmap", "phase-01/plan"}}, nil
		default:
			return nil, nil
		}
	}

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

// ---------------------------------------------------------------------------
// T10. Orchestrator.Delete — stops sessions then calls lifecycle.
// ---------------------------------------------------------------------------

func TestOrchestrator_Delete_StopsSessionsAndCallsLifecycle(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		lifecycle := mocks.NewMockFeatureLifecycle()
		sessions := mocks.NewMockSessionManager()
		sessions.FeatureSessionsFn = func(featureID string) []ports.SessionView {
			return []ports.SessionView{
				mocks.NewMockSessionView("s1", featureID),
				mocks.NewMockSessionView("s2", featureID),
			}
		}

		o := orchestrator.New(orchestrator.Deps{
			Lifecycle: lifecycle,
			Sessions:  sessions,
		}, orchestrator.Hooks{})

		if err := o.Delete("f1"); err != nil {
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

		foundDelete := false
		for _, c := range lifecycle.Calls {
			if c.Method == "Delete" && len(c.Args) == 1 && c.Args[0] == "f1" {
				foundDelete = true
			}
		}
		if !foundDelete {
			t.Errorf("Lifecycle.Delete(\"f1\") not recorded; calls = %+v", lifecycle.Calls)
		}
	})

	t.Run("lifecycle_error_wrapped", func(t *testing.T) {
		lifecycle := mocks.NewMockFeatureLifecycle()
		lifecycle.DeleteFn = func(featureID string) error {
			return errTestLifecycleFailed
		}

		o := orchestrator.New(orchestrator.Deps{
			Lifecycle: lifecycle,
		}, orchestrator.Hooks{})

		err := o.Delete("f1")
		if err == nil {
			t.Fatal("Delete returned nil error, want wrapped")
		}
		if !errorsIs(err, errTestLifecycleFailed) {
			t.Errorf("error chain does not contain lifecycle error; got %v", err)
		}
	})
}
