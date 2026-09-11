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

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// crashLifecycle wraps the real feature manager and panics inside
// ApplyRestackJournalEntry while armed, simulating a process crash between
// the landed ref transaction and the journal's remap persistence and
// worktree sync — the exact window the restack journal exists to recover.
type crashLifecycle struct {
	*feature.Manager
	crashOnApply atomic.Bool
}

func (w *crashLifecycle) ApplyRestackJournalEntry(featureID, repository string) error {
	if w.crashOnApply.Load() {
		panic("crash injected between the ref transaction and the worktree sync")
	}
	return w.Manager.ApplyRestackJournalEntry(featureID, repository)
}

// TestRestackRelocationLifecycle drives a real two-repository, two-layer,
// three-phase feature (layer 1 covers phases 1 and 2, layer 2 covers phase
// 3) from creation through roadmap approval, both layer boundaries, and the
// final-phase tip recording. It then simulates a Final Review fix round by
// dirtying files in both repositories and invoking the round-commit hook as
// the Final Review loop would, with a fix manifest assigning one file in
// the first repository to layer 1 and listing nothing for the second.
//
// The second scenario injects a crash between the ref transaction and the
// worktree sync, constructs a fresh orchestrator, and runs the startup
// recovery scan. The third performs a partial rewind to phase 3 after the
// successful relocation.
func TestRestackRelocationLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real git repositories with bare origins")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	wtBaseDir := filepath.Join(tmp, "worktrees")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	repoA := testutil.InitGitRepo(t)
	testutil.InitBareRemote(t, repoA)
	repoB := testutil.InitGitRepo(t)
	testutil.InitBareRemote(t, repoB)

	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: repoA}
	cfg.Repos["repo-b"] = config.RepoConfig{Path: repoB}

	store := feature.NewStore(stateDir)
	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = wm

	f, err := mgr.Create("Restack Relocation Lifecycle", "drives final review fix relocation",
		[]string{"repo-a", "repo-b"}, cfg.Defaults.Models, "", "", nil,
		feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RunSetup(f.ID); err != nil {
		t.Fatalf("run setup: %v", err)
	}
	workspaceSlug := feature.WorkspaceSlug(f.Slug, f.ID)
	layer1Branch := git.LayerBranchName(workspaceSlug, 1, "bootstrap")
	layer2Branch := git.LayerBranchName(workspaceSlug, 2, "build-and-polish")

	roadmap := "# Roadmap\n\n## Phase 1: Bootstrap\n### Goal\nInit\n\n## Phase 2: Build\n### Goal\nBuild\n\n## Phase 3: Polish\n### Goal\nPolish\n\n" +
		"## Pull Requests\n\n| # | Title | Phases | Rationale |\n|---|---|---|---|\n" +
		"| 1 | Bootstrap | 1-2 | Stands alone. |\n| 2 | Build and polish | 3 | Stands alone. |\n"
	roadmapDir := filepath.Join(stateDir, f.ID, "runs", "run-001", "roadmap")
	if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir roadmap: %v", err)
	}
	roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
	if err := os.WriteFile(roadmapPath, []byte(roadmap), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}
	planGate := feature.PhasePlan
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPlanNeedsReview
		ff.PendingReviewPhase = &planGate
		ff.Artifacts = map[string]string{"roadmap": roadmapPath}
		return nil
	}); err != nil {
		t.Fatalf("seed roadmap gate: %v", err)
	}

	crashing := &crashLifecycle{Manager: mgr}
	// The lifecycle orchestrator drives approval and phase completion
	// without a PhaseRunner, exactly like the layer-boundary lifecycle
	// test: no session layer is wired, so phase dispatch never starts.
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: crashing,
		Store:     store,
		Worktrees: wm,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})
	orch.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	})
	// The hook orchestrator exists only so New installs the round-commit
	// hook on a PhaseRunner the test invokes by hand, as the Final Review
	// loop would.
	pr := &agent.PhaseRunner{CommandRunner: agent.NewExecCommandRunner()}
	hookOrch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   crashing,
		Store:       store,
		Worktrees:   wm,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = hookOrch.Shutdown()
		hookOrch.WaitForCycles()
	})

	if err := orch.HandleReviewDecision(f.ID, orchestrator.ReviewDecision{
		Decision: "proceed",
		Roadmap:  true,
	}); err != nil {
		t.Fatalf("HandleReviewDecision: %v", err)
	}

	worktreeFor := func(name string) string {
		t.Helper()
		ff, err := mgr.Get(f.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		for _, repo := range ff.Repos {
			if repo.Name == name {
				return repo.WorktreePath
			}
		}
		t.Fatalf("repo %q not found", name)
		return ""
	}
	startImplementing := func(phase int) {
		t.Helper()
		if err := store.Modify(f.ID, func(ff *feature.Feature) error {
			ff.Status = feature.StatusImplementing
			ff.CurrentPhase = feature.PhaseImplement
			ff.CurrentRoadmapPhase = phase
			ff.Checkpoints.ManualPublish = true
			// The implementation sessions mark the repositories they
			// edited as touched; the anchor recorder only visits touched
			// repositories, and this test drives the phases by hand.
			if ff.RepoStates == nil {
				ff.RepoStates = map[string]*feature.RepoState{}
			}
			for _, name := range []string{"repo-a", "repo-b"} {
				state, ok := ff.RepoStates[name]
				if !ok {
					state = &feature.RepoState{}
					ff.RepoStates[name] = state
				}
				state.Touched = true
			}
			return nil
		}); err != nil {
			t.Fatalf("seed implementing phase %d: %v", phase, err)
		}
	}
	completePhase := func() {
		t.Helper()
		if err := orch.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
			Phase:           feature.PhaseImplement,
			MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
		}); err != nil {
			t.Fatalf("HandlePhaseCompletion: %v", err)
		}
	}

	// Phase 1 (mid-layer), phase 2 (the layer-1 boundary), and phase 3 (the
	// top layer's only phase, touched by repo-a alone).
	startImplementing(1)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-1-a.txt", "layer 1 work\n", "phase 1 repo a")
	testutil.CommitFile(t, worktreeFor("repo-b"), "phase-1-b.txt", "layer 1 work\n", "phase 1 repo b")
	completePhase()
	startImplementing(2)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-2-a.txt", "more layer 1 work\n", "phase 2 repo a")
	testutil.CommitFile(t, worktreeFor("repo-b"), "phase-2-b.txt", "more layer 1 work\n", "phase 2 repo b")
	completePhase()
	startImplementing(3)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-3-a.txt", "layer 2 work\n", "phase 3 repo a")
	completePhase()

	ff, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after the phases: %v", err)
	}
	layer1TipABefore := refSHA(t, worktreeFor("repo-a"), layer1Branch)
	layer1TipBBefore := refSHA(t, worktreeFor("repo-b"), layer1Branch)
	if got := ff.Stack[0].Repos["repo-a"].TipSHA; got != layer1TipABefore {
		t.Fatalf("recorded layer-1 tip for repo-a = %s, want its ref %s", got, layer1TipABefore)
	}

	// -------------------------------------------------------------------------
	// Scenario 1: a Final Review fix round with a fix manifest.
	// -------------------------------------------------------------------------
	fixIterDir := filepath.Join(tmp, "fix-iteration")
	if err := os.MkdirAll(fixIterDir, 0o755); err != nil {
		t.Fatalf("mkdir fix iteration: %v", err)
	}
	manifest := "entries:\n  - layer: 1\n    repository: repo-a\n    paths:\n      - fix-layer1-a.txt\n"
	if err := os.WriteFile(filepath.Join(fixIterDir, "fix-manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write fix manifest: %v", err)
	}
	dirtyFix := func(name, filename, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(worktreeFor(name), filename), []byte(content), 0o644); err != nil {
			t.Fatalf("dirty %s in %s: %v", filename, name, err)
		}
	}
	dirtyFix("repo-a", "fix-layer1-a.txt", "relocated fix\n")
	dirtyFix("repo-a", "fix-top-a.txt", "top fix\n")
	dirtyFix("repo-b", "fix-top-b.txt", "top fix\n")

	fixInput := agent.RoundCommitInput{
		FeatureID:       f.ID,
		Iteration:       1,
		Kind:            agent.RoundCommitFinalReviewFix,
		FixNumber:       1,
		FixIterationDir: fixIterDir,
		Repos: map[string]string{
			"repo-a": worktreeFor("repo-a"),
			"repo-b": worktreeFor("repo-b"),
		},
	}
	if err := pr.RoundCommitHook(fixInput); err != nil {
		t.Fatalf("final review fix round commit: %v", err)
	}

	// repo-a: layer 1's ref contains the relocated fix as its newest commit,
	// layer 2's commits are replayed above it with unchanged messages, the
	// unlisted file is in a top-layer commit, and base → layer 1 tip → HEAD
	// is one linear chain.
	wtA := worktreeFor("repo-a")
	layer1TipA := refSHA(t, wtA, layer1Branch)
	if layer1TipA == layer1TipABefore {
		t.Fatal("repo-a layer 1 ref did not move")
	}
	if got := runGit(t, wtA, "log", "-n", "1", "--format=%s", layer1TipA); got != "Final review fix 1 (address review feedback)" {
		t.Fatalf("repo-a layer 1 newest subject = %q, want the relocated fix", got)
	}
	if got := runGit(t, wtA, "show", layer1TipA+":fix-layer1-a.txt"); got != "relocated fix" {
		t.Fatalf("repo-a layer 1 fix content = %q, want the relocated change", got)
	}
	if _, ok := tryRunGit(t, wtA, "cat-file", "-e", layer1TipA+":fix-top-a.txt"); ok {
		t.Fatal("repo-a layer 1 tip contains the unlisted file")
	}
	headA, err := git.CurrentHeadSHA(wtA)
	if err != nil {
		t.Fatalf("repo-a head: %v", err)
	}
	// The replayed phase-3 commit keeps its message above the relocated fix.
	if got := runGit(t, wtA, "log", "--format=%s", layer1TipA+".."+headA); !strings.Contains(got, "phase 3 repo a") {
		t.Fatalf("repo-a subjects above layer 1's tip = %q, want the replayed phase-3 commit", got)
	}
	if got := runGit(t, wtA, "show", headA+":fix-top-a.txt"); got != "top fix" {
		t.Fatalf("repo-a top fix content = %q, want the unlisted file in the top commit", got)
	}
	runGit(t, wtA, "merge-base", "--is-ancestor", refSHA(t, wtA, "main"), layer1TipA)
	runGit(t, wtA, "merge-base", "--is-ancestor", layer1TipA, headA)
	if got := refSHA(t, wtA, layer2Branch); got != headA {
		t.Fatalf("repo-a layer 2 ref = %s, want HEAD %s", got, headA)
	}

	// The recorded anchors and tips equal the corresponding live commits.
	ff, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after the fix round: %v", err)
	}
	if got := ff.Stack[0].Repos["repo-a"].TipSHA; got != layer1TipA {
		t.Fatalf("recorded layer-1 tip = %s, want its ref %s", got, layer1TipA)
	}
	if got := ff.Stack[1].Repos["repo-a"].TipSHA; got != headA {
		t.Fatalf("recorded layer-2 tip = %s, want HEAD %s", got, headA)
	}
	anchors := ff.Run().RoadmapPhaseCommitAnchors
	if got := anchors[3]["repo-a"]; got == "" || got == runGit(t, wtA, "rev-parse", layer1TipABefore+"^") {
		t.Fatalf("phase-3 anchor = %q, want a remapped live commit", got)
	}
	runGit(t, wtA, "merge-base", "--is-ancestor", layer1TipA, anchors[3]["repo-a"])
	runGit(t, wtA, "merge-base", "--is-ancestor", anchors[3]["repo-a"], headA)
	if journal := ff.Run().RestackJournal; len(journal) != 0 {
		t.Fatalf("restack journal after the fix round = %+v, want none", journal)
	}

	// repo-b: a single top-layer commit and lower refs unchanged.
	wtB := worktreeFor("repo-b")
	headB, err := git.CurrentHeadSHA(wtB)
	if err != nil {
		t.Fatalf("repo-b head: %v", err)
	}
	if got := refSHA(t, wtB, layer1Branch); got != layer1TipBBefore {
		t.Fatalf("repo-b layer 1 ref = %s, want unchanged %s", got, layer1TipBBefore)
	}
	if got := runGit(t, wtB, "log", "--format=%s", layer1TipBBefore+".."+headB); got != "Final review fix 1 (address review feedback)" {
		t.Fatalf("repo-b subjects = %q, want a single top-layer commit", got)
	}

	// -------------------------------------------------------------------------
	// Scenario 2: a crash between the ref transaction and the worktree sync,
	// then a fresh orchestrator and the startup recovery scan.
	// -------------------------------------------------------------------------
	crashing.crashOnApply.Store(true)
	defer crashing.crashOnApply.Store(false)
	dirtyFix("repo-a", "fix-layer1-a-2.txt", "second relocated fix\n")
	manifest2 := "entries:\n  - layer: 1\n    repository: repo-a\n    paths:\n      - fix-layer1-a-2.txt\n"
	if err := os.WriteFile(filepath.Join(fixIterDir, "fix-manifest.yaml"), []byte(manifest2), 0o644); err != nil {
		t.Fatalf("write second fix manifest: %v", err)
	}
	crashRecovered := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				crashRecovered = true
			}
		}()
		if err := pr.RoundCommitHook(fixInput); err != nil {
			t.Fatalf("final review fix round commit before the crash: %v", err)
		}
	}()
	if !crashRecovered {
		t.Fatal("the injected crash did not fire between the ref transaction and the worktree sync")
	}
	crashing.crashOnApply.Store(false)

	// Before recovery: every layer ref sits at its new SHA — the worktree's
	// checked-out branch moved underneath it, so HEAD already resolves to
	// the new top — while the journal entry is still prepared and its remap
	// is not persisted.
	layer1TipACrashed := refSHA(t, wtA, layer1Branch)
	if layer1TipACrashed == layer1TipA {
		t.Fatal("repo-a layer 1 ref did not move before the crash recovery")
	}
	if got := runGit(t, wtA, "show", layer1TipACrashed+":fix-layer1-a-2.txt"); got != "second relocated fix" {
		t.Fatalf("repo-a crashed layer 1 fix content = %q, want the relocated change", got)
	}
	ff, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after the crash: %v", err)
	}
	if journal := ff.Run().RestackJournal; len(journal) != 1 || journal[0].Repository != "repo-a" {
		t.Fatalf("restack journal after the crash = %+v, want one repo-a entry", journal)
	}
	entry := ff.Run().RestackJournal[0]
	if entry.State != feature.RestackJournalPrepared || entry.PendingSync {
		t.Fatalf("crashed journal entry = %+v, want prepared without pending sync", entry)
	}
	newTop := entry.NewTopSHA
	if got := refSHA(t, wtA, layer2Branch); got != newTop {
		t.Fatalf("repo-a layer 2 ref = %s after the crash, want the new top %s", got, newTop)
	}
	if got, _ := git.CurrentHeadSHA(wtA); got != newTop {
		t.Fatalf("repo-a HEAD = %s after the crash, want the moved branch ref %s", got, newTop)
	}
	// The remap was not persisted: the recorded tips still predate the
	// crashed relocation.
	if got := ff.Stack[0].Repos["repo-a"].TipSHA; got != layer1TipA {
		t.Fatalf("recorded layer-1 tip after the crash = %s, want the pre-crash tip %s (remap not persisted)", got, layer1TipA)
	}

	// A fresh orchestrator runs the startup recovery scan.
	fresh := orchestrator.New(orchestrator.Deps{
		Lifecycle: mgr,
		Store:     store,
		Worktrees: wm,
		Recovery:  &fakeRecoveryNoop{},
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = fresh.Shutdown()
		fresh.WaitForCycles()
	})
	if _, err := fresh.ScanRecovery(nil); err != nil {
		t.Fatalf("ScanRecovery after the crash: %v", err)
	}

	// After the recovery scan: no journal entry, the anchors and tips are
	// remapped, the worktree HEAD equals the new top, and no rebase or
	// cherry-pick is in progress in any worktree.
	ff, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after recovery: %v", err)
	}
	if journal := ff.Run().RestackJournal; len(journal) != 0 {
		t.Fatalf("restack journal after recovery = %+v, want none", journal)
	}
	if got := ff.Stack[0].Repos["repo-a"].TipSHA; got != layer1TipACrashed {
		t.Fatalf("recorded layer-1 tip after recovery = %s, want its ref %s", got, layer1TipACrashed)
	}
	if got := ff.Stack[1].Repos["repo-a"].TipSHA; got != newTop {
		t.Fatalf("recorded layer-2 tip after recovery = %s, want the new top %s", got, newTop)
	}
	if got, _ := git.CurrentHeadSHA(wtA); got != newTop {
		t.Fatalf("repo-a HEAD after recovery = %s, want the new top %s", got, newTop)
	}
	assertNoOperationInProgress(t, wtA)
	assertNoOperationInProgress(t, wtB)

	// -------------------------------------------------------------------------
	// Scenario 3: a partial rewind to phase 3 after the successful
	// relocation.
	// -------------------------------------------------------------------------
	warnings, _, err := mgr.RewindWithRequest(f.ID, feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 3,
	})
	if err != nil {
		t.Fatalf("RewindToPhase 3 after relocation: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("RewindToPhase 3 warnings = %+v, want none", warnings)
	}
	// Each worktree resets to the remapped layer-1 tip, a live commit
	// reachable from the pre-rewind backup branch.
	for _, name := range []string{"repo-a", "repo-b"} {
		wt := worktreeFor(name)
		head, err := git.CurrentHeadSHA(wt)
		if err != nil {
			t.Fatalf("head of %s after the rewind: %v", name, err)
		}
		wantTip := layer1TipACrashed
		if name == "repo-b" {
			wantTip = layer1TipBBefore
		}
		if head != wantTip {
			t.Fatalf("repo %s HEAD after the rewind = %s, want the remapped layer-1 tip %s", name, head, wantTip)
		}
		backups := runGit(t, wt, "branch", "--list", "feature/"+f.Slug+"-pre-rewind-*")
		names := strings.Fields(backups)
		if len(names) == 0 {
			t.Fatalf("repo %s has no pre-rewind backup branch", name)
		}
		runGit(t, wt, "merge-base", "--is-ancestor", head, names[len(names)-1])
	}
}

// assertNoOperationInProgress fails when a merge, cherry-pick, revert, or
// rebase is in progress in the worktree.
func assertNoOperationInProgress(t *testing.T, worktree string) {
	t.Helper()
	gitDir := runGit(t, worktree, "rev-parse", "--git-dir")
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktree, gitDir)
	}
	for _, marker := range []string{"CHERRY_PICK_HEAD", "MERGE_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, marker)); err == nil {
			t.Fatalf("%s is in progress in %s", marker, worktree)
		}
	}
}

// tryRunGit runs git without failing the test, reporting whether it
// succeeded.
func tryRunGit(t *testing.T, dir string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testutil.GitTestEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}
