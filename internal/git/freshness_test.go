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

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestRepoFreshnessInSync(t *testing.T) {
	t.Parallel()

	repo, _ := testutil.InitPublishReadyGitRepo(t)

	if got := RepoFreshness(repo); got != "in sync" {
		t.Errorf("RepoFreshness() = %q, want %q", got, "in sync")
	}
}

func TestRepoFreshnessLocalChanges(t *testing.T) {
	t.Parallel()

	repo, _ := testutil.InitPublishReadyGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	if got := RepoFreshness(repo); got != "local changes" {
		t.Errorf("RepoFreshness() = %q, want %q", got, "local changes")
	}
}

func TestRepoFreshnessLocalChangesAhead(t *testing.T) {
	t.Parallel()

	repo, _ := testutil.InitPublishReadyGitRepo(t)
	testutil.CommitFile(t, repo, "ahead.txt", "ahead\n", "ahead commit")

	if got := RepoFreshness(repo); got != "local changes" {
		t.Errorf("RepoFreshness() = %q, want %q", got, "local changes")
	}
}

func TestRepoFreshnessLocalChangesBehind(t *testing.T) {
	t.Parallel()

	repo, bare := testutil.InitPublishReadyGitRepo(t)
	remoteRepo := cloneFreshnessRepo(t, bare)
	testutil.CommitFile(t, remoteRepo, "behind.txt", "behind\n", "remote commit")
	testutil.SimulatePush(t, remoteRepo, bare, "main", "main")
	runFreshnessGit(t, repo, "fetch", "origin")

	if got := RepoFreshness(repo); got != "local changes" {
		t.Errorf("RepoFreshness() = %q, want %q", got, "local changes")
	}
}

func TestRepoFreshnessLocalChangesDiverged(t *testing.T) {
	t.Parallel()

	repo, bare := testutil.InitPublishReadyGitRepo(t)
	testutil.CommitFile(t, repo, "local.txt", "local\n", "local commit")
	remoteRepo := cloneFreshnessRepo(t, bare)
	testutil.CommitFile(t, remoteRepo, "remote.txt", "remote\n", "remote commit")
	testutil.SimulatePush(t, remoteRepo, bare, "main", "main")
	runFreshnessGit(t, repo, "fetch", "origin")

	if got := RepoFreshness(repo); got != "local changes" {
		t.Errorf("RepoFreshness() = %q, want %q", got, "local changes")
	}
}

func TestRepoFreshnessLocalOnly(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)

	if got := RepoFreshness(repo); got != "local only" {
		t.Errorf("RepoFreshness() = %q, want %q", got, "local only")
	}
}

func cloneFreshnessRepo(t *testing.T, bare string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	runFreshnessGit(t, "", "clone", bare, dir)
	runFreshnessGit(t, dir, "config", "user.email", "test@test.com")
	runFreshnessGit(t, dir, "config", "user.name", "Test")
	runFreshnessGit(t, dir, "config", "commit.gpgsign", "false")
	runFreshnessGit(t, dir, "config", "tag.gpgsign", "false")
	return dir
}

func runFreshnessGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
