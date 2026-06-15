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

package feature

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"gopkg.in/yaml.v3"
)

func TestStoreSaveAndLoad(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	f := &Feature{
		ID:           "test-001",
		Name:         "Test Feature",
		Slug:         "test-feature",
		Description:  "A test feature",
		Tags:         []string{"frontend", "backend"},
		Created:      time.Now().Truncate(time.Second),
		Status:       StatusCreated,
		CurrentPhase: PhaseResearch,
		Repos: []FeatureRepo{
			{Name: "test-repo", Path: "/tmp/test"},
		},
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
		ExitCriteria:  "tests pass",
		SchemaVersion: SchemaVersionCurrent,
	}

	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.Load("test-001")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.ID != f.ID {
		t.Errorf("ID mismatch: got %s, want %s", loaded.ID, f.ID)
	}
	if loaded.Name != f.Name {
		t.Errorf("Name mismatch: got %s, want %s", loaded.Name, f.Name)
	}
	if loaded.Status != f.Status {
		t.Errorf("Status mismatch: got %v, want %v", loaded.Status, f.Status)
	}
	if !slices.Equal(loaded.Tags, f.Tags) {
		t.Errorf("Tags mismatch: got %v, want %v", loaded.Tags, f.Tags)
	}
	if len(loaded.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(loaded.Repos))
	}
	if loaded.Repos[0].Name != "test-repo" {
		t.Errorf("Repo name mismatch: got %s", loaded.Repos[0].Name)
	}
}

func TestStoreList(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	// Empty list
	features, err := store.List()
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features, got %d", len(features))
	}

	// Add two features
	f1 := &Feature{ID: "feat-001", Name: "Feature 1", Slug: "feature-1", Status: StatusCreated, SchemaVersion: SchemaVersionCurrent}
	f2 := &Feature{ID: "feat-002", Name: "Feature 2", Slug: "feature-2", Status: StatusImplementing, SchemaVersion: SchemaVersionCurrent}

	if err := store.Save(f1); err != nil {
		t.Fatalf("save f1: %v", err)
	}
	if err := store.Save(f2); err != nil {
		t.Fatalf("save f2: %v", err)
	}

	features, err = store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(features) != 2 {
		t.Errorf("expected 2 features, got %d", len(features))
	}
}

func TestStoreListPartialLoad(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	// Save a valid feature
	good := &Feature{ID: "good-001", Name: "Good", Slug: "good", Status: StatusCreated, SchemaVersion: SchemaVersionCurrent}
	if err := store.Save(good); err != nil {
		t.Fatalf("save good: %v", err)
	}

	// Create a corrupted feature.yaml (duplicate YAML key triggers yaml.v3 error)
	badDir := filepath.Join(dir, "bad-002")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("mkdir bad: %v", err)
	}
	corruptedYAML := "id: bad-002\nname: Bad\nartifacts:\n  plan: a.md\nartifacts:\n  plan: b.md\n"
	if err := os.WriteFile(filepath.Join(badDir, "feature.yaml"), []byte(corruptedYAML), 0o644); err != nil {
		t.Fatalf("write bad: %v", err)
	}

	features, err := store.List()

	// Should still return the good feature
	if len(features) != 1 {
		t.Errorf("expected 1 feature, got %d", len(features))
	}
	if len(features) > 0 && features[0].ID != "good-001" {
		t.Errorf("expected good-001, got %s", features[0].ID)
	}

	// Should return a PartialLoadError
	if err == nil {
		t.Fatal("expected PartialLoadError, got nil")
	}
	if !IsPartialLoadError(err) {
		t.Fatalf("expected PartialLoadError, got %T: %v", err, err)
	}
	var ple *PartialLoadError
	if errors.As(err, &ple) {
		if len(ple.Warnings) != 1 {
			t.Errorf("expected 1 warning, got %d", len(ple.Warnings))
		}
		if ple.Warnings[0].ID != "bad-002" {
			t.Errorf("expected warning for bad-002, got %s", ple.Warnings[0].ID)
		}
	}
}

func TestStoreDelete(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	f := &Feature{ID: "to-delete", Name: "Delete Me", Slug: "delete-me", SchemaVersion: SchemaVersionCurrent}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := store.Delete("to-delete"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := store.Load("to-delete"); err == nil {
		t.Error("expected error loading deleted feature")
	}
}

// TestStoreNeedUserInputPathPersistence locks in that the run-scoped
// PendingNeedUserInputPath survives a save/load round-trip via the run
// shadow synchroniser.
func TestStoreNeedUserInputPathPersistence(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	gatePath := "/tmp/feat-nui-001/runs/run-001/phase-01/implement/agentic/iteration-02/need-user-input.yaml"
	f := &Feature{
		ID:                       "feat-nui-001",
		Name:                     "Need User Input pause",
		Slug:                     "need-user-input-pause",
		Status:                   StatusNeedUserInput,
		PendingNeedUserInputPath: gatePath,
		SchemaVersion:            SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.Load("feat-nui-001")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Status != StatusNeedUserInput {
		t.Errorf("Status: got %v, want StatusNeedUserInput", loaded.Status)
	}
	if loaded.PendingNeedUserInputPath != gatePath {
		t.Errorf("PendingNeedUserInputPath: got %q, want %q", loaded.PendingNeedUserInputPath, gatePath)
	}
	if run := loaded.Run(); run == nil || run.PendingNeedUserInputPath != gatePath {
		t.Errorf("run.PendingNeedUserInputPath: got %q, want %q", run.PendingNeedUserInputPath, gatePath)
	}
}

func TestStoreFailureFieldsPersistence(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	f := &Feature{
		ID:            "fail-001",
		Name:          "Failing Feature",
		Slug:          "failing-feature",
		Status:        StatusFailed,
		LastError:     "no progress for 3 consecutive iterations",
		FailureType:   FailureSafetyRail,
		SchemaVersion: SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.Load("fail-001")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.LastError != f.LastError {
		t.Errorf("LastError: got %q, want %q", loaded.LastError, f.LastError)
	}
	if loaded.FailureType != f.FailureType {
		t.Errorf("FailureType: got %q, want %q", loaded.FailureType, f.FailureType)
	}
}

func TestStoreLoadReconcilesSuccessfulStatusWithTerminalFinalReviewFailure(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	f := &Feature{
		ID:            "corrupt-fr-001",
		Name:          "Corrupt Final Review",
		Slug:          "corrupt-final-review",
		Status:        StatusCodeReady,
		CurrentPhase:  PhasePublish,
		LastError:     "protocol violation: final_review_reviewer @ /tmp/iter: verification-report.yaml is malformed",
		FailureType:   FailureProtocolViolation,
		SchemaVersion: SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.Load("corrupt-fr-001")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Status != StatusFailed {
		t.Fatalf("Status = %s, want Failed", loaded.Status)
	}
	if loaded.CurrentPhase != PhaseFinalReview {
		t.Fatalf("CurrentPhase = %s, want FinalReview", loaded.CurrentPhase)
	}
	if loaded.FailureType != FailureProtocolViolation {
		t.Fatalf("FailureType = %q, want %q", loaded.FailureType, FailureProtocolViolation)
	}
}

func TestStoreMuteInputNotificationsPersistence(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)
	muted := true

	f := &Feature{
		ID:                     "notify-001",
		Name:                   "Notification Override",
		Slug:                   "notification-override",
		Status:                 StatusCreated,
		MuteInputNotifications: &muted,
		SchemaVersion:          SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.Load("notify-001")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.MuteInputNotifications == nil || !*loaded.MuteInputNotifications {
		t.Fatal("expected mute_input_notifications override to round-trip as true")
	}
}

func TestStoreConcurrentModify(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	f := &Feature{
		ID:            "concurrent",
		Name:          "Concurrent Test",
		Slug:          "concurrent-test",
		SchemaVersion: SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	// Launch many concurrent Modify calls that each append a help request.
	// Without the mutex, these would race and produce a corrupted file.
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := store.Modify("concurrent", func(f *Feature) error {
				f.HelpQueue = append(f.HelpQueue, HelpRequest{
					Question: fmt.Sprintf("question-%d", i),
					Pending:  true,
				})
				return nil
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("modify error: %v", err)
	}

	// Verify the file is valid and all 50 help requests were persisted
	loaded, err := store.Load("concurrent")
	if err != nil {
		t.Fatalf("load after concurrent writes: %v", err)
	}
	if len(loaded.HelpQueue) != n {
		t.Errorf("expected %d help requests, got %d", n, len(loaded.HelpQueue))
	}
}

// TestStoreConcurrentRepoImplModify exercises the per-repo state-mutation
// pattern the orchestrator uses inside its per-stage fan-out: every concurrent
// goroutine calls store.Modify to set its own RepoImpl[name].Status. Each
// goroutine writes a distinct key so the assertion is deterministic — every
// entry must round-trip via the YAML serializer with the new status.
func TestStoreConcurrentRepoImplModify(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	repos := []string{"repo-a", "repo-b", "repo-c", "repo-d", "repo-e"}
	f := &Feature{
		ID:            "concurrent-repo-impl",
		Name:          "Concurrent RepoImpl Test",
		Slug:          "concurrent-repo-impl",
		SchemaVersion: SchemaVersionCurrent,
		Repos: []FeatureRepo{
			{Name: "repo-a"}, {Name: "repo-b"}, {Name: "repo-c"},
			{Name: "repo-d"}, {Name: "repo-e"},
		},
		RepoStates: map[string]*RepoState{
			"repo-a": {},
			"repo-b": {},
			"repo-c": {},
			"repo-d": {},
			"repo-e": {},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	var wg sync.WaitGroup
	for _, name := range repos {
		wg.Add(1)
		go func(repoName string) {
			defer wg.Done()
			err := store.Modify("concurrent-repo-impl", func(f *Feature) error {
				if f.RepoStates[repoName] != nil {
					f.RepoStates[repoName].Touched = true
				}
				return nil
			})
			if err != nil {
				t.Errorf("modify %s: %v", repoName, err)
			}
		}(name)
	}
	wg.Wait()

	loaded, err := store.Load("concurrent-repo-impl")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range repos {
		state := loaded.RepoStates[name]
		if state == nil {
			t.Errorf("RepoStates[%q] missing after concurrent writes", name)
			continue
		}
		if !state.Touched {
			t.Errorf("RepoStates[%q].Touched = false, want true", name)
		}
	}
}

func TestPublishableFieldYAMLRoundTrip(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	// Save a feature with Publishable = false
	falseVal := false
	f1 := &Feature{
		ID:            "pub-001",
		Name:          "Explicit False Ptr",
		Slug:          "explicit-false-ptr",
		Status:        StatusCreated,
		Repos:         []FeatureRepo{{Name: "repo-a", Path: "/tmp/a", Publishable: &falseVal}},
		SchemaVersion: SchemaVersionCurrent,
	}
	if err := store.Save(f1); err != nil {
		t.Fatalf("save f1: %v", err)
	}
	loaded1, err := store.Load("pub-001")
	if err != nil {
		t.Fatalf("load f1: %v", err)
	}
	if loaded1.Repos[0].Publishable == nil {
		t.Fatal("expected Publishable to be non-nil after round-trip")
	}
	if *loaded1.Repos[0].Publishable != false {
		t.Errorf("expected Publishable = false, got %v", *loaded1.Repos[0].Publishable)
	}

	// Save a feature with Publishable = nil (omitted)
	f2 := &Feature{
		ID:            "pub-002",
		Name:          "Nil Pointer Field",
		Slug:          "nil-pointer-field",
		Status:        StatusCreated,
		Repos:         []FeatureRepo{{Name: "repo-b", Path: "/tmp/b"}},
		SchemaVersion: SchemaVersionCurrent,
	}
	if err := store.Save(f2); err != nil {
		t.Fatalf("save f2: %v", err)
	}
	loaded2, err := store.Load("pub-002")
	if err != nil {
		t.Fatalf("load f2: %v", err)
	}
	if loaded2.Repos[0].Publishable != nil {
		t.Errorf("expected Publishable to be nil, got %v", *loaded2.Repos[0].Publishable)
	}

	// Verify raw YAML does NOT contain "publishable" when the field is nil
	data, err := os.ReadFile(filepath.Join(dir, "pub-002", "feature.yaml"))
	if err != nil {
		t.Fatalf("read yaml: %v", err)
	}
	if strings.Contains(string(data), "publishable") {
		t.Error("expected publishable field to be omitted from YAML when nil")
	}
}

// ---------------------------------------------------------------------------
// SealAndForkRun committing-lifecycle + idempotent-reseal tests
// ---------------------------------------------------------------------------

// seedFeatureForSealFork saves a minimal feature with ActiveRun:1 RunCount:1
// and an unsealed run-001 to disk, then returns the reloaded *Feature so
// f.run is the active run.
func seedFeatureForSealFork(t *testing.T, store *Store, id string) *Feature {
	t.Helper()
	f := &Feature{
		ID:            id,
		Name:          "Seal Fork Test",
		Slug:          "seal-fork-test",
		Description:   "test",
		Created:       time.Now(),
		Status:        StatusCreated,
		CurrentPhase:  PhaseInquire,
		SchemaVersion: SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("seed load: %v", err)
	}
	return loaded
}

func TestStore_SealAndForkRun_CommittingLifecycle_SkeletonPersistedBeforePopulate(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)
	id := "feat-commit-skel"
	_ = seedFeatureForSealFork(t, store, id)

	// populate inspects the on-disk run-002 YAML at the moment it runs.
	populateSaw := false
	populateErr := error(nil)
	updated, err := store.SealAndForkRun(id,
		func(oldRun *Run) error {
			now := time.Now()
			oldRun.SealedAt = &now
			oldRun.SealReason = SealReasonRewind
			return nil
		},
		func(oldRun *Run) (*Run, error) {
			return &Run{RunNumber: 2, CarriedFromRun: 1, Committing: true}, nil
		},
		func(oldRun, newRun *Run) error {
			// Read run-002's run.yaml from disk — the Store should have
			// persisted the skeleton before invoking us.
			p := filepath.Join(dir, id, "runs", "run-002", "run.yaml")
			data, readErr := os.ReadFile(p)
			if readErr != nil {
				populateErr = fmt.Errorf("reading skeleton: %w", readErr)
				return populateErr
			}
			var r Run
			if err := yaml.Unmarshal(data, &r); err != nil {
				populateErr = fmt.Errorf("unmarshaling skeleton: %w", err)
				return populateErr
			}
			if !r.Committing {
				populateErr = fmt.Errorf("skeleton on disk has Committing=%v, want true", r.Committing)
				return populateErr
			}
			populateSaw = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("SealAndForkRun: %v", err)
	}
	if updated == nil {
		t.Fatal("SealAndForkRun returned nil feature")
	}
	if !populateSaw {
		t.Fatalf("populate did not observe Committing:true skeleton (err=%v)", populateErr)
	}

	// Final run-002 YAML has Committing:false.
	finalRun, err := store.LoadRun(id, 2)
	if err != nil {
		t.Fatalf("load final run-002: %v", err)
	}
	if finalRun.Committing {
		t.Errorf("final run-002 Committing=true, want false")
	}

	// feature.yaml bumped to ActiveRun:2, RunCount:2.
	reloaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	if reloaded.ActiveRun != 2 || reloaded.RunCount != 2 {
		t.Errorf("ActiveRun/RunCount = %d/%d, want 2/2", reloaded.ActiveRun, reloaded.RunCount)
	}

	// Sealed run-001 has SealedAt stamped.
	sealed, err := store.LoadRun(id, 1)
	if err != nil {
		t.Fatalf("load sealed run-001: %v", err)
	}
	if sealed.SealedAt == nil {
		t.Error("sealed run-001 SealedAt is nil")
	}
}

func TestStore_SealAndForkRun_CommittingLifecycle_PopulateCanMutateNewRun(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)
	id := "feat-commit-mutate"
	_ = seedFeatureForSealFork(t, store, id)

	if _, err := store.SealAndForkRun(id,
		func(oldRun *Run) error {
			now := time.Now()
			oldRun.SealedAt = &now
			return nil
		},
		func(oldRun *Run) (*Run, error) {
			return &Run{RunNumber: 2, CarriedFromRun: 1, Committing: true}, nil
		},
		func(oldRun, newRun *Run) error {
			newRun.CarriedPhases = []string{"inquire", "research"}
			newRun.Artifacts = map[string]string{"inquire": "inquire/out.md"}
			return nil
		},
	); err != nil {
		t.Fatalf("SealAndForkRun: %v", err)
	}

	loaded, err := store.LoadRun(id, 2)
	if err != nil {
		t.Fatalf("load run-002: %v", err)
	}
	if got, want := loaded.CarriedPhases, []string{"inquire", "research"}; !stringSlicesEqual(got, want) {
		t.Errorf("CarriedPhases = %v, want %v", got, want)
	}
	if loaded.Artifacts["inquire"] != "inquire/out.md" {
		t.Errorf("Artifacts[inquire] = %q, want %q", loaded.Artifacts["inquire"], "inquire/out.md")
	}
	if loaded.Committing {
		t.Error("final run-002 Committing=true, want false")
	}
}

func TestStore_SealAndForkRun_PopulateErrorLeavesSkeleton(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)
	id := "feat-populate-err"
	_ = seedFeatureForSealFork(t, store, id)

	_, err := store.SealAndForkRun(id,
		func(oldRun *Run) error {
			now := time.Now()
			oldRun.SealedAt = &now
			return nil
		},
		func(oldRun *Run) (*Run, error) {
			return &Run{RunNumber: 2, CarriedFromRun: 1, Committing: true}, nil
		},
		func(oldRun, newRun *Run) error {
			return errors.New("boom")
		},
	)
	if err == nil {
		t.Fatal("SealAndForkRun returned nil error, want populate error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %q, want contains %q", err.Error(), "boom")
	}

	// Skeleton is still on disk with Committing:true.
	skelPath := filepath.Join(dir, id, "runs", "run-002", "run.yaml")
	data, readErr := os.ReadFile(skelPath)
	if readErr != nil {
		t.Fatalf("skeleton not on disk: %v", readErr)
	}
	var r Run
	if yerr := yaml.Unmarshal(data, &r); yerr != nil {
		t.Fatalf("unmarshal skeleton: %v", yerr)
	}
	if !r.Committing {
		t.Error("skeleton Committing=false, want true")
	}

	// feature.yaml still at ActiveRun:1 / RunCount:1 (bump did not run).
	reloaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	if reloaded.ActiveRun != 1 || reloaded.RunCount != 1 {
		t.Errorf("ActiveRun/RunCount = %d/%d, want 1/1", reloaded.ActiveRun, reloaded.RunCount)
	}
	// Sealed run-001 SealedAt is stamped (seal step completed).
	if reloaded.Run() == nil || reloaded.Run().SealedAt == nil {
		t.Error("sealed run-001 SealedAt is nil on reload")
	}

	// CleanupOrphanRuns sweeps the skeleton.
	deleted, cerr := store.CleanupOrphanRuns(id)
	if cerr != nil {
		t.Fatalf("CleanupOrphanRuns: %v", cerr)
	}
	if len(deleted) != 1 || deleted[0] != 2 {
		t.Errorf("deleted = %v, want [2]", deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, id, "runs", "run-002")); !os.IsNotExist(err) {
		t.Errorf("run-002 still exists after cleanup: %v", err)
	}
}

func TestStore_SealAndForkRun_IdempotentReseal(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)
	id := "feat-reseal"

	// Seed feature.yaml pointing at ActiveRun:1 and a sealed run-001 on disk.
	// This simulates the post-cleanup state: orphan run-002 was removed,
	// ActiveRun still points at the sealed old run.
	f := &Feature{
		ID:            id,
		Name:          "Reseal Test",
		Slug:          "reseal-test",
		Description:   "test",
		Created:       time.Now(),
		Status:        StatusInquiryNeedsReview,
		CurrentPhase:  PhaseKnowledgeBase,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: SchemaVersionCurrent,
	}
	// Build a sealed run-001 (SealedAt set).
	firstSeal := time.Now().Add(-1 * time.Hour)
	rewindTarget := PhasePlan
	f.SetRun(&Run{
		RunNumber:      1,
		SealedAt:       &firstSeal,
		SealReason:     SealReasonRewind,
		RewindTarget:   &rewindTarget,
		BackupBranches: map[string]string{"repo-a": "feature/foo-v1"},
	})
	if err := store.Save(f); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	// store.Save skips writing the sealed run — persist run-001 directly.
	run001Path := filepath.Join(dir, id, "runs", "run-001", "run.yaml")
	run001Data, err := yaml.Marshal(f.Run())
	if err != nil {
		t.Fatalf("marshal seed run-001: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(run001Path), 0o755); err != nil {
		t.Fatalf("mkdir seed run-001: %v", err)
	}
	if err := os.WriteFile(run001Path, run001Data, 0o644); err != nil {
		t.Fatalf("write seed run-001: %v", err)
	}

	// Re-trigger rewind: re-stamp with a DIFFERENT RewindTarget and new backup branches.
	newTarget := PhaseResearch
	laterSeal := time.Now()
	if _, err := store.SealAndForkRun(id,
		func(oldRun *Run) error {
			oldRun.SealedAt = &laterSeal
			oldRun.SealReason = SealReasonRewind
			oldRun.RewindTarget = &newTarget
			oldRun.BackupBranches = map[string]string{"repo-a": "feature/foo-v2"}
			return nil
		},
		func(oldRun *Run) (*Run, error) {
			return &Run{RunNumber: 2, CarriedFromRun: 1, Committing: true}, nil
		},
		func(oldRun, newRun *Run) error {
			newRun.CarriedPhases = []string{"inquire"}
			return nil
		},
	); err != nil {
		t.Fatalf("SealAndForkRun (re-seal): %v", err)
	}

	// Re-loaded run-001 has the newer seal fields.
	resealed, err := store.LoadRun(id, 1)
	if err != nil {
		t.Fatalf("load resealed run-001: %v", err)
	}
	if resealed.SealedAt == nil || !resealed.SealedAt.Equal(laterSeal) {
		t.Errorf("resealed SealedAt = %v, want %v", resealed.SealedAt, laterSeal)
	}
	if resealed.RewindTarget == nil || *resealed.RewindTarget != PhaseResearch {
		t.Errorf("resealed RewindTarget = %v, want %v", resealed.RewindTarget, PhaseResearch)
	}
	if resealed.BackupBranches["repo-a"] != "feature/foo-v2" {
		t.Errorf("BackupBranches[repo-a] = %q, want %q", resealed.BackupBranches["repo-a"], "feature/foo-v2")
	}

	// run-002 is committed (Committing:false).
	newRun, err := store.LoadRun(id, 2)
	if err != nil {
		t.Fatalf("load run-002: %v", err)
	}
	if newRun.Committing {
		t.Error("run-002 Committing=true after reseal, want false")
	}

	// feature.yaml bumped to ActiveRun:2, RunCount:2.
	reloaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	if reloaded.ActiveRun != 2 || reloaded.RunCount != 2 {
		t.Errorf("ActiveRun/RunCount = %d/%d, want 2/2", reloaded.ActiveRun, reloaded.RunCount)
	}
}

func TestStore_SealAndForkRun_ForkMustSetCommitting(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)
	id := "feat-fork-no-commit"
	_ = seedFeatureForSealFork(t, store, id)

	_, err := store.SealAndForkRun(id,
		func(oldRun *Run) error {
			now := time.Now()
			oldRun.SealedAt = &now
			return nil
		},
		func(oldRun *Run) (*Run, error) {
			// Programmer error: Committing not set.
			return &Run{RunNumber: 2, CarriedFromRun: 1}, nil
		},
		nil,
	)
	if err == nil {
		t.Fatal("SealAndForkRun returned nil error, want fork-committing error")
	}
	if !strings.Contains(err.Error(), "Committing:true") {
		t.Errorf("error = %q, want contains %q", err.Error(), "Committing:true")
	}

	// run-002 does NOT exist (skeleton write never ran).
	if _, err := os.Stat(filepath.Join(dir, id, "runs", "run-002")); !os.IsNotExist(err) {
		t.Errorf("run-002 exists, want missing after fork programmer error")
	}

	// feature.yaml unchanged at ActiveRun:1.
	reloaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	if reloaded.ActiveRun != 1 || reloaded.RunCount != 1 {
		t.Errorf("ActiveRun/RunCount = %d/%d, want 1/1", reloaded.ActiveRun, reloaded.RunCount)
	}

	// Sealed run-001 IS on disk (seal step completed before fork ran).
	run001, err := store.LoadRun(id, 1)
	if err != nil {
		t.Fatalf("load run-001: %v", err)
	}
	if run001.SealedAt == nil {
		t.Error("run-001 SealedAt is nil after failed fork; want seal completed")
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestStoreRoundTripsSchemaVersion verifies SchemaVersion 2 round-trips through
// Save/Load and is emitted to the on-disk YAML payload.
func TestStoreRoundTripsSchemaVersion(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	f := &Feature{
		ID:            "fresh-001",
		Name:          "Fresh Feature",
		Slug:          "fresh-feature",
		Description:   "schema-version round trip",
		Created:       time.Now().Truncate(time.Second),
		Status:        StatusCreated,
		CurrentPhase:  PhaseResearch,
		Repos:         []FeatureRepo{{Name: "repo-a", Path: "/tmp/a"}},
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: SchemaVersionCurrent,
	}

	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.Load("fresh-001")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.SchemaVersion != SchemaVersionCurrent {
		t.Errorf("SchemaVersion after load = %d, want %d", loaded.SchemaVersion, SchemaVersionCurrent)
	}

	rawPath := filepath.Join(dir, "fresh-001", "feature.yaml")
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read feature.yaml: %v", err)
	}
	wantStamp := fmt.Sprintf("schema_version: %d", SchemaVersionCurrent)
	if !bytes.Contains(raw, []byte(wantStamp)) {
		t.Errorf("feature.yaml missing %q; got:\n%s", wantStamp, string(raw))
	}
}

// TestStoreLoadAcceptsCurrentSchemaVersion verifies the positive path:
// a feature stamped at SchemaVersionCurrent loads cleanly.
func TestStoreLoadAcceptsCurrentSchemaVersion(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	const featureID = "fresh-001"
	featureDir := filepath.Join(dir, featureID)
	runDir := filepath.Join(featureDir, "runs", "run-001")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	featureYAML := fmt.Sprintf(`id: %s
name: Fresh Feature
slug: fresh-feature
description: current-schema feature
created: 2026-01-01T00:00:00Z
status: Created
current_phase: 1
repos:
  - name: repo-a
    path: /tmp/a
    worktree_path: ""
    branch: ""
models: {}
exit_criteria: ""
inquireness: medium
permissions_queue: []
help_queue: []
active_run: 1
run_count: 1
schema_version: %d
`, featureID, SchemaVersionCurrent)
	if err := os.WriteFile(filepath.Join(featureDir, "feature.yaml"), []byte(featureYAML), 0o644); err != nil {
		t.Fatalf("write feature.yaml: %v", err)
	}
	runYAML := `started_at: 2026-01-01T00:00:00Z
phase_timings: {}
phase_costs: {}
artifacts: {}
`
	if err := os.WriteFile(filepath.Join(runDir, "run.yaml"), []byte(runYAML), 0o644); err != nil {
		t.Fatalf("write run.yaml: %v", err)
	}

	loaded, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("Load() error = %v; want nil", err)
	}
	if loaded.SchemaVersion != SchemaVersionCurrent {
		t.Errorf("loaded.SchemaVersion = %d, want %d", loaded.SchemaVersion, SchemaVersionCurrent)
	}
	if loaded.Run().Setup != nil {
		t.Fatalf("legacy run setup = %+v, want nil", loaded.Run().Setup)
	}
}

func TestStoreSetupStateRoundTrip(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and isolated store state.
	dir := t.TempDir()
	store := NewStore(dir)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	repos := []FeatureRepo{{
		Name:   "api",
		Path:   "/tmp/api",
		Branch: "feature/setup-state",
	}}
	f := &Feature{
		ID:            "setup-001",
		Name:          "Setup State",
		Slug:          "setup-state",
		Description:   "setup serialization",
		Created:       now,
		Status:        StatusSettingUpWorktrees,
		CurrentPhase:  PhaseKnowledgeBase,
		Repos:         repos,
		Models:        config.ModelConfig{},
		ExitCriteria:  "",
		Inquireness:   InquirenessMedium,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: SchemaVersionCurrent,
	}
	setup := NewActiveSetupState(repos, []string{"/tmp/image.png"}, []string{"/tmp/spec.md"}, now)
	task := setup.Tasks["worktree:api"]
	task.Path = "/tmp/worktrees/setup-state/api"
	setup.Tasks["worktree:api"] = task
	f.SetRun(&Run{RunNumber: 1, Setup: setup})

	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got := loaded.Run().Setup
	if got == nil {
		t.Fatal("loaded setup state is nil")
	}
	if got.Status != SetupStatusRunning || got.Attempt != 1 {
		t.Fatalf("loaded setup status/attempt = %q/%d, want running/1", got.Status, got.Attempt)
	}
	if !slices.Equal(got.TaskOrder, []string{"worktree:api", "image:1", "attachment:1"}) {
		t.Fatalf("loaded task order = %v, want worktree/image/attachment", got.TaskOrder)
	}
	if got.Tasks["worktree:api"].Path != "/tmp/worktrees/setup-state/api" {
		t.Fatalf("loaded worktree path = %q", got.Tasks["worktree:api"].Path)
	}
	if got.Tasks["worktree:api"].Branch != "feature/setup-state" {
		t.Fatalf("loaded worktree branch = %q", got.Tasks["worktree:api"].Branch)
	}
}

func TestStoreLoadMigratesLegacyBrainstormStatusAndArtifact(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewStore(dir)

	const featureID = "legacy-brainstorm-001"
	featureDir := filepath.Join(dir, featureID)
	runDir := filepath.Join(featureDir, "runs", "run-001")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	featureYAML := fmt.Sprintf(`id: %s
name: Legacy Brainstorm Feature
slug: legacy-brainstorm-feature
description: legacy brainstorm status
created: 2026-01-01T00:00:00Z
status: BrainstormReady
current_phase: 7
repos:
  - name: repo-a
    path: /tmp/a
models: {}
exit_criteria: ""
inquireness: medium
active_run: 1
run_count: 1
schema_version: %d
`, featureID, SchemaVersionCurrent)
	if err := os.WriteFile(filepath.Join(featureDir, "feature.yaml"), []byte(featureYAML), 0o644); err != nil {
		t.Fatalf("write feature.yaml: %v", err)
	}

	runYAML := `run_number: 1
artifacts:
  brainstorm: brainstorm.md
`
	if err := os.WriteFile(filepath.Join(runDir, "run.yaml"), []byte(runYAML), 0o644); err != nil {
		t.Fatalf("write run.yaml: %v", err)
	}

	loaded, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("Load() error = %v; want nil", err)
	}
	if loaded.Status != StatusDesignReady {
		t.Errorf("loaded.Status = %v, want %v", loaded.Status, StatusDesignReady)
	}
	wantArtifact := filepath.Join("brainstorm", "brainstorm.md")
	if got := loaded.Artifacts["design"]; got != wantArtifact {
		t.Errorf("loaded.Artifacts[design] = %q, want %q", got, wantArtifact)
	}
	if got := loaded.Run().Artifacts["design"]; got != wantArtifact {
		t.Errorf("loaded.Run().Artifacts[design] = %q, want %q", got, wantArtifact)
	}
}

// TestStoreLoadCurrentSchemaIgnoresLegacyJiraKeys verifies that a feature.yaml
// at SchemaVersionCurrent that still contains legacy jira_ticket / jira_base_url
// keys (left over from a pre-Jira-removal state dir) loads successfully, and
// that after a round-trip save those keys are not re-emitted while name /
// description text is preserved.
func TestStoreLoadCurrentSchemaIgnoresLegacyJiraKeys(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	const featureID = "jira-legacy-001"
	featureDir := filepath.Join(dir, featureID)
	runDir := filepath.Join(featureDir, "runs", "run-001")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	featureYAML := fmt.Sprintf(`id: %s
name: "Add login [PROJ-123]"
slug: add-login-proj-123
description: "Implements the requirements from PROJ-123."
created: 2026-01-01T00:00:00Z
status: Created
current_phase: 1
repos:
  - name: repo-a
    path: /tmp/a
    worktree_path: ""
    branch: ""
models: {}
exit_criteria: ""
inquireness: medium
permissions_queue: []
help_queue: []
active_run: 1
run_count: 1
schema_version: %d
jira_ticket: PROJ-123
jira_base_url: https://jira.example.com
`, featureID, SchemaVersionCurrent)
	if err := os.WriteFile(filepath.Join(featureDir, "feature.yaml"), []byte(featureYAML), 0o644); err != nil {
		t.Fatalf("write feature.yaml: %v", err)
	}
	runYAML := `started_at: 2026-01-01T00:00:00Z
phase_timings: {}
phase_costs: {}
artifacts: {}
`
	if err := os.WriteFile(filepath.Join(runDir, "run.yaml"), []byte(runYAML), 0o644); err != nil {
		t.Fatalf("write run.yaml: %v", err)
	}

	loaded, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("Load() error = %v; want nil (current schema with legacy jira keys must load cleanly)", err)
	}
	if loaded.SchemaVersion != SchemaVersionCurrent {
		t.Errorf("loaded.SchemaVersion = %d, want %d", loaded.SchemaVersion, SchemaVersionCurrent)
	}
	if loaded.Name != "Add login [PROJ-123]" {
		t.Errorf("loaded.Name = %q; want ticket-like name preserved", loaded.Name)
	}

	// Round-trip save and re-read the raw YAML.
	if err := store.Save(loaded); err != nil {
		t.Fatalf("Save() error = %v; want nil", err)
	}
	saved, err := os.ReadFile(filepath.Join(featureDir, "feature.yaml"))
	if err != nil {
		t.Fatalf("ReadFile after Save: %v", err)
	}

	savedStr := string(saved)
	if strings.Contains(savedStr, "jira_ticket") {
		t.Errorf("saved feature.yaml still contains 'jira_ticket'; should not be re-emitted after round-trip")
	}
	if strings.Contains(savedStr, "jira_base_url") {
		t.Errorf("saved feature.yaml still contains 'jira_base_url'; should not be re-emitted after round-trip")
	}
	if !strings.Contains(savedStr, "Add login [PROJ-123]") {
		t.Errorf("saved feature.yaml lost ticket-like text in name field")
	}
	if !strings.Contains(savedStr, "PROJ-123") {
		t.Errorf("saved feature.yaml lost ticket reference in description field")
	}
}
