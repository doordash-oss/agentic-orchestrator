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

package orchestrator

import (
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// hintPreflightRepo builds a real git repository wired to a bare origin with
// a three-layer stack on paper: one commit per layer branch off main, in
// ascending order. origin/main is the remote-tracking base the stack
// preflight resolves, and it never moves, so the local remote-tracking
// comparison reports up to date — exactly the shape the rebase hint must
// upgrade to behind.
func hintPreflightRepo(t *testing.T) (worktree, tip1, tip2, tip3 string) {
	t.Helper()
	dir := t.TempDir()
	bare := t.TempDir() + "/remote.git"
	runCompletionGit(t, "", "init", "--bare", "--initial-branch=main", bare)
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "initial"},
		{"remote", "add", "origin", bare},
		{"push", "-u", "origin", "main"},
		{"checkout", "-b", "feature/l1"},
		{"commit", "--allow-empty", "-m", "layer one"},
		{"checkout", "-b", "feature/l2"},
		{"commit", "--allow-empty", "-m", "layer two"},
		{"checkout", "-b", "feature/l3"},
		{"commit", "--allow-empty", "-m", "layer three"},
	} {
		runCompletionGit(t, dir, args...)
	}
	return dir,
		completionGitOutput(t, dir, "rev-parse", "feature/l1"),
		completionGitOutput(t, dir, "rev-parse", "feature/l2"),
		completionGitOutput(t, dir, "rev-parse", "feature/l3")
}

// hintPreflightStack shapes the three-layer stack with a recorded, delivered
// pull request on every layer.
func hintPreflightStack(tip1, tip2, tip3 string) []feature.StackLayer {
	entry := func(tip, prURL string) feature.StackRepoEntry {
		return feature.StackRepoEntry{
			TipSHA:        tip,
			LastPushedSHA: tip,
			PRURL:         prURL,
			PRState:       feature.StackPRStateOpen,
		}
	}
	return []feature.StackLayer{
		{
			Position: 1, Title: "Foundation", Branch: "feature/l1",
			Repos: map[string]feature.StackRepoEntry{
				"repo-a": entry(tip1, "https://github.example/repo-a/pull/1"),
			},
		},
		{
			Position: 2, Title: "Fix auth", Branch: "feature/l2",
			Repos: map[string]feature.StackRepoEntry{
				"repo-a": entry(tip2, "https://github.example/repo-a/pull/2"),
			},
		},
		{
			Position: 3, Title: "Top polish", Branch: "feature/l3",
			Repos: map[string]feature.StackRepoEntry{
				"repo-a": entry(tip3, "https://github.example/repo-a/pull/3"),
			},
		},
	}
}

// newHintPreflightOrchestrator wires the shared fakes: the real
// manager-backed lifecycle (so the preflight's monotonic stack writes
// persist) and the shared MockRemoteOps, whose PRState answers come from the
// test's script.
func newHintPreflightOrchestrator(t *testing.T, repoPath string, stack []feature.StackLayer, prState func(prURL string) (string, error)) (*Orchestrator, *feature.Manager) {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	manager := feature.NewManager(store, config.NewDefault())
	f := &feature.Feature{
		ID:            "feat-hint",
		Name:          "Hint",
		Slug:          "hint",
		Status:        feature.StatusPublished,
		SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun:     1,
		RunCount:      1,
		Stack:         stack,
		Repos: []feature.FeatureRepo{{
			Name:         "repo-a",
			Path:         repoPath,
			WorktreePath: repoPath,
			Branch:       "feature/l1",
			BaseBranch:   "main",
			Publishable:  boolPtr(true),
		}},
		RepoStates: map[string]*feature.RepoState{"repo-a": {Touched: true}},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	remote := mocks.NewMockRemoteOps()
	remote.PRStateFn = func(_, prURL string) (string, error) {
		return prState(prURL)
	}
	return New(Deps{Lifecycle: manager, Store: store, Remote: remote}, Hooks{}), manager
}

func hintPreflightRepoResult(t *testing.T, o *Orchestrator) CompletionRepoResult {
	t.Helper()
	result, err := o.CompletionPreflight("feat-hint")
	if err != nil {
		t.Fatalf("CompletionPreflight: %v", err)
	}
	if len(result.Repos) != 1 {
		t.Fatalf("repos len = %d; want 1", len(result.Repos))
	}
	return result.Repos[0]
}

// A repository whose layer 1 pull request reads merged live, with layers 2
// and 3 open, reports layer 1 merged, freshness behind with a rebase hint
// naming layer 1 and the rebase pass — even though the local remote-tracking
// comparison reports up to date — and the run's stack records layer 1 merged.
func TestCompletionPreflightMergedLayerBelowKeptWorkReportsBehindWithRebaseHint(t *testing.T) {
	t.Parallel()
	repoPath, tip1, tip2, tip3 := hintPreflightRepo(t)
	live := map[string]string{
		"https://github.example/repo-a/pull/1": git.PRStateMerged,
		"https://github.example/repo-a/pull/2": git.PRStateOpen,
		"https://github.example/repo-a/pull/3": git.PRStateOpen,
	}
	o, manager := newHintPreflightOrchestrator(t, repoPath, hintPreflightStack(tip1, tip2, tip3),
		func(prURL string) (string, error) { return live[prURL], nil })

	got := hintPreflightRepoResult(t, o)
	if got.Freshness != preflightFreshnessBehind {
		t.Errorf("freshness = %q; want %q (the merged layer upgrades up-to-date to behind)", got.Freshness, preflightFreshnessBehind)
	}
	if !strings.Contains(got.RebaseHint, "Layer 1") || !strings.Contains(got.RebaseHint, "Foundation") {
		t.Errorf("rebase hint = %q; want it to name layer 1 and its title", got.RebaseHint)
	}
	if !strings.Contains(got.RebaseHint, "rebase pass") {
		t.Errorf("rebase hint = %q; want it to point at the rebase pass", got.RebaseHint)
	}
	if len(got.PullRequests) != 3 {
		t.Fatalf("pull requests len = %d; want one entry per layer", len(got.PullRequests))
	}
	if got.PullRequests[0].State != string(feature.StackPRStateMerged) {
		t.Errorf("layer 1 state = %q; want merged (the live state)", got.PullRequests[0].State)
	}
	if got.PullRequests[1].State != string(feature.StackPRStateOpen) || got.PullRequests[2].State != string(feature.StackPRStateOpen) {
		t.Errorf("layers 2/3 states = %q/%q; want open/open", got.PullRequests[1].State, got.PullRequests[2].State)
	}
	// The preflight's one write: the run's stack now records layer 1 merged.
	persisted, err := manager.Get("feat-hint")
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	if entry := persisted.OrderedStackLayers()[0].Repos["repo-a"]; entry.PRState != feature.StackPRStateMerged {
		t.Errorf("persisted layer 1 PR state = %q; want merged", entry.PRState)
	}
	if entry := persisted.OrderedStackLayers()[1].Repos["repo-a"]; entry.PRState != feature.StackPRStateOpen {
		t.Errorf("persisted layer 2 PR state = %q; want open (never written)", entry.PRState)
	}
}

// A second preflight whose lookups are indeterminate keeps the recorded
// merged state and the hint: merged is a monotonic remote fact, and the hint
// is derived from the recorded entry.
func TestCompletionPreflightIndeterminateLookupKeepsMergedStateAndHint(t *testing.T) {
	t.Parallel()
	repoPath, tip1, tip2, tip3 := hintPreflightRepo(t)
	live := map[string]string{
		"https://github.example/repo-a/pull/1": git.PRStateMerged,
		"https://github.example/repo-a/pull/2": git.PRStateOpen,
		"https://github.example/repo-a/pull/3": git.PRStateOpen,
	}
	stack := hintPreflightStack(tip1, tip2, tip3)
	o, _ := newHintPreflightOrchestrator(t, repoPath, stack,
		func(prURL string) (string, error) { return live[prURL], nil })
	if first := hintPreflightRepoResult(t, o); first.RebaseHint == "" {
		t.Fatal("first preflight produced no hint; fixture is wrong")
	}

	// Every lookup now fails: the preflight must keep the recorded state.
	o.deps.Remote = mocks.NewMockRemoteOps()
	second := hintPreflightRepoResult(t, o)
	if second.PullRequests[0].State != string(feature.StackPRStateMerged) {
		t.Errorf("layer 1 state = %q; want merged kept from the recorded entry", second.PullRequests[0].State)
	}
	if second.Freshness != preflightFreshnessBehind {
		t.Errorf("freshness = %q; want %q", second.Freshness, preflightFreshnessBehind)
	}
	if !strings.Contains(second.RebaseHint, "Layer 1") || !strings.Contains(second.RebaseHint, "rebase pass") {
		t.Errorf("rebase hint = %q; want it kept from the recorded merged entry", second.RebaseHint)
	}
}

// A repository with no merged layer reports today's freshness and no hint.
func TestCompletionPreflightNoMergedLayerReportsTodayFreshnessAndNoHint(t *testing.T) {
	t.Parallel()
	repoPath, tip1, tip2, tip3 := hintPreflightRepo(t)
	o, _ := newHintPreflightOrchestrator(t, repoPath, hintPreflightStack(tip1, tip2, tip3),
		func(prURL string) (string, error) { return git.PRStateOpen, nil })

	got := hintPreflightRepoResult(t, o)
	if got.Freshness != preflightFreshnessUpToDate {
		t.Errorf("freshness = %q; want %q", got.Freshness, preflightFreshnessUpToDate)
	}
	if got.RebaseHint != "" {
		t.Errorf("rebase hint = %q; want none without a merged layer", got.RebaseHint)
	}
}

// A repository whose every layer pull request reads merged has no kept work
// above the merged layers, so it reports no hint.
func TestCompletionPreflightFullyMergedRepositoryReportsNoHint(t *testing.T) {
	t.Parallel()
	repoPath, tip1, tip2, tip3 := hintPreflightRepo(t)
	o, _ := newHintPreflightOrchestrator(t, repoPath, hintPreflightStack(tip1, tip2, tip3),
		func(prURL string) (string, error) { return git.PRStateMerged, nil })

	got := hintPreflightRepoResult(t, o)
	if got.RebaseHint != "" {
		t.Errorf("rebase hint = %q; want none for a fully merged repository", got.RebaseHint)
	}
	for i, entry := range got.PullRequests {
		if entry.State != string(feature.StackPRStateMerged) {
			t.Errorf("layer %d state = %q; want merged", i+1, entry.State)
		}
	}
}

// A closed-unmerged pull request found by the completion preflight is
// observed and persisted on the stack, never downgraded afterwards, and
// parks no blocker — that stays with Phase 15.
func TestCompletionPreflightClosedPullRequestIsPersistedWithoutBlocker(t *testing.T) {
	t.Parallel()
	repoPath, tip1, tip2, tip3 := hintPreflightRepo(t)
	live := map[string]string{
		"https://github.example/repo-a/pull/1": git.PRStateClosed,
		"https://github.example/repo-a/pull/2": git.PRStateOpen,
		"https://github.example/repo-a/pull/3": git.PRStateOpen,
	}
	o, manager := newHintPreflightOrchestrator(t, repoPath, hintPreflightStack(tip1, tip2, tip3),
		func(prURL string) (string, error) { return live[prURL], nil })

	got := hintPreflightRepoResult(t, o)
	if got.PullRequests[0].State != string(feature.StackPRStateClosed) {
		t.Errorf("layer 1 state = %q; want closed (the live state)", got.PullRequests[0].State)
	}
	if got.Blocker != "" {
		t.Errorf("blocker = %q; want none (blocker parking stays with Phase 15)", got.Blocker)
	}
	// A closed layer is not kept work, so no rebase hint is derived from it.
	if got.RebaseHint != "" {
		t.Errorf("rebase hint = %q; want none (closed is not a merged layer)", got.RebaseHint)
	}
	persisted, err := manager.Get("feat-hint")
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	if entry := persisted.OrderedStackLayers()[0].Repos["repo-a"]; entry.PRState != feature.StackPRStateClosed {
		t.Errorf("persisted layer 1 PR state = %q; want closed", entry.PRState)
	}
}
