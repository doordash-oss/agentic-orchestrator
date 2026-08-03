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
