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
	"encoding/json"
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

// TestReviewFeedbackChildTracerJourney proves the backend tracer from a
// multi-repository published parent through typed fetch and launch, selected
// feedback planning input, normal Medium execution, transactional integration,
// and the closed relationship projection.
func TestReviewFeedbackChildTracerJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(state) error = %v", err)
	}
	wtBaseDir := filepath.Join(tmp, "worktrees")

	repoA := reviewFeedbackJourneyRepo(t, "feature/review-parent", "api base\n")
	repoB := reviewFeedbackJourneyRepo(t, "feature/review-parent", "web base\n")
	docsRepo := reviewFeedbackJourneyRepo(t, "feature/review-parent", "docs base\n")
	baseA := journeyGit(t, repoA, "rev-parse", "HEAD")
	baseB := journeyGit(t, repoB, "rev-parse", "HEAD")

	// Add bare push remotes for repoA and repoB so the tail can push.
	remoteA := reviewFeedbackJourneyBareRemote(t, repoA, "feature/review-parent")
	remoteB := reviewFeedbackJourneyBareRemote(t, repoB, "feature/review-parent")

	store := feature.NewStore(stateDir)
	publishable := true
	parent := &feature.Feature{
		ID:            "review-feedback-parent",
		Name:          "Review feedback parent",
		Slug:          "review-feedback-parent",
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now().UTC().Truncate(time.Second),
		Pipeline:      feature.PipelineMoonshot,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repoA", Path: repoA, WorktreePath: repoA, Branch: "feature/review-parent", BaseBranch: "main", Publishable: &publishable},
			{Name: "docs", Path: docsRepo, WorktreePath: docsRepo, Branch: "feature/review-parent", BaseBranch: "main", Publishable: &publishable},
			{Name: "repoB", Path: repoB, WorktreePath: repoB, Branch: "feature/review-parent", BaseBranch: "main", Publishable: &publishable},
		},
		RepoStates: map[string]*feature.RepoState{
			"repoA": {Touched: true, PRURL: "https://github.com/example/api/pull/1"},
			"docs":  {},
			"repoB": {Touched: true, PRURL: "https://github.com/example/web/pull/2"},
		},
		Models:       config.ModelConfig{Planning: "planning-model", Implementation: "implementation-model", Review: "review-model"},
		Effort:       config.EffortConfig{Planning: "high", Implementation: "medium", Review: "low"},
		RiskLevel:    feature.RiskHigh,
		ExitCriteria: "selected review feedback is addressed",
		Inquireness:  feature.InquirenessHigh,
		Checkpoints:  feature.Checkpoints{RoadmapReview: true, ManualPublish: true},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("Store.Save(parent) error = %v", err)
	}

	fakeGH := testutil.InstallFakeGH(t, testutil.FakeGHConfig{Behavior: reviewFeedbackJourneyFakeGHBehavior})
	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, config.NewDefault())
	mgr.Worktrees = wm

	serverEvents := make(chan interface{}, 512)
	stopForwarding := make(chan struct{})
	defer close(stopForwarding)
	sm := session.NewManager(serverEvents)
	t.Cleanup(sm.Shutdown)
	phaseRunner := journeyChildPhaseRunner(t, sm, store, stateDir)
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Sessions:    sm,
		Worktrees:   wm,
		PhaseRunner: phaseRunner,
		CmdRunner:   phaseRunner.CommandRunner,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})
	go func() {
		for {
			select {
			case event := <-orch.Events():
				select {
				case serverEvents <- event:
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
		t.Fatalf("server.NewClient() error = %v", err)
	}

	fetched, err := client.FetchReviewFeedback(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("FetchReviewFeedback() error = %v", err)
	}
	if len(fetched.Repos) != 2 || fetched.Repos[0].Repo != "repoA" || fetched.Repos[1].Repo != "repoB" {
		t.Fatalf("FetchReviewFeedback().Repos = %+v, want repoA then repoB with docs skipped", fetched.Repos)
	}
	if fetched.Repos[0].PrURL != parent.RepoStates["repoA"].PRURL || fetched.Repos[1].PrURL != parent.RepoStates["repoB"].PRURL {
		t.Errorf("fetch PR URLs = %q/%q, want %q/%q", fetched.Repos[0].PrURL, fetched.Repos[1].PrURL, parent.RepoStates["repoA"].PRURL, parent.RepoStates["repoB"].PRURL)
	}
	if got := fetched.Repos[0].Comments; len(got) != 3 || got[0].ID != 11 || got[0].Type != "review" || got[0].Repo != "repoA" ||
		got[1].ID != 12 || got[1].Type != "issue" || got[1].Repo != "repoA" ||
		got[2].ID != 13 || got[2].Type != "review_body" || got[2].Repo != "repoA" {
		t.Fatalf("repoA comments = %+v, want chronologically grouped/tagged review, issue, review body", got)
	}
	if got := fetched.Repos[1].Comments; len(got) != 1 || got[0].ID != 21 || got[0].Type != "review" || got[0].Repo != "repoB" {
		t.Fatalf("repoB comments = %+v, want tagged inline comment 21", got)
	}

	selected := []feature.ReviewFeedbackComment{fetched.Repos[0].Comments[0], fetched.Repos[1].Comments[0]}
	gate := true
	fakeGH.Clear(t)
	launched, err := client.ReviewFeedbackFeature(t.Context(), parent.ID, server.ReviewFeedbackFeatureRequest{
		Comments: selected,
		Gate:     &gate,
	})
	if err != nil {
		t.Fatalf("ReviewFeedbackFeature() error = %v", err)
	}
	if launched.FeatureID == "" || launched.ParentID != parent.ID {
		t.Fatalf("ReviewFeedbackFeature() = %+v, want child reference for parent %s", launched, parent.ID)
	}
	childID := launched.FeatureID
	child, err := store.Load(childID)
	if err != nil {
		t.Fatalf("Store.Load(child) error = %v", err)
	}
	if child.Parent == nil || child.Parent.ParentID != parent.ID || child.Parent.Kind != feature.ChildKindReviewFeedback {
		t.Fatalf("child relationship = %+v, want review-feedback child of %s", child.Parent, parent.ID)
	}
	if child.Pipeline != feature.PipelineMedium {
		t.Errorf("child pipeline = %q, want %q", child.Pipeline, feature.PipelineMedium)
	}
	if child.Models != parent.Models || child.Effort != parent.Effort || child.RiskLevel != parent.RiskLevel ||
		child.Inquireness != parent.Inquireness {
		t.Errorf("child inherited config = models:%+v effort:%+v risk:%q exit:%q inquireness:%q", child.Models, child.Effort, child.RiskLevel, child.ExitCriteria, child.Inquireness)
	}
	if child.ExitCriteria == parent.ExitCriteria {
		t.Errorf("child ExitCriteria inherited parent's; want deterministic review-feedback outcomes contract")
	}
	if !strings.Contains(child.ExitCriteria, feature.ReviewFeedbackOutcomesFilename) {
		t.Errorf("child ExitCriteria = %q, want outcomes file path %q", child.ExitCriteria, feature.ReviewFeedbackOutcomesFilename)
	}
	if !child.Checkpoints.RoadmapReview || !child.Checkpoints.PhasePlanReview {
		t.Errorf("child checkpoints = %+v, want explicit coupled gate enabled", child.Checkpoints)
	}
	if child.BaseSHA("repoA") != baseA || child.BaseSHA("repoB") != baseB {
		t.Errorf("child bases = repoA:%q repoB:%q, want %q/%q", child.BaseSHA("repoA"), child.BaseSHA("repoB"), baseA, baseB)
	}
	if len(child.ReviewFeedback) != 2 || child.ReviewFeedback[0].ID != 11 || child.ReviewFeedback[1].ID != 21 {
		t.Errorf("child structured feedback = %+v, want selected IDs 11 and 21", child.ReviewFeedback)
	}
	for _, want := range []string{"inline selected api", "inline selected web", parent.RepoStates["repoA"].PRURL, parent.RepoStates["repoB"].PRURL} {
		if !strings.Contains(child.Description, want) {
			t.Errorf("child description missing selected context %q:\n%s", want, child.Description)
		}
	}
	for _, unwanted := range []string{"issue not selected", "review body not selected"} {
		if strings.Contains(child.Description, unwanted) {
			t.Errorf("child description contains unselected feedback %q:\n%s", unwanted, child.Description)
		}
	}

	setupBody := waitForJourneySetupComplete(t, srv.URL, childID)
	if setupBody["status"] != feature.StatusCreated.String() {
		t.Fatalf("child status after setup = %v, want Created before explicit start", setupBody["status"])
	}

	// Seed the outcomes artifact so the tail can read per-comment reply bodies.
	outcomes := []feature.ReviewFeedbackOutcome{
		{ID: 11, Disposition: feature.ReviewFeedbackOutcomeDispositionAddressed, Explanation: "fixed the handler"},
		{ID: 21, Disposition: feature.ReviewFeedbackOutcomeDispositionAddressed, Explanation: "added a test"},
	}
	outcomesData, _ := json.Marshal(outcomes)
	outcomesPath := feature.ReviewFeedbackOutcomesPath(stateDir, child)
	if err := os.MkdirAll(filepath.Dir(outcomesPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(outcomes dir): %v", err)
	}
	if err := os.WriteFile(outcomesPath, outcomesData, 0o644); err != nil {
		t.Fatalf("write outcomes: %v", err)
	}

	postAction(t, srv.URL, childID, "start", `{}`)
	waitForJourneyGate(t, srv.URL, childID, 0)
	postReviewSessionProceed(t, srv.URL, childID)
	waitForJourneyGate(t, srv.URL, childID, 1)
	postReviewSessionProceed(t, srv.URL, childID)
	waitForJourneyChildClosed(t, srv.URL, store, childID)
	// The closure tail (cleanup, KB promotion) settles before the
	// review-feedback tail runs. Wait for the parent to reach Published
	// (set by the tail) so assertions observe the completed tail.
	waitForJourneyStatus(t, srv.URL, parent.ID, feature.StatusPublished.String())

	closedChild, err := store.Load(childID)
	if err != nil {
		t.Fatalf("Store.Load(closed child) error = %v", err)
	}
	if closedChild.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted || closedChild.Parent.ClosedAt == nil {
		t.Errorf("closed child relationship = %+v, want completed with timestamp", closedChild.Parent)
	}
	for repoName, repoPath := range map[string]string{"repoA": repoA, "repoB": repoB} {
		if _, err := os.Stat(filepath.Join(repoPath, "child-output.txt")); err != nil {
			t.Errorf("%s parent worktree missing integrated child output: %v", repoName, err)
		}
		if parents := strings.Fields(journeyGit(t, repoPath, "rev-list", "--parents", "-n", "1", "HEAD")); len(parents) != 3 {
			t.Errorf("%s parent tip parents = %v, want explicit two-parent merge", repoName, parents)
		}
	}

	parentDetail := getJourneyJSON(t, srv.URL+"/api/v1/features/"+parent.ID)["feature"].(map[string]any)
	if parentDetail["status"] != feature.StatusPublished.String() {
		t.Errorf("parent status = %v, want Published", parentDetail["status"])
	}
	if parentDetail["active_child"] != nil {
		t.Errorf("parent active_child = %v, want nil after closure", parentDetail["active_child"])
	}
	history, ok := parentDetail["child_history"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("parent child_history = %#v, want one closed pass", parentDetail["child_history"])
	}
	historyItem := history[0].(map[string]any)
	if historyItem["id"] != childID || historyItem["kind"] != feature.ChildKindReviewFeedback ||
		historyItem["outcome"] != feature.ChildCloseOutcomeCompleted {
		t.Errorf("child history item = %+v, want completed review-feedback child %s", historyItem, childID)
	}

	// Phase 3: assert the real tail pushed, replied, resolved, and recorded.
	tailInvocations := fakeGH.Invocations(t)
	if len(tailInvocations) == 0 {
		t.Fatalf("no gh invocations after closure; want tail replies, thread maps, and resolutions")
	}
	// Selected comments are both inline (CommentTypeReview) → direct thread replies.
	replyA := 0
	replyB := 0
	graphqlCount := 0
	for _, inv := range tailInvocations {
		if strings.Contains(inv, "repos/example/api/pulls/1/comments/11/replies") {
			replyA++
		}
		if strings.Contains(inv, "repos/example/web/pulls/2/comments/21/replies") {
			replyB++
		}
		if strings.Contains(inv, "graphql") {
			graphqlCount++
		}
	}
	if replyA != 1 {
		t.Errorf("reply to comment 11 = %d invocations, want 1", replyA)
	}
	if replyB != 1 {
		t.Errorf("reply to comment 21 = %d invocations, want 1", replyB)
	}
	// 2 thread-map fetches + 2 thread resolutions = 4 graphql calls.
	if graphqlCount != 4 {
		t.Errorf("graphql invocations = %d, want 4 (2 thread maps + 2 resolutions)", graphqlCount)
	}
	// Unselected comments (12, 13) must receive no replies.
	for _, inv := range tailInvocations {
		if strings.Contains(inv, "comments/12/replies") || strings.Contains(inv, "comments/13/replies") {
			t.Errorf("unselected comment received a reply: %s", inv)
		}
	}

	// Assert the parent branches were pushed to the remotes.
	if tip := journeyGit(t, remoteA, "rev-parse", "--short", "refs/heads/feature/review-parent"); tip == "" {
		t.Errorf("remoteA has no feature/review-parent ref; tail did not push")
	}
	if tip := journeyGit(t, remoteB, "rev-parse", "--short", "refs/heads/feature/review-parent"); tip == "" {
		t.Errorf("remoteB has no feature/review-parent ref; tail did not push")
	}

	// Assert the tail-settled marker is durable.
	if closedChild.Parent.Transaction == nil || !closedChild.Parent.Transaction.TailSettled {
		t.Errorf("tail-settled marker = %v, want true", closedChild.Parent.Transaction)
	}

	// Assert addressed IDs were recorded.
	addressedA, err := store.LoadAddressedReviewFeedbackIDs(parent.ID, "repoA")
	if err != nil {
		t.Fatalf("load addressed IDs repoA: %v", err)
	}
	if !addressedA[11] {
		t.Errorf("addressed IDs repoA = %v, want ID 11 recorded", addressedA)
	}
	if addressedA[12] || addressedA[13] {
		t.Errorf("addressed IDs repoA = %v, unselected IDs 12/13 should not be recorded", addressedA)
	}
	addressedB, err := store.LoadAddressedReviewFeedbackIDs(parent.ID, "repoB")
	if err != nil {
		t.Fatalf("load addressed IDs repoB: %v", err)
	}
	if !addressedB[21] {
		t.Errorf("addressed IDs repoB = %v, want ID 21 recorded", addressedB)
	}

	// Second fetch: addressed comments disappear; unselected remain.
	// repoB had only comment 21 (now addressed) → 0 filtered comments →
	// the fetch handler skips repos with no comments, so only repoA appears.
	fakeGH.Clear(t)
	refetched, err := client.FetchReviewFeedback(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("second FetchReviewFeedback() error = %v", err)
	}
	if len(refetched.Repos) != 1 || refetched.Repos[0].Repo != "repoA" {
		t.Fatalf("refetched repos = %+v, want only repoA (repoB all addressed → skipped)", refetched.Repos)
	}
	// repoA: only unselected comments 12 and 13 remain (11 is addressed).
	if got := refetched.Repos[0].Comments; len(got) != 2 || got[0].ID != 12 || got[1].ID != 13 {
		t.Errorf("refetched repoA comments = %+v, want only unselected 12 and 13 (11 addressed)", got)
	}

	// Unselected comments received no replies in the tail.
	refetchInvocations := fakeGH.Invocations(t)
	for _, inv := range refetchInvocations {
		if strings.Contains(inv, "replies") || strings.Contains(inv, "resolveReviewThread") {
			t.Errorf("refetch should not trigger replies or resolutions: %s", inv)
		}
	}
}

func reviewFeedbackJourneyRepo(t *testing.T, branch, contents string) string {
	t.Helper()
	repo := testutil.InitGitRepo(t)
	journeyGit(t, repo, "checkout", "-b", branch)
	writeJourneyFile(t, repo, "base.txt", contents)
	journeyGit(t, repo, "add", "base.txt")
	journeyGit(t, repo, "commit", "-m", "review-feedback parent base")
	return repo
}

func reviewFeedbackJourneyBareRemote(t *testing.T, repoPath, branch string) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	journeyGit(t, remote, "init", "--bare")
	journeyGit(t, repoPath, "remote", "add", "origin", remote)
	// Do not push the initial branch: the tail's PullRebase is a no-op when
	// origin/<branch> does not exist (first publish), and the tail's Push
	// pushes the merge commit for the first time — preserving the
	// two-parent merge boundary.
	return remote
}

// ReviewFeedbackFeature mirrors the production request mapping and async
// setup ownership while keeping the e2e server wired to its isolated manager.
func (t *journeyMutationTarget) ReviewFeedbackFeature(featureID string, req server.ReviewFeedbackFeatureRequest) (server.ReviewFeedbackFeatureResponse, error) {
	resp := server.ReviewFeedbackFeatureResponse{ParentID: featureID, Result: "failed"}
	spec, err := server.ReviewFeedbackChildSpecFromRequest(req)
	if err != nil {
		return resp, err
	}
	var child *feature.Feature
	if err := t.orch.WithRelationshipWriteLock(func() error {
		var createErr error
		child, createErr = t.mgr.CreateReviewFeedbackChild(featureID, spec)
		return createErr
	}); err != nil {
		return resp, err
	}
	t.orch.ChildCreated(child)
	t.orch.RunSetupAsync(child.ID)
	resp.FeatureID = child.ID
	resp.Result = "created"
	return resp, nil
}

const reviewFeedbackJourneyFakeGHBehavior = `
endpoint="$2"
if [ "$2" = "--paginate" ]; then
  endpoint="$3"
fi
case "$endpoint" in
  repos/example/api/pulls/1/comments)
    printf '%s\n' '[{"id":11,"path":"api.go","line":8,"body":"inline selected api","diff_hunk":"@@ -7 +7,2 @@","user":{"login":"alice"},"created_at":"2026-08-02T08:00:00Z"}]'
    ;;
  repos/example/api/issues/1/comments)
    if [ "$2" = "--paginate" ]; then
      printf '%s\n' '[{"id":12,"body":"issue not selected","user":{"login":"bob"},"created_at":"2026-08-02T09:00:00Z"}]'
    fi
    ;;
  repos/example/api/pulls/1/reviews)
    printf '%s\n' '[{"id":13,"body":"review body not selected","user":{"login":"carol"},"submitted_at":"2026-08-02T10:00:00Z"}]'
    ;;
  repos/example/web/pulls/2/comments)
    printf '%s\n' '[{"id":21,"path":"web.go","line":5,"body":"inline selected web","diff_hunk":"@@ -4 +4,2 @@","user":{"login":"dana"},"created_at":"2026-08-02T08:30:00Z"}]'
    ;;
  repos/example/web/issues/2/comments)
    if [ "$2" = "--paginate" ]; then
      printf '%s\n' '[]'
    fi
    ;;
  repos/example/web/pulls/2/reviews)
    printf '%s\n' '[]'
    ;;
  repos/example/api/pulls/1/comments/11/replies)
    # Reply to inline comment 11 — succeed silently.
    ;;
  repos/example/web/pulls/2/comments/21/replies)
    # Reply to inline comment 21 — succeed silently.
    ;;
  graphql)
    if echo "$4" | grep -q "resolveReviewThread"; then
      : # Thread resolution — succeed silently.
    elif echo "$4" | grep -q 'name: "api"'; then
      printf '%s\n' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"thread-11","isResolved":false,"comments":{"nodes":[{"databaseId":11}]}}]}}}}}'
    elif echo "$4" | grep -q 'name: "web"'; then
      printf '%s\n' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"thread-21","isResolved":false,"comments":{"nodes":[{"databaseId":21}]}}]}}}}}'
    fi
    ;;
  *)
    printf 'unexpected gh arguments: %s\n' "$*" >&2
    exit 2
    ;;
esac
`
