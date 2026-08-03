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
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
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
