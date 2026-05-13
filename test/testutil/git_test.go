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

package testutil

import (
	"os/exec"
	"strings"
	"testing"
)

func TestInitMinimalGitRepoStartsWithoutCommit(t *testing.T) {
	t.Parallel()

	repo := InitMinimalGitRepo(t)

	if out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "HEAD").CombinedOutput(); err == nil {
		t.Fatalf("rev-parse HEAD unexpectedly succeeded: %s", out)
	}

	commit := CommitFile(t, repo, "first.txt", "first\n", "first commit")
	if commit == "" {
		t.Fatal("CommitFile() returned empty commit hash")
	}
}

func TestPairWithBareRemotePushesSelectedBranch(t *testing.T) {
	t.Parallel()

	repo := InitGitRepo(t)
	CreateBranch(t, repo, "feature/test")
	CommitFile(t, repo, "feature.txt", "feature\n", "feature commit")

	bare := PairWithBareRemote(t, repo, "feature/test", "feature/test")

	got := strings.TrimSpace(runGitOutput(t, repo, "remote", "get-url", "origin"))
	if got != bare {
		t.Fatalf("origin remote = %q, want %q", got, bare)
	}
	if out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "origin/feature/test").CombinedOutput(); err != nil {
		t.Fatalf("origin/feature/test missing: %v\n%s", err, out)
	}
}

func TestInitPublishReadyGitRepoWiresOriginAndUpstream(t *testing.T) {
	t.Parallel()

	repo, bare := InitPublishReadyGitRepo(t)

	got := strings.TrimSpace(runGitOutput(t, repo, "remote", "get-url", "origin"))
	if got != bare {
		t.Fatalf("origin remote = %q, want %q", got, bare)
	}
	if upstream := strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")); upstream != "origin/main" {
		t.Fatalf("upstream = %q, want origin/main", upstream)
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
