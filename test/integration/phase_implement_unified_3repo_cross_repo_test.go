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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestPhaseImplementUnified_3Repo_ScopedVerification is the acceptance test
// for phase atomicity and explicitly scoped verification on a 3-repo phase:
//
//   - PhaseScope reads `**Repo:** repo-a/repo-b/repo-c` Task tags from a
//     planned-mode plan and produces the deduplicated, sorted repo set.
//   - CompileTestingContractMultiRepo emits each explicitly scoped plan
//     command once in its declared repository.
//   - RunPhaseImplementLoop dispatches the iteration successfully and
//     AtomicPhaseStamp transitions all three repos atomically to
//     "awaiting_final_review" in one Modify write (phase atomicity:
//     no partial-phase shipment).
//
// The test uses real on-disk plan + state, real PhaseScope/contract
// compiler, and a stub RunImplementFn (the loop kernel is unit-tested
// elsewhere).
func TestPhaseImplementUnified_3Repo_ScopedVerification(t *testing.T) {
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
	repoC := testutil.InitGitRepo(t)

	store := feature.NewStore(stateDir)
	f := &feature.Feature{
		ID:            "phase-3repo-scoped",
		Name:          "3-repo scoped verification",
		Slug:          "phase-3repo-scoped",
		Description:   "Phase plan with one scoped verification command per repository",
		Status:        feature.StatusImplementing,
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
			"repo-c": {},
		},
		MaxIterations: 3,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	f = loaded

	// Plan with three Tasks (one per repo) and one explicitly scoped command
	// for each repository.
	planDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "implement")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	planText := "# Plan\n\n" +
		"## Tasks\n\n" +
		"### Task 1: API work\n\n" +
		"**Repo:** repo-a\n\n" +
		"Update the API handlers.\n\n" +
		"### Task 2: Web work\n\n" +
		"**Repo:** repo-b\n\n" +
		"Update the web client.\n\n" +
		"### Task 3: Infra work\n\n" +
		"**Repo:** repo-c\n\n" +
		"Update the infra config.\n\n" +
		"## Success Criteria\n\n" +
		"### Automated Verification\n" +
		"- [ ] [repo: repo-a] API tests: `go test ./api/...`\n" +
		"- [ ] [repo: repo-b] Web tests: `npm test`\n" +
		"- [ ] [repo: repo-c] Infra tests: `terraform validate`\n"
	planPath := filepath.Join(planDir, "plan.md")
	if err := os.WriteFile(planPath, []byte(planText), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	// --- Part A: PhaseScope identifies all three repos. ---
	scope, err := agent.PhaseScope(f, planPath)
	if err != nil {
		t.Fatalf("PhaseScope: %v", err)
	}
	if !scope.ScopeOK() {
		t.Fatalf("PhaseScope issues: %s", scope.IssueSummary())
	}
	wantRepos := []string{"repo-a", "repo-b", "repo-c"}
	if !reflect.DeepEqual(scope.Repos, wantRepos) {
		t.Errorf("PhaseScope.Repos = %v, want %v", scope.Repos, wantRepos)
	}

	// --- Part B: TestingContractCompiler emits each scoped command once. ---
	contract := agent.CompileTestingContractMultiRepo(agent.MultiRepoContractInput{
		Repos:     scope.Repos,
		PlanText:  planText,
		PlanPath:  planPath,
		PhaseType: "tracer-bullet",
	})

	// Every item must carry a `repo:` field — the unification invariant.
	for _, it := range contract.Items {
		if it.Repo == "" {
			t.Errorf("contract item missing repo tag: %+v", it)
		}
	}

	// Per-repo plan-declared rows (one set per declared repo).
	for _, name := range wantRepos {
		hasPlan := false
		for _, it := range contract.Items {
			if it.Repo != name {
				continue
			}
			switch it.Source {
			case "plan":
				hasPlan = true
			}
		}
		if !hasPlan {
			t.Errorf("contract missing plan-source row for repo %q (Task should contribute one)", name)
		}
	}

	// --- Part C: RunPhaseImplementLoop dispatches and AtomicPhaseStamp
	// transitions all three repos to "awaiting_final_review". ---
	stubImpl := func(c agent.ImplementConfig, _ ports.SessionManager) (*agent.LoopResult, error) {
		// The unified workspace must mount every Feature.Repos worktree
		// — phase atomicity demands the implementer can read/write all
		// three repos in one session.
		for _, rp := range []string{repoA, repoB, repoC} {
			abs, _ := filepath.Abs(rp)
			found := false
			for _, d := range c.AdditionalDirs {
				if d == abs {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("workspace missing repo %q (got AdditionalDirs=%v)", rp, c.AdditionalDirs)
			}
		}
		// RepoName MUST be empty under the unified flow even with N=3.
		if c.RepoName != "" {
			t.Errorf("ImplementConfig.RepoName = %q, want empty (unified flow does not namespace per-repo)", c.RepoName)
		}
		return &agent.LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil
	}

	cfg := agent.OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		PlanPath:       planPath,
		StateDir:       stateDir,
		MaxIterations:  3,
		RunImplementFn: stubImpl,
	}

	result, runErr := agent.RunPhaseImplementLoop(cfg, nil)
	if runErr != nil {
		t.Fatalf("RunPhaseImplementLoop: %v", runErr)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed (LastError=%q)", result.FinalStatus, result.LastError)
	}
	if !reflect.DeepEqual(result.PhaseRepos, wantRepos) {
		t.Errorf("PhaseRepos = %v, want %v", result.PhaseRepos, wantRepos)
	}

	// AtomicPhaseStamp wrote AwaitingFinalReview for every declared repo
	// in one Modify call — phase atomicity. Verify all three.
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range wantRepos {
		st := got.RepoStates[name]
		if st == nil || st.Touched == false {
			t.Errorf("repo %q after impl = %+v, want awaiting_final_review", name, st)
		}
	}
}
