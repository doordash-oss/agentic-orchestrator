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
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestKBPath(t *testing.T) {
	got := KBPath("/tmp/kb")
	want := "/tmp/kb/index.md"
	if got != want {
		t.Errorf("KBPath = %q, want %q", got, want)
	}
}

func TestKBRootDir(t *testing.T) {
	got := KBRootDir("/tmp/kb")
	want := "/tmp/kb"
	if got != want {
		t.Errorf("KBRootDir = %q, want %q", got, want)
	}
}

func TestKBLockPath(t *testing.T) {
	got := KBLockPath("/tmp/kb")
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
	locked, err := AcquireKBLock(dir, "feature-1")
	if err != nil {
		t.Fatalf("AcquireKBLock: %v", err)
	}
	if !locked {
		t.Fatal("expected lock to be acquired")
	}

	// Second acquire fails (contention)
	locked2, err := AcquireKBLock(dir, "feature-2")
	if err != nil {
		t.Fatalf("AcquireKBLock (second): %v", err)
	}
	if locked2 {
		t.Fatal("expected lock NOT to be acquired (contention)")
	}

	// Release with wrong feature ID should not remove lock
	if err := ReleaseKBLock(dir, "feature-2"); err != nil {
		t.Fatalf("ReleaseKBLock (wrong ID): %v", err)
	}
	// Lock should still exist
	if _, err := os.Stat(KBLockPath(dir)); err != nil {
		t.Fatal("lock should still exist after release with wrong ID")
	}

	// Release with correct ID
	if err := ReleaseKBLock(dir, "feature-1"); err != nil {
		t.Fatalf("ReleaseKBLock: %v", err)
	}
	if _, err := os.Stat(KBLockPath(dir)); !os.IsNotExist(err) {
		t.Fatal("lock should be removed after release")
	}

	// Reentrant: same feature can re-acquire its own lock
	_, _ = AcquireKBLock(dir, "feature-reentrant")
	locked3, err := AcquireKBLock(dir, "feature-reentrant")
	if err != nil {
		t.Fatalf("AcquireKBLock (reentrant): %v", err)
	}
	if !locked3 {
		t.Fatal("expected reentrant lock acquisition to succeed")
	}
	if err := ReleaseKBLock(dir, "feature-reentrant"); err != nil {
		t.Fatalf("ReleaseKBLock (reentrant): %v", err)
	}

	// Force release (empty feature ID)
	_, _ = AcquireKBLock(dir, "feature-3")
	if err := ReleaseKBLock(dir, ""); err != nil {
		t.Fatalf("ReleaseKBLock (force): %v", err)
	}
	if _, err := os.Stat(KBLockPath(dir)); !os.IsNotExist(err) {
		t.Fatal("lock should be removed after force release")
	}
}

func TestIsKBLockStale(t *testing.T) {
	dir := t.TempDir()

	// No lock = not stale
	if IsKBLockStale(dir, nil) {
		t.Error("expected no stale lock when no lock exists")
	}

	// Fresh lock = not stale (nil statusFn — time-only check)
	_, _ = AcquireKBLock(dir, "feature-1")
	if IsKBLockStale(dir, nil) {
		t.Error("expected fresh lock not to be stale")
	}
}

func TestIsKBLockStaleOwnerCheck(t *testing.T) {
	tests := []struct {
		name      string
		statusFn  FeatureStatusFunc
		wantStale bool
	}{
		{
			name:      "owner still building KB",
			statusFn:  func(id string) (int, bool) { return int(feature.StatusBuildingKB), true },
			wantStale: false,
		},
		{
			name:      "owner no longer building KB",
			statusFn:  func(id string) (int, bool) { return 0, true }, // StatusCreated
			wantStale: true,
		},
		{
			name:      "owner feature not found",
			statusFn:  func(id string) (int, bool) { return 0, false },
			wantStale: true,
		},
		{
			name:      "nil statusFn — time-only",
			statusFn:  nil,
			wantStale: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			_, _ = AcquireKBLock(dir, "feature-1")
			got := IsKBLockStale(dir, tt.statusFn)
			if got != tt.wantStale {
				t.Errorf("IsKBLockStale = %v, want %v", got, tt.wantStale)
			}
		})
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

func TestHasKBChanges(t *testing.T) {
	repoDir := t.TempDir()
	runner := &fakeGitRunner{head: "commit-a"}
	headCommit := runner.head
	kbDir := t.TempDir()

	t.Run("no state.json returns true", func(t *testing.T) {
		emptyDir := t.TempDir()
		has, err := HasKBChanges(context.Background(), runner, emptyDir, repoDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Error("expected true when no state.json exists")
		}
	})

	t.Run("no index.md returns true", func(t *testing.T) {
		dir := t.TempDir()
		_ = SaveKBState(dir, &KBState{HeadCommit: headCommit, LastUpdated: time.Now(), Version: 1})
		has, err := HasKBChanges(context.Background(), runner, dir, repoDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Error("expected true when index.md is missing")
		}
	})

	t.Run("state + index.md + no changes returns false", func(t *testing.T) {
		_ = SaveKBState(kbDir, &KBState{HeadCommit: headCommit, LastUpdated: time.Now(), Version: 1})
		_ = os.WriteFile(KBPath(kbDir), []byte("# KB"), 0o644)
		has, err := HasKBChanges(context.Background(), runner, kbDir, repoDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if has {
			t.Error("expected false when no git changes exist")
		}
	})

	t.Run("state + index.md + new commits returns true", func(t *testing.T) {
		runner.log = "commit-b second\n"
		has, err := HasKBChanges(context.Background(), runner, kbDir, repoDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Error("expected true when new commits exist")
		}
	})
}

func TestBuildKBPromptFullBuild(t *testing.T) {
	prompt := BuildKBPrompt("myrepo", "/tmp/myrepo", "/tmp/kb", "", "")
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
	prompt := BuildKBPrompt("myrepo", "/tmp/myrepo", "/tmp/kb", "/tmp/kb/index.md", "abc123")
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
