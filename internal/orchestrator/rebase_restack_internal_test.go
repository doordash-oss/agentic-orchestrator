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

// Real-git coverage of the harness restack loop a rebase child runs at
// Created: the loop replays the kept stack layers onto the persisted target,
// drops merged layers' segments, resets the child worktree to the rebuilt
// top, persists the result on the relationship, and routes the child into
// the deferred Final Review. A replay conflict parks the pass with the
// rebase replay conflict attention code and leaves every ref untouched.

package orchestrator

import (
	"context"
	"fmt"
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
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// rebaseRestackFixture is a real-git three-layer stack whose layer 1 pull
// request merged (squash-merged into main plus one upstream commit) and a
// rebase child at Created pinned at the parent tip.
type rebaseRestackFixture struct {
	t *testing.T

	repoDir    string // parent worktree of repoA, checked out on the top layer branch
	childWT    string // repoA's child worktree, on the child branch at the parent tip
	childBr    string
	layerTips  []string // original layer tips, ascending
	Subjects   []string // replayed commits' subjects, oldest first (phases 3..5)
	Authors    []string // replayed commits' "name <email>" authors, oldest first
	targetSHA  string   // main tip: layer 1 squashed plus one upstream commit
	forkSHA    string   // merge base of the target and the lowest layer tip
	parentTip  string   // layer 3's tip, the child worktree's fork point
	anchors    map[int]string
	store      *feature.Store
	mgr        *feature.Manager
	wm         *git.WorktreeManager
	parentID   string
	childID    string
	dispatched chan *feature.Feature // captured Final Review dispatches

	passRepoDir string // optional pass-through repository
	passChildWT string
	passForkSHA string
	passChildBr string
}

// rebaseRestackFixtureOpts configures the restack fixture.
type rebaseRestackFixtureOpts struct {
	// Conflicting makes the target's squash commit also touch the file
	// layer 2's first commit introduces, so the replay genuinely conflicts.
	Conflicting bool
	// WithPassThrough adds a second, up-to-date repository to the parent;
	// it never enters the work list.
	WithPassThrough bool
	// TopMerged additionally marks layer 3's pull request merged, so the
	// restack drops both end layers and the new top is layer 2.
	TopMerged bool
}

// newRebaseRestackFixture builds the three-layer stack fixture.
func newRebaseRestackFixture(t *testing.T, opts rebaseRestackFixtureOpts) *rebaseRestackFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("real-git rebase restack test")
	}
	conflicting, withPassThrough, topMerged := opts.Conflicting, opts.WithPassThrough, opts.TopMerged
	fx := &rebaseRestackFixture{
		t:          t,
		parentID:   "restack-parent",
		childID:    "restack-child",
		dispatched: make(chan *feature.Feature, 4),
	}

	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	wm := git.NewWorktreeManager(t.TempDir())
	mgr := feature.NewManager(store, config.NewDefault())
	mgr.Worktrees = wm
	fx.store = store
	fx.mgr = mgr
	fx.wm = wm

	repoDir := testutil.InitGitRepo(t)
	fx.repoDir = repoDir

	// The stack chain: fork → layer 1 (phases 1-2) → layer 2 (phases 3-4) →
	// layer 3 (phase 5). Every commit carries a distinct author so the
	// replay's author preservation is observable.
	fx.forkSHA = restackGit(t, repoDir, "rev-parse", "HEAD")
	restackGit(t, repoDir, "checkout", "-b", "stack/1")
	fx.commitLayerCommit("l1.txt", "layer1 v1\n", "layer1 phase1", "One Author <one@example.com>")
	fx.commitLayerCommit("l1.txt", "layer1 v2\n", "layer1 phase2", "Two Author <two@example.com>")
	fx.layerTips = append(fx.layerTips, restackGit(t, repoDir, "rev-parse", "HEAD"))

	restackGit(t, repoDir, "checkout", "-b", "stack/2")
	fx.commitLayerCommit("l2.txt", "layer2 v1\n", "layer2 phase3", "Three Author <three@example.com>")
	fx.commitLayerCommit("l2.txt", "layer2 v2\n", "layer2 phase4", "Four Author <four@example.com>")
	fx.layerTips = append(fx.layerTips, restackGit(t, repoDir, "rev-parse", "HEAD"))

	restackGit(t, repoDir, "checkout", "-b", "stack/3")
	fx.commitLayerCommit("l3.txt", "layer3 v1\n", "layer3 phase5", "Five Author <five@example.com>")
	fx.layerTips = append(fx.layerTips, restackGit(t, repoDir, "rev-parse", "HEAD"))
	fx.parentTip = fx.layerTips[2]

	// Replay expectations: layers 2 and 3's commits, oldest first.
	fx.Subjects = []string{"layer2 phase3", "layer2 phase4", "layer3 phase5"}
	fx.Authors = []string{
		"Three Author <three@example.com>",
		"Four Author <four@example.com>",
		"Five Author <five@example.com>",
	}

	// The target: main squash-merges layer 1's content (and, in the
	// conflicting variant, also touches layer 2's file) and gains one
	// upstream commit.
	restackCheckout(t, repoDir, "main")
	restackGit(t, repoDir, "checkout", "stack/1", "--", "l1.txt")
	if conflicting {
		restackWrite(t, repoDir, "l2.txt", "upstream conflicting content\n")
		restackGit(t, repoDir, "add", "l2.txt")
	}
	restackCommitAs(t, repoDir, "squash merge layer 1", "Main Merger", "main@example.com")
	testutil.CommitFile(t, repoDir, "upstream.txt", "upstream change\n", "upstream advancement")
	fx.targetSHA = restackGit(t, repoDir, "rev-parse", "HEAD")
	restackCheckout(t, repoDir, "stack/3")

	// The anchors: every phase's commit sits on the chain in order.
	commits := restackGitLines(t, repoDir, "rev-list", "--reverse", fx.forkSHA+".."+fx.parentTip)
	if len(commits) != 5 {
		t.Fatalf("chain has %d commits above the fork, want 5", len(commits))
	}
	fx.anchors = map[int]string{}
	for phase, sha := range commits {
		fx.anchors[phase+1] = sha
	}

	// The child worktree on the child branch at the parent tip.
	fx.childBr = "feature/restack-child/rebase-feature-branches"
	fx.childWT = filepath.Join(t.TempDir(), "child-wt")
	restackGit(t, repoDir, "worktree", "add", "-b", fx.childBr, fx.childWT, fx.parentTip)

	parentRepos := []feature.FeatureRepo{{
		Name: "repoA", Path: repoDir, WorktreePath: repoDir,
		Branch: "stack/3", BaseBranch: "main",
	}}
	childRepos := []feature.FeatureRepo{{
		Name: "repoA", Path: repoDir, WorktreePath: fx.childWT,
		Branch: fx.childBr, BaseBranch: "main",
	}}
	if withPassThrough {
		passDir := testutil.InitGitRepo(t)
		restackGit(t, passDir, "checkout", "-b", "feature/pass")
		testutil.CommitFile(t, passDir, "pass.txt", "pass content\n", "pass-through commit")
		fx.passRepoDir = passDir
		fx.passForkSHA = restackGit(t, passDir, "rev-parse", "HEAD")
		fx.passChildBr = "feature/restack-child-pass/rebase-feature-branches"
		fx.passChildWT = filepath.Join(t.TempDir(), "pass-child-wt")
		restackGit(t, passDir, "worktree", "add", "-b", fx.passChildBr, fx.passChildWT, fx.passForkSHA)
		parentRepos = append(parentRepos, feature.FeatureRepo{
			Name: "repoB", Path: passDir, WorktreePath: passDir,
			Branch: "feature/pass", BaseBranch: "main",
		})
		childRepos = append(childRepos, feature.FeatureRepo{
			Name: "repoB", Path: passDir, WorktreePath: fx.passChildWT,
			Branch: fx.passChildBr, BaseBranch: "main",
		})
	}

	publishable := true
	parent := &feature.Feature{
		ID: fx.parentID, Name: "Restack parent", Slug: fx.parentID,
		Status: feature.StatusPublished, CurrentPhase: feature.PhasePublish,
		Created: time.Now(), ActiveRun: 1, RunCount: 1,
		Repos:         parentRepos,
		RepoStates:    map[string]*feature.RepoState{"repoA": {}, "repoB": {}},
		Checkpoints:   feature.Checkpoints{ManualPublish: true},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	parent.Stack = []feature.StackLayer{
		{
			Position: 1, Title: "Layer one", Phases: []int{1, 2}, Branch: "stack/1",
			Repos: map[string]feature.StackRepoEntry{"repoA": {
				TipSHA: fx.layerTips[0], PRURL: "https://fake.example/pr/1",
				PRState: feature.StackPRStateMerged,
			}},
		},
		{
			Position: 2, Title: "Layer two", Phases: []int{3, 4}, Branch: "stack/2",
			Repos: map[string]feature.StackRepoEntry{"repoA": {TipSHA: fx.layerTips[1]}},
		},
		{
			Position: 3, Title: "Layer three", Phases: []int{5}, Branch: "stack/3",
			Repos: map[string]feature.StackRepoEntry{"repoA": {TipSHA: fx.layerTips[2]}},
		},
	}
	if topMerged {
		parent.Stack[2].Repos["repoA"] = feature.StackRepoEntry{
			TipSHA: fx.layerTips[2], PRURL: "https://fake.example/pr/3",
			PRState: feature.StackPRStateMerged,
		}
	}
	run := &feature.Run{
		RunNumber: 1,
		Stack:     feature.CopyStackLayers(parent.Stack),
		RoadmapPhaseCommitAnchors: map[int]map[string]string{
			1: {"repoA": fx.anchors[1]},
			2: {"repoA": fx.anchors[2]},
			3: {"repoA": fx.anchors[3]},
			4: {"repoA": fx.anchors[4]},
			5: {"repoA": fx.anchors[5]},
		},
	}
	parent.SetRun(run)
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}

	layerThreeState := feature.RebaseLayerStateKept
	if topMerged {
		layerThreeState = feature.RebaseLayerStateMerged
	}
	child := &feature.Feature{
		ID: fx.childID, Name: "Rebase feature branches", Slug: fx.childID,
		Status:       feature.StatusCreated,
		CurrentPhase: feature.PipelineMedium.FirstPhase(),
		Pipeline:     feature.PipelineMedium,
		Created:      time.Now(), ActiveRun: 1, RunCount: 1,
		Repos:         childRepos,
		RepoStates:    map[string]*feature.RepoState{"repoA": {}, "repoB": {}},
		SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID, Kind: feature.ChildKindRebase,
			Bases: []feature.ChildRepoBase{{
				Repo: "repoA", SHA: fx.parentTip, ParentBranch: "stack/3",
			}},
			RebaseTargets: []feature.RebaseRepoTarget{{
				Repo: "repoA", Target: "main", Ref: "origin/main",
				Publishable: publishable, TargetSHA: fx.targetSHA,
			}},
			RebaseLayerStates: []feature.RebaseLayerClassification{
				{Repo: "repoA", LayerPosition: 1, LayerTitle: "Layer one", Branch: "stack/1", State: feature.RebaseLayerStateMerged},
				{Repo: "repoA", LayerPosition: 2, LayerTitle: "Layer two", Branch: "stack/2", State: feature.RebaseLayerStateKept},
				{Repo: "repoA", LayerPosition: 3, LayerTitle: "Layer three", Branch: "stack/3", State: layerThreeState},
			},
			RebaseWorkRepos: []string{"repoA"},
		},
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}
	return fx
}

// commitLayerCommit creates one stack commit with an explicit author identity.
func (fx *rebaseRestackFixture) commitLayerCommit(file, content, subject, author string) {
	fx.t.Helper()
	restackWrite(fx.t, fx.repoDir, file, content)
	restackGit(fx.t, fx.repoDir, "add", file)
	name, email := splitAuthor(author)
	restackCommitAs(fx.t, fx.repoDir, subject, name, email)
}

// orchestrator builds the orchestrator with a capturing Final Review stub.
func (fx *rebaseRestackFixture) orchestrator() *Orchestrator {
	fx.t.Helper()
	o := New(Deps{Lifecycle: fx.mgr, Store: fx.store, Worktrees: fx.wm}, Hooks{})
	o.SetRunMultiRepoFinalReviewFn(func(f *feature.Feature, kbInfos ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		fx.dispatched <- f
		ch := make(chan *agent.OrchestratorResult, 1)
		return ch, nil
	})
	fx.t.Cleanup(func() {
		_ = o.Shutdown()
	})
	return o
}

func (fx *rebaseRestackFixture) reloadChild() *feature.Feature {
	fx.t.Helper()
	child, err := fx.store.Load(fx.childID)
	if err != nil {
		fx.t.Fatalf("reload child: %v", err)
	}
	return child
}

// reload returns the parent and the child, for tests that drive integration.
func (fx *rebaseRestackFixture) reload() (*feature.Feature, *feature.Feature) {
	fx.t.Helper()
	parent, err := fx.store.Load(fx.parentID)
	if err != nil {
		fx.t.Fatalf("reload parent: %v", err)
	}
	return parent, fx.reloadChild()
}

func (fx *rebaseRestackFixture) refSHA(ref string) string {
	fx.t.Helper()
	return restackGit(fx.t, fx.repoDir, "rev-parse", ref)
}

// waitForRestackLoop polls the child until the asynchronous restack loop
// reaches the expected end state (landed, parked, or still Created after an
// abort). The loop runs in a background goroutine; the Final Review
// goroutine it dispatches on landing blocks on its result channel in these
// tests, so WaitForCycles cannot be used — the observable store state is
// the loop's own completion signal.
func (fx *rebaseRestackFixture) waitForRestackLoop(t *testing.T, done func(*feature.Feature) bool, what string) *feature.Feature {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		child := fx.reloadChild()
		if done(child) {
			return child
		}
		if time.Now().After(deadline) {
			t.Fatalf("restack loop never %s; child status = %s, transaction = %+v", what, child.Status, child.Parent.Transaction)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitLanded waits for the loop to land the child at FinalReviewing with
// the Final Review dispatched.
func (fx *rebaseRestackFixture) waitLanded(t *testing.T) *feature.Feature {
	t.Helper()
	return fx.waitForRestackLoop(t, func(child *feature.Feature) bool {
		return child.Status == feature.StatusFinalReviewing
	}, "landed")
}

// waitParked waits for the loop to park the child with an attention journal.
func (fx *rebaseRestackFixture) waitParked(t *testing.T) *feature.Feature {
	t.Helper()
	return fx.waitForRestackLoop(t, func(child *feature.Feature) bool {
		return child.Parent.Transaction != nil && child.Parent.Transaction.Phase == feature.TransactionPhaseAttention
	}, "parked")
}

// TestRebaseRestack_ReplaysStackOntoTarget drives the restack loop through
// StartFeature and verifies the full acceptance shape: the child worktree
// descends from the target with layers 2 and 3 replayed in order (identical
// subjects and authors, no layer 1 commits), the relationship records the
// rebuilt tips, the dropped marker, and the anchor remap, the parent's refs
// are unchanged, and the child lands at an approved-shaped status with the
// Final Review dispatched and only the work repository touched.
func TestRebaseRestack_ReplaysStackOntoTarget(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{WithPassThrough: true})
	o := fx.orchestrator()

	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("StartFeature() error = %v", err)
	}

	// The asynchronous loop landed the child and dispatched the Final
	// Review: the child is at FinalReviewing with its current phase Final
	// Review.
	fx.waitLanded(t)
	select {
	case f := <-fx.dispatched:
		if f.ID != fx.childID {
			t.Fatalf("final review dispatched for %s, want the rebase child", f.ID)
		}
		if f.CurrentPhase != feature.PhaseFinalReview {
			t.Fatalf("dispatched child phase = %v, want FinalReview", f.CurrentPhase)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("deferred Final Review was never dispatched")
	}

	child := fx.reloadChild()
	if child.Status != feature.StatusFinalReviewing {
		t.Fatalf("child status = %s, want FinalReviewing (dispatched final review)", child.Status)
	}
	if st := child.RepoStates["repoA"]; st == nil || !st.Touched {
		t.Fatalf("repoA touched state = %+v, want touched", child.RepoStates["repoA"])
	}
	if st := child.RepoStates["repoB"]; st == nil || st.Touched {
		t.Fatalf("repoB touched state = %+v, want untouched pass-through", child.RepoStates["repoB"])
	}

	// The child worktree HEAD descends from the target and carries exactly
	// layers 2 and 3's commits, in order, with their original subjects and
	// authors.
	if !git.IsAncestor(fx.childWT, fx.targetSHA, "HEAD") {
		t.Fatal("child worktree HEAD does not descend from the target commit")
	}
	subjects := restackGitLines(t, fx.childWT, "log", "--format=%s", fx.targetSHA+"..HEAD")
	reverseStrings(subjects)
	if strings.Join(subjects, "|") != strings.Join(fx.Subjects, "|") {
		t.Fatalf("replayed subjects = %v, want %v (no layer 1 commits, layers 2 and 3 in order)", subjects, fx.Subjects)
	}
	authors := restackGitLines(t, fx.childWT, "log", "--format=%an <%ae>", fx.targetSHA+"..HEAD")
	reverseStrings(authors)
	if strings.Join(authors, "|") != strings.Join(fx.Authors, "|") {
		t.Fatalf("replayed authors = %v, want %v", authors, fx.Authors)
	}
	childHead := restackGit(t, fx.childWT, "rev-parse", "HEAD")

	// The relationship records the rebuilt tips, the dropped marker, the
	// anchor remap, and the rebuilt top.
	if len(child.Parent.RebaseRestacks) != 1 {
		t.Fatalf("restacks = %+v, want one entry for repoA", child.Parent.RebaseRestacks)
	}
	rs := child.Parent.RebaseRestacks[0]
	if rs.Repo != "repoA" || rs.RebuiltTop != childHead {
		t.Fatalf("restack = %+v, want repoA with rebuilt top %s", rs, childHead)
	}
	if len(rs.DroppedLayers) != 1 || rs.DroppedLayers[0] != 1 {
		t.Fatalf("dropped layers = %v, want [1]", rs.DroppedLayers)
	}
	if len(rs.RebuiltTips) != 2 {
		t.Fatalf("rebuilt tips = %v, want layers 2 and 3", rs.RebuiltTips)
	}
	if rs.RebuiltTips[3] != childHead {
		t.Fatalf("layer 3 rebuilt tip = %s, want the rebuilt top %s", rs.RebuiltTips[3], childHead)
	}
	layer2Subject := restackGit(t, fx.childWT, "log", "-1", "--format=%s", rs.RebuiltTips[2])
	if layer2Subject != "layer2 phase4" {
		t.Fatalf("layer 2 rebuilt tip subject = %q, want layer2 phase4", layer2Subject)
	}
	// Layer 1's phase anchors remap to the target SHA; layer 3's anchor
	// remaps to the rebuilt top.
	if got := rs.AnchorRemap[1]; got != fx.targetSHA {
		t.Fatalf("anchor 1 remap = %s, want the target SHA %s", got, fx.targetSHA)
	}
	if got := rs.AnchorRemap[2]; got != fx.targetSHA {
		t.Fatalf("anchor 2 remap = %s, want the target SHA %s", got, fx.targetSHA)
	}
	if got := rs.AnchorRemap[5]; got != childHead {
		t.Fatalf("anchor 5 remap = %s, want the rebuilt top %s", got, childHead)
	}
	for _, phase := range []int{3, 4} {
		if rs.AnchorRemap[phase] == "" {
			t.Fatalf("anchor %d remap is empty", phase)
		}
	}

	// The parent's refs are byte-identical: every layer ref and the target
	// branch still sit where the fixture left them.
	for i, want := range fx.layerTips {
		ref := fmt.Sprintf("stack/%d", i+1)
		if got := fx.refSHA(ref); got != want {
			t.Fatalf("parent ref %s moved: %s, want %s", ref, got, want)
		}
	}
	if got := fx.refSHA("main"); got != fx.targetSHA {
		t.Fatalf("parent ref main moved: %s, want %s", got, fx.targetSHA)
	}

	// The pass-through repository is untouched: its child worktree stays
	// byte-identical to the fork point.
	if fx.passChildWT != "" {
		if got := restackGit(t, fx.passChildWT, "rev-parse", "HEAD"); got != fx.passForkSHA {
			t.Fatalf("pass-through child worktree HEAD = %s, want fork point %s", got, fx.passForkSHA)
		}
		if status := restackGit(t, fx.passChildWT, "status", "--porcelain"); status != "" {
			t.Fatalf("pass-through child worktree dirty: %q", status)
		}
	}
}

// TestRebaseRestack_ConflictParksAttention verifies a conflicting replay
// whose scripted resolution sessions never resolve: three attempts run
// (each attempt directory holds its prompt, output, and feedback, the
// second and third prompts carrying the previous failure), the pass parks
// with the rebase conflict resolution attention code carrying the attempt
// count, the repository, the segment, the commit, and the conflicted files,
// every ref stays unchanged with the child worktree at the parent tip, and
// starting the pass again re-runs the loop.
func TestRebaseRestack_ConflictParksAttention(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{Conflicting: true})
	o := fx.orchestrator()

	var sessionWorkDirs []string
	o.SetRunConflictResolutionFn(func(ctx context.Context, req agent.ConflictResolutionRequest) (*agent.ConflictResolutionResult, error) {
		sessionWorkDirs = append(sessionWorkDirs, req.WorkDir)
		// The session completes but never touches the conflicted file, so
		// the markers stay and every attempt fails verification.
		return &agent.ConflictResolutionResult{Status: agent.ConflictResolutionCompleted}, nil
	})

	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("StartFeature() error = %v, want nil (exhaustion parks the pass)", err)
	}

	child := fx.waitParked(t)
	if child.Status != feature.StatusCreated {
		t.Fatalf("child status = %s, want Created (the park performs no status change)", child.Status)
	}
	select {
	case <-fx.dispatched:
		t.Fatal("final review dispatched for a parked pass")
	default:
	}
	journal := child.Parent.Transaction
	if journal == nil || journal.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction = %+v, want attention phase", journal)
	}
	if journal.Attention == nil || journal.Attention.Code != errcat.IntegrationRebaseConflict {
		t.Fatalf("attention record = %+v, want integration_rebase_conflict", journal.Attention)
	}
	rec := journal.Attention
	if rec.Context == nil || len(rec.Context.Repositories) != 1 || rec.Context.Repositories[0].Name != "repoA" {
		t.Fatalf("attention repositories = %+v, want repoA", rec.Context)
	}
	repoRec := rec.Context.Repositories[0]
	files := repoRec.ConflictFiles
	if len(files) == 0 || files[0] != "l2.txt" {
		t.Fatalf("conflict files = %v, want l2.txt", files)
	}
	if repoRec.Attempts != rebaseConflictResolutionAttempts {
		t.Fatalf("attention attempts = %d, want %d", repoRec.Attempts, rebaseConflictResolutionAttempts)
	}
	if repoRec.CommitSHA != fx.anchors[3] {
		t.Fatalf("attention commit = %s, want %s", repoRec.CommitSHA, fx.anchors[3])
	}
	if !strings.Contains(rec.Diagnostics, "repoA") || !strings.Contains(rec.Diagnostics, "phase:2..phase:3") {
		t.Fatalf("diagnostics = %q, want the repository and the conflicting segment", rec.Diagnostics)
	}
	if !strings.Contains(rec.Diagnostics, fx.anchors[3]) {
		t.Fatalf("diagnostics = %q, want the conflicting commit %s", rec.Diagnostics, fx.anchors[3])
	}
	if !strings.Contains(rec.Diagnostics, "exhausted 3 attempts") {
		t.Fatalf("diagnostics = %q, want the exhausted attempt count", rec.Diagnostics)
	}
	if !strings.Contains(rec.Diagnostics, "conflict markers remain in: l2.txt") {
		t.Fatalf("diagnostics = %q, want the last failure reason", rec.Diagnostics)
	}
	if !strings.Contains(rec.Diagnostics, "attempt-03") {
		t.Fatalf("diagnostics = %q, want the last attempt directory", rec.Diagnostics)
	}

	// Three sessions ran, each inside the paused pick's temporary worktree.
	if len(sessionWorkDirs) != rebaseConflictResolutionAttempts {
		t.Fatalf("resolution sessions = %d, want %d", len(sessionWorkDirs), rebaseConflictResolutionAttempts)
	}

	// Every attempt directory holds its prompt and feedback; the second and
	// third prompts carry the previous attempt's failure. (The session
	// output and receipt are written by the real phase-runner entry point;
	// this test scripts the runner seam, so only the harness-authored files
	// are on disk.)
	commitRoot := filepath.Join(fx.store.BaseDir, fx.childID, "runs", "run-001", "rebase-resolution", "repoA", fx.anchors[3][:7])
	for attempt := 1; attempt <= rebaseConflictResolutionAttempts; attempt++ {
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

	// Every ref and the child worktree are untouched, and no temporary
	// worktree remains.
	if got := restackGit(t, fx.childWT, "rev-parse", "HEAD"); got != fx.parentTip {
		t.Fatalf("child worktree HEAD = %s, want the parent tip %s", got, fx.parentTip)
	}
	for i, want := range fx.layerTips {
		ref := fmt.Sprintf("stack/%d", i+1)
		if got := fx.refSHA(ref); got != want {
			t.Fatalf("parent ref %s moved: %s, want %s", ref, got, want)
		}
	}
	assertNoRestackTempWorktrees(t, fx.repoDir)

	// Starting the pass again re-runs the loop: the parked journal is
	// cleared and recreated by the second exhausted run.
	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("second StartFeature() error = %v", err)
	}
	child = fx.waitParked(t)
	if child.Status != feature.StatusCreated {
		t.Fatalf("child status after re-run = %s, want Created", child.Status)
	}
	if child.Parent.Transaction == nil || child.Parent.Transaction.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction after re-run = %+v, want a recreated attention journal", child.Parent.Transaction)
	}
	if child.Parent.Transaction.Attention == nil || child.Parent.Transaction.Attention.Code != errcat.IntegrationRebaseConflict {
		t.Fatalf("re-run attention = %+v, want integration_rebase_conflict", child.Parent.Transaction.Attention)
	}
}

// assertNoRestackTempWorktrees proves no detached temporary worktree from a
// restack remains registered in the repository.
func assertNoRestackTempWorktrees(t *testing.T, repoDir string) {
	t.Helper()
	if restackTempWorktreeListed(t, repoDir) {
		t.Fatal("a temporary restack worktree remains registered")
	}
}

// restackTempWorktreeListed reports whether a temporary restack worktree
// (created under an OS-temp "restack-*" directory) is still registered. The
// check matches path components, not raw output — the fixture's child branch
// name itself contains "restack-".
func restackTempWorktreeListed(t *testing.T, repoDir string) bool {
	t.Helper()
	out := restackGit(t, repoDir, "worktree", "list", "--porcelain")
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

// TestRebaseRestack_IdempotentRecompute simulates a crash after the child
// worktree reset but before the result is persisted: the child is returned
// to Created with no recorded restack, and a second start recomputes the
// same rebuilt chain (identical subjects, authors, and tree content).
func TestRebaseRestack_IdempotentRecompute(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{})
	o := fx.orchestrator()

	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("first StartFeature() error = %v", err)
	}
	fx.waitLanded(t)
	first := fx.reloadChild()
	firstRestack := first.Parent.RebaseRestacks[0]
	firstHeadTree := restackGit(t, fx.childWT, "rev-parse", "HEAD^{tree}")

	// Simulate the crash: the worktree reset landed but nothing persisted.
	if err := fx.store.Modify(fx.childID, func(f *feature.Feature) error {
		f.Status = feature.StatusCreated
		f.CurrentPhase = feature.PipelineMedium.FirstPhase()
		f.Parent.RebaseRestacks = nil
		f.Parent.Transaction = nil
		if st := f.RepoStates["repoA"]; st != nil {
			st.Touched = false
		}
		return nil
	}); err != nil {
		t.Fatalf("simulating the crash: %v", err)
	}

	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("second StartFeature() error = %v", err)
	}
	fx.waitLanded(t)
	second := fx.reloadChild()
	if second.Status != feature.StatusFinalReviewing {
		t.Fatalf("child status after recompute = %s, want FinalReviewing", second.Status)
	}
	secondRestack := second.Parent.RebaseRestacks[0]

	// The recomputed chain is content-identical: same subjects and authors
	// in order and a byte-identical top tree. (Cherry-pick committer
	// timestamps differ between runs, so the SHAs themselves are not
	// comparable; the replayed content is.)
	if firstRestack.RebuiltTips[3] == "" || secondRestack.RebuiltTips[3] == "" {
		t.Fatal("rebuilt top tips are empty")
	}
	firstSubjects := restackGitLines(t, fx.childWT, "log", "--format=%s", fx.targetSHA+"..HEAD")
	reverseStrings(firstSubjects)
	// The first run's chain is gone (refs never moved), so compare the
	// recomputed chain against the invariant expectations instead.
	if strings.Join(firstSubjects, "|") != strings.Join(fx.Subjects, "|") {
		t.Fatalf("recomputed subjects = %v, want %v", firstSubjects, fx.Subjects)
	}
	if got := restackGit(t, fx.childWT, "rev-parse", "HEAD^{tree}"); got != firstHeadTree {
		t.Fatalf("recomputed top tree = %s, want the first run's tree %s", got, firstHeadTree)
	}
	if secondRestack.Repo != "repoA" || len(secondRestack.DroppedLayers) != 1 || secondRestack.DroppedLayers[0] != 1 {
		t.Fatalf("recomputed restack = %+v, want repoA with dropped layer 1", secondRestack)
	}
	if secondRestack.AnchorRemap[1] != fx.targetSHA || secondRestack.AnchorRemap[2] != fx.targetSHA {
		t.Fatalf("recomputed anchor remap = %v, want layer 1's anchors at the target", secondRestack.AnchorRemap)
	}
}

// restackGit runs one git command in dir and returns the trimmed stdout.
func restackGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := restackGitOutput(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return strings.TrimSpace(out)
}

func restackGitOutput(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = testutil.GitTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}

// restackGitLines runs one git command and splits its output into lines.
func restackGitLines(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	out := restackGit(t, dir, args...)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// restackCheckout switches the worktree's branch, tolerating an already
// correct branch.
func restackCheckout(t *testing.T, dir, branch string) {
	t.Helper()
	if _, err := restackGitOutput(t, dir, "checkout", branch); err != nil {
		t.Fatalf("checkout %s in %s: %v", branch, dir, err)
	}
}

// restackWrite writes one file into the worktree.
func restackWrite(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s in %s: %v", file, dir, err)
	}
}

// restackCommit commits staged changes with the default test identity.
func restackCommit(t *testing.T, dir, subject string) {
	t.Helper()
	restackCommitAs(t, dir, subject, "Test Committer", "test@example.com")
}

// restackCommitAs commits staged changes with an explicit identity.
func restackCommitAs(t *testing.T, dir, subject, name, email string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "commit", "-m", subject)
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit %q in %s: %s: %v", subject, dir, strings.TrimSpace(string(out)), err)
	}
}

// splitAuthor splits "Name <email>" into its parts.
func splitAuthor(author string) (string, string) {
	idx := strings.Index(author, " <")
	if idx < 0 {
		return author, author + "@example.com"
	}
	return author[:idx], author[idx+2 : len(author)-1]
}

// reverseStrings reverses a slice in place.
func reverseStrings(s []string) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// landRestackedChildAtReviewPassed runs the asynchronous harness restack,
// waits for it to land the child at FinalReviewing, and leaves the child at
// the approved-shaped state a finished verification round produces, ready
// for RunChildIntegration.
func landRestackedChildAtReviewPassed(t *testing.T, fx *rebaseRestackFixture, o *Orchestrator) {
	t.Helper()
	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("StartFeature() error = %v", err)
	}
	fx.waitLanded(t)
	if err := fx.store.Modify(fx.childID, func(f *feature.Feature) error {
		f.Status = feature.StatusReviewPassed
		return nil
	}); err != nil {
		t.Fatalf("mark the child review-passed: %v", err)
	}
}

// TestRebaseIntegration_LandsRewriteAndDeleteRefs verifies the Task 5
// acceptance shape over the Task 4 fixture: after integration, layer 1's ref
// is gone and its entry is merged with an empty tip, layers 2 and 3's refs
// point at the rebuilt tips with linear history from the target, the parent
// worktree HEAD equals the new top, anchors and tips are remapped, and the
// journal lists one delete and two rewrite refs with anchors equal to the
// pre-integration tips.
func TestRebaseIntegration_LandsRewriteAndDeleteRefs(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{})
	o := fx.orchestrator()
	landRestackedChildAtReviewPassed(t, fx, o)
	childHead := restackGit(t, fx.childWT, "rev-parse", "HEAD")

	if err := o.RunChildIntegration(fx.childID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v", err)
	}

	parent, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	journal := child.Parent.Transaction
	if journal == nil || journal.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("transaction phase = %+v, want merged", journal)
	}
	entry := journal.EntryByRepo("repoA")
	if entry == nil {
		t.Fatalf("repoA entry missing: %+v", journal)
	}
	if len(entry.Refs) != 3 {
		t.Fatalf("repoA refs = %+v, want one delete and two rewrites", entry.Refs)
	}
	if r := entry.Refs[0]; r.Branch != "stack/1" || r.RefKind() != feature.RepoRefKindDelete || r.AnchorSHA != fx.layerTips[0] {
		t.Errorf("layer 1 ref = %+v, want delete at the pre-integration tip %s", r, fx.layerTips[0])
	}
	if r := entry.Refs[1]; r.Branch != "stack/2" || r.RefKind() != feature.RepoRefKindRewrite || r.AnchorSHA != fx.layerTips[1] || r.CandidateSHA == "" {
		t.Errorf("layer 2 ref = %+v, want rewrite from the pre-integration tip %s", r, fx.layerTips[1])
	}
	if r := entry.Refs[2]; r.Branch != "stack/3" || r.RefKind() != feature.RepoRefKindRewrite || r.AnchorSHA != fx.layerTips[2] || r.CandidateSHA != childHead {
		t.Errorf("layer 3 ref = %+v, want rewrite from %s to the child head", r, fx.layerTips[2])
	}
	if entry.PreviousTop != nil {
		t.Errorf("previous top recorded = %+v, want none (the top layer is kept)", entry.PreviousTop)
	}

	// Layer 1's ref is gone; layers 2 and 3 point at the rebuilt tips with
	// linear history from the target.
	if _, err := restackGitOutput(t, fx.repoDir, "rev-parse", "--verify", "refs/heads/stack/1"); err == nil {
		t.Fatal("layer 1 ref still exists after integration")
	}
	layer2Tip := restackGit(t, fx.repoDir, "rev-parse", "stack/2")
	layer3Tip := restackGit(t, fx.repoDir, "rev-parse", "stack/3")
	if layer2Tip != entry.Refs[1].CandidateSHA || layer3Tip != childHead {
		t.Fatalf("layer refs = (%s, %s), want the candidates (%s, %s)", layer2Tip, layer3Tip, entry.Refs[1].CandidateSHA, childHead)
	}
	if !git.IsAncestor(fx.repoDir, fx.targetSHA, layer2Tip) || !git.IsAncestor(fx.repoDir, layer2Tip, layer3Tip) {
		t.Fatal("rebuilt refs do not form a linear chain from the target")
	}
	if got := restackGit(t, fx.repoDir, "log", "--format=%s", fx.targetSHA+"..stack/3"); strings.ReplaceAll(got, "\n", "|") != "layer3 phase5|layer2 phase4|layer2 phase3" {
		t.Fatalf("new history = %q, want the replayed layers 2 and 3 only", got)
	}

	// The parent worktree HEAD equals the new top.
	if got := restackGit(t, fx.repoDir, "rev-parse", "HEAD"); got != childHead {
		t.Fatalf("parent worktree HEAD = %s, want the new top %s", got, childHead)
	}
	if branch := restackGit(t, fx.repoDir, "branch", "--show-current"); branch != "stack/3" {
		t.Fatalf("parent worktree branch = %q, want stack/3", branch)
	}

	// The stack records layer 1 merged with an empty tip and layers 2 and 3
	// at the remapped tips; the run's anchors are remapped too.
	for _, layer := range parent.Stack {
		e := layer.Repos["repoA"]
		switch layer.Position {
		case 1:
			if e.PRState != feature.StackPRStateMerged || e.TipSHA != "" || e.LastPushedSHA != "" || e.PRURL == "" {
				t.Errorf("layer 1 entry = %+v, want merged with cleared tip and pushed SHA and kept URL", e)
			}
		case 2:
			if e.TipSHA != layer2Tip {
				t.Errorf("layer 2 tip = %s, want the remapped tip %s", e.TipSHA, layer2Tip)
			}
		case 3:
			if e.TipSHA != childHead {
				t.Errorf("layer 3 tip = %s, want the remapped tip %s", e.TipSHA, childHead)
			}
		}
	}
	anchors := parent.Run().RoadmapPhaseCommitAnchors
	if anchors[1]["repoA"] != fx.targetSHA || anchors[2]["repoA"] != fx.targetSHA {
		t.Errorf("layer 1 anchors = %v, want the target SHA", anchors)
	}
	if anchors[5]["repoA"] != childHead {
		t.Errorf("anchor 5 = %s, want the new top %s", anchors[5]["repoA"], childHead)
	}
}

// TestRebaseIntegration_TopMergedSwitchesToNewTop verifies the top-merged
// shape: the journal carries a delete ref for the top, a previous-top record,
// and rewrite refs below; after apply the parent worktree is on the highest
// kept layer's branch at its candidate and the repository record names it.
func TestRebaseIntegration_TopMergedSwitchesToNewTop(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{TopMerged: true})
	o := fx.orchestrator()
	landRestackedChildAtReviewPassed(t, fx, o)
	childHead := restackGit(t, fx.childWT, "rev-parse", "HEAD")

	if err := o.RunChildIntegration(fx.childID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v", err)
	}

	parent, child := fx.reload()
	if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close outcome = %q, want completed", child.Parent.CloseOutcome)
	}
	entry := child.Parent.Transaction.EntryByRepo("repoA")
	if entry == nil || len(entry.Refs) != 3 {
		t.Fatalf("repoA refs = %+v, want delete(1), rewrite(2), delete(3)", entry)
	}
	if entry.Refs[0].RefKind() != feature.RepoRefKindDelete || entry.Refs[2].RefKind() != feature.RepoRefKindDelete || entry.Refs[2].Branch != "stack/3" {
		t.Fatalf("refs = %+v, want delete refs for layers 1 and 3", entry.Refs)
	}
	if entry.PreviousTop == nil || entry.PreviousTop.Branch != "stack/3" || entry.PreviousTop.TipSHA != fx.layerTips[2] {
		t.Fatalf("previous top = %+v, want the dropped stack/3 at its pre-integration tip", entry.PreviousTop)
	}
	if top := entry.TopRef(); top == nil || top.Branch != "stack/2" || top.CandidateSHA != childHead {
		t.Fatalf("top ref = %+v, want the kept stack/2 at the child head", top)
	}

	// The parent worktree switched to the new top branch at the candidate
	// and the repository record follows.
	if branch := restackGit(t, fx.repoDir, "branch", "--show-current"); branch != "stack/2" {
		t.Fatalf("parent worktree branch = %q, want the new top stack/2", branch)
	}
	if got := restackGit(t, fx.repoDir, "rev-parse", "HEAD"); got != childHead {
		t.Fatalf("parent worktree HEAD = %s, want the new top candidate %s", got, childHead)
	}
	if repo := featureRepoByName(parent, "repoA"); repo == nil || repo.Branch != "stack/2" {
		t.Fatalf("parent repo record branch = %+v, want stack/2", repo)
	}
	if _, err := restackGitOutput(t, fx.repoDir, "rev-parse", "--verify", "refs/heads/stack/3"); err == nil {
		t.Fatal("the dropped top ref still exists after integration")
	}
	for _, layer := range parent.Stack {
		if layer.Position == 3 {
			if e := layer.Repos["repoA"]; e.PRState != feature.StackPRStateMerged || e.TipSHA != "" {
				t.Errorf("layer 3 entry = %+v, want merged with cleared tip", e)
			}
		}
	}
}

// TestRebaseIntegration_ChildHeadOffChainParksCandidateFailed verifies a
// child head that does not descend from the rebuilt top parks the
// candidate-failed attention before any parent ref changes.
func TestRebaseIntegration_ChildHeadOffChainParksCandidateFailed(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{})
	o := fx.orchestrator()
	landRestackedChildAtReviewPassed(t, fx, o)
	before := fx.refSHA("stack/3")

	// Move the child branch onto a fresh commit on top of the target: the
	// head contains the target (so the mechanical gate's ancestor check
	// passes) but does not descend from the rebuilt top, whose replayed
	// commits carry different SHAs than the original chain the child
	// branch left behind.
	restackGit(t, fx.childWT, "reset", "--hard", fx.targetSHA)
	testutil.CommitFile(t, fx.childWT, "offchain.txt", "off-chain work\n", "off-chain commit on the target")

	if err := o.RunChildIntegration(fx.childID); err != nil {
		t.Fatalf("RunChildIntegration() error = %v, want nil (attention parks)", err)
	}
	child := fx.reloadChild()
	journal := child.Parent.Transaction
	if journal == nil || journal.Phase != feature.TransactionPhaseAttention {
		t.Fatalf("transaction = %+v, want attention", journal)
	}
	if journal.Attention == nil || journal.Attention.Code != errcat.IntegrationCandidateFailed {
		t.Fatalf("attention record = %+v, want integration_candidate_failed", journal.Attention)
	}
	if !strings.Contains(journal.Attention.Diagnostics, "does not descend from the rebuilt top") {
		t.Errorf("diagnostics = %q, want the off-chain child head detail", journal.Attention.Diagnostics)
	}
	if after := fx.refSHA("stack/3"); after != before {
		t.Fatalf("parent ref moved: before=%s after=%s", before, after)
	}
}
