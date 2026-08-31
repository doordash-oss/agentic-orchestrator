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

import "fmt"

// ReviewThreadMap returns comment-database-ID → thread node ID for every
// unresolved review thread on a PR (first 100 threads).
func (c *Client) ReviewThreadMap(owner, repo string, number int) (map[int]string, error) {
	var resp struct {
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
	}
	query := `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100) {
        nodes { id isResolved comments(first: 1) { nodes { databaseId } } }
      }
    }
  }
}`
	variables := map[string]interface{}{"owner": owner, "name": repo, "number": number}
	if err := c.gql.Do(query, variables, &resp); err != nil {
		return nil, fmt.Errorf("fetching review threads: %w", err)
	}
	result := make(map[int]string)
	for _, thread := range resp.Repository.PullRequest.ReviewThreads.Nodes {
		if thread.IsResolved {
			continue
		}
		for _, comment := range thread.Comments.Nodes {
			if comment.DatabaseID != 0 {
				result[comment.DatabaseID] = thread.ID
			}
		}
	}
	return result, nil
}

// ResolveReviewThread marks one review thread resolved.
func (c *Client) ResolveReviewThread(threadNodeID string) error {
	var resp struct {
		ResolveReviewThread struct {
			Thread struct {
				IsResolved bool `json:"isResolved"`
			} `json:"thread"`
		} `json:"resolveReviewThread"`
	}
	mutation := `mutation($threadID: ID!) {
  resolveReviewThread(input: {threadId: $threadID}) { thread { isResolved } }
}`
	if err := c.gql.Do(mutation, map[string]interface{}{"threadID": threadNodeID}, &resp); err != nil {
		return fmt.Errorf("resolving review thread: %w", err)
	}
	return nil
}
