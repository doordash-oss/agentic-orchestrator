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

// TestPhaseImplementUnified_3Repo_CrossRepoVerification is the slice-2
// acceptance test for the "phase atomicity + cross-repo verification"
// invariants on a 3-repo phase:
//
//   - PhaseScope reads `**Repo:** repo-a/repo-b/repo-c` Task tags from a
//     planned-mode plan and produces the deduplicated, sorted repo set.
//   - CompileTestingContractMultiRepo emits per-repo baseline rows + plan-
//     source rows tagged with `repo: <name>` for each declared repo, plus
//     `repo: cross-repo` rows for the planner's `## Cross-Repo Verification`
//     entries.
//   - RunPhaseImplementLoop dispatches the iteration successfully and
//     AtomicPhaseStamp transitions all three repos atomically to
//     "awaiting_final_review" in one Modify write (phase atomicity:
//     no partial-phase shipment).
//
// The test uses real on-disk plan + state, real PhaseScope/contract
// compiler, and a stub RunImplementFn (the loop kernel is unit-tested
// elsewhere).
func TestPhaseImplementUnified_3Repo_CrossRepoVerification(t *testing.T) {
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
		ID:            "phase-3repo-xrepo",
		Name:          "3-repo cross-repo verification",
		Slug:          "phase-3repo-xrepo",
		Description:   "Phase plan with three Tasks plus cross-repo verification",
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

	// Plan with three Tasks (one per repo) plus a cross-repo verification
	// section. The implementer's Tasks tag with `**Repo:** <name>`; the
	// `## Cross-Repo Verification` block declares verification commands
	// that exercise more than one repo at once and must compile to
	// `repo: cross-repo` items.
	planDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "implement")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	planText := "# Plan\n\n" +
		"## Tasks\n\n" +
		"### Task 1: API work\n\n" +
		"**Repo:** repo-a\n\n" +
		"Update the API handlers.\n\n" +
		"#### Automated Verification:\n" +
		"- [ ] api tests: `go test ./api/...`\n\n" +
		"### Task 2: Web work\n\n" +
		"**Repo:** repo-b\n\n" +
		"Update the web client.\n\n" +
		"#### Automated Verification:\n" +
		"- [ ] web tests: `npm test`\n\n" +
		"### Task 3: Infra work\n\n" +
		"**Repo:** repo-c\n\n" +
		"Update the infra config.\n\n" +
		"#### Automated Verification:\n" +
		"- [ ] infra tests: `terraform validate`\n\n" +
		"## Cross-Repo Verification\n\n" +
		"- e2e smoke: `scripts/e2e.sh`\n" +
		"- contract test: `scripts/contract-test.sh`\n"
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

	// --- Part B: TestingContractCompiler emits per-repo baseline +
	// plan-source items + `cross-repo` items, every item tagged with
	// `repo:`. Cross-repo rows are extracted from the plan's
	// `## Cross-Repo Verification` section (production path).
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

	// Per-repo baseline rows (one set per declared repo).
	for _, name := range wantRepos {
		hasBaseline := false
		hasPlan := false
		for _, it := range contract.Items {
			if it.Repo != name {
				continue
			}
			switch it.Source {
			case "baseline":
				hasBaseline = true
			case "plan":
				hasPlan = true
			}
		}
		if !hasBaseline {
			t.Errorf("contract missing baseline row for repo %q", name)
		}
		if !hasPlan {
			t.Errorf("contract missing plan-source row for repo %q (Task should contribute one)", name)
		}
	}

	// Cross-repo rows: one per cross-repo verification command, tagged
	// `repo: cross-repo`.
	crossRepoCount := 0
	for _, it := range contract.Items {
		if it.Source == "cross-repo" {
			if it.Repo != agent.TestingContractCrossRepoTag {
				t.Errorf("cross-repo item has repo = %q, want %q", it.Repo, agent.TestingContractCrossRepoTag)
			}
			crossRepoCount++
		}
	}
	if crossRepoCount != 2 {
		t.Errorf("cross-repo item count = %d, want 2", crossRepoCount)
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
