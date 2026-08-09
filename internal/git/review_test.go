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

package git

import (
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestParsePRURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		owner   string
		repo    string
		number  int
		wantErr bool
	}{
		{"standard", "https://github.com/owner/repo/pull/123", "owner", "repo", 123, false},
		{"trailing slash", "https://github.com/owner/repo/pull/456/", "owner", "repo", 456, false},
		{"invalid number", "https://github.com/owner/repo/pull/abc", "", "", 0, true},
		{"no pull segment", "https://github.com/owner/repo", "", "", 0, true},
		{"empty", "", "", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, number, err := ParsePRURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePRURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
				return
			}
			if owner != tt.owner || repo != tt.repo || number != tt.number {
				t.Errorf("ParsePRURL(%q) = (%q, %q, %d), want (%q, %q, %d)",
					tt.url, owner, repo, number, tt.owner, tt.repo, tt.number)
			}
		})
	}
}

func TestFetchPRCommentsIncludesEveryPRFeedbackSurface(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/example/repo/pulls/7/comments", 200,
		`[{"id":11,"path":"main.go","line":12,"body":"inline","user":{"login":"alice"},"created_at":"2026-07-07T10:00:00Z"},`+
			`{"id":12,"body":"a reply","user":{"login":"dave"},"created_at":"2026-07-07T10:30:00Z","in_reply_to_id":11}]`)
	fake.HandleJSON("/repos/example/repo/issues/7/comments", 200,
		`[{"id":22,"body":"conversation","user":{"login":"bob"},"created_at":"2026-07-07T11:00:00Z"}]`)
	fake.HandleJSON("/repos/example/repo/pulls/7/reviews", 200,
		`[{"id":33,"body":"review summary","user":{"login":"carol"},"submitted_at":"2026-07-07T12:00:00Z"}]`)

	comments, err := FetchPRComments(t.TempDir(), "https://github.com/example/repo/pull/7")
	if err != nil {
		t.Fatalf("FetchPRComments() error = %v", err)
	}
	if len(comments) != 3 {
		t.Fatalf("FetchPRComments() returned %d comments, want 3 (reply to 11 excluded): %+v", len(comments), comments)
	}
	for i, want := range []struct {
		id          int
		commentType string
	}{
		{id: 11, commentType: CommentTypeReview},
		{id: 22, commentType: CommentTypeIssue},
		{id: 33, commentType: CommentTypeReviewBody},
	} {
		if comments[i].ID != want.id || comments[i].Type != want.commentType {
			t.Fatalf("comments[%d] = %+v, want id=%d type=%s", i, comments[i], want.id, want.commentType)
		}
	}
}

func TestFetchPRCommentsSurfacesAPIErrors(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/example/repo/pulls/7/comments", 500, `{"message":"synthetic API failure"}`)

	_, err := FetchPRComments(t.TempDir(), "https://github.com/example/repo/pull/7")
	if err == nil || !strings.Contains(err.Error(), "synthetic API failure") {
		t.Fatalf("FetchPRComments() error = %v, want it to contain %q", err, "synthetic API failure")
	}
}

func TestPRURLHost(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"github.com", "https://github.com/o/r/pull/1", "github.com"},
		{"enterprise host", "https://ghe.corp.example/o/r/pull/2", "ghe.corp.example"},
		{"garbage defaults to github.com", "not a url", "github.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prURLHost(tt.url); got != tt.want {
				t.Errorf("prURLHost(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestSortReviewCommentsChronologically(t *testing.T) {
	comments := []ReviewComment{
		{ID: 3, CreatedAt: "2026-07-07T12:00:00Z"},
		{ID: 1, CreatedAt: "2026-07-07T10:00:00Z"},
		{ID: 2, CreatedAt: "2026-07-07T11:00:00Z"},
	}

	SortReviewCommentsChronologically(comments)

	for i, wantID := range []int{1, 2, 3} {
		if comments[i].ID != wantID {
			t.Fatalf("comments[%d].ID = %d, want %d", i, comments[i].ID, wantID)
		}
	}
}

func TestCommentTypeConstants(t *testing.T) {
	if CommentTypeReview != "review" {
		t.Errorf("CommentTypeReview = %q, want %q", CommentTypeReview, "review")
	}
	if CommentTypeIssue != "issue" {
		t.Errorf("CommentTypeIssue = %q, want %q", CommentTypeIssue, "issue")
	}
	if CommentTypeReviewBody != "review_body" {
		t.Errorf("CommentTypeReviewBody = %q, want %q", CommentTypeReviewBody, "review_body")
	}
}
