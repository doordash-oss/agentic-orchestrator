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
	"testing"
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

func TestParsePaginatedComments(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantCount int
		wantIDs   []int
		wantErr   bool
	}{
		{
			name:      "single page",
			input:     `[{"id":1,"path":"a.go"},{"id":2,"path":"b.go"}]`,
			wantCount: 2,
			wantIDs:   []int{1, 2},
		},
		{
			name:      "multi page",
			input:     `[{"id":1,"path":"a.go"},{"id":2,"path":"b.go"}][{"id":3,"path":"c.go"}]`,
			wantCount: 3,
			wantIDs:   []int{1, 2, 3},
		},
		{
			name:      "three pages",
			input:     `[{"id":1}][{"id":2}][{"id":3}]`,
			wantCount: 3,
			wantIDs:   []int{1, 2, 3},
		},
		{
			name:      "empty array",
			input:     `[]`,
			wantCount: 0,
		},
		{
			name:      "empty input",
			input:     ``,
			wantCount: 0,
		},
		{
			name:    "invalid json",
			input:   `[{"id":1}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comments, err := parsePaginatedComments([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Errorf("parsePaginatedComments() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if len(comments) != tt.wantCount {
				t.Errorf("got %d comments, want %d", len(comments), tt.wantCount)
				return
			}
			for i, wantID := range tt.wantIDs {
				if comments[i].ID != wantID {
					t.Errorf("comment[%d].ID = %d, want %d", i, comments[i].ID, wantID)
				}
			}
		})
	}
}

func TestParsePaginatedCommentsWithType(t *testing.T) {
	input := `[{"id":1,"path":"a.go","type":"review"},{"id":2,"body":"conversation","type":"issue"}]`
	comments, err := parsePaginatedComments([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("got %d comments, want 2", len(comments))
	}
	if comments[0].Type != "review" {
		t.Errorf("comment[0].Type = %q, want %q", comments[0].Type, "review")
	}
	if comments[1].Type != "issue" {
		t.Errorf("comment[1].Type = %q, want %q", comments[1].Type, "issue")
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

func TestParseReviewThreadMap(t *testing.T) {
	t.Run("unresolved threads", func(t *testing.T) {
		input := `{
  "data": {
    "repository": {
      "pullRequest": {
        "reviewThreads": {
          "nodes": [
            {
              "id": "PRRT_abc",
              "isResolved": false,
              "comments": {"nodes": [{"databaseId": 100}]}
            },
            {
              "id": "PRRT_def",
              "isResolved": true,
              "comments": {"nodes": [{"databaseId": 200}]}
            },
            {
              "id": "PRRT_ghi",
              "isResolved": false,
              "comments": {"nodes": [{"databaseId": 300}]}
            }
          ]
        }
      }
    }
  }
}`
		m, err := parseReviewThreadMap([]byte(input))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(m) != 2 {
			t.Fatalf("expected 2 unresolved threads, got %d", len(m))
		}
		if m[100] != "PRRT_abc" {
			t.Errorf("comment 100 -> %q, want PRRT_abc", m[100])
		}
		if _, ok := m[200]; ok {
			t.Error("resolved comment 200 should be excluded")
		}
		if m[300] != "PRRT_ghi" {
			t.Errorf("comment 300 -> %q, want PRRT_ghi", m[300])
		}
	})

	t.Run("empty threads", func(t *testing.T) {
		input := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}`
		m, err := parseReviewThreadMap([]byte(input))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(m) != 0 {
			t.Errorf("expected empty map, got %v", m)
		}
	})
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
