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
	"fmt"
	"io"
	"net/http"
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

// The stacked review-feedback journey proves the stack-aware pass end to end
// on the stack publish fixture: a parent with a three-layer stack is published
// against a bare remote and the in-test GitHub pull store, a scripted inline
// review comment lands on layer 2's pull request, and the child launched for
// it through the REST surface edits a file owned by layer 2, round-commits it
// through the real round-commit hook (once with a fix manifest naming layer 2,
// once with no manifest relying on the commented-layer default), integrates
// through the relocation ladder, and closes. The tail republishes the
// rewritten layers with leases, replies on the commented pull request with the
// relocated SHA, and resolves the review thread.

// The feature-side repository name (stackedReviewFeedbackRepo) matches the
// shared scripted phase-plan text's **Repo:** tag; the GitHub-side repository
// identity (stackedReviewFeedbackGitHubRepo) is served by the bare origin's
// scp-style remote path and the in-test pull store. The feature-side name and
// the GitHub name are independent namespaces.
const (
	stackedReviewFeedbackRepo       = "repoA"
	stackedReviewFeedbackGitHubRepo = "repo-a"
	stackedReviewFeedbackCommentID  = 101
	stackedReviewFeedbackFixMarker  = "fixed by the review feedback pass"
)

// TestStackedReviewFeedbackChildJourneyWithManifest drives the full stacked
// review-feedback pass with the child's fix manifest naming layer 2 for the
// dirty path, so the round-commit hook partitions the fix into a single
// Stack-Layer: 2 commit.
func TestStackedReviewFeedbackChildJourneyWithManifest(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}
	runStackedReviewFeedbackJourney(t, true)
}

// TestStackedReviewFeedbackChildJourneyDefaultingToCommentedLayer drives the
// same journey with no manifest: the only selected comment sits on layer 2,
// so the round-commit child mode must default the unlisted path to that
// commented layer and tag the commit Stack-Layer: 2 as well.
func TestStackedReviewFeedbackChildJourneyDefaultingToCommentedLayer(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}
	runStackedReviewFeedbackJourney(t, false)
}

// stackedReviewFixReport is the child implementer stub's observation of its
// own round commit, delivered back to the test goroutine because the child
// worktree is removed during closure cleanup.
type stackedReviewFixReport struct {
	Worktree    string
	BaseSHA     string
	HeadSHA     string
	Commits     int
	LastMessage string
}

func runStackedReviewFeedbackJourney(t *testing.T, withManifest bool) {
	t.Helper()

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	wtBaseDir := filepath.Join(tmp, "worktrees")
	remotesDir := filepath.Join(tmp, "remotes")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	// The bare origin's directory name parses as a git@localhost:acme
	// scp-style remote so the production origin resolution reads
	// host/owner/repo identity from a remote that never leaves the test
	// machine while pull requests are served by the in-test GitHub fake.
	repoA := testutil.InitGitRepo(t)
	bareA := stackJourneyBareOrigin(t, remotesDir, repoA, stackedReviewFeedbackGitHubRepo)

	fake := testutil.InstallFakeGitHubAPI(t)
	pulls := testutil.NewFakePullStore("acme")
	pulls.Install(t, fake, stackedReviewFeedbackGitHubRepo)

	cfg := config.NewDefault()
	cfg.Repos[stackedReviewFeedbackRepo] = config.RepoConfig{Path: repoA}

	store := feature.NewStore(stateDir)
	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = wm

	serverEvents := make(chan interface{}, 512)
	sm := session.NewManager(serverEvents)
	t.Cleanup(sm.Shutdown)

	fixReports := make(chan stackedReviewFixReport, 4)
	pr := journeyChildPhaseRunner(t, sm, store, stateDir)
	pr.RunImplementFn = stackedReviewFeedbackImplementer(stateDir, withManifest, fixReports)

	// The lifecycle orchestrator drives approval and the mid-flight phases
	// without a PhaseRunner so no plan session dispatches; the execution
	// orchestrator carries the scripted PhaseRunner and owns the final
	// phase's Final Review, the manual publish action, and the whole child
	// pass through closure and the review-feedback tail.
	lifecycleOrch := orchestrator.New(orchestrator.Deps{
		Lifecycle: mgr,
		Store:     store,
		Worktrees: wm,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = lifecycleOrch.Shutdown()
		lifecycleOrch.WaitForCycles()
	})
	execOrch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Worktrees:   wm,
		Sessions:    sm,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = execOrch.Shutdown()
		execOrch.WaitForCycles()
	})

	stopForwarding := make(chan struct{})
	defer close(stopForwarding)
	go func() {
		for {
			select {
			case ev := <-execOrch.Events():
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
		Mutations:             &journeyMutationTarget{mgr: mgr, orch: execOrch},
		DisableHostValidation: true,
	}))
	t.Cleanup(srv.Close)
	client, err := server.NewClient(server.ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("server.NewClient() error = %v", err)
	}

	// ------------------------------------------------------------------
	// Parent: three phases, one distinct file per layer, manual publish.
	// ------------------------------------------------------------------
	parent, err := mgr.Create("Stacked review feedback", "publishes a three-layer stack",
		[]string{stackedReviewFeedbackRepo}, cfg.Defaults.Models, "", "", nil,
		feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := mgr.RunSetup(parent.ID); err != nil {
		t.Fatalf("run setup: %v", err)
	}
	roadmap := "# Roadmap\n\n" +
		"## Phase 1: Bootstrap\n\n### Goal\nStart.\n\n" +
		"## Phase 2: Extension\n\n### Goal\nExtend.\n\n" +
		"## Phase 3: Polish\n\n### Goal\nPolish.\n\n" +
		"## Pull Requests\n\n" +
		"| # | Title | Phases | Rationale |\n|---|---|---|---|\n" +
		"| 1 | Bootstrap | 1 | Stands alone. |\n" +
		"| 2 | Extension | 2 | Builds on bootstrap. |\n" +
		"| 3 | Polish | 3 | Builds on extension. |\n"
	roadmapDir := filepath.Join(stateDir, parent.ID, "runs", "run-001", "roadmap")
	if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir roadmap: %v", err)
	}
	roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
	if err := os.WriteFile(roadmapPath, []byte(roadmap), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}
	planGate := feature.PhasePlan
	if err := store.Modify(parent.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPlanNeedsReview
		ff.PendingReviewPhase = &planGate
		ff.Artifacts = map[string]string{"roadmap": roadmapPath}
		return nil
	}); err != nil {
		t.Fatalf("seed roadmap gate: %v", err)
	}
	if err := lifecycleOrch.HandleReviewDecision(parent.ID, orchestrator.ReviewDecision{
		Decision: "proceed",
		Roadmap:  true,
	}); err != nil {
		t.Fatalf("HandleReviewDecision: %v", err)
	}

	worktreeFor := func() string {
		t.Helper()
		ff, err := mgr.Get(parent.ID)
		if err != nil {
			t.Fatalf("get parent: %v", err)
		}
		return ff.Repos[0].WorktreePath
	}
	startImplementing := func(phase int) {
		t.Helper()
		if err := store.Modify(parent.ID, func(ff *feature.Feature) error {
			ff.Status = feature.StatusImplementing
			ff.CurrentPhase = feature.PhaseImplement
			ff.CurrentRoadmapPhase = phase
			ff.Checkpoints.ManualPublish = true
			if ff.RepoStates == nil {
				ff.RepoStates = map[string]*feature.RepoState{}
			}
			state, ok := ff.RepoStates[stackedReviewFeedbackRepo]
			if !ok {
				state = &feature.RepoState{}
				ff.RepoStates[stackedReviewFeedbackRepo] = state
			}
			state.Touched = true
			return nil
		}); err != nil {
			t.Fatalf("seed implementing phase %d: %v", phase, err)
		}
	}
	completePhaseOn := func(orch *orchestrator.Orchestrator, phase int) {
		t.Helper()
		if err := orch.HandlePhaseCompletion(parent.ID, orchestrator.PhaseCompletionInput{
			Phase:           feature.PhaseImplement,
			MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
		}); err != nil {
			t.Fatalf("HandlePhaseCompletion phase %d: %v", phase, err)
		}
	}

	startImplementing(1)
	testutil.CommitFile(t, worktreeFor(), "layer1.txt", "layer 1 work\n", "phase 1")
	completePhaseOn(lifecycleOrch, 1)
	startImplementing(2)
	testutil.CommitFile(t, worktreeFor(), "layer2.txt", "layer 2 work\n", "phase 2")
	completePhaseOn(lifecycleOrch, 2)
	startImplementing(3)
	testutil.CommitFile(t, worktreeFor(), "layer3.txt", "layer 3 work\n", "phase 3")
	completePhaseOn(execOrch, 3)

	detail := getJourneyJSON(t, srv.URL+"/api/v1/features/"+parent.ID)
	parentBody, _ := detail["feature"].(map[string]any)
	if parentBody == nil || parentBody["status"] != feature.StatusCodeReady.String() {
		t.Fatalf("parent status before publish = %v, want code_ready", parentBody)
	}
	sourceRevision := ""
	if meta, ok := detail["meta"].(map[string]any); ok {
		if rev, ok := meta["revision"].(string); ok {
			sourceRevision = rev
		}
	}
	status, raw := postActionStatus(t, srv.URL, parent.ID, "publish",
		fmt.Sprintf(`{"source_revision":%q,"repos":[%q]}`, sourceRevision, stackedReviewFeedbackRepo))
	if status != http.StatusOK || !strings.Contains(string(raw), `"published"`) {
		t.Fatalf("publish action status = %d body = %s, want 200 published", status, raw)
	}

	// ------------------------------------------------------------------
	// The published stack: three open pull requests, one per layer, with
	// pushed-up-to-date entries and remote branches at the recorded tips.
	// ------------------------------------------------------------------
	parent, err = mgr.Get(parent.ID)
	if err != nil {
		t.Fatalf("reload parent after publish: %v", err)
	}
	if parent.Status != feature.StatusPublished {
		t.Fatalf("parent status = %v, want Published", parent.Status)
	}
	if len(parent.Stack) != 3 {
		t.Fatalf("parent stack layers = %d, want 3", len(parent.Stack))
	}
	workspaceSlug := feature.WorkspaceSlug(parent.Slug, parent.ID)
	layerBranches := []string{
		git.LayerBranchName(workspaceSlug, 1, "bootstrap"),
		git.LayerBranchName(workspaceSlug, 2, "extension"),
		git.LayerBranchName(workspaceSlug, 3, "polish"),
	}
	wantTitles := []string{"Bootstrap", "Extension", "Polish"}
	bareRef := func(branch string) string {
		t.Helper()
		return journeyGit(t, bareA, "rev-parse", "refs/heads/"+branch)
	}
	prURLs := make([]string, 3)
	prNumbers := make([]int, 3)
	for i, wantTitle := range wantTitles {
		layer := parent.Stack[i]
		if layer.Position != i+1 || layer.Title != wantTitle || layer.Branch != layerBranches[i] {
			t.Fatalf("stack layer %d = %+v, want position %d titled %q on %s", i, layer, i+1, wantTitle, layerBranches[i])
		}
		entry := layer.Repos[stackedReviewFeedbackRepo]
		if entry.PRURL == "" || entry.PRState != feature.StackPRStateOpen {
			t.Fatalf("layer %d entry = %+v, want an open pull request", i+1, entry)
		}
		if entry.TipSHA == "" || entry.LastPushedSHA != entry.TipSHA || entry.TipSHA != bareRef(layer.Branch) {
			t.Fatalf("layer %d entry = %+v, want tip %s pushed up to date", i+1, entry, bareRef(layer.Branch))
		}
		prURLs[i] = entry.PRURL
		prNumbers[i] = journeyPRNumber(t, entry.PRURL)
	}
	if got := pulls.CreatedCount(stackedReviewFeedbackGitHubRepo); got != 3 {
		t.Fatalf("created pull requests = %d, want one per layer", got)
	}
	record2, _ := pulls.Pull(stackedReviewFeedbackGitHubRepo, prNumbers[1])
	record3, _ := pulls.Pull(stackedReviewFeedbackGitHubRepo, prNumbers[2])
	if record2.Base != layerBranches[0] || record3.Base != layerBranches[1] {
		t.Fatalf("stacked pull request bases = %q/%q, want each layer based on the one below",
			record2.Base, record3.Base)
	}
	preLayer2Tip := bareRef(layerBranches[1])
	preLayer3Tip := bareRef(layerBranches[2])
	patchedAfterPublish := pulls.PatchedCount()

	// ------------------------------------------------------------------
	// A scripted inline review comment on layer 2's pull request only.
	// ------------------------------------------------------------------
	installStackedReviewFeedbackCommentAPI(t, fake, prNumbers)

	fetched, err := client.FetchReviewFeedback(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("FetchReviewFeedback() error = %v", err)
	}
	if len(fetched.Repos) != 1 || fetched.Repos[0].Repo != stackedReviewFeedbackRepo {
		t.Fatalf("fetched repos = %+v, want only %s offering the layer-2 comment", fetched.Repos, stackedReviewFeedbackRepo)
	}
	groups := fetched.Repos[0].PullRequests
	if len(groups) != 1 || groups[0].Position != 2 || groups[0].Title != "Extension" || groups[0].URL != prURLs[1] {
		t.Fatalf("pull-request groups = %+v, want one group on layer 2 (Extension, %s)", groups, prURLs[1])
	}
	comments := groups[0].Comments
	if len(comments) != 1 {
		t.Fatalf("layer-2 comments = %+v, want exactly the scripted inline comment", comments)
	}
	comment := comments[0]
	if comment.ID != stackedReviewFeedbackCommentID || comment.Type != "review" ||
		comment.PrURL != prURLs[1] || comment.PrNumber != prNumbers[1] ||
		comment.LayerPosition != 2 || comment.LayerTitle != "Extension" {
		t.Fatalf("layer-2 comment = %+v, want inline comment %d tagged to layer 2's pull request", comment, stackedReviewFeedbackCommentID)
	}

	gate := true
	launched, err := client.ReviewFeedbackFeature(t.Context(), parent.ID, server.ReviewFeedbackFeatureRequest{
		ExpectedRevision: fetched.Revision,
		Gate:             &gate,
	})
	if err != nil {
		t.Fatalf("ReviewFeedbackFeature() error = %v", err)
	}
	if launched.FeatureID == "" || launched.ParentID != parent.ID {
		t.Fatalf("ReviewFeedbackFeature() = %+v, want child reference for parent %s", launched, parent.ID)
	}
	if launched.Changed != 0 || launched.Omitted != 0 || launched.Deferred != 0 {
		t.Fatalf("launch reconciliation counts = changed:%d omitted:%d deferred:%d, want all zero",
			launched.Changed, launched.Omitted, launched.Deferred)
	}
	childID := launched.FeatureID

	// ------------------------------------------------------------------
	// Child execution: setup, outcomes artifact, both review gates, the
	// scripted implement round, closure, and the review-feedback tail.
	// ------------------------------------------------------------------
	setupBody := waitForJourneySetupComplete(t, srv.URL, childID)
	if setupBody["status"] != feature.StatusCreated.String() {
		t.Fatalf("child status after setup = %v, want Created before explicit start", setupBody["status"])
	}
	child, err := store.Load(childID)
	if err != nil {
		t.Fatalf("load child: %v", err)
	}
	if len(child.ReviewFeedback) != 1 || child.ReviewFeedback[0].ID != stackedReviewFeedbackCommentID ||
		child.ReviewFeedback[0].LayerPosition != 2 {
		t.Fatalf("child structured feedback = %+v, want comment %d on layer 2", child.ReviewFeedback, stackedReviewFeedbackCommentID)
	}
	outcomes := []feature.ReviewFeedbackOutcome{{
		ID:          stackedReviewFeedbackCommentID,
		Disposition: feature.ReviewFeedbackOutcomeDispositionAddressed,
		Explanation: "rewrote the layer two file",
	}}
	outcomesData, err := json.Marshal(outcomes)
	if err != nil {
		t.Fatalf("marshal outcomes: %v", err)
	}
	outcomesPath := feature.ReviewFeedbackOutcomesPath(stateDir, child)
	if err := os.MkdirAll(filepath.Dir(outcomesPath), 0o755); err != nil {
		t.Fatalf("mkdir outcomes dir: %v", err)
	}
	if err := os.WriteFile(outcomesPath, outcomesData, 0o644); err != nil {
		t.Fatalf("write outcomes: %v", err)
	}

	tailMark := len(fake.Requests())
	postAction(t, srv.URL, childID, "start", `{}`)
	waitForJourneyGate(t, srv.URL, childID, 0)
	postReviewSessionProceed(t, srv.URL, childID)
	waitForJourneyGate(t, srv.URL, childID, 1)
	postReviewSessionProceed(t, srv.URL, childID)
	waitForJourneyChildClosed(t, srv.URL, store, childID)
	waitForStackedReviewFeedbackTailSettled(t, store, childID)

	var report stackedReviewFixReport
	select {
	case report = <-fixReports:
	case <-time.After(30 * time.Second):
		t.Fatalf("child implementer stub never reported its round commit")
	}

	// ------------------------------------------------------------------
	// Assertions.
	// ------------------------------------------------------------------
	closedChild, err := store.Load(childID)
	if err != nil {
		t.Fatalf("load closed child: %v", err)
	}
	if closedChild.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		t.Errorf("closed child relationship = %+v, want completed", closedChild.Parent)
	}
	if closedChild.Parent.Transaction == nil {
		t.Fatalf("closed child transaction journal missing")
	}
	if !closedChild.Parent.Transaction.TailSettled {
		t.Errorf("tail-settled marker = %+v, want settled", closedChild.Parent.Transaction)
	}

	// The round commit: one commit above the launch base, tagged for
	// layer 2 — through the manifest or the commented-layer default.
	if report.Commits != 1 {
		t.Errorf("child round commits above the launch base = %d, want 1", report.Commits)
	}
	if !strings.Contains(report.LastMessage, "Stack-Layer: 2") {
		t.Errorf("child round commit message = %q, want a Stack-Layer: 2 trailer", report.LastMessage)
	}

	// The journal entry recorded one ref per rewritten layer with the
	// pre-integration tips as anchors and the relocated commit map.
	entry := closedChild.Parent.Transaction.EntryByRepo(stackedReviewFeedbackRepo)
	if entry == nil {
		t.Fatalf("journal entry for %s missing", stackedReviewFeedbackRepo)
	}
	if len(entry.Refs) != 2 || entry.Refs[0].Layer != 2 || entry.Refs[1].Layer != 3 ||
		entry.Refs[0].AnchorSHA != preLayer2Tip || entry.Refs[1].AnchorSHA != preLayer3Tip {
		t.Errorf("journal entry refs = %+v, want layers 2 and 3 anchored at %s/%s",
			entry.Refs, preLayer2Tip, preLayer3Tip)
	}
	if len(entry.Relocated) == 0 {
		t.Errorf("journal entry relocated map is empty; want the child commit's relocated SHA")
	}

	// The remote: layer 2's range carries the fix, layer 3's does not,
	// and layer 3 was force-pushed with the replayed commits.
	remoteLayer2Tip := bareRef(layerBranches[1])
	remoteLayer3Tip := bareRef(layerBranches[2])
	if remoteLayer2Tip == preLayer2Tip {
		t.Errorf("remote layer-2 tip = %s unchanged; the relocated fix never landed", remoteLayer2Tip)
	}
	if remoteLayer3Tip == preLayer3Tip {
		t.Errorf("remote layer-3 tip = pre-integration tip %s; the replayed chain was not force-pushed", remoteLayer3Tip)
	}
	layer2Diff := journeyGit(t, bareA, "diff", "refs/heads/"+layerBranches[0], "refs/heads/"+layerBranches[1])
	if !strings.Contains(layer2Diff, "layer2.txt") || !strings.Contains(layer2Diff, stackedReviewFeedbackFixMarker) {
		t.Errorf("remote layer-2 diff lacks the relocated fix:\n%s", layer2Diff)
	}
	layer3Diff := journeyGit(t, bareA, "diff", "refs/heads/"+layerBranches[1], "refs/heads/"+layerBranches[2])
	if strings.Contains(layer3Diff, stackedReviewFeedbackFixMarker) {
		t.Errorf("remote layer-3 diff contains the layer-2 fix; the fix leaked above its layer:\n%s", layer3Diff)
	}
	if strings.Contains(layer3Diff, "layer2.txt") {
		t.Errorf("remote layer-3 diff touches layer2.txt:\n%s", layer3Diff)
	}
	if !strings.Contains(layer3Diff, "layer3.txt") {
		t.Errorf("remote layer-3 diff lacks the replayed layer-3 commits:\n%s", layer3Diff)
	}
	if report.HeadSHA != "" {
		if _, err := journeyGitIn(repoA, "merge-base", "--is-ancestor", report.HeadSHA,
			"refs/heads/"+layerBranches[2]); err == nil {
			t.Errorf("the child's original fix commit %s is on layer 3's history; it must only survive as its relocated copy", report.HeadSHA)
		}
	}

	// The pull store: no new pull request, and the walk's stack-section
	// refresh ran against it — the bodies were maintained through the real
	// publish machinery (the initial pass patched them; the tail's refresh
	// is a no-op when no state or link changed, which is the case here).
	if got := pulls.CreatedCount(stackedReviewFeedbackGitHubRepo); got != 3 {
		t.Errorf("created pull requests after the tail = %d, want still 3 (no new pull request)", got)
	}
	if patchedAfterPublish == 0 {
		t.Errorf("patched pull requests before the child = 0, want the publish walk's body updates recorded")
	}
	if got := pulls.PatchedCount(); got != patchedAfterPublish {
		t.Errorf("patched pull requests after the tail = %d, want still %d (an unchanged stack section produces no rewrite)", got, patchedAfterPublish)
	}

	// The reply names a SHA that resolves on the remote layer-2 branch,
	// and the thread is resolved exactly once.
	tailRequests := fake.Requests()[tailMark:]
	replyPath := fmt.Sprintf("repos/acme/%s/pulls/%d/comments/%d/replies",
		stackedReviewFeedbackGitHubRepo, prNumbers[1], stackedReviewFeedbackCommentID)
	replyLines := make([]string, 0, 1)
	resolveCount := 0
	threadMapCount := 0
	for _, inv := range tailRequests {
		if strings.Contains(inv, replyPath) {
			replyLines = append(replyLines, inv)
		}
		if strings.Contains(inv, "resolveReviewThread") {
			resolveCount++
		}
		if strings.Contains(inv, "reviewThreads") {
			threadMapCount++
		}
	}
	if len(replyLines) != 1 {
		t.Fatalf("reply requests = %d (%v), want exactly 1 on the layer-2 comment", len(replyLines), replyLines)
	}
	if !strings.Contains(replyLines[0], "rewrote the layer two file") {
		t.Errorf("reply body lacks the addressed explanation:\n%s", replyLines[0])
	}
	replySHA := extractStackedReviewReplySHA(t, replyLines[0])
	if _, err := journeyGitIn(bareA, "rev-parse", "--verify", replySHA+"^{commit}"); err != nil {
		t.Fatalf("reply SHA %s does not resolve on the remote: %v", replySHA, err)
	}
	if _, err := journeyGitIn(bareA, "merge-base", "--is-ancestor", replySHA,
		"refs/heads/"+layerBranches[1]); err != nil {
		t.Errorf("reply SHA %s is not an ancestor of the remote layer-2 branch: %v", replySHA, err)
	}
	if resolveCount != 1 {
		t.Errorf("resolveReviewThread requests = %d, want exactly 1 after closure", resolveCount)
	}
	if threadMapCount != 1 {
		t.Errorf("review-thread map queries = %d, want exactly 1 after closure", threadMapCount)
	}

	// The read model: every layer lists an open pull request whose
	// recorded tip equals its last pushed SHA, and the parent is settled.
	finalParent, err := mgr.Get(parent.ID)
	if err != nil {
		t.Fatalf("reload parent after the tail: %v", err)
	}
	if finalParent.Status != feature.StatusPublished {
		t.Errorf("parent status after the tail = %v, want Published", finalParent.Status)
	}
	if len(finalParent.Stack) != 3 {
		t.Fatalf("parent stack layers after the tail = %d, want 3", len(finalParent.Stack))
	}
	for i := range finalParent.Stack {
		entry := finalParent.Stack[i].Repos[stackedReviewFeedbackRepo]
		if entry.PRState != feature.StackPRStateOpen || entry.PRURL == "" ||
			entry.LastPushedSHA != entry.TipSHA || entry.TipSHA == "" {
			t.Errorf("layer %d entry after the tail = %+v, want an open pull request pushed up to date", i+1, entry)
		}
	}
	if got := finalParent.Stack[2].Repos[stackedReviewFeedbackRepo].TipSHA; got != remoteLayer3Tip {
		t.Errorf("recorded layer-3 tip = %s, want the remote tip %s", got, remoteLayer3Tip)
	}
	if got := finalParent.Stack[1].Repos[stackedReviewFeedbackRepo].TipSHA; got != remoteLayer2Tip {
		t.Errorf("recorded layer-2 tip = %s, want the remote tip %s", got, remoteLayer2Tip)
	}

	parentDetail := getJourneyJSON(t, srv.URL+"/api/v1/features/"+parent.ID)["feature"].(map[string]any)
	if parentDetail["status"] != feature.StatusPublished.String() {
		t.Errorf("parent detail status = %v, want Published", parentDetail["status"])
	}
	if parentDetail["active_child"] != nil {
		t.Errorf("parent active_child = %v, want nil after closure", parentDetail["active_child"])
	}
	repoStatuses, _ := parentDetail["repo_status"].([]any)
	var repoStatus map[string]any
	for _, row := range repoStatuses {
		if r, ok := row.(map[string]any); ok && r["name"] == stackedReviewFeedbackRepo {
			repoStatus = r
		}
	}
	if repoStatus == nil {
		t.Fatalf("parent detail repo_status has no %s row: %#v", stackedReviewFeedbackRepo, repoStatuses)
	}
	prEntries, _ := repoStatus["pull_requests"].([]any)
	if len(prEntries) != 3 {
		t.Fatalf("repo_status pull_requests = %#v, want one entry per layer", prEntries)
	}
	for i, row := range prEntries {
		entry, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("pull_requests[%d] = %#v, want an object", i, row)
		}
		if entry["position"] != float64(i+1) || entry["state"] != string(feature.StackPRStateOpen) ||
			entry["pushed_up_to_date"] != true || entry["url"] != prURLs[i] {
			t.Errorf("pull_requests[%d] = %#v, want layer %d open, pushed up to date, at %s",
				i, entry, i+1, prURLs[i])
		}
	}
}

// stackedReviewFeedbackImplementer returns the child implementer stub: it
// rewrites the file layer 2's commit range owns, writes the optional fix
// manifest naming layer 2 into an iteration directory it creates, and invokes
// the real round-commit hook exactly like the implementation loop does. The
// hook partitions the dirty path against the parent's stack and creates the
// Stack-Layer tagged commit; the stub reports the resulting round back to the
// test before the closure cleanup removes the child worktree.
func stackedReviewFeedbackImplementer(stateDir string, withManifest bool, reports chan<- stackedReviewFixReport) func(agent.ImplementConfig, ports.SessionManager) (*agent.LoopResult, error) {
	return func(c agent.ImplementConfig, _ ports.SessionManager) (*agent.LoopResult, error) {
		if c.Feature == nil || c.Feature.Parent == nil || c.Feature.Parent.Kind != feature.ChildKindReviewFeedback {
			return nil, fmt.Errorf("stacked review-feedback journey: implement invoked for %v, want a review-feedback child", c.Feature)
		}
		if c.RoundCommitHook == nil {
			return nil, fmt.Errorf("stacked review-feedback journey: the orchestrator never wired the round-commit hook")
		}
		worktree := ""
		for _, repo := range c.Feature.Repos {
			if repo.Name != stackedReviewFeedbackRepo {
				continue
			}
			worktree = repo.WorktreePath
			if worktree == "" {
				worktree = repo.Path
			}
		}
		if worktree == "" {
			return nil, fmt.Errorf("stacked review-feedback journey: no worktree for %s", stackedReviewFeedbackRepo)
		}
		fixContents := "layer 2 work\n" + stackedReviewFeedbackFixMarker + "\n"
		if err := os.WriteFile(filepath.Join(worktree, "layer2.txt"), []byte(fixContents), 0o644); err != nil {
			return nil, fmt.Errorf("stacked review-feedback journey: rewriting layer2.txt: %w", err)
		}
		iterationDir := filepath.Join(stateDir, c.Feature.ID, "implement", "iterations", "001")
		if err := os.MkdirAll(iterationDir, 0o755); err != nil {
			return nil, fmt.Errorf("stacked review-feedback journey: creating the iteration dir: %w", err)
		}
		if withManifest {
			manifest := "entries:\n" +
				"  - layer: 2\n" +
				"    repository: " + stackedReviewFeedbackRepo + "\n" +
				"    paths:\n" +
				"      - layer2.txt\n"
			if err := os.WriteFile(filepath.Join(iterationDir, agent.FixManifestFilename), []byte(manifest), 0o644); err != nil {
				return nil, fmt.Errorf("stacked review-feedback journey: writing the fix manifest: %w", err)
			}
		}
		input := agent.RoundCommitInput{
			FeatureID:            c.Feature.ID,
			PhaseNumber:          c.Feature.CurrentRoadmapPhase,
			TotalPhases:          c.Feature.TotalRoadmapPhases,
			PhaseType:            c.Feature.RoadmapPhaseType,
			Iteration:            1,
			Kind:                 agent.RoundCommitImplement,
			FirstImplementCommit: true,
			IterationDir:         iterationDir,
			Repos:                map[string]string{stackedReviewFeedbackRepo: worktree},
		}
		if err := c.RoundCommitHook(input); err != nil {
			return nil, fmt.Errorf("stacked review-feedback journey: round commit: %w", err)
		}
		report := stackedReviewFixReport{
			Worktree: worktree,
			BaseSHA:  c.Feature.BaseSHA(stackedReviewFeedbackRepo),
		}
		if sha, err := journeyGitIn(worktree, "rev-parse", "HEAD"); err == nil {
			report.HeadSHA = sha
			if out, err := journeyGitIn(worktree, "rev-list", "--count", report.BaseSHA+"..HEAD"); err == nil {
				if n, err := strconv.Atoi(out); err == nil {
					report.Commits = n
				}
			}
			if msg, err := journeyGitIn(worktree, "log", "-1", "--format=%B"); err == nil {
				report.LastMessage = msg
			}
		}
		reports <- report
		return &agent.LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil
	}
}

// installStackedReviewFeedbackCommentAPI serves the PR feedback endpoints per
// pull-request number: the one scripted inline comment on layer 2's pull
// request, empty comment lists for the layer 1 and layer 3 pull requests so
// the fetch offers only the layer-2 comment, the in-thread reply endpoint for
// that comment, and the GraphQL review-thread map and resolution for layer
// 2's pull request.
func installStackedReviewFeedbackCommentAPI(t *testing.T, fake *testutil.FakeGitHubAPI, prNumbers []int) {
	t.Helper()
	repo := stackedReviewFeedbackGitHubRepo
	layer2 := prNumbers[1]
	fake.HandleJSON(fmt.Sprintf("/repos/acme/%s/pulls/%d/comments", repo, layer2),
		http.StatusOK,
		fmt.Sprintf(`[{"id":%d,"path":"layer2.txt","line":2,"body":"please harden the layer two logic","diff_hunk":"@@ -1 +1,2 @@","user":{"login":"reviewer"},"created_at":"2026-09-01T08:00:00Z"}]`,
			stackedReviewFeedbackCommentID))
	fake.HandleJSON(fmt.Sprintf("/repos/acme/%s/issues/%d/comments", repo, layer2), http.StatusOK, `[]`)
	fake.HandleJSON(fmt.Sprintf("/repos/acme/%s/pulls/%d/reviews", repo, layer2), http.StatusOK, `[]`)
	for _, n := range []int{prNumbers[0], prNumbers[2]} {
		fake.HandleJSON(fmt.Sprintf("/repos/acme/%s/pulls/%d/comments", repo, n), http.StatusOK, `[]`)
		fake.HandleJSON(fmt.Sprintf("/repos/acme/%s/issues/%d/comments", repo, n), http.StatusOK, `[]`)
		fake.HandleJSON(fmt.Sprintf("/repos/acme/%s/pulls/%d/reviews", repo, n), http.StatusOK, `[]`)
	}
	fake.HandleJSON(fmt.Sprintf("/repos/acme/%s/pulls/%d/comments/%d/replies",
		repo, layer2, stackedReviewFeedbackCommentID), http.StatusCreated, `{}`)
	fake.Mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "resolveReviewThread"):
			fmt.Fprint(w, `{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}`)
		case stackedReviewGraphQLThreadMapMatches(body, layer2):
			fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"thread-%d","isResolved":false,"comments":{"nodes":[{"databaseId":%d}]}}]}}}}}`,
				stackedReviewFeedbackCommentID, stackedReviewFeedbackCommentID)
		default:
			fmt.Fprint(w, `{"data":{}}`)
		}
	})
}

// stackedReviewGraphQLThreadMapMatches reports whether a GraphQL request is
// the review-thread map query for the layer-2 pull request of the journey's
// repository, tolerating both the plain and escaped variable encodings go-gh
// emits.
func stackedReviewGraphQLThreadMapMatches(body []byte, number int) bool {
	text := string(body)
	nameMatch := strings.Contains(text, `\"name\":\"`+stackedReviewFeedbackGitHubRepo+`\"`) ||
		strings.Contains(text, `"name":"`+stackedReviewFeedbackGitHubRepo+`"`)
	numberMatch := strings.Contains(text, fmt.Sprintf(`"number":%d`, number)) ||
		strings.Contains(text, fmt.Sprintf(`\"number\":%d`, number))
	return nameMatch && numberMatch
}

// extractStackedReviewReplySHA extracts the commit SHA cited by a reply body
// ("Addressed in `<sha>`" optionally followed by an explanation) from one
// logged fake-API request line.
func extractStackedReviewReplySHA(t *testing.T, requestLine string) string {
	t.Helper()
	const marker = "Addressed in `"
	i := strings.Index(requestLine, marker)
	if i < 0 {
		t.Fatalf("reply request carries no addressed SHA:\n%s", requestLine)
	}
	rest := requestLine[i+len(marker):]
	j := strings.IndexByte(rest, '`')
	if j < 0 {
		t.Fatalf("reply request's addressed SHA is unterminated:\n%s", requestLine)
	}
	sha := rest[:j]
	if sha == "" {
		t.Fatalf("reply request cites an empty SHA:\n%s", requestLine)
	}
	return sha
}

// waitForStackedReviewFeedbackTailSettled polls until the child's transaction
// journal carries the durable tail-settled marker, so assertions observe the
// completed republish/reply/resolve tail rather than racing it.
func waitForStackedReviewFeedbackTailSettled(t *testing.T, store *feature.Store, childID string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		child, err := store.Load(childID)
		if err == nil && child != nil && child.Parent != nil && child.Parent.Transaction != nil {
			last = fmt.Sprintf("phase=%s tailSettled=%v", child.Parent.Transaction.Phase, child.Parent.Transaction.TailSettled)
			if child.Parent.Transaction.TailSettled {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("child %s review-feedback tail never settled; last: %s", childID, last)
}
