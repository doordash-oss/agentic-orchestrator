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
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestDiffBaseForRepo(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Repos: []feature.FeatureRepo{
			{Name: "api", BaseBranch: "develop"},
			{Name: "web", BaseBranch: ""},
		},
	}
	tests := []struct {
		name     string
		repoName string
		want     string
	}{
		{"api has base branch", "api", "develop"},
		{"web empty base falls back to main", "web", "main"},
		{"unknown repo falls back to main", "unknown", "main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DiffBaseForRepo(f, tt.repoName)
			if got != tt.want {
				t.Errorf("DiffBaseForRepo(%q) = %q, want %q", tt.repoName, got, tt.want)
			}
		})
	}
}

func TestDiffBaseForRepoNilFeature(t *testing.T) {
	t.Parallel()
	if got := DiffBaseForRepo(nil, "api"); got != "main" {
		t.Errorf("DiffBaseForRepo(nil, ...) = %q, want %q", got, "main")
	}
}

func TestReviewDiffRangeForRepo_Round1Fallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// No iterations on disk → round 1 fallback
	f := &feature.Feature{
		Repos: []feature.FeatureRepo{
			{Name: "api", BaseBranch: "develop"},
		},
	}
	r := ReviewDiffRangeForRepo(dir, "api", f)
	if r.Base != "develop" {
		t.Errorf("expected base 'develop', got %q", r.Base)
	}
	if r.Head != "HEAD" {
		t.Errorf("expected head 'HEAD', got %q", r.Head)
	}
}

func TestReviewDiffRangeForRepo_Round1FallbackToMain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := &feature.Feature{
		Repos: []feature.FeatureRepo{
			{Name: "web", BaseBranch: ""},
		},
	}
	r := ReviewDiffRangeForRepo(dir, "web", f)
	if r.Base != "main" {
		t.Errorf("expected base 'main', got %q", r.Base)
	}
}

func TestReviewDiffRangeForRepo_UsesPriorAnchorFromDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	am := NewArtifactManager(dir)

	// Write iteration-01 with an anchor for "api"
	iterDir1, _ := am.CreateIterationDir(1)
	meta1 := IterationMeta{
		Iteration:    1,
		ReviewStatus: "changes_requested",
		Anchors: RepoAnchors{
			"api": {Base: "aaaa", Head: "bbbb"},
		},
	}
	if err := am.WriteMeta(iterDir1, meta1); err != nil {
		t.Fatalf("write meta1: %v", err)
	}

	// Write iteration-02 with no anchor for "api" (different repo or failure)
	iterDir2, _ := am.CreateIterationDir(2)
	meta2 := IterationMeta{
		Iteration:    2,
		ReviewStatus: "approved",
		Anchors: RepoAnchors{
			"web": {Base: "cccc", Head: "dddd"},
		},
	}
	if err := am.WriteMeta(iterDir2, meta2); err != nil {
		t.Fatalf("write meta2: %v", err)
	}

	f := &feature.Feature{
		Repos: []feature.FeatureRepo{
			{Name: "api", BaseBranch: "develop"},
			{Name: "web", BaseBranch: "main"},
		},
	}

	// "api" should use the head from iteration-01's anchor
	r := ReviewDiffRangeForRepo(dir, "api", f)
	if r.Base != "bbbb" {
		t.Errorf("api base should be iteration-01 head %q, got %q", "bbbb", r.Base)
	}

	// "web" should use the head from iteration-02's anchor
	r = ReviewDiffRangeForRepo(dir, "web", f)
	if r.Base != "dddd" {
		t.Errorf("web base should be iteration-02 head %q, got %q", "dddd", r.Base)
	}

	// "unknown" repo with no anchor falls back to feature's default
	r = ReviewDiffRangeForRepo(dir, "unknown", f)
	if r.Base != "main" {
		t.Errorf("unknown base should be 'main', got %q", r.Base)
	}
}

func TestReviewDiffRangeForRepo_EmptyRangeWhenHeadEqualsBase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	am := NewArtifactManager(dir)

	iterDir, _ := am.CreateIterationDir(1)
	meta := IterationMeta{
		Iteration:    1,
		ReviewStatus: "approved",
		Anchors: RepoAnchors{
			"api": {Base: "same", Head: "same"},
		},
	}
	if err := am.WriteMeta(iterDir, meta); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	f := &feature.Feature{Repos: []feature.FeatureRepo{{Name: "api"}}}
	r := ReviewDiffRangeForRepo(dir, "api", f)
	// The range base is the prior anchor head "same", head is "HEAD".
	// The range is not empty (base != head) since head defaults to "HEAD".
	if r.Base != "same" {
		t.Errorf("expected base 'same', got %q", r.Base)
	}
	// But an explicitly empty range where base==head should be detectable.
	emptyRange := ReviewDiffRange{Base: "abc", Head: "abc"}
	if !emptyRange.IsEmpty() {
		t.Error("expected empty range when base equals head")
	}
}

func TestReviewDiffRangeForRepo_ReadsFromDiskNotMemory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Write a meta.yaml directly on disk (simulating a resumed process)
	iterDir := filepath.Join(dir, "iteration-01")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := []byte("iteration: 1\nreview_status: changes_requested\nanchors:\n  api:\n    base: old-base\n    head: old-head\n")
	if err := os.WriteFile(filepath.Join(iterDir, "meta.yaml"), yaml, 0o644); err != nil {
		t.Fatal(err)
	}

	f := &feature.Feature{Repos: []feature.FeatureRepo{{Name: "api", BaseBranch: "main"}}}
	r := ReviewDiffRangeForRepo(dir, "api", f)
	if r.Base != "old-head" {
		t.Errorf("expected base from disk 'old-head', got %q", r.Base)
	}
}

func TestLatestAnchorHeadForRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	am := NewArtifactManager(dir)

	// No iterations → empty
	if got := LatestAnchorHeadForRepo(dir, "api"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	// Write iteration-01 with anchor
	iterDir1, _ := am.CreateIterationDir(1)
	meta1 := IterationMeta{
		Iteration:    1,
		Anchors:       RepoAnchors{"api": {Base: "a1", Head: "h1"}},
	}
	if err := am.WriteMeta(iterDir1, meta1); err != nil {
		t.Fatal(err)
	}

	// Write iteration-02 with anchor
	iterDir2, _ := am.CreateIterationDir(2)
	meta2 := IterationMeta{
		Iteration:    2,
		Anchors:       RepoAnchors{"api": {Base: "a2", Head: "h2"}},
	}
	if err := am.WriteMeta(iterDir2, meta2); err != nil {
		t.Fatal(err)
	}

	// Should return the latest (iteration-02) head
	if got := LatestAnchorHeadForRepo(dir, "api"); got != "h2" {
		t.Errorf("expected 'h2', got %q", got)
	}
}
