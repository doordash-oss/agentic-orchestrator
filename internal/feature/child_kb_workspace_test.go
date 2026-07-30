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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestChildKBWorkspaceDir(t *testing.T) {
	got := ChildKBWorkspaceDir("/home/user/.agentic-workflow/features", "child-123", "myrepo")
	want := "/home/user/.agentic-workflow/child-kb/child-123/myrepo"
	if got != want {
		t.Errorf("ChildKBWorkspaceDir = %q, want %q", got, want)
	}
}

func TestSaveLoadWorkspaceState(t *testing.T) {
	dir := t.TempDir()
	state := &ChildKBWorkspaceState{
		Source:          WorkspaceSourceOverlay,
		CanonicalCommit: "abc123",
		ParentHEAD:      "def456",
		SeedBaseCommit:  "ghi789",
		AnalyzedCommit:  "ghi789",
		SeededAt:        time.Now().UTC().Truncate(time.Second),
		LastUpdated:     time.Now().UTC().Truncate(time.Second),
	}
	if err := SaveWorkspaceState(dir, state); err != nil {
		t.Fatalf("SaveWorkspaceState: %v", err)
	}
	loaded, err := LoadWorkspaceState(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceState: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded state is nil")
	}
	if loaded.Source != state.Source || loaded.CanonicalCommit != state.CanonicalCommit ||
		loaded.ParentHEAD != state.ParentHEAD || loaded.SeedBaseCommit != state.SeedBaseCommit ||
		loaded.AnalyzedCommit != state.AnalyzedCommit {
		t.Errorf("loaded state mismatch: got %+v, want %+v", loaded, state)
	}
}

func TestLoadWorkspaceStateMissing(t *testing.T) {
	dir := t.TempDir()
	state, err := LoadWorkspaceState(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceState on missing dir: %v", err)
	}
	if state != nil {
		t.Fatal("expected nil state for missing file")
	}
}

func TestSaveLoadOverlayProvenance(t *testing.T) {
	dir := t.TempDir()
	prov := &OverlayProvenance{
		CanonicalCommit: "abc123",
		ParentHEAD:      "def456",
		Generation:      2,
		CreatedAt:       time.Now().UTC().Truncate(time.Second),
	}
	if err := SaveOverlayProvenance(dir, prov); err != nil {
		t.Fatalf("SaveOverlayProvenance: %v", err)
	}
	loaded, err := LoadOverlayProvenance(dir)
	if err != nil {
		t.Fatalf("LoadOverlayProvenance: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded provenance is nil")
	}
	if loaded.CanonicalCommit != prov.CanonicalCommit || loaded.ParentHEAD != prov.ParentHEAD ||
		loaded.Generation != prov.Generation {
		t.Errorf("loaded provenance mismatch: got %+v, want %+v", loaded, prov)
	}
}

func TestAcquireReleaseOverlayLock(t *testing.T) {
	dir := t.TempDir()

	acquired, err := AcquireOverlayLock(dir, "child-1")
	if err != nil {
		t.Fatalf("AcquireOverlayLock: %v", err)
	}
	if !acquired {
		t.Fatal("expected to acquire lock")
	}

	// Same child can re-acquire (reentrant).
	acquired, err = AcquireOverlayLock(dir, "child-1")
	if err != nil {
		t.Fatalf("reentrant AcquireOverlayLock: %v", err)
	}
	if !acquired {
		t.Fatal("expected reentrant acquire to succeed")
	}

	// Different child cannot acquire.
	acquired, err = AcquireOverlayLock(dir, "child-2")
	if err != nil {
		t.Fatalf("second AcquireOverlayLock: %v", err)
	}
	if acquired {
		t.Fatal("expected second child to fail acquiring lock")
	}

	// Read owner.
	owner := ReadOverlayLockOwner(dir)
	if owner != "child-1" {
		t.Errorf("ReadOverlayLockOwner = %q, want child-1", owner)
	}

	// Different child cannot release.
	if err := ReleaseOverlayLock(dir, "child-2"); err != nil {
		t.Fatalf("ReleaseOverlayLock by wrong child: %v", err)
	}
	if _, err := os.Stat(OverlayLockPath(dir)); err != nil {
		t.Fatal("lock file should still exist after wrong-child release")
	}

	// Owner can release.
	if err := ReleaseOverlayLock(dir, "child-1"); err != nil {
		t.Fatalf("ReleaseOverlayLock: %v", err)
	}
	if _, err := os.Stat(OverlayLockPath(dir)); !os.IsNotExist(err) {
		t.Fatal("lock file should be removed after owner release")
	}
}

func TestIsOverlayLockStale(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a lock with an old timestamp.
	lockPath := OverlayLockPath(dir)
	data := []byte(`{"child_id":"child-1","timestamp":"2020-01-01T00:00:00Z"}`)
	if err := os.WriteFile(lockPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsOverlayLockStale(dir, 5*time.Minute) {
		t.Fatal("expected lock to be stale")
	}

	// Write a lock with a fresh timestamp.
	info := OverlayLockInfo{ChildID: "child-1", Timestamp: time.Now()}
	fresh, _ := json.Marshal(info)
	if err := os.WriteFile(lockPath, fresh, 0o644); err != nil {
		t.Fatal(err)
	}
	if IsOverlayLockStale(dir, 5*time.Minute) {
		t.Fatal("expected lock to be fresh")
	}

	// Corrupt lock is stale.
	if err := os.WriteFile(lockPath, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsOverlayLockStale(dir, 5*time.Minute) {
		t.Fatal("expected corrupt lock to be stale")
	}
}

func TestPromotionJournalAllPromoted(t *testing.T) {
	j := &PromotionJournal{
		Phase: PromotionPhasePromoting,
		Entries: []PromotionEntry{
			{Repo: "a", Done: true},
			{Repo: "b", Done: true},
		},
	}
	if !j.AllPromoted() {
		t.Error("expected all promoted")
	}

	j.Entries[1].Done = false
	if j.AllPromoted() {
		t.Error("expected not all promoted")
	}
}

func TestPromotionJournalEntryByRepo(t *testing.T) {
	j := &PromotionJournal{
		Entries: []PromotionEntry{
			{Repo: "a", MergeHEAD: "sha1"},
			{Repo: "b", MergeHEAD: "sha2"},
		},
	}
	entry := j.EntryByRepo("b")
	if entry == nil || entry.MergeHEAD != "sha2" {
		t.Errorf("EntryByRepo(b) = %+v, want sha2", entry)
	}
	if j.EntryByRepo("c") != nil {
		t.Error("EntryByRepo(c) should be nil")
	}
}

func TestStoreSaveLoadPromotion(t *testing.T) {
	store := NewStore(t.TempDir())
	journal := &PromotionJournal{
		ChildID:   "child-1",
		ParentID:  "parent-1",
		Phase:     PromotionPhasePending,
		Entries:   []PromotionEntry{{Repo: "repo-1", ChildCommit: "abc", MergeHEAD: "def"}},
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := store.SavePromotion("child-1", journal); err != nil {
		t.Fatalf("SavePromotion: %v", err)
	}
	loaded, err := store.LoadPromotion("child-1")
	if err != nil {
		t.Fatalf("LoadPromotion: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded journal is nil")
	}
	if loaded.ChildID != journal.ChildID || loaded.Phase != journal.Phase ||
		len(loaded.Entries) != 1 || loaded.Entries[0].Repo != "repo-1" {
		t.Errorf("loaded journal mismatch: got %+v", loaded)
	}
}

func TestStoreLoadPromotionMissing(t *testing.T) {
	store := NewStore(t.TempDir())
	loaded, err := store.LoadPromotion("nonexistent")
	if err != nil {
		t.Fatalf("LoadPromotion on missing: %v", err)
	}
	if loaded != nil {
		t.Fatal("expected nil journal for missing file")
	}
}

func TestStoreDeletePromotion(t *testing.T) {
	store := NewStore(t.TempDir())
	journal := &PromotionJournal{
		ChildID:  "child-1",
		ParentID: "parent-1",
		Phase:    PromotionPhasePromoted,
	}
	if err := store.SavePromotion("child-1", journal); err != nil {
		t.Fatalf("SavePromotion: %v", err)
	}
	if err := store.DeletePromotion("child-1"); err != nil {
		t.Fatalf("DeletePromotion: %v", err)
	}
	// Delete again is a no-op (file already gone).
	if err := store.DeletePromotion("child-1"); err == nil {
		// os.Remove returns nil for missing files on some platforms
	}
}

func TestParentOverlayPathConsistency(t *testing.T) {
	stateDir := filepath.Join("home", "user", ".agentic-workflow", "features")
	got := ParentOverlayPath(stateDir, "parent-1", "repo-1")
	want := filepath.Join("home", "user", ".agentic-workflow", "overlays", "parent-1", "repo-1")
	if got != want {
		t.Errorf("ParentOverlayPath = %q, want %q", got, want)
	}
}

func TestChildKBWorkspaceDirDisjointFromCanonical(t *testing.T) {
	stateDir := "/base/features"
	workspaceDir := ChildKBWorkspaceDir(stateDir, "child-1", "repo-1")
	canonDir := filepath.Join(filepath.Dir(stateDir), "knowledge-base", "repo-1")
	overlayDir := ParentOverlayPath(stateDir, "parent-1", "repo-1")
	if workspaceDir == canonDir {
		t.Error("workspace dir should be disjoint from canonical KB")
	}
	if workspaceDir == overlayDir {
		t.Error("workspace dir should be disjoint from parent overlay")
	}
}
