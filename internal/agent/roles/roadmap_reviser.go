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

package roles

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// RolePlanRoadmapReviser is the top-level roadmap revision session.
const RolePlanRoadmapReviser Role = "plan_roadmap_reviser"

var roadmapReviserRoleSpec = RoleSpec{
	Phase:        feature.PhasePlan,
	Role:         RolePlanRoadmapReviser,
	SkillName:    "revise-roadmap",
	UserTemplate: "roadmap_revision.user",
	Required:     []feature.Phase{feature.PhaseBrainstorm},
	OutputRoots: []OutputRootSpec{
		artifactDirOutputRoot("Shared roadmap artifact root. Revisions update the roadmap markdown here across attempts."),
		attemptDirOutputRoot("Active roadmap revision attempt directory. Debug prompts, attempt metadata, validator output, and phase_complete are written here."),
	},
	MarkerRoot: "attempt_dir",
	Artifacts: []RoleArtifactSpec{
		roadmapMarkdownRoleArtifact(),
		planAttemptMetaRoleArtifact(),
	},
}

// RoadmapReviserRoleSpec returns the RoleSpec-backed roadmap revision role.
func RoadmapReviserRoleSpec() RoleSpec {
	return CloneRoleSpec(roadmapReviserRoleSpec)
}

// RoadmapRevisionUserInput is the data passed to roadmap_revision.user.tmpl.
type RoadmapRevisionUserInput struct {
	Attempt        int
	CriticFeedback string

	PriorAxisApprovals  prompts.PriorAxisApprovalsInput
	PreviousRoadmapPath string

	RoadmapFormatPath string

	Inquireness prompts.AutonomousInquirenessInput
}

// BuildRoadmapRevisionPrompt renders the roadmap-revision prompt.
func BuildRoadmapRevisionPrompt(in RoadmapRevisionUserInput) string {
	return prompts.RoadmapRevisionUserPrompt(in)
}
