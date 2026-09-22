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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestRemoteUpToDateGuards(t *testing.T) {
	t.Parallel()

	repo, bare := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repo, "feature/test")
	testutil.CommitFile(t, repo, "feature.txt", "feature work\n", "feature commit")
	testutil.SimulatePush(t, repo, bare, "feature/test", "feature/test")

	if IsBehindRemote(repo, "main") {
		t.Error("IsBehindRemote() = true, want false when up to date")
	}
}

func TestIsBehindRemote_Behind(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping behind-remote multi-repo regression in short mode")
	}
	t.Parallel()

	repo, bare := testutil.InitPublishReadyGitRepo(t)

	// Create feature branch
	testutil.CreateBranch(t, repo, "feature/test")

	// Simulate a new commit on remote main by pushing from a clone
	clone := t.TempDir()
	gitClone(t, bare, clone)
	testutil.CommitFile(t, clone, "remote.txt", "remote\n", "remote commit")
	gitPush(t, clone, "main")

	// Fetch in original repo
	if err := Fetch(repo); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if !IsBehindRemote(repo, "main") {
		t.Error("expected behind remote after new remote commit")
	}
}

func TestPRBaseBranch_NoGH(t *testing.T) {
	t.Parallel()

	// PRBaseBranch returns "" when the URL cannot be parsed as a PR URL.
	result := PRBaseBranch("/nonexistent", "https://example.com/not-a-pr")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestPRBaseBranchReturnsBaseRefAndEmptyOnError(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/acme/widgets/pulls/7", 200, `{"base":{"ref":"develop"}}`)
	fake.HandleJSON("/repos/acme/widgets/pulls/8", 404, `{"message":"Not Found"}`)

	if got := PRBaseBranch("/nonexistent", "https://github.com/acme/widgets/pull/7"); got != "develop" {
		t.Errorf("PRBaseBranch() = %q, want %q", got, "develop")
	}
	if got := PRBaseBranch("/nonexistent", "https://github.com/acme/widgets/pull/8"); got != "" {
		t.Errorf("PRBaseBranch() = %q, want empty string on API error", got)
	}
}

// TestFetchBranchForcesRemoteTrackingRefUpdate proves the invariant FetchBranch
// exists for: even when the remote branch moved non-fast-forward, the forced
// refspec lands refs/remotes/origin/<branch> at the remote tip.
func TestFetchBranchForcesRemoteTrackingRefUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real git repository with a bare origin")
	}
	t.Parallel()

	repo, bare := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repo, "feature/test")
	testutil.CommitFile(t, repo, "feature.txt", "first\n", "first commit")
	testutil.SimulatePush(t, repo, bare, "feature/test", "feature/test")

	// Rewrite the remote branch non-fast-forward from a clone so the stale
	// tracking ref can only catch up through the forced refspec.
	clone := t.TempDir()
	gitClone(t, bare, clone)
	runGit(t, clone, "checkout", "-B", "feature/test", "main")
	testutil.CommitFile(t, clone, "feature.txt", "rewritten history\n", "rewritten commit")
	runGit(t, bare, "fetch", clone, "+feature/test:refs/heads/feature/test")

	staleTip := runGit(t, repo, "rev-parse", "refs/remotes/origin/feature/test")
	remoteTip := runGit(t, bare, "rev-parse", "refs/heads/feature/test")
	if staleTip == remoteTip {
		t.Fatalf("remote branch did not move non-fast-forward: both at %s", remoteTip)
	}

	if err := FetchBranch(repo, "feature/test"); err != nil {
		t.Fatalf("FetchBranch: %v", err)
	}
	trackedTip := runGit(t, repo, "rev-parse", "refs/remotes/origin/feature/test")
	if trackedTip != remoteTip {
		t.Fatalf("refs/remotes/origin/feature/test = %s, want forced remote tip %s", trackedTip, remoteTip)
	}

	// A branch the remote does not have must surface the fetch error rather
	// than silently leaving a stale tracking ref in place.
	if err := FetchBranch(repo, "feature/missing"); err == nil {
		t.Fatal("FetchBranch() = nil error, want error for a branch absent on the remote")
	}
}

// helpers for rebase tests

func gitClone(t *testing.T, bare, dest string) {
	t.Helper()
	runGit(t, "", "clone", bare, dest)
	runGit(t, dest, "config", "user.email", "test@test.com")
	runGit(t, dest, "config", "user.name", "Test")
	runGit(t, dest, "config", "commit.gpgsign", "false")
	runGit(t, dest, "config", "tag.gpgsign", "false")
}

func gitPush(t *testing.T, repoPath, branch string) {
	t.Helper()
	// Discover the bare remote path and use SimulatePush to avoid git-push.
	cmd := gitCmd(repoPath, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("gitPush: get remote url: %v", err)
	}
	bareDir := strings.TrimSpace(string(out))
	testutil.SimulatePush(t, repoPath, bareDir, branch, branch)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitCmd(dir, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_EDITOR=:",
		"GIT_SEQUENCE_EDITOR=:",
		"GIT_TERMINAL_PROMPT=0",
	)
	return cmd
}

// TestPush_SyncsRemoteTrackingRefOnSingleBranchClone pins that a successful
// push moves refs/remotes/origin/<branch> even when the clone's fetch
// refspec only maps main, so freshness probes see the branch as in sync.
func TestPush_SyncsRemoteTrackingRefOnSingleBranchClone(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-push tracking-ref regression in short mode")
	}
	t.Parallel()

	repo, _ := testutil.InitPublishReadyGitRepo(t)
	runGit(t, repo, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/main:refs/remotes/origin/main")
	testutil.CreateBranch(t, repo, "feature/test")
	testutil.CommitFile(t, repo, "feature.txt", "feature work\n", "feature commit")

	if err := defaultPush(repo, "feature/test"); err != nil {
		t.Fatalf("defaultPush() error = %v", err)
	}
	head, _ := gitCmd(repo, "rev-parse", "feature/test").Output()
	tracking, err := gitCmd(repo, "rev-parse", "refs/remotes/origin/feature/test").Output()
	if err != nil {
		t.Fatalf("remote-tracking ref missing after push: %v", err)
	}
	if strings.TrimSpace(string(tracking)) != strings.TrimSpace(string(head)) {
		t.Fatalf("origin/feature/test = %s, want pushed tip %s", tracking, head)
	}
}
