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
	"sort"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type ReviewCommentsData struct {
	Mode     string                `json:"mode"` // "address_all" or "auto"
	Comments []ports.ReviewComment `json:"comments"`
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

// SaveReviewComments writes the review comments data to the state directory.
func SaveReviewComments(stateDir string, f *feature.Feature, data ReviewCommentsData) error {
	return SaveReviewCommentsForRepo(stateDir, f, "", data)
}

// SaveReviewCommentsForRepo writes the repo-scoped review comments data to the
// state directory. repoName == "" preserves the legacy single-repo location.
func SaveReviewCommentsForRepo(stateDir string, f *feature.Feature, repoName string, data ReviewCommentsData) error {
	dir := reviewCommentsDirForRepo(stateDir, f, repoName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating review-comments dir: %w", err)
	}
	data.Comments = append([]ports.ReviewComment(nil), data.Comments...)
	sortReviewCommentsChronologically(data.Comments)
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling review comments: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "comments.json"), b, 0o644)
}

// LoadReviewComments reads the saved review comments data.
func LoadReviewComments(stateDir string, f *feature.Feature) (*ReviewCommentsData, error) {
	return LoadReviewCommentsForRepo(stateDir, f, "")
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

// LoadReviewResolutions reads the agent's review resolutions file.
func LoadReviewResolutions(stateDir string, f *feature.Feature) ([]ReviewResolution, error) {
	return LoadReviewResolutionsForRepo(stateDir, f, "")
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

// SaveAddressedIDs persists which comment IDs have been addressed. It merges
// with any previously saved IDs so the set grows across iterations.
func SaveAddressedIDs(stateDir string, f *feature.Feature, ids []int) error {
	return SaveAddressedIDsForRepo(stateDir, f, "", ids)
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

// LoadAddressedIDs reads the set of previously addressed comment IDs.
// Returns an empty map (not an error) if the file doesn't exist yet.
func LoadAddressedIDs(stateDir string, f *feature.Feature) (map[int]bool, error) {
	return LoadAddressedIDsForRepo(stateDir, f, "")
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

// BuildReviewCommentsPlan generates a plan document for addressing review comments.
// resolutionsPath is where the agent writes dispositions.
func BuildReviewCommentsPlan(comments []ports.ReviewComment, prURL, mode, resolutionsPath string) string {
	ordered := append([]ports.ReviewComment(nil), comments...)
	sortReviewCommentsChronologically(ordered)

	var b strings.Builder

	b.WriteString("# Address Review Comments Plan\n\n")
	b.WriteString("## Overview\n\n")
	b.WriteString(fmt.Sprintf("The feature's PR (%s) has received review comments that need attention.\n\n", prURL))

	b.WriteString(standardImplementCycleCommunicationContract())

	b.WriteString("## Mode: Agent Decides\n\n")
	b.WriteString("For each review comment below, decide whether to:\n")
	b.WriteString("- **Address it**: Make code changes to resolve the feedback\n")
	b.WriteString("- **Dismiss it**: If the comment is already handled, not applicable, or the current approach is better — explain your reasoning\n\n")

	b.WriteString("## Review Comments\n\n")
	for i, c := range ordered {
		b.WriteString(fmt.Sprintf("### Comment %d (ID: %d)\n", i+1, c.ID))
		switch c.Type {
		case ports.CommentTypeIssue:
			b.WriteString("**Location**: PR conversation\n")
		case ports.CommentTypeReviewBody:
			b.WriteString("**Location**: PR review\n")
		default:
			b.WriteString(fmt.Sprintf("**File**: `%s`", c.Path))
			if c.Line > 0 {
				b.WriteString(fmt.Sprintf(":%d", c.Line))
			}
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("**Author**: @%s\n", c.User.Login))
		if c.DiffHunk != "" {
			b.WriteString(fmt.Sprintf("**Context**:\n```diff\n%s\n```\n", c.DiffHunk))
		}
		b.WriteString(fmt.Sprintf("**Comment**:\n> %s\n\n", strings.ReplaceAll(c.Body, "\n", "\n> ")))
	}

	b.WriteString("## Resolution Tracking\n\n")
	b.WriteString(fmt.Sprintf("After addressing or deciding on each comment, write a JSON file at:\n`%s`\n\n", resolutionsPath))
	b.WriteString("Format:\n```json\n[\n")
	b.WriteString(`  {"comment_id": 123, "disposition": "addressed", "description": "Fixed error handling"},`)
	b.WriteString("\n")
	b.WriteString(`  {"comment_id": 456, "disposition": "dismissed", "description": "Already handled by existing validation"}`)
	b.WriteString("\n]\n```\n\n")
	b.WriteString("Every comment listed above MUST have an entry in this file.\n\n")

	b.WriteString("## Verification\n\n")
	b.WriteString("Review the verification evidence together with the cycle artifacts below:\n")
	b.WriteString("- `comments.json`\n")
	b.WriteString("- `review-resolutions.json`\n")
	b.WriteString("- the cycle-local verification report\n\n")

	b.WriteString("#### Automated Verification:\n")
	writeGenericProjectVerificationChecklist(&b)

	b.WriteString("## Success Criteria\n\n")
	b.WriteString("- All addressed comments have corresponding code changes\n")
	b.WriteString("- Relevant tests pass\n")
	b.WriteString("- The build succeeds\n")
	b.WriteString(fmt.Sprintf("- `%s` is written with an entry for every comment\n\n", resolutionsPath))

	b.WriteString("## Important Notes\n\n")
	b.WriteString("- Do NOT create a new branch. Work on the current branch.\n")
	b.WriteString("- Do NOT amend or squash commits unnecessarily.\n")
	b.WriteString("- Make targeted changes that directly address the feedback.\n")

	return b.String()
}

func sortReviewCommentsChronologically(comments []ports.ReviewComment) {
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
