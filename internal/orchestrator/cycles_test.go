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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// writeReviewCommentsJSON writes a review-comments fixture to
// <stateDir>/<featureID>/runs/run-001/review-comments/comments.json so
// agent.LoadReviewComments can find it.
func writeReviewCommentsJSON(t *testing.T, stateDir, featureID string, data agent.ReviewCommentsData) {
	t.Helper()
	dir := filepath.Join(stateDir, featureID, "runs", "run-001", "review-comments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir review-comments: %v", err)
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comments.json"), b, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func writeReviewResolutionsJSONForRepo(t *testing.T, stateDir, featureID, repoName string, data []agent.ReviewResolution) {
	t.Helper()
	dir := filepath.Join(stateDir, featureID, "runs", "run-001", "review-comments", repoName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir review-comments repo dir: %v", err)
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("marshal resolutions fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "review-resolutions.json"), b, 0o644); err != nil {
		t.Fatalf("write resolutions fixture: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CompleteRepoCycle (review-comments) — happy path
// ---------------------------------------------------------------------------
func TestCompleteRepoCycle_ReviewComments_HappyPath(t *testing.T) {
	stateDir := t.TempDir()
	f := &feature.Feature{
		ID:        "feat-rc-cycle",
		Slug:      "rc-cycle",
		Status:    feature.StatusPublished,
		ActiveRun: 1,
		RunCount:  1,
		Repos: []feature.FeatureRepo{{
			Name:         "repo-a",
			Path:         "/tmp/repo-a",
			WorktreePath: "/tmp/worktrees/repo-a",
			Branch:       "feature/rc-cycle",
		}},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {
				Touched: true, PRURL: "https://github.com/org/repo-a/pull/7",
			},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"repo-a": {Type: feature.CycleReviewComments, Status: "reviewing", Count: 1},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	if err := agent.SaveReviewCommentsForRepo(stateDir, f, "repo-a", agent.ReviewCommentsData{
		Mode: "auto",
		Comments: []git.ReviewComment{
			{ID: 11, Type: git.CommentTypeReview, User: struct {
				Login string `json:"login"`
			}{Login: "alice"}},
			{ID: 22, Type: git.CommentTypeIssue, User: struct {
				Login string `json:"login"`
			}{Login: "bob"}},
			{ID: 33, Type: git.CommentTypeReviewBody, User: struct {
				Login string `json:"login"`
			}{Login: "carol"}},
		},
	}); err != nil {
		t.Fatalf("SaveReviewCommentsForRepo: %v", err)
	}
	writeReviewResolutionsJSONForRepo(t, stateDir, f.ID, "repo-a", []agent.ReviewResolution{
		{CommentID: 11, Disposition: "addressed", Description: "Fixed it"},
		{CommentID: 22, Disposition: "dismissed", Description: "Already handled"},
		{CommentID: 33, Disposition: "addressed", Description: "Added coverage"},
	})

	pub := mocks.NewMockPublisher()
	reb := mocks.NewMockRebaseOperator()
	reb.PullRebaseFn = func(string, string) git.PullRebaseResult {
		return git.PullRebaseResult{Outcome: git.PullRebaseSuccess}
	}
	rev := mocks.NewMockReviewCommentOperator()
	rev.LatestCommitSHAFn = func(string) (string, error) { return "abc1234", nil }
	rev.FetchReviewThreadMapFn = func(string, string) (map[int]string, error) {
		return map[int]string{11: "thread-id-11"}, nil
	}

	pr := &agent.PhaseRunner{StateDir: stateDir}
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Publisher:   pub,
		Rebaser:     reb,
		Reviewer:    rev,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	if err := o.CompleteRepoCycle(f.ID, "repo-a"); err != nil {
		t.Fatalf("CompleteRepoCycle: %v", err)
	}

	if got := countPublisherCalls(pub, "CommitAll"); got != 1 {
		t.Errorf("CommitAll calls = %d, want 1", got)
	}
	if got := countRebaseCalls(reb, "PullRebase"); got != 1 {
		t.Errorf("PullRebase calls = %d, want 1", got)
	}
	if got := countPublisherCalls(pub, "Push"); got != 1 {
		t.Errorf("Push calls = %d, want 1", got)
	}
	if got := countReviewerCalls(rev, "ReplyToPRComment"); got != 1 {
		t.Errorf("ReplyToPRComment calls = %d, want 1", got)
	}
	if got := countReviewerCalls(rev, "ReplyToIssueComment"); got != 2 {
		t.Errorf("ReplyToIssueComment calls = %d, want 2", got)
	}
	if got := countReviewerCalls(rev, "ResolveReviewThread"); got != 1 {
		t.Errorf("ResolveReviewThread calls = %d, want 1", got)
	}
	if got := countLifecycleCalls(lc, "CompleteRepoCycle"); got != 1 {
		t.Errorf("CompleteRepoCycle lifecycle calls = %d, want 1", got)
	}
	if got := countLifecycleCalls(lc, "FailRepoCycle"); got != 0 {
		t.Errorf("FailRepoCycle lifecycle calls = %d, want 0", got)
	}

	addressed, err := agent.LoadAddressedIDsForRepo(stateDir, f, "repo-a")
	if err != nil {
		t.Fatalf("LoadAddressedIDsForRepo: %v", err)
	}
	if len(addressed) != 3 || !addressed[11] || !addressed[22] || !addressed[33] {
		t.Fatalf("unexpected addressed ids: %v", addressed)
	}
}

// ---------------------------------------------------------------------------
// Helpers — mirror MockCall accessors to index/count typed adapter calls.
// ---------------------------------------------------------------------------

func countPublisherCalls(m *mocks.MockPublisher, method string) int {
	n := 0
	for _, c := range m.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

func countRebaseCalls(m *mocks.MockRebaseOperator, method string) int {
	n := 0
	for _, c := range m.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

func countReviewerCalls(m *mocks.MockReviewCommentOperator, method string) int {
	n := 0
	for _, c := range m.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// lifecycleCallIndex returns the first index of a method in the lifecycle
// call log, or -1 if absent.
func lifecycleCallIndex(lc *mocks.MockFeatureLifecycle, method string) int {
	for i, c := range lc.Calls {
		if c.Method == method {
			return i
		}
	}
	return -1
}

// TestCompleteRepoCycle_Tweak_EmitsTweakReviewApproved verifies that for a
// per-repo tweak cycle, CompleteRepoCycle emits exactly one
// ports.TweakReviewApproved event (with Message == repoName) and does NOT run
// the inline commit/pull-rebase/push chain. The TUI's existing
// OrchTweakReviewApprovedMsg handler routes through completeTweakFinishCmd to
// preserve the rebase-conflict UX.
func TestCompleteRepoCycle_Tweak_EmitsTweakReviewApproved(t *testing.T) {
	const featureID = "feat-tweak-emit"
	const repoName = "repo-a"

	f := &feature.Feature{
		ID:        featureID,
		Slug:      "tweak-emit",
		Status:    feature.StatusPublished,
		ActiveRun: 1,
		RunCount:  1,
		Repos: []feature.FeatureRepo{{
			Name:         repoName,
			Path:         "/tmp/repo-a",
			WorktreePath: "/tmp/worktrees/repo-a",
			Branch:       "feature/tweak-emit",
		}},
		RepoStates: map[string]*feature.RepoState{
			repoName: {
				Touched: true, PRURL: "https://github.com/org/repo-a/pull/7",
			},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			repoName: {Type: feature.CycleTweak, Status: "reviewing", Count: 1},
		},
	}

	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	pub := mocks.NewMockPublisher()
	reb := mocks.NewMockRebaseOperator()

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Publisher: pub,
		Rebaser:   reb,
	}, orchestrator.Hooks{})

	if err := o.CompleteRepoCycle(featureID, repoName); err != nil {
		t.Fatalf("CompleteRepoCycle: %v", err)
	}

	// Exactly one TweakReviewApproved event with the per-repo name.
	events := drainEvents(o)
	tweakEvents := 0
	for _, ev := range events {
		if ev.Type == ports.TweakReviewApproved {
			tweakEvents++
			if ev.FeatureID != featureID {
				t.Errorf("TweakReviewApproved.FeatureID = %q, want %q", ev.FeatureID, featureID)
			}
			if ev.Message != repoName {
				t.Errorf("TweakReviewApproved.Message = %q, want %q", ev.Message, repoName)
			}
		}
	}
	if tweakEvents != 1 {
		t.Errorf("TweakReviewApproved event count = %d, want 1", tweakEvents)
	}

	// Inline chain must NOT have fired.
	if got := countPublisherCalls(pub, "CommitAll"); got != 0 {
		t.Errorf("Publisher.CommitAll calls = %d, want 0 (tweak case must not commit inline)", got)
	}
	if got := countRebaseCalls(reb, "PullRebase"); got != 0 {
		t.Errorf("Rebaser.PullRebase calls = %d, want 0 (tweak case must not pull-rebase inline)", got)
	}
	if got := countPublisherCalls(pub, "Push"); got != 0 {
		t.Errorf("Publisher.Push calls = %d, want 0 (tweak case must not push inline)", got)
	}
	// The cycle must not be marked completed or failed inline; the TUI's
	// completeTweakFinishCmd handles those transitions.
	if got := countLifecycleCalls(lc, "CompleteRepoCycle"); got != 0 {
		t.Errorf("Lifecycle.CompleteRepoCycle calls = %d, want 0 (tweak emits event instead)", got)
	}
	if got := countLifecycleCalls(lc, "FailRepoCycle"); got != 0 {
		t.Errorf("Lifecycle.FailRepoCycle calls = %d, want 0 (tweak emits event instead)", got)
	}
}

// TestStartRepoCycleImplement_Rebase_PassesConflictFilesAndPRURL asserts that
// when CycleRebase is dispatched with a conflicted-file list and the repo has
// an open PR URL, the resulting rebase plan uses the
// "rebase-already-in-progress" template (lists the conflicted files, names the
// rebase target, references the PR) and does NOT instruct the agent to
// `git rebase --abort`.
func TestStartRepoCycleImplement_Rebase_PassesConflictFilesAndPRURL(t *testing.T) {
	const featureID = "feat-rebase-conflict"
	const repoName = "repo-a"

	stateDir := t.TempDir()
	f := &feature.Feature{
		ID:        featureID,
		Slug:      "rebase-conflict",
		Status:    feature.StatusPublished,
		ActiveRun: 1,
		RunCount:  1,
		Repos: []feature.FeatureRepo{{
			Name:         repoName,
			Path:         "/tmp/repo-a",
			WorktreePath: "/tmp/worktrees/repo-a",
			Branch:       "feature/rebase-conflict",
		}},
		RepoStates: map[string]*feature.RepoState{
			repoName: {
				Touched: true, PRURL: "https://github.com/org/repo-a/pull/42",
			},
		},
		MaxIterations: 1,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	pr := &agent.PhaseRunner{
		StateDir: stateDir,
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			// Return a no-op command so the implementation goroutine winds
			// down without running a real session.
			return []string{"true"}, nil, &ports.SessionOpts{}, nil
		},
	}
	// Stub SessionManager that fails StartSession with a benign error. This
	// lets agent.RunImplementationLoop exit on its first iteration via the
	// "starting session" error path without the goroutine ever touching real
	// session/PTY plumbing.
	sm := mocks.NewMockSessionManager()
	sm.DefaultError = errors.New("stub session error")
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    sm,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})
	// StartRepoCycleImplement launches a background implementation loop that
	// writes under stateDir. Wait for it before t.TempDir() cleanup runs so
	// the goroutine cannot race with directory teardown.
	t.Cleanup(o.WaitForCycles)

	conflictFiles := []string{"internal/feature/manager.go", "internal/orchestrator/cycles.go"}
	if _, err := o.StartRepoCycleImplement(featureID, repoName, feature.CycleRebase, "master", conflictFiles...); err != nil {
		t.Fatalf("StartRepoCycleImplement: %v", err)
	}

	// The rebase plan path flattens under runs/run-001/rebase-1/ with no
	// per-repo subdir. The hint repo's conflict files and PRURL are still
	// folded into the per-repo section of the unified rebase plan.
	planPath := filepath.Join(stateDir, featureID, "runs", "run-001", "rebase-1", "rebase-plan.md")
	body, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read rebase plan: %v", err)
	}
	plan := string(body)

	// "Rebase already in progress" template — the agent must continue, not
	// restart from scratch.
	for _, want := range []string{
		"already in progress",
		"git rebase --continue",
		"## Conflicted Files",
		"`internal/feature/manager.go`",
		"`internal/orchestrator/cycles.go`",
		"https://github.com/org/repo-a/pull/42",
		"Do NOT run `git rebase --abort`",
		"origin/master",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("rebase plan missing %q\n--- plan ---\n%s\n--- /plan ---", want, plan)
		}
	}

	// "Start from scratch" template must NOT appear — that template tells the
	// agent to `git rebase --abort`, which would discard the in-progress
	// merge resolution work.
	if strings.Contains(plan, "git rebase --abort 2>/dev/null || true") {
		t.Errorf("rebase plan must NOT contain the start-from-scratch template; got:\n%s", plan)
	}
}

func TestStartRepoCycleImplement_RebaseDuplicateNoOps(t *testing.T) {
	const featureID = "feat-rebase-duplicate"
	const repoName = "repo-a"

	f := &feature.Feature{
		ID:     featureID,
		Status: feature.StatusCodeReady,
		Repos: []feature.FeatureRepo{{
			Name:   repoName,
			Path:   "/tmp/repo-a",
			Branch: "feature/rebase-duplicate",
		}},
		RepoCycles: map[string]*feature.RepoCycleState{
			repoName: {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		},
	}
	lc := lifecycleForFeature(f)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     newFeatureStore(f),
	}, orchestrator.Hooks{})

	if _, err := o.StartRepoCycleImplement(featureID, repoName, feature.CycleRebase, "master"); err != nil {
		t.Fatalf("duplicate rebase dispatch should no-op, got error: %v", err)
	}
}

func TestStartRefactorCycle_AllowsCodeReadyFeature(t *testing.T) {
	t.Parallel()

	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: filepath.Join(runtimeDir, "repo-a")}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	f, err := manager.Create("code ready refactor", "desc", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("Create feature: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusCodeReady
		ff.CurrentPhase = feature.PhasePublish
		ff.ActiveRun = 1
		ff.RunCount = 1
		ff.Repos[0].WorktreePath = filepath.Join(runtimeDir, "worktrees", "repo-a")
		ff.Repos[0].Branch = "feature/code-ready-refactor"
		ff.RepoStates = map[string]*feature.RepoState{"repo-a": {Touched: true}}
		return nil
	}); err != nil {
		t.Fatalf("prepare feature: %v", err)
	}

	releaseBuildSession := make(chan struct{})
	pr := &agent.PhaseRunner{
		StateDir: store.BaseDir,
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			<-releaseBuildSession
			return nil, nil, nil, errors.New("stop refactor test")
		},
	}
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   manager,
		Store:       store,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		close(releaseBuildSession)
		o.WaitForCycles()
	})

	const prompt = "simplify service layout"
	if _, err := o.StartRefactorCycle(f.ID, "repo-a", prompt); err != nil {
		t.Fatalf("StartRefactorCycle() error = %v, want nil for CodeReady feature", err)
	}

	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if got.Status != feature.StatusCodeReady {
		t.Fatalf("Status = %s, want CodeReady", got.Status)
	}
	if got.RefactorPrompt != prompt {
		t.Fatalf("RefactorPrompt = %q, want %q", got.RefactorPrompt, prompt)
	}
	if got.RefactorCount() != 1 {
		t.Fatalf("RefactorCount = %d, want 1", got.RefactorCount())
	}
	rc := got.RepoCycles["repo-a"]
	if rc == nil || rc.Type != feature.CycleRefactor || rc.Status != feature.RepoCycleRunning {
		t.Fatalf("RepoCycles[repo-a] = %+v, want running refactor cycle", rc)
	}
	promptPath := filepath.Join(store.BaseDir, f.ID, "runs", "run-001", "refactor-1", "refactor-prompt.md")
	if _, err := os.Stat(promptPath); err != nil {
		t.Fatalf("refactor prompt artifact was not staged at %s: %v", promptPath, err)
	}
}

// TestCompleteRepoCycle_Rebase_BlocksOnUnfinishedRebase asserts that when the
// agent resolved conflict files but never ran `git rebase --continue`,
// CompleteRepoCycle refuses to force-push. The cycle is marked failed via
// FailRepoCycle and ErrRebaseIncomplete is returned, so the stale pre-rebase
// branch head is never pushed and the PR stays in its unmerged state.
func TestCompleteRepoCycle_Rebase_BlocksOnUnfinishedRebase(t *testing.T) {
	const featureID = "feat-rebase-incomplete"
	const repoName = "repo-a"

	f := &feature.Feature{
		ID:        featureID,
		Slug:      "rebase-incomplete",
		Status:    feature.StatusPublished,
		ActiveRun: 1,
		RunCount:  1,
		Repos: []feature.FeatureRepo{{
			Name:         repoName,
			Path:         "/tmp/repo-a",
			WorktreePath: "/tmp/worktrees/repo-a",
			Branch:       "feature/rebase-incomplete",
		}},
		RepoStates: map[string]*feature.RepoState{
			repoName: {
				Touched: true, PRURL: "https://github.com/org/repo-a/pull/7",
			},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			repoName: {Type: feature.CycleRebase, Status: "reviewing", Count: 1},
		},
	}

	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	pub := mocks.NewMockPublisher()
	reb := mocks.NewMockRebaseOperator()
	reb.RebaseInProgressFn = func(string) (bool, error) { return true, nil }

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Publisher: pub,
		Rebaser:   reb,
	}, orchestrator.Hooks{})

	err := o.CompleteRepoCycle(featureID, repoName)
	if err == nil {
		t.Fatalf("CompleteRepoCycle: expected ErrRebaseIncomplete, got nil")
	}
	if !errors.Is(err, orchestrator.ErrRebaseIncomplete) {
		t.Errorf("CompleteRepoCycle returned %v, want errors.Is(ErrRebaseIncomplete)", err)
	}

	// Force-push must NOT have fired — the stale pre-rebase head must stay
	// off origin until the agent finishes the rebase.
	if got := countRebaseCalls(reb, "ForcePush"); got != 0 {
		t.Errorf("Rebaser.ForcePush calls = %d, want 0 (unfinished rebase must block force-push)", got)
	}
	if got := countPublisherCalls(pub, "CommitAll"); got != 0 {
		t.Errorf("Publisher.CommitAll calls = %d, want 0 (unfinished rebase must block commit)", got)
	}
	if got := countLifecycleCalls(lc, "MarkCodeReady"); got != 0 {
		t.Errorf("Lifecycle.MarkCodeReady calls = %d, want 0 (unfinished rebase must NOT promote to CodeReady)", got)
	}
	if got := countLifecycleCalls(lc, "CompleteRepoCycle"); got != 0 {
		t.Errorf("Lifecycle.CompleteRepoCycle calls = %d, want 0 (unfinished rebase must NOT complete the cycle)", got)
	}
	if got := countLifecycleCalls(lc, "FailRepoCycle"); got != 1 {
		t.Errorf("Lifecycle.FailRepoCycle calls = %d, want 1 (unfinished rebase must mark cycle failed)", got)
	}
}

// TestCompleteRepoCycle_Rebase_HappyPath_KeepsFeaturePublished asserts that a
// successful per-repo rebase cycle (commit + force-push completes cleanly)
// keeps the feature in StatusPublished and clears the RepoCycles entry. The
// rebase finalize chain must NOT call MarkCodeReady, which would regress the
// feature out of StatusPublished and break follow-on published-only flows
// (review-comments, subsequent rebase/tweak cycles, post-publish UX).
//
// Per the unified per-repo cycle model, post-publish cycle state lives in
// RepoCycles[repoName]; the feature-level Status stays StatusPublished for
// the entire lifetime of the cycle and afterward.
func TestCompleteRepoCycle_Rebase_HappyPath_KeepsFeaturePublished(t *testing.T) {
	const featureID = "feat-rebase-happy"
	const repoName = "repo-a"

	f := &feature.Feature{
		ID:        featureID,
		Slug:      "rebase-happy",
		Status:    feature.StatusPublished,
		ActiveRun: 1,
		RunCount:  1,
		Repos: []feature.FeatureRepo{{
			Name:         repoName,
			Path:         "/tmp/repo-a",
			WorktreePath: "/tmp/worktrees/repo-a",
			Branch:       "feature/rebase-happy",
		}},
		RepoStates: map[string]*feature.RepoState{
			repoName: {
				Touched: true, PRURL: "https://github.com/org/repo-a/pull/7",
			},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			repoName: {Type: feature.CycleRebase, Status: "reviewing", Count: 1},
		},
	}

	lc := lifecycleForFeature(f)
	// Mimic real MarkCodeReady — if the rebase path were to call it, the
	// feature Status would transition to StatusCodeReady. The test asserts
	// MarkCodeReady is NOT called and the status sticks at StatusPublished.
	lc.MarkCodeReadyFn = func(id string) error {
		f.Status = feature.StatusCodeReady
		return nil
	}
	// Mimic the real CompleteRepoCycle side-effect so the test can assert
	// the per-repo cycle entry is cleared.
	lc.CompleteRepoCycleFn = func(id, name string) error {
		delete(f.RepoCycles, name)
		if len(f.RepoCycles) == 0 {
			f.RepoCycles = nil
		}
		return nil
	}
	fs := newFeatureStore(f)

	pub := mocks.NewMockPublisher()
	reb := mocks.NewMockRebaseOperator()
	// Rebase is finished — no in-progress merge, force-push succeeds.
	reb.RebaseInProgressFn = func(string) (bool, error) { return false, nil }

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Publisher: pub,
		Rebaser:   reb,
	}, orchestrator.Hooks{})

	if err := o.CompleteRepoCycle(featureID, repoName); err != nil {
		t.Fatalf("CompleteRepoCycle: %v", err)
	}

	// The feature must remain StatusPublished — post-publish cycle state
	// lives in RepoCycles, not on the feature-level Status.
	if f.Status != feature.StatusPublished {
		t.Errorf("Feature.Status after rebase = %q, want %q (rebase cycle must NOT regress feature out of Published)",
			f.Status, feature.StatusPublished)
	}

	// MarkCodeReady must NOT fire — it would transition the feature to
	// StatusCodeReady and break follow-on Published-only flows.
	if got := countLifecycleCalls(lc, "MarkCodeReady"); got != 0 {
		t.Errorf("Lifecycle.MarkCodeReady calls = %d, want 0 (rebase must keep feature Published)", got)
	}

	// The repo cycle entry must be cleared via CompleteRepoCycle.
	if got := countLifecycleCalls(lc, "CompleteRepoCycle"); got != 1 {
		t.Errorf("Lifecycle.CompleteRepoCycle calls = %d, want 1", got)
	}
	if _, ok := f.RepoCycles[repoName]; ok {
		t.Errorf("RepoCycles[%q] still present after successful rebase; want cleared", repoName)
	}

	// The rebase finalize chain must have run — commit any leftover
	// resolutions, then force-push.
	if got := countPublisherCalls(pub, "CommitAll"); got != 1 {
		t.Errorf("Publisher.CommitAll calls = %d, want 1", got)
	}
	if got := countRebaseCalls(reb, "ForcePush"); got != 1 {
		t.Errorf("Rebaser.ForcePush calls = %d, want 1", got)
	}
	if got := countLifecycleCalls(lc, "FailRepoCycle"); got != 0 {
		t.Errorf("Lifecycle.FailRepoCycle calls = %d, want 0 on happy path", got)
	}
}

// TestCompleteTweakFinish_PullRebaseConflict_SurfacesPublishConflictError
// covers the unified N=1 tweak-review path. After a Final-Review-approved
// tweak cycle (which now flows through CompleteTweakFinish for both single-
// and multi-repo features), a pull-rebase conflict on push must surface a
// structured *PublishConflictError so the TUI can route the user into the
// rebase-resolution UX (via RebaseRepoCycleResultMsg) instead of a silent
// dashboard refresh. The cycle must also be marked failed so the follow-up
// CycleRebase can replace it (StartRepoCycle blocks on a still-running entry),
// and the push must NOT have fired (we have unrebased work to land first).
func TestCompleteTweakFinish_PullRebaseConflict_SurfacesPublishConflictError(t *testing.T) {
	const featureID = "feat-tweak-conflict"
	const repoName = "repo-a"
	const branch = "feature/tweak-conflict"

	f := &feature.Feature{
		ID:        featureID,
		Slug:      "tweak-conflict",
		Status:    feature.StatusPublished,
		ActiveRun: 1,
		RunCount:  1,
		Repos: []feature.FeatureRepo{{
			Name:         repoName,
			Path:         "/tmp/repo-a",
			WorktreePath: "/tmp/worktrees/repo-a",
			Branch:       branch,
		}},
		RepoStates: map[string]*feature.RepoState{
			repoName: {
				Touched: true, PRURL: "https://github.com/org/repo-a/pull/9",
			},
		},
		// Single-repo feature — exercises the unified N=1 path that previously
		// surfaced PublishConflictError directly (master) but on the unified
		// branch was regressed to a generic error swallowed by the TUI.
		RepoCycles: map[string]*feature.RepoCycleState{
			repoName: {Type: feature.CycleTweak, Status: "reviewing", Count: 1},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(string) (bool, error) { return false, nil }
	reb := mocks.NewMockRebaseOperator()
	reb.PullRebaseFn = func(string, string) git.PullRebaseResult {
		return git.PullRebaseResult{Outcome: git.PullRebaseConflict}
	}
	// PRBaseBranch resolves the conflict's RebaseTarget — the recovery rebase
	// must rebase onto the PR base (master), NOT the feature branch. Returning
	// "master" here pins the regression: if the orchestrator reverts to using
	// the feature branch, conflict.RebaseTarget will not equal "master".
	reb.PRBaseBranchFn = func(_, _ string) (string, error) { return "master", nil }

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Publisher: pub,
		Rebaser:   reb,
	}, orchestrator.Hooks{})

	// hadChanges=true so CompleteTweakFinish reaches the pull-rebase + push
	// branch. The "had Final Review fix" path is the realistic one — Final
	// Review almost always commits at least the review-fix marker.
	err := o.CompleteTweakFinish(featureID, true)
	if err == nil {
		t.Fatalf("CompleteTweakFinish: expected *PublishConflictError, got nil")
	}
	var conflict *orchestrator.PublishConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("CompleteTweakFinish error = %v (%T), want errors.As(*PublishConflictError)", err, err)
	}
	if conflict.RepoName != repoName {
		t.Errorf("conflict.RepoName = %q, want %q", conflict.RepoName, repoName)
	}
	if conflict.Branch != branch {
		t.Errorf("conflict.Branch = %q, want %q", conflict.Branch, branch)
	}
	// The recovery rebase plan rebases the feature branch ONTO the PR base —
	// so RebaseTarget must be the PR base ("master"), not the feature branch.
	if conflict.RebaseTarget != "master" {
		t.Errorf("conflict.RebaseTarget = %q, want %q (PR base, not the feature branch)", conflict.RebaseTarget, "master")
	}
	if conflict.RebaseTarget == branch {
		t.Errorf("conflict.RebaseTarget = %q is the feature branch — recovery rebase would target origin/<feature> instead of origin/master", conflict.RebaseTarget)
	}

	// The cycle must be marked failed so a follow-up CycleRebase can take
	// over without StartRepoCycle erroring on an already-running entry.
	if got := countLifecycleCalls(lc, "FailRepoCycle"); got != 1 {
		t.Errorf("Lifecycle.FailRepoCycle calls = %d, want 1 (conflict must clear the tweak cycle)", got)
	}
	// CompleteRepoCycle must NOT have fired — the tweak did NOT complete; the
	// rebase resolution flow takes ownership from here.
	if got := countLifecycleCalls(lc, "CompleteRepoCycle"); got != 0 {
		t.Errorf("Lifecycle.CompleteRepoCycle calls = %d, want 0 (tweak conflict must NOT mark complete)", got)
	}
	// Push must NOT have fired — the conflict means we have unrebased work to
	// land first; pushing now would publish the pre-rebase state.
	if got := countPublisherCalls(pub, "Push"); got != 0 {
		t.Errorf("Publisher.Push calls = %d, want 0 (conflict must short-circuit the push)", got)
	}
}

func TestCompleteTweakCommitFinish_AutoPublishOff_CommitsWithoutPush(t *testing.T) {
	const featureID = "feat-tweak-manual"
	const repoName = "repo-a"
	const branch = "feature/tweak-manual"

	for _, tc := range []struct {
		name        string
		publishable bool
	}{
		{name: "publishable manual feature", publishable: true},
		{name: "local only manual feature", publishable: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publishable := tc.publishable
			f := &feature.Feature{
				ID:          featureID,
				Slug:        "tweak-manual",
				Status:      feature.StatusCodeReady,
				ActiveRun:   1,
				RunCount:    1,
				Checkpoints: feature.Checkpoints{ManualPublish: true},
				Repos: []feature.FeatureRepo{{
					Name:         repoName,
					Path:         "/tmp/repo-a",
					WorktreePath: "/tmp/worktrees/repo-a",
					Branch:       branch,
					Publishable:  &publishable,
				}},
				RepoCycles: map[string]*feature.RepoCycleState{
					repoName: {Type: feature.CycleTweak, Status: feature.RepoCycleRunning, Count: 1},
				},
				ActiveCycle: &feature.CycleState{
					Type:   feature.CycleTweak,
					Status: feature.RepoCycleRunning,
					Count:  1,
				},
			}
			lc := lifecycleForFeature(f)
			fs := newFeatureStore(f)

			dirtyChecks := 0
			pub := mocks.NewMockPublisher()
			pub.HasUncommittedChangesFn = func(string) (bool, error) {
				dirtyChecks++
				return dirtyChecks == 1, nil
			}
			reb := mocks.NewMockRebaseOperator()
			reb.PullRebaseFn = func(string, string) git.PullRebaseResult {
				return git.PullRebaseResult{Outcome: git.PullRebaseSuccess}
			}

			o := orchestrator.New(orchestrator.Deps{
				Lifecycle: lc,
				Store:     fs,
				Publisher: pub,
				Rebaser:   reb,
			}, orchestrator.Hooks{})

			hadChanges, err := o.CompleteTweakCommit(featureID)
			if err != nil {
				t.Fatalf("CompleteTweakCommit: %v", err)
			}
			if !hadChanges {
				t.Fatal("CompleteTweakCommit hadChanges = false, want true")
			}

			if err := o.CompleteTweakFinish(featureID, hadChanges); err != nil {
				t.Fatalf("CompleteTweakFinish: %v", err)
			}

			if got := countPublisherCalls(pub, "CommitAll"); got != 1 {
				t.Errorf("Publisher.CommitAll calls = %d, want 1 (post-tweak flow must still commit local changes)", got)
			}
			if got := countRebaseCalls(reb, "PullRebase"); got != 0 {
				t.Errorf("Rebaser.PullRebase calls = %d, want 0 when auto-publish is off", got)
			}
			if got := countPublisherCalls(pub, "Push"); got != 0 {
				t.Errorf("Publisher.Push calls = %d, want 0 when auto-publish is off", got)
			}
			if got := countLifecycleCalls(lc, "CompleteRepoCycle"); got != 1 {
				t.Errorf("Lifecycle.CompleteRepoCycle calls = %d, want 1", got)
			}
			if f.ActiveCycle != nil {
				t.Errorf("ActiveCycle = %+v, want cleared", f.ActiveCycle)
			}
		})
	}
}

// TestCompleteTweakFinish_CompleteRepoCycleError_PropagatesAndFails asserts
// that a post-push CompleteRepoCycle store-write failure surfaces an error
// (so the user does not see a successful tweak completion) and marks the
// cycle failed (so a follow-up CycleRebase / RemoveRepoCycle can take over
// without StartRepoCycle erroring on a still-running entry). Previously the
// error was swallowed (`_ = o.deps.Lifecycle.CompleteRepoCycle(...)`), which
// left RepoCycles[repoName] populated and silently blocked follow-on
// post-publish actions.
func TestCompleteTweakFinish_CompleteRepoCycleError_PropagatesAndFails(t *testing.T) {
	const featureID = "feat-tweak-complete-err"
	const repoName = "repo-a"
	const branch = "feature/tweak-complete-err"

	f := &feature.Feature{
		ID:        featureID,
		Slug:      "tweak-complete-err",
		Status:    feature.StatusPublished,
		ActiveRun: 1,
		RunCount:  1,
		Repos: []feature.FeatureRepo{{
			Name:         repoName,
			Path:         "/tmp/repo-a",
			WorktreePath: "/tmp/worktrees/repo-a",
			Branch:       branch,
		}},
		RepoStates: map[string]*feature.RepoState{
			repoName: {
				Touched: true, PRURL: "https://github.com/org/repo-a/pull/9",
			},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			repoName: {Type: feature.CycleTweak, Status: "reviewing", Count: 1},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteRepoCycleFn = func(string, string) error {
		return errors.New("disk full")
	}
	fs := newFeatureStore(f)

	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(string) (bool, error) { return false, nil }
	reb := mocks.NewMockRebaseOperator()
	reb.PullRebaseFn = func(string, string) git.PullRebaseResult {
		return git.PullRebaseResult{Outcome: git.PullRebaseSuccess}
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Publisher: pub,
		Rebaser:   reb,
	}, orchestrator.Hooks{})

	err := o.CompleteTweakFinish(featureID, true)
	if err == nil {
		t.Fatalf("CompleteTweakFinish: expected error from CompleteRepoCycle failure, got nil")
	}
	if !strings.Contains(err.Error(), "complete repo cycle") {
		t.Errorf("CompleteTweakFinish error = %v, want wrap including \"complete repo cycle\"", err)
	}

	// Push must have fired (we got past push before CompleteRepoCycle).
	if got := countPublisherCalls(pub, "Push"); got != 1 {
		t.Errorf("Publisher.Push calls = %d, want 1 (push happens before CompleteRepoCycle)", got)
	}
	// CompleteRepoCycle must have been attempted.
	if got := countLifecycleCalls(lc, "CompleteRepoCycle"); got != 1 {
		t.Errorf("Lifecycle.CompleteRepoCycle calls = %d, want 1", got)
	}
	// FailRepoCycle must have fired so the still-running entry is cleared
	// for a follow-up cycle.
	if got := countLifecycleCalls(lc, "FailRepoCycle"); got != 1 {
		t.Errorf("Lifecycle.FailRepoCycle calls = %d, want 1 (CompleteRepoCycle failure must mark cycle failed)", got)
	}
}

// TestCompleteTweakFinish_PullRebaseConflict_RebasePlanTargetsPRBase drives
// the tweak-conflict path end-to-end: a PullRebase conflict during
// CompleteTweakFinish must surface a *PublishConflictError whose RebaseTarget
// is the PR base (master), and feeding that target through the
// StartRepoCycleImplement → BuildRebasePlan chain (the same chain the TUI
// drives via RebaseRepoCycleResultMsg → handleRebaseRepoCycleResult) must
// produce a plan that rebases onto origin/master, NOT onto the feature
// branch. Regresses the bug where conflict.Branch (feature/<slug>) was
// forwarded as the rebase target, generating a plan that told the agent to
// `git rebase origin/feature/<slug>` and pointing the recovery flow at the
// wrong ref.
func TestCompleteTweakFinish_PullRebaseConflict_RebasePlanTargetsPRBase(t *testing.T) {
	const featureID = "feat-tweak-conflict-plan"
	const repoName = "repo-a"
	const branch = "feature/tweak-conflict-plan"
	const prURL = "https://github.com/org/repo-a/pull/12"

	stateDir := t.TempDir()
	f := &feature.Feature{
		ID:        featureID,
		Slug:      "tweak-conflict-plan",
		Status:    feature.StatusPublished,
		ActiveRun: 1,
		RunCount:  1,
		Repos: []feature.FeatureRepo{{
			Name:         repoName,
			Path:         "/tmp/repo-a",
			WorktreePath: "/tmp/worktrees/repo-a",
			Branch:       branch,
		}},
		RepoStates: map[string]*feature.RepoState{
			repoName: {
				Touched: true, PRURL: prURL,
			},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			repoName: {Type: feature.CycleTweak, Status: "reviewing", Count: 1},
		},
		MaxIterations: 1,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(string) (bool, error) { return false, nil }
	reb := mocks.NewMockRebaseOperator()
	reb.PullRebaseFn = func(string, string) git.PullRebaseResult {
		return git.PullRebaseResult{Outcome: git.PullRebaseConflict}
	}
	// PRBaseBranch resolves the recovery rebase target. "master" pins the
	// regression: a revert to using conflict.Branch would push the feature
	// branch in here and the plan would say `git rebase origin/feature/<slug>`.
	reb.PRBaseBranchFn = func(_, gotPR string) (string, error) {
		if gotPR != prURL {
			t.Errorf("PRBaseBranch called with PR URL %q, want %q", gotPR, prURL)
		}
		return "master", nil
	}

	pr := &agent.PhaseRunner{
		StateDir: stateDir,
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			return []string{"true"}, nil, &ports.SessionOpts{}, nil
		},
	}
	sm := mocks.NewMockSessionManager()
	sm.DefaultError = errors.New("stub session error")

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    sm,
		Publisher:   pub,
		Rebaser:     reb,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})
	t.Cleanup(o.WaitForCycles)

	// Step 1: drive the tweak-finish path until it surfaces the conflict.
	err := o.CompleteTweakFinish(featureID, true)
	if err == nil {
		t.Fatalf("CompleteTweakFinish: expected *PublishConflictError, got nil")
	}
	var conflict *orchestrator.PublishConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("CompleteTweakFinish error = %v (%T), want errors.As(*PublishConflictError)", err, err)
	}
	if conflict.RebaseTarget != "master" {
		t.Fatalf("conflict.RebaseTarget = %q, want %q (PR base must drive the recovery rebase)", conflict.RebaseTarget, "master")
	}

	// Step 2: feed the conflict's RebaseTarget into the same StartRepoCycleImplement
	// path the TUI drives via handleRebaseRepoCycleResult. The plan body is the
	// only artifact the agent ever sees — that is where the bug surfaces.
	if _, err := o.StartRepoCycleImplement(featureID, repoName, feature.CycleRebase, conflict.RebaseTarget); err != nil {
		t.Fatalf("StartRepoCycleImplement: %v", err)
	}

	// The rebase plan path flattens under runs/run-001/rebase-1/. The exact
	// directory is irrelevant to the regression; the plan body content is.
	planPath := filepath.Join(stateDir, featureID, "runs", "run-001", "rebase-1", "rebase-plan.md")
	body, readErr := os.ReadFile(planPath)
	if readErr != nil {
		t.Fatalf("read rebase plan: %v", readErr)
	}
	plan := string(body)

	// The plan MUST name origin/master — that is where the recovery rebase
	// lands. A revert to forwarding conflict.Branch would replace this with
	// origin/feature/<slug>.
	if !strings.Contains(plan, "origin/master") {
		t.Errorf("rebase plan missing %q\n--- plan ---\n%s\n--- /plan ---", "origin/master", plan)
	}
	// And the plan MUST NOT name the feature branch as the rebase target.
	// `origin/<branch>` is the exact substring BuildRebasePlan would emit if
	// the bug returned.
	if strings.Contains(plan, "origin/"+branch) {
		t.Errorf("rebase plan rebases onto the feature branch %q (regression: tweak conflict forwarded conflict.Branch as RebaseTarget)\n--- plan ---\n%s\n--- /plan ---", "origin/"+branch, plan)
	}
}
