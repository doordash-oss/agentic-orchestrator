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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// These tests follow the initialize-api file's convention of not using
// t.Parallel(): they fork real git, and the race-detector binary misbehaves
// when many fork-heavy tests run at once.

// reconcileSourceBodyFromUpdate repeats one displayed update binding as the
// settlement request the renderer sends after losing the update response.
func reconcileSourceBodyFromUpdate(body map[string]any) map[string]any {
	return map[string]any{
		"repo_key":            body["repo_key"],
		"identity":            body["identity"],
		"mode":                body["mode"],
		"branch":              body["branch"],
		"origin_branch":       body["origin_branch"],
		"expected_local_sha":  body["expected_local_sha"],
		"expected_origin_sha": body["expected_origin_sha"],
	}
}

func (fx *initializeFixture) reconcileSource(body map[string]any) (*httptest.ResponseRecorder, RepositorySourceReconcileResponse) {
	fx.t.Helper()
	w := postTrustedJSON(fx.handler, apiPathWorkspaceRepositoryReconcileSourceUpdate, body)
	var resp RepositorySourceReconcileResponse
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
			fx.t.Fatalf("decode reconcile response: %v", err)
		}
	}
	return w, resp
}

// sourceUpdateRefRunnerFunc adapts a function to the CAS runner interface.
type sourceUpdateRefRunnerFunc func(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) git.BranchProbeCommandResult

func (f sourceUpdateRefRunnerFunc) Run(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) git.BranchProbeCommandResult {
	return f(ctx, repoPath, stdin, args, diagnosticLimit)
}

// reconcileResult carries one asynchronous settlement call's recorder and
// decoded response together so the test reads each exactly once.
type reconcileResult struct {
	w    *httptest.ResponseRecorder
	resp RepositorySourceReconcileResponse
}

func reconcileSourceAsync(fx *initializeFixture, body map[string]any) chan reconcileResult {
	done := make(chan reconcileResult, 1)
	go func() { w, resp := fx.reconcileSource(body); done <- reconcileResult{w: w, resp: resp} }()
	return done
}

// waitForCondition polls cond until it holds or the bound expires.
func waitForCondition(t *testing.T, bound time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWorkspaceRepositoryReconcileSourceUpdateSettlesTargetPresentAfterCompletedUpdate(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "settled")
	body := fx.updateSourceBody(repo, bare)

	if w, _ := fx.updateSource(body); w.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", w.Code, w.Body.String())
	}
	w, resp := fx.reconcileSource(reconcileSourceBodyFromUpdate(body))
	if w.Code != http.StatusOK {
		t.Fatalf("reconcile status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if resp.Outcome != ExpectedTargetPresent {
		t.Fatalf("outcome = %q; want expected_target_present", resp.Outcome)
	}
	if resp.LocalSha == nil || *resp.LocalSha != body["expected_origin_sha"] {
		t.Fatalf("local sha = %v; want the advanced origin tip", resp.LocalSha)
	}
	if resp.RepoKey != "settled" || resp.Branch != "main" || resp.OriginBranch != "main" || resp.Mode != "default" {
		t.Fatalf("echoed binding = key %q mode %q branch %q origin %q", resp.RepoKey, resp.Mode, resp.Branch, resp.OriginBranch)
	}
	if resp.Selection == nil || resp.Selection.ObservedSha != body["expected_origin_sha"] ||
		resp.Selection.Branch != "main" || resp.Selection.Mode != RepositorySourceMode("default") {
		t.Fatalf("fresh selection = %#v; want main at the advanced tip", resp.Selection)
	}
}

func TestWorkspaceRepositoryReconcileSourceUpdateReportsOriginalTipWhenNothingMutated(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "untouched")
	body := fx.updateSourceBody(repo, bare)

	w, resp := fx.reconcileSource(reconcileSourceBodyFromUpdate(body))
	if w.Code != http.StatusOK {
		t.Fatalf("reconcile status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if resp.Outcome != OriginalTipRemains {
		t.Fatalf("outcome = %q; want original_tip_remains", resp.Outcome)
	}
	if resp.LocalSha == nil || *resp.LocalSha != body["expected_local_sha"] {
		t.Fatalf("local sha = %v; want the displayed local tip", resp.LocalSha)
	}
}

func TestWorkspaceRepositoryReconcileSourceUpdateReportsExternallyChangedTip(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "moved-on")
	body := fx.updateSourceBody(repo, bare)
	// An external writer moves the branch to a third commit: the settlement
	// reports the observation without inferring anything about the attempt.
	third := fx.gitIn(repo, "commit-tree", "refs/heads/main^{tree}", "-m", "external")
	fx.gitIn(repo, "update-ref", "refs/heads/main", third)

	w, resp := fx.reconcileSource(reconcileSourceBodyFromUpdate(body))
	if w.Code != http.StatusOK {
		t.Fatalf("reconcile status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if resp.Outcome != LocalStateChanged {
		t.Fatalf("outcome = %q; want local_state_changed", resp.Outcome)
	}
	if resp.LocalSha == nil || *resp.LocalSha != third {
		t.Fatalf("local sha = %v; want the observed third commit", resp.LocalSha)
	}
}

func TestWorkspaceRepositoryReconcileSourceUpdateReportsMissingBranch(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "gone")
	body := fx.updateSourceBody(repo, bare)
	fx.gitIn(repo, "update-ref", "-d", "refs/heads/main")

	w, resp := fx.reconcileSource(reconcileSourceBodyFromUpdate(body))
	if w.Code != http.StatusOK {
		t.Fatalf("reconcile status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if resp.Outcome != BranchMissing {
		t.Fatalf("outcome = %q; want branch_missing", resp.Outcome)
	}
	if resp.LocalSha != nil {
		t.Fatalf("local sha = %v; want none for a missing branch", resp.LocalSha)
	}
}

func TestWorkspaceRepositoryReconcileSourceUpdateWaitsForAdmittedAttempt(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "admitted")
	body := fx.updateSourceBody(repo, bare)
	admitted := make(chan struct{})
	release := make(chan struct{})
	fx.api.updateSourceOptions = git.SourceUpdateOptions{BeforeCAS: func() {
		close(admitted)
		<-release
	}}

	updateDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { w, _ := fx.updateSource(body); updateDone <- w }()
	<-admitted

	reconcileDone := reconcileSourceAsync(fx, reconcileSourceBodyFromUpdate(body))
	// A mutation still holding coordination cannot be settled by a read of
	// its old or new local SHA: the reconciliation must still be waiting.
	select {
	case result := <-reconcileDone:
		t.Fatalf("reconcile settled while the attempt was still running (status %d)", result.w.Code)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	if w := <-updateDone; w.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", w.Code, w.Body.String())
	}
	result := <-reconcileDone
	if result.w.Code != http.StatusOK {
		t.Fatalf("reconcile status = %d body=%s; want 200", result.w.Code, result.w.Body.String())
	}
	if result.resp.Outcome != ExpectedTargetPresent {
		t.Fatalf("outcome = %q; want expected_target_present", result.resp.Outcome)
	}
}

func TestWorkspaceRepositoryReconcileSourceUpdateWaitsForAttemptQueuedOnGuard(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "queued")
	body := fx.updateSourceBody(repo, bare)
	identity, ok := git.ResolveRepoIdentity(repo)
	if !ok {
		t.Fatal("resolve identity")
	}

	// The test holds the canonical guard, so the admitted attempt queues
	// behind it before it can even start validating.
	unlock, locked := git.LockRepositoryUntil(context.Background(), repo)
	if !locked {
		t.Fatal("acquire guard")
	}
	updateDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { w, _ := fx.updateSource(body); updateDone <- w }()
	waitForCondition(t, 5*time.Second, "the update attempt to be admitted", func() bool {
		return !fx.api.sourceUpdates.quiescent(identity)
	})

	reconcileDone := reconcileSourceAsync(fx, reconcileSourceBodyFromUpdate(body))
	select {
	case result := <-reconcileDone:
		t.Fatalf("reconcile settled while an admitted attempt was queued (status %d)", result.w.Code)
	case <-time.After(150 * time.Millisecond):
	}

	unlock()
	if w := <-updateDone; w.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", w.Code, w.Body.String())
	}
	result := <-reconcileDone
	if result.w.Code != http.StatusOK {
		t.Fatalf("reconcile status = %d body=%s; want 200", result.w.Code, result.w.Body.String())
	}
	if result.resp.Outcome != ExpectedTargetPresent {
		t.Fatalf("outcome = %q; want expected_target_present", result.resp.Outcome)
	}
}

func TestWorkspaceRepositoryReconcileSourceUpdateDeadlineExpiryWhileAttemptAdmitted(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "stuck")
	body := fx.updateSourceBody(repo, bare)
	release := make(chan struct{})
	fx.api.updateSourceOptions = git.SourceUpdateOptions{BeforeCAS: func() { <-release }}
	fx.api.reconcileSourceDeadline = 250 * time.Millisecond

	updateDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { w, _ := fx.updateSource(body); updateDone <- w }()
	// Give the attempt time to be admitted, then settle for the deadline.
	time.Sleep(100 * time.Millisecond)

	w, _ := fx.reconcileSource(reconcileSourceBodyFromUpdate(body))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("reconcile status = %d body=%s; want 503", w.Code, w.Body.String())
	}
	assertCanonicalCode(t, w, "source_reconcile_unavailable")
	close(release)
	if w := <-updateDone; w.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestWorkspaceRepositoryReconcileSourceUpdateMissingRepositoryRequiresReselection(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "replaced-reconcile")
	body := fx.updateSourceBody(repo, bare)
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}

	w, _ := fx.reconcileSource(reconcileSourceBodyFromUpdate(body))
	if w.Code != http.StatusConflict {
		t.Fatalf("reconcile status = %d body=%s; want 409", w.Code, w.Body.String())
	}
	assertCanonicalCode(t, w, "invalid_repository")
}

func TestWorkspaceRepositoryReconcileSourceUpdateMalformedExpectationsAreBadRequests(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "malformed-reconcile")
	body := fx.updateSourceBody(repo, bare)
	reconcile := reconcileSourceBodyFromUpdate(body)
	reconcile["expected_origin_sha"] = "not-a-sha"

	w, _ := fx.reconcileSource(reconcile)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("reconcile status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	assertCanonicalCode(t, w, "bad_request")
}

func TestWorkspaceRepositoryReconcileSourceUpdateResponseLossBeforeCASLeavesOriginalTip(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "lost-before")
	body := fx.updateSourceBody(repo, bare)
	// The transport dies before the compare-and-swap: the attempt reports
	// unavailable and proves nothing either way.
	fx.api.updateSourceOptions = git.SourceUpdateOptions{UpdateRefRunner: sourceUpdateRefRunnerFunc(
		func(_ context.Context, _ string, _ string, _ []string, _ int) git.BranchProbeCommandResult {
			return git.BranchProbeCommandResult{ExitCode: -1, Err: context.Canceled, Diagnostics: "connection lost before the ref update"}
		})}

	w, _ := fx.updateSource(body)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("update status = %d body=%s; want 503", w.Code, w.Body.String())
	}
	assertCanonicalCode(t, w, "source_update_unavailable")
	if got := fx.gitIn(repo, "rev-parse", "refs/heads/main"); got != body["expected_local_sha"] {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
	w, resp := fx.reconcileSource(reconcileSourceBodyFromUpdate(body))
	if w.Code != http.StatusOK || resp.Outcome != OriginalTipRemains {
		t.Fatalf("reconcile = status %d outcome %q; want 200 original_tip_remains", w.Code, resp.Outcome)
	}
}

func TestWorkspaceRepositoryReconcileSourceUpdateResponseLossAfterCASProvesTarget(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "lost-after")
	body := fx.updateSourceBody(repo, bare)
	// The compare-and-swap lands, then the post-mutation verification is
	// lost: the attempt reports unavailable and proves nothing, while the
	// settlement read observes the advanced tip.
	var casDone atomic.Bool
	fx.api.updateSourceOptions = git.SourceUpdateOptions{
		UpdateRefRunner: sourceUpdateRefRunnerFunc(func(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) git.BranchProbeCommandResult {
			result := git.ExecSourceUpdateRefRunner{}.Run(ctx, repoPath, stdin, args, diagnosticLimit)
			if result.ExitCode == 0 {
				casDone.Store(true)
			}
			return result
		}),
		OriginCheckOptions: git.OriginCheckOptions{Runner: git.BranchProbeRunnerFunc(func(ctx context.Context, repoPath string, args []string, diagnosticLimit int) git.BranchProbeCommandResult {
			if casDone.Load() && len(args) > 0 && args[0] == "rev-parse" {
				return git.BranchProbeCommandResult{ExitCode: -1, Err: errors.New("connection lost"), Diagnostics: "connection lost while verifying the ref update"}
			}
			return git.ExecBranchProbeRunner{}.Run(ctx, repoPath, args, diagnosticLimit)
		})},
	}

	w, _ := fx.updateSource(body)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("update status = %d body=%s; want 503", w.Code, w.Body.String())
	}
	assertCanonicalCode(t, w, "source_update_unavailable")
	if got := fx.gitIn(repo, "rev-parse", "refs/heads/main"); got != body["expected_origin_sha"] {
		t.Fatalf("refs/heads/main = %s; want the advanced tip the CAS landed", got)
	}
	w, resp := fx.reconcileSource(reconcileSourceBodyFromUpdate(body))
	if w.Code != http.StatusOK || resp.Outcome != ExpectedTargetPresent {
		t.Fatalf("reconcile = status %d outcome %q; want 200 expected_target_present", w.Code, resp.Outcome)
	}
}

func TestWorkspaceRepositoryReconcileSourceUpdateReleasesAttemptWhenClientDisconnects(t *testing.T) {
	fx := newInitializeFixture(t)
	repo, bare := updateSourceFixture(t, fx, "client-gone")
	body := fx.updateSourceBody(repo, bare)
	identity, ok := git.ResolveRepoIdentity(repo)
	if !ok {
		t.Fatal("resolve identity")
	}
	admitted := make(chan struct{})
	release := make(chan struct{})
	fx.api.updateSourceOptions = git.SourceUpdateOptions{BeforeCAS: func() {
		close(admitted)
		<-release
	}}

	srv := httptest.NewServer(fx.handler)
	defer srv.Close()
	payload, _ := json.Marshal(body)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+apiPathWorkspaceRepositoryUpdateSource, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := srv.Client().Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	<-admitted
	// The client vanishes mid-attempt; the server-side attempt still runs to
	// completion and releases its lifetime registration.
	cancel()
	close(release)
	<-clientDone
	waitForCondition(t, 5*time.Second, "the disconnected attempt to release", func() bool {
		return fx.api.sourceUpdates.quiescent(identity)
	})

	// The cancelled context refuses the compare-and-swap, so nothing moved;
	// the settlement read observes the original tip.
	w, resp := fx.reconcileSource(reconcileSourceBodyFromUpdate(body))
	if w.Code != http.StatusOK || resp.Outcome != OriginalTipRemains {
		t.Fatalf("reconcile = status %d outcome %q; want 200 original_tip_remains", w.Code, resp.Outcome)
	}
}

// sequencingCreateTarget records when feature acceptance is dispatched.
type sequencingCreateTarget struct {
	MutationTarget
	called chan struct{}
}

func (t *sequencingCreateTarget) CreateFeature(req CreateFeatureRequest) (CreateFeatureResponse, error) {
	select {
	case <-t.called:
	default:
		close(t.called)
	}
	return CreateFeatureResponse{FeatureID: fixtureFeatureID, Result: resultCreated}, nil
}

// newSequencingFixture builds the initialize fixture shape around a create
// target that observes dispatch ordering, sharing one handler (and therefore
// one source-update tracker) between updates, reconciliations, and creates.
func newSequencingFixture(t *testing.T) (*initializeFixture, *sequencingCreateTarget) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{root}
	target := &sequencingCreateTarget{called: make(chan struct{})}
	api := newAPIHandler(HandlerOptions{Config: cfg, Mutations: target, DisableHostValidation: true})
	fx := &initializeFixture{t: t, api: api, handler: api.routes(), root: root, cfg: cfg}
	return fx, target
}

func (fx *initializeFixture) createFeatureAsync(t *testing.T, name string, sources []map[string]any) chan *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{
		"name": name, "pipeline": "medium", "risk_level": "medium",
		"repository_sources": sources,
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- postTrustedJSON(fx.handler, apiPathFeatures, body) }()
	return done
}

func TestCreateFeatureWaitsForAdmittedSourceUpdate(t *testing.T) {
	fx, target := newSequencingFixture(t)
	repo, bare := updateSourceFixture(t, fx, "accept-after")
	body := fx.updateSourceBody(repo, bare)
	admitted := make(chan struct{})
	release := make(chan struct{})
	fx.api.updateSourceOptions = git.SourceUpdateOptions{BeforeCAS: func() {
		close(admitted)
		<-release
	}}

	updateDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { w, _ := fx.updateSource(body); updateDone <- w }()
	<-admitted

	sources := []map[string]any{{
		"repo_key":     body["repo_key"],
		"identity":     body["identity"],
		"mode":         "default",
		"kind":         "branch",
		"branch":       "main",
		"observed_sha": body["expected_local_sha"],
	}}
	createDone := fx.createFeatureAsync(t, "serialize-with-update", sources)
	// Acceptance may not read and pin the source while an admitted update
	// can still mutate it.
	select {
	case <-target.called:
		t.Fatal("feature acceptance dispatched while the source update was still admitted")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	if w := <-updateDone; w.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", w.Code, w.Body.String())
	}
	if w := <-createDone; w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	select {
	case <-target.called:
	default:
		t.Fatal("feature acceptance never dispatched after the update settled")
	}
}

func TestCreateFeatureFailsClosedWhenAdmittedUpdateCannotSettle(t *testing.T) {
	fx, target := newSequencingFixture(t)
	repo, bare := updateSourceFixture(t, fx, "never-settles")
	body := fx.updateSourceBody(repo, bare)
	release := make(chan struct{})
	fx.api.updateSourceOptions = git.SourceUpdateOptions{BeforeCAS: func() { <-release }}
	fx.api.reconcileSourceDeadline = 250 * time.Millisecond

	updateDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { w, _ := fx.updateSource(body); updateDone <- w }()
	time.Sleep(100 * time.Millisecond)

	sources := []map[string]any{{
		"repo_key":     body["repo_key"],
		"identity":     body["identity"],
		"mode":         "default",
		"kind":         "branch",
		"branch":       "main",
		"observed_sha": body["expected_local_sha"],
	}}
	createDone := fx.createFeatureAsync(t, "blocked-by-update", sources)
	w := <-createDone
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("create status = %d body=%s; want 503", w.Code, w.Body.String())
	}
	assertCanonicalCode(t, w, "unavailable")
	select {
	case <-target.called:
		t.Fatal("feature acceptance dispatched despite the unsettled update")
	default:
	}
	close(release)
	if w := <-updateDone; w.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", w.Code, w.Body.String())
	}
}
