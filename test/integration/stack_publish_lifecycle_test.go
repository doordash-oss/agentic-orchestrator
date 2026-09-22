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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestStackPublishLifecycle drives the real two-repository, two-layer,
// three-phase fixture from roadmap approval through both layer boundaries to
// publish against real bare remotes, with pull-request creation, state, and
// body operations served by an in-test GitHub fake and per-layer bodies from
// a scripted description session. repo-a carries commits in both layers;
// repo-b carries only layer-2 commits, so its layer-1 entry is marked empty
// and its layer-2 pull request bases on the repository's base branch while
// repo-a's layer-2 pull request bases on layer 1's branch.
//
// The second scenario relocates a Final Review fix into repo-a's layer 1
// through the round-commit hook and republishes: layer 1's remote moves to
// the relocated tip, layer 2's rewritten history is delivered by the
// last-pushed lease push, no new pull request is created, and the recorded
// last-pushed SHAs equal the remotes.
func TestStackPublishLifecycle(t *testing.T) {
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

	// The bare origins live in a directory whose name parses as a
	// git@host:owner scp-style remote, so the production origin resolution
	// reads host/owner/repo identity from a remote that never leaves the
	// test machine: every push, fetch, and ls-remote is a local file
	// transfer, and the GitHub REST client is routed at the in-test fake.
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

	f, err := mgr.Create("Stack Publish Lifecycle", "drives bottom-up stack publish",
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

	// The lifecycle orchestrator drives approval and the mid-flight phases
	// without a PhaseRunner, exactly like the layer-boundary fixture, so no
	// plan or implement session is ever dispatched. The publish orchestrator
	// carries the scripted description PhaseRunner; it also owns the final
	// phase's completion (Final Review + the publish tail) and installs the
	// round-commit hook the relocation scenario invokes by hand.
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
			// Publish walks the touched repositories; the implementation
			// sessions mark them, and this test drives the phases by hand.
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

	// Phases 1 and 2 land layer 1's commits in repo-a only; repo-b stays at
	// its base, so layer 1 has no commits there. Phase 3 (layer 2) touches
	// both repositories.
	startImplementing(1)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-1-a.txt", "layer 1 work\n", "phase 1 repo a")
	completePhaseOn(lifecycleOrch)
	startImplementing(2)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-2-a.txt", "more layer 1 work\n", "phase 2 repo a")
	completePhaseOn(lifecycleOrch)
	startImplementing(3)
	testutil.CommitFile(t, worktreeFor("repo-a"), "phase-3-a.txt", "layer 2 work\n", "phase 3 repo a")
	testutil.CommitFile(t, worktreeFor("repo-b"), "phase-3-b.txt", "layer 2 work\n", "phase 3 repo b")
	// The final phase's completion runs on the publish orchestrator so the
	// deferred Final Review and the manual-publish gate share its PhaseRunner.
	completePhaseOn(publishOrch)

	ff, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after the phases: %v", err)
	}
	if ff.Status != feature.StatusCodeReady {
		t.Fatalf("status after the final phase = %v, want CodeReady (manual publish)", ff.Status)
	}

	// -------------------------------------------------------------------------
	// Scenario 1: first publish.
	// -------------------------------------------------------------------------
	if err := publishOrch.PublishWithOptions(f.ID, orchestrator.PublishOptions{}); err != nil {
		t.Fatalf("PublishWithOptions: %v", err)
	}

	prA1 := pulls.URL("repo-a", 1)
	prA2 := pulls.URL("repo-a", 2)
	prB1 := pulls.URL("repo-b", 1)

	// One pull request per layer with commits, in ascending layer order per
	// repository, with the roadmap table titles and the computed bases.
	created := pulls.Created()
	wantCreated := []testutil.FakePullRequest{
		{Repo: "repo-a", Number: 1, Title: "Bootstrap", Head: layer1Branch, Base: "main"},
		{Repo: "repo-a", Number: 2, Title: "Build and polish", Head: layer2Branch, Base: layer1Branch},
		{Repo: "repo-b", Number: 1, Title: "Build and polish", Head: layer2Branch, Base: "main"},
	}
	if len(created) != len(wantCreated) {
		t.Fatalf("created pull requests = %+v, want %+v", created, wantCreated)
	}
	for i, want := range wantCreated {
		got := created[i]
		if got.Repo != want.Repo || got.Number != want.Number || got.Title != want.Title ||
			got.Head != want.Head || got.Base != want.Base {
			t.Fatalf("created pull request %d = %+v, want %+v", i, got, want)
		}
		if got.Draft {
			t.Fatalf("created pull request %d is a draft; want the checkpoint default", i)
		}
	}

	// The bodies come from the scripted description session and carry the
	// stack section (repo-a has two pull requests) and the per-layer
	// cross-repository section.
	prA1Record, ok := pulls.Pull("repo-a", 1)
	if !ok {
		t.Fatal("repo-a pull 1 record missing")
	}
	if !strings.Contains(prA1Record.Body, "Scripted PR body for Bootstrap.") {
		t.Fatalf("repo-a layer-1 body = %q, want the scripted session body", prA1Record.Body)
	}
	stackLine1 := fmt.Sprintf("1. Bootstrap - [#1](%s) (open)", prA1)
	stackLine2 := fmt.Sprintf("2. Build and polish - [#2](%s) (open) (this pull request)", prA2)
	for _, pr := range []struct {
		repo   string
		number int
		url    string
	}{
		{"repo-a", 1, prA1}, {"repo-a", 2, prA2},
	} {
		record, ok := pulls.Pull(pr.repo, pr.number)
		if !ok {
			t.Fatalf("%s pull %d record missing", pr.repo, pr.number)
		}
		if !strings.Contains(record.Body, "## Stack") {
			t.Fatalf("%s#%d body has no stack section:\n%s", pr.repo, pr.number, record.Body)
		}
		if !strings.Contains(record.Body, stackLine1) || !strings.Contains(record.Body, stackLine2) {
			t.Fatalf("%s#%d body stack section lines = missing %q / %q:\n%s", pr.repo, pr.number, stackLine1, stackLine2, record.Body)
		}
	}
	prA2Record, _ := pulls.Pull("repo-a", 2)
	if want := fmt.Sprintf("| repo-b | %s | [#1](%s) |", layer2Branch, prB1); !strings.Contains(prA2Record.Body, want) {
		t.Fatalf("repo-a layer-2 body lacks the sibling link %q:\n%s", want, prA2Record.Body)
	}
	prB1Record, _ := pulls.Pull("repo-b", 1)
	if want := fmt.Sprintf("| repo-a | %s | [#2](%s) |", layer2Branch, prA2); !strings.Contains(prB1Record.Body, want) {
		t.Fatalf("repo-b layer-2 body lacks the sibling link %q:\n%s", want, prB1Record.Body)
	}
	if strings.Contains(prB1Record.Body, "## Stack") {
		t.Fatalf("repo-b body carries a stack section; want none for a single-pull-request repository:\n%s", prB1Record.Body)
	}

	// Every remote branch sits at its recorded tip and last-pushed SHA, and
	// the feature is published with each repository's top-layer pull
	// request as its primary URL.
	bareRef := func(bare, branch string) string {
		t.Helper()
		return runGit(t, bare, "rev-parse", "refs/heads/"+branch)
	}
	ff, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after publish: %v", err)
	}
	if ff.Status != feature.StatusPublished {
		t.Fatalf("status after publish = %v, want Published", ff.Status)
	}
	if got, want := ff.TopStackLayerPRURL("repo-a"), prA2; got != want {
		t.Fatalf("repo-a top-layer PR URL = %q, want the layer-2 pull request %q", got, want)
	}
	if got, want := ff.TopStackLayerPRURL("repo-b"), prB1; got != want {
		t.Fatalf("repo-b top-layer PR URL = %q, want the layer-2 pull request %q", got, want)
	}
	if got := ff.Stack[0].Repos["repo-a"]; got.PRURL != prA1 || got.LastPushedSHA != bareRef(bareA, layer1Branch) {
		t.Fatalf("repo-a layer-1 entry = %+v, want PR %q at the remote tip", got, prA1)
	}
	if got := ff.Stack[1].Repos["repo-a"]; got.PRURL != prA2 || got.LastPushedSHA != bareRef(bareA, layer2Branch) {
		t.Fatalf("repo-a layer-2 entry = %+v, want PR %q at the remote tip", got, prA2)
	}
	if got := ff.Stack[0].Repos["repo-b"]; !got.NoCommits || got.PRURL != "" {
		t.Fatalf("repo-b layer-1 entry = %+v, want the no-commits marker and no pull request", got)
	}
	if got := ff.Stack[1].Repos["repo-b"]; got.PRURL != prB1 || got.LastPushedSHA != bareRef(bareB, layer2Branch) {
		t.Fatalf("repo-b layer-2 entry = %+v, want PR %q at the remote tip", got, prB1)
	}
	if got := bareRef(bareA, layer1Branch); got != refSHA(t, worktreeFor("repo-a"), layer1Branch) {
		t.Fatalf("repo-a remote layer-1 ref = %s, want the local tip %s", got, refSHA(t, worktreeFor("repo-a"), layer1Branch))
	}
	if got := bareRef(bareA, layer2Branch); got != refSHA(t, worktreeFor("repo-a"), layer2Branch) {
		t.Fatalf("repo-a remote layer-2 ref = %s, want the local tip %s", got, refSHA(t, worktreeFor("repo-a"), layer2Branch))
	}
	if got := bareRef(bareB, layer2Branch); got != refSHA(t, worktreeFor("repo-b"), layer2Branch) {
		t.Fatalf("repo-b remote layer-2 ref = %s, want the local tip %s", got, refSHA(t, worktreeFor("repo-b"), layer2Branch))
	}

	// -------------------------------------------------------------------------
	// Scenario 2: a Final Review fix relocated into layer 1, then republish.
	// -------------------------------------------------------------------------
	fixIterDir := filepath.Join(tmp, "fix-iteration")
	if err := os.MkdirAll(fixIterDir, 0o755); err != nil {
		t.Fatalf("mkdir fix iteration: %v", err)
	}
	manifest := "entries:\n  - layer: 1\n    repository: repo-a\n    paths:\n      - fix-layer1-a.txt\n"
	if err := os.WriteFile(filepath.Join(fixIterDir, "fix-manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write fix manifest: %v", err)
	}
	wtA, wtB := worktreeFor("repo-a"), worktreeFor("repo-b")
	for _, dirty := range []struct{ wt, name, content string }{
		{wtA, "fix-layer1-a.txt", "relocated fix\n"},
		{wtA, "fix-top-a.txt", "top fix\n"},
		{wtB, "fix-top-b.txt", "top fix\n"},
	} {
		if err := os.WriteFile(filepath.Join(dirty.wt, dirty.name), []byte(dirty.content), 0o644); err != nil {
			t.Fatalf("dirty %s: %v", dirty.name, err)
		}
	}
	if err := pr.RoundCommitHook(agent.RoundCommitInput{
		FeatureID:       f.ID,
		Iteration:       1,
		Kind:            agent.RoundCommitFinalReviewFix,
		FixNumber:       1,
		FixIterationDir: fixIterDir,
		Repos: map[string]string{
			"repo-a": wtA,
			"repo-b": wtB,
		},
	}); err != nil {
		t.Fatalf("final review fix round commit: %v", err)
	}

	// The relocation appends the fix to layer 1 and replays layer 2's
	// commits above it, so the remote layer-2 tip is no longer an ancestor
	// of the new local tip: the republish must deliver it through the
	// last-pushed lease (the remote holds exactly the recorded SHA).
	oldRemoteL2A := bareRef(bareA, layer2Branch)
	ff, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after relocation: %v", err)
	}
	if got := ff.Stack[1].Repos["repo-a"].LastPushedSHA; got != oldRemoteL2A {
		t.Fatalf("recorded layer-2 last-pushed SHA = %s, want the remote tip %s (the lease precondition)", got, oldRemoteL2A)
	}
	newLayer1A := refSHA(t, wtA, layer1Branch)
	newLayer2A := refSHA(t, wtA, layer2Branch)
	if newLayer1A == bareRef(bareA, layer1Branch) {
		t.Fatal("repo-a layer 1 ref did not move")
	}
	if newLayer2A == oldRemoteL2A {
		t.Fatal("repo-a layer 2 ref did not move")
	}
	if _, ok := tryRunGit(t, wtA, "merge-base", "--is-ancestor", oldRemoteL2A, newLayer2A); ok {
		t.Fatal("the new layer-2 tip fast-forwards the remote; the lease-push coverage is vacuous")
	}

	creationsBefore := map[string]int{"repo-a": pulls.CreatedCount("repo-a"), "repo-b": pulls.CreatedCount("repo-b")}
	requestsBefore := len(fake.Requests())

	if err := publishOrch.PublishWithOptions(f.ID, orchestrator.PublishOptions{}); err != nil {
		t.Fatalf("republish after relocation: %v", err)
	}

	if got := bareRef(bareA, layer1Branch); got != newLayer1A {
		t.Fatalf("repo-a remote layer-1 ref after republish = %s, want the relocated tip %s", got, newLayer1A)
	}
	if got := bareRef(bareA, layer2Branch); got != newLayer2A {
		t.Fatalf("repo-a remote layer-2 ref after republish = %s, want the new top %s", got, newLayer2A)
	}
	if got := bareRef(bareB, layer2Branch); got != refSHA(t, wtB, layer2Branch) {
		t.Fatalf("repo-b remote layer-2 ref after republish = %s, want the new top %s", got, refSHA(t, wtB, layer2Branch))
	}
	// No new pull request; the pass still consulted the open pull requests'
	// live state and bodies through the fake.
	for repo, before := range creationsBefore {
		if got := pulls.CreatedCount(repo); got != before {
			t.Fatalf("republish created pull requests for %s: %d -> %d, want none", repo, before, got)
		}
	}
	if len(fake.Requests()) <= requestsBefore {
		t.Fatal("republish made no GitHub API requests; want live state and body reads")
	}
	// The recorded last-pushed SHAs equal the remotes.
	ff, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("get after republish: %v", err)
	}
	if got := ff.Stack[0].Repos["repo-a"].LastPushedSHA; got != newLayer1A {
		t.Fatalf("repo-a layer-1 last-pushed SHA = %s, want the remote tip %s", got, newLayer1A)
	}
	if got := ff.Stack[1].Repos["repo-a"].LastPushedSHA; got != newLayer2A {
		t.Fatalf("repo-a layer-2 last-pushed SHA = %s, want the remote tip %s", got, newLayer2A)
	}
	if got := ff.Stack[1].Repos["repo-b"].LastPushedSHA; got != bareRef(bareB, layer2Branch) {
		t.Fatalf("repo-b layer-2 last-pushed SHA = %s, want the remote tip %s", got, bareRef(bareB, layer2Branch))
	}
	if ff.Status != feature.StatusPublished {
		t.Fatalf("status after republish = %v, want Published", ff.Status)
	}
}

// stackPublishBareOrigin creates a bare remote under remotesDir whose path
// parses as a git@localhost:acme scp-style remote and points repoPath's
// origin at it, with the base branch pushed and tracked.
func stackPublishBareOrigin(t *testing.T, remotesDir, repoPath, repoName string) string {
	t.Helper()
	remote := filepath.Join(remotesDir, "git@localhost:acme", repoName)
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatalf("mkdir remotes: %v", err)
	}
	runGit(t, remotesDir, "clone", "--quiet", "--bare", repoPath, remote)
	runGit(t, repoPath, "remote", "add", "origin", remote)
	testutil.SimulatePush(t, repoPath, remote, "main", "main")
	return remote
}

// stackPublishDescriptionRunner returns a PhaseRunner whose utility sessions
// answer with a body-only reply naming the layer title parsed from the
// description prompt, mirroring the e2e journeys' scripted bash sessions.
func stackPublishDescriptionRunner(t *testing.T, store *feature.Store, stateDir string) *agent.PhaseRunner {
	t.Helper()
	events := make(chan interface{}, 512)
	sm := session.NewManager(events)
	t.Cleanup(sm.Shutdown)
	scriptsDir := t.TempDir()

	pr := agent.NewPhaseRunner(sm, store, stateDir)
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		title := "the layer"
		if i := strings.Index(opts.Prompt, "Title: "); i >= 0 {
			rest := opts.Prompt[i+len("Title: "):]
			if j := strings.IndexByte(rest, '\n'); j >= 0 {
				title = rest[:j]
			}
		}
		script := testutil.WriteScript(t, scriptsDir, "description.sh", testutil.JSONLInit+"\n"+
			`read -r _agentic_init`+"\n"+
			testutil.JSONLAssistant("Scripted PR body for "+title+".")+"\n"+
			testutil.JSONLSuccess+"\n")
		return []string{"bash", script}, nil, &ports.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
		}, nil
	}
	return pr
}
