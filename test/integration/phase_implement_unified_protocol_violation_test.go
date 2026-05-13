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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestPhaseImplementUnified_ProtocolViolation_EndToEnd drives the real unified
// phase-implement loop with a mock agent process that reports SDK success but
// only writes phase_complete. The observable contract is that the loop never
// invokes review, trips the protocol-violation rail, and atomically stamps the
// phase-declared repos failed with the contract reason.
func TestPhaseImplementUnified_ProtocolViolation_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	scriptsDir := filepath.Join(tmp, "scripts")
	for _, dir := range []string{stateDir, scriptsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	repoA := testutil.InitGitRepo(t)
	repoB := testutil.InitGitRepo(t)
	repoC := testutil.InitGitRepo(t)

	store := feature.NewStore(stateDir)
	f := &feature.Feature{
		ID:            "phase-protocol-violation",
		Name:          "Phase protocol violation",
		Slug:          "phase-protocol-violation",
		Description:   "Agent writes phase_complete without required artifacts",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: repoA, WorktreePath: repoA, Branch: "feature/test", BaseBranch: "main"},
			{Name: "repo-b", Path: repoB, WorktreePath: repoB, Branch: "feature/test", BaseBranch: "main"},
			{Name: "repo-c", Path: repoC, WorktreePath: repoC, Branch: "feature/test", BaseBranch: "main"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {},
			"repo-b": {},
			"repo-c": {Touched: true, PRURL: "https://github.com/example/repo-c/pull/9"},
		},
		MaxIterations: 5,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	f = loaded

	artifactDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "implement")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	planText := "# Plan\n\n" +
		"## Tasks\n\n" +
		"### Task 1: A work\n\n" +
		"**Repo:** repo-a\n\n" +
		"#### Automated Verification:\n" +
		"- [ ] a tests: `go test ./...`\n\n" +
		"### Task 2: B work\n\n" +
		"**Repo:** repo-b\n\n" +
		"#### Automated Verification:\n" +
		"- [ ] b tests: `go test ./...`\n"
	planPath := filepath.Join(artifactDir, "plan.md")
	if err := os.WriteFile(planPath, []byte(planText), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	agentScript := testutil.WriteScript(t, scriptsDir, "agent.sh",
		testutil.JSONLInit+"\n"+testutil.WriteImplementProtocolViolation(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	reviewMarker := filepath.Join(tmp, "review-ran")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		`touch "`+reviewMarker+`"`+"\n"+
			testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	t.Logf("behavior input: mock implement agent writes only phase_complete under %s and reports SDK success; review marker path is %s", artifactDir, reviewMarker)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := agent.OrchestratorConfig{
		Feature:             f,
		FeatureStore:        store,
		PlanPath:            planPath,
		StateDir:            stateDir,
		Model:               "agent",
		ReviewModel:         "reviewer",
		MaxIterations:       5,
		MaxConsecFails:      2,
		MaxConsecNoProgress: 5,
		BuildSession:        protocolViolationBuildSession(agentScript, reviewScript),
	}

	result, runErr := agent.RunPhaseImplementLoop(cfg, sm)
	if runErr != nil {
		t.Fatalf("RunPhaseImplementLoop: %v", runErr)
	}
	if result.FinalStatus != "protocol_violation" {
		t.Fatalf("FinalStatus = %q, want protocol_violation (LastError=%q)", result.FinalStatus, result.LastError)
	}
	t.Logf("behavior observed loop result: final_status=%s iterations=%d phase_repos=%v last_error=%q", result.FinalStatus, result.Iterations, result.PhaseRepos, result.LastError)
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", result.Iterations)
	}
	if !strings.Contains(result.LastError, "protocol violation: implementer @") || !strings.Contains(result.LastError, "progress.md") {
		t.Errorf("LastError = %q, want implementer progress.md protocol violation", result.LastError)
	}
	if !reflect.DeepEqual(result.PhaseRepos, []string{"repo-a", "repo-b"}) {
		t.Errorf("PhaseRepos = %v, want [repo-a repo-b]", result.PhaseRepos)
	}
	if _, err := os.Stat(reviewMarker); !os.IsNotExist(err) {
		t.Fatalf("review gate ran despite protocol violation: stat err = %v", err)
	}

	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load feature after protocol violation: %v", err)
	}
	for _, repo := range []string{"repo-a", "repo-b"} {
		st := got.RepoStates[repo]
		if st == nil || !st.Touched {
			t.Errorf("repo %q state = %+v, want failed phase stamp", repo, st)
			continue
		}
		if !strings.Contains(st.LastError, "protocol violation") || !strings.Contains(st.LastError, "progress.md") {
			t.Errorf("repo %q LastError = %q, want protocol violation progress.md reason", repo, st.LastError)
		}
		t.Logf("behavior observed repo stamp: repo=%s touched=%v last_error=%q", repo, st.Touched, st.LastError)
	}
	if st := got.RepoStates["repo-c"]; st == nil || st.PRURL == "" {
		t.Errorf("repo-c state = %+v, want outside-phase PR state preserved", st)
	} else {
		t.Logf("behavior observed outside-phase repo preserved: repo=repo-c touched=%v pr_url=%q", st.Touched, st.PRURL)
	}
	for _, iter := range []string{"iteration-01", "iteration-02"} {
		iterDir := filepath.Join(artifactDir, iter)
		if _, err := os.Stat(filepath.Join(iterDir, "phase_complete")); err != nil {
			t.Errorf("%s phase_complete missing: %v", iter, err)
		}
		feedback, err := os.ReadFile(filepath.Join(iterDir, "review-feedback.md"))
		if err != nil {
			t.Errorf("%s review-feedback.md missing: %v", iter, err)
			continue
		}
		if !strings.Contains(string(feedback), "## Verdict\nCHANGES_REQUESTED") {
			t.Errorf("%s review-feedback.md missing CHANGES_REQUESTED verdict:\n%s", iter, feedback)
		}
		t.Logf("behavior observed iteration artifact: iter=%s phase_complete=true synthesized_feedback_changes_requested=true", iter)
	}
}

func protocolViolationBuildSession(agentScript, reviewScript string) func(agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
	return func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		script := agentScript
		if opts.Model == "reviewer" && reviewScript != "" {
			script = reviewScript
		}
		return []string{"bash", script}, nil, &ports.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
			LogPath:       opts.LogPath,
		}, nil
	}
}
