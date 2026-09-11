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

package e2e

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestStackPublishJourney publishes a two-layer stack through the REST
// surface with scripted description sessions, covering both delivery arms:
//
//	Manual arm: the feature completes the final phase into CodeReady, the
//	POST actions/publish request delivers the stack (one pull request per
//	layer, layer 2 based on layer 1's branch), and the detail read reports
//	published with the repository's pull request at the top layer.
//
//	Auto arm: the same fixture without the manual-publish checkpoint
//	completes the final phase and the roadmap-final tail publishes the stack
//	by itself; the journey waits for the published status.
func TestStackPublishJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	wtBaseDir := filepath.Join(tmp, "worktrees")
	remotesDir := filepath.Join(tmp, "remotes")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	// The bare origin's directory name parses as a git@localhost:acme
	// scp-style remote, so the production origin resolution reads
	// host/owner/repo identity from a remote that never leaves the test
	// machine while pull requests are served by the in-test GitHub fake.
	repoA := testutil.InitGitRepo(t)
	bareA := stackJourneyBareOrigin(t, remotesDir, repoA, "repo-a")

	fake := testutil.InstallFakeGitHubAPI(t)
	pulls := testutil.NewFakePullStore("acme")
	pulls.Install(t, fake, "repo-a")

	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: repoA}

	store := feature.NewStore(stateDir)
	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = wm

	serverEvents := make(chan interface{}, 512)
	sm := session.NewManager(serverEvents)
	t.Cleanup(sm.Shutdown)
	pr := stackJourneyDescriptionRunner(t, sm, store, stateDir)

	// The lifecycle orchestrator drives approval and the mid-flight phases
	// without a PhaseRunner so no plan or implement session dispatches; the
	// publish orchestrator carries the scripted description PhaseRunner and
	// owns each feature's final-phase completion, the auto-publish tail,
	// and the REST mutation target's publish action.
	lifecycleOrch := orchestrator.New(orchestrator.Deps{
		Lifecycle: mgr,
		Store:     store,
		Worktrees: wm,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = lifecycleOrch.Shutdown()
		lifecycleOrch.WaitForCycles()
	})
	publishOrch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Worktrees:   wm,
		Sessions:    sm,
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

	stopForwarding := make(chan struct{})
	defer close(stopForwarding)
	go func() {
		for {
			select {
			case ev := <-publishOrch.Events():
				select {
				case serverEvents <- ev:
				default:
				}
			case <-stopForwarding:
				return
			}
		}
	}()

	srv := httptest.NewServer(server.NewHandler(server.HandlerOptions{
		Runtime:               server.RuntimeIdentity{RuntimeDir: tmp, StateDir: stateDir},
		Features:              store,
		FeatureStore:          store,
		Events:                serverEvents,
		Mutations:             &journeyMutationTarget{mgr: mgr, orch: publishOrch},
		DisableHostValidation: true,
	}))
	t.Cleanup(srv.Close)

	// driveToFinalBoundary creates one feature, approves its roadmap, and
	// drives the three phases across the two layer boundaries. manual
	// selects the manual-publish checkpoint; the final phase's completion
	// runs on the publish orchestrator so Final Review and the publish tail
	// share its PhaseRunner.
	driveToFinalBoundary := func(name string, manual bool) *feature.Feature {
		t.Helper()
		f, err := mgr.Create(name, "publishes a two-layer stack", []string{"repo-a"},
			cfg.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := mgr.RunSetup(f.ID); err != nil {
			t.Fatalf("run setup %s: %v", name, err)
		}
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
		if err := lifecycleOrch.HandleReviewDecision(f.ID, orchestrator.ReviewDecision{
			Decision: "proceed",
			Roadmap:  true,
		}); err != nil {
			t.Fatalf("HandleReviewDecision: %v", err)
		}

		worktreeFor := func() string {
			t.Helper()
			ff, err := mgr.Get(f.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			return ff.Repos[0].WorktreePath
		}
		startImplementing := func(phase int) {
			t.Helper()
			if err := store.Modify(f.ID, func(ff *feature.Feature) error {
				ff.Status = feature.StatusImplementing
				ff.CurrentPhase = feature.PhaseImplement
				ff.CurrentRoadmapPhase = phase
				ff.Checkpoints.ManualPublish = manual
				if ff.RepoStates == nil {
					ff.RepoStates = map[string]*feature.RepoState{}
				}
				state, ok := ff.RepoStates["repo-a"]
				if !ok {
					state = &feature.RepoState{}
					ff.RepoStates["repo-a"] = state
				}
				state.Touched = true
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

		startImplementing(1)
		testutil.CommitFile(t, worktreeFor(), "phase-1.txt", "layer 1 work\n", "phase 1")
		completePhaseOn(lifecycleOrch)
		startImplementing(2)
		testutil.CommitFile(t, worktreeFor(), "phase-2.txt", "more layer 1 work\n", "phase 2")
		completePhaseOn(lifecycleOrch)
		startImplementing(3)
		testutil.CommitFile(t, worktreeFor(), "phase-3.txt", "layer 2 work\n", "phase 3")
		completePhaseOn(publishOrch)
		return f
	}

	// assertStackPublished pins the delivered shape for one feature: two
	// pull requests in ascending layer order with the table titles and
	// bases, stack sections naming the top layer as current, remote branches
	// at the recorded tips, and the published status with the legacy
	// repository URL at the top layer.
	assertStackPublished := func(f *feature.Feature) {
		t.Helper()
		workspaceSlug := feature.WorkspaceSlug(f.Slug, f.ID)
		layer1Branch := git.LayerBranchName(workspaceSlug, 1, "bootstrap")
		layer2Branch := git.LayerBranchName(workspaceSlug, 2, "build-and-polish")

		ff, err := mgr.Get(f.ID)
		if err != nil {
			t.Fatalf("get %s: %v", f.ID, err)
		}
		if ff.Status != feature.StatusPublished {
			t.Fatalf("%s status = %v, want Published", f.Name, ff.Status)
		}
		pr1 := ff.Stack[0].Repos["repo-a"].PRURL
		pr2 := ff.Stack[1].Repos["repo-a"].PRURL
		if pr1 == "" || pr2 == "" {
			t.Fatalf("%s stack pull requests = %q/%q, want both layers published", f.Name, pr1, pr2)
		}
		if got := ff.RepoStates["repo-a"].PRURL; got != pr2 {
			t.Fatalf("%s legacy repository URL = %q, want the layer-2 pull request %q", f.Name, got, pr2)
		}

		record1, ok := pulls.Pull("repo-a", journeyPRNumber(t, pr1))
		if !ok {
			t.Fatalf("%s layer-1 pull request record missing (%s)", f.Name, pr1)
		}
		record2, _ := pulls.Pull("repo-a", journeyPRNumber(t, pr2))
		if record1.Title != "Bootstrap" || record1.Head != layer1Branch || record1.Base != "main" {
			t.Fatalf("%s layer-1 pull request = %+v, want Bootstrap on %s based on main", f.Name, record1, layer1Branch)
		}
		if record2.Title != "Build and polish" || record2.Head != layer2Branch || record2.Base != layer1Branch {
			t.Fatalf("%s layer-2 pull request = %+v, want Build and polish on %s based on %s", f.Name, record2, layer2Branch, layer1Branch)
		}
		if !strings.Contains(record1.Body, "Scripted PR body for Bootstrap.") {
			t.Fatalf("%s layer-1 body is not the scripted session body:\n%s", f.Name, record1.Body)
		}
		wantTop := fmt.Sprintf("2. Build and polish - [#%d](%s) (open) (this pull request)", journeyPRNumber(t, pr2), pr2)
		for _, record := range []testutil.FakePullRequest{record1, record2} {
			if !strings.Contains(record.Body, "## Stack") || !strings.Contains(record.Body, wantTop) {
				t.Fatalf("%s body lacks the stack section naming the top layer:\n%s", f.Name, record.Body)
			}
		}

		bareRef := func(branch string) string {
			t.Helper()
			return journeyGit(t, bareA, "rev-parse", "refs/heads/"+branch)
		}
		if got := ff.Stack[0].Repos["repo-a"].LastPushedSHA; got != bareRef(layer1Branch) {
			t.Fatalf("%s layer-1 last-pushed SHA = %s, want the remote tip %s", f.Name, got, bareRef(layer1Branch))
		}
		if got := ff.Stack[1].Repos["repo-a"].LastPushedSHA; got != bareRef(layer2Branch) {
			t.Fatalf("%s layer-2 last-pushed SHA = %s, want the remote tip %s", f.Name, got, bareRef(layer2Branch))
		}
	}

	// ------------------------------------------------------------------
	// Manual arm: CodeReady, then the REST publish action.
	// ------------------------------------------------------------------
	manualFeature := driveToFinalBoundary("Stack Publish Manual", true)

	manualDetail := getJourneyJSON(t, srv.URL+"/api/v1/features/"+manualFeature.ID)["feature"].(map[string]any)
	if manualDetail["status"] != feature.StatusCodeReady.String() {
		t.Fatalf("manual feature status before publish = %v, want code_ready", manualDetail["status"])
	}
	sourceRevision := ""
	if meta, ok := getJourneyJSON(t, srv.URL+"/api/v1/features/"+manualFeature.ID)["meta"].(map[string]any); ok {
		if rev, ok := meta["revision"].(string); ok {
			sourceRevision = rev
		}
	}
	body := fmt.Sprintf(`{"source_revision":%q,"repos":["repo-a"]}`, sourceRevision)
	status, raw := postActionStatus(t, srv.URL, manualFeature.ID, "publish", body)
	if status != 200 || !strings.Contains(string(raw), `"published"`) {
		t.Fatalf("publish action status = %d body = %s, want 200 published", status, raw)
	}
	assertStackPublished(manualFeature)

	// ------------------------------------------------------------------
	// Auto arm: the roadmap-final tail publishes without any action.
	// ------------------------------------------------------------------
	autoFeature := driveToFinalBoundary("Stack Publish Auto", false)
	deadline := time.Now().Add(60 * time.Second)
	for {
		ff, err := mgr.Get(autoFeature.ID)
		if err != nil {
			t.Fatalf("get auto feature: %v", err)
		}
		if ff.Status == feature.StatusPublished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("auto feature status = %v, want Published before the deadline", ff.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
	assertStackPublished(autoFeature)

	// The two features published independent stacks: four pull requests
	// over the same repository, two per feature.
	if got := pulls.CreatedCount("repo-a"); got != 4 {
		t.Fatalf("repo-a created pull requests = %d, want 4 (two per feature)", got)
	}
}

// stackJourneyBareOrigin creates a bare remote whose path parses as a
// git@localhost:acme scp-style remote and points repoPath's origin at it,
// with the base branch pushed and tracked.
func stackJourneyBareOrigin(t *testing.T, remotesDir, repoPath, repoName string) string {
	t.Helper()
	remote := filepath.Join(remotesDir, "git@localhost:acme", repoName)
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatalf("mkdir remotes: %v", err)
	}
	journeyGit(t, remotesDir, "clone", "--quiet", "--bare", repoPath, remote)
	journeyGit(t, repoPath, "remote", "add", "origin", remote)
	testutil.SimulatePush(t, repoPath, remote, "main", "main")
	return remote
}

// stackJourneyDescriptionRunner returns a PhaseRunner whose utility sessions
// answer with a body-only reply naming the layer title parsed from the
// description prompt.
func stackJourneyDescriptionRunner(t *testing.T, sm *session.Manager, store *feature.Store, stateDir string) *agent.PhaseRunner {
	t.Helper()
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

// journeyPRNumber extracts the pull-request number from a PR URL.
func journeyPRNumber(t *testing.T, prURL string) int {
	t.Helper()
	fields := strings.Split(strings.TrimRight(prURL, "/"), "/")
	number, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		t.Fatalf("pull request URL %q carries no number: %v", prURL, err)
	}
	return number
}
