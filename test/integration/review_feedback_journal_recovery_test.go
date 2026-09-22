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

// Real-git coverage of the review-feedback child transaction journal's crash
// window: a review-feedback child of a three-layer stacked parent is driven
// through the real preparation and apply path, a crash is injected between
// the multi-ref compare-and-swap transaction and the journal's apply
// persistence, and a fresh orchestrator's startup recovery scan classifies
// every ref at its candidate and finishes closure — remap, worktree sync,
// child close, and the parent's post-closure status.

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// crashOnRefsUpdate wraps the real worktree manager and panics after a
// successful multi-ref compare-and-swap transaction while armed, simulating a
// process crash between the landed ref transaction and the journal's apply
// persistence — the exact window the transaction journal's recovery
// classification exists to close. Mirrors crashLifecycle's crash model for
// the worktree seam.
type crashOnRefsUpdate struct {
	*git.WorktreeManager
	crash atomic.Bool
}

func (w *crashOnRefsUpdate) UpdateRefsTransaction(repoPath string, updates []git.RefUpdate) error {
	if err := w.WorktreeManager.UpdateRefsTransaction(repoPath, updates); err != nil {
		return err
	}
	if w.crash.Load() {
		panic("crash injected between the ref transaction and journal persistence")
	}
	return nil
}

// rfJournalLayerBranch names the parent's layer branch for one stack
// position.
func rfJournalLayerBranch(pos int) string {
	return fmt.Sprintf("stack/layer-%d", pos)
}

// TestReviewFeedbackJournalCrashRecovery drives a review-feedback child of a
// three-layer stacked parent through the real orchestrator integration path:
// preparation relocates the child's Stack-Layer-tagged commits into the
// parent chain and records one ref update per changed layer, the armed
// worktree wrapper performs the real multi-ref transaction and then dies, and
// the startup recovery scan on a fresh orchestrator finishes the closure.
// Both orchestrators run with a mock remote whose leased layer pushes always
// succeed, so the post-closure review-feedback tail republishes the stack and
// settles even though the repository has no origin.
func TestReviewFeedbackJournalCrashRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real git repository through the relocation ladder")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	// A three-layer stack over one real repository: one commit per layer,
	// each phase-completion anchor aliasing its layer's tip — the shape a
	// real roadmap-driven run records.
	repoDir := testutil.InitGitRepo(t)
	layerTips := make(map[int]string, 3)
	for pos := 1; pos <= 3; pos++ {
		runGit(t, repoDir, "checkout", "-b", rfJournalLayerBranch(pos))
		layerTips[pos] = testutil.CommitFile(t, repoDir,
			fmt.Sprintf("p%d.txt", pos),
			fmt.Sprintf("layer %d work", pos),
			fmt.Sprintf("Layer %d work", pos))
	}
	topBranch := rfJournalLayerBranch(3)
	baseSHA := layerTips[3]

	// The review-feedback child: a disposable worktree branched at the top
	// tip with one commit tagged for layer 2 and one for the top layer.
	childBranch := "feature/rf-child-fixes"
	childWT := filepath.Join(tmp, "child-wt")
	runGit(t, repoDir, "worktree", "add", "-b", childBranch, childWT, baseSHA)
	fixL2 := testutil.CommitFile(t, childWT, "fix-l2.txt", "layer 2 fix\n",
		"review fix for layer 2\n\nStack-Layer: 2")
	fixL3 := testutil.CommitFile(t, childWT, "fix-l3.txt", "layer 3 fix\n",
		"review fix for the top layer\n\nStack-Layer: 3")

	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: repoDir}
	store := feature.NewStore(stateDir)
	wm := git.NewWorktreeManager(filepath.Join(tmp, "worktrees"))
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = wm

	publishable := true
	parent := &feature.Feature{
		ID:            "rf-journal-parent",
		Name:          "RF Journal Parent",
		Slug:          "rf-journal-parent",
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now(),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{{
			Name:         "repo-a",
			Path:         repoDir,
			WorktreePath: repoDir,
			Branch:       topBranch,
			BaseBranch:   "main",
			Publishable:  &publishable,
		}},
		RepoStates:  map[string]*feature.RepoState{"repo-a": {Touched: true}},
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	}
	parent.Stack = make([]feature.StackLayer, 0, 3)
	for pos := 1; pos <= 3; pos++ {
		parent.Stack = append(parent.Stack, feature.StackLayer{
			Position: pos,
			Title:    fmt.Sprintf("Layer %d", pos),
			Slug:     fmt.Sprintf("layer-%d", pos),
			Phases:   []int{pos},
			Branch:   rfJournalLayerBranch(pos),
			Repos: map[string]feature.StackRepoEntry{"repo-a": {
				TipSHA:  layerTips[pos],
				PRURL:   fmt.Sprintf("https://github.com/example/repo-a/pull/%d", pos),
				PRState: feature.StackPRStateOpen,
			}},
		})
	}
	// Phase-completion anchors: each phase's anchor is its layer's tip.
	parent.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
		1: {"repo-a": layerTips[1]},
		2: {"repo-a": layerTips[2]},
		3: {"repo-a": layerTips[3]},
	}
	child := &feature.Feature{
		ID:            "rf-journal-child",
		Name:          "RF Journal Child",
		Slug:          "rf-journal-child",
		Status:        feature.StatusReviewPassed,
		CurrentPhase:  feature.PhaseFinalReview,
		Pipeline:      feature.PipelineMedium,
		Created:       time.Now(),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{{
			Name:         "repo-a",
			Path:         repoDir,
			WorktreePath: childWT,
			Branch:       childBranch,
			BaseBranch:   topBranch,
		}},
		RepoStates: map[string]*feature.RepoState{"repo-a": {Touched: true}},
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID,
			Kind:     feature.ChildKindReviewFeedback,
			Bases:    []feature.ChildRepoBase{{Repo: "repo-a", SHA: baseSHA, ParentBranch: topBranch}},
		},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}

	// The repository has no origin, so the republish walk must run against a
	// mock remote: every leased layer push reports the local SHA as
	// delivered, and pull-request state stays at the mock's indeterminate
	// default (treated as open). The shared fake GitHub API keeps the walk's
	// best-effort pull request body refreshes off the network.
	remote := mocks.NewMockRemoteOps()
	remote.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	testutil.InstallFakeGitHubAPI(t)

	crashing := &crashOnRefsUpdate{WorktreeManager: wm}
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: mgr,
		Store:     store,
		Worktrees: crashing,
		Remote:    remote,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})

	// Preparation and apply run through the real orchestrator entry point;
	// the wrapper fires only after the ref transaction lands and before the
	// journal's apply persistence.
	crashing.crash.Store(true)
	crashed := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				crashed = true
			}
		}()
		if err := orch.RunChildIntegration(child.ID); err != nil {
			t.Fatalf("RunChildIntegration before the crash: %v", err)
		}
	}()
	if !crashed {
		t.Fatal("the injected crash did not fire between the ref transaction and journal persistence")
	}
	crashing.crash.Store(false)

	// The durable journal is still in the applying phase with a prepared,
	// never-applied entry: the refs moved but the apply persistence did not.
	childRec, err := store.Load(child.ID)
	if err != nil {
		t.Fatalf("load child after the crash: %v", err)
	}
	tx := childRec.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseApplying {
		t.Fatalf("journal phase after the crash = %+v, want applying (crash before apply persistence)", tx)
	}
	if len(tx.Entries) != 1 || tx.Entries[0].Repo != "repo-a" {
		t.Fatalf("journal entries after the crash = %+v, want one repo-a entry", tx.Entries)
	}
	entry := &tx.Entries[0]
	if entry.PrepState != feature.RepoPrepPrepared || entry.ApplyState != "" {
		t.Fatalf("entry states after the crash = %q/%q, want prepared with no apply state",
			entry.PrepState, entry.ApplyState)
	}
	if entry.PendingSync {
		t.Fatalf("entry carries pending sync before any worktree sync ran: %+v", entry)
	}
	if len(entry.Refs) != 2 || entry.Refs[0].Layer != 2 || entry.Refs[1].Layer != 3 {
		t.Fatalf("entry refs after the crash = %+v, want one ref per changed layer (positions 2 and 3)", entry.Refs)
	}
	relocatedL2, okL2 := entry.Relocated[fixL2]
	relocatedL3, okL3 := entry.Relocated[fixL3]
	if !okL2 || !okL3 {
		t.Fatalf("relocated map after the crash = %v, want both child commits mapped", entry.Relocated)
	}
	// Every listed ref sits at its candidate in the real repository, with
	// no observed SHA persisted yet — the pre-apply durable state.
	for i := range entry.Refs {
		ref := &entry.Refs[i]
		if got := refSHA(t, repoDir, ref.Branch); got != ref.CandidateSHA {
			t.Fatalf("ref %s = %s after the crash, want its candidate %s", ref.Branch, got, ref.CandidateSHA)
		}
		if ref.ObservedSHA != "" {
			t.Fatalf("ref %s observed SHA %q was persisted before the crash", ref.Branch, ref.ObservedSHA)
		}
		if ref.AnchorSHA != layerTips[ref.Layer] {
			t.Fatalf("ref %s anchor = %s, want the pre-integration tip %s",
				ref.Branch, ref.AnchorSHA, layerTips[ref.Layer])
		}
	}
	if entry.Refs[0].CandidateSHA != relocatedL2 {
		t.Fatalf("layer-2 candidate = %s, want the relocated layer-2 fix %s",
			entry.Refs[0].CandidateSHA, relocatedL2)
	}
	newTop := entry.Refs[1].CandidateSHA
	if newTop != relocatedL3 {
		t.Fatalf("layer-3 candidate = %s, want the relocated top fix %s", newTop, relocatedL3)
	}
	// The parent worktree's checked-out top branch moved underneath it.
	headSHA, err := git.CurrentHeadSHA(repoDir)
	if err != nil {
		t.Fatalf("parent head after the crash: %v", err)
	}
	if headSHA != newTop {
		t.Fatalf("parent HEAD after the crash = %s, want the moved top ref %s", headSHA, newTop)
	}

	// The remap was not persisted: the recorded tips and the phase-3 anchor
	// still predate the relocation.
	parentRec, err := store.Load(parent.ID)
	if err != nil {
		t.Fatalf("load parent after the crash: %v", err)
	}
	if got := parentRec.Stack[1].Repos["repo-a"].TipSHA; got != layerTips[2] {
		t.Fatalf("recorded layer-2 tip after the crash = %s, want the pre-crash tip %s (remap not persisted)",
			got, layerTips[2])
	}
	if got := parentRec.Run().RoadmapPhaseCommitAnchors[3]["repo-a"]; got != layerTips[3] {
		t.Fatalf("phase-3 anchor after the crash = %s, want the pre-crash anchor %s (remap not persisted)",
			got, layerTips[3])
	}

	// No cherry-pick or rebase is left in progress in either worktree: the
	// relocation ladder replays inside a detached temporary worktree that
	// the primitive removes on every exit path.
	assertNoOperationInProgress(t, repoDir)
	assertNoOperationInProgress(t, childWT)
	if git.RebaseInProgress(repoDir) || git.RebaseInProgress(childWT) {
		t.Fatal("a rebase is in progress after the crash")
	}

	// A fresh orchestrator instance runs the startup recovery scan over the
	// same store, manager, worktrees, and mock remote.
	fresh := orchestrator.New(orchestrator.Deps{
		Lifecycle: mgr,
		Store:     store,
		Worktrees: wm,
		Remote:    remote,
		Recovery:  &fakeRecoveryNoop{},
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = fresh.Shutdown()
		fresh.WaitForCycles()
	})
	if _, err := fresh.ScanRecovery(context.Background()); err != nil {
		t.Fatalf("ScanRecovery after the crash: %v", err)
	}

	// After the scan: the child is closed Completed, the journal ends
	// merged with the tail settled, and the parent ends in the
	// review-feedback tail's post-closure status (Published).
	childRec, err = store.Load(child.ID)
	if err != nil {
		t.Fatalf("load child after recovery: %v", err)
	}
	parentRec, err = store.Load(parent.ID)
	if err != nil {
		t.Fatalf("load parent after recovery: %v", err)
	}
	if childRec.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted || childRec.Parent.ClosedAt == nil {
		t.Fatalf("child closure after recovery = %q/%v, want Completed with a timestamp",
			childRec.Parent.CloseOutcome, childRec.Parent.ClosedAt)
	}
	tx = childRec.Parent.Transaction
	if tx == nil || tx.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("journal phase after recovery = %+v, want merged", tx)
	}
	if tx.Attention != nil {
		t.Fatalf("journal attention after recovery = %+v, want none", tx.Attention)
	}
	if !tx.TailSettled {
		t.Fatal("tail-settled marker missing after recovery")
	}
	if parentRec.Status != feature.StatusPublished {
		t.Fatalf("parent status after recovery = %s, want Published", parentRec.Status)
	}

	// The remap landed: the layer tips and the phase-3 anchor point at the
	// relocated live commits, and the layer below the lowest change is
	// untouched.
	if got := refSHA(t, repoDir, rfJournalLayerBranch(2)); got != relocatedL2 {
		t.Fatalf("layer-2 ref after recovery = %s, want the relocated fix %s", got, relocatedL2)
	}
	if got := runGit(t, repoDir, "show", rfJournalLayerBranch(2)+":fix-l2.txt"); got != "layer 2 fix" {
		t.Fatalf("layer-2 branch content after recovery = %q, want the relocated fix", got)
	}
	if got := refSHA(t, repoDir, rfJournalLayerBranch(3)); got != newTop {
		t.Fatalf("layer-3 ref after recovery = %s, want the new top %s", got, newTop)
	}
	if got := runGit(t, repoDir, "show", newTop+":fix-l3.txt"); got != "layer 3 fix" {
		t.Fatalf("new top content after recovery = %q, want the relocated top fix", got)
	}
	if got := parentRec.Stack[0].Repos["repo-a"].TipSHA; got != layerTips[1] {
		t.Fatalf("recorded layer-1 tip after recovery = %s, want the untouched tip %s", got, layerTips[1])
	}
	if got := parentRec.Stack[1].Repos["repo-a"].TipSHA; got != relocatedL2 {
		t.Fatalf("recorded layer-2 tip after recovery = %s, want its remapped ref %s", got, relocatedL2)
	}
	if got := parentRec.Stack[2].Repos["repo-a"].TipSHA; got != newTop {
		t.Fatalf("recorded layer-3 tip after recovery = %s, want the new top %s", got, newTop)
	}
	anchor := parentRec.Run().RoadmapPhaseCommitAnchors[3]["repo-a"]
	if anchor == "" || anchor == layerTips[3] {
		t.Fatalf("phase-3 anchor after recovery = %q, want a remapped live commit", anchor)
	}
	runGit(t, repoDir, "merge-base", "--is-ancestor", relocatedL2, anchor)
	runGit(t, repoDir, "merge-base", "--is-ancestor", anchor, newTop)

	// The parent worktree is synced to the new top and carries no leftover
	// operation state.
	headSHA, err = git.CurrentHeadSHA(repoDir)
	if err != nil {
		t.Fatalf("parent head after recovery: %v", err)
	}
	if headSHA != newTop {
		t.Fatalf("parent HEAD after recovery = %s, want the new top %s", headSHA, newTop)
	}
	if status := runGit(t, repoDir, "status", "--porcelain"); status != "" {
		t.Fatalf("parent worktree dirty after recovery: %q", status)
	}
	assertNoOperationInProgress(t, repoDir)
	if git.RebaseInProgress(repoDir) {
		t.Fatal("a rebase is in progress in the parent worktree after recovery")
	}

	// The disposable child worktree and its branch are cleaned up by the
	// closure tail, so no worktree of the repository holds cherry-pick
	// state.
	if _, err := os.Stat(childWT); !os.IsNotExist(err) {
		t.Fatalf("child worktree still present after recovery: %v", err)
	}
	if branches := runGit(t, repoDir, "branch", "--list", childBranch); branches != "" {
		t.Fatalf("child branch %s still present after recovery", childBranch)
	}

	// The settled tail republished through the mock remote: every layer
	// whose tip differs from its recorded pushed SHA — each of the three
	// layers here, none ever pushed — was delivered by a leased layer push.
	pushedLayers := make(map[string]bool)
	for _, call := range remote.Calls {
		if call.Method != "PushLayerBranch" {
			continue
		}
		if branch, ok := call.Args[1].(string); ok {
			pushedLayers[branch] = true
		}
	}
	for pos := 1; pos <= 3; pos++ {
		if !pushedLayers[rfJournalLayerBranch(pos)] {
			t.Fatalf("layer %d branch %s was not delivered by the recovery tail's republish", pos, rfJournalLayerBranch(pos))
		}
	}
}
