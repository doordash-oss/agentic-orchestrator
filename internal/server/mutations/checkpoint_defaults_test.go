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

package mutations

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

const allFalseCheckpointsJSON = `{"inquiry_review":false,"research_review":false,"design_review":false,"roadmap_review":false,"phase_plan_review":false,"manual_publish":false,"draft_publish":false}`

func TestServerMutationTargetCreateFeatureResolvesCheckpoints(t *testing.T) {
	allOn := feature.Checkpoints{InquiryReview: true, ResearchReview: true, DesignReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true}
	tests := []struct {
		name        string
		pipeline    feature.PipelineProfile
		checkpoints string
		defaults    func(*config.Checkpoints)
		want        feature.Checkpoints
	}{
		{
			name:     "omitted uses defaults on large",
			pipeline: feature.PipelineLarge,
			want:     allOn,
		},
		{
			name:     "omitted drops gates medium does not support",
			pipeline: feature.PipelineMedium,
			want:     feature.Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true},
		},
		{
			name:     "omitted follows configured defaults",
			pipeline: feature.PipelineLarge,
			defaults: func(c *config.Checkpoints) { c.DesignReview = false; c.DraftPublish = true },
			want:     feature.Checkpoints{InquiryReview: true, ResearchReview: true, RoadmapReview: true, PhasePlanReview: true, ManualPublish: true, DraftPublish: true},
		},
		{
			name:        "explicit all false stays off",
			pipeline:    feature.PipelineLarge,
			checkpoints: allFalseCheckpointsJSON,
			want:        feature.Checkpoints{},
		},
		{
			name:        "explicit partial is used as given",
			pipeline:    feature.PipelineLarge,
			checkpoints: `{"research_review":true,"manual_publish":true}`,
			want:        feature.Checkpoints{ResearchReview: true, ManualPublish: true},
		},
		{
			name:        "explicit gate medium does not support is dropped",
			pipeline:    feature.PipelineMedium,
			checkpoints: `{"inquiry_review":true,"roadmap_review":true}`,
			want:        feature.Checkpoints{RoadmapReview: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtimeDir := t.TempDir()
			configPath := filepath.Join(runtimeDir, "config.yaml")
			repoPath := filepath.Join(runtimeDir, testRepoAName)
			initMutationGitRepo(t, repoPath)
			cfg := config.NewDefault()
			cfg.Repos[testRepoAName] = config.RepoConfig{Path: repoPath}
			if tt.defaults != nil {
				tt.defaults(&cfg.Defaults.Checkpoints)
			}
			if err := config.Save(configPath, cfg); err != nil {
				t.Fatal(err)
			}
			store := feature.NewStore(filepath.Join(runtimeDir, "features"))
			target := newRESTCreateFeatureTarget(store, feature.NewManager(store, cfg), cfg, configPath)

			body := `{"name":"Gate defaults","repos":["` + testRepoAName + `"],"pipeline":"` + string(tt.pipeline) + `"`
			if tt.checkpoints != "" {
				body += `,"checkpoints":` + tt.checkpoints
			}
			body += `}`
			var req serverruntime.CreateFeatureRequest
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				t.Fatal(err)
			}

			resp, err := target.CreateFeature(req)
			if err != nil {
				t.Fatalf("CreateFeature() error = %v", err)
			}
			created, err := store.Load(resp.FeatureID)
			if err != nil {
				t.Fatal(err)
			}
			if created.Checkpoints != tt.want {
				t.Fatalf("checkpoints = %+v, want %+v", created.Checkpoints, tt.want)
			}
		})
	}
}
