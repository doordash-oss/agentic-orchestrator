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
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// Comment type constants. Canonical values live in ports.
const (
	CommentTypeReview     = ports.CommentTypeReview
	CommentTypeIssue      = ports.CommentTypeIssue
	CommentTypeReviewBody = ports.CommentTypeReviewBody
)

// ReviewComment aliases the canonical port type.
type ReviewComment = ports.ReviewComment

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

// FetchPRComments fetches inline PR review comments using the gh CLI. Returns
// only top-level review comments (excludes replies). Each comment's Type field
// is set to CommentTypeReview so completion can reply directly in the review
// thread and then resolve the conversation.
func FetchPRComments(repoPath, prURL string) ([]ReviewComment, error) {
	owner, repo, number, err := ParsePRURL(prURL)
	if err != nil {
		return nil, err
	}

	// Fetch inline review comments
	reviewEndpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/comments", owner, repo, number)
	reviewCmd := exec.Command("gh", "api", "--paginate", reviewEndpoint)
	reviewCmd.Dir = repoPath
	reviewOut, err := reviewCmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("fetching PR review comments: %s: %w", strings.TrimSpace(string(reviewOut)), err)
	}

	reviewComments, err := parsePaginatedComments(reviewOut)
	if err != nil {
		return nil, err
	}

	// Filter to top-level review comments and tag them
	var result []ReviewComment
	for _, c := range reviewComments {
		if c.InReplyTo == 0 {
			c.Type = CommentTypeReview
			result = append(result, c)
		}
	}

	sortReviewCommentsChronologically(result)
	return result, nil
}

// SortReviewCommentsChronologically orders comments by their GitHub creation
// time in ascending order. Comments without a parseable timestamp are placed
// after dated comments, with ID as a deterministic tie-breaker.
func SortReviewCommentsChronologically(comments []ReviewComment) {
	sortReviewCommentsChronologically(comments)
}

func sortReviewCommentsChronologically(comments []ReviewComment) {
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

// parsePaginatedComments parses output from gh api --paginate, which emits
// one JSON array per page, concatenated (e.g. [{...}][{...}]).
func parsePaginatedComments(data []byte) ([]ReviewComment, error) {
	var comments []ReviewComment
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var page []ReviewComment
		if err := dec.Decode(&page); err != nil {
			return nil, fmt.Errorf("parsing PR comments: %w", err)
		}
		comments = append(comments, page...)
	}
	return comments, nil
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

// ReplyToIssueComment posts a new issue comment on the PR thread.
// Unlike review comments, issue comments don't support threaded replies,
// so this creates a new top-level comment on the PR.
func ReplyToIssueComment(repoPath, prURL string, body string) error {
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

// FetchReviewThreadMap queries the PR's review threads via GraphQL and returns
// a map from comment database ID to the thread's GraphQL node ID. Only
// unresolved threads are included.
func FetchReviewThreadMap(repoPath, prURL string) (map[int]string, error) {
	owner, repo, number, err := ParsePRURL(prURL)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf(`query {
  repository(owner: %q, name: %q) {
    pullRequest(number: %d) {
      reviewThreads(first: 100) {
        nodes {
          id
          isResolved
          comments(first: 1) {
            nodes { databaseId }
          }
        }
      }
    }
  }
}`, owner, repo, number)

	cmd := exec.Command("gh", "api", "graphql", "-f", "query="+query)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("fetching review threads: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return parseReviewThreadMap(out)
}

// parseReviewThreadMap extracts comment-database-ID → thread-node-ID from the
// GraphQL response. Only unresolved threads are included.
func parseReviewThreadMap(data []byte) (map[int]string, error) {
	var resp struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							Comments   struct {
								Nodes []struct {
									DatabaseID int `json:"databaseId"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parsing review threads response: %w", err)
	}

	result := make(map[int]string)
	for _, thread := range resp.Data.Repository.PullRequest.ReviewThreads.Nodes {
		if thread.IsResolved {
			continue
		}
		for _, c := range thread.Comments.Nodes {
			if c.DatabaseID != 0 {
				result[c.DatabaseID] = thread.ID
			}
		}
	}
	return result, nil
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
