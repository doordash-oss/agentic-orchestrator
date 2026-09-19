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

// End-to-end journeys of the harness-driven rebase pass: a published stack
// parent whose layer 1 pull request merged and whose base advanced launches
// a rebase child through the REST surface, the start action runs the harness
// restack and the single scripted Final Review round, integration lands the
// chain through rewrite and delete refs, and the closure tail retargets
// bases, republishes the changed layers, and refreshes the stack sections.

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// rebaseHarnessOpts configures the rebase harness journey fixture.
type rebaseHarnessOpts struct {
	// Conflicting makes the target's squash commit also touch the file
	// layer 2's first commit introduces, so the harness replay genuinely
	// conflicts and the start parks the pass.
	Conflicting bool
	// WithLayerFour adds a never-published fourth layer (no pull request)
	// to the stack; the manual-publish tail must republish layers 2 and 3
	// and create nothing for it.
	WithLayerFour bool
	// FullyMergedRepoB adds a second repository whose every layer pull
	// request merged: it has no kept layer, never enters the work list,
	// and stays untouched as a pass-through repository.
	FullyMergedRepoB bool
	// WithoutPullRequests skips pull-request creation entirely: a plain
	// behind parent whose layers are all kept (no merged layer, nothing
	// ever pushed).
	WithoutPullRequests bool
	// UpToDate skips the base advancement so nothing has work and the
	// launch is refused with the already-up-to-date error.
	UpToDate bool
	// ReviewerCommitLayer2 pushes one reviewer commit (a distinct reviewer
	// identity) onto the remote stack/2 past the published tip, from a
	// second clone of the bare origin — the foreign work the adoption
	// journeys reconcile.
	ReviewerCommitLayer2 bool
	// ReviewerConflictingCommit makes the reviewer commit touch layer 2's
	// file with different content instead of adding a new file, so its
	// adoption conflicts with a locally rewritten layer 2 and runs through
	// the scripted resolution session.
	ReviewerConflictingCommit bool
	// ReviewerMergeOnlyLayer2 pushes a content-free "Update branch" merge
	// of main onto the remote stack/2 instead of a plain reviewer commit:
	// diverged with nothing to adopt.
	ReviewerMergeOnlyLayer2 bool
	// resolutionScript scripts the rebase conflict-resolution sessions' bash
	// bodies (working directory = the paused pick's temporary worktree).
	// Nil defaults to sessions that never touch the conflicted files, so
	// the attempt budget exhausts and the pass parks.
	resolutionScript func(prompt string) string
}

// rebaseHarnessJourney bundles the journey infrastructure: a real-git
// three-layer stack published against a bare origin and the in-test GitHub
// pull store, the feature store, orchestrator, server, and client.
type rebaseHarnessJourney struct {
	t *testing.T

	store    *feature.Store
	mgr      *feature.Manager
	orch     *orchestrator.Orchestrator
	pr       *agent.PhaseRunner
	srv      *httptest.Server
	client   *server.Client
	pulls    *testutil.FakePullStore
	parentID string

	repoDir    string // repo-a's parent worktree, checked out on the top layer branch
	bareRemote string
	repoBDir   string // optional second repository

	layerTips []string // original layer tips, ascending
	targetSHA string   // origin/main: layer 1 squashed plus one upstream commit
	forkSHA   string   // merge base of the target and the lowest layer tip
	// reviewerTip is the remote stack/2 tip the reviewer pushed past the
	// published tip (the plain commit or the Update branch merge).
	reviewerTip string

	prNum1, prNum2, prNum3 int
	prURL1, prURL2, prURL3 string

	// beforeApprove runs inside the scripted Final Review before the
	// approval result is delivered: the changes-requested variant commits
	// the fixer round's commit, and the gate-failure variant moves the
	// child head off the restacked chain.
	beforeApprove func(*feature.Feature)
}

// newRebaseHarnessJourney builds the published stack parent: a three-layer
// stack (fork → layer 1 phases 1-2 → layer 2 phases 3-4 → layer 3 phase 5)
// with every branch pushed to the bare origin and a pull request per layer
// on the fake GitHub API, layer 1's pull request marked merged, and main on
// the origin advanced with layer 1's squashed content plus one upstream
// commit.
func newRebaseHarnessJourney(t *testing.T, opts rebaseHarnessOpts) *rebaseHarnessJourney {
	t.Helper()
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	fx := &rebaseHarnessJourney{
		t:        t,
		parentID: "rebase-harness-parent",
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	wtBaseDir := filepath.Join(tmp, "worktrees")
	remotesDir := filepath.Join(tmp, "remotes")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	// The bare origin's path parses as a git@localhost:acme scp-style
	// remote so pull-request creation reads owner/repo identity from a
	// remote that never leaves the test machine; the fake GitHub API
	// serves the pull store.
	fx.repoDir = testutil.InitGitRepo(t)
	fx.bareRemote = stackJourneyBareOrigin(t, remotesDir, fx.repoDir, "repo-a")
	fx.forkSHA = journeyGit(t, fx.repoDir, "rev-parse", "HEAD")

	fake := testutil.InstallFakeGitHubAPI(t)
	fx.pulls = testutil.NewFakePullStore("acme")
	pullRepos := []string{"repo-a"}
	if opts.FullyMergedRepoB {
		pullRepos = append(pullRepos, "repo-b")
	}
	fx.pulls.Install(t, fake, pullRepos...)

	// The stack chain: fork → stack/1 (phases 1-2) → stack/2 (phases 3-4)
	// → stack/3 (phase 5), every commit with a distinct author so the
	// replay's author preservation stays observable.
	journeyGit(t, fx.repoDir, "checkout", "-b", "stack/1")
	testutil.CommitFile(t, fx.repoDir, "l1.txt", "layer1 v1\n", "layer1 phase1")
	testutil.CommitFile(t, fx.repoDir, "l1.txt", "layer1 v2\n", "layer1 phase2")
	fx.layerTips = append(fx.layerTips, journeyGit(t, fx.repoDir, "rev-parse", "HEAD"))

	journeyGit(t, fx.repoDir, "checkout", "-b", "stack/2")
	testutil.CommitFile(t, fx.repoDir, "l2.txt", "layer2 v1\n", "layer2 phase3")
	testutil.CommitFile(t, fx.repoDir, "l2.txt", "layer2 v2\n", "layer2 phase4")
	fx.layerTips = append(fx.layerTips, journeyGit(t, fx.repoDir, "rev-parse", "HEAD"))

	journeyGit(t, fx.repoDir, "checkout", "-b", "stack/3")
	testutil.CommitFile(t, fx.repoDir, "l3.txt", "layer3 v1\n", "layer3 phase5")
	fx.layerTips = append(fx.layerTips, journeyGit(t, fx.repoDir, "rev-parse", "HEAD"))

	topBranch := "stack/3"
	layerFourTip := ""
	if opts.WithLayerFour {
		journeyGit(t, fx.repoDir, "checkout", "-b", "stack/4")
		testutil.CommitFile(t, fx.repoDir, "l4.txt", "layer4 v1\n", "layer4 phase6")
		layerFourTip = journeyGit(t, fx.repoDir, "rev-parse", "HEAD")
		fx.layerTips = append(fx.layerTips, layerFourTip)
		topBranch = "stack/4"
	}

	for _, branch := range fx.layerBranches() {
		testutil.SimulatePush(t, fx.repoDir, fx.bareRemote, branch, branch)
	}

	// The target: origin/main squash-merges layer 1's content (and, in the
	// conflicting variant, also touches layer 2's file) and gains one
	// upstream commit.
	journeyGit(t, fx.repoDir, "checkout", "main")
	journeyGit(t, fx.repoDir, "checkout", "stack/1", "--", "l1.txt")
	if opts.Conflicting {
		writeJourneyFile(t, fx.repoDir, "l2.txt", "upstream conflicting content\n")
		journeyGit(t, fx.repoDir, "add", "l2.txt")
	}
	journeyGit(t, fx.repoDir, "commit", "-m", "squash merge layer 1")
	testutil.CommitFile(t, fx.repoDir, "upstream.txt", "upstream change\n", "upstream advancement")
	fx.targetSHA = journeyGit(t, fx.repoDir, "rev-parse", "HEAD")
	if !opts.UpToDate {
		testutil.SimulatePush(t, fx.repoDir, fx.bareRemote, "main", "main")
	} else {
		// Up to date: rewind local main to the fork point so neither the
		// local branch nor origin/main — still at the fork from the bare
		// clone's initial push — carries the squash or the upstream
		// commit; the resolved target is the fork itself and nothing is
		// behind.
		journeyGit(t, fx.repoDir, "reset", "--hard", fx.forkSHA)
		fx.targetSHA = fx.forkSHA
	}
	journeyGit(t, fx.repoDir, "checkout", topBranch)

	// The roadmap-phase anchors: every phase's commit sits on the chain.
	chainTop := fx.layerTips[len(fx.layerTips)-1]
	commits := journeyGitLines(t, fx.repoDir, "rev-list", "--reverse", fx.forkSHA+".."+chainTop)
	anchors := map[int]map[string]string{}
	for phase, sha := range commits {
		anchors[phase+1] = map[string]string{"repo-a": sha}
	}

	publishable := true
	parentRepos := []feature.FeatureRepo{{
		Name: "repo-a", Path: fx.repoDir, WorktreePath: fx.repoDir,
		Branch: topBranch, BaseBranch: "main", Publishable: &publishable,
	}}
	repoStates := map[string]*feature.RepoState{"repo-a": {Touched: true}}

	stack := []feature.StackLayer{
		{Position: 1, Title: "Layer one", Phases: []int{1, 2}, Branch: "stack/1",
			Repos: map[string]feature.StackRepoEntry{"repo-a": {TipSHA: fx.layerTips[0]}}},
		{Position: 2, Title: "Layer two", Phases: []int{3, 4}, Branch: "stack/2",
			Repos: map[string]feature.StackRepoEntry{"repo-a": {TipSHA: fx.layerTips[1]}}},
		{Position: 3, Title: "Layer three", Phases: []int{5}, Branch: "stack/3",
			Repos: map[string]feature.StackRepoEntry{"repo-a": {TipSHA: fx.layerTips[2]}}},
	}
	if opts.WithLayerFour {
		stack = append(stack, feature.StackLayer{
			Position: 4, Title: "Layer four", Phases: []int{6}, Branch: "stack/4",
			Repos: map[string]feature.StackRepoEntry{"repo-a": {TipSHA: layerFourTip}},
		})
	}

	// The published pull requests: one per layer, chained on the lower
	// layer's branch, layer 1's marked merged on the fake. The stack
	// entries deliberately record the pre-preflight view (open); the
	// preflight discovers the merged state live and persists it.
	if !opts.WithoutPullRequests {
		fx.prURL1, fx.prNum1 = createHarnessPullOnFake(t, fake, "repo-a", "stack/1", "main", "Layer one")
		fx.prURL2, fx.prNum2 = createHarnessPullOnFake(t, fake, "repo-a", "stack/2", "stack/1", "Layer two")
		fx.prURL3, fx.prNum3 = createHarnessPullOnFake(t, fake, "repo-a", "stack/3", "stack/2", "Layer three")
		for i, layer := range stack {
			entry := layer.Repos["repo-a"]
			switch layer.Position {
			case 1:
				entry.PRURL, entry.LastPushedSHA = fx.prURL1, fx.layerTips[i]
				// The up-to-date variant keeps every pull request open: a
				// merged layer with a tip is drop work, which would make the
				// repository eligible for launch.
				if !opts.UpToDate {
					fx.pulls.MarkMerged("repo-a", fx.prNum1)
				}
			case 2:
				entry.PRURL, entry.LastPushedSHA = fx.prURL2, fx.layerTips[i]
			case 3:
				entry.PRURL, entry.LastPushedSHA = fx.prURL3, fx.layerTips[i]
			}
			stack[i].Repos["repo-a"] = entry
		}
	} else if !opts.UpToDate {
		// Nothing merged and nothing published: a plain behind parent.
	}

	// The reviewer's work on layer 2's remote branch, past the published
	// tip, pushed from a second clone of the bare origin.
	if opts.ReviewerCommitLayer2 || opts.ReviewerMergeOnlyLayer2 {
		clone := fx.reviewerClone(t)
		journeyGit(t, clone, "checkout", "stack/2")
		if opts.ReviewerMergeOnlyLayer2 {
			// A content-free "Update branch" merge of the base: git refuses
			// to merge an unchanged base, so the merge is assembled with
			// plumbing exactly like the redundant merges the push proof
			// admits.
			tree := journeyGit(t, clone, "rev-parse", "HEAD^{tree}")
			fx.reviewerTip = journeyGit(t, clone, "commit-tree", "-p", "HEAD", "-p", "origin/main", "-m", "Update branch", tree)
			journeyGit(t, clone, "reset", "--hard", fx.reviewerTip)
		} else if opts.ReviewerConflictingCommit {
			writeJourneyFile(t, clone, "l2.txt", "reviewer conflicting take\n")
			journeyGit(t, clone, "add", "l2.txt")
			journeyGitAs(t, clone, "Reviewer One", "reviewer1@example.com", "commit", "-m", "reviewer fix one")
			fx.reviewerTip = journeyGit(t, clone, "rev-parse", "HEAD")
		} else {
			writeJourneyFile(t, clone, "review.txt", "reviewer fix\n")
			journeyGit(t, clone, "add", "review.txt")
			journeyGitAs(t, clone, "Reviewer One", "reviewer1@example.com", "commit", "-m", "reviewer fix one")
			fx.reviewerTip = journeyGit(t, clone, "rev-parse", "HEAD")
		}
		journeyGit(t, clone, "push", "origin", "stack/2")
	}

	if opts.FullyMergedRepoB {
		repoB := testutil.InitGitRepo(t)
		bareB := stackJourneyBareOrigin(t, remotesDir, repoB, "repo-b")
		fx.repoBDir = repoB
		journeyGit(t, repoB, "checkout", "-b", "stack/1")
		testutil.CommitFile(t, repoB, "b1.txt", "layer1 v1\n", "repoB layer one")
		journeyGit(t, repoB, "checkout", "-b", "stack/2")
		testutil.CommitFile(t, repoB, "b2.txt", "layer2 v1\n", "repoB layer two")
		journeyGit(t, repoB, "checkout", "-b", "stack/3")
		testutil.CommitFile(t, repoB, "b3.txt", "layer3 v1\n", "repoB layer three")
		testutil.SimulatePush(t, repoB, bareB, "stack/1", "stack/1")
		testutil.SimulatePush(t, repoB, bareB, "stack/2", "stack/2")
		testutil.SimulatePush(t, repoB, bareB, "stack/3", "stack/3")
		journeyGit(t, repoB, "checkout", "stack/3")

		urlB1, numB1 := createHarnessPullOnFake(t, fake, "repo-b", "stack/1", "main", "Layer one")
		urlB2, numB2 := createHarnessPullOnFake(t, fake, "repo-b", "stack/2", "stack/1", "Layer two")
		urlB3, numB3 := createHarnessPullOnFake(t, fake, "repo-b", "stack/3", "stack/2", "Layer three")
		fx.pulls.MarkMerged("repo-b", numB1)
		fx.pulls.MarkMerged("repo-b", numB2)
		fx.pulls.MarkMerged("repo-b", numB3)

		for i := range stack {
			if stack[i].Repos == nil {
				stack[i].Repos = map[string]feature.StackRepoEntry{}
			}
			entry := stack[i].Repos["repo-b"]
			switch stack[i].Position {
			case 1:
				entry.PRURL, entry.PRState, entry.TipSHA = urlB1, feature.StackPRStateOpen, journeyGit(t, repoB, "rev-parse", "stack/1")
			case 2:
				entry.PRURL, entry.PRState, entry.TipSHA = urlB2, feature.StackPRStateOpen, journeyGit(t, repoB, "rev-parse", "stack/2")
			case 3:
				entry.PRURL, entry.PRState, entry.TipSHA = urlB3, feature.StackPRStateOpen, journeyGit(t, repoB, "rev-parse", "stack/3")
			}
			stack[i].Repos["repo-b"] = entry
		}
		parentRepos = append(parentRepos, feature.FeatureRepo{
			Name: "repo-b", Path: repoB, WorktreePath: repoB,
			Branch: "stack/3", BaseBranch: "main", Publishable: &publishable,
		})
		repoStates["repo-b"] = &feature.RepoState{Touched: true}
	}

	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: fx.repoDir}
	if opts.FullyMergedRepoB {
		cfg.Repos["repo-b"] = config.RepoConfig{Path: fx.repoBDir}
	}

	store := feature.NewStore(stateDir)
	parent := &feature.Feature{
		ID:            fx.parentID,
		Name:          "Rebase harness parent",
		Slug:          fx.parentID,
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now().UTC().Truncate(time.Second),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		// An explicit implementation model keeps the scripted
		// conflict-resolution sessions launchable without a provider
		// registry: the model resolves from the feature config and the
		// BuildSessionFn seam replaces the provider entirely.
		Models:     config.ModelConfig{Implementation: "journey-resolution-model"},
		Repos:      parentRepos,
		RepoStates: repoStates,
		Checkpoints: feature.Checkpoints{
			RoadmapReview:   true,
			PhasePlanReview: true,
			ManualPublish:   true,
		},
		Stack: stack,
	}
	run := &feature.Run{
		RunNumber:                 1,
		RepoStates:                repoStates,
		Stack:                     feature.CopyStackLayers(stack),
		RoadmapPhaseCommitAnchors: anchors,
	}
	parent.SetRun(run)
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}

	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = wm

	serverEvents := make(chan interface{}, 512)
	sm := session.NewManager(serverEvents)
	t.Cleanup(sm.Shutdown)
	pr := journeyChildPhaseRunnerWithOpts(t, sm, store, stateDir, journeyPhaseRunnerOptions{
		rebaseResolution: opts.resolutionScript,
	})

	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Sessions:    sm,
		Remote:      realJourneyRemoteOps{},
		Worktrees:   wm,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})
	finalReviewStub := func(f *feature.Feature, _ ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		if fx.beforeApprove != nil {
			fx.beforeApprove(f)
		}
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	}
	orch.SetRunMultiRepoFinalReviewFn(finalReviewStub)

	stopForwarding := make(chan struct{})
	t.Cleanup(func() { close(stopForwarding) })
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

	fx.store = store
	fx.mgr = mgr
	fx.orch = orch
	fx.pr = pr
	fx.srv = srv
	fx.client = client
	return fx
}

// reviewerClone clones the bare origin so reviewer work can be authored and
// pushed without touching the inspected checkout.
func (fx *rebaseHarnessJourney) reviewerClone(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	clone := filepath.Join(parent, "repo-a")
	journeyGit(t, parent, "clone", "--quiet", fx.bareRemote, clone)
	return clone
}

// pushReviewerCommitOntoLayer2 pushes one reviewer commit onto the remote
// stack/2 from a second clone — onto whatever tip the remote currently
// holds — and returns the new remote tip.
func (fx *rebaseHarnessJourney) pushReviewerCommitOntoLayer2(t *testing.T, file, content, subject, authorName, authorEmail string) string {
	t.Helper()
	clone := fx.reviewerClone(t)
	journeyGit(t, clone, "checkout", "stack/2")
	writeJourneyFile(t, clone, file, content)
	journeyGit(t, clone, "add", file)
	journeyGitAs(t, clone, authorName, authorEmail, "commit", "-m", subject)
	sha := journeyGit(t, clone, "rev-parse", "HEAD")
	journeyGit(t, clone, "push", "origin", "stack/2")
	return sha
}

// journeyGitAs runs one git command with an explicit author and committer
// identity, for the reviewer commits whose identities the adoption records
// must preserve.
func journeyGitAs(t *testing.T, dir, authorName, authorEmail string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME="+authorName,
		"GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME="+authorName,
		"GIT_COMMITTER_EMAIL="+authorEmail,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// layerBranches returns the stack's layer branch names, ascending.
func (fx *rebaseHarnessJourney) layerBranches() []string {
	all := []string{"stack/1", "stack/2", "stack/3", "stack/4"}
	return all[:len(fx.layerTips)]
}

// realJourneyRemoteOps keeps every git and GitHub operation genuine; the
// fake GitHub API intercepts the API calls and the bare origins receive the
// pushes.
type realJourneyRemoteOps struct{}

func (realJourneyRemoteOps) Push(worktreePath, branch string) error {
	return git.Push(worktreePath, branch)
}
func (realJourneyRemoteOps) PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
	return git.PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA)
}
func (realJourneyRemoteOps) CreatePR(repoPath, branch, title, body, base string, draft bool) (string, error) {
	return git.CreatePR(repoPath, branch, title, body, draft, base)
}
func (realJourneyRemoteOps) PRBaseBranch(repoPath, prURL string) string {
	return git.PRBaseBranch(repoPath, prURL)
}
func (realJourneyRemoteOps) PRState(repoPath, prURL string) (string, error) {
	return git.PRState(repoPath, prURL)
}
func (realJourneyRemoteOps) GetPRBody(prURL string) (string, error) {
	return git.GetPRBody(prURL)
}
func (realJourneyRemoteOps) UpdatePRBody(prURL, body string) error {
	return git.UpdatePRBody(prURL, body)
}
func (realJourneyRemoteOps) UpdatePRBase(prURL, base string) error {
	return git.UpdatePRBaseBranch(prURL, base)
}
func (realJourneyRemoteOps) ReopenPullRequest(repoPath, branch, prURL string) error {
	return git.ReopenPullRequest(repoPath, branch, prURL)
}

func (fx *rebaseHarnessJourney) reloadParent() *feature.Feature {
	fx.t.Helper()
	parent, err := fx.store.Load(fx.parentID)
	if err != nil {
		fx.t.Fatalf("reload parent: %v", err)
	}
	return parent
}

func (fx *rebaseHarnessJourney) loadChild(childID string) *feature.Feature {
	fx.t.Helper()
	child, err := fx.store.Load(childID)
	if err != nil {
		fx.t.Fatalf("load child: %v", err)
	}
	return child
}

// waitForRebaseTailSettled blocks until the child's closure tail persisted
// its settled marker — the retarget, republish, and stack-section refresh
// all finished.
func (fx *rebaseHarnessJourney) waitForRebaseTailSettled(childID string) {
	fx.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		child := fx.loadChild(childID)
		if child.Parent != nil && child.Parent.Transaction != nil && child.Parent.Transaction.TailSettled {
			return
		}
		if time.Now().After(deadline) {
			fx.t.Fatalf("rebase closure tail did not settle before the deadline; journal = %+v", child.Parent.Transaction)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// harnessBasePatches returns the accepted pull-store patches that changed a
// base branch, for one repository.
func (fx *rebaseHarnessJourney) harnessBasePatches(repo string) []testutil.FakePullPatch {
	var out []testutil.FakePullPatch
	for _, patch := range fx.pulls.Patches() {
		if patch.Base != nil && patch.Repo == repo {
			out = append(out, patch)
		}
	}
	return out
}

// journeyGitLines runs one git command and splits its output into lines.
func journeyGitLines(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	out := journeyGit(t, dir, args...)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// createHarnessPullOnFake creates one pull request through the fake API and
// returns its URL and number.
func createHarnessPullOnFake(t *testing.T, fake *testutil.FakeGitHubAPI, repo, head, base, title string) (string, int) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"title": title, "head": head, "base": base,
		"body": "Original body for " + title + ".",
	})
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	resp, err := http.Post(fake.URL+"/repos/acme/"+repo+"/pulls", "application/json", bytes.NewReader(payload))
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

// TestRebaseHarnessHappyPathJourney drives the complete harness flow: launch
// through REST over a published stack whose layer 1 pull request merged and
// whose base advanced, the start action's restack, the single scripted Final
// Review approval, the rewrite/delete-ref integration, and the closure
// tail's base retarget, republish, and stack-section refresh.
func TestRebaseHarnessHappyPathJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{})

	capturedHEAD := journeyGit(t, fx.repoDir, "rev-parse", "HEAD")
	remoteRefsBefore := journeyGit(t, fx.repoDir, "ls-remote", "origin")

	resp, err := fx.client.RebaseFeature(t.Context(), fx.parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID
	if childID == "" || childID == fx.parentID {
		t.Fatalf("RebaseFeature response = %+v; want child id", resp)
	}
	if resp.Result != "created" {
		t.Fatalf("RebaseFeature result = %q, want created", resp.Result)
	}

	// The child carries the resolved target, the live layer
	// classification, and the work list.
	child := fx.loadChild(childID)
	if child.Parent == nil || child.Parent.Kind != feature.ChildKindRebase {
		t.Fatalf("child relationship = %+v, want a rebase child", child.Parent)
	}
	if child.Pipeline != feature.PipelineMedium {
		t.Fatalf("child pipeline = %q, want %q", child.Pipeline, feature.PipelineMedium)
	}
	if len(child.Parent.RebaseTargets) != 1 {
		t.Fatalf("rebase targets = %+v, want one", child.Parent.RebaseTargets)
	}
	target := child.Parent.RebaseTargets[0]
	if target.Repo != "repo-a" || target.Target != "main" || target.TargetSHA != fx.targetSHA {
		t.Fatalf("rebase target = %+v, want repo-a@main at %s", target, fx.targetSHA)
	}
	if len(child.Parent.RebaseWorkRepos) != 1 || child.Parent.RebaseWorkRepos[0] != "repo-a" {
		t.Fatalf("work repos = %+v, want [repo-a]", child.Parent.RebaseWorkRepos)
	}
	// The child's setup carries only worktree provisioning — the merge
	// setup task kind is gone.
	for key, task := range child.Run().Setup.Tasks {
		if task.Kind != feature.SetupTaskWorktree {
			t.Fatalf("setup task %q kind = %q, want only worktree tasks (no merge setup)", key, task.Kind)
		}
	}
	for _, state := range child.Parent.RebaseLayerStates {
		switch state.LayerPosition {
		case 1:
			if state.State != feature.RebaseLayerStateMerged {
				t.Errorf("layer 1 classification = %q, want merged", state.State)
			}
		case 2, 3:
			if state.State != feature.RebaseLayerStateKept {
				t.Errorf("layer %d classification = %q, want kept", state.LayerPosition, state.State)
			}
		}
	}

	// The merged state observed at preflight is persisted on the parent's
	// stack.
	parent := fx.reloadParent()
	if entry := parent.Stack[0].Repos["repo-a"]; entry.PRState != feature.StackPRStateMerged {
		t.Fatalf("parent stack layer 1 state = %q, want merged persisted at preflight", entry.PRState)
	}

	// The generated content is rebase-worded: restack invariants, worktree
	// anchoring, no branch names, no push, no fetch.
	if !strings.Contains(child.Description, "repo-a") {
		t.Fatalf("description missing repo-a: %q", child.Description)
	}
	if !strings.Contains(child.Description, "harness has already replayed") {
		t.Fatalf("description missing the replay statement: %q", child.Description)
	}
	if !strings.Contains(child.Description, "Never push") || !strings.Contains(child.Description, "Do not fetch") {
		t.Fatalf("description missing the no-push/no-fetch instructions: %q", child.Description)
	}
	if strings.Contains(child.Description, "merge setup") || strings.Contains(child.Description, "already been merged into") {
		t.Fatalf("description carries retired merge wording: %q", child.Description)
	}
	if !strings.Contains(child.ExitCriteria, "git merge-base --is-ancestor") {
		t.Fatalf("exit criteria missing the ancestor check: %q", child.ExitCriteria)
	}
	if !strings.Contains(child.ExitCriteria, "No rebase is in progress") ||
		!strings.Contains(child.ExitCriteria, "No conflict markers remain") ||
		!strings.Contains(child.ExitCriteria, "worktree is clean") {
		t.Fatalf("exit criteria missing the restack invariants: %q", child.ExitCriteria)
	}
	if !strings.Contains(child.ExitCriteria, "byte-identical to the creation-time fork point") {
		t.Fatalf("exit criteria missing the pass-through invariant: %q", child.ExitCriteria)
	}
	if !strings.Contains(child.ExitCriteria, "Nothing was pushed") || !strings.Contains(child.ExitCriteria, "Nothing was fetched") {
		t.Fatalf("exit criteria missing the no-push/no-fetch invariants: %q", child.ExitCriteria)
	}
	if strings.Contains(child.ExitCriteria, "merge setup") || strings.Contains(child.ExitCriteria, "been merged into") || strings.Contains(child.ExitCriteria, "merge commit") {
		t.Fatalf("exit criteria carry retired merge wording: %q", child.ExitCriteria)
	}

	// Fork-point pinning: the child worktree starts at the captured parent
	// tip.
	childBody := waitForJourneySetupComplete(t, fx.srv.URL, childID)
	bases, _ := childBody["bases"].([]any)
	if len(bases) != 1 {
		t.Fatalf("child bases = %v, want 1 entry", bases)
	}
	baseEntry, _ := bases[0].(map[string]any)
	if baseEntry["sha"] != capturedHEAD {
		t.Fatalf("child base SHA = %v, want the captured parent tip %s", baseEntry["sha"], capturedHEAD)
	}

	// Start runs the harness restack and the single verification round; the
	// closure tail retargets, republishes, and refreshes the sections.
	postAction(t, fx.srv.URL, childID, "start", `{}`)
	waitForJourneyChildClosed(t, fx.srv.URL, fx.store, childID)
	fx.waitForRebaseTailSettled(childID)

	// The child closed completed.
	childDetail := getJourneyJSON(t, fx.srv.URL+"/api/v1/features/"+childID)["feature"].(map[string]any)
	if childDetail["close_outcome"] != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close_outcome = %v, want completed", childDetail["close_outcome"])
	}
	child = fx.loadChild(childID)
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}

	// The parent's stack lists layer 1 merged with no tip and no local ref;
	// layers 2 and 3 carry the rebuilt tips in a linear chain from the new
	// remote base.
	parent = fx.reloadParent()
	var layer2Tip, layer3Tip string
	for _, layer := range parent.Stack {
		entry := layer.Repos["repo-a"]
		switch layer.Position {
		case 1:
			if entry.PRState != feature.StackPRStateMerged || entry.TipSHA != "" || entry.LastPushedSHA != "" || entry.PRURL != fx.prURL1 {
				t.Errorf("layer 1 entry = %+v, want merged with cleared tip and pushed SHA and the kept URL", entry)
			}
		case 2:
			layer2Tip = entry.TipSHA
		case 3:
			layer3Tip = entry.TipSHA
		}
	}
	if layer2Tip == "" || layer3Tip == "" {
		t.Fatalf("rebuilt tips missing: layer2=%q layer3=%q", layer2Tip, layer3Tip)
	}
	if _, err := journeyGitIn(fx.repoDir, "rev-parse", "--verify", "refs/heads/stack/1"); err == nil {
		t.Fatal("layer 1 local ref still exists after integration")
	}
	if got := journeyGit(t, fx.repoDir, "rev-parse", "stack/2"); got != layer2Tip {
		t.Fatalf("stack/2 = %s, want the rebuilt tip %s", got, layer2Tip)
	}
	if got := journeyGit(t, fx.repoDir, "rev-parse", "stack/3"); got != layer3Tip {
		t.Fatalf("stack/3 = %s, want the rebuilt tip %s", got, layer3Tip)
	}
	if !git.IsAncestor(fx.repoDir, fx.targetSHA, layer2Tip) || !git.IsAncestor(fx.repoDir, layer2Tip, layer3Tip) {
		t.Fatal("rebuilt refs do not form a linear chain from the new remote base")
	}
	if got := journeyGitLines(t, fx.repoDir, "log", "--format=%s", fx.targetSHA+"..stack/3"); strings.Join(got, "|") != "layer3 phase5|layer2 phase4|layer2 phase3" {
		t.Fatalf("replayed history = %v, want layers 2 and 3 only, newest first", got)
	}

	// The parent worktree is on layer 3's branch at the rebuilt top.
	if branch := journeyGit(t, fx.repoDir, "branch", "--show-current"); branch != "stack/3" {
		t.Fatalf("parent worktree branch = %q, want stack/3", branch)
	}
	if head := journeyGit(t, fx.repoDir, "rev-parse", "HEAD"); head != layer3Tip {
		t.Fatalf("parent worktree HEAD = %s, want the rebuilt top %s", head, layer3Tip)
	}

	// The tail patched layer 2's pull request to the base branch and left
	// layer 3 based on layer 2's branch; layers 2 and 3's remote branches
	// equal their new tips; layer 1's remote branch is untouched.
	patches := fx.harnessBasePatches("repo-a")
	if len(patches) != 1 || patches[0].Number != fx.prNum2 || *patches[0].Base != "main" {
		t.Fatalf("base patches = %+v, want exactly layer 2 (%d) retargeted to main", patches, fx.prNum2)
	}
	if pr, ok := fx.pulls.Pull("repo-a", fx.prNum3); !ok || pr.Base != "stack/2" {
		t.Fatalf("layer 3 pull request base = %q, want stack/2 (no patch)", pr.Base)
	}
	for branch, want := range map[string]string{"stack/2": layer2Tip, "stack/3": layer3Tip} {
		if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/"+branch); got != want {
			t.Fatalf("remote %s = %s, want the rebuilt tip %s", branch, got, want)
		}
	}
	if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/stack/1"); got != fx.layerTips[0] {
		t.Fatalf("remote stack/1 = %s, want the pre-rebase tip %s (never deleted)", got, fx.layerTips[0])
	}

	// Every open pull request body lists layer 1 as merged.
	for _, num := range []int{fx.prNum2, fx.prNum3} {
		pr, ok := fx.pulls.Pull("repo-a", num)
		if !ok {
			t.Fatalf("pull request %d missing", num)
		}
		if !strings.Contains(pr.Body, "## Stack") || !strings.Contains(pr.Body, "Layer one") || !strings.Contains(pr.Body, "(merged)") {
			t.Fatalf("pull request %d body does not list layer 1 as merged:\n%s", num, pr.Body)
		}
	}

	// The read model lists layer 1 merged and layers 2 and 3 pushed
	// up-to-date.
	preflight := getJourneyJSON(t, fx.srv.URL+"/api/v1/features/"+fx.parentID+"/completion/preflight")
	repos, _ := preflight["repos"].([]any)
	var repoEntry map[string]any
	for _, r := range repos {
		entry, _ := r.(map[string]any)
		if entry["repo"] == "repo-a" {
			repoEntry = entry
		}
	}
	if repoEntry == nil {
		t.Fatalf("completion preflight lists no repo-a entry: %+v", preflight)
	}
	prs, _ := repoEntry["pull_requests"].([]any)
	states := map[int]map[string]any{}
	for _, p := range prs {
		entry, _ := p.(map[string]any)
		if pos, ok := entry["position"].(float64); ok {
			states[int(pos)] = entry
		}
	}
	if len(states) != 3 {
		t.Fatalf("completion preflight pull requests = %+v, want three layers", prs)
	}
	if states[1] == nil || states[1]["state"] != "merged" {
		t.Fatalf("layer 1 preflight entry = %+v, want merged", states[1])
	}
	for pos := 2; pos <= 3; pos++ {
		if states[pos] == nil || states[pos]["pushed_up_to_date"] != true {
			t.Fatalf("layer %d preflight entry = %+v, want pushed up-to-date", pos, states[pos])
		}
	}

	// Nothing besides the tail's leased pushes touched the remote during
	// the pass's execution stages: the child never pushed on its own; only
	// the closure tail's two leased layer pushes moved remote refs.
	remoteRefsAfter := journeyGit(t, fx.repoDir, "ls-remote", "origin")
	if remoteRefsAfter == remoteRefsBefore {
		t.Fatal("the closure tail's republish never reached the remote")
	}
}

// driveToCompletion launches the rebase child through the REST surface,
// waits for setup, starts the pass (restack plus the single verification
// round plus integration), and waits for the closure and its tail.
func (fx *rebaseHarnessJourney) driveToCompletion(t *testing.T) string {
	t.Helper()
	resp, err := fx.client.RebaseFeature(t.Context(), fx.parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID
	if childID == "" || childID == fx.parentID {
		t.Fatalf("RebaseFeature response = %+v; want child id", resp)
	}
	waitForJourneySetupComplete(t, fx.srv.URL, childID)
	postAction(t, fx.srv.URL, childID, "start", `{}`)
	waitForJourneyChildClosed(t, fx.srv.URL, fx.store, childID)
	fx.waitForRebaseTailSettled(childID)
	return childID
}

// childWorktreePath resolves the child's first repository worktree.
func (fx *rebaseHarnessJourney) childWorktreePath(f *feature.Feature) string {
	fx.t.Helper()
	if len(f.Repos) == 0 {
		fx.t.Fatalf("child %s has no repositories", f.ID)
	}
	if f.Repos[0].WorktreePath != "" {
		return f.Repos[0].WorktreePath
	}
	return f.Repos[0].Path
}

// journeyStackRefSnapshot captures every stack layer branch and its SHA in
// one worktree, for byte-identical before/after comparisons.
func journeyStackRefSnapshot(t *testing.T, dir string) string {
	t.Helper()
	return journeyGit(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/stack*")
}

// waitForRebaseAttentionJournal polls the child's durable state until its
// transaction journal parks at attention, returning the journal.
func (fx *rebaseHarnessJourney) waitForRebaseAttentionJournal(childID string) *feature.TransactionJournal {
	fx.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		child := fx.loadChild(childID)
		if child.Parent != nil && child.Parent.Transaction != nil &&
			child.Parent.Transaction.Phase == feature.TransactionPhaseAttention {
			return child.Parent.Transaction
		}
		if child.Parent != nil && child.Parent.CloseOutcome != "" {
			fx.t.Fatalf("rebase child %s closed (%q) before parking at attention", childID, child.Parent.CloseOutcome)
		}
		if time.Now().After(deadline) {
			fx.t.Fatalf("rebase child %s never parked at attention; journal = %+v", childID, child.Parent.Transaction)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestRebaseHarnessChangesRequestedJourney drives the changes-requested
// variant: the scripted Final Review first lands a fixer-round commit on the
// child worktree — the top layer's carrier — before delivering its approval,
// and the pass still completes, with the fixer's commit as the newest commit
// of layer 3 on the remote.
func TestRebaseHarnessChangesRequestedJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{})
	fx.beforeApprove = func(f *feature.Feature) {
		wt := fx.childWorktreePath(f)
		testutil.CommitFile(fx.t, wt, "review-fix.txt", "fixer round fix\n", "fixer round: address review feedback")
	}

	childID := fx.driveToCompletion(t)
	child := fx.loadChild(childID)
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}

	parent := fx.reloadParent()
	layer3Tip := parent.Stack[2].Repos["repo-a"].TipSHA
	if layer3Tip == "" {
		t.Fatal("layer 3 rebuilt tip missing after the fixer round")
	}
	if got := journeyGit(t, fx.repoDir, "log", "-1", "--format=%s", layer3Tip); got != "fixer round: address review feedback" {
		t.Fatalf("newest commit of layer 3 = %q, want the fixer round's commit", got)
	}
	if got := journeyGit(t, fx.repoDir, "rev-parse", "refs/heads/stack/3"); got != layer3Tip {
		t.Fatalf("stack/3 = %s, want the fix-carrying tip %s", got, layer3Tip)
	}
	if !git.IsAncestor(fx.repoDir, fx.targetSHA, layer3Tip) {
		t.Fatal("the fix-carrying top does not descend from the target")
	}
	if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/stack/3"); got != layer3Tip {
		t.Fatalf("remote stack/3 = %s, want the fix-carrying tip %s (fixer commit newest on the remote)", got, layer3Tip)
	}
}

// TestRebaseHarnessConflictNeverResolvingParksJourney drives the
// never-resolving variant: the target's squash commit also touches layer 2's
// file, and the scripted resolution sessions never touch the conflicted
// file, so three attempts run and the pass parks with the rebase conflict
// resolution code carrying attempt count 3 — no ref moved, the child
// worktree stays at the parent tip, three attempt directories exist with
// feedback in the second and third prompts, no temporary worktree remains,
// and a second start re-runs the loop and parks again.
func TestRebaseHarnessConflictNeverResolvingParksJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{Conflicting: true})
	capturedHEAD := journeyGit(t, fx.repoDir, "rev-parse", "HEAD")
	stackRefsBefore := journeyStackRefSnapshot(t, fx.repoDir)

	resp, err := fx.client.RebaseFeature(t.Context(), fx.parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID
	waitForJourneySetupComplete(t, fx.srv.URL, childID)
	// Capture the conflicting commit before the pass rewrites the refs.
	commitSHA := journeyConflictCommitSHA(t, fx.repoDir, fx.forkSHA)
	postAction(t, fx.srv.URL, childID, "start", `{}`)

	tx := fx.waitForRebaseAttentionJournal(childID)
	if tx.Attention == nil || tx.Attention.Code != errcat.IntegrationRebaseConflict {
		t.Fatalf("attention record = %+v, want the rebase conflict resolution code", tx.Attention)
	}
	if !strings.Contains(tx.Attention.Diagnostics, "repo-a") || !strings.Contains(tx.Attention.Diagnostics, "l2.txt") {
		t.Fatalf("attention diagnostics = %q, want the repository and the conflicted file named", tx.Attention.Diagnostics)
	}
	if !strings.Contains(tx.Attention.Diagnostics, "exhausted 3 attempts") {
		t.Fatalf("attention diagnostics = %q, want the exhausted attempt count named", tx.Attention.Diagnostics)
	}
	if tx.Attention.Context == nil || len(tx.Attention.Context.Repositories) != 1 || tx.Attention.Context.Repositories[0].Attempts != 3 {
		t.Fatalf("attention repositories = %+v, want repo-a with attempt count 3", tx.Attention.Context)
	}

	child := fx.loadChild(childID)
	if child.Status != feature.StatusCreated {
		t.Fatalf("child status after the exhausted park = %q, want Created (start re-runs the loop)", child.Status)
	}
	childWT := fx.childWorktreePath(child)
	if got := journeyGit(t, childWT, "rev-parse", "HEAD"); got != capturedHEAD {
		t.Fatalf("child worktree HEAD = %s, want the unchanged parent tip %s", got, capturedHEAD)
	}
	if st := journeyGit(t, childWT, "status", "--porcelain"); st != "" {
		t.Fatalf("child worktree dirty after the aborted replay: %q", st)
	}
	if got := journeyStackRefSnapshot(t, fx.repoDir); got != stackRefsBefore {
		t.Fatalf("stack refs changed by the aborted replay:\nbefore:\n%s\nafter:\n%s", stackRefsBefore, got)
	}
	journeyAssertNoRestackTempWorktree(t, fx.repoDir)

	// Three attempt directories exist under the child's active run; the
	// second and third prompts carry the previous attempts' feedback.
	commitRoot := filepath.Join(fx.store.BaseDir, childID, "runs", "run-001", "rebase-resolution", "repo-a", commitSHA[:7])
	for attempt := 1; attempt <= 3; attempt++ {
		dir := filepath.Join(commitRoot, fmt.Sprintf("attempt-%02d", attempt))
		for _, name := range []string{"user-prompt.md", "feedback.md"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Fatalf("attempt %d directory missing %s: %v", attempt, name, err)
			}
		}
		if attempt > 1 {
			prompt, err := os.ReadFile(filepath.Join(dir, "user-prompt.md"))
			if err != nil {
				t.Fatalf("reading attempt %d prompt: %v", attempt, err)
			}
			if !strings.Contains(string(prompt), "conflict markers remain in: l2.txt") {
				t.Fatalf("attempt %d prompt does not carry the previous failure's feedback", attempt)
			}
		}
	}

	// Starting again re-runs the loop from scratch: the journal is cleared,
	// the sessions fail again, and the pass parks with the same code.
	postAction(t, fx.srv.URL, childID, "start", `{}`)
	tx = fx.waitForRebaseAttentionJournal(childID)
	if tx.Attention == nil || tx.Attention.Code != errcat.IntegrationRebaseConflict {
		t.Fatalf("attention record after the second start = %+v, want the rebase conflict resolution code again", tx.Attention)
	}
	if got := journeyStackRefSnapshot(t, fx.repoDir); got != stackRefsBefore {
		t.Fatalf("stack refs changed by the second aborted replay:\nbefore:\n%s\nafter:\n%s", stackRefsBefore, got)
	}
	if got := journeyGit(t, childWT, "rev-parse", "HEAD"); got != capturedHEAD {
		t.Fatalf("child worktree HEAD after the second start = %s, want the unchanged parent tip %s", got, capturedHEAD)
	}
}

// journeyConflictCommitSHA resolves the phase-3 commit — the conflicting
// replay's third commit above the fork — from the fixture chain.
func journeyConflictCommitSHA(t *testing.T, repoDir, forkSHA string) string {
	t.Helper()
	commits := journeyGitLines(t, repoDir, "rev-list", "--reverse", forkSHA+"..stack/3")
	if len(commits) < 3 {
		t.Fatalf("chain above the fork has %d commits, want at least 3", len(commits))
	}
	return commits[2]
}

// journeyAssertNoRestackTempWorktree fails when a temporary restack worktree
// is still registered in the repository. The check matches path components —
// the fixture's branch names themselves contain "restack-".
func journeyAssertNoRestackTempWorktree(t *testing.T, repoDir string) {
	t.Helper()
	out := journeyGit(t, repoDir, "worktree", "list", "--porcelain")
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		for _, part := range strings.Split(filepath.ToSlash(path), "/") {
			if strings.HasPrefix(part, "restack-") {
				t.Fatalf("a temporary restack worktree remains registered: %s", path)
			}
		}
	}
}

// TestRebaseHarnessConflictResolvesJourney drives the resolving variant: the
// scripted resolution session writes marker-free content into the conflicted
// file, so the pass completes through the scripted Final Review, the
// integration, and the tail — the parent's layer 2 is replayed onto the new
// base with the resolved content and its original author and message, layers
// 2 and 3 are republished, the child closes completed, one resolution is
// recorded on the relationship, and the start response reports the
// restack-running dispatch.
func TestRebaseHarnessConflictResolvesJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{
		Conflicting: true,
		resolutionScript: func(prompt string) string {
			// Resolve by taking the commit's own side, so the next commit of
			// the layer replays cleanly.
			return "printf 'layer2 v1\\n' > l2.txt"
		},
	})
	remoteRefsBefore := journeyGit(t, fx.repoDir, "ls-remote", "origin")

	resp, err := fx.client.RebaseFeature(t.Context(), fx.parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID
	waitForJourneySetupComplete(t, fx.srv.URL, childID)
	// Capture the conflicting commit before the pass rewrites the refs.
	conflictSHA := journeyConflictCommitSHA(t, fx.repoDir, fx.forkSHA)

	// Start the pass through the restart surface: the asynchronous restack
	// loop reports the restack-running dispatch.
	startResp := postActionJSON(t, fx.srv.URL, childID, "restart", `{}`)
	if startResp["dispatch"] != "restack" {
		t.Fatalf("restart response dispatch = %v, want restack (the loop is running)", startResp["dispatch"])
	}

	waitForJourneyChildClosed(t, fx.srv.URL, fx.store, childID)
	fx.waitForRebaseTailSettled(childID)

	child := fx.loadChild(childID)
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}

	// One resolution recorded on the relationship: the phase-3 commit, its
	// file, one attempt, not dropped.
	commitSHA := conflictSHA
	if len(child.Parent.RebaseRestacks) != 1 {
		t.Fatalf("restacks = %+v, want one entry for repo-a", child.Parent.RebaseRestacks)
	}
	rs := child.Parent.RebaseRestacks[0]
	if len(rs.ResolvedConflicts) != 1 {
		t.Fatalf("resolved conflicts = %+v, want one", rs.ResolvedConflicts)
	}
	rc := rs.ResolvedConflicts[0]
	if rc.Commit != commitSHA || rc.Attempts != 1 || rc.Dropped || len(rc.Files) != 1 || rc.Files[0] != "l2.txt" {
		t.Fatalf("resolution record = %+v, want the phase-3 commit on l2.txt after one attempt, not dropped", rc)
	}

	// The generated description names the resolved commit so the
	// verification round scrutinized it.
	if !strings.Contains(child.Description, commitSHA) || !strings.Contains(child.Description, "l2.txt") {
		t.Fatalf("description does not name the resolved commit and file:\n%s", child.Description)
	}

	// The parent's layer 2 is replayed onto the new base with the resolved
	// content and the original author and message preserved.
	parent := fx.reloadParent()
	layer2Tip := parent.Stack[1].Repos["repo-a"].TipSHA
	if layer2Tip == "" || !git.IsAncestor(fx.repoDir, fx.targetSHA, layer2Tip) {
		t.Fatalf("layer 2 tip = %q, want a rebuilt tip descending from the target", layer2Tip)
	}
	if content := journeyGit(t, fx.repoDir, "show", layer2Tip+":l2.txt"); content != "layer2 v2" {
		t.Fatalf("layer 2 tip l2.txt = %q, want the replayed phase-4 content", content)
	}
	// The phase-3 commit sits below the phase-4 tip with the resolved
	// content, its original author and message.
	phase3Replayed := journeyGit(t, fx.repoDir, "rev-parse", layer2Tip+"~1")
	if got := journeyGit(t, fx.repoDir, "show", phase3Replayed+":l2.txt"); got != "layer2 v1" {
		t.Fatalf("replayed phase-3 l2.txt = %q, want the resolved content", got)
	}
	if subject := journeyGit(t, fx.repoDir, "log", "-1", "--format=%s", phase3Replayed); subject != "layer2 phase3" {
		t.Fatalf("replayed phase-3 subject = %q, want the preserved message", subject)
	}
	origAuthor := journeyGit(t, fx.repoDir, "log", "-1", "--format=%an <%ae>", commitSHA)
	if author := journeyGit(t, fx.repoDir, "log", "-1", "--format=%an <%ae>", phase3Replayed); author != origAuthor {
		t.Fatalf("replayed phase-3 author = %q, want the preserved %q", author, origAuthor)
	}

	// Layers 2 and 3 were republished to the rebuilt tips.
	for _, branch := range []string{"stack/2", "stack/3"} {
		want := parentStackTip(parent, branch)
		if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/"+branch); got != want {
			t.Fatalf("remote %s = %s, want the rebuilt tip %s", branch, got, want)
		}
	}
	if got := journeyGit(t, fx.repoDir, "ls-remote", "origin"); got == remoteRefsBefore {
		t.Fatal("the closure tail's republish never reached the remote")
	}
	journeyAssertNoRestackTempWorktree(t, fx.repoDir)
}

// TestRebaseHarnessConflictOutOfScopeJourney drives the out-of-scope
// variant: the first scripted resolution session also writes an extra file
// outside the conflicted set, which the harness reverts before recording the
// failed attempt; the second session resolves cleanly and the pass completes
// with a landed commit that does not contain the extra file.
func TestRebaseHarnessConflictOutOfScopeJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{
		Conflicting: true,
		resolutionScript: func(prompt string) string {
			if strings.Contains(prompt, "Previous Attempt Feedback") {
				// The retry resolves cleanly.
				return "printf 'layer2 v1\\n' > l2.txt"
			}
			// The first attempt edits outside the conflicted set and leaves
			// the markers in place.
			return "printf 'out of scope\\n' > extra.txt"
		},
	})

	resp, err := fx.client.RebaseFeature(t.Context(), fx.parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID
	waitForJourneySetupComplete(t, fx.srv.URL, childID)
	// Capture the conflicting commit before the pass rewrites the refs.
	commitSHA := journeyConflictCommitSHA(t, fx.repoDir, fx.forkSHA)
	postAction(t, fx.srv.URL, childID, "start", `{}`)
	waitForJourneyChildClosed(t, fx.srv.URL, fx.store, childID)
	fx.waitForRebaseTailSettled(childID)

	child := fx.loadChild(childID)
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	rs := child.Parent.RebaseRestacks[0]
	if len(rs.ResolvedConflicts) != 1 || rs.ResolvedConflicts[0].Attempts != 2 {
		t.Fatalf("resolved conflicts = %+v, want one resolution after two attempts", rs.ResolvedConflicts)
	}

	// The first attempt's feedback names the reverted out-of-scope file, and
	// the second attempt's prompt carries it.
	commitRoot := filepath.Join(fx.store.BaseDir, childID, "runs", "run-001", "rebase-resolution", "repo-a", commitSHA[:7])
	feedback, err := os.ReadFile(filepath.Join(commitRoot, "attempt-01", "feedback.md"))
	if err != nil {
		t.Fatalf("reading attempt-01 feedback: %v", err)
	}
	if !strings.Contains(string(feedback), "extra.txt") {
		t.Fatalf("attempt-01 feedback does not name the out-of-scope file:\n%s", feedback)
	}
	prompt, err := os.ReadFile(filepath.Join(commitRoot, "attempt-02", "user-prompt.md"))
	if err != nil {
		t.Fatalf("reading attempt-02 prompt: %v", err)
	}
	if !strings.Contains(string(prompt), "extra.txt") {
		t.Fatalf("attempt-02 prompt does not carry the out-of-scope feedback:\n%s", prompt)
	}

	// The landed chain and the parent worktree do not contain the extra
	// file: the out-of-scope edit never reached a commit.
	parent := fx.reloadParent()
	layer3Tip := parent.Stack[2].Repos["repo-a"].TipSHA
	if layer3Tip == "" {
		t.Fatal("layer 3 rebuilt tip missing")
	}
	if _, err := journeyGitIn(fx.repoDir, "show", layer3Tip+":extra.txt"); err == nil {
		t.Fatal("the landed chain contains the out-of-scope extra.txt")
	}
	if _, err := os.Stat(filepath.Join(fx.repoDir, "extra.txt")); !os.IsNotExist(err) {
		t.Fatal("the parent worktree contains the out-of-scope extra.txt")
	}
	journeyAssertNoRestackTempWorktree(t, fx.repoDir)
}

// TestRebaseHarnessConflictStopJourney drives the stop variant: the child is
// stopped during the first resolution attempt — the scripted session sleeps
// so the stop lands mid-attempt — and the pass ends with the child at
// Created, no attention journal, no ref change, and no temporary worktree.
func TestRebaseHarnessConflictStopJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{
		Conflicting: true,
		resolutionScript: func(prompt string) string {
			// Sleep long enough for the stop to land mid-attempt; the
			// session is killed before the sleep finishes.
			return "sleep 60\nprintf 'layer2 v1\\n' > l2.txt"
		},
	})
	capturedHEAD := journeyGit(t, fx.repoDir, "rev-parse", "HEAD")
	stackRefsBefore := journeyStackRefSnapshot(t, fx.repoDir)

	resp, err := fx.client.RebaseFeature(t.Context(), fx.parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID
	waitForJourneySetupComplete(t, fx.srv.URL, childID)
	postAction(t, fx.srv.URL, childID, "start", `{}`)

	// Stop the child while the resolution attempt runs.
	postAction(t, fx.srv.URL, childID, "pause-stop", `{}`)
	waitForJourneyFeatureSessionsQuiescent(t, fx.srv.URL, childID)

	// The loop aborted without parking: the child stays at Created with no
	// attention journal, and the paused pick's temporary worktree is gone.
	deadline := time.Now().Add(30 * time.Second)
	for journeyRestackTempWorktreeListed(t, fx.repoDir) {
		if time.Now().After(deadline) {
			t.Fatal("the temporary restack worktree was never removed after the stop")
		}
		time.Sleep(100 * time.Millisecond)
	}
	child := fx.loadChild(childID)
	if child.Status != feature.StatusCreated {
		t.Fatalf("child status after the stop = %q, want Created (the pass's home state)", child.Status)
	}
	if child.Parent.Transaction != nil {
		t.Fatalf("transaction = %+v, want no attention journal after a stop", child.Parent.Transaction)
	}
	if child.Parent.CloseOutcome != "" {
		t.Fatalf("child closed (%q) after a stop", child.Parent.CloseOutcome)
	}
	if got := journeyStackRefSnapshot(t, fx.repoDir); got != stackRefsBefore {
		t.Fatalf("stack refs changed by the stopped pass:\nbefore:\n%s\nafter:\n%s", stackRefsBefore, got)
	}
	childWT := fx.childWorktreePath(child)
	if got := journeyGit(t, childWT, "rev-parse", "HEAD"); got != capturedHEAD {
		t.Fatalf("child worktree HEAD = %s, want the unchanged parent tip %s", got, capturedHEAD)
	}
	if st := journeyGit(t, childWT, "status", "--porcelain"); st != "" {
		t.Fatalf("child worktree dirty after the stop: %q", st)
	}
}

// journeyRestackTempWorktreeListed reports whether a temporary restack
// worktree is still registered in the repository.
func journeyRestackTempWorktreeListed(t *testing.T, repoDir string) bool {
	t.Helper()
	out := journeyGit(t, repoDir, "worktree", "list", "--porcelain")
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		for _, part := range strings.Split(filepath.ToSlash(path), "/") {
			if strings.HasPrefix(part, "restack-") {
				return true
			}
		}
	}
	return false
}

// postActionJSON posts one feature action and decodes the successful
// response body as a JSON object.
func postActionJSON(t *testing.T, baseURL, featureID, action, body string) map[string]any {
	t.Helper()
	status, payload := postActionStatus(t, baseURL, featureID, action, body)
	if status != http.StatusOK {
		t.Fatalf("POST action %s status = %d; body: %s", action, status, payload)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode action %s response %s: %v", action, payload, err)
	}
	return decoded
}

// TestRebaseHarnessTwoRepoFullyMergedJourney drives the two-repository
// variant: repo-a's layer 1 merged and its base advanced while repo-b's
// every layer pull request merged, so only repo-a enters the work list and
// is rebuilt; repo-b stays a byte-identical pass-through repository.
func TestRebaseHarnessTwoRepoFullyMergedJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{FullyMergedRepoB: true})
	repoBHeadBefore := journeyGit(t, fx.repoBDir, "rev-parse", "HEAD")
	repoBRefsBefore := journeyStackRefSnapshot(t, fx.repoBDir)

	childID := fx.driveToCompletion(t)

	child := fx.loadChild(childID)
	if len(child.Parent.RebaseWorkRepos) != 1 || child.Parent.RebaseWorkRepos[0] != "repo-a" {
		t.Fatalf("work repos = %+v, want exactly [repo-a] (repo-b fully merged)", child.Parent.RebaseWorkRepos)
	}
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}

	// repo-a was rebuilt: layer 1's ref is gone and layer 2 chains from the
	// target.
	parent := fx.reloadParent()
	if _, err := journeyGitIn(fx.repoDir, "rev-parse", "--verify", "refs/heads/stack/1"); err == nil {
		t.Fatal("repo-a layer 1 local ref still exists after integration")
	}
	layer2Tip := parent.Stack[1].Repos["repo-a"].TipSHA
	if layer2Tip == "" || !git.IsAncestor(fx.repoDir, fx.targetSHA, layer2Tip) {
		t.Fatalf("repo-a layer 2 tip = %q, want a rebuilt tip descending from the target", layer2Tip)
	}

	// repo-b stayed untouched: every stack ref and its worktree are
	// byte-identical, and the preflight-persisted merged states remain.
	if got := journeyStackRefSnapshot(t, fx.repoBDir); got != repoBRefsBefore {
		t.Fatalf("repo-b stack refs changed by the pass:\nbefore:\n%s\nafter:\n%s", repoBRefsBefore, got)
	}
	if got := journeyGit(t, fx.repoBDir, "rev-parse", "HEAD"); got != repoBHeadBefore {
		t.Fatalf("repo-b worktree HEAD = %s, want the untouched %s", got, repoBHeadBefore)
	}
	if branch := journeyGit(t, fx.repoBDir, "branch", "--show-current"); branch != "stack/3" {
		t.Fatalf("repo-b worktree branch = %q, want stack/3", branch)
	}
	if st := journeyGit(t, fx.repoBDir, "status", "--porcelain"); st != "" {
		t.Fatalf("repo-b worktree dirty after the pass: %q", st)
	}
	for _, layer := range parent.Stack {
		if entry := layer.Repos["repo-b"]; entry.PRState != feature.StackPRStateMerged {
			t.Fatalf("repo-b layer %d entry = %+v, want the merged state persisted at preflight", layer.Position, entry)
		}
	}
}

// TestRebaseHarnessUpToDateRefusesLaunchJourney drives the up-to-date
// variant: nothing advanced and no layer merged, so the launch is refused
// with the existing already-up-to-date conflict error and no child is
// created.
func TestRebaseHarnessUpToDateRefusesLaunchJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{UpToDate: true})

	status, payload := postActionStatus(t, fx.srv.URL, fx.parentID, "rebase", `{}`)
	if status != http.StatusConflict {
		t.Fatalf("rebase action status = %d, want %d (already up to date); body: %s", status, http.StatusConflict, payload)
	}
	var errResp map[string]any
	if err := json.Unmarshal(payload, &errResp); err != nil {
		t.Fatalf("decode rebase error body %s: %v", payload, err)
	}
	errInfo, _ := errResp["error"].(map[string]any)
	code, _ := errInfo["code"].(string)
	if code != "rebase_already_up_to_date" {
		t.Fatalf("error code = %q, want rebase_already_up_to_date", code)
	}

	features, err := fx.store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, f := range features {
		if f.IsChild() && f.Parent != nil && f.Parent.ParentID == fx.parentID {
			t.Fatalf("unexpected child %s created for the up-to-date parent", f.ID)
		}
	}
}

// TestRebaseHarnessManualPublishUpdateOnlyJourney drives the manual-publish
// variant: a never-published fourth layer sits on top of a stack whose
// layers 1 to 3 carry pull requests, so the closure tail runs the update-only
// walk — republishing layers 2 and 3 and retargeting layer 2's base —
// creating nothing for layer 4, which the completion preflight afterwards
// lists with push mode create.
func TestRebaseHarnessManualPublishUpdateOnlyJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{WithLayerFour: true})
	createdBefore := fx.pulls.CreatedCount("repo-a")

	childID := fx.driveToCompletion(t)
	child := fx.loadChild(childID)
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}

	parent := fx.reloadParent()
	layer4Tip := parent.Stack[3].Repos["repo-a"].TipSHA
	if layer4Tip == "" {
		t.Fatal("layer 4 rebuilt tip missing after the pass")
	}
	if branch := journeyGit(t, fx.repoDir, "branch", "--show-current"); branch != "stack/4" {
		t.Fatalf("parent worktree branch = %q, want the new top stack/4", branch)
	}
	if got := journeyGit(t, fx.repoDir, "rev-parse", "HEAD"); got != layer4Tip {
		t.Fatalf("parent worktree HEAD = %s, want the rebuilt top %s", got, layer4Tip)
	}

	// The tail retargeted exactly layer 2's pull request to the base branch.
	patches := fx.harnessBasePatches("repo-a")
	if len(patches) != 1 || patches[0].Number != fx.prNum2 || *patches[0].Base != "main" {
		t.Fatalf("base patches = %+v, want exactly layer 2 (%d) retargeted to main", patches, fx.prNum2)
	}

	// Layers 2 and 3 were force-pushed to their rebuilt tips; layer 4 —
	// never published — was not pushed and nothing was created for it.
	for _, branch := range []string{"stack/2", "stack/3"} {
		want := parentStackTip(parent, branch)
		if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/"+branch); got != want {
			t.Fatalf("remote %s = %s, want the rebuilt tip %s", branch, got, want)
		}
	}
	if got := journeyGit(t, fx.bareRemote, "rev-parse", "refs/heads/stack/4"); got != fx.layerTips[3] {
		t.Fatalf("remote stack/4 = %s, want the never-pushed original tip %s", got, fx.layerTips[3])
	}
	if created := fx.pulls.CreatedCount("repo-a"); created != createdBefore {
		t.Fatalf("pull requests created by the update-only tail = %d, want none (before = %d)", created-createdBefore, createdBefore)
	}
	for _, num := range []int{fx.prNum2, fx.prNum3} {
		pr, ok := fx.pulls.Pull("repo-a", num)
		if !ok || !strings.Contains(pr.Body, "(merged)") {
			t.Fatalf("pull request %d body does not list layer 1 as merged:\n%s", num, pr.Body)
		}
	}

	// The completion preflight lists layer 4 with push mode create while
	// the republished layers read pushed up-to-date.
	preflight := getJourneyJSON(t, fx.srv.URL+"/api/v1/features/"+fx.parentID+"/completion/preflight")
	repos, _ := preflight["repos"].([]any)
	var repoEntry map[string]any
	for _, r := range repos {
		entry, _ := r.(map[string]any)
		if entry["repo"] == "repo-a" {
			repoEntry = entry
		}
	}
	if repoEntry == nil {
		t.Fatalf("completion preflight lists no repo-a entry: %+v", preflight)
	}
	prs, _ := repoEntry["pull_requests"].([]any)
	modes := map[int]map[string]any{}
	for _, p := range prs {
		entry, _ := p.(map[string]any)
		if pos, ok := entry["position"].(float64); ok {
			modes[int(pos)] = entry
		}
	}
	if modes[4] == nil || modes[4]["push_mode"] != "create" {
		t.Fatalf("layer 4 preflight entry = %+v, want push mode create", modes[4])
	}
	for pos := 2; pos <= 3; pos++ {
		if modes[pos] == nil || modes[pos]["pushed_up_to_date"] != true {
			t.Fatalf("layer %d preflight entry = %+v, want pushed up-to-date", pos, modes[pos])
		}
	}
}

// parentStackTip resolves the parent's recorded tip for one stack branch.
func parentStackTip(parent *feature.Feature, branch string) string {
	for _, layer := range parent.Stack {
		if layer.Branch == branch {
			return layer.Repos["repo-a"].TipSHA
		}
	}
	return ""
}

// TestRebaseHarnessGateFailureJourney drives the gate-failure variant of the
// no-merge flow: the scripted review moves the child head off the restacked
// chain before approving, so the integration preparation parks the pass with
// the candidate-failed attention — the child head does not descend from the
// rebuilt top — without touching any parent ref.
func TestRebaseHarnessGateFailureJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{})
	stackRefsBefore := journeyStackRefSnapshot(t, fx.repoDir)
	fx.beforeApprove = func(f *feature.Feature) {
		wt := fx.childWorktreePath(f)
		journeyGit(fx.t, wt, "reset", "--hard", fx.targetSHA)
		testutil.CommitFile(fx.t, wt, "offchain.txt", "off-chain fix\n", "off-chain commit outside the restacked chain")
	}

	resp, err := fx.client.RebaseFeature(t.Context(), fx.parentID)
	if err != nil {
		t.Fatalf("RebaseFeature() error = %v", err)
	}
	childID := resp.FeatureID
	waitForJourneySetupComplete(t, fx.srv.URL, childID)
	postAction(t, fx.srv.URL, childID, "start", `{}`)

	tx := fx.waitForRebaseAttentionJournal(childID)
	if tx.Attention == nil || tx.Attention.Code != errcat.IntegrationCandidateFailed {
		t.Fatalf("attention record = %+v, want the candidate-failed code", tx.Attention)
	}
	if !strings.Contains(tx.Attention.Diagnostics, "does not descend from the rebuilt top") {
		t.Fatalf("attention diagnostics = %q, want the off-chain child head detail", tx.Attention.Diagnostics)
	}

	child := fx.loadChild(childID)
	if child.Parent.CloseOutcome != "" {
		t.Fatalf("child closed (%q) although the integration parked at attention", child.Parent.CloseOutcome)
	}
	if got := journeyStackRefSnapshot(t, fx.repoDir); got != stackRefsBefore {
		t.Fatalf("parent stack refs changed by the parked integration:\nbefore:\n%s\nafter:\n%s", stackRefsBefore, got)
	}
}

// closedRefusalFeatureDetail fetches the feature detail object over REST.
func closedRefusalFeatureDetail(t *testing.T, baseURL, featureID string) map[string]any {
	t.Helper()
	detail, _ := getJourneyJSON(t, baseURL+"/api/v1/features/"+featureID)["feature"].(map[string]any)
	if detail == nil {
		t.Fatalf("feature detail for %s missing or malformed", featureID)
	}
	return detail
}

// closedRefusalRepoError returns the catalog-rendered stored error record one
// repository owns in the feature detail, or nil when the repository carries
// none.
func closedRefusalRepoError(t *testing.T, baseURL, featureID, repo string) map[string]any {
	t.Helper()
	repos, _ := closedRefusalFeatureDetail(t, baseURL, featureID)["repo_status"].([]any)
	for _, r := range repos {
		entry, _ := r.(map[string]any)
		if entry["name"] == repo {
			record, _ := entry["error"].(map[string]any)
			return record
		}
	}
	t.Fatalf("feature detail for %s lists no %s repository status entry", featureID, repo)
	return nil
}

// closedRefusalActionEnabled reports whether the feature's action catalog
// lists one action as enabled; a missing action reads as disabled.
func closedRefusalActionEnabled(t *testing.T, baseURL, featureID, actionID string) bool {
	t.Helper()
	actions, _ := closedRefusalFeatureDetail(t, baseURL, featureID)["actions"].([]any)
	for _, a := range actions {
		entry, _ := a.(map[string]any)
		if entry["id"] == actionID {
			enabled, _ := entry["enabled"].(bool)
			return enabled
		}
	}
	return false
}

// closedRefusalSourceRevision reads the feature's current meta revision, the
// source-revision guard the resolution actions carry.
func closedRefusalSourceRevision(t *testing.T, baseURL, featureID string) string {
	t.Helper()
	// The stale guard compares against the completion preflight's worktree
	// fingerprint, not the detail payload hash, so the journey reads the
	// revision from the same surface the desktop does.
	out := getJourneyJSON(t, baseURL+"/api/v1/features/"+featureID+"/completion/preflight")
	if revision, ok := out["source_revision"].(string); ok && revision != "" {
		return revision
	}
	t.Fatalf("completion preflight source revision for %s missing: %+v", featureID, out)
	return ""
}

// closedRefusalRepositoryContext returns the single repository context entry
// of a canonical wire error, failing when the block is absent or ambiguous.
func closedRefusalRepositoryContext(t *testing.T, errInfo map[string]any) map[string]any {
	t.Helper()
	ctxBlock, _ := errInfo["context"].(map[string]any)
	if ctxBlock == nil {
		t.Fatalf("canonical error carries no context: %+v", errInfo)
	}
	repos, _ := ctxBlock["repositories"].([]any)
	if len(repos) != 1 {
		t.Fatalf("canonical error repository context = %+v, want exactly one entry", repos)
	}
	entry, _ := repos[0].(map[string]any)
	if entry == nil {
		t.Fatalf("canonical error repository context entry malformed: %+v", repos[0])
	}
	return entry
}

// TestRebaseClosedPullRequestRefusalResolvedThroughReopenJourney drives the
// closed-pull-request refusal variant: layer 2's pull request reads closed on
// the remote, so the rebase launch is refused over REST with the canonical
// closed code — whose repository context names the repository, the layer
// position, the layer title, and the pull request URL — no child is created,
// and the repository is parked with the stored closed record while the action
// catalog enables reopen-pull-request. The Reopen action then flips the pull
// request back to open on the fake, clears the stored record, and records the
// layer open.
func TestRebaseClosedPullRequestRefusalResolvedThroughReopenJourney(t *testing.T) {
	fx := newRebaseHarnessJourney(t, rebaseHarnessOpts{})

	// An external close of layer 2's pull request on the remote.
	if !fx.pulls.MarkClosed("repo-a", fx.prNum2) {
		t.Fatalf("marking layer 2 pull request %d closed on the fake", fx.prNum2)
	}

	// The rebase launch over REST is refused with the canonical closed code.
	status, payload := postActionStatus(t, fx.srv.URL, fx.parentID, "rebase", `{}`)
	if status != http.StatusConflict {
		t.Fatalf("rebase action status = %d, want %d (closed stack pull request); body: %s", status, http.StatusConflict, payload)
	}
	var errResp map[string]any
	if err := json.Unmarshal(payload, &errResp); err != nil {
		t.Fatalf("decode rebase error body %s: %v", payload, err)
	}
	if result, _ := errResp["result"].(string); result == "created" {
		t.Fatalf("refused rebase response result = %q, want no created result", result)
	}
	errInfo, _ := errResp["error"].(map[string]any)
	if errInfo == nil {
		t.Fatalf("rebase error body carries no canonical error: %s", payload)
	}
	if code, _ := errInfo["code"].(string); code != "publish_stack_pull_request_closed" {
		t.Fatalf("rebase refusal code = %q, want publish_stack_pull_request_closed", code)
	}
	repoCtx := closedRefusalRepositoryContext(t, errInfo)
	if name, _ := repoCtx["name"].(string); name != "repo-a" {
		t.Fatalf("refusal repository = %q, want repo-a", name)
	}
	if pos, _ := repoCtx["layer_position"].(float64); int(pos) != 2 {
		t.Fatalf("refusal layer_position = %v, want 2", repoCtx["layer_position"])
	}
	if title, _ := repoCtx["layer_title"].(string); title != "Layer two" {
		t.Fatalf("refusal layer_title = %q, want Layer two", title)
	}
	if url, _ := repoCtx["pull_request_url"].(string); url != fx.prURL2 {
		t.Fatalf("refusal pull_request_url = %q, want %s", url, fx.prURL2)
	}

	// No child feature was created and the parent has no active child.
	features, err := fx.store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, f := range features {
		if f.IsChild() && f.Parent != nil && f.Parent.ParentID == fx.parentID {
			t.Fatalf("unexpected child %s created for the closed-pull-request parent", f.ID)
		}
	}
	if detail := closedRefusalFeatureDetail(t, fx.srv.URL, fx.parentID); detail["active_child"] != nil {
		t.Fatalf("parent detail active_child = %+v, want none after the refused launch", detail["active_child"])
	}

	// The repository is parked: the stored closed record surfaces on repo-a
	// with the same layer context, and the catalog enables reopen-pull-request.
	parked := closedRefusalRepoError(t, fx.srv.URL, fx.parentID, "repo-a")
	if parked == nil {
		t.Fatal("repo-a stores no closed record after the refused rebase launch")
	}
	if code, _ := parked["code"].(string); code != "publish_stack_pull_request_closed" {
		t.Fatalf("parked repo-a record code = %q, want publish_stack_pull_request_closed", code)
	}
	parkedCtx := closedRefusalRepositoryContext(t, parked)
	if pos, _ := parkedCtx["layer_position"].(float64); int(pos) != 2 {
		t.Fatalf("parked record layer_position = %v, want 2", parkedCtx["layer_position"])
	}
	if url, _ := parkedCtx["pull_request_url"].(string); url != fx.prURL2 {
		t.Fatalf("parked record pull_request_url = %q, want %s", url, fx.prURL2)
	}
	if !closedRefusalActionEnabled(t, fx.srv.URL, fx.parentID, "reopen-pull-request") {
		t.Fatal("action catalog does not enable reopen-pull-request for the parked repository")
	}

	// Resolve through Reopen over REST with the current source revision.
	sourceRevision := closedRefusalSourceRevision(t, fx.srv.URL, fx.parentID)
	body := fmt.Sprintf(`{"source_revision":%q,"repository":"repo-a","layer":2}`, sourceRevision)
	reopenStatus, reopenPayload := postActionStatus(t, fx.srv.URL, fx.parentID, "reopen-pull-request", body)
	if reopenStatus != http.StatusOK {
		t.Fatalf("reopen-pull-request status = %d, want %d; body: %s", reopenStatus, http.StatusOK, reopenPayload)
	}
	var reopenResp map[string]any
	if err := json.Unmarshal(reopenPayload, &reopenResp); err != nil {
		t.Fatalf("decode reopen response %s: %v", reopenPayload, err)
	}
	if result, _ := reopenResp["result"].(string); result != "reopened" {
		t.Fatalf("reopen result = %q, want reopened", result)
	}

	// The closed record is cleared from the repository and the pull request
	// reads open on the fake; the parent records the layer open with the same
	// pull request, and the catalog no longer enables reopen.
	if cleared := closedRefusalRepoError(t, fx.srv.URL, fx.parentID, "repo-a"); cleared != nil {
		t.Fatalf("repo-a still stores a record after Reopen: %+v", cleared)
	}
	pr, ok := fx.pulls.Pull("repo-a", fx.prNum2)
	if !ok || pr.State != "open" {
		t.Fatalf("layer 2 pull request after Reopen = %+v, want open", pr)
	}
	parent := fx.reloadParent()
	entry := parent.Stack[1].Repos["repo-a"]
	if entry.PRState != feature.StackPRStateOpen || entry.PRURL != fx.prURL2 {
		t.Fatalf("parent layer 2 entry after Reopen = %+v, want open at %s", entry, fx.prURL2)
	}
	if closedRefusalActionEnabled(t, fx.srv.URL, fx.parentID, "reopen-pull-request") {
		t.Fatal("action catalog still enables reopen-pull-request after the resolution")
	}
}
