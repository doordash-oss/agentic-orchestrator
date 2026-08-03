package github_test // external test package so it can use testutil

import (
	"encoding/json"
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

func TestCreatePRPostsPayloadAndReturnsURL(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.Mux.HandleFunc("/repos/acme/widgets/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s; want POST", r.Method)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decoding payload: %v", err)
		}
		if payload["title"] != "T" || payload["head"] != "feature/x" || payload["base"] != "develop" || payload["draft"] != true {
			t.Errorf("payload = %v; want title/head/base/draft", payload)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"html_url":"https://github.com/acme/widgets/pull/9"}`)
	})

	client, _ := github.ForHost("github.com")
	url, err := client.CreatePR(github.CreatePRParams{
		Owner: "acme", Repo: "widgets", Head: "feature/x", Base: "develop",
		Title: "T", Body: "B", Draft: true,
	})
	if err != nil || url != "https://github.com/acme/widgets/pull/9" {
		t.Fatalf("CreatePR() = %q, %v; want created PR URL", url, err)
	}
}

func TestCreatePRResolvesDefaultBranchWhenBaseEmpty(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/acme/widgets", 200, `{"default_branch":"trunk"}`)
	fake.Mux.HandleFunc("/repos/acme/widgets/pulls", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["base"] != "trunk" {
			t.Errorf("base = %v; want trunk (repo default)", payload["base"])
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"html_url":"https://github.com/acme/widgets/pull/10"}`)
	})

	client, _ := github.ForHost("github.com")
	if _, err := client.CreatePR(github.CreatePRParams{Owner: "acme", Repo: "widgets", Head: "feature/x", Title: "T"}); err != nil {
		t.Fatalf("CreatePR() error = %v", err)
	}
}

func TestCreatePRReturnsExistingURLOn422AlreadyExists(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.Mux.HandleFunc("/repos/acme/widgets/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"Validation Failed","errors":[{"message":"A pull request already exists for acme:feature/x."}]}`)
			return
		}
		if r.URL.Query().Get("head") != "acme:feature/x" || r.URL.Query().Get("state") != "open" {
			t.Errorf("lookup query = %s; want head=acme:feature/x state=open", r.URL.RawQuery)
		}
		fmt.Fprint(w, `[{"html_url":"https://github.com/acme/widgets/pull/5"}]`)
	})

	client, _ := github.ForHost("github.com")
	url, err := client.CreatePR(github.CreatePRParams{Owner: "acme", Repo: "widgets", Head: "feature/x", Base: "main", Title: "T"})
	if err != nil || url != "https://github.com/acme/widgets/pull/5" {
		t.Fatalf("CreatePR() = %q, %v; want existing PR URL on 422", url, err)
	}
}

func TestPRWriteEndpoints(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/acme/widgets/pulls/7", 200, `{}`)
	fake.HandleJSON("/repos/acme/widgets/pulls/7/comments/11/replies", 201, `{}`)
	fake.HandleJSON("/repos/acme/widgets/issues/7/comments", 201, `{}`)

	client, _ := github.ForHost("github.com")
	if err := client.UpdatePRBody("acme", "widgets", 7, "new body"); err != nil {
		t.Fatalf("UpdatePRBody() error = %v", err)
	}
	if err := client.ClosePR("acme", "widgets", 7); err != nil {
		t.Fatalf("ClosePR() error = %v", err)
	}
	if err := client.ReplyToReviewComment("acme", "widgets", 7, 11, "reply"); err != nil {
		t.Fatalf("ReplyToReviewComment() error = %v", err)
	}
	if err := client.CreateIssueComment("acme", "widgets", 7, "comment"); err != nil {
		t.Fatalf("CreateIssueComment() error = %v", err)
	}
	if fake.RequestCount(`PATCH /repos/acme/widgets/pulls/7 {"body":"new body"}`) != 1 {
		t.Fatalf("requests = %v; want one body PATCH", fake.Requests())
	}
	if fake.RequestCount(`{"state":"closed"}`) != 1 {
		t.Fatalf("requests = %v; want one close PATCH", fake.Requests())
	}
}
