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
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestRefactorChildExecutionAndIntegrationJourney records the primary
// Child execution state-mutating journey, API-driven end to end:
//
//	Child 1 (manual publish): POST refactor → real-git setup → start →
//	scripted roadmap and phase-plan sessions behind the configured review
//	gates → stubbed implement and final-review kernels → explicit local
//	no-fast-forward integration → Completed closure → parent CodeReady →
//	disposable worktree/branch cleanup → parent stays CodeReady (manual
//	publish flow remains available).
//
//	Child 2 (auto publish): same lifecycle, after which closure hands
//	delivery back to the parent's current auto-publish configuration: the
//	integrated merge is pushed to the bare origin, PR creation fails at the
//	external-service seam, and the parent stays CodeReady with the child Completed —
//	never reopened.
func TestRefactorChildExecutionAndIntegrationJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	wtBaseDir := filepath.Join(tmp, "worktrees")

	repoDir := testutil.InitGitRepo(t)
	testutil.InitBareRemote(t, repoDir)
	journeyGit(t, repoDir, "checkout", "-b", "feature/journey-parent")
	writeJourneyFile(t, repoDir, "base.txt", "v1\n")
	journeyGit(t, repoDir, "add", "base.txt")
	journeyGit(t, repoDir, "commit", "-m", "parent base commit")
	journeyGit(t, repoDir, "push", "-u", "origin", "feature/journey-parent")

	store := feature.NewStore(stateDir)
	publishable := true
	parent := &feature.Feature{
		ID:            "journey-parent",
		Name:          "Journey parent",
		Slug:          "journey-parent",
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now().UTC().Truncate(time.Second),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{{
			Name:         "repoA",
			Path:         repoDir,
			WorktreePath: repoDir,
			Branch:       "feature/journey-parent",
			BaseBranch:   "main",
			Publishable:  &publishable,
		}},
		RepoStates: map[string]*feature.RepoState{"repoA": {Touched: true}},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}

	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, config.NewDefault())
	mgr.Worktrees = wm
	mgr.Cleanliness = journeyCleanliness(wm)

	serverEvents := make(chan interface{}, 512)
	stopForwarding := make(chan struct{})
	defer close(stopForwarding)

	sm := session.NewManager(serverEvents)
	t.Cleanup(sm.Shutdown)
	pr := journeyChildPhaseRunner(t, sm, store, stateDir)

	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Sessions:    sm,
		Publisher:   failingPRPublishAdapter{PublishAdapter: &git.PublishAdapter{}},
		Worktrees:   wm,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
		Cleanliness: journeyCleanliness(wm),
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})
	go func() {
		for {
			select {
			case ev := <-orch.Events():
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
		Mutations:             &journeyMutationTarget{mgr: mgr, orch: orch},
		DisableHostValidation: true,
	}))
	t.Cleanup(srv.Close)
	client, err := server.NewClient(server.ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	// ------------------------------------------------------------------
	// Child 1: manual publish arm.
	// ------------------------------------------------------------------
	child1ID := runRefactorChildLifecycle(t, store, srv.URL, client, parent.ID, true)

	// The explicit no-fast-forward boundary lives on the parent branch.
	parents := journeyGit(t, repoDir, "rev-list", "--parents", "-n", "1", "HEAD")
	if fields := len(strings.Fields(parents)); fields != 3 {
		t.Fatalf("parent merge parents = %q, want explicit two-parent merge commit", parents)
	}
	mergeHEAD := journeyGit(t, repoDir, "rev-parse", "HEAD")
	if _, err := os.Stat(filepath.Join(repoDir, "child-output.txt")); err != nil {
		t.Fatalf("child work missing from parent after integration: %v", err)
	}

	// Child detail exposes the closed result directly: outcome, timestamp,
	// merge HEAD, no cleanup warning; the parent moved to CodeReady and its
	// active-child projection cleared.
	child1Detail := getJourneyJSON(t, srv.URL+"/api/v1/features/"+child1ID)["feature"].(map[string]any)
	if child1Detail["close_outcome"] != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child1 detail close_outcome = %v, want completed", child1Detail["close_outcome"])
	}
	if child1Detail["closed_at"] == nil || child1Detail["closed_at"] == "" {
		t.Fatalf("child1 detail closed_at missing: %+v", child1Detail)
	}
	if active, ok := child1Detail["active"].(bool); ok && active {
		t.Fatalf("child1 detail active = %v, want false/absent (closed)", child1Detail["active"])
	}
	tx := child1Detail["transaction"].(map[string]any)
	entries := tx["entries"].([]any)
	entry := entries[0].(map[string]any)
	if entry["merge_head"] != mergeHEAD {
		t.Fatalf("child1 detail transaction.merge_head = %v, want parent tip %s", entry["merge_head"], mergeHEAD)
	}
	if entry["child_head_sha"] == "" || entry["parent_anchor_sha"] == "" || entry["parent_branch"] != "feature/journey-parent" {
		t.Fatalf("child1 transaction anchors = %+v, want recorded", entry)
	}
	if warning, _ := entry["cleanup_warning"].(string); warning != "" {
		t.Fatalf("child1 unexpected cleanup warning = %v", warning)
	}

	parentDetail := getJourneyJSON(t, srv.URL+"/api/v1/features/"+parent.ID)["feature"].(map[string]any)
	if parentDetail["status"] != feature.StatusCodeReady.String() {
		t.Fatalf("parent status = %v, want CodeReady (manual publish stays put)", parentDetail["status"])
	}
	if parentDetail["active_child"] != nil {
		t.Fatalf("parent active_child = %v, want cleared after closure", parentDetail["active_child"])
	}

	// Cleanup removed the disposable child worktree and ephemeral branch.
	child1, err := store.Load(child1ID)
	if err != nil {
		t.Fatalf("reload child1: %v", err)
	}
	if child1.Repos[0].WorktreePath != "" {
		t.Fatalf("child1 durable worktree path %q not cleared", child1.Repos[0].WorktreePath)
	}
	if branches := journeyGit(t, repoDir, "branch", "--list", child1.Repos[0].Branch); branches != "" {
		t.Fatalf("child1 branch %s still present after cleanup", child1.Repos[0].Branch)
	}
	list := getJourneyJSON(t, srv.URL+"/api/v1/features")
	if summaries := list["features"].([]any); len(summaries) != 1 || summaries[0].(map[string]any)["id"] != parent.ID {
		t.Fatalf("top-level list = %+v, want only the parent", summaries)
	}

	// ------------------------------------------------------------------
	// Child 2: auto publish arm. The parent's current Review configuration
	// (updated atomically with the launch) selects auto-publish this time.
	// Push succeeds against the bare origin; deterministic PR creation failure
	// keeps the parent CodeReady with the child Completed.
	// ------------------------------------------------------------------
	child2ID := runRefactorChildLifecycle(t, store, srv.URL, client, parent.ID, false)
	// Closure hands delivery back to the parent's auto-publish config; wait
	// for the terminal publication outcome (PR URL or a recorded error)
	// before asserting remote state.
	waitForParentPublishOutcome(t, store, parent.ID)

	child2Detail := getJourneyJSON(t, srv.URL+"/api/v1/features/"+child2ID)["feature"].(map[string]any)
	if child2Detail["close_outcome"] != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child2 detail close_outcome = %v, want completed", child2Detail["close_outcome"])
	}
	parentDetail = getJourneyJSON(t, srv.URL+"/api/v1/features/"+parent.ID)["feature"].(map[string]any)
	if parentDetail["status"] != feature.StatusCodeReady.String() {
		t.Fatalf("parent status = %v, want CodeReady after failed PR creation", parentDetail["status"])
	}
	// The push half of publication delivered the merge to origin.
	child2, err := store.Load(child2ID)
	if err != nil {
		t.Fatalf("reload child2: %v", err)
	}
	parentState, err := store.Load(parent.ID)
	if err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	originTip := journeyGit(t, repoDir, "ls-remote", "origin", "feature/journey-parent")
	if !strings.Contains(originTip, child2.Parent.Transaction.Entries[0].MergeHEAD) {
		t.Fatalf("origin feature/journey-parent = %q, want pushed merge head %s (parent status %s, checkpoints %+v, repo state %+v)",
			originTip, child2.Parent.Transaction.Entries[0].MergeHEAD, parentState.Status, parentState.Checkpoints, parentState.RepoStates["repoA"])
	}
	parent2, err := store.Load(parent.ID)
	if err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	if parent2.RepoStates["repoA"].LastError == "" {
		t.Fatalf("parent repo publish error not recorded after failed PR creation: %+v", parent2.RepoStates["repoA"])
	}
}

// failingPRPublishAdapter keeps the real local Git publish operations while
// replacing only the external GitHub PR call with the failure this journey
// exercises.
type failingPRPublishAdapter struct {
	*git.PublishAdapter
}

func (failingPRPublishAdapter) CreatePR(string, string, string, string, string, bool) (string, error) {
	return "", fmt.Errorf("scripted PR creation failure")
}

// runRefactorChildLifecycle launches one child through the API and drives it
// through setup, the two configured review gates, scripted plan sessions,
// stubbed implement/final-review kernels, and integration, returning the
// child id once the relationship is durably Completed.
func runRefactorChildLifecycle(t *testing.T, store *feature.Store, baseURL string, client *server.Client, parentID string, manualPublish bool) string {
	t.Helper()

	resp, err := client.RefactorFeature(t.Context(), parentID, server.RefactorFeatureRequest{
		Name:     fmt.Sprintf("Rework pass (manual=%v)", manualPublish),
		Pipeline: feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{
			RoadmapReview:   true,
			PhasePlanReview: true,
			ManualPublish:   manualPublish,
		},
	})
	if err != nil {
		t.Fatalf("RefactorFeature() error = %v", err)
	}
	childID := resp.FeatureID
	if childID == "" {
		t.Fatalf("refactor response = %+v; want child id", resp)
	}

	childBody := waitForJourneySetupComplete(t, baseURL, childID)
	if childBody["status"] != "Created" {
		t.Fatalf("child durable status = %v, want Created (parked after setup)", childBody["status"])
	}

	postAction(t, baseURL, childID, "start", `{}`)

	// Gate 1: roadmap review (roadmap phase count 0), then Gate 2: the
	// phase-plan review, then the terminal integration completion.
	waitForJourneyGate(t, baseURL, childID, 0)
	postReviewSessionProceed(t, baseURL, childID)
	waitForJourneyGate(t, baseURL, childID, 1)
	postReviewSessionProceed(t, baseURL, childID)
	waitForJourneyChildClosed(t, baseURL, store, childID)
	return childID
}
