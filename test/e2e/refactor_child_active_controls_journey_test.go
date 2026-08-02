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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestRefactorChildActiveControlsJourney exercises the active-child control
// surface end-to-end through the real API: paired config propagation,
// relationship-guard enforcement on parent mutations, the restricted child
// action catalog, review-session decisions through configured gates, and the
// running-discard state machine that closes the child with outcome
// "discarded" while preserving the parent's paired configuration and
// clearing the active-child projection so a new child can be launched. The
// final step starts a second child and discards it while its session is
// potentially still active, exercising the StopFeatureSessions /
// sessions-quiescence branch of the discard state machine.
func TestRefactorChildActiveControlsJourney(t *testing.T) {
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
	journeyGit(t, repoDir, "checkout", "-b", "feature/active-controls-parent")
	writeJourneyFile(t, repoDir, "base.txt", "v1\n")
	journeyGit(t, repoDir, "add", "base.txt")
	journeyGit(t, repoDir, "commit", "-m", "parent base commit")
	journeyGit(t, repoDir, "push", "-u", "origin", "feature/active-controls-parent")

	store := feature.NewStore(stateDir)
	publishable := true
	parent := &feature.Feature{
		ID:            "active-controls-parent",
		Name:          "Active controls parent",
		Slug:          "active-controls-parent",
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
			Branch:       "feature/active-controls-parent",
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
		Worktrees:   wm,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
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
	// Step 1: Launch a refactor child and drive it to the first review gate.
	// ------------------------------------------------------------------
	resp, err := client.RefactorFeature(t.Context(), parent.ID, server.RefactorFeatureRequest{
		Name:     "Active controls rework",
		Pipeline: feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{
			RoadmapReview:   true,
			PhasePlanReview: true,
			ManualPublish:   true,
		},
	})
	if err != nil {
		t.Fatalf("RefactorFeature() error = %v", err)
	}
	childID := resp.FeatureID
	if childID == "" {
		t.Fatalf("refactor response = %+v; want child id", resp)
	}

	childBody := waitForJourneySetupComplete(t, srv.URL, childID)
	if childBody["status"] != "Created" {
		t.Fatalf("child durable status = %v, want Created (parked after setup)", childBody["status"])
	}

	postAction(t, srv.URL, childID, "start", `{}`)
	waitForJourneyGate(t, srv.URL, childID, 0)

	// ------------------------------------------------------------------
	// Step 2: Active child config — update via the paired config route and
	// verify both parent and child carry identical Review axes.
	// ------------------------------------------------------------------
	const wantInquireness = "high"
	configResp, err := client.UpdateFeatureConfig(t.Context(), childID, server.FeatureConfigMutationRequest{
		Inquireness: wantInquireness,
		Checkpoints: feature.Checkpoints{
			RoadmapReview:   true,
			PhasePlanReview: true,
			ManualPublish:   true,
		},
	})
	if err != nil {
		t.Fatalf("UpdateFeatureConfig() error = %v", err)
	}
	if configResp.Result != "updated" {
		t.Fatalf("UpdateFeatureConfig() result = %v, want updated", configResp.Result)
	}

	parentCfg := getJourneyConfig(t, srv.URL, parent.ID)
	childCfg := getJourneyConfig(t, srv.URL, childID)
	if parentCfg["inquireness"] != wantInquireness {
		t.Fatalf("parent inquireness = %v, want %s", parentCfg["inquireness"], wantInquireness)
	}
	if childCfg["inquireness"] != wantInquireness {
		t.Fatalf("child inquireness = %v, want %s", childCfg["inquireness"], wantInquireness)
	}
	parentCp := parentCfg["checkpoints"].(map[string]any)
	childCp := childCfg["checkpoints"].(map[string]any)
	if parentCp["roadmap_review"] != childCp["roadmap_review"] {
		t.Fatalf("parent/child roadmap_review mismatch: %v vs %v", parentCp["roadmap_review"], childCp["roadmap_review"])
	}
	if parentCp["phase_plan_review"] != childCp["phase_plan_review"] {
		t.Fatalf("parent/child phase_plan_review mismatch: %v vs %v", parentCp["phase_plan_review"], childCp["phase_plan_review"])
	}

	// ------------------------------------------------------------------
	// Step 3: Guard enforcement — parent mutations are rejected with 409
	// while the child is active.
	// ------------------------------------------------------------------
	conflictActions := []struct {
		action string
		body   string
	}{
		{"start", `{}`},
		{"publish", `{}`},
		{"mark-done", `{}`},
		{"cleanup", `{}`},
		{"pause-stop", `{}`},
	}
	for _, ca := range conflictActions {
		status, respBody := postActionStatus(t, srv.URL, parent.ID, ca.action, ca.body)
		if status != http.StatusConflict {
			t.Fatalf("parent %s while child active: status = %d, want %d; body: %s", ca.action, status, http.StatusConflict, respBody)
		}
	}

	// Verify the parent action catalog reflects locked mutations.
	parentDetail := journeyFeatureBody(srv.URL, parent.ID)
	if parentDetail == nil {
		t.Fatalf("parent detail nil")
	}
	actions := parentDetail["actions"].([]any)
	actionByID := map[string]map[string]any{}
	for _, a := range actions {
		am := a.(map[string]any)
		actionByID[am["id"].(string)] = am
	}
	for _, lockedID := range []string{"start", "publish", "mark-done", "cleanup", "refactor"} {
		a, ok := actionByID[lockedID]
		if !ok {
			t.Fatalf("parent catalog missing action %q", lockedID)
		}
		if a["enabled"] != false {
			t.Fatalf("parent action %q enabled = %v, want false while child active", lockedID, a["enabled"])
		}
		reasons, _ := a["disabled_reasons"].([]any)
		if len(reasons) == 0 {
			t.Fatalf("parent action %q has no disabled_reasons", lockedID)
		}
		first := reasons[0].(map[string]any)
		if first["code"] != "active_child_present" {
			t.Fatalf("parent action %q disabled code = %v, want active_child_present", lockedID, first["code"])
		}
	}
	deleteAction, ok := actionByID["delete"]
	if !ok || deleteAction["enabled"] != true {
		t.Fatalf("parent delete action = %+v, want enabled cascade delete", deleteAction)
	}

	// Verify the child catalog does NOT include cleanup or delete.
	childDetail := journeyFeatureBody(srv.URL, childID)
	if childDetail == nil {
		t.Fatalf("child detail nil")
	}
	childActions := childDetail["actions"].([]any)
	childActionIDs := map[string]bool{}
	discardEnabled := false
	for _, a := range childActions {
		am := a.(map[string]any)
		id := am["id"].(string)
		childActionIDs[id] = true
		if id == "discard" {
			discardEnabled, _ = am["enabled"].(bool)
		}
	}
	if childActionIDs["cleanup"] {
		t.Fatalf("child catalog includes cleanup, want excluded")
	}
	if childActionIDs["delete"] {
		t.Fatalf("child catalog includes delete, want excluded")
	}
	if !childActionIDs["discard"] {
		t.Fatalf("child catalog missing discard, want present for active child")
	}
	// Discard must be enabled for an active child even while it is running
	// or at a review gate: the durable discard flow itself records intent,
	// stops sessions, and waits for quiescence. Gating it on "stopped"
	// would hide the primary abandon flow exactly when a running child
	// needs it.
	if !discardEnabled {
		t.Fatalf("child discard action is disabled, want enabled for active child (running children must be discardable)")
	}

	// ------------------------------------------------------------------
	// Step 4: Restart/resume — approve the roadmap gate to continue to the
	// phase-plan review gate.
	// ------------------------------------------------------------------
	postReviewSessionProceed(t, srv.URL, childID)
	waitForJourneyGate(t, srv.URL, childID, 1)

	// ------------------------------------------------------------------
	// Step 5: Running discard — call discard while the child is active at
	// the phase-plan review gate.
	// ------------------------------------------------------------------
	discardJourneyChild(t, srv.URL, childID)

	// Verify the child closed with outcome "discarded".
	discardedDetail := journeyFeatureBody(srv.URL, childID)
	if discardedDetail["close_outcome"] != feature.ChildCloseOutcomeDiscarded {
		t.Fatalf("child close_outcome = %v, want discarded", discardedDetail["close_outcome"])
	}
	if discardedDetail["closed_at"] == nil || discardedDetail["closed_at"] == "" {
		t.Fatalf("child closed_at missing: %+v", discardedDetail)
	}

	// Verify the parent's config remains at the paired config values.
	parentCfgAfter := getJourneyConfig(t, srv.URL, parent.ID)
	if parentCfgAfter["inquireness"] != wantInquireness {
		t.Fatalf("parent inquireness after discard = %v, want %s (paired config preserved)", parentCfgAfter["inquireness"], wantInquireness)
	}

	// Verify the parent's active-child projection is cleared.
	parentDetailAfter := journeyFeatureBody(srv.URL, parent.ID)
	if parentDetailAfter["active_child"] != nil {
		t.Fatalf("parent active_child = %v, want cleared after discard", parentDetailAfter["active_child"])
	}

	// ------------------------------------------------------------------
	// Step 6: The parent can launch a new child after discard.
	// ------------------------------------------------------------------
	resp2, err := client.RefactorFeature(t.Context(), parent.ID, server.RefactorFeatureRequest{
		Name:     "Second rework after discard",
		Pipeline: feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{
			RoadmapReview:   true,
			PhasePlanReview: true,
			ManualPublish:   true,
		},
	})
	if err != nil {
		t.Fatalf("second RefactorFeature() error = %v", err)
	}
	child2ID := resp2.FeatureID
	if child2ID == "" || child2ID == childID {
		t.Fatalf("second refactor response = %+v; want new child id", resp2)
	}
	waitForJourneySetupComplete(t, srv.URL, child2ID)

	// ------------------------------------------------------------------
	// Step 7: Running-session discard — start the second child and
	// immediately discard while its session may still be active. The
	// discardJourneyChild helper polls through the "sessions still
	// draining" response (if any) until the child is durably closed.
	// This exercises the StopFeatureSessions / sessions-quiescence branch
	// of the discard state machine.
	// ------------------------------------------------------------------
	postAction(t, srv.URL, child2ID, "start", `{}`)
	discardJourneyChild(t, srv.URL, child2ID)

	discarded2 := journeyFeatureBody(srv.URL, child2ID)
	if discarded2["close_outcome"] != feature.ChildCloseOutcomeDiscarded {
		t.Fatalf("child2 close_outcome = %v, want discarded", discarded2["close_outcome"])
	}
}

// --- journeyMutationTarget extensions -----------------------------------------

func (t *journeyMutationTarget) DiscardChild(featureID string) (server.DiscardChildResponse, error) {
	resp := server.DiscardChildResponse{FeatureID: featureID, Result: "failed"}
	if err := t.orch.DiscardChild(featureID); err != nil {
		return resp, err
	}
	resp.Result = "discarded"
	return resp, nil
}

// ScanRecovery mirrors the production mutation target so a client cold boot
// (which always pulls the recovery snapshot) sees the real orphan/session
// scan rather than the unimplemented embedded interface.
func (t *journeyMutationTarget) ScanRecovery(ctx context.Context) ([]ports.RecoveryItem, error) {
	return t.orch.ScanRecovery(ctx)
}

// journeyFreshnessProvider mirrors cmd/agentico's git-backed freshness
// provider so the read model can flag a dirty parent (dirty_parent disabled
// reason) the same way production does.
type journeyFreshnessProvider struct{}

func (journeyFreshnessProvider) Freshness(_ *feature.Feature, repo feature.FeatureRepo) server.RepoFreshness {
	worktree := repo.WorktreePath
	if worktree == "" {
		worktree = repo.Path
	}
	switch git.RepoFreshness(worktree) {
	case "in sync":
		return server.RepoFreshnessInSync
	case git.FreshnessLocalChanges:
		return server.RepoFreshnessLocalChanges
	case "local only":
		return server.RepoFreshnessLocalOnly
	default:
		return server.RepoFreshnessUnknown
	}
}

func (t *journeyMutationTarget) StopFeature(featureID string) (server.FeatureStopResponse, error) {
	if err := t.orch.WithRelationshipReadLock(func() error {
		if err := t.orch.RelationshipGuard(featureID, orchestrator.MutationStop); err != nil {
			return err
		}
		return t.orch.InterruptFeature(featureID)
	}); err != nil {
		return server.FeatureStopResponse{}, err
	}
	return server.FeatureStopResponse{FeatureID: featureID, Result: "stopped"}, nil
}

// RestartFeature mirrors the production mutation target: RestartPhase
// computes the restart outcome under the relationship guard, and the
// returned outcome drives the ordinary resume dispatch (StartFeature for a
// phase restart, StartRepoCycleImplement for repo cycles).
func (t *journeyMutationTarget) RestartFeature(featureID string, req server.RestartFeatureRequest) (server.FeatureRestartResponse, error) {
	resp := server.FeatureRestartResponse{FeatureID: featureID, Result: "failed"}
	outcome, err := t.orch.RestartPhase(featureID, req.MaxIterationsDelta, req.MaxPlanIterationsDelta)
	if err != nil {
		return resp, err
	}
	resp.Result = "restarted"
	if outcome.Phase.String() != "" {
		resp.Phase = outcome.Phase.String()
	}
	switch outcome.Action {
	case orchestrator.RestartNoOp:
		resp.Dispatch = "none"
		return resp, nil
	case orchestrator.RestartDispatchPhase:
		resp.Dispatch = "phase"
		if err := t.orch.StartFeature(featureID); err != nil {
			resp.Result = "failed"
			return resp, err
		}
		return resp, nil
	case orchestrator.RestartDispatchRepoCycles:
		resp.Dispatch = "repo_cycles"
		resp.RepoCycleCount = len(outcome.RepoCycleRestarts)
		for _, restart := range outcome.RepoCycleRestarts {
			sessionID, err := t.orch.StartRepoCycleImplement(featureID, restart.RepoName, restart.CycleType, restart.PlanContent)
			if sessionID != "" {
				resp.SessionIDs = append(resp.SessionIDs, sessionID)
			}
			if err != nil {
				resp.Result = "failed"
				return resp, err
			}
		}
		return resp, nil
	default:
		return resp, fmt.Errorf("unknown restart action %d", outcome.Action)
	}
}

func (t *journeyMutationTarget) UpdateFeatureConfig(featureID string, req server.FeatureConfigMutationRequest) (server.FeatureConfigUpdateResponse, error) {
	automaticReviewMode := feature.AutomaticReviewMode("")
	if req.AutomaticReviewMode != nil {
		parsed, err := feature.ParseAutomaticReviewMode(*req.AutomaticReviewMode)
		if err != nil {
			return server.FeatureConfigUpdateResponse{}, err
		}
		automaticReviewMode = parsed
	} else {
		automaticReviewMode = feature.NormalizeAutomaticReviewMode(automaticReviewMode)
	}

	if err := t.orch.WithRelationshipReadLock(func() error {
		parentID, _, paired, dErr := t.orch.DetectPairedConfigTarget(featureID)
		if dErr != nil {
			return fmt.Errorf("detecting paired config target: %w", dErr)
		}
		if paired {
			if err := t.orch.UpdatePairedFeatureConfig(parentID, feature.PairedConfigInput{
				Models:              req.Models,
				Effort:              req.Effort,
				Inquireness:         feature.Inquireness(req.Inquireness),
				Checkpoints:         req.Checkpoints,
				InputNotifications:  feature.InputNotificationsMode(req.InputNotifications),
				AutomaticReviewMode: automaticReviewMode,
			}, feature.PipelineProfile(req.Pipeline), featureID); err != nil {
				return err
			}
			return nil
		}
		return t.orch.UpdateFeatureConfig(featureID, orchestrator.UpdateFeatureConfigInput{
			Models:              req.Models,
			Effort:              req.Effort,
			Inquireness:         feature.Inquireness(req.Inquireness),
			Checkpoints:         req.Checkpoints,
			InputNotifications:  feature.InputNotificationsMode(req.InputNotifications),
			AutomaticReviewMode: automaticReviewMode,
		})
	}); err != nil {
		return server.FeatureConfigUpdateResponse{}, err
	}
	return server.FeatureConfigUpdateResponse{FeatureID: featureID, Result: "updated"}, nil
}

func (t *journeyMutationTarget) PublishFeature(featureID string, req server.PublishFeatureRequest) (server.PublishFeatureResponse, error) {
	if err := t.orch.PublishWithOptions(featureID, orchestrator.PublishOptions{
		Repos: req.Repos,
		Title: req.Title,
		Body:  req.Body,
	}); err != nil {
		return server.PublishFeatureResponse{FeatureID: featureID, Result: "failed"}, err
	}
	return server.PublishFeatureResponse{FeatureID: featureID, Result: "published"}, nil
}

func (t *journeyMutationTarget) MarkDone(featureID string, _ server.GuardedFeatureActionRequest) (server.MarkDoneResponse, error) {
	if err := t.orch.MarkDone(featureID); err != nil {
		return server.MarkDoneResponse{FeatureID: featureID, Result: "failed"}, err
	}
	return server.MarkDoneResponse{FeatureID: featureID, Result: "done"}, nil
}

func (t *journeyMutationTarget) CleanupFeature(featureID string, req server.CleanupActionRequest) (server.CleanupFeatureResponse, error) {
	target := strings.ToLower(strings.TrimSpace(req.Target))
	if target == "" {
		target = "worktrees"
	}
	resp := server.CleanupFeatureResponse{FeatureID: featureID, Target: target}
	if err := t.orch.CleanWorktree(featureID); err != nil {
		resp.Result = "failed"
		return resp, err
	}
	resp.Result = "cleaned"
	return resp, nil
}

func (t *journeyMutationTarget) DeleteFeature(featureID string, _ server.GuardedFeatureActionRequest) (server.DeleteFeatureResponse, error) {
	result, err := t.orch.DeleteCascade(featureID)
	if err != nil {
		return server.DeleteFeatureResponse{FeatureID: featureID}, err
	}
	return server.DeleteFeatureResponse{FeatureID: result.ParentID, OperationID: result.OperationID, Status: result.Status, Diagnostics: result.Diagnostics}, nil
}

// --- helpers ------------------------------------------------------------------

// postActionStatus sends a feature action and returns the HTTP status code
// and response body. Unlike postAction it does not fail on non-200 responses,
// so callers can assert on conflict statuses.
func postActionStatus(t *testing.T, baseURL, featureID, action, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		baseURL+"/api/v1/features/"+featureID+"/actions/"+action, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST action %s: %v", action, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, payload
}

// getJourneyConfig fetches the per-feature config endpoint and returns the
// "current" config object as a map.
func getJourneyConfig(t *testing.T, baseURL, featureID string) map[string]any {
	t.Helper()
	out := getJourneyJSON(t, baseURL+"/api/v1/features/"+featureID+"/config")
	current, ok := out["current"].(map[string]any)
	if !ok {
		t.Fatalf("config response for %s missing current: %+v", featureID, out)
	}
	return current
}

// discardJourneyChild polls the discard action until the child is durably
// closed with outcome "discarded". The discard state machine is resumable:
// transient "sessions still draining" errors are retried automatically.
func discardJourneyChild(t *testing.T, baseURL, childID string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		status, body := postActionStatus(t, baseURL, childID, "discard", `{}`)
		if status == http.StatusOK {
			break
		}
		bodyStr := string(body)
		if !strings.Contains(bodyStr, "draining") {
			t.Fatalf("discard %s: unexpected status %d; body: %s", childID, status, bodyStr)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Wait for the durable discard closure to appear in the REST projection.
	for time.Now().Before(deadline) {
		body := journeyFeatureBody(baseURL, childID)
		if body != nil && body["close_outcome"] == feature.ChildCloseOutcomeDiscarded {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	last := journeyFeatureBody(baseURL, childID)
	lastJSON, _ := json.Marshal(last)
	t.Fatalf("child %s never discarded; last: %s", childID, lastJSON)
}
