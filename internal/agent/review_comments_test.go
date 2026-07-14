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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

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
