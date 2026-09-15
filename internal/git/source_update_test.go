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
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// These tests fork real git and follow the initialize-api convention of not
// using t.Parallel(): the race-detector binary misbehaves when many
// fork-heavy tests run at once.

func runGitUpdateTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = testutil.GitTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %v: %v: %s", dir, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitUpdateSHA(t *testing.T, dir, ref string) string {
	t.Helper()
	return runGitUpdateTest(t, dir, "rev-parse", "--verify", ref+"^{commit}")
}

func hasUpdateBlocker(t *testing.T, blockers []UpdateBlocker, want UpdateBlocker) {
	t.Helper()
	for _, blocker := range blockers {
		if blocker == want {
			return
		}
	}
	t.Fatalf("blockers = %v; want %q", blockers, want)
}

// updateFixture is one repository whose default branch main is behind its
// bare origin while the checkout sits on an unrelated branch work, so main
// is absent from every worktree checkout.
type updateFixture struct {
	repo   string
	bare   string
	writer string
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	repo := testutil.InitGitRepo(t)
	bare := testutil.PairWithBareRemote(t, repo, "main", "main")
	runGitUpdateTest(t, repo, "checkout", "-b", "work")
	// The default-mode source resolves through origin/HEAD, not the
	// checkout HEAD, so main stays the selection while work holds HEAD.
	runGitUpdateTest(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	writer := filepath.Join(t.TempDir(), "writer")
	runGitUpdateTest(t, repo, "clone", bare, writer)
	runGitUpdateTest(t, writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote one")
	runGitUpdateTest(t, writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote two")
	runGitUpdateTest(t, writer, "push", "origin", "main")
	return &updateFixture{repo: repo, bare: bare, writer: writer}
}

// expectation builds the displayed expectations for the fixture's default
// branch from freshly resolved state.
func (fx *updateFixture) expectation(t *testing.T) SourceUpdateExpectation {
	t.Helper()
	head := runGitUpdateTest(t, fx.repo, "rev-parse", "HEAD^{commit}")
	return SourceUpdateExpectation{
		Mode:              LocalSourceModeDefault,
		Branch:            "main",
		OriginBranch:      "main",
		ExpectedLocalSHA:  gitUpdateSHA(t, fx.repo, "refs/heads/main"),
		ExpectedOriginSHA: gitUpdateSHA(t, fx.bare, "refs/heads/main"),
		CheckoutHeadRef:   "refs/heads/work",
		CheckoutHeadSHA:   head,
	}
}

func TestUpdateSourceFromOriginFastForwardsUnoccupiedBranch(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	keepSHA := func() string {
		runGitUpdateTest(t, fx.repo, "branch", "keep")
		return gitUpdateSHA(t, fx.repo, "refs/heads/keep")
	}()
	workSHA := gitUpdateSHA(t, fx.repo, "refs/heads/work")

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateUpdated {
		t.Fatalf("result = %q reason = %q; want updated", outcome.Result, outcome.Reason)
	}
	if outcome.PreviousSHA != expected.ExpectedLocalSHA || outcome.LocalSHA != expected.ExpectedOriginSHA {
		t.Fatalf("outcome SHAs = previous %s local %s; want %s -> %s", outcome.PreviousSHA, outcome.LocalSHA, expected.ExpectedLocalSHA, expected.ExpectedOriginSHA)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedOriginSHA {
		t.Fatalf("refs/heads/main = %s; want the fetched origin tip %s", got, expected.ExpectedOriginSHA)
	}
	// Only the selected branch advances: the checkout, unrelated branches,
	// and the fetched tracking ref stay exact.
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/work"); got != workSHA {
		t.Fatalf("refs/heads/work moved to %s; want %s", got, workSHA)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/keep"); got != keepSHA {
		t.Fatalf("refs/heads/keep moved to %s; want %s", got, keepSHA)
	}
	if got := runGitUpdateTest(t, fx.repo, "symbolic-ref", "--quiet", "HEAD"); got != "refs/heads/work" {
		t.Fatalf("HEAD = %s; want refs/heads/work", got)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/remotes/origin/main"); got != expected.ExpectedOriginSHA {
		t.Fatalf("origin tracking ref = %s; want the fetched tip", got)
	}
}

func TestUpdateSourceFromOriginPreservesUnrelatedDirtyCheckoutState(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)

	tracked := filepath.Join(fx.repo, "README.md")
	if err := os.WriteFile(tracked, []byte("unstaged edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(fx.repo, "staged.txt")
	if err := os.WriteFile(staged, []byte("staged bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitUpdateTest(t, fx.repo, "add", "staged.txt")
	untracked := filepath.Join(fx.repo, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("untracked bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ignored := filepath.Join(fx.repo, "ignored.log")
	if err := os.WriteFile(ignored, []byte("ignored bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	statusBefore := runGitUpdateTest(t, fx.repo, "status", "--porcelain")
	indexBefore := runGitUpdateTest(t, fx.repo, "ls-files", "--stage")

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateUpdated {
		t.Fatalf("result = %q; want updated", outcome.Result)
	}
	if got := runGitUpdateTest(t, fx.repo, "status", "--porcelain"); got != statusBefore {
		t.Fatalf("status changed:\nbefore:\n%s\nafter:\n%s", statusBefore, got)
	}
	if got := runGitUpdateTest(t, fx.repo, "ls-files", "--stage"); got != indexBefore {
		t.Fatalf("index changed:\nbefore:\n%s\nafter:\n%s", indexBefore, got)
	}
	for path, want := range map[string]string{
		tracked:   "unstaged edit\n",
		staged:    "staged bytes\n",
		untracked: "untracked bytes\n",
		ignored:   "ignored bytes\n",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("working file %s = %q err=%v; want %q", path, got, err, want)
		}
	}
}

func TestUpdateSourceFromOriginEqualityReplayIsNoOpSuccess(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	if _, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("replay update: %v", err)
	}
	if outcome.Result != SourceUpdateAlreadyUpToDate {
		t.Fatalf("replay result = %q; want already_up_to_date", outcome.Result)
	}
	if outcome.LocalSHA != expected.ExpectedOriginSHA {
		t.Fatalf("replay local SHA = %s; want the unchanged origin tip", outcome.LocalSHA)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedOriginSHA {
		t.Fatalf("refs/heads/main = %s; want no movement", got)
	}
}

func TestUpdateSourceFromOriginStaleLocalTipRefusesWithFreshComparison(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	// A competing process advances the local branch to a commit origin
	// does not have before the update runs.
	runGitUpdateTest(t, fx.repo, "checkout", "main")
	runGitUpdateTest(t, fx.repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "competing local")
	runGitUpdateTest(t, fx.repo, "checkout", "work")
	competingTip := gitUpdateSHA(t, fx.repo, "refs/heads/main")

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonLocalTipChanged {
		t.Fatalf("result = %q reason = %q; want stale local_tip_changed", outcome.Result, outcome.Reason)
	}
	if outcome.Comparison == nil || outcome.Comparison.Status != OriginCheckDiverged {
		t.Fatalf("fresh comparison = %#v; want diverged against the competing tip", outcome.Comparison)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != competingTip {
		t.Fatalf("refs/heads/main = %s; want the competing value preserved", got)
	}
}

func TestUpdateSourceFromOriginStaleOriginTipRefusesWithFreshComparison(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	runGitUpdateTest(t, fx.writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote three")
	runGitUpdateTest(t, fx.writer, "push", "origin", "main")

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonOriginTipChanged {
		t.Fatalf("result = %q reason = %q; want stale origin_tip_changed", outcome.Result, outcome.Reason)
	}
	if outcome.Comparison == nil || outcome.Comparison.Status != OriginCheckBehind || outcome.Comparison.BehindCount != 3 {
		t.Fatalf("fresh comparison = %#v; want behind by 3", outcome.Comparison)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestUpdateSourceFromOriginDeletedOriginBranchRefuses(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	runGitUpdateTest(t, fx.bare, "update-ref", "-d", "refs/heads/main")

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonOriginBranchMissing {
		t.Fatalf("result = %q reason = %q; want stale origin_branch_missing", outcome.Result, outcome.Reason)
	}
	if !outcome.RemoteBranchMissing || outcome.Comparison != nil {
		t.Fatalf("outcome = %#v; want a proved-absent branch with no comparison", outcome)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the local tip preserved", got)
	}
}

func TestUpdateSourceFromOriginAheadAndDivergedRefuseWithoutRewinding(t *testing.T) {
	fx := newUpdateFixture(t)
	// Local-only commits on the target branch make it diverge from origin.
	runGitUpdateTest(t, fx.repo, "checkout", "main")
	runGitUpdateTest(t, fx.repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "local only")
	runGitUpdateTest(t, fx.repo, "checkout", "work")
	expected := fx.expectation(t)

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonNotFastForward {
		t.Fatalf("result = %q reason = %q; want stale not_fast_forward", outcome.Result, outcome.Reason)
	}
	if outcome.Comparison == nil || outcome.Comparison.Status != OriginCheckDiverged {
		t.Fatalf("comparison = %#v; want diverged", outcome.Comparison)
	}
	hasUpdateBlocker(t, outcome.Blockers, UpdateBlockerLocalNotBehind)
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != outcome.Comparison.LocalSHA {
		t.Fatalf("refs/heads/main = %s; want the local commits preserved", got)
	}
}

func TestUpdateSourceFromOriginLinkedWorktreeOccupancyRefusesWithGuidance(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGitUpdateTest(t, fx.repo, "worktree", "add", linked, "main")
	t.Cleanup(func() { runGitUpdateTest(t, fx.repo, "worktree", "remove", "--force", linked) })

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonBranchCheckedOut {
		t.Fatalf("result = %q reason = %q; want stale branch_checked_out", outcome.Result, outcome.Reason)
	}
	if len(outcome.CheckoutHolders) != 1 || !sameCheckoutPath(outcome.CheckoutHolders[0], linked) {
		t.Fatalf("holders = %v; want the linked worktree %s", outcome.CheckoutHolders, linked)
	}
	hasUpdateBlocker(t, outcome.Blockers, UpdateBlockerBranchCheckedOutInWorktree)
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestUpdateSourceFromOriginalCheckoutAdvancesBranchIndexAndFiles(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	expected := fx.originalExpectation(t, LocalSourceModeDefault)
	keepSHA := func() string {
		runGitUpdateTest(t, fx.repo, "branch", "keep")
		return gitUpdateSHA(t, fx.repo, "refs/heads/keep")
	}()

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateUpdated {
		t.Fatalf("result = %q reason = %q; want updated", outcome.Result, outcome.Reason)
	}
	if outcome.PreviousSHA != expected.ExpectedLocalSHA || outcome.LocalSHA != expected.ExpectedOriginSHA {
		t.Fatalf("outcome SHAs = previous %s local %s; want %s -> %s", outcome.PreviousSHA, outcome.LocalSHA, expected.ExpectedLocalSHA, expected.ExpectedOriginSHA)
	}
	// The branch, symbolic HEAD, index, and working files all advanced to
	// the fetched target.
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedOriginSHA {
		t.Fatalf("refs/heads/main = %s; want the fetched origin tip %s", got, expected.ExpectedOriginSHA)
	}
	if got := runGitUpdateTest(t, fx.repo, "symbolic-ref", "--quiet", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD = %s; want refs/heads/main", got)
	}
	if got := gitUpdateSHA(t, fx.repo, "HEAD^{commit}"); got != expected.ExpectedOriginSHA {
		t.Fatalf("HEAD commit = %s; want the fetched origin tip", got)
	}
	if got := runGitUpdateTest(t, fx.repo, "status", "--porcelain"); got != "" {
		t.Fatalf("status --porcelain = %q; want a clean checkout", got)
	}
	changed, err := os.ReadFile(filepath.Join(fx.repo, "README.md"))
	if err != nil || string(changed) != "remote edit\n" {
		t.Fatalf("README.md = %q err=%v; want the target content", changed, err)
	}
	added, err := os.ReadFile(filepath.Join(fx.repo, "added.txt"))
	if err != nil || string(added) != "added remotely\n" {
		t.Fatalf("added.txt = %q err=%v; want the target content", added, err)
	}
	stage := runGitUpdateTest(t, fx.repo, "ls-files", "--stage")
	if !strings.Contains(stage, "added.txt") {
		t.Fatalf("ls-files --stage missing the incoming file:\n%s", stage)
	}
	// Unrelated refs, the checkout's branch identity, and the origin
	// configuration are preserved; the fetch refreshed the tracking ref.
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/keep"); got != keepSHA {
		t.Fatalf("refs/heads/keep moved to %s; want %s", got, keepSHA)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/remotes/origin/main"); got != expected.ExpectedOriginSHA {
		t.Fatalf("origin tracking ref = %s; want the fetched tip", got)
	}
	if got := runGitUpdateTest(t, fx.repo, "remote", "get-url", "origin"); got != fx.bare {
		t.Fatalf("origin url = %s; want %s", got, fx.bare)
	}
}

func TestUpdateSourceFromOriginCheckoutSwitchRefuses(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	runGitUpdateTest(t, fx.repo, "checkout", "-b", "observer")

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonCheckoutChanged {
		t.Fatalf("result = %q reason = %q; want stale checkout_changed", outcome.Result, outcome.Reason)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestUpdateSourceFromOriginChangedMappingRefuses(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	// The branch is reconfigured to track a non-origin remote after display.
	runGitUpdateTest(t, fx.repo, "remote", "add", "upstream", fx.bare)
	runGitUpdateTest(t, fx.repo, "config", "branch.main.remote", "upstream")

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonMappingChanged {
		t.Fatalf("result = %q reason = %q; want stale mapping_changed", outcome.Result, outcome.Reason)
	}
	if outcome.Plan.Status != OriginCheckOtherUpstream {
		t.Fatalf("fresh plan status = %q; want other_upstream", outcome.Plan.Status)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestUpdateSourceFromOriginDetachedSourceRefuses(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	expected.Mode = LocalSourceModeCurrent
	runGitUpdateTest(t, fx.repo, "checkout", "--detach", "HEAD")

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonSourceChanged {
		t.Fatalf("result = %q reason = %q; want stale source_changed", outcome.Result, outcome.Reason)
	}
	if outcome.Plan.Status != OriginCheckDetached {
		t.Fatalf("fresh plan status = %q; want detached", outcome.Plan.Status)
	}
}

func TestUpdateSourceFromOriginAdvancesSlashBranchAndDifferentlyNamedMapping(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	bare := testutil.PairWithBareRemote(t, repo, "main", "release/base")
	runGitUpdateTest(t, repo, "checkout", "-b", "work")
	runGitUpdateTest(t, repo, "branch", "feature/base")
	runGitUpdateTest(t, repo, "push", "origin", "feature/base:refs/heads/feature/base")
	// The default source resolves through origin/HEAD to the slash branch,
	// and its configured upstream maps to the differently named origin
	// branch release/base.
	runGitUpdateTest(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/feature/base")
	runGitUpdateTest(t, repo, "config", "branch.feature/base.remote", "origin")
	runGitUpdateTest(t, repo, "config", "branch.feature/base.merge", "refs/heads/release/base")
	runGitUpdateTest(t, repo, "fetch", "origin")

	writer := filepath.Join(t.TempDir(), "writer")
	runGitUpdateTest(t, repo, "clone", bare, writer)
	runGitUpdateTest(t, writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote one")
	runGitUpdateTest(t, writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote two")
	runGitUpdateTest(t, writer, "push", "origin", "release/base")

	head := gitUpdateSHA(t, repo, "HEAD^{commit}")
	expected := SourceUpdateExpectation{
		Mode:              LocalSourceModeDefault,
		Branch:            "feature/base",
		OriginBranch:      "release/base",
		ExpectedLocalSHA:  gitUpdateSHA(t, repo, "refs/heads/feature/base"),
		ExpectedOriginSHA: gitUpdateSHA(t, bare, "refs/heads/release/base"),
		CheckoutHeadRef:   "refs/heads/work",
		CheckoutHeadSHA:   head,
	}
	outcome, err := UpdateSourceFromOrigin(context.Background(), repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateUpdated {
		t.Fatalf("result = %q reason = %q; want updated", outcome.Result, outcome.Reason)
	}
	if got := gitUpdateSHA(t, repo, "refs/heads/feature/base"); got != expected.ExpectedOriginSHA {
		t.Fatalf("refs/heads/feature/base = %s; want the fetched release/base tip", got)
	}
	if got := gitUpdateSHA(t, repo, "refs/remotes/origin/release/base"); got != expected.ExpectedOriginSHA {
		t.Fatalf("origin tracking ref = %s; want the fetched tip", got)
	}
	if got := runGitUpdateTest(t, repo, "symbolic-ref", "--quiet", "HEAD"); got != "refs/heads/work" {
		t.Fatalf("HEAD = %s; want refs/heads/work", got)
	}
}

func TestUpdateSourceFromOriginRefChangeBeforeCASRefuses(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	options := SourceUpdateOptions{BeforeCAS: func() {
		// A competing process advances the branch between validation and
		// the compare-and-swap.
		runGitUpdateTest(t, fx.repo, "update-ref", "refs/heads/main", expected.ExpectedOriginSHA)
	}}

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, options)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonLocalTipChanged {
		t.Fatalf("result = %q reason = %q; want stale local_tip_changed", outcome.Result, outcome.Reason)
	}
}

// racingRefRunner moves the ref after the final revalidation but before Git's
// expected-old-value check runs, so only Git's own CAS can refuse it.
type racingRefRunner struct {
	inner SourceUpdateRefRunner
	race  func()
	once  bool
}

func (r *racingRefRunner) Run(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) BranchProbeCommandResult {
	if !r.once {
		r.once = true
		r.race()
	}
	return r.inner.Run(ctx, repoPath, stdin, args, diagnosticLimit)
}

func TestUpdateSourceFromOriginCASOldValueCheckRefusesCompetingRef(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	options := SourceUpdateOptions{UpdateRefRunner: &racingRefRunner{
		inner: ExecSourceUpdateRefRunner{},
		race: func() {
			runGitUpdateTest(t, fx.repo, "update-ref", "refs/heads/main", expected.ExpectedOriginSHA)
		},
	}}

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, options)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonLocalTipChanged {
		t.Fatalf("result = %q reason = %q; want the CAS mismatch as stale local_tip_changed", outcome.Result, outcome.Reason)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedOriginSHA {
		t.Fatalf("refs/heads/main = %s; want the competing value untouched", got)
	}
}

func TestUpdateSourceFromOriginMembershipRaceBeforeCASRefuses(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	linked := filepath.Join(t.TempDir(), "linked")
	options := SourceUpdateOptions{BeforeCAS: func() {
		// An external process checks the branch out between validation and
		// the compare-and-swap.
		runGitUpdateTest(t, fx.repo, "worktree", "add", linked, "main")
	}}
	t.Cleanup(func() { runGitUpdateTest(t, fx.repo, "worktree", "remove", "--force", linked) })

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, options)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonBranchCheckedOut {
		t.Fatalf("result = %q reason = %q; want stale branch_checked_out from the pre-CAS recheck", outcome.Result, outcome.Reason)
	}
	hasUpdateBlocker(t, outcome.Blockers, UpdateBlockerBranchCheckedOutInWorktree)
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestUpdateSourceFromOriginRespectsExistingRefLocks(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	lockPath := filepath.Join(fx.repo, runGitUpdateTest(t, fx.repo, "rev-parse", "--git-path", "refs/heads/main.lock"))
	if err := os.WriteFile(lockPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	// An old lock is still never deleted.
	oldTime := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(lockPath) })

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	outcome, err := UpdateSourceFromOrigin(ctx, fx.repo, expected, SourceUpdateOptions{})
	if err == nil || !errors.Is(err, ErrSourceUpdateUnavailable) {
		t.Fatalf("err = %v outcome = %#v; want unavailability under lock contention", err, outcome)
	}
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Fatalf("lock file was removed: %v", statErr)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestUpdateSourceFromOriginDeadlineExpiresDuringFetch(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	options := SourceUpdateOptions{OriginCheckOptions: OriginCheckOptions{
		Runner: BranchProbeRunnerFunc(func(ctx context.Context, repoPath string, args []string, diagnosticLimit int) BranchProbeCommandResult {
			if len(args) > 0 && args[0] == "ls-remote" {
				<-ctx.Done()
				return BranchProbeCommandResult{ExitCode: -1, Err: ctx.Err(), Diagnostics: "hanging origin probe"}
			}
			return ExecBranchProbeRunner{}.Run(ctx, repoPath, args, diagnosticLimit)
		}),
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	outcome, err := UpdateSourceFromOrigin(ctx, fx.repo, expected, options)
	if err == nil || !errors.Is(err, ErrSourceUpdateUnavailable) {
		t.Fatalf("err = %v outcome = %#v; want unavailability on deadline expiry", err, outcome)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestUpdateSourceFromOriginMalformedExpectationFailsClosed(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	expected.ExpectedOriginSHA = "short"

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err == nil || !errors.Is(err, ErrSourceUpdateUnavailable) {
		t.Fatalf("err = %v outcome = %#v; want unavailability for a malformed expectation", err, outcome)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestUpdateSourceFromOriginFailedMembershipProbeFailsClosed(t *testing.T) {
	fx := newUpdateFixture(t)
	expected := fx.expectation(t)
	options := SourceUpdateOptions{OriginCheckOptions: OriginCheckOptions{
		Runner: BranchProbeRunnerFunc(func(ctx context.Context, repoPath string, args []string, diagnosticLimit int) BranchProbeCommandResult {
			if len(args) >= 2 && args[0] == "worktree" && args[1] == "list" {
				return BranchProbeCommandResult{ExitCode: 128, Diagnostics: "worktree inspection failed"}
			}
			return ExecBranchProbeRunner{}.Run(ctx, repoPath, args, diagnosticLimit)
		}),
	}}

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, options)
	if err == nil || !errors.Is(err, ErrSourceUpdateUnavailable) {
		t.Fatalf("err = %v outcome = %#v; want unavailability when absence cannot be established", err, outcome)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestListWorktreeCheckoutsParsesAdministrativeAnnotations(t *testing.T) {
	fx := newUpdateFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGitUpdateTest(t, fx.repo, "worktree", "add", linked, "main")
	t.Cleanup(func() {
		runGitUpdateTest(t, fx.repo, "worktree", "unlock", linked)
		runGitUpdateTest(t, fx.repo, "worktree", "remove", "--force", linked)
	})
	runGitUpdateTest(t, fx.repo, "worktree", "lock", linked)

	checkouts, err := listWorktreeCheckouts(context.Background(), fx.repo, OriginCheckOptions{})
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	if len(checkouts) != 2 {
		t.Fatalf("checkouts = %d; want the original checkout and the locked linked worktree", len(checkouts))
	}
	holders := holdersOfBranch(checkouts, "main")
	if len(holders) != 1 || !sameCheckoutPath(holders[0], linked) {
		t.Fatalf("holders = %v; want only the linked worktree", holders)
	}
}
