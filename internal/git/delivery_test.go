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
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestPendingAgainstReportsNothingWhenDelivered(t *testing.T) {
	t.Parallel()
	repo, _ := testutil.InitPublishReadyGitRepo(t)

	work, ok := PendingAgainst(repo, "origin/main")
	if !ok {
		t.Fatal("PendingAgainst ok = false; want true")
	}
	if work.Commits != 0 || work.DestinationAhead != 0 || work.Dirty {
		t.Fatalf("work = %+v; want zero value", work)
	}
	if work.Pending() {
		t.Error("Pending() = true; want false")
	}
}

func TestPendingAgainstCountsLocalCommits(t *testing.T) {
	t.Parallel()
	repo, _ := testutil.InitPublishReadyGitRepo(t)
	testutil.CommitFile(t, repo, "ahead.txt", "ahead\n", "ahead commit")

	work, ok := PendingAgainst(repo, "origin/main")
	if !ok {
		t.Fatal("PendingAgainst ok = false; want true")
	}
	if work.Commits != 1 {
		t.Errorf("Commits = %d; want 1", work.Commits)
	}
	if work.DestinationAhead != 0 {
		t.Errorf("DestinationAhead = %d; want 0", work.DestinationAhead)
	}
	if !work.Pending() {
		t.Error("Pending() = false; want true")
	}
}

func TestPendingAgainstReportsDirtyWorktree(t *testing.T) {
	t.Parallel()
	repo, _ := testutil.InitPublishReadyGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	work, ok := PendingAgainst(repo, "origin/main")
	if !ok {
		t.Fatal("PendingAgainst ok = false; want true")
	}
	if work.Commits != 0 {
		t.Errorf("Commits = %d; want 0", work.Commits)
	}
	if !work.Dirty {
		t.Error("Dirty = false; want true")
	}
	if !work.Pending() {
		t.Error("Pending() = false; want true")
	}
}

func TestPendingAgainstCountsDestinationAhead(t *testing.T) {
	t.Parallel()
	repo, bare := testutil.InitPublishReadyGitRepo(t)
	other := cloneFreshnessRepo(t, bare)
	testutil.CommitFile(t, other, "remote.txt", "remote\n", "remote commit")
	testutil.SimulatePush(t, other, bare, "main", "main")
	runFreshnessGit(t, repo, "fetch", "origin")

	work, ok := PendingAgainst(repo, "origin/main")
	if !ok {
		t.Fatal("PendingAgainst ok = false; want true")
	}
	if work.Commits != 0 {
		t.Errorf("Commits = %d; want 0", work.Commits)
	}
	if work.DestinationAhead != 1 {
		t.Errorf("DestinationAhead = %d; want 1", work.DestinationAhead)
	}
	if work.Pending() {
		t.Error("Pending() = true; want false — the destination is ahead, nothing local is undelivered")
	}
}

func TestPendingAgainstUnresolvedDestination(t *testing.T) {
	t.Parallel()
	repo, _ := testutil.InitPublishReadyGitRepo(t)

	if _, ok := PendingAgainst(repo, "origin/does-not-exist"); ok {
		t.Error("PendingAgainst ok = true for an unresolved destination; want false")
	}
	if _, ok := PendingAgainst(repo, ""); ok {
		t.Error("PendingAgainst ok = true for an empty destination; want false")
	}
	if _, ok := PendingAgainst("", "origin/main"); ok {
		t.Error("PendingAgainst ok = true for an empty worktree; want false")
	}
}
