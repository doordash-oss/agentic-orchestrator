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
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestRestoreReadOnlyRepoBaselinePreservesUntrackedEntries(t *testing.T) {
	repoDir := t.TempDir()
	runRepoMutationGit(t, repoDir, "init")
	runRepoMutationGit(t, repoDir, "config", "user.email", "test@example.com")
	runRepoMutationGit(t, repoDir, "config", "user.name", "Test User")
	trackedPath := filepath.Join(repoDir, "tracked.txt")
	if err := os.WriteFile(trackedPath, []byte("tracked baseline\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runRepoMutationGit(t, repoDir, "add", "tracked.txt")
	runRepoMutationGit(t, repoDir, "commit", "-m", "baseline")

	untrackedPath := filepath.Join(repoDir, "notes.txt")
	if err := os.WriteFile(untrackedPath, []byte("untracked baseline\n"), 0o600); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}
	symlinkPath := filepath.Join(repoDir, "current-notes")
	if err := os.Symlink("notes.txt", symlinkPath); err != nil {
		t.Fatalf("create baseline symlink: %v", err)
	}
	emptyDir := filepath.Join(repoDir, "user-empty-dir")
	if err := os.Mkdir(emptyDir, 0o755); err != nil {
		t.Fatalf("create baseline empty dir: %v", err)
	}

	repo := feature.FeatureRepo{Name: "repo", WorktreePath: repoDir}
	baseline, ok, err := captureReadOnlyRepoSnapshot(context.Background(), nil, repo)
	if err != nil || !ok {
		t.Fatalf("capture baseline = ok %v, err %v", ok, err)
	}

	if err := os.WriteFile(trackedPath, []byte("mutated tracked\n"), 0o644); err != nil {
		t.Fatalf("mutate tracked file: %v", err)
	}
	if err := os.WriteFile(untrackedPath, []byte("mutated untracked\n"), 0o600); err != nil {
		t.Fatalf("mutate untracked file: %v", err)
	}
	if err := os.Remove(symlinkPath); err != nil {
		t.Fatalf("remove baseline symlink: %v", err)
	}
	if err := os.Symlink("tracked.txt", symlinkPath); err != nil {
		t.Fatalf("replace baseline symlink: %v", err)
	}
	introducedPath := filepath.Join(repoDir, "new", "introduced.txt")
	if err := os.MkdirAll(filepath.Dir(introducedPath), 0o755); err != nil {
		t.Fatalf("create introduced parent: %v", err)
	}
	if err := os.WriteFile(introducedPath, []byte("introduced\n"), 0o644); err != nil {
		t.Fatalf("write introduced file: %v", err)
	}

	current, ok, err := captureReadOnlyRepoSnapshot(context.Background(), nil, repo)
	if err != nil || !ok {
		t.Fatalf("capture current = ok %v, err %v", ok, err)
	}
	if err := restoreReadOnlyRepoBaseline(context.Background(), nil, current, baseline); err != nil {
		t.Fatalf("restore baseline: %v", err)
	}

	assertRepoMutationFileContent(t, trackedPath, "tracked baseline\n")
	assertRepoMutationFileContent(t, untrackedPath, "untracked baseline\n")
	if target, err := os.Readlink(symlinkPath); err != nil || target != "notes.txt" {
		t.Fatalf("restored symlink target = %q, err %v; want notes.txt", target, err)
	}
	if _, err := os.Lstat(introducedPath); !os.IsNotExist(err) {
		t.Fatalf("introduced path still exists, err = %v", err)
	}
	if info, err := os.Stat(emptyDir); err != nil || !info.IsDir() {
		t.Fatalf("pre-existing empty directory was not preserved: info %v, err %v", info, err)
	}
}

func runRepoMutationGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func assertRepoMutationFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s content = %q, want %q", path, data, want)
	}
}
