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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestLayerBoundaryLifecycle drives a real two-repository, two-layer,
// three-phase feature from creation and setup through roadmap approval and
// each phase completion: layer 1 covers phases 1 and 2, layer 2 covers
// phase 3. Completing phase 2 records layer 1's per-repository tips from
// its refs and splits every worktree onto layer 2's branch; completing
// phase 3 records only layer 2's tips. Per repository, the invariant holds:
// the worktree HEAD is the top layer's branch, the base branch tip, every
// layer tip, and HEAD form one linear ancestry chain, and the repository
// untouched by layer 2 records a layer-2 tip equal to its layer-1 tip.
func TestLayerBoundaryLifecycle(t *testing.T) {
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

	f, err := mgr.Create("Layer Boundary Lifecycle", "drives the layer boundary split",
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

	// A three-phase roadmap whose pull-request table splits it into two
	// layers: "Bootstrap" covers phases 1-2, "Build and polish" covers
	// phase 3.
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

	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: mgr,
		Store:     store,
		Worktrees: wm,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})
	// The final phase's deferred Final Review pass is stubbed to all_passed;
	// manual publish keeps the roadmap-final completion at CodeReady.
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

	// Phase 1 (mid-layer): commits in both repositories, no boundary.
	startImplementing(1)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-1-a.txt", "layer 1 work\n", "phase 1 repo a")
	testutil.CommitFile(t, worktreeFor("repo-b"), "phase-1-b.txt", "layer 1 work\n", "phase 1 repo b")
	completePhase()

	ff, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after phase 1: %v", err)
	}
	if ff.CurrentRoadmapPhase != 2 {
		t.Fatalf("CurrentRoadmapPhase = %d after phase 1, want 2", ff.CurrentRoadmapPhase)
	}
	for _, repo := range ff.Repos {
		if got := git.CurrentBranch(repo.WorktreePath); got != layer1Branch {
			t.Fatalf("after phase 1, repo %s worktree is on %q, want %q", repo.Name, got, layer1Branch)
		}
	}
	if tips := ff.Stack[0].Repos; len(tips) != 0 {
		t.Fatalf("layer 1 tips recorded at a mid-layer phase: %v", tips)
	}

	// Phase 2 (layer 1's last): commits in both repositories, then the
	// boundary split onto layer 2's branch.
	startImplementing(2)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-2-a.txt", "more layer 1 work\n", "phase 2 repo a")
	testutil.CommitFile(t, worktreeFor("repo-b"), "phase-2-b.txt", "more layer 1 work\n", "phase 2 repo b")
	completePhase()

	ff, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after phase 2: %v", err)
	}
	if ff.CurrentRoadmapPhase != 3 {
		t.Fatalf("CurrentRoadmapPhase = %d after phase 2, want 3", ff.CurrentRoadmapPhase)
	}
	layer1TipA := refSHA(t, worktreeFor("repo-a"), layer1Branch)
	layer1TipB := refSHA(t, worktreeFor("repo-b"), layer1Branch)
	for _, repo := range ff.Repos {
		if got := git.CurrentBranch(repo.WorktreePath); got != layer2Branch {
			t.Fatalf("after phase 2, repo %s worktree is on %q, want %q", repo.Name, got, layer2Branch)
		}
		if repo.Branch != layer2Branch {
			t.Fatalf("after phase 2, repo %s recorded branch = %q, want %q", repo.Name, repo.Branch, layer2Branch)
		}
		if task := ff.Run().Setup.Tasks["worktree:"+repo.Name]; task.Branch != layer2Branch {
			t.Fatalf("after phase 2, repo %s setup task branch = %q, want %q", repo.Name, task.Branch, layer2Branch)
		}
	}
	if got := ff.Stack[0].Repos["repo-a"].TipSHA; got != layer1TipA {
		t.Fatalf("layer 1 repo-a tip = %q, want its ref %q", got, layer1TipA)
	}
	if got := ff.Stack[0].Repos["repo-b"].TipSHA; got != layer1TipB {
		t.Fatalf("layer 1 repo-b tip = %q, want its ref %q", got, layer1TipB)
	}

	// Phase 3 (the top layer's only phase): a commit in one repository
	// only; the boundary records just the top layer's tips.
	startImplementing(3)
	headBeforeFinal, err := git.CurrentHeadSHA(worktreeFor("repo-a"))
	if err != nil {
		t.Fatalf("head before phase 3: %v", err)
	}
	phase3SHA := testutil.CommitFile(t, worktreeFor("repo-a"), "phase-3-a.txt", "layer 2 work\n", "phase 3 repo a")
	if phase3SHA == headBeforeFinal {
		t.Fatal("phase 3 commit did not move repo-a's HEAD")
	}
	completePhase()

	ff, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after phase 3: %v", err)
	}
	if ff.CurrentRoadmapPhase != 3 {
		t.Fatalf("CurrentRoadmapPhase = %d after the final phase, want 3 (no further advance)", ff.CurrentRoadmapPhase)
	}
	if ff.Status != feature.StatusCodeReady {
		t.Fatalf("status = %v after the final phase, want CodeReady (manual publish)", ff.Status)
	}
	for _, repo := range ff.Repos {
		if got := git.CurrentBranch(repo.WorktreePath); got != layer2Branch {
			t.Fatalf("after phase 3, repo %s worktree is on %q, want %q", repo.Name, got, layer2Branch)
		}
	}

	// The linear chain: base tip <- layer 1 tip <- layer 2 tip <- HEAD,
	// per repository. In the repository untouched by layer 2, the layer-2
	// tip equals the layer-1 tip and HEAD stays at the layer-1 tip.
	assertLinearChain := func(name string) {
		t.Helper()
		wt := worktreeFor(name)
		baseTip := refSHA(t, wt, "main")
		layer1Tip := refSHA(t, wt, layer1Branch)
		layer2Tip := refSHA(t, wt, layer2Branch)
		head, err := git.CurrentHeadSHA(wt)
		if err != nil {
			t.Fatalf("head of %s: %v", name, err)
		}
		runGit(t, wt, "merge-base", "--is-ancestor", baseTip, layer1Tip)
		runGit(t, wt, "merge-base", "--is-ancestor", layer1Tip, layer2Tip)
		runGit(t, wt, "merge-base", "--is-ancestor", layer2Tip, head)
	}
	assertLinearChain("repo-a")
	assertLinearChain("repo-b")

	// The recorded layer-2 tips: HEAD in the repository layer 2 touched,
	// and the layer-1 tip in the repository it did not.
	if got := ff.Stack[1].Repos["repo-a"].TipSHA; got != phase3SHA {
		t.Fatalf("layer 2 repo-a tip = %q, want HEAD after phase 3 %q", got, phase3SHA)
	}
	if got := ff.Stack[1].Repos["repo-b"].TipSHA; got != layer1TipB {
		t.Fatalf("layer 2 repo-b tip = %q, want the layer-1 tip %q (untouched by layer 2)", got, layer1TipB)
	}
}

// refSHA resolves a branch ref to its SHA inside a worktree.
func refSHA(t *testing.T, worktreePath, branch string) string {
	t.Helper()
	sha, err := git.ReadRefSHA(worktreePath, "refs/heads/"+branch)
	if err != nil {
		t.Fatalf("ref %s in %s: %v", branch, worktreePath, err)
	}
	return sha
}
