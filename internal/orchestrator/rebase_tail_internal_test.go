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
	// DivergentLayer2A records repoa's layer 2 as diverged with a
	// reviewer's tip: a reviewer commit is pushed onto the remote stack/2
	// past the last-pushed SHA, the rebuilt chain adopts it, and the child
	// relationship records the divergence so closure pins the observed
	// remote tip as the layer's last-pushed SHA.
	DivergentLayer2A bool
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
	// reviewerTipA is repoa's observed remote stack/2 tip in the
	// DivergentLayer2A variant: the reviewer commit pushed past tip2.
	reviewerTipA string

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

	// The adopted divergence variant: a reviewer commit lands on the remote
	// stack/2 past the last-pushed SHA, and the objects stay in the local
	// repository so the rebuilt chain below can adopt it.
	if opts.DivergentLayer2A {
		restackGit(t, repoA, "checkout", "-b", "reviewer/stack-2", fx.tip2)
		restackWrite(t, repoA, "review.txt", "reviewer fix\n")
		restackGit(t, repoA, "add", "review.txt")
		restackCommitAs(t, repoA, "reviewer fix one", "Reviewer One", "reviewer1@example.com")
		fx.reviewerTipA = restackGit(t, repoA, "rev-parse", "HEAD")
		testutil.SimulatePush(t, repoA, bareA, "reviewer/stack-2", "stack/2")
	}

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
	// local ref is gone. In the adopted-divergence variant the reviewer's
	// commit is adopted after layer 2's own commits, so the rebuilt tip
	// contains its copy.
	restackGit(t, repoA, "checkout", "-b", "rebuilt", fx.targetSHA)
	restackGit(t, repoA, "cherry-pick", layer2First, layer2Second)
	if opts.DivergentLayer2A {
		restackGit(t, repoA, "cherry-pick", fx.reviewerTipA)
	}
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
	if opts.DivergentLayer2A {
		child.Parent.RebaseLayerStates = []feature.RebaseLayerClassification{
			{Repo: "repoa", LayerPosition: 1, LayerTitle: "Layer one", Branch: "stack/1", State: feature.RebaseLayerStateMerged},
			{
				Repo: "repoa", LayerPosition: 2, LayerTitle: "Layer two", Branch: "stack/2",
				State: feature.RebaseLayerStateKept, Diverged: true, RemoteTip: fx.reviewerTipA,
				RemoteOnlyCommits: 1,
				ForeignCommits:    []feature.RebaseForeignCommit{{SHA: fx.reviewerTipA, Subject: "reviewer fix one", Author: "Reviewer One <reviewer1@example.com>"}},
			},
			{Repo: "repoa", LayerPosition: 3, LayerTitle: "Layer three", Branch: "stack/3", State: feature.RebaseLayerStateKept},
		}
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

// runClosureFromApplied drives the closure over the landed fixture from the
// crash state between the ref transaction and closure persistence: the
// parent sits at CodeReady, the child is active with its journal in the
// applied phase and every ref already at its candidate — exactly the shape
// the startup scan finishes through closeTransactionAfterApply.
func (fx *rebaseTailFixture) runClosureFromApplied() {
	fx.t.Helper()
	if err := fx.store.Modify(fx.parentID, func(f *feature.Feature) error {
		f.Status = feature.StatusCodeReady
		return nil
	}); err != nil {
		fx.t.Fatalf("move parent to CodeReady: %v", err)
	}
	if err := fx.store.Modify(fx.childID, func(f *feature.Feature) error {
		f.Parent.CloseOutcome = ""
		f.Parent.ClosedAt = nil
		if f.Parent.Transaction != nil {
			f.Parent.Transaction.Phase = feature.TransactionPhaseApplied
		}
		return nil
	}); err != nil {
		fx.t.Fatalf("reopen the child at the applied journal: %v", err)
	}
	o := fx.orchestrator()
	if err := o.closeTransactionAfterApply(fx.childID, fx.parentID); err != nil {
		fx.t.Fatalf("closeTransactionAfterApply() error = %v", err)
	}
}

// TestRebaseClosure_PinsAdoptedRemoteTipAndTailRepublishesOverIt proves the
// Task 3 acceptance over the tail fixture's diverged-repository variant with
// the relationship recording layer 2 as diverged at the reviewer's tip:
// closure pins the layer's last-pushed SHA to the observed remote tip, the
// tail pushes layer 2 leased on exactly that SHA (and layer 3 on its
// previous last-pushed SHA), the remote branch ends at the rebuilt tip that
// contains the adopted copy, both pull-request bases are retargeted, the
// stack sections are refreshed, and no diverged record is stored. The
// closure-driven run reads the pin through the recorded lease because the
// tail's successful push then records the delivered rebuilt tip.
func TestRebaseClosure_PinsAdoptedRemoteTipAndTailRepublishesOverIt(t *testing.T) {
	fx := newRebaseTailFixture(t, rebaseTailFixtureOpts{DivergentLayer2A: true})

	fx.runClosureFromApplied()

	// The tail pushed layer 2 leased on the adopted remote tip and layer 3
	// on its previous last-pushed SHA.
	pushes := fx.repoAPushes()
	if len(pushes) != 2 {
		t.Fatalf("repoA pushes = %+v, want exactly layers 2 and 3", pushes)
	}
	if pushes[0].branch != "stack/2" || pushes[0].localSHA != fx.newTip2 || pushes[0].lastPushedSHA != fx.reviewerTipA {
		t.Errorf("layer 2 push = %+v, want %s leased on the adopted reviewer tip %s", pushes[0], fx.newTip2, fx.reviewerTipA)
	}
	if pushes[1].branch != "stack/3" || pushes[1].localSHA != fx.newTip3 || pushes[1].lastPushedSHA != fx.tip3 {
		t.Errorf("layer 3 push = %+v, want %s leased on the previous last-pushed SHA %s", pushes[1], fx.newTip3, fx.tip3)
	}

	// The remote branches end at the rebuilt tips; the adopted copy
	// replaced the reviewer's original commit.
	for _, branch := range []string{"stack/2", "stack/3"} {
		remoteTip := restackGit(t, fx.bareA, "rev-parse", "refs/heads/"+branch)
		localTip := restackGit(t, fx.repoA, "rev-parse", branch)
		if remoteTip != localTip {
			t.Errorf("remote %s = %s, want the rebuilt tip %s", branch, remoteTip, localTip)
		}
	}
	if got := restackGit(t, fx.bareA, "log", "-1", "--format=%an <%ae>", "refs/heads/stack/2"); got != "Reviewer One <reviewer1@example.com>" {
		t.Fatalf("adopted copy author on the remote = %q, want the reviewer's identity preserved", got)
	}
	if git.IsAncestor(fx.repoA, fx.reviewerTipA, "refs/heads/stack/2") {
		t.Fatal("the reviewer's original commit is still reachable from the remote layer 2 branch")
	}

	// Both bases retargeted: layer 2 patched to the base branch, layer 3
	// keeps layer 2's branch.
	patches := fx.basePatches()
	if len(patches) != 1 || patches[0].Number != fx.prNum2 || *patches[0].Base != "main" {
		t.Fatalf("base patches = %+v, want exactly layer 2 (%d) retargeted to main", patches, fx.prNum2)
	}
	if pr, ok := fx.pulls.Pull("repoa", fx.prNum3); !ok || pr.Base != "stack/2" {
		t.Fatalf("layer 3 pull request = %+v, want base stack/2 with no patch", pr)
	}

	// The stack sections list layer 1 as merged in both open pull requests.
	for _, num := range []int{fx.prNum2, fx.prNum3} {
		pr, ok := fx.pulls.Pull("repoa", num)
		if !ok || !strings.Contains(pr.Body, "(merged)") || !strings.Contains(pr.Body, "Layer one") {
			t.Fatalf("pull request %d body does not list layer 1 as merged:\n%s", num, pr.Body)
		}
	}

	// No diverged record is stored; the entries record the delivered tips.
	parent := fx.reloadParent()
	if state := parent.RepoStates["repoa"]; state != nil && state.Error != nil {
		t.Fatalf("repoa stored record = %+v, want none (the republish succeeded)", state.Error)
	}
	for _, layer := range parent.Stack {
		entry := layer.Repos["repoa"]
		switch layer.Position {
		case 2:
			if entry.LastPushedSHA != fx.newTip2 {
				t.Errorf("layer 2 last-pushed SHA = %s, want the delivered rebuilt tip %s", entry.LastPushedSHA, fx.newTip2)
			}
		case 3:
			if entry.LastPushedSHA != fx.newTip3 {
				t.Errorf("layer 3 last-pushed SHA = %s, want the delivered rebuilt tip %s", entry.LastPushedSHA, fx.newTip3)
			}
		}
	}

	// The closure finished and the tail settled with no warning.
	child := fx.reloadChild()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	if child.Parent.Transaction == nil || !child.Parent.Transaction.TailSettled {
		t.Fatalf("journal = %+v, want the tail-settled marker", child.Parent.Transaction)
	}
	if entry := child.Parent.Transaction.EntryByRepo("repoa"); entry == nil || entry.Tail != nil {
		t.Fatalf("repoA tail record = %+v, want none (no failure)", entry.Tail)
	}
}

// TestRebaseClosure_NonDivergedLayerKeepsTodaysLeases proves a repository
// whose classification has no diverged layer keeps today's leases byte for
// byte: closure pins nothing and the tail pushes layers 2 and 3 leased on
// the pre-rebase pushed SHAs exactly as before.
func TestRebaseClosure_NonDivergedLayerKeepsTodaysLeases(t *testing.T) {
	fx := newRebaseTailFixture(t, rebaseTailFixtureOpts{})

	fx.runClosureFromApplied()

	pushes := fx.repoAPushes()
	if len(pushes) != 2 {
		t.Fatalf("repoA pushes = %+v, want exactly layers 2 and 3", pushes)
	}
	if pushes[0].branch != "stack/2" || pushes[0].localSHA != fx.newTip2 || pushes[0].lastPushedSHA != fx.tip2 {
		t.Errorf("layer 2 push = %+v, want %s leased on the pre-rebase tip %s (today's lease)", pushes[0], fx.newTip2, fx.tip2)
	}
	if pushes[1].branch != "stack/3" || pushes[1].localSHA != fx.newTip3 || pushes[1].lastPushedSHA != fx.tip3 {
		t.Errorf("layer 3 push = %+v, want %s leased on the pre-rebase tip %s (today's lease)", pushes[1], fx.newTip3, fx.tip3)
	}
}

// TestRebaseClosure_SecondReviewerPushAfterPreflightRefusesAndSettles
// proves a reviewer push between preflight and the tail leaves the
// republish refused: closure still pins the observed first tip, the lease
// no longer matches the remote (which holds the second commit), the tail
// stores the diverged record for that repository, leaves the remote
// untouched, continues with the other repository, and still settles.
func TestRebaseClosure_SecondReviewerPushAfterPreflightRefusesAndSettles(t *testing.T) {
	fx := newRebaseTailFixture(t, rebaseTailFixtureOpts{DivergentLayer2A: true, WithDivergedRepoB: true})

	// A second reviewer commit lands on the remote stack/2 after the
	// preflight observation.
	restackGit(t, fx.repoA, "checkout", "-b", "reviewer/stack-2-late", fx.reviewerTipA)
	restackWrite(t, fx.repoA, "review2.txt", "late reviewer fix\n")
	restackGit(t, fx.repoA, "add", "review2.txt")
	restackCommitAs(t, fx.repoA, "reviewer fix two", "Reviewer Two", "reviewer2@example.com")
	lateTip := restackGit(t, fx.repoA, "rev-parse", "HEAD")
	testutil.SimulatePush(t, fx.repoA, fx.bareA, "reviewer/stack-2-late", "stack/2")

	fx.runClosureFromApplied()

	// repoa's republish was refused with the diverged record and the remote
	// keeps the second reviewer commit.
	parent := fx.reloadParent()
	state := parent.RepoStates["repoa"]
	if state == nil || state.Error == nil || state.Error.Code != errcat.PublishRemoteDiverged {
		t.Fatalf("repoa stored record = %+v, want publish_remote_diverged", state)
	}
	if got := restackGit(t, fx.bareA, "rev-parse", "refs/heads/stack/2"); got != lateTip {
		t.Fatalf("remote stack/2 = %s, want the second reviewer commit %s untouched", got, lateTip)
	}
	// The pinned lease proves closure recorded the first observed tip: the
	// refused push was leased on it.
	pushes := fx.repoAPushes()
	if len(pushes) != 1 || pushes[0].branch != "stack/2" || pushes[0].lastPushedSHA != fx.reviewerTipA {
		t.Fatalf("repoA pushes = %+v, want only layer 2 leased on the observed first tip %s", pushes, fx.reviewerTipA)
	}

	// The other repository still processed: its retarget happened and its
	// own diverged record stored.
	if stateB := parent.RepoStates["repob"]; stateB == nil || stateB.Error == nil || stateB.Error.Code != errcat.PublishRemoteDiverged {
		t.Fatalf("repob stored record = %+v, want publish_remote_diverged (existing behavior)", stateB)
	}
	patches := fx.basePatches()
	if len(patches) != 2 {
		t.Fatalf("base patches = %+v, want both repositories' layer 2 retargeted before the pushes", patches)
	}

	// The closure finished and the tail settled with both records stored.
	child := fx.reloadChild()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	if child.Parent.Transaction == nil || !child.Parent.Transaction.TailSettled {
		t.Fatalf("journal = %+v, want the tail-settled marker", child.Parent.Transaction)
	}
}

// TestRebaseClosure_StartupScanFinishStillPinsLastPushed proves a crash
// between the ref transaction and closure persistence, finished by the
// startup scan's closeTransactionAfterApply re-entry, still sets the
// diverged layer's last-pushed SHA to the observed remote tip.
func TestRebaseClosure_StartupScanFinishStillPinsLastPushed(t *testing.T) {
	fx := newRebaseTailFixture(t, rebaseTailFixtureOpts{DivergentLayer2A: true})

	// The crash state: refs at candidates, journal applied, child active,
	// parent CodeReady, nothing persisted — exactly what
	// runClosureFromApplied installs and the startup scan finishes.
	fx.runClosureFromApplied()

	// The pin landed: the tail's layer-2 push was leased on the observed
	// reviewer tip and the remote now holds the rebuilt tip that contains
	// the adopted copy.
	pushes := fx.repoAPushes()
	if len(pushes) != 2 || pushes[0].branch != "stack/2" || pushes[0].lastPushedSHA != fx.reviewerTipA {
		t.Fatalf("repoA pushes = %+v, want layer 2 leased on the observed reviewer tip %s", pushes, fx.reviewerTipA)
	}
	if got := restackGit(t, fx.bareA, "rev-parse", "refs/heads/stack/2"); got != fx.newTip2 {
		t.Fatalf("remote stack/2 = %s, want the rebuilt tip %s", got, fx.newTip2)
	}
}
