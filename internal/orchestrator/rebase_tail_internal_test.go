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

// Real-git coverage of the rebase child's closure tail: the base retarget
// of kept layers' open pull requests, the restricted republish walk (full
// under auto-publish, update-only under manual publish), the stack-section
// refresh listing dropped layers as merged, the advisory warning on a
// failed retarget, and the diverged-remote record that stops one repository
// without touching the others.

package orchestrator

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// rebaseTailRemoteOps keeps the pushes and pull-request operations genuine
// (real bare remotes, the in-test GitHub API) while recording every leased
// layer push and allowing individual base retargets to fail.
type rebaseTailRemoteOps struct {
	mu            sync.Mutex
	pushes        []tailLayerPush
	failBasePatch map[string]bool
}

func (r *rebaseTailRemoteOps) Push(worktreePath, branch string) error {
	return git.Push(worktreePath, branch)
}

func (r *rebaseTailRemoteOps) PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
	r.mu.Lock()
	r.pushes = append(r.pushes, tailLayerPush{repoPath: repoPath, branch: branch, localSHA: localSHA, lastPushedSHA: lastPushedSHA})
	r.mu.Unlock()
	return git.PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA)
}

func (r *rebaseTailRemoteOps) CreatePR(repoPath, branch, title, body, base string, draft bool) (string, error) {
	return git.CreatePR(repoPath, branch, title, body, draft, base)
}

func (r *rebaseTailRemoteOps) PRBaseBranch(repoPath, prURL string) string {
	return git.PRBaseBranch(repoPath, prURL)
}

func (r *rebaseTailRemoteOps) PRState(repoPath, prURL string) (string, error) {
	return git.PRState(repoPath, prURL)
}

func (r *rebaseTailRemoteOps) GetPRBody(prURL string) (string, error) {
	return git.GetPRBody(prURL)
}

func (r *rebaseTailRemoteOps) UpdatePRBody(prURL, body string) error {
	return git.UpdatePRBody(prURL, body)
}

func (r *rebaseTailRemoteOps) UpdatePRBase(prURL, base string) error {
	r.mu.Lock()
	failed := r.failBasePatch[prURL]
	r.mu.Unlock()
	if failed {
		return errors.New("retarget blocked by the test")
	}
	return git.UpdatePRBaseBranch(prURL, base)
}

func (r *rebaseTailRemoteOps) layerPushes() []tailLayerPush {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tailLayerPush(nil), r.pushes...)
}

// rebaseTailFixtureOpts configures the rebase tail fixture.
type rebaseTailFixtureOpts struct {
	// WithLayerFour adds a never-published fourth layer to the stack: a
	// kept layer with commits and a rebuilt tip but no pull request.
	WithLayerFour bool
	// AutoPublish drops the manual-publish checkpoint so the tail's walk
	// runs in full instead of update-only.
	AutoPublish bool
	// WithDivergedRepoB adds a second repository (ordered first) whose
	// kept layer's remote branch diverged, so its leased push fails while
	// the other repository still processes.
	WithDivergedRepoB bool
}

// rebaseTailFixture is a real-git three-layer stack after a completed rebase
// pass: layer 1's pull request merged (its ref deleted locally, its entry
// merged with a cleared tip), layers 2 and 3 rewritten onto the target, the
// journal merged with one delete and two rewrite refs, and pull requests on
// the fake GitHub API for every published layer.
type rebaseTailFixture struct {
	t *testing.T

	store    *feature.Store
	mgr      *feature.Manager
	parentID string
	childID  string

	repoA, bareA string
	remote       *rebaseTailRemoteOps
	pulls        *testutil.FakePullStore
	fake         *testutil.FakeGitHubAPI

	prNum1, prNum2, prNum3 int
	prURL1, prURL2, prURL3 string

	tip1, tip2, tip3, tip4    string
	targetSHA                 string
	newTip2, newTip3, newTip4 string

	repoB, bareB     string
	prNumB1, prNumB2 int
	prURLB1, prURLB2 string
	tipB1, tipB2     string
	newTip2B         string

	layerFour bool
}

// newRebaseTailFixture builds the landed rebase-pass state the tail runs
// over: real git repositories with bare remotes, a squashed-merged layer 1
// plus an upstream commit on main, the kept layers' branches already moved
// to their rebuilt tips, and pull requests served by the fake GitHub API.
func newRebaseTailFixture(t *testing.T, opts rebaseTailFixtureOpts) *rebaseTailFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("real-git rebase tail test")
	}
	fx := &rebaseTailFixture{
		t:         t,
		parentID:  "rebase-tail-parent",
		childID:   "rebase-tail-child",
		layerFour: opts.WithLayerFour,
	}

	// The fake GitHub API must be installed before any pull-request
	// helper runs; the pull store serves repoa (and repob when present).
	fx.fake = testutil.InstallFakeGitHubAPI(t)
	fx.pulls = testutil.NewFakePullStore("example")
	repoNames := []string{"repoa"}
	if opts.WithDivergedRepoB {
		repoNames = append(repoNames, "repob")
	}
	fx.pulls.Install(t, fx.fake, repoNames...)

	// repoA: fork → stack/1 (two commits) → stack/2 (two commits) →
	// stack/3 (one commit), every branch pushed to the bare remote.
	repoA := testutil.InitGitRepo(t)
	bareA := rebaseTailBareOrigin(t, repoA, "repoa")
	fx.repoA, fx.bareA = repoA, bareA

	testutil.CreateBranch(t, repoA, "stack/1")
	testutil.CommitFile(t, repoA, "l1.txt", "layer1 v1\n", "layer1 phase1")
	testutil.CommitFile(t, repoA, "l1.txt", "layer1 v2\n", "layer1 phase2")
	fx.tip1 = restackGit(t, repoA, "rev-parse", "HEAD")

	testutil.CreateBranch(t, repoA, "stack/2")
	layer2First := testutil.CommitFile(t, repoA, "l2.txt", "layer2 v1\n", "layer2 phase3")
	layer2Second := testutil.CommitFile(t, repoA, "l2.txt", "layer2 v2\n", "layer2 phase4")
	fx.tip2 = restackGit(t, repoA, "rev-parse", "HEAD")

	testutil.CreateBranch(t, repoA, "stack/3")
	layer3Commit := testutil.CommitFile(t, repoA, "l3.txt", "layer3 v1\n", "layer3 phase5")
	fx.tip3 = restackGit(t, repoA, "rev-parse", "HEAD")

	var layer4Commit string
	if opts.WithLayerFour {
		testutil.CreateBranch(t, repoA, "stack/4")
		layer4Commit = testutil.CommitFile(t, repoA, "l4.txt", "layer4 v1\n", "layer4 phase6")
		fx.tip4 = restackGit(t, repoA, "rev-parse", "HEAD")
	}

	for _, branch := range []string{"stack/1", "stack/2", "stack/3"} {
		testutil.SimulatePush(t, repoA, bareA, branch, branch)
	}
	// stack/4 is never published: it carries no remote branch, exactly as
	// a layer appended after the stack's publish does.

	// The target: main squash-merges layer 1's content and gains one
	// upstream commit, then advances the bare remote's main.
	restackCheckout(t, repoA, "main")
	restackGit(t, repoA, "checkout", "stack/1", "--", "l1.txt")
	restackCommitAs(t, repoA, "squash merge layer 1", "Main Merger", "main@example.com")
	testutil.CommitFile(t, repoA, "upstream.txt", "upstream change\n", "upstream advancement")
	fx.targetSHA = restackGit(t, repoA, "rev-parse", "HEAD")
	testutil.SimulatePush(t, repoA, bareA, "main", "main")

	// The landed chain: the kept layers' commits replayed onto the target
	// and the branches moved to their rebuilt tips; the dropped layer's
	// local ref is gone.
	restackGit(t, repoA, "checkout", "-b", "rebuilt", fx.targetSHA)
	restackGit(t, repoA, "cherry-pick", layer2First, layer2Second)
	fx.newTip2 = restackGit(t, repoA, "rev-parse", "HEAD")
	restackGit(t, repoA, "branch", "-f", "stack/2", fx.newTip2)
	restackGit(t, repoA, "cherry-pick", layer3Commit)
	fx.newTip3 = restackGit(t, repoA, "rev-parse", "HEAD")
	restackGit(t, repoA, "branch", "-f", "stack/3", fx.newTip3)
	if opts.WithLayerFour {
		restackGit(t, repoA, "cherry-pick", layer4Commit)
		fx.newTip4 = restackGit(t, repoA, "rev-parse", "HEAD")
		restackGit(t, repoA, "branch", "-f", "stack/4", fx.newTip4)
	}
	restackGit(t, repoA, "branch", "-D", "stack/1")
	topBranch := "stack/3"
	if opts.WithLayerFour {
		topBranch = "stack/4"
	}
	restackCheckout(t, repoA, topBranch)
	restackGit(t, repoA, "branch", "-D", "rebuilt")

	// The published pull requests: layer 1 merged, layers 2 and 3 open and
	// still chained on the pre-rebase bases.
	fx.prURL1, fx.prNum1 = createRebaseTailPull(t, fx.fake, "repoa", "stack/1", "main", "Layer one")
	fx.prURL2, fx.prNum2 = createRebaseTailPull(t, fx.fake, "repoa", "stack/2", "stack/1", "Layer two")
	fx.prURL3, fx.prNum3 = createRebaseTailPull(t, fx.fake, "repoa", "stack/3", "stack/2", "Layer three")
	fx.pulls.MarkMerged("repoa", fx.prNum1)

	// The optional diverged second repository: the same stack's layers 1
	// and 2 (the layer branch names are shared across repositories), with
	// remote-only work on its kept layer's branch past the last-pushed
	// SHA so the leased rewrite push refuses.
	parentRepos := []feature.FeatureRepo{{
		Name: "repoa", Path: repoA, WorktreePath: repoA,
		Branch: topBranch, BaseBranch: "main",
	}}
	// repobEntries are the second repository's per-layer entries, added to
	// the shared stack below.
	var repobEntries map[int]feature.StackRepoEntry
	var refsB []feature.RepoTransactionRef
	if opts.WithDivergedRepoB {
		repoB := testutil.InitGitRepo(t)
		bareB := rebaseTailBareOrigin(t, repoB, "repob")
		fx.repoB, fx.bareB = repoB, bareB

		testutil.CreateBranch(t, repoB, "stack/1")
		testutil.CommitFile(t, repoB, "b1.txt", "layer1 v1\n", "repoB layer one")
		fx.tipB1 = restackGit(t, repoB, "rev-parse", "HEAD")
		testutil.CreateBranch(t, repoB, "stack/2")
		testutil.CommitFile(t, repoB, "b2.txt", "layer2 v1\n", "repoB layer two")
		fx.tipB2 = restackGit(t, repoB, "rev-parse", "HEAD")
		testutil.SimulatePush(t, repoB, bareB, "stack/1", "stack/1")
		testutil.SimulatePush(t, repoB, bareB, "stack/2", "stack/2")

		// Remote-only work on stack/2 past the last-pushed SHA.
		testutil.CreateBranch(t, repoB, "diverge")
		testutil.CommitFile(t, repoB, "b2.txt", "remote-only edit\n", "remote-only change")
		testutil.SimulatePush(t, repoB, bareB, "diverge", "stack/2")

		// The rebuilt stack/2 tip locally.
		restackCheckout(t, repoB, "stack/2")
		fx.newTip2B = testutil.CommitFile(t, repoB, "b2more.txt", "more work\n", "repoB rebuilt layer two")
		restackGit(t, repoB, "branch", "-D", "diverge")
		restackGit(t, repoB, "branch", "-D", "stack/1")

		fx.prURLB1, fx.prNumB1 = createRebaseTailPull(t, fx.fake, "repob", "stack/1", "main", "Layer one")
		fx.prURLB2, fx.prNumB2 = createRebaseTailPull(t, fx.fake, "repob", "stack/2", "stack/1", "Layer two")
		fx.pulls.MarkMerged("repob", fx.prNumB1)

		// repob participates in the stack's layers 1 and 2 only; its
		// layer-3 entry pins the boundary tip (no commits of its own
		// there), so the walk marks it empty and never touches the
		// repository's absent stack/3 branch.
		repobEntries = map[int]feature.StackRepoEntry{
			1: {PRURL: fx.prURLB1, PRState: feature.StackPRStateMerged},
			2: {PRURL: fx.prURLB2, PRState: feature.StackPRStateOpen, TipSHA: fx.newTip2B, LastPushedSHA: fx.tipB2},
			3: {TipSHA: fx.newTip2B},
		}
		refsB = []feature.RepoTransactionRef{
			{Branch: "stack/1", Layer: 1, Kind: feature.RepoRefKindDelete, AnchorSHA: fx.tipB1},
			{Branch: "stack/2", Layer: 2, AnchorSHA: fx.tipB2, CandidateSHA: fx.newTip2B},
		}
		// repob is ordered first: its failure must not stop repoa.
		parentRepos = append([]feature.FeatureRepo{{
			Name: "repob", Path: repoB, WorktreePath: repoB,
			Branch: "stack/2", BaseBranch: "main",
		}}, parentRepos...)
	}

	publishable := true
	for i := range parentRepos {
		parentRepos[i].Publishable = &publishable
	}

	checkpoints := feature.Checkpoints{ManualPublish: true}
	if opts.AutoPublish {
		checkpoints = feature.Checkpoints{}
	}

	stackA := []feature.StackLayer{
		{Position: 1, Title: "Layer one", Phases: []int{1, 2}, Branch: "stack/1", Repos: map[string]feature.StackRepoEntry{
			"repoa": {PRURL: fx.prURL1, PRState: feature.StackPRStateMerged},
		}},
		{Position: 2, Title: "Layer two", Phases: []int{3, 4}, Branch: "stack/2", Repos: map[string]feature.StackRepoEntry{
			"repoa": {PRURL: fx.prURL2, PRState: feature.StackPRStateOpen, TipSHA: fx.newTip2, LastPushedSHA: fx.tip2},
		}},
		{Position: 3, Title: "Layer three", Phases: []int{5}, Branch: "stack/3", Repos: map[string]feature.StackRepoEntry{
			"repoa": {PRURL: fx.prURL3, PRState: feature.StackPRStateOpen, TipSHA: fx.newTip3, LastPushedSHA: fx.tip3},
		}},
	}
	if opts.WithLayerFour {
		stackA = append(stackA, feature.StackLayer{
			Position: 4, Title: "Layer four", Phases: []int{6}, Branch: "stack/4", Repos: map[string]feature.StackRepoEntry{
				"repoa": {TipSHA: fx.newTip4},
			},
		})
	}
	for position, entry := range repobEntries {
		for i := range stackA {
			if stackA[i].Position != position {
				continue
			}
			if stackA[i].Repos == nil {
				stackA[i].Repos = map[string]feature.StackRepoEntry{}
			}
			stackA[i].Repos["repob"] = entry
		}
	}

	refsA := []feature.RepoTransactionRef{
		{Branch: "stack/1", Layer: 1, Kind: feature.RepoRefKindDelete, AnchorSHA: fx.tip1},
		{Branch: "stack/2", Layer: 2, AnchorSHA: fx.tip2, CandidateSHA: fx.newTip2},
		{Branch: "stack/3", Layer: 3, AnchorSHA: fx.tip3, CandidateSHA: fx.newTip3},
	}
	if opts.WithLayerFour {
		refsA = append(refsA, feature.RepoTransactionRef{
			Branch: "stack/4", Layer: 4, AnchorSHA: fx.tip4, CandidateSHA: fx.newTip4,
		})
	}

	repoStates := map[string]*feature.RepoState{
		"repoa": {Touched: true},
	}
	if opts.WithDivergedRepoB {
		repoStates["repob"] = &feature.RepoState{Touched: true}
	}

	parent := &feature.Feature{
		ID:            fx.parentID,
		Name:          "Rebase tail parent",
		Slug:          fx.parentID,
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now().UTC().Truncate(time.Second),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         parentRepos,
		RepoStates:    repoStates,
		Checkpoints:   checkpoints,
	}
	parent.Stack = stackA

	child := &feature.Feature{
		ID:            fx.childID,
		Name:          "Rebase feature branches",
		Slug:          fx.childID,
		Status:        feature.StatusReviewPassed,
		CurrentPhase:  feature.PhaseFinalReview,
		Pipeline:      feature.PipelineMedium,
		Created:       time.Now().UTC().Truncate(time.Second),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID:     parent.ID,
			Kind:         feature.ChildKindRebase,
			CloseOutcome: feature.ChildCloseOutcomeCompleted,
			Transaction: &feature.TransactionJournal{
				Phase: feature.TransactionPhaseMerged,
				Entries: []feature.RepoTransactionEntry{
					{Repo: "repoa", Refs: refsA},
				},
			},
		},
	}
	if len(refsB) > 0 {
		child.Parent.Transaction.Entries = append([]feature.RepoTransactionEntry{{Repo: "repob", Refs: refsB}}, child.Parent.Transaction.Entries...)
	}

	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}
	mgr := feature.NewManager(store, config.NewDefault())
	fx.store = store
	fx.mgr = mgr
	fx.remote = &rebaseTailRemoteOps{failBasePatch: map[string]bool{}}
	return fx
}

// rebaseTailBareOrigin creates a bare remote whose path parses as a
// scp-style git@localhost:example remote, so PR creation reads
// owner/repo identity from a remote that never leaves the test machine
// while pull requests are served by the in-test GitHub fake.
func rebaseTailBareOrigin(t *testing.T, repoPath, repoName string) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "git@localhost:example", repoName)
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatalf("mkdir remotes: %v", err)
	}
	restackGit(t, filepath.Dir(remote), "clone", "--quiet", "--bare", repoPath, remote)
	restackGit(t, repoPath, "remote", "add", "origin", remote)
	testutil.SimulatePush(t, repoPath, remote, "main", "main")
	return remote
}

// createRebaseTailPull creates one pull request on the fake store and
// returns its URL and number.
func createRebaseTailPull(t *testing.T, fake *testutil.FakeGitHubAPI, repo, head, base, title string) (string, int) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"title": title, "head": head, "base": base,
		"body": "Original body for " + title + ".",
	})
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	resp, err := http.Post(fake.URL+"/repos/example/"+repo+"/pulls", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("create pull request for %s: %v", repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create pull request for %s: status %d", repo, resp.StatusCode)
	}
	var decoded struct {
		HTMLURL string `json:"html_url"`
		Number  int    `json:"number"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return decoded.HTMLURL, decoded.Number
}

// orchestrator builds the orchestrator with the recording remote and a
// scripted description session for never-published layers under
// auto-publish.
func (fx *rebaseTailFixture) orchestrator() *Orchestrator {
	fx.t.Helper()
	events := make(chan interface{}, 64)
	sm := session.NewManager(events)
	fx.t.Cleanup(sm.Shutdown)
	scriptsDir := fx.t.TempDir()
	pr := agent.NewPhaseRunner(sm, fx.store, fx.t.TempDir())
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		script := testutil.WriteScript(fx.t, scriptsDir, "description.sh", testutil.JSONLInit+"\n"+
			"read -r _agentic_init\n"+
			testutil.JSONLAssistant("Scripted PR body for "+opts.RepoName+".")+"\n"+
			testutil.JSONLSuccess+"\n")
		return []string{"bash", script}, nil, &ports.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
		}, nil
	}
	o := New(Deps{
		Lifecycle:   fx.mgr,
		Store:       fx.store,
		Remote:      fx.remote,
		Worktrees:   git.NewWorktreeManager(fx.t.TempDir()),
		PhaseRunner: pr,
	}, Hooks{})
	fx.t.Cleanup(func() {
		_ = o.Shutdown()
		o.WaitForCycles()
	})
	return o
}

// runTail loads the closed child and parent and runs the rebase tail once.
func (fx *rebaseTailFixture) runTail() error {
	fx.t.Helper()
	child, err := fx.store.Load(fx.childID)
	if err != nil {
		fx.t.Fatalf("load child: %v", err)
	}
	parent, err := fx.store.Load(fx.parentID)
	if err != nil {
		fx.t.Fatalf("load parent: %v", err)
	}
	o := fx.orchestrator()
	return o.rebaseIntegrationTail(child, parent)
}

func (fx *rebaseTailFixture) reloadChild() *feature.Feature {
	fx.t.Helper()
	child, err := fx.store.Load(fx.childID)
	if err != nil {
		fx.t.Fatalf("reload child: %v", err)
	}
	return child
}

func (fx *rebaseTailFixture) reloadParent() *feature.Feature {
	fx.t.Helper()
	parent, err := fx.store.Load(fx.parentID)
	if err != nil {
		fx.t.Fatalf("reload parent: %v", err)
	}
	return parent
}

// basePatches returns the accepted PATCH records that changed a base.
func (fx *rebaseTailFixture) basePatches() []testutil.FakePullPatch {
	var out []testutil.FakePullPatch
	for _, patch := range fx.pulls.Patches() {
		if patch.Base != nil {
			out = append(out, patch)
		}
	}
	return out
}

// repoAPushes returns the recorded layer pushes for repoA only.
func (fx *rebaseTailFixture) repoAPushes() []tailLayerPush {
	var out []tailLayerPush
	for _, push := range fx.remote.layerPushes() {
		if push.repoPath == fx.repoA {
			out = append(out, push)
		}
	}
	return out
}

// TestRebaseTail_RetargetsRepublishesAndRefreshesSections verifies the tail
// over the landed rebase fixture with pull requests on layers 1 to 3 where
// layer 1 merged: layer 2's base is patched to the base branch, layer 3
// keeps layer 2's branch, exactly layers 2 and 3 are pushed with their
// previous last-pushed SHAs as leases, no pull request is created, both
// open pull requests list layer 1 as merged in their stack sections, and
// layer 1's remote branch is untouched.
func TestRebaseTail_RetargetsRepublishesAndRefreshesSections(t *testing.T) {
	fx := newRebaseTailFixture(t, rebaseTailFixtureOpts{})

	if err := fx.runTail(); err != nil {
		t.Fatalf("rebaseIntegrationTail() error = %v", err)
	}

	// The retarget: exactly one base patch — layer 2 to the base branch.
	patches := fx.basePatches()
	if len(patches) != 1 || patches[0].Number != fx.prNum2 || *patches[0].Base != "main" {
		t.Fatalf("base patches = %+v, want exactly layer 2 (%d) retargeted to main", patches, fx.prNum2)
	}
	if pr, ok := fx.pulls.Pull("repoa", fx.prNum3); !ok || pr.Base != "stack/2" {
		t.Fatalf("layer 3 pull request = %+v, want base stack/2 with no patch", pr)
	}
	if pr, ok := fx.pulls.Pull("repoa", fx.prNum2); !ok || pr.Base != "main" {
		t.Fatalf("layer 2 pull request base = %q, want main", pr.Base)
	}

	// The republish: exactly layers 2 and 3, leased on the pre-rebase
	// pushed SHAs.
	pushes := fx.repoAPushes()
	if len(pushes) != 2 {
		t.Fatalf("repoA pushes = %+v, want exactly layers 2 and 3", pushes)
	}
	if pushes[0].branch != "stack/2" || pushes[0].localSHA != fx.newTip2 || pushes[0].lastPushedSHA != fx.tip2 {
		t.Errorf("layer 2 push = %+v, want %s leased on %s", pushes[0], fx.newTip2, fx.tip2)
	}
	if pushes[1].branch != "stack/3" || pushes[1].localSHA != fx.newTip3 || pushes[1].lastPushedSHA != fx.tip3 {
		t.Errorf("layer 3 push = %+v, want %s leased on %s", pushes[1], fx.newTip3, fx.tip3)
	}
	if got, want := fx.pulls.CreatedCount("repoa"), 3; got != want {
		t.Fatalf("repoa created pull requests = %d, want %d (no new pull request)", got, want)
	}
	for _, branch := range []string{"stack/2", "stack/3"} {
		remoteTip := restackGit(t, fx.bareA, "rev-parse", "refs/heads/"+branch)
		localTip := restackGit(t, fx.repoA, "rev-parse", branch)
		if remoteTip != localTip {
			t.Errorf("remote %s = %s, want the rebuilt tip %s", branch, remoteTip, localTip)
		}
	}
	// The dropped layer's remote branch is untouched.
	if got := restackGit(t, fx.bareA, "rev-parse", "refs/heads/stack/1"); got != fx.tip1 {
		t.Fatalf("remote stack/1 = %s, want the pre-rebase tip %s (never deleted)", got, fx.tip1)
	}

	// The stack section lists layer 1 as merged in both open pull requests.
	for _, num := range []int{fx.prNum2, fx.prNum3} {
		pr, ok := fx.pulls.Pull("repoa", num)
		if !ok {
			t.Fatalf("pull request %d missing", num)
		}
		if !strings.Contains(pr.Body, git.StackSectionHeader) {
			t.Fatalf("pull request %d body lacks the stack section:\n%s", num, pr.Body)
		}
		if !strings.Contains(pr.Body, "Layer one") || !strings.Contains(pr.Body, "(merged)") {
			t.Fatalf("pull request %d body does not list layer 1 as merged:\n%s", num, pr.Body)
		}
	}

	// The parent's entries record the pushed SHAs and the worktree stays
	// on the new top.
	parent := fx.reloadParent()
	for _, layer := range parent.Stack {
		entry := layer.Repos["repoa"]
		switch layer.Position {
		case 2:
			if entry.LastPushedSHA != fx.newTip2 {
				t.Errorf("layer 2 last-pushed SHA = %s, want the rebuilt tip %s", entry.LastPushedSHA, fx.newTip2)
			}
		case 3:
			if entry.LastPushedSHA != fx.newTip3 {
				t.Errorf("layer 3 last-pushed SHA = %s, want the rebuilt tip %s", entry.LastPushedSHA, fx.newTip3)
			}
		}
	}
	if head := restackGit(t, fx.repoA, "rev-parse", "HEAD"); head != fx.newTip3 {
		t.Fatalf("parent worktree HEAD = %s, want the new top %s", head, fx.newTip3)
	}
	if branch := restackGit(t, fx.repoA, "branch", "--show-current"); branch != "stack/3" {
		t.Fatalf("parent worktree branch = %q, want stack/3", branch)
	}

	// The tail settles with no warning.
	child := fx.reloadChild()
	journal := child.Parent.Transaction
	if journal == nil || !journal.TailSettled {
		t.Fatalf("journal = %+v, want the tail-settled marker", journal)
	}
	if entry := journal.EntryByRepo("repoa"); entry == nil || entry.Tail != nil {
		t.Fatalf("repoA tail record = %+v, want none (no failure)", entry.Tail)
	}
}

// TestRebaseTail_SettledMarkerSkipsReplay verifies that re-entering a
// settled rebase tail replays nothing: no second push, no second base
// patch, no journal churn.
func TestRebaseTail_SettledMarkerSkipsReplay(t *testing.T) {
	fx := newRebaseTailFixture(t, rebaseTailFixtureOpts{})

	if err := fx.runTail(); err != nil {
		t.Fatalf("first rebaseIntegrationTail() error = %v", err)
	}
	pushesAfterFirst := len(fx.repoAPushes())
	patchesAfterFirst := len(fx.basePatches())

	if err := fx.runTail(); err != nil {
		t.Fatalf("second rebaseIntegrationTail() error = %v", err)
	}
	if got := len(fx.repoAPushes()); got != pushesAfterFirst {
		t.Fatalf("pushes after re-entry = %d, want unchanged %d", got, pushesAfterFirst)
	}
	if got := len(fx.basePatches()); got != patchesAfterFirst {
		t.Fatalf("base patches after re-entry = %d, want unchanged %d", got, patchesAfterFirst)
	}
}

// TestRebaseTail_UpdateOnlySkipsNeverPublishedLayer verifies the manual
// publish shape with a never-published layer 4: the update-only walk pushes
// layers 2 and 3, creates nothing for layer 4, and the completion preflight
// afterwards lists layer 4 with push mode create.
func TestRebaseTail_UpdateOnlySkipsNeverPublishedLayer(t *testing.T) {
	fx := newRebaseTailFixture(t, rebaseTailFixtureOpts{WithLayerFour: true})

	if err := fx.runTail(); err != nil {
		t.Fatalf("rebaseIntegrationTail() error = %v", err)
	}

	pushes := fx.repoAPushes()
	if len(pushes) != 2 || pushes[0].branch != "stack/2" || pushes[1].branch != "stack/3" {
		t.Fatalf("repoA pushes = %+v, want exactly layers 2 and 3 (nothing for layer 4)", pushes)
	}
	if got, want := fx.pulls.CreatedCount("repoa"), 3; got != want {
		t.Fatalf("repoa created pull requests = %d, want %d (nothing created for layer 4)", got, want)
	}
	parent := fx.reloadParent()
	for _, layer := range parent.Stack {
		if layer.Position == 4 {
			entry := layer.Repos["repoa"]
			if entry.PRURL != "" || entry.TipSHA != fx.newTip4 {
				t.Fatalf("layer 4 entry = %+v, want no pull request and the rebuilt tip intact", entry)
			}
		}
	}

	preflight, err := fx.orchestrator().CompletionPreflight(fx.parentID)
	if err != nil {
		t.Fatalf("CompletionPreflight() error = %v", err)
	}
	var layerFour *CompletionPullRequestEntry
	for _, repo := range preflight.Repos {
		if repo.Repo != "repoa" {
			continue
		}
		for i := range repo.PullRequests {
			if repo.PullRequests[i].Position == 4 {
				layerFour = &repo.PullRequests[i]
			}
		}
	}
	if layerFour == nil {
		t.Fatalf("completion preflight lists no layer 4 entry: %+v", preflight.Repos)
	}
	if layerFour.PushMode != completionPushModeCreate {
		t.Fatalf("layer 4 push mode = %q, want %q", layerFour.PushMode, completionPushModeCreate)
	}
}

// TestRebaseTail_AutoPublishCreatesNeverPublishedLayerPR verifies the
// auto-publish shape: the full walk pushes layers 2 and 3 and creates layer
// 4's pull request based on layer 3's branch.
func TestRebaseTail_AutoPublishCreatesNeverPublishedLayerPR(t *testing.T) {
	fx := newRebaseTailFixture(t, rebaseTailFixtureOpts{WithLayerFour: true, AutoPublish: true})

	if err := fx.runTail(); err != nil {
		t.Fatalf("rebaseIntegrationTail() error = %v", err)
	}

	if got, want := fx.pulls.CreatedCount("repoa"), 4; got != want {
		t.Fatalf("repoa created pull requests = %d, want %d (layer 4 created)", got, want)
	}
	// The store numbers creations itself; find layer 4's record by head.
	var pr4 *testutil.FakePullRequest
	for i, created := range fx.pulls.Created() {
		if created.Repo == "repoa" && created.Head == "stack/4" {
			pr4 = &fx.pulls.Created()[i]
		}
	}
	if pr4 == nil {
		t.Fatalf("layer 4 pull request missing; created = %+v", fx.pulls.Created())
	}
	if pr4.Base != "stack/3" {
		t.Fatalf("layer 4 pull request base = %q, want layer 3's branch stack/3", pr4.Base)
	}
	if !strings.Contains(pr4.Body, "Scripted PR body") {
		t.Fatalf("layer 4 pull request body = %q, want the scripted description", pr4.Body)
	}

	pushes := fx.repoAPushes()
	if len(pushes) != 3 || pushes[2].branch != "stack/4" {
		t.Fatalf("repoA pushes = %+v, want layers 2, 3, and 4 in order", pushes)
	}
}

// TestRebaseTail_RetargetFailureRecordsWarningAndStillPushes verifies that
// a failed base retarget records the stack-base-retarget warning naming the
// repository, layer, and pull request while the push still happens and the
// tail still settles.
func TestRebaseTail_RetargetFailureRecordsWarningAndStillPushes(t *testing.T) {
	fx := newRebaseTailFixture(t, rebaseTailFixtureOpts{})
	fx.remote.mu.Lock()
	fx.remote.failBasePatch[fx.prURL2] = true
	fx.remote.mu.Unlock()

	if err := fx.runTail(); err != nil {
		t.Fatalf("rebaseIntegrationTail() error = %v", err)
	}

	child := fx.reloadChild()
	entry := child.Parent.Transaction.EntryByRepo("repoa")
	if entry == nil || entry.Tail == nil {
		t.Fatalf("repoA tail record = %+v, want the retarget warning", entry)
	}
	if entry.Tail.Code != errcat.StackBaseRetargetFailed {
		t.Fatalf("tail record code = %q, want %q", entry.Tail.Code, errcat.StackBaseRetargetFailed)
	}
	if len(entry.Tail.Context.Repositories) != 1 {
		t.Fatalf("tail record repositories = %+v, want one", entry.Tail.Context.Repositories)
	}
	repo := entry.Tail.Context.Repositories[0]
	if repo.Name != "repoa" || repo.LayerPosition != 2 || repo.LayerTitle != "Layer two" || repo.PullRequestURL != fx.prURL2 {
		t.Fatalf("tail record repository = %+v, want repoa layer 2 (%q) at %s", repo, "Layer two", fx.prURL2)
	}
	if !strings.Contains(entry.Tail.Diagnostics, `"main"`) {
		t.Fatalf("tail diagnostics = %q, want the intended base main", entry.Tail.Diagnostics)
	}
	rendered := errcat.RenderRecord(*entry.Tail)
	if !strings.Contains(rendered.Summary, "repoa") || !strings.Contains(rendered.Summary, "Layer two") || !strings.Contains(rendered.Summary, fx.prURL2) {
		t.Fatalf("rendered warning summary = %q, want repoa, layer 2, and the pull request URL", rendered.Summary)
	}

	// The push still happened and the pull request keeps its current base.
	pushes := fx.repoAPushes()
	if len(pushes) != 2 || pushes[0].branch != "stack/2" || pushes[1].branch != "stack/3" {
		t.Fatalf("repoA pushes = %+v, want layers 2 and 3 despite the retarget failure", pushes)
	}
	if pr, ok := fx.pulls.Pull("repoa", fx.prNum2); !ok || pr.Base != "stack/1" {
		t.Fatalf("layer 2 pull request base = %q, want the kept stack/1", pr.Base)
	}
	if !child.Parent.Transaction.TailSettled {
		t.Fatal("tail did not settle after the retarget failure")
	}
}

// TestRebaseTail_DivergedRemoteStoresRecordAndContinues verifies that a
// repository whose republish fails with a diverged remote stores the
// remote-diverged record on the parent and the tail continues with the next
// repository.
func TestRebaseTail_DivergedRemoteStoresRecordAndContinues(t *testing.T) {
	fx := newRebaseTailFixture(t, rebaseTailFixtureOpts{WithDivergedRepoB: true})

	if err := fx.runTail(); err != nil {
		t.Fatalf("rebaseIntegrationTail() error = %v", err)
	}

	// repoB carries the stored remote-diverged record naming its layer.
	parent := fx.reloadParent()
	state, ok := parent.RepoStates["repob"]
	if !ok || state == nil || state.Error == nil {
		t.Fatalf("repob repo state = %+v, want a stored publish-failure record", state)
	}
	if state.Error.Code != errcat.PublishRemoteDiverged {
		t.Fatalf("repob record code = %q, want %q", state.Error.Code, errcat.PublishRemoteDiverged)
	}
	if len(state.Error.Context.Repositories) != 1 || state.Error.Context.Repositories[0].LayerPosition != 2 {
		t.Fatalf("repob record repositories = %+v, want layer 2 named", state.Error.Context.Repositories)
	}

	// Both repositories retargeted first — repob's retarget succeeded
	// even though its push later refused — and repoa still processed
	// fully after repob failed: its base patch and both pushes.
	patches := fx.basePatches()
	if len(patches) != 2 {
		t.Fatalf("base patches = %+v, want repob's and repoa's layer 2 both retargeted", patches)
	}
	for _, patch := range patches {
		if patch.Number != fx.prNum2 && patch.Number != fx.prNumB2 {
			t.Fatalf("base patch = %+v, want only the two repositories' layer 2 pull requests", patch)
		}
		if *patch.Base != "main" {
			t.Fatalf("base patch for %s#%d = %q, want main", patch.Repo, patch.Number, *patch.Base)
		}
	}
	pushes := fx.repoAPushes()
	if len(pushes) != 2 || pushes[0].branch != "stack/2" || pushes[1].branch != "stack/3" {
		t.Fatalf("repoA pushes = %+v, want layers 2 and 3 after repoB failed", pushes)
	}

	// The tail settles with the failure recorded, not fatal.
	child := fx.reloadChild()
	if !child.Parent.Transaction.TailSettled {
		t.Fatal("tail did not settle after the diverged republish")
	}
}
