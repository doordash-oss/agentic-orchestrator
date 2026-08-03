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
	"reflect"
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
	if len(loaded.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(loaded.Repos))
	}
	if loaded.Repos[0].Name != "test-repo" {
		t.Errorf("Repo name mismatch: got %s", loaded.Repos[0].Name)
	}
}

func TestStoreSaveAndLoadInputNotifications(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs isolate filesystem state.
	dir := t.TempDir()
	store := NewStore(dir)

	f := &Feature{
		ID:                 "test-input-notifications",
		Name:               "Test Input Notifications",
		Slug:               "test-input-notifications",
		Status:             StatusCreated,
		SchemaVersion:      SchemaVersionCurrent,
		InputNotifications: InputNotificationsMuted,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.InputNotifications != InputNotificationsMuted {
		t.Fatalf("InputNotifications = %q, want %q", loaded.InputNotifications, InputNotificationsMuted)
	}

	loaded.InputNotifications = PersistInputNotificationsMode(InputNotificationsDefault)
	if err := store.Save(loaded); err != nil {
		t.Fatalf("save default: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, f.ID, "feature.yaml"))
	if err != nil {
		t.Fatalf("read feature.yaml: %v", err)
	}
	if strings.Contains(string(raw), "input_notifications") {
		t.Fatalf("feature.yaml contains input_notifications for default mode:\n%s", string(raw))
	}
}

func TestStoreSaveAndLoadAutomaticReviewMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewStore(dir)
	f := &Feature{
		ID:                  "test-automatic-review-mode",
		Name:                "Test Automatic Review Mode",
		Slug:                "test-automatic-review-mode",
		Status:              StatusCreated,
		SchemaVersion:       SchemaVersionCurrent,
		AutomaticReviewMode: AutomaticReviewEnabled,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := NormalizeAutomaticReviewMode(loaded.AutomaticReviewMode); got != AutomaticReviewEnabled {
		t.Errorf("loaded AutomaticReviewMode = %q, want %q", got, AutomaticReviewEnabled)
	}
}

func TestStoreSaveAndLoadRebaseTargetsRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewStore(dir)
	now := time.Now().Truncate(time.Second)
	f := &Feature{
		ID:            "test-rebase-targets-roundtrip",
		Name:          "Test Rebase Targets RoundTrip",
		Slug:          "test-rebase-targets-roundtrip",
		Status:        StatusCreated,
		SchemaVersion: SchemaVersionCurrent,
		Created:       now,
		Parent: &ChildRelationship{
			ParentID: "parent-1",
			Kind:     ChildKindRebase,
			RebaseTargets: []RebaseRepoTarget{
				{
					Repo:        "repoA",
					Target:      "main",
					Ref:         "origin/main",
					Publishable: true,
					TargetSHA:   "0123456789abcdef0123456789abcdef01234567",
				},
				{
					Repo:        "repoB",
					Target:      "develop",
					Ref:         "develop",
					Publishable: false,
					TargetSHA:   "fedcba9876543210fedcba9876543210fedcba98",
				},
			},
			RebaseBehind: []string{"repoA", "repoB"},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Parent == nil || loaded.Parent.Kind != ChildKindRebase {
		t.Fatalf("loaded parent kind = %+v, want rebase", loaded.Parent)
	}
	if len(loaded.Parent.RebaseTargets) != 2 {
		t.Fatalf("loaded RebaseTargets = %+v, want 2", loaded.Parent.RebaseTargets)
	}
	gotA := loaded.Parent.RebaseTargets[0]
	if gotA.TargetSHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("repoA TargetSHA = %q, want persisted SHA", gotA.TargetSHA)
	}
	if gotA.Target != "main" || gotA.Ref != "origin/main" || !gotA.Publishable {
		t.Errorf("repoA target = %+v, want main/origin/main/publishable", gotA)
	}
	gotB := loaded.Parent.RebaseTargets[1]
	if gotB.TargetSHA != "fedcba9876543210fedcba9876543210fedcba98" {
		t.Errorf("repoB TargetSHA = %q, want persisted SHA", gotB.TargetSHA)
	}
	if !reflect.DeepEqual(loaded.Parent.RebaseBehind, []string{"repoA", "repoB"}) {
		t.Errorf("loaded RebaseBehind = %+v, want [repoA repoB]", loaded.Parent.RebaseBehind)
	}

	// Accessor round-trip.
	tgt, ok := loaded.RebaseTargetForRepo("repoA")
	if !ok || tgt.TargetSHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("RebaseTargetForRepo(repoA) = %+v ok=%v, want persisted SHA", tgt, ok)
	}
}

func TestStoreAbsentAutomaticReviewModeInheritsGlobal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewStore(dir)
	f := &Feature{
		ID:            "test-automatic-review-absent",
		Name:          "Test Automatic Review Absent",
		Slug:          "test-automatic-review-absent",
		Status:        StatusCreated,
		SchemaVersion: SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, f.ID, "feature.yaml"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(raw), "automatic_review_mode") {
		t.Fatalf("feature.yaml contains automatic_review_mode for default mode:\n%s", raw)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := NormalizeAutomaticReviewMode(loaded.AutomaticReviewMode); got != AutomaticReviewDefault {
		t.Errorf("loaded AutomaticReviewMode = %q, want %q", got, AutomaticReviewDefault)
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

func TestStoreReadPathsDoNotWaitForInFlightModify(t *testing.T) {
	const featureNameOriginal = "Original"

	// Not parallel: the test intentionally holds a Store.Modify goroutine open
	// while asserting read-path responsiveness.
	dir := t.TempDir()
	store := NewStore(dir)
	f := &Feature{
		ID:            "responsive-read",
		Name:          featureNameOriginal,
		Slug:          "responsive-read",
		Status:        StatusCreated,
		SchemaVersion: SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	modifyDone := make(chan error, 1)
	go func() {
		modifyDone <- store.Modify(f.ID, func(f *Feature) error {
			close(started)
			<-release
			f.Name = "Updated"
			return nil
		})
	}()
	<-started
	t.Cleanup(func() {
		close(release)
		if err := <-modifyDone; err != nil {
			t.Errorf("modify: %v", err)
		}
	})

	readDone := make(chan struct {
		features []*Feature
		loaded   *Feature
		err      error
	}, 1)
	go func() {
		features, err := store.List()
		if err != nil {
			readDone <- struct {
				features []*Feature
				loaded   *Feature
				err      error
			}{err: fmt.Errorf("list: %w", err)}
			return
		}
		loaded, err := store.Load(f.ID)
		if err != nil {
			readDone <- struct {
				features []*Feature
				loaded   *Feature
				err      error
			}{err: fmt.Errorf("load: %w", err)}
			return
		}
		readDone <- struct {
			features []*Feature
			loaded   *Feature
			err      error
		}{features: features, loaded: loaded}
	}()

	select {
	case got := <-readDone:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if len(got.features) != 1 {
			t.Fatalf("features len = %d, want 1", len(got.features))
		}
		if got.features[0].Name != featureNameOriginal {
			t.Fatalf("listed name = %q, want last committed name Original", got.features[0].Name)
		}
		if got.loaded.Name != featureNameOriginal {
			t.Fatalf("loaded name = %q, want last committed name Original", got.loaded.Name)
		}
	case <-time.After(time.Second):
		t.Fatal("read paths blocked behind in-flight Modify")
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

func TestStoreRelationshipChildrenReturnsActiveAndOrderedClosedHistory(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs isolate persisted relationship state.

	store := NewStore(t.TempDir())
	parentID := "parent-history"
	base := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	children := []*Feature{
		{
			ID:      "active",
			Name:    "active child",
			Created: base.Add(-5 * time.Hour),
			Parent:  &ChildRelationship{ParentID: parentID, Kind: ChildKindRefactor},
		},
		{
			ID:      "closed-older",
			Name:    "older close",
			Created: base.Add(-time.Hour),
			Parent: &ChildRelationship{
				ParentID:     parentID,
				Kind:         ChildKindRefactor,
				CloseOutcome: ChildCloseOutcomeDiscarded,
				ClosedAt:     timePointer(base.Add(-time.Hour)),
			},
		},
		{
			ID:      "closed-z",
			Name:    "same timestamps z",
			Created: base,
			Parent: &ChildRelationship{
				ParentID:     parentID,
				Kind:         ChildKindRefactor,
				CloseOutcome: ChildCloseOutcomeCompleted,
				ClosedAt:     timePointer(base),
			},
		},
		{
			ID:      "closed-a",
			Name:    "same timestamps a",
			Created: base,
			Parent: &ChildRelationship{
				ParentID:     parentID,
				Kind:         ChildKindRefactor,
				CloseOutcome: ChildCloseOutcomeDiscarded,
				ClosedAt:     timePointer(base),
			},
		},
		{
			ID:      "closed-created-newer",
			Name:    "same close newer creation",
			Created: base.Add(time.Hour),
			Parent: &ChildRelationship{
				ParentID:     parentID,
				Kind:         ChildKindRefactor,
				CloseOutcome: ChildCloseOutcomeCompleted,
				ClosedAt:     timePointer(base),
			},
		},
	}
	for _, child := range children {
		child.SchemaVersion = SchemaVersionCurrent
		if err := store.Save(child); err != nil {
			t.Fatalf("Save(%q): %v", child.ID, err)
		}
	}

	got, err := store.RelationshipChildren(parentID)
	if err != nil {
		t.Fatalf("RelationshipChildren(%q): %v", parentID, err)
	}
	if got.Active == nil || got.Active.ID != "active" {
		t.Fatalf("RelationshipChildren(%q).Active = %#v, want active", parentID, got.Active)
	}
	gotIDs := make([]string, len(got.Closed))
	for i, child := range got.Closed {
		gotIDs[i] = child.ID
	}
	wantIDs := []string{"closed-created-newer", "closed-a", "closed-z", "closed-older"}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("RelationshipChildren(%q).Closed IDs = %v, want %v", parentID, gotIDs, wantIDs)
	}
}

func TestStoreAllRelationshipChildrenMatchesPerParentScans(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs isolate persisted relationship state.

	store := NewStore(t.TempDir())
	base := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	records := []*Feature{
		{ID: "parent-a", Name: "parent a"},
		{ID: "parent-b", Name: "parent b"},
		{ID: "childless", Name: "no children"},
		{
			ID:      "a-active",
			Created: base,
			Parent:  &ChildRelationship{ParentID: "parent-a", Kind: ChildKindRefactor},
		},
		{
			ID:      "a-closed-new",
			Created: base,
			Parent: &ChildRelationship{
				ParentID:     "parent-a",
				Kind:         ChildKindRefactor,
				CloseOutcome: ChildCloseOutcomeCompleted,
				ClosedAt:     timePointer(base.Add(time.Hour)),
			},
		},
		{
			ID:      "a-closed-old",
			Created: base,
			Parent: &ChildRelationship{
				ParentID:     "parent-a",
				Kind:         ChildKindRefactor,
				CloseOutcome: ChildCloseOutcomeDiscarded,
				ClosedAt:     timePointer(base),
			},
		},
		{
			ID:      "b-closed",
			Created: base,
			Parent: &ChildRelationship{
				ParentID:     "parent-b",
				Kind:         ChildKindRefactor,
				CloseOutcome: ChildCloseOutcomeCompleted,
				ClosedAt:     timePointer(base),
			},
		},
	}
	for _, record := range records {
		record.SchemaVersion = SchemaVersionCurrent
		if err := store.Save(record); err != nil {
			t.Fatalf("Save(%q): %v", record.ID, err)
		}
	}

	all, err := store.AllRelationshipChildren()
	if err != nil {
		t.Fatalf("AllRelationshipChildren(): %v", err)
	}
	for _, parentID := range []string{"parent-a", "parent-b"} {
		perParent, err := store.RelationshipChildren(parentID)
		if err != nil {
			t.Fatalf("RelationshipChildren(%q): %v", parentID, err)
		}
		bulk := all[parentID]
		if bulk == nil {
			t.Fatalf("AllRelationshipChildren()[%q] = nil, want children", parentID)
		}
		if (bulk.Active == nil) != (perParent.Active == nil) ||
			(bulk.Active != nil && bulk.Active.ID != perParent.Active.ID) {
			t.Fatalf("AllRelationshipChildren()[%q].Active = %#v, want %#v", parentID, bulk.Active, perParent.Active)
		}
		bulkIDs := make([]string, len(bulk.Closed))
		for i, child := range bulk.Closed {
			bulkIDs[i] = child.ID
		}
		wantIDs := make([]string, len(perParent.Closed))
		for i, child := range perParent.Closed {
			wantIDs[i] = child.ID
		}
		if !slices.Equal(bulkIDs, wantIDs) {
			t.Fatalf("AllRelationshipChildren()[%q].Closed IDs = %v, want %v", parentID, bulkIDs, wantIDs)
		}
	}
	if _, ok := all["childless"]; ok {
		t.Fatal(`AllRelationshipChildren()["childless"] present, want absent`)
	}
}

func TestStoreAllRelationshipChildrenFailsClosedOnInvalidRecords(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs isolate persisted relationship state.

	store := NewStore(t.TempDir())
	if err := store.Save(&Feature{
		ID:     "bad-child",
		Parent: &ChildRelationship{ParentID: "parent", Kind: ChildKindRefactor, CloseOutcome: "merged"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := store.AllRelationshipChildren(); err == nil || !strings.Contains(err.Error(), "bad-child") {
		t.Fatalf("AllRelationshipChildren() error = %v, want fail-closed error naming bad-child", err)
	}
}

func TestStoreRelationshipChildrenFailsClosedOnInvalidRecords(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs isolate persisted relationship state.

	base := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		children []*Feature
		corrupt  bool
		wantID   string
	}{
		{
			name: "closed without timestamp",
			children: []*Feature{{
				ID:     "missing-close-time",
				Parent: &ChildRelationship{ParentID: "parent", Kind: ChildKindRefactor, CloseOutcome: ChildCloseOutcomeCompleted},
			}},
			wantID: "missing-close-time",
		},
		{
			name: "timestamp without outcome",
			children: []*Feature{{
				ID:     "missing-outcome",
				Parent: &ChildRelationship{ParentID: "parent", Kind: ChildKindRefactor, ClosedAt: &base},
			}},
			wantID: "missing-outcome",
		},
		{
			name: "unknown outcome",
			children: []*Feature{{
				ID:     "unknown-outcome",
				Parent: &ChildRelationship{ParentID: "parent", Kind: ChildKindRefactor, CloseOutcome: "merged", ClosedAt: &base},
			}},
			wantID: "unknown-outcome",
		},
		{
			name: "multiple active children",
			children: []*Feature{
				{ID: "active-a", Parent: &ChildRelationship{ParentID: "parent", Kind: ChildKindRefactor}},
				{ID: "active-b", Parent: &ChildRelationship{ParentID: "parent", Kind: ChildKindRefactor}},
			},
			wantID: "active-b",
		},
		{
			name:    "unreadable candidate",
			corrupt: true,
			wantID:  "corrupt-child",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// parallel-candidate: each case owns an independent temp-backed store.

			store := NewStore(t.TempDir())
			for _, child := range tt.children {
				child.SchemaVersion = SchemaVersionCurrent
				if err := store.Save(child); err != nil {
					t.Fatalf("Save(%q): %v", child.ID, err)
				}
			}
			if tt.corrupt {
				dir := filepath.Join(store.BaseDir, tt.wantID)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("MkdirAll(%q): %v", dir, err)
				}
				if err := os.WriteFile(filepath.Join(dir, "feature.yaml"), []byte("[unterminated"), 0o644); err != nil {
					t.Fatalf("WriteFile(%q): %v", dir, err)
				}
			}

			_, err := store.RelationshipChildren("parent")
			if err == nil {
				t.Fatal("RelationshipChildren(\"parent\") error = nil, want fail-closed error")
			}
			if !strings.Contains(err.Error(), tt.wantID) {
				t.Fatalf("RelationshipChildren(\"parent\") error = %q, want child ID %q", err, tt.wantID)
			}
		})
	}
}

func TestStoreCloseChildIsIdempotentAndPreservesInspectionState(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs isolate persisted relationship state.

	store := NewStore(t.TempDir())
	created := time.Date(2026, time.July, 28, 8, 0, 0, 0, time.UTC)
	firstClose := created.Add(2 * time.Hour)
	retryClose := firstClose.Add(time.Hour)
	child := &Feature{
		ID:            "child-close",
		SchemaVersion: SchemaVersionCurrent,
		Name:          "inspectable child",
		Created:       created,
		Status:        StatusFailed,
		LastError:     "integration needs attention",
		FailureType:   FailureInfrastructure,
		Pipeline:      PipelineMedium,
		Artifacts:     map[string]string{"plan": "phase-01/plan.md"},
		PhaseCosts:    map[string]float64{"implement": 12.5},
		Repos:         []FeatureRepo{{Name: "repo", WorktreePath: "/tmp/child"}},
		Parent: &ChildRelationship{
			ParentID: "parent",
			Kind:     ChildKindRefactor,
			Transaction: &TransactionJournal{
				Phase: TransactionPhaseAttention,
				Entries: []RepoTransactionEntry{{
					Repo:           "repo",
					CleanupWarning: "worktree busy",
				}},
			},
		},
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("Save(%q): %v", child.ID, err)
	}

	if err := store.CloseChild(child.ID, ChildCloseOutcomeCompleted, firstClose); err != nil {
		t.Fatalf("CloseChild(first): %v", err)
	}
	if err := store.CloseChild(child.ID, ChildCloseOutcomeCompleted, retryClose); err != nil {
		t.Fatalf("CloseChild(retry): %v", err)
	}
	if err := store.CloseChild(child.ID, ChildCloseOutcomeDiscarded, retryClose); !errors.Is(err, ErrChildRelationshipClosed) {
		t.Fatalf("CloseChild(different outcome) error = %v, want ErrChildRelationshipClosed", err)
	}

	got, err := store.Load(child.ID)
	if err != nil {
		t.Fatalf("Load(%q): %v", child.ID, err)
	}
	if got.Parent.CloseOutcome != ChildCloseOutcomeCompleted {
		t.Fatalf("CloseOutcome = %q, want %q", got.Parent.CloseOutcome, ChildCloseOutcomeCompleted)
	}
	if got.Parent.ClosedAt == nil || !got.Parent.ClosedAt.Equal(firstClose) {
		t.Fatalf("ClosedAt = %v, want original %v", got.Parent.ClosedAt, firstClose)
	}
	if got.LastError != child.LastError || got.FailureType != child.FailureType {
		t.Fatalf("failure context = (%q, %q), want (%q, %q)", got.LastError, got.FailureType, child.LastError, child.FailureType)
	}
	if got.Artifacts["plan"] != child.Artifacts["plan"] || got.TotalCost() != child.TotalCost() {
		t.Fatalf("inspection state artifacts/cost = (%v, %v), want (%v, %v)", got.Artifacts, got.TotalCost(), child.Artifacts, child.TotalCost())
	}
	if got.Parent.Transaction == nil || got.Parent.Transaction.Entries[0].CleanupWarning != "worktree busy" {
		t.Fatalf("integration diagnostics = %#v, want cleanup warning retained", got.Parent.Transaction)
	}
}

func TestStoreSetClosedChildDiffSummary(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs isolate persisted relationship state.

	store := NewStore(t.TempDir())
	closedAt := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	summary := "Repository: repo\ndiff --git a/auth.go b/auth.go\n+session rotation"

	child := &Feature{
		ID: "child-diff", Name: "diff child", Status: StatusReviewPassed, SchemaVersion: SchemaVersionCurrent,
		Repos: []FeatureRepo{{Name: "repo"}},
		Parent: &ChildRelationship{
			ParentID: "parent", Kind: ChildKindRefactor,
			CloseOutcome: ChildCloseOutcomeCompleted, ClosedAt: &closedAt,
		},
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("Save(%q): %v", child.ID, err)
	}
	if err := store.SetClosedChildDiffSummary(child.ID, summary); err != nil {
		t.Fatalf("SetClosedChildDiffSummary(closed): %v", err)
	}
	got, err := store.Load(child.ID)
	if err != nil {
		t.Fatalf("Load(%q): %v", child.ID, err)
	}
	if got.Parent.DiffSummary != summary {
		t.Fatalf("DiffSummary = %q, want preserved summary", got.Parent.DiffSummary)
	}

	// Empty summaries and repeat writes are no-ops.
	if err := store.SetClosedChildDiffSummary(child.ID, ""); err != nil {
		t.Fatalf("SetClosedChildDiffSummary(empty): %v", err)
	}
	got, _ = store.Load(child.ID)
	if got.Parent.DiffSummary != summary {
		t.Fatalf("DiffSummary after empty write = %q, want unchanged", got.Parent.DiffSummary)
	}

	// An open child and a non-child never receive a summary.
	openChild := &Feature{
		ID: "child-open", Name: "open child", Status: StatusImplementing, SchemaVersion: SchemaVersionCurrent,
		Repos:  []FeatureRepo{{Name: "repo"}},
		Parent: &ChildRelationship{ParentID: "parent", Kind: ChildKindRefactor},
	}
	if err := store.Save(openChild); err != nil {
		t.Fatalf("Save(%q): %v", openChild.ID, err)
	}
	if err := store.SetClosedChildDiffSummary(openChild.ID, summary); err != nil {
		t.Fatalf("SetClosedChildDiffSummary(open child): %v", err)
	}
	got, _ = store.Load(openChild.ID)
	if got.Parent.DiffSummary != "" {
		t.Fatalf("open child DiffSummary = %q, want empty", got.Parent.DiffSummary)
	}

	plain := &Feature{ID: "plain", Name: "plain", Status: StatusImplementing, SchemaVersion: SchemaVersionCurrent}
	if err := store.Save(plain); err != nil {
		t.Fatalf("Save(%q): %v", plain.ID, err)
	}
	if err := store.SetClosedChildDiffSummary(plain.ID, summary); err != nil {
		t.Fatalf("SetClosedChildDiffSummary(non-child): %v", err)
	}
	got, _ = store.Load(plain.ID)
	if got.Parent != nil {
		t.Fatalf("non-child Parent = %#v, want nil", got.Parent)
	}
}

// TestStoreSetClosedChildDiffSummaryBoundsOversizedDiff proves the store
// never persists a diff summary beyond DiffSummaryBudget, even when handed a
// raw multi-megabyte diff.
func TestStoreSetClosedChildDiffSummaryBoundsOversizedDiff(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	closedAt := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	child := &Feature{
		ID: "child-huge-diff", Name: "huge diff child", Status: StatusReviewPassed, SchemaVersion: SchemaVersionCurrent,
		Repos: []FeatureRepo{{Name: "repo"}},
		Parent: &ChildRelationship{
			ParentID: "parent", Kind: ChildKindRefactor,
			CloseOutcome: ChildCloseOutcomeCompleted, ClosedAt: &closedAt,
		},
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("Save(%q): %v", child.ID, err)
	}

	huge := strings.Repeat("+a raw diff line that repeats forever\n", (DiffSummaryBudget*4)/38)
	if err := store.SetClosedChildDiffSummary(child.ID, huge); err != nil {
		t.Fatalf("SetClosedChildDiffSummary(huge): %v", err)
	}
	got, err := store.Load(child.ID)
	if err != nil {
		t.Fatalf("Load(%q): %v", child.ID, err)
	}
	if len(got.Parent.DiffSummary) > DiffSummaryBudget {
		t.Fatalf("persisted DiffSummary length = %d, want <= %d", len(got.Parent.DiffSummary), DiffSummaryBudget)
	}
	if !strings.HasSuffix(got.Parent.DiffSummary, " bytes omitted]") {
		t.Fatalf("persisted DiffSummary missing truncation marker, tail = %q", got.Parent.DiffSummary[len(got.Parent.DiffSummary)-120:])
	}
}

func TestStoreModifyGuardedRejectsClosedChildBeforeCallbacks(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs isolate persisted relationship state.

	store := NewStore(t.TempDir())
	closedAt := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	child := &Feature{
		ID:            "closed-child",
		SchemaVersion: SchemaVersionCurrent,
		Name:          "immutable",
		Status:        StatusDone,
		Repos:         []FeatureRepo{{Name: "repo", WorktreePath: "/tmp/original"}},
		Parent: &ChildRelationship{
			ParentID:     "parent",
			Kind:         ChildKindRefactor,
			CloseOutcome: ChildCloseOutcomeCompleted,
			ClosedAt:     &closedAt,
		},
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("Save(%q): %v", child.ID, err)
	}

	guardCalled := false
	mutationCalled := false
	err := store.ModifyGuarded(
		child.ID,
		func(f *Feature, activeChild *Feature) error {
			guardCalled = true
			f.Name = "changed by guard"
			return nil
		},
		func(f *Feature) error {
			mutationCalled = true
			f.Status = StatusCreated
			f.Repos[0].WorktreePath = "/tmp/changed"
			f.PermissionsQueue = append(f.PermissionsQueue, PermissionRequest{Tool: "changed"})
			return nil
		},
	)
	if !errors.Is(err, ErrChildRelationshipClosed) {
		t.Fatalf("ModifyGuarded(%q) error = %v, want ErrChildRelationshipClosed", child.ID, err)
	}
	if guardCalled || mutationCalled {
		t.Fatalf("callbacks called = (guard %v, mutation %v), want both false", guardCalled, mutationCalled)
	}
	got, loadErr := store.Load(child.ID)
	if loadErr != nil {
		t.Fatalf("Load(%q): %v", child.ID, loadErr)
	}
	if got.Name != child.Name || got.Status != child.Status || got.Repos[0].WorktreePath != child.Repos[0].WorktreePath || len(got.PermissionsQueue) != 0 {
		t.Fatalf("closed child mutated: got %#v, want original %#v", got, child)
	}
}

func timePointer(t time.Time) *time.Time {
	return &t
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

func TestStoreLoadRejectsNonCurrentSchemaVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewStore(dir)

	const featureID = "old-schema-001"
	featureDir := filepath.Join(dir, featureID)
	runDir := filepath.Join(featureDir, "runs", "run-001")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	oldStatus := "Brain" + "stormReady"
	featureYAML := fmt.Sprintf(`id: %s
name: Old Schema Feature
slug: old-schema-feature
description: old schema
created: 2026-01-01T00:00:00Z
status: %s
current_phase: 0
repos:
  - name: repo-a
    path: /tmp/a
models: {}
exit_criteria: ""
inquireness: medium
active_run: 1
run_count: 1
schema_version: %d
`, featureID, oldStatus, SchemaVersionCurrent-1)
	if err := os.WriteFile(filepath.Join(featureDir, "feature.yaml"), []byte(featureYAML), 0o644); err != nil {
		t.Fatalf("write feature.yaml: %v", err)
	}

	runYAML := `run_number: 1
artifacts: {}
`
	if err := os.WriteFile(filepath.Join(runDir, "run.yaml"), []byte(runYAML), 0o644); err != nil {
		t.Fatalf("write run.yaml: %v", err)
	}

	_, err := store.Load(featureID)
	want := fmt.Sprintf("schema version %d, expected %d", SchemaVersionCurrent-1, SchemaVersionCurrent)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Load() error = %v, want containing %q", err, want)
	}
}

func TestStoreListRunsAcceptsOnlyCanonicalDirectoryNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewStore(dir)
	runsDir := filepath.Join(dir, "feature-001", "runs")
	for _, name := range []string{"run-001", "run-999", "run-1000", "run-1", "run-01", "run-01000", "run-abc"} {
		if err := os.MkdirAll(filepath.Join(runsDir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}

	got, err := store.ListRuns("feature-001")
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	want := []int{1, 999, 1000}
	if !slices.Equal(got, want) {
		t.Errorf("ListRuns() = %v, want %v", got, want)
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

// TestStoreLoadDropsLegacyRefactorKeys verifies that a legacy run.yaml
// containing refactor_prompt / refactor_count keys (from the removed Refactor
// post-publish cycle) loads cleanly without activating any cycle, and that a
// round-trip save does not re-emit the dropped keys.
func TestStoreLoadDropsLegacyRefactorKeys(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	const featureID = "refactor-legacy-001"
	featureDir := filepath.Join(dir, featureID)
	runDir := filepath.Join(featureDir, "runs", "run-001")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	featureYAML := fmt.Sprintf(`id: %s
name: Legacy Refactor
slug: legacy-refactor
description: pre-removal state with refactor cycle keys
created: 2026-01-01T00:00:00Z
status: Published
current_phase: 6
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
	runYAML := `run_number: 1
started_at: 2026-01-01T00:00:00Z
rebase_count: 0
refactor_count: 2
active_cycle_type: refactor
active_cycle:
  type: refactor
  status: running
  count: 2
refactor_prompt: extract shared config
repo_cycles:
  repo-a:
    type: refactor
    status: running
    count: 1
  repo-b:
    type: rebase
    status: failed
    count: 1
    last_error: boom
artifacts: {}
`
	if err := os.WriteFile(filepath.Join(runDir, "run.yaml"), []byte(runYAML), 0o644); err != nil {
		t.Fatalf("write run.yaml: %v", err)
	}

	loaded, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("Load() error = %v; want nil (legacy refactor keys must be ignored)", err)
	}
	// Legacy Refactor cycle state must be dropped on load: no active cycle
	// type, no feature-level active cycle, no Refactor repo-cycle entries.
	if got := string(loaded.ActiveCycleType()); got != "" {
		t.Errorf("ActiveCycleType() = %q, want empty for dropped refactor cycle", got)
	}
	if loaded.ActiveCycle != nil {
		t.Errorf("ActiveCycle = %+v, want nil for dropped refactor cycle", loaded.ActiveCycle)
	}
	if _, ok := loaded.RepoCycles["repo-a"]; ok {
		t.Errorf("RepoCycles[repo-a] = %+v, want absent (refactor entry must be dropped)", loaded.RepoCycles["repo-a"])
	}
	if rc := loaded.RepoCycles["repo-b"]; rc == nil || rc.Type != CycleRebase || rc.Status != RepoCycleFailed {
		t.Errorf("RepoCycles[repo-b] = %+v, want surviving failed rebase entry preserved", rc)
	}
	if loaded.HasActiveRepoCycles() {
		t.Error("HasActiveRepoCycles() = true, want false for dropped refactor cycle")
	}
	// No artifact-cycle prefix may engage for dropped cycle state.
	if got := loaded.CyclePrefix(); got != "" {
		t.Errorf("CyclePrefix() = %q, want empty for dropped refactor cycle", got)
	}
	loadedRun, err := store.LoadRun(featureID, 1)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if got := loadedRun.CyclePrefix(); got != "" {
		t.Errorf("Run.CyclePrefix() = %q, want empty for dropped refactor cycle", got)
	}

	// Round-trip save and re-read the raw run.yaml: dropped keys and cycle
	// state must not be re-emitted.
	if err := store.Save(loaded); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	saved, err := os.ReadFile(filepath.Join(runDir, "run.yaml"))
	if err != nil {
		t.Fatalf("ReadFile after Save: %v", err)
	}
	savedStr := string(saved)
	if strings.Contains(savedStr, "refactor") {
		t.Errorf("saved run.yaml still contains 'refactor'; should not be re-emitted after round-trip:\n%s", savedStr)
	}
	if strings.Contains(savedStr, "active_cycle") {
		t.Errorf("saved run.yaml still contains 'active_cycle'; dropped cycle state should not be re-emitted:\n%s", savedStr)
	}
}

// TestStoreLoadDropsLegacyReviewCommentsState verifies that a legacy run.yaml
// containing review-comments cycle state and its retired scalar fields loads
// cleanly, preserves an unrelated rebase entry, and drops the removed state on
// the next save.
func TestStoreLoadDropsLegacyReviewCommentsState(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	dir := t.TempDir()
	store := NewStore(dir)

	const featureID = "review-comments-legacy-001"
	featureDir := filepath.Join(dir, featureID)
	runDir := filepath.Join(featureDir, "runs", "run-001")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	featureYAML := fmt.Sprintf(`id: %s
name: Legacy Review Comments
slug: legacy-review-comments
description: pre-removal state with review-comments cycle keys
created: 2026-01-01T00:00:00Z
status: Published
current_phase: 6
repos:
  - name: repo-active
    path: /tmp/active
    worktree_path: ""
    branch: ""
  - name: repo-history
    path: /tmp/history
    worktree_path: ""
    branch: ""
  - name: repo-rebase
    path: /tmp/rebase
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
	runYAML := `run_number: 1
started_at: 2026-01-01T00:00:00Z
rebase_count: 2
review_comments_count: 4
addressing_reviews: true
active_cycle_type: review-comments
active_cycle:
  type: review-comments
  status: running
  count: 4
  plan_path: /tmp/review-comments-plan.md
repo_cycles:
  repo-active:
    type: review-comments
    status: running
    count: 4
    plan_path: /tmp/repo-active-plan.md
    iteration: 3
    pending_need_user_input_path: /tmp/repo-active-gate.yaml
  repo-history:
    type: review-comments
    status: failed
    count: 2
    plan_path: /tmp/repo-history-plan.md
    iteration: 1
    pending_need_user_input_path: /tmp/repo-history-gate.yaml
    last_error: boom
  repo-rebase:
    type: rebase
    status: failed
    count: 2
    last_error: conflict
artifacts: {}
`
	if err := os.WriteFile(filepath.Join(runDir, "run.yaml"), []byte(runYAML), 0o644); err != nil {
		t.Fatalf("write run.yaml: %v", err)
	}

	loaded, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("Load() error = %v; want nil (legacy review-comments keys must be ignored)", err)
	}
	if got := string(loaded.ActiveCycleType()); got != "" {
		t.Errorf("ActiveCycleType() = %q, want empty for dropped review-comments cycle", got)
	}
	if loaded.ActiveCycle != nil {
		t.Errorf("ActiveCycle = %+v, want nil for dropped review-comments cycle", loaded.ActiveCycle)
	}
	for _, repoName := range []string{"repo-active", "repo-history"} {
		if _, ok := loaded.RepoCycles[repoName]; ok {
			t.Errorf("RepoCycles[%s] = %+v, want absent (review-comments entry must be dropped)", repoName, loaded.RepoCycles[repoName])
		}
	}
	if rc := loaded.RepoCycles["repo-rebase"]; rc == nil || rc.Type != CycleRebase || rc.Status != RepoCycleFailed || rc.Count != 2 {
		t.Errorf("RepoCycles[repo-rebase] = %+v, want surviving failed rebase entry preserved", rc)
	}
	if loaded.HasActiveRepoCycles() {
		t.Error("HasActiveRepoCycles() = true, want false after active review-comments state is dropped")
	}
	if got := loaded.CyclePrefix(); got != "" {
		t.Errorf("CyclePrefix() = %q, want empty for dropped review-comments cycle", got)
	}
	loadedRun, err := store.LoadRun(featureID, 1)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if got := loadedRun.CyclePrefix(); got != "" {
		t.Errorf("Run.CyclePrefix() = %q, want empty for dropped review-comments cycle", got)
	}

	if err := store.Save(loaded); err != nil {
		t.Fatalf("Save() error = %v; want nil", err)
	}
	saved, err := os.ReadFile(filepath.Join(runDir, "run.yaml"))
	if err != nil {
		t.Fatalf("ReadFile after Save: %v", err)
	}
	savedStr := string(saved)
	for _, droppedKey := range []string{
		"review-comments",
		"review_comments_count",
		"addressing_reviews",
		"plan_path",
		"iteration",
		"pending_need_user_input_path",
	} {
		if strings.Contains(savedStr, droppedKey) {
			t.Errorf("saved run.yaml still contains %q; dropped legacy state should not be re-emitted:\n%s", droppedKey, savedStr)
		}
	}
	if !strings.Contains(savedStr, "type: rebase") || !strings.Contains(savedStr, "count: 2") {
		t.Errorf("saved run.yaml lost surviving rebase cycle entry:\n%s", savedStr)
	}
}

func TestStoreRelationshipScansSkipLegacyRecords(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs isolate persisted relationship state.

	store := NewStore(t.TempDir())
	base := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	for _, f := range []*Feature{
		{ID: "parent-a", Name: "parent a", SchemaVersion: SchemaVersionCurrent},
		{
			ID:            "a-closed",
			Created:       base,
			SchemaVersion: SchemaVersionCurrent,
			Parent: &ChildRelationship{
				ParentID:     "parent-a",
				Kind:         ChildKindRefactor,
				CloseOutcome: ChildCloseOutcomeCompleted,
				ClosedAt:     timePointer(base.Add(time.Hour)),
			},
		},
	} {
		if err := store.Save(f); err != nil {
			t.Fatalf("save %s: %v", f.ID, err)
		}
	}
	// An explicitly legacy-versioned record must degrade to a skip: it
	// predates child relationships, and one stale record must not take down
	// every relationship read. (Corrupt records still fail closed.)
	legacyDir := filepath.Join(store.BaseDir, "legacy-record")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	legacy := "id: legacy-record\nname: legacy\nschema_version: 5\n"
	if err := os.WriteFile(filepath.Join(legacyDir, "feature.yaml"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	all, err := store.AllRelationshipChildren()
	if err != nil {
		t.Fatalf("AllRelationshipChildren() error = %v, want legacy record skipped", err)
	}
	if all["parent-a"] == nil || len(all["parent-a"].Closed) != 1 {
		t.Fatalf("AllRelationshipChildren()[parent-a] = %#v, want one closed child", all["parent-a"])
	}

	perParent, err := store.RelationshipChildren("parent-a")
	if err != nil {
		t.Fatalf("RelationshipChildren() error = %v, want legacy record skipped", err)
	}
	if len(perParent.Closed) != 1 {
		t.Fatalf("RelationshipChildren().Closed = %d entries, want 1", len(perParent.Closed))
	}
}
