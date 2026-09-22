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
	"bufio"
	"fmt"
	"io"
	"net/http/httptest"
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
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// stackReadPreflightTarget adapts one orchestrator to the server's mutation
// target for the completion preflight endpoint. Every other method stays on
// the embedded nil interface: this test never calls them.
type stackReadPreflightTarget struct {
	serverruntime.MutationTarget
	orch *orchestrator.Orchestrator
}

func (t *stackReadPreflightTarget) CompletionPreflight(featureID string) (serverruntime.CompletionPreflightResponse, error) {
	if t.orch == nil {
		return serverruntime.CompletionPreflightResponse{FeatureID: featureID}, fmt.Errorf("orchestrator is not available")
	}
	result, err := t.orch.CompletionPreflight(featureID)
	if err != nil {
		return serverruntime.CompletionPreflightResponse{FeatureID: featureID}, err
	}
	resp := serverruntime.CompletionPreflightResponse{
		APIVersion:      serverruntime.APIVersion,
		FeatureID:       result.FeatureID,
		SourceRevision:  result.SourceRevision,
		CanMarkDone:     result.CanMarkDone,
		MarkDoneBlocker: result.MarkDoneBlocker,
	}
	for _, r := range result.Repos {
		repo := serverruntime.CompletionPreflightRepo{
			Repo:                  r.Repo,
			Publishable:           r.Publishable,
			Touched:               r.Touched,
			Status:                r.Status,
			Blocker:               r.Blocker,
			Freshness:             r.Freshness,
			Error:                 serverruntime.WireRepoError(r.Error),
			BaseBranch:            r.BaseBranch,
			Branch:                r.Branch,
			PendingCommits:        r.PendingCommits,
			PendingDirty:          r.PendingDirty,
			PushMode:              serverruntime.CompletionPreflightRepoPushMode(r.PushMode),
			PendingDirtyFiles:     r.PendingDirtyFiles,
			PendingDirtyFileTotal: r.PendingDirtyFileTotal,
		}
		for _, entry := range r.PullRequests {
			repo.PullRequests = append(repo.PullRequests, serverruntime.PullRequestEntry{
				Position:       entry.Position,
				Title:          entry.Title,
				Branch:         entry.Branch,
				URL:            entry.URL,
				State:          serverruntime.PullRequestEntryState(entry.State),
				NoCommits:      entry.NoCommits,
				PushedUpToDate: entry.PushedUpToDate,
				PushMode:       serverruntime.PullRequestEntryPushMode(entry.PushMode),
			})
		}
		resp.Repos = append(resp.Repos, repo)
	}
	return resp, nil
}

// TestStackReadModelSSE publishes the two-repository, two-layer fixture
// against bare remotes and the fake GitHub API, then pins the read model:
// the feature detail's per-repository pull_requests entries carry the
// recorded URLs and open states (repo-b's empty layer 1 is marked
// no_commits), the completion preflight reports every per-layer push mode
// none with status already_published, and the publish pass's per-layer
// repository-status updates reach SSE subscribers as lifecycle updates.
func TestStackReadModelSSE(t *testing.T) {
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
	stackPublishBareOrigin(t, remotesDir, repoA, "repo-a")
	repoB := testutil.InitGitRepo(t)
	stackPublishBareOrigin(t, remotesDir, repoB, "repo-b")

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

	f, err := mgr.Create("Stack Read Model SSE", "exposes the stack read model",
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

	// repo-a carries commits in both layers; repo-b only in layer 2, so its
	// layer-1 entry is the single-PR / no-commits shape.
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

	// Stand up the REST+SSE handler against the same store, fan the publish
	// orchestrator's domain events out to both the handler and this test's
	// collector, and subscribe before publishing so the per-layer
	// repository-status updates flow to the subscriber.
	orchEvents := publishOrch.Events()
	domainTap := make(chan ports.Event, 256)
	handlerDomain := make(chan ports.Event, 256)
	go func() {
		for ev := range orchEvents {
			domainTap <- ev
			handlerDomain <- ev
		}
		close(domainTap)
		close(handlerDomain)
	}()
	srv := httptest.NewServer(serverruntime.NewHandler(serverruntime.HandlerOptions{
		Runtime:      serverruntime.RuntimeIdentity{RuntimeDir: t.TempDir(), StateDir: store.BaseDir},
		Features:     store,
		FeatureStore: store,
		DomainEvents: handlerDomain,
		Mutations:    &stackReadPreflightTarget{orch: publishOrch},
	}))
	t.Cleanup(srv.Close)

	resp, _ := openIntegrationSSE(t, srv)
	defer resp.Body.Close()

	if err := publishOrch.PublishWithOptions(f.ID, orchestrator.PublishOptions{}); err != nil {
		t.Fatalf("PublishWithOptions: %v", err)
	}

	prA1 := pulls.URL("repo-a", 1)
	prA2 := pulls.URL("repo-a", 2)
	prB1 := pulls.URL("repo-b", 1)

	// The publish pass emits one repository-status event per changed layer,
	// each carrying the layer's position: repo-a layers 1 and 2, repo-b
	// layers 1 (the no-commits marking) and 2.
	layerEvents := map[string][]int{}
	for done := false; !done; {
		select {
		case ev, ok := <-domainTap:
			if !ok {
				done = true
				break
			}
			if ev.Type == ports.RepoStatusChanged && ev.LayerPosition > 0 {
				layerEvents[ev.RepoName] = append(layerEvents[ev.RepoName], ev.LayerPosition)
			}
		case <-time.After(2 * time.Second):
			done = true
		}
	}
	if got := layerEvents["repo-a"]; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("repo-a layer-scoped repository-status events = %v, want positions [1 2]", got)
	}
	if got := layerEvents["repo-b"]; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("repo-b layer-scoped repository-status events = %v, want positions [1 2]", got)
	}

	// The same updates reach SSE subscribers as lifecycle updates.
	lifecycleBlocks := drainIntegrationSSELifecycle(t, resp.Body, 4, 5*time.Second)
	if lifecycleBlocks < 4 {
		t.Fatalf("SSE lifecycle.updated blocks during publish = %d, want at least the four repository-status updates", lifecycleBlocks)
	}

	// Feature detail: per-repository pull_requests entries in position
	// order, recorded URLs, open states, and no pr_url key anywhere.
	detail := getIntegrationJSON(t, srv.URL+"/api/v1/features/"+f.ID)
	repoStatus := detail["feature"].(map[string]any)["repo_status"].([]any)
	byRepo := map[string]map[string]any{}
	for _, raw := range repoStatus {
		dto := raw.(map[string]any)
		byRepo[dto["name"].(string)] = dto
	}
	if len(byRepo) != 2 {
		t.Fatalf("repo_status = %+v, want both repositories", repoStatus)
	}
	assertStackEntries(t, "repo-a", byRepo["repo-a"], []stackEntryWant{
		{Position: 1, Title: "Bootstrap", Branch: layer1Branch, URL: prA1, State: "open", NoCommits: false, PushedUpToDate: true},
		{Position: 2, Title: "Build and polish", Branch: layer2Branch, URL: prA2, State: "open", NoCommits: false, PushedUpToDate: true},
	})
	assertStackEntries(t, "repo-b", byRepo["repo-b"], []stackEntryWant{
		{Position: 1, Title: "Bootstrap", Branch: layer1Branch, URL: "", State: "none", NoCommits: true, PushedUpToDate: false},
		{Position: 2, Title: "Build and polish", Branch: layer2Branch, URL: prB1, State: "open", NoCommits: false, PushedUpToDate: true},
	})
	for name, dto := range byRepo {
		if _, ok := dto["pr_url"]; ok {
			t.Fatalf("repo_status[%s] still carries pr_url: %+v", name, dto)
		}
	}

	// Completion preflight over HTTP: the entries come back with every
	// per-layer push mode none and both repositories already published.
	preflight := getIntegrationJSON(t, srv.URL+"/api/v1/features/"+f.ID+"/completion/preflight")
	preflightRepos := preflight["repos"].([]any)
	if len(preflightRepos) != 2 {
		t.Fatalf("preflight repos = %+v, want both repositories", preflightRepos)
	}
	for _, raw := range preflightRepos {
		repo := raw.(map[string]any)
		name := repo["repo"].(string)
		if repo["status"] != "already_published" {
			t.Fatalf("preflight %s status = %v, want already_published", name, repo["status"])
		}
		entries := repo["pull_requests"].([]any)
		if len(entries) != 2 {
			t.Fatalf("preflight %s pull_requests = %+v, want one entry per layer", name, entries)
		}
		for _, entryRaw := range entries {
			entry := entryRaw.(map[string]any)
			if entry["push_mode"] != "none" {
				t.Fatalf("preflight %s layer %v push mode = %v, want none", name, entry["position"], entry["push_mode"])
			}
		}
		if repo["push_mode"] != "fast_forward" {
			t.Fatalf("preflight %s repository push mode = %v, want the fast_forward aggregate", name, repo["push_mode"])
		}
	}
}

type stackEntryWant struct {
	Position       int
	Title          string
	Branch         string
	URL            string
	State          string
	NoCommits      bool
	PushedUpToDate bool
}

func assertStackEntries(t *testing.T, repoName string, dto map[string]any, want []stackEntryWant) {
	t.Helper()
	entries, ok := dto["pull_requests"].([]any)
	if !ok || len(entries) != len(want) {
		t.Fatalf("repo_status[%s] pull_requests = %+v, want %d entries", repoName, dto["pull_requests"], len(want))
	}
	for i, wantEntry := range want {
		entry, ok := entries[i].(map[string]any)
		if !ok {
			t.Fatalf("repo_status[%s] pull_requests[%d] = %+v, want an object", repoName, i, entries[i])
		}
		if entry["position"] != float64(wantEntry.Position) {
			t.Fatalf("repo_status[%s] pull_requests[%d] position = %v, want %d", repoName, i, entry["position"], wantEntry.Position)
		}
		if entry["title"] != wantEntry.Title {
			t.Fatalf("repo_status[%s] pull_requests[%d] title = %v, want %q", repoName, i, entry["title"], wantEntry.Title)
		}
		if entry["branch"] != wantEntry.Branch {
			t.Fatalf("repo_status[%s] pull_requests[%d] branch = %v, want %q", repoName, i, entry["branch"], wantEntry.Branch)
		}
		if entry["state"] != wantEntry.State {
			t.Fatalf("repo_status[%s] pull_requests[%d] state = %v, want %q", repoName, i, entry["state"], wantEntry.State)
		}
		if entry["no_commits"] != wantEntry.NoCommits {
			t.Fatalf("repo_status[%s] pull_requests[%d] no_commits = %v, want %v", repoName, i, entry["no_commits"], wantEntry.NoCommits)
		}
		if entry["pushed_up_to_date"] != wantEntry.PushedUpToDate {
			t.Fatalf("repo_status[%s] pull_requests[%d] pushed_up_to_date = %v, want %v", repoName, i, entry["pushed_up_to_date"], wantEntry.PushedUpToDate)
		}
		gotURL, hasURL := entry["url"]
		if wantEntry.URL == "" {
			if hasURL {
				t.Fatalf("repo_status[%s] pull_requests[%d] url = %v, want omitted", repoName, i, gotURL)
			}
			continue
		}
		if !hasURL || gotURL != wantEntry.URL {
			t.Fatalf("repo_status[%s] pull_requests[%d] url = %v, want %q", repoName, i, gotURL, wantEntry.URL)
		}
	}
}

// drainIntegrationSSELifecycle reads SSE blocks in the background until
// either min lifecycle.updated blocks were seen or the window closes with
// no further updates, returning how many were seen. Heartbeat and other
// frames are skipped.
func drainIntegrationSSELifecycle(t *testing.T, body io.ReadCloser, min int, window time.Duration) int {
	t.Helper()
	blocks := make(chan string, 256)
	go func() {
		defer close(blocks)
		r := bufio.NewReader(body)
		var lines []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if len(lines) > 0 {
					blocks <- strings.Join(lines, "\n")
					lines = nil
				}
				continue
			}
			lines = append(lines, line)
		}
	}()
	deadline := time.Now().Add(window)
	seen := 0
	for seen < min {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return seen
		}
		select {
		case block, ok := <-blocks:
			if !ok {
				return seen
			}
			if strings.Contains(block, "event: lifecycle.updated") {
				seen++
			}
		case <-time.After(remaining):
			return seen
		}
	}
	return seen
}
