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

package feature_test

import (
	"errors"
	"strings"
	"testing"

	gitadapter "github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func launchTestParent() *feature.Feature {
	return &feature.Feature{
		ID:       "parent-launch",
		Name:     "Parent",
		Slug:     "parent-launch",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineLarge,
		Repos: []feature.FeatureRepo{
			{Name: "api", Path: "/src/api", WorktreePath: "/wt/api", Branch: "feature/parent-api", BaseBranch: "main"},
			{Name: "web", Path: "/src/web", WorktreePath: "/wt/web", Branch: "feature/parent-web", BaseBranch: "main"},
		},
		RepoStates: map[string]*feature.RepoState{
			"api": {PRURL: "https://github.example/acme/api/pull/17"},
			"web": {PRURL: "https://github.example/acme/web/pull/23"},
		},
	}
}

// reviewedComments are the snapshot the pending draft was reconciled from.
func reviewedComments() map[string][]feature.ReviewFeedbackComment {
	return map[string][]feature.ReviewFeedbackComment{
		"api": {
			{Repo: "api", ID: 11, Type: "review", Path: "a.go", Line: 3, Author: "alice", Body: "fix this", CreatedAt: "2026-08-02T08:00:00Z"},
			{Repo: "api", ID: 12, Type: "review", Author: "bob", Body: "will be edited", CreatedAt: "2026-08-02T09:00:00Z"},
		},
		"web": {
			{Repo: "web", ID: 21, Type: "issue", Author: "dana", Body: "will be deleted", CreatedAt: "2026-08-02T08:30:00Z"},
		},
	}
}

func launchComment(id int, typ, login, body string) gitadapter.ReviewComment {
	c := gitadapter.ReviewComment{ID: id, Type: typ, Body: body}
	c.User.Login = login
	return c
}

func installLaunchFetchStub(t *testing.T, byRepo map[string][]gitadapter.ReviewComment) {
	t.Helper()
	restore := feature.SwapFetchPRCommentsForTest(func(_ string, prURL string) ([]gitadapter.ReviewComment, error) {
		if strings.Contains(prURL, "/api/") {
			return byRepo["api"], nil
		}
		return byRepo["web"], nil
	})
	t.Cleanup(restore)
}

func TestLaunchFromDraftReconcilesCurrentContentAndClearsDraft(t *testing.T) {
	heads := map[string]string{"/wt/api": strings.Repeat("a", 40), "/wt/web": strings.Repeat("b", 40)}
	mgr := newChildTestManager(t, heads, cleanEverywhere())
	parent := launchTestParent()
	saveChildTestParent(t, mgr, parent)

	draft := feature.ReconcileReviewFeedbackDraft(parent, nil, reviewedComments())
	if err := feature.ApplyReviewFeedbackSelection(draft, map[feature.StableReviewFeedbackRef]bool{
		"api:review:11": false, // unselected reviewed comments contribute nothing
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Store.SaveReviewFeedbackDraft(parent.ID, draft, 0); err != nil {
		t.Fatal(err)
	}

	installLaunchFetchStub(t, map[string][]gitadapter.ReviewComment{
		"api": {
			launchComment(11, feature.ReviewFeedbackCommentTypeReview, "alice", "fix this"),
			launchComment(12, feature.ReviewFeedbackCommentTypeReview, "bob", "edited after review"),
			// Comment 30 is observed only at launch: deferred.
			launchComment(30, feature.ReviewFeedbackCommentTypeIssue, "erin", "arrived later"),
		},
	})

	gate := true
	result, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, &gate)
	if err != nil {
		t.Fatalf("LaunchReviewFeedbackChildFromDraft() error = %v", err)
	}
	if result.Changed != 1 || result.Omitted != 1 || result.Deferred != 1 {
		t.Fatalf("counts = changed:%d omitted:%d deferred:%d, want 1/1/1", result.Changed, result.Omitted, result.Deferred)
	}
	if result.Child == nil || result.Child.Parent == nil || result.Child.Parent.Kind != feature.ChildKindReviewFeedback {
		t.Fatalf("child = %+v, want review-feedback child", result.Child)
	}
	// Only selected, still-present reviewed comments land on the child, with
	// the current (edited) content.
	if len(result.Child.ReviewFeedback) != 1 {
		t.Fatalf("child comments = %+v, want exactly the edited selected comment", result.Child.ReviewFeedback)
	}
	if result.Child.ReviewFeedback[0].Body != "edited after review" || result.Child.ReviewFeedback[0].ID != 12 {
		t.Fatalf("child comment = %+v, want current content of comment 12", result.Child.ReviewFeedback[0])
	}
	// The draft clears only after durable child creation.
	if draft, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID); err != nil || draft != nil {
		t.Fatalf("draft after launch = %+v (err %v), want cleared", draft, err)
	}
}

func TestLaunchFromDraftZeroLaunchablePreservesDraft(t *testing.T) {
	mgr := newChildTestManager(t, map[string]string{"/wt/api": strings.Repeat("a", 40), "/wt/web": strings.Repeat("b", 40)}, cleanEverywhere())
	parent := launchTestParent()
	saveChildTestParent(t, mgr, parent)

	draft := feature.ReconcileReviewFeedbackDraft(parent, nil, reviewedComments())
	if err := feature.ApplyReviewFeedbackSelection(draft, map[feature.StableReviewFeedbackRef]bool{
		"api:review:11": false,
		"api:review:12": false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Store.SaveReviewFeedbackDraft(parent.ID, draft, 0); err != nil {
		t.Fatal(err)
	}
	// The only selected comment was deleted before launch.
	installLaunchFetchStub(t, map[string][]gitadapter.ReviewComment{"api": {}})

	var zero *feature.ReviewFeedbackZeroLaunchableSelectionError
	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, nil); !errors.As(err, &zero) {
		t.Fatalf("launch error = %v, want zero-launchable-selection", err)
	}
	if kept, err := mgr.Store.LoadReviewFeedbackDraft(parent.ID); err != nil || kept == nil {
		t.Fatalf("draft after failed launch = %+v, want preserved", kept)
	}
}

func TestLaunchFromDraftRevisionConflictPreservesDraft(t *testing.T) {
	mgr := newChildTestManager(t, map[string]string{"/wt/api": strings.Repeat("a", 40), "/wt/web": strings.Repeat("b", 40)}, cleanEverywhere())
	parent := launchTestParent()
	saveChildTestParent(t, mgr, parent)
	draft := feature.ReconcileReviewFeedbackDraft(parent, nil, reviewedComments())
	if err := mgr.Store.SaveReviewFeedbackDraft(parent.ID, draft, 0); err != nil {
		t.Fatal(err)
	}

	var conflict *feature.ReviewFeedbackRevisionConflictError
	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision+9, nil); !errors.As(err, &conflict) {
		t.Fatalf("launch error = %v, want revision conflict", err)
	}
	if kept, _ := mgr.Store.LoadReviewFeedbackDraft(parent.ID); kept == nil {
		t.Fatal("draft lost after revision conflict")
	}
}

func TestLaunchFromDraftMissingDraftFails(t *testing.T) {
	mgr := newChildTestManager(t, map[string]string{"/wt/api": strings.Repeat("a", 40), "/wt/web": strings.Repeat("b", 40)}, cleanEverywhere())
	parent := launchTestParent()
	saveChildTestParent(t, mgr, parent)

	if _, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, 1, nil); !errors.Is(err, feature.ErrReviewFeedbackDraftNotFound) {
		t.Fatalf("launch error = %v, want draft-not-found", err)
	}
}

// Regression for the oversized-request failure mode: complete current
// content flows into the child even when bodies and hunks are large.
func TestLaunchFromDraftCarriesLargeCurrentContent(t *testing.T) {
	mgr := newChildTestManager(t, map[string]string{"/wt/api": strings.Repeat("a", 40), "/wt/web": strings.Repeat("b", 40)}, cleanEverywhere())
	parent := launchTestParent()
	saveChildTestParent(t, mgr, parent)

	big := strings.Repeat("x", 72*1024)
	reviewed := map[string][]feature.ReviewFeedbackComment{
		"api": {{Repo: "api", ID: 11, Type: "review", Body: big, DiffHunk: big, CreatedAt: "2026-08-02T08:00:00Z"}},
	}
	draft := feature.ReconcileReviewFeedbackDraft(parent, nil, reviewed)
	if err := mgr.Store.SaveReviewFeedbackDraft(parent.ID, draft, 0); err != nil {
		t.Fatal(err)
	}
	installLaunchFetchStub(t, map[string][]gitadapter.ReviewComment{
		"api": {func() gitadapter.ReviewComment { c := launchComment(11, feature.ReviewFeedbackCommentTypeReview, "alice", big+"y"); c.DiffHunk = big; return c }()},
	})

	result, err := mgr.LaunchReviewFeedbackChildFromDraft(parent.ID, draft.Revision, nil)
	if err != nil {
		t.Fatalf("launch error = %v", err)
	}
	if result.Changed != 1 {
		t.Fatalf("changed = %d, want 1 for the grown body", result.Changed)
	}
	if len(result.Child.ReviewFeedback) != 1 || len(result.Child.ReviewFeedback[0].Body) != len(big)+1 {
		t.Fatalf("child body length = %d, want complete current content", len(result.Child.ReviewFeedback[0].Body))
	}
}
