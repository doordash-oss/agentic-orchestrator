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
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestBuildPostPublishReviewContext(t *testing.T) {
	stateDir := t.TempDir()

	roadmapPath := writeArtifactFile(t, filepath.Join(stateDir, "feat-review", "roadmap"), "roadmap.md")

	f := &feature.Feature{
		ID:                  "feat-review",
		ActiveRun:           1,
		RoadmapPhaseType:    "tdd-fill-in",
		CurrentRoadmapPhase: 2,
		Artifacts: map[string]string{
			"roadmap": roadmapPath,
		},
		Repos: []feature.FeatureRepo{
			{Name: "api", BaseBranch: "main"},
			{Name: "web", BaseBranch: "release"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{},
	}
	f.SetRebaseCount(2)
	f.SetTweakCount(3)

	tests := []struct {
		name         string
		repoName     string
		cycleType    feature.RepoCycleType
		wantArtifact string
		wantDiffBase string
		wantFocus    string
	}{
		{
			name:         "per_repo_rebase",
			repoName:     "web",
			cycleType:    feature.CycleRebase,
			wantArtifact: filepath.Join(stateDir, "feat-review", "runs", "run-001", "rebase-4", "web", "review"),
			wantDiffBase: "release",
			wantFocus:    `repo "web"`,
		},
		{
			name:         "per_repo_tweak",
			repoName:     "web",
			cycleType:    feature.CycleTweak,
			wantArtifact: filepath.Join(stateDir, "feat-review", "runs", "run-001", "tweak-5", "web", "review"),
			wantDiffBase: "release",
			wantFocus:    `repo "web"`,
		},
		{
			name:         "per_repo_review_comments",
			repoName:     "web",
			cycleType:    feature.CycleReviewComments,
			wantArtifact: filepath.Join(stateDir, "feat-review", "runs", "run-001", "review-comments", "web", "review"),
			wantDiffBase: "release",
			wantFocus:    "review-comment follow-up",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.repoName != "" {
				rc := &feature.RepoCycleState{Type: tt.cycleType}
				switch tt.cycleType {
				case feature.CycleRebase:
					rc.Count = 4
				case feature.CycleTweak:
					rc.Count = 5
				}
				f.RepoCycles[tt.repoName] = rc
			}

			got, err := BuildPostPublishReviewContext(stateDir, f, tt.repoName, tt.cycleType)
			if err != nil {
				t.Fatalf("BuildPostPublishReviewContext() error = %v", err)
			}
			if got.ArtifactDir != tt.wantArtifact {
				t.Errorf("ArtifactDir = %q, want %q", got.ArtifactDir, tt.wantArtifact)
			}
			if got.RoadmapPath != roadmapPath {
				t.Errorf("RoadmapPath = %q, want %q", got.RoadmapPath, roadmapPath)
			}
			if got.DiffBase != tt.wantDiffBase {
				t.Errorf("DiffBase = %q, want %q", got.DiffBase, tt.wantDiffBase)
			}
			if got.PhaseType != "tdd-fill-in" {
				t.Errorf("PhaseType = %q, want tdd-fill-in", got.PhaseType)
			}
			if !strings.Contains(got.CycleFocus, tt.wantFocus) {
				t.Errorf("CycleFocus = %q, want it to contain %q", got.CycleFocus, tt.wantFocus)
			}
			if !strings.Contains(got.CycleFocus, "Scope bound") {
				t.Errorf("CycleFocus missing scope-bound clause that prevents reviewers from regrading the originating phase plan: %q", got.CycleFocus)
			}
			if !strings.Contains(got.CycleFocus, "testing-contract.yaml") {
				t.Errorf("CycleFocus should anchor scope to the cycle's testing-contract.yaml: %q", got.CycleFocus)
			}
		})
	}
}

func writeArtifactFile(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatalf("Chtimes(%q): %v", path, err)
	}
	return path
}
