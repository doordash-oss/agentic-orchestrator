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
	"encoding/json"
	"fmt"
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

// TestReviewCommentsLoop_Integration_3RepoTwoWithComments exercises the
// unified feature-level review-comments cycle end-to-end against three
// real git repos:
//
//   - repoA: feature branch with two unaddressed PR comments.
//   - repoB: feature branch with one unaddressed PR comment.
//   - repoC: feature branch with NO unaddressed comments. Stays out of
//     the staged subset; AtomicPhaseStamp must NOT touch its status.
//
// The test uses agent.RunReviewCommentsLoop directly with a stub
// RunImplementFn that performs the actual code edits + force-pushes
// inside the test (simulating what the Claude agent would do in
// production). This exercises:
//
//   - Cross-PR aggregation (every comment from repoA + repoB reaches
//     the implement plan).
//   - Full-workspace mount (repoA + repoB + repoC ALL in --add-dir,
//     even though repoC has no comments).
//   - Plan-less testing contract (per-repo baseline rows only, no
//     plan-source items).
//   - Flat artifact dir layout
//     (review-comments-1/iteration-NN/, no per-repo subdir).
//   - Atomic stamp on success: every staged repo lands at
//     "awaiting_final_review"; repoC's status is preserved.
//   - ActiveCycle lifecycle (set on entry, cleared on success).
//   - ReviewCommentsCount increment.
//   - Combined review-resolutions.json at the cycle root (one entry
//     per aggregated comment, no per-repo subdir).
func TestReviewCommentsLoop_Integration_3RepoTwoWithComments(t *testing.T) {
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
	bareA := testutil.InitBareRemote(t, repoA)
	bareB := testutil.InitBareRemote(t, repoB)
	bareC := testutil.InitBareRemote(t, repoC)
	for _, r := range []string{repoA, repoB, repoC, bareA, bareB, bareC} {
		runGit(t, r, "config", "--local", "core.hooksPath", filepath.Join(tmp, "no-hooks"))
	}
	if err := os.MkdirAll(filepath.Join(tmp, "no-hooks"), 0o755); err != nil {
		t.Fatalf("mkdir no-hooks: %v", err)
	}

	// Each repo: feature branch with one initial commit on a known
	// file the simulated implementer will edit to "address" the
	// comment.
	branch := "feature/test"
	for _, rp := range []string{repoA, repoB, repoC} {
		runGit(t, rp, "checkout", "-b", branch)
		writeFile(t, rp, "src/handler.go", "package handler\n\nfunc Handler() string { return \"v1\" }\n")
		runGit(t, rp, "add", "src/handler.go")
		runGit(t, rp,
			"-c", "user.email=test@test.com", "-c", "user.name=Test",
			"commit", "-m", "initial handler",
		)
	}
	testutil.SimulatePush(t, repoA, bareA, branch, branch)
	testutil.SimulatePush(t, repoB, bareB, branch, branch)
	testutil.SimulatePush(t, repoC, bareC, branch, branch)

	// Build the feature manifest. RepoImpl statuses start at CodeReady
	// (post-publish steady state). Comments are seeded directly on the
	// loop's RepoTargets (the orchestrator entry-point reads them from
	// per-repo `comments.json`; we bypass it to keep this test focused
	// on the loop semantics).
	store := feature.NewStore(stateDir)
	f := &feature.Feature{
		ID:            "rc-int",
		Name:          "Review Comments Integration Test",
		Slug:          "rc-int",
		Description:   "3-repo feature, two with comments, integration",
		Status:        feature.StatusPublished,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
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

	// Build per-repo comment slices. repoA has 2 comments; repoB has
	// 1 comment; repoC has 0.
	cA1 := ports.ReviewComment{ID: 100, Path: "src/handler.go", Line: 3, Body: "rename to Handle()", Type: ports.CommentTypeReview}
	cA1.User.Login = "alice"
	cA2 := ports.ReviewComment{ID: 101, Body: "Add a doc comment to the package", Type: ports.CommentTypeIssue}
	cA2.User.Login = "alice"
	cB1 := ports.ReviewComment{ID: 200, Path: "src/handler.go", Line: 3, Body: "version bump", Type: ports.CommentTypeReview}
	cB1.User.Login = "bob"

	stubFn := func(c agent.ImplementConfig, _ ports.SessionManager) (*agent.LoopResult, error) {
		// Verify the inner ImplementConfig has the flat artifact dir.
		if !strings.HasSuffix(c.ArtifactDir, "review-comments-1") {
			t.Errorf("ImplementConfig.ArtifactDir = %q, want suffix review-comments-1", c.ArtifactDir)
		}
		if strings.Contains(c.ArtifactDir, "/repoA") || strings.Contains(c.ArtifactDir, "/repoB") {
			t.Errorf("ImplementConfig.ArtifactDir = %q includes per-repo subdir (flat layout violated)", c.ArtifactDir)
		}

		// Verify the inner ImplementConfig.AdditionalDirs mounts ALL
		// three repos (full workspace, not just behind subset).
		repoAAbs, _ := filepath.Abs(repoA)
		repoBAbs, _ := filepath.Abs(repoB)
		repoCAbs, _ := filepath.Abs(repoC)
		mounted := map[string]bool{}
		for _, d := range c.AdditionalDirs {
			mounted[d] = true
		}
		if !mounted[repoAAbs] {
			t.Errorf("AdditionalDirs missing repoA worktree %q", repoAAbs)
		}
		if !mounted[repoBAbs] {
			t.Errorf("AdditionalDirs missing repoB worktree %q", repoBAbs)
		}
		if !mounted[repoCAbs] {
			t.Errorf("AdditionalDirs missing repoC worktree %q (review-comments mounts the FULL workspace, not the staged subset)", repoCAbs)
		}

		// Verify the aggregated review plan was written and contains
		// every comment ID across both PRs.
		planBytes, readErr := os.ReadFile(c.PlanPath)
		if readErr != nil {
			t.Fatalf("read review plan: %v", readErr)
		}
		plan := string(planBytes)
		for _, want := range []string{
			"## Repo: `repoA`",
			"## Repo: `repoB`",
			"ID: 100)",
			"ID: 101)",
			"ID: 200)",
			"`repo: repoA`",
			"`repo: repoB`",
		} {
			if !strings.Contains(plan, want) {
				t.Errorf("aggregated plan missing %q", want)
			}
		}
		if strings.Contains(plan, "## Repo: `repoC`") {
			t.Errorf("aggregated plan unexpectedly includes repoC (no comments)")
		}

		// Simulate the agent's per-repo edits + force-push. Comment 100
		// asks to rename Handler→Handle (repoA); 101 asks for a doc
		// comment (repoA); 200 is a version bump (repoB).
		writeFile(t, repoA, "src/handler.go", "// Package handler exposes Handle.\npackage handler\n\nfunc Handle() string { return \"v1\" }\n")
		runGit(t, repoA, "add", "src/handler.go")
		runGit(t, repoA,
			"-c", "user.email=test@test.com", "-c", "user.name=Test",
			"commit", "-m", "Address review comments",
		)
		runGit(t, repoA, "push", "origin", branch)

		writeFile(t, repoB, "src/handler.go", "package handler\n\nfunc Handler() string { return \"v2\" }\n")
		runGit(t, repoB, "add", "src/handler.go")
		runGit(t, repoB,
			"-c", "user.email=test@test.com", "-c", "user.name=Test",
			"commit", "-m", "Address review comments",
		)
		runGit(t, repoB, "push", "origin", branch)

		// Write the combined resolutions JSON at the cycle root.
		// Reverse-engineer the resolutions path from the inner
		// ImplementConfig's ArtifactDir (flat layout).
		resolutionsPath := filepath.Join(c.ArtifactDir, "review-resolutions.json")
		resolutions := []agent.ReviewResolution{
			{CommentID: 100, Disposition: "addressed", Description: "Renamed Handler to Handle"},
			{CommentID: 101, Disposition: "addressed", Description: "Added package doc comment"},
			{CommentID: 200, Disposition: "addressed", Description: "Bumped version to v2"},
		}
		raw, _ := json.MarshalIndent(resolutions, "", "  ")
		if err := os.WriteFile(resolutionsPath, raw, 0o644); err != nil {
			t.Fatalf("write resolutions: %v", err)
		}

		return &agent.LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil
	}

	cfg := agent.ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []agent.ReviewCommentsRepoTarget{
			{
				RepoName: "repoA",
				PRURL:    "https://github.com/example/repoA/pull/1",
				Mode:     "auto",
				Comments: []ports.ReviewComment{cA1, cA2},
			},
			{
				RepoName: "repoB",
				PRURL:    "https://github.com/example/repoB/pull/2",
				Mode:     "auto",
				Comments: []ports.ReviewComment{cB1},
			},
		},
		MaxIterations:  3,
		RunImplementFn: stubFn,
	}

	result, runErr := agent.RunReviewCommentsLoop(cfg, nil)
	if runErr != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", runErr)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if !reflect.DeepEqual(result.Repos, []string{"repoA", "repoB"}) {
		t.Errorf("Repos = %v, want [repoA repoB]", result.Repos)
	}

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
		t.Errorf("repoC = %+v, want pr_ready (preserved — no comments staged)", st)
	}

	if got.ActiveCycle != nil {
		t.Errorf("ActiveCycle = %+v, want nil after success", got.ActiveCycle)
	}
	if got.ReviewCommentsCount() != 1 {
		t.Errorf("ReviewCommentsCount = %d, want 1", got.ReviewCommentsCount())
	}

	flatDir := filepath.Join(agent.ActiveRunDir(stateDir, got), "review-comments-1")
	if _, err := os.Stat(flatDir); err != nil {
		t.Errorf("review-comments-1 dir missing: %v", err)
	}
	for _, repo := range []string{"repoA", "repoB", "repoC"} {
		legacyPath := filepath.Join(flatDir, repo)
		if _, err := os.Stat(legacyPath); err == nil {
			t.Errorf("legacy per-repo subdir %q exists; flat layout violated", legacyPath)
		}
	}

	if _, err := os.Stat(filepath.Join(flatDir, "review-plan.md")); err != nil {
		t.Errorf("review-plan.md missing at flat dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(flatDir, "testing-contract.yaml")); err != nil {
		t.Errorf("testing-contract.yaml missing at flat dir: %v", err)
	}

	// Verify the plan-less contract contains no guessed commands.
	contract, err := agent.ReadTestingContract(filepath.Join(flatDir, "testing-contract.yaml"))
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	for _, item := range contract.Items {
		if item.Source == "plan" {
			t.Errorf("plan-source item leaked into plan-less review-comments contract: %+v", item)
		}
	}
	if len(contract.Items) != 0 {
		t.Errorf("plan-less review-comments contract contains guessed items: %+v", contract.Items)
	}

	// Combined resolutions JSON at the flat root, with one entry per
	// aggregated comment. No per-repo subdir.
	combinedResolutions := filepath.Join(flatDir, "review-resolutions.json")
	rb, err := os.ReadFile(combinedResolutions)
	if err != nil {
		t.Fatalf("read combined resolutions: %v", err)
	}
	var resolutions []agent.ReviewResolution
	if err := json.Unmarshal(rb, &resolutions); err != nil {
		t.Fatalf("unmarshal resolutions: %v", err)
	}
	if len(resolutions) != 3 {
		t.Errorf("combined resolutions = %d entries, want 3 (one per aggregated comment)", len(resolutions))
	}
	gotIDs := map[int]bool{}
	for _, r := range resolutions {
		gotIDs[r.CommentID] = true
	}
	for _, want := range []int{100, 101, 200} {
		if !gotIDs[want] {
			t.Errorf("combined resolutions missing comment ID %d", want)
		}
	}

	// Verify the actual edits landed in each touched repo.
	for _, rp := range []string{repoA, repoB} {
		// Branch should have the new "Address review comments" commit.
		out, err := exec.Command("git", "-C", rp, "log", "--oneline", "-1").CombinedOutput()
		if err != nil {
			t.Errorf("git log for %s: %v\n%s", rp, err, out)
			continue
		}
		if !strings.Contains(string(out), "Address review comments") {
			t.Errorf("%s missing the address-comments commit:\n%s", rp, out)
		}
	}

	// Verify repoA was edited (Handler → Handle + doc comment).
	repoAContent, _ := os.ReadFile(filepath.Join(repoA, "src/handler.go"))
	if !strings.Contains(string(repoAContent), "func Handle()") {
		t.Errorf("repoA src/handler.go missing Handle() rename:\n%s", repoAContent)
	}
	if !strings.Contains(string(repoAContent), "// Package handler") {
		t.Errorf("repoA src/handler.go missing package doc comment:\n%s", repoAContent)
	}

	// Verify repoB was edited (v1 → v2).
	repoBContent, _ := os.ReadFile(filepath.Join(repoB, "src/handler.go"))
	if !strings.Contains(string(repoBContent), "v2") {
		t.Errorf("repoB src/handler.go missing v2 bump:\n%s", repoBContent)
	}

	// Verify repoC stayed untouched.
	repoCContent, _ := os.ReadFile(filepath.Join(repoC, "src/handler.go"))
	if !strings.Contains(string(repoCContent), "v1") || strings.Contains(string(repoCContent), "v2") {
		t.Errorf("repoC src/handler.go was modified despite having no comments:\n%s", repoCContent)
	}

	// Verify both branches were force-pushed (the bare repos should
	// have the new commit).
	for _, bare := range []string{bareA, bareB} {
		out, err := exec.Command("git", "-C", bare, "log", "--oneline", branch, "-1").CombinedOutput()
		if err != nil {
			t.Errorf("git log %s for %s: %v\n%s", branch, bare, err, out)
			continue
		}
		if !strings.Contains(string(out), "Address review comments") {
			t.Errorf("bare %s missing the address-comments commit on %s:\n%s", bare, branch, out)
		}
	}

	_ = fmt.Sprintf // keep import live
}
