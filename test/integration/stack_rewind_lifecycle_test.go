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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestStackRewindLifecycle drives a real two-repository, two-layer,
// three-phase feature (layer 1 covers phases 1 and 2, layer 2 covers
// phase 3) from creation and setup through roadmap approval and the
// phase-2 boundary, committing in both repositories in each phase and in
// the top layer after the boundary. It then exercises, in sequence: a
// partial rewind to phase 3 (each worktree stays on layer 2's branch,
// reset to layer 1's recorded tip); a partial rewind to phase 2 (each
// worktree switches to layer 1's branch, reset to the phase-1 anchor,
// layer 2's ref deleted); and a full rewind to the Plan phase (every
// layer ref but the checked-out one deleted, that branch renamed to the
// provisional layer-1 name, reset to base) followed by a roadmap approval
// on the new run, proving the full-rewind end state composes with the
// approval rename.
func TestStackRewindLifecycle(t *testing.T) {
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

	f, err := mgr.Create("Stack Rewind Lifecycle", "drives stack-aware rewinds",
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
	provisionalBranch := git.LayerBranchName(workspaceSlug, 1, f.Slug)

	// A three-phase roadmap whose pull-request table splits it into two
	// layers: "Bootstrap" covers phases 1-2, "Build and polish" covers
	// phase 3.
	roadmap := "# Roadmap\n\n## Phase 1: Bootstrap\n### Goal\nInit\n\n## Phase 2: Build\n### Goal\nBuild\n\n## Phase 3: Polish\n### Goal\nPolish\n\n" +
		"## Pull Requests\n\n| # | Title | Phases | Rationale |\n|---|---|---|---|\n" +
		"| 1 | Bootstrap | 1-2 | Stands alone. |\n| 2 | Build and polish | 3 | Stands alone. |\n"
	writeRoadmap := func(runNumber int) string {
		t.Helper()
		roadmapDir := filepath.Join(stateDir, f.ID, "runs", feature.RunDirName(runNumber), "roadmap")
		if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
			t.Fatalf("mkdir roadmap: %v", err)
		}
		roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
		if err := os.WriteFile(roadmapPath, []byte(roadmap), 0o644); err != nil {
			t.Fatalf("write roadmap: %v", err)
		}
		return roadmapPath
	}
	planGate := feature.PhasePlan
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPlanNeedsReview
		ff.PendingReviewPhase = &planGate
		ff.Artifacts = map[string]string{"roadmap": writeRoadmap(1)}
		return nil
	}); err != nil {
		t.Fatalf("seed roadmap gate: %v", err)
	}

	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: mgr,
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
			// Both repositories carry work in every phase; the touched
			// state drives the per-repo anchor recording at each boundary.
			if ff.RepoStates == nil {
				ff.RepoStates = make(map[string]*feature.RepoState)
			}
			for _, name := range []string{"repo-a", "repo-b"} {
				st := ff.RepoStates[name]
				if st == nil {
					st = &feature.RepoState{}
					ff.RepoStates[name] = st
				}
				st.Touched = true
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
	rewind := func(request feature.RewindRequest) []feature.RewindWarning {
		t.Helper()
		warnings, _, err := mgr.RewindWithRequest(f.ID, request)
		if err != nil {
			t.Fatalf("RewindWithRequest(%+v): %v", request, err)
		}
		if len(warnings) != 0 {
			t.Fatalf("RewindWithRequest(%+v) warnings = %+v; want none", request, warnings)
		}
		return warnings
	}
	// Each rewind's backup branch names the unix second it ran in; spacing
	// consecutive rewinds apart keeps every backup creatable.
	spacedRewind := func(request feature.RewindRequest) {
		t.Helper()
		time.Sleep(1100 * time.Millisecond)
		rewind(request)
	}
	assertOnBranch := func(branch string) {
		t.Helper()
		ff, err := mgr.Get(f.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		for _, repo := range ff.Repos {
			if got := git.CurrentBranch(repo.WorktreePath); got != branch {
				t.Fatalf("repo %s worktree is on %q, want %q", repo.Name, got, branch)
			}
			if repo.Branch != branch {
				t.Fatalf("repo %s recorded branch = %q, want %q", repo.Name, repo.Branch, branch)
			}
		}
	}
	assertHeadEquals := func(want func(worktree string) string) {
		t.Helper()
		for _, name := range []string{"repo-a", "repo-b"} {
			wt := worktreeFor(name)
			head, err := git.CurrentHeadSHA(wt)
			if err != nil {
				t.Fatalf("head of %s: %v", name, err)
			}
			if head != want(wt) {
				t.Fatalf("repo %s HEAD = %s, want %s", name, head, want(wt))
			}
		}
	}
	backupBranches := func(worktree string) []string {
		t.Helper()
		out := runGit(t, worktree, "branch", "--list", "feature/"+f.Slug+"-pre-rewind-*")
		var names []string
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if name := strings.TrimSpace(line); name != "" {
				names = append(names, name)
			}
		}
		return names
	}
	featureBranches := func(worktree string) []string {
		t.Helper()
		out := runGit(t, worktree, "for-each-ref", "--format=%(refname:short)", "refs/heads")
		var names []string
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if name := strings.TrimSpace(line); strings.HasPrefix(name, "feature/") {
				names = append(names, name)
			}
		}
		return names
	}

	// Phase 1 (mid-layer): commits in both repositories, no boundary.
	startImplementing(1)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-1-a.txt", "layer 1 work\n", "phase 1 repo a")
	testutil.CommitFile(t, worktreeFor("repo-b"), "phase-1-b.txt", "layer 1 work\n", "phase 1 repo b")
	completePhase()

	ff, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after phase 1: %v", err)
	}
	phase1Anchors := map[string]string{
		"repo-a": ff.Run().RoadmapPhaseCommitAnchors[1]["repo-a"],
		"repo-b": ff.Run().RoadmapPhaseCommitAnchors[1]["repo-b"],
	}
	for name, anchor := range phase1Anchors {
		if anchor == "" {
			t.Fatalf("phase-1 anchor for %s is empty", name)
		}
	}

	// Phase 2 (layer 1's last): commits in both repositories, then the
	// boundary split onto layer 2's branch.
	startImplementing(2)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-2-a.txt", "more layer 1 work\n", "phase 2 repo a")
	testutil.CommitFile(t, worktreeFor("repo-b"), "phase-2-b.txt", "more layer 1 work\n", "phase 2 repo b")
	completePhase()

	layer1Tips := map[string]string{
		"repo-a": refSHA(t, worktreeFor("repo-a"), layer1Branch),
		"repo-b": refSHA(t, worktreeFor("repo-b"), layer1Branch),
	}

	// The top layer after the boundary: commits in both repositories, then
	// a partial rewind to phase 3 — the first phase of layer 2.
	startImplementing(3)
	preRewindHeads := map[string]string{
		"repo-a": testutil.CommitFile(t, worktreeFor("repo-a"), "phase-3-a.txt", "layer 2 work\n", "phase 3 repo a"),
		"repo-b": testutil.CommitFile(t, worktreeFor("repo-b"), "phase-3-b.txt", "layer 2 work\n", "phase 3 repo b"),
	}
	rewind(feature.RewindRequest{TargetPhase: feature.PhaseImplement, RoadmapPhase: 3})

	assertOnBranch(layer2Branch)
	assertHeadEquals(func(worktree string) string {
		return layer1Tips[repoNameForWorktree(t, mgr, f.ID, worktree)]
	})
	for _, name := range []string{"repo-a", "repo-b"} {
		wt := worktreeFor(name)
		if got := refSHA(t, wt, layer1Branch); got != layer1Tips[name] {
			t.Fatalf("after the phase-3 rewind, %s layer 1 ref = %s, want it unchanged at %s", name, got, layer1Tips[name])
		}
		backups := backupBranches(wt)
		if len(backups) == 0 {
			t.Fatalf("after the phase-3 rewind, %s has no backup branch", name)
		}
		if got := refSHA(t, wt, backups[len(backups)-1]); got != preRewindHeads[name] {
			t.Fatalf("after the phase-3 rewind, %s backup %s = %s, want the pre-rewind HEAD %s", name, backups[len(backups)-1], got, preRewindHeads[name])
		}
	}
	sealedRun, err := store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if sealedRun.RewindRoadmapPhase == nil || *sealedRun.RewindRoadmapPhase != 3 {
		t.Fatalf("sealed run rewind roadmap phase = %v, want 3", sealedRun.RewindRoadmapPhase)
	}
	activeStack, err := store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	if len(activeStack.Stack) != 2 {
		t.Fatalf("active run stack = %+v, want both layer definitions", activeStack.Stack)
	}
	if got := activeStack.Stack[0].Repos["repo-a"].TipSHA; got != layer1Tips["repo-a"] {
		t.Fatalf("layer 1 repo-a entry = %q, want the recorded tip %q", got, layer1Tips["repo-a"])
	}
	if activeStack.Stack[1].Repos != nil {
		t.Fatalf("layer 2 entries = %+v, want them cleared", activeStack.Stack[1].Repos)
	}

	// A partial rewind to phase 2 from the resulting run: switch to layer
	// 1's branch, reset to the phase-1 anchor, delete layer 2's ref.
	spacedRewind(feature.RewindRequest{TargetPhase: feature.PhaseImplement, RoadmapPhase: 2})

	assertOnBranch(layer1Branch)
	assertHeadEquals(func(worktree string) string {
		return phase1Anchors[repoNameForWorktree(t, mgr, f.ID, worktree)]
	})
	for _, name := range []string{"repo-a", "repo-b"} {
		wt := worktreeFor(name)
		if _, err := git.ReadRefSHA(wt, "refs/heads/"+layer2Branch); err == nil {
			t.Fatalf("after the phase-2 rewind, %s still has layer 2's ref", name)
		}
	}
	activeStack, err = store.LoadRun(f.ID, 3)
	if err != nil {
		t.Fatalf("LoadRun(3): %v", err)
	}
	if len(activeStack.Stack) != 2 {
		t.Fatalf("active run stack = %+v, want both layer definitions", activeStack.Stack)
	}
	for _, layer := range activeStack.Stack {
		if layer.Repos != nil {
			t.Fatalf("layer %d entries = %+v, want every entry cleared", layer.Position, layer.Repos)
		}
	}

	// A full rewind to the Plan phase: collapse to the provisional
	// layer-1 branch at the base tip.
	spacedRewind(feature.RewindRequest{TargetPhase: feature.PhasePlan})

	assertOnBranch(provisionalBranch)
	assertHeadEquals(func(worktree string) string {
		return refSHA(t, worktree, "main")
	})
	for _, name := range []string{"repo-a", "repo-b"} {
		wt := worktreeFor(name)
		branches := featureBranches(wt)
		allowed := map[string]bool{provisionalBranch: true}
		for _, backup := range backupBranches(wt) {
			allowed[backup] = true
		}
		for _, branch := range branches {
			if !allowed[branch] {
				t.Fatalf("after the full rewind, %s still has branch %q; want only %q and backups (all: %v)", name, branch, provisionalBranch, branches)
			}
		}
		if !allowed[provisionalBranch] || len(branches) == 0 {
			t.Fatalf("after the full rewind, %s is missing the provisional branch %q (all: %v)", name, provisionalBranch, branches)
		}
	}
	activeRun, err := store.LoadRun(f.ID, 4)
	if err != nil {
		t.Fatalf("LoadRun(4): %v", err)
	}
	if activeRun.Stack != nil {
		t.Fatalf("active run stack = %+v, want none after a full rewind", activeRun.Stack)
	}

	// A roadmap approval on the new run renames each worktree from the
	// provisional branch to the approved layer-1 name and persists the
	// stack, proving the full-rewind end state composes with the Phase 3
	// rename.
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPlanNeedsReview
		ff.PendingReviewPhase = &planGate
		ff.Artifacts = map[string]string{"roadmap": writeRoadmap(4)}
		return nil
	}); err != nil {
		t.Fatalf("seed roadmap gate: %v", err)
	}
	if err := orch.HandleReviewDecision(f.ID, orchestrator.ReviewDecision{
		Decision: "proceed",
		Roadmap:  true,
	}); err != nil {
		t.Fatalf("HandleReviewDecision after full rewind: %v", err)
	}

	assertOnBranch(layer1Branch)
	ff, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after approval: %v", err)
	}
	if len(ff.Stack) != 2 {
		t.Fatalf("stack after approval = %+v, want both layers persisted", ff.Stack)
	}
	if ff.Stack[0].Branch != layer1Branch || ff.Stack[1].Branch != layer2Branch {
		t.Fatalf("stack branches after approval = [%s, %s], want [%s, %s]",
			ff.Stack[0].Branch, ff.Stack[1].Branch, layer1Branch, layer2Branch)
	}
	for _, name := range []string{"repo-a", "repo-b"} {
		wt := worktreeFor(name)
		if _, err := git.ReadRefSHA(wt, "refs/heads/"+provisionalBranch); err == nil {
			t.Fatalf("after approval, %s still has the provisional ref %q", name, provisionalBranch)
		}
	}
}

// repoNameForWorktree maps a worktree path back to its repository name.
func repoNameForWorktree(t *testing.T, mgr *feature.Manager, featureID, worktree string) string {
	t.Helper()
	ff, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for _, repo := range ff.Repos {
		if repo.WorktreePath == worktree {
			return repo.Name
		}
	}
	t.Fatalf("no repository owns worktree %q", worktree)
	return ""
}
