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

// Real-git coverage of the remote-divergence inspection the rebase preflight
// runs per layer branch: a remote tip holding plain reviewer commits plus a
// merge of the base diverges with the plain commits offered oldest first and
// the merge counted but never adopted; a remote holding exactly the
// last-pushed SHA, a remote already contained in the local chain, and an
// absent remote branch never diverge.

package git_test

import (
	"path/filepath"
	"testing"

	gitpkg "github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// divergenceFixture builds one layer branch pushed to a bare origin at its
// last-pushed tip, and returns the inspected repo, the bare origin, the
// branch name, and the last-pushed SHA.
func divergenceFixture(t *testing.T) (repo, bare, branch, lastPushed string) {
	t.Helper()
	repo = testutil.InitGitRepo(t)
	bare = testutil.InitBareRemote(t, repo)
	branch = "stack/2"
	testutil.CreateBranch(t, repo, branch)
	commitAsAuthor(t, repo, "l2.txt", "layer2 v1\n", "layer2 commit", "Local Author", "local@example.com")
	lastPushed = runGitRefTest(t, repo, "rev-parse", "HEAD")
	testutil.SimulatePush(t, repo, bare, branch, branch)
	return repo, bare, branch, lastPushed
}

func TestInspectRemoteDivergence_PlainCommitsAndBaseMerge(t *testing.T) {
	repo, bare, branch, lastPushed := divergenceFixture(t)
	// A reviewer clone pushes two plain commits and a content-free merge of
	// the base ("Update branch") onto the layer branch.
	reviewer := cloneDivergenceRepo(t, bare)
	runGitRefTest(t, reviewer, "checkout", branch)
	r1 := commitAsAuthor(t, reviewer, "review.txt", "review one\n", "reviewer fix one", "Reviewer One", "reviewer1@example.com")
	r2 := commitAsAuthor(t, reviewer, "review2.txt", "review two\n", "reviewer fix two", "Reviewer Two", "reviewer2@example.com")
	// A content-free "Update branch" merge of the base: GitHub-style, the
	// base side contributes nothing, so the merge commit shares the branch
	// tip's tree. git refuses to merge an unchanged base, so the commit is
	// assembled with plumbing exactly like the redundant merges the push
	// proof admits.
	tree := runGitRefTest(t, reviewer, "rev-parse", "HEAD^{tree}")
	remoteTip := runGitRefTest(t, reviewer, "commit-tree", "-p", "HEAD", "-p", "origin/main", "-m", "Update branch", tree)
	runGitRefTest(t, reviewer, "reset", "--hard", remoteTip)
	// SimulatePush transfers the objects into the bare origin; the fetch
	// below lands them and the remote-tracking ref in the inspected repo,
	// exactly as the preflight's fetch does.
	testutil.SimulatePush(t, reviewer, bare, branch, branch)
	runGitRefTest(t, repo, "fetch", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch)

	report, err := gitpkg.InspectRemoteDivergence(repo, remoteTip, lastPushed, lastPushed)
	if err != nil {
		t.Fatalf("gitpkg.InspectRemoteDivergence() error = %v", err)
	}
	if !report.Diverged {
		t.Fatal("report.Diverged = false, want true with two reviewer commits on the remote")
	}
	if report.RemoteTip != remoteTip {
		t.Fatalf("report.RemoteTip = %s, want %s", report.RemoteTip, remoteTip)
	}
	if report.RemoteOnlyCommits != 3 {
		t.Fatalf("report.RemoteOnlyCommits = %d, want 3 (two plain commits plus the merge)", report.RemoteOnlyCommits)
	}
	if len(report.ForeignCommits) != 2 {
		t.Fatalf("report.ForeignCommits = %+v, want the two plain commits only", report.ForeignCommits)
	}
	if report.ForeignCommits[0].SHA != r1 || report.ForeignCommits[1].SHA != r2 {
		t.Fatalf("report.ForeignCommits = [%s %s], want oldest first [%s %s]",
			report.ForeignCommits[0].SHA, report.ForeignCommits[1].SHA, r1, r2)
	}
	if report.ForeignCommits[0].Subject != "reviewer fix one" || report.ForeignCommits[1].Subject != "reviewer fix two" {
		t.Fatalf("foreign subjects = %q, %q, want the reviewer subjects", report.ForeignCommits[0].Subject, report.ForeignCommits[1].Subject)
	}
	if report.ForeignCommits[0].Author != "Reviewer One <reviewer1@example.com>" ||
		report.ForeignCommits[1].Author != "Reviewer Two <reviewer2@example.com>" {
		t.Fatalf("foreign authors = %q, %q, want the reviewer identities", report.ForeignCommits[0].Author, report.ForeignCommits[1].Author)
	}
}

func TestInspectRemoteDivergence_RemoteEqualsLastPushedWithRewrittenLocal(t *testing.T) {
	repo, bare, branch, lastPushed := divergenceFixture(t)
	_ = bare
	_ = branch
	// The local layer was rewritten after the publish: the new chain never
	// contains the old tip, but the remote still holds exactly the
	// last-pushed SHA.
	runGitRefTest(t, repo, "checkout", "main")
	runGitRefTest(t, repo, "checkout", "-b", "stack/2-rewritten")
	commitAsAuthor(t, repo, "l2.txt", "layer2 v2\n", "layer2 rewritten", "Local Author", "local@example.com")
	localTip := runGitRefTest(t, repo, "rev-parse", "HEAD")

	report, err := gitpkg.InspectRemoteDivergence(repo, lastPushed, localTip, lastPushed)
	if err != nil {
		t.Fatalf("gitpkg.InspectRemoteDivergence() error = %v", err)
	}
	if report.Diverged {
		t.Fatal("report.Diverged = true, want false: the remote holds exactly the last-pushed SHA")
	}
	if report.RemoteOnlyCommits != 0 || len(report.ForeignCommits) != 0 {
		t.Fatalf("report = %+v, want no remote-only commits", report)
	}
}

func TestInspectRemoteDivergence_RemoteAncestorOfLocalTip(t *testing.T) {
	repo, _, _, lastPushed := divergenceFixture(t)
	// The reviewer's commit was already applied locally on top of the
	// last-pushed tip and one more local commit landed above it, so the
	// remote tip is a strict ancestor of the local tip.
	reviewerSHA := commitAsAuthor(t, repo, "review.txt", "applied locally\n", "reviewer fix one", "Reviewer One", "reviewer1@example.com")
	commitAsAuthor(t, repo, "l2.txt", "layer2 v2\n", "layer2 follow-up", "Local Author", "local@example.com")
	localTip := runGitRefTest(t, repo, "rev-parse", "HEAD")

	report, err := gitpkg.InspectRemoteDivergence(repo, reviewerSHA, localTip, lastPushed)
	if err != nil {
		t.Fatalf("gitpkg.InspectRemoteDivergence() error = %v", err)
	}
	if report.Diverged {
		t.Fatal("report.Diverged = true, want false: the remote tip is an ancestor of the local tip")
	}
}

func TestInspectRemoteDivergence_AbsentRemoteBranch(t *testing.T) {
	repo, _, _, lastPushed := divergenceFixture(t)

	report, err := gitpkg.InspectRemoteDivergence(repo, "", lastPushed, lastPushed)
	if err != nil {
		t.Fatalf("gitpkg.InspectRemoteDivergence() error = %v", err)
	}
	if report.Diverged {
		t.Fatal("report.Diverged = true, want false: the remote branch is absent")
	}
}

// cloneDivergenceRepo clones the bare origin so reviewer work can be
// authored and pushed without touching the inspected checkout.
func cloneDivergenceRepo(t *testing.T, bare string) string {
	t.Helper()
	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	runGitRefTest(t, parent, "clone", bare, clone)
	return clone
}
