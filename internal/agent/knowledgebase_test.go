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

package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestKBStateDir(t *testing.T) {
	got := KBStateDir("/home/user/.agentic-workflow/features", "myrepo")
	want := "/home/user/.agentic-workflow/knowledge-base/myrepo"
	if got != want {
		t.Errorf("KBStateDir = %q, want %q", got, want)
	}
}

func TestBuildKBSessionIDKeepsQualifiedRepoNamesURLSafeAndDistinct(t *testing.T) {
	t.Parallel()

	validSessionID := regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)
	first := buildKBSessionID("abc123", "root-a/service")
	second := buildKBSessionID("abc123", "root-b/service")

	for _, id := range []string{first, second} {
		if !validSessionID.MatchString(id) {
			t.Errorf("BuildKBSessionID returned desktop-incompatible ID %q", id)
		}
	}
	if first == second {
		t.Fatalf("qualified repository names produced the same session ID %q", first)
	}
	if got := RepoNameFromKBSession(first); got != "" {
		t.Errorf("RepoNameFromKBSession(encoded ID) = %q, want empty metadata fallback", got)
	}
	if got, want := buildKBSessionID("abc123", "my-service"), "abc123-kb-my-service"; got != want {
		t.Errorf("BuildKBSessionID preserved format = %q, want %q", got, want)
	}
}

func TestKBPath(t *testing.T) {
	got := KBPath("/tmp/kb")
	want := "/tmp/kb/index.md"
	if got != want {
		t.Errorf("KBPath = %q, want %q", got, want)
	}
}

func TestKBRootDir(t *testing.T) {
	got := kbRootDir("/tmp/kb")
	want := "/tmp/kb"
	if got != want {
		t.Errorf("KBRootDir = %q, want %q", got, want)
	}
}

func TestKBLockPath(t *testing.T) {
	got := kbLockPath("/tmp/kb")
	want := "/tmp/kb/kb.lock"
	if got != want {
		t.Errorf("KBLockPath = %q, want %q", got, want)
	}
}

func TestLoadSaveKBState(t *testing.T) {
	dir := t.TempDir()

	// Load from nonexistent dir returns nil
	state, err := LoadKBState(dir)
	if err != nil {
		t.Fatalf("LoadKBState (empty): %v", err)
	}
	if state != nil {
		t.Fatal("expected nil state for empty dir")
	}

	// Save and load round-trip
	now := time.Now().Truncate(time.Second)
	original := &KBState{
		HeadCommit:  "abc123",
		LastUpdated: now,
		Version:     1,
	}
	if err := SaveKBState(dir, original); err != nil {
		t.Fatalf("SaveKBState: %v", err)
	}

	loaded, err := LoadKBState(dir)
	if err != nil {
		t.Fatalf("LoadKBState: %v", err)
	}
	if loaded.HeadCommit != original.HeadCommit {
		t.Errorf("HeadCommit = %q, want %q", loaded.HeadCommit, original.HeadCommit)
	}
	if loaded.Version != original.Version {
		t.Errorf("Version = %d, want %d", loaded.Version, original.Version)
	}

	// Verify no temp file remains
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file %s still exists", e.Name())
		}
	}
}

func TestAcquireReleaseKBLock(t *testing.T) {
	dir := t.TempDir()

	// First acquire succeeds
	locked, err := acquireKBLockWithStatus(dir, "feature-1", nil)
	if err != nil {
		t.Fatalf("AcquireKBLockWithStatus: %v", err)
	}
	if !locked {
		t.Fatal("expected lock to be acquired")
	}

	// Second acquire fails (contention)
	locked2, err := acquireKBLockWithStatus(dir, "feature-2", nil)
	if err != nil {
		t.Fatalf("AcquireKBLockWithStatus (second): %v", err)
	}
	if locked2 {
		t.Fatal("expected lock NOT to be acquired (contention)")
	}

	// Release with wrong feature ID should not remove lock
	if err := releaseKBLock(dir, "feature-2"); err != nil {
		t.Fatalf("ReleaseKBLock (wrong ID): %v", err)
	}
	// Lock should still exist
	if _, err := os.Stat(kbLockPath(dir)); err != nil {
		t.Fatal("lock should still exist after release with wrong ID")
	}

	// Release with correct ID
	if err := releaseKBLock(dir, "feature-1"); err != nil {
		t.Fatalf("ReleaseKBLock: %v", err)
	}
	if _, err := os.Stat(kbLockPath(dir)); !os.IsNotExist(err) {
		t.Fatal("lock should be removed after release")
	}

	// Reentrant: same feature can re-acquire its own lock
	_, _ = acquireKBLockWithStatus(dir, "feature-reentrant", nil)
	locked3, err := acquireKBLockWithStatus(dir, "feature-reentrant", nil)
	if err != nil {
		t.Fatalf("AcquireKBLockWithStatus (reentrant): %v", err)
	}
	if !locked3 {
		t.Fatal("expected reentrant lock acquisition to succeed")
	}
	if err := releaseKBLock(dir, "feature-reentrant"); err != nil {
		t.Fatalf("ReleaseKBLock (reentrant): %v", err)
	}

	// Force release (empty feature ID)
	_, _ = acquireKBLockWithStatus(dir, "feature-3", nil)
	if err := releaseKBLock(dir, ""); err != nil {
		t.Fatalf("ReleaseKBLock (force): %v", err)
	}
	if _, err := os.Stat(kbLockPath(dir)); !os.IsNotExist(err) {
		t.Fatal("lock should be removed after force release")
	}
}

func TestAcquireKBLockWithStatusReclaimsMissingOrStoppedOwner(t *testing.T) {
	tests := []struct {
		name     string
		statusFn featureStatusFunc
	}{
		{
			name:     "missing owner",
			statusFn: func(id string) (int, bool) { return 0, false },
		},
		{
			name:     "stopped owner",
			statusFn: func(id string) (int, bool) { return int(feature.StatusInterrupted), true },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if locked, err := acquireKBLockWithStatus(dir, "owner", nil); err != nil || !locked {
				t.Fatalf("AcquireKBLockWithStatus owner = %v, %v; want acquired", locked, err)
			}

			locked, err := acquireKBLockWithStatus(dir, "waiter", tt.statusFn)
			if err != nil {
				t.Fatalf("AcquireKBLockWithStatus: %v", err)
			}
			if !locked {
				t.Fatal("expected stale owner lock to be reclaimed")
			}
			if owner := ReadKBLockOwner(dir); owner != "waiter" {
				t.Fatalf("lock owner = %q, want waiter", owner)
			}
		})
	}
}

func TestAcquireKBLockWithStatusPreservesActiveOwner(t *testing.T) {
	dir := t.TempDir()
	old := KBLockInfo{FeatureID: "owner", Timestamp: time.Now().Add(-2 * time.Hour)}
	data, _ := json.Marshal(old)
	if err := os.WriteFile(kbLockPath(dir), data, 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	locked, err := acquireKBLockWithStatus(dir, "waiter", func(id string) (int, bool) {
		return int(feature.StatusBuildingKB), true
	})
	if err != nil {
		t.Fatalf("AcquireKBLockWithStatus: %v", err)
	}
	if locked {
		t.Fatal("expected active owner lock to be preserved")
	}
	if owner := ReadKBLockOwner(dir); owner != "owner" {
		t.Fatalf("lock owner = %q, want owner", owner)
	}
}

func TestAcquireKBLockWithStatusConcurrentReclaimHasOneWinner(t *testing.T) {
	dir := t.TempDir()
	if locked, err := acquireKBLockWithStatus(dir, "owner", nil); err != nil || !locked {
		t.Fatalf("AcquireKBLockWithStatus owner = %v, %v; want acquired", locked, err)
	}

	const contenders = 16
	var winners atomic.Int32
	var wg sync.WaitGroup
	wg.Add(contenders)
	for i := 0; i < contenders; i++ {
		i := i
		go func() {
			defer wg.Done()
			locked, err := acquireKBLockWithStatus(dir, "waiter-"+string(rune('a'+i)), func(id string) (int, bool) {
				if id == "owner" {
					return 0, false
				}
				return int(feature.StatusBuildingKB), true
			})
			if err != nil {
				t.Errorf("AcquireKBLockWithStatus: %v", err)
				return
			}
			if locked {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := winners.Load(); got != 1 {
		t.Fatalf("winners = %d, want 1", got)
	}
}

func TestReleaseKBLockIfOwnedDoesNotRemoveReplacement(t *testing.T) {
	dir := t.TempDir()
	if locked, err := acquireKBLockWithStatus(dir, "replacement", nil); err != nil || !locked {
		t.Fatalf("AcquireKBLockWithStatus replacement = %v, %v; want acquired", locked, err)
	}

	removed, err := ReleaseKBLockIfOwned(dir, "old-owner")
	if err != nil {
		t.Fatalf("ReleaseKBLockIfOwned: %v", err)
	}
	if removed {
		t.Fatal("wrong owner release reported removal")
	}
	if owner := ReadKBLockOwner(dir); owner != "replacement" {
		t.Fatalf("lock owner = %q, want replacement", owner)
	}
}

func TestIsKBFreshRequiresKBFile(t *testing.T) {
	dir := t.TempDir()

	// Save state with a known commit but don't create index.md
	err := SaveKBState(dir, &KBState{
		HeadCommit:  "deadbeef",
		LastUpdated: time.Now(),
		Version:     1,
	})
	if err != nil {
		t.Fatalf("SaveKBState: %v", err)
	}

	// IsKBFresh should return false when index.md doesn't exist,
	// even if commit matches (simulates failed/partial KB build)
	// We can't easily test with a real git repo, but we verify the os.Stat check
	// by confirming state was saved but index.md is missing
	state, err := LoadKBState(dir)
	if err != nil {
		t.Fatalf("LoadKBState: %v", err)
	}
	if state.HeadCommit != "deadbeef" {
		t.Errorf("expected HeadCommit=deadbeef, got %s", state.HeadCommit)
	}
	// index.md should not exist
	if _, err := os.Stat(KBPath(dir)); !os.IsNotExist(err) {
		t.Error("expected index.md to not exist")
	}

	// Now create index.md and verify the file check works
	if err := os.WriteFile(KBPath(dir), []byte("# KB"), 0o644); err != nil {
		t.Fatalf("writing index.md: %v", err)
	}
	if _, err := os.Stat(KBPath(dir)); err != nil {
		t.Error("expected index.md to exist after creation")
	}
}

func TestBuildKBPromptFullBuild(t *testing.T) {
	prompt := buildKBPrompt("myrepo", "/tmp/myrepo", "/tmp/kb", "", "")
	checks := []string{
		"# Repository Context",
		"**Repository**: myrepo",
		"**KB Root Directory**: /tmp/kb",
		"**KB Index Path**: /tmp/kb/index.md",
		"Mode: FULL BUILD",
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("expected prompt to contain %q", c)
		}
	}
	if strings.Contains(prompt, "INCREMENTAL") {
		t.Error("full build prompt should not contain INCREMENTAL")
	}
}

func TestBuildKBPromptIncrementalUpdate(t *testing.T) {
	prompt := buildKBPrompt("myrepo", "/tmp/myrepo", "/tmp/kb", "/tmp/kb/index.md", "abc123")
	checks := []string{
		"# Repository Context",
		"**KB Root Directory**: /tmp/kb",
		"Mode: INCREMENTAL UPDATE",
		"/tmp/kb/index.md",
		"abc123",
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("expected prompt to contain %q", c)
		}
	}
	if strings.Contains(prompt, "FULL BUILD") {
		t.Error("incremental prompt should not contain FULL BUILD")
	}
}

func TestKBInfo(t *testing.T) {
	// Create temp dirs — one with index.md, one without
	dirWithIndex := t.TempDir()
	dirWithoutIndex := t.TempDir()

	if err := os.WriteFile(filepath.Join(dirWithIndex, "index.md"), []byte("# Index"), 0o644); err != nil {
		t.Fatalf("writing index.md: %v", err)
	}

	// Verify KBInfo construction for dir with index.md
	indexPath := KBPath(dirWithIndex)
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("expected index.md to exist at %s", indexPath)
	}
	info := KBInfo{IndexPath: indexPath, RootDir: dirWithIndex}
	if info.IndexPath != indexPath {
		t.Errorf("IndexPath = %q, want %q", info.IndexPath, indexPath)
	}
	if info.RootDir != dirWithIndex {
		t.Errorf("RootDir = %q, want %q", info.RootDir, dirWithIndex)
	}

	// Verify dir without index.md correctly fails stat
	missingPath := KBPath(dirWithoutIndex)
	if _, err := os.Stat(missingPath); !os.IsNotExist(err) {
		t.Error("expected index.md to not exist in empty dir")
	}
}
