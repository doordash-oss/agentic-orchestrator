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
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// These tests fork real git and follow the source-update convention of not
// using t.Parallel(): the race-detector binary misbehaves when many
// fork-heavy tests run at once.

// originalCheckoutFixture is one repository whose original checkout holds
// its default branch main, which is clean and behind its bare origin by two
// commits with real file changes.
type originalCheckoutFixture struct {
	repo   string
	bare   string
	writer string
}

func newOriginalCheckoutFixture(t *testing.T) *originalCheckoutFixture {
	t.Helper()
	repo := testutil.InitGitRepo(t)
	bare := testutil.PairWithBareRemote(t, repo, "main", "main")
	runGitUpdateTest(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	writer := filepath.Join(t.TempDir(), "writer")
	runGitUpdateTest(t, repo, "clone", bare, writer)
	if err := os.WriteFile(filepath.Join(writer, "README.md"), []byte("remote edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(writer, "added.txt"), []byte("added remotely\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitUpdateTest(t, writer, "add", "-A")
	runGitUpdateTest(t, writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "remote one")
	runGitUpdateTest(t, writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote two")
	runGitUpdateTest(t, writer, "push", "origin", "main")
	return &originalCheckoutFixture{repo: repo, bare: bare, writer: writer}
}

// originalExpectation binds the displayed expectations for the fixture's
// checked-out default branch.
func (fx *originalCheckoutFixture) originalExpectation(t *testing.T, mode LocalSourceMode) SourceUpdateExpectation {
	t.Helper()
	return SourceUpdateExpectation{
		Mode:              mode,
		Branch:            "main",
		OriginBranch:      "main",
		ExpectedLocalSHA:  gitUpdateSHA(t, fx.repo, "refs/heads/main"),
		ExpectedOriginSHA: gitUpdateSHA(t, fx.bare, "refs/heads/main"),
		CheckoutHeadRef:   "refs/heads/main",
		CheckoutHeadSHA:   gitUpdateSHA(t, fx.repo, "HEAD^{commit}"),
	}
}

// advanceToOrigin performs the fixture's expected update and fails the test
// if it does not succeed.
func (fx *originalCheckoutFixture) advanceToOrigin(t *testing.T, mode LocalSourceMode) SourceUpdateExpectation {
	t.Helper()
	expected := fx.originalExpectation(t, mode)
	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateUpdated {
		t.Fatalf("result = %q reason = %q; want updated", outcome.Result, outcome.Reason)
	}
	return expected
}

func TestUpdateSourceFromOriginalCheckoutCurrentModeAdvancesCheckout(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	expected := fx.originalExpectation(t, LocalSourceModeCurrent)

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateUpdated {
		t.Fatalf("result = %q reason = %q; want updated", outcome.Result, outcome.Reason)
	}
	if got := gitUpdateSHA(t, fx.repo, "HEAD^{commit}"); got != expected.ExpectedOriginSHA {
		t.Fatalf("HEAD = %s; want the fetched origin tip %s", got, expected.ExpectedOriginSHA)
	}
	if got := runGitUpdateTest(t, fx.repo, "status", "--porcelain"); got != "" {
		t.Fatalf("status --porcelain = %q; want a clean checkout", got)
	}
}

func TestUpdateSourceFromOriginalCheckoutSlashBranchAndDifferentlyNamedMapping(t *testing.T) {
	repo := testutil.InitGitRepo(t)
	runGitUpdateTest(t, repo, "checkout", "-b", "feature/base")
	bare := testutil.PairWithBareRemote(t, repo, "feature/base", "release/base")
	runGitUpdateTest(t, repo, "config", "branch.feature/base.remote", "origin")
	runGitUpdateTest(t, repo, "config", "branch.feature/base.merge", "refs/heads/release/base")
	runGitUpdateTest(t, repo, "fetch", "origin")

	writer := filepath.Join(t.TempDir(), "writer")
	runGitUpdateTest(t, repo, "clone", bare, writer)
	if err := os.WriteFile(filepath.Join(writer, "README.md"), []byte("remote edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitUpdateTest(t, writer, "add", "-A")
	runGitUpdateTest(t, writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "remote edit")
	runGitUpdateTest(t, writer, "push", "origin", "release/base")

	expected := SourceUpdateExpectation{
		Mode:              LocalSourceModeCurrent,
		Branch:            "feature/base",
		OriginBranch:      "release/base",
		ExpectedLocalSHA:  gitUpdateSHA(t, repo, "refs/heads/feature/base"),
		ExpectedOriginSHA: gitUpdateSHA(t, bare, "refs/heads/release/base"),
		CheckoutHeadRef:   "refs/heads/feature/base",
		CheckoutHeadSHA:   gitUpdateSHA(t, repo, "HEAD^{commit}"),
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
	if got := runGitUpdateTest(t, repo, "symbolic-ref", "--quiet", "HEAD"); got != "refs/heads/feature/base" {
		t.Fatalf("HEAD = %s; want refs/heads/feature/base", got)
	}
	if got := runGitUpdateTest(t, repo, "status", "--porcelain"); got != "" {
		t.Fatalf("status --porcelain = %q; want a clean checkout", got)
	}
}

func TestUpdateSourceFromOriginalCheckoutEqualityReplayIsNoOpSuccess(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	expected := fx.advanceToOrigin(t, LocalSourceModeDefault)

	// The replay carries the SAME displayed expectations, including the
	// pre-update checkout HEAD: the completed update's HEAD advancement is
	// recognized as this update's own replay, not an unrelated switch.
	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("replay update: %v", err)
	}
	if outcome.Result != SourceUpdateAlreadyUpToDate {
		t.Fatalf("replay result = %q reason = %q; want already_up_to_date", outcome.Result, outcome.Reason)
	}
	if outcome.LocalSHA != expected.ExpectedOriginSHA {
		t.Fatalf("replay local SHA = %s; want the unchanged origin tip", outcome.LocalSHA)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedOriginSHA {
		t.Fatalf("refs/heads/main = %s; want no movement", got)
	}
}

func TestUpdateSourceFromOriginalCheckoutDirtyStatesRefuseAndPreserveBytes(t *testing.T) {
	for name, dirty := range map[string]func(t *testing.T, repo string){
		"staged": func(t *testing.T, repo string) {
			if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGitUpdateTest(t, repo, "add", "staged.txt")
		},
		"unstaged": func(t *testing.T, repo string) {
			if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("unstaged edit\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"untracked": func(t *testing.T, repo string) {
			if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	} {
		fx := newOriginalCheckoutFixture(t)
		expected := fx.originalExpectation(t, LocalSourceModeDefault)
		dirty(t, fx.repo)
		statusBefore := runGitUpdateTest(t, fx.repo, "status", "--porcelain")

		outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
		if err != nil {
			t.Fatalf("%s: update: %v", name, err)
		}
		if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonDirtyCheckout {
			t.Fatalf("%s: result = %q reason = %q; want stale dirty_checkout", name, outcome.Result, outcome.Reason)
		}
		hasUpdateBlocker(t, outcome.Blockers, UpdateBlockerDirtyTargetCheckout)
		if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
			t.Fatalf("%s: refs/heads/main = %s; want the displayed tip preserved", name, got)
		}
		if got := runGitUpdateTest(t, fx.repo, "status", "--porcelain"); got != statusBefore {
			t.Fatalf("%s: local state changed:\nbefore:\n%s\nafter:\n%s", name, statusBefore, got)
		}
	}
}

func TestUpdateSourceFromOriginalCheckoutIgnoredCollisionsRefuse(t *testing.T) {
	for name, shape := range map[string]func(t *testing.T, fx *originalCheckoutFixture){
		// An ignored file sits exactly at an incoming tracked path.
		"ignored file at incoming path": func(t *testing.T, fx *originalCheckoutFixture) {
			if err := os.WriteFile(filepath.Join(fx.repo, "added.txt"), []byte("precious\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		// An ignored FILE occupies a name the incoming tree needs as a
		// directory.
		"ignored file where a directory is incoming": func(t *testing.T, fx *originalCheckoutFixture) {
			if err := os.WriteFile(filepath.Join(fx.repo, "nested"), []byte("precious\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		// An ignored symlink occupies a name the incoming tree needs as a
		// directory.
		"ignored symlink where a directory is incoming": func(t *testing.T, fx *originalCheckoutFixture) {
			if err := os.Symlink(t.TempDir(), filepath.Join(fx.repo, "nested")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		fx := newOriginalCheckoutFixture(t)
		expected := fx.originalExpectation(t, LocalSourceModeDefault)
		// The remote tree adds nested/inner.txt so the ignored occupants
		// above collide with incoming directories, and .gitignore ignores
		// them all.
		if err := os.MkdirAll(filepath.Join(fx.writer, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fx.writer, "nested", "inner.txt"), []byte("inner\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitUpdateTest(t, fx.writer, "add", "-A")
		runGitUpdateTest(t, fx.writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "nested add")
		runGitUpdateTest(t, fx.writer, "push", "origin", "main")
		excludes := filepath.Join(t.TempDir(), "excludes")
		if err := os.WriteFile(excludes, []byte("added.txt\nnested\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitUpdateTest(t, fx.repo, "config", "core.excludesFile", excludes)
		shape(t, fx)
		expected.ExpectedOriginSHA = gitUpdateSHA(t, fx.bare, "refs/heads/main")
		ignoredBefore := readCollateralBytes(t, fx.repo)

		outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
		if err != nil {
			t.Fatalf("%s: update: %v", name, err)
		}
		if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonIgnoredPathCollision {
			t.Fatalf("%s: result = %q reason = %q; want stale ignored_path_collision", name, outcome.Result, outcome.Reason)
		}
		hasUpdateBlocker(t, outcome.Blockers, UpdateBlockerIgnoredPathCollision)
		if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
			t.Fatalf("%s: refs/heads/main = %s; want the displayed tip preserved", name, got)
		}
		assertCollateralBytesUnchanged(t, fx.repo, ignoredBefore)
	}
}

func TestUpdateSourceFromOriginalCheckoutHarmlessIgnoredContentRemains(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	expected := fx.originalExpectation(t, LocalSourceModeDefault)
	// Ignored content that no incoming path touches stays allowed and is
	// never rewritten.
	excludes := filepath.Join(t.TempDir(), "excludes")
	if err := os.WriteFile(excludes, []byte("debug.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitUpdateTest(t, fx.repo, "config", "core.excludesFile", excludes)
	if err := os.WriteFile(filepath.Join(fx.repo, "debug.log"), []byte("precious\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateUpdated {
		t.Fatalf("result = %q reason = %q; want updated", outcome.Result, outcome.Reason)
	}
	got, err := os.ReadFile(filepath.Join(fx.repo, "debug.log"))
	if err != nil || string(got) != "precious\n" {
		t.Fatalf("debug.log = %q err=%v; want the ignored content unchanged", got, err)
	}
}

func TestUpdateSourceFromOriginalCheckoutOperationInProgressRefuses(t *testing.T) {
	for name, marker := range map[string]string{
		"merge":           "MERGE_HEAD",
		"cherry-pick":     "CHERRY_PICK_HEAD",
		"revert":          "REVERT_HEAD",
		"rebase sequence": "sequencer",
	} {
		fx := newOriginalCheckoutFixture(t)
		expected := fx.originalExpectation(t, LocalSourceModeDefault)
		gitDir := runGitUpdateTest(t, fx.repo, "rev-parse", "--git-dir")
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(fx.repo, gitDir)
		}
		markerPath := filepath.Join(gitDir, marker)
		if marker == "sequencer" {
			if err := os.Mkdir(markerPath, 0o755); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(markerPath, []byte("marker\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
		if err != nil {
			t.Fatalf("%s: update: %v", name, err)
		}
		if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonCheckoutOperationInProgress {
			t.Fatalf("%s: result = %q reason = %q; want stale checkout_operation_in_progress", name, outcome.Result, outcome.Reason)
		}
		hasUpdateBlocker(t, outcome.Blockers, UpdateBlockerCheckoutOperationInProgress)
		if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
			t.Fatalf("%s: refs/heads/main = %s; want the displayed tip preserved", name, got)
		}
	}
}

func TestUpdateSourceFromOriginalCheckoutHooksAndAutostashConfigCannotInterfere(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	expected := fx.originalExpectation(t, LocalSourceModeDefault)
	hookMarker := filepath.Join(t.TempDir(), "hook-ran")
	hooksDir := filepath.Join(fx.repo, ".githooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\ntouch " + hookMarker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitUpdateTest(t, fx.repo, "config", "core.hooksPath", ".githooks")
	runGitUpdateTest(t, fx.repo, "config", "merge.autoStash", "true")
	// The hook directory itself is untracked, which the clean-tree gate
	// would refuse; ignore it so the fixture isolates hook behavior.
	excludes := filepath.Join(t.TempDir(), "excludes")
	if err := os.WriteFile(excludes, []byte(".githooks/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitUpdateTest(t, fx.repo, "config", "core.excludesFile", excludes)

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateUpdated {
		t.Fatalf("result = %q reason = %q; want updated", outcome.Result, outcome.Reason)
	}
	if _, err := os.Stat(hookMarker); err == nil {
		t.Fatalf("a hook ran during the fast-forward")
	}
	if got := runGitUpdateTest(t, fx.repo, "status", "--porcelain"); got != "" {
		t.Fatalf("status --porcelain = %q; want a clean checkout (no autostash side effects)", got)
	}
}

func TestUpdateSourceFromOriginalCheckoutAheadAndDivergedRefuse(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	runGitUpdateTest(t, fx.repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "local only")
	expected := fx.originalExpectation(t, LocalSourceModeDefault)

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonNotFastForward {
		t.Fatalf("result = %q reason = %q; want stale not_fast_forward", outcome.Result, outcome.Reason)
	}
	hasUpdateBlocker(t, outcome.Blockers, UpdateBlockerLocalNotBehind)
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the local commits preserved", got)
	}
}

func TestUpdateSourceFromOriginalCheckoutStaleExpectationsRefuse(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)

	// A competing process advances the local branch to a commit origin does
	// not have after display.
	expected := fx.originalExpectation(t, LocalSourceModeDefault)
	runGitUpdateTest(t, fx.repo, "fetch", "origin")
	competing := runGitUpdateTest(t, fx.repo, "commit-tree", "refs/heads/main^{tree}", "-m", "competing local")
	runGitUpdateTest(t, fx.repo, "update-ref", "refs/heads/main", competing)
	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("local tip race: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonLocalTipChanged {
		t.Fatalf("local tip race: result = %q reason = %q; want stale local_tip_changed", outcome.Result, outcome.Reason)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != competing {
		t.Fatalf("refs/heads/main = %s; want the competing value preserved", got)
	}

	// The origin tip moves after display.
	expected = fx.originalExpectation(t, LocalSourceModeDefault)
	runGitUpdateTest(t, fx.writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote three")
	runGitUpdateTest(t, fx.writer, "push", "origin", "main")
	outcome, err = UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("origin tip race: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonOriginTipChanged {
		t.Fatalf("origin tip race: result = %q reason = %q; want stale origin_tip_changed", outcome.Result, outcome.Reason)
	}

	// The checkout switches away from the branch after display: the update
	// never switches a checkout to make the branch available.
	expected = fx.originalExpectation(t, LocalSourceModeDefault)
	runGitUpdateTest(t, fx.repo, "checkout", "-b", "elsewhere")
	outcome, err = UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("checkout switch: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonCheckoutChanged {
		t.Fatalf("checkout switch: result = %q reason = %q; want stale checkout_changed", outcome.Result, outcome.Reason)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestUpdateSourceFromOriginalCheckoutMembershipRaceBeforeMutationRefuses(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	expected := fx.originalExpectation(t, LocalSourceModeDefault)
	options := SourceUpdateOptions{BeforeCAS: func() {
		// An external process switches the original checkout between the
		// final validation and the mutation.
		runGitUpdateTest(t, fx.repo, "checkout", "-b", "elsewhere")
	}}

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, options)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonCheckoutChanged {
		t.Fatalf("result = %q reason = %q; want stale checkout_changed from the pre-mutation recheck", outcome.Result, outcome.Reason)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestUpdateSourceFromOriginalCheckoutLocalEditRaceBeforeMutationRefuses(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	expected := fx.originalExpectation(t, LocalSourceModeDefault)
	options := SourceUpdateOptions{BeforeCAS: func() {
		// A local untracked file appears between the final validation and
		// the mutation: the safety recheck refuses it.
		if err := os.WriteFile(filepath.Join(fx.repo, "added.txt"), []byte("racing\n"), 0o644); err != nil {
			t.Error(err)
		}
	}}

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, options)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonDirtyCheckout {
		t.Fatalf("result = %q reason = %q; want stale dirty_checkout from the pre-mutation recheck", outcome.Result, outcome.Reason)
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

// racingCheckoutRunner injects a change between the final revalidation and
// the fast-forward itself, so only Git's own working-tree protection can
// refuse it.
type racingCheckoutRunner struct {
	inner SourceUpdateRefRunner
	race  func()
	once  bool
}

func (r *racingCheckoutRunner) Run(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) BranchProbeCommandResult {
	if !r.once {
		r.once = true
		for _, arg := range args {
			if arg == "merge" {
				r.race()
				break
			}
		}
	}
	return r.inner.Run(ctx, repoPath, stdin, args, diagnosticLimit)
}

func TestUpdateSourceFromOriginalCheckoutBoundaryRaceRefusesWithoutOverwriting(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		name := "untracked"
		if ignored {
			name = "ignored"
		}
		t.Run(name, func(t *testing.T) {
			fx := newOriginalCheckoutFixture(t)
			expected := fx.originalExpectation(t, LocalSourceModeDefault)
			if ignored {
				if err := os.WriteFile(filepath.Join(fx.repo, ".git", "info", "exclude"), []byte("added.txt\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			options := SourceUpdateOptions{UpdateRefRunner: &racingCheckoutRunner{
				inner: ExecSourceUpdateRefRunner{},
				race: func() {
					// A local file lands on an incoming path between the final
					// revalidation and the fast-forward: Git's own boundary refuses
					// to overwrite it, and the refusal is proved untouched.
					if err := os.WriteFile(filepath.Join(fx.repo, "added.txt"), []byte("precious\n"), 0o644); err != nil {
						t.Error(err)
					}
				},
			}}

			outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, options)
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if outcome.Result != SourceUpdateStale || outcome.Reason != SourceUpdateReasonCheckoutConflict {
				t.Fatalf("result = %q reason = %q; want stale checkout_conflict", outcome.Result, outcome.Reason)
			}
			if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
				t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
			}
			got, err := os.ReadFile(filepath.Join(fx.repo, "added.txt"))
			if err != nil || string(got) != "precious\n" {
				t.Fatalf("added.txt = %q err=%v; want the racing content untouched", got, err)
			}
		})
	}
}

func TestUpdateSourceFromOriginalCheckoutRespectsExistingIndexLocks(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	expected := fx.originalExpectation(t, LocalSourceModeDefault)
	lockPath := filepath.Join(fx.repo, runGitUpdateTest(t, fx.repo, "rev-parse", "--git-path", "index.lock"))
	if err := os.WriteFile(lockPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
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

// hangingMergeRunner hangs the fast-forward command until its context ends,
// so the mutation boundary is interrupted and its outcome must stay unknown.
type hangingMergeRunner struct{}

func (hangingMergeRunner) Run(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) BranchProbeCommandResult {
	for _, arg := range args {
		if arg == "merge" {
			<-ctx.Done()
			return BranchProbeCommandResult{ExitCode: -1, Err: ctx.Err(), Diagnostics: "fast-forward interrupted"}
		}
	}
	return ExecSourceUpdateRefRunner{}.Run(ctx, repoPath, stdin, args, diagnosticLimit)
}

func TestUpdateSourceFromOriginalCheckoutDeadlineDuringMergeIsUnknown(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	expected := fx.originalExpectation(t, LocalSourceModeDefault)
	options := SourceUpdateOptions{UpdateRefRunner: hangingMergeRunner{}}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	outcome, err := UpdateSourceFromOrigin(ctx, fx.repo, expected, options)
	if err == nil || !errors.Is(err, ErrSourceUpdateUnavailable) {
		t.Fatalf("err = %v outcome = %#v; want unavailability, never a claimed refusal or rollback", err, outcome)
	}
}

func TestUpdateSourceFromOriginalCheckoutInspectionFailuresFailClosed(t *testing.T) {
	failingCommand := func(match func(args []string) bool) SourceUpdateOptions {
		return SourceUpdateOptions{OriginCheckOptions: OriginCheckOptions{
			Runner: BranchProbeRunnerFunc(func(ctx context.Context, repoPath string, args []string, diagnosticLimit int) BranchProbeCommandResult {
				if match(args) {
					return BranchProbeCommandResult{ExitCode: 128, Diagnostics: "checkout inspection failed"}
				}
				return ExecBranchProbeRunner{}.Run(ctx, repoPath, args, diagnosticLimit)
			}),
		}}
	}
	for name, options := range map[string]SourceUpdateOptions{
		"status": failingCommand(func(args []string) bool {
			return len(args) > 0 && args[0] == "status"
		}),
		"git dir": failingCommand(func(args []string) bool {
			return len(args) > 1 && args[0] == "rev-parse" && args[1] == "--git-dir"
		}),
		"worktree list": failingCommand(func(args []string) bool {
			return len(args) > 1 && args[0] == "worktree" && args[1] == "list"
		}),
	} {
		fx := newOriginalCheckoutFixture(t)
		expected := fx.originalExpectation(t, LocalSourceModeDefault)
		outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, options)
		if err == nil || !errors.Is(err, ErrSourceUpdateUnavailable) {
			t.Fatalf("%s: err = %v outcome = %#v; want unavailability when safety cannot be proved", name, err, outcome)
		}
		if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
			t.Fatalf("%s: refs/heads/main = %s; want the displayed tip preserved", name, got)
		}
	}
}

func TestUpdateSourceFromOriginalCheckoutUnrelatedLinkedWorktreeDoesNotBlock(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	expected := fx.originalExpectation(t, LocalSourceModeDefault)
	runGitUpdateTest(t, fx.repo, "branch", "topic")
	linked := filepath.Join(t.TempDir(), "linked")
	runGitUpdateTest(t, fx.repo, "worktree", "add", linked, "topic")
	t.Cleanup(func() { runGitUpdateTest(t, fx.repo, "worktree", "remove", "--force", linked) })

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if outcome.Result != SourceUpdateUpdated {
		t.Fatalf("result = %q reason = %q holders = %v; want updated despite the unrelated worktree", outcome.Result, outcome.Reason, outcome.CheckoutHolders)
	}
}

// readCollateralBytes snapshots the ignored-collision fixture's occupant
// files so refusals can prove byte preservation.
func readCollateralBytes(t *testing.T, repo string) map[string]string {
	t.Helper()
	collateral := map[string]string{}
	for _, name := range []string{"added.txt", "nested"} {
		path := filepath.Join(repo, name)
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				t.Fatal(err)
			}
			collateral[name] = "symlink:" + target
			continue
		}
		if info.IsDir() {
			collateral[name] = "dir"
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		collateral[name] = string(content)
	}
	return collateral
}

func assertCollateralBytesUnchanged(t *testing.T, repo string, before map[string]string) {
	t.Helper()
	after := readCollateralBytes(t, repo)
	for name, want := range before {
		if after[name] != want {
			t.Fatalf("collateral %s = %q; want %q", name, after[name], want)
		}
	}
}
