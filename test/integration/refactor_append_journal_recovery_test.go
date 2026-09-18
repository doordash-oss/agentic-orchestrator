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

// Real-git coverage of the refactor append transaction's crash window: a
// one-layer refactor child of a published, auto-publish parent is driven
// through the real preparation and apply path, a crash is injected between
// the create-expecting-absence ref transaction and the journal's apply
// persistence, and a fresh orchestrator's startup recovery scan classifies
// the created ref at its candidate, finishes closure — appended layers
// persisted, worktree switched to the new top branch — and hands off to the
// auto-publish tail, whose stack walk creates the appended layer's pull
// request chained onto the parent layer's open pull request.

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestRefactorAppendJournalCrashRecovery drives a refactor child of a
// published one-layer parent through the real orchestrator integration path:
// preparation records one created ref for the appended layer at parent
// position 2 with the child's recorded tip as its candidate, the armed
// worktree wrapper performs the real create-expecting-absence transaction
// (previous-top verify plus the create) and then dies, and the startup
// recovery scan on a fresh orchestrator finishes the closure and the
// auto-publish tail.
func TestRefactorAppendJournalCrashRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real git repository, a bare origin, and the in-test GitHub fake")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	remotesDir := filepath.Join(tmp, "remotes")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	// One real repository whose origin is a bare remote whose path parses
	// as a git@localhost:acme scp-style remote: every push is a local file
	// transfer, and the GitHub REST client is routed at the in-test fake
	// serving the acme owner's pull store.
	repoDir := testutil.InitGitRepo(t)

	parent := &feature.Feature{
		ID:            "feat-append-crash-parent",
		Name:          "Append Crash Parent",
		Slug:          "append-crash-parent",
		Description:   "publishes a one-layer stack before a refactor child appends a layer",
		Status:        feature.StatusCodeReady,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now(),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	workspaceSlug := feature.WorkspaceSlug(parent.Slug, parent.ID)
	layer1Branch := git.LayerBranchName(workspaceSlug, 1, "parent-foundation")
	appendedBranch := git.LayerBranchName(workspaceSlug, 2, "child-refactor")

	runGit(t, repoDir, "checkout", "-b", layer1Branch)
	parentTip := testutil.CommitFile(t, repoDir, "foundation.txt", "parent layer 1\n", "parent foundation")
	bare := stackPublishBareOrigin(t, remotesDir, repoDir, "repo-a")

	fake := testutil.InstallFakeGitHubAPI(t)
	pulls := testutil.NewFakePullStore("acme")
	pulls.Install(t, fake, "repo-a")

	// The refactor child: a disposable worktree branched at the parent tip
	// with one commit — a one-layer stack whose recorded tip is the child
	// head, so append preparation maps it onto parent position 2.
	child := &feature.Feature{
		ID:            "feat-append-crash-child",
		Name:          "Append Crash Child",
		Slug:          "append-crash-child",
		Description:   "refactors the parent foundation",
		Status:        feature.StatusReviewPassed,
		CurrentPhase:  feature.PhaseFinalReview,
		Pipeline:      feature.PipelineMedium,
		Created:       time.Now(),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	childBranch := git.LayerBranchName(feature.WorkspaceSlug(child.Slug, child.ID), 1, "child-refactor")
	childWT := filepath.Join(tmp, "child-wt")
	runGit(t, repoDir, "worktree", "add", "-b", childBranch, childWT, parentTip)
	childTip := testutil.CommitFile(t, childWT, "refactor.txt", "child refactor work\n", "child refactor")

	publishable := true
	parent.Repos = []feature.FeatureRepo{{
		Name:         "repo-a",
		Path:         repoDir,
		WorktreePath: repoDir,
		Branch:       layer1Branch,
		BaseBranch:   "main",
		Publishable:  &publishable,
	}}
	parent.RepoStates = map[string]*feature.RepoState{"repo-a": {Touched: true}}
	parent.Stack = []feature.StackLayer{{
		Position: 1,
		Title:    "Parent foundation",
		Slug:     "parent-foundation",
		Phases:   []int{1},
		Branch:   layer1Branch,
		Repos:    map[string]feature.StackRepoEntry{"repo-a": {TipSHA: parentTip}},
	}}
	child.Repos = []feature.FeatureRepo{{
		Name:         "repo-a",
		Path:         repoDir,
		WorktreePath: childWT,
		Branch:       childBranch,
		BaseBranch:   "main",
	}}
	child.RepoStates = map[string]*feature.RepoState{"repo-a": {Touched: true}}
	child.Stack = []feature.StackLayer{{
		Position: 1,
		Title:    "Child refactor",
		Slug:     "child-refactor",
		Phases:   []int{1},
		Branch:   childBranch,
		Repos:    map[string]feature.StackRepoEntry{"repo-a": {TipSHA: childTip}},
	}}
	child.Parent = &feature.ChildRelationship{
		ParentID: parent.ID,
		Kind:     feature.ChildKindRefactor,
		Bases:    []feature.ChildRepoBase{{Repo: "repo-a", SHA: parentTip, ParentBranch: layer1Branch}},
	}

	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: repoDir}
	store := feature.NewStore(stateDir)
	wm := git.NewWorktreeManager(filepath.Join(tmp, "worktrees"))
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = wm

	// The parent is saved and published before the child exists, exactly
	// as a refactor pass on a delivered stack: the first publish creates
	// the parent layer's open pull request the appended layer's request
	// later chains onto. Checkpoints stay at auto-publish so the closure
	// tail republishes without a manual gate.
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	firstRunner := stackPublishDescriptionRunner(t, store, stateDir)
	firstPub := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Worktrees:   wm,
		PhaseRunner: firstRunner,
		CmdRunner:   firstRunner.CommandRunner,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = firstPub.Shutdown()
		firstPub.WaitForCycles()
	})
	if err := firstPub.PublishWithOptions(parent.ID, orchestrator.PublishOptions{}); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	pr1 := pulls.URL("repo-a", 1)
	published, err := store.Load(parent.ID)
	if err != nil {
		t.Fatalf("load parent after the first publish: %v", err)
	}
	if published.Status != feature.StatusPublished {
		t.Fatalf("parent status after the first publish = %s, want Published", published.Status)
	}
	if got := published.Stack[0].Repos["repo-a"].PRURL; got != pr1 {
		t.Fatalf("layer-1 pull request URL = %q, want %q", got, pr1)
	}
	if created := pulls.Created(); len(created) != 1 ||
		created[0].Title != "Parent foundation" || created[0].Head != layer1Branch || created[0].Base != "main" {
		t.Fatalf("pull requests after the first publish = %+v, want one Parent foundation on %s based on main",
			pulls.Created(), layer1Branch)
	}

	if err := store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}

	// Preparation and apply run through the real orchestrator entry point;
	// the wrapper fires only after the create-expecting-absence ref
	// transaction lands and before the journal's apply persistence.
	crashing := &crashOnRefsUpdate{WorktreeManager: wm}
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: mgr,
		Store:     store,
		Worktrees: crashing,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})

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
	// never-applied entry: the created ref exists but the apply
	// persistence did not.
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
	if len(entry.Refs) != 1 {
		t.Fatalf("entry refs after the crash = %+v, want one created ref for the appended layer", entry.Refs)
	}
	ref := &entry.Refs[0]
	if ref.RefKind() != feature.RepoRefKindCreate || ref.AnchorSHA != "" {
		t.Fatalf("entry ref after the crash = %+v, want a created ref with an empty anchor", ref)
	}
	if ref.Branch != appendedBranch || ref.Layer != 2 {
		t.Fatalf("entry ref after the crash = %+v, want the appended branch %s at position 2", ref, appendedBranch)
	}
	if ref.CandidateSHA != childTip || entry.ChildHeadSHA != childTip {
		t.Fatalf("appended candidate = %s with child head %s, want the child layer tip %s",
			ref.CandidateSHA, entry.ChildHeadSHA, childTip)
	}
	if ref.ObservedSHA != "" {
		t.Fatalf("ref %s observed SHA %q was persisted before the crash", ref.Branch, ref.ObservedSHA)
	}
	if prev := entry.PreviousTopRef(); prev == nil || prev.Branch != layer1Branch ||
		prev.Layer != 1 || prev.TipSHA != parentTip {
		t.Fatalf("previous top after the crash = %+v, want layer 1 on %s at %s",
			entry.PreviousTopRef(), layer1Branch, parentTip)
	}
	if len(tx.AppendedLayers) != 1 {
		t.Fatalf("appended layer definitions after the crash = %+v, want one", tx.AppendedLayers)
	}
	def := tx.AppendedLayers[0]
	if def.Position != 2 || def.Branch != appendedBranch || def.Title != "Child refactor" ||
		def.Origin == nil || def.Origin.SourceFeatureID != child.ID || def.Origin.SourceLayerPosition != 1 {
		t.Fatalf("appended layer definition after the crash = %+v, want position 2 on %s with the child origin",
			def, appendedBranch)
	}

	// The created ref sits at its candidate while the previous top is
	// untouched, the worktree still checks out the previous top branch,
	// and the parent record predates the closure writes.
	if got := refSHA(t, repoDir, appendedBranch); got != childTip {
		t.Fatalf("created ref %s after the crash = %s, want its candidate %s", appendedBranch, got, childTip)
	}
	if got := refSHA(t, repoDir, layer1Branch); got != parentTip {
		t.Fatalf("previous top %s after the crash = %s, want untouched %s", layer1Branch, got, parentTip)
	}
	if branch := runGit(t, repoDir, "branch", "--show-current"); branch != layer1Branch {
		t.Fatalf("parent worktree branch after the crash = %q, want the previous top %q", branch, layer1Branch)
	}
	parentRec, err := store.Load(parent.ID)
	if err != nil {
		t.Fatalf("load parent after the crash: %v", err)
	}
	if len(parentRec.Stack) != 1 || parentRec.Stack[0].Repos["repo-a"].TipSHA != parentTip {
		t.Fatalf("parent stack after the crash = %+v, want the untouched one-layer stack", parentRec.Stack)
	}
	if parentRec.Repos[0].Branch != layer1Branch {
		t.Fatalf("parent repo branch after the crash = %q, want %q", parentRec.Repos[0].Branch, layer1Branch)
	}
	assertNoOperationInProgress(t, repoDir)

	// A fresh orchestrator instance — healthy worktree ops, the
	// description runner for the tail's PR session, and the noop recovery
	// operator — runs the startup recovery scan over the same store,
	// manager, and worktrees.
	freshRunner := stackPublishDescriptionRunner(t, store, stateDir)
	fresh := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Worktrees:   wm,
		PhaseRunner: freshRunner,
		CmdRunner:   freshRunner.CommandRunner,
		Recovery:    &fakeRecoveryNoop{},
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = fresh.Shutdown()
		fresh.WaitForCycles()
	})
	if _, err := fresh.ScanRecovery(context.Background()); err != nil {
		t.Fatalf("ScanRecovery after the crash: %v", err)
	}

	// After the scan: the child is closed Completed with a merged journal,
	// and the parent ends Published with the appended layer persisted.
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
	if parentRec.Status != feature.StatusPublished {
		t.Fatalf("parent status after recovery = %s, want Published", parentRec.Status)
	}

	// The appended layer is persisted on the parent's stack — named with
	// the parent's workspace slug and the child's slug, origin pointing at
	// the child, per-repo tip equal to the created ref's candidate — and
	// the repository record follows the new top branch.
	if len(parentRec.Stack) != 2 {
		t.Fatalf("parent stack layers after recovery = %d, want 2", len(parentRec.Stack))
	}
	appended := parentRec.Stack[1]
	if appended.Position != 2 || appended.Branch != appendedBranch || appended.Slug != "child-refactor" {
		t.Fatalf("persisted appended layer = %+v, want position 2 on %s", appended, appendedBranch)
	}
	if appended.Origin == nil || appended.Origin.SourceFeatureID != child.ID || appended.Origin.SourceLayerPosition != 1 {
		t.Fatalf("persisted appended layer origin = %+v, want the child feature and layer 1", appended.Origin)
	}
	if got := appended.Repos["repo-a"].TipSHA; got != childTip {
		t.Fatalf("appended layer tip after recovery = %s, want the created ref's candidate %s", got, childTip)
	}
	if parentRec.Stack[0].Repos["repo-a"].TipSHA != parentTip {
		t.Fatalf("parent layer-1 tip after recovery = %s, want untouched %s",
			parentRec.Stack[0].Repos["repo-a"].TipSHA, parentTip)
	}
	if parentRec.Repos[0].Branch != appendedBranch {
		t.Fatalf("parent repo branch after recovery = %q, want the new top branch %q",
			parentRec.Repos[0].Branch, appendedBranch)
	}

	// The parent worktree is switched onto the new top branch at its
	// candidate and carries no leftover operation state.
	if branch := runGit(t, repoDir, "branch", "--show-current"); branch != appendedBranch {
		t.Fatalf("parent worktree branch after recovery = %q, want the new top branch %q", branch, appendedBranch)
	}
	if got := refSHA(t, repoDir, appendedBranch); got != childTip {
		t.Fatalf("created ref %s after recovery = %s, want its candidate %s", appendedBranch, got, childTip)
	}
	if status := runGit(t, repoDir, "status", "--porcelain"); status != "" {
		t.Fatalf("parent worktree dirty after recovery: %q", status)
	}
	assertNoOperationInProgress(t, repoDir)

	// The auto-publish tail created exactly one new pull request — the
	// appended layer's, based on the parent layer's open pull request's
	// branch — and pushed the appended branch to the remote at its tip.
	pr2 := pulls.URL("repo-a", 2)
	if got := appended.Repos["repo-a"].PRURL; got != pr2 {
		t.Fatalf("appended layer pull request URL = %q, want %q", got, pr2)
	}
	created := pulls.Created()
	if len(created) != 2 {
		t.Fatalf("created pull requests after recovery = %+v, want exactly two (the parent layer's and the appended layer's)", created)
	}
	appendedPR := created[1]
	if appendedPR.Repo != "repo-a" || appendedPR.Number != 2 || appendedPR.Title != "Child refactor" ||
		appendedPR.Head != appendedBranch || appendedPR.Base != layer1Branch {
		t.Fatalf("appended layer pull request = %+v, want Child refactor on %s based on %s",
			appendedPR, appendedBranch, layer1Branch)
	}
	if got := runGit(t, bare, "rev-parse", "refs/heads/"+appendedBranch); got != childTip {
		t.Fatalf("remote appended branch = %s, want the candidate %s", got, childTip)
	}
	if got := appended.Repos["repo-a"].LastPushedSHA; got != childTip {
		t.Fatalf("appended layer last-pushed SHA = %s, want the remote tip %s", got, childTip)
	}

	// The appended pull request's body carries the scripted description
	// session's text — the origin-aware context for the child's layer —
	// and a stack section listing both layers; the parent layer's open
	// pull request is refreshed with the same section.
	pr2Record, ok := pulls.Pull("repo-a", 2)
	if !ok {
		t.Fatal("appended layer pull request record missing")
	}
	if !strings.Contains(pr2Record.Body, "Scripted PR body for Child refactor.") {
		t.Fatalf("appended layer body is not the scripted session body:\n%s", pr2Record.Body)
	}
	stackLine1 := fmt.Sprintf("1. Parent foundation - [#1](%s) (open)", pr1)
	stackLine2 := fmt.Sprintf("2. Child refactor - [#2](%s) (open) (this pull request)", pr2)
	if !strings.Contains(pr2Record.Body, "## Stack") ||
		!strings.Contains(pr2Record.Body, stackLine1) || !strings.Contains(pr2Record.Body, stackLine2) {
		t.Fatalf("appended layer body lacks the stack section naming both layers:\n%s", pr2Record.Body)
	}
	pr1Record, _ := pulls.Pull("repo-a", 1)
	if !strings.Contains(pr1Record.Body, "## Stack") ||
		!strings.Contains(pr1Record.Body, stackLine1) || !strings.Contains(pr1Record.Body, stackLine2) {
		t.Fatalf("parent layer body lacks the refreshed stack section naming both layers:\n%s", pr1Record.Body)
	}

	// The disposable child worktree and its branch are cleaned up by the
	// closure tail.
	if _, err := os.Stat(childWT); !os.IsNotExist(err) {
		t.Fatalf("child worktree still present after recovery: %v", err)
	}
	if branches := runGit(t, repoDir, "branch", "--list", childBranch); branches != "" {
		t.Fatalf("child branch %s still present after recovery", childBranch)
	}
}
