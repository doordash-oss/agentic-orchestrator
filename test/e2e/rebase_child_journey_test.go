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

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// rebaseJourneyFixture bundles the shared test infrastructure for rebase
// child journey tests: real git repo with bare remote, feature store,
// orchestrator, server, and client.
type rebaseJourneyFixture struct {
	store    *feature.Store
	mgr      *feature.Manager
	orch     *orchestrator.Orchestrator
	srv      *httptest.Server
	client   *server.Client
	repoDir  string
	parentID string
}

// setupRebaseJourneyFixture creates the shared infrastructure for a rebase
// child journey test with the given number of repos. Each repo is a real git
// repo with a bare remote. The parent feature is Published with the repos. The
// scripted implement kernel merges the persisted rebase target into each
// behind repo's child worktree so the happy path satisfies the mechanical
// integration gate.
func setupRebaseJourneyFixture(t *testing.T, parentID string, repos ...rebaseJourneyRepo) *rebaseJourneyFixture {
	return setupRebaseJourneyFixtureWithOpts(t, parentID, rebaseJourneyFixtureOptions{mergeRebaseTarget: true}, repos...)
}

// rebaseJourneyFixtureOptions configures the rebase journey fixture.
type rebaseJourneyFixtureOptions struct {
	// mergeRebaseTarget controls the scripted implement kernel. When true the
	// stub merges the persisted target so the gate passes (happy path). When
	// false the child branch is left without the target, exercising the gate's
	// not-ancestor failure.
	mergeRebaseTarget bool
	// remote overrides the default failingPRRemoteOps. When nil,
	// failingPRRemoteOps{} is used.
	remote orchestrator.RemoteOps
}

// setupRebaseJourneyFixtureWithOpts is the configurable core of
// setupRebaseJourneyFixture.
func setupRebaseJourneyFixtureWithOpts(t *testing.T, parentID string, opts rebaseJourneyFixtureOptions, repos ...rebaseJourneyRepo) *rebaseJourneyFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	wtBaseDir := filepath.Join(tmp, "worktrees")

	featureRepos := make([]feature.FeatureRepo, 0, len(repos))
	repoStates := make(map[string]*feature.RepoState, len(repos))
	for _, r := range repos {
		featureRepos = append(featureRepos, feature.FeatureRepo{
			Name:         r.name,
			Path:         r.repoDir,
			WorktreePath: r.repoDir,
			Branch:       r.branch,
			BaseBranch:   r.baseBranch,
			Publishable:  &r.publishable,
		})
		repoStates[r.name] = &feature.RepoState{Touched: r.touched, PRURL: r.prURL}
	}

	store := feature.NewStore(stateDir)
	publishedParent := &feature.Feature{
		ID:            parentID,
		Name:          "Rebase journey parent",
		Slug:          parentID,
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now().UTC().Truncate(time.Second),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         featureRepos,
		RepoStates:    repoStates,
		Checkpoints: feature.Checkpoints{
			RoadmapReview:   true,
			PhasePlanReview: true,
			ManualPublish:   true,
		},
	}
	if err := store.Save(publishedParent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}

	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, config.NewDefault())
	mgr.Worktrees = wm

	serverEvents := make(chan interface{}, 512)
	stopForwarding := make(chan struct{})
	t.Cleanup(func() { close(stopForwarding) })

	sm := session.NewManager(serverEvents)
	t.Cleanup(sm.Shutdown)
	pr := journeyChildPhaseRunnerWithOpts(t, sm, store, stateDir, journeyPhaseRunnerOptions{mergeRebaseTarget: opts.mergeRebaseTarget})

	var remote orchestrator.RemoteOps = failingPRRemoteOps{}
	if opts.remote != nil {
		remote = opts.remote
	}

	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Sessions:    sm,
		Remote:      remote,
		Worktrees:   wm,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})
	go func() {
		for {
			select {
			case ev := <-orch.Events():
				select {
				case serverEvents <- ev:
				default:
				}
			case <-stopForwarding:
				return
			}
		}
	}()

	srv := httptest.NewServer(server.NewHandler(server.HandlerOptions{
		Runtime:               server.RuntimeIdentity{RuntimeDir: tmp, StateDir: stateDir},
		Features:              store,
		FeatureStore:          store,
		Events:                serverEvents,
		Mutations:             &journeyMutationTarget{mgr: mgr, orch: orch},
		DisableHostValidation: true,
	}))
	t.Cleanup(srv.Close)
	client, err := server.NewClient(server.ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	return &rebaseJourneyFixture{
		store:    store,
		mgr:      mgr,
		orch:     orch,
		srv:      srv,
		client:   client,
		repoDir:  repos[0].repoDir,
		parentID: parentID,
	}
}

type rebaseJourneyRepo struct {
	name        string
	repoDir     string
	branch      string
	baseBranch  string
	publishable bool
	touched     bool
	prURL       string
}

// rebaseJourneyRepoSetup creates a real git repo with a bare remote, a
// feature branch, and optionally advances the base branch on the remote to
// create a behind state.
func rebaseJourneyRepoSetup(t *testing.T, name, branch string, advanceBase bool) rebaseJourneyRepo {
	t.Helper()
	repoDir := testutil.InitGitRepo(t)
	bareRemote := testutil.InitBareRemote(t, repoDir)
	journeyGit(t, repoDir, "checkout", "-b", branch)
	writeJourneyFile(t, repoDir, "base.txt", "v1\n")
	journeyGit(t, repoDir, "add", "base.txt")
	journeyGit(t, repoDir, "commit", "-m", "feature base commit")
	journeyGit(t, repoDir, "push", "-u", "origin", branch)

	publishable := true
	if advanceBase {
		// Advance the main branch on the bare remote so the feature
		// branch is behind.
		journeyGit(t, repoDir, "checkout", "main")
		writeJourneyFile(t, repoDir, "upstream.txt", "upstream change\n")
		journeyGit(t, repoDir, "add", "upstream.txt")
		journeyGit(t, repoDir, "commit", "-m", "upstream advancement")
		testutil.SimulatePush(t, repoDir, bareRemote, "main", "main")
		journeyGit(t, repoDir, "checkout", branch)
	}

	return rebaseJourneyRepo{
		name:        name,
		repoDir:     repoDir,
		branch:      branch,
		baseBranch:  "main",
		publishable: publishable,
		touched:     true,
	}
}

// TestRebaseChildHappyPathJourney verifies the complete rebase child flow:
// behind feature → rebase action → child of kind "rebase" with medium
// pipeline, generated content, persisted targets/behind set, fork-point-
// pinned worktrees → run to completion → parent branch gains a two-parent
// revertable merge commit → refactor-style tail (auto-publish push when
// configured; CodeReady otherwise).
func TestRebaseChildHappyPathJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	const parentID = "rebase-journey-parent"
	const branch = "feature/rebase-parent"
	repo := rebaseJourneyRepoSetup(t, "repoA", branch, true)
	fx := setupRebaseJourneyFixture(t, parentID, repo)

	// Capture the parent HEAD before launch for fork-point pinning.
	capturedHEAD := journeyGit(t, repo.repoDir, "rev-parse", "HEAD")

	// Post the rebase action (zero-input).
	resp, err := fx.client.RebaseFeature(t.Context(), parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID
	if childID == "" || childID == parentID {
		t.Fatalf("RebaseFeature response = %+v; want child id", resp)
	}
	if resp.Result != "created" {
		t.Fatalf("RebaseFeature result = %q; want created", resp.Result)
	}

	// Assert child kind, pipeline, and persisted targets/behind set.
	child, err := fx.store.Load(childID)
	if err != nil {
		t.Fatalf("Load(child) error = %v", err)
	}
	if child.Parent == nil || child.Parent.ParentID != parentID {
		t.Fatalf("child relationship = %+v, want rebase child of %s", child.Parent, parentID)
	}
	if child.Parent.Kind != feature.ChildKindRebase {
		t.Fatalf("child kind = %q, want %q", child.Parent.Kind, feature.ChildKindRebase)
	}
	if child.Pipeline != feature.PipelineMedium {
		t.Fatalf("child pipeline = %q, want %q", child.Pipeline, feature.PipelineMedium)
	}
	if len(child.Parent.RebaseTargets) != 1 {
		t.Fatalf("rebase targets = %+v, want 1", child.Parent.RebaseTargets)
	}
	target := child.Parent.RebaseTargets[0]
	if target.Repo != "repoA" || target.Target != "main" {
		t.Fatalf("rebase target = %+v, want repoA@main", target)
	}
	if len(child.Parent.RebaseBehind) != 1 || child.Parent.RebaseBehind[0] != "repoA" {
		t.Fatalf("rebase behind = %+v, want [repoA]", child.Parent.RebaseBehind)
	}
	// Assert the creation-time target SHA was persisted and matches the
	// target ref as it stood at the creation-time fetch.
	if target.TargetSHA == "" {
		t.Fatalf("rebase target TargetSHA = empty; want persisted creation-time SHA")
	}
	originMain := journeyGit(t, repo.repoDir, "rev-parse", "origin/main")
	if target.TargetSHA != originMain {
		t.Fatalf("rebase target TargetSHA = %q, want origin/main SHA %q", target.TargetSHA, originMain)
	}

	// Assert fork-point pinning: the child worktree HEAD is at the captured
	// parent tip, not the latest.
	childBody := waitForJourneySetupComplete(t, fx.srv.URL, childID)
	bases, _ := childBody["bases"].([]any)
	if len(bases) != 1 {
		t.Fatalf("child bases = %v, want 1 entry", bases)
	}
	baseEntry, _ := bases[0].(map[string]any)
	if baseEntry["sha"] != capturedHEAD {
		t.Fatalf("child base SHA = %v, want captured parent tip %s", baseEntry["sha"], capturedHEAD)
	}

	// Assert generated content: description names the behind repo and target,
	// contains merge instructions, forbids pushing and fetching.
	if !strings.Contains(child.Description, "repoA") {
		t.Fatalf("description missing repoA: %q", child.Description)
	}
	if !strings.Contains(child.Description, "main") {
		t.Fatalf("description missing target 'main': %q", child.Description)
	}
	if !strings.Contains(child.Description, "merge") {
		t.Fatalf("description missing merge instruction: %q", child.Description)
	}
	if !strings.Contains(child.Description, "Never push") {
		t.Fatalf("description missing no-push instruction: %q", child.Description)
	}
	if !strings.Contains(child.Description, "Do not fetch") {
		t.Fatalf("description missing no-fetch instruction: %q", child.Description)
	}

	// Assert exit criteria contain the git-level completion facts.
	if !strings.Contains(child.ExitCriteria, "ancestor") {
		t.Fatalf("exit criteria missing ancestor check: %q", child.ExitCriteria)
	}
	if !strings.Contains(child.ExitCriteria, "conflict markers") {
		t.Fatalf("exit criteria missing conflict markers check: %q", child.ExitCriteria)
	}
	if !strings.Contains(child.ExitCriteria, "Nothing was pushed") {
		t.Fatalf("exit criteria missing no-push invariant: %q", child.ExitCriteria)
	}

	// Drive the child through the pipeline to completion.
	postAction(t, fx.srv.URL, childID, "start", `{}`)
	waitForJourneyGate(t, fx.srv.URL, childID, 0)
	postReviewSessionProceed(t, fx.srv.URL, childID)
	waitForJourneyGate(t, fx.srv.URL, childID, 1)
	postReviewSessionProceed(t, fx.srv.URL, childID)
	waitForJourneyChildClosed(t, fx.srv.URL, fx.store, childID)

	// Assert the parent branch gains a two-parent merge commit.
	parents := journeyGit(t, repo.repoDir, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("parent merge parents = %q, want explicit two-parent merge commit", parents)
	}

	// Assert the child work is present in the parent.
	if _, err := os.Stat(filepath.Join(repo.repoDir, "child-output.txt")); err != nil {
		t.Fatalf("child work missing from parent after integration: %v", err)
	}

	// Assert the parent is CodeReady (manual publish parent).
	parentDetail := getJourneyJSON(t, fx.srv.URL+"/api/v1/features/"+parentID)["feature"].(map[string]any)
	if parentDetail["status"] != feature.StatusCodeReady.String() {
		t.Fatalf("parent status = %v, want CodeReady", parentDetail["status"])
	}
	if parentDetail["active_child"] != nil {
		t.Fatalf("parent active_child = %v, want cleared after closure", parentDetail["active_child"])
	}

	// Assert child is closed as completed.
	childDetail := getJourneyJSON(t, fx.srv.URL+"/api/v1/features/"+childID)["feature"].(map[string]any)
	if childDetail["close_outcome"] != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close_outcome = %v, want completed", childDetail["close_outcome"])
	}
}

// TestRebaseChildUpToDateReturnsTypedError verifies that posting the rebase
// action on a fully up-to-date feature returns the typed structured error
// with its stable code, and no child and no relationship event are created.
func TestRebaseChildUpToDateReturnsTypedError(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup")
	}

	const parentID = "rebase-uptodate-parent"
	const branch = "feature/rebase-uptodate"
	// Do NOT advance the base — the feature is up to date.
	repo := rebaseJourneyRepoSetup(t, "repoA", branch, false)
	fx := setupRebaseJourneyFixture(t, parentID, repo)

	// Post the rebase action and expect a conflict error (already up to date).
	req, _ := http.NewRequest(http.MethodPost,
		fx.srv.URL+"/api/v1/features/"+parentID+"/actions/rebase",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST rebase: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("rebase action status = %d; want 409 (already up to date); body: %s", resp.StatusCode, payload)
	}
	var errResp map[string]any
	json.NewDecoder(resp.Body).Decode(&errResp)
	errInfo, _ := errResp["error"].(map[string]any)
	code, _ := errInfo["code"].(string)
	if code != "rebase_already_up_to_date" {
		t.Fatalf("error code = %q; want rebase_already_up_to_date", code)
	}

	// Assert no child was created.
	children, err := fx.store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, c := range children {
		if c.IsChild() && c.Parent.ParentID == parentID {
			t.Fatalf("unexpected child %s created for up-to-date parent", c.ID)
		}
	}
}

// TestRebaseChildInterimCompatibility verifies that posting the action with a
// legacy source_revision body launches the child normally.
func TestRebaseChildInterimCompatibility(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup")
	}

	const parentID = "rebase-interim-parent"
	const branch = "feature/rebase-interim"
	repo := rebaseJourneyRepoSetup(t, "repoA", branch, true)
	fx := setupRebaseJourneyFixture(t, parentID, repo)

	// Post with a legacy source_revision body.
	req, _ := http.NewRequest(http.MethodPost,
		fx.srv.URL+"/api/v1/features/"+parentID+"/actions/rebase",
		strings.NewReader(`{"source_revision":"rev-old"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST rebase: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("rebase action status = %d; want 201 (child created)", resp.StatusCode)
	}
	var launchResp map[string]any
	json.NewDecoder(resp.Body).Decode(&launchResp)
	featureID, _ := launchResp["feature_id"].(string)
	if featureID == "" || featureID == parentID {
		t.Fatalf("response feature_id = %q; want child id", featureID)
	}

	// Verify the child is a rebase child.
	child, err := fx.store.Load(featureID)
	if err != nil {
		t.Fatalf("Load(child) error = %v", err)
	}
	if child.Parent.Kind != feature.ChildKindRebase {
		t.Fatalf("child kind = %q, want %q", child.Parent.Kind, feature.ChildKindRebase)
	}
}

// TestRebaseChildActionCatalogEligibility verifies that the rebase action is
// offered under the legacy eligibility rule and disabled with the active-child
// reason when another child pass is active.
func TestRebaseChildActionCatalogEligibility(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup")
	}

	const parentID = "rebase-catalog-parent"
	const branch = "feature/rebase-catalog"
	repo := rebaseJourneyRepoSetup(t, "repoA", branch, true)
	fx := setupRebaseJourneyFixture(t, parentID, repo)

	// Published parent: rebase should be offered.
	detail := getJourneyJSON(t, fx.srv.URL+"/api/v1/features/"+parentID)["feature"].(map[string]any)
	actions, _ := detail["actions"].([]any)
	var rebaseAction map[string]any
	for _, a := range actions {
		act, _ := a.(map[string]any)
		if act["id"] == "rebase" {
			rebaseAction = act
			break
		}
	}
	if rebaseAction == nil {
		t.Fatalf("rebase action not found in catalog")
	}
	if enabled, _ := rebaseAction["enabled"].(bool); !enabled {
		t.Fatalf("rebase action should be enabled for Published parent")
	}

	// Launch a rebase child to make the parent have an active child.
	_, err := fx.client.RebaseFeature(t.Context(), parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}

	// Now the parent has an active child: rebase should be disabled with
	// the active-child reason.
	detail = getJourneyJSON(t, fx.srv.URL+"/api/v1/features/"+parentID)["feature"].(map[string]any)
	actions, _ = detail["actions"].([]any)
	disabledRebaseAction := map[string]any(nil)
	for _, a := range actions {
		act, _ := a.(map[string]any)
		if act["id"] == "rebase" {
			disabledRebaseAction = act
			break
		}
	}
	if disabledRebaseAction == nil {
		t.Fatalf("rebase action not found in catalog after launching child")
	}
	if enabled, _ := disabledRebaseAction["enabled"].(bool); enabled {
		t.Fatalf("rebase action should be disabled while a child is active")
	}
	reasons, _ := disabledRebaseAction["disabled_reasons"].([]any)
	found := false
	for _, r := range reasons {
		reason, _ := r.(map[string]any)
		if reason["code"] == "active_child_present" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("rebase action disabled reasons = %+v; want active_child_present", reasons)
	}
}

// TestRebaseChildMultiRepoJourney verifies that with multiple repos where
// only one is behind, the up-to-date repo's content is byte-identical after
// integration and integration lands across both.
func TestRebaseChildMultiRepoJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	const parentID = "rebase-multi-parent"
	const branch = "feature/rebase-multi"
	repoA := rebaseJourneyRepoSetup(t, "repoA", branch, true)
	repoB := rebaseJourneyRepoSetup(t, "repoB", branch, false)
	fx := setupRebaseJourneyFixture(t, parentID, repoA, repoB)

	// Capture repoB's content before launch (up-to-date repo).
	repoBContentBefore, _ := os.ReadFile(filepath.Join(repoB.repoDir, "base.txt"))
	repoBHeadBefore := journeyGit(t, repoB.repoDir, "rev-parse", "HEAD")

	// Post the rebase action.
	resp, err := fx.client.RebaseFeature(t.Context(), parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID

	// Assert only repoA is behind.
	child, err := fx.store.Load(childID)
	if err != nil {
		t.Fatalf("Load(child) error = %v", err)
	}
	if len(child.Parent.RebaseBehind) != 1 || child.Parent.RebaseBehind[0] != "repoA" {
		t.Fatalf("behind set = %+v, want [repoA]", child.Parent.RebaseBehind)
	}

	// Drive to completion.
	waitForJourneySetupComplete(t, fx.srv.URL, childID)
	postAction(t, fx.srv.URL, childID, "start", `{}`)
	waitForJourneyGate(t, fx.srv.URL, childID, 0)
	postReviewSessionProceed(t, fx.srv.URL, childID)
	waitForJourneyGate(t, fx.srv.URL, childID, 1)
	postReviewSessionProceed(t, fx.srv.URL, childID)
	waitForJourneyChildClosed(t, fx.srv.URL, fx.store, childID)

	// The behind repo lands through an explicit two-parent merge commit.
	repoAParents := journeyGit(t, repoA.repoDir, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(repoAParents)); fields != 3 {
		t.Fatalf("repoA merge parents = %q, want explicit two-parent merge commit", repoAParents)
	}

	// The up-to-date repo is a pass-through: no merge commit and no ref move.
	repoBHeadAfter := journeyGit(t, repoB.repoDir, "rev-parse", "HEAD")
	if repoBHeadAfter != repoBHeadBefore {
		t.Fatalf("repoB HEAD changed: before=%s after=%s", repoBHeadBefore, repoBHeadAfter)
	}
	repoBParents := journeyGit(t, repoB.repoDir, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(repoBParents)); fields == 3 {
		t.Fatalf("repoB merge parents = %q, want no new merge commit for up-to-date repo", repoBParents)
	}

	// Assert the up-to-date repo (repoB) is byte-identical to its pre-launch state.
	repoBContentAfter, _ := os.ReadFile(filepath.Join(repoB.repoDir, "base.txt"))
	if string(repoBContentBefore) != string(repoBContentAfter) {
		t.Fatalf("repoB base.txt changed: before=%q after=%q", repoBContentBefore, repoBContentAfter)
	}
	if _, err := os.Stat(filepath.Join(repoB.repoDir, "child-output.txt")); !os.IsNotExist(err) {
		t.Fatalf("repoB child-output.txt exists or stat failed with non-not-exist error: %v", err)
	}

	child, err = fx.store.Load(childID)
	if err != nil {
		t.Fatalf("Load(child after close) error = %v", err)
	}
	entry := child.Parent.Transaction.EntryByRepo("repoB")
	if entry == nil {
		t.Fatalf("repoB transaction entry missing: %+v", child.Parent.Transaction)
	}
	if entry.CandidateSHA != repoBHeadBefore || entry.MergeHEAD != repoBHeadBefore {
		t.Fatalf("repoB transaction candidate=%s merge_head=%s, want pass-through SHA %s", entry.CandidateSHA, entry.MergeHEAD, repoBHeadBefore)
	}
}

// TestRebaseChildNoPushInvariant verifies that the fake remote records no
// pushes originating from the child during its execution.
func TestRebaseChildNoPushInvariant(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup")
	}

	const parentID = "rebase-nopush-parent"
	const branch = "feature/rebase-nopush"
	repo := rebaseJourneyRepoSetup(t, "repoA", branch, true)
	fx := setupRebaseJourneyFixture(t, parentID, repo)

	// Record the remote state before the child run.
	originRefBefore := journeyGit(t, repo.repoDir, "ls-remote", "origin", branch)

	// Post the rebase action.
	resp, err := fx.client.RebaseFeature(t.Context(), parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID

	// Wait for setup to complete.
	waitForJourneySetupComplete(t, fx.srv.URL, childID)

	// Assert the remote has not been pushed to during child creation and setup.
	originRefAfterSetup := journeyGit(t, repo.repoDir, "ls-remote", "origin", branch)
	if originRefAfterSetup != originRefBefore {
		t.Fatalf("remote ref changed during child setup: before=%q after=%q — child must not push", originRefBefore, originRefAfterSetup)
	}

	// Drive the child through to completion.
	postAction(t, fx.srv.URL, childID, "start", `{}`)
	waitForJourneyGate(t, fx.srv.URL, childID, 0)
	postReviewSessionProceed(t, fx.srv.URL, childID)
	waitForJourneyGate(t, fx.srv.URL, childID, 1)
	postReviewSessionProceed(t, fx.srv.URL, childID)
	waitForJourneyChildClosed(t, fx.srv.URL, fx.store, childID)

	// After closure, the parent is CodeReady (manual publish — no auto-publish).
	// The remote should still not have been pushed to by the child.
	originRefAfterClosure := journeyGit(t, repo.repoDir, "ls-remote", "origin", branch)
	if originRefAfterClosure != originRefBefore {
		t.Fatalf("remote ref changed during child lifecycle: before=%q after=%q — child must not push at any stage", originRefBefore, originRefAfterClosure)
	}
}

// TestRebaseChildTargetPrecedenceDefaultBranch verifies that target resolution
// falls back to the repository default branch when no PR base branch or
// recorded base branch is available.
func TestRebaseChildTargetPrecedenceDefaultBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup")
	}

	const parentID = "rebase-precedence-parent"
	const branch = "feature/rebase-precedence"
	repoDir := testutil.InitGitRepo(t)
	bareRemote := testutil.InitBareRemote(t, repoDir)
	journeyGit(t, repoDir, "checkout", "-b", branch)
	writeJourneyFile(t, repoDir, "base.txt", "v1\n")
	journeyGit(t, repoDir, "add", "base.txt")
	journeyGit(t, repoDir, "commit", "-m", "feature base")
	journeyGit(t, repoDir, "push", "-u", "origin", branch)

	// Advance main on the remote.
	journeyGit(t, repoDir, "checkout", "main")
	writeJourneyFile(t, repoDir, "upstream.txt", "upstream\n")
	journeyGit(t, repoDir, "add", "upstream.txt")
	journeyGit(t, repoDir, "commit", "-m", "upstream advance")
	testutil.SimulatePush(t, repoDir, bareRemote, "main", "main")
	journeyGit(t, repoDir, "checkout", branch)

	publishable := true
	repo := rebaseJourneyRepo{
		name:        "repoA",
		repoDir:     repoDir,
		branch:      branch,
		baseBranch:  "", // No recorded base branch — should fall back to default (main).
		publishable: publishable,
		touched:     true,
	}
	fx := setupRebaseJourneyFixture(t, parentID, repo)

	resp, err := fx.client.RebaseFeature(t.Context(), parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	child, err := fx.store.Load(resp.FeatureID)
	if err != nil {
		t.Fatalf("Load(child) error = %v", err)
	}
	target := child.Parent.RebaseTargets[0]
	if target.Target != "main" {
		t.Fatalf("target = %q, want main (default branch fallback)", target.Target)
	}
}

// TestRebaseChildGateAncestorFailureJourney verifies the e2e gate failure: a
// rebase child whose branch does not contain its persisted target commit
// fails integration with the typed gate attention record, and every parent
// ref is byte-identical before and after. The scripted implement kernel is
// configured to NOT merge the target, so the child branch remains behind the
// creation-time target and the gate's ancestor check fails.
func TestRebaseChildGateAncestorFailureJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	const parentID = "rebase-gate-fail-parent"
	const branch = "feature/rebase-gate-fail"
	repo := rebaseJourneyRepoSetup(t, "repoA", branch, true)
	fx := setupRebaseJourneyFixtureWithOpts(t, parentID, rebaseJourneyFixtureOptions{mergeRebaseTarget: false}, repo)

	// Capture the parent branch ref before the child runs integration.
	parentRefBefore := journeyGit(t, repo.repoDir, "rev-parse", branch)

	// Launch the rebase child.
	resp, err := fx.client.RebaseFeature(t.Context(), parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID

	// Assert the persisted creation-time target SHA is non-empty.
	child, err := fx.store.Load(childID)
	if err != nil {
		t.Fatalf("Load(child) error = %v", err)
	}
	if len(child.Parent.RebaseTargets) != 1 || child.Parent.RebaseTargets[0].TargetSHA == "" {
		t.Fatalf("rebase target = %+v, want non-empty creation-time TargetSHA", child.Parent.RebaseTargets)
	}

	// Drive the child through the pipeline to final review; integration then
	// runs the gate, which must abort on the not-ancestor violation.
	waitForJourneySetupComplete(t, fx.srv.URL, childID)
	postAction(t, fx.srv.URL, childID, "start", `{}`)
	waitForJourneyGate(t, fx.srv.URL, childID, 0)
	postReviewSessionProceed(t, fx.srv.URL, childID)
	waitForJourneyGate(t, fx.srv.URL, childID, 1)
	postReviewSessionProceed(t, fx.srv.URL, childID)

	// Wait for integration to park at attention (child stays active, not
	// closed). Poll the child until its transaction is in attention with the
	// not-ancestor gate code, or timeout.
	deadline := time.Now().Add(30 * time.Second)
	for {
		c, err := fx.store.Load(childID)
		if err != nil {
			t.Fatalf("Load(child) error = %v", err)
		}
		if c.Parent != nil && c.Parent.Transaction != nil &&
			c.Parent.Transaction.Phase == feature.TransactionPhaseAttention &&
			len(c.Parent.Transaction.Entries) > 0 &&
			c.Parent.Transaction.Entries[0].GateCode == feature.GateCodeNotAncestor {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for not-ancestor gate attention; child = %+v", c.Parent)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Every parent ref is byte-identical before and after.
	if parentRefAfter := journeyGit(t, repo.repoDir, "rev-parse", branch); parentRefAfter != parentRefBefore {
		t.Fatalf("parent ref changed: before=%s after=%s; gate must not touch parent refs", parentRefBefore, parentRefAfter)
	}

	// Re-running integration re-evaluates the gate idempotently: it stays
	// parked at attention with the same code.
	if err := fx.orch.RunChildIntegration(childID); err != nil {
		t.Fatalf("RunChildIntegration() re-run error = %v; want nil", err)
	}
	childRerun, _ := fx.store.Load(childID)
	if childRerun.Parent.Transaction == nil ||
		childRerun.Parent.Transaction.Phase != feature.TransactionPhaseAttention ||
		childRerun.Parent.Transaction.Entries[0].GateCode != feature.GateCodeNotAncestor {
		t.Fatalf("re-run gate = %+v; want attention/not-ancestor", childRerun.Parent.Transaction)
	}
	if parentRefAfter := journeyGit(t, repo.repoDir, "rev-parse", branch); parentRefAfter != parentRefBefore {
		t.Fatalf("parent ref changed on re-run: before=%s after=%s", parentRefBefore, parentRefAfter)
	}
}

// prBaseBranchRemoteOps wraps failingPRRemoteOps but returns a fixed PR base
// branch, simulating a GitHub PR whose base is a non-default branch.
type prBaseBranchRemoteOps struct {
	failingPRRemoteOps
	baseBranch string
}

func (o prBaseBranchRemoteOps) PRBaseBranch(repoPath, prURL string) string {
	return o.baseBranch
}

// TestRebaseChildPRBaseBranchPrecedenceJourney verifies the PR-base-branch
// leg of target precedence: when a repo has a recorded PR URL and the fake
// GitHub API returns a PR base branch, the rebase child resolves its target
// to that branch rather than the recorded base branch or the repo default.
func TestRebaseChildPRBaseBranchPrecedenceJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	const parentID = "rebase-pr-precedence-parent"
	const branch = "feature/rebase-pr-precedence"

	// Set up a behind repo with an advanced main branch.
	repo := rebaseJourneyRepoSetup(t, "repoA", branch, true)

	// Create a separate "pr-base" branch on the remote and advance it so
	// it's a resolvable target distinct from "main".
	prBase := "pr-base"
	journeyGit(t, repo.repoDir, "checkout", "-b", prBase)
	writeJourneyFile(t, repo.repoDir, "pr-base-change.txt", "pr-base advancement\n")
	journeyGit(t, repo.repoDir, "add", "pr-base-change.txt")
	journeyGit(t, repo.repoDir, "commit", "-m", "pr-base advancement")
	// Push the pr-base branch to the bare remote so origin/pr-base exists.
	journeyGit(t, repo.repoDir, "push", "origin", prBase)
	// Return to the feature branch.
	journeyGit(t, repo.repoDir, "checkout", branch)

	// Set the PR URL so resolveRebaseTarget queries the fake GitHub API.
	repo.prURL = "https://github.com/example/repoA/pull/1"

	fx := setupRebaseJourneyFixtureWithOpts(t, parentID, rebaseJourneyFixtureOptions{
		mergeRebaseTarget: true,
		remote:            prBaseBranchRemoteOps{baseBranch: prBase},
	}, repo)

	// Launch the rebase action.
	resp, err := fx.client.RebaseFeature(t.Context(), parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID

	// Assert the persisted target resolved to the PR base branch, not
	// "main" (the recorded base branch / default branch).
	child, err := fx.store.Load(childID)
	if err != nil {
		t.Fatalf("Load(child) error = %v", err)
	}
	if len(child.Parent.RebaseTargets) != 1 {
		t.Fatalf("rebase targets = %+v, want 1", child.Parent.RebaseTargets)
	}
	target := child.Parent.RebaseTargets[0]
	if target.Repo != "repoA" {
		t.Fatalf("rebase target repo = %q, want repoA", target.Repo)
	}
	if target.Target != prBase {
		t.Fatalf("rebase target = %q, want %q (PR base branch)", target.Target, prBase)
	}
	if len(child.Parent.RebaseBehind) != 1 || child.Parent.RebaseBehind[0] != "repoA" {
		t.Fatalf("rebase behind = %+v, want [repoA]", child.Parent.RebaseBehind)
	}
}
