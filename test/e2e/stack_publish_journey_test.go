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
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
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

	h := newStackResolutionHarness(t)

	// assertStackPublished pins the delivered shape for one feature: two
	// pull requests in ascending layer order with the table titles and
	// bases, stack sections naming the top layer as current, remote branches
	// at the recorded tips, and the published status with the repository's
	// top-layer pull request URL.
	assertStackPublished := func(f *feature.Feature) {
		t.Helper()
		workspaceSlug := feature.WorkspaceSlug(f.Slug, f.ID)
		layer1Branch := git.LayerBranchName(workspaceSlug, 1, "bootstrap")
		layer2Branch := git.LayerBranchName(workspaceSlug, 2, "build-and-polish")

		ff, err := h.mgr.Get(f.ID)
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
		if got := ff.TopStackLayerPRURL("repo-a"); got != pr2 {
			t.Fatalf("%s top-layer pull request URL = %q, want the layer-2 pull request %q", f.Name, got, pr2)
		}

		record1, ok := h.pulls.Pull("repo-a", journeyPRNumber(t, pr1))
		if !ok {
			t.Fatalf("%s layer-1 pull request record missing (%s)", f.Name, pr1)
		}
		record2, _ := h.pulls.Pull("repo-a", journeyPRNumber(t, pr2))
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
			return journeyGit(t, h.bareA, "rev-parse", "refs/heads/"+branch)
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
	manualFeature := h.driveStackToFinalBoundary(t, "Stack Publish Manual", 2, true)

	manualDetail := getJourneyJSON(t, h.srv.URL+"/api/v1/features/"+manualFeature.ID)["feature"].(map[string]any)
	if manualDetail["status"] != feature.StatusCodeReady.String() {
		t.Fatalf("manual feature status before publish = %v, want code_ready", manualDetail["status"])
	}
	sourceRevision := h.stackResolutionDetailRevision(t, manualFeature.ID)
	body := fmt.Sprintf(`{"source_revision":%q,"repos":["repo-a"]}`, sourceRevision)
	status, raw := postActionStatus(t, h.srv.URL, manualFeature.ID, "publish", body)
	if status != http.StatusOK || !strings.Contains(string(raw), `"published"`) {
		t.Fatalf("publish action status = %d body = %s, want 200 published", status, raw)
	}
	assertStackPublished(manualFeature)

	// ------------------------------------------------------------------
	// Auto arm: the roadmap-final tail publishes without any action.
	// ------------------------------------------------------------------
	autoFeature := h.driveStackToFinalBoundary(t, "Stack Publish Auto", 2, false)
	deadline := time.Now().Add(60 * time.Second)
	for {
		ff, err := h.mgr.Get(autoFeature.ID)
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
	if got := h.pulls.CreatedCount("repo-a"); got != 4 {
		t.Fatalf("repo-a created pull requests = %d, want 4 (two per feature)", got)
	}
}

// stackResolutionHarness bundles the stack publish journey fixture: repo-a
// with a bare origin, the in-test GitHub fake with its pull store, the
// feature store and manager, the lifecycle and publish orchestrators, and
// the REST server. TestStackPublishJourney and the closed-pull-request
// resolution journeys share it; every journey drives its own features, so
// harness state is per-test only.
type stackResolutionHarness struct {
	cfg      *config.Config
	stateDir string
	repoA    string
	bareA    string

	pulls *testutil.FakePullStore

	store *feature.Store
	mgr   *feature.Manager
	pr    *agent.PhaseRunner

	lifecycleOrch *orchestrator.Orchestrator
	publishOrch   *orchestrator.Orchestrator
	srv           *httptest.Server
}

// newStackResolutionHarness builds the shared stack publish fixture. The
// bare origin's directory name parses as a git@localhost:acme scp-style
// remote, so the production origin resolution reads host/owner/repo identity
// from a remote that never leaves the test machine while pull requests are
// served by the in-test GitHub fake.
func newStackResolutionHarness(t *testing.T) *stackResolutionHarness {
	t.Helper()
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	wtBaseDir := filepath.Join(tmp, "worktrees")
	remotesDir := filepath.Join(tmp, "remotes")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

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
	t.Cleanup(func() { close(stopForwarding) })
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

	return &stackResolutionHarness{
		cfg:           cfg,
		stateDir:      stateDir,
		repoA:         repoA,
		bareA:         bareA,
		pulls:         pulls,
		store:         store,
		mgr:           mgr,
		pr:            pr,
		lifecycleOrch: lifecycleOrch,
		publishOrch:   publishOrch,
		srv:           srv,
	}
}

// driveStackToFinalBoundary creates one feature, approves its roadmap, and
// drives every roadmap phase across the layer boundaries. layers selects the
// approved Pull Requests table's shape (1, 2, or 3 layers); manual selects
// the manual-publish checkpoint. The final phase's completion runs on the
// publish orchestrator so Final Review and the publish tail share its
// PhaseRunner.
func (h *stackResolutionHarness) driveStackToFinalBoundary(t *testing.T, name string, layers int, manual bool) *feature.Feature {
	t.Helper()
	f, err := h.mgr.Create(name, fmt.Sprintf("publishes a %d-layer stack", layers), []string{"repo-a"},
		h.cfg.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if err := h.mgr.RunSetup(f.ID); err != nil {
		t.Fatalf("run setup %s: %v", name, err)
	}
	roadmap, phaseCount, phaseContents := stackResolutionRoadmap(t, layers)
	roadmapDir := filepath.Join(h.stateDir, f.ID, "runs", "run-001", "roadmap")
	if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir roadmap: %v", err)
	}
	roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
	if err := os.WriteFile(roadmapPath, []byte(roadmap), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}
	planGate := feature.PhasePlan
	if err := h.store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPlanNeedsReview
		ff.PendingReviewPhase = &planGate
		ff.Artifacts = map[string]string{"roadmap": roadmapPath}
		return nil
	}); err != nil {
		t.Fatalf("seed roadmap gate: %v", err)
	}
	if err := h.lifecycleOrch.HandleReviewDecision(f.ID, orchestrator.ReviewDecision{
		Decision: "proceed",
		Roadmap:  true,
	}); err != nil {
		t.Fatalf("HandleReviewDecision: %v", err)
	}

	worktreeFor := func() string {
		t.Helper()
		ff, err := h.mgr.Get(f.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		return ff.Repos[0].WorktreePath
	}
	startImplementing := func(phase int) {
		t.Helper()
		if err := h.store.Modify(f.ID, func(ff *feature.Feature) error {
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

	for phase := 1; phase <= phaseCount; phase++ {
		startImplementing(phase)
		testutil.CommitFile(t, worktreeFor(), fmt.Sprintf("phase-%d.txt", phase), phaseContents[phase-1], fmt.Sprintf("phase %d", phase))
		if phase == phaseCount {
			completePhaseOn(h.publishOrch)
		} else {
			completePhaseOn(h.lifecycleOrch)
		}
	}
	return f
}

// stackResolutionRoadmap renders the approved roadmap for a stack of the
// given layer count: the roadmap text, the phase count to drive, and each
// phase's commit content. The two-layer roadmap is byte-for-byte the
// historical fixture's roadmap, so TestStackPublishJourney's delivered shape
// is unchanged; the one- and three-layer roadmaps follow the same Pull
// Requests table contract for the resolution journeys.
func stackResolutionRoadmap(t *testing.T, layers int) (string, int, []string) {
	t.Helper()
	switch layers {
	case 1:
		roadmap := "# Roadmap\n\n## Phase 1: Bootstrap\n### Goal\nInit\n\n" +
			"## Pull Requests\n\n| # | Title | Phases | Rationale |\n|---|---|---|---|\n" +
			"| 1 | Bootstrap | 1 | Stands alone. |\n"
		return roadmap, 1, []string{"layer 1 work\n"}
	case 2:
		roadmap := "# Roadmap\n\n## Phase 1: Bootstrap\n### Goal\nInit\n\n## Phase 2: Build\n### Goal\nBuild\n\n## Phase 3: Polish\n### Goal\nPolish\n\n" +
			"## Pull Requests\n\n| # | Title | Phases | Rationale |\n|---|---|---|---|\n" +
			"| 1 | Bootstrap | 1-2 | Stands alone. |\n| 2 | Build and polish | 3 | Stands alone. |\n"
		return roadmap, 3, []string{"layer 1 work\n", "more layer 1 work\n", "layer 2 work\n"}
	case 3:
		roadmap := "# Roadmap\n\n## Phase 1: Bootstrap\n### Goal\nInit\n\n## Phase 2: Build\n### Goal\nBuild\n\n## Phase 3: Ship\n### Goal\nShip\n\n## Phase 4: Polish\n### Goal\nPolish\n\n" +
			"## Pull Requests\n\n| # | Title | Phases | Rationale |\n|---|---|---|---|\n" +
			"| 1 | Bootstrap | 1-2 | Stands alone. |\n| 2 | Ship | 3 | Stands alone. |\n| 3 | Polish | 4 | Stands alone. |\n"
		return roadmap, 4, []string{"layer 1 work\n", "more layer 1 work\n", "layer 2 work\n", "layer 3 work\n"}
	default:
		t.Fatalf("no stack resolution roadmap for %d layers", layers)
		return "", 0, nil
	}
}

// stackResolutionLayerTitles returns the Pull Requests table titles in
// position order for the supported layer counts.
func stackResolutionLayerTitles(layers int) []string {
	switch layers {
	case 1:
		return []string{"Bootstrap"}
	case 2:
		return []string{"Bootstrap", "Build and polish"}
	case 3:
		return []string{"Bootstrap", "Ship", "Polish"}
	default:
		return nil
	}
}

// stackResolutionLayerBranches derives the layer branch names for one
// feature's stack from the table titles, matching the names roadmap
// approval records on the layers.
func stackResolutionLayerBranches(f *feature.Feature, layers int) []string {
	workspaceSlug := feature.WorkspaceSlug(f.Slug, f.ID)
	titles := stackResolutionLayerTitles(layers)
	branches := make([]string, len(titles))
	for i, title := range titles {
		branches[i] = git.LayerBranchName(workspaceSlug, i+1, feature.Slugify(title))
	}
	return branches
}

// TestStackPublishClosedPRResolutionJourneys covers the closed lower pull
// request's two resolutions end to end on the stack publish fixture. Every
// journey drives its own feature through the same lifecycle as
// TestStackPublishJourney and acts only through the REST surface:
//
//   - ReopenThreeLayer: a three-layer stack whose layer 2 pull request was
//     closed externally stops the next Publish with the closed record stored
//     on the repository, Reopen restores the pull request and clears the
//     record, and a later Publish delivers a locally rewritten layer 2
//     again.
//   - ReopenSingleLayer: a one-layer feature whose only pull request was
//     closed resolves through Reopen the same way.
//   - Recreate: Recreate opens a fresh pull request for the closed layer on
//     the lower layer's branch, reusing the closed body with the harness
//     sections re-injected and rewiring the sibling pull requests, while the
//     closed pull request is left untouched.
//   - DeletedBranchRecovery: with the layer branch gone from the remote,
//     Reopen refuses with the head-branch-missing code whose only action is
//     Recreate, and Recreate pushes the branch again and succeeds.
//   - CompletionPreflightParking: the completion preflight parks the closed
//     record after an external close, and Reopen clears it.
func TestStackPublishClosedPRResolutionJourneys(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}
	h := newStackResolutionHarness(t)
	t.Run("ReopenThreeLayer", func(t *testing.T) {
		h.stackResolutionReopenJourney(t, "Stack Reopen Three Layer", 3, true)
	})
	t.Run("ReopenSingleLayer", func(t *testing.T) {
		h.stackResolutionReopenJourney(t, "Stack Reopen Single Layer", 1, false)
	})
	t.Run("Recreate", func(t *testing.T) {
		h.stackResolutionRecreateJourney(t)
	})
	t.Run("DeletedBranchRecovery", func(t *testing.T) {
		h.stackResolutionDeletedBranchJourney(t)
	})
	t.Run("CompletionPreflightParking", func(t *testing.T) {
		h.stackResolutionPreflightParkingJourney(t)
	})
}

// stackResolutionReopenJourney drives the Reopen resolution: publish the
// stack, close the target layer's pull request externally, refuse a
// following Publish, reopen through REST, and — when republish is set —
// land a Final Review fix on the closed layer and verify a following
// Publish delivers it again. The closed layer is layer 2 on stacks of two
// or more layers and the only layer on single-layer features.
func (h *stackResolutionHarness) stackResolutionReopenJourney(t *testing.T, name string, layers int, republish bool) {
	t.Helper()
	f := h.driveStackToFinalBoundary(t, name, layers, true)
	h.stackResolutionPublishStack(t, f)
	h.stackResolutionAssertPublished(t, f, layers)

	closedPosition := layers
	if closedPosition > 2 {
		closedPosition = 2
	}
	closedTitle := stackResolutionLayerTitles(layers)[closedPosition-1]
	prURL := stackResolutionLayerEntry(t, h.stackResolutionReload(t, f.ID), closedPosition).PRURL
	prNumber := journeyPRNumber(t, prURL)
	if !h.pulls.MarkClosed("repo-a", prNumber) {
		t.Fatalf("marking %s closed: pull request not found in the fake store", prURL)
	}

	// Publish stops on the closed pull request. The action's wire answer is
	// the raw refusal — a 400 bad_request carrying the closed pull request
	// in diagnostics, because the target's publish mapping classifies only
	// remote-diverged and remote-changed refusals onto the 409 conflict
	// envelope — while the canonical closed code, its repository context,
	// and both resolution actions reach the client through the
	// repository's stored record.
	status, raw := postActionStatus(t, h.srv.URL, f.ID, "publish",
		fmt.Sprintf(`{"source_revision":%q,"repos":["repo-a"]}`, h.stackResolutionDetailRevision(t, f.ID)))
	h.stackResolutionAssertPublishClosedRefusal(t, status, raw, prURL)
	h.stackResolutionAssertStoredRecord(t, f.ID, errcat.PublishStackPullRequestClosed, closedPosition, closedTitle, prURL,
		[]string{"reopen-pull-request", "recreate-pull-request"})

	// The desktop refreshes the completion preflight after a refused
	// publish; the refresh records the closed state on the layer entry,
	// which is what enables Reopen in the action catalog.
	revision, preflightRepo := h.stackResolutionPreflightRepo(t, f.ID)
	if got := stackResolutionPreflightPullRequest(t, preflightRepo, closedPosition)["state"]; got != "closed" {
		t.Fatalf("completion preflight layer %d state = %v, want closed", closedPosition, got)
	}
	if e := stackResolutionRepoError(preflightRepo); e == nil || e["code"] != string(errcat.PublishStackPullRequestClosed) {
		t.Fatalf("completion preflight repo error = %+v, want the parked closed record", preflightRepo["error"])
	}
	if a := h.stackResolutionCatalogAction(t, f.ID, "reopen-pull-request"); a["enabled"] != true {
		t.Fatalf("reopen action after the close = %+v, want enabled", a)
	}

	h.stackResolutionResolve(t, f.ID, "reopen-pull-request", closedPosition, revision, "reopened")

	// The pull request reads open, the repository stores no record, the
	// read model lists the layer open, and the catalog no longer enables
	// reopen.
	record, _ := h.pulls.Pull("repo-a", prNumber)
	if record.State != "open" {
		t.Fatalf("pull request %d state = %q, want open", prNumber, record.State)
	}
	h.stackResolutionAssertRecordCleared(t, f.ID)
	repoStatus := h.stackResolutionRepoStatus(t, f.ID)
	if got := stackResolutionRepoStatusPullRequest(t, repoStatus, closedPosition)["state"]; got != "open" {
		t.Fatalf("read model layer %d state = %v, want open", closedPosition, got)
	}
	reopenAction := h.stackResolutionCatalogAction(t, f.ID, "reopen-pull-request")
	if reopenAction["enabled"] != false {
		t.Fatalf("reopen action after the resolution = %+v, want disabled", reopenAction)
	}
	reasons, _ := reopenAction["disabled_reasons"].([]any)
	if len(reasons) == 0 {
		t.Fatalf("reopen action carries no disabled reason: %+v", reopenAction)
	}
	firstReason, _ := reasons[0].(map[string]any)
	if firstReason["code"] != "no_closed_pull_request" {
		t.Fatalf("reopen disabled reason = %+v, want no_closed_pull_request", reasons[0])
	}

	if !republish {
		return
	}

	// A Final Review fix round lands on the closed layer through the
	// round-commit hook, and a following Publish delivers the rewritten
	// layer under its push lease.
	fixTip := h.stackResolutionFixLayer(t, f, closedPosition, "l2fix.txt", "layer 2 reopened fix\n")
	ff := h.stackResolutionReload(t, f.ID)
	entry := stackResolutionLayerEntry(t, ff, closedPosition)
	if entry.TipSHA != fixTip || entry.LastPushedSHA == fixTip {
		t.Fatalf("layer %d entry after the fix = %+v, want tip %s beyond the pushed SHA", closedPosition, entry, fixTip)
	}
	h.stackResolutionPublishStack(t, f)

	ff = h.stackResolutionReload(t, f.ID)
	entry = stackResolutionLayerEntry(t, ff, closedPosition)
	branches := stackResolutionLayerBranches(ff, layers)
	if entry.LastPushedSHA != fixTip {
		t.Fatalf("layer %d last-pushed SHA = %s, want the fix tip %s", closedPosition, entry.LastPushedSHA, fixTip)
	}
	if got := journeyGit(t, h.bareA, "rev-parse", "refs/heads/"+branches[closedPosition-1]); got != fixTip {
		t.Fatalf("remote branch %s = %s, want the fix tip %s", branches[closedPosition-1], got, fixTip)
	}
	h.stackResolutionAssertRecordCleared(t, f.ID)
}

// stackResolutionRecreateJourney drives the Recreate resolution on a
// three-layer stack: the closed layer 2 pull request is replaced by a fresh
// one on the same branch and the lower layer's base, the sibling pull
// requests' stack sections list the new link, and the closed pull request
// is left untouched.
func (h *stackResolutionHarness) stackResolutionRecreateJourney(t *testing.T) {
	t.Helper()
	const layers = 3
	f := h.driveStackToFinalBoundary(t, "Stack Recreate Journey", layers, true)
	h.stackResolutionPublishStack(t, f)
	h.stackResolutionAssertPublished(t, f, layers)

	ff := h.stackResolutionReload(t, f.ID)
	oldURL := stackResolutionLayerEntry(t, ff, 2).PRURL
	oldNumber := journeyPRNumber(t, oldURL)
	if !h.pulls.MarkClosed("repo-a", oldNumber) {
		t.Fatalf("marking %s closed: pull request not found in the fake store", oldURL)
	}

	// The desktop refresh parks the closed record and offers Recreate.
	revision, preflightRepo := h.stackResolutionPreflightRepo(t, f.ID)
	if got := stackResolutionPreflightPullRequest(t, preflightRepo, 2)["state"]; got != "closed" {
		t.Fatalf("completion preflight layer 2 state = %v, want closed", got)
	}
	h.stackResolutionAssertStoredRecord(t, f.ID, errcat.PublishStackPullRequestClosed, 2, "Ship", oldURL,
		[]string{"reopen-pull-request", "recreate-pull-request"})
	if a := h.stackResolutionCatalogAction(t, f.ID, "recreate-pull-request"); a["enabled"] != true {
		t.Fatalf("recreate action after the close = %+v, want enabled", a)
	}

	h.stackResolutionResolve(t, f.ID, "recreate-pull-request", 2, revision, "recreated")

	ff = h.stackResolutionReload(t, f.ID)
	entry := stackResolutionLayerEntry(t, ff, 2)
	newURL := entry.PRURL
	newNumber := journeyPRNumber(t, newURL)
	if newURL == oldURL || newNumber <= oldNumber {
		t.Fatalf("layer 2 pull request after recreate = %s, want a fresh one after %s", newURL, oldURL)
	}
	if entry.PRState != feature.StackPRStateOpen {
		t.Fatalf("layer 2 entry state = %q, want open", entry.PRState)
	}

	branches := stackResolutionLayerBranches(ff, layers)
	record, ok := h.pulls.Pull("repo-a", newNumber)
	if !ok {
		t.Fatalf("recreated pull request %d missing from the fake store", newNumber)
	}
	if record.Title != "Ship" || record.Head != branches[1] || record.Base != branches[0] {
		t.Fatalf("recreated pull request = %+v, want Ship on %s based on %s", record, branches[1], branches[0])
	}
	// The body is the closed pull request's body with the harness sections
	// stripped and re-injected: the scripted description survives and the
	// stack section names the new pull request, not the old one.
	if !strings.Contains(record.Body, "Scripted PR body for Ship.") {
		t.Fatalf("recreated body does not reuse the closed body:\n%s", record.Body)
	}
	wantLink := fmt.Sprintf("2. Ship - [#%d](%s) (open)", newNumber, newURL)
	if !strings.Contains(record.Body, "## Stack") || !strings.Contains(record.Body, wantLink) {
		t.Fatalf("recreated body lacks the stack section naming the new pull request:\n%s", record.Body)
	}
	if strings.Contains(record.Body, oldURL) {
		t.Fatalf("recreated body still links the closed pull request:\n%s", record.Body)
	}

	// The old pull request stays closed and untouched.
	oldRecord, _ := h.pulls.Pull("repo-a", oldNumber)
	if oldRecord.State != "closed" {
		t.Fatalf("closed pull request %d state = %q, want closed", oldNumber, oldRecord.State)
	}

	// The sibling layer's pull request body lists the new link in its stack
	// section.
	topURL := stackResolutionLayerEntry(t, ff, 3).PRURL
	topRecord, _ := h.pulls.Pull("repo-a", journeyPRNumber(t, topURL))
	if !strings.Contains(topRecord.Body, wantLink) {
		t.Fatalf("layer 3 body does not list the recreated link:\n%s", topRecord.Body)
	}
	if strings.Contains(topRecord.Body, oldURL) {
		t.Fatalf("layer 3 body still links the closed pull request:\n%s", topRecord.Body)
	}

	// The entry records the new URL open at the pushed tip and the
	// repository stores no record.
	h.stackResolutionAssertRecordCleared(t, f.ID)
	if entry.LastPushedSHA != entry.TipSHA {
		t.Fatalf("layer 2 pushed SHA = %s, want the unchanged tip %s", entry.LastPushedSHA, entry.TipSHA)
	}
	if got := journeyGit(t, h.bareA, "rev-parse", "refs/heads/"+branches[1]); got != entry.TipSHA {
		t.Fatalf("remote branch %s = %s, want the layer tip %s", branches[1], got, entry.TipSHA)
	}
}

// stackResolutionDeletedBranchJourney drives the deleted-head-branch
// recovery on a three-layer stack: with layer 2's remote branch gone and the
// pull request's head marked deleted, Reopen refuses with the
// head-branch-missing code whose only action is Recreate, and Recreate
// pushes the branch again and opens the replacement pull request.
func (h *stackResolutionHarness) stackResolutionDeletedBranchJourney(t *testing.T) {
	t.Helper()
	const layers = 3
	f := h.driveStackToFinalBoundary(t, "Stack Deleted Branch Journey", layers, true)
	h.stackResolutionPublishStack(t, f)
	h.stackResolutionAssertPublished(t, f, layers)

	ff := h.stackResolutionReload(t, f.ID)
	oldURL := stackResolutionLayerEntry(t, ff, 2).PRURL
	oldNumber := journeyPRNumber(t, oldURL)
	branches := stackResolutionLayerBranches(ff, layers)
	if !h.pulls.MarkClosed("repo-a", oldNumber) {
		t.Fatalf("marking %s closed: pull request not found in the fake store", oldURL)
	}
	journeyGit(t, h.repoA, "push", "origin", "--delete", branches[1])
	h.pulls.MarkHeadDeleted("repo-a", oldNumber)

	// The desktop refresh parks the closed record; Reopen then refuses with
	// the head-branch-missing code whose action list is recreate only.
	revision, _ := h.stackResolutionPreflightRepo(t, f.ID)
	status, raw := postActionStatus(t, h.srv.URL, f.ID, "reopen-pull-request",
		fmt.Sprintf(`{"source_revision":%q,"repository":"repo-a","layer":2}`, revision))
	if status != http.StatusConflict {
		t.Fatalf("reopen over a deleted head branch status = %d body = %s, want 409", status, raw)
	}
	conflict := stackResolutionWireError(t, raw)
	if conflict["code"] != string(errcat.PublishHeadBranchMissing) {
		t.Fatalf("reopen conflict code = %v, want %s", conflict["code"], errcat.PublishHeadBranchMissing)
	}
	actions := stackResolutionWireRemediationActions(t, conflict)
	if len(actions) != 1 || actions[0] != "recreate-pull-request" {
		t.Fatalf("reopen conflict actions = %v, want recreate only", actions)
	}
	repo := stackResolutionWireRepoContext(t, conflict)
	if repo["name"] != "repo-a" ||
		stackResolutionWireInt(t, repo["layer_position"]) != 2 ||
		repo["layer_title"] != "Ship" ||
		repo["pull_request_url"] != oldURL {
		t.Fatalf("reopen conflict repository context = %+v, want repo-a layer 2 (Ship) naming %s", repo, oldURL)
	}
	// The refusal stored the head-branch-missing record on the repository.
	h.stackResolutionAssertStoredRecord(t, f.ID, errcat.PublishHeadBranchMissing, 2, "Ship", oldURL,
		[]string{"recreate-pull-request"})

	h.stackResolutionResolve(t, f.ID, "recreate-pull-request", 2, revision, "recreated")

	// The remote branch is restored at the layer tip and the replacement
	// pull request is open.
	ff = h.stackResolutionReload(t, f.ID)
	entry := stackResolutionLayerEntry(t, ff, 2)
	if entry.PRURL == oldURL || entry.PRState != feature.StackPRStateOpen {
		t.Fatalf("layer 2 entry after recreate = %+v, want a fresh open pull request", entry)
	}
	newRecord, ok := h.pulls.Pull("repo-a", journeyPRNumber(t, entry.PRURL))
	if !ok || newRecord.State != "open" {
		t.Fatalf("recreated pull request = %+v, want open", newRecord)
	}
	if got := journeyGit(t, h.bareA, "rev-parse", "refs/heads/"+branches[1]); got != entry.TipSHA {
		t.Fatalf("remote branch %s = %s, want the restored layer tip %s", branches[1], got, entry.TipSHA)
	}
	h.stackResolutionAssertRecordCleared(t, f.ID)
}

// stackResolutionPreflightParkingJourney drives the completion preflight's
// parking of the closed record: an external close is observed by the
// preflight, which records the layer closed and parks the closed record on
// the repository — once, not per refresh — and Reopen clears it.
func (h *stackResolutionHarness) stackResolutionPreflightParkingJourney(t *testing.T) {
	t.Helper()
	const layers = 3
	f := h.driveStackToFinalBoundary(t, "Stack Preflight Parking Journey", layers, true)
	h.stackResolutionPublishStack(t, f)
	h.stackResolutionAssertPublished(t, f, layers)

	prURL := stackResolutionLayerEntry(t, h.stackResolutionReload(t, f.ID), 2).PRURL
	if !h.pulls.MarkClosed("repo-a", journeyPRNumber(t, prURL)) {
		t.Fatalf("marking %s closed: pull request not found in the fake store", prURL)
	}

	// The completion preflight observes the close, records the layer
	// closed, and parks the closed record on the repository; the read model
	// enables Reopen on the parked record.
	revision, preflightRepo := h.stackResolutionPreflightRepo(t, f.ID)
	if got := stackResolutionPreflightPullRequest(t, preflightRepo, 2)["state"]; got != "closed" {
		t.Fatalf("completion preflight layer 2 state = %v, want closed", got)
	}
	if e := stackResolutionRepoError(preflightRepo); e == nil || e["code"] != string(errcat.PublishStackPullRequestClosed) {
		t.Fatalf("completion preflight repo error = %+v, want the parked closed record", preflightRepo["error"])
	}
	// A second preflight leaves the same record parked.
	_, again := h.stackResolutionPreflightRepo(t, f.ID)
	if e := stackResolutionRepoError(again); e == nil || e["code"] != string(errcat.PublishStackPullRequestClosed) {
		t.Fatalf("second completion preflight repo error = %+v, want the same parked record", again["error"])
	}
	h.stackResolutionAssertStoredRecord(t, f.ID, errcat.PublishStackPullRequestClosed, 2, "Ship", prURL,
		[]string{"reopen-pull-request", "recreate-pull-request"})
	if a := h.stackResolutionCatalogAction(t, f.ID, "reopen-pull-request"); a["enabled"] != true {
		t.Fatalf("reopen action after the parked record = %+v, want enabled", a)
	}

	h.stackResolutionResolve(t, f.ID, "reopen-pull-request", 2, revision, "reopened")

	h.stackResolutionAssertRecordCleared(t, f.ID)
	if record, _ := h.pulls.Pull("repo-a", journeyPRNumber(t, prURL)); record.State != "open" {
		t.Fatalf("pull request %d state = %q, want open", journeyPRNumber(t, prURL), record.State)
	}
}

// --- shared journey helpers ---------------------------------------------------

// stackResolutionLayerEntry returns one layer's repo-a stack entry.
func stackResolutionLayerEntry(t *testing.T, ff *feature.Feature, position int) feature.StackRepoEntry {
	t.Helper()
	for _, layer := range ff.Stack {
		if layer.Position == position {
			return layer.Repos["repo-a"]
		}
	}
	t.Fatalf("feature %s has no stack layer at position %d", ff.ID, position)
	return feature.StackRepoEntry{}
}

// stackResolutionReload reads one feature back from the store.
func (h *stackResolutionHarness) stackResolutionReload(t *testing.T, featureID string) *feature.Feature {
	t.Helper()
	ff, err := h.mgr.Get(featureID)
	if err != nil {
		t.Fatalf("get %s: %v", featureID, err)
	}
	return ff
}

// stackResolutionDetailRevision reads the feature detail's meta revision.
func (h *stackResolutionHarness) stackResolutionDetailRevision(t *testing.T, featureID string) string {
	t.Helper()
	out := getJourneyJSON(t, h.srv.URL+"/api/v1/features/"+featureID)
	if meta, ok := out["meta"].(map[string]any); ok {
		if rev, ok := meta["revision"].(string); ok {
			return rev
		}
	}
	return ""
}

// stackResolutionPublishStack delivers one feature's stack through the REST
// publish action.
func (h *stackResolutionHarness) stackResolutionPublishStack(t *testing.T, f *feature.Feature) {
	t.Helper()
	body := fmt.Sprintf(`{"source_revision":%q,"repos":["repo-a"]}`, h.stackResolutionDetailRevision(t, f.ID))
	status, raw := postActionStatus(t, h.srv.URL, f.ID, "publish", body)
	if status != http.StatusOK || !strings.Contains(string(raw), `"published"`) {
		t.Fatalf("publish action status = %d body = %s, want 200 published", status, raw)
	}
}

// stackResolutionResolve dispatches one reopen or recreate action over REST
// with the completion preflight's source revision, asserting success.
func (h *stackResolutionHarness) stackResolutionResolve(t *testing.T, featureID, action string, layer int, sourceRevision, wantResult string) {
	t.Helper()
	body := fmt.Sprintf(`{"source_revision":%q,"repository":"repo-a","layer":%d}`, sourceRevision, layer)
	status, raw := postActionStatus(t, h.srv.URL, featureID, action, body)
	if status != http.StatusOK || !strings.Contains(string(raw), `"`+wantResult+`"`) {
		t.Fatalf("%s action status = %d body = %s, want 200 %s", action, status, raw, wantResult)
	}
}

// stackResolutionAssertPublished pins the delivered shape for one feature's
// whole stack: one pull request per layer in ascending order with the table
// titles and bases, scripted bodies with the stack section naming the top
// layer as current (single-layer deliveries carry no stack section), remote
// branches at the recorded tips, and the published status.
func (h *stackResolutionHarness) stackResolutionAssertPublished(t *testing.T, f *feature.Feature, layers int) {
	t.Helper()
	titles := stackResolutionLayerTitles(layers)

	ff := h.stackResolutionReload(t, f.ID)
	if ff.Status != feature.StatusPublished {
		t.Fatalf("%s status = %v, want Published", f.Name, ff.Status)
	}
	branches := stackResolutionLayerBranches(ff, layers)
	urls := make([]string, layers)
	numbers := make([]int, layers)
	for position := 1; position <= layers; position++ {
		urls[position-1] = stackResolutionLayerEntry(t, ff, position).PRURL
		if urls[position-1] == "" {
			t.Fatalf("%s layer %d pull request URL is empty", f.Name, position)
		}
		numbers[position-1] = journeyPRNumber(t, urls[position-1])
	}
	if got := ff.TopStackLayerPRURL("repo-a"); got != urls[layers-1] {
		t.Fatalf("%s top-layer pull request URL = %q, want %q", f.Name, got, urls[layers-1])
	}

	for i := 0; i < layers; i++ {
		record, ok := h.pulls.Pull("repo-a", numbers[i])
		if !ok {
			t.Fatalf("%s layer %d pull request record missing (%s)", f.Name, i+1, urls[i])
		}
		wantBase := "main"
		if i > 0 {
			wantBase = branches[i-1]
		}
		if record.Title != titles[i] || record.Head != branches[i] || record.Base != wantBase {
			t.Fatalf("%s layer %d pull request = %+v, want %s on %s based on %s",
				f.Name, i+1, record, titles[i], branches[i], wantBase)
		}
		if !strings.Contains(record.Body, "Scripted PR body for "+titles[i]+".") {
			t.Fatalf("%s layer %d body is not the scripted session body:\n%s", f.Name, i+1, record.Body)
		}
		if layers > 1 {
			wantTop := fmt.Sprintf("%d. %s - [#%d](%s) (open) (this pull request)", layers, titles[layers-1], numbers[layers-1], urls[layers-1])
			if !strings.Contains(record.Body, "## Stack") || !strings.Contains(record.Body, wantTop) {
				t.Fatalf("%s layer %d body lacks the stack section naming the top layer:\n%s", f.Name, i+1, record.Body)
			}
		}
	}
	for i := 0; i < layers; i++ {
		entry := stackResolutionLayerEntry(t, ff, i+1)
		if got := journeyGit(t, h.bareA, "rev-parse", "refs/heads/"+branches[i]); got != entry.LastPushedSHA {
			t.Fatalf("%s layer %d last-pushed SHA = %s, want the remote tip %s", f.Name, i+1, entry.LastPushedSHA, got)
		}
	}
}

// stackResolutionAssertPublishClosedRefusal pins the publish action's
// on-the-wire refusal for a closed stack pull request. The mutation target
// — like the production target's publish mapping, which classifies only
// remote-diverged and remote-changed refusals as conflicts — returns the
// raw orchestrator error, so the action answers 400 bad_request with the
// closed pull request named in diagnostics; the canonical closed code and
// its repository context reach the client through the repository's stored
// record, which stackResolutionAssertStoredRecord pins.
func (h *stackResolutionHarness) stackResolutionAssertPublishClosedRefusal(t *testing.T, status int, raw []byte, prURL string) {
	t.Helper()
	if status != http.StatusBadRequest {
		t.Fatalf("publish over a closed pull request status = %d body = %s, want 400 (the raw closed refusal; the canonical record reaches the client through the repository's stored record)", status, raw)
	}
	wireErr := stackResolutionWireError(t, raw)
	if wireErr["code"] != string(errcat.BadRequest) {
		t.Fatalf("publish refusal code = %v, want bad_request", wireErr["code"])
	}
	diagnostics, _ := wireErr["diagnostics"].(string)
	if !strings.Contains(diagnostics, prURL) || !strings.Contains(diagnostics, "closed") {
		t.Fatalf("publish refusal diagnostics = %q, want the closed pull request %s named", diagnostics, prURL)
	}
}

// stackResolutionAssertStoredRecord pins the repository's stored publish
// failure record and its read-model projection: the code, the repository
// context naming the layer (position, title, pull request URL), and the
// remediation action list the canonical error surface renders.
func (h *stackResolutionHarness) stackResolutionAssertStoredRecord(t *testing.T, featureID string, code errcat.Code, position int, title, prURL string, wantActions []string) {
	t.Helper()
	ff := h.stackResolutionReload(t, featureID)
	state := ff.RepoStates["repo-a"]
	if state == nil || state.Error == nil || state.Error.Code != code {
		t.Fatalf("repo-a stored record = %+v, want %s", stackResolutionStateErr(state), code)
	}
	if state.Error.Context == nil || len(state.Error.Context.Repositories) != 1 {
		t.Fatalf("stored record %s carries no repository block: %+v", code, state.Error)
	}
	block := state.Error.Context.Repositories[0]
	if block.Name != "repo-a" || block.LayerPosition != position || block.LayerTitle != title || block.PullRequestURL != prURL {
		t.Fatalf("stored record block = %+v, want repo-a layer %d (%s) naming %s", block, position, title, prURL)
	}

	repoStatus := h.stackResolutionRepoStatus(t, featureID)
	wireErr, _ := repoStatus["error"].(map[string]any)
	if wireErr == nil {
		t.Fatalf("repo_status for %s carries no error", featureID)
	}
	if wireErr["code"] != string(code) {
		t.Fatalf("repo_status error code = %v, want %s", wireErr["code"], code)
	}
	wireRepo := stackResolutionWireRepoContext(t, wireErr)
	if wireRepo["name"] != "repo-a" ||
		stackResolutionWireInt(t, wireRepo["layer_position"]) != position ||
		wireRepo["layer_title"] != title ||
		wireRepo["pull_request_url"] != prURL {
		t.Fatalf("repo_status repository context = %+v, want repo-a layer %d (%s) naming %s", wireRepo, position, title, prURL)
	}
	wireActions := stackResolutionWireRemediationActions(t, wireErr)
	if len(wireActions) != len(wantActions) {
		t.Fatalf("repo_status remediation actions = %v, want %v", wireActions, wantActions)
	}
	for i := range wantActions {
		if wireActions[i] != wantActions[i] {
			t.Fatalf("repo_status remediation actions = %v, want %v", wireActions, wantActions)
		}
	}
}

// stackResolutionAssertRecordCleared pins that the repository stores no
// publish failure record and the read model reports none.
func (h *stackResolutionHarness) stackResolutionAssertRecordCleared(t *testing.T, featureID string) {
	t.Helper()
	ff := h.stackResolutionReload(t, featureID)
	if state := ff.RepoStates["repo-a"]; state != nil && state.Error != nil {
		t.Fatalf("repo-a stored record = %+v, want cleared", state.Error)
	}
	if repoStatus := h.stackResolutionRepoStatus(t, featureID); repoStatus["error"] != nil {
		t.Fatalf("repo_status error = %+v, want none", repoStatus["error"])
	}
}

// stackResolutionFixLayer lands one Final Review fix round on a stack layer
// through the orchestrator's round-commit hook: the worktree is dirtied, the
// fix manifest names the target layer, and the hook commits and relocates
// the fix onto the layer's branch. It returns the layer's new recorded tip.
func (h *stackResolutionHarness) stackResolutionFixLayer(t *testing.T, f *feature.Feature, position int, file, content string) string {
	t.Helper()
	ff := h.stackResolutionReload(t, f.ID)
	worktree := ff.Repos[0].WorktreePath
	writeJourneyFile(t, worktree, file, content)
	iterDir := filepath.Join(h.stateDir, f.ID, "final-review", "iterations", "001")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatalf("mkdir iteration dir: %v", err)
	}
	manifest := "entries:\n" +
		fmt.Sprintf("  - layer: %d\n", position) +
		"    repository: repo-a\n" +
		"    paths:\n" +
		fmt.Sprintf("      - %s\n", file)
	if err := os.WriteFile(filepath.Join(iterDir, agent.FixManifestFilename), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write fix manifest: %v", err)
	}
	if h.pr == nil || h.pr.RoundCommitHook == nil {
		t.Fatal("the orchestrator never wired the round-commit hook")
	}
	if err := h.pr.RoundCommitHook(agent.RoundCommitInput{
		FeatureID:       f.ID,
		Iteration:       1,
		Kind:            agent.RoundCommitFinalReviewFix,
		FixNumber:       1,
		FixIterationDir: iterDir,
		IterationDir:    iterDir,
		Repos:           map[string]string{"repo-a": worktree},
	}); err != nil {
		t.Fatalf("round-commit hook: %v", err)
	}
	return stackResolutionLayerEntry(t, h.stackResolutionReload(t, f.ID), position).TipSHA
}

// stackResolutionPreflightRepo reads the completion preflight over REST and
// returns its source revision and repo-a's repository block.
func (h *stackResolutionHarness) stackResolutionPreflightRepo(t *testing.T, featureID string) (string, map[string]any) {
	t.Helper()
	out := getJourneyJSON(t, h.srv.URL+"/api/v1/features/"+featureID+"/completion/preflight")
	revision, _ := out["source_revision"].(string)
	repos, _ := out["repos"].([]any)
	for _, r := range repos {
		repo, ok := r.(map[string]any)
		if ok && repo["repo"] == "repo-a" {
			return revision, repo
		}
	}
	t.Fatalf("completion preflight for %s carries no repo-a block: %+v", featureID, out)
	return "", nil
}

// stackResolutionPreflightPullRequest returns one layer's pull-request entry
// from a completion preflight repository block.
func stackResolutionPreflightPullRequest(t *testing.T, repo map[string]any, position int) map[string]any {
	t.Helper()
	return stackResolutionPullRequestEntry(t, repo["pull_requests"], position, "completion preflight")
}

// stackResolutionRepoStatus returns repo-a's repository status block from
// the feature detail read model.
func (h *stackResolutionHarness) stackResolutionRepoStatus(t *testing.T, featureID string) map[string]any {
	t.Helper()
	body := journeyFeatureBody(h.srv.URL, featureID)
	if body == nil {
		t.Fatalf("feature detail for %s is unreadable", featureID)
	}
	statuses, _ := body["repo_status"].([]any)
	for _, s := range statuses {
		repoStatus, ok := s.(map[string]any)
		if ok && repoStatus["name"] == "repo-a" {
			return repoStatus
		}
	}
	t.Fatalf("feature detail for %s carries no repo-a status: %+v", featureID, body)
	return nil
}

// stackResolutionRepoStatusPullRequest returns one layer's pull-request
// entry from a feature detail repository status block.
func stackResolutionRepoStatusPullRequest(t *testing.T, repoStatus map[string]any, position int) map[string]any {
	t.Helper()
	return stackResolutionPullRequestEntry(t, repoStatus["pull_requests"], position, "feature detail")
}

// stackResolutionPullRequestEntry finds one layer's pull-request entry in a
// decoded pull_requests list.
func stackResolutionPullRequestEntry(t *testing.T, raw any, position int, source string) map[string]any {
	t.Helper()
	entries, _ := raw.([]any)
	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if ok && stackResolutionWireInt(t, entry["position"]) == position {
			return entry
		}
	}
	t.Fatalf("%s carries no pull request entry for layer %d: %+v", source, position, raw)
	return nil
}

// stackResolutionCatalogAction returns one action's catalog entry from the
// feature detail read model.
func (h *stackResolutionHarness) stackResolutionCatalogAction(t *testing.T, featureID, actionID string) map[string]any {
	t.Helper()
	body := journeyFeatureBody(h.srv.URL, featureID)
	if body == nil {
		t.Fatalf("feature detail for %s is unreadable", featureID)
	}
	actions, _ := body["actions"].([]any)
	for _, a := range actions {
		action, ok := a.(map[string]any)
		if ok && action["id"] == actionID {
			return action
		}
	}
	t.Fatalf("feature detail for %s carries no %s action: %+v", featureID, actionID, body)
	return nil
}

// stackResolutionRepoError returns a repository block's canonical error
// object, or nil when the block carries none.
func stackResolutionRepoError(repo map[string]any) map[string]any {
	wireErr, _ := repo["error"].(map[string]any)
	return wireErr
}

// stackResolutionStateErr renders a repository state's stored record for
// failure messages, tolerating a nil state.
func stackResolutionStateErr(state *feature.RepoState) *errcat.FailureRecord {
	if state == nil {
		return nil
	}
	return state.Error
}

// stackResolutionWireError decodes one mutation error envelope's error
// object.
func stackResolutionWireError(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode error body %s: %v", raw, err)
	}
	wireErr, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error body %s carries no error object", raw)
	}
	return wireErr
}

// stackResolutionWireRepoContext returns a wire error's single repository
// context block.
func stackResolutionWireRepoContext(t *testing.T, wireErr map[string]any) map[string]any {
	t.Helper()
	errorContext, _ := wireErr["context"].(map[string]any)
	repos, _ := errorContext["repositories"].([]any)
	if len(repos) != 1 {
		t.Fatalf("wire error repository context = %+v, want exactly one repository", wireErr["context"])
	}
	repo, ok := repos[0].(map[string]any)
	if !ok {
		t.Fatalf("wire error repository context entry = %+v, want an object", repos[0])
	}
	return repo
}

// stackResolutionWireRemediationActions returns a wire error's remediation
// action list.
func stackResolutionWireRemediationActions(t *testing.T, wireErr map[string]any) []string {
	t.Helper()
	remediation, _ := wireErr["remediation"].(map[string]any)
	if remediation == nil {
		t.Fatalf("wire error carries no remediation: %+v", wireErr)
	}
	raw, _ := remediation["actions"].([]any)
	actions := make([]string, 0, len(raw))
	for _, a := range raw {
		if action, ok := a.(string); ok {
			actions = append(actions, action)
		}
	}
	return actions
}

// stackResolutionWireInt decodes one wire JSON number.
func stackResolutionWireInt(t *testing.T, v any) int {
	t.Helper()
	number, ok := v.(float64)
	if !ok {
		t.Fatalf("wire value %v is not a number", v)
	}
	return int(number)
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
