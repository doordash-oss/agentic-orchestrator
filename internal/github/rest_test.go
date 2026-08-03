package github_test // external test package so it can use testutil

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/github"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestListPRReviewCommentsFollowsLinkPagination(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.Mux.HandleFunc("/repos/acme/widgets/pulls/7/comments", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"id":2,"body":"second"}]`)
			return
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q; want 100", r.URL.Query().Get("per_page"))
		}
		// Absolute next link, exactly as api.github.com emits it; the
		// override transport rewrites the host to this fake server.
		w.Header().Set("Link", `<https://api.github.com/repos/acme/widgets/pulls/7/comments?per_page=100&page=2>; rel="next"`)
		fmt.Fprint(w, `[{"id":1,"body":"first"}]`)
	})

	client, err := github.ForHost("github.com")
	if err != nil {
		t.Fatalf("ForHost() error = %v", err)
	}
	comments, err := client.ListPRReviewComments("acme", "widgets", 7)
	if err != nil {
		t.Fatalf("ListPRReviewComments() error = %v", err)
	}
	if len(comments) != 2 || comments[0].ID != 1 || comments[1].ID != 2 {
		t.Fatalf("comments = %+v; want ids 1,2 across two pages", comments)
	}
}

func TestListIssueCommentsAndReviews(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/acme/widgets/issues/7/comments", 200,
		`[{"id":22,"body":"conversation","user":{"login":"bob"},"created_at":"2026-07-07T11:00:00Z"}]`)
	fake.HandleJSON("/repos/acme/widgets/pulls/7/reviews", 200,
		`[{"id":33,"body":"review summary","user":{"login":"carol"},"submitted_at":"2026-07-07T12:00:00Z"}]`)

	client, _ := github.ForHost("github.com")
	issue, err := client.ListIssueComments("acme", "widgets", 7)
	if err != nil || len(issue) != 1 || issue[0].ID != 22 || issue[0].User.Login != "bob" {
		t.Fatalf("ListIssueComments() = %+v, %v; want one comment id 22 by bob", issue, err)
	}
	reviews, err := client.ListPRReviews("acme", "widgets", 7)
	if err != nil || len(reviews) != 1 || reviews[0].ID != 33 || reviews[0].SubmittedAt != "2026-07-07T12:00:00Z" {
		t.Fatalf("ListPRReviews() = %+v, %v; want one review id 33", reviews, err)
	}
}

func TestGetPRMapsBodyBaseAndURL(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/acme/widgets/pulls/7", 200,
		`{"body":"pr body","base":{"ref":"main"},"html_url":"https://github.com/acme/widgets/pull/7"}`)

	client, _ := github.ForHost("github.com")
	info, err := client.GetPR("acme", "widgets", 7)
	if err != nil {
		t.Fatalf("GetPR() error = %v", err)
	}
	if info.Body != "pr body" || info.BaseRef != "main" || info.URL != "https://github.com/acme/widgets/pull/7" {
		t.Fatalf("GetPR() = %+v; want body/base/url mapped", info)
	}
}
