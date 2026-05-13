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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

func TestBuildReviewCommentsPlan(t *testing.T) {
	comments := []git.ReviewComment{
		{ID: 1, Path: "main.go", Line: 10, Body: "Fix this", Type: git.CommentTypeReview, User: struct {
			Login string `json:"login"`
		}{Login: "reviewer"}},
	}

	t.Run("auto mode", func(t *testing.T) {
		plan := BuildReviewCommentsPlan(comments, "https://github.com/o/r/pull/1", "auto", "/tmp/res.json")
		if !strings.Contains(plan, "Agent Decides") {
			t.Error("expected 'Agent Decides' in plan")
		}
	})

	t.Run("issue comment shows PR conversation", func(t *testing.T) {
		mixed := []git.ReviewComment{
			{ID: 1, Path: "main.go", Line: 10, Body: "inline feedback", Type: git.CommentTypeReview, User: struct {
				Login string `json:"login"`
			}{Login: "reviewer"}},
			{ID: 2, Body: "general feedback", Type: git.CommentTypeIssue, User: struct {
				Login string `json:"login"`
			}{Login: "reviewer"}},
		}
		plan := BuildReviewCommentsPlan(mixed, "https://github.com/o/r/pull/1", "auto", "/tmp/res.json")
		if !strings.Contains(plan, "**Location**: PR conversation") {
			t.Error("expected 'PR conversation' for issue comment")
		}
		if !strings.Contains(plan, "**File**: `main.go`:10") {
			t.Error("expected file path for review comment")
		}
		if !strings.Contains(plan, "Comment 1 (ID: 1)") {
			t.Error("expected first comment")
		}
		if !strings.Contains(plan, "Comment 2 (ID: 2)") {
			t.Error("expected second comment")
		}
	})

	t.Run("review body comment shows PR review location", func(t *testing.T) {
		comments := []git.ReviewComment{
			{ID: 1, Body: "review body feedback", Type: git.CommentTypeReviewBody, User: struct {
				Login string `json:"login"`
			}{Login: "reviewer"}},
		}
		plan := BuildReviewCommentsPlan(comments, "https://github.com/o/r/pull/1", "auto", "/tmp/res.json")
		if !strings.Contains(plan, "**Location**: PR review") {
			t.Error("expected 'PR review' for review body comment")
		}
	})

	t.Run("uses generic verification commands and review artifact context", func(t *testing.T) {
		plan := BuildReviewCommentsPlan(comments, "https://github.com/o/r/pull/1", "auto", "/tmp/review-resolutions.json")

		for _, want := range []string{
			"`run the project build command`",
			"`run the project linter`",
			"`run the full test suite`",
			"`comments.json`",
			"`review-resolutions.json`",
			"cycle-local verification report",
			"/tmp/review-resolutions.json",
		} {
			if !strings.Contains(plan, want) {
				t.Errorf("expected plan to contain %q", want)
			}
		}

		for _, unwanted := range []string{
			"run the project's test suite",
			"run any linters or type checkers",
			"Verify the build succeeds",
		} {
			if strings.Contains(plan, unwanted) {
				t.Errorf("expected plan to omit placeholder text %q", unwanted)
			}
		}
	})
}

func TestSaveLoadReviewComments(t *testing.T) {
	dir := t.TempDir()
	f := &feature.Feature{ID: "test-feature", ActiveRun: 1}

	data := ReviewCommentsData{
		Mode: "auto",
		Comments: []git.ReviewComment{
			{ID: 42, Path: "foo.go", Body: "needs fix", Type: git.CommentTypeReview},
			{ID: 99, Body: "general note", Type: git.CommentTypeIssue},
		},
	}

	if err := SaveReviewComments(dir, f, data); err != nil {
		t.Fatalf("SaveReviewComments: %v", err)
	}

	loaded, err := LoadReviewComments(dir, f)
	if err != nil {
		t.Fatalf("LoadReviewComments: %v", err)
	}
	if loaded.Mode != "auto" || len(loaded.Comments) != 2 {
		t.Fatalf("unexpected loaded data: %+v", loaded)
	}
	if loaded.Comments[0].ID != 42 || loaded.Comments[0].Type != git.CommentTypeReview {
		t.Errorf("unexpected review comment: %+v", loaded.Comments[0])
	}
	if loaded.Comments[1].ID != 99 || loaded.Comments[1].Type != git.CommentTypeIssue {
		t.Errorf("unexpected issue comment: %+v", loaded.Comments[1])
	}
}

func TestSaveLoadReviewCommentsForRepo(t *testing.T) {
	dir := t.TempDir()
	f := &feature.Feature{ID: "test-feature", ActiveRun: 1}

	data := ReviewCommentsData{
		Mode: "auto",
		Comments: []git.ReviewComment{
			{ID: 42, Path: "foo.go", Body: "needs fix", Type: git.CommentTypeReview},
		},
	}

	if err := SaveReviewCommentsForRepo(dir, f, "repo-a", data); err != nil {
		t.Fatalf("SaveReviewCommentsForRepo: %v", err)
	}

	loaded, err := LoadReviewCommentsForRepo(dir, f, "repo-a")
	if err != nil {
		t.Fatalf("LoadReviewCommentsForRepo: %v", err)
	}
	if loaded.Mode != "auto" || len(loaded.Comments) != 1 || loaded.Comments[0].ID != 42 {
		t.Fatalf("unexpected loaded repo-scoped data: %+v", loaded)
	}
}

func TestSaveLoadAddressedIDs(t *testing.T) {
	dir := t.TempDir()
	f := &feature.Feature{ID: "test-feature", ActiveRun: 1}

	// Initially empty
	ids, err := LoadAddressedIDs(dir, f)
	if err != nil {
		t.Fatalf("LoadAddressedIDs (empty): %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected empty set, got %v", ids)
	}

	// Save some IDs
	if err := SaveAddressedIDs(dir, f, []int{1, 2, 3}); err != nil {
		t.Fatalf("SaveAddressedIDs: %v", err)
	}

	ids, err = LoadAddressedIDs(dir, f)
	if err != nil {
		t.Fatalf("LoadAddressedIDs: %v", err)
	}
	if len(ids) != 3 || !ids[1] || !ids[2] || !ids[3] {
		t.Errorf("expected {1,2,3}, got %v", ids)
	}

	// Merge additional IDs (with overlap)
	if err := SaveAddressedIDs(dir, f, []int{3, 4, 5}); err != nil {
		t.Fatalf("SaveAddressedIDs (merge): %v", err)
	}

	ids, err = LoadAddressedIDs(dir, f)
	if err != nil {
		t.Fatalf("LoadAddressedIDs (merged): %v", err)
	}
	if len(ids) != 5 {
		t.Errorf("expected 5 IDs after merge, got %d: %v", len(ids), ids)
	}
	for _, id := range []int{1, 2, 3, 4, 5} {
		if !ids[id] {
			t.Errorf("expected ID %d in set", id)
		}
	}
}

func TestSaveLoadAddressedIDsForRepo(t *testing.T) {
	dir := t.TempDir()
	f := &feature.Feature{ID: "test-feature", ActiveRun: 1}

	if err := SaveAddressedIDsForRepo(dir, f, "repo-a", []int{7, 8}); err != nil {
		t.Fatalf("SaveAddressedIDsForRepo: %v", err)
	}
	if err := SaveAddressedIDsForRepo(dir, f, "repo-a", []int{8, 9}); err != nil {
		t.Fatalf("SaveAddressedIDsForRepo merge: %v", err)
	}

	ids, err := LoadAddressedIDsForRepo(dir, f, "repo-a")
	if err != nil {
		t.Fatalf("LoadAddressedIDsForRepo: %v", err)
	}
	if len(ids) != 3 || !ids[7] || !ids[8] || !ids[9] {
		t.Fatalf("unexpected repo-scoped addressed ids: %v", ids)
	}
}

func TestSaveLoadReviewResolutions(t *testing.T) {
	dir := t.TempDir()
	f := &feature.Feature{ID: "test-feature", ActiveRun: 1}

	resolutions := []ReviewResolution{
		{CommentID: 1, Disposition: "addressed", Description: "Fixed it"},
		{CommentID: 2, Disposition: "dismissed", Description: "Already handled"},
	}

	// Write resolutions file under runs/run-001/review-comments/.
	resDir := filepath.Join(ActiveRunDir(dir, f), "review-comments")
	_ = os.MkdirAll(resDir, 0o755)
	b, _ := json.MarshalIndent(resolutions, "", "  ")
	_ = os.WriteFile(filepath.Join(resDir, "review-resolutions.json"), b, 0o644)

	loaded, err := LoadReviewResolutions(dir, f)
	if err != nil {
		t.Fatalf("LoadReviewResolutions: %v", err)
	}
	if len(loaded) != 2 || loaded[0].Disposition != "addressed" {
		t.Errorf("unexpected resolutions: %+v", loaded)
	}
}

func TestLoadReviewResolutionsForRepo(t *testing.T) {
	dir := t.TempDir()
	f := &feature.Feature{ID: "test-feature", ActiveRun: 1}

	resolutions := []ReviewResolution{
		{CommentID: 1, Disposition: "addressed", Description: "Fixed it"},
	}

	resDir := filepath.Join(ActiveRunDir(dir, f), "review-comments", "repo-a")
	_ = os.MkdirAll(resDir, 0o755)
	b, _ := json.MarshalIndent(resolutions, "", "  ")
	_ = os.WriteFile(filepath.Join(resDir, "review-resolutions.json"), b, 0o644)

	loaded, err := LoadReviewResolutionsForRepo(dir, f, "repo-a")
	if err != nil {
		t.Fatalf("LoadReviewResolutionsForRepo: %v", err)
	}
	if len(loaded) != 1 || loaded[0].CommentID != 1 {
		t.Fatalf("unexpected repo-scoped resolutions: %+v", loaded)
	}
}
