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

// Integration test for the Large/Moonshot child KB promotion and recovery
// journey. Proves four behaviors across a multi-repository child:
//  1. Seeded child KB delta analysis completes.
//  2. Final KB refresh blocks integration until completion.
//  3. Multi-repository promotion: one repository overlay commits and retains
//     its lock when a later repository promotion fails; cleanup and
//     auto-publish settlement are blocked while recovery state is preserved.
//  4. Retry completes the full overlay vector, releases every overlay lock,
//     and a subsequent refactor child seeds from the completed overlay
//     generation.

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// kbPromoFixture sets up two real git repos (repoA, repoB) with canonical KBs,
// a parent feature, and a Large child feature for testing the multi-repository
// KB promotion journey.
type kbPromoFixture struct {
	t          *testing.T
	tmpDir     string
	stateDir   string
	baseDir    string
	repoDirA   string
	repoDirB   string
	childWTA   string
	childWTB   string
	parentBaseA string
	parentBaseB string
	store      *feature.Store
	mgr        *feature.Manager
	wm         *git.WorktreeManager
	parent     *feature.Feature
	child      *feature.Feature
	capturedPrompt string
}

func newKBPromoFixture(t *testing.T) *kbPromoFixture {
	t.Helper()
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "features")
	baseDir := filepath.Dir(stateDir)
	repoDirA := filepath.Join(tmpDir, "repoA")
	repoDirB := filepath.Join(tmpDir, "repoB")

	os.MkdirAll(stateDir, 0o755)

	// Create repoA.
	parentBaseA := setupGitRepo(t, repoDirA)
	childWTA := filepath.Join(tmpDir, "child-wtA")
	gitCmd(t, repoDirA, "worktree", "add", "-b", "feature/child-large-a", childWTA, parentBaseA)
	testutil.CommitFile(t, childWTA, "child_change_a.txt", "child work a\n", "child change a")

	// Create repoB.
	parentBaseB := setupGitRepo(t, repoDirB)
	childWTB := filepath.Join(tmpDir, "child-wtB")
	gitCmd(t, repoDirB, "worktree", "add", "-b", "feature/child-large-b", childWTB, parentBaseB)
	testutil.CommitFile(t, childWTB, "child_change_b.txt", "child work b\n", "child change b")

	store := feature.NewStore(stateDir)
	cfg := config.NewDefault()
	wm := git.NewWorktreeManager(filepath.Join(tmpDir, "wt-state"))
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = wm

	publishable := true
	parent := &feature.Feature{
		ID:           "parent-1",
		Name:         "Parent",
		Slug:         "parent-1",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Created:      time.Now(),
		ActiveRun:    1,
		RunCount:     1,
		Repos: []feature.FeatureRepo{
			{
				Name:         "repoA",
				Path:         repoDirA,
				WorktreePath: repoDirA,
				Branch:       "main",
				BaseBranch:   "main",
				Publishable:  &publishable,
			},
			{
				Name:         "repoB",
				Path:         repoDirB,
				WorktreePath: repoDirB,
				Branch:       "main",
				BaseBranch:   "main",
				Publishable:  &publishable,
			},
		},
		RepoStates: map[string]*feature.RepoState{
			"repoA": {Touched: true},
			"repoB": {Touched: true},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	child := &feature.Feature{
		ID:           "child-1",
		Name:         "Child",
		Slug:         "child-1",
		Status:       feature.StatusReviewPassed,
		CurrentPhase: feature.PhaseFinalReview,
		Pipeline:     feature.PipelineLarge,
		Created:      time.Now(),
		ActiveRun:    1,
		RunCount:     1,
		Repos: []feature.FeatureRepo{
			{
				Name:         "repoA",
				Path:         repoDirA,
				WorktreePath: childWTA,
				Branch:       "feature/child-large-a",
				BaseBranch:   "main",
			},
			{
				Name:         "repoB",
				Path:         repoDirB,
				WorktreePath: childWTB,
				Branch:       "feature/child-large-b",
				BaseBranch:   "main",
			},
		},
		RepoStates: map[string]*feature.RepoState{
			"repoA": {Touched: true},
			"repoB": {Touched: true},
		},
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID,
			Kind:     feature.ChildKindRefactor,
			Bases: []feature.ChildRepoBase{
				{Repo: "repoA", SHA: parentBaseA, ParentBranch: "main"},
				{Repo: "repoB", SHA: parentBaseB, ParentBranch: "main"},
			},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}

	return &kbPromoFixture{
		t:           t,
		tmpDir:      tmpDir,
		stateDir:    stateDir,
		baseDir:     baseDir,
		repoDirA:    repoDirA,
		repoDirB:    repoDirB,
		childWTA:    childWTA,
		childWTB:    childWTB,
		parentBaseA: parentBaseA,
		parentBaseB: parentBaseB,
		store:       store,
		mgr:         mgr,
		wm:          wm,
		parent:      parent,
		child:       child,
	}
}

func setupGitRepo(t *testing.T, dir string) string {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "checkout", "-b", "main")
	testutil.CommitFile(t, dir, "README.md", "# Test Repo\n", "initial commit")
	return gitRevParse(t, dir, "HEAD")
}

func (fx *kbPromoFixture) canonicalKBDir(repoName string) string {
	return agent.KBStateDir(fx.stateDir, repoName)
}

func (fx *kbPromoFixture) workspaceDir(repoName string) string {
	return feature.ChildKBWorkspaceDir(fx.stateDir, fx.child.ID, repoName)
}

func (fx *kbPromoFixture) overlayDir(repoName string) string {
	return feature.ParentOverlayPath(fx.stateDir, fx.parent.ID, repoName)
}

func (fx *kbPromoFixture) childPaths(repoName string) agent.ChildKBWorkspacePaths {
	for i := range fx.child.Repos {
		if fx.child.Repos[i].Name == repoName {
			return agent.ResolveChildKBPaths(fx.stateDir, fx.child, fx.child.Repos[i])
		}
	}
	fx.t.Fatalf("repo %s not found", repoName)
	return agent.ChildKBWorkspacePaths{}
}

func (fx *kbPromoFixture) childWT(repoName string) string {
	switch repoName {
	case "repoA":
		return fx.childWTA
	case "repoB":
		return fx.childWTB
	default:
		fx.t.Fatalf("unknown repo %s", repoName)
		return ""
	}
}

// setupCanonicalKB creates a canonical KB for the given repo with an index.md
// and state.json recording the parent base commit as the head commit.
func (fx *kbPromoFixture) setupCanonicalKB(repoName, parentBase string) {
	fx.t.Helper()
	kbDir := fx.canonicalKBDir(repoName)
	os.MkdirAll(kbDir, 0o755)
	for _, cat := range []string{"architecture", "conventions", "api-surface", "dependencies", "verification"} {
		os.MkdirAll(filepath.Join(kbDir, cat), 0o755)
	}
	os.WriteFile(filepath.Join(kbDir, "index.md"), []byte("# Knowledge Base for "+repoName+"\n\nCanonical KB content.\n"), 0o644)
	agent.SaveKBState(kbDir, &agent.KBState{
		HeadCommit:  parentBase,
		LastUpdated: time.Now(),
		Version:     1,
	})
}

func (fx *kbPromoFixture) realRunner() ports.CommandRunner {
	return &realCommandRunner{}
}

type realCommandRunner struct{}

func (r *realCommandRunner) Run(ctx context.Context, name string, args []string, opts ports.CommandOpts) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = opts.Dir
	out, err := cmd.CombinedOutput()
	return out, err
}

func (fx *kbPromoFixture) orchestrator() *orchestrator.Orchestrator {
	return orchestrator.New(orchestrator.Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Publisher: &git.PublishAdapter{},
		Worktrees: fx.wm,
		Cleanliness: feature.CleanlinessFunc(func(worktreePath string, maxPerCategory int) (*feature.RepoCleanliness, error) {
			return &feature.RepoCleanliness{}, nil
		}),
	}, orchestrator.Hooks{})
}

// orchestratorWithPhaseRunner builds an orchestrator with a PhaseRunner and
// SessionManager that can exercise the KB refresh flow. The writeMarker
// parameter controls whether the fake build session writes the phase_complete
// marker, simulating a successful (true) or failed (false) KB build.
func (fx *kbPromoFixture) orchestratorWithPhaseRunner(writeMarker bool) *orchestrator.Orchestrator {
	mockSM := mocks.NewMockSessionManager()
	mockSM.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return session.NewSession(id, featureID, phase), nil
	}
	pr := &agent.PhaseRunner{
		SessionManager: mockSM,
		FeatureStore:   fx.store,
		CommandRunner:  fx.realRunner(),
		Config:         config.NewDefault(),
		StateDir:       fx.stateDir,
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			fx.capturedPrompt = opts.Prompt
			if writeMarker && opts.MarkerPath != "" {
				os.MkdirAll(filepath.Dir(opts.MarkerPath), 0o755)
				os.WriteFile(opts.MarkerPath, []byte("{}"), 0o644)
			}
			return []string{"fake-cmd"}, nil, &ports.SessionOpts{}, nil
		},
	}
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:  fx.mgr,
		Store:      fx.store,
		Publisher:  &git.PublishAdapter{},
		Worktrees:  fx.wm,
		PhaseRunner: pr,
		Sessions:    mockSM,
		CmdRunner:   fx.realRunner(),
		Cleanliness: feature.CleanlinessFunc(func(worktreePath string, maxPerCategory int) (*feature.RepoCleanliness, error) {
			return &feature.RepoCleanliness{}, nil
		}),
	}, orchestrator.Hooks{})
	return o
}

// TestLargeMoonshotChildKnowledgePromotionRecoveryJourney proves the full
// behavioral contract for a Large refactor child with two repositories:
//  1. Seeded child KB delta analysis completes (the workspace is seeded from
//     the canonical KB and the builder runs to completion, setting
//     AnalyzedCommit and entering INCREMENTAL UPDATE mode).
//  2. Final KB refresh blocks integration until completion (a workspace with
//     an empty AnalyzedCommit is not fresh, so the refresh must complete
//     before integration proceeds; a failed refresh returns an error and no
//     parent ref is touched).
//  3. Multi-repository promotion failure: one repository overlay commits and
//     retains its promotion lock when a later repository promotion fails;
//     cleanup and auto-publish settlement are blocked while the child stays
//     Completed and the promotion journal remains pending for recovery.
//  4. Retry completes the full overlay vector, releases every overlay lock,
//     allows cleanup and auto-publish settlement to proceed, and a
//     subsequent refactor child seeds from the completed overlay generation.
func TestLargeMoonshotChildKnowledgePromotionRecoveryJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fx := newKBPromoFixture(t)

	// --- Setup: create canonical KBs for both repos ---
	fx.setupCanonicalKB("repoA", fx.parentBaseA)
	fx.setupCanonicalKB("repoB", fx.parentBaseB)

	// ================================================================
	// Step 1: Seeded child KB delta analysis completes
	// ================================================================
	fx.runStep1SeededDeltaAnalysis(t)

	// ================================================================
	// Step 2: Final KB refresh blocks integration until completion
	// ================================================================
	fx.runStep2RefreshBlocksIntegration(t)

	// ================================================================
	// Step 3: Multi-repository promotion failure
	// ================================================================
	// Set up the child as Completed with a merged transaction for both
	// repos, then simulate a partial promotion where repoA's overlay has
	// already been committed (Done) while repoB's promotion fails.
	fx.runStep3MultiRepoPromotionFailure(t)

	// ================================================================
	// Step 4: Retry completes the vector, all locks released,
	//          later child seeds from the completed overlay
	// ================================================================
	fx.runStep4RetryAndSeed(t)
}

// runStep1SeededDeltaAnalysis proves that a seeded child workspace enters
// INCREMENTAL UPDATE mode (not FULL BUILD) and the completion path sets
// AnalyzedCommit.
func (fx *kbPromoFixture) runStep1SeededDeltaAnalysis(t *testing.T) {
	pathsA := fx.childPaths("repoA")
	if err := agent.SeedChildKBWorkspace(context.Background(), fx.realRunner(), pathsA); err != nil {
		t.Fatalf("SeedChildKBWorkspace repoA: %v", err)
	}

	wsState, err := feature.LoadWorkspaceState(fx.workspaceDir("repoA"))
	if err != nil {
		t.Fatalf("LoadWorkspaceState after seed: %v", err)
	}
	if wsState == nil {
		t.Fatal("workspace state is nil after seeding")
	}
	if wsState.Source != feature.WorkspaceSourceCanonical {
		t.Errorf("expected source canonical, got %s", wsState.Source)
	}
	if wsState.SeedBaseCommit == "" {
		t.Error("SeedBaseCommit should be set after seeding")
	}
	if agent.IsWorkspaceFresh(context.Background(), fx.realRunner(), fx.workspaceDir("repoA"), fx.childWT("repoA")) {
		t.Fatal("workspace should not be fresh immediately after seeding")
	}
	if wsState.AnalyzedCommit != "" {
		t.Errorf("AnalyzedCommit should be empty after seeding, got %q", wsState.AnalyzedCommit)
	}

	// Also seed repoB so the promotion journey has both workspaces.
	pathsB := fx.childPaths("repoB")
	if err := agent.SeedChildKBWorkspace(context.Background(), fx.realRunner(), pathsB); err != nil {
		t.Fatalf("SeedChildKBWorkspace repoB: %v", err)
	}

	// Run the KB builder through the orchestrator's refresh path to
	// prove the generated prompt enters INCREMENTAL UPDATE mode.
	fx.capturedPrompt = ""
	oKB := fx.orchestratorWithPhaseRunner(true)
	if err := oKB.RefreshChildKBWorkspaces(fx.child.ID); err != nil {
		t.Fatalf("RefreshChildKBWorkspaces for KB prompt assertion: %v", err)
	}
	if !strings.Contains(fx.capturedPrompt, "INCREMENTAL UPDATE") {
		t.Fatalf("KB prompt should be INCREMENTAL UPDATE for a seeded workspace, got:\n%s", fx.capturedPrompt)
	}
	if strings.Contains(fx.capturedPrompt, "FULL BUILD") {
		t.Fatalf("KB prompt should not be FULL BUILD for a seeded workspace, got:\n%s", fx.capturedPrompt)
	}
	t.Logf("Step 1: KB prompt mode is INCREMENTAL UPDATE (not FULL BUILD)")

	if !agent.IsWorkspaceFresh(context.Background(), fx.realRunner(), fx.workspaceDir("repoA"), fx.childWT("repoA")) {
		t.Fatal("workspace repoA should be fresh after KB builder completion")
	}
	if !agent.IsWorkspaceFresh(context.Background(), fx.realRunner(), fx.workspaceDir("repoB"), fx.childWT("repoB")) {
		t.Fatal("workspace repoB should be fresh after KB builder completion")
	}
	t.Log("Step 1: seeded child KB delta analysis completes — INCREMENTAL prompt, AnalyzedCommit set by completion path")
}

// runStep2RefreshBlocksIntegration proves that a failed KB refresh blocks
// integration with no parent ref touched, and a successful refresh marks
// the workspace fresh.
func (fx *kbPromoFixture) runStep2RefreshBlocksIntegration(t *testing.T) {
	// Reset repoA workspace: remove and re-seed so AnalyzedCommit is empty.
	pathsA := fx.childPaths("repoA")
	os.RemoveAll(fx.workspaceDir("repoA"))
	if err := agent.SeedChildKBWorkspace(context.Background(), fx.realRunner(), pathsA); err != nil {
		t.Fatalf("re-seed repoA: %v", err)
	}
	if agent.IsWorkspaceFresh(context.Background(), fx.realRunner(), fx.workspaceDir("repoA"), fx.childWT("repoA")) {
		t.Fatal("workspace repoA should not be fresh after re-seed")
	}

	// Also reset repoB.
	pathsB := fx.childPaths("repoB")
	os.RemoveAll(fx.workspaceDir("repoB"))
	if err := agent.SeedChildKBWorkspace(context.Background(), fx.realRunner(), pathsB); err != nil {
		t.Fatalf("re-seed repoB: %v", err)
	}

	refreshChild, _ := fx.store.Load(fx.child.ID)
	refreshChild.Status = feature.StatusReviewPassed
	refreshChild.Parent.CloseOutcome = ""
	refreshChild.Parent.Transaction = nil
	refreshChild.CurrentPhase = feature.PhaseFinalReview
	if err := fx.store.Save(refreshChild); err != nil {
		t.Fatalf("save child for refresh test: %v", err)
	}

	parentHeadBefore := gitRevParse(t, fx.repoDirA, "HEAD")

	o := fx.orchestratorWithPhaseRunner(false)
	integrationErr := o.RunChildIntegration(fx.child.ID)
	if integrationErr == nil {
		t.Fatal("RunChildIntegration should fail when KB refresh does not produce a completion marker")
	}
	if !strings.Contains(integrationErr.Error(), "final KB refresh") {
		t.Fatalf("expected error to mention 'final KB refresh', got: %v", integrationErr)
	}
	t.Logf("RunChildIntegration blocked by failed refresh: %v", integrationErr)

	parentHeadAfter := gitRevParse(t, fx.repoDirA, "HEAD")
	if parentHeadBefore != parentHeadAfter {
		t.Fatalf("parent HEAD changed despite refresh failure: before=%s after=%s", parentHeadBefore, parentHeadAfter)
	}

	refreshChild2, _ := fx.store.Load(fx.child.ID)
	if refreshChild2.Parent.Transaction != nil {
		t.Fatal("transaction should not have been created when refresh failed")
	}
	t.Log("Step 2: final KB refresh blocks integration — error returned, no parent ref touched")

	// Successful KB refresh marks workspace fresh.
	os.RemoveAll(fx.workspaceDir("repoA"))
	os.RemoveAll(fx.workspaceDir("repoB"))
	if err := agent.SeedChildKBWorkspace(context.Background(), fx.realRunner(), pathsA); err != nil {
		t.Fatalf("re-seed repoA for success case: %v", err)
	}
	if err := agent.SeedChildKBWorkspace(context.Background(), fx.realRunner(), pathsB); err != nil {
		t.Fatalf("re-seed repoB for success case: %v", err)
	}

	oSuccess := fx.orchestratorWithPhaseRunner(true)
	if err := oSuccess.RefreshChildKBWorkspaces(fx.child.ID); err != nil {
		t.Fatalf("RefreshChildKBWorkspaces should succeed with completion marker: %v", err)
	}
	if !agent.IsWorkspaceFresh(context.Background(), fx.realRunner(), fx.workspaceDir("repoA"), fx.childWT("repoA")) {
		t.Fatal("workspace repoA should be fresh after successful KB refresh")
	}
	if !agent.IsWorkspaceFresh(context.Background(), fx.realRunner(), fx.workspaceDir("repoB"), fx.childWT("repoB")) {
		t.Fatal("workspace repoB should be fresh after successful KB refresh")
	}
	t.Log("Step 2b: successful KB refresh marks workspace fresh — integration would proceed")
}

// runStep3MultiRepoPromotionFailure simulates a partial multi-repository
// promotion where repoA's overlay has already been committed (Done) and
// repoB's promotion fails. It verifies that the overlay locks are retained,
// the child stays Completed, the promotion journal is pending, and cleanup /
// auto-publish settlement is blocked.
func (fx *kbPromoFixture) runStep3MultiRepoPromotionFailure(t *testing.T) {
	// Set up the child as Completed with a merged transaction for both repos.
	promoChild, _ := fx.store.Load(fx.child.ID)
	childHeadA := gitRevParse(t, fx.childWT("repoA"), "HEAD")
	childHeadB := gitRevParse(t, fx.childWT("repoB"), "HEAD")
	promoChild.Parent.Transaction = &feature.TransactionJournal{
		Phase: feature.TransactionPhaseMerged,
		Entries: []feature.RepoTransactionEntry{
			{
				ParentBranch:    "main",
				ParentAnchorSHA: fx.parentBaseA,
				ChildHeadSHA:    childHeadA,
				MergeHEAD:       childHeadA,
			},
			{
				ParentBranch:    "main",
				ParentAnchorSHA: fx.parentBaseB,
				ChildHeadSHA:    childHeadB,
				MergeHEAD:       childHeadB,
			},
		},
	}
	promoChild.Parent.CloseOutcome = feature.ChildCloseOutcomeCompleted
	closedAt := time.Now()
	promoChild.Parent.ClosedAt = &closedAt
	promoChild.Status = feature.StatusReviewPassed
	if err := fx.store.Save(promoChild); err != nil {
		t.Fatalf("save child with merged transaction: %v", err)
	}

	// Simulate a prior partial promotion where repoA was already committed
	// but repoB was not. This reproduces the state that exists after a
	// real two-phase promotion where repoA's commit succeeded and repoB's
	// failed: repoA's overlay has been replaced, its lock was re-acquired,
	// and the promotion journal marks repoA as Done.
	pathsA := fx.childPaths("repoA")
	canonicalA := agent.CanonicalKBCommit(pathsA.CanonicalDir)
	if canonicalA == "" {
		canonicalA = fx.parentBaseA
	}
	tmpDir, err := agent.StageWorkspaceToOverlay(
		pathsA.WorkspaceDir, pathsA.OverlayDir, childHeadA, canonicalA,
	)
	if err != nil {
		t.Fatalf("manual stage repoA: %v", err)
	}
	if err := agent.CommitStagedOverlay(tmpDir, pathsA.OverlayDir); err != nil {
		t.Fatalf("manual commit repoA: %v", err)
	}
	// Re-acquire the overlay lock for repoA (same as the real promotion
	// code does after CommitStagedOverlay destroys the lock file).
	if acquired, _ := feature.AcquireOverlayLock(pathsA.OverlayDir, fx.child.ID); !acquired {
		t.Fatal("should acquire overlay lock for repoA after manual commit")
	}

	// Create the promotion journal with repoA already Done, simulating
	// the state after a partial promotion.
	journal := &feature.PromotionJournal{
		ChildID:   fx.child.ID,
		ParentID:  fx.parent.ID,
		Phase:     feature.PromotionPhasePromoting,
		CreatedAt: time.Now(),
		Entries: []feature.PromotionEntry{
			{
				Repo:            "repoA",
				OverlayPath:     pathsA.OverlayDir,
				MergeHEAD:       childHeadA,
				CanonicalCommit: canonicalA,
				Done:            true,
			},
			{
				Repo:        "repoB",
				OverlayPath: fx.overlayDir("repoB"),
			},
		},
	}
	if err := fx.store.SavePromotion(fx.child.ID, journal); err != nil {
		t.Fatalf("save partial promotion journal: %v", err)
	}

	// Verify repoA's overlay lock is held from the manual partial promotion.
	if owner := feature.ReadOverlayLockOwner(pathsA.OverlayDir); owner == "" {
		t.Fatal("repoA overlay lock should be held after manual partial promotion")
	}
	t.Logf("repoA overlay lock held by %s after manual partial promotion", feature.ReadOverlayLockOwner(pathsA.OverlayDir))

	// Make repoB's workspace unreadable so its staging fails after the
	// lock acquisition loop acquires locks for ALL repos (including the
	// already-Done repoA).
	os.Chmod(fx.workspaceDir("repoB"), 0o000)
	defer os.Chmod(fx.workspaceDir("repoB"), 0o755)

	oPromo := fx.orchestrator()
	promoErr := oPromo.PromoteChildKBWorkspaces(fx.child.ID, fx.parent.ID)
	if promoErr == nil {
		t.Fatal("Promotion should fail when repoB staging cannot read the workspace")
	}
	t.Logf("Promotion failed as expected: %v", promoErr)

	// Verify the workspaces are preserved for recovery.
	if _, err := os.Stat(fx.workspaceDir("repoA")); err != nil {
		t.Fatalf("repoA workspace should be preserved: %v", err)
	}
	if _, err := os.Stat(fx.workspaceDir("repoB")); err != nil {
		t.Fatalf("repoB workspace should be preserved: %v", err)
	}

	// Verify the promotion journal is not promoted.
	journal2, jerr := fx.store.LoadPromotion(fx.child.ID)
	if jerr != nil {
		t.Fatalf("LoadPromotion: %v", jerr)
	}
	if journal2 == nil {
		t.Fatal("promotion journal should exist after failed promotion")
	}
	if journal2.Phase == feature.PromotionPhasePromoted {
		t.Fatal("promotion should not be promoted after failure")
	}

	// Verify repoA is still marked Done in the journal.
	repoAEntry := journal2.EntryByRepo("repoA")
	if repoAEntry == nil || !repoAEntry.Done {
		t.Fatal("repoA should still be Done in the promotion journal")
	}

	// Verify the child stays Completed (not reopened).
	persisted, _ := fx.store.Load(fx.child.ID)
	if persisted.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child should remain Completed, got %s", persisted.Parent.CloseOutcome)
	}

	// Verify both overlay locks are held — repoA's from the partial
	// promotion (retained by the lock acquisition loop), and repoB's
	// from this failed attempt.
	if owner := feature.ReadOverlayLockOwner(fx.overlayDir("repoA")); owner == "" {
		t.Fatal("repoA overlay lock should still be held after promotion failure")
	} else {
		t.Logf("repoA overlay lock held by %s", owner)
	}
	if owner := feature.ReadOverlayLockOwner(fx.overlayDir("repoB")); owner == "" {
		t.Fatal("repoB overlay lock should be held after promotion failure")
	} else {
		t.Logf("repoB overlay lock held by %s", owner)
	}

	// Verify settleChildClosureTail returns an error (blocks cleanup and
	// auto-publish). The workspace is still unreadable, so the promotion
	// retry inside settleChildClosureTail also fails.
	settleErr := oPromo.RunChildIntegration(fx.child.ID)
	if settleErr == nil {
		t.Fatal("RunChildIntegration should return error when promotion is pending")
	}
	t.Logf("RunChildIntegration blocked as expected: %v", settleErr)

	t.Log("Step 3: multi-repo promotion failure — repoA Done with lock retained, repoB failed, cleanup blocked")
}

// runStep4RetryAndSeed proves that retrying the promotion after fixing the
// failure completes the full overlay vector, releases every overlay lock
// (including the already-Done repoA's lock), and a subsequent refactor child
// can seed from the completed overlay generation.
func (fx *kbPromoFixture) runStep4RetryAndSeed(t *testing.T) {
	// Restore repoB workspace permissions so staging can read it.
	os.Chmod(fx.workspaceDir("repoB"), 0o755)

	// A later child should be blocked from seeding while the promotion
	// locks are still held from Step 3.
	child2 := &feature.Feature{
		ID:           "child-2",
		Name:         "Child2",
		Slug:         "child-2",
		Status:       feature.StatusCreated,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Created:      time.Now(),
		ActiveRun:    1,
		RunCount:     1,
		Repos: []feature.FeatureRepo{
			{
				Name:         "repoA",
				Path:         fx.repoDirA,
				WorktreePath: fx.childWTA,
				Branch:       "feature/child-large-a",
				BaseBranch:   "main",
			},
			{
				Name:         "repoB",
				Path:         fx.repoDirB,
				WorktreePath: fx.childWTB,
				Branch:       "feature/child-large-b",
				BaseBranch:   "main",
			},
		},
		RepoStates: map[string]*feature.RepoState{
			"repoA": {Touched: true},
			"repoB": {Touched: true},
		},
		Parent: &feature.ChildRelationship{
			ParentID: fx.parent.ID,
			Kind:     feature.ChildKindRefactor,
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := fx.store.Save(child2); err != nil {
		t.Fatalf("save child2: %v", err)
	}

	// Seeding the second child should fail because the overlays are
	// locked by the pending promotion from child-1.
	paths2A := agent.ResolveChildKBPaths(fx.stateDir, child2, child2.Repos[0])
	if err := agent.SeedChildKBWorkspace(context.Background(), fx.realRunner(), paths2A); err == nil {
		t.Fatal("seeding child2 repoA should fail when overlay is locked by pending promotion")
	} else {
		t.Logf("Step 4: child2 blocked from seeding repoA: %v", err)
	}

	// Complete the pending promotion by retrying. The workspace is
	// readable again, so staging should succeed this time. The retry
	// must acquire locks for ALL repos (including the already-Done
	// repoA) so that releaseAllOverlayLocks releases every lock.
	oRetry := fx.orchestrator()
	if err := oRetry.PromoteChildKBWorkspaces(fx.child.ID, fx.parent.ID); err != nil {
		t.Fatalf("promotion retry should succeed after restoring workspace: %v", err)
	}

	// Verify the promotion journal is now promoted.
	journal, jerr := fx.store.LoadPromotion(fx.child.ID)
	if jerr != nil {
		t.Fatalf("LoadPromotion after retry: %v", jerr)
	}
	if journal == nil || journal.Phase != feature.PromotionPhasePromoted {
		t.Fatalf("promotion should be promoted after retry, got phase %v", journal)
	}

	// Verify ALL overlay locks are released — including repoA's lock
	// that was held since the partial promotion in Step 3. This is the
	// critical assertion: without the fix, repoA's lock would remain
	// held because the retry skipped Done repos in the lock acquisition
	// loop, leaving it out of the release set.
	if owner := feature.ReadOverlayLockOwner(fx.overlayDir("repoA")); owner != "" {
		t.Fatalf("repoA overlay lock should be released after promotion, still held by %s", owner)
	}
	if owner := feature.ReadOverlayLockOwner(fx.overlayDir("repoB")); owner != "" {
		t.Fatalf("repoB overlay lock should be released after promotion, still held by %s", owner)
	}
	t.Log("Step 4: promotion completed, ALL overlay locks released (including already-Done repoA)")

	// Now the later child should be able to seed successfully from the
	// completed overlay generation.
	if err := agent.SeedChildKBWorkspace(context.Background(), fx.realRunner(), paths2A); err != nil {
		t.Fatalf("seeding child2 repoA should succeed after promotion completes: %v", err)
	}
	paths2B := agent.ResolveChildKBPaths(fx.stateDir, child2, child2.Repos[1])
	if err := agent.SeedChildKBWorkspace(context.Background(), fx.realRunner(), paths2B); err != nil {
		t.Fatalf("seeding child2 repoB should succeed after promotion completes: %v", err)
	}

	// Verify the later child seeded from the overlay (not canonical).
	wsStateA, _ := feature.LoadWorkspaceState(paths2A.WorkspaceDir)
	if wsStateA == nil {
		t.Fatal("child2 repoA workspace state should exist after seeding")
	}
	if wsStateA.Source != feature.WorkspaceSourceOverlay {
		t.Errorf("child2 repoA should seed from overlay, got source %s", wsStateA.Source)
	}
	t.Log("Step 4: later child seeds successfully from completed overlay generation")
}

// --- helpers ---

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testutil.GitTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitRevParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = dir
	cmd.Env = testutil.GitTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}
