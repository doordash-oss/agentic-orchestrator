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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func refExists(t *testing.T, dir, ref string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", ref)
	cmd.Env = testutil.GitTestEnv()
	return cmd.Run() == nil
}

func TestPlanOriginCheckResolvesConfiguredOriginTracking(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	testutil.PairWithBareRemote(t, repo, "main", "main")

	plan := PlanOriginCheck(context.Background(), repo, LocalSourceModeDefault, OriginCheckOptions{})
	if plan.Status != "" || plan.Mapping == nil || plan.Mapping.Branch != "main" {
		t.Fatalf("plan = status %q mapping %+v; want empty status with origin/main mapping", plan.Status, plan.Mapping)
	}
	if plan.Source.Kind != LocalSourceBranch || plan.Source.Branch != "main" {
		t.Fatalf("plan source = %#v; want branch main", plan.Source)
	}
}

func TestPlanOriginCheckKeepsDifferentlyNamedMappingWithoutCachedRef(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	testutil.PairWithBareRemote(t, repo, "main", "main")
	runGit(t, repo, "branch", "feature/one")
	runGit(t, repo, "checkout", "feature/one")
	runGit(t, repo, "config", "branch.feature/one.remote", "origin")
	runGit(t, repo, "config", "branch.feature/one.merge", "refs/heads/upstream-main")

	plan := PlanOriginCheck(context.Background(), repo, LocalSourceModeCurrent, OriginCheckOptions{})
	if plan.Status != "" || plan.Mapping == nil || plan.Mapping.Branch != "upstream-main" {
		t.Fatalf("plan = status %q mapping %+v; want empty status with upstream-main mapping", plan.Status, plan.Mapping)
	}
	if plan.Source.Branch != "feature/one" {
		t.Fatalf("plan source branch = %q; want feature/one", plan.Source.Branch)
	}
}

func TestPlanOriginCheckSameNameFallbackWithoutUpstream(t *testing.T) {
	t.Parallel()

	repo := testutil.InitGitRepo(t)
	testutil.PairWithBareRemote(t, repo, "main", "main")
	runGit(t, repo, "config", "--unset", "branch.main.remote")
	runGit(t, repo, "config", "--unset", "branch.main.merge")

	plan := PlanOriginCheck(context.Background(), repo, LocalSourceModeDefault, OriginCheckOptions{})
	if plan.Status != "" || plan.Mapping == nil || plan.Mapping.Branch != "main" {
		t.Fatalf("plan = status %q mapping %+v; want same-name origin fallback", plan.Status, plan.Mapping)
	}
}

func TestPlanOriginCheckLocalOnlyOutcomes(t *testing.T) {
	t.Parallel()

	t.Run("no origin remote", func(t *testing.T) {
		t.Parallel()
		repo := testutil.InitGitRepo(t)
		plan := PlanOriginCheck(context.Background(), repo, LocalSourceModeDefault, OriginCheckOptions{})
		if plan.Status != OriginCheckNoOrigin || plan.Mapping != nil {
			t.Fatalf("plan = status %q mapping %+v; want no_origin without mapping", plan.Status, plan.Mapping)
		}
	})

	t.Run("other remote upstream", func(t *testing.T) {
		t.Parallel()
		repo := testutil.InitGitRepo(t)
		other := t.TempDir()
		runGit(t, repo, "init", "--bare", other)
		runGit(t, repo, "remote", "add", "upstream", other)
		runGit(t, repo, "config", "branch.main.remote", "upstream")
		runGit(t, repo, "config", "branch.main.merge", "refs/heads/main")
		plan := PlanOriginCheck(context.Background(), repo, LocalSourceModeDefault, OriginCheckOptions{})
		if plan.Status != OriginCheckOtherUpstream || plan.Mapping != nil {
			t.Fatalf("plan = status %q mapping %+v; want other_upstream without substituting origin", plan.Status, plan.Mapping)
		}
	})

	t.Run("local upstream tracking", func(t *testing.T) {
		t.Parallel()
		repo := testutil.InitGitRepo(t)
		runGit(t, repo, "branch", "trunk")
		runGit(t, repo, "config", "branch.main.remote", ".")
		runGit(t, repo, "config", "branch.main.merge", "refs/heads/trunk")
		plan := PlanOriginCheck(context.Background(), repo, LocalSourceModeDefault, OriginCheckOptions{})
		if plan.Status != OriginCheckOtherUpstream || plan.Mapping != nil {
			t.Fatalf("plan = status %q mapping %+v; want other_upstream for local tracking", plan.Status, plan.Mapping)
		}
	})

	t.Run("malformed merge ref fails safely", func(t *testing.T) {
		t.Parallel()
		repo := testutil.InitGitRepo(t)
		testutil.PairWithBareRemote(t, repo, "main", "main")
		runGit(t, repo, "config", "branch.main.merge", "HEAD")
		plan := PlanOriginCheck(context.Background(), repo, LocalSourceModeDefault, OriginCheckOptions{})
		if plan.Status != OriginCheckUnknown || plan.Mapping != nil {
			t.Fatalf("plan = status %q mapping %+v; want unknown for malformed mapping", plan.Status, plan.Mapping)
		}
	})

	t.Run("detached source never fetches", func(t *testing.T) {
		t.Parallel()
		repo := testutil.InitGitRepo(t)
		testutil.PairWithBareRemote(t, repo, "main", "main")
		runGit(t, repo, "checkout", "--detach", "HEAD")
		plan := PlanOriginCheck(context.Background(), repo, LocalSourceModeCurrent, OriginCheckOptions{})
		if plan.Status != OriginCheckDetached || plan.Mapping != nil {
			t.Fatalf("plan = status %q mapping %+v; want detached without mapping", plan.Status, plan.Mapping)
		}
	})

	t.Run("unborn local source is local-base-missing", func(t *testing.T) {
		t.Parallel()
		repo := testutil.InitMinimalGitRepo(t)
		plan := PlanOriginCheck(context.Background(), repo, LocalSourceModeDefault, OriginCheckOptions{})
		if plan.Status != OriginCheckLocalBaseMissing {
			t.Fatalf("plan status = %q; want local_base_missing", plan.Status)
		}
	})

	t.Run("missing nominated local branch is local-base-missing without remote fallback", func(t *testing.T) {
		t.Parallel()
		repo := testutil.InitGitRepo(t)
		runGit(t, repo, "branch", "-m", "main", "trunk")
		runGit(t, repo, "update-ref", "refs/remotes/origin/main", "refs/heads/trunk")
		runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		plan := PlanOriginCheck(context.Background(), repo, LocalSourceModeDefault, OriginCheckOptions{})
		if plan.Status != OriginCheckLocalBaseMissing {
			t.Fatalf("plan status = %q; want local_base_missing", plan.Status)
		}
	})
}

func originFixture(t *testing.T) (repo, bare string) {
	t.Helper()
	repo = testutil.InitGitRepo(t)
	bare = testutil.PairWithBareRemote(t, repo, "main", "main")
	return repo, bare
}

func TestFetchOriginBranchFetchedAndFresh(t *testing.T) {
	t.Parallel()

	repo, bare := originFixture(t)
	mapping := OriginMapping{Branch: "main"}

	result := FetchOriginBranch(context.Background(), repo, mapping, OriginCheckOptions{})
	if result.State != FetchOriginFetched {
		t.Fatalf("state = %q diagnostics %q; want fetched", result.State, result.Diagnostics)
	}
	if want := gitOutput(t, bare, "rev-parse", "refs/heads/main"); result.SHA != want {
		t.Fatalf("SHA = %q; want remote sha %q", result.SHA, want)
	}

	// A force move on the remote must be reflected by a fresh attempt; a
	// prior cached tracking ref never establishes current success.
	testutil.CommitFile(t, repo, "second.txt", "second\n", "second")
	runGit(t, bare, "fetch", repo, "main:refs/heads/main")

	retry := FetchOriginBranch(context.Background(), repo, mapping, OriginCheckOptions{})
	if retry.State != FetchOriginFetched {
		t.Fatalf("retry state = %q diagnostics %q; want fetched", retry.State, retry.Diagnostics)
	}
	if want := gitOutput(t, bare, "rev-parse", "refs/heads/main"); retry.SHA != want {
		t.Fatalf("retry SHA = %q; want remote sha %q", retry.SHA, want)
	}
}

func TestFetchOriginBranchProvesRemoteDeletionDespiteCachedRef(t *testing.T) {
	t.Parallel()

	repo, bare := originFixture(t)
	runGit(t, bare, "update-ref", "-d", "refs/heads/main")

	if !refExists(t, repo, "refs/remotes/origin/main") {
		t.Fatal("fixture must retain the cached tracking ref before the check")
	}
	if result := FetchOriginBranch(context.Background(), repo, OriginMapping{Branch: "main"}, OriginCheckOptions{}); result.State != FetchOriginAbsent {
		t.Fatalf("state = %q diagnostics %q; want absent proved by current attempt", result.State, result.Diagnostics)
	}
}

func TestFetchOriginBranchUnavailableFailures(t *testing.T) {
	t.Parallel()

	t.Run("ls-remote failure is unavailable with sanitized diagnostics", func(t *testing.T) {
		t.Parallel()
		repo := testutil.InitGitRepo(t)
		runner := BranchProbeRunnerFunc(func(ctx context.Context, repoPath string, args []string, limit int) BranchProbeCommandResult {
			if len(args) > 0 && args[0] == "ls-remote" {
				return BranchProbeCommandResult{
					ExitCode:    128,
					Err:         errors.New("exit status 128"),
					Diagnostics: "\x1b[31mfatal: 'https://user:secret@example.com/repo' not found\x1b[0m",
				}
			}
			return ExecBranchProbeRunner{}.Run(ctx, repoPath, args, limit)
		})
		result := FetchOriginBranch(context.Background(), repo, OriginMapping{Branch: "main"}, OriginCheckOptions{Runner: runner})
		if result.State != FetchOriginUnavailable {
			t.Fatalf("state = %q; want unavailable", result.State)
		}
		if strings.Contains(result.Diagnostics, "secret") {
			t.Fatalf("diagnostics leaked credentials: %q", result.Diagnostics)
		}
		if strings.Contains(result.Diagnostics, "\x1b") {
			t.Fatalf("diagnostics retained terminal controls: %q", result.Diagnostics)
		}
	})

	t.Run("fetch failure after proved existence is unavailable", func(t *testing.T) {
		t.Parallel()
		repo, _ := originFixture(t)
		runner := BranchProbeRunnerFunc(func(ctx context.Context, repoPath string, args []string, limit int) BranchProbeCommandResult {
			if len(args) > 0 && args[0] == "fetch" {
				return BranchProbeCommandResult{ExitCode: 128, Err: errors.New("exit status 128"), Diagnostics: "fatal: the remote end hung up unexpectedly"}
			}
			return ExecBranchProbeRunner{}.Run(ctx, repoPath, args, limit)
		})
		result := FetchOriginBranch(context.Background(), repo, OriginMapping{Branch: "main"}, OriginCheckOptions{Runner: runner})
		if result.State != FetchOriginUnavailable {
			t.Fatalf("state = %q; want unavailable, not absence", result.State)
		}
	})
}

func TestFetchOriginBranchCannotBeBroadenedByConfiguredRefspecs(t *testing.T) {
	t.Parallel()

	repo, bare := originFixture(t)
	// The remote carries a second branch that exists only there; a broadened
	// fetch would create it locally.
	runGit(t, bare, "fetch", repo, "main:refs/heads/remote-only")
	// A malicious fetch refspec targets local branches; tag following would
	// import the remote's tag.
	runGit(t, repo, "tag", "v1", "main")
	runGit(t, bare, "fetch", repo, "v1:refs/tags/v1")
	runGit(t, repo, "tag", "-d", "v1")
	runGit(t, repo, "config", "remote.origin.fetch", "+refs/heads/*:refs/heads/*")

	localRefsBefore := gitOutput(t, repo, "for-each-ref", "refs/heads", "--format=%(refname)")

	result := FetchOriginBranch(context.Background(), repo, OriginMapping{Branch: "main"}, OriginCheckOptions{})
	if result.State != FetchOriginFetched {
		t.Fatalf("state = %q diagnostics %q; want fetched", result.State, result.Diagnostics)
	}

	localRefsAfter := gitOutput(t, repo, "for-each-ref", "refs/heads", "--format=%(refname)")
	if localRefsAfter != localRefsBefore {
		t.Fatalf("local branches changed by check:\nbefore:\n%s\nafter:\n%s", localRefsBefore, localRefsAfter)
	}
	if refExists(t, repo, "refs/heads/remote-only") {
		t.Fatal("configured fetch mapping created a local branch")
	}
	if refExists(t, repo, "refs/tags/v1") {
		t.Fatal("tag following imported a remote tag")
	}
}

func TestCompareOriginSourceStates(t *testing.T) {
	t.Parallel()

	checkedAt := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	options := OriginCheckOptions{Now: func() time.Time { return checkedAt }}
	mapping := OriginMapping{Branch: "main"}

	t.Run("equal shas are up to date", func(t *testing.T) {
		t.Parallel()
		repo, _ := originFixture(t)
		sha := gitOutput(t, repo, "rev-parse", "refs/heads/main")
		comparison, err := CompareOriginSource(context.Background(), repo, sha, sha, mapping, options)
		if err != nil {
			t.Fatalf("CompareOriginSource: %v", err)
		}
		if comparison.Status != OriginCheckUpToDate || comparison.AheadCount != 0 || comparison.BehindCount != 0 {
			t.Fatalf("comparison = %#v; want up_to_date 0/0", comparison)
		}
		if !comparison.CheckedAt.Equal(checkedAt) {
			t.Fatalf("CheckedAt = %v; want %v", comparison.CheckedAt, checkedAt)
		}
	})

	t.Run("local ahead", func(t *testing.T) {
		t.Parallel()
		repo, bare := originFixture(t)
		local := testutil.CommitFile(t, repo, "local.txt", "local\n", "local")
		runGit(t, bare, "fetch", repo, "main:refs/heads/main")
		remote := gitOutput(t, bare, "rev-parse", "refs/heads/main~1")
		comparison, err := CompareOriginSource(context.Background(), repo, local, remote, mapping, options)
		if err != nil {
			t.Fatalf("CompareOriginSource: %v", err)
		}
		if comparison.Status != OriginCheckAhead || comparison.AheadCount != 1 || comparison.BehindCount != 0 {
			t.Fatalf("comparison = %#v; want ahead 1/0", comparison)
		}
	})

	t.Run("local behind", func(t *testing.T) {
		t.Parallel()
		repo, bare := originFixture(t)
		remote := testutil.CommitFile(t, repo, "remote.txt", "remote\n", "remote")
		runGit(t, bare, "fetch", repo, "main:refs/heads/main")
		runGit(t, repo, "reset", "--hard", "HEAD~1")
		local := gitOutput(t, repo, "rev-parse", "refs/heads/main")
		if local == remote {
			t.Fatal("fixture did not separate local and remote shas")
		}
		comparison, err := CompareOriginSource(context.Background(), repo, local, remote, mapping, options)
		if err != nil {
			t.Fatalf("CompareOriginSource: %v", err)
		}
		if comparison.Status != OriginCheckBehind || comparison.BehindCount != 1 || comparison.AheadCount != 0 {
			t.Fatalf("comparison = %#v; want behind 0/1", comparison)
		}
	})

	t.Run("diverged", func(t *testing.T) {
		t.Parallel()
		repo, bare := originFixture(t)
		base := gitOutput(t, repo, "rev-parse", "HEAD")
		runGit(t, repo, "checkout", "-b", "topic")
		testutil.CommitFile(t, repo, "local.txt", "local\n", "local")
		local := gitOutput(t, repo, "rev-parse", "refs/heads/topic")

		runGit(t, repo, "checkout", "main")
		runGit(t, repo, "reset", "--hard", base)
		testutil.CommitFile(t, repo, "remote.txt", "remote\n", "remote")
		remote := gitOutput(t, repo, "rev-parse", "HEAD")
		runGit(t, bare, "fetch", repo, "main:refs/heads/main")

		comparison, err := CompareOriginSource(context.Background(), repo, local, remote, OriginMapping{Branch: "topic"}, options)
		if err != nil {
			t.Fatalf("CompareOriginSource: %v", err)
		}
		if comparison.Status != OriginCheckDiverged || comparison.AheadCount != 1 || comparison.BehindCount != 1 {
			t.Fatalf("comparison = %#v; want diverged 1/1", comparison)
		}
	})
}

func TestProbeUpdateEligibility(t *testing.T) {
	t.Parallel()

	t.Run("unoccupied clean branch is eligible despite unrelated dirty files", func(t *testing.T) {
		t.Parallel()
		repo := testutil.InitGitRepo(t)
		runGit(t, repo, "branch", "topic")
		if err := os.WriteFile(filepath.Join(repo, "unrelated-dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		eligible, blockers := ProbeUpdateEligibility(context.Background(), repo, "topic", OriginCheckOptions{})
		if !eligible || len(blockers) != 0 {
			t.Fatalf("eligible = %v blockers = %v; want eligible with no blockers", eligible, blockers)
		}
	})

	t.Run("dirty checkout holding the branch is blocked", func(t *testing.T) {
		t.Parallel()
		repo := testutil.InitGitRepo(t)
		if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		eligible, blockers := ProbeUpdateEligibility(context.Background(), repo, "main", OriginCheckOptions{})
		if eligible || !containsBlocker(blockers, UpdateBlockerDirtyTargetCheckout) {
			t.Fatalf("eligible = %v blockers = %v; want dirty_target_checkout", eligible, blockers)
		}
	})

	t.Run("branch checked out in a linked worktree is blocked", func(t *testing.T) {
		t.Parallel()
		repo := testutil.InitGitRepo(t)
		runGit(t, repo, "branch", "topic")
		linked := filepath.Join(t.TempDir(), "linked")
		runGit(t, repo, "worktree", "add", linked, "topic")
		t.Cleanup(func() { runGit(t, repo, "worktree", "remove", "--force", linked) })
		eligible, blockers := ProbeUpdateEligibility(context.Background(), repo, "topic", OriginCheckOptions{})
		if eligible || !containsBlocker(blockers, UpdateBlockerBranchCheckedOutInWorktree) {
			t.Fatalf("eligible = %v blockers = %v; want branch_checked_out_in_worktree", eligible, blockers)
		}
	})

	t.Run("active git operation is blocked", func(t *testing.T) {
		t.Parallel()
		repo := testutil.InitGitRepo(t)
		runGit(t, repo, "branch", "topic")
		mu := worktreeMutationLock(repo)
		mu.Lock()
		defer mu.Unlock()
		eligible, blockers := ProbeUpdateEligibility(context.Background(), repo, "topic", OriginCheckOptions{})
		if eligible || !containsBlocker(blockers, UpdateBlockerGitOperationInProgress) {
			t.Fatalf("eligible = %v blockers = %v; want git_operation_in_progress", eligible, blockers)
		}
	})
}

func containsBlocker(blockers []UpdateBlocker, want UpdateBlocker) bool {
	for _, blocker := range blockers {
		if blocker == want {
			return true
		}
	}
	return false
}

// TestOriginCheckPreservesRepositoryState snapshots branch refs, HEAD, the
// index, working files, and configuration before and after successful and
// failed checks to prove a check only updates fetched objects and
// remote-tracking data.
func TestOriginCheckPreservesRepositoryState(t *testing.T) {
	t.Parallel()

	repo, bare := originFixture(t)
	testutil.CommitFile(t, repo, "tracked.txt", "tracked\n", "tracked")
	runGit(t, bare, "fetch", repo, "main:refs/heads/main")
	runGit(t, repo, "reset", "--hard", "HEAD~1")
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ignored.log"), []byte("ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "config", "custom.preserve", "yes")

	snapshot := func() string {
		var parts []string
		parts = append(parts, "refs:"+gitOutput(t, repo, "for-each-ref", "refs/heads", "--format=%(refname) %(objectname)"))
		parts = append(parts, "head:"+gitOutput(t, repo, "symbolic-ref", "HEAD"))
		parts = append(parts, "config:"+gitOutput(t, repo, "config", "--local", "--list"))
		index, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
		if err != nil {
			t.Fatalf("reading index: %v", err)
		}
		parts = append(parts, fmt.Sprintf("index:%d", len(index)))
		files, err := filepath.Glob(filepath.Join(repo, "*"))
		if err != nil {
			t.Fatalf("listing files: %v", err)
		}
		sort.Strings(files)
		for _, file := range files {
			info, err := os.Stat(file)
			if err != nil {
				t.Fatalf("statting %s: %v", file, err)
			}
			if info.IsDir() {
				continue
			}
			content, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("reading %s: %v", file, err)
			}
			parts = append(parts, fmt.Sprintf("file:%s:%q", filepath.Base(file), content))
		}
		return strings.Join(parts, "\n")
	}

	before := snapshot()
	if result := FetchOriginBranch(context.Background(), repo, OriginMapping{Branch: "main"}, OriginCheckOptions{}); result.State != FetchOriginFetched {
		t.Fatalf("successful check state = %q diagnostics %q", result.State, result.Diagnostics)
	}
	if _, err := CompareOriginSource(context.Background(), repo, gitOutput(t, repo, "rev-parse", "refs/heads/main"), gitOutput(t, repo, "rev-parse", "refs/remotes/origin/main"), OriginMapping{Branch: "main"}, OriginCheckOptions{}); err != nil {
		t.Fatalf("compare: %v", err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("repository state changed across successful check:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// A failed check (unreachable remote) must not change local state either.
	runGit(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing-remote"))
	beforeFailed := snapshot()
	if result := FetchOriginBranch(context.Background(), repo, OriginMapping{Branch: "main"}, OriginCheckOptions{}); result.State != FetchOriginUnavailable {
		t.Fatalf("failed check state = %q diagnostics %q; want unavailable", result.State, result.Diagnostics)
	}
	if after := snapshot(); after != beforeFailed {
		t.Fatalf("repository state changed across failed check:\nbefore:\n%s\nafter:\n%s", beforeFailed, after)
	}
}

func TestValidRemoteBranchName(t *testing.T) {
	t.Parallel()

	valid := []string{"main", "release/2026/q3", "release/1.2.x", "a_b.c-d"}
	invalid := []string{"", "-main", "main..other", "main.lock", "a//b", "/main", "main/", ".main", "main.", "main@{u}", "ma in", "main~", "main^", "a:b", "a?b", "a*b", "a[b", "main\x7f"}
	for _, name := range valid {
		if !validRemoteBranchName(name) {
			t.Errorf("validRemoteBranchName(%q) = false; want true", name)
		}
	}
	for _, name := range invalid {
		if validRemoteBranchName(name) {
			t.Errorf("validRemoteBranchName(%q) = true; want false", name)
		}
	}
}
