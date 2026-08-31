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

package orchestrator_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// newRoundCommitHarness wires an Orchestrator with the round-commit hook
// installed on a PhaseRunner (as production New does) and returns the hook
// plus the orchestrator whose event bus the hook emits onto.
func newRoundCommitHarness(t *testing.T, f *feature.Feature) (agent.RoundCommitHook, *orchestrator.Orchestrator) {
	t.Helper()
	pr := &agent.PhaseRunner{}
	lc := lifecycleForFeature(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(f), PhaseRunner: pr}, orchestrator.Hooks{})
	if pr.RoundCommitHook == nil {
		t.Fatal("orchestrator.New must install the round commit hook on the PhaseRunner")
	}
	return pr.RoundCommitHook, o
}

func roadmapFeature(t *testing.T, repoPath string) *feature.Feature {
	t.Helper()
	roadmapPath := writeTempFile(t, "roadmap.md",
		"# Roadmap\n\n## Phase 1: Tracer\n### Goal\nFirst phase.\n\n## Phase 2: Fill\n### Goal\nSecond phase.\n")
	return &feature.Feature{
		ID:                  "feat-round-commits",
		Slug:                "round-commit-messages",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  2,
		RoadmapPhaseType:    "tracer-bullet",
		Artifacts:           map[string]string{"roadmap": roadmapPath},
		Repos:               []feature.FeatureRepo{{Name: "solo", Path: repoPath, WorktreePath: repoPath}},
	}
}

func repoCommitBodies(t *testing.T, repoPath string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "log", "--format=%B%n---commit---").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	var bodies []string
	for _, body := range strings.Split(string(out), "---commit---") {
		if strings.TrimSpace(body) != "" {
			bodies = append(bodies, strings.TrimSpace(body))
		}
	}
	// Newest first; reverse so bodies read in commit order.
	for i, j := 0, len(bodies)-1; i < j; i, j = i+1, j-1 {
		bodies[i], bodies[j] = bodies[j], bodies[i]
	}
	return bodies
}

func dirtyRepo(t *testing.T, repoPath, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoPath, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// The round-commit message sequence must read like the agreed git history:
// unlabeled first implementation round, labeled extra implementation rounds,
// per-phase fix-round counter, and feature-level final-review fixes.
func TestOrchestrator_RoundCommitHook_MessageSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	repo := testutil.InitGitRepo(t)
	f := roadmapFeature(t, repo)
	hook, _ := newRoundCommitHarness(t, f)

	type round struct {
		input agent.RoundCommitInput
		file  string
	}
	rounds := []round{
		{
			input: agent.RoundCommitInput{FeatureID: f.ID, PhaseNumber: 1, TotalPhases: 2, PhaseType: "tracer-bullet", Iteration: 1, Kind: agent.RoundCommitImplement, FirstImplementCommit: true},
			file:  "round-1.txt",
		},
		{
			input: agent.RoundCommitInput{FeatureID: f.ID, PhaseNumber: 1, TotalPhases: 2, PhaseType: "tracer-bullet", Iteration: 3, Kind: agent.RoundCommitImplement, FirstImplementCommit: false},
			file:  "round-3.txt",
		},
		{
			input: agent.RoundCommitInput{FeatureID: f.ID, PhaseNumber: 1, TotalPhases: 2, PhaseType: "tracer-bullet", Iteration: 4, Kind: agent.RoundCommitFix, FixNumber: 1},
			file:  "fix-1.txt",
		},
		{
			input: agent.RoundCommitInput{FeatureID: f.ID, PhaseNumber: 1, TotalPhases: 2, PhaseType: "tracer-bullet", Iteration: 5, Kind: agent.RoundCommitFix, FixNumber: 2},
			file:  "fix-2.txt",
		},
		{
			input: agent.RoundCommitInput{FeatureID: f.ID, Iteration: 6, Kind: agent.RoundCommitFinalReviewFix, FixNumber: 1},
			file:  "fr-fix-1.txt",
		},
	}
	for _, r := range rounds {
		r.input.Repos = map[string]string{"solo": repo}
		dirtyRepo(t, repo, r.file, "change\n")
		if err := hook(r.input); err != nil {
			t.Fatalf("round commit (%s): %v", r.file, err)
		}
	}

	bodies := repoCommitBodies(t, repo)
	// One body is the repo's initial commit.
	want := []string{
		"Phase 1/2 (tracer-bullet): Tracer\n\nFeature: round-commit-messages\n\nGenerated-by: Agentic (https://github.com/doordash-oss/agentic-orchestrator)",
		"Phase 1/2 (tracer-bullet): Tracer - implementation round 3\n\nFeature: round-commit-messages\n\nGenerated-by: Agentic (https://github.com/doordash-oss/agentic-orchestrator)",
		"Phase 1/2 (tracer-bullet): Tracer - fix round 1 (address review feedback)\n\nFeature: round-commit-messages\n\nGenerated-by: Agentic (https://github.com/doordash-oss/agentic-orchestrator)",
		"Phase 1/2 (tracer-bullet): Tracer - fix round 2 (address review feedback)\n\nFeature: round-commit-messages\n\nGenerated-by: Agentic (https://github.com/doordash-oss/agentic-orchestrator)",
		"Final review fix 1 (address review feedback)\n\nFeature: round-commit-messages\n\nGenerated-by: Agentic (https://github.com/doordash-oss/agentic-orchestrator)",
	}
	if len(bodies) != len(want)+1 {
		t.Fatalf("commit count = %d, want %d (rounds + initial); bodies:\n%s", len(bodies), len(want)+1, strings.Join(bodies, "\n---\n"))
	}
	// bodies[0] is the initial commit; round commits follow in order.
	for i, w := range want {
		if got := bodies[i+1]; got != w {
			t.Errorf("round commit %d message:\n got: %q\nwant: %q", i+1, got, w)
		}
	}
}

// Non-roadmap features have no phase prefix; every round is labeled.
func TestOrchestrator_RoundCommitHook_NonRoadmapMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	repo := testutil.InitGitRepo(t)
	f := &feature.Feature{
		ID:     "feat-nonroadmap-rounds",
		Slug:   "nonroadmap-rounds",
		Status: feature.StatusImplementing,
		Repos:  []feature.FeatureRepo{{Name: "solo", Path: repo, WorktreePath: repo}},
	}
	hook, _ := newRoundCommitHarness(t, f)

	dirtyRepo(t, repo, "round-1.txt", "change\n")
	if err := hook(agent.RoundCommitInput{FeatureID: f.ID, Iteration: 1, Kind: agent.RoundCommitImplement, FirstImplementCommit: true, Repos: map[string]string{"solo": repo}}); err != nil {
		t.Fatalf("implement round commit: %v", err)
	}
	dirtyRepo(t, repo, "fix-1.txt", "change\n")
	if err := hook(agent.RoundCommitInput{FeatureID: f.ID, Iteration: 2, Kind: agent.RoundCommitFix, FixNumber: 1, Repos: map[string]string{"solo": repo}}); err != nil {
		t.Fatalf("fix round commit: %v", err)
	}

	bodies := repoCommitBodies(t, repo)
	want := []string{
		"Implementation round 1\n\nFeature: nonroadmap-rounds\n\nGenerated-by: Agentic (https://github.com/doordash-oss/agentic-orchestrator)",
		"Fix round 1 (address review feedback)\n\nFeature: nonroadmap-rounds\n\nGenerated-by: Agentic (https://github.com/doordash-oss/agentic-orchestrator)",
	}
	if len(bodies) != len(want)+1 {
		t.Fatalf("commit count = %d, want %d; bodies:\n%s", len(bodies), len(want)+1, strings.Join(bodies, "\n---\n"))
	}
	for i, w := range want {
		if got := bodies[i+1]; got != w {
			t.Errorf("round commit %d message:\n got: %q\nwant: %q", i+1, got, w)
		}
	}
}

// Only repos the round actually dirtied are committed; clean repos are
// untouched no-ops, and each commit emits a RepoStatusChanged event.
func TestOrchestrator_RoundCommitHook_CommitsOnlyDirtyRepos(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	dirtyRepoPath := testutil.InitGitRepo(t)
	cleanRepoPath := testutil.InitGitRepo(t)
	cleanHeadBefore, err := git.CurrentHeadSHA(cleanRepoPath)
	if err != nil {
		t.Fatalf("clean repo head: %v", err)
	}

	f := roadmapFeature(t, dirtyRepoPath)
	f.Repos = append(f.Repos, feature.FeatureRepo{Name: "clean", Path: cleanRepoPath, WorktreePath: cleanRepoPath})
	hook, o := newRoundCommitHarness(t, f)

	dirtyRepo(t, dirtyRepoPath, "round-1.txt", "change\n")
	input := agent.RoundCommitInput{
		FeatureID:            f.ID,
		PhaseNumber:          1,
		TotalPhases:          2,
		PhaseType:            "tracer-bullet",
		Iteration:            1,
		Kind:                 agent.RoundCommitImplement,
		FirstImplementCommit: true,
		Repos: map[string]string{
			"solo":  dirtyRepoPath,
			"clean": cleanRepoPath,
		},
	}
	if err := hook(input); err != nil {
		t.Fatalf("round commit: %v", err)
	}

	if git.HasUncommittedChanges(dirtyRepoPath) {
		t.Error("dirty repo still has uncommitted changes after the round commit")
	}
	cleanHeadAfter, _ := git.CurrentHeadSHA(cleanRepoPath)
	if cleanHeadAfter != cleanHeadBefore {
		t.Errorf("clean repo HEAD moved: %q -> %q", cleanHeadBefore, cleanHeadAfter)
	}

	events := drainEvents(o)
	found := false
	for _, ev := range events {
		if ev.Type == ports.RepoStatusChanged && ev.RepoName == "solo" && ev.Message == "committed implementation round 1" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected RepoStatusChanged(implementation round 1) for solo; got %+v", events)
	}
}

// A feature that cannot be loaded fails the round loudly instead of
// committing with a degraded message.
func TestOrchestrator_RoundCommitHook_FailsWhenFeatureMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	repo := testutil.InitGitRepo(t)
	dirtyRepo(t, repo, "round-1.txt", "change\n")

	pr := &agent.PhaseRunner{}
	lc := lifecycleForFeature(&feature.Feature{ID: "feat-gone"})
	lc.GetFn = func(id string) (*feature.Feature, error) { return nil, fmt.Errorf("feature not found") }
	orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(), PhaseRunner: pr}, orchestrator.Hooks{})

	err := pr.RoundCommitHook(agent.RoundCommitInput{
		FeatureID: "feat-gone",
		Iteration: 1,
		Kind:      agent.RoundCommitImplement,
		Repos:     map[string]string{"solo": repo},
	})
	if err == nil {
		t.Fatal("round commit must fail when the feature cannot be loaded")
	}
	if !strings.Contains(err.Error(), "feature not found") {
		t.Errorf("error = %v, want load failure surfaced", err)
	}
}
