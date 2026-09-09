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

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// These tests follow the initialize-api file's convention of not using
// t.Parallel(): they fork real git, and the race-detector binary misbehaves
// when many fork-heavy tests run at once.

func (fx *initializeFixture) originStatus(body map[string]any) *httptest.ResponseRecorder {
	fx.t.Helper()
	return postTrustedJSON(fx.handler, apiPathWorkspaceRepositoryOriginStatus, body)
}

func (fx *initializeFixture) originStatusSelector(repoKey, repo string) map[string]any {
	fx.t.Helper()
	return map[string]any{"repo_key": repoKey, "identity": fx.wireIdentity(repo)}
}

func (fx *initializeFixture) awaitOriginStatus(mode string, selectors []map[string]any, refresh []string) RepositoryOriginStatusResponse {
	fx.t.Helper()
	body := map[string]any{"mode": mode, "repositories": selectors}
	if refresh != nil {
		body["refresh"] = refresh
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		w := fx.originStatus(body)
		if w.Code != http.StatusOK {
			fx.t.Fatalf("origin-status status = %d body=%s; want 200", w.Code, w.Body.String())
		}
		var resp RepositoryOriginStatusResponse
		if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
			fx.t.Fatalf("decode origin-status response: %v", err)
		}
		// Refresh is a one-shot fresh attempt: later polls observe without
		// forcing new work.
		delete(body, "refresh")
		checking := false
		for _, row := range resp.Repositories {
			if row.Status == RepositoryOriginStatusStatusChecking {
				checking = true
				break
			}
		}
		if !checking || time.Now().After(deadline) {
			return resp
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestWorkspaceRepositoryOriginStatusResolvesLocalOnlyRows(t *testing.T) {
	fx := newInitializeFixture(t)
	plain := filepath.Join(fx.root, "plain")
	fx.git("init", "--initial-branch=main", plain)
	fx.gitIn(plain, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")

	unborn := fx.unbornClone("unborn", "main")

	detached := filepath.Join(fx.root, "detached")
	fx.git("init", "--initial-branch=main", detached)
	fx.gitIn(detached, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	fx.gitIn(detached, "checkout", "--detach", "HEAD")

	other := filepath.Join(fx.root, "other")
	fx.git("init", "--initial-branch=main", other)
	fx.gitIn(other, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	fx.gitIn(other, "remote", "add", "upstream", filepath.Join(fx.root, "upstream.git"))
	fx.gitIn(other, "config", "branch.main.remote", "upstream")
	fx.gitIn(other, "config", "branch.main.merge", "refs/heads/main")

	resp := fx.awaitOriginStatus("current", []map[string]any{
		fx.originStatusSelector("plain", plain),
		fx.originStatusSelector("unborn", unborn),
		fx.originStatusSelector("detached", detached),
		fx.originStatusSelector("other", other),
	}, nil)
	byKey := make(map[string]RepositoryOriginStatus, len(resp.Repositories))
	for _, row := range resp.Repositories {
		byKey[row.RepoKey] = row
	}
	if len(byKey) != 4 {
		t.Fatalf("rows = %d; want 4 independent rows", len(byKey))
	}
	if got := byKey["plain"]; got.Status != RepositoryOriginStatusStatusNoOrigin || got.CheckedAt == nil || got.Issue != nil {
		t.Errorf("plain row = %#v; want no_origin with a check time and no issue", got)
	}
	if got := byKey["unborn"]; got.Status != RepositoryOriginStatusStatusLocalBaseMissing || got.Issue == nil || got.Issue.Code != "local_base_missing" {
		t.Errorf("unborn row = %#v; want local_base_missing with repair guidance", got)
	}
	if got := byKey["detached"]; got.Status != RepositoryOriginStatusStatusDetached || got.Kind != RepositoryOriginStatusKindDetached || got.Commit == "" || got.OriginBranch != "" {
		t.Errorf("detached row = %#v; want detached source without a mapping", got)
	}
	if got := byKey["other"]; got.Status != RepositoryOriginStatusStatusOtherUpstream || got.Issue != nil {
		t.Errorf("other row = %#v; want other_upstream without substituting origin", got)
	}
}

func TestWorkspaceRepositoryOriginStatusRejectsMalformedRequests(t *testing.T) {
	fx := newInitializeFixture(t)
	repo := filepath.Join(fx.root, "repo")
	fx.git("init", "--initial-branch=main", repo)
	fx.gitIn(repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	selector := fx.originStatusSelector("repo", repo)

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"invalid mode", map[string]any{"mode": "both", "repositories": []map[string]any{selector}}, http.StatusBadRequest},
		{"missing repositories", map[string]any{"mode": "default"}, http.StatusBadRequest},
		{"unknown field", map[string]any{"mode": "default", "repositories": []map[string]any{selector}, "paths": []string{repo}}, http.StatusBadRequest},
		{"empty refresh key", map[string]any{"mode": "default", "repositories": []map[string]any{selector}, "refresh": []string{""}}, http.StatusBadRequest},
		{"duplicate refresh key", map[string]any{"mode": "default", "repositories": []map[string]any{selector}, "refresh": []string{"repo", "repo"}}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		w := fx.originStatus(tc.body)
		if w.Code != tc.want {
			t.Errorf("%s: status = %d body=%s; want %d", tc.name, w.Code, w.Body.String(), tc.want)
		}
	}

	duplicated := map[string]any{"mode": "default", "repositories": []map[string]any{selector, selector}}
	if w := fx.originStatus(duplicated); w.Code != http.StatusBadRequest {
		t.Errorf("duplicate selector: status = %d; want 400", w.Code)
	}

	staleIdentity := fx.originStatusSelector("repo", repo)
	staleIdentity["identity"] = map[string]any{"path": repo, "common_dir": repo, "device": "1", "inode": "2"}
	if w := fx.originStatus(map[string]any{"mode": "default", "repositories": []map[string]any{staleIdentity}}); w.Code != http.StatusConflict {
		t.Errorf("unresolved selector: status = %d; want 409", w.Code)
	}

	untrusted := httptest.NewRequest(http.MethodPost, apiPathWorkspaceRepositoryOriginStatus, nil)
	untrusted.Header.Set("Content-Type", contentTypeJSON)
	untrustedRecorder := httptest.NewRecorder()
	fx.handler.ServeHTTP(untrustedRecorder, untrusted)
	if untrustedRecorder.Code != http.StatusForbidden {
		t.Errorf("untrusted request: status = %d; want 403", untrustedRecorder.Code)
	}
}

func TestWorkspaceRepositoryOriginStatusFetchesAndCompares(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := originStatusRemoteFixture(t, fx, "service")

	w := fx.originStatus(map[string]any{
		"mode":         "default",
		"repositories": []map[string]any{fx.originStatusSelector("service", repo)},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("origin-status status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var first RepositoryOriginStatusResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&first); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(first.Repositories) != 1 || first.Repositories[0].Status != RepositoryOriginStatusStatusChecking {
		t.Fatalf("first row = %#v; want checking", first.Repositories)
	}

	resp := fx.awaitOriginStatus("default", []map[string]any{fx.originStatusSelector("service", repo)}, nil)
	row := resp.Repositories[0]
	if row.AheadCount == nil || row.BehindCount == nil {
		t.Fatalf("row = %#v; want ahead/behind counts", row)
	}
	if row.Status != RepositoryOriginStatusStatusBehind || *row.AheadCount != 0 || *row.BehindCount != 2 {
		t.Fatalf("row = status %q ahead %d behind %d; want behind 0/2", row.Status, *row.AheadCount, *row.BehindCount)
	}
	if want := fx.gitIn(bare, "rev-parse", "refs/heads/main"); row.FetchedSha != want {
		t.Errorf("fetched_sha = %q; want remote sha %q", row.FetchedSha, want)
	}
	if want := fx.gitIn(repo, "rev-parse", "refs/heads/main"); row.LocalSha != want {
		t.Errorf("local_sha = %q; want local sha %q", row.LocalSha, want)
	}
	if row.OriginBranch != "main" || row.CheckedAt == nil {
		t.Errorf("row origin branch/check time = %q/%v; want main with a check time", row.OriginBranch, row.CheckedAt)
	}
	if row.UpdateEligible == nil || !*row.UpdateEligible {
		t.Errorf("row update_eligible = %v; want eligible for a clean unoccupied-behind branch target", row.UpdateEligible)
	}
	if row.StaleComparison != nil {
		t.Errorf("row stale_comparison = %#v; want none after a successful check", row.StaleComparison)
	}
	if row.Issue != nil {
		t.Errorf("row issue = %#v; want none", row.Issue)
	}
}

func TestWorkspaceRepositoryOriginStatusRefreshPreservesStaleComparison(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, _ := originStatusRemoteFixture(t, fx, "service")

	fx.awaitOriginStatus("default", []map[string]any{fx.originStatusSelector("service", repo)}, nil)

	// Break the remote so the refreshed attempt fails while the local source
	// stays valid; the earlier successful comparison must survive as stale.
	fx.gitIn(repo, "remote", "set-url", "origin", filepath.Join(fx.root, "missing-remote"))
	if probe := fx.gitIn(repo, "remote", "get-url", "origin"); probe == "" {
		t.Fatal("origin url missing before the refreshed check")
	}

	resp := fx.awaitOriginStatus("default", []map[string]any{fx.originStatusSelector("service", repo)}, []string{"service"})
	row := resp.Repositories[0]
	if row.Status != RepositoryOriginStatusStatusUnknown || row.Issue == nil || row.Issue.Code != "origin_check_unavailable" {
		t.Fatalf("row = %#v; want unknown with an origin_check_unavailable issue", row)
	}
	if row.StaleComparison == nil || row.StaleComparison.Status != OriginComparisonStatusBehind {
		t.Fatalf("row stale_comparison = %#v; want the earlier behind comparison", row.StaleComparison)
	}
	if row.StaleComparison.BehindCount != 2 || row.FetchedSha != "" {
		t.Errorf("row = %#v; want stale counts preserved and no fresh fetched sha", row)
	}
	if row.UpdateEligible == nil || *row.UpdateEligible {
		t.Errorf("row update_eligible = %v; want false with a comparison_unavailable blocker", row.UpdateEligible)
	}
	if len(row.UpdateBlockers) != 1 || row.UpdateBlockers[0] != RepositoryOriginStatusUpdateBlockers("comparison_unavailable") {
		t.Errorf("row update_blockers = %v; want comparison_unavailable", row.UpdateBlockers)
	}
}

func TestWorkspaceRepositoryOriginStatusModeSwitchInvalidatesComparison(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, _ := originStatusRemoteFixture(t, fx, "service")
	completed := fx.awaitOriginStatus("default", []map[string]any{fx.originStatusSelector("service", repo)}, nil)
	if completed.Repositories[0].Status != RepositoryOriginStatusStatusBehind {
		t.Fatalf("default-mode row = %#v; want behind before the mode switch", completed.Repositories[0])
	}

	// The same repository in the other shared mode is a different comparison
	// identity: the completed default-mode result must not satisfy it.
	w := fx.originStatus(map[string]any{
		"mode":         "current",
		"repositories": []map[string]any{fx.originStatusSelector("service", repo)},
	})
	var resp RepositoryOriginStatusResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Repositories) != 1 || resp.Repositories[0].Status != RepositoryOriginStatusStatusChecking {
		t.Fatalf("current-mode row = %#v; want checking, not the default-mode result", resp.Repositories)
	}
	fx.awaitOriginStatus("current", []map[string]any{fx.originStatusSelector("service", repo)}, nil)
}

// originStatusRemoteFixture builds a repository whose local main is two
// commits behind its bare origin.
func originStatusRemoteFixture(t *testing.T, fx *initializeFixture, name string) (repo, bare string) {
	t.Helper()
	repo = filepath.Join(fx.root, name)
	bare = filepath.Join(fx.root, name+"-origin.git")
	fx.git("init", "--bare", bare)
	fx.gitIn(bare, "symbolic-ref", "HEAD", "refs/heads/main")
	fx.git("init", "--initial-branch=main", repo)
	fx.gitIn(repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	fx.gitIn(repo, "remote", "add", "origin", bare)
	fx.gitIn(bare, "fetch", repo, "main:refs/heads/main")
	fx.gitIn(repo, "fetch", "origin")
	fx.gitIn(repo, "branch", "--set-upstream-to=origin/main", "main")

	writer := filepath.Join(fx.root, name+"-writer")
	fx.git("clone", bare, writer)
	fx.gitIn(writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote one")
	fx.gitIn(writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote two")
	fx.gitIn(writer, "push", "origin", "main")
	return repo, bare
}

func originCoordinatorTestRepo(t *testing.T) (string, git.RepoIdentity, git.OriginCheckPlan) {
	t.Helper()
	repo := testutil.InitGitRepo(t)
	testutil.PairWithBareRemote(t, repo, "main", "main")
	identity, ok := git.ResolveRepoIdentity(repo)
	if !ok {
		t.Fatalf("resolve identity for %s", repo)
	}
	plan := git.PlanOriginCheck(context.Background(), repo, git.LocalSourceModeDefault, git.OriginCheckOptions{})
	if plan.Mapping == nil {
		t.Fatalf("plan = %#v; want a mapped origin", plan)
	}
	return repo, identity, plan
}

func originTestGit(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func comparisonOutcome(status git.OriginCheckStatus) originAttemptOutcome {
	return originAttemptOutcome{Comparison: &git.OriginComparison{
		Status:     status,
		LocalSHA:   "1111111111111111111111111111111111111111",
		FetchedSHA: "2222222222222222222222222222222222222222",
		CheckedAt:  time.Now().UTC(),
	}}
}

func waitOriginAttempt(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("origin check attempt did not complete")
	}
}

func TestOriginCheckCoordinatorCoalescesIdenticalRequests(t *testing.T) {
	repo, identity, plan := originCoordinatorTestRepo(t)
	coordinator := newOriginCheckCoordinator()
	defer coordinator.Shutdown()
	coordinator.deadline = 30 * time.Second

	var calls atomic.Int32
	release := make(chan struct{})
	coordinator.executor = func(ctx context.Context, repoPath string, plan git.OriginCheckPlan) originAttemptOutcome {
		calls.Add(1)
		<-release
		return comparisonOutcome(git.OriginCheckUpToDate)
	}

	_, _, done1 := coordinator.ensure(identity, plan, repo, false)
	_, _, done2 := coordinator.ensure(identity, plan, repo, false)
	if done1 == nil || done2 == nil {
		t.Fatal("ensure scheduled no attempt")
	}
	close(release)
	waitOriginAttempt(t, done1)
	waitOriginAttempt(t, done2)
	if calls.Load() != 1 {
		t.Fatalf("executor calls = %d; want one coalesced attempt", calls.Load())
	}
	if completed, ok, _ := coordinator.ensure(identity, plan, repo, false); !ok || completed.comparison == nil {
		t.Fatalf("completed result = %#v ok=%v; want the cached comparison", completed, ok)
	}
}

func TestOriginCheckCoordinatorRefreshStartsNewAttempt(t *testing.T) {
	repo, identity, plan := originCoordinatorTestRepo(t)
	coordinator := newOriginCheckCoordinator()
	defer coordinator.Shutdown()
	coordinator.deadline = 30 * time.Second

	var calls atomic.Int32
	coordinator.executor = func(ctx context.Context, repoPath string, plan git.OriginCheckPlan) originAttemptOutcome {
		calls.Add(1)
		if calls.Load() == 1 {
			return comparisonOutcome(git.OriginCheckUpToDate)
		}
		return comparisonOutcome(git.OriginCheckBehind)
	}

	_, _, firstDone := coordinator.ensure(identity, plan, repo, false)
	waitOriginAttempt(t, firstDone)
	if completed, ok, _ := coordinator.ensure(identity, plan, repo, false); !ok || completed.comparison.Status != git.OriginCheckUpToDate {
		t.Fatalf("cached result = %#v; want the first comparison", completed)
	}

	_, _, refreshDone := coordinator.ensure(identity, plan, repo, true)
	waitOriginAttempt(t, refreshDone)
	if calls.Load() != 2 {
		t.Fatalf("executor calls = %d; want a fresh attempt for the refresh", calls.Load())
	}
	if completed, ok, _ := coordinator.ensure(identity, plan, repo, false); !ok || completed.comparison.Status != git.OriginCheckBehind {
		t.Fatalf("refreshed result = %#v; want the second comparison", completed)
	}
}

func TestOriginCheckCoordinatorCapsConcurrencyAtFour(t *testing.T) {
	coordinator := newOriginCheckCoordinator()
	defer coordinator.Shutdown()
	coordinator.deadline = 30 * time.Second

	repos := make([]struct {
		path     string
		identity git.RepoIdentity
		plan     git.OriginCheckPlan
	}, 0, 8)
	for i := 0; i < 8; i++ {
		path, identity, plan := originCoordinatorTestRepo(t)
		repos = append(repos, struct {
			path     string
			identity git.RepoIdentity
			plan     git.OriginCheckPlan
		}{path, identity, plan})
	}

	var mu sync.Mutex
	active, maxActive := 0, 0
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	coordinator.executor = func(ctx context.Context, repoPath string, plan git.OriginCheckPlan) originAttemptOutcome {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		entered <- struct{}{}
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		return comparisonOutcome(git.OriginCheckUpToDate)
	}

	dones := make([]<-chan struct{}, 0, len(repos))
	for _, repo := range repos {
		_, _, done := coordinator.ensure(repo.identity, repo.plan, repo.path, false)
		dones = append(dones, done)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("a concurrent slot never admitted a check")
		}
	}
	select {
	case <-entered:
		t.Fatal("a fifth check ran concurrently with four active checks")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the queued checks never ran after release")
		}
	}
	for _, done := range dones {
		waitOriginAttempt(t, done)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxActive > maxConcurrentOriginChecks {
		t.Fatalf("max concurrent checks = %d; want at most %d", maxActive, maxConcurrentOriginChecks)
	}
}

func TestOriginCheckCoordinatorDeadlineExpiresWhileQueued(t *testing.T) {
	coordinator := newOriginCheckCoordinator()
	defer coordinator.Shutdown()
	coordinator.deadline = 150 * time.Millisecond

	blockers := make([]struct {
		path     string
		identity git.RepoIdentity
		plan     git.OriginCheckPlan
	}, 0, maxConcurrentOriginChecks)
	for i := 0; i < maxConcurrentOriginChecks; i++ {
		path, identity, plan := originCoordinatorTestRepo(t)
		blockers = append(blockers, struct {
			path     string
			identity git.RepoIdentity
			plan     git.OriginCheckPlan
		}{path, identity, plan})
	}
	entered := make(chan struct{}, maxConcurrentOriginChecks)
	release := make(chan struct{})
	coordinator.executor = func(ctx context.Context, repoPath string, plan git.OriginCheckPlan) originAttemptOutcome {
		entered <- struct{}{}
		<-release
		return comparisonOutcome(git.OriginCheckUpToDate)
	}
	for _, repo := range blockers {
		coordinator.ensure(repo.identity, repo.plan, repo.path, false)
	}
	for i := 0; i < maxConcurrentOriginChecks; i++ {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("a concurrent slot never admitted a blocking check")
		}
	}

	// Every slot is held, so this attempt can only wait; its deadline expires
	// before it is ever admitted.
	queuedPath, queuedIdentity, queuedPlan := originCoordinatorTestRepo(t)
	_, _, queuedDone := coordinator.ensure(queuedIdentity, queuedPlan, queuedPath, false)
	waitOriginAttempt(t, queuedDone)

	key := originCheckKeyFor(queuedIdentity, queuedPlan)
	coordinator.mu.Lock()
	completed := coordinator.completed[key]
	coordinator.mu.Unlock()
	if completed == nil || !completed.unavailable || completed.diagnostics != "origin check timed out before running" {
		t.Fatalf("queued result = %#v; want a deadline-expired unknown", completed)
	}
	if completed.stale != nil {
		t.Fatalf("queued result preserved a stale comparison = %#v; want none", completed.stale)
	}
	close(release)
}

func TestOriginCheckCoordinatorDeadlineExpiresWhileRunning(t *testing.T) {
	repo, identity, plan := originCoordinatorTestRepo(t)
	coordinator := newOriginCheckCoordinator()
	defer coordinator.Shutdown()
	coordinator.deadline = 150 * time.Millisecond
	coordinator.executor = func(ctx context.Context, repoPath string, plan git.OriginCheckPlan) originAttemptOutcome {
		<-ctx.Done()
		return originAttemptOutcome{Unavailable: true, Diagnostics: "interrupted"}
	}

	_, _, done := coordinator.ensure(identity, plan, repo, false)
	waitOriginAttempt(t, done)
	key := originCheckKeyFor(identity, plan)
	coordinator.mu.Lock()
	completed := coordinator.completed[key]
	coordinator.mu.Unlock()
	if completed == nil || !completed.unavailable || completed.diagnostics != "origin check timed out" {
		t.Fatalf("running result = %#v; want a deadline-expired unknown", completed)
	}
}

func TestOriginCheckCoordinatorShutdownReleasesAttempts(t *testing.T) {
	repo, identity, plan := originCoordinatorTestRepo(t)
	coordinator := newOriginCheckCoordinator()
	coordinator.deadline = 30 * time.Second
	entered := make(chan struct{}, 1)
	shutdown := make(chan struct{})
	coordinator.executor = func(ctx context.Context, repoPath string, plan git.OriginCheckPlan) originAttemptOutcome {
		entered <- struct{}{}
		<-ctx.Done()
		close(shutdown)
		return originAttemptOutcome{Unavailable: true, Diagnostics: "runtime shut down"}
	}

	_, _, done := coordinator.ensure(identity, plan, repo, false)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		coordinator.mu.Lock()
		completed := coordinator.completed[originCheckKeyFor(identity, plan)]
		flightActive := false
		for _, f := range coordinator.inflight {
			if f.key == originCheckKeyFor(identity, plan) {
				flightActive = true
			}
		}
		coordinator.mu.Unlock()
		t.Fatalf("executor never entered: completed=%#v inflight=%v", completed, flightActive)
	}
	go coordinator.Shutdown()
	select {
	case <-shutdown:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown never cancelled the in-flight attempt")
	}
	waitOriginAttempt(t, done)
}

func TestOriginCheckCoordinatorSerializesLinkedWorktrees(t *testing.T) {
	repo, identity, plan := originCoordinatorTestRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	originTestGit(t, "-C", repo, "worktree", "add", linked, "-b", "topic")
	t.Cleanup(func() { originTestGit(t, "-C", repo, "worktree", "remove", "--force", linked) })
	linkedIdentity, ok := git.ResolveRepoIdentity(linked)
	if !ok {
		t.Fatalf("resolve identity for %s", linked)
	}
	linkedPlan := git.PlanOriginCheck(context.Background(), linked, git.LocalSourceModeCurrent, git.OriginCheckOptions{})
	if linkedPlan.Mapping == nil {
		t.Fatalf("linked plan = %#v; want a mapped origin via the same-name fallback", linkedPlan)
	}
	if linkedIdentity.CommonDir != identity.CommonDir {
		t.Fatalf("fixture common dirs differ: %q vs %q", linkedIdentity.CommonDir, identity.CommonDir)
	}

	coordinator := newOriginCheckCoordinator()
	defer coordinator.Shutdown()
	coordinator.deadline = 30 * time.Second

	var mu sync.Mutex
	active, maxActive := 0, 0
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	coordinator.executor = func(ctx context.Context, repoPath string, plan git.OriginCheckPlan) originAttemptOutcome {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		entered <- struct{}{}
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		return comparisonOutcome(git.OriginCheckUpToDate)
	}

	_, _, doneMain := coordinator.ensure(identity, plan, repo, false)
	_, _, doneLinked := coordinator.ensure(linkedIdentity, linkedPlan, linked, false)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first linked-worktree check never ran")
	}
	// The second check shares the common directory: it can only enter after
	// the first releases the mutation boundary.
	close(release)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the second linked-worktree check never ran after the lock released")
	}
	waitOriginAttempt(t, doneMain)
	waitOriginAttempt(t, doneLinked)
	mu.Lock()
	defer mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("max concurrent checks for one common directory = %d; want 1", maxActive)
	}
}

func TestOriginCheckCoordinatorFailedRefreshPreservesStaleComparison(t *testing.T) {
	repo, identity, plan := originCoordinatorTestRepo(t)
	coordinator := newOriginCheckCoordinator()
	defer coordinator.Shutdown()
	coordinator.deadline = 30 * time.Second

	var calls atomic.Int32
	coordinator.executor = func(ctx context.Context, repoPath string, plan git.OriginCheckPlan) originAttemptOutcome {
		if calls.Add(1) == 1 {
			return comparisonOutcome(git.OriginCheckBehind)
		}
		return originAttemptOutcome{Unavailable: true, Diagnostics: "remote unreachable"}
	}
	_, _, firstDone := coordinator.ensure(identity, plan, repo, false)
	waitOriginAttempt(t, firstDone)
	_, _, retryDone := coordinator.ensure(identity, plan, repo, true)
	waitOriginAttempt(t, retryDone)

	key := originCheckKeyFor(identity, plan)
	coordinator.mu.Lock()
	completed := coordinator.completed[key]
	coordinator.mu.Unlock()
	if completed == nil || !completed.unavailable {
		t.Fatalf("retry result = %#v; want an unknown outcome", completed)
	}
	if completed.stale == nil || completed.stale.Status != git.OriginCheckBehind {
		t.Fatalf("retry stale comparison = %#v; want the earlier behind result", completed.stale)
	}
	if completed.updateEligible == nil || *completed.updateEligible || len(completed.updateBlockers) != 1 || completed.updateBlockers[0] != git.UpdateBlockerComparisonUnavailable {
		t.Fatalf("retry eligibility = %v blockers = %v; want false with comparison_unavailable", completed.updateEligible, completed.updateBlockers)
	}

	row := RepositoryOriginStatus{}
	row.applyCompleted(completed)
	if row.StaleComparison == nil || row.StaleComparison.Status != OriginComparisonStatusBehind || row.StaleComparison.BehindCount != 0 {
		t.Fatalf("row stale_comparison = %#v; want the earlier behind comparison on the wire", row.StaleComparison)
	}
	if row.Status != RepositoryOriginStatusStatusUnknown || row.Issue == nil || row.Issue.Code != "origin_check_unavailable" {
		t.Fatalf("row = %#v; want unknown with an origin_check_unavailable issue", row)
	}
}
