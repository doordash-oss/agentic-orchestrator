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
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestRefactorAPIJourney records the primary state-mutating journey:
//
//	POST /api/v1/features/{parent}/actions/refactor
//	  → durable child creation (201 with the child id, before setup finishes)
//	  → asynchronous REAL-git worktree setup pinned at the captured parent
//	    HEAD (proven by advancing the parent branch after launch and
//	    verifying the child worktree is still at the captured commit)
//	  → child parked at durable Created with derived setup_complete
//	  → correlated parent/child REST projections and SSE frames
//	  → the capability gate rejects start on the setup-complete child.
func TestRefactorAPIJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	// Worktrees live alongside the features dir, matching the module wiring:
	// filepath.Join(filepath.Dir(stateDir), "worktrees").
	wtBaseDir := filepath.Join(tmp, "worktrees")

	repoDir := testutil.InitGitRepo(t)
	journeyGit(t, repoDir, "checkout", "-b", "feature/journey-parent")
	writeJourneyFile(t, repoDir, "base.txt", "v1\n")
	journeyGit(t, repoDir, "add", "base.txt")
	journeyGit(t, repoDir, "commit", "-m", "parent base commit")
	capturedHEAD := journeyGit(t, repoDir, "rev-parse", "HEAD")
	if len(capturedHEAD) != 40 {
		t.Fatalf("captured parent HEAD = %q, want full sha", capturedHEAD)
	}

	store := feature.NewStore(stateDir)
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
		}},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}

	// Real-git adapters: the same wiring the fx module provides in
	// internal/orchestrator/module.go.
	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, config.NewDefault())
	mgr.Worktrees = wm

	orch := orchestrator.New(orchestrator.Deps{Lifecycle: mgr, Store: store}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		// Async child setup is tracked by the orchestrator; wait for it before
		// TempDir cleanup runs.
		orch.WaitForCycles()
	})

	// Fan orchestrator lifecycle events into the server event bus so the SSE
	// stream sees relationship-correlated frames (same shape as cmd/agentico).
	serverEvents := make(chan interface{}, 256)
	stopForwarding := make(chan struct{})
	defer close(stopForwarding)
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

	// Subscribe to SSE before the launch so post-launch frames are observed.
	sseResp, err := srv.Client().Get(srv.URL + "/api/v1/events?heartbeat_ms=10")
	if err != nil {
		t.Fatalf("GET SSE: %v", err)
	}
	defer sseResp.Body.Close()
	reader := bufio.NewReader(sseResp.Body)
	if _, err := readJourneySSEBlock(reader, "connected"); err != nil {
		t.Fatalf("read SSE while waiting for %q: %v", "connected", err)
	}

	// --- POST the refactor launch. 201 must return once the relationship is
	// durable, without waiting for worktree setup.
	resp, err := client.RefactorFeature(t.Context(), parent.ID, server.RefactorFeatureRequest{
		Name:     "Rework the auth pipeline",
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("RefactorFeature() error = %v", err)
	}
	childID := resp.FeatureID
	if childID == "" || resp.ParentID != parent.ID {
		t.Fatalf("refactor response = %+v; want 201 body with child id and parent %s", resp, parent.ID)
	}

	// The relationship must be durable immediately after the POST returned.
	launchChild, err := store.Load(childID)
	if err != nil {
		t.Fatalf("child not durable right after POST: %v", err)
	}
	if launchChild.Parent == nil || launchChild.Parent.ParentID != parent.ID ||
		launchChild.Parent.Kind != feature.ChildKindRefactor {
		t.Fatalf("durable child relationship = %+v, want refactor child of %s", launchChild.Parent, parent.ID)
	}
	if len(launchChild.Parent.Bases) != 1 || launchChild.Parent.Bases[0].SHA != capturedHEAD {
		t.Fatalf("durable child bases = %+v, want captured parent HEAD %s", launchChild.Parent.Bases, capturedHEAD)
	}

	// Move the parent branch tip forward AFTER the launch captured the base.
	// Exact-tip setup must ignore this newer commit.
	writeJourneyFile(t, repoDir, "base.txt", "v2\n")
	journeyGit(t, repoDir, "add", "base.txt")
	journeyGit(t, repoDir, "commit", "-m", "advance parent branch after launch")
	movedHEAD := journeyGit(t, repoDir, "rev-parse", "HEAD")
	if movedHEAD == capturedHEAD {
		t.Fatal("parent branch did not advance")
	}

	// --- Wait for asynchronous setup to park the child at Created /
	// setup_complete, then assert the real worktree is at the captured SHA.
	childBody := waitForJourneySetupComplete(t, srv.URL, childID)
	if childBody["status"] != "Created" {
		t.Fatalf("child durable status = %v, want Created (capability gate, no auto-start)", childBody["status"])
	}
	bases, ok := childBody["bases"].([]any)
	if !ok || len(bases) != 1 {
		t.Fatalf("child detail bases = %v, want one base", childBody["bases"])
	}
	if base := bases[0].(map[string]any); base["sha"] != capturedHEAD || base["parent_branch"] != "feature/journey-parent" {
		t.Fatalf("child detail base = %+v, want sha %s on feature/journey-parent", base, capturedHEAD)
	}

	parked, err := store.Load(childID)
	if err != nil {
		t.Fatalf("reload child: %v", err)
	}
	wtPath := parked.Repos[0].WorktreePath
	if wtPath == "" {
		t.Fatalf("child worktree path empty after setup: %+v", parked.Repos[0])
	}
	if got := journeyGit(t, wtPath, "rev-parse", "HEAD"); got != capturedHEAD {
		t.Fatalf("child worktree HEAD = %s, want captured parent tip %s (branch later moved to %s)", got, capturedHEAD, movedHEAD)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "base.txt")); err != nil {
		t.Fatalf("child worktree missing captured-tip content: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(wtPath, "base.txt"))
	if err != nil || string(content) != "v1\n" {
		t.Fatalf("child worktree base.txt = %q (%v), want v1 (captured tip, not latest %s)", content, err, movedHEAD)
	}

	// --- Correlated REST projections.
	list := getJourneyJSON(t, srv.URL+"/api/v1/features")
	summaries := list["features"].([]any)
	if len(summaries) != 1 || summaries[0].(map[string]any)["id"] != parent.ID {
		t.Fatalf("top-level list = %+v, want only the parent (no child leak)", summaries)
	}
	activeChild := summaries[0].(map[string]any)["active_child"].(map[string]any)
	if activeChild["id"] != childID || activeChild["relationship_state"] != "active" {
		t.Fatalf("parent summary active_child = %+v, want active child %s", activeChild, childID)
	}
	if childBody["parent_id"] != parent.ID || childBody["active"] != true {
		t.Fatalf("child detail linkage = %v/%v, want parent %s and active", childBody["parent_id"], childBody["active"], parent.ID)
	}

	// --- Correlated SSE: at least one frame names the child and the parent.
	// The reader goroutine reports failures through a channel instead of
	// calling t.Fatalf after the test may have finished.
	sseBlocks := make(chan journeySSEBlock, 16)
	go readJourneySSEBlocks(reader, "lifecycle.updated", sseBlocks)
	timeout := time.After(10 * time.Second)
	for found := false; !found; {
		select {
		case res := <-sseBlocks:
			if res.err != nil {
				t.Fatalf("read SSE while waiting for %q: %v", "lifecycle.updated", res.err)
			}
			if strings.Contains(res.text, `"child_id":"`+childID+`"`) &&
				strings.Contains(res.text, `"parent_id":"`+parent.ID+`"`) {
				found = true
			}
		case <-timeout:
			t.Fatalf("no SSE lifecycle.updated frame correlating child %s to parent %s", childID, parent.ID)
		}
	}

	// --- Capability gate: this child is eligible (Medium, one repository),
	// so start is accepted and routes into the ordinary pipeline. With no
	// PhaseRunner wired the plan phase dispatch is a no-op, but the action
	// must report a successful start rather than the child_execution_blocked
	// conflict unsupported shapes receive.
	startReq, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/features/"+childID+"/actions/start", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	startReq.Header.Set("Content-Type", "application/json")
	startReq.Header.Set("X-Agentico-Client", "local")
	startResp, err := srv.Client().Do(startReq)
	if err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(startResp.Body)
		t.Fatalf("start child status = %d; want 200 for an eligible child; body: %s", startResp.StatusCode, body)
	}
}

// journeyMutationTarget drives only the mutations this journey exercises; the
// embedded interface satisfies MutationTarget, and any other method would
// fail loudly if dispatched (matching the handler test fakes' pattern is
// avoided here because every non-refactor/start call would be a test bug).
type journeyMutationTarget struct {
	server.MutationTarget
	mgr  *feature.Manager
	orch *orchestrator.Orchestrator
}

func (t *journeyMutationTarget) RefactorFeature(featureID string, req server.RefactorFeatureRequest) (server.RefactorFeatureResponse, error) {
	resp := server.RefactorFeatureResponse{ParentID: featureID, Result: "failed"}
	// Use the single production request→spec mapping so the journey cannot
	// silently drift out of sync with request fields handled in production.
	spec, err := server.RefactorChildSpecFromRequest(req)
	if err != nil {
		return resp, err
	}
	child, err := t.mgr.CreateRefactorChild(featureID, spec)
	if err != nil {
		return resp, err
	}
	t.orch.ChildCreated(child)
	// Mirror the production mutation target: asynchronous child setup is
	// orchestrator-owned so terminal setup errors are recorded and emitted.
	t.orch.RunSetupAsync(child.ID)
	resp.FeatureID = child.ID
	resp.Result = "created"
	return resp, nil
}

// RebaseFeature mirrors the production rebase child launch: orchestrator
// preflight resolves targets and computes behind-ness, then the child is
// created under the relationship write lock with the persisted results.
func (t *journeyMutationTarget) RebaseFeature(featureID string, _ server.RebaseFeatureRequest) (server.RebaseFeatureResponse, error) {
	resp := server.RebaseFeatureResponse{ParentID: featureID, Result: "failed"}
	preflight, err := t.orch.RebaseChildPreflight(featureID)
	if err != nil {
		return resp, err
	}
	spec := feature.RebaseChildSpec{
		Bases:   preflight.Bases,
		Targets: preflight.Targets,
		Behind:  preflight.Behind,
	}
	var child *feature.Feature
	if err := t.orch.WithRelationshipWriteLock(func() error {
		var createErr error
		child, createErr = t.mgr.CreateRebaseChild(featureID, spec)
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

func (t *journeyMutationTarget) StartFeature(featureID string) (server.FeatureStartResponse, error) {
	resp := server.FeatureStartResponse{FeatureID: featureID, Result: "failed"}
	if err := t.orch.StartFeature(featureID); err != nil {
		return resp, err
	}
	resp.Result = "started"
	return resp, nil
}

// ReviewDecision mirrors the production mapping so the journey resumes
// configured roadmap and phase-plan gates through the standard flow.
func (t *journeyMutationTarget) ReviewDecision(featureID string, req server.ReviewDecisionRequest) error {
	decision := orchestrator.ReviewDecision{
		Decision:    req.Decision,
		TargetPhase: journeyParsePhase(req.Phase),
		IsRewind:    req.IsRewind,
		PhasePlan:   req.PhasePlan,
		Roadmap:     req.Roadmap,
		Comment:     req.Comment,
	}
	return t.orch.HandleReviewDecision(featureID, decision)
}

// journeyParsePhase mirrors the production phase-name mapping for the small
// set this journey can request.
func journeyParsePhase(name string) feature.Phase {
	switch name {
	case "Plan":
		return feature.PhasePlan
	case "Implement":
		return feature.PhaseImplement
	default:
		return 0
	}
}

func waitForJourneySetupComplete(t *testing.T, baseURL, childID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var body map[string]any
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/v1/features/" + childID)
		if err == nil && resp.StatusCode == http.StatusOK {
			var detail map[string]any
			if json.NewDecoder(resp.Body).Decode(&detail) == nil {
				body = detail["feature"].(map[string]any)
			}
			resp.Body.Close()
			if body != nil && body["setup_complete"] == true {
				return body
			}
		} else if err == nil {
			resp.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	last, _ := json.Marshal(body)
	t.Fatalf("child %s never reached setup_complete; last body: %s", childID, last)
	return nil
}

func getJourneyJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s status = %d; body: %s", url, resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return out
}

// getJourneyJSONQuiet fetches a JSON object without failing the test —
// used by debug pollers that run off the main test goroutine.
func getJourneyJSONQuiet(url string) (map[string]any, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s status %d", url, resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// journeySSEBlock carries one SSE block (or a read error) from the reader
// goroutine to the test goroutine.
type journeySSEBlock struct {
	text string
	err  error
}

// readJourneySSEBlocks streams SSE blocks for the given event onto out until
// a read error occurs, then reports the error and returns. It never touches
// testing.T; failures are delivered through the channel.
func readJourneySSEBlocks(r *bufio.Reader, event string, out chan<- journeySSEBlock) {
	for {
		block, err := readJourneySSEBlock(r, event)
		out <- journeySSEBlock{text: block, err: err}
		if err != nil {
			return
		}
	}
}

func readJourneySSEBlock(r *bufio.Reader, event string) (string, error) {
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			block := strings.Join(lines, "\n")
			if strings.Contains(block, "event: "+event) {
				return block, nil
			}
			lines = lines[:0]
			continue
		}
		lines = append(lines, line)
	}
}

func journeyGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeJourneyFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
