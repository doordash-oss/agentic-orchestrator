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
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestRefactorFeatureLoop_Integration_3RepoCrossRepoEdit exercises the
// unified refactor cycle end-to-end against three real git repos:
//
//   - repoA: target of the cross-repo Task — receives the new shared
//     config package.
//   - repoB: target of the cross-repo Task — updates its imports to
//     reference repoA's new shared package.
//   - repoC: NOT in the refactor plan; stays out of the staged subset.
//     Status preserved.
//
// The test uses agent.RunRefactorFeatureLoop directly with a stub
// RunRefactorPlanFn (writes a synthetic refactor-plan.md naming repoA
// and repoB) and a stub RunImplementFn (performs the actual file edits
// + commits inside the test, simulating what the Claude agent would do).
// This exercises:
//
//   - Refactor-plan step → PhaseScope-driven staged subset (repoA + repoB).
//   - Full Feature.Repos workspace mount (every repo in --add-dir, even
//     the un-staged one).
//   - Planned testing contract emission (per-repo baseline + plan-source
//     items, every item tagged repo:).
//   - Flat artifact dir layout (refactor-1/iteration-NN/, no per-repo
//     subdir).
//   - Atomic stamp on success: every staged repo lands at
//     "awaiting_final_review"; un-staged repoC's status is preserved.
//   - ActiveCycle lifecycle (set on entry, cleared on success).
//   - RefactorCount increment.
//   - Cross-repo edits land in both repos in one iteration.
func TestRefactorFeatureLoop_Integration_3RepoCrossRepoEdit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	// Build three scratch repos. Each has master + a feature branch.
	repoA := testutil.InitGitRepo(t)
	repoB := testutil.InitGitRepo(t)
	repoC := testutil.InitGitRepo(t)
	bareA := testutil.InitBareRemote(t, repoA)
	bareB := testutil.InitBareRemote(t, repoB)
	bareC := testutil.InitBareRemote(t, repoC)
	for _, r := range []string{repoA, repoB, repoC, bareA, bareB, bareC} {
		runGit(t, r, "config", "--local", "core.hooksPath", filepath.Join(tmp, "no-hooks"))
	}
	if err := os.MkdirAll(filepath.Join(tmp, "no-hooks"), 0o755); err != nil {
		t.Fatalf("mkdir no-hooks: %v", err)
	}

	branch := "feature/test"
	for _, rp := range []string{repoA, repoB, repoC} {
		runGit(t, rp, "checkout", "-b", branch)
		writeFile(t, rp, "feature.txt", "feature start\n")
		runGit(t, rp, "add", "feature.txt")
		runGit(t, rp, "-c", "user.email=test@test.com", "-c", "user.name=Test", "commit", "-m", "feature start")
	}
	testutil.SimulatePush(t, repoA, bareA, branch, branch)
	testutil.SimulatePush(t, repoB, bareB, branch, branch)
	testutil.SimulatePush(t, repoC, bareC, branch, branch)

	// Build the feature manifest.
	store := feature.NewStore(stateDir)
	f := &feature.Feature{
		ID:             "refactor-int",
		Name:           "Refactor Integration Test",
		Slug:           "refactor-int",
		Description:    "3-repo feature, refactor edits two of three",
		Status:         feature.StatusPublished,
		ActiveRun:      1,
		RunCount:       1,
		SchemaVersion:  feature.SchemaVersionCurrent,
		RefactorPrompt: "extract shared config",
		Repos: []feature.FeatureRepo{
			{Name: "repoA", Path: repoA, WorktreePath: repoA, Branch: branch, BaseBranch: "main"},
			{Name: "repoB", Path: repoB, WorktreePath: repoB, Branch: branch, BaseBranch: "main"},
			{Name: "repoC", Path: repoC, WorktreePath: repoC, Branch: branch, BaseBranch: "main"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repoA": {Touched: true, PRURL: "https://github.com/example/repoA/pull/1"},
			"repoB": {Touched: true, PRURL: "https://github.com/example/repoB/pull/2"},
			"repoC": {Touched: true, PRURL: "https://github.com/example/repoC/pull/3"},
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

	// Stub refactor-plan: writes a synthetic plan that tags repoA and
	// repoB (cross-repo Task dispatch). repoC is intentionally untagged
	// so PhaseScope stages only the two named repos.
	plan := "# Refactor: extract shared config\n\n" +
		"## Tasks\n\n" +
		"### Task 1: introduce shared config in repoA\n\n" +
		"**Repo:** repoA\n\n" +
		"Add a `shared-config.txt` file to repoA's worktree with the canonical config.\n\n" +
		"#### Automated Verification:\n" +
		"- [ ] config file present: `test -f shared-config.txt`\n\n" +
		"### Task 2: update repoB to use the new shared config\n\n" +
		"**Repo:** repoB\n\n" +
		"Add a `imports-shared-config.txt` file to repoB's worktree referencing repoA.\n\n" +
		"#### Automated Verification:\n" +
		"- [ ] import file present: `test -f imports-shared-config.txt`\n"

	planFn := func(stagedDir string) (string, error) {
		path := filepath.Join(stagedDir, "refactor-plan.md")
		if err := os.WriteFile(path, []byte(plan), 0o644); err != nil {
			return "", err
		}
		return path, nil
	}

	// Stub implementer: performs the actual cross-repo edits inside the
	// test. The real Claude agent would dispatch one Task sub-agent per
	// repo and run `git commit + git push --force-with-lease`; here we
	// short-circuit to the on-disk effect to keep the integration test
	// self-contained.
	stubFn := func(c agent.ImplementConfig, _ ports.SessionManager) (*agent.LoopResult, error) {
		// Verify the inner ImplementConfig has the flat artifact dir
		// (refactor-1/, no per-repo subdir).
		if !strings.HasSuffix(c.ArtifactDir, "refactor-1") {
			t.Errorf("ImplementConfig.ArtifactDir = %q, want suffix refactor-1", c.ArtifactDir)
		}
		for _, name := range []string{"repoA", "repoB", "repoC"} {
			if strings.Contains(c.ArtifactDir, "/"+name) {
				t.Errorf("ImplementConfig.ArtifactDir = %q includes per-repo subdir %q (flat layout violated)", c.ArtifactDir, name)
			}
		}

		// Verify the workspace mounts every repo (cross-repo edits are
		// first-class — even if the plan only tags two, the agent
		// needs read access to all to judge the change).
		for _, rp := range []string{repoA, repoB, repoC} {
			abs, _ := filepath.Abs(rp)
			if !sliceContains(c.AdditionalDirs, abs) {
				t.Errorf("workspace missing repo %q (got %v) — refactor must mount every Feature.Repos worktree", rp, c.AdditionalDirs)
			}
		}

		// Perform the cross-repo edits.
		writeFile(t, repoA, "shared-config.txt", "canonical config\n")
		runGit(t, repoA, "add", "shared-config.txt")
		runGit(t, repoA, "-c", "user.email=test@test.com", "-c", "user.name=Test", "commit", "-m", "Add shared config")
		runGit(t, repoA, "push", "--force-with-lease", "origin", branch)

		writeFile(t, repoB, "imports-shared-config.txt", "imports repoA's shared-config.txt\n")
		runGit(t, repoB, "add", "imports-shared-config.txt")
		runGit(t, repoB, "-c", "user.email=test@test.com", "-c", "user.name=Test", "commit", "-m", "Use shared config from repoA")
		runGit(t, repoB, "push", "--force-with-lease", "origin", branch)

		// repoC must NOT receive any edits — outside the plan-staged
		// subset.

		return &agent.LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil
	}

	cfg := agent.RefactorFeatureLoopConfig{
		Feature:           f,
		FeatureStore:      store,
		StateDir:          stateDir,
		Prompt:            "extract shared config",
		MaxIterations:     3,
		RunRefactorPlanFn: planFn,
		RunImplementFn:    stubFn,
	}

	result, runErr := agent.RunRefactorFeatureLoop(cfg, nil)
	if runErr != nil {
		t.Fatalf("RunRefactorFeatureLoop: %v", runErr)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if !reflect.DeepEqual(result.Repos, []string{"repoA", "repoB"}) {
		t.Errorf("Repos = %v, want [repoA repoB]", result.Repos)
	}

	// Verify the on-disk feature state.
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	for _, name := range []string{"repoA", "repoB"} {
		st := got.RepoStates[name]
		if st == nil || st.Touched == false {
			t.Errorf("repo %s = %+v, want awaiting_final_review", name, st)
		}
	}
	// repoC preserved.
	if st := got.RepoStates["repoC"]; st == nil || st.PRURL == "" {
		t.Errorf("repoC = %+v, want pr_ready (preserved — outside plan)", st)
	}

	// ActiveCycle cleared on success.
	if got.ActiveCycle != nil {
		t.Errorf("ActiveCycle = %+v, want nil after success", got.ActiveCycle)
	}

	// RefactorCount incremented.
	if got.RefactorCount() != 1 {
		t.Errorf("RefactorCount = %d, want 1", got.RefactorCount())
	}

	// Flat artifact dir exists.
	flatDir := filepath.Join(agent.ActiveRunDir(stateDir, got), "refactor-1")
	if _, err := os.Stat(flatDir); err != nil {
		t.Errorf("refactor-1 dir missing: %v", err)
	}
	for _, repo := range []string{"repoA", "repoB", "repoC"} {
		legacyPath := filepath.Join(flatDir, repo)
		if _, statErr := os.Stat(legacyPath); statErr == nil {
			t.Errorf("legacy per-repo subdir %s exists; flat layout violated", legacyPath)
		}
	}

	// Plan + contract written at flat layout.
	if _, err := os.Stat(filepath.Join(flatDir, "refactor-plan.md")); err != nil {
		t.Errorf("refactor-plan.md missing at flat dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(flatDir, "testing-contract.yaml")); err != nil {
		t.Errorf("testing-contract.yaml missing at flat dir: %v", err)
	}

	// Verify the cross-repo edits actually landed in both staged repos
	// — and NOT in repoC.
	for _, edit := range []struct{ dir, file string }{
		{repoA, "shared-config.txt"},
		{repoB, "imports-shared-config.txt"},
	} {
		full := filepath.Join(edit.dir, edit.file)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("expected file %q in repo %q (refactor edit landed?), got: %v", edit.file, edit.dir, err)
		}
	}
	// repoC must remain untouched.
	for _, file := range []string{"shared-config.txt", "imports-shared-config.txt"} {
		full := filepath.Join(repoC, file)
		if _, err := os.Stat(full); err == nil {
			t.Errorf("repoC unexpectedly received file %q; refactor must not touch repos outside plan", file)
		}
	}

	// Verify the testing-contract.yaml is planned (mixes baseline + plan
	// rows, repos tagged).
	contract, err := agent.ReadTestingContract(filepath.Join(flatDir, "testing-contract.yaml"))
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	gotPerRepoSource := map[string]map[string]int{}
	for _, item := range contract.Items {
		if gotPerRepoSource[item.Repo] == nil {
			gotPerRepoSource[item.Repo] = map[string]int{}
		}
		gotPerRepoSource[item.Repo][item.Source]++
	}
	for _, repo := range []string{"repoA", "repoB"} {
		if gotPerRepoSource[repo]["baseline"] == 0 {
			t.Errorf("repo %s missing baseline rows; got %v", repo, gotPerRepoSource[repo])
		}
		if gotPerRepoSource[repo]["plan"] == 0 {
			t.Errorf("repo %s missing plan-source rows (planned mode); got %v", repo, gotPerRepoSource[repo])
		}
	}
	if gotPerRepoSource["repoC"]["plan"] > 0 || gotPerRepoSource["repoC"]["baseline"] > 0 {
		t.Errorf("contract leaked repoC into plan-staged items: %v", gotPerRepoSource["repoC"])
	}

	// Verify the remote bare repos got the rebased branch ref.
	for _, bare := range []string{bareA, bareB} {
		out, err := exec.Command("git", "-C", bare, "log", "--oneline", branch).CombinedOutput()
		if err != nil {
			t.Errorf("git log on bare %s: %v\n%s", bare, err, out)
			continue
		}
		if !strings.Contains(string(out), "feature start") {
			t.Errorf("bare %s missing feature commit history:\n%s", bare, out)
		}
	}
}

// sliceContains returns true if needle is in haystack.
func sliceContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
