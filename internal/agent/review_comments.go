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

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

type ReviewCommentsData struct {
	Mode     string              `json:"mode"` // "address_all" or "auto"
	Comments []git.ReviewComment `json:"comments"`
}

// ReviewResolution represents the agent's disposition for a single comment.
type ReviewResolution struct {
	CommentID   int    `json:"comment_id"`
	Disposition string `json:"disposition"` // "addressed" or "dismissed"
	Description string `json:"description"` // what was done (addressed) or why (dismissed)
}

// reviewCommentsDir returns the review-comments directory within the
// feature's active run: runs/run-NNN/review-comments/.
func reviewCommentsDir(stateDir string, f *feature.Feature) string {
	return filepath.Join(ActiveRunDir(stateDir, f), "review-comments")
}

func reviewCommentsDirForRepo(stateDir string, f *feature.Feature, repoName string) string {
	return filepath.Join(reviewCommentsDir(stateDir, f), repoName)
}

// SaveReviewCommentsForRepo writes the repo-scoped review comments data to the
// state directory. repoName == "" preserves the legacy single-repo location.
func SaveReviewCommentsForRepo(stateDir string, f *feature.Feature, repoName string, data ReviewCommentsData) error {
	dir := reviewCommentsDirForRepo(stateDir, f, repoName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating review-comments dir: %w", err)
	}
	data.Comments = append([]git.ReviewComment(nil), data.Comments...)
	git.SortReviewCommentsChronologically(data.Comments)
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling review comments: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "comments.json"), b, 0o644)
}

// LoadReviewCommentsForRepo reads the repo-scoped review comments data.
// repoName == "" preserves the legacy single-repo location.
func LoadReviewCommentsForRepo(stateDir string, f *feature.Feature, repoName string) (*ReviewCommentsData, error) {
	path := filepath.Join(reviewCommentsDirForRepo(stateDir, f, repoName), "comments.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result ReviewCommentsData
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parsing review comments: %w", err)
	}
	return &result, nil
}

// LoadReviewResolutionsForRepo reads the repo-scoped review resolutions file.
// repoName == "" preserves the legacy single-repo location.
func LoadReviewResolutionsForRepo(stateDir string, f *feature.Feature, repoName string) ([]ReviewResolution, error) {
	path := filepath.Join(reviewCommentsDirForRepo(stateDir, f, repoName), "review-resolutions.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result []ReviewResolution
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parsing review resolutions: %w", err)
	}
	return result, nil
}

// SaveAddressedIDsForRepo persists which repo-scoped comment IDs have been
// addressed. repoName == "" preserves the legacy single-repo location.
func SaveAddressedIDsForRepo(stateDir string, f *feature.Feature, repoName string, ids []int) error {
	dir := reviewCommentsDirForRepo(stateDir, f, repoName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating review-comments dir: %w", err)
	}
	existing, _ := LoadAddressedIDsForRepo(stateDir, f, repoName)
	for _, id := range ids {
		existing[id] = true
	}
	merged := make([]int, 0, len(existing))
	for id := range existing {
		merged = append(merged, id)
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshaling addressed IDs: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "addressed-ids.json"), b, 0o644)
}

// LoadAddressedIDsForRepo reads the repo-scoped set of previously addressed
// comment IDs. repoName == "" preserves the legacy single-repo location.
func LoadAddressedIDsForRepo(stateDir string, f *feature.Feature, repoName string) (map[int]bool, error) {
	path := filepath.Join(reviewCommentsDirForRepo(stateDir, f, repoName), "addressed-ids.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[int]bool), nil
		}
		return nil, err
	}
	var ids []int
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, fmt.Errorf("parsing addressed IDs: %w", err)
	}
	set := make(map[int]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}
