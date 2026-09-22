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

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestRoadmapApprovalBranchLifecycle drives a real two-repository feature
// from creation through worktree setup to roadmap approval: every worktree
// starts on the provisional layer-1 branch feature/<slug>-<id>/1-<slug>, and
// the human proceed renames each checked-out branch in place to the approved
// layer-1 name, recording it on the repository records, the worktree setup
// tasks, and the stack, with the flat feature/<slug>-<id> name absent from
// every repository's refs.
func TestRoadmapApprovalBranchLifecycle(t *testing.T) {
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

	// Creation + setup: every repository record and worktree lands on the
	// provisional layer-1 branch.
	f, err := mgr.Create("Branch Lifecycle", "drives the provisional layer-1 branch",
		[]string{"repo-a", "repo-b"}, cfg.Defaults.Models, "", "", nil,
		feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RunSetup(f.ID); err != nil {
		t.Fatalf("run setup: %v", err)
	}
	f, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	workspaceSlug := feature.WorkspaceSlug(f.Slug, f.ID)
	provisional := git.LayerBranchName(workspaceSlug, 1, f.Slug)
	for _, repo := range f.Repos {
		if repo.Branch != provisional {
			t.Fatalf("repo %s recorded branch = %q, want provisional %q", repo.Name, repo.Branch, provisional)
		}
		if got := git.CurrentBranch(repo.WorktreePath); got != provisional {
			t.Fatalf("repo %s worktree is on %q, want provisional %q", repo.Name, got, provisional)
		}
		if task := f.Run().Setup.Tasks["worktree:"+repo.Name]; task.Branch != provisional {
			t.Fatalf("repo %s setup task branch = %q, want %q", repo.Name, task.Branch, provisional)
		}
	}

	// Roadmap with a two-row pull-request table whose layer-1 slug differs
	// from the feature slug, so the approval rename is visible.
	roadmap := "# Roadmap\n\n## Phase 1: Bootstrap\n### Goal\nInit\n\n## Phase 2: Build\n### Goal\nBuild\n\n" +
		"## Pull Requests\n\n| # | Title | Phases | Rationale |\n|---|---|---|---|\n" +
		"| 1 | Bootstrap | 1 | Stands alone. |\n| 2 | Build | 2 | Stands alone. |\n"
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

	if err := orch.HandleReviewDecision(f.ID, orchestrator.ReviewDecision{
		Decision: "proceed",
		Roadmap:  true,
	}); err != nil {
		t.Fatalf("HandleReviewDecision: %v", err)
	}

	approved, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after approval: %v", err)
	}
	approvedOne := git.LayerBranchName(workspaceSlug, 1, "bootstrap")
	approvedTwo := git.LayerBranchName(workspaceSlug, 2, "build")
	for _, repo := range approved.Repos {
		if repo.Branch != approvedOne {
			t.Fatalf("repo %s recorded branch = %q, want approved %q", repo.Name, repo.Branch, approvedOne)
		}
		if got := git.CurrentBranch(repo.WorktreePath); got != approvedOne {
			t.Fatalf("repo %s worktree is on %q, want approved %q", repo.Name, got, approvedOne)
		}
		if task := approved.Run().Setup.Tasks["worktree:"+repo.Name]; task.Branch != approvedOne {
			t.Fatalf("repo %s setup task branch = %q, want %q", repo.Name, task.Branch, approvedOne)
		}
		// The flat name never exists as a ref: the namespace is nested only.
		mainRepo := repo.Path
		if flat := runGit(t, mainRepo, "for-each-ref", "--format=%(refname)", "refs/heads/feature/"+workspaceSlug); strings.Contains(flat, "\n") || flat != "refs/heads/"+approvedOne {
			t.Fatalf("repo %s refs under the prefix = %q, want only %q", repo.Name, flat, "refs/heads/"+approvedOne)
		}
	}
	if len(approved.Stack) != 2 ||
		approved.Stack[0].Branch != approvedOne ||
		approved.Stack[1].Branch != approvedTwo {
		t.Fatalf("stack = %+v, want branch names %q and %q", approved.Stack, approvedOne, approvedTwo)
	}
	if approved.TotalRoadmapPhases != 2 {
		t.Fatalf("TotalRoadmapPhases = %d, want 2", approved.TotalRoadmapPhases)
	}
	if approved.CurrentRoadmapPhase != 1 {
		t.Fatalf("CurrentRoadmapPhase = %d, want 1 after approval", approved.CurrentRoadmapPhase)
	}
}
