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
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// These tests follow the initialize-api file's convention of not using
// t.Parallel(): they fork real git, and the race-detector binary misbehaves
// when many fork-heavy tests run at once.

// updateSourceFixture builds a repository whose default branch main is two
// commits behind its bare origin while the checkout sits on an unrelated
// branch, so main is absent from every worktree checkout.
func updateSourceFixture(t *testing.T, fx *initializeFixture, name string) (repo, bare string) {
	t.Helper()
	repo = filepath.Join(fx.root, name)
	bare = filepath.Join(t.TempDir(), name+"-origin.git")
	fx.git("init", "--bare", bare)
	fx.gitIn(bare, "symbolic-ref", "HEAD", "refs/heads/main")
	fx.git("init", "--initial-branch=main", repo)
	fx.gitIn(repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	fx.gitIn(repo, "remote", "add", "origin", bare)
	fx.gitIn(bare, "fetch", repo, "main:refs/heads/main")
	fx.gitIn(repo, "fetch", "origin")
	fx.gitIn(repo, "checkout", "-b", "work")
	fx.gitIn(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	writer := filepath.Join(t.TempDir(), "writer")
	fx.git("clone", bare, writer)
	fx.gitIn(writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote one")
	fx.gitIn(writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote two")
	fx.gitIn(writer, "push", "origin", "main")
	return repo, bare
}

// updateSourceBody builds the displayed expectations for the fixture's
// default branch as the renderer would send them.
func (fx *initializeFixture) updateSourceBody(repo, bare string) map[string]any {
	fx.t.Helper()
	return map[string]any{
		"repo_key":           filepath.Base(repo),
		"identity":           fx.wireIdentity(repo),
		"mode":               "default",
		"branch":             "main",
		"origin_branch":      "main",
		"expected_local_sha": fx.gitIn(repo, "rev-parse", "refs/heads/main"),
		"expected_origin_sha": fx.gitIn(bare, "rev-parse", "refs/heads/main"),
		"checkout_head_ref":  "refs/heads/work",
		"checkout_head_sha":  fx.gitIn(repo, "rev-parse", "HEAD^{commit}"),
	}
}

func (fx *initializeFixture) updateSource(body map[string]any) (*httptest.ResponseRecorder, RepositoryUpdateSourceResponse) {
	fx.t.Helper()
	w := postTrustedJSON(fx.handler, apiPathWorkspaceRepositoryUpdateSource, body)
	var resp RepositoryUpdateSourceResponse
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
			fx.t.Fatalf("decode update-source response: %v", err)
		}
	}
	return w, resp
}

// assertUpdateSourceCanonicalCode pins the canonical error code on a
// non-2xx refusal envelope.
func assertCanonicalCode(t *testing.T, w *httptest.ResponseRecorder, code string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Result().Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if envelope.Error.Code != code {
		t.Fatalf("error code = %q; want %q (body=%s)", envelope.Error.Code, code, w.Body.String())
	}
}

func TestWorkspaceRepositoryUpdateSourceFastForwardsUnoccupiedBranch(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "updatable")
	body := fx.updateSourceBody(repo, bare)
	workSHA := fx.gitIn(repo, "rev-parse", "refs/heads/work")

	w, resp := fx.updateSource(body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if resp.Result != Updated {
		t.Fatalf("result = %q; want updated", resp.Result)
	}
	if resp.RepoKey != "updatable" || resp.Branch != "main" || resp.OriginBranch != "main" {
		t.Fatalf("response targets = key %q branch %q origin %q; want updatable main main", resp.RepoKey, resp.Branch, resp.OriginBranch)
	}
	if resp.PreviousSha == nil || resp.LocalSha == nil || resp.FetchedSha == nil ||
		*resp.LocalSha != body["expected_origin_sha"] || *resp.PreviousSha != body["expected_local_sha"] {
		t.Fatalf("response SHAs = previous %v local %v fetched %v", resp.PreviousSha, resp.LocalSha, resp.FetchedSha)
	}
	if got := fx.gitIn(repo, "rev-parse", "refs/heads/main"); got != body["expected_origin_sha"] {
		t.Fatalf("refs/heads/main = %s; want the fetched origin tip", got)
	}
	if got := fx.gitIn(repo, "symbolic-ref", "--quiet", "HEAD"); got != "refs/heads/work" {
		t.Fatalf("HEAD = %s; want refs/heads/work", got)
	}
	if got := fx.gitIn(repo, "rev-parse", "refs/heads/work"); got != workSHA {
		t.Fatalf("refs/heads/work moved; want %s", workSHA)
	}
}

func TestWorkspaceRepositoryUpdateSourceEqualityReplayIsNoOp(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "replay")
	body := fx.updateSourceBody(repo, bare)

	if w, _ := fx.updateSource(body); w.Code != http.StatusOK {
		t.Fatalf("first update status = %d", w.Code)
	}
	w, resp := fx.updateSource(body)
	if w.Code != http.StatusOK {
		t.Fatalf("replay status = %d body=%s", w.Code, w.Body.String())
	}
	if resp.Result != AlreadyUpToDate {
		t.Fatalf("replay result = %q; want already_up_to_date", resp.Result)
	}
	if resp.LocalSha == nil || *resp.LocalSha != body["expected_origin_sha"] {
		t.Fatalf("replay local SHA = %v; want the unchanged origin tip", resp.LocalSha)
	}
}

func TestWorkspaceRepositoryUpdateSourceStaleOriginTipCarriesFreshStatus(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "stale-origin")
	body := fx.updateSourceBody(repo, bare)
	writer := filepath.Join(t.TempDir(), "writer")
	fx.git("clone", bare, writer)
	fx.gitIn(writer, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "remote three")
	fx.gitIn(writer, "push", "origin", "main")

	w, resp := fx.updateSource(body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if resp.Result != Stale || resp.Reason != OriginTipChanged {
		t.Fatalf("result = %q reason = %q; want stale origin_tip_changed", resp.Result, resp.Reason)
	}
	if resp.Status == nil || resp.Status.Status != RepositoryOriginStatusStatusBehind {
		t.Fatalf("fresh status = %#v; want behind", resp.Status)
	}
	if resp.Status.BehindCount == nil || *resp.Status.BehindCount != 3 {
		t.Fatalf("fresh behind count = %v; want 3", resp.Status.BehindCount)
	}
	if resp.Status.UpdateEligible == nil || *resp.Status.UpdateEligible {
		t.Fatalf("fresh eligibility = %v; want false", resp.Status.UpdateEligible)
	}
	if got := fx.gitIn(repo, "rev-parse", "refs/heads/main"); got != body["expected_local_sha"] {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestWorkspaceRepositoryUpdateSourceOriginalCheckoutTargetRefuses(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "occupied")
	fx.gitIn(repo, "checkout", "main")
	body := fx.updateSourceBody(repo, bare)
	body["checkout_head_ref"] = "refs/heads/main"
	body["checkout_head_sha"] = fx.gitIn(repo, "rev-parse", "HEAD^{commit}")

	w, resp := fx.updateSource(body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if resp.Result != Stale || resp.Reason != BranchCheckedOut {
		t.Fatalf("result = %q reason = %q; want stale branch_checked_out", resp.Result, resp.Reason)
	}
	blocked := false
	for _, blocker := range resp.Status.UpdateBlockers {
		if blocker == RepositoryOriginStatusUpdateBlockers(git.UpdateBlockerBranchCheckedOutOriginal) {
			blocked = true
		}
	}
	if !blocked {
		t.Fatalf("status blockers = %v; want branch_checked_out_in_original_checkout", resp.Status.UpdateBlockers)
	}
	if got := fx.gitIn(repo, "rev-parse", "refs/heads/main"); got != body["expected_local_sha"] {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestWorkspaceRepositoryUpdateSourceMissingAndReplacedRepositoriesRequireReselection(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "replaced")
	body := fx.updateSourceBody(repo, bare)

	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	w, _ := fx.updateSource(body)
	if w.Code != http.StatusConflict {
		t.Fatalf("missing repo status = %d body=%s; want 409", w.Code, w.Body.String())
	}
	assertCanonicalCode(t, w, "invalid_repository")

	// A replacement at the same path has a new identity and must never be
	// mutated through the stale selector.
	fx.git("init", "--initial-branch=main", repo)
	fx.gitIn(repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "replacement")
	w, _ = fx.updateSource(body)
	if w.Code != http.StatusConflict {
		t.Fatalf("replaced repo status = %d body=%s; want 409", w.Code, w.Body.String())
	}
	assertCanonicalCode(t, w, "invalid_repository")
}

func TestWorkspaceRepositoryUpdateSourceResolvesRenamedCatalogKeyByIdentity(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "renamed")
	body := fx.updateSourceBody(repo, bare)
	// The displayed key no longer exists in the catalog, but the identity
	// still resolves the same repository under its current key.
	body["repo_key"] = "a-key-that-no-longer-exists"

	w, resp := fx.updateSource(body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if resp.Result != Updated {
		t.Fatalf("result = %q; want updated", resp.Result)
	}
	if resp.RepoKey != "renamed" {
		t.Fatalf("repo key = %q; want the current catalog key", resp.RepoKey)
	}
}

func TestWorkspaceRepositoryUpdateSourceLocalBaseMissingRefusesWithRequestIdentity(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "missing-base")
	body := fx.updateSourceBody(repo, bare)
	// The displayed branch ref disappears before the update runs.
	fx.gitIn(repo, "update-ref", "-d", "refs/heads/main")

	w, resp := fx.updateSource(body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if resp.Result != Stale || resp.Reason != SourceChanged {
		t.Fatalf("result = %q reason = %q; want stale source_changed", resp.Result, resp.Reason)
	}
	// A terminal plan leaves the current selection unresolved: the response
	// still identifies the refused request's mode and branch.
	if resp.Mode != "default" || resp.Branch != "main" || resp.OriginBranch != "main" {
		t.Fatalf("response identity = mode %q branch %q origin %q; want the request's own targets", resp.Mode, resp.Branch, resp.OriginBranch)
	}
	if resp.Status == nil || resp.Status.Status != RepositoryOriginStatusStatusLocalBaseMissing {
		t.Fatalf("fresh status = %#v; want local_base_missing", resp.Status)
	}
}

func TestWorkspaceRepositoryUpdateSourceMalformedExpectationsAreBadRequests(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "malformed")
	body := fx.updateSourceBody(repo, bare)
	body["expected_origin_sha"] = "not-a-sha"

	w, _ := fx.updateSource(body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	assertCanonicalCode(t, w, "bad_request")
}

func TestWorkspaceRepositoryUpdateSourceDeadlineExpiryIsUnavailable(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "deadline")
	body := fx.updateSourceBody(repo, bare)
	fx.api.updateSourceDeadline = 400 * time.Millisecond
	fx.api.updateSourceOptions = git.SourceUpdateOptions{OriginCheckOptions: git.OriginCheckOptions{
		Runner: git.BranchProbeRunnerFunc(func(ctx context.Context, repoPath string, args []string, diagnosticLimit int) git.BranchProbeCommandResult {
			if len(args) > 0 && args[0] == "ls-remote" {
				<-ctx.Done()
				return git.BranchProbeCommandResult{ExitCode: -1, Err: ctx.Err(), Diagnostics: "hanging origin probe"}
			}
			return git.ExecBranchProbeRunner{}.Run(ctx, repoPath, args, diagnosticLimit)
		}),
	}}

	w, _ := fx.updateSource(body)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s; want 503", w.Code, w.Body.String())
	}
	assertCanonicalCode(t, w, "source_update_unavailable")
	if got := fx.gitIn(repo, "rev-parse", "refs/heads/main"); got != body["expected_local_sha"] {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestWorkspaceRepositoryUpdateSourceMembershipRaceBeforeCASRefuses(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "membership-race")
	body := fx.updateSourceBody(repo, bare)
	linked := filepath.Join(t.TempDir(), "linked")
	fx.api.updateSourceOptions = git.SourceUpdateOptions{BeforeCAS: func() {
		// An external process checks the branch out between validation and
		// the compare-and-swap.
		fx.gitIn(repo, "worktree", "add", linked, "main")
	}}
	t.Cleanup(func() { fx.gitIn(repo, "worktree", "remove", "--force", linked) })

	w, resp := fx.updateSource(body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if resp.Result != Stale || resp.Reason != BranchCheckedOut {
		t.Fatalf("result = %q reason = %q; want stale branch_checked_out", resp.Result, resp.Reason)
	}
	if got := fx.gitIn(repo, "rev-parse", "refs/heads/main"); got != body["expected_local_sha"] {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
}

func TestWorkspaceRepositoryOriginStatusExposesObservedCheckoutHead(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, _ := updateSourceFixture(t, fx, "checkout-head")
	headSHA := fx.gitIn(repo, "rev-parse", "HEAD^{commit}")

	resp := fx.awaitOriginStatus("default", []map[string]any{fx.originStatusSelector("checkout-head", repo)}, nil)
	if len(resp.Repositories) != 1 {
		t.Fatalf("rows = %d; want 1", len(resp.Repositories))
	}
	row := resp.Repositories[0]
	if row.Status != RepositoryOriginStatusStatusBehind {
		t.Fatalf("row status = %q; want behind", row.Status)
	}
	if row.CheckoutHeadRef == nil || *row.CheckoutHeadRef != "refs/heads/work" {
		t.Fatalf("row checkout head ref = %v; want refs/heads/work", row.CheckoutHeadRef)
	}
	if row.CheckoutHeadSha == nil || *row.CheckoutHeadSha != headSHA {
		t.Fatalf("row checkout head sha = %v; want %s", row.CheckoutHeadSha, headSHA)
	}
}
