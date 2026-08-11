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

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// newFRTestFeatureWithGitRepos is like newFRTestFeature but initializes each
// repo directory as a real git repo with an initial commit on a "main"
// branch. The FeatureRepo.Path points at the worktree so the anchor step
// can commit and capture SHAs.
func newFRTestFeatureWithGitRepos(t *testing.T, stateDir, featureID string, repoNames []string) (*feature.Store, *feature.Feature, map[string]string) {
	t.Helper()
	store := feature.NewStore(stateDir)
	repos := make([]feature.FeatureRepo, 0, len(repoNames))
	repoPaths := make(map[string]string, len(repoNames))
	repoStates := map[string]*feature.RepoState{}
	for _, name := range repoNames {
		repoDir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatalf("mkdir repo %q: %v", name, err)
		}
		if err := git.InitRepository(repoDir); err != nil {
			t.Fatalf("git init repo %q: %v", name, err)
		}
		repos = append(repos, feature.FeatureRepo{
			Name:       name,
			Path:       repoDir,
			BaseBranch: defaultTestBranch,
		})
		repoPaths[name] = repoDir
		repoStates[name] = &feature.RepoState{Touched: true}
	}
	f := &feature.Feature{
		ID:                  featureID,
		Name:                "FR Anchor Test",
		Slug:                "fr-anchor-test",
		Description:         "Anchor step integration test",
		ExitCriteria:        "Tests pass",
		Status:              feature.StatusFinalReviewing,
		CurrentPhase:        feature.PhaseReview,
		CurrentRoadmapPhase: 1,
		ActiveRun:           1,
		RunCount:            1,
		SchemaVersion:       feature.SchemaVersionCurrent,
		Repos:               repos,
		RepoStates:          repoStates,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	loaded, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	return store, loaded, repoPaths
}

// writeChangeToFile creates a file (or overwrites one) in repoPath and returns
// the file path.
func writeChangeToFile(t *testing.T, repoPath, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoPath, filename), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}

// TestFRAnchorStep_RecordsAnchorsInMeta verifies that after a Final Review
// iteration where the fix agent edits the worktree, the meta.yaml carries
// per-repo base/head SHAs and the worktree has a deterministic commit.
func TestFRAnchorStep_RecordsAnchorsInMeta(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repoPaths := newFRTestFeatureWithGitRepos(t, env.stateDir, "fr-anchor-meta", []string{testRepoNameAPI})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	// Fix agent writes a file in the repo worktree.
	fixBody := fmt.Sprintf(`echo 'package main' > '%s/fix.go'`, repoPaths[testRepoNameAPI])

	// Review: CHANGES_REQUESTED on iter-1, APPROVED on iter-2+.
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		fmt.Sprintf(`if [ -d "%s/iteration-02" ]; then
%s
%s
%s
%s
%s
else
%s
%s
%s
%s
%s
fi`,
			artDir,
			testutil.JSONLInit,
			testutil.WriteFinalReviewApproved(artDir),
			testutil.JSONLAssistant(agentStatusApproved),
			testutil.WriteFinalReviewApproved(artDir),
			testutil.JSONLSuccess,
			testutil.JSONLInit,
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: please fix"),
			testutil.JSONLAssistant(agentStatusChangesRequested),
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: please fix"),
			testutil.JSONLSuccess))

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			fixBody+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    fixScript,
		}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", result.Iterations)
	}

	// Read the iteration-02 meta and verify anchors.
	am := NewArtifactManager(artDir)
	iterDir := filepath.Join(artDir, fmt.Sprintf("iteration-%02d", result.Iterations))
	meta, err := am.ReadMeta(iterDir)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if len(meta.Anchors) == 0 {
		t.Fatal("expected anchors in meta, got none")
	}
	anchor, ok := meta.Anchors[testRepoNameAPI]
	if !ok {
		t.Fatalf("expected anchor for %s, got %+v", testRepoNameAPI, meta.Anchors)
	}
	if anchor.Base == "" || anchor.Head == "" {
		t.Errorf("anchor base/head should be non-empty, got base=%q head=%q", anchor.Base, anchor.Head)
	}
	// Verify the head SHA matches current HEAD.
	currentHead, _ := git.CurrentHeadSHA(repoPaths[testRepoNameAPI])
	if anchor.Head != currentHead {
		t.Errorf("anchor head %q != current HEAD %q", anchor.Head, currentHead)
	}
	// Verify worktree is clean after anchoring.
	if git.HasUncommittedChanges(repoPaths[testRepoNameAPI]) {
		t.Error("worktree should be clean after anchor step")
	}

	// Also verify iteration-01 meta has anchors (from the changes-requested path).
	iter01Dir := filepath.Join(artDir, "iteration-01")
	meta01, err := am.ReadMeta(iter01Dir)
	if err != nil {
		t.Fatalf("read iter01 meta: %v", err)
	}
	anchor01, ok := meta01.Anchors[testRepoNameAPI]
	if !ok {
		t.Fatal("expected anchor for api in iter01 meta")
	}
	if anchor01.Base == "" || anchor01.Head == "" {
		t.Errorf("iter01 anchor base/head should be non-empty, got base=%q head=%q", anchor01.Base, anchor01.Head)
	}
	// The fix agent wrote fix.go, so iter-01 anchor should have a new commit.
	if anchor01.Base == anchor01.Head {
		t.Errorf("iter01 base and head should differ (fix agent wrote a file), both = %q", anchor01.Base)
	}
}

// TestFRAnchorStep_CleanTreeRecordsHeadWithoutNewCommit verifies that a
// clean-tree iteration records the existing HEAD without creating a new
// commit (base == head).
func TestFRAnchorStep_CleanTreeRecordsHeadWithoutNewCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repoPaths := newFRTestFeatureWithGitRepos(t, env.stateDir, "fr-anchor-clean", []string{testRepoNameAPI})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	// Capture initial HEAD before the loop runs.
	initialHead, err := git.CurrentHeadSHA(repoPaths[testRepoNameAPI])
	if err != nil {
		t.Fatalf("get initial head: %v", err)
	}

	// Review approves immediately — no fix, no file changes.
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession:   mockBuildSessionByModel(map[string]string{"reviewer": reviewScript}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}

	am := NewArtifactManager(artDir)
	iterDir := filepath.Join(artDir, fmt.Sprintf("iteration-%02d", result.Iterations))
	meta, err := am.ReadMeta(iterDir)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	anchor, ok := meta.Anchors[testRepoNameAPI]
	if !ok {
		t.Fatal("expected anchor for api repo")
	}
	if anchor.Base != anchor.Head {
		t.Errorf("clean tree should have base == head, got base=%q head=%q", anchor.Base, anchor.Head)
	}
	if anchor.Head != initialHead {
		t.Errorf("anchor head %q != initial HEAD %q (should be same — no new commit)", anchor.Head, initialHead)
	}
}

// TestFRAnchorStep_DeterministicCommitMessage verifies the commit message
// follows the deterministic harness template (no sweep messages).
func TestFRAnchorStep_DeterministicCommitMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repoPaths := newFRTestFeatureWithGitRepos(t, env.stateDir, "fr-anchor-msg", []string{testRepoNameAPI})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	// Fix agent writes a file so the anchor step creates a commit.
	fixBody := fmt.Sprintf(`echo 'package main' > '%s/fix.go'`, repoPaths[testRepoNameAPI])

	// Review: CHANGES_REQUESTED on iter-1, APPROVED on iter-2+.
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		fmt.Sprintf(`if [ -d "%s/iteration-02" ]; then
%s
%s
%s
%s
%s
else
%s
%s
%s
%s
%s
fi`,
			artDir,
			testutil.JSONLInit,
			testutil.WriteFinalReviewApproved(artDir),
			testutil.JSONLAssistant(agentStatusApproved),
			testutil.WriteFinalReviewApproved(artDir),
			testutil.JSONLSuccess,
			testutil.JSONLInit,
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: please fix"),
			testutil.JSONLAssistant(agentStatusChangesRequested),
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: please fix"),
			testutil.JSONLSuccess))

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			fixBody+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    fixScript,
		}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", result.Iterations)
	}

	// Check the commit log for deterministic messages, not sweep messages.
	out := gitLog(t, repoPaths[testRepoNameAPI], "--format=%s", "-5")
	if strings.Contains(out, "leftover sweep") || strings.Contains(out, "fix sweep") {
		t.Errorf("found sweep message in commit log:\n%s", out)
	}
	if !strings.Contains(out, "Final review iteration") {
		t.Errorf("expected deterministic 'Final review iteration' message in commit log:\n%s", out)
	}
}

// TestFRAnchorStep_CrashRecoveryFoldsStrandedChanges verifies that a crash
// mid-iteration (iteration dir exists without meta.yaml) with uncommitted
// changes in the worktree resumes, re-runs the interrupted iteration, and
// folds the stranded changes into its anchor commit.
func TestFRAnchorStep_CrashRecoveryFoldsStrandedChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repoPaths := newFRTestFeatureWithGitRepos(t, env.stateDir, "fr-anchor-crash", []string{testRepoNameAPI})

	artDir := frArtifactDir(env.stateDir, f)

	// Simulate a crash: iteration-01 dir exists but has no meta.yaml
	// (loop ran review + fix but crashed before the meta write).
	// Stranded uncommitted changes from the crashed fix remain in the worktree.
	iter01 := filepath.Join(artDir, "iteration-01")
	if err := os.MkdirAll(iter01, 0o755); err != nil {
		t.Fatalf("mkdir iter01: %v", err)
	}
	// Write stranded change in the worktree (simulating the fix agent's output
	// that was never committed because the loop crashed before the anchor step).
	writeChangeToFile(t, repoPaths[testRepoNameAPI], "stranded.go", "package main\n")

	// Capture initial HEAD (before the stranded change is committed).
	initialHead, err := git.CurrentHeadSHA(repoPaths[testRepoNameAPI])
	if err != nil {
		t.Fatalf("get initial head: %v", err)
	}

	// The review script approves on the resumed iteration (iteration-01 re-run).
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession:   mockBuildSessionByModel(map[string]string{"reviewer": reviewScript}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	// LatestIteration returns 0 (no meta.yaml in iteration-01), so the loop
	// re-runs iteration 1 from scratch.
	if result.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1 (re-ran interrupted iter-01)", result.Iterations)
	}

	// Verify iteration-01 meta has anchors (the re-run's anchor step).
	am := NewArtifactManager(artDir)
	meta, err := am.ReadMeta(iter01)
	if err != nil {
		t.Fatalf("read iter01 meta: %v", err)
	}
	anchor, ok := meta.Anchors[testRepoNameAPI]
	if !ok {
		t.Fatal("expected anchor for api in iter01 meta")
	}
	if anchor.Base == "" || anchor.Head == "" {
		t.Errorf("anchor base/head should be non-empty, got base=%q head=%q", anchor.Base, anchor.Head)
	}
	// The stranded change should have been committed by the anchor step.
	// base should be the initial HEAD (before the stranded change), head should be the new commit.
	if anchor.Base != initialHead {
		t.Errorf("anchor base %q != initial HEAD %q", anchor.Base, initialHead)
	}
	if anchor.Base == anchor.Head {
		t.Errorf("base and head should differ (stranded change was committed), both = %q", anchor.Base)
	}
	if !fileExists(filepath.Join(repoPaths[testRepoNameAPI], "stranded.go")) {
		t.Error("stranded.go should exist in worktree")
	}
	if git.HasUncommittedChanges(repoPaths[testRepoNameAPI]) {
		t.Error("worktree should be clean after anchor step folded stranded changes")
	}
	// The commit log should contain the deterministic message.
	out := gitLog(t, repoPaths[testRepoNameAPI], "--format=%s", "-3")
	if !strings.Contains(out, "Final review iteration 1") {
		t.Errorf("expected 'Final review iteration 1' in commit log:\n%s", out)
	}
}

// TestFRAnchorStep_CrashAfterAnchorBeforeMeta verifies that a crash
// simulated after the anchor commit but before the meta write completes
// the loop without error and without duplicating change content across
// commits. On re-run, the already-committed changes are clean, so the
// second anchor is a no-op (base == head).
func TestFRAnchorStep_CrashAfterAnchorBeforeMeta(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repoPaths := newFRTestFeatureWithGitRepos(t, env.stateDir, "fr-anchor-crash2", []string{testRepoNameAPI})

	artDir := frArtifactDir(env.stateDir, f)

	// Capture the initial HEAD (before any anchor).
	initialHead, err := git.CurrentHeadSHA(repoPaths[testRepoNameAPI])
	if err != nil {
		t.Fatalf("get initial head: %v", err)
	}

	// Simulate: the anchor step already committed a change (simulating a
	// crash after commit but before meta write). We manually create the
	// change and commit it, then create iteration-01 without meta.yaml.
	writeChangeToFile(t, repoPaths[testRepoNameAPI], "anchored.go", "package main\n")
	_, err = git.CommitAllAndGetHead(repoPaths[testRepoNameAPI], "Final review iteration 1: changes requested")
	if err != nil {
		t.Fatalf("manual anchor commit: %v", err)
	}
	// The worktree is now clean (anchor committed), but no meta.yaml.
	iter01 := filepath.Join(artDir, "iteration-01")
	if err := os.MkdirAll(iter01, 0o755); err != nil {
		t.Fatalf("mkdir iter01: %v", err)
	}

	// Capture the head after the crash-anchor commit.
	crashAnchorHead, err := git.CurrentHeadSHA(repoPaths[testRepoNameAPI])
	if err != nil {
		t.Fatalf("get crash anchor head: %v", err)
	}

	// On re-run, iteration-01 re-runs (no meta.yaml → LatestIteration returns 0).
	// The anchor step finds a clean tree (the crash's commit already committed
	// everything), so base == head (no new commit, no duplicated content).
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession:   mockBuildSessionByModel(map[string]string{"reviewer": reviewScript}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	// Re-run starts at iteration 1 (no meta.yaml from the crash).
	if result.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", result.Iterations)
	}

	// Verify iteration-01 meta has anchors (clean tree → base == head).
	am := NewArtifactManager(artDir)
	meta, err := am.ReadMeta(iter01)
	if err != nil {
		t.Fatalf("read iter01 meta: %v", err)
	}
	anchor, ok := meta.Anchors[testRepoNameAPI]
	if !ok {
		t.Fatal("expected anchor for api in iter01 meta")
	}
	if anchor.Base != anchor.Head {
		t.Errorf("clean tree after crash-anchor should have base == head, got base=%q head=%q", anchor.Base, anchor.Head)
	}
	if anchor.Head != crashAnchorHead {
		t.Errorf("anchor head %q != crash anchor head %q", anchor.Head, crashAnchorHead)
	}

	// Verify no duplicate content: the "anchored.go" file was committed once
	// by the simulated crash anchor, and the re-run anchor did not create
	// a second commit with the same content.
	out := gitLog(t, repoPaths[testRepoNameAPI], "--format=%s", "-5")
	commitCount := strings.Count(out, "Final review iteration")
	if commitCount != 1 {
		// Only the simulated crash-anchor commit should have this message;
		// the re-run's anchor is a no-op (clean tree, no new commit).
		t.Errorf("expected 1 'Final review iteration' commit, got %d:\n%s", commitCount, out)
	}
	_ = initialHead
}

// TestFRAnchorStep_WorktreeCleanAfterAnchoring verifies that after the FR
// loop completes, all staged repo worktrees are clean — so commitRoadmapPhase
// (which uses CommitAllAndGetHead) would be a no-op returning the existing
// HEAD without creating a new commit.
func TestFRAnchorStep_WorktreeCleanAfterAnchoring(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repoPaths := newFRTestFeatureWithGitRepos(t, env.stateDir, "fr-anchor-clean-after", []string{testRepoNameAPI, testRepoNameWeb})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	// Fix agent writes files in both repos.
	fixBody := fmt.Sprintf(`echo 'package main' > '%s/fix.go'
echo 'package main' > '%s/fix.go'`, repoPaths[testRepoNameAPI], repoPaths[testRepoNameWeb])

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: fix both repos")+"\n"+
			testutil.JSONLAssistant(agentStatusChangesRequested)+"\n"+
			testutil.WriteFinalReviewChangesRequested(artDir, "- **High**: fix both repos")+"\n"+
			testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLAssistant(agentStatusApproved)+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			fixBody+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    fixScript,
		}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}

	// Verify all staged repo worktrees are clean.
	for name, path := range repoPaths {
		if git.HasUncommittedChanges(path) {
			t.Errorf("repo %s worktree should be clean after anchoring", name)
		}
		// Simulate commitRoadmapPhase: CommitAllAndGetHead on a clean tree
		// should return the existing HEAD without creating a new commit.
		headBefore, _ := git.CurrentHeadSHA(path)
		headAfter, err := git.CommitAllAndGetHead(path, "Phase 1/1 (test): should be no-op")
		if err != nil {
			t.Errorf("repo %s: CommitAllAndGetHead on clean tree failed: %v", name, err)
		}
		if headBefore != headAfter {
			t.Errorf("repo %s: CommitAllAndGetHead on clean tree created a new commit (before=%q after=%q)", name, headBefore, headAfter)
		}
	}
}

// TestFRAnchorStep_OmitsFailedRepoAnchors verifies that when a commit fails
// for a repo (non-fatal), that repo's anchors are omitted from meta while
// other repos are still anchored.
func TestFRAnchorStep_OmitsFailedRepoAnchors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repoPaths := newFRTestFeatureWithGitRepos(t, env.stateDir, "fr-anchor-fail", []string{testRepoNameAPI, testRepoNameWeb})

	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}

	// Make the "web" repo's directory non-functional for git by removing .git
	// after init (simulating a corrupted repo where CommitAllAndGetHead fails).
	if err := os.RemoveAll(filepath.Join(repoPaths[testRepoNameWeb], ".git")); err != nil {
		t.Fatalf("remove .git: %v", err)
	}

	// Fix agent writes a file in the api repo.
	fixBody := fmt.Sprintf(`echo 'package main' > '%s/fix.go'`, repoPaths[testRepoNameAPI])

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteFinalReviewApproved(artDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	fixScript := testutil.WriteScript(t, env.scriptsDir, "fix.sh",
		testutil.JSONLInit+"\n"+
			fixBody+"\n"+
			testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	cfg := OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    fixScript,
		}),
	}

	result, err := RunFeatureFinalReviewLoop(cfg, sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}

	// Read meta and verify api has anchors but web does not.
	am := NewArtifactManager(artDir)
	iterDir := filepath.Join(artDir, fmt.Sprintf("iteration-%02d", result.Iterations))
	meta, err := am.ReadMeta(iterDir)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if _, ok := meta.Anchors[testRepoNameAPI]; !ok {
		t.Error("expected anchor for api (working repo)")
	}
	if _, ok := meta.Anchors[testRepoNameWeb]; ok {
		t.Error("expected no anchor for web (corrupted repo)")
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func gitLog(t *testing.T, repoPath string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", repoPath, "log"}, args...)
	out, err := exec.Command("git", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v: %s", err, string(out))
	}
	return string(out)
}
