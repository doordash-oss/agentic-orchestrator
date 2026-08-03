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
	"fmt"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	githubapi "github.com/doordash-oss/agentic-orchestrator/internal/github"
)

// PR comment type constants.
const (
	CommentTypeReview     = "review"
	CommentTypeIssue      = "issue"
	CommentTypeReviewBody = "review_body"
)

// ReviewComment is a GitHub PR comment (inline review or issue conversation).
type ReviewComment struct {
	ID   int    `json:"id"`
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
	DiffHunk  string `json:"diff_hunk"`
	InReplyTo int    `json:"in_reply_to_id"`
	Type      string `json:"type"`
	RepoName  string `json:"repo_name,omitempty"`
}

// ParsePRURL extracts owner, repo, and PR number from a GitHub PR URL.
// Expected format: https://github.com/owner/repo/pull/123
func ParsePRURL(prURL string) (owner, repo string, number int, err error) {
	parts := strings.Split(strings.TrimRight(prURL, "/"), "/")
	for i, p := range parts {
		if p == "pull" && i+1 < len(parts) && i >= 2 {
			number, err = strconv.Atoi(parts[i+1])
			if err != nil {
				return "", "", 0, fmt.Errorf("invalid PR number in URL %q: %w", prURL, err)
			}
			return parts[i-2], parts[i-1], number, nil
		}
	}
	return "", "", 0, fmt.Errorf("could not parse PR URL: %s", prURL)
}

// prURLHost extracts the host from a PR URL, defaulting to github.com.
func prURLHost(prURL string) string {
	u, err := url.Parse(prURL)
	if err != nil || u.Host == "" {
		return "github.com"
	}
	return u.Host
}

// FetchPRComments fetches inline review comments, general conversation
// comments, and submitted review bodies via the GitHub API. It excludes
// replies to inline comments so each returned item represents one
// addressable feedback thread or top-level PR comment. repoPath is
// retained for signature stability; the API needs only the PR URL.
func FetchPRComments(_ string, prURL string) ([]ReviewComment, error) {
	owner, repo, number, err := ParsePRURL(prURL)
	if err != nil {
		return nil, err
	}
	client, err := githubapi.ForHost(prURLHost(prURL))
	if err != nil {
		return nil, err
	}

	inline, err := client.ListPRReviewComments(owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("fetching PR review comments: %w", err)
	}
	var result []ReviewComment
	for _, c := range inline {
		if c.InReplyTo == 0 {
			result = append(result, apiComment(c, CommentTypeReview))
		}
	}

	issue, err := client.ListIssueComments(owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("fetching PR issue comments: %w", err)
	}
	for _, c := range issue {
		result = append(result, apiComment(c, CommentTypeIssue))
	}

	reviews, err := client.ListPRReviews(owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("fetching PR reviews: %w", err)
	}
	for _, review := range reviews {
		body := strings.TrimSpace(review.Body)
		if body == "" {
			continue
		}
		comment := ReviewComment{ID: review.ID, Body: body, CreatedAt: review.SubmittedAt, Type: CommentTypeReviewBody}
		comment.User.Login = review.User.Login
		result = append(result, comment)
	}

	SortReviewCommentsChronologically(result)
	return result, nil
}

// apiComment converts a REST comment into the orchestrator's type.
func apiComment(c githubapi.PRComment, commentType string) ReviewComment {
	comment := ReviewComment{
		ID: c.ID, Path: c.Path, Line: c.Line, Body: c.Body,
		CreatedAt: c.CreatedAt, DiffHunk: c.DiffHunk, InReplyTo: c.InReplyTo,
		Type: commentType,
	}
	comment.User.Login = c.User.Login
	return comment
}

// SortReviewCommentsChronologically orders comments by their GitHub creation
// time in ascending order. Comments without a parseable timestamp are placed
// after dated comments, with ID as a deterministic tie-breaker.
func SortReviewCommentsChronologically(comments []ReviewComment) {
	sort.SliceStable(comments, func(i, j int) bool {
		ti, iok := parseReviewCommentCreatedAt(comments[i].CreatedAt)
		tj, jok := parseReviewCommentCreatedAt(comments[j].CreatedAt)
		switch {
		case iok && jok:
			if !ti.Equal(tj) {
				return ti.Before(tj)
			}
		case iok != jok:
			return iok
		case comments[i].CreatedAt != comments[j].CreatedAt:
			return comments[i].CreatedAt < comments[j].CreatedAt
		}
		return comments[i].ID < comments[j].ID
	})
}

func parseReviewCommentCreatedAt(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ReplyToPRComment posts a reply to a specific review comment.
func ReplyToPRComment(repoPath, prURL string, commentID int, body string) error {
	body = InjectPRSignature(body)
	owner, repo, number, err := ParsePRURL(prURL)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/comments/%d/replies",
		owner, repo, number, commentID)
	cmd := exec.Command("gh", "api", endpoint, "-f", "body="+body)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("replying to comment %d: %s: %w",
			commentID, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ReplyToIssueComment posts a top-level conversation comment on a PR.
func ReplyToIssueComment(repoPath, prURL, body string) error {
	body = InjectPRSignature(body)
	owner, repo, number, err := ParsePRURL(prURL)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("repos/%s/%s/issues/%d/comments", owner, repo, number)
	cmd := exec.Command("gh", "api", endpoint, "-f", "body="+body)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("posting issue comment: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// FetchReviewThreadMap returns comment database ID → unresolved thread
// node ID for the PR. repoPath is retained for signature stability.
func FetchReviewThreadMap(_ string, prURL string) (map[int]string, error) {
	owner, repo, number, err := ParsePRURL(prURL)
	if err != nil {
		return nil, err
	}
	client, err := githubapi.ForHost(prURLHost(prURL))
	if err != nil {
		return nil, err
	}
	return client.ReviewThreadMap(owner, repo, number)
}

// ResolveReviewThread resolves a single review thread via GraphQL mutation.
func ResolveReviewThread(repoPath, threadNodeID string) error {
	query := fmt.Sprintf(`mutation {
  resolveReviewThread(input: {threadId: %q}) {
    thread { isResolved }
  }
}`, threadNodeID)

	cmd := exec.Command("gh", "api", "graphql", "-f", "query="+query)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("resolving review thread: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// LatestCommitSHA returns the short SHA of HEAD in the given directory.
func LatestCommitSHA(worktreePath string) (string, error) {
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--short", "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("getting HEAD SHA: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}
