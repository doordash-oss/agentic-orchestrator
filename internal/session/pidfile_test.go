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

package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPIDFileWriteAndRead(t *testing.T) {
	dir := t.TempDir()

	pf := PIDFile{
		PID:         12345,
		StartedAt:   time.Now().Truncate(time.Second),
		FeatureID:   "feat-1",
		Phase:       "implement",
		Iteration:   3,
		WorktreeDir: "/tmp/wt",
		RepoName:    "repo-a",
	}

	if err := WritePIDFile(dir, pf); err != nil {
		t.Fatalf("write PID file: %v", err)
	}

	path := filepath.Join(dir, "session-repo-a.pid")
	loaded, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("read PID file: %v", err)
	}

	if loaded.PID != pf.PID {
		t.Errorf("PID mismatch: got %d, want %d", loaded.PID, pf.PID)
	}
	if loaded.FeatureID != pf.FeatureID {
		t.Errorf("FeatureID mismatch: got %s, want %s", loaded.FeatureID, pf.FeatureID)
	}
	if loaded.Phase != pf.Phase {
		t.Errorf("Phase mismatch: got %s, want %s", loaded.Phase, pf.Phase)
	}
}

func TestPIDFileRemove(t *testing.T) {
	dir := t.TempDir()
	pf := PIDFile{PID: 1, RepoName: "repo-a"}
	WritePIDFile(dir, pf)

	if err := RemovePIDFile(dir, "repo-a"); err != nil {
		t.Fatalf("remove PID file: %v", err)
	}

	path := filepath.Join(dir, "session-repo-a.pid")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed")
	}
}

func TestFindPIDFiles(t *testing.T) {
	dir := t.TempDir()

	// Create two feature dirs with PID files
	feat1Dir := filepath.Join(dir, "feat-1", "implement")
	feat2Dir := filepath.Join(dir, "feat-2", "research")

	WritePIDFile(feat1Dir, PIDFile{PID: 100, FeatureID: "feat-1", Phase: "implement"})
	WritePIDFile(feat2Dir, PIDFile{PID: 200, FeatureID: "feat-2", Phase: "research"})

	pids, err := FindPIDFiles(dir)
	if err != nil {
		t.Fatalf("find PID files: %v", err)
	}
	if len(pids) != 2 {
		t.Errorf("expected 2 PID files, got %d", len(pids))
	}
}

func TestIsProcessRunning(t *testing.T) {
	// Current process should be running
	if !isProcessRunning(os.Getpid()) {
		t.Error("expected current process to be running")
	}

	// Non-existent PID should not be running
	if isProcessRunning(999999999) {
		t.Error("expected non-existent PID to not be running")
	}
}

func TestPIDFileName(t *testing.T) {
	tests := []struct {
		name     string
		repoName string
		want     string
	}{
		{name: "with repo name", repoName: "service-a", want: "session-service-a.pid"},
		{name: "another repo name", repoName: "backend", want: "session-backend.pid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PIDFileName(tt.repoName)
			if got != tt.want {
				t.Errorf("PIDFileName(%q) = %q, want %q", tt.repoName, got, tt.want)
			}
		})
	}
}

// TestPIDFileName_AlwaysPerRepo asserts the contract: PIDFileName always
// produces the per-repo "session-<repo>.pid" form. There is no longer a
// legacy "session.pid" path.
func TestPIDFileName_AlwaysPerRepo(t *testing.T) {
	if got := PIDFileName("foo"); got != "session-foo.pid" {
		t.Errorf("PIDFileName(\"foo\") = %q, want \"session-foo.pid\"", got)
	}
}

func TestWritePIDFileWithRepoName(t *testing.T) {
	dir := t.TempDir()

	pf := PIDFile{
		PID:       12345,
		StartedAt: time.Now().Truncate(time.Second),
		FeatureID: "feat-1",
		Phase:     "implement",
		Iteration: 1,
		RepoName:  "service-a",
	}

	if err := WritePIDFile(dir, pf); err != nil {
		t.Fatalf("write PID file: %v", err)
	}

	// Verify file is created at <dir>/session-service-a.pid
	path := filepath.Join(dir, "session-service-a.pid")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("expected session-service-a.pid to exist")
	}

	// Verify the default session.pid does NOT exist
	defaultPath := filepath.Join(dir, "session.pid")
	if _, err := os.Stat(defaultPath); !os.IsNotExist(err) {
		t.Error("expected session.pid to NOT exist when RepoName is set")
	}

	// Read it back and verify RepoName is preserved
	loaded, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("read PID file: %v", err)
	}
	if loaded.RepoName != "service-a" {
		t.Errorf("RepoName = %q, want %q", loaded.RepoName, "service-a")
	}
	if loaded.PID != 12345 {
		t.Errorf("PID = %d, want %d", loaded.PID, 12345)
	}
}

func TestFindPIDFilesMultipleRepos(t *testing.T) {
	dir := t.TempDir()

	// Create a feature dir with two repo-specific PID files
	featDir := filepath.Join(dir, "feat-multi")

	_ = WritePIDFile(featDir, PIDFile{PID: 100, FeatureID: "feat-multi", Phase: "implement", RepoName: "service-a"})
	_ = WritePIDFile(featDir, PIDFile{PID: 200, FeatureID: "feat-multi", Phase: "implement", RepoName: "service-b"})

	pids, err := FindPIDFiles(dir)
	if err != nil {
		t.Fatalf("find PID files: %v", err)
	}
	if len(pids) != 2 {
		t.Fatalf("expected 2 PID files, got %d", len(pids))
	}

	// Verify both have correct RepoName values
	repoNames := map[string]bool{}
	for _, pf := range pids {
		repoNames[pf.RepoName] = true
	}
	if !repoNames["service-a"] {
		t.Error("expected to find PID file with RepoName=service-a")
	}
	if !repoNames["service-b"] {
		t.Error("expected to find PID file with RepoName=service-b")
	}
}

func TestRemovePIDFileWithRepoName(t *testing.T) {
	dir := t.TempDir()

	// Write a repo-specific PID file
	_ = WritePIDFile(dir, PIDFile{PID: 100, FeatureID: "feat-1", RepoName: "service-a"})

	// Verify it exists
	path := filepath.Join(dir, "session-service-a.pid")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("expected session-service-a.pid to exist before removal")
	}

	// Remove it
	if err := RemovePIDFile(dir, "service-a"); err != nil {
		t.Fatalf("remove PID file: %v", err)
	}

	// Verify it's gone
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected session-service-a.pid to be removed")
	}
}

func TestReadPIDFilePreservesRepoName(t *testing.T) {
	dir := t.TempDir()

	pf := PIDFile{
		PID:         12345,
		StartedAt:   time.Now().Truncate(time.Second),
		FeatureID:   "feat-1",
		Phase:       "implement",
		Iteration:   3,
		WorktreeDir: "/tmp/wt",
		SessionID:   "sess-123",
		RepoName:    "service-a",
	}

	if err := WritePIDFile(dir, pf); err != nil {
		t.Fatalf("write PID file: %v", err)
	}

	path := filepath.Join(dir, "session-service-a.pid")
	loaded, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("read PID file: %v", err)
	}

	if loaded.PID != pf.PID {
		t.Errorf("PID = %d, want %d", loaded.PID, pf.PID)
	}
	if loaded.FeatureID != pf.FeatureID {
		t.Errorf("FeatureID = %q, want %q", loaded.FeatureID, pf.FeatureID)
	}
	if loaded.Phase != pf.Phase {
		t.Errorf("Phase = %q, want %q", loaded.Phase, pf.Phase)
	}
	if loaded.Iteration != pf.Iteration {
		t.Errorf("Iteration = %d, want %d", loaded.Iteration, pf.Iteration)
	}
	if loaded.WorktreeDir != pf.WorktreeDir {
		t.Errorf("WorktreeDir = %q, want %q", loaded.WorktreeDir, pf.WorktreeDir)
	}
	if loaded.SessionID != pf.SessionID {
		t.Errorf("SessionID = %q, want %q", loaded.SessionID, pf.SessionID)
	}
	if loaded.RepoName != pf.RepoName {
		t.Errorf("RepoName = %q, want %q", loaded.RepoName, pf.RepoName)
	}
}
