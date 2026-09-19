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

// Real-git coverage of the rebase child preflight's classification and
// launch conditions: per-layer live pull-request states classify kept,
// merged, and closed layers; a closed-unmerged pull request refuses the
// launch with the Phase 7 stack-closed error naming layer, title, and URL;
// a repository whose every layer merged is excluded from the work list; a
// local-only repository is classified by behind-ness alone; and the work
// list carries exactly the repositories with work while the merged state
// observed live is persisted on the parent's stack.

package orchestrator

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// preflightRemoteOps serves canned pull-request states and base lookups for
// the preflight's remote reads; no other operation is exercised here.
type preflightRemoteOps struct {
	states map[string]string
}

func (r *preflightRemoteOps) Push(worktreePath, branch string) error {
	return git.Push(worktreePath, branch)
}
func (r *preflightRemoteOps) PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
	return git.PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA)
}
func (r *preflightRemoteOps) CreatePR(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
	return git.CreatePR(repoPath, branch, title, body, draft, baseBranch)
}
func (r *preflightRemoteOps) PRBaseBranch(repoPath, prURL string) string {
	return "main"
}
func (r *preflightRemoteOps) PRState(repoPath, prURL string) (string, error) {
	if state, ok := r.states[prURL]; ok {
		return state, nil
	}
	return git.PRStateOpen, nil
}
func (r *preflightRemoteOps) GetPRBody(prURL string) (string, error) {
	return "", nil
}
func (r *preflightRemoteOps) UpdatePRBody(prURL, body string) error {
	return nil
}
func (r *preflightRemoteOps) UpdatePRBase(prURL, base string) error {
	return nil
}
func (r *preflightRemoteOps) ReopenPullRequest(repoPath, branch, prURL string) error {
	return git.ReopenPullRequest(repoPath, branch, prURL)
}

// rebasePreflightFixture builds the pre-rebase published stack the preflight
// classifies: one publishable repository (optionally a second one) with a
// three-layer stack, every branch pushed to a bare origin, and main on the
// origin advanced with layer 1's squashed content plus one upstream commit.
type rebasePreflightFixture struct {
	t *testing.T

	store  *feature.Store
	mgr    *feature.Manager
	remote *preflightRemoteOps

	parentID string
	repoDir  string
	// bareDir maps a repository name to its bare origin path, for reviewer
	// pushes from a second clone.
	bareDir                map[string]string
	prURL1, prURL2, prURL3 string
	tip1, tip2, tip3       string
	targetSHA, forkSHA     string
}

// preflightFixtureOpts configures the preflight fixture.
type preflightFixtureOpts struct {
	// WithRepoB adds a second repository to the parent.
	WithRepoB bool
	// BehindRepoB advances repoB's origin main past its stack (behind)
	// instead of leaving it at the fork point (up to date).
	BehindRepoB bool
	// AdvanceMainA controls whether repoA's origin main advances past the
	// stack (behind); false leaves repoA up to date so launch conditions
	// other than behind-ness (divergence, merged layers) can be exercised
	// alone.
	AdvanceMainA bool
}

// newRebasePreflightFixture builds the shared fixture. behindRepoB controls
// whether the second repository's origin advanced (behind) or stayed at the
// fork point (up to date); repoBEntries customizes its stack entries.
func newRebasePreflightFixture(t *testing.T, opts preflightFixtureOpts) *rebasePreflightFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("real-git rebase preflight test")
	}
	withRepoB, behindRepoB, advanceMainA := opts.WithRepoB, opts.BehindRepoB, opts.AdvanceMainA
	fx := &rebasePreflightFixture{t: t, parentID: "rebase-preflight-parent", bareDir: map[string]string{}}

	newRepo := func(name string) (repoDir, bare string, tips [3]string, fork string) {
		repoDir = testutil.InitGitRepo(t)
		bare = rebaseTailBareOrigin(t, repoDir, name)
		fork = restackGit(t, repoDir, "rev-parse", "HEAD")

		testutil.CreateBranch(t, repoDir, "stack/1")
		testutil.CommitFile(t, repoDir, "l1.txt", "layer1 v1\n", "layer1 phase1")
		testutil.CommitFile(t, repoDir, "l1.txt", "layer1 v2\n", "layer1 phase2")
		tips[0] = restackGit(t, repoDir, "rev-parse", "HEAD")

		testutil.CreateBranch(t, repoDir, "stack/2")
		testutil.CommitFile(t, repoDir, "l2.txt", "layer2 v1\n", "layer2 phase3")
		testutil.CommitFile(t, repoDir, "l2.txt", "layer2 v2\n", "layer2 phase4")
		tips[1] = restackGit(t, repoDir, "rev-parse", "HEAD")

		testutil.CreateBranch(t, repoDir, "stack/3")
		testutil.CommitFile(t, repoDir, "l3.txt", "layer3 v1\n", "layer3 phase5")
		tips[2] = restackGit(t, repoDir, "rev-parse", "HEAD")

		for _, branch := range []string{"stack/1", "stack/2", "stack/3"} {
			testutil.SimulatePush(t, repoDir, bare, branch, branch)
		}
		return repoDir, bare, tips, fork
	}

	repoA, bareA, tipsA, forkA := newRepo("repoa")
	fx.repoDir = repoA
	fx.bareDir["repoa"] = bareA
	fx.tip1, fx.tip2, fx.tip3 = tipsA[0], tipsA[1], tipsA[2]

	fx.forkSHA = forkA
	fx.targetSHA = forkA
	if advanceMainA {
		// Advance repoa's origin/main past the stack: layer 1's squashed
		// content plus one upstream commit.
		restackCheckout(t, repoA, "main")
		restackGit(t, repoA, "checkout", "stack/1", "--", "l1.txt")
		restackCommitAs(t, repoA, "squash merge layer 1", "Main Merger", "main@example.com")
		testutil.CommitFile(t, repoA, "upstream.txt", "upstream change\n", "upstream advancement")
		fx.targetSHA = restackGit(t, repoA, "rev-parse", "HEAD")
		testutil.SimulatePush(t, repoA, bareA, "main", "main")
		restackCheckout(t, repoA, "stack/3")
	}

	publishable := true
	parentRepos := []feature.FeatureRepo{{
		Name: "repoa", Path: repoA, WorktreePath: repoA,
		Branch: "stack/3", BaseBranch: "main", Publishable: &publishable,
	}}
	repoStates := map[string]*feature.RepoState{"repoa": {Touched: true}}
	fx.prURL1 = "https://github.com/example/repoa/pull/1"
	fx.prURL2 = "https://github.com/example/repoa/pull/2"
	fx.prURL3 = "https://github.com/example/repoa/pull/3"
	stack := []feature.StackLayer{
		{Position: 1, Title: "Layer one", Phases: []int{1, 2}, Branch: "stack/1", Repos: map[string]feature.StackRepoEntry{
			"repoa": {PRURL: fx.prURL1, PRState: feature.StackPRStateOpen, TipSHA: fx.tip1},
		}},
		{Position: 2, Title: "Layer two", Phases: []int{3, 4}, Branch: "stack/2", Repos: map[string]feature.StackRepoEntry{
			"repoa": {PRURL: fx.prURL2, PRState: feature.StackPRStateOpen, TipSHA: fx.tip2},
		}},
		{Position: 3, Title: "Layer three", Phases: []int{5}, Branch: "stack/3", Repos: map[string]feature.StackRepoEntry{
			"repoa": {PRURL: fx.prURL3, PRState: feature.StackPRStateOpen, TipSHA: fx.tip3},
		}},
	}

	if withRepoB {
		repoB, bareB, tipsB, _ := newRepo("repob")
		fx.bareDir["repob"] = bareB
		if behindRepoB {
			restackCheckout(t, repoB, "main")
			restackGit(t, repoB, "checkout", "stack/1", "--", "l1.txt")
			restackCommitAs(t, repoB, "squash merge layer 1", "Main Merger", "main@example.com")
			testutil.CommitFile(t, repoB, "upstream.txt", "upstream change\n", "upstream advancement")
			testutil.SimulatePush(t, repoB, bareB, "main", "main")
			restackCheckout(t, repoB, "stack/3")
		}
		parentRepos = append(parentRepos, feature.FeatureRepo{
			Name: "repob", Path: repoB, WorktreePath: repoB,
			Branch: "stack/3", BaseBranch: "main", Publishable: &publishable,
		})
		repoStates["repob"] = &feature.RepoState{Touched: true}
		urlB1 := "https://github.com/example/repob/pull/1"
		urlB2 := "https://github.com/example/repob/pull/2"
		urlB3 := "https://github.com/example/repob/pull/3"
		stack[0].Repos["repob"] = feature.StackRepoEntry{PRURL: urlB1, PRState: feature.StackPRStateOpen, TipSHA: tipsB[0]}
		stack[1].Repos["repob"] = feature.StackRepoEntry{PRURL: urlB2, PRState: feature.StackPRStateOpen, TipSHA: tipsB[1]}
		stack[2].Repos["repob"] = feature.StackRepoEntry{PRURL: urlB3, PRState: feature.StackPRStateOpen, TipSHA: tipsB[2]}
	}

	parent := &feature.Feature{
		ID:            fx.parentID,
		Name:          "Rebase preflight parent",
		Slug:          fx.parentID,
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now().UTC().Truncate(time.Second),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         parentRepos,
		RepoStates:    repoStates,
		Checkpoints:   feature.Checkpoints{ManualPublish: true},
		Stack:         stack,
	}
	parent.SetRun(&feature.Run{RunNumber: 1, RepoStates: repoStates, Stack: feature.CopyStackLayers(stack)})

	cfg := config.NewDefault()
	cfg.Repos["repoa"] = config.RepoConfig{Path: repoA}
	if withRepoB {
		cfg.Repos["repob"] = config.RepoConfig{Path: parentRepos[1].Path}
	}
	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	mgr := feature.NewManager(store, cfg)
	fx.store = store
	fx.mgr = mgr
	fx.remote = &preflightRemoteOps{states: map[string]string{}}
	return fx
}

func (fx *rebasePreflightFixture) orchestrator() *Orchestrator {
	fx.t.Helper()
	orch := New(Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Remote:    fx.remote,
		Worktrees: git.NewWorktreeManager(filepath.Join(fx.t.TempDir(), "worktrees")),
	}, Hooks{})
	fx.t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})
	return orch
}

// TestRebasePreflight_ClassifiesMergedLayerBehindReposAsWork covers the
// primary classification: a repository whose layer 1 pull request reads
// merged and whose remote base moved records layer 1 merged, layers 2 and 3
// kept, behind true, and appears in the work list, and the merged state
// observed live is persisted on the parent's stack.
func TestRebasePreflight_ClassifiesMergedLayerBehindReposAsWork(t *testing.T) {
	fx := newRebasePreflightFixture(t, preflightFixtureOpts{AdvanceMainA: true})
	fx.remote.states[fx.prURL1] = git.PRStateMerged
	orch := fx.orchestrator()

	res, err := orch.RebaseChildPreflight(fx.parentID)
	if err != nil {
		t.Fatalf("RebaseChildPreflight() error = %v", err)
	}
	if len(res.WorkRepos) != 1 || res.WorkRepos[0] != "repoa" {
		t.Fatalf("work repos = %+v, want [repoa]", res.WorkRepos)
	}
	if len(res.Behind) != 1 || res.Behind[0] != "repoa" {
		t.Fatalf("behind = %+v, want [repoa]", res.Behind)
	}
	if len(res.Targets) != 1 || res.Targets[0].TargetSHA != fx.targetSHA {
		t.Fatalf("targets = %+v, want repoa at the advanced target %s", res.Targets, fx.targetSHA)
	}
	states := map[int]feature.RebaseLayerState{}
	for _, c := range res.LayerStates {
		states[c.LayerPosition] = c.State
	}
	if states[1] != feature.RebaseLayerStateMerged {
		t.Fatalf("layer 1 classification = %q, want merged", states[1])
	}
	for pos := 2; pos <= 3; pos++ {
		if states[pos] != feature.RebaseLayerStateKept {
			t.Fatalf("layer %d classification = %q, want kept", pos, states[pos])
		}
	}

	parent, err := fx.store.Load(fx.parentID)
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	if got := parent.Stack[0].Repos["repoa"].PRState; got != feature.StackPRStateMerged {
		t.Fatalf("parent stack layer 1 state = %q, want merged persisted at preflight", got)
	}
}

// TestRebasePreflight_ClosedPullRequestRefusesLaunch proves a closed-unmerged
// pull request in the stack refuses the launch with the Phase 7 stack-closed
// error naming the repository, the layer, its title, and its URL — and parks
// the repository on the closed record the publish walk stores, naming the
// same layer, while creating no child feature.
func TestRebasePreflight_ClosedPullRequestRefusesLaunch(t *testing.T) {
	fx := newRebasePreflightFixture(t, preflightFixtureOpts{AdvanceMainA: true})
	fx.remote.states[fx.prURL2] = git.PRStateClosed
	orch := fx.orchestrator()

	_, err := orch.RebaseChildPreflight(fx.parentID)
	if err == nil {
		t.Fatal("RebaseChildPreflight() succeeded with a closed layer pull request")
	}
	var closed *PublishStackClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("error = %v (%T), want *PublishStackClosedError", err, err)
	}
	if closed.RepoName != "repoa" || closed.LayerPosition != 2 || closed.LayerTitle != "Layer two" || closed.PRURL != fx.prURL2 {
		t.Fatalf("stack-closed error = %+v, want repoa layer 2 (Layer two) at %s", closed, fx.prURL2)
	}

	// The refusal parks the repository on the same needs-action record the
	// publish walk stores, so the closed-PR blocker carries its resolutions
	// no matter which surface detected it.
	parent, err := fx.store.Load(fx.parentID)
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	record := parent.RepoStates["repoa"].Error
	if record == nil || record.Code != errcat.PublishStackPullRequestClosed {
		t.Fatalf("repoa stored record = %+v, want publish_stack_pull_request_closed", record)
	}
	if record.Context == nil || len(record.Context.Repositories) != 1 {
		t.Fatalf("repoa record context = %+v, want exactly one repositories block", record.Context)
	}
	block := record.Context.Repositories[0]
	if block.Name != "repoa" || block.LayerPosition != 2 || block.LayerTitle != "Layer two" || block.PullRequestURL != fx.prURL2 {
		t.Fatalf("repoa record block = %+v, want repoa layer 2 (Layer two) at %s", block, fx.prURL2)
	}
	if entry := stackRepoEntryFor(parent, 2, "repoa"); entry.PRState != feature.StackPRStateClosed {
		t.Fatalf("parent stack layer 2 state = %q, want closed persisted at preflight", entry.PRState)
	}

	// The refused launch creates no child feature: the store still holds
	// exactly the parent.
	stored, err := fx.store.List()
	if err != nil {
		t.Fatalf("list store: %v", err)
	}
	if len(stored) != 1 || stored[0].ID != fx.parentID {
		t.Fatalf("stored features = %d, want only the parent %s (no child created)", len(stored), fx.parentID)
	}
}

// TestRebasePreflight_FullyMergedRepoExcludedRefusesUpToDate proves a
// repository whose every layer pull request merged is classified with no
// kept layer and excluded from the work list — with no other repository
// carrying work the launch is refused with the existing already-up-to-date
// error.
func TestRebasePreflight_FullyMergedRepoExcludedRefusesUpToDate(t *testing.T) {
	fx := newRebasePreflightFixture(t, preflightFixtureOpts{AdvanceMainA: true})
	fx.remote.states[fx.prURL1] = git.PRStateMerged
	fx.remote.states[fx.prURL2] = git.PRStateMerged
	fx.remote.states[fx.prURL3] = git.PRStateMerged
	orch := fx.orchestrator()

	_, err := orch.RebaseChildPreflight(fx.parentID)
	var upToDate *feature.RebaseAlreadyUpToDateError
	if !errors.As(err, &upToDate) {
		t.Fatalf("error = %v (%T), want *RebaseAlreadyUpToDateError", err, err)
	}
	if len(upToDate.Targets) != 1 || upToDate.Targets[0].Repo != "repoa" {
		t.Fatalf("already-up-to-date targets = %+v, want repoa", upToDate.Targets)
	}
}

// TestRebasePreflight_LocalOnlyRepoClassifiedByBehindness proves a
// local-only repository — not publishable, no pull requests — classifies
// every layer as kept and enters the work list by behind-ness alone, with
// the target resolved from the local base branch.
func TestRebasePreflight_LocalOnlyRepoClassifiedByBehindness(t *testing.T) {
	fx := newRebasePreflightFixture(t, preflightFixtureOpts{AdvanceMainA: true})

	// Strip the pull requests and publishability: a local-only repository
	// whose local main advanced past the stack.
	if err := fx.store.Modify(fx.parentID, func(f *feature.Feature) error {
		notPublishable := false
		f.Repos[0].Publishable = &notPublishable
		for i := range f.Stack {
			entry := f.Stack[i].Repos["repoa"]
			entry.PRURL = ""
			entry.PRState = ""
			f.Stack[i].Repos["repoa"] = entry
		}
		return nil
	}); err != nil {
		t.Fatalf("strip pull requests: %v", err)
	}
	// The fixture's local main already advanced past the stack (squash
	// plus upstream) while the worktree sits on the top layer branch, so
	// the repository is behind its local base branch without any remote
	// read.
	orch := fx.orchestrator()
	res, err := orch.RebaseChildPreflight(fx.parentID)
	if err != nil {
		t.Fatalf("RebaseChildPreflight() error = %v", err)
	}
	if len(res.WorkRepos) != 1 || res.WorkRepos[0] != "repoa" {
		t.Fatalf("work repos = %+v, want [repoa] by behind-ness alone", res.WorkRepos)
	}
	for _, c := range res.LayerStates {
		if c.State != feature.RebaseLayerStateKept {
			t.Fatalf("layer %d classification = %q, want kept (no pull requests to read)", c.LayerPosition, c.State)
		}
	}
	if len(res.Targets) != 1 || res.Targets[0].TargetSHA != fx.targetSHA || res.Targets[0].Publishable {
		t.Fatalf("targets = %+v, want the local main head %s on a non-publishable repository", res.Targets, fx.targetSHA)
	}
}

// TestRebasePreflight_UpToDateAndBehindPairLaunchesOnlyBehindRepo proves a
// parent with one up-to-date repository and one behind repository launches
// with only the behind repository in the work list.
func TestRebasePreflight_UpToDateAndBehindPairLaunchesOnlyBehindRepo(t *testing.T) {
	fx := newRebasePreflightFixture(t, preflightFixtureOpts{WithRepoB: true, AdvanceMainA: true})
	orch := fx.orchestrator()

	res, err := orch.RebaseChildPreflight(fx.parentID)
	if err != nil {
		t.Fatalf("RebaseChildPreflight() error = %v", err)
	}
	if len(res.WorkRepos) != 1 || res.WorkRepos[0] != "repoa" {
		t.Fatalf("work repos = %+v, want exactly [repoa] (repob up to date)", res.WorkRepos)
	}
	for _, c := range res.LayerStates {
		if c.Repo == "repob" && c.State != feature.RebaseLayerStateKept {
			t.Fatalf("repob layer %d classification = %q, want kept", c.LayerPosition, c.State)
		}
	}
}

// recordLastPushedSHAs stamps every repoa layer entry's LastPushedSHA with
// the branch tip the fixture pushed, modeling the publish bookkeeping a
// published stack carries.
func (fx *rebasePreflightFixture) recordLastPushedSHAs() {
	fx.t.Helper()
	if err := fx.store.Modify(fx.parentID, func(f *feature.Feature) error {
		for i := range f.Stack {
			entry := f.Stack[i].Repos["repoa"]
			entry.LastPushedSHA = entry.TipSHA
			f.Stack[i].Repos["repoa"] = entry
		}
		return nil
	}); err != nil {
		fx.t.Fatalf("record last-pushed SHAs: %v", err)
	}
}

// reviewerClone clones repoa's bare origin so reviewer work can be authored
// and pushed without touching the inspected checkout.
func (fx *rebasePreflightFixture) reviewerClone() string {
	fx.t.Helper()
	parent := fx.t.TempDir()
	clone := filepath.Join(parent, "reviewer")
	restackGit(fx.t, parent, "clone", "--quiet", fx.bareDir["repoa"], clone)
	return clone
}

// pushReviewerCommit pushes one reviewer commit onto the named layer branch
// from a second clone of the bare origin and returns the commit's SHA.
func (fx *rebasePreflightFixture) pushReviewerCommit(branch, file, content, subject, author string) string {
	fx.t.Helper()
	clone := fx.reviewerClone()
	restackCheckout(fx.t, clone, branch)
	restackWrite(fx.t, clone, file, content)
	restackGit(fx.t, clone, "add", file)
	name, email := splitAuthor(author)
	restackCommitAs(fx.t, clone, subject, name, email)
	sha := restackGit(fx.t, clone, "rev-parse", "HEAD")
	restackGit(fx.t, clone, "push", "origin", branch)
	return sha
}

// pushReviewerMerge pushes a content-free "Update branch" merge of the base
// branch onto the named layer branch from a second clone and returns the
// merge commit's SHA. git refuses to merge an unchanged base, so the merge
// is assembled with plumbing exactly like the redundant merges the push
// proof admits.
func (fx *rebasePreflightFixture) pushReviewerMerge(branch, baseBranch string) string {
	fx.t.Helper()
	clone := fx.reviewerClone()
	restackCheckout(fx.t, clone, branch)
	tree := restackGit(fx.t, clone, "rev-parse", "HEAD^{tree}")
	merge := restackGit(fx.t, clone, "commit-tree", "-p", "HEAD", "-p", "origin/"+baseBranch, "-m", "Update branch", tree)
	restackGit(fx.t, clone, "reset", "--hard", merge)
	restackGit(fx.t, clone, "push", "origin", branch)
	return merge
}

// TestRebasePreflight_ReviewerCommitOnLayer2LaunchesOnDivergence proves
// divergence alone launches the pass: a reviewer commit pushed to layer 2's
// remote branch on a parent that is neither behind nor holds a merged layer
// puts the repository in the work list with layer 2 classified kept and
// diverged carrying the reviewer commit's SHA and the observed remote tip,
// while layers 1 and 3 are not diverged.
func TestRebasePreflight_ReviewerCommitOnLayer2LaunchesOnDivergence(t *testing.T) {
	fx := newRebasePreflightFixture(t, preflightFixtureOpts{})
	fx.recordLastPushedSHAs()
	reviewerSHA := fx.pushReviewerCommit("stack/2", "review.txt", "reviewer fix\n", "reviewer fix one", "Reviewer One <reviewer1@example.com>")
	orch := fx.orchestrator()

	res, err := orch.RebaseChildPreflight(fx.parentID)
	if err != nil {
		t.Fatalf("RebaseChildPreflight() error = %v, want launch on divergence alone", err)
	}
	if len(res.WorkRepos) != 1 || res.WorkRepos[0] != "repoa" {
		t.Fatalf("work repos = %+v, want [repoa] by divergence alone", res.WorkRepos)
	}
	if len(res.Behind) != 0 {
		t.Fatalf("behind = %+v, want none (main not advanced)", res.Behind)
	}
	var layer2 *feature.RebaseLayerClassification
	for i := range res.LayerStates {
		c := &res.LayerStates[i]
		if c.Repo != "repoa" {
			continue
		}
		switch c.LayerPosition {
		case 2:
			layer2 = c
		default:
			if c.Diverged {
				t.Fatalf("layer %d diverged = %+v, want not diverged", c.LayerPosition, c)
			}
		}
	}
	if layer2 == nil {
		t.Fatal("no classification for repoa layer 2")
	}
	if layer2.State != feature.RebaseLayerStateKept || !layer2.Diverged {
		t.Fatalf("layer 2 classification = %+v, want kept and diverged", layer2)
	}
	if len(layer2.ForeignCommits) != 1 || layer2.ForeignCommits[0].SHA != reviewerSHA {
		t.Fatalf("layer 2 foreign commits = %+v, want exactly the reviewer commit %s", layer2.ForeignCommits, reviewerSHA)
	}
	if layer2.ForeignCommits[0].Subject != "reviewer fix one" || layer2.ForeignCommits[0].Author != "Reviewer One <reviewer1@example.com>" {
		t.Fatalf("foreign commit identity = %+v, want the reviewer's subject and author", layer2.ForeignCommits[0])
	}
	if layer2.RemoteOnlyCommits != 1 {
		t.Fatalf("layer 2 remote-only count = %d, want 1", layer2.RemoteOnlyCommits)
	}
	if layer2.RemoteTip == "" || layer2.RemoteTip == fx.tip2 {
		t.Fatalf("layer 2 remote tip = %q, want the observed reviewer tip beyond %s", layer2.RemoteTip, fx.tip2)
	}
	if got := restackGit(t, fx.repoDir, "rev-parse", "refs/remotes/origin/stack/2"); got != layer2.RemoteTip {
		t.Fatalf("layer 2 remote tip %s != remote-tracking ref %s", layer2.RemoteTip, got)
	}
}

// TestRebasePreflight_MergeOnlyRemoteCommitDivergedWithEmptyForeignList
// proves a layer whose only remote-only commit is a content-free merge of
// the base still classifies diverged — with nothing to adopt — and the pass
// still launches on divergence alone.
func TestRebasePreflight_MergeOnlyRemoteCommitDivergedWithEmptyForeignList(t *testing.T) {
	fx := newRebasePreflightFixture(t, preflightFixtureOpts{})
	fx.recordLastPushedSHAs()
	mergeSHA := fx.pushReviewerMerge("stack/2", "main")
	orch := fx.orchestrator()

	res, err := orch.RebaseChildPreflight(fx.parentID)
	if err != nil {
		t.Fatalf("RebaseChildPreflight() error = %v, want launch on divergence alone", err)
	}
	if len(res.WorkRepos) != 1 || res.WorkRepos[0] != "repoa" {
		t.Fatalf("work repos = %+v, want [repoa] by divergence alone", res.WorkRepos)
	}
	var found bool
	for _, c := range res.LayerStates {
		if c.Repo == "repoa" && c.LayerPosition == 2 {
			found = true
			if !c.Diverged || c.RemoteTip != mergeSHA {
				t.Fatalf("layer 2 classification = %+v, want diverged at the merge tip %s", c, mergeSHA)
			}
			if len(c.ForeignCommits) != 0 {
				t.Fatalf("layer 2 foreign commits = %+v, want none (merge commits are never adopted)", c.ForeignCommits)
			}
			if c.RemoteOnlyCommits != 1 {
				t.Fatalf("layer 2 remote-only count = %d, want 1 (the merge)", c.RemoteOnlyCommits)
			}
		}
	}
	if !found {
		t.Fatal("no classification for repoa layer 2")
	}
}

// TestRebasePreflight_NoDivergenceNoMergedNotBehindRefusesUpToDate proves
// the already-up-to-date refusal is unchanged: a parent with no divergence,
// no merged layer, and no behind repository still refuses the launch with
// the existing already-up-to-date error.
func TestRebasePreflight_NoDivergenceNoMergedNotBehindRefusesUpToDate(t *testing.T) {
	fx := newRebasePreflightFixture(t, preflightFixtureOpts{})
	fx.recordLastPushedSHAs()
	orch := fx.orchestrator()

	_, err := orch.RebaseChildPreflight(fx.parentID)
	var upToDate *feature.RebaseAlreadyUpToDateError
	if !errors.As(err, &upToDate) {
		t.Fatalf("error = %v (%T), want *RebaseAlreadyUpToDateError", err, err)
	}
}

// TestRebasePreflight_ClosedPullRequestRefusesBeforeDivergence proves the
// closed-unmerged blocker keeps its precedence: a layer whose pull request
// reads closed refuses the launch with the stack-closed error even when
// another layer's remote branch also holds reviewer work, and no divergence
// result is persisted because no child is created.
func TestRebasePreflight_ClosedPullRequestRefusesBeforeDivergence(t *testing.T) {
	fx := newRebasePreflightFixture(t, preflightFixtureOpts{})
	fx.recordLastPushedSHAs()
	fx.remote.states[fx.prURL2] = git.PRStateClosed
	fx.pushReviewerCommit("stack/3", "review.txt", "reviewer fix\n", "reviewer fix one", "Reviewer One <reviewer1@example.com>")
	orch := fx.orchestrator()

	_, err := orch.RebaseChildPreflight(fx.parentID)
	var closed *PublishStackClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("error = %v (%T), want *PublishStackClosedError", err, err)
	}
	if closed.LayerPosition != 2 || closed.PRURL != fx.prURL2 {
		t.Fatalf("stack-closed error = %+v, want layer 2 at %s", closed, fx.prURL2)
	}
}
