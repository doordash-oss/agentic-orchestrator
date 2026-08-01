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
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// repoName and repoAPath are the fixture repo name/path shared by
// orchestrator_test package tests.
const (
	repoName  = "repo-a"
	repoAPath = "/tmp/repo-a"
)

// mainBranch is the fixture default/base branch name shared by
// orchestrator_test package tests.
const mainBranch = "main"

// apiRepoName is the fixture repo name shared by orchestrator_test package
// multi-repo tests.
const apiRepoName = "api"

// wtR1Path is the fixture worktree path for the "r1" repo shared by
// publish_test.go republish tests.
const wtR1Path = "/tmp/wt-r1"

// agenticRepoName is the fixture repo name shared by orchestrator_test
// package tests.
const agenticRepoName = "agentic"

// repoNameB and repoBPath are the fixture second-repo name/path shared by
// orchestrator_test package multi-repo tests.
const (
	repoNameB = "repo-b"
	repoBPath = "/tmp/repo-b"
	repoNameC = "repo-c"
)

// finalStatusInterrupted and reviewStatusPassed mirror the unexported
// orchestrator package constants of the same name (agent.OrchestratorResult/
// PlanLoopResult.FinalStatus and per-repo RepoStatuses values), duplicated
// here since this is package orchestrator_test and can't import them.
const (
	finalStatusInterrupted = "interrupted"
	reviewStatusPassed     = "review_passed"
	finalStatusFailed      = "failed"
)

// kbStatusCompleted is the fixture KBStatus/RepoCycle completion value shared
// by orchestrator_test package tests.
const kbStatusCompleted = "completed"

// apiRepoWorkPath and repoAWorktreePath are fixture paths shared by
// orchestrator_test package tests.
const (
	apiRepoWorkPath   = "/tmp/api"
	repoAWorktreePath = "/tmp/repo-a-worktree"
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
func TestCompleteRepoCycle_ReviewComments_RepliesToEveryPRFeedbackType(t *testing.T) {
	stateDir := t.TempDir()
	f := &feature.Feature{
		ID:        "feat-rc-cycle",
		Slug:      "rc-cycle",
		Status:    feature.StatusPublished,
		ActiveRun: 1,
		RunCount:  1,
		Repos: []feature.FeatureRepo{{
			Name:         repoName,
			Path:         repoAPath,
			WorktreePath: "/tmp/worktrees/repo-a",
			Branch:       "feature/rc-cycle",
		}},
		RepoStates: map[string]*feature.RepoState{
			repoName: {
				Touched: true, PRURL: "https://github.com/org/repo-a/pull/7",
			},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			repoName: {Type: feature.CycleReviewComments, Status: feature.RepoCycleReviewing, Count: 1},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	if err := agent.SaveReviewCommentsForRepo(stateDir, f, repoName, agent.ReviewCommentsData{
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
	writeReviewResolutionsJSONForRepo(t, stateDir, f.ID, repoName, []agent.ReviewResolution{
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

	if err := o.CompleteRepoCycle(f.ID, repoName); err != nil {
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

	addressed, err := agent.LoadAddressedIDsForRepo(stateDir, f, repoName)
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
