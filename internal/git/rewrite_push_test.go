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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestPushRewrittenBranch_AllowsRedundantRemoteMerge(t *testing.T) {
	repo, bare, branch := remoteMergeFixture(t, false)

	if err := PushRewrittenBranch(repo, branch); err != nil {
		t.Fatalf("PushRewrittenBranch() error = %v; redundant merge should be replaceable", err)
	}
	if got := remoteBranchSHA(t, bare, branch); got != localHeadSHA(t, repo) {
		t.Fatalf("remote tip = %s; want rewritten HEAD", got)
	}
}

func TestPushRewrittenBranch_CreatesAbsentRemoteBranch(t *testing.T) {
	repo, bare := testutil.InitPublishReadyGitRepo(t)
	branch := "feature/absent"
	testutil.CreateBranch(t, repo, branch)
	testutil.CommitFile(t, repo, "created.txt", "created\n", "create remote branch")

	if err := PushRewrittenBranch(repo, branch); err != nil {
		t.Fatalf("PushRewrittenBranch() error = %v; want absent remote branch created", err)
	}
	if got := remoteBranchSHA(t, bare, branch); got != localHeadSHA(t, repo) {
		t.Fatalf("remote tip = %s; want newly published HEAD", got)
	}
}

func TestPushRewrittenBranch_RejectsCompetingCreationAfterAbsenceInspection(t *testing.T) {
	repo, bare := testutil.InitPublishReadyGitRepo(t)
	branch := "feature/competing-creation"
	testutil.CreateBranch(t, repo, branch)
	testutil.CommitFile(t, repo, "local.txt", "local\n", "local branch")

	other := cloneFreshnessRepo(t, bare)
	var competingSHA string
	hookCalled := false
	err := pushRewrittenBranch(repo, branch, func() {
		hookCalled = true
		runRewriteGit(t, other, "checkout", "-b", branch)
		competingSHA = testutil.CommitFile(t, other, "competitor.txt", "competitor\n", "competing branch")
		testutil.SimulatePush(t, other, bare, branch, branch)
	})
	if !hookCalled {
		t.Fatal("beforePush hook was not called after authoritative absence inspection")
	}
	if err == nil {
		t.Fatal("PushRewrittenBranch() error = nil; want competing branch creation rejected")
	}
	if got := remoteBranchSHA(t, bare, branch); got != competingSHA {
		t.Fatalf("remote tip = %s; want competing remote work %s preserved", got, competingSHA)
	}
	if got := runRewriteGit(t, bare, "show", "refs/heads/"+branch+":competitor.txt"); got != "competitor" {
		t.Fatalf("remote competitor content = %q; want preserved", got)
	}
}

func TestPushRewrittenBranch_DoesNotTreatRemoteFailureAsAbsence(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	branch := "feature/unreachable-origin"
	testutil.CreateBranch(t, repo, branch)
	testutil.CommitFile(t, repo, "local.txt", "local\n", "local branch")
	unreachable := filepath.Join(t.TempDir(), "missing-origin.git")
	runRewriteGit(t, repo, "remote", "add", "origin", unreachable)

	err := PushRewrittenBranch(repo, branch)
	if err == nil {
		t.Fatal("PushRewrittenBranch() error = nil; want unreachable remote rejected")
	}
	if !strings.Contains(err.Error(), "checking exact remote branch") {
		t.Fatalf("PushRewrittenBranch() error = %v; want authoritative absence check failure", err)
	}
}

func TestPushRewrittenBranch_RejectsOrdinaryRemoteCommit(t *testing.T) {
	repo, bare := testutil.InitPublishReadyGitRepo(t)
	branch := "feature/ordinary-remote"
	testutil.CreateBranch(t, repo, branch)
	testutil.CommitFile(t, repo, "first.txt", "first\n", "first commit")
	testutil.SimulatePush(t, repo, bare, branch, branch)

	other := cloneFreshnessRepo(t, bare)
	runRewriteGit(t, other, "checkout", branch)
	remoteSHA := testutil.CommitFile(t, other, "remote.txt", "remote\n", "ordinary remote commit")
	testutil.SimulatePush(t, other, bare, branch, branch)

	testutil.CommitFile(t, repo, "rewritten.txt", "rewritten\n", "rewritten local commit")

	err := PushRewrittenBranch(repo, branch)
	assertRewritePushError(t, err, RewritePushRemoteDiverged, branch, 1)
	if got := remoteBranchSHA(t, bare, branch); got != remoteSHA {
		t.Fatalf("remote tip = %s; want rejected remote commit %s", got, remoteSHA)
	}
}

func TestPushRewrittenBranch_RejectsUniqueMergeResolution(t *testing.T) {
	repo, bare, branch := remoteMergeFixture(t, true)
	remoteSHA := remoteBranchSHA(t, bare, branch)

	err := PushRewrittenBranch(repo, branch)
	assertRewritePushError(t, err, RewritePushRemoteDiverged, branch, 1)
	if got := remoteBranchSHA(t, bare, branch); got != remoteSHA {
		t.Fatalf("remote tip = %s; want rejected merge %s", got, remoteSHA)
	}
}

func TestPushRewrittenBranch_RejectsRemoteMoveAfterInspection(t *testing.T) {
	repo, bare, branch := remoteMergeFixture(t, false)
	other := cloneFreshnessRepo(t, bare)
	runRewriteGit(t, other, "checkout", branch)

	var movedSHA string
	err := pushRewrittenBranch(repo, branch, func() {
		movedSHA = testutil.CommitFile(t, other, "moved.txt", "moved\n", "remote moved after inspection")
		testutil.SimulatePush(t, other, bare, branch, branch)
	})
	assertRewritePushError(t, err, RewritePushRemoteChanged, branch, 1)
	if got := remoteBranchSHA(t, bare, branch); got != movedSHA {
		t.Fatalf("remote tip = %s; want post-inspection move %s", got, movedSHA)
	}
}

func TestPushRewrittenBranch_RejectsOrdinaryRemoteCommitHiddenByReplaceRef(t *testing.T) {
	fixture := ordinaryRemoteMasqueradeFixture(t, "feature/replace-ref")
	runRewriteGit(t, fixture.repo, "replace", "--graft", fixture.remoteSHA, fixture.featureA, fixture.master2)

	replacedParents := strings.Fields(runRewriteGit(t, fixture.repo, "rev-list", "--parents", "-n", "1", fixture.remoteSHA))
	if len(replacedParents) != 3 {
		t.Fatalf("replaced remote commit fields = %v; want commit plus two fake parents", replacedParents)
	}
	actualParents := strings.Fields(runRewriteGit(t, fixture.repo, "--no-replace-objects", "rev-list", "--parents", "-n", "1", fixture.remoteSHA))
	if len(actualParents) != 2 {
		t.Fatalf("actual remote commit fields = %v; want commit plus one real parent", actualParents)
	}
	if got := runRewriteGit(t, fixture.repo, "show", "--remerge-diff", "--format=", "--no-ext-diff", fixture.remoteSHA); got != "" {
		t.Fatalf("replacement merge remerge diff = %q; want empty exploit proof", got)
	}

	err := PushRewrittenBranch(fixture.repo, fixture.branch)
	assertRewritePushError(t, err, RewritePushRemoteDiverged, fixture.branch, 1)
	if got := remoteBranchSHA(t, fixture.bare, fixture.branch); got != fixture.remoteSHA {
		t.Fatalf("remote tip = %s; want ordinary remote commit %s preserved", got, fixture.remoteSHA)
	}
	if got := runRewriteGit(t, fixture.bare, "show", "refs/heads/"+fixture.branch+":master-2.txt"); got != fixture.remoteContent {
		t.Fatalf("remote content = %q; want %q preserved", got, fixture.remoteContent)
	}
}

func TestPushRewrittenBranch_RejectsOrdinaryRemoteCommitHiddenByGraft(t *testing.T) {
	fixture := ordinaryRemoteMasqueradeFixture(t, "feature/graft")
	runRewriteGit(t, fixture.repo, "config", "advice.graftFileDeprecated", "false")
	graftPath := runRewriteGit(t, fixture.repo, "rev-parse", "--git-path", "info/grafts")
	if !filepath.IsAbs(graftPath) {
		graftPath = filepath.Join(fixture.repo, graftPath)
	}
	graft := fixture.remoteSHA + " " + fixture.featureA + " " + fixture.master2 + "\n"
	if err := os.WriteFile(graftPath, []byte(graft), 0o600); err != nil {
		t.Fatalf("writing local graft file: %v", err)
	}

	graftedParents := strings.Fields(runRewriteGit(t, fixture.repo, "rev-list", "--parents", "-n", "1", fixture.remoteSHA))
	if len(graftedParents) != 3 {
		t.Fatalf("grafted remote commit fields = %v; want commit plus two fake parents", graftedParents)
	}

	err := PushRewrittenBranch(fixture.repo, fixture.branch)
	assertRewritePushError(t, err, RewritePushRemoteDiverged, fixture.branch, 1)
	if got := remoteBranchSHA(t, fixture.bare, fixture.branch); got != fixture.remoteSHA {
		t.Fatalf("remote tip = %s; want ordinary remote commit %s preserved", got, fixture.remoteSHA)
	}
	if got := runRewriteGit(t, fixture.bare, "show", "refs/heads/"+fixture.branch+":master-2.txt"); got != fixture.remoteContent {
		t.Fatalf("remote content = %q; want %q preserved", got, fixture.remoteContent)
	}
}

func TestRewritePushError_HidesWrappedCommandDetail(t *testing.T) {
	cause := errors.New("raw command output")
	pushErr := &RewritePushError{
		Kind:              RewritePushRemoteDiverged,
		Branch:            "feature/rewrite",
		RemoteOnlyCommits: 1,
		Err:               cause,
	}

	if got := pushErr.Error(); strings.Contains(got, cause.Error()) {
		t.Fatalf("RewritePushError.Error() = %q; want wrapped command detail omitted", got)
	}
	if !errors.Is(pushErr, cause) {
		t.Fatal("errors.Is(RewritePushError, cause) = false; want wrapped detail retained through Unwrap")
	}
}

type ordinaryRemoteMasquerade struct {
	repo          string
	bare          string
	branch        string
	featureA      string
	master2       string
	remoteSHA     string
	remoteContent string
}

func ordinaryRemoteMasqueradeFixture(t *testing.T, branch string) ordinaryRemoteMasquerade {
	t.Helper()

	repo, bare := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repo, branch)
	featureA := testutil.CommitFile(t, repo, "feature.txt", "feature A\n", "feature A")

	runRewriteGit(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "master-1.txt", "master 1\n", "master 1")
	master2 := testutil.CommitFile(t, repo, "master-2.txt", "master 2\n", "master 2")

	// The remote tip is an ordinary one-parent commit whose tree happens to
	// match the clean merge of featureA and master2.
	runRewriteGit(t, repo, "checkout", branch)
	runRewriteGit(t, repo, "checkout", master2, "--", "master-1.txt", "master-2.txt")
	runRewriteGit(t, repo, "commit", "-m", "ordinary remote commit")
	remoteSHA := localHeadSHA(t, repo)
	testutil.SimulatePush(t, repo, bare, branch, branch)
	remoteContent := runRewriteGit(t, bare, "show", "refs/heads/"+branch+":master-2.txt")

	// Local rewritten history contains both would-be fake parents, then changes
	// the remote file. Honoring a local replacement or graft would misclassify
	// the ordinary remote commit as a redundant merge.
	runRewriteGit(t, repo, "reset", "--hard", featureA)
	runRewriteGit(t, repo, "merge", "--no-ff", master2, "-m", "rewritten local merge")
	testutil.CommitFile(t, repo, "master-2.txt", "local rewritten\n", "local rewritten head")

	return ordinaryRemoteMasquerade{
		repo:          repo,
		bare:          bare,
		branch:        branch,
		featureA:      featureA,
		master2:       master2,
		remoteSHA:     remoteSHA,
		remoteContent: remoteContent,
	}
}

func remoteMergeFixture(t *testing.T, uniqueResolution bool) (repo, bare, branch string) {
	t.Helper()

	repo, bare = testutil.InitPublishReadyGitRepo(t)
	branch = "feature/rewrite"
	testutil.CreateBranch(t, repo, branch)
	featureA := testutil.CommitFile(t, repo, "feature.txt", "feature A\n", "feature A")

	runRewriteGit(t, repo, "checkout", "main")
	testutil.CommitFile(t, repo, "master-1.txt", "master 1\n", "master 1")
	master2 := testutil.CommitFile(t, repo, "master-2.txt", "master 2\n", "master 2")

	runRewriteGit(t, repo, "checkout", branch)
	if uniqueResolution {
		runRewriteGit(t, repo, "merge", "--no-ff", "--no-commit", master2)
		testutil.CommitFile(t, repo, "resolution.txt", "unique resolution\n", "remote unique merge")
	} else {
		runRewriteGit(t, repo, "merge", "--no-ff", master2, "-m", "remote redundant merge")
	}
	remoteMerge := localHeadSHA(t, repo)
	testutil.SimulatePush(t, repo, bare, branch, branch)

	runRewriteGit(t, repo, "reset", "--hard", featureA)
	runRewriteGit(t, repo, "merge", "--no-ff", master2, "-m", "rewritten local merge")
	testutil.CommitFile(t, repo, "head.txt", "head\n", "local head")

	parents := strings.Fields(runRewriteGit(t, repo, "rev-list", "--parents", "-n", "1", remoteMerge))
	if len(parents) != 3 {
		t.Fatalf("remote merge parents = %v; want exactly two parents", parents[1:])
	}
	for _, parent := range parents[1:] {
		if err := rewriteGitCommand(repo, "merge-base", "--is-ancestor", parent, "HEAD").Run(); err != nil {
			t.Fatalf("remote merge parent %s is not an ancestor of local HEAD: %v", parent, err)
		}
	}
	remergeDiff := runRewriteGit(t, repo, "show", "--remerge-diff", "--format=", "--no-ext-diff", remoteMerge)
	if uniqueResolution && remergeDiff == "" {
		t.Fatal("remote merge remerge diff is empty; want unique resolution")
	}
	if !uniqueResolution && remergeDiff != "" {
		t.Fatalf("remote merge remerge diff = %q; want empty", remergeDiff)
	}

	return repo, bare, branch
}

func assertRewritePushError(
	t *testing.T,
	err error,
	wantKind RewritePushErrorKind,
	wantBranch string,
	wantRemoteOnly int,
) {
	t.Helper()
	var pushErr *RewritePushError
	if !errors.As(err, &pushErr) {
		t.Fatalf("PushRewrittenBranch() error = %v; want *RewritePushError", err)
	}
	if pushErr.Kind != wantKind {
		t.Errorf("RewritePushError.Kind = %q; want %q", pushErr.Kind, wantKind)
	}
	if pushErr.Branch != wantBranch {
		t.Errorf("RewritePushError.Branch = %q; want %q", pushErr.Branch, wantBranch)
	}
	if pushErr.RemoteOnlyCommits != wantRemoteOnly {
		t.Errorf("RewritePushError.RemoteOnlyCommits = %d; want %d", pushErr.RemoteOnlyCommits, wantRemoteOnly)
	}
}

func remoteBranchSHA(t *testing.T, bare, branch string) string {
	t.Helper()
	return runRewriteGit(t, bare, "rev-parse", "refs/heads/"+branch)
}

func localHeadSHA(t *testing.T, repo string) string {
	t.Helper()
	return runRewriteGit(t, repo, "rev-parse", "HEAD")
}

func runRewriteGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := rewriteGitCommand(dir, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func rewriteGitCommand(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	return cmd
}
