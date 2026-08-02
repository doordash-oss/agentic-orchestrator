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
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// Fixture path/PR-URL for the "agentic" repo used across this file's tests.
const (
	agenticRepoPath = "/tmp/agentic"
	agenticPRURL    = "https://github.com/example/agentic/pull/1"
)

func TestAggregateReviewCommentTargetsIncludesEveryPRFeedbackType(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	f := &feature.Feature{
		ID:            "feat-review-comments-filter",
		Name:          "Review Comments Filter",
		Slug:          "review-comments-filter",
		Status:        feature.StatusPublished,
		ActiveRun:     1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: agenticRepoName, Path: agenticRepoPath, Branch: "feature/review-comments-filter"},
		},
		RepoStates: map[string]*feature.RepoState{
			agenticRepoName: {Touched: true, PRURL: agenticPRURL},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	if err := agent.SaveReviewCommentsForRepo(stateDir, f, agenticRepoName, agent.ReviewCommentsData{
		Mode: "auto",
		Comments: []git.ReviewComment{
			{ID: 1, Type: git.CommentTypeReview, CreatedAt: "2026-07-07T10:00:00Z"},
			{ID: 2, Type: git.CommentTypeIssue, CreatedAt: "2026-07-07T11:00:00Z"},
			{ID: 3, Type: git.CommentTypeReviewBody, CreatedAt: "2026-07-07T12:00:00Z"},
		},
	}); err != nil {
		t.Fatalf("save review comments: %v", err)
	}

	o := New(Deps{Store: store}, Hooks{})
	targets := o.aggregateReviewCommentTargets(f)
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	if len(targets[0].Comments) != 3 {
		t.Fatalf("aggregated comments = %+v, want all three PR feedback types", targets[0].Comments)
	}
}

func TestHandleFeatureReviewCommentsDone_NeedUserInputPausesCycle(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	mgr := feature.NewManager(store, cfg)
	gatePath := filepath.Join(t.TempDir(), "need-user-input.yaml")

	const featureID = "feat-review-comments-nui"
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Review Comments Need User Input",
		Slug:          "review-comments-need-user-input",
		Status:        feature.StatusPublished,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: agenticRepoName, Path: agenticRepoPath, Branch: "feature/review-comments-need-user-input"},
		},
		RepoStates: map[string]*feature.RepoState{
			agenticRepoName: {Touched: true, PRURL: agenticPRURL},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			agenticRepoName: {Type: feature.CycleReviewComments, Status: feature.RepoCycleRunning, Count: 1},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	o.handleFeatureReviewCommentsDone(featureID,
		[]agent.ReviewCommentsRepoTarget{{
			RepoName: agenticRepoName,
			PRURL:    agenticPRURL,
			Comments: []git.ReviewComment{{ID: 1}},
		}},
		&agent.ReviewCommentsLoopResult{
			FinalStatus:       "need_user_input",
			LastError:         "Reviewer request conflicts with product decision.",
			NeedUserInputPath: gatePath,
			Repos:             []string{agenticRepoName},
		},
		nil,
	)

	got, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	rc := got.RepoCycles[agenticRepoName]
	if rc == nil {
		t.Fatal("RepoCycles[agentic] missing")
	}
	if rc.Status != feature.RepoCycleNeedUserInput {
		t.Fatalf("RepoCycles[agentic].Status = %q, want %q", rc.Status, feature.RepoCycleNeedUserInput)
	}
	if rc.PendingNeedUserInputPath != gatePath {
		t.Errorf("PendingNeedUserInputPath = %q, want %q", rc.PendingNeedUserInputPath, gatePath)
	}
	if rc.LastError != "Reviewer request conflicts with product decision." {
		t.Errorf("LastError = %q", rc.LastError)
	}
	if st := got.RepoStates[agenticRepoName]; st == nil || st.LastError != "" {
		t.Errorf("RepoStates[agentic] = %+v, want no failure", st)
	}
}

func TestHandleFeatureReviewCommentsDone_NeedUserInputPersistenceErrorFailsCycle(t *testing.T) {
	store := mocks.NewMockFeatureStore()
	store.ModifyFn = func(string, func(*feature.Feature) error) error {
		return errors.New("store unavailable")
	}
	lifecycle := mocks.NewMockFeatureLifecycle()

	o := New(Deps{Lifecycle: lifecycle, Store: store}, Hooks{})
	o.handleFeatureReviewCommentsDone("feat-review-comments-nui",
		[]agent.ReviewCommentsRepoTarget{{
			RepoName: agenticRepoName,
			PRURL:    agenticPRURL,
			Comments: []git.ReviewComment{{ID: 1}},
		}},
		&agent.ReviewCommentsLoopResult{
			FinalStatus:       "need_user_input",
			LastError:         "Reviewer request conflicts with product decision.",
			NeedUserInputPath: filepath.Join(t.TempDir(), "need-user-input.yaml"),
			Repos:             []string{agenticRepoName},
		},
		nil,
	)

	var failCall *mocks.MockCall
	for i := range lifecycle.Calls {
		if lifecycle.Calls[i].Method == "FailRepoCycle" {
			failCall = &lifecycle.Calls[i]
			break
		}
	}
	if failCall == nil {
		t.Fatal("FailRepoCycle was not called")
	}
	if got := failCall.Args[1]; got != agenticRepoName {
		t.Fatalf("FailRepoCycle repo = %v, want agentic", got)
	}
	msg, _ := failCall.Args[2].(string)
	if !strings.Contains(msg, "review-comments: persist need-user-input gate") ||
		!strings.Contains(msg, "store unavailable") {
		t.Fatalf("FailRepoCycle message = %q, want persistence error context", msg)
	}
}
