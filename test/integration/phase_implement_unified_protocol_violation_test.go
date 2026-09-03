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
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestPhaseImplementUnified_ProtocolViolation_EndToEnd drives the real unified
// phase-implement loop with a mock agent process that reports SDK success but
// omits its required artifacts. The observable contract is that the loop never
// invokes review, never commits a receipt, trips the protocol-violation rail,
// and atomically stamps the phase-declared repos failed with the contract
// reason.
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
		Description:   "Agent reports success without required artifacts",
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
		testutil.JSONLInit+"\n"+testutil.JSONLSuccess+"\n")
	reviewMarker := filepath.Join(tmp, "review-ran")
	reviewScript := testutil.WriteScript(t, scriptsDir, "review.sh",
		`touch "`+reviewMarker+`"`+"\n"+
			testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")
	t.Logf("behavior input: mock implement agent omits required artifacts under %s and reports semantic success; review marker path is %s", artifactDir, reviewMarker)

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
		t.Logf("behavior observed repo stamp: repo=%s touched=%v", repo, st.Touched)
	}
	if st := got.RepoStates["repo-c"]; st == nil || st.PRURL == "" {
		t.Errorf("repo-c state = %+v, want outside-phase PR state preserved", st)
	} else {
		t.Logf("behavior observed outside-phase repo preserved: repo=repo-c touched=%v pr_url=%q", st.Touched, st.PRURL)
	}
	for _, iter := range []string{"iteration-01", "iteration-02"} {
		iterDir := filepath.Join(artifactDir, iter)
		if _, err := os.Stat(filepath.Join(iterDir, agent.PhaseCompleteFile)); !os.IsNotExist(err) {
			t.Errorf("%s completion receipt exists after contract violation: %v", iter, err)
		}
		feedback, err := os.ReadFile(filepath.Join(iterDir, "review-feedback.md"))
		if err != nil {
			t.Errorf("%s review-feedback.md missing: %v", iter, err)
			continue
		}
		if !strings.Contains(string(feedback), "## Verdict\nCHANGES_REQUESTED") {
			t.Errorf("%s review-feedback.md missing CHANGES_REQUESTED verdict:\n%s", iter, feedback)
		}
		t.Logf("behavior observed iteration artifact: iter=%s completion_receipt=false synthesized_feedback_changes_requested=true", iter)
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

// TestPhaseImplementUnified_ProtocolViolation_PersistsFailureRecord pins the
// propagation of the unified loop's protocol-violation outcome into the run's
// durable canonical failure record at the orchestrator completion boundary:
// the per-repo protocol_violation statuses classify once into a
// protocol_violation record whose phase block names implement, whose
// repositories block lists the failed phase repos, and whose diagnostics
// carry the loop's raw last-error text.
func TestPhaseImplementUnified_ProtocolViolation_PersistsFailureRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	repoA := testutil.InitGitRepo(t)
	repoB := testutil.InitGitRepo(t)

	store := feature.NewStore(stateDir)
	f := &feature.Feature{
		ID:            "phase-protocol-violation-record",
		Name:          "Phase protocol violation record",
		Slug:          "phase-protocol-violation-record",
		Description:   "Protocol violation persists canonical failure record",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: repoA, WorktreePath: repoA, Branch: "feature/test", BaseBranch: "main"},
			{Name: "repo-b", Path: repoB, WorktreePath: repoB, Branch: "feature/test", BaseBranch: "main"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {},
			"repo-b": {},
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

	planDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "implement")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan dir: %v", err)
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
	planPath := filepath.Join(planDir, "plan.md")
	if err := os.WriteFile(planPath, []byte(planText), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	diagnostics := "protocol violation: implementer @ iteration-01: missing required artifact progress.md"
	stubImpl := func(_ agent.ImplementConfig, _ ports.SessionManager) (*agent.LoopResult, error) {
		return &agent.LoopResult{
			FinalStatus: "protocol_violation",
			Iterations:  1,
			LastError:   diagnostics,
		}, nil
	}

	cfg := agent.OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		PlanPath:       planPath,
		StateDir:       stateDir,
		MaxIterations:  5,
		RunImplementFn: stubImpl,
	}

	mr, err := agent.RunMultiRepoImplementation(cfg, nil)
	if err != nil {
		t.Fatalf("RunMultiRepoImplementation: %v", err)
	}
	if mr.FinalStatus != "failed" {
		t.Fatalf("multi-repo FinalStatus = %q, want failed", mr.FinalStatus)
	}
	if mr.LastError != diagnostics {
		t.Fatalf("multi-repo LastError = %q, want %q", mr.LastError, diagnostics)
	}
	wantPhaseRepos := []string{"repo-a", "repo-b"}
	if !reflect.DeepEqual(mr.FailedRepos, wantPhaseRepos) {
		t.Fatalf("FailedRepos = %v, want %v", mr.FailedRepos, wantPhaseRepos)
	}
	for _, repo := range wantPhaseRepos {
		if mr.RepoStatuses[repo] != "protocol_violation" {
			t.Fatalf("RepoStatuses[%q] = %q, want protocol_violation", repo, mr.RepoStatuses[repo])
		}
	}

	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: feature.NewManager(store, nil),
		Store:     store,
	}, orchestrator.Hooks{})
	if err := orch.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: mr,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load feature after completion: %v", err)
	}
	if got.Status != feature.StatusFailed {
		t.Fatalf("Status = %v, want Failed", got.Status)
	}
	if code := got.FailureCode(); code != errcat.ProtocolViolation {
		t.Fatalf("FailureCode() = %q, want %q", code, errcat.ProtocolViolation)
	}
	rec := got.FailureRecord()
	if rec == nil {
		t.Fatalf("failure record missing after protocol-violation completion")
	}
	if rec.Diagnostics != diagnostics {
		t.Errorf("record diagnostics = %q, want %q", rec.Diagnostics, diagnostics)
	}
	if rec.Context == nil || rec.Context.Phase == nil || rec.Context.Phase.Name != "implement" {
		t.Fatalf("record phase block = %+v, want implement", rec.Context)
	}
	repoNames := make([]string, 0, len(rec.Context.Repositories))
	for _, repo := range rec.Context.Repositories {
		repoNames = append(repoNames, repo.Name)
	}
	if !reflect.DeepEqual(repoNames, wantPhaseRepos) {
		t.Errorf("record repositories = %v, want %v", repoNames, wantPhaseRepos)
	}
}
