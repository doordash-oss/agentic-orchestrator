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

// RolePlanRoadmapReviser is the top-level roadmap revision session.
const RolePlanRoadmapReviser Role = "plan_roadmap_reviser"

var roadmapReviserRoleSpec = roleSpec{
	Phase:     feature.PhasePlan,
	Role:      RolePlanRoadmapReviser,
	SkillName: "revise-roadmap",
	OutputRoots: []outputRootSpec{
		artifactDirOutputRoot("Shared roadmap artifact root. Revisions update the roadmap markdown here across attempts."),
		attemptDirOutputRoot("Active roadmap revision attempt directory. Debug prompts, attempt metadata, and validator output are written here; the harness records its completion receipt here after validation."),
	},
	Artifacts: []roleArtifactSpec{
		roadmapMarkdownRoleArtifact(),
		planAttemptMetaRoleArtifact(),
	},
	ReadOnlyOutsideRoots: true,
}

// roadmapRevisionUserInput is the data passed to roadmap_revision.user.tmpl.
type roadmapRevisionUserInput struct {
	Attempt        int
	CriticFeedback string

	PriorAxisApprovals  prompts.PriorAxisApprovalsInput
	PreviousRoadmapPath string

	RoadmapFormatPath string

	Inquireness prompts.AutonomousInquirenessInput
}
