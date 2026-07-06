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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestTweakSession_MultiRepo_3Repo_TwoModified_OrchestratorCommitsAndPushes
// covers a 3-repo feature where the agent makes changes in two of the three
// repos during an interactive tweak session; the orchestrator commits both
// modified repos, pull-rebases each, and pushes each. The third clean repo is
// left alone.
//
// Setup: three scratch git repos, each with a bare upstream and the
// feature branch pushed. Two repos (api + backend) get pre-staged
// uncommitted working-tree changes simulating "the agent made changes";
// the third (frontend) is left clean. The orchestrator drives the
// feature-level commit + push chain via CompleteTweakCommit +
// CompleteTweakFinish using REAL git adapters (PublishAdapter +
// RebaseAdapter).
//
// Asserts:
//
//   - api + backend's PR branches advanced on the bare upstream (push
//     fired with the new commits).
//   - frontend's PR branch is unchanged on the bare upstream (no push).
//   - Feature stays StatusPublished throughout.
//   - Feature.ActiveCycle is cleared after the clean finish (no lingering
//     tweak entry).
//   - The cycle entries for every repo are removed (per-repo legacy
//     surface mirrors the feature-level outcome).
func TestTweakSession_MultiRepo_3Repo_TwoModified_OrchestratorCommitsAndPushes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// 1. Build three scratch git repos. Each has a bare upstream and a
	// feature/<slug> branch pushed.
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir stateDir: %v", err)
	}

	repoAPath := testutil.InitGitRepo(t)
	repoBPath := testutil.InitGitRepo(t)
	repoCPath := testutil.InitGitRepo(t)
	bareA := testutil.InitBareRemote(t, repoAPath)
	bareB := testutil.InitBareRemote(t, repoBPath)
	bareC := testutil.InitBareRemote(t, repoCPath)
	for _, rp := range []string{repoAPath, repoBPath, repoCPath, bareA, bareB, bareC} {
		runGit(t, rp, "config", "--local", "core.hooksPath", filepath.Join(tmpDir, "no-hooks"))
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "no-hooks"), 0o755); err != nil {
		t.Fatalf("mkdir no-hooks: %v", err)
	}

	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			Models: config.ModelConfig{
				Research:       "test-research",
				Planning:       "test-planning",
				Implementation: "test-impl",
				Review:         "test-review",
			},
			ExitCriteria:  "tests pass",
			MaxIterations: 1,
		},
		Repos: map[string]config.RepoConfig{
			"api":      {Path: repoAPath},
			"backend":  {Path: repoBPath},
			"frontend": {Path: repoCPath},
		},
	}
	store := feature.NewStore(stateDir)
	fm := feature.NewManager(store, cfg)
	feat, err := fm.Create(
		"tweak-multi",
		"3-repo tweak: orchestrator commits + pushes modified repos",
		[]string{"api", "backend", "frontend"},
		cfg.Defaults.Models,
		"",
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	branch := "feature/" + feat.Slug

	// Create the feature branch on every repo (locally + on bare upstream).
	for _, rp := range []string{repoAPath, repoBPath, repoCPath} {
		runGit(t, rp, "checkout", "-b", branch)
		writeFile(t, rp, "feature.txt", "feature start\n")
		runGit(t, rp, "add", "feature.txt")
		runGit(t, rp, "-c", "user.email=test@test.com", "-c", "user.name=Test", "commit", "-m", "feature start")
	}
	testutil.SimulatePush(t, repoAPath, bareA, branch, branch)
	testutil.SimulatePush(t, repoBPath, bareB, branch, branch)
	testutil.SimulatePush(t, repoCPath, bareC, branch, branch)

	// 2. Update feature.Repos to point at the real repo paths and branches.
	if err := store.Modify(feat.ID, func(f *feature.Feature) error {
		publishable := true
		f.Status = feature.StatusPublished
		f.CurrentPhase = feature.PhasePublish
		f.Checkpoints.ManualPublish = false
		for i := range f.Repos {
			r := &f.Repos[i]
			switch r.Name {
			case "api":
				r.Path = repoAPath
				r.WorktreePath = repoAPath
			case "backend":
				r.Path = repoBPath
				r.WorktreePath = repoBPath
			case "frontend":
				r.Path = repoCPath
				r.WorktreePath = repoCPath
			}
			r.Branch = branch
			r.Publishable = &publishable
		}
		return nil
	}); err != nil {
		t.Fatalf("Modify feature: %v", err)
	}

	// 3. Simulate the agent making changes in api + backend during the
	// tweak session. Per the SKILL prompt, the agent leaves them as
	// unstaged working-tree changes (no commit). frontend is left clean.
	writeFile(t, repoAPath, "tweak.txt", "api tweak\n")
	writeFile(t, repoBPath, "tweak.txt", "backend tweak\n")

	// Capture the bare-upstream HEAD on each repo's feature branch
	// before the orchestrator runs, so we can verify which repos
	// advanced after the push.
	preTweakHeads := map[string]string{
		"api":      bareHead(t, bareA, branch),
		"backend":  bareHead(t, bareB, branch),
		"frontend": bareHead(t, bareC, branch),
	}

	// 4. Build a fully-wired orchestrator with REAL git adapters.
	sm := session.NewManager(nil)
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: fm,
		Store:     store,
		Sessions:  sm,
		Publisher: &git.PublishAdapter{},
		Rebaser:   &git.RebaseAdapter{},
	}, orchestrator.Hooks{})

	// 5. Stamp the tweak cycle as if StartTweak ran. We can't run the
	// real RunTweakSession (no PTY in this test); we simulate the
	// post-session lifecycle by stamping ActiveCycle running directly
	// and opening the per-repo cycle entries — that is exactly what
	// orchestrator.StartTweak does before the session attaches.
	if err := store.Modify(feat.ID, func(f *feature.Feature) error {
		f.SetTweakCount(f.TweakCount() + 1)
		f.SetActiveCycleType(feature.CycleTweak)
		f.ActiveCycle = &feature.CycleState{
			Type:   feature.CycleTweak,
			Status: feature.RepoCycleRunning,
			Count:  f.TweakCount(),
		}
		return nil
	}); err != nil {
		t.Fatalf("stamp ActiveCycle: %v", err)
	}
	for _, name := range []string{"api", "backend", "frontend"} {
		if err := fm.StartRepoCycle(feat.ID, name, feature.CycleTweak); err != nil {
			t.Fatalf("StartRepoCycle %s: %v", name, err)
		}
	}

	// 6. Drive the orchestrator's post-session lifecycle:
	// CompleteTweakCommit + CompleteTweakFinish.
	hadChanges, err := orch.CompleteTweakCommit(feat.ID)
	if err != nil {
		t.Fatalf("CompleteTweakCommit: %v", err)
	}
	if !hadChanges {
		t.Errorf("CompleteTweakCommit hadChanges = false, want true (api + backend had unstaged changes)")
	}
	if err := orch.CompleteTweakFinish(feat.ID, hadChanges); err != nil {
		t.Fatalf("CompleteTweakFinish: %v (every modified repo's pull-rebase + push should have succeeded cleanly)", err)
	}

	// 7. Assert the bare-upstream HEAD advanced on api + backend, but
	// NOT on frontend.
	postA := bareHead(t, bareA, branch)
	postB := bareHead(t, bareB, branch)
	postC := bareHead(t, bareC, branch)
	if postA == preTweakHeads["api"] {
		t.Errorf("api bare HEAD did not advance (preTweak=%s, postTweak=%s) — Push did not fire for api", preTweakHeads["api"], postA)
	}
	if postB == preTweakHeads["backend"] {
		t.Errorf("backend bare HEAD did not advance (preTweak=%s, postTweak=%s) — Push did not fire for backend", preTweakHeads["backend"], postB)
	}
	if postC != preTweakHeads["frontend"] {
		t.Errorf("frontend bare HEAD changed (preTweak=%s, postTweak=%s) — Push fired for clean repo", preTweakHeads["frontend"], postC)
	}

	// 8. Assert feature stays Published; ActiveCycle cleared; per-repo
	// cycle entries removed.
	got, err := fm.Get(feat.ID)
	if err != nil {
		t.Fatalf("Get feature: %v", err)
	}
	if got.Status != feature.StatusPublished {
		t.Errorf("Feature.Status = %s, want StatusPublished", got.Status)
	}
	if got.ActiveCycle != nil {
		t.Errorf("Feature.ActiveCycle = %+v, want nil (clean tweak finish must clear)", got.ActiveCycle)
	}
	for _, name := range []string{"api", "backend", "frontend"} {
		if rc, ok := got.RepoCycles[name]; ok && rc != nil {
			t.Errorf("RepoCycles[%q] still present after clean tweak finish: %+v", name, rc)
		}
	}
}

// bareHead returns the SHA of <branch>'s tip on the bare repo. Used by
// the integration test to confirm whether each repo's PR branch was
// pushed (advanced) by the orchestrator.
func bareHead(t *testing.T, bareRepoPath, branch string) string {
	t.Helper()
	return runGit(t, bareRepoPath, "rev-parse", branch)
}
