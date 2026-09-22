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

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// rewindRemoteOps wires the feature manager's rewind remote-operations seam
// at the production helpers, so the rewind closes pull requests and deletes
// remote branches against the fake GitHub API and the real bare origins.
type rewindRemoteOps struct{}

func (rewindRemoteOps) ClosePR(prURL string) error { return git.ClosePR(prURL) }

func (rewindRemoteOps) PRState(prURL string) (string, error) {
	return git.PRState("", prURL)
}

func (rewindRemoteOps) DeleteRemoteBranch(repoPath, branch string) error {
	return git.DeleteRemoteBranch(repoPath, branch)
}

// TestStackPublishRewindRepublish drives the two-repository, two-layer,
// three-phase fixture from publish through a partial rewind to phase 3
// (layer 2's first phase) and a republish of the rewound layer. The rewind
// closes layer 2's pull requests and deletes its remote branches while
// layer 1's pull requests and remote branches survive; the republish then
// recreates one new layer-2 pull request per repository chained onto layer
// 1's pull request, proving the remote deletion is what keeps a republish
// from hitting a remote-diverged refusal.
func TestStackPublishRewindRepublish(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real git repositories with bare origins")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	wtBaseDir := filepath.Join(tmp, "worktrees")
	remotesDir := filepath.Join(tmp, "remotes")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	repoA := testutil.InitGitRepo(t)
	bareA := stackPublishBareOrigin(t, remotesDir, repoA, "repo-a")
	repoB := testutil.InitGitRepo(t)
	bareB := stackPublishBareOrigin(t, remotesDir, repoB, "repo-b")

	fake := testutil.InstallFakeGitHubAPI(t)
	pulls := testutil.NewFakePullStore("acme")
	pulls.Install(t, fake, "repo-a", "repo-b")

	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: repoA}
	cfg.Repos["repo-b"] = config.RepoConfig{Path: repoB}

	store := feature.NewStore(stateDir)
	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = wm
	mgr.PRs = rewindRemoteOps{}

	f, err := mgr.Create("Stack Publish Rewind Republish", "rewind closes and deletes the upper layer",
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

	lifecycleOrch := orchestrator.New(orchestrator.Deps{
		Lifecycle: mgr,
		Store:     store,
		Worktrees: wm,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = lifecycleOrch.Shutdown()
		lifecycleOrch.WaitForCycles()
	})
	pr := stackPublishDescriptionRunner(t, store, stateDir)
	publishOrch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Worktrees:   wm,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = publishOrch.Shutdown()
		publishOrch.WaitForCycles()
	})
	finalReviewStub := func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	}
	lifecycleOrch.SetRunMultiRepoFinalReviewFn(finalReviewStub)
	publishOrch.SetRunMultiRepoFinalReviewFn(finalReviewStub)

	if err := lifecycleOrch.HandleReviewDecision(f.ID, orchestrator.ReviewDecision{
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
	completePhaseOn := func(orch *orchestrator.Orchestrator) {
		t.Helper()
		if err := orch.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
			Phase:           feature.PhaseImplement,
			MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
		}); err != nil {
			t.Fatalf("HandlePhaseCompletion: %v", err)
		}
	}
	bareRef := func(bare, branch string) string {
		t.Helper()
		return runGit(t, bare, "rev-parse", "refs/heads/"+branch)
	}
	remoteFeatureBranches := func(bare string) []string {
		t.Helper()
		out := runGit(t, bare, "for-each-ref", "--format=%(refname:short)", "refs/heads")
		var names []string
		for _, line := range strings.Split(out, "\n") {
			if name := strings.TrimSpace(line); strings.HasPrefix(name, "feature/") {
				names = append(names, name)
			}
		}
		return names
	}
	prState := func(repo string, number int) string {
		t.Helper()
		record, ok := pulls.Pull(repo, number)
		if !ok {
			t.Fatalf("%s pull %d record missing", repo, number)
		}
		return record.State
	}

	// Phases 1 and 2 land layer 1's commits in repo-a only; phase 3 (layer
	// 2) touches both repositories, mirroring the publish lifecycle fixture.
	startImplementing(1)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-1-a.txt", "layer 1 work\n", "phase 1 repo a")
	completePhaseOn(lifecycleOrch)
	startImplementing(2)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-2-a.txt", "more layer 1 work\n", "phase 2 repo a")
	completePhaseOn(lifecycleOrch)
	startImplementing(3)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-3-a.txt", "layer 2 work\n", "phase 3 repo a")
	testutil.CommitFile(t, worktreeFor("repo-b"), "phase-3-b.txt", "layer 2 work\n", "phase 3 repo b")
	completePhaseOn(publishOrch)

	// -------------------------------------------------------------------------
	// First publish: one PR per layer with commits.
	// -------------------------------------------------------------------------
	if err := publishOrch.PublishWithOptions(f.ID, orchestrator.PublishOptions{}); err != nil {
		t.Fatalf("PublishWithOptions: %v", err)
	}
	prA1 := pulls.URL("repo-a", 1)
	if got := prState("repo-a", 1); got != "open" {
		t.Fatalf("repo-a layer-1 PR state after publish = %q, want open", got)
	}

	ff, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after publish: %v", err)
	}
	layer1Tips := map[string]string{
		"repo-a": ff.Stack[0].Repos["repo-a"].TipSHA,
		"repo-b": ff.Stack[0].Repos["repo-b"].TipSHA,
	}
	for name, tip := range layer1Tips {
		if tip == "" {
			t.Fatalf("layer 1 recorded tip for %s is empty", name)
		}
	}

	// -------------------------------------------------------------------------
	// Partial rewind to phase 3 (layer 2's first phase).
	// -------------------------------------------------------------------------
	warnings, _, err := mgr.RewindWithRequest(f.ID, feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 3,
	})
	if err != nil {
		t.Fatalf("RewindWithRequest: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("RewindWithRequest warnings = %+v, want none", warnings)
	}

	// The fake shows layer 1's pull requests open and layer 2's closed.
	if got := prState("repo-a", 1); got != "open" {
		t.Fatalf("repo-a layer-1 PR state after rewind = %q, want open (below the closing set)", got)
	}
	if got := prState("repo-a", 2); got != "closed" {
		t.Fatalf("repo-a layer-2 PR state after rewind = %q, want closed", got)
	}
	if got := prState("repo-b", 1); got != "closed" {
		t.Fatalf("repo-b layer-2 PR state after rewind = %q, want closed", got)
	}

	// The remotes hold layer 1's branch only under the feature prefix:
	// layer 2's remote branches are gone. (repo-b never had a layer-1
	// branch — the layer delivered no commits there.)
	if got := remoteFeatureBranches(bareA); len(got) != 1 || got[0] != layer1Branch {
		t.Fatalf("repo-a remote feature branches after rewind = %v, want [%s]", got, layer1Branch)
	}
	if got := remoteFeatureBranches(bareB); len(got) != 0 {
		t.Fatalf("repo-b remote feature branches after rewind = %v, want none", got)
	}
	if got := bareRef(bareA, layer1Branch); got != layer1Tips["repo-a"] {
		t.Fatalf("repo-a remote layer-1 ref after rewind = %s, want the recorded tip %s", got, layer1Tips["repo-a"])
	}

	// Each worktree sits on layer 2's branch at layer 1's recorded tip.
	for _, name := range []string{"repo-a", "repo-b"} {
		wt := worktreeFor(name)
		if got := git.CurrentBranch(wt); got != layer2Branch {
			t.Fatalf("%s worktree is on %q after rewind, want %q", name, got, layer2Branch)
		}
		head, err := git.CurrentHeadSHA(wt)
		if err != nil {
			t.Fatalf("head of %s: %v", name, err)
		}
		if head != layer1Tips[name] {
			t.Fatalf("%s HEAD after rewind = %s, want layer 1's recorded tip %s", name, head, layer1Tips[name])
		}
	}

	// The forked run keeps layer 1's entries and clears layer 2's.
	activeRun, err := store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	if len(activeRun.Stack) != 2 {
		t.Fatalf("active run stack = %+v, want both layer definitions", activeRun.Stack)
	}
	if got := activeRun.Stack[0].Repos["repo-a"]; got.PRURL != prA1 || got.LastPushedSHA == "" {
		t.Fatalf("layer 1 repo-a entry after rewind = %+v, want the kept PR %q", got, prA1)
	}
	if got := activeRun.Stack[0].Repos["repo-b"]; !got.NoCommits || got.PRURL != "" {
		t.Fatalf("layer 1 repo-b entry after rewind = %+v, want the kept no-commits marker", got)
	}
	if activeRun.Stack[1].Repos != nil {
		t.Fatalf("layer 2 entries after rewind = %+v, want cleared", activeRun.Stack[1].Repos)
	}

	// -------------------------------------------------------------------------
	// Republish the rewound layer: new work on the new run, then publish.
	// -------------------------------------------------------------------------
	startImplementing(3)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-3-a-again.txt", "redone layer 2 work\n", "phase 3 repo a again")
	testutil.CommitFile(t, worktreeFor("repo-b"), "phase-3-b-again.txt", "redone layer 2 work\n", "phase 3 repo b again")
	completePhaseOn(publishOrch)

	creationsBefore := map[string]int{"repo-a": pulls.CreatedCount("repo-a"), "repo-b": pulls.CreatedCount("repo-b")}
	if err := publishOrch.PublishWithOptions(f.ID, orchestrator.PublishOptions{}); err != nil {
		t.Fatalf("republish after rewind: %v", err)
	}

	// Exactly one new pull request per repository — a fresh number, based
	// on layer 1's branch for repo-a and on the repository base for repo-b
	// (its layer 1 delivered no commits, so there is no layer-1 pull
	// request to chain onto).
	prA3 := pulls.URL("repo-a", 3)
	prB2 := pulls.URL("repo-b", 2)
	created := pulls.Created()
	wantNew := []testutil.FakePullRequest{
		{Repo: "repo-a", Number: 3, Title: "Build and polish", Head: layer2Branch, Base: layer1Branch},
		{Repo: "repo-b", Number: 2, Title: "Build and polish", Head: layer2Branch, Base: "main"},
	}
	for repo, before := range creationsBefore {
		if got := pulls.CreatedCount(repo); got != before+1 {
			t.Fatalf("republish created %d pull requests for %s; want exactly one new layer-2 pull request (%d -> %d)", got-before, repo, before, got)
		}
	}
	for _, want := range wantNew {
		found := false
		for _, got := range created {
			if got.Repo == want.Repo && got.Number == want.Number {
				found = true
				if got.Title != want.Title || got.Head != want.Head || got.Base != want.Base {
					t.Fatalf("new pull request %s#%d = %+v, want %+v", want.Repo, want.Number, got, want)
				}
			}
		}
		if !found {
			t.Fatalf("new pull request %s#%d missing; created = %+v", want.Repo, want.Number, created)
		}
	}
	if got := prState("repo-a", 1); got != "open" {
		t.Fatalf("repo-a layer-1 PR state after republish = %q, want open and untouched", got)
	}

	// The stack entries hold the new URL, the open state, and a pushed SHA
	// equal to the recreated remote tip; layer 1's entries are unchanged.
	ff, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after republish: %v", err)
	}
	newTipA := refSHA(t, worktreeFor("repo-a"), layer2Branch)
	newTipB := refSHA(t, worktreeFor("repo-b"), layer2Branch)
	if got := ff.Stack[1].Repos["repo-a"]; got.PRURL != prA3 || got.PRState != feature.StackPRStateOpen ||
		got.LastPushedSHA != bareRef(bareA, layer2Branch) || got.LastPushedSHA != newTipA {
		t.Fatalf("repo-a layer-2 entry after republish = %+v, want PR %q open at the recreated remote tip %s", got, prA3, newTipA)
	}
	if got := ff.Stack[1].Repos["repo-b"]; got.PRURL != prB2 || got.PRState != feature.StackPRStateOpen ||
		got.LastPushedSHA != bareRef(bareB, layer2Branch) || got.LastPushedSHA != newTipB {
		t.Fatalf("repo-b layer-2 entry after republish = %+v, want PR %q open at the recreated remote tip %s", got, prB2, newTipB)
	}
	if got := ff.Stack[0].Repos["repo-a"]; got.PRURL != prA1 || got.PRState != feature.StackPRStateOpen ||
		got.LastPushedSHA != bareRef(bareA, layer1Branch) {
		t.Fatalf("repo-a layer-1 entry after republish = %+v, want the kept PR %q open at its unchanged tip", got, prA1)
	}
	if got := bareRef(bareA, layer2Branch); got != newTipA {
		t.Fatalf("repo-a remote layer-2 ref after republish = %s, want the new tip %s (recreated through a plain push)", got, newTipA)
	}
	if got := bareRef(bareB, layer2Branch); got != newTipB {
		t.Fatalf("repo-b remote layer-2 ref after republish = %s, want the new tip %s (recreated through a plain push)", got, newTipB)
	}
	if ff.Status != feature.StatusPublished {
		t.Fatalf("status after republish = %v, want Published", ff.Status)
	}
}
