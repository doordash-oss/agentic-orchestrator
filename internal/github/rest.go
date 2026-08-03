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

package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
)

// PRComment is one inline review comment or issue comment as returned by
// the GitHub REST API (subset of fields the orchestrator consumes).
type PRComment struct {
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
}

// ListPRReviewComments returns all inline review comments on a PR.
func (c *Client) ListPRReviewComments(owner, repo string, number int) ([]PRComment, error) {
	return getPaginated[PRComment](c, fmt.Sprintf("repos/%s/%s/pulls/%d/comments", owner, repo, number))
}

var linkNextPattern = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// getPaginated GETs path, following Link rel="next" headers until
// exhausted. GitHub emits absolute next URLs; go-gh's RESTClient accepts
// scheme-prefixed request paths verbatim, so they are passed through.
func getPaginated[T any](c *Client, path string) ([]T, error) {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	next := path + separator + "per_page=100"
	var all []T
	for next != "" {
		resp, err := c.rest.Request(http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		var page []T
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decoding GitHub response for %s: %w", path, decodeErr)
		}
		all = append(all, page...)
		next = ""
		if match := linkNextPattern.FindStringSubmatch(resp.Header.Get("Link")); match != nil {
			next = match[1]
		}
	}
	return all, nil
}

// PRReview is a submitted PR review (its top-level body, not inline comments).
type PRReview struct {
	ID   int    `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	SubmittedAt string `json:"submitted_at"`
}

// ListIssueComments returns all conversation comments on a PR.
func (c *Client) ListIssueComments(owner, repo string, number int) ([]PRComment, error) {
	return getPaginated[PRComment](c, fmt.Sprintf("repos/%s/%s/issues/%d/comments", owner, repo, number))
}

// ListPRReviews returns all submitted reviews on a PR.
func (c *Client) ListPRReviews(owner, repo string, number int) ([]PRReview, error) {
	return getPaginated[PRReview](c, fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", owner, repo, number))
}

// PRInfo is the subset of pull-request details the orchestrator consumes.
type PRInfo struct {
	Body    string
	BaseRef string
	URL     string
}

// GetPR returns body, base branch, and canonical URL of a PR.
func (c *Client) GetPR(owner, repo string, number int) (PRInfo, error) {
	var raw struct {
		Body string `json:"body"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		HTMLURL string `json:"html_url"`
	}
	if err := c.rest.Get(fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, number), &raw); err != nil {
		return PRInfo{}, err
	}
	return PRInfo{Body: raw.Body, BaseRef: raw.Base.Ref, URL: raw.HTMLURL}, nil
}

// CreatePRParams describes a pull request to open.
type CreatePRParams struct {
	Owner, Repo, Head, Base, Title, Body string
	Draft                                bool
}

// CreatePR opens a pull request and returns its URL. An empty Base
// targets the repository's default branch. If GitHub answers 422 with
// "already exists", the existing open PR's URL is returned instead
// (the branch push that preceded the call already updated it).
func (c *Client) CreatePR(p CreatePRParams) (string, error) {
	base := p.Base
	if base == "" {
		var repoInfo struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := c.rest.Get(fmt.Sprintf("repos/%s/%s", p.Owner, p.Repo), &repoInfo); err != nil {
			return "", fmt.Errorf("resolving default branch: %w", err)
		}
		base = repoInfo.DefaultBranch
	}
	payload, err := json.Marshal(map[string]any{
		"title": p.Title, "body": p.Body, "head": p.Head, "base": base, "draft": p.Draft,
	})
	if err != nil {
		return "", err
	}
	var created struct {
		HTMLURL string `json:"html_url"`
	}
	err = c.rest.Post(fmt.Sprintf("repos/%s/%s/pulls", p.Owner, p.Repo), bytes.NewReader(payload), &created)
	if err == nil {
		return created.HTMLURL, nil
	}
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnprocessableEntity &&
		strings.Contains(err.Error(), "already exists") {
		var open []struct {
			HTMLURL string `json:"html_url"`
		}
		lookup := fmt.Sprintf("repos/%s/%s/pulls?head=%s&state=open",
			p.Owner, p.Repo, url.QueryEscape(p.Owner+":"+p.Head))
		if lookupErr := c.rest.Get(lookup, &open); lookupErr == nil && len(open) > 0 {
			return open[0].HTMLURL, nil
		}
	}
	return "", fmt.Errorf("creating PR: %w", err)
}

func (c *Client) patchPR(owner, repo string, number int, fields map[string]any) error {
	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return c.rest.Patch(fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, number), bytes.NewReader(payload), nil)
}

// UpdatePRBody replaces a PR's description.
func (c *Client) UpdatePRBody(owner, repo string, number int, body string) error {
	return c.patchPR(owner, repo, number, map[string]any{"body": body})
}

// ClosePR closes a PR without merging.
func (c *Client) ClosePR(owner, repo string, number int) error {
	return c.patchPR(owner, repo, number, map[string]any{"state": "closed"})
}

func (c *Client) postComment(path, body string) error {
	payload, err := json.Marshal(map[string]any{"body": body})
	if err != nil {
		return err
	}
	return c.rest.Post(path, bytes.NewReader(payload), nil)
}

// ReplyToReviewComment replies in-thread to an inline review comment.
func (c *Client) ReplyToReviewComment(owner, repo string, number, commentID int, body string) error {
	return c.postComment(fmt.Sprintf("repos/%s/%s/pulls/%d/comments/%d/replies", owner, repo, number, commentID), body)
}

// CreateIssueComment posts a top-level conversation comment on a PR.
func (c *Client) CreateIssueComment(owner, repo string, number int, body string) error {
	return c.postComment(fmt.Sprintf("repos/%s/%s/issues/%d/comments", owner, repo, number), body)
}
