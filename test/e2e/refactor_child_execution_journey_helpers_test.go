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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

const journeyRoadmapText = `# Roadmap

## Phase 1: Child integration slice

### Goal

Land the child work and integrate it into the parent.

### Scope

One small change.
`

var journeyPhasePlanText = "# Phase 1 Plan\n\n" +
	"## Overview\nShip the child slice.\n\n" +
	"## Tasks\n\n" +
	"### Task 1: Produce the child artifact\n\n" +
	"**Repo:** repoA\n\n" +
	"#### What to build\nWrite the child output file.\n\n" +
	"#### Acceptance criteria\n- [ ] The child artifact exists in the worktree.\n\n" +
	"#### Blocked by\nNone - can start immediately\n\n" +
	"## Success Criteria\n\n" +
	"### Automated Verification\n- [ ] Artifact present: `test -f child-output.txt`\n\n" +
	"### Manual Verification\n- [ ] None required: internal test fixture.\n\n" +
	"### Visual Evidence\n- [ ] None required: no user-facing rendered surface.\n\n" +
	"### Behavioral Evidence\n- [ ] None required: automated tests provide the artifact.\n"

// journeyChildPhaseRunner wires scripted plan sessions plus stubbed
// implement and final-review kernels so the journey exercises the real
// orchestrator/server wiring without launching provider CLIs. The implement
// stub writes one artifact into the child worktree, which integration must
// carry into the parent.
func journeyChildPhaseRunner(t *testing.T, sm *session.Manager, store *feature.Store, stateDir string) *agent.PhaseRunner {
	return journeyChildPhaseRunnerWithOpts(t, sm, store, stateDir, journeyPhaseRunnerOptions{})
}

// journeyPhaseRunnerOptions configures the scripted phase runner.
type journeyPhaseRunnerOptions struct {
	// mergeRebaseTarget, when true, makes the implement stub merge the
	// persisted creation-time target ref into each behind repo's child
	// worktree so the rebase child satisfies the mechanical integration
	// gate's ancestor check. False leaves the child branch without the
	// target commit (used to exercise the gate's not-ancestor failure).
	mergeRebaseTarget bool
}

// journeyChildPhaseRunnerWithOpts is the configurable core of
// journeyChildPhaseRunner.
func journeyChildPhaseRunnerWithOpts(t *testing.T, sm *session.Manager, store *feature.Store, stateDir string, opts journeyPhaseRunnerOptions) *agent.PhaseRunner {
	t.Helper()
	scriptsDir := t.TempDir()

	pr := agent.NewPhaseRunner(sm, store, stateDir)
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		// Plan sessions write their artifact into the run-scoped artifact
		// directory and finish with the structured agentico-outcome; the
		// harness owns phase_complete. Utility sessions (e.g. PR description
		// generation during auto-publish) just need a text response.
		f, _ := store.Load(opts.FeatureID)
		var script string
		switch {
		case opts.Phase != feature.PhasePlan || f == nil:
			script = testutil.WriteScript(t, scriptsDir, "description.sh", testutil.JSONLInit+"\n"+
				`read -r _agentic_init`+"\n"+
				testutil.JSONLAssistant("TITLE: Child integration\n\nBODY: Integrated the refactor child.")+"\n"+
				testutil.JSONLSuccess+"\n")
		case strings.Contains(strings.ToLower(opts.Prompt), "roadmap"):
			artifactDir := agent.RoadmapDir(stateDir, f)
			script = testutil.WriteScript(t, scriptsDir, "roadmap.sh", testutil.JSONLInit+"\n"+
				`read -r _agentic_init`+"\n"+
				fmt.Sprintf("mkdir -p %q", artifactDir)+"\n"+
				fmt.Sprintf("cat > %q <<'ROADMAP_EOF'\n%s\nROADMAP_EOF", filepath.Join(artifactDir, "roadmap.md"), journeyRoadmapText)+"\n"+
				testutil.JSONLSuccess+"\n")
		default:
			phaseNum := f.CurrentRoadmapPhase
			if phaseNum == 0 {
				phaseNum = 1
			}
			artifactDir := agent.PhasePlanDir(stateDir, f, phaseNum)
			script = testutil.WriteScript(t, scriptsDir, "phase-plan.sh", testutil.JSONLInit+"\n"+
				`read -r _agentic_init`+"\n"+
				fmt.Sprintf("mkdir -p %q", artifactDir)+"\n"+
				fmt.Sprintf("cat > %q <<'PLAN_EOF'\n%s\nPLAN_EOF", filepath.Join(artifactDir, "plan.md"), journeyPhasePlanText)+"\n"+
				testutil.JSONLSuccess+"\n")
		}
		return []string{"bash", script}, nil, &ports.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
		}, nil
	}
	pr.RunImplementFn = func(c agent.ImplementConfig, _ ports.SessionManager) (*agent.LoopResult, error) {
		// Stand in for the implement kernel: leave one real change in the
		// child worktree for the integration boundary to commit and merge.
		for _, repo := range c.Feature.Repos {
			wt := repo.WorktreePath
			if wt == "" {
				wt = repo.Path
			}
			if err := os.WriteFile(filepath.Join(wt, "child-output.txt"), []byte("child work\n"), 0o644); err != nil {
				return nil, fmt.Errorf("write child output: %w", err)
			}
		}
		// For a rebase child, the implement kernel is expected to merge the
		// creation-time target into each behind repo's working branch. The
		// mechanical integration gate re-verifies the result, so the stub
		// must actually land the merge (cleanly, against a non-conflicting
		// target) for the happy path to pass the gate. When
		// mergeRebaseTarget is false the stub leaves the branch without the
		// target, exercising the gate's not-ancestor failure.
		if opts.mergeRebaseTarget && c.Feature.Parent != nil && c.Feature.Parent.Kind == feature.ChildKindRebase {
			behind := make(map[string]bool, len(c.Feature.Parent.RebaseBehind))
			for _, r := range c.Feature.Parent.RebaseBehind {
				behind[r] = true
			}
			for _, repo := range c.Feature.Repos {
				if !behind[repo.Name] {
					continue
				}
				wt := repo.WorktreePath
				if wt == "" {
					wt = repo.Path
				}
				tgt, ok := c.Feature.RebaseTargetForRepo(repo.Name)
				if !ok || tgt.Ref == "" {
					return nil, fmt.Errorf("rebase journey stub: no persisted target for %s", repo.Name)
				}
				if err := journeyMergeRebaseTarget(t, wt, tgt.Ref); err != nil {
					return nil, fmt.Errorf("rebase journey stub merge %s: %w", repo.Name, err)
				}
			}
		}
		return &agent.LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil
	}
	pr.RunFinalReviewFn = func(c agent.OrchestratorConfig, _ ports.SessionManager) (*agent.FeatureFinalReviewResult, error) {
		repos := c.Feature.TouchedRepos()
		if err := agent.AtomicPhaseStamp(c.FeatureStore, agent.AtomicPhaseStampInput{
			FeatureID: c.Feature.ID,
			Repos:     repos,
			Outcome:   agent.PhaseOutcomeFinalReviewPassed,
		}); err != nil {
			return nil, err
		}
		return &agent.FeatureFinalReviewResult{
			FinalStatus: "review_passed",
			Iterations:  1,
			Repos:       repos,
		}, nil
	}
	return pr
}

// journeyMergeRebaseTarget merges the persisted target ref into the child
// worktree's current branch with a no-ff merge commit, using the test git
// identity. It assumes a non-conflicting target (the journey repos diverge on
// disjoint files).
func journeyMergeRebaseTarget(t *testing.T, worktreePath, ref string) error {
	t.Helper()
	cmd := exec.Command("git", "-C", worktreePath, "merge", "--no-ff", "-m",
		"journey stub: merge rebase target "+ref, ref)
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git merge %s: %v\n%s", ref, err, out)
	}
	return nil
}

// postAction runs one feature action through the raw action route and fails
// on a non-200 response.
func postAction(t *testing.T, baseURL, featureID, action, body string) {
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
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST action %s status = %d; body: %s", action, resp.StatusCode, payload)
	}
}

// postReviewSessionProceed approves the feature's active review gate through
// the review-session decision endpoint — the live decision path. It creates
// (or reopens) the session to read the draft revision the decision must
// carry, then submits an unmodified proceed.
func postReviewSessionProceed(t *testing.T, baseURL, featureID string) {
	t.Helper()
	created := journeyPostJSON(t, baseURL+"/api/v1/features/"+featureID+"/reviews", `{}`)
	reviewID, _ := created["review_id"].(string)
	draftRevision, _ := created["draft_revision"].(string)
	if reviewID == "" || draftRevision == "" {
		t.Fatalf("review session create = %v; want review_id and draft_revision", created)
	}
	journeyPostJSON(t, baseURL+"/api/v1/features/"+featureID+"/reviews/"+reviewID+"/decision",
		`{"decision":"proceed","base_revision":"`+draftRevision+`"}`)
}

// journeyPostJSON posts a trusted JSON mutation and returns the decoded
// response, failing on non-200.
func journeyPostJSON(t *testing.T, url, body string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d; body: %s", url, resp.StatusCode, payload)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode %s response: %v (body: %s)", url, err, payload)
	}
	return decoded
}

// waitForJourneyGate polls until the child is parked at the plan-review gate
// for the given roadmap phase.
func waitForJourneyGate(t *testing.T, baseURL, childID string, wantRoadmapPhase float64) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		body := journeyFeatureBody(baseURL, childID)
		if body != nil {
			last = body
			if body["status"] == feature.StatusPlanNeedsReview.String() {
				if phase, ok := body["current_roadmap_phase"].(float64); !ok || phase == wantRoadmapPhase {
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	lastJSON, _ := json.Marshal(last)
	t.Fatalf("child %s never reached plan-review gate for roadmap phase %v; last: %s", childID, wantRoadmapPhase, lastJSON)
}

// waitForJourneyStatus polls until the feature reports the wanted durable
// status, failing with the last observed body on timeout.
func waitForJourneyStatus(t *testing.T, baseURL, featureID, wantStatus string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		body := journeyFeatureBody(baseURL, featureID)
		if body != nil {
			last = body
			if body["status"] == wantStatus {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	lastJSON, _ := json.Marshal(last)
	t.Fatalf("feature %s never reached status %q; last: %s", featureID, wantStatus, lastJSON)
}

// waitForJourneyFeatureSessionsQuiescent polls until the feature has no
// active sessions left. A stop transitions the durable status to
// Interrupted BEFORE the session kill settles, so resume/restart actions
// dispatched on the status alone would race a still-running subprocess.
func waitForJourneyFeatureSessionsQuiescent(t *testing.T, baseURL, featureID string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		sessions, err := getJourneyJSONQuiet(baseURL + "/api/v1/sessions")
		if err == nil && sessions != nil {
			active := false
			rows, _ := sessions["sessions"].([]any)
			lastRows, _ := json.Marshal(rows)
			last = string(lastRows)
			for _, row := range rows {
				sess, _ := row.(map[string]any)
				if sess["feature_id"] == featureID && sess["status"] == "Running" {
					active = true
					break
				}
			}
			if !active {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("feature %s sessions never drained; last: %s", featureID, last)
}

// waitForJourneyChildClosed polls until the child relationship is durably
// closed AND the closure tail (cleanup or its recorded warning) has settled,
// so follow-on assertions never race the post-close work.
func waitForJourneyChildClosed(t *testing.T, baseURL string, store *feature.Store, childID string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		body := journeyFeatureBody(baseURL, childID)
		if body != nil {
			last = body
		}
		if body == nil || body["close_outcome"] != feature.ChildCloseOutcomeCompleted {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		current, err := store.Load(childID)
		if err == nil && len(current.Repos) > 0 {
			allCleared := true
			for _, repo := range current.Repos {
				if repo.WorktreePath != "" {
					allCleared = false
					break
				}
			}
			hasWarning := false
			if current.Parent.Transaction != nil {
				for _, e := range current.Parent.Transaction.Entries {
					if e.CleanupWarning != "" {
						hasWarning = true
						break
					}
				}
			}
			if allCleared || hasWarning {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	lastJSON, _ := json.Marshal(last)
	t.Fatalf("child %s never closed (or cleanup never settled); last: %s", childID, lastJSON)
}

// waitForParentPublishOutcome polls until the parent's per-repo publication
// state records a terminal result (PR URL on success, LastError on failure).
func waitForParentPublishOutcome(t *testing.T, store *feature.Store, parentID string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		parent, err := store.Load(parentID)
		if err == nil {
			if st := parent.RepoStates["repoA"]; st != nil && (st.PRURL != "" || st.LastError != "") {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	parent, _ := store.Load(parentID)
	var repoState feature.RepoState
	if state := parent.RepoStates["repoA"]; state != nil {
		repoState = *state
	}
	t.Fatalf("parent %s publication never settled: status=%s checkpoints=%+v repoA=%+v", parentID, parent.Status, parent.Checkpoints, repoState)
}

func journeyFeatureBody(baseURL, featureID string) map[string]any {
	resp, err := http.Get(baseURL + "/api/v1/features/" + featureID)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var detail map[string]any
	if json.NewDecoder(resp.Body).Decode(&detail) != nil {
		return nil
	}
	body, _ := detail["feature"].(map[string]any)
	return body
}
