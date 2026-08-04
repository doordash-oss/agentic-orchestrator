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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestFindRepo(t *testing.T) {
	tests := []struct {
		name     string
		feature  *feature.Feature
		repoName string
		wantNil  bool
		wantName string
	}{
		{
			name: "found",
			feature: &feature.Feature{
				Repos: []feature.FeatureRepo{
					{Name: "alpha", Path: "/tmp/alpha"},
					{Name: "beta", Path: "/tmp/beta"},
				},
				SchemaVersion: feature.SchemaVersionCurrent,
			},
			repoName: "beta",
			wantNil:  false,
			wantName: "beta",
		},
		{
			name: "not found",
			feature: &feature.Feature{
				Repos: []feature.FeatureRepo{
					{Name: "alpha", Path: "/tmp/alpha"},
				},
				SchemaVersion: feature.SchemaVersionCurrent,
			},
			repoName: "gamma",
			wantNil:  true,
		},
		{
			name: "empty repos",
			feature: &feature.Feature{
				Repos:         nil,
				SchemaVersion: feature.SchemaVersionCurrent,
			},
			repoName: "any",
			wantNil:  true,
		},
		{
			name: "first repo",
			feature: &feature.Feature{
				Repos: []feature.FeatureRepo{
					{Name: "first", Path: "/tmp/first"},
					{Name: "second", Path: "/tmp/second"},
				},
				SchemaVersion: feature.SchemaVersionCurrent,
			},
			repoName: "first",
			wantNil:  false,
			wantName: "first",
		},
		{
			name: "returns pointer to slice element",
			feature: &feature.Feature{
				Repos: []feature.FeatureRepo{
					{Name: "repo", Path: "/tmp/repo", WorktreePath: "/tmp/wt"},
				},
				SchemaVersion: feature.SchemaVersionCurrent,
			},
			repoName: "repo",
			wantNil:  false,
			wantName: "repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findRepo(tt.feature, tt.repoName)
			if tt.wantNil {
				if got != nil {
					t.Errorf("findRepo() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("findRepo() returned nil, want non-nil")
			}
			if got.Name != tt.wantName {
				t.Errorf("findRepo().Name = %q, want %q", got.Name, tt.wantName)
			}
		})
	}
}

func TestOrchestratorResultTypes(t *testing.T) {
	result := &OrchestratorResult{
		FinalStatus: "all_passed",
	}
	if result.FinalStatus != "all_passed" {
		t.Errorf("FinalStatus = %q, want %q", result.FinalStatus, "all_passed")
	}
	if len(result.FailedRepos) != 0 {
		t.Errorf("FailedRepos should be empty, got %v", result.FailedRepos)
	}

	failedResult := &OrchestratorResult{
		FinalStatus: "failed",
		FailedRepos: []string{"repo-a", "repo-b"},
		LastError:   "something went wrong",
	}
	if failedResult.FinalStatus != "failed" {
		t.Errorf("FinalStatus = %q, want %q", failedResult.FinalStatus, "failed")
	}
	if len(failedResult.FailedRepos) != 2 {
		t.Errorf("FailedRepos length = %d, want 2", len(failedResult.FailedRepos))
	}
	if failedResult.LastError == "" {
		t.Error("expected non-empty LastError")
	}
}

func TestOrchestratorUsesResolvedConfig(t *testing.T) {
	cfg := OrchestratorConfig{
		Model:               "custom-impl-model",
		ReviewModel:         "custom-review-model",
		MaxIterations:       7,
		MaxConsecFails:      5,
		MaxConsecNoProgress: 4,
		BuildSession:        mockBuildSession("", ""),
	}

	if cfg.Model != "custom-impl-model" {
		t.Errorf("Model = %q, want %q", cfg.Model, "custom-impl-model")
	}
	if cfg.ReviewModel != "custom-review-model" {
		t.Errorf("ReviewModel = %q, want %q", cfg.ReviewModel, "custom-review-model")
	}
	if cfg.MaxIterations != 7 {
		t.Errorf("MaxIterations = %d, want %d", cfg.MaxIterations, 7)
	}
	if cfg.MaxConsecFails != 5 {
		t.Errorf("MaxConsecFails = %d, want %d", cfg.MaxConsecFails, 5)
	}
	if cfg.MaxConsecNoProgress != 4 {
		t.Errorf("MaxConsecNoProgress = %d, want %d", cfg.MaxConsecNoProgress, 4)
	}
}

func TestResolveImplementArtifactDirForRepo(t *testing.T) {
	tests := []struct {
		name    string
		feature *feature.Feature
		runDir  string
		repo    string
		want    string
	}{
		{
			name: "single repo plain",
			feature: &feature.Feature{
				SchemaVersion: feature.SchemaVersionCurrent,
				Repos:         []feature.FeatureRepo{{Name: "svc"}},
			},
			runDir: "/run/r-001",
			repo:   "svc",
			want:   "/run/r-001/implement/svc",
		},
		{
			name: "multi repo plain",
			feature: &feature.Feature{
				SchemaVersion: feature.SchemaVersionCurrent,
				Repos:         []feature.FeatureRepo{{Name: "a"}, {Name: "b"}},
			},
			runDir: "/run/r-001",
			repo:   "a",
			want:   "/run/r-001/implement/a",
		},
		{
			name: "single repo with roadmap phase",
			feature: &feature.Feature{
				SchemaVersion:       feature.SchemaVersionCurrent,
				Repos:               []feature.FeatureRepo{{Name: "svc"}},
				CurrentRoadmapPhase: 3,
			},
			runDir: "/run/r-002",
			repo:   "svc",
		want:   "/run/r-002/phase-03/implement/svc",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveImplementArtifactDirForRepo(tt.feature, tt.runDir, tt.repo)
			if got != tt.want {
				t.Errorf("resolveImplementArtifactDirForRepo() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestImplementConfig_PhaseTypeAndRoadmapPath(t *testing.T) {
	f := &feature.Feature{
		RoadmapPhaseType: "tdd-fill-in",
		Artifacts:        map[string]string{"roadmap": "roadmap.md"},
		SchemaVersion:    feature.SchemaVersionCurrent,
	}
	cfg := ImplementConfig{
		PhaseType:    f.RoadmapPhaseType,
		RoadmapPath:  f.Artifacts["roadmap"],
		BuildSession: mockBuildSession("", ""),
	}
	if cfg.PhaseType != "tdd-fill-in" {
		t.Errorf("PhaseType = %q, want %q", cfg.PhaseType, "tdd-fill-in")
	}
	if cfg.RoadmapPath != "roadmap.md" {
		t.Errorf("RoadmapPath = %q, want %q", cfg.RoadmapPath, "roadmap.md")
	}
}
