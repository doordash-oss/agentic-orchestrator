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
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// RolePlanRoadmapPlanner is the top-level roadmap planner session.
const RolePlanRoadmapPlanner Role = "plan_roadmap_planner"

var roadmapCreatorRoleSpec = roleSpec{
	Phase:     feature.PhasePlan,
	Role:      RolePlanRoadmapPlanner,
	SkillName: "create-roadmap",
	OutputRoots: []outputRootSpec{
		artifactDirOutputRoot("Shared roadmap artifact root. The roadmap markdown is written here across attempts."),
		attemptDirOutputRoot("Active roadmap attempt directory. Debug prompts, attempt metadata, and validator output are written here; the harness records its completion receipt here after validation."),
	},
	Artifacts: []roleArtifactSpec{
		roadmapMarkdownRoleArtifact(),
		planAttemptMetaRoleArtifact(),
	},
	ReadOnlyOutsideRoots: true,
}

// roadmapUserInput is the data passed to roadmap.user.tmpl.
type roadmapUserInput struct {
	Name         string
	Description  string
	ExitCriteria string
	Repos        []prompts.RepoView

	// RefactorPassForkPoint names a refactor child's fork-point commits
	// ("repo @ sha"). Empty for top-level features.
	RefactorPassForkPoint string

	DesignArtifactPath   string
	ResearchArtifactPath string

	VisualReferences prompts.VisualReferencesInput
	QAFiles          prompts.QAFilesInput

	MultiRepo bool

	Inquireness prompts.GrillMeInquirenessInput
}
